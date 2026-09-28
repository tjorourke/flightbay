package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/tjorourke/flightbay/internal/review"
	"github.com/tjorourke/flightbay/internal/sign"
)

// Review and send on a live mirror, and choosing a signing key in the UI.
//
// A live mirror changes while you choose, so the first visit to Review and
// send freezes it into a bundle in the workspace (FreezeBundle). The plan is
// applied to that frozen copy, exactly as it is for a bundle opened from a
// file, and the reviewed export lands in ExportDir. "Refresh" freezes again.

// ensureReview sets up a review session for a live mirror on first use.
func (s *Server) ensureReview() error {
	if s.Review != nil {
		return nil
	}
	if s.FreezeBundle == nil {
		return errors.New("this workspace was not opened from a bundle file, so there is nothing to export from")
	}
	return s.freeze()
}

func (s *Server) freeze() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Join(s.WorkDir, "frozen")
	path, err := s.FreezeBundle(dir)
	if err != nil {
		return err
	}
	out := s.ExportDir
	if out == "" {
		out = "."
	}
	if abs, err := filepath.Abs(out); err == nil {
		out = abs
	}
	old := s.Review
	rs := &ReviewSession{Source: path, Out: review.OutputName(filepath.Join(out, filepath.Base(path)), false), Frozen: time.Now().UTC()}
	if old != nil {
		rs.Options, rs.Plan = old.Options, old.Plan
		if old.Source != path && strings.HasPrefix(old.Source, dir) {
			_ = os.Remove(old.Source) // the previous frozen copy
		}
	}
	if rs.Options.Signer == nil {
		rs.Options.Signer = s.Signer
	}
	s.Review = rs
	return nil
}

// reviewRefreeze freezes the live mirror again (POST).
func (s *Server) reviewRefreeze(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !sameOrigin(r) {
		jerr(w, http.StatusMethodNotAllowed, "POST from the desk to freeze the live mirror again")
		return
	}
	if s.FreezeBundle == nil {
		jerr(w, http.StatusConflict, "only a live mirror can be refreshed")
		return
	}
	if err := s.freeze(); err != nil {
		jerr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"frozen": s.Review.Frozen, "source": s.Review.Source})
}

// ---------- signing keys ----------

type keyInfo struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	KeyID  string `json:"keyId"`
	Public string `json:"publicPath,omitempty"`
}

// KeysDir is where keys created in the UI are kept: ~/.flightbay/keys.
func KeysDir() string {
	if d := os.Getenv("FLIGHTBAY_HOME"); d != "" {
		return filepath.Join(d, "keys")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".flightbay", "keys")
}

// findKeys lists private keys in the keys directory, the directory flightbay
// was started from, and next to the key in use.
func (s *Server) findKeys() []keyInfo {
	dirs := []string{KeysDir()}
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs, wd)
	}
	if s.Review != nil && s.Review.SignerPath != "" {
		dirs = append(dirs, filepath.Dir(s.Review.SignerPath))
	}
	seen := map[string]bool{}
	var out []keyInfo
	for _, d := range dirs {
		files, _ := filepath.Glob(filepath.Join(d, "*.key"))
		for _, f := range files {
			if abs, err := filepath.Abs(f); err == nil {
				f = abs
			}
			if seen[f] {
				continue
			}
			seen[f] = true
			k, err := sign.LoadPrivate(f)
			if err != nil {
				continue // not a Flightbay signing key
			}
			ki := keyInfo{Path: f, Name: k.Signer, KeyID: k.ID()}
			if pub := strings.TrimSuffix(f, ".key") + ".pub"; fileExists(pub) {
				ki.Public = pub
			}
			out = append(out, ki)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name+out[i].Path < out[j].Name+out[j].Path })
	return out
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func (s *Server) signerInfo() map[string]any {
	if s.Review == nil || s.Review.Options.Signer == nil {
		return nil
	}
	k := s.Review.Options.Signer
	return map[string]any{"name": k.Signer, "keyId": k.ID(), "path": s.Review.SignerPath}
}

// reviewKeys lists signing keys (GET) or chooses, creates or clears the one
// exports are signed with (POST {path} | {create: {name}} | {none: true}).
func (s *Server) reviewKeys(w http.ResponseWriter, r *http.Request) {
	if err := s.ensureReview(); err != nil {
		jerr(w, http.StatusNotFound, err.Error())
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, 200, map[string]any{"signer": s.signerInfo(), "keys": s.findKeys(), "keysDir": KeysDir()})
		return
	}
	if r.Method != http.MethodPost || !sameOrigin(r) {
		jerr(w, http.StatusMethodNotAllowed, "POST from the desk")
		return
	}
	var req struct {
		Path   string `json:"path"`
		None   bool   `json:"none"`
		Create *struct {
			Name string `json:"name"`
		} `json:"create"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		jerr(w, 400, err.Error())
		return
	}
	resp := map[string]any{}
	switch {
	case req.None:
		s.Review.Options.Signer, s.Review.SignerPath = nil, ""
	case req.Create != nil:
		name := strings.TrimSpace(req.Create.Name)
		if name == "" {
			jerr(w, 400, "give the key a name: who signs with it, for example \"ACME platform team\"")
			return
		}
		k, err := sign.Generate(name)
		if err != nil {
			jerr(w, 500, err.Error())
			return
		}
		if err := os.MkdirAll(KeysDir(), 0o700); err != nil {
			jerr(w, 500, err.Error())
			return
		}
		base := filepath.Join(KeysDir(), slug(name))
		for i := 2; fileExists(base+".key") || fileExists(base+".pub"); i++ {
			base = filepath.Join(KeysDir(), slug(name)+"-"+itoa(i))
		}
		if err := os.WriteFile(base+".key", k.MarshalPrivate(), 0o600); err != nil {
			jerr(w, 500, err.Error())
			return
		}
		pub := k.Public().MarshalPublic()
		if err := os.WriteFile(base+".pub", pub, 0o644); err != nil {
			jerr(w, 500, err.Error())
			return
		}
		s.Review.Options.Signer, s.Review.SignerPath = k, base+".key"
		resp["created"] = map[string]any{"path": base + ".key", "publicPath": base + ".pub", "publicKey": string(pub)}
	default:
		p := strings.TrimSpace(req.Path)
		if strings.HasPrefix(p, "~/") {
			h, _ := os.UserHomeDir()
			p = filepath.Join(h, p[2:])
		}
		k, err := sign.LoadPrivate(p)
		if err != nil {
			jerr(w, 400, "not a Flightbay signing key: "+err.Error())
			return
		}
		s.Review.Options.Signer, s.Review.SignerPath = k, p
	}
	resp["signer"] = s.signerInfo()
	resp["keys"] = s.findKeys()
	writeJSON(w, 200, resp)
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	out := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if out == "" {
		return "flightbay-signing"
	}
	return out
}

func itoa(i int) string {
	const d = "0123456789"
	if i < 10 {
		return d[i : i+1]
	}
	return itoa(i/10) + d[i%10:i%10+1]
}
