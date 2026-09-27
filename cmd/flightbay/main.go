// Command flightbay is the customer-side collector. It reads one or more
// clusters through a customer-approved collection policy, redacts in memory
// and streams a tar.zst bundle (optionally age-encrypted).
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"filippo.io/age"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/cli"
	"github.com/tjorourke/flightbay/internal/collect"
	"github.com/tjorourke/flightbay/internal/policy"
	"github.com/tjorourke/flightbay/internal/redact"
)

var version = "0.1.0-dev"

func main() {
	root := &cobra.Command{Use: "flightbay", Short: "Flightbay by Solo: collect an offline, redacted snapshot of Kubernetes clusters", SilenceUsage: true}
	root.AddCommand(collectCmd(), inspectCmd(), extractCmd(), rbacCmd(), docsCmd(), uiCmd(), &cobra.Command{
		Use: "version", Short: "Print the version", Run: func(*cobra.Command, []string) { fmt.Println("flightbay", version) },
	})
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

type kubeFlags struct {
	kubeconfig string
	contexts   []string
}

func (k *kubeFlags) register(c *cobra.Command) {
	c.Flags().StringVar(&k.kubeconfig, "kubeconfig", "", "kubeconfig file (default: $KUBECONFIG or ~/.kube/config)")
	c.Flags().StringSliceVar(&k.contexts, "context", nil, "kubeconfig contexts to collect (default: current context)")
}

func (k *kubeFlags) resolve() (clientcmd.ClientConfig, []string, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if k.kubeconfig != "" {
		rules.ExplicitPath = k.kubeconfig
	}
	base := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{})
	raw, err := base.RawConfig()
	if err != nil {
		return nil, nil, err
	}
	ctxs := k.contexts
	if len(ctxs) == 0 {
		if raw.CurrentContext == "" {
			return nil, nil, fmt.Errorf("no current context; pass --context")
		}
		ctxs = []string{raw.CurrentContext}
	}
	for _, c := range ctxs {
		if _, ok := raw.Contexts[c]; !ok {
			return nil, nil, fmt.Errorf("context %q not found in kubeconfig", c)
		}
	}
	return base, ctxs, nil
}

func restConfig(k *kubeFlags, ctx string, p *policy.Policy) (*rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if k.kubeconfig != "" {
		rules.ExplicitPath = k.kubeconfig
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: ctx}).ClientConfig()
	if err != nil {
		return nil, err
	}
	cfg.QPS, cfg.Burst = p.QPS, p.Burst
	cfg.UserAgent = "flightbay/" + version
	cfg.WarningHandler = rest.NoWarnings{}
	return cfg, nil
}

type logSource struct{ cs kubernetes.Interface }

func (l logSource) Stream(ctx context.Context, ns, pod string, o *corev1.PodLogOptions) (io.ReadCloser, error) {
	return l.cs.CoreV1().Pods(ns).GetLogs(pod, o).Stream(ctx)
}

func clients(cfg *rest.Config) (collect.Clients, error) {
	disc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return collect.Clients{}, err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return collect.Clients{}, err
	}
	meta, err := metadata.NewForConfig(cfg)
	if err != nil {
		return collect.Clients{}, err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return collect.Clients{}, err
	}
	return collect.Clients{Discovery: disc, Dynamic: dyn, Metadata: meta, Logs: logSource{cs}, Forward: &forwarder{cfg: cfg, cs: cs}}, nil
}

func recipients(specs []string) ([]age.Recipient, error) {
	var out []age.Recipient
	for _, s := range specs {
		switch {
		case s == "solo-support":
			return nil, fmt.Errorf("no Solo support key is built into this development build; pass the age recipient (age1...) or @file")
		case strings.HasPrefix(s, "@"):
			f, err := os.Open(strings.TrimPrefix(s, "@"))
			if err != nil {
				return nil, err
			}
			rs, err := age.ParseRecipients(f)
			f.Close()
			if err != nil {
				return nil, err
			}
			out = append(out, rs...)
		default:
			r, err := age.ParseX25519Recipient(s)
			if err != nil {
				return nil, fmt.Errorf("--encrypt-to %q: %w", s, err)
			}
			out = append(out, r)
		}
	}
	return out, nil
}

