package ambient

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/tjorourke/flightbay/internal/store"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/yaml"
)

type PathRequest struct {
	Source      Ref    `json:"source"`
	Destination Ref    `json:"destination"`
	Host        string `json:"host,omitempty"`
	Port        int    `json:"port,omitempty"`
	Method      string `json:"method,omitempty"`
	Path        string `json:"path,omitempty"`
}
type PathCaller struct {
	Ref
	Pods           int    `json:"pods"`
	ServiceAccount string `json:"serviceAccount,omitempty"`
}
type PathDestination struct {
	Service
	DNS ServiceDNS `json:"dns"`
}
type PathOptions struct {
	Callers      []PathCaller      `json:"callers"`
	Destinations []PathDestination `json:"destinations"`
}
type PathHop struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Cluster string `json:"cluster"`
	State   string `json:"state"`
	Detail  string `json:"detail"`
	Refs    []Ref  `json:"refs"`
}
type PathObservation struct {
	Records     int          `json:"records"`
	Denied      int          `json:"denied"`
	WithoutHTTP int          `json:"withoutHttp"`
	Sources     []string     `json:"sources"`
	Examples    []store.Flow `json:"examples"`
	Note        string       `json:"note"`
}
type RequestPath struct {
	Request       PathRequest     `json:"request"`
	Hops          []PathHop       `json:"hops"`
	Configuration []PathHop       `json:"configuration"`
	Checks        []Step          `json:"checks"`
	DNS           ServiceDNS      `json:"dns"`
	CallerPods    []Ref           `json:"callerPods"`
	Principals    []string        `json:"principals"`
	Observed      PathObservation `json:"observed"`
	Notes         []string        `json:"notes"`
	AnalyzedAt    time.Time       `json:"analyzedAt"`
}

func callerObject(ws *store.Workspace, ref Ref) *store.Object {
	g, res := "", ""
	switch ref.Kind {
	case "Pod":
		res = "pods"
	case "ServiceAccount":
		res = "serviceaccounts"
	case "Deployment":
		g, res = "apps", "deployments"
	case "StatefulSet":
		g, res = "apps", "statefulsets"
	case "DaemonSet":
		g, res = "apps", "daemonsets"
	default:
		return nil
	}
	if ref.Group != g || ref.Resource != res {
		return nil
	}
	return ws.Get(ref.Cluster, g, res, ref.Namespace, ref.Name)
}
func callerPods(ws *store.Workspace, o *store.Object) []*store.Object {
	if o.Kind == "Pod" {
		return []*store.Object{o}
	}
	out := []*store.Object{}
	var selector metav1.LabelSelector
	b, _ := json.Marshal(Path(o.Obj, "spec", "selector"))
	_ = json.Unmarshal(b, &selector)
	sel, err := metav1.LabelSelectorAsSelector(&selector)
	for _, p := range ws.List(o.Cluster, "", "pods", o.Namespace, nil) {
		if o.Kind == "ServiceAccount" {
			if defaultString(S(Path(p.Obj, "spec", "serviceAccountName")), "default") == o.Name {
				out = append(out, p)
			}
		} else if err == nil && !sel.Empty() && sel.Matches(labels.Set(p.Labels())) {
			out = append(out, p)
		}
	}
	return out
}
func RequestPathOptions(ws *store.Workspace) PathOptions {
	out := PathOptions{Callers: []PathCaller{}, Destinations: []PathDestination{}}
	for _, c := range ws.Clusters() {
		for _, r := range []struct{ g, res string }{{"", "pods"}, {"", "serviceaccounts"}, {"apps", "deployments"}, {"apps", "statefulsets"}, {"apps", "daemonsets"}} {
			for _, o := range ws.List(c, r.g, r.res, "", nil) {
				n := len(callerPods(ws, o))
				if o.Kind == "ServiceAccount" && n == 0 {
					continue
				}
				out.Callers = append(out.Callers, PathCaller{reference(o), n, S(Path(o.Obj, "spec", "serviceAccountName"))})
			}
		}
		for _, s := range ws.List(c, "", "services", "", nil) {
			ports := []int{}
			for _, p := range L(Path(s.Obj, "spec", "ports")) {
				ports = append(ports, N(M(p)["port"]))
			}
			out.Destinations = append(out.Destinations, PathDestination{Service{Ref: reference(s), Ports: ports}, serviceDNS(ws, s)})
		}
	}
	sort.Slice(out.Callers, func(i, j int) bool {
		a, b := out.Callers[i], out.Callers[j]
		return a.Cluster+"/"+a.Namespace+"/"+a.Name+"/"+a.Kind < b.Cluster+"/"+b.Namespace+"/"+b.Name+"/"+b.Kind
	})
	sort.Slice(out.Destinations, func(i, j int) bool {
		a, b := out.Destinations[i], out.Destinations[j]
		return a.Cluster+"/"+a.Namespace+"/"+a.Name < b.Cluster+"/"+b.Namespace+"/"+b.Name
	})
	return out
}

