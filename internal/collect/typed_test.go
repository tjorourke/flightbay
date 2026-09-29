package collect

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/policy"
)

// The protobuf path converts typed objects to maps; the JSON path decodes the
// API server's JSON. Both must give the same map, or the desk and every check
// would see different objects depending on the encoding.
func TestTypedMatchesJSON(t *testing.T) {
	now := metav1.NewTime(time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC))
	replicas := int32(3)
	objs := []runtime.Object{
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", Labels: map[string]string{"app": "web"}, CreationTimestamp: now,
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-1", UID: "u1"}}},
			Spec: corev1.PodSpec{NodeName: "n1", Containers: []corev1.Container{{Name: "app", Image: "app:1",
				Ports:          []corev1.ContainerPort{{ContainerPort: 8080}},
				Env:            []corev1.EnvVar{{Name: "A", Value: "b"}, {Name: "P", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "s"}, Key: "k"}}}},
				Resources:      corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("128Mi")}},
				ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromString("http")}}}}}},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, StartTime: &now, ContainerStatuses: []corev1.ContainerStatus{{Name: "app", Ready: true, RestartCount: 4,
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: now}}}}}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop"},
			Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "web"}, Ports: []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromInt32(8080)}}}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop"}, Spec: appsv1.DeploymentSpec{Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}}}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "shop"}, Type: corev1.SecretTypeOpaque, Data: map[string][]byte{"k": []byte("value")}},
		&corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "e", Namespace: "shop"}, InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "web"},
			Reason: "BackOff", Count: 3, FirstTimestamp: now, LastTimestamp: now, Type: "Warning"},
	}
	for _, o := range objs {
		typed, err := runtime.DefaultUnstructuredConverter.ToUnstructured(o)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(o)
		var viaJSON unstructured.Unstructured
		if err := viaJSON.UnmarshalJSON(append(b[:len(b)-1], []byte(`,"apiVersion":"v1","kind":"X"}`)...)); err != nil {
			t.Fatal(err)
		}
		delete(viaJSON.Object, "apiVersion")
		delete(viaJSON.Object, "kind")
		if !reflect.DeepEqual(typed, viaJSON.Object) {
			a, _ := json.Marshal(typed)
			t.Errorf("%T differs:\ntyped %s\njson  %s", o, a, b)
		}
	}
}

func TestProtobufGate(t *testing.T) {
	known := clientGoMinor()
	if known == 0 {
		t.Skip("no build info in this test binary")
	}
	if !protobufSafe("30") || !protobufSafe(itoa(known)) || !protobufSafe(itoa(known)+"+") {
		t.Error("servers up to client-go's version use protobuf")
	}
	if protobufSafe(itoa(known + 1)) {
		t.Error("a newer server falls back to JSON, where unknown fields survive")
	}
	if protobufSafe("") {
		t.Error("an unknown server version falls back to JSON")
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// fakeTyped serves pods through the typed path from the harness's dynamic objects.
type fakeTyped struct {
	h     *harness
	calls int
}

func (f *fakeTyped) Supports(gvr schema.GroupVersionResource, kind string) bool {
	return gvr.Group == "" && gvr.Resource == "pods"
}

func (f *fakeTyped) List(ctx context.Context, gvr schema.GroupVersionResource, kind, ns string, opts metav1.ListOptions) ([]map[string]any, string, error) {
	f.calls++
	l, err := f.h.dyn.Resource(gvr).Namespace(ns).List(ctx, opts)
	if err != nil {
		return nil, "", err
	}
	var out []map[string]any
	for _, it := range l.Items {
		out = append(out, it.Object)
	}
	return out, "", nil
}

func TestCollectorUsesProtobufWhenSafe(t *testing.T) {
	for _, tc := range []struct {
		minor string
		want  bool
	}{{"34", true}, {"99", false}} {
		h := newHarness()
		ft := &fakeTyped{h: h}
		h.typed = ft
		h.disc.minor = tc.minor
		p, _ := policy.Profile("standard")
		files, _, _ := run(t, p, h, nil)
		if (ft.calls > 0) != tc.want {
			t.Errorf("server 1.%s: typed lister used %d times", tc.minor, ft.calls)
		}
		pods := false
		for name := range files {
			pods = pods || strings.Contains(name, "/resources/core/v1/pods/")
		}
		if !pods {
			t.Errorf("server 1.%s: pods collected either way", tc.minor)
		}
		if !strings.Contains(string(files[bundle.CollectionLog]), `"operation":"encoding"`) {
			t.Error("the encoding choice is recorded in the collection log")
		}
	}
}
