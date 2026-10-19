package ambient

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
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
	Demo               *BookinfoRequest  `json:"demo,omitempty"`
	Explain            *ExplainRequest   `json:"explain,omitempty"`
	Paths              []string          `json:"paths,omitempty"`
	Headers            map[string]string `json:"headers,omitempty"`
	Issuer             string            `json:"issuer,omitempty"`
	JWKSURI            string            `json:"jwksUri,omitempty"`
	JWKS               string            `json:"jwks,omitempty"`
	RequestPrincipals  []string          `json:"requestPrincipals,omitempty"`
	Mirror             *Backend          `json:"mirror,omitempty"`
	RetryAttempts      int               `json:"retryAttempts,omitempty"`
	Scorecard          *ScorecardRequest `json:"scorecard,omitempty"`
	Parent             *Ref              `json:"parent,omitempty"`
	ParentSection      string            `json:"parentSection,omitempty"`
	TargetKind         string            `json:"targetKind,omitempty"`
	TargetName         string            `json:"targetName,omitempty"`
	TrafficMode        string            `json:"trafficMode,omitempty"`
	RelativeWeights    bool              `json:"relativeWeights,omitempty"`
	MTLSMode           string            `json:"mtlsMode,omitempty"`
	MethodAction       string            `json:"methodAction,omitempty"`
	Burst              *int              `json:"burst,omitempty"`
	BackendTimeout     string            `json:"backendTimeout,omitempty"`
	FailureThreshold   int               `json:"failureThreshold,omitempty"`
	EjectionInterval   string            `json:"ejectionInterval,omitempty"`
	EjectionDuration   string            `json:"ejectionDuration,omitempty"`
	MaxEjectionPercent int               `json:"maxEjectionPercent,omitempty"`
	Waypoint           *WaypointRequest  `json:"waypoint,omitempty"`
	Selector           map[string]string `json:"selector,omitempty"`
	SelectedPod        string            `json:"selectedPod,omitempty"`
	Workload           string            `json:"workload,omitempty"`
	Replicas           int               `json:"replicas,omitempty"`
	// Unpublish reverses global-service publication for this one Service by
	// removing its solo.io/service-scope label.
	Unpublish bool `json:"unpublish,omitempty"`
	// Access builds a Service-targeted AuthorizationPolicy from rules.
	Access          *AccessRequest `json:"access,omitempty"`
	TargetPort      int            `json:"targetPort,omitempty"`
	Route           string         `json:"route,omitempty"`
	Edit            *EditTarget    `json:"edit,omitempty"`
	Cluster         string         `json:"cluster"`
	Namespace       string         `json:"namespace"`
	Service         string         `json:"service"`
	Port            int            `json:"port"`
	Tasks           []string       `json:"tasks"`
	Backends        []Backend      `json:"backends,omitempty"`
	SourceNamespace string         `json:"sourceNamespace,omitempty"`
	Methods         []string       `json:"methods,omitempty"`
	Requests        int            `json:"requests,omitempty"`
	Unit            string         `json:"unit,omitempty"`
	Algorithm       string         `json:"algorithm,omitempty"`
	Timeout         string         `json:"timeout,omitempty"`
	FromRegion      string         `json:"fromRegion,omitempty"`
	ToRegion        string         `json:"toRegion,omitempty"`
}

type ExplainRequest struct {
	From  string   `json:"from"`
	To    string   `json:"to"`
	Ports []string `json:"ports,omitempty"`
	Paths []string `json:"paths,omitempty"`
}

type ScorecardRequest struct {
	Check      string   `json:"check"`
	Target     Ref      `json:"target"`
	Principals []string `json:"principals,omitempty"`
}

// ReviewedPolicyPlan uses the same hashed preview and validation/apply records
// as ambient plans. The server supplies the policy from a verified check recipe.
func ReviewedPolicyPlan(req Request, target *store.Object, policy map[string]any, notes ...string) (*Plan, error) {
	p := basePlan(req, target)
	if target.Kind == "Service" {
		p.ServiceUID = S(Path(target.Obj, "metadata", "uid"))
	}
	p.Objects = append(p.Objects, policy)
	p.Notes = append(p.Notes, notes...)
	return finishPlan(p)
}

