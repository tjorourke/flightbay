'use strict';
/* Ambient v2: Policies & routes (mockup 02). One inventory of every ambient policy, route and waypoint,
   with what it applies to, per-cluster state and who manages it. */

S.am2.pol = S.am2.pol || {data: null, key: '', q: '', types: [], show: {problems: false, drift: false, flightbay: false}, ns: [], group: 'object', scopeOpen: false};
const AM2_TYPES = ['Access', 'Routing', 'Resilience', 'Transport', 'Waypoints'];
const AM2_MANAGED = {flightbay: ['tl', 'Flightbay'], helm: ['mut', 'Helm'], argocd: ['mut', 'Argo CD'], flux: ['mut', 'Flux'], kubectl: ['mut', 'kubectl'], unknown: ['mut', 'Unknown']};
const AM2_STATE = {applied: ['ok', 'Applied'], accepted: ['ok', 'Accepted'], programmed: ['ok', 'Programmed'], missing: ['mut', 'Missing'], differs: ['warn', 'Differs'], 'not-enforced': ['warn', 'Not enforced']};

function am2MidEllipsis(name, max = 32) {
  return name.length <= max ? name : name.slice(0, 21) + '…' + name.slice(-8);
}

function am2PolQuery() {
  const P = S.am2.pol, q = {group: P.group};
  if (P.ns.length) q.namespace = P.ns.join(',');
  if (P.types.length) q.type = P.types.join(',');
  if (P.show.problems) q.problems = '1';
  if (P.show.drift) q.drift = '1';
  if (P.show.flightbay) q.managedBy = 'flightbay';
  if (P.q.trim()) q.q = P.q.trim();
  return q;
}

function am2AppliesTo(a) {
  if (!a) return '<span class="am2-muted">—</span>';
  if (a.kind === 'nothing') return `${am2Pill('bad', 'Targets nothing')} <span class="am2-sub">${esc(a.reason || '')}</span>`;
  const m = String(a.phrase || '').match(/^(Service|Namespace|Pods|Whole mesh|Waypoint|Gateway)\s*(.*)$/);
  if (!m) return esc(a.phrase);
  if (m[1] === 'Whole mesh') return `Whole mesh ${m[2] ? `<span class="am2-sub">${esc(m[2])}</span>` : '<span class="am2-sub">(inherited)</span>'}`;
  if (m[1] === 'Pods') return `Pods <span class="am2-mono">${esc(m[2])}</span>`;
  const [name, ...rest] = m[2].split(' ');
  return `${m[1]} <b>${esc(name)}</b>${rest.length ? ' ' + esc(rest.join(' ')) : ''}`;
}

function am2PolRow(r, clusters, route) {
  const eff = ['DENY', 'ALLOW'].includes(r.effect?.action) ? `${am2Pill(r.effect.action === 'DENY' ? 'bad' : 'ok', r.effect.action)} ${esc(r.effect.phrase)}` : esc(r.effect?.phrase || '');
  const cells = clusters.map(c => { const [t, l] = AM2_STATE[r.perCluster?.[c]] || ['mut', r.perCluster?.[c] || '—']; return `<td>${am2Pill(t, l)}</td>`; }).join('');
  const [mt, ml] = AM2_MANAGED[r.managedBy] || AM2_MANAGED.unknown;
  const refStr = [r.ref.cluster, r.ref.kind, r.ref.namespace, r.ref.name].join('/');
  const objHash = ['AuthorizationPolicy', 'HTTPRoute'].includes(r.kind) ? am2Hash({obj: refStr, otab: 'summary'}) : '';
  const differs = Object.values(r.perCluster || {}).includes('differs'), notEnf = Object.values(r.perCluster || {}).includes('not-enforced');
  const btn = (label, attrs) => `<button type="button" class="am2-btn sm" ${attrs}>${label}</button>`;
  const acts = [
    r.canTest ? btn('Test', `data-am2-go="${esc(am2Hash({obj: refStr, otab: 'tests'}))}"`) : '',
    differs ? btn('Diff', `data-am2-diff='${esc(JSON.stringify({ref: r.ref, perCluster: r.perCluster}))}'`) : '',
    notEnf ? btn('Explain', objHash ? `data-am2-go="${esc(am2Hash({obj: refStr, otab: 'explain'}))}"` : `data-am2-open='${esc(JSON.stringify(r.ref))}'`) : '',
    notEnf && !differs ? '' : btn('Open', objHash ? `data-am2-go="${esc(objHash)}"` : `data-am2-open='${esc(JSON.stringify(r.ref))}'`)
  ].filter(Boolean).join(' ');
  return `<tr><td><span class="am2-kind">${esc(r.kind)}</span><div class="am2-t am2-mono" style="margin-top:3px" title="${esc(r.ref.namespace)}/${esc(r.ref.name)}">${esc(am2MidEllipsis(r.ref.name))}</div></td><td>${am2AppliesTo(r.appliesTo)}</td><td>${eff}</td><td>${esc(r.enforcedBy || '—') || '—'}</td>${cells}<td>${am2Pill(mt, ml)}</td><td class="am2-right am2-nowrap">${acts}</td></tr>`;
}

