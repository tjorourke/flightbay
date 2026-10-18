const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const path=require('node:path');
const load=(ctx,name)=>vm.runInContext(fs.readFileSync(path.join(__dirname,'../web',name),'utf8'),ctx);
const connections=[{name:'original/mesh1',liveCluster:'mesh1',server:'https://mesh1'},{name:'original/mesh2',liveCluster:'mesh2',server:'https://mesh2'},{name:'mesh1',server:'https://unrelated'}];

test('live connection lookup uses recorded bindings, not a context name or current selection',()=>{
  const ctx=vm.createContext({});vm.runInContext(require('./context-helpers.cjs'),ctx);
  assert.equal(ctx.amLiveContext('mesh1',connections).name,'original/mesh1');
  assert.equal(ctx.amLiveContext('mesh1',[{name:'mesh1',server:'https://unrelated'}]),null);
  assert.equal(ctx.amLiveContext(undefined,[{name:'only-context'}]),null);
  assert.equal(ctx.amLiveContext('mesh1',[connections[0],{...connections[0],name:'ambiguous'}]),null);
});

test('multicluster publication reuses each row connection without selecting either again',async()=>{
  const ctx=vm.createContext({S:{cluster:'mesh1'},after:{},$:()=>null,document:{querySelector:()=>null},esc:String,amTaskLinks:()=>'',apiPost:async(_,request)=>({id:request.cluster,request,objects:[],changes:[{}]})});
  load(ctx,'ambient.js');load(ctx,'global-publication.js');const A=ctx.S.ambient;
  A.inventory={services:[{cluster:'mesh1',namespace:'bookinfo',name:'productpage'}]};A.key=JSON.stringify(['mesh1','bookinfo','productpage']);A.contexts=connections;A.settings.globalClusters=['mesh1','mesh2'];
  await ctx.amGenerateGlobalBatch();assert.deepEqual(Array.from(A.globalBatch,r=>r.context),['original/mesh1','original/mesh2']);
  const html=ctx.amGlobalBatchPanel();assert.doesNotMatch(html,/data-amglobalcontext/);assert.match(html,/Live connection · original\/mesh1/);assert.match(html,/Live connection · original\/mesh2/);
  assert.match(html,/<button id="am-global-validate-all" >Dry-run only/);assert.match(html,/id="am-global-apply-all" class="primary" >Review and apply/);
});

for(const live of [false,true])test('shared configuration review '+(live?'reuses its target live cluster':'keeps explicit context choice for an offline capture'),async()=>{
  const ctx=vm.createContext({S:{view:'ambient',cluster:'',ws:live?{live:{}}:{}},after:{},$:()=>null,esc:String,attr:JSON.stringify,api:async url=>url.endsWith('contexts')?(live?connections:connections.map(({liveCluster,...c})=>c)):url.endsWith('tests')?[]:{}});
  load(ctx,'ambient.js');const A=ctx.S.ambient;A.tab='wizard';A.phase=3;A.inventory={services:[{cluster:'mesh2',namespace:'bookinfo',name:'reviews',kind:'Service'}]};A.key=JSON.stringify(['mesh2','bookinfo','reviews']);A.plan={id:'plan',request:{cluster:'mesh2',namespace:'bookinfo',service:'reviews',tasks:['rate-limit']},objects:[{kind:'Example',metadata:{name:'reviewed'}}],notes:[]};
  const html=await ctx.ambientPage();assert.equal(A.context,live?'original/mesh2':'');
  assert.equal(html.includes('id="am-context"'),!live);assert.equal(html.includes('Live connection · original/mesh2'),live);assert.equal(A.validation,null);
});

test('Bookinfo preview reuses the live target even when the kubeconfig name differs',async()=>{
  const elements=new Map();const ctx=vm.createContext({S:{view:'ambient',ambient:{tab:'demo'}},RP:{},after:{},esc:String,$:id=>{if(!elements.has(id))elements.set(id,{});return elements.get(id);},document:{querySelectorAll:()=>[]},render:async()=>{},api:async url=>url.endsWith('contexts')?connections:{clusters:[{cluster:'mesh2',namespaceState:'prepared',classes:[{name:'istio-waypoint'}]}]},apiPost:async(_,request)=>({id:'plan',request,objects:[],notes:[]}),setTimeout,clearTimeout});
  vm.runInContext(require('./context-helpers.cjs'),ctx);load(ctx,'bookinfo-demo.js');vm.runInContext("BD.selected.mesh2=true;bookinfoAfter();",ctx);
  await elements.get('#bd-review').onclick();assert.equal(vm.runInContext('BD.entries[0].context',ctx),'original/mesh2');assert.equal(vm.runInContext('BD.entries[0].validation',ctx),null);
});

test('Service HTTP tests reuse the destination live context without starting a probe',async()=>{
  const ctx=vm.createContext({S:{ambient:{inventory:{services:[]}}},amKey:JSON.stringify,toast:message=>assert.fail(message),api:async url=>url.endsWith('contexts')?connections:url.endsWith('profiles')?[]:{dns:{localName:'reviews.bookinfo.svc.cluster.local'}}});
  vm.runInContext(require('./context-helpers.cjs'),ctx);load(ctx,'http-test.js');vm.runInContext('drawHTTPTest=()=>{}',ctx);
  await ctx.amOpenHTTPTest({cluster:'mesh2',namespace:'bookinfo',name:'reviews'});
  assert.equal(vm.runInContext('HTTP_TEST.context',ctx),'original/mesh2');assert.equal(vm.runInContext('HTTP_TEST.results.length',ctx),0);
});
