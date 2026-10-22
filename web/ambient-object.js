'use strict';
/* Ambient v2: object pages for AuthorizationPolicy and HTTPRoute (mockups 04 and 05) and the Service Tests tab.
   Cases come from the saved or generated suite (api/ambient/suites); runs stream from api/ambient/suites/run
   (ndjson) so bars and rows update as requests come back (REVIEW-2026-10-20 R6). "Try a request" sends one
   request and explains it (R7); History lists the object's revisions from the live mirror (R8). */

S.am2.obj = S.am2.obj || {key: '', suite: null, row: null, clients: [], source: '', samples: 200, busy: false, error: '', last: null, svcList: null, live: null, abort: null, tryReq: null, tryRes: null, tryBusy: false, revs: null, revKey: '', openRev: {}};
const AM2_OTABS = ['summary', 'tests', 'history'];
const AM2_BACKEND_COLOURS = ['var(--teal)', '#7a3fc0', '#c7ccd4', '#c2410c'];
const AM2_BACKEND_PALE = ['#9bd5dc', '#d4bff0', '#e4e7ec', '#f2c9b0'];
const AM2_REQ_METHODS = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD'];
const AM2_BODY_METHODS = ['POST', 'PUT', 'PATCH', 'DELETE'];

const am2ObjRef = obj => { const [cluster, kind, namespace, ...rest] = String(obj || '').split('/'); return {cluster, kind, namespace, name: rest.join('/')}; };
const am2ObjStr = r => [r.cluster, r.kind, r.namespace, r.name].join('/');
const am2PodShort = n => String(n || '').replace(/-[a-f0-9]{8,10}-[a-z0-9]{5}$/, '');

async function am2ObjLoad(route, force) {
  const O = S.am2.obj, ref = am2ObjRef(route.obj);
  if (O.key !== route.obj || force) {
    O.suite = await api('api/ambient/suites', {ref: route.obj});
    const q = await api('api/ambient/policies', {q: ref.name}).catch(() => ({rows: []}));
    O.row = (q.rows || []).find(r => r.ref.name === ref.name && r.kind === ref.kind && r.ref.namespace === ref.namespace) || null;
    const svc = O.suite.service || O.row?.svc;
    O.clients = svc ? ((await api('api/ambient/http-options', {cluster: ref.cluster, namespace: svc.namespace, name: svc.name}).catch(() => ({clients: []}))).clients || []) : [];
    O.conds = []; O.tasks = null;
    if (svc) {
      const inv = await api('api/ambient/investigate', {cluster: ref.cluster, kind: 'Service', namespace: svc.namespace, name: svc.name}).catch(() => ({steps: []}));
      O.conds = (inv.steps || []).filter(st => (st.id.startsWith('route-' + ref.namespace + '-' + ref.name) || st.id.startsWith('policy-status-' + ref.name + '-')) && /(Accepted|ResolvedRefs|WaypointAccepted)$/.test(st.id)).map(st => ({name: st.id.match(/(WaypointAccepted|ResolvedRefs|Accepted)$/)[1], ok: st.state === 'pass'}));
      const cfg = await api('api/ambient/service-config', {cluster: ref.cluster, namespace: svc.namespace, name: svc.name}).catch(() => null);
      O.tasks = cfg?.tasks || null;
    }
    O.key = route.obj; O.error = ''; O.live = null; O.tryRes = null; O.revs = null; O.revKey = '';
    O.last = O.suite.lastRun || null;
  }
  return O;
}

const am2ResById = (O, id) => (O.last?.results || []).find(r => r.id === id);
const am2HttpTone = status => (status && Number(status) >= 400 ? 'bad' : 'ok');
function am2Hdrs(c) {
  const h = Object.entries(c.request?.headers || {}).map(([k, v]) => `${k}: ${v}`).join(', ');
  const parts = [h || (c.request?.direct ? '' : 'no header'), c.request?.body ? 'with body' : '', c.request?.direct ? 'direct to pod' : '', c.from ? 'from ' + am2FromText(c.from) : ''].filter(Boolean);
  return parts.join(' · ');
}
const am2FromText = f => f?.workload ? `${f.workload.namespace}/${am2PodShort(f.workload.name)}` : f?.serviceAccount ? `${f.namespace || ''}/sa/${f.serviceAccount}` : f?.principal || f?.namespace || '';
function am2Mark(res, O, id) {
  if (O?.busy && O.live && !O.live.results[id]) return O.live.current === id ? '<span class="am2-spin" title="Running"></span>' : '<span class="am2-muted" title="Waiting">·</span>';
  if (!res) return '<span class="am2-muted">–</span>';
  return res.status === 'pass' ? '✓' : res.status === 'fail' ? '<span style="color:var(--am2-bad)">✕</span>' : res.status === 'skipped' ? '<span class="am2-muted" title="Skipped">–</span>' : `<span style="color:var(--am2-warn)" title="${esc(res.message || 'Could not tell')}">?</span>`;
}
const am2ObjSvcRow = (O, ref) => { const svc = O.suite?.service || O.row?.svc; return svc ? (S.am2.data?.services || []).find(r => r.ref.namespace === svc.namespace && r.ref.name === svc.name) : null; };
function am2ObjWaypoint(O, ref) {
  const row = am2ObjSvcRow(O, ref);
  return row?.perCluster?.[ref.cluster]?.waypoint || row?.waypoint || '';
}
const am2TaskAvailable = (O, id) => !O.tasks || (O.tasks.find(t => t.id === id)?.status || 'unavailable') !== 'unavailable';

// ---------- header ----------
function am2ObjHeader(route, O, ref) {
  const row = O.row, svc = O.suite.service || row?.svc, svcPath = svc ? am2SvcPath(ref.cluster, svc.namespace, svc.name) : '';
  const crumb = `<div class="am2-bc"><a href="${am2Hash({tab: ''})}">Istio Ambient</a> › <a href="${am2Hash({tab: ''})}">Services</a>${svc ? ` › <a href="${am2Hash({svc: svcPath})}">${esc(svc.namespace)}/${esc(svc.name)}</a> › <a href="${am2Hash({svc: svcPath, stab: 'configuration'})}">Configuration</a>` : ''} › <b>${esc(ref.name)}</b></div>`;
  const isAP = ref.kind === 'AuthorizationPolicy';
  const cs = row?.perCluster?.[ref.cluster];
  const stateLabel = (O.conds || []).length ? '' : (AM2_STATE[cs] ? AM2_STATE[cs][1] : '');
  const condPills = (O.conds || []).map(c => am2Pill(c.ok ? 'ok' : 'warn', c.name)).join(' ');
  const rules = O.suite.explain?.rules?.length;
  const managed = row ? (row.managedByLabel || row.managedBy || 'Unknown') : '';
  const svcName = svc ? `<b>${esc(svc.name)}</b>` : '';
  const wp = am2ObjWaypoint(O, ref), enforcer = row?.enforcedBy === 'waypoint' || !row?.enforcedBy ? (wp ? `waypoint <b class="am2-mono">${esc(wp)}</b>` : '<b>waypoint</b>') : `<b>${esc(row.enforcedBy)}</b>`;
  const statePill = condPills ? ' · ' + condPills : stateLabel ? ' · ' + am2Pill((AM2_STATE[cs] || ['ok'])[0], stateLabel) : '';
  const meta = isAP
    ? `${row?.effect?.action ? am2Pill(row.effect.action === 'DENY' ? 'bad' : 'ok', row.effect.action) : ''} &nbsp;applies to ${row?.appliesTo?.phrase ? esc(row.appliesTo.phrase.split(' ')[0]) + ' ' + svcName : svcName} on ${esc(ref.cluster)} · enforced by ${enforcer}${statePill}${managed ? ' · managed by ' + esc(managed) : ''}`
    : `parent Service ${svcName}${O.suite.cases?.[0]?.request?.port ? ' :' + esc(O.suite.cases[0].request.port) : ''} on ${esc(ref.cluster)} · served by ${enforcer}${statePill}${rules ? ` · ${rules} ${rules === 1 ? 'rule' : 'rules'}` : ''}${managed ? ' · managed by ' + esc(managed) : ''}`;
  const owned = !!(S.ambient.tests || []).find(t => t.applyState === 'applied' && (t.created || []).some(o => o.name === ref.name && (!o.namespace || o.namespace === ref.namespace) && (!o.kind || o.kind === ref.kind)));
  const edit = row?.editable ? `<a class="am2-btn" href="${am2Hash({svc: svcPath, stab: 'configuration', drawer: 'edit:' + row.editKey})}">${ref.kind === 'HTTPRoute' ? 'Edit split' : 'Edit'}</a>` : '';
  const path = svc ? `<a class="am2-btn" href="${am2Hash({tab: 'path', to: svcPath})}">Request path</a>` : '';
  const caseCount = (O.suite.cases || []).length;
  const otab = route.otab === 'explain' ? 'tests' : (route.otab || 'summary');
  const tab = (id, label, count) => `<a href="${am2Hash({obj: route.obj, otab: id === 'summary' ? '' : id})}" class="${otab === id ? 'on' : ''}">${esc(label)}${count ? ` <span class="am2-c">${esc(count)}</span>` : ''}</a>`;
  return `${crumb}<div class="am2-card"><div class="am2-objhead"><span class="am2-kind">${esc(ref.kind)}</span><div style="flex:1"><h1 style="font-size:18px">${esc(ref.name)}</h1><div class="am2-meta">${meta}</div></div><button type="button" class="am2-btn sm0" data-am2-objyaml>YAML</button>${path}${edit}${owned ? '<button type="button" class="am2-btn sm0" data-am2-objundo>Undo</button>' : ''}</div><div class="am2-tabs2">${tab('summary', 'Summary')}${tab('tests', 'Tests', caseCount)}${tab('history', 'History')}</div></div>`;
}

