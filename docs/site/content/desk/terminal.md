---
title: Terminal
weight: 130
description: A shell where kubectl, k9s and stern work against the snapshot, and the fb helper commands for search, logs, findings, proxies, SQL, scores, reach and flows.
---

# Terminal

Every workspace comes with a shell wired to the snapshot. `kubectl`, `k9s` and `stern` talk to a read-only Kubernetes API that the desk serves from the bundle, so you can use the commands you already know. Next to them are the `fb*` helpers, which give you the desk's search, findings, scores and reach on the command line.

![k9s against a snapshot](/images/k9s.png)

## Opening it

| Where | How |
|---|---|
| In the UI | The bar along the bottom of every page. Click it, or press Ctrl+Backtick. The expand button makes it full height. |
| k9s in the UI | **k9s** in the terminal bar, **Open in k9s** on Resources, or `s` in the Navigator. These run `k9s --readonly` in a full-height terminal. |
| Your own terminal | `flightbay-desk shell` |
| Any shell | `export KUBECONFIG=` the path the desk printed |

The in-browser terminal is one shell per workspace. It survives moving between pages, and when you reconnect it replays up to 1 MiB of scrollback. Buttons around the UI (for example **kubectl describe** in the object drawer, **fbscore in terminal** on the Scorecard) type their command into it for you.

`--no-terminal` turns it off; `--shell` picks another shell (bash is the default).

## The shell

The shell is bash with its own rc file, so your prompt hooks and shell functions do not leak in. It sets:

| Variable | Value |
|---|---|
| `KUBECONFIG` | The workspace's kubeconfig: one context per cluster, named `<cluster>@snapshot` |
| `PATH` | The workspace's `bin/` (the helpers) first, then your usual `PATH` |
| `FLIGHTBAY_URL`, `FLIGHTBAY_TOKEN` | How the helpers reach this desk |

The prompt shows the case and the snapshot time, for example `flightbay case 01234567 @2026-09-27T10:12Z`. `k` is an alias for `kubectl`, with tab completion, and `kgp` runs `kubectl get pods`.

`kubectl`, `k9s` and `stern` must be installed on your machine; the desk does not ship them. The [container image](/getting-started/) includes all three.

## kubectl, k9s and stern

```bash
kubectl config get-contexts
k get pods -A
k describe pod -n payments checkout-85b9d479d6-cgf5q
k logs -n payments deploy/checkout --previous
k get httproutes -A -o yaml
k9s
stern -n payments checkout
```

The snapshot API supports lists, gets, describe, YAML and JSON output, the printer columns of custom resources, `logs` (including `--previous`), and the access-review calls k9s makes. It is read-only:

| You try | You get |
|---|---|
| Any write (apply, delete, edit, scale) | `MethodNotAllowed: flightbay: this is a read-only snapshot taken 2026-09-28T00:27:05Z` |
| A type the customer excluded | `Forbidden: ... was excluded by the customer's collection policy` |
| A log stream that was not collected | `BadRequest: flightbay: this log stream was not collected: ...` with the reason |

The API listens on `127.0.0.1` over TLS with a certificate made for this session, and the kubeconfig carries the desk's token.

## The helpers

Each helper is a small script in the workspace's `bin/` that runs `flightbay-desk <command>` against this workspace. You can run the long form from any terminal: it finds the desk through `FLIGHTBAY_URL` and `FLIGHTBAY_TOKEN`, or through `~/.flightbay/current.json`, which records the most recently opened desk.

| Helper | Long form | Does |
|---|---|---|
| `fbq` | `flightbay-desk query` | Search logs |
| `fbsearch` | `flightbay-desk search` | Search objects |
| `fbfind` | `flightbay-desk findings` | List findings |
| `fbpc` | `flightbay-desk proxy-config` | Proxy and control-plane dumps |
| `fbsql` | `flightbay-desk sql` | Read-only SQL |
| `fbscore` | `flightbay-desk posture` | Best-practice scores |
| `fbreach` | `flightbay-desk reach` | Agent reach (Enterprise) |
| `fbactivity` | `flightbay-desk activity` | Tool, model and agent calls through agentgateway (Enterprise) |
| `fbtraffic` | `flightbay-desk traffic` | Ambient traffic map (Enterprise) |
| `fbexplain` | `flightbay-desk explain` | Why a connection is allowed or denied (Enterprise) |
| `fbflows` | `flightbay-desk flows` | Observed traffic |

### fbq

Searches logs with the [log query language](/desk/logs/). Newest first.

```bash
fbq 'level:error ns:payments'
fbq 'level:error ns:istio-system since:2h "connection reset"'
fbq 'trace_id:a4e98f02' --limit 500
```

