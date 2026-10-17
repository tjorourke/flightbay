const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const path=require('node:path');
const source=fs.readFileSync(path.join(__dirname,'../web/app.js'),'utf8');
const table=vm.runInNewContext(source.slice(source.indexOf('function pxTable('),source.indexOf('async function proxies('))+';pxTable',{esc:String,attr:JSON.stringify});

test('dump policy links honour namespace and kind, rather than a unique name in another scope',()=>{
  const t={title:'Authorization policies',columns:['Namespace','Name','Action'],rows:[['payments','allow','Allow']]};
  const wrongNamespace={kind:'AuthorizationPolicy',namespace:'other',name:'allow'};
  const wrongKind={kind:'AgentgatewayPolicy',namespace:'payments',name:'allow'};
  const correct={kind:'AuthorizationPolicy',namespace:'payments',name:'allow'};
  assert.doesNotMatch(table(t,'',[wrongNamespace,wrongKind]),/data-pxyaml/);
  const linked=table(t,'',[wrongNamespace,wrongKind,correct]);
  assert.equal((linked.match(/data-pxyaml/g)||[]).length,1);
  assert.match(linked,/'?namespace"?:"payments/);
});

test('agentgateway dump policy names link only to their fully matching captured kind',()=>{
  const t={title:'Policies',columns:['Kind','Policy'],rows:[['EnterpriseAgentgatewayPolicy','app/rate-limit']]};
  assert.doesNotMatch(table(t,'',[{kind:'AgentgatewayPolicy',namespace:'app',name:'rate-limit'}]),/data-pxyaml/);
  assert.match(table(t,'',[{kind:'EnterpriseAgentgatewayPolicy',namespace:'app',name:'rate-limit'}]),/data-pxyaml/);
});
