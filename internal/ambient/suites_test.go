package ambient

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseSuiteRef(t *testing.T) {
	tests := []struct {
		in      string
		want    Ref
		wantErr bool
	}{
		{"kind-mesh2/AuthorizationPolicy/bookinfo/deny", Ref{Cluster: "kind-mesh2", Group: SecurityGroup, Resource: "authorizationpolicies", Kind: "AuthorizationPolicy", Namespace: "bookinfo", Name: "deny"}, false},
		{"kind-mesh2/HTTPRoute/bookinfo/split", Ref{Cluster: "kind-mesh2", Group: GatewayGroup, Resource: "httproutes", Kind: "HTTPRoute", Namespace: "bookinfo", Name: "split"}, false},
		{"arn:aws:eks/us/eu/HTTPRoute/ns/n", Ref{Cluster: "arn:aws:eks/us/eu", Group: GatewayGroup, Resource: "httproutes", Kind: "HTTPRoute", Namespace: "ns", Name: "n"}, false},
		{"c/Service/ns/n", Ref{}, true},
		{"c/HTTPRoute/ns", Ref{}, true},
		{"", Ref{}, true},
		{"c/HTTPRoute//n", Ref{}, true},
	}
	for _, tc := range tests {
		got, err := ParseSuiteRef(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("%q: err %v", tc.in, err)
			continue
		}
		if !tc.wantErr && got != tc.want {
			t.Errorf("%q: got %+v", tc.in, got)
		}
		if !tc.wantErr && got.SuiteString() != tc.in {
			t.Errorf("round trip %q -> %q", tc.in, got.SuiteString())
		}
	}
}

func TestSuitePath(t *testing.T) {
	dir := t.TempDir()
	ref, _ := ParseSuiteRef("kind-mesh2/AuthorizationPolicy/bookinfo/deny")
	p, err := SuitePath(dir, ref)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "ambient-suites", "kind-mesh2", "security.istio.io", "AuthorizationPolicy", "bookinfo", "deny.json")
	if p != want {
		t.Errorf("got %s\nwant %s", p, want)
	}
	// Hostile names stay inside the directory.
	for _, name := range []string{"../../etc/passwd", "..", "a/b", ".hidden", "x\x00y", "a..b"} {
		r := ref
		r.Name = name
		p, err := SuitePath(dir, r)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(filepath.Join(dir, "ambient-suites"), p)
		if err != nil || strings.HasPrefix(rel, "..") || strings.Count(rel, string(filepath.Separator)) != 4 {
			t.Errorf("name %q escaped: %s", name, p)
		}
	}
	r := ref
	r.Cluster = "gke_proj/zone:cluster"
	p, _ = SuitePath(dir, r)
	if rel, _ := filepath.Rel(dir, p); strings.Count(rel, string(filepath.Separator)) != 5 {
		t.Errorf("cluster with slash must stay one segment: %s", rel)
	}
	if _, err := SuitePath("", ref); err == nil {
		t.Error("empty dir")
	}
	// Distinct names never collide.
	a, b := ref, ref
	a.Name, b.Name = "a/b", "a%2Fb"
	pa, _ := SuitePath(dir, a)
	pb, _ := SuitePath(dir, b)
	if pa == pb {
		t.Errorf("collision %s", pa)
	}
}

func sampleSuite() *Suite {
	ref, _ := ParseSuiteRef("c/AuthorizationPolicy/bookinfo/deny")
	return &Suite{Target: ref, Source: SuiteSource{Kind: SourceWorkload, Ref: Ref{Cluster: "c", Kind: "Pod", Namespace: "bookinfo", Name: "client"}},
		Cases: []Case{{ID: "r1-match", Label: "l", Generated: true, Why: "Rule matches", Request: CaseRequest{Method: "get", Path: "/x", Headers: map[string]string{"color": "red"}, Port: 9080}, Expect: CaseExpect{Status: 403}}}}
}

