// Command flightbay-desk opens a Flightbay bundle for investigation: a web UI,
// a read-only Kubernetes API for kubectl/k9s/stern, and a terminal. Data is
// browsable while the bundle is still loading.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/tjorourke/flightbay/internal/features"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"filippo.io/age"
	"github.com/spf13/cobra"

	"github.com/tjorourke/flightbay/internal/apiserver"
	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/cli"
	"github.com/tjorourke/flightbay/internal/diff"
	"github.com/tjorourke/flightbay/internal/importer"
	"github.com/tjorourke/flightbay/internal/mcp"
	"github.com/tjorourke/flightbay/internal/review"
	"github.com/tjorourke/flightbay/internal/server"
	"github.com/tjorourke/flightbay/internal/sign"
	"github.com/tjorourke/flightbay/internal/store"
	_ "github.com/tjorourke/flightbay/internal/store/duckdb" // SQL and log search
)

var version = "0.1.0-dev"

// newRoot builds the command tree (also used to generate the CLI reference).
func newRoot() *cobra.Command {
	root := &cobra.Command{Use: "flightbay-desk", Short: "Flightbay by Solo: investigate a snapshot bundle offline", SilenceUsage: true}
	root.AddCommand(openCmd(), startCmd(), liveCmd(), shellCmd(), queryCmd(), findingsCmd(), proxyConfigCmd(), searchCmd(), sqlCmd(), flowsCmd(), postureCmd(), reachCmd(), featuresCmd(), diffCmd(), driftCmd(), mcpCmd(), keygenCmd(), &cobra.Command{
		Use: "version", Short: "Print the version", Run: func(*cobra.Command, []string) { fmt.Println("flightbay-desk", version) },
	})
	return root
}

func main() {
	if err := newRoot().Execute(); err != nil {
		os.Exit(1)
	}
}

func home() string {
	if d := os.Getenv("FLIGHTBAY_HOME"); d != "" {
		return d
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".flightbay")
}

type deskInfo struct {
	URL        string `json:"url"`
	Token      string `json:"token"`
	Kubeconfig string `json:"kubeconfig"`
	WorkDir    string `json:"workdir"`
	Bundle     string `json:"bundle"`
	PID        int    `json:"pid"`
}

func randomToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

func parseSize(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	mult := int64(1)
	u := strings.ToUpper(strings.TrimSpace(s))
	for _, sm := range []struct {
		suf string
		m   int64
	}{{"GIB", 1 << 30}, {"MIB", 1 << 20}, {"KIB", 1 << 10}, {"GB", 1e9}, {"MB", 1e6}, {"KB", 1000}, {"G", 1 << 30}, {"M", 1 << 20}, {"K", 1 << 10}} {
		if strings.HasSuffix(u, sm.suf) {
			mult, u = sm.m, strings.TrimSuffix(u, sm.suf)
			break
		}
	}
	var n float64
	if _, err := fmt.Sscanf(u, "%g", &n); err != nil {
		return 0, fmt.Errorf("bad size %q", s)
	}
	return int64(n * float64(mult)), nil
}

