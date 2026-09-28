package features

import "testing"

func TestEnv(t *testing.T) {
	t.Setenv("FLIGHTBAY_ENTERPRISE", "")
	t.Setenv("FLIGHTBAY_FEATURES", "")
	if Enabled(Priorities) {
		t.Fatal("off by default")
	}
	t.Setenv("FLIGHTBAY_ENTERPRISE", "1")
	if !Enabled(Priorities) {
		t.Fatal("FLIGHTBAY_ENTERPRISE=1 turns everything on")
	}
	t.Setenv("FLIGHTBAY_FEATURES", "-priorities")
	if Enabled(Priorities) {
		t.Fatal("a leading - turns one off")
	}
	t.Setenv("FLIGHTBAY_ENTERPRISE", "")
	t.Setenv("FLIGHTBAY_FEATURES", "priorities")
	if !Enabled(Priorities) {
		t.Fatal("named feature")
	}
	Set(map[string]bool{})
	if Enabled(Priorities) {
		t.Fatal("override wins over the environment")
	}
	Set(nil)
}