func prompt(in *bufio.Reader, out io.Writer, q, def string) string {
	fmt.Fprintf(out, "%s [%s]: ", q, def)
	line, _ := in.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

func yes(v string) bool { return strings.HasPrefix(strings.ToLower(v), "y") }

func interactive(p *policy.Policy, in *bufio.Reader, out io.Writer) (*policy.Policy, error) {
	fmt.Fprintln(out, "Choose what Flightbay may collect. Nothing is read until you confirm.")
	prof := prompt(in, out, "Profile: minimal, standard or custom", p.Profile)
	np, err := policy.Profile(prof)
	if err != nil {
		return nil, err
	}
	np.Case, np.Contexts = p.Case, p.Contexts
	if ns := prompt(in, out, "Namespaces to include (comma separated, globs allowed)", strings.Join(orAll(p.Namespaces), ",")); ns != "all" {
		np.Namespaces = splitList(ns)
	}
	if ex := prompt(in, out, "Namespaces to exclude", strings.Join(orNone(p.ExcludeNamespaces), ",")); ex != "none" {
		np.ExcludeNamespaces = splitList(ex)
	}
	logs := yes(prompt(in, out, "Collect container logs? (y/n)", map[bool]string{true: "y", false: "n"}[np.Has(policy.CatLogs)]))
	cats := []policy.Category{}
	for _, c := range np.Categories {
		if c != policy.CatLogs {
			cats = append(cats, c)
		}
	}
	if logs {
		cats = append(cats, policy.CatLogs)
		np.Logs.Since = prompt(in, out, "Log window", np.Logs.Since)
	}
	np.Categories = cats
	np.Secrets = prompt(in, out, "Secrets: omit, metadata or keys (values are never collected)", np.Secrets)
	np.ConfigMaps = prompt(in, out, "ConfigMaps: omit, metadata or scrubbed", np.ConfigMaps)
	if ex := prompt(in, out, "Resources never to read (comma separated)", "none"); ex != "none" {
		np.ExcludeResources = splitList(ex)
	}
	if yes(prompt(in, out, "Include cluster-scoped objects such as nodes? (y/n)", "n")) {
		np.ClusterScoped = true
		np.Categories = append(np.Categories, policy.CatNodes)
	}
	return np, np.Validate()
}

func orAll(s []string) []string {
	if len(s) == 0 {
		return []string{"all"}
	}
	return s
}

func orNone(s []string) []string {
	if len(s) == 0 {
		return []string{"none"}
	}
	return s
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func printPlan(w io.Writer, pl *collect.DryRunPlan, showRBAC bool) {
	fmt.Fprintf(w, "Cluster %s\n  Categories: %s\n  Namespaces: %s\n", pl.Cluster, strings.Join(pl.Categories, ", "), strings.Join(pl.Namespaces, ", "))
	for _, u := range pl.Unavailable {
		fmt.Fprintf(w, "  Not collected: %s\n", u)
	}
	if len(pl.LogStreams) > 0 {
		fmt.Fprintf(w, "  Logs: streams %s, since %s, up to %d bytes per stream (newest kept)\n", strings.Join(pl.LogStreams, ","), pl.LogSince, pl.LogMaxBytes)
	}
	fmt.Fprintf(w, "  Resource types (%d): %s\n", len(pl.Resources), strings.Join(pl.Resources, ", "))
	for _, c := range pl.Categories {
		if c == "proxies" || c == "control-plane" || c == "metrics" {
			fmt.Fprintln(w, "  Diagnostics: port-forward to Solo component pods in the selected namespaces (ztunnel, Envoy,\n"+
				"    agentgateway, istiod, gateway controllers) to read their admin endpoints. Output naming other\n"+
				"    namespaces is removed before writing; Envoy SDS secrets and controller krt snapshots are never kept.")
			break
		}
	}
	fmt.Fprintln(w, "  Secret values are never collected. A dry run reads discovery only, no objects or logs.")
	if showRBAC {
		fmt.Fprintf(w, "\nRequired RBAC:\n%s\n", pl.RBAC)
	}
}

func collectCmd() *cobra.Command {
	var pf cli.PolicyFlags
	var kf kubeFlags
	var out, saveConfig string
	var encryptTo []string
	var dryRun, interactiveMode, assumeYes, quiet, jsonOut bool
	var window int
	c := &cobra.Command{
		Use:     "collect",
		Aliases: []string{"capture"},
		Short:   "Collect a redacted snapshot bundle",
		Example: `  flightbay collect --context mesh1,mesh2 --case 01234567 --since 24h
  flightbay collect --namespaces payments,istio-system --exclude-resources configmaps,secrets --no-portforward
  flightbay collect --config collection.yaml --dry-run
  flightbay collect --interactive`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := pf.Resolve(cmd.Flags())
			if err != nil {
				return err
			}
			_, ctxs, err := kf.resolve()
			if err != nil {
				return err
			}
			p.Contexts = ctxs
			stderr := cmd.ErrOrStderr()
			if interactiveMode {
				if p, err = interactive(p, bufio.NewReader(cmd.InOrStdin()), stderr); err != nil {
					return err
				}
				p.Contexts = ctxs
			}
			if saveConfig != "" {
				b, _ := yaml.Marshal(p)
				if err := os.WriteFile(saveConfig, b, 0o600); err != nil {
					return err
				}
				fmt.Fprintf(stderr, "Saved collection config to %s (reuse with --config)\n", saveConfig)
			}
			rd, err := redact.New(p.RedactExtra)
			if err != nil {
				return err
			}
			if dryRun || interactiveMode {
				var plans []*collect.DryRunPlan
				for _, ctx := range ctxs {
					cfg, err := restConfig(&kf, ctx, p)
					if err != nil {
						return err
					}
					cl, err := clients(cfg)
					if err != nil {
						return err
					}
					pl, err := (&collect.Collector{Policy: p, Redactor: rd}).DryRun(cmd.Context(), ctx, cl)
					if err != nil {
						return fmt.Errorf("%s: %w", ctx, err)
					}
					plans = append(plans, pl)
				}
				if jsonOut {
					b, _ := json.MarshalIndent(plans, "", "  ")
					fmt.Fprintln(cmd.OutOrStdout(), string(b))
				} else {
					for _, pl := range plans {
						printPlan(cmd.OutOrStdout(), pl, dryRun)
					}
				}
				if dryRun {
					return nil
				}
				if !assumeYes && !yes(prompt(bufio.NewReader(cmd.InOrStdin()), stderr, "Collect now? (y/n)", "n")) {
					return fmt.Errorf("cancelled; nothing was collected")
				}
			}
			recips, err := recipients(append(encryptTo, p.EncryptTo...))
			if err != nil {
				return err
			}
			var dst io.Writer
			var finalPath, partPath string
			var f *os.File
			if out == "-" {
				dst = cmd.OutOrStdout()
			} else {
				caseID := p.Case
				if caseID == "" {
					caseID = "nocase"
				}
				name := fmt.Sprintf("flightbay-%s-%s.tar.zst", bundle.SafeID(caseID), time.Now().UTC().Format("20060102T1504Z"))
				if len(recips) > 0 {
					name += ".age"
				}
				finalPath = out
				if st, err := os.Stat(out); err == nil && st.IsDir() {
					finalPath = filepath.Join(out, name)
				}
				partPath = finalPath + ".partial"
				if f, err = os.OpenFile(partPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600); err != nil {
					return err
				}
				defer f.Close()
				dst = f
			}
			w, err := bundle.NewWriter(dst, bundle.WriterOptions{Compression: p.Compression, WindowSize: window, Recipients: recips})
			if err != nil {
				return err
			}
			col := &collect.Collector{Policy: p, Redactor: rd, Writer: w, Version: version}
			if !quiet {
				col.Progress = func(s string) { fmt.Fprintf(stderr, "  %s\n", s) }
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if err := col.Begin(ctxs); err != nil {
				return err
			}
			start := time.Now()
			for _, kctx := range ctxs {
				fmt.Fprintf(stderr, "Collecting %s\n", kctx)
				cfg, err := restConfig(&kf, kctx, p)
				if err == nil {
					cfg.WarningHandler = &collect.Warner{C: col, Cluster: bundle.SafeID(kctx)}
					var cl collect.Clients
					if cl, err = clients(cfg); err == nil {
						err = col.CollectCluster(ctx, kctx, cl)
					}
				}
				if err != nil {
					if ctx.Err() != nil {
						return fmt.Errorf("interrupted; the partial bundle is at %s and will fail verification", partPath)
					}
					col.MarkIncomplete(kctx, err)
					fmt.Fprintf(stderr, "  %s: %v (recorded in the collection log)\n", kctx, err)
				}
			}
			if err := col.Finish(); err != nil {
				return err
			}
			if f != nil {
				if err := f.Close(); err != nil {
					return err
				}
				if err := os.Rename(partPath, finalPath); err != nil {
					return err
				}
				st, _ := os.Stat(finalPath)
				fmt.Fprintf(stderr, "Wrote %s (%d bytes) in %s\nReview it before sending: flightbay inspect %s --redactions\n", finalPath, st.Size(), time.Since(start).Round(time.Millisecond), finalPath)
			}
			return nil
		},
	}
	pf.Register(c.Flags())
	kf.register(c)
	c.Flags().StringVarP(&out, "out", "o", ".", "output directory or file; - streams the bundle to stdout")
	c.Flags().StringSliceVar(&encryptTo, "encrypt-to", nil, "age recipients (age1...) or @file; the bundle becomes .tar.zst.age")
	c.Flags().IntVar(&window, "window-size", 0, "zstd window in bytes (default 8 MiB, max 128 MiB for stock tools)")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "print what would be collected and the RBAC it needs; reads discovery only")
	c.Flags().BoolVar(&jsonOut, "json", false, "print the dry-run plan as JSON")
	c.Flags().BoolVar(&interactiveMode, "interactive", false, "choose categories and scope with prompts, preview, then confirm")
	c.Flags().BoolVarP(&assumeYes, "yes", "y", false, "do not ask for confirmation in --interactive mode")
	c.Flags().StringVar(&saveConfig, "save-config", "", "write the resolved policy to this file for repeatable runs")
	c.Flags().BoolVarP(&quiet, "quiet", "q", false, "less progress output")
	return c
}

func openInput(path string) (io.ReadCloser, error) {
	if path == "-" {
		return io.NopCloser(os.Stdin), nil
	}
	return os.Open(path)
}

func inspectCmd() *cobra.Command {
	var o cli.InspectOptions
	var identities []string
	c := &cobra.Command{
		Use: "inspect BUNDLE", Short: "Verify a bundle and show what it contains before you send it", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := cli.LoadIdentities(identities)
			if err != nil {
				return err
			}
			o.Identities = ids
			f, err := openInput(args[0])
			if err != nil {
				return err
			}
			defer f.Close()
			return cli.Inspect(cmd.OutOrStdout(), f, o)
		},
	}
	c.Flags().BoolVar(&o.Contents, "contents", false, "list every file in the bundle")
	c.Flags().BoolVar(&o.Redactions, "redactions", false, "show redaction counts per location")
	c.Flags().BoolVar(&o.Policy, "policy", false, "print the collection policy recorded in the bundle")
	c.Flags().StringSliceVarP(&identities, "identity", "i", nil, "age identity file for encrypted bundles")
	return c
}

