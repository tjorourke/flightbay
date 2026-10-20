const {test} = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
const path = require('node:path');
const load = f => fs.readFileSync(path.join(__dirname, '../web', f), 'utf8');
const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'}[c]));
const ctx = vm.createContext({S: {cluster: '', ambient: {}}, esc, URLSearchParams, location: {search: '', hash: ''}, localStorage: {getItem: () => null, setItem() {}}, LIVE: {}, render() {}, api: async () => ({}), apiPost: async () => ({}), amLiveContext: () => null});
for (const f of ['ambient-v2.js', 'ambient-service.js', 'ambient-drawer.js', 'ambient-policies.js']) vm.runInContext(load(f), ctx, {filename: f});

const g = n => vm.runInContext(n, ctx);
const c = new Proxy({}, {get: (_, n) => g(n)});

test('Add links use the task short label, with the Service name for mTLS', () => {
  assert.equal(c.am2AddLabel({id: 'methods', shortLabel: 'Allow or block methods'}, {name: 'reviews'}), 'Allow or block methods');
  assert.equal(c.am2AddLabel({id: 'strict-mtls', shortLabel: 'Require mTLS'}, {name: 'reviews'}), 'Require mTLS for reviews');
});
test('split weights are read from the resource summary', () => {
  const w = c.am2Weights({summary: 'Split 90% reviews-v1 / 10% reviews-v2'});
  assert.deepEqual(JSON.parse(JSON.stringify(w)), [{pct: 90, name: 'reviews-v1'}, {pct: 10, name: 'reviews-v2'}]);
  assert.equal(c.am2TaskNow({id: 'weighted-routing', status: 'configured', resources: [{summary: 'Split 80% reviews-v1 / 20% reviews-v2'}]}), 'v1 80% · v2 20%');
});
test('task text for locked, recommended and inherited tasks', () => {
  assert.equal(c.am2TaskNow({status: 'unavailable', reason: 'Needs a waypoint. More text here.'}), 'Needs a waypoint.');
  assert.equal(c.am2TaskNow({status: 'unavailable', shortReason: 'Short', reason: 'Long. Longer.'}), 'Short');
  assert.equal(c.am2TaskNow({status: 'configured', resources: [{action: 'DENY', label: 'Block header color = red'}]}), '1 DENY · Block header color = red');
});
test('answers keep code tags and escape everything else', () => {
  assert.equal(c.am2Safe('x <code>a</code> <b>'), 'x <code>a</code> &lt;b&gt;');
});
test('long object names are shortened in the middle', () => {
  assert.equal(c.am2MidEllipsis('flightbay-test-d7c9c42a35b58c95-deny-rule'), 'flightbay-test-d7c9…-deny-rule');
  assert.equal(c.am2MidEllipsis('short'), 'short');
});
test('YAML highlight marks the name and the action', () => {
  const h = c.am2Yaml('metadata:\n  name: reviews-methods\nspec:\n  action: ALLOW\n  methods:\n  - GET');
  assert.match(h, /<span class="y-k">reviews-methods<\/span>/);
  assert.match(h, /<span class="y-a">ALLOW<\/span>/);
});
test('Applies to renders names in bold and broken targets as a pill', () => {
  assert.match(c.am2AppliesTo({phrase: 'Service reviews :9080', kind: 'service'}), /Service <b>reviews<\/b> :9080/);
  assert.match(c.am2AppliesTo({kind: 'nothing', reason: 'Service x not found'}), /Targets nothing/);
  assert.match(c.am2AppliesTo({phrase: 'Pods app=ratings', kind: 'pods'}), /<span class="am2-mono">app=ratings<\/span>/);
});
test('search and drawer links open the right v2 page', () => {
  assert.equal(c.am2OpenHash({kind: 'Service', cluster: 'c1', namespace: 'a', name: 'b'}), '#ambient?svc=c1/a/b&stab=configuration');
  assert.equal(c.am2OpenHash({kind: 'HTTPRoute', cluster: 'c1', namespace: 'a', name: 'r'}), '#ambient?obj=c1/HTTPRoute/a/r&otab=summary');
  assert.equal(c.am2OpenHash({kind: 'Pod', cluster: 'c1', namespace: 'a', name: 'p'}), '');
});
test('key=value tokens in check text are set in monospace', () => {
  assert.match(c.am2Mono('selector app=details matches 0 Pods'), /<span class="am2-mono">app=details<\/span>/);
});
