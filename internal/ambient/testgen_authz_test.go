package ambient

import (
	"strings"
	"testing"
)

var testSvc = Ref{Cluster: "c", Kind: "Service", Resource: "services", Namespace: "bookinfo", Name: "reviews"}

func azRef(name string) Ref {
	return Ref{Cluster: "c", Group: SecurityGroup, Resource: "authorizationpolicies", Kind: KindAuthorizationPolicy, Namespace: "bookinfo", Name: name}
}

func wl(ns, sa string, curl bool) Workload {
	return Workload{Ref: Ref{Cluster: "c", Resource: "pods", Kind: "Pod", Namespace: ns, Name: sa + "-pod"}, Namespace: ns, ServiceAccount: sa,
		Principal: "cluster.local/ns/" + ns + "/sa/" + sa, InMesh: true, Curl: curl}
}

func azIn(policy AuthzPolicy, others ...AuthzPolicy) AuthzInput {
	ws := []Workload{wl("bookinfo", "bookinfo-productpage", true), wl("bookinfo", "bookinfo-ratings", false), wl("other", "stranger", false)}
	return AuthzInput{Policy: policy, Service: testSvc, Port: 9080, Ports: []int{9080, 9090}, DefaultPath: "/reviews/0", Others: others, Workloads: ws, Source: &ws[0]}
}

func spec(action string, rules ...any) map[string]any {
	m := map[string]any{"rules": rules}
	if action != "" {
		m["action"] = action
	}
	return m
}

func rule(parts ...any) map[string]any {
	r := map[string]any{}
	for i := 0; i+1 < len(parts); i += 2 {
		r[parts[i].(string)] = parts[i+1]
	}
	return r
}

func when(key string, values ...any) any { return []any{map[string]any{"key": key, "values": values}} }
func whenNot(key string, values ...any) any {
	return []any{map[string]any{"key": key, "notValues": values}}
}
func op(kv ...any) any {
	return []any{map[string]any{"operation": rule(kv...)}}
}
func src(kv ...any) any {
	return []any{map[string]any{"source": rule(kv...)}}
}
func list(xs ...any) []any { return xs }

func caseByID(s *Suite, id string) *Case {
	for i := range s.Cases {
		if s.Cases[i].ID == id {
			return &s.Cases[i]
		}
	}
	return nil
}

func ids(s *Suite) string {
	var out []string
	for _, c := range s.Cases {
		out = append(out, c.ID)
	}
	return strings.Join(out, ",")
}

