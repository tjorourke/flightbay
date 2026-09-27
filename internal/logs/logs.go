// Package logs parses captured container log lines into structured records
// and evaluates the Flightbay log query language.
package logs

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Record is one parsed log line.
type Record struct {
	ID        int64             `json:"id"`
	Seq       int               `json:"seq"`
	TS        time.Time         `json:"ts"`
	Cluster   string            `json:"cluster"`
	Namespace string            `json:"namespace"`
	Pod       string            `json:"pod"`
	Container string            `json:"container"`
	Stream    string            `json:"stream"`
	Node      string            `json:"node,omitempty"`
	Level     string            `json:"level"`
	Scope     string            `json:"scope,omitempty"`
	Msg       string            `json:"msg"`
	Fields    map[string]string `json:"fields,omitempty"`
	Raw       string            `json:"raw"`
	Chunk     string            `json:"-"`
}

// SplitTimestamp removes the RFC 3339 timestamp the kubelet adds with timestamps=true.
func SplitTimestamp(line string) (time.Time, string) {
	sp := strings.IndexByte(line, ' ')
	if sp < 20 || sp > 40 {
		return time.Time{}, line
	}
	ts, err := time.Parse(time.RFC3339Nano, line[:sp])
	if err != nil {
		return time.Time{}, line
	}
	return ts, line[sp+1:]
}

// Parsed is the structured view of a line.
type Parsed struct {
	Level, Scope, Msg string
	Fields            map[string]string
}

var (
	appTS     = `\d{4}-\d{2}-\d{2}T\S+`
	istioRe   = regexp.MustCompile(`^` + appTS + `\s+(trace|debug|info|warn|warning|error|fatal|critical)\s+(\S+)\s+(.*)$`)
	klogRe    = regexp.MustCompile(`^([IWEF])\d{4} \d{2}:\d{2}:\d{2}\.\d+\s+\d+\s+([^\]]+)\] (.*)$`)
	envoyRe   = regexp.MustCompile(`^\[(\S+)\] "(\S+) (\S+) (\S+)" (\d{3}) (\S+) (.*)$`)
	logfmtKV  = regexp.MustCompile(`([A-Za-z_][\w.\-]*)=("(?:[^"\\]|\\.)*"|\S*)`)
	logfmtRe  = regexp.MustCompile(`^(?:[A-Za-z_][\w.\-]*=(?:"(?:[^"\\]|\\.)*"|\S*)\s*)+$`)
	errWords  = regexp.MustCompile(`\b(ERROR|FATAL|CRITICAL|Exception|Traceback)\b|^(panic|error|fatal):`)
	warnWords = regexp.MustCompile(`\b(WARN|WARNING)\b|^warning:`)
	dbgWords  = regexp.MustCompile(`\b(DEBUG|TRACE)\b`)
)

// NormalizeLevel maps the many level spellings onto trace, debug, info, warn, error.
func NormalizeLevel(l string) string {
	switch strings.ToLower(strings.TrimSpace(l)) {
	case "error", "err", "e", "fatal", "f", "critical", "crit", "panic", "alert", "emergency", "emerg", "severe":
		return "error"
	case "warn", "warning", "w":
		return "warn"
	case "debug", "d", "dbg":
		return "debug"
	case "trace", "t":
		return "trace"
	case "":
		return ""
	default:
		return "info"
	}
}

func kvFields(s string, into map[string]string) map[string]string {
	for _, m := range logfmtKV.FindAllStringSubmatch(s, -1) {
		v := m[2]
		if strings.HasPrefix(v, `"`) {
			if uq, err := strconv.Unquote(v); err == nil {
				v = uq
			}
		}
		if v == "" {
			continue
		}
		if into == nil {
			into = map[string]string{}
		}
		if _, exists := into[m[1]]; !exists {
			into[m[1]] = v
		}
	}
	return into
}

