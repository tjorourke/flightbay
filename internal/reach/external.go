package reach

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/tjorourke/flightbay/internal/redact"
)

// ---------- destinations outside the cluster ----------
//
// In the OpenAI and Hugging Face incident the package registry was not in
// the cluster. The agents' egress was allowed to reach it, it accepted
// writes (PUT, then MKCOL over WebDAV) and it could fetch from the internet,
// so its reach became theirs. External hosts are therefore first-class
// destinations here: every host an agent is configured for, was seen
// calling, or can reach through a gateway, with how it gets there, what it
// is, and whether the agent holds a credential for it.

// External is one destination outside the cluster, for one agent.
type External struct {
	Host         string   `json:"host"`
	Class        string   `json:"class"`   // registry, proxy, llm, git, storage, exfil, metadata, unknown
	Product      string   `json:"product"` // what it is, in words
	Fetcher      bool     `json:"fetcher"` // fetches from elsewhere on request, so lends its reach
	ReachedVia   string   `json:"reachedVia"`
	Gateway      string   `json:"gateway,omitempty"` // ns/name of the gateway Service, or the proxy or egress waypoint
	Route        string   `json:"route,omitempty"`
	Authn        bool     `json:"authn"`
	ReadOnly     bool     `json:"readOnly"` // the gateway route only admits reads
	Verdict      string   `json:"verdict"`
	DirectEgress string   `json:"directEgress,omitempty"`       // could the agent reach it without the gateway?
	GatewayRoute string   `json:"gatewayRoute,omitempty"`       // a gateway route exists for it, though the agent calls it directly
	GatewayCreds []string `json:"gatewayCredentials,omitempty"` // credentials the gateway adds on the way
	Credentials  []string `json:"credentials,omitempty"`
	Observed     int      `json:"observed"`
	Denied       int      `json:"denied,omitempty"`
	Lookups      int      `json:"lookups,omitempty"`
	Methods      []string `json:"methods,omitempty"`
	Paths        []string `json:"paths,omitempty"`
	Tools        []string `json:"tools,omitempty"`
	WriteSeen    bool     `json:"writeSeen"`
	Write        bool     `json:"write"` // nothing on the way stops a write
	Sources      []string `json:"sources,omitempty"`
	DeclaredIn   []string `json:"declaredIn,omitempty"`
	IP           string   `json:"ip,omitempty"`
	Risk         string   `json:"risk"`
	Why          string   `json:"why"`
}

// Ways an external host is reached.
const (
	ViaDirect  = "direct"
	ViaGateway = "gateway"
	ViaEgress  = "egress-gateway" // Istio ServiceEntry bound to an egress waypoint
	ViaProxy   = "proxy"
	ViaBlocked = "blocked"
	ViaNode    = "node" // image pulls: the kubelet, not the agent
)

// hostKinds classifies external hosts. The first match wins, so the specific
// entries come before the name heuristics at the end. Extend freely.
var hostKinds = []struct {
	re      *regexp.Regexp
	class   string
	product string
}{
	{regexp.MustCompile(`^169\.254\.169\.254$|^169\.254\.170\.2$|^fd00:ec2::254$|^metadata\.google\.internal$|^metadata\.azure\.com$`), "metadata", "cloud instance metadata"},
	// Exfiltration: anything that takes data from anyone and shows it to anyone.
	{regexp.MustCompile(`(^|\.)(pastebin\.com|paste\.ee|hastebin\.com|ghostbin\.\w+|dpaste\.\w+|rentry\.co|0x0\.st|termbin\.com)$`), "exfil", "paste site"},
	{regexp.MustCompile(`(^|\.)(webhook\.site|requestbin\.(com|net)|pipedream\.net|beeceptor\.com|hookbin\.com|requestcatcher\.com|interact\.sh|oast\.(fun|live|me|online|pro|site)|burpcollaborator\.net)$`), "exfil", "request capture (webhook) service"},
	{regexp.MustCompile(`(^|\.)(ngrok\.io|ngrok-free\.app|ngrok\.app|ngrok\.dev|trycloudflare\.com|loca\.lt|localtunnel\.me|serveo\.net|localhost\.run|bore\.pub|pinggy\.link)$`), "exfil", "tunnel service"},
	{regexp.MustCompile(`(^|\.)(transfer\.sh|file\.io|wetransfer\.com|gofile\.io|catbox\.moe|temp\.sh)$`), "exfil", "file drop"},
	{regexp.MustCompile(`^hooks\.slack\.com$|(^|\.)discord(app)?\.com$|^api\.telegram\.org$`), "exfil", "chat webhook"},
	// LLM providers.
	{regexp.MustCompile(`^api\.openai\.com$`), "llm", "OpenAI"},
	{regexp.MustCompile(`^api\.anthropic\.com$`), "llm", "Anthropic"},
	{regexp.MustCompile(`\.openai\.azure\.com$|\.cognitiveservices\.azure\.com$|\.services\.ai\.azure\.com$`), "llm", "Azure OpenAI"},
	{regexp.MustCompile(`^bedrock(-runtime|-agent-runtime)?(-fips)?\.[a-z0-9-]+\.amazonaws\.com$`), "llm", "Amazon Bedrock"},
	{regexp.MustCompile(`^generativelanguage\.googleapis\.com$`), "llm", "Google Gemini"},
	{regexp.MustCompile(`(^|-)aiplatform\.googleapis\.com$`), "llm", "Google Vertex AI"},
	{regexp.MustCompile(`^api\.mistral\.ai$`), "llm", "Mistral"},
	{regexp.MustCompile(`^api\.cohere\.(com|ai)$`), "llm", "Cohere"},
	{regexp.MustCompile(`^api\.groq\.com$`), "llm", "Groq"},
	{regexp.MustCompile(`(^|\.)openrouter\.ai$`), "llm", "OpenRouter"},
	{regexp.MustCompile(`^api\.(together\.xyz|deepseek\.com|x\.ai|perplexity\.ai|fireworks\.ai)$`), "llm", "LLM API"},
	// Source control.
	{regexp.MustCompile(`^api\.githubcopilot\.com$`), "git", "GitHub Copilot (remote MCP)"},
	{regexp.MustCompile(`^(api\.|codeload\.|uploads\.)?github\.com$|^raw\.githubusercontent\.com$|^objects\.githubusercontent\.com$|\.ghe\.com$`), "git", "GitHub"},
	{regexp.MustCompile(`(^|\.)gitlab\.com$`), "git", "GitLab"},
	{regexp.MustCompile(`(^|\.)bitbucket\.org$|^api\.bitbucket\.org$`), "git", "Bitbucket"},
	// Object storage and managed data stores: write means exfiltration and a shared channel.
	{regexp.MustCompile(`(^|\.)s3([.-][a-z0-9-]+)?\.amazonaws\.com$|^s3\.amazonaws\.com$`), "storage", "Amazon S3"},
	{regexp.MustCompile(`^storage\.googleapis\.com$|\.storage\.googleapis\.com$`), "storage", "Google Cloud Storage"},
	{regexp.MustCompile(`\.blob\.core\.windows\.net$|\.dfs\.core\.windows\.net$`), "storage", "Azure Blob Storage"},
	{regexp.MustCompile(`\.r2\.cloudflarestorage\.com$|\.digitaloceanspaces\.com$|\.backblazeb2\.com$`), "storage", "object storage"},
	{regexp.MustCompile(`\.rds\.amazonaws\.com$|\.database\.windows\.net$|\.mongodb\.net$|\.cache\.amazonaws\.com$`), "storage", "managed database"},
	// Package and artifact registries: fetchers.
	{regexp.MustCompile(`(^|\.)jfrog\.io$|artifactory`), "registry", "JFrog Artifactory"},
	{regexp.MustCompile(`nexus|sonatype`), "registry", "Sonatype Nexus"},
	{regexp.MustCompile(`^(pypi\.org|upload\.pypi\.org|files\.pythonhosted\.org|test\.pypi\.org)$`), "registry", "PyPI"},
	{regexp.MustCompile(`^registry\.(npmjs\.org|yarnpkg\.com)$`), "registry", "npm registry"},
	{regexp.MustCompile(`^(proxy|sum)\.golang\.org$`), "registry", "Go module proxy"},
	{regexp.MustCompile(`^(repo1\.maven\.org|repo\.maven\.apache\.org|plugins\.gradle\.org|services\.gradle\.org)$`), "registry", "Maven repository"},
	{regexp.MustCompile(`(^|\.)(huggingface\.co|hf\.co)$`), "registry", "Hugging Face Hub (serves and accepts models and datasets)"},
	{regexp.MustCompile(`^(conda\.anaconda\.org|repo\.anaconda\.com)$`), "registry", "conda channel"},
	{regexp.MustCompile(`^(crates\.io|static\.crates\.io|index\.crates\.io|rubygems\.org|api\.nuget\.org|packagist\.org|repo\.packagist\.org)$`), "registry", "package registry"},
	{regexp.MustCompile(`^ghcr\.io$`), "registry", "GitHub container registry"},
	{regexp.MustCompile(`^((registry-1|index|auth|production\.cloudflare)\.)?docker\.(io|com)$`), "registry", "Docker Hub"},
	{regexp.MustCompile(`^quay\.io$`), "registry", "Quay"},
	{regexp.MustCompile(`(^|\.)gcr\.io$|\.pkg\.dev$`), "registry", "Google Artifact Registry"},
	{regexp.MustCompile(`\.dkr\.ecr\.[a-z0-9-]+\.amazonaws\.com$|^public\.ecr\.aws$`), "registry", "Amazon ECR"},
	{regexp.MustCompile(`\.azurecr\.io$|^mcr\.microsoft\.com$`), "registry", "container registry"},
	// Name heuristics last.
	{regexp.MustCompile(`squid|forward-?proxy|egress-?proxy|(^|[.-])proxy([.-]|$)`), "proxy", "HTTP proxy"},
	{regexp.MustCompile(`artifactory|nexus|registry|mirror|pypi|npm|harbor|verdaccio|devpi|athens`), "registry", "package or artifact registry"},
}

