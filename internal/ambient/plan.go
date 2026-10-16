package ambient

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/tjorourke/flightbay/internal/store"
	"sigs.k8s.io/yaml"
)

type Task struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Layer     string `json:"layer"`
	Supported bool   `json:"supported"`
	Reason    string `json:"reason"`
}
type Backend struct {
	Name   string `json:"name"`
	Port   int    `json:"port"`
	Weight int    `json:"weight"`
}
type Request struct {
	Cluster         string    `json:"cluster"`
	Namespace       string    `json:"namespace"`
	Service         string    `json:"service"`
	Port            int       `json:"port"`
	Tasks           []string  `json:"tasks"`
	Backends        []Backend `json:"backends,omitempty"`
	SourceNamespace string    `json:"sourceNamespace,omitempty"`
	Methods         []string  `json:"methods,omitempty"`
	Requests        int       `json:"requests,omitempty"`
	Unit            string    `json:"unit,omitempty"`
	Algorithm       string    `json:"algorithm,omitempty"`
	Timeout         string    `json:"timeout,omitempty"`
	FromRegion      string    `json:"fromRegion,omitempty"`
	ToRegion        string    `json:"toRegion,omitempty"`
}
type Plan struct {
	ID         string           `json:"id"`
	Request    Request          `json:"request"`
	ServiceUID string           `json:"serviceUid"`
	Objects    []map[string]any `json:"objects"`
	YAML       string           `json:"yaml"`
	Notes      []string         `json:"notes"`
	Evidence   []Evidence       `json:"evidence"`
}

func hasResource(ws *store.Workspace, c, g, r string) bool {
	for _, v := range ws.Resources(c) {
		if v.Group == g && v.Resource == r {
			return true
		}
	}
	return false
}
func Capabilities(ws *store.Workspace, c, ns, service string) []Task {
	svc := ws.Get(c, "", "services", ns, service)
	if svc == nil {
		return []Task{}
	}
	_, wpName, wp := Waypoint(ws, svc)
	cls := ""
	if wp != nil {
		cls = S(Path(wp.Obj, "spec", "gatewayClassName"))
	}
	istio := cls == "istio-waypoint"
	agent := strings.Contains(cls, "agentgateway") && strings.Contains(cls, "waypoint")
	agentPolicyAvailable := (!strings.HasPrefix(cls, "enterprise-") && hasResource(ws, c, "agentgateway.dev", "agentgatewaypolicies")) || (strings.HasPrefix(cls, "enterprise-") && hasResource(ws, c, "enterpriseagentgateway.solo.io", "enterpriseagentgatewaypolicies"))
	security := hasResource(ws, c, "security.istio.io", "authorizationpolicies")
	dr := hasResource(ws, c, "networking.istio.io", "destinationrules")
	selector := len(M(Path(svc.Obj, "spec", "selector"))) > 0
	l7reason := "Needs a selected waypoint of a supported class and the relevant collected API type. The target API server validates its installed schema before applying."
	out := []Task{
		{"l4-default-deny", "Deny by default for the selected workload", "L4 ztunnel", security && selector, "Creates an empty ALLOW policy on the Service's workload selector. Other ALLOW policies are additive; this does not override them."},
		{"l4-allow-namespace", "Allow calls from a namespace", "L4 ztunnel", security && selector && wpName == "", "For direct ambient traffic without a selected waypoint. Behind a waypoint, ztunnel sees the waypoint identity; use an L7 policy for original callers."},
		{"strict-mtls", "Require mutual TLS", "L4 ztunnel", selector && hasResource(ws, c, "security.istio.io", "peerauthentications"), "STRICT PeerAuthentication for the selected workload. Ambient does not support DISABLE mode."},
		{"methods", "Allow selected HTTP methods", "L7 Istio waypoint", istio && security, "Service-targeted AuthorizationPolicy on an Istio waypoint. Other ALLOW policies remain additive."},
		{"weighted-routing", "Balance traffic between Services", "L7 waypoint", (istio || agent) && hasResource(ws, c, GatewayGroup, "httproutes"), "Service-parent HTTPRoute with weighted backends in the same namespace. Weight zero is not primary/backup failover."},
		{"timeouts", "Set a request timeout", "L7 waypoint", (istio || agent) && hasResource(ws, c, GatewayGroup, "httproutes"), "HTTPRoute request timeout, combined with weighted routing when selected. Existing routes may compete; inspect attachment status."},
		{"load-balancing", "Choose endpoint load balancing", "L7 Istio waypoint", istio && dr, "DestinationRule for this Service's endpoints: ROUND_ROBIN, LEAST_REQUEST or RANDOM. This is not a ztunnel load-balancing setting."},
		{"endpoint-failover", "Eject unhealthy endpoints", "L7 Istio waypoint", istio && dr, "DestinationRule outlier detection. Ejects failing endpoints within a Service; does not fail over between different Service names."},
		{"locality-failover", "Fail over between regions", "L7 Istio waypoint", istio && dr && len(regions(ws, svc)) > 1, "Needs captured endpoint nodes with region labels and endpoints in both regions. Uses locality failover plus outlier detection."},
		{"rate-limit", "Limit request rate locally", "L7 agentgateway waypoint", agent && agentPolicyAvailable, "A Service-targeted AgentgatewayPolicy local limit. The bucket is per proxy, not a shared global quota. Stock Istio waypoint rate limiting has no built-in template here."},
		{"service-failover", "Primary/backup between different Services", "L7 capability-dependent", false, "Gateway API weighted backends do not provide priority failover. No verified portable template is available; Flightbay will not substitute weights for failover."},
		{"global-rate-limit", "Shared/global rate limit", "L7 capability-dependent", false, "Requires a controller-specific rate-limit service, its backend and descriptors. Not generated without that integration's configuration."},
	}
	for i := range out {
		if !out[i].Supported {
			if strings.HasPrefix(out[i].Layer, "L7") {
				out[i].Reason += " " + l7reason
			} else {
				out[i].Reason += " Needs a captured workload selector and the corresponding API type."
			}
		}
	}
	return out
}

