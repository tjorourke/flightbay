package ambient

import "testing"

func TestStepArea(t *testing.T) {
	for id, want := range map[string]string{
		"enrolment": "Data plane", "pod-reviews-v1-abc": "Data plane", "takeover-control-plane": "Data plane", "dataplane": "Data plane",
		"waypoint": "Waypoint", "traffic-type": "Waypoint", "waypoint-status-Accepted": "Waypoint", "waypoint-bound-istio.io/WaypointBound": "Waypoint",
		"require-waypoint": "Access", "policy-x": "Access", "policy-status-x-WaypointAccepted": "Access",
		"route-a-0": "Routing", "backend-r-ns-svc": "Routing", "endpoints": "Routing", "dns-resolves": "DNS",
	} {
		if got := StepArea(id); got != want {
			t.Errorf("%s: got %s want %s", id, got, want)
		}
	}
}
