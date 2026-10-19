package mcp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServe(t *testing.T) {
	desk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/api/findings":
			_, _ = w.Write([]byte(`[{"id":"f1","severity":"critical","title":"checkout crash loop"},{"id":"f2","severity":"info","title":"api version"}]`))
		case "/api/logs/search":
			if r.URL.Query().Get("q") != "level:error" || r.URL.Query().Get("limit") != "50" {
				w.WriteHeader(400)
				return
			}
			_, _ = w.Write([]byte(`{"total":1,"records":[{"id":7,"msg":"boom"}]}`))
		case "/api/proxies":
			_, _ = w.Write([]byte(`{"components":[{"cluster":"c","kind":"proxies","namespace":"istio-system","pod":"ztunnel-a"},{"cluster":"c","kind":"proxies","namespace":"istio-system","pod":"ztunnel-b"}]}`))
		case "/api/proxy":
			_, _ = w.Write([]byte(`{"summary":{"counts":{"workloads":3}}}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer desk.Close()
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_findings","arguments":{"severity":"critical"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"search_logs","arguments":{"query":"level:error"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"proxy_config","arguments":{"pod":"ztunnel"}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"proxy_config","arguments":{"pod":"ztunnel-a"}}}`,
		`{"jsonrpc":"2.0","id":7,"method":"nope"}`,
		`not json`,
	}, "\n")
	var out bytes.Buffer
	s := &Server{BaseURL: desk.URL, Token: "tok", Version: "test"}
	if err := s.Serve(strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	var rs []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var r map[string]any
		if err := json.Unmarshal([]byte(l), &r); err != nil {
			t.Fatal(err)
		}
		rs = append(rs, r)
	}
	if len(rs) != 8 { // the notification gets no reply
		t.Fatalf("expected 8 replies, got %d: %s", len(rs), out.String())
	}
	text := func(i int) (string, bool) {
		res := rs[i]["result"].(map[string]any)
		return res["content"].([]any)[0].(map[string]any)["text"].(string), res["isError"] == true
	}
	if v := rs[0]["result"].(map[string]any)["protocolVersion"]; v != "2025-03-26" {
		t.Fatalf("should speak the client's older revision, got %v", v)
	}
	if n := len(rs[1]["result"].(map[string]any)["tools"].([]any)); n != 29 {
		t.Fatalf("tools %d", n)
	}
	if s, _ := text(2); !strings.Contains(s, "crash loop") || strings.Contains(s, "api version") {
		t.Fatalf("severity filter: %s", s)
	}
	if s, isErr := text(3); isErr || !strings.Contains(s, "boom") {
		t.Fatalf("search: %s", s)
	}
	if s, isErr := text(4); !isErr || !strings.Contains(s, "matches 2 pods") {
		t.Fatalf("ambiguous pod should be a tool error: %s", s)
	}
	if s, isErr := text(5); isErr || !strings.Contains(s, "workloads") {
		t.Fatalf("proxy: %s", s)
	}
	if rs[6]["error"].(map[string]any)["code"].(float64) != -32601 || rs[7]["error"].(map[string]any)["code"].(float64) != -32700 {
		t.Fatalf("errors: %v %v", rs[6], rs[7])
	}
}
