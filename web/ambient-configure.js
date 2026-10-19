'use strict';

function amConfigurationTasks(entry){return entry.tasks||entry.request?.tasks||[];}
function amEditHeading(tasks){const titles={'weighted-routing':'Edit traffic split',timeouts:'Edit timeout','load-balancing':'Edit endpoint balancing','endpoint-failover':'Edit endpoint ejection','strict-mtls':'Edit mutual TLS','l4-allow-namespace':'Edit allowed callers',methods:'Edit HTTP methods','rate-limit':'Edit request rate limit','access-rule':'Edit allow or deny rules'};return tasks.length===1?titles[tasks[0]]||'Edit existing settings':'Edit existing settings';}
function amExistingTaskEntries(task){return (S.ambient.result?.editable||[]).filter(e=>amConfigurationTasks(e).includes(task));}
function amConfigurationTitle(entry){
  const tasks=amConfigurationTasks(entry);
  if(tasks.includes('weighted-routing'))return 'Traffic split';
  if(tasks.includes('strict-mtls'))return 'Mutual TLS';
  if(tasks.includes('l4-default-deny'))return 'Default deny';
  if(tasks.includes('l4-allow-namespace'))return 'Allowed caller namespace';
  if(tasks.includes('rate-limit'))return 'Request rate limit';
  if(tasks.includes('access-rule'))return entry.request?.access?.action==='ALLOW'?'Allow requests':'Deny requests';
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
  if(tasks.includes('access-rule'))values.push(...amAccessSummary(r.access).map(line=>`<p class="am-current-value">${line}</p>`));
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
    ${amWaypointPanel(A.result.waypointBinding,svc)}
    ${amPoliciesPanel(svc)}
    <p class="am-current-context"><span>Configured resources are shown below. These values are not measured traffic.</span></p>
    ${routes.length>1?`<div class="am-current-overlap" role="status">${routes.length} routing rules are configured for this Service. They may overlap. Choose the exact route to edit and check its request matches.</div>`:''}
    ${entries.some(e=>!AM_POLICY_KINDS.includes(e.ref.kind))?`<div class="am-current-grid">${entries.map((e,i)=>{if(AM_POLICY_KINDS.includes(e.ref.kind))return '';
      const tasks=amConfigurationTasks(e),editTasks=e.request?.tasks||[],labels={'weighted-routing':'Edit split',timeouts:'Edit timeout','load-balancing':'Edit balancing','endpoint-failover':'Edit ejection','strict-mtls':'Edit TLS','l4-allow-namespace':'Edit callers',methods:'Edit methods','rate-limit':'Edit rate limit','access-rule':'Edit rules'};
      return `<article class="am-current-card"><div class="am-current-card-head"><h3>${esc(amConfigurationTitle(e))}</h3><span class="badge ${e.status==='Accepted'?'green':'blue'}">${esc(e.status||'Captured')}</span></div>
        <p class="am-current-resource">${esc(e.ref.kind)} · ${esc(e.ref.namespace+'/'+e.ref.name)}${e.ref.kind==='HTTPRoute'?' · rule '+(e.rule+1):''}</p>
        ${amConfigurationValues(e)}${e.ref.kind==='HTTPRoute'?`<p class="am-current-match">${esc(amConfigurationMatch(e))}</p>`:''}<p class="am-current-scope">${esc(e.scope||'Existing attachment scope is retained')}</p>
        ${!e.supported?`<p class="am-current-readonly">${esc(e.reason)}</p>`:''}
        <div class="am-current-actions">${e.supported?editTasks.map((task,j)=>`<button ${j===0?'class="primary"':''} data-am-edit-config="${i}" data-am-edit-task="${esc(task)}">${esc(labels[task]||'Edit settings')}</button>`).join(''):''}<button data-amyaml="${attr(e.ref)}">View YAML</button>${amConfigurationTasks(e).includes('access-rule')?`<button data-am-test-rule="${i}" title="Open Test service from an in-mesh Pod with this rule's headers filled in">Test this rule</button>`:''}</div></article>`;
    }).join('')}</div>`:'<div class="am-current-empty"><h3>No custom routing configuration captured</h3><p>Add a capability below, or refresh the capture if you expect existing settings. Missing evidence does not prove that no configuration exists.</p></div>'}
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
  document.querySelectorAll('[data-am-policy-yaml]').forEach(d=>d.addEventListener('toggle',async()=>{if(!d.open||d.dataset.loaded)return;d.dataset.loaded='1';const p=A.result.policies[Number(d.dataset.amPolicyYaml)];try{const o=await api('api/object',p.ref);d.querySelector('pre').textContent=o.yaml;}catch(e){d.querySelector('pre').textContent=e.message;}}));
  if($('[data-am-new-access]'))$('[data-am-new-access]').onclick=()=>{const cap=(A.capabilities||[]).find(c=>c.id==='access-rule');if(!cap?.supported){toast('Allow or deny requests needs an Istio waypoint for this Service. Use Change waypoint above.');return;}amSaveConfigurationDraft();amResetDraft(true);A.tab='wizard';A.tasks={'access-rule':true};A.phase=2;render();};
  document.querySelectorAll('[data-am-test-rule]').forEach(b=>b.onclick=()=>amTestAccessRule(A.result.editable[Number(b.dataset.amTestRule)].request?.access));
  if($('#am-current-waypoint'))$('#am-current-waypoint').onclick=()=>{
    const waypoint=A.result.graph?.groups?.find(g=>g.title==='Waypoint')?.nodes?.[0]?.ref;
    amSaveConfigurationDraft();amResetDraft(true);A.tab='wizard';A.mode='waypoint';A.phase=2;if(waypoint)A.settings={wpMode:'existing',wpExisting:waypoint.namespace+'/'+waypoint.name,wpScope:'service'};render();
  };
}

