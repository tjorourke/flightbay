package flows

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjorourke/flightbay/internal/logs"
)

func agwFields(t *testing.T, line string) map[string]string {
	t.Helper()
	_, rest := logs.SplitTimestamp("2026-09-27T21:59:59.000000001Z " + line) // kubelet prefix, as captured
	p := logs.Parse(rest)
	if p.Scope != "request" {
		t.Fatalf("scope %q for %s", p.Scope, line)
	}
	return p.Fields
}

func TestAgentgatewayTextLine(t *testing.T) {
	// A real line from a capture (mesh1, ar-ingress).
	line := `2026-09-27T21:50:44.861295Z  info  request gateway=agentgateway-system/ar-ingress listener=http route=ar-keycloak/keycloak endpoint=10.244.1.131:8080 src.addr=10.244.1.1:29278 http.method=GET http.host=keycloak.192.168.97.148.sslip.io http.path=/realms/master/protocol/openid-connect/certs?x=1 http.version=HTTP/1.1 http.status=200 protocol=http duration=45ms`
	f, ok := FromAgentgateway(agwFields(t, line))
	if !ok {
		t.Fatal("not recognised")
	}
	want := Flow{Source: "agentgateway", SrcIP: "10.244.1.1", DstIP: "10.244.1.131", DstPort: 8080, DstHost: "keycloak.192.168.97.148.sslip.io",
		Protocol: "http", Method: "GET", Path: "/realms/master/protocol/openid-connect/certs", Route: "ar-keycloak/keycloak", Via: "agentgateway-system/ar-ingress", Count: 1}
	if f != want {
		t.Fatalf("got  %+v\nwant %+v", f, want)
	}
}

func TestAgentgatewayMCPAndDenied(t *testing.T) {
	line := `2026-09-27T21:51:02.100000Z	info	request gateway=kagent/agw listener=mcp route=kagent/github-mcp endpoint=10.244.2.40:3000 src.addr=10.244.1.77:51000 http.method=POST http.host=agw.kagent.svc.cluster.local http.path=/mcp http.status=200 jwt.sub=system:serviceaccount:kagent:demo-agent protocol=mcp mcp.method.name=tools/call mcp.target=github gen_ai.tool.name=create_issue mcp.session.id=abc duration=120ms`
	f, _ := FromAgentgateway(agwFields(t, line))
	if f.Protocol != "mcp" || f.Method != "tools/call" || f.Tool != "github/create_issue" || f.SrcIdentity != "system:serviceaccount:kagent:demo-agent" || f.DstNamespace != "kagent" {
		t.Fatalf("%+v", f)
	}
	denied := `2026-09-27T21:51:03Z info request gateway=kagent/agw listener=mcp route=kagent/github-mcp src.addr=10.244.1.77:51002 http.method=POST http.host=agw.kagent.svc.cluster.local http.path=/mcp http.status=403 mcp.method.name=tools/call gen_ai.tool.name=delete_repo duration=1ms`
	f, _ = FromAgentgateway(agwFields(t, denied))
	if f.Verdict != Denied || f.Tool != "delete_repo" || f.DstIP != "" {
		t.Fatalf("%+v", f)
	}
}

func TestAgentgatewayJSONAndLLM(t *testing.T) {
	line := `{"level":"info","time":"2026-09-27T21:52:00.000Z","scope":"request","gateway":"agentgateway-system/llm","listener":"http","route":"agentgateway-system/openai","endpoint":"api.openai.com:443","src.addr":"10.244.1.9:40000","http.method":"POST","http.host":"llm.example.com","http.path":"/v1/chat/completions","http.status":200,"gen_ai.provider.name":"openai","gen_ai.request.model":"gpt-4o-mini","duration":"812ms"}`
	f, ok := FromAgentgateway(agwFields(t, line))
	if !ok || f.Protocol != "llm" || f.DstHost != "llm.example.com" || f.DstPort != 443 || f.Via != "agentgateway-system/llm" || f.Method != "POST" {
		t.Fatalf("%+v", f)
	}
	noHost := map[string]string{"gateway": "g/a", "src.addr": "10.0.0.1:1", "gen_ai.provider.name": "anthropic", "http.status": "401"}
	f, _ = FromAgentgateway(noHost)
	if f.DstHost != "anthropic" || f.Verdict != Denied {
		t.Fatalf("%+v", f)
	}
	if _, ok := FromAgentgateway(map[string]string{"msg": "x"}); ok {
		t.Fatal("non-request fields accepted")
	}
}

