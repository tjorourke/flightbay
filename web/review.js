'use strict';
// Review and send: removing things from a bundle opened from a file and
// exporting a new one. Also the trust display: signature, approved policy and
// the review ledger. Loaded after app.js; app.js calls the rv* hooks.

S.rv = {data: null, plan: null, preview: null, previewErr: '', busy: false, tab: 'namespaces', q: '', exported: null, timer: null, seq: 0};

async function apiPost(path, body) {
  const r = await fetch(path, {method: 'POST', credentials: 'same-origin', headers: {'Content-Type': 'application/json', Accept: 'application/json'}, body: JSON.stringify(body)});
  const out = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(out.error || r.statusText);
  return out;
}

// ---------- trust badge (every workspace) ----------
function trustBadge(w) {
  const sig = w.trust?.signature;
  const st = w.state || S.state || {};
  let html = '';
  if (sig && sig.signed) {
    if (!sig.valid) html += `<span class="badge red" title="${esc(sig.error || '')}">✕ Signature invalid</span>`;
    else if (sig.trusted) html += `<span class="badge green" title="Key ${esc(sig.keyId)}${sig.trustedFrom ? ' from ' + esc(sig.trustedFrom) : ''}">✓ Signed by ${esc(sig.signer)} (${esc(sig.keyId)}), unmodified</span>`;
    else html += `<span class="badge amber" title="The content is unmodified since it was signed, but this key is not one you trust. Add the customer's .pub to ~/.flightbay/trusted-keys or pass --verify-key.">Signed by ${esc(sig.signer)} (${esc(sig.keyId)}), key not trusted</span>`;
  } else if (sig && st.phase === 'complete') {
    html += '<span class="badge" title="The customer did not sign this bundle (flightbay collect --sign KEY)">Not signed</span>';
  }
  if (w.review) html += ` <a class="badge blue" href="#coverage" title="Open Coverage for the ledger">Reviewed by ${esc(w.review.reviewer || 'the customer')}${rvLedger(w) ? ': ' + esc(rvShort(rvLedger(w))) : ''}</a>`;
  return html;
}

function rvLedger(w) { return w.trust?.ledger || null; }
function rvShort(l) {
  const r = l.removed || {};
  const bits = [];
  if (r.namespaces) bits.push(`${r.namespaces} namespace${r.namespaces === 1 ? '' : 's'}`);
  if (r.types) bits.push(`${r.types} type${r.types === 1 ? '' : 's'}`);
  if (r.objects) bits.push(`${num(r.objects)} objects`);
  if (r.logLines) bits.push(`${num(r.logLines)} log lines`);
  return bits.length ? bits.join(', ') + ' removed' : 'nothing removed';
}

function onWorkspace(w) {
  const el = $('#trust');
  if (el) el.innerHTML = trustBadge(w);
  const link = document.querySelector('.nav a[data-view="review"]');
  if (link) link.classList.toggle('hidden', !w.exportable);
  if (w.reviewLanding && !rvStarted) {
    rvStarted = true;
    if (!location.hash || location.hash === '#overview') location.hash = 'review';
  }
}
let rvStarted = false;

