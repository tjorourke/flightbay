package ambient

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/tjorourke/flightbay/internal/store"
)

// Helpers for the Policies & routes inventory: status conditions, the
// agentgateway classification, spec normalisation for drift, and the field
// diff behind the Diff button.

// ---- conditions ---------------------------------------------------------------

// condStatus returns the first condition of type t: its status ("True",
// "False", "Unknown"), reason and message. Status is "" when absent.
func condStatus(conds []any, t string) (status, reason, message string) {
	for _, c := range conds {
		if m := M(c); S(m["type"]) == t {
			return S(m["status"]), S(m["reason"]), S(m["message"])
		}
	}
	return "", "", ""
}

// condSummary renders the listed condition types as "Type=Status" across
// several condition lists (one per parent or ancestor), each distinct pair
// once, in the order given. Empty when none of them is reported.
func condSummary(groups [][]any, types ...string) string {
	var out []string
	for _, t := range types {
		for _, g := range groups {
			if st, _, _ := condStatus(g, t); st != "" {
				p := t + "=" + st
				if !contains(out, p) {
					out = append(out, p)
				}
			}
		}
	}
	return strings.Join(out, ", ")
}

// ---- agentgateway classification ---------------------------------------------

type agwFeature struct {
	rank   int
	typ    string
	action string
	phrase string
}

// agentgatewayClassify reads spec.traffic and spec.backend of an
// (Enterprise)AgentgatewayPolicy. Every key adds a type; a key with no known
// meaning is Other, never Routing.
func agentgatewayClassify(spec map[string]any) ([]string, PolicyEffect) {
	var feats []agwFeature
	for _, sec := range []string{"traffic", "backend"} {
		m := M(spec[sec])
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		backend := sec == "backend"
		for _, k := range keys {
			if k == "phase" {
				continue // when the policy runs, not what it does
			}
			feats = append(feats, agwKey(m, k, backend))
		}
	}
	if len(feats) == 0 {
		return []string{PolicyTypeOther}, PolicyEffect{Action: "Policy", Phrase: "No settings"}
	}
	sort.SliceStable(feats, func(i, j int) bool { return feats[i].rank < feats[j].rank })
	var types, phrases []string
	for _, f := range feats {
		if !contains(types, f.typ) {
			types = append(types, f.typ)
		}
		if !contains(phrases, f.phrase) {
			phrases = append(phrases, f.phrase)
		}
	}
	return types, PolicyEffect{Action: feats[0].action, Phrase: strings.Join(phrases, "; ")}
}

func agwKey(m map[string]any, k string, backend bool) agwFeature {
	where := ""
	if backend {
		where = "backend "
	}
	switch k {
	case "authorization":
		a := strings.ToUpper(defaultString(S(Path(m, "authorization", "action")), "ALLOW"))
		return agwFeature{0, PolicyTypeAccess, a, fmt.Sprintf("%d match expressions", len(L(Path(m, "authorization", "policy", "matchExpressions"))))}
	case "extAuth":
		return agwFeature{1, PolicyTypeAccess, "Ext auth", "Check with external auth"}
	case "jwt", "jwtAuthentication":
		return agwFeature{2, PolicyTypeAccess, "JWT", "Validate JWTs"}
	case "apiKey", "apiKeyAuthentication":
		return agwFeature{3, PolicyTypeAccess, "API key", "Require an API key"}
	case "basicAuth", "basicAuthentication":
		return agwFeature{4, PolicyTypeAccess, "Basic auth", "Require basic auth"}
	case "rateLimit":
		ph := "Rate limit"
		if l := L(Path(m, "rateLimit", "local")); len(l) > 0 {
			ph = fmt.Sprintf("%d requests a %s", N(M(l[0])["requests"]), rateUnitWord(S(M(l[0])["unit"])))
		}
		return agwFeature{5, PolicyTypeResilience, "Rate limit", ph}
	case "timeouts", "retry":
		return agwFeature{6, PolicyTypeResilience, "Timeout", "Timeouts or retries"}
	case "tls":
		return agwFeature{7, PolicyTypeTransport, "TLS", "Sets " + where + "TLS"}
	case "mtls":
		return agwFeature{8, PolicyTypeTransport, "mTLS", "Sets " + where + "mTLS"}
	}
	return agwFeature{9, PolicyTypeOther, "Policy", "Sets " + where + k}
}

// ---- normalised spec -----------------------------------------------------------

// policyDefaults are values the API server or controller fills in, so an
// object that sets them and one that leaves them out are the same.
var policyDefaults = map[string]any{
	"spec.action":                       "ALLOW",
	"spec.rules[].backendRefs[].weight": float64(1),
	"spec.rules[].backendRefs[].kind":   "Service",
	"spec.rules[].backendRefs[].group":  "",
	"spec.parentRefs[].group":           GatewayGroup,
	"spec.parentRefs[].kind":            "Gateway",
	"spec.targetRefs[].group":           "",
	"spec.targetRef.group":              "",
}

// defaultMatches is what the API server sets when an HTTPRoute rule has no
// matches.
var defaultMatches = `[{"path":{"type":"PathPrefix","value":"/"}}]`

// normalisedPolicySpec is the spec as JSON values with status, metadata and
// defaulted or empty fields removed. Elements of a list are kept even when
// empty: rules: [{}] (allow all) is not rules: [] (allow nothing).
func normalisedPolicySpec(o *store.Object) any {
	b, _ := json.Marshal(o.Obj["spec"])
	var v any
	_ = json.Unmarshal(b, &v)
	return pruneSpec("spec", v)
}

