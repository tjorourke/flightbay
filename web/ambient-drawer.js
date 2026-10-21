'use strict';
/* Ambient v2: the task drawer. Mockups 03 (Choose settings) and 03b (Apply and test).
   It replaces the four-step wizard. The form fields, plan request, validation, apply and undo reuse the
   existing Ambient logic (amRead, amValidatePlans, amConfirmApply, amTestOperation); only the markup is new. */

const AM2_MODE_TITLES = {waypoint: 'Set up a waypoint', 'enrol-namespace': 'Enrol in ambient', 'mesh-strict-mtls':'Mesh-wide STRICT mTLS', endpoints: 'Recover endpoints', 'route-parent': 'Repair route parent'};
S.am2.drawer = S.am2.drawer || {key: '', phase: 1, entries: [], error: '', busy: false, plans: [], seq: 0, progress: [], results: null, task: '', demo: null, scroll: 0, drafts: {}, tests: null, status: ''};

const am2D = () => S.am2.drawer;
const am2Md = t => esc(t).replace(/`([^`]+)`/g, '<span class="am2-mono">$1</span>');
const am2Time = d => new Date(d || Date.now()).toLocaleTimeString('en-GB', {hour12: false});

function am2DrawerParts(route) {
  const [kind, ...rest] = String(route.drawer || '').split(':');
  return {kind, arg: rest.join(':')};
}
const am2CloseHash = route => am2Hash({...route, drawer: '', dphase: '', demo: ''});

async function am2Demo() {
  const D = am2D();
  if (D.demo === null) D.demo = await api('api/ambient/preview-demo').then(r => !!r.demo).catch(() => false);
  return D.demo;
}

// Put the old Ambient state in the shape its form, plan and apply code expects.
async function am2PrepOld(route, ref, kind, arg, cfg) {
  const A = S.ambient, D = am2D();
  if (!A.inventory) A.inventory = await api('api/ambient');
  if (!A.contexts) A.contexts = await api('api/ambient/contexts').catch(() => []);
  const isNs = kind === 'ns';
  const target = isNs ? {cluster: ref.cluster, kind: 'Namespace', namespace: '', name: ref.namespace} : (A.inventory.services || []).find(s => s.cluster === ref.cluster && s.namespace === ref.namespace && s.name === ref.name);
  if (!target) throw new Error(`${ref.namespace}/${ref.name} is not captured on ${ref.cluster}.`);
  const saved = D.drafts[D.key];
  amResetDraft();
  A.key = amKey(isNs ? {...target, kind: 'Namespace'} : target); A.kind = isNs ? 'Namespace' : 'Service'; A.tab = 'wizard'; A.phase = 2;
  if (!A.contexts) A.contexts = [];
  A.context = amLiveContext(ref.cluster, A.contexts)?.name || A.context || '';
  if (!isNs) {
    A.capabilities = await api('api/ambient/capabilities', {cluster: ref.cluster, namespace: ref.namespace, service: ref.name});
    A.result = await api('api/ambient/investigate', amQuery());
  } else {
    A.result = await api('api/ambient/investigate', {cluster: ref.cluster, kind: 'Namespace', namespace: '', name: ref.namespace});
    D.task = D.task || ((A.inventory.enrolledNamespaces || []).some(n => n.cluster === ref.cluster && n.name === ref.namespace) ? 'waypoint' : 'enrol-namespace');
  }
  if (kind === 'add') {
    if (['waypoint', 'enrol-namespace', 'mesh-strict-mtls', 'endpoints', 'route-parent'].includes(arg)) A.mode = arg; else A.tasks = {[arg]: true};
  } else if (kind === 'ns') A.mode = D.task;
  else if (kind === 'edit') {
    const entry = (A.result.editable || []).find(e => `${e.ref.kind}:${e.ref.namespace}/${e.ref.name}#${e.rule}` === arg);
    if (!entry) throw new Error('That configuration is no longer captured. Refresh and try again.');
    amOpenEdit(entry);
  } else if (kind === 'fix') {
    const step = (A.result.steps || []).find(s => s.id === arg), remedy = (step?.actions || [])[0] || (arg === 'require-waypoint' ? {id: 'require-waypoint'} : null);
    if (!remedy) return 'checks';
    const field = step?.evidence?.find(e => e.ref.kind === 'HTTPRoute')?.field, match = field?.match(/parentRefs\[(\d+)\]/);
    if (remedy.id === 'require-waypoint') A.tasks[remedy.id] = true; else A.mode = remedy.id;
    if (remedy.ref) { A.repairRef = remedy.ref; A.settings.repairRoute = remedy.ref.name; A.settings.parentIndex = match ? Number(match[1]) : 0; }
  } else if (kind === 'copy') {
    const first = (cfg?.tasks || []).find(t => t.status === 'configured' && t.id !== 'global-service');
    if (first) A.tasks = {[first.id]: true};
    A.settings.applyClusters = [arg];
  }
  const seed = S.am2.seed;
  if (seed && seed.key === kind + ':' + arg) { A.tasks = seed.tasks; A.settings = seed.settings; S.am2.seed = null; }
  else if (saved) { Object.assign(A.settings, saved.settings); if (!A.edit) A.tasks = saved.tasks || A.tasks; }
  return '';
}

function am2DrawerTask() {
  const A = S.ambient;
  if (A.mode) return A.mode;
  return A.edit ? Object.keys(A.tasks).find(k => A.tasks[k]) : Object.keys(A.tasks).find(k => A.tasks[k]);
}
function am2TaskTitle(cfg, id) { return (cfg?.tasks || []).find(t => t.id === id)?.shortLabel || AM2_MODE_TITLES[id] || id; }

