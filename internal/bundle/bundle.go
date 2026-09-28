// Package bundle writes and reads Flightbay bundles: a plain tar stream,
// compressed with zstd and optionally wrapped in age encryption.
//
// Layout rules that make progressive import possible:
//   - bundle.json is always the first member;
//   - data arrives in bounded chunks, each a complete tar member;
//   - a coverage record follows each finished collection;
//   - manifest.json, with a sha256 for every member, is always last.
package bundle

import (
	"archive/tar"
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"filippo.io/age"
	"github.com/klauspost/compress/zstd"

	"github.com/tjorourke/flightbay/internal/sign"
)

const (
	SchemaVersion = 1
	HeaderName    = "bundle.json"
	ManifestName  = "manifest.json"
	RedactionName = "redaction-report.json"
	CollectionLog = "collection-log.jsonl"
	// SignatureName is the customer's signature over manifest.json. It is the
	// only member allowed after the manifest, and it is optional.
	SignatureName = "manifest.sig"
	// LedgerName records what a customer removed in review (flightbay review).
	LedgerName       = "review-ledger.json"
	MaxWindow        = 128 << 20 // stock zstd and tar --zstd open this without extra flags
	DefaultWindow    = 8 << 20
	ageMagic         = "age-encryption.org/v1"
	DefaultMaxMember = 512 << 20
)

// Header is bundle.json. It is known before collection starts.
type Header struct {
	SchemaVersion int             `json:"schemaVersion"`
	BundleID      string          `json:"bundleId"`
	Case          string          `json:"case,omitempty"`
	Collector     string          `json:"collector,omitempty"`
	CaptureStart  time.Time       `json:"captureStart"`
	Contexts      []string        `json:"contexts"`
	Policy        json.RawMessage `json:"policy,omitempty"`
	Planned       []string        `json:"planned,omitempty"`
	Unavailable   []string        `json:"unavailable,omitempty"`
	// Source is "" or "cluster" for a capture and "repo" for a scan of a code
	// repository (flightbay scan): declared state, with no status, pods or logs.
	Source string    `json:"source,omitempty"`
	Repo   *RepoInfo `json:"repo,omitempty"`
	// CollectorSHA256 is the SHA-256 of the collector binary that wrote the
	// bundle, so either side can match it to a signed release.
	CollectorSHA256 string `json:"collectorSha256,omitempty"`
	// PolicySource describes the approved collection policy file, when one was used.
	PolicySource *PolicySource `json:"policySource,omitempty"`
	// Review is set on a bundle the customer trimmed with flightbay review.
	Review *ReviewInfo `json:"review,omitempty"`
}

// PolicySource records which collection policy file a capture used and who approved it.
type PolicySource struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	// Signature is the check of POLICY.sig at collection time; nil when the
	// file was not signed.
	Signature *sign.Result `json:"signature,omitempty"`
	// ChangedBy lists the command-line flags that refined the file.
	ChangedBy []string `json:"changedBy,omitempty"`
}

// ReviewInfo links a reviewed bundle to the capture it was made from. The
// original's manifest hash and signature are in review-ledger.json, which is
// written at the end, once the original has been read and verified.
type ReviewInfo struct {
	OriginalBundleID string    `json:"originalBundleId"`
	Reviewer         string    `json:"reviewer,omitempty"`
	ReviewedAt       time.Time `json:"reviewedAt"`
}

// SourceRepo marks a bundle built by flightbay scan from a code repository.
const SourceRepo = "repo"

// RepoInfo describes the repository a declared-state bundle was built from.
type RepoInfo struct {
	Name    string         `json:"name"`
	Path    string         `json:"path,omitempty"`
	Remote  string         `json:"remote,omitempty"`
	Commit  string         `json:"commit,omitempty"`
	Branch  string         `json:"branch,omitempty"`
	Dirty   bool           `json:"dirty,omitempty"`
	Options map[string]any `json:"options,omitempty"` // render options: values, overlays, env, release, namespace
}

// Declared reports whether the header describes a repo scan.
func (h *Header) Declared() bool { return h != nil && h.Source == SourceRepo }

