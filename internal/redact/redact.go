// Package redact removes credentials from Kubernetes objects and log text
// before anything is written to a Flightbay bundle. It records how many
// replacements were made and where, never the values themselves.
package redact

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Placeholder is written in place of every redacted value.
const Placeholder = "[REDACTED]"

// LastApplied is the annotation that can hold a full copy of an object.
const LastApplied = "kubectl.kubernetes.io/last-applied-configuration"

// SecretMode controls how much of a Secret is captured. No mode captures values.
type SecretMode string

const (
	SecretMetadata SecretMode = "metadata" // name, type, labels, annotations
	SecretKeys     SecretMode = "keys"     // metadata plus key names and byte lengths
)

type rule struct {
	name    string
	re      *regexp.Regexp
	replace func(r *Redactor, loc string, m []string) (string, bool)
}

// Redactor applies the built-in scrubbers plus customer-supplied patterns.
type Redactor struct {
	rules  []rule
	fields FieldRules
	mu     sync.Mutex
	locs   map[string]map[string]int
	total  int
}

// Report is written to redaction-report.json.
type Report struct {
	Total     int                       `json:"total"`
	Locations map[string]map[string]int `json:"locations"`
	Note      string                    `json:"note"`
}

var (
	pemRe       = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`)
	pemBeginRe  = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)
	pemEndRe    = regexp.MustCompile(`-----END [A-Z ]*PRIVATE KEY-----`)
	authRe      = regexp.MustCompile(`(?i)(authorization"?\s*[:=]\s*"?(?:bearer|basic)\s+)([A-Za-z0-9._~+/=\-]+)`)
	jwtRe       = regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{5,}\.[A-Za-z0-9_\-]{5,}\.[A-Za-z0-9_\-]{5,}`)
	awsRe       = regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)
	apiKeyRe    = regexp.MustCompile(`\bsk-(?:ant-)?[A-Za-z0-9_\-]{16,}`)
	urlCredRe   = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)[^/\s:@"]+:[^/\s@"]+@`)
	sensitiveKV = regexp.MustCompile(`(?i)([\w.\-]*(?:password|passwd|secret|token|api[_\-]?key|credential)[\w.\-]*)("?\s*[=:]\s*)("?)([^\s"&,;}]+)`)
	// SensitiveKey matches object keys and env var names whose values are credentials.
	SensitiveKey = regexp.MustCompile(`(?i)(password|passwd|secret|token|api[_\-]?key|credential)`)
	// Keys that name or point at a credential rather than containing one.
	referenceKey = regexp.MustCompile(`(Name|Namespace|Ref|Refs|Path|File|Mode|Type|Audience|Expiration|ExpirationSeconds|Selector|Kind|Key)$`)
	digitsOnly   = regexp.MustCompile(`^[0-9]{1,7}$`)
)

// Rules describes the built-in scrubbers, in the order they run. The customer
// document (flightbay docs collects) is generated from this list.
var Rules = []struct{ Name, Description string }{
	{"private-key", "PEM private keys (certificates are kept for expiry and SAN checks)"},
	{"authorization", "Authorization: Bearer / Basic header values"},
	{"jwt", "JSON Web Tokens"},
	{"aws-access-key", "AWS access key IDs (AKIA..., ASIA...)"},
	{"api-key", "sk-... and sk-ant-... style API keys"},
	{"url-credentials", "user:password@ in URLs"},
	{"sensitive-key", "the value of any key or variable whose name contains password, passwd, secret, token, api_key or credential"},
}

// New builds a Redactor. Extra patterns are Go regular expressions; any match is replaced.
func New(extra []string) (*Redactor, error) {
	r := &Redactor{locs: map[string]map[string]int{}}
	whole := func(string) func(*Redactor, string, []string) (string, bool) {
		return func(*Redactor, string, []string) (string, bool) { return Placeholder, true }
	}
	r.rules = []rule{
		{"private-key", pemRe, func(*Redactor, string, []string) (string, bool) { return "[REDACTED PRIVATE KEY]", true }},
		{"authorization", authRe, func(_ *Redactor, _ string, m []string) (string, bool) {
			if m[2] == Placeholder || strings.HasPrefix(m[2], "[REDACTED") {
				return m[0], false
			}
			return m[1] + Placeholder, true
		}},
		{"jwt", jwtRe, whole("jwt")},
		{"aws-access-key", awsRe, whole("aws")},
		{"api-key", apiKeyRe, whole("api")},
		{"url-credentials", urlCredRe, func(_ *Redactor, _ string, m []string) (string, bool) { return m[1] + Placeholder + "@", true }},
		{"sensitive-key", sensitiveKV, func(_ *Redactor, _ string, m []string) (string, bool) {
			v := m[4]
			if strings.HasPrefix(v, "[REDACTED") || digitsOnly.MatchString(v) || v == "true" || v == "false" || v == "null" {
				return m[0], false
			}
			return m[1] + m[2] + m[3] + Placeholder, true
		}},
	}
	for i, p := range extra {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("redaction pattern %d %q: %w", i+1, p, err)
		}
		r.rules = append(r.rules, rule{fmt.Sprintf("extra-%d", i+1), re, whole("extra")})
	}
	return r, nil
}

