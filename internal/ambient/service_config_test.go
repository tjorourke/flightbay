package ambient

import (
	"strings"
	"testing"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/store"
)

func cfgTask(t *testing.T, c *ServiceConfig, id string) ConfigTask {
	t.Helper()
	for _, x := range c.Tasks {
		if x.ID == id {
			return x
		}
	}
	t.Fatalf("task %s missing from service-config", id)
	return ConfigTask{}
}

func TestJWTAndWaypointSummariesRespectAdditiveAllow(t *testing.T) {
	ws := fixture(t)
	withWaypointIdentity(ws)
	put(ws, SecurityGroup, "requestauthentications", "RequestAuthentication")
	req := Request{Cluster: "c", Namespace: "app", Service: "front", Port: 80, Tasks: []string{"jwt"}, Issuer: "https://issuer.example", JWKSURI: "https://issuer.example/keys", RequestPrincipals: []string{"https://issuer.example/alice"}, JWTServiceAccounts: []string{"app/client"}}
	p, err := Generate(ws, req)
	if err != nil {
		t.Fatal(err)
	}
	capturePlanObjects(t, ws, p)
	cfg, err := NewServiceConfig(ws, "c", "app", "front")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cfg.Answers.Access, "JWT") {
		t.Fatal(cfg.Answers.Access)
	}
	put(ws, SecurityGroup, "authorizationpolicies", "AuthorizationPolicy", object("AuthorizationPolicy", "tokenless", map[string]any{"targetRefs": []any{map[string]any{"group": "", "kind": "Service", "name": "front"}}, "rules": []any{map[string]any{}}}))
	cfg, err = NewServiceConfig(ws, "c", "app", "front")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cfg.Answers.Access, "Only callers with a valid JWT") {
		t.Fatal(cfg.Answers.Access)
	}
}

// withWaypointIdentity captures the waypoint's service account and the mesh
// trust domain, which the require-waypoint task needs before it is offered.
func withWaypointIdentity(ws *store.Workspace) {
	pod := object("Pod", "wp-1", map[string]any{"serviceAccountName": "waypoint-sa"})
	M(pod["metadata"])["labels"] = map[string]any{"gateway.networking.k8s.io/gateway-name": "wp"}
	put(ws, "", "pods", "Pod", pod)
	cm := object("ConfigMap", "istio", nil)
	cm["data"] = map[string]any{"mesh": "trustDomain: mesh.example\n"}
	put(ws, "", "configmaps", "ConfigMap", cm)
}

func denyRed() map[string]any {
	return object("AuthorizationPolicy", "deny-red", map[string]any{
		"action":     "DENY",
		"targetRefs": []any{map[string]any{"group": "", "kind": "Service", "name": "front"}},
		"rules":      []any{map[string]any{"when": []any{map[string]any{"key": "request.headers[color]", "values": []any{"red"}}}}},
	})
}

func frontRoute(backends ...any) map[string]any {
	r := object("HTTPRoute", "split", map[string]any{
		"parentRefs": []any{map[string]any{"group": "", "kind": "Service", "name": "front", "port": 80}},
		"rules":      []any{map[string]any{"backendRefs": backends}},
	})
	r["status"] = map[string]any{"parents": []any{map[string]any{
		"parentRef": map[string]any{"group": "", "kind": "Service", "name": "front", "port": 80},
		"conditions": []any{
			map[string]any{"type": "Accepted", "status": "True", "observedGeneration": 1},
			map[string]any{"type": "ResolvedRefs", "status": "True", "observedGeneration": 1},
		}}}}
	return r
}