func TestGenerateAuthz(t *testing.T) {
	deny := ParseAuthzPolicy(azRef("deny-red"), spec("DENY", rule("when", when("request.headers[color]", "red"))), "waypoint")
	methods := ParseAuthzPolicy(azRef("reviews-methods"), spec("", rule("to", op("methods", list("GET", "HEAD")))), "waypoint")

	type want struct {
		id     string
		status int
		why    string
		check  func(t *testing.T, c *Case)
	}
	tests := []struct {
		name   string
		in     AuthzInput
		want   []want
		absent []string
		notes  []string
	}{
		{
			name: "DENY on a header, no other policies",
			in:   azIn(deny),
			want: []want{
				{"r1-match", 403, "Rule matches", func(t *testing.T, c *Case) {
					if c.Request.Headers["color"] != "red" || c.Request.Method != "GET" || c.Request.Path != "/reviews/0" || c.Request.Port != 9080 {
						t.Errorf("request %+v", c.Request)
					}
				}},
				{"r1-value-color", 200, "Different value", nil},
				{"r1-absent-color", 200, "Header absent", func(t *testing.T, c *Case) {
					if len(c.Request.Headers) != 0 {
						t.Errorf("header should be absent: %+v", c.Request.Headers)
					}
				}},
				{"r1-case-color", 200, "Case of value", func(t *testing.T, c *Case) {
					if c.Request.Headers["color"] != "Red" {
						t.Errorf("want Red, got %q", c.Request.Headers["color"])
					}
				}},
				{"bypass", 200, "Waypoint bypass", func(t *testing.T, c *Case) {
					if !c.Request.Direct {
						t.Error("bypass must be direct")
					}
				}},
			},
			absent: []string{"r1-method", "r1-path"},
		},
		{
			name: "DENY plus ALLOW on methods adds an interaction case (mockup 04)",
			in:   azIn(deny, methods),
			want: []want{
				{"r1-match", 403, "Rule matches", nil},
				{"r1-value-color", 200, "Different value", nil},
				{"r1-absent-color", 200, "Header absent", nil},
				{"x-bookinfo-reviews-methods", 403, "DENY wins over reviews-methods", func(t *testing.T, c *Case) {
					if c.Request.Method == "GET" || c.Request.Method == "HEAD" || c.Request.Headers["color"] != "red" {
						t.Errorf("want a non-GET/HEAD method with color red, got %+v", c.Request)
					}
				}},
			},
		},
		{
			name: "ALLOW on methods: other method is refused",
			in:   azIn(methods),
			want: []want{
				{"r1-match", 200, "Rule matches", nil},
				{"r1-method", 403, "Other method", func(t *testing.T, c *Case) {
					if c.Request.Method == "GET" || c.Request.Method == "HEAD" {
						t.Errorf("method %s", c.Request.Method)
					}
				}},
			},
		},
		{
			name: "ALLOW on paths with prefix wildcard",
			in:   azIn(ParseAuthzPolicy(azRef("p"), spec("ALLOW", rule("to", op("paths", list("/api/*")))), "waypoint")),
			want: []want{
				{"r1-match", 200, "Rule matches", func(t *testing.T, c *Case) {
					if c.Request.Path != "/api/0" {
						t.Errorf("path %q", c.Request.Path)
					}
				}},
				{"r1-path", 403, "Other path", func(t *testing.T, c *Case) {
					if strings.HasPrefix(c.Request.Path, "/api/") {
						t.Errorf("path %q should not match /api/*", c.Request.Path)
					}
				}},
			},
		},
		{
			name: "ALLOW on ports picks another Service port for the near miss",
			in:   azIn(ParseAuthzPolicy(azRef("p"), spec("ALLOW", rule("to", op("ports", list("9080")))), "waypoint")),
			want: []want{
				{"r1-match", 200, "Rule matches", func(t *testing.T, c *Case) {
					if c.Request.Port != 9080 {
						t.Errorf("port %d", c.Request.Port)
					}
				}},
				{"r1-port", 403, "Other port", func(t *testing.T, c *Case) {
					if c.Request.Port != 9090 {
						t.Errorf("port %d", c.Request.Port)
					}
				}},
			},
		},
		{
			name: "DENY on method and path",
			in:   azIn(ParseAuthzPolicy(azRef("p"), spec("DENY", rule("to", op("methods", list("DELETE"), "paths", list("/admin*")))), "waypoint")),
			want: []want{
				{"r1-match", 403, "Rule matches", func(t *testing.T, c *Case) {
					if c.Request.Method != "DELETE" || c.Request.Path != "/admin0" {
						t.Errorf("%+v", c.Request)
					}
				}},
				{"r1-method", 200, "Other method", nil},
				{"r1-path", 200, "Other path", nil},
			},
		},
		{
			name: "notValues builds an excluded-value near miss",
			in:   azIn(ParseAuthzPolicy(azRef("p"), spec("DENY", rule("when", whenNot("request.headers[x-env]", "prod"))), "waypoint")),
			want: []want{
				{"r1-match", 403, "Rule matches", nil},
				{"r1-excluded-x-env", 200, "Excluded value", func(t *testing.T, c *Case) {
					if c.Request.Headers["x-env"] != "prod" {
						t.Errorf("%+v", c.Request.Headers)
					}
				}},
			},
		},
		{
			name: "from.source.namespaces picks a matching and a non-matching workload",
			in:   azIn(ParseAuthzPolicy(azRef("p"), spec("ALLOW", rule("from", src("namespaces", list("other")))), "waypoint")),
			want: []want{
				{"r1-match", 200, "Rule matches", func(t *testing.T, c *Case) {
					if c.From == nil || c.From.Namespace != "other" || c.From.ServiceAccount != "stranger" {
						t.Errorf("from %+v", c.From)
					}
				}},
				{"r1-caller", 403, "Other caller", func(t *testing.T, c *Case) {
					if c.From == nil || c.From.Namespace == "other" {
						t.Errorf("from %+v", c.From)
					}
				}},
			},
		},
		{
			name: "from.source.principals matches the SA and refuses the rest",
			in:   azIn(ParseAuthzPolicy(azRef("p"), spec("ALLOW", rule("from", src("principals", list("cluster.local/ns/bookinfo/sa/bookinfo-ratings")))), "waypoint")),
			want: []want{
				{"r1-match", 200, "Rule matches", func(t *testing.T, c *Case) {
					if c.From == nil || c.From.ServiceAccount != "bookinfo-ratings" {
						t.Errorf("from %+v", c.From)
					}
				}},
				{"r1-caller", 403, "Other caller", nil},
			},
		},
		{
			name:   "principal that no captured workload has produces no case and a note",
			in:     azIn(ParseAuthzPolicy(azRef("p"), spec("ALLOW", rule("from", src("principals", list("cluster.local/ns/x/sa/ghost")))), "waypoint")),
			absent: []string{"r1-match"},
			notes:  []string{"Not generated: no captured workload matches from.source (rule 1)"},
		},
		{
			name:   "unsupported fields yield no case and a note",
			in:     azIn(ParseAuthzPolicy(azRef("p"), spec("DENY", rule("to", op("hosts", list("a.example.com")))), "waypoint")),
			absent: []string{"r1-match"},
			notes:  []string{"Not generated: to.operation.hosts (rule 1)"},
		},
		{
			name:   "unsupported when key",
			in:     azIn(ParseAuthzPolicy(azRef("p"), spec("DENY", rule("when", when("request.auth.claims[iss]", "x"))), "waypoint")),
			absent: []string{"r1-match"},
			notes:  []string{"Not generated: when request.auth.claims[iss] (rule 1)"},
		},
		{
			name:   "unsupported source field",
			in:     azIn(ParseAuthzPolicy(azRef("p"), spec("ALLOW", rule("from", src("requestPrincipals", list("*")))), "waypoint")),
			absent: []string{"r1-match"},
			notes:  []string{"Not generated: from.source.requestPrincipals (rule 1)"},
		},
		{
			name:   "CUSTOM has no cases",
			in:     azIn(ParseAuthzPolicy(azRef("p"), spec("CUSTOM", rule()), "waypoint")),
			absent: []string{"r1-match", "bypass"},
			notes:  []string{"Not generated: action CUSTOM"},
		},
		{
			name: "ALLOW with no rules refuses everything",
			in:   azIn(ParseAuthzPolicy(azRef("p"), map[string]any{"action": "ALLOW"}, "waypoint")),
			want: []want{{"deny-all", 403, "This policy matches nothing; other ALLOW policies may admit the caller", nil}},
		},
		{
			name:   "DENY with no rules never denies",
			in:     azIn(ParseAuthzPolicy(azRef("p"), map[string]any{"action": "DENY"}, "waypoint")),
			absent: []string{"deny-all", "r1-match"},
			notes:  []string{"never denies anything"},
		},
		{
			name: "ztunnel-enforced policy has no waypoint bypass case",
			in:   azIn(ParseAuthzPolicy(azRef("p"), spec("DENY", rule("from", src("namespaces", list("other")))), "ztunnel")),
			want: []want{{"r1-match", 0, "Rule matches", func(t *testing.T, c *Case) {
				if c.Expect.Transport != "connection-refused-or-reset" {
					t.Fatal(c.Expect)
				}
			}}},
			absent: []string{"bypass"},
		},
		{
			name: "multiple rules each get their own cases",
			in:   azIn(ParseAuthzPolicy(azRef("p"), spec("DENY", rule("to", op("methods", list("DELETE"))), rule("to", op("methods", list("PUT")))), "waypoint")),
			want: []want{{"r1-match", 403, "Rule matches", nil}, {"r2-match", 403, "Rule matches", nil}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := GenerateAuthzSuite(tc.in)
			for _, w := range tc.want {
				c := caseByID(s, w.id)
				if c == nil {
					t.Fatalf("missing case %s; have %s; notes %v", w.id, ids(s), s.Notes)
				}
				if c.Expect.Status != w.status {
					t.Errorf("%s: expect %d, want %d", w.id, c.Expect.Status, w.status)
				}
				if c.Why != w.why && !strings.Contains(c.Why, w.why) {
					t.Errorf("%s: why %q, want %q", w.id, c.Why, w.why)
				}
				if !c.Generated || c.Label == "" || c.Expect.Text == "" {
					t.Errorf("%s: incomplete case %+v", w.id, c)
				}
				if w.check != nil {
					w.check(t, c)
				}
			}
			for _, id := range tc.absent {
				if caseByID(s, id) != nil {
					t.Errorf("case %s should not be generated", id)
				}
			}
			for _, n := range tc.notes {
				found := false
				for _, got := range s.Notes {
					found = found || strings.Contains(got, n)
				}
				if !found {
					t.Errorf("missing note %q in %v", n, s.Notes)
				}
			}
			for _, n := range s.Notes {
				if strings.Contains(n, "Not generated") && !strings.HasPrefix(n, "Not generated:") {
					t.Errorf("malformed note %q", n)
				}
			}
			if err := s.Validate(); err != nil && len(s.Cases) > 0 {
				t.Errorf("generated suite is invalid: %v", err)
			}
		})
	}
}