// ---------- form column ----------
function am2OldFields(svc) {
  const A = S.ambient, target = amTarget();
  let html;
  if (['waypoint', 'enrol-namespace'].includes(A.mode)) html = amWaypoint(target);
  else if (['endpoints', 'route-parent'].includes(A.mode)) html = amRecovery(svc);
  else html = amWizard(svc);
  const t = document.createElement('template');
  t.innerHTML = html;
  const box = t.content.querySelector('#am-parameters, #am-setup-parameters, #am-repair-parameters');
  if (!box) return '';
  box.querySelectorAll('.am-wizard-footer, .am-explainer, #am-live, .am-config-clusters, .am-global-publication > h3, #am-plan, .notice.info:has(#am-back-wizard)').forEach(e => e.remove());
  box.querySelectorAll('#am-generate-repair, #am-generate-setup, #am-generate').forEach(e => e.remove());
  box.classList.add('am2-oldfields');
  return box.outerHTML;
}

const AM2_METHODS = ['GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE', 'OPTIONS'];
function am2Radio(name, value, on, title, sub) {
  return `<label class="am2-radio ${on ? 'on' : ''}"><input type="radio" name="${name}" value="${esc(value)}" ${on ? 'checked' : ''} hidden><span class="o"></span><div><b>${esc(title)}</b><span class="am2-sub">${esc(sub)}</span></div></label>`;
}
function am2PortField(svc, F) {
  const port = F.port || svc.ports[0], named = (svc.portDetails || []).find(p => p.port === port);
  return `<div><span class="am2-lbl">Port</span><select class="am2-input am2-full" id="am-port">${svc.ports.map(p => { const d = (svc.portDetails || []).find(x => x.port === p); return `<option value="${p}" ${p === port ? 'selected' : ''}>${p}${d?.name ? ` (${esc(d.name)})` : ''}</option>`; }).join('')}</select>${svc.ports.length === 1 ? '<div class="am2-sub" style="margin-top:4px">Only port on this Service</div>' : ''}</div>`;
}
function am2MethodsForm(svc) {
  const F = S.ambient.settings, action = F.methodAction || 'ALLOW', on = new Set(String(F.methods ?? 'GET,HEAD').split(',').map(x => x.trim().toUpperCase()).filter(Boolean));
  const sources=F.callerSources||[],label=sources.length?sources.map(s=>[(s.principals||[]).join(' or '),(s.serviceAccounts||[]).join(' or '),(s.namespaces||[]).map(n=>'namespace '+n).join(' or ')].filter(Boolean).join(' and ')||'Existing caller constraints').join(' or '):'Everyone';
  const accounts=S.ambient.inventory?.serviceAccounts||[];
  return `<div id="am2-params" class="am2-fields"><span class="am2-lbl">What should happen?</span>${
    am2Radio('am2-methodAction', 'ALLOW', action === 'ALLOW', 'Only allow the methods I pick', `Pick which HTTP methods reach ${svc.name}. Adds an ALLOW policy.`)
  }${am2Radio('am2-methodAction', 'DENY', action === 'DENY', 'Block the methods I pick', 'Everything else still works. Adds a DENY policy.')
  }<span class="am2-lbl" style="margin-top:16px">Methods</span><div style="display:flex;gap:6px;flex-wrap:wrap" data-ammethods="methods">${AM2_METHODS.map(m => `<label class="am2-chip ${on.has(m) ? 'on' : ''}"><input type="checkbox" value="${m}" ${on.has(m) ? 'checked' : ''} hidden>${on.has(m) ? '✓ ' : ''}${m}</label>`).join('')}</div><div style="display:grid;grid-template-columns:1fr 1fr;gap:14px;margin-top:16px">${am2PortField(svc, F)}<div><span class="am2-lbl">Callers this applies to</span><select class="am2-input am2-full" id="am2-callers"><option value="preserve">${esc(label)}</option><option value="everyone">Everyone</option>${accounts.filter(a=>a.cluster===svc.cluster).map(a=>`<option value="${esc(a.namespace+'/'+a.name)}">${esc(a.namespace+'/'+a.name)}</option>`).join('')}</select></div></div></div>`;
}
function am2MtlsForm(svc) {
  const F = S.ambient.settings, mode = F.mtlsMode || 'STRICT';
  return `<div id="am2-params" class="am2-fields"><span class="am2-lbl">What should happen?</span>${am2Radio('am-mtls-mode', 'STRICT', mode === 'STRICT', 'Require mesh mTLS', 'Only callers with a mesh-authenticated connection can reach these Pods.')}${am2Radio('am-mtls-mode', 'PERMISSIVE', mode === 'PERMISSIVE', 'Allow non-mesh callers too', 'Accept plaintext connections as well as mesh mTLS.')}</div>`;
}
function am2Form(svc, cfg) {
  const A = S.ambient, task = am2DrawerTask();
  if(task==='mesh-strict-mtls'){
    const target=amTarget(),state=(A.inventory.meshMtls||[]).find(m=>m.cluster===target.cluster),defaults=state?.defaults||[];
    return `<div id="am-setup-parameters" class="am2-fields"><h3>Cluster-wide default in istio-system</h3><p>Applies across ${esc(target.cluster)}. Namespace, workload and port overrides remain effective.</p>${!state?.confirmed?'<p>Mesh-root evidence is missing or does not confirm istio-system. Capture mesh configuration before applying.</p>':''}${defaults.length?`<label>Existing root default<select id="am-meshPolicy"><option value="">Select the policy to edit explicitly…</option>${amOpts(defaults.map(e=>({value:e.ref.name,label:e.ref.name+' · '+(e.request?.mtlsMode||'UNSET')})),A.settings.meshPolicy)}</select></label>`:'<p>Create a selectorless PeerAuthentication with mode STRICT.</p>'}${(state?.overrides||[]).length?`<p>More-specific policies: ${state.overrides.map(r=>esc(r.namespace+'/'+r.name)).join(', ')}</p>`:''}</div>`;
  }
  if (task === 'methods' && !A.mode) return am2MethodsForm(svc);
  if (task === 'strict-mtls' && !A.mode) return am2MtlsForm(svc);
  return am2OldFields(svc);
}

function am2YamlBlock() {
  return `<div id="am2-diff"></div><details style="margin-top:18px" open id="am2-yamlbox"><summary class="am2-t" style="cursor:pointer">Generated YAML <span class="am2-sub" id="am2-yamlsub"></span></summary><pre id="am-live-yaml" class="am2-pre" style="margin-top:8px">Rendering…</pre></details><div class="am2-sub" style="margin-top:8px">Name is editable. Flightbay labels it so it shows as "Managed by Flightbay" and can be undone.</div>`;
}

