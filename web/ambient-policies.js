'use strict';
/* Ambient v2: Policies & routes (mockup 02, made dense for real data volume: REVIEW-2026-10-20 R3).
   One flat table of every ambient policy and route in the chosen namespaces: what it applies to, its effect,
   its state on each cluster and who manages it. Waypoints are drawn on the Map, not listed here. */

S.am2.pol = S.am2.pol || {data: null, q: '', types: [], problems: false, compare: false, sort: 'applies', dir: 1, page: 0, typeOpen: false, sig: ''};
const AM2_TYPES = ['Access', 'Routing', 'Resilience', 'Transport', 'Other'];
const AM2_POL_PAGE = 50;
// Per-cluster states (REVIEW R3.7): the status vocabulary plus the neutral "Not here".
const AM2_STATE = {live: ['ok', 'Live'], applied: ['tl', 'Applied'], failed: ['bad', 'Failed'], 'not-enforced': ['warn', 'Not enforced'], differs: ['warn', 'Differs'], 'not-here': ['mut', 'Not here'],
  // older words, still returned by cached responses
  accepted: ['ok', 'Live'], programmed: ['ok', 'Live'], missing: ['mut', 'Not here']};

function am2MidEllipsis(name, max = 32) {
  name = String(name || '');
  if (name.length <= max) return name;
  const tail = Math.min(12, Math.floor((max - 1) / 3));
  return name.slice(0, max - 1 - tail) + '…' + name.slice(-tail);
}
const am2ShortCluster = c => String(c || '').replace(/^kind-/, '');

function am2PolQuery() {
  const P = S.am2.pol, sc = S.am2.scope, q = {group: 'object'};
  if (sc.ns.length) q.namespace = sc.ns.join(',');
  if (P.types.length) q.type = P.types.join(',');
  if (P.problems) q.problems = '1';
  if (P.q.trim()) q.q = P.q.trim();
  return q;
}

function am2AppliesTo(a) {
  if (!a) return '<span class="am2-muted">—</span>';
  if (a.kind === 'nothing') return `<span class="am2-bad-t">Targets nothing</span> <span class="am2-sub">${esc(a.reason || '')}</span>`;
  const m = String(a.phrase || '').match(/^(Service|Namespace|Pods|Whole mesh|Waypoint|Gateway)\s*(.*)$/);
  if (!m) return esc(a.phrase);
  if (m[1] === 'Whole mesh') return `Whole mesh <span class="am2-sub">${esc(m[2] || '(inherited)')}</span>`;
  if (m[1] === 'Pods') return `Pods <span class="am2-mono">${esc(m[2])}</span>`;
  const [name, ...rest] = m[2].split(' ');
  return `${m[1]} <b title="${esc(name)}">${esc(am2MidEllipsis(name, 28))}</b>${rest.length ? ' <span class="am2-sub">' + esc(rest.join(' ')) + '</span>' : ''}`;
}

// One cell for every cluster: a dot per cluster, the word in the title (REVIEW R3.3).
function am2ClustersCell(r, clusters) {
  return `<span class="am2-clcell">${clusters.map(c => {
    const s = r.perCluster?.[c] || 'not-here', [tone, label] = AM2_STATE[s] || ['mut', s];
    return `<span class="am2-cl" title="${esc(c)}: ${esc(label)}${r.perClusterDetail?.[c] ? ' · ' + esc(r.perClusterDetail[c]) : ''}"><i class="am2-dot ${s === 'not-here' ? 'hollow' : tone}"></i>${esc(am2ShortCluster(c))}${s === 'live' || s === 'not-here' || (s === 'not-enforced' && r.appliesTo?.kind === 'nothing') ? '' : ` <span class="am2-st ${tone}">${esc(label)}</span>`}</span>`;
  }).join('')}</span>`;
}

const am2PolRefStr = r => [r.ref.cluster, r.ref.kind, r.ref.namespace, r.ref.name].join('/');

