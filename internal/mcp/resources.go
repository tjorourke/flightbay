package mcp

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Resources give agents the snapshot as addressable context: fixed documents
// (the workspace summary, findings, the Scorecard, coverage, the timeline),
// every finding, every captured proxy or control-plane file and every log
// stream, plus templates for any object, stream or file. Clients can attach
// them to a conversation, and tools link to them.
//
//	flightbay://workspace
//	flightbay://findings                 flightbay://findings/{id}
//	flightbay://scorecard                flightbay://scorecard/{check}
//	flightbay://coverage                 flightbay://timeline
//	flightbay://objects/{cluster}/{group}/{resource}/{namespace}/{name}
//	flightbay://logs/{cluster}/{namespace}/{pod}/{container}/{stream}
//	flightbay://proxies/{cluster}/{kind}/{namespace}/{pod}/{file}
//
// In object URIs the core group is "core" and a cluster-scoped object's
// namespace is "-". Every segment is URL path-escaped.
const scheme = "flightbay://"

// logResourceLines is how many lines a log stream resource returns: the
// newest ones, in order. Larger reads belong in search_logs.
const logResourceLines = 2000

type resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
	Size        int    `json:"size,omitempty"`
}

type resourceTemplate struct {
	URITemplate string `json:"uriTemplate"`
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description"`
	MimeType    string `json:"mimeType,omitempty"`
}

var fixedResources = []resource{
	{URI: scheme + "workspace", Name: "workspace", Title: "Workspace summary", MimeType: "application/json",
		Description: "Case, clusters, capture time, the collection policy (what the customer allowed), import and verification state, and finding counts."},
	{URI: scheme + "findings", Name: "findings", Title: "Findings", MimeType: "application/json",
		Description: "Every analyzer finding with evidence, a log query and a next step."},
	{URI: scheme + "scorecard", Name: "scorecard", Title: "Scorecard", MimeType: "application/json",
		Description: "Security and operational scores per area, with each check's status, affected objects, reason and fix YAML."},
	{URI: scheme + "coverage", Name: "coverage", Title: "Coverage", MimeType: "application/json",
		Description: "What each collection produced, what failed or was excluded and why, the collection log and the redaction report. Read it before concluding anything is absent."},
	{URI: scheme + "timeline", Name: "timeline", Title: "Timeline", MimeType: "application/json",
		Description: "Events, container exits, rollouts and error spikes in time order."},
}

var templates = []resourceTemplate{
	{URITemplate: scheme + "objects/{cluster}/{group}/{resource}/{namespace}/{name}", Name: "object", Title: "Kubernetes object", MimeType: "application/yaml",
		Description: "One object as captured (redacted YAML), followed by its events. group is core for core types; namespace is - for cluster-scoped objects."},
	{URITemplate: scheme + "logs/{cluster}/{namespace}/{pod}/{container}/{stream}", Name: "logs", Title: "Container log stream", MimeType: "text/plain",
		Description: fmt.Sprintf("The newest %d lines of one container log stream (current or previous), oldest first, each with its time and log line id.", logResourceLines)},
	{URITemplate: scheme + "proxies/{cluster}/{kind}/{namespace}/{pod}/{file}", Name: "proxy-file", Title: "Proxy or control-plane file",
		Description: "One captured diagnostic file as captured: an Envoy, ztunnel or agentgateway config dump, istiod debug output or metrics. kind is proxies or control-plane."},
}

// uri joins escaped segments under a resource kind.
func uri(kind string, parts ...string) string {
	esc := make([]string, len(parts))
	for i, p := range parts {
		esc[i] = url.PathEscape(p)
	}
	return scheme + kind + "/" + strings.Join(esc, "/")
}

// ObjectURI is the resource address of an object.
func ObjectURI(cluster, group, resource, namespace, name string) string {
	if group == "" {
		group = "core"
	}
	if namespace == "" {
		namespace = "-"
	}
	return uri("objects", cluster, group, resource, namespace, name)
}

