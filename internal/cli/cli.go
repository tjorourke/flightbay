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

// ProxyConfigOptions select what fbpc prints.
type ProxyConfigOptions struct {
	Cluster, Namespace, Pod string
	File                    string // print this captured file raw
	Section                 string // only tables whose title contains this
}

type pxComponent struct {
	Cluster   string `json:"cluster"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Pod       string `json:"pod"`
	Component string `json:"component"`
	Version   string `json:"version"`
	Files     []struct {
		Name   string `json:"name"`
		Bytes  int    `json:"bytes"`
		State  string `json:"state"`
		Reason string `json:"reason"`
	} `json:"files"`
}

// ProxyConfig prints a captured component's summary or one of its files
// (the fbpc helper). With no pod it lists what was captured.
func ProxyConfig(w io.Writer, base, token string, o ProxyConfigOptions) error {
	var list struct {
		Components []pxComponent `json:"components"`
		Selected   bool          `json:"selected"`
	}
	if err := deskGet(base, token, "/api/proxies", url.Values{"cluster": {o.Cluster}}, &list); err != nil {
		return err
	}
	if o.Pod == "" {
		if len(list.Components) == 0 {
			if !list.Selected {
				fmt.Fprintln(w, "Proxy dumps were not selected by the customer for this bundle (flightbay collect --solo-diagnostics).")
			} else {
				fmt.Fprintln(w, "No Solo components were captured in the selected namespaces.")
			}
			return nil
		}
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "CLUSTER\tCOMPONENT\tNAMESPACE\tPOD\tVERSION\tFILES")
		for _, c := range list.Components {
			ok := 0
			for _, f := range c.Files {
				if f.Bytes > 0 {
					ok++
				}
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d/%d\n", c.Cluster, c.Component, c.Namespace, c.Pod, c.Version, ok, len(c.Files))
		}
		return tw.Flush()
	}
	var match []pxComponent
	for _, c := range list.Components {
		if (c.Pod == o.Pod || strings.HasPrefix(c.Pod, o.Pod)) && (o.Namespace == "" || c.Namespace == o.Namespace) {
			match = append(match, c)
		}
	}
	switch {
	case len(match) == 0:
		return fmt.Errorf("no captured diagnostics for pod %q; run fbpc with no arguments to list them", o.Pod)
	case len(match) > 1:
		var names []string
		for _, c := range match {
			names = append(names, c.Cluster+" "+c.Namespace+"/"+c.Pod)
		}
		return fmt.Errorf("%q matches several pods, narrow with -n or --cluster: %s", o.Pod, strings.Join(names, ", "))
	}
	c := match[0]
	q := url.Values{"cluster": {c.Cluster}, "kind": {c.Kind}, "namespace": {c.Namespace}, "pod": {c.Pod}}
	if o.File != "" {
		q.Set("file", o.File)
		req, _ := http.NewRequest("GET", strings.TrimRight(base, "/")+"/api/proxy/file?"+q.Encode(), nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("%s was not captured for %s (see fbpc %s for the files and reasons)", o.File, c.Pod, c.Pod)
		}
		_, err = io.Copy(w, resp.Body)
		return err
	}
	var p struct {
		Summary struct {
			Counts map[string]int `json:"counts"`
			Notes  []string       `json:"notes"`
			Certs  []struct {
				Identity  string    `json:"identity"`
				Root      bool      `json:"root"`
				NotAfter  time.Time `json:"notAfter"`
				HoursLeft float64   `json:"hoursLeft"`
			} `json:"certs"`
			Tables []struct {
				Title   string     `json:"title"`
				Columns []string   `json:"columns"`
				Rows    [][]string `json:"rows"`
			} `json:"tables"`
		} `json:"summary"`
	}
	if err := deskGet(base, token, "/api/proxy", q, &p); err != nil {
		return err
	}
	fmt.Fprintf(w, "%s %s/%s (%s %s) on %s@snapshot\n", c.Component, c.Namespace, c.Pod, c.Component, c.Version, c.Cluster)
	keys := make([]string, 0, len(p.Summary.Counts))
	for k := range p.Summary.Counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var counts []string
	for _, k := range keys {
		counts = append(counts, fmt.Sprintf("%s=%d", k, p.Summary.Counts[k]))
	}
	if len(counts) > 0 {
		fmt.Fprintln(w, strings.Join(counts, "  "))
	}
	for _, n := range p.Summary.Notes {
		fmt.Fprintln(w, "note:", n)
	}
	sec := strings.ToLower(o.Section)
	for _, t := range p.Summary.Tables {
		if sec != "" && !strings.Contains(strings.ToLower(t.Title), sec) {
			continue
		}
		fmt.Fprintf(w, "\n== %s (%d)\n", t.Title, len(t.Rows))
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, strings.ToUpper(strings.Join(t.Columns, "\t")))
		for _, r := range t.Rows {
			for i := range r {
				if len(r[i]) > 90 {
					r[i] = r[i][:90] + "…"
				}
			}
			fmt.Fprintln(tw, strings.Join(r, "\t"))
		}
		_ = tw.Flush()
	}
	if len(p.Summary.Certs) > 0 && (sec == "" || strings.Contains("certificates", sec)) {
		fmt.Fprintf(w, "\n== Certificates (%d)\n", len(p.Summary.Certs))
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "IDENTITY\tTYPE\tEXPIRES\tHOURS LEFT")
		for _, ct := range p.Summary.Certs {
			typ := "workload"
			if ct.Root {
				typ = "root"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%.1f\n", ct.Identity, typ, ct.NotAfter.Format(time.RFC3339), ct.HoursLeft)
		}
		_ = tw.Flush()
	}
	fmt.Fprintln(w, "\nFiles:")
	for _, f := range c.Files {
		st := f.State
		if f.Reason != "" {
			st += ": " + f.Reason
		}
		fmt.Fprintf(w, "  %-18s %8d bytes  %s\n", f.Name, f.Bytes, st)
	}
	fmt.Fprintf(w, "Raw file: fbpc %s --file config_dump.json | jq\n", c.Pod)
	return nil
}

// Search prints objects of any kind matching a query (the fbsearch helper).
func Search(w io.Writer, base, token, q string, limit int) error {
	var res struct {
		Total   int            `json:"total"`
		ByKind  map[string]int `json:"byKind"`
		Message string         `json:"message"`
		Results []struct {
			Cluster, Kind, Namespace, Name, Match, Snippet string
		} `json:"results"`
	}
	if err := deskGet(base, token, "/api/objects/search", url.Values{"q": {q}, "limit": {fmt.Sprint(limit)}}, &res); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KIND\tNAMESPACE\tNAME\tCLUSTER\tMATCH")
	for _, r := range res.Results {
		m := r.Match
		if r.Snippet != "" {
			s := strings.Join(strings.Fields(r.Snippet), " ")
			if len(s) > 80 {
				s = s[:80] + "…"
			}
			m += ": " + s
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.Kind, r.Namespace, r.Name, r.Cluster, m)
	}
	_ = tw.Flush()
	var kinds []string
	for k, n := range res.ByKind {
		kinds = append(kinds, fmt.Sprintf("%s %d", k, n))
	}
	sort.Strings(kinds)
	fmt.Fprintf(w, "-- %d objects (showing %d)", res.Total, len(res.Results))
	if len(kinds) > 0 {
		fmt.Fprintf(w, ": %s", strings.Join(kinds, ", "))
	}
	if res.Message != "" {
		fmt.Fprintf(w, "\n%s", res.Message)
	}
	fmt.Fprintln(w)
	return nil
}

// SQL runs one read-only query and prints a table (the fbsql helper).
func SQL(w io.Writer, base, token, q string, limit int) error {
	var res struct {
		Columns   []string `json:"columns"`
		Rows      [][]any  `json:"rows"`
		Truncated bool     `json:"truncated"`
		Ms        int64    `json:"ms"`
	}
	if err := deskGet(base, token, "/api/sql", url.Values{"q": {q}, "limit": {fmt.Sprint(limit)}}, &res); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(res.Columns, "\t"))
	for _, r := range res.Rows {
		cells := make([]string, len(r))
		for i, v := range r {
			switch t := v.(type) {
			case nil:
				cells[i] = "NULL"
			case string:
				cells[i] = t
			default:
				b, _ := json.Marshal(t)
				cells[i] = string(b)
			}
			cells[i] = strings.ReplaceAll(cells[i], "\n", " ")
			if len(cells[i]) > 120 {
				cells[i] = cells[i][:120] + "…"
			}
		}
		fmt.Fprintln(tw, strings.Join(cells, "\t"))
	}
	_ = tw.Flush()
	more := ""
	if res.Truncated {
		more = fmt.Sprintf(" (first %d; raise --limit)", len(res.Rows))
	}
	fmt.Fprintf(w, "-- %d rows%s in %d ms\n", len(res.Rows), more, res.Ms)
	return nil
}

// SQLHelp describes the database for the fbsql prompt.
const SQLHelp = `Tables (read-only, DuckDB SQL):
  logs        id, time, cluster, namespace, pod, container, stream, node, level, scope, msg, fields (map), raw
  log_lines   the same with ts as Unix nanoseconds, seq and chunk
  objects     cluster, api_group, version, resource, kind, namespace, name, labels (map), object (JSON text)
  events      cluster, namespace, type, reason, object_kind, object_name, message, count, last_seen, event (JSON text)
Examples:
  SELECT level, count(*) FROM logs GROUP BY ALL ORDER BY 2 DESC;
  SELECT namespace, name, object->>'$.status.phase' AS phase FROM objects WHERE kind = 'Pod';
  SELECT pod, count(*) FROM logs WHERE level = 'error' GROUP BY ALL ORDER BY 2 DESC LIMIT 10;
  SELECT reason, object_name, message FROM events WHERE type = 'Warning';
  SELECT fields['trace_id'] AS trace, count(*) FROM logs WHERE fields['trace_id'] IS NOT NULL GROUP BY ALL;`

// Posture prints best-practice scores and what to fix.
func Posture(w io.Writer, base, token, area string, all bool) error {
	var r struct {
		Areas []struct {
			Title, Area, Grade, Reason string
			Present                    bool
			Score                      int
			Categories                 map[string]int
		} `json:"areas"`
		Checks []struct {
			ID, Area, Category, Severity, Title, Status, Reason, Fix, Source string
			Passed, Total                                                    int
			Items                                                            []struct{ Kind, Namespace, Name, Status, Detail string }
		} `json:"checks"`
	}
	if err := deskGet(base, token, "/api/posture", url.Values{}, &r); err != nil {
		return err
	}
	for _, a := range r.Areas {
		if area != "" && a.Area != area {
			continue
		}
		if !a.Present {
			fmt.Fprintf(w, "%s: not scored (%s)\n", a.Title, a.Reason)
			continue
		}
		var cats []string
		for k, v := range a.Categories {
			cats = append(cats, fmt.Sprintf("%s %d", k, v))
		}
		sort.Strings(cats)
		fmt.Fprintf(w, "%s: %d/100 (%s)   %s\n", a.Title, a.Score, a.Grade, strings.Join(cats, " · "))
	}
	mark := map[string]string{"fail": "FAIL", "warn": "WARN", "pass": "PASS", "na": "N/A "}
	for _, c := range r.Checks {
		if (area != "" && c.Area != area) || (!all && c.Status != "fail" && c.Status != "warn") {
			continue
		}
		fmt.Fprintf(w, "\n[%s] %-8s %s  (%d/%d)  %s\n", mark[c.Status], c.Severity, c.Title, c.Passed, c.Total, c.ID)
		if c.Reason != "" {
			fmt.Fprintf(w, "       %s\n", c.Reason)
		}
		for i, it := range c.Items {
			if it.Status == "pass" || i >= 8 {
				continue
			}
			obj := strings.Trim(it.Kind+" "+strings.Trim(it.Namespace+"/"+it.Name, "/"), " ")
			fmt.Fprintf(w, "       - %s: %s\n", obj, it.Detail)
		}
		if c.Status == "fail" || c.Status == "warn" {
			fmt.Fprintf(w, "       Fix (see %s):\n", c.Source)
			for _, line := range strings.Split(c.Fix, "\n") {
				fmt.Fprintf(w, "         %s\n", line)
			}
		}
	}
	return nil
}
