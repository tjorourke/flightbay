const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const path=require('node:path');
const source=fs.readFileSync(path.join(__dirname,'../web/ambient.js'),'utf8');

test('explainer bridge retains Review and scope when entering from all clusters or another cluster',async()=>{
  for(const cluster of ['', 'mesh1', 'mesh2']){
    let sent,navigation;
    const select={value:cluster},plan={id:'reviewed',request:{explain:{from:'app/caller',to:'app/front',ports:['8080'],paths:['/private/*']}}};
    const ctx=vm.createContext({S:{cluster,ambient:{}},$:()=>select,amResetDraft:()=>{},apiPost:async(url,request)=>{sent={url,request};return plan;},nav:value=>navigation=value});
    vm.runInContext(source.slice(source.indexOf('async function amReviewExplanation(')),ctx);
    await ctx.amReviewExplanation({cluster:'mesh1',from:{label:'app/caller'},to:{label:'app/front',ref:{namespace:'app',name:'front'}},fixScope:{ports:['8080'],paths:['/private/*']}});
    assert.equal(sent.url,'api/ambient/plan');assert.deepEqual(Array.from(sent.request.explain.paths),['/private/*']);
    assert.equal(ctx.S.ambient.phase,3);assert.equal(ctx.S.ambient.plan,plan);assert.equal(ctx.S.ambient.clusterScope,ctx.S.cluster,'ambientPage must not discard the reviewed plan as a scope change');
    assert.equal(ctx.S.cluster,cluster===''?'':'mesh1');assert.equal(navigation,'ambient');
  }
});

test('editing explainer scope sends only choices for a server-regenerated repair',()=>{
  const svc={cluster:'mesh1',namespace:'app',name:'front',ports:[8080]};
  const ctx=vm.createContext({S:{ambient:{settings:{explain:{from:'app/caller',to:'app/front',ports:['8080'],paths:['*']},explainPorts:'8080',explainPaths:'/private/*,/health'}}},amCapture:()=>{},amTarget:()=>svc,amService:()=>svc});
  vm.runInContext(source.slice(source.indexOf('function amRead('),source.indexOf('async function amAction(')),ctx);
  const request=JSON.parse(JSON.stringify(ctx.amRead()));
  assert.deepEqual(request,{cluster:'mesh1',namespace:'app',service:'front',tasks:['explain'],explain:{from:'app/caller',to:'app/front',ports:['8080'],paths:['/private/*','/health']}});
  assert.equal(request.yaml,undefined);
});
