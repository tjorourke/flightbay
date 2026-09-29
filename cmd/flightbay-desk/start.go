package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/tjorourke/flightbay/internal/cli"
	"github.com/tjorourke/flightbay/internal/review"
	"github.com/tjorourke/flightbay/internal/server"
)

// The start page hands over to the next desk through the environment, not
// the command line, so the token does not show in ps.
const (
	envFixedToken = "FLIGHTBAY_TOKEN_FIXED"  // the session token the next desk must use
	envTempWork   = "FLIGHTBAY_WORKDIR_TEMP" // "1": --workdir was created by the start page; remove it on close unless --keep
)

// deskToken is the session token: the one the start page handed over, so the
// browser's cookie stays valid, or a new random one.
func deskToken() string {
	if t := os.Getenv(envFixedToken); len(t) >= 32 {
		os.Unsetenv(envFixedToken) // not for terminal shells or helpers
		return t
	}
	return randomToken()
}

// tempWorkdir reports whether the start page created --workdir for us.
func tempWorkdir() bool {
	v := os.Getenv(envTempWork) == "1"
	os.Unsetenv(envTempWork)
	return v
}

// startOptions are the flags the start page carries over to the desk it
// hands over to.
type startOptions struct {
	listen, workdir, shell string
	identities             []string
	noBrowser, noTerminal  bool
	allowRemote, keep      bool
}

func startCmd() *cobra.Command {
	var o startOptions
	c := &cobra.Command{
		Use:   "start",
		Short: "Show the start page: live stream a cluster, open a bundle, or scan a repo (what flightbay ui runs with no arguments)",
		Long: `The start page is what flightbay ui and flightbay-desk open show when they are given nothing to open.
It has three cards: Live stream a cluster (pick a kubeconfig context, preview what will be read and the
RBAC it needs, then start live mode), Open a file (recent bundles here and in ~/Downloads, drag and drop
or a path; open it, or review it before you send it), and Open a repo (a directory or a Git URL, scanned
in process with the scan options).

When you choose, the start page replaces itself with the desk for it, on the same address and with the
same session token, so the browser tab carries on without a new sign-in.`,
		Example: `  flightbay ui                                  # the same
  flightbay-desk start --listen 127.0.0.1:0 --no-browser
  flightbay-desk start -i ~/.flightbay/support.key   # open encrypted bundles without asking for the key`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runStart(cmd, o) },
	}
	f := c.Flags()
	f.StringVar(&o.listen, "listen", "127.0.0.1:8484", "loopback address for the start page and the desk it opens")
	f.StringVar(&o.workdir, "workdir", "", "workspace directory for uploads, clones, scans and the desk (default ~/.flightbay/workspaces/ws-*)")
	f.StringSliceVarP(&o.identities, "identity", "i", nil, "age identity file for encrypted bundles")
	f.BoolVar(&o.noBrowser, "no-browser", false, "do not open a browser")
	f.BoolVar(&o.noTerminal, "no-terminal", false, "disable the in-browser terminal in the desk")
	f.BoolVar(&o.keep, "keep", false, "keep the workspace directory after the desk closes")
	f.BoolVar(&o.allowRemote, "allow-remote", false, "allow a non-loopback --listen (for containers); the token is still required")
	f.StringVar(&o.shell, "shell", "", "shell for the desk's terminal (default /bin/bash)")
	return c
}

// execDesk replaces the process with the next desk. A variable so tests can
// capture the hand-off without exec'ing.
var execDesk = func(bin string, argv, env []string) error { return syscall.Exec(bin, argv, env) }

// runChild is the fallback where exec is not available (Windows): run the
// desk as a child on the address the start page has just released.
var runChild = func(bin string, argv, env []string) error {
	c := exec.Command(bin, argv[1:]...)
	c.Stdin, c.Stdout, c.Stderr, c.Env = os.Stdin, os.Stdout, os.Stderr, env
	return c.Run()
}

