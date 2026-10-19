package server

import (
	"context"
	"fmt"
	"testing"

	"github.com/tjorourke/flightbay/internal/ambient"
	"github.com/tjorourke/flightbay/internal/bundle"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ktesting "k8s.io/client-go/testing"
)

func TestCustomerServiceGlobalPublishStopAndUndo(t *testing.T) {
	s, dc, srv, _ := ambientFixture(t)
	supportAmbientChanges(t, s, dc)
	ctx := context.Background()
	const ns, name = "customer-payments", "foobar"
	services := schema.GroupVersionResource{Version: "v1", Resource: "services"}
	routes := schema.GroupVersionResource{Group: ambient.GatewayGroup, Version: "v1", Resource: "httproutes"}
	conn, _ := s.ambientConnect("chosen")
	conn.mapper.(*meta.DefaultRESTMapper).Add(schema.GroupVersionKind{Group: ambient.GatewayGroup, Version: "v1", Kind: "HTTPRoute"}, meta.RESTScopeNamespace)
	seed := func(gvr schema.GroupVersionResource, kind, namespace, name string, spec, labels map[string]any) {
		t.Helper()
		api := gvr.Version
		if gvr.Group != "" {
			api = gvr.Group + "/" + api
		}
		o := map[string]any{"apiVersion": api, "kind": kind, "metadata": map[string]any{"name": name, "namespace": namespace, "uid": "uid-" + name, "resourceVersion": "1", "labels": labels}}
		if spec != nil {
			o["spec"] = spec
		}
		s.WS.AddObjects("captured", bundle.APIResource{Group: gvr.Group, Version: gvr.Version, Resource: gvr.Resource, Kind: kind, Namespaced: namespace != ""}, []map[string]any{o}, "customer-fixture")
		if err := dc.Tracker().Create(gvr, &unstructured.Unstructured{Object: ambient.M(ambient.Copy(o))}, namespace); err != nil {
			t.Fatal(err)
		}
	}
	seed(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}, "Namespace", "", ns, nil, map[string]any{"istio.io/dataplane-mode": "ambient", "istio.io/use-waypoint": "customer-wp", "team": "payments"})
	seed(services, "Service", ns, name, map[string]any{"selector": map[string]any{"app": name}, "ports": []any{map[string]any{"port": 8080, "targetPort": 9090}}}, map[string]any{"team": "payments"})
	seed(schema.GroupVersionResource{Group: ambient.GatewayGroup, Version: "v1", Resource: "gateways"}, "Gateway", ns, "customer-wp", map[string]any{"gatewayClassName": "istio-waypoint"}, nil)
	seed(schema.GroupVersionResource{Version: "v1", Resource: "pods"}, "Pod", "istio-system", "istiod", map[string]any{"containers": []any{map[string]any{"image": "istio/pilot:1.30.4-solo"}}}, map[string]any{"app": "istiod"})
	s.WS.AddObjects("captured", bundle.APIResource{Group: "networking.istio.io", Version: "v1", Resource: "serviceentries", Kind: "ServiceEntry", Namespaced: true}, nil, "discovery")
	s.WS.AddObjects("captured", bundle.APIResource{Group: "admin.solo.io", Version: "v1", Resource: "segments", Kind: "Segment"}, nil, "segments")
	// The fake API must honour DELETE dry-run and both atomic preconditions.
	dc.PrependReactor("delete", "*", func(a ktesting.Action) (bool, runtime.Object, error) {
		act := a.(ktesting.DeleteAction)
		if a.GetResource().Resource == "namespaces" {
			t.Fatal("customer cleanup attempted namespace deletion")
		}
		obj, err := dc.Tracker().Get(a.GetResource(), a.GetNamespace(), act.GetName())
		if err != nil {
			return true, nil, err
		}
		u := obj.(*unstructured.Unstructured)
		opts := act.GetDeleteOptions()
		if opts.Preconditions == nil || opts.Preconditions.UID == nil || *opts.Preconditions.UID != u.GetUID() || opts.Preconditions.ResourceVersion == nil || *opts.Preconditions.ResourceVersion != u.GetResourceVersion() {
			t.Fatal("delete lacks matching UID/resourceVersion preconditions")
		}
		if len(opts.DryRun) > 0 {
			return true, nil, nil
		}
		return false, nil, nil
	})
	refresh := func(gvr schema.GroupVersionResource, kind, name string) *unstructured.Unstructured {
		t.Helper()
		r := dc.Resource(gvr).Namespace(ns)
		o, err := r.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if o.GetResourceVersion() == "" {
			o.SetResourceVersion("1")
			if _, err := r.Update(ctx, o, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		s.WS.AddObjects("captured", bundle.APIResource{Group: gvr.Group, Version: gvr.Version, Resource: gvr.Resource, Kind: kind, Namespaced: true}, []map[string]any{o.Object}, "refresh")
		return o
	}
	takeover := true
	q := ambient.Request{Cluster: "captured", Namespace: ns, Service: name, Tasks: []string{"global-service"}, GlobalWeights: map[string]int{"captured": 70, "another-customer-cluster": 30}, GlobalTakeover: &takeover}
	pubToken := validateAmbient(t, srv, q)
	if code, out := ambientPost(t, srv, map[string]any{"operation": "apply", "context": "chosen", "validation": pubToken}); code != 200 {
		t.Fatalf("publish: %d %v", code, out)
	}
	alias := ambient.GlobalClusterServiceName(name, q.Cluster)
	main := refresh(services, "Service", name)
	beforeAlias := refresh(services, "Service", alias)
	beforeRoute := refresh(routes, "HTTPRoute", name+"-cluster-split")
	if main.GetLabels()["solo.io/service-scope"] != "global" {
		t.Fatal("customer Service was not published")
	}
	q.Unpublish = true
	stopToken := validateAmbient(t, srv, q)
	if _, err := dc.Resource(routes).Namespace(ns).Get(ctx, beforeRoute.GetName(), metav1.GetOptions{}); err != nil {
		t.Fatal("dry-run deleted the route", err)
	}
	// Changes made after the review must block the whole stop, before either
	// the Service metadata or any split resource is touched.
	changed := beforeRoute.DeepCopy()
	labels := changed.GetLabels()
	labels["customer-change"] = "keep"
	changed.SetLabels(labels)
	changed.SetResourceVersion("2")
	if _, err := dc.Resource(routes).Namespace(ns).Update(ctx, changed, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if code, out := ambientPost(t, srv, map[string]any{"operation": "apply", "context": "chosen", "validation": stopToken}); code != 409 {
		t.Fatalf("changed route accepted: %d %v", code, out)
	}
	main, _ = dc.Resource(services).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if main.GetLabels()["solo.io/service-scope"] != "global" {
		t.Fatal("failed preflight partially unpublished the Service")
	}
	if _, err := dc.Resource(routes).Namespace(ns).Update(ctx, beforeRoute, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if code, out := ambientPost(t, srv, map[string]any{"operation": "apply", "context": "chosen", "validation": stopToken}); code != 200 {
		t.Fatalf("stop: %d %v", code, out)
	}
	for gvr, n := range map[schema.GroupVersionResource]string{services: alias, routes: beforeRoute.GetName()} {
		if _, err := dc.Resource(gvr).Namespace(ns).Get(ctx, n, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Fatalf("split resource remains: %s %v", n, err)
		}
	}
	main, _ = dc.Resource(services).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if main.GetLabels()["solo.io/service-scope"] != "" || main.GetLabels()["solo.io/service-takeover"] != "" || main.GetLabels()["team"] != "payments" {
		t.Fatal("main Service cleanup changed wrong labels", main.GetLabels())
	}
	if _, err := dc.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Get(ctx, ns, metav1.GetOptions{}); err != nil {
		t.Fatal("customer namespace was removed", err)
	}
	refresh(services, "Service", name)
	s.WS.DeleteObject(q.Cluster, routes.Group, routes.Resource, ns, beforeRoute.GetName())
	s.WS.DeleteObject(q.Cluster, services.Group, services.Resource, ns, alias)
	stopped, err := ambient.Generate(s.WS, q)
	if err != nil || len(stopped.Changes) != 0 || len(stopped.Objects) != 0 {
		t.Fatalf("repeated stop is not a no-op: %v %+v", err, stopped)
	}
	q.Unpublish = false
	republish, err := ambient.Generate(s.WS, q)
	if err != nil || len(republish.Objects) != 2 || len(republish.Changes) != 1 {
		t.Fatalf("customer Service cannot be published again: %v %+v", err, republish)
	}
	if code, out := ambientPost(t, srv, map[string]any{"operation": "cleanup", "context": "chosen", "validation": stopToken}); code != 200 {
		t.Fatalf("undo stop: %d %v", code, out)
	}
	for gvr, before := range map[schema.GroupVersionResource]*unstructured.Unstructured{services: beforeAlias, routes: beforeRoute} {
		after, err := dc.Resource(gvr).Namespace(ns).Get(ctx, before.GetName(), metav1.GetOptions{})
		if err != nil || !ambient.Equal(ambient.RestorableObject(before.Object), ambient.RestorableObject(after.Object)) {
			t.Fatalf("undo failed to restore %s: %v", before.GetName(), err)
		}
	}
	// A newly occupied name must never be overwritten by an undo operation.
	deletedRoute := s.ambientS.records[stopToken].Plan.Changes[0]
	if _, err := ambientPatch(ctx, conn, deletedRoute, true, false); !apierrors.IsAlreadyExists(err) {
		t.Fatalf("undo overwrote an occupied route name: %v", err)
	}
}

func TestConfigurationDeletionNeverDeletesNamespace(t *testing.T) {
	s, dc, _, _ := ambientFixture(t)
	supportAmbientChanges(t, s, dc)
	c, _ := s.ambientConnect("chosen")
	ch := ambient.Change{Delete: true, APIVersion: "v1", Ref: ambient.Ref{Kind: "Namespace", Name: "app"}, UID: "ns-uid"}
	for _, undo := range []bool{false, true} {
		for _, dry := range []bool{false, true} {
			t.Run(fmt.Sprintf("undo=%v,dry=%v", undo, dry), func(t *testing.T) {
				if _, err := ambientPatch(context.Background(), c, ch, undo, dry); err == nil {
					t.Fatal("namespace deletion must be refused")
				}
			})
		}
	}
	for _, a := range dc.Actions() {
		if a.GetVerb() == "delete" {
			t.Fatal("namespace deletion reached the API")
		}
	}
}