// ---------- plan helpers ----------
const rvRemove = () => (S.rv.plan.remove = S.rv.plan.remove || {});
const rvList = k => (rvRemove()[k] = rvRemove()[k] || []);
const rvSame = (a, b) => Object.keys({...a, ...b}).every(k => (a[k] || '') === (b[k] || ''));
// rvCovers reports whether a plan entry applies to a row the way the export
// does: an empty field matches anything, and * and ? are globs. A plan written
// by hand or downloaded ({namespace: payments}, {name: debug-*}) shows ticked.
const rvGlob = p => new RegExp('^' + p.replace(/[.+^${}()|[\]\\]/g, '\\$&').replace(/\*/g, '.*').replace(/\?/g, '.') + '$');
const rvCovers = (entry, row) => Object.entries(entry).every(([k, v]) => !v || v === (row[k] || '') || (/[*?]/.test(v) && rvGlob(v).test(row[k] || '')));
const rvHas = (k, ref) => rvList(k).some(x => rvCovers(x, ref));
function rvToggle(k, ref, on) {
  // Turning a row off also drops broader entries that covered it.
  const l = rvList(k).filter(x => !rvSame(x, ref) && !(on === false && rvCovers(x, ref)));
  if (on) l.push(ref);
  rvRemove()[k] = l;
  rvChanged();
}
function rvChanged() {
  clearTimeout(S.rv.timer);
  S.rv.exported = null;
  S.rv.timer = setTimeout(rvPreview, 350);
  rvSummaryOnly();
}
async function rvPreview() {
  const seq = ++S.rv.seq;
  S.rv.busy = true; rvSummaryOnly();
  try {
    const p = await apiPost('api/review/preview', S.rv.plan);
    if (seq !== S.rv.seq) return;
    S.rv.preview = p.ledger; S.rv.previewErr = '';
  } catch (e) {
    if (seq !== S.rv.seq) return;
    S.rv.previewErr = e.message;
  }
  S.rv.busy = false;
  rvSummaryOnly();
}

// A plan as the server stores it, starting empty.
function rvInitPlan(p) {
  p = p || {};
  p.remove = p.remove || {};
  return p;
}

// ---------- the Review page ----------
async function reviewPage() {
  if (!S.ws?.exportable) return head('Review and send', 'Remove objects, logs and namespaces from a bundle and export a new one.') +
    '<div class="notice info"><div>This workspace was not opened from a bundle file (it is a stream or a live mirror), so there is nothing to export from. Open the bundle file with <code>flightbay ui BUNDLE</code>.</div></div>';
  if (!S.rv.data) {
    S.rv.data = await api('api/review');
    S.rv.plan = rvInitPlan(S.rv.data.plan);
    setTimeout(rvPreview, 0);
  }
  if (S.state && S.state.phase !== 'complete') return head('Review and send', 'Waiting for the bundle to finish loading.') + '<div class="notice info"><div>The bundle is still loading. The review lists everything in it once it has been read and verified.</div></div>';
  const d = S.rv.data;
  const sign = d.signer ? `<span class="green">Signed with ${esc(d.signer.name)} (${esc(d.signer.keyId)})</span>` : '<span class="amber">Not signed</span>. Open the bundle with <code>flightbay ui BUNDLE --sign KEY</code> so Solo can check it came from you unmodified.';
  return head('Review and send', 'Choose what leaves your organisation. The original bundle is not changed; the export is a new file with a ledger of what you removed.',
    `<a class="help-link" href="api/review?format=yaml" download="review-plan.yaml" title="Reuse it with flightbay review --plan">Download plan</a>`) + `
  <div id="rv-summary">${rvSummary()}</div>
  <section class="panel mt"><div class="tabs" id="rv-tabs">${rvTabs().map(([k, label, n]) => `<button class="${S.rv.tab === k ? 'active' : ''}" data-rvtab="${k}">${esc(label)}${n != null ? ` (${num(n)})` : ''}</button>`).join('')}</div>
    <div class="panel-body" id="rv-body">${rvBody()}</div></section>
  <p class="muted mt" style="font-size:12px">Export writes <span class="mono">${esc(d.out)}</span>. ${sign}${d.encrypted ? ' It is encrypted to the recipients you gave.' : ''}</p>`;
}

function rvTabs() {
  const d = S.rv.data;
  const sum = f => d.clusters.reduce((n, c) => n + f(c), 0);
  const r = S.rv.plan.remove || {};
  return [
    ['namespaces', 'Namespaces', sum(c => c.namespaces.length)],
    ['types', 'Resource types', sum(c => c.types.length)],
    ['logs', 'Log streams', sum(c => c.streams.length)],
    ['files', 'Diagnostics and flows', sum(c => c.diagnostics.length + c.flows.length)],
    ['picked', 'Picked items', (r.objects || []).length + (r.logLines || []).length],
    ['redact', 'Redact', null],
  ];
}

