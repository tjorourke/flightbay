package ambient

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tjorourke/flightbay/internal/store"
)

func enterpriseResilienceFixture(t *testing.T) *store.Workspace {
	ws := fixture(t)
	put(ws, "networking.istio.io", "envoyfilters", "EnvoyFilter")
	put(ws, "admin.solo.io", "segments", "Segment")
	p := object("Pod", "pilot", map[string]any{"containers": []any{map[string]any{"image": "us-docker.pkg.dev/soloio-img/istio/pilot:1.30.4-solo"}}})
	put(ws, "", "pods", "Pod", p)
	return ws
}

func TestResilienceAPIBoundariesAndStableUpdates(t *testing.T) {
	ws := enterpriseResilienceFixture(t)
	for _, task := range []string{"rate-limit", "retries", "circuit-breaker"} {
		req := Request{Cluster: "c", Namespace: "app", Service: "front", Port: 80, Tasks: []string{task}, Requests: 5, Unit: "Minutes", RetryAttempts: 2, Timeout: "5s", BackendTimeout: "1s", CircuitBreaker: &CircuitBreakerRequest{MaxConnections: 5, MaxPendingRequests: 6, MaxRequests: 7, MaxRetries: 2, ConnectTimeout: "1s"}, Resilience: &ResilienceRequest{Scope: "api", Match: &RouteMatch{PathType: "Exact", Path: "/api/one", Method: "GET"}}}
		p, err := Generate(ws, req)
		if err != nil {
			t.Fatalf("%s: %v", task, err)
		}
		if !strings.Contains(p.YAML, "/api/one") || !strings.Contains(p.YAML, "HTTPRoute") {
			t.Fatalf("API boundary missing: %s", p.YAML)
		}
		for _, o := range p.Objects {
			if S(o["kind"]) == "HTTPRoute" {
				rules := L(Path(o, "spec", "rules"))
				if len(rules) != 2 || S(Path(M(L(M(rules[1])["backendRefs"])[0]), "name")) != "front" {
					t.Fatal("unmatched APIs would lose their default route")
				}
			}
		}
		if task == "circuit-breaker" {
			if !strings.Contains(p.YAML, "-backend") || !strings.Contains(p.YAML, "connectionPool") {
				t.Fatal("circuit breaker did not isolate backend")
			}
		}
		if task == "retries" && !strings.Contains(p.YAML, "per_try_timeout: 1s") {
			t.Fatal("retry budget missing")
		}
		if task == "rate-limit" {
			for _, o := range p.Objects {
				resource := "envoyfilters"
				group := "networking.istio.io"
				if S(o["kind"]) == "HTTPRoute" {
					resource = "httproutes"
					group = GatewayGroup
				}
				put(ws, group, resource, S(o["kind"]), o)
			}
			req.Requests = 10
			updated, err := Generate(ws, req)
			if err != nil || len(updated.Objects) != 0 || len(updated.Changes) == 0 {
				t.Fatalf("updating quota creates duplicate policy: %+v %v", updated, err)
			}
		}
	}
}

func TestResilienceRejectsUnprovenWaypointAndUnattachedRoute(t *testing.T) {
	req := Request{Cluster: "c", Namespace: "app", Service: "front", Port: 80, Tasks: []string{"rate-limit"}, Requests: 1, Unit: "Seconds", Resilience: &ResilienceRequest{Scope: "service"}}
	if _, err := Generate(fixture(t), req); err == nil {
		t.Fatal("upstream waypoint extension assumed supported")
	}
	ws := enterpriseResilienceFixture(t)
	addRoute(ws, map[string]any{"kind": "Service", "group": "", "name": "stable"}, nil)
	req.Resilience = &ResilienceRequest{Scope: "route", Route: "r"}
	if _, err := Generate(ws, req); err == nil {
		t.Fatal("route that only references a backend was accepted as a frontend")
	}
}

