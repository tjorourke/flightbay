---
title: "flightbay CLI"
weight: 10
description: "Every flightbay command and flag, generated from the command tree."
---

Generated from the `flightbay` command tree. Run any command with `--help` for the same text.

Flightbay by Solo: collect an offline, redacted snapshot of Kubernetes clusters

## flightbay collect

Collect a redacted snapshot bundle

```text
flightbay collect [flags]
```

Aliases: `capture`

Examples:

```bash
  flightbay collect --context mesh1,mesh2 --case 01234567 --since 24h
  flightbay collect --namespaces payments,istio-system --exclude-resources configmaps,secrets --no-portforward
  flightbay collect --config collection.yaml --dry-run
  flightbay collect --interactive
  flightbay collect --attach-flows hubble.jsonl,vpc-flows.log    # embed flow logs in the bundle
  flightbay collect --hubble-flows                               # last 20000 flows from hubble-relay
```

| Flag | Description |
|---|---|
| `--attach-flows strings` | flow log files to embed in the bundle, FILE or FILE@CONTEXT (Hubble, Calico, AWS/GCP/Azure VPC flow logs, generic CSV/JSONL); covered by the manifest |
| `--burst int` | API server request burst |
| `--case string` | support case number, recorded in the bundle |
| `--cluster-scoped` | also collect cluster-scoped objects (nodes, namespaces, CRDs, ClusterRoles) |
| `--collect strings` | categories: objects,logs,events,nodes,helm,proxies,control-plane,metrics |
| `--compression string` | best, better, default or fastest |
| `--config string` | collection config file (YAML); flags refine it, exclusions always win |
| `--configmaps string` | omit, metadata or scrubbed |
| `--context strings` | kubeconfig contexts to collect (default: current context) |
| `--dry-run` | print what would be collected and the RBAC it needs; reads discovery only |
| `--encrypt-to strings` | age recipients (age1...) or @file; the bundle becomes .tar.zst.age |
| `--exclude-namespaces strings` | namespaces to exclude (globs allowed) |
| `--exclude-resources strings` | resources never to read, e.g. configmaps,secrets |
| `--hubble-flows int[=20000]` | read the last N flows from Cilium hubble-relay (kube-system) through port-forward and the local hubble CLI; --hubble-flows alone means 20000 |
| `--interactive` | choose categories and scope with prompts, preview, then confirm |
| `--json` | print the dry-run plan as JSON |
| `--kubeconfig string` | kubeconfig file (default: $KUBECONFIG or ~/.kube/config) |
| `--label-selector string` | only collect namespaced objects matching this label selector |
| `--log-containers strings` | only collect logs from containers matching these globs |
| `--log-pods strings` | only collect logs from pods matching these globs |
| `--log-streams strings` | log streams: current,previous,init,ephemeral |
| `--max-bundle-bytes int` | stop collecting once the compressed bundle reaches this size |
| `--max-log-bytes int` | per-stream log limit in bytes (keeps the newest lines) |
| `--namespaces strings` | namespaces to include (globs allowed); default all |
| `--no-logs` | do not collect container logs |
| `--no-portforward` | never use port-forward |
| `--no-proxy-dumps` | do not collect proxy config dumps |
| `--object-detail string` | metadata or full |
| `-o, --out string` | output directory or file; - streams the bundle to stdout (default ".") |
| `--portforward` | allow port-forward based diagnostics |
| `--profile string` | starting profile: minimal, standard or custom (default "standard") |
| `--qps float32` | API server requests per second |
| `-q, --quiet` | less progress output |
| `--redact-extra string` | file of extra redaction patterns (one Go regular expression per line) |
| `--resources strings` | resource allowlist, e.g. pods,deployments.apps,httproutes.gateway.networking.k8s.io |
| `--save-config string` | write the resolved policy to this file for repeatable runs |
| `--secrets string` | omit, metadata or keys (values are never collected) |
| `--since string` | log window, e.g. 2h |
| `--solo-diagnostics` | add proxy dumps, control-plane debug and metrics, and allow the port-forward they need |
| `--window-size int` | zstd window in bytes (default 8 MiB, max 128 MiB for stock tools) |
| `-y, --yes` | do not ask for confirmation in --interactive mode |

## flightbay docs

Print documentation generated from the collector

## flightbay docs collects

What Flightbay collects and what it never collects (Markdown)

```text
flightbay docs collects
```

## flightbay extract

Extract and verify a bundle into a directory

```text
flightbay extract BUNDLE DIR [flags]
```

| Flag | Description |
|---|---|
| `-i, --identity strings` | age identity file for encrypted bundles |

## flightbay inspect

Verify a bundle and show what it contains before you send it

```text
flightbay inspect BUNDLE [flags]
```

| Flag | Description |
|---|---|
| `--contents` | list every file in the bundle |
| `-i, --identity strings` | age identity file for encrypted bundles |
| `--policy` | print the collection policy recorded in the bundle |
| `--redactions` | show redaction counts per location |

## flightbay rbac

Print least-privilege RBAC for the collection policy

```text
flightbay rbac [flags]
```

