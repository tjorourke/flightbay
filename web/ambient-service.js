'use strict';
/* Ambient v2: the Service page. Mockups 01 (Summary), 01c (All tasks, Cards), 01d (All tasks, List).
   Header, tabs and cluster strip are shared by every tab of the page. */

const AM2_STABS = ['configuration', 'checks', 'tests', 'path', 'changes'];
const AM2_PREF = {cview: 'flightbay.am2.cview', show: 'flightbay.am2.show'};
const am2Pref = (k, d) => { try { return localStorage.getItem(AM2_PREF[k]) || d; } catch (_) { return d; } };
const am2SetPref = (k, v) => { try { localStorage.setItem(AM2_PREF[k], v); } catch (_) { /* private mode */ } };

S.am2.svc = S.am2.svc || {cfg: null, key: '', contexts: null, find: '', hideLocked: false, why: ''};

const am2Safe = s => esc(s).replace(/&lt;code&gt;/g, '<code>').replace(/&lt;\/code&gt;/g, '</code>');
const am2Kinds = ['AuthorizationPolicy', 'HTTPRoute'];

async function am2Writer(cluster) {
  const A = S.am2.svc;
  if (!A.contexts) A.contexts = await api('api/ambient/contexts').catch(() => []);
  return (A.contexts || []).filter(c => c.liveCluster === cluster).length === 1 ? A.contexts.find(c => c.liveCluster === cluster) : null;
}

// ---------- header, tabs, strip ----------
function am2Crumb(ref) {
  return `<div class="am2-bc"><a href="${am2Hash({tab: ''})}">Istio Ambient</a> › <a href="${am2Hash({tab: ''})}">Services</a> › <b>${esc(ref.namespace)}/${esc(ref.name)}</b></div>`;
}

function am2WaypointMeta(h) {
  if (!h?.waypoint) return 'no waypoint';
  return `waypoint <span class="am2-mono">${esc(h.waypoint.name)}</span> ${h.waypoint.ready ? am2Pill('ok', 'Ready') : `<span title="${esc(h.waypoint.reason)}">${am2Pill('warn', 'Not ready')}</span>`}`;
}

function am2ChecksLabel(data, ref) {
  const row = (data.services || []).find(r => r.ref.namespace === ref.namespace && r.ref.name === ref.name);
  const c = row?.perCluster?.[ref.cluster]?.checks || row?.checks || {};
  return c.fail ? `${c.fail} failed` : c.warn ? `${c.warn} ${c.warn === 1 ? 'warning' : 'warnings'}` : '';
}

function am2SvcTabs(route, data, ref, counts) {
  const cur = AM2_STABS.includes(route.stab) ? route.stab : 'configuration';
  const t = (id, label, count) => `<a href="${am2Hash({svc: route.svc, stab: id === 'configuration' ? '' : id})}" class="${cur === id ? 'on' : ''}">${esc(label)}${count ? ` <span class="am2-c">${esc(count)}</span>` : ''}</a>`;
  return `<div class="am2-tabs2">${t('configuration', 'Configuration')}${t('checks', 'Checks', am2ChecksLabel(data, ref))}${t('tests', 'Tests', counts.tests)}${t('path', 'Request path')}${t('changes', 'Changes', counts.changes)}</div>`;
}

function am2Freshness() {
  if (S.ws?.live) {
    const s = Math.max(0, Math.round((Date.now() - (LIVE.lastRender || Date.now())) / 1000));
    return `<span class="am2-dot"></span> Live · updated ${s}s ago`;
  }
  return `Reading snapshot ${esc(S.ws?.case ? 'case ' + S.ws.case : 'from this bundle')}`;
}

function am2Strip(route, data, ref, writer) {
  const clusters = data.summary.clusters.names.filter(n => (data.services || []).some(r => r.ref.namespace === ref.namespace && r.ref.name === ref.name && r.perCluster?.[n]?.present));
  const seg = clusters.length > 1 ? `<span class="am2-seg">${clusters.map(n => `<a href="${am2Hash({...route, svc: am2SvcPath(n, ref.namespace, ref.name)})}" class="${n === ref.cluster ? 'on' : ''}">${esc(n)}</a>`).join('')}</span>` : `<span class="am2-seg"><a class="on">${esc(ref.cluster)}</a></span>`;
  const right = writer ? `Changes are written to <b>${esc(ref.cluster)}</b>` : 'Read-only: open with --kubeconfig to change this';
  return `<div class="am2-strip">${seg}<span>${am2Freshness()}</span><span class="am2-spacer"></span><span>${right}</span></div>`;
}

