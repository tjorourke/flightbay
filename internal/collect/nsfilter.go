package collect

import (
	"regexp"
	"strings"
)

// nsFilter removes everything that names a namespace outside the policy from
// diagnostic output. Proxy and control-plane dumps describe the whole mesh, so
// a waypoint in an allowed namespace would otherwise carry workloads, services
// and certificates from namespaces the customer did not select.
//
// It works bottom-up on decoded JSON: an object whose identity (namespace,
// name, hostname, proxy, uid, identity) points at a disallowed namespace is
// dropped whole; otherwise any entry or array element that mentions one is
// removed and the rest is kept. Mentions are recognised in the forms the
// Solo components use: "namespace" fields, service FQDNs (.ns.svc), SPIFFE
// identities (/ns/<ns>/), ztunnel uids (/Pod/<ns>/), Prometheus labels
// (namespace="ns") and istiod proxy IDs (name.ns).
type nsFilter struct {
	allowed func(string) bool
	removed int
}

const nsName = `([a-z0-9](?:[-a-z0-9]*[a-z0-9])?)`

var nsMentions = []*regexp.Regexp{
	regexp.MustCompile(`\.` + nsName + `\.svc\b`),
	regexp.MustCompile(`/ns/` + nsName + `/`),
	regexp.MustCompile(`/(?:Pod|Service|Workload|WorkloadEntry|ServiceEntry|Gateway)/` + nsName + `/`),
	regexp.MustCompile(`(?:^|[^a-z_])(?:namespace|source_workload_namespace|destination_workload_namespace|destination_service_namespace|source_namespace|destination_namespace|exported_namespace)="` + nsName + `"`),
}

var identityKeys = map[string]bool{
	"namespace": true, "Namespace": true, "name": true, "Name": true, "hostname": true, "proxy": true,
	"uid": true, "identity": true, "service": true, "serviceName": true, "service_name": true, "metadata": true, "Attributes": true,
}

func (f *nsFilter) mentions(s string) bool {
	for _, re := range nsMentions {
		for _, m := range re.FindAllStringSubmatch(s, -1) {
			if !f.allowed(m[1]) {
				return true
			}
		}
	}
	return false
}

// identityDisallowed reports whether a direct scalar of an object places it in
// a disallowed namespace.
func (f *nsFilter) identityDisallowed(obj map[string]any) bool {
	for _, k := range []string{"namespace", "Namespace"} {
		if ns, ok := obj[k].(string); ok && ns != "" && !f.allowed(ns) {
			return true
		}
	}
	if p, ok := obj["proxy"].(string); ok {
		if i := strings.LastIndex(p, "."); i > 0 && !f.allowed(p[i+1:]) {
			return true
		}
	}
	for k, v := range obj {
		if s, ok := v.(string); ok && identityKeys[k] && f.mentions(s) {
			return true
		}
	}
	return false
}

// clean returns the filtered value and whether the caller should drop it.
func (f *nsFilter) clean(v any) (any, bool) {
	switch t := v.(type) {
	case string:
		return t, f.mentions(t)
	case map[string]any:
		if f.identityDisallowed(t) {
			return nil, true
		}
		out := make(map[string]any, len(t))
		for k, val := range t {
			if f.mentions(k) {
				f.removed++
				continue
			}
			o, drop := f.clean(val)
			if drop {
				if identityKeys[k] {
					return nil, true // e.g. metadata: {namespace: other}
				}
				f.removed++
				continue
			}
			out[k] = o
		}
		return out, false
	case []any:
		out := make([]any, 0, len(t))
		for _, el := range t {
			o, drop := f.clean(el)
			if drop {
				f.removed++
				continue
			}
			out = append(out, o)
		}
		return out, false
	default:
		return v, false
	}
}

// Root filters a decoded document. The root itself is never dropped.
func (f *nsFilter) Root(v any) any {
	if m, ok := v.(map[string]any); ok {
		// The root object has no identity of its own; clean its entries.
		out := make(map[string]any, len(m))
		for k, val := range m {
			if f.mentions(k) {
				f.removed++
				continue
			}
			o, drop := f.clean(val)
			if drop {
				f.removed++
				continue
			}
			out[k] = o
		}
		return out
	}
	o, drop := f.clean(v)
	if drop {
		f.removed++
		return nil
	}
	return o
}

// Lines filters line-oriented output such as Prometheus text.
func (f *nsFilter) Lines(text string) string {
	var b strings.Builder
	for _, line := range strings.SplitAfter(text, "\n") {
		if line == "" {
			continue
		}
		if f.mentions(line) {
			f.removed++
			continue
		}
		b.WriteString(line)
	}
	return b.String()
}

// nsKeyed filters documents shaped {key: {namespace: value}}, such as istiod's
// endpointz, where the namespace is only present as a map key. An outer entry
// left with no allowed namespace is removed.
func (f *nsFilter) nsKeyed(doc any) any {
	m, ok := doc.(map[string]any)
	if !ok {
		return doc
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		inner, ok := v.(map[string]any)
		if !ok {
			out[k] = v
			continue
		}
		kept := make(map[string]any, len(inner))
		for ns, x := range inner {
			if f.allowed(ns) {
				kept[ns] = x
			} else {
				f.removed++
			}
		}
		if len(kept) > 0 {
			out[k] = kept
		}
	}
	return out
}
