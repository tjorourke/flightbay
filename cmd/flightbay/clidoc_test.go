package main

import (
	"testing"

	"github.com/tjorourke/flightbay/internal/clidoc"
)

func TestCLIReference(t *testing.T) {
	clidoc.Check(t, newRoot(), "../../docs/site/content/reference/cli-flightbay.md", "flightbay CLI", "Every flightbay command and flag, generated from the command tree.", 10)
}
