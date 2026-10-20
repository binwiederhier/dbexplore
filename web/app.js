const $ = (s, el = document) => el.querySelector(s);
const esc = s => String(s).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const store = {get(k, d){try{const v = localStorage.getItem(k); return v ? JSON.parse(v) : d}catch{return d}}, set(k, v){try{localStorage.setItem(k, JSON.stringify(v))}catch{}}};
async function api(path, body){
  const r = await fetch(path, body ? {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify(body)} : {});
  if(r.status === 401){ location.href = '/login.html'; throw new Error('not signed in') }
  let j; try{ j = await r.json() }catch{ throw new Error(`HTTP ${r.status}`) }
  if(!r.ok) throw new Error(j.error || `HTTP ${r.status}`);
  return j;
}
function toast(t){const el = $('#toast'); el.textContent = t; el.style.display = 'block'; clearTimeout(toast.t); toast.t = setTimeout(() => el.style.display = 'none', 1800)}
const KIND = {r:'▦', p:'▦', v:'◫', m:'◫', f:'▤'};
const fmtN = n => n < 0 ? '?' : n >= 1e6 ? (n/1e6).toFixed(1)+'M' : n >= 1e4 ? Math.round(n/1e3)+'k' : String(n);

// ---------- tree ----------
const T = {sources:[], data:{}, open:new Set(store.get('treeOpen', [])), sel:null};
async function loadSources(){
  T.sources = await api('/api/sources');
  renderTree();
  for(const s of T.sources) if(T.open.has(s.id)) loadTree(s.id);
}
async function loadTree(src, refresh){
  T.data[src] = {loading:true};
  renderTree();
  try{ T.data[src] = await api(`/api/tree?src=${src}${refresh ? '&refresh=1' : ''}`) }
  catch(e){ T.data[src] = {error:e.message} }
  renderTree();
}
function toggle(key){T.open.has(key) ? T.open.delete(key) : T.open.add(key); store.set('treeOpen', [...T.open]); renderTree()}
function renderTree(){
  const f = $('#filter').value.trim().toLowerCase();
  const out = [];
  const row = (depth, key, opts) => out.push(`<div class="node${T.sel === key ? ' sel' : ''}" data-key="${esc(key)}" style="padding-left:${6 + depth*14}px">
    <span class="tw">${opts.tw ?? ''}</span><span class="ic">${opts.ic}</span><span class="nm">${esc(opts.name)}</span>${opts.badge || ''}${opts.ct != null ? `<span class="ct">${opts.ct}</span>` : ''}${opts.act || ''}</div>`);
  for(const s of T.sources){
    const open = T.open.has(s.id) || !!f, d = T.data[s.id];
    row(0, s.id, {tw: open ? '▾' : '▸', ic:'🐘', name:s.label, badge: s.read_write ? '<span class="rw" title="Read-write: the console and row edits change data">RW</span>' : '', act:`<button class="act" data-console="${s.id}" title="New console">SQL</button>`});
    if(!open) continue;
    if(!d || d.loading){ out.push(`<div class="node" style="padding-left:40px;color:var(--dim)"><div class="spin"></div>loading…</div>`); continue }
    if(d.error){ out.push(`<div class="node" style="padding-left:40px;color:var(--err)" title="${esc(d.error)}">${esc(d.error.slice(0,80))}</div>`); continue }
    for(const sc of d.schemas){
      const tabs = f ? sc.tables.filter(t => t.name.toLowerCase().includes(f)) : sc.tables;
      if(f && !tabs.length) continue;
      const sk = `${s.id}/${sc.name}`, sopen = T.open.has(sk) || (!!f && !sc.name.startsWith('pg_') && sc.name !== 'information_schema');
      row(1, sk, {tw: sopen ? '▾' : '▸', ic:'🗂', name:sc.name, ct:sc.tables.length});
      if(!sopen) continue;
      for(const t of tabs) row(2, `${sk}/${t.name}`, {ic:KIND[t.kind] || '▦', name:t.name, ct: t.kind === 'v' ? 'view' : fmtN(t.est)});
    }
  }
  $('#tree').innerHTML = out.join('');
}
$('#tree').addEventListener('click', e => {
  const c = e.target.closest('[data-console]');
  if(c){ e.stopPropagation(); openConsole(c.dataset.console); return }
  const n = e.target.closest('.node[data-key]'); if(!n) return;
  const key = n.dataset.key, parts = key.split('/');
  if(parts.length === 3){ T.sel = key; renderTree(); openTable(...parts); return }
  toggle(key);
  if(parts.length === 1 && T.open.has(key) && !T.data[key]) loadTree(key);
});
$('#filter').addEventListener('input', renderTree);
$('#refreshTree').onclick = () => { peek.cache.clear(); for(const s of T.sources) if(T.open.has(s.id)) loadTree(s.id, true) };
$('#collapseAll').onclick = () => { T.open.clear(); store.set('treeOpen', []); renderTree() };
// sidebar resize
(() => { let x0, w0; const side = $('#side'); const w = store.get('sideW'); if(w) side.style.width = w + 'px';
  $('#split').onmousedown = e => { x0 = e.clientX; w0 = side.offsetWidth; document.onmousemove = m => side.style.width = (w0 + m.clientX - x0) + 'px'; document.onmouseup = () => { document.onmousemove = document.onmouseup = null; store.set('sideW', side.offsetWidth) }; e.preventDefault() };
})();

