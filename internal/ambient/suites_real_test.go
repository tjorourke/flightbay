package ambient

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/tjorourke/flightbay/internal/importer"
	"github.com/tjorourke/flightbay/internal/store"
)

func realBundle(t *testing.T) *store.Workspace {
	t.Helper()
	files, _ := filepath.Glob("../../.preview/flightbay-nocase-*.tar.zst")
	if len(files) == 0 {
		t.Skip("no preview bundle")
	}
	f, err := os.Open(files[len(files)-1])
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ws := store.New()
	t.Cleanup(func() { ws.Close() })
	if err := importer.Run(context.Background(), f, ws, importer.Options{}); err != nil {
		t.Fatal(err)
	}
	return ws
}

// The preview bundle has bookinfo reviews on kind-mesh2 with a DENY on header
// color and a weighted HTTPRoute.
func TestRealBookinfoSuites(t *testing.T) {
	ws := realBundle(t)
	list, err := ServiceSuiteList(ws, t.TempDir(), "kind-mesh2", "bookinfo", "reviews")
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Suites) != 2 || list.Suites[0].Action != "DENY" || list.Suites[0].Cases != 5 || list.Suites[1].Kind != KindHTTPRoute || list.Suites[1].Cases != 4 {
		t.Fatalf("%+v", list)
	}
	g, err := GenerateForRef(ws, list.Suites[0].Ref, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(g.Suite); got != "r1-match,r1-value-color,r1-absent-color,r1-case-color,bypass" {
		t.Errorf("authz ids %s", got)
	}
	if g.Suite.Source.Kind != SourceWorkload || g.Suite.Source.Ref.Name == "" {
		t.Errorf("source %+v", g.Suite.Source)
	}
	if w := ExplainAuthz(*g.Authz, nil).InWords; w != "Deny any request to reviews when header color equals red." {
		t.Errorf("words %q", w)
	}
	rg, err := GenerateForRef(ws, list.Suites[1].Ref, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := caseByID(rg.Suite, "r1-weights")
	if w == nil || !reflect.DeepEqual(w.Expect.Weights, map[string]int{"reviews-v1": 90, "reviews-v2": 10}) {
		t.Fatalf("weights %+v", w)
	}
	if n := caseByID(rg.Suite, "not-route"); n == nil || n.Request.Host != "reviews-v1" {
		t.Errorf("%+v", n)
	}
	checks := map[string]SuiteCheck{}
	for _, c := range rg.Suite.Checks {
		checks[c.ID] = c
	}
	if checks["backends"].Count != "2 / 2" || checks["overlap"].Status != "pass" || checks["cluster-kind-mesh1"].Status != "warn" {
		t.Errorf("%+v", checks)
	}
}