type Plan struct {
	Changes    []Change         `json:"changes,omitempty"`
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

// HasResource is the exported wrapper for use by resmap and others. Do not change DetectEnvironment.
func HasResource(ws *store.Workspace, c, g, r string) bool { return hasResource(ws, c, g, r) }

func Capabilities(ws *store.Workspace, c, ns, service string) []Task {
	svc := ws.Get(c, "", "services", ns, service)
	if svc == nil {
		return []Task{}
	}
	_, wpName, _ := Waypoint(ws, svc)
	env := DetectEnvironment(ws, c, svc)
	istio := env.Selected != nil && env.Selected.Supported && env.Selected.Product == "istio"
	agent := env.Selected != nil && env.Selected.Supported && env.Selected.Product == "agentgateway"
	agentPolicyAvailable := env.PolicyAPI != ""
	security := hasResource(ws, c, "security.istio.io", "authorizationpolicies")
	dr := hasResource(ws, c, "networking.istio.io", "destinationrules")
	selector := len(M(Path(svc.Obj, "spec", "selector"))) > 0
	globalSupported, globalReason := globalServiceSupport(ws, svc, env)
	out := []Task{
		{"require-waypoint", "Require the selected waypoint", "L4 ztunnel", security && selector && waypointPrincipal(ws, svc) != "", "Allows only the captured waypoint service account. Requires an unambiguous captured identity and trust domain. Existing ALLOW policies remain additive."},
		{"jwt", "Require a JWT", "L7 Istio waypoint", istio && security && hasResource(ws, c, "security.istio.io", "requestauthentications"), "RequestAuthentication and requestPrincipals authorization on the Service. Agentgateway JWT generation is UNVERIFIED and unavailable: no tested installed-schema policy mapping is established."},
		{"retries", "Retry failed requests", "L7 Istio waypoint", istio && routeField(ws, c, "retry", "attempts"), "Requires the experimental HTTPRoute retry.attempts field in the captured installed CRD schema and an Istio waypoint. Otherwise unavailable; agentgateway support is UNVERIFIED."},
		{"mirroring", "Mirror requests", "L7 Istio waypoint", istio && routeField(ws, c, "filters", "requestMirror", "backendRef", "name"), "HTTPRoute RequestMirror to a captured Service. Agentgateway waypoint support is UNVERIFIED and unavailable."},
		{"header-routing", "Route by HTTP header", "L7 waypoint", (istio || agent) && routeField(ws, c, "matches", "headers", "value"), "Exact HTTPRoute header matches. Requires the captured installed HTTPRoute schema."},
		{"global-service", "Publish as a global Service", "Multicluster", globalSupported, globalReason},
		{"l4-default-deny", "Deny by default for the selected workload", "L4 ztunnel", security && selector, "Creates an empty ALLOW policy on the Service's workload selector. Other ALLOW policies are additive; this does not override them."},
		{"l4-allow-namespace", "Allow calls from a namespace", "L4 ztunnel", security && selector && wpName == "", "For direct ambient traffic without a selected waypoint. Behind a waypoint, ztunnel sees the waypoint identity; use an L7 policy for original callers."},
		{"strict-mtls", "Require mutual TLS", "L4 ztunnel", selector && hasResource(ws, c, "security.istio.io", "peerauthentications"), "STRICT PeerAuthentication for the selected workload. Ambient does not support DISABLE mode."},
		{"access-rule", "Allow or deny requests by caller, header, path or method", "L7 Istio waypoint", istio && security, "Service-targeted AuthorizationPolicy with your own rules: caller namespaces, caller Services or principals, methods, paths and request headers. ALLOW or DENY."},
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
		if out[i].Supported {
			continue
		}
		why := ""
		switch out[i].ID {
		case "require-waypoint", "jwt", "retries", "mirroring", "header-routing":
			continue // these tasks describe their evidence and implementation prerequisites
		case "global-service":
			continue // the support check already supplied the exact prerequisite
		case "service-failover":
			why = "Not implemented in Flightbay. Enabling this task requires a tested generator for the selected controller's priority-failover API; installing more CRDs or changing weights will not enable it."
		case "global-rate-limit":
			why = "Not implemented in Flightbay. This needs both a configured external rate-limit service and a tested Flightbay integration for its backend and descriptors. Installing the service alone will not enable this task."
		case "l4-allow-namespace":
			if wpName != "" {
				why = "This direct-path template is disabled because a waypoint is selected. For this topology, use a waypoint policy that checks the original caller; Flightbay does not generate that namespace rule here. Do not remove the waypoint just to enable this card."
			}
		case "methods", "access-rule", "load-balancing", "endpoint-failover", "locality-failover":
			if !istio {
				why = "This Flightbay generator requires an Istio/Envoy waypoint. To use this template, select or deploy a supported Istio waypoint and review the binding change. Keeping agentgateway requires a tested agentgateway-specific generator in Flightbay; installing Istio CRDs alone is not enough."
			} else if out[i].ID == "methods" || out[i].ID == "access-rule" {
				why = "AuthorizationPolicy was not found in captured API discovery. Confirm the supported Istio security API is installed, then refresh inventory or collect that API."
			} else if !dr {
				why = "DestinationRule was not found in captured API discovery. Confirm the supported Istio networking API is installed, then refresh inventory or collect that API."
			} else {
				why = "Fewer than two endpoint regions were established. Provide healthy endpoints in at least two regions, label their Nodes with topology.kubernetes.io/region, and capture Nodes plus EndpointSlices."
			}
		case "weighted-routing", "timeouts":
			if !istio && !agent {
				why = "No supported waypoint controller was established for this Service. Select or deploy a supported waypoint and capture its Gateway and GatewayClass before using this template."
			} else {
				why = "HTTPRoute was not found in captured API discovery. Confirm the controller's supported Gateway API CRDs are installed, then refresh inventory or collect that API."
			}
		case "rate-limit":
			if !agent {
				why = "This Flightbay template requires an agentgateway waypoint. Select or deploy a supported agentgateway waypoint and review the binding change; Flightbay has no equivalent local-rate-limit generator for an Istio/Envoy waypoint."
			} else {
				why = "The policy API matching the selected agentgateway controller was not captured. Confirm that edition's policy CRD is installed and refresh inventory. An OSS policy CRD does not substitute for the Enterprise policy CRD, or vice versa."
			}
		}
		if why == "" {
			if !selector {
				why = "This Service has no captured workload selector. This template needs an explicitly selected workload; inspect the intended Service and collect its full spec before configuring a workload policy."
			} else {
				api := "AuthorizationPolicy"
				if out[i].ID == "strict-mtls" {
					api = "PeerAuthentication"
				}
				why = api + " was not found in captured API discovery. Confirm the supported Istio security API is installed, then refresh inventory or collect that API."
			}
		}
		out[i].Reason += " " + why
	}
	return out
}

func Generate(ws *store.Workspace, req Request) (*Plan, error) {
	if contains(req.Tasks, "global-service") {
		return generateGlobalService(ws, req)
	}
	if contains(req.Tasks, "waypoint") || contains(req.Tasks, "enrol-namespace") {
		return generateWaypoint(ws, req)
	}
	if contains(req.Tasks, "service-selector") || contains(req.Tasks, "scale-workload") || contains(req.Tasks, "service-port") || contains(req.Tasks, "route-parent") {
		return generateRepair(ws, req)
	}
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
	detected := DetectEnvironment(ws, req.Cluster, svc)
	inputs["implementation"] = detected.Selected
	inputs["policyAPI"] = detected.PolicyAPI
	if selected["require-waypoint"] {
		inputs["waypointPrincipal"] = waypointPrincipal(ws, svc)
	}
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
	p := &Plan{ID: id, Request: req, ServiceUID: S(Path(svc.Obj, "metadata", "uid")), Objects: []map[string]any{}, Notes: []string{"Generated from this workspace. Validate against the explicitly selected real kubeconfig context before applying.", "New traffic resources use owned names and cannot overwrite existing objects. Existing edits are shown as scoped field changes with undo.", "Policies and routes can change real traffic. Existing ALLOW policies are additive and existing routes may take precedence."}, Evidence: []Evidence{evidence(svc, "spec", svc.Obj["spec"])}}
	if selected["l4-default-deny"] || selected["l4-allow-namespace"] || selected["strict-mtls"] {
		p.Notes = append(p.Notes, "L4 workload-selector policies affect every Service targeting those Pods, not only the selected Service VIP.")
	}
	if selected["methods"] || selected["rate-limit"] || selected["access-rule"] {
		p.Notes = append(p.Notes, "Waypoint L7 policies only apply to traffic that traverses that waypoint. Direct Pod-IP traffic or other bypass paths need separate L4 controls.")
	}
	obj := func(api, kind, suffix string, spec map[string]any) {
		p.Objects = append(p.Objects, map[string]any{"apiVersion": api, "kind": kind, "metadata": map[string]any{"name": name + suffix, "namespace": req.Namespace, "labels": map[string]any{"app.kubernetes.io/managed-by": "flightbay", "flightbay.dev/test-id": id}}, "spec": spec})
	}
	sel := map[string]any{"matchLabels": Path(svc.Obj, "spec", "selector")}
	if selected["l4-default-deny"] {
		obj("security.istio.io/v1", "AuthorizationPolicy", "-deny", map[string]any{"selector": sel, "action": "ALLOW"})
	}
	if selected["require-waypoint"] {
		p.Evidence = append(p.Evidence, waypointIdentityEvidence(ws, svc)...)
		obj("security.istio.io/v1", "AuthorizationPolicy", "-waypoint", map[string]any{"selector": sel, "action": "ALLOW", "rules": []any{map[string]any{"from": []any{map[string]any{"source": map[string]any{"principals": []string{waypointPrincipal(ws, svc)}}}}}}})
		p.Notes = append(p.Notes, "This workload-wide ALLOW admits only the waypoint identity unless another ALLOW policy admits other callers. Review all additive policies. Direct Pod-IP callers will be refused.")
	}
	if selected["l4-allow-namespace"] {
		if req.SourceNamespace == "" || ws.Get(req.Cluster, "", "namespaces", "", req.SourceNamespace) == nil {
			return nil, fmt.Errorf("choose a captured source namespace")
		}
		obj("security.istio.io/v1", "AuthorizationPolicy", "-allow", map[string]any{"selector": sel, "action": "ALLOW", "rules": []any{map[string]any{"from": []any{map[string]any{"source": map[string]any{"namespaces": []string{req.SourceNamespace}}}}}}})
	}
	if selected["strict-mtls"] {
		mode := defaultString(req.MTLSMode, "STRICT")
		if mode != "STRICT" && mode != "PERMISSIVE" {
			return nil, fmt.Errorf("ambient mTLS mode must be STRICT or PERMISSIVE")
		}
		obj("security.istio.io/v1", "PeerAuthentication", "-mtls", map[string]any{"selector": sel, "mtls": map[string]any{"mode": mode}})
	}
	target := []any{map[string]any{"group": "", "kind": "Service", "name": req.Service}}
	if selected["jwt"] {
		if err := generateJWT(req, target, obj); err != nil {
			return nil, err
		}
	}
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
		action := defaultString(req.MethodAction, "ALLOW")
		if action != "ALLOW" && action != "DENY" {
			return nil, fmt.Errorf("method action must be ALLOW or DENY")
		}
		obj("security.istio.io/v1", "AuthorizationPolicy", "-methods", map[string]any{"targetRefs": target, "action": action, "rules": []any{map[string]any{"to": []any{map[string]any{"operation": map[string]any{"methods": req.Methods}}}}}})
	}
	if selected["access-rule"] {
		spec, notes, err := generateAccessRule(ws, req, svc)
		if err != nil {
			return nil, err
		}
		suffix := "-allow-rule"
		if S(spec["action"]) == "DENY" {
			suffix = "-deny-rule"
		}
		obj("security.istio.io/v1", "AuthorizationPolicy", suffix, spec)
		p.Notes = append(p.Notes, notes...)
	}
	if selected["weighted-routing"] || selected["timeouts"] || selected["retries"] || selected["mirroring"] || selected["header-routing"] {
		backends := []any{map[string]any{"name": req.Service, "port": req.Port, "weight": 100}}
		if selected["weighted-routing"] {
			mode := defaultString(req.TrafficMode, "weighted")
			if mode != "weighted" && mode != "equal" && mode != "default" {
				return nil, fmt.Errorf("routing mode must be default, equal or weighted")
			}
			if len(req.Backends) < 2 || len(req.Backends) > 8 {
				return nil, fmt.Errorf("choose two to eight backend Services")
			}
			total := 0
			seen := map[string]bool{}
			backends = nil
			for _, b := range req.Backends {
				o := ws.Get(req.Cluster, "", "services", req.Namespace, b.Name)
				limit := 100
				if req.RelativeWeights {
					limit = 1000000
				}
				if o == nil || !hasPort(o, b.Port) || b.Weight < 0 || b.Weight > limit || seen[b.Name] {
					return nil, fmt.Errorf("backend %q must be a distinct Service in the same namespace, with a valid Service port and weight 0–100", b.Name)
				}
				seen[b.Name] = true
				total += b.Weight
				ref := map[string]any{"name": b.Name, "port": b.Port}
				if mode == "weighted" {
					ref["weight"] = b.Weight
				}
				backends = append(backends, ref)
			}
			if mode == "weighted" && ((!req.RelativeWeights && total != 100) || (req.RelativeWeights && total <= 0)) {
				return nil, fmt.Errorf("backend weights must add up to 100")
			}
		}
		rule := map[string]any{"backendRefs": backends}
		if err := routeOptions(ws, req, selected, rule); err != nil {
			return nil, err
		}
		if selected["timeouts"] {
			d, e := time.ParseDuration(req.Timeout)
			if e != nil || d < 0 || d > 5*time.Minute {
				return nil, fmt.Errorf("request timeout must be between 0s (disabled) and 5m")
			}
			rule["timeouts"] = map[string]any{"request": req.Timeout}
			if req.BackendTimeout != "" {
				bd, e := time.ParseDuration(req.BackendTimeout)
				if e != nil || bd < 0 || bd > 5*time.Minute || (d > 0 && (bd == 0 || bd > d)) {
					return nil, fmt.Errorf("backend timeout must fit within the request timeout")
				}
				M(rule["timeouts"])["backendRequest"] = req.BackendTimeout
			}
		}
		rules := []any{rule}
		if selected["header-routing"] {
			rules = append(rules, map[string]any{"backendRefs": []any{map[string]any{"name": req.Service, "port": req.Port}}})
			p.Notes = append(p.Notes, "Header matches use the chosen backend. A separate catch-all rule sends non-matching requests to the selected Service's original endpoints. Review existing route precedence before applying.")
		}
		obj("gateway.networking.k8s.io/v1", "HTTPRoute", "-route", map[string]any{"parentRefs": []any{map[string]any{"group": "", "kind": "Service", "name": req.Service, "port": req.Port}}, "rules": rules})
		for _, r := range ws.List(req.Cluster, GatewayGroup, "httproutes", req.Namespace, nil) {
			if related(r, svc, "", "") {
				p.Notes = append(p.Notes, "Existing route "+r.Namespace+"/"+r.Name+" may overlap this test route. Acceptance does not prove this route wins route precedence.")
			}
		}
	}
	if selected["load-balancing"] || selected["endpoint-failover"] || selected["locality-failover"] {
		policy := map[string]any{}
		if selected["load-balancing"] {
			if req.Algorithm != "DEFAULT" && req.Algorithm != "ROUND_ROBIN" && req.Algorithm != "LEAST_REQUEST" && req.Algorithm != "RANDOM" {
				return nil, fmt.Errorf("choose ROUND_ROBIN, LEAST_REQUEST or RANDOM")
			}
			if req.Algorithm != "DEFAULT" {
				policy["loadBalancer"] = map[string]any{"simple": req.Algorithm}
			} else {
				p.Notes = append(p.Notes, "Default load balancing leaves the algorithm unspecified. No algorithm override is generated.")
			}
		}
		if selected["endpoint-failover"] || selected["locality-failover"] {
			threshold := req.FailureThreshold
			if threshold == 0 {
				threshold = 3
			}
			percent := req.MaxEjectionPercent
			if percent == 0 {
				percent = 100
			}
			interval := defaultString(req.EjectionInterval, "5s")
			duration := defaultString(req.EjectionDuration, "30s")
			iv, ie := time.ParseDuration(interval)
			dv, de := time.ParseDuration(duration)
			if threshold < 1 || threshold > 100000 || percent < 1 || percent > 100 || ie != nil || de != nil || iv <= 0 || iv > 5*time.Minute || dv <= 0 || dv > time.Hour {
				return nil, fmt.Errorf("choose valid outlier thresholds, interval up to 5m, ejection duration up to 1h and percentage 1–100")
			}
			policy["outlierDetection"] = map[string]any{"consecutive5xxErrors": threshold, "interval": interval, "baseEjectionTime": duration, "maxEjectionPercent": percent}
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
		if len(policy) > 0 || req.Edit != nil {
			obj("networking.istio.io/v1", "DestinationRule", "-destination", map[string]any{"host": req.Service, "trafficPolicy": policy})
		}
		for _, r := range ws.List(req.Cluster, "networking.istio.io", "destinationrules", req.Namespace, nil) {
			host := S(Path(r.Obj, "spec", "host"))
			if serviceHost(host, svc) {
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
		api, kind, err := ratePolicy(ws, req.Cluster, req.Namespace, req.Service)
		if err != nil {
			return nil, err
		}
		burst := req.Requests
		if req.Burst != nil {
			burst = *req.Burst
		}
		if burst < 0 || burst > 1000000 {
			return nil, fmt.Errorf("burst must be between 0 and 1000000")
		}
		obj(api, kind, "-rate", map[string]any{"targetRefs": target, "traffic": map[string]any{"rateLimit": map[string]any{"local": []any{map[string]any{"requests": req.Requests, "unit": req.Unit, "burst": burst}}}}})
		p.Notes = append(p.Notes, "The rate limit is local to each agentgateway proxy. Multiple replicas do not share a global counter.")
	}
	if req.Edit != nil {
		if err := editGenerated(ws, p); err != nil {
			return nil, err
		}
		return finishPlan(p)
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
	if len(p.Objects) == 0 {
		return finishPlan(p)
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
