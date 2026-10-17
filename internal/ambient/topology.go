package ambient

import (
	"sort"

	"github.com/tjorourke/flightbay/internal/store"
)

// Topology is captured configuration, not an assertion of working connectivity.
type Topology struct {
	Clusters []TopologyCluster `json:"clusters"`
}
type TopologyCluster struct {
	Context  string            `json:"context"`
	Identity string            `json:"identity,omitempty"`
	Network  string            `json:"network,omitempty"`
	Services []ServiceSummary  `json:"services"`
	Gateways []TopologyGateway `json:"gateways"`
}
type TopologyGateway struct {
	Ref
	Class       string   `json:"class"`
	Role        string   `json:"role"`
	State       string   `json:"state"`
	Addresses   []string `json:"addresses"`
	Ports       []int    `json:"ports"`
	Peer        string   `json:"peer,omitempty"`
	PeerContext string   `json:"peerContext,omitempty"`
}

func capturedTopology(ws *store.Workspace) Topology {
	t := Topology{Clusters: []TopologyCluster{}}
	for _, c := range ws.Clusters() {
		cl := TopologyCluster{Context: c, Services: []ServiceSummary{}, Gateways: []TopologyGateway{}}
		if ns := ws.Get(c, "", "namespaces", "", "istio-system"); ns != nil {
			cl.Identity = ns.Labels()["topology.istio.io/cluster"]
			cl.Network = ns.Labels()["topology.istio.io/network"]
		}
		// Read actual mesh identity, never derive it by trimming a context name.
		for _, p := range ws.List(c, "", "pods", "istio-system", nil) {
			if p.Labels()["app"] != "istiod" {
				continue
			}
			for _, container := range L(Path(p.Obj, "spec", "containers")) {
				for _, env := range L(M(container)["env"]) {
					e := M(env)
					if S(e["name"]) == "CLUSTER_ID" && cl.Identity == "" {
						cl.Identity = S(e["value"])
					}
				}
			}
		}
		for _, svc := range ws.List(c, "", "services", "", nil) {
			cl.Services = append(cl.Services, summariseService(ws, svc))
		}
		for _, gw := range ws.List(c, GatewayGroup, "gateways", "", nil) {
			class := S(Path(gw.Obj, "spec", "gatewayClassName"))
			if class != "istio-eastwest" && class != "istio-remote" {
				continue
			}
			g := TopologyGateway{Ref: reference(gw), Class: class, Role: "east-west", State: "unknown", Addresses: []string{}, Ports: []int{}}
			field := "status"
			if class == "istio-remote" {
				g.Role, g.Peer, field = "remote-peer", gw.Labels()["topology.istio.io/cluster"], "spec"
			}
			for _, a := range L(Path(gw.Obj, field, "addresses")) {
				if value := S(M(a)["value"]); value != "" {
					g.Addresses = append(g.Addresses, value)
				}
			}
			for _, listener := range L(Path(gw.Obj, "spec", "listeners")) {
				g.Ports = append(g.Ports, N(M(listener)["port"]))
			}
			generation := N(Path(gw.Obj, "metadata", "generation"))
			for _, condition := range L(Path(gw.Obj, "status", "conditions")) {
				v := M(condition)
				if S(v["type"]) != "Programmed" || (generation > 0 && N(v["observedGeneration"]) != generation) {
					continue
				}
				if S(v["status"]) == "True" {
					g.State = "pass"
				} else if S(v["status"]) == "False" {
					g.State = "fail"
				}
			}
			cl.Gateways = append(cl.Gateways, g)
		}
		sort.Slice(cl.Services, func(i, j int) bool {
			return cl.Services[i].Namespace+"/"+cl.Services[i].Name < cl.Services[j].Namespace+"/"+cl.Services[j].Name
		})
		sort.Slice(cl.Gateways, func(i, j int) bool {
			return cl.Gateways[i].Namespace+"/"+cl.Gateways[i].Name < cl.Gateways[j].Namespace+"/"+cl.Gateways[j].Name
		})
		t.Clusters = append(t.Clusters, cl)
	}
	sort.Slice(t.Clusters, func(i, j int) bool { return t.Clusters[i].Context < t.Clusters[j].Context })
	for i := range t.Clusters {
		for j := range t.Clusters[i].Gateways {
			g := &t.Clusters[i].Gateways[j]
			if g.Role != "remote-peer" || g.Peer == "" {
				continue
			}
			matches := []string{}
			for _, remote := range t.Clusters {
				if remote.Context != t.Clusters[i].Context && remote.Identity == g.Peer {
					matches = append(matches, remote.Context)
				}
			}
			if len(matches) == 1 {
				g.PeerContext = matches[0]
			}
		}
	}
	return t
}
