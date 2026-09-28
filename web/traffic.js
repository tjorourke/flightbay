'use strict';
// Activity (who called which MCP tool, model or agent through agentgateway)
// and the Traffic map (ambient connections, with "why was this denied?").
// Both are Flightbay Enterprise features. Loaded after app.js, which routes
// #activity and #traffic here.

S.act = {tab: 'map', kinds: {tool: true, model: true, agent: true, mcp: true, http: false}, q: '', sel: '', data: null, key: '', gz: null, gfull: false, fopen: {}, showDeclared: false};
S.tf = {tab: 'map', flag: '', ns: '', q: '', sel: '', data: null, key: '', gz: null, gfull: false, fopen: {}, ex: null, exFrom: '', exTo: ''};

const fbKey = () => `${S.state?.revision ?? ''}/${S.state?.logLines ?? ''}`;
const fbPlural = (n, one, many) => `${num(n)} ${n === 1 ? one : (many || one + 's')}`;
const fbEntOff = (title, what, flag) => head(title, '') + `<section class="panel"><div class="empty"><strong>${esc(title)} is a Flightbay Enterprise feature</strong>${esc(what)}<br>
  Turn it on by starting the desk with <code>FLIGHTBAY_ENTERPRISE=1 flightbay ui</code> (or <code>FLIGHTBAY_FEATURES=${esc(flag)}</code>).</div></section>`;
const fbTime = t => t && !String(t).startsWith('0001') ? fmtDateTime(t) : '—';
const fbTok = n => n >= 1e6 ? (n / 1e6).toFixed(1) + 'M' : n >= 1e4 ? Math.round(n / 1e3) + 'k' : num(n);
const fbOpenRef = r => openObject({cluster: r.cluster, group: r.group, resource: r.resource, namespace: r.namespace, name: r.name});

// ---------- findings (the same look as Agent reach) ----------
function fbFindings(list, st, key) {
  if (!(list || []).length) return '<div class="empty"><strong>No findings</strong></div>';
  return list.map((f, i) => {
    const open = !!st.fopen[i];
    const its = f.items || [];
    const hasCls = its.some(x => x.class), hasAcc = its.some(x => x.access), hasNote = its.some(x => x.note);
    const items = its.length ? `<div class="table-scroll rc-items"><table><thead><tr><th>Affected</th>${hasCls ? '<th>Reason</th>' : ''}${hasAcc ? '<th>Seen</th>' : ''}${hasNote ? '<th></th>' : ''}</tr></thead><tbody>
      ${its.map(x => `<tr><td class="mono">${esc(x.target)}</td>${hasCls ? `<td>${esc(x.class || '')}</td>` : ''}${hasAcc ? `<td class="mono">${esc(x.access || '')}</td>` : ''}${hasNote ? `<td class="muted rc-small">${esc(x.note || '')}</td>` : ''}</tr>`).join('')}</tbody></table></div>` : '';
    return `<div class="rc-finding ${open ? 'open' : ''}"><button class="rc-fhead" data-fbf="${key}:${i}">
      <span class="pst-sev pst-sev-${esc(f.severity)}">${esc(f.severity)}</span><strong>${esc(f.title)}</strong>
      ${its.length ? `<span class="tag">${its.length} affected</span>` : ''}<span style="flex:1"></span>${f.fix ? '<small class="muted">fix included</small>' : ''}<span class="muted">${open ? '▾' : '▸'}</span></button>
      ${open ? `<div class="rc-fbody"><p>${esc(f.detail)}</p>${items}${(f.advice || []).length ? `<h3 class="mt">What to do</h3><ul class="rc-advice">${f.advice.map(x => `<li>${esc(x)}</li>`).join('')}</ul>` : ''}
        ${f.fix ? `<div class="row between mt"><h3>How to fix</h3><div class="row"><button data-fbcopy="${key}:${i}">Copy YAML</button>${guideLinks(f.source, f.docs)}</div></div><pre class="code mt">${esc(f.fix)}</pre>`
          : f.source || f.docs ? `<p class="mt row">${guideLinks(f.source, f.docs)}</p>` : ''}</div>` : ''}</div>`;
  }).join('');
}
function fbFindingsWire(st, list, key) {
  document.querySelectorAll(`[data-fbf^="${key}:"]`).forEach(b => b.onclick = () => { const i = +b.dataset.fbf.split(':')[1]; st.fopen[i] = !st.fopen[i]; const y = scrollY; render().then(() => scrollTo(0, y)); });
  document.querySelectorAll(`[data-fbcopy^="${key}:"]`).forEach(b => b.onclick = () => { const f = list[+b.dataset.fbcopy.split(':')[1]]; navigator.clipboard.writeText(f.fix).then(() => toast('YAML copied'), () => toast('Clipboard unavailable')); });
}

// ---------- a two-column graph: sources on the left, destinations on the right ----------
// left/right: [{id, label, sub, risk, icon, dim}]; edges: [{from, to, risk, dash, w, tip}].
// Rows on the right are ordered by the average row of what calls them, and the
// left by the average row of what they call, so lines cross as little as a
// single pass allows. Lines are curves in the gap between the two columns, so
// they never cross a box.
function fbGraph({left, right, edges, heads, sel}) {
  const bw = 250, bh = 42, vg = 10, gap = Math.max(220, Math.min(420, 90 + edges.length * 4)), HB = 52, top = HB + 16;
  const L = [...left], R = [...right];
  const li = new Map(L.map((n, i) => [n.id, i]));
  const bary = (n, map, key) => { const xs = edges.filter(e => e[key] === n.id).map(e => map.get(e[key === 'to' ? 'from' : 'to'])).filter(x => x !== undefined); return xs.length ? xs.reduce((a, b) => a + b, 0) / xs.length : 1e9; };
  R.sort((a, b) => bary(a, li, 'to') - bary(b, li, 'to') || a.label.localeCompare(b.label));
  const ri = new Map(R.map((n, i) => [n.id, i]));
  L.sort((a, b) => bary(a, ri, 'from') - bary(b, ri, 'from') || a.label.localeCompare(b.label));
  const lrow = new Map(L.map((n, i) => [n.id, i])), rrow = new Map(R.map((n, i) => [n.id, i]));
  const rows = Math.max(L.length, R.length, 1);
  const lOff = (rows - L.length) * (bh + vg) / 2, rOff = (rows - R.length) * (bh + vg) / 2;
  const X0 = 16, X1 = 16 + bw + gap;
  const W = X1 + bw + 16, H = top + rows * (bh + vg) + 10;
  const yL = id => top + lOff + lrow.get(id) * (bh + vg), yR = id => top + rOff + rrow.get(id) * (bh + vg);
  const fit = (s, px) => { const n = Math.max(4, Math.floor((bw - 44) / px)); s = String(s || ''); return s.length > n ? s.slice(0, n - 1) + '…' : s; };
  const risk = r => ['red', 'amber', 'green', 'grey'].includes(r) ? r : 'grey';
  const rank = {grey: 0, green: 1, amber: 2, red: 3};
  const es = edges.filter(e => lrow.has(e.from) && rrow.has(e.to)).sort((a, b) => rank[risk(a.risk)] - rank[risk(b.risk)]);
  const edgeSvg = es.map(e => {
    const sx = X0 + bw, sy = yL(e.from) + bh / 2, tx = X1 - 3, ty = yR(e.to) + bh / 2;
    const d = `M${sx},${sy} C${sx + gap / 2},${sy} ${tx - gap / 2},${ty} ${tx},${ty}`;
    const on = sel && (sel === e.id || sel === e.from || sel === e.to);
    return `<g class="rg-edge rg-${risk(e.risk)} ${e.dash ? 'rg-dash' : ''} ${on ? 'fb-on' : ''}" data-f="${esc(e.from)}" data-t="${esc(e.to)}" data-fbe="${esc(e.id)}"><title>${esc(e.tip || '')}</title><path class="rg-hit" d="${d}"/><path class="rg-line" d="${d}" style="stroke-width:${e.w || 1.4}" marker-end="url(#fba-${risk(e.risk)})"/></g>`;
  }).join('');
  const node = (n, x, y) => `<g class="rg-node rg-${risk(n.risk)} rg-click ${n.dim ? 'rg-dim' : ''} ${sel === n.id ? 'rg-sel' : ''}" data-rgid="${esc(n.id)}" transform="translate(${x},${y})"><title>${esc(n.tip || n.label)}</title>
    <rect width="${bw}" height="${bh}" rx="7"/>${rgIcon(n.icon || 'service', bw - 26, (bh - 16) / 2, 16, 'rg-nic')}<text x="11" y="17" class="rg-l">${esc(fit(n.label, 7.1))}</text><text x="11" y="32" class="rg-s">${esc(fit(n.sub || '', 6.2))}</text></g>`;
  const headSvg = (x, [icon, label, note]) => `<g class="rg-head" transform="translate(${x},8)"><rect class="rg-hbg" width="${bw}" height="${HB - 14}" rx="8"/>
    <circle class="rg-hdot" cx="20" cy="${(HB - 14) / 2}" r="13"/>${rgIcon(icon, 11, (HB - 14) / 2 - 9, 18, 'rg-hic')}
    <text x="42" y="16" class="rg-hl">${esc(label)}</text><text x="42" y="30" class="rg-hn">${esc(note)}</text></g>`;
  const marker = r => `<marker id="fba-${r}" viewBox="0 0 10 10" refX="9" refY="5" markerUnits="userSpaceOnUse" markerWidth="9" markerHeight="9" orient="auto"><path d="M0,0 L10,5 L0,10 z" class="rg-arrow-${r}"/></marker>`;
  return `<svg class="rg" id="rg-svg" data-w="${W}" data-h="${H}" width="100%" height="100%" role="img"><defs>${['red', 'amber', 'green', 'grey'].map(marker).join('')}</defs><g id="rg-root">
    ${headSvg(X0, heads[0])}${headSvg(X1, heads[1])}${edgeSvg}${L.map(n => node(n, X0, yL(n.id))).join('')}${R.map(n => node(n, X1, yR(n.id))).join('')}</g></svg>`;
}