var classWords = map[string]string{
	"registry": "package or artifact registry", "proxy": "HTTP proxy", "llm": "LLM provider", "git": "source control",
	"storage": "data store", "exfil": "paste, webhook or tunnel service", "metadata": "cloud metadata", "unknown": "unknown",
}

// classifyHost returns the class of an external host and what it is.
func classifyHost(h string) (class, product string) {
	h = strings.ToLower(h)
	for _, k := range hostKinds {
		if k.re.MatchString(h) {
			return k.class, k.product
		}
	}
	return "unknown", ""
}

func fetcherClass(class string) bool { return class == "registry" || class == "proxy" }

// ---------- what an agent is configured to call ----------

// hostDecl is one external host an agent is configured for.
type hostDecl struct {
	where    []string
	paths    []string
	ports    []int
	prefixes []string // env name prefixes that named it: MODEL for MODEL_BASE_URL
	creds    []string // credentials declared alongside it (ModelConfig API key, .netrc machine)
	proxy    bool     // declared as the agent's HTTP proxy
}

// credRef is a credential the agent holds. Only names are known; values are
// never captured.
type credRef struct {
	name   string // env var, Secret key or config entry
	secret string // Secret it comes from
	typ    string // Secret type
	from   string // how it is mounted
}

func (c credRef) label() string {
	switch {
	case c.secret != "" && c.name != "" && c.name != c.secret:
		return c.name + " (Secret " + c.secret + ")"
	case c.secret != "":
		t := ""
		if c.typ != "" && c.typ != "Opaque" {
			t = ", " + c.typ
		}
		return "Secret " + c.secret + " (" + c.from + t + ")"
	case c.from != "":
		return c.name + " (" + c.from + ")"
	}
	return c.name
}

type decls struct {
	svcs       map[string][]string // ns/name -> where
	hosts      map[string]*hostDecl
	proxy      string // proxy host, or ns/name of a proxy Service
	proxyWhere string
	noProxy    []string
	creds      []credRef
}

func (d *decls) host(h, where string) *hostDecl {
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	x := d.hosts[h]
	if x == nil {
		x = &hostDecl{}
		d.hosts[h] = x
	}
	if where != "" && !has(x.where, where) {
		x.where = append(x.where, where)
	}
	return x
}

var (
	anyURLRe    = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]{1,15}://[^\s"'<>,;|\\\[\]{}()]+`)
	envHostName = regexp.MustCompile(`(?i)(_URL|_URI|_ENDPOINT|_ENDPOINTS|_HOST|_HOSTNAME|_REGISTRY|_ADDR|_ADDRESS|_SERVER|_MIRROR|_INDEX|_BASE)$|^(GOPROXY|PIP_INDEX_URL|PIP_EXTRA_INDEX_URL|PIP_TRUSTED_HOST|NPM_CONFIG_REGISTRY|UV_INDEX_URL|UV_EXTRA_INDEX_URL|UV_DEFAULT_INDEX|UV_INDEX|HF_ENDPOINT|CONDA_CHANNEL_ALIAS)$`)
	envSuffix   = regexp.MustCompile(`(?i)_(URL|URI|ENDPOINT|ENDPOINTS|HOST|HOSTNAME|REGISTRY|ADDR|ADDRESS|SERVER|MIRROR|INDEX|BASE)$`)
	proxyEnv    = regexp.MustCompile(`(?i)^(HTTPS?|ALL)_PROXY$`)
	noProxyEnv  = regexp.MustCompile(`(?i)^NO_PROXY$`)
	proseEnv    = regexp.MustCompile(`(?i)(MESSAGE|PROMPT|DESCRIPTION|INSTRUCTION|SYSTEM_MSG|README)`)
	hostTokenRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+(:[0-9]+)?(/.*)?$`)
)

// badHost filters addresses that are not destinations: listen addresses,
// loopback and placeholders.
func badHost(h string) bool {
	h = strings.ToLower(h)
	if h == "" || h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.Contains(h, "redacted") || strings.ContainsAny(h, "<>*$") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback() || ip.IsUnspecified()
	}
	return !strings.Contains(h, ".")
}

// addURL records one URL: a Service when it names one, else an external host.
func (c *cluster) addURL(d *decls, raw, ns, where, envName string) {
	raw = strings.ReplaceAll(raw, redact.Placeholder+"@", "")
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return
	}
	h := strings.ToLower(u.Hostname())
	if k := svcKeyFromHost(h, ns); k != "" && c.svcByKey[k] != nil {
		if !has(d.svcs[k], where) {
			d.svcs[k] = append(d.svcs[k], where)
		}
		return
	}
	if badHost(h) || clusterLocal(h) {
		return
	}
	x := d.host(h, where)
	if p := u.EscapedPath(); p != "" && p != "/" && !has(x.paths, p) {
		x.paths = append(x.paths, p)
	}
	port := 0
	if pp := u.Port(); pp != "" {
		port, _ = strconv.Atoi(pp)
	} else if u.Scheme == "https" {
		port = 443
	} else if u.Scheme == "http" {
		port = 80
	}
	if port > 0 && !hasInt(x.ports, port) {
		x.ports = append(x.ports, port)
	}
	for _, p := range envPrefixes(envName) {
		if !has(x.prefixes, p) {
			x.prefixes = append(x.prefixes, p)
		}
	}
}

// envPrefixes: MODEL_BASE_URL -> [MODEL_BASE, MODEL].
func envPrefixes(name string) []string {
	if name == "" || !envSuffix.MatchString(name) {
		return nil
	}
	base := envSuffix.ReplaceAllString(strings.ToUpper(name), "")
	var out []string
	for base != "" {
		if len(base) >= 2 {
			out = append(out, base)
		}
		i := strings.LastIndex(base, "_")
		if i <= 0 {
			break
		}
		base = base[:i]
	}
	return out
}

// envVar records one environment variable's value.
func (c *cluster) envVar(d *decls, name, value, ns, where string) {
	if value == "" || proseEnv.MatchString(name) {
		return
	}
	switch {
	case proxyEnv.MatchString(name):
		v := value
		if !strings.Contains(v, "://") {
			v = "http://" + v
		}
		u, err := url.Parse(strings.ReplaceAll(v, redact.Placeholder+"@", ""))
		if err != nil || u.Hostname() == "" {
			return
		}
		h := strings.ToLower(u.Hostname())
		if k := svcKeyFromHost(h, ns); k != "" && c.svcByKey[k] != nil {
			d.proxy, d.proxyWhere = k, where
			if !has(d.svcs[k], where) {
				d.svcs[k] = append(d.svcs[k], where)
			}
		} else if !badHost(h) {
			d.proxy, d.proxyWhere = h, where
			d.host(h, where).proxy = true
		}
		return
	case noProxyEnv.MatchString(name):
		for _, t := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' }) {
			d.noProxy = append(d.noProxy, strings.ToLower(strings.TrimPrefix(t, "*")))
		}
		return
	}
	if redact.SensitiveKey.MatchString(name) && !envHostName.MatchString(name) {
		from := "env"
		if strings.Contains(where, "ConfigMap") {
			from = strings.TrimPrefix(where, "env "+name+" (")
			from = strings.TrimSuffix(from, ")")
		}
		d.creds = append(d.creds, credRef{name: name, from: from})
		if !strings.Contains(where, "ConfigMap") {
			return
		}
	}
	for _, u := range anyURLRe.FindAllString(strings.ReplaceAll(value, redact.Placeholder+"@", ""), -1) {
		c.addURL(d, u, ns, where, name)
	}
	if envHostName.MatchString(name) && !strings.Contains(value, "://") {
		for _, t := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == '|' }) {
			t = strings.ToLower(strings.TrimSpace(t))
			if t == "direct" || t == "off" || !hostTokenRe.MatchString(t) {
				continue
			}
			c.addURL(d, "tcp://"+t, ns, where, name)
		}
	}
}

func (c *cluster) configMap(ns, name string) map[string]string {
	o := c.ws.Get(c.name, "", "configmaps", ns, name)
	if o == nil {
		return nil
	}
	return strMap(o.Obj["data"])
}

func (c *cluster) secret(ns, name string) (typ string, keys []string, ok bool) {
	o := c.ws.Get(c.name, "", "secrets", ns, name)
	if o == nil {
		return "", nil, false
	}
	for _, k := range l(path(o.Obj, "x-flightbay", "keys")) {
		keys = append(keys, s(m(k)["name"]))
	}
	for k := range m(o.Obj["data"]) {
		if !has(keys, k) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return s(o.Obj["type"]), keys, true
}

// Config files that name package indexes and registries.
var (
	iniURLRe    = regexp.MustCompile(`(?im)^\s*(?:extra-)?index(?:-url)?\s*[=:]\s*(\S+)`)
	trustedRe   = regexp.MustCompile(`(?im)^\s*trusted-host\s*[=:]\s*(.+)$`)
	npmRegRe    = regexp.MustCompile(`(?im)^\s*(?:@[\w.-]+:)?registry\s*=\s*(\S+)`)
	npmAuthRe   = regexp.MustCompile(`(?im)^\s*//([^/:\s]+)[^:\s]*:(_authToken|_auth|_password|username)\s*=`)
	xmlURLRe    = regexp.MustCompile(`(?i)<url>\s*([^<\s]+)\s*</url>`)
	xmlServerRe = regexp.MustCompile(`(?is)<server>\s*<id>\s*([^<\s]+)\s*</id>.*?</server>`)
	netrcRe     = regexp.MustCompile(`(?m)\bmachine\s+(\S+)`)
	keyURLRe    = regexp.MustCompile(`(?im)^\s*["']?[\w.-]*(url|uri|endpoint|registry|index|mirror|proxy|host|base)[\w.-]*["']?\s*[=:]\s*["']?([a-z][a-z0-9+.-]{1,15}://[^\s"',]+)`)
	credKeyRe   = regexp.MustCompile(`(?im)^\s*["']?([\w.-]*(?:password|passwd|token|secret|api[_-]?key|credential|_auth)[\w.-]*)["']?\s*[=:]`)
)