function am2PlanDiff(plan) {
  return (plan.changes||[]).map(c=>`<details open><summary>${esc(c.ref.kind+' '+c.ref.namespace+'/'+c.ref.name)}</summary>${(c.fields||[]).map(f=>`<div class="am2-sub">${esc(f.path)}</div><div>Before</div><pre class="am2-pre">${esc(f.beforePresent?JSON.stringify(f.before,null,2):'Not set')}</pre><div>After</div><pre class="am2-pre">${esc(f.afterPresent?JSON.stringify(f.after,null,2):'Remove setting')}</pre>`).join('')}</details>`).join('');
}

// ---------- effects column ----------
function am2EffectsPlaceholder(name) {
  return `<h3>What this does to ${esc(name)}</h3><ul id="am2-effects"><li class="am2-muted">Working it out…</li></ul><div id="am2-interactions"></div><div id="am2-testprev"></div>`;
}
function am2FillEffects(plan, tests, name) {
  const ul = $('#am2-effects'), ia = $('#am2-interactions'), tp = $('#am2-testprev');
  if (!ul) return;
  ul.innerHTML = (plan?.effects || []).length ? plan.effects.map(e => `<li>${am2Md(e).replace(/^(.*?return \d+.*?)$/, '$1')}</li>`).join('') : '<li class="am2-muted">No change to how requests are handled.</li>';
  ul.querySelectorAll('li').forEach(li => { li.innerHTML = li.innerHTML.replace(/^((?:[A-Z, ]+?(?:and )?[A-Z]+) return \d+)( from the waypoint\.)$/, '<b>$1</b>$2'); });
  ia.innerHTML = (plan?.interactions || []).map(i => `<div class="am2-callout ${i.level === 'warn' ? 'w' : 'i'}" style="margin-top:${i.level === 'warn' ? 14 : 8}px"><div>${i.level === 'warn' ? '⚠' : 'ℹ'}</div><div>${i.level === 'warn' ? `<b>${esc(i.text.split('. ')[0])}.</b> ${esc(i.text.split('. ').slice(1).join('. '))}` : esc(i.text)}${i.followUpTask ? ` <a href="${am2Hash({...am2Route(), drawer: 'add:' + i.followUpTask})}" style="color:var(--am2-info);font-weight:600">${esc(i.followUpLabel || 'Add it')}</a>` : ''}</div></div>`).join('');
  if (!tests || !(tests.cases || []).length) { tp.innerHTML = ''; return; }
  const src = tests.suites?.[0]?.source?.ref;
  tp.innerHTML = `<h3 style="margin-top:18px">Tests that will run after apply</h3><div class="am2-sub">Generated from the rule.${src ? ` From <span class="am2-mono">${esc(src.namespace)}/${esc(src.name)}</span> (in mesh).` : ''}</div><div style="margin-top:6px">${tests.cases.map(c => {
    const status = c.expect?.status, text = c.expect?.text || (status ? String(status) : '');
    const pill = status ? am2Pill(status < 400 ? 'ok' : 'bad', 'expect ' + status) : am2Pill('info', 'expect ' + text);
    const hdr = Object.entries(c.request?.headers || {}).map(([k, v]) => `${k}: ${v}`).join(', ');
    return `<div class="am2-case">${pill}<span class="am2-mono">${esc(c.request?.method || 'GET')} ${esc(c.request?.path || '/')}</span>${hdr || (c.why && c.id?.startsWith('x-')) ? ` <span class="am2-sub">${esc(hdr)}${c.why && c.id?.startsWith('x-') ? ` (${esc(c.why)})` : ''}</span>` : ''}</div>`;
  }).join('')}</div><div class="am2-sub" style="margin-top:6px">Edit cases on the Tests tab. They are saved with this policy.</div>`;
}

// Light syntax colour for the generated YAML: resource name, allow/deny action and the method list.
function am2Yaml(text) {
  let named = false, inMethods = false;
  return String(text).split('\n').map(line => {
    let h = esc(line);
    if (/^\s*methods:\s*$/.test(line)) inMethods = true; else if (!/^\s*- /.test(line)) inMethods = false;
    if (!named && /^\s{2}name: /.test(line)) { named = true; h = h.replace(/^(\s*name: )(.*)$/, '$1<span class="y-k">$2</span>'); }
    h = h.replace(/(action: )(ALLOW)\b/, '$1<span class="y-a">$2</span>').replace(/(action: )(DENY)\b/, '$1<span class="y-r">$2</span>');
    if (inMethods && /^\s*- [A-Z]+$/.test(line)) h = h.replace(/- ([A-Z]+)$/, '- <span class="y-a">$1</span>');
    return h;
  }).join('\n');
}