// ---------- shared request fields (case editor and Try a request) ----------
function am2ReqFields(p, req, O, opts = {}) {
  req = req || {};
  const [path, query] = String(req.path || '/').split('?');
  const hdrs = Object.entries(req.headers || {});
  if (!hdrs.length) hdrs.push(['', '']);
  const svc = O.suite.service || {};
  const hosts = (S.am2.data?.services || []).filter(r => r.ref.namespace === svc.namespace && r.ref.name !== svc.name).map(r => r.ref.name);
  const bodyOn = AM2_BODY_METHODS.includes(req.method || 'GET');
  return `<div class="am2-reqgrid"><div><span class="am2-lbl">Method</span><select class="am2-input am2-full" data-f="${p}method">${AM2_REQ_METHODS.map(m => `<option ${m === (req.method || 'GET') ? 'selected' : ''}>${m}</option>`).join('')}</select></div><div><span class="am2-lbl">Path</span><input class="am2-input am2-full am2-mono" data-f="${p}path" value="${esc(path || '/')}"></div><div><span class="am2-lbl">Query <span class="am2-sub">(optional)</span></span><input class="am2-input am2-full am2-mono" data-f="${p}query" placeholder="a=1&b=2" value="${esc(query || '')}"></div></div>
<div style="margin-top:10px"><span class="am2-lbl">Headers</span><div data-f="${p}headers">${hdrs.map(([k, v]) => am2HdrRow(k, v)).join('')}</div><a href="#" class="am2-sub" data-am2-addhdr>+ Add header</a></div>
${opts.compact ? '' : `<div class="am2-reqgrid am2-reqgrid2" style="margin-top:10px"><div><span class="am2-lbl">Send to</span><select class="am2-input am2-full" data-f="${p}host"><option value="">${esc(svc.name || 'this Service')} (this Service)</option>${hosts.map(h => `<option ${h === req.host ? 'selected' : ''}>${esc(h)}</option>`).join('')}</select></div><div><span class="am2-lbl">Port</span><input class="am2-input am2-full" type="number" data-f="${p}port" value="${esc(req.port || O.suite.cases?.[0]?.request?.port || '')}"></div><div style="align-self:end"><label class="am2-check"><input type="checkbox" data-f="${p}direct" ${req.direct ? 'checked' : ''}> Send directly to the Pod (skip the waypoint)</label></div></div>`}
<details style="margin-top:10px" ${req.body ? 'open' : ''} data-f="${p}bodybox" ${bodyOn ? '' : 'hidden'}><summary class="am2-sub">Request body</summary><textarea class="am2-input am2-full am2-mono" rows="4" data-f="${p}body" placeholder='{"example": true}'>${esc(req.body || '')}</textarea></details>`;
}
const am2HdrRow = (k, v) => `<div class="am2-hdrrow"><input class="am2-input am2-mono" placeholder="name" value="${esc(k)}" data-hk><input class="am2-input am2-mono" placeholder="value" value="${esc(v)}" data-hv><button type="button" class="am2-btn sm g" data-am2-rmhdr aria-label="Remove header">✕</button></div>`;

// Pure enough: read the request fields back out of a container.
function am2ReadReq(root, p, base) {
  const f = n => root.querySelector(`[data-f="${p}${n}"]`);
  const headers = {};
  root.querySelectorAll(`[data-f="${p}headers"] .am2-hdrrow`).forEach(r => { const k = r.querySelector('[data-hk]').value.trim(); if (k) headers[k] = r.querySelector('[data-hv]').value; });
  const method = f('method').value, q = f('query').value.trim().replace(/^\?/, '');
  const req = {method, path: (f('path').value.trim() || '/') + (q ? '?' + q : ''), headers: Object.keys(headers).length ? headers : undefined, port: Number(f('port')?.value) || base?.port || 0, direct: !!f('direct')?.checked};
  if (f('host')?.value) req.host = f('host').value;
  if (AM2_BODY_METHODS.includes(method) && f('body')?.value) req.body = f('body').value;
  return req;
}
function am2WireReqFields(root, p) {
  root.addEventListener('click', e => {
    if (e.target.closest('[data-am2-addhdr]')) { e.preventDefault(); root.querySelector(`[data-f="${p}headers"]`).insertAdjacentHTML('beforeend', am2HdrRow('', '')); }
    const rm = e.target.closest('[data-am2-rmhdr]'); if (rm) { e.preventDefault(); const rows = root.querySelectorAll(`[data-f="${p}headers"] .am2-hdrrow`); if (rows.length > 1) rm.closest('.am2-hdrrow').remove(); else rm.closest('.am2-hdrrow').querySelectorAll('input').forEach(i => { i.value = ''; }); }
  });
  root.addEventListener('change', e => { if (e.target.matches(`[data-f="${p}method"]`)) { const box = root.querySelector(`[data-f="${p}bodybox"]`); if (box) box.hidden = !AM2_BODY_METHODS.includes(e.target.value); } });
}

