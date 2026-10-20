package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/tjorourke/flightbay/internal/importer"
	"github.com/tjorourke/flightbay/internal/store"
)

// Real-data check, run only when a bundle path is supplied.
func TestAmbientServicesBundle(t *testing.T) {
	path := os.Getenv("FLIGHTBAY_AMBIENT_SERVICES_BUNDLE")
	if path == "" {
		t.Skip("set FLIGHTBAY_AMBIENT_SERVICES_BUNDLE to a bundle path")
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
	s := &Server{WS: ws}
	rec := httptest.NewRecorder()
	s.ambientServices(rec, httptest.NewRequest("GET", "/api/ambient/services?namespace=bookinfo", nil))
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var v map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	b, _ := json.MarshalIndent(v, "", " ")
	if out := os.Getenv("FLIGHTBAY_AMBIENT_SERVICES_OUT"); out != "" {
		os.WriteFile(filepath.Clean(out), b, 0o644)
	}
}

func TestAmbientServicesEmptyWorkspace(t *testing.T) {
	ws := store.New()
	defer ws.Close()
	s := &Server{WS: ws}
	rec := httptest.NewRecorder()
	s.ambientServices(rec, httptest.NewRequest("GET", "/api/ambient/services", nil))
	if rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	rec = httptest.NewRecorder()
	s.ambientServices(rec, httptest.NewRequest("POST", "/api/ambient/services", nil))
	if rec.Code != 405 {
		t.Fatal(rec.Code)
	}
}
