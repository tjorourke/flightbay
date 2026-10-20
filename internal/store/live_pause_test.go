package store

import (
	"context"
	"testing"
	"time"
)

func TestPauseBlocksIngestionButKeepsReadsAndCancellationWorking(t *testing.T) {
	w := New()
	defer w.Close()
	w.SetState(func(s *ImportState) { s.Live = &LiveState{Live: true} })
	release, ok := w.BeginLiveIngest(context.Background())
	if !ok {
		t.Fatal("initial ingest blocked")
	}
	paused := make(chan struct{})
	go func() { w.SetLivePaused(true); close(paused) }()
	select {
	case <-paused:
		t.Fatal("pause did not wait for active commit")
	case <-time.After(10 * time.Millisecond):
	}
	release()
	select {
	case <-paused:
	case <-time.After(time.Second):
		t.Fatal("pause hung")
	}
	if !w.State().Live.Paused {
		t.Fatal("pause missing from state")
	}
	// Collector status replacement must not clear the workspace pause.
	w.SetState(func(s *ImportState) { s.Live = &LiveState{Live: true} })
	if !w.State().Live.Paused {
		t.Fatal("collector cleared pause")
	}
	if release, ok := w.TryLiveIngest(); ok {
		release()
		t.Fatal("periodic flush entered paused workspace")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if release, ok := w.BeginLiveIngest(ctx); ok {
		release()
		t.Fatal("cancelled ingest entered paused workspace")
	}
	resumed := make(chan struct{})
	go func() {
		release, ok := w.BeginLiveIngest(context.Background())
		if ok {
			release()
			close(resumed)
		}
	}()
	w.SetLivePaused(false)
	select {
	case <-resumed:
	case <-time.After(time.Second):
		t.Fatal("resume did not release waiting ingestion")
	}
}