// LogURI is the resource address of a container log stream.
func LogURI(cluster, namespace, pod, container, stream string) string {
	return uri("logs", cluster, namespace, pod, container, stream)
}

// ProxyURI is the resource address of a captured diagnostic file.
func ProxyURI(cluster, kind, namespace, pod, file string) string {
	return uri("proxies", cluster, kind, namespace, pod, file)
}

// parseURI splits a flightbay:// URI into its kind and unescaped segments.
func parseURI(u string) (string, []string, error) {
	if !strings.HasPrefix(u, scheme) {
		return "", nil, fmt.Errorf("not a flightbay resource: %s", u)
	}
	raw := strings.Split(strings.TrimPrefix(u, scheme), "/")
	parts := make([]string, 0, len(raw)-1)
	for _, p := range raw[1:] {
		v, err := url.PathUnescape(p)
		if err != nil {
			return "", nil, fmt.Errorf("bad segment %q in %s", p, u)
		}
		parts = append(parts, v)
	}
	return raw[0], parts, nil
}

type logStream struct{ Cluster, Namespace, Pod, Container, Stream string }

// logStreams lists the captured container log streams from coverage.
func (s *Server) logStreams() ([]logStream, map[logStream]int, error) {
	body, err := s.getRaw("/api/coverage", nil)
	if err != nil {
		return nil, nil, err
	}
	var c struct {
		Coverage []struct {
			Cluster, Type, Namespace, Pod, Container, Stream, State string
			Items                                                   int
		} `json:"coverage"`
	}
	if err := json.Unmarshal(body, &c); err != nil {
		return nil, nil, err
	}
	var out []logStream
	lines := map[logStream]int{}
	for _, x := range c.Coverage {
		if x.Type != "logs" || x.Items == 0 {
			continue
		}
		ls := logStream{x.Cluster, x.Namespace, x.Pod, x.Container, x.Stream}
		if _, seen := lines[ls]; !seen {
			out = append(out, ls)
		}
		lines[ls] += x.Items
	}
	sort.Slice(out, func(i, j int) bool {
		return LogURI(out[i].Cluster, out[i].Namespace, out[i].Pod, out[i].Container, out[i].Stream) < LogURI(out[j].Cluster, out[j].Namespace, out[j].Pod, out[j].Container, out[j].Stream)
	})
	return out, lines, nil
}

type proxyComponent struct {
	Cluster, Kind, Namespace, Pod, Component string
	Files                                    []struct {
		Name  string
		Bytes int
		State string
	}
}

func (s *Server) proxyComponents() ([]proxyComponent, error) {
	body, err := s.getRaw("/api/proxies", nil)
	if err != nil {
		return nil, err
	}
	var p struct {
		Components []proxyComponent `json:"components"`
	}
	return p.Components, json.Unmarshal(body, &p)
}

type finding struct {
	ID, Severity, Title, Detail, Cluster, LogQuery string
	Object                                         *struct{ Cluster, Group, Resource, Kind, Namespace, Name string }
}

func (s *Server) findingList() ([]finding, error) {
	body, err := s.getRaw("/api/findings", nil)
	if err != nil {
		return nil, err
	}
	var fs []finding
	return fs, json.Unmarshal(body, &fs)
}

