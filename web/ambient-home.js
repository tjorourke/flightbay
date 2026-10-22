'use strict';
/* Ambient v2: the Map and Request path tabs keep today's views (PLAN §8); Changes restyles the change log
   with the status vocabulary and filters it by Service on the Service page (PLAN §9). */

async function am2EnsureOld() {
  const A = S.ambient;
  if (!A.inventory) A.inventory = await api('api/ambient');
  if (!A.contexts) A.contexts = await api('api/ambient/contexts').catch(() => []);
  return A.inventory;
}

// ---------- Map ----------
async function am2MapPage(route, data) {
  await am2EnsureOld();
  const A = S.ambient;
  A.tab = 'map';
  const sc = S.am2.scope;
  return `<div class="am2-oldwrap am-shell am-mode-map">${amClusterCanvas(null, {inScope: sv => am2InScopeSvc(sv.namespace, sv.name), selected: sc.svcs}) || '<div class="am2-note am2-muted">No clusters are connected.</div>'}</div>`;
}

// Retarget the Service card buttons to the new Service page (PLAN 8.1).
function am2MapAfter(route) {
  if (route.svc || route.obj || route.tab !== 'map') return;
  const A = S.ambient;
  if (typeof amWireClusterCanvas === 'function') amWireClusterCanvas();
  const base = o => am2SvcPath(o.cluster, o.namespace, o.name);
  document.querySelectorAll('.am-canvas-card[data-map-object]').forEach(card => {
    const ref = JSON.parse(card.dataset.mapObject), row = card.querySelector('.row.wrap');
    if (ref.kind !== 'Service' || !row) return;
    row.querySelectorAll('[data-amrelated],[data-amconfigure]').forEach(b => b.remove());
    const mk = (label, hash) => { const b = document.createElement('button'); b.textContent = label; b.onclick = () => { location.hash = hash; }; return b; };
    const yaml = row.querySelector('[data-amyaml]');
    for (const [label, hash] of [['Open', am2Hash({svc: base(ref), stab: 'configuration'})], ['Request path', am2Hash({tab: 'path', to: base(ref), ...am2ScopeParams()})]]) row.insertBefore(mk(label, hash), yaml || null);
  });
  document.querySelectorAll('[data-amevidence]').forEach(b => b.onclick = () => openObject(JSON.parse(b.dataset.amevidence), b.textContent.trim() === 'YAML' ? 'yaml' : 'summary'));
  document.querySelectorAll('[data-amyaml]').forEach(b => b.onclick = () => openObject(JSON.parse(b.dataset.amyaml), 'yaml'));
  if (A.mapFullscreen === undefined) A.mapFullscreen = false;
}
AM2_AFTER.push(am2MapAfter);

// ---------- Request path ----------
async function am2PathPage(route, data) {
  await am2EnsureOld();
  const sc = S.am2.scope;
  RP.scopeFilter = o => am2InScopeNs(o.namespace) && (!sc.hideSystem || !am2IsSystemNs(o.namespace));
  S.ambient.tab = 'path';
  if (!RP.options) RP.options = await api('api/ambient/request-path/options');
  if (route.to && RP.am2To !== route.to) {
    const ref = am2SvcRef(route.to), dest = (RP.options.destinations || []).find(o => o.cluster === ref.cluster && o.namespace === ref.namespace && o.name === ref.name && o.kind === 'Service');
    if (dest) { RP.destination = rpKey(dest); RP.host = ''; RP.hostCustom = false; RP.port = ''; RP.result = null; }
    RP.am2To = route.to;
  }
  return `<div class="am2-oldwrap am-shell am-mode-path">${await requestPathPage()}</div>`;
}
async function am2PathTab(route, data, ref, ctx) {
  S.ambient.tab = 'path';
  RP.scopeFilter = null;
  await am2EnsureOld();
  if (!RP.options) RP.options = await api('api/ambient/request-path/options');
  const dest = (RP.options.destinations || []).find(o => o.cluster === ref.cluster && o.namespace === ref.namespace && o.name === ref.name && o.kind === 'Service');
  if (dest && RP.am2Svc !== route.svc) { RP.destination = rpKey(dest); RP.host = ''; RP.hostCustom = false; RP.port = ''; RP.result = null; RP.am2Svc = route.svc; }
  return `<div class="am2-oldwrap am-shell am-mode-path" style="margin-top:16px">${await requestPathPage()}</div>`;
}
function am2PathAfter(route) {
  if (route.tab !== 'path' && route.stab !== 'path') return;
  if (!$('#rp-form')) return;
  rpAfter();
}
AM2_AFTER.push(am2PathAfter);

