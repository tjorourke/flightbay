'use strict';

const AM_RETRY_PRESETS=[
  {value:'connect-failure,refused-stream',label:'Connection failures only',detail:'Retry a failed connection or a refused stream.'},
  {value:'gateway-error,connect-failure,refused-stream',label:'Connection failures and gateway errors',detail:'Also retry HTTP 502, 503 and 504.'},
  {value:'5xx',label:'Any HTTP 5xx response',detail:'Retry server errors. Use only for operations that can safely be repeated.'}
];
const AM_CIRCUIT_PRESETS={
  standard:{label:'Standard · 100 connections, 1,000 active requests',maxConnections:100,maxPendingRequests:100,maxRequests:1000,maxRetries:3,maxRequestsPerConnection:0,connectTimeout:'5s'},
  small:{label:'Small backend · 20 connections, 100 active requests',maxConnections:20,maxPendingRequests:20,maxRequests:100,maxRetries:2,maxRequestsPerConnection:0,connectTimeout:'2s'}
};
function amUsesResilience(){const A=S.ambient,e=A.result?.environment?.selected;if(A.edit&&A.edit.ref.kind!=='EnvoyFilter')return false;return e?.product==='istio'&&e.edition==='enterprise'&&Object.keys(A.tasks).some(k=>A.tasks[k]&&['rate-limit','global-rate-limit','retries','circuit-breaker','endpoint-failover'].includes(k));}
function amDurationParts(value){
  const text=String(value??''),simple=/^(\d+(?:\.\d+)?)(ms|s|m)$/.exec(text);
  if(simple)return {value:simple[1],unit:simple[2]};
  const terms=[...text.matchAll(/(\d+(?:\.\d+)?)(ms|h|m|s)/g)];
  if(terms.length&&terms.map(m=>m[0]).join('')===text)return {value:String(terms.reduce((n,m)=>n+Number(m[1])*({ms:.001,s:1,m:60,h:3600}[m[2]]),0)),unit:'s'};
  return {value:'',unit:'s'};
}
function amDurationControl(id,label,value){const p=amDurationParts(value);return `<label>${esc(label)}<span class="am-duration-control" data-amduration="${id}"><input type="number" min="0" step="any" data-amduration-value value="${esc(p.value)}" aria-label="${esc(label)} amount"><select data-amduration-unit aria-label="${esc(label)} unit">${amOpts([{value:'ms',label:'Milliseconds'},{value:'s',label:'Seconds'},{value:'m',label:'Minutes'}],p.unit)}</select><input type="hidden" id="am-${id}" value="${esc(value)}"></span></label>`;}
function amCaptureDurationControls(){document.querySelectorAll('[data-amduration]').forEach(row=>{const amount=row.querySelector('[data-amduration-value]').value,unit=row.querySelector('[data-amduration-unit]').value;$('#am-'+row.dataset.amduration).value=amount===''?'':amount+unit;});}
function amPresetSelect(id,label,options,value){return `<label>${esc(label)}<select id="am-${id}">${amOpts(options,value)}</select></label>`;}
function amCircuitProfile(value){const v=value||amCircuitBreakerDefaults();return Object.keys(AM_CIRCUIT_PRESETS).find(key=>Object.keys(amCircuitBreakerDefaults()).every(k=>String(v[k])===String(AM_CIRCUIT_PRESETS[key][k])))||'custom';}
function amGuidedCircuitFields(value){
  const defaults=amCircuitBreakerDefaults(),v={...defaults,...value};for(const key of ['maxConnections','maxPendingRequests','maxRequests','maxRetries','connectTimeout'])if(!v[key])v[key]=defaults[key];
  return `<div class="am-control-section"><h4>Limit concurrent load</h4>${amPresetSelect('circuit-profile','Start with a profile',[...Object.entries(AM_CIRCUIT_PRESETS).map(([key,p])=>({value:key,label:p.label})),{value:'custom',label:'Custom limits'}],amCircuitProfile(v))}<div class="am-fields">${amInput('cb-maxConnections','Maximum connections',v.maxConnections,'number')}${amInput('cb-maxPendingRequests','Maximum queued requests',v.maxPendingRequests,'number')}${amInput('cb-maxRequests','Maximum active requests',v.maxRequests,'number')}${amDurationControl('cb-connectTimeout','Connection timeout',v.connectTimeout)}</div><details class="am-resilience-help"><summary>Additional connection limits</summary><div class="am-fields">${amInput('cb-maxRetries','Maximum outstanding retries',v.maxRetries,'number')}${amInput('cb-maxRequestsPerConnection','Requests per connection · 0 means unlimited',v.maxRequestsPerConnection,'number')}</div></details><p class="muted">Requests above these limits can receive HTTP 503. Limits are per upstream cluster in each waypoint.</p></div>`;
}
function amLimiterServices(svc,mesh){const inv=S.ambient.inventory||{};return mesh?(inv.publishedGRPCServices||[]).filter(s=>s.cluster===svc.cluster):(inv.services||[]).filter(s=>s.cluster===svc.cluster);}
function amQuotaKey(q){return JSON.stringify([q.domain,q.key,q.value]);}
function amGlobalLimiterMarkup(svc,g){
  const A=S.ambient,services=amLimiterServices(svc,!!g.mesh),namespaces=[...new Set(services.map(s=>s.namespace).concat(g.namespace?[g.namespace]:[]))].sort(),scoped=services.filter(s=>s.namespace===g.namespace),selected=scoped.find(s=>s.name===g.service),ports=selected?.ports||[],quotas=(A.inventory.rateLimitQuotas||[]).filter(q=>q.source.cluster===svc.cluster),quota=quotas.find(q=>q.domain===g.domain&&q.key===g.descriptorKey&&q.value===g.descriptorValue),quotaChoice=quota?amQuotaKey(quota):'custom';
  const serviceOptions=scoped.map(s=>({value:s.name,label:s.name+(/rate.?limit|rls/i.test(s.name)?' · limiter candidate':'')}));if(g.service&&!serviceOptions.some(s=>s.value===g.service))serviceOptions.push({value:g.service,label:g.service+' · not captured'});
  return `<section class="am-control-section"><h4>Choose the shared limiter</h4><p>The waypoint calls an existing rate-limit service. Its shared Redis and Redis authentication are part of ambient setup.</p>${amPresetSelect('rls-connection','How is the limiter reached?',[{value:'direct',label:'Service in this cluster · direct gRPC'},{value:'mesh',label:'Published mesh Service · shared across clusters'}],g.mesh?'mesh':'direct')}<div class="am-fields">${amPresetSelect('rls-namespace','Namespace',[{value:'',label:'Choose a namespace…'},...namespaces],g.namespace||'')}${amPresetSelect('rls-service','Rate-limit Service',[{value:'',label:'Choose the existing limiter…'},...serviceOptions],g.service||'')}${amPresetSelect('rls-port','gRPC Service port',[{value:'',label:'Choose a port…'},...ports.map(port=>({value:port,label:String(port)+(selected?.portDetails?.find(p=>p.port===port)?.name?' · '+selected.portDetails.find(p=>p.port===port).name:'')}))],g.port||'')}</div>
    ${g.mesh?`<p class="muted">Uses the captured published endpoint <code>${esc(selected?.host||[g.service,g.namespace,g.meshDomain||'mesh.internal'].filter(Boolean).join('.'))}</code> through Istio’s mesh transport.</p>`:'<p class="muted">Direct mode uses plaintext HTTP/2 to the selected Service. Select a limiter configured for that transport.</p>'}
    <h4>Choose the API quota</h4>${amPresetSelect('rls-quota','Existing quota',[...quotas.map(q=>({value:amQuotaKey(q),label:`${q.domain} · ${q.value} · ${q.requests}/${q.unit} (${q.source.namespace}/${q.source.name})`})),{value:'custom',label:'Enter a quota reference from ambient setup'}],quotaChoice)}
    ${quota?`<p class="am-control-summary"><strong>${quota.requests} requests per ${esc(quota.unit)}</strong> · shared quota <code>${esc(quota.value)}</code>. Thresholds are managed in the existing limiter.</p>`:`<div class="am-fields">${amInput('rls-domain','Limiter domain',g.domain||'')}${amInput('rls-key','Quota descriptor key',g.descriptorKey||'api')}${amInput('rls-value','Quota descriptor value',g.descriptorValue||'')}</div><p class="muted">${quotas.length?'These values must identify a quota already loaded by the selected limiter.':'No explicit quota definitions were captured. Enter the domain and descriptor supplied with the existing limiter, or capture its quota configuration.'}</p>`}
    ${amPresetSelect('rls-outage','If the limiter is unavailable',[{value:'allow',label:'Allow matching requests · fail open'},{value:'deny',label:'Reject matching requests · fail closed'}],g.failClosed?'deny':'allow')}
    <details class="am-resilience-help"><summary>Redis, authentication and shared counters</summary><p>Flightbay does not deploy Redis or the limiter. The limiter’s Redis endpoint, authentication Secret references and any required TLS belong to ambient setup. All limiter replicas and selected meshes must use the same logical Redis store. Redis credentials are not included in this policy. Captured quota definitions do not prove the selected limiter has loaded them.</p></details></section>`;
}
function amRetryMarkup(F,r){
  const value=r.retryOn??AM_RETRY_PRESETS[0].value,preset=AM_RETRY_PRESETS.find(p=>p.value===value),count=F.retryAttempts??2,customCount=![1,2,3,5].includes(Number(count));
  return `<section class="am-control-section"><h4>Retry failed requests</h4>${amPresetSelect('retry-preset','When should a request be retried?',[...AM_RETRY_PRESETS.map(p=>({value:p.value,label:p.label})),{value:'custom',label:'Custom retry conditions'}],preset?.value||'custom')}${preset?`<p class="muted">${esc(preset.detail)}</p>`:amInput('resilience-retryOn','Envoy retry conditions',value)}<div class="am-fields">${amPresetSelect('retry-count','Maximum retries',[...([1,2,3,5].map(n=>({value:n,label:`${n} ${n===1?'retry':'retries'} · at most ${n+1} attempts`}))),{value:'custom',label:'Custom count (1–10)'}],customCount?'custom':count)}${customCount?amInput('retryAttempts','Retry count',count,'number'):`<input type="hidden" id="am-retryAttempts" value="${count}">`}${amDurationControl('timeout','Total request budget',F.timeout??'5s')}${amDurationControl('backendTimeout','Timeout per attempt',F.backendTimeout??'1s')}</div><details class="am-resilience-help"><summary>Retries across A → B → C</summary><p>This policy controls one proxy hop. Applications must propagate remaining deadlines and cancellation across service calls. Configure retries at a selected layer and bound the time budget to avoid multiplying attempts. Non-idempotent operations can repeat application work.</p></details></section>`;
}
function amEndpointFailureMarkup(F,r){
  const mode=r.disableConsecutive?'percentage':r.failurePercentage>0?'both':'consecutive',percent=r.failurePercentage||50;
  return `<section class="am-control-section"><h4>Avoid failing endpoints</h4>${amPresetSelect('failure-trigger','Detect failures using',[{value:'consecutive',label:'Consecutive HTTP 5xx errors'},{value:'percentage',label:'HTTP error percentage'},{value:'both',label:'Consecutive errors and error percentage'}],mode)}<div class="am-fields">${mode!=='percentage'?amInput('failureThreshold','Consecutive errors before ejection',F.failureThreshold??3,'number'):''}${mode!=='consecutive'?amInput('resilience-failurePercentage','Error threshold (%)',percent,'number'):''}${amDurationControl('ejectionInterval','Detection window',F.ejectionInterval??'5s')}${amDurationControl('ejectionDuration','Keep failing endpoints out for',F.ejectionDuration??'30s')}${amPresetSelect('maxEjectionPercent','Maximum endpoints to eject',[...new Set([10,25,50,100,F.maxEjectionPercent??100])].sort((a,b)=>a-b).map(n=>({value:n,label:n+'%'})),F.maxEjectionPercent??100)}</div>${mode!=='consecutive'?'<p class="muted">Percentage detection requires at least 50 requests per endpoint in a detection window.</p>':''}<details class="am-resilience-help"><summary>What opens, and what stays available?</summary><p>Ejection and connection overflow are per upstream cluster in each waypoint. API-path isolation keeps other APIs on their original backend cluster. These are not a shared closed/open/half-open state machine. Native latency-triggered opening is not generated; use timeouts and application or external health logic for that requirement.</p></details></section>`;
}
function amResilienceMarkup(svc){
  if(!amUsesResilience())return '';
  const A=S.ambient,F=A.settings,r=F.resilience||{scope:'service'},api=r.scope==='api',route=r.scope==='route',match=r.match||{pathType:'PathPrefix',path:'/api',method:''},breaker=A.tasks['circuit-breaker']||A.tasks['endpoint-failover'];
  const routes=(A.result?.editable||[]).filter(e=>e.ref.kind==='HTTPRoute'&&e.request),choices=routes.map(e=>({value:e.ref.name+':'+e.rule,label:e.ref.name+' · rule '+(e.rule+1)}));
  const scopes=[{value:'service',label:'Whole Service'},{value:'api',label:'One API path'},...(!breaker?[{value:'route',label:'An existing HTTPRoute rule'}]:[])];
  return `<fieldset class="am-resilience"><legend>Traffic scope</legend>${amPresetSelect('resilience-scope','Which traffic should this control?',scopes,r.scope||'service')}
    ${api?`<div class="am-fields">${amPresetSelect('resilience-pathType','Path matches',[{value:'Exact',label:'Exactly this path'},{value:'PathPrefix',label:'This path and its children'}],match.pathType)}${amInput('resilience-path','API path',match.path)}${amPresetSelect('resilience-method','HTTP method',[{value:'',label:'Any method'},'GET','HEAD','POST','PUT','PATCH','DELETE','OPTIONS'],match.method||'')}${amPresetSelect('resilience-backend','Application backend',A.inventory.services.filter(s=>s.cluster===svc.cluster&&s.namespace===svc.namespace).map(s=>s.name),r.backend||svc.name)}</div><p class="muted">${breaker?'Uses a separate backend alias for this API’s connection pool and endpoint state.':'Applies the control only to this API’s generated route.'} Other APIs retain their normal route.</p>`:''}
    ${route?amPresetSelect('resilience-route','Route and rule',[{value:'',label:'Choose a captured rule…'},...choices],(r.route||'')+':'+(r.rule||0)):''}
    ${A.tasks['rate-limit']?`<p class="am-control-summary"><strong>${F.requests??10} requests per ${esc(({Seconds:'second',Minutes:'minute',Hours:'hour'})[F.unit||'Seconds'])}</strong> per matched route/virtual host in each waypoint · burst ${F.burst??F.requests??10}. An empty bucket returns HTTP 429.</p><details class="am-resilience-help"><summary>Why does the generated YAML use an EnvoyFilter?</summary><p>Your selected waypoint is Solo Enterprise Istio. Its supported rate-limit implementation is Envoy’s local filter. Flightbay installs the filter, enables it only for this Service or route, and covers captured local and global DNS names. A whole-waypoint filter is shorter but would limit other Services on the shared waypoint too. Local buckets are per route/virtual host in each waypoint process. Select a global quota when both DNS aliases and all proxies must consume one shared counter. The Flightbay annotation stores editor settings; it is not part of Envoy’s rate-limit decision.</p></details>`:''}
    ${A.tasks['global-rate-limit']?amGlobalLimiterMarkup(svc,r.global||{}):''}
    ${A.tasks['circuit-breaker']?amGuidedCircuitFields(F.circuitBreaker):''}
    ${breaker?amEndpointFailureMarkup(F,r):''}
    ${A.tasks.retries?amRetryMarkup(F,r):''}
    <details class="am-resilience-help"><summary>Implementation and scope</summary><p>Generated for the selected Solo Enterprise Istio waypoint. Service/API scope includes captured published names; existing-rule scope names only that rule. Review route precedence and the generated YAML before applying.</p></details></fieldset>`;
}
function amCaptureResilience(){
  amCaptureDurationControls();
  const F=S.ambient.settings,scope=$('#am-resilience-scope');if(!scope)return;
  const r=F.resilience||{};r.scope=scope.value;
  if($('#am-resilience-path'))r.match={pathType:$('#am-resilience-pathType').value,path:$('#am-resilience-path').value,method:$('#am-resilience-method').value};
  if($('#am-resilience-backend'))r.backend=$('#am-resilience-backend').value;
  if($('#am-resilience-route')){const value=$('#am-resilience-route').value,at=value.lastIndexOf(':');r.route=at<0?'':value.slice(0,at);r.rule=at<0?0:Number(value.slice(at+1));}
  if(r.scope==='service'){delete r.match;delete r.route;delete r.rule;delete r.backend;}else if(r.scope==='api'){delete r.route;delete r.rule;}else{delete r.match;delete r.backend;}
  if($('#am-retry-preset'))r.retryOn=$('#am-retry-preset').value==='custom'?$('#am-resilience-retryOn')?.value||'':$('#am-retry-preset').value;
  if($('#am-failure-trigger')){const mode=$('#am-failure-trigger').value;r.disableConsecutive=mode==='percentage';r.failurePercentage=mode==='consecutive'?0:$('#am-resilience-failurePercentage')?Number($('#am-resilience-failurePercentage').value):50;}
  if($('#am-rls-service')){
    const prior=r.global||{},mesh=$('#am-rls-connection').value==='mesh',namespace=$('#am-rls-namespace').value,service=$('#am-rls-service').value,selected=amLimiterServices(amService(),mesh).find(s=>s.namespace===namespace&&s.name===service),choice=$('#am-rls-quota').value,quota=(S.ambient.inventory.rateLimitQuotas||[]).find(q=>q.source.cluster===amService().cluster&&amQuotaKey(q)===choice);
    r.global={namespace,service,port:Number($('#am-rls-port').value),domain:quota?.domain??$('#am-rls-domain')?.value??prior.domain??'',descriptorKey:quota?.key??$('#am-rls-key')?.value??'api',descriptorValue:quota?.value??$('#am-rls-value')?.value??'',failClosed:$('#am-rls-outage').value==='deny',mesh,meshDomain:selected?.meshDomain||prior.meshDomain||'mesh.internal'};
  }
  F.resilience=r;
}
function amValidateResilienceForm(){if($('#am-resilience-retryOn')&&!$('#am-resilience-retryOn').value.trim())throw new Error('Enter custom retry conditions or choose a retry preset.');if($('#am-resilience-failurePercentage')&&!$('#am-resilience-failurePercentage').value.trim())throw new Error('Enter an error percentage from 1 to 100.');}
function amWireResilience(){
  const redraw=(id,change)=>{const field=$('#am-'+id);if(field)field.addEventListener('change',event=>{event.stopImmediatePropagation();amCapture();change?.(field.value);amInvalidate();render();});};
  redraw('resilience-scope');
  redraw('retry-preset',value=>{S.ambient.settings.resilience.retryOn=value==='custom'?'':value;});
  redraw('retry-count',value=>{S.ambient.settings.retryAttempts=value==='custom'?4:Number(value);});
  redraw('failure-trigger',value=>{const r=S.ambient.settings.resilience;r.disableConsecutive=value==='percentage';r.failurePercentage=value==='consecutive'?0:r.failurePercentage||50;});
  redraw('circuit-profile',value=>{if(AM_CIRCUIT_PRESETS[value]){const {label,...settings}=AM_CIRCUIT_PRESETS[value];S.ambient.settings.circuitBreaker={...settings};}});
  for(const id of ['rls-connection','rls-namespace','rls-service','rls-quota'])redraw(id,value=>{
    const F=S.ambient.settings,g=F.resilience.global;
    if(id==='rls-connection'){g.namespace='';g.service='';g.port=0;}
    if(id==='rls-namespace'){g.service='';g.port=0;}
    if(id==='rls-service'){
      const selected=amLimiterServices(amService(),g.mesh).find(s=>s.namespace===g.namespace&&s.name===value),grpc=selected?.portDetails?.filter(p=>/grpc|h2c/i.test(p.name+' '+p.appProtocol))||[];
      g.port=grpc.length===1?grpc[0].port:selected?.ports?.length===1?selected.ports[0]:0;if(selected?.meshDomain)g.meshDomain=selected.meshDomain;
    }
    if(id==='rls-quota'&&value==='custom'){g.domain='';g.descriptorKey='api';g.descriptorValue='';}
  });
  document.querySelectorAll('[id^="am-cb-"], [data-amduration="cb-connectTimeout"] input, [data-amduration="cb-connectTimeout"] select').forEach(field=>field.addEventListener('input',()=>{if($('#am-circuit-profile'))$('#am-circuit-profile').value='custom';}));
  if(S.ambient.edit?.ref.kind==='EnvoyFilter')for(const id of ['am-resilience-scope','am-resilience-pathType','am-resilience-path','am-resilience-method','am-resilience-route','am-resilience-backend','am-port']){const field=$('#'+id);if(field){field.disabled=true;field.title='Editing retains the API boundary. Add a new scoped configuration to change it.';}}
}
