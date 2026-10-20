'use strict';
/* Ambient v2: object pages for AuthorizationPolicy and HTTPRoute (mockups 04 and 05) and the Service Tests tab.
   Cases come from the saved or generated suite (api/ambient/suites); results from running it in the mesh. */

S.am2.obj = S.am2.obj || {key: '', suite: null, row: null, clients: [], source: '', samples: 200, busy: false, error: '', last: null, svcList: null};
const AM2_OTABS = ['summary', 'tests', 'explain', 'history'];
const AM2_BACKEND_COLOURS = ['var(--teal)', '#7a3fc0', '#c7ccd4', '#c2410c'];
const AM2_BACKEND_PALE = ['#9bd5dc', '#d4bff0', '#e4e7ec', '#f2c9b0'];

const am2ObjRef = obj => { const [cluster, kind, namespace, ...rest] = String(obj || '').split('/'); return {cluster, kind, namespace, name: rest.join('/')}; };
const am2ObjStr = r => [r.cluster, r.kind, r.namespace, r.name].join('/');

async function am2ObjLoad(route, force) {
  const O = S.am2.obj, ref = am2ObjRef(route.obj);
  if (O.key !== route.obj || force) {
    O.suite = await api('api/ambient/suites', {ref: route.obj});
    const q = await api('api/ambient/policies', {q: ref.name}).catch(() => ({rows: []}));
    O.row = (q.rows || []).find(r => r.ref.name === ref.name && r.kind === ref.kind && r.ref.namespace === ref.namespace) || null;
    const svc = O.suite.service || O.row?.svc;
    O.clients = svc ? ((await api('api/ambient/http-options', {cluster: ref.cluster, namespace: svc.namespace, name: svc.name}).catch(() => ({clients: []}))).clients || []) : [];
    O.conds = [];
    if (svc) { const inv = await api('api/ambient/investigate', {cluster: ref.cluster, kind: 'Service', namespace: svc.namespace, name: svc.name}).catch(() => ({steps: []})); O.conds = (inv.steps || []).filter(st => (st.id.startsWith('route-' + ref.namespace + '-' + ref.name) || st.id.startsWith('policy-status-' + ref.name + '-')) && /(Accepted|ResolvedRefs|WaypointAccepted)$/.test(st.id)).map(st => ({name: st.id.match(/(WaypointAccepted|ResolvedRefs|Accepted)$/)[1], ok: st.state === 'pass'})); }
    O.key = route.obj; O.error = '';
    O.last = O.suite.lastRun || null;
  }
  return O;
}

const am2ResById = (O, id) => (O.last?.results || []).find(r => r.id === id);
const am2HttpTone = status => (status && Number(status) >= 400 ? 'bad' : 'ok');
function am2Hdrs(c) {
  const h = Object.entries(c.request?.headers || {}).map(([k, v]) => `${k}: ${v}`).join(', ');
  const parts = [h || (c.request?.direct ? '' : 'no header'), c.request?.direct ? 'direct to pod' : ''].filter(Boolean);
  return parts.join(c.request?.direct && h ? ' · ' : '');
}
function am2Mark(res) { return !res ? '<span class="am2-muted">–</span>' : res.status === 'pass' ? '✓' : res.status === 'fail' ? '<span style="color:var(--am2-bad)">✕</span>' : res.status === 'skipped' ? '<span class="am2-muted">–</span>' : '<span style="color:var(--am2-warn)">!</span>'; }