func openCmd() *cobra.Command {
	var listen, workdir, throttle, shell, baseline string
	var identities, deferCats, flowFiles []string
	var noBrowser, noTerminal, allowRemote, keep bool
	var reviewMode bool
	var signKey, reviewer, reviewOut string
	var encryptTo, verifyKeys []string
	c := &cobra.Command{
		Use:   "open [BUNDLE | DIR | -]",
		Short: "Open a bundle and start investigating while it loads (no bundle: the start page)",
		Example: `  flightbay-desk open                                  # the start page: live, a file or a repo
  flightbay-desk open flightbay-01234567-20260927T1012Z.tar.zst
  flightbay-desk open .                                # the newest flightbay-*.tar.zst in this directory
  flightbay collect -o - | flightbay-desk open -
  flightbay-desk open bundle.tar.zst.age -i ~/.flightbay/support.key
  flightbay-desk open bundle.tar.zst --defer logs      # load logs later from the UI
  flightbay-desk open bundle.tar.zst --sign my.key     # remove objects, logs or namespaces, export a signed copy
  flightbay-desk open bundle.tar.zst --flows hubble.jsonl,vpc-flows.log@mesh2   # add flow logs (not signed)`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !loopback(listen) && !allowRemote {
				return fmt.Errorf("--listen must be a loopback address in local mode; use --allow-remote in a container")
			}
			if !loopback(listen) {
				fmt.Fprintln(cmd.ErrOrStderr(), "warning: listening beyond loopback. Anyone who can reach this port and has the token can read the snapshot.")
			}
			ids, err := cli.LoadIdentities(identities)
			if err != nil {
				return err
			}
			trusted, err := sign.Trusted(verifyKeys)
			if err != nil {
				return err
			}
			rate, err := parseSize(throttle)
			if err != nil {
				return err
			}
			reviewerFlows, err := cli.ParseFlowFiles(flowFiles)
			if err != nil {
				return err
			}
			if len(args) == 0 {
				// Nothing to open: the start page, which hands over to open,
				// review or live on the same address.
				return runStart(cmd, startOptions{listen: listen, workdir: workdir, shell: shell, identities: identities,
					noBrowser: noBrowser, noTerminal: noTerminal, allowRemote: allowRemote, keep: keep})
			}
			path := args[0]
			if path != "-" {
				if path, err = resolveBundle(path); err != nil {
					return err
				}
			}
			var src io.ReadCloser
			if path == "-" {
				src = io.NopCloser(os.Stdin)
			} else if src, err = os.Open(path); err != nil {
				return err
			}
			if workdir == "" {
				wsRoot := filepath.Join(home(), "workspaces")
				if err := os.MkdirAll(wsRoot, 0o700); err != nil {
					return err
				}
				sweepWorkspaces(wsRoot, 7*24*time.Hour)
				if workdir, err = os.MkdirTemp(wsRoot, "ws-"); err != nil {
					return err
				}
				if !keep {
					// Runs after ws.Close (deferred later, so it runs first).
					defer os.RemoveAll(workdir)
				}
			} else if tempWorkdir() && !keep {
				defer os.RemoveAll(workdir) // created by the start page
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if err := os.MkdirAll(workdir, 0o700); err != nil {
				return err
			}
			ws, err := store.Open(filepath.Join(workdir, "workspace.duckdb"))
			if err != nil {
				return fmt.Errorf("cannot create the workspace database: %w", err)
			}
			defer ws.Close()
			skip := map[string]bool{}
			for _, d := range deferCats {
				skip[strings.TrimSpace(d)] = true
			}
			opts := importer.Options{Source: path, Throttle: rate, Skip: skip, Trusted: trusted}
			opts.Reader.Identities = ids
			importDone := make(chan error, 1)
			go func() {
				defer src.Close()
				importDone <- importer.Run(ctx, src, ws, opts)
			}()
			ln, err := net.Listen("tcp", listen)
			if err != nil && strings.HasSuffix(listen, ":8484") {
				ln, err = net.Listen("tcp", "127.0.0.1:0")
			}
			if err != nil {
				return err
			}
			base := "http://" + ln.Addr().String()
			exe, _ := os.Executable()
			if shell == "" {
				shell = "/bin/bash"
			}
			tlsCfg, caPEM, err := apiserver.NewSessionTLS()
			if err != nil {
				return err
			}
			kln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				return err
			}
			srv := &server.Server{WS: ws, Token: deskToken(), WorkDir: workdir, BaseURL: base, Terminal: !noTerminal, Shell: shell, DeskBinary: exe,
				KubeURL: "https://" + kln.Addr().String(), KubeCA: caPEM}
			ks := &http.Server{Handler: srv.KubeHandler(), TLSConfig: tlsCfg, ReadHeaderTimeout: 10 * time.Second}
			go func() { _ = ks.ServeTLS(kln, "", "") }()
			defer ks.Close()
			if path != "-" {
				srv.Reload = func() error {
					f, err := os.Open(path)
					if err != nil {
						return err
					}
					defer f.Close()
					o := importer.Options{Source: path, Trusted: trusted}
					o.Reader.Identities = ids
					return importer.Run(context.Background(), f, ws, o)
				}
			}
			if path != "-" {
				srv.BundlePath = path
			}
			srv.Identities = ids
			if reviewMode && path == "-" {
				return errors.New("--review needs a bundle file: a stream cannot be read twice to export from it")
			}
			// Any bundle opened from a file can be trimmed and exported from
			// the Review and send page; a stream cannot be re-read.
			if path != "-" {
				rs := &server.ReviewSession{Source: path, Landing: reviewMode, Plan: review.Plan{Reviewer: reviewer, ScrubNames: true}}
				rs.Options = review.Options{Identities: ids, Trusted: trusted, Tool: "flightbay-desk " + version}
				if rs.Options.Recipients, err = cli.ParseRecipients(encryptTo); err != nil {
					return err
				}
				if signKey != "" {
					if rs.Options.Signer, err = sign.LoadPrivate(signKey); err != nil {
						return err
					}
				}
				rs.Out = reviewOut
				if rs.Out == "" {
					rs.Out = review.OutputName(path, len(rs.Options.Recipients) > 0)
				}
				srv.Review = rs
			}
			// Loads an earlier bundle for the Compare view, from --baseline or
			// picked in the UI.
			srv.LoadBaseline = func(p string) error {
				srv.BaselineLoading(p)
				go func() {
					bws, err := loadBundle(ctx, p, ids)
					if bws == nil {
						srv.BaselineFailed(p, err)
						fmt.Fprintf(cmd.ErrOrStderr(), "Baseline not loaded: %v\n", err)
						return
					}
					if err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "Baseline loaded with problems: %v\n", err)
					}
					srv.SetBaseline(bws, p)
					fmt.Fprintf(cmd.ErrOrStderr(), "Baseline %s loaded for comparison.\n", p)
				}()
				return nil
			}
			if baseline != "" {
				_ = srv.LoadBaseline(baseline)
			}
			defer srv.Close()
			hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
			go func() { _ = hs.Serve(ln) }()
			defer hs.Close()

			errOut := cmd.ErrOrStderr()
			fmt.Fprintf(errOut, "Opening %s\n", path)
			deadline := time.Now().Add(2 * time.Minute)
			for ws.Header() == nil {
				if st := ws.State(); st.Phase == store.PhaseFailed || st.Phase == store.PhaseCancelled {
					return fmt.Errorf("could not open the bundle: %s", st.Error)
				}
				if time.Now().After(deadline) {
					return errors.New("timed out waiting for bundle.json")
				}
				time.Sleep(20 * time.Millisecond)
			}
			if err := srv.Prepare(); err != nil {
				return err
			}
			for _, ff := range reviewerFlows {
				cluster := ff.Cluster
				if cluster == "" {
					if cs := ws.Clusters(); len(cs) > 0 {
						cluster = cs[0]
					}
				}
				f, err := os.Open(ff.Path)
				if err != nil {
					return err
				}
				res, err := ws.AddReviewerFlows(bundle.SafeID(cluster), filepath.Base(ff.Path), f, "")
				f.Close()
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "Flows from %s not loaded: %v\n", ff.Path, err)
					continue
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "Added %d %s flow records (%d flows) from %s to %s; marked as added by the reviewer, not part of the signed bundle.\n", res.Events, res.Format, res.Flows, ff.Path, cluster)
			}
			info := deskInfo{URL: base, Token: srv.Token, Kubeconfig: srv.KubeconfigPath(), WorkDir: workdir, Bundle: path, PID: os.Getpid()}
			b, _ := json.MarshalIndent(info, "", "  ")
			_ = os.WriteFile(filepath.Join(workdir, "desk.json"), b, 0o600)
			// Helpers (fbq, fbpc, flightbay-desk shell) find the workspace through this.
			if err := os.MkdirAll(home(), 0o700); err == nil {
				err = os.WriteFile(filepath.Join(home(), "current.json"), b, 0o600)
			}
			if err != nil {
				fmt.Fprintf(errOut, "warning: could not record the open workspace for helpers: %v\n", err)
			}
			url := base + "/?token=" + srv.Token
			fmt.Fprintf(errOut, "\nFlightbay desk is ready while the bundle loads.\n  UI:          %s\n  kubectl:     export KUBECONFIG=%s   (snapshot API on %s)\n  Shell:       flightbay-desk shell   (kubectl, k9s, stern, fbq, fbsearch, fbsql, fbfind, fbpc, fbflows)\n  Press Ctrl+C to close the workspace.\n\n", url, srv.KubeconfigPath(), srv.KubeURL)
			if !noBrowser {
				openBrowser(url)
			}
			for {
				select {
				case err := <-importDone:
					st := ws.State()
					if err != nil {
						fmt.Fprintf(errOut, "Import stopped: %v\nThe workspace stays open with the data loaded so far, clearly marked as unverified.\n", err)
					} else {
						_ = srv.Prepare()
						msg := ""
						if len(st.Deferred) > 0 {
							msg = fmt.Sprintf("; deferred: %s (load from the UI)", strings.Join(st.Deferred, ", "))
						}
						fmt.Fprintf(errOut, "Import complete and verified: %d objects, %d log lines, %d events, %d bytes%s\n", st.Objects, st.LogLines, st.Events, st.BytesReceived, msg)
					}
					importDone = nil
				case <-ctx.Done():
					fmt.Fprintln(errOut, "Closing workspace.")
					return nil
				}
			}
		},
	}
	c.Flags().StringVar(&listen, "listen", "127.0.0.1:8484", "loopback address for the UI and API")
	c.Flags().StringSliceVarP(&identities, "identity", "i", nil, "age identity file for encrypted bundles")
	c.Flags().StringSliceVar(&deferCats, "defer", nil, "categories to load later from the UI: objects, logs, events, diagnostics")
	c.Flags().StringSliceVar(&flowFiles, "flows", nil, "flow log files to add, FILE or FILE@CLUSTER (Hubble, Calico, AWS/GCP/Azure VPC, generic CSV/JSONL); marked as added by the reviewer, not signed")
	c.Flags().StringVar(&throttle, "throttle", "", "limit import speed, e.g. 512KiB (rehearse slow uploads)")
	c.Flags().StringVar(&workdir, "workdir", "", "workspace directory (default ~/.flightbay/workspaces/ws-*)")
	c.Flags().BoolVar(&noBrowser, "no-browser", false, "do not open a browser")
	c.Flags().BoolVar(&noTerminal, "no-terminal", false, "disable the in-browser terminal")
	c.Flags().BoolVar(&keep, "keep", false, "keep the workspace directory (DuckDB file, kubeconfig) after closing")
	c.Flags().BoolVar(&allowRemote, "allow-remote", false, "allow a non-loopback --listen (for containers); the token is still required")
	c.Flags().StringVar(&shell, "shell", "", "shell for the terminal (default /bin/bash)")
	c.Flags().StringVar(&baseline, "baseline", "", "an earlier bundle from the same customer to compare against (Compare view)")
	c.Flags().StringSliceVar(&verifyKeys, "verify-key", nil, "public keys to check the bundle's signature against (default: ~/.flightbay/trusted-keys/*.pub)")
	c.Flags().BoolVar(&reviewMode, "review", false, "open on the Review and send page (removing objects and exporting works from any bundle file without it)")
	c.Flags().StringVar(&signKey, "sign", "", "sign bundles exported from the Review and send page with this private key (flightbay keygen)")
	c.Flags().StringSliceVar(&encryptTo, "encrypt-to", nil, "encrypt bundles exported from the Review and send page to these age recipients (age1...) or @file")
	c.Flags().StringVar(&reviewer, "reviewer", "", "who is removing things, recorded in the exported bundle's ledger")
	c.Flags().StringVar(&reviewOut, "review-out", "", "file to export to (default NAME-reviewed.tar.zst next to the bundle)")
	return c
}

