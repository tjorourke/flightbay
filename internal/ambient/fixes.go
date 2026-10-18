package ambient

import (
	"fmt"
	"strings"

	"github.com/tjorourke/flightbay/internal/store"
)

// FixGuide describes a repair, not proof that the failure has that cause.
// The catalogue is deliberately not presented as a measured frequency ranking.
type FixGuide struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Fix     string `json:"fix"`
	Verify  string `json:"verify"`
	Command string `json:"command,omitempty"`
}

type AmbientIssue struct {
	Subject Ref  `json:"subject"`
	Step    Step `json:"step"`
}

// Issues is shared by the inventory and Findings. Limit analysis to clusters
// with confirmed ambient evidence; a Gateway API install alone is insufficient.
func Issues(ws *store.Workspace) []AmbientIssue {
	services := []Service{}
	objects := []Ref{}
	for _, a := range AvailabilityByCluster(ws) {
		if !a.Available {
			continue
		}
		for _, o := range ws.List(a.Cluster, "", "services", "", nil) {
			services = append(services, Service{Ref: reference(o)})
		}
		for _, res := range []string{"gateways", "httproutes"} {
			for _, o := range ws.List(a.Cluster, GatewayGroup, res, "", nil) {
				objects = append(objects, reference(o))
			}
		}
	}
	return knownIssues(ws, services, objects)
}