func TraceRequestPath(ws *store.Workspace, q PathRequest) (*RequestPath, error) {
	src := callerObject(ws, q.Source)
	if src == nil {
		return nil, fmt.Errorf("choose a captured caller with its exact cluster and resource kind")
	}
	if q.Destination.Kind != "Service" || q.Destination.Group != "" || q.Destination.Resource != "services" {
		return nil, fmt.Errorf("choose a captured destination Service")
	}
	svc := ws.Get(q.Destination.Cluster, "", "services", q.Destination.Namespace, q.Destination.Name)
	if svc == nil {
		return nil, fmt.Errorf("destination Service is not captured in the selected cluster")
	}
	if q.Port == 0 {
		ps := L(Path(svc.Obj, "spec", "ports"))
		if len(ps) != 1 {
			return nil, fmt.Errorf("choose a destination Service port")
		}
		q.Port = N(M(ps[0])["port"])
	}
	if !hasPort(svc, q.Port) {
		return nil, fmt.Errorf("port must be a port exposed by the selected Service")
	}
	q.Method = strings.ToUpper(strings.TrimSpace(q.Method))
	if q.Method != "" && !contains([]string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "CONNECT", "TRACE"}, q.Method) {
		return nil, fmt.Errorf("choose an HTTP method or leave it unspecified")
	}
	if q.Path != "" {
		u, e := url.ParseRequestURI(q.Path)
		if e != nil || u.IsAbs() || !strings.HasPrefix(q.Path, "/") || strings.HasPrefix(q.Path, "//") {
			return nil, fmt.Errorf("use a relative request path beginning with / or leave it unspecified")
		}
		q.Path = u.Path
	}
	dns := serviceDNS(ws, svc)
	if q.Host == "" {
		q.Host = dns.LocalName
	}
	q.Host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(q.Host)), ".")
	p := &RequestPath{Request: q, DNS: dns, Hops: []PathHop{}, Configuration: []PathHop{}, Checks: []Step{}, CallerPods: []Ref{}, Principals: []string{}, Notes: []string{"This reconstructs a path from captured configuration. It does not send a request or prove every hop carried it.", "Matching traffic records are reported separately. Counts can include both proxy legs or multiple telemetry sources; they are not unique end-to-end requests.", "Related route and policy checks are not a full simulation of HTTP matching, rewrites, retries or load-balancer endpoint selection."}, AnalyzedAt: time.Now().UTC()}
	hop := func(id, title, cluster, state, detail string, refs ...Ref) {
		if refs == nil {
			refs = []Ref{}
		}
		item := PathHop{id, title, cluster, state, detail, refs}
		if id == "address" || id == "routing" || (id == "waypoint" && state == "na") {
			p.Configuration = append(p.Configuration, item)
			return
		}
		p.Hops = append(p.Hops, item)
	}
	pods := callerPods(ws, src)
	for _, pod := range pods {
		p.CallerPods = append(p.CallerPods, reference(pod))
	}
	callerRefs := append([]Ref{}, p.CallerPods...)
	if src.Kind != "Pod" || len(callerRefs) == 0 {
		callerRefs = append(callerRefs, reference(src))
	}
	callerState := "pass"
	if len(pods) == 0 {
		callerState = "unknown"
	}
	hop("caller", "Caller Pods", src.Cluster, callerState, fmt.Sprintf("%s %s/%s · %d captured caller Pods", src.Kind, src.Namespace, src.Name, len(pods)), callerRefs...)
	sourceMesh := false
	for _, pod := range pods {
		if podInAmbient(ws, pod) {
			sourceMesh = true
		}
		sa := defaultString(S(Path(pod.Obj, "spec", "serviceAccountName")), "default")
		td, confirmed := pathTrustDomain(ws, src.Cluster)
		principal := td + "/ns/" + pod.Namespace + "/sa/" + sa
		if !contains(p.Principals, principal) {
			p.Principals = append(p.Principals, principal)
		}
		if !confirmed && !contains(p.Notes, "Caller trust domain uses Istio's cluster.local default; it was not established by captured mesh configuration.") {
			p.Notes = append(p.Notes, "Caller trust domain uses Istio's cluster.local default; it was not established by captured mesh configuration.")
		}
	}
	if sourceMesh {
		state, detail, refs := pathZtunnels(ws, src.Cluster, pods)
		hop("source-ztunnel", "Source ztunnel", src.Cluster, state, detail, refs...)
	} else {
		hop("source-dataplane", "Caller dataplane", src.Cluster, "unknown", "Ambient interception is not established for these caller Pods. A sidecar or non-mesh caller follows a different source path.")
	}
	cross := src.Cluster != svc.Cluster
	hostKnown := q.Host == dns.LocalName || contains(dns.GlobalNames, q.Host) || q.Host == svc.Name+"."+svc.Namespace+".svc" || (!cross && src.Namespace == svc.Namespace && q.Host == svc.Name)
	hostState, hostDetail := "pass", "Hostname maps to the selected Service in captured configuration. DNS resolution itself has not been tested."
	if !hostKnown {
		hostState, hostDetail = "unknown", "This hostname is not mapped to the selected Service by captured DNS/publication configuration. Check ServiceEntries, publication and DNS evidence."
	}
	if q.Host == dns.LocalName && !dns.DomainObserved {
		hostState, hostDetail = "unknown", "This cluster DNS hostname uses an assumed default suffix. Capture CoreDNS or istiod DNS configuration to establish the actual domain; resolution was not tested."
	}
	if cross && q.Host == dns.LocalName {
		hostState, hostDetail = "warning", "Cluster-local DNS normally selects the caller cluster, not this remote Service. Choose a captured global hostname or establish an explicit cross-cluster mapping."
	}
	hop("address", "Destination address", svc.Cluster, hostState, hostDetail, reference(svc))
	sourceWaypoint := false
	if cross && sourceMesh {
		wpName, wp, refs := pathSourceGlobalWaypoint(ws, q)
		if wpName != "" {
			state, detail := "unknown", "The source global alias has a waypoint binding, but its Gateway was not captured."
			if wp != nil {
				state, detail = gatewayPathState(wp)
				sourceWaypoint = true
			}
			hop("source-waypoint", "Source waypoint", src.Cluster, state, detail, refs...)
		}
	}
	if cross {
		top := capturedTopology(ws)
		peerRefs := []Ref{}
		for _, link := range top.Links {
			if link.Kind == "peering" && link.Source.Cluster == src.Cluster && link.Target.Cluster == svc.Cluster {
				peerRefs = append(peerRefs, link.Evidence...)
			}
		}
		if len(peerRefs) > 0 {
			state := "pass"
			gatewayRefs := []Ref{}
			for _, cl := range top.Clusters {
				if cl.Context == svc.Cluster {
					for _, g := range cl.Gateways {
						for _, ref := range peerRefs {
							if g.Ref == ref && g.Role == "east-west" {
								if gateway := Target(ws, g.Ref.Cluster, "Gateway", g.Ref.Namespace, g.Ref.Name); gateway != nil {
									gatewayRefs = append(gatewayRefs, pathGatewayRefs(ws, gateway)...)
								} else {
									gatewayRefs = append(gatewayRefs, g.Ref)
								}
								if g.State == "fail" {
									state = "fail"
								} else if g.State != "pass" && state != "fail" {
									state = "unknown"
								}
							}
						}
					}
				}
			}
			p.Configuration = append(p.Configuration, PathHop{"peering", "Peer configuration", src.Cluster, state, "Remote-peer objects establish the configured link; they are not additional dataplane hops.", peerRefs})
			hop("eastwest", "East-west gateway", svc.Cluster, state, "Configured cross-network HBONE transport through this cluster's east-west gateway. Programming does not prove a request traversed it.", gatewayRefs...)
		} else {
			hop("eastwest", "Cross-cluster transport", svc.Cluster, "unknown", "No matching captured remote-peer/east-west link establishes this path. Same-network or other multicluster transports may differ; inspect peering and network configuration.")
		}
		p.Notes = append(p.Notes, "A global hostname can choose endpoints in more than one cluster. The selected destination cluster is a scenario to investigate, not a predicted load-balancer choice.")
	}
	wpNS, wpName, wp := Waypoint(ws, svc)
	if sourceWaypoint && wpName != "" {
		refs := []Ref{}
		if wp != nil {
			refs = append(refs, pathGatewayRefs(ws, wp)...)
		}
		p.Configuration = append(p.Configuration, PathHop{"destination-waypoint-binding", "Destination waypoint binding", svc.Cluster, "na", "The destination waypoint is configured. A source waypoint forwarding this global Service uses the waypoint HBONE role; the remote hand-off does not apply the destination waypoint a second time.", refs})
	} else if wpName != "" {
		if wp == nil {
			hop("waypoint", "Destination waypoint", svc.Cluster, missing(ws, svc.Cluster, GatewayGroup, "gateways", "Gateway", wpNS), "The selected waypoint was not captured.")
		} else {
			state, detail := gatewayPathState(wp)
			hop("waypoint", "Destination waypoint", svc.Cluster, state, detail, pathGatewayRefs(ws, wp)...)
		}
	} else {
		hop("waypoint", "Waypoint not selected", svc.Cluster, "na", "No waypoint is selected for this Service. Ordinary L4 ambient traffic can work; Service-scoped L7 routing and policy need a waypoint.")
	}
	backendPods := []*store.Object{}
	selector := M(Path(svc.Obj, "spec", "selector"))
	for _, pod := range ws.List(svc.Cluster, "", "pods", svc.Namespace, nil) {
		if len(selector) > 0 && matches(selector, pod.Labels()) {
			backendPods = append(backendPods, pod)
		}
	}
	destMesh := false
	for _, pod := range backendPods {
		destMesh = destMesh || podInAmbient(ws, pod)
	}
	if destMesh {
		state, detail, refs := pathZtunnels(ws, svc.Cluster, backendPods)
		hop("destination-ztunnel", "Destination ztunnel", svc.Cluster, state, detail, refs...)
	} else {
		hop("destination-dataplane", "Destination dataplane", svc.Cluster, "unknown", "Ambient interception is not established for the selected backend Pods. Inspect Pod enrolment, sidecars and capture scope.")
	}
	r, err := Investigate(ws, svc.Cluster, svc.Namespace, svc.Name, "")
	if err != nil {
		return nil, err
	}
	v := &View{Investigation: r, Subject: reference(svc)}
	addSuggestedFixes(v)
	for _, step := range r.Steps {
		if step.ID == "dataplane" && step.State != "fail" {
			p.Notes = append(p.Notes, step.Detail)
			continue
		}
		if step.State == "fail" || step.State == "unknown" || step.State == "warning" {
			p.Checks = append(p.Checks, step)
		}
	}
	routeRefs := []Ref{}
	routeState := "pass"
	routeFailures := 0
	for _, step := range r.Steps {
		if strings.HasPrefix(step.ID, "route-") {
			for _, ev := range step.Evidence {
				if ev.Ref.Kind == "HTTPRoute" {
					seen := false
					for _, ref := range routeRefs {
						seen = seen || ref == ev.Ref
					}
					if !seen {
						routeRefs = append(routeRefs, ev.Ref)
					}
				}
			}
			if step.State == "fail" {
				routeState = "fail"
				routeFailures++
			} else if step.State == "unknown" && routeState != "fail" {
				routeState = "unknown"
			}
		}
		if step.State == "fail" && (step.ID == "waypoint" || step.ID == "traffic-type" || step.ID == "waypoint-listener" || step.ID == "waypoint-namespace") {
			for i := range p.Hops {
				if p.Hops[i].ID == "waypoint" {
					p.Hops[i].State = "fail"
					p.Hops[i].Detail = step.Detail
				}
			}
		}
	}
	if len(routeRefs) > 0 {
		detail := fmt.Sprintf("%d related HTTPRoute resources; %d failed attachment checks. Related routes are not necessarily selected by this request's hostname, method or path.", len(routeRefs), routeFailures)
		routing := PathHop{"routing", "Route attachments", svc.Cluster, routeState, detail, routeRefs}
		p.Configuration = append(p.Configuration, routing)
	}
	ready := findPathStep(r, "endpoints")
	state, detail := "unknown", "EndpointSlice evidence is not available."
	if ready != nil {
		state, detail = ready.State, ready.Detail
	}
	backendRefs := []Ref{}
	for _, pod := range backendPods {
		backendRefs = append(backendRefs, reference(pod))
	}
	backendRefs = append(backendRefs, reference(svc))
	hop("backend", "Destination Pods", svc.Cluster, state, detail+" These are Service-selector candidates, not proof of the endpoint selected by a request.", backendRefs...)
	p.Observed = pathObserved(ws, p, pods, backendPods)
	return p, nil
}

