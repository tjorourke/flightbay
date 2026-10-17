package analyze

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/policy"
	"github.com/tjorourke/flightbay/internal/store"
)

var (
	pods     = bundle.APIResource{Version: "v1", Resource: "pods", Kind: "Pod", Namespaced: true}
	services = bundle.APIResource{Version: "v1", Resource: "services", Kind: "Service", Namespaced: true}
	routes   = bundle.APIResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes", Kind: "HTTPRoute", Namespaced: true}
	gateways = bundle.APIResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways", Kind: "Gateway", Namespaced: true}
	agwPol   = bundle.APIResource{Group: "agentgateway.dev", Version: "v1alpha1", Resource: "agentgatewaypolicies", Kind: "AgentgatewayPolicy", Namespaced: true}
	nsRes    = bundle.APIResource{Version: "v1", Resource: "namespaces", Kind: "Namespace"}
)

func pod(ns, name string, status map[string]any, spec map[string]any, labels map[string]any) map[string]any {
	if spec == nil {
		spec = map[string]any{"containers": []any{map[string]any{"name": "app", "image": "example/app:1"}}}
	}
	return map[string]any{"kind": "Pod", "metadata": map[string]any{"name": name, "namespace": ns, "labels": labels}, "spec": spec, "status": status}
}

func cs(name string, restarts int, state, last map[string]any) map[string]any {
	return map[string]any{"name": name, "restartCount": float64(restarts), "state": state, "lastState": last}
}

func workspace(t *testing.T, complete bool) *store.Workspace {
	t.Helper()
	ws := store.New()
	p, _ := policy.Profile("standard")
	b, _ := json.Marshal(p)
	ws.SetHeader(&bundle.Header{Contexts: []string{"mesh1"}, Policy: b})
	ws.SetState(func(s *store.ImportState) { s.Phase = store.PhaseLoading })
	ws.AddObjects("mesh1", pods, []map[string]any{
		pod("payments", "checkout-1", map[string]any{"phase": "Running", "containerStatuses": []any{
			cs("app", 12, map[string]any{"waiting": map[string]any{"reason": "CrashLoopBackOff", "message": "back-off"}}, map[string]any{"terminated": map[string]any{"reason": "Error", "exitCode": float64(1)}}),
		}}, nil, map[string]any{"app": "checkout"}),
		pod("payments", "worker-1", map[string]any{"phase": "Running", "containerStatuses": []any{
			cs("app", 2, map[string]any{"running": map[string]any{}}, map[string]any{"terminated": map[string]any{"reason": "OOMKilled", "exitCode": float64(137)}}),
		}}, nil, nil),
		pod("payments", "img-1", map[string]any{"phase": "Pending", "containerStatuses": []any{
			cs("app", 0, map[string]any{"waiting": map[string]any{"reason": "ImagePullBackOff", "message": "pull access denied"}}, nil),
		}}, nil, nil),
		pod("default", "inventory-1", map[string]any{"phase": "Pending", "conditions": []any{
			map[string]any{"type": "PodScheduled", "status": "False", "reason": "Unschedulable", "message": "0/3 nodes are available: 3 Insufficient cpu."},
		}}, nil, nil),
		pod("istio-system", "istiod-1", map[string]any{"phase": "Running"}, map[string]any{"containers": []any{map[string]any{"name": "discovery", "image": "docker.io/istio/pilot:1.30.0"}}}, map[string]any{"app": "istiod"}),
		pod("istio-system", "ztunnel-1", map[string]any{"phase": "Running"}, map[string]any{"containers": []any{map[string]any{"name": "istio-proxy", "image": "docker.io/istio/ztunnel:1.29.2"}}}, map[string]any{"app": "ztunnel"}),
	}, "c")
	ws.AddObjects("mesh1", services, []map[string]any{{"kind": "Service", "metadata": map[string]any{"name": "checkout", "namespace": "payments"}}}, "c")
	ws.AddObjects("mesh1", routes, []map[string]any{{
		"kind": "HTTPRoute", "metadata": map[string]any{"name": "checkout-route", "namespace": "payments"},
		"spec": map[string]any{"rules": []any{map[string]any{"backendRefs": []any{map[string]any{"name": "checkout-v2", "port": float64(8080)}, map[string]any{"name": "checkout", "port": float64(8080)}}}}},
		"status": map[string]any{"parents": []any{map[string]any{"conditions": []any{
			map[string]any{"type": "Accepted", "status": "True"},
			map[string]any{"type": "ResolvedRefs", "status": "False", "reason": "BackendNotFound", "message": "Service checkout-v2 not found"},
		}}}},
	}}, "c")
	ws.AddObjects("mesh1", gateways, []map[string]any{{"kind": "Gateway", "metadata": map[string]any{"name": "gw", "namespace": "payments"},
		"status": map[string]any{"conditions": []any{map[string]any{"type": "Programmed", "status": "False", "reason": "AddressNotAssigned"}}}}}, "c")
	ws.AddObjects("mesh1", agwPol, []map[string]any{{"kind": "AgentgatewayPolicy", "metadata": map[string]any{"name": "rate", "namespace": "payments"},
		"status": map[string]any{"ancestors": []any{map[string]any{"conditions": []any{map[string]any{"type": "Accepted", "status": "False", "reason": "TargetNotFound"}}}}}}}, "c")
	for i := 0; i < 4; i++ {
		ws.AddLogLines("mesh1", "payments", "checkout-1", "app", "previous", []string{fmt.Sprintf("2026-09-27T10:0%d:12Z ERROR connection refused destination=10.42.2.%d:5432", i, i)}, "l")
	}
	if complete {
		for _, r := range []string{"pods", "services"} {
			ws.SetCoverage(bundle.Coverage{ID: "mesh1.resource.core.v1." + r, Cluster: "mesh1", Type: bundle.CoverageResource, Version: "v1", Resource: r, State: bundle.StateComplete})
		}
		ws.SetState(func(s *store.ImportState) { s.Phase = store.PhaseComplete; s.Verified = true })
	}
	return ws
}

