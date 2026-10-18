'use strict';

// Grid-aligned serpentine path: connectors occupy only the gaps between nodes.
function rpMapGeometry(count,columns){const width=244,height=112,gapX=64,gapY=64,margin=30,top=42;columns=Math.max(1,Math.min(columns,count||1));return {width:columns*width+(columns-1)*gapX+margin*2,height:Math.ceil(count/columns)*(height+gapY)-gapY+top+margin,nodes:Array.from({length:count},(_,i)=>{const row=Math.floor(i/columns),column=row%2?columns-1-i%columns:i%columns;return {x:margin+column*(width+gapX),y:top+row*(height+gapY),width,height};})};}
function rpTransportLabel(from,to){
  if(from.state==='unknown'||to.state==='unknown')return 'unknown';
  if(from.id==='caller'&&to.id==='source-ztunnel')return 'capture';
  if(to.id==='backend')return 'TCP';
  const proxies=['source-ztunnel','source-waypoint','waypoint','eastwest','destination-ztunnel'];
  return proxies.includes(from.id)&&proxies.includes(to.id)?'HBONE':'';
}
function rpMapPanel(){
  const p=RP.result.path,available=$('#main')?.clientWidth||innerWidth,columns=available<480?1:available<760?2:available<1050?3:4,g=rpMapGeometry(p.hops.length,columns),fit=(s,n=31)=>String(s||'').length>n?String(s).slice(0,n-1)+'…':String(s||'');
  RP.mapKey=JSON.stringify(p.request)+'/'+columns;
  const arrows=g.nodes.slice(0,-1).map((a,i)=>{
    const b=g.nodes[i+1],from=p.hops[i],to=p.hops[i+1],down=b.y>a.y,right=b.x>a.x,sx=down?a.x+a.width/2:right?a.x+a.width+4:a.x-4,sy=down?a.y+a.height+4:a.y+a.height/2,tx=down?b.x+b.width/2:right?b.x-10:b.x+b.width+10,ty=down?b.y-10:b.y+b.height/2,fail=from.state==='fail'||to.state==='fail',eastwest=rpIsEastWest(from)||rpIsEastWest(to),label=rpTransportLabel(from,to),marker=fail?'fail':eastwest?'eastwest':'normal';
    return `<g class="rg-edge rp-map-edge ${fail?'rp-map-edge-fail':''} ${eastwest?'rp-map-edge-eastwest':''}" data-f="${from.id}" data-t="${to.id}"><path d="M ${sx} ${sy} L ${tx} ${ty}" marker-end="url(#rp-arrow-${marker})"/>${label?`<text class="rp-transport-label" x="${(sx+tx)/2+(down?10:0)}" y="${(sy+ty)/2-(down?0:9)}" text-anchor="${down?'start':'middle'}">${esc(label)}</text>`:''}</g>`;
  }).join('');
  const nodes=p.hops.map((h,i)=>{
    const n=g.nodes[i],ref=h.refs.find(r=>r.kind==='Pod')||h.refs[0],subtitle=ref?`${ref.kind} ${ref.namespace?ref.namespace+'/':''}${ref.name}`:'No specific resource established';
    return `<g class="rg-node rp-map-node rp-map-${h.state} ${rpIsEastWest(h)?'rp-map-role-eastwest':''} ${h.id===RP.hop?'rg-sel':''}" data-rgid="${h.id}" transform="translate(${n.x},${n.y})" role="button" tabindex="0" aria-label="${esc(h.title+' · '+h.cluster+' · '+h.state)}"><title>${esc(h.title+'\n'+h.cluster+'\n'+subtitle+'\n'+h.detail)}</title><rect width="${n.width}" height="${n.height}" rx="9"/><circle cx="23" cy="25" r="12"/><text x="23" y="29" class="rp-map-index" text-anchor="middle">${i+1}</text><text x="44" y="29" class="rp-map-title">${esc(fit(h.title,25))}</text><text x="15" y="52" class="rp-map-cluster">${esc(fit(h.cluster,32))}</text><text x="15" y="74" class="rp-map-resource">${esc(fit(subtitle,33))}</text><text x="15" y="97" class="rp-map-state">${esc({pass:'Configured',fail:'Needs attention',unknown:'Not established',warning:'Check scope',na:'Not selected'}[h.state]||h.state)}</text></g>`;
  }).join('');
  const marker=name=>`<marker id="rp-arrow-${name}" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="8" markerHeight="8" orient="auto"><path d="M 0 0 L 10 5 L 0 10 z" class="rp-map-arrow ${name==='fail'?'rp-map-arrow-fail':name==='eastwest'?'rp-map-arrow-eastwest':''}"/></marker>`;
  const svg=`<svg class="rg rp-map-svg" id="rg-svg" data-w="${g.width}" data-h="${g.height}" width="100%" height="100%" role="img" aria-label="Configured dataplane request path"><defs>${marker('normal')}${marker('fail')}${marker('eastwest')}</defs><g id="rg-root"><text x="30" y="23" class="rp-map-heading">Configured dataplane path</text>${arrows}${nodes}</g></svg>`;
  return fbGraphPanel('Request path map',RP.graph,svg,`<small>${p.hops.length} dataplane hops</small>`,'<span class="rp-map-legend-line"></span><strong>Dashed links = configured path, not observed traversal.</strong><span class="rp-ew-legend-mark"></span><strong>Amber = east-west gateway / cross-network HBONE role, not a health verdict.</strong>');
}
function rpSelectHop(id){if(!RP.result.path.hops.some(h=>h.id===id))return;RP.hop=id;RP.evidence='configuration';document.querySelectorAll('[data-rphop]').forEach(el=>{const on=el.dataset.rphop===id;el.classList.toggle('selected',on);el.setAttribute('aria-pressed',String(on));});document.querySelectorAll('.rp-map-node').forEach(el=>el.classList.toggle('rg-sel',el.dataset.rgid===id));document.querySelectorAll('[data-rpevidence]').forEach(el=>el.setAttribute('aria-selected',String(el.dataset.rpevidence==='configuration')));$('#rp-inspector-body').innerHTML=rpInspector();rpWireInspector();}
function rpMapWire(){if(RP.view!=='map'||!$('#rg-svg'))return;const fresh=!RP.graph.gz||RP.graph.gz.key!==RP.mapKey;fbGraphWire(RP.graph,RP.mapKey,rpSelectHop);document.querySelectorAll('.rp-map-node').forEach(g=>g.onkeydown=e=>{if(e.key==='Enter'||e.key===' '){e.preventDefault();rpSelectHop(g.dataset.rgid);}});if(innerWidth<650&&fresh){const k=Math.min(.9,($('#rg-vp').clientWidth-20)/Number($('#rg-svg').dataset.w));RP.graph.gz={key:RP.mapKey,k,x:10,y:10};$('#rg-root').setAttribute('transform',`translate(10,10) scale(${k})`);$('#rg-pct').textContent=Math.round(k*100)+'%';}}
