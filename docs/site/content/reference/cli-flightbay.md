---
title: "flightbay CLI"
weight: 10
description: "Every flightbay command and flag, generated from the command tree."
---

Generated from the `flightbay` command tree. Run any command with `--help` for the same text.

Capture an offline, redacted snapshot of Kubernetes clusters

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
| `--concurrency int` | list and get requests in flight at once (default 4); still bounded by --qps |
| `--config string` | collection config file (YAML); flags refine it, exclusions always win. A CONFIG.sig next to it is checked |
| `--configmaps string` | omit, metadata or scrubbed |
| `--context strings` | kubeconfig contexts to collect (default: current context) |
| `--diagnostics` | add proxy dumps, control-plane debug and metrics, and allow the port-forward they need |
| `--drop-field stringArray` | remove this path from every object before writing, e.g. 'metadata.annotations["corp.example/*"]' (repeatable) |
| `--dry-run` | print what would be collected and the RBAC it needs; reads discovery only |
| `--encrypt-to strings` | age recipients (age1...) or @file; the bundle becomes .tar.zst.age |
| `--exclude-namespaces strings` | namespaces to exclude (globs allowed) |
| `--exclude-resources strings` | resources never to read, e.g. configmaps,secrets |
| `--hubble-flows int[=20000]` | read the last N flows from Cilium hubble-relay (kube-system) through port-forward and the local hubble CLI; --hubble-flows alone means 20000 |
| `--include-resources strings` | allowlist: read only these resource types (same as --resources); --exclude-resources still wins |
| `--interactive` | choose categories and scope with prompts, preview, then confirm |
| `--json` | print the dry-run plan as JSON |
| `--kubeconfig string` | kubeconfig file (default: $KUBECONFIG or ~/.kube/config) |
| `--label-selector string` | only collect namespaced objects matching this label selector |
| `--log-concurrency int` | log streams read at once (default 8) |
| `--log-containers strings` | only collect logs from containers matching these globs |
| `--log-pods strings` | only collect logs from pods matching these globs |
| `--log-qps float32` | log requests per second (default 50); they are proxied to kubelets, so they are limited separately |
| `--log-streams strings` | log streams: current,previous,init,ephemeral |
| `--max-bundle-bytes int` | stop collecting once the compressed bundle reaches this size |
| `--max-log-bytes int` | per-stream log limit in bytes (keeps the newest lines) |
| `--namespaces strings` | namespaces to include (globs allowed); default all |
| `--no-logs` | do not collect container logs |
| `--no-portforward` | never use port-forward |
| `--no-proxy-dumps` | do not collect proxy config dumps |
| `--object-detail string` | metadata or full |
| `-o, --out string` | output directory or file; - streams the bundle to stdout (default ".") |
| `--policy-key strings` | public keys of whoever approves collection policies; --config must then be signed by one of them and flags may only narrow it |
| `--portforward` | allow port-forward based diagnostics |
| `--profile string` | starting profile: minimal, standard or custom (default "standard") |
| `--qps float32` | API server requests per second |
| `-q, --quiet` | less progress output |
| `--redact-extra string` | file of extra redaction patterns (one Go regular expression per line) |
| `--resources strings` | resource allowlist, e.g. pods,deployments.apps,httproutes.gateway.networking.k8s.io |
| `--save-config string` | write the resolved policy to this file for repeatable runs |
| `--secrets string` | omit, metadata or keys (values are never collected) |
| `--sign string` | sign the bundle with this private key (flightbay keygen); recipients can verify it with your public key |
| `--since string` | log window, e.g. 2h |
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
| `--verify-key strings` | public key(s) the bundle must be signed by (default: any in ~/.flightbay/trusted-keys, not required) |

## flightbay keygen

Create an Ed25519 signing key pair: sign bundles and policies with the .key, share the .pub with recipients

```text
flightbay keygen [flags]
```

Examples:

```bash
  flightbay keygen --name "ACME platform team"                 # writes flightbay-signing.key and .pub
  flightbay keygen --name "ACME security" -o approver           # approver.key and approver.pub, for policies
```

