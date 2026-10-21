package ambient

import (
	"encoding/json"
	"strings"

	"github.com/tjorourke/flightbay/internal/store"
)

func stringJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

type RunbookCommand struct {
	Command  string `json:"command"`
	Evidence string `json:"evidence"`
	Mutates  bool   `json:"mutates,omitempty"`
	Docs     string `json:"docs"`
}

// Runbook contains real-context commands, never commands for the snapshot API.
// Commands requiring a proxy are omitted when its identity was not captured.
func Runbook(ws *store.Workspace, target *store.Object) []RunbookCommand {
	ctx := " --context " + shellQuote(target.Cluster)
	namespace := target.Namespace
	if target.Kind == "Namespace" {
		namespace = target.Name
	}
	out := []RunbookCommand{}
	add := func(command, evidence, guide string, mutates bool) {
		out = append(out, RunbookCommand{command, evidence, mutates, istioDocs + guide})
	}
	scope := " --all-namespaces"
	if namespace != "" {
		scope = " -n " + shellQuote(namespace)
	}
	add("istioctl"+ctx+" analyze"+scope, "Captured resources and controller conditions", "ops/diagnostic-tools/istioctl-analyze/", false)
	add("istioctl"+ctx+" proxy-status", "Captured istiod sync diagnostics in Proxy diagnostics", "ops/diagnostic-tools/proxy-cmd/", false)
	if target.Kind == "Service" {
		if warning := waypointL4Warning(ws, target); warning != "" {
			add("kubectl"+ctx+" get authorizationpolicies.security.istio.io --all-namespaces -o yaml", warning, "ambient/usage/l4-policy/#considerations", false)
			add("kubectl"+ctx+" get pods --all-namespaces -o wide", "Compare the captured waypoint service account with downstream workload ALLOW and DENY policies. 503/UC alone is not proof of policy denial.", "ambient/usage/troubleshoot-waypoint/", false)
		}
	}
	for _, pod := range ws.List(target.Cluster, "", "pods", "", nil) {
		zt := pod.Labels()["app"] == "ztunnel" || pod.Labels()["app.kubernetes.io/name"] == "ztunnel"
		if !zt {
			continue
		}
		for _, kind := range []string{"service", "workload"} {
			add("istioctl"+ctx+" ztunnel-config "+kind+" "+shellQuote(pod.Name)+" -n "+shellQuote(pod.Namespace), "Captured ztunnel "+kind+" configuration in Proxy diagnostics; compare "+target.Namespace+"/"+target.Name, "ambient/usage/troubleshoot-ztunnel/", false)
		}
		break
	}
	probe := *target
	if target.Kind == "Namespace" {
		probe.Namespace = target.Name
	}
	ns, name, wp := Waypoint(ws, &probe)
	if target.Kind == "Gateway" {
		ns, name, wp = target.Namespace, target.Name, target
	}
	if wp == nil {
		return out
	}
	for _, pod := range ws.List(target.Cluster, "", "pods", ns, nil) {
		if pod.Labels()["gateway.networking.k8s.io/gateway-name"] != name {
			continue
		}
		if strings.Contains(S(Path(wp.Obj, "spec", "gatewayClassName")), "agentgateway") {
			break
		}
		add("istioctl"+ctx+" proxy-config all "+shellQuote(pod.Name)+" -n "+shellQuote(ns)+" -o json", "Captured waypoint config_dump.json in Proxy diagnostics", "ambient/usage/troubleshoot-waypoint/", false)
		add("istioctl"+ctx+" pc log "+shellQuote(pod.Name)+" -n "+shellQuote(ns)+" --level debug", "Changes live Envoy logging level; then inspect Log explorer. Restore the previous level afterwards.", "ops/diagnostic-tools/proxy-cmd/", true)
		break
	}
	if ws.Get(target.Cluster, "apps", "deployments", ns, name) != nil {
		add("kubectl"+ctx+" logs "+shellQuote("deploy/"+name)+" -n "+shellQuote(ns), "Captured waypoint container logs in Log explorer", "ambient/usage/troubleshoot-waypoint/", false)
	}
	return out
}

func jwtTargets(p, svc *store.Object, wpNS, wpName string) bool {
	refs := append([]any{}, L(Path(p.Obj, "spec", "targetRefs"))...)
	if ref := M(Path(p.Obj, "spec", "targetRef")); ref != nil {
		refs = append(refs, ref)
	}
	for _, v := range refs {
		t := M(v)
		if S(t["kind"]) == "Service" && S(t["group"]) == "" && p.Namespace == svc.Namespace && S(t["name"]) == svc.Name {
			return true
		}
		if S(t["kind"]) == "Gateway" && S(t["group"]) == GatewayGroup && p.Namespace == wpNS && S(t["name"]) == wpName {
			return true
		}
	}
	return false
}

func inspectJWT(ws *store.Workspace, r *Investigation, svc *store.Object, wpNS, wpName string) {
	ras := []*store.Object{}
	for _, ra := range ws.List(svc.Cluster, "security.istio.io", "requestauthentications", "", nil) {
		if jwtTargets(ra, svc, wpNS, wpName) {
			ras = append(ras, ra)
			add(r, "jwt-"+ra.Name, "JWT validation: "+ra.Name, "warning", "RequestAuthentication validates presented tokens but does not require a token. Signature validity, JWKS availability and request claims cannot be established from captured configuration.", evidence(ra, "spec", ra.Obj["spec"]))
		} else if ra.Namespace == svc.Namespace && len(L(Path(ra.Obj, "spec", "targetRefs"))) == 0 {
			add(r, "jwt-selector-"+ra.Name, "JWT workload attachment: "+ra.Name, "warning", "Workload-selector RequestAuthentication is not enforced by ztunnel. For waypoint JWT validation use a supported Service or Gateway targetRefs attachment.", evidence(ra, "spec", ra.Obj["spec"]))
		}
	}
	for _, ap := range ws.List(svc.Cluster, "security.istio.io", "authorizationpolicies", "", nil) {
		if !jwtTargets(ap, svc, wpNS, wpName) || (!strings.Contains(stringJSON(Path(ap.Obj, "spec", "rules")), "requestPrincipals") && !strings.Contains(stringJSON(Path(ap.Obj, "spec", "rules")), "notRequestPrincipals")) {
			continue
		}
		state, detail := "warning", "This authorization rule requires a validated JWT request principal. Requests without a matching principal cannot use this ALLOW rule; other ALLOW policies are additive. Token signature and claims remain request-dependent."
		if acc, ok := accessFromPolicy(ap); ok {
			detail += " Captured matches: " + strings.Join(accessPhrases(acc, false), " or ") + "."
		}
		if len(ras) == 0 {
			state = missing(ws, svc.Cluster, "security.istio.io", "requestauthentications", "RequestAuthentication", svc.Namespace)
			if wpNS != svc.Namespace && missing(ws, svc.Cluster, "security.istio.io", "requestauthentications", "RequestAuthentication", wpNS) == "unknown" {
				state = "unknown"
			}
			detail = "A requestPrincipals rule has no captured RequestAuthentication on this Service or waypoint. Collect both namespaces and verify matching JWT validation before relying on this rule."
		}
		add(r, "jwt-principals-"+ap.Name, "JWT authorization: "+ap.Name, state, detail, evidence(ap, "spec.rules", Path(ap.Obj, "spec", "rules")))
	}
}
