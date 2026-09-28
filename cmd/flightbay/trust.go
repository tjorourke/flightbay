package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/tjorourke/flightbay/internal/cli"
	"github.com/tjorourke/flightbay/internal/review"
	"github.com/tjorourke/flightbay/internal/sign"
)

func loadSigner(file string) (*sign.PrivateKey, error) {
	if file == "" {
		return nil, nil
	}
	return sign.LoadPrivate(file)
}

// selfSHA256 hashes the running collector, so the bundle says which build wrote it.
func selfSHA256() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	f, err := os.Open(exe)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

func keygenCmd() *cobra.Command {
	var name, out string
	var force bool
	c := &cobra.Command{
		Use:   "keygen",
		Short: "Create an Ed25519 signing key pair: sign bundles and policies with the .key, give Solo the .pub",
		Example: `  flightbay keygen --name "ACME platform team"                 # writes flightbay-signing.key and .pub
  flightbay keygen --name "ACME security" -o approver           # approver.key and approver.pub, for policies`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			k, err := sign.Generate(name)
			if err != nil {
				return err
			}
			priv, pub := out+".key", out+".pub"
			for _, f := range []string{priv, pub} {
				if _, err := os.Stat(f); err == nil && !force {
					return fmt.Errorf("%s already exists; refusing to overwrite a key (use --force to replace it)", f)
				}
			}
			if err := os.WriteFile(priv, k.MarshalPrivate(), 0o600); err != nil {
				return err
			}
			if err := os.WriteFile(pub, k.Public().MarshalPublic(), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Key id %s for %s\n  Private key: %s (keep it; never send it)\n  Public key:  %s (give this to Solo)\nSign a capture: flightbay collect --sign %s\n", k.ID(), k.Signer, priv, pub, priv)
			return nil
		},
	}
	c.Flags().StringVar(&name, "name", "", "who signs with this key, e.g. \"ACME platform team\" (shown to Solo when a bundle verifies)")
	c.Flags().StringVarP(&out, "output", "o", "flightbay-signing", "file name without extension: writes NAME.key and NAME.pub")
	c.Flags().BoolVar(&force, "force", false, "overwrite existing key files")
	return c
}

func policyCmd() *cobra.Command {
	p := &cobra.Command{Use: "policy", Short: "Sign and check collection policy files your security team approves"}
	var key string
	sc := &cobra.Command{
		Use:   "sign POLICY",
		Short: "Approve a collection policy: writes POLICY.sig next to it",
		Example: `  flightbay policy sign collection.yaml --key approver.key
  flightbay collect --config collection.yaml --policy-key approver.pub   # refuses an unsigned or edited policy`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if key == "" {
				return errors.New("--key is required: the approver's private key (flightbay keygen)")
			}
			k, err := sign.LoadPrivate(key)
			if err != nil {
				return err
			}
			if _, err := (&cli.PolicyFlags{}).ResolveFile(args[0]); err != nil {
				return fmt.Errorf("not signing a policy that does not load: %w", err)
			}
			raw, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			if err := os.WriteFile(args[0]+".sig", k.Sign(sign.PurposePolicy, raw).Marshal(), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Signed %s as %s (%s): %s.sig\n", args[0], k.Signer, k.ID(), args[0])
			return nil
		},
	}
	sc.Flags().StringVar(&key, "key", "", "approver's private key")
	var keys []string
	vc := &cobra.Command{
		Use: "verify POLICY", Short: "Check POLICY.sig: unmodified since it was signed, and by whom", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			src, err := cli.PolicySource(args[0], keys)
			if err != nil {
				return err
			}
			if src.Signature == nil {
				return fmt.Errorf("%s is not signed", args[0])
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\nsha256 %s\n", args[0], src.Signature.Summary(), src.SHA256)
			return nil
		},
	}
	vc.Flags().StringSliceVar(&keys, "key", nil, "approver public key(s) it must be signed by")
	p.AddCommand(sc, vc)
	return p
}

