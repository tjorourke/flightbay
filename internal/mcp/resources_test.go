package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubDesk answers the desk API calls the resources, completions and
// prompts make, with a snapshot of one pod that restarted.
func stubDesk(t *testing.T) *Server {
	desk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch r.URL.Path {
		case "/api/workspace":
			_, _ = w.Write([]byte(`{"case":"42","clusters":[{"name":"prod"}]}`))
		case "/api/findings":
			_, _ = w.Write([]byte(`[{"id":"f1","severity":"critical","title":"checkout crash loop","cluster":"prod","object":{"cluster":"prod","group":"","resource":"pods","kind":"Pod","namespace":"shop","name":"checkout-1"}}]`))
		case "/api/posture":
			_, _ = w.Write([]byte(`{"areas":[{"area":"ambient","score":40}],"checks":[{"id":"mtls-strict","status":"fail","items":[{"name":"a"},{"name":"b"}],"fix":"big"},{"id":"other","status":"pass"}]}`))
		case "/api/coverage":
			_, _ = w.Write([]byte(`{"coverage":[
				{"cluster":"prod","type":"logs","namespace":"shop","pod":"checkout-1","container":"app","stream":"current","items":3,"state":"complete"},
				{"cluster":"prod","type":"logs","namespace":"shop","pod":"checkout-1","container":"app","stream":"previous","items":2,"state":"complete"},
				{"cluster":"prod","type":"logs","namespace":"pay","pod":"api-1","container":"api","stream":"current","items":1,"state":"complete"},
				{"cluster":"prod","type":"logs","namespace":"pay","pod":"gone","container":"x","stream":"current","items":0,"state":"excluded"},
				{"cluster":"prod","type":"resource","resource":"secrets","state":"excluded"}]}`))
		case "/api/timeline":
			_, _ = w.Write([]byte(`[{"time":"2026-09-28T10:00:00Z","kind":"rollout","title":"a"},{"time":"2026-09-28T11:00:00Z","kind":"termination","title":"b"}]`))
		case "/api/proxies":
			_, _ = w.Write([]byte(`{"components":[{"cluster":"prod","kind":"proxies","namespace":"istio-system","pod":"ztunnel-a","component":"ztunnel","files":[{"name":"config_dump.json","bytes":10,"state":"complete"},{"name":"failed.json","state":"failed"}]}]}`))
		case "/api/proxy/file":
			if q.Get("pod") != "ztunnel-a" || q.Get("file") != "config_dump.json" {
				w.WriteHeader(404)
				return
			}
			_, _ = w.Write([]byte(`{"workloads":[]}`))
		case "/api/object":
			if q.Get("group") != "" || q.Get("namespace") != "shop" || q.Get("name") != "checkout-1" {
				w.WriteHeader(404)
				return
			}
			_, _ = w.Write([]byte(`{"object":{"cluster":"prod","resource":"pods","kind":"Pod","namespace":"shop","name":"checkout-1"},"yaml":"kind: Pod\n","json":"{}",
				"events":[{"type":"Warning","reason":"BackOff","count":4,"message":"restarting"}],"streams":["app/current","app/previous"],
				"related":[{"relation":"owner","cluster":"prod","group":"apps","resource":"replicasets","kind":"ReplicaSet","namespace":"shop","name":"checkout-rs"}]}`))
		case "/api/logs/search":
			if q.Get("pod") != "checkout-1" || q.Get("stream") != "previous" || q.Get("limit") != "2000" {
				w.WriteHeader(400)
				return
			}
			// newest first, as the desk returns them
			_, _ = w.Write([]byte(`{"total":5,"records":[{"id":9,"seq":2,"ts":"2026-09-28T11:00:02Z","raw":"panic: boom"},{"id":8,"seq":1,"ts":"2026-09-28T11:00:01Z","raw":"starting"}]}`))
		case "/api/resources":
			_, _ = w.Write([]byte(`[{"group":"","resource":"pods"},{"group":"apps","resource":"deployments"}]`))
		case "/api/objects":
			_, _ = w.Write([]byte(`{"rows":[{"namespace":"shop","name":"checkout-1"},{"namespace":"pay","name":"api-1"}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(desk.Close)
	return &Server{BaseURL: desk.URL, Token: "tok"}
}

func call(t *testing.T, s *Server, method string, params any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(params)
	res, rerr := s.handle(request{Method: method, Params: b})
	if rerr != nil {
		t.Fatalf("%s: %s", method, rerr.Message)
	}
	j, _ := json.Marshal(res)
	var m map[string]any
	_ = json.Unmarshal(j, &m)
	return m
}

func TestResources(t *testing.T) {
	s := stubDesk(t)
	caps := call(t, s, "initialize", map[string]any{})["capabilities"].(map[string]any)
	if caps["resources"] == nil || caps["completions"] == nil {
		t.Fatalf("capabilities %v", caps)
	}

	var uris []string
	for _, r := range call(t, s, "resources/list", map[string]any{})["resources"].([]any) {
		uris = append(uris, r.(map[string]any)["uri"].(string))
	}
	all := strings.Join(uris, "\n")
	for _, want := range []string{"flightbay://workspace", "flightbay://findings/f1", "flightbay://proxies/prod/proxies/istio-system/ztunnel-a/config_dump.json",
		"flightbay://logs/prod/shop/checkout-1/app/previous", "flightbay://logs/prod/pay/api-1/api/current"} {
		if !strings.Contains(all, want) {
			t.Errorf("resources/list is missing %s", want)
		}
	}
	if strings.Contains(all, "failed.json") || strings.Contains(all, "/gone/") {
		t.Errorf("uncollected files and streams are not resources:\n%s", all)
	}
	if n := len(call(t, s, "resources/templates/list", nil)["resourceTemplates"].([]any)); n != 3 {
		t.Errorf("templates %d", n)
	}

	read := func(u string) string {
		t.Helper()
		return call(t, s, "resources/read", map[string]any{"uri": u})["contents"].([]any)[0].(map[string]any)["text"].(string)
	}
	if got := read(ObjectURI("prod", "", "pods", "shop", "checkout-1")); !strings.Contains(got, "kind: Pod") || !strings.Contains(got, "BackOff x4: restarting") {
		t.Errorf("object: %s", got)
	}
	logs := read("flightbay://logs/prod/shop/checkout-1/app/previous")
	if !strings.Contains(logs, "newest 2 of 5") || strings.Index(logs, "starting") > strings.Index(logs, "panic") || !strings.Contains(logs, "[id 9]") {
		t.Errorf("log stream should read oldest first with ids: %s", logs)
	}
	if got := read("flightbay://findings/f1"); !strings.Contains(got, "crash loop") {
		t.Errorf("finding: %s", got)
	}
	if got := read("flightbay://scorecard"); !strings.Contains(got, `"objects": 2`) || strings.Contains(got, "big") || strings.Contains(got, `"other"`) {
		t.Errorf("scorecard index: %s", got)
	}
	if !strings.Contains(all, "flightbay://scorecard/mtls-strict") || strings.Contains(all, "scorecard/other") {
		t.Errorf("failing checks are resources, passing ones are not:\n%s", all)
	}
	if got := read("flightbay://scorecard/mtls-strict"); !strings.Contains(got, "fail") || strings.Contains(got, "other") {
		t.Errorf("check: %s", got)
	}
	if got := read("flightbay://proxies/prod/proxies/istio-system/ztunnel-a/config_dump.json"); got != `{"workloads":[]}` {
		t.Errorf("proxy file should come back as captured: %s", got)
	}
	for _, bad := range []string{"https://x", "flightbay://nope", "flightbay://objects/prod/pods", "flightbay://findings/zzz"} {
		b, _ := json.Marshal(map[string]any{"uri": bad})
		if _, rerr := s.handle(request{Method: "resources/read", Params: b}); rerr == nil {
			t.Errorf("%s should be an error", bad)
		}
	}
	// segments round-trip through escaping
	u := ObjectURI("prod", "gateway.networking.k8s.io", "httproutes", "", "a/b c")
	if k, p, _ := parseURI(u); k != "objects" || p[1] != "gateway.networking.k8s.io" || p[3] != "-" || p[4] != "a/b c" {
		t.Errorf("parse %s: %v", u, p)
	}
}

func TestCompletions(t *testing.T) {
	s := stubDesk(t)
	complete := func(ref map[string]any, arg, val string, ctx map[string]string) []string {
		t.Helper()
		c := call(t, s, "completion/complete", map[string]any{"ref": ref, "argument": map[string]any{"name": arg, "value": val}, "context": map[string]any{"arguments": ctx}})
		var out []string
		for _, v := range c["completion"].(map[string]any)["values"].([]any) {
			out = append(out, v.(string))
		}
		return out
	}
	logs := map[string]any{"type": "ref/resource", "uri": templates[1].URITemplate}
	if got := strings.Join(complete(logs, "namespace", "", nil), ","); got != "pay,shop" {
		t.Errorf("namespaces %s", got)
	}
	if got := strings.Join(complete(logs, "pod", "", map[string]string{"namespace": "shop"}), ","); got != "checkout-1" {
		t.Errorf("pods narrowed by namespace %s", got)
	}
	if got := strings.Join(complete(logs, "stream", "p", map[string]string{"pod": "checkout-1", "container": "app"}), ","); got != "previous" {
		t.Errorf("streams by prefix %s", got)
	}
	objects := map[string]any{"type": "ref/resource", "uri": templates[0].URITemplate}
	if got := strings.Join(complete(objects, "group", "", nil), ","); got != "apps,core" {
		t.Errorf("groups %s", got)
	}
	if got := strings.Join(complete(objects, "cluster", "", nil), ","); got != "prod" {
		t.Errorf("clusters %s", got)
	}
	if got := strings.Join(complete(objects, "name", "", map[string]string{"resource": "pods", "namespace": "pay"}), ","); got != "api-1" {
		t.Errorf("names %s", got)
	}
	proxies := map[string]any{"type": "ref/resource", "uri": templates[2].URITemplate}
	if got := strings.Join(complete(proxies, "file", "", map[string]string{"pod": "ztunnel-a"}), ","); got != "config_dump.json,failed.json" {
		t.Errorf("files %s", got)
	}
	if got := strings.Join(complete(map[string]any{"type": "ref/prompt", "name": "pod_debug"}, "pod", "api", nil), ","); got != "api-1" {
		t.Errorf("prompt pods %s", got)
	}
}

func TestPromptsAndLinks(t *testing.T) {
	s := stubDesk(t)
	ps := call(t, s, "prompts/list", nil)["prompts"].([]any)
	if len(ps) != 8 {
		t.Fatalf("prompts %d", len(ps))
	}
	get := func(name string, args map[string]string) []any {
		t.Helper()
		return call(t, s, "prompts/get", map[string]any{"name": name, "arguments": args})["messages"].([]any)
	}
	m := get("triage", map[string]string{"symptom": "503s from checkout"})
	if txt := m[0].(map[string]any)["content"].(map[string]any)["text"].(string); !strings.HasPrefix(txt, "The customer reports: 503s") {
		t.Errorf("symptom: %s", txt)
	}
	if len(m) != 3 || m[1].(map[string]any)["content"].(map[string]any)["type"] != "resource" {
		t.Errorf("triage should embed the workspace and findings: %v", m)
	}
	if txt := get("pod_debug", map[string]string{"namespace": "shop", "pod": "checkout-1"})[0].(map[string]any)["content"].(map[string]any)["text"].(string); !strings.Contains(txt, "shop/checkout-1") {
		t.Errorf("pod_debug: %s", txt)
	}
	b, _ := json.Marshal(map[string]any{"name": "pod_debug", "arguments": map[string]string{"pod": "x"}})
	if _, rerr := s.handle(request{Method: "prompts/get", Params: b}); rerr == nil || !strings.Contains(rerr.Message, "namespace") {
		t.Errorf("missing argument: %v", rerr)
	}

	links := func(tool string, args map[string]any) []string {
		t.Helper()
		res := call(t, s, "tools/call", map[string]any{"name": tool, "arguments": args})
		var out []string
		for _, c := range res["content"].([]any) {
			if c.(map[string]any)["type"] == "resource_link" {
				out = append(out, c.(map[string]any)["uri"].(string))
			}
		}
		return out
	}
	if got := strings.Join(links("list_findings", nil), " "); got != "flightbay://findings/f1 flightbay://objects/prod/core/pods/shop/checkout-1" {
		t.Errorf("finding links: %s", got)
	}
	got := strings.Join(links("get_object", map[string]any{"cluster": "prod", "resource": "pods", "namespace": "shop", "name": "checkout-1"}), " ")
	if !strings.Contains(got, "flightbay://logs/prod/shop/checkout-1/app/previous") || !strings.Contains(got, "flightbay://objects/prod/apps/replicasets/shop/checkout-rs") {
		t.Errorf("object links: %s", got)
	}

	text := func(tool string, args map[string]any) string {
		return call(t, s, "tools/call", map[string]any{"name": tool, "arguments": args})["content"].([]any)[0].(map[string]any)["text"].(string)
	}
	if got := text("timeline", map[string]any{"since": "2026-09-28T10:30:00Z"}); strings.Contains(got, "rollout") || !strings.Contains(got, "termination") {
		t.Errorf("timeline since: %s", got)
	}
	if got := text("coverage", map[string]any{"state": "excluded", "type": "resource"}); !strings.Contains(got, "secrets") || strings.Contains(got, "checkout-1") {
		t.Errorf("coverage filter: %s", got)
	}
}
