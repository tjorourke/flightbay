package server

import (
	"net/http"
	"strings"

	"github.com/tjorourke/flightbay/internal/resmap"
)

func (s *Server) resourceMap(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	cluster := q.Get("cluster")
	if cluster == "" {
		jerr(w, 400, "cluster is required")
		return
	}
	ns := q.Get("namespace")
	var kinds []string
	if ks := q.Get("kinds"); ks != "" {
		kinds = strings.Split(ks, ",")
	}
	writeJSON(w, 200, resmap.Build(s.WS, cluster, ns, kinds))
}
