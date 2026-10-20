'use strict';

function amGlobalTargets(svc){
  return (S.ambient.inventory?.topology?.clusters||[]).map(cluster=>{
    const service=cluster.services.find(s=>s.namespace===svc.namespace&&s.name===svc.name);
    const captured=S.ambient.inventory?.services?.find(s=>s.cluster===cluster.context&&s.namespace===svc.namespace&&s.name===svc.name);
    return {cluster:cluster.context,service,global:!!service&&['global','global-only'].includes(service.scope),cleanup:!!service?.globalCleanup,publication:captured?.publication};
  });
}
function amPublicationEvidenceLabel(row){
  if(!row?.global)return row?.service?'Ready to review':'No local Service captured';
  const aliases=row.publication?.globalAliases||[];
  if(!aliases.length)return 'Export label set · alias evidence missing';
  if(aliases.some(a=>!a.addresses?.length))return 'Export label set · alias addresses missing';
  return 'Alias addresses observed · traffic unverified';
}
function amGlobalClusterChoices(svc){
  const A=S.ambient,chosen=A.settings.globalClusters||[svc.cluster];
  return `<section class="am-global-targets"><h3>Clusters contributing local endpoints</h3><p>Select existing Service instances that should contribute endpoints. A remote cluster can consume the published global Service without running its own local instance. This workflow does not copy Services or workloads between clusters.</p>${amGlobalTargets(svc).map(row=>`<label class="am-global-cluster"><input type="checkbox" data-amglobalcluster="${esc(row.cluster)}" ${chosen.includes(row.cluster)?'checked':''} ${row.service?'':'disabled'}><span><strong>${esc(row.cluster)}</strong><small>${row.global?'Already global · no label change required':row.service?'Local Service captured · review publication':'No local '+esc(svc.namespace+'/'+svc.name)+' Service is captured. This cluster can still consume the global name; local workloads are needed only to contribute endpoints.'}</small></span><span class="badge ${row.global?'green':row.service?'blue':'amber'}">${row.global?'Already global':row.service?'Ready to review':'No local Service captured'}</span></label>${!row.service?`<div class="am-global-missing-actions"><button data-amglobalsetup="${esc(row.cluster)}">Set up Service participation…</button></div>`:''}`).join('')}<p class="muted">Each contributing cluster gets its own preview, connection, validation and apply result. Live connections are reused automatically. There is no cross-cluster transaction.</p></section>`;
}
// When the Service is already published somewhere, the configure step shows
// what is live instead of a fresh wizard: the label each cluster carries, the
// names the controller generated, and a per-cluster choice to keep it, stop
// publishing or start publishing. Clusters left on "keep" are not touched.
function amGlobalPublished(svc){return amGlobalTargets(svc).some(r=>r.global||r.cleanup);}
function amPublicationConfigurationCard(svc){
  const rows=amGlobalTargets(svc),published=rows.filter(r=>r.global);if(!published.length&&!rows.some(r=>r.cleanup))return '';
  const routing=amGlobalRouting(svc),names=[...new Set(published.flatMap(r=>r.service.globalHosts||r.publication?.globalNames||[]))],routes=(S.ambient.result?.editable||[]).filter(e=>e.ref.kind==='HTTPRoute'),label=AM_GLOBAL_DIST.find(d=>d[0]===routing.distribution)?.[1]||routing.distribution;
  return `<section class="am-publication-config"><div class="row between wrap"><h3>Global Service configuration <span class="badge">${published.length} clusters</span></h3><button class="primary" data-am-global-config>Edit global publication</button></div><article class="am-policy-card"><div class="row wrap"><span class="badge">Service labels</span><strong>${esc(svc.namespace+'/'+svc.name)}</strong><span class="badge blue">${published.length?'Published globally':'Publication cleanup'}</span></div><p>Published from ${esc(published.map(r=>r.cluster).join(', ')||'no cluster')}. ${esc(label)}.${routing.mixed?' Settings differ between clusters.':''}</p><p>${names.map(n=>`<code>${esc(n)}</code>`).join(' ')}</p><div class="am-publication-clusters">${published.map(r=>`<div><strong>${esc(r.cluster)}</strong><span>${r.service.takeover?'Local DNS included':'Global DNS only'}</span><small>${esc(amPublicationEvidenceLabel(r))}</small></div>`).join('')}</div><p class="muted">Publication is configured on the Service; the mesh generates ServiceEntries. It is not a separate security policy and does not copy authorisation rules between clusters.</p>${routes.length?`<div class="notice info"><div><strong>Explicit HTTP routes still apply</strong><p>Local-name takeover does not rewrite an HTTPRoute’s backend references. Matching requests can still be routed to local version Services, even when the global name has healthy remote endpoints.</p>${routes.map(r=>`<button class="link" data-amyaml="${attr(r.ref)}">${esc(r.ref.namespace+'/'+r.ref.name)}</button>`).join(' ')}</div></div>`:''}</article></section>`;
}
function amGlobalEdits(){return S.ambient.settings.globalEdits||{};}
function amGlobalPendingEdits(svc){
  const e=amGlobalEdits(),r=amGlobalRouting(svc),wantDist=S.ambient.settings.globalDistribution??r.distribution,wantTake=S.ambient.settings.globalTakeover;
  const routingChanged=row=>row.service&&((S.ambient.settings.globalDistribution!==undefined&&(row.service.globalWeights||wantDist!==(row.service.distribution||'default')))||(wantTake!==undefined&&wantTake!==!!row.service.takeover));
  return amGlobalTargets(svc).filter(row=>row.service).map(row=>{
    const a=e[row.cluster];
    if(a==='unpublish'&&(row.global||row.cleanup))return {cluster:row.cluster,action:'unpublish'};
    if(wantDist==='weighted'&&a!=='unpublish')return !row.global||!amGlobalWeightsEqual(amGlobalWeights(svc),row.service.globalWeights)||(wantTake!==undefined&&wantTake!==!!row.service.takeover)?{cluster:row.cluster,action:row.global?'update':'publish'}:null;
    if(a==='publish'&&!row.global)return {cluster:row.cluster,action:'publish'};
    if(row.global&&a!=='unpublish'&&routingChanged(row))return {cluster:row.cluster,action:'update'};
    return null;
  }).filter(Boolean);
}
function amGlobalEditLabel(edit){return (edit.action==='unpublish'?'Stop publishing from ':edit.action==='update'?'Change cross-cluster routing on ':'Publish from ')+edit.cluster;}
// How requests to the global name pick a cluster, and whether the local name
// is taken over. Applied to every cluster that publishes the Service.
const AM_GLOBAL_DIST=[['default','Prefer this cluster, fail over to the others','PreferNetwork · default'],['Any','Spread evenly across every cluster','Any · round robin across all endpoints'],['weighted','Split by weight, e.g. 70/30','HTTPRoute through the Istio waypoint'],['PreferRegion','Prefer the same region, then others','PreferRegion'],['PreferClose','Prefer the same zone, then others','PreferClose']];
function amGlobalWeightsEqual(a,b){if(!a||!b)return false;return [...new Set([...Object.keys(a),...Object.keys(b)])].every(c=>Number(a[c]||0)===Number(b[c]||0));}
function amGlobalRouting(svc){const rows=amGlobalTargets(svc).filter(r=>r.service),first=rows.find(r=>r.global)||rows[0],dist=r=>r?.service?.globalWeights?'weighted':r?.service?.distribution||'default';return {distribution:dist(first),weights:first?.service?.globalWeights,takeover:!!first?.service?.takeover,mixed:new Set(rows.map(dist)).size>1||new Set(rows.map(r=>!!r.service.takeover)).size>1||rows.some(r=>dist(r)==='weighted'&&!amGlobalWeightsEqual(r.service.globalWeights,first?.service?.globalWeights))};}
function amTakeoverPrerequisiteMarkup(svc){
  const A=S.ambient,p=A.result?.environment?.takeoverPrerequisite;if(!p||p.state==='na')return '';
  const setting='PILOT_ENABLE_K8S_SELECT_WORKLOAD_ENTRIES',source=(p.sources||[]).find(r=>r.kind==='Deployment'),context=amLiveContext(svc.cluster,A.contexts)?.name||A.context||'<real-context>',quote=value=>"'"+String(value).replaceAll("'","'\"'\"'")+"'",command=source?`kubectl --context ${quote(context)} -n ${quote(source.namespace)} set env deployment/${quote(source.name)} ${setting}=true`:'';
  return `<div class="notice ${p.state==='fail'?'bad':'info'} am-takeover-prerequisite" role="status"><div><strong>${p.state==='fail'?'Control-plane setting blocks Istio-waypoint takeover':p.state==='pass'?'Istio-waypoint takeover prerequisite checked':'Verify the Istio-waypoint takeover prerequisite'}</strong><p><code>${setting}=${esc(p.setting||'unknown')}</code></p><p>${esc(p.detail)}</p><p>The checkbox changes the Service label only. It does not enable this cluster-level istiod feature or verify backend health.</p>${p.state!=='pass'?`<details><summary>Cluster operator remediation</summary><p>Set this value in the existing istiod Helm/GitOps configuration and roll out istiod, then refresh inventory:</p><pre class="code">env:\n  ${setting}: "true"</pre>${command?`<p>This real-cluster command rolls out istiod; persist the same value in Helm/GitOps so an upgrade does not undo it.</p>${typeof amCommandBlock==='function'?amCommandBlock(command,'Copy operator command'):`<pre class="code">${esc(command)}</pre>`}`:''}</details>`:''}</div></div>`;
}
function amGlobalRoutingMarkup(svc){
  const F=S.ambient.settings,cur=amGlobalRouting(svc),dist=F.globalDistribution??cur.distribution,take=F.globalTakeover??cur.takeover;
  return `<section class="am-global-routing"><h3>Routing across clusters</h3>
    <p class="muted">By default a caller uses this Service's endpoints in its own cluster and only goes to another cluster when none are healthy. Set on every cluster that publishes it.${cur.mixed?' <span class="amber">The clusters are set differently today.</span>':''}</p>
    <div class="am-global-choice am-global-dist" role="radiogroup" aria-label="Traffic distribution">${AM_GLOBAL_DIST.map(([v,l,d])=>`<label><input type="radio" name="am-global-dist" data-amglobaldist value="${v}" ${dist===v?'checked':''}> <span><strong>${esc(l)}</strong><small>${esc(d)}</small></span></label>`).join('')}</div>
    ${dist==='weighted'?amGlobalWeightsMarkup(svc):''}
    ${amTakeoverPrerequisiteMarkup(svc)}
    ${dist==='Any'&&S.ambient.result?.environment?.selected?.product==='istio'?'<p class="muted">With waypoints, an endpoint can be a ready proxy whose application backends are empty. Any is active/active distribution, not a health check. PreferClose is appropriate for local-first failover; active/active routing needs suitable failure detection.</p>':''}
    ${dist==='Any'?'<p class="muted">Spread is per endpoint, not per cluster: 3 Pods in one cluster and 1 in another is about 75/25. ztunnel balances per connection, so a client that reuses one connection stays on one backend; a waypoint balances per request.</p>':''}
    <label class="row am-global-takeover"><input type="checkbox" id="am-global-takeover" ${take?'checked':''}> <span>Send the local name to the global Service too <small class="muted"><code>solo.io/service-takeover=true</code>: makes global endpoints available to <code>${esc(svc.name+'.'+svc.namespace+'.svc.cluster.local')}</code>. Explicit HTTPRoute backends and authorisation policies still apply.</small></span></label>
  </section>`;
}
function amGlobalCurrentConfig(svc){
  const A=S.ambient,rows=amGlobalTargets(svc),edits=amGlobalEdits(),published=rows.filter(r=>r.global);
  const ref=row=>attr({cluster:row.cluster,group:'',resource:'services',namespace:svc.namespace,name:svc.name});
  const card=row=>{
    const scope=row.service?.scope||'',aliases=row.publication?.globalAliases||[],choice=edits[row.cluster]||'keep';
    const status=row.global?['green','Published']:row.service?['blue','Local only']:['amber','No local Service captured'];
    const options=row.global?[['keep','Keep publishing'],['unpublish','Stop publishing from this cluster']]:[['keep','Keep local only'],['publish','Publish from this cluster'],...(row.cleanup?[['unpublish','Remove leftover global routing']]:[])];
    const names=aliases.length?aliases.map(a=>`<li>${(a.hosts||[]).map(h=>`<code>${esc(h)}</code>`).join(' ')}${a.addresses?.length?` <small class="muted">${esc(a.addresses.join(', '))}</small>`:' <small class="amber">no address allocated yet</small>'}${a.ref?.name?` <button class="link" data-amevidence="${attr(a.ref)}">ServiceEntry</button>`:''}</li>`).join(''):'<li class="muted">None captured. The controller creates them after the label is set; refresh to check.</li>';
    return `<article class="am-global-current-row${choice!=='keep'?' am-global-changing':''}">
      <div class="row between wrap"><div class="row"><strong>${esc(row.cluster)}</strong><span class="badge ${status[0]}">${esc(status[1])}</span></div>
        ${row.service?`<div class="row"><button data-amevidence="${ref(row)}">View object</button><button data-amyaml="${ref(row)}">YAML</button></div>`:''}</div>
      ${row.service?`<dl class="am-global-facts"><dt>Label</dt><dd><code>solo.io/service-scope: ${esc(scope||'not set')}</code></dd>
        <dt>Global names</dt><dd><ul>${row.global?names:'<li class="muted">Not published from this cluster.</li>'}</ul></dd>
        ${row.global?`<dt>Evidence</dt><dd>${esc(amPublicationEvidenceLabel(row))}. API metadata alone does not prove proxy delivery or failover.</dd>`:''}</dl>
      <div class="am-global-choice" role="radiogroup" aria-label="${esc(row.cluster)} publication">${options.map(([value,label])=>`<label><input type="radio" name="am-global-edit-${esc(row.cluster)}" data-amglobaledit="${esc(row.cluster)}" value="${value}" ${choice===value?'checked':''}> ${esc(label)}</label>`).join('')}</div>
      ${choice==='unpublish'?`<p class="amber">Removes global publication, takeover and traffic-distribution settings, plus this Service's Flightbay-owned weighted-split HTTPRoute and per-cluster Service. The application Service stays local. Other clusters keep their configuration until you stop them too.</p>`:choice==='publish'?`<p class="muted">Sets <code>solo.io/service-scope: global</code> on this existing Service so it contributes endpoints. Other labels and settings are preserved.</p>`:''}`
      :`<p class="muted">No local ${esc(svc.namespace+'/'+svc.name)} Service is captured. This cluster can still consume the global name; local workloads are needed only to contribute endpoints.</p><div class="am-global-missing-actions"><button data-amglobalsetup="${esc(row.cluster)}">Set up Service participation…</button></div>`}
    </article>`;
  };
  return `<section class="am-global-current"><div class="row between wrap"><h3>Current publication</h3>${rows.filter(r=>r.global||r.cleanup).length>1?'<button data-amglobalstopall>Stop publishing from every cluster</button>':''}</div><p>Published from ${published.length} of ${rows.length} cluster${rows.length===1?'':'s'}: ${esc(published.map(r=>r.cluster).join(', ')||'none')}. Nothing is changed unless you pick a different option for a cluster below.</p>${rows.map(card).join('')}</section>
    ${amGlobalRoutingMarkup(svc)}
    <div class="am-global-publication"><h3>Names</h3>${amDNSMarkup(A.result?.dns)}<p class="muted">Global names come from the generated ServiceEntries in the capture. Refresh after any change to see what the controller published.</p></div>`;
}
function amMissingServiceCommand(svc,context){const quote=s=>"'"+s.replaceAll("'","'\"'\"'")+"'";return context?`kubectl --context ${quote(context)} -n ${quote(svc.namespace)} get service ${quote(svc.name)} -o yaml`:'';}
function amGlobalSetupMarkup(svc,cluster,contexts){
  const context=amLiveContext(cluster,contexts)?.name||'',command=amMissingServiceCommand(svc,context);
  return `<div class="am-global-setup"><p><strong>${esc(cluster)} · ${esc(svc.namespace+'/'+svc.name)}</strong></p><h3>Consume the global Service</h3><p>Publish from the clusters where this Service already exists. A consuming cluster does not need its own copy of the application. Verify peering, the controller-generated global DNS name and the relevant access policies before testing remote requests.</p><h3>Contribute endpoints from this cluster</h3><p>Deploy your application's namespace, workloads and Service through your normal manifests, Helm release or GitOps pipeline. The Service must have the same namespace and name shown above, with ready endpoints. Copying a Service object alone does not deploy its application.</p><h3>Check before deploying</h3><p>Missing capture evidence does not prove that the live Service is absent. Check the intended cluster first; if it exists, refresh this capture with its namespace included.</p>${amContextControl(cluster,contexts,context,'id="am-global-setup-context"')}<pre class="code" id="am-global-setup-command">${esc(command||'Choose the matching context to see the read-only check command.')}</pre><button id="am-global-copy-check" ${command?'':'disabled'}>Copy check command</button><p>After deployment or a capture refresh, return to global publication and select this cluster if it should contribute endpoints.</p><button id="am-global-refresh-evidence">Refresh Service evidence</button></div>`;
}
async function amGlobalSetupGuide(cluster){
  const A=S.ambient,svc=amService();if(!svc)return;
  try{if(!A.contexts)A.contexts=await api('api/ambient/contexts');modal('Set up Service participation',amGlobalSetupMarkup(svc,cluster,A.contexts));
    let context=amLiveContext(cluster,A.contexts)?.name||'';
    const copy=$('#am-global-copy-check');copy.onclick=()=>copyTerminalCommand(amMissingServiceCommand(svc,context),copy);
    const select=$('#am-global-setup-context');if(select)select.onchange=()=>{context=select.value;$('#am-global-setup-command').textContent=amMissingServiceCommand(svc,context)||'Choose the matching context.';copy.disabled=!context;};
    $('#am-global-refresh-evidence').onclick=()=>{closeModal();A.inventory=null;amInvalidate();A.phase=2;render();};
  }catch(e){toast(e.message);}
}
async function amGenerateGlobalBatch(){
  amCapture();const A=S.ambient,svc=amService(),current=amGlobalPublished(svc),edits=current?amGlobalPendingEdits(svc):null;
  const selected=current?edits.map(e=>e.cluster):A.settings.globalClusters||[svc.cluster];
  if(!selected.length)throw new Error(current?'Nothing to change. Choose a cluster to stop or start publishing from, or change the routing across clusters.':'Select at least one participating cluster.');
  const rows=[];
  for(const cluster of selected){
    const action=current?edits.find(e=>e.cluster===cluster).action:'publish';
    const row={cluster,action,context:amLiveContext(cluster,A.contexts)?.name||'',plan:null,validation:null,state:'Preview',error:'',applied:false};
    try{row.plan=await apiPost('api/ambient/plan',{cluster,namespace:svc.namespace,service:svc.name,tasks:['global-service'],...(action==='unpublish'?{unpublish:true}:amGlobalRoutingRequest(svc))});if(!row.plan.changes?.length&&!row.plan.objects.length)row.state=action==='unpublish'?'Already local':'Already global';}catch(e){row.error=e.message;row.state='Preview failed';}
    rows.push(row);
  }
  A.globalBatch=rows;A.batchKind='global';A.plan=null;A.validation=null;A.phase=3;
}
function amGlobalBatchPanel(){
  const A=S.ambient,rows=A.globalBatch;
  for(const row of rows)if(!row.context&&!row.validation)row.context=amLiveContext(row.cluster,A.contexts)?.name||'';
  const pending=rows.filter(r=>r.plan&&!r.applied&&(r.plan.changes?.length||r.plan.objects.length));
  const failedPreview=rows.some(r=>!r.plan);
  return `<section class="panel am-review am-global-review">
    <div class="panel-head"><div><h2>Review ${A.batchKind==='configuration'?'configuration':'global publication'} · ${esc(amService()?.namespace||amTarget()?.namespace||'')}/${esc(amService()?.name||amTarget()?.name||'')}</h2><p>Separate plans and operations per cluster. Clusters you left unchanged are not touched.</p></div><button id="am-back-options">← Edit clusters</button></div>
    <div class="panel-body"><p>Live cluster connections are reused automatically. Review and apply runs any required server dry-runs, then opens the final confirmation. Failures in one row do not roll back another cluster; successful changes appear in Active tests with undo available.</p>
    <div class="am-wizard-footer"><button id="am-global-validate-all" ${failedPreview||!pending.length||pending.some(r=>!r.context)?'disabled':''}>Dry-run only</button><button id="am-global-apply-all" class="primary" ${failedPreview||!pending.length||pending.some(r=>!r.context)?'disabled':''}>Review and apply…</button></div>
    ${rows.map((row,i)=>`<article class="am-global-plan">
      <div class="row between wrap"><h3>${esc(row.cluster)}${row.action==='unpublish'?' · stop publishing':''}</h3><span class="badge ${row.applied||amGlobalNoop(row)?'green':'blue'}">${esc(row.state)}</span></div>
      ${row.error?`<p class="red" role="alert">${esc(row.error)}</p>`:''}
      ${row.plan?`<p>${(row.plan.changes||[]).filter(c=>!c.delete).length} existing resources to update · ${(row.plan.changes||[]).filter(c=>c.delete).length} to delete · ${row.plan.objects.length} to create</p>
        <details class="am-global-yaml" open><summary>YAML for ${esc(row.cluster)}</summary><pre class="code">${esc(row.plan.yaml)}</pre></details>
        ${!amGlobalNoop(row)?amContextControl(row.cluster,A.contexts,row.context,`data-amglobalcontext="${i}" ${row.applied?'disabled':''}`):''}
        <div class="row wrap"><button data-amglobalcopy="${i}">Copy YAML</button>${!amGlobalNoop(row)?`<button data-amglobalvalidate="${i}" ${!row.context||row.applied?'disabled':''}>Dry-run only</button><button class="primary" data-amglobalapply="${i}" ${!row.context||row.applied?'disabled':''}>Review and apply to ${esc(row.cluster)}…</button>`:''}</div>
        ${row.validation&&!row.applied?'<p class="green">Validated for this exact context and plan. Apply within ten minutes.</p>':''}`:''}
    </article>`).join('')}${amTaskLinks(A.batchKind==='configuration'?(rows[0]?.plan?.request.tasks||[]):['global-service'])}</div></section>`;
}
async function amGlobalOperation(index,operation){
  const A=S.ambient,row=A.globalBatch[index];row.error='';
  if(!row.context||operation==='apply'&&!row.validation){row.error=operation==='apply'?'Dry-run this exact plan in the selected context before applying.':'Choose the matching real kubeconfig context first.';row.state='Needs attention';return;}
  row.state=operation==='validate'?'Validating · no changes saved…':'Applying to '+row.context+'…';
  const badge=document.querySelectorAll('.am-global-plan')[index]?.querySelector('.badge');if(badge){badge.className='badge blue';badge.innerHTML='<span class="bd-spinner" aria-hidden="true"></span>'+esc(row.state);}
  try{
    if(operation==='validate'){row.validation=null;const result=await apiPost('api/ambient/test',{operation:'validate',context:row.context,request:row.plan.request,planId:row.plan.id});row.validation=result;row.state='Dry-run passed';}
    else{const result=await apiPost('api/ambient/test',{operation:'apply',context:row.context,validation:row.validation.token});row.applied=true;row.validation=null;row.state='Applied · traffic not verified';row.message=result.message;}
  }catch(e){row.error=e.message;row.validation=null;row.state=operation==='validate'?'Dry-run failed':'Apply failed';}
  if(badge){badge.className='badge '+(row.error?'red':'green');badge.textContent=row.state;}
  A.tests=await api('api/ambient/tests').catch(()=>A.tests);
  // Once every cluster with changes is applied, go back to the Service's
  // configuration list, where Retest traffic and the new state are shown.
  const pending=(A.globalBatch||[]).filter(r=>r.plan&&(r.plan.changes?.length||r.plan.objects?.length));
  if(operation==='apply'&&row.applied&&pending.every(r=>r.applied)&&typeof amRefreshAppliedConfiguration==='function'){if(A.batchKind!=='configuration')await amGlobalWaitForCapture(amService(),pending);const source=(A.globalBatch||[]).find(r=>r.cluster===amService()?.cluster&&r.plan)||row;await amRefreshAppliedConfiguration(source.plan);A.globalBatch=null;A.settings={};A.phase=1;A.edit=null;A.tasks={};A.mode='';await render();$('#am-retest-current')?.scrollIntoView({block:'center'});}
}
function amWireGlobalPublication(){
  const A=S.ambient;
  document.querySelectorAll('[data-am-global-config]').forEach(b=>b.onclick=()=>{amSaveConfigurationDraft();amResetDraft(true);A.tab='wizard';A.tasks={'global-service':true};A.phase=2;render();});
  const publicationRows=amService()?amGlobalTargets(amService()):[];
  document.querySelectorAll('.am-global-cluster').forEach(el=>{
    const cluster=el.querySelector('[data-amglobalcluster]')?.dataset.amglobalcluster,row=publicationRows.find(r=>r.cluster===cluster);if(!row?.global)return;
    const aliases=row.publication?.globalAliases||[],allocated=aliases.length>0&&aliases.every(a=>a.addresses?.length);
    const badge=el.querySelector('.badge');if(badge){badge.className='badge '+(allocated?'blue':'amber');badge.textContent='Export label set';}
    el.querySelector('span')?.insertAdjacentHTML('beforeend',`<small class="am-publication-evidence" role="status">${esc(amPublicationEvidenceLabel(row))}. API metadata alone does not prove proxy delivery or failover.</small>`);
  });
  // addEventListener: the generic parameter handler later replaces onchange.
  document.querySelectorAll('[data-amglobaledit]').forEach(input=>input.addEventListener('change',()=>{amCapture();amInvalidate();render();}));
  document.querySelectorAll('[data-amglobalstopall]').forEach(b=>b.onclick=()=>{const svc=amService(),F=S.ambient.settings;F.globalEdits=Object.fromEntries(amGlobalTargets(svc).filter(r=>r.global||r.cleanup).map(r=>[r.cluster,'unpublish']));amInvalidate();render();});
  document.querySelectorAll('[data-amglobalweight]').forEach(i=>i.addEventListener('change',()=>{const F=S.ambient.settings;F.globalWeights=F.globalWeights||{};F.globalWeights[i.dataset.amglobalweight]=Number(i.value)||0;amInvalidate();render();}));
  document.querySelectorAll('[data-amglobaldist], #am-global-takeover').forEach(input=>input.addEventListener('change',()=>{const F=S.ambient.settings,d=document.querySelector('[data-amglobaldist]:checked');if(d)F.globalDistribution=d.value;const t=$('#am-global-takeover');if(t)F.globalTakeover=t.checked;amInvalidate();render();}));
  document.querySelectorAll('[data-amglobalsetup]').forEach(b=>b.onclick=()=>amGlobalSetupGuide(b.dataset.amglobalsetup));
  document.querySelectorAll('.am-global-plan').forEach((el,i)=>{const row=A.globalBatch[i],badge=el.querySelector('.badge'),ok=row.applied||!!row.validation||amGlobalNoop(row);badge.className='badge '+(row.error?'red':ok?'green':'blue');badge.textContent=(row.error?'✕ ':ok?'✓ ':'')+row.state;});
  document.querySelectorAll('.am-global-plan').forEach((el,i)=>{const row=A.globalBatch[i];if(A.batchKind==='configuration'||!amGlobalNoop(row))return;const evidence=publicationRows.find(r=>r.cluster===row.cluster),aliases=evidence?.publication?.globalAliases||[],allocated=aliases.length>0&&aliases.every(a=>a.addresses?.length),badge=el.querySelector('.badge');badge.className='badge '+(allocated?'blue':'amber');badge.textContent=amPublicationEvidenceLabel({...evidence,global:true});});
  document.querySelectorAll('[data-amglobalcluster]').forEach(box=>box.onchange=()=>{amCapture();A.globalBatch=null;amInvalidate();});
  document.querySelectorAll('[data-amglobalcontext]').forEach(select=>select.onchange=()=>{const row=A.globalBatch[Number(select.dataset.amglobalcontext)];row.context=select.value;row.validation=null;row.error='';row.state='Preview';render();});
  document.querySelectorAll('[data-amglobalcopy]').forEach(button=>button.onclick=()=>copyTerminalCommand(A.globalBatch[Number(button.dataset.amglobalcopy)].plan.yaml));
  document.querySelectorAll('[data-amglobaldownload]').forEach(button=>button.onclick=()=>{const row=A.globalBatch[Number(button.dataset.amglobaldownload)];amDownload(row.plan.yaml,'flightbay-global-'+row.cluster.replace(/[^a-zA-Z0-9._-]/g,'_')+'.yaml');});
  for(const [attribute,operation]of [['amglobalvalidate','validate'],['amglobalapply','apply']])document.querySelectorAll('[data-'+attribute+']').forEach(button=>button.onclick=async()=>{
    if(A.busy)return;const index=Number(button.dataset[attribute]),row=A.globalBatch[index];
    if(operation==='apply'){await amAction(()=>amValidatePlans([row]),'Checking this cluster before final confirmation…');if(A.error||!amValidated(row)||!A.globalBatch?.includes(row)||!await amConfirmApply([row]))return;}
    return amAction(()=>amGlobalOperation(index,operation),operation==='validate'?'Checking this cluster with a server dry-run…':'Applying the confirmed publication plan…');
  });
  for(const [id,operation]of [['am-global-validate-all','validate'],['am-global-apply-all','apply']])if($('#'+id))$('#'+id).onclick=async()=>{
    if(A.busy)return;const pending=A.globalBatch.filter(row=>row.plan&&!row.applied&&(row.plan.changes?.length||row.plan.objects.length));if(!pending.length)return;
    if(operation==='apply'){await amAction(()=>amValidatePlans(pending),'Checking all pending clusters before final confirmation…');if(A.error||pending.some(row=>!amValidated(row)||!A.globalBatch?.includes(row))||!await amConfirmApply(pending))return;}
    return amAction(async()=>{for(let i=0;i<(A.globalBatch?.length||0);i++){const row=A.globalBatch[i];if(!row.plan||row.applied||(!row.plan.changes?.length&&!row.plan.objects.length))continue;await amGlobalOperation(i,operation);if(operation==='apply'&&row.error)break;}},operation==='validate'?'Dry-running each selected cluster. No changes are being saved…':'Applying confirmed cluster plans in sequence. Each row reports its own result…');
  };
  const rows=A.globalBatch;if(rows&&$('.am-global-review .am-wizard-footer')){const pending=rows.filter(r=>r.plan&&!r.applied&&(r.plan.changes?.length||r.plan.objects.length)),reason=rows.some(r=>!r.plan)?'A preview failed. Edit clusters and regenerate before continuing.':!pending.length?'No pending changes. These Services are already global or their publication plans have been applied.':pending.some(r=>!r.context)?'Choose a context only for clusters without a matching live connection.':'Review and apply runs the required dry-runs automatically, then asks for confirmation. Dry-run only saves no changes.';$('.am-global-review .am-wizard-footer').insertAdjacentHTML('afterend',`<p class="am-global-prerequisite" role="status">${esc(reason)}</p>`);}
}

