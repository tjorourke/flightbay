package ambient

import (
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/tjorourke/flightbay/internal/store"
)

// WaypointBinding says which waypoint a Service uses and why, in terms a
// person can check: which label on which object selected it, whether it is
// shared, and whether it is running.
type WaypointBinding struct {
	Name       string `json:"name,omitempty"`
	Namespace  string `json:"namespace,omitempty"`
	Class      string `json:"class,omitempty"`
	Product    string `json:"product,omitempty"` // istio, agentgateway, or the controller name
	Source     string `json:"source"`            // service-label, namespace-label, opt-out, none
	Label      string `json:"label,omitempty"`   // the label exactly as set
	LabelOn    string `json:"labelOn,omitempty"` // "Service bookinfo/reviews" or "Namespace bookinfo"
	Central    bool   `json:"central,omitempty"` // the waypoint lives in another namespace
	For        string `json:"for,omitempty"`     // istio.io/waypoint-for
	Programmed string `json:"programmed,omitempty"`
	Ready      int    `json:"ready"`
	Pods       int    `json:"pods"`
	UsedBy     int    `json:"usedBy"` // Services in the cluster bound to it, this one included
	Captured   bool   `json:"captured"`
	Ref        *Ref   `json:"ref,omitempty"`
}

func waypointBinding(ws *store.Workspace, svc *store.Object) *WaypointBinding {
	b := &WaypointBinding{Source: "none"}
	own := svc.Labels()
	var nsl map[string]string
	if n := ws.Get(svc.Cluster, "", "namespaces", "", svc.Namespace); n != nil {
		nsl = n.Labels()
	}
	name, ns := own["istio.io/use-waypoint"], own["istio.io/use-waypoint-namespace"]
	switch {
	case name == "none":
		b.Source, b.Label, b.LabelOn = "opt-out", "istio.io/use-waypoint=none", "Service "+svc.Namespace+"/"+svc.Name
		return b
	case name != "":
		b.Source, b.Label, b.LabelOn = "service-label", "istio.io/use-waypoint="+name, "Service "+svc.Namespace+"/"+svc.Name
	case nsl["istio.io/use-waypoint"] != "" && nsl["istio.io/use-waypoint"] != "none":
		name, ns = nsl["istio.io/use-waypoint"], nsl["istio.io/use-waypoint-namespace"]
		b.Source, b.Label, b.LabelOn = "namespace-label", "istio.io/use-waypoint="+name, "Namespace "+svc.Namespace
	default:
		return b
	}
	if ns == "" {
		ns = svc.Namespace
	} else {
		b.Label += ", istio.io/use-waypoint-namespace=" + ns
	}
	b.Name, b.Namespace, b.Central = name, ns, ns != svc.Namespace
	wp := ws.Get(svc.Cluster, GatewayGroup, "gateways", ns, name)
	if wp == nil {
		return b
	}
	b.Captured = true
	r := reference(wp)
	b.Ref = &r
	b.Class = S(Path(wp.Obj, "spec", "gatewayClassName"))
	b.For = defaultString(wp.Labels()["istio.io/waypoint-for"], "service")
	if gc := ws.Get(svc.Cluster, GatewayGroup, "gatewayclasses", "", b.Class); gc != nil {
		ctrl := S(Path(gc.Obj, "spec", "controllerName"))
		switch {
		case strings.HasPrefix(ctrl, "istio.io/"):
			b.Product = "istio"
		case strings.Contains(ctrl, "agentgateway"):
			b.Product = "agentgateway"
		default:
			b.Product = ctrl
		}
	}
	for _, c := range L(Path(wp.Obj, "status", "conditions")) {
		if S(M(c)["type"]) == "Programmed" {
			b.Programmed = S(M(c)["status"])
		}
	}
	for _, p := range ws.List(svc.Cluster, "", "pods", ns, nil) {
		if p.Labels()["gateway.networking.k8s.io/gateway-name"] != name {
			continue
		}
		b.Pods++
		for _, c := range L(Path(p.Obj, "status", "conditions")) {
			if S(M(c)["type"]) == "Ready" && S(M(c)["status"]) == "True" {
				b.Ready++
			}
		}
	}
	for _, other := range ws.List(svc.Cluster, "", "services", "", nil) {
		if other.Labels()["gateway.networking.k8s.io/gateway-name"] != "" {
			continue
		}
		if ons, oname, _ := Waypoint(ws, other); oname == name && ons == ns {
			b.UsedBy++
		}
	}
	return b
}

