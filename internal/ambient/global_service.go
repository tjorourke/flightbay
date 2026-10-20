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
		// prerequisites. Include the owned split resources in the same review.
		p := basePlan(req, svc)
		p.ServiceUID = S(Path(svc.Obj, "metadata", "uid"))
		for _, o := range globalSplitResources(ws, svc) {
			deleteChange(p, o, "Remove the weighted cluster split for "+svc.Name)
		}
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
		p.Notes = append(p.Notes, "This plan stops publication and removes Flightbay-owned weighted-split resources for this Service in context "+req.Cluster+". Other clusters keep their configuration; stop publication there too to remove the split everywhere.", "The mesh controller removes the generated ServiceEntry entries. Refresh inventory after applying to confirm the published names and addresses have gone.")
		return finishPlan(p)
	}
	env := DetectEnvironment(ws, req.Cluster, svc)
	if ok, why := globalServiceSupport(ws, svc, env); !ok {
		return nil, fmt.Errorf("%s", why)
	}
	if serviceTakeoverEnabled(ws, svc, req.GlobalTakeover) && env.TakeoverPrerequisite.State == "fail" {
		return nil, fmt.Errorf("cannot enable Istio-waypoint local-name takeover: %s", env.TakeoverPrerequisite.Detail)
	}
	p := basePlan(req, svc)
	p.ServiceUID = S(Path(svc.Obj, "metadata", "uid"))
	if serviceTakeoverEnabled(ws, svc, req.GlobalTakeover) && env.TakeoverPrerequisite.State != "na" {
		p.Notes = append(p.Notes, env.TakeoverPrerequisite.Detail)
		p.Evidence = append(p.Evidence, env.TakeoverPrerequisite.Evidence...)
	}
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
	} else if req.GlobalDistribution != "" {
		for _, o := range globalSplitResources(ws, svc) {
			deleteChange(p, o, "Replace weighted cluster routing with the selected traffic distribution")
		}
	}
	p.Notes = append(p.Notes, "Only this Service in context "+req.Cluster+" is changed. The application is not copied to another cluster. Review the corresponding Service in each cluster that should contribute endpoints.", "Default routing for a global service is PreferNetwork: callers use endpoints in their own cluster and only go to another cluster when none are healthy.", "After applying, refresh inventory to see the actual published global DNS names. Kubernetes dry-run validates the change, not the licence, peer connectivity or end-to-end traffic.")
	return finishPlan(p)
}

// globalSplitResources recognises both current and legacy split routes. A name
// alone is never ownership: require Flightbay's labels and the exact parent.
// Delete the route before its backend Service; undo recreates them in reverse.
func globalSplitResources(ws *store.Workspace, svc *store.Object) []*store.Object {
	owned := func(o *store.Object) bool {
		return o != nil && o.Labels()["app.kubernetes.io/managed-by"] == "flightbay" && o.Labels()["flightbay.dev/test-id"] != ""
	}
	out := []*store.Object{}
	route := Target(ws, svc.Cluster, "HTTPRoute", svc.Namespace, svc.Name+"-cluster-split")
	if owned(route) {
		parents := L(Path(route.Obj, "spec", "parentRefs"))
		label := route.Labels()["flightbay.dev/cluster-split-for"]
		if len(parents) == 1 && (label == "" || label == svc.Name) {
			parent := M(parents[0])
			if S(parent["group"]) == "" && S(parent["kind"]) == "Service" && S(parent["name"]) == svc.Name && (S(parent["namespace"]) == "" || S(parent["namespace"]) == svc.Namespace) {
				out = append(out, route)
			}
		}
	}
	alias := Target(ws, svc.Cluster, "Service", svc.Namespace, GlobalClusterServiceName(svc.Name, svc.Cluster))
	if owned(alias) && alias.Name != svc.Name && alias.Labels()["flightbay.dev/cluster-split-for"] == svc.Name {
		out = append(out, alias)
	}
	return out
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
	if own == svc.Name {
		return fmt.Errorf("the per-cluster Service name collides with the application Service; choose a shorter Service name")
	}
	owned := globalSplitResources(ws, svc)
	for _, existing := range []*store.Object{Target(ws, req.Cluster, "Service", svc.Namespace, own), Target(ws, req.Cluster, "HTTPRoute", svc.Namespace, svc.Name+"-cluster-split")} {
		if existing == nil {
			continue
		}
		matched := false
		for _, o := range owned {
			matched = matched || o == existing
		}
		if !matched {
			return fmt.Errorf("%s %s/%s exists but is not the Flightbay-owned cluster split for this Service; inspect its ownership and attachment before editing", existing.Kind, existing.Namespace, existing.Name)
		}
	}
	labels := map[string]any{"app.kubernetes.io/managed-by": "flightbay", "solo.io/service-scope": "global", "flightbay.dev/cluster-split-for": svc.Name}
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
	backends := []any{}
	for _, c := range clusters {
		backends = append(backends, map[string]any{"group": "networking.istio.io", "kind": "Hostname", "name": GlobalClusterServiceName(svc.Name, c) + "." + svc.Namespace + "." + domain, "port": port, "weight": req.GlobalWeights[c]})
	}
	p.Objects = append(p.Objects, map[string]any{"apiVersion": "gateway.networking.k8s.io/v1", "kind": "HTTPRoute", "metadata": map[string]any{"name": svc.Name + "-cluster-split", "namespace": svc.Namespace, "labels": map[string]any{"app.kubernetes.io/managed-by": "flightbay", "flightbay.dev/cluster-split-for": svc.Name}}, "spec": map[string]any{"parentRefs": []any{map[string]any{"group": "", "kind": "Service", "name": svc.Name, "port": port}}, "rules": []any{map[string]any{"backendRefs": backends}}}})
	p.Notes = append(p.Notes, "Weighted split: this plan creates or updates "+own+" (a global Service selecting only "+req.Cluster+"'s Pods) and its HTTPRoute. Matching existing configuration needs no change. Every cluster in the split needs its "+svc.Name+"-<cluster> Service, so review this in each one. The split covers callers in a cluster whose waypoint enforces the route; it is per request, so it holds even with connection reuse.")
	return nil
}

func globalSplitWeights(ws *store.Workspace, svc *store.Object) map[string]int {
	for _, o := range globalSplitResources(ws, svc) {
		if o.Kind != "HTTPRoute" {
			continue
		}
		rules := L(Path(o.Obj, "spec", "rules"))
		if len(rules) != 1 {
			return nil
		}
		weights := map[string]int{}
		for _, ref := range L(M(rules[0])["backendRefs"]) {
			b := M(ref)
			if S(b["group"]) != "networking.istio.io" || S(b["kind"]) != "Hostname" {
				return nil
			}
			found := ""
			for _, cluster := range ws.Clusters() {
				if strings.HasPrefix(S(b["name"]), GlobalClusterServiceName(svc.Name, cluster)+"."+svc.Namespace+".") {
					if found != "" {
						return nil
					}
					found = cluster
				}
			}
			if found == "" {
				return nil
			}
			weights[found] = 1
			if value, ok := b["weight"]; ok {
				weights[found] = N(value)
			}
		}
		return weights
	}
	return nil
}
