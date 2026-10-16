'use strict';
// Flightbay desk UI. Plain JS, no framework, served from the flightbay-desk binary.
document.querySelectorAll('.brand').forEach(a => a.setAttribute('aria-label', 'Flightbay overview'));

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
  pst: {tab: 'summary', area: '', status: 'attention', q: '', open: null},
  nv: {resource: 'pods', group: '', ns: '', q: '', sel: 0, cmd: false, view: 'list', obj: null, text: '', textErr: '', wrap: false, help: false, hist: []},
  reach: {sel: '', q: '', type: '', dq: '', dchip: 'all', echip: 'all', fixOpen: {}, list: null, listRev: null, detail: null, detailKey: '', scroll: false, gview: '', gexpand: {}, gz: null, gfull: false},
};

async function api(path, params) {
  const qs = params ? '?' + new URLSearchParams(Object.entries(params).filter(([, v]) => v !== undefined && v !== null)).toString() : '';
  const r = await fetch(path + qs, {credentials: 'same-origin', headers: {Accept: 'application/json'}});
  const body = await r.json().catch(() => ({}));
  if (!r.ok) { const err = new Error(body.error || r.statusText); err.body = body; err.status = r.status; throw err; }
  return body;
}

function toast(msg) {
  const t = $('#toast');
  t.textContent = msg; t.classList.remove('hidden');
  clearTimeout(toast.t); toast.t = setTimeout(() => t.classList.add('hidden'), 3500);
}

function stateBadge(st, reason) {
  const label = {complete: 'complete', loading: 'loading', excluded: 'excluded by customer', failed: 'not collected', truncated: 'truncated', 'not-collected': 'not in bundle', removed: 'removed by customer', trimmed: 'some removed by customer'}[st] || st;
  return `<span class="badge state-${esc(st)}" title="${esc(reason || '')}"><span class="dot"></span>${esc(label)}</span>`;
}

function statusCell(v) {
  const s = String(v);
  const cls = /CrashLoop|Error|Failed|BackOff|OOM|Evicted|NotReady|False/.test(s) ? 'red' : /Pending|Init|Terminating|Unknown|Container/.test(s) ? 'amber' : /Running|Ready|Active|Completed|True|Succeeded/.test(s) ? 'green' : '';
  return cls ? `<span class="${cls}">${esc(s)}</span>` : esc(s);
}

// ---------- navigation ----------
function parseHash() {
  const h = location.hash.slice(1) || (S.ws?.agent ? 'traffic' : 'overview');
  const [view, query] = h.split('?');
  const p = new URLSearchParams(query || '');
  if (view === 'logs') {
    for (const k of ['q', 'namespace', 'workload', 'pod', 'container', 'level', 'stream', 'from', 'to']) if (p.has(k)) S.logs[k] = p.get(k);
  }
  if (view === 'search' && p.has('q')) S.search.q = p.get('q');
  if (view === 'posture' || view === 'bestpractices') { location.replace('#scorecard' + (query ? '?' + query : '')); }
  if ((view === 'scorecard' || view === 'posture') && p.has('tab')) S.pst.tab = p.get('tab');
  if (view === 'resources' && p.has('resource')) { S.res.resource = p.get('resource'); S.res.group = p.get('group') || ''; }
  if (view === 'reach') { location.replace('#traffic?tab=agents' + (p.get('agent') ? '&agent=' + encodeURIComponent(p.get('agent')).replace(/%2F/gi, '/') : '')); S.view = 'traffic'; return; }
  // Activity is now the gateway layer of Traffic; keep old links working.
  if (view === 'activity') { location.replace('#traffic?layer=gateway' + (p.get('caller') ? '&caller=' + encodeURIComponent(p.get('caller')) : '')); S.view = 'traffic'; return; }
  S.view = view === 'posture' || view === 'bestpractices' ? 'scorecard' : view;
  if (S.ws?.agent && !AGENT_VIEWS.includes(S.view)) { location.replace('#traffic'); S.view = 'traffic'; }
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
  const navView = S.view === 'navigator' ? 'resources' : S.view;
  document.querySelectorAll('.nav a').forEach(a => a.classList.toggle('active', a.dataset.view === navView));
  const views = {navigator, search, overview, scorecard: posture, reach, resources, logs, timeline, findings, compare, proxies, coverage, ...(typeof reviewPage === 'function' ? {review: reviewPage} : {}), ...(typeof trafficPage === 'function' ? {traffic: trafficPage} : {})};
  const fn = views[S.view] || overview;
  $('#crumb').textContent = S.ws ? `Workspace / ${S.ws.case ? 'Case ' + S.ws.case : 'Snapshot'} / ${S.view}` : 'Workspace';
  try {
    const html = await fn(seq);
    if (seq !== S.renderSeq || html === undefined) return;
    taClose(true);
    if (S.morph && S.lastView === S.view) morphHTML($('#main'), html); else $('#main').innerHTML = html;
    S.lastView = S.view;
    after[S.view]?.();
  } catch (e) {
    if (seq === S.renderSeq) $('#main').innerHTML = `<div class="notice bad">${esc(e.message)}</div>`;
  }
}
const after = {};

// morphHTML updates el to match html, touching only what differs: unchanged
// nodes keep their scroll position, selection and state. Used for live
// refreshes, where the same view is redrawn with slightly newer data.
function morphHTML(el, html) {
  const t = document.createElement('template');
  t.innerHTML = html;
  morphKids(el, t.content);
}
function morphSame(a, b) {
  return a.nodeType === b.nodeType && (a.nodeType !== 1 || (a.tagName === b.tagName && (a.id || '') === (b.id || '')));
}
function morphKids(cur, next) {
  const want = [...next.childNodes];
  want.forEach((n, i) => {
    const o = cur.childNodes[i];
    if (!o) { cur.appendChild(n); return; }
    if (!morphSame(o, n)) { cur.replaceChild(n, o); return; }
    if (o.nodeType !== 1) { if (o.nodeValue !== n.nodeValue) o.nodeValue = n.nodeValue; return; }
    for (const at of [...o.attributes]) if (!n.hasAttribute(at.name)) o.removeAttribute(at.name);
    for (const at of [...n.attributes]) if (o.getAttribute(at.name) !== at.value) o.setAttribute(at.name, at.value);
    if (/^(INPUT|TEXTAREA|SELECT)$/.test(o.tagName) && o !== document.activeElement) {
      if (o.type === 'checkbox' || o.type === 'radio') o.checked = n.checked; else if (o.value !== n.value) o.value = n.value;
    }
    morphKids(o, n);
  });
  while (cur.childNodes.length > want.length) cur.removeChild(cur.lastChild);
}

// Views wire their handlers after every render. With morphHTML the elements
// survive, so a listener added again would fire twice: inside #main, adding
// the same listener (same event and code) replaces the earlier one.
{
  const add = EventTarget.prototype.addEventListener, reg = new WeakMap();
  EventTarget.prototype.addEventListener = function (type, fn, opts) {
    if (this instanceof Element && fn && this.closest?.('#main')) {
      let m = reg.get(this);
      if (!m) reg.set(this, m = new Map());
      const k = type + '|' + String(fn), old = m.get(k);
      if (old) this.removeEventListener(type, old.fn, old.opts);
      m.set(k, {fn, opts});
    }
    return add.call(this, type, fn, opts);
  };
}

// Each view links to its page in the built-in documentation.
const docPages = {overview: 'desk/overview/', search: 'desk/search/', navigator: 'desk/navigator/', resources: 'desk/resources/', logs: 'desk/logs/', timeline: 'desk/timeline/',
  findings: 'desk/findings/', scorecard: 'desk/scorecard/', traffic: 'desk/traffic/', compare: 'desk/compare/', proxies: 'desk/proxy-configs/', coverage: 'desk/coverage/', review: 'collect/review/'};
