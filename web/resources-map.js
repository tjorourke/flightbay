// Resources map. One namespace drawn as lanes of cards: policies, gateways,
// routes, Services, Pods, workloads and the config they reference, with the
// captured relationships drawn between them. The panel, controls, chips and
// card anatomy are the ones the Ambient attachment map uses, so the two maps
// read the same way; the lane layout replaces the Ambient free-form canvas
// because a namespace can hold a few hundred objects.
(function(){
  const esc = window.esc || (s => String(s ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c])));
  const attrOf = window.attr || (o => esc(JSON.stringify(o)));
  const LANES = [
    ['Policies', ['AuthorizationPolicy', 'PeerAuthentication', 'RequestAuthentication', 'NetworkPolicy', 'AgentgatewayPolicy', 'EnterpriseAgentgatewayPolicy', 'DestinationRule', 'Telemetry', 'WasmPlugin', 'Sidecar', 'BackendTLSPolicy', 'HorizontalPodAutoscaler', 'PodDisruptionBudget']],
    ['Gateways & ingress', ['Gateway', 'Ingress']],
    ['Routes', ['HTTPRoute', 'GRPCRoute', 'TCPRoute', 'TLSRoute']],
    ['Services & backends', ['Service', 'ServiceEntry', 'AgentgatewayBackend', 'EnterpriseAgentgatewayBackend']],
    ['Pods & endpoints', ['Pod', 'WorkloadEntry']],
    ['Workloads', ['Deployment', 'StatefulSet', 'DaemonSet', 'Job', 'CronJob']],
    ['Agents & tools', ['Agent', 'RemoteMCPServer', 'MCPServer', 'ToolServer', 'ModelConfig']],
    ['Config & identity', ['ServiceAccount', 'RoleBinding', 'Role', 'ClusterRole', 'ConfigMap', 'Secret', 'PersistentVolumeClaim']],
  ];
  // Plumbing the map bridges over rather than draws: Deployment → Pod skips
  // the ReplicaSet, Service → Pod skips the EndpointSlice. Never offered.
  const HIDDEN_KINDS = ['ReplicaSet', 'EndpointSlice', 'Endpoints', 'ControllerRevision', 'Event', 'Lease'];
  const DEFAULT_KINDS = ['Deployment', 'StatefulSet', 'DaemonSet', 'Job', 'CronJob', 'Pod', 'WorkloadEntry', 'Service', 'ServiceEntry', 'AgentgatewayBackend', 'EnterpriseAgentgatewayBackend', 'Ingress', 'Gateway', 'HTTPRoute', 'GRPCRoute', 'TCPRoute', 'TLSRoute', 'ServiceAccount', 'Agent', 'RemoteMCPServer', 'MCPServer', 'ModelConfig'].concat(LANES[0][1]);
  // Edge kinds the server emits, with the chip label and the verbs a card uses
  // to describe its own links.
  const EDGE_KINDS = {
    parent: {label: 'Gateway → attached route', out: 'Parent of', in: 'Attached to'},
    backend: {label: 'Route or Ingress → backend', out: 'Routes to', in: 'Backend of'},
    endpoints: {label: 'Service or ServiceEntry → Pod', out: 'Selects', in: 'Selected by'},
    owner: {label: 'Owner → owned object', out: 'Owns', in: 'Owned by'},
    uses: {label: 'Uses config, identity or tools', out: 'Uses', in: 'Used by'},
    waypoint: {label: 'use-waypoint label → Gateway', out: 'Uses waypoint', in: 'Waypoint for'},
    policy: {label: 'Policy → target', out: 'Applies to', in: 'Governed by'},
    scales: {label: 'Autoscaler → workload', out: 'Scales', in: 'Scaled by'},
    grants: {label: 'RoleBinding → Role, ServiceAccount', out: 'Binds', in: 'Bound by'},
    global: {label: 'Global Service → ServiceEntry, WorkloadEntry', out: 'Published as', in: 'Published for'},
    selector: {label: 'Selector', out: 'Selects', in: 'Selected by'},
  };
  const TEAL = '#138b9d', RED = '#b42318';
  let graph = null, loading = '';
  let observer = null, onResize = null;

  // Map state lives on S.res so it survives re-renders and tab switches.
  function M(){
    S.res = S.res || {};
    return (S.res.map = S.res.map || {zoom: 1, view: '', kinds: {}, selected: '', fullscreen: false, unlinked: true, q: '', ns: '', cluster: '', sel: [], depth: 1});
  }
  // Focus mode hides only the plumbing kinds the map bridges over, so picking
  // a Job shows its Pods, ServiceAccount and ConfigMaps without touching Kinds.
  const DEPTHS = [[1, '1 hop'], [2, '2 hops'], [99, 'All linked']];

  // The selection is shareable: #resources?tab=map&cluster=…&ns=…&sel=id,id&depth=2.
  // Object names cannot hold commas, so a comma-joined list of ids is safe.
  function rmHashParams(){
    const h = location.hash.slice(1), q = h.indexOf('?');
    return new URLSearchParams(q >= 0 ? h.slice(q + 1) : '');
  }
  function rmApplyHash(){
    const m = M(), h = location.hash;
    if (m.hashSeen === h) return false;
    m.hashSeen = h;
    const p = rmHashParams();
    if (p.get('tab') !== 'map') return false;
    if (p.has('cluster') && p.get('cluster') !== rmCluster()) {
      m.cluster = p.get('cluster'); graph = null;
      if (S.cluster && S.cluster !== m.cluster && typeof setCluster === 'function') setCluster(m.cluster);
    }
    if (p.has('ns')) { if (p.get('ns') !== m.ns && !p.has('sel')) m.sel = []; m.ns = p.get('ns'); m.fitted = false; }
    if (p.has('sel')) { m.sel = p.get('sel').split(',').filter(Boolean); m.fitted = false; }
    if (p.has('depth')) m.depth = +p.get('depth') || 1;
    return true;
  }
  function rmWriteHash(){
    const m = M(), p = new URLSearchParams({tab: 'map'});
    const c = rmCluster();
    if (c) p.set('cluster', c);
    if (m.ns) p.set('ns', m.ns);
    if (m.sel.length) { p.set('sel', m.sel.join(',')); if (m.depth !== 1) p.set('depth', String(m.depth)); }
    const h = '#resources?' + p.toString().replace(/%2F/gi, '/').replace(/%2C/gi, ',');
    if (location.hash !== h) history.replaceState(null, '', h);
    m.hashSeen = location.hash;
  }
  // The selected objects plus everything linked to them within `depth` hops.
  // Hidden kinds cost nothing to cross, so a Deployment one hop out still
  // reaches its Pods through an unticked ReplicaSet.
  function rmFocus(d, nsIds, sel, depth, shown){
    const adj = {};
    const link = (a, b) => { (adj[a] = adj[a] || []).push(b); };
    d.edges.forEach(e => { if (e.to && nsIds.has(e.from) && nsIds.has(e.to)) { link(e.from, e.to); link(e.to, e.from); } });
    const dist = new Map(), queue = [];
    sel.forEach(id => { if (nsIds.has(id)) { dist.set(id, 0); queue.push(id); } });
    while (queue.length) {
      const id = queue.shift(), base = dist.get(id);
      (adj[id] || []).forEach(next => {
        const n = d.byId[next];
        const cost = n && shown(n) ? 1 : 0;
        const nd = base + cost;
        if (nd > depth || (dist.has(next) && dist.get(next) <= nd)) return;
        dist.set(next, nd);
        if (cost) queue.push(next); else queue.unshift(next);
      });
    }
    return new Set(dist.keys());
  }
  function rmCluster(){
    if (S.cluster) return S.cluster;
    const m = M();
    if (m.cluster) return m.cluster;
    const first = ((S.ws && S.ws.clusters) || [])[0];
    return (first && (first.name || first.context || first)) || '';
  }
  function rmLoad(force){
    const root = document.getElementById('rm-root');
    if (!root) return;
    const changed = rmApplyHash();
    const cluster = rmCluster();
    if (!cluster) { root.innerHTML = '<div class="panel empty"><strong>Choose a cluster</strong>The map draws one cluster at a time. Pick one at the top right.</div>'; return; }
    const painted = !!document.getElementById('rm-panel');
    if (graph && graph.cluster === cluster && !force) { if (!painted || changed) paint(); return; }
    if (loading === cluster) return;
    loading = cluster;
    if (!painted) root.innerHTML = `<section class="panel am-topology rm-topology"><div class="panel-head"><div><h2>Topology</h2><p class="muted">Loading captured objects for ${esc(cluster)}…</p></div></div></section>`;
    fetch('/api/resources/map?cluster=' + encodeURIComponent(cluster), {credentials: 'same-origin', headers: {Accept: 'application/json'}})
      .then(r => r.json().then(d => ({ok: r.ok, d})))
      .then(({ok, d}) => {
        loading = '';
        if (!document.getElementById('rm-root')) return;
        if (!ok) { if (!painted) root.innerHTML = '<div class="notice bad">' + esc((d && d.error) || 'Map request failed') + '</div>'; return; }
        const fresh = prepare(d || {}, cluster);
        // Only the drawn namespace matters: churn elsewhere in the cluster
        // should not redraw it.
        const slice = g => { const ns = M().ns; const ids = new Set(); const nodes = g.nodes.filter(n => n.namespace === ns).map(n => { ids.add(n.id); return n; }); return JSON.stringify([nodes, g.edges.filter(e => ids.has(e.from)), g.notes]); };
        const same = graph && graph.cluster === fresh.cluster && slice(graph) === slice(fresh);
        graph = fresh;
        // A quiet refresh waits while the reader is panning, tracing a card or
        // has the Kinds menu open; the next live change tries again.
        if (same && painted && !changed) return;
        if (painted && document.querySelector('#rm-viewport.am-panning, #rm-panel .rm-kind-menu[open], #rm-lines.rm-hovering')) return;
        paint();
      })
      .catch(e => { loading = ''; const r = document.getElementById('rm-root'); if (r && !painted) r.innerHTML = '<div class="notice bad">' + esc(e.message) + '</div>'; });
  }
  // The server repeats a node each time an edge touches it; keep one of each.
  function prepare(d, cluster){
    const byId = {};
    (d.nodes || []).forEach(n => { if (!byId[n.id]) byId[n.id] = n; else if (n.badge && !byId[n.id].badge) byId[n.id].badge = n.badge; });
    return {cluster: d.cluster || cluster, byId, nodes: Object.values(byId), edges: d.edges || [], notes: d.notes || []};
  }

  function paint(){
    const root = document.getElementById('rm-root');
    if (!root || !graph) return;
    const m = M(), d = graph, cluster = rmCluster();
    const nss = [...new Set(d.nodes.map(n => n.namespace).filter(Boolean))].sort();
    if (!m.ns || nss.indexOf(m.ns) < 0) m.ns = (S.res.ns && nss.indexOf(S.res.ns) >= 0) ? S.res.ns : nss.indexOf('bookinfo') >= 0 ? 'bookinfo' : nss.find(n => n.indexOf('kube-') !== 0) || nss[0] || '';
    const nsNodes = d.nodes.filter(n => n.namespace === m.ns);
    const present = {};
    nsNodes.forEach(n => { present[n.kind] = (present[n.kind] || 0) + 1; });
    const allKinds = Object.keys(present).filter(k => HIDDEN_KINDS.indexOf(k) < 0).sort();
    // Untouched kinds follow the defaults; a namespace with none of the default
    // kinds (say, only ConfigMaps) shows everything rather than an empty box.
    const defaultsPresent = allKinds.some(k => DEFAULT_KINDS.indexOf(k) >= 0);
    m.sel = (m.sel || []).filter(id => d.byId[id]);
    const nsIds = new Set(nsNodes.map(n => n.id));
    const sel = m.sel.filter(id => nsIds.has(id)), selSet = new Set(sel), focusing = sel.length > 0;
    const kindOn = k => HIDDEN_KINDS.indexOf(k) >= 0 ? false : k in m.kinds ? m.kinds[k] : focusing ? true : defaultsPresent ? DEFAULT_KINDS.indexOf(k) >= 0 : true;
    const q = (m.q || '').toLowerCase();
    const named = n => !q || n.name.toLowerCase().indexOf(q) >= 0;
    let visible;
    if (focusing) {
      const shown = n => kindOn(n.kind);
      const reach = rmFocus(d, nsIds, sel, m.depth, shown);
      visible = nsNodes.filter(n => selSet.has(n.id) || (reach.has(n.id) && shown(n) && named(n)));
    } else visible = nsNodes.filter(n => kindOn(n.kind) && named(n));
    // Linked objects in other namespaces (a waypoint elsewhere, the
    // ServiceEntries a global Service publishes into istio-system) are drawn
    // too, tagged with their namespace.
    if (m.external !== false) {
      const vis = new Set(visible.map(n => n.id)), extra = new Map();
      d.edges.forEach(e => {
        const a = d.byId[e.from], b = d.byId[e.to];
        if (!a || !b) return;
        if (vis.has(a.id) && b.namespace !== m.ns && kindOn(b.kind)) extra.set(b.id, b);
        else if (vis.has(b.id) && a.namespace !== m.ns && kindOn(a.kind)) extra.set(a.id, a);
      });
      visible = visible.concat([...extra.values()]);
    }
    const nsWide = new Set(d.edges.filter(e => !e.to && !e.broken && e.label === 'namespace-wide').map(e => e.from));
    let links = rmLinks(d, nsNodes, visible);
    if (!m.unlinked && !focusing) {
      const linked = new Set();
      links.edges.forEach(e => { linked.add(e.from); if (e.to) linked.add(e.to); });
      nsWide.forEach(id => linked.add(id));
      visible = visible.filter(n => linked.has(n.id));
    }
    const compact = m.view ? m.view === 'compact' : visible.length > 40;
    const counts = {}, broken = {};
    links.edges.forEach(e => { if (e.broken) { (broken[e.from] = broken[e.from] || []).push(e.reason || 'broken link'); } else counts[e.kind] = (counts[e.kind] || 0) + 1; });
    const brokenTotal = Object.values(broken).reduce((n, a) => n + a.length, 0);
    const summary = rmSummaries(d, links.edges);
    const lanes = LANES.map(([title]) => ({title, nodes: []})).concat([{title: 'Other', nodes: []}]);
    const laneOf = kind => { const i = LANES.findIndex(([, ks]) => ks.indexOf(kind) >= 0); return i >= 0 ? i : /Policy$/.test(kind) ? 0 : LANES.length; };
    visible.forEach(n => lanes[laneOf(n.kind)].nodes.push(n));
    lanes.forEach(l => l.nodes.sort((a, b) => a.kind.localeCompare(b.kind) || a.name.localeCompare(b.name)));
    const shownLanes = lanes.filter(l => l.nodes.length);
    const onCount = allKinds.filter(kindOn).length;
    const clusters = (S.ws && S.ws.clusters) || [];

    const card = n => {
      const ref = attrOf({cluster: n.cluster, group: n.group, resource: n.resource, namespace: n.namespace, name: n.name});
      const reasons = broken[n.id] || [];
      const on = selSet.has(n.id);
      return `<article class="am-canvas-card rm-card${compact ? ' rm-compact' : ''}${reasons.length ? ' rm-card-broken' : ''}${on ? ' rm-card-selected' : ''}" data-rm-node="${esc(n.id)}" data-rm-kind="${esc(n.kind)}">
        <div class="am-canvas-card-head"><span class="badge">${esc(n.kind)}</span>${n.badge ? `<span class="badge ${n.badge === 'global' ? 'blue' : 'amber'}" title="${esc(n.badge === 'global' ? 'solo.io/service-scope is set: published to the peered mesh' : n.badge)}">${esc(badgeLabel(n.badge))}</span>` : ''}${n.namespace !== m.ns ? `<span class="badge rm-ext" title="Linked from another namespace">${esc(n.namespace || 'cluster')}</span>` : ''}<button class="rm-sel-btn" data-rm-sel="${esc(n.id)}" aria-pressed="${on}" title="${on ? 'Remove from the selection' : 'Add to the selection. ⌘, Ctrl or Shift + click a card does the same.'}">${on ? '✓ Selected' : '+ Select'}</button></div>
        <button class="link rm-card-name" data-rm-open="${ref}" title="Open ${esc(n.kind)} ${esc(n.name)}">${esc(n.name)}</button>
        ${compact ? '' : `<small>${esc([nsWide.has(n.id) ? 'Applies to the whole namespace' : '', summary[n.id]].filter(Boolean).join(' · ') || 'No captured relationship in this view')}</small>`}
        ${reasons.length ? `<small class="red">${esc(reasons.join(' · '))}</small>` : ''}
        ${compact ? '' : `<div class="row wrap"><button data-rm-open="${ref}">View object</button><button data-rm-yaml="${ref}">YAML</button><button data-rm-focus="${esc(n.id)}" title="Show only this object and what links to it">Focus</button></div>`}
      </article>`;
    };
    const laneHTML = shownLanes.map(l => `<div class="rm-lane"><h4 class="rm-lane-title">${esc(l.title)} <span class="badge">${l.nodes.length}</span></h4>${l.nodes.map(card).join('')}</div>`).join('');
    const box = `<section class="am-cluster-box rm-box" style="left:10px;top:10px"><header class="rm-box-head">
        <div class="rm-box-title"><span class="am-cluster-symbol" aria-hidden="true">◫</span><span><strong>${esc(m.ns)}</strong><small>${esc(cluster)} · namespace</small></span></div>
        <div class="am-cluster-bar"><small>${focusing ? 'Focused on ' + sel.length + ' selected · ' + esc(depthLabel(m.depth)) + ' · ' : ''}${visible.length} of ${nsNodes.length} captured objects shown${links.outside ? ' · ' + links.outside + ' link' + (links.outside > 1 ? 's' : '') + ' leave this namespace' : ''}</small><div class="row"><button data-rm-view="cards" aria-pressed="${!compact}">Cards</button><button data-rm-view="compact" aria-pressed="${compact}">Compact</button></div></div>
      </header>${shownLanes.length ? `<div class="rm-lanes">${laneHTML}</div>` : `<p class="am-canvas-empty rm-empty">${focusing ? 'The selected objects are hidden by the current kinds or filter.' : 'Nothing in this namespace matches the current kinds and filter.'} Missing data does not prove absence.</p>`}</section>`;
    const chips = Object.keys(EDGE_KINDS).filter(k => counts[k]).map(k => `<button class="am-connection-chip am-peer-chip" data-rm-chip="${k}" aria-pressed="${m.selected === k}" title="Highlight these relationships">⟶ ${esc(EDGE_KINDS[k].label)} <span class="rm-chip-n">${counts[k]}</span></button>`).join('')
      + (brokenTotal ? `<button class="am-connection-chip rm-broken-chip" data-rm-chip="broken" aria-pressed="${m.selected === 'broken'}" title="Highlight objects whose link resolves to nothing">⚠ Broken links <span class="rm-chip-n">${brokenTotal}</span></button>` : '');

    root.innerHTML = `<section class="panel am-topology rm-topology${m.fullscreen ? ' am-map-fullscreen' : ''}" id="rm-panel">
      <div class="panel-head"><div class="rm-head-title"><h2>Topology</h2><span class="muted">${esc(cluster)} · ${esc(m.ns)} · ${visible.length} objects · ${links.edges.length - brokenTotal} relationships drawn</span></div>
        <div class="row wrap am-map-controls"><button id="rm-zoom-out" type="button" aria-label="Zoom out">−</button><output id="rm-zoom">${Math.round(m.zoom * 100)}%</output><button id="rm-zoom-in" type="button" aria-label="Zoom in">+</button><button id="rm-fit" type="button">Fit</button><button id="rm-reset" type="button">Reset layout</button><button id="rm-refresh" type="button" title="Fetch the captured objects again and redraw">Refresh</button><button id="rm-full" type="button">${m.fullscreen ? 'Exit full screen' : 'Full screen'}</button></div></div>
      <div class="am-map-tools">
        <select id="rm-cluster" aria-label="Cluster">${clusters.map(c => { const n = c.name || c.context || c; return `<option ${n === cluster ? 'selected' : ''}>${esc(n)}</option>`; }).join('')}</select>
        <select id="rm-ns" aria-label="Namespace">${nss.map(n => `<option ${n === m.ns ? 'selected' : ''}>${esc(n)}</option>`).join('')}</select>
        <input id="rm-search" type="search" placeholder="Filter by name" value="${esc(m.q || '')}" aria-label="Filter by name" autocomplete="off" spellcheck="false">
        <details class="rm-kind-menu"><summary>Kinds <span class="badge">${onCount}/${allKinds.length}</span></summary><div class="rm-kind-list"><div class="rm-kind-quick"><button class="link" data-rm-kinds="all">All</button><button class="link" data-rm-kinds="none">None</button><button class="link" data-rm-kinds="default">Defaults</button></div>${kindGroups(allKinds).map(([title, ks]) => `<h5 class="rm-menu-group">${esc(title)}</h5>` + ks.map(k => `<label><input type="checkbox" value="${esc(k)}" ${kindOn(k) ? 'checked' : ''}> <span>${esc(k)}</span><small>${present[k]}</small></label>`).join('')).join('')}</div></details>
        ${pickerHTML(nsNodes, selSet)}
        ${focusing ? '' : `<label class="am-map-scope"><input id="rm-unlinked" type="checkbox" ${m.unlinked ? 'checked' : ''}> Unlinked objects</label>`}<label class="am-map-scope" title="Draw linked objects that live in other namespaces, such as a waypoint elsewhere or the ServiceEntries a global Service publishes"><input id="rm-external" type="checkbox" ${m.external !== false ? 'checked' : ''}> Other namespaces</label>
        <span class="muted">${focusing ? '⌘, Ctrl or Shift + click a card to add or remove it' : 'Hover to trace · drag to pan · ⌘/Ctrl + wheel to zoom'}</span>
      </div>
      ${focusing ? selectionBar(sel, d) : ''}
      ${chips ? `<div class="am-map-connections">${chips}</div>` : ''}
      <div class="am-map-viewport" id="rm-viewport" tabindex="0" aria-label="Resource map. Drag empty space to pan; use the zoom controls or Ctrl plus wheel to zoom."><div id="rm-space"><div id="rm-world"><svg id="rm-lines" xmlns="http://www.w3.org/2000/svg" aria-label="Captured relationships"></svg>${box}</div></div></div>
      <p class="am-map-legend"><span id="rm-line-status"></span> Teal: captured relationship. Red dashed: selector matches nothing, missing backend or unresolved parentRef. Config links are drawn once, from the top-most owner. Lines are captured configuration, not live traffic.${d.notes.length ? `<br><span class="rm-notes">${esc(d.notes.join(' '))}</span>` : ''}</p>
    </section>`;
    wire(links.edges, {present, allKinds});
  }

  function badgeLabel(b){ return b === 'enterprise-agentgateway' ? 'Enterprise agentgateway' : b === 'global' ? 'Global' : b; }
  function depthLabel(v){ const d = DEPTHS.find(([n]) => n === v); return d ? d[1] : v + ' hops'; }
  function laneIndex(kind){ const i = LANES.findIndex(([, ks]) => ks.indexOf(kind) >= 0); return i >= 0 ? i : /Policy$/.test(kind) ? 0 : LANES.length; }
  // Kinds in lane order, under the lane's title, so the menu reads like the map.
  function kindGroups(kinds){
    const groups = {};
    kinds.forEach(k => { const i = laneIndex(k); (groups[i] = groups[i] || []).push(k); });
    return Object.keys(groups).map(Number).sort((a, b) => a - b).map(i => [i < LANES.length ? LANES[i][0] : 'Other', groups[i].sort()]);
  }
  // The object picker: every object in the namespace whatever the Kinds menu
  // says, grouped by kind, filtered as you type. Ticks are applied when the
  // menu closes so the list does not jump while you choose.
  function pickerHTML(nsNodes, selSet){
    const byKind = {};
    nsNodes.forEach(n => { if (HIDDEN_KINDS.indexOf(n.kind) < 0) (byKind[n.kind] = byKind[n.kind] || []).push(n); });
    const groups = kindGroups(Object.keys(byKind));
    const rows = groups.map(([, ks]) => ks.map(k => {
      const items = byKind[k].slice().sort((a, b) => a.name.localeCompare(b.name));
      const all = items.every(n => selSet.has(n.id));
      return `<div class="rm-pick-kind" data-rm-pick-group="${esc(k)}"><label class="rm-pick-head"><input type="checkbox" data-rm-pick-kind="${esc(k)}" ${all ? 'checked' : ''}> <span>${esc(k)}</span><small>${items.length}</small></label>${items.map(n => `<label class="rm-pick-item" data-rm-pick-name="${esc(n.name.toLowerCase())}"><input type="checkbox" data-rm-pick="${esc(n.id)}" ${selSet.has(n.id) ? 'checked' : ''}> <span>${esc(n.name)}</span></label>`).join('')}</div>`;
    }).join('')).join('');
    const n = selSet.size;
    return `<details class="rm-kind-menu rm-pick-menu" id="rm-pick"><summary>${n ? `Objects <span class="badge teal">${n} selected</span>` : 'Pick objects…'}</summary>
      <div class="rm-kind-list rm-pick-list" role="dialog" aria-label="Pick objects to show">
        <input id="rm-pick-q" type="search" placeholder="Type to filter: name or kind" autocomplete="off" spellcheck="false" aria-label="Filter objects">
        <div class="rm-kind-quick"><button class="link" data-rm-pick-all>Select matching</button><button class="link" data-rm-pick-none>Clear</button><span class="muted rm-pick-count">${n} selected</span></div>
        <div class="rm-pick-rows">${rows || '<p class="muted">No captured objects in this namespace.</p>'}</div>
        <div class="rm-pick-foot"><small class="muted">Space ticks · Shift + click ticks a range · Enter shows them</small><button class="primary" data-rm-pick-apply>Show on map</button></div>
      </div></details>`;
  }
  function selectionBar(sel, d){
    const m = M();
    return `<div class="rm-selection" aria-label="Selected objects"><span class="rm-selection-label">Focus</span>${sel.map(id => { const n = d.byId[id]; return `<span class="am-connection-chip rm-sel-chip"><span class="badge">${esc(n.kind)}</span>${esc(n.name)}<button class="link" data-rm-unsel="${esc(id)}" aria-label="Remove ${esc(n.kind)} ${esc(n.name)}">×</button></span>`; }).join('')}
      <span class="rm-depth" role="group" aria-label="How far out to draw">${DEPTHS.map(([v, label]) => `<button data-rm-depth="${v}" aria-pressed="${m.depth === v}">${esc(label)}</button>`).join('')}</span>
      <button class="link" data-rm-clear>Clear selection</button></div>`;
  }

  // Edges between the visible cards. A chain that runs through a hidden kind
  // (Deployment → ReplicaSet → Pod with ReplicaSet unticked) is bridged so the
  // ends still connect; "uses" links are kept on the top-most visible owner only.
  function rmLinks(d, nsNodes, visible){
    const vis = new Set(visible.map(n => n.id)), inNs = new Set(nsNodes.map(n => n.id));
    const out = {};
    d.edges.forEach(e => { if (inNs.has(e.from) && e.to) (out[e.from] = out[e.from] || []).push(e); });
    const edges = [], seen = new Set();
    let outside = 0;
    const add = (from, to, kind, via, src) => { const key = from + '>' + to + '>' + kind; if (seen.has(key)) return; seen.add(key); edges.push({from, to, kind, via, broken: !!src.broken, reason: src.reason || '', label: src.label || ''}); };
    const bridge = (from, hidden, kind, via, depth) => {
      if (depth > 3) return;
      (out[hidden] || []).forEach(e => {
        if (e.kind !== kind) return;
        if (vis.has(e.to)) add(from, e.to, kind, via, e);
        else if (inNs.has(e.to) && d.byId[e.to]) bridge(from, e.to, kind, via.concat(d.byId[e.to].kind), depth + 1);
      });
    };
    d.edges.forEach(e => {
      if (!vis.has(e.from)) return;
      if (!e.to) { if (e.broken) add(e.from, '', e.kind, [], e); return; }
      if (vis.has(e.to)) add(e.from, e.to, e.kind, [], e);
      else if (!inNs.has(e.to) && d.byId[e.to]) outside++;
      else if (inNs.has(e.to) && d.byId[e.to] && (e.kind === 'owner' || e.kind === 'endpoints')) bridge(e.from, e.to, e.kind, [d.byId[e.to].kind], 1);
    });
    const owner = {}, usesBy = {};
    edges.forEach(e => { if (e.kind === 'owner' && e.to) owner[e.to] = e.from; if (e.kind === 'uses' && e.to) (usesBy[e.from] = usesBy[e.from] || new Set()).add(e.to); });
    const redundant = e => { if (e.kind !== 'uses' || !e.to) return false; let p = owner[e.from], depth = 0; while (p && depth < 5) { if (usesBy[p] && usesBy[p].has(e.to)) return true; p = owner[p]; depth++; } return false; };
    return {edges: edges.filter(e => !redundant(e)), outside};
  }
  // "Owns 2 ReplicaSets · Uses 1 ServiceAccount" for each card.
  function rmSummaries(d, edges){
    const per = {};
    const bump = (id, verb, kind) => { const p = per[id] = per[id] || {}; const k = verb + '|' + kind; p[k] = (p[k] || 0) + 1; };
    edges.forEach(e => {
      if (!e.to || e.broken) return;
      const v = EDGE_KINDS[e.kind] || {out: e.kind, in: e.kind}, from = d.byId[e.from], to = d.byId[e.to];
      if (from && to) { bump(e.from, v.out, to.kind); bump(e.to, v.in, from.kind); }
    });
    const text = {};
    Object.keys(per).forEach(id => {
      const parts = Object.keys(per[id]).map(k => { const [verb, kind] = k.split('|'), n = per[id][k]; return verb + ' ' + n + ' ' + kind + (n > 1 ? 's' : ''); });
      text[id] = parts.join(' · ');
    });
    return text;
  }

  function wire(edges, kinds){
    const m = M(), panel = document.getElementById('rm-panel'), viewport = document.getElementById('rm-viewport');
    if (!panel || !viewport) return;
    const world = document.getElementById('rm-world'), space = document.getElementById('rm-space'), zoomOut = document.getElementById('rm-zoom');
    const byId = {};
    document.querySelectorAll('#rm-cluster').forEach(sel => sel.onchange = e => {
      const v = e.target.value;
      m.cluster = v; m.ns = ''; m.kinds = {}; m.selected = ''; m.sel = []; m.fitted = false; graph = null; rmWriteHash();
      if (S.cluster && S.cluster !== v) setCluster(v); else rmLoad(true);
    });
    document.getElementById('rm-ns').onchange = e => { m.ns = e.target.value; m.selected = ''; m.sel = []; m.fitted = false; rmWriteHash(); paint(); };
    let t;
    const search = document.getElementById('rm-search');
    search.oninput = e => { clearTimeout(t); const v = e.target.value; t = setTimeout(() => { m.q = v; paint(); const s = document.getElementById('rm-search'); if (s) { s.focus(); s.setSelectionRange(v.length, v.length); } }, 200); };
    panel.querySelectorAll('.rm-kind-menu:not(.rm-pick-menu) .rm-kind-list input').forEach(ch => ch.onchange = () => { m.kinds[ch.value] = ch.checked; paint(); openKinds(); });
    panel.querySelectorAll('[data-rm-kinds]').forEach(b => b.onclick = () => {
      const mode = b.dataset.rmKinds;
      kinds.allKinds.forEach(k => { m.kinds[k] = mode === 'all' ? true : mode === 'none' ? false : DEFAULT_KINDS.indexOf(k) >= 0; });
      paint(); openKinds();
    });
    const openKinds = () => { const dt = document.querySelector('#rm-panel .rm-kind-menu:not(.rm-pick-menu)'); if (dt) dt.open = true; };
    wireSelection(panel, world);
    document.getElementById('rm-external').onchange = e => { m.external = e.target.checked; paint(); };
    const unl = document.getElementById('rm-unlinked'); if (unl) unl.onchange = e => { m.unlinked = e.target.checked; paint(); };
    panel.querySelectorAll('[data-rm-view]').forEach(b => b.onclick = () => { m.view = b.dataset.rmView; paint(); });
    panel.querySelectorAll('[data-rm-open]').forEach(b => b.onclick = () => openObject(JSON.parse(b.dataset.rmOpen)));
    panel.querySelectorAll('[data-rm-yaml]').forEach(b => b.onclick = () => openObject(JSON.parse(b.dataset.rmYaml), 'yaml'));
    const chips = () => panel.querySelectorAll('[data-rm-chip]');
    chips().forEach(b => b.onclick = () => {
      m.selected = m.selected === b.dataset.rmChip ? '' : b.dataset.rmChip;
      chips().forEach(el => el.setAttribute('aria-pressed', String(el.dataset.rmChip === m.selected)));
      world.querySelectorAll('.rm-card').forEach(c => c.classList.toggle('am-link-endpoint', m.selected === 'broken' && c.classList.contains('rm-card-broken')));
      draw();
    });
    const svg = document.getElementById('rm-lines');
    world.querySelectorAll('.rm-card').forEach(c => {
      c.onmouseenter = () => { svg.classList.add('rm-hovering'); svg.querySelectorAll('path[data-from]').forEach(p => p.classList.toggle('rm-hit', p.dataset.from === c.dataset.rmNode || p.dataset.to === c.dataset.rmNode)); };
      c.onmouseleave = () => { svg.classList.remove('rm-hovering'); svg.querySelectorAll('path.rm-hit').forEach(p => p.classList.remove('rm-hit')); };
    });

    const draw = () => rmDrawLinks(world, m.zoom, edges, m.selected, graph.byId);
    const resize = () => {
      let width = 700, height = 450;
      world.querySelectorAll('.rm-box').forEach(b => { width = Math.max(width, b.offsetLeft + b.offsetWidth + 32); height = Math.max(height, b.offsetTop + b.offsetHeight + 32); });
      m.world = {width, height};
      world.style.width = width + 'px'; world.style.height = height + 'px'; world.style.transform = 'scale(' + m.zoom + ')';
      space.style.width = width * m.zoom + 'px'; space.style.height = height * m.zoom + 'px';
      zoomOut.textContent = Math.round(m.zoom * 100) + '%';
      draw();
    };
    const zoom = (value, anchorX = viewport.clientWidth / 2, anchorY = viewport.clientHeight / 2) => {
      const x = (viewport.scrollLeft + anchorX) / m.zoom, y = (viewport.scrollTop + anchorY) / m.zoom;
      m.zoom = Math.min(1.75, Math.max(.2, value));
      resize();
      viewport.scrollLeft = x * m.zoom - anchorX; viewport.scrollTop = y * m.zoom - anchorY;
    };
    // Lanes are tall and narrow, so Fit matches the width and leaves the
    // namespace to scroll vertically; a full fit would shrink cards to specks.
    const fit = () => { zoom(Math.min(1, (viewport.clientWidth - 8) / m.world.width), 0, 0); viewport.scrollTo(0, 0); };
    document.getElementById('rm-zoom-out').onclick = () => zoom(m.zoom / 1.2);
    document.getElementById('rm-zoom-in').onclick = () => zoom(m.zoom * 1.2);
    document.getElementById('rm-fit').onclick = fit;
    document.getElementById('rm-reset').onclick = () => { m.view = ''; m.selected = ''; m.zoom = 1; m.scrollLeft = m.scrollTop = 0; m.fitted = false; paint(); };
    const setFull = on => { m.fullscreen = on; panel.classList.toggle('am-map-fullscreen', on); document.getElementById('rm-full').textContent = on ? 'Exit full screen' : 'Full screen'; sizeViewport(); if (on) viewport.focus(); };
    document.getElementById('rm-refresh').onclick = () => rmLoad(true);
    document.getElementById('rm-full').onclick = () => setFull(!m.fullscreen);
    viewport.onkeydown = e => { if (e.key === 'Escape' && m.fullscreen) { e.stopPropagation(); setFull(false); } };
    viewport.addEventListener('wheel', e => { if (!e.ctrlKey && !e.metaKey) return; e.preventDefault(); const r = viewport.getBoundingClientRect(); zoom(m.zoom * (e.deltaY < 0 ? 1.1 : 1 / 1.1), e.clientX - r.left, e.clientY - r.top); }, {passive: false});
    viewport.onpointerdown = e => {
      if (e.button !== 0 || e.target.closest('.rm-card, button, a, input, select')) return;
      viewport.setPointerCapture(e.pointerId);
      const x = e.clientX, y = e.clientY, left = viewport.scrollLeft, top = viewport.scrollTop;
      viewport.classList.add('am-panning');
      viewport.onpointermove = ev => { viewport.scrollLeft = left + x - ev.clientX; viewport.scrollTop = top + y - ev.clientY; };
      const finish = () => { viewport.onpointermove = null; viewport.classList.remove('am-panning'); };
      viewport.onpointerup = finish; viewport.onpointercancel = finish;
    };
    viewport.onscroll = () => { m.scrollLeft = viewport.scrollLeft; m.scrollTop = viewport.scrollTop; };
    const sizeViewport = () => {
      if (!viewport.isConnected) return;
      if (!m.fullscreen) {
        // Fill down to the terminal dock (or the window), leaving only the
        // legend and a small margin below the viewport.
        const legend = panel.querySelector('.am-map-legend'), dock = document.getElementById('terminal');
        const dockTop = dock && !dock.classList.contains('hidden') && dock.offsetHeight ? dock.getBoundingClientRect().top : innerHeight;
        const limit = Math.min(innerHeight, dockTop > 0 ? dockTop : innerHeight);
        const room = Math.max(360, Math.floor(limit - viewport.getBoundingClientRect().top - (legend ? legend.offsetHeight : 0) - 10));
        if (viewport.style.height !== room + 'px') viewport.style.height = room + 'px';
        viewport.style.maxHeight = 'none';
      }
      else { viewport.style.height = ''; viewport.style.maxHeight = ''; }
      resize();
    };
    if (observer) observer.disconnect();
    observer = new ResizeObserver(sizeViewport); observer.observe(viewport);
    if (onResize) window.removeEventListener('resize', onResize);
    onResize = sizeViewport; window.addEventListener('resize', onResize);
    sizeViewport();
    if (!m.fitted) { m.fitted = true; fit(); } else { viewport.scrollLeft = m.scrollLeft || 0; viewport.scrollTop = m.scrollTop || 0; }
    requestAnimationFrame(sizeViewport);
  }

  function rmSetSel(ids){
    const m = M();
    m.sel = [...new Set(ids)]; m.fitted = false; m.scrollLeft = m.scrollTop = 0;
    rmWriteHash(); paint();
  }
  function wireSelection(panel, world){
    const m = M();
    const toggle = id => rmSetSel(m.sel.indexOf(id) >= 0 ? m.sel.filter(x => x !== id) : m.sel.concat(id));
    panel.querySelectorAll('[data-rm-sel]').forEach(b => b.onclick = e => { e.stopPropagation(); toggle(b.dataset.rmSel); });
    panel.querySelectorAll('[data-rm-focus]').forEach(b => b.onclick = () => rmSetSel([b.dataset.rmFocus]));
    panel.querySelectorAll('[data-rm-unsel]').forEach(b => b.onclick = () => rmSetSel(m.sel.filter(x => x !== b.dataset.rmUnsel)));
    panel.querySelectorAll('[data-rm-depth]').forEach(b => b.onclick = () => { m.depth = +b.dataset.rmDepth; rmSetSel(m.sel); });
    panel.querySelectorAll('[data-rm-clear]').forEach(b => b.onclick = () => rmSetSel([]));
    world.querySelectorAll('.rm-card').forEach(c => c.addEventListener('click', e => {
      if (e.target.closest('button, a, input') || !(e.metaKey || e.ctrlKey || e.shiftKey)) return;
      e.preventDefault(); toggle(c.dataset.rmNode);
    }));

    const pick = document.getElementById('rm-pick');
    if (!pick) return;
    const pending = new Set(m.sel.filter(id => pick.querySelector(`[data-rm-pick="${CSS.escape(id)}"]`)));
    const start = [...pending].sort().join(',');
    const q = pick.querySelector('#rm-pick-q'), count = pick.querySelector('.rm-pick-count');
    const items = () => [...pick.querySelectorAll('.rm-pick-item')].filter(l => !l.hidden);
    const boxes = () => items().map(l => l.querySelector('input'));
    const sync = () => {
      pick.querySelectorAll('.rm-pick-kind').forEach(g => {
        const own = [...g.querySelectorAll('[data-rm-pick]')], on = own.filter(i => i.checked).length;
        const head = g.querySelector('[data-rm-pick-kind]');
        head.checked = on > 0 && on === own.length; head.indeterminate = on > 0 && on < own.length;
      });
      count.textContent = pending.size + ' selected';
    };
    const set = (input, on) => { input.checked = on; if (on) pending.add(input.dataset.rmPick); else pending.delete(input.dataset.rmPick); };
    let last = null;
    pick.querySelectorAll('[data-rm-pick]').forEach(input => input.addEventListener('click', e => {
      const list = boxes();
      if (e.shiftKey && last && list.indexOf(last) >= 0) {
        const a = list.indexOf(last), b = list.indexOf(input);
        list.slice(Math.min(a, b), Math.max(a, b) + 1).forEach(i => set(i, input.checked));
      } else set(input, input.checked);
      last = input; sync();
    }));
    pick.querySelectorAll('[data-rm-pick-kind]').forEach(head => head.onchange = () => {
      const g = head.closest('.rm-pick-kind');
      [...g.querySelectorAll('.rm-pick-item')].filter(l => !l.hidden).forEach(l => set(l.querySelector('input'), head.checked));
      sync();
    });
    q.oninput = () => {
      const v = q.value.trim().toLowerCase();
      pick.querySelectorAll('.rm-pick-kind').forEach(g => {
        const kindHit = g.dataset.rmPickGroup.toLowerCase().indexOf(v) >= 0;
        let any = false;
        g.querySelectorAll('.rm-pick-item').forEach(l => { l.hidden = !!v && !kindHit && l.dataset.rmPickName.indexOf(v) < 0; any = any || !l.hidden; });
        g.hidden = !any;
      });
    };
    pick.querySelector('[data-rm-pick-all]').onclick = () => { boxes().forEach(i => set(i, true)); sync(); };
    pick.querySelector('[data-rm-pick-none]').onclick = () => { pick.querySelectorAll('[data-rm-pick]').forEach(i => set(i, false)); sync(); };
    const apply = () => { pick.open = false; };
    pick.querySelector('[data-rm-pick-apply]').onclick = apply;
    pick.addEventListener('toggle', () => {
      if (pick.open) { setTimeout(() => q.focus(), 0); return; }
      if ([...pending].sort().join(',') !== start) rmSetSel(m.sel.filter(id => !pick.querySelector(`[data-rm-pick="${CSS.escape(id)}"]`)).concat([...pending]));
    });
    pick.onkeydown = e => {
      const list = boxes(), i = list.indexOf(document.activeElement);
      if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); pick.open = false; pick.querySelector('summary').focus(); }
      else if (e.key === 'Enter' && e.target.tagName !== 'BUTTON' && e.target.tagName !== 'SUMMARY') { e.preventDefault(); apply(); }
      else if (e.key === 'ArrowDown' && list.length) { e.preventDefault(); (list[i + 1] || list[0]).focus(); }
      else if (e.key === 'ArrowUp' && list.length) { e.preventDefault(); if (i <= 0) q.focus(); else list[i - 1].focus(); }
    };
    sync();
  }
  // A menu closes when you click anywhere else, as a menu should.
  if (typeof document !== 'undefined' && document.addEventListener) document.addEventListener('pointerdown', e => {
    document.querySelectorAll('#rm-panel .rm-kind-menu[open]').forEach(dt => { if (!dt.contains(e.target)) dt.open = false; });
  }, true);

  // Orthogonal routes around the cards, as the Ambient map draws them. Each
  // edge only considers the cards between its ends so a few hundred cards stay
  // quick; after 1.5s the rest fall back to plain curves.
  function rmDrawLinks(world, zoom, edges, selected, byId){
    const svg = world.querySelector('#rm-lines');
    if (!svg) return;
    const ns = 'http://www.w3.org/2000/svg', wr = world.getBoundingClientRect();
    const rect = e => { const r = e.getBoundingClientRect(); return {x: (r.left - wr.left) / zoom, y: (r.top - wr.top) / zoom, w: r.width / zoom, h: r.height / zoom}; };
    const rects = new Map([...world.querySelectorAll('[data-rm-node]')].map(e => [e.dataset.rmNode, rect(e)]));
    const obstacles = [...rects.values(), ...[...world.querySelectorAll('.rm-box>header, .rm-lane-title')].map(rect)];
    svg.replaceChildren();
    svg.setAttribute('viewBox', '0 0 ' + world.offsetWidth + ' ' + world.offsetHeight);
    const defs = document.createElementNS(ns, 'defs');
    [['rm-arrow', TEAL], ['rm-arrow-broken', RED]].forEach(([id, fill]) => {
      const marker = document.createElementNS(ns, 'marker');
      marker.id = id; marker.setAttribute('viewBox', '0 0 10 10'); marker.setAttribute('refX', '9'); marker.setAttribute('refY', '5'); marker.setAttribute('markerUnits', 'userSpaceOnUse'); marker.setAttribute('markerWidth', '10'); marker.setAttribute('markerHeight', '10'); marker.setAttribute('orient', 'auto');
      const tri = document.createElementNS(ns, 'path'); tri.setAttribute('d', 'M1 1L9 5L1 9Z'); tri.setAttribute('fill', fill); marker.appendChild(tri); defs.appendChild(marker);
    });
    svg.appendChild(defs);
    const started = performance.now();
    let drawn = 0, bridged = 0, blocked = 0, simplified = 0;
    const canRoute = typeof amRoute === 'function' && typeof amRoundedRoute === 'function';
    for (const e of edges) {
      if (!e.to) continue;
      const a = rects.get(e.from), b = rects.get(e.to);
      if (!a || !b) continue;
      let dAttr = '';
      if (canRoute && performance.now() - started < 1500) {
        const box = {x: Math.min(a.x, b.x) - 60, y: Math.min(a.y, b.y) - 60, w: Math.abs(Math.max(a.x + a.w, b.x + b.w) - Math.min(a.x, b.x)) + 120, h: Math.abs(Math.max(a.y + a.h, b.y + b.h) - Math.min(a.y, b.y)) + 120};
        const near = obstacles.filter(r => r.x < box.x + box.w && r.x + r.w > box.x && r.y < box.y + box.h && r.y + r.h > box.y);
        const route = amRoute(a, b, near);
        if (route) dAttr = amRoundedRoute(route); else blocked++;
      } else simplified++;
      if (!dAttr) {
        const sameLane = a.x < b.x + b.w && a.x + a.w > b.x;
        const s = sameLane ? {x: a.x + a.w / 2, y: a.y + a.h} : a.x + a.w <= b.x ? {x: a.x + a.w, y: a.y + a.h / 2} : {x: a.x, y: a.y + a.h / 2};
        const t2 = sameLane ? {x: b.x + b.w / 2, y: b.y} : a.x + a.w <= b.x ? {x: b.x, y: b.y + b.h / 2} : {x: b.x + b.w, y: b.y + b.h / 2};
        dAttr = sameLane ? `M${s.x} ${s.y} C${s.x} ${(s.y + t2.y) / 2} ${t2.x} ${(s.y + t2.y) / 2} ${t2.x} ${t2.y}` : `M${s.x} ${s.y} C${(s.x + t2.x) / 2} ${s.y} ${(s.x + t2.x) / 2} ${t2.y} ${t2.x} ${t2.y}`;
      }
      const dim = selected && selected !== e.kind;
      const p = document.createElementNS(ns, 'path');
      p.setAttribute('d', dAttr); p.setAttribute('fill', 'none'); p.setAttribute('stroke', e.broken ? RED : TEAL); p.setAttribute('stroke-width', selected === e.kind ? '3' : '1.75'); p.setAttribute('stroke-linecap', 'round'); p.setAttribute('stroke-linejoin', 'round');
      p.setAttribute('class', rmEdgeClass(e)); p.setAttribute('marker-end', e.broken ? 'url(#rm-arrow-broken)' : 'url(#rm-arrow)');
      if (e.broken) p.setAttribute('stroke-dasharray', '5 4');
      if (dim) p.setAttribute('opacity', '.15');
      p.dataset.from = e.from; p.dataset.to = e.to; p.dataset.kind = e.kind;
      const from = byId[e.from] || {}, to = byId[e.to] || {};
      const title = document.createElementNS(ns, 'title');
      title.textContent = `${from.kind || ''} ${from.name || ''} → ${to.kind || ''} ${to.name || ''} · ${(EDGE_KINDS[e.kind] || {}).label || e.kind}${e.via && e.via.length ? ' · through hidden ' + e.via.join(' → ') : ''}${e.reason ? ' · ' + e.reason : ''}`;
      p.appendChild(title);
      svg.appendChild(p);
      drawn++;
      if (e.via && e.via.length) bridged++;
    }
    const status = document.getElementById('rm-line-status');
    if (status) status.textContent = `${drawn} relationship${drawn === 1 ? '' : 's'} drawn.${bridged ? ' ' + bridged + ' pass through a hidden kind.' : ''}${blocked ? ' ' + blocked + ' drawn as plain curves where no clear route exists.' : ''}${simplified ? ' ' + simplified + ' simplified to keep the map quick.' : ''}`;
  }
  function rmEdgeClass(edge){ return edge && edge.broken ? 'am-map-line rm-broken' : 'am-map-line'; }
  function rmVisible(node, filter){
    if (!filter) return true;
    if (filter.kinds && filter.kinds.length && filter.kinds.indexOf(node.kind) < 0) return false;
    if (filter.search && String(node.name || '').toLowerCase().indexOf(String(filter.search).toLowerCase()) < 0) return false;
    return true;
  }
  window.rmLoad = rmLoad;
  window.rmEdgeClass = rmEdgeClass;
  window.rmVisible = rmVisible;
  window.rmLinks = rmLinks;
  window.rmFocus = rmFocus;
  // "Show on map" from an object drawer or anywhere else that holds a ref.
  window.rmShowOnMap = ref => {
    const id = [ref.cluster, ref.group || '', ref.resource, ref.namespace || '', ref.name].join('/');
    const p = new URLSearchParams({tab: 'map', cluster: ref.cluster, ns: ref.namespace || '', sel: id});
    location.hash = 'resources?' + p.toString().replace(/%2F/gi, '/');
  };
})();