var sidecar = EnvoyPod{Namespace: "foo", Pod: "sleep-6c9f7c-abcde"}

func TestEnvoySidecarOutboundAndInbound(t *testing.T) {
	// Istio docs, "Envoy Access Logs" task.
	out := `[2020-11-25T21:26:18.409Z] "GET /status/418 HTTP/1.1" 418 - via_upstream - "-" 0 135 4 4 "-" "curl/7.73.0-DEV" "84961386-6d84-929d-98bd-c5aee93b5c88" "httpbin:8000" "10.44.1.27:80" outbound|8000||httpbin.foo.svc.cluster.local 10.44.1.23:37652 10.0.45.184:8000 10.44.1.23:46520 - default`
	f, ok := ParseEnvoy(out, sidecar)
	if !ok {
		t.Fatal("outbound not parsed")
	}
	want := Flow{Source: "envoy", SrcIP: "10.44.1.23", SrcNamespace: "foo", SrcPod: "sleep-6c9f7c-abcde", DstIP: "10.44.1.27", DstPort: 8000,
		DstHost: "httpbin.foo.svc.cluster.local", DstNamespace: "foo", Protocol: "http", Method: "GET", Path: "/status/418", Route: "default", Count: 1, Direction: "outbound"}
	if f != want {
		t.Fatalf("got  %+v\nwant %+v", f, want)
	}
	in := `[2020-11-25T21:26:18.409Z] "GET /status/418?x=1 HTTP/1.1" 418 - via_upstream - "-" 0 135 3 1 "-" "curl/7.73.0-DEV" "84961386-6d84-929d-98bd-c5aee93b5c88" "httpbin:8000" "127.0.0.1:80" inbound|8000|| 127.0.0.1:41854 10.44.1.27:80 10.44.1.23:37652 outbound_.8000_._.httpbin.foo.svc.cluster.local default`
	f, _ = ParseEnvoy(in, EnvoyPod{Namespace: "foo", Pod: "httpbin-74fb669cc6-q9zkc"})
	if f.Direction != "inbound" || f.SrcIP != "10.44.1.23" || f.SrcPod != "" || f.DstWorkload != "httpbin-74fb669cc6-q9zkc" || f.DstPort != 8000 || f.Path != "/status/418" {
		t.Fatalf("%+v", f)
	}
}

func TestEnvoyDeniedPassthroughAndBlackHole(t *testing.T) {
	rbac := `[2023-06-01T10:00:00.000Z] "GET /headers HTTP/1.1" 403 - rbac_access_denied_matched_policy[none] - "-" 0 19 0 - "-" "curl/8.1.2" "2f0b3c4e-1111-2222-3333-444455556666" "httpbin.foo:8000" "-" inbound|80|| - 10.244.0.12:80 10.244.0.11:45678 outbound_.8000_._.httpbin.foo.svc.cluster.local -`
	f, ok := ParseEnvoy(rbac, EnvoyPod{Namespace: "foo", Pod: "httpbin-1"})
	if !ok || f.Verdict != Denied || f.SrcIP != "10.244.0.11" {
		t.Fatalf("%+v", f)
	}
	pass := `[2023-06-01T10:00:01.000Z] "- - -" 0 - - - "-" 1234 5678 900 - "-" "-" "-" "-" "140.82.121.4:443" PassthroughCluster 10.244.0.11:40000 140.82.121.4:443 10.244.0.11:39998 github.com -`
	f, _ = ParseEnvoy(pass, sidecar)
	if f.Protocol != "tcp" || f.DstIP != "140.82.121.4" || f.DstPort != 443 || f.DstHost != "github.com" || f.SrcPod != sidecar.Pod || f.Verdict != "" {
		t.Fatalf("%+v", f)
	}
	bh := `[2023-06-01T10:00:02.000Z] "- - -" 0 UH - - "-" 0 0 0 - "-" "-" "-" "-" "-" BlackHoleCluster - 140.82.121.4:443 10.244.0.11:39998 github.com -`
	f, _ = ParseEnvoy(bh, sidecar)
	if f.Verdict != Denied || f.DstHost != "github.com" || f.DstPort != 443 {
		t.Fatalf("%+v", f)
	}
}