function am2SvcHeader(route, data, ref, cfg, writer, counts) {
  const h = cfg.header || {};
  const meta = [h.ports?.length ? `${h.ports.length === 1 ? 'port' : 'ports'} ${h.ports.join(', ')}` : '', h.versions?.length ? esc(h.versions.join(', ')) : '', am2WaypointMeta(h)].filter(Boolean).join(' · ');
  return `${am2Crumb(ref)}<div class="am2-card"><div class="am2-objhead"><span class="am2-kind">Service</span><div style="flex:1"><h1>${esc(ref.namespace)}/${esc(ref.name)}</h1><div class="am2-meta">${meta}</div></div>${am2Btn('Test service', 'data-am2-testsvc', 'sm0')}${am2Btn('YAML', 'data-am2-yaml', 'sm0')}</div>${am2SvcTabs(route, data, ref, counts)}${am2Strip(route, data, ref, writer)}</div>`;
}

// ---------- Configuration: shared helpers ----------
const am2Group = (cfg, id) => cfg.tasks.filter(t => t.group === id);
const am2Tone = r => ({DENY: 'bad', ALLOW: 'ok', Split: 'info', Inherited: 'mut'}[r.action] || 'tl');
const am2ObjHash = ref => am2Hash({obj: [ref.cluster, ref.kind, ref.namespace, ref.name].join('/'), otab: 'summary'});
const am2Weights = res => [...String(res?.summary || '').matchAll(/(\d+)%\s+([\w.-]+)/g)].map(m => ({pct: +m[1], name: m[2]}));

function am2Item(task, r, ctx) {
  const ref = r.ref || {}, kindLine = `${esc(ref.kind)} · ${esc(ref.name)}${ref.namespace && ref.namespace !== ctx.ref.namespace ? '' : ''}`;
  const inherited = task.status === 'inherited', pill = inherited ? am2Pill('mut', 'Inherited') : am2Pill(am2Tone(r), r.action || 'Set');
  let links;
  if (r.editable && !inherited) {
    const test = am2Kinds.includes(ref.kind) ? `<a class="am2-lnk" href="${am2Hash({obj: [ref.cluster, ref.kind, ref.namespace, ref.name].join('/'), otab: 'tests'})}">Test</a>` : '';
    links = `${test}<a class="am2-lnk" href="${am2Hash({...ctx.route, drawer: 'edit:' + (r.editKey || '')})}">Edit</a>`;
  } else links = `<a class="am2-lnk" ${am2Kinds.includes(ref.kind) ? `href="${am2ObjHash(ref)}"` : `data-am2-open='${esc(JSON.stringify(ref))}' href="#"`}>Open</a>`;
  return `<div class="am2-item">${pill}<div class="am2-nm"><div class="am2-l">${esc(r.label || r.summary)}</div><div class="am2-s">${kindLine}</div></div>${links}</div>`;
}

function am2WeightBar(res) {
  const w = am2Weights(res);
  if (!w.length) return '';
  const colours = ['var(--teal)', '#7a3fc0', '#c2410c', '#1769aa'];
  return `<div class="am2-item" style="border:0;padding-top:0"><div style="flex:1"><div class="am2-bar" style="height:12px">${w.map((x, i) => `<i style="width:${x.pct}%;background:${colours[i % 4]}"></i>`).join('')}</div><div class="am2-sub" style="display:flex;justify-content:space-between;margin-top:4px">${w.map(x => `<span>${esc(x.name)} ${x.pct}%</span>`).join('')}</div></div></div>`;
}

