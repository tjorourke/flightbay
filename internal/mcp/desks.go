package mcp

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Desk is one open desk the stdio server can read: a bundle or a live mirror.
type Desk struct {
	URL     string    `json:"url"`
	Token   string    `json:"-"`
	Bundle  string    `json:"bundle"`
	WorkDir string    `json:"workdir"`
	Started time.Time `json:"started"`
}

// noFallback is appended to every error that means the snapshot cannot be
// read, so an agent reports it instead of going round the desk.
const noFallback = "The clusters in a snapshot are usually a customer's and cannot be reached from this machine. Do not run kubectl, cloud CLIs or any other command against a cluster, and do not call the desk's HTTP API directly: tell the user, and wait for them to open the bundle."

var errNoDesk = errors.New("no Flightbay desk is open on this machine, so there is no snapshot to read. Ask the user to open the bundle with `flightbay ui BUNDLE` (or `flightbay-desk open BUNDLE`), then call the tool again. " + noFallback)

// desk returns the base URL and token for the next request. With Desks set
// (the stdio server), the open desks are listed on every call, so a desk
// opened, closed or restarted after the agent started is followed.
func (s *Server) desk() (string, string, error) {
	if s.Desks == nil {
		return s.BaseURL, s.Token, nil
	}
	ds, err := s.Desks()
	if err != nil {
		return "", "", err
	}
	d, ok := pick(ds, s.selected)
	if !ok {
		return "", "", errNoDesk
	}
	return d.URL, d.Token, nil
}

// pick returns the selected desk when it is still open, else the first
// (Desks lists the most recently opened first).
func pick(ds []Desk, selected string) (Desk, bool) {
	for _, d := range ds {
		if selected != "" && d.WorkDir == selected {
			return d, true
		}
	}
	if len(ds) == 0 {
		return Desk{}, false
	}
	return ds[0], true
}

func (s *Server) workspaces(a map[string]any) (string, error) {
	ds, err := s.Desks()
	if err != nil {
		return "", err
	}
	if len(ds) == 0 {
		return "", errNoDesk
	}
	if use := strings.TrimSpace(arg(a, "use")); use != "" {
		d, err := choose(ds, use)
		if err != nil {
			return "", err
		}
		s.selected = d.WorkDir
	}
	cur, _ := pick(ds, s.selected)
	var b strings.Builder
	for i, d := range ds {
		mark := " "
		if d.WorkDir == cur.WorkDir {
			mark = "*"
		}
		fmt.Fprintf(&b, "%s %d. %s  (%s, opened %s)\n", mark, i+1, label(d.Bundle), d.URL, d.Started.Format(time.RFC3339))
	}
	b.WriteString("\n* is the desk every tool reads. Pass use (a number, bundle file name or live context) to switch.")
	return b.String(), nil
}

// choose matches use against a list number, then the bundle or live context.
func choose(ds []Desk, use string) (Desk, error) {
	if n, err := strconv.Atoi(use); err == nil {
		if n < 1 || n > len(ds) {
			return Desk{}, fmt.Errorf("there are %d desks open; use a number from 1 to %d", len(ds), len(ds))
		}
		return ds[n-1], nil
	}
	var hits []Desk
	for _, d := range ds {
		if strings.Contains(strings.ToLower(d.Bundle), strings.ToLower(use)) {
			hits = append(hits, d)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return Desk{}, fmt.Errorf("no open desk matches %q; call workspaces with no arguments for the list", use)
	default:
		return Desk{}, fmt.Errorf("%d open desks match %q; use the number from the list instead", len(hits), use)
	}
}

// label names a desk the way the user opened it.
func label(bundle string) string {
	if ctx, ok := strings.CutPrefix(bundle, "live:"); ok {
		return "live mirror of " + ctx
	}
	if i := strings.LastIndexAny(bundle, `/\`); i >= 0 {
		return bundle[i+1:]
	}
	return bundle
}
