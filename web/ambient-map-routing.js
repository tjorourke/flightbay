'use strict';

// Orthogonal routing around resource rectangles. No connector is drawn when
// the current layout cannot provide a clear route.
function amSegmentBlocked(a,b,rectangles){
  return rectangles.some(r=>a.x===b.x?a.x>r.x&&a.x<r.x+r.w&&Math.max(a.y,b.y)>r.y&&Math.min(a.y,b.y)<r.y+r.h:a.y>r.y&&a.y<r.y+r.h&&Math.max(a.x,b.x)>r.x&&Math.min(a.x,b.x)<r.x+r.w);
}

function amRoute(a,b,obstacles){
  if(a.x<b.x+b.w&&a.x+a.w>b.x&&a.y<b.y+b.h&&a.y+a.h>b.y)return null;
  const ac={x:a.x+a.w/2,y:a.y+a.h/2},bc={x:b.x+b.w/2,y:b.y+b.h/2};
  const preferred=Math.abs(ac.x-bc.x)>=Math.abs(ac.y-bc.y)?(bc.x>=ac.x?['right','left']:['left','right']):(bc.y>=ac.y?['bottom','top']:['top','bottom']);
  const pairs=[preferred,['bottom','bottom'],['top','top'],['right','left'],['left','right'],['bottom','top'],['top','bottom']];
  let best=null,bestCost=Infinity;const tried=new Set();
  for(const pair of pairs){const key=pair.join('/');if(tried.has(key))continue;tried.add(key);const path=amRoutePorts(a,b,obstacles,...pair);if(!path)continue;const length=path.slice(1).reduce((sum,p,i)=>sum+Math.abs(p.x-path[i].x)+Math.abs(p.y-path[i].y),0),cost=length+Math.max(0,path.length-2)*28;if(cost<bestCost){best=path;bestCost=cost;}if(path.length===2)break;}
  return best;
}

function amRoutePorts(a,b,obstacles,from,to){
  const port=(r,side,d)=>{const p={left:{x:r.x-d,y:r.y+r.h/2},right:{x:r.x+r.w+d,y:r.y+r.h/2},top:{x:r.x+r.w/2,y:r.y-d},bottom:{x:r.x+r.w/2,y:r.y+r.h+d}}[side];return{x:Math.round(p.x*100)/100,y:Math.round(p.y*100)/100};};
  const source=port(a,from,6),target=port(b,to,6),start=port(a,from,18),end=port(b,to,18);
  const same=(r,s)=>Math.abs(r.x-s.x)<.02&&Math.abs(r.y-s.y)<.02&&Math.abs(r.w-s.w)<.02&&Math.abs(r.h-s.h)<.02;
  if(amSegmentBlocked(source,start,obstacles.filter(r=>!same(r,a)))||amSegmentBlocked(end,target,obstacles.filter(r=>!same(r,b))))return null;
  const padded=obstacles.map(r=>({x:r.x-8,y:r.y-8,w:r.w+16,h:r.h+16}));
  if([start,end].some(p=>padded.some(r=>p.x>r.x&&p.x<r.x+r.w&&p.y>r.y&&p.y<r.y+r.h)))return null;
  const unique=values=>[...new Set(values.map(n=>Math.round(n*100)/100))].sort((x,y)=>x-y);
  const xs=unique([start.x,end.x,0,...obstacles.flatMap(r=>[r.x-14,r.x+r.w+14])]);
  const ys=unique([start.y,end.y,0,...obstacles.flatMap(r=>[r.y-14,r.y+r.h+14])]);
  if(xs.length*ys.length>70000)return null;
  const key=p=>ys.indexOf(Math.round(p.y*100)/100)*xs.length+xs.indexOf(Math.round(p.x*100)/100);
  const point=i=>({x:xs[i%xs.length],y:ys[Math.floor(i/xs.length)]});
  const first=key(start),last=key(end),distance=new Float64Array(xs.length*ys.length).fill(Infinity),parent=new Int32Array(distance.length).fill(-1);
  distance[first]=0;
  const heap=[];
  const less=(a,b)=>a.f<b.f||(a.f===b.f&&a.g>b.g);
  const push=value=>{heap.push(value);let i=heap.length-1;while(i){const p=(i-1)>>1;if(!less(heap[i],heap[p]))break;[heap[i],heap[p]]=[heap[p],heap[i]];i=p;}};
  const pop=()=>{const value=heap[0],tail=heap.pop();if(heap.length){heap[0]=tail;let i=0;while(true){let next=i,l=i*2+1,r=l+1;if(l<heap.length&&less(heap[l],heap[next]))next=l;if(r<heap.length&&less(heap[r],heap[next]))next=r;if(next===i)break;[heap[i],heap[next]]=[heap[next],heap[i]];i=next;}}return value;};
  push({i:first,g:0,f:0});
  while(heap.length){
    const current=pop();if(current.g!==distance[current.i])continue;
    if(current.i===last){const points=[];for(let i=last;i!==-1;i=parent[i])points.push(point(i));points.reverse();const result=[source,...points,target];return result.filter((p,i)=>i===0||i===result.length-1||!((result[i-1].x===p.x&&p.x===result[i+1].x)||(result[i-1].y===p.y&&p.y===result[i+1].y)));}
    const p=point(current.i),x=current.i%xs.length,y=Math.floor(current.i/xs.length),neighbors=[];
    if(x>0)neighbors.push(current.i-1);if(x+1<xs.length)neighbors.push(current.i+1);if(y>0)neighbors.push(current.i-xs.length);if(y+1<ys.length)neighbors.push(current.i+xs.length);
    for(const index of neighbors){const next=point(index);if(amSegmentBlocked(p,next,padded))continue;const cost=current.g+Math.abs(p.x-next.x)+Math.abs(p.y-next.y);if(cost>=distance[index])continue;distance[index]=cost;parent[index]=current.i;push({i:index,g:cost,f:cost+Math.abs(next.x-end.x)+Math.abs(next.y-end.y)});}
  }
  return null;
}