func extractCmd() *cobra.Command {
	var identities []string
	c := &cobra.Command{
		Use: "extract BUNDLE DIR", Short: "Extract and verify a bundle into a directory", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := cli.LoadIdentities(identities)
			if err != nil {
				return err
			}
			f, err := openInput(args[0])
			if err != nil {
				return err
			}
			defer f.Close()
			if err := cli.Extract(f, args[1], ids); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Extracted and verified into %s\n", args[1])
			return nil
		},
	}
	c.Flags().StringSliceVarP(&identities, "identity", "i", nil, "age identity file for encrypted bundles")
	return c
}

func rbacCmd() *cobra.Command {
	var pf cli.PolicyFlags
	var kf kubeFlags
	var discover bool
	c := &cobra.Command{
		Use: "rbac", Short: "Print least-privilege RBAC for the collection policy",
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := pf.Resolve(cmd.Flags())
			if err != nil {
				return err
			}
			if !discover {
				fmt.Fprint(cmd.OutOrStdout(), p.RBAC(nil))
				return nil
			}
			_, ctxs, err := kf.resolve()
			if err != nil {
				return err
			}
			cfg, err := restConfig(&kf, ctxs[0], p)
			if err != nil {
				return err
			}
			cl, err := clients(cfg)
			if err != nil {
				return err
			}
			rd, _ := redact.New(nil)
			pl, err := (&collect.Collector{Policy: p, Redactor: rd}).DryRun(cmd.Context(), ctxs[0], cl)
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), pl.RBAC)
			return nil
		},
	}
	pf.Register(c.Flags())
	kf.register(c)
	c.Flags().BoolVar(&discover, "discover", false, "use cluster discovery to list resources explicitly instead of a wildcard")
	return c
}

func docsCmd() *cobra.Command {
	d := &cobra.Command{Use: "docs", Short: "Print documentation generated from the collector"}
	d.AddCommand(&cobra.Command{
		Use: "collects", Short: "What Flightbay collects and what it never collects (Markdown)",
		Run: func(cmd *cobra.Command, _ []string) { fmt.Fprint(cmd.OutOrStdout(), collect.InventoryMarkdown()) },
	})
	return d
}