// sweepWorkspaces removes workspace directories left behind by desks that
// were killed rather than closed.
func sweepWorkspaces(root string, olderThan time.Duration) {
	ents, _ := os.ReadDir(root)
	for _, e := range ents {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "ws-") {
			continue
		}
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > olderThan {
			_ = os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
}

// resolveBundle accepts a bundle file, or a directory in which it picks the
// newest flightbay-*.tar.zst(.age).
func resolveBundle(p string) (string, error) {
	st, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		return p, nil
	}
	var best string
	var bestT time.Time
	for _, pat := range []string{"flightbay-*.tar.zst", "flightbay-*.tar.zst.age"} {
		ms, _ := filepath.Glob(filepath.Join(p, pat))
		for _, m := range ms {
			if fi, err := os.Stat(m); err == nil && fi.Mode().IsRegular() && fi.ModTime().After(bestT) {
				best, bestT = m, fi.ModTime()
			}
		}
	}
	if best == "" {
		return "", fmt.Errorf("no flightbay-*.tar.zst bundle in %s; capture one with flightbay collect, or pass the file", p)
	}
	return best, nil
}

func currentDesk() (deskInfo, error) {
	var d deskInfo
	b, err := os.ReadFile(filepath.Join(home(), "current.json"))
	if err != nil {
		return d, fmt.Errorf("no open workspace found; run flightbay-desk open first")
	}
	return d, json.Unmarshal(b, &d)
}

