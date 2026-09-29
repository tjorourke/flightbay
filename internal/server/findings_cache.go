package server

import (
	"sync"
	"time"

	"github.com/tjorourke/flightbay/internal/analyze"
	"github.com/tjorourke/flightbay/internal/store"
)

// findingsCache keeps the analyzer's findings for a workspace revision. The
// repeated-error rule samples up to 200,000 error lines, which takes seconds
// on a large bundle, and the Overview, Findings and workspace summary all ask
// for the same list. While a bundle is loading the revision changes with
// every chunk, so a result is reused for a few seconds instead.
type findingsCache struct {
	mu  sync.Mutex
	rev int64
	at  time.Time
	out []analyze.Finding
}

const findingsReuseWhileLoading = 3 * time.Second

// findingsList returns the findings, computing them at most once per
// revision (and at most every few seconds while loading). Callers must not
// modify the slice.
func (s *Server) findingsList() []analyze.Finding {
	c := &s.findC
	c.mu.Lock()
	defer c.mu.Unlock()
	rev := s.WS.Revision()
	if c.out != nil {
		if c.rev == rev {
			return c.out
		}
		if ph := s.WS.State().Phase; (ph == store.PhaseLoading || ph == store.PhaseWaiting) && time.Since(c.at) < findingsReuseWhileLoading {
			return c.out
		}
	}
	out := analyze.Run(s.WS)
	if out == nil {
		out = []analyze.Finding{}
	}
	c.rev, c.at, c.out = rev, time.Now(), out
	return out
}
