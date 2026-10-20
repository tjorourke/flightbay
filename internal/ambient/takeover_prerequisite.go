package ambient

import (
	"github.com/tjorourke/flightbay/internal/store"
	"strconv"
	"strings"
)

const K8sWorkloadEntrySelection = "PILOT_ENABLE_K8S_SELECT_WORKLOAD_ENTRIES"

type TakeoverPrerequisite struct {
	State    string     `json:"state"`
	Setting  string     `json:"setting"`
	Detail   string     `json:"detail"`
	Evidence []Evidence `json:"evidence,omitempty"`
	Sources  []Ref      `json:"sources,omitempty"`
}

func waypointTakeoverPrerequisite(ws *store.Workspace, cluster string, selected *ClassChoice) *TakeoverPrerequisite {
	p := &TakeoverPrerequisite{State: "na", Detail: "This istiod prerequisite applies to an Istio/Envoy waypoint; L4 takeover and agentgateway use different discovery paths."}
	if selected == nil || selected.Product != "istio" {
		return p
	}
	states := map[string]bool{}
	inspect := func(o *store.Object, containers []any, path string) {
		for _, v := range containers {
			container := M(v)
			image := strings.ToLower(S(container["image"]))
			if !strings.Contains(image, "/pilot:") {
				continue
			}
			state, setting := "pass", "true (default)"
			for _, v := range L(container["env"]) {
				env := M(v)
				if S(env["name"]) != K8sWorkloadEntrySelection {
					continue
				}
				setting = S(env["value"])
				value, err := strconv.ParseBool(setting)
				if err != nil {
					state = "unknown"
					setting = "unresolved"
				} else if !value {
					state = "fail"
				}
				break
			}
			states[state] = true
			p.Sources = append(p.Sources, reference(o))
			p.Evidence = append(p.Evidence, evidence(o, path+"["+S(container["name"])+"].env["+K8sWorkloadEntrySelection+"]", setting))
			if p.Setting == "" {
				p.Setting = setting
			} else if p.Setting != setting {
				p.Setting = "mixed"
			}
		}
	}
	for _, o := range ws.List(cluster, "apps", "deployments", "", nil) {
		inspect(o, L(Path(o.Obj, "spec", "template", "spec", "containers")), "spec.template.spec.containers")
	}
	if len(states) == 0 {
		for _, o := range ws.List(cluster, "", "pods", "", nil) {
			if Path(o.Obj, "metadata", "deletionTimestamp") == nil {
				inspect(o, L(Path(o.Obj, "spec", "containers")), "spec.containers")
			}
		}
	}
	switch {
	case len(states) == 1 && states["fail"]:
		p.State = "fail"
		p.Detail = K8sWorkloadEntrySelection + " is explicitly false. The Kubernetes Service's Istio waypoint endpoint pool cannot select imported WorkloadEntries. Local-name takeover can return 503 even while ztunnel and the global hostname have healthy remote endpoints. Set the flag to true in istiod's Helm/GitOps configuration, roll out istiod and refresh this capture. The Service checkbox does not change the control plane."
	case len(states) == 1 && states["pass"]:
		p.State = "pass"
		p.Detail = "Imported WorkloadEntry selection is enabled in the captured istiod configuration. This is a prerequisite, not proof of healthy backend pools or successful takeover traffic."
	default:
		p.State = "unknown"
		p.Detail = "The istiod setting is missing, unresolved or differs across captured controllers. Capture the relevant istiod Deployment or Pod and verify " + K8sWorkloadEntrySelection + " is true or uses its enabled default before relying on Istio-waypoint takeover."
	}
	return p
}

func serviceTakeoverEnabled(ws *store.Workspace, svc *store.Object, requested *bool) bool {
	inherited := false
	if ns := Target(ws, svc.Cluster, "Namespace", "", svc.Namespace); ns != nil {
		inherited = ns.Labels()["solo.io/service-takeover"] == "true" || ns.Labels()["solo.io/service-scope"] == "global-only"
	}
	enabled := svc.Labels()["solo.io/service-takeover"] == "true"
	if requested != nil {
		enabled = *requested
	}
	return enabled || inherited || svc.Labels()["solo.io/service-scope"] == "global-only"
}
