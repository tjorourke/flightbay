package scan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/posture"
	"github.com/tjorourke/flightbay/internal/solodocs"
	"github.com/tjorourke/flightbay/internal/store"
)

// Severities in order, highest first.
var severityRank = map[string]int{"critical": 4, "high": 3, "medium": 2, "low": 1}

// ValidSeverity reports whether s is a --fail-on value.
func ValidSeverity(s string) bool { return severityRank[s] > 0 }

// Failure is one failing object of one Scorecard check, with the file it was
// declared in.
type Failure struct {
	Check    string       `json:"check"`
	Area     string       `json:"area"`
	Category string       `json:"category"`
	Severity string       `json:"severity"`
	Title    string       `json:"title"`
	Detail   string       `json:"detail"`
	Guide    string       `json:"guide"`          // a related public field guide
	Docs     string       `json:"docs,omitempty"` // a related product documentation page
	Item     posture.Item `json:"item"`
	File     string       `json:"file"`
	Line     int          `json:"line,omitempty"`
	Renderer string       `json:"renderedBy,omitempty"`
	New      bool         `json:"new"`
}

// Key identifies a failure across two scans: the check and the object.
func (f Failure) Key() string {
	return f.Check + "|" + f.Item.Kind + "|" + f.Item.Namespace + "|" + f.Item.Name
}

// Object names the item as Kind ns/name.
func (f Failure) Object() string {
	if f.Item.Name == "" {
		return ""
	}
	if f.Item.Namespace == "" {
		return f.Item.Kind + " " + f.Item.Name
	}
	return f.Item.Kind + " " + f.Item.Namespace + "/" + f.Item.Name
}

// Gate is the result of scoring a scan.
type Gate struct {
	Repo     *bundle.RepoInfo    `json:"repo,omitempty"`
	Areas    []posture.AreaScore `json:"areas"`
	Failures []Failure           `json:"failures"`
	Baseline string              `json:"baseline,omitempty"` // what --new-only compared with
	Checks   []posture.Check     `json:"-"`
}

// Evaluate scores a scanned workspace with the Scorecard (and Agent reach
// when that feature is on) and lists every failing object with its source.
func Evaluate(ws *store.Workspace) Gate {
	r := posture.Evaluate(ws)
	g := Gate{Areas: r.Areas, Checks: r.Checks, Failures: []Failure{}}
	if h := ws.Header(); h != nil {
		g.Repo = h.Repo
	}
	fallback := ""
	for _, c := range ws.Coverage() {
		if c.Type == bundle.CoverageInput && c.State == bundle.StateComplete && c.Items > 0 {
			fallback = c.Source
			break
		}
	}
	for _, c := range r.Checks {
		if c.Status != posture.Fail {
			continue
		}
		items := 0
		for _, it := range c.Items {
			if it.Status != posture.Fail {
				continue
			}
			items++
			g.Failures = append(g.Failures, failure(ws, c, it, fallback))
		}
		if items == 0 {
			g.Failures = append(g.Failures, failure(ws, c, posture.Item{Status: posture.Fail, Detail: c.Reason}, fallback))
		}
	}
	sort.SliceStable(g.Failures, func(i, j int) bool {
		a, b := g.Failures[i], g.Failures[j]
		if severityRank[a.Severity] != severityRank[b.Severity] {
			return severityRank[a.Severity] > severityRank[b.Severity]
		}
		if a.Check != b.Check {
			return a.Check < b.Check
		}
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})
	return g
}