func str(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	case nil:
		return ""
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

// Parse sniffs the format: JSON, istio/ztunnel text, Envoy access log, klog, logfmt, then raw.
func Parse(line string) Parsed {
	s := strings.TrimSpace(line)
	if strings.HasPrefix(s, "{") {
		var m map[string]any
		if json.Unmarshal([]byte(s), &m) == nil {
			p := Parsed{Fields: map[string]string{}}
			for _, k := range []string{"level", "lvl", "severity", "log.level"} {
				if v, ok := m[k]; ok {
					p.Level = NormalizeLevel(str(v))
					delete(m, k)
					break
				}
			}
			for _, k := range []string{"msg", "message", "log", "text"} {
				if v, ok := m[k]; ok {
					p.Msg = str(v)
					delete(m, k)
					break
				}
			}
			for _, k := range []string{"scope", "logger", "component", "target"} {
				if v, ok := m[k]; ok {
					p.Scope = str(v)
					delete(m, k)
					break
				}
			}
			for _, k := range []string{"time", "ts", "timestamp", "@timestamp"} {
				delete(m, k)
			}
			for k, v := range m {
				if _, nested := v.(map[string]any); nested {
					continue
				}
				p.Fields[k] = str(v)
			}
			if p.Level == "" {
				p.Level = "info"
				if st, err := strconv.Atoi(p.Fields["status"]); err == nil && st >= 500 {
					p.Level = "error"
				}
			}
			if p.Msg == "" {
				p.Msg = s
			}
			return p
		}
	}
	if m := istioRe.FindStringSubmatch(s); m != nil {
		return Parsed{Level: NormalizeLevel(m[1]), Scope: m[2], Msg: m[3], Fields: kvFields(m[3], nil)}
	}
	if m := envoyRe.FindStringSubmatch(s); m != nil {
		st, _ := strconv.Atoi(m[5])
		lvl := "info"
		if st >= 500 {
			lvl = "error"
		} else if st >= 400 {
			lvl = "warn"
		}
		return Parsed{Level: lvl, Scope: "access", Msg: fmt.Sprintf("%s %s %s %s", m[2], m[3], m[4], m[5]),
			Fields: map[string]string{"method": m[2], "path": m[3], "status": m[5], "response_flags": m[6]}}
	}
	if m := klogRe.FindStringSubmatch(s); m != nil {
		return Parsed{Level: NormalizeLevel(m[1]), Scope: m[2], Msg: m[3], Fields: kvFields(m[3], nil)}
	}
	if logfmtRe.MatchString(s) && (strings.Contains(s, "level=") || strings.Contains(s, "msg=")) {
		f := kvFields(s, nil)
		p := Parsed{Level: NormalizeLevel(f["level"]), Msg: f["msg"], Scope: f["scope"], Fields: f}
		delete(f, "level")
		delete(f, "msg")
		delete(f, "scope")
		delete(f, "time")
		delete(f, "ts")
		if p.Level == "" {
			p.Level = "info"
		}
		if p.Msg == "" {
			p.Msg = s
		}
		return p
	}
	lvl := "info"
	switch {
	case errWords.MatchString(s):
		lvl = "error"
	case warnWords.MatchString(s):
		lvl = "warn"
	case dbgWords.MatchString(s):
		lvl = "debug"
	}
	return Parsed{Level: lvl, Msg: s, Fields: kvFields(s, nil)}
}

var (
	sigAddr = regexp.MustCompile(`\b\d{1,3}(\.\d{1,3}){3}(:\d+)?\b`)
	sigUUID = regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`)
	sigNum  = regexp.MustCompile(`\b\d+(\.\d+)?(ms|us|µs|ns|s|m|h|%|b|kb|mb|gb)?\b`)
	sigHex  = regexp.MustCompile(`\b[0-9a-fA-F]*[0-9][0-9a-fA-F]*\b`)
)

// Signature collapses variable parts so similar errors group together.
func Signature(msg string) string {
	s := sigAddr.ReplaceAllString(msg, "<addr>")
	s = sigUUID.ReplaceAllString(s, "<id>")
	s = sigNum.ReplaceAllString(s, "<n>")
	s = sigHex.ReplaceAllStringFunc(s, func(m string) string {
		if len(m) >= 4 {
			return "<id>"
		}
		return m
	})
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

type clause interface{ match(*Record) bool }

type textClause struct{ text string }

func (c textClause) match(r *Record) bool {
	return strings.Contains(strings.ToLower(r.Msg+" "+r.Raw), c.text)
}

type fieldClause struct{ key, val string }

func (c fieldClause) match(r *Record) bool {
	var have string
	switch c.key {
	case "level":
		return valueMatch(r.Level, NormalizeLevel(c.val))
	case "ns", "namespace":
		have = r.Namespace
	case "pod":
		have = r.Pod
	case "container":
		have = r.Container
	case "cluster":
		have = r.Cluster
	case "stream":
		have = r.Stream
	case "scope":
		have = r.Scope
	case "node":
		have = r.Node
	default:
		v, ok := r.Fields[c.key]
		if !ok {
			return false
		}
		have = v
	}
	return valueMatch(have, c.val)
}

func valueMatch(have, want string) bool {
	have, want = strings.ToLower(have), strings.ToLower(want)
	if strings.HasSuffix(want, "*") {
		return strings.HasPrefix(have, strings.TrimSuffix(want, "*"))
	}
	return have == want
}

type notClause struct{ c clause }

func (c notClause) match(r *Record) bool { return !c.c.match(r) }

type orClause struct{ cs []clause }

func (c orClause) match(r *Record) bool {
	for _, x := range c.cs {
		if x.match(r) {
			return true
		}
	}
	return false
}

// Query is a parsed log query. Terms are ANDed; OR joins neighbours.
type Query struct {
	Text      string
	clauses   []clause
	Since     time.Duration
	Reference time.Time // "now" for since:, normally the end of the capture
}

func tokenize(s string) ([]string, error) {
	var toks []string
	var cur strings.Builder
	inQ := false
	for _, r := range s {
		switch {
		case r == '"':
			inQ = !inQ
			cur.WriteRune(r)
		case (r == ' ' || r == '\t') && !inQ:
			if cur.Len() > 0 {
				toks = append(toks, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if inQ {
		return nil, fmt.Errorf("unterminated quote in query")
	}
	if cur.Len() > 0 {
		toks = append(toks, cur.String())
	}
	return toks, nil
}

var fieldTok = regexp.MustCompile(`^([A-Za-z_][\w.\-]*):(.+)$`)

// ParseQuery parses level:error ns:payments pod:checkout* trace_id:x "phrase" -term since:15m.
func ParseQuery(s string) (*Query, error) {
	q := &Query{Text: s}
	toks, err := tokenize(s)
	if err != nil {
		return nil, err
	}
	pendingOr := false
	for _, t := range toks {
		if t == "OR" {
			if len(q.clauses) == 0 {
				return nil, fmt.Errorf("OR needs a term on each side")
			}
			pendingOr = true
			continue
		}
		neg := false
		if strings.HasPrefix(t, "-") && len(t) > 1 {
			neg, t = true, t[1:]
		}
		var c clause
		if m := fieldTok.FindStringSubmatch(t); m != nil && !strings.HasPrefix(t, `"`) {
			key, val := strings.ToLower(m[1]), strings.Trim(m[2], `"`)
			if key == "since" {
				d, err := time.ParseDuration(val)
				if err != nil {
					return nil, fmt.Errorf("since: %w", err)
				}
				q.Since = d
				continue
			}
			c = fieldClause{key: key, val: val}
		} else {
			c = textClause{text: strings.ToLower(strings.Trim(t, `"`))}
		}
		if neg {
			c = notClause{c}
		}
		if pendingOr {
			last := q.clauses[len(q.clauses)-1]
			if oc, ok := last.(orClause); ok {
				oc.cs = append(oc.cs, c)
				q.clauses[len(q.clauses)-1] = oc
			} else {
				q.clauses[len(q.clauses)-1] = orClause{cs: []clause{last, c}}
			}
			pendingOr = false
			continue
		}
		q.clauses = append(q.clauses, c)
	}
	if pendingOr {
		return nil, fmt.Errorf("OR needs a term on each side")
	}
	return q, nil
}

// Match reports whether a record satisfies the query.
func (q *Query) Match(r *Record) bool {
	if q.Since > 0 && !q.Reference.IsZero() && r.TS.Before(q.Reference.Add(-q.Since)) {
		return false
	}
	for _, c := range q.clauses {
		if !c.match(r) {
			return false
		}
	}
	return true
}

// SortNewestFirst orders records by time, newest first, keeping line order stable.
func SortNewestFirst(rs []*Record) {
	sort.SliceStable(rs, func(i, j int) bool {
		if !rs[i].TS.Equal(rs[j].TS) {
			return rs[i].TS.After(rs[j].TS)
		}
		return rs[i].ID > rs[j].ID
	})
}