func TestWaypointBypassExpectation(t *testing.T) {
	deny := ParseAuthzPolicy(azRef("deny"), spec("DENY", rule("when", when("request.headers[color]", "red"))), "waypoint")
	for _, lock := range []bool{false, true} {
		in := azIn(deny)
		in.WaypointLock = lock
		if lock {
			in.Others = append(in.Others, ParseAuthzPolicy(azRef("l4-lock"), spec("ALLOW", rule("from", src("principals", list("cluster.local/ns/bookinfo/sa/waypoint")))), "ztunnel"))
		}
		c := caseByID(GenerateAuthzSuite(in), "bypass")
		want := 200
		if lock {
			want = 0
		}
		if c == nil || c.Expect.Status != want || !c.Request.Direct {
			t.Errorf("lock=%v: %+v", lock, c)
		}
		if lock && c != nil && c.Expect.Transport != "connection-refused-or-reset" {
			t.Fatal(c.Expect)
		}
	}
}

func TestLayeredAuthorizationOutcomes(t *testing.T) {
	r := reqModel{Port: 80, Principal: "cluster.local/ns/app/sa/client", Namespace: "app", SourceKnown: true, Method: "GET"}
	l4 := ParseAuthzPolicy(azRef("deny-all"), map[string]any{"action": "ALLOW"}, "ztunnel")
	l7 := ParseAuthzPolicy(azRef("l7"), spec("ALLOW", map[string]any{}), "waypoint")
	v := decideLayers(AuthzInput{Policy: l4}, []AuthzPolicy{l4}, r, false)
	if v.Status != 0 || v.Transport != "connection-refused-or-reset" {
		t.Fatal(v)
	}
	in := AuthzInput{Policy: l7, WaypointPrincipal: "cluster.local/ns/app/sa/wp"}
	v = decideLayers(in, []AuthzPolicy{l7, l4}, r, false)
	if v.Status != 503 || v.Transport != "" {
		t.Fatal(v)
	}
	l7.Action = "DENY"
	v = decideLayers(in, []AuthzPolicy{l7, l4}, r, false)
	if v.Status != 403 {
		t.Fatal(v)
	}
	l4allow := ParseAuthzPolicy(azRef("caller"), spec("ALLOW", rule("from", src("serviceAccounts", list("app/client")))), "ztunnel")
	v = decideLayers(in, []AuthzPolicy{l7, l4, l4allow}, r, true)
	if v.Status != 200 {
		t.Fatal(v)
	}
}

