'use strict';
// Flightbay start page: what flightbay ui shows when it has nothing to open.
// Loaded only by start.html. When the user picks something the server hands
// over to the desk on the same address and token; this page waits for it.

const $ = s => document.querySelector(s);
const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'}[c]));
const bytes = n => n > 1 << 30 ? (n / (1 << 30)).toFixed(1) + ' GiB' : n > 1 << 20 ? (n / (1 << 20)).toFixed(1) + ' MiB' : n > 1024 ? (n / 1024).toFixed(1) + ' KiB' : n + ' B';
const when = t => (!t || t.startsWith('0001')) ? '' : new Date(t).toISOString().slice(0, 16).replace('T', ' ') + ' UTC';
const cap = s => String(s || '').charAt(0).toUpperCase() + String(s || '').slice(1);
const list = v => String(v || '').split(/[,\n]/).map(s => s.trim()).filter(Boolean);

const ST = {accessSeq: 0, card: '', info: null, contexts: null, files: [], home: '', lastAction: null};

async function call(path, body) {
  const opt = {credentials: 'same-origin', headers: {Accept: 'application/json'}};
  if (body !== undefined) { opt.method = 'POST'; opt.headers['Content-Type'] = 'application/json'; opt.body = JSON.stringify(body); }
  const r = await fetch(path, opt);
  const j = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(j.error || r.statusText);
  return j;
}

function toast(msg) {
  const t = $('#toast');
  t.textContent = msg; t.classList.remove('hidden');
  clearTimeout(toast.t); toast.t = setTimeout(() => t.classList.add('hidden'), 3500);
}

function status(id, msg, cls) {
  const el = $(id);
  el.className = cls || 'muted';
  el.textContent = msg || '';
}

