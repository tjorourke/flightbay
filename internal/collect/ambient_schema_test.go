package collect

import "testing"

func TestHTTPRouteSchemaIsRetainedForAmbientCapabilities(t *testing.T) {
	schema := map[string]any{"openAPIV3Schema": map[string]any{"type": "object"}}
	in := map[string]any{"metadata": map[string]any{"name": "httproutes.gateway.networking.k8s.io"}, "spec": map[string]any{"versions": []any{map[string]any{"name": "v1", "served": true, "schema": schema}}}}
	out := SlimCRD(in)
	version := asMap(out["spec"])["versions"].([]any)[0].(map[string]any)
	if version["schema"] == nil || asMap(out["x-flightbay"])["schemaCollected"] != true {
		t.Fatal("HTTPRoute channel/schema evidence was discarded")
	}
	asMap(in["metadata"])["name"] = "other.example.com"
	out = SlimCRD(in)
	version = asMap(out["spec"])["versions"].([]any)[0].(map[string]any)
	if version["schema"] != nil || asMap(out["x-flightbay"])["schemaCollected"] != false {
		t.Fatal("unrelated schemas were retained")
	}
}
