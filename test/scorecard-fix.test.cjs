const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const path=require('node:path');

test('Scorecard fixes show scoped YAML and require confirmation after dry-run',async()=>{
  const src=fs.readFileSync(path.join(__dirname,'../web/app.js'),'utf8');
  const fn=src.slice(src.indexOf('async function pstReviewFix('),src.indexOf('// ---------- agent reach'));
  let elements={},html='',accepted=false,confirmations=0;const calls=[];
  const ctx=vm.createContext({S:{ambient:{}},esc:x=>String(x??''),guideLinks:()=>'',navigator:{clipboard:{writeText:async()=>{}}},amDownload:()=>{},toast:()=>{},openObject:()=>{},closeModal:()=>{},
    $:s=>elements[s],modal:(_,body)=>{html=body;elements={};for(const match of body.matchAll(/id="([^"]+)"/g))elements['#'+match[1]]={setAttribute:()=>{}};},
    api:async()=>[{name:'mesh1',server:'https://mesh1'}],
    apiPost:async(url,body)=>{calls.push({url,body});if(url==='api/ambient/plan')return {id:'scoped-plan',request:body,notes:['Can deny traffic'],yaml:'namespace: app\nkind: AuthorizationPolicy'};if(body.operation==='validate')return {token:'validated',context:body.context};return {test:{context:body.context,token:'validated'},message:'Applied'};},
    amConfirmApply:async entries=>{confirmations++;assert.equal(entries[0].plan.id,'scoped-plan');assert.equal(entries[0].context,'mesh1');return accepted;},console});
  vm.runInContext(fn,ctx);
  await ctx.pstReviewFix({id:'ambient.default-deny',title:'Default deny',fix:'namespace: <ns>'},{cluster:'mesh1',kind:'Namespace',resource:'namespaces',name:'app',namespace:'',group:'',detail:'no default deny'});
  assert.match(html,/Exact YAML to apply/);assert.match(html,/mesh1/);assert.match(html,/namespace: app/);
  elements['#pst-fix-context'].onchange({target:{value:'mesh1'}});
  await elements['#pst-fix-validate'].onclick();
  await elements['#pst-fix-apply'].onclick();
  assert.equal(confirmations,1);assert.equal(calls.filter(x=>x.body.operation==='apply').length,0,'cancelling must never send an apply request');
  accepted=true;await elements['#pst-fix-apply'].onclick();
  assert.equal(calls.filter(x=>x.body.operation==='apply').length,1);
  assert.equal(calls.find(x=>x.body.operation==='apply').body.validation,'validated');
});
