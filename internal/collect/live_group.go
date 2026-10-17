package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/store"
)

// LiveGroup coordinates independent readers into a cluster-keyed workspace.
// One unreachable cluster never cancels the others. Failed discovery is retried.
type LiveGroup struct {
	Readers    []*Live
	RetryDelay time.Duration
	mu         sync.Mutex
	states     map[string]store.LiveClusterState
}

func (g *LiveGroup) update(phase string, state *store.LiveState, message string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	previous := g.states[state.Context]
	if previous.Phase != phase || previous.Error != message {
		coverage := bundle.Coverage{ID: bundle.SafeID(state.Context) + ".live.initial-sync", Cluster: bundle.SafeID(state.Context), Type: bundle.CoverageDiscovery, Resource: "initial live sync", State: bundle.StateComplete}
		if phase == store.PhaseFailed {
			coverage.State = bundle.StateFailed
			coverage.Reason = message
		} else if phase != store.PhaseLive {
			coverage.State = bundle.StateTruncated
			coverage.Reason = "initial sync has not completed"
		}
		g.Readers[0].WS.PutCoverage(coverage)
	}
	g.states[state.Context] = store.LiveClusterState{LiveState: *state, Phase: phase, Error: message}
	agg := &store.LiveState{Live: true, Connected: true, Requests: map[string]int64{}}
	loading, ready := false, false
	for _, reader := range g.Readers {
		s := g.states[reader.O.Context]
		agg.Clusters = append(agg.Clusters, s)
		agg.Connected = agg.Connected && s.Connected && s.Phase == store.PhaseLive
		loading = loading || s.Phase == store.PhaseLoading
		ready = ready || s.Phase == store.PhaseLive
		agg.ObjectsWatched += s.ObjectsWatched
		agg.Watches += s.Watches
		agg.LogStreams += s.LogStreams
		agg.LogStreamsTotal += s.LogStreamsTotal
		agg.LogLinesDropped += s.LogLinesDropped
		agg.MaxLogStreams += s.MaxLogStreams
		agg.Reconnects += s.Reconnects
		agg.RetryingStreams += s.RetryingStreams
		agg.Refused += s.Refused
		agg.Diagnostics = agg.Diagnostics || s.Diagnostics
		if s.LastEvent.After(agg.LastEvent) {
			agg.LastEvent = s.LastEvent
		}
		for verb, count := range s.Requests {
			agg.Requests[verb] += count
		}
	}
	contexts := []string{}
	for _, r := range g.Readers {
		contexts = append(contexts, r.O.Context)
	}
	agg.Context = strings.Join(contexts, ", ")
	agg.MaxLogLines = g.Readers[0].O.MaxLogLines
	if len(g.Readers) == 1 {
		agg.Server = g.Readers[0].O.Server
	}
	g.Readers[0].WS.SetState(func(s *store.ImportState) {
		s.Live = agg
		s.Error = ""
		if ready {
			s.Phase = store.PhaseLive
		} else if loading {
			s.Phase = store.PhaseLoading
		} else {
			s.Phase = store.PhaseFailed
			s.Error = "No selected cluster is connected; discovery will retry"
		}
		if !loading && s.Finished.IsZero() {
			s.Finished = time.Now().UTC()
		}
	})
}

func (g *LiveGroup) Run(ctx context.Context) error {
	if len(g.Readers) == 0 {
		return fmt.Errorf("select at least one cluster")
	}
	if g.RetryDelay <= 0 {
		g.RetryDelay = 10 * time.Second
	}
	g.states = map[string]store.LiveClusterState{}
	contexts, seen := []string{}, map[string]bool{}
	for _, r := range g.Readers {
		if seen[r.Cluster()] {
			return fmt.Errorf("contexts map to duplicate workspace cluster %q", r.Cluster())
		}
		seen[r.Cluster()] = true
		contexts = append(contexts, r.O.Context)
		r.O.defaults()
		g.states[r.O.Context] = store.LiveClusterState{LiveState: store.LiveState{Live: true, Context: r.O.Context, Server: r.O.Server}, Phase: store.PhaseLoading}
		r.O.State = g.update
	}
	first := g.Readers[0]
	p := first.C.Policy
	pol, _ := json.Marshal(p)
	planned := []string{}
	for _, category := range p.Plan().Categories {
		planned = append(planned, string(category))
	}
	start := time.Now().UTC()
	first.WS.SetHeader(&bundle.Header{Case: p.Case, Collector: "flightbay-desk live " + first.O.Version, CaptureStart: start, Contexts: contexts, Policy: pol, Planned: planned, Unavailable: p.Plan().Unavailable, Source: store.SourceLive})
	first.WS.SetState(func(s *store.ImportState) {
		s.Phase = store.PhaseLoading
		s.Source = "live:" + strings.Join(contexts, ",")
		s.Started = start
	})
	g.update(store.PhaseLoading, &store.LiveState{Live: true, Context: first.O.Context, Server: first.O.Server}, "")
	var wg sync.WaitGroup
	for _, r := range g.Readers {
		wg.Add(1)
		go func(r *Live) {
			defer wg.Done()
			for ctx.Err() == nil {
				err := r.Run(ctx)
				if ctx.Err() != nil {
					return
				}
				if err != nil {
					r.reconnects.Add(1)
					r.progress("discovery failed; retrying in %s: %v", g.RetryDelay, err)
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(g.RetryDelay):
				}
			}
		}(r)
	}
	wg.Wait()
	return nil
}

func (g *LiveGroup) DiagnosticsEnabled() bool {
	for _, r := range g.Readers {
		if r.DiagnosticsEnabled() {
			return true
		}
	}
	return false
}
func (g *LiveGroup) RefreshDiagnostics(ctx context.Context, cluster, ns, pod string) (int, error) {
	if cluster == "" && len(g.Readers) > 1 {
		return 0, fmt.Errorf("select a cluster for diagnostics")
	}
	for _, r := range g.Readers {
		if cluster == "" || cluster == r.Cluster() {
			g.mu.Lock()
			state := g.states[r.O.Context]
			g.mu.Unlock()
			if state.Phase != store.PhaseLive {
				return 0, fmt.Errorf("cluster %s has not completed initial sync", r.O.Context)
			}
			return r.RefreshDiagnostics(ctx, ns, pod)
		}
	}
	return 0, fmt.Errorf("cluster %q is not selected", cluster)
}
