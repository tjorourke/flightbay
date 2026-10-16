package collect

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynfake "k8s.io/client-go/dynamic/fake"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/policy"
)

type fakeForward struct {
	mu     sync.Mutex
	bodies map[string]string // "pod port path" -> body
	opened []string
	gets   []string
}

type fakeSession struct {
	f   *fakeForward
	pod string
}

func (f *fakeForward) Open(_ context.Context, ns, pod string, ports []int) (ForwardSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = append(f.opened, fmt.Sprintf("%s/%s %v", ns, pod, ports))
	return &fakeSession{f: f, pod: pod}, nil
}

func (s *fakeSession) Get(_ context.Context, port int, path string, max int64) ([]byte, bool, error) {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	key := fmt.Sprintf("%s %d %s", s.pod, port, path)
	s.f.gets = append(s.f.gets, key)
	b, ok := s.f.bodies[key]
	if !ok {
		return nil, false, fmt.Errorf("404 from %s", path)
	}
	if int64(len(b)) > max {
		return []byte(b[:max]), true, nil
	}
	return []byte(b), false, nil
}

func (s *fakeSession) Close() {}

func soloPod(ns, name, container, image string) *unstructured.Unstructured {
	return u("v1", "Pod", ns, name, map[string]any{
		"spec":   map[string]any{"nodeName": "worker-1", "containers": []any{map[string]any{"name": container, "image": image}}},
		"status": map[string]any{"phase": "Running"},
	})
}