function am2PolTable(d, route) {
  const clusters = d.clusters || [], cols = 7 + clusters.length;
  const head = `<thead><tr><th>Kind / name</th><th>Applies to</th><th>Effect</th><th>Enforced by</th>${clusters.map(c => `<th>${esc(c)}</th>`).join('')}<th>Managed by</th><th></th></tr></thead>`;
  let body;
  if (S.am2.pol.group === 'service' && d.groups) {
    body = d.groups.map(g => `<tr class="am2-grp ${g.svc ? 'am2-click' : ''}" ${g.svc ? `data-am2-go="${esc(am2Hash({svc: am2SvcPath(g.svc.cluster, g.svc.namespace, g.svc.name), stab: 'configuration'}))}"` : ''}><td colspan="${cols}">${esc(g.title)} · ${g.rows.length} ${g.rows.length === 1 ? 'object' : 'objects'}</td></tr>${g.rows.map(r => am2PolRow(r, clusters, route)).join('')}`).join('');
  } else {
    // By object: still grouped by Service so a group header can open the Service.
    const groups = {};
    for (const r of d.rows) { const k = r.appliesTo?.kind === 'mesh' ? 'mesh' : (r.svc && r.kind !== 'Gateway' && r.appliesTo?.kind !== 'namespace') ? 'svc:' + r.svc.namespace + '/' + r.svc.name : 'ns:' + r.ref.namespace; (groups[k] = groups[k] || {rows: [], svc: r.svc, k}).rows.push(r); }
    body = Object.values(groups).sort((a, b) => ({svc: 0, ns: 1, mesh: 2}[a.k.split(':')[0]] - {svc: 0, ns: 1, mesh: 2}[b.k.split(':')[0]])).map(g => {
      const title = g.k === 'mesh' ? 'Whole mesh' : g.k.startsWith('ns:') ? `${g.k.slice(3)} (namespace-wide)` : g.k.slice(4);
      const svc = g.k.startsWith('svc:') ? g.svc : null;
      return `<tr class="am2-grp ${svc ? 'am2-click' : ''}" ${svc ? `data-am2-go="${esc(am2Hash({svc: am2SvcPath(svc.cluster, svc.namespace, svc.name), stab: 'configuration'}))}"` : ''}><td colspan="${cols}">${esc(title)} · ${g.rows.length} ${g.rows.length === 1 ? 'object' : 'objects'}</td></tr>${g.rows.map(r => am2PolRow(r, clusters, route)).join('')}`;
    }).join('');
  }
  return `<table>${head}<tbody>${body || `<tr><td colspan="${cols}" class="am2-empty">Nothing matches these filters.</td></tr>`}</tbody></table>`;
}

function am2PolFilters(d) {
  const P = S.am2.pol, chip = (on, attrs, label) => `<button type="button" class="am2-chip ${on ? 'on' : ''}" ${attrs}>${label}</button>`;
  const scope = P.ns.map(n => chip(true, `data-am2-pscope="${esc(n)}"`, `${esc(n)} ✕`)).join('') + (P.scopeOpen ? (d.counts?.namespaces || []).filter(n => !P.ns.includes(n.name)).map(n => chip(false, `data-am2-pscope="${esc(n.name)}"`, `${esc(n.name)} <span class="am2-muted">${n.count}</span>`)).join('') : (P.ns.length ? '' : chip(false, 'data-am2-pscopeopen', 'All namespaces ▾')));
  return `<div class="am2-card am2-filterbar"><input class="am2-input" id="am2-pq" style="width:260px" placeholder="Filter by name, kind or target" value="${esc(P.q)}" aria-label="Filter"><span class="am2-muted" style="margin-left:6px">Scope</span>${scope}<span class="am2-muted" style="margin-left:6px">Type</span>${AM2_TYPES.map(t => chip(P.types.includes(t), `data-am2-ptype="${t}"`, t)).join('')}<span class="am2-muted" style="margin-left:6px">Show</span>${chip(P.show.problems, 'data-am2-pshow="problems"', 'Only problems')}${chip(P.show.drift, 'data-am2-pshow="drift"', 'Only drift')}${chip(P.show.flightbay, 'data-am2-pshow="flightbay"', 'Managed by Flightbay')}<span class="am2-seg" style="margin-left:auto"><a href="#" data-am2-pgroup="object" class="${P.group === 'object' ? 'on' : ''}">By object</a><a href="#" data-am2-pgroup="service" class="${P.group === 'service' ? 'on' : ''}">By Service</a></span></div>`;
}

