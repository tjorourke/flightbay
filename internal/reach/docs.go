package reach

import "github.com/tjorourke/flightbay/internal/solodocs"

// The product documentation each finding links to, next to the field guide.
// Documentation base paths and product versions live in internal/solodocs.
const (
	docsEgressViaGateway = solodocs.AgentgatewayRoot + "integrations/agentic-mesh/egress/"
	docsEgressOverview   = solodocs.Istio + "traffic-management/egress/overview/"
	docsWaypointAuthz    = solodocs.Istio + "security/authorization/waypoint-authz/"
	docsNetpolAmbient    = solodocs.Istio + "security/authorization/configure-networkpolicies/"
	docsWorkloadIdentity = solodocs.Istio + "security/workload-identity/"
	docsEnroll           = solodocs.Istio + "setup/add-workloads/"
	docsTokenExchange    = solodocs.Agentgateway + "security/backend-authn/token-exchange/"
	docsBackendAuthn     = solodocs.Agentgateway + "security/backend-authn/"
	docsJWT              = solodocs.Agentgateway + "security/jwt/setup/"

	// Plain Kubernetes: no product-specific page covers these.
	docsPodSecurity     = solodocs.Kubernetes + "concepts/security/pod-security-standards/"
	docsVolumes         = solodocs.Kubernetes + "concepts/storage/persistent-volumes/"
	docsNetworkPolicy   = solodocs.Kubernetes + "concepts/services-networking/network-policies/"
	docsRBAC            = solodocs.Kubernetes + "concepts/security/rbac-good-practices/"
	docsSAToken         = solodocs.Kubernetes + "tasks/configure-pod-container/configure-service-account/"
	docsServiceAccounts = solodocs.Kubernetes + "concepts/security/service-accounts/"
	docsRuntimeClass    = solodocs.Kubernetes + "concepts/containers/runtime-class/"
)

// DocsLinks is every documentation link a finding can carry, for the link
// tests.
func DocsLinks() []string {
	return []string{docsEgressViaGateway, docsEgressOverview, docsWaypointAuthz, docsNetpolAmbient, docsWorkloadIdentity,
		docsEnroll, docsTokenExchange, docsBackendAuthn, docsJWT, docsPodSecurity, docsVolumes, docsNetworkPolicy,
		docsRBAC, docsSAToken, docsServiceAccounts, docsRuntimeClass}
}

// Guide is the field guide every finding links to.
const Guide = guide
