// Package cli holds the testable parts of the flightbay and flightbay-desk
// commands: policy flags, bundle inspection and extraction, and the small
// desk client behind the fbq and fbfind shell helpers.
package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"filippo.io/age"
	"github.com/spf13/pflag"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/policy"
	"github.com/tjorourke/flightbay/internal/redact"
	"github.com/tjorourke/flightbay/internal/review"
	"github.com/tjorourke/flightbay/internal/sign"
	"github.com/tjorourke/flightbay/internal/solodocs"
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
	noLogs, noProxyDumps, diagnostics             bool
	maxLogBytes, maxBundleBytes                   int64
	qps, logQPS                                   float32
	burst, concurrency, logConcurrency            int
	redactExtra                                   string
	hubbleFlows                                   int
	includeResources, dropFields                  []string
	policyKeys                                    []string
	source                                        *bundle.PolicySource
	own                                           map[string]bool // the flags Register added
}

// narrowing flags only ever collect less, so they may refine an approved policy.
var narrowing = map[string]bool{"case": true, "exclude-namespaces": true, "exclude-resources": true, "no-logs": true,
	"no-proxy-dumps": true, "no-portforward": true, "qps": true, "burst": true, "concurrency": true, "log-concurrency": true, "log-qps": true, "drop-field": true, "redact-extra": true, "policy-key": true, "config": true}

