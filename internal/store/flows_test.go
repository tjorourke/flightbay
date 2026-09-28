package store

import (
	"os"
	"strings"
	"testing"

	"github.com/tjorourke/flightbay/internal/bundle"
)

func flowsBy(fs []Flow, source string) []Flow {
	var out []Flow
	for _, f := range fs {
		if f.Source == source {
			out = append(out, f)
		}
	}
	return out
}

// Several HTTP methods on one edge: each method gets its own count, not the
// edge total repeated.
func TestZtunnelFlowsCountPerMethod(t *testing.T) {
	w := New()
	line := func(method string) string {
		return "2026-09-28T11:06:10.074538Z\tinfo\thttp_access\trequest complete\tsrc.workload=\"agent-1\" src.namespace=\"a\" src.identity=\"spiffe://cluster.local/ns/a/sa/agent\" dst.service=\"db.b.svc.cluster.local:8080\" dst.workload=\"db-1\" dst.namespace=\"b\" direction=\"outbound\" method=" + method
	}
	w.AddLogLines("c", "istio-system", "ztunnel-x", "istio-proxy", "current", []string{line("GET"), line("GET"), line("GET"), line("POST")}, "l")
	w.WaitLogs()
	got := map[string]int{}
	for _, f := range flowsBy(w.Flows(""), "ztunnel") {
		if f.DstHost != "db.b.svc.cluster.local" || f.DstPort != 8080 || f.Protocol != "http" {
			t.Fatalf("%+v", f)
		}
		got[f.Method] += f.Count
	}
	if got["GET"] != 3 || got["POST"] != 1 || len(got) != 2 {
		t.Fatalf("per-method counts %v", got)
	}
}

