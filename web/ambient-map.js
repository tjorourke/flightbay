'use strict';

const amMapKey=r=>JSON.stringify([r.kind==='Gateway'?'gateway':'service',r.cluster,r.namespace,r.name]);

function amClusterCanvas(target) {
  const A=S.ambient, clusters=A.inventory?.topology?.clusters||[];
  if(!clusters.length)return '';
  const namespace=target?.kind==='Namespace'?target.name:target?.namespace||'',scope=A.mapAllNamespaces?'':namespace;
  A.mapLayouts ||= {};
  A.mapLayoutKey=JSON.stringify([scope,clusters.map(c=>c.context)]);
  const layout=A.mapLayouts[A.mapLayoutKey] ||= {positions:{},zoom:1,expanded:{},views:{}};
  layout.expanded ||= {};layout.views ||= {};
  let nextX=24;
  const boxes=clusters.map(c=>{
    const id=JSON.stringify(['cluster',c.context]),wide=!!layout.expanded[c.context],width=wide?960:620;
    const view=layout.views[c.context]||'cards',list=view==='list',columns=wide?3:2;
    const p=layout.positions[id]||{x:nextX,y:24};nextX+=width+360;
    const services=c.services.filter(s=>!scope||s.namespace===scope).sort((a,b)=>Number(!!b.globalHosts?.length)-Number(!!a.globalHosts?.length));
    const items=[];
    for(const g of c.gateways)items.push({ref:g,gateway:true,html:`<div class="am-canvas-card-head"><span class="am-gateway-symbol" aria-hidden="true">⇄</span><strong>${g.role==='east-west'?'East-west gateway':'Remote peer'}</strong>${amState(g.state)}</div><button class="link" data-amevidence="${attr(g)}">${esc(g.namespace+'/'+g.name)}</button><small>${esc(g.class)} · ports ${g.ports.map(esc).join(', ')||'not captured'}</small><small>${esc(g.addresses.join(', ')||'Address not captured')}</small>${g.peer?`<div class="am-peer-label">Configured peer → ${esc(g.peerContext||g.peer)}${g.peerContext?'':' · context not established'}</div>`:''}<div class="row wrap"><button data-amevidence="${attr(g)}">View object</button><button data-amyaml="${attr(g)}">YAML</button></div>`});
    for(const s of services)items.push({ref:s,html:`<div class="am-canvas-card-head"><span class="badge">${s.infrastructure?'Gateway Service':'Service'}</span><span class="${s.state==='pass'?'green':s.state==='fail'?'red':'muted'}">${esc(s.detail)}</span></div><strong>${esc(s.name)}</strong><small>${esc(s.cluster)} · ${esc(s.namespace)}</small>${s.globalHosts?.length?`<small class="am-global-label" title="${esc(s.globalHosts.join(', '))}">Global: ${esc(s.globalHosts[0])}${s.globalHosts.length>1?' (+'+(s.globalHosts.length-1)+' more)':''}</small>`:s.scope?`<small class="am-global-label">Export scope: ${esc(s.scope)} · hostname not captured</small>`:''}${!list?`<small>${s.waypoint?'Waypoint: '+esc(s.waypoint):'No waypoint selected'} · ${s.routes} routes${s.takeover?' · local-name takeover':''}</small>`:''}<div class="row wrap"><button data-amdrill="${attr(s)}">Connections</button><button data-amconfigure="${attr(s)}">Configure</button><button data-amyaml="${attr(s)}">YAML</button></div>`});
    let height=250,listY=144;
    const cardWidth=list?width-32:(width-32-(columns-1)*16)/columns;
    const rendered=items.map((item,i)=>{
      const nodeID=amMapKey(item.ref),positionID=view+(wide?'-wide':'')+'|'+nodeID,h=list?(item.gateway?192:146):252;
      const pos=layout.positions[positionID]||{x:list?16:16+(i%columns)*(cardWidth+16),y:list?listY:144+Math.floor(i/columns)*280};listY+=h+24;
      const x=Math.min(pos.x,width-cardWidth-16),y=pos.y;height=Math.max(height,y+h+24);
      return `<article class="am-canvas-card ${list?'am-canvas-list':''} ${item.gateway?'am-canvas-gateway':''} ${target&&amKey(item.ref)===amKey(target)?'am-canvas-selected':''}" style="left:${x}px;top:${y}px;width:${cardWidth}px;height:${h}px" data-map-object="${attr(item.ref)}" data-map-node="${esc(nodeID)}"><button class="am-drag-handle" data-map-drag="${esc(positionID)}" aria-label="Move ${esc(item.ref.name)}" title="Drag to move; arrow keys also move this card">⠿</button>${item.html}</article>`;
    }).join('');
    return `<section class="am-cluster-box" style="left:${p.x}px;top:${p.y}px;width:${width}px;height:${height}px" data-map-node="${esc(id)}"><header><button class="am-cluster-handle" data-map-drag="${esc(id)}" aria-label="Move cluster ${esc(c.context)}" title="Drag cluster; arrow keys also move it"><img class="am-kubernetes-icon" src="static/vendor/kubernetes.svg" alt="Kubernetes"><span><strong>${esc(c.context)}</strong><small>${c.identity?'Mesh: '+esc(c.identity):'Mesh identity not captured'}${c.network?' · Network: '+esc(c.network):''}</small></span><span class="am-drag-dots" aria-hidden="true">⠿</span></button><div class="am-cluster-bar"><small>${services.length} Services · ${c.gateways.length} east-west/peer gateways</small><div class="row"><button data-map-view="${esc(c.context)}" data-view-mode="cards" aria-pressed="${!list}">Cards</button><button data-map-view="${esc(c.context)}" data-view-mode="list" aria-pressed="${list}">List</button><button data-map-expand="${esc(c.context)}" aria-expanded="${wide}">${wide?'Compact':'Expand'}</button></div></div></header>${rendered}${!items.length?'<p class="am-canvas-empty">No resources captured in this scope. Missing data does not prove absence.</p>':''}</section>`;
  });
  const links=A.inventory.topology.links||[];
  const legend=links.map(link=>`<button class="am-connection-chip ${link.kind==='peering'?'am-peer-chip':'am-global-chip'}" data-map-link="${esc(link.id)}" aria-pressed="${A.mapSelectedLink===link.id}" title="Highlight the captured relationship and its resource endpoints">${link.kind==='peering'?'⇄':'◇'} ${esc(link.label)}</button>`).join('');
  return `<section class="panel am-topology ${A.mapFullscreen?'am-map-fullscreen':''}"><div class="panel-head"><div><h2>Cluster attachment map</h2><p class="muted">${clusters.length} contexts${scope?' · namespace '+esc(scope):' · all captured namespaces'} · drag boxes and resource handles to arrange</p></div><div class="row wrap am-map-controls"><button id="am-map-minus" aria-label="Zoom out">−</button><output id="am-map-zoom">${Math.round(layout.zoom*100)}%</output><button id="am-map-plus" aria-label="Zoom in">+</button><button id="am-map-fit">Fit</button><button id="am-map-reset">Reset layout</button><button id="am-map-fullscreen">${A.mapFullscreen?'Exit full screen':'Full screen'}</button></div></div><div class="am-map-tools"><button id="am-map-search-top">Search objects · ⌘K</button>${namespace?`<label class="am-map-scope"><input id="am-map-all-namespaces" type="checkbox" ${A.mapAllNamespaces?'checked':''}> All namespaces</label>`:''}<span class="muted">Use the top search to focus a resource. Click a connection below to highlight it.</span></div>${legend?`<div class="am-map-connections">${legend}</div>`:''}<div class="am-map-viewport" id="am-map-viewport" tabindex="0" aria-label="Cluster map. Drag empty space to pan; use zoom controls or Ctrl plus wheel to zoom."><div id="am-map-space"><div id="am-map-world"><svg id="am-map-lines" xmlns="http://www.w3.org/2000/svg" aria-label="Captured configuration relationships"></svg>${boxes.join('')}</div></div></div><p class="am-map-legend"><span id="am-map-line-status"></span> Teal: peer configuration. Purple dashed: global Service publication. Lines are captured configuration, not a live traffic trace. ${S.cluster?'All captured contexts are shown.':''}</p></section>`;
}