func TestServiceConfig(t *testing.T) {
	cases := []struct {
		name  string
		setup func(ws *store.Workspace)
		check func(t *testing.T, c *ServiceConfig)
	}{
		{
			name:  "nothing set",
			setup: func(ws *store.Workspace) {},
			check: func(t *testing.T, c *ServiceConfig) {
				for _, id := range []string{"access-rule", "timeouts", "weighted-routing", "strict-mtls"} {
					if s := cfgTask(t, c, id).Status; s == "configured" || s == "inherited" {
						t.Errorf("%s is %s on an empty Service", id, s)
					}
				}
				if c.Answers.Access != "Anyone in the mesh" || !c.AnswerEmpty["resilience"] {
					t.Errorf("answers %+v", c.Answers)
				}
				if c.Multicluster != "" || len(c.Drift) != 0 {
					t.Errorf("one cluster should not report drift: %q", c.Multicluster)
				}
			},
		},
		{
			name: "DENY recommends the waypoint lock",
			setup: func(ws *store.Workspace) {
				withWaypointIdentity(ws)
				put(ws, "security.istio.io", "authorizationpolicies", "AuthorizationPolicy", denyRed())
			},
			check: func(t *testing.T, c *ServiceConfig) {
				a := cfgTask(t, c, "access-rule")
				if a.Status != "configured" || len(a.Resources) != 1 {
					t.Fatalf("access-rule %+v", a)
				}
				r := a.Resources[0]
				if r.Action != "DENY" || r.Label != "Block header color = red" || r.Summary != "Deny when request.headers[color] is red" || r.EditKey != "AuthorizationPolicy:app/deny-red#0" {
					t.Errorf("resource %+v", r)
				}
				if r.ManagedBy != "unknown" || r.State == "" {
					t.Errorf("managedBy/state %+v", r)
				}
				if c.Answers.Access != "Anyone in the mesh, except requests with <code>color: red</code>" {
					t.Errorf("answer %q", c.Answers.Access)
				}
				w := cfgTask(t, c, "require-waypoint")
				if w.Status != "recommended" || w.Hint == "" || w.Reason == "" {
					t.Errorf("require-waypoint %+v", w)
				}
			},
		},
		{
			name: "recommendation needs the waypoint identity",
			setup: func(ws *store.Workspace) {
				put(ws, "security.istio.io", "authorizationpolicies", "AuthorizationPolicy", denyRed())
			},
			check: func(t *testing.T, c *ServiceConfig) {
				w := cfgTask(t, c, "require-waypoint")
				if w.Status != "unavailable" || !strings.Contains(w.Reason, "captured identity") {
					t.Errorf("a task Flightbay cannot generate was recommended: %+v", w)
				}
			},
		},
		{
			name: "waypoint lock satisfies the recommendation",
			setup: func(ws *store.Workspace) {
				withWaypointIdentity(ws)
				lock := object("AuthorizationPolicy", "front-waypoint", map[string]any{
					"selector": map[string]any{"matchLabels": map[string]any{"app": "front"}},
					"action":   "ALLOW",
					"rules":    []any{map[string]any{"from": []any{map[string]any{"source": map[string]any{"principals": []any{"mesh.example/ns/app/sa/waypoint-sa"}}}}}},
				})
				put(ws, "security.istio.io", "authorizationpolicies", "AuthorizationPolicy", denyRed(), lock)
			},
			check: func(t *testing.T, c *ServiceConfig) {
				w := cfgTask(t, c, "require-waypoint")
				if w.Status != "configured" || len(w.Resources) != 1 || w.EnforcedBy != "ztunnel" {
					t.Errorf("%+v", w)
				}
				if c.Answers.Access != "Anyone in the mesh, except requests with <code>color: red</code>" && !strings.Contains(c.Answers.Access, "except") {
					t.Errorf("answer %q", c.Answers.Access)
				}
			},
		},
		{
			name: "mesh-wide PeerAuthentication is inherited",
			setup: func(ws *store.Workspace) {
				pa := object("PeerAuthentication", "default", map[string]any{"mtls": map[string]any{"mode": "PERMISSIVE"}})
				M(pa["metadata"])["namespace"] = "istio-system"
				put(ws, "security.istio.io", "peerauthentications", "PeerAuthentication", pa)
			},
			check: func(t *testing.T, c *ServiceConfig) {
				m := cfgTask(t, c, "strict-mtls")
				if m.Status != "inherited" || len(m.Resources) != 1 {
					t.Fatalf("%+v", m)
				}
				r := m.Resources[0]
				if r.Label != "mTLS permissive, from the mesh default" || r.Action != "Inherited" || r.Editable || r.ReadOnlyReason == "" {
					t.Errorf("%+v", r)
				}
				if !strings.Contains(m.Reason, "istio-system/default") {
					t.Errorf("reason %q", m.Reason)
				}
				if m.AddLabel != "Require mTLS for front" {
					t.Errorf("addLabel %q", m.AddLabel)
				}
			},
		},
		{
			name: "namespace STRICT wins over the mesh default",
			setup: func(ws *store.Workspace) {
				mesh := object("PeerAuthentication", "default", map[string]any{"mtls": map[string]any{"mode": "PERMISSIVE"}})
				M(mesh["metadata"])["namespace"] = "istio-system"
				ns := object("PeerAuthentication", "default", map[string]any{"mtls": map[string]any{"mode": "STRICT"}})
				put(ws, "security.istio.io", "peerauthentications", "PeerAuthentication", mesh, ns)
			},
			check: func(t *testing.T, c *ServiceConfig) {
				m := cfgTask(t, c, "strict-mtls")
				if len(m.Resources) != 1 || m.Resources[0].Scope != "namespace" || c.Answers.Transport != "Yes. Only mesh workloads can connect." {
					t.Errorf("%+v / %q", m, c.Answers.Transport)
				}
			},
		},
		{
			name:  "Gateway API without retries makes retries unavailable",
			setup: func(ws *store.Workspace) {},
			check: func(t *testing.T, c *ServiceConfig) {
				r := cfgTask(t, c, "retries")
				if r.Status != "unavailable" || !strings.Contains(r.Reason, "installed HTTPRoute retry schema") || r.ShortReason != "Retry is not in the installed Gateway API version" {
					t.Errorf("%+v", r)
				}
			},
		},
		{
			name: "split route",
			setup: func(ws *store.Workspace) {
				routeSchema(ws, []string{"matches", "headers", "value"})
				put(ws, GatewayGroup, "httproutes", "HTTPRoute", frontRoute(
					map[string]any{"name": "stable", "port": 80, "weight": 80},
					map[string]any{"name": "canary", "port": 80, "weight": 20}))
			},
			check: func(t *testing.T, c *ServiceConfig) {
				w := cfgTask(t, c, "weighted-routing")
				if w.Status != "configured" || len(w.Resources) != 1 {
					t.Fatalf("%+v", w)
				}
				r := w.Resources[0]
				if r.Action != "Split" || r.Label != "All paths on port 80" || r.State != "Accepted" || !r.Editable {
					t.Errorf("%+v", r)
				}
				if c.Answers.Routing != "80% to <code>stable</code>, 20% to <code>canary</code>" {
					t.Errorf("answer %q", c.Answers.Routing)
				}
				if h := cfgTask(t, c, "header-routing"); h.Hint != "Adds a rule to the same HTTPRoute" {
					t.Errorf("hint %q", h.Hint)
				}
			},
		},
		{
			name: "timeout shows in the resilience answer",
			setup: func(ws *store.Workspace) {
				r := frontRoute(map[string]any{"name": "stable", "port": 80})
				M(L(M(r["spec"])["rules"])[0])["timeouts"] = map[string]any{"request": "5s"}
				put(ws, GatewayGroup, "httproutes", "HTTPRoute", r)
			},
			check: func(t *testing.T, c *ServiceConfig) {
				if cfgTask(t, c, "timeouts").Status != "configured" || c.Answers.Resilience != "Times out after <code>5s</code>." || c.AnswerEmpty["resilience"] {
					t.Errorf("%+v %q", cfgTask(t, c, "timeouts"), c.Answers.Resilience)
				}
			},
		},
		{
			name: "unrecognised route is Other configuration",
			setup: func(ws *store.Workspace) {
				put(ws, GatewayGroup, "httproutes", "HTTPRoute", frontRoute(map[string]any{"name": "stable", "port": 80}))
			},
			check: func(t *testing.T, c *ServiceConfig) {
				if len(c.Other) != 1 || c.Other[0].Label != "Other configuration" || c.Other[0].Group != "routing" {
					t.Errorf("%+v", c.Other)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws := fixture(t)
			tc.setup(ws)
			c, err := NewServiceConfig(ws, "c", "app", "front")
			if err != nil {
				t.Fatal(err)
			}
			if len(c.Tasks) != len(TaskTable) {
				t.Fatalf("%d tasks, want %d", len(c.Tasks), len(TaskTable))
			}
			for i, d := range TaskTable {
				if c.Tasks[i].ID != d.ID || c.Tasks[i].Group != d.Group || c.Tasks[i].ShortLabel != d.ShortLabel {
					t.Errorf("task %d is %+v, want table entry %s", i, c.Tasks[i], d.ID)
				}
			}
			tc.check(t, c)
		})
	}
}

func TestServiceConfigClusterDrift(t *testing.T) {
	ws := fixture(t)
	put(ws, "security.istio.io", "authorizationpolicies", "AuthorizationPolicy", denyRed())
	r := frontRoute(map[string]any{"name": "stable", "port": 80, "weight": 80}, map[string]any{"name": "canary", "port": 80, "weight": 20})
	put(ws, GatewayGroup, "httproutes", "HTTPRoute", r)
	// Second cluster: the Service exists, the DENY does not, the route differs.
	ws.AddObjects("c2", bundle.APIResource{Version: "v1", Resource: "services", Kind: "Service", Namespaced: true}, []map[string]any{object("Service", "front", map[string]any{"selector": map[string]any{"app": "front"}})}, "svc")
	other := frontRoute(map[string]any{"name": "stable", "port": 80, "weight": 50}, map[string]any{"name": "canary", "port": 80, "weight": 50})
	ws.AddObjects("c2", bundle.APIResource{Group: GatewayGroup, Version: "v1", Resource: "httproutes", Kind: "HTTPRoute", Namespaced: true}, []map[string]any{other}, "routes")
	c, err := NewServiceConfig(ws, "c", "app", "front")
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, d := range c.Drift {
		states[d.Ref.Kind] = d.Clusters["c2"]
	}
	if states["AuthorizationPolicy"] != "missing" || states["HTTPRoute"] != "differs" {
		t.Fatalf("drift %+v", c.Drift)
	}
	if cfgTask(t, c, "weighted-routing").Status != "differs" {
		t.Errorf("weighted-routing %+v", cfgTask(t, c, "weighted-routing"))
	}
	want := "The DENY only exists on c. The split is different on c2."
	if c.Multicluster != want {
		t.Errorf("multicluster %q, want %q", c.Multicluster, want)
	}
	// Nothing on c2 at all: the plain "no configuration" sentence.
	ws2 := fixture(t)
	put(ws2, "security.istio.io", "authorizationpolicies", "AuthorizationPolicy", denyRed())
	put(ws2, GatewayGroup, "httproutes", "HTTPRoute", r)
	ws2.AddObjects("c2", bundle.APIResource{Version: "v1", Resource: "services", Kind: "Service", Namespaced: true}, []map[string]any{object("Service", "front", map[string]any{})}, "svc")
	c2, _ := NewServiceConfig(ws2, "c", "app", "front")
	if want := "The DENY and the split only exist on c. On c2, front has no configuration."; c2.Multicluster != want {
		t.Errorf("multicluster %q, want %q", c2.Multicluster, want)
	}
}

func TestManagedBy(t *testing.T) {
	mk := func(labels, annotations map[string]any, managed ...string) *store.Object {
		md := map[string]any{"name": "x"}
		if labels != nil {
			md["labels"] = labels
		}
		if annotations != nil {
			md["annotations"] = annotations
		}
		if len(managed) > 0 {
			var mf []any
			for _, m := range managed {
				mf = append(mf, map[string]any{"manager": m})
			}
			md["managedFields"] = mf
		}
		return &store.Object{Obj: map[string]any{"metadata": md}}
	}
	cases := []struct {
		name string
		o    *store.Object
		want string
	}{
		{"flightbay label", mk(map[string]any{"app.kubernetes.io/managed-by": "flightbay"}, nil), "flightbay"},
		{"helm label", mk(map[string]any{"app.kubernetes.io/managed-by": "Helm"}, nil), "helm"},
		{"helm annotation", mk(nil, map[string]any{"meta.helm.sh/release-name": "istio"}), "helm"},
		{"argo tracking id", mk(nil, map[string]any{"argocd.argoproj.io/tracking-id": "a:b/c"}), "argocd"},
		{"argo instance label", mk(map[string]any{"argocd.argoproj.io/instance": "app"}, nil), "argocd"},
		{"flux", mk(map[string]any{"kustomize.toolkit.fluxcd.io/name": "apps"}, nil), "flux"},
		{"kubectl", mk(nil, map[string]any{"kubectl.kubernetes.io/last-applied-configuration": "{}"}), "kubectl"},
		{"nothing", mk(nil, nil), "unknown"},
		{"managedFields win over labels", mk(map[string]any{"app.kubernetes.io/managed-by": "flightbay"}, nil, "argocd-controller"), "argocd"},
		{"managedFields kubectl falls back to annotations", mk(nil, nil, "kubectl-client-side-apply"), "unknown"},
	}
	for _, c := range cases {
		if got := ManagedBy(c.o); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	if ManagedBy(nil) != "unknown" {
		t.Error("nil object")
	}
}