function am2PolRow(r, clusters, compare) {
  const tgt = r.appliesTo?.targets?.[0]?.name, phrase = tgt && r.effect?.phrase === 'Route → ' + tgt ? 'Routes to the same Service' : (r.effect?.phrase || '');
  const eff = ['DENY', 'ALLOW', 'CUSTOM', 'AUDIT'].includes(r.effect?.action) ? `${am2Pill(r.effect.action === 'DENY' ? 'bad' : r.effect.action === 'ALLOW' ? 'ok' : 'info', r.effect.action)} ${esc(phrase)}` : `<span class="${phrase === 'Routes to the same Service' ? 'am2-sub' : ''}">${esc(phrase)}</span>`;
  const states = Object.values(r.perCluster || {}), refStr = am2PolRefStr(r);
  const objHash = ['AuthorizationPolicy', 'HTTPRoute'].includes(r.kind) ? am2Hash({obj: refStr, otab: 'summary'}) : '';
  const btn = (label, attrs) => `<button type="button" class="am2-btn sm" ${attrs}>${label}</button>`;
  const acts = [
    r.canTest ? btn('Test', `data-am2-go="${esc(am2Hash({obj: refStr, otab: 'tests'}))}"`) : '',
    states.includes('differs') ? btn('Diff', `data-am2-diff='${esc(JSON.stringify(r.ref))}'`) : '',
    btn('Open', objHash ? `data-am2-go="${esc(objHash)}"` : `data-am2-open='${esc(JSON.stringify(r.ref))}'`)
  ].filter(Boolean).join(' ');
  const svc = r.svc || (r.appliesTo?.kind === 'service' && r.appliesTo.targets?.[0]);
  const applies = svc ? `<a href="${esc(am2Hash({svc: am2SvcPath(svc.cluster || r.ref.cluster, svc.namespace, svc.name), stab: 'configuration'}))}" class="am2-plain">${am2AppliesTo(r.appliesTo)}</a>` : am2AppliesTo(r.appliesTo);
  const cl = compare ? clusters.map(c => { const s = r.perCluster?.[c] || 'not-here', [t, l] = AM2_STATE[s] || ['mut', s]; return `<td title="${esc(r.perClusterDetail?.[c] || '')}">${am2Pill(t, l)}</td>`; }).join('') : `<td>${am2ClustersCell(r, clusters)}</td>`;
  return `<tr><td class="am2-nowrap"><span class="am2-kindt">${esc(r.kind)}</span> <span class="am2-mono" title="${esc(r.ref.namespace)}/${esc(r.ref.name)}">${esc(am2MidEllipsis(r.ref.name, 40))}</span></td><td>${applies}</td><td>${eff}</td>${cl}<td class="am2-sub am2-nowrap">${esc(r.managedByLabel || r.managedBy || 'Unknown')}</td><td class="am2-right am2-nowrap">${acts}</td></tr>`;
}

// Pure: sort rows. Applies to keeps a Service's objects together.
function am2PolSort(rows, key, dir) {
  const k = {
    name: r => r.ref.name,
    applies: r => r.ref.namespace + '/' + (r.appliesTo?.kind === 'nothing' ? '\uffff' : '') + (r.appliesTo?.phrase || '') + '/' + r.ref.name,
    clusters: r => (r.drift ? '0' : '1') + Object.values(r.perCluster || {}).sort().join(','),
    managed: r => (r.managedByLabel || r.managedBy || '') + r.ref.name
  }[key] || (r => r.ref.name);
  return rows.slice().sort((a, b) => k(a).localeCompare(k(b)) * dir);
}