const AM2_HINTS = {'require-waypoint': 'Callers can skip this rule by going straight to the Pods.'};
const AM2_ADD_ORDER = {
  access: ['methods', 'jwt', 'access-rule', 'l4-default-deny', 'l4-allow-namespace'],
  routing: ['header-routing', 'mirroring', 'load-balancing', 'weighted-routing', 'locality-failover', 'service-failover'],
  transport: ['strict-mtls'],
  resilience: ['timeouts', 'endpoint-failover', 'circuit-breaker', 'retries', 'rate-limit', 'global-rate-limit']
};
function am2AddLabel(t, ref) { return t.id === 'strict-mtls' ? `Require mTLS for ${ref.name}` : t.shortLabel; }

function am2Card(cfg, g, ctx) {
  const tasks = am2Group(cfg, g.id), items = [];
  let hint = '';
  for (const t of tasks) {
    if (t.status === 'configured' || t.status === 'inherited') for (const r of t.resources || []) items.push({t, html: am2Item(t, r, ctx), res: r});
    if (t.status === 'recommended') hint = `<div class="am2-hint"><span>!</span><span style="flex:1">${esc(AM2_HINTS[t.id] || t.reason)}</span><a class="am2-lnk" href="${am2Hash({...ctx.route, drawer: 'add:' + t.id})}">${t.id === 'require-waypoint' ? 'Block that →' : 'Add it →'}</a></div>`;
    else if (t.hint && t.status === 'configured' && !hint) hint = `<div class="am2-hint"><span>!</span><span style="flex:1">${esc(t.hint)}</span></div>`;
  }
  for (const o of cfg.other || []) if (o.group === g.id) items.push({html: `<div class="am2-item">${am2Pill('mut', 'Other')}<div class="am2-nm"><div class="am2-l">Other configuration</div><div class="am2-s">${esc(o.ref?.kind)} · ${esc(o.ref?.name)}</div></div><a class="am2-lnk" href="#" data-am2-open='${esc(JSON.stringify(o.ref))}'>Open</a></div>`});
  const order = AM2_ADD_ORDER[g.id] || [];
  const addable = order.map(id => tasks.find(t => t.id === id)).filter(t => t && !['unavailable', 'needs-waypoint', 'recommended'].includes(t.status) && !(t.status === 'configured' && t.id !== 'access-rule' && t.id !== 'timeouts'));
  const shown = addable.slice(0, 3), more = addable.length > 3;
  const locked = tasks.filter(t => ['unavailable', 'needs-waypoint'].includes(t.status));
  const answer = cfg.answers?.[g.id] || '', empty = cfg.answerEmpty?.[g.id];
  const weights = g.id === 'routing' ? tasks.find(t => t.id === 'weighted-routing' && t.status === 'configured')?.resources?.[0] : null;
  const body = items.map((x, i) => (weights && x.res === weights ? am2WeightBar(weights) : '') + x.html + (hint && x.t?.id === 'access-rule' && g.id === 'access' && i === items.length - 1 ? hint : '')).join('');
  const hintTail = hint && !(g.id === 'access' && items.some(x => x.t?.id === 'access-rule')) ? hint : '';
  return `<div class="am2-qc"><div class="am2-qh">${esc(g.question)}</div><div class="am2-ans ${empty ? 'am2-ans-empty' : ''}">${g.id === 'routing' ? esc(answer.replace(/<\/?code>/g, '')) : am2Safe(answer)}</div>${body}${hintTail}<div class="am2-add"><b>Add</b>${shown.map(t => `<a class="am2-lnk" href="${am2Hash({...ctx.route, drawer: 'add:' + t.id})}">${esc(am2AddLabel(t, ctx.ref))}</a>`).join('')}${more ? `<a class="am2-lnk" href="${am2Hash({...ctx.route, cview: 'all'})}" data-am2-more="${g.id}">More…</a>` : ''}</div>${locked.length ? `<div class="am2-add am2-locked"><span class="am2-lockrow">${locked.slice(0, 2).map(t => `<span class="am2-lock">🔒 ${esc(t.shortLabel)}</span>`).join('')}</span><span class="am2-sub" style="flex:1;text-align:right;white-space:nowrap">Not available on this waypoint · ${am2Why(locked, g.id)}</span></div>` : ''}</div>`;
}

