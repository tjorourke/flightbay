package ambient

import (
	"strings"

	"github.com/tjorourke/flightbay/internal/store"
)

type Availability struct {
	Cluster         string `json:"cluster"`
	Available       bool   `json:"available"`
	IstioAPIs       bool   `json:"istioApis"`
	AmbientEvidence bool   `json:"ambientEvidence"`
}

// Istio's CRDs are shared between sidecar and ambient installations. Requiring
// both served Istio APIs and an ambient signal avoids treating Gateway API or
// sidecar-only installations as ambient. Missing capture data stays unconfirmed.
func AvailabilityByCluster(ws *store.Workspace) []Availability {
	out := []Availability{}
	for _, cluster := range ws.Clusters() {
		a := Availability{Cluster: cluster}
		if h := ws.Header(); h != nil && h.Declared() {
			out = append(out, a)
			continue
		}
		a.IstioAPIs = hasResource(ws, cluster, "security.istio.io", "authorizationpolicies") || hasResource(ws, cluster, "security.istio.io", "peerauthentications") || hasResource(ws, cluster, "networking.istio.io", "serviceentries")
		for _, namespace := range ws.List(cluster, "", "namespaces", "", nil) {
			if namespace.Labels()["istio.io/dataplane-mode"] == "ambient" {
				a.AmbientEvidence = true
				break
			}
		}
		if !a.AmbientEvidence {
			for _, pod := range ws.List(cluster, "", "pods", "", nil) {
				for _, container := range L(Path(pod.Obj, "spec", "containers")) {
					image := strings.ToLower(S(M(container)["image"]))
					if strings.Contains(image, "/ztunnel:") || strings.Contains(image, "/ztunnel@") || strings.HasPrefix(image, "ztunnel:") {
						a.AmbientEvidence = true
					}
				}
			}
		}
		a.Available = a.IstioAPIs && a.AmbientEvidence
		out = append(out, a)
	}
	return out
}