function rvSummary() {
  const p = S.rv.preview, d = S.rv.data;
  if (S.rv.previewErr) return `<div class="notice bad"><div><strong>The plan cannot be applied</strong><br>${esc(S.rv.previewErr)}</div></div>`;
  if (!p) return '<div class="stats"><div class="stat"><div class="eyebrow">What Solo will receive</div><div class="value">…</div><p>Reading the bundle</p></div></div>';
  const k = p.kept || {}, r = p.removed || {}, x = p.redacted || {};
  const cell = (label, kept, removed) => `<div class="stat"><div class="eyebrow">${label}</div><div class="value">${num(kept)}${removed ? `<small class="amber">${num(removed)} removed</small>` : ''}</div></div>`;
  const red = [x.replacements && `${num(x.replacements)} values redacted`, x.fieldsRemoved && `${num(x.fieldsRemoved)} fields removed`, x.namesScrubbed && `${num(x.namesScrubbed)} names replaced`, r.diagnosticEntries && `${num(r.diagnosticEntries)} dump entries cut`].filter(Boolean);
  const ex = S.rv.exported;
  return `<section class="panel"><div class="panel-head"><h2>What Solo will receive</h2><div class="row wrap">${S.rv.busy ? '<small class="blue">Updating…</small>' : ''}<button class="primary" id="rv-export" ${S.rv.busy ? 'disabled' : ''}>Export bundle</button></div></div>
    <div class="stats" style="margin:0;border:0;border-radius:0;grid-template-columns:repeat(5,minmax(0,1fr))">${cell('Objects', k.objects, r.objects)}${cell('Events', k.events, r.events)}${cell('Log lines', k.logLines, r.logLines)}${cell('Diagnostic files', k.diagnosticFiles, r.diagnosticFiles)}${cell('Flow files', k.flowFiles, r.flowFiles)}</div>
    <div class="panel-body" style="border-top:1px solid var(--line)"><div class="row wrap"><span>${esc(p.summary || rvLedgerLine(p))}</span>${red.length ? `<span class="muted">· ${red.join(' · ')}</span>` : ''}</div>
    <div class="muted" style="font-size:12px;margin-top:6px">${p.namesDisclosed ? 'The ledger lists removed namespaces, objects and streams by name.' : 'The ledger records counts only; removed names are not listed.'} Resource types you remove are always listed, so Solo can mark the checks that need them as not applicable.</div>
    ${ex ? rvExported(ex) : ''}</div></section>`;
}

function rvLedgerLine(l) {
  const r = l.removed || {};
  const bits = [['namespaces', 'namespace'], ['types', 'resource type'], ['objects', 'object'], ['events', 'event'], ['logStreams', 'log stream'], ['logLines', 'log line'], ['diagnosticFiles', 'diagnostic file'], ['flowFiles', 'flow file']]
    .filter(([key]) => r[key]).map(([key, one]) => `${num(r[key])} ${one}${r[key] === 1 ? '' : 's'}`);
  return bits.length ? 'Removing ' + bits.join(', ') : 'Nothing removed yet. Everything in the bundle will be sent.';
}

function rvExported(ex) {
  return `<div class="notice info mt"><div><strong>Exported</strong> ${esc(ex.path)} (${bytes(ex.bytes)})${ex.signed ? `, signed by ${esc(ex.signer)} (${esc(ex.keyId)})` : ', not signed'}${ex.encrypted ? ', encrypted' : ''}.<br>
    SHA-256 <span class="mono">${esc(ex.sha256)}</span><br>Check it before you send it:
<pre class="code">flightbay inspect ${esc(ex.path)}
flightbay ui ${esc(ex.path)}</pre></div></div>`;
}

function rvSummaryOnly() {
  const el = $('#rv-summary'); if (!el) return;
  el.innerHTML = rvSummary();
  rvWireSummary();
  const tabs = $('#rv-tabs');
  if (tabs) tabs.querySelectorAll('[data-rvtab]').forEach((b, i) => { const t = rvTabs()[i]; b.textContent = t[1] + (t[2] != null ? ` (${num(t[2])})` : ''); });
}

