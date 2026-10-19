const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const path=require('node:path');
const load=(ctx,file)=>vm.runInContext(fs.readFileSync(path.join(__dirname,'../web',file),'utf8'),ctx);
function setup(extra={}){const ctx=vm.createContext({S:{ambient:{}},$:()=>null,esc:x=>String(x??''),AbortController,TextDecoder,...extra});vm.runInContext(require('./context-helpers.cjs'),ctx);load(ctx,'http-test.js');load(ctx,'http-mesh-test.js');return ctx;}
const result=(index,version,status=200)=>({index,mode:'mesh',name:'reviews.bookinfo.svc.cluster.local',status,body:'{}',responder:version?{uid:version,pod:{cluster:'mesh1',namespace:'bookinfo',name:'reviews-'+version},version,service:'bookinfo/reviews-'+version}:null});

test('traffic graph includes failed and unidentified requests and keeps points in bounds',()=>{
  const ctx=setup(),rows=Array.from({length:100},(_,i)=>result(i+1,i<90?'v1':'v2'));rows.push(result(101,null,500),{...result(102,null,0),error:'timeout'});
  const groups=ctx.httpMeshGroups(rows);assert.equal(groups.reduce((n,g)=>n+g.count,0),102);assert.equal(groups.find(g=>g.unknown).count,2);assert.equal(groups.find(g=>g.version==='v1').count,90);
  for(const group of groups){const points=ctx.httpSharePoints(rows,group.key,110).split(' ').map(p=>p.split(',').map(Number));assert.ok(points.every(([x,y])=>x>=54&&x<=704&&y>=40&&y<=210));}
  vm.runInContext('HTTP_TEST.results=[];HTTP_TEST.count=110;HTTP_TEST.names=["reviews.bookinfo.svc.cluster.local"];HTTP_TEST.profile.path="/reviews/0";',ctx);ctx.rows=rows;vm.runInContext('HTTP_TEST.results=rows;',ctx);
  const html=ctx.httpMeshDashboard();assert.match(html,/Observed traffic share/);assert.match(html,/Unidentified/);assert.match(html,/Response distribution/);assert.match(html,/88\.2%/,'observed percentage must use all 102 completed requests, not only 100 successful responses');
});

test('identical service/version names remain separate by responder cluster',()=>{
  const ctx=setup(),local=result(1,'v1'),remote=result(2,'v1'),unknown=result(3,null,500);remote.responder.pod.cluster='mesh2';
  const rows=[local,remote,unknown],groups=ctx.httpMeshGroups(rows),clusters=ctx.httpMeshClusterGroups(rows);
  assert.equal(groups.length,3);assert.notEqual(ctx.httpResponderKey(local),ctx.httpResponderKey(remote));
  assert.equal(clusters.find(c=>c.cluster==='mesh1').count,1);assert.equal(clusters.find(c=>c.cluster==='mesh2').count,1);assert.equal(clusters.find(c=>c.cluster==='').count,1);
  ctx.rows=rows;vm.runInContext('HTTP_TEST.service={cluster:"mesh1"};HTTP_TEST.count=3;HTTP_TEST.names=["reviews.bookinfo.svc.cluster.local"];HTTP_TEST.results=rows;',ctx);
  const html=ctx.httpMeshDashboard();assert.match(html,/Responding clusters/);assert.match(html,/mesh1 · v1/);assert.match(html,/mesh2 · v1/);assert.match(html,/remote from source/);assert.match(html,/Cluster unverified/);assert.match(html,/33\.3%/);
  assert.match(ctx.httpMeshSamples(rows),/Responder cluster/);assert.match(ctx.httpMeshSamples(rows),/Client logs · responder unverified/);
});

test('gateway failures without responder identities do not draw a 100% backend share',()=>{
  const ctx=setup(),rows=Array.from({length:10},(_,i)=>result(i+1,null,503));
  const graph=ctx.httpMeshGraph(rows,ctx.httpMeshGroups(rows),10);
  assert.match(graph,/10 failed request results/);assert.match(graph,/No identified backend-response distribution/);assert.doesNotMatch(graph,/<svg|<polyline/);
});

test('stopping a successful remote run keeps origin and responding cluster distinct',()=>{
  const ctx=setup(),rows=Array.from({length:12},(_,i)=>{const r=result(i+1,'v2');r.responder.pod.cluster='mesh2';r.authority='reviews.bookinfo.mesh.internal:9080';return r;});ctx.rows=rows;
  vm.runInContext('HTTP_TEST.mode="mesh";HTTP_TEST.service={cluster:"mesh1"};HTTP_TEST.meshClient={pod:{cluster:"mesh1",namespace:"bookinfo",name:"existing-client"}};HTTP_TEST.meshSummary={stopped:true};',ctx);
  const html=ctx.httpTestDirection(rows);assert.match(html,/Request origin/);assert.match(html,/mesh1/);assert.match(html,/Existing application responders/);assert.match(html,/mesh2/);assert.match(html,/12 successful HTTP responses/);assert.match(html,/run stopped/);assert.match(html,/HTTP authority sent/);assert.match(html,/mesh\.internal:9080/);
});

