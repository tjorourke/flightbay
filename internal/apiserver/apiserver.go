// Package apiserver serves a workspace as a read-only Kubernetes API, so
// kubectl, k9s and stern work against a snapshot as if it were a cluster.
// Every cluster in the bundle is served under /clusters/<name>.
package apiserver

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/store"
)

// Server is the fake kube-apiserver.
type Server struct {
	WS    *store.Workspace
	Token string
}

// SnapshotTime is the moment the capture describes.
func SnapshotTime(ws *store.Workspace) time.Time {
	if m := ws.Manifest(); m != nil && !m.CaptureEnd.IsZero() {
		return m.CaptureEnd
	}
	if h := ws.Header(); h != nil && !h.CaptureStart.IsZero() {
		return h.CaptureStart
	}
	return time.Now().UTC()
}

// Kubeconfig returns a kubeconfig with one "<cluster>@snapshot" context per
// cluster. caPEM is the session CA for an https baseURL.
func Kubeconfig(baseURL, token string, clusters []string, caPEM []byte) ([]byte, error) {
	c := clientcmdapi.NewConfig()
	c.AuthInfos["flightbay"] = &clientcmdapi.AuthInfo{Token: token}
	for i, cl := range clusters {
		name := cl + "@snapshot"
		c.Clusters[name] = &clientcmdapi.Cluster{Server: strings.TrimRight(baseURL, "/") + "/clusters/" + cl, CertificateAuthorityData: caPEM}
		c.Contexts[name] = &clientcmdapi.Context{Cluster: name, AuthInfo: "flightbay"}
		if i == 0 {
			c.CurrentContext = name
		}
	}
	return clientcmd.Write(*c)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func status(w http.ResponseWriter, code int, reason metav1.StatusReason, msg string) {
	writeJSON(w, code, metav1.Status{TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"}, Status: metav1.StatusFailure, Code: int32(code), Reason: reason, Message: msg})
}

// Handler returns the HTTP handler. FLIGHTBAY_DEBUG_HTTP=1 logs each request
// to stderr, which is how client compatibility problems get diagnosed.
func (s *Server) Handler() http.Handler {
	h := s.handler()
	if os.Getenv("FLIGHTBAY_DEBUG_HTTP") == "" {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, code: 200}
		start := time.Now()
		h.ServeHTTP(rec, r)
		fmt.Fprintf(os.Stderr, "kube-api %s %s accept=%q ua=%q -> %d (%s)\n", r.Method, r.URL.RequestURI(), r.Header.Get("Accept"), r.UserAgent(), rec.code, time.Since(start).Round(time.Millisecond))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(c int) { r.code = c; r.ResponseWriter.WriteHeader(c) }
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Flightbay-Snapshot", SnapshotTime(s.WS).Format(time.RFC3339))
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if s.Token != "" && subtle.ConstantTimeCompare([]byte(got), []byte(s.Token)) != 1 {
			status(w, http.StatusUnauthorized, metav1.StatusReasonUnauthorized, "flightbay: missing or invalid token for this snapshot")
			return
		}
		segs := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(segs) < 2 || segs[0] != "clusters" {
			status(w, http.StatusNotFound, metav1.StatusReasonNotFound, "flightbay: use /clusters/<name> (see the generated kubeconfig)")
			return
		}
		cluster := segs[1]
		known := false
		for _, c := range s.WS.Clusters() {
			known = known || c == cluster
		}
		if !known {
			status(w, http.StatusNotFound, metav1.StatusReasonNotFound, "flightbay: no cluster "+cluster+" in this bundle")
			return
		}
		s.route(w, r, cluster, segs[2:])
	})
}

func (s *Server) readOnly(w http.ResponseWriter) {
	status(w, http.StatusMethodNotAllowed, metav1.StatusReasonMethodNotAllowed,
		fmt.Sprintf("flightbay: this is a read-only snapshot taken %s", SnapshotTime(s.WS).Format(time.RFC3339)))
}

func (s *Server) route(w http.ResponseWriter, r *http.Request, cluster string, p []string) {
	if len(p) == 0 {
		writeJSON(w, 200, map[string]any{"paths": []string{"/api", "/apis", "/version"}})
		return
	}
	switch p[0] {
	case "version":
		v := s.WS.Version(cluster)
		if len(v) == 0 {
			v = json.RawMessage(`{"major":"1","minor":"0","gitVersion":"v0.0.0-unknown"}`)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(v)
		return
	case "healthz", "livez", "readyz":
		_, _ = w.Write([]byte("ok"))
		return
	case "openapi":
		status(w, http.StatusNotFound, metav1.StatusReasonNotFound, "flightbay: OpenAPI is not served for snapshots")
		return
	}
	var group, version string
	var rest []string
	switch {
	case p[0] == "api" && len(p) == 1:
		writeJSON(w, 200, metav1.APIVersions{TypeMeta: metav1.TypeMeta{Kind: "APIVersions"}, Versions: []string{"v1"},
			ServerAddressByClientCIDRs: []metav1.ServerAddressByClientCIDR{{ClientCIDR: "0.0.0.0/0", ServerAddress: r.Host}}})
		return
	case p[0] == "api":
		version, rest = p[1], p[2:]
	case p[0] == "apis" && len(p) == 1:
		writeJSON(w, 200, s.groups(cluster))
		return
	case p[0] == "apis" && len(p) == 2:
		for _, g := range s.groups(cluster).Groups {
			if g.Name == p[1] {
				writeJSON(w, 200, g)
				return
			}
		}
		status(w, 404, metav1.StatusReasonNotFound, "group not found")
		return
	case p[0] == "apis":
		group, version, rest = p[1], p[2], p[3:]
	default:
		status(w, 404, metav1.StatusReasonNotFound, "not found")
		return
	}
	if group == authGroup && version == "v1" {
		s.accessReview(w, r, rest)
		return
	}
	if len(rest) == 0 {
		writeJSON(w, 200, s.resourceList(cluster, group, version))
		return
	}
	ns := ""
	if rest[0] == "namespaces" && len(rest) >= 3 && group == "" {
		ns, rest = rest[1], rest[2:]
	} else if rest[0] == "namespaces" && len(rest) >= 3 {
		ns, rest = rest[1], rest[2:]
	}
	res, ok := s.resource(cluster, group, version, rest[0])
	if !ok {
		status(w, 404, metav1.StatusReasonNotFound, fmt.Sprintf("the server could not find the requested resource (%s)", rest[0]))
		return
	}
	if len(rest) >= 3 {
		sub := rest[2]
		if r.Method == http.MethodGet && res.Resource == "pods" && group == "" && sub == "log" {
			s.logs(w, r, cluster, ns, rest[1])
			return
		}
		s.readOnly(w)
		return
	}
	if r.Method != http.MethodGet {
		s.readOnly(w)
		return
	}
	if len(rest) == 2 {
		s.get(w, r, cluster, res, ns, rest[1])
		return
	}
	s.list(w, r, cluster, res, ns)
}

type virtualKind int

const (
	real virtualKind = iota
	virtualEvents
	virtualNamespaces
	virtualCRDs
)

type served struct {
	bundle.APIResource
	virtual virtualKind
}

// resources returns what discovery advertises, including events and, when not
// collected, namespaces synthesised from the captured objects.
func (s *Server) resources(cluster string) []served {
	var out []served
	haveNS, haveCRDs := false, false
	for _, r := range s.WS.Resources(cluster) {
		if r.Group == "" && r.Resource == "namespaces" {
			haveNS = true
		}
		if r.Group == "apiextensions.k8s.io" && r.Resource == "customresourcedefinitions" {
			haveCRDs = true
		}
		if r.Group == "" && r.Resource == "events" {
			continue
		}
		if r.SingularName == "" {
			r.SingularName = strings.ToLower(r.Kind)
		}
		out = append(out, served{APIResource: r})
	}
	out = append(out, served{APIResource: bundle.APIResource{Version: "v1", Resource: "events", Kind: "Event", Namespaced: true, SingularName: "event", ShortNames: []string{"ev"}}, virtual: virtualEvents})
	if !haveNS {
		out = append(out, served{APIResource: bundle.APIResource{Version: "v1", Resource: "namespaces", Kind: "Namespace", SingularName: "namespace", ShortNames: []string{"ns"}}, virtual: virtualNamespaces})
	}
	if !haveCRDs {
		out = append(out, served{APIResource: bundle.APIResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions", Kind: "CustomResourceDefinition",
			SingularName: "customresourcedefinition", ShortNames: []string{"crd", "crds"}}, virtual: virtualCRDs})
	}
	return out
}

func (s *Server) resource(cluster, group, version, name string) (served, bool) {
	for _, r := range s.resources(cluster) {
		if r.Group == group && r.Version == version && r.Resource == name {
			return r, true
		}
	}
	return served{}, false
}

func (s *Server) groups(cluster string) metav1.APIGroupList {
	versions := map[string]map[string]bool{}
	for _, r := range s.resources(cluster) {
		if r.Group == "" {
			continue
		}
		if versions[r.Group] == nil {
			versions[r.Group] = map[string]bool{}
		}
		versions[r.Group][r.Version] = true
	}
	versions[authGroup] = map[string]bool{"v1": true}
	names := make([]string, 0, len(versions))
	for g := range versions {
		names = append(names, g)
	}
	sort.Strings(names)
	list := metav1.APIGroupList{TypeMeta: metav1.TypeMeta{Kind: "APIGroupList", APIVersion: "v1"}}
	for _, g := range names {
		grp := metav1.APIGroup{Name: g}
		for v := range versions[g] {
			grp.Versions = append(grp.Versions, metav1.GroupVersionForDiscovery{GroupVersion: g + "/" + v, Version: v})
		}
		sort.Slice(grp.Versions, func(i, j int) bool { return grp.Versions[i].Version > grp.Versions[j].Version })
		grp.PreferredVersion = grp.Versions[0]
		list.Groups = append(list.Groups, grp)
	}
	return list
}

func (s *Server) resourceList(cluster, group, version string) metav1.APIResourceList {
	gv := version
	if group != "" {
		gv = group + "/" + version
	}
	l := metav1.APIResourceList{TypeMeta: metav1.TypeMeta{Kind: "APIResourceList", APIVersion: "v1"}, GroupVersion: gv}
	if group == authGroup && version == "v1" {
		for _, n := range []string{"selfsubjectaccessreviews", "selfsubjectrulesreviews"} {
			l.APIResources = append(l.APIResources, metav1.APIResource{Name: n, Kind: map[string]string{
				"selfsubjectaccessreviews": "SelfSubjectAccessReview", "selfsubjectrulesreviews": "SelfSubjectRulesReview"}[n], Verbs: []string{"create"}})
		}
		return l
	}
	for _, r := range s.resources(cluster) {
		if r.Group != group || r.Version != version {
			continue
		}
		l.APIResources = append(l.APIResources, metav1.APIResource{Name: r.Resource, SingularName: r.SingularName, Namespaced: r.Namespaced,
			Kind: r.Kind, Verbs: []string{"get", "list", "watch"}, ShortNames: r.ShortNames, Categories: r.Categories})
		if r.Group == "" && r.Resource == "pods" {
			l.APIResources = append(l.APIResources, metav1.APIResource{Name: "pods/log", Namespaced: true, Kind: "Pod", Verbs: []string{"get"}})
		}
	}
	return l
}

type item struct {
	rv  int64
	ns  string
	obj map[string]any
}

func (s *Server) items(cluster string, r served, ns string) []item {
	var out []item
	switch r.virtual {
	case virtualEvents:
		for i, e := range s.WS.Events(cluster) {
			md, _ := e.Obj["metadata"].(map[string]any)
			n, _ := md["namespace"].(string)
			if ns != "" && n != ns {
				continue
			}
			if e.Obj["apiVersion"] == nil {
				e.Obj["apiVersion"], e.Obj["kind"] = "v1", "Event"
			}
			out = append(out, item{rv: int64(i + 1), ns: n, obj: e.Obj})
		}
	case virtualNamespaces:
		seen := map[string]bool{}
		for _, o := range s.WS.AllObjects(cluster) {
			if o.Namespace != "" {
				seen[o.Namespace] = true
			}
		}
		names := make([]string, 0, len(seen))
		for n := range seen {
			names = append(names, n)
		}
		sort.Strings(names)
		for i, n := range names {
			out = append(out, item{rv: int64(i + 1), obj: map[string]any{"apiVersion": "v1", "kind": "Namespace",
				"metadata": map[string]any{"name": n, "annotations": map[string]any{"flightbay.dev/synthesised": "true"}}, "status": map[string]any{"phase": "Active"}}})
		}
	case virtualCRDs:
		for i, c := range s.WS.CRDs(cluster) {
			out = append(out, item{rv: int64(i + 1), obj: c})
		}
	default:
		for _, o := range s.WS.List(cluster, r.Group, r.Resource, ns, nil) {
			obj := o.Obj
			if obj["apiVersion"] == nil {
				obj["apiVersion"], obj["kind"] = r.GroupVersion(), r.Kind
			}
			out = append(out, item{rv: o.RV, ns: o.Namespace, obj: obj})
		}
	}
	return out
}

func lookup(obj map[string]any, path string) string {
	var cur any = obj
	for _, p := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = m[p]
	}
	switch v := cur.(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func objLabels(obj map[string]any) labels.Set {
	out := labels.Set{}
	md, _ := obj["metadata"].(map[string]any)
	l, _ := md["labels"].(map[string]any)
	for k, v := range l {
		out[k], _ = v.(string)
	}
	return out
}

func filter(r *http.Request, in []item) ([]item, error) {
	q := r.URL.Query()
	ls, err := labels.Parse(q.Get("labelSelector"))
	if err != nil {
		return nil, err
	}
	fsel, err := fields.ParseSelector(q.Get("fieldSelector"))
	if err != nil {
		return nil, err
	}
	var out []item
	for _, it := range in {
		if !ls.Matches(objLabels(it.obj)) {
			continue
		}
		ok := true
		for _, req := range fsel.Requirements() {
			v := lookup(it.obj, req.Field)
			switch req.Operator {
			case "!=":
				ok = ok && v != req.Value
			default:
				ok = ok && v == req.Value
			}
		}
		if ok {
			out = append(out, it)
		}
	}
	return out, nil
}

// availability decides whether a collection can be served as a complete list.
func (s *Server) availability(w http.ResponseWriter, cluster string, r served) bool {
	var st store.Collection
	switch r.virtual {
	case virtualNamespaces, virtualCRDs:
		// Synthesised or partial by nature: only the selected types' CRDs are read.
		return true
	case virtualEvents:
		st = store.Collection{State: store.StateNotCollected}
		for _, c := range s.WS.Coverage() {
			if c.Type == bundle.CoverageEvents && c.Cluster == cluster {
				st = store.Collection{State: c.State, Reason: c.Reason}
			}
		}
		if st.State == store.StateNotCollected {
			if pol := s.WS.Policy(); pol != nil && !pol.Plan().Selected("events") {
				st = store.Collection{State: store.StateExcluded}
			} else if ph := s.WS.State().Phase; ph == store.PhaseLoading || ph == store.PhaseWaiting {
				st = store.Collection{State: store.StateLoading}
			} else {
				st = store.Collection{State: store.StateComplete}
			}
		}
	default:
		st = s.WS.CollectionState(cluster, r.Group, r.Resource, r.Kind, r.Namespaced)
	}
	name := r.Name()
	switch st.State {
	case store.StateComplete:
		return true
	case store.StateTruncated:
		w.Header().Add("Warning", fmt.Sprintf(`299 - "flightbay: %s on %s is truncated: %s"`, name, cluster, st.Reason))
		return true
	case store.StateLoading:
		w.Header().Set("Retry-After", "1")
		status(w, http.StatusServiceUnavailable, metav1.StatusReasonServiceUnavailable, fmt.Sprintf("flightbay: %s on %s is still loading from the bundle; retry shortly", name, cluster))
	case store.StateExcluded:
		status(w, http.StatusForbidden, metav1.StatusReasonForbidden, fmt.Sprintf("flightbay: %s on %s was excluded by the customer's collection policy", name, cluster))
	case store.StateFailed:
		status(w, http.StatusServiceUnavailable, metav1.StatusReasonServiceUnavailable, fmt.Sprintf("flightbay: %s on %s was not collected: %s", name, cluster, st.Reason))
	default:
		status(w, http.StatusNotFound, metav1.StatusReasonNotFound, fmt.Sprintf("flightbay: %s on %s is not in this bundle", name, cluster))
	}
	return false
}

func wantsTable(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "as=Table")
}

func (s *Server) list(w http.ResponseWriter, r *http.Request, cluster string, res served, ns string) {
	if !s.availability(w, cluster, res) {
		return
	}
	q := r.URL.Query()
	if q.Get("watch") == "true" || q.Get("watch") == "1" {
		s.watch(w, r, cluster, res, ns)
		return
	}
	all, err := filter(r, s.items(cluster, res, ns))
	if err != nil {
		status(w, 400, metav1.StatusReasonBadRequest, err.Error())
		return
	}
	offset := 0
	if c := q.Get("continue"); c != "" {
		b, err := base64.RawURLEncoding.DecodeString(c)
		if err == nil {
			offset, _ = strconv.Atoi(string(b))
		}
	}
	if offset > len(all) {
		offset = len(all)
	}
	page := all[offset:]
	cont := ""
	if limit, _ := strconv.Atoi(q.Get("limit")); limit > 0 && len(page) > limit {
		page = page[:limit]
		cont = base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset + limit)))
	}
	rv := strconv.FormatInt(s.WS.Revision(), 10)
	if wantsTable(r) {
		tbl := s.table(cluster, res, page, q.Get("includeObject"))
		tbl.ResourceVersion, tbl.Continue = rv, cont
		writeJSON(w, 200, tbl)
		return
	}
	objs := make([]map[string]any, 0, len(page))
	for _, it := range page {
		objs = append(objs, withRV(it))
	}
	writeJSON(w, 200, map[string]any{"apiVersion": res.GroupVersion(), "kind": res.Kind + "List",
		"metadata": map[string]any{"resourceVersion": rv, "continue": cont}, "items": objs})
}

