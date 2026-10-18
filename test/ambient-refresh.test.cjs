const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const path=require('node:path');

test('inspection commands use mirror contexts in the built-in terminal',()=>{
  const source=fs.readFileSync(path.join(__dirname,'../web/ambient.js'),'utf8');
  const ctx=vm.createContext({S:{ws:{live:true}}});
  vm.runInContext(source.slice(source.indexOf('function amMirrorCommand('),source.indexOf('function amFixGuide(')),ctx);
  const command="kubectl --context 'kind-mesh1' get 'httproutes.gateway.networking.k8s.io' 'reviews-mesh-split' -n 'bookinfo' -o yaml";
  assert.equal(ctx.amMirrorCommand(command),command.replace("'kind-mesh1'","'kind-mesh1@live'"));
  ctx.S.ws.live=null;assert.equal(ctx.amMirrorCommand(command),command.replace("'kind-mesh1'","'kind-mesh1@snapshot'"));
});

test('ambient refresh retains open and closed checks, nested evidence and scroll by check identity',()=>{
  const source=fs.readFileSync(path.join(__dirname,'../web/ambient.js'),'utf8');
  const code=source.slice(source.indexOf('function amViewScope('),source.indexOf('function amFixGuide('));
  const check=(id,open)=>({dataset:{amStep:id},open,matches:()=>true,closest(){return this;}});
  const evidence=(parent,open)=>({open,matches:()=>false,closest:()=>parent,querySelector:()=>({textContent:'Check details · captured fields'})});
  let a=check('route-parent',false),b=check('endpoints',true),nested=evidence(a,true),details=[a,nested,b],position;
  const main={scrollTop:150},workspace={dataset:{}};
  const ctx=vm.createContext({S:{view:'ambient',cluster:'mesh1',ambient:{key:'petstore',route:'',tab:'investigate'}},document:{querySelectorAll:()=>details},$:s=>s==='#main'?main:workspace,scrollX:0,scrollY:420,scrollTo:(x,y)=>position=[x,y]});
  vm.runInContext(code,ctx);workspace.dataset.amScope=ctx.amViewScope();
  const state=ctx.amCaptureView();
  a=check('route-parent',true);b=check('endpoints',false);nested=evidence(a,false);details=[b,a,nested];main.scrollTop=0;
  ctx.amRestoreView(state);
  assert.equal(a.open,false,'a failed check closed by the reader stays closed');assert.equal(b.open,true);assert.equal(nested.open,true);assert.equal(main.scrollTop,150);assert.deepEqual(position,[0,420]);
  ctx.S.ambient.key='different-service';nested.open=false;ctx.amRestoreView(state);assert.equal(nested.open,false,'a different target must not inherit expansion state');
});
