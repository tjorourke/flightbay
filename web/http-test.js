'use strict';

const HTTP_TEST={profiles:[],profile:{name:'',method:'GET',path:'/',headers:{},body:'',timeout:10},results:[],mode:'service',context:'',port:0,gateway:'',names:[],url:'',busy:false,error:'',count:1,client:'',meshSummary:null};
// 'mesh' runs curl in an existing captured Pod; 'ephemeral' runs it in a
// temporary flightbay-http Pod the server creates and deletes for this test.
function httpInMesh(mode){return mode==='mesh'||mode==='ephemeral';}
const httpSecretHeader=name=>/authorization|cookie|token|secret|key/i.test(name);

async function amOpenHTTPTest(service){
  const T=HTTP_TEST;if(T.busy){toast('An HTTP test is already running');return;}const changed=!T.service||amKey(T.service)!==amKey(service);if(changed){T.results=[];T.meshSummary=null;T.meshWarnings=[];T.meshRoutes=[];T.meshSnapshot=null;T.panel='request';}T.service=service;T.error='';T.names=[];
  T.logReturn=null;
  try{
    [T.options,T.profiles,T.contexts]=await Promise.all([api('api/ambient/http-options',{cluster:service.cluster,namespace:service.namespace,name:service.name}),api('api/ambient/http-profiles'),api('api/ambient/contexts').catch(()=>[])]);
    const svc=S.ambient.inventory?.services.find(s=>amKey(s)===amKey(service));T.ports=T.options.ports||svc?.ports||[];T.port=T.ports[0]||80;T.context=amLiveContext(service.cluster,T.contexts)?.name||'';
    const clients=T.options.clients||[],preferred=clients.findIndex(c=>c.pod.namespace===service.namespace&&c.ambient),index=preferred>=0?preferred:clients.findIndex(c=>c.ambient);T.client=index>=0?String(index):clients.length?'0':'';
    T.names=[T.options.dns?.localName].filter(Boolean);T.gateway='';if(changed){T.mode=!T.contexts.length?'public':clients.length?'mesh':'ephemeral';T.count=1;T.ephemeralNamespace=service.namespace;T.meshCleanup=null;T.countByMode={};T.nativeAttribution=T.mode!=='public';}
    if(!T.profile.name&&T.profile.path==='/')T.profile.path=T.options.defaultPath||'/';drawHTTPTest();
  }catch(e){toast(e.message);}
}
function captureHTTPTest(){
  const T=HTTP_TEST,get=id=>document.querySelector('#http-'+id);if(!get('method'))return;
  T.profile={name:get('profile-name').value.trim(),method:get('method').value,path:get('path').value,timeout:Number(get('timeout').value),body:get('body').value,headers:{}};
  document.querySelectorAll('[data-http-header]').forEach(row=>{const name=row.querySelector('[data-http-name]').value.trim();if(name)T.profile.headers[name]=row.querySelector('[data-http-value]').value;});
  T.mode=get('mode').value;T.context=get('context')?.value??T.context;T.port=Number(get('port').value);T.url=get('url').value;T.gateway=get('gateway').value;T.names=[...document.querySelectorAll('[data-http-dns]:checked')].map(e=>e.value);T.count=Number(get('count')?.value??T.count);T.client=get('client')?.value??T.client;T.ephemeralNamespace=get('ephemeral-ns')?.value??T.ephemeralNamespace;
  if(get('attribute'))T.nativeAttribution=get('attribute').checked;
}
function drawHTTPTest(){
  const T=HTTP_TEST;
  const previous=document.querySelector('.http-test'),sameView=previous?.dataset.httpView===(T.panel||'request'),scrollTop=sameView?document.querySelector('.ht-scroll')?.scrollTop||0:0;
  const open=sameView?new Set([...document.querySelectorAll('.http-test details[open]')].map(d=>d.dataset.httpPreserve||d.className)):new Set();
  modal('Test Service · '+T.service.namespace+'/'+T.service.name,httpTestMarkup());
  document.querySelector('.modal').classList.add('http-test-modal');
  if(sameView)document.querySelectorAll('.http-test details').forEach(d=>{if(open.has(d.dataset.httpPreserve||d.className))d.open=true;});
  if(document.querySelector('.ht-scroll'))document.querySelector('.ht-scroll').scrollTop=scrollTop;
  document.querySelectorAll('[data-http-panel]').forEach(b=>b.onclick=()=>{captureHTTPTest();T.panel=b.dataset.httpPanel;drawHTTPTest();});
  const updateCount=()=>{const count=$('#http-batch-status'),names=T.mode==='public'?1:[...document.querySelectorAll('[data-http-dns]:checked')].length;if(count)count.textContent=T.busy||T.panel==='results'?T.results.length+' / '+T.meshRequested+' completed':(Number($('#http-count').value)*names)+' requests · '+(T.mode==='public'?'Flightbay native client':names+' selected names');};updateCount();document.querySelectorAll('[data-http-dns]').forEach(e=>e.onchange=updateCount);if($('#http-count'))$('#http-count').oninput=updateCount;
  if(T.busy)document.querySelectorAll('.http-test input,.http-test select,.http-test textarea,.http-test button:not(#http-stop):not([data-http-stop]):not([data-http-panel])').forEach(e=>e.disabled=true);
  if($('#http-stop'))$('#http-stop').onclick=()=>{T.abortController?.abort();};
  document.querySelectorAll('[data-http-stop]').forEach(b=>b.onclick=()=>T.abortController?.abort());
  $('#http-mode').onchange=()=>{const oldMode=T.mode,oldCount=T.count;captureHTTPTest();T.countByMode??={};T.countByMode[oldMode]=oldCount;T.count=T.countByMode[T.mode]??1;T.nativeAttribution=T.mode!=='public';T.results=[];T.meshSummary=null;drawHTTPTest();};
  if($('#http-ephemeral-ns'))$('#http-ephemeral-ns').onchange=()=>{captureHTTPTest();drawHTTPTest();};
  if($('#http-attribute'))$('#http-attribute').onchange=()=>{captureHTTPTest();drawHTTPTest();};
  $('#http-profile').onchange=e=>{if(e.target.value===''){T.profile={name:'',method:'GET',path:'/',headers:{},body:'',timeout:10};}else{T.profile=JSON.parse(JSON.stringify(T.profiles[Number(e.target.value)]));}drawHTTPTest();};
  $('#http-add-header').onclick=()=>{captureHTTPTest();const row=document.createElement('div');row.className='http-header-row';row.dataset.httpHeader='';row.innerHTML='<input data-http-name aria-label="Header name" placeholder="Header name"><input data-http-value aria-label="Header value" placeholder="Value"><button>Remove</button>';$('#http-headers').appendChild(row);row.querySelector('button').onclick=()=>row.remove();row.querySelector('[data-http-name]').onchange=e=>row.querySelector('[data-http-value]').type=httpSecretHeader(e.target.value)?'password':'text';row.querySelector('input').focus();};
  document.querySelectorAll('[data-http-remove]').forEach(b=>b.onclick=()=>b.closest('[data-http-header]').remove());
  document.querySelectorAll('[data-http-name]').forEach(e=>e.onchange=()=>e.closest('[data-http-header]').querySelector('[data-http-value]').type=httpSecretHeader(e.value)?'password':'text');
  $('#http-save-profile').onclick=async()=>{captureHTTPTest();try{T.profiles=await apiPost('api/ambient/http-profiles',T.profile);T.error='';toast('Profile saved locally');}catch(e){T.error=e.message;}drawHTTPTest();};
  $('#http-run').onclick=async()=>{captureHTTPTest();if(T.busy)return;T.busy=true;T.panel='results';T.error='';T.results=[];T.meshSummary=null;T.meshCleanup=null;T.meshEphemeral=null;T.meshRequested=T.count*(T.mode==='public'?1:T.names.length);T.meshSnapshot={count:T.count,names:[...T.names],method:T.profile.method,path:T.profile.path};drawHTTPTest();try{if(httpInMesh(T.mode)){await httpRunMeshTest();}else{await httpRunNativeTest();}}catch(e){T.error=e.name==='AbortError'?'Test stopped. Completed responses are retained.':e.message;T.meshSummary={stopped:e.name==='AbortError',failed:e.name!=='AbortError',unattempted:Math.max(0,T.meshRequested-T.results.length),remainingUnconfirmed:true};}finally{T.busy=false;T.abortController=null;if(document.querySelector('.http-test'))drawHTTPTest();else toast('HTTP test finished. Reopen Test service to see the result.');}};
  document.querySelectorAll('[data-http-logs]').forEach(b=>b.onclick=()=>httpTestOpenLogs(Number(b.dataset.httpLogs)));
}