// ---------- AuthorizationPolicy tests (04) ----------
function am2RuleWords(text) {
  const m = String(text || '').match(/^(\w+)(.*)$/);
  return m ? `<b>${esc(m[1])}</b>${esc(m[2]).replace(/\b(header )(\S+)( equals )(\S+?)(\.?)$/, '$1<span class="am2-mono">$2</span>$3<span class="am2-mono">$4</span>$5')}` : esc(text);
}
function am2SourceChips(O) {
  const cur = O.source || (O.suite.source?.kind === 'workload' ? 'pod:' + O.suite.source.ref.name : O.suite.source?.kind === 'ephemeral' ? 'ephemeral' : O.suite.source?.kind === 'external' ? 'external' : '');
  const chip = (id, label, sub) => `<button type="button" class="am2-chip ${cur === id ? 'on' : ''}" data-am2-src="${esc(id)}">${esc(label)}${sub ? ` <span class="am2-sub">${esc(sub)}</span>` : ''}</button>`;
  return O.clients.map(c => chip('pod:' + c.pod.name, `${c.pod.namespace}/${am2PodShort(c.pod.name)}`, c.ambient ? 'in mesh' : '')).join('') + chip('ephemeral', 'Temporary pod') + chip('external', 'Outside the mesh');
}
function am2RunOn(O, ref) {
  const clusters = S.am2.data.summary.clusters.names, perCluster = O.row?.perCluster || {};
  return `<div class="am2-src">${clusters.map(c => { const missing = ['not-here', 'missing'].includes(perCluster[c]); return `<button type="button" class="am2-chip ${c === ref.cluster ? 'on' : ''}" ${missing ? 'disabled' : `data-am2-go="${esc(am2Hash({obj: am2ObjStr({...ref, cluster: c}), otab: 'tests'}))}"`}>${esc(c)}${missing ? ` <span class="am2-sub">(${ref.kind === 'HTTPRoute' ? 'route' : 'policy'} not here)</span>` : ''}</button>`; }).join('')}</div>`;
}
function am2AuthzLeft(O, ref) {
  const ex = O.suite.explain || {}, svc = O.suite.service || {};
  const others = (ex.others || []).map(o => `<div class="am2-rule" style="display:flex;gap:8px;align-items:center">${am2Pill(o.action === 'DENY' ? 'bad' : o.action === 'ALLOW' ? 'ok' : 'info', o.action)}<span class="am2-mono">${esc(o.name)}</span><span class="am2-sub">${esc(o.summary)}</span></div>`).join('');
  const src = O.suite.source?.kind === 'workload' && O.suite.source.ref?.name ? `<div class="am2-sub" style="margin-top:8px">Identity used: <span class="am2-mono">ns/${esc(O.suite.source.ref.namespace)}/sa/${esc(O.clients.find(c => c.pod.name === O.suite.source.ref.name)?.serviceAccount || am2PodShort(O.suite.source.ref.name))}</span></div>` : '';
  return `<div style="display:flex;flex-direction:column;gap:16px"><div class="am2-card am2-pad"><h3>The rule, in words</h3><div class="am2-rule">${am2RuleWords(ex.inWords)}</div><div class="am2-sub" style="margin-top:8px">${esc(ex.where || '')}</div><h3 style="margin-top:14px">Other policies that also decide for ${esc(svc.name || 'this Service')}</h3>${others || `<div class="am2-sub" style="margin-top:6px">None. This is the only policy that applies to ${esc(svc.name || 'this Service')} on ${esc(ref.cluster)}.</div>`}</div><div class="am2-card am2-pad"><h3>Send test requests from</h3><div class="am2-src">${am2SourceChips(O)}</div>${src}<h3 style="margin-top:14px">Run on</h3>${am2RunOn(O, ref)}<div class="am2-callout i" style="margin-top:12px"><div>ℹ</div><div>Test cases are stored with the policy. They re-run automatically after any change to ${esc(svc.name || 'the Service')} from Flightbay, and on demand.</div></div></div></div>`;
}
function am2CaseActs(c) {
  return `<span class="am2-caseacts"><button type="button" class="am2-btn sm g" data-am2-editcase="${esc(c.id)}" title="Edit">Edit</button><button type="button" class="am2-btn sm g" data-am2-dupcase="${esc(c.id)}" title="Duplicate">Duplicate</button>${c.generated ? '' : `<button type="button" class="am2-btn sm g" data-am2-delcase="${esc(c.id)}" title="Delete">Delete</button>`}</span>`;
}
// Where a case is sent, before the path: "reviews-v1:9080" for another Service, "pod-ip:9080" for a direct
// request, nothing for the Service under test. The drawer's results table uses it too.
function am2CaseHost(c) {
  const q = c?.request || {};
  return q.direct ? 'pod-ip' + (q.port ? ':' + q.port : '') : q.host ? q.host + (q.port ? ':' + q.port : '') : '';
}
function am2CaseRow(c, O) {
  const res = O.live?.results[c.id] || am2ResById(O, c.id), fail = res?.status === 'fail';
  const exp = c.expect?.status ? am2Pill(am2HttpTone(c.expect.status), c.expect.status) : `<span class="am2-sub">${esc(c.expect?.text || '')}</span>`;
  const got = res ? (res.got?.status ? am2Pill(am2HttpTone(res.got.status), res.got.status) : `<span class="am2-sub">${esc(res.got?.text || res.status)}</span>`) : '<span class="am2-muted">—</span>';
  const host = am2CaseHost(c);
  const decided = res ? (res.decidedBy ? esc(res.decidedBy) : `<span class="am2-muted" title="${esc(res.decisionNote || '')}">${esc(res.decisionNote ? 'Not attributed' : '')}</span>`) : '';
  return `<tr ${fail ? 'class="am2-failrow"' : ''}><td>${am2Mark(res, O, c.id)}</td><td class="am2-mono">${esc(c.request?.method || 'GET')} ${esc(host + (c.request?.path || '/'))}<div class="am2-sub">${esc(am2Hdrs(c))}</div></td><td class="am2-sub">${esc(c.why || c.label || '')}</td><td>${exp}</td><td>${got}</td><td class="am2-sub">${decided}</td><td class="am2-right am2-nowrap">${am2CaseActs(c)}<button type="button" class="am2-btn sm g" data-am2-log='${esc(JSON.stringify(res?.logRef || {namespace: (O.suite.service || {}).namespace}))}'>Log</button></td></tr>`;
}
function am2LastRunLine(O) {
  if (O.busy && O.live) return `Running ${Object.keys(O.live.results).length} of ${O.live.total} cases…`;
  const run = O.last, results = run?.results || [];
  if (!run || !results.length) return `Never run · ${(O.suite.cases || []).length} cases`;
  const ok = results.filter(r => r.status === 'pass').length;
  return `Last run ${am2Time(run.summary?.at || results[0]?.at)} · ${ok} of ${results.length} behaved as expected${run.summary?.cancelled ? ' · stopped early' : ''}`;
}
const am2RunBtn = (O, label) => O.busy ? '<button type="button" class="am2-btn d sm0" data-am2-stop>■ Stop</button>' : `<button type="button" class="am2-btn p sm0" data-am2-runall>${label}</button>`;
function am2AuthzRight(O, ref) {
  const cases = O.suite.cases || [], fails = O.busy ? [] : (O.last?.results || []).filter(r => r.status === 'fail');
  const svc = O.suite.service || {};
  const callouts = fails.map(r => { const msg = r.message || '', head = msg.split('. ')[0], rest = msg.split('. ').slice(1).join('. '); return `<div class="am2-callout w" style="margin-bottom:8px"><div>⚠</div><div><b>${esc(head.replace(/\.$/, ''))}.</b> ${esc(rest)} <a class="am2-ea2" href="${am2Hash({svc: am2SvcPath(ref.cluster, svc.namespace, svc.name), stab: 'configuration', drawer: 'edit:' + (O.row?.editKey || '')})}">Edit rule</a>${r.got?.status ? ` · <a class="am2-ea2" href="#" data-am2-markexp="${esc(r.id)}">Mark ${esc(r.got.status)} as expected</a>` : ''}</div></div>`; }).join('');
  const bypass = cases.find(c => c.id === 'bypass'), br = bypass && am2ResById(O, 'bypass');
  const canFix = am2TaskAvailable(O, 'require-waypoint');
  const bypassNote = !O.busy && br?.status === 'pass' && bypass.expect?.status === 200 ? `<div class="am2-callout i"><div>ℹ</div><div>The bypass case passes because nothing blocks direct pod traffic. That is expected today, but it means this ${esc(O.row?.effect?.action || 'policy')} can be skipped.${canFix ? ` <a href="${am2Hash({svc: am2SvcPath(ref.cluster, svc.namespace, svc.name), stab: 'configuration', drawer: 'add:require-waypoint'})}" style="font-weight:600">Fix with "Block traffic that skips the waypoint"</a>` : ` Flightbay cannot add "Block traffic that skips the waypoint" here: ${esc((O.tasks || []).find(t => t.id === 'require-waypoint')?.shortReason || (O.tasks || []).find(t => t.id === 'require-waypoint')?.reason || 'it is not available on this waypoint')}`}</div></div>` : '';
  return `<div style="display:flex;flex-direction:column;gap:16px">${am2TryPanel(O, ref)}<div class="am2-card" style="overflow:hidden"><div class="am2-pad" style="display:flex;align-items:center;gap:10px;border-bottom:1px solid var(--am2-line)"><div style="flex:1"><h2>Test cases</h2><div class="am2-sub" id="am2-runline">${esc(am2LastRunLine(O))}</div></div><button type="button" class="am2-btn sm0" data-am2-addcase ${O.busy ? 'disabled' : ''}>+ Add case</button><button type="button" class="am2-btn sm0" data-am2-regen ${O.busy ? 'disabled' : ''}>Generate from rule</button>${am2RunBtn(O, '▶ Run all')}</div><table><thead><tr><th style="width:30px"></th><th>Request</th><th>Why this case</th><th>Expect</th><th>Got</th><th>Decided by</th><th></th></tr></thead><tbody>${cases.map(c => am2CaseRow(c, O)).join('') || '<tr><td colspan="7" class="am2-empty">No cases yet. Use Generate from rule or Add case.</td></tr>'}</tbody></table><div class="am2-pad">${callouts}${bypassNote}${O.error ? `<div class="am2-callout e"><div>✕</div><div>${esc(O.error)}</div></div>` : ''}${(O.suite.notes || []).map(n => `<div class="am2-sub" style="margin-top:6px">${esc(n)}</div>`).join('')}</div></div></div>`;
}