const rvCheck = (on, data) => `<input type="checkbox" ${on ? 'checked' : ''} data-rv="${attr(data)}" aria-label="Remove">`;
const rvMatch = (...xs) => !S.rv.q || xs.join(' ').toLowerCase().includes(S.rv.q.toLowerCase());
const rvFilter = ph => `<input id="rv-q" placeholder="${esc(ph)}" value="${esc(S.rv.q)}" style="width:320px;margin-bottom:10px">`;
const rvClusterCol = () => S.rv.data.clusters.length > 1;

function rvBody() {
  const d = S.rv.data, p = S.rv.plan, r = p.remove || {};
  const multi = rvClusterCol();
  switch (S.rv.tab) {
    case 'namespaces': {
      const rows = d.clusters.flatMap(c => c.namespaces.filter(n => rvMatch(n.name)).map(n => {
        const ref = {cluster: c.name, namespace: n.name};
        return `<tr><td>${rvCheck(rvHas('namespaces', ref), {k: 'namespaces', ref})}</td>${multi ? `<td><span class="tag">${esc(c.name)}</span></td>` : ''}<td class="mono">${esc(n.name)}</td><td>${num(n.objects)}</td><td>${num(n.logStreams)}</td><td>${num(n.logLines)}</td><td>${num(n.events)}</td></tr>`;
      }));
      return `<p class="muted" style="font-size:12px;margin-bottom:10px">Removing a namespace removes its objects, events, logs and proxy dumps, and cuts entries naming it from the dumps of other namespaces.</p>
        <div class="row wrap">${rvFilter('Filter namespaces')}<label class="row"><input type="checkbox" id="rv-scrub" ${p.scrubNames ? 'checked' : ''}> Also replace the removed namespaces' names wherever they still appear</label></div>
        <div class="table-scroll" style="max-height:55vh"><table><thead><tr><th>Remove</th>${multi ? '<th>Cluster</th>' : ''}<th>Namespace</th><th>Objects</th><th>Log streams</th><th>Log lines</th><th>Events</th></tr></thead><tbody>${rows.join('') || '<tr><td colspan="7" class="muted">No namespaces</td></tr>'}</tbody></table></div>`;
    }
    case 'types': {
      const seen = new Map();
      d.clusters.forEach(c => c.types.forEach(t => { const k = t.group + '/' + t.resource; const e = seen.get(k) || {...t, objects: 0}; e.objects += t.objects; seen.set(k, e); }));
      const rows = [...seen.values()].filter(t => rvMatch(t.kind, t.resource, t.group)).map(t => {
        const ref = {group: t.group, resource: t.resource};
        return `<tr><td>${rvCheck(rvHas('types', ref), {k: 'types', ref})}</td><td>${esc(t.kind)}</td><td class="mono">${esc(t.resource)}${t.group ? '.' + esc(t.group) : ''}</td><td>${t.namespaced ? 'namespaced' : 'cluster'}</td><td>${num(t.objects)}</td></tr>`;
      });
      return `<p class="muted" style="font-size:12px;margin-bottom:10px">A removed type is listed in the ledger by name. Checks that need it show as not applicable instead of failing.</p>${rvFilter('Filter types')}
        <div class="table-scroll" style="max-height:55vh"><table><thead><tr><th>Remove</th><th>Kind</th><th>Resource</th><th>Scope</th><th>Objects</th></tr></thead><tbody>${rows.join('')}</tbody></table></div>`;
    }
    case 'logs': {
      const rows = d.clusters.flatMap(c => c.streams.filter(s => rvMatch(s.namespace, s.pod, s.container)).map(s => {
        const ref = {cluster: c.name, namespace: s.namespace, pod: s.pod, container: s.container, stream: s.stream};
        const viaNs = rvHas('namespaces', {cluster: c.name, namespace: s.namespace});
        return `<tr><td>${viaNs ? '<span class="muted" title="Its namespace is removed">✓</span>' : rvCheck(rvHas('logStreams', ref), {k: 'logStreams', ref})}</td>${multi ? `<td><span class="tag">${esc(c.name)}</span></td>` : ''}<td class="mono">${esc(s.namespace)}</td><td class="mono">${esc(s.pod)}</td><td class="mono">${esc(s.container)}</td><td>${esc(s.stream)}</td><td>${num(s.lines)}</td></tr>`;
      }));
      return `<p class="muted" style="font-size:12px;margin-bottom:10px">To remove single lines, find them in the <a href="#logs">Log explorer</a>: a line's detail has <em>Remove this line</em>, and the results have <em>Remove matching lines</em>.</p>
        <div class="row wrap">${rvFilter('Filter by namespace, pod or container')}<label class="row"><input type="checkbox" id="rv-alllogs"> Remove every log stream shown</label></div>
        <div class="table-scroll" style="max-height:55vh"><table><thead><tr><th>Remove</th>${multi ? '<th>Cluster</th>' : ''}<th>Namespace</th><th>Pod</th><th>Container</th><th>Stream</th><th>Lines</th></tr></thead><tbody>${rows.join('') || '<tr><td colspan="7" class="muted">No log streams</td></tr>'}</tbody></table></div>`;
    }
    case 'files': {
      const diag = d.clusters.flatMap(c => c.diagnostics.filter(f => rvMatch(f.namespace, f.pod, f.file, f.component)).map(f => {
        const ref = {cluster: c.name, namespace: f.namespace, pod: f.pod, file: f.file};
        return `<tr><td>${rvCheck(rvHas('diagnostics', ref), {k: 'diagnostics', ref})}</td>${multi ? `<td><span class="tag">${esc(c.name)}</span></td>` : ''}<td>${esc(f.component || f.kind)}</td><td class="mono">${esc(f.namespace)}/${esc(f.pod)}</td><td class="mono">${esc(f.file)}</td><td>${bytes(f.bytes)}</td></tr>`;
      }));
      const flows = d.clusters.flatMap(c => c.flows.map(f => {
        const ref = {cluster: c.name, file: f.file};
        return `<tr><td>${rvCheck(rvHas('flows', ref), {k: 'flows', ref})}</td>${multi ? `<td><span class="tag">${esc(c.name)}</span></td>` : ''}<td>${esc(f.kind)}</td><td class="mono">${esc(f.file)}</td></tr>`;
      }));
      const events = d.clusters.reduce((n, c) => n + c.events, 0);
      return `${rvFilter('Filter by component, pod or file')}
        <h3>Proxy and control-plane dumps</h3><div class="table-scroll mt" style="max-height:40vh"><table><thead><tr><th>Remove</th>${multi ? '<th>Cluster</th>' : ''}<th>Component</th><th>Pod</th><th>File</th><th>Size</th></tr></thead><tbody>${diag.join('') || '<tr><td colspan="6" class="muted">No diagnostics in this bundle</td></tr>'}</tbody></table></div>
        <h3 class="mt">Flow log files</h3><div class="table-scroll mt"><table><thead><tr><th>Remove</th>${multi ? '<th>Cluster</th>' : ''}<th>Format</th><th>File</th></tr></thead><tbody>${flows.join('') || '<tr><td colspan="4" class="muted">No flow files in this bundle</td></tr>'}</tbody></table></div>
        <h3 class="mt">Events</h3><label class="row mt"><input type="checkbox" id="rv-events" ${(p.remove || {}).events ? 'checked' : ''}> Remove all ${num(events)} events</label>`;
    }
    case 'picked': {
      const objs = (r.objects || []).map((o, i) => `<tr><td><button class="ghost" data-unpick="${attr({k: 'objects', i})}" title="Keep it">✕</button></td>${multi ? `<td><span class="tag">${esc(o.cluster || '')}</span></td>` : ''}<td class="mono">${esc(o.resource)}${o.group ? '.' + esc(o.group) : ''}</td><td class="mono">${esc(o.namespace || '')}</td><td class="mono">${esc(o.name)}</td></tr>`);
      const lines = (r.logLines || []).length;
      return `<p class="muted" style="font-size:12px;margin-bottom:10px">Objects and lines you removed one by one, from an object's details or the Log explorer.</p>
        <h3>Objects</h3><div class="table-scroll mt" style="max-height:40vh"><table><thead><tr><th></th>${multi ? '<th>Cluster</th>' : ''}<th>Type</th><th>Namespace</th><th>Name</th></tr></thead><tbody>${objs.join('') || '<tr><td colspan="5" class="muted">None. Open an object and choose Remove from the bundle.</td></tr>'}</tbody></table></div>
        <h3 class="mt">Log lines</h3><div class="row mt"><span>${num(lines)} line${lines === 1 ? '' : 's'} picked</span>${lines ? '<button id="rv-clearlines">Keep them all</button>' : ''}</div>`;
    }
    case 'redact': {
      const ta = (id, list, ph) => `<textarea id="${id}" rows="4" style="width:100%" placeholder="${esc(ph)}" spellcheck="false">${esc((list || []).join('\n'))}</textarea>`;
      return `<div class="grid2"><div>
        <h3>Replace text</h3><p class="muted" style="font-size:12px">Regular expressions, one per line. Every match in objects, logs, events and dumps becomes [REDACTED]. The ledger counts replacements and never records the patterns.</p>${ta('rv-redact', p.redact, 'corp-[a-z]+\\.example\\.com\nACME-[0-9]{6}')}
        <h3 class="mt">Drop log lines</h3><p class="muted" style="font-size:12px">Regular expressions, one per line. A log line matching any of them is removed whole.</p>${ta('rv-droplines', p.dropLogLinesMatching, 'customer_id=\nGET /internal/')}
        <h3 class="mt">Remove fields from objects</h3><p class="muted" style="font-size:12px">One path per line, applied to every object, e.g. <span class="mono">metadata.annotations["corp.example/*"]</span> or <span class="mono">spec.template.spec.containers[*].env</span>.</p>${ta('rv-fields', p.dropFields, 'metadata.annotations["corp.example/*"]')}
        <div class="row mt"><button class="primary" id="rv-apply">Apply</button><small class="muted" id="rv-applyhint"></small></div>
      </div><div>
        <h3>Ledger</h3>
        <label class="mt" style="display:block">Reviewed by<br><input id="rv-reviewer" value="${esc(p.reviewer || '')}" placeholder="Name and team" style="width:100%"></label>
        <label class="row mt"><input type="checkbox" id="rv-disclose" ${p.discloseNames ? 'checked' : ''}> List removed namespaces, objects and streams by name</label>
        <p class="muted" style="font-size:12px">Off: Solo sees how much was removed but not what it was called. On: Solo can tell which namespaces and objects to ask about.</p>
      </div></div>`;
    }
  }
  return '';
}

