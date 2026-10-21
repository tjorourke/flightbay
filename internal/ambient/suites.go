package ambient

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Test suites: saved, replayable test cases for one AuthorizationPolicy or
// HTTPRoute. A case is a request plus what should happen to it. Running a
// suite compares what happened with what was expected, so the outcome is
// pass or fail instead of a raw status code for a person to interpret.

const (
	KindAuthorizationPolicy = "AuthorizationPolicy"
	KindHTTPRoute           = "HTTPRoute"
	SecurityGroup           = "security.istio.io"

	// Source kinds for Suite.Source.
	SourceWorkload  = "workload"
	SourceEphemeral = "ephemeral"
	SourceExternal  = "external"

	// Case result statuses.
	StatusPass    = "pass"
	StatusFail    = "fail"
	StatusError   = "error"
	StatusSkipped = "skipped"
	// StatusUnknown is a case whose expectation could not be checked: it
	// names a rule or backend and the reply did not say which backend
	// answered. It is neither a pass nor a failure.
	StatusUnknown = "unknown"

	DefaultSamples   = 200
	DefaultTolerance = 5

	// MaxBodyBytes is the largest request body a case may carry.
	MaxBodyBytes = 64 << 10

	// GotUnknownBackend is the got text of a case that could not be checked.
	GotUnknownBackend = "could not tell which backend answered"
)

// Suite is every case for one object, the source they are sent from and the
// result of the last run.
type Suite struct {
	Target Ref          `json:"target"`
	Source SuiteSource  `json:"source"`
	Cases  []Case       `json:"cases"`
	Last   *SuiteRun    `json:"lastRun,omitempty"`
	Saved  bool         `json:"saved"`
	Notes  []string     `json:"notes"`
	Checks []SuiteCheck `json:"checks,omitempty"`
	// Service is the Service the cases are sent to.
	Service Ref `json:"service"`
}

// SuiteSource says where requests are sent from. Ref is the client Pod for a
// workload, the namespace for an ephemeral Pod (Ref.Namespace) and empty for
// an external caller.
type SuiteSource struct {
	Kind string `json:"kind"`
	Ref  Ref    `json:"ref"`
}

// CaseFrom asks for a client with a particular identity. Cases that test
// from.source.principals and namespaces carry it; a run that has no such
// client skips the case instead of sending it from the wrong place.
type CaseFrom struct {
	Namespace      string `json:"namespace,omitempty"`
	ServiceAccount string `json:"serviceAccount,omitempty"`
	Principal      string `json:"principal,omitempty"`
	Workload       *Ref   `json:"workload,omitempty"`
}

type CaseRequest struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers,omitempty"`
	Port    int               `json:"port,omitempty"`
	// Direct sends to a Pod IP, skipping the Service and the waypoint.
	Direct bool `json:"direct"`
	// Host is the Service name the request is addressed to. Empty means the
	// suite's service; with Direct it is ignored and a ready Pod is chosen.
	Host string `json:"host,omitempty"`
	// Body is sent as the request body, for POST, PUT, PATCH and DELETE.
	Body string `json:"body,omitempty"`
}

// bodyMethods are the methods a case may send a body with.
var bodyMethods = map[string]bool{"POST": true, "PUT": true, "PATCH": true, "DELETE": true}

// CaseExpect is one of: a status, a rule and backend set, or weights.
type CaseExpect struct {
	Transport string   `json:"transport,omitempty"`
	Status    int      `json:"status,omitempty"`
	Rule      int      `json:"rule,omitempty"` // 1-based
	Backend   string   `json:"backend,omitempty"`
	Backends  []string `json:"backends,omitempty"`
	NotRoute  bool     `json:"notRoute,omitempty"`
	// Weights are percentages that add up to 100.
	Weights   map[string]int `json:"weights,omitempty"`
	Tolerance int            `json:"tolerance,omitempty"`
	Samples   int            `json:"samples,omitempty"`
	// Text is the expectation as a person reads it. Set by Normalize.
	Text string `json:"text"`
}