// ---------- HTTPRoute tests (05) ----------
// Pure: observed percentages from cumulative backend counts.
function am2Observed(backends, completed) {
  const out = {};
  for (const [b, n] of Object.entries(backends || {})) out[b] = completed ? Math.round(n / completed * 1000) / 10 : 0;
  return out;
}
function am2RouteLeft(O, ref) {
  const cases = O.suite.cases || [], wc = cases.find(c => c.expect?.weights), res = wc && (O.live?.results[wc.id] || (!O.busy && am2ResById(O, wc.id)));
  const prog = wc && O.live?.samples[wc.id];
  const matchTxt = (O.suite.explain?.rules?.[0]?.matches || ['PathPrefix /']).join(', ');
  const srcName = `${O.suite.source?.ref?.namespace || ''}/${am2PodShort(O.suite.source?.ref?.name || '')}`;
  const sub = prog && !res ? `${prog.completed} / ${prog.requested} requests from ${esc(srcName)}…` : res ? `${esc(res.got?.samples || wc.expect.samples)} requests from ${esc(srcName)} · ${am2Time(res.at)}` : '';
  const head = `<div class="am2-pad" style="display:flex;align-items:center;gap:10px;border-bottom:1px solid var(--am2-line)"><div style="flex:1"><h2>Rule ${wc?.rule || 1} · ${wc ? 'split' : 'route'}</h2><div class="am2-sub">Matches ${esc(matchTxt)}${sub ? ' · ' + sub : ''}</div></div><select class="am2-input" id="am2-samples" style="padding:4px 8px" ${O.busy ? 'disabled' : ''}>${[50, 100, 200, 500].map(n => `<option value="${n}" ${n === (O.samples || 200) ? 'selected' : ''}>${n} requests</option>`).join('')}</select>${am2RunBtn(O, '▶ Run')}</div>`;
  if (!wc) return `<div class="am2-card" style="overflow:hidden">${head}<div class="am2-pad am2-muted">This rule has no weighted split to measure. Match cases are on the right.</div></div>`;
  const exp = wc.expect.weights, names = Object.keys(exp);
  const live = prog && !res ? am2Observed(prog.backends, prog.completed) : null;
  const obs = res?.weights || [];
  const rows = names.map((n, i) => {
    const o = obs.find(x => x.backend === n), e = exp[n], v = live ? (live[n] ?? 0) : (o?.observed ?? null);
    return `<div class="am2-rrow"><span><span class="am2-lg" style="background:${AM2_BACKEND_COLOURS[i % 4]}"></span><b>${esc(n)}</b></span><div><div class="am2-bar" style="height:7px;margin-bottom:3px"><i style="width:${e}%;background:${AM2_BACKEND_PALE[i % 4]}"></i></div><div class="am2-bar" style="height:7px">${v === null ? '' : `<i class="am2-grow" style="width:${v}%;background:${AM2_BACKEND_COLOURS[i % 4]}"></i>`}</div></div><span class="am2-right">${esc(e)}%</span><span class="am2-right"><b>${v === null ? '—' : esc(v) + '%'}</b></span>${o && !live ? am2Pill(o.pass ? 'ok' : 'bad', o.pass ? '✓' : '✕') : live ? `<span class="am2-sub">${esc(prog.backends?.[n] || 0)}</span>` : '<span></span>'}</div>`;
  }).join('');
  const other = live ? Object.keys(prog.backends || {}).filter(b => !names.includes(b)) : [];
  const okAll = res && obs.every(o => o.pass);
  const statuses = live ? Object.entries(prog.statuses || {}).map(([k, v]) => `${k} × ${v}`).join(' · ') : res?.got?.statuses ? Object.entries(res.got.statuses).map(([k, v]) => `${k} × ${v}`).join(' · ') : '';
  const p50 = live ? prog.p50Ms : res?.got?.p50Ms, p95 = live ? prog.p95Ms : res?.got?.p95Ms;
  const progBar = live ? `<div class="am2-bar" style="height:4px;margin-top:10px"><i class="am2-grow" style="width:${Math.round(prog.completed / Math.max(1, prog.requested) * 100)}%;background:var(--teal)"></i></div>` : '';
  const msg = res?.message || '';
  const verdict = res ? `<div class="am2-callout ${okAll ? 's' : 'e'}" style="margin-top:12px"><div>${okAll ? '✓' : '✕'}</div><div><b>${esc(msg.split('. ')[0].replace(/\.$/, ''))}.</b> ${esc(msg.split('. ').slice(1).join('. '))}</div></div>` : live ? '' : O.busy ? '<div class="am2-sub" style="margin-top:12px">Starting…</div>' : '<div class="am2-sub" style="margin-top:12px">Not run yet. Run measures the split by sending requests and identifying the responding Pod.</div>';
  return `<div class="am2-card" style="overflow:hidden">${head}<div class="am2-pad"><div class="am2-rrow" style="font-size:11px;color:var(--am2-muted);font-weight:600"><span>Backend</span><span>Expected vs observed</span><span class="am2-right">Expected</span><span class="am2-right">Observed</span><span></span></div>${rows}${other.length ? `<div class="am2-sub" style="margin-top:6px">Also answered: ${esc(other.map(b => b + ' ' + prog.backends[b]).join(', '))}</div>` : ''}${progBar}${verdict}${statuses || p50 ? `<div class="am2-sub" style="margin-top:12px">Status codes: ${esc(statuses)}${p50 ? ` · p50 ${p50} ms · p95 ${p95} ms` : ''}</div>` : ''}${O.error ? `<div class="am2-callout e" style="margin-top:12px"><div>✕</div><div>${esc(O.error)}</div></div>` : ''}</div></div>`;
}
function am2RouteRight(O, ref) {
  const cases = (O.suite.cases || []).filter(c => !c.expect?.weights);
  const mrows = cases.map(c => {
    const res = O.live?.results[c.id] || (!O.busy ? am2ResById(O, c.id) : null);
    const mark = O.busy && O.live && !O.live.results[c.id] ? am2Mark(null, O, c.id) : res ? (res.status === 'pass' ? am2Pill('ok', '✓') : res.status === 'fail' ? am2Pill('bad', '✕') : `<span title="${esc(res.message || '')}">${am2Pill('mut', '?')}</span>`) : '';
    const got = res ? `${esc(res.got?.text || '')}${res.status === 'unknown' && res.got?.rule ? ` <span class="am2-sub">(matches rule ${esc(res.got.rule)})</span>` : ''}` : '';
    return `<tr><td class="am2-mono">${esc(c.request?.method || 'GET')} ${esc(am2CaseHost(c))}${esc(c.request?.path || '/')}<div class="am2-sub">${esc(am2Hdrs(c) === 'no header' ? '' : am2Hdrs(c))}</div></td><td>${esc(c.expect?.text || '')}</td><td>${got}</td><td>${mark}</td><td class="am2-right am2-nowrap">${am2CaseActs(c)}</td></tr>`;
  }).join('');
  const checks = (O.suite.checks || []).map(c => {
    const tone = {pass: 'ok', warn: 'warn', info: 'mut', fail: 'bad'}[c.status] || 'mut', mark = {pass: '✓', warn: '!', info: '–', fail: '✕'}[c.status] || '–';
    const act = c.action ? `<button type="button" class="am2-btn sm" data-am2-chk='${esc(JSON.stringify(c.action))}'>${esc(c.action.label)}</button>` : '';
    return `<div class="am2-rrow" style="grid-template-columns:24px 1fr 90px">${am2Pill(tone, mark)}<span class="${c.status === 'info' ? 'am2-muted' : ''}">${esc(c.title)}</span><span class="am2-right ${c.count ? 'am2-sub' : ''}">${c.count ? esc(c.count) : act}</span></div>`;
  }).join('');
  return `<div style="display:flex;flex-direction:column;gap:16px">${am2TryPanel(O, ref)}<div class="am2-card" style="overflow:hidden"><div class="am2-pad" style="display:flex;align-items:center;border-bottom:1px solid var(--am2-line)"><h2 style="flex:1">Match cases</h2><button type="button" class="am2-btn sm" data-am2-addcase ${O.busy ? 'disabled' : ''}>+ Add case</button></div><table><thead><tr><th>Request</th><th>Expect</th><th>Got</th><th></th><th></th></tr></thead><tbody>${mrows || '<tr><td colspan="5" class="am2-empty">No match cases.</td></tr>'}</tbody></table></div><div class="am2-card am2-pad"><h2>Other checks on this route</h2><div style="margin-top:8px">${checks}</div><h3 style="margin-top:14px">Run on</h3>${am2RunOn(O, ref)}</div></div>`;
}

