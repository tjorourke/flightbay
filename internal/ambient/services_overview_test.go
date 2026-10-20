package ambient

import (
	"reflect"
	"testing"
)

func badgeLabels(bs []Badge) []string {
	out := []string{}
	for _, b := range bs {
		out = append(out, b.Label)
	}
	return out
}

func TestDeriveBadges(t *testing.T) {
	tests := []struct {
		name                       string
		f                          ServiceFacts
		access, routing, resilient []string
	}{
		{"open", ServiceFacts{}, []string{}, []string{}, []string{}},
		{"deny and allow", ServiceFacts{Policies: []PolicyFact{
			{Kind: "AuthorizationPolicy", Action: "DENY", Enforced: "waypoint"},
			{Kind: "AuthorizationPolicy", Action: "ALLOW", Enforced: "waypoint"},
			{Kind: "AuthorizationPolicy", Action: "ALLOW", Enforced: "waypoint"}}},
			[]string{"1 DENY", "2 ALLOW"}, []string{}, []string{}},
		{"l4 and jwt", ServiceFacts{Policies: []PolicyFact{
			{Kind: "PeerAuthentication", MTLS: "STRICT"},
			{Kind: "RequestAuthentication"},
			{Kind: "AuthorizationPolicy", Action: "ALLOW", Enforced: "ztunnel"}}},
			[]string{"L4 lock", "JWT"}, []string{}, []string{}},
		{"permissive is not a lock", ServiceFacts{Policies: []PolicyFact{{Kind: "PeerAuthentication", MTLS: "PERMISSIVE"}}}, []string{}, []string{}, []string{}},
		{"split and timeout", ServiceFacts{Routes: []RouteFact{{Frontend: true, Weights: []int{80, 20}, Timeout: true}}},
			[]string{}, []string{"split 80/20", "timeout"}, []string{}},
		{"equal split", ServiceFacts{Routes: []RouteFact{{Frontend: true, Weights: []int{1, 1}}}}, []string{}, []string{"split 50/50"}, []string{}},
		{"relative weights", ServiceFacts{Routes: []RouteFact{{Frontend: true, Weights: []int{3, 1}}}}, []string{}, []string{"split 75/25"}, []string{}},
		{"header and mirror", ServiceFacts{Routes: []RouteFact{{Frontend: true, HeaderMatch: true, Mirror: true}}},
			[]string{}, []string{"header match", "mirror"}, []string{}},
		{"backend only", ServiceFacts{Routes: []RouteFact{{Of: "reviews"}}}, []string{}, []string{"backend of reviews"}, []string{}},
		{"frontend wins over backend", ServiceFacts{Routes: []RouteFact{{Of: "x"}, {Frontend: true, Timeout: true}}}, []string{}, []string{"timeout"}, []string{}},
		{"resilience", ServiceFacts{Outlier: true, CircuitBreaker: true, Retries: true, RateLimit: true},
			[]string{}, []string{}, []string{"outlier", "circuit breaker", "retries", "rate limit"}},
		{"route retry", ServiceFacts{Routes: []RouteFact{{Frontend: true, Retry: true}}}, []string{}, []string{}, []string{"retries"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, r, z := DeriveBadges(tc.f)
			if !reflect.DeepEqual(badgeLabels(a), tc.access) || !reflect.DeepEqual(badgeLabels(r), tc.routing) || !reflect.DeepEqual(badgeLabels(z), tc.resilient) {
				t.Fatalf("got %v %v %v, want %v %v %v", badgeLabels(a), badgeLabels(r), badgeLabels(z), tc.access, tc.routing, tc.resilient)
			}
		})
	}
}

func TestBadgeTones(t *testing.T) {
	a, _, _ := DeriveBadges(ServiceFacts{Policies: []PolicyFact{{Kind: "AuthorizationPolicy", Action: "DENY", Enforced: "waypoint"}}})
	if a[0].Tone != "bad" {
		t.Fatal(a)
	}
	a, _, _ = DeriveBadges(ServiceFacts{Policies: []PolicyFact{{Kind: "AuthorizationPolicy", Action: "DENY", Enforced: "waypoint", Warning: true}}})
	if a[0].Tone != "warn" {
		t.Fatal(a)
	}
}

func TestMarkDifferences(t *testing.T) {
	cells := map[string]ClusterCell{"a": {Present: true}, "b": {Present: true}, "c": {}}
	got, drift := markDifferences(cells, map[string]string{"a": "x", "b": "y"})
	if !drift || !got["a"].Differs || !got["b"].Differs || got["c"].Differs {
		t.Fatalf("%v %v", got, drift)
	}
	got, drift = markDifferences(map[string]ClusterCell{"a": {}, "b": {}}, map[string]string{"a": "x", "b": "x"})
	if drift || got["a"].Differs {
		t.Fatal("equal sets must not differ")
	}
	if _, drift = markDifferences(map[string]ClusterCell{"a": {}}, map[string]string{"a": "x"}); drift {
		t.Fatal("single cluster cannot differ")
	}
}

func TestBuildAttention(t *testing.T) {
	mk := func(state, ns, name, id string) AmbientIssue {
		return AmbientIssue{Subject: Ref{Cluster: "c1", Kind: "Service", Namespace: ns, Name: name}, Step: Step{ID: id, State: state, Detail: "first\nsecond", SuggestedFix: &FixGuide{ID: id}}}
	}
	rows := BuildAttention([]AmbientIssue{mk("warning", "a", "z", "w"), mk("fail", "b", "y", "endpoints"), mk("fail", "a", "x", "waypoint")}, "")
	if len(rows) != 3 || rows[0].State != "fail" || rows[0].Ref.Name != "x" || rows[2].State != "warn" || rows[0].Detail != "first" || !rows[0].HasFix || rows[1].CheckID != "endpoints" {
		t.Fatalf("%+v", rows)
	}
	if got := BuildAttention([]AmbientIssue{mk("fail", "a", "x", "i"), mk("fail", "b", "y", "i")}, "b"); len(got) != 1 {
		t.Fatal(got)
	}
}

func TestSplitLabel(t *testing.T) {
	for in, want := range map[string]string{"80/20": splitLabel([]int{80, 20}), "33/33/34": splitLabel([]int{1, 1, 1}), "50/50": splitLabel([]int{0, 0})} {
		if in != want {
			t.Errorf("want %s got %s", in, want)
		}
	}
}