type Case struct {
	ID        string      `json:"id"`
	Label     string      `json:"label"`
	Generated bool        `json:"generated"`
	Request   CaseRequest `json:"request"`
	Expect    CaseExpect  `json:"expect"`
	Why       string      `json:"why"`
	From      *CaseFrom   `json:"from,omitempty"`
	// Rule is the 1-based rule of the object the case came from.
	Rule int `json:"rule,omitempty"`
}

type CaseGot struct {
	Transport string             `json:"transport,omitempty"`
	Status    int                `json:"status,omitempty"`
	Backend   string             `json:"backend,omitempty"`
	Rule      int                `json:"rule,omitempty"`
	Weights   map[string]float64 `json:"weights,omitempty"`
	Samples   int                `json:"samples,omitempty"`
	Statuses  map[string]int     `json:"statuses,omitempty"`
	P50Ms     int64              `json:"p50Ms,omitempty"`
	P95Ms     int64              `json:"p95Ms,omitempty"`
	Text      string             `json:"text"`
}

// WeightRow is one line of a route rule's expected-vs-observed table.
type WeightRow struct {
	Backend  string  `json:"backend"`
	Expected int     `json:"expected"`
	Observed float64 `json:"observed"`
	Pass     bool    `json:"pass"`
}

// LogRef points at the log line behind a result.
type LogRef struct {
	Cluster   string    `json:"cluster"`
	Namespace string    `json:"namespace"`
	Pod       string    `json:"pod"`
	At        time.Time `json:"at"`
	ID        int64     `json:"id,omitempty"`
	Line      string    `json:"line,omitempty"`
}

type CaseResult struct {
	ID     string  `json:"id"`
	Status string  `json:"status"`
	Got    CaseGot `json:"got"`
	// DecidedBy names the policy (or layer) that made the call. Empty when
	// nothing could say, with DecisionNote saying why; it never blocks a
	// result.
	DecidedBy     string `json:"decidedBy"`
	DecidedByKind string `json:"decidedByKind,omitempty"` // policy, ztunnel, waypoint, none, unknown
	DecidedByHow  string `json:"decidedByHow,omitempty"`  // waypoint-log, explain, predicted
	DecidedByRef  *Ref   `json:"decidedByRef,omitempty"`
	// DecisionNote says why DecidedBy is empty: no log line matched and no
	// policy predicts the status that came back.
	DecisionNote string      `json:"decisionNote,omitempty"`
	LogRef       *LogRef     `json:"logRef,omitempty"`
	Message      string      `json:"message,omitempty"`
	Weights      []WeightRow `json:"weights,omitempty"`
	// Connection is the connection actually tested, before any hostname
	// (plan 9.2): "In-mesh curl from bookinfo/productpage via the Service".
	Connection string    `json:"connection,omitempty"`
	At         time.Time `json:"at"`
}

type SuiteRun struct {
	At      time.Time    `json:"at"`
	Cluster string       `json:"cluster,omitempty"`
	Source  SuiteSource  `json:"source"`
	Results []CaseResult `json:"results"`
	Summary RunSummary   `json:"summary"`
	// Demo marks canned results from the preview harness.
	Demo bool `json:"demo,omitempty"`
}

type RunSummary struct {
	Total   int `json:"total"`
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Errored int `json:"errored"`
	Skipped int `json:"skipped"`
	// Unknown counts cases that could not be checked (StatusUnknown).
	Unknown int       `json:"unknown,omitempty"`
	At      time.Time `json:"at"`
	// Cancelled is true when the run was stopped before every case ran; the
	// results are the cases that finished.
	Cancelled bool `json:"cancelled,omitempty"`
}

// Behaved is "5 of 6 behaved as expected": passes over everything that ran.
func (s RunSummary) OK() bool { return s.Failed == 0 && s.Errored == 0 }

