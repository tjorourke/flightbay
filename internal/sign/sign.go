// Package sign creates and checks the Ed25519 signatures on Flightbay bundles
// and collection policies.
//
// The signer keeps the private key and shares only the public key. A
// bundle's manifest.json already carries a SHA-256 for every member, so one
// signature over the manifest covers the whole bundle. A collection policy
// is signed as a detached POLICY.sig file next to it.
package sign

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Purposes. The purpose is part of the signed message, so a policy signature
// can never be passed off as a bundle signature or the other way round.
const (
	PurposeBundle = "bundle-manifest"
	PurposePolicy = "collection-policy"
)

const (
	privTag   = "FLIGHTBAY-SIGNING-KEY-1"
	pubTag    = "flightbay-ed25519"
	algorithm = "ed25519"
)

// PrivateKey signs. Signer is the name recorded at keygen, e.g. "ACME platform team".
type PrivateKey struct {
	Signer string
	key    ed25519.PrivateKey
}

// PublicKey verifies.
type PublicKey struct {
	Signer string
	Key    ed25519.PublicKey
	Source string // file it was read from, for messages
}

// KeyID is a short, stable identifier derived from the public key.
func KeyID(pub ed25519.PublicKey) string {
	s := sha256.Sum256(pub)
	return "fb:" + hex.EncodeToString(s[:8])
}

// ID returns the key id of the public half.
func (k *PrivateKey) ID() string { return KeyID(k.Public().Key) }

// Public returns the public half.
func (k *PrivateKey) Public() PublicKey {
	return PublicKey{Signer: k.Signer, Key: k.key.Public().(ed25519.PublicKey)}
}

// ID returns the key id.
func (p PublicKey) ID() string { return KeyID(p.Key) }

// Generate makes a new key pair for signer.
func Generate(signer string) (*PrivateKey, error) {
	signer = strings.TrimSpace(signer)
	if signer == "" {
		return nil, errors.New("a signer name is required, e.g. --name \"ACME platform team\"")
	}
	if strings.ContainsAny(signer, "\r\n") {
		return nil, errors.New("the signer name must be one line")
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &PrivateKey{Signer: signer, key: priv}, nil
}

// MarshalPrivate renders the private key file.
func (k *PrivateKey) MarshalPrivate() []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "# Flightbay signing key (private). Keep this file secret; share only the .pub file.\n")
	fmt.Fprintf(&b, "# created: %s\n# signer: %s\n# key id: %s\n", time.Now().UTC().Format(time.RFC3339), k.Signer, k.ID())
	fmt.Fprintf(&b, "%s %s %s\n", privTag, base64.StdEncoding.EncodeToString(k.key.Seed()), k.Signer)
	return b.Bytes()
}

// MarshalPublic renders the public key file.
func (p PublicKey) MarshalPublic() []byte {
	return []byte(fmt.Sprintf("# Flightbay public key, key id %s. Share this with bundle recipients for verification.\n%s %s %s\n",
		p.ID(), pubTag, base64.StdEncoding.EncodeToString(p.Key), p.Signer))
}

// keyLine returns the tag, base64 key and trailing name of the first
// non-comment line.
func keyLine(data []byte) (tag, key, name string, err error) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, " ", 3)
		if len(parts) < 2 {
			return "", "", "", errors.New("malformed key line")
		}
		if len(parts) == 3 {
			name = strings.TrimSpace(parts[2])
		}
		return parts[0], parts[1], name, nil
	}
	return "", "", "", errors.New("no key found")
}

// ParsePrivate reads a private key file.
func ParsePrivate(data []byte) (*PrivateKey, error) {
	tag, key, name, err := keyLine(data)
	if err != nil {
		return nil, err
	}
	if tag == pubTag {
		return nil, errors.New("this is a public key; signing needs the private key file")
	}
	if tag != privTag {
		return nil, fmt.Errorf("not a Flightbay signing key (starts with %q)", tag)
	}
	seed, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("the private key is damaged")
	}
	return &PrivateKey{Signer: name, key: ed25519.NewKeyFromSeed(seed)}, nil
}

// ParsePublic reads a public key file.
func ParsePublic(data []byte) (PublicKey, error) {
	tag, key, name, err := keyLine(data)
	if err != nil {
		return PublicKey{}, err
	}
	if tag == privTag {
		return PublicKey{}, errors.New("this is a private key; verification needs only the .pub file (and the private key should not be shared)")
	}
	if tag != pubTag {
		return PublicKey{}, fmt.Errorf("not a Flightbay public key (starts with %q)", tag)
	}
	raw, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return PublicKey{}, errors.New("the public key is damaged")
	}
	return PublicKey{Signer: name, Key: ed25519.PublicKey(raw)}, nil
}

// LoadPrivate reads a private key from a file, refusing one other users can read.
func LoadPrivate(file string) (*PrivateKey, error) {
	if st, err := os.Stat(file); err == nil && st.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s can be read by other users (mode %o); run chmod 600 %s", file, st.Mode().Perm(), file)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	k, err := ParsePrivate(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return k, nil
}

// LoadPublic reads public keys from files. A directory contributes every *.pub in it.
func LoadPublic(paths ...string) ([]PublicKey, error) {
	var out []PublicKey
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		files := []string{p}
		if st.IsDir() {
			files, _ = filepath.Glob(filepath.Join(p, "*.pub"))
			sort.Strings(files)
		}
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				return nil, err
			}
			k, err := ParsePublic(b)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f, err)
			}
			k.Source = f
			out = append(out, k)
		}
	}
	return out, nil
}