func TestSaveLoadSuite(t *testing.T) {
	dir := t.TempDir()
	s := sampleSuite()
	if got, err := LoadSuite(dir, s.Target); err != nil || got != nil {
		t.Fatalf("nothing saved yet: %v %v", got, err)
	}
	at := time.Date(2026, 10, 20, 11, 52, 9, 0, time.UTC)
	s.Last = &SuiteRun{At: at, Results: []CaseResult{{ID: "r1-match", Status: StatusPass, Got: CaseGot{Status: 403}, DecidedBy: "this", At: at}}}
	s.Last.Summary = Summarize(s.Last.Results, at)
	if err := SaveSuite(dir, s); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSuite(dir, s.Target)
	if err != nil || got == nil {
		t.Fatalf("load: %v %v", got, err)
	}
	if !got.Saved || got.Cases[0].Request.Method != "GET" || got.Cases[0].Expect.Text != "403" || got.Last == nil || got.Last.Summary.Passed != 1 || got.Last.Results[0].Got.Text != "403" {
		t.Errorf("%+v", got)
	}
	if !reflect.DeepEqual(got.Source, s.Source) || got.Target != s.Target {
		t.Errorf("source/target changed")
	}
	// No temp files are left next to the suite.
	p, _ := SuitePath(dir, s.Target)
	ents, _ := os.ReadDir(filepath.Dir(p))
	if len(ents) != 1 {
		t.Errorf("leftovers: %v", ents)
	}
	// A corrupt file is an error, not an empty suite.
	if err := os.WriteFile(p, []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSuite(dir, s.Target); err == nil {
		t.Error("corrupt suite must error")
	}
}

func TestValidateSuite(t *testing.T) {
	mut := func(f func(s *Suite)) *Suite { s := sampleSuite(); f(s); return s }
	tests := []struct {
		name string
		s    *Suite
		err  string
	}{
		{"ok", sampleSuite(), ""},
		{"bad kind", mut(func(s *Suite) { s.Target.Kind = "Service" }), "AuthorizationPolicy and HTTPRoute"},
		{"bad source", mut(func(s *Suite) { s.Source.Kind = "laptop" }), "source kind"},
		{"empty id", mut(func(s *Suite) { s.Cases[0].ID = "" }), "no id"},
		{"duplicate id", mut(func(s *Suite) { s.Cases = append(s.Cases, s.Cases[0]) }), "duplicate"},
		{"bad method", mut(func(s *Suite) { s.Cases[0].Request.Method = "BREW" }), "method"},
		{"absolute path", mut(func(s *Suite) { s.Cases[0].Request.Path = "http://evil/" }), "relative"},
		{"protocol relative", mut(func(s *Suite) { s.Cases[0].Request.Path = "//evil/" }), "relative"},
		{"no expectation", mut(func(s *Suite) { s.Cases[0].Expect = CaseExpect{} }), "expects nothing"},
		{"too many samples", mut(func(s *Suite) { s.Cases[0].Expect = CaseExpect{Weights: map[string]int{"a": 100}, Samples: 5000} }), "samples"},
		{"too many cases", mut(func(s *Suite) {
			for i := 0; i < 201; i++ {
				c := s.Cases[0]
				c.ID = "c" + string(rune('A'+i%26)) + strings.Repeat("x", i/26)
				s.Cases = append(s.Cases, c)
			}
		}), "at most 200"},
	}
	for _, tc := range tests {
		err := tc.s.Validate()
		switch {
		case tc.err == "" && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)):
			t.Errorf("%s: err %v, want %q", tc.name, err, tc.err)
		}
	}
}

func TestNormalizeDefaults(t *testing.T) {
	s := &Suite{Cases: []Case{{ID: "w", Expect: CaseExpect{Weights: map[string]int{"b": 20, "a": 80}}}}}
	s.Normalize()
	c := s.Cases[0]
	if c.Request.Method != "GET" || c.Request.Path != "/" || c.Expect.Samples != 200 || c.Expect.Tolerance != 5 || c.Expect.Text != "a 80% · b 20%" {
		t.Errorf("%+v", c)
	}
	if s.Notes == nil || (&Suite{}).Cases != nil {
		t.Error("notes must be an empty list, not null")
	}
}

