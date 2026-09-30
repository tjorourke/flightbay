package features

import "testing"

func TestEnv(t *testing.T) {
	t.Setenv("FLIGHTBAY_ENTERPRISE", "")
	t.Setenv("FLIGHTBAY_FEATURES", "")
	if !Enabled(Priorities) || !Enabled(Traffic) || !Enabled(Reach) {
		t.Fatal("on by default")
	}
	t.Setenv("FLIGHTBAY_ENTERPRISE", "1")
	if !Enabled(Priorities) {
		t.Fatal("FLIGHTBAY_ENTERPRISE=1 keeps everything on")
	}
	t.Setenv("FLIGHTBAY_FEATURES", "-priorities")
	if Enabled(Priorities) {
		t.Fatal("a leading - turns one off")
	}
	t.Setenv("FLIGHTBAY_ENTERPRISE", "0")
	t.Setenv("FLIGHTBAY_FEATURES", "")
	if Enabled(Priorities) {
		t.Fatal("FLIGHTBAY_ENTERPRISE=0 turns everything off")
	}
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

func TestActivityIsTraffic(t *testing.T) {
	t.Setenv("FLIGHTBAY_ENTERPRISE", "0")
	t.Setenv("FLIGHTBAY_FEATURES", "activity")
	if !Enabled(Traffic) || !Enabled(Activity) {
		t.Fatal("the retired activity feature turns traffic on")
	}
	for _, f := range List() {
		if f.Name == "activity" {
			t.Fatal("activity is no longer listed on its own")
		}
	}
}

func TestAgentMode(t *testing.T) {
	t.Setenv("AGENT", "")
	t.Setenv("FLIGHTBAY_AGENT", "")
	if AgentMode() {
		t.Fatal("off by default")
	}
	t.Setenv("AGENT", "1")
	if !AgentMode() {
		t.Fatal("AGENT=1 turns it on")
	}
	t.Setenv("AGENT", "")
	t.Setenv("FLIGHTBAY_AGENT", "true")
	if !AgentMode() {
		t.Fatal("FLIGHTBAY_AGENT=true turns it on")
	}
	SetAgentMode(false)
	if AgentMode() {
		t.Fatal("override wins")
	}
	mu.Lock()
	agentOverride = nil
	mu.Unlock()
}
