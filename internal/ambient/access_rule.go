package ambient

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/tjorourke/flightbay/internal/store"
)

// AccessRequest is a Service-targeted AuthorizationPolicy. It covers the
// whole rules model of security.istio.io/v1 AuthorizationPolicy:
//
//	rules[]            ORed
//	  from[]           ORed sources; fields inside one source ANDed
//	  to[]             ORed operations; fields inside one operation ANDed
//	  when[]           ANDed conditions: key + values / notValues
//
// RulesYAML is the escape hatch: the rules list written by hand, parsed
// here and validated by the API server's dry-run.
type AccessRequest struct {
	Action    string       `json:"action"`
	Provider  string       `json:"provider,omitempty"` // CUSTOM only
	Rules     []AccessRule `json:"rules,omitempty"`
	RulesYAML string       `json:"rulesYaml,omitempty"`
}

type AccessRule struct {
	From []AccessSource    `json:"from,omitempty"`
	To   []AccessOperation `json:"to,omitempty"`
	When []AccessCondition `json:"when,omitempty"`

	// Shorthand kept for older drafts and simple rules; folded into From,
	// To and When before generation.
	SourceNamespaces []string      `json:"sourceNamespaces,omitempty"`
	SourceServices   []string      `json:"sourceServices,omitempty"`
	Principals       []string      `json:"principals,omitempty"`
	Methods          []string      `json:"methods,omitempty"`
	Paths            []string      `json:"paths,omitempty"`
	Headers          []HeaderMatch `json:"headers,omitempty"`
}

// AccessSource mirrors Source. Services is Flightbay's own: captured Services
// resolved to the principals of the Pods behind them.
type AccessSource struct {
	Principals           []string `json:"principals,omitempty"`
	NotPrincipals        []string `json:"notPrincipals,omitempty"`
	RequestPrincipals    []string `json:"requestPrincipals,omitempty"`
	NotRequestPrincipals []string `json:"notRequestPrincipals,omitempty"`
	Namespaces           []string `json:"namespaces,omitempty"`
	NotNamespaces        []string `json:"notNamespaces,omitempty"`
	ServiceAccounts      []string `json:"serviceAccounts,omitempty"`
	NotServiceAccounts   []string `json:"notServiceAccounts,omitempty"`
	IPBlocks             []string `json:"ipBlocks,omitempty"`
	NotIPBlocks          []string `json:"notIpBlocks,omitempty"`
	RemoteIPBlocks       []string `json:"remoteIpBlocks,omitempty"`
	NotRemoteIPBlocks    []string `json:"notRemoteIpBlocks,omitempty"`
	Services             []string `json:"services,omitempty"`
}

// AccessOperation mirrors Operation.
type AccessOperation struct {
	Hosts      []string `json:"hosts,omitempty"`
	NotHosts   []string `json:"notHosts,omitempty"`
	Ports      []string `json:"ports,omitempty"`
	NotPorts   []string `json:"notPorts,omitempty"`
	Methods    []string `json:"methods,omitempty"`
	NotMethods []string `json:"notMethods,omitempty"`
	Paths      []string `json:"paths,omitempty"`
	NotPaths   []string `json:"notPaths,omitempty"`
}

// AccessCondition mirrors Condition.
type AccessCondition struct {
	Key       string   `json:"key"`
	Values    []string `json:"values,omitempty"`
	NotValues []string `json:"notValues,omitempty"`
}