function fbGraphPanel(title, st, svg, controls, legend) {
  return `<section class="panel mt rc-gpanel ${st.gfull ? 'full' : ''}"><div class="panel-head rc-ghead"><div class="row wrap"><h2>${esc(title)}</h2>${controls}</div>
      <div class="row rc-zoom"><button id="rg-out" title="Zoom out (-)">−</button><span id="rg-pct" class="mono muted">100%</span><button id="rg-in" title="Zoom in (+)">+</button><button id="rg-fit" title="Fit (0)">Fit</button><button id="rg-full" title="${st.gfull ? 'Exit full screen (Esc)' : 'Full screen (f)'}">${st.gfull ? '⤡ Exit' : '⤢ Full screen'}</button></div></div>
    <div class="rg-vp" id="rg-vp" tabindex="0">${svg}</div><div class="rc-legend">${legend}<span class="muted">Hover a box to trace its lines · click a box or a line for detail · drag to move · Ctrl+scroll to zoom</span></div></section>`;
}

// Pan and zoom, full screen, hover tracing and clicks for a graph panel.
function fbGraphWire(st, key, onPick) {
  if (!$('#rg-svg')) return;
  // The viewport grows with the graph up to most of the window, and the
  // first fit takes the height into account, so a tall graph starts whole.
  const vp0 = $('#rg-vp'), H = +$('#rg-svg').dataset.h;
  if (!st.gfull) vp0.style.height = Math.round(Math.max(360, Math.min(H * 0.9 + 24, innerHeight * 0.78))) + 'px';
  st.fitH = true;
  rcPanZoom(key, st);
  const keepY = fn => { const y = scrollY; fn(); render().then(() => scrollTo(0, y)); };
  const full = on => keepY(() => { st.gfull = on; document.body.classList.toggle('rg-noscroll', on); });
  const fb = $('#rg-full'); if (fb) fb.onclick = () => full(!st.gfull);
  if (st.gfull) { document.onkeydown = e => { if (e.key === 'Escape' && st.gfull) { document.onkeydown = null; full(false); } }; $('#rg-vp').focus(); }
  const vp = $('#rg-vp'), rg = $('#rg-svg');
  rg.querySelectorAll('[data-rgid]').forEach(g => g.onclick = () => { if (!vp.dataset.dragged) onPick(g.dataset.rgid); });
  rg.querySelectorAll('[data-fbe]').forEach(g => g.onclick = () => { if (!vp.dataset.dragged) onPick(g.dataset.fbe); });
  const clear = () => { rg.classList.remove('rg-hl'); rg.querySelectorAll('.on').forEach(x => x.classList.remove('on')); };
  rg.querySelectorAll('[data-rgid]').forEach(g => {
    g.onmouseenter = () => {
      clear();
      const id = g.dataset.rgid, near = new Set([id]);
      rg.querySelectorAll('.rg-edge').forEach(e => { if (e.dataset.f === id || e.dataset.t === id) { e.classList.add('on'); near.add(e.dataset.f); near.add(e.dataset.t); } });
      rg.querySelectorAll('[data-rgid]').forEach(x => { if (near.has(x.dataset.rgid)) x.classList.add('on'); });
      rg.classList.add('rg-hl');
    };
    g.onmouseleave = clear;
  });
}

// ---------- Activity ----------
const actIntro = 'Who called which MCP tool, model or agent through agentgateway, as whom, what was refused and what it cost.';
const actKinds = {tool: 'Tools', model: 'Models', agent: 'Agents (A2A)', mcp: 'MCP sessions', http: 'Other HTTP routes'};
const actKindIcon = {tool: 'plug', model: 'sparkles', agent: 'bot', mcp: 'plug', http: 'route'};
const actCallerIcon = {workload: 'server', identity: 'key', user: 'key', outside: 'globe', address: 'network'};
const actTargetLabel = t => t.kind === 'tool' ? `${t.server}/${t.label}` : t.kind === 'model' ? `${t.provider ? t.provider + '/' : ''}${t.label}` : t.label;
const actKindName = {tool: 'tool', model: 'model', agent: 'agent', mcp: 'MCP session', http: 'HTTP route'};

function actEdgeRisk(e, callers) {
  const c = callers.get(e.from);
  if (e.outsideAllowlist || (e.denied && e.denied === e.requests)) return 'red';
  if (c?.anonymous || e.denied) return 'amber';
  return 'green';
}