function head(title, sub, actions = '') {
  const doc = docPages[S.view];
  const help = doc ? `<a class="help-link" href="/docs/${doc}" target="_blank" rel="noopener" title="How to use ${esc(title)}">? Docs</a>` : '';
  return `<div class="page-head"><div><h1>${esc(title)}</h1><p>${sub}</p></div><div class="row wrap">${actions}${help}</div></div>`;
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

// declaredText describes a repo scan (flightbay scan): declared state, not a capture.
const declaredText = w => w.source === 'repo' ? `Declared state from repo ${w.repo?.name || ''}${w.repo?.commit ? ' at commit ' + w.repo.commit.slice(0, 7) : ''}${w.repo?.dirty ? ' (uncommitted changes)' : ''}` : '';

async function loadWorkspace() {
  const w = await api('api/workspace');
  S.ws = w;
  $('#bundle-case').textContent = w.case ? `Case ${w.case}` : 'Snapshot';
  $('#bundle-sub').textContent = `${(w.contexts || []).length} cluster(s) · ${fmtDateTime(w.snapshot)}`;
  $('#snapshot-time').textContent = declaredText(w) || `Captured ${fmtDateTime(w.snapshot)}`;
  $('#collector').textContent = w.collector || '';
  const sel = $('#cluster');
  const cur = sel.value;
  sel.innerHTML = '<option value="">All clusters</option>' + (w.clusters || []).map(c => `<option value="${esc(c.name)}">${esc(c.name)}@snapshot</option>`).join('');
  sel.value = cur;
  const nf = $('#nav-findings');
  const n = (w.findings?.critical || 0) + (w.findings?.warning || 0);
  nf.textContent = n; nf.classList.toggle('hidden', !n);
  document.querySelector('.nav a[data-view="reach"]')?.classList.add('hidden');
  document.querySelector('.nav a[data-view="traffic"]')?.classList.toggle('hidden', !feature('traffic') && !feature('reach'));
  if (w.terminal) $('#terminal').classList.remove('hidden');
  if (!S.state) { S.state = w.state; renderImportStatus(); }
  if (typeof onWorkspace === 'function') onWorkspace(w);
  if (w.agent) agentMode();
}

// Agent mode (AGENT=1): only the views about agents. Links to any other
// view land on Traffic.
const AGENT_VIEWS = ['traffic', 'scorecard', 'resources', 'navigator', 'logs', 'search'];
function agentMode() {
  document.body.classList.add('agent-mode');
  document.title = 'Flightbay agent';
  const b = document.querySelector('.sidebar .brand'); if (b) { b.href = '#traffic'; b.setAttribute('aria-label', 'Flightbay agent: Traffic'); }
  if (!AGENT_VIEWS.includes(S.view)) location.replace('#traffic');
}

// ---------- overview ----------
function findingRow(f) {
  const icon = {critical: '×', warning: '!', info: 'i'}[f.severity];
  return `<div class="finding-row"><span class="sev ${esc(f.severity)}">${icon}</span><div style="flex:1;min-width:0">
    <button class="link" data-finding="${attr(f)}"><strong>${esc(f.title)}</strong></button>
    <p>${esc(f.detail || '')}</p><div class="row wrap mt" style="margin-top:6px"><span class="tag">${esc(f.cluster)}</span><small>${esc(f.rule)}</small>${f.provisional ? '<small class="blue">provisional</small>' : ''}</div></div></div>`;
}

const ovIcon = {
  clusters: '<path d="m12 3 9 5-9 5-9-5z"/><path d="m3 13 9 5 9-5"/>',
  objects: '<path d="M21 8 12 3 3 8v8l9 5 9-5z"/><path d="m3 8 9 5 9-5M12 13v8"/>',
  logs: '<path d="M3 5h18M3 10h18M3 15h12M3 20h8"/>',
  findings: '<path d="M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z"/><path d="M12 9v4M12 17h.01"/>',
};
const ovTile = k => `<span class="stat-tile"><svg viewBox="0 0 24 24" aria-hidden="true">${ovIcon[k]}</svg></span>`;
const ovEmpty = t => `<div class="ov-empty"><span>${esc(t)}</span></div>`;

// Horizontal bars; each row carries a data-ov attribute the page wires to a filter.
function ovBars(rows, cls = r => '') {
  const max = Math.max(1, ...rows.map(r => r.n));
  return `<div class="ov-bars">${rows.map(r => `<button class="ov-bar" data-ov="${attr(r.go)}" title="${esc(r.label)}: ${num(r.n)}"><span>${esc(r.label)}</span><span class="t"><i class="${cls(r)}" style="width:${Math.max(2, Math.round(r.n / max * 100))}%"></i></span><b>${num(r.n)}</b></button>`).join('')}</div>`;
}

// Log volume as an area over the capture window, errors drawn on top.
function ovLogPlot(L) {
  const h = L?.histogram || [];
  if (!h.length || !L.total) return ovEmpty(L?.loading ? 'Logs still loading' : 'No log lines');
  const errs = L.errors || [], W = 400, H = 130, n = h.length;
  const max = Math.max(1, ...h.map(b => b.count));
  const pts = list => list.map((b, i) => `${(i / Math.max(1, n - 1) * W).toFixed(1)},${(H - 6 - (b?.count || 0) / max * (H - 16)).toFixed(1)}`);
  const all = pts(h), err = pts(errs.length === n ? errs : []);
  const span = new Date(h[n - 1].start) - new Date(h[0].start);
  return `<svg class="ov-plot" viewBox="0 0 ${W} ${H}" preserveAspectRatio="none" aria-label="Log lines over time">
    <defs><linearGradient id="ov-ga" x1="0" x2="0" y1="0" y2="1"><stop offset="0" style="stop-color:var(--chart-ok);stop-opacity:.35"/><stop offset="1" style="stop-color:var(--chart-ok);stop-opacity:0"/></linearGradient></defs>
    <g style="stroke:var(--line)" stroke-width="1" vector-effect="non-scaling-stroke"><line x1="0" y1="${H * .25}" x2="${W}" y2="${H * .25}"/><line x1="0" y1="${H * .6}" x2="${W}" y2="${H * .6}"/></g>
    <path d="M0,${H} L${all.join(' L')} L${W},${H}Z" fill="url(#ov-ga)"/>
    <path d="M${all.join(' L')}" fill="none" style="stroke:var(--chart-ok)" stroke-width="2" vector-effect="non-scaling-stroke"/>
    ${err.length && L.errorTotal ? `<path d="M${err.join(' L')}" fill="none" style="stroke:var(--red)" stroke-width="1.5" stroke-dasharray="3 3" vector-effect="non-scaling-stroke"/>` : ''}
  </svg><div class="ov-axis"><span>${esc(fmtAxis(h[0].start, span))}</span><span>${esc(fmtAxis(h[n - 1].start, span))}</span></div>
  <div class="ov-legend"><span><i style="background:var(--chart-ok)"></i>${num(L.total)} lines</span>${L.errorTotal ? `<span><i style="background:var(--red)"></i>${num(L.errorTotal)} errors</span>` : ''}</div>`;
}

function ovCharts(C) {
  if (!C) return '';
  const rules = (C.rules || []).slice(0, 5).map(r => ({label: r.rule, n: r.count, sev: r.severity, go: {findings: r.rule}}));
  const rs = (C.restarts || []).slice(0, 5).map(r => ({label: r.namespace, n: r.restarts, go: {logs: r.namespace}}));
  const restartTotal = (C.restarts || []).reduce((a, r) => a + r.restarts, 0);
  return `<div class="ov-charts">
    <section class="ov-chart v"><h3>Findings by rule</h3><div class="note">${num((C.rules || []).reduce((a, r) => a + r.count, 0))} findings across ${num((C.rules || []).length)} rules</div>
      ${rules.length ? ovBars(rules, r => r.sev) : ovEmpty('No findings')}</section>
    <section class="ov-chart g"><h3>Log volume</h3><div class="note">Lines over the capture window</div>${ovLogPlot(C.logs)}</section>
    <section class="ov-chart m"><h3>Restarts by namespace</h3><div class="note">${num(restartTotal)} container restarts in ${num((C.restarts || []).length)} namespaces</div>
      ${rs.length ? ovBars(rs) : ovEmpty('No restarts')}</section>
  </div>`;
}

async function overview() {
  const [w, charts] = await Promise.all([api('api/workspace'), api('api/overview/charts', {cluster: S.cluster}).catch(() => null)]);
  S.ws = w;
  const clusters = (w.clusters || []).filter(c => !S.cluster || c.name === S.cluster);
  const tot = k => clusters.reduce((a, c) => a + (c[k] || 0), 0);
  const pol = w.policy || {};
  const st = w.state || {};
  return head(w.case ? `Case ${w.case}` : 'Snapshot', `${(w.contexts || []).join(', ')} · ${declaredText(w) || 'captured ' + fmtDateTime(w.snapshot)}`,
    `<button id="go-coverage">Coverage &amp; redactions</button><button class="primary" id="go-findings">Investigate findings →</button>`) + `
  <div class="stats">
    <div class="stat">${ovTile('clusters')}<div><div class="eyebrow">Clusters</div><div class="value">${clusters.length}<small>${esc(clusters.map(c => c.version).filter(Boolean)[0] || '')}</small></div><p>${tot('nodes')} nodes captured · ${tot('namespaces')} namespaces</p></div></div>
    <div class="stat">${ovTile('objects')}<div><div class="eyebrow">Objects</div><div class="value">${num(tot('objects'))}</div><p>${num(tot('pods'))} pods · ${num(tot('podsRunning'))} running</p></div></div>
    <div class="stat">${ovTile('logs')}<div><div class="eyebrow">Log lines</div><div class="value">${num(st.logLines)}</div><p>${st.phase === 'complete' ? 'all streams loaded' : 'still loading'}</p></div></div>
    <div class="stat${w.findings.critical + w.findings.warning ? ' alert' : ''}">${ovTile('findings')}<div><div class="eyebrow">Findings</div><div class="value amber">${w.findings.critical + w.findings.warning}<small>${w.findings.critical} critical</small></div><p>${w.findings.info} informational</p></div></div>
  </div>
  ${ovCharts(charts)}
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
  document.querySelectorAll('[data-ov]').forEach(b => b.onclick = () => {
    const go = JSON.parse(b.dataset.ov);
    if (go.findings) { S.findingsRule = go.findings; nav('findings'); }
    else if (go.logs) openLogs({namespace: go.logs});
  });
};

function wireFindings() {
  document.querySelectorAll('[data-finding]').forEach(b => b.onclick = () => openFinding(JSON.parse(b.dataset.finding)));
}

// ---------- kinds, grouped by area ----------
const areaOrder = ['Workloads', 'Networking', 'Gateway API', 'Istio', 'agentgateway and kgateway', 'kagent', 'Other platform APIs', 'Config, access and storage', 'Other'];
function kindArea(t) {
  const g = t.group || '', k = t.kind || t.resource;
  if ((g === '' && ['Pod', 'ReplicationController', 'PodTemplate'].includes(k)) || ['apps', 'batch', 'autoscaling', 'policy'].includes(g)) return 'Workloads';
  if ((g === '' && ['Service', 'Endpoints'].includes(k)) || g === 'discovery.k8s.io' || g === 'networking.k8s.io') return 'Networking';
  if (g.startsWith('gateway.networking')) return 'Gateway API';
  if (g.endsWith('istio.io')) return 'Istio';
  if (/agentgateway|kgateway|gateway\.solo\.io/.test(g)) return 'agentgateway and kgateway';
  if (g.includes('kagent')) return 'kagent';
  if (g.endsWith('solo.io')) return 'Other platform APIs';
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
// The Navigator behaves like k9s: a table of one resource type, and in-place
// views of the selected object (describe, YAML, events, logs) that Esc backs
// out of. ":" switches type, namespace or cluster; "/" filters; "?" shows
// every key. It reads the same read-only API the terminal's kubectl uses.
const nvQuick = [['pods', ''], ['deployments', 'apps'], ['services', ''], ['gateways', 'gateway.networking.k8s.io'], ['httproutes', 'gateway.networking.k8s.io'], ['events', '']];
const nvKeys = [
  ['List', [['↑ ↓  j k', 'move'], ['g  G', 'first, last'], ['Enter  d', 'describe (Enter on a namespace opens its pods)'], ['y', 'YAML'], ['e', 'events'], ['l  p', 'logs, previous logs'], ['o', 'open in the side drawer'], ['s', 'open this type in k9s (terminal)'], ['Ctrl-D', 'kubectl describe in the terminal']]],
  ['Views', [['Esc  q', 'back to the list'], ['↑ ↓  PgUp PgDn', 'scroll'], ['g  G', 'top, bottom'], ['w', 'wrap long lines'], ['c', 'copy'], ['d y e l', 'switch view']]],
  ['Commands', [[':pods', 'switch type (any kind, plural, short name: po, deploy, svc, httproute…)'], [':pods kube-system', 'type in a namespace'], [':pods all', 'all namespaces'], [':ns', 'namespaces (Enter drills in)'], [':ns kagent', 'stay on this type, in kagent'], [':ctx 2  or  :ctx mesh2', 'switch cluster'], [':help', 'this help']]],
  ['Filter', [['/checkout', 'name, status or content contains'], ['/!Running', 'does not contain'], ['/-l app=web', 'label selector (also label:app=web)'], ['Esc', 'clear the filter'], ['0-9', 'cluster (0 is all)'], ['?', 'help']]],
];

async function navigator() {
  const N = S.nv, w = S.ws || await api('api/workspace');
  const types = await api('api/resources', {cluster: S.cluster});
  S.nvTypes = types;
  const have = types.filter(t => t.count > 0);
  let cur = types.find(t => t.resource === N.resource && t.group === N.group) || have.find(t => t.resource === 'pods') || have[0];
  if (!cur) return head('Resources', 'Nothing captured yet.') + resTabs('navigator');
  N.resource = cur.resource; N.group = cur.group; N.cur = cur;
  // "/!text" and "-l" are handled here; everything else goes to the server filter.
  const f = nvFilter(N.q);
  const list = await api('api/objects', {cluster: S.cluster, group: N.group, resource: N.resource, namespace: N.ns, q: f.server});
  let rows = list.rows;
  if (f.not) rows = rows.filter(r => !(r.name + ' ' + r.cells.join(' ')).toLowerCase().includes(f.not));
  N.rows = rows; N.sel = Math.min(N.sel, Math.max(0, rows.length - 1));
  const nss = [...new Set(list.rows.map(r => r.namespace).filter(Boolean))].sort();
  if (N.ns && !nss.includes(N.ns)) nss.unshift(N.ns);
  const live = !!w.live;
  const suffix = live ? 'live' : 'snapshot';
  const ctx = (w.clusters || []).map((c, i) => `<option value="${esc(c.name)}" ${S.cluster === c.name ? 'selected' : ''}>${i + 1} ${esc(c.name)}@${suffix}</option>`).join('');
  const quick = nvQuick.filter(([r, g]) => have.some(t => t.resource === r && t.group === g));
  const statusCol = list.columns.indexOf('Status');
  const sel = rows[N.sel];
  const conn = live ? `<dd style="color:var(--teal)">● live · read-only${w.live.context ? ' · ' + esc(w.live.context) : ''}</dd>` : '<dd style="color:var(--teal)">offline · read-only</dd>';
  const headKeys = `<div class="nv-keys"><b>↑↓ jk</b><span>move</span><b>Enter d</b><span>describe</span><b>y</b><span>yaml</span><b>e</b><span>events</span><b>l p</b><span>logs</span></div>
      <div class="nv-keys"><b>:</b><span>type, ns, ctx</span><b>/</b><span>filter</span><b>0-9</b><span>cluster</span><b>Esc</b><span>back</span><b>?</b><span>help</span></div>`;
  const header = `<div class="nv-head">
      <dl><dt>Bundle:</dt><dd>${esc(live ? 'live mirror' : w.case ? 'case ' + w.case : w.bundleId || 'snapshot')}</dd>
        <dt>Context:</dt><dd><select id="nv-ctx"><option value="">0 all clusters</option>${ctx}</select></dd>
        <dt>${live ? 'Now:' : 'Snapshot:'}</dt><dd style="color:var(--amber)">${fmtDateTime(w.snapshot)}</dd>
        <dt>Connection:</dt>${conn}</dl>
      ${headKeys}
      <div class="nv-logo"> ┌─┐ ┌─┐ ┌─┐\n │ │ │ │ │ │  flightbay\n │ └─┘ │ │ │  ${live ? 'live     ' : 'snapshot '}\n └─────┘ └─┘  navigator</div>
    </div>`;
  const cmdline = N.cmd ? `<div class="nv-cmd"><span>:</span><input id="nv-cmdline" placeholder="pods · deploy kube-system · ns kagent · ctx 2 · help   (Tab completes, Enter runs, Esc cancels)" autocomplete="off" spellcheck="false"></div>` : '';
  const help = N.help ? `<div class="nv-help" id="nv-help"><div class="row between"><strong>Keys</strong><small class="muted">Esc or ? to close</small></div><div class="nv-help-grid">${nvKeys.map(([t, ks]) => `<div><h4>${t}</h4>${ks.map(([k, d]) => `<div class="nv-help-row"><b>${esc(k)}</b><span>${esc(d)}</span></div>`).join('')}</div>`).join('')}</div></div>` : '';
  let body;
  if (N.view !== 'list' && N.obj) {
    const o = N.obj;
    const vt = {describe: 'describe', yaml: 'yaml', events: 'events', logs: N.prevLogs ? 'logs (previous)' : 'logs'}[N.view];
    const isPod = o.resource === 'pods' && !o.group, canLogs = isPod || workloadKinds.includes(o.kind);
    const tabs = [['describe', 'd'], ['yaml', 'y'], ['events', 'e']].concat(canLogs ? [['logs', 'l']] : []);
    body = `<div class="nv-title">${esc(cur.kind || cur.resource)}(${esc(N.ns || 'all')}) › <b>${esc(vt)}</b> ${esc(o.namespace ? o.namespace + '/' : '')}${esc(o.name)}</div>
      <div class="nv-bar">${tabs.map(([v, k]) => `<button class="nv-type ${N.view === v ? 'on' : ''}" data-nvview="${v}">${k} ${v}</button>`).join('')}
        <span style="flex:1"></span><button class="ghost" data-nvact="wrap">w wrap ${N.wrap ? 'on' : 'off'}</button><button class="ghost" data-nvact="copy">c copy</button><button class="ghost" data-nvact="drawer">o drawer</button><button data-nvact="back">Esc back</button></div>
      <pre class="nv-text ${N.wrap ? 'wrap' : ''}" id="nv-text" tabindex="0">${N.textErr ? `<span class="red">${esc(N.textErr)}</span>` : N.text === null ? '<span class="muted">Loading…</span>' : nvColour(N.view, N.text) || '<span class="muted">Nothing to show.</span>'}</pre>
      <div class="nv-status"><span>${esc(o.name)}${o.namespace ? ' · ' + esc(o.namespace) : ''} · ${esc(o.cluster)}@${suffix}</span><span><b>Esc</b> back · <b>d y e l</b> views · <b>w</b> wrap · <b>c</b> copy · <b>?</b> help</span></div>`;
  } else {
    body = `<div class="nv-title">${esc(cur.kind || cur.resource)}(${esc(N.ns || 'all')})${N.q ? ` <span class="nv-flt">/${esc(N.q)}</span>` : ''} [${rows.length}]</div>
      <div class="nv-bar">${quick.map(([r, g]) => `<button class="nv-type ${r === N.resource && g === N.group ? 'on' : ''}" data-nvtype="${attr({r, g})}">:${r}</button>`).join('')}
        <span style="flex:1"></span><select id="nv-ns"><option value="">All namespaces</option>${nss.map(n => `<option ${n === N.ns ? 'selected' : ''}>${esc(n)}</option>`).join('')}</select>
        <input id="nv-q" placeholder="/ filter · !exclude · -l app=web" value="${esc(N.q)}" style="width:280px" autocomplete="off" spellcheck="false"></div>
      <div class="table-scroll" style="max-height:58vh"><table class="nv-table"><thead><tr>${cur.namespaced ? '<th>Namespace</th>' : ''}${list.columns.map(c => `<th>${esc(c)}</th>`).join('')}<th></th></tr></thead><tbody>
        ${rows.map((r, i) => `<tr class="${i === N.sel ? 'sel' : ''}" data-nvrow="${i}">${cur.namespaced ? `<td>${esc(r.namespace)}</td>` : ''}${r.cells.map((c, j) => `<td>${j === 0 ? `<button class="link nv-open" data-nvopen="${i}">${i === N.sel ? '› ' : ''}${esc(c)}</button>` : j === statusCol ? statusCell(c) : esc(c)}</td>`).join('')}
          <td class="nv-acts"><button class="ghost" data-nvgo="${attr({i, v: 'describe'})}" title="Describe (d)">d</button><button class="ghost" data-nvgo="${attr({i, v: 'yaml'})}" title="YAML (y)">y</button>${(N.resource === 'pods' && !N.group) || workloadKinds.includes(r.kind) ? `<button class="ghost" data-nvgo="${attr({i, v: 'logs'})}" title="Logs (l)">l</button>` : ''}</td></tr>`).join('') || `<tr><td colspan="9" class="muted">No ${esc(cur.kind || cur.resource)} objects${N.q ? ' match the filter' : ''}.</td></tr>`}
      </tbody></table></div>
      <div class="nv-status"><span>${sel ? `${esc(sel.name)}${sel.namespace ? ' · ' + esc(sel.namespace) : ''}${!S.cluster ? ' · ' + esc(sel.cluster) : ''}` : ''}</span><span><b>Enter</b> describe · <b>y</b> yaml · <b>e</b> events · <b>l</b> logs · <b>:</b> command · <b>/</b> filter · <b>?</b> help</span></div>`;
  }
  return head('Resources', 'Every captured object. Browse with the mouse, or switch to the keyboard view to move through them the way you would in k9s.') + resTabs('navigator') + `<div class="nv">${header}<div class="nv-frame" id="nv-frame" tabindex="-1">${cmdline}${help}${body}</div></div>`;
}

// nvFilter turns a k9s-style filter into the server query plus a client-side
// exclusion: "/!text" excludes, "-l k=v" is a label selector.
function nvFilter(q) {
  q = (q || '').trim();
  if (q.startsWith('!')) return {server: '', not: q.slice(1).toLowerCase()};
  const m = q.match(/^-l\s+(.+)$/);
  if (m) return {server: m[1].split(',').map(x => 'label:' + x.trim()).join(' '), not: ''};
  return {server: q, not: ''};
}

// nvColour gives describe and YAML output a little k9s colour: keys, values
// and warnings.
function nvColour(view, text) {
  const t = esc(text || '');
  if (view === 'yaml') return t.replace(/^(\s*-?\s*)([\w.\/-]+):/gm, '$1<span class="nv-k">$2</span>:');
  if (view === 'describe') return t.replace(/^([A-Z][\w ()\/-]*):/gm, '<span class="nv-k">$1</span>:').replace(/\b(Warning|Failed|Error|CrashLoopBackOff|BackOff|Unhealthy|OOMKilled)\b/g, '<span class="red">$1</span>');
  if (view === 'logs') return t.replace(/^(.*\b(ERROR|error|Error|FATAL|panic)\b.*)$/gm, '<span class="red">$1</span>').replace(/^(.*\b(WARN|warn|Warning)\b.*)$/gm, '<span class="amber">$1</span>');
  return t;
}

async function nvLoad(view) {
  const N = S.nv, o = N.obj;
  N.view = view; N.text = null; N.textErr = '';
  render();
  try {
    if (view === 'describe') {
      N.text = (await api('api/describe', {cluster: o.cluster, group: o.group, resource: o.resource, namespace: o.namespace, name: o.name})).text;
    } else if (view === 'yaml') {
      N.text = (await api('api/object', nvRef(o))).yaml;
    } else if (view === 'events') {
      const d = await api('api/object', nvRef(o));
      const ev = d.events || [];
      N.text = ev.length ? ['LAST SEEN            TYPE      REASON                   COUNT  MESSAGE'].concat(ev.map(e => `${(fmtDateTime(e.lastTimestamp || e.eventTime) || '').padEnd(20)} ${(e.type || '').padEnd(9)} ${(e.reason || '').padEnd(24)} ${String(e.count || 1).padEnd(6)} ${e.message || ''}`)).join('\n') : 'No events for this object.';
    } else if (view === 'logs') {
      const isPod = o.resource === 'pods' && !o.group;
      const params = isPod ? {pod: o.name, namespace: o.namespace} : {workload: `${o.kind}/${o.name}`, namespace: o.namespace};
      const res = await api('api/logs/search', {...params, cluster: o.cluster, stream: N.prevLogs ? 'previous' : '', limit: 1000});
      const recs = (res.records || []).slice().reverse();
      N.text = recs.length ? recs.map(r => `${(r.ts || '').slice(11, 23)} ${isPod ? (r.container || '') : r.pod}  ${r.raw || r.msg}`).join('\n') + (res.total > recs.length ? `\n\n… ${num(res.total - recs.length)} older lines: press L for the log explorer` : '') : `No ${N.prevLogs ? 'previous ' : ''}log lines captured.`;
    }
  } catch (e) { N.textErr = e.message; }
  if (S.view === 'navigator') {
    await render();
    const pre = $('#nv-text');
    if (pre) { pre.focus(); if (view === 'logs') pre.scrollTop = pre.scrollHeight; }
  }
}

const labelKey = [{value: 'label', hint: 'label key or key=value', example: 'label:app=checkout'}];
// nvTypeSuggestions lists the captured types for the : command line, matching
// the resource name, kind, singular name or a short name, plus the commands.
function nvTypeSuggestions(p) {
  const w = (p || '').toLowerCase().replace(/^:/, '');
  const types = S.nvTypes || [];
  const dup = {}; types.forEach(t => { dup[t.resource] = (dup[t.resource] || 0) + 1; });
  const cmds = [['ns', 'namespaces, or ns <name> to switch'], ['ctx', 'clusters, or ctx <n|name>'], ['help', 'every key and command']].filter(([c]) => !w || c.startsWith(w)).map(([value, hint]) => ({value, hint}));
  const t = types.filter(t => !w || t.resource.startsWith(w) || (t.kind || '').toLowerCase().startsWith(w) || (t.singularName || '').startsWith(w) || (t.shortNames || []).some(s => s.startsWith(w)))
    .sort((a, b) => (b.count > 0) - (a.count > 0) || b.count - a.count || a.resource.localeCompare(b.resource)).slice(0, 40)
    .map(t => ({value: dup[t.resource] > 1 ? t.name : t.resource, count: t.count, hint: `${t.kind}${(t.shortNames || []).length ? ' · ' + t.shortNames.join(', ') : ''}${t.group ? ' · ' + t.group : ''}${t.count ? '' : ' · none captured'}`}));
  return cmds.concat(t);
}

function nvRef(r) { return {cluster: r.cluster, group: r.group, resource: r.resource, namespace: r.namespace, name: r.name}; }

function nvOpen(i, view) {
  const N = S.nv, r = N.rows[i];
  if (!r) return;
  N.sel = i;
  if (view === 'describe' && N.resource === 'namespaces' && !N.group) { N.ns = r.name; N.resource = 'pods'; N.group = ''; N.sel = 0; N.q = ''; render(); return; }
  N.obj = r; N.prevLogs = false;
  nvLoad(view);
}

function nvBack() {
  const N = S.nv;
  if (N.help) { N.help = false; render(); return; }
  if (N.view !== 'list') { N.view = 'list'; N.obj = null; N.text = ''; render().then(nvFocus); return; }
  if (N.q) { N.q = ''; N.sel = 0; render().then(nvFocus); }
}

function nvFocus() { const f = $('#nv-frame'); if (f && !/INPUT|TEXTAREA|SELECT/.test(document.activeElement?.tagName)) f.focus({preventScroll: true}); }

// nvCommand runs a ":" command: a type (with an optional namespace), ns, ctx or help.
async function nvCommand(value) {
  const N = S.nv;
  const parts = value.trim().replace(/^:/, '').split(/\s+/).filter(Boolean);
  if (!parts.length) { N.cmd = false; render().then(nvFocus); return; }
  const [c0, arg] = [parts[0].toLowerCase(), parts[1]];
  const done = () => { N.cmd = false; N.view = 'list'; N.obj = null; N.sel = 0; render().then(nvFocus); };
  if (c0 === 'help' || c0 === '?' || c0 === 'h') { N.cmd = false; N.help = true; render(); return; }
  if (c0 === 'q' || c0 === 'quit') { N.cmd = false; nav('overview'); return; }
  if (['ctx', 'context', 'contexts'].includes(c0)) {
    const cs = S.ws?.clusters || [];
    if (!arg) { N.cmd = false; toast(cs.map((c, i) => `${i + 1} ${c.name}`).join(' · ') + ' · 0 all'); render().then(nvFocus); return; }
    const pick = /^\d+$/.test(arg) ? (arg === '0' ? {name: ''} : cs[+arg - 1]) : cs.find(c => c.name === arg || c.name.includes(arg));
    if (!pick) { toast(`No cluster ${arg}`); return; }
    N.cmd = false; setCluster(pick.name); return;
  }
  if ((c0 === 'ns' || c0 === 'namespace' || c0 === 'namespaces') && arg) { N.ns = arg === 'all' || arg === '-A' ? '' : arg; done(); return; }
  const types = S.nvTypes || await api('api/resources', {cluster: S.cluster});
  const want = (c0 === 'ns' ? 'namespaces' : c0);
  const exact = x => x.resource === want || x.name === want || (x.kind || '').toLowerCase() === want || x.singularName === want || (x.shortNames || []).includes(want);
  let m = types.filter(exact);
  if (!m.length) m = types.filter(x => x.resource.startsWith(want) || (x.kind || '').toLowerCase().startsWith(want));
  m.sort((a, b) => b.count - a.count || (a.group ? 1 : 0) - (b.group ? 1 : 0));
  if (!m.length) { toast(`No type matches "${want}"`); return; }
  N.resource = m[0].resource; N.group = m[0].group; N.q = '';
  if (arg) N.ns = arg === 'all' || arg === '-A' ? '' : arg;
  done();
}

after.navigator = () => {
  const N = S.nv;
  const setType = (r, g) => { N.resource = r; N.group = g; N.sel = 0; N.q = ''; N.cmd = false; N.view = 'list'; N.obj = null; render().then(nvFocus); };
  document.querySelectorAll('[data-nvtype]').forEach(b => b.onclick = () => { const t = JSON.parse(b.dataset.nvtype); setType(t.r, t.g); });
  document.querySelectorAll('[data-nvrow]').forEach(tr => {
    // One click opens the object (describe), as Enter does.
    tr.onclick = e => { if (e.target.closest('button') || getSelection().toString()) return; nvOpen(+tr.dataset.nvrow, 'describe'); };
  });
  document.querySelectorAll('[data-nvopen]').forEach(b => b.onclick = e => { e.stopPropagation(); nvOpen(+b.dataset.nvopen, 'describe'); });
  document.querySelectorAll('[data-nvgo]').forEach(b => b.onclick = e => { e.stopPropagation(); const x = JSON.parse(b.dataset.nvgo); nvOpen(x.i, x.v); });
  document.querySelectorAll('[data-nvview]').forEach(b => b.onclick = () => { N.prevLogs = false; nvLoad(b.dataset.nvview); });
  document.querySelectorAll('[data-nvact]').forEach(b => b.onclick = () => nvAct(b.dataset.nvact));
  const ns = $('#nv-ns'); if (ns) ns.onchange = e => { N.ns = e.target.value; N.sel = 0; render().then(nvFocus); };
  $('#nv-ctx').onchange = e => setCluster(e.target.value);
  $('#nv-frame').onmousedown = e => { if (!e.target.closest('input,select,button,a')) setTimeout(nvFocus, 0); };
  const q = $('#nv-q');
  if (q) {
    let t; q.oninput = e => { clearTimeout(t); t = setTimeout(() => { const c = e.target.selectionStart, v = e.target.value; N.q = v; N.sel = 0; render().then(() => refocus('#nv-q', c, v)); }, 200); };
    typeahead(q, {scope: 'objects', keys: labelKey, alias: {l: 'label'}, extraQ: () => `kind:${N.resource}${N.group ? '.' + N.group : ''}${N.ns ? ' ns:' + N.ns : ''}`});
    q.onkeydown = e => { if (e.key === 'Enter') { e.preventDefault(); nvFocus(); } if (e.key === 'Escape') { N.q = ''; q.value = ''; render().then(nvFocus); } };
  }
  const cl = $('#nv-cmdline');
  if (cl) {
    // Tab completes a suggestion; Enter runs the command (the highlighted
    // suggestion if the menu is open), once.
    let lastKey = '', ran = false;
    cl.addEventListener('keydown', e => { lastKey = e.key; ran = false; }, true);
    typeahead(cl, {keys: [], bareValues: nvTypeSuggestions, onAccept: i => { if (lastKey === 'Enter') { ran = true; nvCommand(i.value); } }});
    cl.focus();
    cl.onkeydown = e => {
      if (e.key === 'Escape') { N.cmd = false; render().then(nvFocus); return; }
      if (e.key === 'Enter' && !ran) nvCommand(cl.value);
    };
    setTimeout(() => cl.dispatchEvent(new KeyboardEvent('keydown', {key: 'ArrowDown'})), 0);
  } else if (!N.help && N.view === 'list') {
    setTimeout(nvFocus, 0);
  }
};

function nvAct(a) {
  const N = S.nv;
  if (a === 'back') nvBack();
  else if (a === 'wrap') { N.wrap = !N.wrap; render(); }
  else if (a === 'copy') navigator.clipboard.writeText(N.text || '').then(() => toast('Copied'), () => toast('Clipboard unavailable'));
  else if (a === 'drawer' && N.obj) openObject(nvRef(N.obj));
}

// Keyboard: while the navigator is showing and no input, the terminal, a
// drawer or a modal has focus.
document.addEventListener('keydown', e => {
  if (S.view !== 'navigator' || e.metaKey || e.altKey) return;
  if (/INPUT|TEXTAREA|SELECT/.test(document.activeElement?.tagName) || document.activeElement?.closest?.('.xterm')) return;
  if (S.drawerOpen || document.querySelector('.modal-backdrop')) return;
  const N = S.nv, rows = N.rows || [], r = rows[N.sel], k = e.key;
  if (e.ctrlKey) {
    if ((k === 'd' || k === 'D') && (r || N.obj)) { const o = N.obj || r; e.preventDefault(); termRun(`kubectl --context ${o.cluster}@${S.ws?.live ? 'live' : 'snapshot'} describe ${o.resource}${o.group ? '.' + o.group : ''} ${o.name}${o.namespace ? ' -n ' + o.namespace : ''}`); }
    return;
  }
  if (N.help) { if (k === 'Escape' || k === '?' || k === 'q') { N.help = false; render().then(nvFocus); e.preventDefault(); } return; }
  if (k === '?') { N.help = true; render(); e.preventDefault(); return; }
  if (k === ':') { N.cmd = true; render(); e.preventDefault(); return; }
  if (N.view !== 'list') {
    const pre = $('#nv-text'), page = pre ? pre.clientHeight * 0.9 : 400;
    const o = N.obj, canLogs = o && ((o.resource === 'pods' && !o.group) || workloadKinds.includes(o.kind));
    if (k === 'Escape' || k === 'q') { nvBack(); e.preventDefault(); }
    else if (k === 'j' || k === 'ArrowDown') { pre && (pre.scrollTop += 40); e.preventDefault(); }
    else if (k === 'k' || k === 'ArrowUp') { pre && (pre.scrollTop -= 40); e.preventDefault(); }
    else if (k === 'PageDown' || k === ' ') { pre && (pre.scrollTop += page); e.preventDefault(); }
    else if (k === 'PageUp') { pre && (pre.scrollTop -= page); e.preventDefault(); }
    else if (k === 'g') pre && (pre.scrollTop = 0);
    else if (k === 'G') pre && (pre.scrollTop = pre.scrollHeight);
    else if (k === 'w') nvAct('wrap');
    else if (k === 'c') nvAct('copy');
    else if (k === 'o') nvAct('drawer');
    else if (k === 'd') nvLoad('describe');
    else if (k === 'y') nvLoad('yaml');
    else if (k === 'e') nvLoad('events');
    else if ((k === 'l' || k === 'p') && canLogs) { N.prevLogs = k === 'p'; nvLoad('logs'); }
    else if (k === 'L' && canLogs) openLogs(o.resource === 'pods' ? {pod: o.name, namespace: o.namespace} : {workload: `${o.kind}/${o.name}`, namespace: o.namespace});
    return;
  }
  const move = d => { N.sel = Math.max(0, Math.min(rows.length - 1, N.sel + d)); document.querySelectorAll('[data-nvrow]').forEach(tr => tr.classList.toggle('sel', +tr.dataset.nvrow === N.sel)); document.querySelectorAll('.nv-open').forEach((b, i) => { b.textContent = (i === N.sel ? '› ' : '') + b.textContent.replace(/^› /, ''); }); document.querySelector('tr.sel')?.scrollIntoView({block: 'nearest'}); const st = document.querySelector('.nv-status span'); if (st && rows[N.sel]) st.textContent = rows[N.sel].name + (rows[N.sel].namespace ? ' · ' + rows[N.sel].namespace : ''); };
  if (k === 'j' || k === 'ArrowDown') { move(1); e.preventDefault(); }
  else if (k === 'k' || k === 'ArrowUp') { move(-1); e.preventDefault(); }
  else if (k === 'PageDown') { move(15); e.preventDefault(); }
  else if (k === 'PageUp') { move(-15); e.preventDefault(); }
  else if (k === 'g' || k === 'Home') move(-rows.length);
  else if (k === 'G' || k === 'End') move(rows.length);
  else if ((k === 'Enter' || k === 'd') && r) { nvOpen(N.sel, 'describe'); e.preventDefault(); }
  else if (k === 'y' && r) nvOpen(N.sel, 'yaml');
  else if (k === 'e' && r) nvOpen(N.sel, 'events');
  else if ((k === 'l' || k === 'p') && r && ((N.resource === 'pods' && !N.group) || workloadKinds.includes(r.kind))) { S.nv.prevLogs = k === 'p'; N.obj = r; nvLoad('logs'); }
  else if (k === 'o' && r) openObject(nvRef(r));
  else if (k === 's') openK9s(N.resource + (N.group ? '.' + N.group : ''), N.ns);
  else if (k === '/') { $('#nv-q')?.focus(); e.preventDefault(); }
  else if (/^[0-9]$/.test(k)) { const cs = S.ws?.clusters || []; if (k === '0') setCluster(''); else if (cs[+k - 1]) setCluster(cs[+k - 1].name); }
  else if (k === 'Escape') nvBack();
});

// ---------- best practices ----------
const pstColour = n => n >= 90 ? 'var(--green)' : n >= 75 ? '#7fc97f' : n >= 60 ? 'var(--amber)' : 'var(--red)';
const feature = name => !!(S.ws && S.ws.features && S.ws.features[name]);
// Severity tag: coloured by weight with the Enterprise priority view, plain otherwise.
// The field guide and the product docs page, side by side. Either may be
// missing; a docs link that repeats the guide is shown once.
const docsLabel = u => /^https:\/\/docs\.solo\.io\//.test(u) ? 'Product docs' : /^https:\/\/kubernetes\.io\//.test(u) ? 'Kubernetes docs' : /^https:\/\/istio\.io\//.test(u) ? 'Istio docs' : 'Docs';
const guideLinks = (source, docs) => [
  source ? `<a href="${esc(source)}" target="_blank" rel="noopener" title="${esc(source)}">Read the guide ↗</a>` : '',
  docs && docs !== source ? `<a href="${esc(docs)}" target="_blank" rel="noopener" title="${esc(docs)}">${docsLabel(docs)} ↗</a>` : ''].filter(Boolean).join('');
const pstSev = sev => feature('priorities') ? `<span class="pst-sev pst-sev-${esc(sev)}">${esc(sev)}</span>` : `<span class="tag">${esc(sev)}</span>`;
const pstPts = p => p >= 1 ? '+' + Math.round(p) : p > 0 ? '+<1' : '';
// What each category covers. Shown on hover over the category bars and in
// the check rows; keep in step with docs/site/content/desk/scorecard.md.
const pstCatInfo = {
  'Authentication': 'Callers have to prove who they are: JWT, API keys or mTLS required on MCP, A2A, LLM and ingress routes, with issuer and audience pinned and failures closed.',
  'Authorization': 'What an authenticated caller is allowed to do: authorization policies, default deny, no allow-all or overly broad rules, per-tool MCP access and waypoints wired so L7 policy is enforced.',
  'Encryption': 'Traffic is encrypted and certificates are checked: workloads in the mesh with strict mTLS, TLS on listeners, TLS verified to upstreams, valid TLS secrets, credentials not stored in plain text.',
  'Guardrails': 'Controls on what agents and models may do: prompt guards on requests, responses and streams, agents going through the LLM gateway, tool scope and enforcement that fails closed.',
  'Identity': 'Credentials and who workloads run as: no literal or passed-through tokens, secret references that resolve, dedicated service accounts, RBAC without wildcards, escalation or secret reads, and a shared mesh root of trust.',
  'Isolation': 'Limiting what can reach what: egress control, network policies, Pod Security admission, host access, namespace-restricted routes, MCP servers not reachable directly, the metadata endpoint blocked.',
  'Resilience': 'Keeping things up under failure and load: replicas and HA, disruption budgets, probes, resource requests, spread, timeouts, retries, rate limits, and references and policies that actually resolve and attach.',
  'Supply chain': 'Where the software comes from: images pinned to a digest or fixed tag, a trusted Istio distribution, agent artifacts pinned, a hardened registry, MCP selectors scoped, and extensions kept in check.',
  'Workload security': 'How containers run: not privileged, no privilege escalation, dropped capabilities, running as non-root, seccomp and a read-only root filesystem.',
  'Platform': 'The cluster underneath: a supported Kubernetes version, healthy nodes, certificates not about to expire, no deprecated APIs and Helm releases in a good state.',
  'Cost': 'Spend and wasted work: LLM spend limits, rate-limit keys that cannot be spoofed, retries only on idempotent calls, and waypoints that are doing a job.',
  'Observability': 'Being able to see what happened: tracing, audit and access logs, and mesh telemetry.',
  'Multicluster': 'Multicluster mesh set-up: peer gateway configuration and no legacy remote secrets.',
  'Network exposure': 'Mesh ports such as 15008 and 15012 not exposed outside the cluster.',
  'Health': 'The control plane reporting clean metrics, with no config rejections or push errors.',
  'Config hygiene': 'Configuration that points at things that exist, such as istio.io/rev labels resolving to a running revision.',
  'Routing': 'Routing rules that behave in ambient mode, such as VirtualService source matches.',
  'Security': 'General HTTP security settings on ingress, such as a safe CORS policy.',
};
const pstCatTip = k => pstCatInfo[k] || 'A group of related checks.';
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
      <div class="mt">${cats.map(([k, v]) => `<button class="pst-cat" data-pstcat="${attr({area: a.area, cat: k})}" title="${esc(k)}: ${esc(pstCatTip(k))}&#10;&#10;Score ${v}. Click to list these checks."><span>${esc(k)}</span><div class="pst-bar"><i style="width:${v}%;background:${pstColour(v)}"></i></div><small>${v}</small></button>`).join('')}</div></div></section>`;
  };
  const tot = {fail: 0, warn: 0, pass: 0, na: 0};
  r.checks.forEach(c => tot[c.status]++);
  const objs = r.checks.reduce((n, c) => n + c.total, 0);
  const coverage = `<p class="muted pst-coverage-line">Scanned <strong>${r.checks.length}</strong> checks across ${present.length} area${present.length === 1 ? '' : 's'} and ${objs} object${objs === 1 ? '' : 's'}: <span class="red">${tot.fail} failing</span>, <span class="amber">${tot.warn} warnings</span>, ${tot.pass} passing, ${tot.na} not applicable. Hover a category bar to see what it covers, or click it to list its checks.${absent.length ? ` Not scored: ${absent.map(a => `${esc(shortName[a.area] || a.title)} (${esc(a.reason)})`).join('; ')}.` : ''}</p>`;
  // Tabs: Summary, one per scored area, All checks.
  const areaIds = present.map(a => a.area);
  if (P.tab !== 'summary' && P.tab !== 'all' && !areaIds.includes(P.tab)) P.tab = 'summary';
  P.area = areaIds.includes(P.tab) ? P.tab : '';
  const attention = id => r.checks.filter(c => (!id || c.area === id) && (c.status === 'fail' || c.status === 'warn'));
  const failing = id => r.checks.filter(c => (!id || c.area === id) && c.status === 'fail').length;
  const tab = (id, label, n, red) => `<button class="pst-tab ${P.tab === id ? 'on' : ''}" data-psttab="${esc(id)}">${esc(label)}${n ? `<span class="pst-tab-n ${red === undefined ? '' : red ? 'red' : 'amber'}">${n}</span>` : ''}</button>`;
  const tabs = `<nav class="pst-tabs" role="tablist">${tab('summary', 'Summary')}${present.map(a => tab(a.area, shortName[a.area] || a.title, attention(a.area).length, failing(a.area) > 0)).join('')}${tab('all', 'All checks', r.checks.length)}</nav>`;
  const terms = qTerms(P.q);
  const typedStatus = terms.some(t => t.key === 'status'); // a status: term overrides the status chips
  const checks = r.checks.filter(c => (!P.area || c.area === P.area) &&
    (typedStatus || P.status === 'all' || (P.status === 'attention' ? (c.status === 'fail' || c.status === 'warn') : c.status === P.status)) &&
    pstMatch(c, terms));
  const chip = (field, v, label) => `<button class="chip ${P[field] === v && !(field === 'status' && typedStatus) ? 'on' : ''}" data-pst="${attr({field, v})}">${label}</button>`;
  const row = c => {
    const open = P.open === c.id;
    const items = c.items.filter(i => i.status !== 'na');
    return `<div class="pst-check ${open ? 'open' : ''}"><button class="pst-head" data-pstopen="${esc(c.id)}">${pstBadge(c.status)}${pstSev(c.severity)}<strong>${esc(c.title)}</strong><span class="muted" title="${esc(c.category)}: ${esc(pstCatTip(c.category))}">${esc(shortName[c.area] || c.area)} · ${esc(c.category)}</span><span style="flex:1"></span>${c.points ? `<span class="pst-pts" title="Area score points recovered if every object passed">${pstPts(c.points)} pts</span>` : ''}${c.status === 'na' ? `<small class="muted">${esc(c.reason)}</small>` : `<small>${c.passed}/${c.total} pass</small>`}<span class="muted">${open ? '▾' : '▸'}</span></button>
      ${open ? `<div class="pst-body"><p>${esc(c.why)}</p>
        ${items.length ? `<div class="table-scroll mt" style="max-height:40vh"><table><thead><tr><th></th><th>Object</th><th>What we found</th></tr></thead><tbody>${items.map((i, n) => `<tr><td>${pstBadge(i.status)}</td><td class="mono">${i.name ? `<button class="link" data-pstobj="${attr({c: c.id, n})}">${esc(i.kind)} ${esc(i.namespace ? i.namespace + '/' : '')}${esc(i.name)}</button>` : '<span class="muted">cluster</span>'}</td><td style="white-space:normal">${esc(i.detail)}</td></tr>`).join('')}</tbody></table></div>` : ''}
        ${c.status === 'fail' || c.status === 'warn' || c.status === 'na' ? `<div class="row between mt"><h3>How to fix</h3><div class="row"><button data-pstcopy="${esc(c.id)}">Copy YAML</button>${guideLinks(c.source, c.docs)}</div></div><pre class="code mt">${esc(c.fix)}</pre>` : `<p class="mt row">${guideLinks(c.source, c.docs)}</p>`}
      </div>` : ''}</div>`;
  };
  const objList = its => its.filter(i => i.status === 'fail' || i.status === 'warn').slice(0, 4).map(i => (i.kind + ' ' + (i.namespace ? i.namespace + '/' : '') + i.name).trim());
  const firstRow = (p, rank) => `<div class="pst-first-item"><button class="pst-first-row" data-pstgo="${esc(p.id)}"><span class="pst-rank">${rank}</span>${p.points !== undefined ? `<span class="pst-pts big">${pstPts(p.points)}</span>` : ''}${pstBadge(p.status)}${pstSev(p.severity)}
      <span class="pst-first-main"><strong>${esc(p.title)}</strong><small class="muted">${esc(shortName[p.area] || p.area)} · ${p.failing ? p.failing + ' failing' : ''}${p.failing && p.warning ? ', ' : ''}${p.warning ? p.warning + ' warning' : ''} of ${p.total}: <span class="mono">${esc(p.objects.join(', '))}</span></small></span><span class="muted">▸</span></button>${p.source || p.docs ? `<span class="pst-first-links">${guideLinks(p.source, p.docs)}</span>` : ''}</div>`;
  let summary = '';
  if (P.tab === 'summary') {
    if (feature('priorities') && (r.priorities || []).length) {
      summary = `<section class="panel"><div class="panel-head"><h2>Fix these first</h2><small class="muted">By severity and how much of the check fails. +N is what the fix adds to the area score <span class="ent-pill">Enterprise</span></small></div>
        <div>${r.priorities.map(p => firstRow(p, p.rank)).join('')}</div></section>`;
    } else {
      const sevRank = {critical: 0, high: 1, medium: 2, low: 3};
      const worst = attention('').filter(c => c.severity === 'critical' || c.severity === 'high')
        .sort((a, b) => (a.status === b.status ? 0 : a.status === 'fail' ? -1 : 1) || sevRank[a.severity] - sevRank[b.severity]).slice(0, 10)
        .map(c => ({id: c.id, area: c.area, title: c.title, status: c.status, severity: c.severity, total: c.total, source: c.source, docs: c.docs,
          failing: c.items.filter(i => i.status === 'fail').length, warning: c.items.filter(i => i.status === 'warn').length, objects: objList(c.items)}));
      summary = `<section class="panel"><div class="panel-head"><h2>Most serious problems</h2><small class="muted">Critical and high severity checks that fail or warn</small></div>
        <div>${worst.map((p, i) => firstRow(p, i + 1)).join('') || '<div class="empty"><strong>No critical or high problems</strong>Open an area tab to see the rest.</div>'}</div></section>`;
    }
    if (feature('reach')) summary += `<p class="pst-reach-link"><a href="#traffic?tab=agents">See each agent's network reach →</a></p>`;
  }
  let lastArea = '';
  const rows = checks.map(c => { const h = !P.area && c.area !== lastArea ? `<div class="tl-day">${esc((r.areas.find(a => a.area === c.area) || {}).title || c.area)}</div>` : ''; lastArea = c.area; return h + row(c); }).join('');
  const list = P.tab === 'summary' ? '' : `
    <div class="toolbar"><div class="row wrap">${chip('status', 'attention', 'Needs attention')}${chip('status', 'fail', 'Failing')}${chip('status', 'warn', 'Warnings')}${chip('status', 'pass', 'Passing')}${chip('status', 'na', 'Not applicable')}${chip('status', 'all', 'All')}</div>
      <input id="pst-q" placeholder="Filter. Try status: severity: category: kind: ns: or text" value="${esc(P.q)}" style="min-width:340px" autocomplete="off" spellcheck="false"></div>
    <section class="panel">${rows || (P.status === 'attention' && !P.q ? '<div class="empty"><strong>Nothing needs attention</strong>Every applicable check passes. Choose Passing or All to see them.</div>' : '<div class="empty"><strong>No checks match</strong>Change the filters above.</div>')}</section>`;
  return head('Scorecard', "Security and operational checks for agentic workloads, Istio, ingress and Kubernetes, scored against this snapshot. Every problem comes with its evidence and the fix.", '<button id="pst-term">&gt;_ fbscore in terminal</button>') + `
    <div class="pst-cards">${present.map(card).join('')}</div>
    ${coverage}
    ${tabs}
    <div class="pst-tabbody">${summary}${list}</div>
    <p class="muted mt" style="font-size:12px">Scores weight checks by severity (critical 10, high 6, medium 3, low 1) and give partial credit per object. Checks whose data was not captured, or that have nothing to apply to, are not applicable and do not lower the score.</p>`;
}
after.scorecard = () => {
  const P = S.pst;
  document.querySelectorAll('[data-pst]').forEach(b => b.onclick = () => { const f = JSON.parse(b.dataset.pst); P[f.field] = f.v; render(); });
  document.querySelectorAll('[data-psttab]').forEach(b => b.onclick = () => { P.tab = b.dataset.psttab; P.open = null; render(); });
  document.querySelectorAll('[data-pstgo]').forEach(b => b.onclick = () => {
    const id = b.dataset.pstgo, c = S.pstData.checks.find(x => x.id === id); P.tab = c ? c.area : 'all'; P.open = id; P.status = 'attention'; P.q = '';
    render().then(() => { const el = document.querySelector(`[data-pstopen="${CSS.escape(id)}"]`); if (el) el.scrollIntoView({block: 'start', behavior: 'smooth'}); });
  });
  document.querySelectorAll('[data-pstarea]').forEach(b => b.onclick = () => { P.tab = P.tab === b.dataset.pstarea ? 'summary' : b.dataset.pstarea; P.open = null; render(); });
  // A category bar opens its area with the checks in that category, whatever their status.
  document.querySelectorAll('[data-pstcat]').forEach(b => b.onclick = e => {
    e.stopPropagation();
    const x = JSON.parse(b.dataset.pstcat); P.tab = x.area; P.open = null; P.status = 'all'; P.q = `category:"${x.cat.toLowerCase()}"`;
    render().then(() => { const el = $('#pst-q'); if (el) el.scrollIntoView({block: 'center', behavior: 'smooth'}); });
  });
  document.querySelectorAll('[data-pstopen]').forEach(b => b.onclick = () => { P.open = P.open === b.dataset.pstopen ? null : b.dataset.pstopen; render(); });
  document.querySelectorAll('[data-pstobj]').forEach(b => b.onclick = () => { const x = JSON.parse(b.dataset.pstobj); const it = S.pstData.checks.find(c => c.id === x.c).items.filter(i => i.status !== 'na')[x.n]; openObject({cluster: it.cluster, group: it.group, resource: it.resource, namespace: it.namespace, name: it.name}); });
  document.querySelectorAll('[data-pstcopy]').forEach(b => b.onclick = () => { const c = S.pstData.checks.find(x => x.id === b.dataset.pstcopy); navigator.clipboard.writeText(c.fix).then(() => toast('Fix copied'), () => toast('Clipboard unavailable')); });
  const q = $('#pst-q');
  if (q) {
  let t; q.oninput = e => { clearTimeout(t); t = setTimeout(() => { const c = e.target.selectionStart, v = e.target.value; P.q = v; render().then(() => refocus('#pst-q', c, v)); }, 250); };
  typeahead($('#pst-q'), {keys: pstKeys, alias: {sev: 'severity', namespace: 'ns'}, values: (f, p, rest) => {
    const terms = qTerms(rest), checks = (S.pstData?.checks || []).filter(c => pstMatch(c, terms)), get = pstFields[f];
    if (!get) return [];
    const hints = {fail: 'failing', warn: 'warning', pass: 'passing', na: 'not applicable'};
    return countValues(checks.flatMap(get), p, f === 'status' ? v => hints[v] : null);
  }});
  }
  $('#pst-term').onclick = () => termRun('fbscore');
};

