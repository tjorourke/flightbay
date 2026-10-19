// rbac explorer page and wire
(function(){
  function rbacPage(){
    const q = S.rbac || {};
    const url = '/api/rbac/serviceaccount?cluster=' + encodeURIComponent(q.cluster||S.cluster||'') + '&namespace=' + encodeURIComponent(q.namespace||'') + '&name=' + encodeURIComponent(q.name||'');
    fetch(url).then(r=>r.json()).then(d=>{
      const root = document.getElementById('rbac-root') || document.body;
      root.innerHTML = '';
      const h = document.createElement('h2');
      h.textContent = 'Service account ' + (d.namespace||'') + '/' + (d.name||'');
      root.appendChild(h);
      if (!d.found) {
        const m = document.createElement('p');
        m.textContent = 'This ServiceAccount is not in the capture.';
        root.appendChild(m);
      }
      if (d.coverage && !d.coverage.complete) {
        const n = document.createElement('div');
        n.className = 'amber';
        n.textContent = d.coverage.reason || 'incomplete';
        root.appendChild(n);
      }
      // workloads
      const wh = document.createElement('h3'); wh.textContent = 'Workloads'; root.appendChild(wh);
      const wt = document.createElement('table');
      wt.innerHTML = '<tr><th>Kind</th><th>Name</th><th>Pods</th></tr>';
      (d.workloads||[]).forEach(w=>{
        const tr = document.createElement('tr');
        tr.innerHTML = '<td>'+w.ref.kind+'</td><td>'+w.ref.name+'</td><td>'+w.pods+'</td>';
        tr.onclick = ()=> openObject(w.ref);
        wt.appendChild(tr);
      });
      root.appendChild(wt);
      // permissions
      const ph = document.createElement('h3'); ph.textContent = 'Permissions'; root.appendChild(ph);
      const ns = document.createElement('h4'); ns.textContent = 'In this namespace'; root.appendChild(ns);
      const pt = document.createElement('table');
      pt.innerHTML = '<tr><th>API group</th><th>Resource</th><th>Sub</th><th>Verb</th><th>Names</th><th>Binding</th><th>Flags</th></tr>';
      (d.grants||[]).filter(g=>g.scope !== 'cluster').forEach(g=>{
        const tr = document.createElement('tr');
        const fl = (g.flags||[]).join(' ');
        tr.innerHTML = '<td>'+(g.apiGroup||'')+'</td><td>'+g.resource+'</td><td>'+(g.subresource||'')+'</td><td>'+g.verb+'</td><td>'+(g.resourceNames||[]).join(',')+'</td><td><button data-bind="'+g.binding.name+'">'+g.binding.name+'</button></td><td class="'+(fl?'red':'')+'">'+fl+'</td>';
        tr.querySelector('button').onclick = (e)=>{ e.stop(); openObject(g.binding); };
        pt.appendChild(tr);
      });
      root.appendChild(pt);
      const ch = document.createElement('h4'); ch.textContent = 'Cluster-wide'; root.appendChild(ch);
      const ct = document.createElement('table');
      ct.innerHTML = '<tr><th>API group</th><th>Resource</th><th>Sub</th><th>Verb</th><th>Names</th><th>Binding</th><th>Flags</th></tr>';
      (d.grants||[]).filter(g=>g.scope === 'cluster').forEach(g=>{
        const tr = document.createElement('tr');
        const fl = (g.flags||[]).join(' ');
        tr.innerHTML = '<td>'+(g.apiGroup||'')+'</td><td>'+g.resource+'</td><td>'+(g.subresource||'')+'</td><td>'+g.verb+'</td><td>'+(g.resourceNames||[]).join(',')+'</td><td><button data-bind="'+g.binding.name+'">'+g.binding.name+'</button></td><td class="'+(fl?'red':'')+'">'+fl+'</td>';
        tr.querySelector('button').onclick = (e)=>{ e.stop(); openObject(g.binding); };
        ct.appendChild(tr);
      });
      root.appendChild(ct);
      // bindings
      const bh = document.createElement('h3'); bh.textContent = 'Bindings'; root.appendChild(bh);
      (d.bindings||[]).forEach(b=>{
        const div = document.createElement('div');
        div.textContent = b.binding.kind + ' ' + b.binding.name + ' ' + (b.why||'') + (b.roleMissing?' (role not captured)':'') + (b.invalidRoleRef?' (roleRef invalid)':'');
        div.onclick = ()=> openObject(b.binding);
        root.appendChild(div);
      });
      // who can
      const woh = document.createElement('h3'); woh.textContent = 'Who can'; root.appendChild(woh);
      const form = document.createElement('form');
      form.innerHTML = 'verb <input name="verb" value="get"> resource <input name="resource" value="pods"> <button>query</button>';
      form.onsubmit = function(ev){
        ev.preventDefault();
        const qq = new URLSearchParams(new FormData(form));
        qq.set('cluster', q.cluster||S.cluster||'');
        qq.set('namespace', q.namespace||'');
        fetch('/api/rbac/who?'+qq).then(r=>r.json()).then(ww=>{
          const tbl = document.createElement('table');
          tbl.innerHTML = '<tr><th>Kind</th><th>NS</th><th>Name</th><th>Via</th><th>Bindings</th></tr>';
          (ww.subjects||[]).forEach(su=>{
            const tr = document.createElement('tr');
            tr.innerHTML = '<td>'+su.kind+'</td><td>'+su.namespace+'</td><td>'+su.name+'</td><td>'+su.via+'</td><td>'+(su.bindings||[]).map(bb=>bb.name).join(',')+'</td>';
            tbl.appendChild(tr);
          });
          root.appendChild(tbl);
        });
      };
      root.appendChild(form);
      // suggestion
      const sh = document.createElement('h3'); sh.textContent = 'Suggestion'; root.appendChild(sh);
      const note = document.createElement('p'); note.textContent = 'Review text only. Nothing is applied.'; root.appendChild(note);
      const pre = document.createElement('pre'); pre.className='code'; pre.textContent = d.suggestion || ''; root.appendChild(pre);
      const cp = document.createElement('button'); cp.id='d-copy'; cp.textContent='Copy YAML'; cp.onclick=()=>{ navigator.clipboard.writeText(pre.textContent); }; root.appendChild(cp);
    }).catch(()=>{});
  }
  function rbacWireObject(o){
    if (!o || o.kind !== 'ServiceAccount') return;
    const desc = document.getElementById('d-describe');
    if (!desc) return;
    const b = document.createElement('button');
    b.textContent = 'Service account RBAC';
    b.onclick = function(){
      closeDrawer();
      const c = o.cluster || S.cluster || '';
      nav('rbac?cluster='+encodeURIComponent(c)+'&namespace='+encodeURIComponent(o.namespace||'')+'&name='+encodeURIComponent(o.name||''));
    };
    desc.parentNode.insertBefore(b, desc.nextSibling);
  }
  // identity open
  // hook in app.js
  window.rbacWireObject = rbacWireObject;
  window.rbacPage = rbacPage;
  // also for workloads in identity block
  // (hooked from app)
})();
