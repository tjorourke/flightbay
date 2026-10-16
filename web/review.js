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
    '<div class="notice info"><div>This workspace was streamed in, so there is no file to export from. Open the bundle file with <code>flightbay ui BUNDLE</code>.</div></div>';
  if (!S.rv.data) {
    S.rv.data = await api('api/review');
    S.rv.plan = rvInitPlan(S.rv.data.plan);
    setTimeout(rvPreview, 0);
  }
  if (S.state && S.state.phase !== 'complete' && S.state.phase !== 'live') return head('Review and send', 'Waiting for the bundle to finish loading.') + '<div class="notice info"><div>The bundle is still loading. The review lists everything in it once it has been read and verified.</div></div>';
  const d = S.rv.data;
  const sign = d.signer ? `<span class="green">It will be signed by ${esc(d.signer.name)} (${esc(d.signer.keyId)})</span>.` : '<span class="amber">It will not be signed</span>: choose or create a key under Sign the export.';
  return head('Review and send', 'Choose what leaves your organisation. The original bundle is not changed; the export is a new file with a ledger of what you removed.',
    `<a class="help-link" href="api/review?format=yaml" download="review-plan.yaml" title="Reuse it with flightbay review --plan">Download plan</a>`) + `
  ${d.live ? `<div class="notice info"><div><strong>Frozen from the live mirror at ${fmtDateTime(d.frozen)}.</strong> What you choose here applies to that copy, so the cluster changing does not move things under you. <button class="link" id="rv-refreeze">Freeze again now</button></div></div>` : ''}
  <div class="grid2 rv-top"><section class="panel" id="rv-what">${rvWhat()}</section><section class="panel" id="rv-sign">${rvSignPanel()}</section></div>
  <div id="rv-summary" class="mt">${rvSummary()}</div>
  <section class="panel mt"><div class="tabs" id="rv-tabs">${rvTabs().map(([k, label, n]) => `<button class="${S.rv.tab === k ? 'active' : ''}" data-rvtab="${k}">${esc(label)}${n != null ? ` (${num(n)})` : ''}</button>`).join('')}</div>
    <div class="panel-body" id="rv-body">${rvBody()}</div></section>
  <p class="muted mt" style="font-size:12px">Export writes <span class="mono">${esc(d.out)}</span>. ${sign}${d.encrypted ? ' It is encrypted to the recipients you gave.' : ''}</p>`;
}

