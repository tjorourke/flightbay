package scan

import (
	"strings"

	"github.com/tjorourke/flightbay/internal/bundle"
)

// builtin is the discovery information for the Kubernetes kinds and the
// custom resources Flightbay's checks read, so a repo scan can write the same
// apiresources.json a capture writes without a cluster to ask.
type kindInfo struct {
	resource   string
	namespaced bool
	short      []string
}

var builtin = map[string]kindInfo{
	// core
	"/Pod":                   {"pods", true, []string{"po"}},
	"/Service":               {"services", true, []string{"svc"}},
	"/ServiceAccount":        {"serviceaccounts", true, []string{"sa"}},
	"/ConfigMap":             {"configmaps", true, []string{"cm"}},
	"/Secret":                {"secrets", true, nil},
	"/Namespace":             {"namespaces", false, []string{"ns"}},
	"/Node":                  {"nodes", false, []string{"no"}},
	"/PersistentVolume":      {"persistentvolumes", false, []string{"pv"}},
	"/PersistentVolumeClaim": {"persistentvolumeclaims", true, []string{"pvc"}},
	"/Endpoints":             {"endpoints", true, []string{"ep"}},
	"/LimitRange":            {"limitranges", true, []string{"limits"}},
	"/ResourceQuota":         {"resourcequotas", true, []string{"quota"}},
	"/ReplicationController": {"replicationcontrollers", true, []string{"rc"}},
	"/PodTemplate":           {"podtemplates", true, nil},
	"/Event":                 {"events", true, []string{"ev"}},
	// apps, batch, autoscaling, policy
	"apps/Deployment":                     {"deployments", true, []string{"deploy"}},
	"apps/StatefulSet":                    {"statefulsets", true, []string{"sts"}},
	"apps/DaemonSet":                      {"daemonsets", true, []string{"ds"}},
	"apps/ReplicaSet":                     {"replicasets", true, []string{"rs"}},
	"apps/ControllerRevision":             {"controllerrevisions", true, nil},
	"batch/Job":                           {"jobs", true, nil},
	"batch/CronJob":                       {"cronjobs", true, []string{"cj"}},
	"autoscaling/HorizontalPodAutoscaler": {"horizontalpodautoscalers", true, []string{"hpa"}},
	"policy/PodDisruptionBudget":          {"poddisruptionbudgets", true, []string{"pdb"}},
	// networking, rbac, storage, admission
	"networking.k8s.io/NetworkPolicy":                               {"networkpolicies", true, []string{"netpol"}},
	"networking.k8s.io/Ingress":                                     {"ingresses", true, []string{"ing"}},
	"networking.k8s.io/IngressClass":                                {"ingressclasses", false, nil},
	"rbac.authorization.k8s.io/Role":                                {"roles", true, nil},
	"rbac.authorization.k8s.io/RoleBinding":                         {"rolebindings", true, nil},
	"rbac.authorization.k8s.io/ClusterRole":                         {"clusterroles", false, nil},
	"rbac.authorization.k8s.io/ClusterRoleBinding":                  {"clusterrolebindings", false, nil},
	"storage.k8s.io/StorageClass":                                   {"storageclasses", false, []string{"sc"}},
	"scheduling.k8s.io/PriorityClass":                               {"priorityclasses", false, []string{"pc"}},
	"coordination.k8s.io/Lease":                                     {"leases", true, nil},
	"discovery.k8s.io/EndpointSlice":                                {"endpointslices", true, nil},
	"admissionregistration.k8s.io/ValidatingWebhookConfiguration":   {"validatingwebhookconfigurations", false, nil},
	"admissionregistration.k8s.io/MutatingWebhookConfiguration":     {"mutatingwebhookconfigurations", false, nil},
	"admissionregistration.k8s.io/ValidatingAdmissionPolicy":        {"validatingadmissionpolicies", false, nil},
	"admissionregistration.k8s.io/ValidatingAdmissionPolicyBinding": {"validatingadmissionpolicybindings", false, nil},
	"apiextensions.k8s.io/CustomResourceDefinition":                 {"customresourcedefinitions", false, []string{"crd", "crds"}},
	"apiregistration.k8s.io/APIService":                             {"apiservices", false, nil},
	"certificates.k8s.io/CertificateSigningRequest":                 {"certificatesigningrequests", false, []string{"csr"}},
	"node.k8s.io/RuntimeClass":                                      {"runtimeclasses", false, nil},
	// Gateway API
	"gateway.networking.k8s.io/GatewayClass":     {"gatewayclasses", false, []string{"gc"}},
	"gateway.networking.k8s.io/Gateway":          {"gateways", true, []string{"gtw"}},
	"gateway.networking.k8s.io/HTTPRoute":        {"httproutes", true, nil},
	"gateway.networking.k8s.io/GRPCRoute":        {"grpcroutes", true, nil},
	"gateway.networking.k8s.io/TCPRoute":         {"tcproutes", true, nil},
	"gateway.networking.k8s.io/TLSRoute":         {"tlsroutes", true, nil},
	"gateway.networking.k8s.io/UDPRoute":         {"udproutes", true, nil},
	"gateway.networking.k8s.io/ReferenceGrant":   {"referencegrants", true, nil},
	"gateway.networking.k8s.io/BackendTLSPolicy": {"backendtlspolicies", true, nil},
	// Istio
	"security.istio.io/AuthorizationPolicy":   {"authorizationpolicies", true, []string{"ap"}},
	"security.istio.io/PeerAuthentication":    {"peerauthentications", true, []string{"pa"}},
	"security.istio.io/RequestAuthentication": {"requestauthentications", true, []string{"ra"}},
	"networking.istio.io/VirtualService":      {"virtualservices", true, []string{"vs"}},
	"networking.istio.io/DestinationRule":     {"destinationrules", true, []string{"dr"}},
	"networking.istio.io/ServiceEntry":        {"serviceentries", true, []string{"se"}},
	"networking.istio.io/Gateway":             {"gateways", true, []string{"gw"}},
	"networking.istio.io/Sidecar":             {"sidecars", true, nil},
	"networking.istio.io/EnvoyFilter":         {"envoyfilters", true, nil},
	"networking.istio.io/WorkloadEntry":       {"workloadentries", true, []string{"we"}},
	"networking.istio.io/WorkloadGroup":       {"workloadgroups", true, []string{"wg"}},
	"telemetry.istio.io/Telemetry":            {"telemetries", true, []string{"telemetry"}},
	"install.istio.io/IstioOperator":          {"istiooperators", true, []string{"iop"}},
	"sailoperator.io/Istio":                   {"istios", false, nil},
	// agentgateway, kgateway, kagent
	"agentgateway.dev/AgentgatewayBackend":                            {"agentgatewaybackends", true, nil},
	"agentgateway.dev/AgentgatewayPolicy":                             {"agentgatewaypolicies", true, nil},
	"agentgateway.dev/AgentgatewayParameters":                         {"agentgatewayparameters", true, nil},
	"enterpriseagentgateway.solo.io/EnterpriseAgentgatewayBackend":    {"enterpriseagentgatewaybackends", true, nil},
	"enterpriseagentgateway.solo.io/EnterpriseAgentgatewayPolicy":     {"enterpriseagentgatewaypolicies", true, nil},
	"enterpriseagentgateway.solo.io/EnterpriseAgentgatewayParameters": {"enterpriseagentgatewayparameters", true, nil},
	"gateway.kgateway.dev/TrafficPolicy":                              {"trafficpolicies", true, nil},
	"gateway.kgateway.dev/Backend":                                    {"backends", true, nil},
	"gateway.kgateway.dev/GatewayParameters":                          {"gatewayparameters", true, nil},
	"gateway.kgateway.dev/HTTPListenerPolicy":                         {"httplistenerpolicies", true, nil},
	"gateway.kgateway.dev/BackendConfigPolicy":                        {"backendconfigpolicies", true, nil},
	"gateway.kgateway.dev/DirectResponse":                             {"directresponses", true, nil},
	"gateway.kgateway.dev/GatewayExtension":                           {"gatewayextensions", true, nil},
	"kagent.dev/Agent":                                                {"agents", true, nil},
	"kagent.dev/ModelConfig":                                          {"modelconfigs", true, nil},
	"kagent.dev/RemoteMCPServer":                                      {"remotemcpservers", true, nil},
	"kagent.dev/ToolServer":                                           {"toolservers", true, nil},
	"kagent.dev/Memory":                                               {"memories", true, nil},
	"kagent.dev/MCPServer":                                            {"mcpservers", true, nil},
	"kmcp.dev/MCPServer":                                              {"mcpservers", true, nil},
	"agentregistry.dev/MCPServer":                                     {"mcpservers", true, nil},
	// cert-manager, GitOps
	"cert-manager.io/Certificate":                 {"certificates", true, []string{"cert", "certs"}},
	"cert-manager.io/Issuer":                      {"issuers", true, nil},
	"cert-manager.io/ClusterIssuer":               {"clusterissuers", false, nil},
	"argoproj.io/Application":                     {"applications", true, []string{"app", "apps"}},
	"argoproj.io/ApplicationSet":                  {"applicationsets", true, []string{"appset"}},
	"argoproj.io/AppProject":                      {"appprojects", true, []string{"appproj"}},
	"helm.toolkit.fluxcd.io/HelmRelease":          {"helmreleases", true, []string{"hr"}},
	"kustomize.toolkit.fluxcd.io/Kustomization":   {"kustomizations", true, []string{"ks"}},
	"source.toolkit.fluxcd.io/GitRepository":      {"gitrepositories", true, nil},
	"source.toolkit.fluxcd.io/HelmRepository":     {"helmrepositories", true, nil},
	"source.toolkit.fluxcd.io/OCIRepository":      {"ocirepositories", true, nil},
	"cilium.io/CiliumNetworkPolicy":               {"ciliumnetworkpolicies", true, []string{"cnp"}},
	"cilium.io/CiliumClusterwideNetworkPolicy":    {"ciliumclusterwidenetworkpolicies", false, []string{"ccnp"}},
	"policy.networking.k8s.io/AdminNetworkPolicy": {"adminnetworkpolicies", false, nil},
	"monitoring.coreos.com/ServiceMonitor":        {"servicemonitors", true, nil},
	"monitoring.coreos.com/PodMonitor":            {"podmonitors", true, nil},
	"monitoring.coreos.com/PrometheusRule":        {"prometheusrules", true, nil},
}