// accessFromPolicy reads a Service-targeted AuthorizationPolicy back into the
// Allow or deny requests editor. Every Source, Operation and Condition field
// the Istio API defines maps to a field here. Anything else (a field from a
// newer API) loads as rules YAML so the policy is still editable.
func accessFromPolicy(o *store.Object) (*AccessRequest, bool) {
	action := defaultString(S(Path(o.Obj, "spec", "action")), "ALLOW")
	if !contains([]string{"ALLOW", "DENY", "AUDIT", "CUSTOM"}, action) {
		return nil, false
	}
	a := &AccessRequest{Action: action, Provider: S(Path(o.Obj, "spec", "provider", "name"))}
	rules := L(Path(o.Obj, "spec", "rules"))
	if len(rules) == 0 {
		return nil, false
	}
	strs := func(v any) []string {
		out := []string{}
		for _, x := range L(v) {
			out = append(out, S(x))
		}
		return out
	}
	known := func(m map[string]any, keys ...string) bool {
		for k := range m {
			if !contains(keys, k) {
				return false
			}
		}
		return true
	}
	structured := true
	for _, raw := range rules {
		rm := M(raw)
		if !known(rm, "from", "to", "when") {
			structured = false
			break
		}
		r := AccessRule{}
		for _, f := range L(rm["from"]) {
			src := M(M(f)["source"])
			if !known(M(f), "source") || !known(src, "principals", "notPrincipals", "requestPrincipals", "notRequestPrincipals", "namespaces", "notNamespaces", "serviceAccounts", "notServiceAccounts", "ipBlocks", "notIpBlocks", "remoteIpBlocks", "notRemoteIpBlocks") {
				structured = false
			}
			r.From = append(r.From, AccessSource{Principals: strs(src["principals"]), NotPrincipals: strs(src["notPrincipals"]), RequestPrincipals: strs(src["requestPrincipals"]), NotRequestPrincipals: strs(src["notRequestPrincipals"]), Namespaces: strs(src["namespaces"]), NotNamespaces: strs(src["notNamespaces"]), ServiceAccounts: strs(src["serviceAccounts"]), NotServiceAccounts: strs(src["notServiceAccounts"]), IPBlocks: strs(src["ipBlocks"]), NotIPBlocks: strs(src["notIpBlocks"]), RemoteIPBlocks: strs(src["remoteIpBlocks"]), NotRemoteIPBlocks: strs(src["notRemoteIpBlocks"])})
		}
		for _, t := range L(rm["to"]) {
			op := M(M(t)["operation"])
			if !known(M(t), "operation") || !known(op, "hosts", "notHosts", "ports", "notPorts", "methods", "notMethods", "paths", "notPaths") {
				structured = false
			}
			r.To = append(r.To, AccessOperation{Hosts: strs(op["hosts"]), NotHosts: strs(op["notHosts"]), Ports: strs(op["ports"]), NotPorts: strs(op["notPorts"]), Methods: strs(op["methods"]), NotMethods: strs(op["notMethods"]), Paths: strs(op["paths"]), NotPaths: strs(op["notPaths"])})
		}
		for _, w := range L(rm["when"]) {
			wm := M(w)
			if !known(wm, "key", "values", "notValues") {
				structured = false
			}
			r.When = append(r.When, AccessCondition{Key: S(wm["key"]), Values: strs(wm["values"]), NotValues: strs(wm["notValues"])})
		}
		a.Rules = append(a.Rules, r)
	}
	if !structured {
		b, err := yaml.Marshal(rules)
		if err != nil {
			return nil, false
		}
		a.Rules, a.RulesYAML = nil, string(b)
	}
	return a, true
}

// PolicyEntry is one Istio security policy that applies to a Service, with
// why it applies and where it is enforced.
type PolicyEntry struct {
	Ref      Ref    `json:"ref"`
	Action   string `json:"action,omitempty"`
	Scope    string `json:"scope"`    // service, waypoint, workload, namespace, mesh
	Reason   string `json:"reason"`   // a sentence a person can check
	Enforced string `json:"enforced"` // waypoint or ztunnel
	Status   string `json:"status,omitempty"`
	Rules    int    `json:"rules"`
	// Warning is set when the policy is accepted but cannot be enforced, for
	// example an Istio policy on a Service behind an agentgateway waypoint.
	Warning string `json:"warning,omitempty"`
}

