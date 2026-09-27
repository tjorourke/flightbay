'use strict';
// Flightbay desk UI. Plain JS, no framework, served from the flightbay-desk binary.

const $ = s => document.querySelector(s);
const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'}[c]));
const num = n => Number(n || 0).toLocaleString('en-GB');
const bytes = n => n > 1 << 30 ? (n / (1 << 30)).toFixed(1) + ' GiB' : n > 1 << 20 ? (n / (1 << 20)).toFixed(1) + ' MiB' : n > 1024 ? (n / 1024).toFixed(1) + ' KiB' : n + ' B';
const fmtTime = t => { if (!t || t.startsWith('0001')) return '—'; const d = new Date(t); return d.toISOString().slice(11, 23); };
const fmtDateTime = t => { if (!t || t.startsWith('0001')) return '—'; const d = new Date(t); return d.toISOString().slice(0, 16).replace('T', ' ') + ' UTC'; };
const attr = o => esc(JSON.stringify(o));

const S = {
  view: 'overview', cluster: '', ws: null, state: null, renderSeq: 0,
  res: {group: null, resource: null, ns: '', q: '', nsOptions: []},
  logs: {q: '', namespace: '', pod: '', container: '', level: '', stream: '', from: '', to: '', limit: 200, expanded: null, follow: false, total: 0, rev: 0, result: null},
  compare: null, findingSev: '', term: null,
};

async function api(path, params) {
  const qs = params ? '?' + new URLSearchParams(Object.entries(params).filter(([, v]) => v !== undefined && v !== null)).toString() : '';
  const r = await fetch(path + qs, {credentials: 'same-origin', headers: {Accept: 'application/json'}});
  const body = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(body.error || r.statusText);
  return body;
}

function toast(msg) {
  const t = $('#toast');
  t.textContent = msg; t.classList.remove('hidden');
  clearTimeout(toast.t); toast.t = setTimeout(() => t.classList.add('hidden'), 3500);
}

function stateBadge(st, reason) {
  const label = {complete: 'complete', loading: 'loading', excluded: 'excluded by customer', failed: 'not collected', truncated: 'truncated', 'not-collected': 'not in bundle'}[st] || st;
  return `<span class="badge state-${esc(st)}" title="${esc(reason || '')}"><span class="dot"></span>${esc(label)}</span>`;
}

function statusCell(v) {
  const s = String(v);
  const cls = /CrashLoop|Error|Failed|BackOff|OOM|Evicted|NotReady|False/.test(s) ? 'red' : /Pending|Init|Terminating|Unknown|Container/.test(s) ? 'amber' : /Running|Ready|Active|Completed|True|Succeeded/.test(s) ? 'green' : '';
  return cls ? `<span class="${cls}">${esc(s)}</span>` : esc(s);
}

// ---------- navigation ----------
function parseHash() {
  const h = location.hash.slice(1) || 'overview';
  const [view, query] = h.split('?');
  const p = new URLSearchParams(query || '');
  if (view === 'logs') {
    for (const k of ['q', 'namespace', 'pod', 'container', 'level', 'stream', 'from', 'to']) if (p.has(k)) S.logs[k] = p.get(k);
  }
  if (view === 'resources' && p.has('resource')) { S.res.resource = p.get('resource'); S.res.group = p.get('group') || ''; }
  S.view = view;
}

function nav(view) {
  if (location.hash.slice(1) === view) render(); else location.hash = view;
}

function openLogs(filters) {
  Object.assign(S.logs, {q: '', namespace: '', pod: '', container: '', level: '', stream: '', from: '', to: '', expanded: null}, filters);
  closeDrawer(); closeModal();
  if (S.view === 'logs') render(); else location.hash = 'logs';
}

async function render() {
  const seq = ++S.renderSeq;
  document.querySelectorAll('.nav a').forEach(a => a.classList.toggle('active', a.dataset.view === S.view));
  const views = {overview, resources, logs, timeline, findings, compare, proxies, coverage};
  const fn = views[S.view] || overview;
  $('#crumb').textContent = S.ws ? `Workspace / ${S.ws.case ? 'Case ' + S.ws.case : 'Snapshot'} / ${S.view}` : 'Workspace';
  try {
    const html = await fn(seq);
    if (seq !== S.renderSeq || html === undefined) return;
    $('#main').innerHTML = html;
    after[S.view]?.();
  } catch (e) {
    if (seq === S.renderSeq) $('#main').innerHTML = `<div class="notice bad">${esc(e.message)}</div>`;
  }
}
const after = {};

function head(title, sub, actions = '') {
  return `<div class="page-head"><div><h1>${esc(title)}</h1><p>${sub}</p></div><div class="row wrap">${actions}</div></div>`;
}

// ---------- import status ----------
function renderImportStatus() {
  const st = S.state; if (!st) return;
  const el = $('#import-status');
  const counts = `${num(st.objects)} objects · ${num(st.logLines)} log lines · ${num(st.events)} events · ${bytes(st.bytesReceived)}`;
  let html = '';
  if (st.phase === 'loading' || st.phase === 'waiting') {
    html = `<span class="progress"><i></i></span><span class="blue">Loading bundle</span><span>${counts}</span><span class="muted">Browse and search now; results say when data is still arriving.</span>`;
    $('#phase-dot').className = 'dot blue';
  } else if (st.phase === 'complete') {
    html = `<span class="green">✓ Verified</span><span>${counts}</span>`;
    if (st.deferred?.length) html += `<span class="amber">Deferred: ${esc(st.deferred.join(', '))}</span><button id="load-deferred">Load now</button>`;
    $('#phase-dot').className = 'dot green';
  } else {
    html = `<span class="red">⚠ ${st.phase === 'cancelled' ? 'Import cancelled' : 'Incomplete bundle'}</span><span>${esc(st.error || '')}</span><span class="muted">Data shown is what arrived before the failure and is unverified.</span>`;
    $('#phase-dot').className = 'dot red';
  }
  el.innerHTML = html;
  $('#load-deferred')?.addEventListener('click', async () => {
    const r = await fetch('api/load', {method: 'POST', credentials: 'same-origin'});
    toast(r.ok ? 'Loading deferred categories from the bundle' : 'Could not load deferred data');
  });
}

let refreshTimer = null;
function onState(st) {
  const prev = S.state;
  S.state = st;
  renderImportStatus();
  if (!prev) return;
  const phaseChanged = prev.phase !== st.phase;
  if (S.view === 'logs' && !phaseChanged) { checkNewLines(); return; }
  if (S.drawerOpen || document.querySelector('.modal-backdrop')) return;
  if (st.revision !== prev.revision && (st.phase === 'loading' || phaseChanged)) {
    clearTimeout(refreshTimer);
    refreshTimer = setTimeout(() => { loadWorkspace().then(() => { if (!['compare', 'logs'].includes(S.view) || phaseChanged) render(); }); }, phaseChanged ? 50 : 1200);
  }
}

async function loadWorkspace() {
  const w = await api('api/workspace');
  S.ws = w;
  $('#bundle-case').textContent = w.case ? `Case ${w.case}` : 'Snapshot';
  $('#bundle-sub').textContent = `${(w.contexts || []).length} cluster(s) · ${fmtDateTime(w.snapshot)}`;
  $('#snapshot-time').textContent = `Captured ${fmtDateTime(w.snapshot)}`;
  $('#collector').textContent = w.collector || '';
  const sel = $('#cluster');
  const cur = sel.value;
  sel.innerHTML = '<option value="">All clusters</option>' + (w.clusters || []).map(c => `<option value="${esc(c.name)}">${esc(c.name)}@snapshot</option>`).join('');
  sel.value = cur;
  const nf = $('#nav-findings');
  const n = (w.findings?.critical || 0) + (w.findings?.warning || 0);
  nf.textContent = n; nf.classList.toggle('hidden', !n);
  if (w.terminal) $('#terminal').classList.remove('hidden');
  if (!S.state) { S.state = w.state; renderImportStatus(); }
}

// ---------- overview ----------
function findingRow(f) {
  const icon = {critical: '×', warning: '!', info: 'i'}[f.severity];
  return `<div class="finding-row"><span class="sev ${esc(f.severity)}">${icon}</span><div style="flex:1;min-width:0">
    <button class="link" data-finding="${attr(f)}"><strong>${esc(f.title)}</strong></button>
    <p>${esc(f.detail || '')}</p><div class="row wrap mt" style="margin-top:6px"><span class="tag">${esc(f.cluster)}</span><small>${esc(f.rule)}</small>${f.provisional ? '<small class="blue">provisional</small>' : ''}</div></div></div>`;
}

