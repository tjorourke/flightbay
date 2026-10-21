package ambient

import (
	"strings"
	"testing"

	"github.com/tjorourke/flightbay/internal/store"
)

func capturePlanObjects(t *testing.T, ws *store.Workspace, p *Plan) {
	t.Helper()
	resources := map[string]string{"HTTPRoute": "httproutes", "AuthorizationPolicy": "authorizationpolicies", "PeerAuthentication": "peerauthentications", "RequestAuthentication": "requestauthentications", "DestinationRule": "destinationrules", "AgentgatewayPolicy": "agentgatewaypolicies", "EnterpriseAgentgatewayPolicy": "enterpriseagentgatewaypolicies", "Gateway": "gateways", "Service": "services"}
	for _, original := range p.Objects {
		o := M(Copy(original))
		kind := S(o["kind"])
		M(o["metadata"])["uid"] = "uid-" + S(Path(o, "metadata", "name"))
		api := strings.Split(S(o["apiVersion"]), "/")
		group := ""
		if len(api) == 2 {
			group = api[0]
		}
		if resources[kind] == "" {
			t.Fatal("uncovered generated kind", kind)
		}
		put(ws, group, resources[kind], kind, o)
	}
}

func TestConfigureGeneratedResourcesAreReapplyable(t *testing.T) {
	cases := []struct {
		task string
		req  Request
	}{
		{"l4-default-deny", Request{}},
		{"strict-mtls", Request{MTLSMode: "STRICT"}},
		{"methods", Request{Methods: []string{"GET"}}},
		{"access-rule", Request{Access: &AccessRequest{Action: "DENY", Rules: []AccessRule{{Methods: []string{"POST"}}}}}},
		{"weighted-routing", Request{Backends: []Backend{{"stable", 80, 70}, {"canary", 80, 30}}}},
		{"timeouts", Request{Timeout: "5s"}},
		{"load-balancing", Request{Algorithm: "ROUND_ROBIN"}},
		{"endpoint-failover", Request{FailureThreshold: 3}},
		{"retries", Request{RetryAttempts: 2}},
		{"mirroring", Request{Mirror: &Backend{Name: "canary", Port: 80}}},
		{"header-routing", Request{Headers: map[string]string{"x-version": "canary"}, Backends: []Backend{{Name: "canary", Port: 80}}}},
		{"jwt", Request{Issuer: "https://issuer.example", JWKSURI: "https://issuer.example/keys", RequestPrincipals: []string{"https://issuer.example/*"}}},
		{"l4-allow-namespace", Request{SourceNamespace: "app"}},
		{"rate-limit", Request{Requests: 5, Unit: "Seconds"}},
		{"require-waypoint", Request{}},
		{"locality-failover", Request{FromRegion: "east", ToRegion: "west"}},
	}
	for _, tc := range cases {
		t.Run(tc.task, func(t *testing.T) {
			ws := fixture(t)
			put(ws, "security.istio.io", "requestauthentications", "RequestAuthentication")
			routeSchema(ws, []string{"retry", "attempts"}, []string{"filters", "requestMirror", "backendRef", "name"}, []string{"matches", "headers", "value"})
			if tc.task == "l4-allow-namespace" {
				delete(M(Path(Target(ws, "c", "Namespace", "", "app").Obj, "metadata", "labels")), "istio.io/use-waypoint")
			}
			if tc.task == "rate-limit" {
				M(Target(ws, "c", "Gateway", "app", "wp").Obj["spec"])["gatewayClassName"] = "enterprise-agentgateway-waypoint"
			}
			if tc.task == "require-waypoint" {
				pod := object("Pod", "wp-identity", map[string]any{"serviceAccountName": "waypoint-sa"})
				M(pod["metadata"])["labels"] = map[string]any{"gateway.networking.k8s.io/gateway-name": "wp"}
				put(ws, "", "pods", "Pod", pod)
				cm := object("ConfigMap", "istio", nil)
				cm["data"] = map[string]any{"mesh": "trustDomain: mesh.example\n"}
				put(ws, "", "configmaps", "ConfigMap", cm)
			}
			if tc.task == "locality-failover" {
				for _, region := range []string{"east", "west"} {
					slice := object("EndpointSlice", region, nil)
					M(slice["metadata"])["labels"] = map[string]any{"kubernetes.io/service-name": "front"}
					slice["endpoints"] = []any{map[string]any{"deprecatedTopology": map[string]any{"topology.kubernetes.io/region": region}}}
					put(ws, "discovery.k8s.io", "endpointslices", "EndpointSlice", slice)
				}
			}
			req := tc.req
			req.Cluster, req.Namespace, req.Service, req.Port, req.Tasks = "c", "app", "front", 80, []string{tc.task}
			p, err := Generate(ws, req)
			if err != nil || len(p.Objects) == 0 {
				t.Fatalf("initial plan: %v %+v", err, p)
			}
			capturePlanObjects(t, ws, p)
			again, err := Generate(ws, req)
			if err != nil || len(again.Objects) != 0 || len(again.Changes) != 0 {
				t.Fatalf("repeat must be a no-op: %v %+v", err, again)
			}
			// A field that differs is reviewed as an update of the same resource,
			// not a duplicate creation, on any laptop with this capture.
			first := p.Objects[0]
			current := Target(ws, req.Cluster, S(first["kind"]), req.Namespace, S(Path(first, "metadata", "name")))
			current.Obj["spec"] = map[string]any{"customerField": "keep"}
			if tc.task == "access-rule" {
				if _, err := Generate(ws, req); err == nil {
					t.Fatal("implicit destructive access replacement accepted")
				}
				return
			}
			updated, err := Generate(ws, req)
			if err != nil || len(updated.Objects) != 0 || len(updated.Changes) != 1 {
				t.Fatalf("update: %v %+v", err, updated)
			}
			if updated.Changes[0].UID != S(Path(current.Obj, "metadata", "uid")) {
				t.Fatal("update lost captured identity")
			}
		})
	}
}