// configFile records the hosts and credentials a mounted config file names.
func (c *cluster) configFile(d *decls, file, content, ns, where string) {
	base := strings.ToLower(file)
	where = where + " " + file
	add := func(raw string) {
		if !strings.Contains(raw, "://") {
			raw = "https://" + raw
		}
		c.addURL(d, raw, ns, where, "")
	}
	switch {
	case base == "pip.conf" || base == "pip.ini" || base == ".pypirc" || base == "pypirc":
		for _, mm := range iniURLRe.FindAllStringSubmatch(content, -1) {
			add(mm[1])
		}
		for _, mm := range keyURLRe.FindAllStringSubmatch(content, -1) {
			add(mm[2])
		}
		for _, mm := range trustedRe.FindAllStringSubmatch(content, -1) {
			for _, h := range strings.Fields(mm[1]) {
				add(h)
			}
		}
	case base == ".npmrc" || base == "npmrc" || base == ".yarnrc.yml" || base == ".yarnrc":
		for _, mm := range npmRegRe.FindAllStringSubmatch(content, -1) {
			add(mm[1])
		}
		for _, mm := range npmAuthRe.FindAllStringSubmatch(content, -1) {
			add(mm[1])
			d.creds = append(d.creds, credRef{name: "npm " + mm[2] + " for " + mm[1], from: where})
			d.host(mm[1], "").creds = append(d.host(mm[1], "").creds, "npm "+mm[2]+" ("+where+")")
		}
		for _, mm := range keyURLRe.FindAllStringSubmatch(content, -1) {
			add(mm[2])
		}
	case base == "settings.xml":
		for _, mm := range xmlURLRe.FindAllStringSubmatch(content, -1) {
			add(mm[1])
		}
		for _, mm := range xmlServerRe.FindAllStringSubmatch(content, -1) {
			if strings.Contains(mm[0], "<password>") {
				d.creds = append(d.creds, credRef{name: "maven server " + mm[1], from: where})
			}
		}
	case base == ".netrc" || base == "netrc" || base == "_netrc":
		for _, mm := range netrcRe.FindAllStringSubmatch(content, -1) {
			add(mm[1])
			d.host(mm[1], "").creds = append(d.host(mm[1], "").creds, "netrc machine "+mm[1]+" ("+where+")")
			d.creds = append(d.creds, credRef{name: "netrc machine " + mm[1], from: where})
		}
	default:
		// gradle.properties, .condarc, uv.toml, pyproject.toml and anything
		// else: URLs under keys that name an endpoint or index.
		for _, mm := range keyURLRe.FindAllStringSubmatch(content, -1) {
			add(mm[2])
		}
		if base == ".condarc" || base == "condarc" {
			for _, u := range anyURLRe.FindAllString(content, -1) {
				add(u)
			}
		}
	}
	if base == "gradle.properties" || base == ".pypirc" || base == "pypirc" || strings.HasSuffix(base, ".toml") {
		for _, mm := range credKeyRe.FindAllStringSubmatch(content, -1) {
			d.creds = append(d.creds, credRef{name: mm[1], from: where})
		}
	}
}

// declarations collects what a workload is configured to call and the
// credentials it holds: env (values, ConfigMap and Secret references,
// envFrom), mounted ConfigMaps and Secrets, and for kagent agents the
// ModelConfig and tool servers.
func (c *cluster) declarations(w *workload) *decls {
	if w.decl != nil {
		return w.decl
	}
	d := &decls{svcs: map[string][]string{}, hosts: map[string]*hostDecl{}}
	ns := w.ns
	for _, ct := range append(l(w.spec["containers"]), l(w.spec["initContainers"])...) {
		for _, e := range l(m(ct)["env"]) {
			em := m(e)
			name := s(em["name"])
			if v := s(em["value"]); v != "" {
				c.envVar(d, name, v, ns, "env "+name)
				continue
			}
			if ref := m(path(em, "valueFrom", "configMapKeyRef")); ref != nil {
				if cm := c.configMap(ns, s(ref["name"])); cm != nil {
					c.envVar(d, name, cm[s(ref["key"])], ns, "env "+name+" (ConfigMap "+s(ref["name"])+")")
				}
			}
			if ref := m(path(em, "valueFrom", "secretKeyRef")); ref != nil {
				typ, _, _ := c.secret(ns, s(ref["name"]))
				d.creds = append(d.creds, credRef{name: name, secret: s(ref["name"]), typ: typ, from: "env"})
			}
		}
		for _, e := range l(m(ct)["envFrom"]) {
			em := m(e)
			prefix := s(em["prefix"])
			if n := s(path(em, "configMapRef", "name")); n != "" {
				cm := c.configMap(ns, n)
				for _, k := range sortedKeys(cm) {
					c.envVar(d, prefix+k, cm[k], ns, "env "+prefix+k+" (ConfigMap "+n+")")
				}
			}
			if n := s(path(em, "secretRef", "name")); n != "" {
				typ, keys, _ := c.secret(ns, n)
				if len(keys) == 0 {
					d.creds = append(d.creds, credRef{secret: n, typ: typ, from: "envFrom"})
				}
				for _, k := range keys {
					d.creds = append(d.creds, credRef{name: prefix + k, secret: n, typ: typ, from: "envFrom"})
				}
			}
		}
	}
	for _, v := range l(w.spec["volumes"]) {
		vm := m(v)
		var cms []map[string]any
		if cm := m(vm["configMap"]); cm != nil {
			cms = append(cms, cm)
		}
		var secs []map[string]any
		if sc := m(vm["secret"]); sc != nil {
			secs = append(secs, map[string]any{"name": sc["secretName"]})
		}
		for _, src := range l(path(vm, "projected", "sources")) {
			if cm := m(m(src)["configMap"]); cm != nil {
				cms = append(cms, cm)
			}
			if sc := m(m(src)["secret"]); sc != nil {
				secs = append(secs, sc)
			}
		}
		for _, cm := range cms {
			n := s(cm["name"])
			if n == "kube-root-ca.crt" || strings.HasPrefix(n, "istio-ca-root-cert") {
				continue
			}
			data := c.configMap(ns, n)
			files := map[string]string{} // key -> file name
			for k := range data {
				files[k] = k
			}
			if items := l(cm["items"]); len(items) > 0 {
				files = map[string]string{}
				for _, it := range items {
					p := s(m(it)["path"])
					if i := strings.LastIndex(p, "/"); i >= 0 {
						p = p[i+1:]
					}
					files[s(m(it)["key"])] = p
				}
			}
			for _, k := range sortedKeys(files) {
				c.configFile(d, files[k], data[k], ns, "ConfigMap "+n)
			}
		}
		for _, sc := range secs {
			n := s(sc["name"])
			if n == "" {
				continue
			}
			typ, keys, _ := c.secret(ns, n)
			d.creds = append(d.creds, credRef{secret: n, typ: typ, from: "mounted"})
			for _, k := range keys {
				if k == ".netrc" || k == "netrc" {
					d.creds = append(d.creds, credRef{name: k, secret: n, from: "mounted"})
				}
			}
		}
	}
	if w.agentType == "kagent-agent" {
		c.kagentDecls(w, d)
	}
	w.decl = d
	return d
}

// Provider defaults for kagent ModelConfigs with no base URL.
var providerHosts = map[string]string{
	"anthropic": "api.anthropic.com", "openai": "api.openai.com", "gemini": "generativelanguage.googleapis.com",
	"geminivertexai": "aiplatform.googleapis.com", "anthropicvertexai": "aiplatform.googleapis.com", "vertexai": "aiplatform.googleapis.com",
	"mistral": "api.mistral.ai", "cohere": "api.cohere.com", "groq": "api.groq.com", "openrouter": "openrouter.ai",
}

