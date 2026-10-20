'use strict';

const amMapKey=r=>JSON.stringify([r.kind==='Gateway'?'gateway':'service',r.cluster,r.namespace,r.name]);

// opts (Ambient v2, REVIEW R4): {inScope(svc)->bool, selected:['ns/name'], totals} scopes the canvas to the
// chosen namespaces/Services, collapses Services with nothing attached, draws waypoints as nodes with
// "uses waypoint" edges and lines the same Service up across clusters.
function amMapInteresting(s,selected){return !!(selected.includes(s.namespace+'/'+s.name)||s.waypoint||(s.policies||[]).length||s.routes||(s.globalHosts||[]).length||s.state==='fail');}
function amClusterCanvas(target,opts) {
  const A=S.ambient, clusters=A.inventory?.topology?.clusters||[];
  if(!clusters.length)return '';
  A.mapV2=!!opts;const v2=!!opts,selected=opts?.selected||[],inScope=opts?.inScope||(()=>true);
  const shared=new Set();
  const maxGw=Math.max(0,...clusters.map(c=>c.gateways.length));
  let drawnTotal=0,scopeTotal=0,allTotal=0;
  const namespace=target?.kind==='Namespace'?target.name:target?.namespace||'',scope=A.mapAllNamespaces?'':namespace;
  A.mapLayouts ||= {};
  A.mapLayoutKey=JSON.stringify([scope,clusters.map(c=>c.context),v2?'v2':'']);
  const layout=A.mapLayouts[A.mapLayoutKey] ||= {positions:{},zoom:1,expanded:{},views:{}};
  layout.expanded ||= {};layout.views ||= {};layout.moreNs ||= {};
  const keep=(c,s)=>inScope(s)&&(!s.infrastructure||selected.includes(s.namespace+'/'+s.name))&&(selected.length||amMapInteresting(s,selected)||layout.moreNs[c.context+'|'+s.namespace]);
  if(v2){const seen={};for(const c of clusters)for(const s of c.services){if(!keep(c,s))continue;const k=s.namespace+'/'+s.name;seen[k]=(seen[k]||0)+1;if(seen[k]>1)shared.add(k);}}
  let nextX=24;
  const boxes=clusters.map(c=>{
    const id=JSON.stringify(['cluster',c.context]),wide=!!layout.expanded[c.context],width=v2?(wide?1180:780):(wide?960:620);
    const view=layout.views[c.context]||'cards',list=view==='list',columns=wide?(v2?4:3):(v2?3:2);
    const p=layout.positions[id]||{x:nextX,y:24};nextX+=width+(v2?160:360);
    let services=c.services.filter(s=>!scope||s.namespace===scope).sort((a,b)=>Number(!!b.globalHosts?.length)-Number(!!a.globalHosts?.length));
    const more=[];
    if(v2){
      allTotal+=services.length;
      services=services.filter(s=>inScope(s)&&(!s.infrastructure||selected.includes(s.namespace+'/'+s.name)));
      scopeTotal+=services.length;
      const byNs={};
      services=services.filter(s=>{const k=c.context+'|'+s.namespace;if(selected.length||amMapInteresting(s,selected)||layout.moreNs[k])return true;(byNs[s.namespace]=byNs[s.namespace]||[]).push(s);return false;});
      for(const [ns,list] of Object.entries(byNs))more.push({ns,count:list.length,key:c.context+'|'+ns});
      const key=s=>s.namespace+'/'+s.name;
      services.sort((a,b)=>Number(shared.has(key(b)))-Number(shared.has(key(a)))||key(a).localeCompare(key(b)));
      drawnTotal+=services.length;
    }
    const items=[];
    for(const g of c.gateways)items.push({ref:g,gateway:true,html:`<div class="am-canvas-card-head"><span class="am-gateway-symbol" aria-hidden="true">⇄</span><strong>${g.role==='east-west'?'East-west gateway':'Remote peer'}</strong>${amState(g.state)}</div><button class="link" data-amevidence="${attr(g)}">${esc(g.namespace+'/'+g.name)}</button><small>${esc(g.class)} · ports ${g.ports.map(esc).join(', ')||'not captured'}</small><small>${esc(g.addresses.join(', ')||'Address not captured')}</small>${g.peer?`<div class="am-peer-label">Configured peer → ${esc(g.peerContext||g.peer)}${g.peerContext?'':' · context not established'}</div>`:''}<div class="row wrap"><button data-amevidence="${attr(g)}">View object</button><button data-amyaml="${attr(g)}">YAML</button></div>`});
    if(v2)for(const s of services)items.push({ref:s,html:`<div class="am-canvas-card-head"><span class="badge">${s.infrastructure?'Gateway Service':'Service'}</span><span class="${s.state==='pass'?'green':s.state==='fail'?'red':'muted'}">${esc(s.detail)}</span></div><strong title="${esc(s.namespace+'/'+s.name)}">${esc(s.namespace)}/${esc(s.name)}</strong><small>${s.waypoint?'Waypoint '+esc(s.waypoint.split('/').pop()):'No waypoint'} · ${s.routes} ${s.routes===1?'route':'routes'} · ${(s.policies||[]).length} ${(s.policies||[]).length===1?'policy':'policies'}${s.globalHosts?.length?' · global':''}</small><div class="row wrap"><button data-amyaml="${attr(s)}">YAML</button></div>`});
    else for(const s of services)items.push({ref:s,html:`<div class="am-canvas-card-head"><span class="badge">${s.infrastructure?'Gateway Service':'Service'}</span><span class="${s.state==='pass'?'green':s.state==='fail'?'red':'muted'}">${esc(s.detail)}</span></div><strong>${esc(s.name)}</strong><small>${esc(s.cluster)} · ${esc(s.namespace)}</small>${s.globalHosts?.length?`<small class="am-global-label" title="${esc(s.globalHosts.join(', '))}">Global: ${esc(s.globalHosts[0])}${s.globalHosts.length>1?' (+'+(s.globalHosts.length-1)+' more)':''}</small>`:s.scope?`<small class="am-global-label">Export scope: ${esc(s.scope)} · hostname not captured</small>`:''}${!list?`<small>${s.waypoint?'Waypoint: '+esc(s.waypoint):'No waypoint selected'} · ${s.routes} routes${s.takeover?' · local-name takeover':''}</small>`:''}${amMapPolicies(s)}<div class="row wrap"><button data-amrelated="${attr(s)}" data-aminvestigate>Investigate</button><button data-amconfigure="${attr(s)}">Configure</button><button data-amyaml="${attr(s)}">YAML</button></div>`});
    if(v2){
      const wps={};
      for(const sv of services)if(sv.waypoint){const [wns,wname]=sv.waypoint.includes('/')?sv.waypoint.split('/'):[sv.namespace,sv.waypoint];const k=wns+'/'+wname;(wps[k]=wps[k]||{ref:{kind:'Gateway',cluster:c.context,namespace:wns,name:wname,group:'gateway.networking.k8s.io',resource:'gateways'},svcs:[],policies:0}).svcs.push(sv);wps[k].policies+=(sv.policies||[]).length;}
      for(const w of Object.values(wps))items.push({ref:w.ref,waypoint:true,html:`<div class="am-canvas-card-head"><span class="badge">Waypoint</span></div><strong>${esc(w.ref.name)}</strong><small>${esc(w.ref.namespace)} · serves ${w.svcs.length} ${w.svcs.length===1?'Service':'Services'} here</small><small>${esc(w.svcs.slice(0,4).map(x=>x.name).join(', '))}${w.svcs.length>4?' +'+(w.svcs.length-4)+' more':''}</small><small>${w.policies} ${w.policies===1?'policy':'policies'} enforced on these Services</small><div class="row wrap"><button data-amevidence="${attr(w.ref)}">View object</button><button data-amyaml="${attr(w.ref)}">YAML</button></div>`});
      for(const m of more)items.push({ref:{kind:'More',cluster:c.context,namespace:m.ns,name:'more-'+m.ns},more:true,html:`<div class="am-canvas-card-head"><span class="badge">${m.count} Services</span></div><strong>${m.count} more ${m.count===1?'Service':'Services'} in ${esc(m.ns)}</strong><small>Nothing attached: no waypoint, policy, route or global name.</small><div class="row wrap"><button data-map-morens="${esc(m.key)}">Show them</button></div>`});
      c.__wps=wps;
    }
    let height=250,listY=144;
    const cardWidth=list?width-32:(width-32-(columns-1)*16)/columns;
    // Slots line the same Service up on every cluster: gateways, then Services found on several clusters in one shared order, then the rest.
    const sharedList=v2?[...shared].sort():[],gwRows=Math.ceil(maxGw/columns)*columns;let tail=gwRows+Math.ceil(sharedList.filter(k=>services.some(sv=>sv.namespace+'/'+sv.name===k)).length?sharedList.length/columns:0)*columns;
    const slotOf=(item,i)=>{if(!v2)return i;if(item.gateway)return c.gateways.indexOf(item.ref);const k=item.ref.namespace+'/'+item.ref.name;if(!item.waypoint&&!item.more&&shared.has(k))return gwRows+sharedList.indexOf(k);return tail++;};
    const rendered=items.map((item,i)=>{
      const nodeID=amMapKey(item.ref),positionID=view+(wide?'-wide':'')+'|'+nodeID,h=list?(item.gateway?192:240):v2?(item.gateway?212:150):(item.gateway?252:350),slot=slotOf(item,i);
      const pos=layout.positions[positionID]||{x:list?16:16+(slot%columns)*(cardWidth+16),y:list?listY:144+(v2&&slot>=gwRows?Math.ceil(maxGw/columns)*236+Math.floor((slot-gwRows)/columns)*174:Math.floor(slot/columns)*(v2?236:378))};listY+=h+24;
      const x=Math.min(pos.x,width-cardWidth-16),y=pos.y;height=Math.max(height,y+h+24);
      return `<article class="am-canvas-card ${list?'am-canvas-list':''} ${item.gateway?'am-canvas-gateway':''} ${item.gateway&&item.ref.role==='east-west'?'am-canvas-eastwest':''} ${item.waypoint?'am-canvas-waypoint':''} ${item.more?'am-canvas-more':''} ${target&&amKey(item.ref)===amKey(target)?'am-canvas-selected':''}" style="left:${x}px;top:${y}px;width:${cardWidth}px;height:${h}px" data-map-object="${attr(item.ref)}" data-map-node="${esc(nodeID)}"><button class="am-drag-handle" data-map-drag="${esc(positionID)}" aria-label="Move ${esc(item.ref.name)}" title="Drag to move; arrow keys also move this card">⠿</button>${item.html}</article>`;
    }).join('');
    return `<section class="am-cluster-box" style="left:${p.x}px;top:${p.y}px;width:${width}px;height:${height}px" data-map-node="${esc(id)}"><header><button class="am-cluster-handle" data-map-drag="${esc(id)}" aria-label="Move cluster ${esc(c.context)}" title="Drag cluster; arrow keys also move it"><img class="am-kubernetes-icon" src="static/vendor/kubernetes.svg" alt="Kubernetes"><span><strong>${esc(c.context)}</strong><small>${c.identity?'Mesh: '+esc(c.identity):'Mesh identity not captured'}${c.network?' · Network: '+esc(c.network):''}</small></span><span class="am-drag-dots" aria-hidden="true">⠿</span></button><div class="am-cluster-bar"><small>${services.length} Services${more.length?' · '+more.reduce((a,m)=>a+m.count,0)+' collapsed':''} · ${c.gateways.length} east-west/peer gateways</small><div class="row"><button data-map-view="${esc(c.context)}" data-view-mode="cards" aria-pressed="${!list}">Cards</button><button data-map-view="${esc(c.context)}" data-view-mode="list" aria-pressed="${list}">List</button><button data-map-expand="${esc(c.context)}" aria-expanded="${wide}">${wide?'Compact':'Expand'}</button></div></div></header>${rendered}${!items.length?'<p class="am-canvas-empty">No resources captured in this scope. Missing data does not prove absence.</p>':''}</section>`;
  });
  const links=A.inventory.topology.links||[];
  if(v2){const wl=[];for(const c of clusters)for(const w of Object.values(c.__wps||{}))for(const sv of w.svcs)wl.push({id:'wp:'+c.context+':'+w.ref.namespace+'/'+w.ref.name+':'+sv.name,kind:'waypoint',label:sv.name+' uses waypoint '+w.ref.name,source:w.ref,target:{kind:'Service',cluster:c.context,namespace:sv.namespace,name:sv.name}});A.mapExtraLinks=wl;}else A.mapExtraLinks=[];
  const legend=links.map(link=>`<button class="am-connection-chip ${link.kind==='peering'?'am-peer-chip':'am-global-chip'}" data-map-link="${esc(link.id)}" aria-pressed="${A.mapSelectedLink===link.id}" title="Highlight the captured relationship and its resource endpoints">${link.kind==='peering'?'⇄':'◇'} ${esc(link.label)}</button>`).join('');
  return `<section class="panel am-topology ${A.mapFullscreen?'am-map-fullscreen':''}"><div class="panel-head"><div><h2>Cluster attachment map</h2><p class="muted">${clusters.length} contexts${scope?' · namespace '+esc(scope):' · all captured namespaces'} · drag boxes and resource handles to arrange</p></div><div class="row wrap am-map-controls"><button id="am-map-minus" aria-label="Zoom out">−</button><output id="am-map-zoom">${Math.round(layout.zoom*100)}%</output><button id="am-map-plus" aria-label="Zoom in">+</button><button id="am-map-fit">Fit</button><button id="am-map-reset">Reset layout</button><button id="am-map-fullscreen">${A.mapFullscreen?'Exit full screen':'Full screen'}</button></div></div><div class="am-map-tools">${v2?`<input id="am-map-q" class="am2-input" style="width:240px" placeholder="Find a Service on the map…" value="${esc(A.mapQuery||'')}" aria-label="Find on the map"><span class="muted">Showing ${drawnTotal} of ${allTotal} Services${scopeTotal>drawnTotal?` (${scopeTotal-drawnTotal} with nothing attached are collapsed)`:''}. Change the namespaces or pick Services at the top right.</span>`:'<button id="am-map-search-top">Search objects · ⌘K</button>'}${namespace?`<label class="am-map-scope"><input id="am-map-all-namespaces" type="checkbox" ${A.mapAllNamespaces?'checked':''}> All namespaces</label>`:''}${v2?'':'<span class="muted">Use the top search to focus a resource. Click a connection below to highlight it.</span>'}</div>${legend?`<div class="am-map-connections">${legend}</div>`:''}<div class="am-map-viewport" id="am-map-viewport" tabindex="0" aria-label="Cluster map. Drag empty space to pan; use zoom controls or Ctrl plus wheel to zoom."><div id="am-map-space"><div id="am-map-world"><svg id="am-map-lines" xmlns="http://www.w3.org/2000/svg" aria-label="Captured configuration relationships"></svg>${boxes.join('')}</div></div></div><p class="am-map-legend"><span id="am-map-line-status"></span> Teal: peer configuration. Purple dashed: global Service publication. Lines are captured configuration, not a live traffic trace. ${S.cluster?'All captured contexts are shown.':''}</p></section>`;
}