function amWireClusterCanvas(){
  const A=S.ambient,viewport=$('#am-map-viewport');if(!viewport)return;
  A.mapObserver?.disconnect();
  const layout=A.mapLayouts[A.mapLayoutKey],world=$('#am-map-world'),space=$('#am-map-space');
  const resize=()=>{
    let width=700,height=450;
    world.querySelectorAll('.am-cluster-box').forEach(box=>{width=Math.max(width,box.offsetLeft+box.offsetWidth+32);height=Math.max(height,box.offsetTop+box.offsetHeight+32);});
    A.mapWorld={width,height};world.style.width=width+'px';world.style.height=height+'px';world.style.transform=`scale(${layout.zoom})`;space.style.width=width*layout.zoom+'px';space.style.height=height*layout.zoom+'px';$('#am-map-zoom').textContent=Math.round(layout.zoom*100)+'%';
    amDrawMapLinks(world,layout.zoom,A.inventory.topology.links||[],A.mapSelectedLink);
  };
  const zoom=(value,anchorX=viewport.clientWidth/2,anchorY=viewport.clientHeight/2)=>{const x=(viewport.scrollLeft+anchorX)/layout.zoom,y=(viewport.scrollTop+anchorY)/layout.zoom;layout.zoom=Math.min(1.75,Math.max(.2,value));resize();viewport.scrollLeft=x*layout.zoom-anchorX;viewport.scrollTop=y*layout.zoom-anchorY;};
  $('#am-map-minus').onclick=()=>zoom(layout.zoom/1.2);$('#am-map-plus').onclick=()=>zoom(layout.zoom*1.2);
  $('#am-map-fit').onclick=()=>{zoom(Math.min(1,viewport.clientWidth/A.mapWorld.width,viewport.clientHeight/A.mapWorld.height),0,0);viewport.scrollTo(0,0);};
  $('#am-map-reset').onclick=()=>{delete A.mapLayouts[A.mapLayoutKey];A.mapSelectedLink='';render();};
  $('#am-map-fullscreen').onclick=()=>{A.mapFullscreen=!A.mapFullscreen;render();};
  $('#am-map-search-top').onclick=openSearch;
  $('#am-map-all-namespaces')?.addEventListener('change',e=>{A.mapAllNamespaces=e.target.checked;render();});
  document.querySelectorAll('[data-map-view]').forEach(b=>b.onclick=()=>{layout.views[b.dataset.mapView]=b.dataset.viewMode;render();});
  document.querySelectorAll('[data-map-expand]').forEach(b=>b.onclick=()=>{layout.expanded[b.dataset.mapExpand]=!layout.expanded[b.dataset.mapExpand];render();});
  document.querySelectorAll('[data-map-link]').forEach(b=>b.onclick=()=>{A.mapSelectedLink=A.mapSelectedLink===b.dataset.mapLink?'':b.dataset.mapLink;document.querySelectorAll('[data-map-link]').forEach(el=>el.setAttribute('aria-pressed',String(el.dataset.mapLink===A.mapSelectedLink)));resize();const link=A.inventory.topology.links.find(l=>l.id===A.mapSelectedLink);if(link){const ends=[...world.querySelectorAll('[data-map-object]')].filter(e=>[amMapKey(link.source),amMapKey(link.target)].includes(e.dataset.mapNode));if(ends.length===2){const points=ends.map(e=>{const box=e.closest('.am-cluster-box');return{x:box.offsetLeft,y:box.offsetTop,w:box.offsetWidth,h:e.offsetTop+e.offsetHeight};}),left=Math.min(...points.map(p=>p.x)),top=Math.min(...points.map(p=>p.y)),right=Math.max(...points.map(p=>p.x+p.w)),bottom=Math.max(...points.map(p=>p.y+p.h));zoom(Math.min(1.2,viewport.clientWidth/(right-left+60),viewport.clientHeight/(bottom-top+100)),0,0);viewport.scrollLeft=left*layout.zoom-25;viewport.scrollTop=top*layout.zoom-25;}}});
  viewport.addEventListener('wheel',e=>{if(!e.ctrlKey&&!e.metaKey)return;e.preventDefault();const r=viewport.getBoundingClientRect();zoom(layout.zoom*(e.deltaY<0?1.1:1/1.1),e.clientX-r.left,e.clientY-r.top);},{passive:false});
  viewport.onpointerdown=e=>{if(e.button!==0||e.target.closest('.am-cluster-box'))return;viewport.setPointerCapture(e.pointerId);const x=e.clientX,y=e.clientY,left=viewport.scrollLeft,top=viewport.scrollTop;viewport.classList.add('am-panning');viewport.onpointermove=event=>{viewport.scrollLeft=left+x-event.clientX;viewport.scrollTop=top+y-event.clientY;};const finish=()=>{viewport.onpointermove=null;viewport.classList.remove('am-panning');};viewport.onpointerup=finish;viewport.onpointercancel=finish;};
  const move=(node,id,x,y)=>{const cluster=node.classList.contains('am-cluster-box');x=Math.max(cluster?16:12,x);y=Math.max(cluster?16:144,y);if(!cluster){x=Math.min(x,Math.max(12,node.parentElement.clientWidth-node.offsetWidth-12));node.parentElement.style.height=Math.max(node.parentElement.offsetHeight,y+node.offsetHeight+24)+'px';}node.style.left=x+'px';node.style.top=y+'px';layout.positions[id]={x,y};resize();};
  world.querySelectorAll('[data-map-drag]').forEach(handle=>{
    const node=handle.closest('[data-map-node]'),id=handle.dataset.mapDrag;
    handle.onpointerdown=e=>{if(e.button!==0)return;e.preventDefault();handle.setPointerCapture(e.pointerId);const x=node.offsetLeft,y=node.offsetTop,startX=e.clientX,startY=e.clientY;node.classList.add('am-dragging');handle.onpointermove=event=>move(node,id,x+(event.clientX-startX)/layout.zoom,y+(event.clientY-startY)/layout.zoom);const finish=()=>{node.classList.remove('am-dragging');handle.onpointermove=null;handle.onpointerup=null;handle.onpointercancel=null;};handle.onpointerup=finish;handle.onpointercancel=finish;};
    handle.onkeydown=e=>{const d={ArrowLeft:[-1,0],ArrowRight:[1,0],ArrowUp:[0,-1],ArrowDown:[0,1]}[e.key];if(!d)return;e.preventDefault();e.stopPropagation();const step=e.shiftKey?40:10;move(node,id,node.offsetLeft+d[0]*step,node.offsetTop+d[1]*step);};
  });
  viewport.onkeydown=e=>{if(e.key==='Escape'&&A.mapFullscreen){e.stopPropagation();A.mapFullscreen=false;render();}};
  resize();if(!layout.initialized){layout.initialized=true;layout.zoom=Math.min(1,viewport.clientWidth/A.mapWorld.width);resize();}viewport.scrollLeft=layout.scrollLeft||0;viewport.scrollTop=layout.scrollTop||0;
  viewport.onscroll=()=>{layout.scrollLeft=viewport.scrollLeft;layout.scrollTop=viewport.scrollTop;};
  if(A.mapFocus){const focused=[...world.querySelectorAll('[data-map-object]')].find(e=>amKey(JSON.parse(e.dataset.mapObject))===A.mapFocus);if(focused){const box=focused.closest('.am-cluster-box');viewport.scrollLeft=(box.offsetLeft+focused.offsetLeft+focused.offsetWidth/2)*layout.zoom-viewport.clientWidth/2;viewport.scrollTop=(box.offsetTop+focused.offsetTop)*layout.zoom-80;}A.mapFocus=null;}
  A.mapObserver=new ResizeObserver(resize);A.mapObserver.observe(viewport);
}
