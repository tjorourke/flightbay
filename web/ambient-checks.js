'use strict';
/* Ambient v2: the Service Checks tab (mockup 01b). Same investigation data as the old Investigate tab,
   laid out problems first with grouped passes and evidence chips. */

S.am2.checks = S.am2.checks || {inv: null, key: '', open: {}, filter: 'problems', area: {}};

const AM2_AREAS = ['Data plane', 'Waypoint', 'Access', 'Routing', 'DNS'];
const AM2_AREA_TITLE = {'Data plane': 'Enrolled in ambient', Waypoint: 'Waypoint in use and healthy', Access: 'Policies bound and enforced', Routing: 'Routes accepted and backends ready', DNS: 'DNS resolves'};
const AM2_FIX_LABEL = {'require-waypoint': 'block bypass', endpoints: 'recover endpoints', 'route-parent': 'repair route parent', waypoint: 'set up a waypoint', 'enrol-namespace': 'enrol the namespace'};

const am2Problem = s => ['fail', 'warning', 'unknown'].includes(s.state);
const am2Remedy = s => (s.actions || [])[0] || (s.id === 'require-waypoint' ? {id: 'require-waypoint'} : null);
const am2AreaLabel = s => (s.id === 'require-waypoint' ? 'Access · ztunnel' : s.area || 'Routing');

function am2CheckPill(s) {
  return s.state === 'fail' ? am2Pill('bad', '✕') : s.state === 'warning' ? am2Pill('warn', '!') : s.state === 'unknown' ? am2Pill('mut', '?') : am2Pill('ok', '✓');
}

function am2EvidenceChips(s) {
  const chips = (s.evidence || []).map(e => {
    const r = e.ref || {}, fact = e.field ? `${e.field}${e.value ? ' = ' + e.value : ''}` : '';
    return `<span class="am2-chip am2-static" data-am2-open='${esc(JSON.stringify(r))}' title="${esc(fact)}"><span class="am2-kind">${esc(r.kind)}</span> ${esc(r.name)}</span>`;
  });
  return chips.length ? chips.join('') : '<span class="am2-chip am2-static am2-muted">No object was read for this check</span>';
}

function am2ProblemRow(s, ref, cfg, openState) {
  const remedy = am2Remedy(s), open = openState[s.id] ?? false;
  const fix = remedy ? `<a class="am2-btn sm p" href="${am2Hash({svc: am2SvcPath(ref.cluster, ref.namespace, ref.name), stab: 'checks', drawer: 'fix:' + s.id})}">Fix: ${esc(AM2_FIX_LABEL[remedy.id] || remedy.label || remedy.id)}…</a>` : `<button type="button" class="am2-btn sm p" data-am2-evidence="${esc(s.id)}">Runbook</button>`;
  const run = (s.runbook || []).length ? `<details class="am2-runbook"><summary class="am2-sub">Official troubleshooting commands · your own terminal</summary>${(s.runbook || []).map(c => `<div>${esc(c.evidence)}${c.mutates ? ' <b>Changes live logging level.</b>' : ''}${amCommandBlock(c.command)}</div>`).join('')}</details>` : '';
  const docs = (s.docs || []).length ? `<div class="am2-sub" style="margin-top:8px">${(s.docs || []).filter(d => /^https:/.test(d.url)).map(d => `<a href="${esc(d.url)}" target="_blank" rel="noopener">${esc(d.title)} ↗</a>`).join(' · ')}</div>` : '';
  return `<div class="am2-ck am2-prob ${open ? 'open' : ''}" data-am2-ck="${esc(s.id)}">${am2CheckPill(s)}<div><div class="am2-t">${esc(s.title)}</div><div class="am2-sub">${esc(s.detail)}</div></div><span class="am2-sub">${esc(am2AreaLabel(s))}</span><div class="am2-right am2-nowrap"><button type="button" class="am2-btn sm" data-am2-evidence="${esc(s.id)}">Evidence</button> ${fix}</div></div>${open ? `<div class="am2-evbox"><div class="am2-sub" style="margin-bottom:6px">What Flightbay looked at</div><div style="display:flex;gap:8px;flex-wrap:wrap">${am2EvidenceChips(s)}</div>${s.suggestedFix ? `<div class="am2-sub" style="margin-top:10px"><b>Suggested fix · ${esc(s.suggestedFix.title)}</b> ${esc(s.suggestedFix.fix)}</div>` : ''}${run}${docs}</div>` : ''}`;
}