function am2PolTable(d) {
  const P = S.am2.pol, clusters = d.clusters || [];
  const rows = am2PolSort(d.rows || [], P.sort, P.dir);
  const pages = Math.max(1, Math.ceil(rows.length / AM2_POL_PAGE)); P.page = Math.min(P.page, pages - 1);
  const slice = rows.slice(P.page * AM2_POL_PAGE, (P.page + 1) * AM2_POL_PAGE);
  const th = (key, label) => `<th><a href="#" class="am2-sort ${P.sort === key ? 'on' : ''}" data-am2-psort="${key}">${esc(label)}${P.sort === key ? (P.dir > 0 ? ' ↑' : ' ↓') : ''}</a></th>`;
  const cols = 5 + (P.compare ? clusters.length : 1);
  const head = `<thead><tr>${th('name', 'Name')}${th('applies', 'Applies to')}<th>Effect</th>${P.compare ? clusters.map(c => `<th>${esc(c)}</th>`).join('') : th('clusters', 'Clusters')}${th('managed', 'Managed by')}<th></th></tr></thead>`;
  const body = slice.map(r => am2PolRow(r, clusters, P.compare)).join('') || `<tr><td colspan="${cols}" class="am2-empty">Nothing matches these filters.${am2ScopeAll() ? '' : ' Change the namespaces at the top right to see more.'}</td></tr>`;
  const pager = rows.length > AM2_POL_PAGE ? `<div class="am2-pager"><span class="am2-spacer"></span><span class="am2-sub">${P.page * AM2_POL_PAGE + 1}–${Math.min(rows.length, (P.page + 1) * AM2_POL_PAGE)} of ${rows.length}</span><button type="button" class="am2-btn sm" data-am2-ppage="-1" ${P.page ? '' : 'disabled'}>Prev</button><button type="button" class="am2-btn sm" data-am2-ppage="1" ${P.page < pages - 1 ? '' : 'disabled'}>Next</button></div>` : '';
  return `<table class="am2-dense">${head}<tbody>${body}</tbody></table>${pager}`;
}

function am2PolFilters(d) {
  const P = S.am2.pol, by = d.counts?.byType || {};
  const typeLabel = P.types.length ? P.types.join(', ') : 'All types';
  const typePop = P.typeOpen ? `<div class="am2-menu" id="am2-ptypemenu">${AM2_TYPES.map(t => `<label class="am2-scope-opt"><input type="checkbox" data-am2-ptype="${t}" ${P.types.includes(t) ? 'checked' : ''}><span class="am2-scope-name">${t}</span><span class="am2-sub">${esc(by[t] ?? 0)}</span></label>`).join('')}</div>` : '';
  return `<div class="am2-card am2-filterbar"><input class="am2-input" id="am2-pq" style="width:260px" placeholder="Filter by name or target" value="${esc(P.q)}" aria-label="Filter"><span class="am2-rel"><button type="button" class="am2-scopebtn" data-am2-ptypebtn aria-expanded="${P.typeOpen}">Type: ${esc(typeLabel)} <span aria-hidden="true">▾</span></button>${typePop}</span><label class="am2-check"><input type="checkbox" data-am2-pproblems ${P.problems ? 'checked' : ''}> Problems only${d.counts?.problems ? ` <span class="am2-sub">(${esc(d.counts.problems)})</span>` : ''}</label>${(d.clusters || []).length > 1 ? `<label class="am2-check"><input type="checkbox" data-am2-pcompare ${P.compare ? 'checked' : ''}> A column per cluster</label>` : ''}<span class="am2-spacer"></span><button type="button" class="am2-btn sm0" data-am2-export>Export YAML</button></div>`;
}

async function am2PoliciesPage(route, data) {
  const P = S.am2.pol;
  const sig = [route.kind, route.problems].join('|');
  if (P.sig !== sig) {
    P.sig = sig;
    if (route.problems) P.problems = route.problems === '1';
    if (route.kind) P.types = route.kind.split(',').filter(t => AM2_TYPES.includes(t));
  }
  const d = await api('api/ambient/policies', am2PolQuery());
  P.data = d;
  const clusters = d.clusters || [], shown = (d.rows || []).length, all = d.counts?.all ?? shown;
  const where = am2ScopeAll() ? `across ${clusters.length} ${clusters.length === 1 ? 'cluster' : 'clusters'}` : `in ${S.am2.scope.ns.length === 1 ? S.am2.scope.ns[0] : S.am2.scope.ns.length + ' namespaces'} (${all} in every namespace)`;
  const wp = d.counts?.waypoints ? ` <a href="${am2Hash({tab: 'map', ...am2ScopeParams()})}">${d.counts.waypoints} ${d.counts.waypoints === 1 ? 'waypoint is' : 'waypoints are'} on the Map →</a>` : '';
  const header = `<div style="margin-bottom:12px"><h2 style="font-size:16px">All policies and routes</h2><div class="am2-muted">Every ambient policy and route, with what it applies to. ${esc(shown)} ${shown === 1 ? 'object' : 'objects'} ${esc(where)}.${wp}</div></div>`;
  return `${header}${am2PolFilters(d)}<div class="am2-card" style="overflow:hidden" id="am2-poltable">${am2PolTable(d)}</div>`;
}

