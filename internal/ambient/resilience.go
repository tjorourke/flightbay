package ambient

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tjorourke/flightbay/internal/store"
)

// ResilienceRequest selects an actual route boundary. Local buckets are per
// waypoint process, never per application Pod. Global quotas belong to an
// explicitly configured external Envoy Rate Limit Service.
type ResilienceRequest struct {
	Scope              string         `json:"scope"`
	Match              *RouteMatch    `json:"match,omitempty"`
	Route              string         `json:"route,omitempty"`
	Rule               int            `json:"rule,omitempty"`
	Backend            string         `json:"backend,omitempty"`
	RetryOn            string         `json:"retryOn,omitempty"`
	FailurePercentage  int            `json:"failurePercentage,omitempty"`
	DisableConsecutive bool           `json:"disableConsecutive,omitempty"`
	Global             *GlobalLimiter `json:"global,omitempty"`
}
type GlobalLimiter struct {
	Mesh            bool   `json:"mesh,omitempty"`
	MeshDomain      string `json:"meshDomain,omitempty"`
	Namespace       string `json:"namespace"`
	Service         string `json:"service"`
	Port            int    `json:"port"`
	Domain          string `json:"domain"`
	DescriptorKey   string `json:"descriptorKey"`
	DescriptorValue string `json:"descriptorValue"`
	FailClosed      bool   `json:"failClosed"`
}

func enterpriseWaypointFilters(ws *store.Workspace, c string, svc *store.Object) bool {
	e := DetectEnvironment(ws, c, svc)
	return e.Selected != nil && e.Selected.Supported && e.Selected.Product == "istio" && e.Selected.Edition == "enterprise" && hasResource(ws, c, "networking.istio.io", "envoyfilters")
}