async function overview() {
  const w = S.ws = await api('api/workspace');
  const clusters = (w.clusters || []).filter(c => !S.cluster || c.name === S.cluster);
  const tot = k => clusters.reduce((a, c) => a + (c[k] || 0), 0);
  const pol = w.policy || {};
  const st = w.state || {};
  return head(w.case ? `Case ${w.case}` : 'Snapshot', `${(w.contexts || []).join(', ')} · captured ${fmtDateTime(w.snapshot)}`,
    `<button id="go-coverage">Coverage &amp; redactions</button><button class="primary" id="go-findings">Investigate findings →</button>`) + `
  <div class="stats">
    <div class="stat"><div class="eyebrow">Clusters</div><div class="value">${clusters.length}<small>${esc(clusters.map(c => c.version).filter(Boolean)[0] || '')}</small></div><p>${tot('nodes')} nodes captured · ${tot('namespaces')} namespaces</p></div>
    <div class="stat"><div class="eyebrow">Objects</div><div class="value">${num(tot('objects'))}</div><p>${num(tot('pods'))} pods · ${num(tot('podsRunning'))} running</p></div>
    <div class="stat"><div class="eyebrow">Log lines</div><div class="value">${num(st.logLines)}</div><p>${st.phase === 'complete' ? 'all streams loaded' : 'still loading'}</p></div>
    <div class="stat"><div class="eyebrow">Findings</div><div class="value amber">${w.findings.critical + w.findings.warning}<small>${w.findings.critical} critical</small></div><p>${w.findings.info} informational</p></div>
  </div>
  <div class="grid2"><div>
    <section class="panel"><div class="panel-head"><h2>Start here</h2><small>${w.state.phase === 'complete' ? 'Prioritised findings' : 'Provisional until the import is verified'}</small></div>
      ${(w.topFindings || []).filter(f => !S.cluster || f.cluster === S.cluster).map(findingRow).join('') || '<div class="empty"><strong>No warnings or critical findings</strong>Browse resources or search the logs.</div>'}
      <div class="panel-body row between"><small>Rules run against the captured snapshot</small><button class="link" id="all-findings">All findings →</button></div></section>
    ${(w.unavailable || []).length ? `<div class="notice mt"><span>△</span><div><strong>Not collected in this bundle</strong><br>${w.unavailable.map(esc).join('<br>')}</div></div>` : ''}
  </div><div>
    <section class="panel"><div class="panel-head"><h2>Clusters</h2><span class="badge">snapshot contexts</span></div>
      ${clusters.map(c => `<div class="cluster-card"><div class="row between"><button class="link mono" data-cluster="${esc(c.name)}">${esc(c.name)} ↗</button>${c.findings ? `<span class="badge amber">${c.findings} findings</span>` : '<span class="badge green">no warnings</span>'}</div>
        <p>${esc(c.version || 'version unknown')} · ${c.pods} pods · ${c.objects} objects · ${c.namespaces} namespaces</p>${c.gaps ? `<p class="amber">${c.gaps} collection gaps or truncations</p>` : ''}</div>`).join('')}
    </section>
    <section class="panel mt"><div class="panel-head"><h2>Bundle provenance</h2>${st.verified ? '<span class="badge green">verified</span>' : '<span class="badge amber">not yet verified</span>'}</div><div class="panel-body">
      <dl class="metadata"><dt>Collector</dt><dd class="mono">${esc(w.collector || '—')}</dd><dt>Capture</dt><dd>${fmtDateTime(w.captureStart)} → ${fmtDateTime(w.captureEnd)}</dd>
      <dt>Profile</dt><dd>${esc(pol.profile || '—')}</dd><dt>Namespaces</dt><dd>${esc((pol.namespaces || ['all']).join(', '))}${pol.excludeNamespaces?.length ? ' · excluding ' + esc(pol.excludeNamespaces.join(', ')) : ''}</dd>
      <dt>Excluded resources</dt><dd>${esc((pol.excludeResources || []).join(', ') || 'none')}</dd><dt>Secrets</dt><dd>${esc(pol.secrets || '—')} (values never collected)</dd>
      <dt>ConfigMaps</dt><dd>${esc(pol.configMaps || '—')}</dd><dt>Log window</dt><dd>${esc(pol.logs?.since || '—')}, ${esc((pol.logs?.streams || []).join(', '))}</dd>
      <dt>Redactions</dt><dd><button class="link" id="go-coverage2">${num(w.redactionTotal)} replacements ↗</button></dd></dl></div></section>
  </div></div>`;
}
after.overview = () => {
  $('#go-findings').onclick = $('#all-findings').onclick = () => nav('findings');
  $('#go-coverage').onclick = $('#go-coverage2').onclick = () => nav('coverage');
  document.querySelectorAll('[data-cluster]').forEach(b => b.onclick = () => { setCluster(b.dataset.cluster); nav('resources'); });
  wireFindings();
};

function wireFindings() {
  document.querySelectorAll('[data-finding]').forEach(b => b.onclick = () => openFinding(JSON.parse(b.dataset.finding)));
}

// ---------- resources ----------
async function resources() {
  const types = await api('api/resources', {cluster: S.cluster});
  if (!types.length) return head('Resources', 'Captured objects') + `<div class="panel empty"><strong>No resource types yet</strong>${S.state?.phase === 'loading' ? 'The bundle is still loading; types appear as their first chunk arrives.' : 'This bundle holds no objects for the selected cluster.'}</div>`;
  if (S.res.resource === null || !types.find(t => t.resource === S.res.resource && t.group === S.res.group)) {
    const def = types.find(t => t.resource === 'pods' && t.group === '') || types[0];
    S.res.resource = def.resource; S.res.group = def.group;
  }
  const cur = types.find(t => t.resource === S.res.resource && t.group === S.res.group);
  const list = await api('api/objects', {cluster: S.cluster, group: S.res.group, resource: S.res.resource, namespace: S.res.ns, q: S.res.q});
  if (!S.res.ns && !S.res.q) S.res.nsOptions = [...new Set(list.rows.map(r => r.namespace).filter(Boolean))].sort();
  const multi = !S.cluster;
  const stateNote = cur.state === 'complete' ? '' : `<div class="notice ${cur.state === 'failed' ? 'bad' : 'info'}" style="margin-bottom:12px">${stateBadge(cur.state)} <span>${esc({
    loading: 'This collection is still loading. The rows below are what has arrived so far.',
    excluded: 'The customer excluded this resource type. Its absence here says nothing about the cluster.',
    failed: 'Collection failed: ' + (cur.reason || ''),
    truncated: 'This list was truncated: ' + (cur.reason || ''),
    'not-collected': 'No collection record for this type in the bundle.',
  }[cur.state] || '')}</span></div>`;
  return head('Resources', 'Browse captured objects. Click a row for YAML, relationships, events and logs.', `<button id="res-term">&gt;_ kubectl get ${esc(cur.resource)}</button>`) + `
  <div class="resource-layout"><nav class="types" aria-label="Resource types">${types.map(t => `<button class="${t === cur ? 'active' : ''}" data-type="${attr({g: t.group, r: t.resource})}" title="${esc(t.name)} · ${esc(t.state)}"><span>${esc(t.kind || t.resource)}</span><span><small>${t.count}</small> <span class="st state-${esc(t.state)}">●</span></span></button>`).join('')}</nav>
  <div><div class="toolbar"><select id="res-ns" aria-label="Namespace"><option value="">All namespaces</option>${S.res.nsOptions.map(n => `<option ${n === S.res.ns ? 'selected' : ''}>${esc(n)}</option>`).join('')}</select>
    <input id="res-q" placeholder="Filter by name, status or label (press /)" value="${esc(S.res.q)}" aria-label="Filter"><button id="res-reset">Reset</button></div>
    ${stateNote}
    <section class="panel"><div class="panel-head"><div class="row"><h2>${esc(cur.kind || cur.resource)}</h2><span class="badge">${list.rows.length}</span></div><span class="mono muted" style="font-size:10px">${esc(cur.group ? cur.group + '/' + cur.version : cur.version)} · ${esc(cur.name)}</span></div>
    <div class="table-scroll" style="max-height:62vh">${list.rows.length ? `<table><thead><tr>${multi ? '<th>Cluster</th>' : ''}${cur.namespaced ? '<th>Namespace</th>' : ''}${list.columns.map(c => `<th>${esc(c)}</th>`).join('')}</tr></thead><tbody>
      ${list.rows.map(r => `<tr data-obj="${attr({cluster: r.cluster, group: r.group, resource: r.resource, namespace: r.namespace, name: r.name})}">${multi ? `<td><span class="tag">${esc(r.cluster)}</span></td>` : ''}${cur.namespaced ? `<td class="muted">${esc(r.namespace)}</td>` : ''}${r.cells.map((c, i) => `<td class="${i === 0 ? 'mono blue' : ''}">${list.columns[i] === 'Status' ? statusCell(c) : esc(c)}</td>`).join('')}</tr>`).join('')}
    </tbody></table>` : `<div class="empty"><strong>No captured objects match</strong>${cur.state === 'loading' ? 'More may still be loading.' : 'Try another namespace or filter.'}</div>`}</div>
    <div class="table-foot"><span>Captured ${fmtDateTime(S.ws?.snapshot)} · read-only</span><span><kbd>/</kbd> filter · <kbd>⌘K</kbd> jump</span></div></section></div></div>`;
}
after.resources = () => {
  document.querySelectorAll('[data-type]').forEach(b => b.onclick = () => { const t = JSON.parse(b.dataset.type); S.res.group = t.g; S.res.resource = t.r; S.res.ns = ''; S.res.q = ''; render(); });
  document.querySelectorAll('[data-obj]').forEach(tr => tr.onclick = () => openObject(JSON.parse(tr.dataset.obj)));
  $('#res-ns').onchange = e => { S.res.ns = e.target.value; render(); };
  let t; $('#res-q').oninput = e => { clearTimeout(t); t = setTimeout(() => { S.res.q = e.target.value; render().then(() => { const i = $('#res-q'); if (i) { i.focus(); i.setSelectionRange(i.value.length, i.value.length); } }); }, 250); };
  $('#res-reset').onclick = () => { S.res.ns = ''; S.res.q = ''; render(); };
  $('#res-term').onclick = () => termRun(`kubectl get ${S.res.resource}${S.res.group ? '.' + S.res.group : ''} ${S.res.ns ? '-n ' + S.res.ns : '-A'}`);
};