// allResources is everything resources/list offers, in a stable order.
func (s *Server) allResources() ([]resource, error) {
	out := append([]resource{}, fixedResources...)
	if fs, err := s.findingList(); err == nil {
		for _, f := range fs {
			out = append(out, resource{URI: uri("findings", f.ID), Name: "finding-" + f.ID, Title: f.Title, MimeType: "application/json",
				Description: strings.TrimSpace(f.Severity + " finding in " + f.Cluster + ". " + f.Detail)})
		}
	}
	if body, err := s.getRaw("/api/posture", nil); err == nil {
		var rep struct {
			Checks []map[string]any `json:"checks"`
		}
		if json.Unmarshal(body, &rep) == nil {
			for _, c := range scorecardIndex(rep.Checks) {
				out = append(out, resource{URI: c["uri"].(string), Name: "check-" + c["id"].(string), Title: fmt.Sprint(c["title"]), MimeType: "application/json",
					Description: fmt.Sprintf("Scorecard %v %v check: %v", c["area"], c["status"], or(c["reason"], ""))})
			}
		}
	}
	if cs, err := s.proxyComponents(); err == nil {
		for _, c := range cs {
			for _, f := range c.Files {
				if f.State != "" && f.State != "complete" && f.State != "truncated" {
					continue
				}
				out = append(out, resource{URI: ProxyURI(c.Cluster, c.Kind, c.Namespace, c.Pod, f.Name), Name: c.Pod + "/" + f.Name,
					Title: fmt.Sprintf("%s %s/%s %s", c.Component, c.Namespace, c.Pod, f.Name), MimeType: mimeFor(f.Name), Size: f.Bytes})
			}
		}
	}
	if ls, lines, err := s.logStreams(); err == nil {
		for _, l := range ls {
			out = append(out, resource{URI: LogURI(l.Cluster, l.Namespace, l.Pod, l.Container, l.Stream), Name: l.Pod + "/" + l.Container + "/" + l.Stream,
				Title:       fmt.Sprintf("logs %s/%s %s (%s)", l.Namespace, l.Pod, l.Container, l.Stream),
				Description: fmt.Sprintf("%d lines captured; the newest %d are returned", lines[l], logResourceLines), MimeType: "text/plain"})
		}
	}
	return out, nil
}

func mimeFor(file string) string {
	switch {
	case strings.HasSuffix(file, ".json"):
		return "application/json"
	case strings.HasSuffix(file, ".yaml"):
		return "application/yaml"
	}
	return "text/plain"
}

const resourcePage = 500

// listResources pages through allResources. The cursor is the next offset.
func (s *Server) listResources(cursor string) (map[string]any, error) {
	all, err := s.allResources()
	if err != nil {
		return nil, err
	}
	from, _ := strconv.Atoi(cursor)
	if from < 0 || from > len(all) {
		from = 0
	}
	to := min(len(all), from+resourcePage)
	out := map[string]any{"resources": all[from:to]}
	if to < len(all) {
		out["nextCursor"] = strconv.Itoa(to)
	}
	return out, nil
}

type content struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text"`
}

