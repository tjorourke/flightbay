package ambient

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/tjorourke/flightbay/internal/store"
)

type GraphNode struct {
	Ref    Ref      `json:"ref"`
	State  string   `json:"state"`
	Exists bool     `json:"exists"`
	Notes  []string `json:"notes,omitempty"`
}
type GraphGroup struct {
	Title    string      `json:"title"`
	Relation string      `json:"relation"`
	Nodes    []GraphNode `json:"nodes"`
}
type Graph struct {
	Root   Ref          `json:"root"`
	Groups []GraphGroup `json:"groups"`
}
type Editable struct {
	Ref       Ref      `json:"ref"`
	Rule      int      `json:"rule"`
	Supported bool     `json:"supported"`
	Reason    string   `json:"reason"`
	Request   *Request `json:"request,omitempty"`
}
type PodChoice struct {
	Ref
	Labels   map[string]string `json:"labels"`
	Ready    bool              `json:"ready"`
	Selected bool              `json:"selected"`
	Ports    []int             `json:"ports"`
	Detail   string            `json:"detail"`
}
type WorkloadChoice struct {
	Ref
	Replicas int `json:"replicas"`
}
type Recovery struct {
	EndpointState string           `json:"endpointState"`
	Detail        string           `json:"detail"`
	Pods          []PodChoice      `json:"pods"`
	Workloads     []WorkloadChoice `json:"workloads"`
	Command       string           `json:"command"`
}
type View struct {
	*Investigation
	Subject  Ref        `json:"subject"`
	Related  []Ref      `json:"related"`
	Graph    Graph      `json:"graph"`
	Editable []Editable `json:"editable"`
	Recovery *Recovery  `json:"recovery,omitempty"`
}