func (c *cluster) kagentDecls(w *workload, d *decls) {
	a := c.ws.Get(c.name, "kagent.dev", "agents", w.ns, w.name)
	if a == nil {
		return
	}
	decl := m(path(a.Obj, "spec", "declarative"))
	mc := s(decl["modelConfig"])
	if mc == "" && decl != nil { // a BYO agent brings its own model settings, in its env
		mc = "default-model-config"
	}
	if o := c.ws.Get(c.name, "kagent.dev", "modelconfigs", w.ns, mc); mc != "" && o != nil {
		sp := m(o.Obj["spec"])
		where := "ModelConfig " + w.ns + "/" + mc
		before := len(d.hosts)
		for _, p := range []string{"openAI", "anthropic", "ollama", "azureOpenAI", "gemini", "geminiVertexAI", "anthropicVertexAI", "bedrock"} {
			for _, f := range []string{"baseUrl", "baseURL", "host", "azureEndpoint", "endpoint"} {
				if v := s(path(sp, p, f)); v != "" {
					if !strings.Contains(v, "://") {
						v = "https://" + v
					}
					c.addURL(d, v, w.ns, where, "")
				}
			}
		}
		prov := strings.ToLower(s(sp["provider"]))
		host := ""
		if len(d.hosts) == before {
			host = providerHosts[prov]
			if prov == "bedrock" {
				if r := s(path(sp, "bedrock", "region")); r != "" {
					host = "bedrock-runtime." + r + ".amazonaws.com"
				}
			}
			if strings.Contains(prov, "vertex") {
				if r := s(path(sp, "geminiVertexAI", "location")) + s(path(sp, "anthropicVertexAI", "location")); r != "" && r != "global" {
					host = r + "-aiplatform.googleapis.com"
				}
			}
			if host != "" {
				d.host(host, where+" (provider "+s(sp["provider"])+" default)")
			}
		}
		if k := s(sp["apiKeySecretKey"]); k != "" {
			sec := s(sp["apiKeySecret"])
			if sec == redact.Placeholder {
				sec = ""
			}
			label := k + " (" + where + ")"
			for _, x := range d.hosts {
				for _, wh := range x.where {
					if strings.HasPrefix(wh, where) && !has(x.creds, label) {
						x.creds = append(x.creds, label)
					}
				}
			}
		}
	}
	for _, t := range l(decl["tools"]) {
		ms := m(m(t)["mcpServer"])
		if n := s(ms["name"]); n != "" {
			ns := s(ms["namespace"])
			if ns == "" {
				ns = w.ns
			}
			if o := c.ws.Get(c.name, "kagent.dev", "remotemcpservers", ns, n); o != nil {
				c.addURL(d, s(path(o.Obj, "spec", "url")), ns, "RemoteMCPServer "+ns+"/"+n, "")
			} else if c.svcByKey[key(ns, n)] != nil {
				d.svcs[key(ns, n)] = append(d.svcs[key(ns, n)], "kagent tool "+n)
			}
		}
	}
}

// ---------- agentgateway backends ----------

// agwTarget is one thing an agentgateway route sends to.
type agwTarget struct {
	host     string   // external host ("" for an in-cluster Service)
	svc      *service // in-cluster Service
	label    string
	route    string // ns/name of the HTTPRoute
	backend  string // kind ns/name of the backend
	auth     bool   // the route or gateway requires authentication
	readOnly bool   // the route only admits GET and HEAD
	llm      string // provider, for AI backends
	port     int
	path     string
	creds    []string // credentials the gateway adds for this backend
}

var llmDefault = map[string]string{"openai": "api.openai.com", "anthropic": "api.anthropic.com", "gemini": "generativelanguage.googleapis.com", "vertexai": "aiplatform.googleapis.com"}

// llmHost is the host an agentgateway AI provider entry sends to.
func llmHost(p map[string]any) (host, prov string) {
	for k, v := range p {
		switch k {
		case "host", "port", "path", "pathPrefix", "name", "policies", "authHeader":
			continue
		}
		vm := m(v)
		if vm == nil {
			continue
		}
		prov = k
		switch k {
		case "azureopenai", "azure":
			host = s(vm["endpoint"])
		case "bedrock":
			if r := s(vm["region"]); r != "" {
				host = "bedrock-runtime." + r + ".amazonaws.com"
			}
		case "vertexai":
			if r := s(vm["region"]); r != "" && r != "global" {
				host = r + "-aiplatform.googleapis.com"
			} else {
				host = "aiplatform.googleapis.com"
			}
		default:
			host = llmDefault[k]
		}
	}
	if h := s(p["host"]); h != "" {
		host = h
	}
	return
}

// policiesWith returns the agentgateway policies (OSS and Enterprise) by
// target "Kind/ns/name".
func (c *cluster) agwPolicies() map[string][]map[string]any {
	if c.agwPol != nil {
		return c.agwPol
	}
	out := map[string][]map[string]any{}
	for _, g := range []struct{ group, res string }{{"agentgateway.dev", "agentgatewaypolicies"}, {"enterpriseagentgateway.solo.io", "enterpriseagentgatewaypolicies"}} {
		for _, p := range c.list(g.group, g.res) {
			sp := m(p.Obj["spec"])
			for _, tr := range l(sp["targetRefs"]) {
				k := s(m(tr)["kind"]) + "/" + p.Namespace + "/" + s(m(tr)["name"])
				out[k] = append(out[k], sp)
			}
		}
	}
	c.agwPol = out
	return out
}

func authnIn(sp map[string]any) bool {
	t := m(sp["traffic"])
	return t["jwtAuthentication"] != nil || t["apiKeyAuthentication"] != nil || t["basicAuthentication"] != nil || t["extAuth"] != nil || t["entExtAuth"] != nil
}

// readOnlyIn: a traffic.authorization rule that limits request.method and
// does not name a write method.
func readOnlyIn(sp map[string]any) bool {
	a := m(path(sp, "traffic", "authorization"))
	if a == nil || strings.EqualFold(s(a["action"]), "Deny") {
		return false
	}
	text := strings.Join(strList(l(path(a, "policy", "matchExpressions"))), " ")
	if !strings.Contains(text, "request.method") {
		return false
	}
	for mth := range writeMethods {
		if strings.Contains(text, `"`+mth+`"`) || strings.Contains(text, `'`+mth+`'`) {
			return false
		}
	}
	return true
}

// agwTargets lists what the routes on this agentgateway Gateway send to.
func (c *cluster) agwTargets(gw *service) []agwTarget {
	if t, ok := c.agwT[gw]; ok {
		return t
	}
	var out []agwTarget
	backends := map[string]map[string]any{}
	for _, g := range []struct{ group, res string }{{"agentgateway.dev", "agentgatewaybackends"}, {"enterpriseagentgateway.solo.io", "enterpriseagentgatewaybackends"}} {
		for _, b := range c.list(g.group, g.res) {
			backends[key(b.Namespace, b.Name)] = m(b.Obj["spec"])
		}
	}
	pols := c.agwPolicies()
	gwPols := pols["Gateway/"+gw.gatewayNS+"/"+gw.gatewayNm]
	anyOf := func(ps []map[string]any, f func(map[string]any) bool) bool {
		for _, p := range ps {
			if f(p) {
				return true
			}
		}
		return false
	}
	gwAuth, gwRO := anyOf(gwPols, authnIn), anyOf(gwPols, readOnlyIn)
	for _, r := range c.list("gateway.networking.k8s.io", "httproutes") {
		attached := false
		for _, pr := range l(path(r.Obj, "spec", "parentRefs")) {
			pm := m(pr)
			ns := s(pm["namespace"])
			if ns == "" {
				ns = r.Namespace
			}
			if s(pm["name"]) == gw.gatewayNm && ns == gw.gatewayNS && (s(pm["kind"]) == "" || s(pm["kind"]) == "Gateway") {
				attached = true
			}
		}
		if !attached {
			continue
		}
		rp := pols["HTTPRoute/"+r.Namespace+"/"+r.Name]
		auth := gwAuth || anyOf(rp, authnIn)
		ro := gwRO || anyOf(rp, readOnlyIn)
		route := r.Namespace + "/" + r.Name
		for _, rule := range l(path(r.Obj, "spec", "rules")) {
			ruleRO := ro
			if ms := l(m(rule)["matches"]); len(ms) > 0 {
				all := true
				for _, mt := range ms {
					if mth := strings.ToUpper(s(m(mt)["method"])); mth != "GET" && mth != "HEAD" {
						all = false
					}
				}
				ruleRO = ruleRO || all
			}
			for _, br := range l(m(rule)["backendRefs"]) {
				bm := m(br)
				ns := s(bm["namespace"])
				if ns == "" {
					ns = r.Namespace
				}
				bname := s(bm["name"])
				base := agwTarget{route: route, auth: auth, readOnly: ruleRO}
				var ts []agwTarget
				switch s(bm["kind"]) {
				case "AgentgatewayBackend", "EnterpriseAgentgatewayBackend":
					sp := backends[key(ns, bname)]
					if sec := s(path(sp, "policies", "auth", "secretRef", "name")); sec != "" {
						base.creds = []string{"Secret " + sec + " (backend auth)"}
					}
					bp := pols[s(bm["kind"])+"/"+ns+"/"+bname]
					if anyOf(bp, authnIn) {
						base.auth = true
					}
					if anyOf(bp, readOnlyIn) {
						base.readOnly = true
					}
					base.backend = s(bm["kind"]) + " " + ns + "/" + bname
					mc := m(sp["mcp"])
					if mc == nil {
						mc = m(sp["entMcp"])
					}
					for _, t := range l(mc["targets"]) {
						st := m(m(t)["static"])
						if h := s(st["host"]); h != "" {
							x := base
							x.host, x.path = h, s(st["path"])
							if sec := s(path(st, "policies", "auth", "secretRef", "name")); sec != "" {
								x.creds = append(append([]string{}, x.creds...), "Secret "+sec+" (backend auth)")
							}
							x.port = int(num(st["port"]))
							ts = append(ts, x)
						}
					}
					if ai := m(sp["ai"]); ai != nil {
						if p := m(ai["provider"]); p != nil {
							x := base
							x.host, x.llm = llmHost(p)
							ts = append(ts, x)
						}
						for _, g := range l(ai["groups"]) {
							for _, p := range l(m(g)["providers"]) {
								x := base
								x.host, x.llm = llmHost(m(p))
								ts = append(ts, x)
							}
						}
					}
					if st := m(sp["static"]); st != nil && s(st["host"]) != "" {
						x := base
						x.host, x.port = s(st["host"]), int(num(st["port"]))
						ts = append(ts, x)
					}
					if sp["dynamicForwardProxy"] != nil {
						x := base
						x.host = "any host (dynamic forward proxy " + bname + ")"
						ts = append(ts, x)
					}
					if len(ts) == 0 {
						x := base
						x.label = ns + "/" + bname
						ts = append(ts, x)
					}
				case "", "Service":
					x := base
					x.svc = c.svcByKey[key(ns, bname)]
					x.label = ns + "/" + bname
					ts = append(ts, x)
				default:
					continue
				}
				for _, t := range ts {
					if t.host != "" {
						if k := svcKeyFromHost(t.host, ns); k != "" && c.svcByKey[k] != nil {
							t.svc, t.label, t.host = c.svcByKey[k], k, ""
						} else {
							t.host = strings.ToLower(t.host)
							t.label = t.host
						}
					}
					out = append(out, t)
				}
			}
		}
	}
	if c.agwT == nil {
		c.agwT = map[*service][]agwTarget{}
	}
	c.agwT[gw] = out
	return out
}

