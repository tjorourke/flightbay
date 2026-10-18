package server

import (
	"fmt"
	"regexp"

	"github.com/tjorourke/flightbay/internal/ambient"
	"github.com/tjorourke/flightbay/internal/posture"
)

func (s *Server) generateReviewedPlan(req ambient.Request) (*ambient.Plan, error) {
	if req.Scorecard == nil {
		return ambient.Generate(s.WS, req)
	}
	q := req.Scorecard
	ref := q.Target
	if ref.Cluster != req.Cluster {
		return nil, fmt.Errorf("Scorecard target must belong to the selected cluster")
	}
	var item *posture.Item
	for _, check := range posture.Evaluate(s.WS).Checks {
		if check.ID == q.Check {
			for _, i := range check.Items {
				if i.Cluster == ref.Cluster && i.Kind == ref.Kind && i.Group == ref.Group && i.Resource == ref.Resource && i.Namespace == ref.Namespace && i.Name == ref.Name && (i.Status == posture.Fail || i.Status == posture.Warn) {
					v := i
					item = &v
					break
				}
			}
		}
	}
	if item == nil {
		return nil, fmt.Errorf("this entry no longer fails; refresh the Scorecard")
	}
	target := ambient.Target(s.WS, ref.Cluster, ref.Kind, ref.Namespace, ref.Name)
	if target == nil {
		return nil, fmt.Errorf("the affected resource must be captured before generating a repair")
	}
	ns := ref.Namespace
	if ref.Kind == "Namespace" {
		ns = ref.Name
	}
	if ns == "" || req.Namespace != ns {
		return nil, fmt.Errorf("select the affected namespace explicitly")
	}
	api, kind, name := "security.istio.io/v1", "AuthorizationPolicy", "flightbay-scorecard-default-deny"
	spec := map[string]any{}
	notes := []string{"Review this exact cluster and namespace. This is a real access-control change; validate and confirm before applying."}
	switch q.Check {
	case "ambient.default-deny":
		if ref.Kind != "Namespace" {
			return nil, fmt.Errorf("this repair requires a Namespace entry")
		}
		notes = append(notes, "Adds a namespace-wide empty ALLOW policy. Traffic without a matching ALLOW policy will be denied. Existing ALLOW policies remain additive; review legitimate callers before applying.")
	case "k8s.netpol-default-deny":
		if ref.Kind != "Namespace" {
			return nil, fmt.Errorf("this repair requires a Namespace entry")
		}
		api, kind, name = "networking.k8s.io/v1", "NetworkPolicy", "flightbay-scorecard-deny-ingress"
		spec = map[string]any{"podSelector": map[string]any{}, "policyTypes": []any{"Ingress"}}
		notes = append(notes, "Denies ingress not allowed by another NetworkPolicy, subject to CNI enforcement. Review application traffic and ambient HBONE allowances before applying.")
	case "ambient.mtls-strict":
		if ref.Kind != "Namespace" {
			return nil, fmt.Errorf("edit the existing workload or port-level PeerAuthentication; do not add a competing policy")
		}
		for _, p := range s.WS.List(req.Cluster, "security.istio.io", "peerauthentications", ns, nil) {
			if ambient.Path(p.Obj, "spec", "selector") == nil {
				return nil, fmt.Errorf("a namespace PeerAuthentication already exists; inspect and edit its mTLS mode rather than adding a competing policy")
			}
		}
		kind, name = "PeerAuthentication", "flightbay-scorecard-strict-mtls"
		spec = map[string]any{"mtls": map[string]any{"mode": "STRICT"}}
		notes = append(notes, "Requires mesh mTLS for this namespace. Non-mesh callers using plaintext will be rejected; workload-level policy can override namespace defaults.")
	case "mesh.global-service-authz":
		if ref.Kind != "Service" || len(q.Principals) == 0 {
			return nil, fmt.Errorf("choose at least one intended caller principal before reviewing this Service policy")
		}
		valid := regexp.MustCompile(`^[a-zA-Z0-9.-]+/ns/[a-z0-9][a-z0-9-]*/sa/[a-z0-9][a-z0-9-]*$`)
		for _, p := range q.Principals {
			if !valid.MatchString(p) {
				return nil, fmt.Errorf("use explicit trust-domain/ns/namespace/sa/service-account principals; wildcards are not accepted")
			}
		}
		name = "flightbay-scorecard-" + ref.Name + "-callers"
		_, _, wp := ambient.Waypoint(s.WS, target)
		if wp != nil {
			env := ambient.DetectEnvironment(s.WS, req.Cluster, target)
			if env.Selected == nil || env.Selected.Product != "istio" {
				return nil, fmt.Errorf("this waypoint requires a controller-specific caller policy; inspect its implementation before generating YAML")
			}
			spec["targetRefs"] = []any{map[string]any{"group": "", "kind": "Service", "name": ref.Name}}
		} else {
			sel := ambient.Path(target.Obj, "spec", "selector")
			if len(ambient.M(sel)) == 0 {
				return nil, fmt.Errorf("selectorless Services require an explicit enforcement point")
			}
			spec["selector"] = map[string]any{"matchLabels": sel}
		}
		principals := []any{}
		for _, p := range q.Principals {
			principals = append(principals, p)
		}
		spec["action"] = "ALLOW"
		spec["rules"] = []any{map[string]any{"from": []any{map[string]any{"source": map[string]any{"principals": principals}}}}}
		notes = append(notes, "Only the explicitly entered callers are allowed by this policy. Existing ALLOW policies remain additive and may still permit other callers. Without a waypoint this selects workloads and affects every Service selecting those Pods.")
	default:
		return nil, fmt.Errorf("this check needs resource-specific choices; review its YAML and guidance, then inspect the affected object before applying a change")
	}
	obj := map[string]any{"apiVersion": api, "kind": kind, "metadata": map[string]any{"name": name, "namespace": ns}, "spec": spec}
	return ambient.ReviewedPolicyPlan(req, target, obj, notes...)
}