func failure(ws *store.Workspace, c posture.Check, it posture.Item, fallback string) Failure {
	f := Failure{Check: c.ID, Area: c.Area, Category: c.Category, Severity: c.Severity, Title: c.Title, Detail: it.Detail, Guide: c.Source, Docs: c.Docs, Item: it, New: true}
	if o := findObject(ws, it); o != nil {
		ann := asMap(asMap(o.Obj["metadata"])["annotations"])
		f.File = asStr(ann[SourceAnnotation])
		f.Line, _ = strconv.Atoi(asStr(ann[LineAnnotation]))
		f.Renderer = asStr(ann[RenderedByAnnotation])
	}
	ns := it.Namespace
	if it.Kind == "Namespace" {
		ns = it.Name
	}
	if f.File == "" && ns != "" {
		// A namespace-level finding: point at something declared in it.
		for _, o := range ws.AllObjects(it.Cluster) {
			if o.Namespace == ns || (o.Kind == "Namespace" && o.Name == ns) {
				if src := asStr(asMap(asMap(o.Obj["metadata"])["annotations"])[SourceAnnotation]); src != "" {
					f.File = src
					break
				}
			}
		}
	}
	if f.File == "" {
		f.File = fallback
	}
	return f
}

func findObject(ws *store.Workspace, it posture.Item) *store.Object {
	if it.Name == "" {
		return nil
	}
	cl := it.Cluster
	if cl == "" {
		if cs := ws.Clusters(); len(cs) > 0 {
			cl = cs[0]
		}
	}
	res := it.Resource
	if res == "" {
		for _, r := range ws.Resources(cl) {
			if r.Kind == it.Kind && (it.Group == "" || r.Group == it.Group) {
				res = r.Resource
				break
			}
		}
	}
	if o := ws.Get(cl, it.Group, res, it.Namespace, it.Name); o != nil {
		return o
	}
	for _, o := range ws.AllObjects(cl) {
		if o.Kind == it.Kind && o.Namespace == it.Namespace && o.Name == it.Name {
			return o
		}
	}
	return nil
}

// MarkNew compares with a baseline gate: failures the baseline already had
// are not new. It returns how many are new.
func (g *Gate) MarkNew(base Gate, label string) int {
	old := map[string]bool{}
	for _, f := range base.Failures {
		old[f.Key()] = true
	}
	n := 0
	for i := range g.Failures {
		g.Failures[i].New = !old[g.Failures[i].Key()]
		if g.Failures[i].New {
			n++
		}
	}
	g.Baseline = label
	return n
}

// Gating returns the failures that count: new ones (all, without a
// baseline) at or above the threshold. An empty threshold gates nothing.
func (g Gate) Gating(threshold string) []Failure {
	var out []Failure
	min := severityRank[threshold]
	if min == 0 {
		return nil
	}
	for _, f := range g.Failures {
		if f.New && severityRank[f.Severity] >= min {
			out = append(out, f)
		}
	}
	return out
}

// ExitCode is 1 when anything gates at the threshold, else 0.
func (g Gate) ExitCode(threshold string) int {
	if len(g.Gating(threshold)) > 0 {
		return 1
	}
	return 0
}

// Reported is what the report shows: only new failures when compared with
// a baseline, otherwise all.
func (g Gate) Reported() []Failure {
	if g.Baseline == "" {
		return g.Failures
	}
	var out []Failure
	for _, f := range g.Failures {
		if f.New {
			out = append(out, f)
		}
	}
	return out
}

// ---------- formats ----------

// Write renders the gate in a format: text, json, sarif or github.
func (g Gate) Write(w io.Writer, format, threshold, version string) error {
	switch format {
	case "", "text":
		return g.text(w, threshold)
	case "json":
		out := struct {
			Gate
			FailOn   string         `json:"failOn,omitempty"`
			Gating   int            `json:"gating"`
			Summary  map[string]int `json:"summary"`
			Reported []Failure      `json:"reported"`
			Checks   []checkSummary `json:"checks"`
		}{Gate: g, FailOn: threshold, Gating: len(g.Gating(threshold)), Summary: g.summary(), Reported: nonNil(g.Reported()), Checks: g.checkSummaries()}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return err
		}
		_, err = w.Write(append(b, '\n'))
		return err
	case "sarif":
		b, err := json.MarshalIndent(g.SARIF(version), "", "  ")
		if err != nil {
			return err
		}
		_, err = w.Write(append(b, '\n'))
		return err
	case "github":
		return g.github(w, threshold)
	case "codequality":
		b, err := json.MarshalIndent(g.CodeQuality(), "", "  ")
		if err != nil {
			return err
		}
		_, err = w.Write(append(b, '\n'))
		return err
	}
	return fmt.Errorf("unknown format %q: use text, json, sarif, github or codequality", format)
}

