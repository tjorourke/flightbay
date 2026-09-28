// Package clidoc renders a cobra command tree as one Markdown reference page
// for the docs (docs/site/content/reference/cli-*.md). The pages are
// generated from the real commands and a test fails when they drift.
package clidoc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// Markdown returns the reference page for root.
func Markdown(root *cobra.Command, title, description string, weight int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "---\ntitle: %q\nweight: %d\ndescription: %q\n---\n\n", title, weight, description)
	fmt.Fprintf(&b, "Generated from the `%s` command tree. Run any command with `--help` for the same text.\n\n", root.Name())
	var walk func(c *cobra.Command, depth int)
	walk = func(c *cobra.Command, depth int) {
		if c.Hidden || c.Name() == "help" || c.Name() == "completion" {
			return
		}
		if depth > 0 {
			fmt.Fprintf(&b, "## %s\n\n", c.CommandPath())
		}
		if c.Long != "" {
			b.WriteString(strings.TrimSpace(c.Long) + "\n\n")
		} else if c.Short != "" {
			b.WriteString(strings.TrimSpace(c.Short) + "\n\n")
		}
		if c.Runnable() {
			fmt.Fprintf(&b, "```text\n%s\n```\n\n", c.UseLine())
		}
		if len(c.Aliases) > 0 {
			fmt.Fprintf(&b, "Aliases: `%s`\n\n", strings.Join(c.Aliases, "`, `"))
		}
		if c.Example != "" {
			fmt.Fprintf(&b, "Examples:\n\n```bash\n%s\n```\n\n", strings.Trim(c.Example, "\n"))
		}
		if fl := c.NonInheritedFlags(); fl.HasAvailableFlags() {
			b.WriteString("| Flag | Description |\n|---|---|\n")
			for _, line := range strings.Split(strings.TrimRight(fl.FlagUsages(), "\n"), "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				// "-o, --out string   description" -> flag | description
				i := strings.Index(line, "   ")
				flag, desc := line, ""
				if i > 0 {
					flag, desc = strings.TrimSpace(line[:i]), strings.TrimSpace(line[i:])
				}
				desc = strings.ReplaceAll(desc, "|", "\\|")
				fmt.Fprintf(&b, "| `%s` | %s |\n", flag, desc)
			}
			b.WriteString("\n")
		}
		for _, sub := range c.Commands() {
			walk(sub, depth+1)
		}
	}
	walk(root, 0)
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// Check writes the page with UPDATE_DOCS=1 and otherwise fails when the
// committed page is out of date.
func Check(t *testing.T, root *cobra.Command, file, title, description string, weight int) {
	t.Helper()
	want := Markdown(root, title, description, weight)
	if os.Getenv("UPDATE_DOCS") != "" {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(file)
	if err != nil || string(got) != want {
		t.Fatalf("%s is out of date; run UPDATE_DOCS=1 go test ./cmd/...", file)
	}
}
