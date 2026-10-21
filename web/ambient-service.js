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
  return `waypoint <span class="am2-whywrap"><a href="#" class="am2-mono am2-wplink" data-am2-wp>${esc(h.waypoint.name)}</a><div class="am2-pop am2-wppop" hidden><div class="am2-pop-i"><b>${esc(h.waypoint.namespace)}/${esc(h.waypoint.name)}</b><div>${h.waypoint.ready ? 'Accepted and programmed.' : esc(h.waypoint.reason || 'Not ready.')}</div></div><div style="margin-top:8px"><a class="am2-lnk" href="${am2Hash({...am2Route(), drawer: 'add:waypoint'})}">Change waypoint…</a></div></div></span> ${h.waypoint.ready ? am2Pill('ok', 'Ready') : `<span title="${esc(h.waypoint.reason)}">${am2Pill('warn', 'Not ready')}</span>`}`;
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

function am2Strip(route, data, ref, writer, variant, extra) {
  const clusters = data.summary.clusters.names.filter(n => (data.services || []).some(r => r.ref.namespace === ref.namespace && r.ref.name === ref.name && r.perCluster?.[n]?.present));
  const seg = clusters.length > 1 ? `<span class="am2-seg">${clusters.map(n => `<a href="${am2Hash({...route, svc: am2SvcPath(n, ref.namespace, ref.name)})}" class="${n === ref.cluster ? 'on' : ''}">${esc(n)}</a>`).join('')}</span>` : `<span class="am2-seg"><a class="on">${esc(ref.cluster)}</a></span>`;
  if (variant === 'checks') {
    const names = clusters.length > 1 ? clusters : [ref.cluster];
    const fresh = S.ws?.live ? `<span class="am2-dot"></span> Reading the live mirror · updated ${Math.max(0, Math.round((Date.now() - (LIVE.lastRender || Date.now())) / 1000))}s ago` : am2Freshness();
    const f = S.am2.checks?.filter || 'problems';
    return `<div class="am2-mode"><span>Cluster</span><span class="am2-seg">${names.map(n => `<a href="${am2Hash({...route, svc: am2SvcPath(n, ref.namespace, ref.name)})}" class="${n === ref.cluster ? 'on' : ''}">${esc(n)}</a>`).join('')}${names.length > 1 ? '<a href="#" data-am2-compare>Compare</a>' : ''}</span><span style="margin-left:12px">${fresh}</span><span class="am2-spacer"></span><span class="am2-seg"><a href="#" data-am2-filter="problems" class="${f === 'problems' ? 'on' : ''}">Problems ${esc(extra?.problems ?? 0)}</a><a href="#" data-am2-filter="all" class="${f === 'all' ? 'on' : ''}">All ${esc(extra?.all ?? 0)}</a></span></div>`;
  }
  const right = writer ? `Changes are written to <b>${esc(ref.cluster)}</b>` : 'Read-only: open with --kubeconfig to change this';
  return `<div class="am2-strip">${seg}<span>${am2Freshness()}</span><span class="am2-spacer"></span><span>${right}</span></div>`;
}