// SuiteCheck is one "other check" on the object page.
type SuiteCheck struct {
	ID     string       `json:"id"`
	Title  string       `json:"title"`
	Status string       `json:"status"` // pass, warn, info
	Detail string       `json:"detail,omitempty"`
	Count  string       `json:"count,omitempty"`
	Action *CheckAction `json:"action,omitempty"`
}

type CheckAction struct {
	Label   string `json:"label"`
	Kind    string `json:"kind"` // copy-to-cluster, add-timeout, add-retry
	Cluster string `json:"cluster,omitempty"`
}

// Summarize counts results.
func Summarize(results []CaseResult, at time.Time) RunSummary {
	s := RunSummary{Total: len(results), At: at}
	for _, r := range results {
		switch r.Status {
		case StatusPass:
			s.Passed++
		case StatusFail:
			s.Failed++
		case StatusError:
			s.Errored++
		case StatusSkipped:
			s.Skipped++
		case StatusUnknown:
			s.Unknown++
		}
	}
	return s
}

// ---- refs -----------------------------------------------------------------

// SuiteRef builds the Ref for a suite target. Only the two kinds with tests.
func SuiteRef(cluster, kind, ns, name string) (Ref, error) {
	r := Ref{Cluster: cluster, Kind: kind, Namespace: ns, Name: name}
	switch kind {
	case KindAuthorizationPolicy:
		r.Group, r.Resource = SecurityGroup, "authorizationpolicies"
	case KindHTTPRoute:
		r.Group, r.Resource = GatewayGroup, "httproutes"
	default:
		return r, fmt.Errorf("tests exist for AuthorizationPolicy and HTTPRoute, not %q", kind)
	}
	if cluster == "" || ns == "" || name == "" {
		return r, errors.New("a suite ref needs cluster, kind, namespace and name")
	}
	return r, nil
}

// ParseSuiteRef reads "cluster/Kind/namespace/name". A cluster name may
// itself contain slashes, so the last three parts are taken from the right.
func ParseSuiteRef(s string) (Ref, error) {
	parts := strings.Split(strings.Trim(s, "/"), "/")
	if len(parts) < 4 {
		return Ref{}, fmt.Errorf("ref %q is not cluster/Kind/namespace/name", s)
	}
	n := len(parts)
	return SuiteRef(strings.Join(parts[:n-3], "/"), parts[n-3], parts[n-2], parts[n-1])
}

func (r Ref) SuiteString() string {
	return r.Cluster + "/" + r.Kind + "/" + r.Namespace + "/" + r.Name
}

var safeSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// segment makes one path component from a value that may hold anything.
func segment(s string) (string, error) {
	if s == "" {
		return "_", nil
	}
	if safeSegment.MatchString(s) && !strings.Contains(s, "..") {
		return s, nil
	}
	// Context names carry slashes, colons and the like. Keep them readable
	// and unambiguous without allowing traversal.
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == '.':
			b.WriteRune('.')
		default:
			fmt.Fprintf(&b, "%%%02X", r)
		}
	}
	out := strings.ReplaceAll(b.String(), "..", "%2E%2E")
	if out == "" || out[0] == '.' {
		out = "%2E" + strings.TrimPrefix(out, ".")
	}
	return out, nil
}

// SuitePath is <dir>/ambient-suites/<cluster>/<group>/<Kind>/<ns>/<name>.json.
// dir is the state directory the server already uses for ambient records.
func SuitePath(dir string, r Ref) (string, error) {
	if dir == "" {
		return "", errors.New("no state directory")
	}
	if _, err := SuiteRef(r.Cluster, r.Kind, r.Namespace, r.Name); err != nil {
		return "", err
	}
	group := r.Group
	if group == "" {
		group = "core"
	}
	parts := []string{dir, "ambient-suites"}
	for _, p := range []string{r.Cluster, group, r.Kind, r.Namespace} {
		s, err := segment(p)
		if err != nil {
			return "", err
		}
		parts = append(parts, s)
	}
	name, err := segment(r.Name)
	if err != nil {
		return "", err
	}
	return filepath.Join(append(parts, name+".json")...), nil
}