func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	}
	return 0
}

// ---------- Istio ServiceEntries ----------

type seInfo struct {
	ns, name     string
	waypoint     string
	waypointNS   string
	resolutionIP bool
}

func (c *cluster) serviceEntries() map[string]seInfo {
	if c.seByHost != nil {
		return c.seByHost
	}
	out := map[string]seInfo{}
	for _, g := range []string{"networking.istio.io"} {
		for _, o := range c.list(g, "serviceentries") {
			sp := m(o.Obj["spec"])
			if s(sp["location"]) == "MESH_INTERNAL" {
				continue
			}
			lb := o.Labels()
			wp := lb["istio.io/use-waypoint"]
			if wp == "" {
				wp = c.nsLabels[o.Namespace]["istio.io/use-waypoint"]
			}
			if wp == "none" {
				wp = ""
			}
			wpNS := lb["istio.io/use-waypoint-namespace"]
			if wpNS == "" {
				wpNS = o.Namespace
			}
			for _, h := range strList(l(sp["hosts"])) {
				out[strings.ToLower(h)] = seInfo{ns: o.Namespace, name: o.Name, waypoint: wp, waypointNS: wpNS}
			}
		}
	}
	c.seByHost = out
	return out
}

func (c *cluster) seFor(host string) (seInfo, bool) {
	se := c.serviceEntries()
	if x, ok := se[host]; ok {
		return x, true
	}
	for h, x := range se {
		if strings.HasPrefix(h, "*.") && strings.HasSuffix(host, h[1:]) {
			return x, true
		}
	}
	return seInfo{}, false
}

// hasAgentgateway: any agentgateway Gateway Service in the cluster.
func (c *cluster) agentgateways() []*service {
	var out []*service
	for _, sv := range c.services {
		if sv.agwClass != "" {
			out = append(out, sv)
		}
	}
	return out
}

// ---------- image registries ----------

func imageRegistry(img string) string {
	if img == "" {
		return ""
	}
	i := strings.Index(img, "/")
	if i < 0 {
		return "docker.io"
	}
	first := img[:i]
	if strings.ContainsAny(first, ".:") || first == "localhost" {
		return strings.ToLower(first)
	}
	return "docker.io"
}

// ---------- credentials ----------

var credProducts = []struct{ host, cred *regexp.Regexp }{
	{regexp.MustCompile(`jfrog|artifactory`), regexp.MustCompile(`(?i)artifactory|jfrog|^JF_|^RT_`)},
	{regexp.MustCompile(`nexus|sonatype`), regexp.MustCompile(`(?i)nexus`)},
	{regexp.MustCompile(`pypi|pythonhosted`), regexp.MustCompile(`(?i)^PIP_|pypi|twine|^UV_INDEX|poetry`)},
	{regexp.MustCompile(`npmjs|yarnpkg|npm`), regexp.MustCompile(`(?i)^NPM_|NODE_AUTH_TOKEN|YARN_NPM|npm `)},
	{regexp.MustCompile(`huggingface|hf\.co`), regexp.MustCompile(`(?i)^HF_|hugging`)},
	{regexp.MustCompile(`github`), regexp.MustCompile(`(?i)github|^GH_`)},
	{regexp.MustCompile(`gitlab`), regexp.MustCompile(`(?i)gitlab|^GL_|CI_JOB_TOKEN`)},
	{regexp.MustCompile(`bitbucket`), regexp.MustCompile(`(?i)bitbucket`)},
	{regexp.MustCompile(`amazonaws\.com$|\.aws$`), regexp.MustCompile(`(?i)^AWS_`)},
	{regexp.MustCompile(`googleapis\.com$`), regexp.MustCompile(`(?i)^GOOGLE_|^GCP_|^GCLOUD|^GCS_`)},
	{regexp.MustCompile(`windows\.net$|azure`), regexp.MustCompile(`(?i)^AZURE_|^AZ_`)},
	{regexp.MustCompile(`openai`), regexp.MustCompile(`(?i)openai`)},
	{regexp.MustCompile(`anthropic`), regexp.MustCompile(`(?i)anthropic|claude`)},
	{regexp.MustCompile(`mistral`), regexp.MustCompile(`(?i)mistral`)},
	{regexp.MustCompile(`cohere`), regexp.MustCompile(`(?i)cohere`)},
	{regexp.MustCompile(`groq`), regexp.MustCompile(`(?i)groq`)},
	{regexp.MustCompile(`openrouter`), regexp.MustCompile(`(?i)openrouter`)},
	{regexp.MustCompile(`docker\.io|ghcr\.io|quay\.io|gcr\.io|pkg\.dev|ecr|azurecr`), regexp.MustCompile(`(?i)docker|registry|dockerconfig`)},
}

var (
	hostStop   = map[string]bool{"api": true, "www": true, "com": true, "net": true, "org": true, "io": true, "dev": true, "cloud": true, "registry": true, "amazonaws": true, "googleapis": true, "windows": true, "core": true, "azure": true, "services": true, "service": true, "internal": true, "local": true, "prod": true, "blob": true, "storage": true, "files": true, "static": true, "cdn": true, "app": true}
	readOnlyRe = regexp.MustCompile(`(?i)(^|[_-])(read|ro|readonly|read_only|pull|viewer|download|fetch)([_-]|$)|readonly|read-only`)
)

func hostTokens(h string) []string {
	var out []string
	for _, p := range strings.Split(strings.ToLower(h), ".") {
		if len(p) >= 4 && !hostStop[p] && net.ParseIP(h) == nil {
			out = append(out, p)
		}
	}
	return out
}

// credsFor lists the credentials the agent holds that are for this host.
func credsFor(host string, d *decls, hd *hostDecl) []string {
	var out []string
	add := func(x string) {
		if !has(out, x) {
			out = append(out, x)
		}
	}
	if hd != nil {
		for _, x := range hd.creds {
			add(x)
		}
	}
	lh := strings.ToLower(host)
	toks := hostTokens(host)
	for _, cr := range d.creds {
		text := cr.name + " " + cr.secret
		lt := strings.ToLower(text)
		hit := false
		for _, p := range credProducts {
			if p.host.MatchString(lh) && (cr.name != "" && p.cred.MatchString(cr.name) || cr.secret != "" && p.cred.MatchString(cr.secret)) {
				hit = true
			}
		}
		for _, t := range toks {
			if strings.Contains(lt, t) {
				hit = true
			}
		}
		if hd != nil && cr.name != "" {
			for _, p := range hd.prefixes {
				if strings.HasPrefix(strings.ToUpper(cr.name), p+"_") {
					hit = true
				}
			}
		}
		if (cr.typ == "kubernetes.io/dockerconfigjson" || cr.typ == "kubernetes.io/dockercfg") && cr.secret != "" {
			hit = false
			for _, t := range toks {
				if strings.Contains(strings.ToLower(cr.secret), t) {
					hit = true
				}
			}
		}
		if hit {
			add(cr.label())
		}
	}
	return out
}

// ---------- per agent ----------

type extAcc struct {
	host      string
	where     []string
	hd        *hostDecl
	gw        []agwTarget
	gwSvc     map[string]*service
	direct    bool        // configured or observed without a gateway
	selfGW    []agwTarget // this agent is the gateway that calls it
	obsDirect *observed
	obsVia    []*observed
	image     []string
	forced    string // class override (a declared proxy)
	extName   string // ExternalName Service that points here
}