func deskEnv() (string, string, error) {
	if u, t := os.Getenv("FLIGHTBAY_URL"), os.Getenv("FLIGHTBAY_TOKEN"); u != "" && t != "" {
		return u, t, nil
	}
	d, err := currentDesk()
	return d.URL, d.Token, err
}

func shellCmd() *cobra.Command {
	return &cobra.Command{
		Use: "shell", Short: "Start a shell wired to the open workspace (kubectl or k, k9s, stern, fbsearch, fbq, fbfind, fbpc)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			d, err := currentDesk()
			if err != nil {
				return err
			}
			// Same environment as the UI's terminal: bash with its own rc file,
			// so hooks from the user's zsh or bash setup don't leak in.
			env := []string{}
			for _, kv := range os.Environ() {
				switch k, _, _ := strings.Cut(kv, "="); {
				case k == "PROMPT_COMMAND", k == "BASH_ENV", k == "ENV", k == "KUBECONFIG", k == "PS1", strings.HasPrefix(k, "BASH_FUNC_"):
					continue
				}
				env = append(env, kv)
			}
			env = append(env, "KUBECONFIG="+d.Kubeconfig, "FLIGHTBAY_URL="+d.URL, "FLIGHTBAY_TOKEN="+d.Token, "BASH_SILENCE_DEPRECATION_WARNING=1",
				"PATH="+filepath.Join(d.WorkDir, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
				`PS1=\[\e[36m\]flightbay\[\e[0m\] snapshot \W $ `)
			args := []string{"--noprofile", "--rcfile", filepath.Join(d.WorkDir, "bashrc"), "-i"}
			c := exec.Command("bash", args...)
			c.Stdin, c.Stdout, c.Stderr, c.Env = os.Stdin, os.Stdout, os.Stderr, env
			if strings.HasPrefix(d.Bundle, "live:") {
				env = append(env, `PS1=\[\e[32m\]flightbay\[\e[0m\] live \W $ `)
				fmt.Fprintf(cmd.ErrOrStderr(), "Flightbay live shell for %s. kubectl (or k) and k9s read the read-only live mirror. Type exit to leave.\n", strings.TrimPrefix(d.Bundle, "live:"))
			} else {
				fmt.Fprintf(cmd.ErrOrStderr(), "Flightbay snapshot shell for %s. kubectl (or k) and k9s read the snapshot. Type exit to leave.\n", d.Bundle)
			}
			return c.Run()
		},
	}
}