func TestGlobalLimiterUsesCapturedPublishedBackendWithoutProvisioning(t *testing.T) {
	ws := enterpriseResilienceFixture(t)
	alias := object("ServiceEntry", "autogen.remote.rls", map[string]any{"hosts": []any{"rls.shared.mesh.internal"}, "ports": []any{map[string]any{"number": 8081, "protocol": "GRPC"}}})
	put(ws, "networking.istio.io", "serviceentries", "ServiceEntry", alias)
	req := Request{Cluster: "c", Namespace: "app", Service: "front", Port: 80, Tasks: []string{"global-rate-limit"}, Resilience: &ResilienceRequest{Scope: "api", Match: &RouteMatch{PathType: "Exact", Path: "/api/limited"}, Global: &GlobalLimiter{Mesh: true, MeshDomain: "mesh.internal", Namespace: "shared", Service: "rls", Port: 8081, Domain: "customer", DescriptorKey: "api", DescriptorValue: "limited"}}}
	p, err := Generate(ws, req)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range p.Objects {
		if !contains([]string{"HTTPRoute", "EnvoyFilter"}, S(o["kind"])) {
			t.Fatalf("global task must not provision infrastructure: %s", o["kind"])
		}
	}
	if !strings.Contains(p.YAML, "inbound-vip|8081|http|rls.shared.mesh.internal") || strings.Contains(p.YAML, "STRICT_DNS") {
		t.Fatal("published limiter did not use the delivered Istio upstream")
	}
	if !strings.Contains(strings.Join(p.Notes, " "), "Secret references") {
		t.Fatal("Redis/authentication setup prerequisite was hidden")
	}
	req.Resilience.Global.MeshDomain = "not-captured.internal"
	if _, err := Generate(ws, req); err == nil {
		t.Fatal("uncaptured mesh backend accepted")
	}
}

func TestResilienceAlsoCoversPublishedFrontendWithoutBroadeningMatches(t *testing.T) {
	ws := enterpriseResilienceFixture(t)
	alias := object("ServiceEntry", "autogen.app.front", map[string]any{"hosts": []any{"front.app.mesh.internal"}})
	M(alias["metadata"])["labels"] = map[string]any{"solo.io/parent-service": "front", "solo.io/parent-service-namespace": "app", "solo.io/service-scope": "global"}
	put(ws, "networking.istio.io", "serviceentries", "ServiceEntry", alias)
	req := Request{Cluster: "c", Namespace: "app", Service: "front", Port: 80, Tasks: []string{"rate-limit"}, Requests: 2, Unit: "Seconds", Resilience: &ResilienceRequest{Scope: "api", Match: &RouteMatch{PathType: "Exact", Path: "/api/one", Method: "GET"}}}
	p, err := Generate(ws, req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.YAML, "inbound-vip|80|http|front.app.mesh.internal") || !strings.Contains(p.YAML, "INSERT_FIRST") || !strings.Contains(p.YAML, ".global") {
		t.Fatal("published frontend bypasses API policy")
	}
	configured := false
	for _, o := range p.Objects {
		for _, patch := range L(Path(o, "spec", "configPatches")) {
			q := M(patch)
			if S(Path(q, "patch", "operation")) == "INSERT_FIRST" {
				configured = len(M(Path(q, "patch", "value", "typed_per_filter_config"))) > 0
			}
		}
	}
	if !configured {
		t.Fatal("inserted alias route must carry its policy directly; a subsequent MERGE cannot find it during Istio patch ordering")
	}
	match, err := resilienceEnvoyMatch(*req.Resilience.Match)
	if err != nil || S(match["path"]) != "/api/one" || len(L(match["headers"])) != 1 {
		t.Fatalf("API match changed: %+v %v", match, err)
	}
}

