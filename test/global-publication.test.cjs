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