type checkSummary struct {
	ID       string         `json:"id"`
	Area     string         `json:"area"`
	Severity string         `json:"severity"`
	Title    string         `json:"title"`
	Status   posture.Status `json:"status"`
	Reason   string         `json:"reason,omitempty"`
	Passed   int            `json:"passed"`
	Total    int            `json:"total"`
}

func (g Gate) checkSummaries() []checkSummary {
	out := []checkSummary{}
	for _, c := range g.Checks {
		out = append(out, checkSummary{c.ID, c.Area, c.Severity, c.Title, c.Status, c.Reason, c.Passed, c.Total})
	}
	return out
}

func nonNil(f []Failure) []Failure {
	if f == nil {
		return []Failure{}
	}
	return f
}

func (g Gate) summary() map[string]int {
	out := map[string]int{}
	for _, f := range g.Reported() {
		out[f.Severity]++
	}
	return out
}

func (g Gate) text(w io.Writer, threshold string) error {
	if g.Repo != nil {
		c := g.Repo.Commit
		if len(c) > 12 {
			c = c[:12]
		}
		fmt.Fprintf(w, "Declared state from %s", g.Repo.Name)
		if c != "" {
			fmt.Fprintf(w, " at commit %s", c)
		}
		if g.Repo.Dirty {
			fmt.Fprint(w, " (uncommitted changes)")
		}
		fmt.Fprintln(w)
	}
	for _, a := range g.Areas {
		if !a.Present {
			continue
		}
		fmt.Fprintf(w, "  %-45s %3d  %s  (%d pass, %d warn, %d fail, %d not applicable)\n", a.Title, a.Score, a.Grade, a.Counts[posture.Pass], a.Counts[posture.Warn], a.Counts[posture.Fail], a.Counts[posture.NA])
	}
	rep := g.Reported()
	if g.Baseline != "" {
		fmt.Fprintf(w, "\nCompared with %s: %d new failures (%d in total)\n", g.Baseline, len(rep), len(g.Failures))
	}
	if len(rep) == 0 {
		fmt.Fprintln(w, "\nNo failing checks.")
	} else {
		fmt.Fprintln(w)
		last := ""
		for _, f := range rep {
			if f.Check != last {
				fmt.Fprintf(w, "[%s] %s (%s)\n", strings.ToUpper(f.Severity), f.Title, f.Check)
				last = f.Check
			}
			loc := f.File
			if f.Line > 0 {
				loc += ":" + strconv.Itoa(f.Line)
			}
			obj := f.Object()
			if obj != "" {
				obj += ": "
			}
			fmt.Fprintf(w, "    %s  %s%s\n", loc, obj, f.Detail)
		}
	}
	if threshold != "" {
		n := len(g.Gating(threshold))
		if n > 0 {
			fmt.Fprintf(w, "\nFAIL: %d failing %s at %s or above\n", n, Plural(n, "item", "items"), threshold)
		} else {
			fmt.Fprintf(w, "\nPASS: nothing failing at %s or above\n", threshold)
		}
	}
	return nil
}

// Plural picks the word for n.
func Plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func ghEscape(s string, prop bool) string {
	s = strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(s)
	if prop {
		s = strings.NewReplacer(":", "%3A", ",", "%2C").Replace(s)
	}
	return s
}

