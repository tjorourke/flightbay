package scan

import (
	"bytes"
	"context"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/importer"
	"github.com/tjorourke/flightbay/internal/store"
)

// Load imports a bundle held in memory into a new in-memory workspace, the
// same way the desk opens one.
func Load(b []byte) (*store.Workspace, error) {
	ws := store.New()
	if err := importer.Run(context.Background(), bytes.NewReader(b), ws, importer.Options{Source: "scan", Reader: bundle.ReaderOptions{}}); err != nil {
		ws.Close()
		return nil, err
	}
	return ws, nil
}
