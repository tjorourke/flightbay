package ambient

import (
	"fmt"
	"html"
	"math"
	"regexp"
	"strings"
)

// Answers are the four plain-English sentences on the Configuration tab. They
// are generated from facts read off the captured policies and routes, so a
// sentence can never disagree with what is configured. Code values are wrapped
// in <code>; every other value is HTML-escaped.
type Answers struct {
	Access     string `json:"access"`
	Routing    string `json:"routing"`
	Transport  string `json:"transport"`
	Resilience string `json:"resilience"`
}

// AnswerFacts is everything the sentences need and nothing else.
type AnswerFacts struct {
	Service    string
	Access     AccessFacts
	Routing    RoutingFacts
	Transport  TransportFacts
	Resilience ResilienceFacts
}

type AccessFacts struct {
	Allow        []AccessRule // ALLOW rules that restrict who or what gets in
	Deny         []AccessRule
	DefaultDeny  bool     // an ALLOW policy with no rules: nothing is allowed
	WaypointOnly bool     // only the waypoint's identity may reach the Pods
	JWTRequired  bool     // an ALLOW rule demands a request principal
	JWTIssuers   []string // from RequestAuthentication
	Custom       int      // rules that do not fit the structured model
}

// RouteFacts is one rule of one route attached to the Service.
type RouteFacts struct {
	Backends []Backend // Weight is the effective weight; equal splits carry 1 each
	Match    RouteMatchFacts
}

// RouteMatchFacts is empty for a rule that matches every request.
type RouteMatchFacts struct {
	Path    string // prefix or exact value, "" or "/" for everything
	Headers []string
	Method  string
	Query   []string
}

func (m RouteMatchFacts) any() bool {
	return (m.Path != "" && m.Path != "/") || len(m.Headers) > 0 || m.Method != "" || len(m.Query) > 0
}

type RoutingFacts struct{ Routes []RouteFacts }

type TransportFacts struct {
	Mode      string // "", PERMISSIVE, STRICT
	NotInMesh bool
}

type ResilienceFacts struct {
	Timeout       string
	Retries       int
	EjectAfter    int // consecutive errors before a Pod is ejected
	RateRequests  int
	RateUnit      string
	CircuitBreak  bool
	Locality      bool
	GlobalLimiter bool
}

func (r ResilienceFacts) none() bool {
	return r.Timeout == "" && r.Retries == 0 && r.EjectAfter == 0 && r.RateRequests == 0 && !r.CircuitBreak && !r.Locality && !r.GlobalLimiter
}

// maxAnswer is the longest sentence, in visible characters, that fits a card.
const maxAnswer = 90

// maxResilience is wider: the plan's own example, "Times out after 5s. Failing
// Pods are ejected after 3 errors. Limited to 100 requests a second.", is 94.
const maxResilience = 100

var tagRE = regexp.MustCompile(`<[^>]*>`)

func visibleLen(s string) int {
	return len([]rune(html.UnescapeString(tagRE.ReplaceAllString(s, ""))))
}

func code(v string) string { return "<code>" + html.EscapeString(v) + "</code>" }

func codeList(vs []string) string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, code(v))
	}
	return joinWords(out, "and")
}

func joinWords(xs []string, word string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " " + word + " " + xs[len(xs)-1]
}

// BuildAnswers renders the four sentences.
func BuildAnswers(f AnswerFacts) Answers {
	return Answers{Access: accessAnswer(f.Access), Routing: routingAnswer(f.Service, f.Routing), Transport: transportAnswer(f.Transport), Resilience: resilienceAnswer(f.Resilience)}
}

// ResilienceEmpty reports whether the resilience sentence is the greyed
// "nothing is set" one.
func ResilienceEmpty(f AnswerFacts) bool { return f.Resilience.none() }

// ---- access ---------------------------------------------------------------

func principalName(p string) string {
	if i := strings.LastIndex(p, "/sa/"); i >= 0 {
		return p[i+4:]
	}
	return p
}