async function activityPage() {
  const A = S.act;
  if (!feature('activity')) return fbEntOff('Activity', 'It shows who called which MCP tool, model or agent through agentgateway, what was refused, and what it cost in tokens.', 'activity');
  if (!A.data || A.key !== fbKey()) { A.data = await api('api/activity'); A.key = fbKey(); }
  const d = A.data, t = d.totals || {};
  const callers = new Map((d.callers || []).map(c => [c.id, c])), targets = new Map((d.targets || []).map(x => [x.id, x]));
  const terms = qTerms(A.q);
  const textOk = (...xs) => terms.every(tm => { const ok = xs.join(' ').toLowerCase().includes((tm.text ?? `${tm.key}:${tm.val}`).toLowerCase()); return tm.neg ? !ok : ok; });
  const edges = (d.edges || []).filter(e => { const tg = targets.get(e.to); return tg && A.kinds[tg.kind] && textOk(callers.get(e.from)?.label || '', actTargetLabel(tg), tg.kind, (e.gateways || []).join(' ')); });
  const stat = (label, value, sub, cls = '') => `<div class="stat"><div class="eyebrow">${label}</div><div class="value ${cls}">${value}</div><p>${sub}</p></div>`;
  const stats = `<div class="stats fb-stats">${stat('Requests', num(t.requests), `through ${fbPlural((d.coverage || []).length, 'gateway')}`)}
    ${stat('Tool calls', num(t.toolCalls), `${fbPlural((d.targets || []).filter(x => x.kind === 'tool').length, 'tool')} called`)}
    ${stat('LLM tokens', fbTok((t.inputTokens || 0) + (t.outputTokens || 0)), `${num(t.llmCalls)} calls · ${fbTok(t.inputTokens || 0)} in, ${fbTok(t.outputTokens || 0)} out${t.cost ? ` · cost ${t.cost.toFixed(4)}` : ''}`)}
    ${stat('Refused', num(t.denied), 'by gateway policy', t.denied ? 'amber' : '')}
    ${stat('Failed', num(t.errors), 'errors from backends or routing', t.errors ? 'amber' : '')}
    ${stat('No identity', num(t.anonymous), 'tool, model or agent calls', t.anonymous ? 'red' : '')}</div>`;
  const cov = (d.coverage || []).length ? `<p class="muted fb-cov">Gateways with request logs: ${d.coverage.map(c => `<span class="mono">${esc(c.gateway)}</span> <small>(${num(c.requests)}${c.waypoint ? ', waypoint' : ''})</small>`).join(' · ')}${d.first && !String(d.first).startsWith('0001') ? ` · ${fbTime(d.first)} to ${fbTime(d.last)}` : ''}</p>` : '';
  const notes = (d.notes || []).map(n => `<div class="notice" style="margin-bottom:12px"><span>△</span><div>${esc(rcCap(n))}.</div></div>`).join('');
  const findings = `<section class="panel"><div class="panel-head"><h2>Findings</h2><small class="muted">${fbPlural((d.findings || []).length, 'finding')} · open one for what it affects</small></div>${fbFindings(d.findings, A, 'act')}</section>`;
  const tabN = (id, label, n, cls) => `<button class="pst-tab ${A.tab === id ? 'on' : ''}" data-acttab="${id}">${esc(label)}${n ? `<span class="pst-tab-n ${cls || ''}">${num(n)}</span>` : ''}</button>`;
  const ch = d.changes;
  const nChanges = ch ? (ch.added || []).length + (ch.removed || []).length + (ch.described || []).length : 0;
  const tabs = `<nav class="pst-tabs">${tabN('map', 'Map')}${tabN('calls', 'Calls', edges.length)}${tabN('tools', 'Tools', (d.targets || []).filter(x => x.kind === 'tool').length)}${tabN('models', 'Models', (d.targets || []).filter(x => x.kind === 'model').length)}${tabN('denials', 'Refused', (d.denials || []).length, (d.denials || []).length ? 'amber' : '')}${tabN('changes', 'Tool changes', nChanges, (ch?.described || []).length ? 'red' : '')}</nav>`;
  const kindCount = k => (d.edges || []).filter(e => targets.get(e.to)?.kind === k).length;
  const kchips = Object.entries(actKinds).filter(([k]) => kindCount(k)).map(([k, l]) => `<button class="chip ${A.kinds[k] ? 'on' : ''}" data-actkind="${k}">${esc(l)} <small>${kindCount(k)}</small></button>`).join('');
  const toolbar = `<div class="toolbar"><div class="row wrap">${kchips}</div><input id="act-q" placeholder="Filter callers, targets and gateways" value="${esc(A.q)}" style="min-width:280px" autocomplete="off" spellcheck="false"></div>`;
  let body = '';
  if (!(d.edges || []).length) body = '<section class="panel"><div class="empty"><strong>No agentgateway requests in this capture</strong>The activity map is read from agentgateway request logs. Collect the gateway and waypoint pods\' logs to fill it.</div></section>';
  else if (A.tab === 'map') body = toolbar + actMap(d, edges, callers, targets) + actSel(d, callers, targets);
  else if (A.tab === 'calls') body = toolbar + actCalls(edges, callers, targets);
  else if (A.tab === 'tools') body = actTools(d, callers);
  else if (A.tab === 'models') body = actModels(d);
  else if (A.tab === 'denials') body = actDenials(d);
  if (A.tab === 'changes') body = actChanges(d);
  return head('Activity', `${esc(actIntro)} <span class="ent-pill">Enterprise</span>`, '<button id="act-term">&gt;_ fbactivity in terminal</button>') + notes + stats + cov + findings + tabs + body;
}

function actMap(d, edges, callers, targets) {
  const A = S.act;
  const leftIds = new Set(edges.map(e => e.from)), rightIds = new Set(edges.map(e => e.to));
  const maxReq = Math.max(1, ...edges.map(e => e.requests));
  const left = [...leftIds].map(id => { const c = callers.get(id); return {id, label: c.label, sub: [c.agentType ? rcTypes[c.agentType] || c.agentType : c.kind, c.anonymous ? 'no identity' : c.users?.length ? 'JWT ' + c.users.join(', ') : ''].filter(Boolean).join(' · '),
    risk: c.anonymous ? 'amber' : c.denied ? 'amber' : 'green', icon: c.agentType ? 'bot' : actCallerIcon[c.kind], tip: [c.label, c.identity, `${num(c.requests)} calls, ${num(c.denied)} refused, ${num(c.errors)} failed`, c.tokens ? `${num(c.tokens)} tokens` : ''].filter(Boolean).join('\n')}; });
  const right = [...rightIds].map(id => { const x = targets.get(id); return {id, label: x.kind === 'tool' ? x.label : actTargetLabel(x), sub: [actKindName[x.kind], x.kind === 'tool' ? x.server : '', x.undeclared ? 'not in its server\'s list' : ''].filter(Boolean).join(' · '),
    risk: x.undeclared ? 'red' : x.denied ? 'amber' : x.errors > x.requests / 2 ? 'amber' : 'green', icon: actKindIcon[x.kind], tip: [actTargetLabel(x), x.description, `${num(x.requests)} calls, ${num(x.denied)} refused, ${num(x.errors)} failed`, (x.gateways || []).length ? 'through ' + x.gateways.join(', ') : ''].filter(Boolean).join('\n')}; });
  const ge = edges.map(e => ({id: e.from + '→' + e.to, from: e.from, to: e.to, risk: actEdgeRisk(e, callers), dash: e.denied > 0, w: 1.2 + 3 * Math.log(1 + e.requests) / Math.log(1 + maxReq),
    tip: `${callers.get(e.from)?.label} → ${actTargetLabel(targets.get(e.to))}\n${num(e.requests)} calls${e.denied ? `, ${num(e.denied)} refused` : ''}${e.errors ? `, ${num(e.errors)} failed` : ''}\nthrough ${(e.gateways || []).join(', ')}${e.outsideAllowlist ? '\nnot in the agent\'s tool list' : ''}`}));
  if (!ge.length) return '<section class="panel mt"><div class="empty"><strong>Nothing matches</strong>Turn on more kinds above, or change the filter.</div></section>';
  const svg = fbGraph({left, right, edges: ge, sel: A.sel, heads: [['bot', 'Callers', `${fbPlural(left.length, 'caller')}, resolved from identity or address`], ['plug', 'Called', `${fbPlural(right.length, 'target')}: tools, models, agents`]]});
  const legend = '<span><i class="rc-sw red"></i>outside the agent\'s tool list, a tool its server does not list, or every call refused</span><span><i class="rc-sw amber"></i>no identity, or some calls refused</span><span><i class="rc-sw green"></i>identified caller</span><span><i class="rc-sw dash"></i>has refusals</span><span class="muted">Line width follows the number of calls</span>';
  return fbGraphPanel('Who called what', A, svg, `<small class="muted">${fbPlural(ge.length, 'caller-target pair')}</small>`, legend);
}