func generateResilience(ws *store.Workspace, req Request) (*Plan, error) {
	req=normaliseResilienceScope(req)
	svc := ws.Get(req.Cluster, "", "services", req.Namespace, req.Service)
	if svc == nil || !hasPort(svc, req.Port) {
		return nil, fmt.Errorf("select a captured Service and Service port")
	}
	if !enterpriseWaypointFilters(ws, req.Cluster, svc) {
		return nil, fmt.Errorf("route-scoped resilience requires a captured Solo Enterprise Istio waypoint and EnvoyFilter API; upstream Istio does not support EnvoyFilter on waypoints")
	}
	if len(req.Tasks) == 0 {
		return nil, fmt.Errorf("select a resilience control")
	}
	seen := map[string]bool{}
	for _, task := range req.Tasks {
		if seen[task] || !contains([]string{"rate-limit", "global-rate-limit", "retries", "circuit-breaker", "endpoint-failover", "timeouts"}, task) {
			return nil, fmt.Errorf("unsupported or repeated resilience task %s", task)
		}
		seen[task] = true
	}
	breaker := seen["circuit-breaker"] || seen["endpoint-failover"]
	r := req.Resilience
	if r == nil {
		return nil, fmt.Errorf("choose a resilience scope")
	}
	if r.FailurePercentage < 0 || r.FailurePercentage > 100 {
		return nil, fmt.Errorf("failure percentage must be between 0 (off) and 100")
	}
	if r.Scope != "service" && r.Scope != "api" && r.Scope != "route" {
		return nil, fmt.Errorf("choose whole Service, API path or existing HTTPRoute rule")
	}
	wn, wp, _ := Waypoint(ws, svc)
	if wn != req.Namespace {
		return nil, fmt.Errorf("this extension requires the Service and waypoint Gateway in the same namespace")
	}
	if req.Edit != nil {
		existing := Target(ws, req.Cluster, req.Edit.Ref.Kind, req.Edit.Ref.Namespace, req.Edit.Ref.Name)
		if existing == nil || !flightbayOwned(existing) || configurationFingerprint(existing) != req.Edit.Fingerprint {
			return nil, fmt.Errorf("the resilience configuration changed; reopen its captured settings")
		}
		var original Request
		if json.Unmarshal([]byte(S(Path(existing.Obj, "metadata", "annotations", "flightbay.dev/resilience-request"))), &original) == nil && original.Resilience != nil {
			boundary := func(q Request) string {
				q=normaliseResilienceScope(q)
				data, _ := json.Marshal([]any{q.Port, q.Resilience.Scope, q.Resilience.Match, q.Resilience.Route, q.Resilience.Rule})
				return string(data)
			}
			if boundary(original) != boundary(req) {
				return nil, fmt.Errorf("editing retains this API boundary; create a new scoped configuration and undo the old one to change the path, method, route or port")
			}
		}
	}
	p := basePlan(req, svc)
	p.ServiceUID = S(Path(svc.Obj, "metadata", "uid"))
	identity, _ := json.Marshal([]any{req.Cluster, req.Namespace, req.Service, req.Port, r.Scope, r.Match, r.Route, r.Rule})
	hash := sha256.Sum256(identity)
	name := "flightbay-resilience-" + hex.EncodeToString(hash[:])[:12]
	if req.Edit != nil && req.Edit.Ref.Kind == "EnvoyFilter" && strings.HasPrefix(req.Edit.Ref.Name, "flightbay-resilience-") {
		name = strings.TrimSuffix(req.Edit.Ref.Name, "-extension")
	}
	recorded := req
	recorded.Edit = nil
	previous := ws.Get(req.Cluster, "networking.istio.io", "envoyfilters", req.Namespace, name+"-extension")
	if previous != nil {
		if !flightbayOwned(previous) || previous.Labels()["flightbay.dev/resilience-service"] != req.Service {
			return nil, fmt.Errorf("the API resilience resource already exists without matching Flightbay ownership")
		}
		recorded = mergeResilienceRequest(previous, req)
	}
	// The collector canonicalises embedded JSON. Store the same canonical
	// map ordering so captured annotation preconditions match the live value.
	raw, _ := json.Marshal(Copy(recorded))
	add := func(api, kind, suffix string, spec map[string]any) {
		p.Objects = append(p.Objects, map[string]any{"apiVersion": api, "kind": kind, "metadata": map[string]any{"name": name + suffix, "namespace": req.Namespace, "labels": map[string]any{"app.kubernetes.io/managed-by": "flightbay", "flightbay.dev/test-id": p.ID, "flightbay.dev/resilience-service": req.Service}, "annotations": map[string]any{"flightbay.dev/resilience-request": string(raw)}}, "spec": spec})
	}
	host := serviceDNS(ws, svc).LocalName
	routeName := ""
	backend := defaultString(r.Backend, req.Service)
	if r.Scope == "route" {
		route := ws.Get(req.Cluster, GatewayGroup, "httproutes", req.Namespace, r.Route)
		attached := false
		if route != nil {
			for _, v := range L(Path(route.Obj, "spec", "parentRefs")) {
				ref := M(v)
				attached = attached || (S(ref["kind"]) == "Service" && S(ref["group"]) == "" && S(ref["name"]) == svc.Name && defaultString(S(ref["namespace"]), route.Namespace) == svc.Namespace && (N(ref["port"]) == 0 || N(ref["port"]) == req.Port))
			}
		}
		if route == nil || !attached || r.Rule < 0 || r.Rule >= len(L(Path(route.Obj, "spec", "rules"))) {
			return nil, fmt.Errorf("choose a captured HTTPRoute rule attached to this Service")
		}
		routeName = fmt.Sprintf("%s.%s.%d", req.Namespace, r.Route, r.Rule)
		if breaker {
			return nil, fmt.Errorf("API circuit isolation needs a dedicated API path and backend alias; choose API path rather than modifying a shared route backend")
		}
		p.Evidence = append(p.Evidence, evidence(route, "spec", route.Obj["spec"]))
	}
	if r.Scope == "api" {
		if r.Match == nil || r.Match.Path == "" || r.Match.Path == "/" {
			return nil, fmt.Errorf("choose a non-root API path to isolate; use whole Service for a catch-all")
		}
		match, err := buildRouteMatch(*r.Match)
		if err != nil {
			return nil, err
		}
		if existing := ws.Get(req.Cluster, GatewayGroup, "httproutes", req.Namespace, name+"-route"); existing != nil && flightbayOwned(existing) {
			refs := L(Path(M(L(Path(existing.Obj, "spec", "rules"))[0]), "backendRefs"))
			if len(refs) != 1 {
				return nil, fmt.Errorf("the existing API has multiple backends; select its captured HTTPRoute rule instead")
			}
			backend = S(M(refs[0])["name"])
			p.Notes = append(p.Notes, "This API already has a generated route. Its backend is retained so adding another resilience control cannot bypass an existing circuit-isolation alias.")
		}
		dest := ws.Get(req.Cluster, "", "services", req.Namespace, backend)
		if dest == nil || !hasPort(dest, req.Port) {
			return nil, fmt.Errorf("choose a captured backend exposing the selected Service port")
		}
		if breaker {
			if len(M(Path(dest.Obj, "spec", "selector"))) == 0 {
				return nil, fmt.Errorf("API isolation requires a backend with a captured Pod selector")
			}
			alias := name + "-backend"
			add("v1", "Service", "-backend", map[string]any{"selector": Copy(Path(dest.Obj, "spec", "selector")), "ports": Copy(Path(dest.Obj, "spec", "ports"))})
			M(Path(p.Objects[len(p.Objects)-1], "metadata", "labels"))["istio.io/use-waypoint"] = wp
			backend = alias
			host = strings.Replace(serviceDNS(ws, svc).LocalName, req.Service+".", alias+".", 1)
			p.Notes = append(p.Notes, "The backend alias selects the existing application Pods. Its distinct upstream cluster isolates this API's connection pool and outlier state; other APIs retain their existing routes. This is not a new application Deployment.")
		}
		add(GatewayGroup+"/v1", "HTTPRoute", "-route", map[string]any{"parentRefs": []any{map[string]any{"group": "", "kind": "Service", "name": req.Service, "port": req.Port}}, "rules": []any{map[string]any{"matches": []any{match}, "backendRefs": []any{map[string]any{"name": backend, "port": req.Port}}}, map[string]any{"backendRefs": []any{map[string]any{"name": req.Service, "port": req.Port}}}}})
		routeName = req.Namespace + "." + name + "-route.0"
		p.Notes = append(p.Notes, "The API rule competes under normal HTTPRoute precedence. A separate catch-all retains the original Service backend for unmatched APIs when no other route matches. Inspect existing catch-all and specific route precedence; acceptance alone does not establish that this rule wins.")
	}
	routeHost := serviceDNS(ws, svc).LocalName
	routeMatch := map[string]any{"context": "GATEWAY", "routeConfiguration": map[string]any{"name": fmt.Sprintf("inbound-vip|%d|http|%s", req.Port, routeHost)}}
	if routeName != "" {
		M(routeMatch["routeConfiguration"])["vhost"] = map[string]any{"route": map[string]any{"name": routeName}}
	}
	patches := []any{}
	globalRoutes := []map[string]any{}
	routeMatches := []map[string]any{routeMatch}
	for _, alias := range serviceDNS(ws, svc).GlobalNames {
		if r.Scope == "route" {
			continue
		} // an explicitly selected rule does not imply a global alias rule
		globalMatch := map[string]any{"context": "GATEWAY", "routeConfiguration": map[string]any{"name": fmt.Sprintf("inbound-vip|%d|http|%s", req.Port, alias)}}
		if r.Scope == "api" {
			globalRoute := routeName + ".global"
			match, err := resilienceEnvoyMatch(*r.Match)
			if err != nil {
				return nil, err
			}
			backendHost := backend + "." + req.Namespace + strings.TrimPrefix(serviceDNS(ws, svc).LocalName, req.Service+"."+req.Namespace)
			value := map[string]any{"name": globalRoute, "match": match, "route": map[string]any{"cluster": fmt.Sprintf("inbound-vip|%d|http|%s", req.Port, backendHost), "timeout": "0s"}}
			globalRoutes = append(globalRoutes, value)
			patches = append(patches, map[string]any{"applyTo": "HTTP_ROUTE", "match": globalMatch, "patch": map[string]any{"operation": "INSERT_FIRST", "value": value}})
			continue
		}
		routeMatches = append(routeMatches, globalMatch)
	}
	routePatch := func(value map[string]any) {
		// Istio merges existing routes before inserting new ones. Put policy
		// settings directly on inserted alias routes rather than expecting a
		// later MERGE to find a route which has not been inserted yet.
		for _, route := range globalRoutes {
			mergeResilienceRoute(route, value)
		}
		for _, match := range routeMatches {
			patches = append(patches, map[string]any{"applyTo": "HTTP_ROUTE", "match": match, "patch": map[string]any{"operation": "MERGE", "value": value}})
		}
	}
	insertFilter := func(filter string, config map[string]any) {
		patches = append(patches, map[string]any{"applyTo": "HTTP_FILTER", "match": map[string]any{"context": "GATEWAY", "listener": map[string]any{"filterChain": map[string]any{"filter": map[string]any{"name": "envoy.filters.network.http_connection_manager", "subFilter": map[string]any{"name": "envoy.filters.http.router"}}}}}, "patch": map[string]any{"operation": "INSERT_BEFORE", "value": map[string]any{"name": filter, "typed_config": config}}})
	}
	for _, task := range req.Tasks {
		switch task {
		case "rate-limit":
			if req.Requests < 1 || req.Requests > 1000000 {
				return nil, fmt.Errorf("requests must be between 1 and 1000000")
			}
			interval := map[string]string{"Seconds": "1s", "Minutes": "60s", "Hours": "3600s"}[req.Unit]
			if interval == "" {
				return nil, fmt.Errorf("choose Seconds, Minutes or Hours")
			}
			burst := req.Requests
			if req.Burst != nil {
				burst = *req.Burst
			}
			if burst < 1 || burst > 1000000 {
				return nil, fmt.Errorf("burst capacity must be between 1 and 1000000")
			}
			filter := "envoy.filters.http.local_ratelimit." + name
			typ := "type.googleapis.com/envoy.extensions.filters.http.local_ratelimit.v3.LocalRateLimit"
			insertFilter(filter, map[string]any{"@type": typ, "stat_prefix": name})
			config := map[string]any{"@type": typ, "stat_prefix": name, "token_bucket": map[string]any{"max_tokens": burst, "tokens_per_fill": req.Requests, "fill_interval": interval}, "filter_enabled": map[string]any{"default_value": map[string]any{"numerator": 100, "denominator": "HUNDRED"}}, "filter_enforced": map[string]any{"default_value": map[string]any{"numerator": 100, "denominator": "HUNDRED"}}, "response_headers_to_add": []any{map[string]any{"append": false, "header": map[string]any{"key": "x-flightbay-local-rate-limit", "value": "true"}}}}
			value := map[string]any{"typed_per_filter_config": map[string]any{filter: config}}
			if r.Scope == "service" {
				for _, match := range routeMatches {
					patches = append(patches, map[string]any{"applyTo": "VIRTUAL_HOST", "match": match, "patch": map[string]any{"operation": "MERGE", "value": value}})
				}
			} else {
				routePatch(value)
			}
			p.Notes = append(p.Notes, "The token bucket belongs to each waypoint Envoy process and selected route/virtual host. It is not a quota per application Pod and is not shared across waypoint replicas. Unmatched APIs have no bucket from this policy.")
		case "global-rate-limit":
			g := r.Global
			if g == nil {
				return nil, fmt.Errorf("select an existing external Envoy Rate Limit Service and its configured domain/descriptor")
			}
			server := ws.Get(req.Cluster, "", "services", g.Namespace, g.Service)
			meshHost := g.Service + "." + g.Namespace + "." + g.MeshDomain
			capturedAlias := false
			if g.Mesh && g.MeshDomain != "" {
				for _, alias := range ws.List(req.Cluster, "networking.istio.io", "serviceentries", "", nil) {
					for _, host := range L(Path(alias.Obj, "spec", "hosts")) {
						if S(host) != meshHost {
							continue
						}
						for _, port := range L(Path(alias.Obj, "spec", "ports")) {
							if N(M(port)["number"]) == g.Port {
								capturedAlias = true
								p.Evidence = append(p.Evidence, evidence(alias, "spec", alias.Obj["spec"]))
							}
						}
					}
				}
			}
			if ((!g.Mesh && (server == nil || !hasPort(server, g.Port))) || (g.Mesh && !capturedAlias)) || g.Domain == "" || g.DescriptorKey == "" || g.DescriptorValue == "" {
				return nil, fmt.Errorf("provide a captured rate-limit Service port, configured domain and API descriptor key/value")
			}
			filter := "envoy.filters.http.ratelimit." + name
			cluster := name + "-rls"
			if g.Mesh {
				cluster = fmt.Sprintf("inbound-vip|%d|http|%s", g.Port, meshHost)
			}
			insertFilter(filter, map[string]any{"@type": "type.googleapis.com/envoy.extensions.filters.http.ratelimit.v3.RateLimit", "domain": g.Domain, "timeout": "0.1s", "failure_mode_deny": g.FailClosed, "rate_limit_service": map[string]any{"grpc_service": map[string]any{"envoy_grpc": map[string]any{"cluster_name": cluster}}, "transport_api_version": "V3"}})
			actions := []any{map[string]any{"actions": []any{map[string]any{"generic_key": map[string]any{"descriptor_key": g.DescriptorKey, "descriptor_value": g.DescriptorValue}}}}}
			routePatch(map[string]any{"typed_per_filter_config": map[string]any{filter: map[string]any{"@type": "type.googleapis.com/envoy.extensions.filters.http.ratelimit.v3.RateLimitPerRoute", "domain": g.Domain, "rate_limits": actions}}})
			if !g.Mesh {
				patches = append(patches, map[string]any{"applyTo": "CLUSTER", "match": map[string]any{"context": "GATEWAY"}, "patch": map[string]any{"operation": "ADD", "value": map[string]any{"name": cluster, "type": "STRICT_DNS", "connect_timeout": "5s", "http2_protocol_options": map[string]any{}, "load_assignment": map[string]any{"cluster_name": cluster, "endpoints": []any{map[string]any{"lb_endpoints": []any{map[string]any{"endpoint": map[string]any{"address": map[string]any{"socket_address": map[string]any{"address": serviceDNS(ws, server).LocalName, "port_value": g.Port}}}}}}}}}}})
				p.Evidence = append(p.Evidence, evidence(server, "spec", server.Obj["spec"]))
			}
			p.Notes = append(p.Notes, "This configures the waypoint's gRPC client, not the external quota store. Ambient setup must already provide the RLS, shared Redis and Redis authentication through Secret references. Flightbay does not deploy them or copy Redis credentials into this policy. The RLS must already implement this domain and descriptor with the desired threshold. All waypoints must use the same logical RLS and shared quota store for a consistent global counter. Different API descriptor values isolate quotas. Fail-open permits requests if RLS is unavailable; fail-closed can reject only routes using this filter.")
			if !g.Mesh {
				p.Notes = append(p.Notes, "The direct RLS connection is plaintext HTTP/2; use only an endpoint configured for that transport.")
			}
			if g.Mesh {
				p.Notes = append(p.Notes, "The published mesh endpoint uses Istio's delivered upstream cluster and its mesh/HBONE transport instead of a separate plaintext DNS cluster. Publish the existing limiter through ambient setup first; every selected caller cluster must have captured alias evidence.")
			}
		case "retries":
			if req.RetryAttempts < 1 || req.RetryAttempts > 10 {
				return nil, fmt.Errorf("retry attempts must be between 1 and 10")
			}
			on := defaultString(r.RetryOn, "connect-failure,refused-stream")
			conditions := strings.Split(on, ",")
			for i, v := range conditions {
				conditions[i] = strings.TrimSpace(v)
				if !contains([]string{"5xx", "gateway-error", "connect-failure", "refused-stream", "reset", "retriable-4xx"}, strings.TrimSpace(v)) {
					return nil, fmt.Errorf("unsupported retry condition %s", v)
				}
			}
			on = strings.Join(conditions, ",")
			total, err := time.ParseDuration(req.Timeout)
			attempt, e := time.ParseDuration(req.BackendTimeout)
			if err != nil || e != nil || total <= 0 || total > 5*time.Minute || attempt <= 0 || attempt > total {
				return nil, fmt.Errorf("set a total timeout up to 5m and a positive per-attempt timeout within it")
			}
			routePatch(map[string]any{"route": map[string]any{"timeout": envoyResilienceDuration(total), "retry_policy": map[string]any{"num_retries": req.RetryAttempts, "retry_on": on, "per_try_timeout": envoyResilienceDuration(attempt)}}})
			p.Notes = append(p.Notes, "Retries and the total timeout belong to this proxy hop. Envoy stops its own retries when its downstream stream is reset or its request timeout expires. An application must propagate deadlines and cancellation to calls it makes to other services; configuring A's timeout does not automatically cancel B to C. Put retries at one selected layer, use small attempt counts and time budgets, and account for application retries to avoid multiplying attempts across a chain.")
		case "circuit-breaker", "endpoint-failover":
			if task == "endpoint-failover" && seen["circuit-breaker"] {
				continue
			}
			// Reuse the existing validated DestinationRule generator, against the
			// original captured Service, then retarget its policy to the API alias.
			copy := req
			copy.Resilience = nil
			copy.Edit = nil
			copy.Tasks = []string{task}
			if task == "circuit-breaker" {
				copy.Tasks = []string{"circuit-breaker", "endpoint-failover"}
			}
			plan, err := generate(ws, copy)
			if err != nil {
				return nil, err
			}
			for _, o := range plan.Objects {
				if S(o["kind"]) == "DestinationRule" {
					spec := M(Copy(o["spec"]))
					spec["host"] = host
					if r.DisableConsecutive {
						M(Path(spec, "trafficPolicy", "outlierDetection"))["consecutive5xxErrors"] = 0
					}
					add("networking.istio.io/v1", "DestinationRule", "-destination", spec)
				}
			}
			previousPercentage := false
			if previous != nil {
				for _, v := range L(Path(previous.Obj, "spec", "configPatches")) {
					previousPercentage = previousPercentage || Path(M(v), "patch", "value", "outlier_detection", "failure_percentage_threshold") != nil
				}
			}
			if r.FailurePercentage > 0 || previousPercentage {
				if r.FailurePercentage > 100 {
					return nil, fmt.Errorf("failure percentage must be between 1 and 100")
				}
				enforcing := 100
				if r.FailurePercentage == 0 {
					enforcing = 0
				}
				patches = append(patches, map[string]any{"applyTo": "CLUSTER", "match": map[string]any{"context": "GATEWAY", "cluster": map[string]any{"name": fmt.Sprintf("inbound-vip|%d|http|%s", req.Port, host)}}, "patch": map[string]any{"operation": "MERGE", "value": map[string]any{"outlier_detection": map[string]any{"failure_percentage_threshold": r.FailurePercentage, "failure_percentage_minimum_hosts": 1, "failure_percentage_request_volume": 50, "enforcing_failure_percentage": enforcing}}}})
			}
			p.Notes = append(p.Notes, "Connection overflow rejects excess matching requests. Outlier detection ejects failing endpoints; consecutive-error and error-percentage detection are per upstream cluster in each proxy, not a globally shared closed/open/half-open breaker. Percentage detection requires at least 50 requests per interval. Native latency-threshold opening is not generated; use request timeouts and external/application health logic for that requirement.")
		case "timeouts":
			d, err := time.ParseDuration(req.Timeout)
			if err != nil || d < 0 || d > 5*time.Minute {
				return nil, fmt.Errorf("request timeout must be between 0s and 5m")
			}
			routePatch(map[string]any{"route": map[string]any{"timeout": envoyResilienceDuration(d)}})
		}
	}
	if len(patches) > 0 {
		if previous != nil {
			patches = mergeResiliencePatches(L(Path(previous.Obj, "spec", "configPatches")), patches)
		} else {
			patches = mergeResiliencePatches(nil, patches)
		}
		add("networking.istio.io/v1alpha3", "EnvoyFilter", "-extension", map[string]any{"targetRefs": []any{map[string]any{"group": GatewayGroup, "kind": "Gateway", "name": wp}}, "configPatches": patches})
	}
	p.Notes = append(p.Notes, "Service/API scope also covers captured published global names. Newly published aliases require refreshing and regenerating the policy. Each local token bucket belongs to its route or virtual host; use a shared global descriptor when requests through different DNS aliases must consume the same quota. Existing-rule scope applies only to the explicitly selected HTTPRoute rule.")
	return finishPlan(p)
}