func queryCmd() *cobra.Command {
	var limit int
	c := &cobra.Command{
		Use: "query QUERY", Short: "Search logs in the open workspace (the fbq helper)", Args: cobra.MinimumNArgs(1),
		Example: `  fbq 'level:error ns:istio-system since:2h "connection reset"'`,
		RunE: func(cmd *cobra.Command, args []string) error {
			u, t, err := deskEnv()
			if err != nil {
				return err
			}
			return cli.Query(cmd.OutOrStdout(), u, t, strings.Join(args, " "), limit)
		},
	}
	c.Flags().IntVar(&limit, "limit", 100, "maximum lines")
	return c
}

func findingsCmd() *cobra.Command {
	return &cobra.Command{
		Use: "findings", Short: "List findings in the open workspace (the fbfind helper)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			u, t, err := deskEnv()
			if err != nil {
				return err
			}
			return cli.Findings(cmd.OutOrStdout(), u, t)
		},
	}
}

func proxyConfigCmd() *cobra.Command {
	var o cli.ProxyConfigOptions
	c := &cobra.Command{
		Use: "proxy-config [POD]", Short: "Show a captured proxy or control-plane dump (the fbpc helper)", Args: cobra.MaximumNArgs(1),
		Example: `  fbpc                                  # list captured components
  fbpc ztunnel-5b4bw                    # workloads, services, policies, certificates
  fbpc ai-gateway --section routes      # one table
  fbpc istiod --file syncz.json | jq    # a raw captured file`,
		RunE: func(cmd *cobra.Command, args []string) error {
			u, t, err := deskEnv()
			if err != nil {
				return err
			}
			if len(args) == 1 {
				o.Pod = args[0]
			}
			return cli.ProxyConfig(cmd.OutOrStdout(), u, t, o)
		},
	}
	c.Flags().StringVar(&o.Cluster, "cluster", "", "cluster name in the bundle")
	c.Flags().StringVarP(&o.Namespace, "namespace", "n", "", "namespace of the pod")
	c.Flags().StringVar(&o.File, "file", "", "print one captured file as captured, e.g. config_dump.json")
	c.Flags().StringVar(&o.Section, "section", "", "only tables whose title contains this, e.g. routes")
	return c
}