// A relationship is never dropped because two cards overlap or no clear lane exists.
// Offset elbow between the two cards; it may cross other cards, which beats hiding it.
function amFallbackRoute(a,b){
  const ac={x:a.x+a.w/2,y:a.y+a.h/2},bc={x:b.x+b.w/2,y:b.y+b.h/2};
  if(a.x<b.x+b.w&&a.x+a.w>b.x&&a.y<b.y+b.h&&a.y+a.h>b.y){const y=Math.min(a.y,b.y)-14;return [{x:ac.x,y:a.y-6},{x:ac.x,y},{x:bc.x,y},{x:bc.x,y:b.y-6}].filter((p,i,l)=>i===0||p.x!==l[i-1].x||p.y!==l[i-1].y);}
  const horizontal=Math.abs(ac.x-bc.x)>=Math.abs(ac.y-bc.y);
  if(horizontal){const sx=bc.x>=ac.x?a.x+a.w+6:a.x-6,ex=bc.x>=ac.x?b.x-6:b.x+b.w+6,mx=(sx+ex)/2;return [{x:sx,y:ac.y},{x:mx,y:ac.y},{x:mx,y:bc.y},{x:ex,y:bc.y}];}
  const sy=bc.y>=ac.y?a.y+a.h+6:a.y-6,ey=bc.y>=ac.y?b.y-6:b.y+b.h+6,my=(sy+ey)/2;return [{x:ac.x,y:sy},{x:ac.x,y:my},{x:bc.x,y:my},{x:bc.x,y:ey}];
}
function amRoundedRoute(points){
  let d=`M${points[0].x} ${points[0].y}`;
  for(let i=1;i<points.length-1;i++){const prev=points[i-1],p=points[i],next=points[i+1],r=Math.min(6,(Math.abs(p.x-prev.x)+Math.abs(p.y-prev.y))/2,(Math.abs(next.x-p.x)+Math.abs(next.y-p.y))/2),entry={x:p.x-Math.sign(p.x-prev.x)*r,y:p.y-Math.sign(p.y-prev.y)*r},exit={x:p.x+Math.sign(next.x-p.x)*r,y:p.y+Math.sign(next.y-p.y)*r};d+=` L${entry.x} ${entry.y} Q${p.x} ${p.y} ${exit.x} ${exit.y}`;}
  const last=points[points.length-1];return d+` L${last.x} ${last.y}`;
}