func TestResilienceControlsComposeOnOneAPIRoute(t *testing.T) {
	ws := enterpriseResilienceFixture(t)
	req := Request{Cluster: "c", Namespace: "app", Service: "front", Port: 80, Tasks: []string{"rate-limit", "retries", "circuit-breaker"}, Requests: 2, Unit: "Minutes", RetryAttempts: 2, Timeout: "5s", BackendTimeout: "1s", CircuitBreaker: &CircuitBreakerRequest{MaxConnections: 5, MaxPendingRequests: 6, MaxRequests: 7, MaxRetries: 2, ConnectTimeout: "1s"}, Resilience: &ResilienceRequest{Scope: "api", Match: &RouteMatch{PathType: "Exact", Path: "/api/one"}}}
	plan, err := Generate(ws, req)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, o := range plan.Objects {
		kinds[S(o["kind"])]++
		stored := S(Path(o, "metadata", "annotations", "flightbay.dev/resilience-request"))
		var decoded any
		_ = json.Unmarshal([]byte(stored), &decoded)
		canonical, _ := json.Marshal(decoded)
		if stored != string(canonical) {
			t.Fatal("capture canonicalisation would make annotation preconditions fail")
		}
	}
	if kinds["HTTPRoute"] != 1 || kinds["EnvoyFilter"] != 1 || kinds["Service"] != 1 || kinds["DestinationRule"] != 1 {
		t.Fatalf("competing API resources generated: %v", kinds)
	}
	if !strings.Contains(plan.YAML, "retry_policy") || !strings.Contains(plan.YAML, "local_ratelimit") || !strings.Contains(plan.YAML, "connectionPool") {
		t.Fatal("a selected control disappeared")
	}
	// Add controls independently, retaining their original route and filter.
	var routeName string
	for _, task := range []string{"rate-limit", "circuit-breaker", "retries"} {
		req.Tasks = []string{task}
		p, err := generate(ws, req)
		if err != nil {
			t.Fatal(task, err)
		}
		for _, o := range p.Objects {
			kind := S(o["kind"])
			if kind == "HTTPRoute" {
				name := S(Path(o, "metadata", "name"))
				if routeName != "" && routeName != name {
					t.Fatal("adding a control created a competing HTTPRoute")
				}
				routeName = name
			}
			group, res := "networking.istio.io", "envoyfilters"
			switch kind {
			case "HTTPRoute":
				group, res = GatewayGroup, "httproutes"
			case "Service":
				group, res = "", "services"
			case "DestinationRule":
				res = "destinationrules"
			}
			put(ws, group, res, kind, o)
		}
		if task == "retries" && (!strings.Contains(p.YAML, "local_ratelimit") || !strings.Contains(p.YAML, "-backend") || !strings.Contains(p.YAML, "retry_policy")) {
			t.Fatal("adding retries bypassed an earlier rate limit or circuit alias")
		}
	}
}

func TestServiceScopeDropsUnusedPathAndRouteBookkeeping(t *testing.T) {
	ws := enterpriseResilienceFixture(t)
	req := Request{Cluster: "c", Namespace: "app", Service: "front", Port: 80, Tasks: []string{"rate-limit"}, Requests: 10, Unit: "Seconds", Resilience: &ResilienceRequest{Scope: "service", Backend: "front", Route: "unused", Match: &RouteMatch{PathType: "PathPrefix", Path: "/api"}}}
	a, err := Generate(ws, req)
	if err != nil {
		t.Fatal(err)
	}
	if a.Request.Resilience.Match != nil || a.Request.Resilience.Route != "" || a.Request.Resilience.Backend != "" {
		t.Fatal("whole-Service metadata still contains unrelated API/route fields")
	}
	req.Resilience = &ResilienceRequest{Scope: "service"}
	b, err := Generate(ws, req)
	if err != nil {
		t.Fatal(err)
	}
	if S(Path(a.Objects[0], "metadata", "name")) != S(Path(b.Objects[0], "metadata", "name")) {
		t.Fatal("stale form fields create a duplicate whole-Service limiter")
	}
}
