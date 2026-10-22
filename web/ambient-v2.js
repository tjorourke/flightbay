'use strict';
/* Ambient v2: the rebuilt Istio Ambient pages (docs/ux/ambient/PLAN.md).
   Markup, order and wording follow the approved mockups in docs/ux/ambient/*.html.
   On by default. ?amv2=0 (kept in localStorage 'flightbay.amv2') returns to the previous Ambient UI. */

const AM2_FLAG = 'flightbay.amv2';
function am2On() {
  try {
    const q = new URLSearchParams(location.search).get('amv2');
    if (q === '1' || q === '0') { try { localStorage.setItem(AM2_FLAG, q); } catch (_) { /* private mode */ } return q === '1'; }
    return localStorage.getItem(AM2_FLAG) !== '0';
  } catch (_) { return true; }
}

// ---------- routes ----------
const AM2_TABS = ['overview', 'policies', 'map', 'path', 'changes', 'demo'];
const AM2_PARAMS = ['tab', 'ns', 'svcs', 'page', 'kind', 'problems', 'drift', 'group', 'svc', 'stab', 'cview', 'show', 'obj', 'otab', 'drawer', 'dphase', 'demo', 'from', 'to'];

// Parse "#ambient?..." (or the bare query) into a route. Old-style links are redirected:
//   tab=investigate&kind=Service&... -> svc=...&stab=checks
//   tab=wizard&kind=Service&...      -> svc=...&stab=configuration
//   tab=wizard&...&route=            -> obj=.../HTTPRoute/...
function am2ParseRoute(hash, defaultCluster) {
  const raw = String(hash || '').replace(/^#/, '');
  const [view, query] = raw.split('?');
  const p = new URLSearchParams(query || '');
  const r = {view: view || 'ambient'};
  for (const k of AM2_PARAMS) if (p.has(k)) r[k] = p.get(k);
  const legacyTarget = () => {
    const kind = p.get('kind'), name = p.get('name') || p.get('service');
    if (!name) return null;
    const cluster = p.get('cluster') || defaultCluster || '';
    return {cluster, namespace: p.get('namespace') || '', name, kind: p.has('service') ? 'Service' : (kind || 'Service')};
  };
  const t = legacyTarget();
  if (!r.svc && !r.obj && t && (r.tab === 'investigate' || r.tab === 'wizard')) {
    const path = [t.cluster, t.namespace, t.name].join('/');
    if (t.kind === 'Service') {
      if (r.tab === 'wizard' && p.get('route')) { r.obj = [t.cluster, 'HTTPRoute', t.namespace, p.get('route')].join('/'); r.otab = 'summary'; }
      else { r.svc = path; r.stab = r.tab === 'investigate' ? 'checks' : 'configuration'; }
    } else if (t.kind === 'HTTPRoute') { r.obj = [t.cluster, 'HTTPRoute', t.namespace, t.name].join('/'); r.otab = 'summary'; }
    delete r.tab;
  }
  if (r.tab === 'investigate' || r.tab === 'wizard') r.tab = 'overview';
  if (!r.svc && !r.obj && !AM2_TABS.includes(r.tab || '')) r.tab = 'overview';
  return r;
}

function am2Hash(r) {
  const keys = ['tab', 'ns', 'svcs', 'page', 'kind', 'problems', 'drift', 'group', 'svc', 'stab', 'cview', 'show', 'obj', 'otab', 'drawer', 'dphase', 'demo', 'from', 'to'];
  const parts = [];
  for (const k of keys) if (r[k] !== undefined && r[k] !== null && r[k] !== '') parts.push(k + '=' + encodeURIComponent(r[k]).replace(/%2F/gi, '/').replace(/%3A/gi, ':').replace(/%2C/gi, ','));
  return '#ambient' + (parts.length ? '?' + parts.join('&') : '');
}
const am2Route = () => am2ParseRoute(location.hash, S.cluster);
function am2Go(patch, replace) {
  const next = am2Hash({...patch});
  if (replace) { history.replaceState(null, '', next); render(); } else if (location.hash === next) render(); else location.hash = next;
}
const am2SvcRef = svc => { const [cluster, namespace, ...rest] = String(svc || '').split('/'); return {cluster, namespace, name: rest.join('/')}; };
const am2SvcPath = (cluster, ns, name) => [cluster, ns, name].join('/');

// The hash that opens an object from search or an object drawer: a Service opens its page, a route or policy
// its object page, anything else the matching list.
function am2OpenHash(o) {
  if (!o || !o.name) return '';
  if (o.kind === 'Service') return am2Hash({svc: am2SvcPath(o.cluster, o.namespace, o.name), stab: 'configuration'});
  if (['HTTPRoute', 'AuthorizationPolicy'].includes(o.kind)) return am2Hash({obj: [o.cluster, o.kind, o.namespace, o.name].join('/'), otab: 'summary'});
  if (['Gateway', 'Namespace', 'ServiceEntry'].includes(o.kind)) return am2Hash({tab: 'policies', ns: o.kind === 'Namespace' ? o.name : o.namespace});
  return '';
}

// ---------- state ----------
const AM2_AFTER = [];
S.am2 = S.am2 || {data: null, tests: [], ns: '', filter: '', moreNs: false, lastSvc: '', error: ''};

async function am2Load(force) {
  const A = S.am2;
  if (force || !A.data) A.data = await api('api/ambient/services');
  A.tests = await api('api/ambient/tests').catch(() => A.tests || []);
  return A.data;
}

// ---------- shared pieces ----------
// Escape text, setting key=value tokens (app=details) in monospace like the mockup.
const am2Mono = t => esc(t).replace(/\b([A-Za-z][\w.\/-]*=[\w.\/-]+)/g, '<span class="am2-mono">$1</span>');
const am2Pill = (tone, text) => `<span class="am2-pill ${tone}">${esc(text)}</span>`;
const am2Btn = (label, attrs = '', cls = 'sm') => `<button type="button" class="am2-btn ${cls}" ${attrs}>${esc(label)}</button>`;

function am2Header(route, data) {
  const changes = (S.am2.tests || []).length;
  const cur = route.svc || route.obj ? '' : route.tab;
  const sp = am2ScopeParams();
  const tab = (id, label, count) => `<a href="${am2Hash({tab: id === 'overview' ? '' : id, ns: sp.ns, svcs: sp.svcs})}" class="${cur === id ? 'on' : ''}">${esc(label)}${count === undefined || count === null ? '' : `<span class="am2-c">${esc(count)}</span>`}</a>`;
  const scoped = ['overview', 'policies', 'map', 'path'].includes(cur);
  // The Bookinfo installer is not an inventory tab, so it sits beside Docs, on every home tab (DEVIATIONS: header).
  // The right-hand controls wrap as one group and stay right-aligned when the tabs fill the row.
  const demo = cur === 'demo';
  return `<div class="am2-head"><h1>Istio Ambient</h1><nav class="am2-tabs" aria-label="Istio Ambient">${tab('overview', 'Overview')}${tab('policies', 'Policies & routes')}${tab('map', 'Map')}${tab('path', 'Request path')}${tab('changes', 'Changes', changes)}</nav><span class="am2-spacer"></span><div class="am2-head-acts">${scoped ? am2ScopeButton({services: cur === 'map' || cur === 'path'}) : ''}<a class="am2-btn${demo ? ' on' : ''}" id="am2-demo" href="${am2Hash({tab: 'demo', ns: sp.ns, svcs: sp.svcs})}"${demo ? ' aria-current="page"' : ''}>Bookinfo demo</a><a class="am2-btn" href="/docs/desk/ambient/" target="_blank" rel="noopener">Docs ↗</a></div></div>`;
}

// ---------- Overview (mockup 00, scoped and paged for real data volume: REVIEW R2) ----------
const AM2_PAGE = 25, AM2_ATTN_CAP = 5;
function am2Clusters(sum) {
  const c = sum.clusters || {count: 0, names: [], ambientClusters: []};
  const amb = (c.ambientClusters || []).length;
  const tail = !c.count ? '' : amb === c.count ? (c.count === 1 ? ' · ambient installed' : c.count === 2 ? ' · ambient on both' : ' · ambient on all') : ` · ambient on ${amb} of ${c.count}`;
  return `<b>${esc(c.count)} connected</b> · ${esc((c.names || []).join(', '))}${tail}`;
}

// Pure: attention entries in scope, for the selected cluster.
function am2AttentionInScope(entries, inNs, cluster) {
  return (entries || []).filter(x => inNs(x.ref?.namespace) && (!cluster || (x.clusters || [x.cluster]).includes(cluster)));
}

function am2SummaryCards(data, attn) {
  const s = data.summary, n = s.namespaces || {}, pr = s.policiesRoutes || {};
  const sp = am2ScopeParams(), svcAttn = attn.filter(x => (x.ref?.kind || 'Service') === 'Service'), failing = svcAttn.filter(x => x.state === 'fail').length;
  const card = (hash, q, a, style = '') => `<a class="am2-card" href="${hash}"><div class="am2-q">${q}</div><div class="am2-a" ${style}>${a}</div></a>`;
  const attnCard = failing
    ? card('#am2-attention', 'Needs attention', `<b>${esc(failing)} ${failing === 1 ? 'Service' : 'Services'}</b> fail a check${svcAttn.length > failing ? ` · ${svcAttn.length - failing} with a warning` : ''}`, 'style="color:var(--am2-bad)"')
    : svcAttn.length ? card('#am2-attention', 'Needs attention', `<b>${esc(svcAttn.length)} ${svcAttn.length === 1 ? 'Service' : 'Services'}</b> with a warning`, 'style="color:var(--am2-warn)"')
    : card(am2Hash({tab: ''}), 'Needs attention', '<b>No failed checks</b>');
  return `<div class="am2-sum">${card(am2Hash({tab: 'map', ...sp}), 'Clusters', am2Clusters(s))}${card(am2Hash({tab: 'map', ...sp}), 'Namespaces in ambient', `<b>${esc(n.inAmbient)} of ${esc(n.total)}</b> · ${esc(n.withWaypoint)} with a waypoint`)}${card(am2Hash({tab: 'policies', ...sp}), 'Policies and routes', `<b>${esc(pr.objects)} objects</b> · ${esc(pr.differ)} differ between clusters`)}${attnCard}</div>`;
}

function am2AttentionRows(data, attn) {
  if (!attn.length) return '';
  const names = (data.summary.clusters.names || []);
  const where = names.length === 2 ? 'both clusters' : names.length === 1 ? 'the cluster' : 'all clusters';
  const cols = [
    {key: 'state', label: 'State', width: '96px', sortValue: r => (r.state === 'warn' ? 1 : 0), filter: 'select', filterValue: r => (r.state === 'warn' ? 'Warning' : 'Failed')},
    {key: 'svc', label: 'Service', sortValue: r => (r.ref?.namespace || '') + '/' + (r.ref?.name || ''), filter: 'text'},
    {key: 'clusters', label: 'Clusters', sortValue: r => (r.clusters || [r.cluster]).join(', '), filter: 'select', filterValue: r => r.clusters || [r.cluster]},
    {key: 'detail', label: 'Problem', sortValue: r => r.detail || '', filter: 'text', filterValue: r => (r.detail || '') + ' ' + (r.checks || []).map(c => c.title).join(' ')},
    {key: 'more', label: 'Checks', sortValue: r => (r.more || 0) + 1},
    {key: 'act', label: ''}
  ];
  const row = r => {
    const ref = r.ref || {}, cluster = S.cluster && (r.clusters || []).includes(S.cluster) ? S.cluster : (r.cluster || (r.clusters || [])[0]), path = am2SvcPath(cluster, ref.namespace, ref.name), svc = ref.kind === 'Service' || !ref.kind;
    const open = svc ? am2Hash({svc: path, stab: 'checks'}) : am2Hash({obj: [cluster, ref.kind, ref.namespace, ref.name].join('/'), otab: 'summary'});
    const fix = svc ? am2Hash({svc: path, stab: 'checks', drawer: 'fix:' + (r.checkId || '')}) : open;
    const n = (r.more || 0) + 1;
    return `<tr class="am2-click" data-am2-go="${esc(open)}"><td class="am2-nowrap"><i class="am2-dot ${r.state === 'warn' ? 'warn' : 'bad'}"></i> <span class="am2-sub">${r.state === 'warn' ? 'Warning' : 'Failed'}</span></td><td class="am2-nowrap"><b>${esc(ref.namespace)}/${esc(ref.name)}</b></td><td class="am2-sub am2-nowrap">${esc((r.clusters || [r.cluster]).join(', '))}</td><td>${am2Mono(r.detail)}</td><td class="am2-sub" title="${esc((r.checks || []).map(c => c.cluster + ': ' + c.title).join('\n'))}">${n} ${n === 1 ? 'check' : 'checks'}</td><td class="am2-right am2-act am2-nowrap">${am2Btn('Open checks', `data-am2-go="${esc(open)}"`)}${r.hasFix === false ? '' : ' ' + am2Btn('Fix…', `data-am2-go="${esc(fix)}"`, 'sm p')}</td></tr>`;
  };
  return `<div class="am2-card am2-mb" id="am2-attention"><div class="am2-chead"><h2>Needs attention</h2><span class="am2-sub">${esc(attn.length)} ${attn.length === 1 ? 'Service' : 'Services'} with failed checks or warnings across ${where}</span></div>${am2Table('attention', cols, attn, row, {size: 5, empty: 'No Service matches these filters.'})}</div>`;
}

function am2ClusterCell(row, cluster) {
  const c = row.perCluster?.[cluster];
  if (!c || !c.present) return '<span class="am2-muted" title="Not on this cluster">—</span>';
  if (c.noEndpoints) return am2Pill('bad', 'no endpoints');
  if (c.differs) return am2Pill('warn', 'differs');
  return am2Pill('ok', '✓');
}

function am2ChecksCell(row) {
  const f = row.checks?.fail || 0, w = row.checks?.warn || 0;
  return f ? am2Pill('bad', `${f} failed`) : w ? am2Pill('warn', `${w} ${w === 1 ? 'warning' : 'warnings'}`) : '<span class="am2-sub">Pass</span>';
}

const am2Badges = (list, empty) => list?.length ? list.map(b => am2Pill(b.tone || 'mut', b.label)).join(' ') : `<span class="am2-muted">${empty}</span>`;
const am2HasConfig = r => !!(r.configured || r.access?.length || r.routing?.length || r.resilience?.length || r.checks?.fail || r.checks?.warn);

// Pure: Service rows in scope, filtered, with the unconfigured ones hidden when the list is long.
function am2ServicesInScope(rows, {inSvc, q, showAll, cluster}) {
  const all = (rows || []).filter(r => inSvc(r.ref.namespace, r.ref.name) && (!cluster || r.perCluster?.[cluster]?.present) && (!q || (r.ref.namespace + '/' + r.ref.name).toLowerCase().includes(q)));
  const hidden = all.length > AM2_PAGE && !showAll && !q ? all.filter(r => !am2HasConfig(r)) : [];
  return {rows: hidden.length ? all.filter(r => am2HasConfig(r)) : all, hidden: hidden.length, total: all.length};
}

function am2ClusterWord(row, cluster) {
  const c = row.perCluster?.[cluster];
  return !c || !c.present ? 'Not here' : c.noEndpoints ? 'No endpoints' : c.differs ? 'Differs' : 'Present';
}
function am2ServiceTable(data) {
  const A = S.am2, names = data.summary.clusters.names.filter(n => !S.cluster || n === S.cluster);
  const {rows, hidden} = am2ServicesInScope(data.services, {inSvc: am2InScopeSvc, q: '', showAll: A.showAllSvc, cluster: S.cluster});
  const badges = l => (l || []).map(b => b.label);
  const cols = [
    {key: 'svc', label: 'Service', sortValue: r => r.ref.namespace + '/' + r.ref.name, filter: 'text'},
    {key: 'wp', label: 'Waypoint', sortValue: r => r.waypoint || '', filter: 'text'},
    {key: 'access', label: 'Access', sortValue: r => badges(r.access).join(' ') || 'open', filter: 'select', filterValue: r => badges(r.access).length ? badges(r.access) : ['open']},
    {key: 'routing', label: 'Routing', sortValue: r => badges(r.routing).join(' '), filter: 'select', filterValue: r => badges(r.routing).length ? badges(r.routing) : ['none']},
    {key: 'res', label: 'Resilience', sortValue: r => badges(r.resilience).join(' '), filter: 'select', filterValue: r => badges(r.resilience).length ? badges(r.resilience) : ['none']},
    ...names.map(n => ({key: 'c:' + n, label: n, sortValue: r => am2ClusterWord(r, n), filter: 'select'})),
    {key: 'checks', label: 'Checks', sortValue: r => -((r.checks?.fail || 0) * 100 + (r.checks?.warn || 0)), filter: 'select', filterValue: r => (r.checks?.fail ? 'Failed' : r.checks?.warn ? 'Warning' : 'Pass')},
    {key: 'open', label: ''}
  ];
  const row = r => {
    const first = Object.keys(r.perCluster || {}).find(c => r.perCluster[c].present) || names[0], path = am2SvcPath(S.cluster || first, r.ref.namespace, r.ref.name), sel = A.lastSvc === path;
    return `<tr class="am2-click ${sel ? 'am2-sel' : ''}" data-am2-go="${esc(am2Hash({svc: path, stab: 'configuration'}))}"><td class="am2-nowrap"><b title="${esc(r.ref.namespace + '/' + r.ref.name)}">${esc(r.ref.namespace)}/${esc(am2MidEllipsis(r.ref.name, 30))}</b></td><td class="am2-nowrap">${r.waypoint ? `<span class="am2-mono" title="${esc(r.waypoint)}">${esc(am2MidEllipsis(r.waypoint, 24))}</span>` : '<span class="am2-muted">—</span>'}</td><td>${am2Badges(r.access, 'open')}</td><td>${am2Badges(r.routing, '—')}</td><td>${am2Badges(r.resilience, '—')}</td>${names.map(n => `<td>${am2ClusterCell(r, n)}</td>`).join('')}<td>${am2ChecksCell(r)}</td><td class="am2-right am2-act">${am2Btn('Open', '', sel ? 'sm p' : 'sm')}</td></tr>`;
  };
  const left = hidden || A.showAllSvc ? `<label class="am2-sub am2-check"><input type="checkbox" data-am2-showall ${A.showAllSvc ? 'checked' : ''}> Show Services with nothing configured${hidden ? ` (${hidden})` : ''}</label>` : '';
  return am2Table('services', cols, rows, row, {size: 25, empty: am2ScopeAll() ? 'No Services match.' : 'No Services match. Change the namespaces at the top right to see more.', footLeft: left});
}

function am2NamespaceFooter(data) {
  const sc = S.am2.scope;
  if (sc.ns.length !== 1) return '';
  const ns = (data.namespaces || []).find(n => n.name === sc.ns[0]);
  if (!ns) return '';
  const wp = ns.waypoint ? ` · waypoint <span class="am2-mono">${esc(ns.waypoint)}</span> ${ns.waypointScope === 'services' ? 'for some Services' : 'for all Services'}` : ' · no waypoint';
  return `<div class="am2-foot">Namespace <b>${esc(ns.name)}</b>: ${ns.inAmbient ? 'in ambient' : 'not in ambient'}${wp}<span class="am2-spacer"></span>${am2Btn('Namespace settings', `data-am2-go="${esc(am2Hash({...am2Route(), drawer: 'ns:' + ns.name}))}"`)}</div>`;
}

function am2Overview(data) {
  const attn = am2AttentionInScope(data.needsAttention, am2InScopeNs, S.cluster);
  return `${am2SummaryCards(data, attn)}${am2AttentionRows(data, attn)}<div class="am2-card"><div class="am2-chead"><h2>Services in the mesh</h2></div><div id="am2-svctable">${am2ServiceTable(data)}</div><div id="am2-nsfoot">${am2NamespaceFooter(data)}</div></div>`;
}

// ---------- page dispatch ----------
async function amV2Page() {
  const A = S.am2, route = am2Route();
  S.ambient.tab = {map: 'map', path: 'path', demo: 'demo', changes: 'changes'}[route.tab] || 'investigate';
  let data;
  try { data = await am2Load(); A.error = ''; } catch (e) { A.error = e.message; }
  if (data) am2ScopeInit(route, data);
  if (!data) return `<div class="am2">${am2Header(route, null)}<div class="notice bad" role="alert">${esc(A.error)}</div></div>`;
  let body;
  if (route.svc && typeof am2ServicePage === 'function') body = await am2ServicePage(route, data);
  else if (route.obj && typeof am2ObjectPage === 'function') body = await am2ObjectPage(route, data);
  else if (route.tab === 'policies' && typeof am2PoliciesPage === 'function') body = await am2PoliciesPage(route, data);
  else if (route.tab === 'map' && typeof am2MapPage === 'function') body = await am2MapPage(route, data);
  else if (route.tab === 'path' && typeof am2PathPage === 'function') body = await am2PathPage(route, data);
  else if (route.tab === 'changes' && typeof am2ChangesPage === 'function') body = await am2ChangesPage(route, data);
  else if (route.tab === 'demo' && typeof am2DemoPage === 'function') body = await am2DemoPage(route, data);
  else if (route.svc || route.obj || route.tab !== 'overview') body = `<div class="am2-note am2-muted">This page is not built yet.</div>`;
  else body = (data.services || []).length ? am2Overview(data) : `<div class="am2-card"><div class="am2-empty">No mesh Services were captured on the connected clusters. <a href="${am2Hash({tab: 'demo'})}">Deploy the Bookinfo demo</a> to try Ambient.</div></div>`;
  return `<div class="am2" id="am2-root">${route.svc || route.obj ? '' : am2Header(route, data)}${body}</div>${typeof am2DrawerHost === 'function' ? await am2DrawerHost(route, data) : ''}`;
}

function am2After() {
  const route = am2Route(), root = $('#am2-root');
  if (!root) return;
  AM2_AFTER.forEach(f => f(route));
  const A = S.am2, data = A.data;
  am2ScopeWire();
  if (!route.svc && !route.obj && route.tab === 'overview' && data) {
    $('#am2-svctable')?.addEventListener('change', e => { if (e.target.matches('[data-am2-showall]')) { A.showAllSvc = e.target.checked; $('#am2-svctable').innerHTML = am2ServiceTable(data); } });
  }
  root.onclick = e => {
    const go = e.target.closest('[data-am2-go]');
    if (go && root.contains(go)) { const h = go.dataset.am2Go; if (h.includes('svc=')) { const m = h.match(/svc=([^&]+)/); if (m) A.lastSvc = decodeURIComponent(m[1]); } if (location.hash === h) render(); else location.hash = h; }
  };
}
