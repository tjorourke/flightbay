package server

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"

	"github.com/tjorourke/flightbay/internal/features"
	"github.com/tjorourke/flightbay/internal/reach"
)

// actCache keeps the last activity report; the key is the workspace
// revision, the number of log lines loaded and the baseline, since log lines
// arrive after the objects when a bundle opens.
type actCache struct {
	mu  sync.Mutex
	key string
	rep *reach.Activity
}

func (s *Server) activityReport() reach.Activity {
	b := s.baseline()
	b.mu.RLock()
	bws, bpath := b.ws, b.path
	b.mu.RUnlock()
	st := s.WS.State()
	k := fmt.Sprintf("%d/%d/%s", s.WS.Revision(), st.LogLines, bpath)
	s.actC.mu.Lock()
	defer s.actC.mu.Unlock()
	if s.actC.rep == nil || s.actC.key != k {
		name := ""
		if bpath != "" {
			name = filepath.Base(bpath)
		}
		r := reach.EvaluateActivity(s.WS, bws, name)
		s.actC.rep, s.actC.key = &r, k
	}
	return *s.actC.rep
}

// activity serves the agent and tool activity report (Flightbay Enterprise).
func (s *Server) activity(w http.ResponseWriter, r *http.Request) {
	if !features.Enabled(features.Activity) {
		jerr(w, 403, "agent and tool activity is a Flightbay Enterprise feature; start the desk with FLIGHTBAY_ENTERPRISE=1")
		return
	}
	writeJSON(w, 200, s.activityReport())
}

type trafCache struct {
	mu  sync.Mutex
	key string
	rep *reach.Traffic
}

func (s *Server) trafficReport() reach.Traffic {
	st := s.WS.State()
	k := fmt.Sprintf("%d/%d", s.WS.Revision(), st.LogLines)
	s.trafC.mu.Lock()
	defer s.trafC.mu.Unlock()
	if s.trafC.rep == nil || s.trafC.key != k {
		r := reach.EvaluateTraffic(s.WS)
		s.trafC.rep, s.trafC.key = &r, k
	}
	return *s.trafC.rep
}

// traffic serves the ambient traffic map (Flightbay Enterprise).
func (s *Server) traffic(w http.ResponseWriter, r *http.Request) {
	if !features.Enabled(features.Traffic) {
		jerr(w, 403, "the traffic map is a Flightbay Enterprise feature; start the desk with FLIGHTBAY_ENTERPRISE=1")
		return
	}
	writeJSON(w, 200, s.trafficReport())
}

// explain answers "why was this allowed or denied?" for one pair.
//
//	/api/explain?from=ns/workload&to=ns/service[&cluster=c]
func (s *Server) explain(w http.ResponseWriter, r *http.Request) {
	if !features.Enabled(features.Traffic) {
		jerr(w, 403, "the explainer is a Flightbay Enterprise feature; start the desk with FLIGHTBAY_ENTERPRISE=1")
		return
	}
	q := r.URL.Query()
	from, to := q.Get("from"), q.Get("to")
	if from == "" || to == "" {
		jerr(w, 400, "give from (a workload, ns/name) and to (a Service, ns/name)")
		return
	}
	x, err := reach.Explain(s.WS, q.Get("cluster"), from, to)
	if err != nil {
		var ee *reach.ExplainError
		if errors.As(err, &ee) {
			writeJSON(w, 404, ee)
			return
		}
		jerr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, x)
}