// runtimeOnly are the types a repository cannot hold: the cluster creates
// them. A repo scan never proves their absence.
var runtimeOnly = map[string]bool{
	"/pods": true, "/nodes": true, "/events": true, "/endpoints": true, "discovery.k8s.io/endpointslices": true,
	"apps/replicasets": true, "apps/controllerrevisions": true, "coordination.k8s.io/leases": true,
	"events.k8s.io/events": true, "certificates.k8s.io/certificatesigningrequests": true,
}

// RuntimeOnly reports whether a type exists only at runtime (pods, nodes,
// events, endpoints), so a repo scan cannot judge its absence.
func RuntimeOnly(group, resource string) bool { return runtimeOnly[group+"/"+resource] }

// groupVersion splits an apiVersion.
func groupVersion(apiVersion string) (string, string) {
	if i := strings.LastIndex(apiVersion, "/"); i >= 0 {
		return apiVersion[:i], apiVersion[i+1:]
	}
	return "", apiVersion
}

// plural guesses the resource name of an unknown kind the way the API server
// names most custom resources.
func plural(kind string) string {
	k := strings.ToLower(kind)
	switch {
	case strings.HasSuffix(k, "y") && !strings.HasSuffix(k, "ay") && !strings.HasSuffix(k, "ey") && !strings.HasSuffix(k, "oy"):
		return k[:len(k)-1] + "ies"
	case strings.HasSuffix(k, "s") || strings.HasSuffix(k, "x") || strings.HasSuffix(k, "ch") || strings.HasSuffix(k, "sh"):
		return k + "es"
	}
	return k + "s"
}