// LoadSuite returns nil, nil when nothing is saved for the object.
func LoadSuite(dir string, r Ref) (*Suite, error) {
	p, err := SuitePath(dir, r)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s Suite
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("saved suite %s is unreadable: %w", p, err)
	}
	s.Target = r
	s.Saved = true
	s.Normalize()
	return &s, nil
}

// SaveSuite writes the suite atomically.
func SaveSuite(dir string, s *Suite) error {
	p, err := SuitePath(dir, s.Target)
	if err != nil {
		return err
	}
	s.Saved = true
	s.Normalize()
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".suite-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	return os.Rename(name, p)
}

// Validate rejects a suite a client should not be able to save.
func (s *Suite) Validate() error {
	if _, err := SuiteRef(s.Target.Cluster, s.Target.Kind, s.Target.Namespace, s.Target.Name); err != nil {
		return err
	}
	switch s.Source.Kind {
	case "", SourceWorkload, SourceEphemeral, SourceExternal:
	default:
		return fmt.Errorf("source kind %q is not workload, ephemeral or external", s.Source.Kind)
	}
	if len(s.Cases) > 200 {
		return errors.New("a suite holds at most 200 cases")
	}
	seen := map[string]bool{}
	for i := range s.Cases {
		c := &s.Cases[i]
		if c.ID == "" {
			return fmt.Errorf("case %d has no id", i+1)
		}
		if seen[c.ID] {
			return fmt.Errorf("duplicate case id %q", c.ID)
		}
		seen[c.ID] = true
		if err := c.Validate(); err != nil {
			return fmt.Errorf("case %q: %w", c.ID, err)
		}
	}
	return nil
}

var validMethods = map[string]bool{"GET": true, "HEAD": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "OPTIONS": true}

func (c *Case) Validate() error {
	m := strings.ToUpper(c.Request.Method)
	if m == "" {
		m = "GET"
	}
	if !validMethods[m] {
		return fmt.Errorf("method %q is not supported", c.Request.Method)
	}
	p := c.Request.Path
	if p != "" && (!strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || len(p) > 4096) {
		return errors.New("path must be relative and start with /")
	}
	if c.Request.Port < 0 || c.Request.Port > 65535 {
		return errors.New("port is out of range")
	}
	if len(c.Request.Headers) > 64 {
		return errors.New("too many headers")
	}
	for k := range c.Request.Headers {
		if strings.TrimSpace(k) == "" {
			return errors.New("a header has no name")
		}
	}
	if c.Request.Body != "" {
		if !bodyMethods[m] {
			return fmt.Errorf("a %s request cannot carry a body; use POST, PUT, PATCH or DELETE", m)
		}
		if len(c.Request.Body) > MaxBodyBytes {
			return fmt.Errorf("the body is %d bytes; at most %d", len(c.Request.Body), MaxBodyBytes)
		}
	}
	e := c.Expect
	if e.Status < 0 || e.Status > 599 {
		return errors.New("expected status is out of range")
	}
	if e.Transport != "" && (e.Transport != "connection-refused-or-reset" || e.Status != 0 || e.Rule != 0 || e.NotRoute || len(e.Weights) > 0 || e.Backend != "" || len(e.Backends) > 0) {
		return errors.New("transport expectation must be connection-refused-or-reset, without an HTTP or route expectation")
	}
	if e.Status == 0 && e.Rule == 0 && !e.NotRoute && len(e.Weights) == 0 && e.Transport == "" {
		return errors.New("the case expects nothing: set a status, a rule or weights")
	}
	if e.Samples > 1000 {
		return errors.New("at most 1000 samples")
	}
	return nil
}

