const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const path=require('node:path');
test('missing-Service guidance works for arbitrary applications and distinguishes consumption from contribution',()=>{
  const ctx=vm.createContext({esc:String});vm.runInContext(require('./context-helpers.cjs'),ctx);
  vm.runInContext(fs.readFileSync(path.join(__dirname,'../web/global-publication.js'),'utf8'),ctx);
  const service={namespace:'payments',name:'checkout-api'},contexts=[{name:'remote-customer-context',liveCluster:'customer-west',server:'https://west'}];
  const html=ctx.amGlobalSetupMarkup(service,'customer-west',contexts);
  assert.match(html,/payments\/checkout-api/);assert.match(html,/customer-west/);assert.match(html,/consuming cluster does not need/);assert.match(html,/normal manifests, Helm release or GitOps pipeline/);assert.match(html,/Missing capture evidence does not prove/);assert.doesNotMatch(html,/bookinfo/i);
  assert.equal(ctx.amMissingServiceCommand(service,'remote-customer-context'),"kubectl --context 'remote-customer-context' -n 'payments' get service 'checkout-api' -o yaml");
  assert.equal(ctx.amMissingServiceCommand(service,''),'');
  assert.match(ctx.amGlobalSetupMarkup(service,'offline-cluster',[]),/id="am-global-copy-check" disabled/);
});

test('an export label alone does not report global publication readiness',()=>{
  const ctx=vm.createContext({esc:String});vm.runInContext(fs.readFileSync(path.join(__dirname,'../web/global-publication.js'),'utf8'),ctx);
  const row={service:{},global:true};assert.match(ctx.amPublicationEvidenceLabel(row),/alias evidence missing/);
  row.publication={globalAliases:[{hosts:['checkout.payments.mesh.internal'],addresses:[]}]};assert.match(ctx.amPublicationEvidenceLabel(row),/alias addresses missing/);
  row.publication.globalAliases[0].addresses=['240.240.0.7'];assert.match(ctx.amPublicationEvidenceLabel(row),/traffic unverified/);assert.doesNotMatch(ctx.amPublicationEvidenceLabel(row),/ready|working/i);
});