// The panel under the map for the selected box or line.
function actSel(d, callers, targets) {
  const A = S.act;
  if (!A.sel) return '';
  const close = '<button class="ghost" id="act-selx" aria-label="Close">✕</button>';
  const edgeRows = es => `<div class="table-scroll" style="max-height:40vh"><table><thead><tr><th>Caller</th><th>Target</th><th>Calls</th><th>Refused</th><th>Failed</th><th>Tokens</th><th>Gateway</th><th>Last</th></tr></thead><tbody>
    ${es.map(e => `<tr data-actsel="${esc(e.from + '→' + e.to)}" class="fb-row"><td>${esc(callers.get(e.from)?.label || e.from)}</td><td class="mono">${esc(actTargetLabel(targets.get(e.to)))}</td><td class="mono">${num(e.requests)}</td><td class="mono ${e.denied ? 'amber' : ''}">${num(e.denied)}</td><td class="mono">${num(e.errors)}</td><td class="mono">${fbTok((e.inputTokens || 0) + (e.outputTokens || 0))}</td><td class="mono rc-small">${esc((e.gateways || []).join(', '))}</td><td class="rc-small">${fbTime(e.last)}</td></tr>`).join('')}</tbody></table></div>`;
  const c = callers.get(A.sel), x = targets.get(A.sel);
  if (c) {
    const es = (d.edges || []).filter(e => e.from === c.id);
    return `<section class="panel mt" id="act-detail"><div class="panel-head"><h2>${esc(c.label)}</h2><div class="row">${c.ref ? '<button id="act-obj">Open object</button>' : ''}${close}</div></div><div class="panel-body">
      <dl class="metadata"><dt>Kind</dt><dd>${esc(c.agentType ? (rcTypes[c.agentType] || c.agentType) + ' (' + c.kind + ')' : c.kind)}</dd>${c.identity ? `<dt>Mesh identity</dt><dd class="mono">${esc(c.identity)}</dd>` : ''}${(c.users || []).length ? `<dt>JWT subjects</dt><dd class="mono">${esc(c.users.join(', '))}</dd>` : ''}${(c.addresses || []).length ? `<dt>Addresses</dt><dd class="mono">${esc(c.addresses.join(', '))}</dd>` : ''}
      <dt>Calls</dt><dd>${num(c.requests)} · ${num(c.denied)} refused · ${num(c.errors)} failed${c.tokens ? ` · ${num(c.tokens)} tokens` : ''}</dd>${c.anonymous ? '<dt>Identity</dt><dd class="red">none on any call: no mesh identity and no JWT subject</dd>' : ''}</dl></div>${edgeRows(es)}</section>`;
  }
  if (x) {
    const es = (d.edges || []).filter(e => e.to === x.id);
    return `<section class="panel mt" id="act-detail"><div class="panel-head"><h2>${esc(actTargetLabel(x))}</h2>${close}</div><div class="panel-body">
      <dl class="metadata"><dt>Kind</dt><dd>${esc(actKindName[x.kind])}</dd>${x.description ? `<dt>Description</dt><dd>${esc(x.description)}</dd>` : ''}${x.kind === 'tool' ? `<dt>Listed by its server</dt><dd>${x.declared ? '<span class="green">yes</span>' : x.undeclared ? '<span class="red">no: the server it was matched to does not list it</span>' : '<span class="muted">unknown: no captured server list matches this server</span>'}</dd>` : ''}
      ${(x.methods || []).length ? `<dt>Methods</dt><dd class="mono">${esc(x.methods.join(', '))}</dd>` : ''}<dt>Routes</dt><dd class="mono">${esc((x.routes || []).join(', ') || '—')}</dd><dt>Gateways</dt><dd class="mono">${esc((x.gateways || []).join(', '))}</dd>
      <dt>Calls</dt><dd>${num(x.requests)} · ${num(x.denied)} refused · ${num(x.errors)} failed${x.inputTokens ? ` · ${num(x.inputTokens)} tokens in, ${num(x.outputTokens)} out` : ''}</dd></dl></div>${edgeRows(es)}</section>`;
  }
  const e = (d.edges || []).find(y => y.from + '→' + y.to === A.sel);
  if (!e) return '';
  const tg = targets.get(e.to), cl = callers.get(e.from);
  const kv = o => Object.entries(o || {}).sort((a, b) => b[1] - a[1]).map(([k, v]) => `<span class="tag">${esc(k)} × ${num(v)}</span>`).join(' ') || '<span class="muted">none</span>';
  return `<section class="panel mt" id="act-detail"><div class="panel-head"><h2>${esc(cl?.label)} → ${esc(actTargetLabel(tg))}</h2><div class="row"><button id="act-logs">Log lines</button>${close}</div></div><div class="panel-body">
    <dl class="metadata"><dt>Calls</dt><dd>${num(e.requests)} · ${num(e.denied)} refused · ${num(e.errors)} failed</dd><dt>HTTP status</dt><dd>${kv(e.statuses)}</dd><dt>Reasons</dt><dd>${kv(e.reasons)}</dd>
      ${e.inputTokens || e.outputTokens ? `<dt>Tokens</dt><dd>${num(e.inputTokens)} in, ${num(e.outputTokens)} out${e.cost ? ` · cost ${e.cost.toFixed(4)}` : ''}</dd>` : ''}
      <dt>Gateways</dt><dd class="mono">${esc((e.gateways || []).join(', '))}</dd><dt>Routes</dt><dd class="mono">${esc((e.routes || []).join(', ') || '—')}</dd>${(e.users || []).length ? `<dt>JWT subjects</dt><dd class="mono">${esc(e.users.join(', '))}</dd>` : ''}
      <dt>Seen</dt><dd>${fbTime(e.first)} to ${fbTime(e.last)}</dd>${e.error ? `<dt>Logged error</dt><dd class="mono rc-small">${esc(e.error)}</dd>` : ''}
      ${e.outsideAllowlist ? '<dt>Tool list</dt><dd class="red">This tool is not in the kagent Agent\'s toolNames. The list only filters what the model is offered; enforce it on the gateway with an MCP authorization rule.</dd>' : ''}</dl></div></section>`;
}

function actCalls(edges, callers, targets) {
  if (!edges.length) return '<section class="panel"><div class="empty"><strong>Nothing matches</strong></div></section>';
  return `<section class="panel"><div class="table-scroll" style="max-height:66vh"><table><thead><tr><th>Caller</th><th>Target</th><th>Kind</th><th>Calls</th><th>Refused</th><th>Failed</th><th>Tokens</th><th>Gateway</th><th>Last seen</th><th></th></tr></thead><tbody>
    ${edges.map(e => { const c = callers.get(e.from), x = targets.get(e.to); return `<tr><td>${esc(c.label)}${c.anonymous ? ' <span class="rc-badge amber">no identity</span>' : ''}</td><td class="mono">${esc(actTargetLabel(x))}</td><td>${esc(actKindName[x.kind])}</td>
      <td class="mono">${num(e.requests)}</td><td class="mono ${e.denied ? 'amber' : ''}">${num(e.denied)}</td><td class="mono">${num(e.errors)}</td><td class="mono">${fbTok((e.inputTokens || 0) + (e.outputTokens || 0))}</td>
      <td class="mono rc-small">${esc((e.gateways || []).join(', '))}</td><td class="rc-small">${fbTime(e.last)}</td><td>${e.outsideAllowlist ? '<span class="rc-badge red">not in tool list</span>' : x.undeclared ? '<span class="rc-badge red">not listed by server</span>' : ''}</td></tr>`; }).join('')}
    </tbody></table></div></section>`;
}

function actTools(d, callers) {
  const A = S.act;
  const called = (d.targets || []).filter(x => x.kind === 'tool');
  const calledNames = new Set(called.map(x => x.label));
  const idle = (d.tools || []).filter(t => t.declared && !t.calls && !calledNames.has(t.name));
  const listed = x => x.declared ? '<span class="green">yes</span>' : x.undeclared ? '<span class="red">no</span>' : '<span class="muted" title="No captured server list matches this server">unknown</span>';
  const callerOf = x => [...new Set((d.edges || []).filter(e => e.to === x.id).map(e => callers.get(e.from)?.label))].join(', ');
  return `<section class="panel"><div class="panel-head"><h2>Tools called</h2><small class="muted">Checked against the tool list each kagent MCP server reports (status.discoveredTools), where the call can be matched to that server</small></div>
    <div class="table-scroll" style="max-height:52vh">${called.length ? `<table><thead><tr><th>Tool</th><th>Server</th><th>Calls</th><th>Refused</th><th>Failed</th><th>Listed</th><th>Callers</th><th>Description</th></tr></thead><tbody>
    ${called.map(x => `<tr><td class="mono">${esc(x.label)}</td><td class="mono">${esc(x.server)}</td><td class="mono">${num(x.requests)}</td><td class="mono ${x.denied ? 'amber' : ''}">${num(x.denied)}</td><td class="mono">${num(x.errors)}</td><td>${listed(x)}</td><td class="rc-small">${esc(callerOf(x))}</td><td class="rc-small muted">${esc(x.description || '')}</td></tr>`).join('')}
    </tbody></table>` : '<div class="empty"><strong>No tool calls</strong></div>'}</div></section>
    <section class="panel mt"><div class="panel-head"><h2>Listed but never called</h2><div class="row"><small class="muted">${fbPlural(idle.length, 'tool')} across the captured MCP servers</small><button class="ghost" id="act-idle">${A.showDeclared ? 'Hide' : 'Show'}</button></div></div>
    ${A.showDeclared ? `<div class="table-scroll" style="max-height:40vh"><table><thead><tr><th>Server</th><th>Tool</th><th>Description</th></tr></thead><tbody>${idle.map(t => `<tr><td class="mono">${esc(t.server)}</td><td class="mono">${esc(t.name)}</td><td class="rc-small muted">${esc(t.description || '')}</td></tr>`).join('')}</tbody></table></div>` : ''}</section>`;
}