// ---------- object drawer ----------
async function openObject(ref, tab = 'summary') {
  S.drawerOpen = true;
  const d = await api('api/object', ref).catch(e => ({error: e.message}));
  if (d.error) { toast(d.error); S.drawerOpen = false; return; }
  const o = d.object;
  const isPod = o.kind === 'Pod';
  const draw = () => {
    const body = {
      summary: () => {
        const md = o.object.metadata || {}; const st = o.object.status || {};
        const cs = (st.containerStatuses || []).map(c => { const w = c.state?.waiting?.reason; const t = c.lastState?.terminated; return `<tr><td class="mono">${esc(c.name)}</td><td>${c.ready ? '<span class="green">ready</span>' : '<span class="amber">not ready</span>'}</td><td>${statusCell(w || Object.keys(c.state || {})[0] || '')}</td><td>${esc(c.restartCount ?? 0)}</td><td class="muted">${t ? esc(`${t.reason || ''} exit ${t.exitCode ?? ''} at ${fmtTime(t.finishedAt)}`) : ''}</td></tr>`; }).join('');
        return `<dl class="metadata"><dt>Kind</dt><dd>${esc(o.kind)} <span class="muted">${esc(o.group ? o.group + '/' + o.version : o.version)}</span></dd><dt>Namespace</dt><dd>${esc(o.namespace || '—')}</dd><dt>Cluster</dt><dd>${esc(o.cluster)}@snapshot</dd>
          <dt>Created</dt><dd>${fmtDateTime(md.creationTimestamp)}</dd><dt>Labels</dt><dd>${Object.entries(md.labels || {}).map(([k, v]) => `<span class="tag">${esc(k)}=${esc(v)}</span>`).join(' ') || '—'}</dd>
          ${o.object.spec?.nodeName ? `<dt>Node</dt><dd>${esc(o.object.spec.nodeName)}</dd>` : ''}${st.phase ? `<dt>Phase</dt><dd>${statusCell(st.phase)}</dd>` : ''}</dl>
          ${cs ? `<h3 class="mt">Containers</h3><div class="table-scroll"><table><thead><tr><th>Name</th><th>Ready</th><th>State</th><th>Restarts</th><th>Last termination</th></tr></thead><tbody>${cs}</tbody></table></div>` : ''}
          ${o.object['x-flightbay'] ? '<div class="notice info mt">Secret values were never collected. Only metadata' + (o.object['x-flightbay'].keys ? ', key names and byte lengths are' : ' is') + ' shown.</div>' : ''}
          <div class="row wrap mt">${isPod ? `<button class="primary" id="d-logs">Open pod logs →</button>` : ''}<button id="d-describe">&gt;_ kubectl describe</button><button id="d-compare">Compare across clusters</button></div>`;
      },
      yaml: () => `<div class="row between" style="margin-bottom:10px"><small>Captured object (redacted)</small><button id="d-copy">Copy YAML</button></div><pre class="code">${esc(d.yaml)}</pre>`,
      json: () => `<pre class="code">${esc(d.json)}</pre>`,
      related: () => d.related.length ? d.related.map(r => `<div class="related"><span class="rel">${esc(r.relation)}</span><span class="tag">${esc(r.kind)}</span>${r.relation.includes('not captured') ? `<span class="red">${esc(r.namespace)}/${esc(r.name)}</span>` : `<button class="link" data-rel="${attr({cluster: r.cluster, group: r.group, resource: r.resource, namespace: r.namespace, name: r.name})}">${esc(r.namespace ? r.namespace + '/' : '')}${esc(r.name)}</button>`}</div>`).join('') : '<div class="empty">No related objects captured.</div>',
      events: () => (d.events || []).length ? d.events.map(e => `<div class="notice ${e.type === 'Warning' ? '' : 'info'}" style="margin-bottom:8px"><div><strong>${esc(e.type)} · ${esc(e.reason)}</strong> <small>${fmtDateTime(e.lastTimestamp || e.eventTime)}${e.count ? ' · ×' + esc(e.count) : ''}</small><br>${esc(e.message)}</div></div>`).join('') : '<div class="empty">No events captured for this object.</div>',
      logs: () => (d.streams || []).length ? d.streams.map(s => { const [c, st] = s.split('/'); return `<div class="related"><span class="rel">${esc(st)}</span><span class="tag">${esc(c)}</span><button class="link" data-stream="${attr({c, st})}">Open in log explorer →</button></div>`; }).join('') : '<div class="empty">No log streams captured for this pod.</div>',
    };
    const tabs = ['summary', 'yaml', 'json', 'related', 'events'].concat(isPod ? ['logs'] : []);
    $('#overlay').innerHTML = `<div class="drawer-backdrop"></div><aside class="drawer" role="dialog" aria-modal="true" aria-label="Object details">
      <div class="drawer-title"><div class="row between"><div class="row wrap"><span class="badge">${esc(o.kind)}</span><span class="tag">${esc(o.cluster)}</span><span class="muted">${esc(o.namespace)}</span>${d.collection.state !== 'complete' ? stateBadge(d.collection.state, d.collection.reason) : ''}</div><button class="ghost" id="d-close" aria-label="Close">✕</button></div><h2>${esc(o.name)}</h2></div>
      <div class="tabs">${tabs.map(t => `<button class="${t === tab ? 'active' : ''}" data-tab="${t}">${t === 'yaml' || t === 'json' ? t.toUpperCase() : t[0].toUpperCase() + t.slice(1)}${t === 'related' ? ` (${d.related.length})` : t === 'events' ? ` (${(d.events || []).length})` : ''}</button>`).join('')}</div>
      <div class="drawer-body">${body[tab]()}</div></aside>`;
    $('.drawer-backdrop').onclick = closeDrawer; $('#d-close').onclick = closeDrawer; $('#d-close').focus();
    document.querySelectorAll('[data-tab]').forEach(b => b.onclick = () => { tab = b.dataset.tab; draw(); });
    document.querySelectorAll('[data-rel]').forEach(b => b.onclick = () => openObject(JSON.parse(b.dataset.rel)));
    document.querySelectorAll('[data-stream]').forEach(b => b.onclick = () => { const s = JSON.parse(b.dataset.stream); openLogs({pod: o.name, namespace: o.namespace, container: s.c, stream: s.st}); });
    const dl = $('#d-logs'); if (dl) dl.onclick = () => { const prev = (d.streams || []).find(s => s.endsWith('/previous')); openLogs({pod: o.name, namespace: o.namespace, stream: prev ? 'previous' : ''}); };
    const dd = $('#d-describe'); if (dd) dd.onclick = () => { closeDrawer(); termRun(`kubectl --context ${o.cluster}@snapshot describe ${o.resource}${o.group ? '.' + o.group : ''} ${o.name}${o.namespace ? ' -n ' + o.namespace : ''}`); };
    const dc = $('#d-compare'); if (dc) dc.onclick = () => { S.compare = {group: o.group, resource: o.resource, namespace: o.namespace, name: o.name, kind: o.kind}; closeDrawer(); nav('compare'); };
    const cp = $('#d-copy'); if (cp) cp.onclick = () => navigator.clipboard.writeText(d.yaml).then(() => toast('YAML copied'), () => toast('Clipboard unavailable'));
  };
  draw();
}
function closeDrawer() { $('#overlay').innerHTML = ''; S.drawerOpen = false; }

