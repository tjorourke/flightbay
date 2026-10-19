// Minimal smoke for the resources map client script.
// It re-uses ambient map primitives; we just assert it defines the expected globals without throwing on load.
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
});
