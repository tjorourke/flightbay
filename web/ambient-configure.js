'use strict';

function amConfigurationTasks(entry){return entry.tasks||entry.request?.tasks||[];}
function amEditHeading(tasks){const titles={'weighted-routing':'Edit traffic split',timeouts:'Edit timeout','load-balancing':'Edit endpoint balancing','endpoint-failover':'Edit endpoint ejection','strict-mtls':'Edit mutual TLS','l4-allow-namespace':'Edit allowed callers',methods:'Edit HTTP methods','rate-limit':'Edit request rate limit'};return tasks.length===1?titles[tasks[0]]||'Edit existing settings':'Edit existing settings';}
function amExistingTaskEntries(task){return (S.ambient.result?.editable||[]).filter(e=>amConfigurationTasks(e).includes(task));}
function amConfigurationTitle(entry){
  const tasks=amConfigurationTasks(entry);
  if(tasks.includes('weighted-routing'))return 'Traffic split';
  if(tasks.includes('strict-mtls'))return 'Mutual TLS';
  if(tasks.includes('l4-default-deny'))return 'Default deny';
  if(tasks.includes('l4-allow-namespace'))return 'Allowed caller namespace';
  if(tasks.includes('rate-limit'))return 'Request rate limit';
  if(tasks.includes('methods'))return 'HTTP method policy';
  if(entry.ref.kind==='DestinationRule')return 'Endpoint traffic policy';
  if(tasks.includes('timeouts'))return 'Request timeout';
  return entry.ref.kind;
}
function amConfigurationMatch(entry){
  if(entry.ref.kind!=='HTTPRoute')return '';
  if(!entry.matches?.length)return 'All requests';
  return entry.matches.map(m=>[m.method,m.path?`${m.path.type||'PathPrefix'} ${m.path.value||'/'}`:'',m.headers?.length?m.headers.length+' header match'+(m.headers.length===1?'':'es'):'',m.queryParams?.length?m.queryParams.length+' query match'+(m.queryParams.length===1?'':'es'):''].filter(Boolean).join(' · ')).join(' or ');
}
function amConfigurationValues(entry){
  const r=entry.request||{},tasks=amConfigurationTasks(entry),values=[];
  if(tasks.includes('weighted-routing')){
    const total=(r.backends||[]).reduce((n,b)=>n+b.weight,0);
    values.push(`<div class="am-current-weights">${(r.backends||[]).map((b,i)=>{const pct=total?100*b.weight/total:0;return `<div><span><i class="am-current-dot tone-${i%4}"></i><strong>${esc(b.name)}</strong><small>:${b.port}</small></span><b>${pct.toFixed(Number.isInteger(pct)?0:1)}%</b></div>`;}).join('')}</div>`);
    values.push(`<div class="am-current-weight-bar" aria-label="Configured backend weights">${(r.backends||[]).map((b,i)=>`<span class="tone-${i%4}" style="width:${total?100*b.weight/total:0}%"></span>`).join('')}</div>`);
  }
  if(tasks.includes('timeouts'))values.push(`<p class="am-current-value">Request timeout <strong>${esc(r.timeout||'Inherited / unset')}</strong>${r.backendTimeout?' · backend '+esc(r.backendTimeout):''}</p>`);
  if(tasks.includes('strict-mtls'))values.push(`<p class="am-current-value">Mode <strong>${esc(r.mtlsMode||'Inherited / inspect policy')}</strong></p>`);
  if(tasks.includes('l4-default-deny'))values.push('<p class="am-current-value">Explicit allow rules are required. Other ALLOW policies can grant access.</p>');
  if(tasks.includes('l4-allow-namespace'))values.push(`<p class="am-current-value">Allow callers from <strong>${esc(r.sourceNamespace)}</strong></p>`);
  if(tasks.includes('methods'))values.push(`<p class="am-current-value"><strong>${esc(r.methodAction||'ALLOW')}</strong> ${esc((r.methods||[]).join(', '))}</p>`);
  if(tasks.includes('rate-limit'))values.push(`<p class="am-current-value"><strong>${r.requests}</strong> requests / ${esc((r.unit||'').toLowerCase())}${r.burst!==undefined?' · burst '+r.burst:''}</p>`);
  if(tasks.includes('load-balancing'))values.push(`<p class="am-current-value">Endpoint algorithm <strong>${esc(r.algorithm||'Controller default')}</strong></p>`);
  if(tasks.includes('endpoint-failover'))values.push(`<p class="am-current-value">Eject after <strong>${r.failureThreshold}</strong> failures · ${esc(r.ejectionDuration||'default duration')}</p>`);
  return values.join('');
}
function amConfigurationHome(svc){
  const A=S.ambient,entries=A.result.editable||[],routes=entries.filter(e=>e.ref.kind==='HTTPRoute'&&e.request?.backends?.length>1),waypoint=A.result.graph?.groups?.find(g=>g.title==='Waypoint')?.nodes?.[0]?.ref;
  const applied=A.lastChange&&A.lastChange.cluster===svc.cluster&&A.lastChange.namespace===svc.namespace&&A.lastChange.service===svc.name;
  return `<section class="panel am-current"><div class="panel-head"><div><h2>Current configuration</h2><p>${esc(svc.namespace+'/'+svc.name)} · ${esc(svc.cluster)}</p></div><button class="primary" id="am-retest-current">${applied?'Retest traffic':'Test current traffic'}</button></div><div class="panel-body">
    ${applied&&A.currentRefreshPending?'<div class="notice info" role="status">The cluster was updated. This capture has not caught up yet; refresh before editing again.</div>':''}
    <div class="am-current-context"><span>Configured resources are shown below. These values are not measured traffic.</span>${waypoint?`<span>Waypoint <strong>${esc(waypoint.namespace+'/'+waypoint.name)}</strong><button id="am-current-waypoint">Change binding</button></span>`:''}</div>
    ${routes.length>1?`<div class="am-current-overlap" role="status">${routes.length} routing rules are configured for this Service. They may overlap. Choose the exact route to edit and check its request matches.</div>`:''}
    ${entries.length?`<div class="am-current-grid">${entries.map((e,i)=>{
      const tasks=amConfigurationTasks(e),editTasks=e.request?.tasks||[],labels={'weighted-routing':'Edit split',timeouts:'Edit timeout','load-balancing':'Edit balancing','endpoint-failover':'Edit ejection','strict-mtls':'Edit TLS','l4-allow-namespace':'Edit callers',methods:'Edit methods','rate-limit':'Edit rate limit'};
      return `<article class="am-current-card"><div class="am-current-card-head"><h3>${esc(amConfigurationTitle(e))}</h3><span class="badge ${e.status==='Accepted'?'green':'blue'}">${esc(e.status||'Captured')}</span></div>
        <p class="am-current-resource">${esc(e.ref.kind)} · ${esc(e.ref.namespace+'/'+e.ref.name)}${e.ref.kind==='HTTPRoute'?' · rule '+(e.rule+1):''}</p>
        ${amConfigurationValues(e)}${e.ref.kind==='HTTPRoute'?`<p class="am-current-match">${esc(amConfigurationMatch(e))}</p>`:''}<p class="am-current-scope">${esc(e.scope||'Existing attachment scope is retained')}</p>
        ${!e.supported?`<p class="am-current-readonly">${esc(e.reason)}</p>`:''}
        <div class="am-current-actions">${e.supported?editTasks.map((task,j)=>`<button ${j===0?'class="primary"':''} data-am-edit-config="${i}" data-am-edit-task="${esc(task)}">${esc(labels[task]||'Edit settings')}</button>`).join(''):''}<button data-amyaml="${attr(e.ref)}">View YAML</button></div></article>`;
    }).join('')}</div>`:'<div class="am-current-empty"><h3>No custom routing or policy configuration captured</h3><p>Add a capability below, or refresh the capture if you expect existing settings. Missing evidence does not prove that no configuration exists.</p></div>'}
    </div></section>
    ${entries.length?`<details class="panel am-current-add" id="am-add-config" ${A.createOpen?'open':''}><summary>Add another capability <span>Existing settings are edited above</span></summary>${amTaskCards()}</details>`:amTaskCards()}`;
}
function amOpenConfiguration(entry,task){
  if(!entry?.supported||!entry.request)return;
  try{amOpenEdit(entry,task);return render();}catch(e){toast(e.message);}
}
async function amRefreshAppliedConfiguration(plan){
  const A=S.ambient,q=plan.request,key=JSON.stringify([q.cluster,q.namespace,q.service]);if(A.key!==key||!q.service)return;
  A.lastChange={cluster:q.cluster,namespace:q.namespace,service:q.service,planId:plan.id};A.currentRefreshPending=false;
  A.inventory=null;A.edit=null;A.editTasks=null;A.tasks={};A.settings={};A.plan=null;A.validation=null;A.editDrafts={};A.taskDraft=null;A.phase=1;
  if(q.edit?.fingerprint||plan.objects?.length){
    A.currentRefreshPending=true;
    if(S.ws?.live)for(let i=0;i<12;i++){
      if(A.key!==key)return;
      const view=await api('api/ambient/investigate',{cluster:q.cluster,namespace:q.namespace,kind:'Service',name:q.service});
      if(A.key!==key)return;
      const entry=q.edit?(view.editable||[]).find(e=>e.ref.kind===q.edit.ref.kind&&e.ref.name===q.edit.ref.name&&e.ref.namespace===q.edit.ref.namespace&&e.rule===q.edit.rule):null;
      const refs=[...(view.editable||[]).map(e=>e.ref),...(view.graph?.groups||[]).flatMap(g=>g.nodes.map(n=>n.ref))];
      const present=q.edit?.fingerprint?entry?.fingerprint&&entry.fingerprint!==q.edit.fingerprint:(plan.objects||[]).every(o=>refs.some(ref=>ref.kind===o.kind&&ref.namespace===o.metadata.namespace&&ref.name===o.metadata.name));
      if(present){A.currentRefreshPending=false;break;}
      await new Promise(resolve=>setTimeout(resolve,400));
    }
  }
}
function amWireCurrentConfiguration(){
  const A=S.ambient;
  document.querySelectorAll('[data-am-edit-config]').forEach(b=>b.onclick=()=>amOpenConfiguration(A.result.editable[Number(b.dataset.amEditConfig)],b.dataset.amEditTask));
  if($('#am-retest-current'))$('#am-retest-current').onclick=()=>{const service=amService();if(!service)return;HTTP_TEST.panel='request';amOpenHTTPTest(service);};
  if($('#am-current-waypoint'))$('#am-current-waypoint').onclick=()=>{
    const waypoint=A.result.graph?.groups?.find(g=>g.title==='Waypoint')?.nodes?.[0]?.ref;
    amSaveConfigurationDraft();amResetDraft(true);A.tab='wizard';A.mode='waypoint';A.phase=2;if(waypoint)A.settings={wpMode:'existing',wpExisting:waypoint.namespace+'/'+waypoint.name,wpScope:'service'};render();
  };
}
