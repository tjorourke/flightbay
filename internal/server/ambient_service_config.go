package server

import (
	"net/http"

	"github.com/tjorourke/flightbay/internal/ambient"
)

// ambientServiceConfig serves the Configuration tab: answers, tasks with their
// resources and status, and how the other connected clusters compare.
func (s *Server) ambientServiceConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jerr(w, 405, "GET only")
		return
	}
	q := r.URL.Query()
	if q.Get("name") == "" {
		jerr(w, 400, "name is required")
		return
	}
	out, err := ambient.NewServiceConfig(s.WS, q.Get("cluster"), q.Get("namespace"), q.Get("name"))
	if err != nil {
		jerr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, out)
}
