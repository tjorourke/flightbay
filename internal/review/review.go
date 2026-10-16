// Package review turns a captured bundle into the bundle a customer agrees to
// send: whole namespaces, resource types, objects, log streams, log lines,
// diagnostic and flow files taken out, extra patterns and fields redacted,
// and the result signed again with a ledger of what was removed.
//
// It runs in one pass over the original, which is verified end to end; a
// failed verification fails the review, so a damaged capture is never
// re-signed. The same pass with a discarding writer is the preview.
package review

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"filippo.io/age"
	"sigs.k8s.io/yaml"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/collect"
	"github.com/tjorourke/flightbay/internal/logs"
	"github.com/tjorourke/flightbay/internal/redact"
	"github.com/tjorourke/flightbay/internal/sign"
)

// Plan is what the customer chose to remove and redact. Every string field
// in a removal accepts * and ? globs; an empty field matches anything.
type Plan struct {
	Reviewer string `json:"reviewer,omitempty"`
	// DiscloseNames lists removed namespaces, objects and log streams by name
	// in the ledger. Off by default: the ledger then carries counts only, and
	// collection records inside removed namespaces are dropped as well.
	DiscloseNames bool `json:"discloseNames,omitempty"`
	// ScrubNames replaces the names of removed namespaces wherever they still
	// appear in what is kept (log lines, object fields, diagnostics).
	ScrubNames bool     `json:"scrubNames,omitempty"`
	Remove     Removals `json:"remove,omitempty"`
	// DropLogLinesMatching removes whole log lines matching any of these
	// regular expressions.
	DropLogLinesMatching []string `json:"dropLogLinesMatching,omitempty"`
	// Redact replaces every match of these regular expressions with [REDACTED].
	Redact []string `json:"redact,omitempty"`
	// DropFields removes object paths, e.g. metadata.annotations["corp.example/*"].
	DropFields []string `json:"dropFields,omitempty"`
}

// Removals lists what to take out.
type Removals struct {
	Namespaces  []NamespaceRef `json:"namespaces,omitempty"`
	Types       []TypeRef      `json:"types,omitempty"`
	Objects     []ObjectRef    `json:"objects,omitempty"`
	LogStreams  []LogRef       `json:"logStreams,omitempty"`
	LogLines    []LineRef      `json:"logLines,omitempty"`
	Events      bool           `json:"events,omitempty"` // every event
	Diagnostics []FileRef      `json:"diagnostics,omitempty"`
	Flows       []FileRef      `json:"flows,omitempty"`
}

// NamespaceRef is a namespace in a cluster.
type NamespaceRef struct {
	Cluster   string `json:"cluster,omitempty"`
	Namespace string `json:"namespace"`
}

// TypeRef is a resource type, e.g. {resource: configmaps} or {group: gateway.networking.k8s.io, resource: httproutes}.
type TypeRef struct {
	Cluster  string `json:"cluster,omitempty"`
	Group    string `json:"group,omitempty"`
	Resource string `json:"resource"`
}

// ObjectRef is one object.
type ObjectRef struct {
	Cluster   string `json:"cluster,omitempty"`
	Group     string `json:"group,omitempty"`
	Resource  string `json:"resource"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

// LogRef is a container log stream.
type LogRef struct {
	Cluster   string `json:"cluster,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Pod       string `json:"pod,omitempty"`
	Container string `json:"container,omitempty"`
	Stream    string `json:"stream,omitempty"`
}

// LineRef is one log line: its chunk member and its position among the
// chunk's non-empty lines. SHA256, when set, must match the line, so a plan
// made against one bundle cannot remove the wrong line from another.
type LineRef struct {
	Chunk  string `json:"chunk"`
	Index  int    `json:"index"`
	SHA256 string `json:"sha256,omitempty"`
}

// FileRef is a diagnostic or flow file.
type FileRef struct {
	Cluster   string `json:"cluster,omitempty"`
	Namespace string `json:"namespace,omitempty"` // diagnostics: the pod's namespace
	Pod       string `json:"pod,omitempty"`
	File      string `json:"file,omitempty"`
}

// LoadPlan reads a plan from YAML or JSON.
func LoadPlan(file string) (*Plan, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var p Plan
	if err := yaml.UnmarshalStrict(b, &p); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return &p, p.Validate()
}

// Validate compiles every pattern once so mistakes show before any work.
func (p *Plan) Validate() error {
	_, err := p.compile()
	return err
}

