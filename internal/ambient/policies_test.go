package ambient

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/store"
)

// polWS builds a two-cluster workspace. Both clusters get the "app"
// namespace (ambient, waypoint wp), Services front/stable/canary and a Pod
// behind front. Tests add policies on top.
func polWS(t *testing.T) *store.Workspace {
	t.Helper()
	ws := store.New()
	t.Cleanup(func() { ws.Close() })
	ws.SetHeader(&bundle.Header{Contexts: []string{"c1", "c2"}})
	for _, cl := range []string{"c1", "c2"} {
		n := object("Namespace", "app", nil)
		M(n["metadata"])["namespace"] = ""
		M(n["metadata"])["labels"] = map[string]any{"istio.io/dataplane-mode": "ambient", "istio.io/use-waypoint": "wp"}
		polPut(ws, cl, "", "namespaces", "Namespace", n)
		var svcs []map[string]any
		for _, name := range []string{"front", "stable", "canary"} {
			svcs = append(svcs, object("Service", name, map[string]any{"selector": map[string]any{"app": name}, "ports": []any{map[string]any{"port": 80}}}))
		}
		polPut(ws, cl, "", "services", "Service", svcs...)
		wp := object("Gateway", "wp", map[string]any{"gatewayClassName": "istio-waypoint"})
		wp["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Programmed", "status": "True"}}}
		polPut(ws, cl, GatewayGroup, "gateways", "Gateway", wp)
		gc := object("GatewayClass", "istio-waypoint", map[string]any{"controllerName": "istio.io/mesh-controller"})
		M(gc["metadata"])["namespace"] = ""
		polPut(ws, cl, GatewayGroup, "gatewayclasses", "GatewayClass", gc)
		pod := object("Pod", "front-1", map[string]any{})
		M(pod["metadata"])["labels"] = map[string]any{"app": "front"}
		polPut(ws, cl, "", "pods", "Pod", pod)
	}
	return ws
}

func polPut(ws *store.Workspace, cl, g, res, kind string, objs ...map[string]any) {
	r := bundle.APIResource{Group: g, Version: "v1", Resource: res, Kind: kind, Namespaced: res != "namespaces" && res != "gatewayclasses"}
	ws.AddObjects(cl, r, objs, "fixture-"+cl+"-"+res+"-"+objs[0]["metadata"].(map[string]any)["name"].(string))
	ws.SetCoverage(bundle.Coverage{ID: cl + res, Cluster: cl, Type: bundle.CoverageResource, Group: g, Version: "v1", Resource: res, State: bundle.StateComplete})
}

func polAuthz(name, action string, spec map[string]any) map[string]any {
	spec["action"] = action
	return object("AuthorizationPolicy", name, spec)
}

func polSvcTarget(name string) []any {
	return []any{map[string]any{"group": "", "kind": "Service", "name": name}}
}

func polRoute(name string, backends ...any) map[string]any {
	r := object("HTTPRoute", name, map[string]any{
		"parentRefs": []any{map[string]any{"group": "", "kind": "Service", "name": "front", "port": 80}},
		"rules":      []any{map[string]any{"backendRefs": backends}},
	})
	r["status"] = map[string]any{"parents": []any{map[string]any{"conditions": []any{map[string]any{"type": "Accepted", "status": "True"}}}}}
	return r
}

func polBackend(name string, w int) any {
	return map[string]any{"name": name, "port": 80, "weight": w}
}

func polRow(t *testing.T, r *PoliciesResponse, name string) PolicyRow {
	t.Helper()
	for _, x := range r.Rows {
		if x.Ref.Name == name {
			return x
		}
	}
	names := []string{}
	for _, x := range r.Rows {
		names = append(names, x.Ref.Name)
	}
	t.Fatalf("row %s not in %v", name, names)
	return PolicyRow{}
}

func polHasProblem(r PolicyRow, sub string) bool {
	for _, p := range r.Problems {
		if strings.Contains(p, sub) {
			return true
		}
	}
	return false
}

