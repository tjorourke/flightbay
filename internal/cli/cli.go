// Package cli holds the testable parts of the flightbay and flightbay-desk
// commands: policy flags, bundle inspection and extraction, and the small
// desk client behind the fbq and fbfind shell helpers.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"filippo.io/age"
	"github.com/spf13/pflag"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/policy"
	"github.com/tjorourke/flightbay/internal/redact"
)

// PolicyFlags are the collection flags shared by collect, rbac and dry runs.
type PolicyFlags struct {
	profile, config, caseID                       string
	namespaces, excludeNamespaces                 []string
	collect, resources, excludeResources, streams []string
	logPods, logContainers                        []string
	labelSelector, since, configMaps, secrets     string
	objectDetail, compression                     string
	clusterScoped, portForward, noPortForward     bool
	noLogs, noProxyDumps, soloDiagnostics         bool
	maxLogBytes, maxBundleBytes                   int64
	qps                                           float32
	burst                                         int
	redactExtra                                   string
}

// Register adds the flags to a flag set.
func (f *PolicyFlags) Register(fs *pflag.FlagSet) {
	fs.StringVar(&f.profile, "profile", "standard", "starting profile: minimal, standard or custom")
	fs.StringVar(&f.config, "config", "", "collection config file (YAML); flags refine it, exclusions always win")
	fs.StringVar(&f.caseID, "case", "", "support case number, recorded in the bundle")
	fs.StringSliceVar(&f.namespaces, "namespaces", nil, "namespaces to include (globs allowed); default all")
	fs.StringSliceVar(&f.excludeNamespaces, "exclude-namespaces", nil, "namespaces to exclude (globs allowed)")
	fs.StringSliceVar(&f.collect, "collect", nil, "categories: objects,logs,events,nodes,helm,proxies,control-plane,metrics")
	fs.StringSliceVar(&f.resources, "resources", nil, "resource allowlist, e.g. pods,deployments.apps,httproutes.gateway.networking.k8s.io")
	fs.StringSliceVar(&f.excludeResources, "exclude-resources", nil, "resources never to read, e.g. configmaps,secrets")
	fs.StringVar(&f.labelSelector, "label-selector", "", "only collect namespaced objects matching this label selector")
	fs.BoolVar(&f.clusterScoped, "cluster-scoped", false, "also collect cluster-scoped objects (nodes, namespaces, CRDs, ClusterRoles)")
	fs.StringVar(&f.objectDetail, "object-detail", "", "metadata or full")
	fs.StringSliceVar(&f.streams, "log-streams", nil, "log streams: current,previous,init,ephemeral")
	fs.StringVar(&f.since, "since", "", "log window, e.g. 2h")
	fs.Int64Var(&f.maxLogBytes, "max-log-bytes", 0, "per-stream log limit in bytes (keeps the newest lines)")
	fs.StringSliceVar(&f.logPods, "log-pods", nil, "only collect logs from pods matching these globs")
	fs.StringSliceVar(&f.logContainers, "log-containers", nil, "only collect logs from containers matching these globs")
	fs.BoolVar(&f.noLogs, "no-logs", false, "do not collect container logs")
	fs.StringVar(&f.configMaps, "configmaps", "", "omit, metadata or scrubbed")
	fs.StringVar(&f.secrets, "secrets", "", "omit, metadata or keys (values are never collected)")
	fs.BoolVar(&f.portForward, "portforward", false, "allow port-forward based diagnostics")
	fs.BoolVar(&f.noPortForward, "no-portforward", false, "never use port-forward")
	fs.BoolVar(&f.soloDiagnostics, "solo-diagnostics", false, "add proxy dumps, control-plane debug and metrics, and allow the port-forward they need")
	fs.BoolVar(&f.noProxyDumps, "no-proxy-dumps", false, "do not collect proxy config dumps")
	fs.StringVar(&f.compression, "compression", "", "best, better, default or fastest")
	fs.Int64Var(&f.maxBundleBytes, "max-bundle-bytes", 0, "stop collecting once the compressed bundle reaches this size")
	fs.Float32Var(&f.qps, "qps", 0, "API server requests per second")
	fs.IntVar(&f.burst, "burst", 0, "API server request burst")
	fs.StringVar(&f.redactExtra, "redact-extra", "", "file of extra redaction patterns (one Go regular expression per line)")
}

