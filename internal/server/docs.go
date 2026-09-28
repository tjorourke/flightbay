package server

import (
	"bytes"
	"encoding/json"
	"html/template"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	ghtml "github.com/yuin/goldmark/renderer/html"
	"sigs.k8s.io/yaml"

	"github.com/tjorourke/flightbay/docs"
)

// The documentation is embedded and served at /docs, so it works offline in
// the desk. The same Markdown builds the public Hugo site (docs/site). Pages
// hold no snapshot data, so they are served without the workspace token.

type docPage struct {
	Path        string // URL path under /docs, e.g. "desk/logs/" ("" is the home page)
	File        string
	Title       string
	Description string
	Weight      int
	Section     bool
	Body        string // markdown without front matter
	Text        string // plain text for search
}

type docSite struct {
	once   sync.Once
	pages  map[string]*docPage
	order  []*docPage
	md     goldmark.Markdown
	search []byte
}

var docsSite docSite

var frontMatter = regexp.MustCompile(`(?s)\A---\n(.*?)\n---\n?`)

func (d *docSite) load() {
	d.once.Do(func() {
		d.pages = map[string]*docPage{}
		d.md = goldmark.New(goldmark.WithExtensions(extension.GFM), goldmark.WithParserOptions(parser.WithAutoHeadingID()), goldmark.WithRendererOptions(ghtml.WithUnsafe()))
		_ = fs.WalkDir(docs.FS, "site/content", func(p string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() || !strings.HasSuffix(p, ".md") {
				return nil
			}
			raw, _ := docs.FS.ReadFile(p)
			rel := strings.TrimPrefix(p, "site/content/")
			pg := &docPage{File: rel}
			if m := frontMatter.FindSubmatch(raw); m != nil {
				var fm struct {
					Title       string `json:"title"`
					Description string `json:"description"`
					Weight      int    `json:"weight"`
				}
				_ = yaml.Unmarshal(m[1], &fm)
				pg.Title, pg.Description, pg.Weight = fm.Title, fm.Description, fm.Weight
				raw = raw[len(m[0]):]
			}
			pg.Body = string(raw)
			// A page may repeat its title as a first heading; the layout already shows it.
			if t := strings.TrimLeft(pg.Body, "\n"); strings.HasPrefix(t, "# ") {
				if nl := strings.Index(t, "\n"); nl > 0 && strings.EqualFold(strings.TrimSpace(t[2:nl]), strings.TrimSpace(pg.Title)) {
					pg.Body = t[nl+1:]
				}
			}
			switch {
			case rel == "_index.md":
				pg.Path, pg.Section = "", true
			case strings.HasSuffix(rel, "/_index.md"):
				pg.Path, pg.Section = strings.TrimSuffix(rel, "_index.md"), true
			default:
				pg.Path = strings.TrimSuffix(rel, ".md") + "/"
			}
			if pg.Title == "" {
				pg.Title = strings.Trim(path.Base(strings.TrimSuffix(pg.Path, "/")), "/")
			}
			pg.Text = plainText(pg.Body)
			d.pages[pg.Path] = pg
			d.order = append(d.order, pg)
			return nil
		})
		sort.SliceStable(d.order, func(i, j int) bool { return d.less(d.order[i], d.order[j]) })
		type entry struct {
			Path, Title, Description, Text string
		}
		var idx []entry
		for _, p := range d.order {
			t := p.Text
			if len(t) > 6000 {
				t = t[:6000]
			}
			idx = append(idx, entry{"/docs/" + p.Path, p.Title, p.Description, t})
		}
		d.search, _ = json.Marshal(idx)
	})
}

func (d *docSite) parent(p *docPage) string {
	if p.Path == "" {
		return "-"
	}
	dir := strings.TrimSuffix(p.Path, "/")
	if i := strings.LastIndex(dir, "/"); i >= 0 {
		return dir[:i+1]
	}
	return ""
}

func (d *docSite) less(a, b *docPage) bool {
	if a.Weight != b.Weight {
		wa, wb := a.Weight, b.Weight
		if wa == 0 {
			wa = 1000
		}
		if wb == 0 {
			wb = 1000
		}
		if wa != wb {
			return wa < wb
		}
	}
	return a.Title < b.Title
}

var mdStrip = regexp.MustCompile("(?m)```.*$|[#*`>|_\\[\\]()]|!\\[[^\\]]*\\]\\([^)]*\\)")

func plainText(md string) string {
	return strings.Join(strings.Fields(mdStrip.ReplaceAllString(md, " ")), " ")
}