// ---------- live plan ----------
async function am2DrawerLive() {
  const D = am2D(), pre = $('#am-live-yaml');
  if (!pre || D.phase !== 1) return;
  const seq = ++D.seq, A = S.ambient;
  try {
    amCapture(); am2CaptureExtra();
    const req = amRead(), clusters = amConfigurationClusters(req);
    if (!clusters.length) throw new Error('Select at least one cluster.');
    const plans = [];
    for (const cluster of clusters) { const q = await amConfigurationRequestForCluster(req, cluster); plans.push({cluster, plan: await apiPost('api/ambient/plan', q)}); }
    if (seq !== D.seq || !$('#am-live-yaml')) return;
    D.plans = plans;
    const first = plans[0].plan, kinds = [...new Set(plans.flatMap(p => p.plan.objects.map(o => o.apiVersion)))];
    if($('#am2-diff'))$('#am2-diff').innerHTML=plans.map(p=>am2PlanDiff(p.plan)).join('');
    pre.innerHTML = am2Yaml(plans.map(({cluster, plan}) => (plans.length > 1 ? '# Cluster: ' + cluster + '\n' : '') + plan.yaml).join('\n---\n'));
    pre.classList.remove('am2-bad');
    $('#am2-yamlsub').textContent = `${first.objects.length} new object${first.objects.length === 1 ? '' : 's'}${(first.changes || []).length ? ` · ${first.changes.length} update${first.changes.length === 1 ? '' : 's'}` : ''}${kinds.length ? ' · ' + kinds.join(', ') : ''}`;
    let tests = null;
    try { tests = await apiPost('api/ambient/suites/preview', first); } catch (_) { tests = null; }
    if (seq !== D.seq) return;
    am2FillEffects(first, tests, amService()?.name || '');
    $('#am2-primary')?.removeAttribute('disabled');
  } catch (e) {
    if (seq !== D.seq || !$('#am-live-yaml')) return;
    pre.textContent = e.message; pre.classList.add('am2-bad'); $('#am2-yamlsub').textContent = 'Not valid yet';
    $('#am2-effects') && ($('#am2-effects').innerHTML = '<li class="am2-muted">Fix the settings to see the effect.</li>');
  }
}
function am2CaptureExtra() {
  const F = S.ambient.settings, r = document.querySelector('input[name="am2-methodAction"]:checked');
  if (r) F.methodAction = r.value;
  const caller=$('#am2-callers');if(caller&&caller.value!=='preserve'){F.callerSources=caller.value==='everyone'?[]:[{serviceAccounts:[caller.value]}];F.callersEdited=true;}
  document.querySelectorAll('[data-ammethods="methods"] input').forEach(() => {});
}

// ---------- phase 2 and 3 progress ----------
function am2ProgressRows(D) {
  const rows = [];
  for (const e of D.entries) {
    const v = e.validation, p = e.plan, upd = (p?.changes || []).filter(c => !c.delete).length, del = (p?.changes || []).filter(c => c.delete).length;
    if (e.error && !v) { rows.push({tone: 'bad', mark: '✕', title: 'Check failed', sub: `${e.cluster} · ${e.error}`}); continue; }
    if (v) {
      rows.push({tone: 'ok', mark: '✓', title: 'Cluster identity matches', sub: `${e.cluster} · API ${v.server || ''}${p?.request?.edit ? ' · resource unchanged since captured' : ''}`});
      rows.push({tone: 'ok', mark: '✓', title: 'Server dry-run passed', sub: `${p.objects.length} create · ${upd} update · ${del} delete`});
    }
    if (e.record && D.phase === 3) {
      const created = (e.record.created || []).map(o => `${o.kind} ${o.namespace}/${o.name}`).concat((e.record.modified || []).map(c => `${c.ref.kind} ${c.ref.namespace}/${c.ref.name}`));
      rows.push({tone: 'ok', mark: '✓', title: 'Applied', sub: created.join(', ') || e.cluster});
      rows.push(e.status ? {tone: 'ok', mark: '✓', title: e.status.title, sub: e.status.sub} : {tone: 'mut', mark: '…', title: 'Waiting for the status', sub: 'Reading the live mirror'});
    }
    if (e.state === 'Failed') rows.push({tone: 'bad', mark: '✕', title: 'Apply failed and was rolled back', sub: e.error});
  }
  return rows;
}
function am2ProgressHtml(D, extra = '') {
  const rows = am2ProgressRows(D).concat(extra ? [] : []);
  return `<h3>Progress</h3><div style="margin-top:10px">${rows.map(r => `<div class="am2-case">${am2Pill(r.tone, r.mark)}<b style="width:210px">${esc(r.title)}</b><span class="am2-sub">${esc(r.sub)}</span></div>`).join('')}${D.testsRow || ''}</div>`;
}

function am2TestsTable(D) {
  const r = D.results;
  if (!r) return '';
  const rows = r.rows || [];
  return `<h3 style="margin-top:20px">Test results <span class="am2-sub">${esc(r.from || '')}</span></h3><table style="margin-top:8px"><thead><tr><th>Request</th><th>Expected</th><th>Got</th><th>Decided by</th><th></th></tr></thead><tbody>${rows.map(x => `<tr ${x.ok ? '' : 'class="am2-warnrow"'}><td class="am2-mono">${esc(x.request)}${x.note ? ` <span class="am2-sub">${esc(x.note)}</span>` : ''}</td><td>${esc(x.expect)}</td><td>${am2Pill(x.gotTone, x.got)}</td><td class="am2-sub">${esc(x.decided)}</td><td>${x.ok ? '✓' : '✕'}</td></tr>`).join('')}</tbody></table>${r.failure ? `<div class="am2-callout e" style="margin-top:12px"><div>✕</div><div>${r.failure}</div></div>` : ''}`;
}

const AM2_STATUS_WORDS = [['mut', 'Draft', 'only in this browser'], ['info', 'Checked', 'dry-run passed, nothing written'], ['tl', 'Applied', 'written, waiting for status'], ['ok', 'Live', 'accepted by the waypoint / ztunnel'], ['ok', 'Verified', 'live and its tests pass'], ['bad', 'Failed', 'rolled back automatically']];

function am2DoneColumn(D, cfg, ref) {
  const A = S.ambient, e = D.entries[0], others = (cfg?.clusters || []).filter(c => !D.entries.some(x => x.cluster === c));
  const follow = (D.plans[0]?.plan?.interactions || []).filter(i => i.followUpTask);
  const name = (e?.plan?.objects?.[0]?.metadata?.name) || am2TaskTitle(cfg, am2DrawerTask());
  const nTests = D.results?.rows?.length || 0;
  return `<h3>Done. What now?</h3><div class="am2-callout s" style="margin-top:10px"><div>✓</div><div><b>${esc(name)} is live on ${esc(e?.cluster || ref.cluster)}.</b> It is now on the Configuration tab${nTests ? ` with its ${nTests} tests saved` : ''}.</div></div><div style="display:flex;flex-direction:column;gap:8px;margin-top:14px">${
    others.map(c => `<button type="button" class="am2-btn am2-lj" data-am2-same="${esc(c)}">Apply the same to ${esc(c)} →</button>`).join('')
  }${follow.map(i => `<a class="am2-btn am2-lj" href="${am2Hash({...am2Route(), drawer: 'add:' + i.followUpTask})}">Add "${esc((i.followUpLabel || '').replace(/^Also add "|"$/g, '') || i.followUpTask)}" →</a>`).join('')
  }<button type="button" class="am2-btn am2-lj" data-am2-rerun>Run the tests again</button><button type="button" class="am2-btn am2-lj" style="color:var(--am2-bad)" data-am2-undo>Undo this change</button></div><h3 style="margin-top:20px">Status words used everywhere</h3><div style="display:flex;flex-direction:column;gap:6px;margin-top:8px;font-size:12px">${AM2_STATUS_WORDS.map(([t, w, d]) => `<div>${am2Pill(t, w)} ${esc(d)}</div>`).join('')}</div>`;
}