// ---------- header ----------
function am2ObjHeader(route, O, ref) {
  const row = O.row, svc = O.suite.service || row?.svc, svcPath = svc ? am2SvcPath(ref.cluster, svc.namespace, svc.name) : '';
  const crumb = `<div class="am2-bc"><a href="${am2Hash({tab: ''})}">Istio Ambient</a> › <a href="${am2Hash({tab: ''})}">Services</a>${svc ? ` › <a href="${am2Hash({svc: svcPath})}">${esc(svc.namespace)}/${esc(svc.name)}</a> › <a href="${am2Hash({svc: svcPath, stab: 'configuration'})}">Configuration</a>` : ''} › <b>${esc(ref.name)}</b></div>`;
  const isAP = ref.kind === 'AuthorizationPolicy';
  const state = row ? Object.values(row.perCluster || {}).find(Boolean) : '';
  const stateLabel = (O.conds || []).length ? '' : ({applied: 'WaypointAccepted', accepted: 'Accepted', programmed: 'Programmed'}[row?.perCluster?.[ref.cluster]] || state || '');
  const condPills = (O.conds || []).map(c => am2Pill(c.ok ? 'ok' : 'warn', c.name)).join(' ');
  const rules = O.suite.explain?.rules?.length;
  const managed = row ? ({flightbay: 'Flightbay', helm: 'Helm', argocd: 'Argo CD', flux: 'Flux', kubectl: 'kubectl'}[row.managedBy] || 'unknown') : '';
  const svcName = svc ? `<b>${esc(svc.name)}</b>` : '';
  const meta = isAP
    ? `${row?.effect?.action ? am2Pill(row.effect.action === 'DENY' ? 'bad' : 'ok', row.effect.action) : ''} &nbsp;applies to ${row?.appliesTo?.phrase ? esc(row.appliesTo.phrase.split(' ')[0]) + ' ' + svcName : svcName} · enforced by <b>${esc(row?.enforcedBy || 'waypoint')}</b>${condPills ? ' · ' + condPills : stateLabel ? ' · ' + am2Pill('ok', stateLabel) : ''}${managed ? ' · managed by ' + esc(managed) : ''}`
    : `parent Service ${svcName}${O.suite.cases?.[0]?.request?.port ? ' :' + esc(O.suite.cases[0].request.port) : ''} · served by <b>${esc(row?.enforcedBy || 'waypoint')}</b>${condPills ? ' · ' + condPills : stateLabel ? ' · ' + am2Pill('ok', stateLabel) : ''}${rules ? ` · ${rules} ${rules === 1 ? 'rule' : 'rules'}` : ''}${managed ? ' · managed by ' + esc(managed) : ''}`;
  const owned = !!(S.ambient.tests || []).find(t => t.applyState === 'applied' && (t.created || []).some(o => o.name === ref.name));
  const edit = row?.editable ? `<a class="am2-btn" href="${am2Hash({svc: svcPath, stab: 'configuration', drawer: 'edit:' + row.editKey})}">${ref.kind === 'HTTPRoute' ? 'Edit split' : 'Edit'}</a>` : '';
  const caseCount = (O.suite.cases || []).length;
  const tab = (id, label, count) => `<a href="${am2Hash({obj: route.obj, otab: id === 'summary' ? '' : id})}" class="${(route.otab || 'summary') === id ? 'on' : ''}">${esc(label)}${count ? ` <span class="am2-c">${esc(count)}</span>` : ''}</a>`;
  return `${crumb}<div class="am2-card"><div class="am2-objhead"><span class="am2-kind">${esc(ref.kind)}</span><div style="flex:1"><h1 style="font-size:18px">${esc(ref.name)}</h1><div class="am2-meta">${meta}</div></div><button type="button" class="am2-btn sm0" data-am2-objyaml>YAML</button>${edit}${owned ? '<button type="button" class="am2-btn sm0" data-am2-objundo>Undo</button>' : ''}</div><div class="am2-tabs2">${tab('summary', 'Summary')}${tab('tests', 'Tests', caseCount)}${tab('explain', 'Explain a request')}${tab('history', 'History')}</div></div>`;
}

