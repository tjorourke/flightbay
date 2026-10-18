package server

import (
	"context"
	"strings"
	"testing"

	"github.com/tjorourke/flightbay/internal/ambient"
	"github.com/tjorourke/flightbay/internal/bundle"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestExplainerRepairRevalidatesSourceIdentityBeforeApply(t *testing.T) {
	s, dc, srv, _ := ambientFixture(t)
	pods := []map[string]any{}
	for _, name := range []string{"caller", "web"} {
		pods = append(pods, map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": name, "namespace": "app", "uid": "uid-" + name, "labels": map[string]any{"app": name}, "annotations": map[string]any{"ambient.istio.io/redirection": "enabled"}}, "spec": map[string]any{"serviceAccountName": name, "containers": []any{map[string]any{"name": "app", "image": "example/app:1"}}}, "status": map[string]any{"phase": "Running"}})
	}
	s.WS.AddObjects("captured", bundle.APIResource{Version: "v1", Resource: "pods", Kind: "Pod", Namespaced: true}, pods, "pods")
	s.WS.AddObjects("captured", bundle.APIResource{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies", Kind: "NetworkPolicy", Namespaced: true}, nil, "netpol")
	s.WS.AddObjects("captured", bundle.APIResource{Group: "security.istio.io", Version: "v1", Resource: "authorizationpolicies", Kind: "AuthorizationPolicy", Namespaced: true}, []map[string]any{{"kind": "AuthorizationPolicy", "metadata": map[string]any{"name": "deny-web", "namespace": "app"}, "spec": map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "web"}}}}}, "deny")
	for _, r := range []struct{ group, resource string }{{"networking.k8s.io", "networkpolicies"}, {"security.istio.io", "authorizationpolicies"}, {"security.istio.io", "peerauthentications"}} {
		s.WS.SetCoverage(bundle.Coverage{ID: r.resource, Cluster: "captured", Type: bundle.CoverageResource, Group: r.group, Version: "v1", Resource: r.resource, State: bundle.StateComplete})
	}
	resource := dc.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace("app")
	// Live source and capture initially match, so the reviewed server dry-run passes.
	if err := dc.Tracker().Create(schema.GroupVersionResource{Version: "v1", Resource: "pods"}, &unstructured.Unstructured{Object: pods[0]}, "app"); err != nil {
		t.Fatal(err)
	}
	req := ambient.Request{Cluster: "captured", Namespace: "app", Service: "web", Tasks: []string{"explain"}, Explain: &ambient.ExplainRequest{From: "app/caller", To: "app/web", Ports: []string{"8080"}}}
	token := validateAmbient(t, srv, req)
	live, err := resource.Get(context.Background(), "caller", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ambient.M(live.Object["spec"])["serviceAccountName"] = "replacement"
	if _, err = resource.Update(context.Background(), live, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	code, out := ambientPost(t, srv, map[string]any{"operation": "apply", "context": "chosen", "validation": token})
	if code == 200 || !strings.Contains(ambient.S(out["error"]), "source identity") {
		t.Fatalf("changed caller was accepted: %d %+v", code, out)
	}
}