function am2SvcHeader(route, data, ref, cfg, writer, counts, variant, extra) {
  const h = cfg.header || {};
  const ports = h.ports?.length ? `${h.ports.length === 1 ? 'port' : 'ports'} ${h.ports.join(', ')}` : '';
  const meta = (variant === 'checks' ? [ports, h.selectorPods ? `${h.selectorPods} ${h.selectorPods === 1 ? 'workload' : 'workloads'}${h.versions?.length ? ' (' + esc(h.versions.join(', ')) + ')' : ''}` : '', h.inAmbient ? 'in ambient' : '', am2WaypointMeta(h)] : [ports, h.versions?.length ? esc(h.versions.join(', ')) : '', am2WaypointMeta(h)]).filter(Boolean).join(' · ');
  return `${am2Crumb(ref)}<div class="am2-card"><div class="am2-objhead"><span class="am2-kind">Service</span><div style="flex:1"><h1>${esc(ref.namespace)}/${esc(ref.name)}</h1><div class="am2-meta">${meta}</div></div>${am2Btn('Test service', 'data-am2-testsvc', 'sm0')}${am2Btn('YAML', 'data-am2-yaml', 'sm0')}</div>${am2SvcTabs(route, data, ref, counts)}${am2Strip(route, data, ref, writer, variant, extra)}</div>`;
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
  if (g.id === 'access' && !cfg.header?.waypoint && !hint) hint = `<div class="am2-hint"><span>!</span><span style="flex:1">L7 rules need a waypoint.</span><a class="am2-lnk" href="${am2Hash({...ctx.route, drawer: 'add:waypoint'})}">Set one up →</a></div>`;
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
// Line icons (24px grid) for the task cards, keyed by task id.
const AM2_TASK_ICONS = {
  'access-rule': '<path d="M12 3l7 3v5c0 4.5-3 8-7 10-4-2-7-5.5-7-10V6z"/><path d="M9 12l2 2 4-4"/>',
  'methods': '<path d="M4 7h16M4 12h10M4 17h6"/>',
  'jwt': '<circle cx="8" cy="15" r="4"/><path d="M11 12l8-8M16 7l3 3M14 9l2 2"/>',
  'l4-default-deny': '<circle cx="12" cy="12" r="9"/><path d="M5.6 5.6l12.8 12.8"/>',
  'require-waypoint': '<path d="M4 20V10l8-6 8 6v10"/><path d="M9 20v-6h6v6"/>',
  'l4-allow-namespace': '<rect x="4" y="4" width="7" height="7" rx="1.5"/><rect x="13" y="4" width="7" height="7" rx="1.5"/><rect x="4" y="13" width="7" height="7" rx="1.5"/><rect x="13" y="13" width="7" height="7" rx="1.5"/>',
  'l4-allow-service-account': '<circle cx="12" cy="8" r="4"/><path d="M4 21c0-4.4 3.6-7 8-7s8 2.6 8 7"/>',
  'weighted-routing': '<circle cx="12" cy="5" r="2"/><circle cx="6" cy="19" r="2"/><circle cx="18" cy="19" r="2"/><path d="M12 7v4M12 11l-6 6M12 11l6 6"/>',
  'header-routing': '<path d="M3 12h13M12 6l6 6-6 6"/><path d="M20 5v14"/>',
  'mirroring': '<rect x="8" y="8" width="12" height="12" rx="2"/><path d="M16 8V6a2 2 0 00-2-2H6a2 2 0 00-2 2v8a2 2 0 002 2h2"/>',
  'load-balancing': '<path d="M12 3v18M6 21h12M5 7h14"/><path d="M5 7l-3 7a3 3 0 006 0zM19 7l-3 7a3 3 0 006 0z"/>',
  'locality-failover': '<circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3c3 3 3 15 0 18M12 3c-3 3-3 15 0 18"/>',
  'service-failover': '<path d="M4 8h13l-3-3M20 16H7l3 3"/>',
  'strict-mtls': '<rect x="5" y="11" width="14" height="9" rx="2"/><path d="M8 11V8a4 4 0 018 0v3"/>',
  'mesh-strict-mtls': '<circle cx="6" cy="7" r="2.5"/><circle cx="18" cy="7" r="2.5"/><circle cx="12" cy="18" r="2.5"/><path d="M8.5 7h7M7.5 9l3.5 7M16.5 9L13 16"/>',
  'timeouts': '<circle cx="12" cy="13" r="8"/><path d="M12 9v4l3 2M9 3h6"/>',
  'endpoint-failover': '<path d="M12 4l9 16H3z"/><path d="M12 10v4M12 17v.5"/>',
  'circuit-breaker': '<path d="M13 3L5 14h6l-1 7 8-11h-6z"/>',
  'retries': '<path d="M20 12a8 8 0 11-2.4-5.7M20 4v5h-5"/>',
  'rate-limit': '<path d="M4 17a8 8 0 1116 0"/><path d="M12 17l4-5"/>',
  'global-rate-limit': '<path d="M5 20V10M12 20V4M19 20v-7"/>',
  'global-service': '<circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3c3 3 3 15 0 18M12 3c-3 3-3 15 0 18"/>'
};
const am2TaskIcon = id => `<svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${AM2_TASK_ICONS[id] || '<circle cx="12" cy="12" r="3"/>'}</svg>`;
function am2TaskCard(t, ctx) {
  const cls = t.status === 'configured' || t.status === 'differs' ? 'on' : t.status === 'recommended' ? 'rec' : ['unavailable', 'needs-waypoint'].includes(t.status) ? 'off' : '';
  const now = am2TaskNow(t);
  return `<div class="am2-tc ${cls}"><div class="am2-th"><span class="am2-ti">${am2TaskIcon(t.id)}</span><div class="am2-tx"><span class="am2-tt">${esc(t.shortLabel)}</span>${t.layer ? `<span class="am2-tlayer">${esc(t.layer)}</span>` : ''}</div>${am2StatusPill(t)}</div><div class="am2-td">${esc(am2Desc(t, ctx))}</div>${now ? `<div class="am2-now ${cls === 'off' ? 'am2-sub' : ''}">${esc(now)}</div>` : ''}<div class="am2-ft">${am2TaskActions(t, ctx)}</div></div>`;
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
  const [cfg, writer, suites] = await Promise.all([api('api/ambient/service-config', ref), am2Writer(ref.cluster), api('api/ambient/suites/service', ref).catch(() => ({cases: 0}))]);
  A.cfg = cfg; A.writer = writer; A.key = route.svc;
  const changes = (S.am2.tests || []).filter(t => t.request?.namespace === ref.namespace && (t.request?.service || t.request?.name) === ref.name).length;
  const counts = {changes: changes || '', tests: suites.cases || ''};
  const ctx = {ref, route: {...route, drawer: ''}, writer, cfg};
  const view = route.cview || am2Pref('cview', 'summary');
  const variant = stab === 'checks' ? 'checks' : '';
  let extra = {};
  if (stab === 'checks' && typeof am2LoadChecks === 'function') extra = await am2LoadChecks(route, ref);
  const head = am2SvcHeader(route, data, ref, cfg, writer, counts, variant, extra);
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
    const wp = t.closest('[data-am2-wp]');
    if (wp) { e.preventDefault(); const pop = wp.parentElement.querySelector('.am2-pop'), was = pop.hidden; root.querySelectorAll('.am2-pop').forEach(p => { p.hidden = true; }); pop.hidden = !was; return; }
    const why = t.closest('[data-am2-why]');
    if (why) { e.preventDefault(); const pop = why.parentElement.querySelector('.am2-pop'); const was = pop.hidden; root.querySelectorAll('.am2-pop').forEach(p => p.hidden = true); pop.hidden = !was; return; }
    if (!t.closest('.am2-whywrap')) root.querySelectorAll('.am2-pop').forEach(p => p.hidden = true);
    if (t.closest('[data-am2-compare]')) { e.preventDefault(); am2Compare(ref); return; }
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

// Per-cluster state of every configured resource, from the service-config drift data.
function am2Compare(ref) {
  const cfg = S.am2.svc.cfg, clusters = cfg.clusters || [];
  const rows = (cfg.drift || []).map(d => `<tr><td><span class="am2-kind">${esc(d.ref?.kind)}</span> ${esc(d.ref?.name || '')}</td>${clusters.map(c => `<td>${c === ref.cluster ? am2Pill('ok', 'here') : ({same: am2Pill('ok', 'same'), differs: am2Pill('warn', 'differs'), missing: am2Pill('bad', 'missing')}[d.clusters?.[c]] || am2Pill('mut', '—'))}</td>`).join('')}</tr>`).join('');
  modal('Compare ' + ref.namespace + '/' + ref.name + ' across clusters', `<div class="am2"><p class="am2-sub" style="margin-bottom:10px">${esc(cfg.multicluster || 'The configuration is the same on every connected cluster.')}</p><table><thead><tr><th>Resource</th>${clusters.map(c => `<th>${esc(c)}</th>`).join('')}</tr></thead><tbody>${rows || `<tr><td colspan="${clusters.length + 1}" class="am2-empty">No configured resources to compare.</td></tr>`}</tbody></table></div>`);
}
