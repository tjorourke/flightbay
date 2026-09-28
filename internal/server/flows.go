package server

import (
	"net/http"
	"strconv"

	"github.com/tjorourke/flightbay/internal/flows"
)

// flowsList serves GET /api/flows?cluster=&source=&limit=: observed traffic
// from every source (ztunnel, agentgateway, Envoy, CoreDNS and imported flow
// logs), largest first. Not an Enterprise feature: it is the raw evidence.
func (s *Server) flowsList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = 500
	}
	fs := s.WS.FlowsFrom(q.Get("cluster"), q.Get("source"))
	flows.Sort(fs)
	bySource := map[string]int{}
	records := 0
	for _, f := range fs {
		bySource[f.Source] += f.Count
		records += f.Count
	}
	out := map[string]any{"total": len(fs), "records": records, "bySource": bySource, "truncated": len(fs) > limit}
	if len(fs) > limit {
		fs = fs[:limit]
	}
	if fs == nil {
		fs = []flows.Flow{}
	}
	out["flows"] = fs
	writeJSON(w, 200, out)
}
