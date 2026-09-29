package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/tjorourke/flightbay/internal/mcp"
)

// Every open desk records itself in ~/.flightbay/desks/PID.json and removes
// the file when it closes. current.json still names the most recently opened
// desk for older helpers, but it is one slot: a second desk overwrites it and
// a desk that dies leaves it behind, so the MCP server and the helpers read
// the registry and check each desk is still answering.

func desksDir() string { return filepath.Join(home(), "desks") }

// registerDesk records an open desk and returns the function that removes
// it again. On close, current.json passes to the newest desk still
// registered; with none it is left, since readers skip a desk that is gone
// and it may name a desk from an older build that has no entry.
func registerDesk(info deskInfo) (func(), error) {
	b, _ := json.MarshalIndent(info, "", "  ")
	_ = os.WriteFile(filepath.Join(info.WorkDir, "desk.json"), b, 0o600)
	if err := os.MkdirAll(desksDir(), 0o700); err != nil {
		return func() {}, err
	}
	entry := filepath.Join(desksDir(), strconv.Itoa(info.PID)+".json")
	current := filepath.Join(home(), "current.json")
	err := errors.Join(os.WriteFile(entry, b, 0o600), os.WriteFile(current, b, 0o600))
	return func() {
		_ = os.Remove(entry)
		var cur deskInfo
		if c, err := os.ReadFile(current); err != nil || json.Unmarshal(c, &cur) != nil || cur.PID != info.PID {
			return
		}
		var next string
		var nextT time.Time
		ms, _ := filepath.Glob(filepath.Join(desksDir(), "*.json"))
		for _, m := range ms {
			if fi, err := os.Stat(m); err == nil && fi.ModTime().After(nextT) {
				next, nextT = m, fi.ModTime()
			}
		}
		if b, err := os.ReadFile(next); next != "" && err == nil {
			_ = os.WriteFile(current, b, 0o600)
		}
	}, err
}

type openDesk struct {
	deskInfo
	started time.Time
}

// openDesks lists the desks that answer with their token, most recently
// opened first. Entries whose desk is gone are removed.
func openDesks() []openDesk {
	var cands []openDesk
	seen := map[string]bool{}
	add := func(path string) {
		b, err := os.ReadFile(path)
		if err != nil {
			return
		}
		var d deskInfo
		fi, _ := os.Stat(path)
		if json.Unmarshal(b, &d) != nil || d.URL == "" || d.Token == "" || seen[d.URL+d.Token] {
			return
		}
		seen[d.URL+d.Token] = true
		cands = append(cands, openDesk{d, fi.ModTime()})
	}
	ms, _ := filepath.Glob(filepath.Join(desksDir(), "*.json"))
	for _, m := range ms {
		add(m)
	}
	add(filepath.Join(home(), "current.json"))

	alive, gone := make([]bool, len(cands)), make([]bool, len(cands))
	var wg sync.WaitGroup
	for i, d := range cands {
		wg.Add(1)
		go func() {
			defer wg.Done()
			alive[i], gone[i] = answers(d.URL, d.Token)
		}()
	}
	wg.Wait()
	var out []openDesk
	for i, d := range cands {
		if alive[i] {
			out = append(out, d)
			continue
		}
		if gone[i] {
			_ = os.Remove(filepath.Join(desksDir(), strconv.Itoa(d.PID)+".json"))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].started.After(out[j].started) })
	return out
}

// answers reports whether a desk is up and accepts the token, and whether
// nothing listens on its address any more (so its entry can go).
// /api/features is the cheapest authenticated endpoint.
func answers(base, token string) (ok, gone bool) {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(base, "/")+"/api/features", nil)
	if err != nil {
		return false, false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return false, errors.Is(err, syscall.ECONNREFUSED)
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK, false
}

// mcpDesks adapts openDesks for the MCP server.
func mcpDesks() ([]mcp.Desk, error) {
	var out []mcp.Desk
	for _, d := range openDesks() {
		out = append(out, mcp.Desk{URL: d.URL, Token: d.Token, Bundle: d.Bundle, WorkDir: d.WorkDir, Started: d.started})
	}
	return out, nil
}