// ---------- Try a request (R7) ----------
function am2TryPanel(O, ref) {
  const r = O.tryRes;
  const src = O.source ? (O.source === 'ephemeral' ? 'a temporary Pod' : O.source === 'external' ? 'outside the mesh' : O.source.replace(/^pod:/, '').replace(/-[a-f0-9]{8,10}-[a-z0-9]{5}$/, '')) : O.suite.source?.ref?.name ? `${O.suite.source.ref.namespace}/${am2PodShort(O.suite.source.ref.name)}` : 'the suite source';
  return `<details class="am2-card am2-pad" id="am2-try" ${r || O.tryOpen ? 'open' : ''}><summary style="display:flex;align-items:center;gap:10px;cursor:pointer"><h2 style="flex:1">Try a request</h2><span class="am2-sub">Send one request from ${esc(src)} and see what decided it</span></summary><div id="am2-tryform" style="margin-top:10px">${am2ReqFields('t-', O.tryReq || {method: 'GET', path: '/'}, O, {compact: false})}</div><div style="margin-top:10px;display:flex;gap:8px;align-items:center"><button type="button" class="am2-btn p sm0" data-am2-try ${O.tryBusy || O.busy ? 'disabled' : ''}>${O.tryBusy ? 'Sending…' : 'Send'}</button>${r && !r.error ? '<button type="button" class="am2-btn sm0" data-am2-trysave>Save as a case</button>' : ''}</div>${r ? am2TryResult(r) : ''}</details>`;
}
function am2TryResult(r) {
  if (r.error) return `<div class="am2-callout e" style="margin-top:12px"><div>✕</div><div>${esc(r.error)}</div></div>`;
  const g = r.got || {};
  const head = `<div class="am2-tryhead">${g.status ? am2Pill(am2HttpTone(g.status), g.status) : am2Pill('bad', g.error ? 'No response' : '?')}<b>${esc(r.verdict || '')}</b><span class="am2-sub">${g.backend && !(g.status === 403 && r.decidedBy) ? 'answered by ' + esc(g.backend) + ' · ' : ''}${g.ms ? esc(g.ms) + ' ms · ' : ''}from ${esc(r.source || '')} on ${esc(r.cluster || '')}</span></div>`;
  const decided = `<div class="am2-sub" style="margin-top:6px">${r.decidedBy ? `Decided by <b>${esc(r.decidedBy)}</b>${r.decidedByHow ? ', ' + esc({'waypoint-log': 'read from the waypoint access log', log: 'read from the waypoint access log', prediction: 'predicted from the policies', predicted: 'predicted from the policies', reach: 'worked out from the captured configuration'}[r.decidedByHow] || r.decidedByHow) : ''}. ` : ''}${r.confirmedByLog ? 'The waypoint access log confirms it.' : r.note ? esc(r.note) : r.logAvailable ? 'No matching line in the waypoint access log yet.' : ''}${g.error ? ' ' + esc(g.error) : ''}</div>`;
  const route = r.route ? `<h3 style="margin-top:12px">Route ${esc(r.route.name || '')}</h3>${(r.route.rules || []).map(x => `<div class="am2-expl"><span>${x.matched ? '<b style="color:var(--green)">✓</b>' : x.unknown ? '<span class="am2-muted">?</span>' : '<span class="am2-muted">✕</span>'}</span><span>Rule ${esc(x.rule)} <span class="am2-mono am2-sub">${esc((x.matches || []).join(', '))}</span></span><span class="am2-sub">${esc(x.reason || '')}${x.matched && r.route.backend ? ' → ' + esc(r.route.backend) : ''}</span></div>`).join('')}${r.route.note ? `<div class="am2-sub">${esc(r.route.note)}</div>` : ''}` : '';
  const pols = (r.policies || []).length ? `<h3 style="margin-top:12px">Policies, in the order Istio checks them</h3>${r.policies.map(p => `<div class="am2-expl ${p.decided ? 'on' : ''}"><span>${p.matched ? '<b style="color:var(--amber)">●</b>' : p.unknown ? '<span class="am2-muted">?</span>' : '<span class="am2-muted">○</span>'}</span><span>${am2Pill(p.action === 'DENY' ? 'bad' : p.action === 'ALLOW' ? 'ok' : 'info', p.action)} <span class="am2-mono">${esc(p.namespace)}/${esc(p.name)}</span> <span class="am2-sub">${esc(p.scope || '')}${p.enforced === false ? ' · not enforced' : ''}</span></span><span class="am2-sub">${p.matched ? 'matches: ' : p.unknown ? 'cannot tell: ' : 'does not match: '}${esc(p.reason || '')}${p.decided ? ' · <b>decided</b>' : ''}</span></div>`).join('')}` : '<div class="am2-sub" style="margin-top:12px">No AuthorizationPolicy applies to this Service.</div>';
  return `<div class="am2-tryres">${head}${decided}${route}${pols}</div>`;
}

// ---------- Summary, History ----------
function am2ObjSummary(O, ref) {
  const ex = O.suite.explain || {}, row = O.row;
  const rules = (ex.rules || []).map(r => `<div class="am2-rule">Rule ${esc(r.rule)}: ${esc((r.matches || []).join(', '))} → ${(r.backends || []).map(b => `${esc(b.name)} (${esc(b.percent ?? b.weight)}%)`).join(', ')}</div>`).join('');
  const others = (ex.others || []).map(o => `<div class="am2-rule" style="display:flex;gap:8px;align-items:center">${am2Pill(o.action === 'DENY' ? 'bad' : o.action === 'ALLOW' ? 'ok' : 'info', o.action)}<span class="am2-mono">${esc(o.name)}</span><span class="am2-sub">${esc(o.summary)}</span></div>`).join('');
  const states = Object.entries(row?.perCluster || {}).map(([c, s]) => `<div class="am2-rrow" style="grid-template-columns:140px 1fr"><span>${esc(c)}</span><span title="${esc(row.perClusterDetail?.[c] || '')}">${am2Pill(...(AM2_STATE[s] || ['mut', s]))} <span class="am2-sub">${esc(row.perClusterDetail?.[c] || '')}</span></span></div>`).join('');
  return `<div class="am2-grid2"><div class="am2-card am2-pad"><h3>What it does</h3>${ex.inWords ? `<div class="am2-rule">${ref.kind === 'AuthorizationPolicy' ? am2RuleWords(ex.inWords) : esc(ex.inWords)}</div>` : ''}${rules}<div class="am2-sub" style="margin-top:8px">${esc(ex.where || '')}</div>${others ? `<h3 style="margin-top:14px">Other policies that also decide</h3>${others}` : ''}</div><div class="am2-card am2-pad"><h3>State on each cluster</h3><div style="margin-top:8px">${states || '<span class="am2-muted">Not captured.</span>'}</div><div style="margin-top:14px"><button type="button" class="am2-btn sm0" data-am2-objyaml>YAML</button></div></div></div>`;
}
const am2DiffHtml = d => esc(d || '').split('\n').map(l => `<span class="${l.startsWith('+') && !l.startsWith('+++') ? 'am2-dadd' : l.startsWith('-') && !l.startsWith('---') ? 'am2-ddel' : l.startsWith('@@') ? 'am2-dhunk' : ''}">${l}</span>`).join('\n');
async function am2ObjHistory(O, ref) {
  const key = am2ObjStr(ref);
  if (O.revKey !== key) {
    try { O.revs = await api('api/ambient/revisions', {cluster: ref.cluster, kind: ref.kind, namespace: ref.namespace, name: ref.name, group: O.suite.target?.group}); }
    catch (e) { O.revs = {error: e.status === 404 ? 'No revisions recorded for this object.' : e.message}; }
    O.revKey = key;
  }
  const R = O.revs || {};
  if (R.error) return `<div class="am2-card am2-pad am2-muted">${esc(R.error)}</div>`;
  const revs = R.revisions || [];
  const evWord = {added: 'Created', updated: 'Changed', deleted: 'Deleted'};
  const rows = revs.map((r, i) => {
    const fb = r.flightbayToken ? (R.flightbay || []).find(f => f.token === r.flightbayToken) : null;
    const open = O.openRev[i];
    const label = r.initial ? 'As first seen' : evWord[r.event] || r.event;
    return `<tr><td class="am2-nowrap">${esc(new Date(r.at).toLocaleString())}</td><td>${am2Pill(r.event === 'deleted' ? 'bad' : r.initial ? 'mut' : 'info', label)}</td><td class="am2-sub am2-mono">${r.generation ? 'generation ' + esc(r.generation) : ''}${r.resourceVersion ? ' · rv ' + esc(r.resourceVersion) : ''}</td><td>${fb ? `<span class="am2-sub">Applied by Flightbay from this machine (${esc(fb.context)})</span>` : ''}</td><td class="am2-right">${r.diff ? `<button type="button" class="am2-btn sm" data-am2-rev="${i}">${open ? 'Hide diff' : 'Diff to previous'}</button>` : ''} <button type="button" class="am2-btn sm g" data-am2-revyaml="${i}">${open === 'yaml' ? 'Hide YAML' : 'YAML'}</button></td></tr>${open ? `<tr><td colspan="5"><pre class="am2-diff">${open === 'yaml' ? esc(r.yaml || '') : am2DiffHtml(r.diff)}</pre></td></tr>` : ''}`;
  }).join('');
  const head = R.mode === 'snapshot' ? 'This is a snapshot, so only the captured revision is known. Open the clusters live to follow changes.' : 'Every change the live mirror has seen to this object since Flightbay connected, newest first. Status-only updates are left out.';
  const credited = new Set(revs.map(r => r.flightbayToken).filter(Boolean));
  const fbRows = (R.flightbay || []).filter(f => !credited.has(f.token)).map(f => `<tr><td class="am2-nowrap">${esc(f.at ? new Date(f.at).toLocaleString() : '')}</td><td>${am2Pill(f.applyState === 'applied' ? 'tl' : 'mut', {applied: 'Applied', removed: 'Undone', failed: 'Failed'}[f.applyState] || f.applyState)}</td><td class="am2-sub">${esc({created: 'Created', modified: 'Changed', deleted: 'Deleted'}[f.op] || f.op)} by Flightbay from this machine</td><td class="am2-sub">${esc(f.context)}</td><td></td></tr>`).join('');
  return `<div class="am2-card" style="overflow:hidden"><div class="am2-pad am2-sub" style="border-bottom:1px solid var(--am2-line)">${esc(head)}</div><table><thead><tr><th>When</th><th>What</th><th>Version</th><th></th><th></th></tr></thead><tbody>${rows || '<tr><td colspan="5" class="am2-empty">No revisions recorded.</td></tr>'}${fbRows ? `<tr class="am2-grp"><td colspan="5">Before Flightbay connected: changes it made from this machine</td></tr>${fbRows}` : ''}</tbody></table></div>`;
}