// ---------- AuthorizationPolicy tests (04) ----------
function am2RuleWords(text) {
  const m = String(text || '').match(/^(\w+)(.*)$/);
  return m ? `<b>${esc(m[1])}</b>${esc(m[2]).replace(/\b(header )(\S+)( equals )(\S+?)(\.?)$/, '$1<span class="am2-mono">$2</span>$3<span class="am2-mono">$4</span>$5')}` : esc(text);
}
function am2SourceChips(O) {
  const cur = O.source || (O.suite.source?.kind === 'workload' ? 'pod:' + O.suite.source.ref.name : O.suite.source?.kind === 'ephemeral' ? 'ephemeral' : O.suite.source?.kind === 'external' ? 'external' : '');
  const chip = (id, label, sub) => `<button type="button" class="am2-chip ${cur === id ? 'on' : ''}" data-am2-src="${esc(id)}">${esc(label)}${sub ? ` <span class="am2-sub">${esc(sub)}</span>` : ''}</button>`;
  return O.clients.map(c => chip('pod:' + c.pod.name, `${c.pod.namespace}/${c.pod.name.replace(/-[a-f0-9]{8,10}-[a-z0-9]{5}$/, '')}`, c.ambient ? 'in mesh' : '')).join('') + chip('ephemeral', 'Temporary pod') + chip('external', 'Outside the mesh');
}
function am2AuthzLeft(O, ref, route) {
  const ex = O.suite.explain || {}, svc = O.suite.service || {};
  const others = (ex.others || []).map(o => `<div class="am2-rule" style="display:flex;gap:8px;align-items:center">${am2Pill(o.action === 'DENY' ? 'bad' : o.action === 'ALLOW' ? 'ok' : 'info', o.action)}<span class="am2-mono">${esc(o.name)}</span><span class="am2-sub">${esc(o.summary)}</span></div>`).join('');
  const clusters = S.am2.data.summary.clusters.names, perCluster = O.row?.perCluster || {};
  return `<div style="display:flex;flex-direction:column;gap:16px"><div class="am2-card am2-pad"><h3>The rule, in words</h3><div class="am2-rule">${am2RuleWords(ex.inWords)}</div><div class="am2-sub" style="margin-top:8px">${esc(ex.where || '')}</div>${others ? `<h3 style="margin-top:14px">Other policies that also decide for ${esc(svc.name || 'this Service')}</h3>${others}` : ''}</div><div class="am2-card am2-pad"><h3>Send test requests from</h3><div class="am2-src">${am2SourceChips(O)}</div>${O.suite.source?.ref?.name && O.suite.source.kind === 'workload' ? `<div class="am2-sub" style="margin-top:8px">Identity used: <span class="am2-mono">spiffe://cluster.local/ns/${esc(O.suite.source.ref.namespace)}/sa/${esc(O.suite.source.ref.name.replace(/-[a-f0-9]{8,10}-[a-z0-9]{5}$/, ''))}</span></div>` : ''}<h3 style="margin-top:14px">Run on</h3><div class="am2-src">${clusters.map(c => `<button type="button" class="am2-chip ${c === ref.cluster ? 'on' : ''}" ${perCluster[c] === 'missing' ? 'disabled' : ''}>${esc(c)}${perCluster[c] === 'missing' ? ' <span class="am2-sub">(policy missing)</span>' : ''}</button>`).join('')}</div><div class="am2-callout i" style="margin-top:12px"><div>ℹ</div><div>Test cases are stored with the policy. They re-run automatically after any change to ${esc(svc.name || 'the Service')} from Flightbay, and on demand.</div></div></div></div>`;
}
function am2CaseRow(c, O) {
  const res = am2ResById(O, c.id), fail = res?.status === 'fail';
  const exp = c.expect?.status ? am2Pill(am2HttpTone(c.expect.status), c.expect.status) : `<span class="am2-sub">${esc(c.expect?.text || '')}</span>`;
  const got = res ? (res.got?.status ? am2Pill(am2HttpTone(res.got.status), res.got.status) : `<span class="am2-sub">${esc(res.got?.text || res.status)}</span>`) : '<span class="am2-muted">—</span>';
  const host = c.request?.direct ? 'pod-ip' + (c.request.port ? ':' + c.request.port : '') : '';
  return `<tr ${fail ? 'class="am2-failrow"' : ''}><td>${am2Mark(res)}</td><td class="am2-mono">${esc(c.request?.method || 'GET')} ${esc(host + (c.request?.path || '/'))}<div class="am2-sub">${esc(am2Hdrs(c))}</div></td><td class="am2-sub">${esc(c.why || c.label || '')}</td><td>${exp}</td><td>${got}</td><td class="am2-sub">${esc(res?.decidedBy || '')}</td><td class="am2-right"><button type="button" class="am2-btn sm g" data-am2-log='${esc(JSON.stringify(res?.logRef || {namespace: (O.suite.service || {}).namespace}))}'>Log</button></td></tr>`;
}
function am2LastRunLine(O) {
  const run = O.last, results = run?.results || [];
  if (!run || !results.length) return `Never run · ${(O.suite.cases || []).length} cases`;
  const ok = results.filter(r => r.status === 'pass').length;
  return `Last run ${am2Time(run.summary?.at || results[0]?.at)} · ${ok} of ${results.length} behaved as expected`;
}
function am2AuthzRight(O, ref, route) {
  const cases = O.suite.cases || [], fails = (O.last?.results || []).filter(r => r.status === 'fail');
  const callouts = fails.map(r => { const c = cases.find(x => x.id === r.id) || {}, msg = r.message || '', head = msg.split('. ')[0], rest = msg.split('. ').slice(1).join('. '); return `<div class="am2-callout w" style="margin-bottom:8px"><div>⚠</div><div><b>${esc(head)}.</b> ${esc(rest)} <a class="am2-ea2" href="${am2Hash({svc: am2SvcPath(ref.cluster, (O.suite.service || {}).namespace, (O.suite.service || {}).name), stab: 'configuration', drawer: 'edit:' + (O.row?.editKey || '')})}">Edit rule</a>${r.got?.status ? ` · <a class="am2-ea2" href="#" data-am2-markexp="${esc(r.id)}">Mark ${esc(r.got.status)} as expected</a>` : ''}</div></div>`; }).join('');
  const bypass = cases.find(c => c.id === 'bypass'), br = bypass && am2ResById(O, 'bypass');
  const svc = O.suite.service || {};
  const bypassNote = br?.status === 'pass' && bypass.expect?.status === 200 ? `<div class="am2-callout i"><div>ℹ</div><div>The bypass case passes because nothing blocks direct pod traffic. That is expected today, but it means this ${esc(O.row?.effect?.action || 'policy')} can be skipped. <a href="${am2Hash({svc: am2SvcPath(ref.cluster, svc.namespace, svc.name), stab: 'configuration', drawer: 'add:require-waypoint'})}" style="font-weight:600">Fix with "Block traffic that skips the waypoint"</a></div></div>` : '';
  return `<div class="am2-card" style="overflow:hidden"><div class="am2-pad" style="display:flex;align-items:center;gap:10px;border-bottom:1px solid var(--am2-line)"><div style="flex:1"><h2>Test cases</h2><div class="am2-sub">${esc(am2LastRunLine(O))}</div></div><button type="button" class="am2-btn sm0" data-am2-addcase>+ Add case</button><button type="button" class="am2-btn sm0" data-am2-regen>Generate from rule</button><button type="button" class="am2-btn p sm0" data-am2-runall ${O.busy ? 'disabled' : ''}>${O.busy ? 'Running…' : '▶ Run all'}</button></div><table><thead><tr><th style="width:30px"></th><th>Request</th><th>Why this case</th><th>Expect</th><th>Got</th><th>Decided by</th><th></th></tr></thead><tbody>${cases.map(c => am2CaseRow(c, O)).join('') || '<tr><td colspan="7" class="am2-empty">No cases yet. Use Generate from rule or Add case.</td></tr>'}</tbody></table><div class="am2-pad">${callouts}${bypassNote}${O.error ? `<div class="am2-callout e"><div>✕</div><div>${esc(O.error)}</div></div>` : ''}${(O.suite.notes || []).map(n => `<div class="am2-sub" style="margin-top:6px">${esc(n)}</div>`).join('')}</div></div>`;
}