// ---------- what to send: one switch per content type ----------
function rvCats() {
  const d = S.rv.data, r = S.rv.plan.remove || {};
  const cnt = tab => { const {k, refs} = rvTabRefsAll(tab); const off = refs.filter(ref => rvHas(k, ref)).length; return {total: refs.length, off}; };
  const events = d.clusters.reduce((n, c) => n + c.events, 0);
  return [
    ['types', 'Objects', ['resource type', 'resource types'], cnt('types')],
    ['logs', 'Logs', ['container log stream', 'container log streams'], cnt('logs')],
    ['events', 'Events', ['event', 'events'], {total: events, off: r.events ? events : 0}],
    ['diagnostics', 'Proxy and control-plane dumps', ['dump file', 'dump files'], cnt('diagnostics')],
    ['flows', 'Flow logs', ['flow file', 'flow files'], cnt('flows')],
  ];
}
// rvTabRefsAll ignores the filter box: the switches act on everything.
function rvTabRefsAll(tab) { const q = S.rv.q; S.rv.q = ''; const out = rvTabRefs(tab); S.rv.q = q; return out; }
function rvSetAll(tab, on) { const q = S.rv.q; S.rv.q = ''; if (tab === 'events') rvRemove().events = !on; else rvSet(tab, on); S.rv.q = q; }
function rvWhat() {
  const cats = rvCats();
  const row = ([tab, label, sub, c]) => {
    const none = !c.total, all = c.off === 0, some = !all && c.off < c.total;
    return `<label class="rv-cat ${none ? 'muted' : ''}"><input type="checkbox" data-rvcat="${tab}" ${none ? 'disabled' : ''} ${all && !none ? 'checked' : ''} ${some ? 'data-some="1"' : ''}>
      <span><strong>${esc(label)}</strong><small class="muted">${none ? 'none in this bundle' : some ? `${num(c.total - c.off)} of ${num(c.total)} ${esc(sub[1])} selected` : all ? `${c.total === 1 ? '1 ' + esc(sub[0]) : 'all ' + num(c.total) + ' ' + esc(sub[1])}` : 'not sent'}</small></span></label>`;
  };
  return `<div class="panel-head"><h2>What to send</h2><div class="row"><button class="ghost" id="rv-all">Select all</button><button class="ghost" id="rv-none">Deselect all</button></div></div>
    <div class="panel-body rv-cats">${cats.map(row).join('')}
    <p class="muted" style="font-size:12px;margin:8px 0 0">Everything is selected to start with. Untick a type of content here, or pick namespaces, resource types, streams and files in the tabs below.</p></div>`;
}
function rvWhatOnly() {
  const el = $('#rv-what'); if (!el) return;
  el.innerHTML = rvWhat(); rvWireWhat();
}
function rvWireWhat() {
  document.querySelectorAll('[data-rvcat]').forEach(c => { if (c.dataset.some) c.indeterminate = true; c.onchange = () => { rvSetAll(c.dataset.rvcat, c.checked); rvChanged(); render(); }; });
  const all = $('#rv-all'); if (all) all.onclick = () => { const r = rvRemove(); for (const k of ['namespaces', 'types', 'logStreams', 'diagnostics', 'flows', 'objects', 'logLines']) r[k] = []; r.events = false; rvChanged(); render(); };
  const none = $('#rv-none'); if (none) none.onclick = () => { for (const t of ['types', 'logs', 'events', 'diagnostics', 'flows']) rvSetAll(t, false); rvChanged(); render(); };
}