| Flag | Description |
|---|---|
| `--force` | overwrite existing key files |
| `--name string` | who signs with this key, e.g. "ACME platform team" (shown when a bundle verifies) |
| `-o, --output string` | file name without extension: writes NAME.key and NAME.pub (default "flightbay-signing") |

## flightbay policy

Sign and check collection policy files your security team approves

## flightbay policy sign

Approve a collection policy: writes POLICY.sig next to it

```text
flightbay policy sign POLICY [flags]
```

Examples:

```bash
  flightbay policy sign collection.yaml --key approver.key
  flightbay collect --config collection.yaml --policy-key approver.pub   # refuses an unsigned or edited policy
```

| Flag | Description |
|---|---|
| `--key string` | approver's private key |

## flightbay policy verify

Check POLICY.sig: unmodified since it was signed, and by whom

```text
flightbay policy verify POLICY [flags]
```

| Flag | Description |
|---|---|
| `--key strings` | approver public key(s) it must be signed by |

## flightbay rbac

Print least-privilege RBAC for the collection policy

```text
flightbay rbac [flags]
```

Examples:

```bash
  flightbay rbac --namespaces payments
  flightbay rbac --discover --context mesh1
  flightbay rbac --live --namespaces kagent      # for flightbay-desk live: adds watch
```

| Flag | Description |
|---|---|
| `--burst int` | API server request burst |
| `--case string` | support case number, recorded in the bundle |
| `--cluster-scoped` | also collect cluster-scoped objects (nodes, namespaces, CRDs, ClusterRoles) |
| `--collect strings` | categories: objects,logs,events,nodes,helm,proxies,control-plane,metrics |
| `--compression string` | best, better, default or fastest |
| `--concurrency int` | list and get requests in flight at once (default 4); still bounded by --qps |
| `--config string` | collection config file (YAML); flags refine it, exclusions always win. A CONFIG.sig next to it is checked |
| `--configmaps string` | omit, metadata or scrubbed |
| `--context strings` | kubeconfig contexts to collect (default: current context) |
| `--diagnostics` | add proxy dumps, control-plane debug and metrics, and allow the port-forward they need |
| `--discover` | use cluster discovery to list resources explicitly instead of a wildcard |
| `--drop-field stringArray` | remove this path from every object before writing, e.g. 'metadata.annotations["corp.example/*"]' (repeatable) |
| `--exclude-namespaces strings` | namespaces to exclude (globs allowed) |
| `--exclude-resources strings` | resources never to read, e.g. configmaps,secrets |
| `--hubble-flows int[=20000]` | read the last N flows from Cilium hubble-relay (kube-system) through port-forward and the local hubble CLI; --hubble-flows alone means 20000 |
| `--include-resources strings` | allowlist: read only these resource types (same as --resources); --exclude-resources still wins |
| `--kubeconfig string` | kubeconfig file (default: $KUBECONFIG or ~/.kube/config) |
| `--label-selector string` | only collect namespaced objects matching this label selector |
| `--live` | RBAC for flightbay-desk live, which also needs watch |
| `--log-concurrency int` | log streams read at once (default 8) |
| `--log-containers strings` | only collect logs from containers matching these globs |
| `--log-pods strings` | only collect logs from pods matching these globs |
| `--log-qps float32` | log requests per second (default 50); they are proxied to kubelets, so they are limited separately |
| `--log-streams strings` | log streams: current,previous,init,ephemeral |
| `--max-bundle-bytes int` | stop collecting once the compressed bundle reaches this size |
| `--max-log-bytes int` | per-stream log limit in bytes (keeps the newest lines) |
| `--namespaces strings` | namespaces to include (globs allowed); default all |
| `--no-logs` | do not collect container logs |
| `--no-portforward` | never use port-forward |
| `--no-proxy-dumps` | do not collect proxy config dumps |
| `--object-detail string` | metadata or full |
| `--policy-key strings` | public keys of whoever approves collection policies; --config must then be signed by one of them and flags may only narrow it |
| `--portforward` | allow port-forward based diagnostics |
| `--profile string` | starting profile: minimal, standard or custom (default "standard") |
| `--qps float32` | API server requests per second |
| `--redact-extra string` | file of extra redaction patterns (one Go regular expression per line) |
| `--resources strings` | resource allowlist, e.g. pods,deployments.apps,httproutes.gateway.networking.k8s.io |
| `--secrets string` | omit, metadata or keys (values are never collected) |
| `--since string` | log window, e.g. 2h |