// ---------- logs ----------
function logParams(extra = {}) {
  const L = S.logs;
  return Object.assign({q: L.q, cluster: S.cluster, namespace: L.namespace, pod: L.pod, container: L.container, level: L.level, stream: L.stream, from: L.from, to: L.to, limit: L.limit}, extra);
}

async function logs() {
  const L = S.logs;
  let res;
  try { res = await api('api/logs/search', logParams()); } catch (e) { res = {error: e.message, records: [], facets: {}, histogram: [], total: 0}; }
  L.result = res; L.total = res.total; L.rev = res.revision;
  const facet = (key, field, label) => {
    const vals = Object.entries(res.facets?.[key] || {}).sort((a, b) => b[1] - a[1]).slice(0, 12);
    if (!vals.length) return '';
    return `<h3>${label}</h3>` + vals.map(([v, n]) => `<button class="facet ${L[field] === v ? 'active' : ''}" data-facet="${attr({field, v})}"><span></span><span>${esc(v || '—')}</span><small>${num(n)}</small></button>`).join('');
  };
  const chips = ['namespace', 'pod', 'container', 'level', 'stream'].filter(k => L[k]).map(k => `<button class="chip" data-clear="${k}">${k}: ${esc(L[k])} ×</button>`).join('') +
    (L.from ? `<button class="chip" data-clear="time">${fmtTime(L.from)}–${fmtTime(L.to)} ×</button>` : '');
  const max = Math.max(1, ...(res.histogram || []).map(b => b.count));
  const hist = (res.histogram || []).map((b, i) => { const sel = L.from && b.start >= L.from && b.start < L.to; return `<div data-bucket="${i}" class="${sel ? 'sel' : ''}" style="height:${Math.max(3, b.count / max * 76)}px" title="${fmtTime(b.start)} · ${b.count} lines"></div>`; }).join('');
  const rows = res.records.map(r => `<div class="log-row ${r.level === 'error' ? 'error' : r.level === 'warn' ? 'warn' : ''}" data-log="${r.id}" tabindex="0">
      <span class="muted">${fmtTime(r.ts)}</span><span class="${r.level === 'error' ? 'red' : r.level === 'warn' ? 'amber' : 'green'}">${esc(r.level.toUpperCase())}</span>
      <span class="pod" title="${esc(r.cluster)} ${esc(r.namespace)}/${esc(r.pod)}">${esc(r.pod)}<br><span class="muted">${esc(r.container)}${r.stream === 'previous' ? ' · previous' : ''}</span></span>
      <span class="msg">${esc(r.msg)}${Object.entries(r.fields || {}).filter(([k]) => /trace|request_id|connection/.test(k)).map(([k, v]) => ` <span class="blue">${esc(k)}=${esc(v)}</span>`).join('')}</span></div>${L.expanded === r.id ? '<div id="log-detail"></div>' : ''}`).join('');
  return head('Log explorer', 'Search every captured log line, then follow the evidence across pods.', `<label class="row"><input type="checkbox" id="follow" ${L.follow ? 'checked' : ''}> Follow incoming data</label><button id="log-term">&gt;_ fbq in terminal</button>`) + `
  <div class="log-layout"><aside class="facets" aria-label="Facets">${facet('level', 'level', 'Level')}${facet('namespace', 'namespace', 'Namespace')}${facet('pod', 'pod', 'Pod')}${facet('container', 'container', 'Container')}${facet('stream', 'stream', 'Stream')}${!S.cluster ? facet('cluster', 'cluster_', 'Cluster') : ''}
    <h3>Syntax</h3><div class="muted mono" style="font-size:10px;line-height:1.8">level:error<br>ns:payments pod:checkout*<br>trace_id:a4e98f02<br>"connection reset"<br>-healthz · a OR b<br>since:15m</div></aside>
  <section><form class="query" id="log-form"><span class="green">⌕</span><input id="log-q" value="${esc(L.q)}" placeholder='Search, or try level:error ns:payments "timed out"' autocomplete="off" spellcheck="false" aria-label="Log query"><button class="primary">Search ↵</button></form>
    <div class="help-row"><span>Try</span>${['level:error', 'level:warn OR level:error', '"connection"', 'stream:previous'].map(x => `<button data-try="${esc(x)}">${esc(x)}</button>`).join('')}</div>
    ${res.error ? `<div class="notice bad">${esc(res.error)}</div>` : ''}
    <div class="chips">${chips}</div>
    <div id="new-lines"></div>
    <div class="histogram"><div class="row between"><small>Log volume${res.from && !res.from.startsWith('0001') ? ` · ${fmtDateTime(res.from)} – ${fmtTime(res.to)}` : ''}</small><small>Drag across bars to pick a time range</small></div><div class="bars" id="bars">${hist}</div></div>
    <div class="panel"><div class="panel-head"><span><strong>${num(res.total)}</strong> <span class="muted">matching lines · ${num(res.scanned)} searched${res.loading ? ' · <span class="blue">bundle still loading</span>' : ''}</span></span><small>Newest first · click a line for context</small></div>
    <div class="table-scroll" style="max-height:60vh"><div class="log-table"><div class="log-row log-head"><span>TIME (UTC)</span><span>LEVEL</span><span>POD / CONTAINER</span><span>MESSAGE</span></div>
    ${rows || `<div class="empty"><strong>${esc(res.message || 'No matches')}</strong>${res.loading ? 'Log batches are still arriving; results update as they load.' : 'Remove a filter or broaden the search.'}</div>`}</div></div>
    ${res.total > res.records.length ? `<div class="table-foot"><span>Showing ${res.records.length} of ${num(res.total)}</span><button id="more">Show more</button></div>` : ''}</div>
  </section></div>`;
}
after.logs = () => {
  const L = S.logs;
  $('#log-form').onsubmit = e => { e.preventDefault(); L.q = $('#log-q').value; L.expanded = null; L.limit = 200; render(); };
  document.querySelectorAll('[data-try]').forEach(b => b.onclick = () => { L.q = b.dataset.try; render(); });
  document.querySelectorAll('[data-facet]').forEach(b => b.onclick = () => { const f = JSON.parse(b.dataset.facet); if (f.field === 'cluster_') { setCluster(f.v); return; } L[f.field] = L[f.field] === f.v ? '' : f.v; render(); });
  document.querySelectorAll('[data-clear]').forEach(b => b.onclick = () => { const k = b.dataset.clear; if (k === 'time') { L.from = L.to = ''; } else L[k] = ''; render(); });
  document.querySelectorAll('[data-log]').forEach(el => { const go = () => expandLog(Number(el.dataset.log)); el.onclick = go; el.onkeydown = e => { if (e.key === 'Enter') go(); }; });
  $('#follow').onchange = e => { L.follow = e.target.checked; if (L.follow) render(); };
  $('#log-term').onclick = () => termRun(`fbq '${(L.q || 'level:error').replace(/'/g, '')}'`);
  const more = $('#more'); if (more) more.onclick = () => { L.limit += 500; render(); };
  const bars = $('#bars'); let start = null;
  const hist = L.result.histogram || [];
  bars.onmousedown = e => { const b = e.target.dataset.bucket; if (b !== undefined) { start = +b; e.preventDefault(); } };
  bars.onmouseup = e => {
    const b = e.target.dataset.bucket; if (start === null || b === undefined) return;
    const lo = Math.min(start, +b), hi = Math.max(start, +b); start = null;
    L.from = hist[lo].start; L.to = hi + 1 < hist.length ? hist[hi + 1].start : L.result.to; render();
  };
  if (L.expanded) showLogDetail(L.expanded);
};

async function checkNewLines() {
  const L = S.logs;
  if (L.follow) { render(); return; }
  try {
    const r = await api('api/logs/search', logParams({limit: 1}));
    const extra = r.total - L.total;
    const el = $('#new-lines'); if (!el) return;
    el.innerHTML = extra > 0 ? `<div class="new-lines"><span>${num(extra)} new matching line${extra === 1 ? '' : 's'} loaded since this search</span><button id="show-new">Show</button></div>` : '';
    const b = $('#show-new'); if (b) b.onclick = () => render();
  } catch (_) { /* query errors are shown on the next render */ }
}

async function expandLog(id) { S.logs.expanded = S.logs.expanded === id ? null : id; await render(); }