// ruleParts describes one rule as what it matches (methods, paths, headers)
// and who calls (namespaces, principals, Services, address ranges).
func ruleParts(r AccessRule) (what, who string) {
	r = r.fold()
	var methods, paths, headers, callers []string
	for _, t := range r.To {
		methods = append(methods, t.Methods...)
		for _, m := range t.NotMethods {
			methods = append(methods, "not "+m)
		}
		paths = append(paths, t.Paths...)
		for _, p := range t.NotPaths {
			paths = append(paths, "not "+p)
		}
	}
	for _, w := range r.When {
		switch {
		case strings.HasPrefix(w.Key, "request.headers["):
			name := strings.TrimSuffix(strings.TrimPrefix(w.Key, "request.headers["), "]")
			if len(w.Values) > 0 {
				headers = append(headers, name+": "+strings.Join(w.Values, ", "))
			}
			if len(w.NotValues) > 0 {
				headers = append(headers, name+": not "+strings.Join(w.NotValues, ", "))
			}
		case w.Key == "cel":
			headers = append(headers, w.Values...)
		case strings.HasPrefix(w.Key, "request.auth.claims["):
			headers = append(headers, "claim "+strings.TrimSuffix(strings.TrimPrefix(w.Key, "request.auth.claims["), "]")+": "+strings.Join(w.Values, ", "))
		case w.Key == "source.namespace":
			callers = append(callers, "callers in "+code(strings.Join(w.Values, ", ")))
		default:
			callers = append(callers, code(w.Key))
		}
	}
	var ns, sa, svc, ip []string
	for _, s := range r.From {
		ns = append(ns, s.Namespaces...)
		for _, p := range s.Principals {
			sa = append(sa, principalName(p))
		}
		sa = append(sa, s.ServiceAccounts...)
		svc = append(svc, s.Services...)
		ip = append(ip, s.IPBlocks...)
		ip = append(ip, s.RemoteIPBlocks...)
	}
	if len(ns) > 0 {
		callers = append(callers, "callers in "+codeList(ns))
	}
	if len(sa) > 0 {
		callers = append(callers, "callers "+codeList(sa))
	}
	if len(svc) > 0 {
		callers = append(callers, "callers "+codeList(svc))
	}
	if len(ip) > 0 {
		callers = append(callers, "callers from "+codeList(ip))
	}
	var parts []string
	if len(methods) > 0 {
		m := codeList(methods)
		if len(paths)+len(headers) > 0 {
			m += " requests"
		}
		parts = append(parts, m)
	}
	if len(paths) > 0 {
		parts = append(parts, "to "+codeList(paths))
	}
	if len(headers) > 0 {
		hs := make([]string, 0, len(headers))
		for _, h := range headers {
			hs = append(hs, code(h))
		}
		parts = append(parts, "with "+joinWords(hs, "and"))
	}
	what = strings.Join(parts, " ")
	if len(methods) == 0 && what != "" {
		what = "requests " + what
	}
	return what, joinWords(callers, "and")
}

func phrases(rs []AccessRule, deny bool) []string {
	var out []string
	for _, r := range rs {
		what, who := ruleParts(r)
		switch {
		case deny && what != "" && who != "":
			out = append(out, denyWhat(what)+" from "+who)
		case deny && what != "":
			out = append(out, denyWhat(what))
		case deny && who != "":
			out = append(out, who)
		case deny:
			out = append(out, "every request")
		case what != "" && who != "":
			out = append(out, what+" from "+who)
		case what != "":
			out = append(out, what+" from the mesh")
		case who != "":
			out = append(out, who)
		default:
			out = append(out, "every request")
		}
	}
	return out
}

// denyWhat makes a method list read as "<code>DELETE</code> requests".
func denyWhat(what string) string {
	if strings.HasPrefix(what, "requests") || strings.Contains(what, " requests") {
		return what
	}
	return what + " requests"
}

func orJoin(ps []string) string {
	if len(ps) > 2 {
		return fmt.Sprintf("%s or %d more", ps[0], len(ps)-1)
	}
	return joinWords(ps, "or")
}

