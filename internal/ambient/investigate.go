// Package ambient explains ambient configuration using captured evidence and
// generates narrowly scoped configuration plans. It never contacts a cluster.
package ambient

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/yaml"

	"github.com/tjorourke/flightbay/internal/store"
)

const GatewayGroup = "gateway.networking.k8s.io"

type Ref struct {
	Cluster   string `json:"cluster"`
	Group     string `json:"group"`
	Resource  string `json:"resource"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}
type Evidence struct {
	Ref   Ref    `json:"ref"`
	Field string `json:"field"`
	Value string `json:"value"`
}
type Step struct {
	Runbook      []RunbookCommand `json:"runbook,omitempty"`
	SuggestedFix *FixGuide        `json:"suggestedFix,omitempty"`
	Docs         []DocLink        `json:"docs"`
	ID           string           `json:"id"`
	Title        string           `json:"title"`
	State        string           `json:"state"` // pass, fail, warning, unknown, na
	Detail       string           `json:"detail"`
	NextStep     string           `json:"nextStep,omitempty"`
	Evidence     []Evidence       `json:"evidence,omitempty"`
	FixYAML      string           `json:"fixYaml,omitempty"`
	Actions      []Remediation    `json:"actions,omitempty"`
}
type Remediation struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Detail string `json:"detail"`
	Ref    *Ref   `json:"ref,omitempty"`
}
type Investigation struct {
	Cluster       string         `json:"cluster"`
	Namespace     string         `json:"namespace"`
	Service       string         `json:"service"`
	Waypoint      string         `json:"waypoint,omitempty"`
	WaypointClass string         `json:"waypointClass,omitempty"`
	Steps         []Step         `json:"steps"`
	Counts        map[string]int `json:"counts"`
	Notes         []string       `json:"notes"`
}
type Service struct {
	Ref
	UID         string      `json:"uid"`
	Ports       []int       `json:"ports"`
	Regions     []string    `json:"regions"`
	Publication *ServiceDNS `json:"publication,omitempty"`
}
type Inventory struct {
	Issues         []AmbientIssue   `json:"issues"`
	FixGuides      []FixGuide       `json:"fixGuides"`
	Topology       Topology         `json:"topology"`
	Environments   []Environment    `json:"environments"`
	Objects        []Ref            `json:"objects"`
	Waypoints      []WaypointChoice `json:"waypoints"`
	GatewayClasses []string         `json:"gatewayClasses"`
	Services       []Service        `json:"services"`
	Routes         []Ref            `json:"routes"`
	Namespaces     []Ref            `json:"namespaces"`
	// EnrolledNamespaces carry istio.io/dataplane-mode=ambient.
	EnrolledNamespaces []Ref `json:"enrolledNamespaces"`
}
type WaypointChoice struct {
	Ref
	Class   string `json:"class"`
	Traffic string `json:"traffic"`
}

func M(x any) map[string]any { v, _ := x.(map[string]any); return v }
func L(x any) []any          { v, _ := x.([]any); return v }
func S(x any) string         { v, _ := x.(string); return v }
func N(x any) int {
	switch v := x.(type) {
	case float64:
		return int(v)
	case int64:
		return int(v)
	case int:
		return v
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	case string:
		n, _ := strconv.Atoi(v)
		return n
	}
	return 0
}
func Path(x map[string]any, keys ...string) any {
	var v any = x
	for _, k := range keys {
		v = M(v)[k]
	}
	return v
}
func reference(o *store.Object) Ref {
	return Ref{o.Cluster, o.Group, o.Resource, o.Kind, o.Namespace, o.Name}
}
func evidence(o *store.Object, field string, value any) Evidence {
	b, _ := json.Marshal(value)
	return Evidence{reference(o), field, string(b)}
}
func add(r *Investigation, id, title, state, detail string, es ...Evidence) {
	r.Steps = append(r.Steps, Step{ID: id, Title: title, State: state, Detail: detail, Evidence: es})
	r.Counts[state]++
}
func defaultString(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func List(ws *store.Workspace) Inventory {
	r := Inventory{FixGuides: CommonFixes(), Services: []Service{}, Routes: []Ref{}, Namespaces: []Ref{}, EnrolledNamespaces: []Ref{}}
	for _, c := range ws.Clusters() {
		for _, n := range ws.List(c, "", "namespaces", "", nil) {
			r.Namespaces = append(r.Namespaces, reference(n))
			if n.Labels()["istio.io/dataplane-mode"] == "ambient" {
				r.EnrolledNamespaces = append(r.EnrolledNamespaces, reference(n))
			}
		}
		for _, o := range ws.List(c, "", "services", "", nil) {
			v := Service{Ref: reference(o), UID: S(Path(o.Obj, "metadata", "uid")), Ports: []int{}, Regions: regions(ws, o)}
			if scope := o.Labels()["solo.io/service-scope"]; scope == "global" || scope == "global-only" {
				dns := serviceDNS(ws, o)
				v.Publication = &dns
			}
			for _, p := range L(Path(o.Obj, "spec", "ports")) {
				v.Ports = append(v.Ports, N(M(p)["port"]))
			}
			r.Services = append(r.Services, v)
		}
		for _, o := range ws.List(c, GatewayGroup, "httproutes", "", nil) {
			r.Routes = append(r.Routes, reference(o))
		}
	}
	sort.Slice(r.Services, func(i, j int) bool {
		a, b := r.Services[i], r.Services[j]
		return a.Cluster+"/"+a.Namespace+"/"+a.Name < b.Cluster+"/"+b.Namespace+"/"+b.Name
	})
	expandInventory(ws, &r)
	r.Topology = capturedTopology(ws)
	r.Issues = Issues(ws)
	return r
}

// Missing resources are failures only with complete, in-scope collections.
func missing(ws *store.Workspace, c, g, res, kind, ns string) string {
	if h := ws.Header(); h != nil && h.Declared() {
		return "unknown"
	}
	if p := ws.Policy(); p != nil && !p.NamespaceAllowed(ns) {
		return "unknown"
	}
	if ws.CollectionState(c, g, res, kind, true).State == store.StateComplete {
		return "fail"
	}
	return "unknown"
}

func Waypoint(ws *store.Workspace, svc *store.Object) (string, string, *store.Object) {
	lb := svc.Labels()
	name := lb["istio.io/use-waypoint"]
	ns := lb["istio.io/use-waypoint-namespace"]
	if name == "" {
		if n := ws.Get(svc.Cluster, "", "namespaces", "", svc.Namespace); n != nil {
			lb = n.Labels()
			name = lb["istio.io/use-waypoint"]
			ns = lb["istio.io/use-waypoint-namespace"]
		}
	}
	ns = defaultString(ns, svc.Namespace)
	if name == "" || name == "none" {
		return ns, "", nil
	}
	return ns, name, ws.Get(svc.Cluster, GatewayGroup, "gateways", ns, name)
}

func Investigate(ws *store.Workspace, c, ns, name, routeName string) (*Investigation, error) {
	svc := ws.Get(c, "", "services", ns, name)
	if svc == nil {
		return nil, fmt.Errorf("Service %s/%s is not in this workspace", ns, name)
	}
	r := &Investigation{Cluster: c, Namespace: ns, Service: name, Steps: []Step{}, Counts: map[string]int{}, Notes: []string{"Results describe the captured configuration, not proof of a successful request. Missing or reviewed-out evidence remains unknown.", "A Gateway parent can be valid for waypoint-wide routes. A Service parent scopes a route to that Service. Policy targetRefs have their own attachment rules."}}
	nso := ws.Get(c, "", "namespaces", "", ns)
	if nso == nil {
		add(r, "enrolment", "Namespace enrolment", "unknown", "Namespace labels were not captured.")
	} else if nso.Labels()["istio.io/dataplane-mode"] != "ambient" {
		add(r, "enrolment", "Namespace enrolment", "warning", "The namespace is not labelled ambient. Check pod-level enrolment before concluding traffic is captured.", evidence(nso, "metadata.labels", nso.Labels()))
	} else {
		add(r, "enrolment", "Namespace enrolment", "pass", "Namespace is enrolled in ambient.", evidence(nso, "metadata.labels.istio.io/dataplane-mode", "ambient"))
	}
	sel := M(Path(svc.Obj, "spec", "selector"))
	pods := []*store.Object{}
	for _, p := range ws.List(c, "", "pods", ns, nil) {
		if len(sel) > 0 && matches(sel, p.Labels()) {
			pods = append(pods, p)
		}
	}
	if len(pods) == 0 && len(sel) > 0 {
		add(r, "workloads", "Selected workloads", missing(ws, c, "", "pods", "Pod", ns), "No selected Pods are visible. ExternalName or selectorless Services require a separate endpoint check.", evidence(svc, "spec.selector", sel))
	}
	for _, p := range pods {
		state, detail := "pass", "Pod is ready."
		if p.Labels()["istio.io/dataplane-mode"] == "none" || p.Labels()["istio.io/dataplane-mode"] == "sidecar" {
			state, detail = "warning", "Pod opts out of ambient enrolment."
		}
		for _, ct := range L(Path(p.Obj, "spec", "containers")) {
			if S(M(ct)["name"]) == "istio-proxy" {
				state, detail = "warning", "Pod has a sidecar; do not assume the ambient waypoint path applies."
			}
		}
		ready := false
		for _, v := range L(Path(p.Obj, "status", "conditions")) {
			m := M(v)
			if S(m["type"]) == "Ready" && S(m["status"]) == "True" {
				ready = true
			}
		}
		if !ready {
			state, detail = "warning", "Pod is not reported Ready in this capture."
		}
		add(r, "pod-"+p.Name, "Workload "+p.Name, state, detail, evidence(p, "metadata.labels", p.Labels()), evidence(p, "status.conditions", Path(p.Obj, "status", "conditions")))
	}
	wpNS, wpName, wp := Waypoint(ws, svc)
	if wpName != "" {
		conditions(r, svc, "status", []string{"istio.io/WaypointBound"}, "waypoint-bound")
		add(r, "require-waypoint", "Waypoint bypass protection", "warning", "A waypoint binding selects a path, but does not prevent direct workload traffic. Review an L4 ALLOW restricted to the waypoint service account alongside all existing additive ALLOW policies.", evidence(svc, "spec.selector", sel))
		if waypointPrincipal(ws, svc) != "" && len(sel) > 0 && hasResource(ws, c, "security.istio.io", "authorizationpolicies") {
			r.Steps[len(r.Steps)-1].Actions = []Remediation{{ID: "require-waypoint", Label: "Require waypoint in wizard", Detail: "Review a workload-selector ALLOW for the captured waypoint identity."}}
		}
	}
	r.Waypoint = wpName
	if wpName == "" {
		add(r, "waypoint", "Waypoint selection", "warning", "No waypoint is selected for this Service. L4 traffic can still work, but Service-scoped L7 controls need a waypoint.", evidence(svc, "metadata.labels", svc.Labels()))
	} else if wp == nil {
		add(r, "waypoint", "Waypoint exists", missing(ws, c, GatewayGroup, "gateways", "Gateway", wpNS), "Selected waypoint "+wpNS+"/"+wpName+" was not captured. Incomplete collection does not prove it is missing.", evidence(svc, "metadata.labels", svc.Labels()))
	} else {
		r.WaypointClass = S(Path(wp.Obj, "spec", "gatewayClassName"))
		classState, classDetail := "pass", wpNS+"/"+wpName+" ("+r.WaypointClass+")"
		if !strings.Contains(r.WaypointClass, "waypoint") {
			classState = "warning"
			classDetail += " is not a recognised waypoint class; inspect its GatewayClass controller."
			if r.WaypointClass == "istio" || r.WaypointClass == "kgateway" || r.WaypointClass == "enterprise-agentgateway" {
				classState = "fail"
				classDetail += " This is an ingress class, not an ambient waypoint class."
			}
		}
		add(r, "waypoint", "Waypoint selection", classState, classDetail, evidence(wp, "spec.gatewayClassName", r.WaypointClass))
		hbone := false
		implicitHBONE := false
		for _, v := range L(Path(wp.Obj, "spec", "listeners")) {
			if waypointMeshListener(wp, M(v)) {
				hbone = true
				if S(M(v)["protocol"]) != "HBONE" {
					implicitHBONE = true
				}
			}
		}
		if !hbone {
			add(r, "waypoint-listener", "Waypoint HBONE listener", "fail", "The selected Gateway has no HBONE listener for ambient traffic.", evidence(wp, "spec.listeners", Path(wp.Obj, "spec", "listeners")))
		}
		if implicitHBONE {
			rendered := ws.Get(c, "", "services", wpNS, wpName)
			state, detail := "unknown", "Agentgateway declares HTTP mesh port 15088 and supplies HBONE separately. The rendered Service was not captured."
			es := []Evidence{evidence(wp, "spec.listeners", Path(wp.Obj, "spec", "listeners"))}
			if rendered != nil {
				state, detail = "pass", "Agentgateway's HTTP mesh listener on 15088 has a rendered HBONE Service port on 15008."
				if !hasPort(rendered, 15008) {
					state, detail = "fail", "The captured rendered waypoint Service does not expose its expected HBONE transport port 15008."
				}
				es = append(es, evidence(rendered, "spec.ports", Path(rendered.Obj, "spec", "ports")))
			}
			add(r, "waypoint-listener", "Waypoint transport", state, detail, es...)
		}
		traffic := defaultString(wp.Labels()["istio.io/waypoint-for"], "service")
		if traffic != "service" && traffic != "all" {
			add(r, "traffic-type", "Waypoint traffic type", "fail", "This waypoint handles "+traffic+", not Service-addressed traffic.", evidence(wp, "metadata.labels.istio.io/waypoint-for", traffic))
		} else {
			add(r, "traffic-type", "Waypoint traffic type", "pass", "Waypoint handles Service-addressed traffic. Direct Pod-IP traffic follows a different selection path.", evidence(wp, "metadata.labels.istio.io/waypoint-for", traffic))
		}
		conditions(r, wp, "status", []string{"Accepted", "Programmed"}, "waypoint-status")
		if ns != wpNS {
			allowed := false
			unknown := false
			for _, v := range L(Path(wp.Obj, "spec", "listeners")) {
				if !waypointMeshListener(wp, M(v)) {
					continue
				}
				ar := M(M(v)["allowedRoutes"])
				n := M(ar["namespaces"])
				switch defaultString(S(n["from"]), "Same") {
				case "All":
					allowed = true
				case "Selector":
					if nso == nil {
						unknown = true
					} else {
						b, _ := json.Marshal(n["selector"])
						var sl metav1.LabelSelector
						if json.Unmarshal(b, &sl) == nil {
							ls, e := metav1.LabelSelectorAsSelector(&sl)
							if e == nil && ls.Matches(labels.Set(nso.Labels())) {
								allowed = true
							}
						}
					}
				}
			}
			state := "fail"
			if allowed {
				state = "pass"
			} else if unknown {
				state = "unknown"
			}
			add(r, "waypoint-namespace", "Cross-namespace waypoint enrolment", state, "A waypoint in another namespace must permit the Service namespace through allowedRoutes.", evidence(wp, "spec.listeners", Path(wp.Obj, "spec", "listeners")))
		}
	}
	routes := ws.List(c, GatewayGroup, "httproutes", "", nil)
	found := 0
	for _, route := range routes {
		if routeName != "" {
			if route.Namespace != ns || route.Name != routeName {
				continue
			}
		} else if !related(route, svc, wpNS, wpName) {
			continue
		}
		found++
		inspectRoute(ws, r, svc, route, wpNS, wpName)
	}
	if found == 0 {
		if routeName != "" {
			add(r, "routes", "Requested HTTPRoute", missing(ws, c, GatewayGroup, "httproutes", "HTTPRoute", ns), "Requested HTTPRoute "+ns+"/"+routeName+" was not captured. Check its name and collection scope.")
		} else if missing(ws, c, GatewayGroup, "httproutes", "HTTPRoute", ns) == "fail" {
			add(r, "routes", "Optional HTTPRoute", "na", "No related HTTPRoute is configured. Ordinary ambient Service traffic does not require one.")
		} else {
			add(r, "routes", "HTTPRoute coverage", "unknown", "HTTPRoute collection is incomplete. A route is optional for ordinary ambient Service traffic; refresh or collect this API to inspect any custom routing.")
		}
	}
	sidecars := 0
	for _, p := range pods {
		for _, ct := range L(Path(p.Obj, "spec", "containers")) {
			if S(M(ct)["name"]) == "istio-proxy" {
				sidecars++
				break
			}
		}
	}
	sidecarOnly := len(pods) > 0 && sidecars == len(pods)
	for _, p := range ws.List(c, "security.istio.io", "authorizationpolicies", ns, nil) {
		selector := M(Path(p.Obj, "spec", "selector", "matchLabels"))
		targets := L(Path(p.Obj, "spec", "targetRefs"))
		relevant := len(targets) == 0 && (len(selector) == 0 || matches(selector, stringMap(sel)))
		for _, v := range targets {
			t := M(v)
			if (S(t["kind"]) == "Service" && S(t["name"]) == name) || (S(t["kind"]) == "Gateway" && S(t["name"]) == wpName && ns == wpNS) {
				relevant = true
			}
		}
		if !relevant {
			continue
		}
		policyStatus(r, p)
		if len(targets) == 0 && hasL7(Path(p.Obj, "spec", "rules")) {
			state, detail := "fail", "L7 attributes on a workload-selector policy cannot be enforced by ztunnel. Istio fails closed for unsupported L7 rules; attach an L7 policy to the appropriate Service/waypoint instead."
			if sidecarOnly {
				state, detail = "warning", "All selected captured Pods have sidecars. Selector-based L7 rules can be valid there; confirm the actual dataplane before diagnosing this as a ztunnel policy failure."
			}
			add(r, "policy-"+p.Name, "Policy "+p.Name, state, detail, evidence(p, "spec", p.Obj["spec"]))
		} else {
			add(r, "policy-"+p.Name, "Policy "+p.Name, "warning", "Policy is relevant to this Service. Check its allow/deny rules and enforcement point; attachment alone does not prove a request is allowed.", evidence(p, "spec.targetRefs", targets))
		}
	}
	for _, x := range []struct{ g, res string }{{"agentgateway.dev", "agentgatewaypolicies"}, {"enterpriseagentgateway.solo.io", "enterpriseagentgatewaypolicies"}} {
		for _, p := range ws.List(c, x.g, x.res, ns, nil) {
			relevant := false
			for _, v := range L(Path(p.Obj, "spec", "targetRefs")) {
				t := M(v)
				if S(t["kind"]) == "Service" && S(t["name"]) == name {
					relevant = true
				}
				if S(t["kind"]) == "Gateway" && S(t["name"]) == wpName && ns == wpNS {
					relevant = true
				}
				if S(t["kind"]) == "HTTPRoute" {
					if route := ws.Get(c, GatewayGroup, "httproutes", ns, S(t["name"])); route != nil && related(route, svc, wpNS, wpName) {
						relevant = true
					}
				}
			}
			if relevant {
				add(r, "agent-policy-"+p.Name, "Policy "+p.Name, "warning", "Controller-specific policy targets this Service's path. Check the controller's attachment conditions and the enforcement point.", evidence(p, "spec.targetRefs", Path(p.Obj, "spec", "targetRefs")))
				policyStatus(r, p)
			}
		}
	}
	inspectJWT(ws, r, svc, wpNS, wpName)
	for _, p := range ws.List(c, "security.istio.io", "peerauthentications", ns, nil) {
		selector := M(Path(p.Obj, "spec", "selector", "matchLabels"))
		if len(selector) > 0 && !matches(selector, stringMap(sel)) {
			continue
		}
		disabled := S(Path(p.Obj, "spec", "mtls", "mode")) == "DISABLE"
		for _, v := range M(Path(p.Obj, "spec", "portLevelMtls")) {
			if S(M(v)["mode"]) == "DISABLE" {
				disabled = true
			}
		}
		if disabled {
			state, detail := "fail", "DISABLE is not supported by ambient ztunnel. Use STRICT to require mesh mTLS, or remove the unsupported setting rather than assuming plaintext is enabled."
			if sidecarOnly {
				state, detail = "warning", "Selected Pods have sidecars, where DISABLE has different semantics. Check the actual dataplane; do not interpret this as a valid ztunnel plaintext setting."
			}
			add(r, "mtls-"+p.Name, "Ambient mTLS mode", state, detail, evidence(p, "spec", p.Obj["spec"]))
		}
	}
	ready, seen := 0, 0
	for _, ep := range ws.List(c, "discovery.k8s.io", "endpointslices", ns, nil) {
		if ep.Labels()["kubernetes.io/service-name"] != name {
			continue
		}
		for _, v := range L(ep.Obj["endpoints"]) {
			seen++
			if Path(M(v), "conditions", "ready") != false {
				ready++
			}
		}
	}
	if S(Path(svc.Obj, "spec", "type")) == "ExternalName" {
		add(r, "endpoints", "Service endpoints", "na", "ExternalName resolution is outside EndpointSlice discovery.")
	} else {
		state := "pass"
		if ready == 0 {
			state = missing(ws, c, "discovery.k8s.io", "endpointslices", "EndpointSlice", ns)
		}
		add(r, "endpoints", "Ready endpoints", state, fmt.Sprintf("%d ready/eligible endpoints out of %d captured. A route may attach successfully while its backend has no ready endpoints.", ready, seen))
	}
	if wp != nil {
		available := false
		for _, d := range ws.Diagnostics(c) {
			if d.Namespace == wpNS {
				for _, p := range ws.List(c, "", "pods", wpNS, nil) {
					if p.Name == d.Pod && p.Labels()["gateway.networking.k8s.io/gateway-name"] == wpName {
						available = true
					}
				}
			}
		}
		state, detail := "unknown", "No matching waypoint proxy diagnostics were captured. Use a capture with --diagnostics to inspect delivered config and xDS sync in Proxy configs."
		if available {
			state, detail = "pass", "Waypoint proxy diagnostics were captured and are available in Proxy diagnostics. Availability is confirmed; delivered routes, xDS sync and successful traffic require separate evidence."
		}
		add(r, "dataplane", "Proxy diagnostic coverage", state, detail)
	}
	for i := range r.Steps {
		if r.Steps[i].State != "pass" && r.Steps[i].State != "na" {
			r.Steps[i].Runbook = Runbook(ws, svc)
		}
	}
	return r, nil
}
func policyStatus(r *Investigation, p *store.Object) {
	found := false
	inspect := func(status map[string]any, field, prefix string) {
		types := []string{}
		for _, v := range L(status["conditions"]) {
			t := S(M(v)["type"])
			if t == "Accepted" || t == "Attached" || t == "WaypointAccepted" || t == "ZtunnelAccepted" {
				types = append(types, t)
			}
		}
		if len(types) == 0 {
			return
		}
		found = true
		copy := *p
		copy.Obj = map[string]any{"metadata": p.Obj["metadata"], "reported": status}
		before := len(r.Steps)
		conditions(r, &copy, "reported", types, prefix)
		for i := before; i < len(r.Steps); i++ {
			for j := range r.Steps[i].Evidence {
				r.Steps[i].Evidence[j].Field = field + ".conditions"
			}
		}
	}
	inspect(M(p.Obj["status"]), "status", "policy-status-"+p.Name)
	for i, v := range L(Path(p.Obj, "status", "ancestors")) {
		inspect(M(v), fmt.Sprintf("status.ancestors[%d]", i), fmt.Sprintf("policy-status-%s-%d", p.Name, i))
	}
	if !found {
		add(r, "policy-status-"+p.Name, "Policy attachment status: "+p.Name, "unknown", "No controller attachment condition was captured for this policy. Do not infer successful enforcement from its presence alone.")
	}
}

func matches(sel map[string]any, lb map[string]string) bool {
	for k, v := range sel {
		if lb[k] != S(v) {
			return false
		}
	}
	return true
}
func stringMap(m map[string]any) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		out[k] = S(v)
	}
	return out
}
func hasL7(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		for k, v := range x {
			if k == "methods" || k == "paths" || k == "hosts" || k == "requestPrincipals" || strings.HasPrefix(S(v), "request.") {
				return true
			}
			if hasL7(v) {
				return true
			}
		}
	case []any:
		for _, v := range x {
			if hasL7(v) {
				return true
			}
		}
	}
	return false
}
func related(route, svc *store.Object, wpNS, wp string) bool {
	for _, v := range L(Path(route.Obj, "spec", "parentRefs")) {
		p := M(v)
		ns := defaultString(S(p["namespace"]), route.Namespace)
		kind := defaultString(S(p["kind"]), "Gateway")
		if kind == "Service" && ns == svc.Namespace && S(p["name"]) == svc.Name {
			return true
		}
		if kind == "Gateway" && wp != "" && ns == wpNS && S(p["name"]) == wp {
			return true
		}
	}
	for _, v := range L(Path(route.Obj, "spec", "rules")) {
		for _, x := range L(M(v)["backendRefs"]) {
			b := M(x)
			if defaultString(S(b["namespace"]), route.Namespace) == svc.Namespace && S(b["name"]) == svc.Name {
				return true
			}
		}
	}
	return false
}
func conditions(r *Investigation, o *store.Object, field string, types []string, prefix string) {
	for _, typ := range types {
		found := false
		for _, v := range L(Path(o.Obj, field, "conditions")) {
			c := M(v)
			if S(c["type"]) != typ {
				continue
			}
			found = true
			state := "unknown"
			if S(c["status"]) == "True" {
				state = "pass"
			}
			if S(c["status"]) == "False" {
				state = "fail"
			}
			gen := N(Path(o.Obj, "metadata", "generation"))
			if gen > 0 && N(c["observedGeneration"]) != gen {
				state = "unknown"
			}
			detail := strings.TrimSpace(S(c["reason"]) + " " + S(c["message"]))
			if gen > 0 && N(c["observedGeneration"]) != gen {
				detail += ". This status is stale or has no observed generation; it does not confirm the current configuration."
			}
			if detail == "" {
				detail = typ + "=" + S(c["status"])
			}
			add(r, prefix+"-"+typ, o.Kind+" "+o.Name+": "+typ, state, detail, evidence(o, field+".conditions", c))
		}
		if !found {
			add(r, prefix+"-"+typ, o.Kind+" "+o.Name+": "+typ, "unknown", "No current controller condition was captured.")
		}
	}
}
func parent(p map[string]any, routeNS string) (string, string, string, string) {
	kind := defaultString(S(p["kind"]), "Gateway")
	group := S(p["group"])
	if _, ok := p["group"]; !ok {
		group = GatewayGroup
	}
	return group, kind, defaultString(S(p["namespace"]), routeNS), S(p["name"])
}
func inspectRoute(ws *store.Workspace, r *Investigation, svc, route *store.Object, wpNS, wpName string) {
	c := svc.Cluster
	if len(L(Path(route.Obj, "spec", "parentRefs"))) == 0 {
		add(r, "route-"+route.Name+"-unattached", "Route has no parent", "fail", "The HTTPRoute does not name a parent to attach to.", evidence(route, "spec.parentRefs", Path(route.Obj, "spec", "parentRefs")))
	}
	for i, v := range L(Path(route.Obj, "spec", "parentRefs")) {
		p := M(v)
		g, k, ns, name := parent(p, route.Namespace)
		res := ""
		switch {
		case g == "" && k == "Service":
			res = "services"
		case g == GatewayGroup && k == "Gateway":
			res = "gateways"
		}
		id := fmt.Sprintf("route-%s-%s-%d", route.Namespace, route.Name, i)
		field := fmt.Sprintf("spec.parentRefs[%d]", i)
		if res == "" {
			add(r, id, "Route parent: "+route.Name, "fail", "Unsupported or mismatched parent group/kind. Omitted group defaults to gateway.networking.k8s.io and omitted kind to Gateway. A Service parent needs group: \"\" and kind: Service.", evidence(route, field, p))
			continue
		}
		o := ws.Get(c, g, res, ns, name)
		if o == nil {
			add(r, id, "Route parent: "+route.Name, missing(ws, c, g, res, k, ns), fmt.Sprintf("%s %s/%s was not captured. Check default kind, group and namespace.", k, ns, name), evidence(route, field, p))
			continue
		}
		state, detail := "pass", fmt.Sprintf("Parent resolves to %s %s/%s.", k, ns, name)
		if k == "Gateway" && ns == wpNS && name == wpName {
			state = "warning"
			detail += " This is waypoint-wide attachment, not Service-scoped. Support depends on the controller/version. Inspect its per-parent status below; for a route intended only for " + svc.Name + ", use a Service parent."
		} else if k == "Gateway" {
			detail += " This Gateway is not the Service's selected waypoint; acceptance here does not prove it affects in-mesh traffic."
			state = "warning"
		}
		if k == "Service" {
			if svc.Name != "" && (ns != svc.Namespace || name != svc.Name) {
				state, detail = "warning", detail+" This parent is not the selected Service."
			}
			if port := N(p["port"]); port > 0 && !hasPort(o, port) {
				state, detail = "fail", detail+" Parent port does not exist on the Service."
			}
		}
		add(r, id, "Route parent: "+route.Name, state, detail, evidence(route, field, p), evidence(o, "metadata.name", o.Name))
		if svc.Name != "" && k == "Gateway" && ns == wpNS && name == wpName && route.Namespace == svc.Namespace {
			parents := append([]any{}, L(Path(route.Obj, "spec", "parentRefs"))...)
			entry := map[string]any{"group": "", "kind": "Service", "name": svc.Name}
			if len(L(Path(svc.Obj, "spec", "ports"))) == 1 {
				entry["port"] = N(M(L(Path(svc.Obj, "spec", "ports"))[0])["port"])
			}
			parents[i] = entry
			b, _ := yaml.Marshal(map[string]any{"apiVersion": "gateway.networking.k8s.io/v1", "kind": "HTTPRoute", "metadata": map[string]any{"name": route.Name, "namespace": route.Namespace}, "spec": map[string]any{"parentRefs": parents}})
			r.Steps[len(r.Steps)-1].FixYAML = string(b)
		}
		matched := false
		for j, x := range L(Path(route.Obj, "status", "parents")) {
			st := M(x)
			sg, sk, sn, ss := parent(M(st["parentRef"]), route.Namespace)
			if sg != g || sk != k || sn != ns || ss != name || S(M(st["parentRef"])["sectionName"]) != S(p["sectionName"]) || N(M(st["parentRef"])["port"]) != N(p["port"]) {
				continue
			}
			matched = true
			copy := *route
			copy.Obj = map[string]any{"metadata": route.Obj["metadata"], "parent": st}
			before := len(r.Steps)
			conditions(r, &copy, "parent", []string{"Accepted", "ResolvedRefs"}, fmt.Sprintf("%s-status-%d", id, j))
			for n := before; n < len(r.Steps); n++ {
				r.Steps[n].Title += " · " + S(st["controllerName"])
				for e := range r.Steps[n].Evidence {
					r.Steps[n].Evidence[e].Field = fmt.Sprintf("status.parents[%d].conditions", j)
				}
			}
		}
		if !matched {
			add(r, id+"-status", "Attachment status: "+route.Name, "unknown", "No status entry matches this parent. The controller may not support the attachment, may not have reconciled it, or status was not collected.", evidence(route, "status.parents", Path(route.Obj, "status", "parents")))
		}
		if k == "Gateway" {
			if name != wpName || ns != wpNS {
				inspectListeners(ws, r, route, o, p, id)
			}
			if section := S(p["sectionName"]); section != "" {
				exists := false
				for _, v := range L(Path(o.Obj, "spec", "listeners")) {
					if S(M(v)["name"]) == section {
						exists = true
					}
				}
				if !exists {
					add(r, id+"-listener", "Listener section", "fail", "parentRefs.sectionName does not name a listener on the parent Gateway.", evidence(route, field+".sectionName", section))
				}
			}
		}
	}
	for _, rule := range L(Path(route.Obj, "spec", "rules")) {
		for _, v := range L(M(rule)["backendRefs"]) {
			b := M(v)
			if defaultString(S(b["kind"]), "Service") != "Service" || S(b["group"]) != "" {
				continue
			}
			ns := defaultString(S(b["namespace"]), route.Namespace)
			name := S(b["name"])
			o := ws.Get(c, "", "services", ns, name)
			id := "backend-" + route.Name + "-" + ns + "-" + name
			if o == nil {
				add(r, id, "Backend "+ns+"/"+name, missing(ws, c, "", "services", "Service", ns), "Backend Service was not captured.", evidence(route, "spec.rules[].backendRefs", b))
				continue
			}
			state, detail := "pass", "Service and backend port exist."
			if !hasPort(o, N(b["port"])) {
				state, detail = "fail", "backendRefs.port must name a Service port, not a container targetPort."
			}
			add(r, id, "Backend "+ns+"/"+name, state, detail, evidence(route, "spec.rules[].backendRefs", b))
			if ns != route.Namespace {
				state := "unknown"
				if grants(ws, c, ns, route.Namespace, name) {
					state = "pass"
				} else {
					state = missing(ws, c, GatewayGroup, "referencegrants", "ReferenceGrant", ns)
				}
				add(r, id+"-grant", "Cross-namespace backend permission", state, "A cross-namespace backend requires a ReferenceGrant in the backend namespace.")
			}
		}
	}
}
func inspectListeners(ws *store.Workspace, r *Investigation, route, gw *store.Object, p map[string]any, id string) {
	eligible, unknown := false, false
	details := []string{}
	for _, v := range L(Path(gw.Obj, "spec", "listeners")) {
		l := M(v)
		if sec := S(p["sectionName"]); sec != "" && sec != S(l["name"]) {
			continue
		}
		if port := N(p["port"]); port > 0 && port != N(l["port"]) {
			continue
		}
		name := S(l["name"])
		if S(l["protocol"]) != "HTTP" && S(l["protocol"]) != "HTTPS" {
			details = append(details, name+": listener is not HTTP/HTTPS")
			continue
		}
		ar := M(l["allowedRoutes"])
		kinds := L(ar["kinds"])
		ok := len(kinds) == 0
		for _, v := range kinds {
			k := M(v)
			if S(k["kind"]) == "HTTPRoute" && (S(k["group"]) == "" || S(k["group"]) == GatewayGroup) {
				ok = true
			}
		}
		if !ok {
			details = append(details, name+": allowedRoutes.kinds excludes HTTPRoute")
			continue
		}
		nsRule := M(ar["namespaces"])
		nsOK := route.Namespace == gw.Namespace
		switch defaultString(S(nsRule["from"]), "Same") {
		case "All":
			nsOK = true
		case "Selector":
			nsOK = false
			n := ws.Get(route.Cluster, "", "namespaces", "", route.Namespace)
			if n == nil {
				unknown = true
			} else {
				b, _ := json.Marshal(nsRule["selector"])
				var selector metav1.LabelSelector
				if json.Unmarshal(b, &selector) == nil {
					ls, e := metav1.LabelSelectorAsSelector(&selector)
					nsOK = e == nil && ls.Matches(labels.Set(n.Labels()))
				}
			}
		}
		if !nsOK {
			details = append(details, name+": route namespace is not permitted by allowedRoutes")
			continue
		}
		host := S(l["hostname"])
		hosts := L(Path(route.Obj, "spec", "hostnames"))
		hostOK := host == "" || len(hosts) == 0
		for _, h := range hosts {
			if hostOverlap(host, S(h)) {
				hostOK = true
			}
		}
		if !hostOK {
			details = append(details, name+": route and listener hostnames do not intersect")
			continue
		}
		eligible = true
	}
	state := "fail"
	if eligible {
		state = "pass"
	} else if unknown {
		state = "unknown"
	}
	add(r, id+"-eligibility", "Gateway listener attachment", state, "Checks sectionName/port, HTTP protocol, allowed route kinds, namespace permission and hostname intersection. "+strings.Join(details, "; "), evidence(gw, "spec.listeners", Path(gw.Obj, "spec", "listeners")))
}
func hostOverlap(a, b string) bool {
	if a == b {
		return true
	}
	match := func(pattern, host string) bool {
		return strings.HasPrefix(pattern, "*.") && strings.HasSuffix(host, pattern[1:]) && len(host) > len(pattern)-1
	}
	return match(a, b) || match(b, a)
}
func hasPort(svc *store.Object, port int) bool {
	for _, v := range L(Path(svc.Obj, "spec", "ports")) {
		if N(M(v)["port"]) == port {
			return true
		}
	}
	return false
}
func grants(ws *store.Workspace, c, ns, from, name string) bool {
	for _, g := range ws.List(c, GatewayGroup, "referencegrants", ns, nil) {
		okFrom, okTo := false, false
		for _, v := range L(Path(g.Obj, "spec", "from")) {
			m := M(v)
			if S(m["group"]) == GatewayGroup && S(m["kind"]) == "HTTPRoute" && S(m["namespace"]) == from {
				okFrom = true
			}
		}
		for _, v := range L(Path(g.Obj, "spec", "to")) {
			m := M(v)
			if S(m["group"]) == "" && S(m["kind"]) == "Service" && (S(m["name"]) == "" || S(m["name"]) == name) {
				okTo = true
			}
		}
		if okFrom && okTo {
			return true
		}
	}
	return false
}
