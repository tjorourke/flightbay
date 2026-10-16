---
title: "flightbay-desk CLI"
weight: 20
description: "Every flightbay-desk command and flag, generated from the command tree."
---

Generated from the `flightbay-desk` command tree. Run any command with `--help` for the same text.

Investigate a snapshot bundle offline

## flightbay-desk activity

Who called which MCP tool, model or agent through agentgateway, refusals, tokens and tool changes (the fbactivity helper; Flightbay Enterprise)

```text
flightbay-desk activity [calls|tools|models|denials|changes]
```

Examples:

```bash
  fbactivity                 # caller -> tool, model or agent, with findings
  fbactivity tools           # every tool called, and whether its server lists it
  fbactivity models          # tokens per model and per caller
  fbactivity denials         # refused calls and the policies that could have refused them
  fbactivity changes         # tools added, removed or re-described since the baseline
```

## flightbay-desk diff

Compare two bundles from the same customer: objects, image versions and findings

```text
flightbay-desk diff BEFORE AFTER [flags]
```

Examples:

```bash
  flightbay-desk diff flightbay-01234567-20260920T0900Z.tar.zst flightbay-01234567-20260927T1012Z.tar.zst
  flightbay-desk diff before.tar.zst after.tar.zst --yaml    # include changed objects
  flightbay-desk open after.tar.zst --baseline before.tar.zst  # the same in the UI
```

| Flag | Description |
|---|---|
| `-i, --identity strings` | age identity file for encrypted bundles |
| `--json` | print the report as JSON |
| `--yaml` | include the before and after YAML of changed objects |

## flightbay-desk drift

Drift compares a bundle from flightbay scan with a bundle from flightbay collect. It lists
objects in Git but not in the cluster, objects in the cluster but not in Git (limited to the
namespaces and kinds the repository declares, so system objects do not flood it), and fields
that differ once status, server-set metadata and defaulted fields are removed.

```text
flightbay-desk drift REPO_BUNDLE CLUSTER_BUNDLE [flags]
```

Examples:

```bash
  flightbay scan deploy/ --bundle repo.tar.zst
  flightbay collect --namespaces payments -o cluster.tar.zst
  flightbay-desk drift repo.tar.zst cluster.tar.zst
  flightbay-desk open cluster.tar.zst --baseline repo.tar.zst   # the same in the Compare view
```

| Flag | Description |
|---|---|
| `-i, --identity strings` | age identity file for encrypted bundles |
| `--json` | print the report as JSON |

## flightbay-desk explain

Why a workload can or cannot reach a Service: every check in order, the policies each read, and the logs (the fbexplain helper; Flightbay Enterprise)

```text
flightbay-desk explain <from-workload> <to-service> [flags]
```

Examples:

```bash
  fbexplain kagent/tom-demo kagent/github-mcp
  fbexplain agents/eval-agent postgres.tools.svc.cluster.local
  fbexplain spiffe://cluster.local/ns/agents/sa/eval-agent tools/postgres --cluster mesh1
```

| Flag | Description |
|---|---|
| `--cluster string` | cluster, when the capture has several |

## flightbay-desk features

Enterprise features are on by default. Turn them all off with FLIGHTBAY_ENTERPRISE=0, or one with FLIGHTBAY_FEATURES=-reach (FLIGHTBAY_ENTERPRISE=0 FLIGHTBAY_FEATURES=traffic keeps only that one).

```text
flightbay-desk features
```

## flightbay-desk findings

List findings in the open workspace (the fbfind helper)

```text
flightbay-desk findings
```

## flightbay-desk flows

Who actually called whom: ztunnel, agentgateway, Envoy (sidecars, gateways, waypoints) and CoreDNS logs in the capture,
plus flow logs attached with flightbay collect --attach-flows or added with flightbay-desk open --flows
(Cilium Hubble, Calico, AWS / GCP / Azure VPC flow logs, generic CSV or JSON Lines).

```text
flightbay-desk flows [flags]
```

Examples:

```bash
  fbflows
  fbflows --source agentgateway
  fbflows --source hubble --cluster mesh1
```

| Flag | Description |
|---|---|
| `--cluster string` | only this cluster |
| `--limit int` | maximum source/destination pairs (default 200) |
| `--source string` | ztunnel, agentgateway, envoy, coredns, hubble, calico, aws-vpc, gcp-vpc, azure-vnet or generic |

## flightbay-desk keygen

Create an age key pair for encrypted bundles (give customers the public recipient)

```text
flightbay-desk keygen [flags]
```

| Flag | Description |
|---|---|
| `-o, --output string` | identity file to write (default "flightbay-identity.txt") |

## flightbay-desk live