// ---------- HTTPRoute tests (05) ----------
function am2RouteLeft(O, ref) {
  const cases = O.suite.cases || [], wc = cases.find(c => c.expect?.weights), res = wc && am2ResById(O, wc.id);
  const clusters = null;
  const matchTxt = (O.suite.explain?.rules?.[0]?.matches || ['PathPrefix /']).join(', ');
  const head = `<div class="am2-pad" style="display:flex;align-items:center;gap:10px;border-bottom:1px solid var(--am2-line)"><div style="flex:1"><h2>Rule ${wc?.rule || 1} · ${wc ? 'split' : 'route'}</h2><div class="am2-sub">Matches ${esc(matchTxt)}${res ? ` · ${esc(res.got?.samples || wc.expect.samples)} requests from ${esc(O.suite.source?.ref?.namespace || '')}/${esc((O.suite.source?.ref?.name || '').replace(/-[a-f0-9]{8,10}-[a-z0-9]{5}$/, ''))} · ${am2Time(res.at)}` : ''}</div></div><select class="am2-input" id="am2-samples" style="padding:4px 8px">${[50, 100, 200, 500].map(n => `<option value="${n}" ${n === (O.samples || 200) ? 'selected' : ''}>${n} requests</option>`).join('')}</select><button type="button" class="am2-btn p sm0" data-am2-runall ${O.busy ? 'disabled' : ''}>${O.busy ? 'Running…' : '▶ Run'}</button></div>`;
  if (!wc) return `<div class="am2-card" style="overflow:hidden">${head}<div class="am2-pad am2-muted">This rule has no weighted split to measure. Match cases are on the right.</div></div>`;
  const exp = wc.expect.weights, obs = res?.weights || [], names = Object.keys(exp);
  const rows = names.map((n, i) => {
    const o = obs.find(x => x.backend === n), e = exp[n], v = o?.observed ?? null;
    return `<div class="am2-rrow"><span><span class="am2-lg" style="background:${AM2_BACKEND_COLOURS[i % 4]}"></span><b>${esc(n)}</b></span><div><div class="am2-bar" style="height:7px;margin-bottom:3px"><i style="width:${e}%;background:${AM2_BACKEND_PALE[i % 4]}"></i></div><div class="am2-bar" style="height:7px">${v === null ? '' : `<i style="width:${v}%;background:${AM2_BACKEND_COLOURS[i % 4]}"></i>`}</div></div><span class="am2-right">${esc(e)}%</span><span class="am2-right"><b>${v === null ? '—' : esc(v) + '%'}</b></span>${o ? am2Pill(o.pass ? 'ok' : 'bad', o.pass ? '✓' : '✕') : '<span></span>'}</div>`;
  }).join('');
  const okAll = res && obs.every(o => o.pass), statuses = res?.got?.statuses ? Object.entries(res.got.statuses).map(([k, v]) => `${k} × ${v}`).join(' · ') : '';
  const msg = res?.message || '';
  return `<div class="am2-card" style="overflow:hidden">${head}<div class="am2-pad"><div class="am2-rrow" style="font-size:11px;color:var(--am2-muted);font-weight:600"><span>Backend</span><span>Expected vs observed</span><span class="am2-right">Expected</span><span class="am2-right">Observed</span><span></span></div>${rows}${res ? `<div class="am2-callout ${okAll ? 's' : 'e'}" style="margin-top:12px"><div>${okAll ? '✓' : '✕'}</div><div><b>${esc(msg.split('. ')[0])}.</b> ${esc(msg.split('. ').slice(1).join('. '))}</div></div><div class="am2-sub" style="margin-top:12px">Status codes: ${esc(statuses)}${res.got?.p50Ms ? ` · p50 ${res.got.p50Ms} ms · p95 ${res.got.p95Ms} ms` : ''}</div>` : '<div class="am2-sub" style="margin-top:12px">Not run yet. Run measures the split by sending requests and identifying the responding Pod.</div>'}${O.error ? `<div class="am2-callout e" style="margin-top:12px"><div>✕</div><div>${esc(O.error)}</div></div>` : ''}</div></div>`;
}
function am2RouteRight(O, ref) {
  const cases = (O.suite.cases || []).filter(c => !c.expect?.weights);
  const mrows = cases.map(c => { const res = am2ResById(O, c.id); return `<tr><td class="am2-mono">${esc(c.request?.method || 'GET')} ${esc(c.request?.host ? c.request.host + ':' + (c.request.port || '') : '')}${esc(c.request?.path || '/')}</td><td>${esc(c.expect?.text || '')}</td><td>${esc(res?.got?.text || '')}</td><td>${res ? am2Pill(res.status === 'pass' ? 'ok' : 'bad', res.status === 'pass' ? '✓' : '✕') : ''}</td></tr>`; }).join('');
  const checks = (O.suite.checks || []).map(c => {
    const tone = {pass: 'ok', warn: 'warn', info: 'mut', fail: 'bad'}[c.status] || 'mut', mark = {pass: '✓', warn: '!', info: '–', fail: '✕'}[c.status] || '–';
    const act = c.action ? `<button type="button" class="am2-btn sm" data-am2-chk='${esc(JSON.stringify(c.action))}'>${esc(c.action.label)}</button>` : '';
    return `<div class="am2-rrow" style="grid-template-columns:24px 1fr 90px">${am2Pill(tone, mark)}<span class="${c.status === 'info' ? 'am2-muted' : ''}">${esc(c.title)}</span><span class="am2-right ${c.count ? 'am2-sub' : ''}">${c.count ? esc(c.count) : act}</span></div>`;
  }).join('');
  return `<div style="display:flex;flex-direction:column;gap:16px"><div class="am2-card" style="overflow:hidden"><div class="am2-pad" style="display:flex;align-items:center;border-bottom:1px solid var(--am2-line)"><h2 style="flex:1">Match cases</h2><button type="button" class="am2-btn sm" data-am2-addcase>+ Add case</button></div><table><thead><tr><th>Request</th><th>Expect</th><th>Got</th><th></th></tr></thead><tbody>${mrows || '<tr><td colspan="4" class="am2-empty">No match cases.</td></tr>'}</tbody></table></div><div class="am2-card am2-pad"><h2>Other checks on this route</h2><div style="margin-top:8px">${checks}</div></div></div>`;
}

