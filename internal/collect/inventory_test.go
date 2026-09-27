package collect

import (
	"os"
	"testing"
)

// TestCollectsDocIsCurrent keeps docs/COLLECTS.md equal to the generated
// inventory. Regenerate with: go run ./cmd/flightbay docs collects > docs/COLLECTS.md
func TestCollectsDocIsCurrent(t *testing.T) {
	got := InventoryMarkdown()
	if os.Getenv("UPDATE_DOCS") == "1" {
		if err := os.WriteFile("../../docs/COLLECTS.md", []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile("../../docs/COLLECTS.md")
	if err != nil {
		t.Fatalf("docs/COLLECTS.md missing: run UPDATE_DOCS=1 go test ./internal/collect -run CollectsDoc (%v)", err)
	}
	if string(want) != got {
		t.Fatal("docs/COLLECTS.md is out of date with the collector: run UPDATE_DOCS=1 go test ./internal/collect -run CollectsDoc")
	}
}