func byRule(fs []Finding) map[string][]Finding {
	out := map[string][]Finding{}
	for _, f := range fs {
		out[f.Rule] = append(out[f.Rule], f)
	}
	return out
}

func TestWorkloadRules(t *testing.T) {
	fs := byRule(Run(workspace(t, true)))
	if f := fs["workload.crashloop"]; len(f) != 1 || f[0].Severity != Critical || f[0].Object.Name != "checkout-1" || f[0].LogQuery == "" {
		t.Fatalf("crashloop: %+v", f)
	}
	if f := fs["workload.oomkilled"]; len(f) != 1 || f[0].Object.Name != "worker-1" {
		t.Fatalf("oom: %+v", f)
	}
	if f := fs["workload.image-pull"]; len(f) != 1 || f[0].Object.Name != "img-1" {
		t.Fatalf("image pull: %+v", f)
	}
	if f := fs["workload.unschedulable"]; len(f) != 1 || f[0].Detail == "" {
		t.Fatalf("unschedulable: %+v", f)
	}
}

func TestGatewayAndPolicyRules(t *testing.T) {
	fs := byRule(Run(workspace(t, true)))
	if f := fs["gateway.route-condition"]; len(f) != 1 || f[0].Object.Name != "checkout-route" {
		t.Fatalf("route condition: %+v", f)
	}
	if f := fs["gateway.unresolved-backend"]; len(f) != 1 || f[0].Provisional {
		t.Fatalf("unresolved backend: %+v", f)
	}
	if f := fs["gateway.not-programmed"]; len(f) != 1 {
		t.Fatalf("gateway programmed: %+v", f)
	}
	if f := fs["policy.not-accepted"]; len(f) != 1 || f[0].Object.Kind != "AgentgatewayPolicy" {
		t.Fatalf("policy: %+v", f)
	}
	if f := fs["mesh.version-skew"]; len(f) != 1 {
		t.Fatalf("skew: %+v", f)
	}
	if f := fs["logs.error-signature"]; len(f) != 1 || f[0].Count != 4 {
		t.Fatalf("signature: %+v", f)
	}
}

