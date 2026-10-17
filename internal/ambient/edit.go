package ambient

import (
	"fmt"

	"github.com/tjorourke/flightbay/internal/store"
)

func editableConfigurations(ws *store.Workspace, svc *store.Object) []Editable {
	out := []Editable{}
	port := 0
	if ps := L(Path(svc.Obj, "spec", "ports")); len(ps) > 0 {
		port = N(M(ps[0])["port"])
	}
	base := func(o *store.Object, rule int) Request {
		return Request{Cluster: svc.Cluster, Namespace: svc.Namespace, Service: svc.Name, Port: port, Edit: &EditTarget{Ref: reference(o), Rule: rule}}
	}
	wn, wp, _ := Waypoint(ws, svc)
	for _, o := range ws.List(svc.Cluster, GatewayGroup, "httproutes", svc.Namespace, nil) {
		if !related(o, svc, wn, wp) {
			continue
		}
		for i, v := range L(Path(o.Obj, "spec", "rules")) {
			entry := Editable{Ref: reference(o), Rule: i, Reason: "This editor preserves other rules, matches, filters and parent references."}
			req := base(o, i)
			rule := M(v)
			parentsMatch := false
			for _, v := range L(Path(o.Obj, "spec", "parentRefs")) {
				p := M(v)
				g, k, ns, n := parent(p, o.Namespace)
				if g == "" && k == "Service" && ns == svc.Namespace && n == svc.Name {
					parentsMatch = true
					if N(p["port"]) > 0 {
						req.Port = N(p["port"])
					}
				}
			}
			if !parentsMatch {
				entry.Reason = "Choose the frontend Service named by this route's Service parent; Gateway-parent editing is not inferred from backend names."
				out = append(out, entry)
				continue
			}
			valid := true
			total := 0
			explicit := false
			for _, v := range L(rule["backendRefs"]) {
				b := M(v)
				if S(b["group"]) != "" || defaultString(S(b["kind"]), "Service") != "Service" || defaultString(S(b["namespace"]), o.Namespace) != o.Namespace {
					valid = false
					break
				}
				weight := 1
				if w, ok := b["weight"]; ok {
					weight = N(w)
					explicit = true
				}
				total += weight
				req.Backends = append(req.Backends, Backend{S(b["name"]), N(b["port"]), weight})
			}
			if !valid || len(req.Backends) < 1 {
				entry.Reason = "Only same-namespace Service backend rules can be edited here."
				out = append(out, entry)
				continue
			}
			if len(req.Backends) > 1 {
				req.Tasks = append(req.Tasks, "weighted-routing")
				req.TrafficMode = "equal"
				if explicit {
					req.TrafficMode = "weighted"
					req.RelativeWeights = total != 100
				}
			}
			if ts := M(rule["timeouts"]); len(ts) > 0 {
				req.Tasks = append(req.Tasks, "timeouts")
				req.Timeout = defaultString(S(ts["request"]), "0s")
				req.BackendTimeout = S(ts["backendRequest"])
			}
			if len(req.Tasks) == 0 {
				entry.Reason = "This is a single-backend rule without a timeout; use a new configuration plan or add a supported task after loading."
				req.Tasks = []string{"timeouts"}
				req.Timeout = "0s"
			}
			entry.Supported = true
			entry.Request = &req
			out = append(out, entry)
		}
	}
	for _, o := range ws.List(svc.Cluster, "networking.istio.io", "destinationrules", svc.Namespace, nil) {
		host := S(Path(o.Obj, "spec", "host"))
		if !serviceHost(host, svc) {
			continue
		}
		entry := Editable{Ref: reference(o), Reason: "TLS, connection pool and other unedited fields are preserved."}
		req := base(o, 0)
		tp := M(Path(o.Obj, "spec", "trafficPolicy"))
		if len(L(tp["portLevelSettings"])) > 0 {
			entry.Reason = "Port-level DestinationRules need a dedicated rule editor; this wizard will not flatten them."
			out = append(out, entry)
			continue
		}
		req.Algorithm = defaultString(S(Path(tp, "loadBalancer", "simple")), "DEFAULT")
		req.Tasks = []string{"load-balancing"}
		if od := M(tp["outlierDetection"]); len(od) > 0 {
			req.Tasks = append(req.Tasks, "endpoint-failover")
			req.FailureThreshold = N(od["consecutive5xxErrors"])
			req.EjectionInterval = S(od["interval"])
			req.EjectionDuration = S(od["baseEjectionTime"])
			req.MaxEjectionPercent = N(od["maxEjectionPercent"])
		}
		entry.Supported = true
		entry.Request = &req
		out = append(out, entry)
	}
	for _, kind := range []struct{ g, res string }{{"security.istio.io", "authorizationpolicies"}, {"agentgateway.dev", "agentgatewaypolicies"}, {"enterpriseagentgateway.solo.io", "enterpriseagentgatewaypolicies"}} {
		for _, o := range ws.List(svc.Cluster, kind.g, kind.res, svc.Namespace, nil) {
			relevant := false
			for _, v := range L(Path(o.Obj, "spec", "targetRefs")) {
				t := M(v)
				if (S(t["kind"]) == "Service" && S(t["name"]) == svc.Name) || (S(t["kind"]) == "Gateway" && S(t["name"]) == wp && wn == svc.Namespace) {
					relevant = true
				}
			}
			if !relevant {
				continue
			}
			entry := Editable{Ref: reference(o)}
			req := base(o, 0)
			if o.Kind == "AuthorizationPolicy" {
				rules := L(Path(o.Obj, "spec", "rules"))
				if len(rules) == 1 && len(L(M(rules[0])["to"])) == 1 {
					methods := L(Path(M(L(M(rules[0])["to"])[0]), "operation", "methods"))
					for _, v := range methods {
						req.Methods = append(req.Methods, S(v))
					}
					if len(req.Methods) > 0 {
						req.Tasks = []string{"methods"}
						req.MethodAction = defaultString(S(Path(o.Obj, "spec", "action")), "ALLOW")
						entry.Supported = true
					}
				}
				entry.Reason = "Only a single-rule, single-operation HTTP-method policy is edited; other policy shapes remain evidence-only."
			} else {
				local := L(Path(o.Obj, "spec", "traffic", "rateLimit", "local"))
				if len(local) == 1 && N(M(local[0])["requests"]) > 0 {
					req.Tasks = []string{"rate-limit"}
					req.Requests = N(M(local[0])["requests"])
					req.Unit = S(M(local[0])["unit"])
					if b, ok := M(local[0])["burst"]; ok {
						n := N(b)
						req.Burst = &n
					}
					entry.Supported = true
				}
				entry.Reason = "Edits one local request bucket; keys, targetRefs and other policy fields are preserved."
			}
			if entry.Supported {
				entry.Request = &req
			}
			out = append(out, entry)
		}
	}
	return out
}
func editGenerated(ws *store.Workspace, p *Plan) error {
	req := p.Request
	edit := req.Edit
	if edit == nil {
		return nil
	}
	if edit.Ref.Cluster != req.Cluster || edit.Ref.Namespace != req.Namespace {
		return fmt.Errorf("existing edits must stay in the selected cluster and namespace")
	}
	o := Target(ws, req.Cluster, edit.Ref.Kind, edit.Ref.Namespace, edit.Ref.Name)
	if o == nil || o.Group != edit.Ref.Group {
		return fmt.Errorf("selected existing configuration was not captured")
	}
	allowed := false
	svc := Target(ws, req.Cluster, "Service", req.Namespace, req.Service)
	for _, e := range editableConfigurations(ws, svc) {
		if e.Ref == edit.Ref && e.Rule == edit.Rule && e.Supported {
			allowed = true
		}
	}
	if !allowed {
		return fmt.Errorf("this configuration shape is not supported by the scoped editor")
	}
	if len(p.Objects) != 1 {
		return fmt.Errorf("an existing configuration edit must target only one resource; edit other resource kinds separately")
	}
	generated := p.Objects[0]
	if S(generated["kind"]) != o.Kind {
		return fmt.Errorf("selected tasks do not edit this resource kind")
	}
	switch o.Kind {
	case "HTTPRoute":
		rules := L(Copy(Path(o.Obj, "spec", "rules")))
		if edit.Rule < 0 || edit.Rule >= len(rules) {
			return fmt.Errorf("rule index is out of range")
		}
		rule := M(rules[edit.Rule])
		fresh := M(L(Path(generated, "spec", "rules"))[0])
		if contains(req.Tasks, "weighted-routing") {
			backends := L(fresh["backendRefs"])
			for i, v := range backends {
				b := M(v)
				for _, old := range L(rule["backendRefs"]) {
					m := M(old)
					if S(m["name"]) == S(b["name"]) && N(m["port"]) == N(b["port"]) {
						merged := M(Copy(m))
						delete(merged, "weight")
						for k, v := range b {
							merged[k] = v
						}
						backends[i] = merged
						break
					}
				}
			}
			rule["backendRefs"] = backends
		}
		if contains(req.Tasks, "timeouts") {
			ts := M(Copy(rule["timeouts"]))
			if ts == nil {
				ts = map[string]any{}
			}
			for k, v := range M(fresh["timeouts"]) {
				ts[k] = v
			}
			if req.BackendTimeout == "" {
				delete(ts, "backendRequest")
			}
			rule["timeouts"] = ts
		}
		change(p, o, "Edit the selected routing rule; preserve other rules, matches, filters and parents", map[string]any{"/spec/rules": rules})
	case "DestinationRule":
		tp := M(Copy(Path(o.Obj, "spec", "trafficPolicy")))
		if tp == nil {
			tp = map[string]any{}
		}
		fresh := M(Path(generated, "spec", "trafficPolicy"))
		if contains(req.Tasks, "load-balancing") {
			lb := M(tp["loadBalancer"])
			if lb == nil {
				lb = map[string]any{}
			}
			if req.Algorithm == "DEFAULT" {
				delete(lb, "simple")
			} else {
				lb["simple"] = req.Algorithm
			}
			if len(lb) > 0 {
				tp["loadBalancer"] = lb
			} else {
				delete(tp, "loadBalancer")
			}
		}
		if contains(req.Tasks, "endpoint-failover") {
			od := M(tp["outlierDetection"])
			if od == nil {
				od = map[string]any{}
			}
			for k, v := range M(fresh["outlierDetection"]) {
				od[k] = v
			}
			tp["outlierDetection"] = od
		}
		if contains(req.Tasks, "locality-failover") {
			return fmt.Errorf("edit locality settings in a dedicated new plan; existing locality structures are not flattened")
		}
		change(p, o, "Edit selected traffic-policy fields while preserving TLS and other settings", map[string]any{"/spec/trafficPolicy": tp})
	case "AuthorizationPolicy":
		rules := L(Copy(Path(o.Obj, "spec", "rules")))
		to := L(M(rules[0])["to"])
		op := M(M(to[0])["operation"])
		if op == nil {
			op = map[string]any{}
			M(to[0])["operation"] = op
		}
		op["methods"] = req.Methods
		change(p, o, "Edit method constraints while preserving source, path and other rules", map[string]any{"/spec/rules": rules, "/spec/action": defaultString(req.MethodAction, "ALLOW")})
	case "AgentgatewayPolicy", "EnterpriseAgentgatewayPolicy":
		old := L(Copy(Path(o.Obj, "spec", "traffic", "rateLimit", "local")))
		fresh := M(L(Path(generated, "spec", "traffic", "rateLimit", "local"))[0])
		for k, v := range fresh {
			M(old[0])[k] = v
		}
		change(p, o, "Edit the local request bucket while preserving keys and other settings", map[string]any{"/spec/traffic/rateLimit/local": old})
	default:
		return fmt.Errorf("unsupported existing configuration kind")
	}
	p.Objects = []map[string]any{}
	p.Notes = append(p.Notes, "Editing existing "+o.Kind+" "+o.Namespace+"/"+o.Name+". Its existing attachment scope is retained; a Gateway-scoped policy can affect several Services.")
	return nil
}
