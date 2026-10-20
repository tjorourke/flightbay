package server

import (
	"net/http"

	"github.com/tjorourke/flightbay/internal/ambient"
)

// ambientServices serves the Ambient Overview: summary cards, the merged
// Services table, the needs-attention list (one entry per Service) and the
// namespace list. The optional namespace query is a comma-separated list that
// filters the Services table and Needs attention.
func (s *Server) ambientServices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jerr(w, 405, "GET only")
		return
	}
	writeJSON(w, 200, ambient.Overview(s.WS, r.URL.Query().Get("namespace")))
}
