package ambient

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/tjorourke/flightbay/internal/store"
)

// l4Access evaluates each captured destination separately. A selector matching
// one endpoint must never be taken as evidence about all the other endpoints.
type l4Access struct {
	State    string
	Policies []string
}

func l4Policies(ws *store.Workspace, svc, pod *store.Object, port int) []AuthzPolicy {
	var out []AuthzPolicy
	root := rootNamespace(ws, svc.Cluster)
	_, rootKnown := capturedRootNamespace(ws, svc.Cluster)
	for _, o := range ws.List(svc.Cluster, SecurityGroup, "authorizationpolicies", "", nil) {
		if o.Namespace != svc.Namespace && o.Namespace != root {
			continue
		}
		if o.Namespace != svc.Namespace && !rootKnown {
			continue
		}
		spec := M(o.Obj["spec"])
		if policyEnforcedAt(spec) != "ztunnel" {
			continue
		}
		sel := M(Path(spec, "selector", "matchLabels"))
		if len(sel) > 0 && (pod == nil || !matches(sel, pod.Labels())) {
			continue
		}
		p := ParseAuthzPolicy(reference(o), spec, "ztunnel")
		// HTTP attributes are unavailable at ztunnel. Do not infer admission
		// from a fabricated HTTP request (DENY has different missing-field rules).
		for i, r := range p.Rules {
			for _, op := range r.To {
				if len(op.Methods)+len(op.NotMethods)+len(op.Paths)+len(op.NotPaths)+len(op.Hosts)+len(op.NotHosts) > 0 {
					p.Unsupported = append(p.Unsupported, UnsupportedField{i + 1, "L7 attribute at ztunnel"})
				}
			}
		}
		out = append(out, p)
	}
	return out
}

func analyseL4(ws *store.Workspace, svc *store.Object, principal string) l4Access {
	result := l4Access{State: "admitted"}
	_, rootKnown := capturedRootNamespace(ws, svc.Cluster)
	policyComplete := ws.CollectionState(svc.Cluster, SecurityGroup, "authorizationpolicies", "AuthorizationPolicy", true).State == store.StateComplete
	if p := ws.Policy(); p != nil && (!p.NamespaceAllowed(rootNamespace(ws, svc.Cluster)) || !p.NamespaceAllowed(svc.Namespace)) {
		policyComplete = false
	}
	if !rootKnown {
		result.State = "unknown"
	}
	if !policyComplete || ws.CollectionState(svc.Cluster, "", "pods", "Pod", true).State != store.StateComplete || len(servicePorts(svc)) == 0 {
		result.State = "unknown"
	}
	if principal == "" {
		result.State = "unknown"
	}
	seen := map[string]bool{}
	pods := []*store.Object{}
	sel := M(Path(svc.Obj, "spec", "selector"))
	for _, pod := range ws.List(svc.Cluster, "", "pods", svc.Namespace, nil) {
		if len(sel) > 0 && matches(sel, pod.Labels()) {
			pods = append(pods, pod)
		}
	}
	if len(pods) == 0 {
		pods = append(pods, nil)
		result.State = "unknown"
	}
	for _, ep := range ws.List(svc.Cluster, "discovery.k8s.io", "endpointslices", svc.Namespace, nil) {
		if ep.Labels()["kubernetes.io/service-name"] != svc.Name {
			continue
		}
		for _, raw := range L(ep.Obj["endpoints"]) {
			ref := M(M(raw)["targetRef"])
			if S(ref["kind"]) != "Pod" || ws.Get(svc.Cluster, "", "pods", defaultString(S(ref["namespace"]), svc.Namespace), S(ref["name"])) == nil {
				if result.State != "blocked" {
					result.State = "unknown"
				}
			}
		}
	}
	for _, pod := range pods {
		for _, raw := range L(Path(svc.Obj, "spec", "ports")) {
			sp := M(raw)
			port := N(sp["targetPort"])
			if port == 0 {
				port = N(sp["port"])
			}
			knownPort := S(sp["targetPort"]) == "" || N(sp["targetPort"]) > 0
			if !knownPort && pod != nil {
				for _, c := range L(Path(pod.Obj, "spec", "containers")) {
					for _, p := range L(M(c)["ports"]) {
						if S(M(p)["name"]) == S(sp["targetPort"]) {
							port, knownPort = N(M(p)["containerPort"]), true
						}
					}
				}
			}
			policies := l4Policies(ws, svc, pod, port)
			for _, p := range policies {
				seen[p.Ref.Namespace+"/"+p.Ref.Name] = true
			}
			r := reqModel{Port: port, Principal: principal, SourceKnown: principal != ""}
			parts := strings.Split(principal, "/ns/")
			if len(parts) == 2 {
				r.Namespace = strings.Split(parts[1], "/sa/")[0]
			}
			v := decide(policies, r)
			if !v.Known || !knownPort {
				if result.State != "blocked" {
					result.State = "unknown"
				}
			} else if v.Status != 200 {
				if (v.By != nil && v.By.Action == "DENY") || (policyComplete && rootKnown && pod != nil && principal != "") {
					result.State = "blocked"
				} else if result.State != "blocked" {
					result.State = "unknown"
				}
			}
		}
	}
	for name := range seen {
		result.Policies = append(result.Policies, name)
	}
	sort.Strings(result.Policies)
	return result
}

