'use strict';
/* Ambient v2: one table behaviour for Needs attention, Services and Policies & routes. Click a column
   name to sort, type or pick in the row under the names to filter, and page through with a choice of
   page size (kept per table in localStorage). Only tbody and tfoot are redrawn while filtering, so the
   filter box keeps focus. */

S.am2.tables = S.am2.tables || {};
S.am2.tdefs = S.am2.tdefs || {};
const AM2_TSIZES = [5, 10, 25, 50, 100];

function am2TState(id, defaults = {}) {
  if (!S.am2.tables[id]) {
    let size = defaults.size || 25;
    try { const v = Number(localStorage.getItem('flightbay.am2.size.' + id)); if (AM2_TSIZES.includes(v)) size = v; } catch (_) { /* private mode */ }
    S.am2.tables[id] = {sort: defaults.sort || '', dir: defaults.dir || 1, filters: {}, page: 0, size};
  }
  return S.am2.tables[id];
}

const am2TText = v => String(v ?? '').toLowerCase();

// Pure: filter then sort. A text filter matches when every word appears; a select filter matches exactly.
function am2TApply(cols, rows, st) {
  let out = rows.filter(r => cols.every(c => {
    const f = st.filters[c.key];
    if (!f || !c.filter) return true;
    const v = c.filterValue ? c.filterValue(r) : c.sortValue ? c.sortValue(r) : '';
    if (c.filter === 'select') return (Array.isArray(v) ? v : [v]).map(String).includes(f);
    const hay = am2TText(Array.isArray(v) ? v.join(' ') : v);
    return am2TText(f).split(/\s+/).filter(Boolean).every(w => hay.includes(w));
  }));
  const col = cols.find(c => c.key === st.sort && c.sortValue);
  if (col) out = out.slice().sort((a, b) => { const x = col.sortValue(a), y = col.sortValue(b); return (typeof x === 'number' && typeof y === 'number' ? x - y : String(x ?? '').localeCompare(String(y ?? ''), undefined, {numeric: true})) * st.dir; });
  return out;
}

// Pure: the page window.
function am2TPage(total, st) {
  const pages = Math.max(1, Math.ceil(total / st.size));
  const page = Math.min(Math.max(0, st.page), pages - 1);
  return {pages, page, from: total ? page * st.size + 1 : 0, to: Math.min(total, (page + 1) * st.size)};
}

function am2TOptions(c, rows) {
  if (c.options) return c.options;
  const seen = new Set();
  for (const r of rows) { const v = c.filterValue ? c.filterValue(r) : c.sortValue?.(r); (Array.isArray(v) ? v : [v]).forEach(x => { if (x !== undefined && x !== null && x !== '') seen.add(String(x)); }); }
  return [...seen].sort((a, b) => a.localeCompare(b, undefined, {numeric: true}));
}

function am2TBody(id) {
  const d = S.am2.tdefs[id], st = am2TState(id);
  const rows = am2TApply(d.cols, d.rows, st), pg = am2TPage(rows.length, st);
  st.page = pg.page;
  const body = rows.slice(pg.from ? pg.from - 1 : 0, pg.to).map(r => d.row(r)).join('') || `<tr><td colspan="${d.cols.length}" class="am2-empty">${esc(d.opts.empty || 'Nothing matches.')}${Object.values(st.filters).some(Boolean) ? ' <a href="#" data-am2t-clear="' + esc(id) + '">Clear the column filters</a>' : ''}</td></tr>`;
  const nums = [];
  for (let i = 0; i < pg.pages; i++) if (i === 0 || i === pg.pages - 1 || Math.abs(i - pg.page) <= 1) nums.push(i); else if (nums[nums.length - 1] !== '…') nums.push('…');
  const pager = `<div class="am2-pager">${d.opts.footLeft || ''}<span class="am2-spacer"></span><label class="am2-sub am2-check">Rows per page <select class="am2-input am2-tsize" data-am2t-size="${esc(id)}">${AM2_TSIZES.map(n => `<option ${n === st.size ? 'selected' : ''}>${n}</option>`).join('')}</select></label><span class="am2-sub">${pg.from}–${pg.to} of ${rows.length}${rows.length !== d.rows.length ? ` (${d.rows.length} before filters)` : ''}</span><span class="am2-pages"><button type="button" class="am2-btn sm" data-am2t-page="${esc(id)}" data-p="${pg.page - 1}" ${pg.page ? '' : 'disabled'} aria-label="Previous page">‹</button>${nums.map(n => n === '…' ? '<span class="am2-sub">…</span>' : `<button type="button" class="am2-btn sm ${n === pg.page ? 'p' : ''}" data-am2t-page="${esc(id)}" data-p="${n}">${n + 1}</button>`).join('')}<button type="button" class="am2-btn sm" data-am2t-page="${esc(id)}" data-p="${pg.page + 1}" ${pg.page < pg.pages - 1 ? '' : 'disabled'} aria-label="Next page">›</button></span></div>`;
  return {body, foot: `<tr><td colspan="${d.cols.length}">${pager}</td></tr>`};
}

