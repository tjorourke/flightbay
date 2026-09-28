# Flightbay by Solo

Flightbay takes a redacted snapshot of a Kubernetes cluster and lets you investigate it offline as if it were live. You can browse every object, search every log line, run `kubectl` and `k9s` against it, read the Envoy, ztunnel and agentgateway config dumps, and get findings that know about Istio ambient, agentgateway and kgateway.

![Overview of a snapshot](docs/images/overview.png)

It is two commands:

```bash
flightbay collect --context my-cluster      # capture: writes flightbay-<time>.tar.zst
flightbay ui                                # open the newest capture in your browser
```

## Install

From source (Go 1.27, and a C compiler for the UI, which embeds DuckDB):

```bash
git clone https://github.com/tjorourke/flightbay && cd flightbay
make install                  # installs flightbay and flightbay-desk into $(go env GOPATH)/bin
```

Make sure `$(go env GOPATH)/bin` is on your `PATH`. Release builds (Homebrew, `kubectl flightbay` through krew, and a container image) are set up in `.goreleaser.yaml` and `.github/workflows/release.yml`.

## Try it with the sample

`examples/sample-bundle.tar.zst` is a capture of a small kind cluster running Istio ambient, a waypoint and a shop app with faults planted in it: a crash loop, an image that won't pull, a pod that can't be scheduled, a route to a missing Service and a Service pointing at a waypoint that doesn't exist.

```bash
flightbay ui examples/sample-bundle.tar.zst
```

Your browser opens on the workspace. If it doesn't, open the `UI:` address it prints, token included. The button at the top right switches between light and dark. Press Ctrl+C in the terminal to close it; the workspace is deleted when you do.

Things to try:

