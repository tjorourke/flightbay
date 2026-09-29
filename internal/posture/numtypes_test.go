package posture

import (
	"testing"

	"github.com/tjorourke/flightbay/internal/store"
)

// minAvailable decodes as float64 from a bundle and int64 in live mode.
func TestPDBBlocksNumberTypes(t *testing.T) {
	for _, v := range []any{float64(3), int64(3), int(3)} {
		pdb := &store.Object{Obj: map[string]any{"spec": map[string]any{"minAvailable": v}}}
		if !mxPDBBlocks(pdb, 3) {
			t.Errorf("%T minAvailable 3 with 3 replicas blocks eviction", v)
		}
	}
	for _, v := range []any{float64(0), int64(0)} {
		pdb := &store.Object{Obj: map[string]any{"spec": map[string]any{"maxUnavailable": v}}}
		if !mxPDBBlocks(pdb, 3) {
			t.Errorf("%T maxUnavailable 0 blocks eviction", v)
		}
	}
}
