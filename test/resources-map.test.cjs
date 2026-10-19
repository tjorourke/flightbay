// Smoke and link-logic tests for the resources map client script.
const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const path=require('node:path');
const ctx={window:{},S:{}};
vm.createContext(ctx);
vm.runInContext(fs.readFileSync(path.join(__dirname,'../web/resources-map.js'),'utf8'),ctx);
test('resources map loads and registers rm* functions',()=>{
  assert.equal(typeof ctx.window.rmLoad,'function');
  assert.equal(typeof ctx.window.rmVisible,'function');
  assert.equal(typeof ctx.window.rmEdgeClass,'function');
  assert.equal(typeof ctx.window.rmLinks,'function');
});

const node=(kind,name,ns='app')=>({id:`c/${kind}/${ns}/${name}`,kind,name,namespace:ns});
const graph=()=>{
  const dep=node('Deployment','web'),rs=node('ReplicaSet','web-1'),pod=node('Pod','web-1-x'),sa=node('ServiceAccount','web'),svc=node('Service','web'),es=node('EndpointSlice','web-abc'),other=node('ConfigMap','shared','infra');
  const nodes=[dep,rs,pod,sa,svc,es,other];
  const byId=Object.fromEntries(nodes.map(n=>[n.id,n]));
  const edges=[
    {from:dep.id,to:rs.id,kind:'owner'},{from:rs.id,to:pod.id,kind:'owner'},
    {from:dep.id,to:sa.id,kind:'uses'},{from:rs.id,to:sa.id,kind:'uses'},{from:pod.id,to:sa.id,kind:'uses'},
    {from:svc.id,to:es.id,kind:'endpoints'},{from:es.id,to:pod.id,kind:'endpoints'},
    {from:pod.id,to:other.id,kind:'uses'},
    {from:svc.id,to:'',kind:'selector',broken:true,reason:'selector matches nothing'},
  ];
  return {byId,nodes,edges,dep,rs,pod,sa,svc,es};
};

test('hidden ReplicaSet and EndpointSlice are bridged so the ends still connect',()=>{
  const g=graph();
  const nsNodes=g.nodes.filter(n=>n.namespace==='app');
  const visible=nsNodes.filter(n=>!['ReplicaSet','EndpointSlice'].includes(n.kind));
  const {edges,outside}=ctx.window.rmLinks(g,nsNodes,visible);
  const key=e=>`${g.byId[e.from]?.kind}>${e.to?g.byId[e.to].kind:'-'}:${e.kind}`;
  const keys=edges.map(key).sort();
  // Arrays cross the vm boundary, so compare their JSON rather than prototypes.
  assert.equal(JSON.stringify(keys),JSON.stringify(['Deployment>Pod:owner','Deployment>ServiceAccount:uses','Service>-:selector','Service>Pod:endpoints']));
  const bridged=edges.find(e=>e.kind==='owner');
  assert.equal(JSON.stringify(bridged.via),'["ReplicaSet"]');
  assert.equal(outside,1,'the Pod → infra ConfigMap link leaves the namespace');
  assert.ok(edges.find(e=>e.kind==='selector').broken);
});

test('uses links repeat on the top-most visible owner only',()=>{
  const g=graph();
  const nsNodes=g.nodes.filter(n=>n.namespace==='app');
  const {edges}=ctx.window.rmLinks(g,nsNodes,nsNodes);
  const uses=edges.filter(e=>e.kind==='uses').map(e=>g.byId[e.from].kind);
  assert.equal(JSON.stringify(uses),'["Deployment"]');
});

test('focus draws the selection and its neighbours, crossing hidden kinds for free',()=>{
  const g=graph();
  const nsIds=new Set(g.nodes.filter(n=>n.namespace==='app').map(n=>n.id));
  const shown=n=>!['ReplicaSet','EndpointSlice'].includes(n.kind);
  const names=set=>JSON.stringify([...set].map(id=>g.byId[id]).filter(shown).map(n=>n.kind).sort());
  // One hop from the Deployment reaches the Pod through the hidden ReplicaSet,
  // and the ServiceAccount it uses, but not the Service two hops out.
  const one=ctx.window.rmFocus(g,nsIds,[g.dep.id],1,shown);
  assert.equal(names(one),JSON.stringify(['Deployment','Pod','ServiceAccount']));
  const two=ctx.window.rmFocus(g,nsIds,[g.dep.id],2,shown);
  assert.ok(two.has(g.svc.id),'two hops reaches the Service through its EndpointSlice');
  // Objects in another namespace never join the focus.
  const all=ctx.window.rmFocus(g,nsIds,[g.pod.id],99,shown);
  assert.ok(![...all].some(id=>g.byId[id].namespace!=='app'));
});
