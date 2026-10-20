package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tjorourke/flightbay/internal/ambient"
	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/store"
)

var revAPRes = bundle.APIResource{Group: "security.istio.io", Version: "v1", Resource: "authorizationpolicies", Kind: "AuthorizationPolicy", Namespaced: true}

func revAP(ns, rv, action string) map[string]any {
	return map[string]any{"apiVersion": "security.istio.io/v1", "kind": "AuthorizationPolicy",
		"metadata": map[string]any{"name": "deny", "namespace": ns, "resourceVersion": rv, "generation": int64(1)},
		"spec":     map[string]any{"action": action}}
}

func revGet(t *testing.T, srv *httptest.Server, query string) (int, ambientRevisionsOut) {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+"/api/ambient/revisions?"+query, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out ambientRevisionsOut
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func revToken(c byte) string { return strings.Repeat(string(c), 48) }

func TestAmbientRevisionsLive(t *testing.T) {
	ws := store.New()
	t.Cleanup(func() { ws.Close() })
	ws.SetHeader(&bundle.Header{Contexts: []string{"kind-mesh"}, Source: store.SourceLive})
	ws.SetState(func(st *store.ImportState) {
		st.Live = &store.LiveState{Live: true, Context: "kind-mesh", Server: "https://mesh:6443"}
	})
	cluster := bundle.SafeID("kind-mesh")
	ws.AddObjects(cluster, revAPRes, []map[string]any{revAP("app", "10", "DENY"), revAP("other", "11", "DENY")}, "live:0")

	s := &Server{WS: ws, Token: tok, WorkDir: t.TempDir(), AmbientStateDir: t.TempDir()}
	applied := time.Now().UTC().Add(-2 * time.Second)
	plan := &ambient.Plan{Request: ambient.Request{Cluster: cluster}}
	mod := func(ref ambient.Ref) []ambient.Change {
		return []ambient.Change{{Ref: ref, APIVersion: "security.istio.io/v1"}}
	}
	full := ambient.Ref{Cluster: cluster, Group: "security.istio.io", Resource: "authorizationpolicies", Kind: "AuthorizationPolicy", Namespace: "app", Name: "deny"}
	otherNS, otherKind, otherCluster := full, full, full
	otherNS.Namespace = "other"
	otherKind.Kind = "PeerAuthentication"
	otherCluster.Cluster = "kind-elsewhere"
	recs := []*ambientRecord{
		{Token: revToken('a'), Context: "kind-mesh", Server: "https://mesh:6443", Plan: plan, ApplyState: "applied", ApplyFinished: applied, Modified: mod(full)},
		{Token: revToken('b'), Context: "kind-mesh", Server: "https://mesh:6443", Plan: plan, ApplyState: "applied", ApplyFinished: applied, Modified: mod(otherNS)},
		{Token: revToken('c'), Context: "kind-mesh", Server: "https://mesh:6443", Plan: plan, ApplyState: "applied", ApplyFinished: applied, Modified: mod(otherKind)},
		{Token: revToken('d'), Context: "kind-elsewhere", Server: "https://other:6443", Plan: plan, ApplyState: "applied", ApplyFinished: applied, Modified: mod(otherCluster)},
		// Same name, namespace and kind, but created through a context that is
		// another cluster: the record's plan cluster does not make it this one.
		{Token: revToken('e'), Context: "kind-elsewhere", Server: "https://other:6443", Plan: &ambient.Plan{Request: ambient.Request{Cluster: "kind-elsewhere"}}, ApplyState: "applied", ApplyFinished: applied,
			Created: []ambientCreated{{APIVersion: "security.istio.io/v1", Kind: "AuthorizationPolicy", Namespace: "app", Name: "deny"}}},
	}
	for _, r := range recs {
		if err := s.saveAmbientRecord(r); err != nil {
			t.Fatal(err)
		}
	}

	ws.UpsertObject(cluster, revAPRes, revAP("app", "12", "ALLOW"))
	st := revAP("app", "13", "ALLOW")
	st["status"] = map[string]any{"ok": true}
	ws.UpsertObject(cluster, revAPRes, st) // status only: not a revision

	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() { srv.Close(); s.Close() })

	code, out := revGet(t, srv, "cluster="+cluster+"&kind=AuthorizationPolicy&namespace=app&name=deny")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if out.Mode != "live" || out.Ref.Group != "security.istio.io" || out.Ref.Namespace != "app" {
		t.Fatalf("mode/ref %+v %+v", out.Mode, out.Ref)
	}
	if len(out.Revisions) != 2 {
		t.Fatalf("revisions %+v", out.Revisions)
	}
	newest, first := out.Revisions[0], out.Revisions[1]
	if newest.Event != "updated" || newest.ResourceVersion != "12" || newest.Initial {
		t.Fatalf("newest %+v", newest)
	}
	if first.Event != "added" || !first.Initial || first.Diff != "" || first.ResourceVersion != "10" {
		t.Fatalf("first %+v", first)
	}
	if !strings.Contains(newest.Diff, "-  action: DENY") || !strings.Contains(newest.Diff, "+  action: ALLOW") || !strings.HasPrefix(newest.Diff, "--- resourceVersion 10\n+++ resourceVersion 12\n@@ ") {
		t.Fatalf("diff:\n%s", newest.Diff)
	}
	if !strings.Contains(newest.YAML, "action: ALLOW") || strings.Contains(newest.YAML, "status") {
		t.Fatalf("yaml:\n%s", newest.YAML)
	}
	if _, err := time.Parse(time.RFC3339, newest.At); err != nil {
		t.Fatal(err)
	}
	if newest.FlightbayToken != revToken('a') || first.FlightbayToken != "" {
		t.Fatalf("credited %q / %q", newest.FlightbayToken, first.FlightbayToken)
	}
	if len(out.Flightbay) != 1 || out.Flightbay[0].Token != revToken('a') || out.Flightbay[0].Op != "modified" || out.Flightbay[0].ApplyState != "applied" || out.Flightbay[0].Context != "kind-mesh" {
		t.Fatalf("flightbay %+v", out.Flightbay)
	}

	// The other namespace has its own history and none of Flightbay's applies
	// except the one that named it.
	_, other := revGet(t, srv, "cluster="+cluster+"&kind=AuthorizationPolicy&namespace=other&name=deny")
	if len(other.Revisions) != 1 || len(other.Flightbay) != 1 || other.Flightbay[0].Token != revToken('b') {
		t.Fatalf("other namespace %+v", other)
	}

	// A deletion is the newest revision and its diff removes every line.
	ws.DeleteObject(cluster, revAPRes.Group, revAPRes.Resource, "app", "deny")
	_, out = revGet(t, srv, "cluster="+cluster+"&kind=AuthorizationPolicy&namespace=app&name=deny&group=security.istio.io")
	if len(out.Revisions) != 3 || out.Revisions[0].Event != "deleted" || !strings.Contains(out.Revisions[0].Diff, "-  action: ALLOW") || strings.Contains(out.Revisions[0].Diff, "\n+ ") {
		t.Fatalf("after delete %+v", out.Revisions)
	}

	if code, _ := revGet(t, srv, "cluster="+cluster+"&kind=AuthorizationPolicy&namespace=app&name=missing"); code != 404 {
		t.Fatalf("missing object %d", code)
	}
	if code, _ := revGet(t, srv, "kind=AuthorizationPolicy&namespace=app"); code != 400 {
		t.Fatalf("no name %d", code)
	}
}

func TestAmbientRevisionsSnapshot(t *testing.T) {
	ws := store.New()
	t.Cleanup(func() { ws.Close() })
	captured := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	ws.SetHeader(&bundle.Header{Contexts: []string{"captured"}, CaptureStart: captured})
	ws.AddObjects("captured", revAPRes, []map[string]any{revAP("app", "77", "DENY")}, "bundle")
	s := &Server{WS: ws, Token: tok, WorkDir: t.TempDir(), AmbientStateDir: t.TempDir()}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() { srv.Close(); s.Close() })

	code, out := revGet(t, srv, "cluster=captured&kind=AuthorizationPolicy&namespace=app&name=deny")
	if code != 200 || out.Mode != "snapshot" || len(out.Revisions) != 1 {
		t.Fatalf("%d %+v", code, out)
	}
	r := out.Revisions[0]
	if !r.Initial || r.Diff != "" || r.ResourceVersion != "77" || r.At != captured.Format(time.RFC3339) || !strings.Contains(r.YAML, "action: DENY") {
		t.Fatalf("snapshot revision %+v", r)
	}
	if out.Flightbay == nil || len(out.Flightbay) != 0 {
		t.Fatalf("flightbay %+v", out.Flightbay)
	}
}