function am2PassRow(s) {
  return `<div class="am2-ck am2-sub-row">${am2CheckPill(s)}<div><div class="am2-t">${esc(s.title)}</div><div class="am2-sub">${esc(s.detail)}</div></div><span class="am2-sub">${esc(am2AreaLabel(s))}</span><div></div></div>`;
}

function am2GroupedPasses(steps, inv, openAreas) {
  const passes = steps.filter(s => !am2Problem(s));
  const rows = AM2_AREAS.map(area => {
    const ss = passes.filter(s => (s.id === 'require-waypoint' ? 'Access' : s.area || 'Routing') === area);
    if (area === 'DNS') {
      const d = inv.dns;
      if (!d) return '';
      return `<div class="am2-ck"><span class="am2-pill mut">?</span><div><div class="am2-t">DNS not tested</div><div class="am2-sub">${esc(d.localName)}${(d.globalNames || []).length ? ' · ' + esc(d.globalNames.join(', ')) : ''}</div></div><span class="am2-sub">DNS</span><div class="am2-right"></div></div>`;
    }
    if (!ss.length) return '';
    const sum = ss.slice(0, 2).map(s => s.detail).join(' · ');
    const open = openAreas[area];
    return `<div class="am2-ck"><span class="am2-pill ok">✓</span><div><div class="am2-t">${esc(AM2_AREA_TITLE[area])}</div><div class="am2-sub">${esc(sum.length > 140 ? sum.slice(0, 137) + '…' : sum)}</div></div><span class="am2-sub">${esc(area)}</span><div class="am2-right"><button type="button" class="am2-btn sm g" data-am2-area="${esc(area)}">${ss.length} checks ▸</button></div></div>${open ? ss.map(am2PassRow).join('') : ''}`;
  }).join('');
  return `<div class="am2-passhead am2-sub">PASSED · ${passes.length} checks, grouped</div>${rows}`;
}

async function am2LoadChecks(route, ref) {
  const C = S.am2.checks, key = route.svc;
  if (C.key !== key || !C.inv) { C.inv = await api('api/ambient/investigate', {cluster: ref.cluster, kind: 'Service', namespace: ref.namespace, name: ref.name}); C.key = key; C.open = {}; C.area = {}; C.filter = 'problems'; }
  const steps = C.inv.steps || [];
  return {problems: steps.filter(am2Problem).length, all: steps.length};
}
async function am2ChecksTab(route, data, ref, ctx) {
  const C = S.am2.checks;
  await am2LoadChecks(route, ref);
  const inv = C.inv, steps = inv.steps || [], problems = steps.filter(am2Problem).sort((a, b) => ({fail: 0, warning: 1, unknown: 2}[a.state] - {fail: 0, warning: 1, unknown: 2}[b.state]));
  if (C.open && Object.keys(C.open).length === 0 && problems[0]) C.open[problems[0].id] = true;
  const body = C.filter === 'all'
    ? steps.map(s => am2Problem(s) ? am2ProblemRow(s, ref, ctx.cfg, C.open) : am2PassRow(s)).join('')
    : problems.map(s => am2ProblemRow(s, ref, ctx.cfg, C.open)).join('') + am2GroupedPasses(steps, inv, C.area);
  return `<div class="am2-card" style="border-top:0;border-radius:0 0 10px 10px;margin-top:-1px">${body || '<div class="am2-empty">No checks ran for this Service.</div>'}</div>` + `<div hidden id="am2-checks-meta" data-problems="${problems.length}" data-all="${steps.length}"></div>`;
}

function am2ChecksAfter(route) {
  if (!route.svc || (route.stab || '') !== 'checks') return;
  const C = S.am2.checks, root = $('#am2-root');
  if (!root) return;
  root.querySelectorAll('[data-am2-evidence]').forEach(b => b.onclick = e => { e.stopPropagation(); const id = b.dataset.am2Evidence; C.open[id] = !C.open[id]; render(); });
  root.querySelectorAll('[data-am2-area]').forEach(b => b.onclick = () => { C.area[b.dataset.am2Area] = !C.area[b.dataset.am2Area]; render(); });
  root.querySelectorAll('[data-am2-filter]').forEach(b => b.onclick = e => { e.preventDefault(); C.filter = b.dataset.am2Filter; render(); });
  root.querySelectorAll('[data-amcopycommand]').forEach(b => b.onclick = () => copyTerminalCommand(b.dataset.amcopycommand, b));
  root.querySelectorAll('[data-amruncommand]').forEach(b => b.onclick = () => termRun(b.dataset.amruncommand));
}
AM2_AFTER.push(am2ChecksAfter);
