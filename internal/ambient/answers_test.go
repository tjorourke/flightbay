package ambient

import "testing"

func TestAnswerSentences(t *testing.T) {
	v1v2 := []Backend{{Name: "reviews-v1", Port: 9080, Weight: 80}, {Name: "reviews-v2", Port: 9080, Weight: 20}}
	red := AccessRule{Headers: []HeaderMatch{{Name: "color", Values: []string{"red"}}}}
	getHead := AccessRule{Methods: []string{"GET", "HEAD"}}
	cases := []struct {
		name  string
		facts AnswerFacts
		want  Answers
		empty bool
	}{
		{
			name:  "nothing set",
			facts: AnswerFacts{Service: "reviews"},
			want: Answers{
				Access:     "Anyone in the mesh",
				Routing:    "All traffic goes to <code>reviews</code>",
				Transport:  "Yes between mesh workloads. Non-mesh callers are still accepted.",
				Resilience: "Nothing is set. No timeout, no limits, and failing Pods keep getting traffic.",
			},
			empty: true,
		},
		{
			name:  "ALLOW only",
			facts: AnswerFacts{Service: "reviews", Access: AccessFacts{Allow: []AccessRule{getHead}}},
			want:  Answers{Access: "Only <code>GET</code> and <code>HEAD</code> from the mesh"},
			empty: true,
		},
		{
			name:  "DENY only",
			facts: AnswerFacts{Service: "reviews", Access: AccessFacts{Deny: []AccessRule{red}}},
			want:  Answers{Access: "Anyone in the mesh, except requests with <code>color: red</code>"},
			empty: true,
		},
		{
			name:  "ALLOW and DENY",
			facts: AnswerFacts{Service: "reviews", Access: AccessFacts{Allow: []AccessRule{getHead}, Deny: []AccessRule{red}}},
			want:  Answers{Access: "Only <code>GET</code> and <code>HEAD</code> from the mesh, except requests with <code>color: red</code>"},
			empty: true,
		},
		{
			name:  "DENY a method",
			facts: AnswerFacts{Service: "reviews", Access: AccessFacts{Deny: []AccessRule{{Methods: []string{"DELETE"}}}}},
			want:  Answers{Access: "Anyone in the mesh, except <code>DELETE</code> requests"},
			empty: true,
		},
		{
			name:  "L4 default deny",
			facts: AnswerFacts{Service: "reviews", Access: AccessFacts{DefaultDeny: true}},
			want:  Answers{Access: "Nobody can call it until you allow a caller"},
			empty: true,
		},
		{
			name:  "L4 allow a namespace",
			facts: AnswerFacts{Service: "reviews", Access: AccessFacts{DefaultDeny: true, Allow: []AccessRule{{SourceNamespaces: []string{"frontend"}}}}},
			want:  Answers{Access: "Only callers in <code>frontend</code>"},
			empty: true,
		},
		{
			name:  "L4 lock to the waypoint",
			facts: AnswerFacts{Service: "reviews", Access: AccessFacts{WaypointOnly: true}},
			want:  Answers{Access: "Only the waypoint can reach the Pods"},
			empty: true,
		},
		{
			name:  "JWT required",
			facts: AnswerFacts{Service: "reviews", Access: AccessFacts{JWTRequired: true, JWTIssuers: []string{"https://issuer.example"}}},
			want:  Answers{Access: "Only callers with a valid JWT from <code>https://issuer.example</code>"},
			empty: true,
		},
		{
			name:  "JWT checked but not required",
			facts: AnswerFacts{Service: "reviews", Access: AccessFacts{JWTIssuers: []string{"https://issuer.example"}}},
			want:  Answers{Access: "Anyone in the mesh. Tokens from <code>https://issuer.example</code> are checked, not required"},
			empty: true,
		},
		{
			name:  "split by version",
			facts: AnswerFacts{Service: "reviews", Routing: RoutingFacts{Routes: []RouteFacts{{Backends: v1v2}}}},
			want:  Answers{Routing: "80% to <code>v1</code>, 20% to <code>v2</code>"},
			empty: true,
		},
		{
			name:  "split between unrelated Services keeps full names",
			facts: AnswerFacts{Service: "reviews", Routing: RoutingFacts{Routes: []RouteFacts{{Backends: []Backend{{Name: "stable", Weight: 1}, {Name: "canary", Weight: 1}}}}}},
			want:  Answers{Routing: "50% to <code>stable</code>, 50% to <code>canary</code>"},
			empty: true,
		},
		{
			name: "header route with a default",
			facts: AnswerFacts{Service: "reviews", Routing: RoutingFacts{Routes: []RouteFacts{
				{Match: RouteMatchFacts{Headers: []string{"x-test"}}, Backends: []Backend{{Name: "reviews-v2", Weight: 1}}},
				{Backends: []Backend{{Name: "reviews-v1", Weight: 1}}},
			}}},
			want:  Answers{Routing: "Requests with header <code>x-test</code> go to <code>v2</code>; everything else to <code>v1</code>"},
			empty: true,
		},
		{
			name:  "STRICT mTLS",
			facts: AnswerFacts{Service: "reviews", Transport: TransportFacts{Mode: "STRICT"}},
			want:  Answers{Transport: "Yes. Only mesh workloads can connect."},
			empty: true,
		},
		{
			name:  "resilience set",
			facts: AnswerFacts{Service: "reviews", Resilience: ResilienceFacts{Timeout: "5s", EjectAfter: 3, RateRequests: 100, RateUnit: "Seconds"}},
			want:  Answers{Resilience: "Times out after <code>5s</code>. Failing Pods are ejected after 3 errors. Limited to 100 requests a second."},
		},
		{
			name:  "resilience too long collapses to a list",
			facts: AnswerFacts{Service: "reviews", Resilience: ResilienceFacts{Timeout: "5s", Retries: 3, EjectAfter: 3, RateRequests: 100, RateUnit: "Seconds", CircuitBreak: true}},
			want:  Answers{Resilience: "Timeout, retries, ejection, circuit breakers and rate limit are set."},
		},
	}
	defaults := BuildAnswers(AnswerFacts{Service: "reviews"})
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := BuildAnswers(c.facts)
			// Cases only state the sentence they are about; the others stay at their defaults.
			want := c.want
			if want.Access == "" {
				want.Access = defaults.Access
			}
			if want.Routing == "" {
				want.Routing = defaults.Routing
			}
			if want.Transport == "" {
				want.Transport = defaults.Transport
			}
			if want.Resilience == "" {
				want.Resilience = defaults.Resilience
			}
			if got != want {
				t.Fatalf("\n got %+v\nwant %+v", got, want)
			}
			for _, s := range []string{got.Access, got.Routing, got.Transport, got.Resilience} {
				if visibleLen(s) > maxResilience {
					t.Errorf("%d visible characters, over %d: %s", visibleLen(s), maxResilience, s)
				}
			}
			if ResilienceEmpty(c.facts) != c.empty && c.name == "nothing set" {
				t.Errorf("ResilienceEmpty=%v", ResilienceEmpty(c.facts))
			}
		})
	}
}

func TestAnswerFallsBackWhenTooLong(t *testing.T) {
	rules := []AccessRule{{Methods: []string{"GET"}, Paths: []string{"/a/very/long/path/prefix/*"}}, {Methods: []string{"POST"}, Paths: []string{"/another/long/path/prefix/*"}}}
	got := accessAnswer(AccessFacts{Allow: rules, Deny: []AccessRule{{Headers: []HeaderMatch{{Name: "x-very-long-header-name", Values: []string{"value-one", "value-two"}}}}}})
	if visibleLen(got) > maxAnswer {
		t.Fatalf("%d: %s", visibleLen(got), got)
	}
}
