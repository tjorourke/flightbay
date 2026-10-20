package ambient

import (
	"os"
	"strings"
	"testing"
	"time"
)

// The HTTPRoute in Tom's screenshot: one rule, PathPrefix /, split across two
// backends.
func splitRoute() []RouteRuleSpec {
	return []RouteRuleSpec{{
		Matches:  []RouteMatchSpec{{PathType: "PathPrefix", Path: "/"}},
		Backends: []RouteBackend{{Namespace: "bookinfo", Name: "reviews-v1", Weight: 90}, {Namespace: "bookinfo", Name: "reviews-v2", Weight: 10}},
	}}
}

func TestHealthCaseUnknownNotFail(t *testing.T) {
	c := Case{ID: "r1-m1-sub", Request: CaseRequest{Method: "GET", Path: "/health", Port: 9080}, Expect: CaseExpect{Rule: 1, Backends: []string{"reviews-v1", "reviews-v2"}}}
	s := Suite{Cases: []Case{c}}
	s.Normalize()
	c = s.Cases[0]
	if c.Expect.Text != "rule 1 → split" {
		t.Fatalf("expect text %q", c.Expect.Text)
	}
	rule := MatchRouteRule(splitRoute(), c.Request)
	if rule != 1 {
		t.Fatalf("GET /health under PathPrefix / is rule 1, got %d", rule)
	}
	// The reply carried no pod name and no waypoint line was found.
	r := Evaluate(c, Observation{Status: 200, Rule: rule}, time.Now())
	if r.Status != StatusUnknown {
		t.Fatalf("status %s, want unknown: %+v", r.Status, r)
	}
	if r.Got.Text != "could not tell which backend answered" || r.Got.Rule != 1 || r.Got.Status != 200 {
		t.Errorf("got %+v", r.Got)
	}
	if !strings.Contains(r.Message, "did not name the Pod") || !strings.Contains(r.Message, "matches rule 1") {
		t.Errorf("message %q", r.Message)
	}
	if sum := Summarize([]CaseResult{r}, time.Now()); sum.Unknown != 1 || sum.Failed != 0 || sum.Errored != 0 || !sum.OK() {
		t.Errorf("an unknown case neither passes nor fails: %+v", sum)
	}
	// Once the backend is known it passes, and the rule is the matched one.
	r = Evaluate(c, Observation{Status: 200, Rule: rule, Backend: "reviews-v2"}, time.Now())
	if r.Status != StatusPass || r.Got.Rule != 1 || r.Got.Text != "rule 1 → reviews-v2" {
		t.Errorf("%+v", r)
	}
}

func TestExplainRouteRequest(t *testing.T) {
	rules := []RouteRuleSpec{
		{Matches: []RouteMatchSpec{{PathType: "Exact", Path: "/admin"}}, Backends: []RouteBackend{{Name: "admin", Weight: 1}}},
		{Matches: []RouteMatchSpec{{PathType: "PathPrefix", Path: "/api", Method: "POST", Headers: []RouteHeaderMatch{{Name: "X-User", Value: "alice"}}}}, Backends: []RouteBackend{{Name: "api-v2", Weight: 1}}},
		{Matches: []RouteMatchSpec{{PathType: "RegularExpression", Path: "/items/[0-9]+", Query: []RouteHeaderMatch{{Name: "v", Value: "2"}}}}, Backends: []RouteBackend{{Name: "items", Weight: 1}}},
		{Matches: []RouteMatchSpec{{PathType: "PathPrefix", Path: "/"}}, Backends: []RouteBackend{{Name: "web", Weight: 1}}},
	}
	ref := Ref{Kind: KindHTTPRoute, Namespace: "ns", Name: "r"}
	tests := []struct {
		name string
		req  CaseRequest
		want int
		why  map[int]string
	}{
		{"exact wins", CaseRequest{Method: "GET", Path: "/admin"}, 1, map[int]string{1: "path /admin is exactly /admin", 4: "more specific"}},
		{"prefix rule with method and header", CaseRequest{Method: "POST", Path: "/api/x", Headers: map[string]string{"x-user": "alice"}}, 2, map[int]string{2: "header X-User is alice", 1: "is not exactly /admin"}},
		{"header absent", CaseRequest{Method: "POST", Path: "/api/x"}, 4, map[int]string{2: "header X-User is absent"}},
		{"method differs", CaseRequest{Method: "GET", Path: "/api/x", Headers: map[string]string{"X-User": "alice"}}, 4, map[int]string{2: "method GET is not POST"}},
		{"header value differs", CaseRequest{Method: "POST", Path: "/api", Headers: map[string]string{"X-User": "bob"}}, 4, map[int]string{2: "header X-User is bob, not alice"}},
		{"prefix is segment-wise", CaseRequest{Method: "POST", Path: "/apiary", Headers: map[string]string{"X-User": "alice"}}, 4, map[int]string{2: "does not start with /api"}},
		{"regex and query", CaseRequest{Method: "GET", Path: "/items/42?v=2&x=1"}, 3, map[int]string{3: "matches the regular expression"}},
		{"query absent", CaseRequest{Method: "GET", Path: "/items/42"}, 4, map[int]string{3: "query parameter v is absent"}},
		{"regex is a full match", CaseRequest{Method: "GET", Path: "/items/42/x?v=2"}, 4, map[int]string{3: "does not match the regular expression"}},
	}
	for _, tc := range tests {
		x := ExplainRouteRequest(ref, rules, tc.req)
		if x.MatchedRule != tc.want {
			t.Errorf("%s: matched %d, want %d (%+v)", tc.name, x.MatchedRule, tc.want, x.Rules)
			continue
		}
		for rule, frag := range tc.why {
			if !strings.Contains(x.Rules[rule-1].Reason, frag) {
				t.Errorf("%s: rule %d reason %q, want %q", tc.name, rule, x.Rules[rule-1].Reason, frag)
			}
		}
		if !x.Rules[tc.want-1].Matched {
			t.Errorf("%s: winner not marked matched", tc.name)
		}
	}
	x := ExplainRouteRequest(ref, rules[:1], CaseRequest{Method: "GET", Path: "/"})
	if x.MatchedRule != 0 || !strings.Contains(x.Note, "404") || x.Rules[0].Matches[0] != "Exact /admin" {
		t.Errorf("no match: %+v", x)
	}
	if MatchRouteRule([]RouteRuleSpec{{Matches: []RouteMatchSpec{{PathType: "RegularExpression", Path: "("}}}}, CaseRequest{Path: "/"}) != 0 {
		t.Error("an unreadable expression cannot be decided")
	}
	if got := ExplainRouteRequest(ref, []RouteRuleSpec{{}}, CaseRequest{Path: "/x"}); got.MatchedRule != 1 || !strings.Contains(got.Rules[0].Reason, "no matches") {
		t.Errorf("a rule with no matches takes everything: %+v", got)
	}
}

