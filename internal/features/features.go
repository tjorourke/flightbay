// Package features gates Flightbay Enterprise features.
//
// The customer collector and the open desk work without any of these. They
// are turned on for Solo support (and, later, licensed customers) with:
//
//	FLIGHTBAY_ENTERPRISE=1                  every Enterprise feature
//	FLIGHTBAY_FEATURES=priorities,reach     just these (a leading - turns one off)
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
)

var catalogue = map[string]string{
	Priorities: "Best practices: fix-first ranking, points recovered per fix, severity colours",
	Reach:      "Agent reach: every agent's blast radius, the network paths it has and what to lock down",
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

func fromEnv() map[string]bool {
	on := map[string]bool{}
	if truthy(os.Getenv("FLIGHTBAY_ENTERPRISE")) {
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
			delete(on, f[1:])
		default:
			on[f] = true
		}
	}
	return on
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