func accessAnswer(a AccessFacts) string {
	base := "Anyone in the mesh"
	allow := phrases(a.Allow, false)
	switch {
	case a.JWTRequired:
		from := ""
		if len(a.JWTIssuers) > 0 {
			from = " from " + orJoin(codes(a.JWTIssuers))
		}
		base = "Only callers with a valid JWT" + from
	case a.WaypointOnly && len(allow) == 0:
		base = "Only the waypoint can reach the Pods"
	case len(allow) > 0:
		base = "Only " + orJoin(allow)
	case a.DefaultDeny:
		base = "Nobody can call it until you allow a caller"
	case a.Custom > 0 && len(a.Deny) == 0:
		base = fmt.Sprintf("Access is limited by %d custom %s", a.Custom, plural(a.Custom, "rule", "rules"))
	}
	if len(a.Deny) > 0 {
		base += ", except " + orJoin(phrases(a.Deny, true))
	}
	if !a.JWTRequired && len(a.JWTIssuers) > 0 && len(allow) == 0 && len(a.Deny) == 0 && !a.DefaultDeny && !a.WaypointOnly {
		base += ". Tokens from " + orJoin(codes(a.JWTIssuers)) + " are checked, not required"
	}
	if visibleLen(base) > maxAnswer {
		n := len(a.Allow) + len(a.Deny)
		switch {
		case len(a.Allow) > 0 && len(a.Deny) > 0:
			return fmt.Sprintf("Only what %d allow %s permit, minus %d deny %s", len(a.Allow), plural(len(a.Allow), "rule", "rules"), len(a.Deny), plural(len(a.Deny), "rule", "rules"))
		case len(a.Allow) > 0:
			return fmt.Sprintf("Only what %d allow %s permit", len(a.Allow), plural(len(a.Allow), "rule", "rules"))
		case n > 0:
			return fmt.Sprintf("Anyone in the mesh, except what %d deny %s block", n, plural(n, "rule", "rules"))
		}
	}
	return base
}

func codes(vs []string) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, code(v))
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// ---- routing --------------------------------------------------------------

// backendName uses the version suffix when the backend shares the Service
// prefix ("reviews-v2" behind "reviews" is "v2"), otherwise the full name.
func backendName(service, name string) string {
	if service != "" && strings.HasPrefix(name, service+"-") && len(name) > len(service)+1 {
		return name[len(service)+1:]
	}
	return name
}

func percents(bs []Backend) []int {
	total := 0
	for _, b := range bs {
		total += b.Weight
	}
	out := make([]int, len(bs))
	if total <= 0 {
		return out
	}
	sum, last := 0, -1
	for i, b := range bs {
		out[i] = int(math.Round(100 * float64(b.Weight) / float64(total)))
		sum += out[i]
		if b.Weight > 0 {
			last = i
		}
	}
	if last >= 0 && sum != 100 && math.Abs(float64(sum-100)) <= 2 {
		out[last] += 100 - sum
	}
	return out
}

// Split returns "80% to v1, 20% to v2", or "all to X" for one live backend.
func splitSentence(service string, bs []Backend) string {
	live := []Backend{}
	for _, b := range bs {
		if b.Weight > 0 {
			live = append(live, b)
		}
	}
	if len(live) == 0 {
		return ""
	}
	if len(live) == 1 {
		return "all to " + code(backendName(service, live[0].Name))
	}
	ps := percents(live)
	out := make([]string, 0, len(live))
	for i, b := range live {
		out = append(out, fmt.Sprintf("%d%% to %s", ps[i], code(backendName(service, b.Name))))
	}
	return strings.Join(out, ", ")
}

func matchPhrase(m RouteMatchFacts) string {
	var ps []string
	if m.Path != "" && m.Path != "/" {
		ps = append(ps, "to "+code(m.Path))
	}
	if m.Method != "" {
		ps = append(ps, "using "+code(m.Method))
	}
	if len(m.Headers) > 0 {
		ps = append(ps, "with header "+codeList(m.Headers))
	}
	if len(m.Query) > 0 {
		ps = append(ps, "with query "+codeList(m.Query))
	}
	return "Requests " + strings.Join(ps, " ")
}

