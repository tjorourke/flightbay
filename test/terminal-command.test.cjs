const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const path=require('node:path');
test('terminal command controls copy executable read commands with explicit mirror contexts',async()=>{
  const source=fs.readFileSync(path.join(__dirname,'../web/app.js'),'utf8');
  const code=source.slice(source.indexOf('const shellArg ='),source.indexOf('// morphHTML'));
  let copied='',rows=[];
  const ctx=vm.createContext({S:{ws:{live:true,clusters:[{name:'mesh1'},{name:'mesh2'}]},cluster:''},navigator:{clipboard:{writeText:async s=>copied=s}},toast:()=>{},document:{createElement:tag=>({tag,children:[],attrs:{},setAttribute(k,v){this.attrs[k]=v;},appendChild(el){this.children.push(el);if(el.className==='command-copy-tools')rows.push(el);}})}});
  vm.runInContext(code,ctx);
  const pre=text=>({textContent:text,dataset:{},closest:()=>null,replaceWith:()=>{}});
  const blocks=[pre('kubectl get pods -A'),pre("kubectl --context 'mesh2' get services -A"),pre('kubectl delete pod <name>'),pre('kubectl label namespace app istio.io/dataplane-mode=ambient')];
  ctx.wireKubectlBlocks({querySelectorAll:()=>blocks});
  await rows[0].children[0].onclick();assert.equal(copied,"kubectl --context 'mesh1@live' get pods -A\nkubectl --context 'mesh2@live' get pods -A");
  await rows[1].children[0].onclick();assert.equal(copied,"kubectl --context 'mesh2@live' get services -A");
  assert.equal(rows[2].children[0].attrs['aria-label'],'Copy template');assert.match(rows[3].children[0].attrs['aria-label'],/real cluster only/);assert.equal(rows[0].children[0].textContent,'⧉');
  ctx.S.ws.live=null;assert.equal(ctx.mirrorKubectl('get pods','mesh1'),"kubectl --context 'mesh1@snapshot' get pods");
});
