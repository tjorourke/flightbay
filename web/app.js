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
  logs: {q: '', namespace: '', workload: '', pod: '', container: '', level: '', stream: '', from: '', to: '', limit: 200, expanded: null, follow: false, total: 0, rev: 0, result: null},
  compare: null, findingSev: '', term: null,
  search: {q: '', limit: 200},
  pst: {area: '', status: 'attention', q: '', open: null},
  nv: {resource: 'pods', group: '', ns: '', q: '', sel: 0, cmd: false},
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
    for (const k of ['q', 'namespace', 'workload', 'pod', 'container', 'level', 'stream', 'from', 'to']) if (p.has(k)) S.logs[k] = p.get(k);
  }
  if (view === 'search' && p.has('q')) S.search.q = p.get('q');
  if (view === 'resources' && p.has('resource')) { S.res.resource = p.get('resource'); S.res.group = p.get('group') || ''; }
  S.view = view;
}

function nav(view) {
  if (location.hash.slice(1) === view) render(); else location.hash = view;
}

function openLogs(filters) {
  Object.assign(S.logs, {q: '', namespace: '', workload: '', pod: '', container: '', level: '', stream: '', from: '', to: '', expanded: null}, filters);
  closeDrawer(); closeModal();
  if (S.view === 'logs') render(); else location.hash = 'logs';
}