func normaliseResilienceScope(req Request)Request{
	if req.Resilience==nil{return req};r:=*req.Resilience
	switch r.Scope{case "service":r.Match=nil;r.Route="";r.Rule=0;r.Backend="";case "api":r.Route="";r.Rule=0;case "route":r.Match=nil;r.Backend=""}
	req.Resilience=&r;return req
}

func mergeResilienceRequest(previous *store.Object, req Request) Request {
	var old Request
	if json.Unmarshal([]byte(S(Path(previous.Obj, "metadata", "annotations", "flightbay.dev/resilience-request"))), &old) != nil {
		return req
	}
	old.Cluster, old.Namespace, old.Service, old.Port, old.Edit = req.Cluster, req.Namespace, req.Service, req.Port, nil
	r := *req.Resilience
	if old.Resilience != nil && !contains(req.Tasks, "global-rate-limit") {
		r.Global = old.Resilience.Global
	}
	if old.Resilience != nil && !contains(req.Tasks, "retries") {
		r.RetryOn = old.Resilience.RetryOn
	}
	if old.Resilience != nil && !contains(req.Tasks, "circuit-breaker") && !contains(req.Tasks, "endpoint-failover") {
		r.FailurePercentage = old.Resilience.FailurePercentage
		r.DisableConsecutive = old.Resilience.DisableConsecutive
	}
	old.Resilience = &r
	for _, task := range req.Tasks {
		if !contains(old.Tasks, task) {
			old.Tasks = append(old.Tasks, task)
		}
		switch task {
		case "rate-limit":
			old.Requests, old.Unit, old.Burst = req.Requests, req.Unit, req.Burst
		case "retries":
			old.RetryAttempts, old.Timeout, old.BackendTimeout = req.RetryAttempts, req.Timeout, req.BackendTimeout
		case "timeouts":
			old.Timeout = req.Timeout
		case "circuit-breaker":
			old.CircuitBreaker = req.CircuitBreaker
			fallthrough
		case "endpoint-failover":
			old.FailureThreshold, old.EjectionInterval, old.EjectionDuration, old.MaxEjectionPercent = req.FailureThreshold, req.EjectionInterval, req.EjectionDuration, req.MaxEjectionPercent
		}
	}
	return old
}

