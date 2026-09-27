// Command flightbay-desk opens a Flightbay bundle for investigation: a web UI,
// a read-only Kubernetes API for kubectl/k9s/stern, and a terminal. Data is
// browsable while the bundle is still loading.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/tjorourke/flightbay/internal/cli"
	"github.com/tjorourke/flightbay/internal/diff"
	"github.com/tjorourke/flightbay/internal/importer"
	"github.com/tjorourke/flightbay/internal/server"
	"github.com/tjorourke/flightbay/internal/store"
)

var version = "0.1.0-dev"

func main() {
	root := &cobra.Command{Use: "flightbay-desk", Short: "Flightbay by Solo: investigate a snapshot bundle offline", SilenceUsage: true}
	root.AddCommand(openCmd(), shellCmd(), queryCmd(), findingsCmd(), proxyConfigCmd(), diffCmd(), keygenCmd(), &cobra.Command{
		Use: "version", Short: "Print the version", Run: func(*cobra.Command, []string) { fmt.Println("flightbay-desk", version) },
	})
	if err := root.Execute(); err != nil {
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
	for suf, m := range map[string]int64{"KIB": 1 << 10, "MIB": 1 << 20, "GIB": 1 << 30, "KB": 1000, "MB": 1000 * 1000, "K": 1 << 10, "M": 1 << 20} {
		if strings.HasSuffix(u, suf) {
			mult, u = m, strings.TrimSuffix(u, suf)
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
	var identities, deferCats []string
	var noBrowser, noTerminal bool
	c := &cobra.Command{
		Use:   "open BUNDLE",
		Short: "Open a bundle (file or - for stdin) and start investigating while it loads",
		Example: `  flightbay-desk open flightbay-01234567-20260927T1012Z.tar.zst
  flightbay collect -o - | flightbay-desk open -
  flightbay-desk open bundle.tar.zst.age -i ~/.flightbay/support.key
  flightbay-desk open bundle.tar.zst --defer logs      # load logs later from the UI`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !loopback(listen) {
				return fmt.Errorf("--listen must be a loopback address in local mode (hosted mode is not part of this build)")
			}
			ids, err := cli.LoadIdentities(identities)
			if err != nil {
				return err
			}
			rate, err := parseSize(throttle)
			if err != nil {
				return err
			}
			path := args[0]
			var src io.ReadCloser
			if path == "-" {
				src = io.NopCloser(os.Stdin)
			} else if src, err = os.Open(path); err != nil {
				return err
			}
			if workdir == "" {
				if err := os.MkdirAll(filepath.Join(home(), "workspaces"), 0o700); err != nil {
					return err
				}
				if workdir, err = os.MkdirTemp(filepath.Join(home(), "workspaces"), "ws-"); err != nil {
					return err
				}
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			ws := store.New()
			skip := map[string]bool{}
			for _, d := range deferCats {
				skip[strings.TrimSpace(d)] = true
			}
			opts := importer.Options{Source: path, Throttle: rate, Skip: skip}
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
			srv := &server.Server{WS: ws, Token: randomToken(), WorkDir: workdir, BaseURL: base, Terminal: !noTerminal, Shell: shell, DeskBinary: exe,
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
					o := importer.Options{Source: path}
					o.Reader.Identities = ids
					return importer.Run(context.Background(), f, ws, o)
				}
			}
			if baseline != "" {
				go func() {
					bws, err := loadBundle(ctx, baseline, ids)
					if bws == nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "Baseline not loaded: %v\n", err)
						return
					}
					if err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "Baseline loaded with problems: %v\n", err)
					}
					srv.SetBaseline(bws, baseline)
					fmt.Fprintf(cmd.ErrOrStderr(), "Baseline %s loaded for comparison.\n", baseline)
				}()
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
			fmt.Fprintf(errOut, "\nFlightbay desk is ready while the bundle loads.\n  UI:          %s\n  kubectl:     export KUBECONFIG=%s   (snapshot API on %s)\n  Shell:       flightbay-desk shell   (kubectl, k9s, stern, fbq, fbfind, fbpc)\n  Press Ctrl+C to close the workspace.\n\n", url, srv.KubeconfigPath(), srv.KubeURL)
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
	c.Flags().StringVar(&throttle, "throttle", "", "limit import speed, e.g. 512KiB (rehearse slow uploads)")
	c.Flags().StringVar(&workdir, "workdir", "", "workspace directory (default ~/.flightbay/workspaces/ws-*)")
	c.Flags().BoolVar(&noBrowser, "no-browser", false, "do not open a browser")
	c.Flags().BoolVar(&noTerminal, "no-terminal", false, "disable the in-browser terminal")
	c.Flags().StringVar(&shell, "shell", "", "shell for the terminal (default /bin/bash)")
	c.Flags().StringVar(&baseline, "baseline", "", "an earlier bundle from the same customer to compare against (Compare view)")
	return c
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
		Use: "shell", Short: "Start a shell wired to the open workspace (kubectl, k9s, stern, fbq, fbfind, fbpc)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			d, err := currentDesk()
			if err != nil {
				return err
			}
			sh := os.Getenv("SHELL")
			if sh == "" {
				sh = "/bin/bash"
			}
			c := exec.Command(sh)
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
			c.Env = append(os.Environ(), "KUBECONFIG="+d.Kubeconfig, "FLIGHTBAY_URL="+d.URL, "FLIGHTBAY_TOKEN="+d.Token,
				"PATH="+filepath.Join(d.WorkDir, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
			fmt.Fprintf(cmd.ErrOrStderr(), "Flightbay snapshot shell for %s. kubectl and k9s read the snapshot. Type exit to leave.\n", d.Bundle)
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