- **Findings.** The five planted faults, each with its evidence and a next step.
- **Search.** `checkout`, `kind:pod CrashLoopBackOff`, `label:app=checkout`, `"registry.invalid"`.
- **Log explorer.** Click the **Errors** example, drag across the chart to zoom into a spike, then click a line and follow its `trace_id` across pods.
- **Proxy configs.** The waypoint's listeners, clusters, routes and certificates, istiod's view of which proxies are in sync, and ztunnel's workloads.
- **k9s.** Press **Open in k9s** on Resources, or **k9s** in the terminal bar.
- **Terminal.** Open it at the bottom (Ctrl+\`) and run `k get pods -n payments`, `k logs`, `k9s`, `fbsearch` or `fbq`.

## Capture a cluster

```bash
flightbay collect --context mesh1 --dry-run            # what it would read, and the RBAC it needs; reads nothing else
flightbay collect --context mesh1                      # capture everything the default profile allows
flightbay collect --context mesh1 --namespaces payments,istio-system --since 2h --case 01234567
flightbay collect --context mesh1 --solo-diagnostics   # add proxy config dumps, istiod debug output and metrics
flightbay inspect flightbay-*.tar.zst --redactions     # check what's in it before you share it
```

The default `standard` profile collects objects, events, the last 24 hours of logs (up to 20 MiB per container, newest kept) and Helm release summaries from every namespace it can read. Useful options:

| Option | What it does |
|---|---|
| `--namespaces`, `--exclude-namespaces` | Limit the capture (globs work: `team-*`) |
| `--since 2h`, `--max-log-bytes 10MiB`, `--no-logs` | Control log volume |
| `--exclude-resources configmaps,secrets` | Never read these types |
| `--solo-diagnostics` | Also read the admin endpoints of ztunnel, Envoy, agentgateway, istiod and gateway controllers through port-forward |
| `--cluster-scoped` | Also capture nodes, CRDs and other cluster-wide objects |
| `--profile minimal` | Object metadata and events only |
| `--encrypt-to age1...` | Encrypt the bundle so only the matching key can open it |
| `--context a,b` | Several clusters in one bundle |
| `-o -` | Stream to stdout, e.g. `flightbay collect -o - \| flightbay-desk open -` |

`flightbay rbac` prints least-privilege RBAC for the same options, so the collector can run with only what it needs.

### What is and isn't collected

Secret values are never collected: Secrets become names, types, labels and key names. Credentials in object specs, environment variables, ConfigMaps, logs and proxy dumps are replaced before anything is written. That includes JSON embedded inside a field, such as `kubectl.kubernetes.io/last-applied-configuration`. `redaction-report.json` counts what was replaced, and never records the values. [docs/COLLECTS.md](docs/COLLECTS.md) lists everything, and a test regenerates it from the collector's own code so the two can't drift apart.

When you limit namespaces, proxy and control-plane dumps are filtered as well. ztunnel, agentgateway and istiod describe the whole mesh, so any entry that names another namespace is removed before it's written, and the bundle records how many were removed.

One thing is deliberately left out. The agentgateway and kgateway controllers are built on a library called krt, which keeps an in-memory copy of every Kubernetes object the controller watches. That includes Gateways, routes and policies, but also the Secrets it reads, such as TLS keys and API keys for LLM providers. The controller's admin port can dump all of that as one large JSON document. It would be useful for debugging the controller, but it covers every namespace and contains Secret values, so it can't respect "only these namespaces" or "never Secret values". Flightbay collects the controllers' logs, metrics and the objects themselves instead.

## Find things

![Searching every object](docs/images/search.png)

**Search** covers every captured object of every kind: names, namespaces, labels and anything in spec and status, such as images, hostnames and status reasons. ⌘K jumps to an object from anywhere.

```text
checkout                          name, label or content
kind:pod ns:payments              kind accepts short names: po, svc, deploy, httproute
label:app=checkout   name:ist*    labels and name prefixes
"CrashLoopBackOff"   -kind:event  phrases, and - to exclude
```

**Log explorer** searches every log line, like Kibana or OpenSearch Dashboards:

- **Time.** Drag across the chart with your trackpad or mouse to zoom into a range, and keep dragging to drill down. Pinch or scroll over the chart to zoom around the cursor, and double-click to zoom out. There are quick ranges ("last 15 minutes of the capture"), exact from/to times, and **Whole capture** to reset.
- **Fields.** The left panel lists level, namespace, pod (with a search box), container and stream, and then the fields parsed from the logs, such as `trace_id`, `status`, `method`, `path`, `src.workload` and `dst.addr`, each with its most common values. Click a value to filter on it.
- **Examples** above the search box: errors, crashed containers, HTTP 5xx, connection problems, certificates, istiod pushes, and denied requests.
- **Context.** Click a line to see the lines around it in the same container, and follow its trace, request or connection ID across pods.

```text
level:error ns:payments "timed out"    pod:checkout*    trace_id:a4e98f02    status:5*    stream:previous    since:15m    -healthz    a OR b
```

![Log explorer](docs/images/logs.png)

**Proxy configs** shows what each Envoy, ztunnel and agentgateway had loaded when the snapshot was taken: listeners, routes, clusters, endpoints, workloads, policies and certificates. For istiod it shows which proxies are in sync.

![Proxy configs](docs/images/proxies.png)

**Findings** runs rules over the snapshot: crash loops, image pulls, scheduling failures, routes and policies that weren't accepted, missing backends and waypoints, istio-cni not ready, proxies that haven't picked up config, NACKs, expiring certificates, version skew and repeated error messages. They point you at the evidence; they aren't a verdict. A rule that depends on something being missing waits until that data has fully loaded, and never fires on data the customer chose not to collect.

![Findings](docs/images/findings.png)

**Best practices** scores the snapshot against Solo's defence-in-depth guidance, in two areas. Agentic covers agentgateway, kagent and MCP: MCP endpoints with no JWT or external auth, tool allowlists, rate limits on LLM routes, prompt guards, credentials held literally in env or routes, agent identity and audit logging. Istio ambient covers namespace enrolment, workloads that opt out, STRICT mTLS, default-deny, ALLOW policies that admit anything, L7 policy without a waypoint, missing waypoints, egress and telemetry. Each check lists the objects it looked at, says why it matters and gives the YAML to fix it, with a link to the matching guide on mastertheagent.com. Checks are weighted by severity. A check whose data wasn't collected, or that has nothing to apply to, is marked not applicable and doesn't count against the score.

![Best practices](docs/images/posture.png)

**Timeline** puts Kubernetes events, container exits, rollouts and bursts of error logs on one UTC axis, so you can see what changed just before things broke. Filter by kind, severity and namespace, drag across the density strip to zoom into a window, and jump from any entry to the object or to the logs around that moment. A steady run of errors shows as one entry covering its time range, and a minute well above that run's usual rate is called out on its own.

![Timeline](docs/images/timeline.png)

### From the terminal

`kubectl` (or `k`), `k9s` and `stern` work against the snapshot. Each cluster appears as `<context>@snapshot`, and changes are refused.

```bash
flightbay-desk shell                          # a shell wired to the open workspace (or use the UI's terminal)
k get pods -A                                 # k is kubectl, with tab completion
k describe pod -n payments checkout-85b9d479d6-cgf5q
k logs -n payments deploy/checkout --previous
k9s
fbsearch 'kind:pod CrashLoopBackOff'          # object search
fbq 'level:error ns:payments'                 # log search
fbsql "SELECT namespace, pod, count(*) FROM logs WHERE level = 'error' GROUP BY ALL"   # read-only DuckDB SQL; fbsql alone for a prompt
fbpc                                          # captured proxies; fbpc <pod> for one, --file config_dump.json for raw
fbfind                                        # findings
fbscore                                       # best-practice scores, failing checks and their fixes
```

### Compare two captures

```bash
flightbay-desk diff before.tar.zst after.tar.zst       # objects added, removed and changed, image changes, new and resolved findings
flightbay ui after.tar.zst --baseline before.tar.zst   # the same, in the Compare view
```

An object only counts as removed if both captures covered its type and namespace. Anything else is reported as not comparable.

### AI agents

`flightbay-desk mcp` makes the open workspace available to AI agents over MCP. The tools are read-only: search objects and logs, read-only SQL, findings, best-practice scores, objects and proxy configs, plus a `triage` prompt for a guided investigation.

```bash
claude mcp add flightbay -- flightbay-desk mcp
```

The MCP server only talks to your local workspace. Snapshot data reaches a model only if the agent you connect uses a hosted one.

## Commands

| Command | |
|---|---|
| `flightbay collect` | Capture a cluster (alias `capture`) |
| `flightbay ui [bundle \| dir]` | Open the UI on a capture (default: the newest in this directory) |
| `flightbay inspect BUNDLE` | Verify a bundle and show what's in it |
| `flightbay extract BUNDLE DIR` | Verify and unpack it (it's also a normal `tar.zst`: `tar --zstd -xf`) |
| `flightbay rbac` | Least-privilege RBAC for your options |
| `flightbay docs collects` | What is and isn't collected |
| `flightbay-desk open` | What `flightbay ui` runs; `--keep` keeps the workspace, `-i key` opens encrypted bundles |
| `flightbay-desk search / query / findings / proxy-config / sql / posture` | The terminal helpers (`fbsearch`, `fbq`, `fbfind`, `fbpc`, `fbsql`, `fbscore`), usable anywhere a workspace is open |
| `flightbay-desk diff A B` | Compare two captures |
| `flightbay-desk mcp` | MCP server for agents |
| `flightbay-desk keygen` | Create a key pair for `--encrypt-to` |

## How it works

- **The bundle** is a plain `tar` stream compressed with zstd. `bundle.json` comes first, with the capture policy; `manifest.json` comes last, with a SHA-256 for every file. Logs are written early and bulky diagnostics late, so the UI can open before the file has finished reading.
- **The collector** is a single static Go binary with no dependencies. It redacts in memory and never writes unredacted data to disk.
- **The UI** (`flightbay-desk`) stores logs, objects and events in DuckDB in a temporary workspace. It keeps about 90 bytes per log line on disk: 10 million lines is 0.8 GB and loads in about 35 seconds. Searching 10 million lines takes 20–250 ms for structured queries and under a second for free text. It also serves a read-only Kubernetes API, which is what `kubectl` and `k9s` talk to.

## Development

```bash
make build                                    # ./bin/flightbay and ./bin/flightbay-desk
make test                                     # unit tests
test/e2e/e2e.sh                               # kind cluster with planted faults: collector, UI, kubectl, k9s, search, SQL
MESH_CONTEXT=kind-mesh1 test/e2e/mesh.sh      # Solo diagnostics against an existing ambient cluster (read-only)
FLIGHTBAY_SCALE=10000000 go test ./internal/store -run TestScale -v -timeout 60m
docker build -t flightbay-desk .              # UI image with kubectl, k9s, istioctl, stern, jq and yq
```

## Status

Early and unreleased. Still to decide: the licence, and who owns the key customers encrypt bundles to.
