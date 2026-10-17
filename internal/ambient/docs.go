package ambient

import "strings"

type DocLink struct {
	Title  string `json:"title"`
	URL    string `json:"url"`
	Source string `json:"source"`
}

const istioDocs = "https://istio.io/latest/docs/"
const enterpriseIstioDocs = "https://docs.solo.io/istio/latest/"
const gatewayDocs = "https://gateway-api.sigs.k8s.io/"
const kubeDocs = "https://kubernetes.io/docs/"

func istioGuide(e Environment, ossPath, enterprisePath, title string) []DocLink {
	ent := false
	for _, c := range e.Components {
		if c.Product == "Istio" && c.Edition == "enterprise" {
			ent = true
		}
	}
	if ent && enterprisePath != "" {
		return []DocLink{{title, enterpriseIstioDocs + enterprisePath, "Enterprise Istio"}, {"Upstream reference", istioDocs + ossPath, "Istio OSS"}}
	}
	return []DocLink{{title, istioDocs + ossPath, "Istio OSS"}}
}
func agentGuide(e Environment, path, title string) []DocLink {
	if e.Selected != nil && e.Selected.Edition == "enterprise" {
		return []DocLink{{title, "https://docs.solo.io/agentgateway/kubernetes/latest/" + path, "Enterprise agentgateway"}}
	}
	return []DocLink{{title, "https://agentgateway.dev/docs/kubernetes/latest/" + path, "agentgateway OSS"}}
}
func TaskDocs(e Environment, id string) []DocLink {
	switch id {
	case "l4-default-deny", "l4-allow-namespace":
		return istioGuide(e, "ambient/usage/l4-policy/", "security/authorization/ztunnel-authz/", "L4 authorization")
	case "strict-mtls":
		return istioGuide(e, "ambient/usage/verify-mtls-enabled/", "security/verify-mtls/", "Ambient mutual TLS")
	case "methods":
		return istioGuide(e, "ambient/usage/l7-features/", "security/authorization/waypoint-authz/", "L7 waypoint authorization")
	case "weighted-routing":
		return []DocLink{{"Weighted backend routing", gatewayDocs + "guides/user-guides/traffic-splitting/", "Gateway API"}}
	case "timeouts":
		return []DocLink{{"HTTPRoute timeouts", gatewayDocs + "guides/user-guides/http-timeouts/", "Gateway API"}}
	case "load-balancing":
		return append(istioGuide(e, "reference/config/networking/destination-rule/#LoadBalancerSettings", "traffic-management/load-balancing/", "Endpoint load balancing"), DocLink{"DestinationRule API", istioDocs + "reference/config/networking/destination-rule/", "Istio shared API"})
	case "endpoint-failover":
		return istioGuide(e, "reference/config/networking/destination-rule/#OutlierDetection", "resiliency/outlier-detection/", "Outlier detection")
	case "locality-failover":
		return istioGuide(e, "tasks/traffic-management/locality-load-balancing/failover/", "resiliency/failover/", "Locality failover")
	case "rate-limit":
		return agentGuide(e, "documentation/security/rate-limit-http/", "Local request rate limits")
	case "global-rate-limit":
		return agentGuide(e, "documentation/security/rate-limit-global/", "Global rate limiting")
	case "service-failover":
		return []DocLink{{"HTTPRoute backend semantics", gatewayDocs + "reference/api-types/httproute/", "Gateway API"}}
	case "waypoint":
		if e.Selected != nil && e.Selected.Product == "agentgateway" {
			path := "integrations/istio/"
			if e.Selected.Edition == "enterprise" {
				path = "integrations/agentic-mesh/waypoint/"
			}
			return agentGuide(e, path, "Agentgateway waypoint integration")
		}
		return istioGuide(e, "ambient/usage/waypoint/", "waypoints/configuration/", "Configure and enrol waypoints")
	case "enrol-namespace":
		return istioGuide(e, "ambient/usage/add-workloads/", "setup/add-workloads/", "Ambient enrolment")
	case "endpoints", "service-selector", "service-port":
		return []DocLink{{"Debug Services and endpoints", kubeDocs + "tasks/debug/debug-application/debug-service/", "Kubernetes"}, {"EndpointSlices", kubeDocs + "concepts/services-networking/endpoint-slices/", "Kubernetes"}}
	case "scale-workload":
		return []DocLink{{"Scale a Deployment", kubeDocs + "concepts/workloads/controllers/deployment/#scaling-a-deployment", "Kubernetes"}}
	case "route-parent":
		return append(istioGuide(e, "ambient/usage/l7-features/", "waypoints/configuration/#attach-policies-and-routes", "Route attachment"), DocLink{"HTTPRoute parent references", gatewayDocs + "reference/api-types/httproute/", "Gateway API"})
	default:
		return istioGuide(e, "ambient/usage/troubleshoot-waypoint/", "troubleshooting/service-mesh/ambient/", "Troubleshoot ambient")
	}
}
func StepDocs(e Environment, s Step) []DocLink {
	id := s.ID
	switch {
	case id == "enrolment":
		return TaskDocs(e, "enrol-namespace")
	case strings.HasPrefix(id, "pod-") || id == "workloads" || id == "readiness":
		return []DocLink{{"Debug Pod readiness", kubeDocs + "tasks/debug/debug-application/debug-pods/", "Kubernetes"}}
	case id == "endpoints":
		return TaskDocs(e, "endpoints")
	case strings.HasSuffix(id, "-grant"):
		return []DocLink{{"Cross-namespace ReferenceGrants", gatewayDocs + "reference/api-types/referencegrant/", "Gateway API"}}
	case strings.HasPrefix(id, "backend-"):
		return TaskDocs(e, "service-port")
	case strings.HasPrefix(id, "route-") || id == "routes":
		return append(TaskDocs(e, "route-parent"), DocLink{"Route status and troubleshooting", gatewayDocs + "docs/concepts/troubleshooting/", "Gateway API"})
	case strings.HasPrefix(id, "agent-policy-"):
		return agentGuide(e, "reference/api/", "Policy attachment API")
	case strings.HasPrefix(id, "policy-status-"):
		return append(TaskDocs(e, "methods"), DocLink{"Policy status conditions", gatewayDocs + "docs/concepts/troubleshooting/", "Gateway API"})
	case strings.HasPrefix(id, "policy-"):
		return TaskDocs(e, "methods")
	case strings.HasPrefix(id, "mtls-"):
		return TaskDocs(e, "strict-mtls")
	case id == "dataplane":
		return istioGuide(e, "ambient/usage/troubleshoot-waypoint/", "troubleshooting/service-mesh/ambient/", "Inspect delivered waypoint configuration")
	case id == "external-hosts":
		return []DocLink{{"ServiceEntry API", istioDocs + "reference/config/networking/service-entry/", "Istio shared API"}}
	case strings.HasPrefix(id, "waypoint") || strings.HasPrefix(id, "gateway") || id == "traffic-type":
		return TaskDocs(e, "waypoint")
	default:
		return TaskDocs(e, "")
	}
}
func addViewDocs(v *View) {
	for i := range v.Steps {
		v.Steps[i].Docs = StepDocs(v.Environment, v.Steps[i])
		for _, ev := range v.Steps[i].Evidence {
			if ev.Ref.Group == "enterpriseagentgateway.solo.io" || ev.Ref.Group == "agentgateway.dev" {
				env := v.Environment
				edition := "oss"
				if ev.Ref.Group == "enterpriseagentgateway.solo.io" {
					edition = "enterprise"
				}
				env.Selected = &ClassChoice{Product: "agentgateway", Edition: edition}
				v.Steps[i].Docs = agentGuide(env, "reference/api/", "Policy attachment API")
				break
			}
		}
	}
}
