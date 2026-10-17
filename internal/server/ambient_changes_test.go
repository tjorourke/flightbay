package server

import (
	"context"
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/tjorourke/flightbay/internal/ambient"
	"github.com/tjorourke/flightbay/internal/bundle"
)

func supportAmbientChanges(t *testing.T, s *Server, dc *fake.FakeDynamicClient) {
	t.Helper()
	ctx := context.Background()
	for _, item := range []struct {
		gvr      schema.GroupVersionResource
		ns, name string
	}{{schema.GroupVersionResource{Version: "v1", Resource: "services"}, "app", "web"}, {schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}, "", "app"}} {
		res := dc.Resource(item.gvr)
		var r dynamic.ResourceInterface = res
		if item.ns != "" {
			r = res.Namespace(item.ns)
		}
		o, e := r.Get(ctx, item.name, metav1.GetOptions{})
		if e != nil {
			t.Fatal(e)
		}
		o.SetResourceVersion("1")
		if _, e = r.Update(ctx, o, metav1.UpdateOptions{}); e != nil {
			t.Fatal(e)
		}
	}
	old := s.ambientS.connect
	s.ambientS.connect = func(name string) (ambientConnection, error) {
		c, e := old(name)
		if e != nil {
			return c, e
		}
		m := c.mapper.(*meta.DefaultRESTMapper)
		m.Add(schema.GroupVersionKind{Version: "v1", Kind: "Namespace"}, meta.RESTScopeRoot)
		m.Add(schema.GroupVersionKind{Version: "v1", Kind: "Service"}, meta.RESTScopeNamespace)
		m.AddSpecific(schema.GroupVersionKind{Group: ambient.GatewayGroup, Version: "v1", Kind: "Gateway"}, schema.GroupVersionResource{Group: ambient.GatewayGroup, Version: "v1", Resource: "gateways"}, schema.GroupVersionResource{Group: ambient.GatewayGroup, Version: "v1", Resource: "gateway"}, meta.RESTScopeNamespace)
		return c, nil
	}
	cls := &unstructured.Unstructured{Object: map[string]any{"apiVersion": ambient.GatewayGroup + "/v1", "kind": "GatewayClass", "metadata": map[string]any{"name": "istio-waypoint"}, "spec": map[string]any{"controllerName": "istio.io/mesh-controller"}}}
	s.WS.AddObjects("captured", bundle.APIResource{Group: ambient.GatewayGroup, Version: "v1", Resource: "gatewayclasses", Kind: "GatewayClass"}, []map[string]any{cls.Object}, "classes")
	if e := dc.Tracker().Add(cls); e != nil {
		t.Fatal(e)
	}
	dc.PrependReactor("create", "gateways", func(a ktesting.Action) (bool, runtime.Object, error) {
		act := a.(ktesting.CreateAction)
		o := act.GetObject().(*unstructured.Unstructured).DeepCopy()
		o.SetUID("created-gateway")
		o.SetResourceVersion("1")
		o.SetGeneration(1)
		o.Object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Programmed", "status": "True", "observedGeneration": int64(1)}}}
		if len(a.(interface{ GetCreateOptions() metav1.CreateOptions }).GetCreateOptions().DryRun) == 0 {
			if e := dc.Tracker().Create(a.GetResource(), o, a.GetNamespace()); e != nil {
				return true, nil, e
			}
		}
		return true, o, nil
	})
	dc.PrependReactor("patch", "*", func(a ktesting.Action) (bool, runtime.Object, error) {
		act := a.(ktesting.PatchAction)
		original, e := dc.Tracker().Get(a.GetResource(), a.GetNamespace(), act.GetName())
		if e != nil {
			return true, nil, e
		}
		tmp := fake.NewSimpleDynamicClient(runtime.NewScheme(), original.DeepCopyObject())
		var res dynamic.ResourceInterface = tmp.Resource(a.GetResource())
		if a.GetNamespace() != "" {
			res = tmp.Resource(a.GetResource()).Namespace(a.GetNamespace())
		}
		patched, e := res.Patch(ctx, act.GetName(), act.GetPatchType(), act.GetPatch(), metav1.PatchOptions{})
		if e != nil {
			return true, nil, e
		}
		if len(a.(interface{ GetPatchOptions() metav1.PatchOptions }).GetPatchOptions().DryRun) == 0 {
			patched.SetResourceVersion(fmt.Sprint(ambient.N(original.(*unstructured.Unstructured).GetResourceVersion()) + 1))
			if e = dc.Tracker().Update(a.GetResource(), patched, a.GetNamespace()); e != nil {
				return true, nil, e
			}
		}
		return true, patched, nil
	})
}
func TestWaypointBindingDryRunApplyAndUndoPreservesOtherLabels(t *testing.T) {
	s, dc, srv, _ := ambientFixture(t)
	supportAmbientChanges(t, s, dc)
	req := ambient.Request{Cluster: "captured", Namespace: "app", TargetKind: "Namespace", TargetName: "app", Tasks: []string{"waypoint"}, Waypoint: &ambient.WaypointRequest{Mode: "new", Scope: "namespace", Namespace: "app", Name: "test-wp", Class: "istio-waypoint"}}
	token := validateAmbient(t, srv, req)
	nsres := dc.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"})
	before, _ := nsres.Get(context.Background(), "app", metav1.GetOptions{})
	if before.GetLabels()["istio.io/use-waypoint"] != "" {
		t.Fatal("dry-run changed namespace labels")
	}
	if code, out := ambientPost(t, srv, map[string]any{"operation": "apply", "context": "chosen", "validation": token}); code != 200 {
		t.Fatalf("%d %+v", code, out)
	}
	after, _ := nsres.Get(context.Background(), "app", metav1.GetOptions{})
	if after.GetLabels()["istio.io/use-waypoint"] != "test-wp" {
		t.Fatal("namespace binding not applied")
	}
	lb := after.GetLabels()
	lb["team"] = "ops"
	after.SetLabels(lb)
	after.SetResourceVersion("8")
	_, _ = nsres.Update(context.Background(), after, metav1.UpdateOptions{})
	if code, out := ambientPost(t, srv, map[string]any{"operation": "cleanup", "context": "chosen", "validation": token}); code != 200 {
		t.Fatalf("%d %+v", code, out)
	}
	undone, _ := nsres.Get(context.Background(), "app", metav1.GetOptions{})
	if undone.GetLabels()["team"] != "ops" || undone.GetLabels()["istio.io/use-waypoint"] != "" {
		t.Fatalf("undo lost unrelated labels: %+v", undone.GetLabels())
	}
}

