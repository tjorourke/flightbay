const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const path=require('node:path');
const context=vm.createContext({});
vm.runInContext(fs.readFileSync(path.join(__dirname,'../web/ambient-map-routing.js'),'utf8'),context);
test('cross-cluster wires route around every resource, including a blocking middle card',()=>{
  const a={x:40,y:140,w:280,h:250},b={x:1100,y:420,w:280,h:250},middle={x:400,y:100,w:300,h:700};
  const obstacles=[a,b,middle,{x:20,y:20,w:620,h:100},{x:1000,y:300,w:620,h:100}];
  const route=context.amRoute(a,b,obstacles);
  assert.ok(route&&route.length>2);
  for(let i=1;i<route.length;i++){
    assert.ok(route[i].x===route[i-1].x||route[i].y===route[i-1].y,'connector must be orthogonal');
    assert.equal(context.amSegmentBlocked(route[i-1],route[i],obstacles),false,'connector crosses an object');
  }
});
test('overlapped endpoints do not produce a misleading line through a card',()=>{
  const a={x:20,y:20,w:280,h:250},b={x:25,y:25,w:280,h:250};
  assert.equal(context.amRoute(a,b,[a,b]),null);
});
