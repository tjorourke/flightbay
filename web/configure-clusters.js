'use strict';

function amConfigurationClusterChoices(target){
  const A=S.ambient,clusters=(A.inventory?.topology?.clusters||[]).map(c=>c.context),selected=A.settings.applyClusters||[target.cluster];
  if(clusters.length<2)return '';
  const eligible=cluster=>target.kind==='Namespace'?(A.inventory.namespaces||[]).some(n=>n.cluster===cluster&&n.name===target.name):(A.inventory.objects||A.inventory.services||[]).some(o=>o.cluster===cluster&&(o.kind||'Service')===(target.kind||'Service')&&o.namespace===target.namespace&&o.name===target.name);
  return `<fieldset class="am-config-clusters"><legend>Apply this configuration to clusters</legend><p>Each cluster gets its own generated YAML, validation and apply result. Existing-resource edits update the matching resource when present; other clusters get a new configuration. Inspect every preview.</p><div class="row wrap">${clusters.map(cluster=>`<label><input type="checkbox" data-amapplycluster="${esc(cluster)}" ${selected.includes(cluster)?'checked':''} ${eligible(cluster)?'':'disabled'}> ${esc(cluster)}${eligible(cluster)?'':' · target not captured'}</label>`).join('')}</div></fieldset>`;
}
function amConfigurationClusters(request){return S.ambient.settings.applyClusters||[request.cluster];}
async function amConfigurationRequestForCluster(request,cluster){
  const q=JSON.parse(JSON.stringify(request));q.cluster=cluster;
  if(cluster===request.cluster)return q;
  if(q.edit){
    // Never reuse the source UID, fingerprint, or rule authority on a peer.
    const view=await api('api/ambient/investigate',{cluster,kind:'Service',namespace:q.namespace,name:q.service});
    const entry=(view.editable||[]).find(e=>e.ref.kind===q.edit.ref.kind&&e.ref.name===q.edit.ref.name&&e.ref.namespace===q.edit.ref.namespace&&e.rule===q.edit.rule);
    if(entry){if(!entry.supported||!entry.request?.edit)throw new Error(cluster+': matching resource cannot be edited: '+entry.reason);q.edit=entry.request.edit;}
    else delete q.edit;
  }
  if(q.parent)q.parent.cluster=cluster;
  return q;
}
async function amGenerateConfigurationBatch(request){
  const A=S.ambient,selected=amConfigurationClusters(request);if(!selected.length)throw new Error('Select at least one cluster.');
  const rows=[];
  for(const cluster of selected){
    const row={cluster,context:amLiveContext(cluster,A.contexts)?.name||'',plan:null,validation:null,state:'Preview',error:'',applied:false};
    try{const q=await amConfigurationRequestForCluster(request,cluster);row.plan=await apiPost('api/ambient/plan',q);if(!row.plan.objects.length&&!row.plan.changes?.length)row.state='Already configured';}
    catch(e){row.error=e.message;row.state='Preview failed';}
    rows.push(row);
  }
  A.globalBatch=rows;A.batchKind='configuration';A.plan=null;A.validation=null;A.phase=3;
}
function amWireConfigurationClusters(){
  const setup=$('#am-setup-parameters')||$('#am-repair-parameters');
  if(setup&&!setup.querySelector('[data-amapplycluster]')&&amTarget())setup.insertAdjacentHTML('afterbegin',amConfigurationClusterChoices(amTarget()));
  document.querySelectorAll('[data-amapplycluster]').forEach(box=>box.addEventListener('change',()=>{amCapture();S.ambient.settings.applyClusters=[...document.querySelectorAll('[data-amapplycluster]:checked')].map(b=>b.dataset.amapplycluster);amInvalidate();amLiveRender();}));
}
