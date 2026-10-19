const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const path=require('node:path');

function setup(){
  const boxes=['mesh1','mesh2','missing'].map(cluster=>({dataset:{amglobalcluster:cluster},disabled:false}));
  const context=vm.createContext({S:{},after:{},$:()=>null,document:{querySelector:selector=>selector==='[data-amglobalcluster]'?boxes[0]:null,querySelectorAll:selector=>selector==='[data-amglobalcluster]:checked'||selector.startsWith('#main button')?boxes:[]},render:async()=>{},toast:()=>{}});
  for(const file of ['ambient.js','global-publication.js'])vm.runInContext(fs.readFileSync(path.join(__dirname,'../web',file),'utf8'),context);
  const A=context.S.ambient;
  A.inventory={services:['mesh1','mesh2'].map(cluster=>({cluster,namespace:'app',name:'web'}))};A.key=JSON.stringify(['mesh1','app','web']);A.settings.globalClusters=['mesh1','mesh2'];
  return {context,A,boxes};
}

test('busy-state disabling retains selected participating clusters and excludes a missing Service',async()=>{
  const {context,A,boxes}=setup(),requests=[];
  context.apiPost=async(route,request)=>{requests.push(request);return {id:request.cluster,request,changes:[{}],objects:[]};};
  await context.amAction(()=>context.amGenerateGlobalBatch());
  assert.ok(boxes.every(box=>box.disabled),'exercise the real busy-state disabling before reading selections');
  assert.deepEqual(requests.map(request=>request.cluster),['mesh1','mesh2']);
  assert.ok(requests.every(request=>request.namespace==='app'&&request.service==='web'));
  assert.equal(A.globalBatch.length,2);assert.equal(A.phase,3);assert.equal(A.error,'');
});

test('validation is independent per cluster and undo discards stale applied publication rows',async()=>{
  const {context,A}=setup();
  A.globalBatch=['mesh1','mesh2'].map(cluster=>({cluster,context:cluster,plan:{id:cluster,request:{cluster,namespace:'app',service:'web'}},validation:null}));
  context.apiPost=async(route,request)=>{if(request.operation==='validate'){if(request.context==='mesh2')throw new Error('Service UID differs');return {token:'mesh1-only'};}return {message:'Undone'};};
  context.api=async()=>[];
  await context.amGlobalOperation(0,'validate');await context.amGlobalOperation(1,'validate');
  assert.equal(A.globalBatch[0].validation.token,'mesh1-only');assert.equal(A.globalBatch[0].state,'Dry-run passed');
  assert.equal(A.globalBatch[1].validation,null);assert.equal(A.globalBatch[1].state,'Dry-run failed');assert.match(A.globalBatch[1].error,/UID differs/);
  await context.amTestOperation('cleanup',{context:'mesh1'});
  assert.equal(A.globalBatch,null);assert.equal(A.inventory,null);assert.equal(A.phase,2);assert.equal(A.tests.length,0);
});

test('changing tasks invalidates the global batch so it cannot hide a new single-cluster plan',()=>{
  const {context,A}=setup();A.globalBatch=[{cluster:'mesh1'}];A.plan={id:'old'};A.validation={token:'old'};
  context.amInvalidate();
  assert.equal(A.globalBatch,null);assert.equal(A.plan,null);assert.equal(A.validation,null);
});

test('global validation immediately reports working and rejects Apply without dry-run',async()=>{
  const {context,A}=setup();let finish;const waiting=new Promise(resolve=>finish=resolve),badge={};context.esc=String;context.document.querySelectorAll=selector=>selector==='.am-global-plan'?[{querySelector:()=>badge}]:[];
  A.globalBatch=[{cluster:'mesh1',context:'mesh1',plan:{id:'plan',request:{}},validation:null}];let calls=0;context.apiPost=async()=>{calls++;return waiting;};context.api=async()=>[];
  await context.amGlobalOperation(0,'apply');assert.equal(calls,0);assert.match(A.globalBatch[0].error,/Dry-run/);
  const pending=context.amGlobalOperation(0,'validate');assert.match(A.globalBatch[0].state,/Validating/);assert.match(badge.innerHTML,/bd-spinner/);
  finish({token:'validated'});await pending;assert.equal(A.globalBatch[0].state,'Dry-run passed');assert.equal(badge.textContent,'Dry-run passed');assert.equal(A.globalBatch[0].validation.token,'validated');
});