function amDrawMapLinks(world,zoom,links,selected){
  const svg=world.querySelector('#am-map-lines');if(!svg)return;
  const ns='http://www.w3.org/2000/svg',wr=world.getBoundingClientRect();
  const rect=e=>{const r=e.getBoundingClientRect();return {x:(r.left-wr.left)/zoom,y:(r.top-wr.top)/zoom,w:r.width/zoom,h:r.height/zoom};};
  const nodes=new Map([...world.querySelectorAll('[data-map-object]')].map(e=>[amMapKey(JSON.parse(e.dataset.mapObject)),e]));
  const obstacles=[...nodes.values(),...world.querySelectorAll('.am-cluster-box>header')].map(rect);
  svg.replaceChildren();svg.setAttribute('viewBox',`0 0 ${world.offsetWidth} ${world.offsetHeight}`);
  const defs=document.createElementNS(ns,'defs'),marker=document.createElementNS(ns,'marker');marker.id='am-peer-arrow';marker.setAttribute('viewBox','0 0 10 10');marker.setAttribute('refX','9');marker.setAttribute('refY','5');marker.setAttribute('markerUnits','userSpaceOnUse');marker.setAttribute('markerWidth','10');marker.setAttribute('markerHeight','10');marker.setAttribute('orient','auto');const triangle=document.createElementNS(ns,'path');triangle.setAttribute('d','M1 1L9 5L1 9Z');triangle.setAttribute('fill','#138b9d');marker.appendChild(triangle);defs.appendChild(marker);svg.appendChild(defs);
  let drawn=0,hidden=0,blocked=0;
  nodes.forEach(e=>e.classList.remove('am-link-endpoint'));
  for(const link of links){
    const source=nodes.get(amMapKey(link.source)),target=nodes.get(amMapKey(link.target));if(!source||!target){hidden++;continue;}
    const route=amRoute(rect(source),rect(target),obstacles)||amFallbackRoute(rect(source),rect(target));if(!route){blocked++;continue;}
    const path=document.createElementNS(ns,'path');path.setAttribute('d',amRoundedRoute(route));path.setAttribute('fill','none');path.setAttribute('stroke',link.kind==='peering'?'#138b9d':'#9463db');path.setAttribute('stroke-width',selected===link.id?'3':'2');path.setAttribute('stroke-linecap','round');path.setAttribute('stroke-linejoin','round');path.setAttribute('class','am-map-line');path.dataset.linkId=link.id;
    if(link.kind==='peering')path.setAttribute('marker-end','url(#am-peer-arrow)');else path.setAttribute('stroke-dasharray','7 5');
    if(selected&&selected!==link.id)path.setAttribute('opacity','.2');
    const title=document.createElementNS(ns,'title');title.textContent=link.label+' · captured configuration';path.appendChild(title);svg.appendChild(path);drawn++;
    const origin=document.createElementNS(ns,'circle');origin.setAttribute('cx',route[0].x);origin.setAttribute('cy',route[0].y);origin.setAttribute('r','2.5');origin.setAttribute('fill',link.kind==='peering'?'#138b9d':'#9463db');if(selected&&selected!==link.id)origin.setAttribute('opacity','.2');svg.appendChild(origin);
    if(selected===link.id){source.classList.add('am-link-endpoint');target.classList.add('am-link-endpoint');}
  }
  const status=document.querySelector('#am-map-line-status');if(status)status.textContent=`${drawn} connections drawn.${hidden?' '+hidden+' outside this namespace scope.':''}${blocked?' '+blocked+' blocked by this layout; move boxes/cards apart.':''}`;
}
