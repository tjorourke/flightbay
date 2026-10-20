package ambient

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/tjorourke/flightbay/internal/store"
	"sigs.k8s.io/yaml"
)

type FieldChange struct {
	Path          string `json:"path"`
	Before        any    `json:"before"`
	BeforePresent bool   `json:"beforePresent"`
	After         any    `json:"after"`
	AfterPresent  bool   `json:"afterPresent"`
}
type Change struct {
	// Delete retains the reviewed configuration for drift checks and undo.
	Delete       bool           `json:"delete,omitempty"`
	BeforeObject map[string]any `json:"beforeObject,omitempty"`
	Ref          Ref            `json:"ref"`
	APIVersion   string         `json:"apiVersion"`
	UID          string         `json:"uid"`
	Fields       []FieldChange  `json:"fields"`
	Reason       string         `json:"reason"`
}
type EditTarget struct {
	Ref         Ref    `json:"ref"`
	Rule        int    `json:"rule"`
	UID         string `json:"uid,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}
type WaypointRequest struct {
	Mode          string   `json:"mode"`
	Scope         string   `json:"scope"`
	Namespace     string   `json:"namespace"`
	Name          string   `json:"name"`
	Class         string   `json:"class"`
	Namespaces    []string `json:"namespaces,omitempty"`
	GrantAccess   bool     `json:"grantAccess,omitempty"`
	ClearOverride bool     `json:"clearOverride,omitempty"`
	EnrolAmbient  bool     `json:"enrolAmbient,omitempty"`
}

func Copy(v any) any { b, _ := json.Marshal(v); var out any; _ = json.Unmarshal(b, &out); return out }
func Pointer(v any, path string) (any, bool) {
	if path == "" {
		return v, true
	}
	for _, raw := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		key := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		switch o := v.(type) {
		case map[string]any:
			var ok bool
			v, ok = o[key]
			if !ok {
				return nil, false
			}
		case []any:
			i, e := strconv.Atoi(key)
			if e != nil || i < 0 || i >= len(o) {
				return nil, false
			}
			v = o[i]
		default:
			return nil, false
		}
	}
	return v, true
}
func pointerKey(k string) string {
	return strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1")
}
func Equal(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// RestorableObject excludes server bookkeeping, but retains configuration,
// ownership and allocated Service addresses for exact review and undo.
func RestorableObject(o map[string]any) map[string]any {
	out := M(Copy(o))
	delete(out, "status")
	md := M(out["metadata"])
	for _, key := range []string{"uid", "resourceVersion", "generation", "creationTimestamp", "managedFields", "deletionTimestamp", "deletionGracePeriodSeconds", "selfLink"} {
		delete(md, key)
	}
	return out
}

func deleteChange(p *Plan, o *store.Object, reason string) {
	api := o.Version
	if o.Group != "" {
		api = o.Group + "/" + o.Version
	}
	before := RestorableObject(o.Obj)
	before["apiVersion"], before["kind"] = api, o.Kind
	p.Changes = append(p.Changes, Change{Ref: reference(o), APIVersion: api, UID: S(Path(o.Obj, "metadata", "uid")), Fields: []FieldChange{}, Reason: reason, Delete: true, BeforeObject: before})
}
func change(p *Plan, o *store.Object, reason string, values map[string]any, remove ...string) {
	c := Change{Ref: reference(o), APIVersion: o.Version, UID: S(Path(o.Obj, "metadata", "uid")), Reason: reason}
	if o.Group != "" {
		c.APIVersion = o.Group + "/" + o.Version
	}
	keys := []string{}
	for k := range values {
		keys = append(keys, k)
	}
	sortStrings(keys)
	for _, path := range keys {
		before, exists := Pointer(o.Obj, path)
		if exists && Equal(before, values[path]) {
			continue
		}
		c.Fields = append(c.Fields, FieldChange{path, Copy(before), exists, Copy(values[path]), true})
	}
	for _, path := range remove {
		before, exists := Pointer(o.Obj, path)
		if exists {
			c.Fields = append(c.Fields, FieldChange{path, Copy(before), true, nil, false})
		}
	}
	if len(c.Fields) > 0 {
		p.Changes = append(p.Changes, c)
	}
}
func sortStrings(xs []string) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j] < xs[j-1]; j-- {
			xs[j], xs[j-1] = xs[j-1], xs[j]
		}
	}
}
func finishPlan(p *Plan) (*Plan, error) {
	data, e := json.Marshal(map[string]any{"request": p.Request, "uid": p.ServiceUID, "objects": p.Objects, "changes": p.Changes})
	if e != nil {
		return nil, e
	}
	sum := sha256.Sum256(data)
	p.ID = hex.EncodeToString(sum[:])[:16]
	for _, o := range p.Objects {
		md := M(o["metadata"])
		lb := M(md["labels"])
		if lb == nil {
			lb = map[string]any{}
			md["labels"] = lb
		}
		lb["app.kubernetes.io/managed-by"] = "flightbay"
		lb["flightbay.dev/test-id"] = p.ID
	}
	data, _ = json.Marshal(p.Objects)
	if e = json.Unmarshal(data, &p.Objects); e != nil {
		return nil, e
	}
	p.YAML = ""
	appendYaml := func(o any) error {
		b, e := yaml.Marshal(o)
		if e != nil {
			return e
		}
		if p.YAML != "" {
			p.YAML += "---\n"
		}
		p.YAML += string(b)
		return nil
	}
	for _, o := range p.Objects {
		if e = appendYaml(o); e != nil {
			return nil, e
		}
	}
	for _, c := range p.Changes {
		if c.Delete {
			// Keep deletion review YAML non-applicable: kubectl apply must not
			// accidentally recreate the object the reader intended to remove.
			b, err := yaml.Marshal(c.BeforeObject)
			if err != nil {
				return nil, err
			}
			p.YAML += "# DELETE " + c.Ref.Kind + " " + c.Ref.Namespace + "/" + c.Ref.Name + ": " + c.Reason + "\n# Reviewed configuration (restored by Undo):\n# " + strings.ReplaceAll(strings.TrimSuffix(string(b), "\n"), "\n", "\n# ") + "\n"
			continue
		}
		snippet := map[string]any{"apiVersion": c.APIVersion, "kind": c.Ref.Kind, "metadata": map[string]any{"name": c.Ref.Name}}
		if c.Ref.Namespace != "" {
			M(snippet["metadata"])["namespace"] = c.Ref.Namespace
		}
		for _, f := range c.Fields {
			setSnippet(snippet, strings.Split(strings.TrimPrefix(f.Path, "/"), "/"), f.After)
		}
		p.YAML += "# Existing resource: scoped merge snippet. Flightbay applies only the reviewed fields with UID/resourceVersion checks.\n"
		if e = appendYaml(snippet); e != nil {
			return nil, e
		}
	}
	if len(p.Objects) == 0 && len(p.Changes) == 0 {
		p.YAML = "# No configuration change is needed for the chosen defaults.\n"
		p.Notes = append(p.Notes, "No resources or fields will be changed; there is nothing to apply.")
	}
	if len(p.Changes) > 0 {
		p.Notes = append(p.Notes, "Existing-resource changes use UID and resourceVersion preconditions. Undo restores unchanged fields and recreates deleted resources only if their names are free.")
	}
	return p, nil
}
func setSnippet(root map[string]any, parts []string, value any) {
	if len(parts) == 0 {
		return
	}
	key := strings.ReplaceAll(strings.ReplaceAll(parts[0], "~1", "/"), "~0", "~")
	if len(parts) == 1 {
		root[key] = value
		return
	}
	next := M(root[key])
	if next == nil {
		next = map[string]any{}
		root[key] = next
	}
	setSnippet(next, parts[1:], value)
}
func basePlan(req Request, objects ...*store.Object) *Plan {
	p := &Plan{Request: req, Objects: []map[string]any{}, Notes: []string{"Generated from captured evidence. Validate the exact preview against an explicitly selected real context before applying."}, Evidence: []Evidence{}}
	for _, o := range objects {
		if o != nil {
			p.Evidence = append(p.Evidence, evidence(o, "metadata.uid", Path(o.Obj, "metadata", "uid")))
		}
	}
	return p
}
func Target(ws *store.Workspace, c, kind, ns, name string) *store.Object {
	g, res := "", ""
	switch kind {
	case "Namespace":
		res = "namespaces"
		ns = ""
	case "Service":
		res = "services"
	case "Pod":
		res = "pods"
	case "Gateway":
		g, res = GatewayGroup, "gateways"
	case "HTTPRoute":
		g, res = GatewayGroup, "httproutes"
	case "ServiceEntry":
		g, res = "networking.istio.io", "serviceentries"
	case "Deployment":
		g, res = "apps", "deployments"
	case "AuthorizationPolicy":
		g, res = "security.istio.io", "authorizationpolicies"
	case "RequestAuthentication":
		g, res = "security.istio.io", "requestauthentications"
	case "PeerAuthentication":
		g, res = "security.istio.io", "peerauthentications"
	case "DestinationRule":
		g, res = "networking.istio.io", "destinationrules"
	case "EnvoyFilter":
		g, res = "networking.istio.io", "envoyfilters"
	case "AgentgatewayPolicy":
		g, res = "agentgateway.dev", "agentgatewaypolicies"
	case "EnterpriseAgentgatewayPolicy":
		g, res = "enterpriseagentgateway.solo.io", "enterpriseagentgatewaypolicies"
	default:
		return nil
	}
	return ws.Get(c, g, res, ns, name)
}
func generateRepair(ws *store.Workspace, req Request) (*Plan, error) {
	if len(req.Tasks) != 1 {
		return nil, fmt.Errorf("review one remediation at a time")
	}
	svc := Target(ws, req.Cluster, "Service", req.Namespace, req.Service)
	if svc == nil {
		return nil, fmt.Errorf("select the affected Service")
	}
	p := basePlan(req, svc)
	p.ServiceUID = S(Path(svc.Obj, "metadata", "uid"))
	switch req.Tasks[0] {
	case "service-selector":
		pod := Target(ws, req.Cluster, "Pod", req.Namespace, req.SelectedPod)
		if pod == nil || len(req.Selector) == 0 {
			return nil, fmt.Errorf("choose the intended captured Pod and a non-empty selector")
		}
		if !matches(M(Copy(req.Selector)), pod.Labels()) {
			return nil, fmt.Errorf("selector must match the chosen Pod")
		}
		for k := range req.Selector {
			if k == "pod-template-hash" || k == "controller-revision-hash" {
				return nil, fmt.Errorf("use stable workload labels instead of rollout-specific label %s", k)
			}
		}
		change(p, svc, "Retarget the Service to the explicitly chosen workload", map[string]any{"/spec/selector": req.Selector})
		p.Notes = append(p.Notes, "This retargets Service traffic; it does not repair an unhealthy workload. Verify the selected Pod is the intended application.")
	case "scale-workload":
		d := Target(ws, req.Cluster, "Deployment", req.Namespace, req.Workload)
		if d == nil || req.Replicas < 1 || req.Replicas > 20 {
			return nil, fmt.Errorf("choose a Deployment and replica count 1–20")
		}
		if !matches(M(Path(svc.Obj, "spec", "selector")), stringMap(M(Path(d.Obj, "spec", "template", "metadata", "labels")))) || len(M(Path(svc.Obj, "spec", "selector"))) == 0 {
			return nil, fmt.Errorf("Deployment template must match this Service selector")
		}
		change(p, d, "Scale the selected Deployment", map[string]any{"/spec/replicas": req.Replicas})
		p.Notes = append(p.Notes, "Scaling creates real Pods and may consume resources; it does not fix image, readiness or scheduling errors.")
	case "service-port":
		pod := Target(ws, req.Cluster, "Pod", req.Namespace, req.SelectedPod)
		if pod == nil || !matches(M(Path(svc.Obj, "spec", "selector")), pod.Labels()) {
			return nil, fmt.Errorf("choose a Pod selected by this Service")
		}
		found := false
		for _, ct := range L(Path(pod.Obj, "spec", "containers")) {
			for _, v := range L(M(ct)["ports"]) {
				if N(M(v)["containerPort"]) == req.TargetPort {
					found = true
				}
			}
		}
		if !found {
			return nil, fmt.Errorf("choose a declared container port on that Pod")
		}
		ports := L(Copy(Path(svc.Obj, "spec", "ports")))
		changed := false
		for _, v := range ports {
			if N(M(v)["port"]) == req.Port {
				M(v)["targetPort"] = req.TargetPort
				changed = true
			}
		}
		if !changed {
			return nil, fmt.Errorf("choose an existing Service port")
		}
		change(p, svc, "Use the selected workload's declared container port", map[string]any{"/spec/ports": ports})
	case "route-parent":
		routeNS := req.Namespace
		if req.Edit != nil {
			if req.Edit.Ref.Cluster != req.Cluster || req.Edit.Ref.Kind != "HTTPRoute" {
				return nil, fmt.Errorf("choose an HTTPRoute in the selected cluster")
			}
			routeNS = req.Edit.Ref.Namespace
		}
		route := Target(ws, req.Cluster, "HTTPRoute", routeNS, req.Route)
		if route == nil || (req.Parent == nil && !hasPort(svc, req.Port)) {
			return nil, fmt.Errorf("choose the affected HTTPRoute and frontend Service port")
		}
		parents := L(Copy(Path(route.Obj, "spec", "parentRefs")))
		index := 0
		if req.Edit != nil {
			index = req.Edit.Rule
		}
		if len(parents) == 0 {
			parents = []any{map[string]any{}}
			index = 0
		}
		if index < 0 || index >= len(parents) {
			return nil, fmt.Errorf("parent index is out of range")
		}
		entry := map[string]any{"group": "", "kind": "Service", "name": req.Service, "port": req.Port}
		if route.Namespace != svc.Namespace {
			entry["namespace"] = svc.Namespace
		}
		reason := "Attach this parent to the explicitly selected frontend Service"
		if req.Parent != nil {
			ref := req.Parent
			if ref.Cluster != req.Cluster || ref.Kind != "Gateway" || ref.Group != GatewayGroup || ref.Resource != "gateways" {
				return nil, fmt.Errorf("choose a Gateway in the selected cluster")
			}
			gw := Target(ws, req.Cluster, "Gateway", ref.Namespace, ref.Name)
			if gw == nil {
				return nil, fmt.Errorf("chosen Gateway was not captured in this cluster")
			}
			entry = map[string]any{"group": GatewayGroup, "kind": "Gateway", "name": ref.Name, "namespace": ref.Namespace}
			if req.ParentSection != "" {
				found := false
				for _, l := range L(Path(gw.Obj, "spec", "listeners")) {
					if S(M(l)["name"]) == req.ParentSection {
						found = true
					}
				}
				if !found {
					return nil, fmt.Errorf("chosen listener does not exist on this Gateway")
				}
				entry["sectionName"] = req.ParentSection
			}
			reason = "Attach this parent to the explicitly selected Gateway"
		}
		parents[index] = entry
		change(p, route, reason, map[string]any{"/spec/parentRefs": parents})
		p.Notes = append(p.Notes, "Other parent references, route rules and backend selections are preserved. Check listener eligibility and current per-parent Accepted/ResolvedRefs status after applying; selecting a Gateway does not prove it will accept the route.")
	default:
		return nil, fmt.Errorf("unknown remediation")
	}
	return finishPlan(p)
}
