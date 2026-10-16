// Package features gates Flightbay Enterprise features.
//
// These optional features are enabled by default. Turn them off with:
//
//	FLIGHTBAY_ENTERPRISE=0                  every Enterprise feature off
//	FLIGHTBAY_FEATURES=-reach               just this one off
//	FLIGHTBAY_ENTERPRISE=0 FLIGHTBAY_FEATURES=traffic   only these on
//
// Agent mode (AGENT=1 or FLIGHTBAY_AGENT=1) trims the desk to what matters
// for agents: Traffic, the agentic Scorecard, Resources and the Log explorer.
//
// This is a product gate, not a security boundary: anything that must be
// protected (the hosted service, uploads) enforces its own authentication.
package features

import (
	"os"
	"sort"
	"strings"
	"sync"
)

// Feature is one gated capability.
type Feature struct {
	Name       string `json:"name"`
	Title      string `json:"title"`
	Enterprise bool   `json:"enterprise"`
	Enabled    bool   `json:"enabled"`
}

// Known features. Add new ones here so the UI and `flightbay-desk features`
// can list them.
const (
	Priorities = "priorities" // best practices: fix-first ranking, points if fixed, severity colours
	Reach      = "reach"      // agent reach: per-agent blast radius, network paths, fixes
	Traffic    = "traffic"    // the Traffic page: ztunnel connections and agentgateway calls, and the "why was this denied?" explainer
	// Activity was its own feature; the agentgateway calls it covered are
	// now a layer of the Traffic page. The name stays so code reads well,
	// and FLIGHTBAY_FEATURES=activity still turns traffic on.
	Activity = Traffic
)

var catalogue = map[string]string{
	Priorities: "Best practices: fix-first ranking, points recovered per fix, severity colours",
	Reach:      "Agent reach: every agent's blast radius, the network paths it has and what to lock down",
	Traffic:    "Traffic: ztunnel connections (mTLS, waypoints, policy verdicts) and agentgateway calls (tools, models, tokens, refusals, tool changes), and why a connection was denied",
}

var (
	mu       sync.RWMutex
	override map[string]bool // set by tests and flags; nil means read the environment
)

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func falsy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "no", "off":
		return true
	}
	return false
}

func fromEnv() map[string]bool {
	on := map[string]bool{}
	if !falsy(os.Getenv("FLIGHTBAY_ENTERPRISE")) {
		for n := range catalogue {
			on[n] = true
		}
	}
	for _, f := range strings.Split(os.Getenv("FLIGHTBAY_FEATURES"), ",") {
		f = strings.TrimSpace(strings.ToLower(f))
		switch {
		case f == "":
		case f == "all" || f == "enterprise":
			for n := range catalogue {
				on[n] = true
			}
		case strings.HasPrefix(f, "-"):
			delete(on, alias(f[1:]))
		default:
			on[alias(f)] = true
		}
	}
	return on
}

// alias maps retired feature names to the feature that took them over.
func alias(f string) string {
	if f == "activity" {
		return Traffic
	}
	return f
}

// Enabled reports whether a feature is on.
func Enabled(name string) bool {
	mu.RLock()
	o := override
	mu.RUnlock()
	if o != nil {
		return o[name]
	}
	return fromEnv()[name]
}

// Set turns features on or off for this process (tests, --enterprise flags).
// Set(nil) goes back to reading the environment.
func Set(on map[string]bool) {
	mu.Lock()
	override = on
	mu.Unlock()
}

// EnableAll turns every Enterprise feature on for this process.
func EnableAll() {
	on := map[string]bool{}
	for n := range catalogue {
		on[n] = true
	}
	Set(on)
}

// List returns every known feature with its state.
func List() []Feature {
	var out []Feature
	for n, t := range catalogue {
		out = append(out, Feature{Name: n, Title: t, Enterprise: true, Enabled: Enabled(n)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Map returns name -> enabled, for the UI.
func Map() map[string]bool {
	out := map[string]bool{}
	for n := range catalogue {
		out[n] = Enabled(n)
	}
	return out
}

var agentOverride *bool

// AgentMode reports whether the desk runs in agent mode: AGENT=1 or
// FLIGHTBAY_AGENT=1, or SetAgentMode.
func AgentMode() bool {
	mu.RLock()
	o := agentOverride
	mu.RUnlock()
	if o != nil {
		return *o
	}
	return truthy(os.Getenv("FLIGHTBAY_AGENT")) || truthy(os.Getenv("AGENT"))
}

// SetAgentMode turns agent mode on or off for this process (tests, the
// --agent flag).
func SetAgentMode(on bool) {
	mu.Lock()
	agentOverride = &on
	mu.Unlock()
}
