package server

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// liveSave writes the live mirror as a normal, verified bundle: the same
// thing flightbay collect would have produced at this moment, without asking
// the cluster again.
func (s *Server) liveSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jerr(w, http.StatusMethodNotAllowed, "POST to save the live workspace as a bundle")
		return
	}
	if !sameOrigin(r) {
		jerr(w, http.StatusForbidden, "cross-origin request refused")
		return
	}
	if s.SaveBundle == nil || !s.WS.IsLive() {
		jerr(w, http.StatusConflict, "only a live workspace can be saved as a bundle; this one was opened from a bundle already")
		return
	}
	start := time.Now()
	path, err := s.SaveBundle()
	if err != nil {
		jerr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"path": path, "ms": time.Since(start).Milliseconds()})
}

// liveRefreshDiagnostics re-reads one component's admin endpoints over
// port-forward (POST ?pod=namespace/name) and replaces its captured files.
func (s *Server) liveRefreshDiagnostics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jerr(w, http.StatusMethodNotAllowed, "POST ?pod=namespace/name to refresh that pod's diagnostics")
		return
	}
	if !sameOrigin(r) {
		jerr(w, http.StatusForbidden, "cross-origin request refused")
		return
	}
	if s.RefreshDiagnostics == nil || !s.WS.IsLive() {
		jerr(w, http.StatusConflict, "diagnostics refresh needs flightbay-desk live --diagnostics")
		return
	}
	ns, pod, ok := strings.Cut(r.URL.Query().Get("pod"), "/")
	if !ok || ns == "" || pod == "" {
		jerr(w, http.StatusBadRequest, "pod must be namespace/name")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	n, err := s.RefreshDiagnostics(ctx, ns, pod)
	if err != nil {
		jerr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"pod": ns + "/" + pod, "files": n})
}