// children lists the pages directly under a section, in sidebar order.
func (d *docSite) children(section string) []*docPage {
	var out []*docPage
	for _, p := range d.order {
		if p.Path != section && d.parent(p) == section {
			out = append(out, p)
		}
	}
	return out
}

var linkRe = regexp.MustCompile(`(href|src)="/`)

type navItem struct {
	Title, URL string
	Active     bool
	Open       bool
	Children   []navItem
}

func (d *docSite) nav(cur string) []navItem {
	var build func(section string) []navItem
	build = func(section string) []navItem {
		var out []navItem
		for _, p := range d.children(section) {
			it := navItem{Title: p.Title, URL: "/docs/" + p.Path, Active: p.Path == cur}
			if p.Section {
				it.Children = build(p.Path)
				it.Open = strings.HasPrefix(cur, p.Path)
			}
			out = append(out, it)
		}
		return out
	}
	return build("")
}

func (s *Server) docs(w http.ResponseWriter, r *http.Request) {
	d := &docsSite
	d.load()
	p := strings.TrimPrefix(r.URL.Path, "/docs/")
	if r.URL.Path == "/docs" {
		http.Redirect(w, r, "/docs/", http.StatusFound)
		return
	}
	if strings.HasPrefix(p, "images/") {
		b, err := docs.FS.ReadFile(p)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", mime.TypeByExtension(path.Ext(p)))
		w.Header().Set("Cache-Control", "max-age=3600")
		_, _ = w.Write(b)
		return
	}
	if p == "search.json" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(d.search)
		return
	}
	if p != "" && !strings.HasSuffix(p, "/") {
		http.Redirect(w, r, "/docs/"+p+"/", http.StatusFound)
		return
	}
	pg := d.pages[p]
	status := 200
	if pg == nil {
		status = 404
		pg = &docPage{Title: "Page not found", Body: "No page at `/docs/" + p + "`. Use the menu or search."}
	}
	var body bytes.Buffer
	_ = d.md.Convert([]byte(pg.Body), &body)
	html := linkRe.ReplaceAllString(body.String(), `$1="/docs/`)
	var list []*docPage
	if pg.Section {
		list = d.children(pg.Path)
	}
	// Previous and next in reading order.
	var prev, next *docPage
	for i, x := range d.order {
		if x == pg {
			if i > 0 {
				prev = d.order[i-1]
			}
			if i+1 < len(d.order) {
				next = d.order[i+1]
			}
		}
	}
	crumbs := []navItem{{Title: "Docs", URL: "/docs/"}}
	acc := ""
	for _, part := range strings.Split(strings.TrimSuffix(pg.Path, "/"), "/") {
		if part == "" {
			continue
		}
		acc += part + "/"
		if x := d.pages[acc]; x != nil && x != pg {
			crumbs = append(crumbs, navItem{Title: x.Title, URL: "/docs/" + x.Path})
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(status)
	_ = docTmpl.Execute(w, map[string]any{
		"Page": pg, "HTML": template.HTML(html), "Nav": d.nav(pg.Path), "List": list, "Prev": prev, "Next": next, "Crumbs": crumbs,
		"Source": "https://github.com/tjorourke/flightbay/blob/main/docs/site/content/" + pg.File, "Year": strconv.Itoa(2026),
	})
}

var docTmpl = template.Must(template.New("doc").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Page.Title}} · Flightbay docs</title>
<link rel="icon" href="/favicon.svg" type="image/svg+xml">
<link rel="icon" href="/favicon-32.png" type="image/png" sizes="32x32">
<link rel="apple-touch-icon" href="/apple-touch-icon.png">
<script>document.documentElement.dataset.theme = localStorage.getItem('flightbay-theme') || 'dark';</script>
<style>
:root{--bg:#101316;--panel:#161a1e;--raised:#1c2126;--line:#2a3036;--text:#e1e6ea;--muted:#939ea8;--teal:#72d6be;--red:#f18e92;--amber:#efbe72;--mono:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;--sans:Inter,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}
[data-theme=light]{--bg:#f6f7f8;--panel:#fff;--raised:#f0f2f4;--line:#dfe3e7;--text:#1d262d;--muted:#5d6b76;--teal:#0f7f6a}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font:15px/1.65 var(--sans)}
a{color:var(--teal);text-decoration:none}a:hover{text-decoration:underline}
.top{position:sticky;top:0;z-index:5;display:flex;align-items:center;gap:18px;padding:12px 24px;background:var(--panel);border-bottom:1px solid var(--line)}
.brand{font-weight:700;font-size:17px;color:var(--text)}.brand b{color:var(--teal)}.brand small{color:var(--muted);font-weight:500;margin-left:6px}
.top input{flex:1;max-width:460px;background:var(--raised);border:1px solid var(--line);color:var(--text);border-radius:6px;padding:8px 12px;font:14px var(--sans)}
.top .sp{flex:1}.top button{background:var(--raised);border:1px solid var(--line);color:var(--text);border-radius:6px;padding:7px 11px;cursor:pointer}
.wrap{display:grid;grid-template-columns:280px minmax(0,1fr);max-width:1400px;margin:0 auto}
nav.side{position:sticky;top:57px;height:calc(100vh - 57px);overflow:auto;padding:20px 12px 40px 20px;border-right:1px solid var(--line);font-size:14px}
nav.side ul{list-style:none;margin:0;padding:0}nav.side ul ul{padding-left:12px;border-left:1px solid var(--line);margin:2px 0 6px 6px}
nav.side a{display:block;padding:4px 8px;border-radius:5px;color:var(--text)}nav.side a:hover{background:var(--raised);text-decoration:none}
nav.side a.on{background:color-mix(in srgb,var(--teal) 16%,transparent);color:var(--teal);font-weight:600}
nav.side .sec>a{font-weight:600}
main{padding:28px 48px 80px;min-width:0;max-width:980px}
.crumbs{font-size:13px;color:var(--muted);margin-bottom:6px}.crumbs a{color:var(--muted)}
h1{font-size:32px;line-height:1.2;margin:0 0 8px}.lede{color:var(--muted);font-size:17px;margin:0 0 26px}
main h2{font-size:22px;margin:36px 0 10px;padding-top:6px;border-top:1px solid var(--line)}main h3{font-size:17px;margin:26px 0 8px}
main code{font:13px var(--mono);background:var(--raised);padding:2px 5px;border-radius:4px}
main pre{background:var(--panel);border:1px solid var(--line);border-radius:8px;padding:14px 16px;overflow:auto;position:relative}
main pre code{background:none;padding:0;font-size:13px;line-height:1.55}
main pre button{position:absolute;top:8px;right:8px;font:11px var(--sans);background:var(--raised);color:var(--muted);border:1px solid var(--line);border-radius:5px;padding:3px 8px;cursor:pointer;opacity:0}
main pre:hover button{opacity:1}
main table{border-collapse:collapse;width:100%;margin:14px 0;font-size:14px;display:block;overflow:auto}
main th,main td{border:1px solid var(--line);padding:7px 10px;text-align:left;vertical-align:top}main th{background:var(--raised)}
main img{max-width:100%;border:1px solid var(--line);border-radius:8px;margin:10px 0}
main blockquote{margin:14px 0;padding:8px 16px;border-left:3px solid var(--teal);background:var(--panel);color:var(--muted)}
.cards{display:grid;grid-template-columns:repeat(auto-fill,minmax(260px,1fr));gap:12px;margin:24px 0}
.cards a{display:block;padding:14px 16px;border:1px solid var(--line);border-radius:8px;background:var(--panel);color:var(--text)}
.cards a:hover{border-color:var(--teal);text-decoration:none}.cards b{display:block;color:var(--teal);margin-bottom:4px}.cards span{color:var(--muted);font-size:13.5px}
.pn{display:flex;justify-content:space-between;gap:12px;margin-top:48px;padding-top:18px;border-top:1px solid var(--line);font-size:14px}
.pn small{display:block;color:var(--muted)}.foot{margin-top:28px;font-size:12.5px;color:var(--muted)}
#results{position:absolute;top:52px;left:24px;right:24px;max-width:640px;margin-left:300px;background:var(--panel);border:1px solid var(--line);border-radius:8px;box-shadow:0 8px 30px rgba(0,0,0,.3);display:none;max-height:70vh;overflow:auto}
#results a{display:block;padding:10px 14px;border-bottom:1px solid var(--line);color:var(--text)}#results a:hover,#results a.sel{background:var(--raised);text-decoration:none}
#results small{display:block;color:var(--muted);font-size:12.5px;margin-top:2px}#results mark{background:color-mix(in srgb,var(--teal) 30%,transparent);color:inherit}
@media (max-width:900px){.wrap{grid-template-columns:1fr}nav.side{display:none}main{padding:20px}#results{margin-left:0}}
</style></head><body>
<header class="top"><a class="brand" href="/docs/"><b>///</b> flightbay <small>docs</small></a>
<input id="q" placeholder="Search the docs ( / )" autocomplete="off" aria-label="Search the docs"><div id="results"></div>
<span class="sp"></span><a href="/">Back to the desk ↗</a><button id="theme" title="Light or dark">◐</button></header>
<div class="wrap"><nav class="side" aria-label="Documentation">
{{define "items"}}<ul>{{range .}}<li class="{{if .Children}}sec{{end}}"><a href="{{.URL}}" class="{{if .Active}}on{{end}}">{{.Title}}</a>{{if .Children}}{{template "items" .Children}}{{end}}</li>{{end}}</ul>{{end}}
<ul><li><a href="/docs/" class="{{if eq .Page.Path ""}}on{{end}}">Introduction</a></li></ul>
{{template "items" .Nav}}</nav>
<main>
{{if gt (len .Crumbs) 1}}<div class="crumbs">{{range $i, $c := .Crumbs}}{{if $i}} / {{end}}<a href="{{$c.URL}}">{{$c.Title}}</a>{{end}}</div>{{end}}
<h1>{{.Page.Title}}</h1>{{with .Page.Description}}<p class="lede">{{.}}</p>{{end}}
{{.HTML}}
{{if .List}}<div class="cards">{{range .List}}<a href="/docs/{{.Path}}"><b>{{.Title}}</b><span>{{.Description}}</span></a>{{end}}</div>{{end}}
<div class="pn"><div>{{with .Prev}}<a href="/docs/{{.Path}}"><small>Previous</small>{{.Title}}</a>{{end}}</div><div style="text-align:right">{{with .Next}}<a href="/docs/{{.Path}}"><small>Next</small>{{.Title}}</a>{{end}}</div></div>
<p class="foot">Flightbay by Solo · <a href="{{.Source}}">Edit this page</a></p>
</main></div>
<script>
document.getElementById('theme').onclick = () => { const t = document.documentElement.dataset.theme === 'light' ? 'dark' : 'light'; document.documentElement.dataset.theme = t; localStorage.setItem('flightbay-theme', t); };
document.querySelectorAll('main pre').forEach(p => { const b = document.createElement('button'); b.textContent = 'Copy'; b.onclick = () => navigator.clipboard.writeText(p.innerText.replace(/Copy$/, '').trim()).then(() => { b.textContent = 'Copied'; setTimeout(() => b.textContent = 'Copy', 1200); }); p.appendChild(b); });
const q = document.getElementById('q'), res = document.getElementById('results');
let idx = null, sel = 0, hits = [];
const esc = s => s.replace(/[&<>"]/g, c => ({'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;'}[c]));
const load = () => idx ? Promise.resolve(idx) : fetch('/docs/search.json').then(r => r.json()).then(j => idx = j);
function run() {
  const t = q.value.trim().toLowerCase();
  if (!t) { res.style.display = 'none'; return; }
  load().then(ix => {
    const words = t.split(/\s+/);
    hits = ix.map(p => { const hay = (p.Title + ' ' + p.Description + ' ' + p.Text).toLowerCase(); if (!words.every(w => hay.includes(w))) return null;
      const score = words.reduce((s, w) => s + (p.Title.toLowerCase().includes(w) ? 10 : 0) + (p.Description.toLowerCase().includes(w) ? 3 : 0), 0);
      const i = p.Text.toLowerCase().indexOf(words[0]); const snip = i < 0 ? p.Description : '…' + p.Text.slice(Math.max(0, i - 60), i + 120) + '…';
      return {p, score, snip}; }).filter(Boolean).sort((a, b) => b.score - a.score).slice(0, 12);
    sel = 0;
    const hl = s => { let o = esc(s); for (const w of words) o = o.replace(new RegExp(w.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'), 'gi'), m => '<mark>' + m + '</mark>'); return o; };
    res.innerHTML = hits.map((h, i) => '<a href="' + h.p.Path + '" class="' + (i === sel ? 'sel' : '') + '">' + hl(h.p.Title) + '<small>' + hl(h.snip) + '</small></a>').join('') || '<a>No matches</a>';
    res.style.display = 'block';
  });
}
q.oninput = run;
q.onkeydown = e => { if (e.key === 'Enter' && hits[sel]) location.href = hits[sel].p.Path; else if (e.key === 'Escape') { q.value = ''; run(); q.blur(); }
  else if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); sel = (sel + (e.key === 'ArrowDown' ? 1 : hits.length - 1)) % Math.max(1, hits.length); [...res.children].forEach((a, i) => a.classList.toggle('sel', i === sel)); } };
document.addEventListener('keydown', e => { if (e.key === '/' && document.activeElement !== q) { e.preventDefault(); q.focus(); } });
document.addEventListener('click', e => { if (!res.contains(e.target) && e.target !== q) res.style.display = 'none'; });
document.querySelector('nav.side a.on')?.scrollIntoView({block: 'center'});
</script></body></html>`))
