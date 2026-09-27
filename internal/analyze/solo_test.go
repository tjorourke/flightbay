package analyze

import (
	"strings"
	"testing"
	"time"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/store"
)

var (
	dsRes  = bundle.APIResource{Group: "apps", Version: "v1", Resource: "daemonsets", Kind: "DaemonSet", Namespaced: true}
	svcRes = bundle.APIResource{Version: "v1", Resource: "services", Kind: "Service", Namespaced: true}
)

func soloWorkspace(t *testing.T) *store.Workspace {
	ws := workspace(t, true)
	ws.SetHeader(&bundle.Header{Contexts: []string{"mesh1"}, Policy: ws.Header().Policy, CaptureStart: time.Date(2026, 9, 27, 18, 0, 0, 0, time.UTC)})
	return ws
}

func TestCertificateRules(t *testing.T) {
	ws := soloWorkspace(t)
	ws.AddDiagnostic("mesh1", "proxies", "istio-system", "ztunnel-a", "meta.json", []byte(`{"component":"ztunnel"}`))
	ws.AddDiagnostic("mesh1", "proxies", "istio-system", "ztunnel-a", "config_dump.json", []byte(`{"certificates":[
	  {"identity":"spiffe://c/ns/payments/sa/ok","state":"Available","certChain":[{"expirationTime":"2026-09-28T10:00:00Z","serialNumber":"1"}],"rootCerts":[{"expirationTime":"2026-10-07T00:00:00Z","serialNumber":"r"}]},
	  {"identity":"spiffe://c/ns/payments/sa/stuck","state":"Available","certChain":[{"expirationTime":"2026-09-27T06:00:00Z","serialNumber":"2"}]},
	  {"identity":"spiffe://c/ns/payments/sa/csr","state":"Unavailable: signing failed","certChain":[]}]}`))
	fs := byRule(Run(ws))
	if f := fs["cert.expired"]; len(f) != 1 || !strings.Contains(f[0].Title, "sa/stuck") || f[0].Severity != Critical {
		t.Fatalf("expired: %+v", f)
	}
	if f := fs["cert.root-expiring"]; len(f) != 1 || !strings.Contains(f[0].Title, "9 days") {
		t.Fatalf("root expiring: %+v", f)
	}
	if f := fs["cert.unavailable"]; len(f) != 1 || !strings.Contains(f[0].Title, "sa/csr") {
		t.Fatalf("unavailable: %+v", f)
	}
	for _, f := range Run(ws) {
		if strings.Contains(f.Title, "sa/ok") {
			t.Fatalf("a healthy short-lived workload certificate was reported: %+v", f)
		}
	}
}

func TestXDSRules(t *testing.T) {
	ws := soloWorkspace(t)
	ws.AddDiagnostic("mesh1", "control-plane", "istio-system", "istiod-a", "meta.json", []byte(`{"component":"istiod"}`))
	ws.AddDiagnostic("mesh1", "control-plane", "istio-system", "istiod-a", "syncz.json", []byte(`[
	  {"proxy":"checkout-1.payments","proxy_type":"sidecar","resources":{"type.googleapis.com/envoy.config.listener.v3.Listener":{"sent":"v9","acked":"v8"}}},
	  {"proxy":"ztunnel-a.istio-system","proxy_type":"ztunnel","resources":{"type.googleapis.com/istio.workload.Address":{"sent":"v9","acked":"v9"}}}]`))
	ws.AddLogLines("mesh1", "istio-system", "istiod-a", "discovery", "current", []string{
		"2026-09-27T17:59:00Z 2026-09-27T17:59:00.1Z\twarn\tads\tADS:LDS: ACK ERROR sidecar~10.0.0.9~checkout-1.payments~payments.svc.cluster.local-7 Internal:Error adding/updating listener(s) 0.0.0.0_8080: invalid route",
		"2026-09-27T17:59:30Z 2026-09-27T17:59:30.1Z\twarn\tads\tADS:LDS: ACK ERROR sidecar~10.0.0.9~checkout-1.payments~payments.svc.cluster.local-7 Internal:Error adding/updating listener(s) 0.0.0.0_8080: invalid route",
	}, "l")
	fs := byRule(Run(ws))
	if f := fs["xds.stale"]; len(f) != 1 || !strings.Contains(f[0].Title, "checkout-1.payments") {
		t.Fatalf("stale: %+v", f)
	}
	if f := fs["xds.nack"]; len(f) != 1 || f[0].Count != 2 || !strings.Contains(f[0].Title, "checkout-1.payments") {
		t.Fatalf("nack: %+v", f)
	}
}

