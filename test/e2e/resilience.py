"""Live resilience checks against separately installed test ambient/RLS fixtures.

Start a fresh live desk including the fixture namespace and istio-system, set
FLIGHTBAY_TEST_URL and FLIGHTBAY_TEST_TOKEN, then run this script. This test
never asks Flightbay to provision Redis or a rate-limit service.
"""
import collections
import concurrent.futures
import json
import os
import subprocess
import time
import urllib.error
import urllib.request

BASE = os.environ.get('FLIGHTBAY_TEST_URL', 'http://127.0.0.1:18530').rstrip('/')+'/'
TOKEN = os.environ['FLIGHTBAY_TEST_TOKEN']
NS = 'flightbay-resilience-check'

def api(path, data=None):
    request = urllib.request.Request(BASE + path, data=None if data is None else json.dumps(data).encode(), headers={'Authorization': 'Bearer ' + TOKEN, 'Content-Type': 'application/json'})
    try:
        with urllib.request.urlopen(request, timeout=60) as response:
            return json.load(response)
    except urllib.error.HTTPError as error:
        raise RuntimeError(error.read().decode()) from error

def request(context, path, service='api'):
    cmd = ['kubectl', '--context', context, '-n', NS, 'exec', 'client', '--', 'curl', '-sS', '--max-time', '8', '-w', '\n%{http_code}', 'http://' + service + ':9080' + path]
    data = subprocess.check_output(cmd, text=True)
    body, status = data.rsplit('\n', 1)
    return int(status), body

def wait_for_live():
    for _ in range(180):
        try:
            workspace=api('api/workspace')
            clusters=workspace.get('live',{}).get('clusters',[])
            if len(clusters)==2 and all(c.get('phase')=='live' for c in clusters):return
        except Exception:pass
        time.sleep(1)
    raise RuntimeError('both fixture cluster inventories did not finish initial sync')

def apply(context, task, resilience, **settings):
    service = settings.pop('service', 'api')
    q = dict(cluster=context, namespace=NS, service=service, port=9080, tasks=[task] if isinstance(task,str) else task, resilience=resilience, **settings)
    plan = api('api/ambient/plan', q)
    validation = api('api/ambient/test', dict(operation='validate', context=context, request=plan['request'], planId=plan['id']))
    result = api('api/ambient/test', dict(operation='apply', context=context, validation=validation['token']))
    record = result['test']['token']
    time.sleep(3)
    return record

def undo(context, record):
    api('api/ambient/test', dict(operation='cleanup', context=context, validation=record))
    time.sleep(2)

def run(context):
    records = []
    try:
        records.append(apply(context, 'rate-limit', dict(scope='api', match=dict(pathType='Exact', path='/limited', method='GET')), requests=2, unit='Minutes', burst=2))
        limited = [request(context, '/limited')[0] for _ in range(5)]
        healthy = request(context, '/ok')[0]
        print(context, 'local API limit', limited, 'unrelated API', healthy, flush=True)
        assert limited[:2] == [200, 200] and limited[2:] == [429, 429, 429] and healthy == 200
        undo(context, records.pop())
        records.append(apply(context, 'retries', dict(scope='api', match=dict(pathType='Exact', path='/retry', method='GET'), retryOn='5xx'), retryAttempts=2, timeout='5s', backendTimeout='1s'))
        status, body = request(context, '/retry?run='+str(time.time_ns()))
        print(context, 'retry result', status, body, flush=True)
        assert status == 200 and json.loads(body)['attempt'] == 3
        undo(context, records.pop())
        records.append(apply(context, 'circuit-breaker', dict(scope='api', match=dict(pathType='PathPrefix', path='/slow', method='GET')), circuitBreaker=dict(maxConnections=1, maxPendingRequests=1, maxRequests=1, maxRetries=1, maxRequestsPerConnection=0, connectTimeout='1s')))
        with concurrent.futures.ThreadPoolExecutor(max_workers=12) as pool:
            futures = [pool.submit(request, context, '/slow?i=' + str(i)) for i in range(10)]
            healthy = pool.submit(request, context, '/ok').result()[0]
            statuses = [future.result()[0] for future in futures]
        print(context, 'API circuit overflow', dict(collections.Counter(statuses)), 'unrelated API', healthy, flush=True)
        assert 503 in statuses and 200 in statuses and healthy == 200
        undo(context, records.pop())
        for path, threshold, percentage in [('/fail-consecutive',1,0),('/fail-percentage',100000,50)]:
            records.append(apply(context,'endpoint-failover',dict(scope='api',match=dict(pathType='Exact',path=path,method='GET'),failurePercentage=percentage),failureThreshold=threshold,ejectionInterval='10s' if percentage else '1s',ejectionDuration='30s',maxEjectionPercent=100))
            if percentage:
                command='for i in $(seq 1 100); do curl -s --max-time 4 -o /dev/null http://api:9080'+path+' & done; wait'
                subprocess.run(['kubectl','--context',context,'-n',NS,'exec','client','--','sh','-c',command],check=True)
            else:
                request(context,path)
            time.sleep(12 if percentage else 2)
            status,body=request(context,path)
            healthy=request(context,'/ok')[0]
            print(context,path,status,body,'unrelated API',healthy,flush=True)
            assert status==503 and 'no healthy upstream' in body and healthy==200
            undo(context,records.pop())
    finally:
        for record in reversed(records):
            try:
                undo(context, record)
            except Exception as error:
                print('CLEANUP FAILURE', context, record, error, flush=True)

