package server

import (
	"context"
	"testing"

	"github.com/tjorourke/flightbay/internal/ambient"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
)

func bookinfoRemovalFixture(t *testing.T) (ambientConnection, *fake.FakeDynamicClient, *ambientRecord) {
	t.Helper()
	labels := map[string]any{"flightbay.dev/demo": "bookinfo", "app.kubernetes.io/managed-by": "flightbay", "flightbay.dev/test-id": "plan", "istio.io/waypoint-for": "service"}
	ns := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": "bookinfo", "uid": "ns", "resourceVersion": "1", "labels": map[string]any{"flightbay.dev/demo": "bookinfo", "istio.io/use-waypoint": "bookinfo-waypoint"}}}}
	anchor := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": "kube-system", "uid": "anchor"}}}
	svc := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": "productpage", "namespace": "bookinfo", "uid": "svc", "labels": labels}}}
	gw := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "gateway.networking.k8s.io/v1", "kind": "Gateway", "metadata": map[string]any{"name": "bookinfo-waypoint", "namespace": "bookinfo", "uid": "gw", "labels": labels}, "spec": map[string]any{"gatewayClassName": "istio-waypoint"}}}
	other := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": "leave-me-alone", "namespace": "bookinfo", "uid": "other", "labels": map[string]any{"istio.io/use-waypoint": "none"}}}}
	dc := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{{Version: "v1", Resource: "namespaces"}: "NamespaceList", {Version: "v1", Resource: "services"}: "ServiceList", {Group: ambient.GatewayGroup, Version: "v1", Resource: "httproutes"}: "HTTPRouteList", {Group: "networking.istio.io", Version: "v1", Resource: "serviceentries"}: "ServiceEntryList"}, ns, anchor, svc, other)
	if err := dc.Tracker().Create(schema.GroupVersionResource{Group: ambient.GatewayGroup, Version: "v1", Resource: "gateways"}, gw, "bookinfo"); err != nil {
		t.Fatal(err)
	}
	m := meta.NewDefaultRESTMapper([]schema.GroupVersion{{Version: "v1"}, {Group: ambient.GatewayGroup, Version: "v1"}, {Group: "networking.istio.io", Version: "v1"}})
	m.Add(schema.GroupVersionKind{Version: "v1", Kind: "Namespace"}, meta.RESTScopeRoot)
	m.Add(schema.GroupVersionKind{Version: "v1", Kind: "Service"}, meta.RESTScopeNamespace)
	m.AddSpecific(schema.GroupVersionKind{Group: ambient.GatewayGroup, Version: "v1", Kind: "Gateway"}, schema.GroupVersionResource{Group: ambient.GatewayGroup, Version: "v1", Resource: "gateways"}, schema.GroupVersionResource{Group: ambient.GatewayGroup, Version: "v1", Resource: "gateway"}, meta.RESTScopeNamespace)
	r := &ambientRecord{Plan: &ambient.Plan{ID: "plan", ServiceUID: "anchor/ns", Request: ambient.Request{Cluster: "mesh1", Namespace: "bookinfo", Demo: &ambient.BookinfoRequest{Stage: "workloads", WaypointClass: "istio-waypoint", WaypointScope: "namespace", WaypointName: "bookinfo-waypoint"}}}, Created: []ambientCreated{{APIVersion: "v1", Kind: "Service", Namespace: "bookinfo", Name: "productpage", UID: "svc"}, {APIVersion: ambient.GatewayGroup + "/v1", Kind: "Gateway", Namespace: "bookinfo", Name: "bookinfo-waypoint", UID: "gw"}}}
	return ambientConnection{dc, m, "https://local"}, dc, r
}
func TestBookinfoRemovalPreservesNamespaceAndUnrelatedService(t *testing.T) {
	c, dc, r := bookinfoRemovalFixture(t)
	if e := cleanupAmbient(context.Background(), c, r); e != nil {
		t.Fatal(e)
	}
	if _, e := dc.Resource(schema.GroupVersionResource{Version: "v1", Resource: "services"}).Namespace("bookinfo").Get(context.Background(), "leave-me-alone", metav1.GetOptions{}); e != nil {
		t.Fatal("unrelated service deleted")
	}
	ns, e := dc.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Get(context.Background(), "bookinfo", metav1.GetOptions{})
	if e != nil {
		t.Fatal("namespace deleted")
	}
	if ns.GetLabels()["istio.io/use-waypoint"] != "" {
		t.Fatal("dangling demo namespace binding retained")
	}
	if len(r.Created) != 0 {
		t.Fatal("owned resources not removed")
	}
}
func TestBookinfoRemovalChecksAllOwnershipBeforeAnyWrite(t *testing.T) {
	for _, mode := range []string{"foreign-record", "changed-uid", "foreign-consumer", "changed-namespace", "changed-waypoint-scope", "foreign-route", "foreign-serviceentry"} {
		t.Run(mode, func(t *testing.T) {
			c, dc, r := bookinfoRemovalFixture(t)
			services := dc.Resource(schema.GroupVersionResource{Version: "v1", Resource: "services"}).Namespace("bookinfo")
			switch mode {
			case "foreign-record":
				r.Created = append(r.Created, ambientCreated{APIVersion: "v1", Kind: "Service", Namespace: "bookinfo", Name: "leave-me-alone", UID: "other"})
			case "changed-uid":
				s, _ := services.Get(context.Background(), "productpage", metav1.GetOptions{})
				s.SetUID("replacement")
				_, _ = services.Update(context.Background(), s, metav1.UpdateOptions{})
			case "foreign-consumer":
				// Objects inside the demo namespace go with it; a caller in
				// another namespace bound to the demo waypoint must block.
				_, _ = dc.Resource(schema.GroupVersionResource{Version: "v1", Resource: "services"}).Namespace("other").Create(context.Background(), &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": "outside", "namespace": "other", "labels": map[string]any{"istio.io/use-waypoint": "bookinfo-waypoint", "istio.io/use-waypoint-namespace": "bookinfo"}}}}, metav1.CreateOptions{})
			case "changed-namespace":
				nss := dc.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"})
				n, _ := nss.Get(context.Background(), "bookinfo", metav1.GetOptions{})
				n.SetUID("new-namespace")
				_, _ = nss.Update(context.Background(), n, metav1.UpdateOptions{})
			case "changed-waypoint-scope":
				gateways := dc.Resource(schema.GroupVersionResource{Group: ambient.GatewayGroup, Version: "v1", Resource: "gateways"}).Namespace("bookinfo")
				g, _ := gateways.Get(context.Background(), "bookinfo-waypoint", metav1.GetOptions{})
				lb := g.GetLabels()
				lb["istio.io/waypoint-for"] = "all"
				g.SetLabels(lb)
				_, _ = gateways.Update(context.Background(), g, metav1.UpdateOptions{})
			case "foreign-route":
				c.mapper.(*meta.DefaultRESTMapper).Add(schema.GroupVersionKind{Group: ambient.GatewayGroup, Version: "v1", Kind: "HTTPRoute"}, meta.RESTScopeNamespace)
				_, _ = dc.Resource(schema.GroupVersionResource{Group: ambient.GatewayGroup, Version: "v1", Resource: "httproutes"}).Namespace("other").Create(context.Background(), &unstructured.Unstructured{Object: map[string]any{"apiVersion": ambient.GatewayGroup + "/v1", "kind": "HTTPRoute", "metadata": map[string]any{"name": "external-route", "namespace": "other"}, "spec": map[string]any{"parentRefs": []any{map[string]any{"name": "bookinfo-waypoint", "namespace": "bookinfo"}}}}}, metav1.CreateOptions{})
			case "foreign-serviceentry":
				c.mapper.(*meta.DefaultRESTMapper).Add(schema.GroupVersionKind{Group: "networking.istio.io", Version: "v1", Kind: "ServiceEntry"}, meta.RESTScopeNamespace)
				_, _ = dc.Resource(schema.GroupVersionResource{Group: "networking.istio.io", Version: "v1", Resource: "serviceentries"}).Namespace("other").Create(context.Background(), &unstructured.Unstructured{Object: map[string]any{"apiVersion": "networking.istio.io/v1", "kind": "ServiceEntry", "metadata": map[string]any{"name": "external-entry", "namespace": "other", "labels": map[string]any{"istio.io/use-waypoint": "bookinfo-waypoint", "istio.io/use-waypoint-namespace": "bookinfo"}}}}, metav1.CreateOptions{})
			}
			dc.ClearActions()
			if e := cleanupAmbient(context.Background(), c, r); e == nil {
				t.Fatal("unsafe removal accepted")
			}
			for _, a := range dc.Actions() {
				if a.GetVerb() == "delete" || a.GetVerb() == "patch" {
					t.Fatalf("wrote before safety checks finished: %s", a.GetVerb())
				}
			}
		})
	}
}
