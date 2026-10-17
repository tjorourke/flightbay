'use strict';

function amGlobalTargets(svc){
  return (S.ambient.inventory?.topology?.clusters||[]).map(cluster=>{
    const service=cluster.services.find(s=>s.namespace===svc.namespace&&s.name===svc.name);
    return {cluster:cluster.context,service,global:!!service&&['global','global-only'].includes(service.scope)};
  });
}
function amGlobalClusterChoices(svc){
  const A=S.ambient,chosen=A.settings.globalClusters||[svc.cluster];
  return `<section class="am-global-targets"><h3>Participating clusters</h3><p>Publish each existing instance that should contribute endpoints. The Service and workloads are not copied into another cluster.</p>${amGlobalTargets(svc).map(row=>`<label class="am-global-cluster"><input type="checkbox" data-amglobalcluster="${esc(row.cluster)}" ${chosen.includes(row.cluster)?'checked':''} ${row.service?'':'disabled'}><span><strong>${esc(row.cluster)}</strong><small>${row.global?'Already global · no label change required':row.service?'Service captured · review publication':'Service not captured. Deploy '+esc(svc.namespace+'/'+svc.name)+' and its workloads here, or refresh a capture that includes it.'}</small></span><span class="badge ${row.global?'green':row.service?'blue':'amber'}">${row.global?'Already global':row.service?'Ready to review':'Missing Service evidence'}</span></label>`).join('')}<p class="muted">Each cluster gets its own preview, explicit kubeconfig context, validation and apply result. There is no cross-cluster transaction.</p></section>`;
}
async function amGenerateGlobalBatch(){
  amCapture();const A=S.ambient,svc=amService(),selected=A.settings.globalClusters||[svc.cluster];
  if(!selected.length)throw new Error('Select at least one participating cluster.');
  const rows=[];
  for(const cluster of selected){
    const row={cluster,context:'',plan:null,validation:null,state:'Preview',error:'',applied:false};
    try{row.plan=await apiPost('api/ambient/plan',{cluster,namespace:svc.namespace,service:svc.name,tasks:['global-service']});if(!row.plan.changes?.length&&!row.plan.objects.length)row.state='Already global';}catch(e){row.error=e.message;row.state='Preview failed';}
    rows.push(row);
  }
  A.globalBatch=rows;A.plan=null;A.validation=null;A.phase=3;
}
function amGlobalBatchPanel(){
  const A=S.ambient,rows=A.globalBatch;
  const pending=rows.filter(r=>r.plan&&!r.applied&&(r.plan.changes?.length||r.plan.objects.length));
  const failedPreview=rows.some(r=>!r.plan);
  return `<section class="panel am-review am-global-review"><div class="panel-head"><div><h2>Review global publication · ${esc(amService().namespace+'/'+amService().name)}</h2><p>Separate plans and operations per cluster. Already-global instances remain unchanged.</p></div><button id="am-back-options">← Edit clusters</button></div><div class="panel-body"><p>Choose the matching real context for each required change, then dry-run and apply below. Failures in one row do not roll back another cluster; successful changes appear in Active tests with undo available.</p><div class="am-wizard-footer"><button id="am-global-validate-all" ${failedPreview||!pending.length||pending.some(r=>!r.context)?'disabled':''}>Dry-run selected clusters</button><button id="am-global-apply-all" class="primary" ${failedPreview||!pending.length||pending.some(r=>!r.validation)?'disabled':''}>Apply validated cluster plans</button></div>${rows.map((row,i)=>`<article class="am-global-plan"><div class="row between wrap"><h3>${esc(row.cluster)}</h3><span class="badge ${row.applied||row.state==='Already global'?'green':'blue'}">${esc(row.state)}</span></div>${row.error?`<p class="red" role="alert">${esc(row.error)}</p>`:''}${row.plan?`<p>${row.plan.changes?.length||0} existing resources to update · ${row.plan.objects.length} to create</p><details><summary>Reviewed YAML for ${esc(row.cluster)}</summary><pre class="code">${esc(row.plan.yaml)}</pre></details><div class="row wrap"><button data-amglobaldownload="${i}">Download YAML</button>${row.state!=='Already global'?`<label>Kubeconfig context<select data-amglobalcontext="${i}" ${row.applied?'disabled':''}><option value="">Choose matching real context…</option>${amOpts((A.contexts||[]).map(c=>({value:c.name,label:c.name+' · '+c.server})),row.context)}</select></label><button data-amglobalvalidate="${i}" ${!row.context||row.applied?'disabled':''}>Dry-run this cluster</button><button class="primary" data-amglobalapply="${i}" ${!row.validation||row.applied?'disabled':''}>Apply to ${esc(row.cluster)}</button>`:''}</div>${row.validation&&!row.applied?'<p class="green">Validated for this exact context and plan. Apply within ten minutes.</p>':''}`:''}</article>`).join('')}${amTaskLinks(['global-service'])}</div></section>`;
}
async function amGlobalOperation(index,operation){
  const A=S.ambient,row=A.globalBatch[index];row.error='';
  try{
    if(operation==='validate'){row.validation=null;const result=await apiPost('api/ambient/test',{operation:'validate',context:row.context,request:row.plan.request,planId:row.plan.id});row.validation=result;row.state='Dry-run passed';}
    else{await apiPost('api/ambient/test',{operation:'apply',context:row.context,validation:row.validation.token});row.applied=true;row.validation=null;row.state='Applied';}
  }catch(e){row.error=e.message;row.validation=null;row.state=operation==='validate'?'Dry-run failed':'Apply failed';}
  A.tests=await api('api/ambient/tests').catch(()=>A.tests);
}
function amWireGlobalPublication(){
  const A=S.ambient;
  document.querySelectorAll('.am-global-plan').forEach((el,i)=>{const row=A.globalBatch[i],badge=el.querySelector('.badge'),ok=row.applied||!!row.validation||row.state==='Already global';badge.className='badge '+(row.error?'red':ok?'green':'blue');badge.textContent=(row.error?'✕ ':ok?'✓ ':'')+row.state;});
  document.querySelectorAll('[data-amglobalcluster]').forEach(box=>box.onchange=()=>{amCapture();A.globalBatch=null;amInvalidate();});
  document.querySelectorAll('[data-amglobalcontext]').forEach(select=>select.onchange=()=>{const row=A.globalBatch[Number(select.dataset.amglobalcontext)];row.context=select.value;row.validation=null;row.error='';row.state='Preview';render();});
  document.querySelectorAll('[data-amglobaldownload]').forEach(button=>button.onclick=()=>{const row=A.globalBatch[Number(button.dataset.amglobaldownload)];amDownload(row.plan.yaml,'flightbay-global-'+row.cluster.replace(/[^a-zA-Z0-9._-]/g,'_')+'.yaml');});
  for(const [attribute,operation]of [['amglobalvalidate','validate'],['amglobalapply','apply']])document.querySelectorAll('[data-'+attribute+']').forEach(button=>button.onclick=async()=>{const index=Number(button.dataset[attribute]),row=A.globalBatch[index];if(operation==='apply'&&!await amConfirmApply([row]))return;return amAction(()=>amGlobalOperation(index,operation));});
  for(const [id,operation]of [['am-global-validate-all','validate'],['am-global-apply-all','apply']])if($('#'+id))$('#'+id).onclick=async()=>{const pending=A.globalBatch.filter(row=>row.plan&&!row.applied&&(row.plan.changes?.length||row.plan.objects.length));if(operation==='apply'&&!await amConfirmApply(pending))return;return amAction(async()=>{for(let i=0;i<A.globalBatch.length;i++){const row=A.globalBatch[i];if(!row.plan||row.applied||(!row.plan.changes?.length&&!row.plan.objects.length))continue;await amGlobalOperation(i,operation);if(operation==='apply'&&row.error)break;}});};
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