async function render() {
  const seq = ++S.renderSeq;
  document.querySelectorAll('.nav a').forEach(a => a.classList.toggle('active', a.dataset.view === S.view));
  const views = {navigator, search, overview, posture, resources, logs, timeline, findings, compare, proxies, coverage};
  const fn = views[S.view] || overview;
  $('#crumb').textContent = S.ws ? `Workspace / ${S.ws.case ? 'Case ' + S.ws.case : 'Snapshot'} / ${S.view}` : 'Workspace';
  try {
    const html = await fn(seq);
    if (seq !== S.renderSeq || html === undefined) return;
    taClose(true);
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

// refocus puts the caret back in a query input after a re-render replaced it.
function refocus(sel, caret, value) {
  const i = typeof sel === 'string' ? $(sel) : sel; if (!i) return;
  if (value !== undefined) i.value = value;
  i.focus();
  const c = caret == null ? i.value.length : Math.min(caret, i.value.length);
  i.setSelectionRange(c, c);
}

// ---------- typeahead ----------
// One suggestion dropdown for every query input. It reads the key:value term
// under the caret, suggests keys while there is no colon, then values from
// the capture (the server for objects and logs, data already in the page for
// the timeline, best practices and navigator).
const TA = {cache: new Map(), keys: new Map(), menu: null, active: null, reopen: null};

// qTerms splits a query into terms, keeping quoted phrases and -negation.
function qTerms(q) {
  return ((q || '').match(/-?(?:[A-Za-z_][\w.\-]*:)?"[^"]*"?|\S+/g) || []).map(t => {
    const neg = t.startsWith('-') && t.length > 1, body = neg ? t.slice(1) : t;
    const m = body.match(/^([A-Za-z_][\w.\-]*):(.*)$/);
    return m && !body.startsWith('"') ? {neg, key: m[1].toLowerCase(), val: m[2].replace(/^"|"$/g, '')} : {neg, text: body.replace(/"/g, '')};
  }).filter(t => t.text !== '' && t.val !== '');
}

function taToken(value, caret) {
  let start = 0, inQ = false;
  for (let i = 0; i < caret; i++) { const c = value[i]; if (c === '"') inQ = !inQ; else if (!inQ && /\s/.test(c)) start = i + 1; }
  let end = caret;
  while (end < value.length && (inQ || !/\s/.test(value[end]))) { if (value[end] === '"') inQ = !inQ; end++; }
  const upto = value.slice(start, caret);
  const neg = upto.startsWith('-') ? '-' : '', body = upto.slice(neg.length);
  const m = body.match(/^([A-Za-z_][\w.\-]*):(.*)$/s);
  return m ? {start, end, neg, key: m[1], partial: m[2].replace(/^"/, '').replace(/"$/, '')} : {start, end, neg, key: null, partial: body.replace(/^"/, ''), quoted: body.startsWith('"')};
}
const taQuote = v => /[\s"]/.test(v) ? `"${v.replace(/"/g, '')}"` : v;

function taFetch(scope, field, prefix, q) {
  const key = [scope, field, prefix, q, S.cluster].join('\u0000');
  if (!TA.cache.has(key)) {
    if (TA.cache.size > 500) TA.cache.clear();
    TA.cache.set(key, api('api/suggest', {scope, field, prefix, q, cluster: S.cluster}).then(r => r.values || []).catch(() => { TA.cache.delete(key); return []; }));
  }
  return TA.cache.get(key);
}
function taKeys(o) {
  if (o.keys) return Promise.resolve(typeof o.keys === 'function' ? o.keys() : o.keys);
  const k = TA.keys.get(o.scope);
  if (k && Date.now() - k.t < 60000) return k.p;
  const p = api('api/suggest', {scope: o.scope, field: ''}).then(r => r.values || []).catch(() => []);
  TA.keys.set(o.scope, {t: Date.now(), p});
  return p;
}
// countValues turns a list of strings into sorted suggestions for client-side scopes.
function countValues(list, prefix, hint) {
  const m = new Map(), p = (prefix || '').toLowerCase();
  for (const v of list) if (v && String(v).toLowerCase().startsWith(p)) m.set(v, (m.get(v) || 0) + 1);
  return [...m].sort((a, b) => b[1] - a[1] || String(a[0]).localeCompare(String(b[0]))).slice(0, 50).map(([value, count]) => ({value, count, hint: hint?.(value) || ''}));
}

function taClose(keep) {
  const a = TA.active;
  if (keep && a && document.activeElement === a.input && TA.menu?.isConnected) TA.reopen = a.input.id;
  TA.menu?.remove(); TA.active = null;
}
addEventListener('resize', () => TA.active?.place());
document.addEventListener('scroll', () => TA.active?.place(), true);

// typeahead(input, {scope, keys, values, bareValues, bareField, alias, extraQ, onAccept, bare})
//   scope      objects | logs: values come from /api/suggest
//   keys       [{value, hint, example}] or a function; default: the server's keys for scope
//   values     (field, partial, rest) => [{value, count, hint}] for client-side scopes; rest is the query without this term
//   bareValues partial => suggestions for a term with no colon (navigator types)
//   bareField  server field to suggest for bare text, e.g. name
//   extraQ     () => extra query terms that narrow suggestions
//   onAccept   (input) => run the view's search after a value is chosen
function typeahead(input, o) {
  if (!input || input._ta) return input?._ta;
  const st = {items: [], sel: -1, tok: null, seq: 0, timer: null};
  const self = {input, place, close: () => taClose()};
  input._ta = self;
  const rest = () => {
    const v = input.value, t = st.tok;
    return [(t ? v.slice(0, t.start) + v.slice(t.end) : v).trim(), o.extraQ?.() || ''].filter(Boolean).join(' ');
  };
  const field = k => { const l = k.toLowerCase(); return (o.alias || {})[l] || l; };
  async function refresh(force) {
    const tok = st.tok = taToken(input.value, input.selectionStart ?? input.value.length);
    const my = ++st.seq;
    let items = [];
    if (!tok.key) {
      if (tok.quoted || (!tok.partial && !force)) { st.items = []; draw(); return; }
      const p = tok.partial.toLowerCase();
      const keys = await taKeys(o);
      items = keys.filter(k => k.value.toLowerCase().startsWith(p)).map(k => ({type: 'key', value: k.value + ':', hint: k.hint, example: k.example, count: k.count}));
      if (o.bareValues) items = items.concat(o.bareValues(tok.partial).map(x => ({...x, type: 'text'})));
      else if (o.bareField && tok.partial.length >= 3) {
        if (!TA.cache.has([o.scope, o.bareField, tok.partial, rest(), S.cluster].join('\u0000'))) { clearTimeout(st.timer); await new Promise(r => { st.timer = setTimeout(r, 120); }); if (my !== st.seq) return; }
        items = items.concat((await taFetch(o.scope, o.bareField, tok.partial, rest())).slice(0, 12).map(x => ({...x, type: 'text'})));
      }
    } else {
      const f = field(tok.key);
      if (o.values) items = await o.values(f, tok.partial, rest());
      else {
        const q = rest();
        if (!TA.cache.has([o.scope, f, tok.partial, q, S.cluster].join('\u0000'))) { clearTimeout(st.timer); await new Promise(r => { st.timer = setTimeout(r, 120); }); if (my !== st.seq) return; }
        items = await taFetch(o.scope, f, tok.partial, q);
      }
      items = (items || []).filter(x => !(x.value === tok.partial && !x.more && !force)).map(x => ({...x, type: 'value'}));
    }
    if (my !== st.seq) return;
    st.items = items.slice(0, 60); st.sel = -1; draw();
  }
  function place() {
    const m = TA.menu; if (!m || TA.active !== self) return;
    if (!input.isConnected) { taClose(); return; }
    const r = (input.closest('.query') || input).getBoundingClientRect();
    const w = Math.min(Math.max(Math.min(r.width, 640), 360), innerWidth - r.left - 12);
    Object.assign(m.style, {left: r.left + 'px', top: r.bottom + 4 + 'px', width: w + 'px', maxHeight: Math.max(160, Math.min(360, innerHeight - r.bottom - 16)) + 'px'});
  }
  function draw() {
    if (!st.items.length || document.activeElement !== input || !input.isConnected) { if (TA.active === self) taClose(); return; }
    if (TA.active && TA.active !== self) taClose();
    const m = TA.menu || (TA.menu = document.createElement('div'));
    m.className = 'ta-menu'; m.setAttribute('role', 'listbox');
    const tok = st.tok;
    const head = tok.key ? `<div class="ta-head">${esc(field(tok.key))} <span>values in this capture</span></div>` : '';
    m.innerHTML = head + st.items.map((it, i) => `<button type="button" tabindex="-1" role="option" class="ta-item ta-${it.type} ${i === st.sel ? 'sel' : ''}" data-tai="${i}">
      <span class="ta-v">${esc(it.value)}${it.more ? '<b>=…</b>' : ''}</span><span class="ta-h">${esc(it.hint || '')}${it.example ? ` <code>${esc(it.example)}</code>` : ''}</span>${it.count ? `<span class="ta-c">${num(it.count)}</span>` : ''}</button>`).join('') +
      '<div class="ta-foot">↑ ↓ choose · Enter or Tab insert · Esc close</div>';
    if (!m.isConnected) document.body.appendChild(m);
    TA.active = self;
    m.onmousedown = e => e.preventDefault();
    m.querySelectorAll('[data-tai]').forEach(b => { b.onclick = () => accept(+b.dataset.tai); });
    place();
  }
  function highlight() {
    TA.menu?.querySelectorAll('[data-tai]').forEach(b => b.classList.toggle('sel', +b.dataset.tai === st.sel));
    TA.menu?.querySelector('.ta-item.sel')?.scrollIntoView({block: 'nearest'});
  }
  function accept(i) {
    const it = st.items[i], t = st.tok; if (!it || !t) return;
    const v = input.value;
    let ins;
    if (it.type === 'key') ins = t.neg + it.value;
    else if (it.type === 'text') ins = t.neg + (o.bareValues ? it.value : taQuote(it.value) + ' ');
    else ins = t.neg + t.key + ':' + (it.more ? it.value + '=' : taQuote(it.value) + ' ');
    let after = v.slice(t.end);
    if (ins.endsWith(' ') && after.startsWith(' ')) after = after.slice(1);
    input.value = v.slice(0, t.start) + ins + after;
    const c = t.start + ins.length;
    input.setSelectionRange(c, c);
    st.items = []; taClose();
    if (it.type === 'key' || it.more) { refresh(true); return; }
    if (o.onAccept) o.onAccept(input); else input.dispatchEvent(new Event('input', {bubbles: true}));
  }
  input.setAttribute('autocomplete', 'off');
  input.addEventListener('keydown', e => {
    const open = TA.active === self && st.items.length;
    if (!open) {
      if (e.key === 'ArrowDown' && (input.value.trim() === '' || e.altKey || e.ctrlKey)) { e.preventDefault(); e.stopImmediatePropagation(); refresh(true); }
      else if (e.key === ' ' && e.ctrlKey) { e.preventDefault(); refresh(true); }
      return;
    }
    const n = st.items.length;
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { st.sel = e.key === 'ArrowDown' ? (st.sel + 1) % n : (st.sel <= 0 ? n - 1 : st.sel - 1); highlight(); }
    else if ((e.key === 'Enter' && st.sel >= 0) || (e.key === 'Tab' && !e.shiftKey)) accept(Math.max(0, st.sel));
    else if (e.key === 'Escape') { st.items = []; taClose(); }
    else { if (e.key === 'Enter') { st.items = []; taClose(); } return; }
    e.preventDefault(); e.stopImmediatePropagation(); e.stopPropagation();
  }, true);
  input.addEventListener('input', () => refresh());
  input.addEventListener('click', () => { if (TA.active === self) refresh(); });
  input.addEventListener('keyup', e => { if (TA.active === self && /^(ArrowLeft|ArrowRight|Home|End)$/.test(e.key)) refresh(); });
  input.addEventListener('blur', () => setTimeout(() => { if (document.activeElement !== input && TA.active === self) taClose(); }, 120));
  input.addEventListener('focus', () => { if (TA.reopen && TA.reopen === input.id) { TA.reopen = null; setTimeout(() => refresh(), 0); } });
  if (TA.reopen && TA.reopen === input.id && document.activeElement === input) { TA.reopen = null; setTimeout(() => refresh(), 0); }
  return self;
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
  if (st.revision !== prev.revision) { TA.cache.clear(); TA.keys.clear(); }
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

// ---------- kinds, grouped by area ----------
const areaOrder = ['Workloads', 'Networking', 'Gateway API', 'Istio', 'agentgateway and kgateway', 'kagent', 'Solo', 'Config, access and storage', 'Other'];
function kindArea(t) {
  const g = t.group || '', k = t.kind || t.resource;
  if ((g === '' && ['Pod', 'ReplicationController', 'PodTemplate'].includes(k)) || ['apps', 'batch', 'autoscaling', 'policy'].includes(g)) return 'Workloads';
  if ((g === '' && ['Service', 'Endpoints'].includes(k)) || g === 'discovery.k8s.io' || g === 'networking.k8s.io') return 'Networking';
  if (g.startsWith('gateway.networking')) return 'Gateway API';
  if (g.endsWith('istio.io')) return 'Istio';
  if (/agentgateway|kgateway|gateway\.solo\.io/.test(g)) return 'agentgateway and kgateway';
  if (g.includes('kagent')) return 'kagent';
  if (g.endsWith('solo.io')) return 'Solo';
  if (g === '' || ['rbac.authorization.k8s.io', 'coordination.k8s.io', 'storage.k8s.io', 'apiextensions.k8s.io', 'admissionregistration.k8s.io'].includes(g)) return 'Config, access and storage';
  return 'Other';
}
// groupKinds keeps types with captured objects, grouped by area and sorted by count.
function groupKinds(types) {
  const withObjects = types.filter(t => t.count > 0);
  const seen = {};
  withObjects.forEach(t => { const k = t.kind || t.resource; seen[k] = (seen[k] || 0) + 1; });
  const groups = {};
  withObjects.forEach(t => (groups[kindArea(t)] ||= []).push({...t, dup: seen[t.kind || t.resource] > 1}));
  Object.values(groups).forEach(g => g.sort((a, b) => b.count - a.count || (a.kind || '').localeCompare(b.kind || '')));
  return {groups: areaOrder.filter(a => groups[a]).map(a => [a, groups[a]]), hidden: types.length - withObjects.length};
}
const kindLabel = t => `${esc(t.kind || t.resource)}${t.dup ? `<span class="grp">${esc(t.group || 'core')}</span>` : ''}`;

// ---------- navigator (k9s-style, keyboard first) ----------
const nvQuick = [['pods', ''], ['deployments', 'apps'], ['services', ''], ['gateways', 'gateway.networking.k8s.io'], ['httproutes', 'gateway.networking.k8s.io'], ['events', '']];

async function navigator() {
  const N = S.nv, w = S.ws || await api('api/workspace');
  const types = await api('api/resources', {cluster: S.cluster});
  S.nvTypes = types;
  const have = types.filter(t => t.count > 0);
  let cur = types.find(t => t.resource === N.resource && t.group === N.group) || have.find(t => t.resource === 'pods') || have[0];
  if (!cur) return head('Navigator', 'Nothing captured yet.');
  N.resource = cur.resource; N.group = cur.group;
  const list = await api('api/objects', {cluster: S.cluster, group: N.group, resource: N.resource, namespace: N.ns, q: N.q});
  N.rows = list.rows; N.sel = Math.min(N.sel, Math.max(0, list.rows.length - 1));
  const nss = [...new Set(list.rows.map(r => r.namespace).filter(Boolean))].sort();
  if (N.ns && !nss.includes(N.ns)) nss.unshift(N.ns);
  const ctx = (w.clusters || []).map((c, i) => `<option value="${esc(c.name)}" ${S.cluster === c.name ? 'selected' : ''}>${i + 1} ${esc(c.name)}@snapshot</option>`).join('');
  const quick = nvQuick.filter(([r, g]) => have.some(t => t.resource === r && t.group === g));
  const statusCol = list.columns.indexOf('Status');
  const sel = list.rows[N.sel];
  return `<div class="nv">
    <div class="nv-head">
      <dl><dt>Bundle:</dt><dd>${esc(w.case ? 'case ' + w.case : w.bundleId || 'snapshot')}</dd>
        <dt>Context:</dt><dd><select id="nv-ctx"><option value="">0 all clusters</option>${ctx}</select></dd>
        <dt>Snapshot:</dt><dd style="color:var(--amber)">${fmtDateTime(w.snapshot)}</dd>
        <dt>Connection:</dt><dd style="color:var(--teal)">offline · read-only</dd></dl>
      <div class="nv-keys"><b>↑ ↓ / j k</b><span>navigate</span><b>/</b><span>filter</span><b>:</b><span>resource type</span><b>0-9</b><span>cluster</span></div>
      <div class="nv-keys"><b>Enter</b><span>describe object</span><b>y</b><span>captured YAML</span><b>l · p</b><span>logs · previous logs</span><b>s</b><span>open in k9s</span><b>Esc</b><span>back</span></div>
      <div class="nv-logo"> ┌─┐ ┌─┐ ┌─┐\n │ │ │ │ │ │  flightbay\n │ └─┘ │ │ │  snapshot\n └─────┘ └─┘  navigator</div>
    </div>
    <div class="nv-frame"><div class="nv-title">${esc(cur.kind || cur.resource)}(${esc(N.ns || 'all')}) [${list.rows.length}]</div>
      ${N.cmd ? `<div class="nv-cmd"><span>:</span><input id="nv-cmdline" placeholder="pods, deploy, svc, httproute, gateway…  (Enter to switch, Esc to cancel)" autocomplete="off" spellcheck="false"></div>` : ''}
      <div class="nv-bar">${quick.map(([r, g]) => `<button class="nv-type ${r === N.resource && g === N.group ? 'on' : ''}" data-nvtype="${attr({r, g})}">:${r}</button>`).join('')}
        <span style="flex:1"></span><select id="nv-ns"><option value="">All namespaces</option>${nss.map(n => `<option ${n === N.ns ? 'selected' : ''}>${esc(n)}</option>`).join('')}</select>
        <input id="nv-q" placeholder="/ filter by name, status or label:" value="${esc(N.q)}" style="width:280px" autocomplete="off" spellcheck="false"></div>
      <div class="table-scroll" style="max-height:58vh"><table><thead><tr>${cur.namespaced ? '<th>Namespace</th>' : ''}${list.columns.map(c => `<th>${esc(c)}</th>`).join('')}<th></th></tr></thead><tbody>
        ${list.rows.map((r, i) => `<tr class="${i === N.sel ? 'sel' : ''}" data-nvrow="${i}">${cur.namespaced ? `<td>${esc(r.namespace)}</td>` : ''}${r.cells.map((c, j) => `<td>${j === 0 && i === N.sel ? '› ' : ''}${j === statusCol ? statusCell(c) : esc(c)}</td>`).join('')}
          <td style="text-align:right"><button class="ghost" data-nvyaml="${i}">YAML</button>${(N.resource === 'pods' && !N.group) || workloadKinds.includes(r.kind) ? `<button class="ghost" data-nvlogs="${i}">Logs</button>` : ''}</td></tr>`).join('') || `<tr><td colspan="9" class="muted">No ${esc(cur.kind || cur.resource)} objects${N.q ? ' match the filter' : ''}.</td></tr>`}
      </tbody></table></div>
      <div class="nv-status"><span>${sel ? `${esc(sel.name)}${sel.namespace ? ' · ' + esc(sel.namespace) : ''}${!S.cluster ? ' · ' + esc(sel.cluster) : ''}` : ''}</span><span><b>Enter</b> describe · <b>y</b> yaml · <b>l</b> logs · <b>:</b> type · <b>/</b> filter</span></div>
    </div></div>`;
}

const labelKey = [{value: 'label', hint: 'label key or key=value', example: 'label:app=checkout'}];
// nvTypeSuggestions lists the captured types for the : command line, matching
// the resource name, kind, singular name or a short name.
function nvTypeSuggestions(p) {
  const w = (p || '').toLowerCase().replace(/^:/, '');
  const types = S.nvTypes || [];
  const dup = {}; types.forEach(t => { dup[t.resource] = (dup[t.resource] || 0) + 1; });
  return types.filter(t => !w || t.resource.startsWith(w) || (t.kind || '').toLowerCase().startsWith(w) || (t.singularName || '').startsWith(w) || (t.shortNames || []).some(s => s.startsWith(w)))
    .sort((a, b) => (b.count > 0) - (a.count > 0) || b.count - a.count || a.resource.localeCompare(b.resource)).slice(0, 40)
    .map(t => ({value: dup[t.resource] > 1 ? t.name : t.resource, count: t.count, hint: `${t.kind}${(t.shortNames || []).length ? ' · ' + t.shortNames.join(', ') : ''}${t.group ? ' · ' + t.group : ''}${t.count ? '' : ' · none captured'}`}));
}

function nvRef(r) { return {cluster: r.cluster, group: r.group, resource: r.resource, namespace: r.namespace, name: r.name}; }

after.navigator = () => {
  const N = S.nv;
  const setType = (r, g) => { N.resource = r; N.group = g; N.sel = 0; N.q = ''; N.cmd = false; render(); };
  document.querySelectorAll('[data-nvtype]').forEach(b => b.onclick = () => { const t = JSON.parse(b.dataset.nvtype); setType(t.r, t.g); });
  document.querySelectorAll('[data-nvrow]').forEach(tr => { tr.onclick = () => { N.sel = +tr.dataset.nvrow; render(); }; tr.ondblclick = () => openObject(nvRef(N.rows[+tr.dataset.nvrow])); });
  document.querySelectorAll('[data-nvyaml]').forEach(b => b.onclick = e => { e.stopPropagation(); openObject(nvRef(N.rows[+b.dataset.nvyaml]), 'yaml'); });
  document.querySelectorAll('[data-nvlogs]').forEach(b => b.onclick = e => { e.stopPropagation(); const r = N.rows[+b.dataset.nvlogs]; openLogs(r.kind === 'Pod' ? {pod: r.name, namespace: r.namespace} : {workload: `${r.kind}/${r.name}`, namespace: r.namespace}); });
  $('#nv-ns').onchange = e => { N.ns = e.target.value; N.sel = 0; render(); };
  $('#nv-ctx').onchange = e => setCluster(e.target.value);
  let t; $('#nv-q').oninput = e => { clearTimeout(t); t = setTimeout(() => { const c = e.target.selectionStart, v = e.target.value; N.q = v; N.sel = 0; render().then(() => refocus('#nv-q', c, v)); }, 200); };
  typeahead($('#nv-q'), {scope: 'objects', keys: labelKey, alias: {l: 'label'}, extraQ: () => `kind:${N.resource}${N.group ? '.' + N.group : ''}${N.ns ? ' ns:' + N.ns : ''}`});
  $('#nv-q').onkeydown = e => { if (e.key === 'Escape' || e.key === 'Enter') { e.target.blur(); } };
  const cl = $('#nv-cmdline');
  if (cl) {
    const go = async value => {
      const want = value.trim().toLowerCase().replace(/^:/, '');
      const types = await api('api/resources', {cluster: S.cluster});
      const exact = x => x.resource === want || x.name === want || (x.kind || '').toLowerCase() === want || x.singularName === want || (x.shortNames || []).includes(want);
      let m = types.filter(exact);
      if (!m.length) m = types.filter(x => x.resource.startsWith(want) || (x.kind || '').toLowerCase().startsWith(want));
      m.sort((a, b) => b.count - a.count || (a.group ? 1 : 0) - (b.group ? 1 : 0));
      if (m.length) setType(m[0].resource, m[0].group); else toast(`No type matches "${want}" in this bundle`);
    };
    typeahead(cl, {keys: [], bareValues: nvTypeSuggestions, onAccept: i => go(i.value)});
    cl.focus();
    cl.onkeydown = e => {
      if (e.key === 'Escape') { N.cmd = false; render(); return; }
      if (e.key === 'Enter') go(cl.value);
    };
    // Show the captured types straight away, as k9s does.
    setTimeout(() => cl.dispatchEvent(new KeyboardEvent('keydown', {key: 'ArrowDown'})), 0);
  }
};

// Keyboard: only while the navigator is showing and nothing else has focus.
document.addEventListener('keydown', e => {
  if (S.view !== 'navigator' || e.metaKey || e.ctrlKey || e.altKey) return;
  if (/INPUT|TEXTAREA|SELECT/.test(document.activeElement?.tagName) || document.activeElement?.closest?.('.xterm')) return;
  if (S.drawerOpen || document.querySelector('.modal-backdrop')) return;
  const N = S.nv, rows = N.rows || [], r = rows[N.sel];
  const move = d => { N.sel = Math.max(0, Math.min(rows.length - 1, N.sel + d)); document.querySelectorAll('[data-nvrow]').forEach(tr => tr.classList.toggle('sel', +tr.dataset.nvrow === N.sel)); document.querySelector('tr.sel')?.scrollIntoView({block: 'nearest'}); const st = document.querySelector('.nv-status span'); if (st && rows[N.sel]) st.textContent = rows[N.sel].name + (rows[N.sel].namespace ? ' · ' + rows[N.sel].namespace : ''); };
  const k = e.key;
  if (k === 'j' || k === 'ArrowDown') { move(1); e.preventDefault(); }
  else if (k === 'k' || k === 'ArrowUp') { move(-1); e.preventDefault(); }
  else if (k === 'g') move(-rows.length);
  else if (k === 'G') move(rows.length);
  else if (k === 'Enter' && r && !(N.resource === 'namespaces' && !N.group)) openObject(nvRef(r));
  else if (k === 'y' && r) openObject(nvRef(r), 'yaml');
  else if ((k === 'l' || k === 'p') && r && N.resource === 'pods' && !N.group) openLogs({pod: r.name, namespace: r.namespace, stream: k === 'p' ? 'previous' : ''});
  else if ((k === 'l' || k === 'p') && r && workloadKinds.includes(r.kind)) openLogs({workload: `${r.kind}/${r.name}`, namespace: r.namespace, stream: k === 'p' ? 'previous' : ''});
  else if (k === 'Enter' && r && N.resource === 'namespaces' && !N.group) { N.ns = r.name; N.resource = 'pods'; N.sel = 0; render(); }
  else if (k === 'd' && r) openObject(nvRef(r), 'events');
  else if (k === 's') openK9s(N.resource + (N.group ? '.' + N.group : ''), N.ns);
  else if (k === ':') { N.cmd = true; render(); e.preventDefault(); }
  else if (k === '/') { $('#nv-q')?.focus(); e.preventDefault(); }
  else if (/^[0-9]$/.test(k)) { const cs = S.ws?.clusters || []; if (k === '0') setCluster(''); else if (cs[+k - 1]) setCluster(cs[+k - 1].name); }
  else if (k === 'Escape') { if (N.q) { N.q = ''; render(); } }
});

// ---------- best practices ----------
const pstColour = n => n >= 90 ? 'var(--teal)' : n >= 75 ? '#7fc97f' : n >= 60 ? 'var(--amber)' : 'var(--red)';
const feature = name => !!(S.ws && S.ws.features && S.ws.features[name]);
// Severity tag: coloured by weight with the Enterprise priority view, plain otherwise.
const pstSev = sev => feature('priorities') ? `<span class="pst-sev pst-sev-${esc(sev)}">${esc(sev)}</span>` : `<span class="tag">${esc(sev)}</span>`;
const pstPts = p => p >= 1 ? '+' + Math.round(p) : p > 0 ? '+<1' : '';
const pstBadge = st => `<span class="pst-badge pst-${st}">${{fail: 'FAIL', warn: 'WARN', pass: 'PASS', na: 'N/A'}[st]}</span>`;

// The filter box takes free text and key:value terms. Values match as a
// case-insensitive prefix; kind: and ns: match any object a check looked at.
const pstFields = {area: c => [c.area], status: c => [c.status], severity: c => [c.severity], sev: c => [c.severity], category: c => [c.category],
  kind: c => c.items.map(i => i.kind), ns: c => c.items.map(i => i.namespace), namespace: c => c.items.map(i => i.namespace), id: c => [c.id]};
const pstKeys = [
  {value: 'status', hint: 'fail, warn, pass or na', example: 'status:fail'},
  {value: 'severity', hint: 'critical, high, medium or low', example: 'severity:critical'},
  {value: 'area', hint: 'which part of the stack', example: 'area:agentic'},
  {value: 'category', hint: 'group of checks', example: 'category:identity'},
  {value: 'kind', hint: 'kind of object checked', example: 'kind:AgentgatewayPolicy'},
  {value: 'ns', hint: 'namespace of an object checked', example: 'ns:kagent'},
  {value: 'id', hint: 'check identifier', example: 'id:ag-'},
];
function pstMatch(c, terms) {
  return terms.every(t => {
    let ok;
    if (t.key && pstFields[t.key]) { const w = t.val.toLowerCase(); ok = pstFields[t.key](c).some(v => String(v || '').toLowerCase().startsWith(w)); }
    else ok = (c.title + ' ' + c.id + ' ' + c.category + ' ' + c.items.map(i => i.name + ' ' + i.detail).join(' ')).toLowerCase().includes((t.text ?? `${t.key}:${t.val}`).toLowerCase());
    return t.neg ? !ok : ok;
  });
}

async function posture() {
  const P = S.pst;
  const r = await api('api/posture');
  S.pstData = r;
  const shortName = {agentic: 'Agentic', ambient: 'Istio', gateway: 'Ingress', kubernetes: 'Kubernetes'};
  const present = r.areas.filter(a => a.present && a.grade), absent = r.areas.filter(a => !a.present || !a.grade);
  const card = a => {
    const cats = Object.entries(a.categories || {}).sort((x, y) => x[1] - y[1]);
    const c = a.counts || {};
    return `<section class="panel pst-card ${P.area === a.area ? 'on' : ''}" data-pstarea="${esc(a.area)}" title="Show only ${esc(a.title)}"><div class="panel-body">
      <div class="row" style="gap:16px;align-items:center"><div class="pst-ring" style="--pct:${a.score};--col:${pstColour(a.score)}"><div><strong>${a.score}</strong><span>${esc(a.grade || '')}</span></div></div>
      <div style="min-width:0"><h2 style="font-size:15px">${esc(a.title)}</h2><p class="muted" style="margin:4px 0 0;font-size:12px"><span class="red">${c.fail || 0} failing</span> · <span class="amber">${c.warn || 0} warnings</span> · ${c.pass || 0} passing · ${c.na || 0} n/a</p></div></div>
      <div class="mt">${cats.map(([k, v]) => `<div class="pst-cat"><span>${esc(k)}</span><div class="pst-bar"><i style="width:${v}%;background:${pstColour(v)}"></i></div><small>${v}</small></div>`).join('')}</div></div></section>`;
  };
  const tot = {fail: 0, warn: 0, pass: 0, na: 0};
  r.checks.forEach(c => tot[c.status]++);
  const objs = r.checks.reduce((n, c) => n + c.total, 0);
  const coverage = `<div class="notice info pst-coverage"><div><strong>Scanned ${r.checks.length} best practices</strong> across ${present.length} area${present.length === 1 ? '' : 's'}, evaluating ${objs} object${objs === 1 ? '' : 's'}: <span class="red">${tot.fail} failing</span>, <span class="amber">${tot.warn} with warnings</span>, ${tot.pass} passing, ${tot.na} not applicable to this capture.
    ${absent.length ? `<br><small class="muted">Not scored: ${absent.map(a => `${esc(a.title)} (${esc(a.reason)})`).join('; ')}.</small>` : ''}</div></div>`;
  const terms = qTerms(P.q);
  const typedStatus = terms.some(t => t.key === 'status'); // a status: term overrides the status chips
  const checks = r.checks.filter(c => (!P.area || c.area === P.area) &&
    (typedStatus || P.status === 'all' || (P.status === 'attention' ? (c.status === 'fail' || c.status === 'warn') : c.status === P.status)) &&
    pstMatch(c, terms));
  const chip = (field, v, label) => `<button class="chip ${P[field] === v && !(field === 'status' && typedStatus) ? 'on' : ''}" data-pst="${attr({field, v})}">${label}</button>`;
  const row = c => {
    const open = P.open === c.id;
    const items = c.items.filter(i => i.status !== 'na');
    return `<div class="pst-check ${open ? 'open' : ''}"><button class="pst-head" data-pstopen="${esc(c.id)}">${pstBadge(c.status)}${pstSev(c.severity)}<strong>${esc(c.title)}</strong><span class="muted">${esc(shortName[c.area] || c.area)} · ${esc(c.category)}</span><span style="flex:1"></span>${c.points ? `<span class="pst-pts" title="Area score points recovered if every object passed">${pstPts(c.points)} pts</span>` : ''}${c.status === 'na' ? `<small class="muted">${esc(c.reason)}</small>` : `<small>${c.passed}/${c.total} pass</small>`}<span class="muted">${open ? '▾' : '▸'}</span></button>
      ${open ? `<div class="pst-body"><p>${esc(c.why)}</p>
        ${items.length ? `<div class="table-scroll mt" style="max-height:40vh"><table><thead><tr><th></th><th>Object</th><th>What we found</th></tr></thead><tbody>${items.map((i, n) => `<tr><td>${pstBadge(i.status)}</td><td class="mono">${i.name ? `<button class="link" data-pstobj="${attr({c: c.id, n})}">${esc(i.kind)} ${esc(i.namespace ? i.namespace + '/' : '')}${esc(i.name)}</button>` : '<span class="muted">cluster</span>'}</td><td style="white-space:normal">${esc(i.detail)}</td></tr>`).join('')}</tbody></table></div>` : ''}
        ${c.status === 'fail' || c.status === 'warn' || c.status === 'na' ? `<div class="row between mt"><h3>How to fix</h3><div class="row"><button data-pstcopy="${esc(c.id)}">Copy YAML</button><a href="${esc(c.source)}" target="_blank" rel="noopener">Read the guide ↗</a></div></div><pre class="code mt">${esc(c.fix)}</pre>` : `<p class="mt"><a href="${esc(c.source)}" target="_blank" rel="noopener">Read the guide ↗</a></p>`}
      </div>` : ''}</div>`;
  };
  const pri = (r.priorities || []).filter(p => !P.area || p.area === P.area);
  const fixFirst = !feature('priorities') || !pri.length ? '' : `<section class="panel pst-first"><div class="panel-head"><h2>Fix these first</h2><small class="muted">Ranked by severity and how much of it fails. +N is the area score it gives back <span class="ent-pill">Enterprise</span></small></div>
    <div class="pst-first-list">${pri.map(p => `<button class="pst-first-row" data-pstgo="${esc(p.id)}"><span class="pst-rank">${p.rank}</span><span class="pst-pts big">${pstPts(p.points)}</span>${pstBadge(p.status)}${pstSev(p.severity)}
      <span class="pst-first-main"><strong>${esc(p.title)}</strong><small class="muted">${esc(shortName[p.area] || p.area)} · ${p.failing ? p.failing + ' failing' : ''}${p.failing && p.warning ? ', ' : ''}${p.warning ? p.warning + ' warning' : ''} of ${p.total}: <span class="mono">${esc(p.objects.join(', '))}</span></small></span><span class="muted">▸</span></button>`).join('')}</div></section>`;
  let lastArea = '';
  const rows = checks.map(c => { const h = !P.area && c.area !== lastArea ? `<div class="tl-day">${esc((r.areas.find(a => a.area === c.area) || {}).title || c.area)}</div>` : ''; lastArea = c.area; return h + row(c); }).join('');
  return head('Best practices', "Solo's guidance for agentic workloads, Istio, ingress and Kubernetes, checked against this snapshot. Problems first, each with its evidence and fix.", '<button id="pst-term">&gt;_ fbscore in terminal</button>') + `
    ${coverage}
    ${fixFirst}
    <div class="pst-cards">${present.map(card).join('')}</div>
    <div class="toolbar mt"><div class="row wrap">${chip('area', '', 'All areas')}${present.map(a => chip('area', a.area, shortName[a.area] || a.title)).join('')}<span class="muted">·</span>${chip('status', 'attention', 'Needs attention')}${chip('status', 'fail', 'Failing')}${chip('status', 'pass', 'Passing')}${chip('status', 'na', 'Not applicable')}${chip('status', 'all', 'All')}</div>
      <input id="pst-q" placeholder="Filter. Try status: severity: area: category: kind: ns: or text" value="${esc(P.q)}" style="min-width:340px" autocomplete="off" spellcheck="false"></div>
    <section class="panel">${rows || (P.status === 'attention' && !P.q ? '<div class="empty"><strong>Nothing needs attention</strong>Every applicable check passes. Choose Passing or All to see them.</div>' : '<div class="empty"><strong>No checks match</strong>Change the filters above.</div>')}</section>
    <p class="muted mt" style="font-size:12px">Scores weight checks by severity (critical 10, high 6, medium 3, low 1) and give partial credit per object. Checks whose data was not captured, or that have nothing to apply to, are not applicable and do not lower the score.</p>`;
}
after.posture = () => {
  const P = S.pst;
  document.querySelectorAll('[data-pst]').forEach(b => b.onclick = () => { const f = JSON.parse(b.dataset.pst); P[f.field] = f.v; render(); });
  document.querySelectorAll('[data-pstgo]').forEach(b => b.onclick = () => {
    const id = b.dataset.pstgo; P.open = id; P.status = 'attention'; P.q = '';
    render().then(() => { const el = document.querySelector(`[data-pstopen="${CSS.escape(id)}"]`); if (el) el.scrollIntoView({block: 'start', behavior: 'smooth'}); });
  });
  document.querySelectorAll('[data-pstarea]').forEach(b => b.onclick = () => { P.area = P.area === b.dataset.pstarea ? '' : b.dataset.pstarea; render(); });
  document.querySelectorAll('[data-pstopen]').forEach(b => b.onclick = () => { P.open = P.open === b.dataset.pstopen ? null : b.dataset.pstopen; render(); });
  document.querySelectorAll('[data-pstobj]').forEach(b => b.onclick = () => { const x = JSON.parse(b.dataset.pstobj); const it = S.pstData.checks.find(c => c.id === x.c).items.filter(i => i.status !== 'na')[x.n]; openObject({cluster: it.cluster, group: it.group, resource: it.resource, namespace: it.namespace, name: it.name}); });
  document.querySelectorAll('[data-pstcopy]').forEach(b => b.onclick = () => { const c = S.pstData.checks.find(x => x.id === b.dataset.pstcopy); navigator.clipboard.writeText(c.fix).then(() => toast('Fix copied'), () => toast('Clipboard unavailable')); });
  let t; $('#pst-q').oninput = e => { clearTimeout(t); t = setTimeout(() => { const c = e.target.selectionStart, v = e.target.value; P.q = v; render().then(() => refocus('#pst-q', c, v)); }, 250); };
  typeahead($('#pst-q'), {keys: pstKeys, alias: {sev: 'severity', namespace: 'ns'}, values: (f, p, rest) => {
    const terms = qTerms(rest), checks = (S.pstData?.checks || []).filter(c => pstMatch(c, terms)), get = pstFields[f];
    if (!get) return [];
    const hints = {fail: 'failing', warn: 'warning', pass: 'passing', na: 'not applicable'};
    return countValues(checks.flatMap(get), p, f === 'status' ? v => hints[v] : null);
  }});
  $('#pst-term').onclick = () => termRun('fbscore');
};

// ---------- search ----------
const objAlias = {type: 'kind', k: 'kind', namespace: 'ns', l: 'label'};
function hl(text, q) {
  // Quoted phrases highlight as a whole; key:value filters and -exclusions do not.
  const words = ((q || '').match(/-?"[^"]+"|\S+/g) || []).filter(w => !w.startsWith('-') && (w.startsWith('"') || !/^\w+:/.test(w))).map(w => w.replace(/"/g, '')).filter(Boolean);
  let out = esc(text);
  for (const w of words) out = out.replace(new RegExp(w.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'), 'gi'), m => `<mark>${m}</mark>`);
  return out;
}

function whyMatched(h, q) {
  if (h.snippet) return `<span class="mono muted" style="font-size:11px;word-break:break-all">…${hl(h.snippet, q)}…</span>`;
  if (h.match === 'label') {
    const words = q.toLowerCase().split(/\s+/).filter(w => w && !w.includes(':')).map(w => w.replace(/"/g, ''));
    const hits = Object.entries(h.labels || {}).filter(([k, v]) => words.some(w => (k + '=' + v).toLowerCase().includes(w)));
    return hits.slice(0, 3).map(([k, v]) => `<span class="tag">${hl(k + '=' + v, q)}</span>`).join(' ');
  }
  return `<small class="muted">${esc(h.match === 'filter' ? 'matches the filters' : h.match)}</small>`;
}

async function search() {
  const Q = S.search;
  const examples = ['kind:pod', 'CrashLoopBackOff', 'kind:httproute', 'label:app', 'kind:deployment ns:istio-system', '"image"'];
  const bar = `<form class="query" id="obj-form"><span class="green">⌕</span><input id="obj-q" value="${esc(Q.q)}" placeholder='Search every object. Try kind: ns: label: name: or free text, e.g. kind:pod label:app=checkout' autocomplete="off" spellcheck="false" aria-label="Object search"><button class="primary">Search ↵</button></form>
    <div class="help-row"><span>Try</span>${examples.map(x => `<button data-otry="${esc(x)}">${esc(x)}</button>`).join('')}</div>`;
  const header = head('Search', 'Every captured object of every kind: names, namespaces, labels, and anything in spec and status (images, hostnames, status reasons).');
  if (!Q.q.trim()) {
    const types = await api('api/resources', {cluster: S.cluster});
    const {groups, hidden} = groupKinds(types);
    return header + bar + `<section class="panel mt"><div class="panel-head"><h2>What was captured</h2><small>${groups.reduce((n, [, g]) => n + g.length, 0)} kinds with objects · click one to list it</small></div><div class="panel-body">
      ${groups.length ? `<div class="kind-groups">${groups.map(([area, ts]) => `<div class="kind-group"><h3>${esc(area)}</h3>${ts.map(t => `<button class="kind-row" data-okind="${esc(t.name)}"><span>${kindLabel(t)}</span><small>${num(t.count)}</small></button>`).join('')}</div>`).join('')}</div>` : '<span class="muted">No objects loaded yet.</span>'}
      ${hidden ? `<p class="muted mt" style="font-size:12px">${hidden} other resource types have no captured objects and are not shown. <button class="link" id="go-cov">See coverage</button> for types that were excluded or failed.</p>` : ''}</div></section>
      <div class="notice info mt"><div><strong>Syntax</strong>: <code>kind:pod</code> (or po, deploy, svc, httproute) · <code>ns:payments</code> · <code>name:checkout*</code> · <code>label:app=checkout</code> · <code>"free text"</code> in names, labels, spec and status · <code>-</code> to exclude, e.g. <code>-kind:event</code>. Space means AND.</div></div>`;
  }
  const r = await api('api/objects/search', {q: Q.q, cluster: S.cluster, limit: Q.limit}).catch(e => ({error: e.message, results: [], byKind: {}, total: 0}));
  S.lastObj = r;
  const kinds = Object.entries(r.byKind || {}).sort((a, b) => b[1] - a[1]);
  return header + bar + (r.error ? `<div class="notice bad">${esc(r.error)}</div>` : '') + `
    <div class="chips">${kinds.map(([k, n]) => `<button class="chip" data-okind="${esc(k)}">${esc(k)} <small>${n}</small></button>`).join('')}</div>
    <section class="panel"><div class="panel-head"><span><strong>${num(r.total)}</strong> <span class="muted">objects${r.loading ? ' · <span class="blue">bundle still loading</span>' : ''}</span></span>
      <button class="link" id="obj-logs">Search the logs for this instead →</button></div>
    <div class="table-scroll" style="max-height:66vh">${r.results.length ? `<table><thead><tr><th>Kind</th><th>Namespace</th><th>Name</th>${!S.cluster ? '<th>Cluster</th>' : ''}<th>Why it matched</th></tr></thead><tbody>
      ${r.results.map((h, i) => `<tr data-ohit="${i}" style="cursor:pointer"><td><span class="badge">${esc(h.kind)}</span></td><td class="muted">${esc(h.namespace || '—')}</td><td class="mono blue">${hl(h.name, Q.q)}</td>${!S.cluster ? `<td><span class="tag">${esc(h.cluster)}</span></td>` : ''}
        <td style="white-space:normal;min-width:260px;max-width:560px">${whyMatched(h, Q.q)}</td></tr>`).join('')}
    </tbody></table>` : `<div class="empty"><strong>${esc(r.message || 'No matches')}</strong></div>`}</div>
    ${r.total > r.results.length ? `<div class="table-foot"><span>Showing ${r.results.length} of ${num(r.total)}</span><button id="obj-more">Show more</button></div>` : ''}</section>`;
}
after.search = () => {
  const Q = S.search;
  const go = (q, keep) => { Q.q = q; Q.limit = 200; history.replaceState(null, '', '#search' + (q ? '?q=' + encodeURIComponent(q) : '')); render().then(() => { if (keep) refocus('#obj-q', keep.caret, keep.value); }); };
  typeahead($('#obj-q'), {scope: 'objects', alias: objAlias, bareField: 'name', onAccept: i => go(i.value.trim(), {value: i.value, caret: i.selectionStart})});
  $('#obj-form').onsubmit = e => { e.preventDefault(); go($('#obj-q').value.trim()); };
  document.querySelectorAll('[data-otry]').forEach(b => b.onclick = () => go(b.dataset.otry));
  document.querySelectorAll('[data-okind]').forEach(b => b.onclick = () => go(`kind:${b.dataset.okind.toLowerCase()} ${Q.q.replace(/-?kind:\S+/g, '')}`.trim()));
  const gc = $('#go-cov'); if (gc) gc.onclick = () => nav('coverage');
  const more = $('#obj-more'); if (more) more.onclick = () => { Q.limit += 500; render(); };
  const lg = $('#obj-logs'); if (lg) lg.onclick = () => openLogs({q: Q.q.split(/\s+/).filter(w => !/^-?(kind|label|name|ns|namespace|cluster):/.test(w)).join(' ')});
  document.querySelectorAll('[data-ohit]').forEach(tr => tr.onclick = () => {
    const h = S.lastObj.results[+tr.dataset.ohit];
    openObject({cluster: h.cluster, group: h.group, resource: h.resource, namespace: h.namespace, name: h.name});
  });
  const i = $('#obj-q'); if (i && !Q.q) i.focus();
};

// ---------- resources ----------
async function resources() {
  const allTypes = await api('api/resources', {cluster: S.cluster});
  const {groups, hidden} = groupKinds(allTypes);
  // Only types with captured objects, unless one was opened explicitly (e.g. from a finding).
  const types = groups.flatMap(([, g]) => g);
  const explicit = allTypes.find(t => t.resource === S.res.resource && t.group === S.res.group && t.count === 0);
  if (explicit) types.push(explicit);
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
  return head('Resources', 'Browse captured objects. Click a row for YAML, relationships, events and logs.', `<button id="res-k9s" title="k9s on this type and namespace, in the terminal">Open in k9s</button><button id="res-term">&gt;_ kubectl get ${esc(cur.resource)}</button>`) + `
  <div class="resource-layout"><nav class="types" aria-label="Resource types">${groups.map(([area, ts]) => `<h3 style="margin:12px 8px 4px;font-size:10px;letter-spacing:.08em;text-transform:uppercase" class="muted">${esc(area)}</h3>` + ts.map(t => `<button class="${t.resource === cur.resource && t.group === cur.group ? 'active' : ''}" data-type="${attr({g: t.group, r: t.resource})}" title="${esc(t.name)} · ${esc(t.state)}"><span>${kindLabel(t)}</span><span><small>${t.count}</small> <span class="st state-${esc(t.state)}">●</span></span></button>`).join('')).join('')}
    ${hidden ? `<p class="muted" style="font-size:11px;margin:12px 8px">${hidden} types with no captured objects are hidden. <button class="link" id="res-cov">Coverage</button> shows which were excluded or failed.</p>` : ''}</nav>
  <div><div class="toolbar"><select id="res-ns" aria-label="Namespace"><option value="">All namespaces</option>${S.res.nsOptions.map(n => `<option ${n === S.res.ns ? 'selected' : ''}>${esc(n)}</option>`).join('')}</select>
    <input id="res-q" placeholder="Filter by name, status or label: (press /)" value="${esc(S.res.q)}" aria-label="Filter" autocomplete="off" spellcheck="false"><button id="res-reset">Reset</button></div>
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
  let t; $('#res-q').oninput = e => { clearTimeout(t); t = setTimeout(() => { const c = e.target.selectionStart, v = e.target.value; S.res.q = v; render().then(() => refocus('#res-q', c, v)); }, 250); };
  typeahead($('#res-q'), {scope: 'objects', keys: labelKey, alias: {l: 'label'}, extraQ: () => `kind:${S.res.resource}${S.res.group ? '.' + S.res.group : ''}${S.res.ns ? ' ns:' + S.res.ns : ''}`});
  $('#res-reset').onclick = () => { S.res.ns = ''; S.res.q = ''; render(); };
  const rc = $('#res-cov'); if (rc) rc.onclick = () => nav('coverage');
  $('#res-k9s').onclick = () => openK9s(S.res.resource + (S.res.group ? '.' + S.res.group : ''), S.res.ns);
  $('#res-term').onclick = () => termRun(`kubectl get ${S.res.resource}${S.res.group ? '.' + S.res.group : ''} ${S.res.ns ? '-n ' + S.res.ns : '-A'}`);
};

// ---------- object drawer ----------
const workloadKinds = ['Deployment', 'DaemonSet', 'StatefulSet', 'ReplicaSet', 'Job', 'CronJob'];
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
          <div class="row wrap mt">${isPod ? `<button class="primary" id="d-logs">Open pod logs →</button>` : ''}${workloadKinds.includes(o.kind) ? `<button class="primary" id="d-wlogs">Open logs of all its pods →</button>` : ''}<button id="d-describe">&gt;_ kubectl describe</button>${(S.ws?.clusters || []).length > 1 ? '<button id="d-compare">Compare across clusters</button>' : ''}</div>`;
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
    const dw = $('#d-wlogs'); if (dw) dw.onclick = () => openLogs({workload: `${o.kind}/${o.name}`, namespace: o.namespace});
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
  return Object.assign({q: L.q, cluster: S.cluster, namespace: L.namespace, workload: L.workload, pod: L.pod, container: L.container, level: L.level, stream: L.stream, from: L.from, to: L.to, limit: L.limit}, extra);
}

const logExamples = [
  ['Errors', 'level:error'],
  ['Warnings and errors', 'level:warn OR level:error'],
  ['Crashed containers', 'stream:previous'],
  ['HTTP 5xx', 'status:5*'],
  ['Connection problems', '"connection refused" OR "connection reset" OR "timed out"'],
  ['Certificates', 'certificate OR cert'],
  ['istiod pushes', 'pod:istiod* push'],
  ['Denied / forbidden', 'denied OR forbidden OR 403'],
];
const quickRanges = [['All', 0], ['Last 5 min', 5], ['Last 15 min', 15], ['Last hour', 60], ['Last 6 hours', 360], ['Last 24 hours', 1440]];
const toLocalInput = iso => iso ? new Date(iso).toISOString().slice(0, 19) : '';
const fromLocalInput = v => v ? new Date(v + 'Z').toISOString() : '';
const fmtAxis = (t, span) => { const d = new Date(t).toISOString(); return span > 864e5 ? d.slice(5, 16).replace('T', ' ') : span > 6e4 ? d.slice(11, 19) : d.slice(11, 23); };

function addTerm(term) {
  const L = S.logs;
  const q = (L.q || '').trim();
  if ((' ' + q + ' ').includes(' ' + term + ' ')) return;
  L.q = (q ? q + ' ' : '') + term; L.expanded = null; render();
}
function fieldTerm(k, v) { return /[\s"]/.test(v) ? `${k}:"${v.replace(/"/g, '')}"` : `${k}:${v}`; }

async function logs() {
  const L = S.logs;
  let res;
  try { res = await api('api/logs/search', logParams()); } catch (e) { res = {error: e.message, records: [], facets: {}, histogram: [], total: 0}; }
  const fields = await api('api/logs/fields', {q: L.q, cluster: S.cluster}).catch(() => []);
  L.result = res; L.total = res.total; L.rev = res.revision;
  if (!L.from && !L.to && res.from && !res.from.startsWith('0001')) L.bounds = {from: res.from, to: res.to};
  const facet = (key, field, label, extra = '') => {
    let vals = Object.entries(res.facets?.[key] || {}).sort((a, b) => b[1] - a[1]);
    if (key === 'pod' && L.podFilter) vals = vals.filter(([v]) => v.toLowerCase().includes(L.podFilter.toLowerCase()));
    vals = vals.slice(0, key === 'pod' ? 40 : 12);
    if (!vals.length && !extra) return '';
    return `<h3>${label}</h3>${extra}` + vals.map(([v, n]) => `<button class="facet ${L[field] === v ? 'active' : ''}" data-facet="${attr({field, v})}"><span></span><span>${esc(v || '—')}</span><small>${num(n)}</small></button>`).join('');
  };
  const workloadFacet = (() => {
    const vals = Object.entries(res.facets?.workload || {}).filter(([v]) => v && v !== '/').sort((a, b) => b[1] - a[1]).slice(0, 30);
    if (!vals.length) return '';
    return `<h3>Workload <small class="muted" style="text-transform:none;letter-spacing:0">all pods of a controller</small></h3>` + vals.map(([v, n]) => { const [k, name] = v.split('/'); return `<button class="facet ${L.workload === v ? 'active' : ''}" data-facet="${attr({field: 'workload', v})}" title="${esc(v)}"><span></span><span><span class="tag" style="font-size:9px;padding:1px 4px">${esc(k.replace('Deployment', 'deploy').replace('DaemonSet', 'ds').replace('StatefulSet', 'sts').replace('StaticPod', 'static'))}</span> ${esc(name)}</span><small>${num(n)}</small></button>`; }).join('');
  })();
  const podPicker = Object.keys(res.facets?.pod || {}).length > 8 ? `<input id="pod-filter" placeholder="Find a pod…" value="${esc(L.podFilter || '')}" style="width:100%;margin:0 0 6px;font-size:12px">` : '';
  const fieldList = fields.length ? `<h3>Fields <small class="muted" style="text-transform:none;letter-spacing:0">click a value to filter</small></h3>` + fields.map(f => `<details class="field" ${L.openField === f.field ? 'open' : ''} data-fieldname="${esc(f.field)}"><summary><span class="mono">${esc(f.field)}</span><small>${num(f.count)}</small></summary>
      ${f.values.map(v => `<button class="facet" data-addterm="${attr(fieldTerm(f.field, v.value))}" title="${esc(f.field)}:${esc(v.value)}"><span></span><span class="mono" style="font-size:11px">${esc(v.value || '""')}</span><small>${num(v.count)}</small></button>`).join('')}</details>`).join('') : '';
  const chips = ['namespace', 'workload', 'pod', 'container', 'level', 'stream'].filter(k => L[k]).map(k => `<button class="chip" data-clear="${k}">${k}: ${esc(L[k])} ×</button>`).join('');
  const hist = res.histogram || [];
  const max = Math.max(1, ...hist.map(b => b.count));
  const span = res.to && res.from ? new Date(res.to) - new Date(res.from) : 0;
  const bars = hist.map((b, i) => `<div data-bucket="${i}" style="height:${b.count ? Math.max(3, b.count / max * 96) : 0}px" title="${fmtDateTime(b.start)} · ${num(b.count)} lines"></div>`).join('');
  const axis = hist.length ? `<div class="hist-axis"><span>${fmtAxis(res.from, span)}</span><span>${fmtAxis(new Date((new Date(res.from).getTime() + new Date(res.to).getTime()) / 2).toISOString(), span)}</span><span>${fmtAxis(res.to, span)}</span></div>` : '';
  const zoomed = !!(L.from || L.to);
  const rows = res.records.map(r => `<div class="log-row ${r.level === 'error' ? 'error' : r.level === 'warn' ? 'warn' : ''}" data-log="${r.id}" tabindex="0">
      <span class="muted">${fmtTime(r.ts)}</span><span class="${r.level === 'error' ? 'red' : r.level === 'warn' ? 'amber' : 'green'}">${esc(r.level.toUpperCase())}</span>
      <span class="pod" title="${esc(r.cluster)} ${esc(r.namespace)}/${esc(r.pod)}">${esc(r.pod)}<br><span class="muted">${esc(r.container)}${r.stream === 'previous' ? ' · previous' : ''}</span></span>
      <span class="msg">${esc(r.msg)}${Object.entries(r.fields || {}).filter(([k]) => /trace|request_id|connection/.test(k)).map(([k, v]) => ` <span class="blue">${esc(k)}=${esc(v)}</span>`).join('')}</span></div>${L.expanded === r.id ? '<div id="log-detail"></div>' : ''}`).join('');
  return head('Log explorer', 'Search every captured log line. Drag across the chart to zoom into a time range; pinch or scroll over it to zoom around the cursor.', `<label class="row"><input type="checkbox" id="follow" ${L.follow ? 'checked' : ''}> Follow incoming data</label><button id="log-term">&gt;_ fbq in terminal</button>`) + `
  <div class="log-layout"><aside class="facets" aria-label="Filters">${facet('level', 'level', 'Level')}${facet('namespace', 'namespace', 'Namespace')}${workloadFacet}${facet('pod', 'pod', 'Pod', podPicker)}${facet('container', 'container', 'Container')}${facet('stream', 'stream', 'Stream')}${!S.cluster ? facet('cluster', 'cluster_', 'Cluster') : ''}
    ${fieldList}
    <h3>Syntax</h3><div class="muted mono" style="font-size:10px;line-height:1.8">level:error<br>ns:payments pod:checkout*<br>trace_id:a4e98f02<br>"connection reset"<br>-healthz · a OR b<br>status:5* · since:15m</div></aside>
  <section><form class="query" id="log-form"><span class="green">⌕</span><input id="log-q" value="${esc(L.q)}" placeholder='Search logs. Try level: ns: pod: container: since: or "free text"' autocomplete="off" spellcheck="false" aria-label="Log query"><button class="primary">Search ↵</button></form>
    <div class="help-row"><span>Examples</span>${logExamples.map(([t, q]) => `<button data-try="${esc(q)}" title="${esc(q)}">${esc(t)}</button>`).join('')}</div>
    ${res.error ? `<div class="notice bad">${esc(res.error)}</div>` : ''}
    <div class="time-bar"><select id="quick-range" aria-label="Quick range">${quickRanges.map(([t, m]) => `<option value="${m}">${t}${m ? ' of the capture' : ' of the capture'}</option>`).join('')}<option value="custom" ${zoomed ? 'selected' : ''}>Custom range</option></select>
      <input type="datetime-local" step="1" id="t-from" value="${toLocalInput(L.from || res.from)}" aria-label="From (UTC)"><span class="muted">→</span><input type="datetime-local" step="1" id="t-to" value="${toLocalInput(L.to || res.to)}" aria-label="To (UTC)"><small class="muted">UTC</small>
      <button id="t-apply">Apply</button>${zoomed ? '<button id="t-out">Zoom out</button><button id="t-reset">Whole capture</button>' : ''}</div>
    <div class="chips">${chips}</div>
    <div id="new-lines"></div>
    <div class="histogram"><div class="row between"><small>${num(res.total)} lines${zoomed ? ' in the selected range' : ''}</small><small class="muted">Drag to zoom · pinch or scroll to zoom · double-click to zoom out</small></div>
      <div class="bars-wrap" id="bars-wrap"><div class="bars" id="bars">${bars}</div><div class="brush hidden" id="brush"></div></div>${axis}</div>
    <div class="panel"><div class="panel-head"><span><strong>${num(res.total)}</strong> <span class="muted">matching lines · ${num(res.scanned)} searched${res.loading ? ' · <span class="blue">bundle still loading</span>' : ''}</span></span><small>Newest first · click a line for context</small></div>
    <div class="table-scroll" style="max-height:60vh"><div class="log-table"><div class="log-row log-head"><span>TIME (UTC)</span><span>LEVEL</span><span>POD / CONTAINER</span><span>MESSAGE</span></div>
    ${rows || `<div class="empty"><strong>${esc(res.message || 'No matches')}</strong>${res.loading ? 'Log batches are still arriving; results update as they load.' : zoomed ? 'Nothing in this time range. Zoom out or pick another range.' : 'Remove a filter or broaden the search.'}</div>`}</div></div>
    ${res.total > res.records.length ? `<div class="table-foot"><span>Showing ${res.records.length} of ${num(res.total)}</span><button id="more">Show more</button></div>` : ''}</div>
  </section></div>`;
}

function setRange(from, to) {
  const L = S.logs;
  const b = L.bounds;
  if (b && from && to && new Date(from) <= new Date(b.from) && new Date(to) >= new Date(b.to)) { L.from = L.to = ''; }
  else { L.from = from; L.to = to; }
  L.expanded = null; L.limit = 200; render();
}

after.logs = () => {
  const L = S.logs;
  $('#log-form').onsubmit = e => { e.preventDefault(); L.q = $('#log-q').value; L.expanded = null; L.limit = 200; render(); };
  typeahead($('#log-q'), {scope: 'logs', alias: {namespace: 'ns', deploy: 'workload', deployment: 'workload'}, onAccept: i => {
    const keep = {value: i.value, caret: i.selectionStart};
    L.q = i.value.trim(); L.expanded = null; L.limit = 200; render().then(() => refocus('#log-q', keep.caret, keep.value));
  }});
  document.querySelectorAll('[data-try]').forEach(b => b.onclick = () => { L.q = b.dataset.try; L.expanded = null; render(); });
  document.querySelectorAll('[data-facet]').forEach(b => b.onclick = () => { const f = JSON.parse(b.dataset.facet); if (f.field === 'cluster_') { setCluster(f.v); return; } L[f.field] = L[f.field] === f.v ? '' : f.v; render(); });
  document.querySelectorAll('[data-addterm]').forEach(b => b.onclick = () => addTerm(JSON.parse(b.dataset.addterm)));
  document.querySelectorAll('details.field').forEach(d => d.ontoggle = () => { if (d.open) L.openField = d.dataset.fieldname; });
  document.querySelectorAll('[data-clear]').forEach(b => b.onclick = () => { L[b.dataset.clear] = ''; render(); });
  document.querySelectorAll('[data-log]').forEach(el => { const go = () => expandLog(Number(el.dataset.log)); el.onclick = go; el.onkeydown = e => { if (e.key === 'Enter') go(); }; });
  const pf = $('#pod-filter'); if (pf) { let t; pf.oninput = e => { clearTimeout(t); t = setTimeout(() => { L.podFilter = e.target.value; render().then(() => { const i = $('#pod-filter'); if (i) { i.focus(); i.setSelectionRange(i.value.length, i.value.length); } }); }, 200); }; }
  $('#follow').onchange = e => { L.follow = e.target.checked; if (L.follow) render(); };
  $('#log-term').onclick = () => termRun(`fbq '${(L.q || 'level:error').replace(/'/g, '')}'`);
  const more = $('#more'); if (more) more.onclick = () => { L.limit += 500; render(); };
  // Time controls.
  const res = L.result, b = L.bounds || {from: res.from, to: res.to};
  $('#quick-range').onchange = e => {
    const m = e.target.value; if (m === 'custom') return;
    if (+m === 0) { setRange('', ''); return; }
    const end = new Date(b.to); setRange(new Date(end - m * 6e4).toISOString(), end.toISOString());
  };
  $('#t-apply').onclick = () => setRange(fromLocalInput($('#t-from').value), fromLocalInput($('#t-to').value));
  const out = $('#t-out'); if (out) out.onclick = () => zoomBy(2, 0.5);
  const rs = $('#t-reset'); if (rs) rs.onclick = () => setRange('', '');
  // Histogram: drag to select, pinch/scroll to zoom around the cursor, double-click to zoom out.
  const wrap = $('#bars-wrap'), brush = $('#brush');
  if (!wrap || !res.from || res.from.startsWith('0001')) return;
  const t0 = new Date(res.from).getTime(), t1 = new Date(res.to).getTime();
  const posToTime = x => { const r = wrap.getBoundingClientRect(); return t0 + Math.min(1, Math.max(0, (x - r.left) / r.width)) * (t1 - t0); };
  let startX = null;
  wrap.onpointerdown = e => { startX = e.clientX; wrap.setPointerCapture(e.pointerId); brush.classList.remove('hidden'); brush.style.left = (e.clientX - wrap.getBoundingClientRect().left) + 'px'; brush.style.width = '0px'; e.preventDefault(); };
  wrap.onpointermove = e => {
    if (startX === null) return;
    const r = wrap.getBoundingClientRect(), a = Math.min(startX, e.clientX) - r.left, z = Math.max(startX, e.clientX) - r.left;
    brush.style.left = Math.max(0, a) + 'px'; brush.style.width = Math.min(r.width, z) - Math.max(0, a) + 'px';
    brush.title = `${fmtDateTime(new Date(posToTime(Math.min(startX, e.clientX))).toISOString())} → ${fmtTime(new Date(posToTime(Math.max(startX, e.clientX))).toISOString())}`;
  };
  wrap.onpointerup = e => {
    if (startX === null) return;
    const a = Math.min(startX, e.clientX), z = Math.max(startX, e.clientX); startX = null;
    if (z - a < 4) { brush.classList.add('hidden'); return; } // a click, not a drag
    setRange(new Date(posToTime(a)).toISOString(), new Date(posToTime(z)).toISOString());
  };
  wrap.ondblclick = () => zoomBy(2, 0.5);
  let wheelT, pending = null;
  wrap.onwheel = e => {
    e.preventDefault();
    const r = wrap.getBoundingClientRect(), frac = Math.min(1, Math.max(0, (e.clientX - r.left) / r.width));
    const factor = Math.exp(e.deltaY * (e.ctrlKey ? 0.01 : 0.002)); // ctrlKey = trackpad pinch
    pending = (pending || 1) * factor;
    clearTimeout(wheelT); wheelT = setTimeout(() => { const f = pending; pending = null; zoomBy(f, frac); }, 180);
  };
  function zoomBy(factor, frac) {
    const width = (t1 - t0) * factor, centre = t0 + frac * (t1 - t0);
    let from = centre - frac * width, to = from + width;
    if (b.from) { from = Math.max(from, new Date(b.from).getTime()); to = Math.min(to, new Date(b.to).getTime()); }
    if (to - from < 1000) to = from + 1000;
    setRange(new Date(from).toISOString(), new Date(to).toISOString());
  }
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
// The timeline answers "what happened, in what order": a rollout, then exits,
// then an error spike is the usual shape of an incident. Filters narrow it,
// the density strip shows where things cluster, and dragging on it zooms in.
const tlKinds = [['event', 'Events'], ['termination', 'Container exits'], ['rollout', 'Rollouts'], ['error-spike', 'Error spikes']];
const tlSevColour = {critical: 'var(--red)', warning: 'var(--amber)', info: 'var(--blue, #6aa0ff)'};
// The filter box takes free text and key:value terms (ns:, kind:, severity:,
// reason:, object:, cluster:). Values match as a case-insensitive prefix, and
// a leading - excludes.
const tlReason = i => i.kind === 'event' ? (i.title || '').split(':')[0] : i.kind === 'termination' ? (i.detail || '').split(',')[0] : '';
const tlFields = {ns: i => i.namespace, namespace: i => i.namespace, kind: i => i.kind, type: i => i.kind, severity: i => i.severity, sev: i => i.severity,
  reason: tlReason, object: i => i.object?.kind || (i.kind === 'event' ? ((i.title || '').split(': ')[1] || '').split('/')[0] : ''), cluster: i => i.cluster};
const tlKeys = [
  {value: 'ns', hint: 'namespace', example: 'ns:istio-system'},
  {value: 'kind', hint: 'event, termination, rollout or error-spike', example: 'kind:termination'},
  {value: 'severity', hint: 'critical, warning or info', example: 'severity:warning'},
  {value: 'reason', hint: 'event or exit reason', example: 'reason:BackOff'},
  {value: 'object', hint: 'kind of the object involved', example: 'object:Pod'},
  {value: 'cluster', hint: 'cluster in the bundle', example: 'cluster:mesh1'},
];
function tlMatch(i, terms) {
  return terms.every(t => {
    let ok;
    if (t.key && tlFields[t.key]) ok = String(tlFields[t.key](i) || '').toLowerCase().startsWith(t.val.toLowerCase());
    else ok = (i.title + ' ' + (i.detail || '') + ' ' + (i.namespace || '')).toLowerCase().includes((t.text ?? `${t.key}:${t.val}`).toLowerCase());
    return t.neg ? !ok : ok;
  });
}
function tlFilter(items, T, withTime = true) {
  const terms = qTerms(T.q);
  return items.filter(i => !T.hideKinds.includes(i.kind) && (!T.sev || i.severity === T.sev) && (!T.ns || i.namespace === T.ns) &&
    (!terms.length || tlMatch(i, terms)) &&
    (!withTime || ((!T.from || i.time >= T.from) && (!T.to || i.time <= T.to))));
}
async function timeline() {
  S.tl = S.tl || {q: '', sev: '', ns: '', hideKinds: [], from: '', to: '', limit: 300, newest: true};
  const T = S.tl;
  const items = await api('api/timeline', {cluster: S.cluster});
  S.tlItems = items;
  if (!items.length) return head('Timeline', 'Events, container exits, rollouts and error spikes on one UTC time axis.') +
    `<div class="panel empty"><strong>Nothing on the timeline yet</strong>${S.state?.phase === 'loading' ? 'Events and logs are still loading.' : 'No events, exits or rollouts were captured.'}</div>`;
  const base = tlFilter(items, T, false);
  const shown = tlFilter(items, T);
  // Density strip over the selected window (or everything that passes the other filters).
  const lo = new Date(T.from || (base[0] || items[0]).time).getTime(), hi = new Date(T.to || (base[base.length - 1] || items[items.length - 1]).time).getTime();
  const N = 90, span = Math.max(hi - lo, 60000), w = span / N;
  const buckets = Array.from({length: N}, () => ({critical: 0, warning: 0, info: 0}));
  for (const i of shown) { const b = Math.min(N - 1, Math.max(0, Math.floor((new Date(i.time).getTime() - lo) / w))); buckets[b][i.severity] = (buckets[b][i.severity] || 0) + 1; }
  const max = Math.max(1, ...buckets.map(b => b.critical + b.warning + b.info));
  const bar = (b, n) => { const tot = b.critical + b.warning + b.info; return `<div class="tl-col" title="${new Date(lo + n * w).toISOString().slice(0, 19).replace('T', ' ')} · ${tot}">${['info', 'warning', 'critical'].map(k => b[k] ? `<i style="height:${b[k] / max * 100}%;background:${tlSevColour[k]}"></i>` : '').join('')}</div>`; };
  const namespaces = [...new Set(items.map(i => i.namespace).filter(Boolean))].sort();
  const count = k => base.filter(i => i.kind === k).length;
  const fmt = t => `${fmtDateTime(t).slice(0, 16)}:${new Date(t).toISOString().slice(17, 19)}`;
  let lastDay = '';
  const ordered = T.newest ? [...shown].reverse() : shown;
  S.tlShown = ordered;
  const rows = ordered.slice(0, T.limit).map((i, n) => {
    const day = i.time.slice(0, 10), sep = day !== lastDay ? `<div class="tl-day">${day}</div>` : ''; lastDay = day;
    return sep + `<div class="timeline-item"><time>${fmt(i.time).slice(11)}</time><span class="node ${esc(i.severity)}"></span>
    <div><h3>${esc(i.title)}</h3><p>${esc(i.detail || '')}</p><div class="row" style="margin-top:4px">${(S.ws?.clusters || []).length > 1 ? `<span class="tag">${esc(i.cluster)}</span>` : ''}${i.namespace ? `<button class="tag link" data-tns="${esc(i.namespace)}">${esc(i.namespace)}</button>` : ''}<small>${esc(tlKinds.find(k => k[0] === i.kind)?.[1] || i.kind)}</small></div></div>
    <div class="row">${i.object ? `<button data-tobj="${n}">Inspect →</button>` : ''}<button data-tlog="${n}" title="${i.logQuery ? esc(i.logQuery) : 'Logs two minutes either side'}">${i.logQuery ? 'Logs →' : 'Logs around →'}</button></div></div>`;
  }).join('');
  return head('Timeline', 'What happened, in order: Kubernetes events, container exits, rollouts and error-log spikes on one UTC axis. Use it to see what changed just before things broke.') + `
    <div class="toolbar"><div class="row wrap">${tlKinds.map(([k, t]) => `<button class="chip ${T.hideKinds.includes(k) ? '' : 'on'}" data-tkind="${k}">${t} <small>${count(k)}</small></button>`).join('')}</div>
      <div class="row"><select id="tl-sev"><option value="">All severities</option>${['critical', 'warning', 'info'].map(v => `<option ${T.sev === v ? 'selected' : ''}>${v}</option>`).join('')}</select>
      <select id="tl-ns"><option value="">All namespaces</option>${namespaces.map(v => `<option ${T.ns === v ? 'selected' : ''}>${esc(v)}</option>`).join('')}</select>
      <input id="tl-q" placeholder="Filter. Try ns: kind: severity: reason: or text" value="${esc(T.q)}" style="min-width:300px" autocomplete="off" spellcheck="false"></div></div>
    <section class="panel"><div class="panel-head"><h2>${shown.length} of ${items.length} entries</h2><div class="row"><small class="muted">${fmt(new Date(lo).toISOString())} → ${fmt(new Date(hi).toISOString())} UTC · drag to zoom</small><button id="tl-order">${T.newest ? 'Newest first' : 'Oldest first'} ⇅</button>${T.from || T.to ? '<button id="tl-reset">Reset zoom</button>' : ''}</div></div>
      <div class="panel-body"><div class="tl-strip" id="tl-strip" data-lo="${lo}" data-w="${w}">${buckets.map(bar).join('')}<div class="tl-sel" id="tl-sel"></div></div></div></section>
    <section class="panel mt">${rows || '<div class="empty"><strong>No entries match</strong>Change the filters or reset the zoom.</div>'}
      ${shown.length > T.limit ? `<div class="empty"><button id="tl-more">Show ${Math.min(300, shown.length - T.limit)} more (${shown.length - T.limit} hidden)</button></div>` : ''}</section>`;
}
after.timeline = () => {
  const T = S.tl, items = S.tlShown || [];
  if (!S.tlItems?.length) return;
  document.querySelectorAll('[data-tobj]').forEach(b => b.onclick = () => openObject(items[+b.dataset.tobj].object));
  document.querySelectorAll('[data-tlog]').forEach(b => b.onclick = () => {
    const i = items[+b.dataset.tlog], ok = t => t && !t.startsWith('0001');
    if (i.logQuery) return openLogs({q: i.logQuery, from: ok(i.from) ? i.from : '', to: ok(i.to) ? i.to : ''});
    const t = new Date(i.time).getTime();
    openLogs({q: i.namespace ? `ns:${i.namespace}` : '', from: new Date(t - 120000).toISOString(), to: new Date(t + 120000).toISOString()});
  });
  document.querySelectorAll('[data-tkind]').forEach(b => b.onclick = () => { const k = b.dataset.tkind; T.hideKinds = T.hideKinds.includes(k) ? T.hideKinds.filter(x => x !== k) : [...T.hideKinds, k]; render(); });
  document.querySelectorAll('[data-tns]').forEach(b => b.onclick = () => { T.ns = b.dataset.tns; render(); });
  $('#tl-sev').onchange = e => { T.sev = e.target.value; render(); };
  $('#tl-ns').onchange = e => { T.ns = e.target.value; render(); };
  const more = $('#tl-more'); if (more) more.onclick = () => { T.limit += 300; render(); };
  $('#tl-order').onclick = () => { T.newest = !T.newest; render(); };
  const reset = $('#tl-reset'); if (reset) reset.onclick = () => { T.from = T.to = ''; render(); };
  let tm; $('#tl-q').oninput = e => { clearTimeout(tm); tm = setTimeout(() => { const c = e.target.selectionStart, v = e.target.value; T.q = v; T.limit = 300; render().then(() => refocus('#tl-q', c, v)); }, 250); };
  typeahead($('#tl-q'), {keys: tlKeys, alias: {namespace: 'ns', type: 'kind', sev: 'severity'}, values: (f, p, rest) => {
    const terms = qTerms(rest), all = (S.tlItems || []).filter(i => tlMatch(i, terms)), get = tlFields[f];
    if (!get) return [];
    const label = {event: 'Kubernetes events', termination: 'container exits', rollout: 'ReplicaSet created', 'error-spike': 'error-log runs'};
    return countValues(all.map(get), p, f === 'kind' ? v => label[v] : null);
  }});
  // Drag (or click a column) on the strip to zoom into that window.
  const strip = $('#tl-strip'), sel = $('#tl-sel'), lo = +strip.dataset.lo, w = +strip.dataset.w;
  const frac = e => { const r = strip.getBoundingClientRect(); return Math.min(1, Math.max(0, (e.clientX - r.left) / r.width)); };
  let a = null;
  strip.onmousedown = e => { a = frac(e); sel.style.display = 'block'; sel.style.left = a * 100 + '%'; sel.style.width = '0'; e.preventDefault(); };
  window.onmousemove = e => { if (a === null) return; const b = frac(e); sel.style.left = Math.min(a, b) * 100 + '%'; sel.style.width = Math.abs(b - a) * 100 + '%'; };
  window.onmouseup = e => {
    if (a === null) return; let b = frac(e), x = Math.min(a, b), y = Math.max(a, b); a = null;
    const N = 90; if (y - x < 1 / N) { x = Math.floor(x * N) / N; y = x + 1 / N; }
    T.from = new Date(lo + x * N * w).toISOString(); T.to = new Date(lo + y * N * w).toISOString(); T.limit = 300; render();
  };
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
    <div class="row wrap"><span class="badge green">${sm.added || 0} added</span><span class="badge red">${sm.removed || 0} removed</span><span class="badge amber">${sm.changed || 0} changed</span><span class="badge">${sm.versions || 0} image changes</span><span class="badge red">${sm.newFindings || 0} new findings</span><span class="badge green">${sm.resolvedFindings || 0} resolved</span>${b.workspaceLoading ? '<span class="badge blue">this bundle is still loading</span>' : ''}</div>
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

const cmpIntro = "See what changed between two captures of the same cluster: objects added, removed or changed, image upgrades, and findings that appeared or went away. It is the quickest way to answer \"what changed since it last worked?\"";

function candidateRows(cands, cur) {
  return cands.map((c, i) => `<tr class="${c.current ? 'sel' : ''}"><td class="mono" style="white-space:normal;word-break:break-all" title="${esc(c.path)}">${esc(c.name)}${c.encrypted && c.unreadable ? ' <span class="badge amber">encrypted</span>' : ''}</td>
    <td>${esc(c.case || '—')}</td>
    <td>${(c.clusters || []).map(x => `<span class="tag">${esc(x)}</span>`).join(' ') || '<span class="muted">unknown</span>'}${c.sameCluster ? ' <span class="badge green">same cluster</span>' : ''}</td>
    <td>${c.captured && !c.captured.startsWith('0001') ? fmtDateTime(c.captured) : `<span class="muted" title="file modified">${fmtDateTime(c.modified)}</span>`}</td>
    <td class="muted">${bytes(c.size)}</td>
    <td style="text-align:right">${c.current ? '<span class="badge">loaded</span>' : `<button class="${i === 0 && !cur ? 'primary' : ''}" data-cand="${esc(c.path)}" ${c.unreadable ? `title="${esc(c.unreadable)}"` : ''}>Compare</button>`}</td></tr>`).join('');
}

function baselinePicker(base, cands, collapsed) {
  const list = cands?.candidates || [];
  const dirs = (cands?.dirs || []).filter((d, i, a) => a.indexOf(d) === i);
  const same = list.filter(c => c.sameCluster).length;
  if (base.canLoad === false) return `<div class="notice info"><div><strong>Loading an earlier capture is not available here.</strong> Start the desk with <code>flightbay-desk open after.tar.zst --baseline before.tar.zst</code>, or compare on the command line with <code>flightbay-desk diff before.tar.zst after.tar.zst</code>.</div></div>`;
  const body = `<p class="muted" style="margin:0 0 12px">Pick an earlier bundle from the same cluster. Flightbay loads it next to this one on your machine and lists the differences. Nothing leaves this computer.</p>
    ${base.error ? `<div class="notice bad" style="margin-bottom:12px">The last load failed: ${esc(base.error)}</div>` : ''}
    ${list.length ? `<div class="table-scroll" style="max-height:44vh"><table><thead><tr><th>Bundle</th><th>Case</th><th>Cluster</th><th>Captured</th><th>Size</th><th></th></tr></thead><tbody>${candidateRows(list, base.available)}</tbody></table></div>
      ${!same && S.ws?.clusters?.length ? `<p class="muted mt" style="font-size:12px">None of these were captured from ${esc((S.ws.clusters || []).map(c => c.name).join(', '))}. You can still compare them, but most objects will show as added or removed.</p>` : ''}`
      : `<div class="empty" style="padding:18px"><strong>No other bundles found</strong>Looked in ${dirs.map(d => `<code>${esc(d)}</code>`).join(', ') || 'the bundle directory, the working directory and ~/Downloads'}. Paste the path of an earlier capture below.</div>`}
    <form class="query mt" id="cmp-path-form"><span class="muted">⤓</span><input id="cmp-path" placeholder="Or paste a path, e.g. ~/Downloads/flightbay-MESH1-20260920T0900Z.tar.zst" autocomplete="off" spellcheck="false" aria-label="Bundle path"><button class="primary">Compare ↵</button></form>`;
  if (collapsed) return `<details class="panel mt cmp-other"><summary class="panel-head"><h2>Compare with a different capture</h2><small>${list.length} bundle${list.length === 1 ? '' : 's'} found</small></summary><div class="panel-body">${body}</div></details>`;
  return `<section class="panel"><div class="panel-head"><h2>Compare with an earlier capture</h2><small>${list.length ? `${list.length} bundle${list.length === 1 ? '' : 's'} found${same ? `, ${same} from this cluster` : ''}` : ''}</small></div><div class="panel-body">${body}</div></section>`;
}

function crossClusterSection(c, d) {
  const clusters = S.ws?.clusters || [];
  if (clusters.length < 2) return `<p class="muted mt" style="font-size:12px">Comparing one object across clusters applies to captures that include two or more clusters. This bundle has ${clusters.length ? 'one, ' + esc(clusters[0].name) : 'no clusters loaded yet'}.</p>`;
  const intro = `<p class="muted" style="margin:0 0 12px">This bundle holds ${clusters.length} clusters. Pick an object to see how its captured YAML differs between them, for example a route or a policy that should be identical everywhere.</p>`;
  if (!c) return `<section class="panel mt"><div class="panel-head"><h2>The same object across clusters</h2><small>${clusters.map(x => esc(x.name)).join(' · ')}</small></div><div class="panel-body">${intro}
    <div class="toolbar" style="margin:0"><input id="cmp-q" placeholder="Find an object to compare, e.g. checkout-route or kind:httproute" aria-label="Find object" autocomplete="off" spellcheck="false"><div id="cmp-results" class="search-results" style="width:100%"></div></div></div></section>`;
  const found = d.clusters.filter(x => x.found);
  const [L, R] = [found[0], found[1]];
  let body;
  if (found.length < 2) body = `<div class="notice info">${esc(c.kind || c.resource)} ${esc(c.namespace)}/${esc(c.name)} was captured in ${found.map(x => esc(x.cluster)).join(', ') || 'no cluster'} only. Missing in: ${d.clusters.filter(x => !x.found).map(x => esc(x.cluster)).join(', ')}.</div>`;
  else {
    const df = lineDiff(L.yaml.split('\n'), R.yaml.split('\n'));
    const changes = df.a.filter(x => x.c).length + df.b.filter(x => x.c).length;
    body = `<div class="row" style="margin-bottom:10px"><span class="badge ${changes ? 'amber' : 'green'}">${changes ? changes + ' changed lines' : 'identical'}</span><small>metadata noise (uid, resourceVersion, timestamps) removed</small></div>
      <div class="diff-grid"><div class="diff-col"><div class="panel-head"><h2>${esc(L.cluster)}</h2></div><div class="diff-code">${df.a.map(x => `<span class="${x.c || ''}">${esc(x.t) || ' '}</span>`).join('')}</div></div>
      <div class="diff-col"><div class="panel-head"><h2>${esc(R.cluster)}</h2></div><div class="diff-code">${df.b.map(x => `<span class="${x.c || ''}">${esc(x.t) || ' '}</span>`).join('')}</div></div></div>`;
  }
  return `<section class="panel mt"><div class="panel-head"><h2>${esc(c.kind || c.resource)} ${esc(c.namespace ? c.namespace + '/' : '')}${esc(c.name)} across clusters</h2><button id="cmp-clear">Pick another object</button></div><div class="panel-body">${body}</div></section>`;
}

async function compare() {
  const c = S.compare;
  const [base, cands, d] = await Promise.all([
    api('api/baseline').catch(() => ({available: false})),
    api('api/baseline/candidates').catch(() => ({candidates: []})),
    c && (S.ws?.clusters || []).length > 1 ? api('api/diff', c) : null,
  ]);
  S.baseReport = base;
  let top;
  if (base.loading) {
    const name = (base.loadingPath || '').split('/').pop();
    top = `<section class="panel"><div class="panel-head"><h2>Loading the earlier capture</h2><small>${base.loadingSince ? 'started ' + fmtTime(base.loadingSince) + ' UTC' : ''}</small></div><div class="panel-body">
      <div class="row"><span class="progress"><i></i></span><span class="mono">${esc(name)}</span></div>
      <p class="muted mt" style="font-size:12px">The whole bundle is read and verified before the comparison appears. Large captures take a minute or two; this page refreshes by itself.</p></div></section>`;
    if (base.available) top += baselineSection(base);
  } else if (base.available) {
    top = baselineSection(base) + baselinePicker(base, cands, true);
  } else {
    top = baselinePicker(base, cands, false);
  }
  return head('Compare', esc(cmpIntro)) + top + crossClusterSection(c, d);
}

async function loadBaseline(path) {
  const r = await fetch('api/baseline/load', {method: 'POST', credentials: 'same-origin', headers: {'Content-Type': 'application/json', Accept: 'application/json'}, body: JSON.stringify({path})});
  const body = await r.json().catch(() => ({}));
  if (!r.ok) { toast(body.error || 'Could not load that bundle'); return; }
  S.baseSel = null; S.baseKind = '';
  toast('Loading ' + (body.path || path).split('/').pop() + ' as the baseline');
  render();
}

let cmpPoll = null;
after.compare = () => {
  document.querySelectorAll('[data-base]').forEach(tr => tr.onclick = () => { S.baseSel = S.baseSel === +tr.dataset.base ? null : +tr.dataset.base; render(); });
  const bk = $('#base-kind'); if (bk) bk.onchange = e => { S.baseKind = e.target.value; S.baseSel = null; render(); };
  wireFindings();
  document.querySelectorAll('[data-cand]').forEach(b => b.onclick = () => { b.disabled = true; loadBaseline(b.dataset.cand); });
  const pf = $('#cmp-path-form'); if (pf) pf.onsubmit = e => { e.preventDefault(); const v = $('#cmp-path').value.trim(); if (v) loadBaseline(v); else toast('Paste the path of a .tar.zst bundle'); };
  clearTimeout(cmpPoll);
  if (S.baseReport?.loading) cmpPoll = setTimeout(() => { if (S.view === 'compare' && !S.drawerOpen) render(); }, 1500);
  const clr = $('#cmp-clear'); if (clr) clr.onclick = () => { S.compare = null; render(); };
  const q = $('#cmp-q'); if (!q) return;
  typeahead(q, {scope: 'objects', alias: objAlias});
  let t;
  q.oninput = () => { clearTimeout(t); t = setTimeout(async () => {
    const hits = q.value.trim() ? await api('api/search', {q: q.value}).catch(() => []) : [];
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
  modal('Jump to an object or view', `<input id="gs" style="width:100%" placeholder="Any object: name, label, image, hostname. Try kind: ns: label:" autocomplete="off"><div id="gs-res" class="search-results"></div>`);
  const views = ['navigator', 'search', 'overview', 'posture', 'resources', 'logs', 'timeline', 'findings', 'compare', 'proxies', 'coverage'];
  let hits = [], sel = 0;
  const draw = () => {
    $('#gs-res').innerHTML = hits.map((h, i) => `<button class="${i === sel ? 'sel' : ''}" data-i="${i}">${h.all ? `<span class="green">⌕</span> Search everything for “${esc(h.all)}”<small>Search page</small>` : h.view ? `<span class="green">↗</span> ${esc(h.view)}<small>view</small>` : `<span class="badge">${esc(h.kind)}</span>${esc(h.namespace ? h.namespace + '/' : '')}${esc(h.name)}${h.snippet ? ` <span class="muted mono" style="font-size:10px">…${esc(h.snippet.slice(0, 70))}…</span>` : ''}<small>${esc(h.cluster)}</small>`}</button>`).join('');
    document.querySelectorAll('#gs-res [data-i]').forEach(b => b.onclick = () => pick(hits[+b.dataset.i]));
  };
  const pick = h => { closeModal(); if (h.all) { S.search.q = h.all; nav('search'); } else if (h.view) nav(h.view); else openObject(h); };
  let t;
  const input = $('#gs');
  typeahead(input, {scope: 'objects', alias: objAlias});
  input.oninput = () => { clearTimeout(t); t = setTimeout(async () => {
    const q = input.value.toLowerCase();
    hits = views.filter(v => q && v.includes(q)).map(v => ({view: v})).concat(q ? (await api('api/search', {q: input.value})).slice(0, 12) : []).concat(q ? [{all: input.value}] : []); sel = 0; draw();
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
// openK9s runs real k9s against the snapshot in the terminal, full height.
function openK9s(resource, ns) {
  $('#terminal').classList.add('expanded');
  termRun(`k9s --readonly ${resource ? '-c ' + resource : ''} ${ns ? '-n ' + ns : '-A'}`.replace(/\s+/g, ' ').trim());
}

function termRun(cmd) {
  if (!S.ws?.terminal) { navigator.clipboard?.writeText(cmd); toast('Terminal disabled; command copied: ' + cmd); return; }
  toggleTerminal(true);
  const t = ensureTerminal(); if (t) t.send({type: 'input', data: cmd + '\r'});
}

// ---------- boot ----------
function applyTheme(t) {
  document.documentElement.dataset.theme = t;
  localStorage.setItem('flightbay-theme', t);
  $('#theme-toggle').textContent = t === 'light' ? '☾' : '☀';
  $('#theme-toggle').title = t === 'light' ? 'Switch to dark' : 'Switch to light';
}

function boot() {
  applyTheme(document.documentElement.dataset.theme || 'dark');
  $('#theme-toggle').onclick = () => applyTheme(document.documentElement.dataset.theme === 'light' ? 'dark' : 'light');
  $('#open-search').onclick = openSearch;
  $('#cluster').onchange = e => setCluster(e.target.value);
  $('#term-toggle').onclick = () => toggleTerminal();
  $('#term-k9s').onclick = () => openK9s('pods', '');
  $('#term-expand').onclick = () => { $('#terminal').classList.toggle('expanded'); toggleTerminal(true); };
  document.addEventListener('keydown', e => {
    const typing = /INPUT|TEXTAREA|SELECT/.test(document.activeElement?.tagName) || document.activeElement?.closest?.('.xterm');
    if ((e.metaKey || e.ctrlKey) && e.key === 'k') { e.preventDefault(); openSearch(); }
    else if (e.ctrlKey && e.key === '`') { e.preventDefault(); toggleTerminal(); }
    else if (e.key === 'Escape') { closeModal(); closeDrawer(); }
    else if (!typing && e.key === '/' && S.view !== 'navigator') { e.preventDefault(); ($('#log-q') || $('#res-q') || $('#obj-q'))?.focus(); }
  });
  addEventListener('hashchange', () => { parseHash(); closeDrawer(); render(); });
  parseHash();
  loadWorkspace().then(render).catch(e => { $('#main').innerHTML = `<div class="notice bad">${esc(e.message)}</div>`; });
  const es = new EventSource('api/state/stream');
  es.addEventListener('state', e => onState(JSON.parse(e.data)));
}
boot();
