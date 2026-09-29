package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDeskRegistry(t *testing.T) {
	t.Setenv("FLIGHTBAY_HOME", t.TempDir())
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
		}
	}))
	defer up.Close()
	// An address nothing listens on, like a desk that was killed.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := "http://" + ln.Addr().String()
	ln.Close()

	undead, err := registerDesk(deskInfo{URL: dead, Token: "x", WorkDir: t.TempDir(), Bundle: "old.tar.zst", PID: 1})
	if err != nil {
		t.Fatal(err)
	}
	_ = undead // killed: never cleaned up
	cleanup, err := registerDesk(deskInfo{URL: up.URL, Token: "tok", WorkDir: t.TempDir(), Bundle: "live:kind-mesh1", PID: 2})
	if err != nil {
		t.Fatal(err)
	}
	// current.json now names desk 2; make it stale like the reported case.
	stale, _ := os.ReadFile(filepath.Join(desksDir(), "1.json"))
	_ = os.WriteFile(filepath.Join(home(), "current.json"), stale, 0o600)

	ds := openDesks()
	if len(ds) != 1 || ds[0].URL != up.URL {
		t.Fatalf("open desks: %+v", ds)
	}
	if _, err := os.Stat(filepath.Join(desksDir(), "1.json")); !os.IsNotExist(err) {
		t.Fatal("entry for a desk that no longer listens was kept")
	}
	if d, err := currentDesk(); err != nil || d.URL != up.URL {
		t.Fatalf("currentDesk = %+v, %v; want the live desk, not the stale current.json", d, err)
	}

	cleanup()
	if _, err := os.Stat(filepath.Join(desksDir(), "2.json")); !os.IsNotExist(err) {
		t.Fatal("entry kept after the desk closed")
	}
	if b, _ := os.ReadFile(filepath.Join(home(), "current.json")); string(b) != string(stale) {
		t.Fatal("closing desk 2 changed a current.json that named another desk")
	}
	if _, err := currentDesk(); err == nil {
		t.Fatal("currentDesk found a desk after every desk closed")
	}

	// Two desks: closing the newer hands current.json to the older.
	c3, _ := registerDesk(deskInfo{URL: up.URL, Token: "tok", WorkDir: t.TempDir(), Bundle: "a.tar.zst", PID: 3})
	c4, _ := registerDesk(deskInfo{URL: up.URL, Token: "tok", WorkDir: t.TempDir(), Bundle: "b.tar.zst", PID: 4})
	c4()
	three, _ := os.ReadFile(filepath.Join(desksDir(), "3.json"))
	if b, _ := os.ReadFile(filepath.Join(home(), "current.json")); string(b) != string(three) {
		t.Fatalf("current.json after closing the newer desk: %s", b)
	}
	c3()
}