func TestWaypointMissingWaitsForGatewayCoverage(t *testing.T) {
	ws := soloWorkspace(t)
	ws.AddObjects("mesh1", svcRes, []map[string]any{{"kind": "Service", "metadata": map[string]any{"name": "reviews", "namespace": "payments", "labels": map[string]any{"istio.io/use-waypoint": "payments-wp"}}},
		{"kind": "Service", "metadata": map[string]any{"name": "ok", "namespace": "payments", "labels": map[string]any{"istio.io/use-waypoint": "gw"}}}}, "s")
	if f := byRule(Run(ws))["mesh.waypoint-missing"]; len(f) != 0 {
		t.Fatalf("absence rule fired without complete Gateway coverage: %+v", f)
	}
	ws.SetCoverage(bundle.Coverage{ID: "mesh1.resource.gateway.networking.k8s.io.v1.gateways", Cluster: "mesh1", Type: bundle.CoverageResource,
		Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways", State: bundle.StateComplete})
	f := byRule(Run(ws))["mesh.waypoint-missing"]
	if len(f) != 1 || f[0].Object.Name != "reviews" {
		t.Fatalf("waypoint missing: %+v", f)
	}
}

func TestCNIAndAgentgatewaySkewAndGatewayAPI(t *testing.T) {
	ws := soloWorkspace(t)
	ws.AddObjects("mesh1", dsRes, []map[string]any{{"kind": "DaemonSet", "metadata": map[string]any{"name": "istio-cni-node", "namespace": "istio-system"},
		"status": map[string]any{"desiredNumberScheduled": float64(3), "numberReady": float64(2)}}}, "d")
	ws.AddObjects("mesh1", pods, []map[string]any{
		pod("agw", "controller-1", map[string]any{"phase": "Running"}, map[string]any{"containers": []any{map[string]any{"name": "c", "image": "x/enterprise-agentgateway-controller:2026.8.2"}}}, nil),
		pod("agw", "gw-1", map[string]any{"phase": "Running"}, map[string]any{"containers": []any{map[string]any{"name": "a", "image": "x/agentgateway-enterprise:2026.7.1"}}}, nil),
	}, "p2")
	for _, n := range []struct{ name, v string }{{"gateways", "v1.4.0"}, {"httproutes", "v1.4.0"}, {"grpcroutes", "v1.2.1"}} {
		ws.AddCRD("mesh1", map[string]any{"metadata": map[string]any{"name": n.name + ".gateway.networking.k8s.io", "annotations": map[string]any{"gateway.networking.k8s.io/bundle-version": n.v}}})
	}
	fs := byRule(Run(ws))
	if f := fs["mesh.cni-not-ready"]; len(f) != 1 || !strings.Contains(f[0].Title, "2 of 3") {
		t.Fatalf("cni: %+v", f)
	}
	if f := fs["agentgateway.version-skew"]; len(f) != 1 || !strings.Contains(f[0].Title, "2026.7.1") {
		t.Fatalf("agw skew: %+v", f)
	}
	if f := fs["gateway.api-version-mixed"]; len(f) != 1 || !strings.Contains(strings.Join(f[0].Evidence, " "), "grpcroutes") {
		t.Fatalf("gateway api: %+v", f)
	}
}
