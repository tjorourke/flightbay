'use strict';
// The Traffic page: ztunnel connections (the network layer) and calls
// through agentgateway (the gateway layer: tools, models, agents, tokens,
// refusals) on one map, with "why was this denied?". Also the Agents page's
// usage and lock-down tabs. Flightbay Enterprise (feature traffic). Loaded
// after app.js, which routes #traffic here and #activity to it.

S.act = {tab: 'map', kinds: {meta: true, tool: true, model: true, agent: true, mcp: true, http: false}, q: '', sel: '', data: null, key: '', gz: null, gfull: false, fopen: {}, showDeclared: false};
S.tf = {tab: 'map', flag: '', ns: '', q: '', sel: '', data: null, key: '', gz: null, gfull: false, fopen: {}, ex: null, exFrom: '', exTo: '', agents: false, focus: '', focusName: '', freach: null, fkey: '', fexp: {}};

const fbKey = () => `${S.state?.revision ?? ''}/${S.state?.logLines ?? ''}`;
const fbPlural = (n, one, many) => `${num(n)} ${n === 1 ? one : (many || one + 's')}`;
const fbEntOff = (title, what, flag) => head(title, '') + `<section class="panel"><div class="empty"><strong>${esc(title)} is a Flightbay Enterprise feature</strong>${esc(what)}<br>
  Turn it on by starting the desk with <code>FLIGHTBAY_ENTERPRISE=1 flightbay ui</code> (or <code>FLIGHTBAY_FEATURES=${esc(flag)}</code>).</div></section>`;
const fbTime = t => t && !String(t).startsWith('0001') ? fmtDateTime(t) : '—';
const fbTok = n => n >= 1e6 ? (n / 1e6).toFixed(1) + 'M' : n >= 1e4 ? Math.round(n / 1e3) + 'k' : num(n);
const fbOpenRef = r => openObject({cluster: r.cluster, group: r.group, resource: r.resource, namespace: r.namespace, name: r.name});

// ---------- findings (the same look as Agent reach) ----------
function fbFindings(list, st, key, limit) {
  if (!(list || []).length) return '<div class="empty"><strong>No findings</strong></div>';
  const more = limit && !st.fall && list.length > limit ? list.length - limit : 0;
  return list.slice(0, more ? limit : list.length).map((f, i) => {
    const open = !!st.fopen[i];
    const its = f.items || [];
    const hasCls = its.some(x => x.class), hasAcc = its.some(x => x.access), hasNote = its.some(x => x.note);
    const items = its.length ? `<div class="table-scroll rc-items"><table><thead><tr><th>Affected</th>${hasCls ? '<th>Reason</th>' : ''}${hasAcc ? '<th>Seen</th>' : ''}${hasNote ? '<th></th>' : ''}</tr></thead><tbody>
      ${its.map(x => `<tr><td class="mono">${esc(x.target)}</td>${hasCls ? `<td>${esc(x.class || '')}</td>` : ''}${hasAcc ? `<td class="mono">${esc(x.access || '')}</td>` : ''}${hasNote ? `<td class="muted rc-small">${esc(x.note || '')}</td>` : ''}</tr>`).join('')}</tbody></table></div>` : '';
    return `<div class="rc-finding ${open ? 'open' : ''}"><button class="rc-fhead" data-fbf="${key}:${i}">
      <span class="pst-sev pst-sev-${esc(f.severity)}">${esc(f.severity)}</span><strong>${esc(f.title)}</strong>
      ${its.length ? `<span class="tag">${f.unit ? fbPlural(its.length, f.unit) : its.length + ' affected'}</span>` : ''}<span style="flex:1"></span>${f.fix ? '<small class="muted">fix included</small>' : ''}<span class="muted">${open ? '▾' : '▸'}</span></button>
      ${open ? `<div class="rc-fbody"><p>${esc(f.detail)}</p>${items}${(f.advice || []).length ? `<h3 class="mt">What to do</h3><ul class="rc-advice">${f.advice.map(x => `<li>${esc(x)}</li>`).join('')}</ul>` : ''}
        ${f.fix ? `<div class="row between mt"><h3>How to fix</h3><div class="row"><button data-fbcopy="${key}:${i}">Copy YAML</button>${guideLinks(f.source, f.docs)}</div></div><pre class="code mt">${esc(f.fix)}</pre>`
          : f.source || f.docs ? `<p class="mt row">${guideLinks(f.source, f.docs)}</p>` : ''}</div>` : ''}</div>`;
  }).join('') + (more ? `<div style="padding:10px 16px"><button class="ghost" data-fball="${key}">Show all ${list.length}</button></div>` : limit && st.fall && list.length > limit ? `<div style="padding:10px 16px"><button class="ghost" data-fball="${key}">Show the ${limit} most serious</button></div>` : '');
}
function fbFindingsWire(st, list, key) {
  document.querySelectorAll(`[data-fball="${key}"]`).forEach(b => b.onclick = () => { st.fall = !st.fall; const y = scrollY; render().then(() => scrollTo(0, y)); });
  document.querySelectorAll(`[data-fbf^="${key}:"]`).forEach(b => b.onclick = () => { const i = +b.dataset.fbf.split(':')[1]; st.fopen[i] = !st.fopen[i]; const y = scrollY; render().then(() => scrollTo(0, y)); });
  document.querySelectorAll(`[data-fbcopy^="${key}:"]`).forEach(b => b.onclick = () => { const f = list[+b.dataset.fbcopy.split(':')[1]]; navigator.clipboard.writeText(f.fix).then(() => toast('YAML copied'), () => toast('Clipboard unavailable')); });
}

// ---------- a two-column graph: sources on the left, destinations on the right ----------
// left/right: [{id, label, sub, risk, icon, dim}]; edges: [{from, to, risk, dash, w, tip}].
// Rows on the right are ordered by the average row of what calls them, and the
// left by the average row of what they call, so lines cross as little as a
// single pass allows. Lines are curves in the gap between the two columns, so
// they never cross a box.
// far, when given, is a third column; its edges run from a right-hand box
// to a far box, and an edge's by lists the left-hand boxes it stands for,
// so hovering one of those traces through to the far column.
function fbGraph({left, right, far = [], edges, heads, sel}) {
  const bw = 250, bh = 42, vg = 10, gap = Math.max(220, Math.min(420, 90 + edges.length * 4)), gap2 = 200, HB = (heads || []).some(h => Array.isArray(h[2]) && h[2].length > 1) ? 64 : 52, top = HB + 16;
  const L = [...left], R = [...right], F = [...far];
  const li = new Map(L.map((n, i) => [n.id, i]));
  const bary = (n, map, key) => { const xs = edges.filter(e => e[key] === n.id).map(e => map.get(e[key === 'to' ? 'from' : 'to'])).filter(x => x !== undefined); return xs.length ? xs.reduce((a, b) => a + b, 0) / xs.length : 1e9; };
  R.sort((a, b) => (a.order ?? 1) - (b.order ?? 1) || bary(a, li, 'to') - bary(b, li, 'to') || a.label.localeCompare(b.label));
  const ri = new Map(R.map((n, i) => [n.id, i]));
  L.sort((a, b) => bary(a, ri, 'from') - bary(b, ri, 'from') || a.label.localeCompare(b.label));
  const lrow = new Map(L.map((n, i) => [n.id, i])), rrow = new Map(R.map((n, i) => [n.id, i]));
  const ri2 = new Map(R.map((n, i) => [n.id, i]));
  F.sort((a, b) => bary(a, ri2, 'to') - bary(b, ri2, 'to') || (a.order ?? 1) - (b.order ?? 1) || a.label.localeCompare(b.label));
  const frow = new Map(F.map((n, i) => [n.id, i]));
  const rows = Math.max(L.length, R.length, F.length, 1);
  const lOff = (rows - L.length) * (bh + vg) / 2, rOff = (rows - R.length) * (bh + vg) / 2, fOff = (rows - F.length) * (bh + vg) / 2;
  const X0 = 16, X1 = 16 + bw + gap, X2 = X1 + bw + gap2;
  const W = (F.length ? X2 : X1) + bw + 16, H = top + rows * (bh + vg) + 10;
  const yL = id => top + lOff + lrow.get(id) * (bh + vg), yR = id => top + rOff + rrow.get(id) * (bh + vg), yF = id => top + fOff + frow.get(id) * (bh + vg);
  const hfit = s => { const n = Math.floor((bw - 48) / 5.2); s = String(s || ''); return s.length > n ? s.slice(0, n - 1) + '…' : s; };
  const fit = (s, px) => { const n = Math.max(4, Math.floor((bw - 44) / px)); s = String(s || ''); return s.length > n ? s.slice(0, n - 1) + '…' : s; };
  const risk = r => ['red', 'amber', 'green', 'grey'].includes(r) ? r : 'grey';
  const rank = {grey: 0, green: 1, amber: 2, red: 3};
  const es = edges.filter(e => (lrow.has(e.from) && rrow.has(e.to)) || (rrow.has(e.from) && frow.has(e.to))).sort((a, b) => rank[risk(a.risk)] - rank[risk(b.risk)]);
  const edgeSvg = es.map(e => {
    const second = rrow.has(e.from), g = second ? gap2 : gap;
    const sx = (second ? X1 : X0) + bw, sy = (second ? yR(e.from) : yL(e.from)) + bh / 2, tx = (second ? X2 : X1) - 3, ty = (second ? yF(e.to) : yR(e.to)) + bh / 2;
    const d = `M${sx},${sy} C${sx + g / 2},${sy} ${tx - g / 2},${ty} ${tx},${ty}`;
    const on = sel && (sel === e.id || sel === e.from || sel === e.to || (e.by || []).includes(sel));
    return `<g class="rg-edge rg-${risk(e.risk)} ${e.dash ? 'rg-dash' : ''} ${e.cls || ''} ${on ? 'fb-on' : ''}" data-f="${esc(e.from)}" data-t="${esc(e.to)}" data-fbe="${esc(e.id)}"${e.by ? ` data-by="${esc(e.by.join(' '))}"` : ''}><title>${esc(e.tip || '')}</title><path class="rg-hit" d="${d}"/><path class="rg-line" d="${d}" style="stroke-width:${e.w || 1.4}" marker-end="url(#fba-${risk(e.risk)})"/></g>`;
  }).join('');
  const node = (n, x, y) => `<g class="rg-node rg-${risk(n.risk)} rg-click ${n.dim ? 'rg-dim' : ''} ${n.cls || ''} ${sel === n.id ? 'rg-sel' : ''}" data-rgid="${esc(n.id)}" transform="translate(${x},${y})"><title>${esc(n.tip || n.label)}</title>
    <rect width="${bw}" height="${bh}" rx="7"/>${rgIcon(n.icon || 'service', bw - 26, (bh - 16) / 2, 16, 'rg-nic')}<text x="11" y="17" class="rg-l">${esc(fit(n.label, 7.1))}</text><text x="11" y="32" class="rg-s">${esc(fit(n.sub || '', 6.2))}</text></g>`;
  const headSvg = (x, [icon, label, note]) => `<g class="rg-head" transform="translate(${x},8)"><rect class="rg-hbg" width="${bw}" height="${HB - 14}" rx="8"/>
    <circle class="rg-hdot" cx="20" cy="${(HB - 14) / 2}" r="13"/>${rgIcon(icon, 11, (HB - 14) / 2 - 9, 18, 'rg-hic')}
    <title>${esc([label, ...[].concat(note || [])].join('\n'))}</title><text x="42" y="16" class="rg-hl">${esc(label)}</text>${[].concat(note || []).map((t, i) => `<text x="42" y="${30 + i * 13}" class="rg-hn">${esc(hfit(t))}</text>`).join('')}</g>`;
  const marker = r => `<marker id="fba-${r}" viewBox="0 0 10 10" refX="9" refY="5" markerUnits="userSpaceOnUse" markerWidth="9" markerHeight="9" orient="auto"><path d="M0,0 L10,5 L0,10 z" class="rg-arrow-${r}"/></marker>`;
  return `<svg class="rg" id="rg-svg" data-w="${W}" data-h="${H}" width="100%" height="100%" role="img"><defs>${['red', 'amber', 'green', 'grey'].map(marker).join('')}</defs><g id="rg-root">
    ${headSvg(X0, heads[0])}${headSvg(X1, heads[1])}${F.length && heads[2] ? headSvg(X2, heads[2]) : ''}${edgeSvg}${L.map(n => node(n, X0, yL(n.id))).join('')}${R.map(n => node(n, X1, yR(n.id))).join('')}${F.map(n => node(n, X2, yF(n.id))).join('')}</g></svg>`;
}

function fbGraphPanel(title, st, svg, controls, legend) {
  return `<section class="panel mt rc-gpanel ${st.gfull ? 'full' : ''}"><div class="panel-head rc-ghead"><div class="row wrap"><h2>${esc(title)}</h2>${controls}</div>
      <div class="row rc-zoom"><button id="rg-out" title="Zoom out (-)">−</button><span id="rg-pct" class="mono muted">100%</span><button id="rg-in" title="Zoom in (+)">+</button><button id="rg-fit" title="Fit (0)">Fit</button><button id="rg-full" title="${st.gfull ? 'Exit full screen (Esc)' : 'Full screen (f)'}">${st.gfull ? '⤡ Exit' : '⤢ Full screen'}</button></div></div>
    <div class="rg-vp" id="rg-vp" tabindex="0">${svg}</div><div class="rc-legend">${legend}<span class="muted">Hover a box to trace its lines · click it to keep them lit (click it again, the background or Esc to let go) and for its detail · drag to move · Ctrl+scroll to zoom</span></div></section>`;
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
  // Clicking a box pins its trace (the same one hover shows) until it is
  // clicked again, the background is clicked, or Esc is pressed; it survives
  // redraws, so the page can be scrolled with it on.
  const clear = () => { rg.classList.remove('rg-hl'); rg.querySelectorAll('.on').forEach(x => x.classList.remove('on')); };
  const pinned = () => st.pin && rg.querySelector(`[data-rgid="${CSS.escape(st.pin)}"]`);
  const restore = () => { clear(); if (pinned()) light(st.pin); rg.querySelectorAll('.rg-node').forEach(g => g.classList.toggle('rg-pin', g.dataset.rgid === st.pin)); };
  rg.querySelectorAll('[data-rgid]').forEach(g => g.onclick = ev => {
    ev.stopPropagation();
    if (vp.dataset.dragged) return;
    st.pin = st.pin === g.dataset.rgid ? '' : g.dataset.rgid;
    restore();
    onPick(g.dataset.rgid);
  });
  rg.querySelectorAll('[data-fbe]').forEach(g => g.onclick = ev => { ev.stopPropagation(); if (!vp.dataset.dragged) onPick(g.dataset.fbe); });
  vp.onclick = () => { if (!vp.dataset.dragged && st.pin) { st.pin = ''; restore(); } };
  if (!window.fbPinEsc) {
    window.fbPinEsc = true;
    document.addEventListener('keydown', e => {
      if (e.key !== 'Escape' || document.querySelector('.modal-backdrop') || !S.tf.pin) return;
      S.tf.pin = '';
      document.querySelectorAll('#rg-svg').forEach(r => { r.classList.remove('rg-hl'); r.querySelectorAll('.on,.rg-pin').forEach(y => y.classList.remove('on', 'rg-pin')); });
    });
  }
  function light(id) {
      const near = new Set([id]);
      const byOf = e => (e.dataset.by || '').split(' ').filter(Boolean);
      const edges = [...rg.querySelectorAll('.rg-edge')];
      edges.forEach(e => { if (e.dataset.f === id || e.dataset.t === id) { e.classList.add('on'); near.add(e.dataset.f); near.add(e.dataset.t); } });
      // A caller lights its tools beyond the Service; a tool lights its callers.
      edges.forEach(e => {
        const by = byOf(e);
        if (by.includes(id)) { e.classList.add('on'); near.add(e.dataset.f); near.add(e.dataset.t); edges.forEach(x => { if (x.dataset.f === id && x.dataset.t === e.dataset.f) x.classList.add('on'); }); }
        if (e.dataset.t === id) by.forEach(c => { near.add(c); edges.forEach(x => { if (x.dataset.f === c && x.dataset.t === e.dataset.f) x.classList.add('on'); }); });
      });
      rg.querySelectorAll('[data-rgid]').forEach(x => { if (near.has(x.dataset.rgid)) x.classList.add('on'); });
      rg.classList.add('rg-hl');
  }
  rg.querySelectorAll('[data-rgid]').forEach(g => {
    g.onmouseenter = () => { clear(); light(g.dataset.rgid); };
    g.onmouseleave = restore;
  });
  restore();
}

// ---------- Activity ----------
const actKinds = {tool: 'Tools', meta: 'Code and Search mode (run_code, get_tool)', model: 'Models', agent: 'Agents (A2A)', mcp: 'MCP sessions', http: 'Other HTTP routes'};
const actKindIcon = {tool: 'plug', meta: 'swap', model: 'sparkles', agent: 'bot', mcp: 'plug', http: 'route'};
const actCallerKind = {identity: 'service account, no workload captured', agentic: 'agent by its calls, no workload captured', workload: 'workload', user: 'JWT subject', outside: 'outside the cluster', address: 'address'};
const actCallerIcon = {workload: 'server', identity: 'key', user: 'key', outside: 'globe', address: 'network'};
const actTargetLabel = t => t.kind === 'tool' ? `${t.server}/${t.label}` : t.kind === 'model' ? `${t.provider ? t.provider + '/' : ''}${t.label}` : t.label;
const actKindName = {meta: 'meta-tool', tool: 'tool', model: 'model', agent: 'agent', mcp: 'MCP session', http: 'HTTP route'};

