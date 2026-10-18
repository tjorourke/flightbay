package server

import (
	"context"
	"testing"

	"github.com/tjorourke/flightbay/internal/ambient"
	"github.com/tjorourke/flightbay/internal/bundle"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestBookinfoNamespaceDryRunApplyAndNamespaceRetention(t *testing.T) {
	s, dc, srv, _ := ambientFixture(t)
	anchor := map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": "kube-system", "uid": "cluster-anchor"}}
	gc := map[string]any{"apiVersion": "gateway.networking.k8s.io/v1", "kind": "GatewayClass", "metadata": map[string]any{"name": "istio-waypoint", "uid": "class-uid"}, "spec": map[string]any{"controllerName": "istio.io/mesh-controller"}}
	for _, v := range []struct {
		r bundle.APIResource
		o map[string]any
	}{{bundle.APIResource{Version: "v1", Resource: "namespaces", Kind: "Namespace"}, anchor}, {bundle.APIResource{Group: ambient.GatewayGroup, Version: "v1", Resource: "gatewayclasses", Kind: "GatewayClass"}, gc}} {
		s.WS.AddObjects("captured", v.r, []map[string]any{v.o}, "demo")
		if e := dc.Tracker().Create(schema.GroupVersionResource{Group: v.r.Group, Version: v.r.Version, Resource: v.r.Resource}, &unstructured.Unstructured{Object: v.o}, ""); e != nil {
			t.Fatal(e)
		}
	}
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{{Version: "v1"}, {Group: ambient.GatewayGroup, Version: "v1"}})
	mapper.Add(schema.GroupVersionKind{Version: "v1", Kind: "Namespace"}, meta.RESTScopeRoot)
	mapper.Add(schema.GroupVersionKind{Group: ambient.GatewayGroup, Version: "v1", Kind: "GatewayClass"}, meta.RESTScopeRoot)
	mapper.Add(schema.GroupVersionKind{Version: "v1", Kind: "Node"}, meta.RESTScopeRoot)
	s.ambientS.connect = func(string) (ambientConnection, error) {
		return ambientConnection{dc, mapper, "https://test-cluster"}, nil
	}
	q := ambient.Request{Cluster: "captured", Namespace: "flightbay-bookinfo-test", Tasks: []string{"bookinfo-demo"}, Demo: &ambient.BookinfoRequest{Stage: "namespace", WaypointClass: "istio-waypoint"}}
	code, p := ambientPostPath(t, srv, "/api/ambient/plan", q)
	if code != 200 {
		t.Fatalf("plan %d %+v", code, p)
	}
	code, v := ambientPost(t, srv, map[string]any{"operation": "validate", "context": "real", "request": q, "planId": p["id"]})
	if code != 200 {
		t.Fatalf("validate %d %+v", code, v)
	}
	ns := dc.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"})
	if _, e := ns.Get(context.Background(), q.Namespace, metav1.GetOptions{}); e == nil {
		t.Fatal("namespace was created during dry-run")
	}
	code, out := ambientPost(t, srv, map[string]any{"operation": "apply", "context": "real", "validation": v["token"]})
	if code != 200 {
		t.Fatalf("apply %d %+v", code, out)
	}
	actual, e := ns.Get(context.Background(), q.Namespace, metav1.GetOptions{})
	if e != nil || actual.GetLabels()["flightbay.dev/demo"] != "bookinfo" {
		t.Fatal("namespace not correctly owned")
	}
	code, _ = ambientPost(t, srv, map[string]any{"operation": "cleanup", "context": "real", "validation": v["token"]})
	if code == 200 {
		t.Fatal("namespace cleanup must not delete possible unrelated contents")
	}
	if _, e = ns.Get(context.Background(), q.Namespace, metav1.GetOptions{}); e != nil {
		t.Fatal("namespace was deleted")
	}
	if e = ns.Delete(context.Background(), q.Namespace, metav1.DeleteOptions{}); e != nil {
		t.Fatal(e)
	}
	code, out = ambientPost(t, srv, map[string]any{"operation": "cleanup", "context": "real", "validation": v["token"]})
	if code != 200 {
		t.Fatalf("record for manually removed namespace could not be cleared: %d %+v", code, out)
	}
	if _, e := ambientResource(ambientConnection{dc, mapper, ""}, "v1", "Node", ""); e == nil {
		t.Fatal("exception accidentally allowed arbitrary cluster-scoped resources")
	}
}
