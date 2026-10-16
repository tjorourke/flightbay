'use strict';

// Snapshot analysis and explicitly selected real-cluster test actions are
// separate. Changing any choice invalidates the validated test plan.
S.ambient = {inventory: null, key: '', route: '', tab: 'investigate', tasks: {}, settings: {}, plan: null, validation: null, context: '', contexts: null, tests: [], status: null, error: '', busy: false};
const amKey = s => JSON.stringify([s.cluster, s.namespace, s.name]);
const amService = () => S.ambient.inventory?.services.find(s => amKey(s) === S.ambient.key);
const amQuery = () => { const s = amService(); return s ? {cluster: s.cluster, namespace: s.namespace, service: s.name, route: S.ambient.route} : {}; };
const amState = s => `<span class="badge ${s === 'pass' ? 'green' : s === 'fail' ? 'red' : s === 'warning' ? 'amber' : 'muted'}">${esc({pass: 'Pass', fail: 'Failed', warning: 'Check', unknown: 'Unknown', na: 'Not applicable'}[s] || s)}</span>`;
function amInvalidate() { S.ambient.plan = null; S.ambient.validation = null; $('#am-plan')?.replaceChildren(); }
function ambientObjectButton(o) { return o.kind === 'Service' ? '<button id="d-ambient">Investigate ambient →</button>' : ''; }
function ambientWireObject(o) { if (!$('#d-ambient')) $('#d-describe')?.insertAdjacentHTML('afterend', ambientObjectButton(o)); const b = $('#d-ambient'); if (b) b.onclick = () => { S.ambient.key = amKey(o); S.ambient.route = ''; amInvalidate(); closeDrawer(); nav('ambient'); }; }
function amDownload(text, name) { const url = URL.createObjectURL(new Blob([text], {type: 'application/yaml'})); const a = document.createElement('a'); a.href = url; a.download = name; a.click(); setTimeout(() => URL.revokeObjectURL(url), 1000); }

async function ambientPage() {
  const A = S.ambient;
  if (!A.inventory) A.inventory = await api('api/ambient');
  const services = A.inventory.services.filter(s => !S.cluster || s.cluster === S.cluster);
  const svc = services.find(s => amKey(s) === A.key);
  const select = `<section class="panel"><div class="panel-body"><label>Service to investigate or configure <select id="am-service" style="min-width:300px"><option value="">Choose a captured Service…</option>${services.map(s => `<option value="${esc(amKey(s))}" ${svc === s ? 'selected' : ''}>${esc(s.cluster)} · ${esc(s.namespace)}/${esc(s.name)}</option>`).join('')}</select></label><p class="muted">Works offline from captured objects. Live tests use a separate, explicitly selected local kubeconfig context; they never write through the snapshot API.</p></div></section>`;
  const tabs = `<nav class="pst-tabs">${[['investigate', 'Guided investigation'], ['wizard', 'Configuration wizard']].map(([id, label]) => `<button class="pst-tab ${A.tab === id ? 'on' : ''}" data-amtab="${id}">${label}</button>`).join('')}</nav>`;
  if (A.tab === 'wizard') {
    if (!A.contexts) A.contexts = await api('api/ambient/contexts').catch(e => { A.error = e.message; return []; });
    A.tests = await api('api/ambient/tests').catch(() => A.tests);
  }
  let body = '<div class="panel empty"><strong>Choose a Service above</strong>The investigation traces enrolment, waypoint selection, route attachment, policies and endpoints. The wizard explains which L4/L7 controls this Service can use.</div>';
  if (svc) {
    if (A.tab === 'investigate') {
      const routes = A.inventory.routes.filter(r => r.cluster === svc.cluster && r.namespace === svc.namespace);
      const result = await api('api/ambient/investigate', amQuery()); A.result = result;
      body = `<div class="toolbar"><label>HTTPRoute <select id="am-route"><option value="">Related routes</option>${routes.map(r => `<option ${A.route === r.name ? 'selected' : ''}>${esc(r.name)}</option>`).join('')}</select></label><a class="help-link" href="#proxies">Inspect delivered proxy config →</a></div>
        <div class="row wrap">${['fail', 'warning', 'unknown', 'pass'].map(s => `${amState(s)} ${num(result.counts[s])}`).join(' · ')}</div>
        ${result.steps.some(s => s.state === 'fail') ? `<div class="notice bad mt"><div><strong>First failing check: ${esc(result.steps.find(s => s.state === 'fail').title)}</strong><p>${esc(result.steps.find(s => s.state === 'fail').detail)}</p><button data-amjump="${result.steps.findIndex(s => s.state === 'fail')}">Inspect the failing step ↓</button></div></div>` : ''}
        ${result.notes.map(n => `<p class="muted">${esc(n)}</p>`).join('')}
        ${result.steps.map((s, i) => `<section class="panel mt" id="am-step-${i}"><div class="panel-head"><h2>${amState(s.state)} ${esc(s.title)}</h2></div><div class="panel-body"><p>${esc(s.detail)}</p>${(s.evidence || []).map(e => `<div class="related"><button class="link mono" data-amevidence="${attr(e.ref)}">${esc(e.ref.kind)} ${esc(e.ref.namespace)}/${esc(e.ref.name)}</button><span class="mono">${esc(e.field)}</span><code style="overflow-wrap:anywhere">${esc(e.value)}</code></div>`).join('')}${s.fixYaml ? `<details class="mt"><summary>Suggested Service-parent change, only if this route is intended for this Service</summary><p>Review this merge snippet against the original route. It preserves the other parent references and does not change rules. It has not been applied.</p><pre class="code">${esc(s.fixYaml)}</pre><button data-amfix="${i}">Download suggested YAML</button></details>` : ''}</div></section>`).join('')}`;
    } else {
      A.capabilities = await api('api/ambient/capabilities', amQuery());
      body = amWizard(svc);
    }
  }
  if (!svc && A.tab === 'wizard') body += amTests();
  return head('Ambient', 'Investigate the attachment chain or generate a testable configuration plan for a deployed Service.', '<button id="am-refresh">Refresh captured inventory</button>') + select + tabs + (A.error ? `<div class="notice bad mt">${esc(A.error)}</div>` : '') + body;
}