func runStart(cmd *cobra.Command, o startOptions) error {
	errOut := cmd.ErrOrStderr()
	if !loopback(o.listen) && !o.allowRemote {
		return fmt.Errorf("--listen must be a loopback address in local mode; use --allow-remote in a container")
	}
	ids, err := cli.LoadIdentities(o.identities)
	if err != nil {
		return err
	}
	temp := false
	if o.workdir == "" {
		wsRoot := filepath.Join(home(), "workspaces")
		if err := os.MkdirAll(wsRoot, 0o700); err != nil {
			return err
		}
		sweepWorkspaces(wsRoot, 7*24*time.Hour)
		if o.workdir, err = os.MkdirTemp(wsRoot, "ws-"); err != nil {
			return err
		}
		temp = true
	} else if err := os.MkdirAll(o.workdir, 0o700); err != nil {
		return err
	}
	if abs, err := filepath.Abs(o.workdir); err == nil {
		o.workdir = abs
	}
	for i, id := range o.identities {
		if abs, err := filepath.Abs(id); err == nil {
			o.identities[i] = abs
		}
	}
	var handedOver atomic.Bool
	defer func() {
		if temp && !o.keep && !handedOver.Load() {
			os.RemoveAll(o.workdir)
		}
	}()
	ln, err := net.Listen("tcp", o.listen)
	if err != nil && strings.HasSuffix(o.listen, ":8484") {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		return err
	}
	addr := ln.Addr().String()
	base := "http://" + addr
	token := deskToken()
	desk, _ := os.Executable()
	ss := &server.StartServer{Token: token, WorkDir: o.workdir, Identities: ids, Version: version, LivePlan: livePlan, LiveAccess: liveAccess}
	hs := &http.Server{Handler: ss.Handler(), ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	launched := make(chan error, 1)
	ss.Launch = func(a server.StartAction) error {
		argv, env := handoffCommand(desk, a, o, addr, token, temp)
		fmt.Fprintf(errOut, "Opening: %s\n", strings.Join(argv, " "))
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = hs.Shutdown(sctx) // closes the listener; the next desk binds the same address
		cancel()
		handedOver.Store(true)
		err := execDesk(desk, argv, env)
		if err == nil {
			return nil // only reached when execDesk is replaced (tests)
		}
		// No exec on this platform: run the desk as a child and wait for it.
		launched <- runChild(desk, argv, env)
		return nil
	}
	go func() {
		if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			launched <- err
		}
	}()
	info := deskInfo{URL: base, Token: token, WorkDir: o.workdir, Bundle: "start", PID: os.Getpid()}
	b, _ := json.MarshalIndent(info, "", "  ")
	_ = os.WriteFile(filepath.Join(o.workdir, "desk.json"), b, 0o600)
	url := base + "/?token=" + token
	fmt.Fprintf(errOut, "\nFlightbay start page: live stream a cluster, open a bundle, or scan a repo.\n  UI:          %s\n  Workspace:   %s\n  Press Ctrl+C to quit.\n\n", url, o.workdir)
	if !o.noBrowser {
		openBrowser(url)
	}
	select {
	case err := <-launched:
		return err
	case <-ctx.Done():
		_ = hs.Close()
		fmt.Fprintln(errOut, "Closing the start page.")
		return nil
	}
}

// liveArgs turns the Live card's form into flightbay-desk live flags.
func liveArgs(r server.LiveRequest) []string {
	var a []string
	if r.Kubeconfig != "" {
		a = append(a, "--kubeconfig", r.Kubeconfig)
	}
	if r.Context != "" {
		a = append(a, "--context", r.Context)
	}
	if r.Namespaces != "" {
		a = append(a, "--namespaces", r.Namespaces)
	}
	if r.ExcludeNamespaces != "" {
		a = append(a, "--exclude-namespaces", r.ExcludeNamespaces)
	}
	if r.ClusterScoped {
		a = append(a, "--cluster-scoped")
	}
	if r.SoloDiagnostics {
		a = append(a, "--solo-diagnostics")
	}
	return a
}