func TestEnvoyGatewayWaypointAndJSON(t *testing.T) {
	gw := EnvoyPod{Namespace: "istio-ingress", Pod: "istio-ingressgateway-5c8b-xyz", Gateway: true, Via: "istio-ingress/istio-ingressgateway"}
	ing := `[2024-02-01T09:00:00.000Z] "GET /productpage HTTP/1.1" 200 - via_upstream - "-" 0 5293 29 28 "203.0.113.50" "Mozilla/5.0" "d0f01c0a-1111-2222-3333-444455556666" "bookinfo.example.com" "10.244.1.9:9080" outbound|9080||productpage.bookinfo.svc.cluster.local 10.244.0.20:51234 10.244.0.20:8080 10.244.0.1:40000 - -`
	f, _ := ParseEnvoy(ing, gw)
	if f.SrcPod != "" || f.SrcIP != "203.0.113.50" || f.Via != gw.Via || f.DstHost != "productpage.bookinfo.svc.cluster.local" || f.DstNamespace != "bookinfo" {
		t.Fatalf("%+v", f)
	}
	wp := EnvoyPod{Namespace: "bookinfo", Pod: "waypoint-7b5d-abc", Gateway: true, Via: "bookinfo/waypoint"}
	vip := `[2024-02-01T09:00:01.000Z] "GET /reviews/0 HTTP/1.1" 200 - via_upstream - "-" 0 358 12 11 "-" "Mozilla/5.0" "a1b2c3d4-1111-2222-3333-444455556666" "reviews:9080" "10.244.1.14:9080" inbound-vip|9080|http|reviews.bookinfo.svc.cluster.local envoy://internal_client_address/ 10.96.44.10:9080 10.244.1.9:52000 - default`
	f, _ = ParseEnvoy(vip, wp)
	if f.Direction != "inbound" || f.DstHost != "reviews.bookinfo.svc.cluster.local" || f.SrcIP != "10.244.1.9" || f.Via != "bookinfo/waypoint" {
		t.Fatalf("%+v", f)
	}
	tunnel := `[2024-02-01T09:00:01.000Z] "- - -" 0 - - - "-" 100 200 12 - "-" "-" "-" "-" "10.244.1.14:15008" connect_originate 10.244.2.2:40000 10.244.1.14:9080 10.244.1.9:52000 - -`
	if _, ok := ParseEnvoy(tunnel, wp); ok {
		t.Fatal("HBONE tunnel leg should be skipped")
	}
	js := `{"authority":"artifactory.tools:8081","bytes_received":0,"bytes_sent":1024,"downstream_local_address":"10.96.3.4:8081","downstream_remote_address":"10.244.1.30:49876","duration":7,"method":"GET","path":"/artifactory/api/npm/npm/react?token=x","protocol":"HTTP/1.1","request_id":"c1","requested_server_name":null,"response_code":200,"response_flags":"-","route_name":"default","start_time":"2024-02-01T09:00:02.000Z","upstream_cluster":"outbound|8081||artifactory.tools.svc.cluster.local","upstream_host":"10.244.3.3:8081","upstream_local_address":"10.244.1.30:50000","user_agent":"npm/10"}`
	f, ok := ParseEnvoy(js, EnvoyPod{Namespace: "ci", Pod: "builder-0"})
	if !ok || f.DstHost != "artifactory.tools.svc.cluster.local" || f.DstPort != 8081 || f.DstNamespace != "tools" || f.SrcPod != "builder-0" || f.Path != "/artifactory/api/npm/npm/react" {
		t.Fatalf("%+v", f)
	}
	if _, ok := ParseEnvoy(`2024-02-01T09:00:00Z info envoy config  gRPC config stream closed`, sidecar); ok {
		t.Fatal("non-access line accepted")
	}
}