// Normalize fills defaults and the derived text, and orders the cases as
// they were given.
func (s *Suite) Normalize() {
	if s.Cases == nil {
		s.Cases = []Case{}
	}
	if s.Notes == nil {
		s.Notes = []string{}
	}
	for i := range s.Cases {
		c := &s.Cases[i]
		c.Request.Method = strings.ToUpper(c.Request.Method)
		if c.Request.Method == "" {
			c.Request.Method = "GET"
		}
		if c.Request.Path == "" {
			c.Request.Path = "/"
		}
		if len(c.Expect.Weights) > 0 {
			if c.Expect.Samples == 0 {
				c.Expect.Samples = DefaultSamples
			}
			if c.Expect.Tolerance == 0 {
				c.Expect.Tolerance = DefaultTolerance
			}
		}
		c.Expect.Text = ExpectText(c.Expect)
	}
	if s.Last != nil {
		for i := range s.Last.Results {
			if s.Last.Results[i].Got.Text == "" {
				s.Last.Results[i].Got.Text = GotText(s.Last.Results[i].Got)
			}
		}
	}
}

// MergeGenerated replaces generated cases with a fresh set and keeps cases a
// person added. The last run is dropped: its results no longer line up.
func MergeGenerated(saved, fresh *Suite) *Suite {
	out := *fresh
	if saved == nil {
		return &out
	}
	out.Source = saved.Source
	out.Cases = append([]Case{}, fresh.Cases...)
	ids := map[string]bool{}
	for _, c := range fresh.Cases {
		ids[c.ID] = true
	}
	for _, c := range saved.Cases {
		if !c.Generated && !ids[c.ID] {
			out.Cases = append(out.Cases, c)
		}
	}
	out.Saved = false
	return &out
}

// ---- text -----------------------------------------------------------------

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func ExpectText(e CaseExpect) string {
	switch {
	case e.Transport != "":
		return "connection refused or reset (curl 000)"
	case len(e.Weights) > 0:
		parts := []string{}
		for _, k := range sortedKeys(e.Weights) {
			parts = append(parts, fmt.Sprintf("%s %d%%", k, e.Weights[k]))
		}
		return strings.Join(parts, " · ")
	case e.NotRoute:
		return "not this route"
	case e.Rule > 0:
		if len(e.Backends) == 1 {
			return fmt.Sprintf("rule %d → %s", e.Rule, e.Backends[0])
		}
		if e.Backend != "" {
			return fmt.Sprintf("rule %d → %s", e.Rule, e.Backend)
		}
		return fmt.Sprintf("rule %d → split", e.Rule)
	case e.Status > 0:
		return fmt.Sprint(e.Status)
	}
	return ""
}

func GotText(g CaseGot) string {
	switch {
	case g.Transport != "":
		return strings.ReplaceAll(g.Transport, "-", " ") + " (curl 000)"
	case len(g.Weights) > 0:
		parts := []string{}
		for _, k := range sortedKeys(g.Weights) {
			parts = append(parts, fmt.Sprintf("%s %s%%", k, trimFloat(g.Weights[k])))
		}
		return strings.Join(parts, " · ")
	case g.Rule > 0 && g.Backend != "":
		return fmt.Sprintf("rule %d → %s", g.Rule, g.Backend)
	case g.Backend != "" && g.Status == 0:
		return g.Backend
	case g.Status > 0 && g.Backend != "":
		return fmt.Sprintf("%d · %s", g.Status, g.Backend)
	case g.Status > 0:
		return fmt.Sprint(g.Status)
	}
	return ""
}

func trimFloat(f float64) string {
	s := fmt.Sprintf("%.1f", f)
	return strings.TrimSuffix(s, ".0")
}

// ---- evaluation -----------------------------------------------------------

// Sample is one request of a weights case.
type Sample struct {
	Status     int
	Backend    string
	DurationMs int64
}

// Observation is what a runner saw for one case.
type Observation struct {
	Transport  string
	Status     int
	Backend    string // backend Service, when it could be told
	Pod        string
	Samples    []Sample
	Error      string
	Connection string
	// Client is namespace/pod of the sender, for Decided by.
	Client string
	// Rule is the 1-based route rule the request matches, worked out from
	// the route's matches; 0 when it was not worked out.
	Rule int
	// DurationMs is how long a single request took.
	DurationMs int64

	DecidedBy     string
	DecidedByKind string
	DecidedByHow  string
	DecidedByRef  *Ref
	DecisionNote  string
	Log           *LogRef
}

