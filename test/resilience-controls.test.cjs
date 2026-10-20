const {test}=require('node:test');const assert=require('node:assert/strict');const fs=require('node:fs');const vm=require('node:vm');const path=require('node:path');
function setup(fields={}){
 const service={cluster:'mesh1',namespace:'bookinfo',name:'reviews'};
 const A={tasks:{'global-rate-limit':true},settings:{resilience:{scope:'service'}},result:{environment:{selected:{product:'istio',edition:'enterprise'}}},inventory:{services:[{cluster:'mesh1',namespace:'limits',name:'rls',ports:[8083,8084],portDetails:[{port:8083,name:'grpc'},{port:8084,name:'health'}]}],rateLimitQuotas:[{source:{cluster:'mesh1',namespace:'limits',name:'quotas'},domain:'customer',key:'api',value:'reviews',requests:50,unit:'minute'}]}};
 const ctx=vm.createContext({S:{ambient:A},esc:x=>String(x??''),amOpts:(xs,value)=>xs.map(x=>{const v=typeof x==='object'?x.value:x,l=typeof x==='object'?x.label:x;return `<option value="${v}" ${String(v)===String(value)?'selected':''}>${l}</option>`;}).join(''),amInput:(id,label,value,type='text')=>`<label>${label}<input id="am-${id}" type="${type}" value="${value}"></label>`,amCircuitBreakerDefaults:()=>({maxConnections:100,maxPendingRequests:100,maxRequests:1000,maxRetries:3,maxRequestsPerConnection:0,connectTimeout:'5s'}),amService:()=>service,$:id=>fields[id]||null,document:{querySelectorAll:()=>[]},amInvalidate:()=>{},render:()=>{}});
 vm.runInContext(fs.readFileSync(path.join(__dirname,'../web/resilience.js'),'utf8'),ctx);ctx.amCapture=()=>ctx.amCaptureResilience();return ctx;
}
test('global limiter uses namespace, Service, port, quota and outage dropdowns',()=>{
 const ctx=setup(),html=ctx.amGlobalLimiterMarkup(ctx.amService(),{namespace:'limits',service:'rls',port:8083,domain:'customer',descriptorKey:'api',descriptorValue:'reviews'});
 for(const id of ['rls-namespace','rls-service','rls-port','rls-quota','rls-outage'])assert.ok(html.includes(`<select id="am-${id}">`),id);
 assert.match(html,/50 requests per minute/);assert.doesNotMatch(html,/id="am-rls-domain"/,'known quota does not require raw descriptor fields');
 assert.match(html,/Flightbay does not deploy Redis/);
});
test('retry presets and duration controls replace raw conditions and time strings',()=>{
 const ctx=setup(),html=ctx.amRetryMarkup({retryAttempts:2,timeout:'1m',backendTimeout:'250ms'},{retryOn:'5xx'});
 assert.match(html,/select id="am-retry-preset"/);assert.match(html,/at most 3 attempts/);assert.doesNotMatch(html,/id="am-resilience-retryOn"/);
 assert.match(html,/data-amduration-unit/);assert.deepEqual(JSON.parse(JSON.stringify(ctx.amDurationParts('1m30s'))),{value:'90',unit:'s'});
 assert.equal(ctx.amDurationParts('tom').value,'','invalid legacy values must not silently become a default timeout');
});
test('selecting a limiter derives the advertised gRPC port, not a hard-coded port',()=>{
 const fields={};const field=(id,value)=>fields['#am-'+id]={value,addEventListener(type,fn){this.change=fn;}};
 for(const [id,value]of Object.entries({'resilience-scope':'service','rls-connection':'direct','rls-namespace':'limits','rls-service':'rls','rls-port':'','rls-quota':'custom','rls-domain':'customer','rls-key':'api','rls-value':'reviews','rls-outage':'deny'}))field(id,value);
 const ctx=setup(fields);ctx.amWireResilience();fields['#am-rls-service'].change({stopImmediatePropagation(){}});
 assert.equal(ctx.S.ambient.settings.resilience.global.port,8083);assert.equal(ctx.S.ambient.settings.resilience.global.failClosed,true);
});
test('circuit breaker offers load profiles and explicit error-detection choices',()=>{
 const ctx=setup(),html=ctx.amGuidedCircuitFields();assert.match(html,/am-circuit-profile/);assert.match(html,/Small backend/);
 const detection=ctx.amEndpointFailureMarkup({failureThreshold:3},{failurePercentage:50,disableConsecutive:true});assert.match(detection,/select id="am-failure-trigger"/);assert.doesNotMatch(detection,/id="am-failureThreshold"/);assert.match(detection,/data-amduration/);
});