function am2Why(tasks, key) {
  return `<span class="am2-whywrap"><a class="am2-lnk" style="font-weight:500" href="#" data-am2-why="${esc(key)}">Why?</a><div class="am2-pop" hidden>${tasks.map(t => `<div class="am2-pop-i"><b>${esc(t.shortLabel)}</b><div>${esc(t.reason || t.shortReason || '')}</div></div>`).join('')}</div></span>`;
}

function am2McFooter(cfg, ctx) {
  if (!cfg.multicluster) return '';
  const missing = Object.entries((cfg.drift || [])[0]?.clusters || {}).filter(([, s]) => s !== 'same').map(([c]) => c);
  const target = missing[0] || (cfg.clusters || []).find(c => c !== ctx.ref.cluster) || '';
  return `<div class="am2-mcfoot"><span class="am2-pill warn">${esc(target)}</span><span style="flex:1">${esc(cfg.multicluster)}</span><a class="am2-lnk" href="#" data-am2-compare>Compare</a><a class="am2-lnk" href="${am2Hash({...ctx.route, drawer: 'copy:' + target})}">Copy to ${esc(target)} →</a></div>`;
}

function am2ViewSwitch(route, cfg, view) {
  const n = cfg.tasks.length;
  return `<span class="am2-seg"><a href="${am2Hash({...route, cview: 'summary', show: ''})}" class="${view === 'summary' ? 'on' : ''}" data-am2-cview="summary">Summary</a><a href="${am2Hash({...route, cview: 'all'})}" class="${view === 'all' ? 'on' : ''}" data-am2-cview="all">All tasks · ${n}</a></span>`;
}

function am2Summary(cfg, route, ctx) {
  const groups = cfg.groups.filter(g => g.id !== 'multicluster');
  return `<div style="display:flex;align-items:center;margin-top:16px"><span class="am2-sub" style="flex:1">What is configured for ${esc(ctx.ref.name)} today, and what you can add.</span>${am2ViewSwitch(route, cfg, 'summary')}</div><div class="am2-qgrid" style="margin-top:10px">${groups.map(g => am2Card(cfg, g, ctx)).join('')}</div>${am2McFooter(cfg, ctx)}`;
}