func TestFlowsFromAllLogSources(t *testing.T) {
	w := New()
	w.AddObjects("c", bundle.APIResource{Version: "v1", Resource: "pods", Kind: "Pod", Namespaced: true}, []map[string]any{
		{"metadata": map[string]any{"name": "waypoint-7b5d-abc", "namespace": "bookinfo", "labels": map[string]any{"gateway.networking.k8s.io/gateway-name": "waypoint"}}},
		{"metadata": map[string]any{"name": "sleep-1", "namespace": "foo"}},
	}, "p")
	k := "2026-09-27T21:50:44.900000000Z "
	w.AddLogLines("c", "agentgateway-system", "ar-ingress-1", "agentgateway", "current", []string{
		k + "2026-09-27T21:50:44.861295Z  info  request gateway=agentgateway-system/ar-ingress listener=http route=ar-keycloak/keycloak endpoint=10.244.1.131:8080 src.addr=10.244.1.1:29278 http.method=GET http.host=keycloak.example.com http.path=/realms/a?x=1 http.version=HTTP/1.1 http.status=200 protocol=http duration=45ms",
		k + "2026-09-27T21:50:45.861295Z  info  request gateway=agentgateway-system/ar-ingress listener=http route=ar-keycloak/keycloak endpoint=10.244.1.131:8080 src.addr=10.244.1.1:29290 http.method=GET http.host=keycloak.example.com http.path=/realms/a?x=2 http.version=HTTP/1.1 http.status=200 protocol=http duration=45ms",
		k + "2026-09-27T21:50:46.861295Z  info  request gateway=agentgateway-system/ar-ingress listener=http route=ar-keycloak/keycloak endpoint=10.244.1.131:8080 src.addr=10.244.1.1:29291 http.method=GET http.host=keycloak.example.com http.path=/realms/a http.version=HTTP/1.1 http.status=403 protocol=http duration=1ms",
	}, "agw")
	w.AddLogLines("c", "foo", "sleep-1", "istio-proxy", "current", []string{
		k + `[2020-11-25T21:26:18.409Z] "GET /status/418 HTTP/1.1" 418 - via_upstream - "-" 0 135 4 4 "-" "curl/7.73.0-DEV" "84961386-6d84-929d-98bd-c5aee93b5c88" "httpbin:8000" "10.44.1.27:80" outbound|8000||httpbin.foo.svc.cluster.local 10.44.1.23:37652 10.0.45.184:8000 10.44.1.23:46520 - default`,
		k + `[2020-11-25T21:26:19.409Z] "GET /status/418 HTTP/1.1" 418 - via_upstream - "-" 0 135 4 4 "-" "curl/7.73.0-DEV" "94961386-6d84-929d-98bd-c5aee93b5c88" "httpbin:8000" "10.44.1.27:80" outbound|8000||httpbin.foo.svc.cluster.local 10.44.1.23:37653 10.0.45.184:8000 10.44.1.23:46521 - default`,
		k + `2020-11-25T21:26:20.000Z info envoy upstream  cds: added/updated 12 cluster(s)`,
	}, "sc")
	w.AddLogLines("c", "bookinfo", "waypoint-7b5d-abc", "istio-proxy", "current", []string{
		k + `[2024-02-01T09:00:01.000Z] "GET /reviews/0 HTTP/1.1" 200 - via_upstream - "-" 0 358 12 11 "-" "Mozilla/5.0" "a1b2" "reviews:9080" "10.244.1.14:9080" inbound-vip|9080|http|reviews.bookinfo.svc.cluster.local envoy://internal_client_address/ 10.96.44.10:9080 10.244.1.9:52000 - default`,
	}, "wp")
	w.AddLogLines("c", "kube-system", "coredns-5d78c9869d-x7x2k", "coredns", "current", []string{
		k + `[INFO] 10.244.1.5:41234 - 4242 "A IN api.github.com. udp 32 false 512" NOERROR qr,rd,ra 110 0.0003s`,
		k + `[INFO] 10.244.1.5:41235 - 4243 "A IN api.github.com. udp 32 false 512" NOERROR qr,rd,ra 110 0.0003s`,
		k + `[INFO] 10.244.1.5:41236 - 4244 "A IN kubernetes.default.svc.cluster.local. udp 54 false 512" NOERROR qr,aa,rd 147 0.000139s`,
	}, "dns")
	w.WaitLogs()
	all := w.Flows("")

	agw := flowsBy(all, "agentgateway")
	var ok, denied int
	for _, f := range agw {
		if f.Via != "agentgateway-system/ar-ingress" || f.SrcIP != "10.244.1.1" || f.DstIP != "10.244.1.131" || f.Path != "/realms/a" {
			t.Fatalf("agw %+v", f)
		}
		if f.Verdict == "denied" {
			denied += f.Count
		} else {
			ok += f.Count
		}
	}
	if ok != 2 || denied != 1 {
		t.Fatalf("agentgateway: %+v", agw)
	}

	env := flowsBy(all, "envoy")
	if len(env) != 2 {
		t.Fatalf("envoy: %+v", env)
	}
	for _, f := range env {
		switch f.Via {
		case "":
			if f.SrcPod != "sleep-1" || f.Count != 2 || f.DstHost != "httpbin.foo.svc.cluster.local" {
				t.Fatalf("sidecar %+v", f)
			}
		case "bookinfo/waypoint":
			if f.SrcIP != "10.244.1.9" || f.DstHost != "reviews.bookinfo.svc.cluster.local" {
				t.Fatalf("waypoint %+v", f)
			}
		default:
			t.Fatalf("%+v", f)
		}
	}
	dns := flowsBy(all, "coredns")
	if len(dns) != 1 || dns[0].DstHost != "api.github.com" || dns[0].Count != 2 || dns[0].SrcIP != "10.244.1.5" {
		t.Fatalf("coredns: %+v", dns)
	}
	if got := w.FlowsFrom("c", "coredns"); len(got) != 1 {
		t.Fatalf("FlowsFrom %+v", got)
	}
	if got := w.FlowsFrom("other", ""); len(got) != 0 {
		t.Fatalf("cluster filter %+v", got)
	}
	// The table is queryable in the SQL console.
	res, err := w.SQL("SELECT source, sum(count) FROM flows GROUP BY ALL ORDER BY 1", 10)
	if err != nil || len(res.Rows) != 2 {
		t.Fatalf("%v %+v", err, res)
	}
}

func TestReviewerFlowFile(t *testing.T) {
	w := New()
	f, err := os.Open("../flows/testdata/generic.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	res, err := w.AddReviewerFlows("c", "generic.csv", f, "")
	if err != nil || res.Format != "generic" || res.Events != 18 {
		t.Fatalf("%v %+v", err, res)
	}
	fs := flowsBy(w.Flows("c"), "generic")
	if len(fs) != 3 || fs[0].Origin != OriginReviewer || fs[0].File != "generic.csv" || fs[0].Cluster != "c" {
		t.Fatalf("%+v", fs)
	}
	cov := w.Coverage()
	if len(cov) != 1 || cov[0].Type != bundle.CoverageFlows || !strings.Contains(cov[0].Reason, "not part of the signed bundle") {
		t.Fatalf("%+v", cov)
	}
	if _, err := w.AddReviewerFlows("c", "junk.txt", strings.NewReader("hello"), ""); err == nil || w.Coverage()[1].State != bundle.StateFailed {
		t.Fatalf("junk file: %v %+v", err, w.Coverage())
	}
}
