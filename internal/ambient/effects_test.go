package ambient

import (
	"reflect"
	"strings"
	"testing"

	"github.com/tjorourke/flightbay/internal/store"
)

func effectReq(task string, r Request) Request {
	r.Cluster, r.Namespace, r.Service, r.Port, r.Tasks = "c", "app", "front", 80, []string{task}
	return r
}

func allowLock() map[string]any {
	return object("AuthorizationPolicy", "front-waypoint", map[string]any{
		"selector": map[string]any{"matchLabels": map[string]any{"app": "front"}},
		"action":   "ALLOW",
		"rules":    []any{map[string]any{"from": []any{map[string]any{"source": map[string]any{"principals": []any{"mesh.example/ns/app/sa/waypoint-sa"}}}}}},
	})
}

func existingAllow() map[string]any {
	return object("AuthorizationPolicy", "front-allow", map[string]any{
		"targetRefs": []any{map[string]any{"group": "", "kind": "Service", "name": "front"}},
		"action":     "ALLOW",
		"rules":      []any{map[string]any{"to": []any{map[string]any{"operation": map[string]any{"methods": []any{"GET"}}}}}},
	})
}

func hasText(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func findInteraction(is []Interaction, sub string) *Interaction {
	for i := range is {
		if strings.Contains(is[i].Text, sub) {
			return &is[i]
		}
	}
	return nil
}

func TestEffectSentences(t *testing.T) {
	cases := []struct {
		name  string
		req   Request
		setup func(ws *store.Workspace)
		want  []string
	}{
		{"methods allow", effectReq("methods", Request{Methods: []string{"GET", "HEAD"}}), nil,
			[]string{"GET and HEAD keep working.", "POST, PUT, PATCH, DELETE return 403 from the waypoint."}},
		{"methods deny", effectReq("methods", Request{MethodAction: "DENY", Methods: []string{"POST", "DELETE"}}), nil,
			[]string{"POST and DELETE return 403 from the waypoint.", "Everything else still works."}},
		{"methods deny one", effectReq("methods", Request{MethodAction: "DENY", Methods: []string{"DELETE"}}), nil,
			[]string{"DELETE returns 403 from the waypoint."}},
		{"weighted", effectReq("weighted-routing", Request{Backends: []Backend{{"stable", 80, 80}, {"canary", 80, 20}}}), nil,
			[]string{"80% of requests go to stable, 20% to canary."}},
		{"weighted relative", effectReq("weighted-routing", Request{RelativeWeights: true, Backends: []Backend{{"stable", 80, 3}, {"canary", 80, 1}}}), nil,
			[]string{"75% of requests go to stable, 25% to canary."}},
		{"equal", effectReq("weighted-routing", Request{TrafficMode: "equal", Backends: []Backend{{"stable", 80, 0}, {"canary", 80, 0}}}), nil,
			[]string{"Requests are shared equally between stable and canary."}},
		{"timeouts", effectReq("timeouts", Request{Timeout: "5s"}), nil,
			[]string{"Requests that take longer than 5s fail with 504."}},
		{"retries", effectReq("retries", Request{RetryAttempts: 3}), nil,
			[]string{"A failed request to front is retried up to 3 times before the caller sees the error."}},
		{"mirroring", effectReq("mirroring", Request{Mirror: &Backend{Name: "canary", Port: 80}}), nil,
			[]string{"Every request to front is also sent to canary."}},
		{"header routing", effectReq("header-routing", Request{Headers: map[string]string{"x-version": "canary"}, Backends: []Backend{{Name: "canary", Port: 80}}}), nil,
			[]string{"Requests with `x-version: canary` go to canary.", "Everything else still goes to front."}},
		{"route match", effectReq("header-routing", Request{RouteMatch: &RouteMatch{Path: "/api", Method: "POST"}, Backends: []Backend{{Name: "canary", Port: 80}}}), nil,
			[]string{"Requests with a path starting `/api` and method is POST go to canary."}},
		{"load balancing", effectReq("load-balancing", Request{Algorithm: "LEAST_REQUEST"}), nil,
			[]string{"Each waypoint sends the next request to the endpoint of front with the fewest active requests."}},
		{"endpoint failover", effectReq("endpoint-failover", Request{FailureThreshold: 5, EjectionDuration: "1m", MaxEjectionPercent: 50}), nil,
			[]string{"An endpoint of front that returns 5 5xx errors in a row is taken out of rotation for 1m.", "At most 50% of the endpoints can be out at once."}},
		{"circuit breaker", effectReq("circuit-breaker", Request{CircuitBreaker: &CircuitBreakerRequest{MaxRequests: 100, MaxPendingRequests: 10}}), nil,
			[]string{"Each waypoint allows 100 concurrent requests and 10 pending requests to front.", "Requests over the limit get 503 straight away instead of queueing."}},
		{"rate limit", effectReq("rate-limit", Request{Requests: 100, Unit: "Seconds"}), nil,
			[]string{"Requests to front above 100 per second are refused with 429."}},
		{"strict", effectReq("strict-mtls", Request{MTLSMode: "STRICT"}), nil,
			[]string{"Callers outside the mesh are refused."}},
		{"permissive", effectReq("strict-mtls", Request{MTLSMode: "PERMISSIVE"}), nil,
			[]string{"front accepts both mTLS and plain-text callers."}},
		{"default deny", effectReq("l4-default-deny", Request{}), nil,
			[]string{"Callers are refused unless another ALLOW policy admits them."}},
		{"require waypoint", effectReq("require-waypoint", Request{}), nil,
			[]string{"Only the waypoint can call the Pods of front.", "Callers that go straight to a pod IP are refused."}},
		{"jwt", effectReq("jwt", Request{Issuer: "https://issuer.example"}), nil,
			[]string{"Requests to front need a valid JWT from https://issuer.example."}},
		{"access deny", effectReq("access-rule", Request{Access: &AccessRequest{Action: "DENY", Rules: []AccessRule{{Headers: []HeaderMatch{{Name: "color", Values: []string{"red"}}}}}}}), nil,
			[]string{"Requests with `color: red` get 403 from the waypoint.", "Everything else still works."}},
		{"access allow", effectReq("access-rule", Request{Access: &AccessRequest{Action: "ALLOW", Rules: []AccessRule{{SourceNamespaces: []string{"shop"}}}}}), nil,
			[]string{"Only callers in `shop` can reach front.", "Everything else gets 403 from the waypoint."}},
		{"global publish", effectReq("global-service", Request{}), nil,
			[]string{"Endpoints of front become reachable from the other connected clusters."}},
		{"global unpublish", effectReq("global-service", Request{Unpublish: true}), nil,
			[]string{"front stops being shared with other clusters."}},
		{"waypoint", effectReq("waypoint", Request{Waypoint: &WaypointRequest{Scope: "namespace", Name: "wp"}}), nil,
			[]string{"Every Service in app sends its traffic through wp."}},
		{"enrol", effectReq("enrol-namespace", Request{}), nil,
			[]string{"Pods in app join the ambient mesh: their traffic gets mTLS from ztunnel."}},
		{"existing DENY runs first", effectReq("methods", Request{Methods: []string{"GET", "HEAD"}}),
			func(ws *store.Workspace) {
				put(ws, "security.istio.io", "authorizationpolicies", "AuthorizationPolicy", denyRed())
			},
			[]string{"GET and HEAD keep working.", "Requests with `color: red` still get 403: your existing DENY runs first."}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws := fixture(t)
			if tc.setup != nil {
				tc.setup(ws)
			}
			got, _ := Effects(ws, tc.req, nil)
			for _, w := range tc.want {
				if !hasText(got, w) {
					t.Errorf("missing effect %q in %q", w, got)
				}
			}
		})
	}
}