function am2ObjTestsBody(O, ref) {
  return ref.kind === 'HTTPRoute' ? `<div class="am2-grid2">${am2RouteLeft(O, ref)}${am2RouteRight(O, ref)}</div>` : `<div class="am2-grid">${am2AuthzLeft(O, ref)}${am2AuthzRight(O, ref)}</div>`;
}

async function am2ObjectPage(route, data) {
  const O = await am2ObjLoad(route), ref = am2ObjRef(route.obj), tab = route.otab === 'explain' ? 'tests' : AM2_OTABS.includes(route.otab) ? route.otab : 'summary';
  if (!S.ambient.tests?.length) S.ambient.tests = await api('api/ambient/tests').catch(() => []);
  let body;
  if (tab === 'tests') body = am2ObjTestsBody(O, ref);
  else if (tab === 'history') body = await am2ObjHistory(O, ref);
  else body = am2ObjSummary(O, ref);
  return am2ObjHeader(route, O, ref) + `<div id="am2-objbody">${body}</div>`;
}

// Repaint only the tests body while a run streams, keeping the Try-a-request fields as typed.
let am2PaintQueued = false;
function am2ObjPaint(route) {
  if (am2PaintQueued) return;
  am2PaintQueued = true;
  requestAnimationFrame(() => {
    am2PaintQueued = false;
    const O = S.am2.obj, el = $('#am2-objbody'), ref = am2ObjRef(route.obj);
    if (!el || !route.obj || ((route.otab || 'summary') !== 'tests' && route.otab !== 'explain')) return;
    const tf = $('#am2-tryform'); if (tf) O.tryReq = am2ReadReq(tf, 't-', O.tryReq);
    el.innerHTML = am2ObjTestsBody(O, ref);
    am2ObjWireBody(route);
  });
}

// ---------- Service Tests tab ----------
async function am2TestsTab(route, data, ref, ctx) {
  const L = await api('api/ambient/suites/service', ref);
  S.am2.obj.svcList = L;
  const rows = (L.suites || []).map(s => {
    const refStr = am2ObjStr(s.ref), pass = s.passed, tot = s.cases;
    return `<tr><td><span class="am2-kind">${esc(s.kind)}</span> <b class="am2-mono">${esc(am2MidEllipsis(s.name, 40))}</b></td><td>${esc(tot)} cases</td><td class="am2-sub">${s.lastRun ? esc(new Date(s.lastRun).toLocaleTimeString('en-GB', {hour12: false})) : 'Never run'}</td><td>${s.lastRun ? am2Pill(s.failed ? 'bad' : 'ok', s.failed ? `${s.failed} failed` : `${pass ?? tot} passed`) : am2Pill('mut', 'Not run')}</td><td class="am2-right am2-act"><button type="button" class="am2-btn sm" data-am2-runsuite="${esc(refStr)}">Run</button> <a class="am2-btn sm" href="${am2Hash({obj: refStr, otab: 'tests'})}">Open</a></td></tr>`;
  }).join('');
  return `<div class="am2-card" style="overflow:hidden;border-radius:0 0 10px 10px"><div class="am2-pad" style="display:flex;align-items:center;gap:10px;border-bottom:1px solid var(--am2-line)"><div style="flex:1"><h2>Test suites</h2><div class="am2-sub">${esc(L.cases || 0)} cases across ${(L.suites || []).length} ${(L.suites || []).length === 1 ? 'suite' : 'suites'}${L.failed ? ` · ${L.failed} failing` : ''}</div></div><button type="button" class="am2-btn p sm0" data-am2-runservice ${(L.suites || []).length ? '' : 'disabled'}>▶ Run all</button></div>${rows ? `<table><thead><tr><th>Suite</th><th>Cases</th><th>Last run</th><th>Result</th><th></th></tr></thead><tbody>${rows}</tbody></table>` : '<div class="am2-empty">No suites yet. Suites are created when you add configuration, or when you open a policy or route and choose Generate.</div>'}</div>`;
}

