package ambient

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tjorourke/flightbay/internal/store"
)

func configurationFingerprint(o *store.Object) string {
	b, _ := json.Marshal(o.Obj["spec"])
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func editableConfigurations(ws *store.Workspace, svc *store.Object) []Editable {
	out := []Editable{}
	port := 0
	if ps := L(Path(svc.Obj, "spec", "ports")); len(ps) > 0 {
		port = N(M(ps[0])["port"])
	}
	base := func(o *store.Object, rule int) Request {
		return Request{Cluster: svc.Cluster, Namespace: svc.Namespace, Service: svc.Name, Port: port, Edit: &EditTarget{Ref: reference(o), Rule: rule, UID: S(Path(o.Obj, "metadata", "uid")), Fingerprint: configurationFingerprint(o)}}
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
		if cp := M(tp["connectionPool"]); len(cp) > 0 {
			req.Tasks = append(req.Tasks, "circuit-breaker")
			req.CircuitBreaker = &CircuitBreakerRequest{MaxConnections: N(Path(cp, "tcp", "maxConnections")), MaxPendingRequests: N(Path(cp, "http", "http1MaxPendingRequests")), MaxRequests: N(Path(cp, "http", "http2MaxRequests")), MaxRetries: N(Path(cp, "http", "maxRetries")), MaxRequestsPerConnection: N(Path(cp, "http", "maxRequestsPerConnection")), ConnectTimeout: S(Path(cp, "tcp", "connectTimeout"))}
		}
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
			targets := L(Path(o.Obj, "spec", "targetRefs"))
			selector := M(Path(o.Obj, "spec", "selector", "matchLabels"))
			relevant := o.Kind == "AuthorizationPolicy" && len(targets) == 0 && (len(selector) == 0 || matches(selector, stringMap(M(Path(svc.Obj, "spec", "selector")))))
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
				if len(targets) > 0 && len(rules) == 1 && len(L(M(rules[0])["to"])) == 1 {
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
				entry.Reason = "Only Service-targeted policies whose rules use caller namespaces or principals, methods, paths and request headers are edited here; other policy shapes remain evidence-only."
				if !entry.Supported && len(targets) > 0 {
					if access, ok := accessFromPolicy(o); ok {
						req.Tasks = []string{"access-rule"}
						req.Access = access
						entry.Supported = true
					}
				}
				if len(targets) == 0 && defaultString(S(Path(o.Obj, "spec", "action")), "ALLOW") == "ALLOW" {
					if len(rules) == 0 {
						entry.Tasks = []string{"l4-default-deny"}
						entry.Reason = "Default-deny policy is already configured. Inspect its workload scope and additive allow policies; it has no request parameters to edit."
					}
					if len(rules) == 1 && len(L(M(rules[0])["from"])) == 1 {
						from := M(L(M(rules[0])["from"])[0])
						namespaces := L(Path(from, "source", "namespaces"))
						if len(namespaces) == 1 {
							req.Tasks = []string{"l4-allow-namespace"}
							req.SourceNamespace = S(namespaces[0])
							entry.Supported = true
							entry.Reason = "Changes only the source namespace constraint. Selectors, other source constraints, operations and conditions are preserved."
						}
					}
				}
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
	for _, o := range ws.List(svc.Cluster, "security.istio.io", "peerauthentications", svc.Namespace, nil) {
		selector := M(Path(o.Obj, "spec", "selector", "matchLabels"))
		if len(selector) > 0 && !matches(selector, stringMap(M(Path(svc.Obj, "spec", "selector")))) {
			continue
		}
		e := Editable{Ref: reference(o), Tasks: []string{"strict-mtls"}, Reason: "Changes only the existing mTLS mode; workload selector and port-level settings are preserved."}
		req := base(o, 0)
		req.Tasks = []string{"strict-mtls"}
		req.MTLSMode = S(Path(o.Obj, "spec", "mtls", "mode"))
		if req.MTLSMode == "STRICT" || req.MTLSMode == "PERMISSIVE" {
			e.Supported = true
			e.Request = &req
		} else {
			e.Reason = "This policy inherits its mode or uses a mode unsupported by the simple editor. Inspect the original policy and port-level settings."
		}
		out = append(out, e)
	}
	for _, o := range ws.List(svc.Cluster, "networking.istio.io", "envoyfilters", svc.Namespace, nil) {
		if o.Labels()["flightbay.dev/resilience-service"] != svc.Name || !flightbayOwned(o) {
			continue
		}
		var req Request
		if json.Unmarshal([]byte(S(Path(o.Obj, "metadata", "annotations", "flightbay.dev/resilience-request"))), &req) != nil || req.Resilience == nil {
			continue
		}
		req.Cluster, req.Namespace, req.Service = svc.Cluster, svc.Namespace, svc.Name
		req.Edit = &EditTarget{Ref: reference(o), Fingerprint: configurationFingerprint(o)}
		out = append(out, Editable{Ref: reference(o), Supported: true, Request: &req, Reason: "Generated route-scoped resilience. Updates this owned configuration and retains independent API policies."})
	}
	for i := range out {
		e := &out[i]
		o := ws.Get(e.Ref.Cluster, e.Ref.Group, e.Ref.Resource, e.Ref.Namespace, e.Ref.Name)
		if o == nil {
			continue
		}
		e.Fingerprint = configurationFingerprint(o)
		e.Status = "Captured"
		if e.Request != nil {
			e.Tasks = append([]string{}, e.Request.Tasks...)
		}
		e.Scope = "Preserves existing attachment scope"
		if o.Kind == "HTTPRoute" {
			rules := L(Path(o.Obj, "spec", "rules"))
			if e.Rule < len(rules) {
				e.Matches = M(rules[e.Rule])["matches"]
			}
			e.Scope = "Related route; inspect its parent references"
			for _, v := range L(Path(o.Obj, "spec", "parentRefs")) {
				p := M(v)
				g, k, ns, n := parent(p, o.Namespace)
				if g != "" || k != "Service" || ns != svc.Namespace || n != svc.Name {
					continue
				}
				e.Scope = "Service " + ns + "/" + n
				if N(p["port"]) > 0 {
					e.Scope += fmt.Sprintf(" · port %d", N(p["port"]))
				}
				for _, s := range L(Path(o.Obj, "status", "parents")) {
					status := M(s)
					sg, sk, sns, sn := parent(M(status["parentRef"]), o.Namespace)
					if sg != g || sk != k || sns != ns || sn != n || N(M(status["parentRef"])["port"]) != N(p["port"]) {
						continue
					}
					accepted, resolved := false, false
					for _, c := range L(status["conditions"]) {
						cond := M(c)
						if N(cond["observedGeneration"]) != N(Path(o.Obj, "metadata", "generation")) {
							continue
						}
						if S(cond["type"]) == "Accepted" {
							accepted = S(cond["status"]) == "True"
						}
						if S(cond["type"]) == "ResolvedRefs" {
							resolved = S(cond["status"]) == "True"
						}
					}
					if accepted && resolved {
						e.Status = "Accepted"
					}
				}
			}
		} else if o.Kind == "PeerAuthentication" || o.Kind == "AuthorizationPolicy" {
			targets := L(Path(o.Obj, "spec", "targetRefs"))
			if len(targets) > 0 {
				names := []string{}
				for _, t := range targets {
					ref := M(t)
					names = append(names, S(ref["kind"])+" "+S(ref["name"]))
				}
				e.Scope = strings.Join(names, ", ")
			} else if len(M(Path(o.Obj, "spec", "selector", "matchLabels"))) > 0 {
				e.Scope = "Workload selector · can affect other Services using these Pods"
			} else {
				e.Scope = "Namespace-wide · affects other workloads in " + svc.Namespace
			}
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
	if edit.UID != "" && edit.UID != S(Path(o.Obj, "metadata", "uid")) {
		return fmt.Errorf("the configuration was replaced after loading; reopen its current settings")
	}
	if edit.Fingerprint != "" && edit.Fingerprint != configurationFingerprint(o) {
		return fmt.Errorf("the configuration changed after loading; reopen its current settings before reviewing an update")
	}
	allowed := false
	svc := Target(ws, req.Cluster, "Service", req.Namespace, req.Service)
	for _, e := range editableConfigurations(ws, svc) {
		if e.Ref == edit.Ref && e.Rule == edit.Rule && e.Supported {
			allowed = true
			tasks := e.Request.Tasks
			if o.Kind == "HTTPRoute" {
				tasks = []string{"weighted-routing", "timeouts"}
			}
			if o.Kind == "DestinationRule" {
				tasks = []string{"load-balancing", "endpoint-failover", "circuit-breaker"}
			}
			for _, task := range req.Tasks {
				if !contains(tasks, task) {
					return fmt.Errorf("task %s does not edit this existing configuration shape", task)
				}
			}
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
		if contains(req.Tasks, "circuit-breaker") {
			cp := M(tp["connectionPool"])
			if cp == nil {
				cp = map[string]any{}
			}
			for protocol, settings := range M(fresh["connectionPool"]) {
				merged := M(cp[protocol])
				if merged == nil {
					merged = map[string]any{}
				}
				for key, value := range M(settings) {
					merged[key] = value
				}
				cp[protocol] = merged
			}
			tp["connectionPool"] = cp
		}
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
		if contains(req.Tasks, "access-rule") {
			change(p, o, "Replace this policy's action and rules with the reviewed ones; targetRefs and metadata are preserved", map[string]any{"/spec/rules": Copy(Path(generated, "spec", "rules")), "/spec/action": S(Path(generated, "spec", "action"))})
			break
		}
		rules := L(Copy(Path(o.Obj, "spec", "rules")))
		if contains(req.Tasks, "l4-allow-namespace") {
			from := M(L(M(rules[0])["from"])[0])
			M(from["source"])["namespaces"] = []string{req.SourceNamespace}
			change(p, o, "Edit the allowed source namespace while preserving all other policy constraints", map[string]any{"/spec/rules": rules})
			break
		}
		to := L(M(rules[0])["to"])
		op := M(M(to[0])["operation"])
		if op == nil {
			op = map[string]any{}
			M(to[0])["operation"] = op
		}
		op["methods"] = req.Methods
		change(p, o, "Edit method constraints while preserving source, path and other rules", map[string]any{"/spec/rules": rules, "/spec/action": defaultString(req.MethodAction, "ALLOW")})
	case "PeerAuthentication":
		change(p, o, "Edit the existing mTLS mode while preserving selector and port-level overrides", map[string]any{"/spec/mtls/mode": req.MTLSMode})
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