func reviewCmd() *cobra.Command {
	var planFile, out, signKey, reviewer string
	var encryptTo, identities, verifyKeys []string
	var discloseNames, noBrowser bool
	c := &cobra.Command{
		Use:   "review BUNDLE",
		Short: "Decide what to send to Solo: remove namespaces, types, objects, logs and files, redact more, then sign the result",
		Long: `Opens the bundle in the Flightbay UI in review mode. The Review page lists everything in it; take out
what you do not want to send, add redaction patterns, check what Solo will receive, and export. The export is
a new bundle, signed with your key if you give one, with a ledger of what was removed (counts only unless you
choose to list names).

With --plan the same review runs without the UI, from a plan file (YAML or JSON), for repeatable reviews.`,
		Example: `  flightbay review flightbay-01234567-20260928T1012Z.tar.zst --sign flightbay-signing.key
  flightbay review bundle.tar.zst --plan review.yaml --sign flightbay-signing.key --encrypt-to age1...`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.HasSuffix(args[0], ".age") && len(identities) == 0 {
				return errors.New("this bundle is encrypted; review needs the identity that opens it (-i). Capture without --encrypt-to, review, then encrypt the reviewed bundle with --encrypt-to")
			}
			if planFile == "" {
				desk, err := findDesk()
				if err != nil {
					return err
				}
				argv := []string{desk, "open", "--review", args[0]}
				for _, k := range []struct{ flag, v string }{{"--sign", signKey}, {"--reviewer", reviewer}} {
					if k.v != "" {
						argv = append(argv, k.flag, k.v)
					}
				}
				for _, r := range encryptTo {
					argv = append(argv, "--encrypt-to", r)
				}
				for _, i := range identities {
					argv = append(argv, "-i", i)
				}
				for _, v := range verifyKeys {
					argv = append(argv, "--verify-key", v)
				}
				if noBrowser {
					argv = append(argv, "--no-browser")
				}
				if err := syscall.Exec(desk, argv, os.Environ()); err != nil {
					x := exec.Command(desk, argv[1:]...)
					x.Stdin, x.Stdout, x.Stderr = os.Stdin, os.Stdout, os.Stderr
					return x.Run()
				}
				return nil
			}
			plan, err := review.LoadPlan(planFile)
			if err != nil {
				return err
			}
			if reviewer != "" {
				plan.Reviewer = reviewer
			}
			if cmd.Flags().Changed("disclose-names") {
				plan.DiscloseNames = discloseNames
			}
			o := review.Options{Tool: "flightbay " + version}
			if o.Identities, err = cli.LoadIdentities(identities); err != nil {
				return err
			}
			if o.Recipients, err = recipients(encryptTo); err != nil {
				return err
			}
			if o.Signer, err = loadSigner(signKey); err != nil {
				return err
			}
			if o.Trusted, err = sign.Trusted(verifyKeys); err != nil {
				return err
			}
			if out == "" {
				out = review.OutputName(args[0], len(o.Recipients) > 0)
			}
			if abs1, _ := filepath.Abs(out); abs1 == mustAbs(args[0]) {
				return errors.New("the reviewed bundle must not overwrite the original")
			}
			led, err := review.WriteFile(cmd.Context(), args[0], out, plan, o)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s\n  Removed: %s\n", out, led.Summary())
			if o.Signer != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "  Signed by %s (%s)\n", o.Signer.Signer, o.Signer.ID())
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "  Not signed: pass --sign KEY so Solo can verify it came from you unmodified")
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Check it before sending: flightbay inspect %s\n", out)
			return nil
		},
	}
	c.Flags().StringVar(&planFile, "plan", "", "apply this review plan without the UI (YAML or JSON)")
	c.Flags().StringVarP(&out, "out", "o", "", "reviewed bundle to write (default: NAME-reviewed.tar.zst next to the original)")
	c.Flags().StringVar(&signKey, "sign", "", "sign the reviewed bundle with this private key")
	c.Flags().StringSliceVar(&encryptTo, "encrypt-to", nil, "encrypt the reviewed bundle to these age recipients (age1...) or @file")
	c.Flags().StringVar(&reviewer, "reviewer", "", "who reviewed it, recorded in the ledger")
	c.Flags().BoolVar(&discloseNames, "disclose-names", false, "list removed namespaces, objects and streams by name in the ledger (default: counts only)")
	c.Flags().StringSliceVarP(&identities, "identity", "i", nil, "age identity to open an encrypted bundle")
	c.Flags().StringSliceVar(&verifyKeys, "verify-key", nil, "public keys to check the original's signature against")
	c.Flags().BoolVar(&noBrowser, "no-browser", false, "do not open a browser (UI mode)")
	return c
}

func mustAbs(p string) string {
	a, _ := filepath.Abs(p)
	return a
}