Live mode connects to a cluster with your kubeconfig and keeps a workspace in step with it:
an initial list of every selected resource type, then watches; events as they happen; and
container logs followed as kubectl logs -f would. Everything in the desk works on it: the UI,
search, SQL, the Scorecard, agent reach and findings, and kubectl and k9s in the terminal,
whose watches update as the cluster changes.

It only reads. The requests it makes are get, list, watch and pods/log (plus port-forward to
supported components when --diagnostics is given and you ask for a refresh). A guard in the
client refuses anything else before it leaves the process. Scope and redaction are the ones
flightbay collect uses, with the same flags. Save as bundle writes the mirror as a normal,
verified bundle at any moment.

```text
flightbay-desk live [flags]
```

Examples:

```bash
  flightbay-desk live --context kind-mesh1 --namespaces kagent,istio-system
  flightbay-desk live --context prod --namespaces payments --exclude-resources configmaps,secrets
  flightbay-desk live --context mesh1 --diagnostics --max-log-streams 50
  flightbay-desk live --context mesh1 --namespaces payments --dry-run      # the plan and the RBAC it needs
  flightbay rbac --live --namespaces payments                               # the same RBAC, without a cluster
```

| Flag | Description |
|---|---|
| `--allow-remote` | allow a non-loopback --listen (for containers); the token is still required |
| `--burst int` | API server request burst |
| `--case string` | support case number, recorded in the bundle |
| `--cluster-scoped` | also collect cluster-scoped objects (nodes, namespaces, CRDs, ClusterRoles) |
| `--collect strings` | categories: objects,logs,events,nodes,helm,proxies,control-plane,metrics |
| `--compression string` | best, better, default or fastest |
| `--concurrency int` | list and get requests in flight at once (default 4); still bounded by --qps |
| `--config string` | collection config file (YAML); flags refine it, exclusions always win. A CONFIG.sig next to it is checked |
| `--configmaps string` | omit, metadata or scrubbed |
| `--context string` | kubeconfig context to mirror (default: current context) |
| `--diagnostics` | add proxy dumps, control-plane debug and metrics, and allow the port-forward they need |
| `--drop-field stringArray` | remove this path from every object before writing, e.g. 'metadata.annotations["corp.example/*"]' (repeatable) |
| `--dry-run` | print what would be watched and the RBAC it needs; reads discovery only |
| `--exclude-namespaces strings` | namespaces to exclude (globs allowed) |
| `--exclude-resources strings` | resources never to read, e.g. configmaps,secrets |
| `--hubble-flows int[=20000]` | read the last N flows from Cilium hubble-relay (kube-system) through port-forward and the local hubble CLI; --hubble-flows alone means 20000 |
| `--include-resources strings` | allowlist: read only these resource types (same as --resources); --exclude-resources still wins |
| `--json` | print the dry-run plan as JSON |
| `--keep` | keep the workspace directory (DuckDB file, kubeconfig) after stopping |
| `--kubeconfig string` | kubeconfig file (default: $KUBECONFIG or ~/.kube/config) |
| `--label-selector string` | only collect namespaced objects matching this label selector |
| `--listen string` | loopback address for the UI and API (default "127.0.0.1:8484") |
| `--log-concurrency int` | log streams read at once (default 8) |
| `--log-containers strings` | only collect logs from containers matching these globs |
| `--log-pods strings` | only collect logs from pods matching these globs |
| `--log-qps float32` | log requests per second (default 50); they are proxied to kubelets, so they are limited separately |
| `--log-streams strings` | log streams: current,previous,init,ephemeral |
| `--log-tail int` | lines of history per stream when following starts (within --since) (default 500) |
| `--max-bundle-bytes int` | stop collecting once the compressed bundle reaches this size |
| `--max-log-bytes int` | per-stream log limit in bytes (keeps the newest lines) |
| `--max-log-lines int` | log lines kept in the workspace; the oldest are dropped first (default 1000000) |
| `--max-log-streams int` | container log streams followed at once; beyond it unhealthy pods come first and the rest take turns (default 100) |
| `--namespaces strings` | namespaces to include (globs allowed); default all |
| `--no-browser` | do not open a browser |
| `--no-logs` | do not collect container logs |
| `--no-portforward` | never use port-forward |
| `--no-proxy-dumps` | do not collect proxy config dumps |
| `--no-terminal` | disable the in-browser terminal |
| `--object-detail string` | metadata or full |
| `--policy-key strings` | public keys of whoever approves collection policies; --config must then be signed by one of them and flags may only narrow it |
| `--portforward` | allow port-forward based diagnostics |
| `--profile string` | starting profile: minimal, standard or custom (default "standard") |
| `--qps float32` | API server requests per second |
| `--redact-extra string` | file of extra redaction patterns (one Go regular expression per line) |
| `--resources strings` | resource allowlist, e.g. pods,deployments.apps,httproutes.gateway.networking.k8s.io |
| `--save-dir string` | directory Save as bundle and Review and send write to (default ".") |
| `--secrets string` | omit, metadata or keys (values are never collected) |
| `--shell string` | shell for the terminal (default /bin/bash) |
| `--sign string` | sign bundles exported from Review and send with this private key (flightbay keygen); the page can also choose or create one |
| `--since string` | log window, e.g. 2h |
| `--workdir string` | workspace directory (default ~/.flightbay/workspaces/ws-*) |