function amWizard(svc) {
  const A = S.ambient, F = A.settings, selected = id => !!A.tasks[id];
  const input = (id, label, value, type = 'text') => `<label>${label}<input id="am-${id}" type="${type}" value="${esc(value)}" style="width:180px"></label>`;
  const opts = (xs, v) => xs.map(x => `<option value="${esc(x)}" ${String(v) === String(x) ? 'selected' : ''}>${esc(x)}</option>`).join('');
  const others = A.inventory.services.filter(s => s.cluster === svc.cluster && s.namespace === svc.namespace);
  const backends = F.backends || [svc, others.find(s => s.name !== svc.name) || svc].map((s, i) => ({name: s.name, port: s.ports[0], weight: i ? 10 : 90}));
  const ns = A.inventory.namespaces.filter(n => n.cluster === svc.cluster).map(n => n.name);
  const choices = `<section class="panel mt"><div class="panel-head"><h2>1. Choose tasks and enforcement points</h2></div><div class="panel-body">${A.capabilities.map(c => `<label class="am-task"><input type="checkbox" data-amtask="${esc(c.id)}" ${selected(c.id) ? 'checked' : ''} ${c.supported ? '' : 'disabled'}><span><strong>${esc(c.title)}</strong> <span class="badge">${esc(c.layer)}</span> ${c.supported ? '' : '<span class="badge amber">Unavailable</span>'}<small class="muted" style="display:block">${esc(c.reason)}</small></span></label>`).join('')}</div></section>`;
  const params = `<section class="panel mt"><div class="panel-head"><h2>2. Configure the selected Service</h2></div><div class="panel-body" id="am-parameters"><div class="row wrap"><label>Service port <select id="am-port">${opts(svc.ports, F.port || svc.ports[0])}</select></label>
    ${selected('l4-allow-namespace') ? `<label>Source namespace <select id="am-source">${opts(ns, F.sourceNamespace)}</select></label>` : ''}
    ${selected('methods') ? input('methods', 'Allowed HTTP methods (comma separated)', F.methods || 'GET,HEAD') : ''}
    ${selected('timeouts') ? input('timeout', 'Request timeout', F.timeout || '5s') : ''}
    ${selected('load-balancing') ? `<label>Endpoint algorithm <select id="am-algorithm">${opts(['ROUND_ROBIN', 'LEAST_REQUEST', 'RANDOM'], F.algorithm)}</select></label>` : ''}
    ${selected('rate-limit') ? input('requests', 'Requests per proxy', F.requests || 10, 'number') + `<label>Window <select id="am-unit">${opts(['Seconds', 'Minutes', 'Hours'], F.unit)}</select></label>` : ''}
    ${selected('locality-failover') ? `<label>From region <select id="am-from">${opts(svc.regions || [], F.fromRegion)}</select></label><label>Fallback region <select id="am-to">${opts(svc.regions || [], F.toRegion)}</select></label>` : ''}</div>
    ${selected('weighted-routing') ? `<h3 class="mt">Backend Services (weights must total 100)</h3>${backends.map((b, i) => `<div class="row wrap mt" data-ambackend="${i}"><select data-amname>${others.map(s => `<option ${b.name === s.name ? 'selected' : ''}>${esc(s.name)}</option>`).join('')}</select><label>Service port <input data-amport type="number" value="${esc(b.port)}"></label><label>Weight <input data-amweight type="number" value="${esc(b.weight)}" min="0" max="100"></label><button data-amremove="${i}">Remove</button></div>`).join('')}<button class="mt" id="am-add-backend">Add backend Service</button>` : ''}
    <p class="muted">Failover by endpoint ejection, locality failover and weighted service routing have different semantics. The generated notes describe the chosen behaviour; the wizard never treats a zero-weight backend as a standby.</p><button class="primary mt" id="am-generate" ${A.busy ? 'disabled' : ''}>Generate YAML</button></div></section>`;
  return choices + params + `<div id="am-plan">${A.plan ? amPlanPanel() : ''}</div>` + amTests();
}
function amPlanPanel() {
  const A = S.ambient, p = A.plan;
  return `<section class="panel mt"><div class="panel-head"><h2>3. Review generated YAML</h2><div class="row"><button id="am-copy">Copy YAML</button><button id="am-download">Download YAML</button></div></div><div class="panel-body">${p.notes.map(n => `<p>${esc(n)}</p>`).join('')}<pre class="code">${esc(p.yaml)}</pre></div></section>
    <section class="panel mt"><div class="panel-head"><h2>4. Optional: apply for testing</h2></div><div class="panel-body"><div class="notice info"><div>This step uses your real local kubeconfig, separately from the read-only workspace. Policies and routes can affect real traffic. Choose the target context, run server-side dry-run, then apply exactly this generated plan. No existing objects will be overwritten.</div></div>
    <label>Real target context <select id="am-context"><option value="">Choose explicitly…</option>${(A.contexts || []).map(c => `<option value="${esc(c.name)}" ${c.name === A.context ? 'selected' : ''}>${esc(c.name)} — ${esc(c.server)}</option>`).join('')}</select></label>
    <div class="row mt"><button id="am-validate" ${A.busy || !A.context ? 'disabled' : ''}>Validate with server dry-run</button><button class="primary" id="am-apply" ${A.busy || !A.validation ? 'disabled' : ''}>Apply for testing</button></div>
    ${A.validation ? `<p class="green">Validated against ${esc(A.validation.context)} · ${esc(A.validation.server)}. Validation expires after 10 minutes. Applying creates only the listed test resources.</p>` : ''}${A.message ? `<p>${esc(A.message)}</p>` : ''}</div></section>`;
}
function amTests() { const A = S.ambient; return `<section class="panel mt"><div class="panel-head"><h2>Active test resources</h2></div><div class="panel-body"><p>Recorded in your local Flightbay home, independently of this capture. Tests can be inspected and cleaned up after reopening the desk.</p>${A.tests.length ? A.tests.map((t, i) => `<div class="notice info"><div><strong>${esc(t.context)}</strong> · ${esc(t.plan.request.namespace)}/${esc(t.plan.request.service)}<br><span class="mono">${t.created.map(o => esc(o.kind + ' ' + o.name)).join(', ')}</span><div class="row mt"><button data-amstatus="${i}">Refresh attachment status</button><button data-amcleanup="${i}">Remove these test resources</button></div></div></div>`).join('') : '<p class="muted">No active tests recorded.</p>'}${A.status ? `<h3>Observed test-resource status</h3><p>Creation and Accepted conditions do not prove runtime behaviour. Exercise traffic separately.</p><pre class="code">${esc(JSON.stringify(A.status, null, 2))}</pre>` : ''}</div></section>`; }
function amRead() {
  const A = S.ambient, F = A.settings;
  const get = id => $('#am-' + id)?.value;
  F.port = Number(get('port')); F.sourceNamespace = get('source'); F.methods = get('methods'); F.timeout = get('timeout'); F.algorithm = get('algorithm'); F.requests = Number(get('requests')); F.unit = get('unit'); F.fromRegion = get('from'); F.toRegion = get('to');
  if (A.tasks['weighted-routing']) F.backends = [...document.querySelectorAll('[data-ambackend]')].map(r => ({name: r.querySelector('[data-amname]').value, port: Number(r.querySelector('[data-amport]').value), weight: Number(r.querySelector('[data-amweight]').value)}));
  const s = amService();
  return {cluster: s.cluster, namespace: s.namespace, service: s.name, port: F.port, tasks: Object.keys(A.tasks).filter(k => A.tasks[k]), ...(A.tasks['weighted-routing'] ? {backends: F.backends} : {}), ...(F.sourceNamespace ? {sourceNamespace: F.sourceNamespace} : {}), ...(F.methods ? {methods: F.methods.split(',').map(x => x.trim()).filter(Boolean)} : {}), ...(F.timeout ? {timeout: F.timeout} : {}), ...(F.algorithm ? {algorithm: F.algorithm} : {}), ...(F.requests ? {requests: F.requests} : {}), ...(F.unit ? {unit: F.unit} : {}), ...(F.fromRegion ? {fromRegion: F.fromRegion} : {}), ...(F.toRegion ? {toRegion: F.toRegion} : {})};
}
async function amAction(fn) { const A = S.ambient; if (A.busy) return; A.busy = true; A.error = ''; document.querySelectorAll('#main button, #main input, #main select').forEach(e => e.disabled = true); try { await fn(); } catch (e) { A.error = e.message; if (e.body?.test) A.tests = await api('api/ambient/tests').catch(() => A.tests); } finally { A.busy = false; await render(); } }
async function amTestOperation(operation, rec) { const A = S.ambient; const result = await apiPost('api/ambient/test', {operation, context: rec?.context || A.context, validation: rec?.token || A.validation?.token || '', ...(operation === 'validate' ? {request: A.plan.request, planId: A.plan.id} : {})}); if (operation === 'validate') A.validation = result; else if (operation === 'status') A.status = result; else { A.message = result.message; A.validation = null; A.tests = await api('api/ambient/tests'); } }
after.ambient = () => {
  const A = S.ambient;
  $('#am-refresh').onclick = () => { A.inventory = null; A.validation = null; render(); };
  $('#am-service').onchange = e => { A.key = e.target.value; A.route = ''; A.tasks = {}; A.settings = {}; A.error = ''; amInvalidate(); render(); };
  document.querySelectorAll('[data-amtab]').forEach(b => b.onclick = () => { A.tab = b.dataset.amtab; A.error = ''; render(); });
  const route = $('#am-route'); if (route) route.onchange = e => { A.route = e.target.value; render(); };
  document.querySelectorAll('[data-amevidence]').forEach(b => b.onclick = () => openObject(JSON.parse(b.dataset.amevidence)));
  document.querySelectorAll('[data-amjump]').forEach(b => b.onclick = () => $('#am-step-' + b.dataset.amjump)?.scrollIntoView({block: 'start'}));
  document.querySelectorAll('[data-amfix]').forEach(b => b.onclick = () => amDownload(A.result.steps[Number(b.dataset.amfix)].fixYaml, 'ambient-service-parent-suggestion.yaml'));
  document.querySelectorAll('[data-amtask]').forEach(b => b.onchange = () => { amRead(); A.tasks[b.dataset.amtask] = b.checked; amInvalidate(); render(); });
  document.querySelectorAll('#am-parameters input, #am-parameters select').forEach(e => e.onchange = () => { amRead(); amInvalidate(); });
  const add = $('#am-add-backend'); if (add) add.onclick = () => { amRead(); const s = amService(); A.settings.backends.push({name: s.name, port: s.ports[0], weight: 0}); amInvalidate(); render(); };
  document.querySelectorAll('[data-amremove]').forEach(b => b.onclick = () => { amRead(); A.settings.backends.splice(Number(b.dataset.amremove), 1); amInvalidate(); render(); });
  const gen = $('#am-generate'); if (gen) gen.onclick = () => amAction(async () => { A.plan = await apiPost('api/ambient/plan', amRead()); A.validation = null; A.message = ''; });
  const cp = $('#am-copy'); if (cp) cp.onclick = () => navigator.clipboard.writeText(A.plan.yaml).then(() => toast('YAML copied'), () => toast('Clipboard unavailable'));
  const dl = $('#am-download'); if (dl) dl.onclick = () => amDownload(A.plan.yaml, 'flightbay-ambient-' + A.plan.id + '.yaml');
  const ctx = $('#am-context'); if (ctx) ctx.onchange = () => { A.context = ctx.value; A.validation = null; render(); };
  const validate = $('#am-validate'); if (validate) validate.onclick = () => amAction(() => amTestOperation('validate'));
  const apply = $('#am-apply'); if (apply) apply.onclick = () => amAction(() => amTestOperation('apply'));
  document.querySelectorAll('[data-amstatus]').forEach(b => b.onclick = () => amAction(() => amTestOperation('status', A.tests[Number(b.dataset.amstatus)])));
  document.querySelectorAll('[data-amcleanup]').forEach(b => b.onclick = () => amAction(() => amTestOperation('cleanup', A.tests[Number(b.dataset.amcleanup)])));
};
