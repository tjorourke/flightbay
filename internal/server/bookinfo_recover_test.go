package server

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tjorourke/flightbay/internal/ambient"
	"github.com/tjorourke/flightbay/internal/store"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
)

func recoveredRemovalDesk(t *testing.T) (*Server, *fake.FakeDynamicClient, *httptest.Server) {
	t.Helper()
	c, dc, _ := bookinfoRemovalFixture(t)
	nss := dc.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"})
	ns, _ := nss.Get(context.Background(), "bookinfo", metav1.GetOptions{})
	lb := ns.GetLabels()
	lb["app.kubernetes.io/managed-by"] = "flightbay"
	lb["flightbay.dev/test-id"] = "namespace-plan"
	lb["flightbay.dev/waypoint-name"] = "bookinfo-waypoint"
	lb["flightbay.dev/waypoint-scope"] = "namespace"
	ns.SetLabels(lb)
	if _, err := nss.Update(context.Background(), ns, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	s := &Server{WS: store.New(), Token: tok, WorkDir: t.TempDir(), AmbientStateDir: t.TempDir()}
	s.ambientS.connect = func(string) (ambientConnection, error) { return c, nil }
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() { srv.Close(); s.Close(); s.WS.Close() })
	return s, dc, srv
}

func TestBookinfoRecoveredPreviewCannotDeleteReplacementOrUnownedNamespace(t *testing.T) {
	for _, mode := range []string{"namespace-uid", "service-uid", "cluster-uid", "namespace-ownership", "external-consumer", "missing-confirmation"} {
		t.Run(mode, func(t *testing.T) {
			_, dc, srv := recoveredRemovalDesk(t)
			q := map[string]any{"operation": "preview", "cluster": "mesh1", "context": "chosen", "namespace": "bookinfo"}
			code, out := ambientPostPath(t, srv, "/api/ambient/bookinfo/remove", q)
			if code != 200 || len(out["installations"].([]any)) != 1 {
				t.Fatalf("preview failed: %d %v", code, out)
			}
			q["ids"] = []string{out["installations"].([]any)[0].(map[string]any)["id"].(string)}
			q["operation"], q["confirm"] = "remove", true
			nss := dc.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"})
			switch mode {
			case "namespace-uid", "cluster-uid", "namespace-ownership":
				name := "bookinfo"
				if mode == "cluster-uid" {
					name = "kube-system"
				}
				n, _ := nss.Get(context.Background(), name, metav1.GetOptions{})
				if mode == "namespace-ownership" {
					n.SetLabels(map[string]string{"team": "customer"})
				} else {
					n.SetUID("new-instance")
				}
				_, _ = nss.Update(context.Background(), n, metav1.UpdateOptions{})
			case "service-uid":
				res := dc.Resource(schema.GroupVersionResource{Version: "v1", Resource: "services"}).Namespace("bookinfo")
				o, _ := res.Get(context.Background(), "productpage", metav1.GetOptions{})
				o.SetUID("replacement")
				_, _ = res.Update(context.Background(), o, metav1.UpdateOptions{})
			case "external-consumer":
				_, _ = dc.Resource(schema.GroupVersionResource{Version: "v1", Resource: "services"}).Namespace("customer").Create(context.Background(), &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": "foobar", "namespace": "customer", "labels": map[string]any{"istio.io/use-waypoint": "bookinfo-waypoint", "istio.io/use-waypoint-namespace": "bookinfo"}}}}, metav1.CreateOptions{})
			case "missing-confirmation":
				q["confirm"] = false
			}
			dc.ClearActions()
			code, out = ambientPostPath(t, srv, "/api/ambient/bookinfo/remove", q)
			if code != 409 && code != 400 {
				t.Fatalf("unsafe removal accepted: %d %v", code, out)
			}
			for _, a := range dc.Actions() {
				if a.GetVerb() == "delete" || a.GetVerb() == "patch" {
					t.Fatal("wrote before the changed installation was refused")
				}
			}
		})
	}
}

func TestBookinfoRecoveryDoesNotAdoptNamespaceByName(t *testing.T) {
	for _, missing := range []string{"flightbay.dev/demo", "app.kubernetes.io/managed-by", "flightbay.dev/test-id"} {
		t.Run(missing, func(t *testing.T) {
			s, dc, _ := recoveredRemovalDesk(t)
			res := dc.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"})
			n, _ := res.Get(context.Background(), "bookinfo", metav1.GetOptions{})
			lb := n.GetLabels()
			delete(lb, missing)
			n.SetLabels(lb)
			_, _ = res.Update(context.Background(), n, metav1.UpdateOptions{})
			c, _ := s.ambientConnect("chosen")
			rec, err := recoverBookinfoRemoval(context.Background(), c, "mesh1", "chosen", "bookinfo")
			if err != nil || rec != nil {
				t.Fatalf("customer namespace adopted: %v %+v", err, rec)
			}
		})
	}
}

func TestBookinfoRecoveryIgnoresStaleLocalHistory(t *testing.T) {
	s, _, srv := recoveredRemovalDesk(t)
	stale := &ambientRecord{Token: strings.Repeat("a", 48), Context: "chosen", Server: "https://local", Plan: &ambient.Plan{ID: "old", ServiceUID: "anchor/old-namespace", Request: ambient.Request{Cluster: "mesh1", Namespace: "bookinfo", Demo: &ambient.BookinfoRequest{Stage: "workloads"}}}, Created: []ambientCreated{{APIVersion: "v1", Kind: "Service", Namespace: "bookinfo", Name: "productpage", UID: "old-service"}}}
	if err := s.saveAmbientRecord(stale); err != nil {
		t.Fatal(err)
	}
	code, out := ambientPostPath(t, srv, "/api/ambient/bookinfo/remove", map[string]any{"operation": "preview", "cluster": "mesh1", "context": "chosen", "namespace": "bookinfo"})
	if code != 200 || len(out["installations"].([]any)) != 1 || out["installations"].([]any)[0].(map[string]any)["planId"] != "plan" {
		t.Fatalf("old laptop history hid the current live demo: %d %v", code, out)
	}
}

func TestBookinfoRecoveryIncludesNamespaceAfterWorkloadCleanup(t *testing.T) {
	s, dc, srv := recoveredRemovalDesk(t)
	c, _ := s.ambientConnect("chosen")
	rec, err := recoverBookinfoRemoval(context.Background(), c, "mesh1", "chosen", "bookinfo")
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range rec.Created {
		res, _ := ambientResource(c, o.APIVersion, o.Kind, o.Namespace)
		if err := res.Delete(context.Background(), o.Name, metav1.DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	dc.ClearActions()
	code, out := ambientPostPath(t, srv, "/api/ambient/bookinfo/remove", map[string]any{"operation": "preview", "cluster": "mesh1", "context": "chosen", "namespace": "bookinfo"})
	if code != 200 || len(out["installations"].([]any)) != 1 {
		t.Fatalf("empty demo namespace cannot be removed: %d %v", code, out)
	}
	resources := out["installations"].([]any)[0].(map[string]any)["resources"].([]any)
	if len(resources) != 1 || resources[0].(map[string]any)["kind"] != "Namespace" {
		t.Fatal("namespace missing from cleanup preview", resources)
	}
}