function actModels(d) {
  const models = (d.targets || []).filter(x => x.kind === 'model');
  const spenders = (d.callers || []).filter(c => c.tokens > 0).sort((a, b) => b.tokens - a.tokens);
  if (!models.length) return '<section class="panel"><div class="empty"><strong>No LLM calls</strong>No agentgateway request log in the capture names a model or provider.</div></section>';
  return `<div class="grid2 rc-grid"><section class="panel"><div class="panel-head"><h2>Models</h2></div><div class="table-scroll"><table><thead><tr><th>Model</th><th>Provider</th><th>Calls</th><th>Tokens in</th><th>Tokens out</th><th>Cost</th></tr></thead><tbody>
    ${models.map(x => `<tr><td class="mono">${esc(x.label)}</td><td>${esc(x.provider || '—')}</td><td class="mono">${num(x.requests)}</td><td class="mono">${num(x.inputTokens)}</td><td class="mono">${num(x.outputTokens)}</td><td class="mono">${x.cost ? x.cost.toFixed(4) : '—'}</td></tr>`).join('')}</tbody></table></div></section>
    <section class="panel"><div class="panel-head"><h2>Spend by caller</h2></div><div class="table-scroll"><table><thead><tr><th>Caller</th><th>Tokens</th><th>Calls</th><th></th></tr></thead><tbody>
    ${spenders.map(c => `<tr><td>${esc(c.label)}</td><td class="mono">${num(c.tokens)}</td><td class="mono">${num(c.requests)}</td><td>${c.anonymous ? '<span class="rc-badge amber">no identity</span>' : ''}</td></tr>`).join('') || '<tr><td colspan="4" class="muted">No token counts were logged</td></tr>'}</tbody></table></div></section></div>`;
}

function actDenials(d) {
  const ds = d.denials || [];
  if (!ds.length) return '<section class="panel"><div class="empty"><strong>No refused calls</strong>No agentgateway request in the capture was refused by policy.</div></section>';
  return ds.map((x, i) => `<section class="panel ${i ? 'mt' : ''}"><div class="panel-head"><h2>${num(x.count)} × ${esc(x.caller)} → ${esc(x.target)}</h2><span class="rc-badge amber">${esc(x.reason)}</span></div><div class="panel-body">
    <p>${esc(x.meaning || '')}</p>
    <dl class="metadata"><dt>Gateway</dt><dd class="mono">${esc(x.gateway)}</dd>${x.route ? `<dt>Route</dt><dd class="mono">${esc(x.route)}</dd>` : ''}${x.status ? `<dt>Status</dt><dd class="mono">${esc(x.status)}</dd>` : ''}<dt>Last</dt><dd>${fbTime(x.last)}</dd>${x.error ? `<dt>Logged error</dt><dd class="mono rc-small">${esc(x.error)}</dd>` : ''}</dl>
    <h3 class="mt">Policies at this gateway</h3>${(x.policies || []).length ? `<div class="table-scroll rc-items"><table><thead><tr><th></th><th>Policy</th><th>Attached to</th><th>Sets</th></tr></thead><tbody>
      ${x.policies.map((p, j) => `<tr><td>${p.matches ? '<span class="rc-badge amber" title="It has a section that produces this refusal">likely</span>' : ''}</td><td><button class="link mono" data-actpol="${i}:${j}">${esc(p.ref.kind)} ${esc(p.ref.namespace)}/${esc(p.ref.name)}</button></td><td class="mono rc-small">${esc(p.target)}</td><td class="mono rc-small">${esc((p.sections || []).join(', '))}</td></tr>`).join('')}</tbody></table></div>`
      : '<p class="muted">No agentgateway policy in the capture is attached to this route, gateway or Service. The refusal may come from a policy on the listener or the GatewayClass, or one the customer excluded.</p>'}
    ${x.look ? `<p class="mt"><strong>Next:</strong> ${esc(x.look)}</p>` : ''}</div></section>`).join('');
}

function actChanges(d) {
  const c = d.changes;
  if (!c) return `<section class="panel"><div class="empty"><strong>No baseline loaded</strong>Load an earlier capture of the same cluster on the Compare page, or open this one with <code>--baseline</code>. This tab then lists tools that appeared, went away or changed their description.<br><a href="#compare">Go to Compare →</a></div></section>`;
  const list = (title, rows, cols) => `<section class="panel mt"><div class="panel-head"><h2>${esc(title)}</h2><small class="muted">${rows.length}</small></div>${rows.length ? `<div class="table-scroll" style="max-height:40vh"><table><thead><tr>${cols.map(x => `<th>${x}</th>`).join('')}</tr></thead><tbody>${rows.join('')}</tbody></table></div>` : '<div class="empty">None</div>'}</section>`;
  return `<p class="muted">Compared with <span class="mono">${esc(c.baseline)}</span>.${c.baselineNote ? ' ' + esc(c.baselineNote) + '.' : ''}</p>
    ${list('Description changed', (c.described || []).map(x => `<tr><td class="mono">${esc(x.server)}/${esc(x.name)}</td><td class="rc-small">${esc(x.before)}</td><td class="rc-small red">${esc(x.after)}</td></tr>`), ['Tool', 'Before', 'Now'])}
    ${list('Added', (c.added || []).map(x => `<tr><td class="mono">${esc(x.server)}/${esc(x.name)}</td><td class="rc-small muted">${esc(x.description || '')}</td></tr>`), ['Tool', 'Description'])}
    ${list('Removed', (c.removed || []).map(x => `<tr><td class="mono">${esc(x.server)}/${esc(x.name)}</td><td class="rc-small muted">${esc(x.description || '')}</td></tr>`), ['Tool', 'Description'])}
    ${list('Called now, not called in the baseline', (c.newlyCalled || []).map(x => `<tr><td class="mono">${esc(x)}</td></tr>`), ['Tool'])}
    ${list('Caller and target pairs not in the baseline', (c.newCallers || []).map(x => `<tr><td class="mono">${esc(x)}</td></tr>`), ['Caller → target'])}`;
}

