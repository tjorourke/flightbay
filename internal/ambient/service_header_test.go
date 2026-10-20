package ambient

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/tjorourke/flightbay/internal/store"
)

func labelledPod(name string, labels map[string]any, ready string) map[string]any {
	p := object("Pod", name, map[string]any{})
	M(p["metadata"])["labels"] = labels
	p["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": ready}}}
	return p
}

func TestServiceConfigHeader(t *testing.T) {
	gateway := func(conds ...any) map[string]any {
		g := object("Gateway", "wp", map[string]any{"gatewayClassName": "istio-waypoint"})
		g["status"] = map[string]any{"conditions": conds}
		return g
	}
	cond := func(t, s, msg string) map[string]any {
		return map[string]any{"type": t, "status": s, "message": msg}
	}
	cases := []struct {
		name  string
		setup func(ws *store.Workspace)
		want  ServiceHeader
	}{
		{
			name:  "fixture: ready waypoint, one ready pod, no version",
			setup: func(ws *store.Workspace) {},
			want: ServiceHeader{Ports: []int{80}, PortNames: []string{"80"}, Versions: []string{}, InAmbient: true, SelectorPods: 1, ReadyPods: 1,
				Waypoint: &HeaderWaypoint{Name: "wp", Namespace: "app", Ready: true}},
		},
		{
			name: "versions sorted, distinct, fallback label, not-ready pod (plus the fixture pod)",
			setup: func(ws *store.Workspace) {
				put(ws, "", "pods", "Pod",
					labelledPod("a", map[string]any{"app": "front", "version": "v2"}, "True"),
					labelledPod("b", map[string]any{"app": "front", "version": "v1"}, "False"),
					labelledPod("c", map[string]any{"app": "front", "version": "v2"}, "True"),
					labelledPod("d", map[string]any{"app": "front", "app.kubernetes.io/version": "v3"}, "True"),
					labelledPod("other", map[string]any{"app": "stable", "version": "v9"}, "True"))
			},
			want: ServiceHeader{Ports: []int{80}, PortNames: []string{"80"}, Versions: []string{"v1", "v2", "v3"}, InAmbient: true, SelectorPods: 5, ReadyPods: 4,
				Waypoint: &HeaderWaypoint{Name: "wp", Namespace: "app", Ready: true}},
		},
		{
			name: "waypoint not programmed gives a reason",
			setup: func(ws *store.Workspace) {
				put(ws, GatewayGroup, "gateways", "Gateway", gateway(cond("Accepted", "True", ""), cond("Programmed", "False", "no pods")))
			},
			want: ServiceHeader{Ports: []int{80}, PortNames: []string{"80"}, Versions: []string{}, InAmbient: true, SelectorPods: 1, ReadyPods: 1,
				Waypoint: &HeaderWaypoint{Name: "wp", Namespace: "app", Reason: "Programmed is False: no pods"}},
		},
		{
			name: "waypoint named but not captured",
			setup: func(ws *store.Workspace) {
				n := object("Namespace", "app", nil)
				M(n["metadata"])["namespace"] = ""
				M(n["metadata"])["labels"] = map[string]any{"istio.io/dataplane-mode": "ambient", "istio.io/use-waypoint": "ghost"}
				put(ws, "", "namespaces", "Namespace", n)
			},
			want: ServiceHeader{Ports: []int{80}, PortNames: []string{"80"}, Versions: []string{}, InAmbient: true, SelectorPods: 1, ReadyPods: 1,
				Waypoint: &HeaderWaypoint{Name: "ghost", Namespace: "app", Reason: "Waypoint Gateway app/ghost was not captured"}},
		},
		{
			name: "no waypoint and not in ambient",
			setup: func(ws *store.Workspace) {
				n := object("Namespace", "app", nil)
				M(n["metadata"])["namespace"] = ""
				put(ws, "", "namespaces", "Namespace", n)
				put(ws, "", "pods", "Pod", labelledPod("front-1", map[string]any{"app": "front"}, "True"))
			},
			want: ServiceHeader{Ports: []int{80}, PortNames: []string{"80"}, Versions: []string{}, InAmbient: false, SelectorPods: 1, ReadyPods: 1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws := fixture(t)
			tc.setup(ws)
			c, err := NewServiceConfig(ws, "c", "app", "front")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(c.Header, tc.want) {
				g, _ := json.Marshal(c.Header)
				w, _ := json.Marshal(tc.want)
				t.Errorf("header\n got %s\nwant %s", g, w)
			}
		})
	}
}