func pathSourceGlobalWaypoint(ws *store.Workspace, q PathRequest) (string, *store.Object, []Ref) {
	for _, entry := range ws.List(q.Source.Cluster, "networking.istio.io", "serviceentries", "", nil) {
		lb := entry.Labels()
		if lb["solo.io/parent-service"] != q.Destination.Name || lb["solo.io/parent-service-namespace"] != q.Destination.Namespace {
			continue
		}
		matchesHost := false
		for _, host := range L(Path(entry.Obj, "spec", "hosts")) {
			matchesHost = matchesHost || S(host) == q.Host
		}
		if !matchesHost {
			continue
		}
		bound := false
		for _, v := range L(Path(entry.Obj, "status", "conditions")) {
			c := M(v)
			bound = bound || S(c["type"]) == "istio.io/WaypointBound" && S(c["status"]) == "True"
		}
		if !bound {
			continue
		}
		// Generated aliases live in the mesh root namespace but inherit their
		// parent Service namespace's binding unless an explicit override exists.
		probe := *entry
		probe.Namespace = q.Destination.Namespace
		_, name, wp := Waypoint(ws, &probe)
		if name == "" {
			continue
		}
		refs := []Ref{}
		if wp != nil {
			refs = append(refs, pathGatewayRefs(ws, wp)...)
		}
		refs = append(refs, reference(entry))
		return name, wp, refs
	}
	return "", nil, nil
}

