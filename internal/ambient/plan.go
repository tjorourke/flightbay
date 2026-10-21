package ambient

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
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
	// Name optionally names the primary generated resource. It must be a
	// DNS-1123 label. The default is <service>-<task> for a single task.
	Name               string                 `json:"name,omitempty"`
	Resilience         *ResilienceRequest     `json:"resilience,omitempty"`
	CircuitBreaker     *CircuitBreakerRequest `json:"circuitBreaker,omitempty"`
	Demo               *BookinfoRequest       `json:"demo,omitempty"`
	Explain            *ExplainRequest        `json:"explain,omitempty"`
	Paths              []string               `json:"paths,omitempty"`
	Headers            map[string]string      `json:"headers,omitempty"`
	Issuer             string                 `json:"issuer,omitempty"`
	JWKSURI            string                 `json:"jwksUri,omitempty"`
	JWKS               string                 `json:"jwks,omitempty"`
	RequestPrincipals  []string               `json:"requestPrincipals,omitempty"`
	Mirror             *Backend               `json:"mirror,omitempty"`
	RetryAttempts      int                    `json:"retryAttempts,omitempty"`
	Scorecard          *ScorecardRequest      `json:"scorecard,omitempty"`
	Parent             *Ref                   `json:"parent,omitempty"`
	ParentSection      string                 `json:"parentSection,omitempty"`
	TargetKind         string                 `json:"targetKind,omitempty"`
	TargetName         string                 `json:"targetName,omitempty"`
	TrafficMode        string                 `json:"trafficMode,omitempty"`
	RelativeWeights    bool                   `json:"relativeWeights,omitempty"`
	MTLSMode           string                 `json:"mtlsMode,omitempty"`
	MethodAction       string                 `json:"methodAction,omitempty"`
	Burst              *int                   `json:"burst,omitempty"`
	BackendTimeout     string                 `json:"backendTimeout,omitempty"`
	FailureThreshold   int                    `json:"failureThreshold,omitempty"`
	EjectionInterval   string                 `json:"ejectionInterval,omitempty"`
	EjectionDuration   string                 `json:"ejectionDuration,omitempty"`
	MaxEjectionPercent int                    `json:"maxEjectionPercent,omitempty"`
	Waypoint           *WaypointRequest       `json:"waypoint,omitempty"`
	Selector           map[string]string      `json:"selector,omitempty"`
	SelectedPod        string                 `json:"selectedPod,omitempty"`
	Workload           string                 `json:"workload,omitempty"`
	Replicas           int                    `json:"replicas,omitempty"`
	// Unpublish reverses global-service publication for this one Service by
	// removing global metadata and its owned weighted-split route and Service.
	Unpublish bool `json:"unpublish,omitempty"`
	// GlobalDistribution sets networking.istio.io/traffic-distribution on a
	// global Service: "" leaves it, "default" removes it (PreferNetwork),
	// otherwise Any, PreferClose, PreferRegion or PreferNetwork.
	GlobalDistribution string `json:"globalDistribution,omitempty"`
	// GlobalWeights splits requests to this Service across clusters by weight
	// (cluster name → weight), through the Service's Istio waypoint.
	GlobalWeights map[string]int `json:"globalWeights,omitempty"`
	// GlobalTakeover sets (true) or removes (false) solo.io/service-takeover.
	GlobalTakeover *bool `json:"globalTakeover,omitempty"`
	// RouteMatch is the full HTTPRoute match for Route by request: path,
	// method, headers and query parameters, each Exact or RegularExpression.
	// When set it replaces the exact-only Headers map.
	RouteMatch *RouteMatch `json:"routeMatch,omitempty"`
	// Access builds a Service-targeted AuthorizationPolicy from rules.
	Access               *AccessRequest `json:"access,omitempty"`
	AccessOperation      string         `json:"accessOperation,omitempty"` // append (default) or replace (explicit full-policy edit)
	CallerSources        []AccessSource `json:"callerSources,omitempty"`
	CallersEdited        bool           `json:"callersEdited,omitempty"`
	JWTPrincipals        []string       `json:"jwtPrincipals,omitempty"`
	JWTServiceAccounts   []string       `json:"jwtServiceAccounts,omitempty"`
	SourceServiceAccount string         `json:"sourceServiceAccount,omitempty"`
	TargetPort           int            `json:"targetPort,omitempty"`
	Route                string         `json:"route,omitempty"`
	Edit                 *EditTarget    `json:"edit,omitempty"`
	Cluster              string         `json:"cluster"`
	Namespace            string         `json:"namespace"`
	Service              string         `json:"service"`
	Port                 int            `json:"port"`
	Tasks                []string       `json:"tasks"`
	Backends             []Backend      `json:"backends,omitempty"`
	SourceNamespace      string         `json:"sourceNamespace,omitempty"`
	Methods              []string       `json:"methods,omitempty"`
	Requests             int            `json:"requests,omitempty"`
	Unit                 string         `json:"unit,omitempty"`
	Algorithm            string         `json:"algorithm,omitempty"`
	Timeout              string         `json:"timeout,omitempty"`
	FromRegion           string         `json:"fromRegion,omitempty"`
	ToRegion             string         `json:"toRegion,omitempty"`
}