test('an already-global Service only plans the clusters whose choice changed, and unpublish removes the label',async()=>{
  const {context,A}=setup(),requests=[];
  A.inventory.topology={clusters:[{context:'mesh1',services:[{namespace:'app',name:'web',scope:'global'}]},{context:'mesh2',services:[{namespace:'app',name:'web',scope:'global'}]},{context:'mesh3',services:[{namespace:'app',name:'web',scope:''}]}]};
  A.inventory.services.push({cluster:'mesh3',namespace:'app',name:'web'});
  const svc={cluster:'mesh1',namespace:'app',name:'web'};
  assert.equal(context.amGlobalPublished(svc),true);
  A.settings.globalEdits={mesh1:'keep',mesh2:'unpublish',mesh3:'publish'};
  assert.deepEqual(JSON.parse(JSON.stringify(context.amGlobalPendingEdits(svc))),[{cluster:'mesh2',action:'unpublish'},{cluster:'mesh3',action:'publish'}]);
  context.apiPost=async(route,request)=>{requests.push(request);return {id:request.cluster,request,changes:[{}],objects:[]};};
  await context.amAction(()=>context.amGenerateGlobalBatch());
  assert.equal(A.error,'');
  assert.deepEqual(requests.map(r=>[r.cluster,!!r.unpublish]),[['mesh2',true],['mesh3',false]]);
  // Nothing changed: no plan is generated and the reader is told why.
  A.settings.globalEdits={mesh1:'keep',mesh2:'keep'};requests.length=0;
  await context.amAction(()=>context.amGenerateGlobalBatch());
  assert.equal(requests.length,0);assert.match(A.error,/Nothing to change/);
});

test('stop all still generates cleanup plans when weighted routing is selected',async()=>{
  const {context,A}=setup(),requests=[];
  A.inventory.topology={clusters:['mesh1','mesh2'].map(cluster=>({context:cluster,services:[{namespace:'app',name:'web',scope:'global',globalCleanup:true}]}))};
  A.settings.globalDistribution='weighted';
  A.settings.globalEdits={mesh1:'unpublish',mesh2:'unpublish'};
  context.apiPost=async(route,request)=>{requests.push(request);return {id:request.cluster,request,changes:[{delete:true}],objects:[]};};
  await context.amGenerateGlobalBatch();
  assert.deepEqual(requests.map(r=>[r.cluster,r.unpublish]),[['mesh1',true],['mesh2',true]]);
  assert.ok(requests.every(r=>!r.globalWeights));
  assert.ok(A.globalBatch.every(r=>!context.amGlobalNoop(r)));
});

test('an arbitrary already-local customer Service can remove leftover split resources',async()=>{
  const {context,A}=setup(),requests=[];
  const svc={cluster:'mesh1',namespace:'customer-payments',name:'foobar'};
  A.key=JSON.stringify([svc.cluster,svc.namespace,svc.name]);
  A.inventory.services=[svc];
  A.inventory.topology={clusters:[{context:'mesh1',services:[{...svc,scope:'',globalCleanup:true}]},{context:'mesh2',services:[{...svc,scope:''}]}]};
  assert.equal(context.amGlobalPublished(svc),true);
  A.settings.globalEdits={mesh1:'unpublish'};
  context.apiPost=async(route,request)=>{requests.push(request);return {id:'cleanup',request,changes:[{delete:true,ref:{kind:'HTTPRoute',name:'foobar-cluster-split'}}],objects:[]};};
  await context.amGenerateGlobalBatch();
  assert.equal(requests.length,1);
  assert.equal(requests[0].namespace,'customer-payments');
  assert.equal(requests[0].service,'foobar');
  assert.equal(requests[0].unpublish,true);
  assert.equal(context.amGlobalNoop(A.globalBatch[0]),false);
});

test('post-stop refresh waits for split resources to disappear, not just the global label',async()=>{
  const {context,A}=setup();let calls=0;
  context.setTimeout=fn=>fn();
  context.api=async()=>{calls++;return {topology:{clusters:[{context:'mesh1',services:[{namespace:'app',name:'web',scope:'',globalCleanup:calls<3}]}]}};};
  await context.amGlobalWaitForCapture({namespace:'app',name:'web'},[{cluster:'mesh1',action:'unpublish'}]);
  assert.equal(calls,3);
  assert.equal(A.inventory.topology.clusters[0].services[0].globalCleanup,false);
});

test('global review distinguishes resource deletions from updates',()=>{
  const {context,A}=setup();
  context.esc=String;context.amContextControl=()=>'';context.amTaskLinks=()=>'';
  A.globalBatch=[{cluster:'mesh1',context:'mesh1',action:'unpublish',state:'Preview',plan:{changes:[{}, {delete:true}, {delete:true}],objects:[],yaml:'# DELETE HTTPRoute app/web-cluster-split'}}];
  const html=context.amGlobalBatchPanel();
  assert.match(html,/1 existing resources to update · 2 to delete · 0 to create/);
  assert.match(html,/# DELETE HTTPRoute app\/web-cluster-split/);
  assert.match(html,/data-amglobalapply/);
});