func TestCoreDNS(t *testing.T) {
	f, ok := ParseCoreDNS(`[INFO] 10.244.1.5:41234 - 4242 "A IN api.github.com. udp 32 false 512" NOERROR qr,rd,ra 110 0.0003s`)
	want := Flow{Source: "coredns", Protocol: "dns", SrcIP: "10.244.1.5", DstHost: "api.github.com", Method: "A", Count: 1}
	if !ok || f != want {
		t.Fatalf("got %+v want %+v", f, want)
	}
	f, ok = ParseCoreDNS(`[INFO] [fd00:10:244::5]:50759 - 29008 "AAAA IN Example.ORG. udp 41 false 4096" NOERROR qr,rd,ra,ad 68 0.037990251s`)
	if !ok || f.SrcIP != "fd00:10:244::5" || f.DstHost != "example.org" || f.Method != "AAAA" {
		t.Fatalf("%+v", f)
	}
	for _, skip := range []string{
		`[INFO] 10.244.0.5:48329 - 8347 "AAAA IN kubernetes.default.svc.cluster.local. udp 54 false 512" NOERROR qr,aa,rd 147 0.000139s`,
		`[INFO] 10.244.0.5:48329 - 8348 "A IN api.github.com.default.svc.cluster.local. udp 58 false 512" NXDOMAIN qr,aa,rd 151 0.0001s`,
		`[INFO] 10.244.0.5:48329 - 8349 "A IN api.github.com.eu-west-2.compute.internal. udp 58 false 512" NXDOMAIN qr,rd,ra 151 0.0001s`,
		`[INFO] 10.244.0.5:48329 - 8350 "PTR IN 10.0.96.10.in-addr.arpa. udp 41 false 512" NOERROR qr,aa,rd 116 0.0001s`,
		// Seen live on kind: the loop plugin's probe, and busybox trying the bare service.namespace name.
		`[INFO] 127.0.0.1:45416 - 16595 "HINFO IN 3447766166934586325.5955448503077033232. udp 57 false 512" NXDOMAIN qr,rd,ra 57 0.038222135s`,
		`[INFO] 10.244.0.7:53750 - 8316 "A IN kubernetes.default. udp 36 false 512" NXDOMAIN qr,rd,ra 36 0.020673965s`,
		`[INFO] plugin/reload: Running configuration SHA512 = 591cf328cccc12bc490481273e738df59329c62c0b729d94e8b61db9961c2fa5`,
	} {
		if f, ok := ParseCoreDNS(skip); ok {
			t.Errorf("should skip %s: %+v", skip, f)
		}
	}
	if _, ok := ParseCoreDNS(`[INFO] 10.0.0.1:1 - 1 "A IN db.payments. udp 1 false 512" NXDOMAIN - 1 0s`, "payments"); ok {
		t.Error("captured namespace not treated as search-path noise")
	}
	if _, ok := ParseCoreDNS(`[INFO] 10.0.0.1:1 - 1 "A IN db.corp.example. udp 1 false 512" NOERROR - 1 0s`, "corp.example"); ok {
		t.Error("extra search domain not skipped")
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func parseFixture(t *testing.T, name, wantFormat string) []Flow {
	t.Helper()
	b := readFixture(t, name)
	if got := Detect(b); got != wantFormat {
		t.Fatalf("%s: detected %q, want %q", name, got, wantFormat)
	}
	fs, format, err := ParseFile(bytes.NewReader(b), "")
	if err != nil || format != wantFormat {
		t.Fatalf("%s: %v %s", name, err, format)
	}
	return fs
}

func find(fs []Flow, pred func(Flow) bool) *Flow {
	for i := range fs {
		if pred(fs[i]) {
			return &fs[i]
		}
	}
	return nil
}

func TestHubble(t *testing.T) {
	fs := parseFixture(t, "hubble.jsonl", FmtHubble)
	// xwing -> deathstar forwarded twice (snake_case and camelCase lines), dropped once; the reply is skipped.
	fw := find(fs, func(f Flow) bool { return f.SrcPod == "xwing" && f.DstPort == 80 && f.Verdict == Allowed })
	if fw == nil || fw.Count != 2 || fw.DstWorkload != "deathstar-7848d6c4d5-8hhsr" || fw.Protocol != "tcp" || fw.DstNamespace != "default" {
		t.Fatalf("forwarded: %+v in %+v", fw, fs)
	}
	if d := find(fs, func(f Flow) bool { return f.SrcPod == "xwing" && f.Verdict == Denied }); d == nil || d.Count != 1 {
		t.Fatalf("dropped: %+v", fs)
	}
	h := find(fs, func(f Flow) bool { return f.Protocol == "http" })
	if h == nil || h.Method != "POST" || h.Path != "/v1/request-landing" || h.DstHost != "deathstar.default.svc.cluster.local" || h.SrcPod != "tiefighter" {
		t.Fatalf("l7 http: %+v", h)
	}
	if d := find(fs, func(f Flow) bool { return f.Protocol == "dns" }); d == nil || d.DstHost != "api.github.com" || d.Method != "A" {
		t.Fatalf("dns: %+v", d)
	}
	if w := find(fs, func(f Flow) bool { return f.DstIP == "140.82.121.6" }); w == nil || w.DstHost != "api.github.com" || w.DstPort != 443 {
		t.Fatalf("world: %+v", w)
	}
	if len(fs) != 5 {
		t.Fatalf("want 5 aggregated flows, got %d: %+v", len(fs), fs)
	}
}

func TestAWSVPC(t *testing.T) {
	fs := parseFixture(t, "aws-vpc.log", FmtAWS)
	ssh := find(fs, func(f Flow) bool { return f.DstPort == 22 })
	if ssh == nil || ssh.Count != 2 || ssh.SrcIP != "172.31.16.139" || ssh.Protocol != "tcp" || ssh.Verdict != Allowed {
		t.Fatalf("%+v", fs)
	}
	if r := find(fs, func(f Flow) bool { return f.DstPort == 3389 }); r == nil || r.Verdict != Denied {
		t.Fatalf("%+v", fs)
	}
	if len(fs) != 2 {
		t.Fatalf("NODATA/SKIPDATA should be dropped: %+v", fs)
	}
	fs = parseFixture(t, "aws-vpc-custom.log", FmtAWS)
	// pkt-srcaddr wins over srcaddr (traffic through a NAT gateway).
	if n := find(fs, func(f Flow) bool { return f.SrcIP == "10.40.2.236" }); n == nil || n.DstIP != "10.20.33.164" || n.DstPort != 43416 {
		t.Fatalf("%+v", fs)
	}
	var p AWSParser
	if _, ok := p.Line("${version} ${srcaddr} ${dstaddr} ${dstport} ${protocol} ${action}"); ok {
		t.Fatal("header returned a flow")
	}
	if f, ok := p.Line("5 10.0.0.1 10.0.0.2 443 6 ACCEPT"); !ok || f.DstPort != 443 {
		t.Fatalf("format-string header: %+v", f)
	}
}

func TestGeneric(t *testing.T) {
	fs := parseFixture(t, "generic.csv", FmtGeneric)
	s := find(fs, func(f Flow) bool { return f.DstHost == "api.stripe.com" })
	if s == nil || s.Count != 15 || s.SrcNamespace != "payments" || s.SrcPod != "checkout-7d9f8c-x2k" || s.DstPort != 443 || s.Verdict != Allowed {
		t.Fatalf("%+v", fs)
	}
	if d := find(fs, func(f Flow) bool { return f.DstHost == "github.com" }); d == nil || d.Verdict != Denied || d.Count != 2 || d.DstIP != "140.82.121.4" {
		t.Fatalf("%+v", fs)
	}
	if m := find(fs, func(f Flow) bool { return f.Method == "POST" }); m == nil || m.Path != "/mcp" || m.Count != 1 || m.Protocol != "http" {
		t.Fatalf("%+v", fs)
	}
	fs = parseFixture(t, "generic.jsonl", FmtGeneric)
	if a := find(fs, func(f Flow) bool { return f.SrcPod == "web-1" }); a == nil || a.DstHost != "s3.eu-west-2.amazonaws.com" || a.Count != 40 || a.Protocol != "tcp" || a.DstPort != 443 {
		t.Fatalf("%+v", fs)
	}
	if b := find(fs, func(f Flow) bool { return f.DstPort == 5432 }); b == nil || b.Verdict != Denied || b.Protocol != "tcp" {
		t.Fatalf("%+v", fs)
	}
}

func TestCalico(t *testing.T) {
	fs := parseFixture(t, "calico.jsonl", FmtCalico)
	if len(fs) != 2 {
		t.Fatalf("dst-reported allow between workloads should be skipped: %+v", fs)
	}
	a := find(fs, func(f Flow) bool { return f.Verdict == Allowed })
	if a == nil || a.Count != 3 || a.SrcPod != "frontend-7d9f8c-*" || a.DstWorkload != "checkout-5c6b-*" || a.DstHost != "checkout.shop.svc.cluster.local" || a.DstPort != 8080 {
		t.Fatalf("%+v", a)
	}
	d := find(fs, func(f Flow) bool { return f.Verdict == Denied })
	if d == nil || d.DstHost != "dynamodb.eu-west-2.amazonaws.com" || d.DstNamespace != "" || d.DstIP != "52.94.236.248" {
		t.Fatalf("%+v", d)
	}
}

func TestGCP(t *testing.T) {
	fs := parseFixture(t, "gcp.json", FmtGCP)
	if len(fs) != 2 {
		t.Fatalf("DEST duplicate should be skipped: %+v", fs)
	}
	a := find(fs, func(f Flow) bool { return f.DstPort == 8080 })
	if a == nil || a.SrcNamespace != "shop" || a.SrcPod != "web-6d4b7" || a.DstWorkload != "api-7f9c2" || a.DstHost != "api.shop.svc.cluster.local" || a.Protocol != "tcp" {
		t.Fatalf("%+v", a)
	}
	if d := find(fs, func(f Flow) bool { return f.DstPort == 443 }); d == nil || d.Verdict != Denied {
		t.Fatalf("%+v", fs)
	}
	// JSON Lines of the bare payload also work.
	if f, ok := ParseGCP([]byte(`{"connection":{"src_ip":"10.0.0.1","dest_ip":"10.0.0.2","dest_port":53,"protocol":17},"reporter":"SRC"}`)); !ok || f.Protocol != "udp" {
		t.Fatalf("%+v", f)
	}
}

func TestAzure(t *testing.T) {
	fs := parseFixture(t, "azure-nsg.json", FmtAzure)
	// Two denied inbound, one begun outbound; the E and C tuples are updates.
	if len(fs) != 3 {
		t.Fatalf("%+v", fs)
	}
	if d := find(fs, func(f Flow) bool { return f.DstPort == 443 && f.Verdict == Denied }); d == nil || d.Protocol != "udp" || d.SrcIP != "94.102.49.190" {
		t.Fatalf("%+v", fs)
	}
	fs = parseFixture(t, "azure-vnet.json", FmtAzure)
	if len(fs) != 3 {
		t.Fatalf("%+v", fs)
	}
	if d := find(fs, func(f Flow) bool { return f.Verdict == Denied }); d == nil || d.DstPort != 22 || d.Route != "DefaultRule_DenyAllInBound" {
		t.Fatalf("%+v", fs)
	}
}

func TestDetectUnknown(t *testing.T) {
	for _, s := range []string{"", "hello world\n", `{"foo":1}`, "a,b,c\n1,2,3\n"} {
		if f := Detect([]byte(s)); f != "" {
			t.Errorf("%q detected as %s", s, f)
		}
	}
	if _, _, err := ParseFile(strings.NewReader("nothing here"), ""); err == nil {
		t.Error("expected an error for an unknown format")
	}
}

func TestFilter(t *testing.T) {
	allowed := func(ns string) bool { return ns != "kube-system" }
	scrub := func(s string) string { return strings.ReplaceAll(s, "token=abc", "token=[REDACTED]") }
	out, dropped, err := Filter(readFixture(t, "hubble.jsonl"), FmtHubble, allowed, scrub)
	if err != nil || dropped != 1 || bytes.Contains(out, []byte("kube-system")) || bytes.Contains(out, []byte("token=abc")) {
		t.Fatalf("dropped %d err %v\n%s", dropped, err, out)
	}
	out, dropped, _ = Filter(readFixture(t, "gcp.json"), FmtGCP, func(ns string) bool { return ns != "shop" }, nil)
	if dropped != 2 || Detect(out) != FmtGCP {
		t.Fatalf("gcp: dropped %d\n%s", dropped, out)
	}
	out, dropped, _ = Filter(readFixture(t, "generic.csv"), FmtGeneric, func(ns string) bool { return ns != "kagent" }, nil)
	if dropped != 2 || bytes.Contains(out, []byte("demo-agent")) || !bytes.HasPrefix(out, []byte("src_ip,")) {
		t.Fatalf("csv: dropped %d\n%s", dropped, out)
	}
	out, dropped, _ = Filter(readFixture(t, "aws-vpc.log"), FmtAWS, allowed, nil)
	if dropped != 0 || !bytes.Equal(out, readFixture(t, "aws-vpc.log")) {
		t.Fatal("aws content changed")
	}
}

func TestAggregateAndNames(t *testing.T) {
	fs := Aggregate([]Flow{{Source: "x", SrcIP: "1.1.1.1", DstHost: "a"}, {Source: "x", SrcIP: "1.1.1.1", DstHost: "a", Count: 4}, {Source: "x", SrcIP: "1.1.1.1", DstHost: "b"}})
	if len(fs) != 2 || fs[0].Count != 5 {
		t.Fatalf("%+v", fs)
	}
	if n := (Flow{SrcNamespace: "a", SrcPod: "p", DstHost: "h", DstPort: 443}); n.SrcName() != "a/p" || n.DstName() != "h:443" {
		t.Fatal(n.SrcName(), n.DstName())
	}
}
