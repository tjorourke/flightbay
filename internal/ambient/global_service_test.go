package ambient

import (
	"strings"
	"testing"
)

func TestGlobalPublicationIsScopedAndIdempotent(t *testing.T) {
	ws := fixture(t)
	req := Request{Cluster: "c", Namespace: "app", Service: "front", Tasks: []string{"global-service"}}
	if _, err := Generate(ws, req); err == nil {
		t.Fatal("unknown installation should not claim support")
	}
	pilot := object("Pod", "istiod", map[string]any{"containers": []any{map[string]any{"name": "discovery", "image": "us-docker.pkg.dev/soloio-img/istio/pilot:1.30.4-solo-distroless", "args": []any{"discovery", "--domain", "corp.internal"}}}})
	M(pilot["metadata"])["namespace"] = "istio-system"
	M(pilot["metadata"])["labels"] = map[string]any{"app": "istiod"}
	put(ws, "", "pods", "Pod", pilot)
	put(ws, "networking.istio.io", "serviceentries", "ServiceEntry")
	put(ws, "admin.solo.io", "segments", "Segment")
	svc := Target(ws, "c", "Service", "app", "front")
	M(svc.Obj["metadata"])["labels"] = map[string]any{"app": "front", "team": "keep", "solo.io/service-takeover": "true"}
	before := Copy(svc.Obj)
	p, err := Generate(ws, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Changes) != 1 || len(p.Changes[0].Fields) != 1 || len(p.Objects) != 0 {
		t.Fatalf("unexpected change scope: %+v", p)
	}
	f := p.Changes[0].Fields[0]
	if f.Path != "/metadata/labels/solo.io~1service-scope" || f.After != "global" || f.BeforePresent {
		t.Fatalf("wrong publication field: %+v", f)
	}
	if !Equal(before, svc.Obj) {
		t.Fatal("generating mutated the capture")
	}
	if !strings.Contains(p.YAML, "solo.io/service-scope: global") || strings.Contains(p.YAML, "selector:") {
		t.Fatal(p.YAML)
	}
	M(svc.Obj["metadata"])["labels"].(map[string]any)["solo.io/service-scope"] = "global"
	p, err = Generate(ws, req)
	if err != nil || len(p.Changes) != 0 {
		t.Fatalf("publication is not idempotent: %v", err)
	}
	M(svc.Obj["metadata"])["labels"].(map[string]any)["solo.io/service-scope"] = "global-only"
	p, err = Generate(ws, req)
	if err != nil || len(p.Changes) != 0 {
		t.Fatal("legacy takeover behaviour was rewritten")
	}
}

func TestServiceDNSUsesCapturedDomainsAndPublishedAliases(t *testing.T) {
	ws := fixture(t)
	svc := Target(ws, "c", "Service", "app", "front")
	d := serviceDNS(ws, svc)
	if d.DomainObserved || d.LocalName != "front.app.svc.cluster.local" || len(d.GlobalNames) != 0 {
		t.Fatalf("default DNS must be labelled assumed: %+v", d)
	}
	coreDNS := object("ConfigMap", "coredns", nil)
	M(coreDNS["metadata"])["namespace"] = "kube-system"
	coreDNS["data"] = map[string]any{"Corefile": ".:53 {\n kubernetes dns.example in-addr.arpa ip6.arpa {\n pods insecure\n }\n}\n"}
	put(ws, "", "configmaps", "ConfigMap", coreDNS)
	if observed := serviceDNS(ws, svc); !observed.DomainObserved || observed.LocalName != "front.app.svc.dns.example" {
		t.Fatalf("CoreDNS suffix ignored: %+v", observed)
	}
	pod := object("Pod", "istiod", map[string]any{"containers": []any{map[string]any{"args": []any{"--domain=private.example"}}}})
	M(pod["metadata"])["namespace"] = "istio-system"
	M(pod["metadata"])["labels"] = map[string]any{"app": "istiod"}
	put(ws, "", "pods", "Pod", pod)
	entry := object("ServiceEntry", "autogen.front", map[string]any{"hosts": []any{"front.app.mesh.local", "custom.global.example"}})
	M(entry["metadata"])["labels"] = map[string]any{"solo.io/parent-service": "front", "solo.io/parent-service-namespace": "app", "solo.io/service-scope": "global"}
	put(ws, "networking.istio.io", "serviceentries", "ServiceEntry", entry)
	d = serviceDNS(ws, svc)
	if !d.DomainObserved || d.LocalName != "front.app.svc.private.example" || len(d.GlobalNames) != 2 || d.GlobalNames[1] != "front.app.mesh.local" {
		t.Fatalf("captured names not used: %+v", d)
	}
	other := object("Pod", "other-istiod", map[string]any{"containers": []any{map[string]any{"args": []any{"--domain", "different.example"}}}})
	M(other["metadata"])["namespace"] = "istio-system"
	M(other["metadata"])["labels"] = map[string]any{"app": "istiod"}
	put(ws, "", "pods", "Pod", other)
	if d = serviceDNS(ws, svc); d.LocalName != "" || d.DomainObserved {
		t.Fatal("conflicting suffixes were guessed")
	}
}

