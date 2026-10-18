const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const path=require('node:path');
const source=fs.readFileSync(path.join(__dirname,'../web/bookinfo-demo.js'),'utf8');

test('namespace Apply automatically prepares separate workload previews without applying them',async()=>{
  const elements=new Map(),calls=[];let confirmations=0;
  const context=vm.createContext({S:{view:'ambient',ambient:{tab:'demo'}},RP:{},$:id=>{if(!elements.has(id))elements.set(id,{});return elements.get(id);},document:{querySelectorAll:()=>[]},render:async()=>{},api:async()=>{},apiPost:async(_,q)=>{calls.push(q);if(q.operation==='validate')return {token:q.context,context:q.context,plan:{id:q.planId},validated:new Date().toISOString()};if(q.operation==='apply')return {test:{context:q.context},message:'Namespace created'};return {id:'workloads-'+q.cluster,request:q,objects:[],notes:[]};},amConfirmApply:async()=>{confirmations++;return true;},setTimeout,clearTimeout});
  vm.runInContext(require('./context-helpers.cjs'),context);
  vm.runInContext(source,context);
  vm.runInContext(`bdSchedulePoll=()=>{};bdRefresh=async()=>{BD.catalog={clusters:['mesh1','mesh2'].map(cluster=>({cluster,namespaceState:'prepared'}))};};BD.entries=['mesh1','mesh2'].map(cluster=>({cluster,context:cluster,request:{cluster,namespace:'bookinfo',demo:{stage:'namespace'}},plan:{id:cluster,objects:[],notes:[]},validation:null}));bookinfoAfter();`,context);
  await elements.get('#bd-apply-all').onclick();
  assert.equal(confirmations,1);assert.equal(calls.filter(q=>q.operation==='validate').length,2,'primary action must perform the missing dry-runs automatically');assert.equal(calls.filter(q=>q.operation==='apply').length,2);assert.equal(calls.filter(q=>q.demo?.stage==='workloads').length,2);
  assert.equal(vm.runInContext('BD.entries.every(e=>e.namespaceRecord&&!e.record&&!e.validation&&e.request.demo.stage==="workloads")',context),true);
  assert.match(vm.runInContext('BD.entries[0].message',context),/confirm Stage 2/);
});

test('reload recovers workload records and automatic checks distinguish readiness from traffic',async()=>{
  const ctx=vm.createContext({S:{view:'ambient',ambient:{tab:'demo'}},$:()=>null,esc:String,setTimeout,clearTimeout,api:async(url)=>url.endsWith('installations')?[{context:'mesh1',applyState:'applied',validated:'2026-10-18',created:[{}],plan:{id:'plan',request:{cluster:'mesh1',namespace:'bookinfo',demo:{stage:'workloads'}},objects:[]}}]:url.endsWith('contexts')?[{name:'mesh1'}]:{state:'ready',message:'Workloads ready. Request connectivity has not been tested.',checkedAt:new Date().toISOString(),created:1,expected:1,ready:1,resources:[]}});
  vm.runInContext(source,ctx);await ctx.bdRecover();await ctx.bdPoll();
  assert.equal(vm.runInContext('BD.entries[0].progress.state',ctx),'ready');assert.equal(vm.runInContext('bdEntryState(BD.entries[0])',ctx),'Bookinfo ready');assert.match(vm.runInContext('bdProgress(BD.entries[0])',ctx),/connectivity has not been tested/);
  assert.equal(vm.runInContext('BD.entries[0].validation',ctx),null,'recovery must not grant apply validation');
});
