package ambient

import (
	"sort"
	"strings"

	"github.com/tjorourke/flightbay/internal/store"
	"sigs.k8s.io/yaml"
)

// Topology is captured configuration, not an assertion of working connectivity.
type Topology struct {
	Clusters []TopologyCluster `json:"clusters"`
	Links    []TopologyLink    `json:"links"`
}
type TopologyLink struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Source   Ref    `json:"source"`
	Target   Ref    `json:"target"`
	Label    string `json:"label"`
	Evidence []Ref  `json:"evidence"`
}
type TopologyService struct {
	ServiceSummary
	GlobalHosts    []string `json:"globalHosts,omitempty"`
	GlobalEvidence []Ref    `json:"globalEvidence,omitempty"`
	identities     []string
}
type TopologyCluster struct {
	Context     string            `json:"context"`
	Identity    string            `json:"identity,omitempty"`
	Network     string            `json:"network,omitempty"`
	TrustDomain string            `json:"trustDomain,omitempty"`
	Services    []TopologyService `json:"services"`
	Gateways    []TopologyGateway `json:"gateways"`
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
	t := Topology{Clusters: []TopologyCluster{}, Links: []TopologyLink{}}
	for _, c := range ws.Clusters() {
		cl := TopologyCluster{Context: c, Services: []TopologyService{}, Gateways: []TopologyGateway{}}
		if ns := ws.Get(c, "", "namespaces", "", "istio-system"); ns != nil {
			cl.Identity = ns.Labels()["topology.istio.io/cluster"]
			cl.Network = ns.Labels()["topology.istio.io/network"]
		}
		if config := ws.Get(c, "", "configmaps", "istio-system", "istio"); config != nil {
			if text := S(Path(config.Obj, "data", "mesh")); text != "" {
				var mesh struct {
					TrustDomain string `json:"trustDomain"`
				}
				if yaml.Unmarshal([]byte(text), &mesh) == nil {
					cl.TrustDomain = defaultString(mesh.TrustDomain, "cluster.local")
				}
			}
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
			s := TopologyService{ServiceSummary: summariseService(ws, svc)}
			if s.Scope == "global" || s.Scope == "global-only" {
				for _, entry := range ws.List(c, "networking.istio.io", "serviceentries", "", nil) {
					labels := entry.Labels()
					if labels["solo.io/parent-service"] != svc.Name || labels["solo.io/parent-service-namespace"] != svc.Namespace || labels["solo.io/service-scope"] == "" {
						continue
					}
					for _, host := range L(Path(entry.Obj, "spec", "hosts")) {
						if name := S(host); name != "" && !contains(s.GlobalHosts, name) {
							s.GlobalHosts = append(s.GlobalHosts, name)
						}
					}
					for _, identity := range L(Path(entry.Obj, "spec", "subjectAltNames")) {
						s.identities = append(s.identities, S(identity))
					}
					s.GlobalEvidence = append(s.GlobalEvidence, reference(entry))
				}
			}
			sort.Strings(s.GlobalHosts)
			cl.Services = append(cl.Services, s)
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
				for _, remote := range t.Clusters {
					if remote.Context != g.PeerContext {
						continue
					}
					for _, gateway := range remote.Gateways {
						if gateway.Role != "east-west" {
							continue
						}
						matched := false
						for _, address := range gateway.Addresses {
							if contains(g.Addresses, address) {
								matched = true
							}
						}
						if matched {
							t.Links = append(t.Links, TopologyLink{ID: "peer/" + g.Cluster + "/" + g.Namespace + "/" + g.Name + "/" + gateway.Cluster + "/" + gateway.Name, Kind: "peering", Source: g.Ref, Target: gateway.Ref, Label: "Configured peering: " + g.Cluster + " → " + gateway.Cluster, Evidence: []Ref{g.Ref, gateway.Ref}})
						}
					}
				}
			}
		}
	}
	for i, source := range t.Clusters {
		for _, target := range t.Clusters[i+1:] {
			forward, reverse := false, false
			for _, link := range t.Links {
				if link.Kind != "peering" {
					continue
				}
				forward = forward || (link.Source.Cluster == source.Context && link.Target.Cluster == target.Context)
				reverse = reverse || (link.Source.Cluster == target.Context && link.Target.Cluster == source.Context)
			}
			if !forward || !reverse {
				continue
			}
			for _, a := range source.Services {
				for _, b := range target.Services {
					if !hasRemoteIdentity(a.identities, target.TrustDomain, b.Namespace) || !hasRemoteIdentity(b.identities, source.TrustDomain, a.Namespace) {
						continue
					}
					for _, host := range a.GlobalHosts {
						if !contains(b.GlobalHosts, host) {
							continue
						}
						evidence := append([]Ref{a.Ref, b.Ref}, a.GlobalEvidence...)
						evidence = append(evidence, b.GlobalEvidence...)
						t.Links = append(t.Links, TopologyLink{ID: "global/" + source.Context + "/" + target.Context + "/" + host, Kind: "global-service", Source: a.Ref, Target: b.Ref, Label: host, Evidence: evidence})
					}
				}
			}
		}
	}
	return t
}

func hasRemoteIdentity(identities []string, cluster, namespace string) bool {
	if cluster == "" {
		return false
	}
	prefix := "spiffe://" + cluster + "/ns/" + namespace + "/sa/"
	for _, identity := range identities {
		if strings.HasPrefix(identity, prefix) {
			return true
		}
	}
	return false
}
