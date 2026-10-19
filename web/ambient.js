'use strict';

S.ambient = {inventory: null, key: '', route: '', tab: 'investigate', tasks: {}, settings: {}, plan: null, validation: null, context: '', contexts: null, tests: [], status: null, error: '', busy: false, mode: '', edit: null, kind: '', phase: 1, serviceFilter: '', previewTab: 'summary'};
const amKey = o => JSON.stringify(o.kind && o.kind !== 'Service' ? [o.cluster, o.kind, o.namespace, o.name] : [o.cluster, o.namespace, o.name]);
const amTarget = () => S.ambient.inventory?.objects?.find(o => amKey(o) === S.ambient.key) || S.ambient.inventory?.services.find(o => amKey(o) === S.ambient.key);
const amService = () => S.ambient.inventory?.services.find(o => amKey(o) === S.ambient.key);
const amQuery = () => { const o = amTarget(); return o ? {cluster: o.cluster, namespace: o.namespace, kind: o.kind || 'Service', name: o.name, service: o.kind === 'Service' ? o.name : '', route: S.ambient.route} : {}; };
const amState = s => `<span class="badge ${s === 'pass' ? 'green' : s === 'fail' ? 'red' : s === 'warning' ? 'amber' : 'muted'}">${esc({pass: 'Pass', fail: 'Failed', warning: 'Check', unknown: 'Unknown', na: 'Not applicable'}[s] || s)}</span>`;
const amLabel = o => `${o.kind || 'Service'} · ${o.cluster} · ${o.namespace ? o.namespace + '/' : ''}${o.name}`;
const amOpts = (xs, value) => xs.map(x => { const v = typeof x === 'object' ? x.value : x, l = typeof x === 'object' ? x.label : x; return `<option value="${esc(v)}" ${String(value) === String(v) ? 'selected' : ''}>${esc(l)}</option>`; }).join('');
const amInput = (id, label, value, type = 'text') => `<label>${label}<input id="am-${id}" type="${type}" value="${esc(value ?? '')}"></label>`;
function amLiveContext(cluster,contexts){if(!cluster)return null;const matches=(contexts||[]).filter(c=>c.liveCluster===cluster);return matches.length===1?matches[0]:null;}
function amContextControl(cluster,contexts,current,attributes=''){
  const live=amLiveContext(cluster,contexts);
  if(live&&current===live.name)return `<div class="am-live-context"><strong>Live connection · ${esc(live.name)}</strong><div class="mono">${esc(live.server)}</div><small>Using this desk's connection for ${esc(cluster)}.</small></div>`;
  return `<label>Kubeconfig context<select ${attributes}><option value="">Choose the context for ${esc(cluster)}…</option>${amOpts((contexts||[]).map(c=>({value:c.name,label:c.name+' · '+c.server})),current)}</select><small>No matching live connection is available. Choose the context to validate against this capture.</small></label>`;
}
function amValidated(entry){const v=entry.validation,age=Date.now()-Date.parse(v?.validated);return !!v?.token&&v.context===entry.context&&v.plan?.id===entry.plan.id&&age>=-60000&&age<9*60*1000;}
async function amValidatePlans(entries,force=false){
  for(const entry of entries){
    if(!force&&amValidated(entry))continue;
    const context=entry.context,plan=entry.plan;entry.validation=null;entry.error='';entry.state='Validating…';
    try{
      if(!context)throw new Error('No matching live connection is available for '+(entry.cluster||plan.request.cluster)+'. Choose a real context first.');
      const validation=await apiPost('api/ambient/test',{operation:'validate',context,request:plan.request,planId:plan.id});
      if(entry.context!==context||entry.plan!==plan)throw new Error('The preview or connection changed during validation. Review it again.');
      entry.validation=validation;entry.state='Dry-run passed';
    }catch(err){entry.error=err.message;entry.state='Dry-run failed';throw err;}
  }
}
function amInvalidate() { S.ambient.plan = null; S.ambient.globalBatch = null; S.ambient.validation = null;S.ambient.testFeedback=null; $('#am-plan')?.replaceChildren(); }
function amResetDraft(keepEdits=false) {const A=S.ambient;if(!keepEdits){A.editDrafts={};A.taskDraft=null;}Object.assign(A,{route:'',tasks:{},settings:{},globalBatch:null,mode:'',edit:null,editTasks:null,lastChange:null,currentRefreshPending:false,repairRef:null,error:'',message:'',context:'',phase:1,previewTab:'summary',createOpen:false});amInvalidate();}
function amChoose(o) { const A = S.ambient; amResetDraft(); A.key = amKey(o); A.kind=o.kind; A.serviceFilter='';A.checkFilter='all';A.pickerOpen=false;if(A.tab==='map')A.mapFocus=amKey(o); history.replaceState(null,'','#ambient'); if(S.cluster&&S.cluster!==o.cluster){A.clusterScope=o.cluster;setCluster(o.cluster);}else render(); }
function amClear(wizard=false){const A=S.ambient;amResetDraft();A.key='';A.kind='';A.result=null;A.serviceFilter='';if(wizard)A.tab='wizard';A.pickerOpen=A.tab!=='map';history.replaceState(null,'','#ambient');render().then(()=>{if(A.pickerOpen)$('#am-object-search')?.focus();});}
function ambientObjectButton(o) { return S.ws?.istioAmbient?.some(c=>c.cluster===o.cluster&&c.available)&&['Service', 'Namespace', 'Pod', 'Gateway', 'HTTPRoute', 'ServiceEntry'].includes(o.kind) ? '<button id="d-ambient">Investigate Istio Ambient →</button>' : ''; }
function ambientWireObject(o) { if (!$('#d-ambient')) $('#d-describe')?.insertAdjacentHTML('afterend', ambientObjectButton(o)); const b = $('#d-ambient'); if (b) b.onclick = () => { amResetDraft();S.ambient.key = amKey(o);S.ambient.kind=o.kind;S.ambient.tab='investigate';closeDrawer();nav('ambient'); }; }
function amDownload(text, name) { const url = URL.createObjectURL(new Blob([text], {type: 'application/yaml'})); const a = document.createElement('a'); a.href = url; a.download = name; a.click(); setTimeout(() => URL.revokeObjectURL(url), 1000); }

