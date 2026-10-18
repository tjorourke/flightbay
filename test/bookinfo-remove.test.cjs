const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const path=require('node:path');

test('Bookinfo removal previews exact resources and sends no remove until confirmation',async()=>{
  let elements={},closed=0;const requests=[];
  const ctx=vm.createContext({setInterval,clearInterval,esc:x=>String(x??''),$:s=>elements[s],api:async url=>url.endsWith('contexts')?[{name:'mesh1',liveCluster:'mesh1'}]:{clusters:[]},apiPost:async(url,body)=>{requests.push(body);return body.operation==='preview'?{note:'Namespace retained',installations:[{id:'record-identity',resources:[{kind:'Service',namespace:'bookinfo',name:'reviews'}]}]}:{message:'Bookinfo removed'};},modal:(_,body)=>{elements={};for(const m of body.matchAll(/id="([^"]+)"/g))elements['#'+m[1]]={focus:()=>{},disabled:false};},closeModal:()=>{closed++;},render:async()=>{},toast:()=>{},S:{ambient:{}},RP:{},console});
  vm.runInContext(require('./context-helpers.cjs'),ctx);
  vm.runInContext(fs.readFileSync(path.join(__dirname,'../web/bookinfo-demo.js'),'utf8'),ctx);
  vm.runInContext('bdRun=async fn=>fn()',ctx);
  await ctx.bdRemoveBookinfo('mesh1');assert.equal(requests.filter(r=>r.operation==='remove').length,0);elements['#bd-remove-cancel'].onclick();assert.equal(closed,1);
  await ctx.bdRemoveBookinfo('mesh1');vm.runInContext('BD.namespace="changed-after-preview"',ctx);await elements['#bd-remove-confirm'].onclick();
  const sent=requests.filter(r=>r.operation==='remove');assert.equal(sent.length,1);assert.equal(sent[0].namespace,'bookinfo');assert.equal(sent[0].cluster,'mesh1');assert.equal(sent[0].confirm,true);assert.deepEqual(Array.from(sent[0].ids),['record-identity']);assert.equal(sent[0].resources,undefined,'client must not submit an arbitrary resource deletion list');
});

for(const fails of [false,true])test('Bookinfo removal shows immediate loading, prevents duplicate writes and retains '+(fails?'failure':'success')+' feedback',async()=>{
  let elements={},body='',title='',finishPreview,finishRemove,removeCalls=0;
  const preview=new Promise(resolve=>finishPreview=resolve),remove=new Promise((resolve,reject)=>finishRemove=fails?()=>reject(new Error('controller timed out')):()=>resolve({message:'Resources confirmed absent. Namespace retained.'}));
  const ctx=vm.createContext({setInterval,clearInterval,esc:x=>String(x??''),$:s=>elements[s],api:async url=>url.endsWith('contexts')?[{name:'mesh1',liveCluster:'mesh1'}]:{clusters:[]},apiPost:async(_,q)=>{if(q.operation==='preview')return preview;removeCalls++;return remove;},modal:(t,b)=>{title=t;body=b;elements={};for(const m of b.matchAll(/id="([^"]+)"/g))elements['#'+m[1]]={focus(){}};},closeModal(){},render:async()=>{},S:{ambient:{}},RP:{}});
  vm.runInContext(require('./context-helpers.cjs'),ctx);
  vm.runInContext(fs.readFileSync(path.join(__dirname,'../web/bookinfo-demo.js'),'utf8'),ctx);
  const loading=ctx.bdRemoveBookinfo('mesh1');assert.match(body,/bd-spinner/);assert.match(body,/Nothing is being deleted/);
  await ctx.bdRemoveBookinfo('mesh1');
  finishPreview({note:'Retained namespace',installations:[{id:'owned',resources:[{kind:'Service',namespace:'bookinfo',name:'reviews'}]}]});await loading;
  const confirm=elements['#bd-remove-confirm'].onclick,pending=confirm();assert.match(body,/bd-spinner/);assert.match(body,/Closing this dialog does not cancel removal/);await confirm();assert.equal(removeCalls,1);
  finishRemove();await pending;
  assert.match(title,fails?/needs attention/:/complete/);assert.match(body,fails?/Some resources may already have been removed/:/Removal confirmed/);
  assert.match(vm.runInContext(fails?'BD.error':'BD.notice',ctx),fails?/timed out/:/confirmed absent/);assert.equal(vm.runInContext('BD.removal',ctx),null);
});