// handoffCommand is the argv and environment of the desk the start page
// replaces itself with: the same address, the same token (in the
// environment), the same workspace directory, and no second browser.
func handoffCommand(desk string, a server.StartAction, o startOptions, addr, token string, temp bool) ([]string, []string) {
	common := []string{"--listen", addr, "--workdir", o.workdir, "--no-browser"}
	if o.keep {
		common = append(common, "--keep")
	}
	if o.allowRemote {
		common = append(common, "--allow-remote")
	}
	if o.noTerminal {
		common = append(common, "--no-terminal")
	}
	if o.shell != "" {
		common = append(common, "--shell", o.shell)
	}
	idArgs := func() []string {
		var out []string
		ids := append([]string{}, o.identities...)
		if a.Identity != "" {
			ids = append(ids, a.Identity)
		}
		for _, id := range ids {
			out = append(out, "-i", id)
		}
		return out
	}
	var argv []string
	switch a.Kind {
	case "live":
		argv = append([]string{desk, "live"}, common...)
		if a.Live != nil {
			argv = append(argv, liveArgs(*a.Live)...)
		}
	case "review":
		argv = append([]string{desk, "open", "--review", a.Path}, common...)
		argv = append(argv, idArgs()...)
		// An uploaded bundle lives in the workspace, which is removed on
		// close: write the reviewed bundle to the directory the user is in.
		if rel, err := filepath.Rel(o.workdir, a.Path); err == nil && !strings.HasPrefix(rel, "..") {
			if cwd, err := os.Getwd(); err == nil {
				argv = append(argv, "--review-out", filepath.Join(cwd, filepath.Base(review.OutputName(a.Path, false))))
			}
		}
	default:
		argv = append([]string{desk, "open", a.Path}, common...)
		argv = append(argv, idArgs()...)
	}
	env := []string{}
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); k == envFixedToken || k == envTempWork {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, envFixedToken+"="+token)
	if temp {
		env = append(env, envTempWork+"=1")
	}
	return argv, env
}

// livePlan runs flightbay-desk live --dry-run --json in process: the plan
// and RBAC the Live card previews are exactly the ones the command prints.
func livePlan(ctx context.Context, r server.LiveRequest) (json.RawMessage, error) {
	c := liveCmd()
	var out, errb bytes.Buffer
	c.SetArgs(append(liveArgs(r), "--dry-run", "--json"))
	c.SetOut(&out)
	c.SetErr(&errb)
	c.SilenceUsage, c.SilenceErrors = true, true
	done := make(chan error, 1)
	// Discovery has no deadline of its own; an unreachable API server would
	// hold the request until TCP gives up.
	go func() { done <- c.ExecuteContext(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			return nil, err
		}
	case <-ctx.Done():
		return nil, errors.New("the API server did not answer in time; check the context and that the cluster is reachable")
	}
	b := bytes.TrimSpace(out.Bytes())
	if !json.Valid(b) {
		return nil, fmt.Errorf("unexpected dry-run output: %s", strings.TrimSpace(errb.String()))
	}
	return json.RawMessage(b), nil
}

// liveAccess asks the API server (SelfSubjectAccessReview) what the context's
// credentials allow: listing cluster-scoped objects, and port-forwarding to
// pods in the namespaces in scope, which Solo diagnostics need.
func liveAccess(ctx context.Context, r server.LiveRequest) (server.LiveAccess, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if r.Kubeconfig != "" {
		rules.ExplicitPath = r.Kubeconfig
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: r.Context}).ClientConfig()
	if err != nil {
		return server.LiveAccess{}, err
	}
	cfg.Timeout = 10 * time.Second
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return server.LiveAccess{}, err
	}
	can := func(verb, group, resource, sub, ns string) (bool, error) {
		rv, err := cs.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authorizationv1.SelfSubjectAccessReview{
			Spec: authorizationv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authorizationv1.ResourceAttributes{
				Verb: verb, Group: group, Resource: resource, Subresource: sub, Namespace: ns}}}, metav1.CreateOptions{})
		if err != nil {
			return false, err
		}
		return rv.Status.Allowed, nil
	}
	var a server.LiveAccess
	nodes, err := can("list", "", "nodes", "", "")
	if err != nil {
		return a, err
	}
	roles, err := can("list", "rbac.authorization.k8s.io", "clusterroles", "", "")
	if err != nil {
		return a, err
	}
	a.ClusterScoped = nodes && roles
	if !a.ClusterScoped {
		a.ClusterScopedWhy = "these credentials cannot list nodes and ClusterRoles, so only namespaced objects are read"
	}
	// Port-forward in the first namespace named, or anywhere when none are.
	ns := ""
	for _, n := range strings.Split(r.Namespaces, ",") {
		if n = strings.TrimSpace(n); n != "" && !strings.ContainsAny(n, "*?[") {
			ns = n
			break
		}
	}
	pf, err := can("create", "", "pods", "portforward", ns)
	if err != nil {
		return a, err
	}
	a.SoloDiagnostics = pf
	if !pf {
		where := "in the namespaces in scope"
		if ns != "" {
			where = "in " + ns
		}
		a.SoloDiagnostics, a.SoloDiagnosticsWhy = false, "these credentials cannot port-forward to pods "+where+", which proxy and control-plane diagnostics need"
	}
	return a, nil
}