function amWireClusterCanvas(){
  const A=S.ambient,viewport=$('#am-map-viewport');if(!viewport)return;
  document.querySelectorAll('.am-canvas-card[data-map-object]').forEach(card=>{const ref=JSON.parse(card.dataset.mapObject);if(ref.kind!=='Service'||A.mapV2)return;const svc=A.inventory.topology.clusters.find(c=>c.context===ref.cluster)?.services.find(s=>s.namespace===ref.namespace&&s.name===ref.name);if(!svc?.dns)return;card.querySelector('.am-global-label')?.remove();card.querySelector('.row.wrap')?.insertAdjacentHTML('beforebegin',`<div class="am-card-dns"><small title="${esc(svc.dns.domainSource)}">Cluster DNS${svc.dns.domainObserved?'':' · default suffix'}</small><code title="${esc(svc.dns.localName||svc.dns.domainSource)}">${esc(svc.dns.localName||'Not established')}</code><small>Global mesh DNS</small><code title="${esc((svc.dns.globalNames||[]).join(', '))}">${esc(svc.dns.globalNames?.join(', ')||'No published name captured')}</code></div>`);});
  const title=document.querySelector('.am-topology>.panel-head h2'),subtitle=document.querySelector('.am-topology>.panel-head p');
  if(title){title.textContent='Connections';if(subtitle)title.title=subtitle.textContent;}subtitle?.remove();
  A.mapObserver?.disconnect();
  const layout=A.mapLayouts[A.mapLayoutKey],world=$('#am-map-world'),space=$('#am-map-space');
  const resize=()=>{
    let width=700,height=450;
    world.querySelectorAll('.am-cluster-box').forEach(box=>{width=Math.max(width,box.offsetLeft+box.offsetWidth+32);height=Math.max(height,box.offsetTop+box.offsetHeight+32);});
    A.mapWorld={width,height};world.style.width=width+'px';world.style.height=height+'px';world.style.transform=`scale(${layout.zoom})`;space.style.width=width*layout.zoom+'px';space.style.height=height*layout.zoom+'px';$('#am-map-zoom').textContent=Math.round(layout.zoom*100)+'%';
    amDrawMapLinks(world,layout.zoom,(A.inventory.topology.links||[]).concat(A.mapExtraLinks||[]),A.mapSelectedLink);
  };
  const zoom=(value,anchorX=viewport.clientWidth/2,anchorY=viewport.clientHeight/2)=>{const x=(viewport.scrollLeft+anchorX)/layout.zoom,y=(viewport.scrollTop+anchorY)/layout.zoom;layout.zoom=Math.min(1.75,Math.max(.2,value));resize();viewport.scrollLeft=x*layout.zoom-anchorX;viewport.scrollTop=y*layout.zoom-anchorY;};
  $('#am-map-minus').onclick=()=>zoom(layout.zoom/1.2);$('#am-map-plus').onclick=()=>zoom(layout.zoom*1.2);
  $('#am-map-fit').onclick=()=>{zoom(Math.min(1,viewport.clientWidth/A.mapWorld.width,viewport.clientHeight/A.mapWorld.height),0,0);viewport.scrollTo(0,0);};
  $('#am-map-reset').onclick=()=>{delete A.mapLayouts[A.mapLayoutKey];A.mapSelectedLink='';render();};
  $('#am-map-fullscreen').onclick=()=>{A.mapFullscreen=!A.mapFullscreen;render();};
  if($('#am-map-search-top'))$('#am-map-search-top').onclick=openSearch;
  document.querySelectorAll('[data-map-morens]').forEach(b=>b.onclick=()=>{layout.moreNs[b.dataset.mapMorens]=true;render();});
  const mq=$('#am-map-q');
  const applyQuery=(scroll)=>{const q=(A.mapQuery||'').trim().toLowerCase();let first=null;world.querySelectorAll('.am-canvas-card[data-map-object]').forEach(card=>{const r=JSON.parse(card.dataset.mapObject),hit=!q||(r.namespace+'/'+r.name).toLowerCase().includes(q);card.classList.toggle('am-map-dim',!hit);card.classList.toggle('am-map-hit',!!q&&hit);if(q&&hit&&!first)first=card;});if(scroll&&first){const box=first.closest('.am-cluster-box');viewport.scrollLeft=(box.offsetLeft+first.offsetLeft+first.offsetWidth/2)*layout.zoom-viewport.clientWidth/2;viewport.scrollTop=(box.offsetTop+first.offsetTop)*layout.zoom-80;}};
  if(mq){mq.oninput=()=>{A.mapQuery=mq.value;applyQuery(true);};applyQuery(false);}
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
  const sizeViewport=()=>{if(!viewport.isConnected)return;if(!A.mapFullscreen){const room=Math.max(360,Math.floor(innerHeight-viewport.getBoundingClientRect().top-scrollY-45));if(viewport.style.height!==room+'px')viewport.style.height=room+'px';viewport.style.maxHeight='none';}resize();};
  A.mapObserver=new ResizeObserver(sizeViewport);A.mapObserver.observe(viewport);
  if(A.mapResize)window.removeEventListener('resize',A.mapResize);A.mapResize=sizeViewport;window.addEventListener('resize',A.mapResize);requestAnimationFrame(sizeViewport);
}

// Policies that reach a Service, as links that open each one in the object
// drawer. A red mark means accepted but not enforced by this waypoint.
function amMapPolicies(s){
  const ps=s.policies||[];
  if(!ps.length)return '<small class="muted am-map-policies">No security policies captured</small>';
  const shown=ps.slice(0,3);
  return `<div class="am-map-policies"><small>Policies</small>${shown.map(p=>`<button class="am-policy-link ${p.warning?'am-policy-warn':''}" data-amevidence="${attr(p.ref)}" title="${esc(p.ref.kind+' '+p.ref.namespace+'/'+p.ref.name+' · '+p.reason+(p.warning?' · '+p.warning:''))}">${p.action?`<b class="${p.action==='DENY'?'red':p.action==='ALLOW'?'green':''}">${esc(p.action)}</b> `:''}${esc(p.ref.name)}${p.warning?' ⚠':''}</button>`).join('')}${ps.length>3?`<button class="link" data-amconfigure="${attr(s)}" title="See every policy on Configure">+${ps.length-3} more</button>`:''}</div>`;
}