// ---------- Configuration: All tasks (01c cards, 01d list) ----------
function am2TaskNow(t) {
  if (t.status === 'unavailable' || t.status === 'needs-waypoint') return t.shortReason || String(t.reason || '').split(/(?<=\.)\s/)[0];
  if (t.status === 'recommended') return t.reason || '';
  const r = (t.resources || [])[0];
  if (!r) return '';
  if (t.id === 'weighted-routing') { const w = am2Weights(r); if (w.length) return w.map(x => `${x.name.replace(/^.*-(v\d+)$/, '$1')} ${x.pct}%`).join(' · '); }
  const n = (t.resources || []).length;
  if (t.status === 'inherited') return r.label || r.summary || '';
  return r.action && ['DENY', 'ALLOW'].includes(r.action) ? `${n} ${r.action} · ${r.label || r.summary}` : (r.label || r.summary || '');
}
const am2StatusPill = t => ({configured: am2Pill('ok', 'Configured'), 'not-set': am2Pill('mut', 'Not set'), recommended: am2Pill('warn', 'Recommended'), inherited: am2Pill('info', 'Inherited'), unavailable: am2Pill('mut', '🔒 Not available'), 'needs-waypoint': am2Pill('mut', '🔒 Not available'), differs: am2Pill('warn', 'Differs')}[t.status] || am2Pill('mut', t.status));
function am2TaskActions(t, ctx) {
  const drawer = d => am2Hash({...ctx.route, drawer: d}), r = (t.resources || [])[0];
  if (t.status === 'configured' || t.status === 'differs') {
    const test = r && am2Kinds.includes(r.ref?.kind) ? `<a class="am2-lnk" href="${am2Hash({obj: [r.ref.cluster, r.ref.kind, r.ref.namespace, r.ref.name].join('/'), otab: 'tests'})}">Test</a>` : '';
    return `${test}<a class="am2-lnk" href="${drawer(r?.editKey ? 'edit:' + r.editKey : 'add:' + t.id)}">Edit</a>`;
  }
  if (t.status === 'inherited') return `<a class="am2-lnk" href="${drawer('add:' + t.id)}">Override →</a>`;
  if (t.status === 'unavailable' || t.status === 'needs-waypoint') return am2Why([t], t.id).replace('class="am2-lnk" style="font-weight:500"', 'class="am2-lnk am2-lnk-mut"');
  return `<a class="am2-lnk" href="${drawer('add:' + t.id)}">Add →</a>`;
}
const am2Desc = (t, ctx) => String(t.description || '').replace(/\bthe Service\b/, ctx.ref.name);
function am2TaskCard(t, ctx) {
  const cls = t.status === 'configured' || t.status === 'differs' ? 'on' : t.status === 'recommended' ? 'rec' : ['unavailable', 'needs-waypoint'].includes(t.status) ? 'off' : '';
  const now = am2TaskNow(t);
  return `<div class="am2-tc ${cls}"><div style="display:flex;justify-content:space-between;align-items:center;gap:8px"><span class="am2-tt">${esc(t.shortLabel)}</span>${am2StatusPill(t)}</div><div class="am2-td">${esc(am2Desc(t, ctx))}</div>${now ? `<div class="am2-now ${cls === 'off' ? 'am2-sub' : ''}">${esc(now)}</div>` : ''}<div class="am2-ft">${am2TaskActions(t, ctx)}</div></div>`;
}
function am2TaskRow(t, ctx) {
  const off = ['unavailable', 'needs-waypoint'].includes(t.status);
  return `<div class="am2-lr ${off ? 'off' : ''}"><div><div class="am2-tt">${esc(t.shortLabel)}</div><div class="am2-sub">${esc(am2Desc(t, ctx))}</div></div><div>${am2StatusPill(t)}</div><div class="am2-now">${esc(am2TaskNow(t)) || '—'}</div><div class="am2-ac">${am2TaskActions(t, ctx)}</div></div>`;
}
function am2GroupAnswer(cfg, g, ctx) {
  if (g.id === 'multicluster') {
    const c = (cfg.drift || [])[0]?.clusters || {}, only = (cfg.clusters || []).filter(n => c[n] === undefined || c[n] === 'same');
    return (cfg.clusters || []).length < 2 ? 'Only one cluster is connected' : only.length === (cfg.clusters || []).length ? 'In every connected cluster' : `Only on ${only.join(', ')}`;
  }
  const a = cfg.answers?.[g.id] || '';
  if (g.id === 'routing') return esc(a.replace(/<\/?code>/g, ''));
  return cfg.answerEmpty?.[g.id] ? esc(a.split('.')[0]) : am2Safe(a);
}
function am2AllSections(cfg, route, ctx) {
  const A = S.am2.svc, show = route.show || am2Pref('show', 'cards'), q = A.find.trim().toLowerCase();
  const keep = t => !(A.hideLocked && ['unavailable', 'needs-waypoint'].includes(t.status)) && (!q || (t.shortLabel + ' ' + t.description).toLowerCase().includes(q));
  const sections = cfg.groups.map(g => {
    const ts = am2Group(cfg, g.id).filter(keep);
    if (!ts.length) return '';
    return `<div class="am2-ghead"><h2>${esc(g.question)}</h2><span class="am2-a">${am2GroupAnswer(cfg, g, ctx)}</span></div>${show === 'list' ? `<div class="am2-list">${ts.map(t => am2TaskRow(t, ctx)).join('')}</div>` : `<div class="am2-tgrid">${ts.map(t => am2TaskCard(t, ctx)).join('')}</div>`}`;
  }).join('');
  return sections;
}
function am2AllTasks(cfg, route, ctx) {
  const A = S.am2.svc, show = route.show || am2Pref('show', 'cards'), sections = am2AllSections(cfg, route, ctx);
  A.last = {cfg, route, ctx};
  return `<div class="am2-toolbar">${am2ViewSwitch(route, cfg, 'all')}<input class="am2-input" id="am2-find" style="width:220px" placeholder="Find a task…" value="${esc(A.find)}" aria-label="Find a task"><button type="button" class="am2-chip ${A.hideLocked ? 'on' : ''}" id="am2-hidelocked" style="padding:3px 10px">Hide unavailable</button><span class="am2-spacer"></span><span class="am2-sub">Show as</span><span class="am2-seg"><a href="${am2Hash({...route, cview: 'all', show: 'cards'})}" class="${show === 'cards' ? 'on' : ''}" data-am2-show="cards">Cards</a><a href="${am2Hash({...route, cview: 'all', show: 'list'})}" class="${show === 'list' ? 'on' : ''}" data-am2-show="list">List</a></span></div><div id="am2-alltasks">${sections}</div>`;
}