async function showLogDetail(id) {
  const ctx = await api('api/logs/context', {id, n: 3});
  const el = $('#log-detail'); if (!el) return;
  const r = ctx.find(x => x.id === id) || ctx[0]; if (!r) return;
  const pivots = Object.entries(r.fields || {}).filter(([k]) => /trace|request|connection|span|correlation/i.test(k));
  el.innerHTML = `<div class="line-detail"><div class="row between"><strong>Context in ${esc(r.namespace)}/${esc(r.pod)} · ${esc(r.container)} (${esc(r.stream)})</strong><span class="tag">${esc(r.cluster)}</span></div>
    <pre>${ctx.map(x => `${x.id === id ? '›' : ' '} ${fmtTime(x.ts)} ${esc(x.raw)}`).join('\n')}</pre>
    <div class="row wrap"><button id="lc-pod">Inspect pod →</button><button id="lc-stream">All lines from this stream</button>${pivots.map(([k, v]) => `<button class="active" data-pivot="${attr({k, v})}">Follow ${esc(k)}=${esc(v)} across pods →</button>`).join('')}</div>
    ${Object.keys(r.fields || {}).length ? `<pre>${esc(JSON.stringify(r.fields, null, 2))}</pre>` : ''}</div>`;
  $('#lc-pod').onclick = () => openObject({cluster: r.cluster, group: '', resource: 'pods', namespace: r.namespace, name: r.pod});
  $('#lc-stream').onclick = () => openLogs({namespace: r.namespace, pod: r.pod, container: r.container, stream: r.stream});
  document.querySelectorAll('[data-pivot]').forEach(b => b.onclick = () => { const p = JSON.parse(b.dataset.pivot); openLogs({q: `${p.k}:${p.v}`}); toast(`Following ${p.k} across every pod`); });
}

// ---------- timeline ----------
async function timeline() {
  const items = await api('api/timeline', {cluster: S.cluster});
  return head('Timeline', 'Events, container exits, rollouts and error spikes on one UTC time axis.') + (items.length ? `<section class="panel">${items.map((i, n) => `
    <div class="timeline-item"><time>${fmtDateTime(i.time).slice(0, 16)}:${new Date(i.time).toISOString().slice(17, 19)}</time><span class="node ${esc(i.severity)}"></span>
    <div><h3>${esc(i.title)}</h3><p>${esc(i.detail || '')}</p><div class="row" style="margin-top:4px"><span class="tag">${esc(i.cluster)}</span><small>${esc(i.kind)}</small></div></div>
    <div class="row">${i.object ? `<button data-tobj="${n}">Inspect →</button>` : ''}${i.logQuery ? `<button data-tlog="${n}">Logs →</button>` : ''}</div></div>`).join('')}</section>` :
    `<div class="panel empty"><strong>Nothing on the timeline yet</strong>${S.state?.phase === 'loading' ? 'Events and logs are still loading.' : 'No events, exits or rollouts were captured.'}</div>`);
}
after.timeline = async () => {
  const items = await api('api/timeline', {cluster: S.cluster});
  document.querySelectorAll('[data-tobj]').forEach(b => b.onclick = () => openObject(items[+b.dataset.tobj].object));
  document.querySelectorAll('[data-tlog]').forEach(b => b.onclick = () => { const i = items[+b.dataset.tlog]; openLogs({q: i.logQuery, from: i.from && !i.from.startsWith('0001') ? i.from : '', to: i.to && !i.to.startsWith('0001') ? i.to : ''}); });
};

// ---------- findings ----------
async function findings() {
  const all = await api('api/findings', {cluster: S.cluster});
  const fs = all.filter(f => !S.findingSev || f.severity === S.findingSev);
  const prov = all.some(f => f.provisional);
  return head('Findings', 'Signals to investigate, each linked to its evidence. They are not a root-cause verdict.', `<select id="sev"><option value="">All severities</option>${['critical', 'warning', 'info'].map(s => `<option ${S.findingSev === s ? 'selected' : ''}>${s}</option>`).join('')}</select>`) +
    (prov ? '<div class="notice info" style="margin-bottom:14px">These findings are provisional: the bundle has not finished loading or failed verification. Rules that depend on something being absent wait for complete coverage.</div>' : '') +
    `<section class="panel"><div class="panel-head"><h2>${fs.length} findings</h2><small>Deterministic rules</small></div>${fs.map(findingRow).join('') || '<div class="empty"><strong>No findings in this selection</strong></div>'}</section>`;
}
after.findings = () => { $('#sev').onchange = e => { S.findingSev = e.target.value; render(); }; wireFindings(); };

function openFinding(f) {
  modal(f.title, `<div class="row wrap"><span class="badge ${f.severity === 'critical' ? 'red' : f.severity === 'warning' ? 'amber' : 'blue'}">${esc(f.severity)}</span><span class="tag">${esc(f.rule)}</span><span class="tag">${esc(f.cluster)}</span>${f.provisional ? '<span class="badge blue">provisional</span>' : ''}</div>
    <p class="mt">${esc(f.detail || '')}</p>${(f.evidence || []).length ? `<h3 class="mt">Evidence</h3><pre class="code mt">${esc(f.evidence.join('\n'))}</pre>` : ''}
    ${f.nextStep ? `<h3 class="mt">Next step</h3><p class="mt muted">${esc(f.nextStep)}</p>` : ''}
    <div class="row wrap mt">${f.object ? '<button class="primary" id="f-obj">Inspect object →</button>' : ''}${f.logQuery ? '<button id="f-logs">Open the logs →</button>' : ''}</div>`);
  const o = $('#f-obj'); if (o) o.onclick = () => { closeModal(); openObject(f.object); };
  const l = $('#f-logs'); if (l) l.onclick = () => openLogs({q: f.logQuery});
}

// ---------- compare ----------
function lineDiff(a, b) {
  if (a.length * b.length > 4e6) return {a: a.map(t => ({t})), b: b.map(t => ({t}))};
  const n = a.length, m = b.length, dp = Array.from({length: n + 1}, () => new Uint16Array(m + 1));
  for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) dp[i][j] = a[i] === b[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
  const A = [], B = []; let i = 0, j = 0;
  while (i < n && j < m) { if (a[i] === b[j]) { A.push({t: a[i++]}); B.push({t: b[j++]}); } else if (dp[i + 1][j] >= dp[i][j + 1]) A.push({t: a[i++], c: 'del'}); else B.push({t: b[j++], c: 'add'}); }
  while (i < n) A.push({t: a[i++], c: 'del'}); while (j < m) B.push({t: b[j++], c: 'add'});
  return {a: A, b: B};
}

function baselineSection(b) {
  if (!b?.available) return '';
  const r = b.report, sm = r.summary || {};
  const sel = S.baseSel;
  const ch = (r.changes || []).filter(x => !S.baseKind || x.kind === S.baseKind);
  const kinds = [...new Set((r.changes || []).map(x => x.kind))].sort();
  let detail = '';
  if (sel != null && ch[sel]) {
    const x = ch[sel];
    if (x.change === 'changed') {
      const df = lineDiff((x.before || '').split('\n'), (x.after || '').split('\n'));
      detail = `<div class="diff-grid mt"><div class="diff-col"><div class="panel-head"><h2>Before</h2></div><div class="diff-code">${df.a.map(l => `<span class="${l.c || ''}">${esc(l.t) || ' '}</span>`).join('')}</div></div>
        <div class="diff-col"><div class="panel-head"><h2>After</h2></div><div class="diff-code">${df.b.map(l => `<span class="${l.c || ''}">${esc(l.t) || ' '}</span>`).join('')}</div></div></div>`;
    } else detail = `<pre class="code mt">${esc(x.after || x.before)}</pre>`;
  }
  return `<section class="panel" style="margin-bottom:16px"><div class="panel-head"><h2>Against the earlier bundle</h2><small class="mono">${esc(r.before.case || r.before.bundleId)} ${fmtDateTime(r.before.captured)} → ${esc(r.after.case || r.after.bundleId)} ${fmtDateTime(r.after.captured)}</small></div><div class="panel-body">
    <div class="row wrap"><span class="badge green">${sm.added || 0} added</span><span class="badge red">${sm.removed || 0} removed</span><span class="badge amber">${sm.changed || 0} changed</span><span class="badge">${sm.versions || 0} image changes</span><span class="badge red">${sm.newFindings || 0} new findings</span><span class="badge green">${sm.resolvedFindings || 0} resolved</span>${b.loading ? '<span class="badge blue">still loading</span>' : ''}</div>
    ${(r.versions || []).length ? `<h3 class="mt">Image changes</h3><div class="table-scroll"><table><tbody>${r.versions.map(v => `<tr><td><span class="tag">${esc(v.cluster)}</span></td><td class="mono">${esc(v.namespace)}/${esc(v.workload)} ${esc(v.container)}</td><td class="muted">${esc(v.before)}</td><td>→</td><td class="amber">${esc(v.after)}</td></tr>`).join('')}</tbody></table></div>` : ''}
    ${(r.newFindings || []).length ? `<h3 class="mt">New since the earlier bundle</h3>${r.newFindings.map(findingRow).join('')}` : ''}
    ${(r.resolvedFindings || []).length ? `<h3 class="mt">No longer present</h3>${r.resolvedFindings.map(f => `<div class="finding-row"><span class="sev info">✓</span><div><strong>${esc(f.title)}</strong><p>${esc(f.cluster)} · ${esc(f.rule)}</p></div></div>`).join('')}` : ''}
    ${(r.changes || []).length ? `<h3 class="mt">Objects</h3><div class="toolbar"><select id="base-kind"><option value="">All kinds</option>${kinds.map(k => `<option ${S.baseKind === k ? 'selected' : ''}>${esc(k)}</option>`).join('')}</select></div>
      <div class="table-scroll" style="max-height:40vh"><table><thead><tr><th>Change</th><th>Cluster</th><th>Kind</th><th>Object</th><th>Fields</th></tr></thead><tbody>
      ${ch.map((x, i) => `<tr data-base="${i}" style="cursor:pointer" class="${i === sel ? 'sel' : ''}"><td class="${x.change === 'added' ? 'green' : x.change === 'removed' ? 'red' : 'amber'}">${esc(x.change)}</td><td><span class="tag">${esc(x.cluster)}</span></td><td>${esc(x.kind)}</td><td class="mono">${esc(x.namespace ? x.namespace + '/' : '')}${esc(x.name)}</td><td class="muted" style="white-space:normal">${esc((x.fields || []).join(', '))}</td></tr>`).join('')}
      </tbody></table></div>${detail}` : '<p class="muted mt">No object changes between the two captures.</p>'}
    ${(r.notComparable || []).length ? `<div class="notice info mt"><div><strong>Not comparable</strong>: one bundle did not capture these completely, so their absence proves nothing.<br><small>${r.notComparable.slice(0, 12).map(esc).join('<br>')}${r.notComparable.length > 12 ? '<br>…' : ''}</small></div></div>` : ''}
  </div></section>`;
}