func TestPolicyRows(t *testing.T) {
	denyRed := polAuthz("deny-red", "DENY", map[string]any{
		"targetRefs": polSvcTarget("front"),
		"rules":      []any{map[string]any{"when": []any{map[string]any{"key": "request.headers[color]", "values": []any{"red"}}}}},
	})
	cases := []struct {
		name  string
		setup func(ws *store.Workspace)
		row   string
		check func(t *testing.T, r PolicyRow)
	}{
		{"deny on a service", func(ws *store.Workspace) {
			polPut(ws, "c1", "security.istio.io", "authorizationpolicies", "AuthorizationPolicy", denyRed)
		}, "deny-red", func(t *testing.T, r PolicyRow) {
			if r.AppliesTo.Kind != "service" || r.AppliesTo.Phrase != "Service front" || len(r.AppliesTo.Targets) != 1 || r.AppliesTo.Targets[0].Name != "front" {
				t.Errorf("appliesTo %+v", r.AppliesTo)
			}
			if r.Effect.Action != "DENY" || r.Effect.Phrase != "header color = red" || r.Type != "Access" || r.EnforcedBy != "waypoint" {
				t.Errorf("effect %+v %s %s", r.Effect, r.Type, r.EnforcedBy)
			}
			if !r.CanTest || !r.Editable || r.Svc == nil || r.Svc.Name != "front" || r.EditKey != "AuthorizationPolicy:app/deny-red#0" {
				t.Errorf("links %+v %v %v %s", r.Svc, r.CanTest, r.Editable, r.EditKey)
			}
			if r.PerCluster["c1"] != "applied" || r.PerCluster["c2"] != "missing" || !r.Drift {
				t.Errorf("clusters %+v drift %v", r.PerCluster, r.Drift)
			}
			if len(r.Services) != 1 || r.Services[0] != "front" {
				t.Errorf("services %v", r.Services)
			}
		}},
		{"allow with no rules denies everything", func(ws *store.Workspace) {
			polPut(ws, "c1", "security.istio.io", "authorizationpolicies", "AuthorizationPolicy", polAuthz("lock", "ALLOW", map[string]any{}))
		}, "lock", func(t *testing.T, r PolicyRow) {
			if r.AppliesTo.Kind != "namespace" || r.AppliesTo.Phrase != "Namespace app" || r.EnforcedBy != "ztunnel" || r.CanTest || r.Editable {
				t.Errorf("%+v %s %v %v", r.AppliesTo, r.EnforcedBy, r.CanTest, r.Editable)
			}
			if !strings.Contains(r.Effect.Phrase, "default deny") {
				t.Errorf("effect %+v", r.Effect)
			}
		}},
		{"selector with pods", func(ws *store.Workspace) {
			polPut(ws, "c1", "security.istio.io", "authorizationpolicies", "AuthorizationPolicy", polAuthz("l4", "ALLOW", map[string]any{
				"selector": map[string]any{"matchLabels": map[string]any{"app": "front"}},
				"rules":    []any{map[string]any{"from": []any{map[string]any{"source": map[string]any{"namespaces": []any{"app"}}}}}},
			}))
		}, "l4", func(t *testing.T, r PolicyRow) {
			if r.AppliesTo.Kind != "pods" || r.AppliesTo.Phrase != "Pods app=front" || r.EnforcedBy != "ztunnel" || len(r.Services) != 1 {
				t.Errorf("%+v %s %v", r.AppliesTo, r.EnforcedBy, r.Services)
			}
		}},
		{"targets nothing: missing Service", func(ws *store.Workspace) {
			polPut(ws, "c1", "security.istio.io", "authorizationpolicies", "AuthorizationPolicy", polAuthz("orphan", "ALLOW", map[string]any{"targetRefs": polSvcTarget("ghost"), "rules": []any{map[string]any{}}}))
		}, "orphan", func(t *testing.T, r PolicyRow) {
			if r.AppliesTo.Kind != "nothing" || r.AppliesTo.Reason != "Service ghost does not exist" || len(r.AppliesTo.Targets) != 1 {
				t.Errorf("%+v", r.AppliesTo)
			}
			if r.PerCluster["c1"] != "not-enforced" || !polHasProblem(r, "Targets nothing") || r.CanTest || r.Svc != nil {
				t.Errorf("%+v %v %v", r.PerCluster, r.Problems, r.CanTest)
			}
		}},
		{"targets nothing: missing Gateway", func(ws *store.Workspace) {
			polPut(ws, "c1", "security.istio.io", "authorizationpolicies", "AuthorizationPolicy", polAuthz("no-gw", "ALLOW", map[string]any{
				"targetRefs": []any{map[string]any{"group": GatewayGroup, "kind": "Gateway", "name": "gone"}}, "rules": []any{map[string]any{}}}))
		}, "no-gw", func(t *testing.T, r PolicyRow) {
			if r.AppliesTo.Kind != "nothing" || r.AppliesTo.Reason != "Gateway gone does not exist" {
				t.Errorf("%+v", r.AppliesTo)
			}
		}},
		{"targets nothing: selector matches no Pods", func(ws *store.Workspace) {
			polPut(ws, "c1", "security.istio.io", "authorizationpolicies", "AuthorizationPolicy", polAuthz("no-pods", "ALLOW", map[string]any{
				"selector": map[string]any{"matchLabels": map[string]any{"app": "nobody"}}, "rules": []any{map[string]any{}}}))
		}, "no-pods", func(t *testing.T, r PolicyRow) {
			if r.AppliesTo.Kind != "nothing" || !strings.Contains(r.AppliesTo.Reason, "app=nobody matches no Pods") {
				t.Errorf("%+v", r.AppliesTo)
			}
		}},
		{"targets waypoint", func(ws *store.Workspace) {
			polPut(ws, "c1", "security.istio.io", "authorizationpolicies", "AuthorizationPolicy", polAuthz("wp-deny", "DENY", map[string]any{
				"targetRefs": []any{map[string]any{"group": GatewayGroup, "kind": "Gateway", "name": "wp"}}, "rules": []any{map[string]any{"to": []any{map[string]any{"operation": map[string]any{"methods": []any{"DELETE"}}}}}}}))
		}, "wp-deny", func(t *testing.T, r PolicyRow) {
			if r.AppliesTo.Kind != "waypoint" || r.AppliesTo.Phrase != "Waypoint wp" || len(r.Services) != 3 || r.PerCluster["c1"] != "applied" {
				t.Errorf("%+v %v %v", r.AppliesTo, r.Services, r.PerCluster)
			}
		}},
		{"split route", func(ws *store.Workspace) {
			polPut(ws, "c1", GatewayGroup, "httproutes", "HTTPRoute", polRoute("split", polBackend("stable", 80), polBackend("canary", 20)))
		}, "split", func(t *testing.T, r PolicyRow) {
			if r.Effect.Action != "Split" || r.Effect.Phrase != "Split stable 80 / canary 20" || r.Type != "Routing" || r.PerCluster["c1"] != "accepted" {
				t.Errorf("%+v %s %v", r.Effect, r.Type, r.PerCluster)
			}
			if r.AppliesTo.Phrase != "Service front :80" || !r.CanTest || r.AppliesTo.Kind != "service" {
				t.Errorf("%+v", r.AppliesTo)
			}
		}},
		{"route to a missing parent and backend", func(ws *store.Workspace) {
			r := polRoute("lost", polBackend("nothere", 1))
			M(r["spec"])["parentRefs"] = []any{map[string]any{"group": "", "kind": "Service", "name": "ghost"}}
			polPut(ws, "c1", GatewayGroup, "httproutes", "HTTPRoute", r)
		}, "lost", func(t *testing.T, r PolicyRow) {
			if r.AppliesTo.Kind != "nothing" || r.AppliesTo.Reason != "Service ghost does not exist" || r.PerCluster["c1"] != "not-enforced" {
				t.Errorf("%+v %v", r.AppliesTo, r.PerCluster)
			}
			if !polHasProblem(r, "Backend Service nothere does not exist") {
				t.Errorf("problems %v", r.Problems)
			}
		}},
		{"route not accepted", func(ws *store.Workspace) {
			r := polRoute("bad", polBackend("stable", 1))
			r["status"] = map[string]any{"parents": []any{map[string]any{"conditions": []any{map[string]any{"type": "Accepted", "status": "False", "reason": "NotAllowed"}}}}}
			polPut(ws, "c1", GatewayGroup, "httproutes", "HTTPRoute", r)
		}, "bad", func(t *testing.T, r PolicyRow) {
			if r.PerCluster["c1"] != "not-enforced" || !polHasProblem(r, "NotAllowed") {
				t.Errorf("%v %v", r.PerCluster, r.Problems)
			}
		}},
		{"peer authentication in the root namespace is mesh wide", func(ws *store.Workspace) {
			p := object("PeerAuthentication", "default", map[string]any{"mtls": map[string]any{"mode": "STRICT"}})
			M(p["metadata"])["namespace"] = "istio-system"
			polPut(ws, "c1", "security.istio.io", "peerauthentications", "PeerAuthentication", p)
		}, "default", func(t *testing.T, r PolicyRow) {
			if r.AppliesTo.Kind != "mesh" || r.AppliesTo.Phrase != "Whole mesh" || r.Effect.Action != "STRICT" || r.Effect.Phrase != "mTLS STRICT" || r.Type != "Transport" || r.EnforcedBy != "ztunnel" {
				t.Errorf("%+v %+v %s", r.AppliesTo, r.Effect, r.Type)
			}
			if r.Editable || r.CanTest {
				t.Errorf("mesh-wide objects are not edited from a Service")
			}
		}},
		{"destination rule with outlier detection", func(ws *store.Workspace) {
			polPut(ws, "c1", "networking.istio.io", "destinationrules", "DestinationRule", object("DestinationRule", "front-dr", map[string]any{
				"host": "front", "trafficPolicy": map[string]any{"outlierDetection": map[string]any{"consecutive5xxErrors": 3}}}))
		}, "front-dr", func(t *testing.T, r PolicyRow) {
			if r.Effect.Action != "outlier" || r.Effect.Phrase != "Eject a Pod after 3 errors" || r.Type != "Resilience" || r.AppliesTo.Phrase != "Service front" || r.EnforcedBy != "waypoint" {
				t.Errorf("%+v %s %+v %s", r.Effect, r.Type, r.AppliesTo, r.EnforcedBy)
			}
		}},
		{"destination rule for a missing Service", func(ws *store.Workspace) {
			polPut(ws, "c1", "networking.istio.io", "destinationrules", "DestinationRule", object("DestinationRule", "ghost-dr", map[string]any{
				"host": "ghost.app.svc.cluster.local", "trafficPolicy": map[string]any{"loadBalancer": map[string]any{"simple": "ROUND_ROBIN"}}}))
		}, "ghost-dr", func(t *testing.T, r PolicyRow) {
			if r.AppliesTo.Kind != "nothing" || r.AppliesTo.Reason != "Service ghost does not exist" {
				t.Errorf("%+v", r.AppliesTo)
			}
		}},
		{"waypoint gateway", func(ws *store.Workspace) {}, "wp", func(t *testing.T, r PolicyRow) {
			if r.Type != "Waypoints" || r.AppliesTo.Kind != "namespace" || r.AppliesTo.Phrase != "Namespace app (use-waypoint label)" || r.Effect.Phrase != "Istio waypoint, 3 Services" {
				t.Errorf("%+v %+v", r.AppliesTo, r.Effect)
			}
			if r.PerCluster["c1"] != "programmed" || r.PerCluster["c2"] != "programmed" || r.Drift {
				t.Errorf("%+v %v", r.PerCluster, r.Drift)
			}
		}},
		{"agentgateway policy on an Istio waypoint is not enforced", func(ws *store.Workspace) {
			p := object("AgentgatewayPolicy", "rl", map[string]any{
				"targetRefs": []any{map[string]any{"group": GatewayGroup, "kind": "Gateway", "name": "wp"}},
				"traffic":    map[string]any{"rateLimit": map[string]any{"local": []any{map[string]any{"requests": 10, "unit": "Minutes"}}}}})
			polPut(ws, "c1", "agentgateway.dev", "agentgatewaypolicies", "AgentgatewayPolicy", p)
		}, "rl", func(t *testing.T, r PolicyRow) {
			if r.Type != "Resilience" || r.Effect.Action != "Rate limit" || r.PerCluster["c1"] != "not-enforced" || !polHasProblem(r, "Istio waypoint") {
				t.Errorf("%+v %s %v %v", r.Effect, r.Type, r.PerCluster, r.Problems)
			}
		}},
		{"agentgateway policy for an ingress gateway is left out", func(ws *store.Workspace) {
			ing := object("Gateway", "ingress", map[string]any{"gatewayClassName": "agentgateway"})
			polPut(ws, "c1", GatewayGroup, "gateways", "Gateway", object("Gateway", "wp", map[string]any{"gatewayClassName": "istio-waypoint"}), ing)
			p := object("AgentgatewayPolicy", "edge", map[string]any{"targetRefs": []any{map[string]any{"group": GatewayGroup, "kind": "Gateway", "name": "ingress"}}, "traffic": map[string]any{}})
			polPut(ws, "c1", "agentgateway.dev", "agentgatewaypolicies", "AgentgatewayPolicy", p)
		}, "", func(t *testing.T, _ PolicyRow) {}},
		{"flightbay owned envoy filter", func(ws *store.Workspace) {
			ef := object("EnvoyFilter", "front-timeout", map[string]any{})
			M(ef["metadata"])["labels"] = map[string]any{"app.kubernetes.io/managed-by": "flightbay", "flightbay.dev/test-id": "t1", "flightbay.dev/resilience-service": "front"}
			M(ef["metadata"])["annotations"] = map[string]any{"flightbay.dev/resilience-request": `{"tasks":["timeouts"],"timeout":"5s","resilience":{"scope":"service"}}`}
			polPut(ws, "c1", "networking.istio.io", "envoyfilters", "EnvoyFilter", ef, object("EnvoyFilter", "someone-elses", map[string]any{}))
		}, "front-timeout", func(t *testing.T, r PolicyRow) {
			if r.Type != "Resilience" || r.Effect.Action != "Timeout" || r.Effect.Phrase != "Request timeout 5s" || r.AppliesTo.Phrase != "Service front" || r.ManagedBy != "flightbay" {
				t.Errorf("%+v %s %+v %s", r.Effect, r.Type, r.AppliesTo, r.ManagedBy)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws := polWS(t)
			tc.setup(ws)
			out := NewPolicies(ws, PolicyFilter{})
			if tc.row == "" {
				for _, r := range out.Rows {
					if r.Ref.Name == "edge" || r.Ref.Name == "ingress" {
						t.Fatalf("%s should not be listed", r.Ref.Name)
					}
				}
				return
			}
			tc.check(t, polRow(t, out, tc.row))
		})
	}
}

func TestPolicyDrift(t *testing.T) {
	ws := polWS(t)
	a := polRoute("shared", polBackend("stable", 80), polBackend("canary", 20))
	b := polRoute("shared", polBackend("stable", 50), polBackend("canary", 50))
	same1, same2 := polAuthz("same", "DENY", map[string]any{"targetRefs": polSvcTarget("front"), "rules": []any{map[string]any{}}}), polAuthz("same", "DENY", map[string]any{"targetRefs": polSvcTarget("front"), "rules": []any{map[string]any{}}})
	polPut(ws, "c1", GatewayGroup, "httproutes", "HTTPRoute", a)
	polPut(ws, "c2", GatewayGroup, "httproutes", "HTTPRoute", b)
	polPut(ws, "c1", "security.istio.io", "authorizationpolicies", "AuthorizationPolicy", same1)
	polPut(ws, "c2", "security.istio.io", "authorizationpolicies", "AuthorizationPolicy", same2)
	polPut(ws, "c1", "security.istio.io", "authorizationpolicies", "AuthorizationPolicy", polAuthz("only-c1", "DENY", map[string]any{"targetRefs": polSvcTarget("front"), "rules": []any{map[string]any{}}}))
	out := NewPolicies(ws, PolicyFilter{})
	if r := polRow(t, out, "shared"); r.PerCluster["c1"] != "accepted" || r.PerCluster["c2"] != "differs" || !r.Drift {
		t.Errorf("shared %+v %v", r.PerCluster, r.Drift)
	}
	if r := polRow(t, out, "same"); r.PerCluster["c1"] != "applied" || r.PerCluster["c2"] != "applied" || r.Drift {
		t.Errorf("same %+v %v", r.PerCluster, r.Drift)
	}
	if r := polRow(t, out, "only-c1"); r.PerCluster["c2"] != "missing" || !r.Drift {
		t.Errorf("only-c1 %+v", r.PerCluster)
	}
	// Status and metadata noise is not drift.
	same2["status"] = map[string]any{"conditions": []any{}}
	drift := NewPolicies(ws, PolicyFilter{Drift: true})
	got := []string{}
	for _, r := range drift.Rows {
		got = append(got, r.Ref.Name)
	}
	// wp is on both clusters and identical, so it is not drift either.
	if strings.Join(got, ",") != "shared,only-c1" {
		t.Errorf("drift rows %v", got)
	}
	if drift.Counts.Drift != 2 || drift.Counts.Shown != 2 || drift.Counts.All != out.Counts.All {
		t.Errorf("counts %+v", drift.Counts)
	}
}

func TestPolicyDriftIgnoresClustersWithoutTheNamespace(t *testing.T) {
	ws := polWS(t)
	o := object("AuthorizationPolicy", "elsewhere", map[string]any{"rules": []any{map[string]any{}}})
	M(o["metadata"])["namespace"] = "lonely"
	polPut(ws, "c1", "security.istio.io", "authorizationpolicies", "AuthorizationPolicy", o)
	r := polRow(t, NewPolicies(ws, PolicyFilter{}), "elsewhere")
	if r.PerCluster["c2"] != "missing" || r.Drift {
		t.Errorf("%+v drift %v", r.PerCluster, r.Drift)
	}
}

func TestPolicyManagedBy(t *testing.T) {
	ws := polWS(t)
	mk := func(name string, labels, annotations map[string]any) map[string]any {
		o := polAuthz(name, "ALLOW", map[string]any{"targetRefs": polSvcTarget("front"), "rules": []any{map[string]any{}}})
		if labels != nil {
			M(o["metadata"])["labels"] = labels
		}
		if annotations != nil {
			M(o["metadata"])["annotations"] = annotations
		}
		return o
	}
	polPut(ws, "c1", "security.istio.io", "authorizationpolicies", "AuthorizationPolicy",
		mk("by-fb", map[string]any{"app.kubernetes.io/managed-by": "flightbay"}, nil),
		mk("by-helm", nil, map[string]any{"meta.helm.sh/release-name": "x"}),
		mk("by-argo", map[string]any{"argocd.argoproj.io/instance": "x"}, nil),
		mk("by-flux", map[string]any{"kustomize.toolkit.fluxcd.io/name": "x"}, nil),
		mk("by-kubectl", nil, map[string]any{"kubectl.kubernetes.io/last-applied-configuration": "{}"}),
		mk("by-nobody", nil, nil))
	out := NewPolicies(ws, PolicyFilter{})
	for name, want := range map[string]string{"by-fb": "flightbay", "by-helm": "helm", "by-argo": "argocd", "by-flux": "flux", "by-kubectl": "kubectl", "by-nobody": "unknown"} {
		if got := polRow(t, out, name).ManagedBy; got != want {
			t.Errorf("%s managed by %q, want %q", name, got, want)
		}
	}
	only := NewPolicies(ws, PolicyFilter{ManagedBy: []string{"Flightbay", "helm"}})
	if len(only.Rows) != 2 || only.Rows[0].ManagedBy == "" {
		t.Errorf("filter left %d rows", len(only.Rows))
	}
}

func TestPolicyFiltersAndCounts(t *testing.T) {
	ws := polWS(t)
	polPut(ws, "c1", "security.istio.io", "authorizationpolicies", "AuthorizationPolicy",
		polAuthz("deny-red", "DENY", map[string]any{"targetRefs": polSvcTarget("front"), "rules": []any{map[string]any{"when": []any{map[string]any{"key": "request.headers[color]", "values": []any{"red"}}}}}}),
		polAuthz("orphan", "ALLOW", map[string]any{"targetRefs": polSvcTarget("ghost"), "rules": []any{map[string]any{}}}))
	polPut(ws, "c1", GatewayGroup, "httproutes", "HTTPRoute", polRoute("split", polBackend("stable", 80), polBackend("canary", 20)))
	pa := object("PeerAuthentication", "default", map[string]any{"mtls": map[string]any{"mode": "PERMISSIVE"}})
	M(pa["metadata"])["namespace"] = "istio-system"
	polPut(ws, "c1", "security.istio.io", "peerauthentications", "PeerAuthentication", pa)
	other := polAuthz("elsewhere", "ALLOW", map[string]any{"rules": []any{map[string]any{}}})
	M(other["metadata"])["namespace"] = "other"
	polPut(ws, "c1", "security.istio.io", "authorizationpolicies", "AuthorizationPolicy", other)

	names := func(f PolicyFilter) string {
		var ns []string
		for _, r := range NewPolicies(ws, f).Rows {
			ns = append(ns, r.Ref.Name)
		}
		return strings.Join(ns, ",")
	}
	cases := []struct {
		name string
		f    PolicyFilter
		want string
	}{
		{"everything", PolicyFilter{}, "wp,split,deny-red,orphan,default,elsewhere"},
		{"namespace keeps mesh-wide objects", PolicyFilter{Namespaces: []string{"app"}}, "wp,split,deny-red,orphan,default"},
		{"another namespace", PolicyFilter{Namespaces: []string{"other"}}, "default,elsewhere"},
		{"type", PolicyFilter{Types: []string{"routing"}}, "split"},
		{"two types", PolicyFilter{Types: []string{"Routing", "Waypoints"}}, "wp,split"},
		{"problems", PolicyFilter{Problems: true}, "orphan"},
		{"text on effect", PolicyFilter{Q: "COLOR"}, "deny-red"},
		{"text on service", PolicyFilter{Q: "front"}, "split,deny-red"},
		{"text on kind", PolicyFilter{Q: "peerauth"}, "default"},
		{"text on target", PolicyFilter{Q: "ghost"}, "orphan"},
		{"combined", PolicyFilter{Namespaces: []string{"app"}, Types: []string{"Access"}, Problems: true}, "orphan"},
		{"no match", PolicyFilter{Q: "zzz"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := names(tc.f); got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
	all := NewPolicies(ws, PolicyFilter{Types: []string{"Access"}})
	c := all.Counts
	if c.All != 6 || c.Shown != 3 || c.Problems != 1 || c.ByType["Access"] != 3 || c.ByType["Routing"] != 1 || c.ByType["Waypoints"] != 1 || c.ByType["Transport"] != 1 || c.ByType["Resilience"] != 0 {
		t.Errorf("counts %+v", c)
	}
	var ns []string
	for _, n := range c.Namespaces {
		ns = append(ns, n.Name+"="+string(rune('0'+n.Count)))
	}
	if strings.Join(ns, ",") != "app=4,istio-system=1,other=1" {
		t.Errorf("namespaces %v", ns)
	}
	if strings.Join(all.Clusters, ",") != "c1,c2" {
		t.Errorf("clusters %v", all.Clusters)
	}
	if len(all.Groups) != 0 {
		t.Errorf("groups only come with group=service")
	}
}

func TestPolicyGrouping(t *testing.T) {
	ws := polWS(t)
	polPut(ws, "c1", "security.istio.io", "authorizationpolicies", "AuthorizationPolicy",
		polAuthz("deny-front", "DENY", map[string]any{"targetRefs": polSvcTarget("front"), "rules": []any{map[string]any{}}}),
		polAuthz("orphan", "ALLOW", map[string]any{"targetRefs": polSvcTarget("ghost"), "rules": []any{map[string]any{}}}),
		polAuthz("l4", "ALLOW", map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "front"}}, "rules": []any{map[string]any{}}}),
		polAuthz("ns-lock", "ALLOW", map[string]any{}),
		polAuthz("deny-stable", "DENY", map[string]any{"targetRefs": polSvcTarget("stable"), "rules": []any{map[string]any{}}}))
	polPut(ws, "c1", GatewayGroup, "httproutes", "HTTPRoute", polRoute("split", polBackend("stable", 80), polBackend("canary", 20)))
	pa := object("PeerAuthentication", "default", map[string]any{"mtls": map[string]any{"mode": "PERMISSIVE"}})
	M(pa["metadata"])["namespace"] = "istio-system"
	polPut(ws, "c1", "security.istio.io", "peerauthentications", "PeerAuthentication", pa)

	out := NewPolicies(ws, PolicyFilter{Group: "service"})
	type g struct{ key, title, kind, rows string }
	var got []g
	for _, x := range out.Groups {
		var rs []string
		for _, r := range x.Rows {
			rs = append(rs, r.Ref.Name)
		}
		got = append(got, g{x.Key, x.Title, x.Kind, strings.Join(rs, ",")})
	}
	want := []g{
		{"service:app/front", "app/front", "service", "split,deny-front,l4"},
		{"service:app/stable", "app/stable", "service", "deny-stable"},
		{"namespace:app", "Namespace app", "namespace", "wp,ns-lock,orphan"},
		{"mesh", "Whole mesh", "mesh", "default"},
	}
	if len(got) != len(want) {
		t.Fatalf("groups %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("group %d: %+v want %+v", i, got[i], want[i])
		}
	}
	if out.Groups[0].Svc == nil || out.Groups[0].Svc.Name != "front" || out.Groups[2].Svc != nil {
		t.Errorf("group svc %+v / %+v", out.Groups[0].Svc, out.Groups[2].Svc)
	}
	n := 0
	for _, x := range out.Groups {
		n += len(x.Rows)
	}
	if n != len(out.Rows) {
		t.Errorf("groups hold %d rows, rows %d", n, len(out.Rows))
	}
	if obj := NewPolicies(ws, PolicyFilter{Group: "object"}); len(obj.Groups) != 0 {
		t.Errorf("group=object returned groups")
	}
}

// Every object servicePolicies() returns for any Service shows up as a row.
func TestPolicyRowsCoverServicePolicies(t *testing.T) {
	ws := polWS(t)
	polPut(ws, "c1", "security.istio.io", "authorizationpolicies", "AuthorizationPolicy",
		polAuthz("a", "DENY", map[string]any{"targetRefs": polSvcTarget("front"), "rules": []any{map[string]any{}}}),
		polAuthz("b", "ALLOW", map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "front"}}, "rules": []any{map[string]any{}}}),
		polAuthz("c", "ALLOW", map[string]any{}))
	pa := object("PeerAuthentication", "default", map[string]any{"mtls": map[string]any{"mode": "STRICT"}})
	M(pa["metadata"])["namespace"] = "istio-system"
	polPut(ws, "c1", "security.istio.io", "peerauthentications", "PeerAuthentication", pa)
	out := NewPolicies(ws, PolicyFilter{})
	have := map[string]bool{}
	for _, r := range out.Rows {
		have[r.Kind+"/"+r.Ref.Namespace+"/"+r.Ref.Name] = true
	}
	for _, svc := range ws.List("c1", "", "services", "", nil) {
		for _, e := range servicePolicies(ws, svc) {
			if !have[e.Ref.Kind+"/"+e.Ref.Namespace+"/"+e.Ref.Name] {
				t.Errorf("%s/%s reaches %s but has no row", e.Ref.Kind, e.Ref.Name, svc.Name)
			}
		}
	}
}

