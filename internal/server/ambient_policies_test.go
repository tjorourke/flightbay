package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/store"
)

func TestAmbientPolicyDiffEndpoint(t *testing.T) {
	ws := store.New()
	defer ws.Close()
	ws.SetHeader(&bundle.Header{Contexts: []string{"c1", "c2"}})
	r := bundle.APIResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes", Kind: "HTTPRoute", Namespaced: true}
	route := func(w int) map[string]any {
		return map[string]any{"apiVersion": "gateway.networking.k8s.io/v1", "kind": "HTTPRoute",
			"metadata": map[string]any{"name": "split", "namespace": "app"},
			"spec":     map[string]any{"rules": []any{map[string]any{"backendRefs": []any{map[string]any{"name": "a", "port": 80, "weight": w}}}}}}
	}
	ws.AddObjects("c1", r, []map[string]any{route(90)}, "f1")
	ws.AddObjects("c2", r, []map[string]any{route(80)}, "f2")
	s := &Server{WS: ws}

	get := func(q string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.ambientPolicyDiff(rec, httptest.NewRequest("GET", "/api/ambient/policies/diff?"+q, nil))
		return rec
	}
	rec := get("kind=HTTPRoute&namespace=app&name=split")
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var v struct {
		Ref    map[string]string `json:"ref"`
		Fields []struct {
			Path   string            `json:"path"`
			Values map[string]string `json:"values"`
		} `json:"fields"`
		OnlyOn []string `json:"onlyOn"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Ref["kind"] != "HTTPRoute" || len(v.Fields) != 1 || v.Fields[0].Path != "spec.rules[0].backendRefs[0].weight" || v.Fields[0].Values["c1"] != "90" || v.Fields[0].Values["c2"] != "80" || v.OnlyOn == nil {
		t.Errorf("%s", rec.Body.String())
	}
	if c := get("kind=HTTPRoute&namespace=app&name=nope").Code; c != 404 {
		t.Errorf("missing object: %d", c)
	}
	if c := get("kind=Pod&namespace=app&name=split").Code; c != 400 {
		t.Errorf("unknown kind: %d", c)
	}
	if c := get("namespace=app").Code; c != 400 {
		t.Errorf("no kind: %d", c)
	}
}