// Coverage types and states.
const (
	CoverageResource = "resource"
	CoverageLogs     = "logs"
	CoverageEvents   = "events"
	// CoverageDiagnostic is a proxy dump, control-plane debug output or metrics scrape.
	CoverageDiagnostic = "diagnostic"
	// CoverageFlows is a flow log file attached to the bundle (Hubble, Calico,
	// VPC flow logs, generic CSV/JSON) or collected from hubble-relay.
	CoverageFlows = "flows"
	// CoverageInput is one repo scan input: a manifest file, a Helm chart, a
	// Kustomize overlay or a GitOps source (flightbay scan).
	CoverageInput = "input"

	StateComplete  = "complete"
	StateFailed    = "failed"
	StateExcluded  = "excluded"
	StateTruncated = "truncated"
	// StateRemoved marks data the customer took out in review before sending.
	StateRemoved = "removed"
)

// Coverage says what one collection produced, so absence can be told apart
// from exclusion, failure or truncation.
type Coverage struct {
	ID         string   `json:"id"`
	Cluster    string   `json:"cluster"`
	Type       string   `json:"type"`
	Group      string   `json:"group,omitempty"`
	Version    string   `json:"version,omitempty"`
	Resource   string   `json:"resource,omitempty"`
	Kind       string   `json:"kind,omitempty"`
	Namespaced bool     `json:"namespaced,omitempty"`
	Namespace  string   `json:"namespace,omitempty"`
	Pod        string   `json:"pod,omitempty"`
	Container  string   `json:"container,omitempty"`
	Stream     string   `json:"stream,omitempty"`
	Chunks     []string `json:"chunks,omitempty"`
	Items      int      `json:"items"`
	State      string   `json:"state"`
	Reason     string   `json:"reason,omitempty"`
	Source     string   `json:"source,omitempty"`   // repo scan: the input path
	Renderer   string   `json:"renderer,omitempty"` // repo scan: yaml, helm, kustomize, argocd, flux
	// Trimmed counts what the customer removed from this collection in review
	// while keeping the rest of it.
	Trimmed *Trimmed `json:"trimmed,omitempty"`
}

// Trimmed says how a collection was cut down in review.
type Trimmed struct {
	Objects     int `json:"objects,omitempty"`     // removed one by one
	ByNamespace int `json:"byNamespace,omitempty"` // removed with a whole namespace
	Lines       int `json:"lines,omitempty"`       // log lines
	Files       int `json:"files,omitempty"`       // diagnostic or flow files
}

// Manifest is manifest.json, written last.
type Manifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	BundleID      string            `json:"bundleId"`
	CaptureEnd    time.Time         `json:"captureEnd"`
	Complete      bool              `json:"complete"`
	Errors        int               `json:"errors"`
	Coverage      []string          `json:"coverage"`
	Members       map[string]string `json:"members"`
}

// WriterOptions configure compression and encryption.
type WriterOptions struct {
	Compression string // best, better, default, fastest
	WindowSize  int    // bytes, power of two, capped at MaxWindow
	Recipients  []age.Recipient
	FlushEvery  time.Duration
	// Signer, when set, signs manifest.json and writes manifest.sig after it.
	Signer *sign.PrivateKey
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// Writer streams a bundle in a single pass with no temporary files.
type Writer struct {
	mu         sync.Mutex
	out        *countingWriter
	enc        io.WriteCloser
	zw         *zstd.Encoder
	tw         *tar.Writer
	header     *Header
	hashes     map[string]string
	coverage   []string
	errors     int
	lastFlush  time.Time
	flushEvery time.Duration
	closed     bool
	signer     *sign.PrivateKey
}

func level(name string) zstd.EncoderLevel {
	switch name {
	case "fastest":
		return zstd.SpeedFastest
	case "default":
		return zstd.SpeedDefault
	case "better":
		return zstd.SpeedBetterCompression
	default:
		return zstd.SpeedBestCompression
	}
}

// NewWriter starts a bundle on w.
func NewWriter(w io.Writer, o WriterOptions) (*Writer, error) {
	cw := &countingWriter{w: w}
	var sink io.Writer = cw
	var enc io.WriteCloser
	if len(o.Recipients) > 0 {
		e, err := age.Encrypt(cw, o.Recipients...)
		if err != nil {
			return nil, fmt.Errorf("age: %w", err)
		}
		enc, sink = e, e
	}
	window := o.WindowSize
	if window <= 0 {
		window = DefaultWindow
	}
	if window > MaxWindow {
		window = MaxWindow
	}
	zw, err := zstd.NewWriter(sink, zstd.WithEncoderLevel(level(o.Compression)),
		zstd.WithEncoderConcurrency(runtime.GOMAXPROCS(0)), zstd.WithWindowSize(window))
	if err != nil {
		return nil, err
	}
	fe := o.FlushEvery
	if fe == 0 {
		fe = 250 * time.Millisecond
	}
	return &Writer{out: cw, enc: enc, zw: zw, tw: tar.NewWriter(zw), hashes: map[string]string{}, flushEvery: fe, signer: o.Signer}, nil
}

var safeSegment = regexp.MustCompile(`^[A-Za-z0-9._@:+=,\-]+$`)

// ValidName reports whether a member name is safe to extract.
func ValidName(name string) error {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || path.Clean(name) != name {
		return fmt.Errorf("unsafe member path %q", name)
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == ".." || seg == "." || !safeSegment.MatchString(seg) {
			return fmt.Errorf("unsafe member path %q", name)
		}
	}
	return nil
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func (w *Writer) write(name string, data []byte) error {
	if w.closed {
		return errors.New("bundle writer is closed")
	}
	if err := w.tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), ModTime: time.Now().UTC(), Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
		return err
	}
	if _, err := w.tw.Write(data); err != nil {
		return err
	}
	w.hashes[name] = sha(data)
	if time.Since(w.lastFlush) >= w.flushEvery {
		return w.flushLocked()
	}
	return nil
}

