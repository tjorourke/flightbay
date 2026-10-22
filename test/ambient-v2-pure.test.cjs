const {test} = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
const path = require('node:path');
const load = f => fs.readFileSync(path.join(__dirname, '../web', f), 'utf8');
const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'}[c]));
const ctx = vm.createContext({document: {addEventListener() {}}, S: {cluster: '', ambient: {}}, esc, URLSearchParams, location: {search: '', hash: ''}, localStorage: {getItem: () => null, setItem() {}}, LIVE: {}, render() {}, api: async () => ({}), apiPost: async () => ({}), amLiveContext: () => null});
for (const f of ['ambient-v2.js', 'ambient-scope.js', 'ambient-table.js', 'ambient-service.js', 'ambient-drawer.js', 'ambient-policies.js', 'ambient-object.js', 'ambient-home.js']) vm.runInContext(load(f), ctx, {filename: f});

const g = n => vm.runInContext(n, ctx);
const c = new Proxy({}, {get: (_, n) => g(n)});

test('drawer diff renders exact before and after rules, including old raw fields',()=>{
  const html=c.am2PlanDiff({changes:[{ref:{kind:'AuthorizationPolicy',namespace:'app',name:'access'},fields:[{path:'/spec/rules',beforePresent:true,afterPresent:true,before:[{futureField:'keep'}],after:[{futureField:'keep'},{to:[{operation:{methods:['POST']}}]}]}]}]});
  assert.match(html,/Before/);assert.match(html,/After/);assert.match(html,/futureField/);assert.match(html,/POST/);assert.match(html,/\/spec\/rules/);
});

test('methods drawer shows restricted callers and preserves alternatives',()=>{
  ctx.S.ambient.settings={methods:'GET',callerSources:[{principals:['mesh.local/ns/app/sa/client']},{serviceAccounts:['app/other']}]};
  ctx.S.ambient.inventory={serviceAccounts:[{cluster:'c',namespace:'app',name:'client'}]};
  const html=c.am2MethodsForm({cluster:'c',namespace:'app',name:'front',ports:[80]});
  assert.match(html,/<option value="preserve">mesh.local\/ns\/app\/sa\/client or app\/other<\/option>/);assert.match(html,/value="app\/client"/);
});

