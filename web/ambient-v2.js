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
const AM2_PARAMS = ['tab', 'ns', 'kind', 'problems', 'drift', 'group', 'svc', 'stab', 'cview', 'show', 'obj', 'otab', 'drawer', 'dphase', 'demo', 'from', 'to'];

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
  const keys = ['tab', 'ns', 'kind', 'problems', 'drift', 'group', 'svc', 'stab', 'cview', 'show', 'obj', 'otab', 'drawer', 'dphase', 'demo', 'from', 'to'];
  const parts = [];
  for (const k of keys) if (r[k] !== undefined && r[k] !== null && r[k] !== '') parts.push(k + '=' + encodeURIComponent(r[k]).replace(/%2F/gi, '/').replace(/%3A/gi, ':'));
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
  const policies = data?.summary?.policiesRoutes?.objects;
  const changes = (S.am2.tests || []).length;
  const cur = route.svc || route.obj ? '' : route.tab;
  const tab = (id, label, count) => `<a href="${am2Hash({tab: id === 'overview' ? '' : id})}" class="${cur === id ? 'on' : ''}">${esc(label)}${count === undefined || count === null ? '' : `<span class="am2-c">${esc(count)}</span>`}</a>`;
  return `<div class="am2-head"><h1>Istio Ambient</h1><nav class="am2-tabs" aria-label="Istio Ambient">${tab('overview', 'Overview')}${tab('policies', 'Policies & routes', policies)}${tab('map', 'Map')}${tab('path', 'Request path')}${tab('changes', 'Changes', changes)}</nav><span class="am2-spacer"></span><a class="am2-btn" href="/docs/desk/ambient/" target="_blank" rel="noopener">Docs ↗</a></div>`;
}

// ---------- Overview (mockup 00) ----------
function am2Clusters(sum) {
  const c = sum.clusters || {count: 0, names: [], ambientClusters: []};
  const amb = (c.ambientClusters || []).length;
  const tail = !c.count ? '' : amb === c.count ? (c.count === 1 ? ' · ambient installed' : c.count === 2 ? ' · ambient on both' : ' · ambient on all') : ` · ambient on ${amb} of ${c.count}`;
  return `<b>${esc(c.count)} connected</b> · ${esc((c.names || []).join(', '))}${tail}`;
}

function am2SummaryCards(data) {
  const s = data.summary, n = s.namespaces || {}, pr = s.policiesRoutes || {}, na = s.needsAttention || {};
  const card = (hash, q, a, style = '') => `<a class="am2-card" href="${hash}"><div class="am2-q">${q}</div><div class="am2-a" ${style}>${a}</div></a>`;
  return `<div class="am2-sum">${
    card(am2Hash({tab: ''}), 'Clusters', am2Clusters(s))
  }${card(am2Hash({tab: 'map'}), 'Namespaces in ambient', `<b>${esc(n.inAmbient)} of ${esc(n.total)}</b> · ${esc(n.withWaypoint)} with a waypoint`)
  }${card(am2Hash({tab: 'policies', drift: pr.differ ? '1' : ''}), 'Policies and routes', `<b>${esc(pr.objects)} objects</b> · ${esc(pr.differ)} differ between clusters`)
  }${na.services ? card(am2Hash({tab: ''}), 'Needs attention', `<b>${esc(na.services)} ${na.services === 1 ? 'Service' : 'Services'}</b> fail a check`, 'style="color:var(--am2-bad)"') : card(am2Hash({tab: ''}), 'Needs attention', '<b>No failed checks</b>')}</div>`;
}