func (w *Writer) flushLocked() error {
	w.lastFlush = time.Now()
	if err := w.tw.Flush(); err != nil {
		return err
	}
	return w.zw.Flush()
}

// NewBundleID returns a random identifier.
func NewBundleID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// WriteHeader writes bundle.json. It must be the first member.
func (w *Writer) WriteHeader(h Header) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.header != nil || len(w.hashes) > 0 {
		return errors.New("bundle.json must be written first and only once")
	}
	h.SchemaVersion = SchemaVersion
	if h.BundleID == "" {
		h.BundleID = NewBundleID()
	}
	b, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	w.header = &h
	if err := w.write(HeaderName, b); err != nil {
		return err
	}
	return w.flushLocked()
}

// Add writes one data member.
func (w *Writer) Add(name string, data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.header == nil {
		return errors.New("bundle.json must be written before any data")
	}
	if err := ValidName(name); err != nil {
		return err
	}
	if name == HeaderName || name == ManifestName || name == SignatureName {
		return fmt.Errorf("%s is reserved", name)
	}
	if _, dup := w.hashes[name]; dup {
		return fmt.Errorf("duplicate member %s", name)
	}
	return w.write(name, data)
}

// AddJSON marshals v and writes it.
func (w *Writer) AddJSON(name string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return w.Add(name, b)
}

// Coverage records a finished collection and flushes, so importers learn promptly.
func (w *Writer) Coverage(c Coverage) error {
	c.ID = SafeID(c.ID)
	if err := w.AddJSON(CoveragePath(c.Cluster, c.ID), c); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.coverage = append(w.coverage, c.ID)
	if c.State == StateFailed {
		w.errors++
	}
	return w.flushLocked()
}

// Flush forces buffered data out to the underlying writer.
func (w *Writer) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.flushLocked()
}

// CompressedBytes is the number of bytes written to the destination so far.
func (w *Writer) CompressedBytes() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.out.n
}

// Close writes manifest.json and finishes every layer of the stream.
func (w *Writer) Close(m Manifest) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	if w.header == nil {
		return errors.New("bundle.json was never written")
	}
	m.SchemaVersion = SchemaVersion
	m.BundleID = w.header.BundleID
	if m.CaptureEnd.IsZero() {
		m.CaptureEnd = time.Now().UTC()
	}
	m.Members = make(map[string]string, len(w.hashes))
	for k, v := range w.hashes {
		m.Members[k] = v
	}
	m.Coverage = append([]string{}, w.coverage...)
	m.Errors += w.errors
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := w.write(ManifestName, b); err != nil {
		return err
	}
	if w.signer != nil {
		if err := w.write(SignatureName, w.signer.Sign(sign.PurposeBundle, b).Marshal()); err != nil {
			return err
		}
	}
	w.closed = true
	if err := w.tw.Close(); err != nil {
		return err
	}
	if err := w.zw.Close(); err != nil {
		return err
	}
	if w.enc != nil {
		return w.enc.Close()
	}
	return nil
}

// ReaderOptions bound the resources a malformed or hostile bundle can consume.
type ReaderOptions struct {
	Identities     []age.Identity
	MaxMemberBytes int64 // default 512 MiB
	MaxTotalBytes  int64 // default unlimited
}

