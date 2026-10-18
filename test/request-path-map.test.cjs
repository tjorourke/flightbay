const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const path=require('node:path');
test('request maps keep consecutive hops grid-adjacent, boxes separate and labels within the canvas',()=>{
  const ctx=vm.createContext({});vm.runInContext(fs.readFileSync(path.join(__dirname,'../web/request-path-map.js'),'utf8'),ctx);
  for(const count of [1,2,5,7,8,10])for(const columns of [1,2,3,4]){const g=ctx.rpMapGeometry(count,columns);assert.equal(g.nodes.length,count);g.nodes.forEach((n,i)=>{assert.ok(n.x>=0&&n.y>=0&&n.x+n.width<=g.width&&n.y+n.height<=g.height);if(i){const a=g.nodes[i-1];assert.ok(a.y===n.y||a.x===n.x,'a connector must be straight through the gap');}g.nodes.slice(i+1).forEach(b=>assert.ok(n.x+n.width<=b.x||b.x+b.width<=n.x||n.y+n.height<=b.y||b.y+b.height<=n.y,'node boxes overlap'));});}
});