func (s *Server) get(w http.ResponseWriter, r *http.Request, cluster string, res served, ns, name string) {
	for _, it := range s.items(cluster, res, ns) {
		if lookup(it.obj, "metadata.name") == name {
			if wantsTable(r) {
				writeJSON(w, 200, s.table(cluster, res, []item{it}, r.URL.Query().Get("includeObject")))
				return
			}
			writeJSON(w, 200, withRV(it))
			return
		}
	}
	st := s.WS.CollectionState(cluster, res.Group, res.Resource, res.Kind, res.Namespaced)
	if res.virtual == real && st.State != store.StateComplete && st.State != store.StateTruncated {
		s.availability(w, cluster, res)
		return
	}
	gr := res.Resource
	if res.Group != "" {
		gr += "." + res.Group
	}
	status(w, 404, metav1.StatusReasonNotFound, fmt.Sprintf("%s %q not found", gr, name))
}

func (s *Server) watch(w http.ResponseWriter, r *http.Request, cluster string, res served, ns string) {
	fl, ok := w.(http.Flusher)
	if !ok {
		status(w, 500, metav1.StatusReasonInternalError, "streaming unsupported")
		return
	}
	q := r.URL.Query()
	since, _ := strconv.ParseInt(q.Get("resourceVersion"), 10, 64)
	// WatchList (sendInitialEvents=true) is how current informers start: the
	// whole initial state as ADDED events, then a bookmark marking its end.
	// Without that bookmark the reflector never syncs and k9s shows nothing.
	initialEvents := q.Get("sendInitialEvents") == "true"
	if initialEvents || (since >= s.WS.Revision() && res.virtual != real) {
		since = 0
	}
	bookmarks := q.Get("allowWatchBookmarks") == "true"
	ch, cancel := s.WS.Subscribe()
	defer cancel()
	timeout := 30 * time.Minute
	if ts, err := strconv.Atoi(q.Get("timeoutSeconds")); err == nil && ts > 0 {
		timeout = time.Duration(ts) * time.Second
	}
	deadline := time.After(timeout)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	fl.Flush()
	enc := json.NewEncoder(w)
	sent := map[string]bool{}
	first := true
	for {
		rev := s.WS.Revision()
		items, _ := filter(r, s.items(cluster, res, ns))
		for _, it := range items {
			key := it.ns + "/" + lookup(it.obj, "metadata.name")
			if sent[key] || (res.virtual == real && it.rv <= since) {
				continue
			}
			sent[key] = true
			var obj any = withRV(it)
			if wantsTable(r) {
				obj = s.table(cluster, res, []item{it}, q.Get("includeObject"))
			}
			if err := enc.Encode(map[string]any{"type": "ADDED", "object": obj}); err != nil {
				return
			}
		}
		if first && initialEvents && bookmarks {
			bm := map[string]any{"apiVersion": res.GroupVersion(), "kind": res.Kind, "metadata": map[string]any{
				"resourceVersion": strconv.FormatInt(rev, 10), "annotations": map[string]any{"k8s.io/initial-events-end": "true"}}}
			if err := enc.Encode(map[string]any{"type": "BOOKMARK", "object": bm}); err != nil {
				return
			}
		}
		first = false
		fl.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-deadline:
			return
		case <-ch:
		}
	}
}