func TestMergeGenerated(t *testing.T) {
	saved := sampleSuite()
	saved.Cases = append(saved.Cases, Case{ID: "mine", Request: CaseRequest{Path: "/m"}, Expect: CaseExpect{Status: 200}}, Case{ID: "r1-match", Generated: false})
	saved.Source = SuiteSource{Kind: SourceExternal}
	saved.Last = &SuiteRun{}
	fresh := &Suite{Target: saved.Target, Cases: []Case{{ID: "r1-match", Generated: true, Expect: CaseExpect{Status: 403}}, {ID: "r1-new", Generated: true, Expect: CaseExpect{Status: 200}}}}
	got := MergeGenerated(saved, fresh)
	var idsGot []string
	for _, c := range got.Cases {
		idsGot = append(idsGot, c.ID)
	}
	if !reflect.DeepEqual(idsGot, []string{"r1-match", "r1-new", "mine"}) {
		t.Errorf("ids %v", idsGot)
	}
	if got.Source.Kind != SourceExternal || got.Last != nil || got.Saved {
		t.Errorf("%+v", got)
	}
	if MergeGenerated(nil, fresh).Cases[0].ID != "r1-match" {
		t.Error("nil saved")
	}
}

func TestEvaluate(t *testing.T) {
	at := time.Now()
	status := func(n int) Case { return Case{ID: "s", Expect: CaseExpect{Status: n}} }
	rule := Case{ID: "r", Expect: CaseExpect{Rule: 1, Backends: []string{"reviews-v1", "reviews-v2"}}}
	one := Case{ID: "r", Expect: CaseExpect{Rule: 2, Backend: "reviews-v3"}}
	neg := Case{ID: "n", Expect: CaseExpect{NotRoute: true, Backend: "reviews-v1"}}
	tests := []struct {
		name    string
		c       Case
		o       Observation
		status  string
		gotText string
		msg     string
	}{
		{"status pass", status(403), Observation{Status: 403}, StatusPass, "403", ""},
		{"status fail (the Red case)", status(403), Observation{Status: 200, DecidedBy: "reviews-methods", DecidedByKind: "policy"}, StatusFail, "200", "Expected 403, got 200."},
		{"status error from transport", status(200), Observation{Error: "connection refused"}, StatusError, "", "connection refused"},
		{"transport error with a status is still compared", status(503), Observation{Status: 503, Error: "x"}, StatusPass, "503", ""},
		{"rule pass", rule, Observation{Status: 200, Backend: "reviews-v1"}, StatusPass, "rule 1 → reviews-v1", ""},
		{"rule fail", rule, Observation{Status: 200, Backend: "reviews-v3"}, StatusFail, "200 · reviews-v3", "Expected rule 1 (reviews-v1, reviews-v2), but reviews-v3 answered."},
		{"rule unidentified backend", rule, Observation{Status: 404}, StatusUnknown, GotUnknownBackend, "could not tell which backend answered"},
		{"rule unidentified backend, matching rule known", rule, Observation{Status: 200, Rule: 1}, StatusUnknown, GotUnknownBackend, "matches rule 1, as expected"},
		{"rule unidentified backend, other rule", rule, Observation{Status: 200, Rule: 2}, StatusUnknown, GotUnknownBackend, "matches rule 2, not rule 1"},
		{"negative unidentified backend", neg, Observation{Status: 200}, StatusUnknown, GotUnknownBackend, "could not tell which backend answered"},
		{"single backend rule", one, Observation{Status: 200, Backend: "reviews-v3"}, StatusPass, "rule 2 → reviews-v3", ""},
		{"negative pass", neg, Observation{Status: 200, Backend: "reviews-v1"}, StatusPass, "200 · reviews-v1", ""},
		{"negative fail", neg, Observation{Status: 200, Backend: "reviews-v2"}, StatusFail, "200 · reviews-v2", "Expected reviews-v1 to answer directly"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := Evaluate(tc.c, tc.o, at)
			if r.Status != tc.status {
				t.Fatalf("status %s (%s), want %s", r.Status, r.Message, tc.status)
			}
			if tc.gotText != "" && r.Got.Text != tc.gotText {
				t.Errorf("got text %q, want %q", r.Got.Text, tc.gotText)
			}
			if !strings.Contains(r.Message, tc.msg) {
				t.Errorf("message %q, want %q", r.Message, tc.msg)
			}
			if r.ID != tc.c.ID || !r.At.Equal(at) {
				t.Errorf("id/at: %+v", r)
			}
			if tc.o.DecidedBy == "" && (r.DecidedBy != "" || r.DecidedByKind != "") {
				t.Errorf("nothing decided: decided by stays empty, never \"unknown\": %+v", r)
			}
			if tc.o.DecidedBy != "" && r.DecidedBy != tc.o.DecidedBy {
				t.Errorf("decided by %q", r.DecidedBy)
			}
		})
	}
}

