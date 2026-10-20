package server

import (
	"github.com/tjorourke/flightbay/internal/ambient"
	"testing"
)

func TestCreatedSharedConfigurationCannotBeRemovedBeforeItsLaterEdit(t *testing.T) {
	first := &ambientRecord{Token: "first", Context: "mesh1", Server: "api", Created: []ambientCreated{{Kind: "EnvoyFilter", Namespace: "app", Name: "api-controls", UID: "uid"}}}
	later := &ambientRecord{Token: "later", Context: "mesh1", Server: "api", ApplyState: "applied", Modified: []ambient.Change{{Ref: ambient.Ref{Kind: "EnvoyFilter", Namespace: "app", Name: "api-controls"}, UID: "uid"}}}
	records := map[string]*ambientRecord{"first": first, "later": later}
	if ambientCleanupDependency(first, records) == nil {
		t.Fatal("undo would delete another applied control")
	}
	later.ApplyState = "removed"
	if err := ambientCleanupDependency(first, records); err != nil {
		t.Fatal(err)
	}
	later.ApplyState = "applied"
	later.Context = "mesh2"
	if err := ambientCleanupDependency(first, records); err != nil {
		t.Fatal("an independent cluster blocked undo", err)
	}
}