async function compare() {
  const c = S.compare;
  const base = await api('api/baseline').catch(() => ({available: false}));
  S.baseReport = base;
  const picker = `<div class="toolbar"><input id="cmp-q" placeholder="Find an object to compare, e.g. checkout-route" aria-label="Find object"><div id="cmp-results" class="search-results" style="width:100%"></div></div>`;
  if (!c) return head('Compare', base.available ? 'Changes since an earlier bundle, and the same object across clusters.' : 'The same object across the clusters in this bundle. Open with --baseline to compare against an earlier bundle.') + baselineSection(base) + picker;
  const d = await api('api/diff', c);
  const found = d.clusters.filter(x => x.found);
  const [L, R] = [found[0], found[1]];
  let body;
  if (found.length < 2) body = `<div class="notice info">${esc(c.kind || c.resource)} ${esc(c.namespace)}/${esc(c.name)} was captured in ${found.map(x => esc(x.cluster)).join(', ') || 'no cluster'} only. Missing in: ${d.clusters.filter(x => !x.found).map(x => esc(x.cluster)).join(', ')}.</div>`;
  else {
    const df = lineDiff(L.yaml.split('\n'), R.yaml.split('\n'));
    const changes = df.a.filter(x => x.c).length + df.b.filter(x => x.c).length;
    body = `<div class="row" style="margin-bottom:10px"><span class="badge ${changes ? 'amber' : 'green'}">${changes ? changes + ' changed lines' : 'identical'}</span><small>metadata noise (uid, resourceVersion, timestamps) removed</small></div>
      <section class="panel"><div class="diff-grid"><div class="diff-col"><div class="panel-head"><h2>${esc(L.cluster)}</h2></div><div class="diff-code">${df.a.map(x => `<span class="${x.c || ''}">${esc(x.t) || ' '}</span>`).join('')}</div></div>
      <div class="diff-col"><div class="panel-head"><h2>${esc(R.cluster)}</h2></div><div class="diff-code">${df.b.map(x => `<span class="${x.c || ''}">${esc(x.t) || ' '}</span>`).join('')}</div></div></div></section>`;
  }
  return head('Compare', `${esc(c.kind || c.resource)} ${esc(c.namespace)}/${esc(c.name)} across clusters`, '<button id="cmp-clear">Pick another object</button>') + body;
}
after.compare = () => {
  document.querySelectorAll('[data-base]').forEach(tr => tr.onclick = () => { S.baseSel = S.baseSel === +tr.dataset.base ? null : +tr.dataset.base; render(); });
  const bk = $('#base-kind'); if (bk) bk.onchange = e => { S.baseKind = e.target.value; S.baseSel = null; render(); };
  wireFindings();
  const clr = $('#cmp-clear'); if (clr) clr.onclick = () => { S.compare = null; render(); };
  const q = $('#cmp-q'); if (!q) return;
  q.focus(); let t;
  q.oninput = () => { clearTimeout(t); t = setTimeout(async () => {
    const hits = await api('api/search', {q: q.value});
    $('#cmp-results').innerHTML = hits.map((h, i) => `<button data-hit="${i}"><span class="badge">${esc(h.kind)}</span>${esc(h.namespace ? h.namespace + '/' : '')}${esc(h.name)}<small>${esc(h.cluster)}</small></button>`).join('');
    document.querySelectorAll('[data-hit]').forEach(b => b.onclick = () => { const h = hits[+b.dataset.hit]; S.compare = {group: h.group, resource: h.resource, namespace: h.namespace, name: h.name, kind: h.kind}; render(); });
  }, 200); };
};

// ---------- proxies ----------
const compLabel = {ztunnel: 'ztunnel', envoy: 'Envoy', agentgateway: 'agentgateway', istiod: 'istiod', controller: 'Controller'};
const pxKey = c => [c.cluster, c.kind, c.namespace, c.pod].join('/');

function certCell(c) {
  if (!c.notAfter || c.notAfter.startsWith('0001')) return `<span class="amber">${esc(c.state || 'no certificate')}</span>`;
  const cls = c.hoursLeft <= 0 ? 'red' : (c.root && c.daysLeft < 30) ? 'amber' : '';
  const left = c.hoursLeft <= 0 ? 'expired' : c.daysLeft >= 2 ? `${c.daysLeft} days` : `${c.hoursLeft} h`;
  return `<span class="${cls}">${esc(left)}</span>`;
}

function pxTable(t, q) {
  const rows = q ? t.rows.filter(r => r.join(' ').toLowerCase().includes(q)) : t.rows;
  return `<section class="panel mt"><div class="panel-head"><h2>${esc(t.title)}</h2><span class="badge">${rows.length}${rows.length !== t.rows.length ? ' of ' + t.rows.length : ''}</span></div>
    <div class="table-scroll" style="max-height:48vh">${rows.length ? `<table><thead><tr>${t.columns.map(c => `<th>${esc(c)}</th>`).join('')}</tr></thead><tbody>
    ${rows.slice(0, 500).map(r => `<tr>${r.map((c, i) => `<td class="${i === 0 ? 'mono' : ''}" style="white-space:normal;max-width:520px">${/^STALE/.test(c) ? `<span class="red">${esc(c)}</span>` : esc(c)}</td>`).join('')}</tr>`).join('')}</tbody></table>` : '<div class="empty">Nothing captured here' + (q ? ' matches the filter' : '') + '.</div>'}</div>
    ${rows.length > 500 ? `<div class="table-foot"><span>Showing 500 of ${rows.length}; narrow with the filter</span></div>` : ''}</section>`;
}