func rootNamespace(ws *store.Workspace, cluster string) string {
	for _, cm := range ws.List(cluster, "", "configmaps", "istio-system", nil) {
		if cm.Name != "istio" && !strings.HasPrefix(cm.Name, "istio-") {
			continue
		}
		var mesh map[string]any
		if yaml.Unmarshal([]byte(S(Path(cm.Obj, "data", "mesh"))), &mesh) == nil {
			if v := S(mesh["rootNamespace"]); v != "" {
				return v
			}
		}
	}
	return "istio-system"
}

// servicePolicies lists AuthorizationPolicy, PeerAuthentication and
// RequestAuthentication objects that reach this Service: by targetRefs to the
// Service or its waypoint, by a selector matching its Pods, namespace-wide,
// or mesh-wide from the root namespace.
func servicePolicies(ws *store.Workspace, svc *store.Object) []PolicyEntry {
	out := []PolicyEntry{}
	wpNS, wpName, _ := Waypoint(ws, svc)
	podSel := M(Path(svc.Obj, "spec", "selector"))
	root := rootNamespace(ws, svc.Cluster)
	podLabels := []map[string]string{}
	for _, p := range ws.List(svc.Cluster, "", "pods", svc.Namespace, nil) {
		if len(podSel) > 0 && matches(podSel, p.Labels()) {
			podLabels = append(podLabels, p.Labels())
		}
	}
	for _, kind := range []struct{ res, kind string }{{"authorizationpolicies", "AuthorizationPolicy"}, {"peerauthentications", "PeerAuthentication"}, {"requestauthentications", "RequestAuthentication"}} {
		for _, ns := range dedupe(sortedPair(svc.Namespace, root, wpNS)) {
			for _, o := range ws.List(svc.Cluster, "security.istio.io", kind.res, ns, nil) {
				e := PolicyEntry{Ref: reference(o), Action: S(Path(o.Obj, "spec", "action")), Rules: len(L(Path(o.Obj, "spec", "rules")))}
				if o.Kind == "AuthorizationPolicy" && e.Action == "" {
					e.Action = "ALLOW"
				}
				for _, c := range L(Path(o.Obj, "status", "conditions")) {
					if m := S(M(c)["message"]); m != "" {
						e.Status = S(M(c)["type"]) + ": " + m
					}
				}
				targets := L(Path(o.Obj, "spec", "targetRefs"))
				if tr := M(Path(o.Obj, "spec", "targetRef")); tr != nil {
					targets = append(targets, tr)
				}
				sel := M(Path(o.Obj, "spec", "selector", "matchLabels"))
				switch {
				case len(targets) > 0:
					for _, t := range targets {
						tm := M(t)
						k, n := S(tm["kind"]), S(tm["name"])
						if k == "Service" && n == svc.Name && o.Namespace == svc.Namespace {
							e.Scope, e.Reason, e.Enforced = "service", "targetRefs names this Service", "waypoint"
						} else if k == "Gateway" && n == wpName && o.Namespace == wpNS {
							e.Scope, e.Reason, e.Enforced = "waypoint", "targetRefs names this Service's waypoint "+wpNS+"/"+wpName+", so it applies to every Service behind it", "waypoint"
						}
					}
				case len(sel) > 0:
					if o.Namespace != svc.Namespace {
						break
					}
					for _, lb := range podLabels {
						if matches(sel, lb) {
							e.Scope, e.Reason, e.Enforced = "workload", "selector matches this Service's Pods", "ztunnel"
							break
						}
					}
				case o.Namespace == root:
					e.Scope, e.Reason, e.Enforced = "mesh", "no selector in the root namespace "+root+", so it applies across the mesh", "ztunnel"
				case o.Namespace == svc.Namespace:
					e.Scope, e.Reason, e.Enforced = "namespace", "no selector or targetRefs, so it applies to every workload in "+svc.Namespace, "ztunnel"
				}
				if e.Scope != "" && e.Enforced == "waypoint" {
					if b := waypointBinding(ws, svc); b.Product != "" && b.Product != "istio" {
						e.Warning = "Not enforced: " + b.Namespace + "/" + b.Name + " is an " + b.Product + " waypoint (" + b.Class + "), which does not apply Istio " + o.Kind + ". istiod still reports WaypointAccepted. Bind an Istio waypoint, or use the waypoint's own policy API."
					}
				}
				if e.Scope != "" {
					out = append(out, e)
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		order := map[string]int{"service": 0, "waypoint": 1, "workload": 2, "namespace": 3, "mesh": 4}
		return order[out[i].Scope] < order[out[j].Scope]
	})
	return out
}

func sortedPair(vals ...string) []string {
	out := []string{}
	for _, v := range vals {
		if v != "" {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}