// cols: [{key, label, sortValue(r), filter: 'text'|'select'|undefined, filterValue(r), options, cls, width}]
// opts: {sort, dir, size, empty, footLeft, cls}
function am2Table(id, cols, rows, row, opts = {}) {
  S.am2.tdefs[id] = {cols, rows, row, opts};
  const st = am2TState(id, opts);
  const th = c => {
    const sortable = !!c.sortValue, on = st.sort === c.key;
    return `<th class="${c.cls || ''}" ${c.width ? `style="width:${c.width}"` : ''} ${on ? `aria-sort="${st.dir > 0 ? 'ascending' : 'descending'}"` : ''}>${sortable ? `<button type="button" class="am2-thsort ${on ? 'on' : ''}" data-am2t-sort="${esc(id)}" data-k="${esc(c.key)}">${esc(c.label)}<span class="am2-arrow">${on ? (st.dir > 0 ? '▲' : '▼') : '↕'}</span></button>` : esc(c.label || '')}</th>`;
  };
  const fl = c => {
    if (!c.filter) return '<th></th>';
    const v = st.filters[c.key] || '';
    if (c.filter === 'select') return `<th><select class="am2-input am2-tfilter" data-am2t-filter="${esc(id)}" data-k="${esc(c.key)}" aria-label="Filter ${esc(c.label)}"><option value="">All</option>${am2TOptions(c, rows).map(o => `<option ${o === v ? 'selected' : ''}>${esc(o)}</option>`).join('')}</select></th>`;
    return `<th><input class="am2-input am2-tfilter" data-am2t-filter="${esc(id)}" data-k="${esc(c.key)}" placeholder="Filter" value="${esc(v)}" aria-label="Filter ${esc(c.label)}"></th>`;
  };
  const {body, foot} = am2TBody(id);
  return `<table class="am2-ttable ${opts.cls || ''}" id="am2t-${esc(id)}"><thead><tr>${cols.map(th).join('')}</tr><tr class="am2-tfilters">${cols.map(fl).join('')}</tr></thead><tbody>${body}</tbody><tfoot>${foot}</tfoot></table>`;
}

function am2TRedraw(id) {
  const t = document.getElementById('am2t-' + id);
  if (!t || !S.am2.tdefs[id]) return;
  const {body, foot} = am2TBody(id);
  t.tBodies[0].innerHTML = body;
  t.tFoot.innerHTML = foot;
}
function am2TRedrawHead(id) {
  const t = document.getElementById('am2t-' + id), d = S.am2.tdefs[id];
  if (!t || !d) return;
  t.outerHTML = am2Table(id, d.cols, d.rows, d.row, d.opts);
}

if (!S.am2.tWired) {
  S.am2.tWired = true;
  let timer;
  document.addEventListener('input', e => {
    const f = e.target.closest?.('input[data-am2t-filter]');
    if (!f) return;
    const st = am2TState(f.dataset.am2tFilter);
    st.filters[f.dataset.k] = f.value; st.page = 0;
    clearTimeout(timer); timer = setTimeout(() => am2TRedraw(f.dataset.am2tFilter), 120);
  });
  document.addEventListener('change', e => {
    const f = e.target.closest?.('select[data-am2t-filter]');
    if (f) { const st = am2TState(f.dataset.am2tFilter); st.filters[f.dataset.k] = f.value; st.page = 0; am2TRedraw(f.dataset.am2tFilter); return; }
    const s = e.target.closest?.('[data-am2t-size]');
    if (s) { const id = s.dataset.am2tSize, st = am2TState(id); st.size = Number(s.value) || 25; st.page = 0; try { localStorage.setItem('flightbay.am2.size.' + id, st.size); } catch (_) { /* ignore */ } am2TRedraw(id); }
  });
  document.addEventListener('click', e => {
    const so = e.target.closest?.('[data-am2t-sort]');
    if (so) { e.preventDefault(); e.stopPropagation(); const id = so.dataset.am2tSort, st = am2TState(id), k = so.dataset.k; st.dir = st.sort === k ? -st.dir : 1; st.sort = k; st.page = 0; am2TRedrawHead(id); return; }
    const pg = e.target.closest?.('[data-am2t-page]');
    if (pg) { e.preventDefault(); e.stopPropagation(); const id = pg.dataset.am2tPage; am2TState(id).page = Number(pg.dataset.p) || 0; am2TRedraw(id); return; }
    const cl = e.target.closest?.('[data-am2t-clear]');
    if (cl) { e.preventDefault(); e.stopPropagation(); const id = cl.dataset.am2tClear; am2TState(id).filters = {}; am2TRedrawHead(id); }
  }, true);
}
