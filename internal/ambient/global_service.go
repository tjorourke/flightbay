package ambient

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tjorourke/flightbay/internal/store"
)

func globalServiceSupport(ws *store.Workspace, svc *store.Object, env Environment) (bool, string) {
	enterprise := false
	for _, component := range env.Components {
		if component.Product == "Istio" && component.Edition == "enterprise" {
			enterprise = true
		}
	}
	if !enterprise {
		return false, "This template requires a captured Solo Enterprise Istio installation. Refresh inventory with istio-system Pods included; an agentgateway edition alone does not establish global-service support."
	}
	ns := Target(ws, svc.Cluster, "Namespace", "", svc.Namespace)
	if ns == nil || ns.Labels()["istio.io/dataplane-mode"] != "ambient" {
		return false, "This template supports Services in an ambient-enrolled namespace. Capture or configure the namespace enrolment first."
	}
	if !hasResource(ws, svc.Cluster, "networking.istio.io", "serviceentries") {
		return false, "Capture the installed Istio ServiceEntry API before generating global-service publication."
	}
	if svc.Labels()["gateway.networking.k8s.io/gateway-name"] != "" || len(M(Path(svc.Obj, "spec", "selector"))) == 0 || S(Path(svc.Obj, "spec", "type")) == "ExternalName" {
		return false, "Choose an application Service with a workload selector, not a controller-managed gateway Service or ExternalName."
	}
	return true, "Publish this existing Service by setting solo.io/service-scope=global. Review one context at a time; publish the corresponding Service in each participating cluster. Peering and an Enterprise-level Istio licence are prerequisites."
}

func generateGlobalService(ws *store.Workspace, req Request) (*Plan, error) {
	if len(req.Tasks) != 1 || req.Edit != nil {
		return nil, fmt.Errorf("review global-service publication separately from routing or existing-policy edits")
	}
	svc := Target(ws, req.Cluster, "Service", req.Namespace, req.Service)
	if svc == nil {
		return nil, fmt.Errorf("select a captured application Service")
	}
	if req.Unpublish {
		// Taking a Service out of the global mesh needs none of the publish
		// prerequisites: it removes one label and leaves everything else.
		p := basePlan(req, svc)
		p.ServiceUID = S(Path(svc.Obj, "metadata", "uid"))
		scope := svc.Labels()["solo.io/service-scope"]
		if scope == "" {
			// Clear the routing settings that only matter for a global Service.
			change(p, svc, "Remove global routing settings left on this local Service", map[string]any{}, "/metadata/labels/solo.io~1service-takeover", "/metadata/annotations/networking.istio.io~1traffic-distribution")
			p.Notes = append(p.Notes, "This Service has no solo.io/service-scope label in context "+req.Cluster+"; it is already local only.")
		} else {
			change(p, svc, "Stop publishing this Service from "+req.Cluster+" to the peered mesh", map[string]any{}, "/metadata/labels/solo.io~1service-scope", "/metadata/labels/solo.io~1service-takeover", "/metadata/annotations/networking.istio.io~1traffic-distribution")
		}
		if scope == "global-only" {
			p.Notes = append(p.Notes, "This Service used legacy global-only publication, which also took over the local name. Removing the label returns the local cluster.local name to this cluster's endpoints only.")
		}
		p.Notes = append(p.Notes, "Only this Service in context "+req.Cluster+" is changed. Other clusters keep publishing; callers there stop reaching this cluster's endpoints through the global name once the controller removes them.", "The mesh controller removes the generated ServiceEntry entries. Refresh inventory after applying to confirm the published names and addresses have gone.")
		return finishPlan(p)
	}
	if ok, why := globalServiceSupport(ws, svc, DetectEnvironment(ws, req.Cluster, svc)); !ok {
		return nil, fmt.Errorf("%s", why)
	}
	p := basePlan(req, svc)
	p.ServiceUID = S(Path(svc.Obj, "metadata", "uid"))
	values := map[string]any{}
	remove := []string{}
	if svc.Labels()["solo.io/service-scope"] == "global-only" {
		p.Notes = append(p.Notes, "This Service already uses legacy global-only publication; its existing local-name takeover behaviour is preserved.")
	} else {
		values["/metadata/labels/solo.io~1service-scope"] = "global"
	}
	switch d := req.GlobalDistribution; d {
	case "":
	case "default":
		remove = append(remove, "/metadata/annotations/networking.istio.io~1traffic-distribution")
	case "Any", "PreferClose", "PreferRegion", "PreferNetwork":
		values["/metadata/annotations/networking.istio.io~1traffic-distribution"] = d
		if d == "Any" {
			p.Notes = append(p.Notes, "Any spreads connections round robin across every endpoint in every cluster, so the share follows replica counts, not clusters. ztunnel balances per connection; a client that keeps one connection open stays on one backend. Set the same value in each cluster that serves this Service.")
		}
		if td := S(Path(svc.Obj, "spec", "trafficDistribution")); td != "" {
			p.Notes = append(p.Notes, "spec.trafficDistribution is "+td+" on this Service and takes precedence over the annotation; remove it for this setting to apply.")
		}
	default:
		return nil, fmt.Errorf("traffic distribution must be Any, PreferClose, PreferRegion or PreferNetwork")
	}
	if req.GlobalTakeover != nil {
		if *req.GlobalTakeover {
			values["/metadata/labels/solo.io~1service-takeover"] = "true"
			p.Notes = append(p.Notes, "Takeover sends requests for the local cluster.local name through the global hostname as well, so local callers use every cluster's endpoints. Solo recommends a traffic distribution other than PreferNetwork with takeover.")
		} else {
			remove = append(remove, "/metadata/labels/solo.io~1service-takeover")
		}
	}
	change(p, svc, "Publish this Service across the peered mesh with the chosen routing", values, remove...)
	if len(req.GlobalWeights) > 0 {
		if err := globalWeightedSplit(ws, req, svc, p); err != nil {
			return nil, err
		}
	}
	p.Notes = append(p.Notes, "Only this Service in context "+req.Cluster+" is changed. The application is not copied to another cluster. Review the corresponding Service in each cluster that should contribute endpoints.", "Default routing for a global service is PreferNetwork: callers use endpoints in their own cluster and only go to another cluster when none are healthy.", "After applying, refresh inventory to see the actual published global DNS names. Kubernetes dry-run validates the change, not the licence, peer connectivity or end-to-end traffic.")
	return finishPlan(p)
}

