package server

import (
	"net/http"
	"strings"
	"sync"

	"github.com/tjorourke/flightbay/internal/features"
	"github.com/tjorourke/flightbay/internal/reach"
)

type reachCache struct {
	mu  sync.Mutex
	rev int64
	rep *reach.Report
}

func (s *Server) reachReport() reach.Report {
	s.reachC.mu.Lock()
	defer s.reachC.mu.Unlock()
	rev := s.WS.Revision()
	if s.reachC.rep == nil || s.reachC.rev != rev {
		r := reach.Evaluate(s.WS)
		s.reachC.rep, s.reachC.rev = &r, rev
	}
	return *s.reachC.rep
}

// reach serves the agent reach report (Flightbay Enterprise).
//
//	/api/reach               every agent, without the per-destination detail
//	/api/reach?agent=ID      one agent in full (ID, ns/name or name)
//	/api/reach?full=1        everything
func (s *Server) reach(w http.ResponseWriter, r *http.Request) {
	if !features.Enabled(features.Reach) {
		jerr(w, 403, "agent reach is a Flightbay Enterprise feature; start the desk with FLIGHTBAY_ENTERPRISE=1")
		return
	}
	rep := s.reachReport()
	q := r.URL.Query()
	if id := q.Get("agent"); id != "" {
		for _, a := range rep.Agents {
			if a.ID == id || a.Namespace+"/"+a.Name == id || a.Name == id || strings.HasSuffix(a.ID, "/"+id) {
				writeJSON(w, 200, a)
				return
			}
		}
		jerr(w, 404, "no agent "+id)
		return
	}
	if q.Get("full") == "" {
		slim := rep
		slim.Agents = make([]reach.Agent, len(rep.Agents))
		for i, a := range rep.Agents {
			a.Direct, a.Nodes, a.Edges, a.External = nil, nil, nil, nil
			// Copy before blanking: the slice is shared with the cached report,
			// and ?agent= must still serve the fix YAML afterwards.
			a.Findings = append([]reach.Finding(nil), a.Findings...)
			for j := range a.Findings {
				a.Findings[j].Fix, a.Findings[j].Advice, a.Findings[j].Items = "", nil, nil
			}
			slim.Agents[i] = a
		}
		writeJSON(w, 200, slim)
		return
	}
	writeJSON(w, 200, rep)
}