// ---------- Summary, Explain, History ----------
function am2ObjSummary(O, ref) {
  const ex = O.suite.explain || {}, row = O.row;
  const rules = (ex.rules || []).map(r => `<div class="am2-rule">Rule ${esc(r.rule)}: ${esc((r.matches || []).join(', '))} → ${(r.backends || []).map(b => `${esc(b.name)} (${esc(b.percent ?? b.weight)}%)`).join(', ')}</div>`).join('');
  const others = (ex.others || []).map(o => `<div class="am2-rule" style="display:flex;gap:8px;align-items:center">${am2Pill(o.action === 'DENY' ? 'bad' : o.action === 'ALLOW' ? 'ok' : 'info', o.action)}<span class="am2-mono">${esc(o.name)}</span><span class="am2-sub">${esc(o.summary)}</span></div>`).join('');
  const states = Object.entries(row?.perCluster || {}).map(([c, s]) => `<div class="am2-rrow" style="grid-template-columns:140px 1fr"><span>${esc(c)}</span><span>${am2Pill(...(AM2_STATE[s] || ['mut', s]))}</span></div>`).join('');
  return `<div class="am2-grid2"><div class="am2-card am2-pad"><h3>What it does</h3>${ex.inWords ? `<div class="am2-rule">${ref.kind === 'AuthorizationPolicy' ? am2RuleWords(ex.inWords) : esc(ex.inWords)}</div>` : ''}${rules}<div class="am2-sub" style="margin-top:8px">${esc(ex.where || '')}</div>${others ? `<h3 style="margin-top:14px">Other policies that also decide</h3>${others}` : ''}</div><div class="am2-card am2-pad"><h3>State on each cluster</h3><div style="margin-top:8px">${states || '<span class="am2-muted">Not captured.</span>'}</div><div style="margin-top:14px"><button type="button" class="am2-btn sm0" data-am2-objyaml>YAML</button></div></div></div>`;
}
function am2ObjExplain(O, ref) {
  const r = O.explain;
  return `<div class="am2-card am2-pad" style="max-width:760px"><h2>Explain a request</h2><div class="am2-sub" style="margin:4px 0 12px">Send one request through the mesh and see which rule decided it.</div><div style="display:grid;grid-template-columns:120px 1fr;gap:10px"><div><span class="am2-lbl">Method</span><select class="am2-input am2-full" id="am2-xm">${['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD'].map(m => `<option>${m}</option>`).join('')}</select></div><div><span class="am2-lbl">Path</span><input class="am2-input am2-full" id="am2-xp" value="${esc(r?.path || '/')}"></div><div style="grid-column:1/-1"><span class="am2-lbl">Header (optional)</span><div style="display:grid;grid-template-columns:1fr 1fr;gap:10px"><input class="am2-input" id="am2-xhn" placeholder="name, e.g. color"><input class="am2-input" id="am2-xhv" placeholder="value, e.g. red"></div></div></div><div style="margin-top:12px"><button type="button" class="am2-btn p sm0" data-am2-explain ${O.busy ? 'disabled' : ''}>Explain</button></div>${r ? `<div class="am2-callout ${r.status === 'pass' || r.got?.status ? 'i' : 'e'}" style="margin-top:14px"><div>ℹ</div><div><b>${esc(r.got?.text || r.status)}</b> · decided by ${esc(r.decidedBy || 'unknown')}${r.message ? '. ' + esc(r.message) : ''}</div></div>` : ''}${O.error ? `<div class="am2-callout e" style="margin-top:12px"><div>✕</div><div>${esc(O.error)}</div></div>` : ''}</div>`;
}
function am2ObjHistory(ref) {
  const rows = (S.ambient.tests || []).filter(t => (t.created || []).some(o => o.name === ref.name) || (t.modified || []).some(c => c.ref?.name === ref.name));
  return `<div class="am2-card" style="overflow:hidden"><table><thead><tr><th>When</th><th>State</th><th>Change</th></tr></thead><tbody>${rows.map(t => `<tr><td>${esc(new Date(t.applyFinished || t.validated).toLocaleString())}</td><td>${am2Pill(t.applyState === 'applied' ? 'ok' : t.applyState === 'failed' ? 'bad' : 'mut', {applied: 'Applied', removed: 'Undone', failed: 'Failed'}[t.applyState] || t.applyState)}</td><td>${esc((t.created || []).map(o => '+ ' + o.kind + ' ' + o.name).concat((t.modified || []).map(c => '~ ' + c.ref.kind + ' ' + c.ref.name)).join(', '))}</td></tr>`).join('') || '<tr><td colspan="3" class="am2-empty">Flightbay has not changed this object from this machine.</td></tr>'}</tbody></table></div>`;
}

