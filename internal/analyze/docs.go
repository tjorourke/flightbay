package analyze

import (
	"strings"

	"github.com/tjorourke/flightbay/internal/solodocs"
)

// ruleDocs is product documentation that helps with each rule's next step. Rules
// about plain Kubernetes (pod status, scheduling, log signatures) and about
// the capture itself have none.
var ruleDocs = map[string]string{
	"cert.unavailable":          solodocs.Istio + "troubleshooting/service-mesh/ambient/",
	"cert.expired":              solodocs.Istio + "troubleshooting/service-mesh/ambient/",
	"cert.root-expiring":        solodocs.Istio + "security/workload-identity/",
	"xds.stale":                 solodocs.Istio + "troubleshooting/service-mesh/istio/",
	"xds.nack":                  solodocs.Istio + "troubleshooting/service-mesh/istio/",
	"mesh.waypoint-missing":     solodocs.Istio + "waypoints/configuration/",
	"mesh.cni-not-ready":        solodocs.Istio + "troubleshooting/service-mesh/ambient/",
	"mesh.ambient-no-ztunnel":   solodocs.Istio + "troubleshooting/service-mesh/ambient/",
	"mesh.version-skew":         solodocs.Istio + "operations/upgrade/",
	"agentgateway.version-skew": solodocs.Agentgateway + "operations/upgrade/",
}

// DocsFor returns the docs page for a finding, or "". A policy that was not
// accepted gets the debugging page of the product that owns its API group.
func DocsFor(f Finding) string {
	if d, ok := ruleDocs[f.Rule]; ok {
		return d
	}
	group := ""
	if f.Object != nil {
		group = f.Object.Group
	}
	switch f.Rule {
	case "policy.not-accepted":
		switch {
		case strings.HasSuffix(group, "agentgateway.dev") || strings.HasSuffix(group, "agentgateway.solo.io"):
			return solodocs.Agentgateway + "operations/debug/"
		case strings.HasSuffix(group, "kgateway.dev") || strings.HasSuffix(group, "kgateway.solo.io"):
			return solodocs.Kgateway + "operations/debug/"
		case strings.HasSuffix(group, "istio.io"):
			return solodocs.Istio + "troubleshooting/service-mesh/istio/"
		}
	}
	return ""
}

// DocsLinks is every docs page a finding can carry, for the link tests.
func DocsLinks() []string {
	out := []string{solodocs.Agentgateway + "operations/debug/", solodocs.Kgateway + "operations/debug/", solodocs.Istio + "troubleshooting/service-mesh/istio/"}
	for _, d := range ruleDocs {
		out = append(out, d)
	}
	return out
}
