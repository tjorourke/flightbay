// Command scorecard-checks regenerates docs/site/content/desk/scorecard-checks.md
// from the check definitions in internal/posture. It reads the spec{...}
// literals and reach.go's mk(...) calls straight from the source, so the page
// can never list a check the code does not have.
//
//	go run ./scripts/scorecard-checks            # rewrite the page
//	go run ./scripts/scorecard-checks -list      # tab-separated dump
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type check struct {
	id, category, severity, title, source, docs, file string
	reach                                             bool
}

func main() {
	dir := flag.String("dir", "internal/posture", "posture package directory")
	docsDir := flag.String("docs-dir", "internal/solodocs", "product documentation link definitions")
	out := flag.String("out", "docs/site/content/desk/scorecard-checks.md", "page to write")
	list := flag.Bool("list", false, "print a tab-separated list instead of writing the page")
	flag.Parse()

	checks, err := read(*dir, *docsDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *list {
		for _, c := range checks {
			fmt.Printf("%s\t%s\t%s\t%s\t%s\t%s\n", c.id, c.file, c.category, c.title, c.source, c.docs)
		}
		return
	}
	if err := os.WriteFile(*out, []byte(render(checks)), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s (%d checks)\n", *out, len(checks))
}

func parseDir(fset *token.FileSet, dir string) ([]*ast.File, []string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, nil, err
	}
	var parsed []*ast.File
	var names []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			return nil, nil, err
		}
		parsed = append(parsed, af)
		names = append(names, filepath.Base(f))
	}
	return parsed, names, nil
}

// collectConsts folds the package's string constants into consts, under
// prefix (so solodocs.Istio is found as "solodocs.Istio"). It repeats until
// nothing new resolves, so a constant built from another one works whatever
// the declaration order.
func collectConsts(parsed []*ast.File, prefix string, consts map[string]string) {
	for changed := true; changed; {
		changed = false
		for _, af := range parsed {
			for _, d := range af.Decls {
				gd, ok := d.(*ast.GenDecl)
				if !ok || gd.Tok != token.CONST {
					continue
				}
				for _, sp := range gd.Specs {
					vs := sp.(*ast.ValueSpec)
					for i, n := range vs.Names {
						if _, done := consts[prefix+n.Name]; done || i >= len(vs.Values) {
							continue
						}
						if v, ok := evalIn(vs.Values[i], consts, prefix); ok {
							consts[prefix+n.Name] = v
							changed = true
						}
					}
				}
			}
		}
	}
}

func read(dir, docsDir string) ([]check, error) {
	fset := token.NewFileSet()
	consts := map[string]string{}
	if docsDir != "" {
		dp, _, err := parseDir(fset, docsDir)
		if err != nil {
			return nil, err
		}
		collectConsts(dp, filepath.Base(docsDir)+".", consts)
	}
	parsed, names, err := parseDir(fset, dir)
	if err != nil {
		return nil, err
	}
	collectConsts(parsed, "", consts)
	var out []check
	for i, af := range parsed {
		file := names[i]
		var reachSource, reachErr string
		ast.Inspect(af, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CompositeLit:
				if id, ok := x.Type.(*ast.Ident); !ok || id.Name != "spec" || len(x.Elts) < 7 {
					return true
				}
				v := make([]string, len(x.Elts))
				for j, e := range x.Elts {
					s, ok := eval(e, consts)
					if !ok && j != 5 { // the fix text may be built at run time
						reachErr = fmt.Sprintf("%s: cannot read element %d of a spec literal", fset.Position(e.Pos()), j)
					}
					v[j] = s
				}
				c := check{id: v[0], category: v[1], severity: v[2], title: v[3], source: v[6], file: file}
				if len(v) > 7 {
					c.docs = v[7]
				}
				out = append(out, c)
			case *ast.CallExpr:
				// reach.go: mk(id, cat, sev, title, why, fix, docs, eval), all
				// sharing the Source set inside mk.
				if fn, ok := x.Fun.(*ast.Ident); ok && fn.Name == "mk" && len(x.Args) >= 7 {
					v := make([]string, len(x.Args))
					for j, e := range x.Args[:len(x.Args)-1] {
						v[j], _ = eval(e, consts)
					}
					c := check{id: v[0], category: v[1], severity: v[2], title: v[3], file: file, reach: true, source: reachSource}
					if len(x.Args) >= 8 {
						c.docs = v[6]
					}
					out = append(out, c)
				}
			case *ast.KeyValueExpr:
				if k, ok := x.Key.(*ast.Ident); ok && k.Name == "Source" && file == "reach.go" {
					reachSource, _ = eval(x.Value, consts)
				}
			}
			return true
		})
		if reachErr != "" {
			return nil, fmt.Errorf("%s", reachErr)
		}
		for j := range out {
			if out[j].file == file && out[j].reach && out[j].source == "" {
				out[j].source = reachSource
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out, nil
}

// eval folds a string expression made of literals, constants
// (pkg.Name for another package's) and +.
func eval(e ast.Expr, consts map[string]string) (string, bool) { return evalIn(e, consts, "") }

// evalIn is eval inside a package whose own constants are stored under prefix.
func evalIn(e ast.Expr, consts map[string]string, prefix string) (string, bool) {
	eval := func(e ast.Expr, consts map[string]string) (string, bool) { return evalIn(e, consts, prefix) }
	switch x := e.(type) {
	case *ast.SelectorExpr:
		if pkg, ok := x.X.(*ast.Ident); ok {
			s, ok := consts[pkg.Name+"."+x.Sel.Name]
			return s, ok
		}
		return "", false
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(x.Value)
		return s, err == nil
	case *ast.Ident:
		s, ok := consts[prefix+x.Name]
		return s, ok
	case *ast.ParenExpr:
		return eval(x.X, consts)
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return "", false
		}
		a, ok1 := eval(x.X, consts)
		b, ok2 := eval(x.Y, consts)
		return a + b, ok1 && ok2
	}
	return "", false
}

