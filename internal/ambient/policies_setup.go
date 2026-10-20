package ambient

import (
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/tjorourke/flightbay/internal/store"
)

// Setup rows on Policies & routes: ambient configuration that lives in
// labels on a Service or Namespace rather than in a policy object. Each is
// one row per labelled object, merged across clusters like other rows, and
// flagged setup so the page can style it.
const (
	PolicyKindWaypointUse = "Waypoint use"
	PolicyKindGlobal      = "Global Service"
	PolicyKindEnrolment   = "Ambient enrolment"
)

// setupLabel is the label each setup kind reads, for not-here details.
var setupLabel = map[string]string{
	PolicyKindWaypointUse: "istio.io/use-waypoint",
	PolicyKindGlobal:      "solo.io/service-scope",
	PolicyKindEnrolment:   "istio.io/dataplane-mode",
}

// setupObs lists the setup rows one cluster contributes.
func (c *polCtx) setupObs(cl string) []polObs {
	var out []polObs
	nsLabels := map[string]map[string]string{}
	ztunnel := len(c.ws.List(cl, "", "pods", "", map[string]string{"app": "ztunnel"})) > 0
	for _, n := range c.ws.List(cl, "", "namespaces", "", nil) {
		lb := n.Labels()
		nsLabels[n.Name] = lb
		if v := lb["istio.io/dataplane-mode"]; v == "ambient" || v == "none" {
			out = append(out, c.enrolmentObs(n, v, ztunnel))
		}
		if v := lb["istio.io/use-waypoint"]; v != "" {
			out = append(out, c.waypointUseObs(cl, n, n.Name, v, lb["istio.io/use-waypoint-namespace"]))
		}
	}
	scopes := serviceScopeConfigs(c.ws, cl)
	root := rootNamespace(c.ws, cl)
	for _, s := range c.ws.List(cl, "", "services", "", nil) {
		if isWaypointService(s) {
			continue
		}
		lb := s.Labels()
		if v := lb["istio.io/use-waypoint"]; v != "" {
			out = append(out, c.waypointUseObs(cl, s, s.Namespace, v, lb["istio.io/use-waypoint-namespace"]))
		}
		if scope, source := globalPublication(s, nsLabels[s.Namespace], scopes); scope != "" {
			out = append(out, c.globalObs(cl, root, s, scope, source))
		}
	}
	return out
}

func setupBase(o *store.Object, kind, ns string) polObs {
	ob := polObs{obj: o, kind: kind, setup: true, ns: ns, state: PolicyStateApplied, applies: PolicyAppliesTo{Targets: []Ref{reference(o)}}}
	if o.Kind == "Service" {
		ob.applies.Kind, ob.applies.Phrase = "service", "Service "+o.Name
		ob.primary, ob.grouped, ob.services = o, true, []*store.Object{o}
	} else {
		ob.applies.Kind, ob.applies.Phrase = "namespace", "Namespace "+ns
	}
	return ob
}

// waypointUseObs is one istio.io/use-waypoint label on a Service or Namespace.
func (c *polCtx) waypointUseObs(cl string, o *store.Object, ns, name, wpNS string) polObs {
	ob := setupBase(o, PolicyKindWaypointUse, ns)
	ob.types, ob.enforcedBy = []string{PolicyTypeRouting}, "ztunnel"
	if o.Kind == "Namespace" {
		// The Services that take the waypoint from the namespace label.
		for _, s := range c.ws.List(cl, "", "services", ns, nil) {
			if !isWaypointService(s) && s.Labels()["istio.io/use-waypoint"] == "" {
				ob.services = append(ob.services, s)
			}
		}
	}
	if name == "none" {
		ob.effect = PolicyEffect{Phrase: "Opts out of the waypoint", Action: "Opt out"}
		ob.state, ob.detail, ob.spec = PolicyStateLive, "Label istio.io/use-waypoint=none", "none"
		return ob
	}
	wpNS = defaultString(wpNS, ns)
	wp := wpNS + "/" + name
	ob.effect = PolicyEffect{Phrase: "Uses waypoint " + wp, Action: "Waypoint"}
	ob.spec = wp
	gw := c.ws.Get(cl, GatewayGroup, "gateways", wpNS, name)
	if gw == nil {
		if c.absent(cl, GatewayGroup, "gateways", "Gateway", wpNS) {
			ob.state = PolicyStateFailed
			ob.detail = "Waypoint " + wp + " does not exist on this cluster"
			ob.problems = append(ob.problems, "Waypoint "+wp+" does not exist, so traffic sent to it fails")
		} else {
			ob.detail = "Waypoint " + wp + " was not captured"
		}
		return ob
	}
	conds := L(Path(gw.Obj, "status", "conditions"))
	st, reason, msg := condStatus(conds, "Programmed")
	ob.detail = "Waypoint " + wp + ": " + defaultString(condSummary([][]any{conds}, "Programmed"), "no Programmed condition")
	switch st {
	case "True":
		ob.state = PolicyStateLive
	case "False":
		ob.state = PolicyStateFailed
		ob.problems = append(ob.problems, strings.TrimSpace("Waypoint "+wp+" is not programmed: "+reason+" "+msg))
	}
	return ob
}

