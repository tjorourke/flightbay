package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestHTTPProbeSendsProfileAndRedactsResults(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Method != "POST" || r.Host != "global.app.mesh.internal" || r.Header.Get("X-Customer") != "demo" || r.Header.Get("Authorization") != "Bearer secret-fixture" || r.Header.Get("X-Flightbay-Test-ID") == "" {
			t.Errorf("profile not sent correctly")
		}
		w.Header().Set("Location", "/should-not-follow")
		w.Header().Set("Set-Cookie", "session=private")
		w.WriteHeader(302)
		fmt.Fprint(w, "received Bearer secret-fixture")
	}))
	defer srv.Close()
	p := httpProfile{Method: "POST", Path: "/", Timeout: 2, Headers: map[string]string{"Authorization": "Bearer secret-fixture", "X-Customer": "demo"}}
	r := runHTTPProbe(context.Background(), p, srv.URL, "global.app.mesh.internal", "public")
	if hits.Load() != 1 || r.Status != 302 || r.ResponseHeaders["Set-Cookie"] != "[redacted]" || strings.Contains(r.Body, "secret-fixture") {
		t.Fatalf("redirect followed or secrets exposed: %+v", r)
	}
	data, _ := json.Marshal(r)
	if strings.Contains(string(data), "secret-fixture") {
		t.Fatal("authorization value leaked into results")
	}
}

func TestHTTPProfilesStayPrivateAndSeparateFromClusterChangeRecords(t *testing.T) {
	s, _, srv, _ := ambientFixture(t)
	p := httpProfile{Name: "Customer headers", Method: "GET", Path: "/", Headers: map[string]string{"Authorization": "Bearer private-profile-value"}, Timeout: 5}
	code, _ := ambientPostPath(t, srv, "/api/ambient/http-profiles", p)
	if code != 200 {
		t.Fatalf("profile save: HTTP %d", code)
	}
	files, err := filepath.Glob(filepath.Join(s.AmbientStateDir, "http-profiles", "*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("profile storage: %v %v", files, err)
	}
	st, err := os.Stat(files[0])
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatal("profile file is not owner-only")
	}
	if err := s.ambientRecords(); err != nil {
		t.Fatalf("profiles broke change-record recovery: %v", err)
	}
	request, _ := http.NewRequest("GET", srv.URL+"/api/ambient/http-profiles", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("profiles were available without the session token")
	}
}
func TestHTTPProbeBoundsResponseAndValidatesHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, strings.Repeat("x", 40000)) }))
	defer srv.Close()
	r := runHTTPProbe(context.Background(), httpProfile{Method: "GET", Timeout: 2}, srv.URL, "app.example", "public")
	if !r.Truncated || len(r.Body) != 32768 {
		t.Fatalf("response not bounded: %d", len(r.Body))
	}
	for _, p := range []httpProfile{{Method: "GET HEAD", Path: "/"}, {Method: "GET", Path: "//other.host"}, {Method: "GET", Path: "/", Headers: map[string]string{"X-Test": "a\r\nHost: other"}}, {Method: "GET", Path: "/", Headers: map[string]string{"Content-Length": "999"}}} {
		if validateHTTPProfile(&p) == nil {
			t.Fatalf("invalid profile accepted: %+v", p)
		}
	}
}