async function proxies() {
  const d = await api('api/proxies', {cluster: S.cluster});
  S.px = S.px || {sel: '', tab: 'summary', file: '', q: ''};
  const comps = d.components;
  if (!comps.length) {
    return head('Proxies', 'Envoy, ztunnel and agentgateway config dumps, istiod debug output and metrics.') + `<div class="panel empty"><strong>${d.selected ? 'No Solo components captured' : 'Proxy dumps, control-plane debug and metrics were not selected'}</strong>${
      d.loading ? 'Diagnostics are written last in a bundle, so they may still be arriving.' : d.selected ? 'The selected namespaces held no ztunnel, Envoy, agentgateway, istiod or controller pods.' : 'The customer did not allow port-forward diagnostics for this bundle (flightbay collect --solo-diagnostics). Their absence says nothing about the proxies.'}</div>`;
  }
  if (!comps.find(c => pxKey(c) === S.px.sel)) { S.px.sel = pxKey(comps[0]); S.px.file = ''; }
  const cur = comps.find(c => pxKey(c) === S.px.sel);
  const groups = {};
  comps.forEach(c => (groups[c.component || 'unknown'] ||= []).push(c));
  const nav = Object.entries(groups).map(([g, cs]) => `<h3 style="margin:10px 8px 4px;font-size:11px" class="muted">${esc(compLabel[g] || g)}</h3>` + cs.map(c => {
    const bad = c.files.some(f => f.state !== 'complete');
    return `<button class="${pxKey(c) === S.px.sel ? 'active' : ''}" data-px="${attr(pxKey(c))}" title="${esc(c.namespace)}/${esc(c.pod)}"><span style="overflow:hidden;text-overflow:ellipsis">${esc(c.pod)}</span><span>${c.expired ? '<small class="red">expired</small>' : c.expiring ? '<small class="amber">cert</small>' : ''} <span class="st state-${bad ? 'truncated' : 'complete'}">●</span></span></button>`;
  }).join('')).join('');
  let body = '';
  const tabs = ['summary', 'files'];
  if (S.px.tab === 'summary') {
    const p = await api('api/proxy', {cluster: cur.cluster, kind: cur.kind, namespace: cur.namespace, pod: cur.pod}).catch(e => ({error: e.message}));
    if (p.error) body = `<div class="notice ${cur.files.length ? 'bad' : 'info'}">${cur.files.length ? esc(p.error) : 'Nothing was captured for this pod. See the Files tab for the reason.'}</div>`;
    else {
      const sm = p.summary; const q = S.px.q.toLowerCase();
      body = `<div class="stats">${Object.entries(sm.counts || {}).map(([k, v]) => `<div class="stat"><div class="eyebrow">${esc(k)}</div><div class="value ${k === 'stale' && v ? 'red' : ''}">${num(v)}</div></div>`).join('')}</div>
        ${(sm.notes || []).map(n => `<div class="notice info mt">${esc(n)}</div>`).join('')}
        ${sm.certs?.length ? `<section class="panel mt"><div class="panel-head"><h2>Certificates</h2><small>Measured at snapshot time · workload certificates are short-lived by design</small></div><div class="table-scroll" style="max-height:34vh"><table><thead><tr><th>Identity</th><th>Type</th><th>State</th><th>Expires</th><th>Left</th></tr></thead><tbody>
          ${sm.certs.map(c => `<tr><td class="mono">${esc(c.identity)}</td><td>${c.root ? 'root' : 'workload'}</td><td>${esc(c.state || '')}</td><td>${fmtDateTime(c.notAfter)}</td><td>${certCell(c)}</td></tr>`).join('')}</tbody></table></div></section>` : ''}
        ${(sm.tables || []).map(t => pxTable(t, q)).join('')}`;
    }
  } else {
    const files = cur.files;
    const sel = files.find(f => f.name === S.px.file && f.bytes) || files.find(f => f.bytes);
    let viewer = '';
    if (sel) {
      S.px.file = sel.name;
      const r = await fetch('api/proxy/file?' + new URLSearchParams({cluster: cur.cluster, kind: cur.kind, namespace: cur.namespace, pod: cur.pod, file: sel.name}), {credentials: 'same-origin'});
      let text = await r.text();
      if (sel.name.endsWith('.json')) { try { text = JSON.stringify(JSON.parse(text), null, 2); } catch (_) { /* shown as captured */ } }
      const q = S.px.q.toLowerCase();
      const lines = text.split('\n');
      const shown = q ? lines.filter(l => l.toLowerCase().includes(q)) : lines;
      viewer = `<section class="panel mt"><div class="panel-head"><h2 class="mono">${esc(sel.name)}</h2><small>${bytes(sel.bytes)} · ${q ? `${num(shown.length)} of ${num(lines.length)} lines match` : num(lines.length) + ' lines'} · redacted and namespace-filtered at collection</small></div>
        <pre class="code" style="max-height:60vh;overflow:auto;margin:0">${esc(shown.slice(0, 20000).join('\n'))}${shown.length > 20000 ? '\n… narrow with the filter to see more' : ''}</pre></section>`;
    }
    body = `<section class="panel"><div class="table-scroll"><table><thead><tr><th>File</th><th>Size</th><th>State</th><th>Notes</th></tr></thead><tbody>
      ${files.map(f => `<tr ${f.bytes ? `data-pxfile="${esc(f.name)}" style="cursor:pointer"` : ''} class="${f.name === S.px.file ? 'sel' : ''}"><td class="mono ${f.bytes ? 'blue' : 'muted'}">${esc(f.name)}</td><td>${f.bytes ? bytes(f.bytes) : '—'}</td><td>${stateBadge(f.state, f.reason)}</td><td class="muted" style="white-space:normal">${esc(f.reason || '')}</td></tr>`).join('')}
      </tbody></table></div></section>${viewer}`;
  }
  const sums = Object.entries(d.summaries || {});
  return head('Proxies', 'Envoy, ztunnel and agentgateway config dumps, istiod debug output and metrics, as captured.', `<button id="px-term">&gt;_ fbpc ${esc(cur.pod)}</button>`) +
    (sums.length ? `<div class="row wrap" style="margin-bottom:10px">${sums.flatMap(([cl, raw]) => (raw.components || []).map(c => `<span class="tag" title="${esc(c.image)}">${esc(cl)} · ${esc(compLabel[c.component] || c.component)} ${esc(c.version || '')} ×${c.pods.length}</span>`)).join(' ')}</div>` : '') + `
  <div class="resource-layout"><nav class="types" aria-label="Components">${nav}</nav>
  <div><div class="toolbar"><div class="row wrap"><span class="badge">${esc(compLabel[cur.component] || cur.component)}</span><span class="tag">${esc(cur.cluster)}</span><span class="mono">${esc(cur.namespace)}/${esc(cur.pod)}</span><small class="muted" title="${esc(cur.image)}">${esc(cur.version || '')}${cur.node ? ' · ' + esc(cur.node) : ''}</small></div></div>
    <div class="toolbar"><div class="tabs" style="border:0;margin:0">${tabs.map(t => `<button class="${t === S.px.tab ? 'active' : ''}" data-pxtab="${t}">${t === 'summary' ? 'Summary' : 'Files'}</button>`).join('')}</div>
    <input id="px-q" placeholder="Filter rows or lines" value="${esc(S.px.q)}" aria-label="Filter"></div>
    ${body}</div></div>`;
}
after.proxies = () => {
  document.querySelectorAll('[data-px]').forEach(b => b.onclick = () => { S.px.sel = JSON.parse(b.dataset.px); S.px.file = ''; render(); });
  document.querySelectorAll('[data-pxtab]').forEach(b => b.onclick = () => { S.px.tab = b.dataset.pxtab; render(); });
  document.querySelectorAll('[data-pxfile]').forEach(tr => tr.onclick = () => { S.px.file = tr.dataset.pxfile; render(); });
  let t; const qi = $('#px-q'); if (qi) qi.oninput = e => { clearTimeout(t); t = setTimeout(() => { S.px.q = e.target.value; render().then(() => { const i = $('#px-q'); if (i) { i.focus(); i.setSelectionRange(i.value.length, i.value.length); } }); }, 250); };
  const pt = $('#px-term'); if (pt) pt.onclick = () => { const [cl, , ns, pod] = S.px.sel.split('/'); termRun(`fbpc --cluster ${cl} -n ${ns} ${pod}`); };
};