// enrolmentObs is the istio.io/dataplane-mode label on a Namespace.
func (c *polCtx) enrolmentObs(n *store.Object, v string, ztunnel bool) polObs {
	ob := setupBase(n, PolicyKindEnrolment, n.Name)
	ob.types, ob.enforcedBy, ob.spec = []string{PolicyTypeTransport}, "ztunnel", v
	ob.detail = "Label istio.io/dataplane-mode=" + v
	if v == "none" {
		ob.effect = PolicyEffect{Phrase: "Opted out of ambient", Action: "Opt out"}
		ob.state = PolicyStateLive
		return ob
	}
	ob.effect = PolicyEffect{Phrase: "In ambient", Action: "Ambient"}
	if ztunnel {
		ob.state = PolicyStateLive
		ob.detail += "; ztunnel is running"
	} else {
		ob.detail += "; no ztunnel Pods captured"
	}
	return ob
}

// globalObs is a Service published to the peered mesh.
func (c *polCtx) globalObs(cl, root string, s *store.Object, scope, source string) polObs {
	ob := setupBase(s, PolicyKindGlobal, s.Namespace)
	ob.types, ob.enforcedBy = []string{PolicyTypeRouting}, "ztunnel"
	lb := s.Labels()
	// Takeover and traffic distribution change behaviour too, so they are
	// part of what has to match between clusters.
	ob.spec = scope + "|" + lb["solo.io/service-takeover"] + "|" + S(Path(s.Obj, "metadata", "annotations", "networking.istio.io/traffic-distribution"))
	ob.effect = PolicyEffect{Phrase: "Published to the peered mesh", Action: "Global"}
	ob.detail = source
	if se := globalServiceEntry(c.ws, cl, root, s); se != nil {
		if hosts := L(Path(se.Obj, "spec", "hosts")); len(hosts) > 0 {
			ob.effect.Phrase = "Published as " + S(hosts[0])
		}
		ob.state = PolicyStateLive
		ob.detail += "; ServiceEntry " + se.Namespace + "/" + se.Name
	} else {
		ob.detail += "; no generated ServiceEntry captured yet"
	}
	if lb["solo.io/service-takeover"] == "true" {
		ob.detail += "; takes over the local name"
	}
	return ob
}

// globalServiceEntry finds the ServiceEntry Istio generated for a global
// Service: autogen.<ns>.<name> in the root namespace, or one labelled with
// the Service as its parent.
func globalServiceEntry(ws *store.Workspace, cl, root string, s *store.Object) *store.Object {
	name := "autogen." + s.Namespace + "." + s.Name
	for _, ns := range []string{root, "istio-system"} {
		if se := ws.Get(cl, "networking.istio.io", "serviceentries", ns, name); se != nil {
			return se
		}
	}
	for _, se := range ws.List(cl, "networking.istio.io", "serviceentries", "", nil) {
		lb := se.Labels()
		if lb["solo.io/parent-service"] == s.Name && lb["solo.io/parent-service-namespace"] == s.Namespace && strings.HasPrefix(lb["solo.io/service-scope"], "global") {
			return se
		}
	}
	return nil
}