func pathGatewayRefs(ws *store.Workspace, gateway *store.Object) []Ref {
	refs := []Ref{}
	for _, pod := range ws.List(gateway.Cluster, "", "pods", gateway.Namespace, nil) {
		if pod.Labels()["gateway.networking.k8s.io/gateway-name"] == gateway.Name {
			refs = append(refs, reference(pod))
		}
	}
	return append(refs, reference(gateway))
}
func pathTrustDomain(ws *store.Workspace, c string) (string, bool) {
	if cm := ws.Get(c, "", "configmaps", "istio-system", "istio"); cm != nil {
		var mesh struct {
			TrustDomain string `json:"trustDomain"`
		}
		if yaml.Unmarshal([]byte(S(Path(cm.Obj, "data", "mesh"))), &mesh) == nil && mesh.TrustDomain != "" {
			return mesh.TrustDomain, true
		}
	}
	return "cluster.local", false
}
func podInAmbient(ws *store.Workspace, pod *store.Object) bool {
	if pod.Labels()["istio.io/dataplane-mode"] == "none" || pod.Labels()["istio.io/dataplane-mode"] == "sidecar" {
		return false
	}
	for _, ct := range L(Path(pod.Obj, "spec", "containers")) {
		if S(M(ct)["name"]) == "istio-proxy" {
			return false
		}
	}
	ns := ws.Get(pod.Cluster, "", "namespaces", "", pod.Namespace)
	return pod.Labels()["istio.io/dataplane-mode"] == "ambient" || (ns != nil && ns.Labels()["istio.io/dataplane-mode"] == "ambient")
}
func pathZtunnels(ws *store.Workspace, c string, pods []*store.Object) (string, string, []Ref) {
	nodes := map[string]bool{}
	for _, p := range pods {
		nodes[S(Path(p.Obj, "spec", "nodeName"))] = true
	}
	refs := []Ref{}
	covered := map[string]bool{}
	for _, p := range ws.List(c, "", "pods", "", nil) {
		if p.Labels()["gateway.networking.k8s.io/gateway-name"] != "" || p.Labels()["istio.io/gateway-name"] != "" {
			continue
		}
		node := S(Path(p.Obj, "spec", "nodeName"))
		if !nodes[node] || node == "" {
			continue
		}
		zt := p.Labels()["app"] == "ztunnel"
		for _, ct := range L(Path(p.Obj, "spec", "containers")) {
			image := S(M(ct)["image"])
			zt = zt || strings.Contains(image, "/ztunnel:") || strings.Contains(image, "/ztunnel@")
		}
		if zt {
			refs = append(refs, reference(p))
			if podReady(p) {
				covered[node] = true
			}
		}
	}
	if len(nodes) > 0 && len(covered) == len(nodes) {
		return "pass", "Ready ztunnel Pods are captured on the workload nodes. This establishes infrastructure configuration, not observed traversal.", refs
	}
	return "unknown", "Ready ztunnel coverage was not established for every selected workload node. Inspect node placement, ztunnel readiness and collection scope.", refs
}
func gatewayPathState(gw *store.Object) (string, string) {
	r := &Investigation{Counts: map[string]int{}}
	conditions(r, gw, "status", []string{"Accepted", "Programmed"}, "gateway")
	state := "pass"
	for _, s := range r.Steps {
		if s.State == "fail" {
			return "fail", s.Detail
		}
		if s.State != "pass" {
			state = "unknown"
		}
	}
	return state, gw.Namespace + "/" + gw.Name + " · " + S(Path(gw.Obj, "spec", "gatewayClassName")) + ". Current controller conditions are checked; proxy delivery is separate evidence."
}
func findPathStep(r *Investigation, id string) *Step {
	for i := range r.Steps {
		if r.Steps[i].ID == id {
			return &r.Steps[i]
		}
	}
	return nil
}
func pathObserved(ws *store.Workspace, p *RequestPath, src, dst []*store.Object) PathObservation {
	o := PathObservation{Sources: []string{}, Examples: []store.Flow{}, Note: "No matching request evidence was found in the captured traffic. This is not evidence that the path failed."}
	q := p.Request
	cross := q.Source.Cluster != q.Destination.Cluster
	ports := map[int]bool{q.Port: true, 15008: true}
	if svc := ws.Get(q.Destination.Cluster, "", "services", q.Destination.Namespace, q.Destination.Name); svc != nil {
		for _, v := range L(Path(svc.Obj, "spec", "ports")) {
			port := M(v)
			if N(port["port"]) == q.Port {
				if target := N(port["targetPort"]); target > 0 {
					ports[target] = true
				}
			}
		}
	}
	for _, f := range ws.Flows(q.Source.Cluster) {
		if f.Source == "coredns" || f.Protocol == "dns" {
			continue
		}
		sourceMatch := false
		for _, pod := range src {
			ip := S(Path(pod.Obj, "status", "podIP"))
			if f.SrcNamespace == pod.Namespace && (f.SrcPod == pod.Name || (q.Source.Kind != "Pod" && q.Source.Kind != "ServiceAccount" && f.SrcPod == q.Source.Name)) {
				sourceMatch = true
			}
			if ip != "" && f.SrcIP == ip {
				sourceMatch = true
			}
		}
		if !sourceMatch {
			continue
		}
		destinationMatch := f.DstHost == q.Host && q.Host != ""
		if !cross {
			destinationMatch = destinationMatch || (f.DstNamespace == q.Destination.Namespace && f.DstWorkload == q.Destination.Name)
			for _, pod := range dst {
				ip := S(Path(pod.Obj, "status", "podIP"))
				destinationMatch = destinationMatch || (ip != "" && f.DstIP == ip)
			}
		}
		if !destinationMatch {
			continue
		}
		if f.DstPort > 0 && !ports[f.DstPort] {
			continue
		}
		if (q.Method != "" && f.Method == "") || (q.Path != "" && f.Path == "") {
			o.WithoutHTTP += f.Count
			continue
		}
		if q.Method != "" && f.Method != q.Method || q.Path != "" && f.Path != q.Path {
			continue
		}
		o.Records += f.Count
		if f.Verdict == "denied" {
			o.Denied += f.Count
		}
		if !contains(o.Sources, f.Source) {
			o.Sources = append(o.Sources, f.Source)
		}
		if len(o.Examples) < 12 {
			o.Examples = append(o.Examples, f)
		}
	}
	if o.Records > 0 {
		o.Note = "Matching captured traffic records exist for these endpoints and request filters. They do not confirm every hop, current health or successful application responses."
	}
	if cross {
		o.Note += " Hostname-level evidence does not prove that the selected remote cluster served the request."
	}
	return o
}