func expandInventory(ws *store.Workspace, r *Inventory) {
	r.Objects = []Ref{}
	r.Waypoints = []WaypointChoice{}
	r.GatewayClasses = []string{}
	for _, c := range ws.Clusters() {
		for _, x := range []struct{ g, res string }{{"", "namespaces"}, {"", "services"}, {"", "pods"}, {GatewayGroup, "gateways"}, {GatewayGroup, "httproutes"}, {"networking.istio.io", "serviceentries"}} {
			for _, o := range ws.List(c, x.g, x.res, "", nil) {
				r.Objects = append(r.Objects, reference(o))
				if o.Kind == "Gateway" && strings.Contains(S(Path(o.Obj, "spec", "gatewayClassName")), "waypoint") {
					cls := S(Path(o.Obj, "spec", "gatewayClassName"))
					r.Waypoints = append(r.Waypoints, WaypointChoice{reference(o), cls, defaultString(o.Labels()["istio.io/waypoint-for"], "service")})
					if !contains(r.GatewayClasses, cls) {
						r.GatewayClasses = append(r.GatewayClasses, cls)
					}
				}
			}
		}
		for _, o := range ws.List(c, GatewayGroup, "gatewayclasses", "", nil) {
			if strings.Contains(o.Name, "waypoint") && !contains(r.GatewayClasses, o.Name) {
				r.GatewayClasses = append(r.GatewayClasses, o.Name)
			}
		}
	}
	for _, cls := range []string{"istio-waypoint", "agentgateway-waypoint", "enterprise-agentgateway-waypoint"} {
		if !contains(r.GatewayClasses, cls) {
			r.GatewayClasses = append(r.GatewayClasses, cls)
		}
	}
	sort.Slice(r.Objects, func(i, j int) bool {
		a, b := r.Objects[i], r.Objects[j]
		return a.Kind+"/"+a.Cluster+"/"+a.Namespace+"/"+a.Name < b.Kind+"/"+b.Cluster+"/"+b.Namespace+"/"+b.Name
	})
}
func NewView(ws *store.Workspace, c, kind, ns, name, route string) (*View, error) {
	kind = defaultString(kind, "Service")
	o := Target(ws, c, kind, ns, name)
	if o == nil {
		return nil, fmt.Errorf("%s %s/%s was not captured", kind, ns, name)
	}
	var r *Investigation
	var err error
	if kind == "Service" {
		r, err = Investigate(ws, c, ns, name, route)
		if err != nil {
			return nil, err
		}
	} else {
		r = investigateObject(ws, o)
	}
	v := &View{Investigation: r, Subject: reference(o), Related: []Ref{}, Editable: []Editable{}, Graph: Graph{Root: reference(o), Groups: []GraphGroup{}}}
	if kind == "Service" {
		v.Recovery = recovery(ws, o)
		v.Graph = serviceGraph(ws, o, r)
		v.Editable = editableConfigurations(ws, o)
		for i := range r.Steps {
			s := &r.Steps[i]
			switch {
			case s.ID == "waypoint" && (s.State == "fail" || s.State == "warning" || s.State == "unknown"):
				s.Actions = append(s.Actions, Remediation{"waypoint", "Set up or select a waypoint", "Bind this Service, its namespace, or selected shared namespaces.", nil})
			case s.ID == "endpoints" && (s.State == "fail" || s.State == "unknown"):
				s.Actions = append(s.Actions, Remediation{"endpoints", "Investigate and fix endpoints", "Check collection scope, intended workload selector, replica count and target port before choosing a configuration change.", nil})
			}
			if s.FixYAML != "" {
				for _, e := range s.Evidence {
					if e.Ref.Kind == "HTTPRoute" {
						ref := e.Ref
						s.Actions = append(s.Actions, Remediation{"route-parent", "Fix parent in wizard", "Review the selected frontend Service and parent entry before generating a scoped change.", &ref})
						break
					}
				}
			}
		}
	}
	for _, g := range v.Graph.Groups {
		for _, n := range g.Nodes {
			if n.Exists {
				v.Related = append(v.Related, n.Ref)
			}
		}
	}
	if kind != "Service" {
		for _, s := range ws.List(c, "", "services", "", nil) {
			match := false
			switch kind {
			case "Namespace":
				match = s.Namespace == name
			case "Pod":
				match = s.Namespace == ns && len(M(Path(s.Obj, "spec", "selector"))) > 0 && matches(M(Path(s.Obj, "spec", "selector")), o.Labels())
			case "Gateway":
				wn, wp, _ := Waypoint(ws, s)
				match = wn == ns && wp == name
			case "HTTPRoute":
				for _, p := range L(Path(o.Obj, "spec", "parentRefs")) {
					g, k, pns, pn := parent(M(p), ns)
					if g == "" && k == "Service" && pns == s.Namespace && pn == s.Name {
						match = true
					}
				}
			}
			if match {
				v.Related = append(v.Related, reference(s))
			}
		}
		nodes := []GraphNode{}
		for _, ref := range v.Related {
			nodes = append(nodes, GraphNode{Ref: ref, State: "unknown", Exists: true})
		}
		v.Graph.Groups = append(v.Graph.Groups, GraphGroup{"Related Services", "Inspect this scope's Service-specific routing and policies", nodes})
	}
	return v, nil
}
func investigateObject(ws *store.Workspace, o *store.Object) *Investigation {
	ns := o.Namespace
	if o.Kind == "Namespace" {
		ns = o.Name
	}
	r := &Investigation{Cluster: o.Cluster, Namespace: ns, Steps: []Step{}, Counts: map[string]int{}, Notes: []string{"Namespace, Service, Pod, Gateway, HTTPRoute and ServiceEntry are different ambient entry points. Follow a related Service for Service-scoped traffic controls.", "A shared waypoint has explicitly enrolled namespaces or objects; Istio has no automatic global waypoint default."}}
	switch o.Kind {
	case "Namespace":
		mode := o.Labels()["istio.io/dataplane-mode"]
		state := "pass"
		if mode != "ambient" {
			state = "warning"
		}
		add(r, "enrolment", "Namespace ambient enrolment", state, "Effective namespace label: "+defaultString(mode, "not set"), evidence(o, "metadata.labels", o.Labels()))
		r.Steps[len(r.Steps)-1].Actions = []Remediation{{"enrol-namespace", "Configure ambient enrolment", "Review the namespace-level label change.", nil}}
		probe := &store.Object{Cluster: o.Cluster, Namespace: o.Name, Obj: map[string]any{}}
		wn, wp, gw := Waypoint(ws, probe)
		state = "warning"
		if gw != nil {
			state = "pass"
		}
		add(r, "waypoint", "Namespace default waypoint", state, defaultString(wp, "No namespace default is selected")+" "+wn, evidence(o, "metadata.labels", o.Labels()))
		r.Steps[len(r.Steps)-1].Actions = []Remediation{{"waypoint", "Configure namespace or shared waypoint", "Service and Pod overrides are preserved unless explicitly changed.", nil}}
		if gw != nil {
			conditions(r, gw, "status", []string{"Accepted", "Programmed"}, "waypoint-status")
		}
	case "Pod":
		n := Target(ws, o.Cluster, "Namespace", "", ns)
		mode := ""
		if n != nil {
			mode = n.Labels()["istio.io/dataplane-mode"]
		}
		state := "unknown"
		if mode == "ambient" {
			state = "pass"
		}
		if o.Labels()["istio.io/dataplane-mode"] == "none" {
			state = "warning"
		}
		detail := "Namespace mode " + defaultString(mode, "not captured/set") + "; pod override " + defaultString(o.Labels()["istio.io/dataplane-mode"], "none")
		for _, ct := range L(Path(o.Obj, "spec", "containers")) {
			if S(M(ct)["name"]) == "istio-proxy" {
				state = "warning"
				detail += "; an Istio sidecar is present"
			}
		}
		add(r, "enrolment", "Pod dataplane", state, detail, evidence(o, "metadata.labels", o.Labels()), evidence(o, "metadata.annotations.ambient.istio.io/redirection", Path(o.Obj, "metadata", "annotations", "ambient.istio.io/redirection")))
		ready := podReady(o)
		state = "warning"
		if ready {
			state = "pass"
		}
		add(r, "readiness", "Pod readiness", state, "Inspect Ready conditions, startup errors and events; changing a Service selector does not repair this Pod.", evidence(o, "status", o.Obj["status"]))
		wn, wp, gw := Waypoint(ws, o)
		if wp != "" {
			state = "unknown"
			if gw != nil {
				traffic := defaultString(gw.Labels()["istio.io/waypoint-for"], "service")
				state = "pass"
				if traffic != "workload" && traffic != "all" {
					state = "fail"
				}
				add(r, "waypoint", "Pod-IP waypoint traffic type", state, "Direct Pod-IP traffic needs workload/all handling; service-only waypoint selection is insufficient.", evidence(gw, "metadata.labels.istio.io/waypoint-for", traffic))
			} else {
				add(r, "waypoint", "Pod waypoint", "unknown", wn+"/"+wp+" was not captured.")
			}
		}
	case "Gateway":
		add(r, "gateway-class", "Gateway class", "warning", S(Path(o.Obj, "spec", "gatewayClassName"))+"; a Gateway is infrastructure, while Services/namespaces choose which traffic uses it.", evidence(o, "spec", o.Obj["spec"]))
		conditions(r, o, "status", []string{"Accepted", "Programmed"}, "gateway-status")
		r.Steps[0].Actions = []Remediation{{"waypoint", "Enrol a namespace to this waypoint", "Choose the namespaces and review the binding scope.", nil}}
	case "HTTPRoute":
		probe := &store.Object{Cluster: o.Cluster, Namespace: ns, Obj: map[string]any{}}
		wn, wp := "", ""
		for _, v := range L(Path(o.Obj, "spec", "parentRefs")) {
			g, k, pns, pn := parent(M(v), ns)
			if g == GatewayGroup && k == "Gateway" {
				gw := Target(ws, o.Cluster, "Gateway", pns, pn)
				if gw != nil && strings.Contains(S(Path(gw.Obj, "spec", "gatewayClassName")), "waypoint") {
					wn, wp = pns, pn
					break
				}
			}
		}
		inspectRoute(ws, r, probe, o, wn, wp)
		r.Notes = append(r.Notes, "Select the intended frontend Service from Related Services before changing a route parent; backend names do not identify frontend intent.")
	case "ServiceEntry":
		add(r, "external-hosts", "External service configuration", "warning", "ServiceEntry represents external destinations, so Kubernetes EndpointSlice readiness is not applicable. Inspect hosts, addresses, resolution, ports and export scope.", evidence(o, "spec", o.Obj["spec"]))
		wn, wp, gw := Waypoint(ws, o)
		state := "warning"
		if gw != nil {
			state = "pass"
		}
		add(r, "waypoint", "External service waypoint", state, defaultString(wp, "No waypoint selected")+" "+wn)
		r.Steps[len(r.Steps)-1].Actions = []Remediation{{"waypoint", "Set up an external-service waypoint binding", "Review namespace/object enrolment and the installed controller's support.", nil}}
		if gw != nil {
			conditions(r, gw, "status", []string{"Accepted", "Programmed"}, "waypoint-status")
		}
	}
	return r
}
func podReady(p *store.Object) bool {
	for _, v := range L(Path(p.Obj, "status", "conditions")) {
		if S(M(v)["type"]) == "Ready" && S(M(v)["status"]) == "True" {
			return true
		}
	}
	return false
}
func recovery(ws *store.Workspace, svc *store.Object) *Recovery {
	r := &Recovery{EndpointState: ws.CollectionState(svc.Cluster, "discovery.k8s.io", "endpointslices", "EndpointSlice", true).State, Pods: []PodChoice{}, Workloads: []WorkloadChoice{}}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	r.Command = "flightbay collect --context " + quote(svc.Cluster) + " --namespaces " + quote(svc.Namespace) + " --collect objects --include-resources services,pods,deployments.apps,endpointslices.discovery.k8s.io --no-logs --no-portforward --out ~/Downloads/flightbay-endpoint-evidence.tar.zst"
	for _, p := range ws.List(svc.Cluster, "", "pods", svc.Namespace, nil) {
		v := PodChoice{Ref: reference(p), Labels: p.Labels(), Ready: podReady(p), Selected: len(M(Path(svc.Obj, "spec", "selector"))) > 0 && matches(M(Path(svc.Obj, "spec", "selector")), p.Labels()), Ports: []int{}}
		for _, ct := range L(Path(p.Obj, "spec", "containers")) {
			for _, port := range L(M(ct)["ports"]) {
				n := N(M(port)["containerPort"])
				if n > 0 {
					v.Ports = append(v.Ports, n)
				}
			}
		}
		b, _ := jsonText(p.Obj["status"])
		v.Detail = b
		r.Pods = append(r.Pods, v)
	}
	for _, d := range ws.List(svc.Cluster, "apps", "deployments", svc.Namespace, nil) {
		if len(M(Path(svc.Obj, "spec", "selector"))) > 0 && matches(M(Path(svc.Obj, "spec", "selector")), stringMap(M(Path(d.Obj, "spec", "template", "metadata", "labels")))) {
			r.Workloads = append(r.Workloads, WorkloadChoice{reference(d), N(Path(d.Obj, "spec", "replicas"))})
		}
	}
	selected, ready := 0, 0
	for _, p := range r.Pods {
		if p.Selected {
			selected++
			if p.Ready {
				ready++
			}
		}
	}
	r.Detail = fmt.Sprintf("%d captured Pods match this Service selector; %d are Ready. EndpointSlice collection: %s. A complete zero-endpoint result is different from missing evidence.", selected, ready, r.EndpointState)
	return r
}
func jsonText(v any) (string, error) { b, e := json.Marshal(v); return string(b), e }
func serviceGraph(ws *store.Workspace, svc *store.Object, r *Investigation) Graph {
	g := Graph{Root: reference(svc), Groups: []GraphGroup{}}
	stateFor := func(ref Ref) string {
		state := "unknown"
		for _, s := range r.Steps {
			for _, e := range s.Evidence {
				if e.Ref == ref {
					if s.State == "fail" {
						return "fail"
					}
					if s.State == "warning" {
						state = "warning"
					} else if state == "unknown" {
						state = s.State
					}
				}
			}
		}
		return state
	}
	addGroup := func(title, relation string, objs []*store.Object) {
		nodes := []GraphNode{}
		for _, o := range objs {
			if o != nil {
				nodes = append(nodes, GraphNode{Ref: reference(o), State: stateFor(reference(o)), Exists: true})
			}
		}
		g.Groups = append(g.Groups, GraphGroup{title, relation, nodes})
	}
	addGroup("Namespace", "inherits defaults unless overridden", []*store.Object{Target(ws, svc.Cluster, "Namespace", "", svc.Namespace)})
	wn, wp, gw := Waypoint(ws, svc)
	addGroup("Waypoint", "Service uses this waypoint", []*store.Object{gw})
	routes := []*store.Object{}
	backends := map[Ref][]string{}
	for _, route := range ws.List(svc.Cluster, GatewayGroup, "httproutes", "", nil) {
		if !related(route, svc, wn, wp) {
			continue
		}
		routes = append(routes, route)
		for i, v := range L(Path(route.Obj, "spec", "rules")) {
			refs := L(M(v)["backendRefs"])
			total := 0
			for _, b := range refs {
				if w, ok := M(b)["weight"]; ok {
					total += N(w)
				} else {
					total++
				}
			}
			for _, b := range refs {
				m := M(b)
				if defaultString(S(m["kind"]), "Service") != "Service" || S(m["group"]) != "" {
					continue
				}
				ns := defaultString(S(m["namespace"]), route.Namespace)
				ref := Ref{svc.Cluster, "", "services", "Service", ns, S(m["name"])}
				w := 1
				if value, ok := m["weight"]; ok {
					w = N(value)
				}
				note := fmt.Sprintf("%s rule %d · port %d · weight %d", route.Name, i+1, N(m["port"]), w)
				if total > 0 {
					note += fmt.Sprintf(" (%.1f%%)", 100*float64(w)/float64(total))
				}
				backends[ref] = append(backends[ref], note)
			}
		}
	}
	addGroup("HTTPRoutes", "route parent/backend relationships", routes)
	nodes := []GraphNode{}
	for ref, notes := range backends {
		o := ws.Get(ref.Cluster, ref.Group, ref.Resource, ref.Namespace, ref.Name)
		state := stateFor(ref)
		if o == nil {
			state = missing(ws, ref.Cluster, "", "services", "Service", ref.Namespace)
		}
		nodes = append(nodes, GraphNode{ref, state, o != nil, notes})
	}
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].Ref.Namespace+nodes[i].Ref.Name < nodes[j].Ref.Namespace+nodes[j].Ref.Name
	})
	g.Groups = append(g.Groups, GraphGroup{"Backend Services", "routes forward to these backends", nodes})
	policies := []*store.Object{}
	seen := map[Ref]bool{}
	for _, s := range r.Steps {
		for _, e := range s.Evidence {
			if strings.Contains(e.Ref.Kind, "Policy") || e.Ref.Kind == "DestinationRule" || e.Ref.Kind == "PeerAuthentication" {
				if !seen[e.Ref] {
					if o := ws.Get(e.Ref.Cluster, e.Ref.Group, e.Ref.Resource, e.Ref.Namespace, e.Ref.Name); o != nil {
						seen[e.Ref] = true
						policies = append(policies, o)
					}
				}
			}
		}
	}
	for _, d := range ws.List(svc.Cluster, "networking.istio.io", "destinationrules", svc.Namespace, nil) {
		h := S(Path(d.Obj, "spec", "host"))
		if serviceHost(h, svc) {
			policies = append(policies, d)
		}
	}
	addGroup("Policies and destination rules", "targets the Service, waypoint or workload", policies)
	pods := []*store.Object{}
	for _, p := range ws.List(svc.Cluster, "", "pods", svc.Namespace, nil) {
		if len(M(Path(svc.Obj, "spec", "selector"))) > 0 && matches(M(Path(svc.Obj, "spec", "selector")), p.Labels()) {
			pods = append(pods, p)
		}
	}
	addGroup("Workloads", "selected by the Service", pods)
	return g
}
func serviceHost(host string, svc *store.Object) bool {
	return host == svc.Name || host == svc.Name+"."+svc.Namespace || host == svc.Name+"."+svc.Namespace+".svc" || strings.HasPrefix(host, svc.Name+"."+svc.Namespace+".svc.")
}