// ---------- page ----------
async function am2ServicePage(route, data) {
  const A = S.am2.svc, ref = am2SvcRef(route.svc);
  const stab = AM2_STABS.includes(route.stab) ? route.stab : 'configuration';
  const [cfg, writer] = await Promise.all([api('api/ambient/service-config', ref), am2Writer(ref.cluster)]);
  A.cfg = cfg; A.writer = writer; A.key = route.svc;
  const changes = (S.am2.tests || []).filter(t => t.request?.namespace === ref.namespace && (t.request?.service || t.request?.name) === ref.name).length;
  const counts = {changes: changes || '', tests: cfg.tests?.length || ''};
  const ctx = {ref, route: {...route, drawer: ''}, writer, cfg};
  const view = route.cview || am2Pref('cview', 'summary');
  const head = am2SvcHeader(route, data, ref, cfg, writer, counts);
  let body;
  if (stab === 'configuration') body = view === 'all' ? am2AllTasks(cfg, {...route, cview: 'all'}, ctx) : am2Summary(cfg, {...route, cview: 'summary'}, ctx);
  else if (stab === 'checks' && typeof am2ChecksTab === 'function') body = await am2ChecksTab(route, data, ref, ctx);
  else if (stab === 'tests' && typeof am2TestsTab === 'function') body = await am2TestsTab(route, data, ref, ctx);
  else if (stab === 'path' && typeof am2PathTab === 'function') body = await am2PathTab(route, data, ref, ctx);
  else if (stab === 'changes' && typeof am2ChangesTab === 'function') body = await am2ChangesTab(route, data, ref, ctx);
  else body = '<div class="am2-note am2-muted">This tab is not built yet.</div>';
  return head + body;
}

function am2ServiceAfter(route) {
  const A = S.am2.svc, root = $('#am2-root');
  if (!root || !route.svc) return;
  const ref = am2SvcRef(route.svc);
  const svcObj = () => (S.ambient.inventory?.services || []).find(s => s.cluster === ref.cluster && s.namespace === ref.namespace && s.name === ref.name) || {kind: 'Service', ...ref};
  const ensureInventory = async () => { if (!S.ambient.inventory) S.ambient.inventory = await api('api/ambient'); };
  root.addEventListener('click', async e => {
    const t = e.target;
    if (t.closest('[data-am2-testsvc]')) { await ensureInventory(); amOpenHTTPTest(svcObj()); return; }
    if (t.closest('[data-am2-yaml]')) { openObject({...svcObj(), kind: 'Service'}, 'yaml'); return; }
    const open = t.closest('[data-am2-open]');
    if (open) { e.preventDefault(); openObject(JSON.parse(open.dataset.am2Open)); return; }
    const why = t.closest('[data-am2-why]');
    if (why) { e.preventDefault(); const pop = why.parentElement.querySelector('.am2-pop'); const was = pop.hidden; root.querySelectorAll('.am2-pop').forEach(p => p.hidden = true); pop.hidden = !was; return; }
    if (!t.closest('.am2-whywrap')) root.querySelectorAll('.am2-pop').forEach(p => p.hidden = true);
    const cv = t.closest('[data-am2-cview]'); if (cv) am2SetPref('cview', cv.dataset.am2Cview);
    const sh = t.closest('[data-am2-show]'); if (sh) am2SetPref('show', sh.dataset.am2Show);
    if (t.closest('[data-am2-more]')) am2SetPref('cview', 'all');
  });
  const f = $('#am2-find');
  if (f) f.oninput = () => { A.find = f.value; const l = A.last; $('#am2-alltasks').innerHTML = am2AllSections(l.cfg, l.route, l.ctx); };
  const hl = $('#am2-hidelocked');
  if (hl) hl.onclick = () => { A.hideLocked = !A.hideLocked; render(); };
}
AM2_AFTER.push(am2ServiceAfter);
