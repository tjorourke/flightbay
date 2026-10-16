package sign

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSignVerify(t *testing.T) {
	k, err := Generate("ACME platform team")
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"members":{"a":"1"}}`)
	sig := k.Sign(PurposeBundle, data)
	parsed, err := Parse(sig.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	if r := Verify(parsed, PurposeBundle, data, nil); !r.Valid || r.Trusted || r.Signer != "ACME platform team" {
		t.Fatalf("valid, untrusted: %+v", r)
	}
	if r := Verify(parsed, PurposeBundle, data, []PublicKey{k.Public()}); !r.Trusted {
		t.Fatalf("trusted with its own key: %+v", r)
	}
	other, _ := Generate("someone else")
	if r := Verify(parsed, PurposeBundle, data, []PublicKey{other.Public()}); !r.Valid || r.Trusted {
		t.Fatalf("a different trusted key does not vouch for it: %+v", r)
	}
	if r := Verify(parsed, PurposeBundle, []byte(`{"members":{"a":"2"}}`), nil); r.Valid || !strings.Contains(r.Error, "changed") {
		t.Fatalf("tampered content: %+v", r)
	}
	if r := Verify(parsed, PurposePolicy, data, nil); r.Valid {
		t.Fatal("a bundle signature must not verify as a policy signature")
	}
	forged := parsed
	forged.Signer = "External signer"
	if r := Verify(forged, PurposeBundle, data, nil); r.Valid {
		t.Fatal("the signer name is covered by the signature")
	}
	swapped := parsed
	swapped.PublicKey, swapped.KeyID = other.Sign(PurposeBundle, data).PublicKey, other.ID()
	if r := Verify(swapped, PurposeBundle, data, nil); r.Valid {
		t.Fatal("swapping in another public key must fail")
	}
}

func TestKeyFiles(t *testing.T) {
	dir := t.TempDir()
	k, _ := Generate("ACME")
	priv, pub := filepath.Join(dir, "acme.key"), filepath.Join(dir, "acme.pub")
	if err := os.WriteFile(priv, k.MarshalPrivate(), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPrivate(priv); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("a world-readable private key is refused: %v", err)
	}
	_ = os.Chmod(priv, 0o600)
	k2, err := LoadPrivate(priv)
	if err != nil || k2.ID() != k.ID() || k2.Signer != "ACME" {
		t.Fatalf("round trip: %v %v", err, k2)
	}
	_ = os.WriteFile(pub, k.Public().MarshalPublic(), 0o644)
	keys, err := LoadPublic(dir)
	if err != nil || len(keys) != 1 || keys[0].ID() != k.ID() || keys[0].Signer != "ACME" {
		t.Fatalf("directory of .pub files: %v %+v", err, keys)
	}
	if _, err := ParsePublic(k.MarshalPrivate()); err == nil {
		t.Fatal("a private key is not accepted as a public key")
	}
	if _, err := ParsePrivate(k.Public().MarshalPublic()); err == nil {
		t.Fatal("a public key cannot sign")
	}
	if _, err := Generate(" "); err == nil {
		t.Fatal("a signer name is required")
	}
}