func samples(counts map[string]int, status int, ms int64) []Sample {
	var out []Sample
	for _, k := range sortedKeys(counts) {
		for i := 0; i < counts[k]; i++ {
			out = append(out, Sample{Status: status, Backend: k, DurationMs: ms + int64(i%30)})
		}
	}
	return out
}

func TestEvaluateWeights(t *testing.T) {
	c := Case{ID: "w", Expect: CaseExpect{Weights: map[string]int{"reviews-v1": 80, "reviews-v2": 20, "reviews-v3": 0}, Tolerance: 5, Samples: 200}}
	at := time.Now()
	tests := []struct {
		name    string
		o       Observation
		status  string
		v1      float64
		message string
	}{
		{"mockup 05: 78.5 / 21.5", Observation{Samples: samples(map[string]int{"reviews-v1": 157, "reviews-v2": 43}, 200, 9)}, StatusPass, 78.5, "Split behaves as configured. Within ±5% tolerance at 200 requests."},
		{"boundary: exactly at tolerance passes", Observation{Samples: samples(map[string]int{"reviews-v1": 170, "reviews-v2": 30}, 200, 9)}, StatusPass, 85, "Split behaves"},
		{"just over tolerance fails", Observation{Samples: samples(map[string]int{"reviews-v1": 175, "reviews-v2": 25}, 200, 9)}, StatusFail, 87.5, "reviews-v1 got 87.5% where 80% was expected"},
		{"79/21 passes", Observation{Samples: samples(map[string]int{"reviews-v1": 158, "reviews-v2": 42}, 200, 9)}, StatusPass, 79, ""},
		{"unexpected backend fails", Observation{Samples: samples(map[string]int{"reviews-v1": 100, "reviews-v2": 40, "reviews-v3": 60}, 200, 9)}, StatusFail, 50, "got 50%"},
		{"75/25 is exactly 5 away", Observation{Samples: samples(map[string]int{"reviews-v1": 150, "reviews-v2": 50}, 200, 9)}, StatusPass, 75, ""},
		{"no samples", Observation{Error: "pod gone"}, StatusError, 0, "pod gone"},
		{"mostly unidentified replies", Observation{Samples: append(samples(map[string]int{"reviews-v1": 10}, 200, 1), samples(map[string]int{"": 90}, 200, 1)...)}, StatusError, 0, "Only 10 of 100 replies"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := Evaluate(c, tc.o, at)
			if r.Status != tc.status {
				t.Fatalf("status %s: %s", r.Status, r.Message)
			}
			if !strings.Contains(r.Message, tc.message) {
				t.Errorf("message %q, want %q", r.Message, tc.message)
			}
			if tc.status != StatusError {
				if r.Got.Weights["reviews-v1"] != tc.v1 {
					t.Errorf("v1 observed %v, want %v", r.Got.Weights["reviews-v1"], tc.v1)
				}
				if len(r.Weights) != 3 || r.Weights[0].Backend != "reviews-v1" || r.Weights[0].Expected != 80 {
					t.Errorf("rows %+v", r.Weights)
				}
				if r.Got.Samples != len(tc.o.Samples) || r.Got.Statuses["200"] != len(tc.o.Samples) || r.Got.P50Ms == 0 || r.Got.P95Ms < r.Got.P50Ms {
					t.Errorf("stats %+v", r.Got)
				}
			}
		})
	}
	// Row detail for the mockup.
	r := Evaluate(c, Observation{Samples: samples(map[string]int{"reviews-v1": 157, "reviews-v2": 43}, 200, 9)}, at)
	want := []WeightRow{{"reviews-v1", 80, 78.5, true}, {"reviews-v2", 20, 21.5, true}, {"reviews-v3", 0, 0, true}}
	if !reflect.DeepEqual(r.Weights, want) {
		t.Errorf("rows %+v", r.Weights)
	}
}