func (r *Redactor) count(loc, name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.locs[loc] == nil {
		r.locs[loc] = map[string]int{}
	}
	r.locs[loc][name]++
	r.total++
}

// String scrubs free text (a log line, a ConfigMap value, an argument).
func (r *Redactor) String(loc, s string) string {
	if s == "" {
		return s
	}
	for _, ru := range r.rules {
		if !ru.re.MatchString(s) {
			continue
		}
		ru := ru
		s = ru.re.ReplaceAllStringFunc(s, func(match string) string {
			m := ru.re.FindStringSubmatch(match)
			out, changed := ru.replace(r, loc, m)
			if changed {
				r.count(loc, ru.name)
			}
			return out
		})
	}
	return s
}

// LineRedactor scrubs a stream of log lines, including private keys that span lines.
type LineRedactor struct {
	r     *Redactor
	loc   string
	inKey bool
}

// Lines returns a stateful redactor for one log stream.
func (r *Redactor) Lines(loc string) *LineRedactor { return &LineRedactor{r: r, loc: loc} }

// Line scrubs one line.
func (l *LineRedactor) Line(s string) string {
	if l.inKey {
		if pemEndRe.MatchString(s) {
			l.inKey = false
		}
		return "[REDACTED PRIVATE KEY]"
	}
	if pemBeginRe.MatchString(s) && !pemEndRe.MatchString(s) {
		l.inKey = true
		l.r.count(l.loc, "private-key")
		idx := pemBeginRe.FindStringIndex(s)
		return l.r.String(l.loc, s[:idx[0]]) + "[REDACTED PRIVATE KEY]"
	}
	return l.r.String(l.loc, s)
}

// Object returns a scrubbed deep copy of an unstructured Kubernetes object.
func (r *Redactor) Object(loc string, obj map[string]any) map[string]any {
	out, _ := r.walk(loc, "", obj).(map[string]any)
	if md, ok := out["metadata"].(map[string]any); ok {
		delete(md, "managedFields")
	}
	r.dropFields(loc, out)
	return out
}

func (r *Redactor) walk(loc, key string, v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		name, _ := t["name"].(string)
		for k, val := range t {
			if k == "managedFields" {
				continue
			}
			if s, ok := val.(string); ok && s != "" {
				if (k == "value" && name != "" && SensitiveKey.MatchString(name)) ||
					(SensitiveKey.MatchString(k) && !referenceKey.MatchString(k) && k != LastApplied) {
					r.count(loc, "sensitive-key")
					out[k] = Placeholder
					continue
				}
			}
			out[k] = r.walk(loc, k, val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = r.walk(loc, key, val)
		}
		return out
	case string:
		// A string that is itself JSON (last-applied-configuration, JSON in a
		// ConfigMap value, an embedded manifest) is redacted structurally, so
		// env name/value pairs and sensitive keys inside it are caught too.
		if ts := strings.TrimSpace(t); len(ts) > 1 && (ts[0] == '{' || ts[0] == '[') {
			var inner any
			if json.Unmarshal([]byte(ts), &inner) == nil {
				if b, err := json.Marshal(r.walk(loc, key, inner)); err == nil {
					return string(b)
				}
			}
		}
		return r.String(loc, t)
	default:
		return v
	}
}

