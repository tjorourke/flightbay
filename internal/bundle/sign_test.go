package bundle

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/tjorourke/flightbay/internal/sign"
)

// repack writes members as a bundle stream exactly as given, bypassing the
// Writer's rules, to build tampered bundles.
func repack(t *testing.T, ms []Member) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw, _ := zstd.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, m := range ms {
		if err := tw.WriteHeader(&tar.Header{Name: m.Name, Mode: 0o644, Size: int64(len(m.Data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write(m.Data)
	}
	_ = tw.Close()
	_ = zw.Close()
	return buf.Bytes()
}

func TestSignedBundle(t *testing.T) {
	k, _ := sign.Generate("ACME platform team")
	data := buildBundle(t, WriterOptions{Signer: k})
	ms, r, err := readAll(t, data, ReaderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Verify(); err != nil {
		t.Fatalf("a signed bundle verifies: %v", err)
	}
	if last := ms[len(ms)-1].Name; last != SignatureName {
		t.Fatalf("manifest.sig is last, got %s", last)
	}
	res := r.Signature([]sign.PublicKey{k.Public()})
	if !res.Valid || !res.Trusted || res.KeyID != k.ID() {
		t.Fatalf("signature: %+v", res)
	}
	if r.ManifestSHA256() == "" {
		t.Fatal("manifest hash")
	}

	// Unsigned bundles read as unsigned.
	_, r2, _ := readAll(t, buildBundle(t, WriterOptions{}), ReaderOptions{})
	if r2.Signature(nil).Signed {
		t.Fatal("unsigned")
	}

	// Drop the log member and its manifest entry: the checksums still agree,
	// but the signature no longer matches the manifest.
	var tampered []Member
	for _, m := range ms {
		switch {
		case strings.Contains(m.Name, "/logs/"):
			continue
		case m.Name == ManifestName:
			var man map[string]any
			_ = json.Unmarshal(m.Data, &man)
			for name := range man["members"].(map[string]any) {
				if strings.Contains(name, "/logs/") {
					delete(man["members"].(map[string]any), name)
				}
			}
			m.Data, _ = json.MarshalIndent(man, "", "  ")
		}
		tampered = append(tampered, m)
	}
	_, r3, err := readAll(t, repack(t, tampered), ReaderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := r3.Verify(); err != nil {
		t.Fatalf("the checksums alone cannot see this: %v", err)
	}
	if res := r3.Signature(nil); res.Valid || !res.Signed {
		t.Fatalf("the signature catches a rewritten manifest: %+v", res)
	}

	// Nothing may follow the signature, and it may not come before the manifest.
	extra := append(append([]Member{}, ms...), Member{Name: "late.json", Data: []byte("{}")})
	if _, _, err := readAll(t, repack(t, extra), ReaderOptions{}); err == nil {
		t.Fatal("member after manifest.sig")
	}
	early := append([]Member{}, ms[0], ms[len(ms)-1])
	if _, _, err := readAll(t, repack(t, early), ReaderOptions{}); err == nil {
		t.Fatal("manifest.sig before the manifest")
	}
}
