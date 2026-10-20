package collect

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/policy"
	"github.com/tjorourke/flightbay/internal/redact"
	"github.com/tjorourke/flightbay/internal/store"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type unavailableDiscovery struct {
	*fakeDiscovery
	offline atomic.Bool
}

func (d *unavailableDiscovery) ServerGroupsAndResources() ([]*metav1.APIGroup, []*metav1.APIResourceList, error) {
	if d.offline.Load() {
		return nil, nil, fmt.Errorf("offline fixture")
	}
	return d.fakeDiscovery.ServerGroupsAndResources()
}

func TestLiveGroupKeepsBothClustersWatchingAndExportsBoth(t *testing.T) {
	ws := store.New()
	p, _ := policy.Profile("standard")
	p.Namespaces = []string{"payments"}
	p.Contexts = []string{"mesh1", "mesh2"}
	rd, _ := redact.New(nil)
	h1, h2 := newHarness(), newHarness()
	for i := range h2.disc.lists[0].APIResources {
		r := &h2.disc.lists[0].APIResources[i]
		if r.Name == "configmaps" {
			r.Verbs = []string{"get", "list"}
		}
	}
	g := &LiveGroup{RetryDelay: 50 * time.Millisecond}
	discovery2 := &unavailableDiscovery{fakeDiscovery: h2.disc}
	discovery2.offline.Store(true)
	for i, h := range []*harness{h1, h2} {
		cl := h.clients()
		if i == 1 {
			cl.Discovery = discovery2
		}
		cl.Logs = &liveLogs{}
		g.Readers = append(g.Readers, &Live{WS: ws, C: &Collector{Policy: p, Redactor: rd}, CL: cl, O: LiveOptions{Context: p.Contexts[i], FlushEvery: 50 * time.Millisecond}})
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- g.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("fleet did not stop")
		}
	}()
	waitFor(t, "healthy cluster survives unavailable peer", func() bool {
		st := ws.State()
		return st.Live != nil && len(st.Live.Clusters) == 2 && st.Live.Clusters[0].Phase == store.PhaseLive && st.Live.Clusters[1].Phase == store.PhaseFailed
	})
	if ws.State().Live.Connected {
		t.Fatal("unavailable peer was reported healthy")
	}
	discovery2.offline.Store(false)
	waitFor(t, "both clusters initially synced", func() bool {
		st := ws.State()
		return st.Live != nil && len(st.Live.Clusters) == 2 && st.Live.Clusters[0].Phase == store.PhaseLive && st.Live.Clusters[1].Phase == store.PhaseLive
	})
	if len(ws.Header().Contexts) != 2 {
		t.Fatal("runner overwrote the shared header")
	}
	for _, c := range p.Contexts {
		if ws.Get(c, "", "pods", "payments", "checkout-1") == nil {
			t.Fatalf("missing objects in %s", c)
		}
		waitFor(t, "logs on "+c, func() bool { return len(ws.LogStream(c, "payments", "checkout-1", "app", "current")) > 0 })
	}
	if err := h2.dyn.Tracker().Create(schema.GroupVersionResource{Version: "v1", Resource: "pods"}, crashPod("payments", "only-mesh2"), "payments"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "remote watch", func() bool { return ws.Get("mesh2", "", "pods", "payments", "only-mesh2") != nil })
	ws.SetLivePaused(true)
	beforeLogs := ws.State().LogLines
	if err := h2.dyn.Tracker().Create(schema.GroupVersionResource{Version: "v1", Resource: "pods"}, crashPod("payments", "arrived-while-paused"), "payments"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if ws.Get("mesh2", "", "pods", "payments", "arrived-while-paused") != nil || ws.State().LogLines != beforeLogs {
		t.Fatal("paused mirror still ingested objects or logs")
	}
	ws.SetLivePaused(false)
	waitFor(t, "resume catches up watch", func() bool { return ws.Get("mesh2", "", "pods", "payments", "arrived-while-paused") != nil })
	if ws.Get("mesh1", "", "pods", "payments", "only-mesh2") != nil {
		t.Fatal("watch leaked across clusters")
	}
	for _, a := range h2.dyn.Actions() {
		if a.GetVerb() == "watch" && a.GetResource().Resource == "configmaps" {
			t.Fatal("attempted to watch a list-only API")
		}
	}
	var out bytes.Buffer
	if err := ExportWorkspace(ws, &out, ExportOptions{}); err != nil {
		t.Fatal(err)
	}
	r, err := bundle.NewReader(bytes.NewReader(out.Bytes()), bundle.ReaderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for {
		member, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if member.Name == "bundle.json" {
			var h bundle.Header
			if err := json.Unmarshal(member.Data, &h); err != nil {
				t.Fatal(err)
			}
			if len(h.Contexts) != 2 {
				t.Fatal("export lost a context")
			}
		}
		for _, c := range p.Contexts {
			if member.Name == bundle.ResourcePath(c, "", "v1", "pods", 1) {
				seen[c] = true
			}
		}
	}
	if len(seen) != 2 {
		t.Fatalf("export did not include both clusters: %v", seen)
	}
	if err := r.Verify(); err != nil {
		t.Fatal(err)
	}
}