// externals works out every destination outside the cluster for one agent.
func (e *engine) externals(w *workload, a *Agent, reach []pair, obs map[string]*observed) []External {
	c := e.c
	d := c.declarations(w)
	acc := map[string]*extAcc{}
	get := func(h string) *extAcc {
		h = strings.ToLower(strings.TrimSuffix(h, "."))
		x := acc[h]
		if x == nil {
			x = &extAcc{host: h, gwSvc: map[string]*service{}}
			acc[h] = x
		}
		return x
	}
	addWhere := func(x *extAcc, where string) {
		if where != "" && !has(x.where, where) {
			x.where = append(x.where, where)
		}
	}
	for h, hd := range d.hosts {
		x := get(h)
		x.hd, x.direct = hd, true
		for _, wh := range hd.where {
			addWhere(x, wh)
		}
		if hd.proxy {
			x.forced = "proxy"
		}
	}
	// ExternalName Services it is configured for or called.
	for _, sv := range c.services {
		if sv.external == "" {
			continue
		}
		k := key(sv.ns, sv.name)
		if len(d.svcs[k]) == 0 && obs[k] == nil {
			continue
		}
		x := get(sv.external)
		x.direct, x.extName = true, k
		addWhere(x, "ExternalName Service "+k)
		if o := obs[k]; o != nil {
			x.obsDirect = o
		}
	}
	// Backends of the agentgateways it can reach, and of the gateway itself.
	for _, gw := range c.agentgateways() {
		self := false
		for _, b := range gw.backends {
			if b == w {
				self = true
			}
		}
		reachable := false
		for _, p := range reach {
			if p.sv == gw && p.h.verdict == Allowed {
				reachable = true
			}
		}
		if !self && !reachable {
			continue
		}
		for _, t := range c.agwTargets(gw) {
			if t.host == "" {
				continue
			}
			x := get(t.host)
			where := t.backend
			if where == "" {
				where = "route " + t.route
			}
			if self {
				x.direct = true
				x.selfGW = append(x.selfGW, t)
				addWhere(x, where+" (this gateway calls it)")
				continue
			}
			x.gw = append(x.gw, t)
			x.gwSvc[t.route] = gw
			addWhere(x, where+" through "+svcLabel(gw))
		}
	}
	// ServiceEntries in the agent's namespace.
	for h, se := range c.serviceEntries() {
		if se.ns == w.ns && !strings.HasPrefix(h, "*") {
			x := get(h)
			x.direct = true
			addWhere(x, "ServiceEntry "+se.ns+"/"+se.name)
		}
	}
	// Observed traffic.
	for k, o := range obs {
		switch {
		case strings.HasPrefix(k, "ext:"):
			x := get(strings.TrimPrefix(k, "ext:"))
			if o.requests > 0 {
				x.direct = true
			}
			x.obsDirect = o
		case strings.HasPrefix(k, "via:"):
			parts := strings.SplitN(strings.TrimPrefix(k, "via:"), "|", 2)
			if len(parts) != 2 || !strings.HasPrefix(parts[1], "ext:") {
				continue
			}
			x := get(strings.TrimPrefix(parts[1], "ext:"))
			x.obsVia = append(x.obsVia, o)
			if gw := c.svcByKey[parts[0]]; gw != nil {
				found := false
				for _, t := range x.gw {
					if x.gwSvc[t.route] == gw {
						found = true
					}
				}
				if !found { // seen through a gateway whose routes were not captured
					route := ""
					if len(o.routes) > 0 {
						route = o.routes[0]
					}
					x.gw = append(x.gw, agwTarget{host: x.host, route: route, label: x.host})
					x.gwSvc[route] = gw
				}
			}
		}
	}
	// Image pulls: the node's traffic, listed so the table is complete.
	for _, img := range w.images {
		if h := imageRegistry(img); h != "" {
			x := get(h)
			x.image = append(x.image, img)
		}
	}

	var out []External
	internet := e.internetOf(w)
	for _, h := range sortedKeys(acc) {
		x := acc[h]
		ex := External{Host: h, DeclaredIn: x.where}
		ex.Class, ex.Product = classifyHost(h)
		if x.forced != "" {
			ex.Class, ex.Product = x.forced, "HTTP proxy (the agent's "+strings.TrimPrefix(d.proxyWhere, "env ")+")"
		}
		if strings.HasPrefix(h, "any host (") {
			ex.Class, ex.Product = "proxy", "dynamic forward proxy: sends to whatever host the request names"
		}
		if ex.Product == "" {
			ex.Product = classWords[ex.Class]
		}
		ex.Fetcher = fetcherClass(ex.Class)
		onlyImage := len(x.image) > 0 && !x.direct && len(x.gw) == 0 && x.obsDirect == nil && len(x.obsVia) == 0
		if onlyImage {
			ex.ReachedVia, ex.Verdict, ex.Risk, ex.Why = ViaNode, "", "grey", "image pulls by the node, not agent traffic"
			ex.DeclaredIn = []string{"image pulls (node): " + strings.Join(firstN(x.image, 3), ", ")}
			out = append(out, ex)
			continue
		}
		for _, img := range x.image {
			ex.DeclaredIn = append(ex.DeclaredIn, "image "+img+" (node pull)")
		}
		// Observed.
		for _, o := range append([]*observed{x.obsDirect}, x.obsVia...) {
			if o == nil {
				continue
			}
			ex.Observed += o.requests
			ex.Denied += o.denied
			ex.Lookups += o.lookups
			ex.Methods = union(ex.Methods, o.methods)
			ex.Tools = union(ex.Tools, o.tools)
			ex.Sources = union(ex.Sources, o.sources)
			for _, p := range o.paths {
				if len(ex.Paths) < 5 && !has(ex.Paths, p) {
					ex.Paths = append(ex.Paths, p)
				}
			}
			if ex.IP == "" && o.ip != "" {
				ex.IP = o.ip
			}
		}
		for _, mth := range ex.Methods {
			if writeMethods[mth] {
				ex.WriteSeen = true
			}
		}
		if net.ParseIP(h) != nil {
			ex.IP = h
		}
		// Direct egress: NetworkPolicy ipBlocks decide it when the address
		// is known; otherwise the agent's internet verdict stands for it.
		direct := internet
		switch {
		case ex.Class == "metadata":
			direct, _ = c.egressTo(w, metadataIP)
		case ex.IP != "":
			direct, _ = c.egressTo(w, net.ParseIP(ex.IP))
		}
		ex.DirectEgress = direct
		ex.Verdict = direct
		ex.ReachedVia = ViaDirect
		proxied := d.proxy != "" && h != d.proxy && !noProxy(h, d.noProxy) && ex.Class != "metadata"
		if se, ok := c.seFor(h); ok && se.waypoint != "" {
			ex.ReachedVia, ex.Gateway = ViaEgress, "waypoint "+se.waypointNS+"/"+se.waypoint
			for _, wp := range c.works {
				if wp.ns == se.waypointNS && wp.labels["gateway.networking.k8s.io/gateway-name"] == se.waypoint {
					ex.Verdict, _ = c.npEgress(w, wp.ns, wp.labels, []string{"15008"}, nil)
				}
			}
		} else if proxied {
			ex.ReachedVia, ex.Gateway = ViaProxy, d.proxy
			if sv := c.svcByKey[d.proxy]; sv != nil {
				ex.Verdict = e.hop(w, sv).verdict
			}
		}
		for _, t := range x.selfGW {
			for _, cr := range t.creds {
				if !has(ex.GatewayCreds, cr) {
					ex.GatewayCreds = append(ex.GatewayCreds, cr)
				}
			}
		}
		if len(x.gw) > 0 {
			// Prefer the route that requires authentication and only reads.
			best := x.gw[0]
			for _, t := range x.gw[1:] {
				if (t.auth && t.readOnly) && !(best.auth && best.readOnly) || t.auth && !best.auth {
					best = t
				}
			}
			gw := x.gwSvc[best.route]
			gwLabel := ""
			if gw != nil {
				gwLabel = svcLabel(gw)
			}
			ex.GatewayCreds = union(ex.GatewayCreds, best.creds)
			// The agent is configured to call it (or was seen calling it)
			// directly: that is how it is reached, whatever routes exist.
			if x.direct && len(x.obsVia) == 0 {
				ex.GatewayRoute = gwLabel + " route " + best.route
			} else {
				ex.ReachedVia, ex.Route, ex.Authn, ex.ReadOnly, ex.Verdict, ex.Gateway = ViaGateway, best.route, best.auth, best.readOnly, Allowed, gwLabel
			}
		}
		if ex.Verdict == Denied && ex.ReachedVia != ViaGateway {
			ex.ReachedVia = ViaBlocked
		}
		ex.Credentials = credsFor(h, d, x.hd)
		readOnlyCreds := len(ex.Credentials) > 0
		for _, cr := range ex.Credentials {
			if !readOnlyRe.MatchString(cr) {
				readOnlyCreds = false
			}
		}
		// Write: nothing on the way stops it, and it is not read-only.
		ex.Write = ex.Verdict == Allowed && !(ex.ReachedVia == ViaGateway && ex.ReadOnly && direct != Allowed)
		ex.Risk, ex.Why = externalRisk(ex, readOnlyCreds)
		out = append(out, ex)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if riskRank[a.Risk] != riskRank[b.Risk] {
			return riskRank[a.Risk] < riskRank[b.Risk]
		}
		if extRank[a.Class] != extRank[b.Class] {
			return extRank[a.Class] < extRank[b.Class]
		}
		return a.Host < b.Host
	})
	return out
}

var extRank = map[string]int{"exfil": 0, "metadata": 1, "registry": 2, "proxy": 3, "storage": 4, "git": 5, "llm": 6, "unknown": 7}

func noProxy(h string, list []string) bool {
	for _, n := range list {
		n = strings.TrimPrefix(n, ".")
		if n == "" {
			continue
		}
		if h == n || strings.HasSuffix(h, "."+n) {
			return true
		}
		if _, cidr, err := net.ParseCIDR(n); err == nil {
			if ip := net.ParseIP(h); ip != nil && cidr.Contains(ip) {
				return true
			}
		}
	}
	return false
}

