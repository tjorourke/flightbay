package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"sync"

	"github.com/coder/websocket"
	"github.com/creack/pty"
)

const scrollback = 1 << 20

// termSession is one shell per workspace. It survives page changes: output is
// kept in a scrollback buffer and replayed when a browser reconnects.
type termSession struct {
	mu      sync.Mutex
	ptmx    *os.File
	cmd     *exec.Cmd
	buf     []byte
	clients map[*websocket.Conn]context.Context
	done    chan struct{}
}

func (s *Server) session() (*termSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.term != nil {
		select {
		case <-s.term.done:
		default:
			return s.term, nil
		}
	}
	if err := s.Prepare(); err != nil {
		return nil, err
	}
	shell := s.Shell
	if shell == "" {
		shell = "/bin/bash"
	}
	cmd := exec.Command(shell, "--noprofile", "--norc", "-i")
	cmd.Env = s.ShellEnv()
	cmd.Dir = s.WorkDir
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 30, Cols: 120})
	if err != nil {
		return nil, err
	}
	t := &termSession{ptmx: ptmx, cmd: cmd, clients: map[*websocket.Conn]context.Context{}, done: make(chan struct{})}
	intro := "\x1b[36mFlightbay snapshot shell.\x1b[0m kubectl, k9s and stern read the snapshot through KUBECONFIG.\r\n" +
		"Helpers: fbq '<log query>', fbfind, fbpc [pod]. Contexts: kubectl config get-contexts. Writes are rejected: this is a read-only snapshot.\r\n"
	t.buf = append(t.buf, intro...)
	go t.pump()
	s.term = t
	return t, nil
}

func (t *termSession) pump() {
	defer close(t.done)
	b := make([]byte, 32<<10)
	for {
		n, err := t.ptmx.Read(b)
		if n > 0 {
			chunk := append([]byte{}, b[:n]...)
			t.mu.Lock()
			t.buf = append(t.buf, chunk...)
			if len(t.buf) > scrollback {
				t.buf = t.buf[len(t.buf)-scrollback:]
			}
			for c, ctx := range t.clients {
				_ = c.Write(ctx, websocket.MessageBinary, chunk)
			}
			t.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (t *termSession) close() {
	_ = t.ptmx.Close()
	if t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
}

func (s *Server) terminal(w http.ResponseWriter, r *http.Request) {
	if !s.Terminal {
		jerr(w, http.StatusNotFound, "the terminal is disabled for this workspace")
		return
	}
	// Accept enforces a same-origin check by default.
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	t, err := s.session()
	if err != nil {
		_ = c.Close(websocket.StatusInternalError, err.Error())
		return
	}
	ctx := r.Context()
	t.mu.Lock()
	if len(t.buf) > 0 {
		_ = c.Write(ctx, websocket.MessageBinary, t.buf)
	}
	t.clients[c] = ctx
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		delete(t.clients, c)
		t.mu.Unlock()
	}()
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		var msg struct {
			Type string `json:"type"`
			Data string `json:"data"`
			Cols uint16 `json:"cols"`
			Rows uint16 `json:"rows"`
		}
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		switch msg.Type {
		case "input":
			_, _ = t.ptmx.Write([]byte(msg.Data))
		case "resize":
			if msg.Cols > 0 && msg.Rows > 0 {
				_ = pty.Setsize(t.ptmx, &pty.Winsize{Cols: msg.Cols, Rows: msg.Rows})
			}
		}
	}
}

func (s *Server) timeline(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, buildTimeline(s.WS, r.URL.Query().Get("cluster")))
}
