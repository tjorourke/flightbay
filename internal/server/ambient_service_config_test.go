package server

import "github.com/tjorourke/flightbay/internal/ambient"

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/store"
)

func TestAmbientServiceConfigEndpoint(t *testing.T) {
	ws := store.New()
	defer ws.Close()
	ws.SetHeader(&bundle.Header{Contexts: []string{"c"}})
	svc := map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": "reviews", "namespace": "bookinfo", "uid": "u1"}, "spec": map[string]any{"selector": map[string]any{"app": "reviews"}, "ports": []any{map[string]any{"port": 9080}}}}
	ws.AddObjects("c", bundle.APIResource{Version: "v1", Resource: "services", Kind: "Service", Namespaced: true}, []map[string]any{svc}, "svc")
	s := &Server{WS: ws}
	for _, tc := range []struct {
		method, url string
		code        int
	}{
		{"POST", "/api/ambient/service-config?cluster=c&namespace=bookinfo&name=reviews", 405},
		{"GET", "/api/ambient/service-config?cluster=c&namespace=bookinfo", 400},
		{"GET", "/api/ambient/service-config?cluster=c&namespace=bookinfo&name=missing", 400},
		{"GET", "/api/ambient/service-config?cluster=c&namespace=bookinfo&name=reviews", 200},
	} {
		w := httptest.NewRecorder()
		s.ambientServiceConfig(w, httptest.NewRequest(tc.method, tc.url, nil))
		if w.Code != tc.code {
			t.Fatalf("%s %s: %d: %s", tc.method, tc.url, w.Code, w.Body)
		}
		if tc.code != 200 {
			continue
		}
		var out struct {
			Subject struct{ Name string }
			Answers map[string]string
			Tasks   []struct{ ID, Status string }
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out.Subject.Name != "reviews" || len(out.Tasks) != len(ambient.TaskTable) || out.Answers["access"] == "" {
			t.Fatalf("%+v", out)
		}
	}
}