// Evaluate compares an observation with the case's expectation.
func Evaluate(c Case, o Observation, at time.Time) CaseResult {
	r := CaseResult{ID: c.ID, At: at, Connection: o.Connection, DecidedBy: o.DecidedBy, DecidedByKind: o.DecidedByKind, DecidedByHow: o.DecidedByHow, DecidedByRef: o.DecidedByRef, DecisionNote: o.DecisionNote, LogRef: o.Log}
	if r.DecidedBy != "" {
		r.DecisionNote = ""
	}
	e := c.Expect
	if len(e.Weights) > 0 {
		evaluateWeights(&r, e, o)
		r.Got.Text = GotText(r.Got)
		return r
	}
	r.Got.Status, r.Got.Backend, r.Got.Rule = o.Status, o.Backend, o.Rule
	r.Got.Transport = o.Transport
	if o.Error != "" {
		r.Status, r.Message = StatusError, o.Error
		r.Got.Text = ""
		return r
	}
	if e.Transport != "" || o.Transport != "" {
		r.Status = StatusFail
		if e.Transport == "connection-refused-or-reset" && o.Status == 0 && (o.Transport == "connection-reset" || o.Transport == "connection-refused") {
			r.Status = StatusPass
		}
		r.Got.Text = GotText(r.Got)
		if r.Status != StatusPass {
			r.Message = "Expected " + ExpectText(e) + ", got " + r.Got.Text
		}
		return r
	}
	if o.Status == 0 {
		r.Status, r.Message = StatusError, "No HTTP response or classified transport refusal was observed"
		return r
	}
	switch {
	case e.NotRoute:
		switch {
		case o.Backend == "":
			r.Status, r.Got.Text = StatusUnknown, GotUnknownBackend
			r.Message = unknownBackendWhy(o) + " So it cannot say whether " + defaultString(e.Backend, "the Service") + " answered directly."
			return r
		case o.Backend == e.Backend:
			r.Status = StatusPass
		default:
			r.Status = StatusFail
			r.Message = fmt.Sprintf("Expected %s to answer directly, but %s did.", e.Backend, o.Backend)
		}
	case e.Rule > 0:
		switch {
		case o.Backend == "":
			r.Status, r.Got.Text = StatusUnknown, GotUnknownBackend
			r.Message = unknownBackendWhy(o)
			switch {
			case o.Rule == e.Rule:
				r.Message += fmt.Sprintf(" By its method, path and headers the request matches rule %d, as expected, but the backend that answered is not known.", e.Rule)
			case o.Rule > 0:
				r.Message += fmt.Sprintf(" By its method, path and headers the request matches rule %d, not rule %d.", o.Rule, e.Rule)
			}
			return r
		case contains(e.Backends, o.Backend) || (len(e.Backends) == 0 && o.Backend == e.Backend):
			r.Status = StatusPass
			if r.Got.Rule == 0 {
				r.Got.Rule = e.Rule
			}
		default:
			r.Status = StatusFail
			r.Message = fmt.Sprintf("Expected rule %d (%s), but %s answered.", e.Rule, strings.Join(e.Backends, ", "), o.Backend)
		}
	default:
		if o.Status == e.Status {
			r.Status = StatusPass
		} else {
			r.Status = StatusFail
			r.Message = fmt.Sprintf("Expected %d, got %d.", e.Status, o.Status)
		}
	}
	r.Got.Text = GotText(r.Got)
	return r
}

// unknownBackendWhy says why the backend that answered is not known.
func unknownBackendWhy(o Observation) string {
	s := fmt.Sprintf("The reply (%d) did not name the Pod that sent it", o.Status)
	switch {
	case o.Log != nil:
		s += " and the matching waypoint log line had no upstream Service"
	default:
		s += " and no waypoint log line was found for the request"
	}
	return s + ", so Flightbay could not tell which backend answered."
}

