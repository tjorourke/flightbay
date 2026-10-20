package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tjorourke/flightbay/internal/ambient"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
)

func TestMeshProbeAttributionUsesExactLivePodAndBackendSelectors(t *testing.T) {
	pods := []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "reviews-v1-real", Namespace: "bookinfo", UID: "uid-v1", Labels: map[string]string{"app": "reviews", "version": "v1"}}}}
	services := []corev1.Service{{ObjectMeta: metav1.ObjectMeta{Name: "reviews", Namespace: "bookinfo"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "reviews"}}}, {ObjectMeta: metav1.ObjectMeta{Name: "reviews-v1", Namespace: "bookinfo"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "reviews", "version": "v1"}}}}
	backends := map[string]bool{"bookinfo/reviews-v1": true}
	r := meshResponseIdentity(httpProbeResult{Body: `{"podname":"reviews-v1-real"}`}, pods, services, backends)
	if r == nil || r.UID != "uid-v1" || r.Version != "v1" || r.Service != "bookinfo/reviews-v1" {
		t.Fatalf("incorrect attribution: %+v", r)
	}
	if r := meshResponseIdentity(httpProbeResult{Body: `{"podname":"reviews-v1-invented"}`}, pods, services, backends); r != nil {
		t.Fatal("guessed identity from a Pod-name prefix")
	}
	if r := meshResponseIdentity(httpProbeResult{Body: `{"version":"v1"}`}, pods, services, backends); r != nil {
		t.Fatal("version alone was treated as a known responder")
	}
	backends["bookinfo/reviews"] = true
	if r := meshResponseIdentity(httpProbeResult{ResponseHeaders: map[string]string{"X-Pod-Name": "reviews-v1-real"}}, pods, services, backends); r == nil || r.Service != "" {
		t.Fatalf("ambiguous selectors chosen as an exact service: %+v", r)
	}
}

func TestMeshProbeSummaryKeepsErrorsAndUnknownInDistribution(t *testing.T) {
	rows := []meshProbeResult{}
	for i := 0; i < 100; i++ {
		version := "v1"
		if i >= 90 {
			version = "v2"
		}
		rows = append(rows, meshProbeResult{httpProbeResult: httpProbeResult{Name: "reviews", Status: 200}, Responder: &meshResponder{UID: version, Version: version, Pod: ambient.Ref{Name: version}}})
	}
	rows = append(rows, meshProbeResult{httpProbeResult: httpProbeResult{Name: "reviews", Status: 500}}, meshProbeResult{httpProbeResult: httpProbeResult{Name: "reviews", Error: "timeout"}})
	s := summarizeMeshProbe(rows, 110)
	if s.Completed != 102 || s.Successful != 100 || s.Failed != 2 || s.Unknown != 2 || s.Unattempted != 8 {
		t.Fatalf("incorrect totals: %+v", s)
	}
	percent := 0.0
	for _, g := range s.Distribution {
		percent += g.Percent
	}
	if percent < 99.99 || percent > 100.01 {
		t.Fatalf("errors excluded from denominator: %f", percent)
	}
}

func TestMeshProbeDistributionUsesEachDNSNamesOwnDenominator(t *testing.T) {
	rows := []meshProbeResult{{httpProbeResult: httpProbeResult{Name: "local", Status: 200}}, {httpProbeResult: httpProbeResult{Name: "global", Status: 200}}, {httpProbeResult: httpProbeResult{Name: "global", Status: 200}}}
	s := summarizeMeshProbe(rows, 3)
	for _, g := range s.Distribution {
		if g.Percent != 100 {
			t.Fatalf("DNS name percentage used the whole batch denominator: %+v", g)
		}
	}
}

func TestMeshProbeParsesChunkedResponsesAndRedactsSecrets(t *testing.T) {
	p := httpProfile{Method: "GET", Headers: map[string]string{"Authorization": "Bearer private-token"}}
	body := `{"podname":"reviews-v1-real","echo":"Bearer private-token"}`
	for _, chunked := range []bool{false, true} {
		payload := "HTTP/1.1 200 OK\r\nSet-Cookie: private-cookie\r\n"
		if chunked {
			payload += "Transfer-Encoding: chunked\r\n\r\n" + fmt.Sprintf("%x\r\n%s\r\n0\r\n\r\n", len(body), body)
		} else {
			payload += fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
		}
		var result httpProbeResult
		parseMeshCurl(payload+"\nMARKER 200 0.123\n", "MARKER", p, &result)
		if result.Error != "" || result.Status != 200 || result.ResponseHeaders["Set-Cookie"] != "[redacted]" || strings.Contains(result.Body, "private-token") {
			t.Fatalf("bad parse/redaction: %+v", result)
		}
	}
	var head httpProbeResult
	parseMeshCurl("HTTP/1.1 200 OK\r\nContent-Length: 999\r\n\r\n\nMARKER 200 0.1\n", "MARKER", httpProfile{Method: "HEAD"}, &head)
	if head.Error != "" {
		t.Fatalf("HEAD body incorrectly required: %+v", head)
	}
}

