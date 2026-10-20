package ambient

import "testing"

// A new task in Capabilities() must get a group and a label before it ships.
func TestTaskTableMatchesCapabilities(t *testing.T) {
	ws := fixture(t)
	caps := Capabilities(ws, "c", "app", "front")
	if len(caps) == 0 {
		t.Fatal("no capabilities returned")
	}
	seen := map[string]bool{}
	for _, c := range caps {
		seen[c.ID] = true
		d, ok := TaskByID(c.ID)
		if !ok {
			t.Errorf("task %q is in Capabilities() but has no entry in TaskTable", c.ID)
			continue
		}
		if d.Title == "" || d.ShortLabel == "" || d.AddLabel == "" || d.Description == "" || d.Group == "" {
			t.Errorf("task %q has an incomplete entry: %+v", c.ID, d)
		}
	}
	ids := map[string]bool{}
	groups := map[string]bool{}
	for _, g := range TaskGroups {
		groups[g.ID] = true
	}
	for _, d := range TaskTable {
		if ids[d.ID] {
			t.Errorf("task %q is listed twice", d.ID)
		}
		ids[d.ID] = true
		if !seen[d.ID] {
			t.Errorf("task %q is in TaskTable but not in Capabilities()", d.ID)
		}
		if !groups[d.Group] {
			t.Errorf("task %q has unknown group %q", d.ID, d.Group)
		}
		if d.Layer != "" && d.Layer != "L4" && d.Layer != "L7" {
			t.Errorf("task %q has layer %q", d.ID, d.Layer)
		}
	}
}

func TestAddLabelTemplate(t *testing.T) {
	d, _ := TaskByID("strict-mtls")
	if got := d.AddLabelFor("reviews"); got != "Require mTLS for reviews" {
		t.Fatal(got)
	}
}