async function am2PoliciesPage(route, data) {
  const P = S.am2.pol;
  // Route parameters seed the filters the first time and when they change.
  const sig = [route.ns, route.kind, route.problems, route.drift, route.group].join('|');
  if (P.sig !== sig) {
    P.sig = sig;
    if (route.ns !== undefined) P.ns = route.ns ? route.ns.split(',') : [];
    if (route.problems) P.show.problems = route.problems === '1';
    if (route.drift) P.show.drift = route.drift === '1';
    if (route.group) P.group = route.group === 'service' ? 'service' : 'object';
    if (route.kind) P.types = route.kind.split(',').filter(t => AM2_TYPES.includes(t));
  }
  const d = await api('api/ambient/policies', am2PolQuery());
  P.data = d;
  const clusters = d.clusters || [];
  const total = d.counts?.all ?? d.rows.length;
  const header = `<div style="display:flex;align-items:flex-end;gap:12px;margin-bottom:12px"><div style="flex:1"><h2 style="font-size:16px">All policies and routes</h2><div class="am2-muted">Every ambient policy, route and waypoint in the connected clusters, with what it applies to. ${esc(total)} objects across ${clusters.length} ${clusters.length === 1 ? 'cluster' : 'clusters'}.</div></div><button type="button" class="am2-btn" data-am2-export>Export YAML</button></div>`;
  return `${header}${am2PolFilters(d)}<div class="am2-card" style="overflow:hidden" id="am2-poltable">${am2PolTable(d, route)}</div>`;
}

function am2PoliciesAfter(route) {
  if (route.svc || route.obj || route.tab !== 'policies') return;
  const P = S.am2.pol, root = $('#am2-root');
  if (!root) return;
  let timer;
  const q = $('#am2-pq');
  if (q) q.oninput = () => { P.q = q.value; clearTimeout(timer); timer = setTimeout(async () => { const d = await api('api/ambient/policies', am2PolQuery()); P.data = d; $('#am2-poltable').innerHTML = am2PolTable(d, route); }, 250); };
  if (root.dataset.pol) return;
  root.dataset.pol = '1';
  root.addEventListener('click', async e => {
    const t = e.target, rerender = () => render();
    const sc = t.closest('[data-am2-pscope]'), so = t.closest('[data-am2-pscopeopen]'), ty = t.closest('[data-am2-ptype]'), sh = t.closest('[data-am2-pshow]'), gp = t.closest('[data-am2-pgroup]');
    if (sc) { const n = sc.dataset.am2Pscope; P.ns = P.ns.includes(n) ? P.ns.filter(x => x !== n) : [...P.ns, n]; P.scopeOpen = false; rerender(); }
    else if (so) { P.scopeOpen = !P.scopeOpen; rerender(); }
    else if (ty) { const n = ty.dataset.am2Ptype; P.types = P.types.includes(n) ? P.types.filter(x => x !== n) : [...P.types, n]; rerender(); }
    else if (sh) { const k = sh.dataset.am2Pshow; P.show[k] = !P.show[k]; rerender(); }
    else if (gp) { e.preventDefault(); P.group = gp.dataset.am2Pgroup; rerender(); }
    else if (t.closest('[data-am2-export]')) {
      const rows = P.data?.rows || [], docs = [];
      for (const r of rows) { try { const o = await api('api/object', r.ref); if (o.yaml) docs.push(o.yaml.trim()); } catch (_) { /* skip objects that cannot be read */ } }
      amDownload(docs.join('\n---\n') + '\n', 'ambient-policies-and-routes.yaml');
    } else if (t.closest('[data-am2-diff]')) {
      e.stopPropagation();
      const x = JSON.parse(t.closest('[data-am2-diff]').dataset.am2Diff);
      modal('Differs between clusters', `<div class="am2"><p class="am2-sub" style="margin-bottom:10px">${esc(x.ref.kind)} ${esc(x.ref.name)}: the spec is not the same on every cluster.</p><table><thead><tr><th>Cluster</th><th>State</th></tr></thead><tbody>${Object.entries(x.perCluster).map(([c, s]) => `<tr><td>${esc(c)}</td><td>${am2Pill(...(AM2_STATE[s] || ['mut', s]))}</td></tr>`).join('')}</tbody></table></div>`);
    }
    const open = t.closest('[data-am2-open]');
    if (open) openObject(JSON.parse(open.dataset.am2Open));
  });
}
AM2_AFTER.push(am2PoliciesAfter);
