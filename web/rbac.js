// RBAC explorer. Returns HTML for the desk render path. Never writes to document.body.
(function(){
  function saLink(cluster, namespace, name){
    return 'rbac?cluster=' + encodeURIComponent(cluster || '') + '&namespace=' + encodeURIComponent(namespace || '') + '&name=' + encodeURIComponent(name || '');
  }
  function grantRows(grants){
    if (!grants.length) return '<p class="muted">None captured.</p>';
    const body = grants.map(g => {
      const fl = (g.flags || []).join(' ');
      const names = (g.resourceNames || []).join(', ') || 'all';
      return `<tr><td>${esc(g.apiGroup || 'core')}</td><td class="mono">${esc(g.resource)}</td><td>${esc(g.subresource || '')}</td><td>${esc(g.verb)}</td><td>${esc(names)}</td><td><button class="link" data-rbac-ref="${attr(g.binding)}">${esc(g.binding.kind)} ${esc(g.binding.name)}</button></td><td class="${fl ? 'red' : ''}">${esc(fl)}</td></tr>`;
    }).join('');
    return `<div class="table-scroll"><table><thead><tr><th>API group</th><th>Resource</th><th>Sub</th><th>Verb</th><th>Names</th><th>Binding</th><th>Flags</th></tr></thead><tbody>${body}</tbody></table></div>`;
  }
  function clusterNames(){
    return (S.ws?.clusters || []).map(c => c.name).filter(Boolean);
  }
  function rbacCluster(){
    if (S.cluster) return S.cluster;
    return S.rbac?.cluster || clusterNames()[0] || '';
  }
  function accountToolbar(cluster, namespace, filter, nsOptions){
    const clusters = clusterNames();
    return `<div class="toolbar"><select id="rbac-cluster" aria-label="Cluster">${clusters.map(n => `<option value="${esc(n)}" ${n === cluster ? 'selected' : ''}>${esc(n)}</option>`).join('')}</select>
      <select id="rbac-ns" aria-label="Namespace"><option value="">All namespaces</option>${nsOptions.map(n => `<option ${n === namespace ? 'selected' : ''}>${esc(n)}</option>`).join('')}</select>
      <input id="rbac-q" placeholder="Filter by name (press /)" value="${esc(filter)}" aria-label="Filter" autocomplete="off" spellcheck="false"><button id="rbac-reset">Reset</button></div>`;
  }
  async function rbacPage(){
    const q = S.rbac || (S.rbac = {});
    const cluster = rbacCluster();
    const namespace = q.listNs || '';
    const filter = q.q || '';
    const name = q.name || '';
    const saNs = q.namespace || '';
    let accounts = [];
    try {
      const listed = await api('api/objects', {cluster, group: '', resource: 'serviceaccounts'});
      accounts = listed.rows || [];
    } catch (_) { accounts = []; }
    const nsOptions = [...new Set(accounts.map(a => a.namespace).filter(Boolean))].sort();
    const words = filter.toLowerCase().split(/\s+/).filter(Boolean);
    const shown = accounts.filter(a => (!namespace || a.namespace === namespace) && words.every(w => (a.namespace + '/' + a.name).toLowerCase().includes(w)));
    const toolbar = accountToolbar(cluster, namespace, filter, nsOptions);
    const listed = `<section class="panel"><div class="panel-head"><div class="row"><h2>Service accounts</h2><span class="badge">${shown.length}</span></div><span class="mono muted" style="font-size:10px">${esc(cluster)}</span></div>
      <div class="table-scroll" style="max-height:42vh">${shown.length ? `<table><thead><tr><th>Namespace</th><th>Name</th></tr></thead><tbody>${shown.map(a => `<tr class="${a.name === name && a.namespace === saNs ? 'active' : ''}" data-rbac-sa="${attr({cluster: a.cluster, namespace: a.namespace, name: a.name})}"><td class="muted">${esc(a.namespace)}</td><td class="mono blue">${esc(a.name)}</td></tr>`).join('')}</tbody></table>` : '<div class="empty"><strong>No captured service accounts match</strong>Try another namespace or filter.</div>'}</div>
      <div class="table-foot"><span>${accounts.length} captured in ${esc(cluster)}</span><span><kbd>/</kbd> filter</span></div></section>`;
    if (!name || !saNs) {
      return head('RBAC', 'One cluster at a time. Filter the captured ServiceAccounts, then open one.') + toolbar + listed;
    }
    let d;
    try {
      d = await api('api/rbac/serviceaccount', {cluster, namespace: saNs, name});
    } catch (e) {
      return head('RBAC', cluster + ' / ' + saNs + '/' + name) + toolbar + listed + `<div class="notice bad mt">${esc(e.message)}</div>`;
    }
    const title = 'Service account ' + (d.namespace || saNs) + '/' + (d.name || name);
    if (!d.found) {
      return head('RBAC', title) + toolbar + listed + `<div class="notice mt">This ServiceAccount is not in the capture.</div>` + whoPanel(saNs, nsOptions);
    }
    const cov = d.coverage && !d.coverage.complete ? `<div class="notice">${esc(d.coverage.reason || 'RBAC was not fully captured. Absence of a binding is not evidence that the permission is missing.')}</div>` : '';
    const workloads = (d.workloads || []).length ? `<div class="table-scroll"><table><thead><tr><th>Kind</th><th>Name</th><th>Pods</th></tr></thead><tbody>${d.workloads.map(w => `<tr data-rbac-ref="${attr(w.ref)}"><td>${esc(w.ref.kind)}</td><td class="mono">${esc(w.ref.name)}</td><td>${esc(w.pods)}</td></tr>`).join('')}</tbody></table></div>` : '<p class="muted">No captured workload uses this account.</p>';
    // Kubernetes' own bootstrap bindings reach every authenticated identity
    // (API discovery, self access reviews, /version). They are cluster-wide by
    // design and are kept apart so they are not mistaken for grants made to
    // this account.
    const isDefault = g => (g.flags || []).includes('kubernetes-default');
    const nsGrants = (d.grants || []).filter(g => g.scope !== 'cluster');
    const clGrants = (d.grants || []).filter(g => g.scope === 'cluster' && !isDefault(g));
    const defGrants = (d.grants || []).filter(isDefault);
    const own = (d.bindings || []).filter(b => !b.kubernetesDefault), defs = (d.bindings || []).filter(b => b.kubernetesDefault);
    const bindingRow = b => { const extra = (b.roleMissing ? ' role not captured' : '') + (b.invalidRoleRef ? ' roleRef is not valid' : ''); return `<div><button class="link" data-rbac-ref="${attr(b.binding)}">${esc(b.binding.kind)} ${esc(b.binding.name)}</button> <span class="muted">${esc(b.why || '')}${esc(extra)}</span></div>`; };
    const bindings = (own.length ? own.map(bindingRow).join('') : `<p class="muted"><strong>No bindings name this ServiceAccount.</strong> It has no permissions of its own${defs.length ? ', only the Kubernetes defaults below' : ''}.</p>`)
      + (defs.length ? `<details class="mt"><summary>Kubernetes defaults · ${defs.length} binding${defs.length === 1 ? '' : 's'} every authenticated identity gets</summary><p class="muted">These bootstrap ClusterRoleBindings (label <code>kubernetes.io/bootstrapping=rbac-defaults</code>) match the groups <code>system:authenticated</code> or <code>system:serviceaccounts</code>, which include every ServiceAccount in every namespace. They allow API discovery, checking your own access and reading /version. They do not allow reading or changing cluster objects, and they are not something this workload's YAML created.</p>${defs.map(bindingRow).join('')}</details>` : '');
    return head('RBAC', title) + toolbar + listed + cov + `
      <section class="panel"><div class="panel-head"><h2>Workloads</h2></div><div class="panel-body">${workloads}</div></section>
      <section class="panel mt"><div class="panel-head"><h2>Permissions</h2></div><div class="panel-body">
        <h3>In this namespace</h3>${grantRows(nsGrants)}
        <h3 class="mt">Cluster-wide</h3>${clGrants.length ? grantRows(clGrants) : '<p class="muted">None granted to this account.</p>'}
        ${defGrants.length ? `<details class="mt"><summary>Kubernetes defaults · ${defGrants.length} permission${defGrants.length === 1 ? '' : 's'} every authenticated identity has</summary>${grantRows(defGrants)}</details>` : ''}
      </div></section>
      <section class="panel mt"><div class="panel-head"><h2>Bindings</h2></div><div class="panel-body">${bindings}</div></section>
      ${whoPanel(saNs, nsOptions)}
      <section class="panel mt"><div class="panel-head"><h2>Suggestion</h2></div><div class="panel-body">
        <p class="muted">Review text only. Nothing is applied.</p>
        <pre class="code" id="rbac-suggestion">${esc(d.suggestion || '')}</pre>
        <button id="rbac-copy" class="mt">Copy YAML</button>
      </div></section>`;
  }
  function whoPanel(namespace, nsOptions){
    const verbs = ['get', 'list', 'watch', 'create', 'update', 'patch', 'delete', 'deletecollection'];
    return `<section class="panel mt"><div class="panel-head"><h2>Who can</h2></div><div class="panel-body">
      <form id="rbac-who" class="toolbar">
        <select name="verb" aria-label="Verb">${verbs.map(v => `<option ${v === 'get' ? 'selected' : ''}>${v}</option>`).join('')}</select>
        <input name="resource" value="pods" aria-label="Resource" placeholder="resource">
        <select name="namespace" aria-label="Namespace">${(nsOptions || []).map(n => `<option ${n === namespace ? 'selected' : ''}>${esc(n)}</option>`).join('')}</select>
        <input name="group" value="" aria-label="API group" placeholder="API group">
        <input name="subresource" value="" aria-label="Subresource" placeholder="subresource">
        <input name="name" value="" aria-label="Object name" placeholder="object name">
        <button type="submit">Query</button>
      </form>
      <div id="rbac-who-results"></div>
    </div></section>`;
  }
  after.rbac = () => {
    const q = S.rbac || (S.rbac = {});
    const open = (cluster, namespace, name) => nav(saLink(cluster || rbacCluster(), namespace, name));
    const clusterSel = $('#rbac-cluster');
    if (clusterSel) clusterSel.onchange = () => { q.cluster = clusterSel.value; q.listNs = ''; q.name = ''; q.namespace = ''; if (S.cluster && S.cluster !== clusterSel.value) setCluster(clusterSel.value); else render(); };
    const ns = $('#rbac-ns');
    if (ns) ns.onchange = () => { q.listNs = ns.value; render(); };
    const filter = $('#rbac-q');
    if (filter) {
      let t; filter.oninput = e => { clearTimeout(t); t = setTimeout(() => { const c = e.target.selectionStart, v = e.target.value; q.q = v; render().then(() => refocus('#rbac-q', c, v)); }, 200); };
      filter.onkeydown = e => { if (e.key === 'Enter') { e.preventDefault(); const row = document.querySelector('[data-rbac-sa]'); if (row) row.click(); } };
    }
    const reset = $('#rbac-reset');
    if (reset) reset.onclick = () => { q.listNs = ''; q.q = ''; render(); };
    document.querySelectorAll('[data-rbac-sa]').forEach(b => b.onclick = () => { const a = JSON.parse(b.dataset.rbacSa); q.cluster = a.cluster; open(a.cluster, a.namespace, a.name); });
    document.querySelectorAll('[data-rbac-ref]').forEach(el => el.onclick = () => openObject(JSON.parse(el.dataset.rbacRef)));
    const copy = $('#rbac-copy');
    if (copy) copy.onclick = () => { navigator.clipboard.writeText($('#rbac-suggestion').textContent || ''); toast('YAML copied'); };
    const form = $('#rbac-who');
    if (!form) return;
    form.onsubmit = ev => {
      ev.preventDefault();
      const qq = new URLSearchParams(new FormData(form));
      qq.set('cluster', rbacCluster());
      const box = $('#rbac-who-results');
      box.innerHTML = '<p class="muted">Querying…</p>';
      api('api/rbac/who?' + qq).then(ww => {
        const subjects = ww.subjects || [];
        if (!subjects.length) { box.innerHTML = '<p class="muted">No captured subject grants this.</p>' + ((ww.notes || []).length ? '<p class="muted">' + esc(ww.notes.join(' ')) + '</p>' : ''); return; }
        box.innerHTML = `<div class="table-scroll mt"><table><thead><tr><th>Kind</th><th>Namespace</th><th>Name</th><th>Via</th><th>Bindings</th></tr></thead><tbody>${subjects.map(su => `<tr><td>${esc(su.kind)}</td><td>${esc(su.namespace)}</td><td class="mono">${esc(su.name)}</td><td>${esc(su.via)}</td><td>${esc((su.bindings || []).map(b => b.name).join(', '))}</td></tr>`).join('')}</tbody></table></div>` + ((ww.notes || []).length ? '<p class="muted">' + esc(ww.notes.join(' ')) + '</p>' : '');
      }).catch(e => { box.innerHTML = '<div class="notice bad">' + esc(e.message) + '</div>'; });
    };
  };
  function rbacWireObject(o){
    if (!o || o.kind !== 'ServiceAccount') return;
    const desc = document.getElementById('d-describe');
    if (!desc) return;
    const b = document.createElement('button');
    b.textContent = 'Service account RBAC';
    b.onclick = function(){
      closeDrawer();
      nav(saLink(o.cluster || S.cluster || '', o.namespace || '', o.name || ''));
    };
    desc.parentNode.insertBefore(b, desc.nextSibling);
  }
  window.rbacWireObject = rbacWireObject;
  window.rbacPage = rbacPage;
})();