test('mesh batch streams partial progress and a final distribution without backend port-forward',async()=>{
  const events=[{type:'start',requested:3,routes:[],warnings:['Multiple routes'],client:{pod:{namespace:'bookinfo',name:'client'}}},...Array.from({length:3},(_,i)=>({type:'result',result:result(i+1,i<2?'v1':'v2')})),{type:'complete',summary:{requested:3,completed:3,successful:3,failed:0,unknown:0,unattempted:0}}];
  const bytes=new TextEncoder().encode(events.map(e=>JSON.stringify(e)).join('\n')+'\n'),chunks=[bytes.slice(0,17),bytes.slice(17,91),bytes.slice(91)];let paints=0,sent;
  const ctx=setup({fetch:async(url,options)=>{sent={url,body:JSON.parse(options.body)};return {ok:true,body:{getReader:()=>({read:async()=>chunks.length?{value:chunks.shift(),done:false}:{done:true},releaseLock(){}})}};}});
  vm.runInContext('httpPaintMesh=()=>{};HTTP_TEST.mode="mesh";HTTP_TEST.service={cluster:"mesh1",namespace:"bookinfo",name:"reviews"};HTTP_TEST.context="mesh1";HTTP_TEST.options={clients:[{pod:{cluster:"mesh1",namespace:"bookinfo",name:"client"},container:"curl"}]};HTTP_TEST.client="0";HTTP_TEST.count=3;HTTP_TEST.names=["reviews.bookinfo.svc.cluster.local"];HTTP_TEST.port=9080;',ctx);ctx.httpPaintMesh=()=>paints++;
  await ctx.httpRunMeshTest();assert.equal(sent.url,'api/ambient/http-mesh-test');assert.equal(sent.body.count,3);assert.equal(sent.body.client.name,'client');assert.equal(vm.runInContext('HTTP_TEST.results.length',ctx),3);assert.equal(vm.runInContext('HTTP_TEST.meshSummary.completed',ctx),3);assert.ok(paints>=5);assert.match(vm.runInContext('HTTP_TEST.meshWarnings[0]',ctx),/Multiple routes/);
});

test('premature stream closure retains observed responses rather than inventing completion',async()=>{
  const bytes=new TextEncoder().encode(JSON.stringify({type:'result',result:result(1,'v1')})+'\n');let delivered=false;
  const ctx=setup({fetch:async()=>({ok:true,body:{getReader:()=>({read:async()=>delivered?{done:true}:(delivered=true,{done:false,value:bytes}),releaseLock(){}})}})});
  vm.runInContext('httpPaintMesh=()=>{};HTTP_TEST.options={clients:[{pod:{namespace:"bookinfo",name:"client"},container:"curl"}]};HTTP_TEST.client="0";HTTP_TEST.count=3;HTTP_TEST.names=["reviews"];',ctx);
  await assert.rejects(ctx.httpRunMeshTest(),/before completion/);assert.equal(vm.runInContext('HTTP_TEST.results.length',ctx),1);assert.equal(vm.runInContext('HTTP_TEST.meshSummary',ctx),null);
});

test('native Flightbay batch uses the HTTP endpoint with no Pod/client deployment requirement',async()=>{
  let sent;const events=[{type:'start',origin:'flightbay',requested:2,names:['app.example'],connection:'https://app.example',connectionNote:'Native HTTP; no workloads deployed',routes:[],warnings:[],attributionClusters:[]},{type:'result',result:{index:1,name:'app.example',mode:'public',status:200}},{type:'result',result:{index:2,name:'app.example',mode:'public',status:200}},{type:'complete',summary:{completed:2,successful:2,unknown:2}}];
  const bytes=new TextEncoder().encode(events.map(e=>JSON.stringify(e)).join('\n')+'\n');let delivered=false;
  const ctx=setup({fetch:async(url,options)=>{sent={url,body:JSON.parse(options.body),signal:options.signal};return {ok:true,body:{getReader:()=>({read:async()=>delivered?{done:true}:(delivered=true,{value:bytes,done:false}),releaseLock(){}})}};}});
  vm.runInContext('httpPaintMesh=()=>{};HTTP_TEST.mode="public";HTTP_TEST.url="https://app.example";HTTP_TEST.service={cluster:"mesh1"};HTTP_TEST.context="";HTTP_TEST.count=2;HTTP_TEST.names=[];HTTP_TEST.options={gateways:[]};HTTP_TEST.gateway="";HTTP_TEST.meshSnapshot={names:[],count:2,method:"GET",path:"/"};',ctx);
  await ctx.httpRunNativeTest();assert.equal(sent.url,'api/ambient/http-test');assert.equal(sent.body.mode,'public');assert.equal(sent.body.count,2);assert.equal(sent.body.client,undefined);assert.ok(sent.signal);assert.equal(vm.runInContext('HTTP_TEST.results.length',ctx),2);
  const html=ctx.httpMeshDashboard();assert.match(html,/Flightbay native HTTP client/);assert.match(html,/app\.example/);assert.match(html,/Cluster unverified/);assert.equal(vm.runInContext('HTTP_TEST.meshClient',ctx),null);
});