func externalRisk(ex External, readOnlyCreds bool) (string, string) {
	sensitive := ex.Fetcher || ex.Class == "storage"
	switch {
	case ex.ReachedVia == ViaNode:
		return "grey", "image pulls by the node, not agent traffic"
	case ex.Verdict == Denied:
		return "grey", "blocked by NetworkPolicy"
	case ex.Verdict == Unknown:
		return "amber", "could not be evaluated from the capture"
	case ex.Class == "exfil":
		return "red", "takes data from anyone and shows it to anyone: an exfiltration channel"
	case ex.Class == "metadata":
		return "red", "hands out the node's cloud credentials"
	case sensitive && ex.WriteSeen:
		return "red", "writes were seen: " + strings.Join(ex.Methods, ", ")
	case sensitive && len(ex.Credentials) > 0 && !readOnlyCreds:
		return "red", "holds a credential for it that probably allows writes"
	case sensitive && ex.Write:
		if ex.Fetcher {
			return "red", "fetches from the internet on request, and nothing limits the methods"
		}
		return "red", "a store the agent can write to: exfiltration and a shared channel"
	case ex.ReachedVia == ViaGateway && ex.Authn && (ex.ReadOnly || ex.Class == "llm") && ex.DirectEgress != Allowed:
		return "green", "through the gateway, which requires authentication"
	case ex.ReachedVia == ViaGateway && ex.DirectEgress == Allowed:
		return "amber", "through the gateway, but direct egress is open too, so the gateway can be bypassed"
	case ex.ReachedVia == ViaGateway && !ex.Authn:
		return "amber", "through the gateway, with no authentication on the route"
	case ex.ReachedVia == ViaGateway:
		return "green", "through the gateway"
	case ex.ReachedVia == ViaDirect:
		return "amber", "direct egress: nothing in the cluster sees or limits these calls"
	}
	return "amber", "reached through " + ex.ReachedVia
}

// neededHosts are the external hosts an agent is configured for or called,
// the list an egress allowlist should hold.
func neededHosts(a Agent) []string {
	var out []string
	for _, x := range a.External {
		if x.ReachedVia == ViaNode || x.ReachedVia == ViaGateway || strings.HasPrefix(x.Host, "any host") {
			continue // the gateway reaches those, not the agent's own egress
		}
		configured := false
		for _, wh := range x.DeclaredIn {
			if !strings.HasPrefix(wh, "image ") {
				configured = true
			}
		}
		if configured || x.Observed > 0 {
			out = append(out, x.Host)
		}
	}
	sort.Strings(out)
	return out
}

// externalPaths are the chains out of the cluster that go through an
// external fetcher, and the exfiltration hosts the agent can reach.
func externalPaths(w *workload, a Agent) []Path {
	var out []Path
	me := w.ns + "/" + w.name
	for _, x := range a.External {
		if x.Verdict != Allowed || x.ReachedVia == ViaNode {
			continue
		}
		hops := []string{me}
		if x.ReachedVia == ViaGateway && x.Gateway != "" {
			hops = append(hops, x.Gateway)
		}
		switch {
		case x.Fetcher:
			out = append(out, Path{Hops: append(hops, x.Host, "internet"), Target: "internet", Kind: "registry",
				Why: x.Host + " is " + article(x.Product) + " outside the cluster that fetches from the internet on request (remote repositories, URL import, proxying), so an SSRF there lends the agent its reach"})
		case x.Class == "exfil":
			out = append(out, Path{Hops: append(hops, x.Host), Target: x.Host, Kind: "exfil", Why: x.Product + ": whatever the agent sends there, anyone can read"})
		}
	}
	return out
}

func article(s string) string {
	if s == "" {
		return "a service"
	}
	if strings.ContainsAny(strings.ToLower(s[:1]), "aeiou") {
		return "an " + s
	}
	return "a " + s
}

// ---------- findings ----------

func (e *engine) externalFindings(w *workload, a Agent) []Finding {
	c := e.c
	var out []Finding
	gws := c.agentgateways()
	selfGW := w.agentType == "agentgateway"
	var fetch, exfil, direct, creds, unexpected []External
	for _, x := range a.External {
		if x.ReachedVia == ViaNode || x.Verdict == Denied {
			continue
		}
		configured := false
		for _, wh := range x.DeclaredIn {
			if !strings.HasPrefix(wh, "image ") {
				configured = true
			}
		}
		lockedGW := x.ReachedVia == ViaGateway && x.Authn && x.ReadOnly && x.DirectEgress != Allowed
		if x.Fetcher && x.Verdict == Allowed && !lockedGW && !strings.HasPrefix(x.Host, "any host") || strings.HasPrefix(x.Host, "any host") && !selfGW {
			fetch = append(fetch, x)
		}
		if x.Class == "exfil" && x.Verdict != Denied && (configured || x.Observed > 0 || x.Lookups > 0) {
			exfil = append(exfil, x)
		}
		if len(gws) > 0 && !selfGW && x.Verdict == Allowed && (x.ReachedVia == ViaDirect || x.ReachedVia == ViaProxy) && (configured || x.Observed > 0) {
			direct = append(direct, x)
		}
		if (x.Fetcher || x.Class == "git" || x.Class == "storage") && len(x.Credentials) > 0 && x.Verdict == Allowed {
			ro := true
			for _, cr := range x.Credentials {
				if !readOnlyRe.MatchString(cr) {
					ro = false
				}
			}
			if !ro {
				creds = append(creds, x)
			}
		}
		if (x.Observed > 0 || x.Lookups > 0) && !configured && len(x.DeclaredIn) == 0 {
			unexpected = append(unexpected, x)
		}
	}
	item := func(x External) FindingItem {
		via := x.ReachedVia
		if x.Gateway != "" {
			via += " (" + x.Gateway + ")"
		}
		note := "reached " + via
		if len(x.Credentials) > 0 {
			note += "; holds " + strings.Join(x.Credentials, ", ")
		}
		if x.WriteSeen {
			note += "; writes seen: " + strings.Join(x.Methods, ",")
		}
		return FindingItem{Target: x.Host, Class: x.Product, Note: note}
	}
	if len(fetch) > 0 {
		first := fetch[0]
		sev := "high"
		for _, x := range fetch {
			if x.Write || x.WriteSeen || len(x.Credentials) > 0 {
				sev = "critical"
			}
		}
		title := "Can reach an external registry that fetches from the internet: " + first.Host
		if len(fetch) > 1 {
			title += fmt.Sprintf(" and %d more", len(fetch)-1)
		}
		var items []FindingItem
		for _, x := range fetch {
			items = append(items, item(x))
		}
		detail := fmt.Sprintf("%s can call %s, %s outside the cluster. A registry like this fetches from the internet on request (remote repositories, URL import, proxying), so an SSRF or a remote repository there lends the agent its reach, however tightly the agent's own egress is locked down. In the OpenAI and Hugging Face incident the agents used exactly this: the registry accepted PUT, then MKCOL over WebDAV, and fetched for them.", w.name, first.Host, article(first.Product))
		if first.WriteSeen {
			detail += " Writes were seen in the capture: " + strings.Join(first.Methods, ", ") + "."
		}
		if len(first.Credentials) > 0 {
			detail += " It holds " + strings.Join(first.Credentials, ", ") + "."
		}
		out = append(out, Finding{Severity: sev, Title: title, Detail: detail, Items: items, Source: guide,
			Fix: e.gatewayReadOnlyFix(w, first),
			Advice: []string{
				"Give the agent a read-only token for " + first.Host + ", scoped to the repositories it installs from.",
				"Turn off remote repositories, URL import and WebDAV (MKCOL, PUT) on the registry for that token.",
				"Give the registry its own egress allowlist: only the upstream indexes it really proxies.",
				"Ask the registry's owner: who else can write to it, and what does it fetch on request?",
			}})
	}
	if len(exfil) > 0 {
		var items []FindingItem
		for _, x := range exfil {
			items = append(items, item(x))
		}
		out = append(out, Finding{Severity: "critical", Title: "Can reach paste, webhook or tunnel services",
			Detail: fmt.Sprintf("%s is configured for, or was seen calling, %s. Services like these take data from anyone and show it to anyone, which makes them an exfiltration channel and a way to talk to other agents.", w.name, joinHosts(exfil)),
			Items:  items, Source: guide, Fix: e.denyEgressFix(w, a)})
	}
	if len(direct) > 0 {
		var items []FindingItem
		for _, x := range direct {
			items = append(items, item(x))
		}
		out = append(out, Finding{Severity: "high", Title: "Calls external services directly, not through the gateway",
			Detail: fmt.Sprintf("This cluster runs agentgateway, but %s reaches %s on its own egress, where nothing authenticates, limits or records the calls. Send them through the gateway and allow only the gateway on the way out.", w.name, joinHosts(direct)),
			Items:  items, Source: guide, Fix: e.gatewayReadOnlyFix(w, direct[0])})
	}
	for _, x := range creds {
		out = append(out, Finding{Severity: "high", Title: "Holds a credential for " + x.Host + " that probably allows writes",
			Detail: fmt.Sprintf("%s holds %s for %s (%s), and nothing in the name says it is read-only. A prompt injection can use it to publish packages, push code or write objects that other people then trust.", w.name, strings.Join(x.Credentials, ", "), x.Host, x.Product),
			Items:  []FindingItem{item(x)}, Source: guide,
			Fix:    "# Replace it with a read-only, narrowly scoped token and name it so (for example " + credHint(x) + "),\n# or have agentgateway inject the credential so the agent never holds it:\n" + e.gatewayReadOnlyFix(w, x),
			Advice: []string{"Issue a read-only token (pull or read scope) for the agent.", "Prefer short-lived tokens from agentgateway token exchange to static keys in the pod."}})
	}
	for _, x := range a.External {
		if x.ReachedVia != ViaGateway || x.Authn || len(x.GatewayCreds) == 0 || selfGW || x.Class == "llm" {
			continue
		}
		group, pkind := "agentgateway.dev", "AgentgatewayPolicy"
		if c.enterprise {
			group, pkind = "enterpriseagentgateway.solo.io", "EnterpriseAgentgatewayPolicy"
		}
		rt := x.Route
		rns, rname := "<namespace>", rt
		if i := strings.Index(rt, "/"); i > 0 {
			rns, rname = rt[:i], rt[i+1:]
		}
		out = append(out, Finding{Severity: "high", Title: "Can use the gateway's credential for " + x.Host + " without authenticating",
			Detail: fmt.Sprintf("%s reaches %s through %s on route %s, which asks for no authentication, and the gateway adds %s on the way. Anything that can reach the gateway borrows that credential, with whatever it allows.", w.name, x.Host, x.Gateway, x.Route, strings.Join(x.GatewayCreds, ", ")),
			Items:  []FindingItem{item(x)}, Source: guide,
			Fix: fmt.Sprintf("apiVersion: %s/v1alpha1\nkind: %s\nmetadata: {name: %s-authn, namespace: %s}\nspec:\n  targetRefs:\n  - {group: gateway.networking.k8s.io, kind: HTTPRoute, name: %s}\n  traffic:\n    jwtAuthentication:\n      mode: Strict\n      providers:\n      - issuer: <your issuer>\n        audiences: [%s]\n        jwks: {remote: {jwksPath: /.well-known/jwks.json, backendRef: {kind: Service, name: <idp>, namespace: <idp namespace>, port: 443}}}",
				group, pkind, resourceName(rname), rns, rname, resourceName(rname))})
	}
	if len(unexpected) > 0 {
		var items []FindingItem
		for _, x := range unexpected {
			it := item(x)
			it.Note = fmt.Sprintf("seen %d times by %s", x.Observed+x.Lookups, strings.Join(x.Sources, ", "))
			items = append(items, it)
		}
		out = append(out, Finding{Severity: "medium", Title: "Reaches external hosts it is not configured for",
			Detail: fmt.Sprintf("%s called %s, which nothing in its configuration names. Either it needs them and they should be in its allowlist, or something made it call them.", w.name, joinHosts(unexpected)),
			Items:  items, Source: guide, Fix: e.denyEgressFix(w, a)})
	}
	return out
}