function amPicker(objects, target) {
  return `<section class="panel am-target-panel"><div class="panel-body"><div class="am-target-row"><label>Object type <select id="am-kind"><option value="">All types</option>${amOpts(['Namespace', 'Service', 'Pod', 'Gateway', 'HTTPRoute', 'ServiceEntry'], S.ambient.kind)}</select></label><div class="am-picker"><label for="am-object-search">${S.ambient.tab==='wizard'?'Where should this configuration apply?':'Find an object'}</label><input id="am-object-search" role="combobox" aria-autocomplete="list" aria-controls="am-object-options" aria-expanded="false" autocomplete="off" placeholder="Search by name, namespace or cluster…" value="${esc(target ? amLabel(target) : '')}"><div id="am-object-options" role="listbox" class="hidden"></div></div><button id="am-clear" class="ghost" title="Clear the selected object, type filter and configuration draft">Clear selection</button></div><div class="am-target-hint">${target?`<span class="badge">${esc(target.kind)}</span> <strong>${esc(target.namespace?target.namespace+'/':'')}${esc(target.name)}</strong> <span class="muted">${esc(target.cluster)}</span>`:'Choose a namespace for an overview, or a Service to configure its traffic.'}</div></div></section>`;
}
function amEnvironment(){const A=S.ambient,e=A.result?.environment;if(!e)return '';const cl=e.selected;return `<details class="am-implementation"><summary>Implementation details</summary><div class="am-implementation-body"><strong>${esc(cl?`${({istio:'Istio',agentgateway:'agentgateway'})[cl.product]||'Unrecognised'} ${cl.edition==='unknown'?'':cl.edition.toUpperCase()} waypoint`:e.summary)}</strong>${A.tab==='wizard'&&e.policyKind?`<p>Generated policy type: <b>${esc(e.policyKind)}</b></p>`:''}${cl?`<p>${esc(cl.name)}<br>Controller: ${esc(cl.controller||'Not captured')}<br>${esc(cl.reason)}</p>`:''}${(e.components||[]).map(c=>`<p><b>${esc(c.product)}</b> · ${esc(c.edition)}<br>${c.evidence.map(esc).join('<br>')}</p>`).join('')}<p>${(e.evidence||[]).map(esc).join('<br>')}</p></div></details>`;}
function amSteps(){const A=S.ambient,current=!amTarget()?0:A.phase;return `<nav class="am-stepper" aria-label="Configuration progress">${['Target','Configurations','Configure','Review'].map((label,i)=>`<button data-amphase="${i}" ${i>current?'disabled':''} ${i===current?'aria-current="step"':''}><span>${i<current?'✓':i+1}</span>${label}</button>`).join('')}</nav>`;}
function amConfigurationNav(target){
  const A=S.ambient,entries=A.result?.editable||[],edit=A.edit;
  const resource=edit?`${edit.ref.kind} ${edit.ref.name}${edit.ref.kind==='HTTPRoute'?' · Rule '+(edit.rule+1):''}`:A.mode?({waypoint:'Waypoint setup','enrol-namespace':'Ambient enrolment',endpoints:'Endpoint recovery','route-parent':'Route parent repair'}[A.mode]||A.mode):Object.keys(A.tasks).filter(k=>A.tasks[k]).map(k=>amTaskText[k]?.[0]||k).join(' · ')||'Choose a configuration';
  return `<nav class="am-breadcrumb" aria-label="Configuration breadcrumb"><div class="am-breadcrumb-path"><button data-amconfig-home title="Return to all configurations for this target">All configurations</button><span aria-hidden="true">›</span><span class="am-breadcrumb-target">${esc(target.namespace?target.namespace+'/':'')}${esc(target.name)}</span>${A.phase>1||edit||A.mode?`<span aria-hidden="true">›</span><span>${esc(resource)}</span><span aria-hidden="true">›</span><strong aria-current="page">${A.phase===3?'Review':A.phase===1?'Tasks':'Configure'}</strong>`:''}</div>${A.phase===1&&!edit&&A.taskDraft?'<button data-amresume-task>Resume configuration draft</button>':''}${entries.length?`<details class="am-config-switcher"><summary>Switch configuration · ${entries.length}</summary><div class="am-config-choices">${entries.map((e,i)=>`<button data-amedit="${i}" ${e.supported?'':'disabled'} ${edit&&amEditKey(edit)===amEditKey({ref:e.ref,rule:e.rule})?'aria-current="true"':''}><strong>${esc(e.ref.kind)} ${esc(e.ref.name)}${e.ref.kind==='HTTPRoute'?' · Rule '+(e.rule+1):''}</strong><small>${esc(e.supported?amConfigurationSummary(e):e.reason)}</small></button>`).join('')}</div></details>`:''}</nav>`;
}
function amLanding(){return `<section class="panel am-landing"><div class="panel-body"><h2>${S.ambient.tab==='wizard'?'What would you like to configure?':'Explore your ambient mesh'}</h2><p>Choose a starting point. You can change it or clear the selection at any time.</p><div class="am-intents"><button data-amintent="Service"><strong>Service traffic</strong><span>Routing, rate limits, timeouts and access policies</span></button><button data-amintent="Namespace"><strong>Namespace setup</strong><span>See all Services and set up a shared waypoint</span></button><button data-amintent="Gateway"><strong>Waypoint connections</strong><span>Inspect readiness and connected Services</span></button></div></div></section>`;}
async function ambientPage() {
  const A = S.ambient;
  if(S.ws?.istioAmbient&&!istioAmbientAvailable())return head('Istio Ambient','No confirmed ambient installation in the selected capture scope.')+'<div class="notice info">Include Istio API discovery plus an ambient-enrolled Namespace or ztunnel Pods. Gateway API CRDs or a sidecar-only installation do not establish ambient support. Missing capture data does not prove the cluster lacks ambient.</div>';
  if(A.clusterScope!==S.cluster){if(A.clusterScope!==undefined) {amResetDraft();A.key='';A.kind='';A.result=null;}A.clusterScope=S.cluster;}
  if (!A.inventory) A.inventory = await api('api/ambient');
  A.objects = (A.inventory.objects || A.inventory.services).filter(o => !S.cluster || o.cluster === S.cluster);
  const target = A.objects.find(o => amKey(o) === A.key), svc = amService();
  const pickerOpen=A.pickerOpen||(!target&&A.tab!=='map');
  const picker = `<div id="am-target-selector" ${pickerOpen?'':'hidden'}>${amPicker(A.objects,target)}</div>`;
  const tabs = `<nav class="pst-tabs am-tabs">${[['investigate', 'Investigate'], ['map', 'Attachment map'], ['path','Request path'], ['wizard', 'Configure']].map(([id, label]) => `<button class="pst-tab ${A.tab === id ? 'on' : ''}" data-amtab="${id}">${label}</button>`).join('')}</nav>`;
  if (A.tab === 'wizard') { if (!A.contexts) A.contexts = await api('api/ambient/contexts').catch(e => { A.error = e.message; return []; }); if(!A.context)A.context=amLiveContext(target?.cluster,A.contexts)?.name||''; A.tests = await api('api/ambient/tests').catch(() => A.tests); }
  let body = A.tab==='map'?amClusterCanvas(null):amLanding();
  if(A.tab==='path')body=await requestPathPage();
  if(A.tab==='demo')body=await bookinfoPage();
  if(!target && A.tab==='investigate') body=body+amKnownIssues()+amFixCatalogue();
  if (target && !['path','demo'].includes(A.tab)) {
    A.result = await api('api/ambient/investigate', amQuery());
    if (A.tab === 'investigate') body = amInvestigation(target);
    else if (A.tab === 'map') body = amClusterCanvas(target) + (target.kind==='Namespace'||target.kind==='Gateway'?'':`<details class="am-map-details"><summary>Routes, policies and workloads for ${esc(target.name)}</summary>${amGraph(target)}</details>`);
    else if(A.phase===3&&A.globalBatch)body=amGlobalBatchPanel();
    else if(A.phase===3&&A.plan)body=amPlanPanel();
    else if (A.mode === 'waypoint' || A.mode === 'enrol-namespace') body = amWaypoint(target);
    else if (svc && ['endpoints', 'route-parent'].includes(A.mode)) body = amRecovery(svc);
    else if (svc) { A.capabilities = await api('api/ambient/capabilities', {cluster: svc.cluster, namespace: svc.namespace, service: svc.name}); body = amWizard(svc); }
    else body = `<section class="panel am-landing"><div class="panel-body"><h2>Configure ${esc(target.name)}</h2><p>${target.kind==='Namespace'?'Set up this namespace, or select a Service below to configure its traffic.':'Choose an action for this object, or follow a connected Service.'}</p><div class="am-intents">${['Namespace', 'Gateway', 'ServiceEntry'].includes(target.kind) ? '<button id="am-open-waypoint"><strong>Set up a waypoint</strong><span>Use an existing waypoint or deploy a new one</span></button>' : ''}${target.kind === 'Namespace' ? '<button id="am-open-enrol"><strong>Enable ambient</strong><span>Enrol workloads in this namespace</span></button>' : ''}</div></div></section>` + (A.result.services?.length?amServiceOverview(target):amRelated());
  }
  if (A.tab === 'wizard'&&A.tests.length) body += `<details class="am-active-tests" ${A.status?'open':''}><summary>${A.tests.length} active test${A.tests.length===1?'':'s'} · status and undo</summary>${amTests()}</details>`;
  const targetBar=`<section class="am-targetbar"><div class="am-target-identity">${target?`<span class="badge">${esc(target.kind)}</span><strong title="${esc(amLabel(target))}">${esc(target.namespace?target.namespace+'/':'')}${esc(target.name)}</strong><span class="am-context-name">${esc(target.cluster)}</span>`:'<strong>All captured clusters</strong><span class="am-context-name">Select a resource to investigate or configure</span>'}</div><div class="am-target-actions"><button id="am-target-toggle" aria-expanded="${!!pickerOpen}" aria-controls="am-target-selector">${target?'Change target':'Select target'}</button>${target?`<button data-amevidence="${attr(target)}">View object</button><button data-amyaml="${attr(target)}">YAML</button>${amEnvironment()}`:''}</div></section>`;
  const header=`<header class="am-header"><h1>Istio Ambient</h1>${tabs}<div class="am-header-actions"><button id="am-bookinfo">Deploy Bookinfo demo</button><button id="am-start-new" class="primary">New configuration</button><button id="am-refresh" title="Refresh captured inventory">Refresh</button><a class="help-link" href="/docs/desk/ambient/" target="_blank" rel="noopener">Docs ↗</a></div></header>`;
  return `<div class="am-shell am-mode-${A.tab}${A.tab==='demo'?' am-mode-path':''}">${header}${['path','demo'].includes(A.tab)?'':targetBar+picker}${A.tab==='wizard'?`${target?amConfigurationNav(target):''}${amSteps()}`:''}${A.error?`<div class="notice bad mt" role="alert">${esc(A.error)}</div>`:''}<div class="am-workspace" id="am-workspace">${A.tab==='wizard'&&A.testFeedback?`<div class="am-test-feedback ${A.testFeedback.ok?'green':'red'}" role="status"><strong>${A.testFeedback.ok?'✓':'✕'} ${esc(A.testFeedback.label)}</strong><p>${esc(A.testFeedback.message)}</p></div>`:''}${body}</div></div>`;
}
function amRelated() { const seen = new Set(); return `<section class="panel mt"><div class="panel-head"><h2>Related resources</h2><small>Open the resource or its full YAML</small></div><div class="table-scroll"><table><thead><tr><th>Object</th><th>Context</th><th>Actions</th></tr></thead><tbody>${(S.ambient.result.related || []).filter(r => { const k = amKey(r); if (seen.has(k)) return false; seen.add(k); return true; }).map(r => `<tr><td><span class="badge">${esc(r.kind)}</span> ${esc(r.namespace ? r.namespace + '/' : '')}${esc(r.name)}</td><td>${esc(r.cluster)}</td><td><div class="row wrap"><button data-amrelated="${attr(r)}">Investigate</button><button data-amevidence="${attr(r)}">View object</button><button data-amyaml="${attr(r)}">YAML</button></div></td></tr>`).join('') || '<tr><td colspan="3">No related resources captured. Missing collection data does not prove absence.</td></tr>'}</tbody></table></div></section>`; }
function amInvestigation(target) {
  const A = S.ambient, r = A.result, routes = A.inventory.routes.filter(x => x.cluster === target.cluster && x.namespace === target.namespace);
  return `${target.kind === 'Service' ? `<div class="toolbar"><label>HTTPRoute <select id="am-route"><option value="">Related routes</option>${amOpts(routes.map(x => x.name), A.route)}</select></label><button id="am-open-waypoint">Select or deploy a waypoint</button></div>` : ''}<div class="row wrap">${['fail', 'warning', 'unknown', 'pass'].map(s => `${amState(s)} ${num(r.counts[s])}`).join(' · ')}</div>
  ${r.steps.some(s => s.state === 'fail') ? `<div class="notice bad mt"><div><strong>First failing check: ${esc(r.steps.find(s => s.state === 'fail').title)}</strong><p>${esc(r.steps.find(s => s.state === 'fail').detail)}</p><button data-amjump="${r.steps.findIndex(s => s.state === 'fail')}">Inspect failing step ↓</button></div></div>` : ''}
  <details class="am-explainer"><summary>How to read this investigation</summary>${r.notes.map(n=>`<p>${esc(n)}</p>`).join('')}</details>
  <div class="am-checks">${r.steps.map((s, i) => `<details class="panel am-check" id="am-step-${i}" ${s.state==='fail'?'open':''}><summary>${amState(s.state)}<strong>${esc(s.title)}</strong><span class="muted">${esc(s.detail.slice(0,110))}${s.detail.length>110?'…':''}</span></summary><div class="panel-body"><p>${esc(s.detail)}</p><div class="row wrap mt">${(s.actions || []).map(a => `<button data-amremedy="${attr(a)}" title="${esc(a.detail)}">${esc(a.label)} →</button>`).join('')}</div>${s.evidence?.length?`<details class="am-evidence"><summary>View evidence (${s.evidence.length})</summary>${s.evidence.map(e=>`<div class="am-evidence-row"><button class="link mono" data-amevidence="${attr(e.ref)}">${esc(e.ref.kind)} ${esc(e.ref.namespace?e.ref.namespace+'/':'')}${esc(e.ref.name)}</button><code>${esc(e.field)}</code><pre class="code">${esc(amPretty(e.value))}</pre></div>`).join('')}</details>`:''}${s.fixYaml ? `<details class="mt"><summary>Suggested Service-parent merge snippet</summary><pre class="code">${esc(s.fixYaml)}</pre><button data-amfix="${i}">Download suggested YAML</button></details>` : ''}</div></details>`).join('')}</div>` + amRelated();
}
function amPretty(value){try{return JSON.stringify(JSON.parse(value),null,2);}catch{return String(value);}}
function amViewScope(){const A=S.ambient;return JSON.stringify([S.cluster,A.key,A.route,A.tab]);}
function amDetailKeys(){const counts=new Map();return [...document.querySelectorAll('#main details')].map(el=>{const check=el.closest('.am-check'),base=el.matches('.am-check')?'check:'+el.dataset.amStep:(check?.dataset.amStep||'page')+':'+(el.querySelector(':scope > summary')?.textContent.trim()||''),n=counts.get(base)||0;counts.set(base,n+1);return [base+':'+n,el];});}
function amCaptureView(){const scope=$('#am-workspace')?.dataset.amScope;if(S.view!=='ambient'||S.ambient.tab!=='investigate'||!scope)return null;return {scope,details:new Map(amDetailKeys().map(([k,el])=>[k,el.open])),top:$('#main')?.scrollTop||0,x:scrollX,y:scrollY};}
function amRestoreView(state){if(!state||S.view!=='ambient'||state.scope!==amViewScope())return;for(const [key,el]of amDetailKeys())if(state.details.has(key))el.open=state.details.get(key);const main=$('#main');if(main)main.scrollTop=state.top;scrollTo(state.x,state.y);}
function amMirrorCommand(command){return command.replace(/^kubectl --context '([^']+)'/,(_,cluster)=>`kubectl --context '${cluster}@${S.ws?.live?'live':'snapshot'}'`);}
function amCommandBlock(command,label='Copy command'){return `<div class="terminal-command"><pre class="code">${esc(command)}</pre><button class="command-copy-icon" data-amcopycommand="${esc(command)}" title="${label}" aria-label="${label}">⧉</button></div>`;}
function amFixGuide(g){if(!g)return '';const mirror=g.command?amMirrorCommand(g.command):'';return `<div class="notice info mt"><div><strong>Suggested fix · ${esc(g.title)}</strong><p>${esc(g.fix)}</p>${g.command?`<details class="mt"><summary>Inspect this resource</summary><p class="muted">Flightbay terminal · read-only ${S.ws?.live?'live mirror':'snapshot'}</p>${amCommandBlock(mirror)}<div class="row wrap mt"><button data-amruncommand="${esc(mirror)}">Run in Flightbay terminal</button></div><details class="mt"><summary>Your own terminal · original cluster context</summary>${amCommandBlock(g.command,'Copy real-context command')}</details></details>`:''}<p class="mt"><strong>Verify:</strong> ${esc(g.verify)}</p></div></div>`;}
function amFixCatalogue(){const guides=S.ambient.inventory.fixGuides||[];return `<details class="panel mt"><summary class="panel-head">Common ambient problems and repairs (${guides.length})</summary><div class="panel-body"><p class="muted">A troubleshooting catalogue, not a measured frequency ranking. Repairs depend on the captured evidence and your intended traffic path.</p>${guides.map(g=>`<details class="mt"><summary>${esc(g.title)}</summary><p>${esc(g.fix)}</p><p><strong>Verify:</strong> ${esc(g.verify)}</p></details>`).join('')}</div></details>`;}
function amKnownIssues(){const issues=(S.ambient.inventory.issues||[]).filter(x=>!S.cluster||x.subject.cluster===S.cluster);return `<section class="panel am-known-issues" aria-label="Known ambient failures"><div class="panel-head"><h2>Needs attention</h2><span class="badge ${issues.length?'red':'muted'}">${issues.length} failed checks</span></div>${issues.length?`<div class="table-scroll"><table><thead><tr><th>Cluster</th><th>Resource</th><th>Failed check</th><th>Suggested fix</th><th></th></tr></thead><tbody>${issues.map((x,i)=>`<tr><td>${esc(x.subject.cluster)}</td><td><span class="badge">${esc(x.subject.kind)}</span> ${esc(x.subject.namespace)}/${esc(x.subject.name)}</td><td style="white-space:normal"><strong>${esc(x.step.title)}</strong><p>${esc(x.step.detail)}</p></td><td style="white-space:normal">${esc(x.step.suggestedFix?.fix||x.step.nextStep||'Inspect captured fields and controller status.')}</td><td><button data-amissue="${i}">Investigate and fix →</button></td></tr>`).join('')}</tbody></table></div>`:'<div class="panel-body">No confirmed failures in the captured checks for this scope. Missing evidence and untested traffic can still need investigation.</div>'}</section>`;}
function amParentRecovery(svc){const A=S.ambient,F=A.settings,route=A.repairRef||{namespace:svc.namespace,name:F.repairRoute||A.route},gateways=A.inventory.objects.filter(o=>o.kind==='Gateway'&&o.cluster===svc.cluster),gw=gateways.find(o=>o.namespace+'/'+o.name===F.parentGateway);return `<section class="panel"><div class="panel-head"><h2>Repair an HTTPRoute parent</h2></div><div class="panel-body" id="am-repair-parameters"><p>Choose the intended frontend. A Gateway parent can be an ingress or waypoint-wide route; a Service parent is a separate, explicit GAMMA choice. Other parents, matches, filters and backends are preserved.</p><p><strong>Cluster:</strong> ${esc(svc.cluster)} · <strong>HTTPRoute:</strong> ${esc(route.namespace)}/${esc(route.name)}</p>${amInput('repairRoute','HTTPRoute name',F.repairRoute||route.name)}${amInput('parentIndex','Parent entry (zero-based)',F.parentIndex??0,'number')}<label>Parent type<select id="am-parentKind">${amOpts(['Gateway','Service'],F.parentKind||'Gateway')}</select></label>${(F.parentKind||'Gateway')==='Gateway'?`<label>Existing Gateway in ${esc(svc.cluster)}<select id="am-parentGateway"><option value="">Choose the intended Gateway…</option>${amOpts(gateways.map(o=>({value:o.namespace+'/'+o.name,label:o.namespace+'/'+o.name})),F.parentGateway)}</select></label>${amInput('parentSection','Listener name (optional)',F.parentSection||'')}<p class="muted">${gw?'The chosen Gateway is captured. Listener eligibility and route status must still be verified.':'No Gateway is selected automatically.'}</p>`:`<p>Explicit frontend Service: ${esc(svc.namespace)}/${esc(svc.name)}. Service-scoped L7 traffic needs an appropriate waypoint.</p><label>Frontend Service port<select id="am-port">${amOpts(svc.ports,F.port||svc.ports[0])}</select></label>`}<button id="am-generate-repair" class="primary">Review parent change</button></div></section>`;}
function amDNSMarkup(d){if(!d)return '';const name=(label,value)=>`<div class="am-dns-row"><strong>${label}</strong><code>${esc(value)}</code><button class="ghost" data-amcopydns="${esc(value)}" aria-label="Copy ${esc(label)}">Copy</button></div>`;return `<div class="am-dns-names">${d.localName?name(d.domainObserved?'Cluster DNS':'Cluster DNS (default suffix)',d.localName):'<p>Cluster DNS suffix not established.</p>'}<small class="muted">${esc(d.domainSource)}</small>${(d.globalNames||[]).map(host=>name('Global mesh DNS',host)).join('')||'<p class="muted">No generated global DNS hostname captured.</p>'}${d.externalName?name('ExternalName target',d.externalName):''}<small class="muted">Names are from configuration, not a DNS reachability test.</small></div>`;}
function amWireStatusFilters(){
  const A=S.ambient;if(A.tab!=='investigate'||!A.result)return;
  const checks=[...document.querySelectorAll('.am-check')],row=document.querySelector('#am-workspace>.row.wrap');if(!row)return;
  const labels={all:'All',fail:'Failed',warning:'Check',unknown:'Unknown',pass:'Pass',na:'Not applicable'};
  row.classList.add('am-check-filters');row.setAttribute('aria-label','Filter investigation checks');
  row.innerHTML=Object.entries(labels).map(([state,label])=>`<button data-amcheckfilter="${state}" aria-pressed="false">${state==='all'?label:amState(state)} <strong>${state==='all'?checks.length:num(A.result.counts[state])}</strong></button>`).join('')+'<span id="am-check-count" class="muted" aria-live="polite"></span>';
  document.querySelector('.am-checks').insertAdjacentHTML('afterend','<div id="am-check-empty" class="notice info" hidden></div>');
  const apply=()=>{let visible=0;checks.forEach((check,i)=>{const show=!A.checkFilter||A.checkFilter==='all'||A.result.steps[i].state===A.checkFilter;check.hidden=!show;if(show)visible++;});row.querySelectorAll('[data-amcheckfilter]').forEach(b=>b.setAttribute('aria-pressed',String(b.dataset.amcheckfilter===(A.checkFilter||'all'))));$('#am-check-count').textContent=`Showing ${visible} of ${checks.length} checks`;const empty=$('#am-check-empty');empty.hidden=visible>0;empty.textContent=`No ${labels[A.checkFilter]||''} checks for this resource. Choose All to reset.`;const failure=document.querySelector('#am-workspace>.notice.bad');if(failure)failure.hidden=!!A.checkFilter&&!['all','fail'].includes(A.checkFilter);};
  row.querySelectorAll('[data-amcheckfilter]').forEach(b=>b.onclick=()=>{A.checkFilter=A.checkFilter===b.dataset.amcheckfilter?'all':b.dataset.amcheckfilter;apply();});apply();
}
function amOfficial(links){const safe=(links||[]).filter(d=>{try{const u=new URL(d.url);return u.protocol==='https:'&&['istio.io','docs.solo.io','agentgateway.dev','gateway-api.sigs.k8s.io','kubernetes.io'].includes(u.hostname);}catch{return false;}});return safe.length?`<div class="am-official"><span>Official docs</span>${safe.map(d=>`<a href="${esc(d.url)}" target="_blank" rel="noopener noreferrer">${esc(d.title)} <small>${esc(d.source)}</small> ↗</a>`).join('')}</div>`:'';}
function amTaskLinks(ids){const A=S.ambient,e=A.result?.environment,seen=new Set(),links=[];for(const id of ids){let docs=e?.taskDocs?.[id]||[];if(id==='waypoint'){const F=A.settings;const cls=F.wpMode==='new'?F.wpClass:A.inventory?.waypoints?.find(w=>w.cluster===amTarget()?.cluster&&w.namespace+'/'+w.name===F.wpExisting)?.class;if(cls&&e?.classDocs?.[cls])docs=e.classDocs[cls];}for(const d of docs){if(!seen.has(d.url)){seen.add(d.url);links.push(d);}}}return amOfficial(links);}
function amServiceOverview(target){const A=S.ambient,services=A.result.services||[],ready=services.filter(s=>s.ready>0).length;return `<section class="panel am-service-overview" id="am-graph"><div class="panel-head"><div><h2>${target.kind==='Namespace'?'Services in '+esc(target.name):'Services using '+esc(target.name)}</h2><p class="muted">${services.length} captured · ${ready} with ready endpoints · ${services.filter(s=>s.infrastructure).length} gateway infrastructure</p></div><span class="badge">All ${services.length} included</span></div><div class="panel-body"><div class="am-overview-tools"><label>Find a Service<input id="am-service-filter" placeholder="Filter by name or waypoint…" value="${esc(A.serviceFilter)}"></label><span id="am-service-count" aria-live="polite"></span></div><div class="am-service-grid">${services.map(s=>`<article class="am-service-card" data-amservicecard="${esc((s.name+' '+s.waypoint).toLowerCase())}"><div class="row between"><span class="badge">${s.infrastructure?'Gateway Service':'Service'}</span><span class="${s.state==='pass'?'green':s.state==='fail'?'red':'muted'}">${esc(s.detail)}</span></div><h3><button class="link" data-amrelated="${attr(s)}">${esc(s.name)}</button></h3><p class="am-service-wp">${s.waypoint?`Via <strong>${esc(s.waypoint)}</strong>`:'No waypoint selected'}</p><small class="muted">${s.routes} related route${s.routes===1?'':'s'}</small><div class="row mt"><button data-amdrill="${attr(s)}">Explore connections →</button><button data-amconfigure="${attr(s)}">Configure</button><button class="ghost" data-amevidence="${attr(s)}" aria-label="View ${esc(s.name)} YAML">YAML</button></div></article>`).join('')}</div><p id="am-service-empty" class="hidden">No Services match this filter. <button id="am-filter-clear">Clear filter</button></p><p class="muted mt">Endpoint readiness is separate from route attachment and observed traffic. Uncollected data is identified explicitly.</p></div></section>`;}
function amGraph(target) {
  const A = S.ambient, graph = A.result.graph;
  if(target.kind==='Namespace'||target.kind==='Gateway')return amServiceOverview(target);
  const node = n => `<div class="am-map-node"><div class="row between">${amKey(n.ref)===amKey(target)?'<span class="badge">Selected</span>':amState(n.state)}<span class="badge">${esc(n.ref.kind)}</span></div><button class="link mono" data-amevidence="${attr(n.ref)}" ${n.exists ? '' : 'disabled'}>${esc(n.ref.namespace ? n.ref.namespace + '/' : '')}${esc(n.ref.name)}</button>${(n.notes || []).map(x => `<small>${esc(x)}</small>`).join('')}${(A.result.editable || []).map((e, i) => amKey(e.ref) === amKey(n.ref) ? `<button data-amedit="${i}" ${e.supported ? '' : 'disabled'} title="${esc(e.reason)}">Edit${e.ref.kind === 'HTTPRoute' ? ' rule ' + (e.rule + 1) : ''} in wizard →</button>` : '').join('')}</div>`;
  return `<section class="panel" id="am-graph"><div class="panel-head"><h2>Connections for ${esc(target.name)}</h2></div><div class="panel-body"><p class="muted">Captured relationships. Weights apply per route rule; they are not measured traffic.</p><div class="am-map-root">${node({ref: target, state: 'na', exists: true})}</div><div class="am-map-branches">${graph.groups.map(g => `<section class="am-map-branch"><h3>${esc(g.title)}</h3><p class="muted">${esc(g.relation)}</p>${g.nodes.map(node).join('') || '<p class="muted">None captured.</p>'}</section>`).join('')}</div></div></section>` + amEditors();
}
const amTaskText={
  explain:['Traffic repair','Review the caller access repair and its scope.','Access'],
  'require-waypoint':['Require waypoint','Only admit the captured waypoint service account at ztunnel.','Access'],
  jwt:['Require a JWT','Validate tokens and restrict request principals at the Istio waypoint.','Access'],
  retries:['Retry failed requests','Set retry attempts when the installed experimental schema permits it.','Resilience'],
  mirroring:['Mirror requests','Copy requests to a captured Service; ignore its responses.','Routing'],
  'header-routing':['Route by header','Send requests matching exact HTTP headers to a selected backend.','Routing'],
  'global-service':['Publish a global Service','Review the export label for this Service in the selected cluster.','Multicluster'],
  'l4-default-deny':['Deny by default','Require an explicit allow policy for this workload.','Access'],
  'l4-allow-namespace':['Allow a namespace','Permit callers from a selected namespace.','Access'],
  'strict-mtls':['Set mutual TLS','Choose how this workload accepts connections.','Access'],
  methods:['Control HTTP methods','Allow or deny selected methods at the waypoint.','Access'],
  'access-rule':['Allow or deny requests','Write your own rules: calling namespaces or Services, headers, paths and methods.','Access'],
  'weighted-routing':['Split traffic','Send requests to two or more backend Services.','Routing'],
  timeouts:['Set a timeout','Limit how long a request can take.','Routing'],
  'load-balancing':['Balance endpoints','Choose how the waypoint selects healthy endpoints.','Routing'],
  'endpoint-failover':['Eject failing endpoints','Temporarily avoid endpoints returning errors.','Resilience'],
  'locality-failover':['Fail over by region','Choose a fallback region for this Service.','Resilience'],
  'rate-limit':['Limit request rate','Set a request budget per waypoint proxy.','Traffic limits']
};
function amTaskCards(){
  const A=S.ambient,allowed=A.edit?(A.editTasks||{HTTPRoute:['weighted-routing','timeouts'],DestinationRule:['load-balancing','endpoint-failover'],AuthorizationPolicy:['methods'],PeerAuthentication:['strict-mtls'],AgentgatewayPolicy:['rate-limit'],EnterpriseAgentgatewayPolicy:['rate-limit']}[A.edit.ref.kind]||[]):null;
  const installed=new Set(A.edit?[]:(A.result?.editable||[]).flatMap(e=>e.tasks||e.request?.tasks||[]));
  const supported=A.capabilities.filter(c=>c.supported&&!installed.has(c.id)&&(!allowed||allowed.includes(c.id))),unavailable=A.capabilities.filter(c=>!supported.includes(c)&&!installed.has(c.id));
  return `<section class="panel am-task-picker"><div class="panel-head"><div><h2>${A.edit?'Fields to change':'Add configuration'}</h2><p class="muted">${A.edit?'Choose the fields to update on this existing resource.':'Choose a new capability. Edit settings already configured from Current configuration above.'}</p></div></div>
    <div class="panel-body">${A.edit?'':amWaypointStrip(unavailable)}<div class="am-task-cards">${supported.map(c=>{const [title,desc,group]=amTaskText[c.id]||[c.title,c.reason,'Configuration'];return `<label class="am-task-card ${A.tasks[c.id]?'selected':''}"><input type="checkbox" data-amtask="${esc(c.id)}" ${A.tasks[c.id]?'checked':''}><span><small>${esc(group)} · ${esc(c.layer)}</small><strong>${esc(title)}</strong><span>${esc(desc)}</span></span></label>`;}).join('')}</div>
    ${supported.length?'':installed.size?'<p class="muted">Available capabilities are already configured. Use their Edit actions above.</p>':`<div class="notice info">No traffic templates match the captured waypoint and APIs. <button id="am-open-waypoint">Set up a waypoint</button></div>`}
    <details class="am-unavailable"><summary>${unavailable.length} other tasks · why unavailable?</summary>${unavailable.map(c=>`<div><strong>${esc(c.title)}</strong><p>${esc(allowed&&!allowed.includes(c.id)?'This task edits another configuration. Return to Current configuration to choose it.':c.reason)}</p>${amNeedsIstioWaypoint(c)?'<button data-amwaypoint="new" data-amwpistio="1">Deploy an Istio waypoint…</button> <button data-amwaypoint="existing">Use an existing waypoint…</button>':''}</div>`).join('')}</details>
    <div class="am-wizard-footer"><button id="am-change-target">Change target</button><button id="am-next-options" class="primary" ${Object.values(A.tasks).some(Boolean)?'':'disabled'}>${A.edit?'Edit selected fields':'Configure selected tasks'} →</button></div></div></section>`;
}
function amEditors() {
  return `<section class="panel mt am-existing"><div class="panel-head"><div><h2>Existing configuration</h2><p class="muted">Edit captured settings on the original resource. Other fields and attachments are retained.</p></div></div><div class="table-scroll"><table><thead><tr><th>Object</th><th>Scope</th><th>Captured settings and editing support</th><th></th></tr></thead><tbody>${(S.ambient.result.editable || []).map((e, i) => `<tr><td><button class="link" data-amevidence="${attr(e.ref)}">${esc(e.ref.kind)} ${esc(e.ref.namespace)}/${esc(e.ref.name)}</button></td><td>${e.ref.kind === 'HTTPRoute' ? 'Rule ' + (e.rule + 1) : esc(e.ref.namespace)}</td><td>${e.request?.tasks?.length ? `<strong>${e.request.tasks.map(t=>esc(amTaskText[t]?.[0]||t)).join(' · ')}</strong><br><span class="am-config-values">${esc(amConfigurationSummary(e))}</span><br>` : ''}${esc(e.reason)}</td><td><button data-amedit="${i}" ${e.supported ? '' : 'disabled'}>Edit existing</button></td></tr>`).join('') || '<tr><td colspan="4">No existing configuration captured for this target. Refresh inventory if you have just applied a change.</td></tr>'}</tbody></table></div></section>`;
}
function amConfigurationSummary(entry){
  const r=entry.request;if(!r)return entry.reason||'';
  const values=[];
  if(r.backends?.length){const total=r.backends.reduce((n,b)=>n+(r.trafficMode==='equal'?1:b.weight),0);values.push(r.backends.map(b=>`${b.name}:${b.port}${r.backends.length>1?` ${total?Math.round(100*(r.trafficMode==='equal'?1:b.weight)/total)+'%':'weight '+b.weight}`:''}`).join(' · '));}
  if(r.timeout)values.push('Timeout '+r.timeout);
  if(r.methods?.length)values.push((r.methodAction||'ALLOW')+' '+r.methods.join(', '));
  if(r.algorithm)values.push(r.algorithm);
  if(r.requests)values.push(`${r.requests} requests / ${r.unit}`);
  if(r.failureThreshold)values.push('Eject after '+r.failureThreshold+' failures');
  return values.join(' · ')||r.tasks.map(t=>amTaskText[t]?.[0]||t).join(' · ');
}
function amEditKey(edit){return JSON.stringify([edit.ref.cluster,edit.ref.group,edit.ref.kind,edit.ref.namespace,edit.ref.name,edit.rule]);}
function amSaveConfigurationDraft(){
  const A=S.ambient;amCapture();
  if(A.edit){A.editDrafts??={};A.editDrafts[amEditKey(A.edit)]=JSON.parse(JSON.stringify({settings:A.settings,tasks:A.tasks}));}
  else if(A.mode||Object.values(A.tasks).some(Boolean))A.taskDraft=JSON.parse(JSON.stringify({settings:A.settings,tasks:A.tasks,mode:A.mode,repairRef:A.repairRef}));
}
function amShowConfigurations(){amSaveConfigurationDraft();amResetDraft(true);S.ambient.tab='wizard';}
function amResumeTaskDraft(){const A=S.ambient;if(!A.taskDraft)return;const draft=JSON.parse(JSON.stringify(A.taskDraft));amResetDraft(true);Object.assign(A,draft,{tab:'wizard',phase:2});}
function amOpenEdit(entry,task) {
  if(!entry?.supported || !entry.request)return;
  amSaveConfigurationDraft();
  // Copy the captured request: changing a draft must not change the defaults used
  // when this resource is opened again, even before the next inventory refresh.
  const req=JSON.parse(JSON.stringify(entry.request));
  amResetDraft(true);const A=S.ambient;
  A.key=JSON.stringify([req.cluster,req.namespace,req.service]);A.kind='Service';
  A.tab='wizard';A.phase=2;A.edit=req.edit;
  A.editTasks=entry.ref.kind==='HTTPRoute'?['weighted-routing','timeouts']:entry.ref.kind==='DestinationRule'?['load-balancing','endpoint-failover']:req.tasks;
  A.tasks=Object.fromEntries(req.tasks.map(t=>[t,true]));
  A.settings={...req,methods:req.methods?.join(',')};
  const draft=A.editDrafts?.[amEditKey(req.edit)];if(draft){const saved=JSON.parse(JSON.stringify(draft));A.tasks=saved.tasks;A.settings=saved.settings;}
  if(task&&req.tasks.includes(task))A.tasks={[task]:true};
}

