package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tjorourke/flightbay/internal/importer"
	"github.com/tjorourke/flightbay/internal/store"
)

// Browser preview from an explicit local fixture. This test harness never
// exposes cluster mutations and does not build/install a distributable binary.
func TestAmbientBrowserPreview(t *testing.T) {
	path := os.Getenv("FLIGHTBAY_AMBIENT_PREVIEW")
	if path == "" {
		t.Skip("set a local bundle path to preview the source UI")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ws := store.New()
	defer ws.Close()
	if err := importer.Run(context.Background(), f, ws, importer.Options{}); err != nil {
		t.Fatal(err)
	}
	s := &Server{WS: ws, Token: "ambient-ui-test", WorkDir: t.TempDir(), AmbientStateDir: t.TempDir()}
	defer s.Close()
	h := s.Handler()
	webRoot := filepath.Join("..", "..", "web")
	static := http.StripPrefix("/static/", http.FileServer(http.Dir(webRoot)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/ambient/test" || r.URL.Path == "/api/ambient/http-test" || r.URL.Path == "/api/ambient/http-mesh-test" {
			jerr(w, 403, "browser preview: real cluster actions are disabled")
			return
		}
		if s.authed(r) {
			w.Header().Set("Cache-Control", "no-store")
			if strings.HasPrefix(r.URL.Path, "/static/") {
				static.ServeHTTP(w, r)
				return
			}
			if r.URL.Path == "/" && r.URL.RawQuery == "" {
				http.ServeFile(w, r, filepath.Join(webRoot, "index.html"))
				return
			}
		}
		h.ServeHTTP(w, r)
	}))
	defer srv.Close()
	fmt.Printf("AMBIENT_PREVIEW=%s/?token=ambient-ui-test#ambient\n", srv.URL)
	select {
	case <-time.After(15 * time.Minute):
	case <-t.Context().Done():
	}
}