test('selecting Bookinfo Reviews offers the existing mesh client, correct port and real endpoint',async()=>{
  const ctx=setup({S:{ambient:{inventory:{services:[]}}},amKey:JSON.stringify,toast:message=>assert.fail(message),api:async url=>url.endsWith('contexts')?[{name:'original-mesh1',liveCluster:'mesh1',server:'https://mesh1'}]:url.endsWith('profiles')?[]:{ports:[9080],defaultPath:'/reviews/0',dns:{localName:'reviews.bookinfo.svc.cluster.local'},clients:[{pod:{cluster:'mesh1',namespace:'bookinfo',name:'demo-client'},container:'curl',ambient:true}]}});
  vm.runInContext('drawHTTPTest=()=>{};',ctx);await ctx.amOpenHTTPTest({cluster:'mesh1',namespace:'bookinfo',name:'reviews'});
  assert.equal(vm.runInContext('HTTP_TEST.mode',ctx),'mesh');assert.equal(vm.runInContext('HTTP_TEST.profile.path',ctx),'/reviews/0');assert.equal(vm.runInContext('HTTP_TEST.port',ctx),9080);assert.equal(vm.runInContext('HTTP_TEST.context',ctx),'original-mesh1');assert.equal(vm.runInContext('HTTP_TEST.results.length',ctx),0,'opening the dialog must not generate traffic');
});

test('a Service without an existing curl client opens native mode with one request',async()=>{
  const ctx=setup({S:{ambient:{inventory:{services:[]}}},amKey:JSON.stringify,toast:message=>assert.fail(message),api:async url=>url.endsWith('contexts')?[]:url.endsWith('profiles')?[]:{ports:[8080],dns:{localName:'checkout.payments.svc.cluster.local'},clients:[],gateways:[]}});
  vm.runInContext('drawHTTPTest=()=>{};',ctx);await ctx.amOpenHTTPTest({cluster:'customer-west',namespace:'payments',name:'checkout'});
  assert.equal(vm.runInContext('HTTP_TEST.mode',ctx),'public');assert.equal(vm.runInContext('HTTP_TEST.count',ctx),1);assert.equal(vm.runInContext('HTTP_TEST.client',ctx),'');
});

test('native request setup remains available when no kubeconfig contexts can be read',async()=>{
  const ctx=setup({S:{ambient:{inventory:{services:[]}}},amKey:JSON.stringify,toast:message=>assert.fail(message),api:async url=>{if(url.endsWith('contexts'))throw new Error('no kubeconfig');return url.endsWith('profiles')?[]:{ports:[8080],dns:{localName:'app.example'},clients:[],gateways:[]};}});
  vm.runInContext('drawHTTPTest=()=>{};',ctx);await ctx.amOpenHTTPTest({cluster:'captured',namespace:'payments',name:'checkout'});
  assert.equal(vm.runInContext('HTTP_TEST.mode',ctx),'public');assert.equal(vm.runInContext('HTTP_TEST.contexts.length',ctx),0);assert.equal(vm.runInContext('HTTP_TEST.nativeAttribution',ctx),false);
});

test('a Service with a live connection but no curl client defaults to a temporary Pod in its namespace',async()=>{
  const ctx=setup({S:{ambient:{inventory:{services:[]}}},amKey:JSON.stringify,toast:message=>assert.fail(message),api:async url=>url.endsWith('contexts')?[{name:'kind-mesh1',liveCluster:'mesh1',server:'https://mesh1'}]:url.endsWith('profiles')?[]:{ports:[8080],dns:{localName:'web.shop.svc.cluster.local'},clients:[],gateways:[]}});
  vm.runInContext('drawHTTPTest=()=>{};',ctx);await ctx.amOpenHTTPTest({cluster:'mesh1',namespace:'shop',name:'web'});
  assert.equal(vm.runInContext('HTTP_TEST.mode',ctx),'ephemeral');assert.equal(vm.runInContext('HTTP_TEST.count',ctx),200);assert.equal(vm.runInContext('HTTP_TEST.ephemeralNamespace',ctx),'shop');
});