func TestGlobalServicePublicationDryRunApplyUndo(t *testing.T) {
	s, dc, srv, _ := ambientFixture(t)
	supportAmbientChanges(t, s, dc)
	s.WS.AddObjects("captured", bundle.APIResource{Version: "v1", Resource: "pods", Kind: "Pod", Namespaced: true}, []map[string]any{{"metadata": map[string]any{"name": "istiod", "namespace": "istio-system"}, "spec": map[string]any{"containers": []any{map[string]any{"image": "istio/pilot:1.30.4-solo"}}}}}, "pilot")
	s.WS.AddObjects("captured", bundle.APIResource{Group: "networking.istio.io", Version: "v1", Resource: "serviceentries", Kind: "ServiceEntry", Namespaced: true}, nil, "serviceentries")
	req := ambient.Request{Cluster: "captured", Namespace: "app", Service: "web", Tasks: []string{"global-service"}}
	token := validateAmbient(t, srv, req)
	resource := dc.Resource(schema.GroupVersionResource{Version: "v1", Resource: "services"}).Namespace("app")
	before, err := resource.Get(context.Background(), "web", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if before.GetLabels()["solo.io/service-scope"] != "" {
		t.Fatal("dry-run changed the Service")
	}
	if code, out := ambientPost(t, srv, map[string]any{"operation": "apply", "context": "chosen", "validation": token}); code != 200 {
		t.Fatalf("apply: %d %v", code, out)
	}
	current, _ := resource.Get(context.Background(), "web", metav1.GetOptions{})
	if current.GetLabels()["solo.io/service-scope"] != "global" {
		t.Fatal("global publication label missing")
	}
	labels := current.GetLabels()
	labels["unrelated"] = "preserve"
	current.SetLabels(labels)
	if _, err = resource.Update(context.Background(), current, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if code, out := ambientPost(t, srv, map[string]any{"operation": "cleanup", "context": "chosen", "validation": token}); code != 200 {
		t.Fatalf("cleanup: %d %v", code, out)
	}
	after, _ := resource.Get(context.Background(), "web", metav1.GetOptions{})
	if after.GetLabels()["solo.io/service-scope"] != "" || after.GetLabels()["unrelated"] != "preserve" {
		t.Fatalf("undo did not preserve other labels: %v", after.GetLabels())
	}
}
func TestBindingConflictRetainsCreatedWaypoint(t *testing.T) {
	s, dc, srv, _ := ambientFixture(t)
	supportAmbientChanges(t, s, dc)
	req := ambient.Request{Cluster: "captured", Namespace: "app", TargetKind: "Namespace", TargetName: "app", Tasks: []string{"waypoint"}, Waypoint: &ambient.WaypointRequest{Mode: "new", Scope: "namespace", Namespace: "app", Name: "test-wp", Class: "istio-waypoint"}}
	token := validateAmbient(t, srv, req)
	if code, out := ambientPost(t, srv, map[string]any{"operation": "apply", "context": "chosen", "validation": token}); code != 200 {
		t.Fatal(out)
	}
	res := dc.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"})
	n, _ := res.Get(context.Background(), "app", metav1.GetOptions{})
	lb := n.GetLabels()
	lb["istio.io/use-waypoint"] = "user-change"
	n.SetLabels(lb)
	n.SetResourceVersion("9")
	_, _ = res.Update(context.Background(), n, metav1.UpdateOptions{})
	if code, _ := ambientPost(t, srv, map[string]any{"operation": "cleanup", "context": "chosen", "validation": token}); code != 409 {
		t.Fatal("conflicting edit was overwritten")
	}
	if _, e := dc.Resource(schema.GroupVersionResource{Group: ambient.GatewayGroup, Version: "v1", Resource: "gateways"}).Namespace("app").Get(context.Background(), "test-wp", metav1.GetOptions{}); e != nil {
		t.Fatal("Gateway deleted before bindings could be safely restored")
	}
}
