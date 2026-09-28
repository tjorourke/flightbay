package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/sign"
)

func resolveFlags(t *testing.T, args ...string) (*PolicyFlags, error) {
	t.Helper()
	fs := pflag.NewFlagSet("t", pflag.ContinueOnError)
	// Flags of the command itself, as collect has; they never count as
	// changing the policy.
	fs.StringSlice("context", nil, "")
	fs.String("sign", "", "")
	fs.BoolP("quiet", "q", false, "")
	var pf PolicyFlags
	pf.Register(fs)
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	_, err := pf.Resolve(fs)
	return &pf, err
}

func TestAllowlistAndDropFields(t *testing.T) {
	p, err := resolve(t, "--include-resources", "pods,httproutes.gateway.networking.k8s.io", "--exclude-resources", "pods", "--drop-field", `metadata.annotations["corp.example/*"]`)
	if err != nil {
		t.Fatal(err)
	}
	if !p.ResourceAllowed("httproutes", "gateway.networking.k8s.io", "HTTPRoute", true) || p.ResourceAllowed("services", "", "Service", true) {
		t.Fatal("only allowlisted types are read")
	}
	if p.ResourceAllowed("pods", "", "Pod", true) {
		t.Fatal("exclusions still win over the allowlist")
	}
	if len(p.DropFields) != 1 {
		t.Fatalf("drop fields: %v", p.DropFields)
	}
	if !strings.Contains(p.RBAC(nil), "httproutes") || strings.Contains(p.RBAC(nil), "'*'") {
		t.Fatalf("RBAC lists the allowlist instead of a wildcard:\n%s", p.RBAC(nil))
	}
	if _, err := resolve(t, "--drop-field", "metadata.name"); err == nil {
		t.Fatal("a field rule that would break objects is refused")
	}
}

func TestApprovedPolicy(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "collection.yaml")
	body := []byte("profile: standard\nnamespaces: [payments]\n")
	_ = os.WriteFile(cfg, body, 0o600)
	approver, _ := sign.Generate("ACME security")
	pub := filepath.Join(dir, "approver.pub")
	_ = os.WriteFile(pub, approver.Public().MarshalPublic(), 0o644)

	// Unsigned: allowed without --policy-key, refused with it.
	pf, err := resolveFlags(t, "--config", cfg)
	if err != nil || pf.Source() == nil || pf.Source().Signature != nil || pf.Source().SHA256 == "" {
		t.Fatalf("unsigned policy is recorded by hash: %v %+v", err, pf.Source())
	}
	if _, err := resolveFlags(t, "--config", cfg, "--policy-key", pub); err == nil || !strings.Contains(err.Error(), "not signed") {
		t.Fatalf("--policy-key requires a signature: %v", err)
	}

	_ = os.WriteFile(cfg+".sig", approver.Sign(sign.PurposePolicy, body).Marshal(), 0o644)
	pf, err = resolveFlags(t, "--config", cfg, "--policy-key", pub, "--exclude-namespaces", "kube-*", "--case", "1", "--context", "prod", "--sign", "k", "-q")
	if err != nil {
		t.Fatalf("signed policy with narrowing flags: %v", err)
	}
	if len(pf.Source().ChangedBy) != 0 {
		t.Fatalf("command flags are not policy changes: %v", pf.Source().ChangedBy)
	}
	if s := pf.Source().Signature; s == nil || !s.Trusted || s.Signer != "ACME security" {
		t.Fatalf("approval recorded: %+v", s)
	}
	if _, err := resolveFlags(t, "--config", cfg, "--policy-key", pub, "--namespaces", "payments,billing"); err == nil || !strings.Contains(err.Error(), "--namespaces") {
		t.Fatalf("a widening flag is refused on an approved policy: %v", err)
	}
	pf, err = resolveFlags(t, "--config", cfg, "--since", "72h")
	if err != nil || strings.Join(pf.Source().ChangedBy, " ") != "--since" {
		t.Fatalf("without --policy-key a refinement is allowed and recorded: %v %+v", err, pf.Source())
	}

	other, _ := sign.Generate("someone else")
	otherPub := filepath.Join(dir, "other.pub")
	_ = os.WriteFile(otherPub, other.Public().MarshalPublic(), 0o644)
	if _, err := resolveFlags(t, "--config", cfg, "--policy-key", otherPub); err == nil || !strings.Contains(err.Error(), "not one of") {
		t.Fatalf("signed by the wrong approver: %v", err)
	}

	_ = os.WriteFile(cfg, append(body, []byte("secrets: keys\n")...), 0o600)
	if _, err := resolveFlags(t, "--config", cfg); err == nil || !strings.Contains(err.Error(), "edited after it was signed") {
		t.Fatalf("an edited policy is refused even without --policy-key: %v", err)
	}
}

func TestInspectSignature(t *testing.T) {
	k, _ := sign.Generate("ACME")
	var buf bytes.Buffer
	w, _ := bundle.NewWriter(&buf, bundle.WriterOptions{Signer: k})
	_ = w.WriteHeader(bundle.Header{CaptureStart: time.Now(), Contexts: []string{"c"}, CollectorSHA256: "abc123",
		PolicySource: &bundle.PolicySource{File: "p.yaml", SHA256: "ff", Signature: &sign.Result{Signed: true, Valid: true, Signer: "ACME security", KeyID: "fb:1"}}})
	_ = w.Close(bundle.Manifest{Complete: true})
	var out bytes.Buffer
	if err := Inspect(&out, bytes.NewReader(buf.Bytes()), InspectOptions{Trusted: []sign.PublicKey{k.Public()}, RequireTrusted: true}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Signature: signed by ACME (" + k.ID() + "), unmodified, key trusted", "Collector binary SHA-256: abc123", "Policy file: p.yaml (sha256 ff), approved by ACME security"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in\n%s", want, out.String())
		}
	}
	other, _ := sign.Generate("x")
	if err := Inspect(&bytes.Buffer{}, bytes.NewReader(buf.Bytes()), InspectOptions{Trusted: []sign.PublicKey{other.Public()}, RequireTrusted: true}); err == nil {
		t.Fatal("--verify-key with another key fails")
	}
}