// Empty reports whether the plan changes nothing.
func (p *Plan) Empty() bool {
	r := p.Remove
	return len(r.Namespaces)+len(r.Types)+len(r.Objects)+len(r.LogStreams)+len(r.LogLines)+len(r.Diagnostics)+len(r.Flows) == 0 &&
		!r.Events && len(p.DropLogLinesMatching)+len(p.Redact)+len(p.DropFields) == 0
}

// Ledger is review-ledger.json: what was removed, counted, and by whom.
type Ledger struct {
	SchemaVersion  int       `json:"schemaVersion"`
	Tool           string    `json:"tool,omitempty"`
	Reviewer       string    `json:"reviewer,omitempty"`
	ReviewedAt     time.Time `json:"reviewedAt"`
	Original       Original  `json:"original"`
	NamesDisclosed bool      `json:"namesDisclosed"`
	Removed        Counts    `json:"removed"`
	Kept           Counts    `json:"kept"`
	Redacted       Redacted  `json:"redacted"`
	// Types are always listed: a type name says nothing about the customer,
	// and the desk uses them to mark checks as not applicable.
	Types []string `json:"types,omitempty"`
	// Namespaces, Objects, LogStreams and Files are listed only with DiscloseNames.
	Namespaces []string `json:"namespaces,omitempty"`
	Objects    []string `json:"objects,omitempty"`
	LogStreams []string `json:"logStreams,omitempty"`
	Files      []string `json:"files,omitempty"`
	// Previous is the ledger of an earlier review of the same capture.
	Previous *Ledger `json:"previous,omitempty"`
}

// Original identifies the bundle the review started from.
type Original struct {
	BundleID       string       `json:"bundleId"`
	Case           string       `json:"case,omitempty"`
	ManifestSHA256 string       `json:"manifestSha256"`
	Signature      *sign.Result `json:"signature,omitempty"`
}

// Counts of what was removed or kept.
type Counts struct {
	Namespaces         int `json:"namespaces,omitempty"`
	Types              int `json:"types,omitempty"`
	Objects            int `json:"objects"`
	ObjectsByNamespace int `json:"objectsByNamespace,omitempty"`
	ObjectsByType      int `json:"objectsByType,omitempty"`
	Events             int `json:"events"`
	LogStreams         int `json:"logStreams,omitempty"`
	LogLines           int `json:"logLines"`
	DiagnosticFiles    int `json:"diagnosticFiles"`
	DiagnosticEntries  int `json:"diagnosticEntries,omitempty"` // entries naming a removed namespace, cut from kept dumps
	FlowFiles          int `json:"flowFiles"`
}

// Redacted counts redactions added in review. The patterns themselves are
// never recorded: they can describe what they hide.
type Redacted struct {
	Patterns      int `json:"patterns,omitempty"`
	Replacements  int `json:"replacements,omitempty"`
	FieldRules    int `json:"fieldRules,omitempty"`
	FieldsRemoved int `json:"fieldsRemoved,omitempty"`
	LinePatterns  int `json:"linePatterns,omitempty"`
	NamesScrubbed int `json:"namesScrubbed,omitempty"`
}

// Summary is one line for a terminal.
func (l *Ledger) Summary() string {
	var parts []string
	add := func(n int, one, many string) {
		if n == 1 {
			parts = append(parts, "1 "+one)
		} else if n > 1 {
			parts = append(parts, fmt.Sprintf("%d %s", n, many))
		}
	}
	r := l.Removed
	add(r.Namespaces, "namespace", "namespaces")
	add(r.Types, "resource type", "resource types")
	add(r.Objects, "object", "objects")
	add(r.Events, "event", "events")
	add(r.LogStreams, "log stream", "log streams")
	add(r.LogLines, "log line", "log lines")
	add(r.DiagnosticFiles, "diagnostic file", "diagnostic files")
	add(r.FlowFiles, "flow file", "flow files")
	add(r.DiagnosticEntries, "diagnostic entry", "diagnostic entries")
	d := l.Redacted
	add(d.Replacements, "value redacted", "values redacted")
	add(d.FieldsRemoved, "field removed", "fields removed")
	add(d.NamesScrubbed, "name scrubbed", "names scrubbed")
	if len(parts) == 0 {
		return "nothing"
	}
	return strings.Join(parts, ", ")
}

// Options control the output of Apply.
type Options struct {
	Identities  []age.Identity   // to read an encrypted original
	Trusted     []sign.PublicKey // to check the original's signature
	Recipients  []age.Recipient  // encrypt the reviewed bundle
	Signer      *sign.PrivateKey // sign the reviewed bundle
	Compression string
	Tool        string    // e.g. "flightbay 0.1.0"
	Now         time.Time // for tests
}