func TestMeshProbeExecIsCurlArgumentsWithoutShellInterpolation(t *testing.T) {
	p := httpProfile{Method: "POST", Timeout: 5, Headers: map[string]string{"X-Test": "$(touch /tmp/must-not-run)"}, Body: "private body"}
	args := meshCurlCommand(p, "http://reviews:9080/reviews/0", "marker", "id")
	if args[0] != "curl" || strings.Contains(strings.Join(args, " "), "private body") {
		t.Fatal("unexpected executable/body handling")
	}
	found := false
	for i, arg := range args {
		if arg == "--header" && i+1 < len(args) && args[i+1] == "X-Test: $(touch /tmp/must-not-run)" {
			found = true
		}
	}
	if !found {
		t.Fatal("header was interpolated instead of passed as a single argument")
	}
	bounded := &meshOutput{Max: 4}
	if _, err := bounded.Write([]byte("12345")); err == nil || !bounded.Overflow || bounded.Len() > 4 {
		t.Fatal("exec output was not bounded")
	}
}

func TestMeshProbeReportsOverlappingRoutesAndFiltersWrongPort(t *testing.T) {
	c, dc, _ := bookinfoRemovalFixture(t)
	gvr := schema.GroupVersionResource{Group: ambient.GatewayGroup, Version: "v1", Resource: "httproutes"}
	for i, weights := range [][]int{{50, 50}, {10, 90}, {99, 1}} {
		port := int64(9080)
		if i == 2 {
			port = 9090
		}
		route := &unstructured.Unstructured{Object: map[string]any{"apiVersion": ambient.GatewayGroup + "/v1", "kind": "HTTPRoute", "metadata": map[string]any{"name": fmt.Sprintf("route-%d", i), "namespace": "bookinfo", "generation": int64(1)}, "spec": map[string]any{"parentRefs": []any{map[string]any{"kind": "Service", "name": "reviews", "port": port}}, "rules": []any{map[string]any{"backendRefs": []any{map[string]any{"name": "reviews-v1", "weight": int64(weights[0])}, map[string]any{"name": "reviews-v2", "weight": int64(weights[1])}}}}}}}
		if _, err := dc.Resource(gvr).Namespace("bookinfo").Create(context.Background(), route, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	rules, backends, warnings := meshRouteEvidence(context.Background(), c, ambient.Ref{Cluster: "mesh1", Namespace: "bookinfo", Name: "reviews"}, 9080)
	if len(rules) != 2 || !backends["bookinfo/reviews-v1"] || !strings.Contains(strings.Join(warnings, " "), "Multiple") {
		t.Fatalf("route conflicts hidden: %+v %+v %v", rules, backends, warnings)
	}
}

func TestMeshBackendReadinessDoesNotInventEndpoints(t *testing.T) {
	dc := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{{Version: "v1", Resource: "services"}: "ServiceList", {Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}: "EndpointSliceList"})
	c := ambientConnection{client: dc}
	ctx := context.Background()
	svcGVR := schema.GroupVersionResource{Version: "v1", Resource: "services"}
	svc := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": "empty-backend", "namespace": "bookinfo"}, "spec": map[string]any{"ports": []any{map[string]any{"name": "http", "port": int64(9080)}}}}}
	if _, err := dc.Resource(svcGVR).Namespace("bookinfo").Create(ctx, svc, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	n, err := meshBackendReadyEndpoints(ctx, c, "bookinfo", "empty-backend", 9080)
	if err != nil || n != 0 {
		t.Fatalf("empty backend: %d %v", n, err)
	}
	slice := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "discovery.k8s.io/v1", "kind": "EndpointSlice", "metadata": map[string]any{"name": "backend-slice", "namespace": "bookinfo", "labels": map[string]any{"kubernetes.io/service-name": "empty-backend"}}, "ports": []any{map[string]any{"name": "http", "port": int64(9080)}}, "endpoints": []any{map[string]any{"addresses": []any{"10.0.0.1"}, "conditions": map[string]any{"ready": false}}, map[string]any{"addresses": []any{"10.0.0.2"}, "conditions": map[string]any{"ready": true}}}}}
	if _, err := dc.Resource(schema.GroupVersionResource{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}).Namespace("bookinfo").Create(ctx, slice, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	n, err = meshBackendReadyEndpoints(ctx, c, "bookinfo", "empty-backend", 9080)
	if err != nil || n != 1 {
		t.Fatalf("ready backend: %d %v", n, err)
	}
	if _, err := meshBackendReadyEndpoints(ctx, c, "bookinfo", "empty-backend", 9999); err == nil {
		t.Fatal("wrong Service port accepted")
	}
}

func TestMeshProbeRejectsUnboundedBatchBeforeConnecting(t *testing.T) {
	for _, count := range []int{0, -1, 1001} {
		s := &Server{}
		payload, _ := json.Marshal(meshProbeRequest{Context: "mesh1", Count: count, Names: []string{"reviews"}})
		w := httptest.NewRecorder()
		s.httpMeshProbe(w, httptest.NewRequest("POST", "http://localhost/api/ambient/http-mesh-test", bytes.NewReader(payload)))
		if w.Code != 400 {
			t.Fatalf("count %d accepted: %d", count, w.Code)
		}
	}
}