## flightbay review

The same removals as the Review and send page in flightbay ui, applied from a plan file (YAML or JSON)
without opening the UI. Download a plan from the Review and send page, or write one by hand. The export is a
new bundle, signed with your key if you give one, with a ledger of what was removed (counts only unless you
choose to list names).

Without --plan this opens flightbay ui on the Review and send page, the same as flightbay ui BUNDLE --review.

```text
flightbay review BUNDLE [flags]
```

Examples:

```bash
  flightbay review bundle.tar.zst --plan review.yaml --sign flightbay-signing.key --encrypt-to age1...
  flightbay ui bundle.tar.zst --sign flightbay-signing.key      # the same, choosing in the UI
```

| Flag | Description |
|---|---|
| `--disclose-names` | list removed namespaces, objects and streams by name in the ledger (default: counts only) |
| `--encrypt-to strings` | encrypt the reviewed bundle to these age recipients (age1...) or @file |
| `-i, --identity strings` | age identity to open an encrypted bundle |
| `--no-browser` | do not open a browser (UI mode) |
| `-o, --out string` | reviewed bundle to write (default: NAME-reviewed.tar.zst next to the original) |
| `--plan string` | apply this review plan without the UI (YAML or JSON) |
| `--reviewer string` | who reviewed it, recorded in the ledger |
| `--sign string` | sign the reviewed bundle with this private key |
| `--verify-key strings` | public keys to check the original's signature against |

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

With no arguments flightbay ui opens the start page, with three cards: live stream a cluster, open a
file (recent bundles here and in ~/Downloads are listed), or scan a repo. Flags such as --listen,
--no-browser, --workdir, --keep and -i carry over to whatever you open from it.

With a bundle it opens that bundle, with a directory the newest flightbay-*.tar.zst in it, and with
--live it mirrors a cluster read-only. --agent (or AGENT=1) opens agent mode: Traffic, the agentic
Scorecard, Resources, the Log explorer and Ambient. Everything else after ui is passed to flightbay-desk.

```text
flightbay ui [BUNDLE | DIR] | ui --live [--context CTX] [scope flags]
```

Examples:

```bash
  flightbay ui                                   # the start page
  flightbay ui .                                 # the newest bundle here
  flightbay ui flightbay-DEMO-20260927T1842Z.tar.zst
  flightbay ui ~/Downloads                         # newest bundle in a folder
  flightbay ui BUNDLE --sign flightbay-signing.key # remove objects, logs or namespaces on Review and send, export a signed copy
  flightbay ui --live --context kind-mesh1 --namespaces kagent   # mirror a cluster live, read-only
  flightbay ui --agent --live --context kind-mesh1                # agent mode: Traffic, agentic Scorecard, Resources, Logs
  AGENT=1 flightbay ui --live --context kind-mesh1                # the same
```

## flightbay upgrade

Download the latest published stable release, verify its SHA-256 checksums, and replace
flightbay in place. A flightbay-desk installed beside it is upgraded too. Symlinks are preserved.
Downloads are verified and staged before either binary is replaced. Requires a writable install
directory. Supports macOS and Linux. Homebrew installations should use brew upgrade flightbay.

A push to main does not publish binaries. A new version tag runs the release workflow.

```text
flightbay upgrade [flags]
```

Examples:

```bash
  flightbay upgrade --check
  flightbay upgrade
  flightbay upgrade --version v0.2.5
  flightbay upgrade --collector-only
```

| Flag | Description |
|---|---|
| `--check` | show the installed collector version and available release without changing files |
| `--collector-only` | upgrade only flightbay, leaving a sibling desk unchanged |
| `--version string` | published stable version to install (default latest) |

## flightbay version

Print the version

```text
flightbay version
```