function httpTestOpenLogs(index){
  captureHTTPTest();const T=HTTP_TEST,r=T.results[index];if(!r)return;
  T.logReturn={hash:location.hash||'#ambient',cluster:S.cluster,x:scrollX,y:scrollY,mainScrollTop:$('#main')?.scrollTop||0,modalScrollTop:(document.querySelector('.ht-scroll')||document.querySelector('.http-test-modal'))?.scrollTop||0,openDetails:[...document.querySelectorAll('.http-test details')].map(e=>e.open),index,ambient:S.view==='ambient'?Object.fromEntries(['key','kind','tab','phase','clusterScope','pickerOpen'].map(k=>[k,S.ambient[k]])):null,awaitingReturn:true};
  const ref=r.responder?.pod||r.pod||T.service;S.cluster=ref.cluster;$('#cluster').value=ref.cluster;
  openLogs({namespace:ref.namespace,pod:ref.kind==='Pod'?ref.name:'',from:new Date(new Date(r.started).getTime()-2000).toISOString(),to:new Date(new Date(r.finished).getTime()+2000).toISOString()});
}

function httpTestReturnAction(){
  const T=HTTP_TEST;if(!T.logReturn||!T.results.length)return '';
  return `<button id="http-back-to-test" class="primary" title="Return to the Service test with the profile and results retained">← Back to HTTP test</button>`;
}