// ---------- the command line equivalent ----------
function sh(a) {
  if (/^[A-Za-z0-9_./:=@%+,-]+$/.test(a)) return a;
  if (/^~\/[A-Za-z0-9_./:=@%+,-]+$/.test(a)) return a; // leave ~ unquoted so the shell expands it
  return "'" + a.replace(/'/g, "'\\''") + "'";
}
function tilde(p) { return ST.home && p.startsWith(ST.home + '/') ? '~' + p.slice(ST.home.length) : p; }

function liveForm() {
  return {kubeconfig: $('#lv-kubeconfig').value.trim(), context: $('#lv-context').value, namespaces: $('#lv-ns').value.trim(),
    excludeNamespaces: $('#lv-exns').value.trim(), clusterScoped: $('#lv-cluster').checked, soloDiagnostics: $('#lv-solo').checked};
}

function repoForm() {
  return {source: $('#rp-source').value.trim(), values: list($('#rp-values').value), set: list($('#rp-set').value),
    overlay: $('#rp-overlay').value.trim(), env: $('#rp-env').value.trim(), namespace: $('#rp-namespace').value.trim(), release: $('#rp-release').value.trim()};
}

function cliFor() {
  const a = ['flightbay'];
  if (ST.card === 'live') {
    const f = liveForm();
    a.push('ui', '--live');
    if (f.kubeconfig) a.push('--kubeconfig', f.kubeconfig);
    if (f.context) a.push('--context', f.context);
    if (f.namespaces) a.push('--namespaces', list(f.namespaces).join(','));
    if (f.excludeNamespaces) a.push('--exclude-namespaces', list(f.excludeNamespaces).join(','));
    if (f.clusterScoped) a.push('--cluster-scoped');
    if (f.soloDiagnostics) a.push('--solo-diagnostics');
  } else if (ST.card === 'file') {
    const p = ST.lastAction?.path || $('#fl-path').value.trim() || ST.files[0]?.path || 'BUNDLE';
    a.push(ST.lastAction?.review ? 'review' : 'ui', tilde(p));
    const id = $('#fl-identity').value.trim();
    if (id) a.push('-i', id);
  } else if (ST.card === 'repo') {
    const f = repoForm();
    let src = f.source || '.';
    const pre = [];
    if (/^(https?|ssh|git|file):\/\/|^[\w.-]+@[\w.-]+:/.test(src)) {
      const name = src.replace(/\/+$/, '').split(/[/:]/).pop().replace(/\.git$/, '') || 'repo';
      pre.push('git clone --depth 1 ' + sh(src) + ' ' + sh(name));
      src = name;
    }
    a.push('scan', src);
    for (const v of f.values) a.push('--values', v);
    for (const v of f.set) a.push('--set', v);
    if (f.overlay) a.push('--overlay', f.overlay);
    if (f.env) a.push('--env', f.env);
    if (f.namespace) a.push('--namespace', f.namespace);
    if (f.release) a.push('--release', f.release);
    return [...pre, a.map(sh).join(' '), 'flightbay ui .'].join(' && ');
  } else {
    a.push('ui');
  }
  return a.map(sh).join(' ');
}

function updateCLI() { $('#cli').textContent = cliFor(); }

// ---------- cards ----------
function openCard(name) {
  ST.card = ST.card === name ? '' : name;
  for (const b of document.querySelectorAll('.start-card')) {
    const on = b.dataset.card === ST.card;
    b.classList.toggle('active', on);
    b.setAttribute('aria-expanded', on ? 'true' : 'false');
  }
  for (const f of document.querySelectorAll('.start-form')) f.classList.toggle('hidden', f.dataset.for !== ST.card);
  $('#cards').classList.toggle('has-open', !!ST.card);
  $('#cards').dataset.open = ST.card;
  history.replaceState(null, '', ST.card ? '#' + ST.card : location.pathname);
  if (ST.card === 'live' && !ST.contexts) loadContexts();
  if (ST.card === 'file') loadRecent();
  if (ST.card === 'repo') setTimeout(() => $('#rp-source').focus(), 0);
  updateCLI();
}

// ---------- live ----------
async function loadContexts() {
  const sel = $('#lv-context');
  const kc = $('#lv-kubeconfig').value.trim();
  sel.innerHTML = '<option>Reading the kubeconfig…</option>';
  try {
    const j = await call('/api/start/contexts' + (kc ? '?kubeconfig=' + encodeURIComponent(kc) : ''));
    ST.contexts = j;
    $('#lv-files').textContent = j.files?.length ? 'Read ' + j.files.map(tilde).join(', ') : '';
    if (j.error) { sel.innerHTML = ''; status('#lv-status', j.error, 'red'); updateCLI(); return; }
    status('#lv-status', j.current ? '' : 'The kubeconfig has no current context; pick one.');
    sel.innerHTML = j.contexts.map(c => `<option value="${esc(c.name)}"${c.name === j.current ? ' selected' : ''}>${esc(c.name)}${c.name === j.current ? ' (current)' : ''}</option>`).join('');
    showServer();
    checkAccess();
  } catch (e) {
    sel.innerHTML = '';
    status('#lv-status', e.message, 'red');
  }
  updateCLI();
}

// checkAccess turns the live options on, or off with the reason, from what
// the context's credentials are allowed (a SelfSubjectAccessReview each).
async function checkAccess() {
  const f = liveForm();
  const seq = ++ST.accessSeq;
  if (!f.context) return;
  for (const [id, why] of [['#lv-cluster', '#lv-cluster-why'], ['#lv-solo', '#lv-solo-why']]) { $(id).checked = true; $(why).textContent = ''; }
  try {
    const a = await call('/api/start/live-access', f);
    if (seq !== ST.accessSeq) return;
    $('#lv-cluster').checked = a.clusterScoped;
    $('#lv-cluster-why').textContent = a.clusterScopedWhy ? 'Off: ' + a.clusterScopedWhy + '.' : '';
    $('#lv-solo').checked = a.soloDiagnostics;
    $('#lv-solo-why').textContent = a.soloDiagnosticsWhy ? 'Off: ' + a.soloDiagnosticsWhy + '.' : '';
  } catch (e) {
    // Unreachable or no access review allowed: leave both on; live mode
    // records anything it is refused.
  }
  optionsSummary();
  updateCLI();
}

function optionsSummary() {
  const on = [$('#lv-cluster').checked && 'cluster-scoped objects', $('#lv-solo').checked && 'Solo diagnostics'].filter(Boolean);
  $('#lv-options-sum').textContent = on.length === 2 ? 'Options: everything these credentials can read' :
    on.length ? 'Options: ' + on[0] + ' on, ' + (on[0].startsWith('cluster') ? 'Solo diagnostics' : 'cluster-scoped objects') + ' off' : 'Options: namespaced objects and logs only';
}

function showServer() {
  const c = ST.contexts?.contexts?.find(x => x.name === $('#lv-context').value);
  $('#lv-server').textContent = c ? [c.server, c.namespace && 'namespace ' + c.namespace].filter(Boolean).join(' · ') : '';
}

function renderPlan(j) {
  const p = j.plan || {};
  const res = p.resources || [];
  const diag = (p.categories || []).some(c => ['proxies', 'control-plane', 'metrics'].includes(c));
  $('#lv-plan').innerHTML = `
    <div class="start-plan">
      <dl class="metadata">
        <dt>Cluster</dt><dd class="mono">${esc(p.cluster)}</dd>
        <dt>Namespaces</dt><dd>${esc((p.namespaces || []).join(', '))}${j.request?.excludeNamespaces ? ' <span class="muted">(excluding ' + esc(j.request.excludeNamespaces) + ')</span>' : ''}</dd>
        <dt>Categories</dt><dd>${esc((p.categories || []).join(', '))}</dd>
        <dt>Resource types</dt><dd><details><summary>${res.length} types, listed once and then watched</summary><p class="mono start-types">${esc(res.join(', '))}</p></details></dd>
        ${p.logStreams?.length ? `<dt>Logs</dt><dd>Followed as <code>kubectl logs -f</code> would, streams ${esc(p.logStreams.join(', '))}, starting within ${esc(p.logSince)}</dd>` : ''}
        ${(p.unavailable || []).map(u => `<dt>Not read</dt><dd>${esc(u)}</dd>`).join('')}
        <dt>Requests</dt><dd>get, list, watch and pods/log only${diag ? ', plus pods/portforward for diagnostics' : ''}. Anything else is refused inside the process. Secret values are never kept.</dd>
      </dl>
      <div class="row between start-rbac-head"><h3>RBAC it needs</h3><button class="ghost" id="lv-copy-rbac">Copy</button></div>
      <pre class="code start-rbac">${esc(p.rbac)}</pre>
    </div>`;
  $('#lv-copy-rbac').onclick = () => { navigator.clipboard?.writeText(p.rbac || ''); toast('RBAC copied'); };
}

async function previewLive() {
  const f = liveForm();
  if (!f.context) { status('#lv-status', 'Pick a context first.', 'red'); return; }
  status('#lv-status', 'Reading discovery from ' + f.context + '…');
  $('#lv-preview').disabled = true;
  try {
    const j = await call('/api/start/live-plan', f);
    renderPlan(j);
    status('#lv-status', 'The preview read API discovery only: no objects, no logs.');
  } catch (e) {
    $('#lv-plan').innerHTML = `<div class="notice bad">${esc(e.message)}</div>`;
    status('#lv-status', '');
  } finally {
    $('#lv-preview').disabled = false;
  }
}

async function startLive() {
  const f = liveForm();
  if (!f.context) { status('#lv-status', 'Pick a context first.', 'red'); return; }
  try {
    await call('/api/start/live', f);
    opening('Starting live mode on ' + f.context, 'Listing every selected type, then watching. The desk opens as soon as it is connected.');
  } catch (e) { status('#lv-status', e.message, 'red'); }
}

// ---------- files ----------
function fileRow(f) {
  const badges = [];
  if (f.encrypted) badges.push('<span class="badge amber">encrypted</span>');
  if (f.reviewed) badges.push('<span class="badge green">reviewed</span>');
  if (f.source === 'repo') badges.push('<span class="badge blue">repo scan</span>');
  if (f.source === 'live') badges.push('<span class="badge blue">saved live</span>');
  if (f.unreadable && !f.encrypted) badges.push(`<span class="badge red" title="${esc(f.unreadable)}">unreadable</span>`);
  const where = f.dir === ST.info?.cwd ? 'here' : /\/Downloads$/.test(f.dir || '') ? 'Downloads' : /\/uploads$/.test(f.dir || '') ? 'uploaded' : tilde(f.dir || '');
  const meta = [f.case && 'case ' + f.case, (f.clusters || []).join(', '), when(f.captured) || when(f.modified), bytes(f.size), where].filter(Boolean);
  return `<div class="start-file">
    <div class="start-file-main"><div class="row wrap start-file-title"><span class="mono start-file-name" title="${esc(f.path)}">${esc(f.name)}</span>${badges.join('')}</div>
      <small class="muted">${meta.map(esc).join(' · ')}</small></div>
    <div class="row start-file-actions">
      <button class="primary" data-open="${esc(f.path)}">Open</button>
      <button data-review="${esc(f.path)}">Review before sending</button>
    </div></div>`;
}

async function loadRecent() {
  try {
    const j = await call('/api/start/recent');
    ST.files = j.files || [];
    const dirs = (j.dirs || []).filter(d => !/\/uploads$/.test(d)).map(tilde);
    $('#fl-list').innerHTML = ST.files.length ? ST.files.map(fileRow).join('')
      : `<p class="muted">No <code>flightbay-*.tar.zst</code> or <code>.tar.zst.age</code> files in ${dirs.map(d => '<code>' + esc(d) + '</code>').join(' or ')}.</p>`;
    for (const b of document.querySelectorAll('[data-open]')) b.onclick = () => openFile(b.dataset.open, false);
    for (const b of document.querySelectorAll('[data-review]')) b.onclick = () => openFile(b.dataset.review, true);
  } catch (e) {
    $('#fl-list').innerHTML = `<div class="notice bad">${esc(e.message)}</div>`;
  }
  updateCLI();
}

async function openFile(path, review) {
  if (!path) { status('#fl-status', 'Pick a bundle, drop one here, or paste its path.', 'red'); return; }
  ST.lastAction = {path, review};
  updateCLI();
  const identity = $('#fl-identity').value.trim();
  status('#fl-status', 'Checking ' + path.split('/').pop() + '…');
  try {
    await call('/api/start/open', {path, review, identity});
    const name = path.split('/').pop();
    opening(review ? 'Opening ' + name + ' for review' : 'Opening ' + name,
      review ? 'Review and send lists everything in it. Take out what you do not want to send, then export.' : 'You can start investigating while it loads.');
  } catch (e) {
    status('#fl-status', cap(e.message), 'red');
    if (/encrypted/.test(e.message)) $('#fl-identity').focus();
  }
}

function upload(file) {
  if (!/\.tar\.zst(\.age)?$|\.age$/.test(file.name)) { status('#fl-status', file.name + ' is not a Flightbay bundle (.tar.zst or .tar.zst.age).', 'red'); return; }
  const max = ST.info?.maxUpload || 0;
  if (max && file.size > max) { status('#fl-status', `${file.name} is larger than the ${bytes(max)} upload limit; paste its path instead.`, 'red'); return; }
  const bar = $('#fl-progress');
  bar.classList.remove('hidden'); bar.firstElementChild.style.width = '0';
  status('#fl-status', 'Copying ' + file.name + ' into the workspace…');
  const x = new XMLHttpRequest();
  x.open('POST', '/api/start/upload?name=' + encodeURIComponent(file.name));
  x.upload.onprogress = e => { if (e.lengthComputable) bar.firstElementChild.style.width = (100 * e.loaded / e.total).toFixed(1) + '%'; };
  x.onload = () => {
    bar.classList.add('hidden');
    let j = {};
    try { j = JSON.parse(x.responseText); } catch (e) { /* not JSON */ }
    if (x.status !== 200) { status('#fl-status', j.error || x.statusText, 'red'); return; }
    status('#fl-status', 'Copied ' + file.name + '. Open it, or review it before sending.', 'green');
    $('#fl-path').value = j.file.path;
    ST.lastAction = {path: j.file.path};
    loadRecent();
  };
  x.onerror = () => { bar.classList.add('hidden'); status('#fl-status', 'Upload failed.', 'red'); };
  x.send(file);
}

// ---------- repo ----------
async function scanRepo() {
  const f = repoForm();
  if (!f.source) { status('#rp-status', 'Give a directory or a Git URL.', 'red'); return; }
  const out = $('#rp-result');
  out.innerHTML = '<pre class="code start-log" id="rp-log"></pre>';
  const logEl = $('#rp-log');
  const addLog = l => { logEl.textContent += l + '\n'; logEl.scrollTop = logEl.scrollHeight; };
  $('#rp-scan').disabled = true;
  status('#rp-status', 'Scanning…');
  let last = null;
  try {
    const r = await fetch('/api/start/scan', {method: 'POST', credentials: 'same-origin', headers: {'Content-Type': 'application/json', Accept: 'application/x-ndjson'}, body: JSON.stringify(f)});
    if (!r.ok) { const j = await r.json().catch(() => ({})); throw new Error(j.error || r.statusText); }
    const rd = r.body.getReader();
    const dec = new TextDecoder();
    let buf = '';
    for (;;) {
      const {value, done} = await rd.read();
      if (done) break;
      buf += dec.decode(value, {stream: true});
      let i;
      while ((i = buf.indexOf('\n')) >= 0) {
        const line = buf.slice(0, i); buf = buf.slice(i + 1);
        if (!line.trim()) continue;
        const m = JSON.parse(line);
        if (m.log !== undefined) addLog(m.log); else last = m;
      }
    }
  } catch (e) {
    last = {error: e.message};
  }
  $('#rp-scan').disabled = false;
  const failed = last?.failed || [];
  const failList = failed.length ? `<div class="notice ${last?.error ? 'bad' : ''} start-failed"><div><strong>${failed.length} input${failed.length === 1 ? '' : 's'} could not be rendered</strong><ul>${failed.map(x => `<li><code>${esc(x.renderer)}</code> <span class="mono">${esc(x.source)}</span>: ${esc(x.reason)}</li>`).join('')}</ul></div></div>` : '';
  if (!last || last.error) {
    status('#rp-status', '');
    out.insertAdjacentHTML('afterbegin', `<div class="notice bad">${esc(cap(last?.error || 'The scan stopped without a result.'))}</div>${failList}`);
    return;
  }
  status('#rp-status', `${last.objects} objects of ${last.kinds} kinds from ${last.inputs} inputs.`, 'green');
  out.insertAdjacentHTML('afterbegin', failList);
  if (last.opening) opening('Opening the scan of ' + f.source, `${last.objects} objects, declared state.` + (failed.length ? ` ${failed.length} inputs failed to render; Coverage lists them.` : ''));
}

// ---------- hand-off ----------
function opening(title, detail) {
  $('#opening-title').textContent = title + '…';
  $('#opening-detail').textContent = detail || '';
  $('#opening').classList.remove('hidden');
  const started = Date.now();
  const poll = async () => {
    try {
      const r = await fetch('/api/workspace', {credentials: 'same-origin', cache: 'no-store'});
      if (r.ok) { location.replace('/'); return; }
    } catch (e) { /* the start page has gone and the desk is not up yet */ }
    if (Date.now() - started > 90000) {
      $('#opening-detail').textContent = 'The desk has not answered yet. Check the terminal where you ran flightbay ui for an error; this page keeps trying.';
    }
    setTimeout(poll, 400);
  };
  setTimeout(poll, 300);
}

// ---------- boot ----------
function applyTheme(t) {
  document.documentElement.dataset.theme = t;
  localStorage.setItem('flightbay-theme', t);
  $('#theme-toggle').textContent = t === 'light' ? '☾' : '☀';
  $('#theme-toggle').title = t === 'light' ? 'Switch to dark' : 'Switch to light';
}

async function boot() {
  applyTheme(document.documentElement.dataset.theme || 'dark');
  $('#theme-toggle').onclick = () => applyTheme(document.documentElement.dataset.theme === 'light' ? 'dark' : 'light');
  for (const b of document.querySelectorAll('.start-card')) b.onclick = () => openCard(b.dataset.card);
  $('#lv-reload').onclick = () => loadContexts();
  $('#lv-kubeconfig').onchange = () => loadContexts();
  $('#lv-context').onchange = () => { showServer(); $('#lv-plan').innerHTML = ''; updateCLI(); checkAccess(); };
  $('#lv-ns').onchange = () => checkAccess();
  for (const id of ['#lv-cluster', '#lv-solo']) $(id).addEventListener('change', optionsSummary);
  $('#lv-preview').onclick = previewLive;
  $('#lv-start').onclick = startLive;
  $('#fl-open').onclick = () => openFile($('#fl-path').value.trim(), false);
  $('#fl-review').onclick = () => openFile($('#fl-path').value.trim(), true);
  $('#fl-refresh').onclick = loadRecent;
  $('#fl-path').onkeydown = e => { if (e.key === 'Enter') openFile($('#fl-path').value.trim(), false); };
  $('#fl-choose').onclick = e => { e.stopPropagation(); $('#fl-input').click(); };
  $('#fl-input').onchange = e => { if (e.target.files[0]) upload(e.target.files[0]); e.target.value = ''; };
  const drop = $('#fl-drop');
  drop.onclick = () => $('#fl-input').click();
  drop.onkeydown = e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); $('#fl-input').click(); } };
  for (const ev of ['dragenter', 'dragover']) drop.addEventListener(ev, e => { e.preventDefault(); drop.classList.add('over'); });
  for (const ev of ['dragleave', 'drop']) drop.addEventListener(ev, e => { e.preventDefault(); drop.classList.remove('over'); });
  drop.addEventListener('drop', e => { const f = e.dataTransfer?.files?.[0]; if (f) upload(f); });
  // A file dropped anywhere else on the page would make the browser navigate to it.
  for (const ev of ['dragover', 'drop']) window.addEventListener(ev, e => { if (!e.target.closest?.('#fl-drop')) e.preventDefault(); });
  $('#rp-scan').onclick = scanRepo;
  $('#rp-source').onkeydown = e => { if (e.key === 'Enter') scanRepo(); };
  $('#fl-path').addEventListener('input', () => { ST.lastAction = null; });
  $('#main').addEventListener('input', updateCLI);
  $('#main').addEventListener('change', updateCLI);
  $('#cli-copy').onclick = () => { navigator.clipboard?.writeText($('#cli').textContent); toast('Command copied'); };
  try {
    ST.info = await call('/api/start');
    ST.home = ST.info.home || '';
  } catch (e) { /* not signed in: the server already said so */ }
  const want = location.hash.slice(1);
  if (['live', 'file', 'repo'].includes(want)) openCard(want);
  updateCLI();
}

boot();