func applied(name, action, enforced string, spec map[string]any) AppliedPolicy {
	spec["action"] = action
	ref := Ref{Kind: KindAuthorizationPolicy, Group: SecurityGroup, Resource: "authorizationpolicies", Namespace: "bookinfo", Name: name}
	return AppliedPolicy{Policy: ParseAuthzPolicy(ref, spec, enforced), Scope: "Service reviews"}
}

func TestPredictRequestAcrossPolicies(t *testing.T) {
	deny := applied("deny-red", "DENY", "waypoint", map[string]any{"rules": []any{map[string]any{"when": []any{map[string]any{"key": "request.headers[color]", "values": []any{"red"}}}}}})
	allow := applied("reviews-methods", "ALLOW", "waypoint", map[string]any{"rules": []any{map[string]any{"to": []any{map[string]any{"operation": map[string]any{"methods": []any{"GET", "HEAD"}}}}}}})
	mesh := applied("from-bookinfo", "ALLOW", "ztunnel", map[string]any{"rules": []any{map[string]any{"from": []any{map[string]any{"source": map[string]any{"namespaces": []any{"bookinfo"}}}}}}})
	custom := applied("ext", "CUSTOM", "waypoint", map[string]any{"rules": []any{map[string]any{"to": []any{map[string]any{"operation": map[string]any{"paths": []any{"/admin*"}}}}}}})
	caller := Caller{Namespace: "bookinfo", Principal: "cluster.local/ns/bookinfo/sa/demo", Known: true}
	all := []AppliedPolicy{allow, mesh, deny, custom}

	p := PredictRequest(all, CaseRequest{Method: "GET", Path: "/", Headers: map[string]string{"Color": "red"}}, 9080, caller, true, "cluster.local/ns/bookinfo/sa/bookinfo-waypoint")
	if !p.Known || p.Status != 403 || p.By == nil || p.By.Name != "deny-red" || p.Verdict != "Denied by bookinfo/deny-red" {
		t.Fatalf("%+v", p)
	}
	var order []string
	for _, e := range p.Policies {
		order = append(order, e.Action+":"+e.Name)
	}
	if strings.Join(order, ",") != "CUSTOM:ext,DENY:deny-red,ALLOW:from-bookinfo,ALLOW:reviews-methods" {
		t.Errorf("Istio order: %v", order)
	}
	if !p.Policies[1].Matched || !p.Policies[1].Decided || p.Policies[1].Reason != "header color is red" || p.Policies[1].Order != 2 {
		t.Errorf("deny eval %+v", p.Policies[1])
	}
	if p.Policies[0].Matched || !strings.Contains(p.Policies[0].Reason, "path / is not in /admin*") {
		t.Errorf("custom eval %+v", p.Policies[0])
	}

	// No header: the DENY does not match and the ALLOW for GET does.
	p = PredictRequest(all, CaseRequest{Method: "GET", Path: "/"}, 9080, caller, true, "cluster.local/ns/bookinfo/sa/bookinfo-waypoint")
	if !p.Known || p.Status != 200 || p.DecidedBy != "reviews-methods" || p.Policies[1].Reason != "header color is absent" {
		t.Fatalf("%+v", p)
	}
	// ztunnel sees the waypoint's identity, which is in bookinfo.
	if !p.Policies[2].Matched || !strings.Contains(p.Policies[2].Reason, "waypoint's identity") {
		t.Errorf("ztunnel eval %+v", p.Policies[2])
	}
	// POST matches no ALLOW at the waypoint.
	p = PredictRequest(all, CaseRequest{Method: "POST", Path: "/"}, 9080, caller, true, "")
	if !p.Known || p.Status != 403 || p.DecidedBy != "no ALLOW matched" || p.By != nil {
		t.Fatalf("%+v", p)
	}
	// A CUSTOM match is for the external authorizer to answer.
	p = PredictRequest(all, CaseRequest{Method: "GET", Path: "/admin/x"}, 9080, caller, true, "")
	if p.Known || !strings.Contains(p.Verdict, "external authorizer") {
		t.Fatalf("%+v", p)
	}
	// Straight to the Pod: only ztunnel policies, with the caller's identity.
	p = PredictRequest(all, CaseRequest{Method: "POST", Path: "/", Direct: true}, 9080, Caller{Namespace: "other", Principal: "cluster.local/ns/other/sa/x", Known: true}, true, "")
	if !p.Known || p.Status != 0 || p.DecidedBy != "no ALLOW matched" || !strings.Contains(p.Policies[1].Reason, "straight to the Pod") {
		t.Fatalf("%+v", p)
	}
	// Nothing applies.
	p = PredictRequest(nil, CaseRequest{Method: "GET", Path: "/"}, 9080, caller, false, "")
	if !p.Known || p.Status != 200 || p.Verdict != "Allowed: no AuthorizationPolicy applies" || p.DecidedBy != "no policy refused it" {
		t.Fatalf("%+v", p)
	}
	// A DENY with a field the evaluator cannot read makes it unknown.
	odd := applied("odd", "DENY", "waypoint", map[string]any{"rules": []any{map[string]any{"from": []any{map[string]any{"source": map[string]any{"ipBlocks": []any{"10.0.0.0/8"}}}}}}})
	p = PredictRequest([]AppliedPolicy{odd}, CaseRequest{Method: "GET", Path: "/"}, 9080, caller, true, "")
	if p.Known || !strings.Contains(p.Verdict, "ipBlocks") {
		t.Fatalf("%+v", p)
	}
}