async function am2ObjectPage(route, data) {
  const O = await am2ObjLoad(route), ref = am2ObjRef(route.obj), tab = AM2_OTABS.includes(route.otab) ? route.otab : 'summary';
  if (!S.ambient.tests?.length) S.ambient.tests = await api('api/ambient/tests').catch(() => []);
  let body;
  if (tab === 'tests') body = ref.kind === 'HTTPRoute' ? `<div class="am2-grid2">${am2RouteLeft(O, ref)}${am2RouteRight(O, ref)}</div>` : `<div class="am2-grid">${am2AuthzLeft(O, ref, route)}${am2AuthzRight(O, ref, route)}</div>`;
  else if (tab === 'explain') body = am2ObjExplain(O, ref);
  else if (tab === 'history') body = am2ObjHistory(ref);
  else body = am2ObjSummary(O, ref);
  return am2ObjHeader(route, O, ref) + body;
}

// ---------- Service Tests tab ----------
async function am2TestsTab(route, data, ref, ctx) {
  const L = await api('api/ambient/suites/service', ref);
  S.am2.obj.svcList = L;
  const rows = (L.suites || []).map(s => {
    const refStr = am2ObjStr(s.ref), run = s.lastRun, pass = s.passed, tot = s.cases;
    return `<tr><td><span class="am2-kind">${esc(s.kind)}</span> <b class="am2-mono">${esc(am2MidEllipsis(s.name, 40))}</b></td><td>${esc(tot)} cases</td><td class="am2-sub">${s.lastRun ? esc(new Date(s.lastRun).toLocaleTimeString('en-GB', {hour12: false})) : 'Never run'}</td><td>${s.lastRun ? am2Pill(s.failed ? 'bad' : 'ok', s.failed ? `${s.failed} failed` : `${pass ?? tot} passed`) : am2Pill('mut', 'Not run')}</td><td class="am2-right am2-act"><button type="button" class="am2-btn sm" data-am2-runsuite="${esc(refStr)}">Run</button> <a class="am2-btn sm" href="${am2Hash({obj: refStr, otab: 'tests'})}">Open</a></td></tr>`;
  }).join('');
  return `<div class="am2-card" style="overflow:hidden;border-radius:0 0 10px 10px"><div class="am2-pad" style="display:flex;align-items:center;gap:10px;border-bottom:1px solid var(--am2-line)"><div style="flex:1"><h2>Test suites</h2><div class="am2-sub">${esc(L.cases || 0)} cases across ${(L.suites || []).length} ${(L.suites || []).length === 1 ? 'suite' : 'suites'}${L.failed ? ` · ${L.failed} failing` : ''}</div></div><button type="button" class="am2-btn p sm0" data-am2-runservice ${(L.suites || []).length ? '' : 'disabled'}>▶ Run all</button></div>${rows ? `<table><thead><tr><th>Suite</th><th>Cases</th><th>Last run</th><th>Result</th><th></th></tr></thead><tbody>${rows}</tbody></table>` : '<div class="am2-empty">No suites yet. Suites are created when you add configuration, or when you open a policy or route and choose Generate.</div>'}</div>`;
}

