package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tjorourke/flightbay/internal/ambient"
)

func TestNativeHTTPBatchStreamsWithoutKubernetesAccessOrWorkloadCreation(t *testing.T) {
	s, dc, desk, app := ambientFixture(t)
	s.ambientS.connect = func(string) (ambientConnection, error) {
		t.Error("native HTTP attempted a cluster connection despite attribution being disabled")
		return ambientConnection{}, fmt.Errorf("unexpected cluster connection")
	}
	var requests atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"message":"native response"}`)
	}))
	defer origin.Close()
	dc.ClearActions()
	for _, tc := range []struct {
		count, want int
		stream      bool
	}{{0, 1, false}, {5, 5, true}} {
		payload, _ := json.Marshal(httpProbeRequest{Service: ambient.Ref{Cluster: app.Cluster, Kind: "Service", Namespace: app.Namespace, Name: app.Service}, Context: "metadata-context", SkipAttribution: true, Mode: "public", URL: origin.URL, Count: tc.count, Profile: httpProfile{Method: "GET", Path: "/", Timeout: 5}})
		req, _ := http.NewRequest("POST", desk.URL+"/api/ambient/http-test", bytes.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Origin", desk.URL)
		if tc.stream {
			req.Header.Set("Accept", "application/x-ndjson")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 {
			t.Fatalf("native batch returned %d", resp.StatusCode)
		}
		decoder := json.NewDecoder(resp.Body)
		if !tc.stream {
			var out struct {
				Results []httpProbeResult `json:"results"`
			}
			if err := decoder.Decode(&out); err != nil || len(out.Results) != 1 {
				t.Fatalf("legacy single-request contract changed: %+v %v", out, err)
			}
		} else {
			seen, complete := 0, false
			for decoder.More() {
				var event struct {
					Type    string           `json:"type"`
					Origin  string           `json:"origin"`
					Result  meshProbeResult  `json:"result"`
					Summary meshProbeSummary `json:"summary"`
				}
				if err := decoder.Decode(&event); err != nil {
					t.Fatal(err)
				}
				switch event.Type {
				case "start":
					if event.Origin != "flightbay" {
						t.Fatal("native origin not explicit")
					}
				case "result":
					seen++
					if event.Result.Status != 200 || event.Result.Mode != "public" || event.Result.Responder != nil {
						t.Fatalf("invented cluster identity: %+v", event.Result)
					}
				case "complete":
					complete = true
					if event.Summary.Completed != tc.want || event.Summary.Successful != tc.want || event.Summary.Unknown != tc.want {
						t.Fatalf("bad summary: %+v", event.Summary)
					}
				}
			}
			if seen != tc.want || !complete {
				t.Fatalf("incomplete native stream: %d %v", seen, complete)
			}
		}
		resp.Body.Close()
	}
	if requests.Load() != 6 {
		t.Fatalf("wrong HTTP count: %d", requests.Load())
	}
	if len(dc.Actions()) != 0 {
		t.Fatalf("native HTTP touched Kubernetes: %+v", dc.Actions())
	}
}

func TestNativeHTTPRejectsUnboundedTrafficBeforeSendingRequests(t *testing.T) {
	_, dc, desk, app := ambientFixture(t)
	var requests atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer origin.Close()
	dc.ClearActions()
	code, _ := ambientPostPath(t, desk, "/api/ambient/http-test", httpProbeRequest{Service: ambient.Ref{Cluster: app.Cluster, Kind: "Service", Namespace: app.Namespace, Name: app.Service}, Mode: "public", URL: origin.URL, Count: 1001})
	if code != 400 || requests.Load() != 0 || len(dc.Actions()) != 0 {
		t.Fatalf("unbounded native traffic was attempted: HTTP %d, requests %d, cluster actions %v", code, requests.Load(), dc.Actions())
	}
}

func TestNativeHTTPStopCancelsInFlightTraffic(t *testing.T) {
	_, dc, desk, app := ambientFixture(t)
	started, stopped := make(chan struct{}), make(chan struct{})
	var requests atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			fmt.Fprint(w, `{"ok":true}`)
			return
		}
		close(started)
		<-r.Context().Done()
		close(stopped)
	}))
	defer origin.Close()
	dc.ClearActions()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	payload, _ := json.Marshal(httpProbeRequest{Service: ambient.Ref{Cluster: app.Cluster, Kind: "Service", Namespace: app.Namespace, Name: app.Service}, Mode: "public", URL: origin.URL, Count: 1000})
	req, _ := http.NewRequestWithContext(ctx, "POST", desk.URL+"/api/ambient/http-test", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Origin", desk.URL)
	req.Header.Set("Accept", "application/x-ndjson")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	decoder := json.NewDecoder(resp.Body)
	for i := 0; i < 2; i++ {
		var event map[string]any
		if err := decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("second request did not start")
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not cancel the in-flight native request")
	}
	if requests.Load() != 2 || len(dc.Actions()) != 0 {
		t.Fatalf("traffic continued or cluster was changed: %d %+v", requests.Load(), dc.Actions())
	}
}