// ---------- agent reach (Flightbay Enterprise) ----------
// By agent: what each agent, MCP server and agent gateway can reach on the
// network, what those destinations can reach for it, and how to close it down.
const rcIntro = 'Every agent, MCP server and agent gateway: what it can reach, what it actually used and called, and how to lock it down to that.';
const rcTypes = {'kagent-agent': 'kagent agent', 'mcp-server': 'MCP server', agentgateway: 'agentgateway', 'possible-agent': 'possible agent'};
const rcRadiusCls = {open: 'red', wide: 'amber', narrow: 'blue', contained: 'green'};
const rcRadiusWhy = {open: 'reaches the internet, or has a critical finding', wide: 'half the cluster or more', narrow: 'more than three Services', contained: 'three Services or fewer'};
const rcNetCls = {allowed: 'red', denied: 'green', unknown: 'amber'};
const rcRiskRank = {red: 0, amber: 1, green: 2, grey: 3};
const rcName = a => a.namespace + '/' + a.name;
const rcCap = s => s ? s[0].toUpperCase() + s.slice(1) : '';
const rcBadge = (v, cls, label) => `<span class="rc-badge ${cls || ''}">${esc(label ?? v ?? '—')}</span>`;
const rcTypePill = t => `<span class="rc-type rc-type-${esc(t)}">${esc(rcTypes[t] || t)}</span>`;
const rcRisk = r => `<span class="rc-risk rc-${esc(r)}"><i></i>${esc(r)}</span>`;
// Flow sources as small badges: which logs saw a call.
const rcSrcs = xs => (xs || []).map(x => `<span class="rc-src rc-src-${esc(x)}">${esc(x)}</span>`).join('');
const rcSrcList = xs => (xs || []).length ? xs.join(', ') : '';
// How a destination was found: the policy evaluation, the agent's own
// configuration, a gateway route, and every flow source that saw it.
const rcFoundLabel = {policy: 'policy', config: 'config', route: 'gateway route', image: 'image pull'};
function rcFound(x, external) {
  const out = [];
  if (external) {
    if ((x.declaredIn || []).some(d => !/node pull|image pulls/.test(d))) out.push('config');
    if (x.reachedVia === 'gateway' && x.route) out.push('route');
    if (x.reachedVia === 'node') out.push('image');
  } else {
    out.push('policy');
    if (x.declared) out.push('config');
  }
  for (const s of (external ? x.sources : x.observedBy) || []) if (!out.includes(s)) out.push(s);
  return out;
}
const rcFoundChips = ks => ks.map(k => `<span class="rc-src rc-src-${esc(k)}">${esc(rcFoundLabel[k] || k)}</span>`).join('');
// rcFoundOk filters on how a destination was found.
const rcFoundOk = (ks, k) => k === 'any' ? true : k === 'traffic' ? ks.some(x => !rcFoundLabel[x]) : k === 'only-policy' ? ks.length === 1 && ks[0] === 'policy' : ks.includes(k);
// Every flow source Flightbay reads, with what it is and how to get it when
// the capture has none.
const rcSources = [
  ['ztunnel', 'ztunnel access logs', 'no ztunnel access logs in the capture (Istio ambient only)'],
  ['agentgateway', 'agentgateway request logs', 'no agentgateway request logs in the capture'],
  ['envoy', 'Envoy access logs: sidecars, waypoints, gateways', 'turn on Istio access logging (meshConfig.accessLogFile)'],
  ['coredns', 'CoreDNS query logs', 'the CoreDNS log plugin is off by default: add log to the Corefile'],
  ['hubble', 'Cilium Hubble flows', 'flightbay collect --hubble-flows, or attach hubble observe output'],
  ['calico', 'Calico flow logs', 'attach with flightbay collect --attach-flows'],
  ['aws-vpc', 'AWS VPC flow logs', 'attach with flightbay collect --attach-flows'],
  ['gcp-vpc', 'GCP VPC flow logs', 'attach with flightbay collect --attach-flows'],
  ['azure-vnet', 'Azure NSG / VNet flow logs', 'attach with flightbay collect --attach-flows'],
  ['generic', 'any other flow export (CSV or JSON Lines)', 'attach with flightbay collect --attach-flows'],
];
// rcSrcState says what a source means for this agent: in the capture and
// covering it, in the capture but blind to it, or not in the capture at all.
function rcSrcState(src, agent) {
  const inCap = (S.reach.flowSrc || {})[src] > 0 || (src === 'coredns' && (agent.external || []).some(x => (x.sources || []).includes('coredns')));
  const covers = (agent.observedBy || []).includes(src);
  const why = src === 'ztunnel' && !(agent.identity || {}).inMesh ? `${agent.name} is not in the ambient mesh, so ztunnel does not carry its traffic` : '';
  return covers ? {cls: 'green', text: 'in the capture, covers this agent'} : inCap ? {cls: 'amber', text: 'in the capture, but not for this agent', why} : {cls: 'muted', text: 'not in the capture'};
}
// rcFoundMenu is a compact "Found by" dropdown: what says a destination is
// reachable, then every flow source with how many destinations it saw, and
// for the ones that saw nothing, why.
function rcFoundMenu(items, cur, attrName, agent) {
  const all = items.map(x => x.found);
  const n = k => all.filter(ks => rcFoundOk(ks, k)).length;
  const kinds = [['any', 'Any', 'every destination'], ['only-policy', 'Policy only', 'allowed, but not configured and never seen'], ['config', 'Configured', "in the agent's own configuration"], ['route', 'Gateway route', 'reached through an agentgateway route'], ['image', 'Image pull', 'the node pulls its image from there'], ['traffic', 'Seen in traffic', 'any flow source below saw it']];
  const label = (kinds.find(k => k[0] === cur) || [cur, cur])[1];
  const row = (k, name, sub, count, state) => `<button class="rc-fitem ${cur === k ? 'on' : ''}" ${attrName}="${k}" ${count || k === 'any' ? '' : 'disabled'}>
      <span class="rc-fname">${esc(name)}</span><span class="rc-fn">${count}</span><span class="rc-fsub ${state ? state.cls : 'muted'}">${esc(state ? state.text : sub)}</span>${state && !count && (state.cls !== 'amber' || state.why) ? `<span class="rc-fsub muted">${esc(state.cls === 'muted' ? sub : state.cls === 'green' ? 'covers it, and saw none of its destinations' : state.why)}</span>` : ''}</button>`;
  const known = new Set(rcSources.map(x => x[0]));
  const extra = [...new Set(all.flat())].filter(k => !rcFoundLabel[k] && !known.has(k) && k !== 'policy');
  return `<div class="rc-fmenu"><button class="chip ${cur && cur !== 'any' ? 'on' : ''}" data-fmenu>Found by: ${esc(label)} ▾</button>
    <div class="rc-fpop" hidden><div class="rc-fhead">Found by</div>${kinds.filter(([k]) => k === 'any' || n(k)).map(([k, name, sub]) => row(k, name, sub, n(k))).join('')}
      <div class="rc-fhead">Flow sources</div>${[...rcSources, ...extra.map(k => [k, k, ''])].map(([k, name, how]) => row(k, k, how, n(k), Object.assign(rcSrcState(k, agent), {text: `${name} · ${rcSrcState(k, agent).text}`}))).join('')}</div></div>`;
}
const rcOrigin = f => f.origin === 'bundle' ? `flow file ${f.file || ''}, in the bundle` : f.origin === 'reviewer' ? `flow file ${f.file || ''}, added when opened (not signed)` : 'captured logs';
// rcLogQuery is a Logs search that finds the lines behind a flow record, for
// the sources whose records come from captured logs.
function rcLogQuery(f) {
  if (f.origin && f.origin !== 'logs') return '';
  const host = String(f.dstHost || f.dstIp || '').replace(/\.$/, '');
  const q = s => `"${String(s).replace(/"/g, '')}"`;
  const ip = /^[\d.]+$|:/.test(host);
  if (f.source === 'ztunnel') return [f.srcPod ? `src.workload:${f.srcPod}` : '', host ? (ip ? q(host) : `dst.service:${host}`) : '', f.method ? `method:${f.method}` : ''].filter(Boolean).join(' ');
  if (f.source === 'agentgateway') return ['scope:request', f.route ? `route:${f.route}` : '', host ? q(host) : '', f.method && f.protocol !== 'mcp' ? `http.method:${f.method}` : ''].filter(Boolean).join(' ');
  if (f.source === 'coredns') return host ? `pod:coredns* ${q(host)}` : '';
  if (f.source === 'envoy') return [f.srcPod ? `pod:${f.srcPod}` : '', host ? q(host) : ''].filter(Boolean).join(' ');
  return '';
}
// rcConfigLinks opens the objects the evidence names (the Backend, route,
// Gateway, ConfigMap, ModelConfig or the agent's own Deployment) in a modal,
// with the lines that mention the destination highlighted.
function rcConfigLinks(x, external) {
  const refs = x.configRefs || [];
  if (!refs.length) return '';
  const sv = rcSvc(x.to);
  const hl = external ? [x.host] : sv ? [`${sv.name}.${sv.namespace}`, sv.name] : [];
  return `<div class="rc-cfg"><h4>Open the config</h4>${refs.map(r => {
    const env = /\(env ([A-Za-z_][A-Za-z0-9_]*)\)/.exec(r.label || '');
    return `<button class="rc-cfgbtn" data-rcref="${attr({ref: {cluster: r.cluster, group: r.group, resource: r.resource, namespace: r.namespace, name: r.name}, label: r.label || `${r.kind} ${r.namespace ? r.namespace + '/' : ''}${r.name}`, hl: env ? [env[1], ...hl] : hl})}"><span class="rc-cfgk">${esc(r.kind)}</span> <span class="mono">${esc(r.namespace ? r.namespace + '/' : '')}${esc(r.name)}</span>${env ? ` <span class="muted">env ${esc(env[1])}</span>` : ''}</button>`;
  }).join('')}</div>`;
}
// rcConfigModal shows one object's captured YAML, the matching lines marked.
async function rcConfigModal({ref, label, hl}) {
  const d = await api('api/object', ref).catch(e => ({error: e.message}));
  if (d.error) { toast(d.error); return; }
  const terms = (hl || []).filter(Boolean).map(t => t.toLowerCase());
  const lines = String(d.yaml || '').split('\n');
  const hit = l => terms.some(t => l.toLowerCase().includes(t));
  const n = lines.filter(hit).length;
  modal(label, `<div class="row between wrap" style="margin-bottom:10px"><small class="muted">Captured object (redacted)${n ? ` · ${n} line${n === 1 ? '' : 's'} mention ${esc(hl.join(' or '))}` : ''}</small>
      <div class="row"><button id="cm-copy">Copy YAML</button><button id="cm-open">Open in the object view</button></div></div>
    <pre class="code rc-cmyaml">${lines.map(l => `<span class="${hit(l) ? 'rc-cmhit' : ''}">${esc(l)}</span>`).join('\n')}</pre>`);
  $('.modal').classList.add('modal-wide');
  $('#cm-copy').onclick = () => navigator.clipboard.writeText(d.yaml).then(() => toast('YAML copied'), () => toast('Clipboard unavailable'));
  $('#cm-open').onclick = () => { closeModal(); openObject(ref); };
  $('.rc-cmhit')?.scrollIntoView({block: 'center'});
}
// rcEvidence is the panel that opens under a destination: what said it is
// reachable, where it is configured, and the flow records that show it.
function rcEvidence(x, external, agent) {
  const ev = x.evidence || [];
  const policy = external
    ? `<div>${esc(rcVia(x))}${x.gateway ? ` · <span class="mono">${esc(x.gateway)}</span>` : ''}${x.route ? ` · route <span class="mono">${esc(x.route)}</span>` : ''}${x.verdict && x.reachedVia !== 'direct' ? ` · egress <span class="${rcNetCls[x.verdict] || ''}">${esc(x.verdict)}</span>` : x.verdict ? ` <span class="${rcNetCls[x.verdict] || ''}">${esc(x.verdict)}</span>` : ''}</div><div class="muted">${esc(x.why || '')}</div>`
    : `<div><span class="${x.verdict === 'denied' ? 'green' : x.verdict === 'allowed' ? '' : 'amber'}">${esc(x.verdict)}</span>${x.layer ? ` by <strong>${esc(x.layer)}</strong>` : ''}${x.via ? ` · via ${esc(x.via)}` : ''}</div>${(x.reasons || []).map(r => `<div class="muted">${esc(r)}</div>`).join('')}${(x.conditions || []).map(r => `<div class="muted">if ${esc(r)}</div>`).join('')}`;
  const decl = (x.declaredIn || []).length ? x.declaredIn.map(d => `<div class="mono">${esc(d)}</div>`).join('') : x.declared ? '<div>yes</div>' : '<span class="muted">not in its configuration</span>';
  const seen = external ? (x.observed || x.lookups) : x.observed;
  const none = agent.observed ? `No flow source saw it. ${esc(rcSrcList(agent.observedBy))} cover${(agent.observedBy || []).length === 1 ? 's' : ''} this agent, so it was allowed but not called while the capture ran.` : 'No flow logs cover this agent, so the capture cannot say whether it was called. Attach flow logs with <span class="mono">flightbay collect --attach-flows</span> or <span class="mono">flightbay-desk open --flows</span>.';
  const rows = ev.map((f, i) => {
    const req = f.source === 'coredns' ? `DNS lookup${f.method ? ' ' + esc(f.method) : ''}${f.dstIp ? ` <span class="muted">→ ${esc(f.dstIp)}</span>` : ''}` : [f.method, f.path, f.tool ? 'tool ' + f.tool : ''].filter(Boolean).map(esc).join(' ') || `<span class="muted">${esc(f.protocol || 'connection')}</span>`;
    const dst = [f.dstHost, f.dstIp && f.dstIp !== f.dstHost && f.source !== 'coredns' ? f.dstIp : ''].filter(Boolean).join(' · ') + (f.dstPort ? ':' + f.dstPort : '');
    const src = [f.srcNamespace && f.srcPod ? f.srcNamespace + '/' + f.srcPod : f.srcPod, f.srcIp, f.srcIdentity].filter(Boolean)[0] || '';
    const lq = rcLogQuery(f);
    return `<tr><td>${rcFoundChips([f.source])}</td><td class="rc-small">${esc(rcOrigin(f))}</td><td class="num">${num(f.count)}</td><td class="mono rc-small">${req}</td>
      <td class="mono rc-small">${esc(src)}</td><td class="mono rc-small">${esc(dst)}</td><td class="mono rc-small">${esc([f.route, f.via && f.via !== f.route ? 'via ' + f.via : ''].filter(Boolean).join(' · '))}</td>
      <td class="${f.verdict === 'denied' ? 'amber' : 'muted'} rc-small">${esc(f.verdict || '')}</td><td>${lq ? `<button class="link rc-small" data-rclogs="${esc(lq)}">log lines →</button>` : ''}</td></tr>`;
  }).join('');
  return `<div class="rc-ev-body"><div class="rc-ev-grid">
    <div><h4>${external ? 'How it is reached' : 'Policy verdict'}</h4>${policy}</div>
    <div><h4>Configured in</h4>${decl}</div>
    <div><h4>Flow sources</h4>${rcSources.map(([k]) => { const saw = ((external ? x.sources : x.observedBy) || []).includes(k), st = rcSrcState(k, agent); return `<div class="rc-srow">${saw ? `<span class="rc-src rc-src-${k}">${k}</span> <span class="green">saw it</span>` : `<span class="rc-src rc-src-off">${k}</span> <span class="${st.cls === 'green' ? 'amber' : 'muted'}">${st.cls === 'green' ? 'covers this agent, did not see it' : st.text}</span>`}</div>`; }).join('')}${x.ip && external ? `<div class="rc-small muted">address ${esc(x.ip)}${(x.sources || []).includes('coredns') ? ', named by its DNS lookup' : ''}</div>` : ''}</div></div>
    ${rcConfigLinks(x, external)}
    ${ev.length ? `<h4 class="mt">Flow records <small class="muted">${ev.length}${ev.length >= 20 ? ', the largest 20' : ''} · counts are per source, not summed</small></h4>
      <div class="table-scroll"><table class="rc-evt"><thead><tr><th>Source</th><th>From</th><th>Count</th><th>Request</th><th>Caller as logged</th><th>Destination as logged</th><th>Route</th><th>Verdict</th><th></th></tr></thead><tbody>${rows}</tbody></table></div>`
      : `<p class="muted mt">${seen ? 'Seen, but the records were merged away.' : none}</p>`}</div>`;
}
const rcClsTag = {registry: 'registry', proxy: 'proxy', llm: 'llm', git: 'git', storage: 'storage', exfil: 'exfil', metadata: 'metadata', unknown: 'unknown'};
const rcVia = x => ({direct: 'direct egress', gateway: 'through agentgateway', 'egress-gateway': 'egress waypoint', proxy: 'HTTP proxy', blocked: 'blocked', node: 'image pulls (node)'})[x.reachedVia] || x.reachedVia;
// svc:<cluster>/<ns>/svc/<name> -> {namespace, name}
const rcSvc = id => { if (!id || !id.startsWith('svc:')) return null; const p = id.slice(4).split('/'); return p.length >= 3 ? {namespace: p[p.length - 3], name: p[p.length - 1]} : null; };
const rcFields = {type: a => [a.type], ns: a => [a.namespace], namespace: a => [a.namespace], radius: a => [a.radius], internet: a => [a.internet]};
const rcKeys = [
  {value: 'type', hint: 'kagent-agent, mcp-server, agentgateway or possible-agent', example: 'type:mcp-server'},
  {value: 'ns', hint: 'namespace of the agent', example: 'ns:kagent'},
  {value: 'radius', hint: 'open, wide, narrow or contained', example: 'radius:open'},
  {value: 'internet', hint: 'allowed, denied or unknown', example: 'internet:allowed'},
];
function rcMatch(a, terms) {
  return terms.every(t => {
    let ok;
    if (t.key && rcFields[t.key]) { const w = t.val.toLowerCase(); ok = rcFields[t.key](a).some(v => String(v || '').toLowerCase().startsWith(w)); }
    else ok = (rcName(a) + ' ' + a.type + ' ' + (rcTypes[a.type] || '') + ' ' + a.summary).toLowerCase().includes((t.text ?? `${t.key}:${t.val}`).toLowerCase());
    return t.neg ? !ok : ok;
  });
}

