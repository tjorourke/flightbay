package store

import (
	"fmt"
	"testing"

	"github.com/tjorourke/flightbay/internal/bundle"
)

var apRes = bundle.APIResource{Group: "security.istio.io", Version: "v1", Resource: "authorizationpolicies", Kind: "AuthorizationPolicy", Namespaced: true}

func apObj(rv string, gen int64, action string, status string) map[string]any {
	o := map[string]any{"apiVersion": "security.istio.io/v1", "kind": "AuthorizationPolicy",
		"metadata": map[string]any{"name": "deny", "namespace": "app", "resourceVersion": rv, "generation": gen, "uid": "u1",
			"managedFields": []any{map[string]any{"manager": "kubectl"}}, "labels": map[string]any{"team": "a"}},
		"spec": map[string]any{"action": action}}
	if status != "" {
		o["status"] = map[string]any{"conditions": []any{map[string]any{"type": status}}}
	}
	return o
}

func TestRevisionsInitialUpdateStatusDelete(t *testing.T) {
	w := New()
	defer w.Close()
	w.AddObjects("c1", apRes, []map[string]any{apObj("100", 1, "DENY", "")}, "live:0")

	// A status-only update with a new resourceVersion is noise.
	w.UpsertObject("c1", apRes, apObj("101", 1, "DENY", "Accepted"))
	// A spec change is a revision.
	w.UpsertObject("c1", apRes, apObj("102", 2, "ALLOW", "Accepted"))
	// A label-only change (no generation bump) is a revision too.
	lbl := apObj("103", 2, "ALLOW", "Accepted")
	lbl["metadata"].(map[string]any)["labels"] = map[string]any{"team": "b"}
	w.UpsertObject("c1", apRes, lbl)
	// A generation change with the same spec is recorded.
	gen := apObj("104", 3, "ALLOW", "Accepted")
	gen["metadata"].(map[string]any)["labels"] = map[string]any{"team": "b"}
	w.UpsertObject("c1", apRes, gen)
	w.DeleteObject("c1", apRes.Group, apRes.Resource, "app", "deny")

	ref, revs, ok := w.ObjectRevisions("c1", "security.istio.io", "AuthorizationPolicy", "app", "deny")
	if !ok {
		t.Fatal("no revisions")
	}
	if ref.Resource != "authorizationpolicies" || ref.Kind != "AuthorizationPolicy" {
		t.Fatalf("ref %+v", ref)
	}
	want := []struct {
		event, rv string
		gen       int64
		initial   bool
	}{{"added", "100", 1, true}, {"updated", "102", 2, false}, {"updated", "103", 2, false}, {"updated", "104", 3, false}, {"deleted", "", 3, false}}
	if len(revs) != len(want) {
		t.Fatalf("got %d revisions: %+v", len(revs), revs)
	}
	for i, wnt := range want {
		r := revs[i]
		if r.Event != wnt.event || r.ResourceVersion != wnt.rv || r.Generation != wnt.gen || r.Initial != wnt.initial {
			t.Fatalf("revision %d = %s rv=%s gen=%d initial=%v, want %+v", i, r.Event, r.ResourceVersion, r.Generation, r.Initial, wnt)
		}
		if r.SpecHash == "" || r.At.IsZero() {
			t.Fatalf("revision %d missing hash or time", i)
		}
		if _, has := r.Object["status"]; has {
			t.Fatalf("revision %d kept status", i)
		}
		md := r.Object["metadata"].(map[string]any)
		for _, k := range []string{"managedFields", "resourceVersion", "uid", "generation"} {
			if _, has := md[k]; has {
				t.Fatalf("revision %d kept metadata.%s", i, k)
			}
		}
	}
	if revs[0].SpecHash == revs[1].SpecHash || revs[3].SpecHash != revs[2].SpecHash || revs[4].SpecHash != revs[3].SpecHash {
		t.Fatal("hashes do not follow the configuration")
	}
	if revs[0].Object["spec"].(map[string]any)["action"] != "DENY" {
		t.Fatal("initial snapshot lost its spec")
	}

	// Group "" finds the only group with that kind and name.
	if _, r2, ok := w.ObjectRevisions("c1", "", "AuthorizationPolicy", "app", "deny"); !ok || len(r2) != 5 {
		t.Fatal("group fallback")
	}
	// The ref is matched in full.
	if _, _, ok := w.ObjectRevisions("c2", "security.istio.io", "AuthorizationPolicy", "app", "deny"); ok {
		t.Fatal("another cluster shares the history")
	}
	if _, _, ok := w.ObjectRevisions("c1", "security.istio.io", "AuthorizationPolicy", "other", "deny"); ok {
		t.Fatal("another namespace shares the history")
	}

	// Re-creation after a delete starts with "added".
	w.UpsertObject("c1", apRes, apObj("200", 1, "DENY", ""))
	_, revs, _ = w.ObjectRevisions("c1", "security.istio.io", "AuthorizationPolicy", "app", "deny")
	if last := revs[len(revs)-1]; last.Event != "added" || last.Initial || last.ResourceVersion != "200" {
		t.Fatalf("re-created %+v", last)
	}
}

func TestRevisionsBounded(t *testing.T) {
	w := New()
	defer w.Close()
	w.AddObjects("c1", apRes, []map[string]any{apObj("1", 1, "A0", "")}, "live:0")
	for i := 1; i <= MaxRevisions+20; i++ {
		w.UpsertObject("c1", apRes, apObj(fmt.Sprint(i+1), int64(i+1), fmt.Sprintf("A%d", i), ""))
	}
	_, revs, _ := w.ObjectRevisions("c1", apRes.Group, apRes.Kind, "app", "deny")
	if len(revs) != MaxRevisions {
		t.Fatalf("kept %d revisions, want %d", len(revs), MaxRevisions)
	}
	if revs[0].Initial || revs[len(revs)-1].ResourceVersion != fmt.Sprint(MaxRevisions+21) {
		t.Fatalf("kept the wrong end: first %+v last %+v", revs[0], revs[len(revs)-1])
	}
}
