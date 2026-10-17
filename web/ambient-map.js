'use strict';

// A navigable configuration canvas. Layout changes are local view state only.
function amClusterCanvas(target) {
  const A=S.ambient, clusters=A.inventory?.topology?.clusters||[];
  if(!clusters.length)return '';
  const namespace=target?.kind==='Namespace'?target.name:target?.namespace||'';
  const scope=A.mapAllNamespaces?'':namespace;
  const key=JSON.stringify([scope,clusters.map(c=>c.context)]);
  A.mapLayouts ||= {};
  const layout=A.mapLayouts[key] ||= {positions:{},zoom:1};
  A.mapLayoutKey=key;
  const q=(A.mapSearch||'').trim().toLowerCase();
  const boxes=clusters.map((c,index)=>{
    const services=c.services.filter(s=>(!scope||s.namespace===scope)&&(!q||`${s.namespace}/${s.name}`.toLowerCase().includes(q)));
    const id=JSON.stringify(['cluster',c.context]);
    const p=layout.positions[id]||{x:24+index*650,y:24};
    const cards=[];
    for(const g of c.gateways){
      cards.push({id:JSON.stringify(['gateway',g.cluster,g.namespace,g.name]),gateway:true,html:`<div class="am-canvas-card-head"><span class="am-gateway-symbol" aria-hidden="true">⇄</span><strong>${g.role==='east-west'?'East-west gateway':'Remote peer'}</strong>${amState(g.state)}</div><button class="link" data-amevidence="${attr(g)}">${esc(g.namespace+'/'+g.name)}</button><small>${esc(g.class)} · ${g.ports.map(esc).join(', ')||'No listeners captured'}</small><small>${esc(g.addresses.join(', ')||'Address not captured')}</small>${g.peer?`<div class="am-peer-label">Configured peer → ${esc(g.peerContext||g.peer)}${g.peerContext?'':' · context not established'}</div>`:''}<small>Controller status only · traffic not tested here</small>`});
    }
    for(const s of services){
      cards.push({id:JSON.stringify(['service',s.cluster,s.namespace,s.name]),selected:target?.kind==='Service'&&amKey(s)===amKey(target),html:`<div class="am-canvas-card-head"><span class="badge">${s.infrastructure?'Gateway Service':'Service'}</span><span class="${s.state==='pass'?'green':s.state==='fail'?'red':'muted'}">${esc(s.detail)}</span></div><strong>${esc(s.name)}</strong><small>${esc(s.namespace)}</small>${s.scope?`<small class="am-global-label">Export scope: ${esc(s.scope)}${s.takeover?' · local-name takeover':''}</small>`:''}<small>${s.waypoint?'Waypoint: '+esc(s.waypoint):'No waypoint selected'} · ${s.routes} routes</small><div class="row wrap"><button data-amdrill="${attr(s)}">Connections</button><button data-amconfigure="${attr(s)}">Configure</button><button class="ghost" data-amevidence="${attr(s)}">YAML</button></div>`});
    }
    let height=128;
    const rendered=cards.map((card,i)=>{
      const pos=layout.positions[card.id]||{x:16+(i%2)*302,y:110+Math.floor(i/2)*230};
      height=Math.max(height,pos.y+230);
      return `<article class="am-canvas-card ${card.gateway?'am-canvas-gateway':''} ${card.selected?'am-canvas-selected':''}" style="left:${pos.x}px;top:${pos.y}px" data-map-node="${esc(card.id)}"><button class="am-drag-handle" data-map-drag="${esc(card.id)}" aria-label="Move ${card.gateway?'gateway':'Service'} card" title="Drag to move; arrow keys move the focused handle">⠿</button>${card.html}</article>`;
    }).join('');
    height=Math.max(height,240);
    return {x:p.x,y:p.y,height,html:`<section class="am-cluster-box" style="left:${p.x}px;top:${p.y}px;height:${height}px" data-map-node="${esc(id)}"><header><button class="am-cluster-handle" data-map-drag="${esc(id)}" aria-label="Move cluster ${esc(c.context)}" title="Drag cluster; arrow keys also move it"><span class="am-cluster-symbol" aria-hidden="true">⬡</span><span><strong>${esc(c.context)}</strong><small>${c.identity?'Mesh: '+esc(c.identity):'Mesh identity not captured'}${c.network?' · Network: '+esc(c.network):''}</small></span><span class="am-drag-dots" aria-hidden="true">⠿</span></button><div class="am-cluster-caption">${services.length} Services${scope?' · '+esc(scope):' · all namespaces'} · ${c.gateways.length} captured east-west/peer gateways</div></header>${rendered}${!cards.length?'<p class="am-canvas-empty">No matching Services or east-west gateways captured. Missing data does not prove absence.</p>':''}</section>`};
  });
  A.mapWorld={width:Math.max(700,...boxes.map(b=>b.x+642)),height:Math.max(450,...boxes.map(b=>b.y+b.height+24))};
  return `<section class="panel am-topology"><div class="panel-head"><div><h2>Cluster attachment map</h2><p class="muted">${clusters.length} captured context${clusters.length===1?'':'s'} · drag cluster headers or card handles to arrange</p></div><div class="row wrap am-map-controls"><button id="am-map-minus" aria-label="Zoom out">−</button><output id="am-map-zoom">${Math.round(layout.zoom*100)}%</output><button id="am-map-plus" aria-label="Zoom in">+</button><button id="am-map-fit">Fit</button><button id="am-map-reset">Reset layout</button></div></div><div class="am-map-tools"><label>Find a Service <input id="am-map-search" value="${esc(A.mapSearch||'')}" placeholder="Name or namespace…"></label>${namespace?`<label class="am-map-scope"><input id="am-map-all-namespaces" type="checkbox" ${A.mapAllNamespaces?'checked':''}> All namespaces</label>`:''}<span class="muted">Separate boxes do not imply peering. Export labels do not prove remote membership or reachability.</span></div><div class="am-map-viewport" id="am-map-viewport" tabindex="0" aria-label="Cluster map. Scroll to explore; use zoom controls or Ctrl plus wheel to zoom."><div id="am-map-space"><div id="am-map-world">${boxes.map(b=>b.html).join('')}</div></div></div><p class="am-map-legend">⇄ East-west transport / peer configuration · Green status is captured readiness, not a traffic test. ${S.cluster?'The canvas includes all captured contexts; the workspace filter still scopes other views.':''}</p></section>`;
}