func TestReconcilePreservesOwnershipAndRejectsForeignResources(t *testing.T) {
	ws, req := capturedGlobalSplit(t)
	route := Target(ws, "c", "HTTPRoute", "app", "front-cluster-split")
	labels := M(Path(route.Obj, "metadata", "labels"))
	delete(labels, "flightbay.dev/test-id")
	if _, err := Generate(ws, req); err == nil {
		t.Fatal("adopted a foreign split")
	}
}

func TestAccessAppendPreservesRawRulesAndRejectsStaleOrIncompatibleEdits(t *testing.T) {
	ws := fixture(t)
	old := object("AuthorizationPolicy", "existing", map[string]any{"action": "ALLOW", "targetRefs": []any{map[string]any{"group": "", "kind": "Service", "name": "front"}}, "rules": []any{map[string]any{"to": []any{map[string]any{"operation": map[string]any{"methods": []any{"GET"}}}}}, map[string]any{"futureField": map[string]any{"keep": true}}}})
	put(ws, SecurityGroup, "authorizationpolicies", "AuthorizationPolicy", old)
	o := Target(ws, "c", "AuthorizationPolicy", "app", "existing")
	req := Request{Cluster: "c", Namespace: "app", Service: "front", Port: 80, Tasks: []string{"access-rule"}, Access: &AccessRequest{Action: "ALLOW", Rules: []AccessRule{{Methods: []string{"POST"}}}}, Edit: &EditTarget{Ref: reference(o), UID: S(Path(o.Obj, "metadata", "uid")), Fingerprint: configurationFingerprint(o)}, AccessOperation: "append"}
	p, err := Generate(ws, req)
	if err != nil || len(p.Objects) != 0 || len(p.Changes) != 1 {
		t.Fatalf("%v %+v", err, p)
	}
	f := p.Changes[0].Fields[0]
	if f.Path != "/spec/rules" || len(L(f.Before)) != 2 || len(L(f.After)) != 3 || !Equal(L(f.After)[:2], L(Path(o.Obj, "spec", "rules"))) {
		t.Fatal(f)
	}
	M(o.Obj["spec"])["rules"] = Copy(f.After)
	req.Edit.Fingerprint = configurationFingerprint(o)
	if again, err := Generate(ws, req); err != nil || len(again.Changes) != 0 {
		t.Fatalf("duplicate append %v %+v", err, again)
	}
	req.Access.Action = "DENY"
	if _, err := Generate(ws, req); err == nil {
		t.Fatal("append changed action")
	}
	req.Access.Action = "ALLOW"
	req.Edit.Fingerprint = "stale"
	if _, err := Generate(ws, req); err == nil {
		t.Fatal("stale append accepted")
	}
}

func TestNewOwnedWaypointCanBeReapplied(t *testing.T) {
	ws := fixture(t)
	req := Request{Cluster: "c", Namespace: "app", Service: "front", Tasks: []string{"waypoint"}, Waypoint: &WaypointRequest{Mode: "new", Name: "owned-wp", Class: "istio-waypoint", Scope: "service"}}
	p, err := Generate(ws, req)
	if err != nil {
		t.Fatal(err)
	}
	capturePlanObjects(t, ws, p)
	again, err := Generate(ws, req)
	if err != nil || len(again.Objects) != 0 {
		t.Fatalf("owned waypoint recreation: %v %+v", err, again)
	}
}