func routingAnswer(service string, r RoutingFacts) string {
	var def *RouteFacts
	var matched []RouteFacts
	for i := range r.Routes {
		if r.Routes[i].Match.any() {
			matched = append(matched, r.Routes[i])
		} else if def == nil {
			def = &r.Routes[i]
		}
	}
	defaultText := "all traffic goes to " + code(service)
	if def != nil {
		if s := splitSentence(service, def.Backends); s != "" {
			defaultText = s
		}
	}
	if len(matched) == 0 {
		if def == nil || strings.HasPrefix(defaultText, "all traffic") {
			return "All traffic goes to " + code(service)
		}
		return strings.ToUpper(defaultText[:1]) + defaultText[1:]
	}
	var parts []string
	for _, m := range matched {
		to := splitSentence(service, m.Backends)
		if to == "" {
			continue
		}
		to = strings.Replace(to, "all to ", "", 1)
		parts = append(parts, matchPhrase(m.Match)+" go to "+to)
	}
	out := orSemi(parts)
	if def != nil {
		rest := splitSentence(service, def.Backends)
		rest = strings.Replace(rest, "all to ", "", 1)
		out += "; everything else to " + rest
	} else {
		out += "; everything else to " + code(service)
	}
	if visibleLen(out) > maxAnswer {
		return fmt.Sprintf("%d routing %s send requests to different backends", len(matched)+btoi(def != nil), plural(len(matched)+btoi(def != nil), "rule", "rules"))
	}
	return out
}

func orSemi(ps []string) string { return strings.Join(ps, "; ") }

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---- transport ------------------------------------------------------------

func transportAnswer(t TransportFacts) string {
	switch {
	case t.NotInMesh:
		return "No. This Service is not in the ambient mesh, so traffic is plain."
	case t.Mode == "STRICT":
		return "Yes. Only mesh workloads can connect."
	case t.Mode == "DISABLE":
		return "Not required. Plain connections are accepted."
	}
	return "Yes between mesh workloads. Non-mesh callers are still accepted."
}

// ---- resilience -----------------------------------------------------------

func rateUnitWord(u string) string {
	switch strings.ToLower(strings.TrimSuffix(strings.TrimSpace(u), "s")) {
	case "second", "":
		return "second"
	case "minute":
		return "minute"
	case "hour":
		return "hour"
	}
	return strings.ToLower(u)
}

func resilienceAnswer(r ResilienceFacts) string {
	if r.none() {
		return "Nothing is set. No timeout, no limits, and failing Pods keep getting traffic."
	}
	var parts, nouns []string
	if r.Timeout != "" {
		parts = append(parts, "Times out after "+code(r.Timeout)+".")
		nouns = append(nouns, "timeout")
	}
	if r.Retries > 0 {
		parts = append(parts, fmt.Sprintf("Retries up to %d %s.", r.Retries, plural(r.Retries, "time", "times")))
		nouns = append(nouns, "retries")
	}
	if r.EjectAfter > 0 {
		parts = append(parts, fmt.Sprintf("Failing Pods are ejected after %d %s.", r.EjectAfter, plural(r.EjectAfter, "error", "errors")))
		nouns = append(nouns, "ejection")
	}
	if r.CircuitBreak {
		parts = append(parts, "Connections and queued requests are capped.")
		nouns = append(nouns, "circuit breakers")
	}
	if r.RateRequests > 0 {
		parts = append(parts, fmt.Sprintf("Limited to %d requests a %s.", r.RateRequests, rateUnitWord(r.RateUnit)))
		nouns = append(nouns, "rate limit")
	}
	if r.GlobalLimiter {
		parts = append(parts, "Requests share a limit across waypoints.")
		nouns = append(nouns, "shared rate limit")
	}
	if r.Locality {
		parts = append(parts, "Traffic fails over between regions.")
		nouns = append(nouns, "regional failover")
	}
	out := strings.Join(parts, " ")
	if visibleLen(out) > maxResilience {
		s := joinWords(nouns, "and")
		return strings.ToUpper(s[:1]) + s[1:] + " are set."
	}
	return out
}