func TestCaseBodyAndRequestFields(t *testing.T) {
	base := func(m, body string) Case {
		return Case{ID: "b", Request: CaseRequest{Method: m, Path: "/reviews/0?x=1&y=2", Body: body, Headers: map[string]string{"X-A": "1", "X-B": "2"}, Host: "reviews-v2"}, Expect: CaseExpect{Status: 200}}
	}
	if err := (&Case{ID: "x", Request: CaseRequest{Method: "POST", Path: "/"}, Expect: CaseExpect{Status: 200}}).Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		m, body string
		ok      bool
	}{{"POST", `{"a":1}`, true}, {"PUT", "x", true}, {"PATCH", "x", true}, {"DELETE", "x", true}, {"GET", "x", false}, {"HEAD", "x", false}, {"POST", strings.Repeat("a", MaxBodyBytes+1), false}, {"POST", strings.Repeat("a", MaxBodyBytes), true}} {
		c := base(tc.m, tc.body)
		if err := c.Validate(); (err == nil) != tc.ok {
			t.Errorf("%s %d bytes: %v", tc.m, len(tc.body), err)
		}
	}
	dir := t.TempDir()
	ref, _ := SuiteRef("c1", KindHTTPRoute, "bookinfo", "split")
	c := base("POST", `{"a":1}`)
	c.From = &CaseFrom{Namespace: "bookinfo", ServiceAccount: "productpage"}
	s := &Suite{Target: ref, Cases: []Case{c}}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := SaveSuite(dir, s); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSuite(dir, ref)
	if err != nil || got == nil {
		t.Fatal(err)
	}
	r := got.Cases[0].Request
	if r.Body != `{"a":1}` || r.Path != "/reviews/0?x=1&y=2" || r.Host != "reviews-v2" || len(r.Headers) != 2 || r.Headers["X-B"] != "2" || got.Cases[0].From == nil || got.Cases[0].From.ServiceAccount != "productpage" {
		t.Errorf("round trip lost a field: %+v %+v", r, got.Cases[0].From)
	}
	p, _ := SuitePath(dir, ref)
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), `"body": "{\"a\":1}"`) {
		t.Errorf("body not in the saved file: %s", b)
	}
	// The query string is part of the request the route sees.
	if rr := routeRequest(r); rr.Path != "/reviews/0" || rr.Query["x"] != "1" || rr.Query["y"] != "2" {
		t.Errorf("%+v", rr)
	}
}