// Resolve builds the policy: profile, then config file, then changed flags.
func (f *PolicyFlags) Resolve(fs *pflag.FlagSet) (*policy.Policy, error) {
	var p *policy.Policy
	var err error
	if f.config != "" {
		p, err = policy.Load(f.config)
		if err == nil && fs.Changed("profile") {
			err = fmt.Errorf("use either --profile or the profile field in --config, not both")
		}
	} else {
		p, err = policy.Profile(f.profile)
	}
	if err != nil {
		return nil, err
	}
	ch := fs.Changed
	if ch("case") {
		p.Case = f.caseID
	}
	if ch("namespaces") {
		p.Namespaces = f.namespaces
	}
	p.ExcludeNamespaces = append(p.ExcludeNamespaces, f.excludeNamespaces...)
	if ch("collect") {
		p.Categories = nil
		for _, c := range f.collect {
			p.Categories = append(p.Categories, policy.Category(strings.TrimSpace(c)))
		}
	}
	if f.soloDiagnostics {
		for _, c := range []policy.Category{policy.CatProxies, policy.CatControlPlane, policy.CatMetrics} {
			if !p.Has(c) {
				p.Categories = append(p.Categories, c)
			}
		}
		p.PortForward = true
	}
	drop := func(cat policy.Category) {
		var cats []policy.Category
		for _, c := range p.Categories {
			if c != cat {
				cats = append(cats, c)
			}
		}
		p.Categories = cats
	}
	if f.noLogs {
		drop(policy.CatLogs)
	}
	if f.noProxyDumps {
		drop(policy.CatProxies)
	}
	if ch("resources") {
		p.Resources = f.resources
	}
	p.ExcludeResources = append(p.ExcludeResources, f.excludeResources...)
	if ch("label-selector") {
		p.LabelSelector = f.labelSelector
	}
	if ch("cluster-scoped") {
		p.ClusterScoped = f.clusterScoped
	}
	if ch("object-detail") {
		p.ObjectDetail = f.objectDetail
	}
	if ch("log-streams") {
		p.Logs.Streams = f.streams
	}
	if ch("since") {
		p.Logs.Since = f.since
	}
	if ch("max-log-bytes") {
		p.Logs.MaxBytes = f.maxLogBytes
	}
	if ch("log-pods") {
		p.Logs.Pods = f.logPods
	}
	if ch("log-containers") {
		p.Logs.Containers = f.logContainers
	}
	if ch("configmaps") {
		p.ConfigMaps = f.configMaps
	}
	if ch("secrets") {
		p.Secrets = f.secrets
	}
	if ch("portforward") {
		p.PortForward = f.portForward
	}
	if f.noPortForward {
		p.PortForward = false
	}
	if ch("compression") {
		p.Compression = f.compression
	}
	if ch("max-bundle-bytes") {
		p.MaxBundleBytes = f.maxBundleBytes
	}
	if ch("qps") {
		p.QPS = f.qps
	}
	if ch("burst") {
		p.Burst = f.burst
	}
	if f.redactExtra != "" {
		b, err := os.ReadFile(f.redactExtra)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(b), "\n") {
			if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
				p.RedactExtra = append(p.RedactExtra, line)
			}
		}
	}
	if _, err := redact.New(p.RedactExtra); err != nil {
		return nil, err
	}
	return p, p.Validate()
}

// LoadIdentities reads age identity files.
func LoadIdentities(files []string) ([]age.Identity, error) {
	var out []age.Identity
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			return nil, err
		}
		ids, err := age.ParseIdentities(fh)
		fh.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		out = append(out, ids...)
	}
	return out, nil
}

// InspectOptions choose what inspect prints.
type InspectOptions struct {
	Contents, Redactions, Policy bool
	Identities                   []age.Identity
}