type CircuitBreakerRequest struct {
	MaxConnections           int    `json:"maxConnections"`
	MaxPendingRequests       int    `json:"maxPendingRequests"`
	MaxRequests              int    `json:"maxRequests"`
	MaxRetries               int    `json:"maxRetries"`
	MaxRequestsPerConnection int    `json:"maxRequestsPerConnection"`
	ConnectTimeout           string `json:"connectTimeout"`
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
	// Effects and Interactions describe what the change does and how it
	// combines with existing configuration. Set by AnnotatePlan.
	Effects      []string      `json:"effects"`
	Interactions []Interaction `json:"interactions"`
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
	extensions := enterpriseWaypointFilters(ws, c, svc)
	out := []Task{
		{"require-waypoint", "Block traffic that skips the waypoint", "L4 ztunnel", security && selector && waypointPrincipal(ws, svc) != "", "Allows only the captured waypoint service account. Requires an unambiguous captured identity and trust domain. Existing ALLOW policies remain additive."},
		{"jwt", "Require a JWT", "L7 Istio waypoint", istio && security && hasResource(ws, c, "security.istio.io", "requestauthentications"), "RequestAuthentication and requestPrincipals authorization on the Service. Agentgateway JWT generation is UNVERIFIED and unavailable: no tested installed-schema policy mapping is established."},
		{"retries", "Retry failed requests", "L7 Istio waypoint", istio && (routeField(ws, c, "retry", "attempts") || extensions), "Uses the installed HTTPRoute retry schema, or route-scoped EnvoyFilter on a captured Solo Enterprise waypoint. Attempts and timeouts apply to one proxy hop; applications must propagate deadlines and cancellation across service calls."},
		{"mirroring", "Mirror requests", "L7 Istio waypoint", istio && routeField(ws, c, "filters", "requestMirror", "backendRef", "name"), "HTTPRoute RequestMirror to a captured Service. Agentgateway waypoint support is UNVERIFIED and unavailable."},
		{"header-routing", "Route matching requests", "L7 waypoint", (istio || agent) && routeField(ws, c, "matches", "headers", "value"), "HTTPRoute match on path, method, headers and query parameters (exact or regular expression) to another backend. Requires the captured installed HTTPRoute schema."},
		{"global-service", "Publish as a global Service", "Multicluster", globalSupported, globalReason},
		{"l4-default-deny", "Deny by default for the selected workload", "L4 ztunnel", security && selector, "Creates an empty ALLOW policy on the Service's workload selector. Other ALLOW policies are additive; this does not override them."},
		{"l4-allow-namespace", "Allow calls from a namespace", "L4 ztunnel", security && selector && wpName == "", "For direct ambient traffic without a selected waypoint. Behind a waypoint, ztunnel sees the waypoint identity; use an L7 policy for original callers."},
		{"l4-allow-service-account", "Allow one service account", "L4 ztunnel", security && selector && wpName == "", "For direct ambient traffic without a waypoint. Allows one captured namespace/account. Other ALLOW policies remain additive."},
		{"strict-mtls", "Require mutual TLS", "L4 ztunnel", selector && hasResource(ws, c, "security.istio.io", "peerauthentications"), "STRICT PeerAuthentication for the selected workload. Ambient does not support DISABLE mode."},
		{"mesh-strict-mtls", "Mesh-wide STRICT mTLS", "L4 ztunnel", meshRootConfirmed(ws, c) && hasResource(ws, c, SecurityGroup, "peerauthentications"), "Cluster-wide default in istio-system. Requires captured mesh-root evidence; namespace and workload overrides remain effective."},
		{"access-rule", "Allow or deny requests by caller, header, path or method", map[bool]string{true: "L7 agentgateway waypoint", false: "L7 Istio waypoint"}[agent], (istio && security) || (agent && agentPolicyAvailable), map[bool]string{true: "Service-targeted agentgateway authorization (CEL) with your rules: request headers, methods and paths. Allow or Deny. Caller identity rules need an Istio waypoint.", false: "Service-targeted AuthorizationPolicy with your own rules: caller namespaces, caller Services or principals, methods, paths and request headers. ALLOW, DENY, AUDIT or CUSTOM."}[agent]},
		{"methods", "Allow selected HTTP methods", "L7 Istio waypoint", istio && security, "Service-targeted AuthorizationPolicy on an Istio waypoint. Other ALLOW policies remain additive."},
		{"weighted-routing", "Balance traffic between Services", "L7 waypoint", (istio || agent) && hasResource(ws, c, GatewayGroup, "httproutes"), "Service-parent HTTPRoute with weighted backends in the same namespace. Weight zero is not primary/backup failover."},
		{"timeouts", "Set a request timeout", "L7 waypoint", (istio || agent) && hasResource(ws, c, GatewayGroup, "httproutes"), "HTTPRoute request timeout, combined with weighted routing when selected. Existing routes may compete; inspect attachment status."},
		{"load-balancing", "Choose endpoint load balancing", "L7 Istio waypoint", istio && dr, "DestinationRule for this Service's endpoints: ROUND_ROBIN, LEAST_REQUEST or RANDOM. This is not a ztunnel load-balancing setting."},
		{"endpoint-failover", "Eject unhealthy endpoints", "L7 Istio waypoint", istio && dr, "DestinationRule outlier detection. Ejects failing endpoints within a Service; does not fail over between different Service names."},
		{"circuit-breaker", "Limit connections and concurrent requests", "L7 Istio waypoint", istio && dr, "DestinationRule connectionPool limits TCP connections, pending HTTP requests, concurrent requests and retries per upstream cluster in each waypoint proxy. Overflow requests can receive HTTP 503. Endpoint ejection is a separate task."},
		{"locality-failover", "Fail over between regions", "L7 Istio waypoint", istio && dr && len(regions(ws, svc)) > 1, "Needs captured endpoint nodes with region labels and endpoints in both regions. Uses locality failover plus outlier detection."},
		{"rate-limit", "Limit request rate locally", "L7 waypoint", (agent && agentPolicyAvailable) || extensions, "AgentgatewayPolicy for agentgateway, or a route/Service-scoped EnvoyFilter on Solo Enterprise Istio. Buckets are per waypoint process, not per application Pod or shared globally. Upstream Istio waypoint EnvoyFilter is unsupported."},
		{"service-failover", "Primary/backup between different Services", "L7 capability-dependent", false, "Gateway API weighted backends do not provide priority failover. No verified portable template is available; Flightbay will not substitute weights for failover."},
		{"global-rate-limit", "Shared/global rate limit", "L7 Enterprise Istio waypoint", extensions, "Calls an explicitly selected external Envoy Rate Limit Service for the selected API's domain and descriptor. The RLS quota and shared backing store must already be configured; this client policy does not deploy or configure the quota store."},
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
		case "mesh-strict-mtls":
			why = "Captured mesh configuration must confirm istio-system as the mesh root. Configure this cluster-wide default from Namespace settings."
			if !hasResource(ws, c, SecurityGroup, "peerauthentications") {
				why = "PeerAuthentication was not found in captured API discovery. Capture the installed security API and mesh-root configuration."
			}
		case "service-failover":
			why = "Not implemented in Flightbay. Enabling this task requires a tested generator for the selected controller's priority-failover API; installing more CRDs or changing weights will not enable it."
		case "global-rate-limit":
			why = "This generator requires Solo Enterprise Istio waypoint EnvoyFilter support and an existing external Envoy Rate Limit Service with a shared quota store. A CRD alone does not establish that integration."
		case "l4-allow-namespace", "l4-allow-service-account":
			if wpName != "" {
				why = "This direct-path template is disabled because a waypoint is selected. For this topology, use a waypoint policy that checks the original caller; Flightbay does not generate that namespace rule here. Do not remove the waypoint just to enable this card."
			}
		case "methods", "access-rule", "load-balancing", "endpoint-failover", "locality-failover", "circuit-breaker":
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
				why = "This needs an agentgateway policy API or captured Solo Enterprise Istio waypoint EnvoyFilter support. Upstream Istio does not support EnvoyFilter on waypoint proxies."
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
	p, err := generate(ws, req)
	if err != nil {
		return nil, err
	}
	return ReconcilePlan(ws, p)
}

func generate(ws *store.Workspace, req Request) (*Plan, error) {
	if contains(req.Tasks, "mesh-strict-mtls") {
		return generateMeshStrictMTLS(ws, req)
	}
	if req.Resilience != nil {
		return generateResilience(ws, req)
	}
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
	if contains(req.Tasks, "access-rule") && !agentWaypoint(ws, req) {
		if req.AccessOperation == "" {
			req.AccessOperation = "append"
		}
		if req.AccessOperation != "append" && req.AccessOperation != "replace" {
			return nil, fmt.Errorf("choose append or explicit full-policy replacement")
		}
		if req.AccessOperation == "replace" && req.Edit == nil {
			return nil, fmt.Errorf("select the existing policy explicitly before replacing its rules")
		}
		if req.Edit == nil && req.Name == "" {
			var candidates []*store.Object
			for _, o := range ws.List(req.Cluster, SecurityGroup, "authorizationpolicies", req.Namespace, nil) {
				if !jwtTargets(o, svc, "", "") {
					continue
				}
				candidates = append(candidates, o)
			}
			if len(candidates) > 1 {
				return nil, fmt.Errorf("several policies target this Service; select the policy to append to, or choose a new policy name")
			}
			if len(candidates) == 1 {
				o := candidates[0]
				if !flightbayOwned(o) {
					return nil, fmt.Errorf("select existing policy %s/%s explicitly before appending to a policy not managed by Flightbay", o.Namespace, o.Name)
				}
				req.Edit = &EditTarget{Ref: reference(o), UID: S(Path(o.Obj, "metadata", "uid")), Fingerprint: configurationFingerprint(o)}
			}
		}
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
	name, explicit, err := resourceName(req, id)
	if err != nil {
		return nil, err
	}
	p := &Plan{ID: id, Request: req, ServiceUID: S(Path(svc.Obj, "metadata", "uid")), Objects: []map[string]any{}, Notes: []string{"Generated from this workspace. Validate against the explicitly selected real kubeconfig context before applying.", "New traffic resources use owned names and cannot overwrite existing objects. Existing edits are shown as scoped field changes with undo.", "Policies and routes can change real traffic. Existing ALLOW policies are additive and existing routes may take precedence."}, Evidence: []Evidence{evidence(svc, "spec", svc.Obj["spec"])}}
	if selected["l4-default-deny"] || selected["l4-allow-namespace"] || selected["strict-mtls"] {
		p.Notes = append(p.Notes, "L4 workload-selector policies affect every Service targeting those Pods, not only the selected Service VIP.")
	}
	if selected["methods"] || selected["rate-limit"] || selected["access-rule"] {
		p.Notes = append(p.Notes, "Waypoint L7 policies only apply to traffic that traverses that waypoint. Direct Pod-IP traffic or other bypass paths need separate L4 controls.")
	}
	obj := func(api, kind, suffix string, spec map[string]any) {
		objName := name + suffix
		if explicit && len(p.Objects) == 0 {
			objName = name // the primary resource carries the chosen name exactly
		}
		p.Objects = append(p.Objects, map[string]any{"apiVersion": api, "kind": kind, "metadata": map[string]any{"name": objName, "namespace": req.Namespace, "labels": map[string]any{"app.kubernetes.io/managed-by": "flightbay", "flightbay.dev/test-id": id}}, "spec": spec})
	}
	sel := map[string]any{"matchLabels": Path(svc.Obj, "spec", "selector")}
	if selected["l4-default-deny"] {
		obj("security.istio.io/v1", "AuthorizationPolicy", "-deny", map[string]any{"selector": sel, "action": "ALLOW"})
	}
	if selected["require-waypoint"] {
		p.Evidence = append(p.Evidence, waypointIdentityEvidence(ws, svc)...)
		obj("security.istio.io/v1", "AuthorizationPolicy", "-waypoint", map[string]any{"selector": sel, "action": "ALLOW", "rules": []any{map[string]any{"from": []any{map[string]any{"source": map[string]any{"principals": []string{waypointPrincipal(ws, svc)}}}}}}})
		p.Notes = append(p.Notes, "This workload-wide ALLOW admits the waypoint identity. Direct Pod-IP callers are refused only if no other ALLOW rule admits them. Remove or narrow conflicting caller-allowing rules.")
		p.Evidence = append(p.Evidence, l4DependencyEvidence(ws, req.Cluster)...)
		for _, pe := range servicePolicies(ws, svc) {
			if pe.Action == "ALLOW" && pe.Enforced == "ztunnel" {
				p.Notes = append(p.Notes, "Review additive ALLOW policy "+pe.Ref.Namespace+"/"+pe.Ref.Name+"; remove or narrow any rules admitting direct callers.")
			}
		}
	}
	if selected["l4-allow-namespace"] {
		if req.SourceNamespace == "" || ws.Get(req.Cluster, "", "namespaces", "", req.SourceNamespace) == nil {
			return nil, fmt.Errorf("choose a captured source namespace")
		}
		obj("security.istio.io/v1", "AuthorizationPolicy", "-allow", map[string]any{"selector": sel, "action": "ALLOW", "rules": []any{map[string]any{"from": []any{map[string]any{"source": map[string]any{"namespaces": []string{req.SourceNamespace}}}}}}})
	}
	if selected["l4-allow-service-account"] {
		parts := strings.Split(req.SourceServiceAccount, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.Contains(req.SourceServiceAccount, "*") {
			return nil, fmt.Errorf("choose one captured service account as namespace/account")
		}
		captured := ws.Get(req.Cluster, "", "serviceaccounts", parts[0], parts[1]) != nil
		for _, pod := range ws.List(req.Cluster, "", "pods", parts[0], nil) {
			captured = captured || defaultString(S(Path(pod.Obj, "spec", "serviceAccountName")), "default") == parts[1]
		}
		if !captured {
			return nil, fmt.Errorf("choose a captured service account")
		}
		obj("security.istio.io/v1", "AuthorizationPolicy", "-allow", map[string]any{"selector": sel, "action": "ALLOW", "rules": []any{map[string]any{"from": []any{map[string]any{"source": map[string]any{"serviceAccounts": []string{req.SourceServiceAccount}}}}}}})
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
		methodReq := req
		methodReq.Access = &AccessRequest{Action: action, Rules: []AccessRule{{From: req.CallerSources, To: []AccessOperation{{Methods: req.Methods}}}}}
		spec, _, err := generateAccessRule(ws, methodReq, svc)
		if err != nil {
			return nil, err
		}
		obj("security.istio.io/v1", "AuthorizationPolicy", "-methods", spec)
	}
	if selected["access-rule"] && agentWaypoint(ws, req) {
		api, kind, err := ratePolicy(ws, req.Cluster, req.Namespace, req.Service)
		if err != nil {
			return nil, err
		}
		spec, notes, err := agentgatewayAccess(req)
		if err != nil {
			return nil, err
		}
		obj(api, kind, "-access", spec)
		p.Notes = append(p.Notes, notes...)
	} else if selected["access-rule"] {
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
	if selected["load-balancing"] || selected["endpoint-failover"] || selected["locality-failover"] || selected["circuit-breaker"] {
		policy := map[string]any{}
		if selected["circuit-breaker"] {
			b := req.CircuitBreaker
			if b == nil {
				return nil, fmt.Errorf("provide circuit-breaker connection and request limits")
			}
			for _, n := range []int{b.MaxConnections, b.MaxPendingRequests, b.MaxRequests, b.MaxRetries} {
				if n < 1 || n > 1000000 {
					return nil, fmt.Errorf("circuit-breaker connection, pending, concurrent and retry limits must be between 1 and 1000000")
				}
			}
			if b.MaxRequestsPerConnection < 0 || b.MaxRequestsPerConnection > 1000000 {
				return nil, fmt.Errorf("requests per connection must be between 0 (unlimited) and 1000000")
			}
			d, err := time.ParseDuration(b.ConnectTimeout)
			if err != nil || d < time.Millisecond || d > 5*time.Minute {
				return nil, fmt.Errorf("choose a circuit-breaker connect timeout from 1ms to 5m")
			}
			policy["connectionPool"] = map[string]any{"tcp": map[string]any{"maxConnections": b.MaxConnections, "connectTimeout": b.ConnectTimeout}, "http": map[string]any{"http1MaxPendingRequests": b.MaxPendingRequests, "http2MaxRequests": b.MaxRequests, "maxRetries": b.MaxRetries, "maxRequestsPerConnection": b.MaxRequestsPerConnection}}
			p.Notes = append(p.Notes, "Connection-pool circuit breakers apply per upstream cluster in each waypoint proxy, not as a shared request-rate quota. Overflow can return HTTP 503. maxRequestsPerConnection zero means unlimited requests per connection. These limits apply after routing: select the backend Service to limit when an HTTPRoute routes to another Service.")
		}
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

var dns1123Label = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// ValidResourceName reports whether s is a DNS-1123 label (at most 63 characters).
func ValidResourceName(s string) bool { return len(s) <= 63 && dns1123Label.MatchString(s) }

// resourceName chooses the base name for generated resources. A requested
// name, or <service>-<task> for one task, names the primary resource exactly;
// further resources add their suffix. Edits never rename existing resources and
// multi-task requests keep the hash-based name so their objects stay distinct.
func resourceName(req Request, id string) (name string, explicit bool, err error) {
	legacy := "flightbay-test-" + id
	if req.Edit != nil {
		return legacy, false, nil
	}
	if req.Name != "" {
		if !ValidResourceName(req.Name) {
			return "", false, fmt.Errorf("%q is not a valid name. Use lowercase letters, digits and hyphens, starting and ending with a letter or digit, up to 63 characters", req.Name)
		}
		return req.Name, true, nil
	}
	if len(req.Tasks) == 1 {
		if d := req.Service + "-" + req.Tasks[0]; ValidResourceName(d) {
			return d, true, nil
		}
	}
	return legacy, false, nil
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

// agentWaypoint reports whether the Service's waypoint is agentgateway.
func agentWaypoint(ws *store.Workspace, req Request) bool {
	svc := ws.Get(req.Cluster, "", "services", req.Namespace, req.Service)
	if svc == nil {
		return false
	}
	e := DetectEnvironment(ws, req.Cluster, svc)
	return e.Selected != nil && e.Selected.Product == "agentgateway"
}

func meshRootConfirmed(ws *store.Workspace, cluster string) bool {
	found := false
	for _, cm := range ws.List(cluster, "", "configmaps", "", nil) {
		if cm.Name != "istio" && !strings.HasPrefix(cm.Name, "istio-") {
			continue
		}
		raw := S(Path(cm.Obj, "data", "mesh"))
		var mesh map[string]any
		if raw == "" || yaml.Unmarshal([]byte(raw), &mesh) != nil {
			continue
		}
		if defaultString(S(mesh["rootNamespace"]), "istio-system") != "istio-system" {
			return false
		}
		found = true
	}
	return found
}

type MeshMTLSState struct {
	Cluster   string     `json:"cluster"`
	Confirmed bool       `json:"confirmed"`
	Defaults  []Editable `json:"defaults"`
	Overrides []Ref      `json:"overrides"`
}

func meshMTLSState(ws *store.Workspace, cluster string) MeshMTLSState {
	s := MeshMTLSState{Cluster: cluster, Confirmed: meshRootConfirmed(ws, cluster), Defaults: []Editable{}, Overrides: []Ref{}}
	for _, o := range ws.List(cluster, SecurityGroup, "peerauthentications", "", nil) {
		if o.Namespace == "istio-system" && len(M(Path(o.Obj, "spec", "selector"))) == 0 {
			req := Request{Cluster: cluster, Namespace: "istio-system", Tasks: []string{"mesh-strict-mtls"}, MTLSMode: S(Path(o.Obj, "spec", "mtls", "mode")), Edit: &EditTarget{Ref: reference(o), UID: S(Path(o.Obj, "metadata", "uid")), Fingerprint: configurationFingerprint(o)}}
			s.Defaults = append(s.Defaults, Editable{Ref: reference(o), Supported: true, Request: &req, Tasks: req.Tasks, Reason: "Mesh default; more-specific overrides remain effective"})
		} else {
			s.Overrides = append(s.Overrides, reference(o))
		}
	}
	return s
}

func generateMeshStrictMTLS(ws *store.Workspace, req Request) (*Plan, error) {
	if len(req.Tasks) != 1 || req.Namespace != "istio-system" || !meshRootConfirmed(ws, req.Cluster) {
		return nil, fmt.Errorf("mesh-wide STRICT requires captured evidence that istio-system is the mesh root; choose the cluster-wide task in namespace settings")
	}
	if !hasResource(ws, req.Cluster, SecurityGroup, "peerauthentications") {
		return nil, fmt.Errorf("PeerAuthentication API was not captured")
	}
	ns := ws.Get(req.Cluster, "", "namespaces", "", "istio-system")
	if ns == nil {
		return nil, fmt.Errorf("capture the istio-system namespace")
	}
	p := basePlan(req, ns)
	state := meshMTLSState(ws, req.Cluster)
	if len(state.Defaults) > 0 && req.Edit == nil {
		return nil, fmt.Errorf("an existing root default is captured; select it explicitly instead of creating a competing PeerAuthentication")
	}
	if req.Edit != nil {
		var selected *store.Object
		for _, e := range state.Defaults {
			if e.Ref == req.Edit.Ref {
				selected = ws.Get(req.Cluster, SecurityGroup, "peerauthentications", "istio-system", e.Ref.Name)
			}
		}
		if selected == nil || req.Edit.UID != S(Path(selected.Obj, "metadata", "uid")) || req.Edit.Fingerprint != configurationFingerprint(selected) {
			return nil, fmt.Errorf("root default changed; reload its current settings")
		}
		change(p, selected, "Set the explicitly selected mesh default to STRICT; preserve other fields", map[string]any{"/spec/mtls/mode": "STRICT"})
	} else {
		name := defaultString(req.Name, "mesh-strict-mtls")
		if !ValidResourceName(name) {
			return nil, fmt.Errorf("choose a valid policy name")
		}
		p.Objects = append(p.Objects, map[string]any{"apiVersion": "security.istio.io/v1", "kind": "PeerAuthentication", "metadata": map[string]any{"name": name, "namespace": "istio-system"}, "spec": map[string]any{"mtls": map[string]any{"mode": "STRICT"}}})
	}
	p.Notes = append(p.Notes, "Cluster-wide mesh default: STRICT mTLS in the captured root namespace istio-system. More-specific namespace, workload and port overrides remain effective.")
	for _, ref := range state.Overrides {
		p.Notes = append(p.Notes, "Review more-specific override "+ref.Namespace+"/"+ref.Name)
	}
	for _, k := range []struct{ group, resource, kind string }{{SecurityGroup, "peerauthentications", "PeerAuthentication"}, {"", "configmaps", "ConfigMap"}} {
		b, _ := json.Marshal(l4DependencyValue(ws, req.Cluster, k.group, k.resource))
		p.Evidence = append(p.Evidence, Evidence{Ref: Ref{Cluster: req.Cluster, Group: k.group, Resource: k.resource, Kind: k.kind}, Field: "l4-dependencies", Value: string(b)})
	}
	return finishPlan(p)
}
