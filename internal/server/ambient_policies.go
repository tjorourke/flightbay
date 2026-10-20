package server

import (
	"net/http"
	"strings"

	"github.com/tjorourke/flightbay/internal/ambient"
)

// ambientPolicies serves Policies & routes: every ambient policy and route
// with what it applies to and how each cluster compares. Waypoint Gateways
// are only counted (counts.waypoints).
func (s *Server) ambientPolicies(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jerr(w, 405, "GET only")
		return
	}
	q := r.URL.Query()
	list := func(k string) []string {
		var out []string
		for _, v := range q[k] {
			for _, p := range strings.Split(v, ",") {
				if p = strings.TrimSpace(p); p != "" {
					out = append(out, p)
				}
			}
		}
		return out
	}
	on := func(k string) bool {
		switch strings.ToLower(q.Get(k)) {
		case "1", "true", "yes", "on":
			return true
		}
		return false
	}
	group := q.Get("group")
	switch group {
	case "":
		group = "object"
	case "object", "service":
	default:
		jerr(w, 400, "group must be object or service")
		return
	}
	writeJSON(w, 200, ambient.NewPolicies(s.WS, ambient.PolicyFilter{
		Namespaces: list("namespace"), Types: list("type"), ManagedBy: list("managedBy"),
		Problems: on("problems"), Drift: on("drift"), Group: group, Q: q.Get("q"),
	}))
}

// ambientPolicyDiff serves the field diff of one inventory object between
// the connected clusters: GET ?kind=&namespace=&name=.
func (s *Server) ambientPolicyDiff(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jerr(w, 405, "GET only")
		return
	}
	q := r.URL.Query()
	kind, ns, name := q.Get("kind"), q.Get("namespace"), q.Get("name")
	if kind == "" || name == "" {
		jerr(w, 400, "kind and name are required")
		return
	}
	if !ambient.PolicyKindKnown(kind) {
		jerr(w, 400, "kind is not a policy or route kind: "+kind)
		return
	}
	out, ok := ambient.PolicyFieldDiff(s.WS, kind, ns, name)
	if !ok {
		jerr(w, 404, kind+" "+ns+"/"+name+" is not on any connected cluster")
		return
	}
	writeJSON(w, 200, out)
}
