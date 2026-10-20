'use strict';
/* Ambient v2: one namespace and Service scope shared by Overview, Policies & routes, Map and Request path
   (docs/ux/ambient/REVIEW-2026-10-20.md R1). The selection lives in the URL (ns=a,b&svcs=ns/name,...) and
   in localStorage, so it survives switching tabs and reloading. The popover is mounted on <body>, so the
   page can re-render underneath it while you tick boxes. */

const AM2_SCOPE_KEY = 'flightbay.am2.scope';
const AM2_SYSTEM_NS = ['kube-system', 'kube-public', 'kube-node-lease', 'local-path-storage', 'istio-system', 'istio-eastwest'];

S.am2.scope = S.am2.scope || {ns: [], svcs: [], inAmbientOnly: true, hideSystem: true, ready: false, open: false, q: '', withServices: false};

const am2ScopeSplit = v => String(v || '').split(',').map(s => s.trim()).filter(s => s && s !== '*');
const am2IsSystemNs = n => AM2_SYSTEM_NS.includes(n?.name || n) || /^istio-/.test(n?.name || n) || !!n?.system;

// Pure: the default selection when nothing is in the URL or storage. Namespaces in ambient with
// configuration; otherwise the in-ambient namespace with most Services; never "everything" when big.
function am2ScopeDefault(namespaces) {
  const list = (namespaces || []).filter(n => !am2IsSystemNs(n));
  const configured = list.filter(n => n.inAmbient && n.configured > 0).map(n => n.name);
  if (configured.length) return configured;
  const amb = list.filter(n => n.inAmbient).sort((a, b) => (b.services || 0) - (a.services || 0));
  if (amb.length) return [amb[0].name];
  if (list.length <= 10) return [];
  return [list.slice().sort((a, b) => (b.services || 0) - (a.services || 0))[0].name];
}

// Pure: route + stored + namespaces -> {ns, svcs}. Route wins, then storage, then the default.
function am2ScopeResolve(route, stored, namespaces) {
  if (route && (route.ns !== undefined || route.svcs !== undefined)) return {ns: am2ScopeSplit(route.ns), svcs: am2ScopeSplit(route.svcs)};
  if (stored && Array.isArray(stored.ns)) return {ns: stored.ns.slice(), svcs: (stored.svcs || []).slice()};
  return {ns: am2ScopeDefault(namespaces), svcs: []};
}

function am2ScopeInit(route, data) {
  const sc = S.am2.scope;
  const fromRoute = route.ns !== undefined || route.svcs !== undefined;
  if (sc.ready && !fromRoute) return sc;
  let stored = null;
  try { stored = JSON.parse(localStorage.getItem(AM2_SCOPE_KEY) || 'null'); } catch (_) { /* ignore */ }
  const r = am2ScopeResolve(route, sc.ready ? {ns: sc.ns, svcs: sc.svcs} : stored, data?.namespaces);
  sc.ns = r.ns; sc.svcs = r.svcs;
  if (stored && typeof stored.inAmbientOnly === 'boolean' && !sc.ready) { sc.inAmbientOnly = stored.inAmbientOnly; sc.hideSystem = stored.hideSystem !== false; }
  sc.ready = true;
  return sc;
}

function am2ScopeSave() {
  const sc = S.am2.scope;
  try { localStorage.setItem(AM2_SCOPE_KEY, JSON.stringify({ns: sc.ns, svcs: sc.svcs, inAmbientOnly: sc.inAmbientOnly, hideSystem: sc.hideSystem})); } catch (_) { /* private mode */ }
}

// The URL parameters for the current scope; an empty selection is written as ns= so "All" survives reload.
function am2ScopeParams() {
  const sc = S.am2.scope;
  return {ns: sc.ns.length ? sc.ns.join(',') : '*', svcs: sc.svcs.join(',')};
}
const am2ScopeAll = () => !S.am2.scope.ns.length;
const am2InScopeNs = ns => am2ScopeAll() || S.am2.scope.ns.includes(ns);
const am2InScopeSvc = (ns, name) => am2InScopeNs(ns) && (!S.am2.scope.svcs.length || S.am2.scope.svcs.includes(ns + '/' + name) || !S.am2.scope.svcs.some(s => s.startsWith(ns + '/')));

// Pure: the trigger label.
function am2ScopeLabel(ns, svcs) {
  if (!ns.length) return 'All namespaces';
  const head = ns.slice(0, 2).join(', ') + (ns.length > 2 ? ` +${ns.length - 2}` : '');
  return `Namespaces: ${head}${svcs.length ? ` · ${svcs.length} ${svcs.length === 1 ? 'Service' : 'Services'}` : ''}`;
}