// Secret reduces a Secret to metadata, optionally with key names and byte lengths.
// Values never survive. With helm set, Helm release Secrets become a chart summary.
func (r *Redactor) Secret(loc string, obj map[string]any, mode SecretMode, helm bool) map[string]any {
	out := map[string]any{}
	for _, k := range []string{"apiVersion", "kind", "type", "immutable"} {
		if v, ok := obj[k]; ok {
			out[k] = v
		}
	}
	if md, ok := obj["metadata"].(map[string]any); ok {
		clean := map[string]any{}
		for k, v := range md {
			if k == "managedFields" {
				continue
			}
			clean[k] = v
		}
		if ann, ok := md["annotations"].(map[string]any); ok {
			a := map[string]any{}
			for k, v := range ann {
				if k == LastApplied {
					r.count(loc, "last-applied")
					continue
				}
				if s, ok := v.(string); ok {
					a[k] = r.String(loc, s)
				} else {
					a[k] = v
				}
			}
			clean["annotations"] = a
		}
		out["metadata"] = clean
	}
	info := map[string]any{"captureMode": string(mode), "valuesCollected": false}
	data, _ := obj["data"].(map[string]any)
	strData, _ := obj["stringData"].(map[string]any)
	for range data {
		r.count(loc, "secret-value")
	}
	for range strData {
		r.count(loc, "secret-value")
	}
	if typ, _ := obj["type"].(string); typ == "helm.sh/release.v1" && helm {
		if rel, ok := data["release"].(string); ok {
			if h := helmSummary(rel); h != nil {
				info["helm"] = h
			}
		}
	} else if mode == SecretKeys {
		type kv struct {
			name  string
			bytes int
		}
		var keys []kv
		for k, v := range data {
			s, _ := v.(string)
			n := len(s)
			if dec, err := base64.StdEncoding.DecodeString(s); err == nil {
				n = len(dec)
			}
			keys = append(keys, kv{k, n})
		}
		for k, v := range strData {
			s, _ := v.(string)
			keys = append(keys, kv{k, len(s)})
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i].name < keys[j].name })
		list := make([]any, 0, len(keys))
		for _, k := range keys {
			list = append(list, map[string]any{"name": k.name, "bytes": k.bytes})
		}
		info["keys"] = list
	}
	out["x-flightbay"] = info
	r.dropFields(loc, out)
	return out
}

func helmSummary(k8sValue string) map[string]any {
	helmValue, err := base64.StdEncoding.DecodeString(k8sValue)
	if err != nil {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(string(helmValue))
	if err != nil {
		return nil
	}
	if len(raw) > 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil
		}
		raw, err = io.ReadAll(io.LimitReader(zr, 64<<20))
		if err != nil {
			return nil
		}
	}
	var rel struct {
		Name    string  `json:"name"`
		Version float64 `json:"version"`
		Info    struct {
			Status string `json:"status"`
		} `json:"info"`
		Chart struct {
			Metadata struct {
				Name       string `json:"name"`
				Version    string `json:"version"`
				AppVersion string `json:"appVersion"`
			} `json:"metadata"`
		} `json:"chart"`
	}
	if err := json.Unmarshal(raw, &rel); err != nil {
		return nil
	}
	return map[string]any{
		"release": rel.Name, "revision": rel.Version, "status": rel.Info.Status,
		"chart": rel.Chart.Metadata.Name, "chartVersion": rel.Chart.Metadata.Version, "appVersion": rel.Chart.Metadata.AppVersion,
	}
}

// Report returns counts per location and rule.
func (r *Redactor) Report() Report {
	r.mu.Lock()
	defer r.mu.Unlock()
	locs := make(map[string]map[string]int, len(r.locs))
	for k, v := range r.locs {
		m := make(map[string]int, len(v))
		for kk, vv := range v {
			m[kk] = vv
		}
		locs[k] = m
	}
	return Report{Total: r.total, Locations: locs, Note: "Counts only. Redacted values are never recorded."}
}