func mergeResiliencePatches(before, desired []any) []any {
	out := L(Copy(before))
	indices := map[string]int{}
	key := func(p map[string]any) string {
		value := []any{p["applyTo"], p["match"], Path(p, "patch", "operation")}
		if S(p["applyTo"]) == "HTTP_FILTER" || S(Path(p, "patch", "operation")) == "ADD" || S(Path(p, "patch", "operation")) == "INSERT_FIRST" {
			value = append(value, Path(p, "patch", "value", "name"))
		}
		data, _ := json.Marshal(value)
		return string(data)
	}
	for i, p := range out {
		indices[key(M(p))] = i
	}
	for _, raw := range desired {
		p := M(Copy(raw))
		k := key(p)
		if i, ok := indices[k]; ok {
			target := M(Path(M(out[i]), "patch", "value"))
			mergeResilienceRoute(target, M(Path(p, "patch", "value")))
			if S(Path(p, "patch", "operation")) == "INSERT_FIRST" {
				target["match"] = Copy(Path(p, "patch", "value", "match"))
			}
		} else {
			indices[k] = len(out)
			out = append(out, p)
		}
	}
	return out
}

func mergeResilienceRoute(into, values map[string]any) {
	for key, value := range values {
		if child := M(value); child != nil {
			existing := M(into[key])
			if existing == nil {
				existing = map[string]any{}
				into[key] = existing
			}
			mergeResilienceRoute(existing, child)
		} else {
			into[key] = Copy(value)
		}
	}
}

