package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/tjorourke/flightbay/internal/ambient"
	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/store"
)

func ambientFixture(t *testing.T) (*Server, *fake.FakeDynamicClient, *httptest.Server, ambient.Request) {
	t.Helper()
	ws := store.New()
	t.Cleanup(func() { ws.Close() })
	ws.SetHeader(&bundle.Header{Contexts: []string{"captured"}})
	ns := map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": "app", "uid": "ns-uid", "labels": map[string]any{"istio.io/dataplane-mode": "ambient"}}}
	svc := map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": "web", "namespace": "app", "uid": "service-uid"}, "spec": map[string]any{"selector": map[string]any{"app": "web"}, "ports": []any{map[string]any{"port": int64(80), "targetPort": int64(8080)}}}}
	for _, v := range []struct {
		r     bundle.APIResource
		items []map[string]any
	}{{bundle.APIResource{Version: "v1", Resource: "namespaces", Kind: "Namespace"}, []map[string]any{ns}}, {bundle.APIResource{Version: "v1", Resource: "services", Kind: "Service", Namespaced: true}, []map[string]any{svc}}, {bundle.APIResource{Group: "security.istio.io", Version: "v1", Resource: "authorizationpolicies", Kind: "AuthorizationPolicy", Namespaced: true}, nil}, {bundle.APIResource{Group: "security.istio.io", Version: "v1", Resource: "peerauthentications", Kind: "PeerAuthentication", Namespaced: true}, nil}} {
		ws.AddObjects("captured", v.r, v.items, "fixture-"+v.r.Resource)
	}
	dc := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		{Group: "security.istio.io", Version: "v1", Resource: "authorizationpolicies"}: "AuthorizationPolicyList",
		{Version: "v1", Resource: "pods"}:                                              "PodList", {Version: "v1", Resource: "configmaps"}: "ConfigMapList",
		{Version: "v1", Resource: "services"}: "ServiceList", {Version: "v1", Resource: "namespaces"}: "NamespaceList",
		{Group: "security.istio.io", Version: "v1", Resource: "peerauthentications"}: "PeerAuthenticationList",
	}, &unstructured.Unstructured{Object: ns}, &unstructured.Unstructured{Object: svc})
	seq := 0
	dc.PrependReactor("create", "*", func(a ktesting.Action) (bool, runtime.Object, error) {
		act := a.(ktesting.CreateAction)
		obj := act.GetObject().(*unstructured.Unstructured).DeepCopy()
		_, err := dc.Tracker().Get(a.GetResource(), a.GetNamespace(), obj.GetName())
		if err == nil {
			return true, nil, apierrors.NewAlreadyExists(a.GetResource().GroupResource(), obj.GetName())
		}
		seq++
		obj.SetUID(types.UID(fmt.Sprintf("test-%d", seq)))
		if len(a.(interface{ GetCreateOptions() metav1.CreateOptions }).GetCreateOptions().DryRun) == 0 {
			if err = dc.Tracker().Create(a.GetResource(), obj, a.GetNamespace()); err != nil {
				return true, nil, err
			}
		}
		return true, obj, nil
	})
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{{Version: "v1"}, {Group: "security.istio.io", Version: "v1"}})
	for _, k := range []string{"AuthorizationPolicy", "PeerAuthentication"} {
		mapper.Add(schema.GroupVersionKind{Group: "security.istio.io", Version: "v1", Kind: k}, meta.RESTScopeNamespace)
	}
	s := &Server{WS: ws, Token: tok, WorkDir: t.TempDir(), AmbientStateDir: t.TempDir()}
	s.ambientS.connect = func(string) (ambientConnection, error) {
		return ambientConnection{dc, mapper, "https://test-cluster"}, nil
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() { srv.Close(); s.Close() })
	return s, dc, srv, ambient.Request{Cluster: "captured", Namespace: "app", Service: "web", Port: 80, Tasks: []string{"l4-default-deny", "strict-mtls"}}
}
func ambientPost(t *testing.T, srv *httptest.Server, body any) (int, map[string]any) {
	return ambientPostPath(t, srv, "/api/ambient/test", body)
}
func ambientPostPath(t *testing.T, srv *httptest.Server, path string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Origin", srv.URL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}
func validateAmbient(t *testing.T, srv *httptest.Server, req ambient.Request) string {
	t.Helper()
	code, p := ambientPostPath(t, srv, "/api/ambient/plan", req)
	if code != 200 {
		t.Fatal(p)
	}
	code, out := ambientPost(t, srv, map[string]any{"operation": "validate", "context": "chosen", "request": req, "planId": p["id"]})
	if code != 200 {
		t.Fatalf("validate %d %+v", code, out)
	}
	return out["token"].(string)
}

func TestAmbientValidateApplyCleanupAndRecovery(t *testing.T) {
	s, dc, srv, req := ambientFixture(t)
	token := validateAmbient(t, srv, req)
	for _, a := range dc.Actions() {
		if a.GetVerb() == "create" && len(a.(interface{ GetCreateOptions() metav1.CreateOptions }).GetCreateOptions().DryRun) == 0 {
			t.Fatal("validation wrote a resource")
		}
	}
	body := map[string]any{"operation": "apply", "context": "chosen", "validation": token}
	code, out := ambientPost(t, srv, body)
	if code != 200 {
		t.Fatalf("apply %d %+v", code, out)
	}
	rec := s.ambientS.records[token]
	if len(rec.Created) != 2 {
		t.Fatalf("created %+v", rec.Created)
	}
	// A new desk process can recover and remove the exact created UIDs.
	s2 := &Server{WS: s.WS, Token: tok + "-new", WorkDir: t.TempDir(), AmbientStateDir: s.AmbientStateDir}
	s2.ambientS.connect = s.ambientS.connect
	if err := s2.ambientRecords(); err != nil {
		t.Fatal(err)
	}
	recovered := s2.ambientS.records[token]
	if len(recovered.Created) != 2 {
		t.Fatal("lost test resources after restart")
	}
	conn, _ := s2.ambientConnect("chosen")
	if err := cleanupAmbient(context.Background(), conn, recovered); err != nil {
		t.Fatal(err)
	}
	if len(recovered.Created) != 0 {
		t.Fatal("cleanup still records resources")
	}
	if err := s2.saveAmbientRecord(recovered); err != nil {
		t.Fatal(err)
	}
	for _, gvr := range []schema.GroupVersionResource{{Group: "security.istio.io", Version: "v1", Resource: "authorizationpolicies"}, {Group: "security.istio.io", Version: "v1", Resource: "peerauthentications"}} {
		for _, o := range rec.Created {
			if _, e := dc.Resource(gvr).Namespace("app").Get(context.Background(), o.Name, metav1.GetOptions{}); !apierrors.IsNotFound(e) {
				t.Fatalf("test object still exists: %s %v", o.Name, e)
			}
		}
	}
}
func TestAmbientWrongContextAndStaleCaptureCannotWrite(t *testing.T) {
	_, dc, srv, req := ambientFixture(t)
	code, _ := ambientPost(t, srv, map[string]any{"operation": "validate", "request": req})
	if code != 400 {
		t.Fatalf("missing explicit context: %d", code)
	}
	svc, _ := dc.Resource(schema.GroupVersionResource{Version: "v1", Resource: "services"}).Namespace("app").Get(context.Background(), "web", metav1.GetOptions{})
	svc.SetUID("another-cluster")
	_, _ = dc.Resource(schema.GroupVersionResource{Version: "v1", Resource: "services"}).Namespace("app").Update(context.Background(), svc, metav1.UpdateOptions{})
	_, p := ambientPostPath(t, srv, "/api/ambient/plan", req)
	code, _ = ambientPost(t, srv, map[string]any{"operation": "validate", "context": "chosen", "request": req, "planId": p["id"]})
	if code != 409 {
		t.Fatalf("different Service UID not refused: %d", code)
	}
	for _, a := range dc.Actions() {
		if a.GetVerb() == "create" {
			t.Fatal("stale capture reached create")
		}
	}
}
func TestAmbientChangedPreviewIsRejected(t *testing.T) {
	s, dc, srv, req := ambientFixture(t)
	_, p := ambientPostPath(t, srv, "/api/ambient/plan", req)
	svc := s.WS.Get(req.Cluster, "", "services", req.Namespace, req.Service)
	ambient.M(ambient.Path(svc.Obj, "spec", "selector"))["app"] = "changed"
	code, out := ambientPost(t, srv, map[string]any{"operation": "validate", "context": "chosen", "request": req, "planId": p["id"]})
	if code != 409 {
		t.Fatalf("stale preview accepted: %d %+v", code, out)
	}
	for _, a := range dc.Actions() {
		if a.GetVerb() == "create" {
			t.Fatal("stale preview reached a write")
		}
	}
}
func TestAmbientPartialApplyRollsBackOnlyItsOwnCreates(t *testing.T) {
	s, dc, srv, req := ambientFixture(t)
	token := validateAmbient(t, srv, req)
	dc.PrependReactor("create", "peerauthentications", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "security.istio.io", Resource: "peerauthentications"}, "test", fmt.Errorf("test denial"))
	})
	code, out := ambientPost(t, srv, map[string]any{"operation": "apply", "context": "chosen", "validation": token})
	if code != 400 {
		t.Fatalf("wanted apply failure %d %+v", code, out)
	}
	if len(s.ambientS.records[token].Created) != 0 {
		t.Fatal("partial apply was not rolled back")
	}
}
func TestAmbientCleanupRefusesReplacementUID(t *testing.T) {
	s, dc, srv, req := ambientFixture(t)
	token := validateAmbient(t, srv, req)
	if code, out := ambientPost(t, srv, map[string]any{"operation": "apply", "context": "chosen", "validation": token}); code != 200 {
		t.Fatal(out)
	}
	created := s.ambientS.records[token].Created[0]
	conn, _ := s.ambientConnect("chosen")
	res, _ := ambientResource(conn, created.APIVersion, created.Kind, created.Namespace)
	obj, _ := res.Get(context.Background(), created.Name, metav1.GetOptions{})
	obj.SetUID("user-replacement")
	_, _ = res.Update(context.Background(), obj, metav1.UpdateOptions{})
	code, _ := ambientPost(t, srv, map[string]any{"operation": "cleanup", "context": "chosen", "validation": token})
	if code != 409 {
		t.Fatal("cleanup accepted a different UID")
	}
	if _, e := res.Get(context.Background(), created.Name, metav1.GetOptions{}); e != nil {
		t.Fatal("replacement object was deleted")
	}
	_ = dc
}
func TestAmbientWriteEndpointsRequireAuthenticationAndSameOrigin(t *testing.T) {
	_, _, srv, _ := ambientFixture(t)
	resp, e := http.Post(srv.URL+"/api/ambient/test", "application/json", bytes.NewBufferString(`{}`))
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal(resp.StatusCode)
	}
	req, _ := http.NewRequest("POST", srv.URL+"/api/ambient/test", bytes.NewBufferString(`{}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Origin", "https://unrelated.example")
	resp, e = http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal(resp.StatusCode)
	}
}
