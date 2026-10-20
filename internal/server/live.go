package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

func (s *Server) liveIngestion(w http.ResponseWriter, r *http.Request) {
	if !probePost(w, r) {
		return
	}
	if !s.WS.IsLive() || s.WS.State().Phase != "live" {
		jerr(w, http.StatusConflict, "ingestion can be paused after the live workspace has completed initial sync")
		return
	}
	var q struct {
		Paused *bool `json:"paused"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&q) != nil || q.Paused == nil {
		jerr(w, http.StatusBadRequest, "provide paused: true or false")
		return
	}
	s.WS.SetLivePaused(*q.Paused)
	writeJSON(w, http.StatusOK, s.WS.State())
}

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
	if (s.RefreshDiagnostics == nil && s.RefreshClusterDiagnostics == nil) || !s.WS.IsLive() {
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
	var n int
	var err error
	if s.RefreshClusterDiagnostics != nil {
		n, err = s.RefreshClusterDiagnostics(ctx, r.URL.Query().Get("cluster"), ns, pod)
	} else {
		n, err = s.RefreshDiagnostics(ctx, ns, pod)
	}
	if err != nil {
		jerr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"pod": ns + "/" + pod, "files": n})
}