// ErrUnverified wraps a verification failure of the original bundle.
var ErrUnverified = errors.New("the original bundle did not verify")

type compiled struct {
	p         *Plan
	lineDrop  []*regexp.Regexp
	redactRe  []*regexp.Regexp
	fields    redact.FieldRules
	nameRe    *regexp.Regexp
	lineRefs  map[string]map[int]string // chunk -> index -> sha ("" = any)
	nsRemoved func(cluster, ns string) bool
}

func glob(pat, s string) bool {
	if pat == "" || pat == s {
		return true
	}
	ok, _ := path.Match(pat, s)
	return ok
}

func (p *Plan) compile() (*compiled, error) {
	c := &compiled{p: p, lineRefs: map[string]map[int]string{}}
	re := func(list []string, what string) ([]*regexp.Regexp, error) {
		var out []*regexp.Regexp
		for i, s := range list {
			if strings.TrimSpace(s) == "" {
				continue
			}
			r, err := regexp.Compile(s)
			if err != nil {
				return nil, fmt.Errorf("%s %d %q: %w", what, i+1, s, err)
			}
			if r.MatchString("") {
				return nil, fmt.Errorf("%s %d %q matches empty text, so it would match everything", what, i+1, s)
			}
			out = append(out, r)
		}
		return out, nil
	}
	var err error
	if c.lineDrop, err = re(p.DropLogLinesMatching, "log line pattern"); err != nil {
		return nil, err
	}
	if c.redactRe, err = re(p.Redact, "redaction pattern"); err != nil {
		return nil, err
	}
	if c.fields, err = redact.ParseFieldRules(p.DropFields); err != nil {
		return nil, err
	}
	for _, n := range p.Remove.Namespaces {
		if n.Namespace == "" {
			return nil, errors.New("a namespace removal needs a namespace")
		}
		if _, err := path.Match(n.Namespace, ""); err != nil {
			return nil, fmt.Errorf("namespace %q: %w", n.Namespace, err)
		}
	}
	for _, t := range p.Remove.Types {
		if t.Resource == "" {
			return nil, errors.New("a type removal needs a resource, e.g. configmaps")
		}
	}
	for _, o := range p.Remove.Objects {
		if o.Resource == "" || o.Name == "" {
			return nil, errors.New("an object removal needs a resource and a name")
		}
	}
	for _, l := range p.Remove.LogLines {
		if c.lineRefs[l.Chunk] == nil {
			c.lineRefs[l.Chunk] = map[int]string{}
		}
		c.lineRefs[l.Chunk][l.Index] = l.SHA256
	}
	c.nsRemoved = func(cluster, ns string) bool {
		if ns == "" {
			return false
		}
		for _, n := range p.Remove.Namespaces {
			if glob(n.Cluster, cluster) && glob(n.Namespace, ns) {
				return true
			}
		}
		return false
	}
	return c, nil
}

func (c *compiled) typeRemoved(cluster, group, resource string) bool {
	for _, t := range c.p.Remove.Types {
		if glob(t.Cluster, cluster) && glob(t.Group, group) && glob(t.Resource, resource) {
			return true
		}
	}
	return false
}

func (c *compiled) objectRemoved(cluster, group, resource, ns, name string) bool {
	for _, o := range c.p.Remove.Objects {
		if glob(o.Cluster, cluster) && o.Group == group && o.Resource == resource && o.Namespace == ns && glob(o.Name, name) {
			return true
		}
	}
	return false
}

func (c *compiled) streamRemoved(cluster, ns, pod, container, stream string) bool {
	if c.nsRemoved(cluster, ns) {
		return true
	}
	for _, l := range c.p.Remove.LogStreams {
		if glob(l.Cluster, cluster) && glob(l.Namespace, ns) && glob(l.Pod, pod) && glob(l.Container, container) && glob(l.Stream, stream) {
			return true
		}
	}
	return false
}

func fileRemoved(refs []FileRef, cluster, ns, pod, file string) bool {
	for _, f := range refs {
		if glob(f.Cluster, cluster) && glob(f.Namespace, ns) && glob(f.Pod, pod) && glob(f.File, file) {
			return true
		}
	}
	return false
}

// run holds the state of one Apply.
type run struct {
	c       *compiled
	led     *Ledger
	chunks  map[string]*bundle.Trimmed // per member: what was removed from it
	dropped map[string]bool            // members dropped whole
	names   map[string]bool            // removed namespace names (cluster/ns), for the ledger
	streams map[string]bool
	objects map[string]bool
	files   map[string]bool
	types   map[string]bool
	nsNames map[string]bool // plain names of removed namespaces, for scrubbing
}