// ---------- case editor (R6.1): a side panel ----------
function am2CasePanel(O, ref, existing, onSave) {
  const c = existing ? JSON.parse(JSON.stringify(existing)) : {id: '', why: '', request: {method: 'GET', path: '/', port: O.suite.cases?.[0]?.request?.port || 0}, expect: {status: 200}};
  const isRoute = ref.kind === 'HTTPRoute', rules = O.suite.explain?.rules || [];
  const backends = [...new Set(rules.flatMap(r => (r.backends || []).map(b => b.name)))];
  const e = c.expect || {}, kind = e.transport ? 'transport' : e.weights ? 'weights' : e.notRoute ? 'notRoute' : e.rule ? 'rule' : e.backend || e.backends?.length ? 'backend' : 'status';
  const fromVal = c.from?.workload ? 'pod:' + c.from.workload.name : c.from?.serviceAccount ? 'sa' : '';
  const weights = e.weights || Object.fromEntries((rules[0]?.backends || []).map(b => [b.name, b.percent ?? b.weight]));
  const host = document.createElement('div');
  host.className = 'am2-sidepanel-host';
  host.innerHTML = `<div class="am2-sidepanel-back" data-cp-close></div><aside class="am2-sidepanel am2" role="dialog" aria-label="${existing ? 'Edit' : 'Add'} a test case"><div class="am2-sp-head"><h2>${existing ? 'Edit test case' : 'Add a test case'}</h2><button type="button" class="am2-btn sm g" data-cp-close aria-label="Close">✕</button></div><div class="am2-sp-body">
<h3>Request</h3>${am2ReqFields('c-', c.request, O)}
<h3 style="margin-top:16px">From</h3><select class="am2-input am2-full" data-cp="from"><option value="">The source chosen on the page</option>${O.clients.map(cl => `<option value="pod:${esc(cl.pod.name)}" ${fromVal === 'pod:' + cl.pod.name ? 'selected' : ''}>${esc(cl.pod.namespace)}/${esc(am2PodShort(cl.pod.name))}${cl.ambient ? ' (in mesh)' : ''}</option>`).join('')}<option value="sa" ${fromVal === 'sa' ? 'selected' : ''}>A client running as a Service account…</option></select><div class="am2-reqgrid" data-cp="sabox" style="margin-top:8px" ${fromVal === 'sa' ? '' : 'hidden'}><div><span class="am2-lbl">Namespace</span><input class="am2-input am2-full" data-cp="sans" value="${esc(c.from?.namespace || '')}"></div><div><span class="am2-lbl">Service account</span><input class="am2-input am2-full" data-cp="saname" value="${esc(c.from?.serviceAccount || '')}"></div></div><div class="am2-sub" style="margin-top:4px">If no running client has that identity, the case is skipped rather than sent from the wrong place.</div>
<h3 style="margin-top:16px">Expect</h3><div class="am2-expkinds">${[['status', 'A status code'],['transport','Connection refused or reset (curl 000)'], ...(isRoute ? [['backend', 'A backend answers'], ['rule', 'A rule matches'], ['notRoute', 'Not this route'], ['weights', 'A traffic split']] : [])].map(([k, l]) => `<label class="am2-check"><input type="radio" name="am2-expk" value="${k}" ${k === kind ? 'checked' : ''}> ${l}</label>`).join('')}</div>
<div data-exp="status" ${kind === 'status' ? '' : 'hidden'}><input class="am2-input" type="number" data-cp="status" value="${esc(e.status || 200)}" style="width:120px"></div>
<div data-exp="backend" ${kind === 'backend' ? '' : 'hidden'}><select class="am2-input" data-cp="backend">${backends.map(b => `<option ${b === (e.backend || e.backends?.[0]) ? 'selected' : ''}>${esc(b)}</option>`).join('')}</select></div>
<div data-exp="rule" ${kind === 'rule' ? '' : 'hidden'}><select class="am2-input" data-cp="rule">${rules.map(r => `<option value="${esc(r.rule)}" ${Number(r.rule) === Number(e.rule) ? 'selected' : ''}>Rule ${esc(r.rule)}: ${esc((r.matches || []).join(', '))}</option>`).join('')}</select></div>
<div data-exp="notRoute" ${kind === 'notRoute' ? '' : 'hidden'} class="am2-sub">The request should not be handled by this route.</div>
<div data-exp="weights" ${kind === 'weights' ? '' : 'hidden'}>${Object.entries(weights).map(([b, w]) => `<div class="am2-hdrrow"><span class="am2-mono">${esc(b)}</span><input class="am2-input" type="number" data-cpw="${esc(b)}" value="${esc(w)}" style="width:90px"><span class="am2-sub">%</span></div>`).join('')}<div class="am2-reqgrid" style="margin-top:6px"><div><span class="am2-lbl">Tolerance ±%</span><input class="am2-input am2-full" type="number" data-cp="tol" value="${esc(e.tolerance || 5)}"></div><div><span class="am2-lbl">Requests</span><input class="am2-input am2-full" type="number" data-cp="samples" value="${esc(e.samples || 200)}"></div></div></div>
<h3 style="margin-top:16px">Why</h3><input class="am2-input am2-full" data-cp="why" placeholder="What this case proves" value="${esc(c.why || '')}">
<div class="am2-callout e" data-cp="err" hidden style="margin-top:12px"></div>
</div><div class="am2-sp-foot"><button type="button" class="am2-btn sm0" data-cp-close>Cancel</button><button type="button" class="am2-btn p sm0" data-cp-save>${existing ? 'Save case' : 'Add case'}</button></div></aside>`;
  document.body.appendChild(host);
  const q = s => host.querySelector(`[data-cp="${s}"]`);
  am2WireReqFields(host, 'c-');
  const close = () => { host.remove(); document.removeEventListener('keydown', esc_); };
  const esc_ = ev => { if (ev.key === 'Escape') close(); };
  document.addEventListener('keydown', esc_);
  host.addEventListener('change', ev => {
    if (ev.target.name === 'am2-expk') host.querySelectorAll('[data-exp]').forEach(d => { d.hidden = d.dataset.exp !== ev.target.value; });
    if (ev.target === q('from')) q('sabox').hidden = ev.target.value !== 'sa';
  });
  host.addEventListener('click', async ev => {
    if (ev.target.closest('[data-cp-close]')) { close(); return; }
    if (!ev.target.closest('[data-cp-save]')) return;
    const req = am2ReadReq(host, 'c-', c.request), k = host.querySelector('input[name="am2-expk"]:checked').value;
    const expect = k === 'transport' ? {transport:'connection-refused-or-reset'} : k === 'status' ? {status: Number(q('status').value) || 200} : k === 'backend' ? {backend: q('backend').value} : k === 'rule' ? {rule: Number(q('rule').value) || 1} : k === 'notRoute' ? {notRoute: true} : {weights: Object.fromEntries([...host.querySelectorAll('[data-cpw]')].map(i => [i.dataset.cpw, Number(i.value) || 0])), tolerance: Number(q('tol').value) || 5, samples: Number(q('samples').value) || 200};
    if (expect.weights && Object.values(expect.weights).reduce((a, b) => a + b, 0) !== 100) { const er = q('err'); er.hidden = false; er.textContent = 'The weights must add up to 100.'; return; }
    expect.text = expect.transport ? 'connection refused or reset (curl 000)' : expect.status ? String(expect.status) : expect.backend ? 'backend ' + expect.backend : expect.rule ? 'rule ' + expect.rule : expect.notRoute ? 'not this route' : 'split';
    const fv = q('from').value;
    const from = fv.startsWith('pod:') ? (() => { const cl = O.clients.find(x => 'pod:' + x.pod.name === fv); return cl ? {namespace: cl.pod.namespace, workload: cl.pod} : undefined; })() : fv === 'sa' && q('saname').value.trim() ? {namespace: q('sans').value.trim(), serviceAccount: q('saname').value.trim()} : undefined;
    const out = {...c, id: c.id || 'u-' + Date.now().toString(36), label: c.label || 'Added case', generated: false, why: q('why').value.trim() || 'Added case', request: req, expect, from};
    try { await onSave(out); close(); } catch (err) { const er = q('err'); er.hidden = false; er.textContent = err.message; }
  });
  setTimeout(() => host.querySelector('[data-f="c-path"]')?.focus(), 0);
}

// ---------- running ----------
function am2ObjSourceBody(O) {
  if (!O.source) return undefined;
  if (O.source === 'ephemeral') return {kind: 'ephemeral', ref: {namespace: (O.suite.service || {}).namespace}};
  if (O.source === 'external') return {kind: 'external'};
  return {kind: 'workload', ref: (O.clients.find(c => 'pod:' + c.pod.name === O.source) || {}).pod};
}
async function am2ObjContext(cluster) {
  if (!S.ambient.contexts) S.ambient.contexts = await api('api/ambient/contexts').catch(() => []);
  return amLiveContext(cluster, S.ambient.contexts)?.name || '';
}

// Pure: fold one ndjson event into the live state.
function am2RunEvent(live, ev) {
  if (ev.type === 'start') { live.total = ev.total; live.cases = ev.cases || []; }
  else if (ev.type === 'case-start') live.current = ev.id;
  else if (ev.type === 'sample') live.samples[ev.id] = ev;
  else if (ev.type === 'case-done') { live.results[ev.id] = ev.result; if (live.current === ev.id) live.current = ''; }
  else if (ev.type === 'done') live.done = ev;
  else if (ev.type === 'error') live.error = ev.error;
  return live;
}

async function am2ObjRun(route, ids, extra = {}) {
  const O = S.am2.obj, ref = am2ObjRef(route.obj);
  if (O.busy) return;
  O.busy = true; O.error = ''; O.live = {total: 0, cases: [], current: '', samples: {}, results: {}, done: null, error: ''}; O.abort = new AbortController();
  am2ObjPaint(route);
  try {
    const body = {ref: route.obj, all: !ids, cases: ids || undefined, context: await am2ObjContext(ref.cluster), save: true, ...extra};
    const src = am2ObjSourceBody(O); if (src) body.source = src;
    if (O.samples && ref.kind === 'HTTPRoute') body.samples = O.samples;
    const res = await fetch('api/ambient/suites/run', {method: 'POST', credentials: 'same-origin', headers: {'Content-Type': 'application/json', Accept: 'application/x-ndjson'}, body: JSON.stringify(body), signal: O.abort.signal});
    if (!res.ok) { const er = await res.json().catch(() => ({})); throw new Error(er.error || res.statusText); }
    const reader = res.body.getReader(), dec = new TextDecoder();
    let buf = '';
    const handle = line => { if (!line.trim()) return; am2RunEvent(O.live, JSON.parse(line)); am2ObjPaint(route); };
    for (;;) { const {value, done} = await reader.read(); buf += dec.decode(value || new Uint8Array(), {stream: !done}); let at; while ((at = buf.indexOf('\n')) >= 0) { handle(buf.slice(0, at)); buf = buf.slice(at + 1); } if (done) break; }
    if (buf.trim()) handle(buf);
    if (O.live.error) throw new Error(O.live.error);
    const r = O.live.done;
    if (r) {
      O.last = r.lastRun || {results: r.results, summary: r.summary};
      if (r.suite) O.suite = {...O.suite, ...r.suite, explain: O.suite.explain, checks: O.suite.checks || r.suite.checks};
    } else if (!O.abort.signal.aborted) throw new Error('The run ended before it finished.');
    S.am2.data = null;
  } catch (e) {
    if (e.name === 'AbortError') { const got = Object.values(O.live?.results || {}); if (got.length) O.last = {results: got, summary: {cancelled: true, at: new Date().toISOString()}}; toast('Run stopped'); }
    else O.error = e.message;
  }
  O.busy = false; O.live = null; O.abort = null;
  render();
}