func Generate(ws *store.Workspace, req Request) (*Plan, error) {
	svc := ws.Get(req.Cluster, "", "services", req.Namespace, req.Service)
	if svc == nil {
		return nil, fmt.Errorf("select a Service in this workspace")
	}
	if !hasPort(svc, req.Port) {
		return nil, fmt.Errorf("select a Service port, not its container targetPort")
	}
	if len(req.Tasks) == 0 || len(req.Tasks) > 10 {
		return nil, fmt.Errorf("select between one and ten tasks")
	}
	caps := map[string]Task{}
	for _, t := range Capabilities(ws, req.Cluster, req.Namespace, req.Service) {
		caps[t.ID] = t
	}
	selected := map[string]bool{}
	for _, id := range req.Tasks {
		t, ok := caps[id]
		if !ok || !t.Supported {
			return nil, fmt.Errorf("task %q is not supported for this Service: %s", id, t.Reason)
		}
		if selected[id] {
			return nil, fmt.Errorf("task %q was selected twice", id)
		}
		selected[id] = true
	}
	// Stable IDs bind the preview, validated schema and test resources to the same choices.
	inputs := map[string]any{"request": req, "serviceUid": Path(svc.Obj, "metadata", "uid"), "serviceSpec": svc.Obj["spec"], "serviceLabels": svc.Labels()}
	if n := ws.Get(req.Cluster, "", "namespaces", "", req.Namespace); n != nil {
		inputs["namespaceLabels"] = n.Labels()
	}
	if _, _, wp := Waypoint(ws, svc); wp != nil {
		inputs["waypoint"] = map[string]any{"uid": Path(wp.Obj, "metadata", "uid"), "spec": wp.Obj["spec"], "labels": wp.Labels()}
	}
	backends := map[string]any{}
	for _, b := range req.Backends {
		if o := ws.Get(req.Cluster, "", "services", req.Namespace, b.Name); o != nil {
			backends[b.Name] = map[string]any{"uid": Path(o.Obj, "metadata", "uid"), "spec": o.Obj["spec"]}
		}
	}
	inputs["backends"] = backends
	encoded, _ := json.Marshal(inputs)
	sum := sha256.Sum256(encoded)
	id := hex.EncodeToString(sum[:])[:16]
	name := "flightbay-test-" + id
	p := &Plan{ID: id, Request: req, ServiceUID: S(Path(svc.Obj, "metadata", "uid")), Objects: []map[string]any{}, Notes: []string{"Generated from this workspace. Validate against the explicitly selected real kubeconfig context before applying.", "Only Flightbay-named test resources are created. Existing resources are never overwritten. Cleanup deletes only the recorded UIDs.", "Policies and routes can change real traffic. Existing ALLOW policies are additive and existing routes may take precedence."}, Evidence: []Evidence{evidence(svc, "spec", svc.Obj["spec"])}}
	if selected["l4-default-deny"] || selected["l4-allow-namespace"] || selected["strict-mtls"] {
		p.Notes = append(p.Notes, "L4 workload-selector policies affect every Service targeting those Pods, not only the selected Service VIP.")
	}
	if selected["methods"] || selected["rate-limit"] {
		p.Notes = append(p.Notes, "Waypoint L7 policies only apply to traffic that traverses that waypoint. Direct Pod-IP traffic or other bypass paths need separate L4 controls.")
	}
	obj := func(api, kind, suffix string, spec map[string]any) {
		p.Objects = append(p.Objects, map[string]any{"apiVersion": api, "kind": kind, "metadata": map[string]any{"name": name + suffix, "namespace": req.Namespace, "labels": map[string]any{"app.kubernetes.io/managed-by": "flightbay", "flightbay.dev/test-id": id}}, "spec": spec})
	}
	sel := map[string]any{"matchLabels": Path(svc.Obj, "spec", "selector")}
	if selected["l4-default-deny"] {
		obj("security.istio.io/v1", "AuthorizationPolicy", "-deny", map[string]any{"selector": sel, "action": "ALLOW"})
	}
	if selected["l4-allow-namespace"] {
		if req.SourceNamespace == "" || ws.Get(req.Cluster, "", "namespaces", "", req.SourceNamespace) == nil {
			return nil, fmt.Errorf("choose a captured source namespace")
		}
		obj("security.istio.io/v1", "AuthorizationPolicy", "-allow", map[string]any{"selector": sel, "action": "ALLOW", "rules": []any{map[string]any{"from": []any{map[string]any{"source": map[string]any{"namespaces": []string{req.SourceNamespace}}}}}}})
	}
	if selected["strict-mtls"] {
		obj("security.istio.io/v1", "PeerAuthentication", "-mtls", map[string]any{"selector": sel, "mtls": map[string]any{"mode": "STRICT"}})
	}
	target := []any{map[string]any{"group": "", "kind": "Service", "name": req.Service}}
	if selected["methods"] {
		valid := map[string]bool{}
		for _, m := range []string{"GET", "HEAD", "POST", "PUT", "DELETE", "PATCH", "OPTIONS", "CONNECT", "TRACE"} {
			valid[m] = true
		}
		if len(req.Methods) == 0 {
			return nil, fmt.Errorf("select at least one HTTP method")
		}
		for _, m := range req.Methods {
			if !valid[m] {
				return nil, fmt.Errorf("unsupported HTTP method %q", m)
			}
		}
		obj("security.istio.io/v1", "AuthorizationPolicy", "-methods", map[string]any{"targetRefs": target, "action": "ALLOW", "rules": []any{map[string]any{"to": []any{map[string]any{"operation": map[string]any{"methods": req.Methods}}}}}})
	}
	if selected["weighted-routing"] || selected["timeouts"] {
		backends := []any{map[string]any{"name": req.Service, "port": req.Port, "weight": 100}}
		if selected["weighted-routing"] {
			if len(req.Backends) < 2 || len(req.Backends) > 8 {
				return nil, fmt.Errorf("choose two to eight backend Services")
			}
			total := 0
			seen := map[string]bool{}
			backends = nil
			for _, b := range req.Backends {
				o := ws.Get(req.Cluster, "", "services", req.Namespace, b.Name)
				if o == nil || !hasPort(o, b.Port) || b.Weight < 0 || b.Weight > 100 || seen[b.Name] {
					return nil, fmt.Errorf("backend %q must be a distinct Service in the same namespace, with a valid Service port and weight 0–100", b.Name)
				}
				seen[b.Name] = true
				total += b.Weight
				backends = append(backends, map[string]any{"name": b.Name, "port": b.Port, "weight": b.Weight})
			}
			if total != 100 {
				return nil, fmt.Errorf("backend weights must add up to 100")
			}
		}
		rule := map[string]any{"backendRefs": backends}
		if selected["timeouts"] {
			d, e := time.ParseDuration(req.Timeout)
			if e != nil || d <= 0 || d > 5*time.Minute {
				return nil, fmt.Errorf("request timeout must be a positive duration up to 5m")
			}
			rule["timeouts"] = map[string]any{"request": req.Timeout}
		}
		obj("gateway.networking.k8s.io/v1", "HTTPRoute", "-route", map[string]any{"parentRefs": []any{map[string]any{"group": "", "kind": "Service", "name": req.Service, "port": req.Port}}, "rules": []any{rule}})
		for _, r := range ws.List(req.Cluster, GatewayGroup, "httproutes", req.Namespace, nil) {
			if related(r, svc, "", "") {
				p.Notes = append(p.Notes, "Existing route "+r.Namespace+"/"+r.Name+" may overlap this test route. Acceptance does not prove this route wins route precedence.")
			}
		}
	}
	if selected["load-balancing"] || selected["endpoint-failover"] || selected["locality-failover"] {
		policy := map[string]any{}
		if selected["load-balancing"] {
			if req.Algorithm != "ROUND_ROBIN" && req.Algorithm != "LEAST_REQUEST" && req.Algorithm != "RANDOM" {
				return nil, fmt.Errorf("choose ROUND_ROBIN, LEAST_REQUEST or RANDOM")
			}
			policy["loadBalancer"] = map[string]any{"simple": req.Algorithm}
		}
		if selected["endpoint-failover"] || selected["locality-failover"] {
			policy["outlierDetection"] = map[string]any{"consecutive5xxErrors": 3, "interval": "5s", "baseEjectionTime": "30s", "maxEjectionPercent": 100}
			p.Notes = append(p.Notes, "Outlier ejection is based on observed failures and is per proxy. This configuration does not generate load or prove failover has occurred.")
		}
		if selected["locality-failover"] {
			rs := regions(ws, svc)
			if req.FromRegion == req.ToRegion || !contains(rs, req.FromRegion) || !contains(rs, req.ToRegion) {
				return nil, fmt.Errorf("choose two distinct captured endpoint regions")
			}
			lb := M(policy["loadBalancer"])
			if lb == nil {
				lb = map[string]any{}
			}
			lb["localityLbSetting"] = map[string]any{"enabled": true, "failover": []any{map[string]any{"from": req.FromRegion, "to": req.ToRegion}}}
			policy["loadBalancer"] = lb
		}
		obj("networking.istio.io/v1", "DestinationRule", "-destination", map[string]any{"host": req.Service, "trafficPolicy": policy})
		for _, r := range ws.List(req.Cluster, "networking.istio.io", "destinationrules", req.Namespace, nil) {
			host := S(Path(r.Obj, "spec", "host"))
			if host == req.Service || strings.HasPrefix(host, req.Service+".") {
				p.Notes = append(p.Notes, "Existing DestinationRule "+r.Name+" may conflict. Inspect the effective rule before testing.")
			}
		}
	}
	if selected["rate-limit"] {
		if req.Requests < 1 || req.Requests > 1000000 {
			return nil, fmt.Errorf("requests must be between 1 and 1000000")
		}
		if req.Unit != "Seconds" && req.Unit != "Minutes" && req.Unit != "Hours" {
			return nil, fmt.Errorf("choose Seconds, Minutes or Hours")
		}
		_, _, wp := Waypoint(ws, svc)
		enterprise := strings.HasPrefix(S(Path(wp.Obj, "spec", "gatewayClassName")), "enterprise-")
		api, kind := "agentgateway.dev/v1alpha1", "AgentgatewayPolicy"
		if enterprise {
			api, kind = "enterpriseagentgateway.solo.io/v1alpha1", "EnterpriseAgentgatewayPolicy"
		}
		obj(api, kind, "-rate", map[string]any{"targetRefs": target, "traffic": map[string]any{"rateLimit": map[string]any{"local": []any{map[string]any{"requests": req.Requests, "unit": req.Unit, "burst": req.Requests}}}}})
		p.Notes = append(p.Notes, "The rate limit is local to each agentgateway proxy. Multiple replicas do not share a global counter.")
	}
	// Keep objects in Kubernetes' JSON-compatible unstructured representation.
	data, _ := json.Marshal(p.Objects)
	if err := json.Unmarshal(data, &p.Objects); err != nil {
		return nil, err
	}
	for i, o := range p.Objects {
		b, err := yaml.Marshal(o)
		if err != nil {
			return nil, err
		}
		if i > 0 {
			p.YAML += "---\n"
		}
		p.YAML += string(b)
	}
	return p, nil
}
func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
func regions(ws *store.Workspace, svc *store.Object) []string {
	seen := map[string]bool{}
	for _, ep := range ws.List(svc.Cluster, "discovery.k8s.io", "endpointslices", svc.Namespace, nil) {
		if ep.Labels()["kubernetes.io/service-name"] != svc.Name {
			continue
		}
		for _, v := range L(ep.Obj["endpoints"]) {
			e := M(v)
			region := S(M(e["deprecatedTopology"])["topology.kubernetes.io/region"])
			if n := ws.Get(svc.Cluster, "", "nodes", "", S(e["nodeName"])); n != nil {
				region = defaultString(n.Labels()["topology.kubernetes.io/region"], region)
			}
			if region != "" {
				seen[region] = true
			}
		}
	}
	out := []string{}
	for r := range seen {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}