// The Activity filter: free text, or key:value terms matched as a
// case-insensitive prefix, like the Scorecard's. Every term is matched
// against one caller-to-target pair.
const lastPartJS = x => String(x || '').split('/').pop();
const actState = (e, c) => [e.requests > e.denied + e.errors ? 'ok' : '', e.denied ? 'refused' : '', e.errors ? 'failed' : '', c?.anonymous ? 'noidentity' : '', e.outsideAllowlist ? 'outsidelist' : ''].filter(Boolean);
const actFields = {
  caller: (e, c) => [c?.label, c?.label?.split('/').pop(), c?.identity].filter(Boolean),
  tool: (e, c, t) => t?.kind === 'tool' ? [t.label] : [],
  server: (e, c, t) => t?.kind === 'tool' || t?.kind === 'mcp' ? [t.server, ...(t.routes || [])].filter(Boolean) : [],
  model: (e, c, t) => t?.kind === 'model' ? [t.label, t.provider].filter(Boolean) : [],
  kind: (e, c, t) => [t?.kind],
  gateway: e => e.gateways || [],
  route: e => e.routes || [],
  user: (e, c) => [...(e.users || []), ...(c?.users || [])],
  state: (e, c) => actState(e, c),
};
const actKeys = [
  {value: 'caller', hint: 'who called: workload, service account or address', example: 'caller:kagent/tom-demo'},
  {value: 'tool', hint: 'MCP tool name', example: 'tool:list_issues'},
  {value: 'server', hint: 'MCP server or its route', example: 'server:github'},
  {value: 'model', hint: 'LLM model or provider', example: 'model:claude'},
  {value: 'kind', hint: 'tool, model, agent, mcp or http', example: 'kind:tool'},
  {value: 'gateway', hint: 'agentgateway gateway or waypoint', example: 'gateway:kagent/'},
  {value: 'route', hint: 'gateway route', example: 'route:kagent/github-mcp'},
  {value: 'user', hint: 'JWT subject', example: 'user:alice'},
  {value: 'state', hint: 'ok, refused, failed, noidentity or outsidelist', example: 'state:refused'},
];
const actStateHint = {ok: 'some calls got through', refused: 'refused by gateway policy', failed: 'backend or routing errors', noidentity: 'no mesh identity and no JWT subject', outsidelist: 'not in the kagent Agent\'s toolNames'};
function actMatch(e, terms, callers, targets) {
  const c = callers.get(e.from), t = targets.get(e.to);
  return terms.every(tm => {
    let ok;
    if (tm.key && actFields[tm.key]) { const w = tm.val.toLowerCase(); ok = actFields[tm.key](e, c, t).some(v => String(v || '').toLowerCase().startsWith(w)); }
    else ok = [c?.label, c?.identity, t && actTargetLabel(t), t?.server, t?.kind, t?.description, ...(e.gateways || []), ...(e.routes || []), ...(e.users || [])].join(' ').toLowerCase().includes((tm.text ?? `${tm.key}:${tm.val}`).toLowerCase());
    return tm.neg ? !ok : ok;
  });
}