after.activity = () => {
  const A = S.act, d = A.data;
  $('#act-term')?.addEventListener('click', () => termRun('fbactivity'));
  if (!feature('activity') || !d) return;
  const keepY = fn => { const y = scrollY; fn(); render().then(() => scrollTo(0, y)); };
  fbFindingsWire(A, d.findings || [], 'act');
  document.querySelectorAll('[data-acttab]').forEach(b => b.onclick = () => { A.tab = b.dataset.acttab; render(); });
  document.querySelectorAll('[data-actkind]').forEach(b => b.onclick = () => keepY(() => { A.kinds[b.dataset.actkind] = !A.kinds[b.dataset.actkind]; A.gz = null; }));
  const q = $('#act-q');
  if (q) { let t; q.oninput = e => { clearTimeout(t); t = setTimeout(() => { const c = e.target.selectionStart, v = e.target.value; A.q = v; A.gz = null; render().then(() => refocus('#act-q', c, v)); }, 250); }; }
  const pick = id => { A.sel = A.sel === id ? '' : id; const y = scrollY; render().then(() => { scrollTo(0, y); if (A.sel) $('#act-detail')?.scrollIntoView({block: 'nearest', behavior: 'smooth'}); }); };
  fbGraphWire(A, 'act|' + Object.entries(A.kinds).filter(([, v]) => v).map(([k]) => k).join(',') + '|' + A.q + '|' + A.key + (A.gfull ? '|full' : ''), pick);
  $('#act-selx')?.addEventListener('click', () => keepY(() => { A.sel = ''; }));
  document.querySelectorAll('[data-actsel]').forEach(tr => tr.onclick = () => pick(tr.dataset.actsel));
  const c = (d.callers || []).find(x => x.id === A.sel);
  $('#act-obj')?.addEventListener('click', () => c?.ref && fbOpenRef(c.ref));
  $('#act-logs')?.addEventListener('click', () => {
    const e = (d.edges || []).find(y => y.from + '→' + y.to === A.sel), x = (d.targets || []).find(y => y.id === e?.to);
    if (!e || !x) return;
    const c = (d.callers || []).find(y => y.id === e.from);
    const q = v => /[\s"]/.test(v) ? `"${v.replace(/"/g, '')}"` : v;
    const words = ['scope:request'];
    if (x.kind === 'tool') words.push('gen_ai.tool.name:' + q(x.label));
    else if (x.kind === 'model' && x.label !== '(model not logged)') words.push('gen_ai.response.model:' + q(x.label));
    if ((e.routes || []).length === 1) words.push('route:' + q(e.routes[0]));
    if (c?.identity) words.push('src.identity:' + q(c.identity));
    else if ((c?.users || []).length === 1) words.push('jwt.sub:' + q(c.users[0]));
    openLogs({q: words.join(' ')});
  });
  $('#act-idle')?.addEventListener('click', () => keepY(() => { A.showDeclared = !A.showDeclared; }));
  document.querySelectorAll('[data-actpol]').forEach(b => b.onclick = () => { const [i, j] = b.dataset.actpol.split(':').map(Number); fbOpenRef(d.denials[i].policies[j].ref); });
};

// ---------- Traffic map ----------
const tfIntro = 'Every connection ztunnel logged, whether it went over mutual TLS, which waypoint carried it, and what the captured policy says about it.';
const tfFlags = {plaintext: ['Plaintext in the mesh', 'red'], 'plaintext-in': ['Plaintext from outside the mesh', 'amber'], unpoliced: ['No policy', 'amber'], denied: ['Refused', 'amber'], mismatch: ['Logs and policy disagree', 'amber']};
const tfVerdictCls = {allowed: '', denied: 'green', unknown: 'amber'};

function tfFromHash(p) {
  if (p.get('from') && p.get('to')) { S.tf.exFrom = p.get('from'); S.tf.exTo = p.get('to'); S.tf.tab = 'explain'; S.tf.ex = null; }
}

async function trafficPage() {
  const T = S.tf;
  if (!feature('traffic')) return fbEntOff('Traffic map', 'It shows every connection ztunnel logged with its mTLS state, waypoint and policy verdict, and explains why any connection was allowed or denied.', 'traffic');
  // #traffic?from=..&to=.. opens the explainer (app.js may parse the hash
  // before this file has loaded, so read it here as well).
  if (T.lastHash !== location.hash) { T.lastHash = location.hash; tfFromHash(new URLSearchParams(location.hash.split('?')[1] || '')); }
  if (!T.data || T.key !== fbKey()) { T.data = await api('api/traffic'); T.key = fbKey(); }
  const d = T.data, t = d.totals || {};
  const nodes = new Map((d.nodes || []).map(n => [n.id, n]));
  const lab = id => nodes.get(id)?.label || id;
  const terms = qTerms(T.q);
  const edges = (d.edges || []).filter(e => (!T.flag || (e.flags || []).includes(T.flag)) && (!T.ns || nodes.get(e.from)?.namespace === T.ns || nodes.get(e.to)?.namespace === T.ns) &&
    terms.every(tm => { const ok = [lab(e.from), lab(e.to), e.via, e.why, e.verdict, e.tls].join(' ').toLowerCase().includes((tm.text ?? `${tm.key}:${tm.val}`).toLowerCase()); return tm.neg ? !ok : ok; }));
  const stat = (label, value, sub, flag, cls = '') => `<div class="stat ${flag !== undefined ? 'fb-statbtn' : ''} ${flag && T.flag === flag ? 'on' : ''}" ${flag !== undefined ? `data-tfflag="${flag}" title="Show only these"` : ''}><div class="eyebrow">${label}</div><div class="value ${cls}">${value}</div><p>${sub}</p></div>`;
  const stats = `<div class="stats fb-stats">${stat('Edges', num(t.edges), `${num(t.connections)} connections logged`, '')}
    ${stat('Plaintext in the mesh', num(t.plaintext), 'between two enrolled workloads', 'plaintext', t.plaintext ? 'red' : '')}
    ${stat('No policy', num(t.unpoliced), 'no NetworkPolicy or AuthorizationPolicy', 'unpoliced', t.unpoliced ? 'amber' : '')}
    ${stat('Refused', num(t.denied), 'edges with refused connections', 'denied', t.denied ? 'amber' : '')}
    ${stat('Disagree', num(t.mismatch), 'logs and captured policy differ', 'mismatch', t.mismatch ? 'amber' : '')}
    ${stat('Plaintext in', num((d.edges || []).filter(e => (e.flags || []).includes('plaintext-in')).length), 'from workloads outside the mesh', 'plaintext-in')}</div>`;
  const notes = (d.notes || []).map(n => `<div class="notice" style="margin-bottom:12px"><span>△</span><div>${esc(rcCap(n))}.</div></div>`).join('');
  const findings = `<section class="panel"><div class="panel-head"><h2>Findings</h2><small class="muted">${fbPlural((d.findings || []).length, 'finding')}</small></div>${fbFindings(d.findings, T, 'tf')}</section>`;
  const tab = (id, label, n) => `<button class="pst-tab ${T.tab === id ? 'on' : ''}" data-tftab="${id}">${esc(label)}${n !== undefined ? `<span class="pst-tab-n">${num(n)}</span>` : ''}</button>`;
  const tabs = `<nav class="pst-tabs">${tab('map', 'Map')}${tab('edges', 'Edges', edges.length)}${tab('explain', 'Why? Explain a connection')}</nav>`;
  const nsOpts = `<select id="tf-ns"><option value="">All namespaces</option>${(d.namespaces || []).map(n => `<option ${T.ns === n ? 'selected' : ''}>${esc(n)}</option>`).join('')}</select>`;
  const fchips = `<button class="chip ${!T.flag ? 'on' : ''}" data-tfflag="">All</button>` + Object.entries(tfFlags).map(([k, [l]]) => { const n = (d.edges || []).filter(e => (e.flags || []).includes(k)).length; return n ? `<button class="chip ${T.flag === k ? 'on' : ''}" data-tfflag="${k}">${esc(l)} <small>${n}</small></button>` : ''; }).join('');
  const toolbar = `<div class="toolbar"><div class="row wrap">${fchips}</div><div class="row">${nsOpts}<input id="tf-q" placeholder="Filter workloads, Services, waypoints" value="${esc(T.q)}" style="min-width:240px" autocomplete="off" spellcheck="false"></div></div>`;
  let body;
  if (!d.captured) body = '<section class="panel"><div class="empty"><strong>No ztunnel access logs in this capture</strong>The traffic map is read from ztunnel access logs. Collect the istio-system ztunnel pods\' logs to fill it. You can still explain any pair on the Why? tab from the captured policy.</div></section>';
  else if (T.tab === 'map') body = toolbar + tfMap(d, edges, nodes) + tfEdgePanel(d, nodes);
  else if (T.tab === 'edges') body = toolbar + tfEdges(edges, lab) + tfEdgePanel(d, nodes);
  if (T.tab === 'explain') body = tfExplainTab(d);
  return head('Traffic map', `${esc(tfIntro)} <span class="ent-pill">Enterprise</span>`, '<button id="tf-term">&gt;_ fbtraffic in terminal</button>') + notes + stats + findings + tabs + body;
}

function tfMap(d, edges, nodes) {
  const T = S.tf;
  if (!edges.length) return '<section class="panel mt"><div class="empty"><strong>Nothing matches</strong>Change the flag, namespace or filter.</div></section>';
  const ncls = n => !n ? 'grey' : n.kind === 'external' ? 'grey' : n.inMesh ? 'green' : 'amber';
  const icon = n => n?.kind === 'waypoint' ? 'router' : n?.kind === 'service' ? 'network' : n?.kind === 'external' ? 'cloud' : n?.agentType ? 'bot' : 'server';
  const sub = n => !n ? '' : [n.kind === 'service' ? 'Service' : n.kind, n.kind === 'external' ? 'outside the capture' : n.inMesh ? 'in the mesh' : 'not in the mesh', n.waypoint ? 'waypoint ' + n.waypoint : '', n.agentType ? rcTypes[n.agentType] || n.agentType : ''].filter(Boolean).join(' · ');
  const mk = id => { const n = nodes.get(id); return {id, label: n?.label || id, sub: sub(n), risk: ncls(n), icon: icon(n), tip: [n?.label, sub(n)].filter(Boolean).join('\n')}; };
  const left = [...new Set(edges.map(e => e.from))].map(mk), right = [...new Set(edges.map(e => e.to))].map(mk);
  const maxC = Math.max(1, ...edges.map(e => e.connections));
  const ge = edges.map(e => ({id: e.id, from: e.from, to: e.to, risk: e.risk, dash: e.denied > 0 || e.verdict === 'denied', w: 1.2 + 3 * Math.log(1 + e.connections) / Math.log(1 + maxC),
    tip: `${nodes.get(e.from)?.label} → ${nodes.get(e.to)?.label}\n${num(e.connections)} connections, ${e.tls}${e.denied ? `, ${num(e.denied)} refused` : ''}${e.via ? `\nvia ${e.via}` : ''}\npolicy: ${e.verdict}${e.layer ? ' (' + e.layer + ')' : ''}\n${e.why}\nClick for the checks`}));
  const svg = fbGraph({left, right, edges: ge, sel: T.sel, heads: [['server', 'From', `${fbPlural(left.length, 'workload')}`], ['network', 'To', `${fbPlural(right.length, 'destination')}`]]});
  const legend = '<span><i class="rc-sw red"></i>plaintext between mesh workloads</span><span><i class="rc-sw amber"></i>no policy, plaintext from outside the mesh, or logs and policy disagree</span><span><i class="rc-sw green"></i>mutual TLS and policy looks at it</span><span><i class="rc-sw dash"></i>refused, or policy denies it</span><span class="muted">Box colour: green in the mesh, amber not in the mesh, grey outside the capture</span>';
  return fbGraphPanel('Connections', T, svg, `<small class="muted">${fbPlural(edges.length, 'edge')}</small>`, legend);
}

function tfEdges(edges, lab) {
  if (!edges.length) return '<section class="panel"><div class="empty"><strong>Nothing matches</strong></div></section>';
  return `<section class="panel"><div class="table-scroll" style="max-height:62vh"><table><thead><tr><th>Risk</th><th>From</th><th>To</th><th>Connections</th><th>Refused</th><th>TLS</th><th>Policy</th><th>Via</th><th>Why</th><th></th></tr></thead><tbody>
    ${edges.map(e => `<tr class="fb-row ${S.tf.sel === e.id ? 'sel' : ''}" data-tfsel="${esc(e.id)}"><td>${rcRisk(e.risk)}</td><td class="mono">${esc(lab(e.from))}</td><td class="mono">${esc(lab(e.to))}</td><td class="mono">${num(e.connections)}</td><td class="mono ${e.denied ? 'amber' : ''}">${num(e.denied)}</td>
      <td class="${e.tls === 'mtls' ? 'green' : e.tls === 'plaintext' ? 'amber' : ''}">${esc(e.tls === 'mtls' ? 'mTLS' : e.tls)}</td><td class="${tfVerdictCls[e.verdict] || ''}">${esc(e.verdict)}${e.layer ? ` <small class="muted">${esc(e.layer)}</small>` : ''}</td>
      <td class="mono rc-small">${esc(e.via || '')}</td><td class="rc-wrap rc-small">${esc(e.why)}</td><td><button class="ghost" data-tfwhy="${esc(e.id)}">Why?</button></td></tr>`).join('')}
    </tbody></table></div></section>`;
}

// The panel for the selected edge: what was seen, and the explanation.
function tfEdgePanel(d, nodes) {
  const T = S.tf;
  if (!T.sel) return '';
  const n = nodes.get(T.sel);
  if (n) {
    const es = (d.edges || []).filter(e => e.from === n.id || e.to === n.id);
    return `<section class="panel mt" id="tf-detail"><div class="panel-head"><h2>${esc(n.label)}</h2><div class="row">${n.ref ? '<button id="tf-obj">Open object</button>' : ''}<button class="ghost" id="tf-selx" aria-label="Close">✕</button></div></div>
      <div class="panel-body"><p class="muted">${esc([n.kind, n.inMesh ? 'in the mesh' : 'not in the mesh', n.waypoint ? 'waypoint ' + n.waypoint : ''].filter(Boolean).join(' · '))}. ${fbPlural(es.length, 'edge')}; click one for its checks.</p></div>
      ${tfEdges(es, id => nodes.get(id)?.label || id)}</section>`;
  }
  const e = (d.edges || []).find(x => x.id === T.sel);
  if (!e) return '';
  const from = nodes.get(e.from), to = nodes.get(e.to);
  const canExplain = from?.kind === 'workload' || from?.kind === 'waypoint';
  return `<section class="panel mt" id="tf-detail"><div class="panel-head"><h2>${esc(from?.label)} → ${esc(to?.label)}</h2><div class="row">${to?.ref ? '<button id="tf-obj">Open destination</button>' : ''}<button class="ghost" id="tf-selx" aria-label="Close">✕</button></div></div><div class="panel-body">
    <dl class="metadata"><dt>Seen</dt><dd>${num(e.connections)} connections${e.requests ? `, ${num(e.requests)} HTTP requests` : ''}${e.denied ? `, <span class="amber">${num(e.denied)} refused</span>` : ''} · ${fbTime(e.first)} to ${fbTime(e.last)}</dd>
      <dt>Transport</dt><dd>${e.tls === 'mtls' ? '<span class="green">mutual TLS (HBONE)</span>' : e.tls === 'mixed' ? `<span class="amber">mixed: ${num(e.hbone)} HBONE, ${num(e.plaintext)} plaintext</span>` : '<span class="amber">plaintext</span>'}${e.srcIdentity ? `<br><small class="mono muted">${esc(e.srcIdentity)} → ${esc(e.dstIdentity || '?')}</small>` : ''}</dd>
      ${e.via ? `<dt>Waypoint</dt><dd class="mono">${esc(e.via)}</dd>` : ''}${(e.methods || []).length ? `<dt>Methods</dt><dd class="mono">${esc(e.methods.join(', '))}${(e.paths || []).length ? ` <span class="muted">${esc(e.paths.join(', '))}</span>` : ''}</dd>` : ''}
      ${e.bytesSent || e.bytesRecv ? `<dt>Bytes</dt><dd class="mono">${num(e.bytesSent)} sent, ${num(e.bytesRecv)} received</dd>` : ''}
      <dt>Policy</dt><dd class="${tfVerdictCls[e.verdict] || ''}">${esc(e.verdict)}${e.layer ? ` (${esc(e.layer)})` : ''}</dd><dt>Flags</dt><dd>${(e.flags || []).map(f => `<span class="rc-badge ${(tfFlags[f] || [])[1] || ''}">${esc((tfFlags[f] || [f])[0])}</span>`).join(' ') || '<span class="muted">none</span>'}</dd></dl>
    ${canExplain && to?.kind !== 'external' ? `<div id="tf-why" class="mt">${tfExplainBody(T.ex && T.ex.key === e.id ? T.ex : null)}</div>` : `<p class="muted mt">${to?.kind === 'external' ? 'The destination is outside the capture, so only NetworkPolicy egress could stop it.' : 'The source is not a workload in the capture, so there is nothing to explain from.'}</p>`}</div></section>`;
}

function tfExplainBody(ex) {
  if (!ex) return '<p class="muted">Working out each check…</p>';
  if (ex.error) return `<div class="notice bad"><div>${esc(ex.error)}${(ex.suggestions || []).length ? `<br>Did you mean: ${ex.suggestions.map(s => `<button class="link mono" data-tfsug="${esc(s)}">${esc(s)}</button>`).join(', ')}` : ''}</div></div>`;
  const x = ex.res;
  const vcls = {allowed: 'green', denied: 'red', unknown: 'amber', none: ''};
  const steps = (x.steps || []).map((s, i, all) => {
    const newBackend = s.backend && (i === 0 || all[i - 1].backend !== s.backend) && new Set(all.map(y => y.backend)).size > 1;
    return `${newBackend ? `<div class="tf-backend">to pod of ${esc(s.backend)}</div>` : ''}<div class="tf-step tf-${esc(s.verdict)}"><div class="tf-dot">${i + 1}</div><div class="tf-sbody"><div class="row between"><strong>${esc(s.point)}</strong><span class="rc-badge ${vcls[s.verdict] || ''}">${esc(s.verdict === 'none' ? 'nothing applies' : s.verdict)}</span></div>
      ${s.detail ? `<p>${esc(s.detail)}</p>` : ''}${(s.policies || []).length ? `<div class="row wrap">${s.policies.map((p, j) => `<button class="link mono rc-small" data-tfpol="${i}:${j}">${esc(p.kind)} ${esc(p.namespace)}/${esc(p.name)}</button>`).join('')}</div>` : ''}</div></div>`;
  }).join('');
  const o = x.observed;
  return `<div class="tf-verdict tf-v-${esc(x.verdict)}"><strong>${esc(x.summary)}</strong></div>
    <h3 class="mt">Checks, in the order traffic meets them</h3><div class="tf-steps">${steps || '<p class="muted">No checks: see the summary.</p>'}</div>
    ${(x.conditions || []).length ? `<h3 class="mt">Not evaluated from a snapshot</h3><ul class="rc-advice">${x.conditions.map(c => `<li>${esc(c)}</li>`).join('')}</ul>` : ''}
    ${o ? `<h3 class="mt">In the access logs</h3><p>${num(o.connections)} connections, ${num(o.denied)} refused, ${num(o.hbone)} over HBONE, ${num(o.plaintext)} plaintext.${o.note ? ` <span class="${o.agrees ? 'muted' : 'amber'}">${esc(rcCap(o.note))}.</span>` : ''}</p>` : ''}
    ${(x.advice || []).map(a => `<div class="notice info mt"><div>${esc(a)}</div></div>`).join('')}
    ${x.fix ? `<div class="row between mt"><h3>To allow it</h3><button id="tf-fixcopy">Copy YAML</button></div><p class="muted rc-small">${esc(x.fixNote || '')}</p><pre class="code">${esc(x.fix)}</pre>` : ''}
    <p class="mt row"><button class="ghost" id="tf-exterm">&gt;_ fbexplain in terminal</button></p>`;
}

function tfExplainTab(d) {
  const T = S.tf;
  const opts = kind => (d.nodes || []).filter(n => kind === 'from' ? (n.kind === 'workload' || n.kind === 'waypoint') : n.kind !== 'external').map(n => `<option value="${esc(n.label)}">`).join('');
  return `<section class="panel"><div class="panel-head"><h2>Why was this allowed or denied?</h2><small class="muted">Runs the Agent reach evaluator for one pair: NetworkPolicy, mTLS, ztunnel and waypoint authorization, in order</small></div><div class="panel-body">
    <form id="tf-exform" class="row wrap" style="gap:10px"><label class="tf-exl">From workload <input id="tf-exfrom" list="tf-fromlist" value="${esc(T.exFrom)}" placeholder="namespace/workload, pod or spiffe:// ID" autocomplete="off" spellcheck="false" style="min-width:300px"></label>
      <label class="tf-exl">To Service <input id="tf-exto" list="tf-tolist" value="${esc(T.exTo)}" placeholder="namespace/service or host name" autocomplete="off" spellcheck="false" style="min-width:300px"></label>
      <button type="submit" class="primary">Explain</button></form><datalist id="tf-fromlist">${opts('from')}</datalist><datalist id="tf-tolist">${opts('to')}</datalist>
    <p class="muted rc-small mt">Any workload and Service in the capture, not only pairs seen in the logs. The suggestions are the ones on the map.</p>
    ${T.ex && T.ex.key === 'form' ? `<div class="mt" id="tf-why">${tfExplainBody(T.ex)}</div>` : ''}</div></section>`;
}

async function tfRunExplain(key, from, to, cluster) {
  const T = S.tf;
  const q = {from, to};
  if (cluster) q.cluster = cluster;
  try {
    const res = await api('api/explain', q);
    T.ex = {key, res, from, to};
  } catch (e) {
    T.ex = {key, error: e.message, suggestions: e.body?.suggestions || [], from, to};
  }
}

after.traffic = () => {
  const T = S.tf, d = T.data;
  $('#tf-term')?.addEventListener('click', () => termRun('fbtraffic'));
  if (!feature('traffic') || !d) return;
  const keepY = fn => { const y = scrollY; fn(); render().then(() => scrollTo(0, y)); };
  const nodes = new Map((d.nodes || []).map(n => [n.id, n]));
  fbFindingsWire(T, d.findings || [], 'tf');
  document.querySelectorAll('[data-tftab]').forEach(b => b.onclick = () => { T.tab = b.dataset.tftab; render(); });
  document.querySelectorAll('[data-tfflag]').forEach(b => b.onclick = () => keepY(() => { T.flag = T.flag === b.dataset.tfflag ? '' : b.dataset.tfflag; T.gz = null; if (T.tab === 'explain') T.tab = 'map'; }));
  const ns = $('#tf-ns'); if (ns) ns.onchange = () => keepY(() => { T.ns = ns.value; T.gz = null; });
  const q = $('#tf-q');
  if (q) { let t; q.oninput = e => { clearTimeout(t); t = setTimeout(() => { const c = e.target.selectionStart, v = e.target.value; T.q = v; T.gz = null; render().then(() => refocus('#tf-q', c, v)); }, 250); }; }
  // Selecting an edge fetches its explanation.
  const select = id => {
    T.sel = T.sel === id ? '' : id;
    const e = (d.edges || []).find(x => x.id === T.sel);
    const y = scrollY;
    render().then(async () => {
      scrollTo(0, y);
      $('#tf-detail')?.scrollIntoView({block: 'nearest', behavior: 'smooth'});
      if (!e) return;
      const from = nodes.get(e.from), to = nodes.get(e.to);
      if (!(from?.kind === 'workload' || from?.kind === 'waypoint') || to?.kind === 'external') return;
      if (T.ex?.key !== e.id) { await tfRunExplain(e.id, from.label, to.kind === 'service' ? to.label : to.label, e.cluster); const w = $('#tf-why'); if (w && T.sel === e.id) { w.innerHTML = tfExplainBody(T.ex); tfWireExplain(); } }
    });
  };
  fbGraphWire(T, 'tf|' + T.flag + '|' + T.ns + '|' + T.q + '|' + T.key + (T.gfull ? '|full' : ''), select);
  document.querySelectorAll('[data-tfsel]').forEach(tr => tr.onclick = ev => { if (!ev.target.closest('[data-tfwhy]')) select(tr.dataset.tfsel); });
  document.querySelectorAll('[data-tfwhy]').forEach(b => b.onclick = () => select(b.dataset.tfwhy));
  $('#tf-selx')?.addEventListener('click', () => keepY(() => { T.sel = ''; }));
  const sn = nodes.get(T.sel), se = (d.edges || []).find(x => x.id === T.sel);
  $('#tf-obj')?.addEventListener('click', () => { const r = sn?.ref || nodes.get(se?.to)?.ref; if (r) fbOpenRef(r); });
  // An edge selected before this render (a live refresh, a tab switch) gets its explanation back.
  if (se && T.ex?.key !== se.id && $('#tf-why')) { const f = nodes.get(se.from), to = nodes.get(se.to); tfRunExplain(se.id, f.label, to.label, se.cluster).then(() => { const w = $('#tf-why'); if (w) { w.innerHTML = tfExplainBody(T.ex); tfWireExplain(); } }); }
  const form = $('#tf-exform');
  if (form) {
    form.onsubmit = async ev => {
      ev.preventDefault();
      T.exFrom = $('#tf-exfrom').value.trim(); T.exTo = $('#tf-exto').value.trim();
      if (!T.exFrom || !T.exTo) return;
      await tfRunExplain('form', T.exFrom, T.exTo);
      keepY(() => {});
    };
    if (T.exFrom && T.exTo && !T.ex) form.requestSubmit();
  }
  tfWireExplain();
};

function tfWireExplain() {
  const T = S.tf, x = T.ex?.res;
  document.querySelectorAll('[data-tfpol]').forEach(b => b.onclick = () => { const [i, j] = b.dataset.tfpol.split(':').map(Number); const p = x.steps[i].policies[j]; fbOpenRef(p); });
  $('#tf-fixcopy')?.addEventListener('click', () => navigator.clipboard.writeText(x.fix).then(() => toast('YAML copied'), () => toast('Clipboard unavailable')));
  $('#tf-exterm')?.addEventListener('click', () => termRun(`fbexplain ${T.ex.from} ${T.ex.to}`));
  document.querySelectorAll('[data-tfsug]').forEach(b => b.onclick = () => {
    const s = b.dataset.tfsug;
    if (T.ex.error.startsWith('no workload')) T.exFrom = s; else T.exTo = s;
    T.tab = 'explain'; T.ex = null; render();
  });
}