func TestAuthzUnknownOtherPolicyDropsCase(t *testing.T) {
	deny := ParseAuthzPolicy(azRef("deny"), spec("DENY", rule("when", when("request.headers[color]", "red"))), "waypoint")
	jwt := ParseAuthzPolicy(azRef("jwt-only"), spec("ALLOW", rule("from", src("requestPrincipals", list("*")))), "waypoint")
	s := GenerateAuthzSuite(azIn(deny, jwt))
	// A DENY match is decided before any ALLOW, so it is still known.
	if c := caseByID(s, "r1-match"); c == nil || c.Expect.Status != 403 {
		t.Fatalf("match case: %+v", c)
	}
	// Whether a near miss is allowed depends on a JWT field the evaluator cannot read.
	if caseByID(s, "r1-value-color") != nil {
		t.Error("near miss must be dropped when another policy is undecidable")
	}
	found := false
	for _, n := range s.Notes {
		found = found || strings.Contains(n, "jwt-only")
	}
	if !found {
		t.Errorf("want a note naming jwt-only, got %v", s.Notes)
	}
}

func TestAuthzDenyOtherInteraction(t *testing.T) {
	allow := ParseAuthzPolicy(azRef("allow-get"), spec("ALLOW", rule("to", op("methods", list("GET")))), "waypoint")
	denyDelete := ParseAuthzPolicy(azRef("deny-delete"), spec("DENY", rule("to", op("methods", list("DELETE")))), "waypoint")
	s := GenerateAuthzSuite(azIn(allow, denyDelete))
	c := caseByID(s, "x-bookinfo-deny-delete")
	// ALLOW GET and DENY DELETE cannot both match one request: no interaction case.
	if c != nil {
		t.Errorf("unexpected case %+v", c)
	}
	both := ParseAuthzPolicy(azRef("deny-red"), spec("DENY", rule("when", when("request.headers[color]", "red"))), "waypoint")
	s = GenerateAuthzSuite(azIn(allow, both))
	c = caseByID(s, "x-bookinfo-deny-red")
	if c == nil || c.Expect.Status != 403 || c.Request.Headers["color"] != "red" || c.Request.Method != "GET" {
		t.Errorf("want GET with color red refused by the DENY: %+v", c)
	}
}

