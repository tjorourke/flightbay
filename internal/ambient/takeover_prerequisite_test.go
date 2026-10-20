package ambient

import (
	"github.com/tjorourke/flightbay/internal/store"
	"strings"
	"testing"
)

func takeoverController(ws *store.Workspace, name string, env []any) {
	o := object("Deployment", name, map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "discovery", "image": "us-docker.pkg.dev/soloio-img/istio/pilot:1.30.4-solo-distroless", "env": env}}}}})
	M(o["metadata"])["namespace"] = "istio-system"
	put(ws, "apps", "deployments", "Deployment", o)
}
func TestIstioWaypointTakeoverDetectsControllerPrerequisite(t *testing.T) {
	for _, tc := range []struct{ name, value, want string }{{"disabled", "false", "fail"}, {"enabled", "true", "pass"}, {"unresolved", "bad", "unknown"}, {"default", "", "pass"}} {
		t.Run(tc.name, func(t *testing.T) {
			ws := enterpriseResilienceFixture(t)
			env := []any{}
			if tc.value != "" {
				env = append(env, map[string]any{"name": K8sWorkloadEntrySelection, "value": tc.value})
			}
			env = append(env, map[string]any{"name": "SOLO_LICENSE_KEY", "value": "must-not-be-evidence"})
			takeoverController(ws, "istiod", env)
			svc := Target(ws, "c", "Service", "app", "front")
			e := DetectEnvironment(ws, "c", svc)
			p := e.TakeoverPrerequisite
			if p.State != tc.want {
				t.Fatalf("%+v", p)
			}
			if len(p.Sources) != 1 || p.Sources[0].Kind != "Deployment" {
				t.Fatal("missing controller evidence")
			}
			for _, ev := range p.Evidence {
				if strings.Contains(ev.Value, "must-not-be-evidence") {
					t.Fatal("unrelated environment data leaked")
				}
			}
		})
	}
}
func TestTakeoverGenerationBlocksKnownDisabledFlagButAllowsTurningOff(t *testing.T) {
	ws := enterpriseResilienceFixture(t)
	put(ws, "networking.istio.io", "serviceentries", "ServiceEntry")
	takeoverController(ws, "istiod", []any{map[string]any{"name": K8sWorkloadEntrySelection, "value": "false"}})
	on, off := true, false
	req := Request{Cluster: "c", Namespace: "app", Service: "front", Tasks: []string{"global-service"}, GlobalTakeover: &on}
	if _, err := Generate(ws, req); err == nil || !strings.Contains(err.Error(), K8sWorkloadEntrySelection) {
		t.Fatalf("disabled prerequisite accepted: %v", err)
	}
	req.GlobalTakeover = &off
	if _, err := Generate(ws, req); err != nil {
		t.Fatal("turning takeover off should remain possible", err)
	}
	svc := Target(ws, "c", "Service", "app", "front")
	M(svc.Obj["metadata"])["labels"] = map[string]any{"solo.io/service-scope": "global", "solo.io/service-takeover": "true"}
	view, err := NewView(ws, "c", "Service", "app", "front", "")
	if err != nil {
		t.Fatal(err)
	}
	step := findStep(t, view.Investigation, "takeover-control-plane")
	if step.State != "fail" {
		t.Fatal("missing failing investigation check")
	}
}
func TestTakeoverPrerequisiteDoesNotAssumeUncapturedControllerOrApplyToAgentgateway(t *testing.T) {
	ws := fixture(t)
	p := waypointTakeoverPrerequisite(ws, "c", &ClassChoice{Product: "istio"})
	if p.State != "unknown" {
		t.Fatal(p)
	}
	takeoverController(ws, "istiod", []any{map[string]any{"name": K8sWorkloadEntrySelection, "value": "false"}})
	if p := waypointTakeoverPrerequisite(ws, "c", &ClassChoice{Product: "agentgateway"}); p.State != "na" {
		t.Fatal("applied istiod prerequisite to another controller")
	}
}
