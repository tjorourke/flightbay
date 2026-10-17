package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/tjorourke/flightbay/internal/sign"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/tjorourke/flightbay/internal/apiserver"
	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/cli"
	"github.com/tjorourke/flightbay/internal/collect"
	"github.com/tjorourke/flightbay/internal/policy"
	"github.com/tjorourke/flightbay/internal/redact"
	"github.com/tjorourke/flightbay/internal/server"
	"github.com/tjorourke/flightbay/internal/store"
)

func liveCmd() *cobra.Command {
	var pf cli.PolicyFlags
	var kubeconfig, kctx, listen, workdir, shell, saveDir, signKey string
	var selectedContexts []string
	var noBrowser, noTerminal, allowRemote, keep, dryRun, jsonOut bool
	var maxStreams, maxLines int
	var logTail int64
	c := &cobra.Command{
		Use:   "live",
		Short: "Mirror selected clusters into one desk, read-only, and keep them up to date",
		Long: `Live mode connects to selected kubeconfig contexts and keeps one workspace in step with them:
an initial list of every selected resource type, then watches; events as they happen; and
container logs followed as kubectl logs -f would. Everything in the desk works on it: the UI,
search, SQL, the Scorecard, agent reach and findings, and kubectl and k9s in the terminal,
whose watches update as the cluster changes.

It only reads. The requests it makes are get, list, watch and pods/log (plus port-forward to
supported components when --diagnostics is given and you ask for a refresh). A guard in the
client refuses anything else before it leaves the process. Scope and redaction are the ones
flightbay collect uses, with the same flags. Save as bundle writes the mirror as a normal,
verified bundle at any moment.

The Ambient configuration wizard is separate from this read-only mirror. Only explicit
dry-run and apply-for-testing actions use a separately selected real kubeconfig context
to create owned test configuration; the mirror transport and snapshot API remain read-only.`,
		Example: `  flightbay-desk live --context kind-mesh1,kind-mesh2 --namespaces bookinfo,istio-system
  flightbay-desk live --context kind-mesh1 --namespaces kagent,istio-system
  flightbay-desk live --context prod --namespaces payments --exclude-resources configmaps,secrets
  flightbay-desk live --context mesh1 --diagnostics --max-log-streams 50
  flightbay-desk live --context mesh1 --namespaces payments --dry-run      # the plan and the RBAC it needs
  flightbay rbac --live --namespaces payments                               # the same RBAC, without a cluster`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			errOut := cmd.ErrOrStderr()
			p, err := pf.Resolve(cmd.Flags())
			if err != nil {
				return err
			}
			ctxs, err := collect.ResolveContexts(kubeconfig, selectedContexts)
			if err != nil {
				return err
			}
			kctx = strings.Join(ctxs, ",")
			p.Contexts = ctxs
			rd, err := redact.New(p.RedactExtra)
			if err != nil {
				return err
			}
			diagnostics := p.PortForward && (p.Has(policy.CatProxies) || p.Has(policy.CatControlPlane) || p.Has(policy.CatMetrics))
			live := &collect.LiveGroup{}
			for _, contextName := range ctxs {
				cfg, err := collect.RestConfig(kubeconfig, contextName, p, "flightbay-desk-live/"+version)
				if err != nil {
					return err
				}
				guard := &collect.ReadOnlyGuard{AllowPortForward: diagnostics}
				guard.Wrap(cfg)
				cl, err := collect.NewClients(cfg)
				if err != nil {
					return err
				}
				if !diagnostics {
					cl.Forward = nil
				}
				name := contextName
				live.Readers = append(live.Readers, &collect.Live{C: &collect.Collector{Policy: p, Redactor: rd, Version: version, Live: true}, CL: cl, O: collect.LiveOptions{Context: name, Server: cfg.Host, Version: version, MaxLogStreams: maxStreams, LogTail: logTail, MaxLogLines: maxLines, Guard: guard, Progress: func(s string) { fmt.Fprintf(errOut, "  [%s] %s\n", name, s) }}})
			}
			requestSummary := func() string {
				parts := []string{}
				for _, r := range live.Readers {
					parts = append(parts, r.O.Context+": "+r.O.Guard.Summary())
				}
				return strings.Join(parts, "; ")
			}
			if dryRun {
				out := cmd.OutOrStdout()
				plans := []any{}
				for _, reader := range live.Readers {
					pl, err := reader.C.DryRun(cmd.Context(), reader.O.Context, reader.CL)
					if err != nil {
						return fmt.Errorf("%s: %w", reader.O.Context, err)
					}
					plans = append(plans, pl)
					if !jsonOut {
						pl.Print(out, false)
						fmt.Fprintf(out, "\nRequired RBAC (live adds watch):\n%s\n", pl.RBAC)
					}
				}
				if jsonOut {
					var result any = map[string]any{"clusters": plans}
					if len(plans) == 1 {
						result = plans[0]
					}
					b, _ := json.MarshalIndent(result, "", "  ")
					fmt.Fprintln(out, string(b))
					return nil
				}
				fmt.Fprintf(out, "  Live: every type above is listed once, then watched. Events are watched. Logs are followed for up to\n"+
					"    %d container streams at once (--max-log-streams), starting from the last %d lines within --since; a restarted\n"+
					"    container's previous log is read once. At most %d lines are kept (--max-log-lines); the oldest go first.\n", maxStreams, logTail, maxLines)
				if diagnostics {
					fmt.Fprintln(out, "  Diagnostics: read once when the mirror is ready, then only when you press Refresh diagnostics.")
				}
				fmt.Fprintln(out, "  Requests: get, list, watch and pods/log only"+map[bool]string{true: ", plus pods/portforward for diagnostics.", false: "."}[diagnostics])
				return nil
			}
			if !loopback(listen) && !allowRemote {
				return fmt.Errorf("--listen must be a loopback address in local mode; use --allow-remote in a container")
			}
			if !loopback(listen) {
				fmt.Fprintln(errOut, "warning: listening beyond loopback. Anyone who can reach this port and has the token can read the mirror.")
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
					defer os.RemoveAll(workdir)
				}
			} else if tempWorkdir() && !keep {
				defer os.RemoveAll(workdir) // created by the start page
			}
			if err := os.MkdirAll(workdir, 0o700); err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			ws, err := store.Open(filepath.Join(workdir, "workspace.duckdb"))
			if err != nil {
				return fmt.Errorf("cannot create the workspace database: %w", err)
			}
			defer ws.Close()

			ns := "all namespaces"
			if len(p.Namespaces) > 0 {
				ns = strings.Join(p.Namespaces, ", ")
			}
			if len(p.ExcludeNamespaces) > 0 {
				ns += " (excluding " + strings.Join(p.ExcludeNamespaces, ", ") + ")"
			}
			fmt.Fprintf(errOut, "Flightbay live: READ-ONLY mirror of %s\n", kctx)
			for _, r := range live.Readers {
				fmt.Fprintf(errOut, "  %s: %s\n", r.O.Context, r.O.Server)
				r.WS = ws
			}
			fmt.Fprintf(errOut, "  Scope: %s; categories %s\n", ns, strings.Join(categories(p), ", "))
			fmt.Fprintf(errOut, "  Requests: get, list, watch and pods/log only%s. Anything else is refused inside this process.\n",
				map[bool]string{true: ", plus pods/portforward for diagnostics you ask for", false: ""}[diagnostics])
			fmt.Fprintf(errOut, "  Logs: up to %d streams followed, %d lines kept. Redaction: as flightbay collect; Secret values are never kept.\n", maxStreams, maxLines)

			liveDone := make(chan error, 1)
			liveFinished := make(chan struct{})
			liveCtx, cancelLive := context.WithCancel(ctx)
			go func(result chan<- error) { defer close(liveFinished); result <- live.Run(liveCtx) }(liveDone)
			defer func() {
				cancelLive()
				<-liveFinished
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
			srv := &server.Server{WS: ws, Token: deskToken(), WorkDir: workdir, BaseURL: base, Terminal: !noTerminal, Shell: shell, DeskBinary: exe, AmbientKubeconfig: kubeconfig,
				KubeURL: "https://" + kln.Addr().String(), KubeCA: caPEM}
			if live.DiagnosticsEnabled() {
				srv.RefreshClusterDiagnostics = live.RefreshDiagnostics
			}
			srv.SaveBundle = func() (string, error) { return saveLive(ws, saveDir, kctx, p.Compression) }
			srv.FreezeBundle = func(dir string) (string, error) { return saveLive(ws, dir, kctx, "fastest") }
			srv.ExportDir = saveDir
			if signKey != "" {
				if srv.Signer, err = sign.LoadPrivate(signKey); err != nil {
					return fmt.Errorf("--sign: %w", err)
				}
			}
			ks := &http.Server{Handler: srv.KubeHandler(), TLSConfig: tlsCfg, ReadHeaderTimeout: 10 * time.Second}
			go func() { _ = ks.ServeTLS(kln, "", "") }()
			defer ks.Close()
			defer srv.Close()
			hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
			go func() { _ = hs.Serve(ln) }()
			defer hs.Close()

			for ws.Header() == nil {
				select {
				case err := <-liveDone:
					if err == nil {
						err = errors.New("stopped before connecting")
					}
					return fmt.Errorf("cannot mirror %s: %w", kctx, err)
				case <-time.After(20 * time.Millisecond):
				}
			}
			if err := srv.Prepare(); err != nil {
				return err
			}
			unregister, err := registerDesk(deskInfo{URL: base, Token: srv.Token, Kubeconfig: srv.KubeconfigPath(), WorkDir: workdir, Bundle: "live:" + kctx, PID: os.Getpid()})
			defer unregister()
			if err != nil {
				fmt.Fprintf(errOut, "warning: could not record the open workspace for helpers: %v\n", err)
			}
			url := base + "/?token=" + srv.Token
			mirrorContexts := []string{}
			for _, name := range ctxs {
				mirrorContexts = append(mirrorContexts, bundle.SafeID(name)+"@live")
			}
			fmt.Fprintf(errOut, "\nFlightbay desk is mirroring %s live.\n  UI:          %s\n  kubectl:     export KUBECONFIG=%s   (contexts %s, read-only, on %s)\n  Shell:       flightbay-desk shell   (kubectl, k9s, stern, fbq, fbsearch, fbsql, fbfind, fbpc, fbflows)\n  Press Ctrl+C to stop; nothing is kept unless you use Save as bundle or --keep.\n\n",
				kctx, url, srv.KubeconfigPath(), strings.Join(mirrorContexts, ", "), srv.KubeURL)
			if !noBrowser {
				openBrowser(url)
			}
			announced := false
			tick := time.NewTicker(500 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case err := <-liveDone:
					if err != nil {
						fmt.Fprintf(errOut, "Live mirror stopped: %v\n", err)
					}
					liveDone = nil
				case <-tick.C:
					if st := ws.State(); !announced && st.Phase == store.PhaseLive {
						announced = true
						_ = srv.Prepare()
						fmt.Fprintf(errOut, "Live: %d objects, %d events; watching (%s since start). Requests so far: %s\n",
							st.Objects, st.Events, time.Since(st.Started).Round(time.Millisecond), requestSummary())
					}
				case <-ctx.Done():
					fmt.Fprintf(errOut, "Stopping. Requests made to %s: %s\n", kctx, requestSummary())
					return nil
				}
			}
		},
	}
	pf.Register(c.Flags())
	c.Flags().StringVar(&kubeconfig, "kubeconfig", "", "kubeconfig paths separated by the OS path-list separator (default: $KUBECONFIG or ~/.kube/config)")
	c.Flags().StringSliceVar(&selectedContexts, "context", nil, "kubeconfig contexts to mirror, comma-separated or repeated (default: current context)")
	c.Flags().StringVar(&listen, "listen", "127.0.0.1:8484", "loopback address for the UI and API")
	c.Flags().StringVar(&workdir, "workdir", "", "workspace directory (default ~/.flightbay/workspaces/ws-*)")
	c.Flags().BoolVar(&noBrowser, "no-browser", false, "do not open a browser")
	c.Flags().BoolVar(&noTerminal, "no-terminal", false, "disable the in-browser terminal")
	c.Flags().BoolVar(&keep, "keep", false, "keep the workspace directory (DuckDB file, kubeconfig) after stopping")
	c.Flags().BoolVar(&allowRemote, "allow-remote", false, "allow a non-loopback --listen (for containers); the token is still required")
	c.Flags().StringVar(&shell, "shell", "", "shell for the terminal (default /bin/bash)")
	c.Flags().IntVar(&maxStreams, "max-log-streams", 100, "container log streams followed per cluster; beyond it unhealthy pods come first and the rest take turns")
	c.Flags().Int64Var(&logTail, "log-tail", 500, "lines of history per stream when following starts (within --since)")
	c.Flags().IntVar(&maxLines, "max-log-lines", 1_000_000, "log lines kept in the workspace; the oldest are dropped first")
	c.Flags().StringVar(&saveDir, "save-dir", ".", "directory Save as bundle and Review and send write to")
	c.Flags().StringVar(&signKey, "sign", "", "sign bundles exported from Review and send with this private key (flightbay keygen); the page can also choose or create one")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "print what would be watched and the RBAC it needs; reads discovery only")
	c.Flags().BoolVar(&jsonOut, "json", false, "print the dry-run plan as JSON")
	return c
}

func categories(p *policy.Policy) []string {
	var out []string
	for _, c := range p.Plan().Categories {
		out = append(out, string(c))
	}
	return out
}

// saveLive writes the live workspace as a bundle next to where collect would
// put one, then reads it back to prove it verifies.
func saveLive(ws *store.Workspace, dir, kctx, compression string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("flightbay-live-%s-%s.tar.zst", bundle.SafeID(kctx), time.Now().UTC().Format("20060102T150405Z"))
	final := filepath.Join(dir, name)
	if abs, err := filepath.Abs(final); err == nil {
		final = abs
	}
	part := final + ".partial"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	werr := collect.ExportWorkspace(ws, f, collect.ExportOptions{Collector: "flightbay-desk live " + version, Compression: compression})
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(part)
		return "", werr
	}
	if err := verifyBundle(part); err != nil {
		os.Remove(part)
		return "", fmt.Errorf("the saved bundle did not verify: %w", err)
	}
	return final, os.Rename(part, final)
}

func verifyBundle(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	r, err := bundle.NewReader(f, bundle.ReaderOptions{})
	if err != nil {
		return err
	}
	for {
		if _, err := r.Next(); err == io.EOF {
			break
		} else if err != nil {
			return err
		}
	}
	return r.Verify()
}