test('transport denial is rendered as a connection outcome, without an HTTP 403',()=>{
  const html=c.am2CaseRow({id:'deny',request:{method:'GET',path:'/',direct:true},expect:{transport:'connection-refused-or-reset',text:'connection refused or reset (curl 000)'}},{suite:{service:{}},last:{results:[{id:'deny',status:'pass',got:{transport:'connection-reset',text:'connection reset (curl 000)'}}]}});
  assert.match(html,/connection refused or reset \(curl 000\)/);assert.match(html,/connection reset \(curl 000\)/);assert.doesNotMatch(html,/403/);
});

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
test('the header reaches the Bookinfo demo from every home tab and marks it while it is open', () => {
  const sc = ctx.S.am2.scope, saved = {ns: sc.ns, svcs: sc.svcs};
  sc.ns = ['bookinfo', 'kagent']; sc.svcs = [];
  try {
    const demoLink = h => (h.match(/<a [^>]*id="am2-demo"[^>]*>[^<]*<\/a>/) || [''])[0];
    for (const tab of ['overview', 'policies', 'map', 'path', 'changes']) {
      const link = demoLink(c.am2Header({tab}, null));
      assert.match(link, />Bookinfo demo<\/a>$/, tab);
      assert.match(link, /href="#ambient\?tab=demo&ns=bookinfo,kagent"/, tab);
      assert.doesNotMatch(link, /\bon\b|aria-current/, tab);
    }
    const open = c.am2Header({tab: 'demo'}, null);
    assert.match(demoLink(open), /class="am2-btn on"/);
    assert.match(demoLink(open), /aria-current="page"/);
    assert.doesNotMatch(open.match(/<nav class="am2-tabs"[^]*?<\/nav>/)[0], /class="on"/, 'no inventory tab is current on the demo page');
    assert.doesNotMatch(open, /am2-scopebtn/, 'the namespace scope does not apply to the demo page');
    // Scope, demo and Docs wrap together, so on a narrow window they stay right-aligned under the tabs.
    assert.match(c.am2Header({tab: 'map'}, null), /<div class="am2-head-acts"><button[^>]*id="am2-scopebtn"[^]*id="am2-demo"[^]*Docs ↗<\/a><\/div><\/div>$/);
  } finally { sc.ns = saved.ns; sc.svcs = saved.svcs; }
});
test('the demo page keeps the request-path colours the Bookinfo form is drawn with', async () => {
  ctx.bookinfoPage = async () => '<section class="bd-setup"></section>';
  const html = await c.am2DemoPage({tab: 'demo'}, {});
  assert.match(html, /^<div class="am2-oldwrap am-shell am-mode-demo am-mode-path"><section class="bd-setup">/);
  const css = fs.readFileSync(path.join(__dirname, '../web/request-path.css'), 'utf8');
  const used = new Set([...css.matchAll(/\.bd-[^{]*\{[^}]*\}/g)].flatMap(m => [...m[0].matchAll(/var\((--rp-[\w-]+)\)/g)].map(v => v[1])));
  const defined = new Set([...css.match(/^\.am-mode-path\{[^}]*\}/m)[0].matchAll(/(--rp-[\w-]+):/g)].map(v => v[1]));
  assert.ok(used.size > 0);
  for (const v of used) assert.ok(defined.has(v), v + ' is used by the Bookinfo form but not defined on .am-mode-path');
});
test('drawer verdict: a case Flightbay could not check is not a mismatch and leaves the change Live', () => {
  const v = s => { const r = c.am2TestsVerdict(s); return {text: r.text, verified: r.verified}; };
  assert.deepEqual(v(['pass', 'pass', 'pass', 'pass']), {text: 'All matched what we expected', verified: true});
  // Tom's first run: /health could not be attributed without the waypoint log.
  assert.deepEqual(v(['pass', 'unknown', 'pass', 'pass']), {text: '1 could not be checked', verified: null});
  assert.deepEqual(v(['pass', 'fail', 'pass', 'pass']), {text: '1 did not match what we expected', verified: false});
  assert.deepEqual(v(['error', 'unknown', 'skipped', 'pass']), {text: '1 did not match what we expected · 2 could not be checked', verified: false});
  assert.deepEqual(v([]), {text: '', verified: null});
  assert.equal(c.am2CaseFailed('unknown'), false);
  assert.equal(c.am2CaseFailed('error'), true);
});
test('cases sent to another Service or a Pod say so before the path', () => {
  assert.equal(c.am2CaseHost({request: {method: 'GET', path: '/reviews/0', host: 'reviews-v1', port: 9080}}), 'reviews-v1:9080');
  assert.equal(c.am2CaseHost({request: {path: '/reviews/0', host: 'reviews-v1'}}), 'reviews-v1');
  assert.equal(c.am2CaseHost({request: {path: '/', direct: true, port: 9080, host: 'ignored'}}), 'pod-ip:9080');
  assert.equal(c.am2CaseHost({request: {path: '/reviews/0'}}), '');
  assert.equal(c.am2CaseHost({}), '');
});
test('an edit that changes nothing is a note, not an error', () => {
  assert.equal(c.am2PlanWritesNothing({plan: {objects: [], changes: []}}), true);
  assert.equal(c.am2PlanWritesNothing({plan: {objects: [], changes: [{}]}}), false);
  assert.equal(c.am2PlanWritesNothing({plan: {objects: [{}]}}), false);
  assert.equal(c.am2NothingToChange(['kind-mesh1'], 0), 'Nothing to change: kind-mesh1 already has this configuration.');
  assert.equal(c.am2NothingToChange(['kind-mesh1', 'kind-mesh2'], 0), 'Nothing to change: kind-mesh1 and kind-mesh2 already have this configuration.');
  assert.equal(c.am2NothingToChange(['kind-mesh2'], 1), 'kind-mesh2 already has this configuration, so nothing is written there.');
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
test('table: column filters, sort and paging', () => {
  const cols = [{key: 'n', sortValue: r => r.n, filter: 'text'}, {key: 'k', sortValue: r => r.k, filter: 'select', filterValue: r => r.tags}];
  const rows = [{n: 'b-two', k: 'x', tags: ['a']}, {n: 'a-one', k: 'y', tags: ['a', 'b']}, {n: 'c-three', k: 'x', tags: ['b']}];
  const st = {sort: 'n', dir: 1, filters: {}, page: 0, size: 2};
  assert.deepEqual([...c.am2TApply(cols, rows, st).map(r => r.n)], ['a-one', 'b-two', 'c-three']);
  assert.deepEqual([...c.am2TApply(cols, rows, {...st, dir: -1}).map(r => r.n)], ['c-three', 'b-two', 'a-one']);
  assert.deepEqual([...c.am2TApply(cols, rows, {...st, filters: {n: 'tw'}}).map(r => r.n)], ['b-two']);
  assert.deepEqual([...c.am2TApply(cols, rows, {...st, filters: {k: 'b'}}).map(r => r.n)], ['a-one', 'c-three']);
  assert.deepEqual(JSON.parse(JSON.stringify(c.am2TPage(5, {page: 2, size: 2}))), {pages: 3, page: 2, from: 5, to: 5});
  assert.deepEqual(JSON.parse(JSON.stringify(c.am2TPage(5, {page: 9, size: 2}))), {pages: 3, page: 2, from: 5, to: 5});
  assert.deepEqual(JSON.parse(JSON.stringify(c.am2TPage(0, {page: 0, size: 25}))), {pages: 1, page: 0, from: 0, to: 0});
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
