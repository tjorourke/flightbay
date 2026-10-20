package ambient

import (
	"testing"
	"time"
)

func TestServiceSuiteList(t *testing.T) {
	ws := fixture(t)
	deny := object("AuthorizationPolicy", "deny-red", map[string]any{
		"action":     "DENY",
		"targetRefs": []any{map[string]any{"group": "", "kind": "Service", "name": "front"}},
		"rules":      []any{rule("when", when("request.headers[color]", "red"))},
	})
	put(ws, SecurityGroup, "authorizationpolicies", "AuthorizationPolicy", deny)
	route := object("HTTPRoute", "split", map[string]any{
		"parentRefs": []any{map[string]any{"group": "", "kind": "Service", "name": "front", "port": 80}},
		"rules": []any{map[string]any{"matches": []any{map[string]any{"path": map[string]any{"type": "PathPrefix", "value": "/"}}},
			"backendRefs": []any{map[string]any{"name": "stable", "port": 80, "weight": 90}, map[string]any{"name": "canary", "port": 80, "weight": 10}}}},
	})
	put(ws, GatewayGroup, "httproutes", "HTTPRoute", route)
	// An unrelated policy on another Service must not show up.
	other := object("AuthorizationPolicy", "other", map[string]any{"action": "DENY", "targetRefs": []any{map[string]any{"kind": "Service", "name": "stable"}}, "rules": []any{rule()}})
	put(ws, SecurityGroup, "authorizationpolicies", "AuthorizationPolicy", deny, other)

	dir := t.TempDir()
	list, err := ServiceSuiteList(ws, dir, "c", "app", "front")
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Suites) != 2 || list.Suites[0].Kind != KindAuthorizationPolicy || list.Suites[1].Kind != KindHTTPRoute {
		t.Fatalf("%+v", list.Suites)
	}
	a, r := list.Suites[0], list.Suites[1]
	if a.Name != "deny-red" || a.Action != "DENY" || a.Saved || a.Cases < 4 || a.LastRun != nil || a.Status != "" {
		t.Errorf("authz row %+v", a)
	}
	if r.Name != "split" || r.Saved || r.Cases < 3 {
		t.Errorf("route row %+v", r)
	}
	if list.NeverRun != 2 || list.Cases != a.Cases+r.Cases || list.Passed != 0 || list.Failed != 0 {
		t.Errorf("totals %+v", list)
	}

	// A saved suite wins over generating, and its last run is counted.
	g, err := GenerateForRef(ws, a.Ref, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := g.Suite
	s.Cases = s.Cases[:2]
	at := time.Now()
	s.Last = &SuiteRun{At: at, Results: []CaseResult{{ID: s.Cases[0].ID, Status: StatusPass}, {ID: s.Cases[1].ID, Status: StatusFail}}}
	s.Last.Summary = Summarize(s.Last.Results, at)
	if err := SaveSuite(dir, s); err != nil {
		t.Fatal(err)
	}
	list, err = ServiceSuiteList(ws, dir, "c", "app", "front")
	if err != nil {
		t.Fatal(err)
	}
	a = list.Suites[0]
	if !a.Saved || a.Cases != 2 || a.Status != "fail" || a.LastRun == nil || a.LastRun.Passed != 1 {
		t.Errorf("saved row %+v", a)
	}
	if list.Passed != 1 || list.Failed != 1 || list.NeverRun != 1 || list.Cases != 2+list.Suites[1].Cases {
		t.Errorf("totals %+v", list)
	}

	if _, err := ServiceSuiteList(ws, dir, "c", "app", "missing"); err == nil {
		t.Error("unknown Service must error")
	}
	// No state dir still lists generated suites.
	if l, err := ServiceSuiteList(ws, "", "c", "app", "front"); err != nil || len(l.Suites) != 2 {
		t.Errorf("%v %+v", err, l)
	}
}

func TestGenerateForObjectKinds(t *testing.T) {
	ws := fixture(t)
	if _, err := GenerateForObject(ws, "c", map[string]any{"kind": "Gateway"}, nil, nil); err == nil {
		t.Error("only policies and routes have suites")
	}
	// A route attached to the waypoint, not a Service, cannot be tested.
	gw := map[string]any{"kind": "HTTPRoute", "metadata": map[string]any{"name": "gw", "namespace": "app"}, "spec": map[string]any{"parentRefs": []any{map[string]any{"kind": "Gateway", "name": "wp"}}}}
	if _, err := GenerateForObject(ws, "c", gw, nil, nil); err == nil {
		t.Error("gateway-parent route has no Service to test")
	}
	// A draft policy resolves its Service from targetRefs.
	draft := map[string]any{"kind": "AuthorizationPolicy", "metadata": map[string]any{"name": "d", "namespace": "app"},
		"spec": map[string]any{"action": "DENY", "targetRefs": []any{map[string]any{"kind": "Service", "name": "stable"}}, "rules": []any{rule("to", op("methods", list("DELETE")))}}}
	g, err := GenerateForObject(ws, "c", draft, nil, []map[string]any{draft})
	if err != nil {
		t.Fatal(err)
	}
	if g.Suite.Service.Name != "stable" || caseByID(g.Suite, "r1-match") == nil || g.Authz == nil {
		t.Errorf("%+v", g.Suite)
	}
	// A policy that targets nothing findable is an error, not a panic.
	orphan := map[string]any{"kind": "AuthorizationPolicy", "metadata": map[string]any{"name": "o", "namespace": "app"}, "spec": map[string]any{"action": "DENY", "targetRefs": []any{map[string]any{"kind": "Service", "name": "nope"}}}}
	if _, err := GenerateForObject(ws, "c", orphan, nil, nil); err == nil {
		t.Error("orphan policy")
	}
}

func TestDraftsReplaceOthers(t *testing.T) {
	ws := fixture(t)
	allow := object("AuthorizationPolicy", "allow-get", map[string]any{"targetRefs": []any{map[string]any{"kind": "Service", "name": "front"}}, "rules": []any{rule("to", op("methods", list("GET")))}})
	put(ws, SecurityGroup, "authorizationpolicies", "AuthorizationPolicy", allow)
	deny := map[string]any{"kind": "AuthorizationPolicy", "metadata": map[string]any{"name": "deny-red", "namespace": "app"},
		"spec": map[string]any{"action": "DENY", "targetRefs": []any{map[string]any{"kind": "Service", "name": "front"}}, "rules": []any{rule("when", when("request.headers[color]", "red"))}}}
	// A draft of the ALLOW changes the interaction case.
	draftAllow := map[string]any{"kind": "AuthorizationPolicy", "metadata": map[string]any{"name": "allow-get", "namespace": "app"},
		"spec": map[string]any{"targetRefs": []any{map[string]any{"kind": "Service", "name": "front"}}, "rules": []any{rule("to", op("methods", list("GET", "POST")))}}}
	g0, err := GenerateForObject(ws, "c", deny, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c := caseByID(g0.Suite, "x-app-allow-get"); c == nil || c.Request.Method != "POST" {
		t.Fatalf("captured ALLOW GET: want POST, got %+v", c)
	}
	g, err := GenerateForObject(ws, "c", deny, nil, []map[string]any{draftAllow})
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Authz.Others) != 1 || g.Authz.Others[0].Rules[0].To[0].Methods[1] != "POST" {
		t.Fatalf("draft must replace the captured policy: %+v", g.Authz.Others)
	}
	c := caseByID(g.Suite, "x-app-allow-get")
	if c == nil || c.Request.Method != "PUT" || c.Expect.Status != 403 {
		t.Errorf("%+v", c)
	}
}
