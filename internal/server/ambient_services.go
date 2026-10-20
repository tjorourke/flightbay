package server

import (
	"net/http"

	"github.com/tjorourke/flightbay/internal/ambient"
)

// ambientServices serves the Ambient Overview: summary cards, the merged
// Services table, the needs-attention list and the namespace footer.
func (s *Server) ambientServices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jerr(w, 405, "GET only")
		return
	}
	writeJSON(w, 200, ambient.Overview(s.WS, r.URL.Query().Get("namespace")))
}