func TestPercentile(t *testing.T) {
	v := []int64{5, 1, 4, 2, 3}
	if percentile(v, 50) != 3 || percentile(v, 95) != 5 || percentile(nil, 50) != 0 || percentile([]int64{7}, 95) != 7 {
		t.Error("percentile")
	}
}

func TestSummarize(t *testing.T) {
	at := time.Now()
	s := Summarize([]CaseResult{{Status: StatusPass}, {Status: StatusPass}, {Status: StatusFail}, {Status: StatusError}, {Status: StatusSkipped}}, at)
	if s.Total != 5 || s.Passed != 2 || s.Failed != 1 || s.Errored != 1 || s.Skipped != 1 || s.OK() {
		t.Errorf("%+v", s)
	}
	if !Summarize([]CaseResult{{Status: StatusPass}, {Status: StatusSkipped}}, at).OK() {
		t.Error("skipped is not a failure")
	}
}

func TestExpectAndGotText(t *testing.T) {
	for _, tc := range []struct {
		e    CaseExpect
		want string
	}{
		{CaseExpect{Status: 403}, "403"},
		{CaseExpect{Rule: 1, Backends: []string{"a", "b"}}, "rule 1 → split"},
		{CaseExpect{Rule: 1, Backends: []string{"a"}}, "rule 1 → a"},
		{CaseExpect{NotRoute: true}, "not this route"},
		{CaseExpect{Weights: map[string]int{"b": 20, "a": 80}}, "a 80% · b 20%"},
		{CaseExpect{}, ""},
	} {
		if got := ExpectText(tc.e); got != tc.want {
			t.Errorf("%+v: %q", tc.e, got)
		}
	}
	if got := GotText(CaseGot{Weights: map[string]float64{"a": 78.5, "b": 21.5}}); got != "a 78.5% · b 21.5%" {
		t.Errorf("%q", got)
	}
	if got := GotText(CaseGot{Weights: map[string]float64{"a": 80}}); got != "a 80%" {
		t.Errorf("%q", got)
	}
}

func TestParseRBACDetail(t *testing.T) {
	tests := []struct {
		name   string
		fields map[string]string
		want   RBACDetail
	}{
		{"matched deny", map[string]string{"response_code_details": "rbac_access_denied_matched_policy[ns[bookinfo]-policy[deny-red]-rule[0]]", "upstream_cluster": "inbound-vip|9080|http|reviews.bookinfo.svc.cluster.local"},
			RBACDetail{Namespace: "bookinfo", Policy: "deny-red", Rule: "0", Backend: "reviews"}},
		{"no ALLOW matched", map[string]string{"response_code_details": "rbac_access_denied_ns_no_policies_match"}, RBACDetail{NoMatch: true}},
		{"routed to a backend Service", map[string]string{"response_code_details": "via_upstream", "upstream_cluster": "outbound|9080||reviews-v2.bookinfo.svc.cluster.local"}, RBACDetail{Backend: "reviews-v2"}},
		{"nothing", map[string]string{"response_code_details": "-", "upstream_cluster": "-"}, RBACDetail{}},
	}
	for _, tc := range tests {
		if got := ParseRBACDetail(tc.fields); got != tc.want {
			t.Errorf("%s: %+v want %+v", tc.name, got, tc.want)
		}
	}
}