function am2ScopeButton(opts = {}) {
  const sc = S.am2.scope;
  sc.withServices = !!opts.services;
  return `<button type="button" class="am2-scopebtn" id="am2-scopebtn" aria-haspopup="dialog" aria-expanded="${sc.open}">${esc(am2ScopeLabel(sc.ns, sc.svcs))} <span aria-hidden="true">▾</span></button>`;
}

function am2ScopeNamespaces() {
  const sc = S.am2.scope, data = S.am2.data || {};
  return (data.namespaces || []).filter(n => (!sc.hideSystem || !am2IsSystemNs(n)) && (!sc.inAmbientOnly || n.inAmbient || sc.ns.includes(n.name)));
}

function am2ScopeServices() {
  const sc = S.am2.scope, data = S.am2.data || {};
  if (!sc.ns.length) return [];
  return (data.services || []).filter(r => sc.ns.includes(r.ref.namespace)).map(r => r.ref.namespace + '/' + r.ref.name).sort();
}

function am2ScopePopoverHTML() {
  const sc = S.am2.scope, q = sc.q.trim().toLowerCase();
  const list = am2ScopeNamespaces().filter(n => !q || n.name.toLowerCase().includes(q));
  const box = (attr, val, on, label, sub, dot) => `<label class="am2-scope-opt"><input type="checkbox" ${attr}="${esc(val)}" ${on ? 'checked' : ''}><span class="am2-scope-name">${dot ? `<i class="am2-dot ${dot}"></i>` : ''}${esc(label)}</span><span class="am2-sub">${esc(sub)}</span></label>`;
  const nsRows = list.map(n => box('data-scope-ns', n.name, sc.ns.includes(n.name), n.name, `${n.services || 0} ${n.services === 1 ? 'Service' : 'Services'}`, n.inAmbient ? 'ok' : 'mut')).join('') || '<div class="am2-sub am2-pad">No namespaces match.</div>';
  const svcs = sc.withServices ? am2ScopeServices().filter(s => !q || s.toLowerCase().includes(q)) : [];
  const svcRows = svcs.map(s => box('data-scope-svc', s, sc.svcs.includes(s), s, '')).join('');
  return `<div class="am2-scope-pop am2" role="dialog" aria-label="Choose namespaces" id="am2-scopepop">
<input class="am2-input am2-full" id="am2-scopeq" placeholder="Search namespaces${sc.withServices ? ' and Services' : ''}…" value="${esc(sc.q)}" aria-label="Search">
<div class="am2-scope-flags"><label><input type="checkbox" data-scope-flag="inAmbientOnly" ${sc.inAmbientOnly ? 'checked' : ''}> In ambient only</label><label><input type="checkbox" data-scope-flag="hideSystem" ${sc.hideSystem ? 'checked' : ''}> Hide system namespaces</label></div>
<div class="am2-scope-acts"><a href="#" data-scope-act="all">Select all</a><a href="#" data-scope-act="clear">Clear</a><span class="am2-spacer"></span><span class="am2-sub">${sc.ns.length ? `${sc.ns.length} selected` : 'Showing all namespaces'}</span></div>
<div class="am2-scope-list" id="am2-scopelist"><div class="am2-scope-h">Namespaces</div>${nsRows}${sc.withServices && sc.ns.length ? `<div class="am2-scope-h">Services in the chosen namespaces <span class="am2-sub">(none ticked means all)</span></div>${svcRows || '<div class="am2-sub am2-pad">No Services match.</div>'}` : ''}</div>
<div class="am2-scope-foot"><button type="button" class="am2-btn p sm0" data-scope-act="done">Done</button></div></div>`;
}

function am2ScopePlace() {
  const pop = document.getElementById('am2-scopepop'), btn = document.getElementById('am2-scopebtn');
  if (!pop || !btn) return;
  const r = btn.getBoundingClientRect();
  pop.style.top = (window.scrollY + r.bottom + 6) + 'px';
  pop.style.left = Math.max(8, Math.min(window.scrollX + r.left, window.scrollX + document.documentElement.clientWidth - pop.offsetWidth - 8)) + 'px';
}