func TestGlobalUnpublishRemovesOnlyTheScopeLabel(t *testing.T) {
	ws := fixture(t)
	svc := Target(ws, "c", "Service", "app", "front")
	M(svc.Obj["metadata"])["labels"] = map[string]any{"app": "front", "solo.io/service-scope": "global"}
	before := Copy(svc.Obj)
	// No Enterprise install is captured: removing the label must not need one.
	p, err := Generate(ws, Request{Cluster: "c", Namespace: "app", Service: "front", Tasks: []string{"global-service"}, Unpublish: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Changes) != 1 || len(p.Changes[0].Fields) != 1 || len(p.Objects) != 0 {
		t.Fatalf("unexpected change scope: %+v", p)
	}
	f := p.Changes[0].Fields[0]
	if f.Path != "/metadata/labels/solo.io~1service-scope" || !f.BeforePresent || f.AfterPresent {
		t.Fatalf("wrong unpublish field: %+v", f)
	}
	if !Equal(before, svc.Obj) {
		t.Fatal("generating mutated the capture")
	}
	delete(M(svc.Obj["metadata"])["labels"].(map[string]any), "solo.io/service-scope")
	p, err = Generate(ws, Request{Cluster: "c", Namespace: "app", Service: "front", Tasks: []string{"global-service"}, Unpublish: true})
	if err != nil || len(p.Changes) != 0 {
		t.Fatalf("unpublishing a local Service should be a no-op: %v %+v", err, p)
	}
}

func TestGlobalUnpublishIncludesWeightedSplit(t *testing.T) {
	for _, scope := range []string{"global", "global-only", ""} {
		t.Run("scope="+scope, func(t *testing.T) {
			ws := fixture(t)
			svc := Target(ws, "c", "Service", "app", "front")
			M(svc.Obj["metadata"])["labels"] = map[string]any{"app": "front"}
			if scope != "" {
				M(Path(svc.Obj, "metadata", "labels"))["solo.io/service-scope"] = scope
			}
			route := object("HTTPRoute", "front-cluster-split", map[string]any{"parentRefs": []any{map[string]any{"group": "", "kind": "Service", "name": "front"}}})
			M(route["metadata"])["labels"] = map[string]any{"app.kubernetes.io/managed-by": "flightbay", "flightbay.dev/test-id": "earlier-plan"}
			put(ws, GatewayGroup, "httproutes", "HTTPRoute", route)
			alias := object("Service", "front-c", map[string]any{"selector": map[string]any{"app": "front"}})
			M(alias["metadata"])["labels"] = map[string]any{"app.kubernetes.io/managed-by": "flightbay", "flightbay.dev/test-id": "earlier-plan", "flightbay.dev/cluster-split-for": "front", "solo.io/service-scope": "global"}
			put(ws, "", "services", "Service", alias)
			p, err := Generate(ws, Request{Cluster: "c", Namespace: "app", Service: "front", Tasks: []string{"global-service"}, Unpublish: true})
			if err != nil {
				t.Fatal(err)
			}
			// Both generated resources must be reviewed, including legacy routes
			// with no cluster-split-for label and already-local main Services.
			for _, name := range []string{"front-cluster-split", "front-c"} {
				found := false
				for _, ch := range p.Changes {
					if ch.Ref.Name == name && ch.Delete && ch.UID != "" && ch.BeforeObject != nil {
						found = true
					}
				}
				if !found {
					t.Fatalf("stop plan omitted %s: %+v", name, p.Changes)
				}
			}
			if !strings.Contains(p.YAML, "# DELETE HTTPRoute app/front-cluster-split") || !strings.Contains(p.YAML, "# DELETE Service app/front-c") {
				t.Fatal("deletions missing from review", p.YAML)
			}
			if !summariseService(ws, svc).GlobalCleanup {
				t.Fatal("leftover split is not exposed in inventory")
			}
		})
	}
}

func TestGlobalUnpublishPreservesUnrelatedResources(t *testing.T) {
	for _, variant := range []string{"unmanaged", "missing-test-id", "other-parent", "other-namespace", "other-split", "multiple-parents"} {
		t.Run(variant, func(t *testing.T) {
			ws := fixture(t)
			parent := map[string]any{"group": "", "kind": "Service", "name": "front"}
			route := object("HTTPRoute", "front-cluster-split", map[string]any{"parentRefs": []any{parent}})
			labels := map[string]any{"app.kubernetes.io/managed-by": "flightbay", "flightbay.dev/test-id": "old"}
			M(route["metadata"])["labels"] = labels
			switch variant {
			case "unmanaged":
				labels["app.kubernetes.io/managed-by"] = "customer"
			case "missing-test-id":
				delete(labels, "flightbay.dev/test-id")
			case "other-parent":
				parent["name"] = "another-service"
			case "other-namespace":
				parent["namespace"] = "another-namespace"
			case "other-split":
				labels["flightbay.dev/cluster-split-for"] = "another-service"
			case "multiple-parents":
				M(route["spec"])["parentRefs"] = []any{parent, map[string]any{"kind": "Service", "name": "another-service"}}
			}
			put(ws, GatewayGroup, "httproutes", "HTTPRoute", route)
			alias := object("Service", "front-c", nil)
			M(alias["metadata"])["labels"] = map[string]any{"app.kubernetes.io/managed-by": "flightbay", "flightbay.dev/test-id": "other", "flightbay.dev/cluster-split-for": "another-service"}
			put(ws, "", "services", "Service", alias)
			p, err := Generate(ws, Request{Cluster: "c", Namespace: "app", Service: "front", Tasks: []string{"global-service"}, Unpublish: true})
			if err != nil || len(p.Changes) != 0 {
				t.Fatalf("unrelated resources included: %v %+v", err, p)
			}
		})
	}
}

func TestAccessRuleBuildsServiceTargetedPolicy(t *testing.T) {
	ws := fixture(t)
	r := Request{Cluster: "c", Namespace: "app", Service: "front", Tasks: []string{"access-rule"}, Access: &AccessRequest{Action: "DENY", Rules: []AccessRule{{Headers: []HeaderMatch{{Name: "X-Req-Origin-Type", Values: []string{"be"}}}}}}}
	svc := Target(ws, "c", "Service", "app", "front")
	spec, _, err := generateAccessRule(ws, r, svc)
	if err != nil {
		t.Fatal(err)
	}
	rule := spec["rules"].([]any)[0].(map[string]any)
	when := rule["when"].([]any)[0].(map[string]any)
	if spec["action"] != "DENY" || when["key"] != "request.headers[x-req-origin-type]" || when["values"].([]string)[0] != "be" {
		t.Fatalf("unexpected spec: %+v", spec)
	}
	if spec["targetRefs"].([]any)[0].(map[string]any)["name"] != "front" {
		t.Fatalf("not Service-targeted: %+v", spec)
	}
	r.Access.Rules = []AccessRule{{}}
	if _, _, err := generateAccessRule(ws, r, svc); err == nil {
		t.Fatal("an empty rule would match everything and must be refused")
	}
	r.Access.Rules = []AccessRule{{Headers: []HeaderMatch{{Name: "x-env", Values: []string{"prod"}, Not: true}}, Methods: []string{"POST"}, SourceNamespaces: []string{"app"}}}
	spec, _, err = generateAccessRule(ws, r, svc)
	if err != nil {
		t.Fatal(err)
	}
	rule = spec["rules"].([]any)[0].(map[string]any)
	if rule["from"] == nil || rule["to"] == nil || rule["when"].([]any)[0].(map[string]any)["notValues"] == nil {
		t.Fatalf("combined rule: %+v", rule)
	}
}

func TestHeaderDenyPolicyLoadsIntoAccessEditorAndWaypointBinding(t *testing.T) {
	ws := fixture(t)
	policy := object("AuthorizationPolicy", "block-header-be", map[string]any{"targetRefs": []any{map[string]any{"group": "", "kind": "Service", "name": "front"}}, "action": "DENY", "rules": []any{map[string]any{"when": []any{map[string]any{"key": "request.headers[x-req-origin-type]", "values": []any{"be"}}}}}})
	put(ws, "security.istio.io", "authorizationpolicies", "AuthorizationPolicy", policy)
	v, err := NewView(ws, "c", "Service", "app", "front", "")
	if err != nil {
		t.Fatal(err)
	}
	var req *Request
	for _, e := range v.Editable {
		if e.Ref.Name == "block-header-be" && e.Supported {
			req = e.Request
		}
	}
	if req == nil || req.Access == nil || req.Access.Action != "DENY" || req.Access.Rules[0].When[0].Key != "request.headers[x-req-origin-type]" || req.Access.Rules[0].When[0].Values[0] != "be" {
		t.Fatalf("header DENY did not load into the access editor: %+v", req)
	}
	if v.WaypointBinding == nil {
		t.Fatal("no waypoint binding reported")
	}
}

func TestAccessRuleCoversTheWholeRulesModel(t *testing.T) {
	ws := fixture(t)
	svc := Target(ws, "c", "Service", "app", "front")
	r := Request{Cluster: "c", Namespace: "app", Service: "front", Tasks: []string{"access-rule"}, Access: &AccessRequest{Action: "ALLOW", Rules: []AccessRule{{
		From: []AccessSource{{NotNamespaces: []string{"app"}, IPBlocks: []string{"10.0.0.0/8"}}, {RequestPrincipals: []string{"https://issuer/*"}}},
		To:   []AccessOperation{{NotPaths: []string{"/admin*"}, Ports: []string{"8080"}}},
		When: []AccessCondition{{Key: "request.headers[X-Env]", NotValues: []string{"prod"}}, {Key: "request.auth.claims[groups]", Values: []string{"ops"}}},
	}}}}
	spec, _, err := generateAccessRule(ws, r, svc)
	if err != nil {
		t.Fatal(err)
	}
	rule := spec["rules"].([]any)[0].(map[string]any)
	if len(rule["from"].([]any)) != 2 || rule["to"].([]any)[0].(map[string]any)["operation"].(map[string]any)["notPaths"] == nil {
		t.Fatalf("from/to: %+v", rule)
	}
	when := rule["when"].([]any)
	if when[0].(map[string]any)["key"] != "request.headers[x-env]" || when[1].(map[string]any)["key"] != "request.auth.claims[groups]" {
		t.Fatalf("when: %+v", when)
	}
	r.Access.Rules[0].When = []AccessCondition{{Key: "made.up", Values: []string{"x"}}}
	if _, _, err := generateAccessRule(ws, r, svc); err == nil {
		t.Fatal("unknown condition key accepted")
	}
	r.Access = &AccessRequest{Action: "DENY", RulesYAML: "- to:\n  - operation:\n      notHosts: [a.example]\n"}
	spec, _, err = generateAccessRule(ws, r, svc)
	if err != nil || len(spec["rules"].([]any)) != 1 {
		t.Fatalf("rules YAML: %v %+v", err, spec)
	}
}

func TestServicePoliciesListsEveryScope(t *testing.T) {
	ws := fixture(t)
	put(ws, "security.istio.io", "authorizationpolicies", "AuthorizationPolicy",
		object("AuthorizationPolicy", "by-target", map[string]any{"targetRefs": []any{map[string]any{"kind": "Service", "name": "front"}}, "action": "DENY", "rules": []any{map[string]any{}}}),
		object("AuthorizationPolicy", "ns-wide", map[string]any{}),
		object("AuthorizationPolicy", "other-target", map[string]any{"targetRefs": []any{map[string]any{"kind": "Service", "name": "nope"}}}))
	svc := Target(ws, "c", "Service", "app", "front")
	got := map[string]string{}
	for _, p := range servicePolicies(ws, svc) {
		got[p.Ref.Name] = p.Scope
	}
	if got["by-target"] != "service" || got["ns-wide"] != "namespace" || got["other-target"] != "" {
		t.Fatalf("scopes: %v", got)
	}
}

func TestAgentgatewayAccessBuildsCEL(t *testing.T) {
	r := Request{Service: "front", Access: &AccessRequest{Action: "DENY", Rules: []AccessRule{{
		When: []AccessCondition{{Key: "request.headers[X-Req-Origin-Type]", Values: []string{"be"}}},
		To:   []AccessOperation{{Methods: []string{"GET"}, Paths: []string{"/reviews/*"}}},
	}}}}
	spec, _, err := agentgatewayAccess(r)
	if err != nil {
		t.Fatal(err)
	}
	auth := spec["traffic"].(map[string]any)["authorization"].(map[string]any)
	expr := auth["policy"].(map[string]any)["matchExpressions"].([]string)[0]
	want := `(((request.method in ["GET"] && (request.path.startsWith("/reviews/")))) && ("x-req-origin-type" in request.headers && request.headers["x-req-origin-type"] in ["be"]))`
	if auth["action"] != "Deny" || expr != want {
		t.Fatalf("got %s %s", auth["action"], expr)
	}
	r.Access.Rules = []AccessRule{{From: []AccessSource{{Namespaces: []string{"web"}}}}}
	if _, _, err := agentgatewayAccess(r); err == nil {
		t.Fatal("caller rules must be refused on agentgateway")
	}
}

func TestBuildRouteMatch(t *testing.T) {
	m, err := buildRouteMatch(RouteMatch{PathType: "RegularExpression", Path: "^/reviews/[0-9]+$", Method: "GET", Headers: []RouteMatchKV{{Name: "x-env", Type: "RegularExpression", Value: "^canary"}}, Query: []RouteMatchKV{{Name: "v", Value: "2"}}})
	if err != nil {
		t.Fatal(err)
	}
	if M(m["path"])["type"] != "RegularExpression" || m["method"] != "GET" || len(m["headers"].([]any)) != 1 || len(m["queryParams"].([]any)) != 1 {
		t.Fatalf("%+v", m)
	}
	if _, err := buildRouteMatch(RouteMatch{Headers: []RouteMatchKV{{Name: "x", Type: "RegularExpression", Value: "("}}}); err == nil {
		t.Fatal("bad regex accepted")
	}
	if _, err := buildRouteMatch(RouteMatch{}); err == nil {
		t.Fatal("empty match accepted")
	}
}