def global_limit(mesh=False):
    wait_for_live()
    context='kind-mesh1'
    namespace=json.loads(subprocess.check_output(['kubectl','--context',context,'get','namespace',NS,'-o','json'],text=True))
    assert namespace['metadata']['labels'].get('flightbay.dev/purpose')=='resilience-verification', 'only reset the dedicated test fixture Redis'
    subprocess.run(['kubectl','--context',context,'-n',NS,'exec','deploy/redis','--','sh','-c','REDISCLI_AUTH="$REDIS_PASSWORD" redis-cli FLUSHDB'],check=True,stdout=subprocess.DEVNULL)
    records=[]
    try:
        targets=[('kind-mesh1','api'),('kind-mesh2','api')] if mesh else [('kind-mesh1','api'),('kind-mesh1','api2')]
        for context,service in targets:
            settings=dict(scope='api',match=dict(pathType='Exact',path='/shared',method='GET'))
            settings['global']=dict(namespace=NS,service='rls',port=8081,domain='flightbay-check',descriptorKey='api',descriptorValue='mesh-shared-api' if mesh else 'shared-api',failClosed=True,mesh=mesh,meshDomain='mesh.internal')
            records.append((context,apply(context,'global-rate-limit',settings,service=service)))
        calls=[targets[0],targets[1],targets[0],targets[1],targets[0]]
        if mesh:calls[1]=(targets[1][0],'api.'+NS+'.mesh.internal')
        statuses=[request(c,'/shared',service=s)[0] for c,s in calls]
        healthy=[request(c,'/ok',service=s)[0] for c,s in targets]
        print('shared quota across '+('two meshes' if mesh else 'two waypoints'),statuses,'unrelated APIs',healthy,flush=True)
        assert statuses[:3]==[200,200,200] and statuses[3:]==[429,429] and healthy==[200,200]
    finally:
        for context,record in reversed(records):undo(context,record)

def composed_controls(context,sequential=False):
    records=[]
    path='/retry-sequential' if sequential else '/retry-combined'
    scope=dict(scope='api',match=dict(pathType='Exact',path=path,method='GET'),retryOn='5xx')
    settings=dict(requests=2,unit='Minutes',burst=2,retryAttempts=2,timeout='5s',backendTimeout='1s',failureThreshold=10,circuitBreaker=dict(maxConnections=10,maxPendingRequests=10,maxRequests=10,maxRetries=3,maxRequestsPerConnection=0,connectTimeout='1s'))
    try:
        tasks=['rate-limit','retries','circuit-breaker']
        for selected in tasks if sequential else [tasks]:records.append(apply(context,selected,scope,**settings))
        codes=[]
        for _ in range(3):
            status,body=request(context,path+'?run='+str(time.time_ns()));codes.append(status)
            if status==200:assert json.loads(body)['attempt']==3
        print(context,'sequential' if sequential else 'combined','rate limit + retry + circuit',codes,flush=True)
        assert codes==[200,200,429] and request(context,'/ok')[0]==200
        if sequential:
            try:undo(context,records[0])
            except RuntimeError as error:assert 'undo the later change' in str(error)
            else:raise AssertionError('an earlier undo deleted controls still used by later changes')
    finally:
        for record in reversed(records):undo(context,record)

if __name__=='__main__':
    wait_for_live()
    for _ in range(90):
        try:
            view=api('api/ambient/capabilities?cluster=kind-mesh1&namespace='+NS+'&service=api')
            if any(task['id']=='rate-limit' and task['supported'] for task in view):break
        except Exception:pass
        time.sleep(1)
    else:raise RuntimeError('resilience capabilities did not become available')
    for context in ['kind-mesh1','kind-mesh2']:
        run(context)
        composed_controls(context)
        composed_controls(context,sequential=True)
    global_limit()
    global_limit(mesh=True)
