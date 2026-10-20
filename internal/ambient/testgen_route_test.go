package ambient

import (
	"reflect"
	"strings"
	"testing"
)

var routeRef = Ref{Cluster: "c", Group: GatewayGroup, Resource: "httproutes", Kind: KindHTTPRoute, Namespace: "bookinfo", Name: "split"}

func backend(name string, w int) RouteBackend {
	return RouteBackend{Namespace: "bookinfo", Name: name, Port: 9080, Weight: w}
}

func routeIn(rules ...RouteRuleSpec) RouteInput {
	in := RouteInput{Route: routeRef, Service: testSvc, Port: 9080, DefaultPath: "/reviews/0", Rules: rules, Backends: map[string]BackendState{}}
	for _, r := range rules {
		for _, b := range r.Backends {
			in.Backends[b.Namespace+"/"+b.Name] = BackendState{Exists: true, Ready: 1}
		}
	}
	return in
}

func prefix(p string) RouteMatchSpec { return RouteMatchSpec{PathType: "PathPrefix", Path: p} }

func TestGenerateRoute(t *testing.T) {
	split := RouteRuleSpec{Matches: []RouteMatchSpec{prefix("/")}, Backends: []RouteBackend{backend("reviews-v1", 80), backend("reviews-v2", 20)}}
	tests := []struct {
		name  string
		in    RouteInput
		check func(t *testing.T, s *Suite)
		notes []string
	}{
		{
			name: "split rule: match, sub path, weights, negative",
			in:   routeIn(split),
			check: func(t *testing.T, s *Suite) {
				if ids(s) != "r1-m1,r1-m1-sub,r1-weights,not-route" {
					t.Fatalf("ids %s", ids(s))
				}
				m := caseByID(s, "r1-m1")
				if m.Request.Path != "/reviews/0" || m.Expect.Rule != 1 || !reflect.DeepEqual(m.Expect.Backends, []string{"reviews-v1", "reviews-v2"}) || m.Expect.Text != "rule 1 → split" {
					t.Errorf("%+v", m)
				}
				w := caseByID(s, "r1-weights")
				if !reflect.DeepEqual(w.Expect.Weights, map[string]int{"reviews-v1": 80, "reviews-v2": 20}) || w.Expect.Samples != 200 || w.Expect.Tolerance != 5 {
					t.Errorf("%+v", w.Expect)
				}
				n := caseByID(s, "not-route")
				if n.Request.Host != "reviews-v1" || !n.Expect.NotRoute || n.Expect.Backend != "reviews-v1" || n.Expect.Text != "not this route" {
					t.Errorf("%+v", n)
				}
			},
		},
		{
			name: "single backend rule expects that backend, no weights case",
			in:   routeIn(RouteRuleSpec{Matches: []RouteMatchSpec{{PathType: "Exact", Path: "/only"}}, Backends: []RouteBackend{backend("reviews-v3", 1)}}),
			check: func(t *testing.T, s *Suite) {
				if caseByID(s, "r1-weights") != nil {
					t.Error("no weights case for one backend")
				}
				m := caseByID(s, "r1-m1")
				if m.Request.Path != "/only" || m.Expect.Backend != "reviews-v3" || m.Expect.Text != "rule 1 → reviews-v3" {
					t.Errorf("%+v", m)
				}
				if caseByID(s, "r1-m1-sub") != nil {
					t.Error("exact match has no sub path case")
				}
			},
		},
		{
			name: "header, method and query matches build the request",
			in: routeIn(RouteRuleSpec{Matches: []RouteMatchSpec{{PathType: "PathPrefix", Path: "/api", Method: "POST",
				Headers: []RouteHeaderMatch{{Name: "x-canary", Value: "1"}}, Query: []RouteHeaderMatch{{Name: "v", Value: "2"}}}},
				Backends: []RouteBackend{backend("reviews-v2", 1)}}),
			check: func(t *testing.T, s *Suite) {
				m := caseByID(s, "r1-m1")
				if m.Request.Method != "POST" || m.Request.Path != "/api?v=2" || m.Request.Headers["x-canary"] != "1" {
					t.Errorf("%+v", m.Request)
				}
			},
		},
		{
			name: "more specific later rule wins its own request; earlier broad rule shadows nothing it should not",
			in: routeIn(
				RouteRuleSpec{Matches: []RouteMatchSpec{prefix("/")}, Backends: []RouteBackend{backend("reviews-v1", 1)}},
				RouteRuleSpec{Matches: []RouteMatchSpec{{PathType: "Exact", Path: "/special"}}, Backends: []RouteBackend{backend("reviews-v2", 1)}}),
			check: func(t *testing.T, s *Suite) {
				if m := caseByID(s, "r2-m1"); m == nil || m.Expect.Rule != 2 || m.Expect.Backend != "reviews-v2" {
					t.Errorf("exact rule 2 should win /special: %+v", m)
				}
				if m := caseByID(s, "r1-m1"); m == nil || m.Expect.Rule != 1 {
					t.Errorf("%+v", m)
				}
			},
		},
		{
			name: "an identical later match is never reached: one case, and a note",
			in: routeIn(
				RouteRuleSpec{Matches: []RouteMatchSpec{prefix("/api")}, Backends: []RouteBackend{backend("reviews-v1", 1)}},
				RouteRuleSpec{Matches: []RouteMatchSpec{prefix("/api")}, Backends: []RouteBackend{backend("reviews-v2", 1)}}),
			check: func(t *testing.T, s *Suite) {
				if caseByID(s, "r2-m1") != nil {
					t.Errorf("the same request is not sent twice: %s", ids(s))
				}
				if m := caseByID(s, "r1-m1"); m == nil || m.Expect.Rule != 1 {
					t.Errorf("%+v", m)
				}
			},
			notes: []string{"Rule 2, match 1 (PathPrefix /api) is never reached"},
		},
		{
			name: "regex path match is not generated",
			in:   routeIn(RouteRuleSpec{Matches: []RouteMatchSpec{{PathType: "RegularExpression", Path: "/a.*"}}, Backends: []RouteBackend{backend("reviews-v1", 1)}}),
			check: func(t *testing.T, s *Suite) {
				if len(s.Cases) != 0 {
					t.Errorf("cases %s", ids(s))
				}
			},
			notes: []string{"Not generated: rules[1].matches[1].path RegularExpression"},
		},
		{
			name: "timeout, retry and mirror are notes, not cases",
			in: routeIn(RouteRuleSpec{Matches: []RouteMatchSpec{prefix("/")}, Backends: []RouteBackend{backend("reviews-v1", 1)},
				Timeout: "2s", Retry: true, Filters: []string{"requestMirror", "urlRewrite"}}),
			check: func(t *testing.T, s *Suite) {
				if len(s.Cases) == 0 {
					t.Fatal("match case still generated")
				}
			},
			notes: []string{"Not generated: rules[1].timeouts", "Not generated: rules[1].retry", "Not generated: rules[1].filters.requestMirror", "Not generated: rules[1].filters.urlRewrite"},
		},
		{
			name: "rule with only zero weights has nothing to expect",
			in:   routeIn(RouteRuleSpec{Matches: []RouteMatchSpec{prefix("/")}, Backends: []RouteBackend{backend("reviews-v1", 0)}}),
			check: func(t *testing.T, s *Suite) {
				if len(s.Cases) != 0 {
					t.Errorf("cases %s", ids(s))
				}
			},
			notes: []string{"Not generated: rule 1 has no backend"},
		},
		{
			name: "no matches means match everything",
			in:   routeIn(RouteRuleSpec{Backends: []RouteBackend{backend("reviews-v1", 1)}}),
			check: func(t *testing.T, s *Suite) {
				if caseByID(s, "r1-m1") == nil {
					t.Errorf("ids %s", ids(s))
				}
			},
		},
		{
			name: "negative case is skipped when the backend is the parent Service",
			in:   routeIn(RouteRuleSpec{Matches: []RouteMatchSpec{prefix("/")}, Backends: []RouteBackend{backend("reviews", 1)}}),
			check: func(t *testing.T, s *Suite) {
				if caseByID(s, "not-route") != nil {
					t.Error("negative case would address the parent")
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := GenerateRouteSuite(tc.in)
			tc.check(t, s)
			for _, n := range tc.notes {
				found := false
				for _, got := range s.Notes {
					found = found || strings.Contains(got, n)
				}
				if !found {
					t.Errorf("missing note %q in %v", n, s.Notes)
				}
			}
			if len(s.Cases) > 0 {
				if err := s.Validate(); err != nil {
					t.Errorf("invalid: %v", err)
				}
			}
		})
	}
}

func TestPercentWeights(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []RouteBackend
		want map[string]int
	}{
		{"80/20", []RouteBackend{backend("a", 80), backend("b", 20)}, map[string]int{"a": 80, "b": 20}},
		{"relative", []RouteBackend{backend("a", 3), backend("b", 1)}, map[string]int{"a": 75, "b": 25}},
		{"equal thirds sum to 100", []RouteBackend{backend("a", 1), backend("b", 1), backend("c", 1)}, map[string]int{"a": 34, "b": 33, "c": 33}},
		{"zero weight kept", []RouteBackend{backend("a", 1), backend("b", 0)}, map[string]int{"a": 100, "b": 0}},
		{"all zero", []RouteBackend{backend("a", 0)}, map[string]int{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := percentWeights(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestRouteWinnerPrecedence(t *testing.T) {
	rules := []RouteRuleSpec{
		{Matches: []RouteMatchSpec{prefix("/")}},
		{Matches: []RouteMatchSpec{prefix("/api")}},
		{Matches: []RouteMatchSpec{{PathType: "PathPrefix", Path: "/api", Method: "POST"}}},
		{Matches: []RouteMatchSpec{{PathType: "Exact", Path: "/api"}}},
		{Matches: []RouteMatchSpec{{PathType: "PathPrefix", Path: "/api", Headers: []RouteHeaderMatch{{Name: "X", Value: "1"}}}}},
	}
	req := func(m, p string, h map[string]string) routeReq {
		return routeReq{Method: m, Path: p, Headers: h, Query: map[string]string{}}
	}
	for _, tc := range []struct {
		name string
		r    routeReq
		want int
	}{
		{"root", req("GET", "/other", nil), 0},
		{"longest prefix", req("GET", "/api/x", nil), 1},
		{"exact beats prefix", req("GET", "/api", nil), 3},
		{"method beats plain prefix", req("POST", "/api/x", nil), 2},
		{"header beats plain prefix", req("GET", "/api/x", map[string]string{"x": "1"}), 4},
		{"prefix is per segment", req("GET", "/apix", nil), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, known := routeWinner(rules, tc.r)
			if !known || got != tc.want {
				t.Errorf("got %d known=%v want %d", got, known, tc.want)
			}
		})
	}
	if _, known := routeWinner([]RouteRuleSpec{{Matches: []RouteMatchSpec{{PathType: "RegularExpression", Path: "/x"}}}}, req("GET", "/x", nil)); known {
		t.Error("regex must be undecidable")
	}
}

func TestRouteChecks(t *testing.T) {
	find := func(cs []SuiteCheck, id string) *SuiteCheck {
		for i := range cs {
			if cs[i].ID == id {
				return &cs[i]
			}
		}
		return nil
	}
	split := RouteRuleSpec{Matches: []RouteMatchSpec{prefix("/")}, Backends: []RouteBackend{backend("reviews-v1", 80), backend("reviews-v2", 20), backend("reviews-v3", 0)}}

	t.Run("all backends ready, no overlap, route missing elsewhere", func(t *testing.T) {
		in := routeIn(split)
		in.Clusters = []ClusterPresence{{"kind-mesh1", true, false}, {"kind-mesh3", false, false}, {"kind-mesh4", true, true}}
		cs := RouteChecks(in)
		if c := find(cs, "backends"); c.Status != "pass" || c.Count != "3 / 3" {
			t.Errorf("%+v", c)
		}
		if c := find(cs, "overlap"); c.Status != "pass" || c.Title != "Only one route targets reviews :9080, no overlap" {
			t.Errorf("%+v", c)
		}
		if c := find(cs, "cluster-kind-mesh1"); c == nil || c.Status != "warn" || c.Action == nil || c.Action.Cluster != "kind-mesh1" || c.Action.Kind != "copy-to-cluster" {
			t.Errorf("%+v", c)
		}
		if find(cs, "cluster-kind-mesh3") != nil {
			t.Error("a cluster without the Service is not listed")
		}
		if c := find(cs, "cluster-kind-mesh4"); c == nil || c.Status != "pass" {
			t.Errorf("%+v", c)
		}
		if c := find(cs, "resilience"); c == nil || c.Status != "info" || c.Action == nil {
			t.Errorf("%+v", c)
		}
	})
	t.Run("missing and unready backends", func(t *testing.T) {
		in := routeIn(split)
		in.Backends["bookinfo/reviews-v2"] = BackendState{Exists: true, Ready: 0}
		in.Backends["bookinfo/reviews-v3"] = BackendState{}
		c := find(RouteChecks(in), "backends")
		if c.Status != "warn" || c.Count != "1 / 3" || !strings.Contains(c.Detail, "reviews-v2 has no ready endpoints") || !strings.Contains(c.Detail, "reviews-v3 does not exist") {
			t.Errorf("%+v", c)
		}
	})
	t.Run("overlapping route", func(t *testing.T) {
		in := routeIn(split)
		in.Others = []OtherRoute{{Ref: Ref{Name: "other"}, Rules: []RouteRuleSpec{{Matches: []RouteMatchSpec{prefix("/reviews")}}}}}
		c := find(RouteChecks(in), "overlap")
		if c.Status != "warn" || !strings.Contains(c.Detail, "other also matches") {
			t.Errorf("%+v", c)
		}
	})
	t.Run("disjoint routes do not overlap", func(t *testing.T) {
		in := routeIn(RouteRuleSpec{Matches: []RouteMatchSpec{{PathType: "Exact", Path: "/a"}}, Backends: []RouteBackend{backend("reviews-v1", 1)}})
		in.Others = []OtherRoute{{Ref: Ref{Name: "other"}, Rules: []RouteRuleSpec{{Matches: []RouteMatchSpec{{PathType: "Exact", Path: "/b"}}}}}}
		if c := find(RouteChecks(in), "overlap"); c.Status != "pass" {
			t.Errorf("%+v", c)
		}
	})
	t.Run("timeout configured", func(t *testing.T) {
		r := split
		r.Timeout = "2s"
		c := find(RouteChecks(routeIn(r)), "resilience")
		if c.Action != nil || !strings.Contains(c.Title, "configured") {
			t.Errorf("%+v", c)
		}
	})
}

func TestMatchesOverlap(t *testing.T) {
	for _, tc := range []struct {
		name string
		a, b RouteMatchSpec
		want bool
	}{
		{"same prefix", prefix("/a"), prefix("/a"), true},
		{"nested prefix", prefix("/a"), prefix("/a/b"), true},
		{"disjoint prefix", prefix("/a"), prefix("/b"), false},
		{"exact inside prefix", RouteMatchSpec{PathType: "Exact", Path: "/a/b"}, prefix("/a"), true},
		{"different methods", RouteMatchSpec{Path: "/", Method: "GET"}, RouteMatchSpec{Path: "/", Method: "POST"}, false},
		{"different header values", RouteMatchSpec{Path: "/", Headers: []RouteHeaderMatch{{Name: "x", Value: "1"}}}, RouteMatchSpec{Path: "/", Headers: []RouteHeaderMatch{{Name: "X", Value: "2"}}}, false},
		{"regex is assumed to overlap", RouteMatchSpec{PathType: "RegularExpression", Path: "/x"}, prefix("/y"), true},
	} {
		if got := matchesOverlap(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: got %v", tc.name, got)
		}
	}
}

func TestExplainRoute(t *testing.T) {
	in := routeIn(
		RouteRuleSpec{Matches: []RouteMatchSpec{prefix("/")}, Backends: []RouteBackend{backend("reviews-v1", 80), backend("reviews-v2", 20), backend("reviews-v3", 0)}, Timeout: "3s"},
		RouteRuleSpec{Matches: []RouteMatchSpec{{PathType: "Exact", Path: "/x"}, {PathType: "Exact", Path: "/y", Method: "POST"}}, Backends: []RouteBackend{backend("reviews-v3", 1)}},
	)
	e := ExplainRoute(in, []string{"n"})
	want := "Rule 1: requests matching PathPrefix / go to reviews-v1 (80%) and reviews-v2 (20%). Rule 2: requests matching Exact /x or Exact /y POST go to reviews-v3."
	if e.InWords != want {
		t.Errorf("\n got %q\nwant %q", e.InWords, want)
	}
	if len(e.Rules) != 2 || e.Rules[0].Timeout != "3s" || len(e.Rules[0].Backends) != 3 || e.Rules[0].Backends[0].Percent != 80 || e.Rules[1].Matches[1] != "Exact /y POST" || len(e.Notes) != 1 {
		t.Errorf("%+v", e)
	}
}