test('temporary Pod option explains the lifecycle and flags a namespace outside the mesh',()=>{
  const ctx=setup({S:{ambient:{inventory:{namespaces:[{cluster:'mesh1',name:'shop'},{cluster:'mesh1',name:'tools'},{cluster:'mesh2',name:'other'}],enrolledNamespaces:[{cluster:'mesh1',name:'shop'}]}}}});
  vm.runInContext('HTTP_TEST.mode="ephemeral";HTTP_TEST.service={cluster:"mesh1",namespace:"shop",name:"web"};HTTP_TEST.contexts=[{name:"kind-mesh1",liveCluster:"mesh1",server:"https://mesh1"}];HTTP_TEST.context="kind-mesh1";HTTP_TEST.options={dns:{localName:"web.shop.svc.cluster.local"},clients:[],gateways:[]};HTTP_TEST.ports=[8080];HTTP_TEST.port=8080;HTTP_TEST.ephemeralNamespace="shop";',ctx);
  let html=ctx.httpTestMarkup();
  assert.match(html,/<option value="ephemeral" selected >Temporary Pod · flightbay-http \(created now, deleted after\)/);assert.match(html,/<option value="mesh"[^>]*disabled>Existing Pod · in-mesh curl/);
  assert.match(html,/id="http-ephemeral-ns"><option value="shop" selected>shop<\/option><option value="tools" >tools<\/option><\/select>/);assert.doesNotMatch(html,/value="other"/);
  assert.match(html,/creates Pod flightbay-http-xxxxxx in shop using curlimages\/curl:8\.12\.1/);assert.match(html,/expires by itself after 15 minutes/);assert.doesNotMatch(html,/not labelled istio\.io\/dataplane-mode=ambient/);
  vm.runInContext('HTTP_TEST.ephemeralNamespace="tools";',ctx);html=ctx.httpTestMarkup();assert.match(html,/Namespace tools is not labelled istio\.io\/dataplane-mode=ambient/);
});

test('temporary Pod run sends the namespace instead of a client and reports the deletion',async()=>{
  const events=[{type:'start',requested:1,routes:[],warnings:['Temporary Pod shop/flightbay-http-abc123 was created for this test and is deleted when it ends.'],client:{pod:{cluster:'mesh1',namespace:'shop',name:'flightbay-http-abc123'},container:'curl'},ephemeral:{namespace:'shop',name:'flightbay-http-abc123'}},{type:'result',result:result(1,'v1')},{type:'complete',summary:{requested:1,completed:1,successful:1,failed:0,unknown:0,unattempted:0}},{type:'cleanup',pod:'flightbay-http-abc123',namespace:'shop',deleted:true}];
  const bytes=new TextEncoder().encode(events.map(e=>JSON.stringify(e)).join('\n')+'\n');let delivered=false,sent;
  const ctx=setup({fetch:async(url,options)=>{sent=JSON.parse(options.body);return {ok:true,body:{getReader:()=>({read:async()=>delivered?{done:true}:(delivered=true,{value:bytes,done:false}),releaseLock(){}})}};}});
  vm.runInContext('httpPaintMesh=()=>{};HTTP_TEST.mode="ephemeral";HTTP_TEST.service={cluster:"mesh1",namespace:"shop",name:"web"};HTTP_TEST.context="kind-mesh1";HTTP_TEST.options={clients:[]};HTTP_TEST.ephemeralNamespace="shop";HTTP_TEST.count=1;HTTP_TEST.names=["web.shop.svc.cluster.local"];HTTP_TEST.port=8080;',ctx);
  await ctx.httpRunMeshTest();
  assert.deepEqual(sent.ephemeral,{namespace:'shop'});assert.equal(sent.client,undefined);assert.equal(sent.container,undefined);
  assert.equal(vm.runInContext('HTTP_TEST.meshClient.pod.name',ctx),'flightbay-http-abc123');assert.equal(vm.runInContext('HTTP_TEST.meshCleanup.deleted',ctx),true);
  let html=ctx.httpMeshDashboard();assert.match(html,/hm-ephemeral-status green[^>]*>Temporary Pod <code>shop\/flightbay-http-abc123<\/code> deleted/);assert.match(html,/temporary flightbay-http Pod|deleted when it ends/);
  vm.runInContext('HTTP_TEST.meshCleanup={pod:"flightbay-http-abc123",namespace:"shop",deleted:false,error:"forbidden"};',ctx);html=ctx.httpMeshDashboard();assert.match(html,/hm-ephemeral-status red/);assert.match(html,/forbidden/);assert.match(html,/kubectl delete pod -n shop flightbay-http-abc123/);
});