// The agents list and one agent's detail. They are the Agents tab of the
// Traffic page; #reach links open that tab.
async function reach() { return reachBody(); }
async function reachBody() {
  const R = S.reach;
  if (!feature('reach')) {
    return `<section class="panel"><div class="empty"><strong>Agents is a Flightbay Enterprise feature</strong>
      It shows, agent by agent, what each agent, MCP server and agent gateway can reach on the network and how wide the blast radius is.<br>
      It was turned off: the desk was started with <code>FLIGHTBAY_ENTERPRISE=0</code> or <code>FLIGHTBAY_FEATURES=-reach</code>.</div></section>`;
  }
  const rev = S.state?.revision;
  if (!R.list || R.listRev !== rev) { R.list = await api('api/reach'); R.listRev = rev; R.flowSrc = (await api('api/flows', {limit: 1}).catch(() => ({}))).bySource || {}; }
  if (feature('traffic') && (!R.act || R.actRev !== rev)) { R.act = await api('api/activity').catch(() => null); R.actRev = rev; }
  const gw = new Map((R.act?.callers || []).map(c => [c.id, c]));
  const gwTok = (R.act?.callers || []).some(c => c.tokens > 0);
  const rep = R.list, all = rep.agents || [];
  const terms = qTerms(R.q);
  const shown = all.filter(a => (!R.type || a.type === R.type) && rcMatch(a, terms));
  let detail = '';
  if (R.sel) {
    const key = R.sel + '\u0000' + rev;
    if (R.detailKey !== key) {
      R.detail = await api('api/reach', {agent: R.sel}).catch(e => ({error: e.message}));
      R.usage = R.detail.error ? null : await api('api/reach/usage', {agent: R.sel}).catch(() => null);
      if (R.detailAgent !== R.sel) { R.fixOpen = {}; R.gexpand = {}; R.gz = null; R.evOpen = {}; R.ldAll = false; }
      R.detailKey = key; R.detailAgent = R.sel;
    }
    detail = R.detail.error ? `<div class="notice bad mt">${esc(R.detail.error)}</div>` : rcDetail(R.detail);
  }
  const typeCount = t => all.filter(a => a.type === t).length;
  const chip = (t, label, n) => `<button class="chip ${R.type === t ? 'on' : ''}" data-rctype="${esc(t)}">${esc(label)}${n !== undefined ? ` <small>${n}</small>` : ''}</button>`;
  const chips = chip('', 'All', all.length) + Object.keys(rcTypes).filter(typeCount).map(t => chip(t, rcTypes[t], typeCount(t))).join('');
  const isSel = a => R.sel && (rcName(a) === R.sel || a.id === R.sel);
  const sev = (n, s) => n ? `<span class="pst-sev pst-sev-${s}">${n} ${s === 'critical' ? 'crit' : s}</span>` : '';
  const multi = (rep.clusters || []).length > 1;
  const rows = shown.map(a => { const c = a.counts || {}; return `<tr class="${isSel(a) ? 'sel' : ''}" data-rcagent="${esc(rcName(a))}" title="Show the blast radius of ${esc(rcName(a))}">
    <td><div class="mono rc-agent-name">${esc(rcName(a))}</div><div class="row" style="gap:6px;margin-top:4px">${rcTypePill(a.type)}${multi ? `<span class="tag">${esc(a.cluster)}</span>` : ''}</div></td>
    <td>${rcBadge(a.radius, rcRadiusCls[a.radius])}</td><td>${rcBadge(a.internet, rcNetCls[a.internet])}</td>
    <td class="mono">${c.direct}<small class="muted"> / ${c.services}</small></td><td class="mono">${c.closure}</td><td class="mono ${c.write ? 'red' : ''}">${c.write}</td>
    <td class="mono">${a.observed ? `${c.observed}<small class="muted"> / ${c.direct}</small>` : '<span class="muted">—</span>'}</td>
    ${R.act ? (() => { const g = gw.get('w:' + a.id); return `<td class="mono">${g ? `${num(g.requests)}${g.denied ? ` <small class="amber">${num(g.denied)} refused</small>` : ''}` : '<span class="muted">—</span>'}</td>${gwTok ? `<td class="mono">${g?.tokens ? fbTok(g.tokens) : '<span class="muted">—</span>'}</td>` : ''}`; })() : ''}
    <td><div class="row" style="gap:5px">${sev(c.critical, 'critical')}${sev(c.high, 'high')}${!c.critical && !c.high ? '<span class="muted">none</span>' : ''}</div></td>
    <td class="rc-sum">${esc(a.summary)}</td></tr>`; }).join('');
  const notes = (rep.notes || []).map(n => `<div class="notice" style="margin-bottom:12px"><span>△</span><div>${esc(rcCap(n))}.</div></div>`).join('');
  const list = all.length ? `<section class="panel"><div class="panel-head"><h2>${shown.length === all.length ? all.length : `${shown.length} of ${all.length}`} agent${all.length === 1 ? '' : 's'}</h2><small class="muted">Sorted by risk. Click one for its blast radius.</small></div>
    <div class="table-scroll" style="max-height:${R.sel ? '38vh' : '66vh'}">${shown.length ? `<table class="rc-list"><thead><tr><th>Agent</th><th>Radius</th><th>Internet</th><th title="Services reachable in one hop, of the Services in the capture">Direct</th><th title="Services reachable in any number of hops">Total</th><th title="Direct destinations it can change data on">Write</th><th title="Destinations it was seen using, of those it may reach directly">Used</th>${R.act ? '<th title="Calls through agentgateway">Gateway calls</th>' + (gwTok ? '<th title="LLM tokens through agentgateway">Tokens</th>' : '') : ''}<th>Findings</th><th>Summary</th></tr></thead><tbody>${rows}</tbody></table>`
      : '<div class="empty"><strong>No agents match</strong>Change the filter or the type.</div>'}</div></section>`
    : `<section class="panel"><div class="empty"><strong>No agents found</strong>No kagent agents, MCP servers, agent gateways or workloads that look like agents were captured.</div></section>`;
  return notes + `
    <div class="toolbar"><div class="row wrap">${chips}<button id="rc-term-all" class="ghost">&gt;_ fbreach in terminal</button></div>
      <input id="rc-q" placeholder="Filter. Try type: ns: radius: internet: or text" value="${esc(R.q)}" style="min-width:320px" autocomplete="off" spellcheck="false"></div>
    ${list}${detail}`;
}