function am2AttentionRows(data) {
  const rows = (data.needsAttention || []).filter(x => !S.cluster || x.cluster === S.cluster);
  if (!rows.length) return '';
  const names = (data.summary.clusters.names || []);
  const where = names.length === 2 ? 'both clusters' : names.length === 1 ? 'the cluster' : 'all clusters';
  return `<div class="am2-card am2-mb" id="am2-attention"><div class="am2-chead"><h2>Needs attention</h2><span class="am2-sub">Failed checks across ${where}</span></div><table><tbody>${rows.map(r => {
    const ref = r.ref || {}, path = am2SvcPath(r.cluster, ref.namespace, ref.name), svc = ref.kind === 'Service' || !ref.kind;
    const open = svc ? am2Hash({svc: path, stab: 'checks'}) : am2Hash({obj: [r.cluster, ref.kind, ref.namespace, ref.name].join('/'), otab: 'summary'});
    const fix = svc ? am2Hash({svc: path, stab: 'checks', drawer: 'fix:' + (r.checkId || '')}) : open;
    return `<tr><td style="width:36px">${r.state === 'warn' ? am2Pill('warn', '!') : am2Pill('bad', '✕')}</td><td><b>${esc(ref.namespace)}/${esc(ref.name)}</b> <span class="am2-sub">${esc(r.cluster)}</span></td><td>${am2Mono(r.detail)}</td><td class="am2-right am2-act">${am2Btn('Open checks', `data-am2-go="${esc(open)}"`)} ${am2Btn('Fix…', `data-am2-go="${esc(fix)}"`, 'sm p')}</td></tr>`;
  }).join('')}</tbody></table></div>`;
}

function am2ClusterCell(row, cluster) {
  const c = row.perCluster?.[cluster];
  if (!c || !c.present) return am2Pill('mut', 'missing');
  if (c.noEndpoints) return am2Pill('bad', 'no endpoints');
  if (c.differs) return am2Pill('warn', 'differs');
  return am2Pill('ok', '✓');
}

function am2ChecksCell(row) {
  const f = row.checks?.fail || 0, w = row.checks?.warn || 0;
  return f ? am2Pill('bad', `${f} failed`) : w ? am2Pill('warn', `${w} ${w === 1 ? 'warning' : 'warnings'}`) : am2Pill('ok', 'Pass');
}

const am2Badges = (list, empty) => list?.length ? list.map(b => am2Pill(b.tone || 'mut', b.label)).join(' ') : `<span class="am2-muted">${empty}</span>`;

function am2ServiceRows(data) {
  const A = S.am2, names = data.summary.clusters.names.filter(n => !S.cluster || n === S.cluster), q = A.filter.trim().toLowerCase();
  const rows = (data.services || []).filter(r => (!A.ns || r.ref.namespace === A.ns) && (!q || (r.ref.namespace + '/' + r.ref.name).toLowerCase().includes(q)));
  if (!rows.length) return `<tr><td colspan="${6 + names.length}" class="am2-empty">No Services match.</td></tr>`;
  return rows.map(r => {
    const first = Object.keys(r.perCluster || {}).find(c => r.perCluster[c].present) || names[0], path = am2SvcPath(S.cluster || first, r.ref.namespace, r.ref.name), sel = A.lastSvc === path;
    return `<tr class="am2-click ${sel ? 'am2-sel' : ''}" data-am2-go="${esc(am2Hash({svc: path, stab: 'configuration'}))}"><td><b>${esc(r.ref.namespace)}/${esc(r.ref.name)}</b></td><td>${r.waypoint ? `<span class="am2-mono">${esc(r.waypoint)}</span>` : '<span class="am2-muted">—</span>'}</td><td>${am2Badges(r.access, 'open')}</td><td>${am2Badges(r.routing, '—')}</td><td>${am2Badges(r.resilience, '—')}</td>${names.map(n => `<td>${am2ClusterCell(r, n)}</td>`).join('')}<td>${am2ChecksCell(r)}</td><td class="am2-right am2-act">${am2Btn('Open', '', sel ? 'sm p' : 'sm')}</td></tr>`;
  }).join('');
}