func diagHarness() (*harness, *fakeForward) {
	h := newHarness()
	objs := append(objects(),
		soloPod("istio-system", "ztunnel-a", "istio-proxy", "us-docker.pkg.dev/soloio-img/istio/ztunnel:1.30.4-solo-distroless"),
		soloPod("istio-system", "istiod-a", "discovery", "us-docker.pkg.dev/soloio-img/istio/pilot:1.30.4-solo-distroless"),
		soloPod("payments", "waypoint-a", "istio-proxy", "docker.io/istio/proxyv2:1.30.4"),
		soloPod("payments", "gw-a", "agentgateway", "us-docker.pkg.dev/solo-public/enterprise-agentgateway/agentgateway-enterprise:2026.8.2"),
		soloPod("secret-team", "ztunnel-other", "istio-proxy", "docker.io/istio/ztunnel:1.30.4"),
	)
	h.dyn = dynfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), gvrs, objs...)
	ztunnel := `{"workloads":[
	  {"uid":"mesh1//Pod/payments/checkout-1","name":"checkout-1","namespace":"payments","serviceAccount":"checkout"},
	  {"uid":"mesh1//Pod/secret-team/vault-0","name":"vault-0","namespace":"secret-team","serviceAccount":"vault"}],
	 "services":[{"name":"checkout","namespace":"payments","hostname":"checkout.payments.svc.cluster.local"},
	  {"name":"vault","namespace":"secret-team","hostname":"vault.secret-team.svc.cluster.local"}],
	 "policies":[],
	 "certificates":[{"identity":"spiffe://mesh1/ns/payments/sa/checkout","state":"Available","certChain":[]},
	  {"identity":"spiffe://mesh1/ns/secret-team/sa/vault","state":"Available","certChain":[]}],
	 "config":{"xdsAddress":"istiod.istio-system.svc:15012","caAddress":"istiod.istio-system.svc:15012"},
	 "version":{"version":"1.30.4"}}`
	envoy := `{"configs":[
	  {"@type":"type.googleapis.com/envoy.admin.v3.ClustersConfigDump","dynamic_active_clusters":[
	    {"cluster":{"name":"outbound|8080||checkout.payments.svc.cluster.local"}},
	    {"cluster":{"name":"outbound|8200||vault.secret-team.svc.cluster.local"}}]},
	  {"@type":"type.googleapis.com/envoy.admin.v3.SecretsConfigDump","dynamic_active_secrets":[{"name":"default","secret":{"tls_certificate":{"private_key":{"inline_bytes":"` + canary + `"}}}}]}]}`
	configz := `[{"kind":"AuthorizationPolicy","apiVersion":"security.istio.io/v1","metadata":{"name":"allow","namespace":"payments"},"spec":{}},
	  {"kind":"AuthorizationPolicy","apiVersion":"security.istio.io/v1","metadata":{"name":"deny","namespace":"secret-team"},"spec":{}},
	  {"kind":"GatewayClass","apiVersion":"gateway.networking.k8s.io/v1","metadata":{"name":"secret-team-class"},"spec":{}},
	  {"kind":"VirtualService","apiVersion":"networking.istio.io/v1","metadata":{"name":"vs","namespace":"payments"},"spec":{"headers":{"api_key":"` + canary + `"}}}]`
	metrics := "# TYPE istio_requests_total counter\n" +
		`istio_requests_total{destination_service="checkout.payments.svc.cluster.local",namespace="payments"} 10` + "\n" +
		`istio_requests_total{destination_service="vault.secret-team.svc.cluster.local",namespace="secret-team"} 3` + "\n"
	f := &fakeForward{bodies: map[string]string{
		"ztunnel-a 15000 /config_dump":              ztunnel,
		"ztunnel-a 15020 /metrics":                  metrics,
		"istiod-a 8080 /debug/syncz":                `[{"proxy":"waypoint-a.payments","proxy_type":"router"},{"proxy":"vault-0.secret-team","proxy_type":"sidecar"}]`,
		"istiod-a 8080 /debug/configz":              configz,
		"istiod-a 8080 /debug/registryz":            `[{"Attributes":{"Name":"checkout","Namespace":"payments"},"hostname":"checkout.payments.svc.cluster.local"}]`,
		"istiod-a 8080 /debug/endpointz":            `{"checkout.payments.svc.cluster.local":{"payments":{}},"vault.secret-team.svc.cluster.local":{"secret-team":{}},"vault.secret-team.mesh.internal":{"secret-team":{"Shards":{}}}}`,
		"istiod-a 8080 /debug/mesh":                 `{"trustDomain":"mesh1"}`,
		"istiod-a 8080 /debug/connections":          `{"totalClients":1}`,
		"istiod-a 15014 /metrics":                   metrics,
		"waypoint-a 15000 /config_dump?include_eds": envoy,
		"waypoint-a 15000 /clusters?format=json":    `{"cluster_statuses":[]}`,
		"waypoint-a 15000 /listeners?format=json":   `{"listener_statuses":[]}`,
		"waypoint-a 15000 /server_info":             `{"version":"1.30.4","state":"LIVE"}`,
		"waypoint-a 15000 /certs":                   `{"certificates":[{"cert_chain":[{"days_until_expiration":"12","subject_alt_names":[{"uri":"spiffe://mesh1/ns/payments/sa/waypoint"}]}]}]}`,
		"waypoint-a 15000 /stats":                   "cluster.outbound|8080||checkout.payments.svc.cluster.local.upstream_rq_total: 4\n",
		"waypoint-a 15020 /stats/prometheus":        metrics,
		"gw-a 15000 /config_dump":                   `{"binds":[],"policies":[],"workloads":[],"services":[],"backends":[{"backend":{"auth":{"key":"sk-ant-` + canary + `aaaaaaaaaaaaaaaa"}}}]}`,
		"gw-a 15020 /metrics":                       "agentgateway_requests_total 1\n",
	}}
	h.fwd = f
	return h, f
}

func diagPolicy(ns ...string) *policy.Policy {
	p, _ := policy.Profile("standard")
	p.Namespaces = ns
	p.Categories = append(p.Categories, policy.CatProxies, policy.CatControlPlane, policy.CatMetrics)
	p.PortForward = true
	return p
}