function rcDetail(a) {
  const R = S.reach, c = a.counts || {}, id = a.identity || {};
  const tile = (value, cls, caption, eyebrow) => `<div class="rc-tile"><div class="eyebrow">${esc(eyebrow)}</div><div class="rc-tile-v ${cls || ''}">${value}</div><small>${caption}</small></div>`;
  const tiles = [
    tile(esc(a.radius), rcRadiusCls[a.radius], esc(rcRadiusWhy[a.radius] || ''), 'Radius'),
    tile(esc(a.internet), rcNetCls[a.internet], a.metadata ? `cloud metadata ${esc(a.metadata)}` : 'direct egress', 'Internet'),
    tile(`${c.direct}<small> / ${c.services}</small>`, '', `Reaches ${c.direct} of ${c.services} Services`, 'Direct'),
    tile(String(c.closure), '', `${c.closure} in any number of hops`, 'Total'),
    tile(String(c.write), c.write ? 'red' : 'green', `Can write to ${c.write}`, 'Write'),
    a.observed ? tile(String(c.observed), '', `Called ${c.observed} in the capture, seen by ${esc(rcSrcList(a.observedBy))}`, 'Used') : tile('—', 'muted', 'no flow logs cover this agent', 'Used'),
    a.observed ? tile(String(c.neverUsed), c.neverUsed ? 'amber' : 'green', `${c.neverUsed} allowed but never called, per ${esc(rcSrcList(a.observedBy))}`, 'Never used') : tile('—', 'muted', 'needs flow logs (ztunnel, agentgateway, Hubble…) to tell', 'Never used'),
    tile(String(c.external || 0), (a.external || []).some(x => x.risk === 'red') ? 'red' : '', `hosts outside the cluster${(a.external || []).filter(x => x.reachedVia === 'gateway').length ? `, ${(a.external || []).filter(x => x.reachedVia === 'gateway').length} through the gateway` : ''}`, 'External'),
  ].join('');
  const header = `<section class="panel mt" id="rc-detail"><div class="panel-body">
    <div class="row between wrap" style="align-items:flex-start"><div class="row wrap"><h2 class="mono" style="font-size:17px">${esc(rcName(a))}</h2>${rcTypePill(a.type)}<span class="tag">${esc(a.kind)}</span>${(R.list?.clusters || []).length > 1 ? `<span class="tag">${esc(a.cluster)}</span>` : ''}</div>
      <div class="row wrap">${a.ref ? `<button id="rc-obj">Open ${esc(a.kind)}</button>` : ''}<button id="rc-logs">Logs from this agent</button>${feature('traffic') ? `<a class="btn" href="#traffic?agent=${encodeURIComponent(rcName(a))}" title="Its connections and gateway calls on the Traffic page">Traffic</a>` : ''}<button id="rc-term">&gt;_ fbreach in terminal</button><button class="ghost" id="rc-close" title="Back to the list" aria-label="Close">✕</button></div></div>
    <p class="rc-summary">${esc(a.summary)}</p><p class="muted" style="font-size:12px;margin-top:4px">Detected: ${esc(a.why)}${a.observed ? ` · Observed by ${esc(rcSrcList(a.observedBy))}` : ' · No flow logs cover this agent, so what it actually calls is unknown'}</p>
    <div class="rc-tiles">${tiles}</div></div></section>`;
  const G = rcGraphModel(a);
  const vbtn = (v, label, hint) => `<button class="chip ${G.view === v ? 'on' : ''}" data-rgview="${v}" title="${esc(hint)}">${label}</button>`;
  const graph = `<section class="panel mt rc-gpanel ${R.gfull ? 'full' : ''}" id="rc-gpanel"><div class="panel-head rc-ghead"><div class="row wrap"><h2>Blast radius</h2>
      ${vbtn('risky', 'Risky', 'Red destinations and anything leading out of the cluster; the rest folded by namespace')}${vbtn('byns', 'By namespace', 'One box per namespace; click one to open it up')}${vbtn('all', 'Everything', 'Every destination as its own box')}
      ${R.usage ? `<span class="rc-layer">${[['allowed', 'Allowed', 'What policy allows, as before'], ['used', 'Used', 'Only what it was seen using'], ['both', 'Both', 'Everything allowed, with what it used drawn solid and the rest faded']].map(([k, l, h]) => `<button class="chip ${(R.glayer || 'both') === k ? 'on' : ''}" data-rglayer="${k}" title="${esc(h)}">${l}</button>`).join('')}</span>` : ''}
      ${rcFoundMenu([...(a.direct || []).map(e => ({found: rcFound(e, false)})), ...(a.external || []).filter(x => x.reachedVia !== 'node').map(x => ({found: rcFound(x, true)}))], R.gfound || 'any', 'data-rggf', a)}
      ${Object.keys(R.gexpand).length ? '<button class="ghost" id="rg-collapse">Fold namespaces</button>' : ''}<small class="muted">${G.layer === 'used' ? `${G.usedN} used of the ${G.total} it may reach` : G.folded ? `${G.shown} of ${G.total} shown one by one, ${G.folded} folded into ${G.groups} namespace${G.groups === 1 ? '' : 's'}` : `all ${G.total} destinations`}</small></div>
      <div class="row rc-zoom"><button id="rg-out" title="Zoom out (-)">−</button><span id="rg-pct" class="mono muted">100%</span><button id="rg-in" title="Zoom in (+)">+</button><button id="rg-fit" title="Fit (0)">Fit</button><button id="rg-full" title="${R.gfull ? 'Exit full screen (Esc)' : 'Full screen (f)'}">${R.gfull ? '⤡ Exit' : '⤢ Full screen'}</button></div></div>
    <div class="rg-vp" id="rg-vp" tabindex="0">${rcGraph(a, G)}</div>
    ${rcGraphSel(a, G)}
    <div class="rc-legend"><span><i class="rc-sw red"></i>open, write access or internet</span><span><i class="rc-sw amber"></i>allowed with no request-level rule, or unknown</span><span><i class="rc-sw green"></i>read-only or blocked</span><span><i class="rc-sw grey"></i>blocked or not applicable</span><span><i class="rc-sw dash"></i>denied or unknown verdict</span><span>${rgIcon('eye', 0, 0, 14, 'rg-leg-eye')} seen in traffic</span><span class="muted">Hover a box to trace its lines · click it for how it was found · drag to move · pinch or Ctrl+scroll to zoom</span></div></section>`;
  const kindCls = {internet: 'red', fetcher: 'red', registry: 'red', exfil: 'red', metadata: 'red', credentials: 'red', store: 'amber', service: 'amber'};
  const paths = (a.paths || []).map(p => `<div class="rc-path"><span class="rc-kind ${kindCls[p.kind] || ''}">${esc(p.kind)}</span>
    <div class="rc-chain">${p.hops.map(h => `<span class="rc-hop ${h === 'internet' || h === '169.254.169.254' ? 'net' : ''}">${esc(h)}</span>`).join('<span class="rc-arrow">→</span>')}</div>
    <small class="muted rc-why">${esc(p.why)}</small></div>`).join('');
  const pathsPanel = `<section class="panel mt"><div class="panel-head"><h2>Paths</h2><small class="muted">From the agent to the internet, cloud metadata, fetchers and stores</small></div>
    ${paths || '<div class="empty"><strong>No path to the internet or cloud metadata</strong>Nothing this agent can reach leads out of the cluster.</div>'}</section>`;
  // Findings: one line each; open one for the affected list and the fix.
  const itemTable = f => {
    const its = f.items || [];
    if (!its.length) return '';
    const hasCls = its.some(x => x.class), hasAcc = its.some(x => x.access), hasOth = its.some(x => (x.others || []).length), hasNote = its.some(x => x.note);
    const link = t => t.includes('/') ? `<button class="link mono" data-rcsvc="${attr({cluster: a.cluster, group: '', resource: 'services', namespace: t.split('/')[0], name: t.split('/')[1]})}">${esc(t)}</button>` : `<span class="mono">${esc(t)}</span>`;
    return `<div class="table-scroll rc-items"><table><thead><tr><th>Affected</th>${hasCls ? '<th>What it is</th>' : ''}${hasAcc ? '<th>Allowed</th>' : ''}${hasOth ? '<th>Also reachable by</th>' : ''}${hasNote ? '<th></th>' : ''}</tr></thead><tbody>
      ${its.map(x => `<tr><td>${link(x.target)}</td>${hasCls ? `<td>${esc(x.class || '')}</td>` : ''}${hasAcc ? `<td class="mono red">${esc(x.access || '')}</td>` : ''}${hasOth ? `<td class="rc-small">${(x.others || []).length} other agent${(x.others || []).length === 1 ? '' : 's'} <span class="muted">${esc((x.others || []).join(', '))}</span></td>` : ''}${hasNote ? `<td class="muted rc-small">${esc(x.note || '')}</td>` : ''}</tr>`).join('')}</tbody></table></div>`;
  };
  const findings = (a.findings || []).map((f, i) => {
    const open = !!R.fixOpen[i];
    const n = (f.items || []).length;
    return `<div class="rc-finding ${open ? 'open' : ''}"><button class="rc-fhead" data-rcfopen="${i}">
      <span class="pst-sev pst-sev-${esc(f.severity)}">${esc(f.severity)}</span><strong>${esc(f.title)}</strong>
      ${n ? `<span class="tag">${n} affected</span>` : ''}<span style="flex:1"></span>${f.fix ? '<small class="muted">fix included</small>' : ''}<span class="muted">${open ? '▾' : '▸'}</span></button>
      ${open ? `<div class="rc-fbody"><p>${esc(f.detail)}</p>${itemTable(f)}${(f.advice || []).length ? `<h3 class="mt">Other steps</h3><ul class="rc-advice">${f.advice.map(x => `<li>${esc(x)}</li>`).join('')}</ul>` : ''}
        ${f.fix ? `<div class="row between mt"><h3>How to fix</h3><div class="row"><button data-rccopy="${i}">Copy YAML</button>${guideLinks(f.source, f.docs)}</div></div><pre class="code mt">${esc(f.fix)}</pre>`
          : f.source || f.docs ? `<p class="mt row">${guideLinks(f.source, f.docs)}</p>` : ''}</div>` : ''}</div>`;
  }).join('');
  const findingsPanel = `<section class="panel"><div class="panel-head"><h2>Findings</h2><small class="muted">${c.critical || 0} critical · ${c.high || 0} high · ${(a.findings || []).length} in all · open one for what it affects and the fix</small></div>
    ${findings || '<div class="empty"><strong>No findings</strong></div>'}</section>`;
  const yes = (b, good) => b ? `<span class="${good ? 'green' : 'amber'}">yes</span>` : `<span class="${good ? 'amber' : 'green'}">no</span>`;
  const lst = (xs, cls, none) => (xs || []).length ? xs.map(x => `<div class="${cls || ''}">${esc(x)}</div>`).join('') : `<span class="muted">${none || 'none'}</span>`;
  const identity = `<section class="panel"><div class="panel-head"><h2>Identity</h2><small class="muted">Service account, token, RBAC, Secrets and host access</small></div><div class="panel-body">
    <dl class="metadata rc-id"><dt>Service account</dt><dd class="mono">${esc(id.serviceAccount || '—')}${id.principal ? `<br><small class="muted">${esc(id.principal)}</small>` : ''}</dd>
      <dt>In mesh</dt><dd>${yes(id.inMesh, true)}</dd>
      <dt>Token mounted</dt><dd>${yes(id.tokenMounted, false)}</dd>
      <dt>RBAC rules</dt><dd class="mono rc-small">${lst(id.rbac)}${id.rbacComplete === false ? '<div class="amber">RBAC was not fully captured</div>' : ''}</dd>
      <dt>Dangerous rules</dt><dd class="rc-small">${lst(id.dangerous, 'red')}</dd>
      <dt>Cloud role</dt><dd class="mono rc-small">${id.cloudRole ? esc(id.cloudRole) : '<span class="muted">none</span>'}</dd>
      <dt>Secrets</dt><dd>${(id.secrets || []).length ? id.secrets.map(x => `<span class="tag">${esc(x)}</span>`).join(' ') : '<span class="muted">none</span>'}</dd>
      <dt>Host access</dt><dd class="rc-small">${lst(id.hostAccess, 'red')}</dd>
      <dt>Sandbox</dt><dd>${id.sandbox ? `<span class="green">${esc(id.sandbox)}</span>` : '<span class="muted">none</span>'}</dd>
      <dt>Shared volumes</dt><dd class="rc-small">${lst(id.sharedVolumes, 'amber')}</dd></dl></div></section>`;
  // Destinations: every direct edge.
  const never = new Set(a.neverUsed || []);
  const dest = e => { const s = rcSvc(e.to); return s ? s.namespace + '/' + s.name : e.to.replace(/^(ext|svc):/, ''); };
  const direct = [...(a.direct || [])].sort((x, y) => (rcRiskRank[x.risk] ?? 9) - (rcRiskRank[y.risk] ?? 9));
  const dterms = qTerms(R.dq);
  const chipOk = (e, k) => k === 'risky' ? (e.risk === 'red' || e.risk === 'amber') : k === 'used' ? e.observed > 0 : k === 'never' ? never.has(dest(e)) : true;
  const dChipOk = e => chipOk(e, R.dchip);
  const dMatch = e => dterms.every(t => { const ok = (dest(e) + ' ' + e.verdict + ' ' + e.risk + ' ' + e.why + ' ' + (e.reasons || []).join(' ') + ' ' + (e.methods || ['any']).join(' ')).toLowerCase().includes((t.text ?? `${t.key}:${t.val}`).toLowerCase()); return t.neg ? !ok : ok; });
  direct.forEach(e => { e.found = rcFound(e, false); });
  const drows = direct.filter(e => dChipOk(e) && dMatch(e) && rcFoundOk(e.found, R.dfound || 'any'));
  const counts = Object.fromEntries(['all', 'risky', 'used', 'never'].map(k => [k, direct.filter(e => chipOk(e, k)).length]));
  const dchip = (k, label) => `<button class="chip ${R.dchip === k ? 'on' : ''}" data-rcd="${k}">${label} <small>${counts[k]}</small></button>`;
  const cl = a.cluster;
  const destTable = `<section class="panel mt"><div class="panel-head"><h2>Destinations</h2><small class="muted">Services it can reach in one hop, sorted by risk${a.observed ? ` · calls seen by ${esc(rcSrcList(a.observedBy))}` : ''}. Hosts outside the cluster are on the External tab</small></div><div class="panel-body" style="padding-bottom:0">
    <div class="toolbar"><div class="row wrap">${dchip('all', 'All')}${dchip('risky', 'Risky')}${dchip('used', 'Used')}${dchip('never', 'Never used')}${rcFoundMenu(direct, R.dfound || 'any', 'data-rcdf', a)}</div>
      <input id="rc-dq" placeholder="Filter destinations" value="${esc(R.dq)}" autocomplete="off" spellcheck="false" style="min-width:240px"></div>
</div>
    <div class="table-scroll" style="max-height:60vh">${drows.length ? `<table class="rc-dest"><thead><tr><th>Risk</th><th>Destination</th><th>Verdict</th><th>Methods</th><th>Called</th><th>Found by</th><th>Why</th></tr></thead><tbody>
      ${drows.map(e => { const s = rcSvc(e.to); const open = !!(R.evOpen || {})[e.to]; return `<tr class="rc-evrow ${open ? 'open' : ''}"><td>${rcRisk(e.risk)}</td>
        <td class="mono rc-dst">${s ? `<button class="link" data-rcsvc="${attr({cluster: cl, group: '', resource: 'services', namespace: s.namespace, name: s.name})}">${esc(dest(e))}</button>` : esc(dest(e))}${never.has(dest(e)) ? '<br><small class="muted">never used</small>' : ''}</td>
        <td class="${e.verdict === 'allowed' ? '' : e.verdict === 'denied' ? 'green' : 'amber'}">${esc(e.verdict)}${e.layer ? ` <small class="muted">${esc(e.layer)}</small>` : ''}</td>
        <td class="mono rc-small">${e.methods == null ? '<span class="muted">any method</span>' : esc(e.methods.join(', ') || 'none')}${e.write ? ' <span class="red">· write</span>' : ''}</td>
        <td class="rc-small">${e.observed ? `${num(e.observed)}${(e.observedMethods || []).length ? ` <span class="muted mono">${esc(e.observedMethods.join(', '))}</span>` : ''}${e.observedDenied ? ` <span class="amber">· ${num(e.observedDenied)} denied</span>` : ''}${(e.tools || []).length ? `<div class="rc-tools">tools ${e.tools.map(t => `<span class="mono">${esc(t)}</span>`).join(', ')}</div>` : ''}<div class="rc-srcs">${rcSrcs(e.observedBy)}</div>` : '<span class="muted">not seen</span>'}</td>
        <td class="rc-found"><button class="rc-evbtn" data-rcev="${esc(e.to)}" title="Show the evidence">${rcFoundChips(e.found)}<span class="muted">${open ? '▾' : '▸'}</span></button></td>
        <td class="rc-wrap">${esc(e.why)}${(e.reasons || []).length ? `<br><small class="muted">${esc(e.reasons.join('; '))}</small>` : ''}${e.via ? `<br><small class="muted">via ${esc(e.via)}</small>` : ''}</td></tr>${open ? `<tr class="rc-ev"><td colspan="7">${rcEvidence(e, false, a)}</td></tr>` : ''}`; }).join('')}
    </tbody></table>` : `<div class="empty"><strong>${direct.length ? 'No destinations match' : 'No destinations'}</strong>${direct.length ? 'Change the filter above.' : 'Nothing in the cluster is reachable from this agent.'}</div>`}</div></section>`;
  const ext = a.external || [];
  const eChipOk = (x, k) => k === 'registries' ? (x.class === 'registry' || x.class === 'proxy') : k === 'llm' ? x.class === 'llm' : k === 'direct' ? x.reachedVia === 'direct' : k === 'gateway' ? x.reachedVia === 'gateway' : k === 'observed' ? (x.observed > 0 || x.lookups > 0) : true;
  const echips = [['all', 'All'], ['registries', 'Registries'], ['llm', 'LLM'], ['direct', 'Direct'], ['gateway', 'Through gateway'], ['observed', 'Observed']];
  ext.forEach(x => { x.found = rcFound(x, true); });
  const erows = ext.filter(x => eChipOk(x, R.echip) && rcFoundOk(x.found, R.efound || 'any'));
  const eHow = x => {
    const bits = [`<strong>${esc(rcVia(x))}</strong>`];
    if (x.reachedVia === 'gateway') bits.push(`<div class="mono rc-small">${esc(x.gateway || '')}${x.route ? ` · route ${esc(x.route)}` : ''}</div><div class="rc-small">${x.authn ? '<span class="green">authn required</span>' : '<span class="amber">no authn</span>'}${x.readOnly ? ' · <span class="green">read-only</span>' : ' · <span class="amber">any method</span>'}</div>`);
    else if (x.gateway) bits.push(`<div class="mono rc-small">${esc(x.gateway)}</div>`);
    if (x.reachedVia !== 'node' && x.verdict && x.reachedVia !== 'blocked') bits.push(`<div class="rc-small">${x.reachedVia === 'gateway' ? `direct egress <span class="${rcNetCls[x.directEgress] || ''}">${esc(x.directEgress || '—')}</span>` : `egress <span class="${rcNetCls[x.verdict] || ''}">${esc(x.verdict)}</span>`}</div>`);
    if (x.gatewayRoute) bits.push(`<div class="rc-small muted">a route exists: ${esc(x.gatewayRoute)}</div>`);
    return bits.join('');
  };
  const eCred = x => (x.credentials || []).length || (x.gatewayCredentials || []).length
    ? `${(x.credentials || []).map(c => `<div class="mono rc-small red">${esc(c)}</div>`).join('')}${(x.gatewayCredentials || []).map(c => `<div class="mono rc-small amber" title="The gateway adds this on the way">${esc(c)} <span class="muted">gateway</span></div>`).join('')}`
    : '<span class="muted">none seen</span>';
  const eObs = x => x.observed || x.lookups
    ? `${x.observed ? num(x.observed) : ''}${(x.methods || []).length ? ` <span class="mono ${x.writeSeen ? 'red' : 'muted'}">${esc(x.methods.join(', '))}</span>` : ''}${x.lookups ? `<div class="rc-small muted">${num(x.lookups)} DNS lookup${x.lookups === 1 ? '' : 's'}</div>` : ''}${(x.tools || []).length ? `<div class="rc-tools">tools ${x.tools.map(t => `<span class="mono">${esc(t)}</span>`).join(', ')}</div>` : ''}<div class="rc-srcs">${rcSrcs(x.sources)}</div>`
    : '<span class="muted">not seen</span>';
  const extTable = `<section class="panel mt"><div class="panel-head"><h2>External</h2><small class="muted">Hosts outside the cluster it is configured for, was seen calling, or can reach through a gateway</small></div><div class="panel-body" style="padding-bottom:0">
    <div class="toolbar"><div class="row wrap">${echips.map(([k, label]) => `<button class="chip ${R.echip === k ? 'on' : ''}" data-rce="${k}">${label} <small>${ext.filter(x => eChipOk(x, k)).length}</small></button>`).join('')}${rcFoundMenu(ext, R.efound || 'any', 'data-rcef', a)}</div></div>
</div>
    <div class="table-scroll" style="max-height:62vh">${erows.length ? `<table class="rc-ext"><thead><tr><th>Host</th><th>What it is</th><th>How it's reached</th><th>Credential</th><th>Observed</th><th>Configured in</th><th>Found by</th><th>Risk</th></tr></thead><tbody>
      ${erows.map(x => { const open = !!(R.evOpen || {})['ext:' + x.host]; return `<tr class="rc-evrow ${open ? 'open' : ''} ${x.reachedVia === 'node' ? 'rc-dim' : ''}"><td class="mono rc-host">${esc(x.host)}${x.ip && x.ip !== x.host ? `<br><small class="muted">${esc(x.ip)}</small>` : ''}</td>
        <td class="rc-what"><span class="rc-cls rc-cls-${esc(x.class)}">${esc(rcClsTag[x.class] || x.class)}</span><div class="rc-small">${esc(x.product || '')}</div></td>
        <td class="rc-how">${eHow(x)}</td><td class="rc-cred">${eCred(x)}</td><td class="rc-small rc-obs">${eObs(x)}</td>
        <td class="rc-small rc-in">${(x.declaredIn || []).map(d => `<div>${esc(d)}</div>`).join('') || '<span class="muted">not configured</span>'}</td>
        <td class="rc-found"><button class="rc-evbtn" data-rcev="${esc('ext:' + x.host)}" title="Show the evidence">${rcFoundChips(x.found)}<span class="muted">${open ? '▾' : '▸'}</span></button></td>
        <td class="rc-riskcell">${rcRisk(x.risk)}<div class="rc-small muted">${esc(x.why)}</div></td></tr>${open ? `<tr class="rc-ev"><td colspan="8">${rcEvidence(x, true, a)}</td></tr>` : ''}`; }).join('')}
    </tbody></table>` : `<div class="empty"><strong>${ext.length ? 'No hosts match' : 'No external destinations'}</strong>${ext.length ? 'Choose another filter above.' : 'Nothing in its configuration, its gateways or the flow logs points outside the cluster.'}</div>`}</div></section>`;
  const U = R.usage;
  const tabs = [['graph', 'Blast radius', ''], ...(U ? [['usage', 'What it does', U.totals.used]] : []), ...(R.act && typeof rcToolsTab === 'function' ? [['tools', 'Tools and models', (R.act.edges || []).filter(e => e.from === 'w:' + a.id && ['tool', 'model', 'agent'].includes((R.act.targets || []).find(x => x.id === e.to)?.kind)).length]] : []), ...(U ? [['lockdown', 'Lock down', '']] : []), ['findings', 'Findings', (a.findings || []).length], ['paths', 'Paths', (a.paths || []).length], ['dest', 'Destinations', direct.length], ['external', 'External', ext.filter(x => x.reachedVia !== 'node').length], ['identity', 'Identity', '']];
  const tab = R.dtab || 'graph';
  const crit = (c.critical || 0) + (c.high || 0);
  const tabRed = k => (k === 'findings' && crit) || (k === 'external' && ext.some(x => x.risk === 'red'));
  const tabBar = `<nav class="pst-tabs rc-tabs">${tabs.map(([k, label, n]) => `<button class="pst-tab ${tab === k ? 'on' : ''}" data-rctab="${k}">${label}${n !== '' && n ? `<span class="pst-tab-n ${tabRed(k) ? 'red' : ''}">${n}</span>` : ''}</button>`).join('')}</nav>`;
  const body = {graph: graph + pathsPanel, usage: U && typeof rcUsageTab === 'function' ? rcUsageTab(a, U) : '', lockdown: U && typeof rcLockTab === 'function' ? rcLockTab(a, U) : '', tools: R.act && typeof rcToolsTab === 'function' ? rcToolsTab(a, R.act) : '', findings: `<div class="mt">${findingsPanel}${R.act && typeof rcGwFindings === 'function' ? rcGwFindings(a, R.act) : ''}</div>`, paths: pathsPanel, dest: destTable, external: extTable, identity: `<div class="mt">${identity}</div>`}[tab] || graph;
  return header + tabBar + body;
}

// rcGraphModel turns the agent's nodes and direct edges into the graph for
// the chosen view. The server folds unremarkable destinations into one node
// per namespace; here every destination is known (from a.direct), so the
// view decides what to draw: only the risky ones, one box per namespace, or
// everything. Folded namespaces open up when clicked.
function rcGraphModel(a) {
  const R = S.reach;
  const svcParts = id => { const p = id.slice(4).split('/'); return {ns: p[p.length - 3], name: p[p.length - 1]}; };
  let nodes = (a.nodes || []).filter(n => n.type !== 'group');
  let edges = (a.edges || []).filter(e => e.from !== e.to && !e.to.startsWith('group:'));
  const have = new Set(nodes.map(n => n.id));
  const agentId = (nodes.find(n => n.type === 'agent') || {}).id;
  for (const e of a.direct || []) {
    if (have.has(e.to) || !e.to.startsWith('svc:') || e.verdict === 'denied') continue;
    const p = svcParts(e.to);
    nodes.push({id: e.to, type: 'service', label: p.ns + '/' + p.name, namespace: p.ns, depth: 1, risk: e.risk, detail: e.why});
    have.add(e.to);
    edges.push(e);
  }
  const first = nodes.filter(n => n.type === 'service' && (n.depth || 1) === 1 && edges.some(e => e.from === agentId && e.to === n.id));
  // Every Service it reaches in one hop, including the few drawn further
  // right because a gateway also leads to them.
  const directSvc = (a.direct || []).filter(e => e.to.startsWith('svc:') && e.verdict !== 'denied');
  const directBy = new Map(directSvc.map(e => [e.to, e]));
  for (const n of nodes) if (n.type === 'service' && (n.depth || 1) > 1 && directBy.has(n.id)) n.alsoDirect = directBy.get(n.id).risk;
  const direct = {n: directSvc.length, counts: directSvc.reduce((m, e) => (m[e.risk] = (m[e.risk] || 0) + 1, m), {})};
  const total = Math.max(first.length, directSvc.length);
  const view = R.gview || (total > 14 ? 'risky' : 'all');
  const layer = R.usage ? (R.glayer || 'both') : 'allowed';
  const UD = new Map((R.usage?.destinations || []).map(d => [d.to, d]));
  const outgoing = new Set(edges.map(e => e.from));
  const keep = n => view === 'all' || layer === 'used' || R.gexpand[n.namespace] || outgoing.has(n.id) || (view === 'risky' && n.risk === 'red' && !/^group:/.test(n.id));
  const rank = {red: 0, amber: 1, green: 2, grey: 3};
  const groups = {};
  for (const n of first) if (!keep(n)) (groups[n.namespace] ||= []).push(n);
  const folded = new Set(Object.values(groups).flat().map(n => n.id));
  nodes = nodes.filter(n => !folded.has(n.id));
  edges = edges.filter(e => !folded.has(e.to));
  for (const [ns, list] of Object.entries(groups)) {
    const worst = list.reduce((w, n) => rank[n.risk] < rank[w] ? n.risk : w, 'grey');
    const id = 'fold:' + ns;
    nodes.push({id, type: 'group', label: ns, namespace: ns, depth: 1, risk: worst, count: list.length, members: list.map(n => n.id), detail: list.map(n => n.label.split('/').pop()).join(', ')});
    edges.push({from: agentId, to: id, verdict: 'allowed', risk: worst, why: `${list.length} destinations in ${ns}; click to open the namespace up`});
  }
  let usedN = 0;
  if (layer !== 'allowed') {
    const U = UD;
    const directOut = [...U.values()].some(d => d.used && d.kind === 'external' && (d.via === 'direct' || !d.via));
    const usedId = id => {
      if (id.startsWith('fold:')) { const g = nodes.find(n => n.id === id); return (g?.members || []).some(m => U.get(m)?.used); }
      if (id === 'internet') return directOut;
      return !!U.get(id)?.used;
    };
    for (const e of edges) {
      const d = U.get(e.to);
      e.used = usedId(e.to); e.usedDenied = !!d?.mismatch;
      // Unused is only known when a flow source covers the agent; otherwise it is unknown, not unused.
      if (e.from === agentId) e.unused = !!a.observed && !e.used && e.to !== 'metadata';
    }
    for (const n of nodes) if (n.type !== 'agent' && edges.some(e => e.to === n.id && e.from === agentId)) n.unused = !!a.observed && !usedId(n.id) && n.id !== 'metadata';
    usedN = [...U.values()].filter(d => d.used && d.kind === 'service').length;
    if (layer === 'used') {
      // Only what it was seen using: its direct destinations, and the hosts
      // outside the cluster it called, through whatever led there.
      const keepE = edges.filter(e => e.used);
      const reach = new Set([agentId]);
      for (let changed = true; changed;) { changed = false; for (const e of keepE) if (reach.has(e.from) && !reach.has(e.to)) { reach.add(e.to); changed = true; } }
      nodes = nodes.filter(n => reach.has(n.id));
      edges = keepE.filter(e => reach.has(e.from) && reach.has(e.to));
    }
  }
  return {nodes, edges, view, total, direct, layer, usedN, shown: total - folded.size, folded: folded.size, groups: Object.keys(groups).length};
}

