package ambient

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tjorourke/flightbay/internal/store"
)

// Environment describes observed installations, not licences or a claim that
// every feature in an installed CRD is supported by the active controller.
type Environment struct {
	TakeoverPrerequisite *TakeoverPrerequisite `json:"takeoverPrerequisite,omitempty"`
	ClassDocs            map[string][]DocLink  `json:"classDocs"`
	TaskDocs             map[string][]DocLink  `json:"taskDocs"`
	Cluster              string                `json:"cluster"`
	Edition              string                `json:"edition"`
	Summary              string                `json:"summary"`
	Components           []Component           `json:"components"`
	Classes              []ClassChoice         `json:"classes"`
	Selected             *ClassChoice          `json:"selected,omitempty"`
	PolicyAPI            string                `json:"policyApi,omitempty"`
	PolicyKind           string                `json:"policyKind,omitempty"`
	Evidence             []string              `json:"evidence"`
}
type Component struct {
	Product  string   `json:"product"`
	Edition  string   `json:"edition"`
	Evidence []string `json:"evidence"`
}
type ClassChoice struct {
	Name       string `json:"name"`
	Controller string `json:"controller,omitempty"`
	Product    string `json:"product"`
	Edition    string `json:"edition"`
	Source     string `json:"source"`
	Supported  bool   `json:"supported"`
	Reason     string `json:"reason"`
}