func envoyResilienceDuration(value time.Duration) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.9f", value.Seconds()), "0"), ".") + "s"
}

func resilienceEnvoyMatch(match RouteMatch) (map[string]any, error) {
	normalized, err := buildRouteMatch(match)
	if err != nil {
		return nil, err
	}
	path := M(normalized["path"])
	out := map[string]any{}
	switch S(path["type"]) {
	case "Exact":
		out["path"] = path["value"]
	case "RegularExpression":
		out["safe_regex"] = map[string]any{"regex": path["value"]}
	default:
		out["path_separated_prefix"] = strings.TrimSuffix(S(path["value"]), "/")
	}
	headers := []any{}
	if method := S(normalized["method"]); method != "" {
		headers = append(headers, map[string]any{"name": ":method", "string_match": map[string]any{"exact": method}})
	}
	for _, v := range L(normalized["headers"]) {
		h := M(v)
		key := "exact"
		var value any = h["value"]
		if S(h["type"]) == "RegularExpression" {
			key = "safe_regex"
			value = map[string]any{"regex": h["value"]}
		}
		headers = append(headers, map[string]any{"name": h["name"], "string_match": map[string]any{key: value}})
	}
	if len(headers) > 0 {
		out["headers"] = headers
	}
	query := []any{}
	for _, v := range L(normalized["queryParams"]) {
		q := M(v)
		key := "exact"
		var value any = q["value"]
		if S(q["type"]) == "RegularExpression" {
			key = "safe_regex"
			value = map[string]any{"regex": q["value"]}
		}
		query = append(query, map[string]any{"name": q["name"], "string_match": map[string]any{key: value}})
	}
	if len(query) > 0 {
		out["query_parameters"] = query
	}
	return out, nil
}
