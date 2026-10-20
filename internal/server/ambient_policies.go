package server

import (
	"net/http"
	"strings"

	"github.com/tjorourke/flightbay/internal/ambient"
)

// ambientPolicies serves Policies & routes: every ambient policy, route and
// waypoint with what it applies to and how each cluster compares.
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