// ---------- Changes ----------
const AM2_CHANGE_STATE = {applied: ['tl', 'Applied'], removed: ['mut', 'Undone'], failed: ['bad', 'Failed'], applying: ['info', 'Applying']};
const am2ChangeWhat = t => { const q = t.plan?.request || {}; return (q.tasks || []).map(k => (typeof amTaskText !== 'undefined' && amTaskText[k]?.[0]) || k).join(', ') || (q.demo ? 'Bookinfo ' + q.demo.stage : 'Change'); };

async function am2ChangesRows(svcRef) {
  const A = S.ambient;
  A.tests = await api('api/ambient/tests').catch(() => A.tests || []);
  S.am2.tests = A.tests;
  return A.tests.map((t, i) => ({t, i})).filter(({t}) => !svcRef || ((t.plan?.request?.namespace === svcRef.namespace) && ((t.plan?.request?.service || t.plan?.request?.targetName) === svcRef.name)));
}
function am2ChangesTable(rows, svcRef) {
  const verified = t => (t.verified === true ? am2Pill('ok', 'Verified ✓') : t.verified === false ? am2Pill('bad', 'Failed') : t.applyState === 'applied' ? am2Pill('ok', 'Live') : '');
  const done = rows.filter(({t}) => ['removed', 'failed'].includes(t.applyState));
  return `<div class="am2-card" style="overflow:hidden"><div class="am2-pad" style="display:flex;align-items:center;gap:10px;border-bottom:1px solid var(--am2-line)"><div style="flex:1"><h2>Changes</h2><div class="am2-sub">${svcRef ? 'Changes Flightbay applied to this Service from this machine.' : 'Every change Flightbay applied from this machine: what it changed, where, its status, and undo. Undo restores only fields that are still as Flightbay left them.'}</div></div>${done.length ? `<button type="button" class="am2-btn sm" data-am2-forgetdone>Forget ${done.length} undone or failed</button>` : ''}</div><div class="am2-pad" style="display:flex;gap:10px;align-items:center;border-bottom:1px solid var(--am2-line)"><label style="display:flex;gap:6px;align-items:center"><input type="checkbox" id="am2-chg-all"> Select all</label><button type="button" class="am2-btn sm" data-am2-undosel disabled>Undo selected</button><button type="button" class="am2-btn sm" data-am2-forgetsel disabled>Forget selected</button></div><table><thead><tr><th style="width:30px"></th><th>State</th><th>When</th><th>What</th><th>Where</th><th>Objects</th><th>Tests</th><th></th></tr></thead><tbody>${rows.map(({t, i}) => {
    const [tone, label] = AM2_CHANGE_STATE[t.applyState] || ['info', t.applyState || 'Recorded'], q = t.plan?.request || {}, when = t.applyFinished && !String(t.applyFinished).startsWith('0001') ? t.applyFinished : t.validated;
    return `<tr><td><input type="checkbox" data-am2-chgsel="${i}" aria-label="Select change"></td><td>${am2Pill(tone, label)}${t.applyError ? `<div class="am2-sub" style="color:var(--am2-bad)" title="${esc(t.applyError)}">${esc(t.applyError.slice(0, 90))}${t.applyError.length > 90 ? '…' : ''}</div>` : ''}</td><td class="am2-sub">${esc(when ? new Date(when).toLocaleString() : '')}</td><td>${esc(am2ChangeWhat(t))}</td><td>${esc(t.context)}<div class="am2-sub">${esc((q.namespace || '') + '/' + (q.service || q.targetName || ''))}</div></td><td class="am2-mono am2-sub">${(t.created || []).map(o => esc('+ ' + o.kind + ' ' + o.name)).concat((t.modified || []).map(c => esc('~ ' + c.ref.kind + ' ' + c.ref.name))).join('<br>')}</td><td>${verified(t)}</td><td class="am2-right am2-act">${t.applyState === 'applied' ? `<button type="button" class="am2-btn sm" data-am2-cleanup="${i}">Undo</button> <button type="button" class="am2-btn sm" data-am2-status="${i}">Status</button>` : ''}${!['applied', 'applying'].includes(t.applyState) ? `<button type="button" class="am2-btn sm" data-am2-forget="${i}">Forget</button>` : ''}</td></tr>`;
  }).join('') || `<tr><td colspan="8" class="am2-empty">No changes applied yet.</td></tr>`}</tbody></table></div>${S.ambient.status ? `<details open class="am2-pad"><summary>Live status</summary><pre>${esc(JSON.stringify(S.ambient.status, null, 2))}</pre></details>` : ''}`;
}
async function am2ChangesPage(route, data) {
  return am2ChangesTable(await am2ChangesRows(null), null);
}
async function am2ChangesTab(route, data, ref, ctx) {
  return `<div style="margin-top:16px">${am2ChangesTable(await am2ChangesRows(ref), ref)}</div>`;
}
function am2ChangesAfter(route) {
  const root = $('#am2-root');
  if (!root || !(route.tab === 'changes' || route.stab === 'changes')) return;
  const A = S.ambient;
  if (root.dataset.chg) return;
  root.dataset.chg = '1';
  const sel = () => [...document.querySelectorAll('[data-am2-chgsel]:checked')].map(c => A.tests[Number(c.dataset.am2Chgsel)]);
  const sync = () => { const n = sel(), a = n.filter(t => t.applyState === 'applied'), u = $('[data-am2-undosel]'), f = $('[data-am2-forgetsel]'); if (!u) return; u.disabled = !a.length; f.disabled = !n.length; u.textContent = a.length ? `Undo ${a.length} applied` : 'Undo selected'; };
  const forget = async tokens => { try { await apiPost('api/ambient/tests', {tokens}); S.am2.data = null; } catch (e) { toast(e.message); } render(); };
  root.addEventListener('change', e => { if (e.target.id === 'am2-chg-all') { document.querySelectorAll('[data-am2-chgsel]').forEach(c => { c.checked = e.target.checked; }); } sync(); });
  root.addEventListener('click', async e => {
    const t = e.target, one = k => A.tests[Number(t.closest(`[data-am2-${k}]`).dataset['am2' + k[0].toUpperCase() + k.slice(1)])];
    if (t.closest('[data-am2-forget]')) forget([one('forget').token]);
    else if (t.closest('[data-am2-forgetdone]')) forget(A.tests.filter(x => ['removed', 'failed'].includes(x.applyState)).map(x => x.token));
    else if (t.closest('[data-am2-forgetsel]')) { const list = sel(), live = list.filter(x => ['applied', 'applying'].includes(x.applyState)); if (live.length && !confirm(live.length + ' of these are still applied. Forgetting them leaves the cluster changes in place and removes the undo record. Continue?')) return; forget(list.map(x => x.token)); }
    else if (t.closest('[data-am2-cleanup]') || t.closest('[data-am2-status]')) {
      const op = t.closest('[data-am2-cleanup]') ? 'cleanup' : 'status', rec = one(op);
      try { await amTestOperation(op, rec); S.am2.data = null; toast(op === 'cleanup' ? 'Change undone' : 'Status refreshed'); } catch (err) { toast(err.message); }
      render();
    } else if (t.closest('[data-am2-undosel]')) {
      const failed = [];
      for (const rec of sel().filter(x => x.applyState === 'applied')) { try { await amTestOperation('cleanup', rec); } catch (err) { failed.push(rec.context + ': ' + err.message); } }
      if (failed.length) toast(failed.join(' · ')); S.am2.data = null; render();
    }
  });
}
AM2_AFTER.push(am2ChangesAfter);

// ---------- Bookinfo demo (the header's Bookinfo demo button, and the Overview empty state) ----------
// am-mode-path carries the --rp-* colours the Bookinfo form is drawn with, as on the previous Ambient page.
async function am2DemoPage(route, data) {
  S.ambient.tab = 'demo';
  await am2EnsureOld();
  return `<div class="am2-oldwrap am-shell am-mode-demo am-mode-path">${await bookinfoPage()}</div>`;
}
function am2DemoAfter(route) { if (route.tab === 'demo' && typeof bookinfoAfter === 'function') bookinfoAfter(); }
AM2_AFTER.push(am2DemoAfter);