// scrub applies redaction patterns and name scrubbing to text.
func (r *run) scrub(s string) string {
	for _, re := range r.c.redactRe {
		if re.MatchString(s) {
			s = re.ReplaceAllStringFunc(s, func(string) string { r.led.Redacted.Replacements++; return redact.Placeholder })
		}
	}
	if r.c.nameRe != nil && r.c.nameRe.MatchString(s) {
		s = r.c.nameRe.ReplaceAllStringFunc(s, func(string) string { r.led.Redacted.NamesScrubbed++; return "[REMOVED NAMESPACE]" })
	}
	return s
}

func (r *run) textChanges() bool { return len(r.c.redactRe) > 0 || r.c.nameRe != nil }

// walk scrubs every string in a decoded JSON value in place.
func (r *run) walk(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			nk := k
			if r.c.nameRe != nil {
				nk = r.scrub(k)
			}
			if nk != k {
				delete(t, k)
			}
			t[nk] = r.walk(val)
		}
		return t
	case []any:
		for i, val := range t {
			t[i] = r.walk(val)
		}
		return t
	case string:
		return r.scrub(t)
	}
	return v
}

func (r *run) object(obj map[string]any) {
	if len(r.c.fields) > 0 {
		for _, n := range r.c.fields.Apply(obj) {
			r.led.Redacted.FieldsRemoved += n
		}
	}
	if r.textChanges() {
		r.walk(obj)
	}
}

func str(v any) string { s, _ := v.(string); return s }

func meta(obj map[string]any) (ns, name string) {
	md, _ := obj["metadata"].(map[string]any)
	return str(md["namespace"]), str(md["name"])
}

func typeName(group, resource string) string {
	if group == "" {
		return resource
	}
	return resource + "." + group
}

func (r *run) trim(member string) *bundle.Trimmed {
	t := r.chunks[member]
	if t == nil {
		t = &bundle.Trimmed{}
		r.chunks[member] = t
	}
	return t
}

// resources filters one resource chunk. It returns nil to drop the member.
func (r *run) resources(name string, info bundle.PathInfo, data []byte) ([]byte, error) {
	var list map[string]any
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	items, _ := list["items"].([]any)
	if r.c.typeRemoved(info.Cluster, info.Group, info.Resource) {
		r.types[typeName(info.Group, info.Resource)] = true
		r.led.Removed.ObjectsByType += len(items)
		r.led.Removed.Objects += len(items)
		r.dropped[name] = true
		return nil, nil
	}
	isNS := info.Group == "" && info.Resource == "namespaces"
	kept := items[:0]
	for _, it := range items {
		obj, _ := it.(map[string]any)
		ns, n := meta(obj)
		if isNS {
			ns = n
		}
		switch {
		case r.c.nsRemoved(info.Cluster, ns):
			r.names[info.Cluster+"/"+ns] = true
			r.trim(name).ByNamespace++
			r.led.Removed.ObjectsByNamespace++
			r.led.Removed.Objects++
		case r.c.objectRemoved(info.Cluster, info.Group, info.Resource, ns, n):
			r.objects[strings.Trim(strings.Join([]string{info.Cluster, typeName(info.Group, info.Resource), ns, n}, "/"), "/")] = true
			r.trim(name).Objects++
			r.led.Removed.Objects++
		default:
			r.object(obj)
			kept = append(kept, obj)
		}
	}
	r.led.Kept.Objects += len(kept)
	if len(kept) == 0 && len(items) > 0 {
		r.dropped[name] = true
		return nil, nil
	}
	list["items"] = kept
	return json.Marshal(list)
}

func (r *run) events(name string, info bundle.PathInfo, data []byte) ([]byte, error) {
	var list map[string]any
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	items, _ := list["items"].([]any)
	kept := items[:0]
	for _, it := range items {
		obj, _ := it.(map[string]any)
		ns, _ := meta(obj)
		inv, _ := obj["involvedObject"].(map[string]any)
		if ns == "" {
			ns = str(inv["namespace"])
		}
		gone := r.c.p.Remove.Events || r.c.nsRemoved(info.Cluster, ns)
		if !gone && inv != nil {
			group, _, _ := strings.Cut(str(inv["apiVersion"]), "/")
			if !strings.Contains(str(inv["apiVersion"]), "/") {
				group = ""
			}
			for _, o := range r.c.p.Remove.Objects {
				if glob(o.Cluster, info.Cluster) && o.Group == group && o.Namespace == str(inv["namespace"]) && glob(o.Name, str(inv["name"])) &&
					strings.EqualFold(strings.TrimSuffix(o.Resource, "s"), str(inv["kind"])) {
					gone = true
				}
			}
		}
		if gone {
			r.trim(name).Objects++
			r.led.Removed.Events++
			continue
		}
		r.object(obj)
		kept = append(kept, obj)
	}
	r.led.Kept.Events += len(kept)
	if len(kept) == 0 && len(items) > 0 {
		r.dropped[name] = true
		return nil, nil
	}
	list["items"] = kept
	return json.Marshal(list)
}

