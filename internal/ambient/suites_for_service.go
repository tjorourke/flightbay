package ambient

import (
	"fmt"
	"sort"

	"github.com/tjorourke/flightbay/internal/store"
)

// Generated is a fresh suite with the inputs it was built from, so a caller
// can also explain the object without rebuilding them.
type Generated struct {
	Suite *Suite
	Authz *AuthzInput
	Route *RouteInput
}

// GenerateForObject builds the suite for one raw object (captured or a plan
// draft). svc is the Service to test against; nil lets the object decide.
func GenerateForObject(ws *store.Workspace, cluster string, obj map[string]any, svc *store.Object, drafts []map[string]any) (*Generated, error) {
	switch S(obj["kind"]) {
	case KindAuthorizationPolicy:
		spec := M(obj["spec"])
		if svc == nil {
			ns := S(M(obj["metadata"])["namespace"])
			svc = ServiceForPolicy(ws, cluster, ns, spec)
		}
		in, err := AuthzInputFor(ws, cluster, obj, svc, drafts)
		if err != nil {
			return nil, err
		}
		return &Generated{Suite: GenerateAuthzSuite(*in), Authz: in}, nil
	case KindHTTPRoute:
		in, err := RouteInputFor(ws, cluster, obj, drafts)
		if err != nil {
			return nil, err
		}
		return &Generated{Suite: GenerateRouteSuite(*in), Route: in}, nil
	}
	return nil, fmt.Errorf("tests exist for AuthorizationPolicy and HTTPRoute, not %q", S(obj["kind"]))
}

// GenerateForRef does the same for an object in the capture.
func GenerateForRef(ws *store.Workspace, ref Ref, svc *store.Object) (*Generated, error) {
	o := ws.Get(ref.Cluster, ref.Group, ref.Resource, ref.Namespace, ref.Name)
	if o == nil {
		return nil, fmt.Errorf("%s %s/%s is not in the capture of %s", ref.Kind, ref.Namespace, ref.Name, ref.Cluster)
	}
	obj := o.Obj
	if S(obj["kind"]) == "" {
		cp := map[string]any{}
		for k, v := range obj {
			cp[k] = v
		}
		cp["kind"] = ref.Kind
		obj = cp
	}
	return GenerateForObject(ws, ref.Cluster, obj, svc, nil)
}

// ServiceSuite is one row of the Service's Tests tab.
type ServiceSuite struct {
	Ref    Ref    `json:"ref"`
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Action string `json:"action,omitempty"`
	Saved  bool   `json:"saved"`
	Cases  int    `json:"cases"`
	// LastRun is nil when the suite has never run.
	LastRun *RunSummary `json:"lastRun,omitempty"`
	// Status is pass, fail, or "" when never run.
	Status string `json:"status,omitempty"`
}

// ServiceSuites lists every suite for objects that affect a Service.
type ServiceSuites struct {
	Service Ref            `json:"service"`
	Suites  []ServiceSuite `json:"suites"`
	// Cases counts every case across the suites; Passed and Failed count
	// the latest results (Failed includes errors).
	Cases  int `json:"cases"`
	Passed int `json:"passed"`
	Failed int `json:"failed"`
	// NeverRun counts suites without a result.
	NeverRun int `json:"neverRun"`
}

func suiteRefs(ws *store.Workspace, svc *store.Object) []Ref {
	seen := map[string]bool{}
	out := []Ref{}
	add := func(r Ref) {
		if r.Kind != KindAuthorizationPolicy && r.Kind != KindHTTPRoute {
			return
		}
		if (r.Kind == KindAuthorizationPolicy && r.Group != SecurityGroup) || (r.Kind == KindHTTPRoute && r.Group != GatewayGroup) {
			return
		}
		if k := r.SuiteString(); !seen[k] {
			seen[k] = true
			out = append(out, r)
		}
	}
	for _, e := range servicePolicies(ws, svc) {
		add(e.Ref)
	}
	for _, e := range editableConfigurations(ws, svc) {
		add(e.Ref)
	}
	wn, wp, _ := Waypoint(ws, svc)
	for _, o := range ws.List(svc.Cluster, GatewayGroup, "httproutes", svc.Namespace, nil) {
		if related(o, svc, wn, wp) {
			add(reference(o))
		}
	}
	return out
}

// ServiceSuiteList reads saved suites from dir and generates the rest on the
// fly (flagged saved:false) for every AuthorizationPolicy and HTTPRoute that
// affects the Service.
func ServiceSuiteList(ws *store.Workspace, dir, cluster, ns, name string) (*ServiceSuites, error) {
	svc := ws.Get(cluster, "", "services", ns, name)
	if svc == nil {
		return nil, fmt.Errorf("Service %s/%s is not in the capture of %s", ns, name, cluster)
	}
	out := &ServiceSuites{Service: reference(svc), Suites: []ServiceSuite{}}
	for _, ref := range suiteRefs(ws, svc) {
		var suite *Suite
		if dir != "" {
			s, err := LoadSuite(dir, ref)
			if err != nil {
				return nil, err
			}
			suite = s
		}
		var action string
		if o := ws.Get(ref.Cluster, ref.Group, ref.Resource, ref.Namespace, ref.Name); o != nil && ref.Kind == KindAuthorizationPolicy {
			action = defaultString(S(Path(o.Obj, "spec", "action")), "ALLOW")
		}
		if suite == nil {
			g, err := GenerateForRef(ws, ref, svc)
			if err != nil {
				// A route on the waypoint rather than this Service has no
				// Service parent to send requests to: leave it out.
				continue
			}
			suite = g.Suite
		}
		row := ServiceSuite{Ref: ref, Kind: ref.Kind, Name: ref.Name, Action: action, Saved: suite.Saved, Cases: len(suite.Cases)}
		if suite.Last != nil {
			sum := suite.Last.Summary
			row.LastRun = &sum
			row.Status = "pass"
			if !sum.OK() {
				row.Status = "fail"
			}
			out.Passed += sum.Passed
			out.Failed += sum.Failed + sum.Errored
		} else {
			out.NeverRun++
		}
		out.Cases += row.Cases
		out.Suites = append(out.Suites, row)
	}
	sort.SliceStable(out.Suites, func(i, j int) bool {
		a, b := out.Suites[i], out.Suites[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Name < b.Name
	})
	return out, nil
}
