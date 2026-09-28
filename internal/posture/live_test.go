package posture

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/tjorourke/flightbay/internal/importer"
	"github.com/tjorourke/flightbay/internal/store"
)

// FLIGHTBAY_POSTURE_BUNDLE=path go test ./internal/posture -run Live -v
func TestLive(t *testing.T) {
	p := os.Getenv("FLIGHTBAY_POSTURE_BUNDLE")
	if p == "" {
		t.Skip()
	}
	f, _ := os.Open(p)
	ws := store.New()
	if err := importer.Run(context.Background(), f, ws, importer.Options{}); err != nil {
		t.Fatal(err)
	}
	r := Evaluate(ws)
	for _, a := range r.Areas {
		fmt.Printf("== %s present=%v score=%d %s %v %s\n", a.Title, a.Present, a.Score, a.Grade, a.Categories, a.Reason)
	}
	for _, c := range r.Checks {
		fmt.Printf("%-5s %-8s %-45s %d/%d %s\n", c.Status, c.Severity, c.ID, c.Passed, c.Total, c.Reason)
		for i, it := range c.Items {
			if i < 4 && it.Status != Pass {
				fmt.Printf("        %s %s/%s: %s\n", it.Kind, it.Namespace, it.Name, it.Detail)
			}
		}
	}
}