func (g Gate) github(w io.Writer, threshold string) error {
	min := severityRank[threshold]
	for _, f := range g.Reported() {
		cmd := "warning"
		switch {
		case min > 0 && severityRank[f.Severity] >= min:
			cmd = "error"
		case min == 0 && severityRank[f.Severity] >= severityRank["high"]:
			cmd = "error"
		case f.Severity == "low":
			cmd = "notice"
		}
		props := "file=" + ghEscape(f.File, true)
		if f.Line > 0 {
			props += ",line=" + strconv.Itoa(f.Line)
		}
		props += ",title=" + ghEscape("Flightbay "+f.Check, true)
		fmt.Fprintf(w, "::%s %s::%s\n", cmd, props, ghEscape(message(f), false))
	}
	return nil
}

func message(f Failure) string {
	m := f.Title
	if obj := f.Object(); obj != "" {
		m += ": " + obj
	}
	if f.Detail != "" {
		m += ": " + f.Detail
	}
	return m
}

// ---------- GitLab Code Quality ----------

var codeQualitySeverity = map[string]string{"critical": "blocker", "high": "critical", "medium": "major", "low": "minor"}

// CodeQuality renders the reported failures as a GitLab Code Quality report
// (the Code Climate issue subset GitLab reads): one issue per failing object.
func (g Gate) CodeQuality() []map[string]any {
	out := []map[string]any{}
	for _, f := range g.Reported() {
		line := f.Line
		if line < 1 {
			line = 1
		}
		sum := sha256.Sum256([]byte(f.Key()))
		out = append(out, map[string]any{
			"type": "issue", "check_name": f.Check, "description": message(f), "categories": []string{"Security"},
			"severity":    codeQualitySeverity[f.Severity],
			"fingerprint": hex.EncodeToString(sum[:16]),
			"location":    map[string]any{"path": f.File, "lines": map[string]int{"begin": line}},
			"content":     map[string]string{"body": strings.Join(links(f.Guide, f.Docs), "\n")},
		})
	}
	return out
}

// links labels the field guide and the docs page, skipping empty ones and a
// docs link that repeats the guide.
func links(guide, docs string) []string {
	var out []string
	if guide != "" {
		out = append(out, "Guide: "+guide)
	}
	if docs != "" && docs != guide {
		label := "Product docs: "
		if !solodocs.IsProductDocs(docs) {
			label = "Docs: "
		}
		out = append(out, label+docs)
	}
	return out
}

// ---------- SARIF 2.1.0 ----------

type sarifLog struct {
	Version string     `json:"version"`
	Schema  string     `json:"$schema"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool           sarifTool           `json:"tool"`
	Results        []sarifResult       `json:"results"`
	Properties     map[string]any      `json:"properties,omitempty"`
	VersionControl []map[string]string `json:"versionControlProvenance,omitempty"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name           string      `json:"name"`
	InformationURI string      `json:"informationUri"`
	Version        string      `json:"version,omitempty"`
	Rules          []sarifRule `json:"rules"`
}

type sarifText struct {
	Text     string `json:"text"`
	Markdown string `json:"markdown,omitempty"`
}

type sarifRule struct {
	ID                   string         `json:"id"`
	Name                 string         `json:"name"`
	ShortDescription     sarifText      `json:"shortDescription"`
	FullDescription      sarifText      `json:"fullDescription"`
	HelpURI              string         `json:"helpUri,omitempty"`
	Help                 sarifText      `json:"help"`
	DefaultConfiguration map[string]any `json:"defaultConfiguration"`
	Properties           map[string]any `json:"properties"`
}

type sarifResult struct {
	RuleID              string            `json:"ruleId"`
	RuleIndex           int               `json:"ruleIndex"`
	Level               string            `json:"level"`
	Message             sarifText         `json:"message"`
	Locations           []sarifLocation   `json:"locations"`
	PartialFingerprints map[string]string `json:"partialFingerprints"`
	Properties          map[string]any    `json:"properties,omitempty"`
}

type sarifLocation struct {
	PhysicalLocation sarifPhysical `json:"physicalLocation"`
}

type sarifPhysical struct {
	ArtifactLocation map[string]string `json:"artifactLocation"`
	Region           map[string]int    `json:"region"`
}