function rvWireSummary() {
  const b = $('#rv-export');
  if (b) b.onclick = async () => {
    b.disabled = true; b.textContent = 'Exporting…';
    try {
      S.rv.exported = await apiPost('api/review/export', S.rv.plan);
      S.rv.preview = S.rv.exported.ledger;
      toast('Exported ' + S.rv.exported.path);
    } catch (e) { toast(e.message); }
    rvSummaryOnly();
  };
}

after.review = () => {
  if (!S.rv.data) return;
  rvWireSummary();
  document.querySelectorAll('[data-rvtab]').forEach(b => b.onclick = () => { S.rv.tab = b.dataset.rvtab; S.rv.q = ''; render(); });
  document.querySelectorAll('[data-rv]').forEach(c => c.onchange = () => { const d = JSON.parse(c.dataset.rv); rvToggle(d.k, d.ref, c.checked); if (d.k === 'namespaces') rvNsScrubDefault(); });
  document.querySelectorAll('[data-unpick]').forEach(b => b.onclick = () => { const d = JSON.parse(b.dataset.unpick); rvList(d.k).splice(d.i, 1); rvChanged(); render(); });
  const q = $('#rv-q');
  if (q) q.oninput = () => { S.rv.q = q.value; const caret = q.selectionStart; render().then(() => refocus('#rv-q', caret)); };
  const on = (id, fn) => { const el = $(id); if (el) el.onchange = () => fn(el); };
  on('#rv-scrub', el => { S.rv.plan.scrubNames = el.checked; rvChanged(); });
  on('#rv-events', el => { rvRemove().events = el.checked; rvChanged(); });
  on('#rv-disclose', el => { S.rv.plan.discloseNames = el.checked; rvChanged(); });
  on('#rv-reviewer', el => { S.rv.plan.reviewer = el.value.trim(); rvChanged(); });
  on('#rv-alllogs', el => {
    S.rv.data.clusters.forEach(c => c.streams.filter(s => rvMatch(s.namespace, s.pod, s.container)).forEach(s => {
      const ref = {cluster: c.name, namespace: s.namespace, pod: s.pod, container: s.container, stream: s.stream};
      rvRemove().logStreams = rvList('logStreams').filter(x => !rvSame(x, ref)).concat(el.checked ? [ref] : []);
    }));
    rvChanged(); render();
  });
  const cl = $('#rv-clearlines'); if (cl) cl.onclick = () => { rvRemove().logLines = []; rvChanged(); render(); };
  const ap = $('#rv-apply');
  if (ap) ap.onclick = () => {
    const lines = id => $(id).value.split('\n').map(s => s.trim()).filter(Boolean);
    Object.assign(S.rv.plan, {redact: lines('#rv-redact'), dropLogLinesMatching: lines('#rv-droplines'), dropFields: lines('#rv-fields')});
    rvChanged();
    $('#rv-applyhint').textContent = 'Applied. The summary above shows the effect.';
  };
};