function am2NamespaceChips(data) {
  const A = S.am2, counts = {};
  for (const r of data.services || []) counts[r.ref.namespace] = (counts[r.ref.namespace] || 0) + 1;
  const names = Object.keys(counts).sort((a, b) => counts[b] - counts[a] || a.localeCompare(b));
  if (!A.ns || !counts[A.ns]) A.ns = (data.namespaces || []).filter(n => n.inAmbient && counts[n.name]).sort((a, b) => counts[b.name] - counts[a.name])[0]?.name || names[0] || '';
  const shown = A.moreNs ? names : names.slice(0, 3), rest = names.length - shown.length;
  const chip = n => `<button type="button" class="am2-chip ${A.ns === n ? 'on' : ''}" data-am2-ns="${esc(n)}">${esc(n)}</button>`;
  return shown.map(chip).join('') + (rest > 0 ? `<button type="button" class="am2-chip" data-am2-morens>+ ${rest}</button>` : '');
}

function am2NamespaceFooter(data) {
  const ns = (data.namespaces || []).find(n => n.name === S.am2.ns);
  if (!ns) return '';
  const wp = ns.waypoint ? ` · waypoint <span class="am2-mono">${esc(ns.waypoint)}</span> ${ns.waypointScope === 'services' ? 'for some Services' : 'for all Services'}` : ' · no waypoint';
  return `<div class="am2-foot">Namespace <b>${esc(ns.name)}</b>: ${ns.inAmbient ? 'in ambient' : 'not in ambient'}${wp}<span class="am2-spacer"></span>${am2Btn('Namespace settings', `data-am2-go="${esc(am2Hash({...am2Route(), drawer: 'ns:' + ns.name}))}"`)}</div>`;
}

function am2Overview(data) {
  const names = data.summary.clusters.names.filter(n => !S.cluster || n === S.cluster);
  const chips = am2NamespaceChips(data);
  return `${am2SummaryCards(data)}${am2AttentionRows(data)}<div class="am2-card"><div class="am2-chead"><h2>Services in the mesh</h2><input class="am2-input" id="am2-filter" style="width:240px" placeholder="Filter Services…" value="${esc(S.am2.filter)}" aria-label="Filter Services"><span id="am2-chips" style="display:contents">${chips}</span></div><table><thead><tr><th>Service</th><th>Waypoint</th><th>Access</th><th>Routing</th><th>Resilience</th>${names.map(n => `<th>${esc(n)}</th>`).join('')}<th>Checks</th><th></th></tr></thead><tbody id="am2-rows">${am2ServiceRows(data)}</tbody></table><div id="am2-nsfoot">${am2NamespaceFooter(data)}</div></div>`;
}

// ---------- page dispatch ----------
async function amV2Page() {
  const A = S.am2, route = am2Route();
  S.ambient.tab = {map: 'map', path: 'path', demo: 'demo', changes: 'changes'}[route.tab] || 'investigate';
  let data;
  try { data = await am2Load(); A.error = ''; } catch (e) { A.error = e.message; }
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
  if (!route.svc && !route.obj && route.tab === 'overview' && data) {
    const redraw = () => {
      $('#am2-rows').innerHTML = am2ServiceRows(data);
      $('#am2-chips').innerHTML = am2NamespaceChips(data);
      $('#am2-nsfoot').innerHTML = am2NamespaceFooter(data);
    };
    const f = $('#am2-filter');
    if (f) f.oninput = () => { A.filter = f.value; $('#am2-rows').innerHTML = am2ServiceRows(data); };
    $('#am2-chips')?.addEventListener('click', e => {
      const ns = e.target.closest('[data-am2-ns]'), more = e.target.closest('[data-am2-morens]');
      if (ns) { A.ns = ns.dataset.am2Ns; redraw(); } else if (more) { A.moreNs = true; redraw(); }
    });
  }
  root.onclick = e => {
    const go = e.target.closest('[data-am2-go]');
    if (go && root.contains(go)) { const h = go.dataset.am2Go; if (h.includes('svc=')) { const m = h.match(/svc=([^&]+)/); if (m) A.lastSvc = decodeURIComponent(m[1]); } if (location.hash === h) render(); else location.hash = h; }
  };
}