// Member is one decoded tar member.
type Member struct {
	Name   string
	Data   []byte
	SHA256 string
}

// Reader decodes a bundle incrementally.
type Reader struct {
	tr        *tar.Reader
	dec       *zstd.Decoder
	opts      ReaderOptions
	seen      map[string]string
	header    *Header
	manifest  *Manifest
	rawMan    []byte
	sig       []byte
	total     int64
	eof       bool
	err       error
	Encrypted bool
}

// NewReader starts decoding. Encrypted bundles need an identity.
func NewReader(r io.Reader, o ReaderOptions) (*Reader, error) {
	br := bufio.NewReaderSize(r, 64<<10)
	head, _ := br.Peek(len(ageMagic))
	var src io.Reader = br
	encrypted := false
	if bytes.Equal(head, []byte(ageMagic)) {
		encrypted = true
		if len(o.Identities) == 0 {
			return nil, errors.New("bundle is encrypted with age; provide an identity file")
		}
		d, err := age.Decrypt(br, o.Identities...)
		if err != nil {
			return nil, fmt.Errorf("decrypt bundle: %w", err)
		}
		src = d
	}
	dec, err := zstd.NewReader(src, zstd.WithDecoderMaxWindow(MaxWindow), zstd.WithDecoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	if o.MaxMemberBytes <= 0 {
		o.MaxMemberBytes = DefaultMaxMember
	}
	return &Reader{tr: tar.NewReader(dec), dec: dec, opts: o, seen: map[string]string{}, Encrypted: encrypted}, nil
}

func (r *Reader) fail(err error) (*Member, error) {
	r.err = err
	return nil, err
}

// Next returns the next member, io.EOF at the end of the archive.
func (r *Reader) Next() (*Member, error) {
	if r.err != nil {
		return nil, r.err
	}
	if r.eof {
		return nil, io.EOF
	}
	h, err := r.tr.Next()
	if err == io.EOF {
		r.eof = true
		r.dec.Close()
		return nil, io.EOF
	}
	if err != nil {
		return r.fail(fmt.Errorf("read bundle: %w", err))
	}
	if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
		return r.fail(fmt.Errorf("unexpected tar entry type %q for %s", h.Typeflag, h.Name))
	}
	if err := ValidName(h.Name); err != nil {
		return r.fail(err)
	}
	if r.header == nil && h.Name != HeaderName {
		return r.fail(fmt.Errorf("first member must be %s, got %s", HeaderName, h.Name))
	}
	if r.manifest != nil && (h.Name != SignatureName || r.sig != nil) {
		return r.fail(fmt.Errorf("member %s after %s", h.Name, ManifestName))
	}
	if h.Name == SignatureName && r.manifest == nil {
		return r.fail(fmt.Errorf("%s before %s", SignatureName, ManifestName))
	}
	if _, dup := r.seen[h.Name]; dup {
		return r.fail(fmt.Errorf("duplicate member %s", h.Name))
	}
	if h.Size < 0 || h.Size > r.opts.MaxMemberBytes {
		return r.fail(fmt.Errorf("member %s is %d bytes, over the %d byte limit", h.Name, h.Size, r.opts.MaxMemberBytes))
	}
	r.total += h.Size
	if r.opts.MaxTotalBytes > 0 && r.total > r.opts.MaxTotalBytes {
		return r.fail(fmt.Errorf("bundle expands past the %d byte limit", r.opts.MaxTotalBytes))
	}
	data := make([]byte, h.Size)
	if _, err := io.ReadFull(r.tr, data); err != nil {
		return r.fail(fmt.Errorf("read %s: %w", h.Name, err))
	}
	sum := sha(data)
	r.seen[h.Name] = sum
	switch h.Name {
	case HeaderName:
		var hd Header
		if err := json.Unmarshal(data, &hd); err != nil {
			return r.fail(fmt.Errorf("parse %s: %w", HeaderName, err))
		}
		if hd.SchemaVersion != SchemaVersion {
			return r.fail(fmt.Errorf("unsupported bundle schema %d", hd.SchemaVersion))
		}
		r.header = &hd
	case ManifestName:
		var m Manifest
		if err := json.Unmarshal(data, &m); err != nil {
			return r.fail(fmt.Errorf("parse %s: %w", ManifestName, err))
		}
		r.manifest = &m
		r.rawMan = data
	case SignatureName:
		r.sig = data
	}
	return &Member{Name: h.Name, Data: data, SHA256: sum}, nil
}

