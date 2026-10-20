const {test}=require('node:test');const assert=require('node:assert/strict');const vm=require('node:vm');const fs=require('node:fs');const path=require('node:path');
function setup(){const ctx=vm.createContext({S:{ambient:{inventory:{services:[]}}},esc:x=>String(x??''),$:()=>null,amKey:JSON.stringify,amLiveContext:()=>null,toast:message=>assert.fail(message),api:async url=>url.endsWith('contexts')||url.endsWith('profiles')?[]:{defaultPath:'/reviews/0',ports:[9080],dns:{localName:'reviews.bookinfo.svc.cluster.local'},clients:[]}});for(const file of ['http-test.js','http-mesh-test.js'])vm.runInContext(fs.readFileSync(path.join(__dirname,'../web',file),'utf8'),ctx);vm.runInContext('drawHTTPTest=()=>{};',ctx);return ctx;}
test('opening general traffic test clears auto-filled deny-test headers and results',async()=>{
 const ctx=setup();vm.runInContext('HTTP_TEST.service={cluster:"mesh1",namespace:"bookinfo",name:"reviews"};HTTP_TEST.policyTest={action:"DENY"};HTTP_TEST.profile.headers={color:"red"};HTTP_TEST.results=[{status:403}];HTTP_TEST.panel="results";',ctx);
 await ctx.amOpenHTTPTest({cluster:'mesh1',namespace:'bookinfo',name:'reviews'});
 assert.equal(vm.runInContext('Object.keys(HTTP_TEST.profile.headers).length',ctx),0);assert.equal(vm.runInContext('HTTP_TEST.profile.path',ctx),'/reviews/0');assert.equal(vm.runInContext('HTTP_TEST.results.length',ctx),0);assert.equal(vm.runInContext('HTTP_TEST.panel',ctx),'request');
});
test('explicit policy test retains its headers while ordinary saved profiles remain reusable',async()=>{
 const ctx=setup(),svc={cluster:'mesh1',namespace:'bookinfo',name:'reviews'};
 vm.runInContext('HTTP_TEST.policyTest={action:"DENY"};HTTP_TEST.profile.headers={color:"red"};',ctx);await ctx.amOpenHTTPTest(svc,{policyTest:true});assert.equal(vm.runInContext('HTTP_TEST.profile.headers.color',ctx),'red');
 vm.runInContext('HTTP_TEST.policyTest=null;HTTP_TEST.profile.name="Saved caller";HTTP_TEST.profile.headers={"x-test":"keep"};',ctx);await ctx.amOpenHTTPTest(svc);assert.equal(vm.runInContext('HTTP_TEST.profile.headers["x-test"]',ctx),'keep');
});
test('header notice uses recorded result headers and masks sensitive values',()=>{
 const ctx=setup();vm.runInContext('HTTP_TEST.panel="results";HTTP_TEST.meshSnapshot={headers:{color:"red",Authorization:"must-not-appear"},policyTest:{action:"DENY"}};HTTP_TEST.profile.headers={color:"blue"};',ctx);
 const html=ctx.httpRequestHeaderNotice();assert.match(html,/403 is expected/);assert.match(html,/color: red/);assert.doesNotMatch(html,/blue|must-not-appear/);assert.match(html,/\[hidden\]/);assert.match(html,/Start a clean traffic test/);
});