func TestAbsenceRulesWaitForCoverage(t *testing.T) {
	fs := Run(workspace(t, false))
	for _, f := range fs {
		if f.Rule == "gateway.unresolved-backend" {
			t.Fatalf("absence rule fired before services coverage was complete: %+v", f)
		}
		if !f.Provisional {
			t.Fatalf("finding during import not marked provisional: %+v", f)
		}
	}
}

func TestFindingsReadinessForLiveAndBundleWorkspaces(t *testing.T) {
	for _, tc := range []struct {
		name        string
		liveSource  bool
		manifest    bool
		phase       string
		verified    bool
		provisional bool
	}{
		{"live initial list", true, false, store.PhaseLoading, false, true},
		{"live initial list complete", true, false, store.PhaseLive, false, false},
		{"live discovery failed", true, false, store.PhaseFailed, false, true},
		{"verified bundle", false, true, store.PhaseComplete, true, false},
		{"unverified bundle", false, true, store.PhaseComplete, false, true},
		{"saved live bundle unverified", true, true, store.PhaseComplete, false, true},
		{"saved live bundle verified", true, true, store.PhaseComplete, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws := workspace(t, true)
			defer ws.Close()
			if tc.liveSource {
				h := ws.Header()
				h.Source = store.SourceLive
				ws.SetHeader(h)
			}
			if tc.manifest {
				ws.SetManifest(&bundle.Manifest{})
			}
			ws.SetState(func(s *store.ImportState) {
				s.Phase, s.Verified = tc.phase, tc.verified
			})
			fs := Run(ws)
			if len(fs) == 0 {
				t.Fatal("expected findings from the unhealthy fixture workloads")
			}
			for _, f := range fs {
				if f.Provisional != tc.provisional {
					t.Fatalf("%s: provisional=%v, want %v", f.Rule, f.Provisional, tc.provisional)
				}
			}
			if tc.phase == store.PhaseLive {
				ws.SetCoverage(bundle.Coverage{ID: "mesh1.resource.core.v1.services", Cluster: "mesh1", Type: bundle.CoverageResource, Version: "v1", Resource: "services", State: bundle.StateFailed, Reason: "forbidden"})
				fs = Run(ws)
				if len(byRule(fs)["gateway.unresolved-backend"]) != 0 {
					t.Fatal("live readiness bypassed missing Service coverage")
				}
				if len(byRule(fs)["collection.gap"]) == 0 {
					t.Fatal("live collection failure was hidden")
				}
			}
		})
	}
}

func TestAmbientWithoutZtunnel(t *testing.T) {
	ws := store.New()
	p, _ := policy.Profile("standard")
	p.ClusterScoped = true
	b, _ := json.Marshal(p)
	ws.SetHeader(&bundle.Header{Contexts: []string{"c"}, Policy: b})
	ws.AddObjects("c", nsRes, []map[string]any{{"kind": "Namespace", "metadata": map[string]any{"name": "payments", "labels": map[string]any{"istio.io/dataplane-mode": "ambient"}}}}, "x")
	ws.AddObjects("c", pods, []map[string]any{pod("payments", "a", map[string]any{"phase": "Running"}, nil, nil)}, "x")
	ws.SetCoverage(bundle.Coverage{ID: "c.resource.core.v1.pods", Cluster: "c", Type: bundle.CoverageResource, Version: "v1", Resource: "pods", State: bundle.StateComplete})
	ws.SetState(func(s *store.ImportState) { s.Phase = store.PhaseComplete })
	if f := byRule(Run(ws))["mesh.ambient-no-ztunnel"]; len(f) != 1 {
		t.Fatalf("ambient rule: %+v", Run(ws))
	}
}