func evaluateWeights(r *CaseResult, e CaseExpect, o Observation) {
	n := len(o.Samples)
	if n == 0 {
		r.Status, r.Message = StatusError, firstNonEmpty(o.Error, "No requests completed.")
		return
	}
	counts := map[string]int{}
	statuses := map[string]int{}
	identified := 0
	var durations []int64
	for _, s := range o.Samples {
		statuses[fmt.Sprint(s.Status)]++
		durations = append(durations, s.DurationMs)
		if s.Backend != "" {
			counts[s.Backend]++
			identified++
		}
	}
	r.Got.Samples = n
	r.Got.Statuses = statuses
	r.Got.P50Ms, r.Got.P95Ms = percentile(durations, 50), percentile(durations, 95)
	if identified*2 < n {
		r.Status = StatusError
		r.Message = fmt.Sprintf("Only %d of %d replies could be traced to a backend, so the split cannot be measured.", identified, n)
		return
	}
	tol := float64(e.Tolerance)
	if tol == 0 {
		tol = DefaultTolerance
	}
	r.Got.Weights = map[string]float64{}
	names := map[string]bool{}
	for k := range e.Weights {
		names[k] = true
	}
	for k := range counts {
		names[k] = true
	}
	ordered := sortedKeys(names)
	ok := true
	worst := ""
	for _, k := range ordered {
		obs := math.Round(1000*float64(counts[k])/float64(identified)) / 10
		r.Got.Weights[k] = obs
		row := WeightRow{Backend: k, Expected: e.Weights[k], Observed: obs}
		row.Pass = math.Abs(obs-float64(row.Expected)) <= tol
		if !row.Pass && ok {
			ok = false
			worst = fmt.Sprintf("%s got %s%% where %d%% was expected (tolerance ±%d%%).", k, trimFloat(obs), row.Expected, int(tol))
		}
		r.Weights = append(r.Weights, row)
	}
	if ok {
		r.Status = StatusPass
		r.Message = fmt.Sprintf("Split behaves as configured. Within ±%d%% tolerance at %d requests.", int(tol), n)
		return
	}
	r.Status, r.Message = StatusFail, worst
}

func percentile(v []int64, p int) int64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]int64(nil), v...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	i := (len(s)*p + 99) / 100
	if i < 1 {
		i = 1
	}
	return s[i-1]
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

// ---- decided by -----------------------------------------------------------

var rbacMatched = regexp.MustCompile(`rbac_access_denied_matched_policy\[ns\[([^\]]*)\]-policy\[([^\]]*)\]-rule\[([^\]]*)\]\]`)

// RBACDetail is the policy a waypoint named when it refused a request.
type RBACDetail struct {
	Namespace string
	Policy    string
	Rule      string
	// NoMatch is true for "rbac_access_denied_no_policies_match": no ALLOW
	// policy matched.
	NoMatch bool
	// Backend is the Service the waypoint routed to, from upstream_cluster.
	Backend string
}

// ParseRBACDetail reads the fields of a waypoint access log line, as parsed
// by flows.EnvoyFields (JSON or Istio text format).
func ParseRBACDetail(fields map[string]string) RBACDetail {
	d := RBACDetail{}
	details := fields["response_code_details"]
	if m := rbacMatched.FindStringSubmatch(details); m != nil {
		d.Namespace, d.Policy, d.Rule = m[1], m[2], m[3]
	} else if strings.Contains(details, "rbac_access_denied") {
		d.NoMatch = true
	}
	if parts := strings.Split(fields["upstream_cluster"], "|"); len(parts) == 4 && (parts[0] == "outbound" || parts[0] == "inbound-vip") {
		host := parts[3]
		if i := strings.Index(host, "."); i > 0 {
			host = host[:i]
		}
		d.Backend = host
	}
	return d
}