function actEdgeRisk(e, callers) {
  const c = callers.get(e.from);
  if (e.outsideAllowlist || (e.denied && e.denied === e.requests)) return 'red';
  if (c?.anonymous || e.denied) return 'amber';
  return 'green';
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
    const links = `${c.agentType && feature('reach') ? `<a class="btn" href="#traffic?tab=agents&agent=${encodeURIComponent(c.label)}">Open agent</a>` : ''}${S.tf.focus !== c.id ? `<button id="tf-focusc" data-id="${esc(c.id)}">Show only its traffic</button>` : ''}`;
    return `<section class="panel mt" id="act-detail"><div class="panel-head"><h2>${esc(c.label)}</h2><div class="row">${links}${c.ref ? '<button id="act-obj">Open object</button>' : ''}${close}</div></div><div class="panel-body">
      <dl class="metadata"><dt>Kind</dt><dd>${esc(c.agentType ? (rcTypes[c.agentType] || c.agentType) + ' (workload)' : actCallerKind[c.kind] || c.kind)}</dd>${c.identity ? `<dt>Mesh identity</dt><dd class="mono">${esc(c.identity)}</dd>` : ''}${(c.users || []).length ? `<dt>JWT subjects</dt><dd class="mono">${esc(c.users.join(', '))}</dd>` : ''}${(c.addresses || []).length ? `<dt>Addresses</dt><dd class="mono">${esc(c.addresses.join(', '))}</dd>` : ''}
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

// Every tool the capture names, per MCP server: called, listed by the
// server (kagent's discovered tools), allowed by an agentgateway allowlist,
// or configured on an agent. A server outside the cluster has no list of its
// own, so for it this is the whole of what can be known.
function actTools(d, callers, matched, terms) {
  const A = S.act;
  const servers = (d.servers || []).filter(x => x.called || x.allowed || x.configured || x.listed);
  if (!servers.length) return '<section class="panel"><div class="empty"><strong>No MCP tools in this capture</strong>No tool was called through agentgateway, and no server list, allowlist or agent configuration names one.</div></section>';
  // Which servers to list: a multi-select, remembered; the first time, the
  // servers that were called (or the first one).
  if (!A.srvs) A.srvs = new Set(servers.filter(x => x.called).map(x => x.name).slice(0, 5));
  for (const n of [...A.srvs]) if (!servers.some(x => x.name === n)) A.srvs.delete(n);
  if (!A.srvs.size && !A.srvNone) A.srvs.add(servers[0].name);
  const multi = A.srvs.size > 1;
  const hit = new Set((matched || []).map(e => e.to));
  const called = new Map((d.targets || []).filter(x => x.kind === 'tool').map(x => [x.server + '/' + x.label, x]));
  const onlyTools = (terms || []).every(tm => !tm.key || ['tool', 'server', 'kind'].includes(tm.key));
  const ok = t => (terms || []).every(tm => {
    let r;
    if (tm.key === 'tool') r = t.name.toLowerCase().startsWith(tm.val.toLowerCase());
    else if (tm.key === 'server') r = t.server.toLowerCase().startsWith(tm.val.toLowerCase()) || lastPartJS(t.server).toLowerCase().startsWith(tm.val.toLowerCase());
    else if (tm.key === 'kind') r = 'tool'.startsWith(tm.val.toLowerCase());
    else if (tm.key) { const c = called.get(t.server + '/' + t.name) || (d.targets || []).find(x => x.kind === 'tool' && x.label === t.name); r = !!c && hit.has(c.id); } // caller:, state: and the rest: tools with matching calls
    else r = (t.server + ' ' + t.name + ' ' + (t.description || '') + ' ' + (t.allowedFor || []).join(' ') + ' ' + (t.configuredOn || []).join(' ')).toLowerCase().includes(tm.text.toLowerCase());
    return tm.neg ? !r : r;
  });
  const tools = (d.tools || []).filter(t => A.srvs.has(t.server) && ok(t)).sort((x, y) => (y.calls - x.calls) || ((y.allowedFor || []).length - (x.allowedFor || []).length) || x.name.localeCompare(y.name));
  const nTools = n => (d.tools || []).filter(t => t.server === n).length;
  const modeNote = x => (x.meta?.run_code ? ` Reached through Code Mode ${fbPlural(x.meta.run_code, 'time')}: agents call run_code, whose code calls these tools, and the logs do not say which; allowed tools with no direct call may have run there.` : '')
    + (x.meta?.get_tool ? ` ${fbPlural(x.meta.get_tool, 'get_tool lookup')} (Search mode).` : '')
    + (x.toolMode && x.toolMode !== 'Standard' ? ` Its backend is in ${x.toolMode} mode now.` : x.meta?.run_code && x.toolMode === 'Standard' ? ' Its backend is in Standard mode now, so those calls were made before it changed.' : '');
  const why0 = x => x.hasList ? `the server's own list (kagent): ${fbPlural(x.listed, 'tool')}.`
    : `the full list is not in the capture (it is not a kagent MCP server, and agentgateway does not log the tools a server offers). These ${fbPlural(nTools(x.name), 'tool')} are the ones seen: called, in an allowlist, or in an agent's configuration. It may offer more.`;
  const why = x => why0(x) + modeNote(x);
  const picked = servers.filter(x => A.srvs.has(x.name));
  // One line per server for a few; for more, what kind of list each has.
  const known = picked.length <= 3 ? picked.map(x => `<div><strong class="mono">${esc(x.name)}</strong>: ${esc(why(x))}</div>`).join('')
    : (() => { const own = picked.filter(x => x.hasList).length, cm = picked.filter(x => x.name === 'code_mode').length, seen = picked.length - own - cm;
      return `<div>${fbPlural(picked.length, 'server')}: ${own ? `${num(own)} with their own tool list (kagent)` : ''}${own && seen ? '; ' : ''}${seen ? `${num(seen)} with only the tools seen in the capture (called, in an allowlist or in an agent's configuration; they may offer more)` : ''}${cm ? `; code_mode is agentgateway's Code Mode (agents call run_code, whose code calls the real tools)` : ''}. Hover a server in the list for its counts.</div>`; })();
  const label = picked.length === servers.length ? 'all' : !picked.length ? 'none' : picked.length === 1 ? picked[0].name : `${picked[0].name} and ${picked.length - 1} more`;
  const row = x => `<label class="rc-fitem ts-row" data-srvname="${esc(x.name.toLowerCase())}" title="${esc(`${nTools(x.name)} tools: ${x.called} called, ${x.allowed} in an allowlist, ${x.configured} configured on an agent, ${x.listed} listed by the server`)}">
    <span class="rc-fname"><input type="checkbox" data-srvck="${esc(x.name)}" ${A.srvs.has(x.name) ? 'checked' : ''}> <span class="mono">${esc(x.name)}</span></span><span class="rc-fn">${num(nTools(x.name))}</span>
    <span class="rc-fsub muted">${x.called ? `${num(x.called)} called` : 'none called'}${x.meta?.run_code ? ` · run_code ×${num(x.meta.run_code)}` : ''}${x.hasList ? ' · server list' : ' · no server list'}${x.toolMode && x.toolMode !== 'Standard' ? ' · ' + x.toolMode + ' mode' : ''}</span></label>`;
  const picker = `<div class="rc-fmenu"><button class="chip on" data-fmenu data-srvmenu>MCP servers: ${esc(label)} <small>${picked.length}/${servers.length}</small> ▾</button>
    <div class="rc-fpop ts-pop" hidden><input type="search" id="srv-q" placeholder="Find a server" autocomplete="off" spellcheck="false" style="width:100%;margin:2px 0 6px">
    <div class="row" style="gap:6px;margin-bottom:6px"><button class="ghost" data-srvall="1">Select all</button><button class="ghost" data-srvall="0">Clear</button><small class="muted" style="margin-left:auto">most called first</small></div>
    <div class="ts-list">${servers.map(row).join('')}</div></div></div>`;
  const yes = (v, cls) => v ? `<span class="${cls || 'green'}">yes</span>` : '<span class="muted">—</span>';
  const list = xs => (xs || []).length ? `<span class="rc-small" title="${esc(xs.join('\n'))}">${esc(xs.slice(0, 3).join(', '))}${xs.length > 3 ? ` <span class="muted">and ${xs.length - 3} more</span>` : ''}</span>` : '<span class="muted">—</span>';
  const calledAs = t => called.get(t.server + '/' + t.name) || (t.calls ? (d.targets || []).find(x => x.kind === 'tool' && x.label === t.name) : null); // kagent servers file calls under their own name
  const metaRows = picked.flatMap(x => Object.entries(x.meta || {}).map(([k, n]) => `<tr><td class="mono">${esc(k)}${multi ? `<br><small class="muted">${esc(x.name)}</small>` : ''}</td><td class="mono">${num(n)}</td><td colspan="4"><span class="uz-st used">${k === 'run_code' ? 'Code Mode entry point' : 'Search mode lookup'}</span></td><td class="rc-small muted">${k === 'run_code' ? "agentgateway gives Code Mode agents this one tool; the code it runs calls the server's tools, each checked against the allowlist" : 'progressive disclosure: finds a tool and its schema before invoke_tool calls it (invoke_tool is logged as the tool it calls)'}</td></tr>`)).join('');
  const rows = metaRows + tools.map(t => { const c = calledAs(t); return `<tr><td class="mono">${esc(t.name)}${multi ? `<br><small class="muted">${esc(t.server)}</small>` : ''}</td>
    <td class="mono">${t.calls ? `${num(t.calls)}${c?.denied ? ` <small class="amber">${num(c.denied)} refused</small>` : ''}${c?.errors ? ` <small class="muted">${num(c.errors)} failed</small>` : ''}` : picked.find(x => x.name === t.server)?.meta?.run_code ? '<span class="amber" title="Its server was reached through Code Mode; the logs do not say which tools run_code used">not called directly · may have run in run_code</span>' : '<span class="muted">not called</span>'}${c?.undeclared ? ' <span class="rc-badge red">not listed by its server</span>' : ''}</td>
    <td>${list(t.callers)}</td><td>${list(t.allowedFor)}</td><td>${list(t.configuredOn)}</td><td>${yes(t.declared)}</td><td class="rc-small muted">${esc(t.description || '')}</td></tr>`; }).join('');
  return `<section class="panel"><div class="panel-head"><h2>Tools by MCP server</h2><small class="muted">${fbPlural(tools.length, 'tool')} on ${fbPlural(picked.length, 'server')}</small></div>
    <div class="panel-body" style="padding-bottom:0"><div class="row wrap" style="align-items:flex-start;gap:14px">${picker}<div class="rc-small" style="flex:1;min-width:280px">${known || '<span class="muted">Pick one or more servers.</span>'}</div></div>${terms?.length && !onlyTools ? '<p class="muted rc-small mt">Keys other than tool: and server: keep the tools with calls that match them.</p>' : ''}</div>
    <div class="table-scroll mt" style="max-height:60vh">${rows ? `<table><thead><tr><th>Tool</th><th>Calls</th><th>Called by</th><th title="Who an agentgateway MCP authorization rule allows to call it">Allowed for</th><th title="Agents whose toolNames or ALLOWED_TOOLS name it">Configured on</th><th title="In the server's discovered tool list (kagent)">Listed</th><th>Description</th></tr></thead><tbody>${rows}</tbody></table>` : '<div class="empty"><strong>No tools match</strong></div>'}</div></section>`;
}

function actModels(d, matched, terms) {
  const hit = new Set((matched || []).map(e => e.to)), hitC = new Set((matched || []).map(e => e.from));
  const models = (d.targets || []).filter(x => x.kind === 'model' && (!terms?.length || hit.has(x.id)));
  const spenders = (d.callers || []).filter(c => c.tokens > 0 && (!terms?.length || hitC.has(c.id))).sort((a, b) => b.tokens - a.tokens);
  if (!models.length) return terms?.length ? '<section class="panel"><div class="empty"><strong>No model calls match</strong></div></section>' : '<section class="panel"><div class="empty"><strong>No LLM calls</strong>No agentgateway request log in the capture names a model or provider.</div></section>';
  return `<div class="grid2 rc-grid"><section class="panel"><div class="panel-head"><h2>Models</h2></div><div class="table-scroll"><table><thead><tr><th>Model</th><th>Provider</th><th>Calls</th><th>Tokens in</th><th>Tokens out</th><th>Cost</th></tr></thead><tbody>
    ${models.map(x => `<tr><td class="mono">${esc(x.label)}</td><td>${esc(x.provider || '—')}</td><td class="mono">${num(x.requests)}</td><td class="mono">${num(x.inputTokens)}</td><td class="mono">${num(x.outputTokens)}</td><td class="mono">${x.cost ? x.cost.toFixed(4) : '—'}</td></tr>`).join('')}</tbody></table></div></section>
    <section class="panel"><div class="panel-head"><h2>Spend by caller</h2></div><div class="table-scroll"><table><thead><tr><th>Caller</th><th>Tokens</th><th>Calls</th><th></th></tr></thead><tbody>
    ${spenders.map(c => `<tr><td>${esc(c.label)}</td><td class="mono">${num(c.tokens)}</td><td class="mono">${num(c.requests)}</td><td>${c.anonymous ? '<span class="rc-badge amber">no identity</span>' : ''}</td></tr>`).join('') || '<tr><td colspan="4" class="muted">No token counts were logged</td></tr>'}</tbody></table></div></section></div>`;
}

function actDenials(d, matched, terms) {
  const pairs = new Set((matched || []).map(e => e.from + '→' + e.to));
  const all = d.denials || [];
  const ds = terms?.length ? all.filter(x => pairs.has(x.from + '→' + x.to) && (terms.every(tm => tm.key !== 'gateway' || x.gateway.toLowerCase().startsWith(tm.val.toLowerCase()) || tm.neg))) : all;
  if (all.length && !ds.length) return '<section class="panel"><div class="empty"><strong>No refusals match</strong></div></section>';
  if (!ds.length) return '<section class="panel"><div class="empty"><strong>No refused calls</strong>No agentgateway request in the capture was refused by policy.</div></section>';
  return ds.map(x => [x, all.indexOf(x)]).map(([x, i], k) => `<section class="panel ${k ? 'mt' : ''}"><div class="panel-head"><h2>${num(x.count)} × ${esc(x.caller)} → ${esc(x.target)}</h2><span class="rc-badge amber">${esc(x.reason)}</span></div><div class="panel-body">
    <p>${esc(x.meaning || '')}</p>
    <dl class="metadata"><dt>Gateway</dt><dd class="mono">${esc(x.gateway)}</dd>${x.route ? `<dt>Route</dt><dd class="mono">${esc(x.route)}</dd>` : ''}${x.status ? `<dt>Status</dt><dd class="mono">${esc(x.status)}</dd>` : ''}<dt>Last</dt><dd>${fbTime(x.last)}</dd>${x.error ? `<dt>Logged error</dt><dd class="mono rc-small">${esc(x.error)}</dd>` : ''}</dl>
    <h3 class="mt">Policies at this gateway</h3>${(x.policies || []).length ? `<div class="table-scroll rc-items"><table><thead><tr><th></th><th>Policy</th><th>Attached to</th><th>Sets</th></tr></thead><tbody>
      ${x.policies.map((p, j) => `<tr><td>${p.matches ? '<span class="rc-badge amber" title="It has a section that produces this refusal">likely</span>' : ''}</td><td><button class="link mono" data-actpol="${i}:${j}">${esc(p.ref.kind)} ${esc(p.ref.namespace)}/${esc(p.ref.name)}</button></td><td class="mono rc-small">${esc(p.target)}</td><td class="mono rc-small">${esc((p.sections || []).join(', '))}</td></tr>`).join('')}</tbody></table></div>`
      : '<p class="muted">No agentgateway policy in the capture is attached to this route, gateway or Service. The refusal may come from a policy on the listener or the GatewayClass, or one the customer excluded.</p>'}
    ${x.look ? `<p class="mt"><strong>Next:</strong> ${esc(x.look)}</p>` : ''}
    ${feature('traffic') && x.from.startsWith('w:') && /waypoint/.test(x.gateway) && x.route ? `<p class="mt"><a class="btn" href="#traffic?from=${encodeURIComponent(x.caller)}&to=${encodeURIComponent(x.route)}">Why? The mesh checks between ${esc(x.caller)} and ${esc(x.route)}</a> <small class="muted">NetworkPolicy, mTLS and Istio authorization; the agentgateway policy above is checked after them</small></p>` : ''}</div></section>`).join('');
}

function actChanges(d) {
  const c = d.changes;
  if (!c) return `<section class="panel"><div class="empty"><strong>No baseline loaded</strong>Load an earlier capture of the same cluster on the Compare page, or open this one with <code>--baseline</code>. This tab then lists the MCP tools that appeared, went away or changed their description.<br><a href="#compare">Go to Compare →</a></div></section>`;
  const list = (title, rows, cols) => `<section class="panel mt"><div class="panel-head"><h2>${esc(title)}</h2><small class="muted">${rows.length}</small></div>${rows.length ? `<div class="table-scroll" style="max-height:40vh"><table><thead><tr>${cols.map(x => `<th>${x}</th>`).join('')}</tr></thead><tbody>${rows.join('')}</tbody></table></div>` : '<div class="empty">None</div>'}</section>`;
  return `<p class="muted">Compared with <span class="mono">${esc(c.baseline)}</span>.${c.baselineNote ? ' ' + esc(c.baselineNote) + '.' : ''}</p>
    ${list('Description changed', (c.described || []).map(x => `<tr><td class="mono">${esc(x.server)}/${esc(x.name)}</td><td class="rc-small">${esc(x.before)}</td><td class="rc-small red">${esc(x.after)}</td></tr>`), ['Tool', 'Before', 'Now'])}
    ${list('Added', (c.added || []).map(x => `<tr><td class="mono">${esc(x.server)}/${esc(x.name)}</td><td class="rc-small muted">${esc(x.description || '')}</td></tr>`), ['Tool', 'Description'])}
    ${list('Removed', (c.removed || []).map(x => `<tr><td class="mono">${esc(x.server)}/${esc(x.name)}</td><td class="rc-small muted">${esc(x.description || '')}</td></tr>`), ['Tool', 'Description'])}
    ${list('Called now, not called in the baseline', (c.newlyCalled || []).map(x => `<tr><td class="mono">${esc(x)}</td></tr>`), ['Tool'])}
    ${list('Caller and target pairs not in the baseline', (c.newCallers || []).map(x => `<tr><td class="mono">${esc(x)}</td></tr>`), ['Caller → target'])}`;
}

// ---------- Traffic map ----------
const tfIntro = 'Captured network connections and gateway calls, with policy decisions and caller details.';
const tfLayerName = {both: 'Both', network: 'Network', gateway: 'Gateway calls'};
const tfNoAct = {edges: [], callers: [], targets: [], tools: [], denials: [], findings: [], notes: [], coverage: [], totals: {}};

// One filter for both layers. Network edges answer the network keys,
// gateway calls the gateway keys; from: (or caller:), to:, ns: and layer:
// work on both. A key a layer does not have leaves its edges out.
function tfNetFields(e, nodes) {
  const n = id => nodes.get(id);
  return k => tfFields[k] ? tfFields[k](e, n).filter(Boolean) : null;
}
function tfGwFields(e, callers, targets) {
  const c = callers.get(e.from), t = targets.get(e.to);
  return k => k === 'from' ? actFields.caller(e, c, t)
    : k === 'to' ? [t && actTargetLabel(t), t?.label].filter(Boolean)
    : k === 'ns' ? [c?.namespace, ...(t?.routes || []).map(r => r.split('/')[0])].filter(Boolean)
    : actFields[k] ? actFields[k](e, c, t) : null;
}
function tfTermsOk(fields, text, layer, terms) {
  return terms.every(tm => {
    const k = tm.key === 'caller' ? 'from' : tm.key === 'namespace' ? 'ns' : tm.key;
    let ok;
    if (k === 'layer') ok = layer.startsWith(tm.val.toLowerCase());
    else if (k) { const vs = fields(k), w = tm.val.toLowerCase(); ok = !!vs && vs.some(v => String(v || '').toLowerCase().startsWith(w)); }
    else ok = text.toLowerCase().includes(tm.text.toLowerCase());
    return tm.neg ? !ok : ok;
  });
}
const tfNetText = (e, nodes) => [nodes.get(e.from)?.label, nodes.get(e.to)?.label, e.via, e.why, e.verdict, e.tls].join(' ');
const tfGwText = (e, callers, targets) => { const c = callers.get(e.from), t = targets.get(e.to); return [c?.label, c?.identity, t && actTargetLabel(t), t?.server, t?.kind, t?.description, ...(e.gateways || []), ...(e.routes || []), ...(e.users || [])].join(' '); };
const tfNetOk = (e, terms, nodes) => tfTermsOk(tfNetFields(e, nodes), tfNetText(e, nodes), 'network', terms);
const tfGwOk = (e, terms, callers, targets) => tfTermsOk(tfGwFields(e, callers, targets), tfGwText(e, callers, targets), 'gateway', terms);
const tfFlags = {plaintext: ['Plaintext in the mesh', 'red'], 'plaintext-in': ['Plaintext from outside the mesh', 'amber'], unpoliced: ['No policy', 'amber'], denied: ['Refused', 'amber'], mismatch: ['Logs and policy disagree', 'amber']};
const tfVerdictCls = {allowed: '', denied: 'green', unknown: 'amber'};

const tfFields = {
  from: (e, n) => [n(e.from)?.label, n(e.from)?.label?.split('/').pop()],
  to: (e, n) => [n(e.to)?.label, n(e.to)?.label?.split('/').pop()],
  ns: (e, n) => [n(e.from)?.namespace, n(e.to)?.namespace],
  flag: e => e.flags || [],
  tls: e => [e.tls],
  verdict: e => [e.verdict],
  via: e => [e.via, lastPartJS(e.via)],
  kind: (e, n) => [n(e.from)?.agentType ? 'agent' : '', n(e.to)?.kind],
};
const tfKeys = [
  {value: 'from', hint: 'source workload', example: 'from:kagent/tom-demo'},
  {value: 'to', hint: 'destination Service or workload', example: 'to:kagent/github-mcp'},
  {value: 'ns', hint: 'namespace at either end', example: 'ns:kagent'},
  {value: 'flag', hint: 'plaintext, plaintext-in, unpoliced, denied or mismatch', example: 'flag:unpoliced'},
  {value: 'tls', hint: 'mtls, plaintext or mixed', example: 'tls:plaintext'},
  {value: 'verdict', hint: 'allowed, denied or unknown', example: 'verdict:denied'},
  {value: 'via', hint: 'waypoint that carried it', example: 'via:github-mcp-waypoint'},
  {value: 'kind', hint: 'agent (source), service, workload, external (destination)', example: 'kind:agent'},
];
const tfAllKeys = [
  {value: 'from', hint: 'who: workload, service account or address (also caller:)', example: 'from:kagent/tom-demo'},
  {value: 'to', hint: 'destination Service, or the tool, model or agent called', example: 'to:kagent/github-mcp'},
  {value: 'ns', hint: 'namespace at either end', example: 'ns:kagent'},
  {value: 'layer', hint: 'network (ztunnel) or gateway (agentgateway)', example: 'layer:gateway'},
  ...actKeys.filter(k => !['caller', 'kind'].includes(k.value)),
  {value: 'kind', hint: 'agent (source); service, workload, external; tool, model, agent, mcp, http', example: 'kind:tool'},
  ...tfKeys.filter(k => !['from', 'to', 'ns', 'kind'].includes(k.value)),
];

function tfMatch(e, terms, nodes) {
  const n = id => nodes.get(id);
  return terms.every(tm => {
    let ok;
    if (tm.key && tfFields[tm.key]) { const w = tm.val.toLowerCase(); ok = tfFields[tm.key](e, n).some(v => String(v || '').toLowerCase().startsWith(w)); }
    else ok = [n(e.from)?.label, n(e.to)?.label, e.via, e.why, e.verdict, e.tls].join(' ').toLowerCase().includes((tm.text ?? `${tm.key}:${tm.val}`).toLowerCase());
    return tm.neg ? !ok : ok;
  });
}

function tfFromHash(p) {
  if (p.get('from') && p.get('to')) { S.tf.exFrom = p.get('from'); S.tf.exTo = p.get('to'); S.tf.tab = 'explain'; S.tf.ex = null; }
  if (p.get('tab') === 'agents') { S.tf.tab = 'agents'; S.reach.sel = p.get('agent') || ''; S.reach.scroll = !!p.get('agent'); return; }
  if (p.get('tab')) S.tf.tab = p.get('tab');
  if (p.get('agent')) { S.tf.focusName = p.get('agent'); S.tf.focus = ''; S.tf.tab = 'map'; S.tf.sel = ''; S.tf.gz = null; }
  if (p.get('layer')) S.tf.layer = p.get('layer');
  if (p.get('caller')) { S.tf.q = 'caller:' + p.get('caller'); S.tf.tab = 'map'; }
}

async function trafficPage() {
  const T = S.tf, A = S.act;
  if (!feature('traffic') && !feature('reach')) return fbEntOff('Traffic', 'It shows every connection ztunnel logged and every call through agentgateway, with mutual TLS, waypoints, policy verdicts, tools, models and tokens, and explains why any connection was allowed or denied.', 'traffic');
  // #traffic?from=..&to=.. opens the explainer, ?agent= follows one agent,
  // ?layer= and ?caller= come from the old Activity links. app.js may parse
  // the hash before this file has loaded, so read it here as well.
  if (T.lastHash !== location.hash) { T.lastHash = location.hash; tfFromHash(new URLSearchParams(location.hash.split('?')[1] || '')); }
  const hasTraffic = feature('traffic');
  if (!hasTraffic) T.tab = 'agents';
  if (!T.data || T.key !== fbKey()) {
    const [d, a, r] = await Promise.all([hasTraffic ? api('api/traffic') : Promise.resolve({nodes: [], edges: [], findings: [], notes: [], totals: {}, namespaces: []}), hasTraffic ? api('api/activity').catch(() => null) : null,
      feature('reach') ? api('api/reach').catch(() => null) : null]);
    T.data = d; A.data = a; T.reach = r; T.key = A.key = fbKey();
    if (feature('reach') && !S.reach.flowSrc) S.reach.flowSrc = (await api('api/flows', {limit: 1}).catch(() => ({}))).bySource || {};
  }
  const d = T.data, a = A.data || tfNoAct, t = d.totals || {}, at = a.totals || {};
  const nodes = new Map((d.nodes || []).map(n => [n.id, n]));
  const callers = new Map((a.callers || []).map(c => [c.id, c])), targets = new Map((a.targets || []).map(x => [x.id, x]));
  const hasNet = !!d.captured, hasGw = (a.edges || []).length > 0;
  const layer = T.layer && (T.layer !== 'network' || hasNet) && (T.layer !== 'gateway' || hasGw) ? T.layer : hasNet && hasGw ? 'both' : hasGw ? 'gateway' : 'network';
  if (T.focusName) {
    const n = (d.nodes || []).find(x => (x.kind === 'workload' || x.kind === 'waypoint') && x.label === T.focusName) || (a.callers || []).find(c => c.label === T.focusName);
    if (n) T.focus = n.id; else toast(`${T.focusName} has no traffic in this capture`);
    T.focusName = '';
  }
  const fnode = T.focus ? nodes.get(T.focus) || callers.get(T.focus) : null;
  if (fnode && feature('reach') && T.fkey !== T.focus + '|' + T.key) { T.freach = await api('api/reach', {agent: fnode.label}).catch(() => null); T.fkey = T.focus + '|' + T.key; }
  const terms = qTerms(T.q);
  const typedKind = terms.some(tm => tm.key === 'kind');
  const srcNS = id => nodes.get(id)?.namespace || callers.get(id)?.namespace;
  const isAgent = id => nodes.get(id)?.agentType || callers.get(id)?.agentType || callers.get(id)?.agentic;
  const common = (from, to) => (!T.focus || from === T.focus) && (!T.agents || isAgent(from)) && (!T.ns || srcNS(from) === T.ns || nodes.get(to)?.namespace === T.ns);
  const netAll = (d.edges || []).filter(e => tfNetOk(e, terms, nodes));
  const gwAll = (a.edges || []).filter(e => tfGwOk(e, terms, callers, targets));
  const netE = layer === 'gateway' ? [] : netAll.filter(e => common(e.from, e.to) && (!T.flag || (e.flags || []).includes(T.flag)));
  const gwE = layer === 'network' || T.flag ? [] : gwAll.filter(e => common(e.from, e.to) && (typedKind || A.kinds[targets.get(e.to)?.kind]));
  const stat = (label, value, sub, attrs, cls = '', on = false) => `<div class="stat fb-statbtn ${on ? 'on' : ''}" ${attrs}><div class="eyebrow">${label}</div><div class="value ${cls}">${value}</div><p>${sub}</p></div>`;
  const netRefused = (d.edges || []).filter(e => e.denied > 0).length;
  const stats = `<div class="stats fb-stats">${stat('Connections', num(t.connections), `${fbPlural(t.edges || 0, 'edge')} from ztunnel`, 'data-tflayer="network" title="Only ztunnel connections"', '', layer === 'network')}
    ${stat('Gateway calls', num(at.requests), `${num(at.toolCalls)} tool, ${num(at.llmCalls)} LLM · ${fbTok((at.inputTokens || 0) + (at.outputTokens || 0))} tokens`, 'data-tflayer="gateway" title="Only calls through agentgateway"', '', layer === 'gateway')}
    ${stat('Plaintext in the mesh', num(t.plaintext), 'between two enrolled workloads', 'data-tfflag="plaintext" title="Show only these"', t.plaintext ? 'red' : '', T.flag === 'plaintext')}
    ${stat('No policy', num(t.unpoliced), 'no NetworkPolicy or AuthorizationPolicy', 'data-tfflag="unpoliced" title="Show only these"', t.unpoliced ? 'amber' : '', T.flag === 'unpoliced')}
    ${stat('Refused', num(netRefused + (a.denials || []).length), `${fbPlural(netRefused, 'edge')} by the mesh, ${num(at.denied)} calls by the gateway`, 'data-tftab="refused" title="Open the Refused tab"', netRefused + (at.denied || 0) ? 'amber' : '')}
    ${stat('No identity', num(at.anonymous), 'tool, model or agent calls', 'data-tfq="state:noidentity" title="Show only these"', at.anonymous ? 'red' : '', T.q === 'state:noidentity')}</div>`;
  const cov = (a.coverage || []).length ? `<p class="muted fb-cov">Gateways with request logs: ${a.coverage.map(c => `<span class="mono">${esc(c.gateway)}</span> <small>(${num(c.requests)}${c.waypoint ? ', waypoint' : ''})</small>`).join(' · ')}</p>` : '';
  const toolNote = n => /cannot see the full list of tools|through Code Mode/.test(n); // shown per server on the Tools tab
  const noteHTML = xs => xs.map(n => `<div class="notice" style="margin-bottom:12px"><span>△</span><div>${esc(rcCap(n))}.</div></div>`).join('');
  const notes = noteHTML([...(d.notes || []), ...(a.notes || []).filter(n => !toolNote(n))]);
  const rank = {critical: 0, high: 1, medium: 2, low: 3};
  const allFindings = [...(d.findings || []), ...(a.findings || []), ...tfAgentFindings(T.reach)].sort((x, y) => (rank[x.severity] ?? 9) - (rank[y.severity] ?? 9));
  T.findings = allFindings;
  const findings = `<section class="panel"><div class="panel-head"><h2>Findings</h2><small class="muted">${fbPlural(allFindings.length, 'finding')} from the network, the gateways and each agent's reach</small></div>${fbFindings(allFindings, T, 'tf', 5)}</section>`;
  const tab = (id, label, n, cls) => `<button class="pst-tab ${T.tab === id ? 'on' : ''}" data-tftab="${id}">${esc(label)}${n ? `<span class="pst-tab-n ${cls || ''}">${num(n)}</span>` : ''}</button>`;
  const ch = a.changes, nChanges = ch ? (ch.added || []).length + (ch.removed || []).length + (ch.described || []).length : 0;
  const nTools = (a.targets || []).filter(x => x.kind === 'tool' || x.kind === 'model').length;
  const nAg = (T.reach?.agents || []).length;
  const tabs = `<nav class="pst-tabs">${hasTraffic ? tab('map', 'Map') : ''}${feature('reach') ? tab('agents', 'Agents', nAg) : ''}${hasNet ? tab('edges', 'Connections', netE.length) : ''}${hasGw ? tab('calls', 'Calls', gwE.length) : ''}${hasGw ? tab('tools', 'Tools and models', nTools) : ''}${hasTraffic ? tab('refused', 'Refused', netRefused + (a.denials || []).length, 'amber') : ''}${hasGw ? tab('changes', 'Tool changes', nChanges, (ch?.described || []).length ? 'red' : '') : ''}${hasTraffic ? tab('explain', 'Why? Explain a connection') : ''}</nav>`;
  const nsOpts = `<select id="tf-ns"><option value="">All namespaces</option>${(d.namespaces || []).map(n => `<option ${T.ns === n ? 'selected' : ''}>${esc(n)}</option>`).join('')}</select>`;
  const lchips = hasNet && hasGw ? `<span class="rc-layer" style="margin:0 6px 0 0;padding:0 8px 0 0;border-left:0;border-right:1px solid var(--line)">${Object.entries(tfLayerName).map(([k, l]) => `<button class="chip ${layer === k ? 'on' : ''}" data-tflayer="${k}">${l}</button>`).join('')}</span>` : '';
  const nAgents = [...(d.edges || []), ...(a.edges || [])].filter(e => isAgent(e.from)).length;
  const achip = nAgents ? `<button class="chip ${T.agents ? 'on' : ''}" id="tf-agents" title="Only traffic from agents, MCP servers and agent gateways">Agents only <small>${nAgents}</small></button>` : '';
  const fchips = layer === 'gateway' ? '' : Object.entries(tfFlags).map(([k, [l]]) => { const n = (d.edges || []).filter(e => (e.flags || []).includes(k)).length; return n ? `<button class="chip ${T.flag === k ? 'on' : ''}" data-tfflag="${k}">${esc(l)} <small>${n}</small></button>` : ''; }).join('');
  const kindCount = k => (a.edges || []).filter(e => targets.get(e.to)?.kind === k).length;
  const kchips = layer === 'network' || typedKind ? '' : Object.entries(actKinds).filter(([k]) => kindCount(k)).map(([k, l]) => `<button class="chip ${A.kinds[k] ? 'on' : ''}" data-actkind="${k}" title="Gateway calls to ${esc(l.toLowerCase())}">${esc(l)} <small>${kindCount(k)}</small></button>`).join('');
  const input = `<input id="tf-q" placeholder="Filter. Try from: to: tool: server: flag: state: layer: or text" value="${esc(T.q)}" style="min-width:340px" autocomplete="off" spellcheck="false">`;
  const toolbar = `<div class="toolbar"><div class="row wrap">${tfFoundMenu(d, a, hasNet, hasGw)}${achip}${fchips}${kchips}</div><div class="row">${nsOpts}${input}</div></div>`;
  void lchips;
  const toolbarPlain = `<div class="toolbar">${input}</div>`;
  const sel = tfSelPanel(d, a, nodes, callers, targets);
  let body;
  if (T.tab === 'agents') return head('Traffic', `${esc(tfIntro)} <span class="ent-pill">Enterprise</span>`, '<button id="tf-term">&gt;_ fbtraffic in terminal</button>') + notes + stats + cov + findings + tabs + await reachBody();
  if (!hasNet && !hasGw && T.tab !== 'explain') body = '<section class="panel"><div class="empty"><strong>No traffic in this capture</strong>The page is read from ztunnel access logs (istio-system) and agentgateway request logs (gateways and waypoints). Collect those pods\' logs to fill it. You can still explain any pair on the Why? tab from the captured policy.</div></section>';
  else if (T.tab === 'map') body = toolbar + tfFocusBar(fnode, netE, gwE) + tfMap(d, a, netE, gwE, nodes, callers, targets, layer) + sel;
  else if (T.tab === 'edges') body = toolbar + tfEdges(netE, id => nodes.get(id)?.label || id) + sel;
  else if (T.tab === 'calls') body = toolbar + actCalls(gwE, callers, targets) + sel;
  else if (T.tab === 'tools') body = toolbarPlain + actTools(a, callers, gwAll, terms) + '<div class="mt"></div>' + actModels(a, gwAll, terms);
  else if (T.tab === 'refused') {
    const netRef = netAll.filter(e => e.denied > 0);
    body = toolbarPlain + (hasGw ? `<h3 class="mt">Refused by the gateways</h3>${actDenials(a, gwAll, terms)}` : '') + (hasNet ? `<h3 class="mt">Refused by the mesh</h3>${netRef.length ? tfEdges(netRef, id => nodes.get(id)?.label || id) : '<section class="panel"><div class="empty"><strong>No refused connections</strong>ztunnel logged no connection refused by policy.</div></section>'}` : '') + sel;
  } else if (T.tab === 'changes') body = actChanges(a);
  if (T.tab === 'explain') body = tfExplainTab(d);
  return head('Traffic', `${esc(tfIntro)} <span class="ent-pill">Enterprise</span>`, '<button id="tf-term">&gt;_ fbtraffic in terminal</button>') + notes + stats + cov + findings + tabs + body;
}

// The detail under the map for what is selected: the network side
// (connections and the checks) and the gateway side (calls, tools, tokens).
function tfSelPanel(d, a, nodes, callers, targets) {
  const T = S.tf;
  if (!T.sel) return '';
  // A caller-to-Service line (drawn with both layers on, or a connection that
  // also carried gateway calls): what the caller may call on the MCP servers
  // behind that Service, and the calls themselves.
  if (T.sel.startsWith('svctl:')) T.sel = T.sel.slice(6);
  if (T.sel.startsWith('svct:')) {
    const [svc, tool] = T.sel.slice(5).split('|');
    const es = (a.edges || []).filter(e => e.service === svc && (t => t && (t.kind === 'tool' ? t.label : actTargetLabel(t)) === tool)(targets.get(e.to)));
    const srvs = [...new Set((a.edges || []).filter(e => e.service === svc).map(e => targets.get(e.to)).filter(x => x?.kind === 'tool').map(x => x.server))];
    const info = (a.tools || []).find(t => t.name === tool && srvs.includes(t.server));
    const guarded = info && (a.tools || []).some(t => t.server === info.server && (t.allowedFor || []).length);
    const may = !info ? '' : guarded ? ((info.allowedFor || []).length ? `Allowed for <span class="mono">${esc(info.allowedFor.join(', '))}</span> by ${esc((info.policies || []).map(p => p.namespace + '/' + p.name).join(', '))}.` : 'The allowlist on this server gives it to no one.') : `No allowlist on ${esc(info.server)}: every caller may call it.`;
    return `<section class="panel mt"><div class="panel-head"><h2><span class="mono">${esc(tool)}</span> through ${esc(nodes.get(svc)?.label || (p => p[p.length - 3] + '/' + p[p.length - 1])(svc.slice(4).split('/')))}</h2><button class="ghost" id="act-selx" aria-label="Close">✕</button></div>
      <div class="panel-body" style="padding-bottom:0"><p class="rc-small" style="margin:0">${may}${(info?.configuredOn || []).length ? ` In the configuration of <span class="mono">${esc(info.configuredOn.join(', '))}</span>.` : ''}${info?.description ? `<br><span class="muted">${esc(info.description)}</span>` : ''}</p></div>
      ${es.length ? `
      <div class="table-scroll" style="max-height:44vh"><table><thead><tr><th>Caller</th><th>Calls</th><th>Refused</th><th>Failed</th><th>Last seen</th><th></th></tr></thead><tbody>
      ${es.sort((x, y) => y.requests - x.requests).map(e => { const c = callers.get(e.from); return `<tr><td>${esc(c?.label || e.from)}</td><td class="mono">${num(e.requests)}</td><td class="mono ${e.denied ? 'amber' : ''}">${num(e.denied)}</td><td class="mono">${num(e.errors)}</td><td class="rc-small">${fbTime(e.last)}</td><td>${c && (c.agentType || c.agentic) ? `<button class="ghost" data-tffollow="${esc(c.id)}">Every tool it may call</button>` : ''}</td></tr>`; }).join('')}</tbody></table></div>` : '<div class="empty" style="padding:18px"><strong>Not called in this capture</strong></div>'}</section>`;
  }
  if (T.sel.startsWith('acc:') && T.focus) {
    const srv = T.sel.slice(4, T.sel.lastIndexOf('/'));
    const who = taWho(a, [T.focus]);
    return taPanel(a, who, [srv], `Tools ${who.label} may call on ${srv}`);
  }
  let pair = null;
  if (T.sel.startsWith('gwsvc|')) { const [, f, t] = T.sel.split('|'); pair = {from: f, to: t}; }
  else { const ne = (d.edges || []).find(e => e.id === T.sel); if (ne) pair = {from: ne.from, to: ne.to}; }
  if (pair) {
    const through = (a.edges || []).filter(e => e.from === pair.from && e.service === pair.to);
    // MCP servers are named by tool calls; session requests (initialize,
    // tools/list) carry the route, which is only a server when one is known by it.
    const known = new Set((a.servers || []).map(x => x.name));
    let servers = [...new Set(through.map(e => targets.get(e.to)).filter(x => x?.kind === 'tool').map(x => x.server))];
    if (!servers.length) servers = [...new Set(through.map(e => targets.get(e.to)).filter(x => x?.kind === 'mcp' && known.has(x.server)).map(x => x.server))];
    if (through.length) {
      const who = taWho(a, [pair.from]);
      const svcName = nodes.get(pair.to)?.label || (p => p[p.length - 3] + '/' + p[p.length - 1])(pair.to.slice(4).split('/'));
      const calls = `<section class="panel mt"><div class="panel-head"><h2>${esc(who.label)} → ${esc(svcName)} through agentgateway</h2><div class="row"><small class="muted">${fbPlural(through.reduce((n, e) => n + e.requests, 0), 'call')}</small>${T.sel.startsWith('gwsvc|') ? '<button class="ghost" id="act-selx" aria-label="Close">✕</button>' : ''}</div></div>
        <div class="table-scroll" style="max-height:30vh"><table><thead><tr><th>Tool, model or agent</th><th>Kind</th><th>Calls</th><th>Refused</th><th>Failed</th><th>Last seen</th></tr></thead><tbody>
        ${through.sort((x, y) => y.requests - x.requests).map(e => { const t = targets.get(e.to); return `<tr class="fb-row" data-actsel="${esc(e.from + '→' + e.to)}"><td class="mono">${esc(t ? actTargetLabel(t) : e.to)}</td><td>${esc(actKindName[t?.kind] || '')}</td><td class="mono">${num(e.requests)}</td><td class="mono ${e.denied ? 'amber' : ''}">${num(e.denied)}</td><td class="mono">${num(e.errors)}</td><td class="rc-small">${fbTime(e.last)}</td></tr>`; }).join('')}</tbody></table></div></section>`;
      return (T.sel.startsWith('gwsvc|') ? '' : tfEdgePanel(d, nodes)) + taPanel(a, who, servers, `Tools ${who.label} may call on ${servers.join(', ') || svcName}`) + calls;
    }
  }
  const net = tfEdgePanel(d, nodes);
  const gwKnown = callers.has(T.sel) || targets.has(T.sel) || (a.edges || []).some(e => e.from + '→' + e.to === T.sel);
  const via = (a.edges || []).filter(e => e.service === T.sel);
  if (!gwKnown && via.length) {
    const lbl = x => x ? actTargetLabel(x) : '';
    return net + `<section class="panel mt"><div class="panel-head"><h2>Calls through agentgateway to ${esc(nodes.get(T.sel)?.label || (T.sel.startsWith('svc:') ? (p => p[p.length - 3] + '/' + p[p.length - 1])(T.sel.slice(4).split('/')) : T.sel))}</h2><small class="muted">${fbPlural(via.reduce((n, e) => n + e.requests, 0), 'call')} · click a row for the detail</small></div>
      <div class="table-scroll" style="max-height:44vh"><table><thead><tr><th>Caller</th><th>Tool, model or agent</th><th>Kind</th><th>Calls</th><th>Refused</th><th>Failed</th><th>Gateway</th></tr></thead><tbody>
      ${via.sort((x, y) => y.requests - x.requests).map(e => { const c = callers.get(e.from), t = targets.get(e.to); return `<tr class="fb-row" data-actsel="${esc(e.from + '→' + e.to)}"><td>${esc(c?.label || e.from)}</td><td class="mono">${esc(lbl(t))}</td><td>${esc(actKindName[t?.kind] || '')}</td><td class="mono">${num(e.requests)}</td><td class="mono ${e.denied ? 'amber' : ''}">${num(e.denied)}</td><td class="mono">${num(e.errors)}</td><td class="mono rc-small">${esc((e.gateways || []).join(', '))}</td></tr>`; }).join('')}</tbody></table></div></section>`;
  }
  if (!gwKnown) return net;
  S.act.sel = T.sel;
  const ge = (a.edges || []).find(e => e.from + '→' + e.to === T.sel), gt = ge && targets.get(ge.to);
  const access = ge && (gt?.kind === 'tool' || (gt?.kind === 'mcp' && (a.servers || []).some(x => x.name === gt.server))) ? (() => { const who = taWho(a, [ge.from]); return taPanel(a, who, [gt.server], `Tools ${who.label} may call on ${gt.server}`); })() : '';
  // A caller box: every server it called tools on.
  const cw = !ge && callers.has(T.sel) ? (() => { const who = taWho(a, [T.sel]); return taPanel(a, who, taServers(a, who), `Tools ${who.label} may call`); })() : '';
  return net + actSel(a, callers, targets) + access + cw;
}

function tfMap(d, a, netE, gwE0, nodes, callers, targets, layer) {
  let gwE = gwE0;
  const T = S.tf;
  if (!netE.length && !gwE.length) return '<section class="panel mt"><div class="empty"><strong>Nothing matches</strong>Change the layer, flag, namespace or filter.</div></section>';
  T.texp ||= {};
  const ncls = n => !n ? 'grey' : n.kind === 'external' ? 'grey' : n.inMesh ? 'green' : 'amber';
  const icon = n => n?.kind === 'waypoint' ? 'router' : n?.kind === 'service' ? 'network' : n?.kind === 'external' ? 'cloud' : n?.agentType ? 'bot' : 'server';
  const sub = n => !n ? '' : [n.kind === 'service' ? 'Service' : n.kind, n.kind === 'external' ? 'outside the capture' : n.inMesh ? 'in the mesh' : 'not in the mesh', n.waypoint ? 'waypoint ' + n.waypoint : '', n.agentType ? rcTypes[n.agentType] || n.agentType : ''].filter(Boolean).join(' · ');
  const mk = id => { const n = nodes.get(id); return {id, label: n?.label || id, sub: sub(n), risk: ncls(n), icon: icon(n), tip: [n?.label, sub(n)].filter(Boolean).join('\n')}; };
  const mkCaller = id => { if (nodes.has(id)) return mk(id); const c = callers.get(id) || {label: id}; return {id, label: c.label, sub: [c.agentType ? rcTypes[c.agentType] || c.agentType : c.agentic && c.kind === 'identity' ? actCallerKind.agentic : actCallerKind[c.kind] || c.kind, c.anonymous ? 'no identity' : ''].filter(Boolean).join(' · '), risk: c.anonymous ? 'amber' : 'green', icon: c.agentType || c.agentic ? 'bot' : actCallerIcon[c.kind], tip: [c.label, c.identity].filter(Boolean).join('\n')}; };
  const mkTarget = id => { const x = targets.get(id) || {label: id}; return {id, label: x.kind === 'tool' ? x.label : actTargetLabel(x), sub: [actKindName[x.kind], x.kind === 'tool' ? 'on ' + x.server : '', x.undeclared ? 'not in its server\'s list' : ''].filter(Boolean).join(' · '),
    risk: x.undeclared ? 'red' : x.denied ? 'amber' : x.errors > x.requests / 2 ? 'amber' : 'green', icon: actKindIcon[x.kind], tip: [actTargetLabel(x), x.description, `${num(x.requests)} calls, ${num(x.denied)} refused, ${num(x.errors)} failed`, (x.gateways || []).length ? 'through ' + x.gateways.join(', ') : ''].filter(Boolean).join('\n')}; };
  const L = new Map(), R = new Map();
  netE.forEach(e => { if (!L.has(e.from)) L.set(e.from, mk(e.from)); if (!R.has(e.to)) R.set(e.to, mk(e.to)); });
  // With both layers on, a gateway call is drawn on the Service it went
  // through (the waypoint's Service, or the gateway's), so the right-hand
  // column holds Services only; the tools are on the line and in the box.
  // The Gateway calls layer draws each tool on its own.
  const both = layer === 'both';
  // Following one caller: a box for every tool it has access to on the MCP
  // servers it uses, drawn by what it did with it. Its tool calls and MCP
  // sessions on those servers are drawn by these boxes instead.
  const acc = [];
  if (T.focus && layer !== 'network') {
    const who = taWho(a, [T.focus]);
    for (const srv of taServers(a, who)) acc.push(toolAccess(a, who, srv));
  }
  if (acc.length) {
    const accSrv = new Set(acc.map(x => x.server));
    const mcpRoutes = new Set(gwE.filter(e => e.from === T.focus && accSrv.has(targets.get(e.to)?.server) && targets.get(e.to)?.kind === 'tool').flatMap(e => e.routes || []));
    gwE = gwE.filter(e => { const t = targets.get(e.to); return !(e.from === T.focus && (t?.kind === 'tool' && accSrv.has(t.server) || t?.kind === 'mcp' && ((t.routes || []).some(r => mcpRoutes.has(r)) || accSrv.has(t.server)))); });
  }
  const onSvc = new Map();
  if (both) {
    const loose = [];
    for (const e of gwE) {
      if (!e.service) { loose.push(e); continue; }
      const k = e.from + '|' + e.service;
      let x = onSvc.get(k);
      if (!x) { x = {from: e.from, to: e.service, requests: 0, denied: 0, errors: 0, tokens: 0, tools: new Map(), gateways: new Set(), outside: false}; onSvc.set(k, x); }
      x.requests += e.requests; x.denied += e.denied; x.errors += e.errors; x.tokens += (e.inputTokens || 0) + (e.outputTokens || 0);
      const t = targets.get(e.to);
      if (t && t.kind !== 'mcp' && t.kind !== 'http') x.tools.set(t.kind === 'tool' ? t.label : actTargetLabel(t), (x.tools.get(t.kind === 'tool' ? t.label : actTargetLabel(t)) || 0) + e.requests);
      (e.gateways || []).forEach(g => x.gateways.add(g));
      x.outside ||= e.outsideAllowlist;
    }
    gwE = loose;
  }
  const svcLabel = id => nodes.get(id)?.label || (id.startsWith('svc:') ? id.slice(4).split('/').filter((_, i, a) => i === a.length - 3 || i === a.length - 1).join('/') : id);
  const svcTools = new Map();
  for (const x of onSvc.values()) {
    if (!L.has(x.from)) L.set(x.from, mkCaller(x.from));
    if (!R.has(x.to)) R.set(x.to, nodes.has(x.to) ? mk(x.to) : {id: x.to, label: svcLabel(x.to), sub: 'Service', risk: 'green', icon: 'network', tip: svcLabel(x.to)});
    const st = svcTools.get(x.to) || new Set(); x.tools.forEach((_, k) => st.add(k)); svcTools.set(x.to, st);
  }
  for (const [id, st] of svcTools) if (st.size) { const n = R.get(id); n.sub = `${n.sub} · ${fbPlural(st.size, 'tool')} called`; n.icon = 'plug'; n.tip += `\nCalled through agentgateway: ${[...st].join(', ')}`; }
  gwE.forEach(e => {
    if (!L.has(e.from)) L.set(e.from, mkCaller(e.from));
    if (!R.has(e.to)) R.set(e.to, mkTarget(e.to));
  });
  const left = [...L.values()], right = [...R.values()];
  const maxC = Math.max(1, ...netE.map(e => e.connections)), maxR = Math.max(1, ...gwE.map(e => e.requests));
  const ge = netE.map(e => ({id: e.id, from: e.from, to: e.to, risk: e.risk, dash: e.denied > 0 || e.verdict === 'denied', w: 1.2 + 3 * Math.log(1 + e.connections) / Math.log(1 + maxC),
    tip: `${nodes.get(e.from)?.label} → ${nodes.get(e.to)?.label}\n${num(e.connections)} connections, ${e.tls}${e.denied ? `, ${num(e.denied)} refused` : ''}${e.via ? `\nvia ${e.via}` : ''}\npolicy: ${e.verdict}${e.layer ? ' (' + e.layer + ')' : ''}\n${e.why}\nClick for the checks`}));
  // The overview with both layers: a Tools column behind each Service,
  // with every tool of the MCP servers behind it, called or not, coloured
  // by what the captured allowlists say about it.
  const far = [], svcSum = new Map();
  const tview = T.tools || 'both'; // called, allowed or both
  const showTool = (called, allowed, configured) => tview === 'both' || (tview === 'called' ? called : tview === 'configured' ? configured : allowed);
  if (both && !acc.length) {
    const per = new Map(); // svc|tool -> calls through it by the callers drawn
    for (const x of onSvc.values()) for (const [tool, n] of x.tools) {
      const k = x.to + '|' + tool;
      const y = per.get(k) || {calls: 0, callers: new Set()};
      y.calls += n; y.callers.add(x.from); per.set(k, y);
    }
    const maxT = Math.max(1, ...[...per.values()].map(y => y.calls));
    const whoCache = new Map(), whoOf = c => { if (!whoCache.has(c)) whoCache.set(c, taWho(a, [c])); return whoCache.get(c); };
    // Servers an agent is configured for, or allowlisted on, are drawn too,
    // even when the capture has no call to them: a dashed line to the
    // Service they are reached through.
    const svcServers = new Map(), cfgBy = new Map(); // svc -> servers; tool key -> agent ids configured for it
    for (const n of left) {
      const c = callers.get(n.id);
      if (!(nodes.get(n.id)?.agentType || c?.agentType || c?.agentic)) continue;
      const who = taWho(a, [n.id]);
      for (const srv of taServers(a, who)) {
        const si = (a.servers || []).find(x => x.name === srv);
        for (const t of (a.tools || []).filter(t => t.server === srv && ((t.configuredOn || []).includes(who.label) || taAllowed(t, who)))) {
          const k = srv + '/' + t.name; (cfgBy.get(k) || cfgBy.set(k, new Set()).get(k)).add(n.id);
        }
        // Its own configuration says which Service it reaches the server at;
        // without that, only a server with one Service is drawn.
        const mine = c?.serverServices?.[srv] || ((si?.services || []).length === 1 ? si.services : []);
        for (const svc of mine) {
          (svcServers.get(svc) || svcServers.set(svc, new Set()).get(svc)).add(srv);
          const reached = [...onSvc.values()].some(x => x.from === n.id && x.to === svc) || ge.some(g => g.from === n.id && g.to === svc);
          if (reached) continue;
          if (!R.has(svc)) { const node = nodes.has(svc) ? mk(svc) : {id: svc, label: svcLabel(svc), sub: 'Service', risk: 'grey', icon: 'network', tip: svcLabel(svc)}; R.set(svc, node); right.push(node); }
          ge.push({id: 'cfg|' + n.id + '|' + svc, from: n.id, to: svc, risk: 'grey', dash: true, cls: 'rg-unused', w: 1, tip: `${n.label} → ${svcLabel(svc)}\nConfigured for or allowed tools on ${srv}, and not seen calling it in the capture`});
        }
      }
    }
    const svcs = [...new Set([...[...onSvc.values()].map(x => x.to), ...svcServers.keys()])];
    for (const svc of svcs) {
      // The MCP servers behind a Service: the servers its tool calls name.
      const svcTargets = (a.edges || []).filter(e => e.service === svc).map(e => targets.get(e.to));
      const servers = [...new Set([...svcTargets.filter(x => x?.kind === 'tool' || x?.kind === 'meta').flatMap(x => x.server.split(',')), ...(svcServers.get(svc) || [])])];
      const metas = [...new Set(svcTargets.filter(x => x?.kind === 'meta'))];
      const sum = {allowed: 0, called: 0, red: 0};
      for (const mt of metas) {
        const y = per.get(svc + '|' + actTargetLabel(mt)) || {calls: 0, callers: new Set()};
        if (!y.calls) continue;
        const id = 'svct:' + svc + '|' + actTargetLabel(mt), cl = [...y.callers].map(c => callers.get(c)?.label || c);
        far.push({id, order: -1, label: mt.label, sub: `${mt.mode === 'Code' ? 'Code Mode' : 'Search lookup'} · ${fbPlural(y.calls, 'call')} · ${fbPlural(cl.length, 'caller')}`, risk: 'green', icon: 'swap',
          tip: `${mt.label} on ${mt.server}: ${mt.mode === 'Code' ? 'Code Mode gives agents this one tool; the code it runs calls the server\'s tools, and the logs do not say which' : 'Search mode: agents look a tool up before invoke_tool calls it'}\n${num(y.calls)} calls by ${cl.join(', ')}`});
        ge.push({id: 'svctl:' + id, from: svc, to: id, by: [...y.callers], risk: 'green', cls: 'fb-gw', w: 1.2 + 3 * Math.log(1 + y.calls) / Math.log(1 + maxT), tip: `${svcLabel(svc)} → ${mt.label}: ${num(y.calls)} calls`});
      }
      const codeCalls = metas.some(mt => mt.mode === 'Code' && (per.get(svc + '|' + actTargetLabel(mt))?.calls || 0) > 0);
      for (const srv of servers) {
        const infos = (a.tools || []).filter(t => t.server === srv);
        const guarded = infos.some(t => (t.allowedFor || []).length);
        const names = new Set([...infos.map(t => t.name), ...[...per.keys()].filter(k => k.startsWith(svc + '|')).map(k => k.slice(svc.length + 1)).filter(n => (a.targets || []).some(x => x.kind === 'tool' && x.server === srv && x.label === n))]);
        const idle = [];
        for (const name of names) {
          const t = infos.find(x => x.name === name) || {name, server: srv};
          const y = per.get(svc + '|' + name) || {calls: 0, callers: new Set()};
          const allowed = guarded ? (t.allowedFor || []).length > 0 : true;
          const refusedBy = guarded && y.calls ? [...y.callers].filter(c => !taAllowed(t, whoOf(c))) : [];
          const cfgOn = (t.configuredOn || []).length > 0;
          const state = y.calls ? (refusedBy.length ? (allowed ? 'called, not allowed for some' : 'called, not allowed') : guarded ? 'called' : 'called, no allowlist') : allowed && codeCalls ? 'allowed, may have run in run_code'
            : !guarded ? (cfgOn ? 'in its config, not enforced' : 'not in any config, not blocked') : allowed ? 'allowed, not called' : cfgOn ? 'configured, not allowed' : 'not allowed';
          if (allowed && guarded) sum.allowed++;
          if (!guarded) sum.openTools = (sum.openTools || 0) + 1;
          if (cfgOn) sum.configured = (sum.configured || 0) + 1;
          if (!guarded) sum.open = true;
          if (y.calls) sum.called++;
          if (refusedBy.length) sum.red++;
          if (!showTool(y.calls > 0, allowed, (t.configuredOn || []).length > 0)) continue;
          const cfgd = cfgBy.get(srv + '/' + name);
          if (!y.calls && allowed && !codeCalls && !(cfgd && (t.configuredOn || []).length) && !T.fexp['svcf:' + svc + '|' + srv]) { idle.push(t); continue; }
          const id = 'svct:' + svc + '|' + name;
          const cl = [...y.callers].map(c => callers.get(c)?.label || c);
          const risk = refusedBy.length ? 'red' : y.calls ? 'green' : allowed && codeCalls ? 'amber' : 'grey';
          const who = guarded ? (t.allowedFor || []).length ? `Allowed for ${t.allowedFor.join(', ')} by ${(t.policies || []).map(p => p.namespace + '/' + p.name).join(', ')}` : 'The allowlist gives it to nobody' : `No allowlist on ${srv}: every caller may call it`;
          const inCfg = cfgOn ? ` · ${t.configuredOn.map(lastPartJS).join(', ')}` : '';
          far.push({id, order: y.calls ? 0 : inCfg ? 1.5 : 2, label: name, sub: y.calls ? `${state} · ${fbPlural(y.calls, 'call')} · ${fbPlural(cl.length, 'caller')}` : `${lastPartJS(srv)} · ${state}${inCfg || (allowed && guarded ? ` for ${fbPlural((t.allowedFor || []).length, 'caller')}` : '')}`, risk: !guarded && cfgOn && !y.calls ? 'amber' : risk, icon: 'plug', cls: y.calls || (allowed && codeCalls) || (!guarded && cfgOn) ? '' : 'rg-unusednode',
            tip: `${name} on ${srv}, through ${svcLabel(svc)}\n${state}${y.calls ? `: ${num(y.calls)} calls by ${cl.join(', ')}` : ''}${refusedBy.length ? `\nNot allowed for ${refusedBy.map(c => callers.get(c)?.label || c).join(', ')} by the captured allowlist` : ''}\n${who}${(t.configuredOn || []).length ? `\nIn the configuration of ${t.configuredOn.join(', ')}` : ''}${t.description ? '\n' + t.description : ''}\nClick for its callers and who may call it`});
          ge.push({id: 'svctl:' + id, from: svc, to: id, by: [...new Set([...y.callers, ...(cfgd || [])])], risk, dash: !y.calls, cls: y.calls ? 'fb-gw' : 'rg-unused', w: y.calls ? 1.2 + 3 * Math.log(1 + y.calls) / Math.log(1 + maxT) : 1, tip: `${svcLabel(svc)} → ${name}: ${state}`});
        }
        // Many unused tools on one server (a kagent server lists them all) fold into one box.
        if (idle.length > 20) {
          const id = 'svcf:' + svc + '|' + srv;
          far.push({id, order: 3, label: `${fbPlural(idle.length, 'more tool')} on ${lastPartJS(srv)}`, sub: `${guarded ? 'allowed, not called' : 'not in any config, not blocked'} · click to open up`, risk: 'grey', icon: 'folder', cls: 'rg-unusednode', tip: idle.map(t => t.name).join('\n')});
          ge.push({id: 'svctl:' + id, from: svc, to: id, risk: 'grey', dash: true, cls: 'rg-unused', w: 1, tip: `${idle.length} tools on ${srv} no one drawn here called`});
        } else for (const t of idle) {
          const id = 'svct:' + svc + '|' + t.name;
          far.push({id, order: 2, label: t.name, sub: `${lastPartJS(srv)} · ${guarded ? `allowed, not called for ${fbPlural((t.allowedFor || []).length, 'caller')}` : 'not in any config, not blocked'}`, risk: 'grey', icon: 'plug', cls: 'rg-unusednode',
            tip: `${t.name} on ${srv}\nallowed, not called\n${guarded ? `Allowed for ${(t.allowedFor || []).join(', ')} by ${(t.policies || []).map(p => p.namespace + '/' + p.name).join(', ')}` : `No allowlist on ${srv}: every caller may call it`}${(t.configuredOn || []).length ? `\nIn the configuration of ${t.configuredOn.join(', ')}` : ''}${t.description ? '\n' + t.description : ''}`});
          ge.push({id: 'svctl:' + id, from: svc, to: id, risk: 'grey', dash: true, cls: 'rg-unused', w: 1, tip: `${svcLabel(svc)} → ${t.name}: allowed, not called`});
        }
      }
      svcSum.set(svc, sum);
      const n = R.get(svc);
      if (n && servers.length) { n.sub = sum.open ? `no allowlist · ${num(sum.configured || 0)} in agents' config · ${num(sum.called)} called` : `${num(sum.allowed)} allowed · ${num(sum.called)} called${sum.red ? ` · ${num(sum.red)} not allowed` : ''}`; if (sum.open && sum.configured) n.risk = 'amber'; n.tip += `\nTools on ${servers.join(', ')}: ${sum.allowed ? `${fbPlural(sum.allowed, 'tool')} allowed by an allowlist, ` : ''}${sum.openTools ? `${fbPlural(sum.openTools, 'tool')} no allowlist covers (${num(sum.configured || 0)} in agents' config, none enforced), ` : ''}${num(sum.called)} called${sum.red ? `, ${num(sum.red)} called by a caller the allowlist does not allow` : ''}`; if (sum.red) n.risk = 'red'; }
    }
  }
  const maxS = Math.max(1, ...[...onSvc.values()].map(x => x.requests));
  for (const x of onSvc.values()) {
    const calls = `Through agentgateway (${[...x.gateways].join(', ')}): ${num(x.requests)} calls${x.denied ? `, ${num(x.denied)} refused` : ''}${x.errors ? `, ${num(x.errors)} failed` : ''}${x.tokens ? `, ${num(x.tokens)} tokens` : ''}${x.tools.size ? '\n' + [...x.tools].map(([k, n]) => `  ${k} ×${n}`).join('\n') : ''}`;
    const net = ge.find(g => g.from === x.from && g.to === x.to);
    if (net) { net.tip += '\n' + calls; continue; }
    ge.push({id: 'gwsvc|' + x.from + '|' + x.to, from: x.from, to: x.to, risk: x.outside ? 'red' : callers.get(x.from)?.anonymous || x.denied ? 'amber' : 'green', dash: x.denied > 0, cls: 'fb-gw', w: 1.2 + 3 * Math.log(1 + x.requests) / Math.log(1 + maxS),
      tip: `${callers.get(x.from)?.label || x.from} → ${svcLabel(x.to)}\n${calls}\nNo ztunnel connection logged for this pair (the caller is not in the capture, or the logs do not cover it)`});
  }
  gwE.forEach(e => ge.push({id: e.from + '→' + e.to, from: e.from, to: e.to, risk: actEdgeRisk(e, callers), dash: e.denied > 0, cls: 'fb-gw', w: 1.2 + 3 * Math.log(1 + e.requests) / Math.log(1 + maxR),
    tip: `${callers.get(e.from)?.label} → ${actTargetLabel(targets.get(e.to) || {})}\n${num(e.requests)} calls through ${(e.gateways || []).join(', ')}${e.denied ? `, ${num(e.denied)} refused` : ''}${e.errors ? `, ${num(e.errors)} failed` : ''}${e.inputTokens || e.outputTokens ? `\n${num((e.inputTokens || 0) + (e.outputTokens || 0))} tokens` : ''}${e.outsideAllowlist ? '\nnot in the agent\'s tool list' : ''}`}));
  let ghosts = 0;
  const nSvc = [...R.keys()].filter(id => !targets.has(id)).length, nCalled = R.size - nSvc;
  const nOnSvc = [...svcTools.values()].reduce((n, st) => n + st.size, 0);
  if (T.focus && T.freach && (!acc.length || T.showGhosts)) {
    const byNs = {};
    for (const e of tfUnused(netE)) { const p = e.to.slice(4).split('/'); (byNs[p[p.length - 3]] ||= []).push({id: e.to, label: p[p.length - 3] + '/' + p[p.length - 1], e}); }
    for (const [ns, list] of Object.entries(byNs).sort()) {
      ghosts += list.length;
      if (T.fexp[ns] || list.length === 1) {
        for (const g of list) {
          right.push({id: g.id, order: 3, label: g.label, sub: 'allowed, not used', risk: 'grey', icon: 'network', cls: 'rg-unusednode', tip: `${g.label}\nPolicy allows ${fnodeLabel()} to reach it (${g.e.verdict}${g.e.layer ? ', ' + g.e.layer : ''}), and it made no connection to it in the capture.\nClick for the checks`});
          ge.push({id: 'ghost>' + g.id, from: T.focus, to: g.id, risk: 'grey', dash: true, cls: 'rg-unused', w: 1, tip: `allowed, not used: ${g.label}`});
        }
      } else {
        right.push({id: 'ghost:' + ns, order: 3, label: `${ns} (${list.length})`, sub: 'allowed, not used · click to open up', risk: 'grey', icon: 'folder', cls: 'rg-unusednode', tip: list.map(g => g.label).join('\n')});
        ge.push({id: 'ghost>ns:' + ns, from: T.focus, to: 'ghost:' + ns, risk: 'grey', dash: true, cls: 'rg-unused', w: 1, tip: `${list.length} Services in ${ns} it may reach and did not`});
      }
    }
  }
  if (acc.length) {
    if (!left.some(n => n.id === T.focus)) left.push(nodes.has(T.focus) ? mk(T.focus) : mkCaller(T.focus));
    const maxA = Math.max(1, ...acc.flatMap(x => x.rows.map(r => r.calls)));
    for (const x of acc) {
      const shown = [], idle = [];
      for (const r of x.rows) { if (!showTool(r.calls > 0, r.allowed, r.configured)) continue; (r.calls || !r.allowed || r.configured || x.rows.length <= 16 || T.fexp['acc:' + x.server] ? shown : idle).push(r); }
      for (const r of shown) {
        const id = 'acc:' + x.server + '/' + r.name;
        const st = !r.allowed ? 'called, not allowed' : r.calls ? `${r.open ? 'called, no allowlist' : 'allowed, called'} ×${num(r.calls)}` : r.viaCode ? 'allowed, may have run in run_code' : r.open ? (r.configured ? 'in its config, not enforced' : 'not in its config, not blocked') : 'allowed, not called';
        right.push({id, order: r.calls || !r.allowed ? 0 : r.viaCode ? 1 : r.configured ? 1.5 + acc.indexOf(x) / 100 : 2 + acc.indexOf(x) / 100, label: r.name, sub: `${lastPartJS(x.server)} · ${st}`, risk: !r.allowed ? 'red' : r.calls ? 'green' : r.viaCode ? 'amber' : 'grey', icon: 'plug', cls: r.calls || !r.allowed || r.viaCode ? '' : 'rg-unusednode',
          tip: `${r.name} on ${x.server}\n${st}${r.refused ? `, ${num(r.refused)} refused` : ''}${r.configured ? '\nin its own tool list' : ''}${r.description ? '\n' + r.description : ''}\n${x.note}`});
        ge.push({id: 'accl:' + id, from: T.focus, to: id, risk: !r.allowed ? 'red' : r.calls ? 'green' : r.viaCode ? 'amber' : 'grey', dash: !r.calls, cls: r.calls ? 'fb-gw' : 'rg-unused', w: r.calls ? 1.2 + 3 * Math.log(1 + r.calls) / Math.log(1 + maxA) : 1, tip: `${st}: ${r.name}`});
      }
      for (const [k, n] of Object.entries(x.meta || {})) {
        const id = 'accm:' + x.server + '/' + k;
        right.push({id, order: 0, label: k, sub: `${lastPartJS(x.server)} · ${k === 'run_code' ? 'Code Mode' : 'Search lookup'} ×${num(n)}`, risk: 'green', icon: 'swap', tip: `${k} on ${x.server}: ${num(n)} calls\n${k === 'run_code' ? 'Code Mode: the code it sends calls the tools; the logs do not say which, so the allowed tools in amber may have run' : 'Search mode: looks a tool up before invoke_tool calls it'}`});
        ge.push({id: 'accl:' + id, from: T.focus, to: id, risk: 'green', cls: 'fb-gw', w: 2, tip: `${k} ×${num(n)}`});
      }
      if (idle.length) {
        right.push({id: 'accfold:' + x.server, order: 2.5 + acc.indexOf(x) / 100, label: `${fbPlural(idle.length, 'more tool')} on ${lastPartJS(x.server)}`, sub: 'allowed, not called · click to open up', risk: 'grey', icon: 'folder', cls: 'rg-unusednode', tip: idle.map(r => r.name).join('\n')});
        ge.push({id: 'accl:accfold:' + x.server, from: T.focus, to: 'accfold:' + x.server, risk: 'grey', dash: true, cls: 'rg-unused', w: 1, tip: `${idle.length} tools it may call on ${x.server} and did not`});
      }
    }
  }
  const nAcc = acc.reduce((n, x) => n + x.rows.length, 0);
  const toNote = [nAcc ? `${fbPlural(nAcc, 'tool')} it may call or called` : '', nSvc ? fbPlural(nSvc, 'destination') : '', nCalled ? `${num(nCalled)} tools, models or agents called` : far.length ? 'MCP tools behind them' : nOnSvc ? `${num(nOnSvc)} tools on them` : '', ghosts ? `${num(ghosts)} allowed, not used` : ''].filter(Boolean).join(' · ');
  const svg = fbGraph({left, right, far, edges: ge, sel: T.sel, heads: [['server', 'From', fbPlural(left.length, 'source')], ['network', 'To', toNote], ['plug', 'Tools', (() => { const t = [...svcSum.values()].reduce((m, x) => ({allowed: m.allowed + x.allowed, open: m.open + (x.openTools || 0), conf: m.conf + (x.openTools ? Math.min(x.configured || 0, x.openTools) : 0), called: m.called + x.called}), {allowed: 0, open: 0, conf: 0, called: 0}); const l1 = [t.allowed ? `${num(t.allowed)} allowed` : '', `${num(t.called)} called`].filter(Boolean).join(' · '), l2 = [t.conf ? `${num(t.conf)} not enforced` : '', t.open - t.conf ? `${num(t.open - t.conf)} not blocked` : ''].filter(Boolean).join(' · '); return l2 ? [l1, l2] : l1; })()]]});
  const legend = (acc.length ? `<span class="muted">Following ${esc(left.find(n => n.id === T.focus)?.label || '')}: a box for every tool it has access to on ${acc.map(x => esc(x.server)).join(', ')}.</span><span><i class="rc-sw green"></i>allowed, called</span><span><i class="rc-sw dash"></i><span class="muted">faded:</span> allowed, not called</span><span><i class="rc-sw red"></i>called, not allowed by the captured allowlist</span>` : '')
    + (both && !acc.length && far.length ? '<span class="muted">Tools behind each Service:</span><span><i class="rc-sw green"></i>called, and allowed for its callers</span><span><i class="rc-sw red"></i>called by a caller the captured allowlist does not allow</span><span><i class="rc-sw amber"></i>allowed, may have run inside run_code (Code Mode does not log which tools); or in an agent\'s config on a server no allowlist covers, so not enforced</span><span><i class="rc-sw grey"></i><span class="muted">faded, dashed:</span> allowed, not called (or configured, not allowed)</span><span class="muted">Click a box to keep its lines lit; an agent\'s panel below has Show only its traffic.</span>' : '')
    + (both && !acc.length && !far.length ? '<span class="muted">Click a box to keep its lines lit. An agent\'s panel below lists every tool it may call, and Show only its traffic draws them.</span>' : '')
    + (both && !acc.length ? '<span class="muted">Both layers: calls through agentgateway are drawn on the Service they went through; hover a line for its tools. Choose Gateway calls for a box per tool.</span>' : '')
    + (T.focus && T.freach && (!acc.length || T.showGhosts) ? '<span><i class="rc-sw dash"></i><span class="muted">faded Services:</span> allowed by policy, not used</span>' : '')
    + (netE.length ? '<span><i class="rc-sw red"></i>plaintext between mesh workloads</span><span><i class="rc-sw amber"></i>no policy, plaintext from outside the mesh, or logs and policy disagree</span><span><i class="rc-sw green"></i>mutual TLS and policy looks at it</span>' : '')
    + (gwE.length ? '<span><i class="rc-sw red"></i>call outside the agent\'s tool list, to an unlisted tool, or every call refused</span><span><i class="rc-sw amber"></i>call with no identity, or some refused</span>' : '')
    + '<span><i class="rc-sw dash"></i>refused, or policy denies it</span>';
  const tchips = '';
  return fbGraphPanel('Traffic', T, svg, `<small class="muted">${[netE.length ? fbPlural(netE.length, 'connection edge') : '', gwE0.length ? fbPlural(gwE0.length, 'caller and target pair') : ''].filter(Boolean).join(' · ')}</small>${tchips}`, legend);
}

const fnodeLabel = () => S.tf.data?.nodes?.find(n => n.id === S.tf.focus)?.label || S.act.data?.callers?.find(c => c.id === S.tf.focus)?.label || '';
// The focused agent's reach destinations it made no connection to.
function tfUnused(edges) {
  const T = S.tf;
  if (!T.focus || !T.freach) return [];
  const used = new Set(edges.map(e => e.to));
  return (T.freach.direct || []).filter(e => e.to.startsWith('svc:') && e.verdict !== 'denied' && !used.has(e.to));
}

function tfFocusBar(fnode, netE, gwE) {
  const T = S.tf;
  if (!fnode) return '';
  const un = tfUnused(netE);
  const calls = gwE.reduce((n, e) => n + e.requests, 0);
  return `<div class="tf-focus"><strong class="mono">${esc(fnode.label)}</strong><span>${fbPlural(new Set(netE.map(e => e.to)).size, 'destination')} used${calls ? `, ${num(calls)} calls through agentgateway to ${fbPlural(new Set(gwE.map(e => e.to)).size, 'target')}` : ''}${T.freach ? `, ${num(un.length)} more Services allowed by policy and not used` : ''}.</span>
    ${T.freach ? '' : `<span class="muted">${feature('reach') ? 'Not an agent on the Agents page, so what else it may reach is not drawn.' : 'Turn on Agent reach to see what else it may reach.'}</span>`}
    <span style="flex:1"></span>${T.freach && (gwE.length || T.showGhosts) ? `<button id="tf-ghosts">${T.showGhosts ? 'Hide' : 'Show'} Services it may reach and did not use</button>` : ''}${T.freach ? `<a class="btn" href="#traffic?tab=agents&agent=${encodeURIComponent(fnode.label)}">Open agent</a>` : ''}<button class="ghost" id="tf-unfocus">Show everything</button></div>`;
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
    const src = n.kind === 'workload' || n.kind === 'waypoint';
    return `<section class="panel mt" id="tf-detail"><div class="panel-head"><h2>${esc(n.label)}</h2><div class="row">${src && T.focus !== n.id && !(S.act.data?.callers || []).some(c => c.id === n.id) ? '<button id="tf-focus">Show only its traffic</button>' : ''}${n.agentType && feature('reach') && !(S.act.data?.callers || []).some(c => c.id === n.id) ? `<a class="btn" href="#traffic?tab=agents&agent=${encodeURIComponent(n.label)}">Open agent</a>` : ''}${n.ref ? '<button id="tf-obj">Open object</button>' : ''}<button class="ghost" id="tf-selx" aria-label="Close">✕</button></div></div>
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
  if (!ex) return '<p class="muted">Evaluating policies…</p>';
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
    <h3 class="mt">Policy checks in order</h3><div class="tf-steps">${steps || '<p class="muted">No checks: see the summary.</p>'}</div>
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
  const T = S.tf, d = T.data, A = S.act, a = A.data || tfNoAct;
  $('#tf-term')?.addEventListener('click', () => termRun('fbtraffic'));
  if ((!feature('traffic') && !feature('reach')) || !d) return;
  const keepY = fn => { const y = scrollY; fn(); render().then(() => scrollTo(0, y)); };
  const nodes = new Map((d.nodes || []).map(n => [n.id, n]));
  fbFindingsWire(T, T.findings || [], 'tf');
  // Found by: the menu, and what each choice draws.
  document.querySelectorAll('[data-fmenu]').forEach(b => b.onclick = ev => {
    ev.stopPropagation();
    const pop = b.nextElementSibling, open = pop.hidden;
    document.querySelectorAll('.rc-fpop').forEach(p => { p.hidden = true; });
    pop.hidden = !open;
  });
  document.querySelectorAll('.rc-fpop').forEach(p => p.onclick = ev => ev.stopPropagation());
  if (!window.tfFmenuClose) { window.tfFmenuClose = true; document.addEventListener('click', () => document.querySelectorAll('.rc-fpop').forEach(p => { p.hidden = true; })); }
  document.querySelectorAll('[data-tffound]').forEach(b => b.onclick = () => keepY(() => { T.found = b.dataset.tffound; [T.layer, T.tools] = tfFoundSet[T.found]; T.flag = ''; T.gz = null; if (T.tab === 'explain' || T.tab === 'agents') T.tab = 'map'; }));
  // Agent findings open the agent.
  document.querySelectorAll('.rc-finding .rc-items td.mono').forEach(td => { const n = td.textContent.trim(); if ((T.reach?.agents || []).some(x => x.namespace + '/' + x.name === n)) { td.innerHTML = `<a href="#traffic?tab=agents&agent=${encodeURIComponent(n).replace(/%2F/gi, '/')}">${esc(n)}</a>`; } });
  if (T.tab === 'agents') { document.querySelectorAll('[data-tftab]').forEach(b => b.onclick = () => { T.tab = b.dataset.tftab; history.replaceState(null, '', '#traffic'); T.lastHash = location.hash; render(); }); after.reach(); return; }
  document.querySelectorAll('[data-tftab]').forEach(b => b.onclick = () => { T.tab = b.dataset.tftab; render(); });
  document.querySelectorAll('[data-tflayer]').forEach(b => b.onclick = () => keepY(() => { T.layer = T.layer === b.dataset.tflayer && b.classList.contains('stat') ? 'both' : b.dataset.tflayer; T.found = T.layer === 'network' ? 'ztunnel' : T.layer === 'gateway' ? 'agentgateway' : 'any'; T.tools = tfFoundSet[T.found][1]; T.flag = ''; T.gz = null; if (T.tab === 'explain') T.tab = 'map'; }));
  document.querySelectorAll('[data-tfflag]').forEach(b => b.onclick = () => keepY(() => { T.flag = T.flag === b.dataset.tfflag ? '' : b.dataset.tfflag; T.gz = null; if (T.tab === 'explain') T.tab = 'map'; }));
  document.querySelectorAll('[data-tfq]').forEach(b => b.onclick = () => keepY(() => { T.q = T.q === b.dataset.tfq ? '' : b.dataset.tfq; T.gz = null; if (T.tab === 'explain') T.tab = 'map'; }));
  document.querySelectorAll('[data-actkind]').forEach(b => b.onclick = () => keepY(() => { A.kinds[b.dataset.actkind] = !A.kinds[b.dataset.actkind]; T.gz = null; }));
  const ns = $('#tf-ns'); if (ns) ns.onchange = () => keepY(() => { T.ns = ns.value; T.gz = null; });
  const q = $('#tf-q');
  if (q) {
    let t; q.oninput = e => { clearTimeout(t); t = setTimeout(() => { const c = e.target.selectionStart, v = e.target.value; T.q = v; T.gz = null; render().then(() => refocus('#tf-q', c, v)); }, 250); };
    typeahead(q, {keys: tfAllKeys, alias: {namespace: 'ns', caller: 'from'}, values: (f, p, rest) => {
      const D = S.tf.data, X = S.act.data || tfNoAct;
      const nm = new Map((D.nodes || []).map(n => [n.id, n])), cm = new Map((X.callers || []).map(c => [c.id, c])), tm = new Map((X.targets || []).map(x => [x.id, x]));
      const terms = qTerms(rest);
      if (f === 'layer') return countValues(['network', 'gateway'], p, v => v === 'network' ? 'ztunnel connections' : 'agentgateway calls');
      const vals = [];
      for (const e of D.edges || []) if (tfNetOk(e, terms, nm)) vals.push(...(tfNetFields(e, nm)(f) || []));
      for (const e of X.edges || []) if (tfGwOk(e, terms, cm, tm)) vals.push(...(tfGwFields(e, cm, tm)(f) || []));
      if ((f === 'tool' || f === 'server') && !terms.length) for (const x of X.tools || []) if (x.declared) vals.push(f === 'tool' ? x.name : lastPartJS(x.server));
      return countValues(vals.filter(Boolean), p, f === 'flag' ? v => (tfFlags[v] || [])[0] : f === 'state' ? v => actStateHint[v] : null);
    }});
  }
  // Gateway detail: the caller or target panel, and the refusals.
  const cm = new Map((a.callers || []).map(c => [c.id, c]));
  $('#act-selx')?.addEventListener('click', () => keepY(() => { T.sel = ''; }));
  $('#act-obj')?.addEventListener('click', () => { const c = cm.get(T.sel); if (c?.ref) fbOpenRef(c.ref); });
  $('#act-logs')?.addEventListener('click', () => {
    const e = (a.edges || []).find(y => y.from + '→' + y.to === T.sel), x = (a.targets || []).find(y => y.id === e?.to), c = cm.get(e?.from);
    if (!e || !x) return;
    const qq = v => /[\s"]/.test(v) ? `"${v.replace(/"/g, '')}"` : v;
    const words = ['scope:request'];
    if (x.kind === 'tool') words.push('gen_ai.tool.name:' + qq(x.label));
    else if (x.kind === 'model' && x.label !== '(model not logged)') words.push('gen_ai.response.model:' + qq(x.label));
    if ((e.routes || []).length === 1) words.push('route:' + qq(e.routes[0]));
    if (c?.identity) words.push('src.identity:' + qq(c.identity));
    else if ((c?.users || []).length === 1) words.push('jwt.sub:' + qq(c.users[0]));
    openLogs({q: words.join(' ')});
  });
  // The server picker: ticks, Select all and Clear redraw the table and keep
  // the list open, with what was typed in its search box.
  const reopen = () => { const y = scrollY; A.srvOpen = true; render().then(() => scrollTo(0, y)); };
  document.querySelectorAll('[data-srvck]').forEach(b => b.onchange = () => { if (b.checked) A.srvs.add(b.dataset.srvck); else A.srvs.delete(b.dataset.srvck); A.srvNone = !A.srvs.size; reopen(); });
  document.querySelectorAll('[data-srvall]').forEach(b => b.onclick = () => { const on = b.dataset.srvall === '1'; A.srvs = new Set(on ? (a.servers || []).filter(x => x.called || x.allowed || x.configured || x.listed).map(x => x.name) : []); A.srvNone = !on; reopen(); });
  const sq = $('#srv-q');
  if (sq) {
    const f = () => { const q = sq.value.trim().toLowerCase(); A.srvQ = sq.value; document.querySelectorAll('.ts-row').forEach(r => { r.hidden = !!q && !r.dataset.srvname.includes(q); }); };
    sq.oninput = f;
    if (A.srvOpen) { sq.closest('.rc-fpop').hidden = false; sq.value = A.srvQ || ''; f(); sq.focus(); }
  }
  A.srvOpen = false;
  document.querySelectorAll('[data-actpol]').forEach(b => b.onclick = () => { const [i, j] = b.dataset.actpol.split(':').map(Number); fbOpenRef(a.denials[i].policies[j].ref); });
  // Selecting an edge fetches its explanation.
  const select = id => {
    if (id.startsWith('ghost:')) { keepY(() => { T.fexp[id.slice(6)] = true; T.gz = null; }); return; }
    if (id.startsWith('accl:')) id = id.slice(5);
    if (id.startsWith('svctl:svcf:')) id = id.slice(6);
    if (id.startsWith('svcf:')) { keepY(() => { T.fexp[id] = true; T.gz = null; }); return; }
    if (id.startsWith('accfold:')) { keepY(() => { T.fexp['acc:' + id.slice(8)] = true; T.gz = null; }); return; }
    const cm = new Map((S.act.data?.callers || []).map(c => [c.id, c]));
    const agentish = nodes.get(id)?.agentType || cm.get(id)?.agentType || cm.get(id)?.agentic;
    void agentish; // following an agent is its panel's Show only its traffic
    if (id.startsWith('fold>')) id = id.slice(5);
    if (id.startsWith('tools:')) { keepY(() => { (T.texp ||= {})[id.slice(6)] = true; T.gz = null; }); return; }
    if (id.startsWith('ghost>')) id = id.slice(6);
    if (id.startsWith('ns:')) { keepY(() => { T.fexp[id.slice(3)] = true; T.gz = null; }); return; }
    if (T.focus && !nodes.has(id) && id.startsWith('svc:')) {
      const p = id.slice(4).split('/');
      T.exFrom = fnodeLabel(); T.exTo = p[p.length - 3] + '/' + p[p.length - 1]; T.tab = 'explain'; T.ex = null; render(); return;
    }
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
  fbGraphWire(T, 'tf|' + (T.layer || '') + '|' + Object.entries(A.kinds).filter(([, v]) => v).map(([k]) => k).join(',') + '|' + T.flag + '|' + T.ns + '|' + T.q + '|' + T.focus + '|' + T.agents + '|' + Object.keys(T.fexp).join(',') + '|' + Object.keys(T.texp || {}).join(',') + '|' + (T.showGhosts ? 'g' : '') + '|' + (T.tools || '') + (T.gfull ? '|full' : ''), select);
  document.querySelectorAll('[data-tfsel]').forEach(tr => tr.onclick = ev => { if (!ev.target.closest('[data-tfwhy]')) select(tr.dataset.tfsel); });
  document.querySelectorAll('[data-actsel]').forEach(tr => tr.onclick = () => select(tr.dataset.actsel));
  document.querySelectorAll('[data-tfwhy]').forEach(b => b.onclick = () => select(b.dataset.tfwhy));
  $('#tf-selx')?.addEventListener('click', () => keepY(() => { T.sel = ''; }));
  document.querySelectorAll('[data-tffollow]').forEach(b => b.onclick = () => { T.focus = b.dataset.tffollow; T.sel = T.focus; T.fexp = {}; T.gz = null; render(); });
  document.querySelectorAll('[data-tftools]').forEach(b => b.onclick = () => keepY(() => { T.tools = b.dataset.tftools; T.gz = null; }));
  $('#tf-ghosts')?.addEventListener('click', () => keepY(() => { T.showGhosts = !T.showGhosts; T.gz = null; }));
  $('#tf-agents')?.addEventListener('click', () => keepY(() => { T.agents = !T.agents; T.gz = null; }));
  $('#tf-unfocus')?.addEventListener('click', () => { T.focus = ''; T.freach = null; T.fexp = {}; T.gz = null; T.sel = ''; history.replaceState(null, '', '#traffic'); T.lastHash = location.hash; render(); });
  $('#tf-focus')?.addEventListener('click', () => { T.focus = T.sel; T.sel = ''; T.fexp = {}; T.gz = null; render(); });
  $('#tf-focusc')?.addEventListener('click', ev => { T.focus = ev.currentTarget.dataset.id; T.sel = ''; T.fexp = {}; T.gz = null; render(); });
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

// ---------- Agent reach: what it does, and lock down ----------
// These read S.reach.usage (/api/reach/usage), fetched with the agent.
const uzDest = id => (S.reach.usage?.destinations || []).find(d => d.to === id);
const uzState = d => d.mismatch ? ['mismatch', 'used, policy denies'] : d.used && !d.verdict ? ['mismatch', 'used, not in reach'] : d.used ? ['used', 'used'] : d.tried ? ['tried', 'tried, refused'] : ['unused', 'allowed, not used'];
const uzTLS = t => t === 'mtls' ? 'mTLS' : t || '';

// A line's tooltip: what the agent did over it.
function rcUseTip(id) {
  const d = uzDest(id);
  if (!d) return '';
  const bits = [d.used ? 'Used:' : d.tried ? 'Tried, every attempt refused:' : 'Allowed, not used in the capture'];
  if (d.connections) bits.push(`${num(d.connections)} connections${d.tls ? ' (' + uzTLS(d.tls) + ')' : ''}${d.refused ? `, ${num(d.refused)} refused` : ''}`);
  if (d.calls) bits.push(`${num(d.calls)} calls through agentgateway${d.callsRefused ? `, ${num(d.callsRefused)} refused` : ''}`);
  if ((d.tools || []).length) bits.push('tools: ' + d.tools.map(t => t.name + (t.calls ? ' ×' + t.calls : '')).join(', '));
  if (d.tokens) bits.push(`${num(d.tokens)} tokens`);
  if (d.mismatch) bits.push('The captured policy denies this, yet it was used');
  return bits.join('\n');
}

// Under a selected box: the same, as a small table.
function rcUseDetail(id) {
  const d = uzDest(id);
  if (!d) return '';
  const [cls, label] = uzState(d);
  return `<div class="row wrap mt" style="gap:14px;font-size:12px"><span class="uz-st ${cls}">${esc(label)}</span>
    ${d.connections ? `<span>${num(d.connections)} connections${d.tls ? ` · ${esc(uzTLS(d.tls))}` : ''}${d.refused ? ` · <span class="amber">${num(d.refused)} refused</span>` : ''}</span>` : ''}
    ${d.calls ? `<span>${num(d.calls)} calls${d.callsRefused ? ` · <span class="amber">${num(d.callsRefused)} refused</span>` : ''}${d.callsFailed ? ` · ${num(d.callsFailed)} failed` : ''}</span>` : ''}
    ${(d.tools || []).length ? `<span>tools <span class="mono">${esc(d.tools.map(t => t.name + (t.calls ? ' ×' + t.calls : '')).join(', '))}</span></span>` : ''}
    ${d.tokens ? `<span>${num(d.tokens)} tokens</span>` : ''}${d.last && !String(d.last).startsWith('0001') ? `<span class="muted">last ${fbTime(d.last)}</span>` : ''}</div>`;
}

// The timeline: connections, gateway calls and refusals in lanes, with a
// marker where something was first seen.
function uzTimeline(tl) {
  const bs = tl.buckets || [];
  if (!bs.length) return '<div class="empty"><strong>No activity in the logs</strong>No ztunnel connection or agentgateway request from this agent was captured.</div>';
  const W = 1000, L = 128, top = 22, lh = 38, gap = 12, n = bs.length, bw = (W - L - 8) / n;
  const lanes = [['Connections', b => b.connections, 'uz-c'], ['Gateway calls', b => b.requests, 'uz-r'], ['Tool and LLM calls', b => b.toolCalls + b.llmCalls, 'uz-t'], ['Refused', b => b.refused, 'uz-x'], ['Failed', b => b.errors, 'uz-e']]
    .filter(([, f]) => bs.some(b => f(b) > 0));
  if (!lanes.length) return '<div class="empty"><strong>No activity in the logs</strong></div>';
  const H = top + lanes.length * (lh + gap) + 20;
  const t0 = new Date(tl.from).getTime(), t1 = new Date(tl.to).getTime(), span = Math.max(1, t1 - t0);
  const x = t => L + (t - t0) / span * (W - L - 8);
  const lane = ([name, f, cls], i) => {
    const y0 = top + i * (lh + gap), max = Math.max(1, ...bs.map(f));
    return `<text class="uz-lane" x="0" y="${y0 + lh / 2 + 4}">${esc(name)}</text><text class="uz-axis" x="${L - 8}" y="${y0 + 9}" text-anchor="end">${num(max)}</text>
      <line class="uz-grid" x1="${L}" x2="${W - 8}" y1="${y0 + lh}" y2="${y0 + lh}"/>
      ${bs.map((b, j) => { const v = f(b); if (!v) return ''; const h = Math.max(2, v / max * lh); return `<rect class="${cls}" x="${(L + j * bw + 0.5).toFixed(1)}" y="${(y0 + lh - h).toFixed(1)}" width="${Math.max(1, bw - 1).toFixed(1)}" height="${h.toFixed(1)}" rx="1"/>`; }).join('')}`;
  };
  const hits = bs.map((b, j) => `<rect class="uz-hit" x="${(L + j * bw).toFixed(1)}" y="${top - 4}" width="${bw.toFixed(1)}" height="${H - top - 16}"><title>${esc(`${fbTime(b.start)} (${tl.stepSeconds}s)\n${num(b.connections)} connections · ${num(b.requests)} gateway calls · ${num(b.toolCalls)} tool, ${num(b.llmCalls)} LLM\n${num(b.refused)} refused · ${num(b.errors)} failed${b.tokens ? ` · ${num(b.tokens)} tokens` : ''}`)}</title></rect>`).join('');
  const evs = (tl.events || []).map(e => { const xx = x(new Date(e.at).getTime()).toFixed(1); return `<g><title>${esc(`${fbTime(e.at)} first ${e.kind}: ${e.what} (${num(e.count)} in all)`)}</title><line class="uz-ev" x1="${xx}" x2="${xx}" y1="6" y2="${top - 4}"/><circle class="uz-evd" cx="${xx}" cy="6" r="3"/></g>`; }).join('');
  const axis = `<text class="uz-axis" x="${L}" y="${H - 4}">${esc(fbTime(tl.from))}</text><text class="uz-axis" x="${W - 8}" y="${H - 4}" text-anchor="end">${esc(fbTime(tl.to))}</text>`;
  return `<svg class="uz-tl" viewBox="0 0 ${W} ${H}" role="img" aria-label="Activity over time">${lanes.map(lane).join('')}${hits}${evs}${axis}</svg>`;
}

function rcUsageTab(a, U) {
  const R = S.reach, f = R.uzf || 'all', t = U.totals;
  const ds = (U.destinations || []).filter(d => f === 'all' || (f === 'used' ? d.used : f === 'unused' ? !d.used && !d.tried && d.allowed : f === 'refused' ? d.tried || d.refused || d.callsRefused : f === 'odd' ? d.mismatch || (d.used && !d.verdict) : true));
  const chip = (k, l, n) => `<button class="chip ${f === k ? 'on' : ''}" data-uzf="${k}">${l} <small>${num(n)}</small></button>`;
  const notes = (U.notes || []).map(n => `<div class="notice" style="margin-top:12px"><span>△</span><div>${esc(rcCap(n))}.</div></div>`).join('');
  const why = d => d.to.startsWith('svc:') && feature('traffic') ? `<a class="btn" style="padding:3px 8px;font-size:12px" href="#traffic?from=${encodeURIComponent(rcName(a))}&to=${encodeURIComponent(d.label)}">Why?</a>` : '';
  const rows = ds.map(d => { const [cls, label] = uzState(d); return `<tr><td><span class="uz-st ${cls}">${esc(label)}</span></td><td class="mono rc-dst">${esc(d.label)}${d.via ? `<br><small class="muted">via ${esc(d.via)}</small>` : ''}</td>
    <td class="${d.verdict === 'denied' ? 'green' : d.verdict === 'unknown' ? 'amber' : ''}">${esc(d.verdict || '—')}</td>
    <td class="mono rc-small">${d.connections ? `${num(d.connections)}${d.tls ? ` <span class="${d.tls === 'mtls' ? 'green' : 'amber'}">${esc(uzTLS(d.tls))}</span>` : ''}${d.refused ? ` <span class="amber">· ${num(d.refused)} refused</span>` : ''}` : d.observed ? `<span class="muted">${num(d.observed)} in flows</span>` : '—'}</td>
    <td class="mono rc-small">${d.calls ? `${num(d.calls)}${d.callsRefused ? ` <span class="amber">· ${num(d.callsRefused)} refused</span>` : ''}${d.callsFailed ? ` · ${num(d.callsFailed)} failed` : ''}` : '—'}</td>
    <td class="rc-small">${(d.tools || []).map(x => `<span class="mono">${esc(x.name)}${x.calls ? ` ×${x.calls}` : ''}</span>`).join(', ')}${(d.models || []).map(m => `<div class="mono">${esc(m.name)} · ${num(m.tokens)} tokens</div>`).join('')}</td>
    <td class="rc-small">${d.last && !String(d.last).startsWith('0001') ? fbTime(d.last) : ''}</td><td>${why(d)}</td></tr>`; }).join('');
  return `<section class="panel mt"><div class="panel-head"><h2>Over time</h2><small class="muted">${esc(fbTime(U.timeline.from))} to ${esc(fbTime(U.timeline.to))}${U.timeline.stepSeconds ? ` · one bar per ${U.timeline.stepSeconds}s` : ''} · markers show the first time a destination, tool, model or refusal was seen · from ${esc((U.sources || []).join(', ') || 'no source')}</small></div>
    <div class="panel-body">${uzTimeline(U.timeline)}${notes}</div>
    ${(U.timeline.events || []).length ? `<div class="table-scroll" style="max-height:28vh;border-top:1px solid var(--line)"><table><thead><tr><th>First seen</th><th>What</th><th></th><th>In all</th></tr></thead><tbody>${U.timeline.events.map(e => `<tr><td class="rc-small">${fbTime(e.at)}</td><td>${esc(e.kind)}</td><td class="mono">${esc(e.what)}</td><td class="mono">${num(e.count)}</td></tr>`).join('')}</tbody></table></div>` : ''}</section>
  <section class="panel mt"><div class="panel-head"><h2>Allowed and used</h2><small class="muted">${num(t.allowed)} allowed · ${num(t.used)} used · ${num(t.unused)} not observed (includes configured destinations)${t.tried ? ` · ${num(t.tried)} tried and refused` : ''}${t.mismatch + t.unexpected ? ` · <span class="red">${num(t.mismatch + t.unexpected)} denied by policy or outside evaluated reach</span>` : ''}</small></div>
    <div class="panel-body" style="padding-bottom:0"><div class="toolbar"><div class="row wrap">${chip('all', 'All', (U.destinations || []).length)}${chip('used', 'Used', t.used + t.mismatch + t.unexpected)}${chip('unused', 'Allowed, not used', t.unused)}${chip('refused', 'Refused', (U.destinations || []).filter(d => d.tried || d.refused || d.callsRefused).length)}${t.mismatch + t.unexpected ? chip('odd', 'Policy disagrees', t.mismatch + t.unexpected) : ''}</div>
    ${t.unused ? `<button data-rctab="lockdown">Lock down to what it uses →</button>` : ''}</div></div>
    <div class="table-scroll" style="max-height:56vh">${rows ? `<table><thead><tr><th>State</th><th>Destination</th><th>Policy</th><th>Connections</th><th>Gateway calls</th><th>Tools and models</th><th>Last seen</th><th></th></tr></thead><tbody>${rows}</tbody></table>` : '<div class="empty"><strong>Nothing matches</strong></div>'}</div></section>`;
}

function rcLockTab(a, U) {
  const L = U.lockdown || {};
  if (!L.fix) return `<section class="panel mt"><div class="empty"><strong>Insufficient data for a proposal</strong>${esc(rcCap((L.notes || [])[0] || 'no traffic from this agent was captured'))}.</div></section>`;
  const b = {...(L.before || {})}, af = {...(L.after || {})};
  b.ch = (b.critical || 0) + (b.high || 0); af.ch = (af.critical || 0) + (af.high || 0);
  const cmp = (label, k, better) => { const x = b[k], y = af[k]; const cls = x === y ? '' : better(x, y) ? 'green' : 'red'; return `<div><div class="eyebrow">${label}</div><div class="ld-v">${esc(String(x ?? '—'))}<span class="arrow">→</span><span class="${cls}">${esc(L.evaluated ? String(y ?? '—') : '?')}</span></div></div>`; };
  const less = (x, y) => y < x;
  const radRank = {contained: 0, narrow: 1, wide: 2, open: 3};
  const list = xs => xs.length ? `<ul class="ld-list">${xs.map(x => `<li>${esc(x)}</li>`).join('')}</ul>` : '<p class="muted">none</p>';
  const R = S.reach;
  return `<section class="panel mt"><div class="panel-head"><h2>Proposed restrictions</h2><small class="muted">Egress policy and tool allowlists based on ${esc(a.name)}'s captured traffic. Review the YAML before applying it.</small></div><div class="panel-body">
    <h3>If you apply it${L.evaluated ? '' : ' <small class="muted">(not re-evaluated)</small>'}</h3>
    <div class="ld-cmp">${cmp('Radius', 'radius', (x, y) => (radRank[y] ?? 9) < (radRank[x] ?? 9))}${cmp('Internet', 'internet', (x, y) => y === 'denied' && x !== 'denied')}${cmp('Direct', 'direct', less)}${cmp('Total', 'closure', less)}${cmp('Write', 'write', less)}${cmp('Critical + high', 'ch', less)}</div>
    <p class="muted rc-small">Agent reach is re-evaluated with the proposed egress policies. Total reach includes indirect paths through retained destinations, which may need separate restrictions.</p>
    ${(L.cut || []).length ? `<div class="notice bad mt"><div><strong>This would cut off destinations it uses:</strong> ${esc(L.cut.join(', '))}</div></div>` : L.evaluated ? `<div class="notice info mt"><div>Every Service it used stays reachable: ${esc((L.stillUsed || []).join(', ') || 'none')}.</div></div>` : ''}
    ${(L.breaks || []).map(x => `<div class="notice mt"><span>△</span><div>${esc(x)}</div></div>`).join('')}
    ${(L.notes || []).map(x => `<div class="notice mt"><span>△</span><div>${esc(rcCap(x))}</div></div>`).join('')}
    <div class="grid2 rc-grid mt"><div><h3>Keeps</h3>${list(L.keeps || [])}</div><div><h3>Removes <small class="muted">${num((L.removes || []).length)} allowed, not observed in this capture</small></h3>${(L.removes || []).length > 12 && !R.ldAll ? list(L.removes.slice(0, 12)) + `<button class="ghost" id="ld-all">Show all ${num(L.removes.length)}</button>` : list(L.removes || [])}</div></div>
    <div class="row between mt"><h3>The policy</h3><div class="row"><button id="ld-copy">Copy YAML</button><button id="ld-dl">Download</button></div></div>
    <pre class="code mt">${esc(L.fix)}</pre>
    <p class="muted rc-small mt">Built from the capture's log window only. Something the agent does rarely (a nightly job, a fallback model) may not be in it. Apply in a test namespace first, or watch refusals on the Traffic map after applying.</p></div></section>`;
}

function rcUsageWire(a) {
  const R = S.reach, U = R.usage;
  if (R.act && R.gwf) {
    const me = rcName(a);
    fbFindingsWire(R.gwf, (R.act.findings || []).map(f => ({...f, items: (f.items || []).filter(it => (it.note || '').includes(me) || (it.target || '').includes(me))})).filter(f => f.items.length), 'rcgw');
  }
  if (!U) return;
  const keepY = fn => { const y = scrollY; fn(); render().then(() => scrollTo(0, y)); };
  document.querySelectorAll('[data-uzf]').forEach(b => b.onclick = () => keepY(() => { R.uzf = b.dataset.uzf; }));
  $('#ld-all')?.addEventListener('click', () => keepY(() => { R.ldAll = true; }));
  $('#ld-copy')?.addEventListener('click', () => navigator.clipboard.writeText(U.lockdown.fix).then(() => toast('YAML copied'), () => toast('Clipboard unavailable')));
  $('#ld-dl')?.addEventListener('click', () => {
    const url = URL.createObjectURL(new Blob([U.lockdown.fix + '\n'], {type: 'application/yaml'}));
    const l = document.createElement('a'); l.href = url; l.download = `${a.namespace}-${a.name}-lockdown.yaml`; l.click(); setTimeout(() => URL.revokeObjectURL(url), 1000);
  });
}

// ---------- Agents: tools and models, and gateway findings ----------
function rcToolsTab(a, act) {
  const id = 'w:' + a.id, targets = new Map((act.targets || []).map(x => [x.id, x]));
  const es = (act.edges || []).filter(e => e.from === id);
  const by = k => es.filter(e => targets.get(e.to)?.kind === k).map(e => ({e, t: targets.get(e.to)}));
  const tools = by('tool'), models = by('model'), agents = by('agent'), other = es.filter(e => ['mcp', 'http'].includes(targets.get(e.to)?.kind));
  const refusals = (act.denials || []).filter(d => d.from === id);
  const link = feature('traffic') ? `<a class="btn" href="#traffic?agent=${encodeURIComponent(rcName(a))}">Open on Traffic</a>` : '';
  // Tools it may call (an allowlist names its service account, or its own
  // configuration names them) and did not, in this capture.
  const sa = lastPartJS(a.identity?.serviceAccount || ''), me = rcName(a); // serviceAccount is ns/name
  const forMe = w => w === a.namespace + '/' + sa || w === 'service account ' + sa;
  const mine = t => (t.allowedFor || []).some(forMe) || (t.configuredOn || []).includes(me);
  const calledNames = new Set(tools.map(({t}) => t.server + '/' + t.label));
  const idle = (act.tools || []).filter(t => mine(t) && !calledNames.has(t.server + '/' + t.name));
  const idleBy = {};
  idle.forEach(t => (idleBy[t.server] ||= []).push(t));
  const whoA = taWho(act, [id], a.namespace, sa);
  const accessPanel = taPanel(act, whoA, taServers(act, whoA), `Tools ${me} may call`);
  const idlePanel0 = idle.length ? `<section class="panel mt"><div class="panel-head"><h2>May call, did not</h2><small class="muted">${fbPlural(idle.length, 'tool')} an allowlist or its own configuration gives ${esc(me)} that it did not call in this capture. Candidates to take away.</small></div><div class="panel-body">
    ${Object.entries(idleBy).map(([srv, ts]) => `<p style="margin:0 0 8px"><strong class="mono">${esc(srv)}</strong> <span class="mono rc-small">${ts.map(t => esc(t.name)).join(', ')}</span></p><p class="muted rc-small" style="margin:0 0 12px">${esc([...new Set(ts.flatMap(t => [...(t.allowedFor || []).filter(forMe).map(() => 'allowed by ' + (t.policies || []).map(p => p.namespace + '/' + p.name).join(', ')), ...((t.configuredOn || []).includes(me) ? ['in its configured tool list'] : [])]))].join('; '))}</p>`).join('')}</div></section>` : '';
  void idlePanel0;
  if (!es.length) return accessPanel + `<section class="panel mt"><div class="empty"><strong>No calls through agentgateway</strong>No agentgateway gateway or waypoint in the capture logged a request from ${esc(me)}. Calls it makes directly, not through a gateway, are on the Traffic page as connections, without tool names.</div></section>`;
  const tbl = (title, head, rows, note) => rows.length ? `<section class="panel mt"><div class="panel-head"><h2>${esc(title)}</h2>${note ? `<small class="muted">${note}</small>` : ''}</div><div class="table-scroll" style="max-height:48vh"><table><thead><tr>${head.map(h => `<th>${h}</th>`).join('')}</tr></thead><tbody>${rows.join('')}</tbody></table></div></section>` : '';
  const flag = (e, t) => e.outsideAllowlist ? '<span class="rc-badge red">not in its tool list</span>' : t.undeclared ? '<span class="rc-badge red">not listed by its server</span>' : t.declared ? '<span class="rc-badge green">listed</span>' : '';
  return accessPanel + `<div class="row between mt"><p class="muted" style="margin:0">Every call ${esc(rcName(a))} made through agentgateway: ${fbPlural(es.reduce((n, e) => n + e.requests, 0), 'call')}, ${num(es.reduce((n, e) => n + e.denied, 0))} refused, ${fbTok(es.reduce((n, e) => n + (e.inputTokens || 0) + (e.outputTokens || 0), 0))} tokens.</p>${link}</div>`
    + tbl('Tools', ['Tool', 'Server', 'Calls', 'Refused', 'Failed', 'Gateway', 'Last seen', ''], tools.sort((x, y) => y.e.requests - x.e.requests).map(({e, t}) => `<tr><td class="mono">${esc(t.label)}</td><td class="mono">${esc(t.server || '')}</td><td class="mono">${num(e.requests)}</td><td class="mono ${e.denied ? 'amber' : ''}">${num(e.denied)}</td><td class="mono">${num(e.errors)}</td><td class="mono rc-small">${esc((e.gateways || []).join(', '))}</td><td class="rc-small">${fbTime(e.last)}</td><td>${flag(e, t)}</td></tr>`), 'checked against its MCP server\'s list and the Agent\'s toolNames')
    + tbl('Models', ['Model', 'Provider', 'Calls', 'Tokens in', 'Tokens out', 'Cost', 'Last seen'], models.map(({e, t}) => `<tr><td class="mono">${esc(t.label)}</td><td>${esc(t.provider || '—')}</td><td class="mono">${num(e.requests)}</td><td class="mono">${num(e.inputTokens)}</td><td class="mono">${num(e.outputTokens)}</td><td class="mono">${e.cost ? e.cost.toFixed(4) : '—'}</td><td class="rc-small">${fbTime(e.last)}</td></tr>`))
    + tbl('Agents it called (A2A)', ['Agent', 'Calls', 'Refused', 'Last seen'], agents.map(({e, t}) => `<tr><td class="mono">${esc(t.label)}</td><td class="mono">${num(e.requests)}</td><td class="mono">${num(e.denied)}</td><td class="rc-small">${fbTime(e.last)}</td></tr>`))
    + tbl('Refused', ['Target', 'Reason', 'Count', 'Gateway', 'What it means'], refusals.map(d => `<tr><td class="mono">${esc(d.target)}</td><td><span class="rc-badge amber">${esc(d.reason)}</span></td><td class="mono">${num(d.count)}</td><td class="mono rc-small">${esc(d.gateway)}</td><td class="rc-small">${esc(d.meaning || '')}</td></tr>`), 'the Refused tab on Traffic lists the policies behind each')
    + tbl('Other requests', ['Target', 'Kind', 'Calls', 'Failed'], other.map(e => { const t = targets.get(e.to); return `<tr><td class="mono">${esc(actTargetLabel(t))}</td><td>${esc(actKindName[t.kind])}</td><td class="mono">${num(e.requests)}</td><td class="mono">${num(e.errors)}</td></tr>`; }), 'MCP sessions (initialize, tools/list) and plain HTTP')
    ;
}

// Gateway findings that name this agent, with only its rows.
function rcGwFindings(a, act) {
  const me = rcName(a);
  const fs = (act.findings || []).map(f => ({...f, items: (f.items || []).filter(it => (it.note || '').includes(me) || (it.target || '').includes(me))})).filter(f => f.items.length);
  if (!fs.length) return '';
  S.reach.gwf ||= {fopen: {}};
  return `<section class="panel mt"><div class="panel-head"><h2>From the gateways</h2><small class="muted">agentgateway findings that name ${esc(me)}</small></div>${fbFindings(fs, S.reach.gwf, 'rcgw')}</section>`;
}


// ---------- tool access: what one caller may call on one MCP server ----------
// Read from the captured agentgateway allowlists (backend.mcp.authorization).
// A server with an allowlist allows a caller only the tools a clause names
// for its service account or JWT subject (or for any caller); a server with
// none allows every tool it offers.
function taWho(act, ids, ns, sa) {
  const callers = (act.callers || []).filter(c => ids.includes(c.id) || (ns && sa && (c.identity || '').endsWith(`/ns/${ns}/sa/${sa}`)));
  if (!sa) for (const c of callers) { const m = /\/ns\/([^/]+)\/sa\/([^/]+)$/.exec(c.identity || ''); if (m) { ns = m[1]; sa = m[2]; break; } }
  return {ns, sa, ids: [...new Set([...ids, ...callers.map(c => c.id)])], users: [...new Set(callers.flatMap(c => c.users || []))], label: callers[0]?.label || (ns && sa ? ns + '/' + sa : ids[0])};
}
function taServers(act, who) {
  const mine = t => taAllowed(t, who) || (t.configuredOn || []).includes(who.label);
  const called = new Set((act.edges || []).filter(e => who.ids.includes(e.from)).map(e => (act.targets || []).find(x => x.id === e.to)).filter(x => x?.kind === 'tool').map(x => x.server));
  return [...new Set((act.tools || []).filter(t => mine(t) || called.has(t.server)).map(t => t.server).concat([...called]))].filter(Boolean);
}
function taAllowed(t, who) {
  return (t.allowedFor || []).some(w => w === 'any caller' || (who.sa && (w === `${who.ns}/${who.sa}` || w === 'service account ' + who.sa)) || who.users.some(u => w === 'JWT subject ' + u));
}
function toolAccess(act, who, server) {
  const tools = (act.tools || []).filter(t => t.server === server);
  const si = (act.servers || []).find(x => x.name === server) || {};
  const guarded = tools.some(t => (t.allowedFor || []).length);
  const calls = new Map();
  for (const e of act.edges || []) {
    if (!who.ids.includes(e.from)) continue;
    const x = (act.targets || []).find(y => y.id === e.to);
    if (x?.kind !== 'tool' || !(x.server === server || (server.includes('/') && tools.some(t => t.name === x.label)))) continue;
    const c = calls.get(x.label) || {calls: 0, refused: 0};
    c.calls += e.requests; c.refused += e.denied; calls.set(x.label, c);
  }
  // Meta-tools the caller used on this server (Code and Search modes).
  const meta = {};
  for (const e of act.edges || []) {
    if (!who.ids.includes(e.from)) continue;
    const x = (act.targets || []).find(y => y.id === e.to);
    if (x?.kind === 'meta' && x.server.split(',').includes(server)) meta[x.label] = (meta[x.label] || 0) + e.requests;
  }
  const names = new Set([...tools.map(t => t.name), ...calls.keys()]);
  const rows = [...names].map(n => {
    const t = tools.find(x => x.name === n) || {name: n};
    const allowed = guarded ? taAllowed(t, who) : true;
    const c = calls.get(n) || {calls: 0, refused: 0};
    return {name: n, allowed, open: !guarded, viaCode: allowed && !c.calls && meta.run_code > 0, configured: (t.configuredOn || []).includes(who.label), listed: !!t.declared, calls: c.calls, refused: c.refused, policies: (t.policies || []).map(p => p.namespace + '/' + p.name), description: t.description || ''};
  }).filter(r => r.allowed || r.calls || r.configured);
  rows.sort((x, y) => (y.allowed - x.allowed) || (y.calls - x.calls) || x.name.localeCompare(y.name));
  const policies = [...new Set(tools.flatMap(t => (t.policies || []).map(p => p.namespace + '/' + p.name)))];
  const n = rows.filter(r => r.allowed).length;
  let note;
  if (guarded) note = n ? `${fbPlural(n, 'tool')}: what the allowlist ${policies.join(', ')} gives ${who.label}.` : `The allowlist ${policies.join(', ')} names no tool for ${who.label}, so it may call none.`;
  else if (si.hasList) note = `No agentgateway allowlist covers ${server}, so nothing enforces which tools ${who.label} calls: it can call all ${fbPlural(n, 'tool')} the server lists, whatever its own configuration says.`;
  else note = `No agentgateway allowlist covers ${server}, so nothing enforces which tools ${who.label} calls: it can call every tool the server offers, whatever its own configuration says. Its tool list is not in the capture (it is outside the cluster, and agentgateway does not log tools/list), so these ${fbPlural(n, 'tool')} are only the ones known from calls, other allowlists and agent configuration; it may offer more.`;
  if (meta.run_code) note += ` It called run_code ${fbPlural(meta.run_code, 'time')} (Code Mode): agentgateway checks each tool its code calls against the same allowlist, but does not log which, so every allowed tool it did not call directly may have run inside run_code.`;
  if (meta.get_tool) note += ` It looked tools up with get_tool ${fbPlural(meta.get_tool, 'time')} (Search mode); the logs do not say which.`;
  return {server, rows, guarded, allowedN: n, note, meta};
}
function taTable(acc) {
  const flag = r => r.viaCode ? '<span class="uz-st tried" title="Code Mode: its run_code calls may have used this tool; the logs do not say">allowed, may have run in run_code</span>' : !r.allowed && r.calls ? '<span class="rc-badge red" title="Called, although the captured allowlist does not name it for this caller: the policy may be newer than the calls">called, not allowed</span>'
    : r.open && r.configured && !r.calls ? '<span class="uz-st tried" title="In its own configuration; no allowlist enforces it">in its config, not enforced</span>' : r.open && !r.calls ? '<span class="uz-st unused">not in its config, not blocked</span>'
    : r.allowed && r.calls ? '<span class="uz-st used">allowed, called</span>' : r.allowed ? '<span class="uz-st unused">allowed, not called</span>' : '<span class="uz-st tried">configured, not allowed</span>';
  const rows = acc.rows.map(r => `<tr><td class="mono">${esc(r.name)}</td><td>${flag(r)}</td><td class="mono">${r.calls ? `${num(r.calls)}${r.refused ? ` <small class="amber">${num(r.refused)} refused</small>` : ''}` : '<span class="muted">—</span>'}</td>
    <td>${r.configured ? '<span class="green">yes</span>' : '<span class="muted">—</span>'}</td><td class="rc-small muted">${esc(r.description)}</td></tr>`).join('');
  const metaRows = Object.entries(acc.meta || {}).map(([k, n]) => `<tr><td class="mono">${esc(k)}</td><td><span class="uz-st used">${k === 'run_code' ? 'Code Mode entry point' : 'Search mode lookup'}</span></td><td class="mono">${num(n)}</td><td><span class="muted">—</span></td><td class="rc-small muted">${k === 'run_code' ? 'agentgateway gives Code Mode agents this one tool; the code it runs calls the tools below' : 'progressive disclosure: finds a tool and its schema before calling it'}</td></tr>`).join('');
  const rowsAll = metaRows + rows;
  return `<p class="rc-small ${acc.guarded ? '' : 'amber'}" style="margin:0 0 8px">${esc(acc.note)}</p>${rowsAll ? `<div class="table-scroll" style="max-height:50vh"><table><thead><tr><th>Tool</th><th>Access</th><th>Calls</th><th title="In the agent's own toolNames or ALLOWED_TOOLS">In its config</th><th>Description</th></tr></thead><tbody>${rowsAll}</tbody></table></div>` : ''}`;
}
function taPanel(act, who, servers, title) {
  if (!servers.length) return '';
  return `<section class="panel mt"><div class="panel-head"><h2>${esc(title)}</h2><small class="muted">from the captured agentgateway allowlists, the agent's configuration and its calls</small></div><div class="panel-body">
    ${servers.map(srv => { const acc = toolAccess(act, who, srv); const nc = acc.rows.filter(r => r.configured).length; return `<h3 style="margin:${servers.length > 1 ? '14px' : '0'} 0 6px"><span class="mono">${esc(srv)}</span> <small class="${acc.guarded ? 'muted' : 'amber'}">${acc.guarded ? fbPlural(acc.allowedN, 'tool') + ' allowed by an allowlist' : `no allowlist: ${nc} in its config, all ${acc.allowedN} callable`}</small></h3>${taTable(acc)}`; }).join('')}</div></section>`;
}


// ---------- Traffic: Found by ----------
// One menu for where what the map draws comes from, like the one on an
// agent's graph: ztunnel connections, agentgateway calls, and for tools,
// whether they were called, are allowed by an allowlist, or are configured
// on an agent. It sets the layer and the tools view.
const tfFoundKinds = [
  ['any', 'Any', 'everything: connections, gateway calls, and every tool'],
  ['ztunnel', 'ztunnel connections', 'the network layer: every connection ztunnel logged'],
  ['agentgateway', 'agentgateway calls', 'the gateway layer: a box per tool, model and agent called'],
  ['called', 'Tools called', 'tools that were called, behind the Service they went through'],
  ['allowed', 'Tools allowed', 'tools an allowlist allows (every tool of a server without one), called or not'],
  ['configured', 'Tools configured', "tools named in an agent's toolNames or ALLOWED_TOOLS"],
];
const tfFoundSet = {any: ['both', 'both'], ztunnel: ['network', 'both'], agentgateway: ['gateway', 'called'], called: ['both', 'called'], allowed: ['both', 'allowed'], configured: ['both', 'configured']};
function tfFoundMenu(d, a, hasNet, hasGw) {
  const T = S.tf, cur = T.found || 'any';
  const tools = a.tools || [];
  const guarded = new Set(tools.filter(t => (t.allowedFor || []).length).map(t => t.server));
  const n = {any: (d.edges || []).length + (a.edges || []).length, ztunnel: (d.edges || []).length, agentgateway: (a.edges || []).length,
    called: tools.filter(t => t.calls).length, allowed: tools.filter(t => guarded.has(t.server) ? (t.allowedFor || []).length : t.declared || t.calls).length, configured: tools.filter(t => (t.configuredOn || []).length).length};
  const ok = k => k === 'any' || (k === 'ztunnel' ? hasNet : k === 'agentgateway' ? hasGw : hasGw || tools.length) && n[k];
  const label = (tfFoundKinds.find(k => k[0] === cur) || [cur, cur])[1];
  const row = ([k, name, sub]) => `<button class="rc-fitem ${cur === k ? 'on' : ''}" data-tffound="${k}" ${ok(k) ? '' : 'disabled'}><span class="rc-fname">${esc(name)}</span><span class="rc-fn">${num(n[k])}</span><span class="rc-fsub muted">${esc(sub)}</span></button>`;
  const flows = Object.entries(S.reach.flowSrc || {}).filter(([k]) => !['ztunnel', 'agentgateway'].includes(k));
  return `<div class="rc-fmenu"><button class="chip ${cur !== 'any' ? 'on' : ''}" data-fmenu>Found by: ${esc(label)} ▾</button>
    <div class="rc-fpop" hidden><div class="rc-fhead">Traffic</div>${tfFoundKinds.slice(0, 3).map(row).join('')}<div class="rc-fhead">Tools</div>${tfFoundKinds.slice(3).map(row).join('')}
    ${flows.length ? `<div class="rc-fhead">Other flow sources</div>${flows.map(([k, v]) => `<div class="rc-fitem" style="cursor:default"><span class="rc-fname">${esc(k)}</span><span class="rc-fn">${num(v.flows ?? v)}</span><span class="rc-fsub muted">used by each agent's reach on the Agents tab, not drawn here</span></div>`).join('')}` : ''}</div></div>`;
}

// Every agent's reach findings, grouped by title so each is listed once with
// the agents it applies to. The fix YAML is per agent, on the Agents tab.
function tfAgentFindings(rep) {
  if (!rep) return [];
  const by = new Map();
  // Titles carry counts ("Can reach 57 of 58 Services"); group without them.
  const plain = t => { const x = t.replace(/\b(\d+) of \1 /g, 'every ').replace(/\b\d+ of \d+ /g, 'most ').replace(/\b\d+ /g, '').replace(/\(s\)/g, 's'); return x.charAt(0).toUpperCase() + x.slice(1); };
  const rank = {critical: 0, high: 1, medium: 2, low: 3};
  for (const ag of rep.agents || []) for (const f of ag.findings || []) {
    const k = plain(f.title);
    let g = by.get(k);
    if (g && (rank[f.severity] ?? 9) < (rank[g.severity] ?? 9)) g.severity = f.severity;
    if (!g) { g = {severity: f.severity, title: k, detail: String(f.detail || '').split(ag.name).join('the agent'), items: [], source: f.source, docs: f.docs, advice: ['Open an agent (click its name) for what the finding affects for it and the YAML that fixes it.'], agentFinding: true}; by.set(k, g); }
    g.items.push({target: ag.namespace + '/' + ag.name, class: rcTypes[ag.type] || ag.type, note: f.title});
  }
  // "every" and "most" of the Services are one finding.
  const merged = new Map();
  for (const g of by.values()) {
    const k = g.title.replace(/^Can reach (every|most) /, 'Can reach most or all ');
    const x = merged.get(k);
    if (x) { x.items.push(...g.items); if ((rank[g.severity] ?? 9) < (rank[x.severity] ?? 9)) x.severity = g.severity; } else merged.set(k, {...g, title: k, unit: 'agent'});
  }
  return [...merged.values()];
}