// Called after hash parsing, before rendering: URL parameters must not replace
// the active map/wizard state that was present when the logs were opened.
function httpTestNavigation(){
  const T=HTTP_TEST,back=T.logReturn;if(!back)return;
  if(S.view==='logs'){back.awaitingReturn=true;return;}
  if(!back.awaitingReturn||location.hash!==back.hash)return;
  S.cluster=back.cluster;$('#cluster').value=back.cluster;
  if(back.ambient)Object.assign(S.ambient,back.ambient);
  T.reopenAfterRender=true;back.awaitingReturn=false;
}

function httpTestNavigationRendered(){
  const T=HTTP_TEST,back=T.logReturn;
  const button=$('#http-back-to-test');if(button)button.onclick=()=>{location.hash=back.hash;};
  if(!T.reopenAfterRender||!back||location.hash!==back.hash)return;
  T.reopenAfterRender=false;drawHTTPTest();
  document.querySelectorAll('.http-test details').forEach((e,i)=>e.open=!!back.openDetails[i]);
  requestAnimationFrame(()=>{if(!document.querySelector('.http-test'))return;scrollTo(back.x,back.y);const main=$('#main');if(main)main.scrollTop=back.mainScrollTop;const modal=document.querySelector('.ht-scroll')||document.querySelector('.http-test-modal');if(modal)modal.scrollTop=back.modalScrollTop;document.querySelector(`[data-http-logs="${back.index}"]`)?.focus({preventScroll:true});});
}
