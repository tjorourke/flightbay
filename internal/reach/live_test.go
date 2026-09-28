package reach

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/tjorourke/flightbay/internal/importer"
	"github.com/tjorourke/flightbay/internal/store"
)

// FLIGHTBAY_REACH_BUNDLE=path go test ./internal/reach -run Live -v
// Add FLIGHTBAY_REACH_JSON=file to write the full report.
func TestLive(t *testing.T) {
	p := os.Getenv("FLIGHTBAY_REACH_BUNDLE")
	if p == "" {
		t.Skip()
	}
	f, _ := os.Open(p)
	ws := store.New()
	if err := importer.Run(context.Background(), f, ws, importer.Options{}); err != nil {
		t.Fatal(err)
	}
	ws.WaitLogs()
	r := Evaluate(ws)
	fmt.Println("flat:", r.Flat, "notes:", r.Notes)
	for _, a := range r.Agents {
		fmt.Printf("\n== %s/%s [%s] radius=%s internet=%s metadata=%s direct=%d closure=%d/%d write=%d observed=%d neverUsed=%d crit=%d high=%d obsCovered=%v\n   %s\n",
			a.Namespace, a.Name, a.Type, a.Radius, a.Internet, a.Metadata, a.Counts.Direct, a.Counts.Closure, a.Counts.Services, a.Counts.Write, a.Counts.Observed, a.Counts.NeverUsed, a.Counts.Critical, a.Counts.High, a.Observed, a.Summary)
		fmt.Printf("   identity: sa=%s mesh=%v token=%v rbac=%d dangerous=%v secrets=%v host=%v\n", a.Identity.ServiceAccount, a.Identity.InMesh, a.Identity.TokenMounted, len(a.Identity.RBAC), a.Identity.Dangerous, a.Identity.Secrets, a.Identity.HostAccess)
		for _, f := range a.Findings {
			fmt.Printf("   [%s] %s\n", f.Severity, f.Title)
		}
		for _, p := range a.Paths {
			fmt.Printf("   path %s: %s\n", p.Kind, strings.Join(p.Hops, " -> "))
		}
		for _, e := range a.Direct {
			if e.Observed > 0 || e.Declared || e.Risk == "red" {
				fmt.Printf("   -> %-60s %s %s obs=%d decl=%v %v %s\n", e.To, e.Verdict, e.Risk, e.Observed, e.Declared, e.ObsMethods, e.Why)
			}
		}
		fmt.Printf("   graph: %d nodes %d edges\n", len(a.Nodes), len(a.Edges))
	}
	if out := os.Getenv("FLIGHTBAY_REACH_JSON"); out != "" {
		b, _ := json.MarshalIndent(r, "", " ")
		_ = os.WriteFile(out, b, 0o644)
	}
}
