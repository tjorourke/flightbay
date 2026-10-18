const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const path=require('node:path');

function setup(){const elements={'#rp-result':{hidden:false},'#rp-change-hint':{textContent:''}};const ctx=vm.createContext({S:{cluster:''},$:s=>elements[s],esc:s=>String(s??'').replaceAll('<','&lt;').replaceAll('"','&quot;'),closeModal:()=>{},closeDrawer:()=>{},nav:()=>{},amState:()=>'',attr:()=>'',fmtDateTime:()=>'',console});vm.runInContext(fs.readFileSync(path.join(__dirname,'../web/request-path.js'),'utf8'),ctx);ctx.rpKey=vm.runInContext('rpKey',ctx);return {ctx,state:vm.runInContext('RP',ctx),elements};}
test('request-path choices preserve cluster identity and can be found by published hostname',()=>{const {ctx,state}=setup();const one={cluster:'mesh1',kind:'Service',namespace:'app',name:'pets',dns:{globalNames:['pets.app.mesh.internal']}},two={...one,cluster:'mesh2'};assert.notEqual(ctx.rpKey(one),ctx.rpKey(two));state.options={callers:[],destinations:[one,two]};const html=ctx.rpOptionsMarkup([one,two],'','pets.app.mesh.internal','Choose');assert.match(html,/mesh1/);assert.match(html,/mesh2/);assert.doesNotMatch(ctx.rpOptionsMarkup([one],'','missing-host','Choose'),/mesh1/);});
test('editing a request invalidates prior evidence and in-flight analysis',()=>{const {ctx,state,elements}=setup();state.result={path:{}};const revision=state.revision;ctx.rpInvalidate();assert.equal(state.result,null);assert.equal(state.revision,revision+1);assert.equal(elements['#rp-result'].hidden,true);assert.match(elements['#rp-change-hint'].textContent,/Trace the path/);});
test('Service selection fills the port and chooses mesh DNS for a remote cluster while preserving custom intent',()=>{const {ctx,state}=setup();const source={cluster:'mesh1',kind:'Deployment',namespace:'bookinfo',name:'productpage-v1'},dest={cluster:'mesh2',kind:'Service',namespace:'bookinfo',name:'reviews',ports:[9080],dns:{localName:'reviews.bookinfo.svc.cluster.local',globalNames:['reviews.bookinfo.mesh.internal']}};state.options={callers:[source],destinations:[dest]};state.source=ctx.rpKey(source);state.destination=ctx.rpKey(dest);ctx.rpApplyDefaults();assert.equal(state.port,'9080');assert.equal(state.host,'reviews.bookinfo.mesh.internal');state.host='';state.hostCustom=true;ctx.rpApplyDefaults();assert.equal(state.host,'','an empty custom field must not silently switch back to automatic DNS');});

test('dataplane steps omit DNS and route records without losing their evidence',()=>{
  const {ctx,state}=setup(),p={hops:[{id:'caller',title:'Caller Pods',cluster:'mesh1',state:'pass',detail:'Caller',refs:[{kind:'Pod',name:'caller'}]},{id:'eastwest',title:'East-west gateway',cluster:'mesh2',state:'pass',detail:'HBONE',refs:[{kind:'Gateway',name:'eastwest'}]},{id:'backend',title:'Destination Pods',cluster:'mesh2',state:'pass',detail:'Endpoints',refs:[{kind:'Pod',name:'backend'}]}],configuration:[{id:'address',title:'Destination address',state:'pass',detail:'DNS mapping',refs:[]},{id:'routing',title:'Route attachments',state:'pass',detail:'Related HTTPRoute',refs:[]}]};
  state.result={path:p};state.hop='caller';state.evidence='routing';
  const steps=ctx.rpStepsPanel(p),evidence=ctx.rpInspector();assert.match(steps,/Dataplane hops/);assert.match(steps,/rp-hop-eastwest/);assert.doesNotMatch(steps,/Destination address|Route attachments/);assert.match(evidence,/Destination address/);assert.match(evidence,/Route attachments/);assert.doesNotMatch(evidence,/rp-hop-number/);
});

test('east-west map roles and HBONE labels retain failure markers',()=>{
  const {ctx,state}=setup();ctx.innerWidth=1440;ctx.$=()=>({clientWidth:1120});ctx.fbGraphPanel=(title,graph,svg,extra,legend)=>svg+legend;
  vm.runInContext(fs.readFileSync(path.join(__dirname,'../web/request-path-map.js'),'utf8'),ctx);
  const hops=[{id:'source-ztunnel',title:'Source ztunnel',cluster:'mesh1',state:'pass',detail:'Ready',refs:[{kind:'Pod',name:'ztunnel'}]},{id:'eastwest',title:'East-west gateway',cluster:'mesh2',state:'pass',detail:'Transport',refs:[{kind:'Gateway',name:'eastwest'}]},{id:'destination-ztunnel',title:'Destination ztunnel',cluster:'mesh2',state:'pass',detail:'Ready',refs:[{kind:'Pod',name:'ztunnel'}]}];
  state.result={path:{request:{},hops}};let html=ctx.rpMapPanel();assert.match(html,/rp-map-role-eastwest/);assert.match(html,/rp-map-edge-eastwest/);assert.match(html,/>HBONE</);assert.match(html,/Amber = east-west/);
  hops[1].state='fail';html=ctx.rpMapPanel();assert.match(html,/rp-map-fail rp-map-role-eastwest/);assert.match(html,/url\(#rp-arrow-fail\)/);
});
