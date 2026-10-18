package ambient

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/tjorourke/flightbay/internal/store"
	"sigs.k8s.io/yaml"
)

func meshTrustDomain(raw string) string {
	if raw == "" {
		return ""
	}
	var mesh struct {
		TrustDomain string `json:"trustDomain"`
	}
	if yaml.Unmarshal([]byte(raw), &mesh) != nil {
		return ""
	}
	return defaultString(mesh.TrustDomain, "cluster.local")
}

// routeField requires an explicit served v1 schema. Missing schemas never imply support.
func routeField(ws *store.Workspace, c string, fields ...string) bool {
	crd := ws.CRD(c, GatewayGroup, "httproutes")
	if crd == nil {
		return false
	}
	for _, v := range L(Path(crd, "spec", "versions")) {
		if S(M(v)["name"]) != "v1" || M(v)["served"] != true {
			continue
		}
		n := M(Path(M(v), "schema", "openAPIV3Schema", "properties", "spec", "properties", "rules", "items"))
		for _, f := range fields {
			if S(n["type"]) == "array" {
				n = M(n["items"])
			}
			n = M(M(n["properties"])[f])
		}
		return len(n) > 0
	}
	return false
}

func waypointPrincipal(ws *store.Workspace, svc *store.Object) string {
	ns, name, wp := Waypoint(ws, svc)
	if wp == nil {
		return ""
	}
	sa, td := "", ""
	for _, pod := range ws.List(svc.Cluster, "", "pods", ns, nil) {
		if pod.Labels()["gateway.networking.k8s.io/gateway-name"] != name {
			continue
		}
		v := defaultString(S(Path(pod.Obj, "spec", "serviceAccountName")), "default")
		if sa != "" && sa != v {
			return ""
		}
		sa = v
	}
	for _, cm := range ws.List(svc.Cluster, "", "configmaps", "", nil) {
		// Mesh config is YAML, parsed rather than inferred from a context name.
		if cm.Name != "istio" && !strings.HasPrefix(cm.Name, "istio-") {
			continue
		}
		v := meshTrustDomain(S(Path(cm.Obj, "data", "mesh")))
		if v != "" {
			if td != "" && td != v {
				return ""
			}
			td = v
		}
	}
	for _, edge := range ws.AccessEdges(svc.Cluster) {
		for _, id := range []string{edge.SrcIdentity, edge.DstIdentity} {
			if !strings.HasPrefix(id, "spiffe://") {
				continue
			}
			v, _, ok := strings.Cut(strings.TrimPrefix(id, "spiffe://"), "/ns/")
			if ok {
				if td != "" && td != v {
					return ""
				}
				td = v
			}
		}
	}
	if sa == "" || td == "" {
		return ""
	}
	return td + "/ns/" + ns + "/sa/" + sa
}

func waypointIdentityEvidence(ws *store.Workspace, svc *store.Object) []Evidence {
	ns, name, _ := Waypoint(ws, svc)
	es := []Evidence{}
	for _, pod := range ws.List(svc.Cluster, "", "pods", ns, nil) {
		if pod.Labels()["gateway.networking.k8s.io/gateway-name"] == name {
			es = append(es, evidence(pod, "spec.serviceAccountName", Path(pod.Obj, "spec", "serviceAccountName")))
		}
	}
	for _, cm := range ws.List(svc.Cluster, "", "configmaps", "", nil) {
		if (cm.Name == "istio" || strings.HasPrefix(cm.Name, "istio-")) && Path(cm.Obj, "data", "mesh") != nil {
			es = append(es, evidence(cm, "data.mesh", Path(cm.Obj, "data", "mesh")))
		}
	}
	return es
}