// Line icons for the graph (24x24, stroked), drawn as nested SVGs.
const rgIconPaths = {
  bot: '<rect x="3" y="11" width="18" height="10" rx="2"/><circle cx="12" cy="5" r="2"/><path d="M12 7v4M8 16h.01M16 16h.01"/>',
  network: '<rect x="16" y="16" width="6" height="6" rx="1"/><rect x="2" y="16" width="6" height="6" rx="1"/><rect x="9" y="2" width="6" height="6" rx="1"/><path d="M5 16v-3a1 1 0 0 1 1-1h12a1 1 0 0 1 1 1v3M12 12V8"/>',
  router: '<rect x="2" y="14" width="20" height="8" rx="2"/><path d="M6 18h.01M10 18h.01M15 10v4M17.84 7.17a4 4 0 0 0-5.66 0M20.66 4.34a8 8 0 0 0-11.31 0"/>',
  route: '<circle cx="6" cy="19" r="3"/><path d="M9 19h8.5a3.5 3.5 0 0 0 0-7h-11a3.5 3.5 0 0 1 0-7H15"/><circle cx="18" cy="5" r="3"/>',
  globe: '<circle cx="12" cy="12" r="10"/><path d="M2 12h20M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/>',
  cloud: '<path d="M17.5 19H9a7 7 0 1 1 6.71-9h1.79a4.5 4.5 0 1 1 0 9Z"/>',
  server: '<rect x="2" y="2" width="20" height="8" rx="2"/><rect x="2" y="14" width="20" height="8" rx="2"/><path d="M6 6h.01M6 18h.01"/>',
  database: '<ellipse cx="12" cy="5" rx="9" ry="3"/><path d="M3 5v14a9 3 0 0 0 18 0V5M3 12a9 3 0 0 0 18 0"/>',
  key: '<circle cx="7.5" cy="15.5" r="5.5"/><path d="m21 2-9.6 9.6M15.5 7.5l3 3L22 7l-3-3"/>',
  plug: '<path d="M12 22v-5M9 8V2M15 8V2M18 8v5a4 4 0 0 1-4 4h-4a4 4 0 0 1-4-4V8Z"/>',
  sparkles: '<path d="M12 3l1.9 5.1L19 10l-5.1 1.9L12 17l-1.9-5.1L5 10l5.1-1.9z"/><path d="M19 16v5M16.5 18.5h5"/>',
  git: '<circle cx="6" cy="6" r="3"/><circle cx="6" cy="18" r="3"/><circle cx="18" cy="6" r="3"/><path d="M6 9v6M18 9a9 9 0 0 1-9 9"/>',
  package: '<path d="M21 8 12 3 3 8v8l9 5 9-5z"/><path d="M3 8l9 5 9-5M12 13v8"/>',
  download: '<path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4M7 10l5 5 5-5M12 15V3"/>',
  folder: '<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>',
  alert: '<path d="M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z"/><path d="M12 9v4M12 17h.01"/>',
  swap: '<path d="M16 3l4 4-4 4M20 7H4M8 21l-4-4 4-4M4 17h16"/>',
  service: '<circle cx="12" cy="12" r="9"/><circle cx="12" cy="12" r="3"/>',
  eye: '<path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12z"/><circle cx="12" cy="12" r="3"/>',
};
const rgIcon = (name, x, y, size, cls = '') => `<svg class="rg-ic ${cls}" x="${x}" y="${y}" width="${size}" height="${size}" viewBox="0 0 24 24">${rgIconPaths[name] || rgIconPaths.service}</svg>`;
// rgNodeIcon picks the icon for a box from its type, then its first telling class.
const rgClassIcon = {gateway: 'router', waypoint: 'router', store: 'database', storage: 'database', credentials: 'key', mcp: 'plug', llm: 'sparkles', git: 'git', registry: 'package', fetcher: 'download', exfil: 'alert', proxy: 'swap', apiserver: 'server', metadata: 'server'};
function rgNodeIcon(n) {
  const byType = {agent: 'bot', group: 'folder', internet: 'globe', metadata: 'server'}[n.type];
  if (byType) return byType;
  const c = (n.classes || []).find(k => rgClassIcon[k]);
  return c ? rgClassIcon[c] : n.type === 'external' ? 'cloud' : 'service';
}

// rgSubject is the destination behind a graph box: its direct edge, its
// external host, or failing both the line that leads to it.
function rgSubject(a, G, n) {
  if (n.type === 'external') { const x = (a.external || []).find(x => 'ext:' + x.host === n.id); return x ? {x, external: true} : null; }
  if (n.type !== 'service') return null;
  const e = (a.direct || []).find(e => e.to === n.id) || G.edges.find(e => e.to === n.id);
  return e ? {x: e, external: false} : null;
}
function rgFoundOf(a, G, n) {
  if (n.type === 'group') return [...new Set((n.members || []).flatMap(id => { const e = (a.direct || []).find(e => e.to === id); return e ? rcFound(e, false) : []; }))];
  const s = rgSubject(a, G, n);
  return s ? rcFound(s.x, s.external) : [];
}
// rgGroupOk: a folded namespace matches when any Service in it does.
function rgMatch(a, G, n, k) {
  if (!k || k === 'any' || n.type === 'agent') return true;
  if (n.type === 'group') return (n.members || []).some(id => { const e = (a.direct || []).find(e => e.to === id); return e && rcFoundOk(rcFound(e, false), k); });
  if (n.type === 'internet' || n.type === 'metadata') return false;
  return rcFoundOk(rgFoundOf(a, G, n), k);
}
// rcGraphSel is the evidence for the box clicked in the graph.
function rcGraphSel(a, G) {
  const R = S.reach, n = R.gsel && G.nodes.find(x => x.id === R.gsel);
  if (!n) return '';
  const s = rgSubject(a, G, n);
  if (!s) return '';
  const sv = rcSvc(n.id);
  return `<div class="rc-gsel"><div class="row between wrap"><div class="row wrap"><strong class="mono">${esc(n.label)}</strong>${rcFoundChips(rcFound(s.x, s.external))}</div>
    <div class="row">${sv && feature('traffic') ? `<a class="btn" href="#traffic?from=${encodeURIComponent(rcName(a))}&to=${encodeURIComponent(sv.namespace + '/' + sv.name)}" title="Every check between the agent and this Service">Why?</a>` : ''}${sv ? `<button data-rgopen="${esc(n.id)}">Open Service</button>` : ''}<button class="ghost" id="rg-selx" title="Close">✕</button></div></div>
    ${typeof rcUseDetail === 'function' ? rcUseDetail(n.id) : ''}${rcEvidence(s.x, s.external, a)}</div>`;
}

// rcGraph draws the model as columns: the agent, its direct destinations,
// what they reach, then the way out. Lines between adjacent columns run along
// a short trunk per source ("bus" routing) so a wide fan-out stays readable;
// a line that skips a column runs along a lane above every box.
function rcGraph(a, G) {
  const nodes = G.nodes, ids = new Set(nodes.map(n => n.id));
  const es = G.edges.filter(e => ids.has(e.from) && ids.has(e.to));
  if (!nodes.length) return '<div class="empty"><strong>No graph for this agent</strong></div>';
  const inbound = {};
  es.forEach(e => (inbound[e.to] ||= []).push(e.from));
  const out = new Set(['internet', 'metadata', 'external']);
  const col = {};
  let maxSvc = 0;
  for (const n of nodes) if (n.type !== 'agent' && !out.has(n.type)) { col[n.id] = Math.max(1, n.depth || 1); maxSvc = Math.max(maxSvc, col[n.id]); }
  const E = maxSvc + 1;
  for (const n of nodes) {
    if (n.type === 'agent') col[n.id] = 0;
    else if (n.type === 'internet' || n.type === 'metadata') col[n.id] = E;
    else if (n.type === 'external') col[n.id] = E + 1; // every host outside the cluster in the last column
  }
  const ncol = Math.max(...Object.values(col)) + 1;
  const cols = Array.from({length: ncol}, () => []);
  nodes.forEach(n => cols[col[n.id]].push(n));
  // Chains between two direct destinations stay in the Paths tab: drawing
  // them would loop lines back across the column.
  for (let i = es.length - 1; i >= 0; i--) if (col[es[i].from] >= col[es[i].to]) es.splice(i, 1);
  const rank = {red: 0, amber: 1, green: 2, grey: 3};
  const row = {};
  cols.forEach((list, c) => {
    if (c >= 1) {
      const bc = new Map(list.map((n, i) => {
        if (n.type === 'internet') return [n, -2e9];
        if (n.type === 'metadata') return [n, -1e9];
        const ps = (inbound[n.id] || []).filter(p => row[p] !== undefined);
        const base = ps.length ? ps.reduce((s, p) => s + row[p], 0) / ps.length * 1e6 : 1e12;
        return [n, base + (rank[n.risk] ?? 3) * 1e4 + (n.type === 'group' ? 5e3 : 0) + i];
      }));
      list.sort((x, y) => bc.get(x) - bc.get(y));
    }
    list.forEach((n, i) => { row[n.id] = i; });
  });
  const bw = 232, bh = 44, gap = 118, vg = 14, lane = 7, sg = 16;
  // A long first column wraps into a grid inside a frame, with one line from
  // the agent into the frame. Destinations with onward lines sit in the
  // rightmost sub-column so their lines leave without crossing a box.
  const c1 = cols[1] || [];
  const perCol = 12;
  const nsub = c1.length > perCol + 2 ? Math.min(5, Math.ceil(c1.length / perCol)) : 1;
  const grid = nsub > 1;
  const cell = {};
  if (grid) {
    const onward = new Set(es.filter(e => col[e.from] === 1 && col[e.to] > 1).map(e => e.from));
    const rows = Math.ceil(c1.length / nsub);
    const srcs = c1.filter(n => onward.has(n.id)), rest = c1.filter(n => !onward.has(n.id));
    srcs.forEach((n, i) => { cell[n.id] = {sc: nsub - 1, r: i}; });
    let k = 0;
    const used = new Set(srcs.map((_, i) => `${nsub - 1}:${i}`));
    for (let sc = 0; sc < nsub; sc++) for (let r = 0; r < Math.max(rows, srcs.length); r++) {
      if (k >= rest.length) break;
      if (used.has(`${sc}:${r}`)) continue;
      if (sc < nsub - 1 && r >= rows) continue;
      cell[rest[k++].id] = {sc, r};
    }
    c1.forEach(n => { row[n.id] = cell[n.id].r; });
  }
  const extra = grid ? (nsub - 1) * (bw + sg) + 28 : 0; // extra width of column 1
  const long = es.filter(e => col[e.to] !== col[e.from] + 1);
  const HB = 52; // column headings
  const top = HB + 18 + long.length * lane + (grid ? 26 : 0);
  const tall = Math.max(...cols.map((l, c) => c === 1 && grid ? Math.max(...c1.map(n => cell[n.id].r)) + 1 : l.length));
  const colX = c => 16 + c * (bw + gap) + (c > 1 ? extra : 0) + (c === 1 && grid ? 14 : 0);
  const nodeX = id => colX(col[id]) + (grid && col[id] === 1 ? cell[id].sc * (bw + sg) : 0);
  const X = c => colX(c), Y = id => top + row[id] * (bh + vg);
  const W = 32 + ncol * bw + (ncol - 1) * gap + extra, H = top + tall * (bh + vg) + 14;
  const fit = (s, px) => { const n = Math.max(4, Math.floor((bw - 62) / px)); s = String(s || ''); return s.length > n ? s.slice(0, n - 1) + '…' : s; };
  const byId = new Map(nodes.map(n => [n.id, n]));
  const labelOf = id => byId.get(id)?.label || id;
  const dash = e => e.verdict === 'denied' || e.verdict === 'unknown';
  const risk = r => ['red', 'amber', 'green', 'grey'].includes(r) ? r : 'grey';
  // One trunk per source, offset so trunks of different sources do not overlap.
  const srcSlot = {};
  cols.forEach(list => { let i = 0; for (const n of list) if (es.some(e => e.from === n.id && col[e.to] === col[n.id] + 1)) srcSlot[n.id] = i++; });
  let li = 0;
  const intoGrid = e => grid && col[e.from] === 0 && col[e.to] === 1;
  const ordered = [...es].filter(e => !intoGrid(e)).sort((x, y) => (rank[y.risk] ?? 3) - (rank[x.risk] ?? 3)); // red drawn last, on top
  let frame = '';
  if (grid) {
    const agent = nodes.find(n => n.type === 'agent');
    const into = es.filter(intoGrid);
    const worst = into.reduce((w, e) => (rank[e.risk] ?? 3) < (rank[w] ?? 3) ? e.risk : w, 'grey');
    const fx = colX(1) - 14, fy = top - 30, fw = nsub * bw + (nsub - 1) * sg + 28, fh = H - fy - 4;
    const D = G.direct || {n: into.length, counts: {}}, dc = D.counts;
    const beyond = nodes.filter(n => n.alsoDirect).length;
    const sy = Y(agent.id) + bh / 2;
    // The caption has to fit the frame, which is two boxes wide at the least:
    // the longer wording goes first, then shorter ones.
    const head = `${D.n} Service${D.n === 1 ? '' : 's'}${['red', 'amber', 'green', 'grey'].filter(r => dc[r]).map(r => ` · ${dc[r]} ${r}`).join('')}`;
    const room = Math.floor((fw - 28) / 6.7);
    const caption = [beyond ? `${head} · ${beyond} drawn to the right, behind a gateway` : head, `${head} · ${beyond} more to the right`, head, `${D.n} Services`].find(t => t.length <= room) || `${D.n} Services`;
    frame = `<g class="rg-frame"><rect x="${fx}" y="${fy}" width="${fw}" height="${fh}" rx="10"/><text x="${fx + 14}" y="${fy + 18}" class="rg-fl">${esc(caption)}</text></g>
      <g class="rg-edge rg-${risk(worst)}" data-f="${esc(agent.id)}" data-t="rg-frame"><title>${esc(`${labelOf(agent.id)} reaches ${D.n} Services directly`)}</title><path class="rg-line rg-trunk" d="M${colX(0) + bw},${sy} H${fx - 3}" marker-end="url(#rga-${risk(worst)})"/></g>`;
  }
  const edgeSvg = ordered.map(e => {
    const cs = col[e.from], ct = col[e.to];
    const sx = nodeX(e.from) + bw, sy = Y(e.from) + bh / 2, tx = nodeX(e.to) - 3, ty = Y(e.to) + bh / 2;
    let d;
    if (ct === cs + 1) {
      const bx = sx + 20 + ((srcSlot[e.from] || 0) % 8) * 10;
      const r = Math.min(8, Math.abs(ty - sy) / 2);
      d = Math.abs(ty - sy) < 1 ? `M${sx},${sy} H${tx}` : `M${sx},${sy} H${bx - r} Q${bx},${sy} ${bx},${sy + Math.sign(ty - sy) * r} V${ty - Math.sign(ty - sy) * r} Q${bx},${ty} ${bx + r},${ty} H${tx}`;
    } else {
      const i = li++, ly = HB + 8 + i * lane;
      const g1 = sx + 12 + (i % 6) * 5, g2 = Math.max(4, nodeX(e.to) - 18 - (i % 6) * 5);
      d = `M${sx},${sy} H${g1} V${ly} H${g2} V${ty} H${tx}`;
    }
    const tip = [`${labelOf(e.from)} → ${labelOf(e.to)}`, `${e.verdict}${e.layer ? ' (' + e.layer + ')' : ''}: ${e.why || ''}`, ...(e.reasons || []), e.via ? 'via ' + e.via : '', e.methods ? 'methods: ' + e.methods.join(', ') : '', e.observed ? `called ${e.observed} times${(e.observedBy || []).length ? ' (seen by ' + e.observedBy.join(', ') + ')' : ''}` : '', (e.tools || []).length ? 'tools: ' + e.tools.join(', ') : ''].filter(Boolean).join('\n');
    const useTip = typeof rcUseTip === 'function' && e.from === nodes.find(n => n.type === 'agent')?.id ? rcUseTip(e.to) : '';
    return `<g class="rg-edge rg-${risk(e.usedDenied ? 'red' : e.risk)} ${dash(e) || e.unused ? 'rg-dash' : ''} ${e.unused ? 'rg-unused' : ''}" data-f="${esc(e.from)}" data-t="${esc(e.to)}"><title>${esc(tip + (useTip ? '\n' + useTip : ''))}</title><path class="rg-hit" d="${d}"/><path class="rg-line" d="${d}" marker-end="url(#rga-${risk(e.usedDenied ? 'red' : e.risk)})"/></g>`;
  }).join('');
  const split = n => n.type === 'service' && n.label.includes('/') ? [n.label.slice(n.label.indexOf('/') + 1), n.label.slice(0, n.label.indexOf('/'))] : [n.label, ''];
  const title = n => n.type === 'group' ? `${n.label} (${n.count})` : n.type === 'agent' ? n.label.split('/').pop() : split(n)[0];
  // Past the first column a box says which box leads to it, since the lines
  // from several gateways share the gap between the columns.
  const short = id => { const n = byId.get(id); return n ? split(n)[0].split('/').pop() : id; };
  const viaOf = n => col[n.id] > 1 && n.type === 'service' ? [...new Set(es.filter(e => e.to === n.id && col[e.from] === col[n.id] - 1).map(e => short(e.from)))] : [];
  const sub = n => n.type === 'agent' ? [rcTypes[a.type] || a.type, n.namespace].filter(Boolean).join(' · ') : n.type === 'group' ? `${n.count} Service${n.count === 1 ? '' : 's'} · click to open up` : n.type === 'internet' ? 'outside the cluster' : n.type === 'metadata' ? 'cloud instance metadata'
    : n.type === 'external' ? [...(n.classes || []), n.via].filter(Boolean).join(' · ') || 'external host'
    : viaOf(n).length ? [`via ${viaOf(n).join(', ')}`, n.alsoDirect ? 'also direct' : '', ...(n.classes || [])].filter(Boolean).join(' · ')
    : [...(n.classes || []), split(n)[1]].filter(Boolean).join(' · ') || 'service';
  const dEdge = new Map([...(a.direct || []).map(e => [e.to, rcFound(e, false)]), ...(a.external || []).map(x => ['ext:' + x.host, rcFound(x, true)])]);
  const foundOf = n => (dEdge.get(n.id) || []).map(k => rcFoundLabel[k] || k).join(', ');
  const nodeSvg = nodes.map(n => {
    const click = n.type === 'group' || n.ref || rcSvc(n.id);
    const tip = [n.label, n.namespace && n.type !== 'group' ? 'namespace ' + n.namespace : '', (n.classes || []).join(', '), n.detail, viaOf(n).length ? `Reached through ${viaOf(n).join(', ')}` : '', n.alsoDirect ? `Also reachable directly (${n.alsoDirect})` : '', foundOf(n) ? `Found by ${foundOf(n)} (evidence on the ${n.type === 'external' ? 'External' : 'Destinations'} tab)` : '', click ? (n.type === 'group' ? 'Click to open the namespace up' : 'Click to open') : ''].filter(Boolean).join('\n');
    const fk = rgFoundOf(a, G, n), seen = fk.some(k => !rcFoundLabel[k] && k !== 'policy');
    const dim = !rgMatch(a, G, n, S.reach.gfound), sel = S.reach.gsel === n.id;
    return `<g class="rg-node rg-${risk(n.risk)} rg-t-${esc(n.type)} ${click ? 'rg-click' : ''} ${dim ? 'rg-dim' : ''} ${n.unused ? 'rg-unusednode' : ''} ${sel ? 'rg-sel' : ''}" data-rgid="${esc(n.id)}"${grid && col[n.id] === 1 ? ' data-grid="1"' : ''} transform="translate(${nodeX(n.id)},${Y(n.id)})"><title>${esc(tip)}</title>
      <rect width="${bw}" height="${bh}" rx="7"/>${rgIcon(rgNodeIcon(n), bw - 27, (bh - 16) / 2, 16, 'rg-nic')}${seen ? `<g><title>${esc('seen in traffic by ' + fk.filter(k => !rcFoundLabel[k]).join(', '))}</title>${rgIcon('eye', bw - 48, (bh - 14) / 2, 14, 'rg-eye')}</g>` : ''}<text x="11" y="18" class="rg-l">${esc(fit(title(n), 7.1))}</text><text x="11" y="34" class="rg-s">${esc(fit(sub(n), 6.2))}</text></g>`;
  }).join('');
  const heads = cols.map((list, c) => {
    if (!list.length) return '';
    const svcs = list.filter(n => n.type === 'service' || n.type === 'group');
    const ins = es.filter(e => col[e.to] === c && col[e.from] === c - 1);
    const viaGW = ins.length && ins.every(e => /^agentgateway route|^HTTP proxy/.test(e.via || ''));
    let icon, label, note;
    if (c === 0) [icon, label, note] = ['bot', 'Agent', rcTypes[a.type] || a.type];
    else if (c === 1) [icon, label, note] = G.layer === 'used' ? ['network', 'Used directly', `${G.usedN} of ${G.direct?.n ?? list.length} Services it may reach`] : ['network', 'Reaches directly', `${G.direct?.n ?? list.length} Services, one hop${G.layer === 'both' ? `, ${G.usedN} used` : ''}`];
    else if (list.some(n => n.type === 'external')) [icon, label, note] = ['cloud', 'Hosts outside the cluster', `${list.length} host${list.length === 1 ? '' : 's'} by name`];
    else if (list.some(n => n.type === 'internet' || n.type === 'metadata')) [icon, label, note] = ['globe', 'Ways out', 'the internet and cloud metadata'];
    else [icon, label, note] = viaGW ? ['router', 'Through a gateway', `${svcs.length} Service${svcs.length === 1 ? '' : 's'} behind a gateway`] : ['route', `Hop ${c}`, `${svcs.length} Service${svcs.length === 1 ? '' : 's'}, one hop further`];
    const x = c === 1 && grid ? colX(1) - 14 : colX(c), w = c === 1 && grid ? nsub * bw + (nsub - 1) * sg + 28 : bw;
    return `<g class="rg-head" transform="translate(${x},8)"><rect class="rg-hbg" width="${w}" height="${HB - 14}" rx="8"/>
      <circle class="rg-hdot" cx="20" cy="${(HB - 14) / 2}" r="13"/>${rgIcon(icon, 11, (HB - 14) / 2 - 9, 18, 'rg-hic')}
      <text x="42" y="16" class="rg-hl">${esc(label)}</text><text x="42" y="30" class="rg-hn">${esc(note)}</text></g>`;
  }).join('');
  const marker = r => `<marker id="rga-${r}" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto"><path d="M0,0 L10,5 L0,10 z" class="rg-arrow-${r}"/></marker>`;
  return `<svg class="rg" id="rg-svg" data-w="${W}" data-h="${H}" width="100%" height="100%" role="img" aria-label="Blast radius of ${esc(rcName(a))}"><defs>${['red', 'amber', 'green', 'grey'].map(marker).join('')}</defs><g id="rg-root">${heads}${frame}${edgeSvg}${nodeSvg}</g></svg>`;
}