async function am2ObjTry(route) {
  const O = S.am2.obj, ref = am2ObjRef(route.obj), tf = $('#am2-tryform');
  if (!tf || O.tryBusy) return;
  O.tryReq = am2ReadReq(tf, 't-', O.tryReq);
  O.tryBusy = true; O.tryRes = null; am2ObjPaint(route);
  try {
    const body = {ref: route.obj, request: O.tryReq, context: await am2ObjContext(ref.cluster)};
    const src = am2ObjSourceBody(O); if (src) body.source = src;
    O.tryRes = await apiPost('api/ambient/suites/explain', body);
  } catch (e) { O.tryRes = {error: e.message}; }
  O.tryBusy = false; am2ObjPaint(route);
}

const am2SaveSuite = async O => { await apiPost('api/ambient/suites', O.suite); O.suite = {...O.suite, ...(await api('api/ambient/suites', {ref: O.key})), lastRun: O.last}; };

// Wiring that must be redone each time the tests body is repainted.
function am2ObjWireBody(route) {
  const O = S.am2.obj, sm = $('#am2-samples'); if (sm) sm.onchange = () => { O.samples = Number(sm.value); };
  const tf = $('#am2-tryform'); if (tf && !tf.dataset.wired) { tf.dataset.wired = '1'; am2WireReqFields(tf, 't-'); }
  const td = $('#am2-try'); if (td) td.ontoggle = () => { O.tryOpen = td.open; };
}

function am2ObjectAfter(route) {
  const root = $('#am2-root');
  if (!root || !(route.obj || (route.svc && route.stab === 'tests'))) return;
  const O = S.am2.obj;
  if (route.obj) am2ObjWireBody(route);
  if (root.dataset.obj) return;
  root.dataset.obj = '1';
  root.addEventListener('click', async e => {
    const t = e.target, ref = route.obj ? am2ObjRef(route.obj) : null;
    if (t.closest('[data-am2-objyaml]')) { openObject(ref ? {cluster: ref.cluster, kind: ref.kind, namespace: ref.namespace, name: ref.name, group: O.suite.target?.group, resource: O.suite.target?.resource} : {}, 'yaml'); return; }
    const src = t.closest('[data-am2-src]'); if (src) { O.source = src.dataset.am2Src; am2ObjPaint(route); return; }
    if (t.closest('[data-am2-runall]')) { am2ObjRun(route, null); return; }
    if (t.closest('[data-am2-stop]')) { O.abort?.abort(); return; }
    if (t.closest('[data-am2-try]')) { am2ObjTry(route); return; }
    if (t.closest('[data-am2-trysave]')) {
      const r = O.tryRes, isRoute = ref.kind === 'HTTPRoute';
      const expect = isRoute && r.got?.backend && r.got.status < 400 ? {backend: r.got.backend, text: 'backend ' + r.got.backend} : {status: r.got?.status || 200, text: String(r.got?.status || 200)};
      am2CasePanel(O, ref, {id: '', why: '', label: 'Added case', request: O.tryReq, expect}, async c => { O.suite.cases = [...(O.suite.cases || []), c]; await am2SaveSuite(O); render(); });
      return;
    }
    const mark = t.closest('[data-am2-markexp]');
    if (mark) { e.preventDefault(); const r = (O.last?.results || []).find(x => x.id === mark.dataset.am2Markexp), c = O.suite.cases.find(x => x.id === mark.dataset.am2Markexp); if (c && r?.got?.status) { c.expect = {status: r.got.status, text: String(r.got.status)}; c.generated = false; await am2SaveSuite(O).catch(err => { O.error = err.message; }); await am2ObjRun(route, null); } return; }
    if (t.closest('[data-am2-regen]')) { O.suite = {...(await api('api/ambient/suites', {ref: route.obj, regenerate: '1'})), lastRun: O.last}; await apiPost('api/ambient/suites', O.suite).catch(err => { O.error = err.message; }); render(); return; }
    if (t.closest('[data-am2-addcase]')) { am2CasePanel(O, ref, null, async c => { O.suite.cases = [...(O.suite.cases || []), c]; await am2SaveSuite(O); render(); }); return; }
    const ed = t.closest('[data-am2-editcase]');
    if (ed) { const c = O.suite.cases.find(x => x.id === ed.dataset.am2Editcase); if (c) am2CasePanel(O, ref, c, async n => { O.suite.cases = O.suite.cases.map(x => x.id === c.id ? {...n, generated: false} : x); await am2SaveSuite(O); render(); }); return; }
    const dp = t.closest('[data-am2-dupcase]');
    if (dp) { const c = O.suite.cases.find(x => x.id === dp.dataset.am2Dupcase); if (c) am2CasePanel(O, ref, {...c, id: '', generated: false, why: (c.why || '') + ' (copy)'}, async n => { O.suite.cases = [...O.suite.cases, n]; await am2SaveSuite(O); render(); }); return; }
    const dl = t.closest('[data-am2-delcase]');
    if (dl) { if (!confirm('Delete this test case?')) return; O.suite.cases = O.suite.cases.filter(x => x.id !== dl.dataset.am2Delcase); await am2SaveSuite(O).catch(err => toast(err.message)); render(); return; }
    const log = t.closest('[data-am2-log]'); if (log) { const l = JSON.parse(log.dataset.am2Log); openLogs({namespace: l.namespace || '', pod: l.pod || ''}); return; }
    const chk = t.closest('[data-am2-chk]');
    if (chk) { const a = JSON.parse(chk.dataset.am2Chk), svc = O.suite.service || {}; if (a.kind === 'copy-to-cluster') am2Go({svc: am2SvcPath(ref.cluster, svc.namespace, svc.name), stab: 'configuration', drawer: 'copy:' + a.cluster}); else if (a.kind === 'add-timeout') am2Go({svc: am2SvcPath(ref.cluster, svc.namespace, svc.name), stab: 'configuration', drawer: 'add:timeouts'}); return; }
    const rv = t.closest('[data-am2-rev]'), ry = t.closest('[data-am2-revyaml]');
    if (rv || ry) { const i = Number((rv || ry).dataset[rv ? 'am2Rev' : 'am2Revyaml']), want = rv ? 'diff' : 'yaml'; O.openRev[i] = O.openRev[i] === want ? '' : want; render(); return; }
    const rs = t.closest('[data-am2-runsuite]');
    if (rs) { try { await apiPost('api/ambient/suites/run', {ref: rs.dataset.am2Runsuite, all: true, context: await am2ObjContext(am2SvcRef(route.svc).cluster), save: true}); } catch (err) { toast(err.message); } render(); return; }
    if (t.closest('[data-am2-runservice]')) { for (const s of S.am2.obj.svcList?.suites || []) { try { await apiPost('api/ambient/suites/run', {ref: am2ObjStr(s.ref), all: true, context: await am2ObjContext(am2SvcRef(route.svc).cluster), save: true}); } catch (err) { toast(err.message); break; } } render(); return; }
    if (t.closest('[data-am2-objundo]')) {
      const rec = (S.ambient.tests || []).find(x => x.applyState === 'applied' && (x.created || []).some(o => o.name === ref.name));
      if (rec && confirm('Undo this change on ' + rec.context + '?')) { try { await apiPost('api/ambient/test', {operation: 'cleanup', context: rec.context, validation: rec.token}); S.ambient.tests = await api('api/ambient/tests'); S.am2.data = null; O.key = ''; toast('Change undone'); } catch (err) { toast(err.message); } render(); }
    }
  });
}
AM2_AFTER.push(am2ObjectAfter);