| Flag | Default |
|---|---|
| `--limit` | 100 lines |

### fbsearch

Finds objects with the [object search syntax](/desk/search/).

```bash
fbsearch checkout
fbsearch 'kind:pod ns:payments CrashLoopBackOff'
fbsearch 'label:app=checkout -kind:event'
fbsearch '"shop.example.com"'
```

| Flag | Default |
|---|---|
| `--limit` | 50 results |

### fbfind

Lists every [finding](/desk/findings/), with its log query ready to paste into `fbq`.

```bash
fbfind
```

### fbpc

Shows captured [proxy configs](/desk/proxy-configs/).

```bash
fbpc                                  # list captured components
fbpc ztunnel-5b4bw                    # workloads, services, policies, certificates
fbpc ai-gateway --section routes      # only tables whose title contains "routes"
fbpc istiod --file syncz.json | jq    # one raw file, as captured
```

| Flag | Does |
|---|---|
| `--cluster` | Cluster in the bundle |
| `-n`, `--namespace` | Namespace of the pod |
| `--section` | Only tables whose title contains this |
| `--file` | Print one captured file as captured |

### fbsql

Runs read-only [SQL](/desk/sql/). With no query it starts a prompt; end statements with `;`, and type `help` for the tables.

```bash
fbsql "SELECT namespace, pod, count(*) FROM logs WHERE level = 'error' GROUP BY ALL ORDER BY 3 DESC"
fbsql
```

| Flag | Default |
|---|---|
| `--limit` | 200 rows |

### fbscore

Prints [Scorecard](/desk/scorecard/) results: each area's score, grade and category scores, then the checks that fail or warn, with the YAML to fix them.

```bash
fbscore
fbscore --area agentic       # agentic, ambient, gateway or kubernetes
fbscore --all                # include passing and not applicable checks
fbscore --top 10             # the ten fixes that give back the most score (Enterprise)
```

`fbscore` also answers to `flightbay-desk best-practices` and `flightbay-desk score`.

### fbreach

[Agent reach](/desk/agent-reach/) for every agent, or one in detail. Needs Flightbay Enterprise.

```bash
fbreach
fbreach kagent/demo-agent
fbreach kagent/demo-agent --fix      # include the YAML for each finding
```

### fbactivity

[Gateway calls](/desk/activity/): who called which tool, model or agent. Needs Flightbay Enterprise.

```bash
fbactivity
fbactivity tools
fbactivity models
fbactivity denials
fbactivity changes           # needs a baseline
```

### fbtraffic

The [Traffic](/desk/traffic/) network layer as a table, worst first. Needs Flightbay Enterprise.

```bash
fbtraffic
fbtraffic --flag plaintext   # plaintext, plaintext-in, unpoliced, denied or mismatch
fbtraffic --namespace kagent --fix
```

### fbexplain

Why a workload can or cannot reach a Service: each check in order, the policies it read, what the logs saw, and the YAML to allow it. Needs Flightbay Enterprise.

```bash
fbexplain kagent/tom-demo kagent/github-mcp
fbexplain spiffe://cluster.local/ns/agents/sa/eval-agent tools/postgres --cluster mesh1
```

### fbflows

[Observed traffic](/collect/observed-traffic/), grouped by source and destination, with counts, verdicts and the top methods and paths.

```bash
fbflows
fbflows --source agentgateway
fbflows --source hubble --cluster mesh1
```

| Flag | Does |
|---|---|
| `--source` | `ztunnel`, `agentgateway`, `envoy`, `coredns`, `hubble`, `calico`, `aws-vpc`, `gcp-vpc`, `azure-vnet` or `generic` |
| `--cluster` | One cluster |
| `--limit` | Maximum source and destination pairs (200) |

```text
SOURCE        FROM                                      TO                                       PROTO  VERDICT  COUNT  VIA  TOP
ztunnel       kagent/kagent-controller-cdc96c97c-9bb2d  kagent-tools.kagent.svc.cluster.local    http   -        1475   -    POST (+2 more)
-- 12 source/destination pairs (showing 8) from 3619 records: agentgateway 48, ztunnel 3571
```

## Using the helpers in scripts

The helpers print plain text and exit non-zero on error, so they work in pipes:

```bash
fbq 'level:error' --limit 1000 | grep -c timeout
fbpc istiod --file syncz.json | jq length
```

For structured output, the desk's HTTP API is on `$FLIGHTBAY_URL` with `Authorization: Bearer $FLIGHTBAY_TOKEN`, and the [MCP server](/desk/mcp/) returns JSON.
