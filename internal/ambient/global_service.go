package ambient

import (
	"fmt"

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
	if ok, why := globalServiceSupport(ws, svc, DetectEnvironment(ws, req.Cluster, svc)); !ok {
		return nil, fmt.Errorf("%s", why)
	}
	p := basePlan(req, svc)
	p.ServiceUID = S(Path(svc.Obj, "metadata", "uid"))
	if svc.Labels()["solo.io/service-scope"] == "global-only" {
		p.Notes = append(p.Notes, "This Service already uses legacy global-only publication; its existing local-name takeover behaviour is preserved.")
	} else {
		change(p, svc, "Publish this Service across the peered mesh", map[string]any{"/metadata/labels/solo.io~1service-scope": "global"})
	}
	p.Notes = append(p.Notes, "Only this Service in context "+req.Cluster+" is changed. The application is not copied to another cluster. Review the corresponding Service in each cluster that should contribute endpoints.", "The mesh controller generates the global ServiceEntry and DNS hostnames. This plan does not create or edit generated ServiceEntries, establish peering, or change service-takeover settings.", "After applying, refresh inventory to see the actual published global DNS names. Kubernetes dry-run validates the label patch, not the licence, peer connectivity or end-to-end traffic.")
	return finishPlan(p)
}