// ---------- behaviour ----------
function am2CaseModal(O, onSave) {
  modal('Add a test case', `<div class="am2"><div style="display:grid;grid-template-columns:110px 1fr 110px;gap:10px"><div><span class="am2-lbl">Method</span><select class="am2-input am2-full" id="am2-cm">${['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD'].map(m => `<option>${m}</option>`).join('')}</select></div><div><span class="am2-lbl">Path</span><input class="am2-input am2-full" id="am2-cp" value="/"></div><div><span class="am2-lbl">Expect status</span><input class="am2-input am2-full" id="am2-ce" value="200"></div><div style="grid-column:1/3"><span class="am2-lbl">Header (optional)</span><div style="display:grid;grid-template-columns:1fr 1fr;gap:10px"><input class="am2-input" id="am2-chn" placeholder="name"><input class="am2-input" id="am2-chv" placeholder="value"></div></div><div><span class="am2-lbl">Why</span><input class="am2-input am2-full" id="am2-cw" placeholder="My case"></div></div><div style="margin-top:14px"><button type="button" class="am2-btn p sm0" id="am2-csave">Add case</button></div></div>`);
  $('#am2-csave').onclick = () => {
    const h = $('#am2-chn').value.trim(), expect = Number($('#am2-ce').value) || 200;
    onSave({id: 'u-' + Date.now().toString(36), label: 'Added case', generated: false, why: $('#am2-cw').value.trim() || 'Added case', request: {method: $('#am2-cm').value, path: $('#am2-cp').value || '/', headers: h ? {[h]: $('#am2-chv').value} : undefined, port: O.suite.cases?.[0]?.request?.port || 0, direct: false}, expect: {status: expect, text: String(expect)}});
    closeModal();
  };
}

async function am2ObjRun(route, ids, extra = {}) {
  const O = S.am2.obj, ref = am2ObjRef(route.obj);
  if (O.busy) return;
  O.busy = true; O.error = ''; render();
  try {
    if (!S.ambient.contexts) S.ambient.contexts = await api('api/ambient/contexts').catch(() => []);
    const context = amLiveContext(ref.cluster, S.ambient.contexts)?.name || '';
    const body = {ref: route.obj, all: !ids, cases: ids || undefined, context, save: true, ...extra};
    if (O.source) body.source = O.source === 'ephemeral' ? {kind: 'ephemeral', ref: {namespace: (O.suite.service || {}).namespace}} : O.source === 'external' ? {kind: 'external'} : {kind: 'workload', ref: (O.clients.find(c => 'pod:' + c.pod.name === O.source) || {}).pod};
    if (O.samples && ref.kind === 'HTTPRoute') body.samples = O.samples;
    const r = await apiPost('api/ambient/suites/run', body);
    O.last = r.lastRun || {results: r.results, summary: r.summary};
    if (r.suite) { O.suite = {...O.suite, ...r.suite, explain: O.suite.explain, checks: O.suite.checks || r.suite.checks}; }
    S.am2.data = null;
  } catch (e) { O.error = e.message; }
  O.busy = false; render();
}