// loadBundle imports a whole bundle into a fresh workspace and verifies it.
func loadBundle(ctx context.Context, path string, ids []age.Identity) (*store.Workspace, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	ws := store.New()
	o := importer.Options{Source: path}
	o.Reader.Identities = ids
	if err := importer.Run(ctx, f, ws, o); err != nil {
		return ws, fmt.Errorf("%s: %w (compared as an unverified, possibly partial capture)", path, err)
	}
	return ws, nil
}

func diffCmd() *cobra.Command {
	var identities []string
	var jsonOut, showYAML bool
	c := &cobra.Command{
		Use:   "diff BEFORE AFTER",
		Short: "Compare two bundles from the same customer: objects, image versions and findings",
		Example: `  flightbay-desk diff flightbay-01234567-20260920T0900Z.tar.zst flightbay-01234567-20260927T1012Z.tar.zst
  flightbay-desk diff before.tar.zst after.tar.zst --yaml    # include changed objects
  flightbay-desk open after.tar.zst --baseline before.tar.zst  # the same in the UI`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := cli.LoadIdentities(identities)
			if err != nil {
				return err
			}
			before, err1 := loadBundle(cmd.Context(), args[0], ids)
			after, err2 := loadBundle(cmd.Context(), args[1], ids)
			for _, e := range []error{err1, err2} {
				if e != nil {
					if before == nil || after == nil {
						return e
					}
					fmt.Fprintln(cmd.ErrOrStderr(), "warning:", e)
				}
			}
			r := diff.Compare(before, after)
			if jsonOut {
				_, err = cmd.OutOrStdout().Write(append(r.JSON(), '\n'))
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), r.Text(showYAML))
			return nil
		},
	}
	c.Flags().StringSliceVarP(&identities, "identity", "i", nil, "age identity file for encrypted bundles")
	c.Flags().BoolVar(&jsonOut, "json", false, "print the report as JSON")
	c.Flags().BoolVar(&showYAML, "yaml", false, "include the before and after YAML of changed objects")
	return c
}

func driftCmd() *cobra.Command {
	var identities []string
	var jsonOut bool
	c := &cobra.Command{
		Use:   "drift REPO_BUNDLE CLUSTER_BUNDLE",
		Short: "Compare a repo scan (declared state) with a capture of the cluster (live state)",
		Long: `Drift compares a bundle from flightbay scan with a bundle from flightbay collect. It lists
objects in Git but not in the cluster, objects in the cluster but not in Git (limited to the
namespaces and kinds the repository declares, so system objects do not flood it), and fields
that differ once status, server-set metadata and defaulted fields are removed.`,
		Example: `  flightbay scan deploy/ --bundle repo.tar.zst
  flightbay collect --namespaces payments -o cluster.tar.zst
  flightbay-desk drift repo.tar.zst cluster.tar.zst
  flightbay-desk open cluster.tar.zst --baseline repo.tar.zst   # the same in the Compare view`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := cli.LoadIdentities(identities)
			if err != nil {
				return err
			}
			repo, err1 := loadBundle(cmd.Context(), args[0], ids)
			live, err2 := loadBundle(cmd.Context(), args[1], ids)
			for _, e := range []error{err1, err2} {
				if e != nil {
					if repo == nil || live == nil {
						return e
					}
					fmt.Fprintln(cmd.ErrOrStderr(), "warning:", e)
				}
			}
			if !diff.IsDeclared(repo) && diff.IsDeclared(live) {
				repo, live = live, repo
				fmt.Fprintln(cmd.ErrOrStderr(), "note: the second bundle is the repo scan; comparing it as the declared side")
			}
			if !diff.IsDeclared(repo) {
				return fmt.Errorf("%s is not a repo scan (flightbay scan); use flightbay-desk diff to compare two captures", args[0])
			}
			r := diff.Drift(repo, live)
			if jsonOut {
				_, err = cmd.OutOrStdout().Write(append(r.JSON(), '\n'))
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), r.Text())
			return nil
		},
	}
	c.Flags().StringSliceVarP(&identities, "identity", "i", nil, "age identity file for encrypted bundles")
	c.Flags().BoolVar(&jsonOut, "json", false, "print the report as JSON")
	return c
}

func mcpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Serve the open workspace to AI agents over MCP (stdio), read-only",
		Long: `Serves the open workspace as a Model Context Protocol server on stdin/stdout.
Tools: workspace_summary, list_findings, best_practices, agent_reach, search_logs, log_context,
search_objects, run_sql, list_resource_types, list_objects, get_object, proxy_config,
compare_baseline. Prompt: triage. best_practices priorities and agent_reach need Flightbay Enterprise.

The server only talks to the local desk. Snapshot data reaches a model only if the
agent you connect is configured to use one.`,
		Example: `  claude mcp add flightbay -- flightbay-desk mcp
  opencode: "mcp": {"flightbay": {"type": "local", "command": ["flightbay-desk", "mcp"]}}`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			u, t, err := deskEnv()
			if err != nil {
				return err
			}
			s := &mcp.Server{BaseURL: u, Token: t, Version: version}
			return s.Serve(cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
}

func searchCmd() *cobra.Command {
	var limit int
	c := &cobra.Command{
		Use: "search QUERY", Short: "Find objects of any kind in the open workspace (the fbsearch helper)", Args: cobra.MinimumNArgs(1),
		Example: `  fbsearch checkout                              # names, labels and content of every captured object
  fbsearch 'kind:pod ns:payments CrashLoopBackOff'
  fbsearch 'label:app=checkout -kind:event'
  fbsearch '"shop.example.com"'                  # e.g. which routes use a hostname`,
		RunE: func(cmd *cobra.Command, args []string) error {
			u, t, err := deskEnv()
			if err != nil {
				return err
			}
			return cli.Search(cmd.OutOrStdout(), u, t, strings.Join(args, " "), limit)
		},
	}
	c.Flags().IntVar(&limit, "limit", 50, "maximum results")
	return c
}

func sqlCmd() *cobra.Command {
	var limit int
	c := &cobra.Command{
		Use: "sql [QUERY]", Short: "Run read-only SQL over logs, objects, events and flows (the fbsql helper); no query starts a prompt",
		Example: `  fbsql "SELECT pod, count(*) FROM logs WHERE level = 'error' GROUP BY ALL ORDER BY 2 DESC"
  fbsql            # interactive; type help for the tables`,
		RunE: func(cmd *cobra.Command, args []string) error {
			u, t, err := deskEnv()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(args) > 0 {
				return cli.SQL(out, u, t, strings.Join(args, " "), limit)
			}
			fmt.Fprintln(out, "Flightbay SQL (read-only DuckDB). End statements with ; and type help or exit.")
			in := bufio.NewScanner(cmd.InOrStdin())
			var buf strings.Builder
			fmt.Fprint(out, "fbsql> ")
			for in.Scan() {
				line := strings.TrimSpace(in.Text())
				switch {
				case buf.Len() == 0 && (line == "exit" || line == "quit" || line == `\q`):
					return nil
				case buf.Len() == 0 && (line == "help" || line == `\?`):
					fmt.Fprintln(out, cli.SQLHelp)
				default:
					buf.WriteString(line + " ")
					if strings.HasSuffix(line, ";") {
						if err := cli.SQL(out, u, t, buf.String(), limit); err != nil {
							fmt.Fprintln(out, "error:", err)
						}
						buf.Reset()
					}
				}
				if buf.Len() == 0 {
					fmt.Fprint(out, "fbsql> ")
				} else {
					fmt.Fprint(out, "   ... ")
				}
			}
			return in.Err()
		},
	}
	c.Flags().IntVar(&limit, "limit", 200, "maximum rows")
	return c
}