// withRV returns the object with metadata.resourceVersion set, without
// mutating the shared stored copy. Reflectors resume watches from it.
func withRV(it item) map[string]any {
	md, _ := it.obj["metadata"].(map[string]any)
	if md == nil || md["resourceVersion"] != nil && it.rv == 0 {
		return it.obj
	}
	out := make(map[string]any, len(it.obj))
	for k, v := range it.obj {
		out[k] = v
	}
	m2 := make(map[string]any, len(md)+1)
	for k, v := range md {
		m2[k] = v
	}
	m2["resourceVersion"] = strconv.FormatInt(it.rv, 10)
	out["metadata"] = m2
	return out
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request, cluster, ns, pod string) {
	o := s.WS.Get(cluster, "", "pods", ns, pod)
	q := r.URL.Query()
	// Subscribe before reading, so a chunk committed between the first read
	// and the subscription still wakes a follower.
	var updates <-chan struct{}
	if q.Get("follow") == "true" {
		ch, cancel := s.WS.Subscribe()
		defer cancel()
		updates = ch
	}
	container := q.Get("container")
	if container == "" {
		if o == nil {
			status(w, 404, metav1.StatusReasonNotFound, fmt.Sprintf("pods %q not found", pod))
			return
		}
		spec, _ := o.Obj["spec"].(map[string]any)
		cs, _ := spec["containers"].([]any)
		var names []string
		for _, c := range cs {
			m, _ := c.(map[string]any)
			n, _ := m["name"].(string)
			names = append(names, n)
		}
		if len(names) != 1 {
			status(w, 400, metav1.StatusReasonBadRequest, fmt.Sprintf("a container name must be specified for pod %s, choose one of: [%s]", pod, strings.Join(names, " ")))
			return
		}
		container = names[0]
	}
	stream := "current"
	if q.Get("previous") == "true" {
		stream = "previous"
	}
	var cov *bundle.Coverage
	for _, c := range s.WS.Coverage() {
		if c.Type == bundle.CoverageLogs && c.Cluster == cluster && c.Namespace == ns && c.Pod == pod && c.Container == container && c.Stream == stream {
			c := c
			cov = &c
		}
	}
	recs := s.WS.LogStream(cluster, ns, pod, container, stream)
	if len(recs) == 0 && cov != nil && cov.State == bundle.StateFailed {
		status(w, 400, metav1.StatusReasonBadRequest, "flightbay: this log stream was not collected: "+cov.Reason)
		return
	}
	if len(recs) == 0 && cov == nil && o == nil {
		status(w, 404, metav1.StatusReasonNotFound, fmt.Sprintf("pods %q not found", pod))
		return
	}
	if cov != nil && cov.State == bundle.StateTruncated {
		w.Header().Add("Warning", `299 - "flightbay: `+cov.Reason+`"`)
	}
	timestamps := q.Get("timestamps") == "true"
	var sinceT time.Time
	if st := q.Get("sinceTime"); st != "" {
		sinceT, _ = time.Parse(time.RFC3339, st)
	}
	if ss, err := strconv.Atoi(q.Get("sinceSeconds")); err == nil && ss > 0 {
		sinceT = SnapshotTime(s.WS).Add(-time.Duration(ss) * time.Second)
	}
	var sel []int
	for i, rec := range recs {
		if !sinceT.IsZero() && rec.TS.Before(sinceT) {
			continue
		}
		sel = append(sel, i)
	}
	if tl, err := strconv.Atoi(q.Get("tailLines")); err == nil && tl >= 0 && tl < len(sel) {
		sel = sel[len(sel)-tl:]
	}
	w.Header().Set("Content-Type", "text/plain")
	line := func(i int) string {
		rec := recs[i]
		if timestamps {
			return rec.TS.Format(time.RFC3339Nano) + " " + rec.Raw + "\n"
		}
		return rec.Raw + "\n"
	}
	var b strings.Builder
	for _, i := range sel {
		b.WriteString(line(i))
	}
	_, _ = w.Write([]byte(b.String()))
	if q.Get("follow") != "true" {
		return
	}
	fl, _ := w.(http.Flusher)
	if fl != nil {
		fl.Flush()
	}
	sent := len(recs)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-updates:
		case <-tick.C:
		}
		now := s.WS.LogStreamFrom(cluster, ns, pod, container, stream, sent)
		for _, rec := range now {
			out := rec.Raw + "\n"
			if timestamps {
				out = rec.TS.Format(time.RFC3339Nano) + " " + out
			}
			if _, err := w.Write([]byte(out)); err != nil {
				return
			}
		}
		sent += len(now)
		if fl != nil {
			fl.Flush()
		}
	}
}

func (s *Server) table(cluster string, res served, items []item, include string) metav1.Table {
	ref := SnapshotTime(s.WS)
	cols, cells := columns(res.APIResource, s.WS.CRD(cluster, res.Group, res.Resource), ref)
	tbl := metav1.Table{TypeMeta: metav1.TypeMeta{Kind: "Table", APIVersion: "meta.k8s.io/v1"}, ColumnDefinitions: cols}
	for _, it := range items {
		var raw []byte
		if include == "Object" {
			raw, _ = json.Marshal(it.obj)
		} else {
			md, _ := it.obj["metadata"].(map[string]any)
			raw, _ = json.Marshal(map[string]any{"apiVersion": "meta.k8s.io/v1", "kind": "PartialObjectMetadata", "metadata": md})
		}
		tbl.Rows = append(tbl.Rows, metav1.TableRow{Cells: cells(it.obj), Object: runtime.RawExtension{Raw: raw}})
	}
	return tbl
}
