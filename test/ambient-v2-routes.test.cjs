const {test} = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
const path = require('node:path');
const source = fs.readFileSync(path.join(__dirname, '../web/ambient-v2.js'), 'utf8');
const ctx = vm.createContext({S: {cluster: ''}, URLSearchParams, location: {search: '', hash: ''}, localStorage: {getItem: () => null, setItem() {}}, esc: String});
vm.runInContext(source, ctx);
const parse = (h, c) => JSON.parse(JSON.stringify(ctx.am2ParseRoute(h, c)));

test('bare and overview routes land on Overview', () => {
  assert.equal(parse('#ambient').tab, 'overview');
  assert.equal(parse('#ambient?tab=overview').tab, 'overview');
  assert.equal(parse('#ambient?tab=bogus').tab, 'overview');
});
test('service, object and drawer routes round-trip', () => {
  for (const h of ['#ambient?svc=kind-mesh1/bookinfo/reviews&stab=configuration&cview=all&show=list',
    '#ambient?svc=kind-mesh1/bookinfo/reviews&stab=checks&drawer=fix:workloads',
    '#ambient?obj=kind-mesh1/AuthorizationPolicy/bookinfo/p&otab=tests',
    '#ambient?tab=policies&ns=bookinfo&problems=1&drift=1&group=service',
    '#ambient?svc=kind-mesh1/bookinfo/reviews&stab=configuration&drawer=add:methods&dphase=3&demo=1',
    '#ambient?tab=demo&ns=bookinfo,kagent']) {
    assert.equal(ctx.am2Hash(parse(h)), h);
  }
});
test('old links redirect to the new routes', () => {
  const inv = parse('#ambient?tab=investigate&cluster=kind-mesh1&namespace=bookinfo&kind=Service&name=reviews');
  assert.equal(inv.svc, 'kind-mesh1/bookinfo/reviews'); assert.equal(inv.stab, 'checks'); assert.equal(inv.tab, undefined);
  const wiz = parse('#ambient?tab=wizard&cluster=kind-mesh1&namespace=bookinfo&service=reviews');
  assert.equal(wiz.svc, 'kind-mesh1/bookinfo/reviews'); assert.equal(wiz.stab, 'configuration');
  const route = parse('#ambient?tab=wizard&cluster=kind-mesh1&namespace=bookinfo&service=reviews&route=r1');
  assert.equal(route.obj, 'kind-mesh1/HTTPRoute/bookinfo/r1');
  assert.equal(parse('#ambient?tab=path&from=a&to=b').tab, 'path');
  assert.equal(parse('#ambient?tab=map').tab, 'map');
  assert.equal(parse('#ambient?tab=demo').tab, 'demo');
});
test('legacy routes use the global cluster when none is given', () => {
  assert.equal(parse('#ambient?tab=investigate&namespace=a&kind=Service&name=b', 'kind-mesh2').svc, 'kind-mesh2/a/b');
});
