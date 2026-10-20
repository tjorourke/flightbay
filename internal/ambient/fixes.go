package ambient

import (
	"fmt"
	"regexp"
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
		// One fault is reported once: drop the steps that only fail because
		// an earlier step in the same investigation already failed.
		r.Steps = suppressConsequences(r.Steps)
		o := Target(ws, subject.Cluster, subject.Kind, subject.Namespace, subject.Name)
		if o != nil {
			for i := range r.Steps {
				if r.Steps[i].State == "fail" {
					r.Steps[i].Runbook = Runbook(ws, o)
				}
			}
		}
		v := &View{Investigation: r, Subject: subject}
		addSuggestedFixes(v)
		for _, s := range r.Steps {
			switch s.State {
			case "fail":
			case "warning", "warn":
				w, ok := actionableWarning(ws, o, r, s)
				if !ok {
					continue
				}
				s = w
			default:
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

// routeParentStep matches the per-parent step "route-<ns>-<name>-<i>" written
// by inspectRoute, and its "-listener" / "-eligibility" children.
var (
	routeParentStep  = regexp.MustCompile(`^(route-.+-\d+)$`)
	routeListenerSub = regexp.MustCompile(`^(route-.+-\d+)-(listener|eligibility)$`)
)

// consequenceRule is one known cause → consequence chain inside a single
// investigation (one subject on one cluster). When a step matching cause has
// State "fail", every other step for which drop(key, id) is true is removed:
// it fails only because the cause failed and would show one fault twice.
type consequenceRule struct {
	name  string
	cause func(id string) (key string, ok bool)
	drop  func(key, id string) bool
}

func exactCause(ids ...string) func(string) (string, bool) {
	return func(id string) (string, bool) {
		for _, x := range ids {
			if id == x {
				return id, true
			}
		}
		return "", false
	}
}

// consequenceRules is the table suppressConsequences applies. Step IDs are
// the ones written by Investigate / inspectRoute / investigateObject.
var consequenceRules = []consequenceRule{
	// The selector matches no Pods, so there cannot be ready endpoints.
	{"no-pods", exactCause("workloads"), func(_, id string) bool { return id == "endpoints" }},
	// A route parent that does not resolve (missing, wrong kind, wrong port)
	// cannot be accepted: its per-parent status, listener and eligibility
	// checks are consequences of the same parentRef.
	{"route-parent-unresolved", func(id string) (string, bool) {
		m := routeParentStep.FindStringSubmatch(id)
		if m == nil {
			return "", false
		}
		return m[1], true
	}, func(key, id string) bool {
		return strings.HasPrefix(id, key+"-status") || id == key+"-listener" || id == key+"-eligibility"
	}},
	// The listener section is missing or rejects the route, so the parent's
	// Accepted condition is False for that reason.
	{"route-listener-rejects", func(id string) (string, bool) {
		m := routeListenerSub.FindStringSubmatch(id)
		if m == nil {
			return "", false
		}
		return m[1], true
	}, func(key, id string) bool {
		return id == key+"-status" || (strings.HasPrefix(id, key+"-status-") && strings.HasSuffix(id, "-Accepted"))
	}},
	// The waypoint Gateway is missing, the wrong class, has no HBONE
	// listener, or is not Accepted/Programmed: the Service's waypoint binding
	// ("Service does not use waypoint", istio.io/WaypointBound=False) follows.
	{"waypoint-not-ready", exactCause("waypoint", "waypoint-listener", "waypoint-status-Accepted", "waypoint-status-Programmed"), func(_, id string) bool {
		return strings.HasPrefix(id, "waypoint-bound-")
	}},
	// A Gateway that is not Accepted cannot be Programmed.
	{"gateway-not-accepted", exactCause("waypoint-status-Accepted", "gateway-status-Accepted"), func(key, id string) bool {
		return id == strings.TrimSuffix(key, "Accepted")+"Programmed"
	}},
}

// suppressConsequences removes steps that are consequences of another failed
// step in the same investigation, using consequenceRules. Order is kept.
func suppressConsequences(steps []Step) []Step {
	type hit struct {
		rule int
		key  string
		at   int
	}
	causes := []hit{}
	for i, s := range steps {
		if s.State != "fail" {
			continue
		}
		for ri, rule := range consequenceRules {
			if key, ok := rule.cause(s.ID); ok {
				causes = append(causes, hit{ri, key, i})
			}
		}
	}
	if len(causes) == 0 {
		return steps
	}
	out := make([]Step, 0, len(steps))
	for i, s := range steps {
		dropped := false
		for _, c := range causes {
			if c.at != i && consequenceRules[c.rule].drop(c.key, s.ID) {
				dropped = true
				break
			}
		}
		if !dropped {
			out = append(out, s)
		}
	}
	return out
}

// actionableWarning decides whether a "warning" step goes into Needs
// attention. Warnings are opt-in by step ID: most of them are notes that
// fire for every Service and would bury real faults.
//
// Collected (allow list):
//   - require-waypoint: only when the Service has an enforced waypoint L7
//     AuthorizationPolicy and no ztunnel ALLOW limits callers to the waypoint
//     identity, so the L7 rules can be bypassed. Security relevant.
//   - policy-<name>: only when the policy is accepted but not enforced
//     (PolicyEntry.Warning, e.g. an Istio policy behind an agentgateway waypoint).
//   - waypoint: only when a waypoint is selected and its class is not a
//     recognised waypoint class (not the "no waypoint selected" note).
//   - jwt-selector-<name>: workload-selector RequestAuthentication is not
//     enforced by ztunnel.
//   - jwt-<name>: only when no requestPrincipals rule requires a token, so
//     requests without a token are accepted.
//   - mtls-<name>: PeerAuthentication DISABLE on sidecar Pods.
//   - takeover-control-plane: the waypoint takeover prerequisite.
//
// Not collected (deny list, informational or one per object): enrolment
// (namespace not labelled ambient), pod-* (sidecar / opt-out / not Ready
// Pods; no ready Pods already fails endpoints), waypoint with no waypoint
// selected, route-* parent notes (waypoint-wide or non-waypoint Gateway
// parent), the generic policy-* / agent-policy-* "check its rules" notes,
// jwt-principals-*, external-hosts, dataplane and dns-* (untested DNS), and
// any step ID not listed above.
func actionableWarning(ws *store.Workspace, o *store.Object, r *Investigation, s Step) (Step, bool) {
	svc := o
	if svc != nil && svc.Kind != "Service" {
		svc = nil
	}
	switch {
	case s.ID == "require-waypoint":
		if svc == nil {
			return s, false
		}
		open, deny := waypointBypassOpen(ws, svc)
		if !open {
			return s, false
		}
		s.Detail = "Waypoint policies can be bypassed: callers that reach the Pods directly skip the waypoint and its L7 rules. Add an L4 ALLOW limited to the waypoint identity."
		if deny {
			s.Detail = "L7 DENY can be bypassed: callers that reach the Pods directly skip the waypoint and its DENY rules. Add an L4 ALLOW limited to the waypoint identity."
		}
		return s, true
	case s.ID == "waypoint":
		return s, r != nil && r.Waypoint != ""
	case strings.HasPrefix(s.ID, "jwt-selector-"):
		return s, true
	case strings.HasPrefix(s.ID, "jwt-principals-"):
		return s, false
	case strings.HasPrefix(s.ID, "jwt-"):
		if r == nil {
			return s, false
		}
		for _, x := range r.Steps {
			if strings.HasPrefix(x.ID, "jwt-principals-") {
				return s, false
			}
		}
		s.Detail = "RequestAuthentication validates tokens but no authorization rule requires one, so requests without a token are accepted."
		return s, true
	case strings.HasPrefix(s.ID, "mtls-"), s.ID == "takeover-control-plane":
		return s, true
	case strings.HasPrefix(s.ID, "policy-") && !strings.HasPrefix(s.ID, "policy-status-"):
		if svc == nil {
			return s, false
		}
		name := strings.TrimPrefix(s.ID, "policy-")
		for _, p := range servicePolicies(ws, svc) {
			if p.Ref.Name == name && p.Warning != "" {
				s.Detail = p.Warning
				return s, true
			}
		}
	}
	return s, false
}

// waypointBypassOpen reports whether the Service has an enforced waypoint L7
// authorization policy (open) that callers can skip by going straight to the
// Pods, and whether one of those policies is a DENY.
func waypointBypassOpen(ws *store.Workspace, svc *store.Object) (open, deny bool) {
	_, wpName, _ := Waypoint(ws, svc)
	if wpName == "" {
		return false, false
	}
	pols := servicePolicies(ws, svc)
	l7 := false
	for _, p := range pols {
		if p.Enforced != "waypoint" || p.Warning != "" || p.Ref.Kind == "RequestAuthentication" || p.Action == "" {
			continue
		}
		l7 = true
		if strings.EqualFold(p.Action, "DENY") {
			deny = true
		}
	}
	if !l7 {
		return false, false
	}
	want := waypointPrincipal(ws, svc)
	for _, p := range pols {
		if want == "" || p.Enforced != "ztunnel" || p.Warning != "" || !strings.EqualFold(p.Action, "ALLOW") || p.Ref.Kind != "AuthorizationPolicy" {
			continue
		}
		o := ws.Get(p.Ref.Cluster, p.Ref.Group, p.Ref.Resource, p.Ref.Namespace, p.Ref.Name)
		if o == nil {
			continue
		}
		for _, rv := range L(Path(o.Obj, "spec", "rules")) {
			for _, fv := range L(M(rv)["from"]) {
				for _, pr := range L(Path(M(fv), "source", "principals")) {
					if S(pr) == want {
						return false, false
					}
				}
			}
		}
	}
	return true, deny
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