// Signature is what a signature file or bundle member holds.
type Signature struct {
	Version   int       `json:"version"`
	Algorithm string    `json:"algorithm"`
	Purpose   string    `json:"purpose"`
	KeyID     string    `json:"keyId"`
	Signer    string    `json:"signer"`
	PublicKey string    `json:"publicKey"` // base64; lets a reader check integrity without the key file
	SignedAt  time.Time `json:"signedAt"`
	SHA256    string    `json:"sha256"` // of the signed content
	Value     string    `json:"signature"`
}

func message(purpose, keyID, signer string, at time.Time, sum string) []byte {
	return []byte(strings.Join([]string{"flightbay signature v1", purpose, keyID, signer, at.UTC().Format(time.RFC3339Nano), sum}, "\n"))
}

// Sign signs data for a purpose.
func (k *PrivateKey) Sign(purpose string, data []byte) Signature {
	sum := sha256.Sum256(data)
	s := Signature{Version: 1, Algorithm: algorithm, Purpose: purpose, KeyID: k.ID(), Signer: k.Signer,
		PublicKey: base64.StdEncoding.EncodeToString(k.Public().Key), SignedAt: time.Now().UTC().Truncate(time.Second), SHA256: hex.EncodeToString(sum[:])}
	s.Value = base64.StdEncoding.EncodeToString(ed25519.Sign(k.key, message(purpose, s.KeyID, s.Signer, s.SignedAt, s.SHA256)))
	return s
}

// Marshal renders a signature as indented JSON.
func (s Signature) Marshal() []byte {
	b, _ := json.MarshalIndent(s, "", "  ")
	return append(b, '\n')
}

// Parse reads a signature.
func Parse(b []byte) (Signature, error) {
	var s Signature
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("unreadable signature: %w", err)
	}
	if s.Version != 1 || s.Algorithm != algorithm {
		return s, fmt.Errorf("unsupported signature (version %d, %s)", s.Version, s.Algorithm)
	}
	return s, nil
}

// Result is the outcome of checking a signature.
type Result struct {
	Signed bool   `json:"signed"`
	Valid  bool   `json:"valid"` // the signature matches the content and the key it names
	KeyID  string `json:"keyId,omitempty"`
	Signer string `json:"signer,omitempty"`
	// Trusted is set when the key is one of the keys the reader was given
	// (--verify-key, trusted-keys). Valid but untrusted means the content is
	// unmodified since it was signed, by a key nobody here has vouched for.
	Trusted     bool      `json:"trusted"`
	TrustedFrom string    `json:"trustedFrom,omitempty"`
	SignedAt    time.Time `json:"signedAt,omitempty"`
	Error       string    `json:"error,omitempty"`
}

// Summary is one line for a terminal or a header bar.
func (r Result) Summary() string {
	switch {
	case !r.Signed:
		return "not signed"
	case !r.Valid:
		return "signature invalid: " + r.Error
	case r.Trusted:
		return fmt.Sprintf("signed by %s (%s), unmodified, key trusted", r.Signer, r.KeyID)
	default:
		return fmt.Sprintf("signed by %s (%s), unmodified; key not in your trusted keys", r.Signer, r.KeyID)
	}
}

// Verify checks sig against data for purpose. With trusted keys, the
// signature's key must be one of them for Trusted to be set; the signer name
// shown is then the one from the trusted key file, not the one in the
// signature.
func Verify(sig Signature, purpose string, data []byte, trusted []PublicKey) Result {
	r := Result{Signed: true, KeyID: sig.KeyID, Signer: sig.Signer, SignedAt: sig.SignedAt}
	fail := func(msg string) Result { r.Error = msg; return r }
	if sig.Purpose != purpose {
		return fail(fmt.Sprintf("this signature is for a %s, not a %s", sig.Purpose, purpose))
	}
	pub, err := base64.StdEncoding.DecodeString(sig.PublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return fail("the public key in the signature is damaged")
	}
	if KeyID(pub) != sig.KeyID {
		return fail("the key id does not match the public key")
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != sig.SHA256 {
		return fail("the content has changed since it was signed")
	}
	raw, err := base64.StdEncoding.DecodeString(sig.Value)
	if err != nil || !ed25519.Verify(pub, message(sig.Purpose, sig.KeyID, sig.Signer, sig.SignedAt, sig.SHA256), raw) {
		return fail("the signature does not match")
	}
	r.Valid = true
	for _, t := range trusted {
		if bytes.Equal(t.Key, pub) {
			r.Trusted, r.TrustedFrom = true, t.Source
			if t.Signer != "" {
				r.Signer = t.Signer
			}
			break
		}
	}
	return r
}

// TrustedKeysDir is where the desk and inspect look for trusted public keys
// when none are given: ~/.flightbay/trusted-keys/*.pub.
func TrustedKeysDir() string {
	if d := os.Getenv("FLIGHTBAY_HOME"); d != "" {
		return filepath.Join(d, "trusted-keys")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".flightbay", "trusted-keys")
}

// Trusted loads the given key files, or the trusted-keys directory when none
// are given and it exists.
func Trusted(files []string) ([]PublicKey, error) {
	if len(files) > 0 {
		return LoadPublic(files...)
	}
	if st, err := os.Stat(TrustedKeysDir()); err == nil && st.IsDir() {
		return LoadPublic(TrustedKeysDir())
	}
	return nil, nil
}