func TestPolicyJSONShape(t *testing.T) {
	ws := polWS(t)
	polPut(ws, "c1", "security.istio.io", "authorizationpolicies", "AuthorizationPolicy",
		polAuthz("deny", "DENY", map[string]any{"targetRefs": polSvcTarget("front"), "rules": []any{map[string]any{}}}))
	b, err := json.Marshal(NewPolicies(ws, PolicyFilter{Group: "service"}))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for _, k := range []string{"rows", "groups", "counts", "clusters"} {
		if m[k] == nil {
			t.Errorf("%s missing", k)
		}
	}
	row := M(L(m["rows"])[0])
	for _, k := range []string{"ref", "kind", "appliesTo", "effect", "type", "enforcedBy", "perCluster", "managedBy", "services", "problems", "drift", "canTest", "editable", "editKey"} {
		if _, ok := row[k]; !ok {
			t.Errorf("row.%s missing", k)
		}
	}
	for _, k := range []string{"all", "problems", "drift", "byType", "namespaces"} {
		if _, ok := M(m["counts"])[k]; !ok {
			t.Errorf("counts.%s missing", k)
		}
	}
}

// The preview bundle has bookinfo/reviews with a DENY AuthorizationPolicy and
// a weighted HTTPRoute on kind-mesh2 only.
func TestRealPolicies(t *testing.T) {
	ws := realBundle(t)
	out := NewPolicies(ws, PolicyFilter{Namespaces: []string{"bookinfo"}, Group: "service"})
	var deny, route *PolicyRow
	for i, r := range out.Rows {
		switch r.Kind {
		case "AuthorizationPolicy":
			deny = &out.Rows[i]
		case "HTTPRoute":
			route = &out.Rows[i]
		}
	}
	if deny == nil || route == nil {
		t.Fatalf("rows %d", len(out.Rows))
	}
	if deny.Effect.Action != "DENY" || deny.Effect.Phrase != "header color = red" || deny.AppliesTo.Phrase != "Service reviews" || deny.EnforcedBy != "waypoint" {
		t.Errorf("deny %+v %+v", deny.Effect, deny.AppliesTo)
	}
	if deny.PerCluster["kind-mesh2"] != "applied" || deny.PerCluster["kind-mesh1"] != "missing" || !deny.Drift || !deny.CanTest || !deny.Editable || deny.ManagedBy != "flightbay" {
		t.Errorf("deny %+v", deny)
	}
	if route.Effect.Action != "Split" || route.PerCluster["kind-mesh2"] != "accepted" || route.PerCluster["kind-mesh1"] != "missing" || route.AppliesTo.Phrase != "Service reviews :9080" || !route.CanTest {
		t.Errorf("route %+v", route)
	}
	if out.Groups[0].Key != "service:bookinfo/reviews" || out.Groups[0].Svc.Name != "reviews" {
		t.Errorf("groups %+v", out.Groups[0])
	}
	if len(out.Clusters) != 2 {
		t.Errorf("clusters %v", out.Clusters)
	}
}
