package ambient

import (
	"reflect"
	"testing"
)

// The workspace stores redacted objects; the server preflight compares them with
// raw live objects. Redaction alone must never look like a dependency change.
func TestL4DependencyObjectIgnoresRedactionOnlyDifferences(t *testing.T) {
	pod := func(arg, env string) map[string]any {
		return map[string]any{
			"metadata": map[string]any{"uid": "u1", "labels": map[string]any{"app": "x"}},
			"spec": map[string]any{
				"serviceAccountName": "sa",
				"containers": []any{map[string]any{
					"args": []any{arg}, "env": []any{map[string]any{"name": "E", "value": env}},
					"ports": []any{map[string]any{"containerPort": 9080}},
				}},
			},
		}
	}
	if a, b := L4DependencyObject("pods", "p", pod("--token=[REDACTED]", `{"a":1}`)), L4DependencyObject("pods", "p", pod("--token=abc", "{\n \"a\": 1\n}")); !reflect.DeepEqual(a, b) {
		t.Fatalf("redaction-only Pod difference reported as drift:\n%v\n%v", a, b)
	}
	changed := pod("x", "y")
	changed["spec"].(map[string]any)["serviceAccountName"] = "other"
	if reflect.DeepEqual(L4DependencyObject("pods", "p", pod("x", "y")), L4DependencyObject("pods", "p", changed)) {
		t.Fatal("a changed service account must still be detected")
	}
	cm := func(name, mesh, other string) map[string]any {
		return map[string]any{"metadata": map[string]any{"uid": "c"}, "data": map[string]any{"mesh": mesh, "config": other}}
	}
	if !reflect.DeepEqual(L4DependencyObject("configmaps", "istio-sidecar-injector", cm("", "", "a")), L4DependencyObject("configmaps", "istio-sidecar-injector", cm("", "", "b"))) {
		t.Fatal("unrelated ConfigMap data must not count as a dependency")
	}
	if reflect.DeepEqual(L4DependencyObject("configmaps", "istio", cm("", "trustDomain: a", "")), L4DependencyObject("configmaps", "istio", cm("", "trustDomain: b", ""))) {
		t.Fatal("a changed mesh config must still be detected")
	}
}
