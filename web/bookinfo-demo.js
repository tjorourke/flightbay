'use strict';

function bdSavedNamespace(){try{return localStorage.getItem('flightbay.bookinfo.namespace')||'bookinfo';}catch{return 'bookinfo';}}
const BD={catalog:null,namespace:bdSavedNamespace(),selected:{},classes:{},waypointScope:'namespace',waypointName:'bookinfo-waypoint',traffic:true,global:false,entries:[],contexts:[],busy:false,error:'',notice:'',removal:null};
function bdWaypointNames(){return BD.waypointScope==='service'?['productpage','details','ratings','reviews'].map(s=>({service:s,name:BD.waypointName+'-'+s})):[{service:'Namespace default',name:BD.waypointName}];}
function bdWaypointChoices(){return `<fieldset class="bd-waypoint-options"><legend>Create new waypoints</legend><p class="bd-help">These are new waypoint Gateways, not existing instances. Choose how Bookinfo's main Services will use them.</p><div class="bd-scope-cards">${[['namespace','One new waypoint per namespace','All Bookinfo Services share the namespace default.'],['service','One new waypoint per main Service','Four dedicated waypoints: productpage, details, ratings and reviews. No namespace default.']].map(([value,title,detail])=>`<label class="bd-scope-card ${BD.waypointScope===value?'selected':''}"><input type="radio" name="bd-waypoint-scope" value="${value}" ${BD.waypointScope===value?'checked':''}><span><strong>${title}</strong><small>${detail}</small></span></label>`).join('')}</div><label class="bd-waypoint-name">${BD.waypointScope==='service'?'New waypoint name prefix':'New waypoint name'}<input id="bd-waypoint-name" value="${esc(BD.waypointName)}" placeholder="bookinfo-waypoint"></label><div class="bd-binding-preview"><strong>Will be created in each selected cluster</strong>${bdWaypointNames().map(x=>`<div><span>${esc(x.service)}</span><code>${esc(x.name)}</code></div>`).join('')}<p>${BD.waypointScope==='service'?'The four main Services get explicit waypoint labels. reviews-v1/v2 remain direct routing backends.':'The namespace default is set only after the new waypoint reports Programmed.'}</p></div></fieldset>`;}
async function bookinfoPage(){
  if(!BD.catalog)BD.catalog=await api('api/ambient/bookinfo',{namespace:BD.namespace});
  if(BD.recovered!==BD.namespace&&!BD.busy){await bdRecover();BD.recovered=BD.namespace;}
  const selected=BD.catalog.clusters.filter(c=>BD.selected[c.cluster]),globalOK=selected.length>0&&selected.every(c=>c.globalSupported);
  return `<section class="rp-intro"><div><span class="rp-eyebrow">PACKAGED DEMO</span><h2>Deploy Bookinfo</h2><p>Install the same known configuration on one or more clusters. Review the exact YAML, validate it against each context, then confirm before anything is created.</p></div><span class="rp-readonly">No changes until you confirm Apply</span></section>
  <section class="panel bd-setup"><div class="panel-body"><label>Create a new namespace<input id="bd-namespace" value="${esc(BD.namespace)}" placeholder="bookinfo"></label><p class="bd-help">Enter bookinfo or another fresh name, such as bookinfo-demo. The first confirmation creates it on every selected cluster. Existing namespaces and application resources are never overwritten.</p>${bdWaypointChoices()}<h3>Where should Bookinfo run?</h3><div class="bd-clusters">${BD.catalog.clusters.map(c=>`<article class="bd-cluster"><label class="bd-cluster-check"><input type="checkbox" data-bdcluster="${esc(c.cluster)}" ${BD.selected[c.cluster]?'checked':''} ${!c.anchor||!c.classes.length?'disabled':''}><span><strong>${esc(c.cluster)}</strong><small>${!c.anchor?'Namespace identity missing from capture':c.installed?'Bookinfo already present':c.namespaceState==='prepared'?'Demo namespace ready':c.namespaceState==='occupied'?'Namespace exists; choose a fresh namespace':'Ready for a clean installation'}</small></span></label><label>New waypoint type (GatewayClass)<select data-bdclass="${esc(c.cluster)}"><option value="">Choose implementation…</option>${c.classes.map(cl=>`<option value="${esc(cl.name)}" ${(BD.classes[c.cluster]||bdDefaultClass(c))===cl.name?'selected':''}>${esc(cl.name)} · ${esc(cl.product==='istio'?'Istio':cl.product)} ${esc(cl.edition)}${cl.product==='istio'?' · recommended for ambient':' · enforces agentgateway policies, not Istio AuthorizationPolicy'}</option>`).join('')}</select></label><small>This selects the controller implementation for the new waypoint${BD.waypointScope==='service'?'s':''}, not an existing waypoint.</small><small>${esc(c.globalReason)}</small>${c.ownedResources?`<button type="button" class="bd-remove-demo" data-bdremove="${esc(c.cluster)}">Remove Bookinfo…</button>`:''}</article>`).join('')}</div>
  <div class="bd-options"><label><input id="bd-global" type="checkbox" ${BD.global?'checked':''} ${!globalOK?'disabled':''}><span><strong>Publish the four main Services globally</strong><small>Productpage, details, ratings and reviews contribute endpoints from each installed cluster. Existing peering and Enterprise Istio are required.</small></span></label><label><input id="bd-traffic" type="checkbox" ${BD.traffic?'checked':''}><span><strong>Generate demo traffic</strong><small>A small curl Pod calls Productpage every five seconds, which exercises the Bookinfo dependencies.</small></span></label></div><button class="primary" id="bd-review" ${BD.busy||!selected.length?'disabled':''}>${BD.busy?'Working…':'Review installation →'}</button></div></section>
   ${BD.busy?'<div class="notice" role="status" aria-busy="true"><span class="bd-spinner" aria-hidden="true"></span>'+(BD.waiting?esc(BD.waiting):'Working on the selected installation stage. Per-cluster results appear below.')+'</div>':''}${BD.error?`<div class="notice bad" role="alert">${esc(BD.error)}</div>`:''}${BD.notice?`<div class="notice" role="status">${esc(BD.notice)}</div>`:''}
  <div id="bd-plans">${BD.entries.length?bdPlans():''}</div>
  <details class="panel bd-files"><summary>Bundled YAML and image sources</summary><div class="panel-body"><p>The manifests below ship inside the Flightbay binary. The reviewed plan fills in the namespace, selected waypoint class and global-publication labels.</p>${BD.catalog.images.map(image=>`<code class="bd-image">${esc(image)}</code>`).join('')}${BD.catalog.notes.map(n=>`<p>${esc(n)}</p>`).join('')}${BD.catalog.files.map((f,i)=>`<details><summary>${esc(f.name)}</summary><pre class="code">${esc(f.yaml)}</pre><button data-bdfile="${i}">Download this YAML</button></details>`).join('')}</div></details>`;
}
function bdPlans(){
  const pending=BD.entries.filter(e=>!e.record),advance=BD.entries.some(e=>e.record&&e.request.demo.stage==='namespace');
  return `<section class="panel bd-review"><div class="panel-head"><div><h3>Review each cluster</h3><p class="bd-help">One confirmation deploys everything: the namespace first, then the workloads and waypoint, each with a server dry-run before it is applied.</p></div></div><div class="panel-body"><div class="bd-actions"><button id="bd-validate-all" ${BD.busy||!pending.length||pending.some(e=>!e.context)?'disabled':''}>Dry-run only</button><button class="primary" id="bd-apply-all" ${BD.busy||!pending.length||pending.some(e=>!e.context)?'disabled':''}>Deploy Bookinfo…</button>${advance?`<button id="bd-advance" ${BD.busy?'disabled':''}>Continue to Bookinfo workloads →</button>`:''}</div>
  ${BD.entries.map(bdEntryMarkup).join('')}</div></section>`;
}
function bdEntryMarkup(e,i){
  const namespaceStage=e.request.demo.stage==='namespace';
  return `<article class="bd-entry">
    <div class="bd-entry-title"><h3>${esc(e.cluster)}</h3><span id="bd-state-${i}" class="badge ${e.error?'red':e.progress?.state==='ready'?'green':'blue'}">${esc(bdEntryState(e))}</span></div>
    <p><strong>${namespaceStage?'Stage 1 · Prepare namespace':'Stage 2 · Deploy Bookinfo'}</strong> · ${esc(e.request.namespace)} · ${e.plan.objects.length+(e.plan.changes?.length||0)} resources</p>
    ${e.namespaceRecord?'<p class="green">Namespace ready.</p>':''}
    <div id="bd-progress-${i}">${bdProgress(e)}</div>
    ${amContextControl(e.cluster,BD.contexts,e.context,`data-bdcontext="${i}" ${BD.busy||e.record?'disabled':''}`)}
    <details ${!e.record&&!e.applying?'open':''}><summary>Exact YAML to apply · ${(e.plan.objects?.length||0)+(e.preview?.objects?.length||0)} resources</summary><pre class="code">${esc(e.plan.yaml+(e.preview?'\n---\n# Then, once the namespace exists:\n'+e.preview.yaml:''))}</pre><div class="bd-actions"><button data-bdcopy="${i}">Copy YAML</button><button data-bddownload="${i}">Download YAML</button></div></details>
    ${e.error?`<div class="notice bad" role="alert">${esc(e.error)}</div>`:''}${e.message?`<p role="status">${esc(e.message)}</p>`:''}
    ${e.record&&!namespaceStage?`<div class="bd-actions"><button data-bdstatus="${i}" ${BD.busy?'disabled':''}>Check deployment readiness</button><button data-bdexplore="${i}">Open demo request path →</button><button data-bdundo="${i}" ${BD.busy?'disabled':''}>Remove these demo workloads</button></div>`:''}
  </article>`;
}
function bdStatus(rows){return `<div class="table-scroll"><table><thead><tr><th>Resource</th><th>State</th></tr></thead><tbody>${rows.map(row=>{const o=row.object||row.changed||{},s=row.status||{};let state=row.changed?'Updated':'Created';if(row.error)state=row.error;else if(o.kind==='Deployment')state=s.observedGeneration>=row.generation&&(s.availableReplicas||0)>0?'Ready':'Waiting for ready Pods';else if(['Gateway','HTTPRoute'].includes(o.kind)){const types=o.kind==='Gateway'?['Accepted','Programmed']:['Accepted','ResolvedRefs'];const conditions=o.kind==='Gateway'?(s.conditions||[]):(s.parents||[]).flatMap(p=>p.conditions||[]);state=types.every(t=>conditions.some(c=>c.type===t&&c.status==='True'&&c.observedGeneration===row.generation))?'Ready':'Waiting for controller status';}return `<tr><td>${esc(o.kind)} ${esc(o.namespace?o.namespace+'/':'')}${esc(o.name)}</td><td>${esc(state)}</td></tr>`;}).join('')}</tbody></table></div>`;}
function bdEntryState(e){return e.error||e.progress?.state==='failed'?'Needs attention':e.applying?'Applying…':e.queued?'Queued for Apply':e.progress?.state==='ready'?'Bookinfo ready':e.progress?.state==='namespace-ready'?'Namespace ready · workloads not deployed':e.record?(e.request.demo.stage==='namespace'?'Namespace created · workloads not deployed':'Applied · checking readiness'):e.validation?'Dry-run passed · not applied':'Preview only';}
function bdProgress(e){const p=e.progress;if(!p)return e.applying?'<p role="status"><span class="bd-spinner" aria-hidden="true"></span>Creating reviewed resources. Waiting for live progress…</p>':'';return `<div class="bd-progress" role="status" aria-live="polite"><strong>${!['ready','namespace-ready','failed'].includes(p.state)?'<span class="bd-spinner" aria-hidden="true"></span>':''}${esc(p.message)}</strong><p>${p.created}/${p.expected} configuration resources recorded · ${p.ready}/${p.expected} ready · checked ${esc(new Date(p.checkedAt).toLocaleTimeString())}</p>${e.pollStopped?'<p>Automatic checks paused after ten minutes. Check readiness again to resume.</p>':''}<details class="bd-resources" data-bd-resources="${esc(e.cluster)}" ${BD.resourcesClosed?.[e.cluster]?'':'open'}><summary>Per-resource progress</summary><table><tbody>${(p.resources||[]).map(r=>`<tr><td>${esc(r.ref.kind)} ${esc(r.ref.name)}</td><td>${esc(r.state)} · ${esc(r.detail)}</td></tr>`).join('')}</tbody></table></details></div>`;}
async function bdRecover(){
  const records=await api('api/ambient/bookinfo/installations',{namespace:BD.namespace});
  if(BD.entries.length)return;
  const latest=new Map();for(const r of records){const q=r.plan?.request;if(!q?.demo||q.namespace!==BD.namespace||r.applyState==='removed'||(!r.applyState&&!(r.created||[]).length&&!(r.modified||[]).length))continue;const prev=latest.get(q.cluster);if(!prev||q.demo.stage==='workloads'&&prev.plan.request.demo.stage==='namespace'||q.demo.stage===prev.plan.request.demo.stage&&new Date(r.validated)>new Date(prev.validated))latest.set(q.cluster,r);}
  if(!latest.size)return;
  BD.contexts=await api('api/ambient/contexts');
  BD.entries=[...latest.values()].map(r=>({cluster:r.plan.request.cluster,context:r.context,request:r.plan.request,plan:r.plan,record:r.applyState==='failed'&&!(r.created||[]).length&&!(r.modified||[]).length?null:r,validation:null,applying:r.applyState==='applying',error:r.applyState==='failed'?r.applyError:'',message:r.applyState==='failed'?'Earlier Apply failed. Inspect the error and validate again before retrying.':''}));
}
function bdSchedulePoll(){clearTimeout(BD.pollTimer);if(S.view!=='ambient'||S.ambient.tab!=='demo'||BD.polling)return;if(BD.entries.some(e=>(e.record||e.applying)&&!e.pollStopped&&!['ready','namespace-ready','failed'].includes(e.progress?.state)))BD.pollTimer=setTimeout(bdPoll,2000);}
async function bdPoll(){
  if(BD.polling||S.view!=='ambient'||S.ambient.tab!=='demo')return;BD.polling=true;
  try{await Promise.all(BD.entries.filter(e=>(e.record||e.applying)&&!e.pollStopped&&!['ready','namespace-ready','failed'].includes(e.progress?.state)).map(async e=>{
    e.pollStarted||=Date.now();const plan=e.plan;
    try{const progress=await api('api/ambient/bookinfo/progress',{cluster:e.cluster,namespace:e.request.namespace,context:e.context,planId:plan.id});if(e.plan!==plan||!BD.entries.includes(e))return;e.progress=progress;e.pollError='';if(!['pending','applying'].includes(progress.state))e.applying=false;}catch(err){e.pollError='Live readiness check failed: '+err.message;}
    if(Date.now()-e.pollStarted>600000)e.pollStopped=true;
    const i=BD.entries.indexOf(e),el=$('#bd-progress-'+i);if(el){el.innerHTML=(e.pollError?`<p role="alert">${esc(e.pollError)}</p>`:'')+bdProgress(e);bdWireResources(el);}const badge=$('#bd-state-'+i);if(badge){badge.textContent=bdEntryState(e);badge.className='badge '+(e.error||e.progress?.state==='failed'?'red':e.progress?.state==='ready'?'green':'blue');}
  }));}finally{BD.polling=false;bdSchedulePoll();}
}
async function bdAdvance(){for(const e of BD.entries.filter(e=>e.record&&!e.error&&e.request.demo.stage==='namespace')){
  e.message='Namespace created. Preparing the workloads…';await render();let row;
  for(let i=0;i<30;i++){await bdRefresh();row=BD.catalog.clusters.find(c=>c.cluster===e.cluster);if(row?.namespaceState==='prepared')break;await new Promise(r=>setTimeout(r,1000));}
  if(row?.namespaceState!=='prepared')throw new Error(e.cluster+': namespace created, but capture is not ready. Use Continue to prepare the workload preview.');
  const request={...e.request,demo:{...e.request.demo,stage:'workloads'}},plan=await apiPost('api/ambient/plan',request);
  e.namespaceRecord=e.record;e.request=request;e.plan=plan;e.record=null;e.validation=null;e.progress=null;e.pollStarted=null;e.message='';
}}
function bdInvalidate(){BD.entries=[];BD.error='';}
async function bdRun(fn){if(BD.busy)return;BD.busy=true;BD.error='';try{await render();await fn();}catch(e){BD.error=e.message;}finally{BD.busy=false;await render();}}
async function bdRefresh(){BD.catalog=await api('api/ambient/bookinfo',{namespace:BD.namespace});S.ambient.inventory=null;RP.options=null;}
function bookinfoAfter(){
  bdWireResources();
  document.querySelectorAll('[data-amtab]').forEach(b=>b.onclick=()=>{S.ambient.tab=b.dataset.amtab;render();});$('#am-start-new').onclick=()=>amClear(true);$('#am-refresh').onclick=()=>bdRun(async()=>{await bdRefresh();});
  $('#bd-namespace').onchange=e=>{BD.namespace=e.target.value.trim();try{localStorage.setItem('flightbay.bookinfo.namespace',BD.namespace);}catch{}BD.catalog=null;bdInvalidate();render();};document.querySelectorAll('[data-bdcluster]').forEach(b=>b.onchange=()=>{BD.selected[b.dataset.bdcluster]=b.checked;bdInvalidate();if(!BD.catalog.clusters.filter(c=>BD.selected[c.cluster]).every(c=>c.globalSupported))BD.global=false;render();});
  document.querySelectorAll('[name="bd-waypoint-scope"]').forEach(b=>b.onchange=()=>{if(b.checked){BD.waypointScope=b.value;bdInvalidate();render();}});$('#bd-waypoint-name').onchange=e=>{BD.waypointName=e.target.value.trim();bdInvalidate();render();};
  document.querySelectorAll('[data-bdclass]').forEach(b=>b.onchange=()=>{BD.classes[b.dataset.bdclass]=b.value;bdInvalidate();render();});$('#bd-global').onchange=e=>{BD.global=e.target.checked;bdInvalidate();render();};$('#bd-traffic').onchange=e=>{BD.traffic=e.target.checked;bdInvalidate();render();};
  $('#bd-review').onclick=async()=>{await bdRun(async()=>{await bdRefresh();
    // A namespace still being deleted from an earlier removal rejects new
    // objects; wait (up to two minutes) until it is gone, then deploy fresh.
    for(let i=0;i<60&&BD.catalog.clusters.some(c=>BD.selected[c.cluster]&&c.namespaceState==='terminating');i++){BD.error='';BD.waiting='Waiting for namespace '+BD.namespace+' to finish deleting on '+BD.catalog.clusters.filter(c=>BD.selected[c.cluster]&&c.namespaceState==='terminating').map(c=>c.cluster).join(', ')+'…';await render();await new Promise(r=>setTimeout(r,2000));await bdRefresh();}
    BD.waiting='';if(BD.catalog.clusters.some(c=>BD.selected[c.cluster]&&c.namespaceState==='terminating'))throw new Error('Namespace '+BD.namespace+' is still being deleted. Try again when it has gone.');
    BD.contexts=await api('api/ambient/contexts');BD.entries=[];for(const row of BD.catalog.clusters.filter(c=>BD.selected[c.cluster])){if(row.namespaceState==='occupied'||row.installed)throw new Error(row.cluster+': namespace or Bookinfo resources already exist. Choose a fresh namespace; nothing will be overwritten.');const request={cluster:row.cluster,namespace:BD.namespace,targetKind:'Namespace',targetName:BD.namespace,tasks:['bookinfo-demo'],demo:{stage:row.namespaceState==='prepared'?'workloads':'namespace',waypointClass:BD.classes[row.cluster]||bdDefaultClass(row),waypointScope:BD.waypointScope,waypointName:BD.waypointName,global:BD.global,traffic:BD.traffic}};const plan=await apiPost('api/ambient/plan',request);const preview=request.demo.stage==='namespace'?await apiPost('api/ambient/plan',{...request,demo:{...request.demo,stage:'workloads',preview:true}}).catch(()=>null):null;BD.entries.push({cluster:row.cluster,request,plan,preview,context:amLiveContext(row.cluster,BD.contexts)?.name||'',validation:null,record:null});}});if(!BD.error)await bdDeploy();};
  document.querySelectorAll('[data-bdcontext]').forEach(b=>b.onchange=()=>{const e=BD.entries[Number(b.dataset.bdcontext)];e.context=b.value;e.validation=null;render();});
  if($('#bd-validate-all'))$('#bd-validate-all').onclick=()=>bdRun(()=>amValidatePlans(BD.entries.filter(e=>!e.record),true));
  if($('#bd-apply-all'))$('#bd-apply-all').onclick=()=>bdDeploy();
  if($('#bd-advance'))$('#bd-advance').onclick=()=>bdRun(bdAdvance);
  document.querySelectorAll('[data-bdcopy]').forEach(b=>b.onclick=()=>copyTerminalCommand(BD.entries[Number(b.dataset.bdcopy)].plan.yaml));document.querySelectorAll('[data-bddownload]').forEach(b=>b.onclick=()=>{const e=BD.entries[Number(b.dataset.bddownload)];amDownload(e.plan.yaml,'bookinfo-'+e.cluster+'-'+e.request.demo.stage+'.yaml');});document.querySelectorAll('[data-bdfile]').forEach(b=>b.onclick=()=>{const f=BD.catalog.files[Number(b.dataset.bdfile)];amDownload(f.yaml,f.name);});
  document.querySelectorAll('[data-bdstatus]').forEach(b=>b.onclick=async()=>{const e=BD.entries[Number(b.dataset.bdstatus)];e.pollStopped=false;e.pollStarted=Date.now();e.progress=null;await bdPoll();});
  document.querySelectorAll('[data-bdundo]').forEach(b=>b.onclick=()=>{const e=BD.entries[Number(b.dataset.bdundo)];bdRemoveBookinfo(e.cluster,e.context);});
  document.querySelectorAll('[data-bdremove]').forEach(b=>b.onclick=()=>bdRemoveBookinfo(b.dataset.bdremove));
  document.querySelectorAll('[data-bdexplore]').forEach(b=>b.onclick=()=>{const e=BD.entries[Number(b.dataset.bdexplore)],base={cluster:e.cluster,namespace:BD.namespace,group:''};RP.options=null;RP.view='map';rpOpen({source:{...base,group:'apps',kind:'Deployment',resource:'deployments',name:'productpage-v1'},destination:{...base,kind:'Service',resource:'services',name:'reviews'}});});
  if(BD.busy)document.querySelectorAll('.bd-setup input,.bd-setup select,.bd-setup button').forEach(el=>el.disabled=true);
  bdSchedulePoll();
}