## flightbay-desk mcp

Serves the open workspace as a Model Context Protocol server on stdin/stdout.
Tools: workspaces, workspace_summary, list_findings, best_practices, agent_reach, agent_activity, traffic_map,
explain_connection, search_logs, log_context, search_objects, run_sql, list_resource_types,
list_objects, get_object, proxy_config, compare_baseline. Prompt: triage. best_practices
priorities, agent_reach, agent_activity, traffic_map and explain_connection need Flightbay Enterprise.

The server finds the open desks on every call (~/.flightbay/desks), so it can start before
the bundle is opened; workspaces switches between them when several are open. It tells the
agent that the snapshot's clusters are not reachable, so it answers from the snapshot only.

The server only talks to local desks. Snapshot data reaches a model only if the
agent you connect is configured to use one.

```text
flightbay-desk mcp
```

Examples:

```bash
  claude mcp add flightbay -- flightbay-desk mcp
  opencode: "mcp": {"flightbay": {"type": "local", "command": ["flightbay-desk", "mcp"]}}
```

## flightbay-desk open

Open a bundle and start investigating while it loads (no bundle: the start page)

```text
flightbay-desk open [BUNDLE | DIR | -] [flags]
```

Examples:

```bash
  flightbay-desk open                                  # the start page: live, a file or a repo
  flightbay-desk open flightbay-01234567-20260927T1012Z.tar.zst
  flightbay-desk open .                                # the newest flightbay-*.tar.zst in this directory
  flightbay collect -o - | flightbay-desk open -
  flightbay-desk open bundle.tar.zst.age -i ~/.flightbay/support.key
  flightbay-desk open bundle.tar.zst --defer logs      # load logs later from the UI
  flightbay-desk open bundle.tar.zst --sign my.key     # remove objects, logs or namespaces, export a signed copy
  flightbay-desk open bundle.tar.zst --flows hubble.jsonl,vpc-flows.log@mesh2   # add flow logs (not signed)
```

| Flag | Description |
|---|---|
| `--allow-remote` | allow a non-loopback --listen (for containers); the token is still required |
| `--baseline string` | an earlier bundle from the same customer to compare against (Compare view) |
| `--defer strings` | categories to load later from the UI: objects, logs, events, diagnostics |
| `--encrypt-to strings` | encrypt bundles exported from the Review and send page to these age recipients (age1...) or @file |
| `--flows strings` | flow log files to add, FILE or FILE@CLUSTER (Hubble, Calico, AWS/GCP/Azure VPC, generic CSV/JSONL); marked as added by the reviewer, not signed |
| `-i, --identity strings` | age identity file for encrypted bundles |
| `--keep` | keep the workspace directory (DuckDB file, kubeconfig) after closing |
| `--listen string` | loopback address for the UI and API (default "127.0.0.1:8484") |
| `--no-browser` | do not open a browser |
| `--no-terminal` | disable the in-browser terminal |
| `--review` | open on the Review and send page (removing objects and exporting works from any bundle file without it) |
| `--review-out string` | file to export to (default NAME-reviewed.tar.zst next to the bundle) |
| `--reviewer string` | who is removing things, recorded in the exported bundle's ledger |
| `--shell string` | shell for the terminal (default /bin/bash) |
| `--sign string` | sign bundles exported from the Review and send page with this private key (flightbay keygen) |
| `--throttle string` | limit import speed, e.g. 512KiB (rehearse slow uploads) |
| `--verify-key strings` | public keys to check the bundle's signature against (default: ~/.flightbay/trusted-keys/*.pub) |
| `--workdir string` | workspace directory (default ~/.flightbay/workspaces/ws-*) |

## flightbay-desk posture

Best-practice scores (agentic, Istio, ingress, Kubernetes) with fixes (the fbscore helper)

```text
flightbay-desk posture [flags]
```

Aliases: `best-practices`, `score`

Examples:

```bash
  fbscore                 # failing and warning checks, with the YAML to fix them
  fbscore --area agentic
  fbscore --all           # every check, including passing and not applicable
  fbscore --top 10        # the ten fixes that recover the most score (Enterprise)
```

| Flag | Description |
|---|---|
| `--all` | show passing and not applicable checks too |
| `--area string` | agentic, ambient, gateway or kubernetes |
| `--top int` | rank the N problems that recover the most score (Flightbay Enterprise) |

## flightbay-desk proxy-config

Show a captured proxy or control-plane dump (the fbpc helper)

