package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/store"
)

func TestAmbientContextsBindOnlyOriginalLiveConnections(t *testing.T) {
	for _, tc := range []struct {
		name   string
		live   *store.LiveState
		source string
		want   map[string]string
	}{
		{"single live", &store.LiveState{Context: "other", Server: "https://other.example:6443"}, store.SourceLive, map[string]string{"other": "other"}},
		{"multiple live", &store.LiveState{Clusters: []store.LiveClusterState{{LiveState: store.LiveState{Context: "fake", Server: "https://fake.example"}}, {LiveState: store.LiveState{Context: "other", Server: "https://other.example:6443"}}}}, store.SourceLive, map[string]string{"fake": "fake", "other": "other"}},
		{"context repointed", &store.LiveState{Context: "fake", Server: "https://old.example"}, store.SourceLive, map[string]string{}},
		{"offline capture", &store.LiveState{Context: "fake", Server: "https://fake.example"}, "", map[string]string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws := store.New()
			defer ws.Close()
			ws.SetHeader(&bundle.Header{Source: tc.source, Contexts: []string{"fake", "other"}})
			ws.SetState(func(st *store.ImportState) { st.Live = tc.live })
			s := &Server{WS: ws, AmbientKubeconfig: writeKubeconfig(t, "https://fake.example")}
			w := httptest.NewRecorder()
			s.ambientContexts(w, httptest.NewRequest("GET", "/api/ambient/contexts", nil))
			if w.Code != 200 {
				t.Fatalf("%d: %s", w.Code, w.Body)
			}
			var rows []map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				if row["liveCluster"] != tc.want[row["name"]] {
					t.Fatalf("unexpected binding: %+v", row)
				}
			}
		})
	}
}
