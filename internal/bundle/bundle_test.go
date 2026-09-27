package bundle

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/klauspost/compress/zstd"
)

func header() Header {
	return Header{BundleID: "b-1", Case: "01234567", CaptureStart: time.Date(2026, 9, 27, 10, 10, 0, 0, time.UTC), Contexts: []string{"mesh1"}}
}

func buildBundle(t *testing.T, opts WriterOptions) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := NewWriter(&buf, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteHeader(header()); err != nil {
		t.Fatal(err)
	}
	pods := ResourcePath("mesh1", "", "v1", "pods", 1)
	if err := w.Add(pods, []byte(`{"kind":"List","items":[{"kind":"Pod","metadata":{"name":"a"}}]}`)); err != nil {
		t.Fatal(err)
	}
	logs := LogPath("mesh1", "payments", "a", "app", "current", 1)
	if err := w.Add(logs, []byte("2026-09-27T10:00:00Z hello\n")); err != nil {
		t.Fatal(err)
	}
	if err := w.Coverage(Coverage{ID: "res-v1-pods", Cluster: "mesh1", Type: CoverageResource, Resource: "pods", Version: "v1", Chunks: []string{pods}, Items: 1, State: StateComplete}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(Manifest{Complete: true}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func readAll(t *testing.T, data []byte, opts ReaderOptions) ([]Member, *Reader, error) {
	t.Helper()
	r, err := NewReader(bytes.NewReader(data), opts)
	if err != nil {
		return nil, nil, err
	}
	var out []Member
	for {
		m, err := r.Next()
		if err == io.EOF {
			return out, r, nil
		}
		if err != nil {
			return out, r, err
		}
		out = append(out, *m)
	}
}

func TestRoundTripOrderingAndVerification(t *testing.T) {
	data := buildBundle(t, WriterOptions{Compression: "best"})
	members, r, err := readAll(t, data, ReaderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if members[0].Name != HeaderName {
		t.Fatalf("header not first: %s", members[0].Name)
	}
	if last := members[len(members)-1].Name; last != ManifestName {
		t.Fatalf("manifest not last: %s", last)
	}
	var h Header
	if err := json.Unmarshal(members[0].Data, &h); err != nil || h.SchemaVersion != SchemaVersion || h.Case != "01234567" {
		t.Fatalf("bad header %+v %v", h, err)
	}
	if err := r.Verify(); err != nil {
		t.Fatalf("verify: %v", err)
	}
	m := r.Manifest()
	if m == nil || !m.Complete || len(m.Members) != len(members)-1 || m.CaptureEnd.IsZero() {
		t.Fatalf("bad manifest %+v", m)
	}
}

func TestHeaderMustComeFirst(t *testing.T) {
	w, _ := NewWriter(io.Discard, WriterOptions{})
	if err := w.Add("clusters/a/x.json", []byte("{}")); err == nil {
		t.Fatal("expected error adding before header")
	}
}

func TestRejectsBadMemberNames(t *testing.T) {
	w, _ := NewWriter(io.Discard, WriterOptions{})
	_ = w.WriteHeader(header())
	for _, n := range []string{"../evil", "/abs", "clusters/../../x", HeaderName, ManifestName} {
		if err := w.Add(n, nil); err == nil {
			t.Fatalf("expected rejection of %q", n)
		}
	}
}

func TestStreamingReadBeforeWriterFinishes(t *testing.T) {
	pr, pw := io.Pipe()
	w, err := NewWriter(pw, WriterOptions{Compression: "best"})
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 4)
	errc := make(chan error, 1)
	go func() {
		r, err := NewReader(pr, ReaderOptions{})
		if err != nil {
			errc <- err
			return
		}
		for {
			m, err := r.Next()
			if err != nil {
				if err != io.EOF {
					errc <- err
				}
				close(got)
				return
			}
			got <- m.Name
		}
	}()
	if err := w.WriteHeader(header()); err != nil {
		t.Fatal(err)
	}
	first := ResourcePath("mesh1", "", "v1", "pods", 1)
	if err := w.Add(first, []byte(`{"items":[]}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{HeaderName, first} {
		select {
		case name := <-got:
			if name != want {
				t.Fatalf("got %s want %s", name, want)
			}
		case err := <-errc:
			t.Fatal(err)
		case <-time.After(5 * time.Second):
			t.Fatalf("%s was not readable before the writer closed", want)
		}
	}
	if err := w.Close(Manifest{Complete: true}); err != nil {
		t.Fatal(err)
	}
	_ = pw.Close()
	for range got {
	}
}

func rawBundle(t *testing.T, members map[string]string, order []string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw, _ := zstd.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, n := range order {
		body := members[n]
		_ = tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	_ = zw.Close()
	return buf.Bytes()
}

func TestVerifyDetectsTampering(t *testing.T) {
	hb, _ := json.Marshal(Header{SchemaVersion: SchemaVersion, BundleID: "b"})
	mb, _ := json.Marshal(Manifest{SchemaVersion: SchemaVersion, BundleID: "b", Complete: true,
		Members: map[string]string{HeaderName: sha(hb), "clusters/a/x.json": sha([]byte("original"))}})
	data := rawBundle(t, map[string]string{HeaderName: string(hb), "clusters/a/x.json": "tampered", ManifestName: string(mb)},
		[]string{HeaderName, "clusters/a/x.json", ManifestName})
	_, r, err := readAll(t, data, ReaderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Verify(); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("expected checksum failure, got %v", err)
	}
}

func TestVerifyDetectsUnlistedMember(t *testing.T) {
	hb, _ := json.Marshal(Header{SchemaVersion: SchemaVersion, BundleID: "b"})
	mb, _ := json.Marshal(Manifest{SchemaVersion: SchemaVersion, BundleID: "b", Members: map[string]string{HeaderName: sha(hb)}})
	data := rawBundle(t, map[string]string{HeaderName: string(hb), "clusters/a/extra.json": "x", ManifestName: string(mb)},
		[]string{HeaderName, "clusters/a/extra.json", ManifestName})
	_, r, _ := readAll(t, data, ReaderOptions{})
	if err := r.Verify(); err == nil || !strings.Contains(err.Error(), "not listed") {
		t.Fatalf("expected unlisted member error, got %v", err)
	}
}

func TestReaderRejectsTraversalAndMissingHeader(t *testing.T) {
	data := rawBundle(t, map[string]string{"../evil": "x"}, []string{"../evil"})
	if _, _, err := readAll(t, data, ReaderOptions{}); err == nil {
		t.Fatal("expected unsafe path error")
	}
	hb, _ := json.Marshal(Header{SchemaVersion: SchemaVersion})
	data = rawBundle(t, map[string]string{"clusters/a/x.json": "x", HeaderName: string(hb)}, []string{"clusters/a/x.json", HeaderName})
	if _, _, err := readAll(t, data, ReaderOptions{}); err == nil || !strings.Contains(err.Error(), HeaderName) {
		t.Fatalf("expected header-first error, got %v", err)
	}
}

func TestTruncatedBundleFailsVerification(t *testing.T) {
	data := buildBundle(t, WriterOptions{})
	_, r, err := readAll(t, data[:len(data)/2], ReaderOptions{})
	if err == nil {
		err = r.Verify()
	}
	if err == nil {
		t.Fatal("truncated bundle accepted")
	}
}

func TestMemberSizeLimit(t *testing.T) {
	data := buildBundle(t, WriterOptions{})
	if _, _, err := readAll(t, data, ReaderOptions{MaxMemberBytes: 10}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("expected size limit error, got %v", err)
	}
}

func TestAgeEncryption(t *testing.T) {
	id, _ := age.GenerateX25519Identity()
	data := buildBundle(t, WriterOptions{Recipients: []age.Recipient{id.Recipient()}})
	if bytes.Contains(data, []byte("mesh1")) || !bytes.HasPrefix(data, []byte("age-encryption.org/v1")) {
		t.Fatal("bundle is not encrypted")
	}
	if _, _, err := readAll(t, data, ReaderOptions{}); err == nil || !strings.Contains(err.Error(), "encrypted") {
		t.Fatalf("expected clear encrypted error, got %v", err)
	}
	_, r, err := readAll(t, data, ReaderOptions{Identities: []age.Identity{id}})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Verify(); err != nil {
		t.Fatal(err)
	}
}

func TestWindowFitsStockTools(t *testing.T) {
	data := buildBundle(t, WriterOptions{Compression: "best"})
	d, err := zstd.NewReader(bytes.NewReader(data), zstd.WithDecoderMaxWindow(MaxWindow))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, d); err != nil {
		t.Fatalf("decoder capped at %d rejected the bundle: %v", MaxWindow, err)
	}
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skip("zstd CLI not installed")
	}
	f := filepath.Join(t.TempDir(), "b.tar.zst")
	_ = os.WriteFile(f, data, 0o600)
	out, err := exec.Command("sh", "-c", "zstd -dc "+f+" | tar -tf -").CombinedOutput()
	if err != nil || !strings.Contains(string(out), HeaderName) {
		t.Fatalf("stock zstd/tar failed: %v %s", err, out)
	}
}

func TestPathHelpersRoundTrip(t *testing.T) {
	p := ResourcePath("mesh1", "gateway.networking.k8s.io", "v1", "httproutes", 3)
	info, ok := ParsePath(p)
	if !ok || info.Kind != PathResource || info.Cluster != "mesh1" || info.Group != "gateway.networking.k8s.io" || info.Resource != "httproutes" || info.Part != 3 {
		t.Fatalf("bad parse %+v", info)
	}
	l := LogPath("mesh1", "payments", "checkout-1", "app", "previous", 1)
	info, ok = ParsePath(l)
	if !ok || info.Kind != PathLog || info.Namespace != "payments" || info.Pod != "checkout-1" || info.Container != "app" || info.Stream != "previous" {
		t.Fatalf("bad log parse %+v", info)
	}
	if _, ok := ParsePath(errors.New("x").Error()); ok {
		t.Fatal("parsed garbage")
	}
}