// GlobalClusterServiceName is the per-cluster global Service used to address
// one cluster's endpoints: reviews in kind-mesh1 → reviews-kind-mesh1.
func GlobalClusterServiceName(service, cluster string) string {
	clean := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			return r
		}
		if r >= 'A' && r <= 'Z' {
			return r + 32
		}
		return '-'
	}, cluster)
	name := strings.Trim(service+"-"+clean, "-")
	if len(name) > 63 {
		name = strings.TrimRight(name[:63], "-")
	}
	return name
}

// globalWeightedSplit sends a chosen share of requests to each cluster. It
// adds, in this cluster, a global Service that selects only this cluster's
// Pods (so its global hostname means "this cluster"), and an HTTPRoute on the
// Service whose weighted backends are every cluster's hostname. The route is
// enforced by this cluster's Istio waypoint, so callers here are split;
// apply the same plan in each cluster whose callers should be split.
// Verified on kind-mesh1/2: 70/30 gave 137/63 over 200 requests.
func globalWeightedSplit(ws *store.Workspace, req Request, svc *store.Object, p *Plan) error {
	if product, _ := waypointProduct(ws, svc); product != "istio" {
		return fmt.Errorf("a weighted split across clusters needs an Istio waypoint on %s/%s; bind one first", svc.Namespace, svc.Name)
	}
	dns := serviceDNS(ws, svc)
	domain := ""
	for _, n := range dns.GlobalNames {
		if prefix := svc.Name + "." + svc.Namespace + "."; strings.HasPrefix(n, prefix) {
			domain = strings.TrimPrefix(n, prefix)
			break
		}
	}
	if domain == "" {
		domain = "mesh.internal"
		p.Notes = append(p.Notes, "No published global name was captured for this Service yet, so the global domain is assumed to be mesh.internal. Refresh after publishing and review again if your mesh uses another domain.")
	}
	ports := L(Path(svc.Obj, "spec", "ports"))
	if len(ports) == 0 {
		return fmt.Errorf("the Service has no ports")
	}
	port := req.Port
	if port == 0 {
		port = N(M(ports[0])["port"])
	}
	clusters := []string{}
	total := 0
	for c, w := range req.GlobalWeights {
		if w < 0 || w > 1000 {
			return fmt.Errorf("weights must be between 0 and 1000")
		}
		if w > 0 {
			clusters = append(clusters, c)
			total += w
		}
	}
	sort.Strings(clusters)
	if total == 0 || len(clusters) < 2 {
		return fmt.Errorf("give at least two clusters a weight above zero")
	}
	own := GlobalClusterServiceName(svc.Name, req.Cluster)
	labels := map[string]any{"app.kubernetes.io/managed-by": "flightbay", "solo.io/service-scope": "global", "flightbay.dev/cluster-split-for": svc.Name}
	if ws.Get(req.Cluster, "", "services", svc.Namespace, own) == nil {
		cleanPorts := []any{}
		for _, pt := range ports {
			pm := M(pt)
			cp := map[string]any{"port": pm["port"]}
			for _, k := range []string{"name", "protocol", "targetPort", "appProtocol"} {
				if pm[k] != nil {
					cp[k] = pm[k]
				}
			}
			cleanPorts = append(cleanPorts, cp)
		}
		p.Objects = append(p.Objects, map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": own, "namespace": svc.Namespace, "labels": labels}, "spec": map[string]any{"selector": Path(svc.Obj, "spec", "selector"), "ports": cleanPorts}})
	}
	backends := []any{}
	for _, c := range clusters {
		backends = append(backends, map[string]any{"group": "networking.istio.io", "kind": "Hostname", "name": GlobalClusterServiceName(svc.Name, c) + "." + svc.Namespace + "." + domain, "port": port, "weight": req.GlobalWeights[c]})
	}
	p.Objects = append(p.Objects, map[string]any{"apiVersion": "gateway.networking.k8s.io/v1", "kind": "HTTPRoute", "metadata": map[string]any{"name": svc.Name + "-cluster-split", "namespace": svc.Namespace, "labels": map[string]any{"app.kubernetes.io/managed-by": "flightbay"}}, "spec": map[string]any{"parentRefs": []any{map[string]any{"group": "", "kind": "Service", "name": svc.Name, "port": port}}, "rules": []any{map[string]any{"backendRefs": backends}}}})
	p.Notes = append(p.Notes, "Weighted split: this plan adds "+own+" (a global Service selecting only "+req.Cluster+"'s Pods) and an HTTPRoute on "+svc.Name+" whose backends are each cluster's own global hostname. Every cluster in the split needs its "+svc.Name+"-<cluster> Service, so apply this in each one. The split covers callers in a cluster whose waypoint enforces the route; it is per request, so it holds even with connection reuse.")
	return nil
}