// resolver turns kinds into API resources: built-in table first, then CRDs
// declared in the repository, then a guess.
type resolver struct {
	crds map[string]kindInfo // group/Kind -> from a CRD in the repo
}

func newResolver() *resolver { return &resolver{crds: map[string]kindInfo{}} }

// addCRD learns a kind from a CustomResourceDefinition in the repo.
func (r *resolver) addCRD(obj map[string]any) {
	spec, _ := obj["spec"].(map[string]any)
	names, _ := spec["names"].(map[string]any)
	group, _ := spec["group"].(string)
	kind, _ := names["kind"].(string)
	pl, _ := names["plural"].(string)
	if group == "" || kind == "" || pl == "" {
		return
	}
	scope, _ := spec["scope"].(string)
	var short []string
	for _, s := range asList(names["shortNames"]) {
		if x, ok := s.(string); ok {
			short = append(short, x)
		}
	}
	r.crds[group+"/"+kind] = kindInfo{pl, scope != "Cluster", short}
}

func (r *resolver) resolve(apiVersion, kind string) (bundle.APIResource, bool) {
	g, v := groupVersion(apiVersion)
	ki, known := builtin[g+"/"+kind]
	if !known {
		ki, known = r.crds[g+"/"+kind]
	}
	if !known {
		ki = kindInfo{plural(kind), true, nil}
	}
	return bundle.APIResource{Group: g, Version: v, Resource: ki.resource, Kind: kind, Namespaced: ki.namespaced,
		SingularName: strings.ToLower(kind), ShortNames: ki.short}, known
}

func asList(v any) []any         { x, _ := v.([]any); return x }
func asMap(v any) map[string]any { x, _ := v.(map[string]any); return x }
func asStr(v any) string         { x, _ := v.(string); return x }
