package server

import (
	"testing"

	"github.com/tjorourke/flightbay/internal/ambient"
	"github.com/tjorourke/flightbay/internal/bundle"
)

func TestScorecardFixUsesReviewedValidationAndUndo(t *testing.T) {
	s, _, srv, _ := ambientFixture(t)
	for _, r := range []struct{ group, resource string }{{"", "namespaces"}, {"security.istio.io", "authorizationpolicies"}} {
		s.WS.SetCoverage(bundle.Coverage{ID: r.resource, Cluster: "captured", Type: bundle.CoverageResource, Group: r.group, Version: "v1", Resource: r.resource, State: bundle.StateComplete})
	}
	req := ambient.Request{Cluster: "captured", Namespace: "app", TargetKind: "Namespace", TargetName: "app", Tasks: []string{"scorecard"}, Scorecard: &ambient.ScorecardRequest{Check: "ambient.default-deny", Target: ambient.Ref{Cluster: "captured", Resource: "namespaces", Kind: "Namespace", Name: "app"}}}
	code, p := ambientPostPath(t, srv, "/api/ambient/plan", req)
	if code != 200 {
		t.Fatalf("plan failed: %d %+v", code, p)
	}
	objects := ambient.L(p["objects"])
	if len(objects) != 1 || ambient.S(ambient.Path(ambient.M(objects[0]), "metadata", "namespace")) != "app" {
		t.Fatalf("not scoped to namespace: %+v", p)
	}
	code, _ = ambientPost(t, srv, map[string]any{"operation": "apply", "context": "real", "validation": "not-validated"})
	if code == 200 {
		t.Fatal("apply without validated preview succeeded")
	}
	code, v := ambientPost(t, srv, map[string]any{"operation": "validate", "context": "real", "request": req, "planId": p["id"]})
	if code != 200 {
		t.Fatalf("validation failed: %d %+v", code, v)
	}
	code, out := ambientPost(t, srv, map[string]any{"operation": "apply", "context": "real", "validation": v["token"]})
	if code != 200 {
		t.Fatalf("apply failed: %d %+v", code, out)
	}
	code, out = ambientPost(t, srv, map[string]any{"operation": "cleanup", "context": "real", "validation": v["token"]})
	if code != 200 {
		t.Fatalf("undo failed: %d %+v", code, out)
	}
	req.Scorecard.Target.Cluster = "other"
	code, _ = ambientPostPath(t, srv, "/api/ambient/plan", req)
	if code == 200 {
		t.Fatal("cross-cluster Scorecard target accepted")
	}
	req.Scorecard.Target.Cluster = "captured"
	req.Scorecard.Check = "unreviewed-recipe"
	code, _ = ambientPostPath(t, srv, "/api/ambient/plan", req)
	if code == 200 {
		t.Fatal("unsupported recipe accepted")
	}
}