// Header returns bundle.json once read.
func (r *Reader) Header() *Header { return r.header }

// Manifest returns manifest.json once read.
func (r *Reader) Manifest() *Manifest { return r.manifest }

// ManifestSHA256 is the hex SHA-256 of manifest.json once read: the one
// value that identifies the exact content of a bundle.
func (r *Reader) ManifestSHA256() string {
	if r.rawMan == nil {
		return ""
	}
	return sha(r.rawMan)
}

// Signed reports whether the bundle carries manifest.sig (read so far).
func (r *Reader) Signed() bool { return r.sig != nil }

// Signature checks manifest.sig against manifest.json and the trusted keys.
// Call it after reading to the end. A bundle that also fails Verify is
// modified however its signature reads.
func (r *Reader) Signature(trusted []sign.PublicKey) sign.Result {
	if r.sig == nil {
		return sign.Result{}
	}
	if r.rawMan == nil {
		return sign.Result{Signed: true, Error: "manifest.json is missing"}
	}
	s, err := sign.Parse(r.sig)
	if err != nil {
		return sign.Result{Signed: true, Error: err.Error()}
	}
	return sign.Verify(s, sign.PurposeBundle, r.rawMan, trusted)
}

// Verify checks the finished stream against the manifest.
func (r *Reader) Verify() error {
	if r.err != nil {
		return fmt.Errorf("bundle is incomplete: %w", r.err)
	}
	if !r.eof {
		return errors.New("bundle has not been read to the end")
	}
	if r.manifest == nil {
		return fmt.Errorf("bundle is incomplete: %s missing (truncated upload or interrupted collection)", ManifestName)
	}
	var problems []string
	for name, want := range r.manifest.Members {
		got, ok := r.seen[name]
		switch {
		case !ok:
			problems = append(problems, name+": missing")
		case got != want:
			problems = append(problems, name+": checksum mismatch")
		}
	}
	for name := range r.seen {
		if name == ManifestName || name == SignatureName {
			continue
		}
		if _, ok := r.manifest.Members[name]; !ok {
			problems = append(problems, name+": not listed in manifest")
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("bundle failed verification: %s", strings.Join(problems, "; "))
	}
	return nil
}

// Path kinds.
const (
	PathHeader     = "header"
	PathManifest   = "manifest"
	PathResource   = "resource"
	PathLog        = "log"
	PathEvents     = "events"
	PathCoverage   = "coverage"
	PathDiscovery  = "discovery"
	PathRedaction  = "redaction"
	PathCollection = "collection-log"
	PathSolo       = "solo"
	PathFlows      = "flows"
	PathSignature  = "signature"
	PathLedger     = "ledger"
)

// PathInfo classifies a member name.
type PathInfo struct {
	Kind, Cluster, Group, Version, Resource, Namespace, Pod, Container, Stream, ID, File, Sub string
	Part                                                                                      int
}

func groupDir(g string) string {
	if g == "" {
		return "core"
	}
	return g
}

// SafeID converts an arbitrary identifier into a single safe path segment.
func SafeID(id string) string {
	var b strings.Builder
	for _, r := range id {
		if r < 128 && (r == '.' || r == '-' || r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func ResourcePath(cluster, group, version, resource string, part int) string {
	return fmt.Sprintf("clusters/%s/resources/%s/%s/%s/part-%06d.json", cluster, groupDir(group), version, resource, part)
}

func LogPath(cluster, ns, pod, container, stream string, part int) string {
	return fmt.Sprintf("clusters/%s/logs/%s/%s/%s/%s/part-%06d.log", cluster, ns, pod, container, stream, part)
}

func EventsPath(cluster string, part int) string {
	return fmt.Sprintf("clusters/%s/events/part-%06d.json", cluster, part)
}

func CoveragePath(cluster, id string) string {
	return fmt.Sprintf("clusters/%s/coverage/%s.json", cluster, id)
}

// SoloPath is clusters/<c>/solo/<kind>/<ns>/<pod>/<file>, or
// clusters/<c>/solo/<file> when kind is empty. kind is proxies or control-plane.
func SoloPath(cluster, kind, ns, pod, file string) string {
	if kind == "" {
		return "clusters/" + SafeID(cluster) + "/solo/" + file
	}
	return "clusters/" + SafeID(cluster) + "/solo/" + kind + "/" + SafeID(ns) + "/" + SafeID(pod) + "/" + file
}

// FlowsPath is clusters/<c>/flows/<format>/<file>: a flow log file attached
// to the bundle, in a format internal/flows reads.
func FlowsPath(cluster, format, file string) string {
	return "clusters/" + SafeID(cluster) + "/flows/" + SafeID(format) + "/" + SafeID(file)
}

func DiscoveryPath(cluster, file string) string {
	return fmt.Sprintf("clusters/%s/discovery/%s", cluster, file)
}

func partNum(s, ext string) (int, bool) {
	if !strings.HasPrefix(s, "part-") || !strings.HasSuffix(s, ext) {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(s, "part-"), ext))
	return n, err == nil
}

// ParsePath classifies a member name produced by the helpers above.
func ParsePath(name string) (PathInfo, bool) {
	switch name {
	case HeaderName:
		return PathInfo{Kind: PathHeader}, true
	case ManifestName:
		return PathInfo{Kind: PathManifest}, true
	case RedactionName:
		return PathInfo{Kind: PathRedaction}, true
	case CollectionLog:
		return PathInfo{Kind: PathCollection}, true
	case SignatureName:
		return PathInfo{Kind: PathSignature}, true
	case LedgerName:
		return PathInfo{Kind: PathLedger}, true
	}
	s := strings.Split(name, "/")
	if len(s) < 4 || s[0] != "clusters" {
		return PathInfo{}, false
	}
	info := PathInfo{Cluster: s[1]}
	switch s[2] {
	case "resources":
		if len(s) != 7 {
			return PathInfo{}, false
		}
		n, ok := partNum(s[6], ".json")
		if !ok {
			return PathInfo{}, false
		}
		info.Kind, info.Group, info.Version, info.Resource, info.Part = PathResource, s[3], s[4], s[5], n
		if info.Group == "core" {
			info.Group = ""
		}
	case "logs":
		if len(s) != 8 {
			return PathInfo{}, false
		}
		n, ok := partNum(s[7], ".log")
		if !ok {
			return PathInfo{}, false
		}
		info.Kind, info.Namespace, info.Pod, info.Container, info.Stream, info.Part = PathLog, s[3], s[4], s[5], s[6], n
	case "events":
		n, ok := partNum(s[3], ".json")
		if !ok || len(s) != 4 {
			return PathInfo{}, false
		}
		info.Kind, info.Part = PathEvents, n
	case "coverage":
		if len(s) != 4 || !strings.HasSuffix(s[3], ".json") {
			return PathInfo{}, false
		}
		info.Kind, info.ID = PathCoverage, strings.TrimSuffix(s[3], ".json")
	case "discovery":
		info.Kind, info.File = PathDiscovery, strings.Join(s[3:], "/")
	case "flows":
		if len(s) != 5 || s[3] == "" || s[4] == "" {
			return PathInfo{}, false
		}
		info.Kind, info.Sub, info.File = PathFlows, s[3], s[4]
	case "solo":
		// solo/summary.json or solo/<kind>/<ns>/<pod>/<file>
		info.Kind = PathSolo
		switch len(s) {
		case 4:
			info.File = s[3]
		case 7:
			info.Sub, info.Namespace, info.Pod, info.File = s[3], s[4], s[5], s[6]
		default:
			return PathInfo{}, false
		}
	default:
		return PathInfo{}, false
	}
	return info, true
}

// APIResource describes a collected resource type. It is written to
// clusters/<name>/discovery/apiresources.json so the desk can serve discovery.
type APIResource struct {
	Group        string   `json:"group"`
	Version      string   `json:"version"`
	Resource     string   `json:"resource"`
	Kind         string   `json:"kind"`
	Namespaced   bool     `json:"namespaced"`
	SingularName string   `json:"singularName,omitempty"`
	ShortNames   []string `json:"shortNames,omitempty"`
	Categories   []string `json:"categories,omitempty"`
}

// GroupVersion returns "v1" or "apps/v1".
func (a APIResource) GroupVersion() string {
	if a.Group == "" {
		return a.Version
	}
	return a.Group + "/" + a.Version
}

// Name returns "pods" or "deployments.apps".
func (a APIResource) Name() string {
	if a.Group == "" {
		return a.Resource
	}
	return a.Resource + "." + a.Group
}