// ---------- coverage ----------
async function coverage() {
  const c = await api('api/coverage');
  const st = c.state;
  const rep = c.redaction || {};
  const locs = Object.entries(rep.locations || {}).sort();
  const cov = c.coverage.filter(x => !S.cluster || x.cluster === S.cluster);
  const target = x => x.type === 'logs' ? `logs ${x.namespace}/${x.pod}/${x.container} (${x.stream})` : x.type === 'events' ? 'events' : x.type === 'diagnostic' ? `${x.container} ${x.namespace}/${x.pod} ${x.resource}` : (x.group ? `${x.resource}.${x.group}` : x.resource);
  return head('Coverage', 'What the customer allowed, what was collected, and what the importer verified.') + `
  <div class="grid2"><div>
    <section class="panel"><div class="panel-head"><h2>Collections</h2><small>${cov.length} records · ${cov.filter(x => x.state !== 'complete').length} not complete</small></div>
    <div class="table-scroll" style="max-height:55vh"><table><thead><tr><th>Cluster</th><th>Collection</th><th>Items</th><th>State</th><th>Reason</th></tr></thead><tbody>
    ${cov.map(x => `<tr><td><span class="tag">${esc(x.cluster)}</span></td><td class="mono">${esc(target(x))}</td><td>${num(x.items)}</td><td>${stateBadge(x.state === 'complete' ? 'complete' : x.state, x.reason)}</td><td class="muted" style="white-space:normal">${esc(x.reason || '')}</td></tr>`).join('')}
    </tbody></table></div></section>
    <section class="panel mt"><div class="panel-head"><h2>Collection log</h2><small>${(c.collectionLog || []).length} entries</small></div>
    <div class="table-scroll" style="max-height:40vh"><table><thead><tr><th>Cluster</th><th>Operation</th><th>Target</th><th>Result</th><th>Error</th></tr></thead><tbody>
    ${(c.collectionLog || []).map(e => `<tr><td>${esc(e.cluster)}</td><td>${esc(e.operation)}</td><td class="mono">${esc(e.target || '')} ${esc(e.namespace || '')}</td><td class="${/fail|partial/.test(e.result) ? 'red' : ''}">${esc(e.result)}</td><td class="muted" style="white-space:normal">${esc(e.error || '')}</td></tr>`).join('')}
    </tbody></table></div></section>
  </div><div>
    <section class="panel"><div class="panel-head"><h2>Import</h2>${st.verified ? '<span class="badge green">verified</span>' : `<span class="badge amber">${esc(st.phase)}</span>`}</div><div class="panel-body"><dl class="metadata">
      <dt>Source</dt><dd class="mono">${esc(st.source)}</dd><dt>Received</dt><dd>${bytes(st.bytesReceived)} in ${num(st.members)} members</dd><dt>Objects</dt><dd>${num(st.objects)}</dd><dt>Log lines</dt><dd>${num(st.logLines)}</dd><dt>Events</dt><dd>${num(st.events)}</dd>
      <dt>Started</dt><dd>${fmtDateTime(st.started)}</dd><dt>Finished</dt><dd>${fmtDateTime(st.finished)}</dd>${st.error ? `<dt>Error</dt><dd class="red">${esc(st.error)}</dd>` : ''}${st.deferred?.length ? `<dt>Deferred</dt><dd>${esc(st.deferred.join(', '))}</dd>` : ''}</dl></div></section>
    ${(c.unavailable || []).length ? `<div class="notice mt"><span>△</span><div><strong>Not collected by policy or build</strong><br>${c.unavailable.map(esc).join('<br>')}</div></div>` : ''}
    <section class="panel mt"><div class="panel-head"><h2>Redaction report</h2><span class="badge">${num(rep.total)} replacements</span></div><div class="panel-body">
      <p class="muted" style="font-size:12px">Counts only. Redacted values are never recorded, and Secret values are never collected.</p>
      <div class="table-scroll mt" style="max-height:30vh"><table><tbody>${locs.map(([l, rules]) => `<tr><td class="mono" style="white-space:normal">${esc(l)}</td><td>${Object.entries(rules).map(([r, n]) => `<span class="tag">${esc(r)} ${n}</span>`).join(' ')}</td></tr>`).join('') || '<tr><td class="muted">No replacements recorded</td></tr>'}</tbody></table></div></div></section>
    <section class="panel mt"><div class="panel-head"><h2>Collection policy</h2></div><div class="panel-body"><pre class="code">${esc(JSON.stringify(c.policy, null, 2))}</pre></div></section>
  </div></div>`;
}

// ---------- modal, search, cluster ----------
function modal(title, body) {
  $('#modal-root').innerHTML = `<div class="modal-backdrop"><section class="modal" role="dialog" aria-modal="true" aria-label="${esc(title)}"><div class="panel-head"><h2>${esc(title)}</h2><button class="ghost" id="m-close" aria-label="Close">✕</button></div><div class="panel-body">${body}</div></section></div>`;
  $('.modal-backdrop').onclick = e => { if (e.target.classList.contains('modal-backdrop')) closeModal(); };
  $('#m-close').onclick = closeModal;
}
function closeModal() { $('#modal-root').innerHTML = ''; }

function openSearch() {
  modal('Jump to an object or view', `<input id="gs" style="width:100%" placeholder="Pods, routes, services…" autocomplete="off"><div id="gs-res" class="search-results"></div>`);
  const views = ['overview', 'resources', 'logs', 'timeline', 'findings', 'compare', 'proxies', 'coverage'];
  let hits = [], sel = 0;
  const draw = () => {
    $('#gs-res').innerHTML = hits.map((h, i) => `<button class="${i === sel ? 'sel' : ''}" data-i="${i}">${h.view ? `<span class="green">↗</span> ${esc(h.view)}<small>view</small>` : `<span class="badge">${esc(h.kind)}</span>${esc(h.namespace ? h.namespace + '/' : '')}${esc(h.name)}<small>${esc(h.cluster)}</small>`}</button>`).join('');
    document.querySelectorAll('#gs-res [data-i]').forEach(b => b.onclick = () => pick(hits[+b.dataset.i]));
  };
  const pick = h => { closeModal(); if (h.view) nav(h.view); else openObject(h); };
  let t;
  const input = $('#gs');
  input.oninput = () => { clearTimeout(t); t = setTimeout(async () => {
    const q = input.value.toLowerCase();
    hits = views.filter(v => q && v.includes(q)).map(v => ({view: v})).concat(q ? await api('api/search', {q}) : []); sel = 0; draw();
  }, 150); };
  input.onkeydown = e => {
    if (e.key === 'ArrowDown') { sel = Math.min(sel + 1, hits.length - 1); draw(); e.preventDefault(); }
    if (e.key === 'ArrowUp') { sel = Math.max(sel - 1, 0); draw(); e.preventDefault(); }
    if (e.key === 'Enter' && hits[sel]) pick(hits[sel]);
  };
  input.focus();
}

function setCluster(c) { S.cluster = c; $('#cluster').value = c; S.res.ns = ''; S.logs.namespace = S.logs.pod = S.logs.container = ''; render(); }

// ---------- terminal ----------
function ensureTerminal() {
  if (S.term) return S.term;
  if (!window.Terminal) { toast('Terminal assets failed to load'); return null; }
  const term = new window.Terminal({fontSize: 12, fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace', cursorBlink: true, scrollback: 5000,
    theme: {background: '#0d1215', foreground: '#c9d6de', cursor: '#72d6be', selectionBackground: '#2b4a41'}});
  const fit = new window.FitAddon.FitAddon();
  term.loadAddon(fit);
  term.open($('#term-body'));
  const t = {term, fit, ws: null, queue: []};
  const connect = () => {
    const u = new URL('api/terminal', location.href); u.protocol = location.protocol === 'https:' ? 'wss:' : 'ws:'; u.hash = '';
    const ws = new WebSocket(u.href);
    ws.binaryType = 'arraybuffer';
    ws.onopen = () => { t.ws = ws; fitTerm(); t.queue.splice(0).forEach(m => ws.send(m)); };
    ws.onmessage = e => term.write(typeof e.data === 'string' ? e.data : new Uint8Array(e.data));
    ws.onclose = () => { t.ws = null; term.write('\r\n\x1b[33m[flightbay] terminal disconnected, reconnecting…\x1b[0m\r\n'); setTimeout(connect, 1500); };
  };
  term.onData(d => send({type: 'input', data: d}));
  const send = m => { const s = JSON.stringify(m); if (t.ws?.readyState === 1) t.ws.send(s); else t.queue.push(s); };
  t.send = send;
  const fitTerm = () => { try { fit.fit(); send({type: 'resize', cols: term.cols, rows: term.rows}); } catch (_) { /* hidden */ } };
  t.fitTerm = fitTerm;
  window.addEventListener('resize', fitTerm);
  S.term = t;
  connect();
  return t;
}
function toggleTerminal(open) {
  const dock = $('#terminal');
  if (dock.classList.contains('hidden')) { toast('The terminal is disabled for this workspace'); return; }
  const willOpen = open ?? dock.classList.contains('collapsed');
  dock.classList.toggle('collapsed', !willOpen);
  if (willOpen) { const t = ensureTerminal(); if (t) setTimeout(() => { t.fitTerm(); t.term.focus(); }, 30); }
}
function termRun(cmd) {
  if (!S.ws?.terminal) { navigator.clipboard?.writeText(cmd); toast('Terminal disabled; command copied: ' + cmd); return; }
  toggleTerminal(true);
  const t = ensureTerminal(); if (t) t.send({type: 'input', data: cmd + '\r'});
}

// ---------- boot ----------
function boot() {
  $('#open-search').onclick = openSearch;
  $('#cluster').onchange = e => setCluster(e.target.value);
  $('#term-toggle').onclick = () => toggleTerminal();
  $('#term-expand').onclick = () => { $('#terminal').classList.toggle('expanded'); toggleTerminal(true); };
  document.addEventListener('keydown', e => {
    const typing = /INPUT|TEXTAREA|SELECT/.test(document.activeElement?.tagName) || document.activeElement?.closest?.('.xterm');
    if ((e.metaKey || e.ctrlKey) && e.key === 'k') { e.preventDefault(); openSearch(); }
    else if (e.ctrlKey && e.key === '`') { e.preventDefault(); toggleTerminal(); }
    else if (e.key === 'Escape') { closeModal(); closeDrawer(); }
    else if (!typing && e.key === '/') { e.preventDefault(); ($('#log-q') || $('#res-q'))?.focus(); }
  });
  addEventListener('hashchange', () => { parseHash(); closeDrawer(); render(); });
  parseHash();
  loadWorkspace().then(render).catch(e => { $('#main').innerHTML = `<div class="notice bad">${esc(e.message)}</div>`; });
  const es = new EventSource('api/state/stream');
  es.addEventListener('state', e => onState(JSON.parse(e.data)));
}
boot();
