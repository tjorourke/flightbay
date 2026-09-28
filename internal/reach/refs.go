package reach

import (
	"regexp"
	"strings"
)

// ---------- objects behind the words ----------
//
// DeclaredIn and Via are written for people ("EnterpriseAgentgatewayBackend
// agentgateway-system/x through agentgateway-system/gw", "env LLM_URL
// (ConfigMap agent-config)"). configRefs finds the objects those words name,
// so the UI can open each one. Only objects that exist in the capture are
// returned, so a link never leads nowhere.

var (
	kindRefRe  = regexp.MustCompile(`\b([A-Z][A-Za-z]+) ([a-z0-9][-a-z0-9.]*(?:/[a-z0-9][-a-z0-9.]*)?)`)
	routeRefRe = regexp.MustCompile(`\broute ([a-z0-9][-a-z0-9.]*/[a-z0-9][-a-z0-9.]*)`)
	gwRefRe    = regexp.MustCompile(`\b(?:through|gateway) ([a-z0-9][-a-z0-9.]*/[a-z0-9][-a-z0-9.]*)`)
	envRefRe   = regexp.MustCompile(`^env ([A-Za-z_][A-Za-z0-9_]*)`)
	toolRefRe  = regexp.MustCompile(`^kagent tool `)
	imageRefRe = regexp.MustCompile(`^image`)
)

// configRefs resolves the phrases to objects, first mention first, no repeats.
func (c *cluster) configRefs(w *workload, phrases ...string) []Ref {
	kinds := map[string][2]string{} // Kind -> group, resource
	for _, r := range c.ws.Resources(c.name) {
		if _, ok := kinds[r.Kind]; !ok {
			kinds[r.Kind] = [2]string{r.Group, r.Resource}
		}
	}
	var out []Ref
	seen := map[string]bool{}
	add := func(group, resource, ns, name, label string) bool {
		o := c.ws.Get(c.name, group, resource, ns, name)
		if o == nil {
			o = c.ws.Get(c.name, group, resource, "", name) // cluster-scoped
		}
		if o == nil {
			return false
		}
		r := refOf(o)
		k := r.Group + "/" + r.Resource + "/" + r.Namespace + "/" + r.Name
		if !seen[k] {
			seen[k] = true
			r.Label = label
			out = append(out, *r)
		}
		return true
	}
	split := func(s string) (string, string) {
		if i := strings.Index(s, "/"); i > 0 {
			return s[:i], s[i+1:]
		}
		return w.ns, s
	}
	for _, p := range phrases {
		if m := envRefRe.FindStringSubmatch(p); m != nil && w.obj != nil {
			add(w.obj.Group, w.obj.Resource, w.ns, w.name, w.kind+" "+w.ns+"/"+w.name+" (env "+m[1]+")")
		}
		if imageRefRe.MatchString(p) && w.obj != nil {
			add(w.obj.Group, w.obj.Resource, w.ns, w.name, w.kind+" "+w.ns+"/"+w.name+" (image)")
		}
		if toolRefRe.MatchString(p) {
			if !add("kagent.dev", "agents", w.ns, w.name, "Agent "+w.ns+"/"+w.name+" (tools)") && w.obj != nil {
				add(w.obj.Group, w.obj.Resource, w.ns, w.name, w.kind+" "+w.ns+"/"+w.name)
			}
		}
		for _, m := range kindRefRe.FindAllStringSubmatch(p, -1) {
			gr, ok := kinds[m[1]]
			if !ok {
				continue
			}
			ns, name := split(m[2])
			add(gr[0], gr[1], ns, name, m[1]+" "+ns+"/"+name)
		}
		for _, m := range routeRefRe.FindAllStringSubmatch(p, -1) {
			ns, name := split(m[1])
			if !add("gateway.networking.k8s.io", "httproutes", ns, name, "HTTPRoute "+ns+"/"+name) {
				add("gateway.networking.k8s.io", "grpcroutes", ns, name, "GRPCRoute "+ns+"/"+name)
			}
		}
		for _, m := range gwRefRe.FindAllStringSubmatch(p, -1) {
			ns, name := split(m[1])
			if !add("gateway.networking.k8s.io", "gateways", ns, name, "Gateway "+ns+"/"+name) {
				add("", "services", ns, name, "Service "+ns+"/"+name)
			}
		}
	}
	return out
}