// ---------- tabs ----------
const tabs = []; let active = null, seq = 0;
function saveTabs(){ store.set('tabs', tabs.map(t => ({kind:t.kind, src:t.src, schema:t.schema, table:t.table, where:t.where, order:t.order, sql:t.sql, pageSize:t.pageSize, view:t.view}))); store.set('activeIdx', tabs.indexOf(active)) }
function srcLabel(id){ return (T.sources.find(s => s.id === id) || {label:id}).label }
const isRW = id => !!T.sources.find(s => s.id === id)?.read_write;
function paintMode(src){ const ro = $('#top .ro'), rw = isRW(src); ro.textContent = rw ? 'READ-WRITE' : 'READ-ONLY'; ro.classList.toggle('rw', rw) }
function openTable(src, schema, table, where){
  let t = where == null && tabs.find(t => t.kind === 'table' && t.src === src && t.schema === schema && t.table === table && !t.where);
  if(!t){
    t = {id:++seq, kind:'table', src, schema, table, where:where || '', order:'', page:0, pageSize:100};
    const i = active ? tabs.indexOf(active) : -1;
    i >= 0 ? tabs.splice(i + 1, 0, t) : tabs.push(t);
  }
  activate(t);
  if(!t.res && !t.loading) runTab(t);
}
function openConsole(src, sql = ''){
  src = src || (T.sources[0] || {}).id;
  const n = tabs.filter(t => t.kind === 'console').length + 1;
  const t = {id:++seq, kind:'console', src, sql, page:0, pageSize:100, n};
  tabs.push(t); activate(t);
  setTimeout(() => $('.ask input')?.focus(), 0);
}
function activate(t){ active = t; renderTabs(); renderPane(); saveTabs(); if(t.kind === 'table' && !t.res && !t.loading && !t.error) runTab(t) }
function closeTab(t){ const i = tabs.indexOf(t); tabs.splice(i, 1); if(active === t) active = tabs[Math.min(i, tabs.length - 1)] || null; renderTabs(); renderPane(); saveTabs() }
function tabTitle(t){ return t.kind === 'table' ? `${t.table}${t.where ? ' ⧩' : ''}` : `console ${t.n}` }
function renderTabs(){
  $('#tabs').innerHTML = tabs.map(t => `<div class="tab${t === active ? ' on' : ''}" data-id="${t.id}" title="${esc(t.kind === 'table' ? `${srcLabel(t.src)} · ${t.schema}.${t.table}${t.where ? ` where ${t.where}` : ''}` : srcLabel(t.src))}">
    <span>${t.kind === 'table' ? '▦' : '⌨'}</span><span>${esc(tabTitle(t))}</span><span class="x" data-close="${t.id}">✕</span></div>`).join('');
}
$('#tabs').addEventListener('click', e => {
  const x = e.target.closest('[data-close]'); const tab = e.target.closest('.tab'); if(!tab) return;
  const t = tabs.find(t => t.id == tab.dataset.id);
  x ? closeTab(t) : activate(t);
});
$('#tabs').addEventListener('auxclick', e => { const tab = e.target.closest('.tab'); if(tab && e.button === 1) closeTab(tabs.find(t => t.id == tab.dataset.id)) });
$('#newConsole').onclick = () => openConsole(active?.src);

// ---------- pane ----------
function renderPane(){
  const p = $('#pane'), t = active;
  paintMode(t?.src);
  if(!t){ p.innerHTML = `<div class="empty"><div style="font-size:34px">🗄</div><div>Pick a table on the left, or open a query console.</div></div>`; $('#crumbs').textContent = 'Ready'; return }
  $('#crumbs').innerHTML = [srcLabel(t.src), t.schema, t.table].filter(Boolean).map(esc).join(' <span class="cr">›</span> ') + (t.kind === 'console' ? ` <span class="cr">›</span> console ${t.n}` : '');
  if(t.kind === 'table'){
    p.innerHTML = `<div class="tb">
        <button data-a="run" title="Reload (Ctrl+Enter)">⟳</button><span class="sep"></span>
        ${pagerHtml(t)}<span class="sep"></span><span class="seg" title="Result view"><button data-a="vgrid" class="${t.view !== 'cards' ? 'on' : ''}">▦ Table</button><button data-a="vcards" class="${t.view === 'cards' ? 'on' : ''}">▤ Cards</button></span><span class="sep"></span>
        <button data-a="count" title="Count rows matching the filter">Σ count</button>
        <button data-a="ddl" title="Show DDL">DDL</button>
        <button data-a="toconsole" title="Open the current query in a console">⌨ to console</button>
        ${isRW(t.src) ? '<button data-a="insert" title="Insert a row">+ Row</button>' : ''}
        <span class="grow"></span>
        <button data-a="tsv" title="Copy selected rows (or the whole page) as TSV — Ctrl+C copies the selection">⧉ TSV</button>
      </div>
      <div class="filt"><label>WHERE<input data-f="where" spellcheck="false" placeholder="e.g. name like 'foo%'" value="${esc(t.where)}"></label>
        <label>ORDER BY<input data-f="order" spellcheck="false" placeholder="e.g. created_at desc" value="${esc(t.order)}"></label></div>
      <div class="gridwrap"></div><div class="foot"></div>`;
  } else {
    p.innerHTML = `<div class="console">
        <div class="ask">${T.sources.some(s => s.ai) ? `<span class="lbl">✦ Ask AI</span><input data-f="ask" spellcheck="false" placeholder="Describe the query, e.g. 'customers with their latest order date' (Enter to generate)"><button data-a="ask">Generate</button>` : '<span class="grow"></span>'}
          <select data-f="src">${T.sources.map(s => `<option value="${s.id}"${s.id === t.src ? ' selected' : ''}>${esc(s.label)}</option>`).join('')}</select></div>
        <textarea data-f="sql" spellcheck="false" placeholder="select * from my_table where …   (Ctrl+Enter to run)">${esc(t.sql)}</textarea>
        <div class="tb"><button class="primary" data-a="runq" title="Ctrl+Enter">▶ Run</button><span class="sep"></span>${pagerHtml(t)}<span class="sep"></span><span class="seg" title="Result view"><button data-a="vgrid" class="${t.view !== 'cards' ? 'on' : ''}">▦ Table</button><button data-a="vcards" class="${t.view === 'cards' ? 'on' : ''}">▤ Cards</button></span><span class="grow"></span><button data-a="tsv">⧉ TSV</button></div>
      </div><div class="gridwrap"></div><div class="foot"></div>`;
  }
  renderGrid(t);
}
function pagerHtml(t){
  return `<span class="pager"><button data-a="first" title="First page">⏮</button><button data-a="prev" title="Previous page">◀</button>
    <span data-pg style="padding:0 6px">page ${t.page + 1}</span><button data-a="next" title="Next page">▶</button>
    <select data-f="pageSize" title="Rows per page">${[25,50,100,200,500].map(n => `<option${n === t.pageSize ? ' selected' : ''}>${n}</option>`).join('')}</select></span>`;
}
function fkMap(t){ const m = {}; for(const fk of t.fks || []) for(const c of fk.cols) m[c] ||= fk; return m }
// Postgres reserved keywords: as identifiers they must be quoted (a bare "user" is current_user)
const RESERVED = new Set(('all analyse analyze and any array as asc asymmetric authorization binary both case cast check collate collation column concurrently constraint create cross ' +
  'current_catalog current_date current_role current_schema current_time current_timestamp current_user default deferrable desc distinct do else end except false fetch for foreign freeze ' +
  'from full grant group having ilike in initially inner intersect into is isnull join lateral leading left like limit localtime localtimestamp natural not notnull null offset on only or ' +
  'order outer overlaps placing primary references returning right select session_user similar some symmetric system_user table tablesample then to trailing true union unique user using ' +
  'variadic verbose when where window with').split(' '));