async function bdRemoveBookinfo(cluster,chosenContext){
  if(BD.removal)return;
  const namespace=BD.namespace;
  const action={cluster,namespace,phase:'preview'};BD.removal=action;
  BD.error='';BD.notice='';
  const loading=(title,message)=>modal(title,`<div class="bd-operation" role="status" aria-live="polite" aria-busy="true"><span class="bd-spinner" aria-hidden="true"></span><div><strong>${esc(message)}</strong><p>${esc(cluster)} · ${esc(namespace)}</p><p id="bd-remove-elapsed">Checking live cluster resources…</p><p>${action.phase==='remove'?'Waiting for Kubernetes to confirm recorded resources are absent. This can take a few minutes. Closing this dialog does not cancel removal.':'Checking recorded ownership, resource UIDs and waypoint consumers. Nothing is being deleted.'}</p></div></div>`);
  loading('Preparing Bookinfo removal','Loading removal preview…');
  const started=Date.now(),timer=setInterval(()=>{const el=$('#bd-remove-elapsed');if(el)el.textContent=`${Math.floor((Date.now()-started)/1000)}s elapsed · ${action.phase==='remove'?'Removal is still running; waiting for Kubernetes confirmation.':'Live ownership checks are still running.'}`;},1000);
  try{
    const contexts=await api('api/ambient/contexts'),context=chosenContext||amLiveContext(cluster,contexts)?.name;
    if(!context){modal('Choose context for Bookinfo removal',`<p>${esc(cluster)} · ${esc(namespace)}</p><label>Real kubeconfig context<select id="bd-remove-context"><option value="">Choose context…</option>${contexts.map(c=>`<option value="${esc(c.name)}">${esc(c.name)} · ${esc(c.server)}</option>`).join('')}</select></label><button id="bd-remove-preview">Preview Bookinfo resources</button>`);$('#bd-remove-preview').onclick=()=>{const value=$('#bd-remove-context').value;if(value)bdRemoveBookinfo(cluster,value);};return;}
    const preview=await apiPost('api/ambient/bookinfo/remove',{operation:'preview',cluster,namespace,context}),installations=preview.installations||[],resources=installations.flatMap(r=>r.resources);
    modal('Remove Bookinfo from '+cluster+'?',`<div class="notice bad"><div><strong>Only the verified Bookinfo demo</strong><p>${esc(preview.note)}</p></div></div><p class="mt"><strong>Cluster:</strong> ${esc(cluster)} · <strong>Context:</strong> ${esc(context)} · <strong>Namespace:</strong> ${esc(namespace)}</p>${installations.some(r=>r.recovered)?'<p>Verified from live cluster ownership. This demo can be removed from another laptop without the original installer history.</p>':''}${resources.length?`<div class="table-scroll bd-remove-list"><table><thead><tr><th>Kind</th><th>Bookinfo resource to remove</th></tr></thead><tbody>${resources.map(o=>`<tr><td>${esc(o.kind)}</td><td class="mono">${esc((o.namespace?o.namespace+'/':'')+o.name)}</td></tr>`).join('')}</tbody></table></div><p>Ownership and UIDs are checked again before removal. Removal is refused if a demo waypoint also serves an unrelated Service or ServiceEntry.</p><div class="row between mt"><button id="bd-remove-cancel">Cancel</button><button id="bd-remove-confirm" class="am-danger-apply">Remove ${resources.length} Bookinfo resources</button></div>`:'<p>No verified Flightbay-owned Bookinfo demo was found in this context. No namespace or resources will be deleted.</p>'}`);
    if($('#bd-remove-cancel'))$('#bd-remove-cancel').onclick=()=>{closeModal();render();};
    if($('#bd-remove-confirm')){let removing=false;$('#bd-remove-confirm').onclick=async()=>{
      if(removing||BD.removal)return;removing=true;BD.removal=action;action.phase='remove';
      loading('Removing Bookinfo from '+cluster,'Removing '+resources.length+' recorded Bookinfo resources…');
      const removalStarted=Date.now(),removalTimer=setInterval(()=>{const el=$('#bd-remove-elapsed');if(el)el.textContent=`${Math.floor((Date.now()-removalStarted)/1000)}s elapsed · Waiting for Kubernetes to confirm deletion.`;},1000);
      try{
        const result=await apiPost('api/ambient/bookinfo/remove',{operation:'remove',cluster,namespace,context,ids:installations.map(r=>r.id),confirm:true});
        BD.entries=BD.entries.filter(e=>e.cluster!==cluster||e.request.namespace!==namespace);
        BD.notice=cluster+' · '+namespace+': '+result.message;
        modal('Bookinfo removal complete',`<div class="notice" role="status"><strong>✓ Removal confirmed</strong><p>${esc(BD.notice)}</p></div>`);
        try{await bdRefresh();}catch(e){BD.error='Removal completed, but refreshing the capture failed: '+e.message;}
      }catch(e){
        BD.error=cluster+' · '+namespace+': '+e.message;
        modal('Bookinfo removal needs attention',`<div class="notice bad" role="alert"><strong>Removal could not be confirmed</strong><p>${esc(BD.error)}</p><p>Some resources may already have been removed. Preview removal again to inspect the remaining recorded resources before retrying.</p></div>`);
      }finally{clearInterval(removalTimer);BD.removal=null;await render();}
    };$('#bd-remove-cancel').focus({preventScroll:true});}
  }catch(e){BD.error=e.message;modal('Bookinfo removal preview failed',`<div class="notice bad" role="alert"><strong>Nothing was deleted</strong><p>${esc(e.message)}</p></div>`);await render();}
  finally{clearInterval(timer);if(BD.removal===action)BD.removal=null;}
}

