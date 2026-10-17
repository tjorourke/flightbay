package ambient

import (
	"fmt"

	"github.com/tjorourke/flightbay/internal/store"
)

type ServiceSummary struct {
	Ref
	State          string `json:"state"`
	Detail         string `json:"detail"`
	Ready          int    `json:"ready"`
	Total          int    `json:"total"`
	Waypoint       string `json:"waypoint,omitempty"`
	WaypointClass  string `json:"waypointClass,omitempty"`
	Routes         int    `json:"routes"`
	Infrastructure bool   `json:"infrastructure"`
	Scope          string `json:"scope,omitempty"`
	Takeover       bool   `json:"takeover"`
}

func summariseService(ws *store.Workspace, svc *store.Object) ServiceSummary {
	r := ServiceSummary{Ref: reference(svc), State: "unknown"}
	r.Scope = svc.Labels()["solo.io/service-scope"]
	r.Takeover = svc.Labels()["solo.io/service-takeover"] == "true"
	r.Infrastructure = svc.Labels()["gateway.networking.k8s.io/gateway-name"] != ""
	for _, ep := range ws.List(svc.Cluster, "discovery.k8s.io", "endpointslices", svc.Namespace, nil) {
		if ep.Labels()["kubernetes.io/service-name"] != svc.Name {
			continue
		}
		for _, v := range L(ep.Obj["endpoints"]) {
			r.Total++
			if Path(M(v), "conditions", "ready") != false {
				r.Ready++
			}
		}
	}
	if r.Ready > 0 {
		r.State = "pass"
		r.Detail = fmt.Sprintf("%d ready / %d endpoints", r.Ready, r.Total)
	} else if S(Path(svc.Obj, "spec", "type")) == "ExternalName" {
		r.State = "na"
		r.Detail = "ExternalName: DNS destination"
	} else if missing(ws, svc.Cluster, "discovery.k8s.io", "endpointslices", "EndpointSlice", svc.Namespace) == "fail" {
		r.State = "fail"
		r.Detail = "No ready endpoints"
	} else {
		r.Detail = "Endpoint data not collected or incomplete"
	}
	wn, wp, gw := Waypoint(ws, svc)
	if wp != "" && !r.Infrastructure {
		r.Waypoint = wn + "/" + wp
	}
	if gw != nil {
		r.WaypointClass = S(Path(gw.Obj, "spec", "gatewayClassName"))
	}
	for _, route := range ws.List(svc.Cluster, GatewayGroup, "httproutes", "", nil) {
		if related(route, svc, wn, wp) {
			r.Routes++
		}
	}
	return r
}