// ---------- host ----------
async function am2DrawerHost(route, data) {
  const D = am2D();
  if (!route.drawer) { D.key = ''; return ''; }
  const {kind, arg} = am2DrawerParts(route);
  const ref = kind === 'ns' ? {cluster: S.cluster || data.summary.clusters.names[0], namespace: arg, name: arg} : am2SvcRef(route.svc);
  const key = route.svc + '|' + route.drawer;
  if (!S.ambient.contexts) S.ambient.contexts = await api('api/ambient/contexts').catch(() => []);
  let cfg = null;
  try {
    if (kind !== 'ns') cfg = S.am2.svc.cfg && S.am2.svc.key === route.svc ? S.am2.svc.cfg : await api('api/ambient/service-config', ref);
    // Two renders can overlap (boot, live updates). The first prepares the drawer; any other waits for it,
    // otherwise it would render while the draft is reset and show an empty form.
    if (D.prepKey === key && D.prep) await D.prep;
    if (D.key !== key) {
      D.key = key; D.phase = 1; D.entries = []; D.plans = []; D.error = ''; D.results = null; D.progress = []; D.task = '';
      D.prepKey = key; D.prep = am2PrepOld(route, ref, kind, arg, cfg);
      let redirect;
      try { redirect = await D.prep; } finally { D.prep = null; }
      if (redirect) { setTimeout(() => am2Go({svc: route.svc, stab: redirect}, true), 0); return ''; }
    }
    if (route.dphase === '3' && route.demo === '1' && await am2Demo() && D.phase !== 3) await am2DemoPhase3(route, ref, cfg);
  } catch (e) {
    D.error = e.message;
  }
  const A = S.ambient, svc = kind === 'ns' ? null : amService();
  const task = am2DrawerTask(), title = kind === 'ns' ? am2TaskTitle(cfg, D.task) : am2TaskTitle(cfg, task);
  const phase = D.phase;
  const where = kind === 'ns' ? `Namespace settings for <b>${esc(ref.namespace)}</b>` : `${kind === 'edit' ? 'Edit configuration of' : 'Add configuration to'} <b>${esc(ref.namespace)}/${esc(ref.name)}</b>`;
  const step = (n, label) => `<span class="${phase === n ? 'on' : ''}"><i>${phase > n ? '✓' : n}</i>${esc(label)}</span>`;
  const stateText = phase === 1 ? 'Draft saved · nothing written' : phase === 2 ? 'Checked · nothing written' : `Applied to ${esc(D.entries.map(e => e.cluster).join(', ') || ref.cluster)} at ${am2Time(D.entries[0]?.record?.applyFinished)}`;
  const left = phase === 1 ? (svc || kind === 'ns' ? am2Form(svc, cfg) + am2YamlBlock() : '') : phase === 2 ? am2ProgressHtml(D) + D.entries.map(e=>am2PlanDiff(e.plan)).join('') : am2ProgressHtml(D) + am2TestsTable(D);
  const right = phase === 3 ? am2DoneColumn(D, cfg, ref) : am2EffectsPlaceholder(ref.name);
  const menu = am2SwitchMenu(cfg, route, kind, ref);
  const footer = am2DrawerFooter(D, cfg, ref, route, kind);
  return `<div class="am2-shade" data-am2-close></div><div class="am2-drawer" id="am2-drawer" role="dialog" aria-label="${esc(title)}"><div class="am2-dh"><div style="flex:1"><div class="am2-sub">${where} · ${esc(ref.cluster)}</div><h2 style="font-size:18px;margin-top:2px">${esc(title)}</h2></div>${menu}<button type="button" class="am2-btn g" data-am2-close aria-label="Close">✕</button></div><div class="am2-steps">${step(1, 'Choose settings')}${step(2, 'Check against the cluster')}${step(3, 'Apply and test')}<span style="margin-left:auto;font-weight:500">${stateText}</span></div>${D.error ? `<div class="am2-callout e" style="margin:12px 22px 0" role="alert"><div>✕</div><div>${esc(D.error)}</div></div>` : ''}<div class="am2-db"><div class="am2-form" id="am2-formcol">${left}</div><div class="am2-eff">${right}</div></div>${footer}</div><div hidden id="am-target-selector"></div>`;
}

function am2SwitchMenu(cfg, route, kind, ref) {
  if (kind === 'ns') return `<details class="am2-menu"><summary class="am2-btn">Switch task ▾</summary><div class="am2-menu-list">${['enrol-namespace', 'waypoint','mesh-strict-mtls'].map(t => `<button type="button" data-am2-nstask="${t}">${esc(AM2_MODE_TITLES[t])}</button>`).join('')}</div></details>`;
  const groups = (cfg?.groups || []).filter(g => g.id !== 'multicluster');
  return `<details class="am2-menu"><summary class="am2-btn">Switch task ▾</summary><div class="am2-menu-list">${groups.map(g => `<div class="am2-menu-h">${esc(g.question)}</div>${(cfg.tasks || []).filter(t => t.group === g.id && !['unavailable', 'needs-waypoint'].includes(t.status)).map(t => `<a href="${am2Hash({...route, drawer: 'add:' + t.id})}">${esc(t.shortLabel)}</a>`).join('')}`).join('')}</div></details>`;
}