function amWizard(svc) {
  const A = S.ambient, F = A.settings, selected = id => !!A.tasks[id];
  const others = A.inventory.services.filter(s => s.cluster === svc.cluster && s.namespace === svc.namespace), namespaces = A.inventory.namespaces.filter(n => n.cluster === svc.cluster).map(n => n.name);
  const backends = F.backends || [svc, others.find(s => s.name !== svc.name) || svc].map((s, i) => ({name: s.name, port: s.ports[0], weight: i ? 10 : 90}));
  const edit = A.edit ? `<div class="notice info"><div><strong>Editing ${esc(A.edit.ref.kind)} ${esc(A.edit.ref.namespace)}/${esc(A.edit.ref.name)}${A.edit.ref.kind === 'HTTPRoute' ? ' · rule ' + (A.edit.rule + 1) : ''}</strong><p>Existing attachments and unedited fields are retained. The preview shows the exact existing-field changes.</p><button data-amconfig-home>← All configurations for this Service</button></div></div>` : '';
  if(A.phase===1){
    if(A.edit)return edit+amTaskCards();
    return amConfigurationHome(svc);
  }
  const tasks=Object.keys(A.tasks).filter(k=>A.tasks[k]),names=tasks.map(k=>amTaskText[k]?.[0]||k),hasPortOptions=tasks.some(k=>['methods','weighted-routing','timeouts','rate-limit','jwt','retries','mirroring','header-routing'].includes(k));
  const globalCurrent=selected('global-service')&&!A.edit&&amGlobalPublished(svc),globalEdits=globalCurrent?amGlobalPendingEdits(svc):[],globalFrom=globalCurrent?amGlobalTargets(svc).filter(r=>r.global).map(r=>r.cluster):[];
  const fields = `<section class="panel am-options"><div class="panel-head"><div><h2>${esc(globalCurrent?'Global Service · published from '+globalFrom.length+' cluster'+(globalFrom.length===1?'':'s'):A.edit?amEditHeading(tasks):names.length===1?names[0]:'Configure selected tasks')}</h2><p class="muted">${esc(globalCurrent?'This Service is already global. Check what is live, open its YAML, or change which clusters publish it.':A.edit?'Loaded from the selected resource. Review only the fields you change.':names.length===1?amTaskText[tasks[0]]?.[1]||'Choose the settings for this task.':'Set the options below, then review the generated changes.')}</p></div><button id="am-edit-tasks">${A.edit?'Edit other fields':'Change tasks'}</button></div><div class="panel-body" id="am-parameters"><div class="am-fields">${hasPortOptions?`<label>Service port <select id="am-port">${amOpts(svc.ports, F.port || svc.ports[0])}</select></label>`:''}
    ${globalCurrent?amGlobalCurrentConfig(svc):selected('global-service')?amGlobalClusterChoices(svc):''}
    ${selected('global-service')&&!globalCurrent?`<div class="am-global-publication"><h3>Publication change</h3><p>The reviewed change sets <code>solo.io/service-scope: global</code> on each selected existing Service. Other labels, selectors, ports and service-takeover settings are preserved.</p><p>Each cluster gets an independent plan and explicit kubeconfig context. Peering and a supported Enterprise Istio licence must already be configured.</p>${amDNSMarkup(A.result.dns)}<p>The controller chooses the global hostname. Refresh after applying to see its published names.</p></div>`:''}
    ${selected('access-rule') ? amAccessRuleMarkup(svc) : ''}
    ${selected('l4-allow-namespace') ? `<label>Source namespace <select id="am-source">${amOpts(namespaces, F.sourceNamespace)}</select></label>` : ''}
    ${selected('strict-mtls') ? `<fieldset class="am-choice-group"><legend>Which connections should this workload accept?</legend>${[['STRICT','Require mesh mTLS','Only callers with a mesh-authenticated connection can reach these Pods.'],['PERMISSIVE','Allow non-mesh callers too','Accept plaintext connections as well as mesh mTLS.']].map(([value,label,detail])=>`<label class="am-choice"><input type="radio" name="am-mtls-mode" value="${value}" ${(F.mtlsMode||'STRICT')===value?'checked':''}><span><strong>${label}</strong><small>${detail}</small><code>${value}</code></span></label>`).join('')}</fieldset>` : ''}
    ${selected('methods') ? `<label>Method rule <select id="am-methodAction">${amOpts([{value:'ALLOW',label:'Allow only selected methods (additive ALLOW policy)'},{value:'DENY',label:'Deny selected methods'}], F.methodAction || 'ALLOW')}</select></label>` + amInput('methods', 'HTTP methods (comma separated)', F.methods ?? 'GET,HEAD') : ''}
    ${selected('timeouts') ? amInput('timeout', 'Request timeout (0s disables)', F.timeout ?? '5s') + amInput('backendTimeout', 'Backend timeout (optional)', F.backendTimeout ?? '') : ''}
    ${selected('load-balancing') ? `<label>Endpoint algorithm <select id="am-algorithm">${amOpts([{value:'DEFAULT',label:'Controller default — no override'},'ROUND_ROBIN','LEAST_REQUEST','RANDOM'], F.algorithm || 'DEFAULT')}</select></label>` : ''}
    ${selected('weighted-routing') ? `<label>Service traffic distribution <select id="am-trafficMode">${amOpts([{value:'equal',label:'Default/equal — omit explicit backend weights'},{value:'weighted',label:'Explicit backend weights'}], F.trafficMode || 'equal')}</select></label><label class="row"><input id="am-relativeWeights" type="checkbox" ${F.relativeWeights ? 'checked' : ''}> Relative weights, rather than percentages totalling 100</label>` : ''}
    ${selected('endpoint-failover') || selected('locality-failover') ? amInput('failureThreshold', 'Consecutive 5xx errors', F.failureThreshold ?? 3, 'number') + amInput('ejectionInterval', 'Check interval', F.ejectionInterval ?? '5s') + amInput('ejectionDuration', 'Base ejection duration', F.ejectionDuration ?? '30s') + amInput('maxEjectionPercent', 'Maximum ejected endpoints (%)', F.maxEjectionPercent ?? 100, 'number') : ''}
    ${selected('rate-limit') ? amInput('requests', 'Requests per proxy', F.requests ?? 10, 'number') + amInput('burst', 'Burst capacity', F.burst ?? F.requests ?? 10, 'number') + `<label>Window <select id="am-unit">${amOpts(['Seconds','Minutes','Hours'], F.unit)}</select></label>` : ''}
    ${selected('locality-failover') ? `<label>From region <select id="am-fromRegion">${amOpts(svc.regions || [], F.fromRegion)}</select></label><label>Fallback region <select id="am-toRegion">${amOpts(svc.regions || [], F.toRegion)}</select></label>` : ''}</div>
    ${selected('weighted-routing') ? `<h3 class="mt">Backend Services</h3>${backends.map((b, i) => `<div class="row wrap mt" data-ambackend="${i}"><label>Backend Service<select data-amname>${amOpts(others.map(s => s.name), b.name)}</select></label><label>Service port<select data-amport>${amOpts(others.find(s=>s.name===b.name)?.ports||[],b.port)}</select></label>${(F.trafficMode || 'equal') === 'weighted' ? `<label>Weight<input data-amweight type="number" value="${esc(b.weight)}" min="0"></label>` : ''}<button data-amremove="${i}" aria-label="Remove backend ${esc(b.name)}">Remove</button></div>`).join('')}<button id="am-add-backend" class="mt">Add backend Service</button><p class="muted">Equal mode uses Gateway API's default weight of one for each chosen backend. Explicit weights are independent of the endpoint algorithm inside each Service.</p>` : ''}
    <details class="am-explainer"><summary>Scope and defaults</summary><p>Default choices omit overrides where supported. L4 selectors affect workloads and all Services targeting them. The review shows the exact resource types and fields that will change.</p></details><div class="am-wizard-footer"><button id="${A.edit?'am-current-home':'am-options-back'}">${A.edit?'← Current configuration':'← Choose tasks'}</button><button class="primary" id="am-generate" ${globalCurrent&&!globalEdits.length?'disabled title="Pick a different option for a cluster first"':''}>Review ${A.edit||globalCurrent?'changes':'configuration'} →</button></div></div></section>`;
  const workloadTasks=tasks.filter(k=>['strict-mtls','l4-default-deny','l4-allow-namespace'].includes(k));
  const scopeText=(workloadTasks.length?'Pods selected by this Service. Workload policies apply across workload ports. ':'')+(tasks.length>workloadTasks.length?'Service routing and policy resources are also listed in the review.':'');
  if(globalCurrent)return `<div class="am-config-layout">${fields}<aside class="am-config-summary"><h3>Current state</h3><p>Published from ${esc(globalFrom.join(', '))}.</p><h3>This change</h3>${globalEdits.length?`<ul>${globalEdits.map(e=>`<li>${esc(amGlobalEditLabel(e))}</li>`).join('')}</ul>`:'<p class="muted">No changes yet.</p>'}<h4>Next step</h4><p>${globalEdits.length?'Review changes runs the required dry-run per cluster before final confirmation.':'Open View object or YAML to inspect the live label, or pick a different option for a cluster to change it.'}</p><span class="am-unapplied">${globalEdits.length?'Draft · nothing applied':'Live configuration · unchanged'}</span></aside></div>`;
  return edit+`<div class="am-config-layout">${fields}<aside class="am-config-summary"><h3>This change</h3><ul>${names.map(n=>`<li>${esc(n)}</li>`).join('')}</ul><h4>Applies to</h4><p>${scopeText}</p><h4>Next step</h4><p>Review and apply runs the required dry-run before final confirmation. After saving, retest traffic from Current configuration.</p><span class="am-unapplied">Draft · nothing applied</span></aside></div>`;
}
function amWaypoint(target) {
  const A = S.ambient, F = A.settings, nsList = A.inventory.namespaces.filter(n => n.cluster === target.cluster).map(n => n.name), targetNS = target.kind === 'Namespace' ? target.name : (F.targetNamespace || target.namespace);
  const wps = A.inventory.waypoints.filter(w => w.cluster === target.cluster && ['service','all'].includes(w.traffic));
  const mode = F.wpMode || 'existing', scope = F.wpScope || (['Service','ServiceEntry'].includes(target.kind) ? 'service' : 'namespace');
  const selected = F.enrolNamespaces || [targetNS];
  const classes=(A.result.environment?.classes||[]).filter(c=>c.supported),preferred=classes.find(c=>c.name===A.result.environment?.selected?.name)||classes[0];
  const classChoices=classes.map(c=>({value:c.name,label:`${c.product==='istio'?'Istio':'agentgateway'} · ${c.edition==='unknown'?'edition unconfirmed':c.edition.toUpperCase()} (${c.name})`}));
  return `<section class="panel"><div class="panel-head"><h2>${A.mode === 'enrol-namespace' ? 'Enrol namespace in ambient' : 'Select, deploy or share a waypoint'}</h2></div><div class="panel-body" id="am-setup-parameters"><p>Shared scope enrols an explicit list of namespaces. There is no automatic global waypoint default. Object overrides remain effective unless you explicitly clear one.</p><div class="am-fields">
    ${target.kind === 'Gateway' ? `<label>Namespace to enrol <select id="am-targetNamespace">${amOpts(nsList, targetNS)}</select></label>` : ''}
    ${A.mode !== 'enrol-namespace' ? `<label>Waypoint <select id="am-wpMode">${amOpts([{value:'existing',label:'Choose an existing waypoint'},{value:'new',label:'Deploy a new waypoint'}], mode)}</select></label><label>Binding scope <select id="am-wpScope">${amOpts([...(target.kind === 'Service' || target.kind === 'ServiceEntry' ? [{value:'service',label:'Only this object'}] : []),{value:'namespace',label:'Namespace default'},{value:'shared',label:'Shared across selected namespaces'}], scope)}</select></label>
    ${mode === 'existing' ? `<label>Existing waypoint <select id="am-wpExisting"><option value="">Choose a service-capable waypoint…</option>${wps.map(w => `<option value="${esc(w.namespace + '/' + w.name)}" ${(F.wpExisting || (target.kind === 'Gateway' ? target.namespace + '/' + target.name : '')) === w.namespace + '/' + w.name ? 'selected' : ''}>${esc(w.namespace)}/${esc(w.name)} · ${esc(w.class)}</option>`).join('')}</select></label>` : `<label>Waypoint namespace <select id="am-wpNamespace">${amOpts(nsList, F.wpNamespace || targetNS)}</select></label>${amInput('wpName','New waypoint name',F.wpName || 'flightbay-waypoint')}<label>Detected waypoint implementation <select id="am-wpClass">${classChoices.length?amOpts(classChoices,F.wpClass||preferred?.name):'<option value="">No supported class captured</option>'}</select><small>Only implementations observed in this cluster are offered.</small></label>`}
    ${mode === 'existing' ? `<label class="row"><input id="am-wpGrant" type="checkbox" ${F.wpGrant ? 'checked' : ''}> Extend a Same/explicit-name HBONE allowlist for selected namespaces, if required</label>` : ''}${['Service','ServiceEntry'].includes(target.kind) && scope !== 'service' ? `<label class="row"><input id="am-wpClear" type="checkbox" ${F.wpClear ? 'checked' : ''}> Clear this object's waypoint override to use the namespace default</label>` : ''}<label class="row"><input id="am-wpEnrol" type="checkbox" ${F.wpEnrol ? 'checked' : ''}> Also enrol the namespace(s) in ambient</label>` : ''}</div>
    ${scope === 'shared' && A.mode !== 'enrol-namespace' ? `<h3>Namespaces to enrol</h3><div class="row"><button id="am-enrol-all">Select all captured namespaces (${nsList.length})</button><button id="am-enrol-none">Clear selection</button></div><div class="row wrap">${nsList.map(n => `<label><input data-amenrolns="${esc(n)}" type="checkbox" ${selected.includes(n) ? 'checked' : ''}> ${esc(n)}</label>`).join('')}</div>` : ''}
    <p class="muted">You’ll review the affected namespaces and fields before anything is applied.</p><div class="am-wizard-footer"><button id="am-back-wizard">Back to tasks</button><button class="primary" id="am-generate-setup">Review configuration →</button></div></div></section>`;
}
function amRecovery(svc) {
  const A = S.ambient, R = A.result.recovery, F = A.settings;
  if(A.mode==='route-parent')return amParentRecovery(svc);
  if (A.mode === 'route-parent') return `<section class="panel"><div class="panel-head"><h2>Repair an HTTPRoute parent</h2></div><div class="panel-body" id="am-repair-parameters"><p>The chosen Service is explicit frontend intent. Gateway parents can be valid for other scopes. Other parents and all route rules are preserved.</p><label>HTTPRoute<select id="am-repairRoute">${amOpts(A.inventory.routes.filter(r => r.cluster === svc.cluster && r.namespace === svc.namespace).map(r => r.name), F.repairRoute || A.route)}</select></label>${amInput('parentIndex','Parent entry (zero-based)',F.parentIndex ?? 0,'number')}<label>Frontend Service port<select id="am-port">${amOpts(svc.ports,F.port || svc.ports[0])}</select></label><button id="am-generate-repair" class="primary">Generate parent change</button></div></section><div id="am-plan">${A.plan ? amPlanPanel() : ''}</div>`;
  const pods = R.pods, deployments = R.workloads;
  const op = F.repairOperation || (R.endpointState !== 'complete' ? 'evidence' : deployments.some(d => d.replicas === 0) ? 'scale-workload' : pods.length && !pods.some(p => p.selected) ? 'service-selector' : 'evidence');
  const candidates = pods.filter(p => op !== 'service-port' || p.selected), pod = candidates.find(p => p.name === F.selectedPod);
  let selector = F.selector;
  if (!selector && pod) { const preferred = ['app','app.kubernetes.io/name','app.kubernetes.io/instance']; const keys = preferred.filter(k => pod.labels[k]); selector = keys.length ? Object.fromEntries(keys.map(k => [k,pod.labels[k]])) : Object.fromEntries(Object.entries(pod.labels).filter(([k]) => !['pod-template-hash','controller-revision-hash','statefulset.kubernetes.io/pod-name','apps.kubernetes.io/pod-index','controller-uid','job-name','batch.kubernetes.io/controller-uid','batch.kubernetes.io/job-name'].includes(k))); }
  return `<section class="panel"><div class="panel-head"><h2>Endpoint recovery</h2></div><div class="panel-body" id="am-repair-parameters"><p>${esc(R.detail)}</p><p>A selector change can retarget traffic; scaling does not fix readiness, image or scheduling errors. Flightbay does not invent endpoints or declare an arbitrary workload to be the correct application.</p><label>Recovery action<select id="am-repairOperation">${amOpts([{value:'evidence',label:'Collect missing evidence / inspect readiness'},...(pods.length ? [{value:'service-selector',label:'Choose the intended workload and correct the Service selector'}] : []),...(deployments.length ? [{value:'scale-workload',label:'Set replicas on a matching Deployment'}] : []),...(pods.some(p => p.selected && p.ports.length) ? [{value:'service-port',label:'Correct the Service targetPort from a selected Pod'}] : [])],op)}</select></label>
    ${op === 'evidence' ? `<p>Collect a fresh, narrowly scoped file, then inspect Pod startup/readiness/events and EndpointSlice controller output. No safe cluster configuration change can be inferred from missing data alone.</p><pre class="code">${esc(R.command)}</pre><button id="am-copy-evidence">Copy capture command</button>` : ''}
    ${['service-selector','service-port'].includes(op) ? `<label>Intended captured Pod<select id="am-selectedPod"><option value="">Choose the intended workload explicitly…</option>${amOpts(candidates.map(p => ({value:p.name,label:p.name + (p.ready ? ' · Ready' : ' · not Ready') + (p.selected ? ' · already selected' : '')})),F.selectedPod)}</select></label>${op === 'service-selector' ? `<label>Selector labels (JSON)<textarea id="am-selector" rows="5">${esc(JSON.stringify(selector || {},null,2))}</textarea></label>` : `<label>Service port<select id="am-port">${amOpts(svc.ports,F.port || svc.ports[0])}</select></label><label>Declared container target port<select id="am-targetPort">${amOpts(pod?.ports || [],F.targetPort)}</select></label>`}` : ''}
    ${op === 'scale-workload' ? `<label>Matching Deployment<select id="am-workload"><option value="">Choose a matching Deployment…</option>${amOpts(deployments.map(d => ({value:d.name,label:d.name+' · '+d.replicas+' replicas'})),F.workload)}</select></label>${amInput('replicas','Desired replicas',F.replicas ?? 1,'number')}` : ''}
    <div class="table-scroll mt"><table><thead><tr><th>Pod</th><th>Selected</th><th>Ready</th><th>Declared ports</th><th></th></tr></thead><tbody>${pods.map(p => `<tr><td>${esc(p.name)}</td><td>${p.selected ? 'Yes' : 'No'}</td><td>${p.ready ? 'Yes' : 'No'}</td><td>${p.ports.map(esc).join(', ')}</td><td><button data-amevidence="${attr(p)}">Inspect Pod</button></td></tr>`).join('')}</tbody></table></div>${op !== 'evidence' ? '<button id="am-generate-repair" class="primary mt">Generate scoped recovery YAML</button>' : ''}</div></section><div id="am-plan">${A.plan ? amPlanPanel() : ''}</div>`;
}
function amPlanPanel() {
  const A = S.ambient, p = A.plan, noChange = !p.objects.length && !(p.changes || []).length;
  const fieldName=path=>path.replace(/^\/metadata\/labels\//,'Label: ').replace(/^\/spec\//,'').replaceAll('~1','/');
  const summary=`<div class="am-review-counts"><span><b>${p.objects.length}</b> to create</span><span><b>${p.changes?.length||0}</b> to update</span><span>Target <b>${esc(p.request.namespace)}/${esc(p.request.service||p.request.targetName||'')}</b></span></div>${p.objects.map(o=>`<article class="am-review-object"><span class="badge green">Create</span><div><strong>${esc(o.kind)} ${esc(o.metadata.namespace||'')}/${esc(o.metadata.name)}</strong><small>${esc(o.apiVersion)}</small></div></article>`).join('')}${(p.changes||[]).map(c=>`<details class="am-review-object am-change" open><summary><span class="badge blue">Update</span> ${esc(c.ref.kind)} <strong>${esc(c.ref.name)}</strong><small>${esc(c.reason)}</small></summary>${c.fields.map(f=>`<div class="am-diff"><strong>${esc(fieldName(f.path))}</strong><div><small>Before</small><pre class="code">${esc(f.beforePresent?JSON.stringify(f.before,null,2):'Not set')}</pre></div><div><small>After</small><pre class="code">${esc(f.afterPresent?JSON.stringify(f.after,null,2):'Remove setting')}</pre></div></div>`).join('')}</details>`).join('')}${noChange?'<p>No changes are required for these choices.</p>':''}<details class="am-explainer"><summary>Configuration notes and scope</summary>${p.notes.map(n=>`<p>${esc(n)}</p>`).join('')}</details>`;
  return `<section class="panel am-review" id="am-plan"><div class="panel-head"><div><h2>Review your configuration</h2><p class="muted">Nothing has been applied. Download the YAML or test it on a cluster.</p></div><button id="am-back-options">← Edit options</button></div><div class="am-preview-tabs"><button data-ampreview="summary" class="${A.previewTab==='summary'?'on':''}">Change summary</button><button data-ampreview="yaml" class="${A.previewTab==='yaml'?'on':''}">Generated YAML</button><span class="spacer"></span><button id="am-copy">Copy YAML</button><button id="am-download">Download YAML</button></div><div class="panel-body">${A.previewTab==='yaml'?`<pre class="code am-yaml">${esc(p.yaml)}</pre>`:summary}</div></section>
    <details class="panel am-test-panel mt" ${A.validation||A.message?'open':''}><summary><strong>Review and apply</strong><span>Automatic dry-run, then final confirmation</span></summary><div class="panel-body"><p>Review and apply checks the target cluster identity and runs any required server dry-run before opening the final confirmation. Dry-run only checks the plan without applying it.</p>${amContextControl(A.plan.request.cluster,A.contexts,A.context,'id="am-context"')}<div class="am-wizard-footer"><button id="am-validate" ${A.busy||!A.context||noChange?'disabled':''}>Dry-run only</button><button class="primary" id="am-apply" ${A.busy||!A.context||noChange?'disabled':''}>Review and apply…</button></div>${A.validation?`<p class="green">Validated for ${esc(A.validation.context)}. Review and apply will recheck if validation expires.</p>`:''}${A.message?`<p role="status">${esc(A.message)}</p>`:''}</div></details>`;
}
function amTests() { const A = S.ambient; return `<section class="panel mt"><div class="panel-head"><h2>Active tests and changes</h2></div><div class="panel-body"><p>Recorded in your local Flightbay home so they can be inspected and undone after reopening the desk.</p>${A.tests.length ? A.tests.map((t,i) => `<div class="notice info"><div><strong>${esc(t.context)}</strong> · ${esc(t.plan.request.namespace)}/${esc(t.plan.request.service || t.plan.request.targetName || '')}<div class="mono">${(t.created || []).map(o => esc('Created '+o.kind+' '+o.name)).concat((t.modified || []).map(c => esc('Changed '+c.ref.kind+' '+c.ref.name))).join('<br>')}</div><div class="row mt"><button data-amstatus="${i}">Refresh status</button><button data-amcleanup="${i}">${(t.modified || []).length ? 'Undo changes and remove test resources' : 'Remove these test resources'}</button></div></div></div>`).join('') : '<p class="muted">No active tests recorded.</p>'}${A.status ? `<pre class="code">${esc(JSON.stringify(A.status,null,2))}</pre>` : ''}</div></section>`; }

function amCapture() {
  const F = S.ambient.settings, get = id => $('#am-'+id);
  if(document.querySelector('[data-amglobaledit]'))F.globalEdits=Object.fromEntries([...document.querySelectorAll('[data-amglobaledit]:checked')].map(e=>[e.dataset.amglobaledit,e.value]));
  if(document.querySelector('[data-amglobalcluster]'))F.globalClusters=[...document.querySelectorAll('[data-amglobalcluster]:checked')].map(e=>e.dataset.amglobalcluster).filter(cluster=>S.ambient.inventory.services.some(s=>s.cluster===cluster&&s.namespace===amService()?.namespace&&s.name===amService()?.name));
  const mtls=document.querySelector('input[name="am-mtls-mode"]:checked');if(mtls)F.mtlsMode=mtls.value;
  if($('#am-access'))amCaptureAccessRule();
  const text = ['source','methods','timeout','backendTimeout','algorithm','unit','fromRegion','toRegion','trafficMode','mtlsMode','methodAction','wpMode','wpScope','wpExisting','wpNamespace','wpName','wpClass','targetNamespace','repairOperation','selectedPod','workload','repairRoute'];
  text.push('parentKind','parentGateway','parentSection');
  text.push('issuer','jwksUri','jwks','requestPrincipals','paths','jwtMethods','p1Headers','mirrorService','headerService','explainPorts','explainPaths');
  for (const k of text) if (get(k)) F[k === 'source' ? 'sourceNamespace' : k] = get(k).value;
  for (const k of ['port','requests','burst','failureThreshold','maxEjectionPercent','targetPort','replicas','parentIndex']) if (get(k)) F[k] = Number(get(k).value);
  for(const k of ['retryAttempts','mirrorPort','headerPort'])if(get(k))F[k]=Number(get(k).value);
  for (const k of ['relativeWeights','wpGrant','wpClear','wpEnrol']) if (get(k)) F[k] = get(k).checked;
  for (const k of ['ejectionInterval','ejectionDuration']) if (get(k)) F[k] = get(k).value;
  if ($('#am-selector')) F.selector = JSON.parse($('#am-selector').value);
  if (document.querySelector('[data-ambackend]')) F.backends = [...document.querySelectorAll('[data-ambackend]')].map((r,i) => ({name:r.querySelector('[data-amname]').value,port:Number(r.querySelector('[data-amport]').value),weight:r.querySelector('[data-amweight]') ? Number(r.querySelector('[data-amweight]').value) : (F.backends?.[i]?.weight ?? 1)}));
  if (document.querySelector('[data-amenrolns]')) F.enrolNamespaces = [...document.querySelectorAll('[data-amenrolns]:checked')].map(e => e.dataset.amenrolns);
}
function amRead() {
  amCapture(); const A = S.ambient, F = A.settings, o = amTarget(), s = amService();
  if(F.explain)return {cluster:s.cluster,namespace:s.namespace,service:s.name,tasks:['explain'],explain:{...F.explain,ports:(F.explainPorts??F.explain.ports?.join(',')??'').split(',').map(x=>x.trim()).filter(Boolean),paths:(F.explainPaths??F.explain.paths?.join(',')??'').split(',').map(x=>x.trim()).filter(Boolean)}};
  if(A.mode==='route-parent'&&(F.parentKind||'Gateway')==='Gateway'){
    const parent=A.inventory.objects.find(o=>o.cluster===s.cluster&&o.kind==='Gateway'&&o.namespace+'/'+o.name===F.parentGateway);
    if(!parent)throw new Error('Choose the intended existing Gateway in this cluster.');
    return {cluster:s.cluster,namespace:s.namespace,service:s.name,tasks:['route-parent'],route:F.repairRoute||A.route,parent,parentSection:F.parentSection||'',edit:{ref:A.repairRef,rule:F.parentIndex??0}};
  }
  if(A.mode==='waypoint'&&F.wpMode==='new'&&!F.wpClass)throw new Error('No supported waypoint implementation was detected. Refresh or capture the GatewayClasses and deployed Gateways first.');
  if (['waypoint','enrol-namespace'].includes(A.mode)) { const ns = o.kind === 'Namespace' ? o.name : (F.targetNamespace || o.namespace); const [wn, name] = (F.wpExisting || (o.kind === 'Gateway' ? o.namespace+'/'+o.name : '')).split('/'); return {cluster:o.cluster,namespace:ns,service:s?.name || '',targetKind:o.kind === 'Gateway' ? 'Namespace' : o.kind,targetName:o.kind === 'Gateway' ? ns : o.name,tasks:[A.mode],...(A.mode === 'waypoint' ? {waypoint:{mode:F.wpMode || 'existing',scope:F.wpScope || (['Service','ServiceEntry'].includes(o.kind) ? 'service' : 'namespace'),namespace:(F.wpMode || 'existing') === 'existing' ? wn : (F.wpNamespace || ns),name:(F.wpMode || 'existing') === 'existing' ? name : (F.wpName || 'flightbay-waypoint'),class:F.wpClass || 'istio-waypoint',namespaces:F.enrolNamespaces || [ns],grantAccess:!!F.wpGrant,clearOverride:!!F.wpClear,enrolAmbient:!!F.wpEnrol}} : {})}; }
  if (A.mode === 'route-parent') return {cluster:s.cluster,namespace:s.namespace,service:s.name,port:F.port || s.ports[0],tasks:['route-parent'],route:F.repairRoute || A.route,edit:{ref:A.repairRef || {cluster:s.cluster,namespace:s.namespace,kind:'HTTPRoute',group:'gateway.networking.k8s.io',name:F.repairRoute || A.route,resource:'httproutes'},rule:F.parentIndex || 0}};
  if (A.mode === 'endpoints') return {cluster:s.cluster,namespace:s.namespace,service:s.name,port:F.port || s.ports[0],tasks:[F.repairOperation],selectedPod:F.selectedPod || '',selector:F.selector || {},workload:F.workload || '',replicas:F.replicas ?? 1,targetPort:F.targetPort || 0};
  const req = {cluster:s.cluster,namespace:s.namespace,service:s.name,port:F.port || s.ports[0],tasks:Object.keys(A.tasks).filter(k => A.tasks[k])};
  for (const k of ['sourceNamespace','timeout','backendTimeout','algorithm','requests','unit','burst','trafficMode','relativeWeights','mtlsMode','methodAction','failureThreshold','ejectionInterval','ejectionDuration','maxEjectionPercent','fromRegion','toRegion']) if (F[k] !== undefined && F[k] !== '') req[k] = F[k];
  if (A.tasks['access-rule']) req.access = amAccessRequest();
  if (A.tasks.methods) req.methods = (F.methods || '').split(',').map(x => x.trim()).filter(Boolean);
  if (A.tasks['weighted-routing']) req.backends = F.backends;
  if (A.edit) req.edit = A.edit;
  if(A.tasks.jwt){req.issuer=F.issuer;req.jwksUri=F.jwksUri;req.jwks=F.jwks;req.requestPrincipals=(F.requestPrincipals||'').split(',').map(x=>x.trim()).filter(Boolean);req.paths=(F.paths||'').split(',').map(x=>x.trim()).filter(Boolean);req.methods=(F.jwtMethods||'').split(',').map(x=>x.trim()).filter(Boolean);}
  if(A.tasks.jwt||A.tasks['header-routing'])req.headers=JSON.parse(F.p1Headers||'{}');
  if(A.tasks.retries)req.retryAttempts=F.retryAttempts;
  if(A.tasks.mirroring)req.mirror={name:F.mirrorService,port:F.mirrorPort};
  if(A.tasks['header-routing']&&!A.tasks['weighted-routing'])req.backends=[{name:F.headerService,port:F.headerPort}];
  return req;
}
async function amAction(fn,label='Working on the selected task…') { const A = S.ambient; if (A.busy) return; A.busy = true; A.error = ''; $('#am-workspace')?.insertAdjacentHTML('afterbegin',`<div id="am-working" class="notice" role="status" aria-live="polite" aria-busy="true"><span class="bd-spinner" aria-hidden="true"></span><strong>${esc(label)}</strong></div>`); document.querySelectorAll('#main button, #main input, #main select, #main textarea').forEach(e => e.disabled = true); try { await fn(); } catch (e) { A.error = e.message; if (e.body?.test) A.tests = await api('api/ambient/tests').catch(() => A.tests); } finally { A.busy = false; await render(); } }
async function amTestOperation(operation, rec) {
  const A = S.ambient;
  const result = await apiPost('api/ambient/test',{operation,context:rec?.context || A.context,validation:rec?.token || A.validation?.token || '',...(operation === 'validate' ? {request:A.plan.request,planId:A.plan.id} : {})});
  if (operation === 'validate') A.validation = result;
  else if (operation === 'status') A.status = result;
  else {
    A.message = result.message;
    A.validation = null;
    A.tests = await api('api/ambient/tests');
    if(operation==='cleanup'&&A.globalBatch){
      // An applied row is no longer current after undo. Re-read publication
      // evidence before offering another independent plan for these clusters.
      A.globalBatch=null;A.inventory=null;A.status=null;A.phase=2;
      toast(result.message);
    }
  }
}
function amWirePicker() {
  const A = S.ambient, input = $('#am-object-search'), list = $('#am-object-options'); let matches = [], active = 0;
  const close = () => { list.classList.add('hidden'); input.setAttribute('aria-expanded','false'); input.removeAttribute('aria-activedescendant'); };
  const draw = text => { const all = A.objects.filter(o => (!A.kind || o.kind === A.kind) && amLabel(o).toLowerCase().includes(text.toLowerCase())); matches = all.slice(0,80); active = Math.min(active,Math.max(0,matches.length-1)); list.innerHTML = matches.map((o,i) => `<button type="button" role="option" id="am-option-${i}" aria-selected="${i===active}" data-ampick="${i}">${esc(amLabel(o))}</button>`).join('') + (all.length>80 ? '<small>Type more to narrow these results.</small>' : !matches.length ? '<small>No captured objects match.</small>' : ''); list.classList.remove('hidden'); input.setAttribute('aria-expanded','true'); if(matches.length) input.setAttribute('aria-activedescendant','am-option-'+active); list.querySelectorAll('[data-ampick]').forEach(b => { b.onmousedown = e => e.preventDefault(); b.onclick = () => amChoose(matches[Number(b.dataset.ampick)]); }); };
  input.onfocus = () => { input.select(); active=0; draw(''); }; input.oninput = () => { active=0; draw(input.value); }; input.onblur = () => setTimeout(close,120); input.onkeydown = e => { if (e.key==='Escape') { close(); e.stopPropagation(); } else if (e.key==='ArrowDown'||e.key==='ArrowUp') { e.preventDefault(); active=Math.max(0,Math.min(matches.length-1,active+(e.key==='ArrowDown'?1:-1))); draw(input.value===amLabel(amTarget() || {})?'':input.value); $('#am-option-'+active)?.scrollIntoView({block:'nearest'}); } else if (e.key==='Enter') { e.preventDefault(); if(matches[active]) amChoose(matches[active]); } };
  $('#am-kind').onchange = e => {const kind=e.target.value;if(amTarget()&&kind&&kind!==amTarget().kind){amResetDraft();A.key='';A.result=null;history.replaceState(null,'','#ambient');}A.kind=kind;render().then(()=>$('#am-object-search')?.focus());};
}
after.ambient = () => {
  if($('#am-bookinfo'))$('#am-bookinfo').onclick=()=>{S.ambient.tab='demo';nav('ambient?tab=demo');};
  if(S.ambient.tab==='demo'){bookinfoAfter();return;}
  if(S.ambient.tab==='path'){rpAfter();return;}
  if(!$('#am-target-selector'))return;
  const A = S.ambient; amWirePicker();
  amP1Fields();
  const createSection=document.querySelector('#am-add-config');if(createSection)createSection.ontoggle=()=>A.createOpen=createSection.open;
  if(amTarget()?.kind==='Service')document.querySelector('.am-target-actions')?.insertAdjacentHTML('beforeend','<button id="am-trace-path">Trace request path →</button>');
  if($('#am-trace-path'))$('#am-trace-path').onclick=()=>rpOpen({destination:amTarget()});
  document.querySelectorAll('.am-canvas-card[data-map-object]').forEach(card=>{const ref=JSON.parse(card.dataset.mapObject);if(ref.kind==='Service'){const b=document.createElement('button');b.textContent='Request path';b.onclick=()=>rpOpen({destination:ref});card.querySelector('.row.wrap')?.appendChild(b);}});
  if($('#am-workspace'))$('#am-workspace').dataset.amScope=amViewScope();
  document.querySelectorAll('.am-check').forEach((el,i)=>el.dataset.amStep=A.result.steps[i].id);
  if(A.pendingStep)A.checkFilter='all';
  if(amTarget()){
    $('#am-start-new')?.insertAdjacentHTML('beforebegin','<button id="am-show-issues">All issues</button>');
    $('#am-show-issues').onclick=()=>{A.tab='investigate';amClear();};
  }
  amWireClusterCanvas();
  amWireGlobalPublication();
  amWireAccessRule();
  const testPanel=document.querySelector('.am-test-panel');if(testPanel){if(A.testOpen)testPanel.open=true;testPanel.ontoggle=()=>A.testOpen=testPanel.open;}
  if($('#am-apply'))$('#am-apply').textContent='Review and apply…';
  amWireStatusFilters();
  if(A.result?.dns&&amTarget()?.kind==='Service')document.querySelector('.am-target-actions')?.insertAdjacentHTML('beforeend',`<details class="am-implementation"><summary>DNS names</summary><div class="am-implementation-body">${amDNSMarkup(A.result.dns)}</div></details>`);
  if(amTarget()?.kind==='Service')document.querySelector('.am-target-actions')?.insertAdjacentHTML('beforeend',`<button data-amhttp="${attr(amTarget())}">Test service</button>`);
  document.querySelectorAll('.am-canvas-card[data-map-object]').forEach(card=>{const ref=JSON.parse(card.dataset.mapObject);if(ref.kind==='Service')card.querySelector('.row.wrap')?.insertAdjacentHTML('beforeend',`<button data-amhttp="${attr(ref)}">Test</button>`);});
  document.querySelectorAll('[data-amhttp]').forEach(b=>b.onclick=()=>amOpenHTTPTest(JSON.parse(b.dataset.amhttp)));
  document.querySelectorAll('[data-amcopydns]').forEach(b=>b.onclick=()=>navigator.clipboard.writeText(b.dataset.amcopydns).then(()=>toast('DNS name copied'),()=>toast('Clipboard unavailable')));
  $('#am-target-toggle').onclick=()=>{try{amCapture();}catch(e){toast(e.message);return;}A.pickerOpen=!amTarget()||$('#am-target-selector').hidden;render().then(()=>{if(A.pickerOpen)$('#am-object-search')?.focus();});};
  document.querySelectorAll('input[name="am-mtls-mode"]').forEach(e=>e.addEventListener('change',()=>{amCapture();amInvalidate();}));
  const policyBadge=document.querySelector('.am-environment .badge');if(policyBadge){policyBadge.textContent=policyBadge.textContent.replace(/^YAML:/,'Generated policy type:');policyBadge.hidden=A.tab!=='wizard';}
  document.querySelectorAll('.am-evidence>summary').forEach(e=>e.textContent='Check details · captured fields');
  document.querySelectorAll('.am-check').forEach((el,i)=>{const step=A.result.steps[i];el.querySelector('.panel-body>p')?.insertAdjacentHTML('afterend',amFixGuide(step.suggestedFix));el.querySelector('.panel-body')?.insertAdjacentHTML('beforeend',amRunbook(step.runbook)+amOfficial(step.docs));});
  document.querySelectorAll('[data-amcopycommand]').forEach(b=>b.onclick=()=>copyTerminalCommand(b.dataset.amcopycommand,b));
  document.querySelectorAll('[data-amruncommand]').forEach(b=>b.onclick=()=>termRun(b.dataset.amruncommand));
  document.querySelectorAll('[data-amissue]').forEach(b=>b.onclick=()=>{const issues=(A.inventory.issues||[]).filter(x=>!S.cluster||x.subject.cluster===S.cluster),issue=issues[Number(b.dataset.amissue)];A.tab='investigate';A.pendingStep=issue.step.id;amChoose(issue.subject);});
  if(A.pendingStep&&A.tab==='investigate'&&amTarget()){const i=A.result.steps.findIndex(s=>s.id===A.pendingStep);A.pendingStep=null;if(i>=0){const step=$('#am-step-'+i);if(step){step.open=true;step.scrollIntoView({block:'start'});}}}
  document.querySelectorAll('.am-task-card').forEach(el=>{const id=el.querySelector('[data-amtask]')?.dataset.amtask;const holder=document.createElement('article');holder.className='am-task-option';el.replaceWith(holder);holder.appendChild(el);holder.insertAdjacentHTML('beforeend',amTaskLinks([id]));});
  if(A.tab==='wizard'&&A.phase===1&&!$('#am-open-waypoint'))$('#am-change-target')?.insertAdjacentHTML('afterend','<button id="am-open-waypoint">Waypoint setup</button>');
  document.querySelectorAll('.am-unavailable>div').forEach(el=>{const task=A.capabilities?.find(c=>c.title===el.querySelector('strong')?.textContent);if(task)el.insertAdjacentHTML('beforeend',amTaskLinks([task.id]));});
  if(A.tab==='wizard'&&A.phase===3) $('#am-plan .panel-body')?.insertAdjacentHTML('beforeend',amTaskLinks(A.mode?[A.mode]:Object.keys(A.tasks).filter(k=>A.tasks[k])));
  if(A.tab==='wizard'&&A.phase===2){const box=$('#am-parameters')||$('#am-setup-parameters')||$('#am-repair-parameters');let docs=amTaskLinks(A.mode?[A.mode]:Object.keys(A.tasks).filter(k=>A.tasks[k]));if(A.mode==='waypoint'&&A.settings.wpMode==='new'){const cls=$('#am-wpClass')?.value;docs=amOfficial(A.result.environment?.classDocs?.[cls]);}box?.insertAdjacentHTML('beforeend',docs);}
  $('#am-clear').onclick=()=>amClear();$('#am-start-new').onclick=()=>amClear(true);
  $('#am-refresh').onclick = () => {A.inventory=null;A.contexts=null;A.editDrafts={};A.taskDraft=null;amInvalidate();if(A.phase===3)A.phase=2;render();};
   document.querySelectorAll('[data-amintent]').forEach(b=>b.onclick=()=>{A.kind=b.dataset.amintent;A.pickerOpen=true;render().then(()=>$('#am-object-search')?.focus());});
   document.querySelectorAll('[data-amphase]').forEach(b=>b.onclick=()=>{const phase=Number(b.dataset.amphase);if(phase===0){amClear(true);return;}try{if(phase===1){amShowConfigurations();render();return;}amCapture();}catch(e){toast(e.message);return;}A.phase=phase;render();});
  document.querySelectorAll('[data-amconfig-home]').forEach(b=>b.onclick=()=>{try{amShowConfigurations();render();}catch(e){toast(e.message);}});
  document.querySelectorAll('[data-amresume-task]').forEach(b=>b.onclick=()=>{amResumeTaskDraft();render();});
  for(const id of ['am-edit-tasks','am-options-back'])if($('#'+id))$('#'+id).onclick=()=>{amCapture();if(A.settings.explain)amResetDraft();A.phase=1;render();};
  if($('#am-change-target'))$('#am-change-target').onclick=()=>amClear(true);
  if($('#am-next-options'))$('#am-next-options').onclick=()=>{A.phase=2;render().then(()=>$('#am-workspace')?.scrollIntoView({block:'start'}));};
  if($('#am-back-options'))$('#am-back-options').onclick=()=>{A.phase=2;render();};
  document.querySelectorAll('[data-ampreview]').forEach(b=>b.onclick=()=>{A.previewTab=b.dataset.ampreview;render();});
  const filterServices=()=>{const q=(A.serviceFilter||'').toLowerCase();let count=0;document.querySelectorAll('[data-amservicecard]').forEach(card=>{const show=card.dataset.amservicecard.includes(q);card.hidden=!show;if(show)count++;});if($('#am-service-count'))$('#am-service-count').textContent=`Showing ${count} of ${A.result.services.length}`;$('#am-service-empty')?.classList.toggle('hidden',count>0);};
  if($('#am-service-filter')){$('#am-service-filter').oninput=e=>{A.serviceFilter=e.target.value;filterServices();};filterServices();}
  if($('#am-filter-clear'))$('#am-filter-clear').onclick=()=>{A.serviceFilter='';$('#am-service-filter').value='';filterServices();};
  document.querySelectorAll('[data-amdrill]').forEach(b=>b.onclick=()=>{A.tab='map';amChoose(JSON.parse(b.dataset.amdrill));});
  document.querySelectorAll('[data-amconfigure]').forEach(b=>b.onclick=()=>{A.tab='wizard';amChoose(JSON.parse(b.dataset.amconfigure));});
  document.querySelectorAll('[data-amtab]').forEach(b => b.onclick = () => {try{amCapture();}catch{}A.tab=b.dataset.amtab;A.error='';render();});
  if ($('#am-route')) $('#am-route').onchange = e => { A.route=e.target.value; render(); };
  document.querySelectorAll('[data-amevidence]').forEach(b => b.onclick = () => openObject(JSON.parse(b.dataset.amevidence),b.textContent.trim()==='YAML'?'yaml':'summary'));
  document.querySelectorAll('[data-amyaml]').forEach(b => b.onclick = () => openObject(JSON.parse(b.dataset.amyaml),'yaml'));
  document.querySelectorAll('[data-amrelated]').forEach(b => b.onclick = () => { const ref=JSON.parse(b.dataset.amrelated);if(b.hasAttribute('data-aminvestigate'))A.tab='investigate'; if(A.objects.some(o=>amKey(o)===amKey(ref))) amChoose(ref); else openObject(ref); });
  document.querySelectorAll('[data-amjump]').forEach(b=>b.onclick=()=>{const step=$('#am-step-'+b.dataset.amjump);if(step){step.open=true;step.scrollIntoView({block:'start'});}});
  document.querySelectorAll('[data-amfix]').forEach(b => b.onclick = () => amDownload(A.result.steps[Number(b.dataset.amfix)].fixYaml,'ambient-parent-suggestion.yaml'));
   const openMode=mode=>{try{amSaveConfigurationDraft();amResetDraft(true);A.tab='wizard';A.mode=mode;A.phase=2;render();}catch(e){toast(e.message);}};
  if ($('#am-open-waypoint')) $('#am-open-waypoint').onclick = () => openMode('waypoint');
  // Open Waypoint setup already on "use existing" or "deploy new", so the choice is one click.
  document.querySelectorAll('[data-amwaypoint]').forEach(b=>b.onclick=()=>{const istio=b.dataset.amwpistio?(A.result?.environment?.classes||[]).find(c=>c.product==='istio'&&c.supported)?.name:'';openMode('waypoint');A.settings.wpMode=b.dataset.amwaypoint;if(istio&&b.dataset.amwaypoint==='new')A.settings.wpClass=istio;render();});
  if ($('#am-open-enrol')) $('#am-open-enrol').onclick = () => openMode('enrol-namespace');
  if ($('#am-enrol-all')) $('#am-enrol-all').onclick = () => { amCapture(); const o=amTarget(); A.settings.enrolNamespaces=A.inventory.namespaces.filter(n=>n.cluster===o.cluster).map(n=>n.name); amInvalidate(); render(); };
  if ($('#am-enrol-none')) $('#am-enrol-none').onclick = () => { amCapture(); A.settings.enrolNamespaces=[]; amInvalidate(); render(); };
  document.querySelectorAll('[data-amremedy]').forEach(b=>b.onclick=()=>{const a=JSON.parse(b.dataset.amremedy),step=A.result.steps[Number(b.closest('.am-check')?.id.replace('am-step-',''))],field=step?.evidence?.find(e=>e.ref.kind==='HTTPRoute')?.field,match=field?.match(/parentRefs\[(\d+)\]/);amResetDraft();A.tab='wizard';if(a.id==='require-waypoint')A.tasks[a.id]=true;else A.mode=a.id;A.phase=2;if(a.ref){A.repairRef=a.ref;A.settings.repairRoute=a.ref.name;A.settings.parentIndex=match?Number(match[1]):0;}render();});
  document.querySelectorAll('[data-amedit]').forEach(b=>b.onclick=()=>{try{amOpenEdit(A.result.editable[Number(b.dataset.amedit)]);render();}catch(e){toast(e.message);}});
  for(const id of ['am-new-plan','am-back-wizard','am-current-home'])if($('#'+id))$('#'+id).onclick=()=>{try{amShowConfigurations();render();}catch(e){toast(e.message);}};
  document.querySelectorAll('[data-amtask]').forEach(b => b.onchange=()=>{try{amCapture();if(b.checked&&b.dataset.amtask==='global-service')A.tasks={};else if(b.checked)delete A.tasks['global-service'];A.tasks[b.dataset.amtask]=b.checked;amInvalidate();render();}catch(e){toast(e.message);}});
  document.querySelectorAll('#am-parameters input, #am-parameters select, #am-setup-parameters input, #am-setup-parameters select, #am-repair-parameters input, #am-repair-parameters select, #am-repair-parameters textarea').forEach(e=>e.onchange=()=>{try{amCapture();amInvalidate();if(['am-trafficMode','am-wpMode','am-wpClass','am-wpExisting','am-wpScope','am-targetNamespace','am-repairOperation','am-selectedPod'].includes(e.id)){if(e.id==='am-selectedPod')delete A.settings.selector;render();}}catch(err){toast(err.message);}});
  document.querySelectorAll('[data-amname]').forEach(el=>el.onchange=()=>{amCapture();const i=Number(el.closest('[data-ambackend]').dataset.ambackend),backend=A.settings.backends[i],svc=amService(),chosen=A.inventory.services.find(s=>s.cluster===svc.cluster&&s.namespace===svc.namespace&&s.name===backend.name);if(chosen&&!chosen.ports.includes(backend.port))backend.port=chosen.ports[0];amInvalidate();render();});
  for(const id of ['am-parentKind','am-parentGateway'])if($('#'+id))$('#'+id).onchange=()=>{amCapture();amInvalidate();render();};
  for(const id of ['am-mirrorService','am-headerService'])if($('#'+id))$('#'+id).onchange=()=>{amCapture();amInvalidate();render();};
  if($('#am-add-backend')) $('#am-add-backend').onclick=()=>{amCapture();const s=amService();A.settings.backends.push({name:s.name,port:s.ports[0],weight:0});amInvalidate();render();};
  document.querySelectorAll('[data-amremove]').forEach(b=>b.onclick=()=>{amCapture();A.settings.backends.splice(Number(b.dataset.amremove),1);amInvalidate();render();});
  for(const id of ['am-generate','am-generate-setup','am-generate-repair'])if($('#'+id))$('#'+id).onclick=()=>amAction(async()=>{if(A.tasks['global-service']&&!A.mode){await amGenerateGlobalBatch();return;}const request=amRead();if(!request.edit&&!A.mode&&request.tasks.some(task=>amExistingTaskEntries(task).length)){A.phase=1;throw new Error('This capability already has a captured configuration. Choose its Edit action from Current configuration.');}A.plan=await apiPost('api/ambient/plan',request);A.validation=null;A.message='';A.phase=3;A.previewTab='summary';});
  if($('#am-copy-evidence'))$('#am-copy-evidence').onclick=()=>navigator.clipboard.writeText(A.result.recovery.command).then(()=>toast('Capture command copied'));
  if($('#am-copy'))$('#am-copy').onclick=()=>navigator.clipboard.writeText(A.plan.yaml).then(()=>toast('YAML copied'),()=>toast('Clipboard unavailable'));
  if($('#am-download'))$('#am-download').onclick=()=>amDownload(A.plan.yaml,'flightbay-ambient-'+A.plan.id+'.yaml');
  if($('#am-context'))$('#am-context').onchange=()=>{A.context=$('#am-context').value;A.validation=null;render();};
  if($('#am-validate'))$('#am-validate').onclick=()=>amAction(async()=>{A.testFeedback=null;try{await amTestOperation('validate');A.testFeedback={ok:true,label:'Dry-run passed',message:'The API server accepted this plan without saving it. Review the YAML and confirm Apply to write the changes.'};}catch(e){A.testFeedback={ok:false,label:'Dry-run failed · nothing applied',message:e.message};throw e;}});
  if($('#am-apply'))$('#am-apply').onclick=async()=>{if(A.busy)return;const entry={plan:A.plan,context:A.context,validation:A.validation};await amAction(async()=>{A.validation=null;await amValidatePlans([entry]);A.validation=entry.validation;},'Checking the preview before final confirmation…');if(A.error||!amValidated(entry)||A.plan!==entry.plan||A.context!==entry.context||!await amConfirmApply([entry]))return;return amAction(async()=>{try{await amTestOperation('apply',{context:entry.context,token:entry.validation.token});try{await amRefreshAppliedConfiguration(entry.plan);}catch(refreshError){A.currentRefreshPending=true;}A.testFeedback={ok:true,label:entry.plan.request.edit?'Existing configuration updated':'Applied',message:'Changes were saved to '+entry.context+'. Retest traffic to measure the effect, or use Active tests to undo them.'};}catch(e){A.testFeedback={ok:false,label:'Apply failed · inspect Active tests',message:e.message};throw e;}});};
  for(const [attribute,operation,label]of [['amstatus','status','Checking recorded resources on the selected cluster…'],['amcleanup','cleanup','Undoing recorded changes and waiting for deletion confirmation…']])document.querySelectorAll('[data-'+attribute+']').forEach(b=>b.onclick=()=>amAction(async()=>{A.testFeedback=null;try{await amTestOperation(operation,A.tests[Number(b.dataset[attribute])]);A.testFeedback={ok:true,label:operation==='status'?'Status refreshed':'Cleanup confirmed',message:operation==='status'?'Live resource status is shown in Active tests. This does not test request connectivity.':A.message};}catch(e){A.testFeedback={ok:false,label:operation==='status'?'Status check failed':'Cleanup incomplete',message:e.message};throw e;}},label));
  document.querySelectorAll('[data-amcleanup]').forEach(b=>{if(A.tests[Number(b.dataset.amcleanup)]?.plan?.request?.demo?.stage==='namespace'){b.disabled=true;b.textContent='Namespace retained';b.title='Demo workload removal is separate. Namespaces are not deleted automatically.';}});
  amWireCurrentConfiguration();
};

function amRunbook(commands){return (commands||[]).length?`<details class="mt"><summary>Official troubleshooting commands · your own terminal</summary><p>Commands use the original context. Compare their output with the captured evidence below.</p>${commands.map(c=>`<p>${esc(c.evidence)}${c.mutates?' <strong>Changes live logging level.</strong>':''}</p>${amCommandBlock(c.command)}<a href="${esc(c.docs)}" target="_blank" rel="noopener">Official docs ↗</a>`).join('')}</details>`:'';}

function amP1Fields(){
  const A=S.ambient,F=A.settings,box=document.querySelector('#am-parameters .am-fields'),svc=amService();if(!box||!svc)return;
  const selected=id=>A.tasks[id];let fields='';
  if(selected('explain'))fields+=amInput('explainPorts','Repair destination ports (comma separated; captured ports only)',F.explainPorts??F.explain?.ports?.join(',')??'')+(F.explain?.paths?.length?amInput('explainPaths','Waypoint repair paths (comma separated)',F.explainPaths??F.explain.paths.join(',')):'');
  if(selected('jwt'))fields+=amInput('issuer','Token issuer',F.issuer||'')+amInput('jwksUri','HTTPS JWKS URI (or use inline public JWKS)',F.jwksUri||'')+amInput('jwks','Inline public JWKS JSON (alternative to URI)',F.jwks||'')+amInput('requestPrincipals','Request principals (comma separated issuer/subject or issuer/*)',F.requestPrincipals||'')+amInput('paths','Optional paths (comma separated)',F.paths||'')+amInput('jwtMethods','Optional HTTP methods (comma separated)',F.jwtMethods||'');
  if(selected('jwt')||selected('header-routing'))fields+=amInput('p1Headers','HTTP headers (JSON name/value object)',F.p1Headers||'{}');
  if(selected('retries')){F.retryAttempts??=2;fields+=amInput('retryAttempts','Retry attempts (1–10)',F.retryAttempts,'number');}
  for(const [task,prefix]of [['mirroring','mirror'],['header-routing','header']])if(selected(task)&&(task!=='header-routing'||!selected('weighted-routing'))){const others=A.inventory.services.filter(s=>s.cluster===svc.cluster&&s.namespace===svc.namespace&&(task!=='mirroring'||s.name!==svc.name));const chosen=others.find(s=>s.name===F[prefix+'Service'])||others[0];F[prefix+'Service']=chosen?.name||'';if(!chosen?.ports.includes(F[prefix+'Port']))F[prefix+'Port']=chosen?.ports[0];fields+=`<label>${task==='mirroring'?'Mirror':'Matching requests backend'} Service<select id="am-${prefix}Service">${amOpts(others.map(s=>s.name),F[prefix+'Service'])}</select></label><label>Backend Service port<select id="am-${prefix}Port">${amOpts(chosen?.ports||[],F[prefix+'Port'])}</select></label>`;}
  box.insertAdjacentHTML('beforeend',fields);
}

async function amReviewExplanation(x){
  const p=await apiPost('api/ambient/plan',{cluster:x.cluster,namespace:x.to.ref.namespace,service:x.to.ref.name,tasks:['explain'],explain:{from:x.from.label,to:x.to.label,...(x.fixScope||{})}});
  amResetDraft();const A=S.ambient;if(S.cluster&&S.cluster!==x.cluster){S.cluster=x.cluster;$('#cluster').value=x.cluster;}A.clusterScope=S.cluster;A.key=JSON.stringify([x.cluster,x.to.ref.namespace,x.to.ref.name]);A.kind='Service';A.tab='wizard';A.phase=3;A.plan=p;A.tasks={explain:true};A.settings={...p.request};nav('ambient');
}

// ---------- Allow or deny requests: a Service-targeted AuthorizationPolicy ----------
// Rules are ORed; every condition inside one rule must match. The draft lives
// in settings.access as plain strings so it survives re-renders.
const AM_METHODS=['GET','HEAD','POST','PUT','PATCH','DELETE','OPTIONS'];
function amAccessDraft(){
  const F=S.ambient.settings;
  F.access=F.access||{action:'DENY',rules:[{sourceNamespaces:'',sourceServices:'',principals:'',methods:[],paths:'',headers:[{name:'',op:'is',values:''}]}]};
  return F.access;
}
function amAccessRuleMarkup(svc){
  const A=S.ambient,a=amAccessDraft(),inv=A.inventory||{};
  const namespaces=(inv.namespaces||[]).filter(n=>n.cluster===svc.cluster).map(n=>n.name);
  const services=(inv.services||[]).filter(s=>s.cluster===svc.cluster).map(s=>s.namespace+'/'+s.name);
  const verb=a.action==='ALLOW'?'allowed':'denied';
  const rule=(r,i)=>`<article class="am-access-rule" data-amaccessrule="${i}">
    <div class="row between"><h4>Rule ${i+1}${i?' <small class="muted">or</small>':''}</h4>${a.rules.length>1?`<button class="link" data-amaccessremove="${i}">Remove rule</button>`:''}</div>
    <div class="am-access-grid">
      <h5>Who is calling</h5>
      <label>Caller namespaces <input data-amaccess="sourceNamespaces" list="am-access-ns" value="${esc(r.sourceNamespaces)}" placeholder="any · comma separated" autocomplete="off"></label>
      <label>Caller Services <input data-amaccess="sourceServices" list="am-access-svc" value="${esc(r.sourceServices)}" placeholder="namespace/name, … · resolved to their service accounts" autocomplete="off"></label>
      <label>Caller principals <input data-amaccess="principals" value="${esc(r.principals)}" placeholder="cluster.local/ns/NS/sa/ACCOUNT, …" autocomplete="off" spellcheck="false"></label>
      <h5>What they ask for</h5>
      <div class="am-access-methods" role="group" aria-label="HTTP methods"><span>Methods</span>${AM_METHODS.map(m=>`<label><input type="checkbox" data-amaccessmethod="${m}" ${r.methods.includes(m)?'checked':''}> ${m}</label>`).join('')}<small class="muted">none ticked = any method</small></div>
      <label>Paths <input data-amaccess="paths" value="${esc(r.paths)}" placeholder="any · /api/*, /admin, …" autocomplete="off" spellcheck="false"></label>
      <h5>Request headers</h5>
      ${r.headers.map((h,j)=>`<div class="am-access-header" data-amaccessheader="${j}"><input data-amaccessh="name" value="${esc(h.name)}" placeholder="header name, e.g. x-req-origin-type" aria-label="Header name" autocomplete="off" spellcheck="false"><select data-amaccessh="op" aria-label="Match">${amOpts([{value:'is',label:'is one of'},{value:'not',label:'is not one of'}],h.op)}</select><input data-amaccessh="values" value="${esc(h.values)}" placeholder="values, comma separated" aria-label="Header values" autocomplete="off" spellcheck="false"><button class="link" data-amaccessheaderremove="${i}:${j}" aria-label="Remove header">×</button></div>`).join('')}
      <div><button class="link" data-amaccessheaderadd="${i}">+ Add header</button></div>
    </div></article>`;
  return `<section class="am-access" id="am-access">
    <fieldset class="am-choice-group"><legend>When a rule matches the request</legend>${[['DENY','Deny it','Refused even if another policy allows it. Everything else is untouched.'],['ALLOW','Allow it','Once this Service has an ALLOW policy, requests that no ALLOW rule matches are refused.']].map(([v,l,d])=>`<label class="am-choice"><input type="radio" name="am-access-action" value="${v}" ${a.action===v?'checked':''}><span><strong>${l}</strong><small>${d}</small><code>${v}</code></span></label>`).join('')}</fieldset>
    <datalist id="am-access-ns">${namespaces.map(n=>`<option value="${esc(n)}">`).join('')}</datalist><datalist id="am-access-svc">${services.map(n=>`<option value="${esc(n)}">`).join('')}</datalist>
    ${a.rules.map(rule).join('')}
    <div class="row wrap"><button data-amaccessadd>Add another rule</button><small class="muted">A request matching any rule is ${verb}. Inside one rule, every condition you fill in must match. Empty fields mean "any".</small></div>
    <p class="muted">Applied by the waypoint for ${esc(svc.namespace+'/'+svc.name)}. Requests that skip the waypoint, such as ingress traffic without <code>istio.io/ingress-use-waypoint</code>, are not checked.</p>
  </section>`;
}
function amCaptureAccessRule(){
  const a=amAccessDraft();
  const action=document.querySelector('input[name="am-access-action"]:checked');if(action)a.action=action.value;
  document.querySelectorAll('[data-amaccessrule]').forEach(el=>{
    const r=a.rules[Number(el.dataset.amaccessrule)];if(!r)return;
    el.querySelectorAll('[data-amaccess]').forEach(i=>{r[i.dataset.amaccess]=i.value;});
    r.methods=[...el.querySelectorAll('[data-amaccessmethod]:checked')].map(i=>i.dataset.amaccessmethod);
    r.headers=[...el.querySelectorAll('[data-amaccessheader]')].map(h=>({name:h.querySelector('[data-amaccessh="name"]').value,op:h.querySelector('[data-amaccessh="op"]').value,values:h.querySelector('[data-amaccessh="values"]').value}));
  });
}
function amAccessRequest(){
  const a=amAccessDraft(),list=v=>String(v||'').split(',').map(x=>x.trim()).filter(Boolean);
  return {action:a.action,rules:a.rules.map(r=>({sourceNamespaces:list(r.sourceNamespaces),sourceServices:list(r.sourceServices),principals:list(r.principals),methods:r.methods,paths:list(r.paths),headers:r.headers.filter(h=>h.name.trim()||h.values.trim()).map(h=>({name:h.name.trim(),values:list(h.values),not:h.op==='not'}))}))};
}
function amWireAccessRule(){
  if(!$('#am-access'))return;
  const a=amAccessDraft(),redraw=change=>{amCaptureAccessRule();change();amInvalidate();render();};
  $('[data-amaccessadd]').onclick=()=>redraw(()=>a.rules.push({sourceNamespaces:'',sourceServices:'',principals:'',methods:[],paths:'',headers:[]}));
  document.querySelectorAll('[data-amaccessremove]').forEach(b=>b.onclick=()=>redraw(()=>a.rules.splice(Number(b.dataset.amaccessremove),1)));
  document.querySelectorAll('[data-amaccessheaderadd]').forEach(b=>b.onclick=()=>redraw(()=>a.rules[Number(b.dataset.amaccessheaderadd)].headers.push({name:'',op:'is',values:''})));
  document.querySelectorAll('[data-amaccessheaderremove]').forEach(b=>b.onclick=()=>redraw(()=>{const [i,j]=b.dataset.amaccessheaderremove.split(':').map(Number);a.rules[i].headers.splice(j,1);}));
  // The radio changes the wording of the rule summary, so redraw on it.
  document.querySelectorAll('input[name="am-access-action"]').forEach(r=>r.addEventListener('change',()=>redraw(()=>{})));
}

// Which waypoint the selected Service uses, shown above the task cards so
// the L7 tasks it unlocks or blocks are explained where you choose them.
function amNeedsIstioWaypoint(c){return /Istio\/Envoy waypoint|Istio waypoint/.test(c.reason||'')||/Istio waypoint/.test(c.layer||'');}
function amWaypointStrip(unavailable){
  const A=S.ambient,sel=A.result?.environment?.selected,blocked=unavailable.filter(amNeedsIstioWaypoint);
  const product=sel?.product==='istio'?'Istio':sel?.product==='agentgateway'?(sel.edition==='enterprise'?'Enterprise agentgateway':'agentgateway'):sel?.product||'';
  const examples=blocked.slice(0,3).map(c=>amTaskText[c.id]?.[0]||c.title).join(', ');
  return `<div class="am-waypoint-strip"><div><strong>Waypoint</strong> <span>${sel?esc(sel.name)+(product?' · '+esc(product):''):'None bound to this Service'}</span>${blocked.length?`<small>${blocked.length} L7 task${blocked.length===1?'':'s'} need an Istio waypoint: ${esc(examples)}${blocked.length>3?'…':''}. Deploy one for this Service or its namespace, or bind an existing one.</small>`:''}</div><div class="row"><button data-amwaypoint="existing">Use an existing waypoint…</button><button data-amwaypoint="new"${blocked.length?' data-amwpistio="1"':''}>${blocked.length?'Deploy an Istio waypoint…':'Deploy a new waypoint…'}</button></div></div>`;
}
