const {test}=require('node:test');const assert=require('node:assert/strict');const vm=require('node:vm');const fs=require('node:fs');const path=require('node:path');
const source=fs.readFileSync(path.join(__dirname,'../web/ambient.js'),'utf8');
test('every sample replaces invalid form and YAML drafts on both waypoint implementations',()=>{
 for(const product of ['istio','agentgateway']){
  let paints=0;const sample={value:'',addEventListener(type,fn){this.listener=fn;},change(){let stopped=false;this.listener({stopImmediatePropagation(){stopped=true;}});if(!stopped)this.onchange?.();}};
  const ctx=vm.createContext({S:{ambient:{settings:{},result:{environment:{selected:{product}}}}},$:id=>id==='#am-access-sample'?sample:id==='#am-access'?{}:null,document:{querySelectorAll:()=>[]},amService:()=>({namespace:'bookinfo'}),amInvalidate:()=>{},render:()=>paints++});
  vm.runInContext(source.slice(source.indexOf('const AM_SOURCE_FIELDS='),source.indexOf('function amRulesToYAML(')),ctx);
  vm.runInContext(source.slice(source.indexOf('function amAccessSamples('),source.indexOf('// Method multi-select:')),ctx);
  const samples=ctx.amAccessSamples(product==='agentgateway',{namespace:'bookinfo'});
  for(let i=0;i<samples.length;i++)for(const mode of ['form','yaml']){
    ctx.S.ambient.settings.access={action:'DENY',mode,yaml:'[invalid yaml',rules:[{from:[],to:[],when:[{key:'tom',op:'is',values:'be'}]}]};
    ctx.amWireAccessRule();sample.onchange=()=>assert.fail('generic parameter handler must not overwrite the chosen sample with the old invalid DOM');sample.value=String(i);sample.change();
    const got=JSON.parse(JSON.stringify(ctx.amAccessRequest()));assert.equal(got.action,samples[i][1].action);assert.doesNotMatch(JSON.stringify(got),/tom|invalid yaml/);assert.equal(ctx.S.ambient.settings.access.mode,'form');
  }
  assert.equal(paints,samples.length*2);
  const prior=ctx.S.ambient.settings.access;sample.value='';sample.change();assert.equal(ctx.S.ambient.settings.access,prior,'placeholder must not select sample zero');
 }
});