// globalPublication says whether a Service is published globally and why:
// its own solo.io/service-scope label, a serviceScopeConfigs entry in
// meshConfig, or the istio.io/global label those entries usually select.
func globalPublication(s *store.Object, nsl map[string]string, scopes []serviceScope) (scope, source string) {
	lb := s.Labels()
	switch v := lb["solo.io/service-scope"]; v {
	case "global", "global-only":
		return v, "Label solo.io/service-scope=" + v
	case "":
	default:
		return "", "" // an explicit local scope wins
	}
	for _, sc := range scopes {
		if !strings.HasPrefix(strings.ToUpper(sc.Scope), "GLOBAL") {
			continue
		}
		if selectorMatches(sc.Services, lb) && selectorMatches(sc.Namespaces, nsl) {
			return strings.ToLower(strings.ReplaceAll(sc.Scope, "_", "-")), "Selected by serviceScopeConfigs in meshConfig"
		}
	}
	if lb["istio.io/global"] == "true" {
		return "global", "Label istio.io/global=true"
	}
	return "", ""
}

type labelSelector struct {
	MatchLabels      map[string]string `json:"matchLabels"`
	MatchExpressions []struct {
		Key      string   `json:"key"`
		Operator string   `json:"operator"`
		Values   []string `json:"values"`
	} `json:"matchExpressions"`
}

type serviceScope struct {
	Scope      string         `json:"scope"`
	Services   *labelSelector `json:"servicesSelector"`
	Namespaces *labelSelector `json:"namespaceSelector"`
}

// selectorMatches applies a Kubernetes label selector. No selector matches
// everything, as in serviceScopeConfigs.
func selectorMatches(sel *labelSelector, lb map[string]string) bool {
	if sel == nil {
		return true
	}
	for k, v := range sel.MatchLabels {
		if lb[k] != v {
			return false
		}
	}
	for _, e := range sel.MatchExpressions {
		v, has := lb[e.Key]
		in := false
		for _, x := range e.Values {
			in = in || x == v
		}
		switch e.Operator {
		case "In":
			if !has || !in {
				return false
			}
		case "NotIn":
			if has && in {
				return false
			}
		case "Exists":
			if !has {
				return false
			}
		case "DoesNotExist":
			if has {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// serviceScopeConfigs reads meshConfig.serviceScopeConfigs from the istio
// ConfigMaps in istio-system.
func serviceScopeConfigs(ws *store.Workspace, cluster string) []serviceScope {
	for _, cm := range ws.List(cluster, "", "configmaps", "istio-system", nil) {
		if cm.Name != "istio" && !strings.HasPrefix(cm.Name, "istio-") {
			continue
		}
		var mesh struct {
			Scopes []serviceScope `json:"serviceScopeConfigs"`
		}
		if yaml.Unmarshal([]byte(S(Path(cm.Obj, "data", "mesh"))), &mesh) == nil && len(mesh.Scopes) > 0 {
			return mesh.Scopes
		}
	}
	return nil
}

// setupAbsent explains a setup row's absence on cluster cl and says whether
// it is drift: the same object is on cl and serves traffic on both clusters.
func (c *polCtx) setupAbsent(ob polObs, holder, cl string) (detail string, drift bool) {
	o := ob.obj
	label := setupLabel[ob.kind]
	if o.Kind == "Namespace" {
		if c.ws.Get(cl, "", "namespaces", "", o.Name) == nil {
			return "Namespace " + o.Name + " is not on this cluster", false
		}
		return "Namespace " + o.Name + " has no " + label + " label here", c.nsServes(holder, o.Name) && c.nsServes(cl, o.Name)
	}
	if c.ws.Get(cl, "", "services", o.Namespace, o.Name) == nil {
		return "Service " + o.Name + " is not on this cluster", false
	}
	if ob.kind == PolicyKindGlobal {
		return "Service " + o.Name + " is not published from this cluster", c.servesOnBoth(ob, cl)
	}
	return "Service " + o.Name + " has no " + label + " label here", c.servesOnBoth(ob, cl)
}

// nsServes is true when a Service in the namespace has ready endpoints.
func (c *polCtx) nsServes(cl, ns string) bool {
	for _, s := range c.ws.List(cl, "", "services", ns, nil) {
		if !isWaypointService(s) && readyEndpoints(c.ws, s) > 0 {
			return true
		}
	}
	return false
}

// singleServiceTarget is the Service a row configures when its appliesTo
// names exactly one Service.
func singleServiceTarget(a PolicyAppliesTo) *Ref {
	if a.Kind != "service" || len(a.Targets) != 1 || a.Targets[0].Kind != "Service" {
		return nil
	}
	return &a.Targets[0]
}
