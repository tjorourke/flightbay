package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAmbientToolsProtocolAndReadOnlyBoundary(t *testing.T) {
	posts := 0
	desk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/api/ambient":
			_, _ = w.Write([]byte(`{"objects":[{"cluster":"mesh1","namespace":"app","name":"petstore"},{"cluster":"mesh2","namespace":"app","name":"petstore"}],"services":[],"routes":[],"namespaces":[],"waypoints":[],"issues":[{"subject":{"cluster":"mesh1","namespace":"app","name":"petstore"},"step":{"state":"fail","id":"missing-parent"}},{"subject":{"cluster":"mesh2","namespace":"app","name":"other"},"step":{"state":"fail","id":"other"}}],"fixGuides":[{"id":"route-parent"}]}`))
		case "/api/ambient/investigate":
			if r.URL.Query().Get("cluster") != "mesh1" || r.URL.Query().Get("name") != "petstore" {
				t.Error("lost target identity")
			}
			_, _ = w.Write([]byte(`{"steps":[{"state":"fail","detail":"missing gateway"}],"dns":{"localName":"petstore.app.svc.cluster.local"}}`))
		case "/api/ambient/capabilities":
			_, _ = w.Write([]byte(`[{"id":"global-service","supported":true}]`))
		case "/api/ambient/plan":
			posts++
			if r.Method != "POST" || r.Header.Get("Origin") == "" {
				t.Error("preview POST needs same-origin headers")
			}
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "preview", "request": req, "yaml": "kind: HTTPRoute"})
		case "/api/ambient/tests":
			_, _ = w.Write([]byte(`[{"token":"SECRET-VALIDATION-TOKEN","owner":"PRIVATE-OWNER","context":"mesh1","plan":{"id":"plan-a"},"created":[],"modified":[]}]`))
		default:
			t.Errorf("unexpected or mutating endpoint: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer desk.Close()
	s := &Server{BaseURL: desk.URL, Token: "test-token"}
	invoke := func(name string, args map[string]any) string {
		t.Helper()
		r := call(t, s, "tools/call", map[string]any{"name": name, "arguments": args})
		if r["isError"] == true {
			t.Fatalf("tool failed: %+v", r)
		}
		return r["content"].([]any)[0].(map[string]any)["text"].(string)
	}
	text := invoke("ambient_issues", map[string]any{"cluster": "mesh1"})
	if strings.Contains(text, "mesh2") || !strings.Contains(text, "missing-parent") {
		t.Fatal(text)
	}
	text = invoke("ambient_inventory", map[string]any{"cluster": "mesh2"})
	if strings.Contains(text, "mesh1") {
		t.Fatal(text)
	}
	invoke("ambient_investigate", map[string]any{"cluster": "mesh1", "namespace": "app", "kind": "Service", "name": "petstore"})
	invoke("ambient_capabilities", map[string]any{"cluster": "mesh1", "namespace": "app", "service": "petstore"})
	invoke("ambient_plan_preview", map[string]any{"request": map[string]any{"cluster": "mesh1", "namespace": "app", "service": "petstore", "tasks": []string{"route-parent"}}})
	invoke("scorecard_fix_preview", map[string]any{"check": "ambient.default-deny", "target": map[string]any{"cluster": "mesh1", "kind": "Namespace", "name": "app", "resource": "namespaces", "group": "", "namespace": ""}})
	text = invoke("ambient_change_history", nil)
	if strings.Contains(text, "SECRET") || strings.Contains(text, "PRIVATE") {
		t.Fatal("validation credential leaked")
	}
	if posts != 2 {
		t.Fatalf("expected only two preview POSTs, got %d", posts)
	}
	for _, tool := range call(t, s, "tools/list", nil)["tools"].([]any) {
		m := tool.(map[string]any)
		name := m["name"].(string)
		if strings.HasPrefix(name, "ambient_") || name == "scorecard_fix_preview" {
			if m["annotations"].(map[string]any)["readOnlyHint"] != true {
				t.Fatalf("not read-only: %s", name)
			}
		}
	}
	text = call(t, s, "resources/read", map[string]any{"uri": "flightbay://ambient"})["contents"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "missing-parent") {
		t.Fatal(text)
	}
}