// ---------- signing ----------
function rvSignPanel() {
  const k = S.rv.keys, sig = S.rv.data.signer;
  if (!k) return `<div class="panel-head"><h2>Sign the export</h2></div><div class="panel-body muted">Looking for signing keys…</div>`;
  const opts = (k.keys || []).map(x => `<option value="${esc(x.path)}" ${sig && sig.path === x.path ? 'selected' : ''}>${esc(x.name || '(no name)')} · ${esc(x.keyId)} · ${esc(x.path.replace(/^.*\/(?=[^/]+\/[^/]+$)/, '…/'))}</option>`).join('');
  const created = S.rv.created;
  return `<div class="panel-head"><h2>Sign the export</h2>${sig ? `<span class="badge green">✓ ${esc(sig.name)} (${esc(sig.keyId)})</span>` : '<span class="badge amber">Not signed</span>'}</div><div class="panel-body">
    <p class="muted" style="font-size:12px;margin:0 0 10px">A signature lets recipients check the bundle came from you and was not changed. Share your public key (.pub) once; the private key (.key) stays with you.</p>
    <div class="row wrap"><select id="rv-key" style="min-width:320px"><option value="">Don't sign</option>${opts}${sig && !(k.keys || []).some(x => x.path === sig.path) ? `<option selected value="${esc(sig.path || '')}">${esc(sig.name)} · ${esc(sig.keyId)}</option>` : ''}</select></div>
    <div class="row wrap mt"><input id="rv-keypath" placeholder="or a key file: ~/keys/acme.key" style="min-width:260px"><button id="rv-usekey">Use this key</button></div>
    <div class="row wrap mt"><button id="rv-newkey-open" class="primary">Create a signing key…</button>${created ? `<button id="rv-showpub">Show the public key</button>` : ''}</div></div>`;
}
async function rvLoadKeys() {
  try { S.rv.keys = await api('api/review/keys'); } catch (e) { S.rv.keys = {keys: [], keysDir: '~/.flightbay/keys'}; }
  const el = $('#rv-sign'); if (el) { el.innerHTML = rvSignPanel(); rvWireSign(); }
}
async function rvSetKey(body) {
  try {
    const r = await apiPost('api/review/keys', body);
    S.rv.keys = {...S.rv.keys, keys: r.keys}; S.rv.data.signer = r.signer || null;
    if (r.created) S.rv.created = r.created;
    toast(r.signer ? `Exports will be signed by ${r.signer.name} (${r.signer.keyId})` : 'Exports will not be signed');
  } catch (e) { toast(e.message); }
  const el = $('#rv-sign'); if (el) { el.innerHTML = rvSignPanel(); rvWireSign(); }
  rvSummaryOnly();
}
// Creating a key happens in a modal, so nothing on the page (or a live
// refresh) can close it half way.
function rvKeyModal() {
  const dir = S.rv.keys?.keysDir || '~/.flightbay/keys';
  modal('Create a signing key', `<p class="muted" style="margin:0 0 12px">A signing key has two halves. The private key (.key) stays on this machine and signs what you export. Share the public key (.pub) with anyone who needs to verify your bundles.</p>
    <label style="display:block">Who signs with it<input id="rvm-name" placeholder="ACME platform team" style="width:100%;margin-top:6px" autocomplete="off"></label>
    <p class="muted" style="font-size:12px">Shown whenever one of your bundles verifies. Saved in <span class="mono">${esc(dir)}</span>, readable only by you.</p>
    <div id="rvm-err" class="red" style="font-size:13px"></div>
    <div class="row mt" style="justify-content:flex-end"><button id="rvm-cancel">Cancel</button><button class="primary" id="rvm-create">Create and use this key</button></div>`);
  const name = $('#rvm-name'); name.focus();
  $('#rvm-cancel').onclick = closeModal;
  const go = async () => {
    const v = name.value.trim();
    if (!v) { $('#rvm-err').textContent = 'Give the key a name: who signs with it.'; name.focus(); return; }
    $('#rvm-create').disabled = true; $('#rvm-create').textContent = 'Creating…';
    try {
      const r = await apiPost('api/review/keys', {create: {name: v}});
      S.rv.keys = {...S.rv.keys, keys: r.keys}; S.rv.data.signer = r.signer || null; S.rv.created = r.created;
      const el = $('#rv-sign'); if (el) { el.innerHTML = rvSignPanel(); rvWireSign(); }
      rvSummaryOnly();
      rvPubModal(r.created, true);
    } catch (e) { $('#rvm-err').textContent = e.message; $('#rvm-create').disabled = false; $('#rvm-create').textContent = 'Create and use this key'; }
  };
  $('#rvm-create').onclick = go;
  name.onkeydown = e => { if (e.key === 'Enter') go(); };
}
function rvPubModal(c, fresh) {
  modal(fresh ? 'Key created' : 'Your public key', `<p style="margin:0 0 8px">${fresh ? 'Exports from this page are now signed with it. ' : ''}Share this public key with bundle recipients. It is safe to share; the private key never leaves this machine.</p>
    <pre class="code" style="white-space:pre-wrap;word-break:break-all">${esc(c.publicKey)}</pre>
    <dl class="metadata mt" style="font-size:12px"><dt>Public key</dt><dd class="mono">${esc(c.publicPath)}</dd><dt>Private key</dt><dd class="mono">${esc(c.path)} <span class="muted">(keep it, never send it)</span></dd></dl>
    <div class="row mt" style="justify-content:flex-end"><button id="rvm-copy">Copy public key</button><button class="primary" id="rvm-done">Done</button></div>`);
  $('#rvm-copy').onclick = () => navigator.clipboard.writeText(c.publicKey).then(() => toast('Public key copied'), () => toast('Clipboard unavailable'));
  $('#rvm-done').onclick = closeModal;
}

