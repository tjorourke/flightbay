package ambient

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tjorourke/flightbay/internal/store"
)

// AccessRequest is a Service-targeted AuthorizationPolicy built from rules a
// person picks: who is calling (namespaces, captured Services, principals),
// what they ask for (methods, paths) and request headers. Rules are ORed;
// everything inside one rule must match.
type AccessRequest struct {
	Action string       `json:"action"`
	Rules  []AccessRule `json:"rules"`
}

type AccessRule struct {
	SourceNamespaces []string      `json:"sourceNamespaces,omitempty"`
	SourceServices   []string      `json:"sourceServices,omitempty"` // namespace/name of captured Services
	Principals       []string      `json:"principals,omitempty"`
	Methods          []string      `json:"methods,omitempty"`
	Paths            []string      `json:"paths,omitempty"`
	Headers          []HeaderMatch `json:"headers,omitempty"`
}

type HeaderMatch struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`
	Not    bool     `json:"not,omitempty"`
}

var httpMethods = []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "CONNECT", "TRACE"}

// servicePrincipals turns a captured Service into the SPIFFE principals of
// the Pods behind it. It refuses rather than guesses when the trust domain or
// the Pods are not in the capture.
func servicePrincipals(ws *store.Workspace, cluster, ref, td string) ([]string, error) {
	ns, name, ok := strings.Cut(ref, "/")
	if !ok || ns == "" || name == "" {
		return nil, fmt.Errorf("caller Service %q must be namespace/name", ref)
	}
	svc := ws.Get(cluster, "", "services", ns, name)
	if svc == nil {
		return nil, fmt.Errorf("caller Service %s is not in this capture", ref)
	}
	sel := map[string]string{}
	for k, v := range M(Path(svc.Obj, "spec", "selector")) {
		sel[k] = S(v)
	}
	if len(sel) == 0 {
		return nil, fmt.Errorf("caller Service %s has no workload selector, so its callers' identity cannot be established; add the principal instead", ref)
	}
	if td == "" {
		return nil, fmt.Errorf("the mesh trust domain was not established from the capture; enter caller principals directly (trust-domain/ns/NAMESPACE/sa/ACCOUNT)")
	}
	seen := map[string]bool{}
	for _, pod := range ws.List(cluster, "", "pods", ns, nil) {
		lb := pod.Labels()
		match := true
		for k, v := range sel {
			if lb[k] != v {
				match = false
			}
		}
		if match {
			seen[td+"/ns/"+ns+"/sa/"+defaultString(S(Path(pod.Obj, "spec", "serviceAccountName")), "default")] = true
		}
	}
	if len(seen) == 0 {
		return nil, fmt.Errorf("no captured Pods back caller Service %s, so its identity is unknown; add the principal instead", ref)
	}
	out := []string{}
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

func generateAccessRule(ws *store.Workspace, req Request, svc *store.Object) (map[string]any, []string, error) {
	a := req.Access
	if a == nil || len(a.Rules) == 0 {
		return nil, nil, fmt.Errorf("add at least one rule")
	}
	action := defaultString(a.Action, "DENY")
	if action != "ALLOW" && action != "DENY" {
		return nil, nil, fmt.Errorf("action must be ALLOW or DENY")
	}
	if len(a.Rules) > 10 {
		return nil, nil, fmt.Errorf("use at most ten rules in one policy")
	}
	td := clusterTrustDomain(ws, req.Cluster)
	notes := []string{}
	rules := []any{}
	for i, r := range a.Rules {
		n := i + 1
		rule := map[string]any{}
		source := map[string]any{}
		for _, ns := range r.SourceNamespaces {
			if ws.Get(req.Cluster, "", "namespaces", "", ns) == nil {
				return nil, nil, fmt.Errorf("rule %d: namespace %q is not in this capture", n, ns)
			}
		}
		if len(r.SourceNamespaces) > 0 {
			source["namespaces"] = r.SourceNamespaces
		}
		principals := []string{}
		for _, ref := range r.SourceServices {
			ps, err := servicePrincipals(ws, req.Cluster, ref, td)
			if err != nil {
				return nil, nil, fmt.Errorf("rule %d: %v", n, err)
			}
			principals = append(principals, ps...)
		}
		for _, p := range r.Principals {
			p = strings.TrimPrefix(strings.TrimSpace(p), "spiffe://")
			if p == "" {
				continue
			}
			if p != "*" && !strings.Contains(p, "/ns/") && !strings.HasSuffix(p, "*") && !strings.HasPrefix(p, "*") {
				return nil, nil, fmt.Errorf("rule %d: principal %q should look like trust-domain/ns/NAMESPACE/sa/ACCOUNT", n, p)
			}
			principals = append(principals, p)
		}
		if len(principals) > 0 {
			sort.Strings(principals)
			source["principals"] = dedupe(principals)
		}
		if len(source) > 0 {
			rule["from"] = []any{map[string]any{"source": source}}
		}
		op := map[string]any{}
		for _, m := range r.Methods {
			if !contains(httpMethods, m) {
				return nil, nil, fmt.Errorf("rule %d: unsupported HTTP method %q", n, m)
			}
		}
		if len(r.Methods) > 0 {
			op["methods"] = r.Methods
		}
		for _, p := range r.Paths {
			if p != "*" && !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "*") {
				return nil, nil, fmt.Errorf("rule %d: paths must start with /, or use * as a prefix or suffix wildcard", n)
			}
		}
		if len(r.Paths) > 0 {
			op["paths"] = r.Paths
		}
		if len(op) > 0 {
			rule["to"] = []any{map[string]any{"operation": op}}
		}
		when := []any{}
		for _, h := range r.Headers {
			name := strings.ToLower(strings.TrimSpace(h.Name))
			vals := []string{}
			for _, v := range h.Values {
				if v = strings.TrimSpace(v); v != "" {
					vals = append(vals, v)
				}
			}
			if !headerName(name) || len(vals) == 0 {
				return nil, nil, fmt.Errorf("rule %d: each header needs a valid name and at least one value", n)
			}
			key := "values"
			if h.Not {
				key = "notValues"
			}
			when = append(when, map[string]any{"key": "request.headers[" + name + "]", key: vals})
		}
		if len(when) > 0 {
			rule["when"] = when
		}
		if len(rule) == 0 {
			return nil, nil, fmt.Errorf("rule %d is empty; an empty %s rule would match every request", n, action)
		}
		rules = append(rules, rule)
	}
	if action == "DENY" {
		notes = append(notes, "DENY policies are evaluated before ALLOW policies, so a matching request is refused whatever else allows it.")
	} else {
		notes = append(notes, "An ALLOW policy on this Service refuses every request that no ALLOW rule matches. Existing ALLOW policies stay additive.")
	}
	notes = append(notes, "This Service-targeted policy is enforced by the waypoint only. Traffic that skips the waypoint (ingress without istio.io/ingress-use-waypoint, Pod IPs, headless Services) is not checked.", "Header values match exactly and are case-sensitive. Header names are matched lowercase.")
	spec := map[string]any{"targetRefs": []any{map[string]any{"group": "", "kind": "Service", "name": req.Service}}, "action": action, "rules": rules}
	return spec, notes, nil
}

func dedupe(in []string) []string {
	out := []string{}
	for i, v := range in {
		if i == 0 || in[i-1] != v {
			out = append(out, v)
		}
	}
	return out
}