func waypointL4Warning(ws *store.Workspace, svc *store.Object) string {
	a := analyseL4(ws, svc, waypointPrincipal(ws, svc))
	if a.State == "admitted" {
		return ""
	}
	return fmt.Sprintf("Service %s/%s: downstream ztunnel sees the waypoint identity, not the original caller. L4 admission is %s. Review policies %s before binding. A refusal may surface as 503 with UC at the waypoint; UC alone does not diagnose a policy denial.", svc.Namespace, svc.Name, a.State, defaultString(strings.Join(a.Policies, ", "), "(none captured)"))
}

// l4ExclusiveWaypoint is deliberately conservative: every ALLOW branch must
// constrain the caller to this exact identity, across every destination Pod.
func l4ExclusiveWaypoint(policies []AuthzPolicy, principal string) bool {
	if principal == "" {
		return false
	}
	found := false
	for _, p := range policies {
		if p.Enforced != "ztunnel" || p.Action != "ALLOW" {
			continue
		}
		found = true
		if len(p.Unsupported) > 0 {
			return false
		}
		for _, r := range p.Rules {
			if len(r.From) == 0 {
				return false
			}
			for _, s := range r.From {
				if len(s.Principals) != 1 || s.Principals[0] != principal {
					return false
				}
			}
		}
	}
	return found
}

func serviceWaypointLock(ws *store.Workspace, svc *store.Object) bool {
	if _, known := capturedRootNamespace(ws, svc.Cluster); !known {
		return false
	}
	if ws.CollectionState(svc.Cluster, SecurityGroup, "authorizationpolicies", "AuthorizationPolicy", true).State != store.StateComplete || ws.CollectionState(svc.Cluster, "", "pods", "Pod", true).State != store.StateComplete {
		return false
	}
	principal := waypointPrincipal(ws, svc)
	if analyseL4(ws, svc, principal).State != "admitted" {
		return false
	}
	sel := M(Path(svc.Obj, "spec", "selector"))
	found := false
	for _, pod := range ws.List(svc.Cluster, "", "pods", svc.Namespace, nil) {
		if len(sel) == 0 || !matches(sel, pod.Labels()) {
			continue
		}
		found = true
		if !l4ExclusiveWaypoint(l4Policies(ws, svc, pod, 0), principal) {
			return false
		}
	}
	return found
}

// Capture the entire policy/identity dependency sets, including absence, so a
// newly added ALLOW cannot invalidate the preview without a fresh review.
func l4DependencyEvidence(ws *store.Workspace, cluster string) []Evidence {
	var out []Evidence
	for _, k := range []struct{ group, resource, kind string }{{SecurityGroup, "authorizationpolicies", "AuthorizationPolicy"}, {"", "pods", "Pod"}, {"", "configmaps", "ConfigMap"}, {"", "services", "Service"}, {"", "namespaces", "Namespace"}} {
		b, _ := json.Marshal(l4DependencyValue(ws, cluster, k.group, k.resource))
		out = append(out, Evidence{Ref: Ref{Cluster: cluster, Group: k.group, Resource: k.resource, Kind: k.kind}, Field: "l4-dependencies", Value: string(b)})
	}
	return out
}

func l4DependencyValue(ws *store.Workspace, cluster, group, resource string) any {
	values := map[string]any{}
	for _, o := range ws.List(cluster, group, resource, "", nil) {
		values[o.Namespace+"/"+o.Name] = L4DependencyObject(resource, o.Name, o.Obj)
	}
	return values
}

// L4DependencyObject reduces an object to the fields the L4 analysis reads.
// The captured workspace holds redacted copies (secret-looking Pod args and
// embedded JSON are rewritten), so comparing whole specs or ConfigMap data with
// the raw live object reports drift that never happened. Both the capture and
// the server preflight must call this with the same arguments.
func L4DependencyObject(resource, name string, obj map[string]any) any {
	uid, labels := Path(obj, "metadata", "uid"), Path(obj, "metadata", "labels")
	switch resource {
	case "pods":
		ports := []any{}
		for _, c := range L(Path(obj, "spec", "containers")) {
			ports = append(ports, M(c)["ports"])
		}
		return map[string]any{"uid": uid, "labels": labels, "serviceAccountName": Path(obj, "spec", "serviceAccountName"), "ports": ports}
	case "configmaps":
		// Only the Istio mesh config (root namespace, trust domain) is read.
		if name != "istio" && !strings.HasPrefix(name, "istio-") {
			return map[string]any{"uid": uid}
		}
		return map[string]any{"uid": uid, "mesh": Path(obj, "data", "mesh")}
	}
	return map[string]any{"uid": uid, "spec": obj["spec"], "labels": labels, "data": obj["data"]}
}