// Scan once when inventory is refreshed. Reuse investigation rules so the
// landing page and drill-down agree, including collection-completeness checks.
func knownIssues(ws *store.Workspace, services []Service, objects []Ref) []AmbientIssue {
	issues := []AmbientIssue{}
	seen := map[string]bool{}
	collect := func(subject Ref, r *Investigation) {
		if o := Target(ws, subject.Cluster, subject.Kind, subject.Namespace, subject.Name); o != nil {
			for i := range r.Steps {
				if r.Steps[i].State == "fail" {
					r.Steps[i].Runbook = Runbook(ws, o)
				}
			}
		}
		v := &View{Investigation: r, Subject: subject}
		addSuggestedFixes(v)
		for _, s := range r.Steps {
			if s.State != "fail" {
				continue
			}
			key := subject.Cluster + "/" + subject.Kind + "/" + subject.Namespace + "/" + subject.Name + "/" + s.ID
			if len(s.Evidence) > 0 {
				e := s.Evidence[0]
				key = e.Ref.Cluster + "/" + e.Ref.Kind + "/" + e.Ref.Namespace + "/" + e.Ref.Name + "/" + s.ID
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			issues = append(issues, AmbientIssue{subject, s})
		}
	}
	for _, s := range services {
		if r, err := Investigate(ws, s.Cluster, s.Namespace, s.Name, ""); err == nil {
			collect(s.Ref, r)
		}
	}
	// Include orphan routes and Gateways even when no Service refers to them.
	for _, ref := range objects {
		if ref.Kind != "HTTPRoute" && ref.Kind != "Gateway" {
			continue
		}
		if o := Target(ws, ref.Cluster, ref.Kind, ref.Namespace, ref.Name); o != nil {
			collect(ref, investigateObject(ws, o))
		}
	}
	return issues
}

func CommonFixes() []FixGuide {
	return []FixGuide{
		{"waypoint-bound", "Service waypoint binding rejected", "Read the Service's istio.io/WaypointBound reason and message. Check the selected Gateway, namespace access and service traffic type before reviewing a binding repair.", "The Service reports istio.io/WaypointBound=True for its current configuration; repeat the original request.", ""},
		{"jwt", "JWT authentication or attachment needs attention", "Attach RequestAuthentication and requestPrincipals authorization to the same Service or waypoint. RequestAuthentication alone permits requests without a token. Verify issuer, JWKS and the intended subject restrictions.", "Test a valid token, an invalid token and a request without a token. Captured policy does not verify token signatures.", ""},
		{"enrolment", "Namespace not enrolled in ambient", "If ambient is intended, label the namespace istio.io/dataplane-mode=ambient. Check Pod overrides and sidecar injection before changing the namespace.", "Confirm the namespace label, then inspect Pod redirection and ztunnel workload registration.", ""},
		{"pod-dataplane", "Pod opted out or still has a sidecar", "Remove an unintended Pod-template opt-out or sidecar injection setting on its owning workload. For migration, roll out that workload after reviewing its traffic policies; do not delete a sidecar from a running Pod.", "New Pods have the intended dataplane and appear in ztunnel workload configuration.", ""},
		{"readiness", "Workload not Ready", "Inspect Pod events and container status. Correct the reported probe, image, scheduling or application-startup problem on the owning workload.", "Pods report Ready and the Service has ready EndpointSlice addresses.", ""},
		{"workloads", "Service selector matches no Pods", "Choose the intended workload and use its stable labels as the Service selector. Check for a deliberately selectorless Service before editing.", "The selector matches the intended Pods and EndpointSlices contain their addresses.", ""},
		{"endpoints", "Service has no ready endpoints", "Check selector matching, replica count, Pod readiness and targetPort. Use endpoint recovery to review a selector, scale or port change supported by captured evidence.", "Ready EndpointSlice addresses exist, then repeat the original request.", ""},
		{"waypoint", "Waypoint missing or wrong class", "For Service L7 controls, select an existing service-capable waypoint or deploy one using an installed waypoint GatewayClass. Keep ingress and waypoint roles distinct.", "The selected Gateway is Accepted and Programmed, and its binding resolves in this cluster.", ""},
		{"waypoint-listener", "Waypoint transport listener missing", "Check the installed implementation's listener format. Istio uses HBONE on 15008; agentgateway can declare HTTP mesh on 15088 with generated HBONE transport. Repair Gateway configuration or reconciliation, not its generated Service.", "The Gateway is Programmed and its rendered Service exposes the expected HBONE transport.", ""},
		{"traffic-type", "Waypoint handles the wrong traffic type", "Select a waypoint whose istio.io/waypoint-for label supports service/all for Service traffic, or workload/all for direct Pod-IP traffic. Review the scope before broadening an existing waypoint.", "Repeat the request using the intended Service or Pod address and inspect the waypoint path.", ""},
		{"waypoint-namespace", "Shared waypoint rejects a namespace", "Review the cross-namespace waypoint binding and the mesh listener's allowedRoutes namespace selector. Permit the intended namespace explicitly rather than opening every namespace.", "The namespace matches the listener allowlist and traffic reaches the shared waypoint.", ""},
		{"gateway-status", "Gateway not Accepted or Programmed", "Read the controller's reason and message. Check GatewayClass, listener validity, controller Pods and generated workload events; correct the reported field before retrying.", "Accepted and Programmed are True for the current generation.", ""},
		{"route-parent", "HTTPRoute parent missing or incorrect", "Choose the intended existing Gateway, namespace and listener, or explicitly choose a frontend Service for GAMMA routing. A backend Service does not prove frontend intent. Review only the affected parent entry.", "The repaired parent has current Accepted=True and ResolvedRefs=True conditions; repeat the original request.", ""},
		{"route-listener", "Route references the wrong listener", "Set parentRefs.sectionName to an existing compatible listener and, when specified, use that listener's port. Do not substitute the backend Service port for the Gateway listener port.", "The parent resolves to the intended listener and its Accepted condition is True.", ""},
		{"route-eligibility", "Gateway listener rejects the route", "Check HTTP/HTTPS protocol, allowedRoutes.kinds, namespace permission, hostname intersection, sectionName and port. Change only the constraint that excludes the intended route.", "The route reports Accepted=True on the intended Gateway listener.", ""},
		{"route-status", "Route status missing, stale or rejected", "Refresh the route and controller status. Inspect the parent-specific reason, observedGeneration and controller logs. Do not treat status for an older generation as acceptance of this change.", "Current-generation Accepted and ResolvedRefs conditions exist for the exact parent.", ""},
		{"backend", "Backend Service or Service port is wrong", "Select an existing backend in the intended namespace and set backendRefs.port to its Service port, not its container targetPort. Preserve route matches, filters and other backend weights.", "ResolvedRefs is True, the backend has ready endpoints and the request reaches it.", ""},
		{"backend-grant", "Cross-namespace backend lacks permission", "Create or correct a ReferenceGrant in the backend namespace, limited to the route's namespace and the intended Service. Gateway allowedRoutes is a different permission.", "ResolvedRefs becomes True for the route's parent without opening unrelated backend references.", ""},
		{"policy-l7", "L7 policy sent to ztunnel", "Move HTTP attributes to an AuthorizationPolicy targetRefs attachment supported by the selected waypoint, preserving the original caller and operation restrictions. Keep workload-selector policy L4-only.", "The policy is accepted at the waypoint; test an allowed request and a request that must be denied.", ""},
		{"policy-status", "Policy not attached or enforcing unexpectedly", "Inspect targetRefs, namespace, API edition and controller attachment conditions. Compare applicable DENY and additive ALLOW policies against the actual caller identity and enforcement point. Do not remove access controls as a connectivity fix.", "Current attachment conditions are accepted and both allowed and denied request tests behave as intended.", ""},
		{"mtls", "Unsupported ambient mTLS DISABLE setting", "Replace an unintended DISABLE with the reviewed STRICT or PERMISSIVE mode, including port-level overrides. Edit the existing policy instead of adding a competing PeerAuthentication.", "Effective policy has no unsupported DISABLE mode; test mesh and permitted non-mesh callers according to intent.", ""},
		{"dataplane", "Accepted config not delivered to the proxy", "Capture fresh waypoint and ztunnel diagnostics, inspect xDS sync and controller connectivity, and compare delivered routes with the accepted resources. Fix the reported sync or transport error before restarting components.", "The intended current configuration appears in the proxy and the original request succeeds.", ""},
	}
}

func addSuggestedFixes(v *View) {
	guides := map[string]FixGuide{}
	for _, g := range CommonFixes() {
		guides[g.ID] = g
	}
	for i := range v.Steps {
		s := &v.Steps[i]
		if s.State == "pass" || s.State == "na" {
			continue
		}
		if s.ID == "dataplane" && s.State == "warning" {
			continue
		} // Availability is not a reported delivery error.
		id := s.ID
		switch {
		case strings.HasPrefix(id, "waypoint-bound"):
			id = "waypoint-bound"
		case strings.HasPrefix(id, "jwt-"):
			id = "jwt"
		case strings.HasPrefix(id, "pod-"):
			id = "readiness"
			if strings.Contains(s.Detail, "sidecar") || strings.Contains(s.Detail, "opts out") {
				id = "pod-dataplane"
			}
		case id == "readiness":
		case strings.HasPrefix(id, "mtls-"):
			id = "mtls"
		case strings.HasPrefix(id, "policy-status-") || strings.HasPrefix(id, "agent-policy-"):
			id = "policy-status"
		case strings.HasPrefix(id, "policy-"):
			id = "policy-status"
			if strings.Contains(s.Detail, "L7 attributes") {
				id = "policy-l7"
			}
		case strings.HasPrefix(id, "waypoint-status-") || strings.HasPrefix(id, "gateway-status-"):
			id = "gateway-status"
		case strings.HasPrefix(id, "backend-"):
			id = "backend"
			if s.Title == "Cross-namespace backend permission" {
				id = "backend-grant"
			}
		case strings.HasPrefix(id, "route-"):
			id = "route-parent"
			switch {
			case strings.HasSuffix(s.ID, "-eligibility"):
				id = "route-eligibility"
			case strings.HasSuffix(s.ID, "-listener"):
				id = "route-listener"
			case strings.Contains(s.ID, "-status"):
				id = "route-status"
			}
		}
		g, ok := guides[id]
		if !ok {
			continue
		}
		if s.State == "unknown" {
			g.Fix = "First refresh or collect the missing resource and its controller status in the selected cluster. Missing capture data is not proof of a broken resource. If the problem is confirmed: " + g.Fix
		}
		if len(s.Evidence) > 0 {
			e := s.Evidence[0].Ref
			resource := e.Resource
			if e.Group != "" {
				resource += "." + e.Group
			}
			g.Command = "kubectl --context " + shellQuote(e.Cluster) + " get " + shellQuote(resource) + " " + shellQuote(e.Name)
			if e.Namespace != "" {
				g.Command += " -n " + shellQuote(e.Namespace)
			}
			g.Command += " -o yaml"
		}
		if s.NextStep != "" {
			g.Fix = s.NextStep
		}
		s.SuggestedFix = &g
		if (id == "route-parent" || id == "route-listener" || id == "route-eligibility") && v.Subject.Kind == "Service" && s.State != "unknown" && len(s.Actions) == 0 {
			for _, e := range s.Evidence {
				if e.Ref.Kind == "HTTPRoute" {
					ref := e.Ref
					s.Actions = append(s.Actions, Remediation{"route-parent", "Review parent repair", "Choose the intended captured Gateway or frontend Service and preview the affected parent entry.", &ref})
					break
				}
			}
		}
	}
}

func shellQuote(s string) string { return fmt.Sprintf("'%s'", strings.ReplaceAll(s, "'", "'\"'\"'")) }