// Scrubbing names is on by default the first time a namespace is removed.
function rvNsScrubDefault() {
  if (S.rv.plan.scrubNames === undefined) S.rv.plan.scrubNames = true;
}

// ---------- hooks from other pages ----------
function rvObjectButton(o) {
  if (!S.ws?.exportable) return '';
  const ref = {cluster: o.cluster, group: o.group || '', resource: o.resource, namespace: o.namespace || '', name: o.name};
  const picked = S.rv.plan && rvHas('objects', ref);
  return `<button id="d-rvremove" data-ref="${attr(ref)}">${picked ? 'Keep in the bundle' : 'Remove from the bundle'}</button>`;
}
async function rvEnsurePlan() {
  if (!S.rv.data) { S.rv.data = await api('api/review'); S.rv.plan = rvInitPlan(S.rv.data.plan); }
}
function rvWireObject() {
  const b = $('#d-rvremove'); if (!b) return;
  b.onclick = async () => {
    await rvEnsurePlan();
    const ref = JSON.parse(b.dataset.ref);
    const on = !rvHas('objects', ref);
    rvToggle('objects', ref, on);
    b.textContent = on ? 'Keep in the bundle' : 'Remove from the bundle';
    toast(on ? `${ref.name} will not be sent` : `${ref.name} will be sent`);
  };
}
function rvLineButton(r) {
  return S.ws?.exportable ? `<button id="lc-rvremove" data-id="${r.id}">Remove this line from the bundle</button>` : '';
}
function rvWireLine() {
  const b = $('#lc-rvremove'); if (!b) return;
  b.onclick = async () => {
    await rvEnsurePlan();
    const res = await api('api/review/lines', {id: b.dataset.id});
    rvAddLines(res.lines);
    b.disabled = true; b.textContent = 'Removed from the bundle';
  };
}
function rvAddLines(refs) {
  const have = new Set(rvList('logLines').map(l => l.chunk + '#' + l.index));
  let n = 0;
  for (const l of refs) if (!have.has(l.chunk + '#' + l.index)) { rvList('logLines').push(l); n++; }
  rvChanged();
  toast(`${num(n)} line${n === 1 ? '' : 's'} will not be sent (${num(rvList('logLines').length)} picked in all)`);
}
function rvLogsAction() {
  return S.ws?.exportable ? '<button id="log-rvremove" title="Leave every line matching this search out of the reviewed bundle">Remove matching lines</button>' : '';
}
function rvWireLogs() {
  const b = $('#log-rvremove'); if (!b) return;
  b.onclick = async () => {
    await rvEnsurePlan();
    const p = logParams(); delete p.limit; delete p.offset;
    const n = S.logs.total;
    if (!confirm(`Leave out all ${num(n)} matching lines from the bundle you send?`)) return;
    try { rvAddLines((await api('api/review/lines', p)).lines); } catch (e) { toast(e.message); }
  };
}