func joinHosts(xs []External) string {
	var h []string
	for _, x := range xs {
		h = append(h, x.Host)
	}
	return strings.Join(firstN(h, 5), ", ")
}

func credHint(x External) string {
	p := strings.ToUpper(strings.SplitN(hostTokensOr(x.Host), ".", 2)[0])
	return p + "_READ_TOKEN"
}

func hostTokensOr(h string) string {
	if t := hostTokens(h); len(t) > 0 {
		return t[0]
	}
	return "REGISTRY"
}

func resourceName(h string) string {
	r := regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(strings.ToLower(h), "-")
	r = strings.Trim(r, "-")
	if len(r) > 50 {
		r = r[:50]
	}
	return r
}

// gatewayReadOnlyFix routes an external host through agentgateway with a
// GET/HEAD-only rule and a path allowlist, and denies the agent's direct
// egress. It uses the Enterprise kinds when the capture does.
func (e *engine) gatewayReadOnlyFix(w *workload, x External) string {
	c := e.c
	group, bkind, pkind := "agentgateway.dev", "AgentgatewayBackend", "AgentgatewayPolicy"
	other := "enterpriseagentgateway.solo.io/v1alpha1 EnterpriseAgentgatewayBackend / EnterpriseAgentgatewayPolicy"
	if c.enterprise {
		group, bkind, pkind = "enterpriseagentgateway.solo.io", "EnterpriseAgentgatewayBackend", "EnterpriseAgentgatewayPolicy"
		other = "agentgateway.dev/v1alpha1 AgentgatewayBackend / AgentgatewayPolicy on OSS"
	}
	gwName, gwNS := "<agentgateway>", "agentgateway-system"
	var gwSvc *service
	if gws := c.agentgateways(); len(gws) > 0 {
		gwSvc = gws[0]
		for _, g := range gws {
			if x.Gateway == svcLabel(g) {
				gwSvc = g
			}
		}
		gwName, gwNS = gwSvc.gatewayNm, gwSvc.gatewayNS
	}
	name := resourceName(x.Host)
	prefix := "/"
	if len(x.Paths) > 0 {
		prefix = pathPrefix(x.Paths[0])
	}
	for _, p := range e.hostPaths(w, x.Host) {
		prefix = pathPrefix(p)
		break
	}
	port := 443
	var b strings.Builder
	// A route to it exists already: make that route read-only rather than
	// adding a second one.
	route := x.Route
	if route == "" && x.GatewayRoute != "" {
		if i := strings.LastIndex(x.GatewayRoute, " route "); i >= 0 {
			route = x.GatewayRoute[i+len(" route "):]
		}
	}
	if i := strings.Index(route, "/"); i > 0 {
		rns, rname := route[:i], route[i+1:]
		cel := `request.method in ["GET", "HEAD"]`
		if prefix != "/" {
			cel += ` && request.path.startsWith("` + prefix + `")`
		}
		fmt.Fprintf(&b, "# %s already has a route on the gateway (%s): allow only reads on it,\n# and stop %s calling it directly. (or %s)\n", x.Host, route, w.name, other)
		fmt.Fprintf(&b, "apiVersion: %s/v1alpha1\nkind: %s\nmetadata: {name: %s-read-only, namespace: %s}\nspec:\n  targetRefs:\n  - {group: gateway.networking.k8s.io, kind: HTTPRoute, name: %s}\n  traffic:\n    authorization:\n      action: Allow\n      policy:\n        matchExpressions:\n        - '%s'\n---\n", group, pkind, resourceName(rname), rns, rname, cel)
		if x.ReachedVia != ViaGateway {
			b.WriteString("# point the agent at the gateway instead, e.g. http://" + gwName + "." + gwNS + ".svc" + prefix + "\n")
		}
		b.WriteString(e.gatewayOnlyEgress(w, gwSvc))
		return b.String()
	}
	fmt.Fprintf(&b, "# Send %s through agentgateway, read-only, and stop %s calling it directly.\n# (or %s)\n", x.Host, w.name, other)
	fmt.Fprintf(&b, "apiVersion: %s/v1alpha1\nkind: %s\nmetadata: {name: %s, namespace: %s}\nspec:\n  static: {host: %s, port: %d}\n  policies:\n    tls: {sni: %s}\n---\n", group, bkind, name, gwNS, x.Host, port, x.Host)
	fmt.Fprintf(&b, "apiVersion: gateway.networking.k8s.io/v1\nkind: HTTPRoute\nmetadata: {name: %s, namespace: %s}\nspec:\n  parentRefs: [{name: %s}]\n  rules:\n  - matches: [{path: {type: PathPrefix, value: %s}}]\n    backendRefs: [{group: %s, kind: %s, name: %s, port: %d}]\n---\n", name, gwNS, yamlStr(gwName), yamlStr(prefix), group, bkind, name, port)
	cel := `request.method in ["GET", "HEAD"]`
	if prefix != "/" {
		cel += ` && request.path.startsWith("` + prefix + `")`
	}
	fmt.Fprintf(&b, "apiVersion: %s/v1alpha1\nkind: %s\nmetadata: {name: %s-read-only, namespace: %s}\nspec:\n  targetRefs:\n  - {group: gateway.networking.k8s.io, kind: HTTPRoute, name: %s}\n  traffic:\n    authorization:\n      action: Allow\n      policy:\n        matchExpressions:\n        - '%s'\n    # add jwtAuthentication so only named agents can use the route\n---\n", group, pkind, name, gwNS, name, cel)
	b.WriteString("# point the agent at the gateway instead, e.g. http://" + gwName + "." + gwNS + ".svc" + prefix + "\n")
	b.WriteString(e.gatewayOnlyEgress(w, gwSvc))
	return b.String()
}

// hostPaths: the paths the agent is configured to use on a host.
func (e *engine) hostPaths(w *workload, host string) []string {
	if hd := e.c.declarations(w).hosts[host]; hd != nil {
		return hd.paths
	}
	return nil
}

// pathPrefix keeps the first three segments: /artifactory/api/pypi/pypi/simple -> /artifactory/api/pypi/.
func pathPrefix(p string) string {
	segs := strings.Split(strings.Trim(p, "/"), "/")
	if len(segs) == 0 || segs[0] == "" {
		return "/"
	}
	if len(segs) > 3 {
		segs = segs[:3]
	}
	return "/" + strings.Join(segs, "/") + "/"
}

// gatewayOnlyEgress: the agent may reach cluster DNS and the gateway, nothing else.
func (e *engine) gatewayOnlyEgress(w *workload, gw *service) string {
	if gw != nil {
		return e.egressPolicy(w, []*service{gw}, nil)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "apiVersion: networking.k8s.io/v1\nkind: NetworkPolicy\nmetadata: {name: %s-egress, namespace: %s}\nspec:\n", w.name, w.ns)
	b.WriteString(podSelectorYAML(w, "  ") + "\n")
	b.WriteString("  policyTypes: [Egress]\n  egress:\n  - to:   # cluster DNS\n    - namespaceSelector: {matchLabels: {kubernetes.io/metadata.name: kube-system}}\n      podSelector: {matchLabels: {k8s-app: kube-dns}}\n    ports: [{port: 53, protocol: UDP}, {port: 53, protocol: TCP}]\n")
	b.WriteString("  - to:   # the agentgateway pods, once installed\n    - namespaceSelector: {matchLabels: {kubernetes.io/metadata.name: agentgateway-system}}\n      podSelector: {matchLabels: {gateway.networking.k8s.io/gateway-name: agentgateway}}\n")
	return strings.TrimRight(b.String(), "\n")
}

// denyEgressFix is the agent's egress allowlist without the hosts at issue.
func (e *engine) denyEgressFix(w *workload, a Agent) string {
	var gw *service
	if gws := e.c.agentgateways(); len(gws) > 0 {
		gw = gws[0]
	}
	return "# No egress to anything outside the cluster except through the gateway.\n" + e.gatewayOnlyEgress(w, gw)
}
