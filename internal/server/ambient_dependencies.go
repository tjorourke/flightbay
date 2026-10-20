package server

import "fmt"

// A later edit must be undone before deleting a resource created by an earlier
// change. UID/ownership alone cannot detect that another control now shares it.
func ambientCleanupDependency(record *ambientRecord, records map[string]*ambientRecord) error {
	for _, other := range records {
		if other.Token == record.Token || other.Context != record.Context || other.Server != record.Server || other.ApplyState == "removed" {
			continue
		}
		for _, created := range record.Created {
			for _, modified := range other.Modified {
				if created.UID != "" && string(created.UID) == modified.UID && created.Kind == modified.Ref.Kind && created.Namespace == modified.Ref.Namespace && created.Name == modified.Ref.Name {
					return fmt.Errorf("undo the later change to %s %s/%s in %s before removing this configuration; that change still uses a resource created here", created.Kind, created.Namespace, created.Name, record.Context)
				}
			}
		}
	}
	return nil
}
