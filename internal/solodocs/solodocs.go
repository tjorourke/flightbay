// Package solodocs holds links to upstream product documentation, so the Scorecard, Agent reach and the
// analyzer can link each result to the page that explains the feature.
//
// Each product is its own site. Where a product publishes a `latest` alias it
// is used, so links follow new releases without an edit here:
//
//	/istio/latest/        redirects to the current Istio documentation
//	/kgateway/latest/     redirects to the current kgateway documentation
//	/kagent/latest/       redirects to the current kagent documentation
//	/agentregistry/latest/ and /agentgateway/kubernetes/latest/ are served as is
//
// If a product drops its alias, pin the newest version here: it is the only
// place that names one. `FLIGHTBAY_CHECK_DOCS_LINKS=1 go test ./internal/posture
// -run DocsLinks` fetches every link and reports the ones that no longer load.
package solodocs

import "strings"

// Host is the product documentation host.
const Host = "https://docs.solo.io/"

// Base paths, one per product. Page paths are joined on after the slash.
const (
	// AgentgatewayRoot is the agentgateway documentation for Kubernetes.
	AgentgatewayRoot = Host + "agentgateway/kubernetes/latest/"
	// Agentgateway is its main documentation tree (guides, not integrations
	// or reference).
	Agentgateway  = AgentgatewayRoot + "documentation/"
	Kagent        = Host + "kagent/latest/"
	Agentregistry = Host + "agentregistry/latest/"
	Istio         = Host + "istio/latest/"
	Kgateway      = Host + "kgateway/latest/"
)

// Upstream Kubernetes documentation for checks not covered by product docs.
const (
	KubernetesSite = "https://kubernetes.io/"
	Kubernetes     = KubernetesSite + "docs/"
)

// AllowedHosts are hosts that documentation links may point at: product docs,
// plus upstream Kubernetes and Istio sites for their respective checks.
var AllowedHosts = []string{"docs.solo.io", "kubernetes.io", "istio.io"}

// Page joins a page path onto a product base path.
func Page(base, path string) string {
	return strings.TrimSuffix(base, "/") + "/" + strings.TrimPrefix(path, "/")
}

// IsProductDocs reports whether a link is on the configured product documentation host.
func IsProductDocs(url string) bool { return strings.HasPrefix(url, Host) }