function rvWireSign() {
  const sel = $('#rv-key'); if (sel) sel.onchange = () => rvSetKey(sel.value ? {path: sel.value} : {none: true});
  const use = $('#rv-usekey'); if (use) use.onclick = () => { const v = $('#rv-keypath').value.trim(); if (v) rvSetKey({path: v}); };
  const nk = $('#rv-newkey-open'); if (nk) nk.onclick = rvKeyModal;
  const sp = $('#rv-showpub'); if (sp) sp.onclick = () => rvPubModal(S.rv.created, false);
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
  if (!d) return '';
  if (S.rv.previewErr) return `<div class="notice bad"><div><strong>The plan cannot be applied</strong><br>${esc(S.rv.previewErr)}</div></div>`;
  if (!p) return '<div class="stats"><div class="stat"><div class="eyebrow">What the recipient will receive</div><div class="value">…</div><p>Reading the bundle</p></div></div>';
  const k = p.kept || {}, r = p.removed || {}, x = p.redacted || {};
  const cell = (label, kept, removed) => `<div class="stat"><div class="eyebrow">${label}</div><div class="value">${num(kept)}${removed ? `<small class="amber">${num(removed)} removed</small>` : ''}</div></div>`;
  const red = [x.replacements && `${num(x.replacements)} values redacted`, x.fieldsRemoved && `${num(x.fieldsRemoved)} fields removed`, x.namesScrubbed && `${num(x.namesScrubbed)} names replaced`, r.diagnosticEntries && `${num(r.diagnosticEntries)} dump entries cut`].filter(Boolean);
  const ex = S.rv.exported;
  return `<section class="panel"><div class="panel-head"><h2>What the recipient will receive</h2><div class="row wrap">${S.rv.busy ? '<small class="blue">Updating…</small>' : ''}<button class="primary" id="rv-export" ${S.rv.busy ? 'disabled' : ''}>Export bundle</button></div></div>
    <div class="stats" style="margin:0;border:0;border-radius:0;grid-template-columns:repeat(5,minmax(0,1fr))">${cell('Objects', k.objects, r.objects)}${cell('Events', k.events, r.events)}${cell('Log lines', k.logLines, r.logLines)}${cell('Diagnostic files', k.diagnosticFiles, r.diagnosticFiles)}${cell('Flow files', k.flowFiles, r.flowFiles)}</div>
    <div class="panel-body" style="border-top:1px solid var(--line)"><div class="row wrap"><span>${esc(p.summary || rvLedgerLine(p))}</span>${red.length ? `<span class="muted">· ${red.join(' · ')}</span>` : ''}</div>
    <div class="muted" style="font-size:12px;margin-top:6px">${p.namesDisclosed ? 'The ledger lists removed namespaces, objects and streams by name.' : 'The ledger records counts only; removed names are not listed.'} Resource types you remove are always listed, so Flightbay can mark checks that need them as not applicable.</div>
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

// Ticked means "send it". The plan still records what is removed; a tick
// clears the removal, an untick adds it.
const rvCheck = (removed, data) => `<input type="checkbox" ${removed ? '' : 'checked'} data-rv="${attr(data)}" aria-label="Send">`;
const rvMatch = (...xs) => !S.rv.q || xs.join(' ').toLowerCase().includes(S.rv.q.toLowerCase());
const rvFilter = ph => `<input id="rv-q" placeholder="${esc(ph)}" value="${esc(S.rv.q)}" style="width:320px;margin-bottom:10px">`;
const rvClusterCol = () => S.rv.data.clusters.length > 1;

// The rows of a tab as plan references, for Select all and Deselect all.
function rvTabRefs(tab) {
  const d = S.rv.data;
  switch (tab) {
    case 'namespaces': return {k: 'namespaces', refs: d.clusters.flatMap(c => c.namespaces.filter(n => rvMatch(n.name)).map(n => ({cluster: c.name, namespace: n.name})))};
    case 'types': { const seen = new Map(); d.clusters.forEach(c => c.types.forEach(t => { if (rvMatch(t.kind, t.resource, t.group)) seen.set(t.group + '/' + t.resource, {group: t.group, resource: t.resource}); })); return {k: 'types', refs: [...seen.values()]}; }
    case 'logs': return {k: 'logStreams', refs: d.clusters.flatMap(c => c.streams.filter(x => rvMatch(x.namespace, x.pod, x.container)).map(x => ({cluster: c.name, namespace: x.namespace, pod: x.pod, container: x.container, stream: x.stream})))};
    case 'diagnostics': return {k: 'diagnostics', refs: d.clusters.flatMap(c => c.diagnostics.filter(f => rvMatch(f.namespace, f.pod, f.file, f.component)).map(f => ({cluster: c.name, namespace: f.namespace, pod: f.pod, file: f.file})))};
    case 'flows': return {k: 'flows', refs: d.clusters.flatMap(c => c.flows.map(f => ({cluster: c.name, file: f.file})))};
  }
  return {k: '', refs: []};
}
// rvSet sends (on) or holds back (off) every row of a tab.
function rvSet(tab, on) {
  const {k, refs} = rvTabRefs(tab);
  if (!k) return;
  let l = rvList(k);
  for (const ref of refs) { l = l.filter(x => !rvSame(x, ref) && !(on && rvCovers(x, ref))); if (!on) l.push(ref); }
  rvRemove()[k] = l;
}
const rvBulk = (tab, what) => `<div class="row rv-bulk"><button class="ghost" data-rvbulk="${attr({tab, on: true})}">Select all</button><button class="ghost" data-rvbulk="${attr({tab, on: false})}">Deselect all</button>${S.rv.q ? `<small class="muted">applies to the ${esc(what)} shown by the filter</small>` : ''}</div>`;

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
        <div class="row wrap">${rvFilter('Filter namespaces')}${rvBulk('namespaces', 'namespaces')}<label class="row"><input type="checkbox" id="rv-scrub" ${p.scrubNames ? 'checked' : ''}> Also replace the removed namespaces' names wherever they still appear</label></div>
        <div class="table-scroll" style="max-height:55vh"><table><thead><tr><th>Send</th>${multi ? '<th>Cluster</th>' : ''}<th>Namespace</th><th>Objects</th><th>Log streams</th><th>Log lines</th><th>Events</th></tr></thead><tbody>${rows.join('') || '<tr><td colspan="7" class="muted">No namespaces</td></tr>'}</tbody></table></div>`;
    }
    case 'types': {
      const seen = new Map();
      d.clusters.forEach(c => c.types.forEach(t => { const k = t.group + '/' + t.resource; const e = seen.get(k) || {...t, objects: 0}; e.objects += t.objects; seen.set(k, e); }));
      const rows = [...seen.values()].filter(t => rvMatch(t.kind, t.resource, t.group)).map(t => {
        const ref = {group: t.group, resource: t.resource};
        return `<tr><td>${rvCheck(rvHas('types', ref), {k: 'types', ref})}</td><td>${esc(t.kind)}</td><td class="mono">${esc(t.resource)}${t.group ? '.' + esc(t.group) : ''}</td><td>${t.namespaced ? 'namespaced' : 'cluster'}</td><td>${num(t.objects)}</td></tr>`;
      });
      return `<p class="muted" style="font-size:12px;margin-bottom:10px">A removed type is listed in the ledger by name. Checks that need it show as not applicable instead of failing.</p><div class="row wrap">${rvFilter('Filter types')}${rvBulk('types', 'types')}</div>
        <div class="table-scroll" style="max-height:55vh"><table><thead><tr><th>Send</th><th>Kind</th><th>Resource</th><th>Scope</th><th>Objects</th></tr></thead><tbody>${rows.join('')}</tbody></table></div>`;
    }
    case 'logs': {
      const rows = d.clusters.flatMap(c => c.streams.filter(s => rvMatch(s.namespace, s.pod, s.container)).map(s => {
        const ref = {cluster: c.name, namespace: s.namespace, pod: s.pod, container: s.container, stream: s.stream};
        const viaNs = rvHas('namespaces', {cluster: c.name, namespace: s.namespace});
        return `<tr><td>${viaNs ? '<span class="muted" title="Its namespace is not being sent">✕</span>' : rvCheck(rvHas('logStreams', ref), {k: 'logStreams', ref})}</td>${multi ? `<td><span class="tag">${esc(c.name)}</span></td>` : ''}<td class="mono">${esc(s.namespace)}</td><td class="mono">${esc(s.pod)}</td><td class="mono">${esc(s.container)}</td><td>${esc(s.stream)}</td><td>${num(s.lines)}</td></tr>`;
      }));
      return `<p class="muted" style="font-size:12px;margin-bottom:10px">To remove single lines, find them in the <a href="#logs">Log explorer</a>: a line's detail has <em>Remove this line</em>, and the results have <em>Remove matching lines</em>.</p>
        <div class="row wrap">${rvFilter('Filter by namespace, pod or container')}${rvBulk('logs', 'streams')}</div>
        <div class="table-scroll" style="max-height:55vh"><table><thead><tr><th>Send</th>${multi ? '<th>Cluster</th>' : ''}<th>Namespace</th><th>Pod</th><th>Container</th><th>Stream</th><th>Lines</th></tr></thead><tbody>${rows.join('') || '<tr><td colspan="7" class="muted">No log streams</td></tr>'}</tbody></table></div>`;
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
        <h3>Proxy and control-plane dumps</h3>${rvBulk('diagnostics', 'dumps')}<div class="table-scroll mt" style="max-height:40vh"><table><thead><tr><th>Send</th>${multi ? '<th>Cluster</th>' : ''}<th>Component</th><th>Pod</th><th>File</th><th>Size</th></tr></thead><tbody>${diag.join('') || '<tr><td colspan="6" class="muted">No diagnostics in this bundle</td></tr>'}</tbody></table></div>
        <h3 class="mt">Flow log files</h3>${rvBulk('flows', 'flow files')}<div class="table-scroll mt"><table><thead><tr><th>Send</th>${multi ? '<th>Cluster</th>' : ''}<th>Format</th><th>File</th></tr></thead><tbody>${flows.join('') || '<tr><td colspan="4" class="muted">No flow files in this bundle</td></tr>'}</tbody></table></div>
        <h3 class="mt">Events</h3><label class="row mt"><input type="checkbox" id="rv-events" ${(p.remove || {}).events ? '' : 'checked'}> Send all ${num(events)} events</label>`;
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
        <p class="muted" style="font-size:12px">Off: recipients see how much was removed but not what it was called. On: they can tell which namespaces and objects to ask about.</p>
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
  rvWireWhat();
  if (!S.rv.keys) rvLoadKeys(); else rvWireSign();
  document.querySelectorAll('[data-rvbulk]').forEach(b => b.onclick = () => { const x = JSON.parse(b.dataset.rvbulk); rvSet(x.tab, x.on); rvChanged(); render(); });
  const rf = $('#rv-refreeze');
  if (rf) rf.onclick = async () => { rf.disabled = true; rf.textContent = 'Freezing…'; try { await apiPost('api/review/refreeze', {}); S.rv.data = null; S.rv.preview = null; render(); toast('Frozen again from the live mirror'); } catch (e) { toast(e.message); } };
  document.querySelectorAll('[data-rvtab]').forEach(b => b.onclick = () => { S.rv.tab = b.dataset.rvtab; S.rv.q = ''; render(); });
  document.querySelectorAll('[data-rv]').forEach(c => c.onchange = () => { const d = JSON.parse(c.dataset.rv); rvToggle(d.k, d.ref, !c.checked); if (d.k === 'namespaces') rvNsScrubDefault(); rvWhatOnly(); });
  document.querySelectorAll('[data-unpick]').forEach(b => b.onclick = () => { const d = JSON.parse(b.dataset.unpick); rvList(d.k).splice(d.i, 1); rvChanged(); render(); });
  const q = $('#rv-q');
  if (q) q.oninput = () => { S.rv.q = q.value; const caret = q.selectionStart; render().then(() => refocus('#rv-q', caret)); };
  const on = (id, fn) => { const el = $(id); if (el) el.onchange = () => fn(el); };
  on('#rv-scrub', el => { S.rv.plan.scrubNames = el.checked; rvChanged(); });
  on('#rv-events', el => { rvRemove().events = !el.checked; rvChanged(); rvWhatOnly(); });
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