// Bookinfo is an ambient demo: default to the Istio waypoint so Istio
// AuthorizationPolicy, JWT and routing behave as they do in a customer mesh.
function bdDefaultClass(row){return ((row.classes||[]).find(c=>c.product==='istio')||(row.classes||[])[0])?.name;}

// Apply validated plans one cluster at a time; stop at the first failure.
async function bdApplyEntries(entries){
  let failed=false;for(const e of entries){e.queued=true;e.message='';}
  for(const e of entries){
    e.queued=false;e.applying=true;e.error='';e.progress=null;e.pollStarted=null;e.pollStopped=false;e.message='Applying the '+(e.request.demo.stage==='namespace'?'namespace':'workloads')+' to '+e.context+'…';await render();
    try{const result=await apiPost('api/ambient/test',{operation:'apply',context:e.context,validation:e.validation.token});e.record=result.test;e.validation=null;e.message='';}
    catch(err){e.error=err.message;e.validation=null;if(err.body?.test)e.record=err.body.test;failed=true;break;}
    finally{e.applying=false;}
  }
  for(const e of entries)if(e.queued){e.queued=false;e.validation=null;e.message='Not attempted because an earlier cluster failed.';}
  if(failed)throw new Error('Installation stopped after a cluster failed. Earlier successful changes remain.');
}
// Keep "Per-resource progress" the way the reader left it across repaints.
function bdWireResources(root){(root||document).querySelectorAll('[data-bd-resources]').forEach(d=>d.addEventListener('toggle',()=>{BD.resourcesClosed=BD.resourcesClosed||{};BD.resourcesClosed[d.dataset.bdResources]=!d.open;}));}

