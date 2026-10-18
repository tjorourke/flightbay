package server

import (
	"context"
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

func TestMeshResponseAttributionFindsTheRemoteClusterWithoutAssumingTheSource(t *testing.T) {
	inventories := []meshResponderInventory{
		{Cluster: "mesh1", Pods: []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "reviews-local", Namespace: "bookinfo", UID: "local"}}}},
		{Cluster: "mesh2", Pods: []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "reviews-remote", Namespace: "bookinfo", UID: "remote"}}}},
	}
	for _, tc := range []struct{ name, cluster string }{{"reviews-local", "mesh1"}, {"reviews-remote", "mesh2"}} {
		responder, evidence := meshResponseAcrossClusters(httpProbeResult{Body: `{"podname":"` + tc.name + `"}`}, inventories)
		if responder == nil || responder.Pod.Cluster != tc.cluster || !strings.Contains(evidence, "uniquely matched") {
			t.Fatalf("wrong responder for %s: %+v %s", tc.name, responder, evidence)
		}
	}
	responder, evidence := meshResponseAcrossClusters(httpProbeResult{Body: `{"podname":"not-in-either-cluster"}`}, inventories)
	if responder != nil || evidence == "" {
		t.Fatal("invented a responder cluster")
	}
	// The same name can be deployed in both clusters. List order must not decide.
	inventories[1].Pods[0].Name = "reviews-local"
	responder, evidence = meshResponseAcrossClusters(httpProbeResult{Body: `{"podname":"reviews-local"}`}, inventories)
	if responder != nil || !strings.Contains(evidence, "Ambiguous") {
		t.Fatalf("ambiguous name was attributed: %+v %s", responder, evidence)
	}
}

func TestMeshDistributionDoesNotMergeRespondersAcrossClusters(t *testing.T) {
	rows := []meshProbeResult{}
	for _, cluster := range []string{"mesh1", "mesh2"} {
		rows = append(rows, meshProbeResult{httpProbeResult: httpProbeResult{Name: "reviews.global", Status: 200}, Responder: &meshResponder{UID: "same-uid", Version: "v1", Service: "bookinfo/reviews-v1", Pod: ambient.Ref{Cluster: cluster, Name: "same-pod"}}})
	}
	summary := summarizeMeshProbe(rows, 2)
	if len(summary.Distribution) != 2 || summary.Distribution[0].Percent != 50 || summary.Distribution[1].Percent != 50 {
		t.Fatalf("cluster identities merged: %+v", summary)
	}
}

func TestRemoteResponderInventoryChecksClusterUIDBeforeReadingApplicationObjects(t *testing.T) {
	anchor := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": "kube-system", "uid": "verified-cluster"}}}
	pod := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "reviews-remote", "namespace": "bookinfo", "uid": "remote-pod"}}}
	dc := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{{Version: "v1", Resource: "pods"}: "PodList", {Version: "v1", Resource: "services"}: "ServiceList"}, anchor, pod)
	c := ambientConnection{client: dc}
	if _, err := loadMeshResponderInventory(context.Background(), c, "mesh2", "bookinfo", "wrong-cluster"); err == nil {
		t.Fatal("accepted a repointed remote context")
	}
	if len(dc.Actions()) != 1 || dc.Actions()[0].GetResource().Resource != "namespaces" {
		t.Fatal("read application inventory before verifying cluster identity")
	}
	inventory, err := loadMeshResponderInventory(context.Background(), c, "mesh2", "bookinfo", "verified-cluster")
	if err != nil || inventory.Cluster != "mesh2" || len(inventory.Pods) != 1 || inventory.Pods[0].Name != "reviews-remote" {
		t.Fatalf("verified inventory failed: %+v %v", inventory, err)
	}
}
