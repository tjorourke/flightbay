const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const path=require('node:path');
const load=(ctx,name)=>vm.runInContext(fs.readFileSync(path.join(__dirname,'../web',name),'utf8'),ctx);

for(const outcome of ['cancel','apply','failure'])test('global primary action automatically validates both clusters before '+outcome,async()=>{
  const button={},calls=[];let confirmations=0;
  const ctx=vm.createContext({S:{},after:{},esc:String,$:id=>id==='#am-global-apply-all'?button:null,document:{querySelectorAll:()=>[]},render:async()=>{},api:async()=>[],apiPost:async(_,q)=>{calls.push(q);if(q.operation==='validate'){if(outcome==='failure'&&q.context==='mesh2')throw new Error('mesh2 identity changed');return {token:q.context,context:q.context,plan:{id:q.planId},validated:new Date().toISOString()};}return {message:'Applied'};},amConfirmApply:async entries=>{confirmations++;assert.equal(entries.length,2);assert.equal(calls.filter(q=>q.operation==='apply').length,0);assert.ok(entries.every(e=>e.validation));return outcome==='apply';}});
  load(ctx,'ambient.js');load(ctx,'global-publication.js');ctx.amConfirmApply=async entries=>{confirmations++;assert.ok(entries.every(e=>e.validation));assert.equal(calls.filter(q=>q.operation==='apply').length,0);return outcome==='apply';};
  const A=ctx.S.ambient;A.globalBatch=['mesh1','mesh2'].map(cluster=>({cluster,context:cluster,plan:{id:cluster,request:{cluster},objects:[],changes:[{}]},validation:null}));ctx.amWireGlobalPublication();
  await button.onclick();assert.equal(calls.filter(q=>q.operation==='validate').length,2);
  assert.equal(confirmations,outcome==='failure'?0:1);assert.equal(calls.filter(q=>q.operation==='apply').length,outcome==='apply'?2:0);
  if(outcome==='failure')assert.match(A.error,/identity changed/);
});

test('expired validation is refreshed while a current exact plan/context validation is reused',async()=>{
  const calls=[],ctx=vm.createContext({apiPost:async(_,q)=>{calls.push(q);return {token:'new',context:q.context,plan:{id:q.planId},validated:new Date().toISOString()};}});vm.runInContext(require('./context-helpers.cjs'),ctx);
  const entry={context:'mesh1',plan:{id:'p',request:{cluster:'mesh1'}},validation:{token:'old',context:'mesh1',plan:{id:'p'},validated:new Date(Date.now()-11*60000).toISOString()}};
  await ctx.amValidatePlans([entry]);assert.equal(calls.length,1);assert.equal(entry.validation.token,'new');await ctx.amValidatePlans([entry]);assert.equal(calls.length,1);
  entry.context='mesh2';await ctx.amValidatePlans([entry]);assert.equal(calls.length,2);assert.equal(entry.validation.context,'mesh2');
});

test('Bookinfo primary action stops before confirmation or writes when its automatic dry-run fails',async()=>{
  const elements=new Map(),calls=[];let confirmations=0;
  const ctx=vm.createContext({S:{view:'ambient',ambient:{tab:'demo'}},RP:{},esc:String,$:id=>{if(!elements.has(id))elements.set(id,{});return elements.get(id);},document:{querySelectorAll:()=>[]},render:async()=>{},apiPost:async(_,q)=>{calls.push(q);throw new Error('dry-run admission rejected');},amConfirmApply:async()=>{confirmations++;return true;},setTimeout,clearTimeout});
  vm.runInContext(require('./context-helpers.cjs'),ctx);load(ctx,'bookinfo-demo.js');vm.runInContext("bdSchedulePoll=()=>{};BD.entries=[{context:'mesh1',plan:{id:'plan',request:{cluster:'mesh1'}}}];bookinfoAfter();",ctx);
  await elements.get('#bd-apply-all').onclick();assert.equal(confirmations,0);assert.deepEqual(calls.map(q=>q.operation),['validate']);assert.match(vm.runInContext('BD.error',ctx),/admission rejected/);assert.equal(vm.runInContext('BD.busy',ctx),false);
});