// Register adds the flags to a flag set.
func (f *PolicyFlags) Register(fs *pflag.FlagSet) {
	before := map[string]bool{}
	fs.VisitAll(func(fl *pflag.Flag) { before[fl.Name] = true })
	defer func() {
		f.own = map[string]bool{}
		fs.VisitAll(func(fl *pflag.Flag) {
			if !before[fl.Name] {
				f.own[fl.Name] = true
			}
		})
	}()
	fs.StringVar(&f.profile, "profile", "standard", "starting profile: minimal, standard or custom")
	fs.StringVar(&f.config, "config", "", "collection config file (YAML); flags refine it, exclusions always win. A CONFIG.sig next to it is checked")
	fs.StringSliceVar(&f.policyKeys, "policy-key", nil, "public keys of whoever approves collection policies; --config must then be signed by one of them and flags may only narrow it")
	fs.StringVar(&f.caseID, "case", "", "support case number, recorded in the bundle")
	fs.StringSliceVar(&f.namespaces, "namespaces", nil, "namespaces to include (globs allowed); default all")
	fs.StringSliceVar(&f.excludeNamespaces, "exclude-namespaces", nil, "namespaces to exclude (globs allowed)")
	fs.StringSliceVar(&f.collect, "collect", nil, "categories: objects,logs,events,nodes,helm,proxies,control-plane,metrics")
	fs.StringSliceVar(&f.resources, "resources", nil, "resource allowlist, e.g. pods,deployments.apps,httproutes.gateway.networking.k8s.io")
	fs.StringSliceVar(&f.includeResources, "include-resources", nil, "allowlist: read only these resource types (same as --resources); --exclude-resources still wins")
	fs.StringSliceVar(&f.excludeResources, "exclude-resources", nil, "resources never to read, e.g. configmaps,secrets")
	fs.StringArrayVar(&f.dropFields, "drop-field", nil, `remove this path from every object before writing, e.g. 'metadata.annotations["corp.example/*"]' (repeatable)`)
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
	fs.BoolVar(&f.diagnostics, "diagnostics", false, "add proxy dumps, control-plane debug and metrics, and allow the port-forward they need")
	fs.BoolVar(&f.noProxyDumps, "no-proxy-dumps", false, "do not collect proxy config dumps")
	fs.StringVar(&f.compression, "compression", "", "best, better, default or fastest")
	fs.Int64Var(&f.maxBundleBytes, "max-bundle-bytes", 0, "stop collecting once the compressed bundle reaches this size")
	fs.Float32Var(&f.qps, "qps", 0, "API server requests per second")
	fs.IntVar(&f.burst, "burst", 0, "API server request burst")
	fs.IntVar(&f.concurrency, "concurrency", 0, "list and get requests in flight at once (default 4); still bounded by --qps")
	fs.IntVar(&f.logConcurrency, "log-concurrency", 0, "log streams read at once (default 8)")
	fs.Float32Var(&f.logQPS, "log-qps", 0, "log requests per second (default 50); they are proxied to kubelets, so they are limited separately")
	fs.StringVar(&f.redactExtra, "redact-extra", "", "file of extra redaction patterns (one Go regular expression per line)")
	fs.IntVar(&f.hubbleFlows, "hubble-flows", 0, "read the last N flows from Cilium hubble-relay (kube-system) through port-forward and the local hubble CLI; --hubble-flows alone means 20000")
	fs.Lookup("hubble-flows").NoOptDefVal = "20000"
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
		if err == nil {
			f.source, err = PolicySource(f.config, f.policyKeys)
		}
		if err == nil {
			fs.Visit(func(fl *pflag.Flag) {
				if f.own[fl.Name] && !narrowing[fl.Name] {
					f.source.ChangedBy = append(f.source.ChangedBy, "--"+fl.Name)
				}
			})
			if len(f.policyKeys) > 0 && len(f.source.ChangedBy) > 0 {
				err = fmt.Errorf("the approved policy %s can only be narrowed on the command line; %s would change what it allows. Edit the file and have it signed again", f.config, strings.Join(f.source.ChangedBy, ", "))
			}
		}
	} else if len(f.policyKeys) > 0 {
		err = errors.New("--policy-key needs --config: the approved policy file to check")
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
	if f.diagnostics {
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
	if ch("include-resources") {
		p.Resources = append(p.Resources[:len(p.Resources):len(p.Resources)], f.includeResources...)
	}
	p.DropFields = append(p.DropFields, f.dropFields...)
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
	if ch("hubble-flows") {
		p.Flows.Hubble = f.hubbleFlows
		if f.hubbleFlows > 0 && !ch("portforward") {
			p.PortForward = true // asking for hubble flows is asking for the port-forward they need
		}
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
	if ch("concurrency") {
		p.Concurrency = f.concurrency
	}
	if ch("log-concurrency") {
		p.Logs.Concurrency = f.logConcurrency
	}
	if ch("log-qps") {
		p.Logs.QPS = f.logQPS
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
	if _, err := NewRedactor(p); err != nil {
		return nil, err
	}
	return p, p.Validate()
}

// Source describes the policy file used, with its signature check; nil
// without --config. Valid after Resolve.
func (f *PolicyFlags) Source() *bundle.PolicySource { return f.source }

// PolicySource hashes a policy file and checks FILE.sig when it exists. With
// trusted keys the signature is required and must be by one of them.
func PolicySource(file string, keyFiles []string) (*bundle.PolicySource, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	src := &bundle.PolicySource{File: filepath.Base(file), SHA256: hex.EncodeToString(sum[:])}
	var trusted []sign.PublicKey
	if len(keyFiles) > 0 {
		if trusted, err = sign.LoadPublic(keyFiles...); err != nil {
			return nil, err
		}
	}
	sigRaw, err := os.ReadFile(file + ".sig")
	switch {
	case os.IsNotExist(err):
		if len(keyFiles) > 0 {
			return nil, fmt.Errorf("%s is not signed (no %s.sig); an approver signs it with: flightbay policy sign %s --key approver.key", file, filepath.Base(file), file)
		}
		return src, nil
	case err != nil:
		return nil, err
	}
	sg, err := sign.Parse(sigRaw)
	if err != nil {
		return nil, fmt.Errorf("%s.sig: %w", file, err)
	}
	res := sign.Verify(sg, sign.PurposePolicy, raw, trusted)
	if !res.Valid {
		return nil, fmt.Errorf("%s does not match its approval signature (%s); it may have been edited after it was signed", file, res.Error)
	}
	if len(keyFiles) > 0 && !res.Trusted {
		return nil, fmt.Errorf("%s is signed by %s (%s), which is not one of the --policy-key keys", file, res.Signer, res.KeyID)
	}
	src.Signature = &res
	return src, nil
}

// NewRedactor builds the redactor a policy asks for: the built-in scrubbers,
// the customer's extra patterns and the fields to drop.
func NewRedactor(p *policy.Policy) (*redact.Redactor, error) {
	rd, err := redact.New(p.RedactExtra)
	if err != nil {
		return nil, err
	}
	if err := rd.DropFields(p.DropFields); err != nil {
		return nil, err
	}
	return rd, nil
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
	Trusted                      []sign.PublicKey // keys to check the signature against
	RequireTrusted               bool             // fail unless signed by one of Trusted
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
	var ledger *review.Ledger
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
		case bundle.PathLedger:
			ledger = &review.Ledger{}
			if json.Unmarshal(m.Data, ledger) != nil {
				ledger = nil
			}
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
		if h.Declared() && h.Repo != nil {
			fmt.Fprintf(w, "Source: declared state from repository %s", h.Repo.Name)
			if h.Repo.Commit != "" {
				fmt.Fprintf(w, " at commit %s", h.Repo.Commit)
			}
			if h.Repo.Branch != "" {
				fmt.Fprintf(w, " (branch %s)", h.Repo.Branch)
			}
			if h.Repo.Dirty {
				fmt.Fprint(w, ", with uncommitted changes")
			}
			fmt.Fprintln(w)
			if h.Repo.Remote != "" {
				fmt.Fprintf(w, "Remote: %s\n", h.Repo.Remote)
			}
		}
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
	sig := r.Signature(o.Trusted)
	fmt.Fprintf(w, "Signature: %s\n", sig.Summary())
	if sig.Trusted && sig.TrustedFrom != "" {
		fmt.Fprintf(w, "Trusted key: %s\n", sig.TrustedFrom)
	}
	if verr == nil && o.RequireTrusted && !(sig.Valid && sig.Trusted) {
		verr = fmt.Errorf("the bundle is not signed by any of the --verify-key keys (%s)", sig.Summary())
	}
	if sum := r.ManifestSHA256(); sum != "" {
		fmt.Fprintf(w, "Manifest SHA-256: %s\n", sum)
	}
	if h != nil && h.CollectorSHA256 != "" {
		fmt.Fprintf(w, "Collector binary SHA-256: %s\n", h.CollectorSHA256)
	}
	if h != nil && h.PolicySource != nil {
		ps := h.PolicySource
		fmt.Fprintf(w, "Policy file: %s (sha256 %s)", ps.File, ps.SHA256)
		if ps.Signature != nil {
			fmt.Fprintf(w, ", approved by %s (%s)", ps.Signature.Signer, ps.Signature.KeyID)
		} else {
			fmt.Fprint(w, ", not signed")
		}
		if len(ps.ChangedBy) > 0 {
			fmt.Fprintf(w, ", refined by %s", strings.Join(ps.ChangedBy, " "))
		}
		fmt.Fprintln(w)
	}
	if h != nil && h.Review != nil {
		fmt.Fprintf(w, "Reviewed: by %s at %s, from bundle %s\n", orDash(h.Review.Reviewer), h.Review.ReviewedAt.Format(time.RFC3339), h.Review.OriginalBundleID)
	}
	if ledger != nil {
		fmt.Fprintf(w, "Original manifest SHA-256: %s\n", ledger.Original.ManifestSHA256)
		if s := ledger.Original.Signature; s != nil {
			fmt.Fprintf(w, "Original signature: %s\n", s.Summary())
		}
		fmt.Fprintf(w, "Removed in review: %s\n", ledger.Summary())
		if len(ledger.Types) > 0 {
			fmt.Fprintf(w, "Types removed: %s\n", strings.Join(ledger.Types, ", "))
		}
		for _, l := range [][2]any{{"Namespaces removed", ledger.Namespaces}, {"Objects removed", ledger.Objects}, {"Log streams removed", ledger.LogStreams}, {"Files removed", ledger.Files}} {
			if list := l[1].([]string); len(list) > 0 {
				fmt.Fprintf(w, "%s: %s\n", l[0], strings.Join(list, ", "))
			}
		}
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
		if c.Type == bundle.CoverageInput {
			target = c.Renderer + " " + c.Source
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
				fmt.Fprintln(w, "Proxy dumps were not selected for this bundle (flightbay collect --diagnostics).")
			} else {
				fmt.Fprintln(w, "No matching components were captured in the selected namespaces.")
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
  flows       observed traffic parsed from Envoy and CoreDNS logs and imported flow logs: cluster, source, src_ip,
              src_namespace, src_pod, src_identity, dst_ip, dst_port, dst_host, dst_namespace, dst_workload, protocol,
              method, path, tool, route, via, verdict, direction, count, file, origin (logs, bundle, reviewer).
              ztunnel and agentgateway flows are read from logs directly: see fbflows or GET /api/flows.
Examples:
  SELECT level, count(*) FROM logs GROUP BY ALL ORDER BY 2 DESC;
  SELECT namespace, name, object->>'$.status.phase' AS phase FROM objects WHERE kind = 'Pod';
  SELECT pod, count(*) FROM logs WHERE level = 'error' GROUP BY ALL ORDER BY 2 DESC LIMIT 10;
  SELECT reason, object_name, message FROM events WHERE type = 'Warning';
  SELECT fields['trace_id'] AS trace, count(*) FROM logs WHERE fields['trace_id'] IS NOT NULL GROUP BY ALL;
  SELECT source, src_pod, dst_host, dst_port, sum(count) FROM flows GROUP BY ALL ORDER BY 5 DESC;`

// Posture prints best-practice scores and what to fix.
func Posture(w io.Writer, base, token, area string, all bool, top int) error {
	var r struct {
		Areas []struct {
			Title, Area, Grade, Reason string
			Present                    bool
			Score                      int
			Categories                 map[string]int
		} `json:"areas"`
		Checks []struct {
			ID, Area, Category, Severity, Title, Status, Reason, Fix, Source, Docs string
			Passed, Total                                                          int
			Points                                                                 float64
			Items                                                                  []struct{ Kind, Namespace, Name, Status, Detail string }
		} `json:"checks"`
		Priorities []struct {
			Rank                                                 int
			ID, Area, Title, Severity, Status, Fix, Source, Docs string
			Points                                               float64
			Failing, Warning, Total                              int
			Objects                                              []string
		} `json:"priorities"`
	}
	q := url.Values{}
	if top > 0 {
		q.Set("top", strconv.Itoa(top))
	}
	if err := deskGet(base, token, "/api/posture", q, &r); err != nil {
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
	if top > 0 {
		fmt.Fprintf(w, "\nFix these first (ranked by severity and share failing; +N is the area score it gives back):\n")
		n := 0
		for _, p := range r.Priorities {
			if area != "" && p.Area != area {
				continue
			}
			n++
			fmt.Fprintf(w, "\n%2d. +%-5.1f [%s] %-8s %s  (%s)\n", p.Rank, p.Points, mark[p.Status], p.Severity, p.Title, p.ID)
			fmt.Fprintf(w, "     %d failing, %d warning of %d: %s\n", p.Failing, p.Warning, p.Total, strings.Join(p.Objects, ", "))
			fmt.Fprintf(w, "     Guide: %s\n", p.Source)
			if p.Docs != "" && p.Docs != p.Source {
				fmt.Fprintf(w, "     %s %s\n", docsLabel(p.Docs), p.Docs)
			}
		}
		if n == 0 {
			fmt.Fprintln(w, "  nothing to fix")
		}
		return nil
	}
	for _, c := range r.Checks {
		if (area != "" && c.Area != area) || (!all && c.Status != "fail" && c.Status != "warn") {
			continue
		}
		pts := ""
		if c.Points > 0 {
			pts = fmt.Sprintf("  +%.1f if fixed", c.Points)
		}
		fmt.Fprintf(w, "\n[%s] %-8s %s  (%d/%d)  %s%s\n", mark[c.Status], c.Severity, c.Title, c.Passed, c.Total, c.ID, pts)
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
			fmt.Fprintf(w, "       Guide: %s\n", c.Source)
			if c.Docs != "" && c.Docs != c.Source {
				fmt.Fprintf(w, "       %s %s\n", docsLabel(c.Docs), c.Docs)
			}
			fmt.Fprintf(w, "       Fix:\n")
			for _, line := range strings.Split(c.Fix, "\n") {
				fmt.Fprintf(w, "         %s\n", line)
			}
		}
	}
	return nil
}

// docsLabel names a documentation link for the relevant product or platform.
func docsLabel(u string) string {
	if solodocs.IsProductDocs(u) {
		return "Product docs:"
	}
	return "Docs:"
}

// Reach prints agent reach (the fbreach helper): every agent's blast radius,
// or one agent's paths, findings and fixes.
func Reach(w io.Writer, base, token, agent string, showFix bool) error {
	type finding struct{ Severity, Title, Detail, Fix, Source, Docs string }
	type agentT struct {
		Namespace, Name, Type, Summary, Radius, Internet, Metadata string
		Counts                                                     struct{ Services, Direct, Closure, Write, Observed, NeverUsed, External, Critical, High int }
		Observed                                                   bool
		ObservedBy                                                 []string
		External                                                   []struct {
			Host, Class, Product, ReachedVia, Gateway, Route, Verdict, Risk, Why string
			Authn, ReadOnly, WriteSeen                                           bool
			Credentials, Methods, Tools, Sources, DeclaredIn                     []string
			Observed                                                             int
		}
		Identity struct {
			ServiceAccount           string
			InMesh, TokenMounted     bool
			Dangerous, Secrets, RBAC []string
			CloudRole, Sandbox       string
		}
		Paths []struct {
			Hops      []string
			Kind, Why string
		}
		Findings []finding
		Direct   []struct {
			To, Verdict, Risk, Why string
			Methods, ObsMethods    []string
			Tools, ObservedBy      []string
			Observed               int
			Declared, Write        bool
		}
	}
	if agent == "" {
		var r struct {
			Agents []agentT
			Notes  []string
			Flat   bool
		}
		if err := deskGet(base, token, "/api/reach", url.Values{}, &r); err != nil {
			return err
		}
		if len(r.Agents) == 0 {
			fmt.Fprintln(w, "No agents, MCP servers or agent gateways found in this capture.")
			return nil
		}
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "AGENT\tTYPE\tRADIUS\tINTERNET\tDIRECT\tTOTAL\tWRITE\tEXTERNAL\tCRIT\tHIGH")
		for _, a := range r.Agents {
			fmt.Fprintf(tw, "%s/%s\t%s\t%s\t%s\t%d/%d\t%d\t%d\t%d\t%d\t%d\n", a.Namespace, a.Name, a.Type, a.Radius, a.Internet, a.Counts.Direct, a.Counts.Services, a.Counts.Closure, a.Counts.Write, a.Counts.External, a.Counts.Critical, a.Counts.High)
		}
		tw.Flush()
		for _, n := range r.Notes {
			fmt.Fprintf(w, "\nNote: %s\n", n)
		}
		fmt.Fprintln(w, "\nfbreach <namespace/name> for one agent's paths and fixes.")
		return nil
	}
	var a agentT
	if err := deskGet(base, token, "/api/reach", url.Values{"agent": {agent}}, &a); err != nil {
		return err
	}
	fmt.Fprintf(w, "%s/%s (%s): radius %s\n%s\n\n", a.Namespace, a.Name, a.Type, a.Radius, a.Summary)
	fmt.Fprintf(w, "Reach: %d of %d Services directly, %d in any number of hops; internet %s; can write to %d; %d external hosts\n",
		a.Counts.Direct, a.Counts.Services, a.Counts.Closure, a.Internet, a.Counts.Write, a.Counts.External)
	if a.Observed {
		fmt.Fprintf(w, "Observed by %s: called %d; %d allowed but never used\n", strings.Join(a.ObservedBy, ", "), a.Counts.Observed, a.Counts.NeverUsed)
	} else {
		fmt.Fprintln(w, "Observed: no flow source covers this agent, so what it uses is unknown")
	}
	id := a.Identity
	fmt.Fprintf(w, "Identity: %s, in mesh %v, API token mounted %v", id.ServiceAccount, id.InMesh, id.TokenMounted)
	if id.CloudRole != "" {
		fmt.Fprintf(w, ", cloud role %s", id.CloudRole)
	}
	if len(id.Secrets) > 0 {
		fmt.Fprintf(w, ", Secrets %s", strings.Join(id.Secrets, ","))
	}
	fmt.Fprintln(w)
	if len(a.Paths) > 0 {
		fmt.Fprintln(w, "\nPaths:")
		for _, p := range a.Paths {
			fmt.Fprintf(w, "  %-8s %s\n", p.Kind, strings.Join(p.Hops, " -> "))
		}
	}
	fmt.Fprintln(w, "\nFindings:")
	for _, f := range a.Findings {
		fmt.Fprintf(w, "  [%s] %s\n      %s\n", strings.ToUpper(f.Severity), f.Title, f.Detail)
		if f.Source != "" {
			fmt.Fprintf(w, "      Guide: %s\n", f.Source)
		}
		if f.Docs != "" && f.Docs != f.Source {
			fmt.Fprintf(w, "      %s %s\n", docsLabel(f.Docs), f.Docs)
		}
		if showFix && f.Fix != "" {
			for _, line := range strings.Split(f.Fix, "\n") {
				fmt.Fprintf(w, "        %s\n", line)
			}
		}
	}
	if len(a.External) > 0 {
		fmt.Fprintln(w, "\nExternal (outside the cluster):")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  RISK\tHOST\tWHAT\tREACHED\tCREDENTIAL\tOBSERVED\tCONFIGURED IN")
		for _, x := range a.External {
			via := x.ReachedVia
			if x.Gateway != "" {
				via += " " + x.Gateway
			}
			if x.ReachedVia == "gateway" {
				if x.Authn {
					via += " (authn"
				} else {
					via += " (no authn"
				}
				if x.ReadOnly {
					via += ", read-only"
				}
				via += ")"
			}
			obs := "-"
			if x.Observed > 0 {
				obs = fmt.Sprintf("%dx %s by %s", x.Observed, strings.Join(x.Methods, ","), strings.Join(x.Sources, ","))
				if len(x.Tools) > 0 {
					obs += " tools " + strings.Join(x.Tools, ",")
				}
			}
			cred := strings.Join(x.Credentials, ", ")
			if cred == "" {
				cred = "-"
			}
			in := strings.Join(x.DeclaredIn, "; ")
			if len(in) > 70 {
				in = in[:67] + "..."
			}
			what := x.Class
			if x.Product != "" && x.Product != x.Class {
				what += ": " + x.Product
			}
			if len(what) > 40 {
				what = what[:37] + "..."
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%s\t%s\n", x.Risk, x.Host, what, via, cred, obs, in)
		}
		tw.Flush()
	}
	shown := 0
	fmt.Fprintln(w, "\nDestinations (risky, used or configured first):")
	for _, e := range a.Direct {
		if shown >= 20 {
			fmt.Fprintf(w, "  ... %d more; see the Agent reach page\n", len(a.Direct)-shown)
			break
		}
		methods := "any method"
		if e.Methods != nil {
			methods = strings.Join(e.Methods, ",")
		}
		used := ""
		if e.Observed > 0 {
			used = fmt.Sprintf(" called %dx %s", e.Observed, strings.Join(e.ObsMethods, ","))
			if len(e.Tools) > 0 {
				used += " tools " + strings.Join(e.Tools, ",")
			}
			if len(e.ObservedBy) > 0 {
				used += " [" + strings.Join(e.ObservedBy, ",") + "]"
			}
		}
		if e.Declared {
			used += " configured"
		}
		fmt.Fprintf(w, "  %-5s %-60s %s%s  %s\n", e.Risk, strings.TrimPrefix(e.To, "svc:"), methods, used, e.Why)
		shown++
	}
	if !showFix {
		fmt.Fprintln(w, "\nAdd --fix for the YAML to lock each finding down.")
	}
	return nil
}

// Flows prints observed traffic grouped by source and destination (the
// fbflows helper): who actually called whom, from every flow source.
func Flows(w io.Writer, base, token, cluster, source string, limit int) error {
	var res struct {
		Total     int            `json:"total"`
		Records   int            `json:"records"`
		BySource  map[string]int `json:"bySource"`
		Truncated bool           `json:"truncated"`
		Flows     []struct {
			Source, SrcIP, SrcNamespace, SrcPod, SrcIdentity string
			DstIP, DstHost, DstNamespace, DstWorkload        string
			DstPort                                          int
			Protocol, Method, Path, Tool, Via, Verdict, File string
			Count                                            int
		} `json:"flows"`
	}
	q := url.Values{"limit": {"100000"}}
	if cluster != "" {
		q.Set("cluster", cluster)
	}
	if source != "" {
		q.Set("source", source)
	}
	if err := deskGet(base, token, "/api/flows", q, &res); err != nil {
		return err
	}
	type group struct {
		source, from, to, proto, verdict, via string
		count                                 int
		details                               map[string]int
	}
	idx := map[string]*group{}
	var groups []*group
	for _, f := range res.Flows {
		from := f.SrcIP
		switch {
		case f.SrcPod != "" && f.SrcNamespace != "":
			from = f.SrcNamespace + "/" + f.SrcPod
		case f.SrcIdentity != "":
			from = f.SrcIdentity
		case from == "" && f.SrcNamespace != "":
			from = f.SrcNamespace + "/*"
		}
		to := f.DstHost
		switch {
		case to != "":
		case f.DstWorkload != "" && f.DstNamespace != "":
			to = f.DstNamespace + "/" + f.DstWorkload
		default:
			to = f.DstIP
		}
		if f.DstPort > 0 {
			to += ":" + strconv.Itoa(f.DstPort)
		}
		k := strings.Join([]string{f.Source, from, to, f.Protocol, f.Verdict, f.Via}, "\x00")
		g := idx[k]
		if g == nil {
			g = &group{source: f.Source, from: from, to: to, proto: f.Protocol, verdict: f.Verdict, via: f.Via, details: map[string]int{}}
			idx[k] = g
			groups = append(groups, g)
		}
		g.count += f.Count
		d := strings.TrimSpace(f.Method + " " + firstNonEmpty(f.Tool, f.Path))
		if d != "" {
			g.details[d] += f.Count
		}
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].source != groups[j].source {
			return groups[i].source < groups[j].source
		}
		return groups[i].count > groups[j].count
	})
	if limit > 0 && len(groups) > limit {
		groups = groups[:limit]
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SOURCE\tFROM\tTO\tPROTO\tVERDICT\tCOUNT\tVIA\tTOP")
	for _, g := range groups {
		var ds []string
		for d := range g.details {
			ds = append(ds, d)
		}
		sort.Slice(ds, func(i, j int) bool {
			return g.details[ds[i]] > g.details[ds[j]] || g.details[ds[i]] == g.details[ds[j]] && ds[i] < ds[j]
		})
		top := ""
		if len(ds) > 0 {
			top = ds[0]
			if len(top) > 60 {
				top = top[:60] + "…"
			}
			if len(ds) > 1 {
				top += fmt.Sprintf(" (+%d more)", len(ds)-1)
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n", g.source, g.from, g.to, dashIfEmpty(g.proto), dashIfEmpty(g.verdict), g.count, dashIfEmpty(g.via), top)
	}
	_ = tw.Flush()
	var srcs []string
	for k, n := range res.BySource {
		srcs = append(srcs, fmt.Sprintf("%s %d", k, n))
	}
	sort.Strings(srcs)
	fmt.Fprintf(w, "-- %d source/destination pairs (showing %d) from %d records", len(idx), len(groups), res.Records)
	if len(srcs) > 0 {
		fmt.Fprintf(w, ": %s", strings.Join(srcs, ", "))
	}
	fmt.Fprintln(w)
	if res.Total == 0 {
		fmt.Fprintln(w, "No observed traffic. Flows come from ztunnel, agentgateway, Envoy and CoreDNS logs in the capture, and from flow logs attached with flightbay collect --attach-flows or flightbay-desk open --flows.")
	}
	return nil
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// FlowFile is one --flows / --attach-flows argument: FILE or FILE@CLUSTER.
type FlowFile struct {
	Path    string
	Cluster string // "" = the first cluster in the bundle
}

// ParseFlowFiles splits FILE[@CLUSTER] arguments and checks each file exists.
func ParseFlowFiles(args []string) ([]FlowFile, error) {
	var out []FlowFile
	for _, a := range args {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		ff := FlowFile{Path: a}
		if _, err := os.Stat(a); err != nil {
			if i := strings.LastIndex(a, "@"); i > 0 {
				ff.Path, ff.Cluster = a[:i], a[i+1:]
			}
		}
		st, err := os.Stat(ff.Path)
		if err != nil {
			return nil, fmt.Errorf("flow file: %w", err)
		}
		if st.IsDir() {
			return nil, fmt.Errorf("flow file %s is a directory", ff.Path)
		}
		out = append(out, ff)
	}
	return out, nil
}

// ResolveFile loads a policy file the way --config does, with no flags.
func (f *PolicyFlags) ResolveFile(file string) (*policy.Policy, error) {
	p, err := policy.Load(file)
	if err != nil {
		return nil, err
	}
	if _, err := NewRedactor(p); err != nil {
		return nil, err
	}
	return p, nil
}

// ParseRecipients reads age recipients: age1... strings or @file.
func ParseRecipients(specs []string) ([]age.Recipient, error) {
	var out []age.Recipient
	for _, s := range specs {
		if strings.HasPrefix(s, "@") {
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
			continue
		}
		r, err := age.ParseX25519Recipient(s)
		if err != nil {
			return nil, fmt.Errorf("--encrypt-to %q: %w", s, err)
		}
		out = append(out, r)
	}
	return out, nil
}