function am2PoliciesAfter(route) {
  if (route.svc || route.obj || route.tab !== 'policies') return;
  const P = S.am2.pol, root = $('#am2-root');
  if (!root) return;
  let timer;
  const q = $('#am2-pq');
  if (q) q.oninput = () => { P.q = q.value; P.page = 0; clearTimeout(timer); timer = setTimeout(async () => { const d = await api('api/ambient/policies', am2PolQuery()); P.data = d; $('#am2-poltable').innerHTML = am2PolTable(d); }, 250); };
  if (root.dataset.pol) return;
  root.dataset.pol = '1';
  const redraw = () => { const el = $('#am2-poltable'); if (el && P.data) el.innerHTML = am2PolTable(P.data); };
  root.addEventListener('change', e => {
    const t = e.target;
    if (t.matches('[data-am2-ptype]')) { const n = t.dataset.am2Ptype; P.types = t.checked ? [...P.types, n] : P.types.filter(x => x !== n); P.page = 0; render(); }
    else if (t.matches('[data-am2-pproblems]')) { P.problems = t.checked; P.page = 0; render(); }
    else if (t.matches('[data-am2-pcompare]')) { P.compare = t.checked; redraw(); }
  });
  if (!S.am2.polDocWired) S.am2.polDocWired = document.addEventListener('click', e => { if (P.typeOpen && !e.target.closest('#am2-ptypemenu') && !e.target.closest('[data-am2-ptypebtn]')) { P.typeOpen = false; render(); } }) || true;
  root.addEventListener('click', async e => {
    const t = e.target;
    if (t.closest('[data-am2-ptypebtn]')) { P.typeOpen = !P.typeOpen; render(); return; }
    const so = t.closest('[data-am2-psort]');
    if (so) { e.preventDefault(); const k = so.dataset.am2Psort; P.dir = P.sort === k ? -P.dir : 1; P.sort = k; redraw(); return; }
    const pg = t.closest('[data-am2-ppage]');
    if (pg) { P.page += Number(pg.dataset.am2Ppage); redraw(); return; }
    if (t.closest('[data-am2-export]')) {
      const rows = P.data?.rows || [], docs = [];
      for (const r of rows) { try { const o = await api('api/object', r.ref); if (o.yaml) docs.push(o.yaml.trim()); } catch (_) { /* skip objects that cannot be read */ } }
      amDownload(docs.join('\n---\n') + '\n', 'ambient-policies-and-routes.yaml');
      return;
    }
    const df = t.closest('[data-am2-diff]');
    if (df) {
      e.stopPropagation();
      const ref = JSON.parse(df.dataset.am2Diff);
      try {
        const d = await api('api/ambient/policies/diff', {kind: ref.kind, namespace: ref.namespace, name: ref.name});
        const rows = (d.fields || []).map(f => `<tr><td class="am2-mono">${esc(f.path)}</td>${d.clusters.map(c => `<td class="am2-mono">${f.values?.[c] === undefined ? '<span class="am2-muted">absent</span>' : esc(f.values[c])}</td>`).join('')}</tr>`).join('');
        modal(`${ref.kind} ${ref.name}: differences between clusters`, `<div class="am2"><table class="am2-dense"><thead><tr><th>Field</th>${d.clusters.map(c => `<th>${esc(c)}</th>`).join('')}</tr></thead><tbody>${rows || `<tr><td colspan="${d.clusters.length + 1}" class="am2-empty">The specs are the same once defaults are ignored.</td></tr>`}</tbody></table>${(d.onlyOn || []).length ? `<p class="am2-sub" style="margin-top:10px">Only on ${esc(d.onlyOn.join(', '))}.</p>` : ''}</div>`);
      } catch (err) { toast(err.message); }
      return;
    }
    const open = t.closest('[data-am2-open]');
    if (open) openObject(JSON.parse(open.dataset.am2Open));
  });
}
AM2_AFTER.push(am2PoliciesAfter);
