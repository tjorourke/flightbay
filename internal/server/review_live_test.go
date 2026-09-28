package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjorourke/flightbay/internal/sign"
)

// A live mirror has no bundle file: the first visit to Review and send
// freezes it (FreezeBundle), and the page can choose or create a signing key.
func TestReviewLiveFreezeAndKeys(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FLIGHTBAY_HOME", filepath.Join(dir, "home"))
	src := filepath.Join(dir, "frozen-source.tar.zst")
	smallBundle(t, src)
	ws := importFile(t, src, nil)
	freezes := 0
	exportDir := filepath.Join(dir, "out")
	s := &Server{WS: ws, Token: tok, WorkDir: dir, ExportDir: exportDir,
		FreezeBundle: func(d string) (string, error) {
			freezes++
			_ = os.MkdirAll(d, 0o700)
			dst := filepath.Join(d, "flightbay-live-test.tar.zst")
			b, _ := os.ReadFile(src)
			return dst, os.WriteFile(dst, b, 0o600)
		}}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(func() { srv.Close(); s.Close() })
	post := func(path string, body any, v any) int {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", srv.URL+path, bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Origin", srv.URL)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		_ = json.NewDecoder(resp.Body).Decode(v)
		return resp.StatusCode
	}

	var wsInfo map[string]any
	get(t, srv, "/api/workspace", &wsInfo)
	if wsInfo["exportable"] != true {
		t.Fatalf("a live mirror is exportable: %v", wsInfo["exportable"])
	}
	var st map[string]any
	if code := get(t, srv, "/api/review", &st); code != 200 || st["live"] != true || freezes != 1 {
		t.Fatalf("first visit freezes the mirror: code %d freezes %d %v", code, freezes, st["live"])
	}
	if !strings.HasPrefix(st["out"].(string), exportDir) || !strings.HasSuffix(st["out"].(string), "-reviewed.tar.zst") {
		t.Fatalf("export goes to the export dir: %v", st["out"])
	}
	if st["signer"] != nil {
		t.Fatalf("no signer yet: %v", st["signer"])
	}

	// Create a key in the UI.
	var created struct {
		Signer  map[string]any `json:"signer"`
		Created map[string]any `json:"created"`
		Keys    []keyInfo      `json:"keys"`
	}
	if code := post("/api/review/keys", map[string]any{"create": map[string]string{"name": "ACME platform team"}}, &created); code != 200 {
		t.Fatalf("create key: %d", code)
	}
	priv := created.Created["path"].(string)
	if fi, err := os.Stat(priv); err != nil || fi.Mode().Perm() != 0o600 || !strings.HasPrefix(priv, KeysDir()) {
		t.Fatalf("private key saved 0600 in the keys dir: %s %v", priv, err)
	}
	if !strings.Contains(created.Created["publicKey"].(string), "ACME platform team") || created.Signer["name"] != "ACME platform team" || len(created.Keys) != 1 {
		t.Fatalf("created: %+v", created)
	}

	// Choose another key by path, then none.
	other, _ := sign.Generate("Other")
	otherPath := filepath.Join(dir, "other.key")
	_ = os.WriteFile(otherPath, other.MarshalPrivate(), 0o600)
	var chosen struct{ Signer map[string]any }
	if code := post("/api/review/keys", map[string]string{"path": otherPath}, &chosen); code != 200 || chosen.Signer["keyId"] != other.ID() {
		t.Fatalf("choose key: %d %+v", code, chosen)
	}
	if code := post("/api/review/keys", map[string]string{"path": filepath.Join(dir, "missing.key")}, &map[string]any{}); code != 400 {
		t.Fatalf("a missing key is refused: %d", code)
	}
	if code := post("/api/review/keys", map[string]bool{"none": true}, &chosen); code != 200 || chosen.Signer != nil {
		t.Fatalf("clear signer: %d %+v", code, chosen)
	}

	// Freezing again replaces the frozen copy and keeps the session.
	if code := post("/api/review/refreeze", map[string]any{}, &map[string]any{}); code != 200 || freezes != 2 {
		t.Fatalf("refreeze: %d freezes %d", code, freezes)
	}
}
