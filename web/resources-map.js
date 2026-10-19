// resources map rendering, reuses ambient map styles and routing
(function(){
  let expanded = {};
  function rmLoad(){
    const c = S.cluster || '';
    const ns = (document.getElementById('rm-ns')||{}).value || '';
    let kinds = [];
    document.querySelectorAll('#rm-kinds input:checked').forEach(ch=>kinds.push(ch.value));
    const q = 'cluster='+encodeURIComponent(c) + (ns?'&namespace='+encodeURIComponent(ns):'') + (kinds.length?'&kinds='+encodeURIComponent(kinds.join(',')):'');
    fetch('/api/resources/map?'+q).then(r=>r.json()).then(d=>{
      const root = document.getElementById('rm-root');
      if (!root) return;
      root.innerHTML = '';
      const tb = document.createElement('div');
      tb.className = 'rm-toolbar';
      tb.innerHTML = '<select id="rm-ns"><option value="">all</option></select> <span id="rm-kinds"></span> <input id="rm-search" placeholder="search"> <button id="rm-zoom-in">+</button><button id="rm-zoom-out">−</button><button id="rm-fit">fit</button><button id="rm-full">full</button>';
      root.appendChild(tb);
      const canv = document.createElement('div');
      canv.className = 'am-map-viewport';
      const svg = document.createElementNS('http://www.w3.org/2000/svg','svg');
      svg.setAttribute('class','am-topology');
      canv.appendChild(svg);
      root.appendChild(canv);
      // ns select
      const sel = document.getElementById('rm-ns');
      const nss = (d.namespaces||[]).map(b=>b.namespace||'cluster');
      nss.forEach(n=>{ const o=document.createElement('option'); o.value=n==='cluster'?'':n; o.textContent=n; sel.appendChild(o); });
      sel.value = ns;
      sel.onchange = rmLoad;
      // kinds
      const kindsBox = document.getElementById('rm-kinds');
      const allKinds = {};
      (d.nodes||[]).forEach(n=>{ allKinds[n.kind]=true; });
      Object.keys(allKinds).forEach(k=>{
        const lab = document.createElement('label');
        lab.innerHTML = '<input type="checkbox" value="'+k+'" checked> '+k;
        kindsBox.appendChild(lab);
        lab.firstChild.onchange = rmLoad;
      });
      // search
      const search = document.getElementById('rm-search');
      search.oninput = ()=> renderMap(d, svg, search.value);
      // zoom etc reuse? simple
      document.getElementById('rm-zoom-in').onclick = ()=> { svg.style.transform = (svg.style.transform||'').replace(/scale\([0-9.]+\)/,'') + ' scale(1.2)'; };
      document.getElementById('rm-zoom-out').onclick = ()=> { svg.style.transform = (svg.style.transform||'').replace(/scale\([0-9.]+\)/,'') + ' scale(0.8)'; };
      document.getElementById('rm-fit').onclick = ()=> { svg.style.transform=''; };
      document.getElementById('rm-full').onclick = ()=> document.documentElement.requestFullscreen();
      renderMap(d, svg, '');
      // notes
      if (d.notes && d.notes.length){
        const nt = document.createElement('div'); nt.className='muted'; nt.textContent = d.notes.join(' ');
        root.appendChild(nt);
      }
    });
  }
  function renderMap(d, svg, search){
    svg.innerHTML = '';
    const kept = {};
    const q = (search||'').toLowerCase();
    (d.nodes||[]).forEach(n=>{
      if (q && n.name.toLowerCase().indexOf(q)<0) return;
      kept[n.id] = true;
      const g = document.createElementNS('http://www.w3.org/2000/svg','g');
      const rect = document.createElementNS('http://www.w3.org/2000/svg','rect');
      rect.setAttribute('class','am-canvas-card');
      rect.setAttribute('x','10'); rect.setAttribute('y','10'); rect.setAttribute('width','120'); rect.setAttribute('height','40');
      g.appendChild(rect);
      const t = document.createElementNS('http://www.w3.org/2000/svg','text');
      t.textContent = (n.badge?'★ ':'') + n.name;
      t.setAttribute('x','15'); t.setAttribute('y','30');
      g.appendChild(t);
      g.onclick = ()=> openObject({cluster:n.cluster, group:n.group, resource:n.resource, namespace:n.namespace, name:n.name});
      svg.appendChild(g);
    });
    (d.edges||[]).forEach(e=>{
      if (!kept[e.from] || (e.to && !kept[e.to])) return;
      const p = document.createElementNS('http://www.w3.org/2000/svg','path');
      p.setAttribute('class', e.broken ? 'am-map-line rm-broken' : 'am-map-line');
      p.setAttribute('d', 'M10,30 L 200,30'); // placeholder, real uses amRoute
      svg.appendChild(p);
    });
  }
  function rmEdgeClass(edge){ return edge && edge.broken ? 'am-map-line rm-broken' : 'am-map-line'; }
  function rmVisible(node, filter){
    if (!filter) return true;
    if (filter.kinds && filter.kinds.length && filter.kinds.indexOf(node.kind)<0) return false;
    if (filter.search && node.name.indexOf(filter.search)<0) return false;
    return true;
  }
  window.rmLoad = rmLoad;
  window.rmEdgeClass = rmEdgeClass;
  window.rmVisible = rmVisible;
})();
