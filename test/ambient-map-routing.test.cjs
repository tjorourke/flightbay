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
test('return peering uses a clear bottom lane instead of squeezing arrowheads between cards',()=>{
  const leftGateway={x:40,y:144,w:286,h:252},leftPeer={x:342,y:144,w:286,h:252};
  const rightGateway={x:1020,y:144,w:286,h:252},rightPeer={x:1322,y:144,w:286,h:252};
  const obstacles=[leftGateway,leftPeer,rightGateway,rightPeer,{x:40,y:424,w:286,h:252},{x:1020,y:424,w:286,h:252}];
  const route=context.amRoute(rightPeer,leftGateway,obstacles);
  assert.ok(route);
  assert.ok(route[0].y>rightPeer.y+rightPeer.h,'return path should leave the bottom of the source');
  assert.ok(route.at(-1).y>leftGateway.y+leftGateway.h,'arrow should arrive below the destination');
  for(let i=1;i<route.length;i++)assert.equal(context.amSegmentBlocked(route[i-1],route[i],obstacles),false);
  assert.ok(context.amRoundedRoute(route).includes('Q'),'bends should be rounded');
  assert.equal(route.at(-1).y-(leftGateway.y+leftGateway.h),6,'arrow tip has a visible gap from the border');
});
test('fractional zoom geometry does not introduce tiny diagonal segments or arrowhead kinks',()=>{
  const a={x:40.000004,y:144.000002,w:286.000007,h:252.000005},b={x:1020.000003,y:144.000001,w:286.000007,h:252.000005};
  const route=context.amRoute(a,b,[a,b]);
  assert.equal(route.length,2,'straight connection stays straight after rounding SVG coordinates');
  assert.equal(route[0].y,route[1].y);
  assert.equal(context.amRoundedRoute(route).includes('Q'),false);
});