func flowsCmd() *cobra.Command {
	var cluster, source string
	var limit int
	c := &cobra.Command{
		Use:   "flows",
		Short: "Observed traffic, grouped by source and destination (the fbflows helper)",
		Long: "Who actually called whom: ztunnel, agentgateway, Envoy (sidecars, gateways, waypoints) and CoreDNS logs in the capture,\n" +
			"plus flow logs attached with flightbay collect --attach-flows or added with flightbay-desk open --flows\n" +
			"(Cilium Hubble, Calico, AWS / GCP / Azure VPC flow logs, generic CSV or JSON Lines).",
		Example: "  fbflows\n  fbflows --source agentgateway\n  fbflows --source hubble --cluster mesh1",
		RunE: func(cmd *cobra.Command, _ []string) error {
			u, t, err := deskEnv()
			if err != nil {
				return err
			}
			return cli.Flows(cmd.OutOrStdout(), u, t, cluster, source, limit)
		},
	}
	c.Flags().StringVar(&cluster, "cluster", "", "only this cluster")
	c.Flags().StringVar(&source, "source", "", "ztunnel, agentgateway, envoy, coredns, hubble, calico, aws-vpc, gcp-vpc, azure-vnet or generic")
	c.Flags().IntVar(&limit, "limit", 200, "maximum source/destination pairs")
	return c
}

func reachCmd() *cobra.Command {
	var fix bool
	c := &cobra.Command{
		Use:     "reach [agent]",
		Short:   "Every agent's blast radius, or one agent's network paths and fixes (the fbreach helper; Flightbay Enterprise)",
		Example: "  fbreach                       # every agent, MCP server and agent gateway\n  fbreach kagent/demo-agent     # its paths, identity, findings and destinations\n  fbreach kagent/demo-agent --fix",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			u, t, err := deskEnv()
			if err != nil {
				return err
			}
			agent := ""
			if len(args) == 1 {
				agent = args[0]
			}
			return cli.Reach(cmd.OutOrStdout(), u, t, agent, fix)
		},
	}
	c.Flags().BoolVar(&fix, "fix", false, "print the YAML that locks each finding down")
	return c
}

func featuresCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "features",
		Short: "List Flightbay Enterprise features and whether they are on",
		Long:  "Enterprise features are off by default. Turn them all on with FLIGHTBAY_ENTERPRISE=1, or pick some with FLIGHTBAY_FEATURES=priorities (a leading - turns one off).",
		RunE: func(cmd *cobra.Command, _ []string) error {
			for _, f := range features.List() {
				st := "off"
				if f.Enabled {
					st = "on"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%-12s %-3s  %s\n", f.Name, st, f.Title)
			}
			return nil
		},
	}
}

func postureCmd() *cobra.Command {
	var area string
	var all bool
	var top int
	c := &cobra.Command{
		Use: "posture", Aliases: []string{"best-practices", "score"},
		Short:   "Best-practice scores (agentic, Istio, ingress, Kubernetes) with fixes (the fbscore helper)",
		Example: "  fbscore                 # failing and warning checks, with the YAML to fix them\n  fbscore --area agentic\n  fbscore --all           # every check, including passing and not applicable\n  fbscore --top 10        # the ten fixes that recover the most score (Enterprise)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			u, t, err := deskEnv()
			if err != nil {
				return err
			}
			return cli.Posture(cmd.OutOrStdout(), u, t, area, all, top)
		},
	}
	c.Flags().StringVar(&area, "area", "", "agentic, ambient, gateway or kubernetes")
	c.Flags().IntVar(&top, "top", 0, "rank the N problems that recover the most score (Flightbay Enterprise)")
	c.Flags().BoolVar(&all, "all", false, "show passing and not applicable checks too")
	return c
}

func keygenCmd() *cobra.Command {
	var out string
	c := &cobra.Command{
		Use: "keygen", Short: "Create an age key pair for encrypted bundles (give customers the public recipient)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			id, err := age.GenerateX25519Identity()
			if err != nil {
				return err
			}
			if _, err := os.Stat(out); err == nil {
				return fmt.Errorf("%s already exists; refusing to overwrite a key", out)
			}
			body := fmt.Sprintf("# created: %s\n# public key: %s\n%s\n", time.Now().UTC().Format(time.RFC3339), id.Recipient(), id)
			if err := os.WriteFile(out, []byte(body), 0o600); err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Wrote private identity to %s (keep it secret).\nCustomers encrypt with: flightbay collect --encrypt-to ", out)
			fmt.Fprintln(cmd.OutOrStdout(), id.Recipient())
			return nil
		},
	}
	c.Flags().StringVarP(&out, "output", "o", "flightbay-identity.txt", "identity file to write")
	return c
}
