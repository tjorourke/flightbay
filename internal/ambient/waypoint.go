package ambient

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tjorourke/flightbay/internal/store"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation"
)

func generateWaypoint(ws *store.Workspace, req Request) (*Plan, error) {
	if len(req.Tasks) != 1 {
		return nil, fmt.Errorf("set up waypoint/enrolment first, then refresh the live inventory before adding traffic policies")
	}
	ns := Target(ws, req.Cluster, "Namespace", "", req.Namespace)
	if ns == nil {
		return nil, fmt.Errorf("capture the target namespace, including its UID and labels")
	}
	p := basePlan(req, ns)
	if req.Tasks[0] == "enrol-namespace" {
		change(p, ns, "Enrol this namespace in ambient", map[string]any{"/metadata/labels/istio.io~1dataplane-mode": "ambient"})
		return finishPlan(p)
	}
	w := req.Waypoint
	if w == nil {
		return nil, fmt.Errorf("choose an existing waypoint or configure a new one")
	}
	if w.Scope != "service" && w.Scope != "namespace" && w.Scope != "shared" && w.Scope != "object" {
		return nil, fmt.Errorf("choose service, namespace, shared or object scope")
	}
	kind := defaultString(req.TargetKind, "Service")
	name := defaultString(req.TargetName, req.Service)
	var target *store.Object
	if kind != "Namespace" {
		target = Target(ws, req.Cluster, kind, req.Namespace, name)
		if target == nil || (kind != "Service" && kind != "ServiceEntry") {
			return nil, fmt.Errorf("object binding supports a captured Service or ServiceEntry")
		}
		if kind == "Service" {
			p.ServiceUID = S(Path(target.Obj, "metadata", "uid"))
		}
	}
	if (w.Scope == "service" || w.Scope == "object") && target == nil {
		return nil, fmt.Errorf("choose an object for object-scoped binding")
	}
	if len(validation.IsDNS1123Label(w.Name)) > 0 || len(w.Name) > 63 {
		return nil, fmt.Errorf("choose a valid waypoint name up to 63 characters")
	}
	wpNS := defaultString(w.Namespace, req.Namespace)
	if Target(ws, req.Cluster, "Namespace", "", wpNS) == nil {
		return nil, fmt.Errorf("capture the waypoint namespace %s first", wpNS)
	}
	names := []string{req.Namespace}
	if w.Scope == "shared" {
		names = nil
		for _, n := range w.Namespaces {
			if !contains(names, n) {
				names = append(names, n)
			}
		}
		if len(names) == 0 {
			return nil, fmt.Errorf("choose namespaces to enrol in the shared waypoint")
		}
		if !contains(names, req.Namespace) {
			return nil, fmt.Errorf("include the selected target namespace in the shared enrolment")
		}
	}
	for _, n := range names {
		if Target(ws, req.Cluster, "Namespace", "", n) == nil {
			return nil, fmt.Errorf("namespace %s must be captured before it can be enrolled", n)
		}
	}
	allowed := append([]string{wpNS}, names...)
	sortStrings(allowed)
	unique := []string{}
	for _, n := range allowed {
		if !contains(unique, n) {
			unique = append(unique, n)
		}
	}
	permission := func(names []string) map[string]any {
		return map[string]any{"namespaces": map[string]any{"from": "Selector", "selector": map[string]any{"matchExpressions": []any{map[string]any{"key": "kubernetes.io/metadata.name", "operator": "In", "values": names}}}}}
	}
	wp := Target(ws, req.Cluster, "Gateway", wpNS, w.Name)
	switch w.Mode {
	case "new":
		if wp != nil {
			return nil, fmt.Errorf("Gateway %s/%s already exists; choose it as an existing waypoint or choose another name", wpNS, w.Name)
		}
		if w.Class != "istio-waypoint" && w.Class != "agentgateway-waypoint" && w.Class != "enterprise-agentgateway-waypoint" {
			return nil, fmt.Errorf("choose a supported waypoint class")
		}
		p.Objects = append(p.Objects, map[string]any{"apiVersion": GatewayGroup + "/v1", "kind": "Gateway", "metadata": map[string]any{"name": w.Name, "namespace": wpNS, "labels": map[string]any{"istio.io/waypoint-for": "service"}}, "spec": map[string]any{"gatewayClassName": w.Class, "listeners": []any{map[string]any{"name": "mesh", "port": 15008, "protocol": "HBONE", "allowedRoutes": permission(unique)}}}})
	case "existing":
		if wp == nil || !strings.Contains(S(Path(wp.Obj, "spec", "gatewayClassName")), "waypoint") {
			return nil, fmt.Errorf("choose a captured waypoint Gateway")
		}
		traffic := defaultString(wp.Labels()["istio.io/waypoint-for"], "service")
		if traffic != "service" && traffic != "all" {
			return nil, fmt.Errorf("this waypoint does not handle service-addressed traffic")
		}
		listeners := L(Copy(Path(wp.Obj, "spec", "listeners")))
		changed := false
		hbone := false
		for _, v := range listeners {
			l := M(v)
			if !waypointMeshListener(wp, l) {
				continue
			}
			hbone = true
			allAllowed := true
			for _, n := range names {
				if !namespaceAllowed(ws, req.Cluster, wpNS, n, M(l["allowedRoutes"])) {
					allAllowed = false
				}
			}
			if allAllowed {
				continue
			}
			if !w.GrantAccess {
				return nil, fmt.Errorf("waypoint does not allow the selected namespaces; enable the explicit namespace-access change or choose another waypoint")
			}
			ar := M(l["allowedRoutes"])
			rule := M(ar["namespaces"])
			from := defaultString(S(rule["from"]), "Same")
			keep := append([]string{}, unique...)
			if from == "Selector" {
				selector := M(rule["selector"])
				expr := L(selector["matchExpressions"])
				if len(M(selector["matchLabels"])) != 0 || len(expr) != 1 || S(M(expr[0])["key"]) != "kubernetes.io/metadata.name" || S(M(expr[0])["operator"]) != "In" {
					return nil, fmt.Errorf("this waypoint uses a general namespace selector; review its access policy manually rather than replacing it")
				}
				for _, v := range L(M(expr[0])["values"]) {
					if n := S(v); n != "" && !contains(keep, n) {
						keep = append(keep, n)
					}
				}
			} else if from != "Same" {
				return nil, fmt.Errorf("cannot safely extend this waypoint namespace policy")
			}
			sortStrings(keep)
			if ar == nil {
				ar = map[string]any{}
			}
			ar["namespaces"] = permission(keep)["namespaces"]
			l["allowedRoutes"] = ar
			changed = true
		}
		if !hbone {
			return nil, fmt.Errorf("selected waypoint has no HBONE listener")
		}
		if changed {
			change(p, wp, "Extend only the HBONE namespace allowlist, preserving existing allowed names", map[string]any{"/spec/listeners": listeners})
		}
	default:
		return nil, fmt.Errorf("choose existing or new waypoint mode")
	}
	wpLabels := map[string]any{"/metadata/labels/istio.io~1use-waypoint": w.Name, "/metadata/labels/istio.io~1use-waypoint-namespace": wpNS}
	if w.Scope == "service" || w.Scope == "object" {
		change(p, target, "Bind only this object to the selected waypoint", wpLabels)
	} else {
		for _, name := range names {
			n := Target(ws, req.Cluster, "Namespace", "", name)
			fields := map[string]any{}
			for k, v := range wpLabels {
				fields[k] = v
			}
			if w.EnrolAmbient {
				fields["/metadata/labels/istio.io~1dataplane-mode"] = "ambient"
			}
			change(p, n, "Set this namespace's default waypoint", fields)
		}
		if target != nil && target.Labels()["istio.io/use-waypoint"] != "" {
			if w.ClearOverride {
				change(p, target, "Remove this object's override so it inherits the namespace default", map[string]any{}, "/metadata/labels/istio.io~1use-waypoint", "/metadata/labels/istio.io~1use-waypoint-namespace")
			} else {
				p.Notes = append(p.Notes, "The selected object has an explicit waypoint override. It is preserved and can override this namespace default; choose the explicit clear-override option if that is intended.")
			}
		}
	}
	if (w.Scope == "service" || w.Scope == "object") && w.EnrolAmbient {
		change(p, ns, "Enrol the target namespace in ambient", map[string]any{"/metadata/labels/istio.io~1dataplane-mode": "ambient"})
	}
	p.Notes = append(p.Notes, "There is no automatic global waypoint default. Shared scope enrols only the selected namespaces. Service/object overrides remain effective unless explicitly cleared.", "A new Gateway is created and must report Programmed before bindings are changed. Undo restores the previous bindings before removing the new Gateway. Refresh a live inventory or take a new capture to investigate the resulting state.")
	return finishPlan(p)
}
func namespaceAllowed(ws *store.Workspace, c, wpNS, ns string, ar map[string]any) bool {
	n := M(ar["namespaces"])
	switch defaultString(S(n["from"]), "Same") {
	case "All":
		return true
	case "Same":
		return wpNS == ns
	case "Selector":
		o := Target(ws, c, "Namespace", "", ns)
		if o == nil {
			return false
		}
		b, _ := json.Marshal(n["selector"])
		var sel metav1.LabelSelector
		if json.Unmarshal(b, &sel) != nil {
			return false
		}
		ls, e := metav1.LabelSelectorAsSelector(&sel)
		return e == nil && ls.Matches(labels.Set(o.Labels()))
	}
	return false
}
