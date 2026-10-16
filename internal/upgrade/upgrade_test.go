package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func archive(t *testing.T, name string) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	data := []byte("new " + name)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func fixture(t *testing.T, badDesk bool) (updater, string, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"flightbay", "flightbay-desk"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("old "+name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	assets := map[string][]byte{}
	for _, name := range []string{"flightbay", "flightbay-desk"} {
		file := name + "_0.2.5_darwin_arm64.tar.gz"
		data := archive(t, name)
		sum := []byte(fmt.Sprintf("%x  %s\n", sha256.Sum256(data), file))
		assets[file] = data
		if name == "flightbay" {
			assets["checksums-flightbay.txt"] = sum
		} else {
			assets[file+".sha256"] = sum
			if badDesk {
				assets[file] = []byte("tampered desk archive")
			}
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/releases/") {
			fmt.Fprint(w, `{"tag_name":"v0.2.5","draft":false}`)
			return
		}
		if b, ok := assets[filepath.Base(r.URL.Path)]; ok {
			_, _ = w.Write(b)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	out := &bytes.Buffer{}
	return updater{client: srv.Client(), api: srv.URL, downloads: srv.URL + "/downloads", goos: "darwin", arch: "arm64", exe: filepath.Join(dir, "flightbay"), out: out}, dir, out
}

func TestUpgradePreservesSymlinkAndUpdatesBothBinaries(t *testing.T) {
	u, dir, out := fixture(t, false)
	link := filepath.Join(t.TempDir(), "flightbay")
	if err := os.Symlink(u.exe, link); err != nil {
		t.Skip(err)
	}
	u.exe = link
	if err := u.run(context.Background(), "0.2.4", Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Readlink(link); err != nil {
		t.Fatalf("upgrade replaced the symlink: %v", err)
	}
	for _, name := range []string{"flightbay", "flightbay-desk"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(b) != "new "+name {
			t.Fatalf("%s: %q %v", name, b, err)
		}
		st, _ := os.Stat(filepath.Join(dir, name))
		if st.Mode().Perm()&0o111 == 0 {
			t.Fatalf("%s is not executable", name)
		}
	}
	if !strings.Contains(out.String(), "v0.2.5") {
		t.Fatal(out.String())
	}
	left, _ := filepath.Glob(filepath.Join(dir, ".flightbay-*"))
	if len(left) != 0 {
		t.Fatalf("temporary files not cleaned: %v", left)
	}
}

func TestBadDeskChecksumLeavesBothInstalledBinariesUntouched(t *testing.T) {
	u, dir, _ := fixture(t, true)
	err := u.run(context.Background(), "0.2.4", Options{})
	if err == nil || !strings.Contains(err.Error(), "checksum verification failed") {
		t.Fatalf("wanted a checksum failure: %v", err)
	}
	for _, name := range []string{"flightbay", "flightbay-desk"} {
		b, _ := os.ReadFile(filepath.Join(dir, name))
		if string(b) != "old "+name {
			t.Fatalf("%s changed on failed verification", name)
		}
	}
	left, _ := filepath.Glob(filepath.Join(dir, ".flightbay-*"))
	if len(left) != 0 {
		t.Fatalf("failed upgrade left temporary files: %v", left)
	}
}

func TestCheckDoesNotReplaceInstalledFiles(t *testing.T) {
	u, dir, _ := fixture(t, true)
	if err := u.run(context.Background(), "0.2.4", Options{Check: true}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "flightbay"))
	if string(b) != "old flightbay" {
		t.Fatal("--check changed the installed binary")
	}
}

func TestCollectorOnlyDoesNotDownloadOrReplaceDesk(t *testing.T) {
	u, dir, _ := fixture(t, true)
	if err := u.run(context.Background(), "0.2.4", Options{CollectorOnly: true}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "flightbay-desk"))
	if string(b) != "old flightbay-desk" {
		t.Fatal("--collector-only changed the desk")
	}
}

func TestHomebrewInstallationIsNotOverwritten(t *testing.T) {
	u, dir, _ := fixture(t, false)
	cellar := filepath.Join(dir, "Cellar", "flightbay", "0.2.4", "bin")
	if err := os.MkdirAll(cellar, 0o755); err != nil {
		t.Fatal(err)
	}
	u.exe = filepath.Join(cellar, "flightbay")
	if err := os.WriteFile(u.exe, []byte("brew binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := u.run(context.Background(), "0.2.4", Options{})
	if err == nil || !strings.Contains(err.Error(), "brew upgrade flightbay") {
		t.Fatalf("expected Homebrew instructions: %v", err)
	}
	b, _ := os.ReadFile(u.exe)
	if string(b) != "brew binary" {
		t.Fatal("Homebrew-managed binary changed")
	}
}

func TestRejectInvalidVersionAndChecksum(t *testing.T) {
	u, _, _ := fixture(t, false)
	if err := u.run(context.Background(), "dev", Options{Version: "../../main"}); err == nil {
		t.Fatal("invalid release tag accepted")
	}
	if _, err := checksum([]byte("not-a-hash  flightbay.tar.gz"), "flightbay.tar.gz"); err == nil {
		t.Fatal("invalid checksum accepted")
	}
}
