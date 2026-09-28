package main

import (
	"testing"

	"github.com/tjorourke/flightbay/internal/clidoc"
)

func TestCLIReference(t *testing.T) {
	clidoc.Check(t, newRoot(), "../../docs/site/content/reference/cli-flightbay-desk.md", "flightbay-desk CLI", "Every flightbay-desk command and flag, generated from the command tree.", 20)
}