// Inspect reads a whole bundle, verifies it and prints what it contains.
func Inspect(w io.Writer, src io.Reader, o InspectOptions) error {
	r, err := bundle.NewReader(src, bundle.ReaderOptions{Identities: o.Identities})
	if err != nil {
		return err
	}
	type stat struct {
		files int
		bytes int64
	}
	stats := map[string]*stat{}
	var names []string
	var covs []bundle.Coverage
	var rep redact.Report
	var gaps []string
	var readErr error
	for {
		m, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			readErr = err
			break
		}
		names = append(names, m.Name)
		info, _ := bundle.ParsePath(m.Name)
		k := info.Kind
		if k == "" {
			k = "other"
		}
		if stats[k] == nil {
			stats[k] = &stat{}
		}
		stats[k].files++
		stats[k].bytes += int64(len(m.Data))
		switch info.Kind {
		case bundle.PathCoverage:
			var c bundle.Coverage
			if json.Unmarshal(m.Data, &c) == nil {
				covs = append(covs, c)
			}
		case bundle.PathRedaction:
			_ = json.Unmarshal(m.Data, &rep)
		case bundle.PathCollection:
			for _, line := range strings.Split(strings.TrimSpace(string(m.Data)), "\n") {
				if strings.Contains(line, `"failed"`) || strings.Contains(line, `"partial"`) {
					gaps = append(gaps, line)
				}
			}
		}
	}
	verr := readErr
	if verr == nil {
		verr = r.Verify()
	}
	h := r.Header()
	if h != nil {
		fmt.Fprintf(w, "Bundle: %s\nCase: %s\nCollector: %s\nCaptured: %s\nClusters: %s\n", h.BundleID, orDash(h.Case), orDash(h.Collector),
			h.CaptureStart.Format(time.RFC3339), strings.Join(h.Contexts, ", "))
		if len(h.Planned) > 0 {
			fmt.Fprintf(w, "Collected categories: %s\n", strings.Join(h.Planned, ", "))
		}
		for _, u := range h.Unavailable {
			fmt.Fprintf(w, "Not collected: %s\n", u)
		}
	}
	if m := r.Manifest(); m != nil {
		fmt.Fprintf(w, "Finished: %s (complete: %v, collection errors: %d)\n", m.CaptureEnd.Format(time.RFC3339), m.Complete, m.Errors)
	}
	if r.Encrypted {
		fmt.Fprintln(w, "Encryption: age")
	}
	if verr != nil {
		fmt.Fprintf(w, "Verified: NO: %v\n", verr)
	} else {
		fmt.Fprintln(w, "Verified: yes (every member matches the manifest checksum)")
	}
	fmt.Fprintln(w, "Secret values: never collected")
	if o.Policy && h != nil && len(h.Policy) > 0 {
		var p any
		_ = json.Unmarshal(h.Policy, &p)
		b, _ := json.MarshalIndent(p, "  ", "  ")
		fmt.Fprintf(w, "\nCollection policy:\n  %s\n", b)
	}
	fmt.Fprintln(w, "\nCoverage:")
	tw := tabwriter.NewWriter(w, 2, 2, 2, ' ', 0)
	sort.Slice(covs, func(i, j int) bool { return covs[i].ID < covs[j].ID })
	for _, c := range covs {
		target := c.Resource
		if c.Group != "" {
			target += "." + c.Group
		}
		if c.Type == bundle.CoverageLogs {
			target = fmt.Sprintf("logs %s/%s/%s (%s)", c.Namespace, c.Pod, c.Container, c.Stream)
		}
		state := c.State
		if c.Reason != "" {
			state += ": " + c.Reason
		}
		fmt.Fprintf(tw, "  %s\t%s\t%d items\t%s\n", c.Cluster, target, c.Items, state)
	}
	tw.Flush()
	if len(gaps) > 0 {
		fmt.Fprintf(w, "\nCollection log (%d problems):\n", len(gaps))
		for _, g := range gaps {
			fmt.Fprintf(w, "  %s\n", g)
		}
	}
	fmt.Fprintln(w, "\nMembers by type:")
	kinds := make([]string, 0, len(stats))
	for k := range stats {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		fmt.Fprintf(w, "  %-15s %5d files  %10d bytes\n", k, stats[k].files, stats[k].bytes)
	}
	fmt.Fprintf(w, "\nRedactions: %d (values are never recorded)\n", rep.Total)
	if o.Redactions {
		locs := make([]string, 0, len(rep.Locations))
		for l := range rep.Locations {
			locs = append(locs, l)
		}
		sort.Strings(locs)
		for _, l := range locs {
			var parts []string
			for rule, n := range rep.Locations[l] {
				parts = append(parts, fmt.Sprintf("%s=%d", rule, n))
			}
			sort.Strings(parts)
			fmt.Fprintf(w, "  %s: %s\n", l, strings.Join(parts, ", "))
		}
	}
	if o.Contents {
		fmt.Fprintln(w, "\nContents:")
		for _, n := range names {
			fmt.Fprintf(w, "  %s\n", n)
		}
	}
	return verr
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// Extract writes every member under dir after validating its path, then verifies.
func Extract(src io.Reader, dir string, ids []age.Identity) error {
	r, err := bundle.NewReader(src, bundle.ReaderOptions{Identities: ids})
	if err != nil {
		return err
	}
	for {
		m, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		p := filepath.Join(dir, filepath.FromSlash(m.Name))
		if !strings.HasPrefix(p, filepath.Clean(dir)+string(os.PathSeparator)) {
			return fmt.Errorf("unsafe path %s", m.Name)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, m.Data, 0o644); err != nil {
			return err
		}
	}
	return r.Verify()
}