func TestDiagnosticsCollectedScopedAndRedacted(t *testing.T) {
	h, f := diagHarness()
	files, order, r := run(t, diagPolicy("payments", "istio-system"), h, nil)
	for name, data := range files {
		if bytes.Contains(data, []byte(canary)) {
			t.Fatalf("canary in %s: %s", name, data)
		}
		if bytes.Contains(data, []byte("secret-team")) {
			t.Fatalf("namespace outside the selection leaked into %s: %s", name, data)
		}
	}
	for _, o := range f.opened {
		if strings.HasPrefix(o, "secret-team/") {
			t.Fatalf("port-forward opened outside the selection: %v", f.opened)
		}
	}
	zt := string(files[bundle.DiagnosticsPath("mesh1", "proxies", "istio-system", "ztunnel-a", "config_dump.json")])
	if !strings.Contains(zt, "checkout-1") || !strings.Contains(zt, "spiffe://mesh1/ns/payments/sa/checkout") || !strings.Contains(zt, "xdsAddress") {
		t.Fatalf("ztunnel dump lost in-scope data: %s", zt)
	}
	env := string(files[bundle.DiagnosticsPath("mesh1", "proxies", "payments", "waypoint-a", "config_dump.json")])
	if strings.Contains(env, "SecretsConfigDump") || !strings.Contains(env, "checkout.payments.svc") {
		t.Fatalf("envoy dump: %s", env)
	}
	cz := string(files[bundle.DiagnosticsPath("mesh1", "control-plane", "istio-system", "istiod-a", "configz.json")])
	if !strings.Contains(cz, `"allow"`) || strings.Contains(cz, `"deny"`) {
		t.Fatalf("configz: %s", cz)
	}
	if _, ok := files[bundle.DiagnosticsPath("mesh1", "control-plane", "istio-system", "istiod-a", "metrics.prom")]; !ok {
		t.Fatal("istiod metrics missing")
	}
	var sum DiagnosticSummary
	if err := json.Unmarshal(files[bundle.DiagnosticsPath("mesh1", "", "", "", "summary.json")], &sum); err != nil || len(sum.Components) != 4 {
		t.Fatalf("summary: %v %+v", err, sum)
	}
	cov := coverage(t, files)
	c := cov["mesh1.proxies.istio-system.ztunnel-a.config_dump"]
	if c.State != bundle.StateComplete || !strings.Contains(c.Reason, "outside the selection removed") {
		t.Fatalf("ztunnel coverage %+v", c)
	}
	if !r.Manifest().Complete {
		t.Fatal("manifest incomplete")
	}
	// Diagnostics come after objects and logs.
	lastLog, firstDiag := -1, -1
	for i, n := range order {
		if strings.Contains(n, "/logs/") {
			lastLog = i
		}
		if strings.Contains(n, "/diagnostics/") && firstDiag < 0 {
			firstDiag = i
		}
	}
	if firstDiag < lastLog {
		t.Fatalf("diagnostics written before logs finished (diagnostics at %d, last log at %d)", firstDiag, lastLog)
	}
}

func TestDiagnosticsHonourResourceExclusions(t *testing.T) {
	h, f := diagHarness()
	p := diagPolicy("payments", "istio-system")
	p.ExcludeResources = []string{"services"}
	files, _, _ := run(t, p, h, nil)
	for _, g := range f.gets {
		if strings.Contains(g, "/debug/registryz") || strings.Contains(g, "/debug/endpointz") || strings.Contains(g, "/clusters?") {
			t.Fatalf("endpoint exposing excluded services was requested: %s", g)
		}
	}
	zt := string(files[bundle.DiagnosticsPath("mesh1", "proxies", "istio-system", "ztunnel-a", "config_dump.json")])
	if strings.Contains(zt, `"services"`) || !strings.Contains(zt, `"workloads"`) {
		t.Fatalf("services not stripped from ztunnel dump: %s", zt)
	}
	if c := coverage(t, files)["mesh1.control-plane.istio-system.istiod-a.registryz"]; c.State != bundle.StateExcluded {
		t.Fatalf("registryz coverage %+v", c)
	}
}

func TestDiagnosticsNeedPortForward(t *testing.T) {
	h, f := diagHarness()
	p := diagPolicy("payments", "istio-system")
	p.PortForward = false
	files, _, _ := run(t, p, h, nil)
	if len(f.opened) != 0 {
		t.Fatalf("port-forward used without permission: %v", f.opened)
	}
	for name := range files {
		if strings.Contains(name, "/diagnostics/proxies/") {
			t.Fatalf("proxy dump collected without port-forward: %s", name)
		}
	}
}

func TestOversizedJSONDiagnosticIsNotKept(t *testing.T) {
	h, _ := diagHarness()
	files, _, _ := run(t, diagPolicy("payments", "istio-system"), h, func(c *Collector) { c.MaxDiagBytes = 64 })
	if _, ok := files[bundle.DiagnosticsPath("mesh1", "proxies", "istio-system", "ztunnel-a", "config_dump.json")]; ok {
		t.Fatal("a cut JSON document was archived")
	}
	if c := coverage(t, files)["mesh1.proxies.istio-system.ztunnel-a.config_dump"]; c.State != bundle.StateTruncated {
		t.Fatalf("coverage %+v", c)
	}
}