// Pan and zoom for the graph viewport. The transform is kept per agent and
// view in S.reach.gz so re-renders keep your place.
function rcPanZoom(key, st) {
  const R = st || S.reach, vp = $('#rg-vp'), svg = $('#rg-svg'), root = $('#rg-root');
  if (!vp || !svg || !root) return;
  const W = +svg.dataset.w, H = +svg.dataset.h;
  const pct = $('#rg-pct');
  const apply = () => { const z = R.gz; root.setAttribute('transform', `translate(${z.x},${z.y}) scale(${z.k})`); if (pct) pct.textContent = Math.round(z.k * 100) + '%'; };
  const fitView = () => {
    const vw = vp.clientWidth, vh = vp.clientHeight;
    const k = Math.max(0.2, Math.min(1.25, vw / (W + 20), R.gfull || R.fitH ? vh / (H + 20) : 1.25));
    R.gz = {key, k, x: Math.max(10, (vw - W * k) / 2), y: H * k < vh ? (vh - H * k) / 2 : 10};
    apply();
  };
  if (!R.gz || R.gz.key !== key) fitView(); else apply();
  const zoomAt = (f, cx, cy) => {
    const z = R.gz, k = Math.max(0.15, Math.min(4, z.k * f));
    z.x = cx - (cx - z.x) * (k / z.k); z.y = cy - (cy - z.y) * (k / z.k); z.k = k; apply();
  };
  const centre = () => [vp.clientWidth / 2, vp.clientHeight / 2];
  $('#rg-in').onclick = () => zoomAt(1.25, ...centre());
  $('#rg-out').onclick = () => zoomAt(0.8, ...centre());
  $('#rg-fit').onclick = fitView;
  vp.addEventListener('wheel', e => {
    const r = vp.getBoundingClientRect();
    if (e.ctrlKey || e.metaKey) { e.preventDefault(); zoomAt(Math.exp(-e.deltaY * 0.01), e.clientX - r.left, e.clientY - r.top); }
    else if (R.gfull) { e.preventDefault(); R.gz.x -= e.deltaX; R.gz.y -= e.deltaY; apply(); }
  }, {passive: false});
  let drag = null;
  vp.onpointerdown = e => { if (e.button !== 0) return; drag = {x: e.clientX, y: e.clientY, ox: R.gz.x, oy: R.gz.y, moved: false}; };
  vp.onpointermove = e => {
    if (!drag) return;
    const dx = e.clientX - drag.x, dy = e.clientY - drag.y;
    if (!drag.moved && Math.hypot(dx, dy) < 4) return;
    if (!drag.moved) { drag.moved = true; vp.setPointerCapture(e.pointerId); vp.classList.add('grabbing'); }
    R.gz.x = drag.ox + dx; R.gz.y = drag.oy + dy; apply();
  };
  vp.onpointerup = e => { if (drag?.moved) { vp.dataset.dragged = '1'; setTimeout(() => delete vp.dataset.dragged, 0); } drag = null; vp.classList.remove('grabbing'); };
  vp.onkeydown = e => {
    if (e.key === '+' || e.key === '=') zoomAt(1.25, ...centre());
    else if (e.key === '-') zoomAt(0.8, ...centre());
    else if (e.key === '0') fitView();
    else if (e.key === 'f') $('#rg-full').click();
    else return;
    e.preventDefault();
  };
}

after.reach = () => {
  const R = S.reach;
  $('#rc-term-all')?.addEventListener('click', () => termRun('fbreach'));
  if (!feature('reach')) return;
  document.querySelectorAll('[data-rcagent]').forEach(tr => tr.onclick = () => {
    const n = tr.dataset.rcagent;
    R.scroll = true;
    if (R.sel === n) { render(); return; }
    location.hash = 'traffic?tab=agents&agent=' + encodeURIComponent(n).replace(/%2F/gi, '/');
  });
  document.querySelectorAll('[data-rctype]').forEach(b => b.onclick = () => { R.type = R.type === b.dataset.rctype ? '' : b.dataset.rctype; render(); });
  const q = $('#rc-q');
  if (q) {
    let t; q.oninput = e => { clearTimeout(t); t = setTimeout(() => { const c = e.target.selectionStart, v = e.target.value; R.q = v; render().then(() => refocus('#rc-q', c, v)); }, 250); };
    typeahead(q, {keys: rcKeys, alias: {namespace: 'ns'}, values: (f, p, rest) => {
      const terms = qTerms(rest), get = rcFields[f];
      if (!get) return [];
      const agents = (R.list?.agents || []).filter(a => (!R.type || a.type === R.type) && rcMatch(a, terms));
      return countValues(agents.flatMap(get), p, f === 'type' ? v => rcTypes[v] : f === 'radius' ? v => rcRadiusWhy[v] : null);
    }});
  }
  const a = R.detail;
  if (!R.sel || !a || a.error) return;
  if (R.scroll) { R.scroll = false; $('#rc-detail')?.scrollIntoView({block: 'start', behavior: 'smooth'}); }
  $('#rc-close').onclick = () => { location.hash = 'traffic?tab=agents'; };
  $('#rc-term').onclick = () => termRun('fbreach ' + rcName(a));
  $('#rc-logs').onclick = () => openLogs(a.kind === 'Pod' ? {pod: a.name, namespace: a.namespace} : {workload: `${a.kind}/${a.name}`, namespace: a.namespace});
  const ob = $('#rc-obj'); if (ob) ob.onclick = () => openObject({cluster: a.ref.cluster, group: a.ref.group, resource: a.ref.resource, namespace: a.ref.namespace, name: a.ref.name});
  const openSvc = (ns, name) => openObject({cluster: a.cluster, group: '', resource: 'services', namespace: ns, name});
  const G = rcGraphModel(a);
  const gkey = a.id + '|' + G.view + '|' + (G.layer || '') + '|' + Object.keys(R.gexpand).sort().join(',') + '|' + (R.gfull ? 'full' : '');
  rcPanZoom(gkey);
  const keepY = fn => { const y = scrollY; fn(); render().then(() => scrollTo(0, y)); };
  document.querySelectorAll('[data-rgview]').forEach(b => b.onclick = () => keepY(() => { R.gview = b.dataset.rgview; R.gexpand = {}; }));
  document.querySelectorAll('[data-rglayer]').forEach(b => b.onclick = () => keepY(() => { R.glayer = b.dataset.rglayer; R.gz = null; }));
  if (typeof rcUsageWire === 'function') rcUsageWire(a);
  const col = $('#rg-collapse'); if (col) col.onclick = () => keepY(() => { R.gexpand = {}; });
  document.querySelectorAll('[data-fmenu]').forEach(b => b.onclick = ev => {
    ev.stopPropagation();
    const pop = b.nextElementSibling, open = pop.hidden;
    document.querySelectorAll('.rc-fpop').forEach(p => { p.hidden = true; });
    pop.hidden = !open;
  });
  document.querySelectorAll('.rc-fpop').forEach(p => p.onclick = ev => ev.stopPropagation());
  if (!window.rcFmenuClose) { window.rcFmenuClose = true; document.addEventListener('click', () => document.querySelectorAll('.rc-fpop').forEach(p => { p.hidden = true; })); }
  document.querySelectorAll('[data-rggf]').forEach(b => b.onclick = () => keepY(() => { R.gfound = b.dataset.rggf; }));
  const selx = $('#rg-selx'); if (selx) selx.onclick = () => keepY(() => { R.gsel = ''; });
  document.querySelectorAll('[data-rgopen]').forEach(b => b.onclick = () => { const sv = rcSvc(b.dataset.rgopen); if (sv) openSvc(sv.namespace, sv.name); });
  const full = on => keepY(() => { R.gfull = on; document.body.classList.toggle('rg-noscroll', on); });
  const fb = $('#rg-full'); if (fb) fb.onclick = () => full(!R.gfull);
  if (R.gfull && fb) { document.onkeydown = e => { if (e.key === 'Escape' && R.gfull) { document.onkeydown = null; full(false); } }; $('#rg-vp').focus(); }
  const vp = $('#rg-vp');
  document.querySelectorAll('[data-rgid]').forEach(g => g.onclick = () => {
    if (vp?.dataset.dragged) return;
    const n = G.nodes.find(x => x.id === g.dataset.rgid); if (!n) return;
    if (n.type === 'group') { keepY(() => { R.gexpand[n.namespace] = true; }); return; }
    if (n.type === 'service' || n.type === 'external') { keepY(() => { R.gsel = R.gsel === n.id ? '' : n.id; }); return; }
    if (n.ref) { openObject({cluster: n.ref.cluster, group: n.ref.group, resource: n.ref.resource, namespace: n.ref.namespace, name: n.ref.name}); return; }
    const sv = rcSvc(n.id); if (sv) openSvc(sv.namespace, sv.name);
  });
  // Hovering a box keeps its own lines and neighbours and fades the rest.
  const rg = $('#rg-svg');
  if (rg) {
    // The selected box (R.gsel) keeps its trace lit, the same one hover shows.
    const clear = () => { rg.classList.remove('rg-hl'); rg.querySelectorAll('.on').forEach(x => x.classList.remove('on')); };
    const light = g => {
      const id = g.dataset.rgid, near = new Set([id]);
      rg.querySelectorAll('.rg-edge').forEach(e => {
        const f = e.dataset.f, t = e.dataset.t;
        if (f === id || t === id || (t === 'rg-frame' && g.dataset.grid)) { e.classList.add('on'); near.add(f); near.add(t); }
      });
      rg.querySelectorAll('[data-rgid]').forEach(x => { if (near.has(x.dataset.rgid)) x.classList.add('on'); });
      rg.classList.add('rg-hl');
    };
    const pinned = () => R.gsel && rg.querySelector(`[data-rgid="${CSS.escape(R.gsel)}"]`);
    const restore = () => { clear(); const p = pinned(); if (p) light(p); };
    rg.querySelectorAll('[data-rgid]').forEach(g => {
      g.onmouseenter = () => { clear(); light(g); };
      g.onmouseleave = restore;
    });
    restore();
  }
  document.querySelectorAll('#main [data-rcsvc]').forEach(b => b.onclick = () => openObject(JSON.parse(b.dataset.rcsvc)));
  document.querySelectorAll('[data-rcfopen]').forEach(b => b.onclick = () => { const i = +b.dataset.rcfopen; R.fixOpen[i] = !R.fixOpen[i]; const y = scrollY; render().then(() => scrollTo(0, y)); });
  document.querySelectorAll('[data-rctab]').forEach(b => b.onclick = () => { R.dtab = b.dataset.rctab; const y = scrollY; render().then(() => scrollTo(0, y)); });
  document.querySelectorAll('[data-rccopy]').forEach(b => b.onclick = () => { const f = a.findings[+b.dataset.rccopy]; navigator.clipboard.writeText(f.fix).then(() => toast('Fix copied'), () => toast('Clipboard unavailable')); });
  document.querySelectorAll('[data-rcd]').forEach(b => b.onclick = () => { R.dchip = b.dataset.rcd; const y = scrollY; render().then(() => scrollTo(0, y)); });
  document.querySelectorAll('[data-rce]').forEach(b => b.onclick = () => { R.echip = b.dataset.rce; const y = scrollY; render().then(() => scrollTo(0, y)); });
  document.querySelectorAll('[data-rcdf]').forEach(b => b.onclick = () => { R.dfound = b.dataset.rcdf; const y = scrollY; render().then(() => scrollTo(0, y)); });
  document.querySelectorAll('[data-rcef]').forEach(b => b.onclick = () => { R.efound = b.dataset.rcef; const y = scrollY; render().then(() => scrollTo(0, y)); });
  document.querySelectorAll('[data-rcev]').forEach(b => b.onclick = () => { R.evOpen = R.evOpen || {}; R.evOpen[b.dataset.rcev] = !R.evOpen[b.dataset.rcev]; const y = scrollY; render().then(() => scrollTo(0, y)); });
  document.querySelectorAll('[data-rclogs]').forEach(b => b.onclick = () => openLogs({q: b.dataset.rclogs}));
  document.querySelectorAll('[data-rcref]').forEach(b => b.onclick = () => rcConfigModal(JSON.parse(b.dataset.rcref)));
  const dq = $('#rc-dq');
  if (dq) { let t; dq.oninput = e => { clearTimeout(t); t = setTimeout(() => { const c = e.target.selectionStart, v = e.target.value, y = scrollY; R.dq = v; render().then(() => { scrollTo(0, y); refocus('#rc-dq', c, v); }); }, 250); }; }
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
  const bar = `<form class="query" id="obj-form"><span class="green">⌕</span><input id="obj-q" value="${esc(Q.q)}" placeholder='Search every object. Try kind: ns: label: name: or free text, e.g. kind:pod label:app=checkout' autocomplete="off" spellcheck="false" aria-label="Object search">${Q.q ? '<button type="button" class="ghost" id="obj-clear" title="Clear the search (Esc)">Clear ✕</button>' : ''}<button class="primary">Search ↵</button></form>
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
    <div class="chips">${kinds.map(([k, n]) => { const on = new RegExp(`(^|\\s)kind:${k.toLowerCase()}(\\s|$)`, 'i').test(Q.q); return `<button class="chip ${on ? 'on' : ''}" data-okind="${esc(k)}" title="${on ? 'Remove this kind from the search' : 'Show only this kind'}">${esc(k)} <small>${n}</small>${on ? ' ×' : ''}</button>`; }).join('')}</div>
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
  document.querySelectorAll('[data-okind]').forEach(b => b.onclick = () => {
    const rest = Q.q.replace(/-?kind:\S+/g, '').replace(/\s+/g, ' ').trim();
    go(b.classList.contains('on') ? rest : `kind:${b.dataset.okind.toLowerCase()} ${rest}`.trim());
  });
  const clr = $('#obj-clear'); if (clr) clr.onclick = () => go('');
  // Esc closes the suggestions first; with none open it clears the search.
  $('#obj-q').addEventListener('keydown', e => { if (e.key === 'Escape' && Q.q && !TA.menu?.isConnected) go(''); }, {capture: true});
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
// Resources has two ways in: Browse (mouse, grouped types, drawer) and
// Keyboard (the k9s-style Navigator). The same objects; the last tab used
// is where the menu entry opens.
function resTabs(active) {
  try { localStorage.setItem('flightbay-res-tab', active); } catch (_) { /* private mode */ }
  return `<nav class="pst-tabs res-tabs"><a class="pst-tab ${active === 'resources' ? 'on' : ''}" href="#resources" data-restab="resources">Browse<small class="muted">mouse, every type</small></a><a class="pst-tab ${active === 'navigator' ? 'on' : ''}" href="#navigator" data-restab="navigator">Keyboard<small class="muted">k9s-style: : / Enter Esc</small></a></nav>`;
}

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
  return head('Resources', 'Every captured object. Browse with the mouse, or switch to the keyboard view to move through them the way you would in k9s.', `<button id="res-k9s" title="k9s on this type and namespace, in the terminal">Open in k9s</button><button id="res-term">&gt;_ kubectl get ${esc(cur.resource)}</button>`) + resTabs('resources') + `
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
          ${md.annotations?.['flightbay.dev/source'] ? `<dt>Source</dt><dd class="mono">${esc(md.annotations['flightbay.dev/source'])}${md.annotations['flightbay.dev/source-line'] ? ':' + esc(md.annotations['flightbay.dev/source-line']) : ''} <span class="muted">${esc(md.annotations['flightbay.dev/rendered-by'] || '')}</span></dd>` : ''}
          ${o.object.spec?.nodeName ? `<dt>Node</dt><dd>${esc(o.object.spec.nodeName)}</dd>` : ''}${st.phase ? `<dt>Phase</dt><dd>${statusCell(st.phase)}</dd>` : ''}</dl>
          ${cs ? `<h3 class="mt">Containers</h3><div class="table-scroll"><table><thead><tr><th>Name</th><th>Ready</th><th>State</th><th>Restarts</th><th>Last termination</th></tr></thead><tbody>${cs}</tbody></table></div>` : ''}
          ${o.object['x-flightbay'] ? '<div class="notice info mt">Secret values were never collected. Only metadata' + (o.object['x-flightbay'].keys ? ', key names and byte lengths are' : ' is') + ' shown.</div>' : ''}
          <div class="row wrap mt">${isPod ? `<button class="primary" id="d-logs">Open pod logs →</button>` : ''}${workloadKinds.includes(o.kind) ? `<button class="primary" id="d-wlogs">Open logs of all its pods →</button>` : ''}<button id="d-describe">&gt;_ kubectl describe</button>${(S.ws?.clusters || []).length > 1 ? '<button id="d-compare">Compare across clusters</button>' : ''}${typeof rvObjectButton === 'function' ? rvObjectButton(o) : ''}</div>`;
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
    if (typeof rvWireObject === 'function') rvWireObject();
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
  const anyFilter = L.q || L.from || L.to || ['namespace', 'workload', 'pod', 'container', 'level', 'stream'].some(k => L[k]);
  const hist = res.histogram || [];
  const max = Math.max(1, ...hist.map(b => b.count));
  const span = res.to && res.from ? new Date(res.to) - new Date(res.from) : 0;
  const bars = hist.map((b, i) => `<div data-bucket="${i}" style="height:${b.count ? Math.max(3, b.count / max * 96) : 0}px" title="${fmtDateTime(b.start)} · ${num(b.count)} lines"></div>`).join('');
  const axis = hist.length ? `<div class="hist-axis"><span>${fmtAxis(res.from, span)}</span><span>${fmtAxis(new Date((new Date(res.from).getTime() + new Date(res.to).getTime()) / 2).toISOString(), span)}</span><span>${fmtAxis(res.to, span)}</span></div>` : '';
  const zoomed = !!(L.from || L.to);
  const records = res.records || [];
  const rows = records.map(r => `<div class="log-row ${r.level === 'error' ? 'error' : r.level === 'warn' ? 'warn' : ''}" data-log="${r.id}" tabindex="0">
      <span class="muted">${fmtTime(r.ts)}</span><span class="${r.level === 'error' ? 'red' : r.level === 'warn' ? 'amber' : 'green'}">${esc(r.level.toUpperCase())}</span>
      <span class="pod" title="${esc(r.cluster)} ${esc(r.namespace)}/${esc(r.pod)}">${esc(r.pod)}<br><span class="muted">${esc(r.container)}${r.stream === 'previous' ? ' · previous' : ''}</span></span>
      <span class="msg">${esc(r.msg)}${Object.entries(r.fields || {}).filter(([k]) => /trace|request_id|connection/.test(k)).map(([k, v]) => ` <span class="blue">${esc(k)}=${esc(v)}</span>`).join('')}</span></div>${L.expanded === r.id ? '<div id="log-detail"></div>' : ''}`).join('');
  return head('Log explorer', 'Search every captured log line. Drag across the chart to zoom into a time range; pinch or scroll over it to zoom around the cursor.', `<label class="row"><input type="checkbox" id="follow" ${L.follow ? 'checked' : ''}> Follow incoming data</label><button id="log-term">&gt;_ fbq in terminal</button>${typeof rvLogsAction === 'function' ? rvLogsAction() : ''}`) + `
  <div class="log-layout"><aside class="facets" aria-label="Filters">${facet('level', 'level', 'Level')}${facet('namespace', 'namespace', 'Namespace')}${workloadFacet}${facet('pod', 'pod', 'Pod', podPicker)}${facet('container', 'container', 'Container')}${facet('stream', 'stream', 'Stream')}${!S.cluster ? facet('cluster', 'cluster_', 'Cluster') : ''}
    ${fieldList}
    <h3>Syntax</h3><div class="muted mono" style="font-size:10px;line-height:1.8">level:error<br>ns:payments pod:checkout*<br>trace_id:a4e98f02<br>"connection reset"<br>-healthz · a OR b<br>status:5* · since:15m</div></aside>
  <section><form class="query" id="log-form"><span class="green">⌕</span><input id="log-q" value="${esc(L.q)}" placeholder='Search logs. Try level: ns: pod: container: since: or "free text"' autocomplete="off" spellcheck="false" aria-label="Log query"><button class="primary">Search ↵</button>${anyFilter ? '<button type="button" id="log-clear" title="Clear the query, filters and time range">Clear filters</button>' : ''}</form>
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
    ${res.total > records.length ? `<div class="table-foot"><span>Showing ${records.length} of ${num(res.total)}</span><button id="more">Show more</button></div>` : ''}</div>
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
  if (typeof rvWireLogs === 'function') rvWireLogs();
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
  const lc = $('#log-clear');
  if (lc) lc.onclick = () => {
    for (const k of ['q', 'namespace', 'workload', 'pod', 'container', 'level', 'stream', 'from', 'to', 'podFilter']) L[k] = '';
    L.expanded = null; L.limit = 200;
    if (location.hash.includes('?')) history.replaceState(null, '', '#logs');
    render();
  };
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
    <div class="row wrap"><button id="lc-pod">Inspect pod →</button><button id="lc-stream">All lines from this stream</button>${typeof rvLineButton === 'function' ? rvLineButton(r) : ''}${pivots.map(([k, v]) => `<button class="active" data-pivot="${attr({k, v})}">Follow ${esc(k)}=${esc(v)} across pods →</button>`).join('')}</div>
    ${Object.keys(r.fields || {}).length ? `<pre>${esc(JSON.stringify(r.fields, null, 2))}</pre>` : ''}</div>`;
  $('#lc-pod').onclick = () => openObject({cluster: r.cluster, group: '', resource: 'pods', namespace: r.namespace, name: r.pod});
  $('#lc-stream').onclick = () => openLogs({namespace: r.namespace, pod: r.pod, container: r.container, stream: r.stream});
  if (typeof rvWireLine === 'function') rvWireLine();
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
  return head('Timeline', 'Kubernetes events, container exits, rollouts and error-log spikes on one UTC axis.') + `
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
  const fs = all.filter(f => (!S.findingSev || f.severity === S.findingSev) && (!S.findingsRule || f.rule === S.findingsRule));
  const prov = all.some(f => f.provisional);
  return head('Findings', 'Things to look at, each linked to its evidence. None of them is a root cause on its own.', `<select id="sev"><option value="">All severities</option>${['critical', 'warning', 'info'].map(s => `<option ${S.findingSev === s ? 'selected' : ''}>${s}</option>`).join('')}</select>`) +
    (prov ? '<div class="notice info" style="margin-bottom:14px">These findings are provisional: the bundle has not finished loading or failed verification. Rules that depend on something being absent wait for complete coverage.</div>' : '') +
    `<section class="panel"><div class="panel-head"><h2>${fs.length} findings</h2>${S.findingsRule ? `<button class="chip" id="clear-rule" title="Show every rule">rule: ${esc(S.findingsRule)} ×</button>` : '<small>Deterministic rules</small>'}</div>${fs.map(findingRow).join('') || '<div class="empty"><strong>No findings in this selection</strong></div>'}</section>`;
}
after.findings = () => { $('#sev').onchange = e => { S.findingSev = e.target.value; render(); }; const cr = $('#clear-rule'); if (cr) cr.onclick = () => { S.findingsRule = ''; render(); }; wireFindings(); };