func TestCollectionFailuresSurface(t *testing.T) {
	ws := workspace(t, true)
	ws.SetCoverage(bundle.Coverage{ID: "x", Cluster: "mesh1", Type: bundle.CoverageResource, Resource: "secrets", Version: "v1", State: bundle.StateFailed, Reason: "forbidden"})
	if f := byRule(Run(ws))["collection.gap"]; len(f) != 1 || f[0].Severity != Info {
		t.Fatalf("collection gap: %+v", f)
	}
}

func TestCrashLoopCaughtBetweenExitAndBackOff(t *testing.T) {
	ws := workspace(t, true)
	ws.AddObjects("mesh1", pods, []map[string]any{
		pod("payments", "checkout-2", map[string]any{"phase": "Running", "containerStatuses": []any{
			cs("app", 4, map[string]any{"terminated": map[string]any{"reason": "Error", "exitCode": float64(1)}}, map[string]any{"terminated": map[string]any{"reason": "Error", "exitCode": float64(1)}}),
		}}, nil, nil),
		// One clean exit after a couple of restarts is not a crash loop.
		pod("payments", "job-1", map[string]any{"phase": "Succeeded", "containerStatuses": []any{
			cs("app", 1, map[string]any{"terminated": map[string]any{"reason": "Completed", "exitCode": float64(0)}}, nil),
		}}, nil, nil),
	}, "c2")
	var got []Finding
	for _, f := range byRule(Run(ws))["workload.crashloop"] {
		if f.Object.Name != "checkout-1" {
			got = append(got, f)
		}
	}
	if len(got) != 1 || got[0].Object.Name != "checkout-2" {
		t.Fatalf("crash loop between exit and back-off: %+v", got)
	}
	if got[0].LogQuery != "pod:checkout-2 container:app" {
		t.Fatalf("no previous stream captured, so the query should read the current one: %q", got[0].LogQuery)
	}
}

func TestTwoConsecutiveFailuresAreACrashLoop(t *testing.T) {
	ws := workspace(t, true)
	ws.AddObjects("mesh1", pods, []map[string]any{pod("payments", "checkout-3", map[string]any{"phase": "Running", "containerStatuses": []any{
		cs("app", 1, map[string]any{"terminated": map[string]any{"reason": "Error", "exitCode": float64(1)}}, map[string]any{"terminated": map[string]any{"reason": "Error", "exitCode": float64(1)}}),
	}}, nil, nil)}, "c3")
	found := false
	for _, f := range byRule(Run(ws))["workload.crashloop"] {
		found = found || f.Object.Name == "checkout-3"
	}
	if !found {
		t.Fatal("two consecutive non-zero exits not reported")
	}
}

func TestCrashLoopCaughtWhileRunningAgain(t *testing.T) {
	ws := workspace(t, true)
	ws.SetHeader(&bundle.Header{Contexts: []string{"mesh1"}, Policy: ws.Header().Policy, CaptureStart: time.Date(2026, 9, 27, 22, 13, 23, 0, time.UTC)})
	ws.AddObjects("mesh1", pods, []map[string]any{
		pod("payments", "checkout-9", map[string]any{"phase": "Running", "containerStatuses": []any{
			cs("app", 4, map[string]any{"running": map[string]any{"startedAt": "2026-09-27T22:13:21Z"}}, map[string]any{"terminated": map[string]any{"reason": "Error", "exitCode": float64(1)}}),
		}}, nil, nil),
		// Crashed a few times long ago, stable for hours: not a crash loop.
		pod("payments", "stable-1", map[string]any{"phase": "Running", "containerStatuses": []any{
			cs("app", 4, map[string]any{"running": map[string]any{"startedAt": "2026-09-27T08:00:00Z"}}, map[string]any{"terminated": map[string]any{"reason": "Error", "exitCode": float64(1)}}),
		}}, nil, nil),
	}, "c9")
	got := map[string]bool{}
	for _, f := range byRule(Run(ws))["workload.crashloop"] {
		got[f.Object.Name] = true
	}
	if !got["checkout-9"] || got["stable-1"] {
		t.Fatalf("crash loop while running again: %v", got)
	}
}
