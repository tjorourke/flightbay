package ambient

import (
	"reflect"
	"strings"
	"testing"

	"github.com/tjorourke/flightbay/internal/bundle"
)

func TestKnownIssuesFindsMissingParentBeforeSelectingService(t *testing.T) {
	ws := fixture(t)
	addRoute(ws, map[string]any{"name": "missing-gateway"}, nil)
	inv := List(ws)
	count := 0
	for _, issue := range inv.Issues {
		if issue.Step.ID != "route-app-r-0" {
			continue
		}
		count++
		if issue.Subject.Cluster != "c" || issue.Subject.Kind != "Service" || issue.Subject.Name != "stable" || issue.Step.SuggestedFix == nil || issue.Step.SuggestedFix.ID != "route-parent" {
			t.Fatalf("missing route diagnosis: %+v", issue)
		}
	}
	if count != 1 {
		t.Fatalf("wanted one deduplicated missing-parent issue, got %d", count)
	}
	v, err := NewView(ws, "c", "Service", "app", "stable", "")
	if err != nil {
		t.Fatal(err)
	}
	step := findStep(t, v.Investigation, "route-app-r-0")
	if len(step.Actions) != 1 || step.Actions[0].ID != "route-parent" {
		t.Fatalf("missing repair action: %+v", step)
	}
	ws.SetCoverage(bundle.Coverage{ID: "gateways", Cluster: "c", Type: bundle.CoverageResource, Group: GatewayGroup, Version: "v1", Resource: "gateways", State: bundle.StateExcluded})
	for _, issue := range List(ws).Issues {
		if issue.Step.ID == step.ID {
			t.Fatal("missing capture data incorrectly reported as a failure")
		}
	}
	ws.SetCoverage(bundle.Coverage{ID: "gateways", Cluster: "c", Type: bundle.CoverageResource, Group: GatewayGroup, Version: "v1", Resource: "gateways", State: bundle.StateComplete})
	M(L(Path(Target(ws, "c", "HTTPRoute", "app", "r").Obj, "spec", "parentRefs"))[0])["name"] = "wp"
	for _, issue := range List(ws).Issues {
		if issue.Step.ID == step.ID {
			t.Fatal("repaired parent is still listed as failed")
		}
	}
}

func TestKnownIssuesIncludesOrphanGatewayAndRoute(t *testing.T) {
	ws := fixture(t)
	gw := object("Gateway", "orphan", map[string]any{"gatewayClassName": "istio-waypoint"})
	gw["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Programmed", "status": "False", "observedGeneration": 1}}}
	put(ws, GatewayGroup, "gateways", "Gateway", gw)
	r := object("HTTPRoute", "orphan", map[string]any{"parentRefs": []any{map[string]any{"name": "absent"}}})
	put(ws, GatewayGroup, "httproutes", "HTTPRoute", r)
	found := map[string]bool{}
	for _, issue := range List(ws).Issues {
		found[issue.Subject.Kind+"/"+issue.Subject.Name] = true
	}
	if !found["Gateway/orphan"] || !found["HTTPRoute/orphan"] {
		t.Fatalf("orphan failures hidden: %v", found)
	}
}

func TestSuggestedFixClassification(t *testing.T) {
	tests := []struct{ id, title, detail, want string }{
		{"enrolment", "", "", "enrolment"}, {"pod-a", "", "Pod opts out", "pod-dataplane"}, {"pod-a", "", "Pod not ready", "readiness"},
		{"workloads", "", "", "workloads"}, {"endpoints", "", "", "endpoints"}, {"waypoint", "", "", "waypoint"},
		{"waypoint-listener", "", "", "waypoint-listener"}, {"traffic-type", "", "", "traffic-type"}, {"waypoint-namespace", "", "", "waypoint-namespace"},
		{"waypoint-status-Programmed", "", "", "gateway-status"}, {"route-app-r-0", "", "", "route-parent"}, {"route-app-r-0-listener", "", "", "route-listener"},
		{"route-app-r-0-eligibility", "", "", "route-eligibility"}, {"route-app-r-0-status-0-Accepted", "", "", "route-status"},
		{"backend-r-app-svc", "", "", "backend"}, {"backend-r-app-svc-grant", "Cross-namespace backend permission", "", "backend-grant"},
		{"policy-p", "", "L7 attributes on a selector policy", "policy-l7"}, {"policy-status-p", "", "", "policy-status"},
		{"mtls-p", "", "", "mtls"}, {"dataplane", "", "", "dataplane"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			v := &View{Investigation: &Investigation{Steps: []Step{{ID: tt.id, Title: tt.title, Detail: tt.detail, State: "fail"}}}}
			addSuggestedFixes(v)
			g := v.Steps[0].SuggestedFix
			if g == nil || g.ID != tt.want || g.Fix == "" || g.Verify == "" {
				t.Fatalf("unexpected guide: %+v", g)
			}
			v.Steps[0].State = "unknown"
			addSuggestedFixes(v)
			if !strings.Contains(v.Steps[0].SuggestedFix.Fix, "Missing capture data is not proof") {
				t.Fatal("unknown evidence presented as a confirmed fault")
			}
		})
	}
	if len(CommonFixes()) != 22 {
		t.Fatal("expected 22 distinct problem guides")
	}
}

func TestGatewayParentRepairPreservesOtherParentsAndRules(t *testing.T) {
	ws := fixture(t)
	addRoute(ws, map[string]any{"name": "keep"}, nil)
	r := Target(ws, "c", "HTTPRoute", "app", "r")
	M(r.Obj["spec"])["parentRefs"] = []any{map[string]any{"name": "keep"}, map[string]any{"name": "missing"}}
	before := Copy(r.Obj["spec"])
	req := Request{Cluster: "c", Namespace: "app", Service: "front", Route: "r", Tasks: []string{"route-parent"}, Parent: &Ref{Cluster: "c", Group: GatewayGroup, Resource: "gateways", Kind: "Gateway", Namespace: "app", Name: "wp"}, ParentSection: "mesh", Edit: &EditTarget{Ref: reference(r), Rule: 1}}
	p, err := Generate(ws, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Changes) != 1 || len(p.Changes[0].Fields) != 1 {
		t.Fatalf("unexpected changes: %+v", p.Changes)
	}
	parents := L(p.Changes[0].Fields[0].After)
	if S(M(parents[0])["name"]) != "keep" || S(M(parents[1])["name"]) != "wp" || S(M(parents[1])["sectionName"]) != "mesh" {
		t.Fatalf("wrong parent repair: %+v", parents)
	}
	if !reflect.DeepEqual(before, Copy(r.Obj["spec"])) {
		t.Fatal("plan generation mutated the captured route")
	}
	req.ParentSection = "absent"
	if _, err = Generate(ws, req); err == nil {
		t.Fatal("missing listener accepted")
	}
	req.ParentSection = "mesh"
	req.Parent.Cluster = "other"
	if _, err = Generate(ws, req); err == nil {
		t.Fatal("cross-cluster Gateway accepted")
	}
}