function am2DrawerFooter(D, cfg, ref, route, kind) {
  const A = S.ambient, close = `data-am2-close`;
  if (D.phase === 3) return `<div class="am2-df"><button type="button" class="am2-btn" ${close}>Close</button><span style="flex:1"></span><a class="am2-btn" href="${am2Hash({svc: route.svc, stab: 'changes'})}">View change in Changes</a><button type="button" class="am2-btn p" ${close}>Back to ${esc(ref.name)}</button></div>`;
  const clusters = (cfg?.clusters || S.am2.data?.summary?.clusters?.names || []), selected = A.settings.applyClusters || [ref.cluster], others = clusters.filter(c => c !== ref.cluster);
  const ctxs = (A.contexts || []), live = amLiveContext(ref.cluster, ctxs);
  // Only contexts that reach a connected cluster can be written to (never the rest of the kubeconfig).
  const reach = ctxs.filter(c => c.liveCluster === ref.cluster);
  if (live && A.context !== live.name && !reach.some(c => c.name === A.context)) A.context = live.name;
  const sel = live ? `<span class="am2-writeto" title="${esc(live.name)} · ${esc(live.server || '')}"><b>${esc(ref.cluster)}</b> <span class="am2-sub">live connection</span></span><input type="hidden" id="am2-context" value="${esc(live.name)}">`
    : reach.length ? `<select class="am2-sel" id="am2-context" aria-label="Write to"><option value="">Choose the context for ${esc(ref.cluster)}…</option>${reach.map(c => `<option value="${esc(c.name)}" ${A.context === c.name ? 'selected' : ''}>${esc(c.name)}</option>`).join('')}</select>`
    : `<span class="am2-sub">No kubeconfig context reaches ${esc(ref.cluster)}. Open with --kubeconfig to change it.</span>`;
  const chips = others.map(c => `<button type="button" class="am2-chip ${selected.includes(c) ? 'on' : ''}" data-am2-addcluster="${esc(c)}">${selected.includes(c) ? '✓ ' : '+ '}${esc(c)}</button>`).join('');
  const primary = D.phase === 2 ? `Apply to ${esc(D.entries.map(e => e.cluster).join(', '))} →` : 'Check against cluster →';
  const writer = !!(live || A.context);
  return `<div class="am2-df"><button type="button" class="am2-btn" ${close}>Cancel</button><span class="am2-sub" style="margin-left:8px">Write to</span>${sel}${chips}<span style="flex:1"></span><button type="button" class="am2-btn" data-am2-download>Download YAML</button><button type="button" class="am2-btn p" id="am2-primary" data-am2-primary ${D.busy ? 'disabled' : ''} title="${writer ? '' : 'Open with --kubeconfig to write to this cluster'}">${D.busy ? 'Working…' : primary}</button></div>`;
}

// ---------- actions ----------
const am2Rerender = () => render();

async function am2Check() {
  const D = am2D(), A = S.ambient;
  if (D.busy) return;
  D.busy = true; D.error = ''; am2Rerender();
  try {
    amCapture(); am2CaptureExtra();
    const req = amRead();
    let entries;
    if (A.tasks['global-service'] && !A.mode) { await amGenerateGlobalBatch(); entries = A.globalBatch; }
    else {
      const clusters = amConfigurationClusters(req);
      entries = [];
      for (const cluster of clusters) {
        const q = await amConfigurationRequestForCluster(req, cluster);
        const context = cluster === req.cluster ? (A.context || amLiveContext(cluster, A.contexts)?.name || '') : (amLiveContext(cluster, A.contexts)?.name || '');
        entries.push({cluster, context, plan: await apiPost('api/ambient/plan', q), validation: null, state: 'Preview', error: ''});
      }
    }
    for (const e of entries) if (!e.plan) throw new Error(e.error || 'The preview failed.');
    D.entries = entries;
    await amValidatePlans(entries, true);
    D.phase = 2;
  } catch (e) {
    D.error = e.message; D.phase = 1;
  }
  D.busy = false; am2Rerender();
}

async function am2Apply() {
  const D = am2D(), A = S.ambient;
  if (D.busy) return;
  const entries = D.entries.filter(e => !e.record);
  D.busy = true; D.error = ''; am2Rerender();
  try {
    await amValidatePlans(entries);
    if (!await amConfirmApply(entries)) { D.busy = false; am2Rerender(); return; }
    for (const e of entries) {
      try {
        const r = await apiPost('api/ambient/test', {operation: 'apply', context: e.context, validation: e.validation.token});
        e.record = r.test; e.state = 'Applied';
      } catch (err) { e.state = 'Failed'; e.error = err.message; D.error = (D.entries.length > 1 ? e.cluster + ': ' : '') + err.message; if (entries.length > 1) D.error = 'Applied to ' + entries.filter(x => x.record).map(x => x.cluster).join(', ') + ' but not ' + e.cluster + '. ' + D.error; break; }
    }
    A.tests = await api('api/ambient/tests').catch(() => A.tests);
    if (entries.some(e => e.record)) { D.phase = 3; S.am2.data = null; S.ambient.inventory = null; am2StatusAndTests(); }
  } catch (e) { D.error = e.message; }
  D.busy = false; am2Rerender();
}

// After apply: poll the record status and run the saved or generated tests.
async function am2StatusAndTests() {
  const D = am2D();
  for (const e of D.entries.filter(x => x.record)) {
    try {
      const r = await apiPost('api/ambient/test', {operation: 'status', context: e.context, validation: e.record.token});
      const wp = e.plan?.request?.service ? 'The waypoint' : 'The cluster';
      e.status = {title: e.plan.request.tasks?.includes('weighted-routing') || e.plan.request.tasks?.includes('timeouts') ? 'Route accepted' : 'Waypoint picked it up', sub: `${e.cluster} · ${Array.isArray(r) ? r.length : (r.items || []).length || 1} resource${(r.items || []).length === 1 ? '' : 's'} read back`};
    } catch (_) { e.status = {title: 'Applied', sub: 'Status could not be read back'}; }
  }
  await am2RunTests();
  am2Rerender();
}

