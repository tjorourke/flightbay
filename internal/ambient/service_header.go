package ambient

import (
	"sort"
	"strconv"

	"github.com/tjorourke/flightbay/internal/store"
)

// HeaderWaypoint is the waypoint serving a Service on one cluster.
type HeaderWaypoint struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Ready     bool   `json:"ready"`
	Reason    string `json:"reason"`
}

// ServiceHeader is what the Service page header draws.
type ServiceHeader struct {
	Ports        []int           `json:"ports"`
	PortNames    []string        `json:"portNames"`
	Versions     []string        `json:"versions"`
	Waypoint     *HeaderWaypoint `json:"waypoint"`
	InAmbient    bool            `json:"inAmbient"`
	SelectorPods int             `json:"selectorPods"`
	ReadyPods    int             `json:"readyPods"`
}

// buildHeader reads the header facts for one captured Service. The waypoint
// comes from Waypoint(), the same lookup servicePolicies and the overview use.
func (b *cfgBuilder) buildHeader() ServiceHeader {
	h := ServiceHeader{Ports: []int{}, PortNames: []string{}, Versions: []string{}, InAmbient: b.inAmbient()}
	for _, pv := range L(Path(b.svc.Obj, "spec", "ports")) {
		p := M(pv)
		h.Ports = append(h.Ports, N(p["port"]))
		name := S(p["name"])
		if name == "" {
			name = strconv.Itoa(N(p["port"]))
		}
		h.PortNames = append(h.PortNames, name)
	}
	sel := M(Path(b.svc.Obj, "spec", "selector"))
	seen := map[string]bool{}
	if len(sel) > 0 {
		for _, p := range b.ws.List(b.svc.Cluster, "", "pods", b.svc.Namespace, nil) {
			if !matches(sel, p.Labels()) {
				continue
			}
			h.SelectorPods++
			if podReady(p) {
				h.ReadyPods++
			}
			lb := p.Labels()
			v := lb["version"]
			if v == "" {
				v = lb["app.kubernetes.io/version"]
			}
			if v != "" && !seen[v] {
				seen[v] = true
				h.Versions = append(h.Versions, v)
			}
		}
	}
	sort.Strings(h.Versions)
	if wpNS, wpName, gw := Waypoint(b.ws, b.svc); wpName != "" {
		h.Waypoint = waypointHeader(wpNS, wpName, gw)
	}
	return h
}

// waypointHeader is ready when the Gateway reports Accepted and Programmed
// true (the conditions the investigation checks). Otherwise reason says why.
func waypointHeader(ns, name string, gw *store.Object) *HeaderWaypoint {
	w := &HeaderWaypoint{Name: name, Namespace: ns}
	if gw == nil {
		w.Reason = "Waypoint Gateway " + ns + "/" + name + " was not captured"
		return w
	}
	got := map[string]map[string]any{}
	for _, c := range L(Path(gw.Obj, "status", "conditions")) {
		got[S(M(c)["type"])] = M(c)
	}
	for _, t := range []string{"Accepted", "Programmed"} {
		c, ok := got[t]
		switch {
		case !ok:
			w.Reason = t + " is not reported"
		case S(c["status"]) != "True":
			w.Reason = t + " is " + S(c["status"])
			if m := defaultString(S(c["message"]), S(c["reason"])); m != "" {
				w.Reason += ": " + m
			}
		default:
			continue
		}
		return w
	}
	w.Ready = true
	return w
}
