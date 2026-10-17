const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const path=require('node:path');

test('return from logs restores the wizard, caller cluster, modal position and original results without rerunning HTTP',()=>{
  const selector={value:''},main={scrollTop:22},modal={scrollTop:410},details=[{open:true},{open:false}];let restored=0,focused=false;
  const context=vm.createContext({S:{view:'ambient',cluster:'',ambient:{key:'service-key',kind:'Service',tab:'wizard',phase:3,clusterScope:'',pickerOpen:false,settings:{retained:'draft'}}},location:{hash:'#ambient?tab=map'},scrollX:0,scrollY:70,document:{querySelector:s=>s==='.http-test-modal'?modal:s==='.http-test'?{}:s.startsWith('[data-http-logs=')?{focus:()=>focused=true}:null,querySelectorAll:s=>s==='.http-test details'?details:[]},$:s=>s==='#cluster'?selector:s==='#main'?main:null,requestAnimationFrame:fn=>fn(),scrollTo:(x,y)=>assert.equal(y,70),openLogs:filters=>{context.S.view='logs';context.location.hash='#logs';assert.equal(filters.namespace,'gateway-ns');},amKey:()=>'',console});
  vm.runInContext(fs.readFileSync(path.join(__dirname,'../web/http-test.js'),'utf8'),context);
  const state=vm.runInContext('HTTP_TEST',context);state.service={cluster:'mesh1',namespace:'app',name:'web'};state.results=[{id:'keep-result-id',started:'2026-10-17T12:00:00Z',finished:'2026-10-17T12:00:01Z',pod:{cluster:'mesh1',namespace:'gateway-ns',name:'gateway-pod'}}];state.profile.headers={Authorization:'unsaved-local-value'};
  context.httpTestOpenLogs(0);assert.equal(context.S.cluster,'mesh1');assert.match(context.httpTestReturnAction(),/Back to HTTP test/);
  context.S.ambient.tab='map';context.S.ambient.phase=1;context.S.ambient.key='URL-reset-key';context.location.hash='#ambient?tab=map';context.S.view='ambient';
  context.httpTestNavigation();context.drawHTTPTest=()=>restored++;context.httpTestNavigationRendered();
  assert.equal(context.S.cluster,'');assert.equal(context.S.ambient.tab,'wizard');assert.equal(context.S.ambient.phase,3);assert.equal(context.S.ambient.key,'service-key');assert.equal(context.S.ambient.settings.retained,'draft');assert.equal(state.results[0].id,'keep-result-id');assert.equal(state.profile.headers.Authorization,'unsaved-local-value');assert.equal(modal.scrollTop,410);assert.equal(restored,1);assert.equal(focused,true);
  context.httpTestNavigationRendered();assert.equal(restored,1,'normal live redraw must not reopen a dismissed modal');
});