// One line per rule: "Deny when header x-req-origin-type is be and from namespace web".
function amAccessSummary(a){
  if(!a)return [];
  const verb={ALLOW:'Allow',DENY:'Deny',AUDIT:'Audit',CUSTOM:'Send to '+(a.provider||'external authorizer')}[a.action]||a.action;
  if(a.rulesYaml)return [`<strong>${esc(verb)}</strong> · rules written as YAML`];
  const list=v=>(v||[]).map(x=>`<code>${esc(x)}</code>`).join(', ');
  const fields=(o,names)=>Object.entries(o||{}).filter(([,v])=>v?.length).map(([k,v])=>`${names[k]||k} ${list(v)}`).join(' and ');
  const src={principals:'principal',notPrincipals:'principal not',namespaces:'namespace',notNamespaces:'namespace not',services:'Service',serviceAccounts:'service account',notServiceAccounts:'service account not',requestPrincipals:'JWT principal',notRequestPrincipals:'JWT principal not',ipBlocks:'IP',notIpBlocks:'IP not',remoteIpBlocks:'remote IP',notRemoteIpBlocks:'remote IP not'};
  const op={methods:'method',notMethods:'method not',paths:'path',notPaths:'path not',ports:'port',notPorts:'port not',hosts:'host',notHosts:'host not'};
  return (a.rules||[]).map((r,i)=>{
    const parts=[(r.from||[]).map(f=>fields(f,src)).filter(Boolean).map(x=>'from '+x).join(' or '),(r.to||[]).map(t=>fields(t,op)).filter(Boolean).join(' or '),...(r.when||[]).map(c=>`<code>${esc(c.key)}</code> ${c.notValues?.length?'is not':'is'} ${list(c.values?.length?c.values:c.notValues)}`)].filter(Boolean);
    return `<strong>${esc(verb)}</strong>${(a.rules||[]).length>1?' · rule '+(i+1):''} when ${parts.join(' and ')||'any request'}`;
  });
}
// Every Istio security policy that reaches this Service, why, and where it
// is enforced, with its YAML one click away and Edit where the editor can
// represent it faithfully.
const AM_POLICY_KINDS=['AuthorizationPolicy','PeerAuthentication','RequestAuthentication'];
function amPoliciesPanel(svc){
  const A=S.ambient,list=A.result.policies||[],entries=A.result.editable||[];
  const scope={service:['This Service','blue'],waypoint:['Its waypoint','blue'],workload:['Its Pods','amber'],namespace:['Whole namespace','amber'],mesh:['Whole mesh','red']};
  const body=list.length?list.map((p,i)=>{
    const ei=entries.findIndex(e=>e.ref.kind===p.ref.kind&&e.ref.namespace===p.ref.namespace&&e.ref.name===p.ref.name),e=entries[ei];
    const access=e?.request?.access,summary=access?amAccessSummary(access):[];
    const [label,tone]=scope[p.scope]||[p.scope,''];
    return `<article class="am-policy-card">
      <div class="row between wrap"><div class="row wrap"><span class="badge">${esc(p.ref.kind)}</span><strong>${esc(p.ref.namespace+'/'+p.ref.name)}</strong>${p.action?`<span class="badge ${p.action==='DENY'?'red':p.action==='ALLOW'?'green':''}">${esc(p.action)}</span>`:''}<span class="badge ${tone}">${esc(label)}</span><small class="muted">enforced by ${esc(p.enforced)}</small></div>
        <div class="row">${e?.supported?`<button class="primary" data-am-edit-config="${ei}" data-am-edit-task="${esc((e.request?.tasks||[])[0]||'')}">Edit</button>`:''}${access?`<button data-am-test-rule="${ei}">Test this rule</button>`:''}<button data-amyaml="${attr(p.ref)}">Open</button></div></div>
      <p class="muted">Applies because ${esc(p.reason)}.${p.status?' '+esc(p.status)+'.':''}</p>
      ${p.warning?`<p class="red" role="alert"><strong>⚠ ${esc(p.warning)}</strong></p>`:''}
      ${summary.map(l=>`<p class="am-current-value">${l}</p>`).join('')}
      ${!e?.supported&&p.ref.kind==='AuthorizationPolicy'&&p.scope==='service'?`<p class="am-current-readonly">${esc(e?.reason||'Read-only here.')}</p>`:''}
      <details class="am-policy-yaml" data-am-policy-yaml="${i}"><summary>YAML</summary><pre class="code">Loading…</pre></details>
    </article>`;}).join(''):`<p class="muted">No AuthorizationPolicy, PeerAuthentication or RequestAuthentication in ${esc(svc.namespace)} or the root namespace reaches this Service. Missing evidence does not prove there are none: check the capture includes security.istio.io.</p>`;
  return `<section class="am-policies"><div class="row between"><h3>Security policies for this Service <span class="badge">${list.length}</span></h3><button data-am-new-access>New allow or deny policy</button></div>${body}</section>`;
}
// Which waypoint the Service uses, how that was decided, and what it means
// for the tasks below. One place, so "is a waypoint in use?" has one answer.
function amWaypointPanel(b,svc){
  if(!b)return '';
  const product=b.product==='istio'?'Istio':b.product==='agentgateway'?'agentgateway':b.product||'unknown implementation';
  if(b.source==='none'||b.source==='opt-out'){
    return `<section class="am-waypoint-panel am-waypoint-none"><div><h3>No waypoint in use</h3><p>${b.source==='opt-out'?`This Service opts out with <code>${esc(b.label)}</code> on ${esc(b.labelOn)}.`:`Neither the Service nor namespace ${esc(svc.namespace)} has an <code>istio.io/use-waypoint</code> label.`} Traffic is L4 only through ztunnel, so header, path, method and JWT policies cannot be enforced.</p></div><div class="row"><button id="am-current-waypoint" data-amwaypoint-mode="existing">Use an existing waypoint…</button><button data-amwaypoint="new" data-amwpistio="1" class="primary">Deploy an Istio waypoint…</button></div></section>`;
  }
  const scope=b.source==='service-label'?(b.usedBy>1?`Shared: bound by a label on this Service, and used by ${b.usedBy-1} other Service${b.usedBy===2?'':'s'}`:'Dedicated to this Service: bound by a label on the Service itself'):`Namespace-wide: every Service in ${esc(svc.namespace)} uses it unless it sets its own label`;
  const health=!b.captured?'<span class="badge amber">Not captured</span>':b.ready>0?`<span class="badge green">Ready ${b.ready}/${b.pods}</span>`:b.pods?`<span class="badge red">0/${b.pods} ready</span>`:'<span class="badge red">No pods</span>';
  const istioOnly=b.product!=='istio';
  return `<section class="am-waypoint-panel"><div>
      <h3>Waypoint in use <code>${esc(b.namespace+'/'+b.name)}</code> ${health}${b.programmed&&b.programmed!=='True'?' <span class="badge red">Not programmed</span>':''}</h3>
      <dl class="am-global-facts">
        <dt>Implementation</dt><dd>${esc(product)}${b.class?` <small class="muted">${esc(b.class)}</small>`:''}</dd>
        <dt>How it is selected</dt><dd>${scope}. <code>${esc(b.label)}</code> on ${esc(b.labelOn)}${b.central?`. The waypoint runs centrally in namespace ${esc(b.namespace)}`:''}.</dd>
        <dt>Handles</dt><dd>${esc(b.for||'service')} traffic${b.for==='workload'?' only, so requests to the Service name skip it':''}</dd>
        ${istioOnly?`<dt>Policies</dt><dd>${b.product==='agentgateway'?'Allow or deny requests and rate limits are written as agentgateway policies, which this waypoint enforces. <span class="amber">Istio AuthorizationPolicies are accepted by istiod but not enforced here; JWT, retries, mirroring and caller-identity rules need an Istio waypoint.</span>':'<span class="amber">Not an Istio waypoint, so Istio L7 policies are not enforced here.</span>'}</dd>`:'<dt>Policies</dt><dd>Istio L7 policies such as Allow or deny requests apply here. Traffic that skips the waypoint is not checked.</dd>'}
      </dl></div>
    <div class="row">${b.ref?`<button data-amyaml="${attr(b.ref)}">Waypoint YAML</button>`:''}<button id="am-current-waypoint">Change waypoint…</button>${istioOnly?'<button data-amwaypoint="new" data-amwpistio="1">Deploy an Istio waypoint…</button>':''}</div></section>`;
}
// Open Test service from an in-mesh client with the rule's headers set, so
// the result shows whether the waypoint applied the rule.
async function amTestAccessRule(access){
  const service=amService();if(!service)return;
  const rule=access?.rules?.[0]||{},headers={},to=(rule.to||[])[0]||{};
  for(const c of rule.when||[]){const m=/^request\.headers\[(.+)\]$/.exec(c.key||'');if(m&&c.values?.length)headers[m[1]]=c.values[0];}
  HTTP_TEST.profile={name:'',method:to.methods?.[0]||'GET',path:(to.paths?.[0]||'/').replace(/\*$/,''),headers,body:'',timeout:10};
  HTTP_TEST.panel='request';
  await amOpenHTTPTest(service);
  // Without an existing curl Pod, run from a temporary one in the Service's
  // namespace so the request still goes through ztunnel and the waypoint.
  if((HTTP_TEST.options?.clients||[]).length){HTTP_TEST.mode='mesh';HTTP_TEST.count=5;if(typeof drawHTTPTest==='function')drawHTTPTest();}
  else if((HTTP_TEST.contexts||[]).length){HTTP_TEST.mode='ephemeral';HTTP_TEST.ephemeralNamespace=service.namespace;HTTP_TEST.count=5;if(typeof drawHTTPTest==='function')drawHTTPTest();}
  toast(Object.keys(headers).length?'Headers from the rule are filled in. A DENY that works returns 403 from an in-mesh client.':'Fill in a request that matches the rule, then run it.');
}