func pruneSpec(pattern string, v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, x := range t {
			p := pattern + "." + k
			x = pruneSpec(p, x)
			if emptyValue(x) {
				continue
			}
			if d, ok := policyDefaults[p]; ok && d == x {
				continue
			}
			if p == "spec.rules[].matches" {
				if b, _ := json.Marshal(x); string(b) == defaultMatches {
					continue
				}
			}
			out[k] = x
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = pruneSpec(pattern+"[]", x)
		}
		return out
	}
	return v
}

func emptyValue(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case map[string]any:
		return len(t) == 0
	case []any:
		return len(t) == 0
	}
	return false
}

// policySpecKey is the canonical JSON of the normalised spec (map keys
// marshal sorted), so two objects drift exactly when their keys differ.
func policySpecKey(o *store.Object) string {
	b, _ := json.Marshal(normalisedPolicySpec(o))
	return string(b)
}

// ---- field diff ----------------------------------------------------------------

type PolicyDiffRef struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// PolicyFieldDiffEntry is one differing field. Values holds compact JSON per
// cluster that has the object; a cluster without the field has no key.
type PolicyFieldDiffEntry struct {
	Path   string            `json:"path"`
	Values map[string]string `json:"values"`
}

type PolicyDiffResponse struct {
	Ref      PolicyDiffRef          `json:"ref"`
	Clusters []string               `json:"clusters"`
	Fields   []PolicyFieldDiffEntry `json:"fields"`
	// OnlyOn lists the clusters that hold the object when some connected
	// cluster does not. Empty when every cluster has it.
	OnlyOn []string `json:"onlyOn"`
}

// PolicyKindKnown says whether kind is one the inventory covers.
func PolicyKindKnown(kind string) bool {
	for _, k := range policyKinds {
		if k.kind == kind {
			return true
		}
	}
	return false
}

// PolicyFieldDiff compares the normalised spec of one inventory object across
// the connected clusters. ok is false when no cluster has it.
func PolicyFieldDiff(ws *store.Workspace, kind, ns, name string) (*PolicyDiffResponse, bool) {
	out := &PolicyDiffResponse{Ref: PolicyDiffRef{kind, ns, name}, Clusters: ws.Clusters(), Fields: []PolicyFieldDiffEntry{}, OnlyOn: []string{}}
	if out.Clusters == nil {
		out.Clusters = []string{}
	}
	specs := map[string]any{}
	var holders []string
	for _, k := range policyKinds {
		if k.kind != kind {
			continue
		}
		for _, cl := range out.Clusters {
			if o := ws.Get(cl, k.group, k.resource, ns, name); o != nil {
				specs[cl] = normalisedPolicySpec(o)
				holders = append(holders, cl)
			}
		}
	}
	if len(holders) == 0 {
		return out, false
	}
	if len(holders) < len(out.Clusters) {
		out.OnlyOn = holders
	}
	if len(holders) > 1 {
		vals := make([]any, len(holders))
		has := make([]bool, len(holders))
		for i, cl := range holders {
			vals[i], has[i] = specs[cl], true
		}
		diffWalk("spec", holders, vals, has, &out.Fields)
	}
	return out, true
}

// diffWalk descends while every present value is a map (or every one a
// list) and records a field wherever the clusters disagree.
func diffWalk(path string, cls []string, vals []any, has []bool, out *[]PolicyFieldDiffEntry) {
	allMaps, allLists, same := true, true, true
	var first string
	firstSet := false
	for i, v := range vals {
		if !has[i] {
			same = false
			continue
		}
		if _, ok := v.(map[string]any); !ok {
			allMaps = false
		}
		if _, ok := v.([]any); !ok {
			allLists = false
		}
		b, _ := json.Marshal(v)
		if !firstSet {
			first, firstSet = string(b), true
		} else if string(b) != first {
			same = false
		}
	}
	if same {
		return
	}
	switch {
	case allMaps && firstSet:
		keys := map[string]bool{}
		for i, v := range vals {
			if has[i] {
				for k := range v.(map[string]any) {
					keys[k] = true
				}
			}
		}
		ks := make([]string, 0, len(keys))
		for k := range keys {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, k := range ks {
			sub, subHas := make([]any, len(vals)), make([]bool, len(vals))
			for i, v := range vals {
				if has[i] {
					sub[i], subHas[i] = v.(map[string]any)[k]
				}
			}
			diffWalk(path+"."+k, cls, sub, subHas, out)
		}
		return
	case allLists && firstSet:
		n := 0
		for i, v := range vals {
			if has[i] && len(v.([]any)) > n {
				n = len(v.([]any))
			}
		}
		for j := 0; j < n; j++ {
			sub, subHas := make([]any, len(vals)), make([]bool, len(vals))
			for i, v := range vals {
				if l, ok := v.([]any); has[i] && ok && j < len(l) {
					sub[i], subHas[i] = l[j], true
				}
			}
			diffWalk(path+"["+strconv.Itoa(j)+"]", cls, sub, subHas, out)
		}
		return
	}
	e := PolicyFieldDiffEntry{Path: path, Values: map[string]string{}}
	for i, v := range vals {
		if has[i] {
			b, _ := json.Marshal(v)
			e.Values[cls[i]] = string(b)
		}
	}
	*out = append(*out, e)
}
