package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/tjorourke/flightbay/internal/bundle"
	"github.com/tjorourke/flightbay/internal/scan"
)

// exitError carries a process exit code: 1 when the CI gate fails, 2 when
// the scan itself could not run.
type exitError struct {
	code int
	msg  string
}

func (e exitError) Error() string { return e.msg }

func scanCmd() *cobra.Command {
	var o scan.Options
	var bundleOut, failOn, format, output, baselineScan, baseRef, fixPack string
	var noBundle, newOnly, quiet bool
	c := &cobra.Command{
		Use:   "scan [PATH...]",
		Short: "Build a bundle from a code repository (manifests, Helm, Kustomize, Argo CD, Flux) and score it",
		Long: `Scan builds a normal Flightbay bundle from the declared state in a repository instead of a
live cluster, so the desk, the Scorecard, Agent reach, search, SQL and Compare work on it.
It renders plain YAML and JSON manifests, Helm charts (values.yaml, --values, --set, vendored
subcharts), Kustomize overlays, and the Argo CD Applications and Flux HelmReleases and
Kustomizations whose source is a path in the same repository.

With --fail-on it is a CI gate: the Scorecard runs in process and the command exits 1 when a
check at or above that severity fails. --format sarif writes SARIF 2.1.0 for code scanning.
Nothing is fetched from the network unless --helm-fetch is given.`,
		Example: `  flightbay scan .
  flightbay scan deploy/ --env prod
  flightbay scan charts/api --values charts/api=values-prod.yaml --set replicas=3 --namespace api
  flightbay scan . --overlay k8s/overlays/prod
  flightbay scan . --fail-on high --format sarif -o flightbay.sarif
  flightbay scan . --fail-on critical --new-only --base-ref origin/main --format github
  flightbay scan . --fix-pack flightbay-fixes`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if failOn != "" && !scan.ValidSeverity(failOn) {
				return exitError{2, "--fail-on must be critical, high, medium or low"}
			}
			switch format {
			case "", "text", "json", "sarif", "github", "codequality":
			default:
				return exitError{2, "--format must be text, json, sarif, github or codequality"}
			}
			if newOnly && baseRef == "" && baselineScan == "" {
				return exitError{2, "--new-only needs --base-ref REF or --baseline-scan FILE"}
			}
			stderr := cmd.ErrOrStderr()
			o.Paths, o.Version = args, version
			if !quiet {
				o.Log = stderr
			}
			start := time.Now()
			res, err := scan.Run(o)
			if err != nil {
				return exitError{2, err.Error()}
			}
			failed := 0
			for _, in := range res.Inputs {
				if in.State == bundle.StateFailed {
					failed++
					fmt.Fprintf(stderr, "  could not render %s %s: %s\n", in.Renderer, in.Source, in.Reason)
				}
			}
			fmt.Fprintf(stderr, "Scanned %s: %d objects of %d kinds from %d inputs (%d failed) in %s\n", res.Header.Repo.Name, res.Objects, len(res.Resources), len(res.Inputs), failed, time.Since(start).Round(time.Millisecond))
			if !noBundle {
				p := bundleOut
				if st, err := os.Stat(p); err == nil && st.IsDir() {
					p = filepath.Join(p, scan.FileName(res, time.Now()))
				}
				if err := os.WriteFile(p, res.Bundle, 0o600); err != nil {
					return exitError{2, err.Error()}
				}
				fmt.Fprintf(stderr, "Wrote %s (%d bytes). Open it with: flightbay-desk open %s\n", p, len(res.Bundle), p)
			}
			if format == "" && failOn == "" && fixPack == "" && !newOnly && baselineScan == "" {
				format = "text"
			}
			if format == "" && fixPack != "" && failOn == "" {
				format = "none"
			}
			ws, err := scan.Load(res.Bundle)
			if err != nil {
				return exitError{2, err.Error()}
			}
			defer ws.Close()
			g := scan.Evaluate(ws)
			if baselineScan != "" || baseRef != "" {
				var base *scan.Result
				label := baselineScan
				if baselineScan != "" {
					b, err := os.ReadFile(baselineScan)
					if err != nil {
						return exitError{2, err.Error()}
					}
					base = &scan.Result{Bundle: b}
				} else {
					fmt.Fprintf(stderr, "Scanning the base %s for comparison\n", baseRef)
					label = baseRef
					if base, err = scan.RunAtRef(o, baseRef); err != nil {
						return exitError{2, err.Error()}
					}
				}
				var bg scan.Gate
				if base != nil {
					bws, err := scan.Load(base.Bundle)
					if err != nil {
						return exitError{2, "baseline: " + err.Error()}
					}
					bg = scan.Evaluate(bws)
					bws.Close()
				}
				n := g.MarkNew(bg, label)
				fmt.Fprintf(stderr, "%d of %d failing items are new compared with %s\n", n, len(g.Failures), label)
			}
			if fixPack != "" {
				fp, err := scan.WriteFixPack(fixPack, g, ws)
				if err != nil {
					return exitError{2, err.Error()}
				}
				fmt.Fprintf(stderr, "Wrote a fix pack to %s: %d check files, %d proposals, README.md\n", fixPack, len(fp.Files), len(fp.Proposals))
			}
			if format != "none" {
				if format == "" {
					format = "text"
				}
				var w io.Writer = cmd.OutOrStdout()
				if output != "" && output != "-" {
					f, err := os.Create(output)
					if err != nil {
						return exitError{2, err.Error()}
					}
					defer f.Close()
					w = f
				}
				if err := g.Write(w, format, failOn, version); err != nil {
					return exitError{2, err.Error()}
				}
				if output != "" && output != "-" {
					fmt.Fprintf(stderr, "Wrote the %s report to %s\n", format, output)
				}
			}
			if n := len(g.Gating(failOn)); n > 0 {
				what := "failing " + scan.Plural(n, "item", "items")
				if g.Baseline != "" {
					what = "new " + what
				}
				return exitError{1, fmt.Sprintf("%d %s at %s or above", n, what, failOn)}
			}
			return nil
		},
	}
	f := c.Flags()
	f.StringArrayVar(&o.Values, "values", nil, "Helm values file for every chart, or CHARTDIR=FILE for one chart (repeatable; resolved in the chart directory first)")
	f.StringArrayVar(&o.Set, "set", nil, "Helm value key=value for every chart, as helm --set (repeatable; values are not recorded in the bundle)")
	f.StringArrayVar(&o.Overlays, "overlay", nil, "Kustomize directory to build (repeatable; default: every kustomization that is not only a base)")
	f.StringVar(&o.Env, "env", "", "environment to pick: overlays/<env>, envs/<env>, values-<env>.yaml; prints what it picked")
	f.StringVar(&o.Release, "release", "", "Helm release name (default: the chart name)")
	f.StringVar(&o.Namespace, "namespace", "", "namespace for namespaced objects that have none (default: the chart's flightbay.dev/namespace annotation, else default)")
	f.StringVar(&o.Root, "root", "", "repository root that source paths and Argo CD paths are relative to (default: the Git top level, else the scanned directory)")
	f.StringVar(&o.ContextName, "context-name", "", "name of the synthetic cluster in the bundle (default repo:<name>)")
	f.StringVar(&o.RepoURL, "repo-url", "", "treat Argo CD and Flux sources with this repository URL as the repository being scanned")
	f.BoolVar(&o.HelmFetch, "helm-fetch", false, "run helm dependency update for charts with missing dependencies (uses the network; the repository is not changed)")
	f.StringVar(&bundleOut, "bundle", ".", "where to write the bundle: a directory or a file")
	f.BoolVar(&noBundle, "no-bundle", false, "score only; do not write the bundle")
	f.StringVar(&failOn, "fail-on", "", "exit 1 when a check at or above this severity fails: critical, high, medium or low")
	f.StringVar(&format, "format", "", "report format: text, json, sarif, github (workflow annotations) or codequality (GitLab)")
	f.StringVarP(&output, "output", "o", "", "write the report to this file instead of stdout")
	f.StringVar(&baselineScan, "baseline-scan", "", "an earlier scan bundle: report and gate only failures it does not have")
	f.BoolVar(&newOnly, "new-only", false, "report and gate only new failures (with --base-ref or --baseline-scan)")
	f.StringVar(&baseRef, "base-ref", "", "Git ref to compare with (such as origin/main): scanned from a temporary worktree")
	f.StringVar(&fixPack, "fix-pack", "", "write one YAML file per failing check, proposals and a README.md into this directory")
	f.BoolVarP(&quiet, "quiet", "q", false, "less progress output")
	return c
}