function am2ObjectAfter(route) {
  const root = $('#am2-root');
  if (!root || !(route.obj || (route.svc && route.stab === 'tests'))) return;
  const O = S.am2.obj;
  if (root.dataset.obj) return;
  root.dataset.obj = '1';
  const save = async () => { await apiPost('api/ambient/suites', O.suite).catch(e => { O.error = e.message; }); };
  root.addEventListener('click', async e => {
    const t = e.target, ref = route.obj ? am2ObjRef(route.obj) : null;
    if (t.closest('[data-am2-objyaml]')) { openObject(ref ? {cluster: ref.cluster, kind: ref.kind, namespace: ref.namespace, name: ref.name, group: O.suite.target?.group, resource: O.suite.target?.resource} : {}, 'yaml'); return; }
    const src = t.closest('[data-am2-src]'); if (src) { O.source = src.dataset.am2Src; render(); return; }
    if (t.closest('[data-am2-runall]')) { am2ObjRun(route, null); return; }
    const mark = t.closest('[data-am2-markexp]');
    if (mark) { e.preventDefault(); const r = (O.last?.results || []).find(x => x.id === mark.dataset.am2Markexp), c = O.suite.cases.find(x => x.id === mark.dataset.am2Markexp); if (c && r?.got?.status) { c.expect = {status: r.got.status, text: String(r.got.status)}; c.generated = false; await save(); await am2ObjRun(route, null); } return; }
    if (t.closest('[data-am2-regen]')) { O.suite = {...(await api('api/ambient/suites', {ref: route.obj, regenerate: '1'})), lastRun: O.last}; await save(); render(); return; }
    if (t.closest('[data-am2-addcase]')) { am2CaseModal(O, async c => { O.suite.cases = [...(O.suite.cases || []), c]; await save(); render(); }); return; }
    const log = t.closest('[data-am2-log]'); if (log) { const l = JSON.parse(log.dataset.am2Log); openLogs({namespace: l.namespace || '', pod: l.pod || ''}); return; }
    const chk = t.closest('[data-am2-chk]');
    if (chk) { const a = JSON.parse(chk.dataset.am2Chk), svc = O.suite.service || {}; if (a.kind === 'copy-to-cluster') am2Go({svc: am2SvcPath(ref.cluster, svc.namespace, svc.name), stab: 'configuration', drawer: 'copy:' + a.cluster}); else if (a.kind === 'add-timeout') am2Go({svc: am2SvcPath(ref.cluster, svc.namespace, svc.name), stab: 'configuration', drawer: 'add:timeouts'}); return; }
    if (t.closest('[data-am2-explain]')) {
      const h = $('#am2-xhn').value.trim(), id = 'x-' + Date.now().toString(36), c = {id, label: 'Explain', generated: false, why: 'Explain a request', request: {method: $('#am2-xm').value, path: $('#am2-xp').value || '/', headers: h ? {[h]: $('#am2-xhv').value} : undefined, port: O.suite.cases?.[0]?.request?.port || 0, direct: false}, expect: {status: 200, text: '200'}};
      O.busy = true; O.error = ''; render();
      try { const ctxName = amLiveContext(ref.cluster, S.ambient.contexts || [])?.name || ''; const r = await apiPost('api/ambient/suites/run', {ref: route.obj, cases: [id], suite: {...O.suite, cases: [...O.suite.cases, c]}, context: ctxName, save: false}); O.explain = {...(r.results || [])[0], path: c.request.path}; } catch (err) { O.error = err.message; }
      O.busy = false; render(); return;
    }
    const rs = t.closest('[data-am2-runsuite]');
    if (rs) { try { await apiPost('api/ambient/suites/run', {ref: rs.dataset.am2Runsuite, all: true, context: amLiveContext(am2SvcRef(route.svc).cluster, S.ambient.contexts || [])?.name || '', save: true}); } catch (err) { toast(err.message); } render(); return; }
    if (t.closest('[data-am2-runservice]')) { for (const s of S.am2.obj.svcList?.suites || []) { try { await apiPost('api/ambient/suites/run', {ref: am2ObjStr(s.ref), all: true, context: amLiveContext(am2SvcRef(route.svc).cluster, S.ambient.contexts || [])?.name || '', save: true}); } catch (err) { toast(err.message); break; } } render(); return; }
    if (t.closest('[data-am2-objundo]')) {
      const rec = (S.ambient.tests || []).find(x => x.applyState === 'applied' && (x.created || []).some(o => o.name === ref.name));
      if (rec && confirm('Undo this change on ' + rec.context + '?')) { try { await apiPost('api/ambient/test', {operation: 'cleanup', context: rec.context, validation: rec.token}); S.ambient.tests = await api('api/ambient/tests'); S.am2.data = null; O.key = ''; toast('Change undone'); } catch (err) { toast(err.message); } render(); }
    }
  });
  const sm = $('#am2-samples'); if (sm) sm.onchange = () => { O.samples = Number(sm.value); };
}
AM2_AFTER.push(am2ObjectAfter);