async function bdDeploy(){
    const entries=BD.entries.filter(e=>!e.record);S.ambient.contexts=BD.contexts;
    if(BD.busy||!entries.length||entries.some(e=>!e.context))return;
    await bdRun(()=>amValidatePlans(entries));
    if(BD.error||entries.some(e=>!BD.entries.includes(e)||!amValidated(e))||!await amConfirmApply(entries.map(e=>e.preview?{...e,plan:{...e.plan,yaml:e.plan.yaml+'\n---\n# Then, once the namespace exists, dry-run and applied:\n'+e.preview.yaml}}:e)))return;
    // One confirmation covers both stages. When the confirmed plans create
    // the namespace, the workload plans are prepared, dry-run and applied
    // straight after, because Kubernetes cannot dry-run into a namespace
    // that does not exist yet.
    const creatingNamespace=entries.some(e=>e.request.demo.stage==='namespace');
    await bdRun(async()=>{
      await bdApplyEntries(entries);
      await bdRefresh();await bdAdvance();
      const next=BD.entries.filter(e=>!e.record&&!e.error&&e.request.demo.stage==='workloads'&&entries.some(x=>x.cluster===e.cluster));
      if(creatingNamespace&&next.length){
        for(const e of next)e.message='Dry-run of the workloads…';await render();
        await amValidatePlans(next);
        if(next.some(e=>!amValidated(e)))throw new Error('The workload dry-run failed. Nothing beyond the namespace was applied; see the cluster below.');
        await bdApplyEntries(next);
      }
    });
  }