// SARIFLevel maps a check severity to a SARIF level.
func SARIFLevel(sev string) string {
	switch sev {
	case "critical", "high":
		return "error"
	case "medium":
		return "warning"
	}
	return "note"
}

var securitySeverity = map[string]string{"critical": "9.5", "high": "8.0", "medium": "5.5", "low": "3.0"}

// SARIF renders the reported failures as a SARIF 2.1.0 log: one result per
// failing object, one rule per check, located at the file (and line) the
// object was declared in.
func (g Gate) SARIF(version string) any {
	rules := []sarifRule{}
	index := map[string]int{}
	checks := map[string]posture.Check{}
	for _, c := range g.Checks {
		checks[c.ID] = c
	}
	results := []sarifResult{}
	for _, f := range g.Reported() {
		i, ok := index[f.Check]
		if !ok {
			c := checks[f.Check]
			i = len(rules)
			index[f.Check] = i
			why := c.Why
			if why == "" {
				why = f.Title
			}
			// helpUri is the product documentation where there is a page for
			// it; the field guide goes in the help text either way.
			helpURI := f.Docs
			if helpURI == "" {
				helpURI = f.Guide
			}
			ls := links(f.Guide, f.Docs)
			help := "Fix:\n" + c.Fix + "\n\n" + strings.Join(ls, "\n")
			rules = append(rules, sarifRule{ID: f.Check, Name: ruleName(f.Check), ShortDescription: sarifText{Text: f.Title}, FullDescription: sarifText{Text: why},
				HelpURI: helpURI, Help: sarifText{Text: help, Markdown: "**" + f.Title + "**\n\n" + why + "\n\n```yaml\n" + c.Fix + "\n```\n\n" + strings.Join(ls, "\n\n")},
				DefaultConfiguration: map[string]any{"level": SARIFLevel(f.Severity)},
				Properties:           map[string]any{"security-severity": securitySeverity[f.Severity], "tags": []string{"flightbay", f.Area, strings.ToLower(f.Category)}, "precision": "high"}})
		}
		line := f.Line
		if line < 1 {
			line = 1
		}
		file := f.File
		if file == "" {
			file = "."
		}
		sum := sha256.Sum256([]byte(f.Key()))
		results = append(results, sarifResult{RuleID: f.Check, RuleIndex: i, Level: SARIFLevel(f.Severity), Message: sarifText{Text: message(f)},
			Locations:           []sarifLocation{{PhysicalLocation: sarifPhysical{ArtifactLocation: map[string]string{"uri": file, "uriBaseId": "%SRCROOT%"}, Region: map[string]int{"startLine": line}}}},
			PartialFingerprints: map[string]string{"flightbayObject/v1": hex.EncodeToString(sum[:16])},
			Properties:          map[string]any{"severity": f.Severity, "object": f.Object(), "renderedBy": f.Renderer}})
	}
	run := sarifRun{Tool: sarifTool{Driver: sarifDriver{Name: "Flightbay", InformationURI: "https://github.com/tjorourke/flightbay", Version: version, Rules: rules}}, Results: results}
	if g.Repo != nil && g.Repo.Remote != "" {
		vc := map[string]string{"repositoryUri": g.Repo.Remote}
		if g.Repo.Commit != "" {
			vc["revisionId"] = g.Repo.Commit
		}
		if g.Repo.Branch != "" {
			vc["branch"] = g.Repo.Branch
		}
		run.VersionControl = []map[string]string{vc}
	}
	return sarifLog{Version: "2.1.0", Schema: "https://json.schemastore.org/sarif-2.1.0.json", Runs: []sarifRun{run}}
}

func ruleName(id string) string {
	var b strings.Builder
	for _, part := range strings.FieldsFunc(id, func(r rune) bool { return r == '.' || r == '-' }) {
		if part == "" {
			continue
		}
		b.WriteString(strings.ToUpper(part[:1]) + part[1:])
	}
	return b.String()
}
