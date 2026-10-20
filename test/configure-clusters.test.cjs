const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const path=require('node:path');
const source=fs.readFileSync(path.join(__dirname,'../web/configure-clusters.js'),'utf8');

test('multi-cluster edits use each peers own resource fingerprint or create when absent',async()=>{
  const original={cluster:'mesh1',namespace:'bookinfo',service:'reviews',tasks:['access-rule'],access:{action:'DENY',rules:[{when:[{key:'request.headers[color]',values:['red']}]}]},edit:{ref:{cluster:'mesh1',namespace:'bookinfo',kind:'AuthorizationPolicy',name:'deny'},rule:0,fingerprint:'source'}};
  const peer={ref:{cluster:'mesh2',namespace:'bookinfo',kind:'AuthorizationPolicy',name:'deny'},rule:0,fingerprint:'peer'};
  const ctx=vm.createContext({S:{ambient:{settings:{applyClusters:['mesh1','mesh2']},contexts:[]}},api:async()=>({editable:[{ref:peer.ref,rule:0,supported:true,request:{edit:peer}}]}),amLiveContext:()=>null,apiPost:async(_,request)=>({request,objects:[{}],changes:[]})});vm.runInContext(source,ctx);
  const remapped=await ctx.amConfigurationRequestForCluster(original,'mesh2');assert.equal(remapped.edit.fingerprint,'peer');assert.equal(remapped.edit.ref.cluster,'mesh2');assert.equal(original.edit.fingerprint,'source');
  ctx.api=async()=>({editable:[]});const created=await ctx.amConfigurationRequestForCluster(original,'mesh2');assert.equal(created.edit,undefined);assert.deepEqual(JSON.parse(JSON.stringify(created.access)),original.access);
  await ctx.amGenerateConfigurationBatch(original);assert.equal(ctx.S.ambient.globalBatch.length,2);assert.equal(ctx.S.ambient.globalBatch[0].plan.request.cluster,'mesh1');assert.equal(ctx.S.ambient.globalBatch[1].plan.request.cluster,'mesh2');assert.equal(ctx.S.ambient.phase,3);
});

test('a peer preview failure is retained separately and does not become a valid plan',async()=>{
  const ctx=vm.createContext({S:{ambient:{settings:{applyClusters:['mesh1','mesh2']},contexts:[]}},amLiveContext:()=>null,apiPost:async(_,request)=>{if(request.cluster==='mesh2')throw new Error('no supported waypoint');return {objects:[{}],changes:[]};}});vm.runInContext(source,ctx);
  await ctx.amGenerateConfigurationBatch({cluster:'mesh1',tasks:['methods']});assert.equal(ctx.S.ambient.globalBatch[1].plan,null);assert.match(ctx.S.ambient.globalBatch[1].error,/no supported waypoint/);
});