func area(file string) string {
	switch {
	case strings.HasPrefix(file, "agentic"), file == "reach.go":
		return "Agentic"
	case strings.HasPrefix(file, "ambient"):
		return "Istio"
	case strings.HasPrefix(file, "gateway"):
		return "Ingress"
	case strings.HasPrefix(file, "kubernetes"):
		return "Kubernetes"
	}
	return "Other"
}

func link(label, url string) string {
	if url == "" {
		return ""
	}
	return "[" + label + "](" + url + ")"
}

func host(url string) string {
	u := strings.TrimPrefix(strings.TrimPrefix(url, "https://"), "http://")
	if i := strings.Index(u, "/"); i >= 0 {
		u = u[:i]
	}
	return u
}

func render(checks []check) string {
	order := []string{"Agentic", "Istio", "Ingress", "Kubernetes"}
	by := map[string][]check{}
	base, reach := 0, 0
	for _, c := range checks {
		a := area(c.file)
		by[a] = append(by[a], c)
		if c.reach {
			reach++
		} else {
			base++
		}
	}
	var b strings.Builder
	b.WriteString(`---
title: Scorecard checks
weight: 85
description: Every Scorecard check, with its ID, area, category, severity, title and links to related field guides and product documentation.
---

# Scorecard checks

This is every check the [Scorecard](/desk/scorecard/) runs, grouped by area. It is generated from the check definitions in the source by ` + "`go run ./scripts/scorecard-checks`" + `, so rerun that after changing a check.

`)
	fmt.Fprintf(&b, "There are %d checks in every build. The %s marked \"(Enterprise, Agent reach)\" only run when the `reach` feature of [Flightbay Enterprise](/enterprise/) is on, which makes %d.\n\n", base, words(reach), base+reach)
	b.WriteString("| Area | Checks |\n|---|---|\n")
	for _, a := range order {
		n, r := len(by[a]), 0
		for _, c := range by[a] {
			if c.reach {
				r++
			}
		}
		if r > 0 {
			fmt.Fprintf(&b, "| %s | %d (%d, plus %d with Agent reach) |\n", a, n, n-r, r)
		} else {
			fmt.Fprintf(&b, "| %s | %d |\n", a, n)
		}
	}
	b.WriteString("\nIn the desk, filter the Scorecard with `id:` to find one check, for example `id:agentic.mcp-auth-required`. Each check in the desk has the explanation, the objects it looked at, the fix YAML, a link to a related field guide and upstream product documentation.\n\n")
	b.WriteString("Severity weights: critical 10, high 6, medium 3, low 1. See [how the score works](/desk/scorecard/).\n")
	for _, a := range order {
		cs := by[a]
		if len(cs) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n## %s (%d checks)\n\n| ID | Category | Severity | Title | Guide | Docs |\n|---|---|---|---|---|---|\n", a, len(cs))
		for _, c := range cs {
			t := c.title
			if c.reach {
				t += " (Enterprise, Agent reach)"
			}
			guide, docs := "", ""
			if c.source != "" {
				guide = link("Guide", c.source)
			}
			if c.docs != "" {
				label := "Product docs"
				if host(c.docs) != "docs.solo.io" {
					label = host(c.docs)
				}
				docs = link(label, c.docs)
			}
			fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s | %s |\n", c.id, c.category, c.severity, strings.ReplaceAll(t, "|", "\\|"), guide, docs)
		}
	}
	return b.String()
}

func words(n int) string {
	w := []string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten"}
	if n < len(w) {
		return w[n]
	}
	return strconv.Itoa(n)
}