type HeaderMatch struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`
	Not    bool     `json:"not,omitempty"`
}

var httpMethods = []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "CONNECT", "TRACE"}

// Condition keys Istio documents for AuthorizationPolicy.
var conditionKey = regexp.MustCompile(`^(request\.headers\[[^\]]+\]|source\.ip|remote\.ip|source\.namespace|source\.principal|source\.serviceAccount|request\.auth\.principal|request\.auth\.audiences|request\.auth\.presenter|request\.auth\.claims\[[^\]]+\](\[[^\]]+\])*|destination\.ip|destination\.port|connection\.sni|experimental\.envoy\.filters\..+)$`)

func cleanList(in []string) []string {
	out := []string{}
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

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

// fold moves the shorthand fields into From, To and When.
func (r AccessRule) fold() AccessRule {
	if len(r.SourceNamespaces)+len(r.SourceServices)+len(r.Principals) > 0 {
		r.From = append([]AccessSource{{Namespaces: r.SourceNamespaces, Services: r.SourceServices, Principals: r.Principals}}, r.From...)
	}
	if len(r.Methods)+len(r.Paths) > 0 {
		r.To = append([]AccessOperation{{Methods: r.Methods, Paths: r.Paths}}, r.To...)
	}
	for _, h := range r.Headers {
		c := AccessCondition{Key: "request.headers[" + strings.TrimSpace(h.Name) + "]"}
		if h.Not {
			c.NotValues = h.Values
		} else {
			c.Values = h.Values
		}
		r.When = append(r.When, c)
	}
	r.SourceNamespaces, r.SourceServices, r.Principals, r.Methods, r.Paths, r.Headers = nil, nil, nil, nil, nil, nil
	return r
}

func putList(m map[string]any, key string, v []string) {
	if v = cleanList(v); len(v) > 0 {
		m[key] = v
	}
}

func generateAccessRule(ws *store.Workspace, req Request, svc *store.Object) (map[string]any, []string, error) {
	a := req.Access
	if a == nil {
		return nil, nil, fmt.Errorf("add at least one rule")
	}
	action := defaultString(a.Action, "DENY")
	if !contains([]string{"ALLOW", "DENY", "AUDIT", "CUSTOM"}, action) {
		return nil, nil, fmt.Errorf("action must be ALLOW, DENY, AUDIT or CUSTOM")
	}
	spec := map[string]any{"targetRefs": []any{map[string]any{"group": "", "kind": "Service", "name": req.Service}}, "action": action}
	if action == "CUSTOM" {
		if strings.TrimSpace(a.Provider) == "" {
			return nil, nil, fmt.Errorf("CUSTOM needs the name of an extension provider from the mesh config")
		}
		spec["provider"] = map[string]any{"name": strings.TrimSpace(a.Provider)}
	}
	notes := []string{}
	if strings.TrimSpace(a.RulesYAML) != "" {
		var rules []any
		if err := yaml.Unmarshal([]byte(a.RulesYAML), &rules); err != nil {
			return nil, nil, fmt.Errorf("rules YAML is not a list of rules: %v", err)
		}
		if len(rules) == 0 {
			return nil, nil, fmt.Errorf("rules YAML is empty; an empty %s policy matches every request", action)
		}
		for i, r := range rules {
			if M(r) == nil {
				return nil, nil, fmt.Errorf("rule %d in the YAML is not a mapping", i+1)
			}
		}
		spec["rules"] = rules
		notes = append(notes, "Rules were written as YAML. Flightbay checked only that they parse; the server dry-run validates every field.")
		return spec, append(notes, accessNotes(action)...), nil
	}
	if len(a.Rules) == 0 {
		return nil, nil, fmt.Errorf("add at least one rule")
	}
	if len(a.Rules) > 20 {
		return nil, nil, fmt.Errorf("use at most twenty rules in one policy")
	}
	td := clusterTrustDomain(ws, req.Cluster)
	rules := []any{}
	for i, raw := range a.Rules {
		n := i + 1
		r := raw.fold()
		rule := map[string]any{}
		from := []any{}
		for _, s := range r.From {
			src := map[string]any{}
			for _, ns := range append(cleanList(s.Namespaces), cleanList(s.NotNamespaces)...) {
				if !strings.ContainsAny(ns, "*") && ws.Get(req.Cluster, "", "namespaces", "", ns) == nil && ws.CollectionState(req.Cluster, "", "namespaces", "Namespace", false).State == store.StateComplete {
					return nil, nil, fmt.Errorf("rule %d: namespace %q is not in this capture", n, ns)
				}
			}
			principals := []string{}
			for _, ref := range cleanList(s.Services) {
				ps, err := servicePrincipals(ws, req.Cluster, ref, td)
				if err != nil {
					return nil, nil, fmt.Errorf("rule %d: %v", n, err)
				}
				principals = append(principals, ps...)
			}
			for _, p := range cleanList(s.Principals) {
				principals = append(principals, strings.TrimPrefix(p, "spiffe://"))
			}
			sort.Strings(principals)
			putList(src, "principals", dedupe(principals))
			putList(src, "notPrincipals", s.NotPrincipals)
			putList(src, "requestPrincipals", s.RequestPrincipals)
			putList(src, "notRequestPrincipals", s.NotRequestPrincipals)
			putList(src, "namespaces", s.Namespaces)
			putList(src, "notNamespaces", s.NotNamespaces)
			putList(src, "serviceAccounts", s.ServiceAccounts)
			putList(src, "notServiceAccounts", s.NotServiceAccounts)
			putList(src, "ipBlocks", s.IPBlocks)
			putList(src, "notIpBlocks", s.NotIPBlocks)
			putList(src, "remoteIpBlocks", s.RemoteIPBlocks)
			putList(src, "notRemoteIpBlocks", s.NotRemoteIPBlocks)
			if len(src) > 0 {
				from = append(from, map[string]any{"source": src})
			}
		}
		if len(from) > 0 {
			rule["from"] = from
		}
		to := []any{}
		for _, o := range r.To {
			for _, m := range append(cleanList(o.Methods), cleanList(o.NotMethods)...) {
				if !contains(httpMethods, m) {
					return nil, nil, fmt.Errorf("rule %d: unsupported HTTP method %q", n, m)
				}
			}
			for _, p := range append(cleanList(o.Paths), cleanList(o.NotPaths)...) {
				if p != "*" && !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "*") {
					return nil, nil, fmt.Errorf("rule %d: paths must start with /, or use * as a prefix or suffix wildcard", n)
				}
			}
			for _, p := range append(cleanList(o.Ports), cleanList(o.NotPorts)...) {
				if strings.Trim(p, "0123456789") != "" {
					return nil, nil, fmt.Errorf("rule %d: port %q must be a number", n, p)
				}
			}
			op := map[string]any{}
			putList(op, "hosts", o.Hosts)
			putList(op, "notHosts", o.NotHosts)
			putList(op, "ports", o.Ports)
			putList(op, "notPorts", o.NotPorts)
			putList(op, "methods", o.Methods)
			putList(op, "notMethods", o.NotMethods)
			putList(op, "paths", o.Paths)
			putList(op, "notPaths", o.NotPaths)
			if len(op) > 0 {
				to = append(to, map[string]any{"operation": op})
			}
		}
		if len(to) > 0 {
			rule["to"] = to
		}
		when := []any{}
		for _, c := range r.When {
			key := strings.TrimSpace(c.Key)
			if strings.HasPrefix(key, "request.headers[") && strings.HasSuffix(key, "]") {
				name := strings.ToLower(key[len("request.headers[") : len(key)-1])
				if !headerName(name) {
					return nil, nil, fmt.Errorf("rule %d: %q is not a valid header name", n, name)
				}
				key = "request.headers[" + name + "]"
			}
			if !conditionKey.MatchString(key) {
				return nil, nil, fmt.Errorf("rule %d: %q is not an AuthorizationPolicy condition key (for example request.headers[name], source.ip, request.auth.claims[iss])", n, key)
			}
			vals, nots := cleanList(c.Values), cleanList(c.NotValues)
			if len(vals)+len(nots) == 0 {
				return nil, nil, fmt.Errorf("rule %d: condition %s needs values or notValues", n, key)
			}
			cond := map[string]any{"key": key}
			putList(cond, "values", vals)
			putList(cond, "notValues", nots)
			when = append(when, cond)
		}
		if len(when) > 0 {
			rule["when"] = when
		}
		if len(rule) == 0 {
			return nil, nil, fmt.Errorf("rule %d is empty; an empty %s rule would match every request", n, action)
		}
		rules = append(rules, rule)
	}
	spec["rules"] = rules
	return spec, append(notes, accessNotes(action)...), nil
}

func accessNotes(action string) []string {
	n := []string{}
	switch action {
	case "DENY":
		n = append(n, "DENY policies are evaluated before ALLOW policies, so a matching request is refused whatever else allows it.")
	case "ALLOW":
		n = append(n, "An ALLOW policy on this Service refuses every request that no ALLOW rule matches. Existing ALLOW policies stay additive.")
	case "AUDIT":
		n = append(n, "AUDIT only marks matching requests for the audit provider; it neither allows nor denies.")
	case "CUSTOM":
		n = append(n, "CUSTOM sends matching requests to the external authorizer named in the mesh config, which decides.")
	}
	return append(n, "This Service-targeted policy is enforced by the waypoint only. Traffic that skips the waypoint (ingress without istio.io/ingress-use-waypoint, Pod IPs, port-forwards) is not checked.", "Header values match exactly and are case-sensitive. Header names are matched lowercase.")
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