func TestMethodsDenyDoesNotListAllowSentence(t *testing.T) {
	got, _ := Effects(fixture(t), effectReq("methods", Request{MethodAction: "DENY", Methods: []string{"POST"}}), nil)
	for _, e := range got {
		if strings.Contains(e, "keep working") {
			t.Fatalf("DENY described as ALLOW: %q", got)
		}
	}
}

func TestInteractions(t *testing.T) {
	get := effectReq("methods", Request{Methods: []string{"GET", "HEAD"}})
	first := "This is the first ALLOW on front. Once it exists, anything it does not match is denied. You do not need \"Deny by default\" as well for L7 traffic."
	bypass := "Callers that reach pod IPs directly skip the waypoint and skip this rule."
	cases := []struct {
		name  string
		req   Request
		plan  *Plan
		setup func(ws *store.Workspace)
		check func(t *testing.T, is []Interaction)
	}{
		{"first ALLOW warns", get, nil, nil, func(t *testing.T, is []Interaction) {
			i := findInteraction(is, "first ALLOW")
			if i == nil || i.Level != "warn" || i.Text != first {
				t.Fatalf("%+v", is)
			}
		}},
		{"a DENY is not an ALLOW", effectReq("methods", Request{MethodAction: "DENY", Methods: []string{"POST"}}), nil, nil, func(t *testing.T, is []Interaction) {
			if findInteraction(is, "first ALLOW") != nil {
				t.Fatalf("%+v", is)
			}
		}},
		{"second ALLOW is not the first", get, nil, func(ws *store.Workspace) {
			put(ws, "security.istio.io", "authorizationpolicies", "AuthorizationPolicy", existingAllow())
		}, func(t *testing.T, is []Interaction) {
			if findInteraction(is, "first ALLOW") != nil {
				t.Fatalf("%+v", is)
			}
		}},
		{"bypass offers require-waypoint", get, nil, withWaypointIdentity, func(t *testing.T, is []Interaction) {
			i := findInteraction(is, bypass)
			if i == nil || i.Level != "info" || i.FollowUpTask != "require-waypoint" || i.FollowUpLabel != `Also add "Block traffic that skips the waypoint"` {
				t.Fatalf("%+v", is)
			}
		}},
		{"bypass without captured identity still points at the task", get, nil, nil, func(t *testing.T, is []Interaction) {
			i := findInteraction(is, bypass)
			if i == nil || i.FollowUpTask != "require-waypoint" {
				t.Fatalf("%+v", is)
			}
		}},
		{"existing waypoint lock silences bypass", get, nil, func(ws *store.Workspace) {
			withWaypointIdentity(ws)
			put(ws, "security.istio.io", "authorizationpolicies", "AuthorizationPolicy", allowLock())
		}, func(t *testing.T, is []Interaction) {
			if findInteraction(is, bypass) != nil {
				t.Fatalf("%+v", is)
			}
		}},
		{"require-waypoint in the same request", effectReq("require-waypoint", Request{}), nil, withWaypointIdentity, func(t *testing.T, is []Interaction) {
			if findInteraction(is, bypass) != nil {
				t.Fatalf("%+v", is)
			}
		}},
		{"service without a waypoint warns", get, nil, func(ws *store.Workspace) {
			delete(M(Path(Target(ws, "c", "Namespace", "", "app").Obj, "metadata", "labels")), "istio.io/use-waypoint")
		}, func(t *testing.T, is []Interaction) {
			i := findInteraction(is, "does not use a waypoint")
			if i == nil || i.Level != "warn" || i.FollowUpTask != "waypoint" {
				t.Fatalf("%+v", is)
			}
		}},
		{"waypoint Gateway missing warns", get, nil, func(ws *store.Workspace) {
			M(Target(ws, "c", "Namespace", "", "app").Obj["metadata"])["labels"] = map[string]any{"istio.io/use-waypoint": "gone"}
		}, func(t *testing.T, is []Interaction) {
			if i := findInteraction(is, "was not captured"); i == nil || i.Level != "warn" {
				t.Fatalf("%+v", is)
			}
		}},
		{"routing overlaps an existing route", effectReq("weighted-routing", Request{Backends: []Backend{{"front", 80, 50}, {"canary", 80, 50}}}),
			&Plan{Objects: []map[string]any{{"kind": "HTTPRoute", "metadata": map[string]any{"name": "front-weighted-routing"}}}},
			func(ws *store.Workspace) {
				put(ws, GatewayGroup, "httproutes", "HTTPRoute", frontRoute(map[string]any{"name": "stable", "port": 80}))
			}, func(t *testing.T, is []Interaction) {
				if i := findInteraction(is, "The route app/split already applies to front"); i == nil || i.Level != "warn" {
					t.Fatalf("%+v", is)
				}
			}},
		{"backend with no ready endpoints warns", effectReq("weighted-routing", Request{Backends: []Backend{{"front", 80, 50}, {"canary", 80, 50}}}), nil, nil, func(t *testing.T, is []Interaction) {
			if i := findInteraction(is, "canary has no ready endpoints"); i == nil || i.Level != "warn" {
				t.Fatalf("%+v", is)
			}
			if findInteraction(is, "front has no ready endpoints") != nil {
				t.Fatalf("front is ready: %+v", is)
			}
		}},
		{"not ready endpoints count as none", effectReq("mirroring", Request{Mirror: &Backend{Name: "front", Port: 80}}), nil, func(ws *store.Workspace) {
			for _, o := range ws.List("c", "discovery.k8s.io", "endpointslices", "app", nil) {
				M(M(L(o.Obj["endpoints"])[0])["conditions"])["ready"] = false
			}
		}, func(t *testing.T, is []Interaction) {
			if findInteraction(is, "front has no ready endpoints") == nil {
				t.Fatalf("%+v", is)
			}
		}},
		{"strict mTLS has no waypoint interactions", effectReq("strict-mtls", Request{MTLSMode: "STRICT"}), nil, nil, func(t *testing.T, is []Interaction) {
			if len(is) != 0 {
				t.Fatalf("%+v", is)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws := fixture(t)
			if tc.setup != nil {
				tc.setup(ws)
			}
			_, is := Effects(ws, tc.req, tc.plan)
			tc.check(t, is)
		})
	}
}

func TestEffectsAddRuleToExistingRoute(t *testing.T) {
	ws := fixture(t)
	req := effectReq("header-routing", Request{Headers: map[string]string{"x": "y"}, Backends: []Backend{{Name: "canary", Port: 80}}})
	plan := &Plan{Changes: []Change{{Ref: Ref{Kind: "HTTPRoute", Name: "split"}, Fields: []FieldChange{{Path: "/spec/rules", Before: []any{1}, After: []any{1, 2}}}}}}
	got, _ := Effects(ws, req, plan)
	if !hasText(got, "Adds rule 2 to split.") {
		t.Fatalf("%q", got)
	}
}

func TestEffectsNeverNil(t *testing.T) {
	e, i := Effects(fixture(t), Request{Cluster: "c", Namespace: "app", Service: "missing", Tasks: []string{"methods"}}, nil)
	if e == nil || i == nil || !reflect.DeepEqual(e, []string{}) {
		t.Fatalf("%#v %#v", e, i)
	}
}

func TestAnnotatePlanSetsFields(t *testing.T) {
	ws := fixture(t)
	req := effectReq("methods", Request{Methods: []string{"GET", "HEAD"}})
	p, err := Generate(ws, req)
	if err != nil {
		t.Fatal(err)
	}
	AnnotatePlan(ws, p)
	if len(p.Effects) != 2 || findInteraction(p.Interactions, "first ALLOW") == nil {
		t.Fatalf("%+v %+v", p.Effects, p.Interactions)
	}
}