func (r *run) logs(name string, info bundle.PathInfo, data []byte) []byte {
	if r.c.streamRemoved(info.Cluster, info.Namespace, info.Pod, info.Container, info.Stream) {
		key := strings.Join([]string{info.Cluster, info.Namespace, info.Pod, info.Container, info.Stream}, "/")
		if !r.c.nsRemoved(info.Cluster, info.Namespace) {
			r.streams[key] = true
		} else {
			r.names[info.Cluster+"/"+info.Namespace] = true
		}
		r.led.Removed.LogLines += countLines(data)
		r.dropped[name] = true
		return nil
	}
	refs := r.c.lineRefs[name]
	if refs == nil && len(r.c.lineDrop) == 0 && !r.textChanges() {
		r.led.Kept.LogLines += countLines(data)
		return data
	}
	var b strings.Builder
	idx := 0
	kept := 0
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		i := idx
		idx++
		if want, ok := refs[i]; ok && (want == "" || want == lineSHA(line)) {
			r.trim(name).Lines++
			r.led.Removed.LogLines++
			continue
		}
		drop := false
		for _, re := range r.c.lineDrop {
			if re.MatchString(line) {
				drop = true
				break
			}
		}
		if drop {
			r.trim(name).Lines++
			r.led.Removed.LogLines++
			continue
		}
		b.WriteString(r.scrub(line))
		b.WriteByte('\n')
		kept++
	}
	r.led.Kept.LogLines += kept
	if kept == 0 {
		r.dropped[name] = true
		return nil
	}
	return []byte(b.String())
}

func countLines(data []byte) int {
	n := 0
	for _, l := range strings.Split(string(data), "\n") {
		if l != "" {
			n++
		}
	}
	return n
}