func TestEvaluator(t *testing.T) {
	deny := ParseAuthzPolicy(azRef("d"), spec("DENY", rule("when", when("request.headers[Color]", "red"))), "waypoint")
	allowGet := ParseAuthzPolicy(azRef("a"), spec("ALLOW", rule("to", op("methods", list("GET", "HEAD")))), "waypoint")
	allowNS := ParseAuthzPolicy(azRef("n"), spec("ALLOW", rule("from", src("namespaces", list("bookinfo")))), "waypoint")
	jwt := ParseAuthzPolicy(azRef("j"), spec("ALLOW", rule("from", src("requestPrincipals", list("*")))), "waypoint")
	denyIP := ParseAuthzPolicy(azRef("ip"), spec("DENY", rule("from", src("ipBlocks", list("10.0.0.0/8")))), "waypoint")
	req := func(method, path string, headers map[string]string, ns string, known bool) reqModel {
		h := map[string]string{}
		for k, v := range headers {
			h[strings.ToLower(k)] = v
		}
		return reqModel{Method: method, Path: path, Port: 9080, Headers: h, Namespace: ns, Principal: "cluster.local/ns/" + ns + "/sa/x", SourceKnown: known}
	}
	tests := []struct {
		name     string
		policies []AuthzPolicy
		req      reqModel
		status   int
		known    bool
		by       string
	}{
		{"no policies allow", nil, req("GET", "/", nil, "a", true), 200, true, ""},
		{"DENY match", []AuthzPolicy{deny}, req("GET", "/", map[string]string{"color": "red"}, "a", true), 403, true, "d"},
		{"header name is case-insensitive, value exact", []AuthzPolicy{deny}, req("GET", "/", map[string]string{"COLOR": "Red"}, "a", true), 200, true, ""},
		{"ALLOW with a match", []AuthzPolicy{allowGet}, req("GET", "/", nil, "a", true), 200, true, "a"},
		{"ALLOW with no match", []AuthzPolicy{allowGet}, req("POST", "/", nil, "a", true), 403, true, ""},
		{"DENY beats ALLOW", []AuthzPolicy{allowGet, deny}, req("GET", "/", map[string]string{"color": "red"}, "a", true), 403, true, "d"},
		{"two ALLOWs are a union", []AuthzPolicy{allowGet, allowNS}, req("POST", "/", nil, "bookinfo", true), 200, true, "n"},
		{"caller unknown makes a from rule unknown", []AuthzPolicy{allowNS}, req("GET", "/", nil, "", false), 0, false, ""},
		{"unsupported ALLOW makes a miss unknown", []AuthzPolicy{allowGet, jwt}, req("POST", "/", nil, "a", true), 0, false, ""},
		{"unsupported ALLOW is irrelevant when another ALLOW matches", []AuthzPolicy{allowGet, jwt}, req("GET", "/", nil, "a", true), 200, true, "a"},
		{"unsupported DENY is unknown unless another DENY matches", []AuthzPolicy{denyIP}, req("GET", "/", nil, "a", true), 0, false, ""},
		{"a matching DENY wins over an unknown DENY", []AuthzPolicy{denyIP, deny}, req("GET", "/", map[string]string{"color": "red"}, "a", true), 403, true, "d"},
		{"CUSTOM is ignored", []AuthzPolicy{{Ref: azRef("c"), Action: "CUSTOM"}}, req("GET", "/", nil, "a", true), 200, true, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := decide(tc.policies, tc.req)
			if v.Known != tc.known || v.Status != tc.status {
				t.Fatalf("got known=%v status=%d, want known=%v status=%d", v.Known, v.Status, tc.known, tc.status)
			}
			by := ""
			if v.By != nil {
				by = v.By.Ref.Name
			}
			if by != tc.by {
				t.Errorf("decided by %q, want %q", by, tc.by)
			}
		})
	}
}

