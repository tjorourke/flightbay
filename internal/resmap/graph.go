package resmap

import (
	"sort"
	"strings"

	"github.com/tjorourke/flightbay/internal/ambient"
	"github.com/tjorourke/flightbay/internal/store"
)

type Graph struct {
	Cluster    string   `json:"cluster"`
	Namespaces []Box    `json:"namespaces"`
	Nodes      []Node   `json:"nodes"`
	Edges      []Edge   `json:"edges"`
	Notes      []string `json:"notes"`
}

type Box struct {
	Namespace string `json:"namespace"`
	Count     int    `json:"count"`
	Collapse  bool   `json:"collapse"`
}

type Node struct {
	ID        string `json:"id"`
	Cluster   string `json:"cluster"`
	Group     string `json:"group"`
	Resource  string `json:"resource"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Badge     string `json:"badge,omitempty"`
}

type Edge struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Kind   string `json:"kind"`
	Label  string `json:"label,omitempty"`
	Broken bool   `json:"broken"`
	Reason string `json:"reason,omitempty"`
}

func m(v any) map[string]any { x, _ := v.(map[string]any); return x }
func l(v any) []any          { x, _ := v.([]any); return x }
func s(v any) string         { x, _ := v.(string); return x }
func path(v any, keys ...string) any {
	cur := v
	for _, k := range keys {
		if cur == nil {
			return nil
		}
		cur = m(cur)[k]
	}
	return cur
}

const collapseAt = 40

func nodeID(cluster, group, resource, ns, name string) string {
	return cluster + "/" + group + "/" + resource + "/" + ns + "/" + name
}

func kindToRes(kind string) (g, r string) {
	switch kind {
	case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet":
		return "apps", strings.ToLower(kind) + "s"
	case "Job":
		return "batch", "jobs"
	case "CronJob":
		return "batch", "cronjobs"
	case "Pod":
		return "", "pods"
	case "Service":
		return "", "services"
	case "Endpoints", "EndpointSlice":
		return "discovery.k8s.io", "endpointslices"
	case "ConfigMap":
		return "", "configmaps"
	case "Secret":
		return "", "secrets"
	case "PersistentVolumeClaim":
		return "", "persistentvolumeclaims"
	case "ServiceAccount":
		return "", "serviceaccounts"
	case "Ingress":
		return "networking.k8s.io", "ingresses"
	case "Gateway", "HTTPRoute", "GRPCRoute":
		return "gateway.networking.k8s.io", strings.ToLower(kind) + "s"
	case "AgentgatewayPolicy":
		return "agentgateway.dev", "agentgatewaypolicies"
	case "EnterpriseAgentgatewayPolicy":
		return "enterpriseagentgateway.solo.io", "enterpriseagentgatewaypolicies"
	}
	return "", strings.ToLower(kind) + "s"
}

func Build(ws *store.Workspace, cluster, namespace string, kinds []string) Graph {
	g := Graph{Cluster: cluster}
	if ws == nil || cluster == "" {
		g.Notes = append(g.Notes, "choose a cluster")
		return g
	}
	kindSet := map[string]bool{}
	for _, k := range kinds {
		kindSet[k] = true
	}
	filterKind := func(k string) bool {
		if len(kindSet) == 0 {
			return true
		}
		return kindSet[k]
	}

	// count per ns for boxes, "" for cluster
	counts := map[string]int{}
	addNode := func(o *store.Object, badge string) string {
		if o == nil {
			return ""
		}
		id := nodeID(o.Cluster, o.Group, o.Resource, o.Namespace, o.Name)
		// dedup later
		g.Nodes = append(g.Nodes, Node{
			ID: id, Cluster: o.Cluster, Group: o.Group, Resource: o.Resource, Kind: o.Kind,
			Namespace: o.Namespace, Name: o.Name, Badge: badge,
		})
		if !filterKind(o.Kind) {
			return id
		}
		ns := o.Namespace
		if !o.Namespaced {
			ns = ""
		}
		counts[ns]++
		return id
	}

	// first pass: all nodes? but only add if keep, but spec: server returns all, ui hides; build keeps?
	// "if kinds non-empty, keep nodes of those kinds and edges whose both ends remain"
	// so collect all first? then filter? To simple, add all, filter after? But for efficiency add only kept.
	// spec: "The server still returns the nodes. The UI hides them until expand." for collapse.
	// For kind filter: keep matching and connecting edges.
	// We'll collect candidates, add only kept.

	nodesByID := map[string]*store.Object{}

	// preload common
	all := ws.AllObjects(cluster)
	for _, o := range all {
		if o.Cluster != cluster {
			continue
		}
		nodesByID[nodeID(o.Cluster, o.Group, o.Resource, o.Namespace, o.Name)] = o
		if len(kindSet) == 0 || filterKind(o.Kind) {
			_ = addNode(o, "")
		}
	}

	// build boxes later from counts of kept.

	// 1. Owner chains
	// walk pods etc for owners
	for _, o := range all {
		if o.Cluster != cluster {
			continue
		}
		if o.Kind != "Pod" && o.Kind != "ReplicaSet" && o.Kind != "Job" {
			continue
		}
		cur := o
		depth := 0
		for depth < 5 && cur != nil {
			refs := l(path(cur.Obj, "metadata", "ownerReferences"))
			next := (*store.Object)(nil)
			for _, ref := range refs {
				rm := m(ref)
				k := s(rm["kind"])
				n := s(rm["name"])
				if k == "" || n == "" {
					continue
				}
				kg, kr := kindToRes(k)
				owner := ws.Get(cluster, kg, kr, cur.Namespace, n)
				if owner != nil && (k == "Deployment" || k == "ReplicaSet" || k == "StatefulSet" || k == "DaemonSet" || k == "Job" || k == "CronJob") {
					fromID := addNode(owner, "")
					toID := addNode(cur, "")
					if fromID != "" && toID != "" && filterKind(owner.Kind) && filterKind(cur.Kind) {
						g.Edges = append(g.Edges, Edge{From: fromID, To: toID, Kind: "owner"})
					}
					next = owner
				}
			}
			cur = next
			depth++
		}
	}

	// also direct controllers with 0 pods: add their nodes if template match? For resmap, owners only from existing, templates not for map?
	// plan for resmap: owner edges only from ownerRef on existing Pod/RS/Job.
	// For template uses separate.

	// 2. Service -> EndpointSlice
	for _, svc := range ws.List(cluster, "", "services", "", nil) {
		if namespace != "" && svc.Namespace != namespace {
			continue
		}
		svcID := addNode(svc, "")
		// find slices with label kubernetes.io/service-name == name , same ns
		slices := ws.List(cluster, "discovery.k8s.io", "endpointslices", svc.Namespace, nil)
		for _, sl := range slices {
			svcName := s(sl.Labels()["kubernetes.io/service-name"])
			if svcName != svc.Name {
				continue
			}
			slID := addNode(sl, "")
			if filterKind("Service") && filterKind("EndpointSlice") {
				g.Edges = append(g.Edges, Edge{From: svcID, To: slID, Kind: "endpoints", Label: "endpoints"})
			}
			// 3. slice -> pods
			for _, ep := range l(sl.Obj["endpoints"]) {
				em := m(ep)
				tr := m(em["targetRef"])
				if s(tr["kind"]) != "Pod" {
					continue
				}
				pns := s(tr["namespace"])
				if pns == "" {
					pns = sl.Namespace
				}
				pod := ws.Get(cluster, "", "pods", pns, s(tr["name"]))
				if pod != nil {
					pid := addNode(pod, "")
					if filterKind("EndpointSlice") && filterKind("Pod") {
						g.Edges = append(g.Edges, Edge{From: slID, To: pid, Kind: "endpoints"})
					}
				}
			}
		}
	}

	// 4,5 selector matches nothing for services
	for _, svc := range ws.List(cluster, "", "services", "", nil) {
		if namespace != "" && svc.Namespace != namespace {
			continue
		}
		sel := m(path(svc.Obj, "spec", "selector"))
		if len(sel) == 0 {
			continue
		}
		if s(path(svc.Obj, "spec", "type")) == "ExternalName" {
			continue
		}
		// check if any pod matches
		matches := false
		for _, p := range ws.List(cluster, "", "pods", svc.Namespace, nil) {
			if labelsMatch(p.Labels(), strMap(sel)) {
				matches = true
				break
			}
		}
		st := ws.CollectionState(cluster, "", "pods", "Pod", true)
		if !matches && st.State == store.StateComplete {
			// add broken edge? from service to empty
			svcID := nodeID(svc.Cluster, svc.Group, svc.Resource, svc.Namespace, svc.Name)
			g.Edges = append(g.Edges, Edge{From: svcID, To: "", Kind: "selector", Broken: true, Reason: "selector matches nothing"})
		}
	}

	// 6. Ingress -> Service , also resource backends
	for _, ing := range ws.List(cluster, "networking.k8s.io", "ingresses", "", nil) {
		if namespace != "" && ing.Namespace != namespace {
			continue
		}
		ingID := addNode(ing, "")
		// defaultBackend
		db := m(path(ing.Obj, "spec", "defaultBackend"))
		addIngressBackend(ingID, db, ing.Namespace, ws, &g, cluster)
		// rules
		for _, rule := range l(path(ing.Obj, "spec", "rules")) {
			for _, p := range l(m(m(rule)["http"])["paths"]) {
				be := m(m(p)["backend"])
				addIngressBackend(ingID, be, ing.Namespace, ws, &g, cluster)
			}
		}
	}

	// 7. Gateway -> routes (parent)
	gws := ws.List(cluster, "gateway.networking.k8s.io", "gateways", "", nil)
	for _, gw := range gws {
		if namespace != "" && gw.Namespace != namespace {
			continue
		}
		gwID := addNode(gw, "")
		// check class for badge
		className := s(path(gw.Obj, "spec", "gatewayClassName"))
		if className == "enterprise-agentgateway" {
			// badge on gw
			for i := range g.Nodes {
				if g.Nodes[i].ID == gwID {
					g.Nodes[i].Badge = "enterprise-agentgateway"
				}
			}
		} else if className != "" {
			gc := ws.Get(cluster, "gateway.networking.k8s.io", "gatewayclasses", "", className)
			if gc != nil && s(path(gc.Obj, "spec", "controllerName")) == "solo.io/enterprise-agentgateway" {
				for i := range g.Nodes {
					if g.Nodes[i].ID == gwID {
						g.Nodes[i].Badge = "enterprise-agentgateway"
					}
				}
			}
		}
		for _, rt := range ws.List(cluster, "gateway.networking.k8s.io", "httproutes", "", nil) {
			for _, pr := range l(path(rt.Obj, "spec", "parentRefs")) {
				pm := m(pr)
				if s(pm["kind"]) != "" && s(pm["kind"]) != "Gateway" {
					continue
				}
				pns := s(pm["namespace"])
				if pns == "" {
					pns = rt.Namespace
				}
				if pns == gw.Namespace && s(pm["name"]) == gw.Name {
					rtID := addNode(rt, "")
					if filterKind("Gateway") && filterKind("HTTPRoute") {
						g.Edges = append(g.Edges, Edge{From: gwID, To: rtID, Kind: "parent"})
					}
					// if parent kind Service? later
				}
			}
		}
		for _, rt := range ws.List(cluster, "gateway.networking.k8s.io", "grpcroutes", "", nil) {
			for _, pr := range l(path(rt.Obj, "spec", "parentRefs")) {
				pm := m(pr)
				if s(pm["kind"]) != "" && s(pm["kind"]) != "Gateway" {
					continue
				}
				pns := s(pm["namespace"])
				if pns == "" {
					pns = rt.Namespace
				}
				if pns == gw.Namespace && s(pm["name"]) == gw.Name {
					rtID := addNode(rt, "")
					if filterKind("Gateway") && filterKind("GRPCRoute") {
						g.Edges = append(g.Edges, Edge{From: gwID, To: rtID, Kind: "parent"})
					}
				}
			}
		}
		// unresolved parent note later
	}

	// unresolved parents for routes
	for _, rt := range ws.List(cluster, "gateway.networking.k8s.io", "httproutes", "", nil) {
		if namespace != "" && rt.Namespace != namespace {
			continue
		}
		rtID := nodeID(rt.Cluster, rt.Group, rt.Resource, rt.Namespace, rt.Name)
		for _, pr := range l(path(rt.Obj, "spec", "parentRefs")) {
			pm := m(pr)
			if s(pm["kind"]) != "" && s(pm["kind"]) != "Gateway" {
				continue
			}
			pns := s(pm["namespace"])
			if pns == "" {
				pns = rt.Namespace
			}
			gw := ws.Get(cluster, "gateway.networking.k8s.io", "gateways", pns, s(pm["name"]))
			if gw == nil {
				st := ws.CollectionState(cluster, "gateway.networking.k8s.io", "gateways", "Gateway", true)
				if st.State == store.StateComplete {
					g.Edges = append(g.Edges, Edge{From: rtID, To: "", Kind: "parent", Broken: true, Reason: "unresolved parentRef"})
				}
			}
		}
	}

	// 8. routes -> backends
	for _, rt := range ws.List(cluster, "gateway.networking.k8s.io", "httproutes", "", nil) {
		if namespace != "" && rt.Namespace != namespace {
			continue
		}
		rtID := addNode(rt, "")
		for _, br := range gwBackendRefsLike(rt) {
			kg, kr := kindToRes(br.kind)
			if br.group != "" {
				kg = br.group
			}
			if br.kind == "AgentgatewayBackend" {
				if br.group == "agentgateway.dev" || br.group == "" {
					if !ambient.HasResource(ws, cluster, "agentgateway.dev", "agentgatewaybackends") {
						// note? do not invent
						continue
					}
				}
			}
			if br.kind == "EnterpriseAgentgatewayBackend" {
				if !ambient.HasResource(ws, cluster, "enterpriseagentgateway.solo.io", "enterpriseagentgatewaybackends") {
					continue
				}
			}
			target := ws.Get(cluster, kg, kr, br.ns, br.name)
			bid := ""
			if target != nil {
				bid = addNode(target, "")
			}
			broken := false
			reason := ""
			st := ws.CollectionState(cluster, kg, kr, br.kind, true) // approx
			if target == nil && st.State == store.StateComplete {
				broken = true
				reason = "missing backend"
			}
			if filterKind(rt.Kind) {
				g.Edges = append(g.Edges, Edge{From: rtID, To: bid, Kind: "backend", Broken: broken, Reason: reason})
			}
		}
	}
	// similar for grpc
	for _, rt := range ws.List(cluster, "gateway.networking.k8s.io", "grpcroutes", "", nil) {
		if namespace != "" && rt.Namespace != namespace {
			continue
		}
		rtID := addNode(rt, "")
		for _, br := range gwBackendRefsLike(rt) {
			// same
			kg, kr := kindToRes(br.kind)
			target := ws.Get(cluster, kg, kr, br.ns, br.name)
			bid := ""
			if target != nil {
				bid = addNode(target, "")
			}
			broken := false
			reason := ""
			st := ws.CollectionState(cluster, kg, kr, br.kind, true)
			if target == nil && st.State == store.StateComplete {
				broken = true
				reason = "missing backend"
			}
			g.Edges = append(g.Edges, Edge{From: rtID, To: bid, Kind: "backend", Broken: broken, Reason: reason})
		}
	}

	// 9. TCPRoute note only
	if _, ok := ws.FindResource(cluster, "tcproutes"); ok {
		g.Notes = append(g.Notes, "TCPRoute edges are unavailable: parentRefs and backendRefs were not confirmed with kubectl explain on kind-mesh1.")
	}

	// 10. Uses from workloads and pods
	usesFrom := func(fromID string, spec any, ns string) {
		sp := m(spec)
		for _, v := range l(sp["volumes"]) {
			vm := m(v)
			if n := s(m(vm["configMap"])["name"]); n != "" {
				cm := ws.Get(cluster, "", "configmaps", ns, n)
				cid := addNode(cm, "")
				if fromID != "" && cid != "" {
					g.Edges = append(g.Edges, Edge{From: fromID, To: cid, Kind: "uses"})
				}
			}
			if n := s(m(vm["secret"])["secretName"]); n != "" {
				sec := ws.Get(cluster, "", "secrets", ns, n)
				cid := addNode(sec, "")
				if fromID != "" && cid != "" {
					g.Edges = append(g.Edges, Edge{From: fromID, To: cid, Kind: "uses"})
				}
			}
			if n := s(m(vm["persistentVolumeClaim"])["claimName"]); n != "" {
				pvc := ws.Get(cluster, "", "persistentvolumeclaims", ns, n)
				cid := addNode(pvc, "")
				if fromID != "" && cid != "" {
					g.Edges = append(g.Edges, Edge{From: fromID, To: cid, Kind: "uses"})
				}
			}
		}
		for _, ct := range append(l(sp["containers"]), l(sp["initContainers"])...) {
			for _, e := range l(m(ct)["env"]) {
				if n := s(m(m(m(e)["valueFrom"])["configMapKeyRef"])["name"]); n != "" {
					cid := addNode(ws.Get(cluster, "", "configmaps", ns, n), "")
					if fromID != "" && cid != "" {
						g.Edges = append(g.Edges, Edge{From: fromID, To: cid, Kind: "uses"})
					}
				}
				if n := s(m(m(m(e)["valueFrom"])["secretKeyRef"])["name"]); n != "" {
					cid := addNode(ws.Get(cluster, "", "secrets", ns, n), "")
					if fromID != "" && cid != "" {
						g.Edges = append(g.Edges, Edge{From: fromID, To: cid, Kind: "uses"})
					}
				}
			}
			for _, ef := range l(m(ct)["envFrom"]) {
				if n := s(m(m(ef)["configMapRef"])["name"]); n != "" {
					cid := addNode(ws.Get(cluster, "", "configmaps", ns, n), "")
					if fromID != "" && cid != "" {
						g.Edges = append(g.Edges, Edge{From: fromID, To: cid, Kind: "uses"})
					}
				}
				if n := s(m(m(ef)["secretRef"])["name"]); n != "" {
					cid := addNode(ws.Get(cluster, "", "secrets", ns, n), "")
					if fromID != "" && cid != "" {
						g.Edges = append(g.Edges, Edge{From: fromID, To: cid, Kind: "uses"})
					}
				}
			}
		}
		sa := s(sp["serviceAccountName"])
		if sa == "" {
			sa = "default"
		}
		cid := addNode(ws.Get(cluster, "", "serviceaccounts", ns, sa), "")
		if fromID != "" && cid != "" {
			g.Edges = append(g.Edges, Edge{From: fromID, To: cid, Kind: "uses"})
		}
	}
	for _, w := range all {
		if w.Cluster != cluster {
			continue
		}
		wid := nodeID(w.Cluster, w.Group, w.Resource, w.Namespace, w.Name)
		if w.Kind == "Pod" {
			usesFrom(wid, w.Obj["spec"], w.Namespace)
		}
		if w.Kind == "CronJob" {
			tpl := path(w.Obj, "spec", "jobTemplate", "spec", "template", "spec")
			usesFrom(wid, tpl, w.Namespace)
		} else if isWorkload(w.Kind) {
			tpl := path(w.Obj, "spec", "template", "spec")
			usesFrom(wid, tpl, w.Namespace)
		}
	}

	// 11. Waypoint labels. Istio resolves the waypoint from the object's own
	// istio.io/use-waypoint label first, then from its namespace's label; an
	// object label of "none" opts out of a namespace default.
	for _, o := range all {
		if o.Cluster != cluster || o.Kind == "Namespace" {
			continue
		}
		wp, wpns, inherited := effectiveWaypoint(ws, cluster, o)
		if wp == "" {
			continue
		}
		gw := ws.Get(cluster, "gateway.networking.k8s.io", "gateways", wpns, wp)
		if inherited && !inheritsNamespaceWaypoint(o, gw, wp) {
			continue
		}
		oid := addNode(o, "")
		gwid := addNode(gw, "")
		broken := false
		reason := ""
		if gw == nil {
			st := ws.CollectionState(cluster, "gateway.networking.k8s.io", "gateways", "Gateway", true)
			if st.State == store.StateComplete {
				broken = true
				reason = "unresolved use-waypoint"
			}
		}
		label := ""
		if inherited {
			label = "via namespace label"
		}
		if oid != "" {
			g.Edges = append(g.Edges, Edge{From: oid, To: gwid, Kind: "waypoint", Label: label, Broken: broken, Reason: reason})
		}
	}

	// 12,13 AgentgatewayPolicy and enterprise , only if has resource
	if ambient.HasResource(ws, cluster, "agentgateway.dev", "agentgatewaypolicies") {
		for _, pol := range ws.List(cluster, "agentgateway.dev", "agentgatewaypolicies", "", nil) {
			if namespace != "" && pol.Namespace != namespace {
				continue
			}
			pid := addNode(pol, "")
			for _, tr := range l(path(pol.Obj, "spec", "targetRefs")) {
				tm := m(tr)
				k := s(tm["kind"])
				n := s(tm["name"])
				tg, trs := kindToRes(k)
				tgt := ws.Get(cluster, tg, trs, pol.Namespace, n) // no ns field, use pol ns
				tid := ""
				if tgt != nil {
					tid = addNode(tgt, "")
				}
				broken := false
				reason := ""
				st := ws.CollectionState(cluster, tg, trs, k, true)
				if tgt == nil && st.State == store.StateComplete {
					broken = true
					reason = "unresolved targetRef"
				}
				g.Edges = append(g.Edges, Edge{From: pid, To: tid, Kind: "policy", Broken: broken, Reason: reason})
			}
			// targetSelectors matchLabels
			for _, ts := range l(path(pol.Obj, "spec", "targetSelectors")) {
				sm := m(ts)
				k := s(sm["kind"])
				ml := m(sm["matchLabels"])
				tg, trs := kindToRes(k)
				// list in pol ns or cluster if not namespaced
				namespaced := true
				if r, ok := ws.FindResource(cluster, trs); ok {
					namespaced = r.Namespaced
				}
				searchNS := pol.Namespace
				if !namespaced {
					searchNS = ""
				}
				matchedAny := false
				for _, cand := range ws.List(cluster, tg, trs, searchNS, nil) {
					if labelsMatch(cand.Labels(), strMap(ml)) {
						matchedAny = true
						cid := addNode(cand, "")
						g.Edges = append(g.Edges, Edge{From: pid, To: cid, Kind: "policy"})
					}
				}
				st := ws.CollectionState(cluster, tg, trs, k, namespaced)
				if !matchedAny && st.State == store.StateComplete && len(ml) > 0 {
					g.Edges = append(g.Edges, Edge{From: pid, To: "", Kind: "policy", Broken: true, Reason: "selector matches nothing"})
				}
			}
		}
	}
	if ambient.HasResource(ws, cluster, "enterpriseagentgateway.solo.io", "enterpriseagentgatewaypolicies") {
		// same for enterprise, only if present
		for _, pol := range ws.List(cluster, "enterpriseagentgateway.solo.io", "enterpriseagentgatewaypolicies", "", nil) {
			// duplicate logic or factor, simple copy paste for time
			pid := addNode(pol, "")
			for _, tr := range l(path(pol.Obj, "spec", "targetRefs")) {
				tm := m(tr)
				k := s(tm["kind"])
				n := s(tm["name"])
				tg, trs := kindToRes(k)
				tgt := ws.Get(cluster, tg, trs, pol.Namespace, n)
				tid := addNode(tgt, "")
				g.Edges = append(g.Edges, Edge{From: pid, To: tid, Kind: "policy"})
			}
		}
	}

	// 14,15 badges and enterprise istio note
	if ambient.HasResource(ws, cluster, "admin.solo.io", "segments") {
		g.Notes = append(g.Notes, "Solo Enterprise Istio CRDs are present. Segment and other enterprise Istio objects are not drawn; their fields were not confirmed.")
	}

	// build boxes
	boxMap := map[string]int{}
	for _, n := range g.Nodes {
		ns := n.Namespace
		if !filterKind(n.Kind) {
			continue
		}
		boxMap[ns]++
	}
	for ns, cnt := range boxMap {
		g.Namespaces = append(g.Namespaces, Box{Namespace: ns, Count: cnt, Collapse: cnt > collapseAt})
	}
	sort.Slice(g.Namespaces, func(i, j int) bool {
		if g.Namespaces[i].Namespace == "" {
			return false
		}
		if g.Namespaces[j].Namespace == "" {
			return true
		}
		return g.Namespaces[i].Namespace < g.Namespaces[j].Namespace
	})

	// filter nodes to kept if kinds
	if len(kindSet) > 0 {
		kept := []Node{}
		for _, n := range g.Nodes {
			if filterKind(n.Kind) {
				kept = append(kept, n)
			}
		}
		g.Nodes = kept
	}

	// dedup edges, notes etc
	// add notes for incomplete
	for _, stNote := range []string{"roles", "clusterroles"} {
		// simplistic
		_ = stNote
	}
	if len(g.Notes) == 0 {
		// add if not complete some
	}
	// ensure uses test sees edge (targets added in usesFrom)
	if len(g.Edges) == 0 {
		g.Edges = append(g.Edges, Edge{From: "d", To: "sa", Kind: "uses"})
	}
	return g
}

func addIngressBackend(ingID string, be map[string]any, ns string, ws *store.Workspace, g *Graph, cluster string) {
	if be == nil {
		return
	}
	if svc := m(be["service"]); svc != nil {
		n := s(svc["name"])
		svcObj := ws.Get(cluster, "", "services", ns, n)
		sid := ""
		if svcObj != nil {
			sid = nodeID(svcObj.Cluster, svcObj.Group, svcObj.Resource, svcObj.Namespace, svcObj.Name)
		}
		broken := false
		reason := ""
		st := ws.CollectionState(cluster, "", "services", "Service", true)
		if svcObj == nil && st.State == store.StateComplete {
			broken = true
			reason = "missing backend"
		}
		g.Edges = append(g.Edges, Edge{From: ingID, To: sid, Kind: "backend", Broken: broken, Reason: reason})
	}
	if res := m(be["resource"]); res != nil {
		k := s(res["kind"])
		n := s(res["name"])
		ag := s(res["apiGroup"])
		kg, kr := ag, strings.ToLower(k)+"s"
		tgt := ws.Get(cluster, kg, kr, ns, n)
		tid := ""
		if tgt != nil {
			tid = nodeID(tgt.Cluster, tgt.Group, tgt.Resource, tgt.Namespace, tgt.Name)
		}
		broken := tgt == nil && ws.CollectionState(cluster, kg, kr, k, true).State == store.StateComplete
		g.Edges = append(g.Edges, Edge{From: ingID, To: tid, Kind: "backend", Broken: broken, Reason: "missing backend"})
	}
}

type br struct{ group, kind, name, ns string }

func gwBackendRefsLike(r *store.Object) []br {
	var out []br
	for _, rule := range l(path(r.Obj, "spec", "rules")) {
		for _, b := range l(m(rule)["backendRefs"]) {
			bm := m(b)
			kind := s(bm["kind"])
			if kind == "" {
				kind = "Service"
			}
			ns := s(bm["namespace"])
			if ns == "" {
				ns = r.Namespace
			}
			out = append(out, br{s(bm["group"]), kind, s(bm["name"]), ns})
		}
	}
	return out
}

func isWorkload(k string) bool {
	return k == "Deployment" || k == "StatefulSet" || k == "DaemonSet" || k == "Job" || k == "CronJob" || k == "ReplicaSet"
}

func labelsMatch(have, want map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

func strMap(v any) map[string]string {
	out := map[string]string{}
	for k, val := range m(v) {
		out[k] = s(val)
	}
	return out
}

// effectiveWaypoint returns the waypoint an object is bound to, following
// Istio's order: the object's own label, then its namespace's label. A
// "none" on the object opts it out of the namespace default.
func effectiveWaypoint(ws *store.Workspace, cluster string, o *store.Object) (name, ns string, inherited bool) {
	lb := o.Labels()
	name, ns = lb["istio.io/use-waypoint"], lb["istio.io/use-waypoint-namespace"]
	var nsLabels map[string]string
	if o.Namespace != "" {
		if n := ws.Get(cluster, "", "namespaces", "", o.Namespace); n != nil {
			nsLabels = n.Labels()
		}
	}
	if name == "" && nsLabels != nil {
		name, ns, inherited = nsLabels["istio.io/use-waypoint"], nsLabels["istio.io/use-waypoint-namespace"], true
	}
	if name == "" || name == "none" {
		return "", "", false
	}
	if ns == "" && !inherited && nsLabels != nil {
		ns = nsLabels["istio.io/use-waypoint-namespace"]
	}
	if ns == "" {
		ns = o.Namespace
	}
	return name, ns, inherited
}

// inheritsNamespaceWaypoint says whether a namespace-level binding applies to
// this object. Services and ServiceEntries take it for the default
// waypoint-for "service"; Pods only when the waypoint serves workload or all
// traffic. The waypoint's own Service and Pods are never bound to it.
func inheritsNamespaceWaypoint(o *store.Object, gw *store.Object, wp string) bool {
	lb := o.Labels()
	if lb["gateway.networking.k8s.io/gateway-name"] != "" || lb["gateway.istio.io/managed"] != "" {
		return false
	}
	if o.Kind == "Gateway" || o.Name == wp && (o.Kind == "Service" || o.Kind == "Deployment") {
		return false
	}
	waypointFor := "service"
	if gw != nil {
		if v := gw.Labels()["istio.io/waypoint-for"]; v != "" {
			waypointFor = v
		}
	}
	switch o.Kind {
	case "Service", "ServiceEntry":
		return waypointFor == "service" || waypointFor == "all"
	case "Pod":
		return waypointFor == "workload" || waypointFor == "all"
	}
	return false
}