func classFamily(name, controller string) (string, string) {
	switch controller {
	case "istio.io/mesh-controller":
		return "istio", "unknown"
	case "solo.io/enterprise-agentgateway":
		return "agentgateway", "enterprise"
	}
	if strings.Contains(controller, "agentgateway") && !strings.Contains(controller, "enterprise") {
		return "agentgateway", "oss"
	}
	if controller != "" {
		return "unknown", "unknown"
	}
	switch name {
	case "istio-waypoint":
		return "istio", "unknown"
	case "enterprise-agentgateway-waypoint":
		return "agentgateway", "enterprise"
	case "agentgateway-waypoint":
		return "agentgateway", "oss"
	}
	return "unknown", "unknown"
}
func DetectEnvironment(ws *store.Workspace, c string, target *store.Object) Environment {
	e := Environment{Cluster: c, Edition: "unknown", Components: []Component{}, Classes: []ClassChoice{}, Evidence: []string{}}
	istioOSS, istioEnt := false, false
	imagesSeen := map[string]bool{}
	for _, p := range ws.List(c, "", "pods", "", nil) {
		for _, v := range L(Path(p.Obj, "spec", "containers")) {
			image := S(M(v)["image"])
			if imagesSeen[image] {
				continue
			}
			imagesSeen[image] = true
			low := strings.ToLower(image)
			if strings.Contains(low, "/pilot:") || strings.Contains(low, "/ztunnel:") {
				if strings.Contains(low, "soloio") || strings.Contains(low, "-solo") {
					istioEnt = hasResource(ws, c, "admin.solo.io", "segments")
					e.Evidence = append(e.Evidence, "Istio image: "+image)
				} else if strings.HasPrefix(low, "istio/") || strings.HasPrefix(low, "docker.io/istio/") {
					istioOSS = true
					e.Evidence = append(e.Evidence, "Istio image: "+image)
				}
			}
		}
	}
	istioEdition := "unknown"
	if istioOSS && istioEnt {
		istioEdition = "mixed"
	} else if istioEnt {
		istioEdition = "enterprise"
	} else if istioOSS {
		istioEdition = "oss"
	}
	seen := map[string]bool{}
	addClass := func(name, controller, source string) {
		if seen[name] {
			return
		}
		product, edition := classFamily(name, controller)
		if product == "agentgateway" && edition == "enterprise" && !hasResource(ws, c, "enterpriseagentgateway.solo.io", "enterpriseagentgatewaypolicies") {
			return
		}
		if product == "istio" {
			edition = istioEdition
		}
		supported := product != "unknown"
		reason := "Observed " + source
		if source == "GatewayClass" {
			for _, gc := range ws.List(c, GatewayGroup, "gatewayclasses", "", nil) {
				if gc.Name == name {
					for _, v := range L(Path(gc.Obj, "status", "conditions")) {
						if S(M(v)["type"]) == "Accepted" && S(M(v)["status"]) == "False" {
							supported = false
							reason = "GatewayClass was rejected by its controller"
						}
					}
				}
			}
		}
		e.Classes = append(e.Classes, ClassChoice{name, controller, product, edition, source, supported, reason})
		seen[name] = true
	}
	for _, gc := range ws.List(c, GatewayGroup, "gatewayclasses", "", nil) {
		ctrl := S(Path(gc.Obj, "spec", "controllerName"))
		if ctrl == "istio.io/mesh-controller" || strings.Contains(gc.Name, "waypoint") {
			addClass(gc.Name, ctrl, "GatewayClass")
		}
	}
	for _, gw := range ws.List(c, GatewayGroup, "gateways", "", nil) {
		cls := S(Path(gw.Obj, "spec", "gatewayClassName"))
		if strings.Contains(cls, "waypoint") {
			addClass(cls, "", "deployed Gateway")
		}
	}
	sort.Slice(e.Classes, func(i, j int) bool { return e.Classes[i].Name < e.Classes[j].Name })
	if istioEdition != "unknown" || hasResource(ws, c, "security.istio.io", "authorizationpolicies") {
		e.Components = append(e.Components, Component{"Istio", istioEdition, append([]string{}, e.Evidence...)})
	}
	ossAPI := hasResource(ws, c, "agentgateway.dev", "agentgatewaypolicies")
	entAPI := hasResource(ws, c, "enterpriseagentgateway.solo.io", "enterpriseagentgatewaypolicies")
	if ossAPI {
		e.Components = append(e.Components, Component{"agentgateway API", "oss", []string{"agentgateway.dev/AgentgatewayPolicy discovered"}})
	}
	if entAPI {
		e.Components = append(e.Components, Component{"agentgateway API", "enterprise", []string{"enterpriseagentgateway.solo.io/EnterpriseAgentgatewayPolicy discovered"}})
	}
	oss, ent := istioOSS, istioEnt
	for _, cl := range e.Classes {
		if cl.Edition == "oss" {
			oss = true
		}
		if cl.Edition == "enterprise" {
			ent = true
		}
	}
	if oss && ent {
		e.Edition = "mixed"
		e.Summary = "OSS and Enterprise components observed"
	} else if ent {
		e.Edition = "enterprise"
		e.Summary = "Enterprise components observed"
	} else if oss {
		e.Edition = "oss"
		e.Summary = "OSS components observed"
	} else {
		e.Summary = "Edition not established from this capture"
	}
	var gw *store.Object
	if target != nil {
		switch target.Kind {
		case "Gateway":
			gw = target
		case "Namespace":
			probe := &store.Object{Cluster: c, Namespace: target.Name, Obj: map[string]any{}}
			_, _, gw = Waypoint(ws, probe)
		default:
			_, _, gw = Waypoint(ws, target)
		}
	}
	if gw != nil {
		cls := S(Path(gw.Obj, "spec", "gatewayClassName"))
		for i := range e.Classes {
			if e.Classes[i].Name == cls {
				selected := e.Classes[i]
				e.Selected = &selected
				break
			}
		}
	}
	if e.Selected != nil && e.Selected.Product == "agentgateway" {
		g, res, kind := "agentgateway.dev", "agentgatewaypolicies", "AgentgatewayPolicy"
		if e.Selected.Edition == "enterprise" {
			g, res, kind = "enterpriseagentgateway.solo.io", "enterpriseagentgatewaypolicies", "EnterpriseAgentgatewayPolicy"
		}
		if hasResource(ws, c, g, res) {
			e.PolicyKind = kind
			for _, r := range ws.Resources(c) {
				if r.Group == g && r.Resource == res {
					e.PolicyAPI = g + "/" + r.Version
					break
				}
			}
		} else {
			e.Evidence = append(e.Evidence, "Selected controller's policy API was not captured; generation is unavailable")
		}
	}
	e.TakeoverPrerequisite = waypointTakeoverPrerequisite(ws, c, e.Selected)
	e.Evidence = append(e.Evidence, "CRDs can coexist without active controllers. The selected waypoint/controller determines policy YAML; API-server dry-run still validates the installed schema.")
	e.TaskDocs = map[string][]DocLink{}
	e.TaskDocs["global-service"] = TaskDocs(e, "global-service")
	for _, id := range []string{"require-waypoint", "jwt", "retries", "mirroring", "header-routing", "l4-default-deny", "l4-allow-namespace", "strict-mtls", "methods", "access-rule", "weighted-routing", "timeouts", "load-balancing", "endpoint-failover", "circuit-breaker", "locality-failover", "rate-limit", "service-failover", "global-rate-limit", "waypoint", "enrol-namespace", "endpoints", "route-parent", "service-selector", "service-port", "scale-workload"} {
		e.TaskDocs[id] = TaskDocs(e, id)
	}
	e.ClassDocs = map[string][]DocLink{}
	for _, cl := range e.Classes {
		copy := e
		copy.Selected = &cl
		e.ClassDocs[cl.Name] = TaskDocs(copy, "waypoint")
	}
	return e
}
func ratePolicy(ws *store.Workspace, c, ns, svc string) (string, string, error) {
	target := Target(ws, c, "Service", ns, svc)
	e := DetectEnvironment(ws, c, target)
	if e.Selected == nil || e.Selected.Product != "agentgateway" || !e.Selected.Supported || e.PolicyAPI == "" {
		return "", "", fmt.Errorf("no matching agentgateway controller and policy API were established for this Service")
	}
	return e.PolicyAPI, e.PolicyKind, nil
}