function amConfirmApply(entries){
  if(S.applyConfirmCancel||!entries.length)return Promise.resolve(false);
  return new Promise(resolve=>{
    let finished=false;
    const finish=accepted=>{if(finished)return;finished=true;S.applyConfirmCancel=null;document.removeEventListener('keydown',escape,true);closeModal();resolve(accepted);};
    const escape=e=>{if(e.key==='Escape'){e.preventDefault();e.stopImmediatePropagation();finish(false);}};
    modal('Are you sure you want to apply?',`<div class="am-apply-confirm"><div class="notice bad"><div><strong>Direct change to a live Kubernetes cluster</strong><p>This writes to the selected contexts immediately and can change production traffic. Check your deployment/GitOps process before bypassing it. Dry-run is validation, not approval or proof of working traffic.</p></div></div>${entries.map(entry=>{const server=S.ambient.contexts?.find(c=>c.name===entry.context)?.server||'API server recorded by validation';return `<section><h3>${esc(entry.context)}</h3><p class="mono">${esc(server)}</p><p>Captured cluster: <strong>${esc(entry.plan.request.cluster)}</strong> · ${esc(entry.plan.request.namespace+'/'+(entry.plan.request.service||entry.plan.request.targetName||''))} · plan <code>${esc(entry.plan.id)}</code></p><h4>YAML to apply</h4><pre class="code">${esc(entry.plan.yaml)}</pre></section>`;}).join('')}<p>Each cluster is applied separately. If a later operation fails, earlier successful changes remain and can be undone from Active tests.</p><div class="row between"><button id="am-confirm-cancel">Cancel</button><button id="am-confirm-apply" class="am-danger-apply">Apply ${entries.length===1?'to '+esc(entries[0].context):'to '+entries.length+' clusters'}</button></div></div>`);
    S.applyConfirmCancel=()=>finish(false);document.addEventListener('keydown',escape,true);
    $('#am-confirm-cancel').onclick=()=>finish(false);$('#am-confirm-apply').onclick=()=>finish(true);$('#am-confirm-cancel').focus({preventScroll:true});
  });
}

