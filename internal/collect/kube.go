package collect

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	corev1 "k8s.io/api/core/v1"
	k8sdiscovery "k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/flowcontrol"

	"github.com/tjorourke/flightbay/internal/policy"
)

// RestConfig loads one kubeconfig context with the policy's rate limits.
func RestConfig(kubeconfig, context string, p *policy.Policy, userAgent string) (*rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: context}).ClientConfig()
	if err != nil {
		return nil, err
	}
	cfg.QPS, cfg.Burst = p.QPS, p.Burst
	cfg.UserAgent = userAgent
	cfg.WarningHandler = rest.NoWarnings{}
	return cfg, nil
}

// ResolveContexts returns the requested contexts, or the current one, after
// checking that each exists in the kubeconfig.
func ResolveContexts(kubeconfig string, contexts []string) ([]string, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}
	raw, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).RawConfig()
	if err != nil {
		return nil, err
	}
	if len(contexts) == 0 {
		if raw.CurrentContext == "" {
			return nil, fmt.Errorf("no current context; pass --context")
		}
		contexts = []string{raw.CurrentContext}
	}
	for _, c := range contexts {
		if _, ok := raw.Contexts[c]; !ok {
			return nil, fmt.Errorf("context %q not found in kubeconfig", c)
		}
	}
	return contexts, nil
}

type logSource struct{ cs kubernetes.Interface }

func (l logSource) Stream(ctx context.Context, ns, pod string, o *corev1.PodLogOptions) (io.ReadCloser, error) {
	return l.cs.CoreV1().Pods(ns).GetLogs(pod, o).Stream(ctx)
}

// NewLogSource is a log client with its own rate limit. A log request is
// proxied by the API server to a kubelet rather than served from etcd or the
// watch cache, so logs are limited separately from list and get requests.
func NewLogSource(cfg *rest.Config, qps float32, burst int) (LogSource, error) {
	c := rest.CopyConfig(cfg)
	c.RateLimiter = nil // its own budget, never the shared list limiter
	if qps > 0 {
		c.QPS, c.Burst = qps, max(burst, int(qps))
	}
	cs, err := kubernetes.NewForConfig(c)
	if err != nil {
		return nil, err
	}
	return logSource{cs}, nil
}

// NewClients builds the discovery, dynamic, metadata, log and port-forward
// clients for one cluster.
func NewClients(cfg *rest.Config) (Clients, error) {
	// One rate limiter for every client, so the policy's QPS is the budget
	// for the whole collector rather than for each client separately.
	if cfg.RateLimiter == nil && cfg.QPS > 0 {
		cfg = rest.CopyConfig(cfg)
		cfg.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(cfg.QPS, max(cfg.Burst, 1))
	}
	disc, err := k8sdiscovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return Clients{}, err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return Clients{}, err
	}
	meta, err := metadata.NewForConfig(cfg)
	if err != nil {
		return Clients{}, err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return Clients{}, err
	}
	return Clients{Discovery: disc, Dynamic: dyn, Metadata: meta, Logs: logSource{cs}, Forward: &forwarder{cfg: cfg, cs: cs}, Typed: NewProtoLister(cfg)}, nil
}

// ReadOnlyGuard is a safety net under every live-mode client: a transport
// that lets reads through (get, list, watch, pods/log, discovery) and refuses
// anything else before it leaves the process. Port-forward, which diagnostics
// need, is let through only when AllowPortForward is set. It also counts the
// requests it passes, by verb, so a run can prove what it asked the cluster.
type ReadOnlyGuard struct {
	AllowPortForward bool

	mu      sync.Mutex
	counts  map[string]int64
	refused atomic.Int64
	last    string
}

// ErrWriteRefused is returned for any request the guard blocks.
var ErrWriteRefused = fmt.Errorf("flightbay live is read-only")

// Wrap installs the guard on a rest.Config. Every client built from the
// config afterwards, including port-forward dialers, goes through it.
func (g *ReadOnlyGuard) Wrap(cfg *rest.Config) {
	prev := cfg.WrapTransport
	cfg.WrapTransport = func(rt http.RoundTripper) http.RoundTripper {
		if prev != nil {
			rt = prev(rt)
		}
		return guardRT{g: g, next: rt}
	}
}

type guardRT struct {
	g    *ReadOnlyGuard
	next http.RoundTripper
}

func (t guardRT) RoundTrip(r *http.Request) (*http.Response, error) {
	verb, ok := t.g.Classify(r.Method, r.URL.Path, r.URL.Query().Get("watch"))
	if !ok {
		t.g.refused.Add(1)
		t.g.mu.Lock()
		t.g.last = r.Method + " " + r.URL.Path
		t.g.mu.Unlock()
		if r.Body != nil {
			_ = r.Body.Close()
		}
		return nil, fmt.Errorf("%w: refused %s %s", ErrWriteRefused, r.Method, r.URL.Path)
	}
	t.g.mu.Lock()
	if t.g.counts == nil {
		t.g.counts = map[string]int64{}
	}
	t.g.counts[verb]++
	t.g.mu.Unlock()
	return t.next.RoundTrip(r)
}

// Classify names the Kubernetes verb of a request and says whether the guard
// allows it.
func (g *ReadOnlyGuard) Classify(method, path, watchParam string) (string, bool) {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	last := segs[len(segs)-1]
	switch method {
	case http.MethodGet, http.MethodHead:
	case http.MethodPost:
		if last == "portforward" && g.AllowPortForward {
			return "portforward", true
		}
		return "", false
	default:
		return "", false
	}
	// GET can still open a session: exec and attach over websockets, and the
	// API server proxy reaches the pod's own endpoints.
	for _, s := range segs {
		if s == "exec" || s == "attach" || s == "proxy" {
			return "", false
		}
	}
	if last == "portforward" {
		if g.AllowPortForward {
			return "portforward", true
		}
		return "", false
	}
	if watchParam == "true" || watchParam == "1" {
		return "watch", true
	}
	// /api, /apis, /apis/<g>, /apis/<g>/<v>, /version, /openapi: discovery.
	var rest []string
	switch {
	case len(segs) >= 2 && segs[0] == "api":
		rest = segs[2:]
	case len(segs) >= 3 && segs[0] == "apis":
		rest = segs[3:]
	default:
		return "discovery", true
	}
	if len(rest) == 0 {
		return "discovery", true
	}
	if rest[0] == "namespaces" && len(rest) >= 3 {
		rest = rest[2:]
	}
	switch {
	case len(rest) == 1:
		return "list", true
	case len(rest) == 3 && rest[2] == "log":
		return "log", true
	default:
		return "get", true
	}
}

// Counts returns requests passed, by verb, and how many were refused.
func (g *ReadOnlyGuard) Counts() (map[string]int64, int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make(map[string]int64, len(g.counts))
	for k, v := range g.counts {
		out[k] = v
	}
	return out, g.refused.Load()
}

// Summary is a one-line account of the requests made.
func (g *ReadOnlyGuard) Summary() string {
	c, refused := g.Counts()
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k, c[k]))
	}
	return fmt.Sprintf("%s; refused %d", strings.Join(parts, ", "), refused)
}