| Flag | Description |
|---|---|
| `--burst int` | API server request burst |
| `--case string` | support case number, recorded in the bundle |
| `--cluster-scoped` | also collect cluster-scoped objects (nodes, namespaces, CRDs, ClusterRoles) |
| `--collect strings` | categories: objects,logs,events,nodes,helm,proxies,control-plane,metrics |
| `--compression string` | best, better, default or fastest |
| `--config string` | collection config file (YAML); flags refine it, exclusions always win |
| `--configmaps string` | omit, metadata or scrubbed |
| `--context strings` | kubeconfig contexts to collect (default: current context) |
| `--discover` | use cluster discovery to list resources explicitly instead of a wildcard |
| `--exclude-namespaces strings` | namespaces to exclude (globs allowed) |
| `--exclude-resources strings` | resources never to read, e.g. configmaps,secrets |
| `--hubble-flows int[=20000]` | read the last N flows from Cilium hubble-relay (kube-system) through port-forward and the local hubble CLI; --hubble-flows alone means 20000 |
| `--kubeconfig string` | kubeconfig file (default: $KUBECONFIG or ~/.kube/config) |
| `--label-selector string` | only collect namespaced objects matching this label selector |
| `--log-containers strings` | only collect logs from containers matching these globs |
| `--log-pods strings` | only collect logs from pods matching these globs |
| `--log-streams strings` | log streams: current,previous,init,ephemeral |
| `--max-bundle-bytes int` | stop collecting once the compressed bundle reaches this size |
| `--max-log-bytes int` | per-stream log limit in bytes (keeps the newest lines) |
| `--namespaces strings` | namespaces to include (globs allowed); default all |
| `--no-logs` | do not collect container logs |
| `--no-portforward` | never use port-forward |
| `--no-proxy-dumps` | do not collect proxy config dumps |
| `--object-detail string` | metadata or full |
| `--portforward` | allow port-forward based diagnostics |
| `--profile string` | starting profile: minimal, standard or custom (default "standard") |
| `--qps float32` | API server requests per second |
| `--redact-extra string` | file of extra redaction patterns (one Go regular expression per line) |
| `--resources strings` | resource allowlist, e.g. pods,deployments.apps,httproutes.gateway.networking.k8s.io |
| `--secrets string` | omit, metadata or keys (values are never collected) |
| `--since string` | log window, e.g. 2h |
| `--solo-diagnostics` | add proxy dumps, control-plane debug and metrics, and allow the port-forward they need |

## flightbay scan

Scan builds a normal Flightbay bundle from the declared state in a repository instead of a
live cluster, so the desk, the Scorecard, Agent reach, search, SQL and Compare work on it.
It renders plain YAML and JSON manifests, Helm charts (values.yaml, --values, --set, vendored
subcharts), Kustomize overlays, and the Argo CD Applications and Flux HelmReleases and
Kustomizations whose source is a path in the same repository.

With --fail-on it is a CI gate: the Scorecard runs in process and the command exits 1 when a
check at or above that severity fails. --format sarif writes SARIF 2.1.0 for code scanning.
Nothing is fetched from the network unless --helm-fetch is given.

```text
flightbay scan [PATH...] [flags]
```

Examples:

```bash
  flightbay scan .
  flightbay scan deploy/ --env prod
  flightbay scan charts/api --values charts/api=values-prod.yaml --set replicas=3 --namespace api
  flightbay scan . --overlay k8s/overlays/prod
  flightbay scan . --fail-on high --format sarif -o flightbay.sarif
  flightbay scan . --fail-on critical --new-only --base-ref origin/main --format github
  flightbay scan . --fix-pack flightbay-fixes
```

| Flag | Description |
|---|---|
| `--base-ref string` | Git ref to compare with (such as origin/main): scanned from a temporary worktree |
| `--baseline-scan string` | an earlier scan bundle: report and gate only failures it does not have |
| `--bundle string` | where to write the bundle: a directory or a file (default ".") |
| `--context-name string` | name of the synthetic cluster in the bundle (default repo:<name>) |
| `--env string` | environment to pick: overlays/<env>, envs/<env>, values-<env>.yaml; prints what it picked |
| `--fail-on string` | exit 1 when a check at or above this severity fails: critical, high, medium or low |
| `--fix-pack string` | write one YAML file per failing check, proposals and a README.md into this directory |
| `--format string` | report format: text, json, sarif, github (workflow annotations) or codequality (GitLab) |
| `--helm-fetch` | run helm dependency update for charts with missing dependencies (uses the network; the repository is not changed) |
| `--namespace string` | namespace for namespaced objects that have none (default: the chart's flightbay.dev/namespace annotation, else default) |
| `--new-only` | report and gate only new failures (with --base-ref or --baseline-scan) |
| `--no-bundle` | score only; do not write the bundle |
| `-o, --output string` | write the report to this file instead of stdout |
| `--overlay stringArray` | Kustomize directory to build (repeatable; default: every kustomization that is not only a base) |
| `-q, --quiet` | less progress output |
| `--release string` | Helm release name (default: the chart name) |
| `--repo-url string` | treat Argo CD and Flux sources with this repository URL as the repository being scanned |
| `--root string` | repository root that source paths and Argo CD paths are relative to (default: the Git top level, else the scanned directory) |
| `--set stringArray` | Helm value key=value for every chart, as helm --set (repeatable; values are not recorded in the bundle) |
| `--values stringArray` | Helm values file for every chart, or CHARTDIR=FILE for one chart (repeatable; resolved in the chart directory first) |

## flightbay ui

Open the UI on a bundle (default: the newest flightbay-*.tar.zst in this directory)

```text
flightbay ui [BUNDLE | DIR]
```

Examples:

```bash
  flightbay ui                                   # newest bundle here
  flightbay ui flightbay-DEMO-20260927T1842Z.tar.zst
  flightbay ui ~/Downloads                         # newest bundle in a folder
```

## flightbay version

Print the version

```text
flightbay version
```