func generateJWT(req Request, target []any, obj func(string, string, string, map[string]any)) error {
	validURL := func(s string) bool {
		u, e := url.Parse(s)
		return e == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
	}
	if strings.TrimSpace(req.Issuer) == "" || strings.ContainsAny(req.Issuer, "\r\n\t ") || len(req.RequestPrincipals) == 0 {
		return fmt.Errorf("choose an issuer and at least one request principal")
	}
	jwtRule := map[string]any{"issuer": req.Issuer}
	if req.JWKS != "" {
		var keys struct {
			Keys []map[string]any `json:"keys"`
		}
		if req.JWKSURI != "" || json.Unmarshal([]byte(req.JWKS), &keys) != nil || len(keys.Keys) == 0 {
			return fmt.Errorf("provide either a JWKS JSON object with keys or an HTTPS JWKS URI")
		}
		for _, key := range keys.Keys {
			for _, field := range []string{"d", "p", "q", "dp", "dq", "qi", "oth", "k"} {
				if _, ok := key[field]; ok {
					return fmt.Errorf("JWKS must contain public keys only")
				}
			}
		}
		jwtRule["jwks"] = req.JWKS
	} else {
		if !validURL(req.JWKSURI) {
			return fmt.Errorf("choose an HTTPS JWKS URI or inline public JWKS")
		}
		jwtRule["jwksUri"] = req.JWKSURI
	}
	for _, method := range req.Methods {
		if !contains([]string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "CONNECT", "TRACE"}, method) {
			return fmt.Errorf("unsupported HTTP method %q", method)
		}
	}
	for _, path := range req.Paths {
		if path != "*" && !strings.HasPrefix(path, "/") {
			return fmt.Errorf("paths must start with / or be *")
		}
	}
	for _, p := range req.RequestPrincipals {
		if !strings.HasPrefix(p, req.Issuer+"/") {
			return fmt.Errorf("request principals must name the selected issuer and subject, or issuer/*")
		}
	}
	op := map[string]any{"ports": []string{fmt.Sprint(req.Port)}}
	if len(req.Paths) > 0 {
		op["paths"] = req.Paths
	}
	if len(req.Methods) > 0 {
		op["methods"] = req.Methods
	}
	rule := map[string]any{"from": []any{map[string]any{"source": map[string]any{"requestPrincipals": req.RequestPrincipals}}}, "to": []any{map[string]any{"operation": op}}}
	keys := []string{}
	for k := range req.Headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	when := []any{}
	for _, k := range keys {
		if !headerName(k) || req.Headers[k] == "" {
			return fmt.Errorf("choose non-empty HTTP header names and values")
		}
		when = append(when, map[string]any{"key": "request.headers[" + strings.ToLower(k) + "]", "values": []string{req.Headers[k]}})
	}
	if len(when) > 0 {
		rule["when"] = when
	}
	obj("security.istio.io/v1", "RequestAuthentication", "-jwt", map[string]any{"targetRefs": target, "jwtRules": []any{jwtRule}})
	obj("security.istio.io/v1", "AuthorizationPolicy", "-jwt-allow", map[string]any{"targetRefs": target, "action": "ALLOW", "rules": []any{rule}})
	return nil
}

func headerName(k string) bool {
	if k == "" {
		return false
	}
	for _, r := range k {
		if !strings.ContainsRune("!#$%&'*+-.^_`|~0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ", r) {
			return false
		}
	}
	return true
}

func routeOptions(ws *store.Workspace, req Request, selected map[string]bool, rule map[string]any) error {
	if selected["retries"] {
		if req.RetryAttempts < 1 || req.RetryAttempts > 10 {
			return fmt.Errorf("retry attempts must be between 1 and 10")
		}
		rule["retry"] = map[string]any{"attempts": req.RetryAttempts}
	}
	if selected["mirroring"] {
		b := req.Mirror
		if b == nil || b.Name == req.Service || !hasPortOrFalse(ws.Get(req.Cluster, "", "services", req.Namespace, b.Name), b.Port) {
			return fmt.Errorf("choose a different captured mirror Service and its Service port")
		}
		rule["filters"] = []any{map[string]any{"type": "RequestMirror", "requestMirror": map[string]any{"backendRef": map[string]any{"name": b.Name, "port": b.Port}}}}
	}
	if selected["header-routing"] {
		if !selected["weighted-routing"] {
			if len(req.Backends) != 1 || !hasPortOrFalse(ws.Get(req.Cluster, "", "services", req.Namespace, req.Backends[0].Name), req.Backends[0].Port) {
				return fmt.Errorf("choose one captured header-routing backend and its Service port")
			}
			rule["backendRefs"] = []any{map[string]any{"name": req.Backends[0].Name, "port": req.Backends[0].Port}}
		}
		if len(req.Headers) == 0 {
			return fmt.Errorf("choose at least one exact header match")
		}
		keys := []string{}
		for k := range req.Headers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		headers := []any{}
		for _, k := range keys {
			if !headerName(k) || req.Headers[k] == "" {
				return fmt.Errorf("choose non-empty HTTP header names and values")
			}
			headers = append(headers, map[string]any{"type": "Exact", "name": k, "value": req.Headers[k]})
		}
		rule["matches"] = []any{map[string]any{"headers": headers}}
	}
	return nil
}
func hasPortOrFalse(o *store.Object, port int) bool { return o != nil && hasPort(o, port) }