```text
flightbay-desk proxy-config [POD] [flags]
```

Examples:

```bash
  fbpc                                  # list captured components
  fbpc ztunnel-5b4bw                    # workloads, services, policies, certificates
  fbpc ai-gateway --section routes      # one table
  fbpc istiod --file syncz.json | jq    # a raw captured file
```

| Flag | Description |
|---|---|
| `--cluster string` | cluster name in the bundle |
| `--file string` | print one captured file as captured, e.g. config_dump.json |
| `-n, --namespace string` | namespace of the pod |
| `--section string` | only tables whose title contains this, e.g. routes |

## flightbay-desk query

Search logs in the open workspace (the fbq helper)

```text
flightbay-desk query QUERY [flags]
```

Examples:

```bash
  fbq 'level:error ns:istio-system since:2h "connection reset"'
```

| Flag | Description |
|---|---|
| `--limit int` | maximum lines (default 100) |

## flightbay-desk reach

Every agent's blast radius, or one agent's network paths and fixes (the fbreach helper; Flightbay Enterprise)

```text
flightbay-desk reach [agent] [flags]
```

Examples:

```bash
  fbreach                       # every agent, MCP server and agent gateway
  fbreach kagent/demo-agent     # its paths, identity, findings and destinations
  fbreach kagent/demo-agent --fix
```

| Flag | Description |
|---|---|
| `--fix` | print the YAML that locks each finding down |

## flightbay-desk search

Find objects of any kind in the open workspace (the fbsearch helper)

```text
flightbay-desk search QUERY [flags]
```

Examples:

```bash
  fbsearch checkout                              # names, labels and content of every captured object
  fbsearch 'kind:pod ns:payments CrashLoopBackOff'
  fbsearch 'label:app=checkout -kind:event'
  fbsearch '"shop.example.com"'                  # e.g. which routes use a hostname
```

| Flag | Description |
|---|---|
| `--limit int` | maximum results (default 50) |

## flightbay-desk shell

Start a shell wired to the open workspace (kubectl or k, k9s, stern, fbsearch, fbq, fbfind, fbpc)

```text
flightbay-desk shell
```

## flightbay-desk sql

Run read-only SQL over logs, objects, events and flows (the fbsql helper); no query starts a prompt

```text
flightbay-desk sql [QUERY] [flags]
```

Examples:

```bash
  fbsql "SELECT pod, count(*) FROM logs WHERE level = 'error' GROUP BY ALL ORDER BY 2 DESC"
  fbsql            # interactive; type help for the tables
```

| Flag | Description |
|---|---|
| `--limit int` | maximum rows (default 200) |

## flightbay-desk start

The start page is what flightbay ui and flightbay-desk open show when they are given nothing to open.
It has three cards: Live stream a cluster (pick a kubeconfig context, preview what will be read and the
RBAC it needs, then start live mode), Open a file (recent bundles here and in ~/Downloads, drag and drop
or a path; open it, or review it before you send it), and Open a repo (a directory or a Git URL, scanned
in process with the scan options).

When you choose, the start page replaces itself with the desk for it, on the same address and with the
same session token, so the browser tab carries on without a new sign-in.

```text
flightbay-desk start [flags]
```

Examples:

```bash
  flightbay ui                                  # the same
  flightbay-desk start --listen 127.0.0.1:0 --no-browser
  flightbay-desk start -i ~/.flightbay/support.key   # open encrypted bundles without asking for the key
```

| Flag | Description |
|---|---|
| `--allow-remote` | allow a non-loopback --listen (for containers); the token is still required |
| `-i, --identity strings` | age identity file for encrypted bundles |
| `--keep` | keep the workspace directory after the desk closes |
| `--listen string` | loopback address for the start page and the desk it opens (default "127.0.0.1:8484") |
| `--no-browser` | do not open a browser |
| `--no-terminal` | disable the in-browser terminal in the desk |
| `--shell string` | shell for the desk's terminal (default /bin/bash) |
| `--workdir string` | workspace directory for uploads, clones, scans and the desk (default ~/.flightbay/workspaces/ws-*) |

## flightbay-desk traffic

The ambient traffic map: connections with mTLS, waypoints and policy verdicts (the fbtraffic helper; Flightbay Enterprise)

```text
flightbay-desk traffic [flags]
```

Examples:

```bash
  fbtraffic
  fbtraffic --flag plaintext      # plaintext between mesh workloads
  fbtraffic --flag unpoliced --fix
  fbtraffic --namespace kagent
```

| Flag | Description |
|---|---|
| `--fix` | print the YAML for each finding |
| `--flag string` | only edges with this flag: plaintext, plaintext-in, unpoliced, denied or mismatch |
| `-n, --namespace string` | only edges to or from this namespace |

## flightbay-desk version

Print the version

```text
flightbay-desk version
```