function am2TestRefs(entry) {
  const q = entry.plan.request, refs = [];
  for (const o of entry.plan.objects || []) if (['AuthorizationPolicy', 'HTTPRoute'].includes(o.kind)) refs.push([entry.cluster, o.kind, o.metadata.namespace || q.namespace, o.metadata.name].join('/'));
  for (const c of entry.plan.changes || []) if (['AuthorizationPolicy', 'HTTPRoute'].includes(c.ref.kind)) refs.push([entry.cluster, c.ref.kind, c.ref.namespace, c.ref.name].join('/'));
  return [...new Set(refs)];
}
async function am2RunTests() {
  const D = am2D(), rows = [];
  let from = '', failure = '', total = 0, passed = 0, ok = true;
  const e = D.entries.find(x => x.record) || D.entries[0];
  if (!e) return;
  for (const ref of am2TestRefs(e)) {
    try {
      const r = await apiPost('api/ambient/suites/run', {ref, all: true, context: e.context, save: true});
      const cases = Object.fromEntries((r.suite?.cases || []).map(c => [c.id, c]));
      from = from || (r.suite?.source?.ref ? `from ${r.suite.source.ref.namespace}/${r.suite.source.ref.name} · in-mesh curl` : '');
      for (const res of r.results || []) {
        const c = cases[res.id] || {}, pass = res.status === 'pass', hdr = Object.entries(c.request?.headers || {}).map(([k, v]) => `${k}: ${v}`).join(', ');
        const status = res.got?.status;
        rows.push({request: `${c.request?.method || 'GET'} ${c.request?.path || '/'}`, note: hdr, expect: c.expect?.text || String(c.expect?.status || ''), got: res.got?.text || String(status || res.status), gotTone: status ? (status < 400 ? 'ok' : 'bad') : 'mut', decided: res.decidedBy || '', ok: pass, message: res.message});
        total++; if (pass) passed++;
      }
    } catch (err) { D.testsError = err.message; }
  }
  const bad = rows.find(x => !x.ok && x.message);
  if (bad) failure = `<b>${esc(bad.message.split('. ')[0].replace(/\.$/, ''))}.</b> ${esc(bad.message.split('. ').slice(1).join('. '))}`;
  D.results = rows.length ? {rows, from, failure} : null;
  if (total && e?.record?.token && e.context) apiPost('api/ambient/test', {operation: 'verified', context: e.context, validation: e.record.token, verified: passed === total}).then(r => { e.record.verified = passed === total; }).catch(() => {});
  D.testsRow = total ? `<div class="am2-case">${am2Pill(passed === total ? 'ok' : 'warn', `${passed} / ${total}`)}<b style="width:210px">Tests</b><span class="am2-sub">${passed === total ? 'All matched what we expected' : `${total - passed} did not match what we expected`}</span></div>` : '';
}

async function am2DemoPhase3(route, ref, cfg) {
  const D = am2D(), A = S.ambient, cluster = ref.cluster;
  const fake = {cluster, context: A.context || cluster, plan: {id: 'demo', request: {cluster, namespace: ref.namespace, service: ref.name, tasks: ['methods']}, objects: [{kind: 'AuthorizationPolicy', metadata: {namespace: ref.namespace, name: ref.name + '-methods'}}], changes: [], interactions: [{level: 'info', text: 'x', followUpTask: 'require-waypoint', followUpLabel: 'Also add "Block traffic that skips the waypoint"'}], effects: []}, validation: {server: 'https://127.0.0.1:56674'}, state: 'Applied', record: {applyFinished: Date.now(), created: [{kind: 'AuthorizationPolicy', namespace: ref.namespace, name: ref.name + '-methods'}], token: 'demo'}, status: {title: 'Waypoint picked it up', sub: `${cfg?.header?.waypoint?.name || 'waypoint'} status: WaypointAccepted · 3s`}};
  D.entries = [fake]; D.plans = [{cluster, plan: fake.plan}]; D.phase = 3;
  const row = (request, note, expect, got, tone, decided, ok) => ({request, note, expect, got, gotTone: tone, decided, ok});
  D.results = {from: `from ${ref.namespace}/productpage-v1-6d8… · in-mesh curl`, rows: [row('GET /reviews/0', '', '200', '200', 'ok', `${ref.name}-methods ALLOW`, true), row('HEAD /reviews/0', '', '200', '200', 'ok', `${ref.name}-methods ALLOW`, true), row('POST /reviews/0', '', '403', '403', 'bad', 'no ALLOW matched', true), row('DELETE /reviews/0', '', '403', '403', 'bad', 'no ALLOW matched', true), row('GET /reviews/0', 'color: red', '403', '200', 'ok', 'existing DENY did not match', false)], failure: '<b>The existing DENY did not fire.</b> Waypoint log shows header <span class="am2-mono">Color: Red</span> was sent with capitals; the rule matches the exact value <span class="am2-mono">red</span>. <a class="am2-ea">Open the DENY policy</a> · <a class="am2-ea">View waypoint log line</a>'};
  D.testsRow = `<div class="am2-case">${am2Pill('warn', '4 / 5')}<b style="width:210px">Tests</b><span class="am2-sub">1 did not match what we expected</span></div>`;
}