// ---------- Coverage: trust panel ----------
function rvCoveragePanel() {
  const w = S.ws || {};
  const sig = w.trust?.signature;
  const led = rvLedger(w);
  const ps = w.policySource;
  let sigText = 'Not signed';
  if (sig?.signed) sigText = !sig.valid ? `<span class="red">Signature invalid: ${esc(sig.error || '')}</span>` : `${sig.trusted ? '<span class="green">' : '<span class="amber">'}Signed by ${esc(sig.signer)} (${esc(sig.keyId)}), unmodified${sig.trusted ? ', key trusted' : ', key not in your trusted keys'}</span>`;
  const policy = ps ? `<dt>Policy file</dt><dd>${esc(ps.file)} <span class="mono muted">${esc((ps.sha256 || '').slice(0, 16))}</span>${ps.signature ? `<br><span class="green">Approved by ${esc(ps.signature.signer)} (${esc(ps.signature.keyId)})</span>` : '<br><span class="muted">not signed</span>'}${ps.changedBy?.length ? `<br><span class="amber">Refined by ${esc(ps.changedBy.join(' '))}</span>` : ''}</dd>` : '';
  let ledger = '';
  if (led) {
    const r = led.removed || {}, x = led.redacted || {};
    const row = (label, v) => v ? `<dt>${label}</dt><dd>${typeof v === 'number' ? num(v) : v}</dd>` : '';
    const list = (label, xs) => (xs || []).length ? `<dt>${label}</dt><dd class="mono" style="white-space:normal">${xs.map(esc).join('<br>')}</dd>` : '';
    ledger = `<section class="panel mt"><div class="panel-head"><h2>Removed by the customer</h2><span class="badge">${led.namesDisclosed ? 'names listed' : 'counts only'}</span></div><div class="panel-body"><dl class="metadata">
      <dt>Reviewed by</dt><dd>${esc(led.reviewer || '—')} · ${fmtDateTime(led.reviewedAt)}</dd>
      <dt>Original</dt><dd class="mono" style="white-space:normal">${esc(led.original?.bundleId || '')}<br>manifest ${esc(led.original?.manifestSha256 || '')}${led.original?.signature ? `<br>${esc(led.original.signature.signed ? (led.original.signature.valid ? 'signed by ' + led.original.signature.signer + ' (' + led.original.signature.keyId + ')' : 'signature invalid') : 'not signed')}` : ''}</dd>
      ${row('Namespaces', r.namespaces)}${row('Resource types', r.types)}${row('Objects', r.objects)}${row('Events', r.events)}${row('Log streams', r.logStreams)}${row('Log lines', r.logLines)}${row('Diagnostic files', r.diagnosticFiles)}${row('Dump entries', r.diagnosticEntries)}${row('Flow files', r.flowFiles)}
      ${row('Values redacted', x.replacements)}${row('Fields removed', x.fieldsRemoved)}${row('Names replaced', x.namesScrubbed)}
      ${list('Types', led.types)}${list('Namespace names', led.namespaces)}${list('Object names', led.objects)}${list('Stream names', led.logStreams)}${list('File names', led.files)}
    </dl><p class="muted" style="font-size:12px">Checks that need a removed type, or a type with objects removed, show as not applicable rather than failing.</p></div></section>`;
  }
  return `<section class="panel"><div class="panel-head"><h2>Origin</h2></div><div class="panel-body"><dl class="metadata">
    <dt>Signature</dt><dd>${sigText}</dd>${policy}
    ${w.collectorSha256 ? `<dt>Collector build</dt><dd class="mono" style="white-space:normal">${esc(w.collectorSha256)}</dd>` : ''}
  </dl></div></section>${ledger}`;
}