// text scrubs a text or JSON member that is kept whole (diagnostics, flows,
// the collection log). JSON is scrubbed value by value so it stays valid.
func (r *run) text(data []byte) []byte {
	if !r.textChanges() {
		return data
	}
	var v any
	if t := strings.TrimSpace(string(data)); (strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[")) && json.Unmarshal(data, &v) == nil {
		b, err := json.Marshal(r.walk(v))
		if err == nil {
			return b
		}
	}
	lines := strings.Split(string(data), "\n")
	for i, l := range lines {
		lines[i] = r.scrub(l)
	}
	return []byte(strings.Join(lines, "\n"))
}

// diagnostic removes entries naming a removed namespace from a proxy or
// control-plane dump that is otherwise kept, as collection does for a
// namespace-limited capture.
func (r *run) diagnostic(info bundle.PathInfo, data []byte) []byte {
	if len(r.c.p.Remove.Namespaces) == 0 {
		return data
	}
	allowed := func(ns string) bool { return !r.c.nsRemoved(info.Cluster, ns) }
	var doc any
	if json.Unmarshal(data, &doc) == nil {
		out, n := collect.FilterNamespacesJSON(doc, allowed, info.File == "endpointz.json")
		if n == 0 {
			return data
		}
		r.led.Removed.DiagnosticEntries += n
		b, err := json.MarshalIndent(out, "", " ")
		if err != nil {
			return data
		}
		return b
	}
	out, n := collect.FilterNamespacesLines(string(data), allowed)
	r.led.Removed.DiagnosticEntries += n
	return []byte(out)
}

// redactionReport drops locations inside removed namespaces, which name them.
func (r *run) redactionReport(data []byte) []byte {
	var rep redact.Report
	if json.Unmarshal(data, &rep) != nil || len(r.c.p.Remove.Namespaces) == 0 && r.c.nameRe == nil {
		return r.text(data)
	}
	for loc, rules := range rep.Locations {
		info, ok := bundle.ParsePath(loc + "/part-000001.log")
		gone := false
		if ok && r.c.nsRemoved(info.Cluster, info.Namespace) {
			gone = true
		}
		segs := strings.Split(loc, "/")
		if len(segs) > 1 {
			for _, s := range segs[2:] {
				if r.c.nsRemoved(segs[1], s) {
					gone = true
				}
			}
		}
		if gone || r.removedMember(loc) {
			for _, n := range rules {
				rep.Total -= n
			}
			delete(rep.Locations, loc)
		}
	}
	b, _ := json.MarshalIndent(rep, "", "  ")
	return r.text(b)
}

func (r *run) removedMember(loc string) bool {
	for m := range r.dropped {
		if strings.HasPrefix(m, loc+"/") || m == loc {
			return true
		}
	}
	return false
}

// collectionLog keeps lines that do not name a removed namespace (when names
// are not disclosed) and scrubs the rest.
func (r *run) collectionLog(data []byte) []byte {
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		var e map[string]any
		if json.Unmarshal([]byte(line), &e) == nil {
			if cl := str(e["cluster"]); !r.c.p.DiscloseNames {
				ns := str(e["namespace"])
				if ns == "" {
					// targets look like pods/NS/NAME or logs NS/POD/CONTAINER
					for _, part := range strings.FieldsFunc(str(e["target"]), func(c rune) bool { return c == '/' || c == ' ' }) {
						if r.c.nsRemoved(cl, part) {
							ns = part
						}
					}
				}
				if r.c.nsRemoved(cl, ns) {
					continue
				}
			}
			if r.textChanges() {
				r.walk(e)
				nb, _ := json.Marshal(e)
				line = string(nb)
			}
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// coverage rewrites a collection record for what was removed. ok=false drops it.
func (r *run) coverage(data []byte) (bundle.Coverage, bool, error) {
	var c bundle.Coverage
	if err := json.Unmarshal(data, &c); err != nil {
		return c, false, err
	}
	hideNS := !r.c.p.DiscloseNames && r.c.nsRemoved(c.Cluster, c.Namespace)
	var chunks []string
	var t bundle.Trimmed
	for _, ch := range c.Chunks {
		if tr := r.chunks[ch]; tr != nil {
			t.Objects += tr.Objects
			t.ByNamespace += tr.ByNamespace
			t.Lines += tr.Lines
		}
		if !r.dropped[ch] {
			chunks = append(chunks, ch)
		}
	}
	removed := func(reason string) (bundle.Coverage, bool, error) {
		c.State, c.Reason, c.Items, c.Chunks = bundle.StateRemoved, reason, 0, nil
		return c, true, nil
	}
	switch c.Type {
	case bundle.CoverageResource:
		if r.c.typeRemoved(c.Cluster, c.Group, c.Resource) {
			r.types[typeName(c.Group, c.Resource)] = true
			return removed("removed by the customer before sending")
		}
	case bundle.CoverageLogs:
		if r.c.streamRemoved(c.Cluster, c.Namespace, c.Pod, c.Container, c.Stream) {
			if !r.c.nsRemoved(c.Cluster, c.Namespace) {
				r.streams[strings.Join([]string{c.Cluster, c.Namespace, c.Pod, c.Container, c.Stream}, "/")] = true
			}
			if hideNS || !r.c.p.DiscloseNames {
				return c, false, nil // the record itself names the pod
			}
			return removed("removed by the customer before sending")
		}
	case bundle.CoverageDiagnostic:
		if r.c.nsRemoved(c.Cluster, c.Namespace) || fileRemoved(r.c.p.Remove.Diagnostics, c.Cluster, c.Namespace, c.Pod, "") {
			if hideNS {
				return c, false, nil
			}
			return removed("removed by the customer before sending")
		}
	case bundle.CoverageFlows:
		if r.c.p.Remove.Flows != nil && len(c.Chunks) > 0 && len(chunks) == 0 {
			return removed("removed by the customer before sending")
		}
	}
	if hideNS {
		return c, false, nil
	}
	if t != (bundle.Trimmed{}) || len(chunks) != len(c.Chunks) {
		if len(chunks) < len(c.Chunks) && c.Type != bundle.CoverageResource && c.Type != bundle.CoverageEvents && c.Type != bundle.CoverageLogs {
			t.Files += len(c.Chunks) - len(chunks)
		}
		c.Chunks = chunks
		c.Trimmed = &t
		switch c.Type {
		case bundle.CoverageResource, bundle.CoverageEvents:
			c.Items -= t.Objects + t.ByNamespace
		case bundle.CoverageLogs:
			c.Items -= t.Lines
		}
		if c.Items < 0 {
			c.Items = 0
		}
	}
	return c, true, nil
}

// Apply writes the reviewed bundle to dst (io.Discard for a preview) and
// returns its ledger. It fails if the original does not verify.
func Apply(ctx context.Context, src io.Reader, dst io.Writer, p *Plan, o Options) (*Ledger, error) {
	c, err := p.compile()
	if err != nil {
		return nil, err
	}
	now := o.Now
	if now.IsZero() {
		now = time.Now().UTC().Truncate(time.Second)
	}
	led := &Ledger{SchemaVersion: 1, Tool: o.Tool, Reviewer: p.Reviewer, ReviewedAt: now, NamesDisclosed: p.DiscloseNames,
		Redacted: Redacted{Patterns: len(c.redactRe), FieldRules: len(c.fields), LinePatterns: len(c.lineDrop)}}
	ru := &run{c: c, led: led, chunks: map[string]*bundle.Trimmed{}, dropped: map[string]bool{}, names: map[string]bool{},
		streams: map[string]bool{}, objects: map[string]bool{}, files: map[string]bool{}, types: map[string]bool{}, nsNames: map[string]bool{}}
	if p.ScrubNames {
		var alts []string
		for _, n := range p.Remove.Namespaces {
			if !strings.ContainsAny(n.Namespace, "*?[") {
				alts = append(alts, regexp.QuoteMeta(n.Namespace))
			}
		}
		if len(alts) > 0 {
			sort.Slice(alts, func(i, j int) bool { return len(alts[i]) > len(alts[j]) })
			c.nameRe = regexp.MustCompile(`\b(?:` + strings.Join(alts, "|") + `)\b`)
		}
	}
	rd, err := bundle.NewReader(src, bundle.ReaderOptions{Identities: o.Identities})
	if err != nil {
		return nil, err
	}
	comp := o.Compression
	if comp == "" {
		comp = "best"
	}
	w, err := bundle.NewWriter(dst, bundle.WriterOptions{Compression: comp, Recipients: o.Recipients, Signer: o.Signer})
	if err != nil {
		return nil, err
	}
	var man *bundle.Manifest
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		m, err := rd.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrUnverified, err)
		}
		info, known := bundle.ParsePath(m.Name)
		var out []byte
		switch {
		case !known:
			out = ru.text(m.Data)
		case info.Kind == bundle.PathHeader:
			h := *rd.Header()
			led.Original.BundleID, led.Original.Case = h.BundleID, h.Case
			h.BundleID = ""
			if !p.DiscloseNames && len(p.Remove.Namespaces) > 0 {
				h.Policy = hideNamespaces(h.Policy, c)
			}
			h.Review = &bundle.ReviewInfo{OriginalBundleID: led.Original.BundleID, Reviewer: p.Reviewer, ReviewedAt: now}
			if err := w.WriteHeader(h); err != nil {
				return nil, err
			}
			continue
		case info.Kind == bundle.PathManifest:
			man = rd.Manifest()
			continue
		case info.Kind == bundle.PathSignature:
			continue
		case info.Kind == bundle.PathLedger:
			var prev Ledger
			if json.Unmarshal(m.Data, &prev) == nil {
				led.Previous = &prev
			}
			continue
		case info.Kind == bundle.PathResource:
			if out, err = ru.resources(m.Name, info, m.Data); err != nil {
				return nil, fmt.Errorf("%s: %w", m.Name, err)
			}
		case info.Kind == bundle.PathEvents:
			if out, err = ru.events(m.Name, info, m.Data); err != nil {
				return nil, fmt.Errorf("%s: %w", m.Name, err)
			}
		case info.Kind == bundle.PathLog:
			out = ru.logs(m.Name, info, m.Data)
		case info.Kind == bundle.PathCoverage:
			cov, keep, err := ru.coverage(m.Data)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", m.Name, err)
			}
			if keep {
				if err := w.Coverage(cov); err != nil {
					return nil, err
				}
			}
			continue
		case info.Kind == bundle.PathCollection:
			out = ru.collectionLog(m.Data)
		case info.Kind == bundle.PathRedaction:
			out = ru.redactionReport(m.Data)
		case info.Kind == bundle.PathDiagnostics:
			if info.Sub != "" && (c.nsRemoved(info.Cluster, info.Namespace) || fileRemoved(p.Remove.Diagnostics, info.Cluster, info.Namespace, info.Pod, info.File)) {
				if !c.nsRemoved(info.Cluster, info.Namespace) {
					ru.files[m.Name] = true
				}
				led.Removed.DiagnosticFiles++
				ru.dropped[m.Name] = true
				continue
			}
			if info.Sub != "" {
				led.Kept.DiagnosticFiles++
			}
			out = ru.text(ru.diagnostic(info, m.Data))
		case info.Kind == bundle.PathFlows:
			if fileRemoved(p.Remove.Flows, info.Cluster, "", "", info.File) {
				ru.files[m.Name] = true
				led.Removed.FlowFiles++
				ru.dropped[m.Name] = true
				continue
			}
			led.Kept.FlowFiles++
			out = ru.text(m.Data)
		default:
			out = ru.text(m.Data)
		}
		if out == nil {
			continue
		}
		if err := w.Add(m.Name, out); err != nil {
			return nil, err
		}
	}
	if err := rd.Verify(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnverified, err)
	}
	led.Original.ManifestSHA256 = rd.ManifestSHA256()
	if rd.Signed() {
		trusted := o.Trusted
		if o.Signer != nil {
			// A capture signed with the key that signs the review is the reviewer's own.
			trusted = append(append([]sign.PublicKey{}, trusted...), o.Signer.Public())
		}
		s := rd.Signature(trusted)
		led.Original.Signature = &s
	}
	led.Removed.Namespaces = len(ru.names)
	if n := countNamespaces(p.Remove.Namespaces); n > led.Removed.Namespaces {
		led.Removed.Namespaces = n
	}
	led.Removed.Types = len(ru.types)
	led.Removed.LogStreams = len(ru.streams)
	for k := range ru.types {
		led.Types = append(led.Types, k)
	}
	sort.Strings(led.Types)
	if p.DiscloseNames {
		led.Namespaces = keys(ru.names)
		led.Objects = keys(ru.objects)
		led.LogStreams = keys(ru.streams)
		led.Files = keys(ru.files)
	}
	if err := w.AddJSON(bundle.LedgerName, led); err != nil {
		return nil, err
	}
	m := bundle.Manifest{}
	if man != nil {
		m.Complete, m.Errors = man.Complete, man.Errors
	}
	if err := w.Close(m); err != nil {
		return nil, err
	}
	return led, nil
}