// The field controls that the old wizard wired in after.ambient, without its picker or review panels.
function am2WireFields() {
  const A = S.ambient, redo = fn => { try { amCapture(); fn(); amInvalidate(); } catch (e) { toast(e.message); return; } am2Rerender(); };
  if (typeof amP1Fields === 'function') amP1Fields();
  if (typeof amWireResilience === 'function') amWireResilience();
  if (typeof amWireAccessRule === 'function') amWireAccessRule();
  if (typeof amWireFormHelpers === 'function') amWireFormHelpers();
  if (typeof amWireGlobalPublication === 'function') amWireGlobalPublication();
  $('#am-add-backend')?.addEventListener('click', () => redo(() => { const s = amService(); A.settings.backends.push({name: s.name, port: s.ports[0], weight: 0}); }));
  document.querySelectorAll('[data-amremove]').forEach(b => b.onclick = () => redo(() => A.settings.backends.splice(Number(b.dataset.amremove), 1)));
  document.querySelectorAll('[data-amname]').forEach(el => el.onchange = () => redo(() => { const i = Number(el.closest('[data-ambackend]').dataset.ambackend), backend = A.settings.backends[i], svc = amService(), chosen = A.inventory.services.find(s => s.cluster === svc.cluster && s.namespace === svc.namespace && s.name === backend.name); if (chosen && !chosen.ports.includes(backend.port)) backend.port = chosen.ports[0]; }));
  for (const id of ['mirrorService', 'headerService', 'parentKind', 'parentGateway']) $('#am-' + id)?.addEventListener('change', () => redo(() => {}));
  $('#am-enrol-all')?.addEventListener('click', () => redo(() => { const o = amTarget(); A.settings.enrolNamespaces = A.inventory.namespaces.filter(n => n.cluster === o.cluster).map(n => n.name); }));
  $('#am-enrol-none')?.addEventListener('click', () => redo(() => { A.settings.enrolNamespaces = []; }));
}

function am2DrawerAfter(route) {
  const root = $('#am2-root'), dr = $('#am2-drawer');
  const D = am2D();
  if (!route.drawer) return;
  if (!dr) return;
  const closeDrawer = () => {
    try { amCapture(); D.drafts[D.key] = JSON.parse(JSON.stringify({settings: S.ambient.settings, tasks: S.ambient.tasks})); } catch (_) { /* draft is best effort */ }
    location.hash = am2CloseHash(route);
  };
  document.querySelectorAll('[data-am2-close]').forEach(b => b.onclick = e => { e.preventDefault(); closeDrawer(); });
  document.onkeydown = e => { if (e.key === 'Escape' && $('#am2-drawer') && !$('#modal-root .modal')) closeDrawer(); };
  const body = $('#am2-formcol');
  if (body) { body.scrollTop = D.scroll || 0; body.onscroll = () => { D.scroll = body.scrollTop; }; }
  // field interactions
  const box = $('#am-parameters, #am-setup-parameters, #am-repair-parameters, #am2-params');
  if (box && D.phase === 1) {
    let t; const later = ms => { clearTimeout(t); t = setTimeout(am2DrawerLive, ms); };
    box.addEventListener('input', () => later(350)); box.addEventListener('change', e => {
      const t2 = e.target;
      if (t2.name === 'am2-methodAction') { S.ambient.settings.methodAction = t2.value; box.querySelectorAll('.am2-radio').forEach(r => r.classList.toggle('on', r.contains(t2))); }
      if (t2.name === 'am-mtls-mode') { S.ambient.settings.mtlsMode = t2.value; box.querySelectorAll('.am2-radio').forEach(r => r.classList.toggle('on', r.contains(t2))); }
      if (t2.closest('[data-ammethods]')) { const lab = t2.closest('label'); lab.classList.toggle('on', t2.checked); lab.firstChild && (lab.lastChild.textContent = lab.lastChild.textContent.replace(/^✓ /, '')); lab.lastChild.textContent = (t2.checked ? '✓ ' : '') + t2.value; }
      try { amCapture(); amInvalidate(); } catch (_) { /* invalid mid-edit */ }
      if (['am-trafficMode', 'am-wpMode', 'am-wpClass', 'am-wpExisting', 'am-wpScope', 'am-wpNamespace', 'am-wpName', 'am-targetNamespace', 'am-repairOperation', 'am-selectedPod'].includes(t2.id)) { if (t2.id === 'am-selectedPod') delete S.ambient.settings.selector; am2Rerender(); return; }
      later(150);
    });
    am2WireFields();
    later(30);
  }
  $('#am2-context')?.addEventListener('change', e => { S.ambient.context = e.target.value; D.entries.forEach(x => { x.validation = null; }); });
  document.querySelectorAll('[data-am2-addcluster]').forEach(b => b.onclick = () => {
    const c = b.dataset.am2Addcluster, A = S.ambient, cur = A.settings.applyClusters || [am2SvcRef(route.svc).cluster];
    A.settings.applyClusters = cur.includes(c) ? cur.filter(x => x !== c) : [...cur, c];
    if (!A.settings.applyClusters.length) A.settings.applyClusters = [am2SvcRef(route.svc).cluster];
    am2Rerender();
  });
  document.querySelectorAll('[data-am2-nstask]').forEach(b => b.onclick = () => { D.task = b.dataset.am2Nstask; S.ambient.mode = D.task; S.ambient.settings = {}; $('.am2-menu')?.removeAttribute('open'); am2Rerender(); });
  const prim = $('[data-am2-primary]');
  if (prim) prim.onclick = () => D.phase === 2 ? am2Apply() : am2Check();
  const dl = $('[data-am2-download]');
  if (dl) dl.onclick = () => { const y = $('#am-live-yaml')?.textContent; if (y) amDownload(y, 'flightbay-ambient.yaml'); };
  $('[data-am2-rerun]')?.addEventListener('click', async () => { await am2RunTests(); am2Rerender(); });
  $('[data-am2-undo]')?.addEventListener('click', async () => {
    const e = D.entries.find(x => x.record); if (!e || D.busy) return;
    D.busy = true; D.error = '';
    try { await apiPost('api/ambient/test', {operation: 'cleanup', context: e.context, validation: e.record.token}); S.am2.data = null; S.ambient.inventory = null; D.phase = 1; D.entries = []; toast('Change undone'); } catch (err) { D.error = err.message; }
    D.busy = false; am2Rerender();
  });
  document.querySelectorAll('[data-am2-same]').forEach(b => b.onclick = () => { S.ambient.settings.applyClusters = [b.dataset.am2Same]; D.phase = 1; D.entries = []; D.results = null; am2Check(); });
  const sw = $('.am2-menu');
  if (sw) document.addEventListener('click', ev => { if (!sw.contains(ev.target)) sw.removeAttribute('open'); }, {once: true});
}
AM2_AFTER.push(am2DrawerAfter);