function amWireClusterCanvas(){
  const A=S.ambient,viewport=$('#am-map-viewport');if(!viewport)return;
  const layout=A.mapLayouts[A.mapLayoutKey],world=$('#am-map-world'),space=$('#am-map-space');
  const resize=()=>{let width=700,height=450;world.querySelectorAll('.am-cluster-box').forEach(box=>{width=Math.max(width,box.offsetLeft+box.offsetWidth+24);height=Math.max(height,box.offsetTop+box.offsetHeight+24);});A.mapWorld={width,height};world.style.width=width+'px';world.style.height=height+'px';world.style.transform=`scale(${layout.zoom})`;space.style.width=width*layout.zoom+'px';space.style.height=height*layout.zoom+'px';$('#am-map-zoom').textContent=Math.round(layout.zoom*100)+'%';};
  const zoom=(value,anchorX=viewport.clientWidth/2,anchorY=viewport.clientHeight/2)=>{const x=(viewport.scrollLeft+anchorX)/layout.zoom,y=(viewport.scrollTop+anchorY)/layout.zoom;layout.zoom=Math.min(1.75,Math.max(.2,value));resize();viewport.scrollLeft=x*layout.zoom-anchorX;viewport.scrollTop=y*layout.zoom-anchorY;};
  $('#am-map-minus').onclick=()=>zoom(layout.zoom/1.2);$('#am-map-plus').onclick=()=>zoom(layout.zoom*1.2);
  $('#am-map-fit').onclick=()=>{zoom(Math.min(1,viewport.clientWidth/A.mapWorld.width,viewport.clientHeight/A.mapWorld.height),0,0);viewport.scrollTo(0,0);};
  $('#am-map-reset').onclick=()=>{delete A.mapLayouts[A.mapLayoutKey];render();};
  $('#am-map-all-namespaces')?.addEventListener('change',e=>{A.mapAllNamespaces=e.target.checked;render();});
  $('#am-map-search').onchange=e=>{A.mapSearch=e.target.value;render();};
  viewport.addEventListener('wheel',e=>{if(!e.ctrlKey&&!e.metaKey)return;e.preventDefault();const r=viewport.getBoundingClientRect();zoom(layout.zoom*(e.deltaY<0?1.1:1/1.1),e.clientX-r.left,e.clientY-r.top);},{passive:false});
  viewport.onpointerdown=e=>{if(e.button!==0||e.target.closest('.am-cluster-box'))return;viewport.setPointerCapture(e.pointerId);const x=e.clientX,y=e.clientY,left=viewport.scrollLeft,top=viewport.scrollTop;viewport.classList.add('am-panning');viewport.onpointermove=event=>{viewport.scrollLeft=left+x-event.clientX;viewport.scrollTop=top+y-event.clientY;};const finish=()=>{viewport.onpointermove=null;viewport.classList.remove('am-panning');};viewport.onpointerup=finish;viewport.onpointercancel=finish;};
  const move=(node,id,x,y)=>{const isCluster=node.classList.contains('am-cluster-box');x=Math.max(isCluster?16:12,x);y=Math.max(isCluster?16:110,y);if(!isCluster){x=Math.min(x,Math.max(12,node.parentElement.clientWidth-node.offsetWidth-12));node.parentElement.style.height=Math.max(node.parentElement.offsetHeight,y+node.offsetHeight+16)+'px';}node.style.left=x+'px';node.style.top=y+'px';layout.positions[id]={x,y};resize();};
  world.querySelectorAll('[data-map-drag]').forEach(handle=>{
    const node=handle.closest('[data-map-node]'),id=handle.dataset.mapDrag;
    handle.onpointerdown=e=>{if(e.button!==0)return;e.preventDefault();handle.setPointerCapture(e.pointerId);const x=node.offsetLeft,y=node.offsetTop,startX=e.clientX,startY=e.clientY;node.classList.add('am-dragging');handle.onpointermove=event=>move(node,id,x+(event.clientX-startX)/layout.zoom,y+(event.clientY-startY)/layout.zoom);const finish=()=>{node.classList.remove('am-dragging');handle.onpointermove=null;handle.onpointerup=null;handle.onpointercancel=null;};handle.onpointerup=finish;handle.onpointercancel=finish;};
    handle.onkeydown=e=>{const d={ArrowLeft:[-1,0],ArrowRight:[1,0],ArrowUp:[0,-1],ArrowDown:[0,1]}[e.key];if(!d)return;e.preventDefault();const step=e.shiftKey?40:10;move(node,id,node.offsetLeft+d[0]*step,node.offsetTop+d[1]*step);};
  });
  resize();
  viewport.scrollLeft=layout.scrollLeft||0;viewport.scrollTop=layout.scrollTop||0;
  viewport.onscroll=()=>{layout.scrollLeft=viewport.scrollLeft;layout.scrollTop=viewport.scrollTop;};
}
