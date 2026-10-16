package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/tjorourke/flightbay/internal/store"
)

func post(t *testing.T, url string, v any) int {
	t.Helper()
	req, _ := http.NewRequest("POST", url, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if v != nil {
		_ = json.NewDecoder(resp.Body).Decode(v)
	}
	return resp.StatusCode
}

func TestLiveEndpointsOnABundle(t *testing.T) {
	_, srv := newServer(t, false)
	if code := post(t, srv.URL+"/api/live/save", nil); code != http.StatusConflict {
		t.Fatalf("save on a bundle workspace: %d", code)
	}
	if code := post(t, srv.URL+"/api/live/refresh-diagnostics?pod=istio-system/ztunnel-1", nil); code != http.StatusConflict {
		t.Fatalf("refresh on a bundle workspace: %d", code)
	}
	var w map[string]any
	get(t, srv, "/api/workspace", &w)
	if _, ok := w["live"]; ok {
		t.Fatal("a bundle workspace reported live state")
	}
}

func TestLiveWorkspaceAndEndpoints(t *testing.T) {
	s, srv := newServer(t, false)
	h := *s.WS.Header()
	h.Source, h.Contexts = store.SourceLive, []string{"kind-live"}
	s.WS.SetHeader(&h)
	s.WS.SetState(func(st *store.ImportState) {
		st.Phase = store.PhaseLive
		st.Live = &store.LiveState{Live: true, Context: "kind-live", Connected: true, LogStreams: 3, LogStreamsTotal: 5}
	})
	var saved bool
	s.SaveBundle = func() (string, error) { saved = true; return "/tmp/flightbay-live-x.tar.zst", nil }
	var gotNS, gotPod string
	s.RefreshDiagnostics = func(_ context.Context, ns, pod string) (int, error) {
		gotNS, gotPod = ns, pod
		if pod == "nope" {
			return 0, errors.New("not a supported component")
		}
		return 4, nil
	}
	var w struct {
		Source  string          `json:"source"`
		Live    store.LiveState `json:"live"`
		CanSave bool            `json:"canSave"`
		Refresh bool            `json:"canRefreshDiagnostics"`
	}
	get(t, srv, "/api/workspace", &w)
	if w.Source != "live" || !w.Live.Live || w.Live.Context != "kind-live" || w.Live.LogStreamsTotal != 5 || !w.CanSave || !w.Refresh {
		t.Fatalf("workspace: %+v", w)
	}
	var r struct{ Path string }
	if code := post(t, srv.URL+"/api/live/save", &r); code != 200 || !saved || r.Path == "" {
		t.Fatalf("save: %d %+v", code, r)
	}
	var d struct{ Files int }
	if code := post(t, srv.URL+"/api/live/refresh-diagnostics?pod=istio-system/ztunnel-1", &d); code != 200 || d.Files != 4 || gotNS != "istio-system" || gotPod != "ztunnel-1" {
		t.Fatalf("refresh: %d %+v %s/%s", code, d, gotNS, gotPod)
	}
	if code := post(t, srv.URL+"/api/live/refresh-diagnostics?pod=istio-system/nope", nil); code != 400 {
		t.Fatalf("refresh error: %d", code)
	}
	if code := post(t, srv.URL+"/api/live/refresh-diagnostics?pod=bad", nil); code != 400 {
		t.Fatalf("bad pod accepted: %d", code)
	}
	if code := get(t, srv, "/api/live/save", nil); code != http.StatusMethodNotAllowed {
		t.Fatalf("GET save: %d", code)
	}
	// A cross-site form post is refused.
	req, _ := http.NewRequest("POST", srv.URL+"/api/live/save", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Origin", "https://evil.example")
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin save: %d", resp.StatusCode)
	}
	// Kubeconfig contexts are <cluster>@live.
	if err := s.Prepare(); err != nil {
		t.Fatal(err)
	}
	kc, _ := os.ReadFile(s.KubeconfigPath())
	if !strings.Contains(string(kc), "kind-live@live") || strings.Contains(string(kc), "@snapshot") {
		t.Fatalf("kubeconfig contexts:\n%s", kc)
	}
}