func TestStrMatch(t *testing.T) {
	for _, tc := range []struct {
		p, v string
		ok   bool
	}{{"*", "anything", true}, {"/api/*", "/api/x", true}, {"/api/*", "/api", false}, {"*.json", "a.json", true}, {"*.json", "a.xml", false}, {"GET", "GET", true}, {"GET", "get", false}, {"", "", true}} {
		if strMatch(tc.p, tc.v) != tc.ok {
			t.Errorf("strMatch(%q, %q) != %v", tc.p, tc.v, tc.ok)
		}
	}
}

func TestRuleInWords(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    AuthzPolicy
		want string
	}{
		{"deny header", ParseAuthzPolicy(azRef("d"), spec("DENY", rule("when", when("request.headers[color]", "red"))), "waypoint"), "Deny any request to reviews when header color equals red."},
		{"allow methods", ParseAuthzPolicy(azRef("a"), spec("ALLOW", rule("to", op("methods", list("GET", "HEAD")))), "waypoint"), "Allow GET or HEAD requests to reviews."},
		{"allow ns and path", ParseAuthzPolicy(azRef("a"), spec("ALLOW", rule("from", src("namespaces", list("web")), "to", op("paths", list("/api/*")))), "waypoint"), "Allow any request to reviews for /api/* from namespace web."},
		{"principal", ParseAuthzPolicy(azRef("a"), spec("ALLOW", rule("from", src("principals", list("cluster.local/ns/b/sa/productpage")))), "waypoint"), "Allow any request to reviews from productpage."},
		{"no rules allow", ParseAuthzPolicy(azRef("a"), map[string]any{}, "waypoint"), "This ALLOW policy permits nothing. Other ALLOW policies can still admit requests to reviews."},
		{"two rules", ParseAuthzPolicy(azRef("a"), spec("DENY", rule("to", op("methods", list("DELETE"))), rule("to", op("methods", list("PUT")))), "waypoint"), "Deny DELETE requests to reviews, or PUT requests to reviews."},
		{"not values", ParseAuthzPolicy(azRef("a"), spec("DENY", rule("when", whenNot("request.headers[x]", "1"))), "waypoint"), "Deny any request to reviews when header x does not equal 1."},
		{"custom", ParseAuthzPolicy(azRef("a"), spec("CUSTOM"), "waypoint"), "CUSTOM rules are decided by an external provider, so there is no sentence for them."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := RuleInWords(tc.p, "reviews"); got != tc.want {
				t.Errorf("\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestPolicySummaryAndExplain(t *testing.T) {
	deny := ParseAuthzPolicy(azRef("deny"), spec("DENY", rule("when", when("request.headers[color]", "red"))), "waypoint")
	methods := ParseAuthzPolicy(azRef("reviews-methods"), spec("", rule("to", op("methods", list("GET", "HEAD")))), "waypoint")
	if got := PolicySummary(methods); got != "GET, HEAD only" {
		t.Errorf("summary %q", got)
	}
	if got := PolicySummary(ParseAuthzPolicy(azRef("x"), spec("DENY", rule("to", op("methods", list("DELETE")))), "waypoint")); got != "DELETE refused" {
		t.Errorf("summary %q", got)
	}
	e := ExplainAuthz(azIn(deny, methods), []string{"n"})
	if len(e.Others) != 1 || e.Others[0].Name != "reviews-methods" || e.Others[0].Action != "ALLOW" || e.Others[0].Summary != "GET, HEAD only" {
		t.Errorf("others %+v", e.Others)
	}
	if !strings.Contains(e.Where, "before ALLOW policies") || len(e.Notes) != 1 {
		t.Errorf("%+v", e)
	}
	z := ParseAuthzPolicy(azRef("z"), spec("ALLOW", rule("from", src("namespaces", list("a")))), "ztunnel")
	if got := ExplainAuthz(azIn(z), nil).Where; !strings.Contains(got, "ztunnel") {
		t.Errorf("where %q", got)
	}
}

func TestGeneratedIDsAreUniqueAndStable(t *testing.T) {
	deny := ParseAuthzPolicy(azRef("deny"), spec("DENY", rule("when", when("request.headers[color]", "red"))), "waypoint")
	methods := ParseAuthzPolicy(azRef("reviews-methods"), spec("", rule("to", op("methods", list("GET", "HEAD")))), "waypoint")
	a, b := GenerateAuthzSuite(azIn(deny, methods)), GenerateAuthzSuite(azIn(deny, methods))
	if ids(a) != ids(b) {
		t.Errorf("not deterministic: %s vs %s", ids(a), ids(b))
	}
	seen := map[string]bool{}
	for _, c := range a.Cases {
		if seen[c.ID] {
			t.Errorf("duplicate id %s", c.ID)
		}
		seen[c.ID] = true
	}
}

func TestPredictDecision(t *testing.T) {
	deny := ParseAuthzPolicy(azRef("deny-red"), spec("DENY", rule("when", when("request.headers[color]", "red"))), "waypoint")
	methods := ParseAuthzPolicy(azRef("reviews-methods"), spec("", rule("to", op("methods", list("GET", "HEAD")))), "waypoint")
	in := azIn(deny, methods)
	tests := []struct {
		name   string
		c      Case
		by     string
		status int
		ok     bool
	}{
		{"deny matches", Case{Request: CaseRequest{Method: "POST", Path: "/", Headers: map[string]string{"Color": "red"}, Port: 9080}}, "deny-red", 403, true},
		{"allow matches", Case{Request: CaseRequest{Method: "GET", Path: "/x?y=1", Port: 9080}}, "reviews-methods", 200, true},
		{"no allow matches", Case{Request: CaseRequest{Method: "DELETE", Path: "/", Port: 9080}}, "", 403, true},
	}
	for _, tc := range tests {
		by, status, ok := PredictDecision(in, tc.c)
		name := ""
		if by != nil {
			name = by.Name
		}
		if name != tc.by || status != tc.status || ok != tc.ok {
			t.Errorf("%s: by=%q status=%d ok=%v", tc.name, name, status, ok)
		}
	}
	jwt := ParseAuthzPolicy(azRef("jwt"), spec("ALLOW", rule("from", src("requestPrincipals", list("*")))), "waypoint")
	if _, _, ok := PredictDecision(azIn(deny, jwt), Case{Request: CaseRequest{Method: "DELETE", Path: "/", Port: 9080}}); ok {
		t.Error("undecidable policy must not predict")
	}
}