// TextSHA is the hash a LineRef carries: hex SHA-256 of the line's text
// after the kubelet timestamp, which is what the desk stores as raw.
func TextSHA(text string) string {
	s := sha256.Sum256([]byte(text))
	return hex.EncodeToString(s[:])
}

// LineSHA hashes a line as it appears in a bundle log chunk.
func LineSHA(line string) string {
	_, rest := logs.SplitTimestamp(line)
	return TextSHA(rest)
}

func lineSHA(line string) string { return LineSHA(line) }

// hideNamespaces drops removed namespaces from the recorded policy's
// namespace list, which would otherwise name them.
func hideNamespaces(raw json.RawMessage, c *compiled) json.RawMessage {
	var pol map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &pol) != nil {
		return raw
	}
	list, _ := pol["namespaces"].([]any)
	if len(list) == 0 {
		return raw
	}
	var kept []any
	for _, v := range list {
		if !c.nsRemoved("", str(v)) && !anyClusterRemoved(c, str(v)) {
			kept = append(kept, v)
		}
	}
	pol["namespaces"] = kept
	b, err := json.Marshal(pol)
	if err != nil {
		return raw
	}
	return b
}

func anyClusterRemoved(c *compiled, ns string) bool {
	for _, n := range c.p.Remove.Namespaces {
		if glob(n.Namespace, ns) {
			return true
		}
	}
	return false
}

func countNamespaces(refs []NamespaceRef) int {
	n := 0
	for _, r := range refs {
		if !strings.ContainsAny(r.Namespace, "*?[") {
			n++
		}
	}
	return n
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// WriteFile reviews the bundle at src into dst, through dst.partial so a
// failed review never leaves a file that looks finished.
func WriteFile(ctx context.Context, src, dst string, p *Plan, o Options) (*Ledger, error) {
	in, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer in.Close()
	part := dst + ".partial"
	out, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	led, err := Apply(ctx, in, out, p, o)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(part)
		return nil, err
	}
	return led, os.Rename(part, dst)
}

// OutputName is where a reviewed bundle goes by default: next to the
// original, still matching flightbay-*.tar.zst so the desk finds it.
func OutputName(orig string, encrypted bool) string {
	base := strings.TrimSuffix(strings.TrimSuffix(orig, ".age"), ".tar.zst")
	base = strings.TrimSuffix(base, "-reviewed")
	out := base + "-reviewed.tar.zst"
	if encrypted {
		out += ".age"
	}
	return out
}