const qid = c => /^[a-z_][a-z0-9_$]*$/.test(c) && !RESERVED.has(c) ? c : `"${c.replace(/"/g, '""')}"`;
const lit = v => typeof v === 'number' || typeof v === 'boolean' ? String(v) : `'${(typeof v === 'object' ? JSON.stringify(v) : String(v)).replace(/'/g, "''")}'`;
// fkRef returns the FK of a cell and the WHERE clause selecting the referenced row, or null if
// the column is no FK or (part of) the key is NULL.
function fkRef(t, ri, ci){
  const fk = fkMap(t)[t.res.columns[ci]]; if(!fk) return null;
  const row = t.res.rows[ri], conds = [];
  for(let i = 0; i < fk.cols.length; i++){
    const v = row[t.res.columns.indexOf(fk.cols[i])];
    if(v === null || v === undefined) return null;
    conds.push(`${qid(fk.ref_cols[i])} = ${lit(v)}`);
  }
  return {fk, where:conds.join(' and ')};
}
function followFk(t, ri, ci){
  if(!fkMap(t)[t.res.columns[ci]]) return;
  const ref = fkRef(t, ri, ci); if(!ref) return toast('Foreign key is NULL');
  hidePeek();
  openTable(t.src, ref.fk.ref_schema, ref.fk.ref_table, ref.where);
}
// sortDir is 'asc' or 'desc' if the ORDER BY bar sorts by exactly this column, else ''.
function sortDir(t, c){
  const m = (t.order || '').trim().match(/^("(?:[^"]|"")+"|[\w$]+)(?:\s+(asc|desc))?$/i);
  if(!m) return '';
  const name = m[1].startsWith('"') ? m[1].slice(1, -1).replace(/""/g, '"') : m[1].toLowerCase();
  return name === c ? (m[2] || 'asc').toLowerCase() : '';
}
function cellClass(v){ return v === null ? 'n' : typeof v === 'number' ? 'num' : typeof v === 'boolean' ? 'b' : typeof v === 'object' ? 'j' : '' }
function cellText(v){ return v === null ? '<null>' : typeof v === 'object' ? JSON.stringify(v) : String(v) }
function renderGrid(t){
  const w = $('.gridwrap'), foot = $('.foot'); if(!w) return;
  const pg = $('[data-pg]'); if(pg) pg.textContent = `page ${t.page + 1}`;
  $('[data-a="prev"]').disabled = $('[data-a="first"]').disabled = t.page === 0 || t.loading;
  $('[data-a="next"]').disabled = !t.res?.has_more || t.loading;
  let html = '';
  if(t.error) html = `<div class="msg err">${esc(t.error)}</div>`;
  else if(t.res){
    const r = t.res, cols = t.cols ? Object.fromEntries(t.cols.map(c => [c.name, c])) : {};
    const fkOf = t.kind === 'table' ? fkMap(t) : {};
    const numCol = r.columns.map(c => /^(smallint|integer|bigint|numeric|real|double precision)\b/.test(cols[c]?.type || ''));
    const cls = (v, ci) => typeof v === 'string' && numCol[ci] ? 'num' : cellClass(v);
    if(!r.columns.length) html = r.command ? `<div class="msg ok">${esc(r.command)}</div>` : `<div class="msg">Statement returned no columns.</div>`;
    else {
      const cell = (v, ci) => { const fk = fkOf[r.columns[ci]], txt = esc(cellText(v).slice(0, 300)); return fk && v !== null ? `<span class="fk">${txt}</span>` : txt };
      if(t.view === 'cards'){
        const pk = r.columns.map((c, i) => cols[c]?.pk ? i : -1).filter(i => i >= 0), idCols = pk.length ? pk : [0];
        html = `<div class="cards">${r.rows.map((row, ri) => `<div class="card${t.rsel?.has(ri) ? ' rsel' : ''}" data-ri="${ri}">
          <div class="ch"><span class="no">#${t.page * t.pageSize + ri + 1}</span><span class="id">${esc(idCols.map(i => cellText(row[i])).join(' · '))}</span></div>
          <table>${row.map((v, ci) => `<tr><td class="k" title="${esc(r.columns[ci])}">${esc(r.columns[ci])}</td><td class="${cls(v, ci)}" data-r="${ri}" data-c="${ci}"${fkOf[r.columns[ci]] && v !== null ? '' : ` title="${esc(cellText(v).slice(0, 1000))}"`}>${cell(v, ci)}</td></tr>`).join('')}</table></div>`).join('')}</div>`;
      } else
      html = `<table class="grid"><thead><tr><th class="rn"></th>${r.columns.map((c, i) => {
        const m = cols[c], so = {asc:'↑', desc:'↓'}[sortDir(t, c)] || '';
        return `<th data-col="${i}" title="${esc(m ? `${m.type}${m.notnull ? ' not null' : ''}${m.pk ? ' · primary key' : ''}` : c)}">${m?.pk ? '🔑 ' : ''}${esc(c)}${m ? `<span class="ty">${esc(m.type.replace(/ without time zone|character varying/g, s => s.startsWith(' ') ? '' : 'varchar'))}</span>` : ''}${fkOf[c] ? `<span class="fkm" title="references ${esc(`${fkOf[c].ref_schema}.${fkOf[c].ref_table}(${fkOf[c].ref_cols.join(', ')})`)}">↗</span>` : ''}${so ? `<span class="so">${so}</span>` : ''}</th>`
      }).join('')}</tr></thead><tbody>${r.rows.map((row, ri) => `<tr${t.rsel?.has(ri) ? ' class="rsel"' : ''} data-ri="${ri}"><td class="rn">${t.page * t.pageSize + ri + 1}</td>${row.map((v, ci) => `<td class="${cls(v, ci)}" data-r="${ri}" data-c="${ci}">${cell(v, ci)}</td>`).join('')}</tr>`).join('')}</tbody></table>`;
      if(!r.rows.length) html += `<div class="msg" style="color:var(--dim)">No rows.</div>`;
    }
  } else if(t.kind === 'console' && !t.loading) html = `<div class="msg" style="color:var(--dim)">Write a query, or ask the AI to write one. Ctrl+Enter runs it.</div>`;
  if(t.loading) html += `<div class="loading"><div class="spin"></div>${esc(t.loading)}</div>`;
  w.innerHTML = html;
  if(t.res && !t.loading) w.scrollTop = 0;
  const r = t.res;
  foot.innerHTML = r ? `<span>${r.rows.length ? `rows ${t.page * t.pageSize + 1}–${t.page * t.pageSize + r.rows.length}` : '0 rows'}${r.has_more ? '+' : ''}</span>
      ${t.count != null ? `<span>of <b style="color:var(--text)">${t.count.toLocaleString()}</b></span>` : ''}
      ${r.command ? `<span class="cmd" title="Committed">${esc(r.command)}</span>` : ''}<span>${r.ms} ms</span>${r.truncated ? `<span style="color:var(--bool)" title="Only the first ${r.rows.length} rows are shown (max_rows)">truncated</span>` : ''}
      <span class="sql" title="${esc(r.sql)}">${esc(r.sql.replace(/\s+/g, ' '))}</span>` : '';
}
// riskyWrite returns a confirmation question for statements that are easy to regret, else null.
function riskyWrite(sql){
  const s = sql.replace(/^(\s|--[^\n]*(\n|$)|\/\*[\s\S]*?\*\/)+/, ''), verb = (s.match(/^\w+/) || [''])[0].toUpperCase();
  if(verb === 'DROP' || verb === 'TRUNCATE') return `Run this ${verb} statement? It cannot be undone.`;
  if((verb === 'UPDATE' || verb === 'DELETE') && !/\bwhere\b/i.test(s)) return `This ${verb} has no WHERE clause and changes every row. Run it?`;
  return null;
}
// afterWrite drops what a committed write may have made stale: hover cards, and for DDL the
// columns and FKs of open tabs and the tree.
function afterWrite(src, command){
  peek.cache.clear();
  if(!/^(CREATE|ALTER|DROP|COMMENT|GRANT|REVOKE)\b/.test(command)) return;
  for(const x of tabs) if(x.src === src){ x.cols = x.fks = null }
  if(T.open.has(src)) loadTree(src, true);
}
async function runTab(t){
  if(t.kind === 'console' && t.page === 0 && isRW(t.src)){ const q = riskyWrite(t.sql); if(q && !confirm(q)) return }
  t.loading = 'running…'; t.error = null; if(t === active) renderGrid(t);
  try{
    if(t.kind === 'table'){
      const q = `src=${t.src}&schema=${encodeURIComponent(t.schema)}&table=${encodeURIComponent(t.table)}`;
      if(!t.cols) t.cols = await api(`/api/columns?${q}`).catch(() => null);
      if(!t.fks) t.fks = await api(`/api/fks?${q}`).catch(() => null);
      t.res = await api('/api/table', {src:t.src, schema:t.schema, table:t.table, where:t.where, order:t.order, page:t.page, page_size:t.pageSize});
    } else {
      t.res = await api('/api/query', {src:t.src, sql:t.sql, page:t.page, page_size:t.pageSize});
    }
  } catch(e){ t.error = e.message; t.res = null }
  if(t.res?.command) afterWrite(t.src, t.res.command);
  t.loading = null; t.rsel = new Set(); t.anchor = null;
  if(t === active) renderGrid(t);
  saveTabs();
}
function reset(t){ t.page = 0; t.count = null; runTab(t) }
$('#pane').addEventListener('click', async e => {
  const t = active; if(!t) return;
  const th = e.target.closest('th[data-col]');
  if(th && t.res){
    const c = t.res.columns[th.dataset.col];
    if(t.kind !== 'table'){ return }
    const q = qid(c), dir = sortDir(t, c);
    t.order = dir === 'asc' ? `${q} desc` : dir === 'desc' ? '' : q;
    $('[data-f="order"]').value = t.order; saveTabs(); reset(t); return;
  }
  const fkEl = e.target.closest('.fk');
  if(fkEl){ const c = fkEl.closest('td'); followFk(t, +c.dataset.r, +c.dataset.c); return }
  const tr = e.target.closest('[data-ri]');
  if(tr){
    const ri = +tr.dataset.ri; t.rsel ||= new Set();
    if(e.shiftKey && t.anchor != null){ t.rsel = new Set(); for(let i = Math.min(t.anchor, ri); i <= Math.max(t.anchor, ri); i++) t.rsel.add(i) }
    else if(e.ctrlKey || e.metaKey){ t.rsel.has(ri) ? t.rsel.delete(ri) : t.rsel.add(ri); t.anchor = ri }
    else { t.rsel = new Set([ri]); t.anchor = ri }
    paintSel(t);
    $('.grid td.sel')?.classList.remove('sel'); e.target.closest('td[data-r]')?.classList.add('sel');
    return;
  }
  const b = e.target.closest('[data-a]'); if(!b) return;
  const a = b.dataset.a;
  if(a === 'run' || a === 'runq'){ if(t.kind === 'console') t.sql = $('[data-f="sql"]').value; reset(t) }
  else if(a === 'first'){ t.page = 0; runTab(t) }
  else if(a === 'prev'){ t.page = Math.max(0, t.page - 1); runTab(t) }
  else if(a === 'next'){ t.page++; runTab(t) }
  else if(a === 'count'){
    b.disabled = true;
    try{ t.count = (await api('/api/count', {src:t.src, schema:t.schema, table:t.table, where:t.where})).count; if(t === active) renderGrid(t) }
    catch(err){ toast(err.message) } finally { b.disabled = false }
  }
  else if(a === 'ddl'){
    try{ const d = await api(`/api/ddl?src=${t.src}&schema=${encodeURIComponent(t.schema)}&table=${encodeURIComponent(t.table)}`); showModal(`DDL · ${t.schema}.${t.table}`, d.ddl) }
    catch(err){ toast(err.message) }
  }
  else if(a === 'toconsole'){
    let sql = `select *\nfrom ${t.schema === 'public' ? '' : qid(t.schema) + '.'}${qid(t.table)}`;
    if(t.where.trim()) sql += `\nwhere ${t.where.trim()}`;
    if(t.order.trim()) sql += `\norder by ${t.order.trim()}`;
    openConsole(t.src, sql);
  }
  else if(a === 'insert'){ if(t.cols) editRow(t, null); else toast('Columns are not loaded') }
  else if(a === 'vgrid' || a === 'vcards'){ t.view = a === 'vcards' ? 'cards' : 'grid'; saveTabs(); renderPane() }
  else if(a === 'tsv') copyRows(t, t.rsel?.size ? null : t.res?.rows.map((_, i) => i));
  else if(a === 'ask') ask(t);
});
$('#pane').addEventListener('dblclick', e => {
  const tr = e.target.closest('[data-ri]'); if(!tr || !active?.res || e.target.closest('.fk')) return;
  showRow(active, +tr.dataset.ri);
});
// drag across rows to select a range
$('#pane').addEventListener('mousedown', e => {
  const tr = e.target.closest('[data-ri]'), t = active;
  if(!tr || !t || e.button !== 0 || e.shiftKey || e.ctrlKey || e.metaKey) return;
  const start = +tr.dataset.ri;
  const move = m => { const o = m.target.closest?.('[data-ri]'); if(!o) return; const ri = +o.dataset.ri;
    t.rsel = new Set(); for(let i = Math.min(start, ri); i <= Math.max(start, ri); i++) t.rsel.add(i); t.anchor = start; paintSel(t) };
  document.addEventListener('mouseover', move);
  document.addEventListener('mouseup', () => document.removeEventListener('mouseover', move), {once:true});
});
function paintSel(t){ document.querySelectorAll('#pane [data-ri]').forEach(tr => tr.classList.toggle('rsel', !!t.rsel?.has(+tr.dataset.ri))) }
const tsvVal = v => v === null ? '' : cellText(v).replace(/[\t\r\n]+/g, ' ');
async function copyRows(t, idx){
  if(!t?.res) return;
  idx = idx || [...(t.rsel || [])].sort((a, b) => a - b);
  if(!idx.length) return toast('Select rows first');
  const tsv = [t.res.columns, ...idx.map(i => t.res.rows[i].map(tsvVal))].map(r => r.join('\t')).join('\n');
  try{ await navigator.clipboard.writeText(tsv); toast(`Copied ${idx.length} row${idx.length > 1 ? 's' : ''} as TSV`) }catch{ showModal('TSV', tsv) }
}
// ---------- FK peek: hovering an FK link shows the referenced row in a card ----------
const peek = {el:null, timer:null, seq:0, cache:new Map()};
function fkCell(el){
  const link = el.closest?.('.fk'); if(!link) return null;
  if(link.dataset.ci != null && rowView) return {link, t:rowView.t, ri:rowView.ri, ci:+link.dataset.ci};
  const td = link.closest('td[data-r]'); if(!td || !active?.res) return null;
  return {link, t:active, ri:+td.dataset.r, ci:+td.dataset.c};
}
function hidePeek(){ clearTimeout(peek.timer); peek.seq++; peek.el = null; $('#peek').style.display = 'none' }
function placePeek(link){
  const p = $('#peek'), r = link.getBoundingClientRect(), w = p.offsetWidth, h = p.offsetHeight;
  const top = r.bottom + 6 + h < innerHeight ? r.bottom + 6 : Math.max(6, r.top - 6 - h);
  p.style.left = Math.max(6, Math.min(r.left, innerWidth - w - 6)) + 'px';
  p.style.top = top + 'px';
}
function renderPeek(link, title, body){
  const p = $('#peek');
  p.innerHTML = `<div class="ch"><span class="id">${esc(title)}</span></div>${body}`;
  p.style.display = 'block';
  placePeek(link);
}
async function showPeek(c){
  const ref = fkRef(c.t, c.ri, c.ci); if(!ref) return;
  const {fk, where} = ref, title = `${fk.ref_table} · ${where}`, key = JSON.stringify([c.t.src, fk.ref_schema, fk.ref_table, where]), seq = ++peek.seq;
  let res = peek.cache.get(key);
  if(!res){
    renderPeek(c.link, title, '<div class="msg" style="color:var(--dim)">loading…</div>');
    try{ res = await api('/api/table', {src:c.t.src, schema:fk.ref_schema, table:fk.ref_table, where, page_size:1}); peek.cache.set(key, res) }
    catch(e){ res = {error:e.message} }
    if(peek.cache.size > 200) peek.cache.delete(peek.cache.keys().next().value);
  }
  if(seq !== peek.seq) return; // the pointer moved on meanwhile
  if(res.error) return renderPeek(c.link, title, `<div class="msg err">${esc(res.error)}</div>`);
  if(!res.rows.length) return renderPeek(c.link, title, '<div class="msg" style="color:var(--dim)">No matching row.</div>');
  const row = res.rows[0];
  renderPeek(c.link, title, `<table>${res.columns.map((col, i) => `<tr><td class="k">${esc(col)}</td><td class="${cellClass(row[i])}">${esc(cellText(row[i]).slice(0, 200))}</td></tr>`).join('')}</table>`);
}
document.addEventListener('mouseover', e => {
  const c = fkCell(e.target);
  if(!c){ if(peek.el && !e.target.closest?.('#peek')) hidePeek(); return }
  if(c.link === peek.el) return;
  hidePeek(); peek.el = c.link;
  peek.timer = setTimeout(() => showPeek(c), 350);
});
document.addEventListener('scroll', hidePeek, true);
document.addEventListener('mousedown', hidePeek);
let rowView = null;
function prettyVal(v){
  if(v === null) return 'NULL';
  if(typeof v === 'object') return JSON.stringify(v, null, 2);
  if(typeof v === 'string' && /^[\[{]/.test(v.trim())){ try{ return JSON.stringify(JSON.parse(v), null, 2) }catch{} }
  return String(v);
}
function showRow(t, ri){
  rowView = {t, ri};
  t.rsel = new Set([ri]); t.anchor = ri; paintSel(t);
  document.querySelector(`#pane [data-ri="${ri}"]`)?.scrollIntoView({block:'nearest'});
  const r = t.res, row = r.rows[ri], cols = t.cols ? Object.fromEntries(t.cols.map(c => [c.name, c])) : {}, fkOf = t.kind === 'table' ? fkMap(t) : {};
  const f = $('#mFilter').value.trim().toLowerCase();
  $('#mTitle').textContent = `${t.kind === 'table' ? t.table : 'result'} · row ${t.page * t.pageSize + ri + 1}`;
  $('#mBody').innerHTML = `<table class="rowv">${r.columns.map((c, ci) => {
    if(f && !c.toLowerCase().includes(f) && !String(row[ci]).toLowerCase().includes(f)) return '';
    const v = row[ci], m = cols[c], txt = esc(prettyVal(v));
    return `<tr><td class="k">${fieldLabel(c, m)}</td><td class="v ${cellClass(v)}">${fkOf[c] && v !== null ? `<span class="fk" data-ci="${ci}">${txt}</span>` : txt}</td></tr>`;
  }).join('')}</table>`;
  $('#mPrev').disabled = ri === 0; $('#mNext').disabled = ri >= r.rows.length - 1;
  modalButtons();
  $('#modal').classList.add('on');
}
function fieldLabel(c, m){ return `${m?.pk ? '🔑 ' : ''}${esc(c)}${m ? `<span class="ty">${esc(m.type)}</span>` : ''}` }
// modalButtons shows the header buttons for the modal's mode: text, row view or row editor.
function modalButtons(){
  const v = rowView, editing = !!v?.edit, show = (el, on) => el.style.display = on ? '' : 'none';
  const editable = !!v && !editing && v.t.kind === 'table' && isRW(v.t.src) && (v.t.cols || []).some(c => c.pk);
  for(const id of ['#mFilter', '#mPrev', '#mNext']) show($(id), !!v && !editing);
  show($('#mEdit'), editable); show($('#mDelete'), editable);
  show($('#mSave'), editing); show($('#mCancel'), editing); show($('#mCopy'), !editing);
}

// ---------- row editor (read-write sources) ----------
// Values are edited as text and parsed by the database as the column type. Updates and deletes
// address the row by its primary key; the server refuses them unless exactly one row matches.
const editText = v => v === null || v === undefined ? '' : typeof v === 'object' ? JSON.stringify(v, null, 2) : String(v);
function rowKey(t, ri){ return Object.fromEntries(t.cols.filter(c => c.pk).map(c => [c.name, t.res.rows[ri][t.res.columns.indexOf(c.name)]])) }
// editRow opens the editor for row ri of t, or for a new row if ri is null.
function editRow(t, ri){
  const row = ri == null ? null : t.res.rows[ri], names = row ? t.res.columns : t.cols.map(c => c.name);
  const meta = Object.fromEntries((t.cols || []).map(c => [c.name, c])), orig = names.map((_, i) => row ? row[i] : undefined);
  rowView = {t, ri, edit:true, names, orig, nul:new Set(orig.flatMap((v, i) => v === null ? [i] : []))};
  $('#mTitle').textContent = row ? `${t.table} \u00b7 edit row ${t.page * t.pageSize + ri + 1}` : `${t.table} \u00b7 new row`;
  $('#mBody').innerHTML = `<div class="msg err" id="mErr" style="display:none"></div><table class="rowv">${names.map((n, i) => {
    const o = orig[i], text = editText(o);
    return `<tr><td class="k">${fieldLabel(n, meta[n])}</td><td class="ed"><textarea data-i="${i}" rows="${Math.min(8, text.split('\n').length)}" spellcheck="false"
      placeholder="${o === null ? 'NULL' : row ? '' : 'default'}"${o === null ? ' disabled' : ''}>${esc(text)}</textarea><button class="nul${o === null ? ' on' : ''}" data-nul="${i}" title="Set to NULL">NULL</button></td></tr>`;
  }).join('')}</table>`;
  modalButtons();
  $('#modal').classList.add('on');
  $('#mBody textarea:not(:disabled)')?.focus();
}
// fieldValue returns the field's new value: a string, null, or undefined if it is to be left alone
// (unchanged in an update, untouched in an insert so it gets its default).
function fieldValue(i){
  const v = rowView, ta = $(`#mBody textarea[data-i="${i}"]`), nul = v.nul.has(i), o = v.orig[i];
  if(v.ri == null) return nul ? null : ta.value !== '' ? ta.value : undefined;
  if(nul) return o === null ? undefined : null;
  return o === null || ta.value !== editText(o) ? ta.value : undefined;
}
async function saveRow(){
  const v = rowView, t = v.t, changes = {}, base = {src:t.src, schema:t.schema, table:t.table};
  v.names.forEach((n, i) => { const val = fieldValue(i); if(val !== undefined) changes[n] = val });
  if(!Object.keys(changes).length) return toast('No changes');
  $('#mSave').disabled = true;
  try{
    if(v.ri == null){
      await api('/api/rows/insert', {...base, values:changes});
      closeModal(); toast('Row inserted'); peek.cache.clear(); reset(t);
    } else {
      const res = await api('/api/rows/update', {...base, key:rowKey(t, v.ri), set:changes});
      t.res.rows[v.ri] = t.res.columns.map(c => res.rows[0][res.columns.indexOf(c)]);
      peek.cache.clear(); keepScroll(() => renderGrid(t)); showRow(t, v.ri); toast('Row saved');
    }
  } catch(err){ const el = $('#mErr'); if(el){ el.textContent = err.message; el.style.display = '' } else toast(err.message) }
  finally{ $('#mSave').disabled = false }
}
async function deleteRow(){
  const {t, ri} = rowView;
  if(!confirm(`Delete this row from ${t.schema}.${t.table}? This cannot be undone.`)) return;
  try{
    await api('/api/rows/delete', {src:t.src, schema:t.schema, table:t.table, key:rowKey(t, ri)});
    t.res.rows.splice(ri, 1); t.rsel = new Set(); t.count = null;
    closeModal(); peek.cache.clear(); keepScroll(() => renderGrid(t)); toast('Row deleted');
  } catch(err){ toast(err.message) }
}
function keepScroll(f){ const w = $('.gridwrap'), top = w?.scrollTop; f(); if(w) w.scrollTop = top }
$('#mEdit').onclick = () => rowView && editRow(rowView.t, rowView.ri);
$('#mDelete').onclick = () => rowView && deleteRow();
$('#mSave').onclick = () => rowView?.edit && saveRow();
$('#mCancel').onclick = () => rowView?.ri == null ? closeModal() : showRow(rowView.t, rowView.ri);
$('#mBody').addEventListener('input', e => {
  const i = e.target.dataset?.i; if(i == null || !rowView?.edit) return;
  e.target.classList.toggle('chg', fieldValue(+i) !== undefined);
});
$('#mBody').addEventListener('click', e => {
  const nb = e.target.closest('[data-nul]');
  if(nb && rowView?.edit){
    const i = +nb.dataset.nul, ta = $(`#mBody textarea[data-i="${i}"]`), on = !rowView.nul.has(i);
    on ? rowView.nul.add(i) : rowView.nul.delete(i);
    nb.classList.toggle('on', on); ta.disabled = on; ta.placeholder = on ? 'NULL' : rowView.ri == null ? 'default' : '';
    ta.classList.toggle('chg', fieldValue(i) !== undefined);
    if(!on) ta.focus();
  }
});
$('#mBody').addEventListener('click', e => { const fk = e.target.closest('.fk[data-ci]'); if(fk && rowView){ closeModal(); followFk(rowView.t, rowView.ri, +fk.dataset.ci) } });
$('#mFilter').addEventListener('input', () => rowView && showRow(rowView.t, rowView.ri));
$('#mPrev').onclick = () => rowView && rowView.ri > 0 && showRow(rowView.t, rowView.ri - 1);
$('#mNext').onclick = () => rowView && rowView.ri < rowView.t.res.rows.length - 1 && showRow(rowView.t, rowView.ri + 1);
$('#pane').addEventListener('change', e => {
  const t = active, f = e.target.dataset.f; if(!t || !f) return;
  if(f === 'pageSize'){ t.pageSize = +e.target.value; saveTabs(); if(!t.res?.command) reset(t) } // never rerun a write
  if(f === 'src'){ t.src = e.target.value; renderTabs(); renderPane(); saveTabs() }
});
$('#pane').addEventListener('input', e => { const t = active, f = e.target.dataset.f; if(t && (f === 'where' || f === 'order' || f === 'sql')){ t[f] = e.target.value; saveTabs() } });
$('#pane').addEventListener('keydown', e => {
  const t = active, f = e.target.dataset.f; if(!t) return;
  if((f === 'where' || f === 'order') && e.key === 'Enter'){ e.preventDefault(); reset(t) }
  if(f === 'sql' && e.key === 'Enter' && (e.ctrlKey || e.metaKey)){ e.preventDefault(); t.sql = e.target.value; reset(t) }
  if(f === 'sql' && e.key === 'Tab'){ e.preventDefault(); e.target.setRangeText('  ', e.target.selectionStart, e.target.selectionEnd, 'end') }
  if(f === 'ask' && e.key === 'Enter'){ e.preventDefault(); ask(t) }
});
document.addEventListener('keydown', e => {
  const modal = $('#modal').classList.contains('on'), typing = /^(INPUT|TEXTAREA|SELECT)$/.test(document.activeElement?.tagName);
  if(e.key === 'Escape'){ hidePeek(); closeModal() }
  if(modal && rowView?.edit && e.key === 'Enter' && (e.ctrlKey || e.metaKey)){ e.preventDefault(); saveRow(); return }
  if(modal && rowView && !rowView.edit && !typing && (e.key === 'ArrowUp' || e.key === 'ArrowDown')){ e.preventDefault(); (e.key === 'ArrowUp' ? $('#mPrev') : $('#mNext')).click() }
  if(!modal && !typing && active?.res && (e.ctrlKey || e.metaKey) && !e.altKey){
    if(e.key === 'c' && active.rsel?.size && window.getSelection().isCollapsed){ e.preventDefault(); copyRows(active) }
    if(e.key === 'a'){ e.preventDefault(); active.rsel = new Set(active.res.rows.map((_, i) => i)); paintSel(active) }
  }
  if(e.key === 'Enter' && (e.ctrlKey || e.metaKey) && active && !modal && !['sql', 'where', 'order', 'ask'].includes(e.target.dataset?.f)){ e.preventDefault(); if(active.kind === 'console') active.sql = $('[data-f="sql"]').value; reset(active) }
});
async function ask(t){
  const inp = $('[data-f="ask"]'), prompt = inp.value.trim(); if(!prompt) return;
  const btn = $('[data-a="ask"]'); btn.disabled = inp.disabled = true; btn.textContent = 'Thinking…';
  try{
    const {sql} = await api('/api/assist', {src:t.src, prompt, current_sql:$('[data-f="sql"]').value});
    t.sql = sql; if(t === active){ $('[data-f="sql"]').value = sql } saveTabs();
    if(isRW(t.src)) toast('Read-write source: review the query, then run it'); else reset(t);
  } catch(err){ toast(err.message); t.error = `AI: ${err.message}`; renderGrid(t) }
  finally{ if(t === active){ const b2 = $('[data-a="ask"]'), i2 = $('[data-f="ask"]'); if(b2){ b2.disabled = false; b2.textContent = 'Generate' } if(i2) i2.disabled = false } }
}
function showModal(title, body){
  rowView = null; $('#mTitle').textContent = title;
  $('#mBody').innerHTML = `<pre>${esc(body)}</pre>`;
  modalButtons();
  $('#modal').classList.add('on');
}
function closeModal(){ $('#modal').classList.remove('on') }
$('#mClose').onclick = closeModal;
$('#modal').onclick = e => { if(e.target.id === 'modal' && !rowView?.edit) closeModal() }; // a stray click must not drop edits
$('#mCopy').onclick = async () => {
  let txt = $('#mBody').textContent;
  if(rowView){ const {t, ri} = rowView; txt = t.res.columns.map((c, ci) => `${c}\t${tsvVal(t.res.rows[ri][ci])}`).join('\n') }
  try{ await navigator.clipboard.writeText(txt); toast('Copied') }catch{}
};

// ---------- boot ----------
(async () => {
  await loadSources().catch(e => $('#tree').innerHTML = `<div class="msg err">${esc(e.message)}</div>`);
  for(const s of store.get('tabs', [])){
    const t = {...s, id:++seq, page:0, pageSize:s.pageSize || 100, where:s.where || '', order:s.order || '', sql:s.sql || ''};
    if(t.kind === 'console') t.n = tabs.filter(x => x.kind === 'console').length + 1;
    tabs.push(t);
  }
  const ai = store.get('activeIdx', -1);
  active = tabs[ai] || tabs[0] || null;
  if(active) activate(active); else { renderTabs(); renderPane() }
})();
