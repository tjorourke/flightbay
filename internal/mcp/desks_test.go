package mcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func fakeDesk(t *testing.T, token, bundle string) *httptest.Server {
	t.Helper()
	d := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":"not signed in"}`))
			return
		}
		_, _ = w.Write([]byte(`{"bundle":"` + bundle + `"}`))
	}))
	t.Cleanup(d.Close)
	return d
}

func callText(t *testing.T, s *Server, name string, args map[string]any) (string, bool) {
	t.Helper()
	for _, tl := range s.tools() {
		if tl.Name == name {
			out, err := tl.call(s, args)
			if err != nil {
				return err.Error(), true
			}
			return out, false
		}
	}
	t.Fatalf("no tool %s", name)
	return "", false
}

func TestDesksFollowedPerCall(t *testing.T) {
	a := fakeDesk(t, "ta", "a")
	b := fakeDesk(t, "tb", "b")
	var open []Desk
	s := &Server{Desks: func() ([]Desk, error) { return open, nil }}

	// No desk yet: the error tells the agent not to go round the desk.
	out, isErr := callText(t, s, "workspace_summary", nil)
	if !isErr || !strings.Contains(out, "no Flightbay desk is open") || !strings.Contains(out, "Do not run kubectl") {
		t.Fatalf("no desk: %q", out)
	}

	// A desk opened after the server started is picked up.
	open = []Desk{{URL: a.URL, Token: "ta", Bundle: "/tmp/flightbay-A.tar.zst", WorkDir: "wa", Started: time.Now()}}
	if out, _ := callText(t, s, "workspace_summary", nil); !strings.Contains(out, `"a"`) {
		t.Fatalf("desk a: %q", out)
	}

	// Newest first by default; switch with workspaces.
	open = append([]Desk{{URL: b.URL, Token: "tb", Bundle: "live:kind-mesh1", WorkDir: "wb", Started: time.Now()}}, open...)
	if out, _ := callText(t, s, "workspace_summary", nil); !strings.Contains(out, `"b"`) {
		t.Fatalf("newest: %q", out)
	}
	list, _ := callText(t, s, "workspaces", nil)
	if !strings.Contains(list, "* 1. live mirror of kind-mesh1") || !strings.Contains(list, "  2. flightbay-A.tar.zst") {
		t.Fatalf("list: %q", list)
	}
	if out, isErr := callText(t, s, "workspaces", map[string]any{"use": "flightbay-A"}); isErr || !strings.Contains(out, "* 2.") {
		t.Fatalf("use: %q", out)
	}
	if out, _ := callText(t, s, "workspace_summary", nil); !strings.Contains(out, `"a"`) {
		t.Fatalf("after use: %q", out)
	}
	if out, isErr := callText(t, s, "workspaces", map[string]any{"use": "7"}); !isErr || !strings.Contains(out, "2 desks open") {
		t.Fatalf("bad number: %q", out)
	}

	// The selected desk closes: fall back to the newest.
	open = open[:1]
	if out, _ := callText(t, s, "workspace_summary", nil); !strings.Contains(out, `"b"`) {
		t.Fatalf("fallback: %q", out)
	}
}

func TestRefusedTokenIsNotASignIn(t *testing.T) {
	d := fakeDesk(t, "right", "a")
	s := &Server{BaseURL: d.URL, Token: "wrong"}
	out, isErr := callText(t, s, "workspace_summary", nil)
	if !isErr || !strings.Contains(out, "does not need to sign in") || !strings.Contains(out, "Do not run kubectl") {
		t.Fatalf("401: %q", out)
	}
	for _, tl := range s.tools() {
		if tl.Name == "workspaces" {
			t.Fatal("workspaces offered by a server pinned to one desk")
		}
	}
}
