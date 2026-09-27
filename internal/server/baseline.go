package server

import (
	"net/http"
	"sync"

	"github.com/tjorourke/flightbay/internal/diff"
	"github.com/tjorourke/flightbay/internal/store"
)

type baselineState struct {
	mu   sync.RWMutex
	ws   *store.Workspace
	path string
}

func (s *Server) baseline() *baselineState { return &s.base }

// SetBaseline attaches an earlier bundle to compare the workspace against.
func (s *Server) SetBaseline(ws *store.Workspace, path string) {
	b := s.baseline()
	b.mu.Lock()
	b.ws, b.path = ws, path
	b.mu.Unlock()
}

func (s *Server) baselineDiff(w http.ResponseWriter, _ *http.Request) {
	b := s.baseline()
	b.mu.RLock()
	bws, path := b.ws, b.path
	b.mu.RUnlock()
	if bws == nil {
		writeJSON(w, 200, map[string]any{"available": false})
		return
	}
	r := diff.Compare(bws, s.WS)
	writeJSON(w, 200, map[string]any{"available": true, "path": path, "report": r, "loading": s.WS.State().Phase == store.PhaseLoading})
}