function am2ScopeRenderPop(keepFocus) {
  const sc = S.am2.scope;
  let host = document.getElementById('am2-scopehost');
  if (!sc.open) { host?.remove(); return; }
  if (!host) { host = document.createElement('div'); host.id = 'am2-scopehost'; document.body.appendChild(host); }
  const scroll = document.getElementById('am2-scopelist')?.scrollTop || 0;
  host.innerHTML = am2ScopePopoverHTML();
  const list = document.getElementById('am2-scopelist'); if (list) list.scrollTop = scroll;
  am2ScopePlace();
  if (keepFocus) { const q = document.getElementById('am2-scopeq'); if (q) { q.focus(); q.setSelectionRange(q.value.length, q.value.length); } }
}

// Apply the selection: update the URL (replace, so ticking boxes does not fill history) and re-render the page.
function am2ScopeApply() {
  am2ScopeSave();
  const route = am2Route(), p = am2ScopeParams();
  const next = am2Hash({...route, ns: p.ns, svcs: p.svcs, view: undefined});
  if (location.hash !== next) history.replaceState(null, '', next);
  S.am2.scopeVersion = (S.am2.scopeVersion || 0) + 1;
  render();
}

let am2ScopeWired = false;
function am2ScopeWire() {
  const btn = document.getElementById('am2-scopebtn');
  if (btn) btn.onclick = e => { e.stopPropagation(); S.am2.scope.open = !S.am2.scope.open; S.am2.scope.q = ''; am2ScopeRenderPop(true); btn.setAttribute('aria-expanded', S.am2.scope.open); };
  if (S.am2.scope.open) { if (btn) am2ScopeRenderPop(false); else { S.am2.scope.open = false; am2ScopeRenderPop(); } }
  if (am2ScopeWired) return;
  am2ScopeWired = true;
  const close = () => { if (!S.am2.scope.open) return; S.am2.scope.open = false; am2ScopeRenderPop(); document.getElementById('am2-scopebtn')?.setAttribute('aria-expanded', 'false'); document.getElementById('am2-scopebtn')?.focus(); };
  document.addEventListener('click', e => {
    if (!S.am2.scope.open) return;
    const pop = e.target.closest('#am2-scopepop');
    if (!pop) { if (!e.target.closest('#am2-scopebtn')) close(); return; }
    const act = e.target.closest('[data-scope-act]');
    if (!act) return;
    e.preventDefault();
    const sc = S.am2.scope, a = act.dataset.scopeAct;
    if (a === 'done') { close(); return; }
    if (a === 'all') sc.ns = am2ScopeNamespaces().map(n => n.name);
    if (a === 'clear') { sc.ns = []; sc.svcs = []; }
    am2ScopeRenderPop(false); am2ScopeApply();
  });
  document.addEventListener('change', e => {
    if (!e.target.closest('#am2-scopepop')) return;
    const sc = S.am2.scope, t = e.target;
    if (t.dataset.scopeNs) { const n = t.dataset.scopeNs; sc.ns = t.checked ? [...new Set([...sc.ns, n])] : sc.ns.filter(x => x !== n); if (!t.checked) sc.svcs = sc.svcs.filter(s => !s.startsWith(n + '/')); }
    else if (t.dataset.scopeSvc) { const n = t.dataset.scopeSvc; sc.svcs = t.checked ? [...new Set([...sc.svcs, n])] : sc.svcs.filter(x => x !== n); }
    else if (t.dataset.scopeFlag) sc[t.dataset.scopeFlag] = t.checked;
    else return;
    am2ScopeRenderPop(false); am2ScopeApply();
  });
  document.addEventListener('input', e => { if (e.target.id === 'am2-scopeq') { S.am2.scope.q = e.target.value; am2ScopeRenderPop(true); } });
  document.addEventListener('keydown', e => {
    if (!S.am2.scope.open) return;
    if (e.key === 'Escape') { e.preventDefault(); close(); return; }
    const pop = document.getElementById('am2-scopepop');
    if (!pop || !pop.contains(document.activeElement)) return;
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      const boxes = [...pop.querySelectorAll('input')], i = boxes.indexOf(document.activeElement);
      const n = boxes[Math.max(0, Math.min(boxes.length - 1, i + (e.key === 'ArrowDown' ? 1 : -1)))];
      if (n) { e.preventDefault(); n.focus(); }
    } else if (e.key === 'Enter' && document.activeElement.type === 'checkbox') { e.preventDefault(); document.activeElement.click(); }
  });
  window.addEventListener('resize', am2ScopePlace);
  window.addEventListener('hashchange', () => { if (!location.hash.startsWith('#ambient')) close(); });
}

if (typeof module !== 'undefined') module.exports = {am2ScopeDefault, am2ScopeResolve, am2ScopeLabel, am2ScopeSplit, am2IsSystemNs};
