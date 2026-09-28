// Package solodocs holds the base paths of the official Solo product
// documentation on docs.solo.io, so the Scorecard, Agent reach and the
// analyzer can link each result to the page that explains the feature.
//
// Each product is its own site. Where a product publishes a `latest` alias it
// is used, so links follow new releases without an edit here:
//
//	/istio/latest/        redirects to the current Solo Enterprise for Istio (1.31.x on 2026-09-28)
//	/kgateway/latest/     redirects to the current Solo Enterprise for kgateway (2.3.x)
//	/kagent/latest/       redirects to the current Solo Enterprise for kagent (0.5.x)
//	/agentregistry/latest/ and /agentgateway/kubernetes/latest/ are served as is
//
// If a product drops its alias, pin the newest version here: it is the only
// place that names one. `FLIGHTBAY_CHECK_DOCS_LINKS=1 go test ./internal/posture
// -run DocsLinks` fetches every link and reports the ones that no longer load.
package solodocs

import "strings"

// Host is the Solo documentation site.
const Host = "https://docs.solo.io/"

// Base paths, one per product. Page paths are joined on after the slash.
const (
	// AgentgatewayRoot is Solo Enterprise for agentgateway on Kubernetes.
	AgentgatewayRoot = Host + "agentgateway/kubernetes/latest/"
	// Agentgateway is its main documentation tree (guides, not integrations
	// or reference).
	Agentgateway  = AgentgatewayRoot + "documentation/"
	Kagent        = Host + "kagent/latest/"
	Agentregistry = Host + "agentregistry/latest/"
	Istio         = Host + "istio/latest/"
	Kgateway      = Host + "kgateway/latest/"
)

// Upstream Kubernetes documentation, for Kubernetes-only checks the Solo docs
// do not cover.
const (
	KubernetesSite = "https://kubernetes.io/"
	Kubernetes     = KubernetesSite + "docs/"
)

// AllowedHosts are the hosts a Docs link may point at: the Solo docs, plus the
// upstream Kubernetes and Istio sites for checks that are about plain
// Kubernetes rather than a Solo product.
var AllowedHosts = []string{"docs.solo.io", "kubernetes.io", "istio.io"}

// Page joins a page path onto a product base path.
func Page(base, path string) string {
	return strings.TrimSuffix(base, "/") + "/" + strings.TrimPrefix(path, "/")
}

// IsSolo reports whether a link is on docs.solo.io.
func IsSolo(url string) bool { return strings.HasPrefix(url, Host) }