func deskGet(base, token, path string, q url.Values, v any) error {
	req, err := http.NewRequest("GET", strings.TrimRight(base, "/")+path+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach flightbay-desk at %s: %w", base, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("flightbay-desk: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// Query prints log search results (the fbq helper).
func Query(w io.Writer, base, token, q string, limit int) error {
	var res struct {
		Total   int  `json:"total"`
		Loading bool `json:"loading"`
		Message string
		Records []struct {
			TS        time.Time `json:"ts"`
			Cluster   string    `json:"cluster"`
			Namespace string    `json:"namespace"`
			Pod       string    `json:"pod"`
			Container string    `json:"container"`
			Stream    string    `json:"stream"`
			Level     string    `json:"level"`
			Msg       string    `json:"msg"`
		} `json:"records"`
	}
	if err := deskGet(base, token, "/api/logs/search", url.Values{"q": {q}, "limit": {fmt.Sprint(limit)}}, &res); err != nil {
		return err
	}
	for i := len(res.Records) - 1; i >= 0; i-- {
		r := res.Records[i]
		prev := ""
		if r.Stream == "previous" {
			prev = " (previous)"
		}
		fmt.Fprintf(w, "%s %-5s %s %s/%s/%s%s  %s\n", r.TS.Format("15:04:05.000"), strings.ToUpper(r.Level), r.Cluster, r.Namespace, r.Pod, r.Container, prev, r.Msg)
	}
	fmt.Fprintf(w, "-- %d matching lines (showing %d)", res.Total, len(res.Records))
	if res.Loading {
		fmt.Fprint(w, "; the bundle is still loading, so more may arrive")
	}
	if res.Message != "" {
		fmt.Fprintf(w, "; %s", res.Message)
	}
	fmt.Fprintln(w)
	return nil
}

// Findings prints analyzer results (the fbfind helper).
func Findings(w io.Writer, base, token string) error {
	var fs []struct {
		Severity    string `json:"severity"`
		Title       string `json:"title"`
		Cluster     string `json:"cluster"`
		Detail      string `json:"detail"`
		LogQuery    string `json:"logQuery"`
		Provisional bool   `json:"provisional"`
	}
	if err := deskGet(base, token, "/api/findings", url.Values{}, &fs); err != nil {
		return err
	}
	for _, f := range fs {
		p := ""
		if f.Provisional {
			p = " (provisional: import not verified yet)"
		}
		fmt.Fprintf(w, "[%s] %s: %s%s\n", strings.ToUpper(f.Severity), f.Cluster, f.Title, p)
		if f.Detail != "" {
			fmt.Fprintf(w, "    %s\n", f.Detail)
		}
		if f.LogQuery != "" {
			fmt.Fprintf(w, "    fbq '%s'\n", f.LogQuery)
		}
	}
	if len(fs) == 0 {
		fmt.Fprintln(w, "No findings.")
	}
	return nil
}