function openFinding(f) {
  modal(f.title, `<div class="row wrap"><span class="badge ${f.severity === 'critical' ? 'red' : f.severity === 'warning' ? 'amber' : 'blue'}">${esc(f.severity)}</span><span class="tag">${esc(f.rule)}</span><span class="tag">${esc(f.cluster)}</span>${f.provisional ? '<span class="badge blue">provisional</span>' : ''}</div>
    <p class="mt">${esc(f.detail || '')}</p>${(f.evidence || []).length ? `<h3 class="mt">Evidence</h3><pre class="code mt">${esc(f.evidence.join('\n'))}</pre>` : ''}
    ${f.nextStep ? `<h3 class="mt">Next step</h3><p class="mt muted">${esc(f.nextStep)}</p>` : ''}
    ${f.source || f.docs ? `<p class="mt row">${guideLinks(f.source, f.docs)}</p>` : ''}
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

const cmpIntro = "See what changed between two captures of the same cluster: objects added, removed or changed, image upgrades, and findings that appeared or went away.";

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
// pxName splits a pod name into what it runs and its generated suffix
// (ReplicaSet hash and pod id, or the DaemonSet pod id).
const pxName = pod => { const m = /^(.+?)-([a-z0-9]{6,10}-[a-z0-9]{5}|[a-z0-9]{5})$/.exec(pod || ''); return m ? [m[1], m[2]] : [pod, '']; };
// pxTitle is how a proxy is told apart: ztunnel runs one per node, so the
// node names it; everything else by what it runs.
const pxTitle = c => c.component === 'ztunnel' && c.node ? c.node : pxName(c.pod)[0];

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
    ${rows.slice(0, 500).map(r => `<tr>${r.map((c, i) => `<td class="${i === 0 ? 'mono' : ''} ${String(c).length > 48 ? 'px-long' : 'px-short'}">${/^STALE/.test(c) ? `<span class="red">${esc(c)}</span>` : esc(c)}</td>`).join('')}</tr>`).join('')}</tbody></table>` : '<div class="empty">Nothing captured here' + (q ? ' matches the filter' : '') + '.</div>'}</div>
    ${rows.length > 500 ? `<div class="table-foot"><span>Showing 500 of ${rows.length}; narrow with the filter</span></div>` : ''}</section>`;
}

async function proxies() {
  const d = await api('api/proxies', {cluster: S.cluster});
  S.px = S.px || {sel: '', tab: 'summary', file: '', q: ''};
  const comps = d.components;
  if (!comps.length) {
    return head('Proxy configs', 'Envoy, ztunnel and agentgateway config dumps, istiod debug output and metrics.') + `<div class="panel empty"><strong>${d.selected ? 'No matching components captured' : 'Proxy dumps, control-plane debug and metrics were not selected'}</strong>${
      S.ws?.live ? (S.ws.canRefreshDiagnostics ? 'Live mode reads diagnostics once when the mirror is ready; they may still be arriving. Component pods appear here as soon as they are read.' : 'Live mode reads proxy dumps only with flightbay-desk live --diagnostics.') : d.loading ? 'Diagnostics are written last in a bundle, so they may still be arriving.' : d.selected ? 'The selected namespaces held no ztunnel, Envoy, agentgateway, istiod or controller pods.' : 'Port-forward diagnostics were not allowed for this bundle (flightbay collect --diagnostics). Their absence says nothing about the proxies.'}</div>`;
  }
  if (!comps.find(c => pxKey(c) === S.px.sel)) { S.px.sel = pxKey(comps[0]); S.px.file = ''; }
  const cur = comps.find(c => pxKey(c) === S.px.sel);
  const groups = {};
  comps.forEach(c => (groups[c.component || 'unknown'] ||= []).push(c));
  const multi = new Set(comps.map(c => c.cluster)).size > 1;
  const nav = Object.entries(groups).map(([g, cs]) => {
    const vers = [...new Set(cs.map(c => c.version).filter(Boolean))];
    return `<div class="px-group"><span>${esc(compLabel[g] || g)}</span><small title="${esc(vers.join(', '))}">${esc(vers.length === 1 ? vers[0] : vers.length ? vers.length + ' versions' : '')}${vers.length ? ' · ' : ''}${cs.length}</small></div>` + cs.map(c => {
      const bad = c.files.some(f => f.state !== 'complete');
      const sub = g === 'ztunnel' ? c.pod : [c.namespace, c.node].filter(Boolean).join(' · ');
      return `<button class="px-item ${pxKey(c) === S.px.sel ? 'active' : ''}" data-px="${attr(pxKey(c))}" title="${esc(c.namespace)}/${esc(c.pod)}${c.node ? ' on ' + esc(c.node) : ''}">
        <span class="px-iname">${esc(pxTitle(c))}${c.expired ? ' <small class="red">expired</small>' : c.expiring ? ' <small class="amber">cert</small>' : ''}</span><span class="st state-${bad ? 'truncated' : 'complete'}" title="${bad ? 'some files incomplete' : 'captured'}">●</span>
        <small class="px-isub">${multi ? esc(c.cluster) + ' · ' : ''}${esc(sub)}</small></button>`;
    }).join('');
  }).join('');
  let body = '';
  const tabs = ['summary', 'files'];
  if (S.px.tab === 'summary') {
    const p = await api('api/proxy', {cluster: cur.cluster, kind: cur.kind, namespace: cur.namespace, pod: cur.pod}).catch(e => ({error: e.message}));
    if (p.error) body = `<div class="notice ${cur.files.length ? 'bad' : 'info'}">${cur.files.length ? esc(p.error) : 'Nothing was captured for this pod. See the Files tab for the reason.'}</div>`;
    else {
      const sm = p.summary; const q = S.px.q.toLowerCase();
      body = `<div class="stats">${Object.entries(sm.counts || {}).map(([k, v]) => `<div class="stat"><div class="eyebrow">${esc(k)}</div><div class="value ${k === 'stale' && v ? 'red' : ''}">${num(v)}</div></div>`).join('')}</div>
        ${(sm.notes || []).map(n => `<div class="notice info mt">${esc(n)}</div>`).join('')}
        ${sm.certs?.length ? `<section class="panel mt"><div class="panel-head"><h2>Certificates</h2><small>At snapshot time. Workload certificates are short-lived.</small></div><div class="table-scroll" style="max-height:34vh"><table><thead><tr><th>Identity</th><th>Type</th><th>State</th><th>Expires</th><th>Left</th></tr></thead><tbody>
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
  const meta = [['Node', cur.node], ['Pod', `${cur.namespace}/${cur.pod}`], ['Version', cur.version], ['Cluster', multi ? cur.cluster : ''], ['Image', cur.image]].filter(([, v]) => v);
  return head('Proxy configs', 'Envoy, ztunnel and agentgateway config dumps, istiod debug output and metrics, as captured.', `${S.ws?.canRefreshDiagnostics ? '<button id="px-refresh" title="Read this pod\'s admin endpoints again now, over port-forward">Refresh diagnostics</button>' : ''}<button id="px-term">&gt;_ fbpc ${esc(cur.pod)}</button>`) + `
  <div class="resource-layout px-layout"><nav class="types px-nav" aria-label="Components">${nav}</nav>
  <div><div class="px-head"><div class="row"><span class="badge">${esc(compLabel[cur.component] || cur.component)}</span><h2 class="mono">${esc(pxTitle(cur))}</h2></div>
    <dl class="px-meta">${meta.map(([k, v]) => `<div><dt>${k}</dt><dd class="mono">${esc(v)}</dd></div>`).join('')}</dl></div>
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
  const pr = $('#px-refresh'); if (pr) pr.onclick = () => liveRefreshDiagnostics(S.px.sel, pr);
};

// ---------- coverage ----------
async function coverage() {
  const c = await api('api/coverage');
  const st = c.state;
  const rep = c.redaction || {};
  const locs = Object.entries(rep.locations || {}).sort();
  const allCov = c.coverage.filter(x => !S.cluster || x.cluster === S.cluster);
  const cov = allCov.filter(x => x.type !== 'resource' || x.items > 0 || x.state !== 'complete');
  const emptyTypes = allCov.length - cov.length;
  const target = x => x.type === 'logs' ? `logs ${x.namespace}/${x.pod}/${x.container} (${x.stream})` : x.type === 'events' ? 'events' : x.type === 'diagnostic' ? `${x.container} ${x.namespace}/${x.pod} ${x.resource}` : (x.group ? `${x.resource}.${x.group}` : x.resource);
  return head('Coverage', 'What the customer allowed, what was collected, and what the importer verified.') + `
  <div class="grid2"><div>
    <section class="panel"><div class="panel-head"><h2>Collections</h2><small>${cov.length} records · ${cov.filter(x => x.state !== 'complete').length} not complete${emptyTypes ? ` · ${emptyTypes} empty resource types hidden` : ''}</small></div>
    <p class="muted" style="font-size:12px;padding:0 16px 10px">Deployment and ReplicaSet are separate Kubernetes resource types. Deployments create ReplicaSets to manage rollout revisions.</p>
    <div class="table-scroll" style="max-height:55vh"><table><thead><tr><th>Cluster</th><th>Collection</th><th>Items</th><th>State</th><th>Reason</th></tr></thead><tbody>
    ${cov.map(x => `<tr><td><span class="tag">${esc(x.cluster)}</span></td><td class="mono">${esc(target(x))}</td><td>${num(x.items)}</td><td>${stateBadge(x.state === 'complete' ? 'complete' : x.state, x.reason)}</td><td class="muted" style="white-space:normal">${esc(x.reason || '')}</td></tr>`).join('')}
    </tbody></table></div></section>
    <section class="panel mt"><div class="panel-head"><h2>Collection log</h2><small>${(c.collectionLog || []).length} entries</small></div>
    <div class="table-scroll" style="max-height:40vh"><table><thead><tr><th>Cluster</th><th>Operation</th><th>Target</th><th>Result</th><th>Error</th></tr></thead><tbody>
    ${(c.collectionLog || []).map(e => `<tr><td>${esc(e.cluster)}</td><td>${esc(e.operation)}</td><td class="mono">${esc(e.target || '')} ${esc(e.namespace || '')}</td><td class="${/fail|partial/.test(e.result) ? 'red' : ''}">${esc(e.result)}</td><td class="muted" style="white-space:normal">${esc(e.error || '')}</td></tr>`).join('')}
    </tbody></table></div></section>
  </div><div>
    ${typeof rvCoveragePanel === 'function' ? rvCoveragePanel() + '<div class="mt"></div>' : ''}
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
  const views = ['navigator', 'search', 'overview', 'scorecard', ...(feature('traffic') || feature('reach') ? ['traffic'] : []), 'resources', 'logs', 'timeline', 'findings', 'compare', 'proxies', 'coverage'].filter(v => !S.ws?.agent || AGENT_VIEWS.includes(v));
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
    theme: {background: '#0a0910', foreground: '#d6d3e2', cursor: '#a78bfa', selectionBackground: '#3a2f7a'}});
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
  // The Resources menu entry opens the tab (Browse or Keyboard) used last.
  const resLink = document.querySelector('.nav a[data-view="resources"]');
  if (resLink) resLink.onclick = e => { let t = 'resources'; try { t = localStorage.getItem('flightbay-res-tab') || t; } catch (_) { /* private mode */ } if (t === 'navigator') { e.preventDefault(); nav('navigator'); } };
  $('#cluster').onchange = e => setCluster(e.target.value);
  $('#term-toggle').onclick = () => toggleTerminal();
  $('#term-k9s').onclick = () => openK9s('pods', '');
  $('#term-expand').onclick = () => { $('#terminal').classList.toggle('expanded'); toggleTerminal(true); };
  document.addEventListener('keydown', e => {
    const typing = /INPUT|TEXTAREA|SELECT/.test(document.activeElement?.tagName) || document.activeElement?.closest?.('.xterm');
    if ((e.metaKey || e.ctrlKey) && e.key === 'k') { e.preventDefault(); openSearch(); }
    else if (e.ctrlKey && e.key === '`') { e.preventDefault(); toggleTerminal(); }
    else if (e.key === 'Escape') { closeModal(); closeDrawer(); }
    else if (!typing && e.key === '/' && S.view !== 'navigator') { e.preventDefault(); ($('#log-q') || $('#res-q') || $('#obj-q') || $('#rc-q'))?.focus(); }
  });
  addEventListener('hashchange', () => { parseHash(); closeDrawer(); render(); });
  parseHash();
  loadWorkspace().then(render).catch(e => { $('#main').innerHTML = `<div class="notice bad">${esc(e.message)}</div>`; });
  const es = new EventSource('api/state/stream');
  es.addEventListener('state', e => onState(JSON.parse(e.data)));
}
boot();

// ---------- live mode ----------
// flightbay-desk live mirrors a running cluster. The server says so in
// /api/workspace (source "live", S.ws.live) and in every state event
// (state.live). This block swaps the offline banner for a live one, adds Save
// as bundle, and refreshes views as the mirror changes. It wraps the shared
// functions rather than editing them, so bundle workspaces behave as before.
const LIVE = {lastRender: 0, pending: false, timer: null, tick: null, saving: false};
const isLive = () => !!(S.ws?.live?.live || S.state?.live?.live);

function liveAgo(t) {
  if (!t || String(t).startsWith('0001')) return 'none yet';
  const s = Math.max(0, Math.round((Date.now() - new Date(t).getTime()) / 1000));
  return s < 60 ? `${s}s ago` : s < 3600 ? `${Math.floor(s / 60)}m ago` : `${Math.floor(s / 3600)}h ago`;
}

function renderLiveBanner() {
  const lv = S.state?.live || S.ws?.live; if (!lv) return;
  const badge = document.querySelector('#snapshot-bar .badge');
  if (badge) {
    const up = lv.connected !== false;
    badge.className = `badge ${up ? 'green' : 'amber'}`;
    badge.title = up ? `Connected to ${lv.server || lv.context}. Read-only: get, list, watch and pods/log only.` : `Reconnecting to ${lv.server || lv.context} (${lv.reconnects} reconnects so far). The mirror shows the last state received.`;
    badge.innerHTML = `<span class="dot"></span> Live · ${esc(lv.context)} · ${num(lv.objectsWatched)} objects · ${num(lv.logStreams)}${lv.logStreamsTotal > lv.logStreams ? ' of ' + num(lv.logStreamsTotal) : ''} log streams · last change ${esc(liveAgo(lv.lastEvent))}${up ? '' : ' · reconnecting'}`;
  }
  const t = $('#snapshot-time');
  if (t) t.innerHTML = `<span class="muted">Read-only mirror${lv.logLinesDropped ? ` · ${num(lv.logLinesDropped)} old log lines dropped` : ''}</span> <button id="live-save" class="ghost" title="Write the mirror as it is now to a normal, verified bundle">${LIVE.saving ? 'Saving…' : 'Save as bundle'}</button> <button id="live-review" class="primary" title="Choose what to send, sign it and export it">Export and sign…</button>`;
  const b = $('#live-save'); if (b) b.onclick = liveSave;
  const rv = $('#live-review'); if (rv) rv.onclick = () => nav('review');
}

async function liveSave() {
  if (LIVE.saving) return;
  LIVE.saving = true; renderLiveBanner();
  try {
    const r = await fetch('api/live/save', {method: 'POST', credentials: 'same-origin'});
    const body = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(body.error || r.statusText);
    modal('Saved as bundle', `<p>The mirror as it was a moment ago is now a normal, verified bundle:</p><pre class="code">${esc(body.path)}</pre>
      <p class="muted">Check it with <code>flightbay inspect ${esc(body.path)}</code>, open it later with <code>flightbay-desk open ${esc(body.path)}</code>, or send it like any capture.</p>`);
  } catch (e) { toast('Save failed: ' + e.message); }
  LIVE.saving = false; renderLiveBanner();
}

// Throttled refresh: at most once every 3 s, and never while the user types
// or has a drawer or modal open. A blocked refresh waits for its moment.
function liveBusy() {
  const a = document.activeElement, sel = getSelection();
  if (sel && !sel.isCollapsed && $('#main')?.contains(sel.anchorNode)) return true; // you are selecting or copying
  return S.drawerOpen || !!document.querySelector('.modal-backdrop') || !!TA.active ||
    (a && (/INPUT|TEXTAREA|SELECT/.test(a.tagName) || a.isContentEditable || a.closest?.('.xterm')));
}
function liveSchedule() {
  LIVE.pending = true;
  if (LIVE.timer) return;
  const wait = Math.max(0, 3000 - (Date.now() - LIVE.lastRender));
  LIVE.timer = setTimeout(liveRefresh, wait);
}
async function liveRefresh() {
  LIVE.timer = null;
  if (!LIVE.pending) return;
  if (liveBusy() || document.hidden) { LIVE.timer = setTimeout(liveRefresh, 1000); return; }
  LIVE.pending = false; LIVE.lastRender = Date.now();
  if (S.view === 'logs') { checkNewLines(); return; } // the log explorer has Follow incoming data
  if (S.view === 'compare') return;
  // Review and send works on a frozen copy of the mirror: redrawing it would
  // only lose what you are in the middle of choosing.
  if (S.view === 'review') return;
  // Patch the page in place rather than rebuilding it, so scroll positions,
  // text selections, open panels and zoom survive new data.
  const main = $('#main'), top = main?.scrollTop || 0, wy = scrollY;
  S.morph = true;
  try { await loadWorkspace(); await render(); } catch (_) { /* the next change retries */ } finally { S.morph = false; }
  if (main && main.scrollTop !== top) main.scrollTop = top;
  if (scrollY !== wy) scrollTo(0, wy);
}

{
  const baseImportStatus = renderImportStatus;
  renderImportStatus = function () {
    if (!isLive()) return baseImportStatus();
    const st = S.state || S.ws?.state || {};
    const el = $('#import-status');
    const loading = st.phase === 'loading' || st.phase === 'waiting';
    if (el) el.innerHTML = loading ? `<span class="progress"><i></i></span><span class="blue">Listing the cluster</span><span>${num(st.objects)} objects so far</span>`
      : `<span>${num(st.objects)} objects · ${num(st.logLines)} log lines · ${num(st.events)} events</span>`;
    const dot = $('#phase-dot'); if (dot) dot.className = `dot ${loading ? 'blue' : st.live?.connected === false ? 'amber' : 'green'}`;
    renderLiveBanner();
  };

  const baseOnState = onState;
  onState = function (st) {
    const prev = S.state;
    if (!st.live) return baseOnState(st);
    if (S.ws) S.ws.live = st.live;
    if (!prev || prev.phase !== st.phase || st.phase !== 'live') return baseOnState(st);
    S.state = st;
    renderImportStatus();
    if (st.revision !== prev.revision) { TA.cache.clear(); TA.keys.clear(); liveSchedule(); }
  };

  const baseLoadWorkspace = loadWorkspace;
  loadWorkspace = async function () {
    await baseLoadWorkspace();
    if (!isLive()) return;
    const w = S.ws;
    $('#bundle-case').textContent = 'Live';
    $('#bundle-sub').textContent = `${w.live.context} · read-only`;
    $('#collector').textContent = w.collector || '';
    document.querySelectorAll('#cluster option').forEach(o => { if (o.value) o.textContent = o.value + '@live'; });
    document.title = `Flightbay · Live · ${w.live.context}`;
    const meta = document.querySelector('.terminal-meta');
    if (meta) meta.textContent = 'bash · KUBECONFIG points at the read-only live mirror · kubectl, k9s (watches update), fbsearch, fbq, fbpc, fbscore';
    renderImportStatus();
  };

  // Views written for bundles say "snapshot", "captured" and "verified". In
  // live mode relabel the few that would mislead; the views are unchanged.
  const baseRender = render;
  render = async function () {
    await baseRender();
    if (!isLive()) return;
    const c = $('#crumb'); if (c) c.textContent = c.textContent.replace('/ Snapshot /', `/ Live ${S.ws.live.context} /`);
  };
  const baseOverview = overview;
  overview = async function (seq) {
    const html = await baseOverview(seq);
    if (!isLive() || typeof html !== 'string') return html;
    return html.replace('<h1>Snapshot</h1>', '<h1>Live</h1>')
      .replace(/ · captured [^<]*/, ' · live, read-only')
      .replace('still loading', 'following live')
      .replace('Provisional until the import is verified', 'Recomputed as the cluster changes')
      .replace('Rules run against the captured snapshot', 'Rules run against the live mirror')
      .replace('<span class="badge">snapshot contexts</span>', '<span class="badge green">live</span>')
      .replace('<h2>Bundle provenance</h2><span class="badge amber">not yet verified</span>', '<h2>Live mirror</h2><span class="badge green">read-only</span>');
  };

  // "last change 4s ago" keeps counting between events.
  LIVE.tick = setInterval(() => { if (isLive()) renderLiveBanner(); }, 1000);
  // boot() ran before this block; if the workspace is already loaded, redo the header.
  if (S.ws) loadWorkspace().catch(() => {});
}

// Proxy configs: Refresh diagnostics for the selected pod (live with --diagnostics).
async function liveRefreshDiagnostics(podKey, btn) {
  const [, , ns, pod] = podKey.split('/');
  if (btn) { btn.disabled = true; btn.textContent = 'Refreshing…'; }
  try {
    const r = await fetch('api/live/refresh-diagnostics?' + new URLSearchParams({pod: `${ns}/${pod}`}), {method: 'POST', credentials: 'same-origin'});
    const body = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(body.error || r.statusText);
    toast(`Refreshed ${ns}/${pod}: ${body.files} files`);
    await render();
  } catch (e) { toast('Refresh failed: ' + e.message); if (btn) { btn.disabled = false; btn.textContent = 'Refresh diagnostics'; } }
}