// readResource returns a resource's text.
func (s *Server) readResource(u string) (content, error) {
	kind, p, err := parseURI(u)
	if err != nil {
		return content{}, err
	}
	need := func(n int) error {
		if len(p) != n {
			return fmt.Errorf("%s needs %d path segments, see resources/templates/list", u, n)
		}
		return nil
	}
	text := func(t, mime string, err error) (content, error) {
		if err != nil {
			return content{}, err
		}
		return content{URI: u, MimeType: mime, Text: clip(t)}, nil
	}
	jsonText := func(t string, err error) (content, error) { return text(t, "application/json", err) }
	switch kind {
	case "workspace":
		return jsonText(s.get("/api/workspace", nil))
	case "coverage":
		return jsonText(s.get("/api/coverage", nil))
	case "timeline":
		return jsonText(s.get("/api/timeline", nil))
	case "findings":
		if len(p) == 0 {
			return jsonText(s.get("/api/findings", nil))
		}
		if err := need(1); err != nil {
			return content{}, err
		}
		body, err := s.getRaw("/api/findings", nil)
		if err != nil {
			return content{}, err
		}
		var fs []map[string]any
		if err := json.Unmarshal(body, &fs); err != nil {
			return content{}, err
		}
		for _, f := range fs {
			if f["id"] == p[0] {
				b, _ := json.MarshalIndent(f, "", " ")
				return text(string(b), "application/json", nil)
			}
		}
		return content{}, fmt.Errorf("no finding %q in this snapshot", p[0])
	case "scorecard":
		body, err := s.getRaw("/api/posture", nil)
		if err != nil {
			return content{}, err
		}
		var rep struct {
			Areas      any              `json:"areas"`
			Priorities any              `json:"priorities,omitempty"`
			Checks     []map[string]any `json:"checks"`
		}
		if err := json.Unmarshal(body, &rep); err != nil {
			return content{}, err
		}
		if len(p) == 0 {
			// The index: the full report runs to hundreds of KB, mostly
			// affected-object lists, so each check is its own resource.
			idx := map[string]any{"areas": rep.Areas, "priorities": rep.Priorities, "checks": scorecardIndex(rep.Checks),
				"note": "Failing and warning checks only. Read a check's uri for its affected objects, why it matters and the fix YAML; best_practices returns every check."}
			b, _ := json.MarshalIndent(idx, "", " ")
			return text(string(b), "application/json", nil)
		}
		if err := need(1); err != nil {
			return content{}, err
		}
		for _, c := range rep.Checks {
			if c["id"] == p[0] {
				b, _ := json.MarshalIndent(c, "", " ")
				return text(string(b), "application/json", nil)
			}
		}
		return content{}, fmt.Errorf("no Scorecard check %q", p[0])
	case "objects":
		if err := need(5); err != nil {
			return content{}, err
		}
		group, ns := p[1], p[3]
		if group == "core" {
			group = ""
		}
		if ns == "-" {
			ns = ""
		}
		body, err := s.getRaw("/api/object", url.Values{"cluster": {p[0]}, "group": {group}, "resource": {p[2]}, "namespace": {ns}, "name": {p[4]}})
		if err != nil {
			return content{}, err
		}
		var o struct {
			YAML   string           `json:"yaml"`
			Events []map[string]any `json:"events"`
		}
		if err := json.Unmarshal(body, &o); err != nil {
			return content{}, err
		}
		t := o.YAML
		if len(o.Events) > 0 {
			t += "---\n# events for this object\n"
			for _, e := range o.Events {
				t += fmt.Sprintf("# %v %v x%v: %v\n", e["type"], e["reason"], or(e["count"], 1), e["message"])
			}
		}
		return text(t, "application/yaml", nil)
	case "logs":
		if err := need(5); err != nil {
			return content{}, err
		}
		body, err := s.getRaw("/api/logs/search", url.Values{"cluster": {p[0]}, "namespace": {p[1]}, "pod": {p[2]}, "container": {p[3]}, "stream": {p[4]}, "limit": {strconv.Itoa(logResourceLines)}})
		if err != nil {
			return content{}, err
		}
		var r struct {
			Total   int `json:"total"`
			Records []struct {
				ID  int64  `json:"id"`
				Seq int    `json:"seq"`
				TS  string `json:"ts"`
				Raw string `json:"raw"`
			} `json:"records"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return content{}, err
		}
		sort.Slice(r.Records, func(i, j int) bool { return r.Records[i].Seq < r.Records[j].Seq })
		var b strings.Builder
		if r.Total > len(r.Records) {
			fmt.Fprintf(&b, "# the newest %d of %d lines; use search_logs to read further back or filter\n", len(r.Records), r.Total)
		}
		for _, l := range r.Records {
			ts := l.TS
			if strings.HasPrefix(ts, "0001") {
				ts = "-"
			}
			fmt.Fprintf(&b, "%s [id %d] %s\n", ts, l.ID, l.Raw)
		}
		return text(b.String(), "text/plain", nil)
	case "proxies":
		if err := need(5); err != nil {
			return content{}, err
		}
		t, err := s.getText("/api/proxy/file", url.Values{"cluster": {p[0]}, "kind": {p[1]}, "namespace": {p[2]}, "pod": {p[3]}, "file": {p[4]}})
		return text(t, mimeFor(p[4]), err)
	}
	return content{}, fmt.Errorf("unknown resource kind %q, see resources/list and resources/templates/list", kind)
}

// scorecardIndex summarises the failing and warning checks, fails first.
func scorecardIndex(checks []map[string]any) []map[string]any {
	out := []map[string]any{}
	for _, want := range []string{"fail", "warn"} {
		for _, c := range checks {
			if c["status"] != want {
				continue
			}
			items, _ := c["items"].([]any)
			id, _ := c["id"].(string)
			out = append(out, map[string]any{"id": id, "uri": uri("scorecard", id), "area": c["area"], "severity": c["severity"], "status": c["status"],
				"title": c["title"], "reason": c["reason"], "objects": len(items)})
		}
	}
	return out
}

func or(v, d any) any {
	if v == nil {
		return d
	}
	return v
}

// complete suggests values for a template argument, using the arguments
// already chosen to narrow them (a namespace narrows pods, a pod containers).
func (s *Server) complete(template, argName, prefix string, ctx map[string]string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	add := func(v string) {
		if v != "" && !seen[v] && strings.HasPrefix(v, prefix) {
			seen[v] = true
			out = append(out, v)
		}
	}
	match := func(k, v string) bool { return ctx[k] == "" || ctx[k] == v }
	switch {
	case strings.Contains(template, "/logs/"):
		ls, _, err := s.logStreams()
		if err != nil {
			return nil, err
		}
		for _, l := range ls {
			if !match("cluster", l.Cluster) || (argName != "namespace" && !match("namespace", l.Namespace)) ||
				(argName != "pod" && argName != "namespace" && !match("pod", l.Pod)) || (argName == "stream" && !match("container", l.Container)) {
				continue
			}
			add(map[string]string{"cluster": l.Cluster, "namespace": l.Namespace, "pod": l.Pod, "container": l.Container, "stream": l.Stream}[argName])
		}
	case strings.Contains(template, "/proxies/"):
		cs, err := s.proxyComponents()
		if err != nil {
			return nil, err
		}
		for _, c := range cs {
			if !match("cluster", c.Cluster) || (argName != "kind" && !match("kind", c.Kind)) || (argName != "namespace" && argName != "kind" && !match("namespace", c.Namespace)) ||
				(argName == "file" && !match("pod", c.Pod)) {
				continue
			}
			if argName == "file" {
				for _, f := range c.Files {
					add(f.Name)
				}
				continue
			}
			add(map[string]string{"cluster": c.Cluster, "kind": c.Kind, "namespace": c.Namespace, "pod": c.Pod}[argName])
		}
	case strings.Contains(template, "/objects/"):
		switch argName {
		case "group", "resource", "cluster":
			body, err := s.getRaw("/api/resources", url.Values{"cluster": {ctx["cluster"]}})
			if err != nil {
				return nil, err
			}
			var rs []struct{ Group, Resource string }
			_ = json.Unmarshal(body, &rs)
			if argName == "cluster" {
				body, err := s.getRaw("/api/workspace", nil)
				if err != nil {
					return nil, err
				}
				var w struct{ Clusters []struct{ Name string } }
				_ = json.Unmarshal(body, &w)
				for _, c := range w.Clusters {
					add(c.Name)
				}
				break
			}
			for _, r := range rs {
				g := r.Group
				if g == "" {
					g = "core"
				}
				if argName == "group" {
					add(g)
				} else if ctx["group"] == "" || ctx["group"] == g {
					add(r.Resource)
				}
			}
		case "namespace", "name":
			group := ctx["group"]
			if group == "core" {
				group = ""
			}
			if ctx["resource"] == "" {
				return nil, nil
			}
			body, err := s.getRaw("/api/objects", url.Values{"cluster": {ctx["cluster"]}, "group": {group}, "resource": {ctx["resource"]}})
			if err != nil {
				return nil, err
			}
			var l struct {
				Rows []struct{ Namespace, Name string } `json:"rows"`
			}
			_ = json.Unmarshal(body, &l)
			for _, r := range l.Rows {
				if argName == "namespace" {
					ns := r.Namespace
					if ns == "" {
						ns = "-"
					}
					add(ns)
				} else if ctx["namespace"] == "" || ctx["namespace"] == r.Namespace || (ctx["namespace"] == "-" && r.Namespace == "") {
					add(r.Name)
				}
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// completePrompt suggests prompt argument values: pods and namespaces from
// the captured log streams, agents from the agent_reach list.
func (s *Server) completePrompt(argName, prefix string, ctx map[string]string) ([]string, error) {
	switch argName {
	case "namespace", "pod", "cluster":
		return s.complete(scheme+"logs/", argName, prefix, ctx)
	case "agent":
		body, err := s.getRaw("/api/reach", nil)
		if err != nil {
			return nil, nil // Enterprise only: no suggestions rather than an error
		}
		var r struct {
			Agents []struct{ Namespace, Name string } `json:"agents"`
		}
		_ = json.Unmarshal(body, &r)
		var out []string
		for _, a := range r.Agents {
			if v := a.Namespace + "/" + a.Name; strings.HasPrefix(v, prefix) {
				out = append(out, v)
			}
		}
		sort.Strings(out)
		return out, nil
	}
	return nil, nil
}

// findingLinks links each finding in a list_findings result to its own
// resource and to the object it is about.
func findingLinks(result string) []resource {
	var fs []finding
	if json.Unmarshal([]byte(result), &fs) != nil {
		return nil
	}
	var out []resource
	seen := map[string]bool{}
	for _, f := range fs {
		if len(out) >= 50 {
			break
		}
		out = append(out, resource{URI: uri("findings", f.ID), Name: "finding-" + f.ID, Title: f.Title, MimeType: "application/json"})
		if o := f.Object; o != nil && o.Name != "" && o.Resource != "" {
			u := ObjectURI(or2(o.Cluster, f.Cluster), o.Group, o.Resource, o.Namespace, o.Name)
			if !seen[u] {
				seen[u] = true
				out = append(out, resource{URI: u, Name: o.Name, Title: strings.TrimSpace(o.Kind + " " + strings.Trim(o.Namespace+"/"+o.Name, "/")), MimeType: "application/yaml"})
			}
		}
	}
	return out
}

// objectLinks links a get_object result to the pod's log streams and to
// its related objects.
func objectLinks(result string) []resource {
	var d struct {
		Object  struct{ Cluster, Group, Resource, Kind, Namespace, Name string }
		Streams []string
		Related []struct{ Relation, Cluster, Group, Resource, Kind, Namespace, Name string }
	}
	if json.Unmarshal([]byte(result), &d) != nil {
		return nil
	}
	o := d.Object
	var out []resource
	for _, st := range d.Streams {
		c, stream, ok := strings.Cut(st, "/")
		if !ok {
			continue
		}
		out = append(out, resource{URI: LogURI(o.Cluster, o.Namespace, o.Name, c, stream), Name: o.Name + "/" + st,
			Title: fmt.Sprintf("logs %s %s (%s)", o.Name, c, stream), MimeType: "text/plain"})
	}
	for _, r := range d.Related {
		if len(out) >= 50 {
			break
		}
		if r.Resource == "" || r.Name == "" {
			continue
		}
		out = append(out, resource{URI: ObjectURI(or2(r.Cluster, o.Cluster), r.Group, r.Resource, r.Namespace, r.Name), Name: r.Name,
			Title: strings.TrimSpace(r.Relation + ": " + r.Kind + " " + strings.Trim(r.Namespace+"/"+r.Name, "/")), MimeType: "application/yaml"})
	}
	return out
}

func or2(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
