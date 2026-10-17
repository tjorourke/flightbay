'use strict';

const HTTP_TEST={profiles:[],profile:{name:'',method:'GET',path:'/',headers:{},body:'',timeout:10},results:[],mode:'service',context:'',port:0,gateway:'',names:[],url:'',busy:false,error:''};
const httpSecretHeader=name=>/authorization|cookie|token|secret|key/i.test(name);

async function amOpenHTTPTest(service){
  const T=HTTP_TEST;if(T.busy){toast('An HTTP test is already running');return;}if(!T.service||amKey(T.service)!==amKey(service))T.results=[];T.service=service;T.error='';T.names=[];
  T.logReturn=null;
  try{
    [T.options,T.profiles,T.contexts]=await Promise.all([api('api/ambient/http-options',{cluster:service.cluster,namespace:service.namespace,name:service.name}),api('api/ambient/http-profiles'),api('api/ambient/contexts')]);
    const svc=S.ambient.inventory?.services.find(s=>amKey(s)===amKey(service));T.ports=svc?.ports||[];T.port=T.ports[0]||80;T.context=T.contexts.find(c=>c.cluster===service.cluster||c.name===service.cluster)?.name||'';
    T.names=[T.options.dns?.localName,...(T.options.dns?.globalNames||[])].filter(Boolean);T.gateway='';T.mode='service';drawHTTPTest();
  }catch(e){toast(e.message);}
}
function captureHTTPTest(){
  const T=HTTP_TEST,get=id=>document.querySelector('#http-'+id);if(!get('method'))return;
  T.profile={name:get('profile-name').value.trim(),method:get('method').value,path:get('path').value,timeout:Number(get('timeout').value),body:get('body').value,headers:{}};
  document.querySelectorAll('[data-http-header]').forEach(row=>{const name=row.querySelector('[data-http-name]').value.trim();if(name)T.profile.headers[name]=row.querySelector('[data-http-value]').value;});
  T.mode=get('mode').value;T.context=get('context').value;T.port=Number(get('port').value);T.url=get('url').value;T.gateway=get('gateway').value;T.names=[...document.querySelectorAll('[data-http-dns]:checked')].map(e=>e.value);
}
function drawHTTPTest(){
  const T=HTTP_TEST,P=T.profile,dns=T.options.dns,names=[dns.localName,...(dns.globalNames||[])].filter(Boolean);
  const note=T.mode==='public'?'Flightbay connects directly to this address. Cluster DNS must be resolvable and reachable from this machine.':T.mode==='service'?'Port-forward to an existing backend Pod. Selected DNS names become HTTP authorities; this bypasses DNS resolution and the normal mesh/waypoint/east-west path.':'Port-forward to an existing HTTP ingress gateway. Its configured route must target the global Service to exercise east-west routing. DNS resolution is bypassed; no route or Pod is created.';
  modal('Test Service · '+T.service.namespace+'/'+T.service.name,`<div class="http-test"><p><strong>${esc(T.service.cluster)}</strong> · Requests originate from this Flightbay host. No test Pods are deployed.</p><div class="http-profile-bar"><label>Saved header profile<select id="http-profile"><option value="">Unsaved / new profile</option>${T.profiles.map((p,i)=>`<option value="${i}" ${p.name===P.name?'selected':''}>${esc(p.name)}</option>`).join('')}</select></label><label>Profile name<input id="http-profile-name" value="${esc(P.name)}" placeholder="e.g. customer-user"></label><button id="http-save-profile">Save profile</button></div><div class="http-fields"><label>Connection<select id="http-mode">${amOpts([{value:'public',label:'Public / reachable address'},{value:'service',label:'Service port-forward · backend test'},{value:'gateway',label:'Gateway port-forward · route test'}],T.mode)}</select></label><label ${T.mode==='public'?'hidden':''}>Real kubeconfig context<select id="http-context"><option value="">Choose the matching context…</option>${amOpts(T.contexts.map(c=>c.name),T.context)}</select></label><label ${T.mode==='public'?'':'hidden'}>Public origin URL<input id="http-url" value="${esc(T.url)}" placeholder="https://app.example.com"></label><label ${T.mode==='service'?'':'hidden'}>Service port<select id="http-port">${amOpts(T.ports,T.port)}</select></label><label ${T.mode==='gateway'?'':'hidden'}>HTTP ingress gateway<select id="http-gateway"><option value="">Choose a captured gateway…</option>${T.options.gateways.map((g,i)=>`<option value="${i}" ${String(i)===T.gateway?'selected':''}>${esc(g.label)}</option>`).join('')}</select></label><label>Method<select id="http-method">${amOpts(['GET','HEAD','POST','PUT','PATCH','DELETE','OPTIONS'],P.method)}</select></label><label>Path and query<input id="http-path" value="${esc(P.path)}"></label><label>Timeout per request (seconds)<input id="http-timeout" type="number" min="1" max="30" value="${P.timeout}"></label></div><div class="notice info"><p>${esc(note)}</p></div><fieldset class="http-dns" ${T.mode==='public'?'hidden':''}><legend>DNS names / HTTP authorities to test</legend>${names.map(name=>`<label><input type="checkbox" data-http-dns value="${esc(name)}" ${T.names.includes(name)?'checked':''}><code>${esc(name)}</code></label>`).join('')}<small>${esc(dns.domainSource)}. One request per selected name.</small></fieldset><h3>HTTP headers</h3><div id="http-headers">${Object.entries(P.headers).map(([name,value],i)=>`<div class="http-header-row" data-http-header><input data-http-name aria-label="Header name" value="${esc(name)}" placeholder="Header name"><input data-http-value aria-label="Header value" type="${httpSecretHeader(name)?'password':'text'}" value="${esc(value)}" placeholder="Value"><button data-http-remove="${i}" aria-label="Remove header ${esc(name)}">Remove</button></div>`).join('')}</div><button id="http-add-header">Add header</button><details><summary>Request body</summary><textarea id="http-body" rows="3">${esc(P.body)}</textarea></details><small class="muted">Profiles stay local to Flightbay and are not included in capture exports. Sensitive headers are masked in results. The client does not follow redirects.</small>${T.error?`<p class="red" role="alert">${esc(T.error)}</p>`:''}<div class="http-run"><button class="primary" id="http-run" ${T.busy?'disabled':''}>${T.busy?'Running…':'Run HTTP test'}</button><span>${T.names.length} selected DNS names</span></div><div class="http-results">${T.results.map((r,i)=>`<article><div class="row wrap"><strong>${r.error?'Request error':'HTTP '+r.status}</strong><span>${r.durationMs} ms</span><code>${esc(r.authority)}</code></div><p>${esc(r.note)}</p><small>Connection: ${esc(r.connection)}${r.pod?' → '+esc(r.pod.cluster+'/'+r.pod.namespace+'/'+r.pod.name):''}<br>Request ID: <code>${esc(r.id)}</code> · ${esc(r.started)}</small>${r.error?`<p class="red">${esc(r.error)}</p>`:''}<details><summary>Response headers and body${r.truncated?' (truncated at 32 KiB)':''}</summary><pre class="code">${esc(JSON.stringify(r.responseHeaders||{},null,2))}</pre><pre class="code">${esc(r.body)}</pre></details><button data-http-logs="${i}">Logs around this request</button><small class="muted">X-Flightbay-Test-ID is sent for correlation; an exact match requires the app/gateway to log that header.</small></article>`).join('')}</div></div>`);
  document.querySelector('.modal').classList.add('http-test-modal');
  if(T.results.length)document.querySelector('.http-results').insertAdjacentHTML('beforebegin','<h3>Last test results</h3>');
  const updateCount=()=>{const count=document.querySelector('.http-run>span');if(count)count.textContent=T.mode==='public'?'1 direct request':[...document.querySelectorAll('[data-http-dns]:checked')].length+' selected DNS names';};updateCount();document.querySelectorAll('[data-http-dns]').forEach(e=>e.onchange=updateCount);
  if(T.busy)document.querySelectorAll('.http-test input,.http-test select,.http-test textarea,.http-test button').forEach(e=>e.disabled=true);
  $('#http-mode').onchange=()=>{captureHTTPTest();drawHTTPTest();};
  $('#http-profile').onchange=e=>{if(e.target.value===''){T.profile={name:'',method:'GET',path:'/',headers:{},body:'',timeout:10};}else{T.profile=JSON.parse(JSON.stringify(T.profiles[Number(e.target.value)]));}drawHTTPTest();};
  $('#http-add-header').onclick=()=>{captureHTTPTest();const row=document.createElement('div');row.className='http-header-row';row.dataset.httpHeader='';row.innerHTML='<input data-http-name aria-label="Header name" placeholder="Header name"><input data-http-value aria-label="Header value" placeholder="Value"><button>Remove</button>';$('#http-headers').appendChild(row);row.querySelector('button').onclick=()=>row.remove();row.querySelector('[data-http-name]').onchange=e=>row.querySelector('[data-http-value]').type=httpSecretHeader(e.target.value)?'password':'text';row.querySelector('input').focus();};
  document.querySelectorAll('[data-http-remove]').forEach(b=>b.onclick=()=>b.closest('[data-http-header]').remove());
  document.querySelectorAll('[data-http-name]').forEach(e=>e.onchange=()=>e.closest('[data-http-header]').querySelector('[data-http-value]').type=httpSecretHeader(e.value)?'password':'text');
  $('#http-save-profile').onclick=async()=>{captureHTTPTest();try{T.profiles=await apiPost('api/ambient/http-profiles',T.profile);T.error='';toast('Profile saved locally');}catch(e){T.error=e.message;}drawHTTPTest();};
  $('#http-run').onclick=async()=>{captureHTTPTest();if(T.busy)return;T.busy=true;T.error='';T.results=[];drawHTTPTest();const g=T.gateway!==''?T.options.gateways[Number(T.gateway)]:null;try{const response=await apiPost('api/ambient/http-test',{service:T.service,context:T.context,mode:T.mode,url:T.url,names:T.names,port:T.port,gatewayService:g?.service,gatewayPort:g?.port,profile:T.profile});T.results=response.results;}catch(e){T.error=e.message;}finally{T.busy=false;if(document.querySelector('.http-test'))drawHTTPTest();else toast('HTTP test finished. Reopen Test service to see the result.');}};
  document.querySelectorAll('[data-http-logs]').forEach(b=>b.onclick=()=>httpTestOpenLogs(Number(b.dataset.httpLogs)));
}

