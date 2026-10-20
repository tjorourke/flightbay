const {test} = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
const path = require('node:path');
const load = f => fs.readFileSync(path.join(__dirname, '../web', f), 'utf8');
const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'}[c]));
const ctx = vm.createContext({S: {cluster: '', ambient: {}}, esc, URLSearchParams, location: {search: '', hash: ''}, localStorage: {getItem: () => null, setItem() {}}, LIVE: {}, render() {}, api: async () => ({}), apiPost: async () => ({}), amLiveContext: () => null});
for (const f of ['ambient-v2.js', 'ambient-scope.js', 'ambient-service.js', 'ambient-drawer.js', 'ambient-policies.js', 'ambient-object.js']) vm.runInContext(load(f), ctx, {filename: f});

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
  assert.equal(c.am2MidEllipsis('flightbay-test-d7c9c42a35b58c95-deny-rule'), 'flightbay-test-d7c9c4…-deny-rule');
  assert.ok(c.am2MidEllipsis('a'.repeat(60), 40).length <= 40);
  assert.equal(c.am2MidEllipsis('short'), 'short');
});
test('YAML highlight marks the name and the action', () => {
  const h = c.am2Yaml('metadata:\n  name: reviews-methods\nspec:\n  action: ALLOW\n  methods:\n  - GET');
  assert.match(h, /<span class="y-k">reviews-methods<\/span>/);
  assert.match(h, /<span class="y-a">ALLOW<\/span>/);
});
test('Applies to renders names in bold and broken targets as a pill', () => {
  assert.match(c.am2AppliesTo({phrase: 'Service reviews :9080', kind: 'service'}), /Service <b[^>]*>reviews<\/b> <span class="am2-sub">:9080<\/span>/);
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

// ---- REVIEW-2026-10-20 ----
const ns = (name, o = {}) => ({name, services: 3, configured: 0, inAmbient: false, ...o});
test('default scope: configured ambient namespaces, never everything when there are many', () => {
  assert.deepEqual([...c.am2ScopeDefault([ns('bookinfo', {inAmbient: true, configured: 2}), ns('kagent', {inAmbient: true, configured: 5}), ns('other', {inAmbient: true})])], ['bookinfo', 'kagent']);
  assert.deepEqual([...c.am2ScopeDefault([ns('a', {inAmbient: true, services: 2}), ns('b', {inAmbient: true, services: 9})])], ['b']);
  assert.deepEqual([...c.am2ScopeDefault([ns('istio-system', {inAmbient: true, configured: 3}), ns('x')])], []);
  const many = Array.from({length: 12}, (_, i) => ns('n' + i, {services: i}));
  assert.deepEqual([...c.am2ScopeDefault(many)], ['n11']);
});
test('scope resolves from the route first, then storage, then the default', () => {
  const nss = [ns('bookinfo', {inAmbient: true, configured: 1})];
  assert.deepEqual(JSON.parse(JSON.stringify(c.am2ScopeResolve({ns: 'a,b', svcs: 'a/x'}, {ns: ['z']}, nss))), {ns: ['a', 'b'], svcs: ['a/x']});
  assert.deepEqual(JSON.parse(JSON.stringify(c.am2ScopeResolve({ns: '*'}, {ns: ['z']}, nss))), {ns: [], svcs: []});
  assert.deepEqual(JSON.parse(JSON.stringify(c.am2ScopeResolve({}, {ns: ['z'], svcs: []}, nss))), {ns: ['z'], svcs: []});
  assert.deepEqual(JSON.parse(JSON.stringify(c.am2ScopeResolve({}, null, nss))), {ns: ['bookinfo'], svcs: []});
});
test('scope label', () => {
  assert.equal(c.am2ScopeLabel([], []), 'All namespaces');
  assert.equal(c.am2ScopeLabel(['a', 'b', 'c', 'd'], ['a/x']), 'Namespaces: a, b +2 · 1 Service');
});
test('scope survives the hash round trip with commas kept readable', () => {
  const h = c.am2Hash({tab: 'policies', ns: 'bookinfo,kagent', svcs: 'bookinfo/reviews'});
  assert.equal(h, '#ambient?tab=policies&ns=bookinfo,kagent&svcs=bookinfo/reviews');
  const r = c.am2ParseRoute(h, '');
  assert.equal(r.ns, 'bookinfo,kagent'); assert.equal(r.svcs, 'bookinfo/reviews');
});
test('attention entries follow the namespace scope and the cluster', () => {
  const e = [{ref: {namespace: 'a', name: 'x'}, clusters: ['c1']}, {ref: {namespace: 'b', name: 'y'}, clusters: ['c1', 'c2']}];
  assert.equal(c.am2AttentionInScope(e, n => n === 'b', '').length, 1);
  assert.equal(c.am2AttentionInScope(e, () => true, 'c2').length, 1);
});
test('long Service lists hide rows with nothing configured', () => {
  const rows = Array.from({length: 30}, (_, i) => ({ref: {namespace: 'a', name: 's' + i}, configured: i < 4, perCluster: {}}));
  const r = c.am2ServicesInScope(rows, {inSvc: () => true, q: '', showAll: false, cluster: ''});
  assert.equal(r.rows.length, 4); assert.equal(r.hidden, 26); assert.equal(r.total, 30);
  assert.equal(c.am2ServicesInScope(rows, {inSvc: () => true, q: '', showAll: true, cluster: ''}).rows.length, 30);
  assert.equal(c.am2ServicesInScope(rows, {inSvc: () => true, q: 's1', showAll: false, cluster: ''}).rows.length, 11);
});
test('policies sort by what they apply to, keeping a Service together', () => {
  const r = (n, ns, phrase, kind) => ({ref: {name: n, namespace: ns}, appliesTo: {phrase, kind}, perCluster: {}});
  const out = c.am2PolSort([r('b', 'x', 'Service s2'), r('z', 'x', 'nothing', 'nothing'), r('a', 'x', 'Service s1'), r('c', 'x', 'Service s1')], 'applies', 1).map(x => x.ref.name);
  assert.deepEqual([...out], ['a', 'c', 'b', 'z']);
});
test('observed split percentages from streamed counts', () => {
  assert.deepEqual(JSON.parse(JSON.stringify(c.am2Observed({v1: 79, v2: 8}, 87))), {v1: 90.8, v2: 9.2});
  assert.deepEqual(JSON.parse(JSON.stringify(c.am2Observed({}, 0))), {});
});
test('streamed run events fold into the live state', () => {
  const live = {total: 0, cases: [], current: '', samples: {}, results: {}, done: null, error: ''};
  c.am2RunEvent(live, {type: 'start', total: 2, cases: ['a', 'b']});
  c.am2RunEvent(live, {type: 'case-start', id: 'a'});
  assert.equal(live.current, 'a');
  c.am2RunEvent(live, {type: 'sample', id: 'a', completed: 10, requested: 200});
  assert.equal(live.samples.a.completed, 10);
  c.am2RunEvent(live, {type: 'case-done', id: 'a', result: {id: 'a', status: 'pass'}});
  assert.equal(live.current, ''); assert.equal(live.results.a.status, 'pass');
  c.am2RunEvent(live, {type: 'done', results: []});
  assert.ok(live.done);
});