function amGlobalRoutingRequest(svc){const F=S.ambient.settings,cur=amGlobalRouting(svc),out={};const d=F.globalDistribution??cur.distribution;if(d==='weighted')out.globalWeights=amGlobalWeights(svc);else if(F.globalDistribution!==undefined||d!=='default')out.globalDistribution=d;if(F.globalTakeover!==undefined)out.globalTakeover=F.globalTakeover;return out;}
function amGlobalWeights(svc){const F=S.ambient.settings,rows=amGlobalTargets(svc).filter(r=>r.service),live=amGlobalRouting(svc).weights;F.globalWeights=F.globalWeights||(live?{...live}:Object.fromEntries(rows.map((r,i)=>[r.cluster,i===0?70:Math.round(30/Math.max(rows.length-1,1))])));return F.globalWeights;}
function amGlobalWeightsMarkup(svc){
  const w=amGlobalWeights(svc),total=Object.values(w).reduce((a,b)=>a+Number(b||0),0);
  return `<div class="am-global-weights">${Object.keys(w).map(c=>`<label>${esc(c)} <input type="number" min="0" max="1000" data-amglobalweight="${esc(c)}" value="${esc(w[c])}"> <small class="muted">${total?Math.round(100*w[c]/total):0}%</small></label>`).join('')}</div><p class="muted">Each cluster gets a Service <code>${esc(svc.name)}-&lt;cluster&gt;</code> that selects only its own Pods, published globally, and an HTTPRoute on <code>${esc(svc.name)}</code> sends each request to one of those hostnames by weight. It needs an Istio waypoint on ${esc(svc.name)} in each cluster whose callers should be split, and is per request.</p>`;
}

function amGlobalNoop(row){return ['Already global','Already local'].includes(row.state)||!!row.plan&&!row.plan.changes?.length&&!row.plan.objects?.length;}
// Wait until the live mirror shows the new publication state, so the
// configuration list does not offer the same change again.
async function amGlobalWaitForCapture(svc,rows){
  for(let i=0;i<20;i++){
    const inv=await api('api/ambient').catch(()=>null);
    const ok=inv&&rows.every(r=>{const c=(inv.topology?.clusters||[]).find(x=>x.context===r.cluster);const s=c?.services.find(x=>x.namespace===svc.namespace&&x.name===svc.name);const global=!!s&&['global','global-only'].includes(s.scope),q=r.plan?.request||{};if(r.action==='unpublish')return !!s&&!global&&!s.globalCleanup;if(!global)return false;if(q.globalWeights&&!amGlobalWeightsEqual(q.globalWeights,s.globalWeights))return false;if(q.globalDistribution&&(s.globalWeights||(s.distribution||'default')!==q.globalDistribution))return false;return q.globalTakeover===undefined||!!s.takeover===q.globalTakeover;});
    if(ok){S.ambient.inventory=inv;return;}
    await new Promise(res=>setTimeout(res,500));
  }
}
