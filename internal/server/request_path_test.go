package server

import (
	"testing"

	"github.com/tjorourke/flightbay/internal/ambient"
	"github.com/tjorourke/flightbay/internal/bundle"
)

func TestRequestPathEndpointDoesNotContactCluster(t *testing.T) {
	s, dc, srv, _ := ambientFixture(t)
	pod := map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "web-1", "namespace": "app", "labels": map[string]any{"app": "web"}}, "spec": map[string]any{"nodeName": "node-a", "serviceAccountName": "default"}}
	s.WS.AddObjects("captured", bundle.APIResource{Version: "v1", Resource: "pods", Kind: "Pod", Namespaced: true}, []map[string]any{pod}, "pod")
	q := ambient.PathRequest{Source: ambient.Ref{Cluster: "captured", Kind: "Pod", Resource: "pods", Namespace: "app", Name: "web-1"}, Destination: ambient.Ref{Cluster: "captured", Kind: "Service", Resource: "services", Namespace: "app", Name: "web"}, Port: 80, Method: "GET", Path: "/health"}
	before := len(dc.Actions())
	code, res := ambientPostPath(t, srv, "/api/ambient/request-path", q)
	if code != 200 {
		t.Fatalf("trace failed: %d %+v", code, res)
	}
	if len(dc.Actions()) != before {
		t.Fatal("read-only path reconstruction contacted the real-cluster client")
	}
	p := ambient.M(res["path"])
	if len(ambient.L(p["hops"])) < 4 || ambient.N(ambient.Path(p, "observed", "records")) != 0 {
		t.Fatal(res)
	}
	q.Source.Cluster = "not-captured"
	code, _ = ambientPostPath(t, srv, "/api/ambient/request-path", q)
	if code != 400 {
		t.Fatal("unknown cluster accepted")
	}
}