function httpTestOpenLogs(index){
  captureHTTPTest();const T=HTTP_TEST,r=T.results[index];if(!r)return;
  T.logReturn={hash:location.hash||'#ambient',cluster:S.cluster,x:scrollX,y:scrollY,mainScrollTop:$('#main')?.scrollTop||0,modalScrollTop:document.querySelector('.http-test-modal')?.scrollTop||0,openDetails:[...document.querySelectorAll('.http-test details')].map(e=>e.open),index,ambient:S.view==='ambient'?Object.fromEntries(['key','kind','tab','phase','clusterScope','pickerOpen'].map(k=>[k,S.ambient[k]])):null,awaitingReturn:true};
  const ref=r.pod||T.service;S.cluster=ref.cluster;$('#cluster').value=ref.cluster;
  openLogs({namespace:ref.namespace,pod:r.pod?.name||'',from:new Date(new Date(r.started).getTime()-2000).toISOString(),to:new Date(new Date(r.finished).getTime()+2000).toISOString()});
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
  requestAnimationFrame(()=>{if(!document.querySelector('.http-test'))return;scrollTo(back.x,back.y);const main=$('#main');if(main)main.scrollTop=back.mainScrollTop;const modal=document.querySelector('.http-test-modal');if(modal)modal.scrollTop=back.modalScrollTop;document.querySelector(`[data-http-logs="${back.index}"]`)?.focus({preventScroll:true});});
}
