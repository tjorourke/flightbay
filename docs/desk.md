# Investigate a bundle

`flightbay-desk` turns a bundle into a workspace on your machine: a web UI, a terminal where `kubectl`, `k9s` and `stern` work against the snapshot, helper commands, read-only SQL and an MCP server for AI agents. Nothing is sent anywhere.

- [Open a bundle](#open-a-bundle)
- [Try it with the sample](#try-it-with-the-sample)
- [Find things](#find-things)
- [From the terminal](#from-the-terminal)
- [Watch a live cluster](#watch-a-live-cluster)
- [Compare two captures](#compare-two-captures)
- [AI agents](#ai-agents)

Istio ambient has its own guide, [Istio ambient](ambient.md), and the Agents and Traffic pages are covered in [Flightbay Enterprise](enterprise.md).

## Open a bundle

```bash
flightbay ui flightbay-01234567-20260927T1012Z.tar.zst   # that bundle
flightbay ui .                                           # the newest capture in this directory
```

`flightbay ui` runs `flightbay-desk open` with the same arguments. Your browser opens on the workspace; if it doesn't, open the `UI:` address it prints, token included. Press Ctrl+C in the terminal to close it, and the workspace is deleted (`--keep` keeps it).

Run `flightbay ui` with no arguments and it opens a start page instead, with three cards: **Live stream clusters** (select contexts from one or more kubeconfig files, preview the reads and RBAC per cluster, then mirror them into one workspace), **Open a file** (captures in this directory and `~/Downloads` are listed; open one, or review it before you send it; drag and drop works too) and **Open a repo** (a directory or a Git URL, scanned for manifests, Helm charts and Kustomize overlays). The footer shows the command line for whatever you pick, so you can skip the page next time.

![The start page](images/start.png)

## Try it with the sample

`examples/sample-bundle.tar.zst` is a capture of a small kind cluster running Istio ambient, a waypoint and a shop app with faults planted in it: a crash loop, an image that won't pull, a pod that can't be scheduled, a route to a missing Service and a Service pointing at a waypoint that doesn't exist.

```bash
flightbay ui examples/sample-bundle.tar.zst
```

Things to try:

- **Findings.** The five planted faults, each with its evidence and a next step.
- **Search.** `checkout`, `kind:pod CrashLoopBackOff`, `label:app=checkout`, `"registry.invalid"`.
- **Log explorer.** Click the **Errors** example, drag across the chart to zoom into a spike, then click a line and follow its `trace_id` across pods.
- **Proxy diagnostics.** Read-only delivered configuration from Istio and agentgateway, including waypoint listeners, clusters, routes and certificates, istiod sync state and ztunnel workloads.
- **k9s.** Press **Open in k9s** on Resources, or **k9s** in the terminal bar.
- **Terminal.** Open it at the bottom (Ctrl+\`) and run `k get pods -n payments`, `k logs`, `k9s`, `fbsearch` or `fbq`.

The button at the top right switches between light and dark.

## Find things

### Search

**Search** covers every captured object of every kind: names, namespaces, labels and anything in spec and status, such as images, hostnames and status reasons. ⌘K jumps to an object from anywhere.

```text
checkout                          name, label or content
kind:pod ns:payments              kind accepts short names: po, svc, deploy, httproute
label:app=checkout   name:ist*    labels and name prefixes
"CrashLoopBackOff"   -kind:event  phrases, and - to exclude
```

Search boxes complete as you type from what is in the capture: `kind:` lists the kinds that have objects, `label:` lists label keys and then their values, `ns:` in the log explorer lists namespaces with lines matching the rest of the query.

![Searching every object](images/search.png)

### Log explorer

The **Log explorer** searches every log line, like Kibana or OpenSearch Dashboards:

- **Time.** Drag across the chart to zoom into a range, and keep dragging to drill down. Pinch or scroll over the chart to zoom around the cursor, and double-click to zoom out. There are quick ranges ("last 15 minutes of the capture"), exact from/to times, and **Whole capture** to reset.
- **Fields.** The left panel lists level, namespace, pod, container and stream, then the fields parsed from the logs, such as `trace_id`, `status`, `method`, `path`, `src.workload` and `dst.addr`, each with its most common values. Click a value to filter on it.
- **Examples** above the search box: errors, crashed containers, HTTP 5xx, connection problems, certificates, istiod pushes, and denied requests.
- **Context.** Click a line to see the lines around it in the same container, and follow its trace, request or connection ID across pods.

```text
level:error ns:payments "timed out"    pod:checkout*    trace_id:a4e98f02    status:5*    stream:previous    since:15m    -healthz    a OR b
```

![Log explorer](images/logs.png)

### Proxy diagnostics

**Proxy configs** shows what each Envoy, ztunnel and agentgateway had loaded when the snapshot was taken: listeners, routes, clusters, endpoints, workloads, policies and certificates. For istiod it shows which proxies are in sync. Filter by component family, then open captured policy resources or their YAML from the policy tables.

![Proxy configs](images/proxies.png)

### Findings

**Findings** runs rules over the snapshot: crash loops, image pulls, scheduling failures, routes and policies that weren't accepted, missing backends and waypoints, istio-cni not ready, proxies that haven't picked up config, NACKs, expiring certificates, version skew and repeated error messages. They point you at the evidence; they aren't a verdict. A rule that depends on something being missing waits until that data has fully loaded, and never fires on data the customer chose not to collect.

![Findings](images/findings.png)

### Scorecard

The **Scorecard** scores the snapshot against 129 security and operational checks (134 with [Agent reach](enterprise.md#agent-reach)), drawn from public field guides, agentgateway, kagent, Istio and kgateway source, and Kubernetes hardening guidance, in four areas:

- **Agentic**: MCP and A2A endpoints without authentication, client tokens passed through to MCP servers, missing tool allowlists, guardrails that fail open, LLM spend without a budget, credentials held inline, and agents that bypass the gateway.
- **Istio**: namespace enrolment, pods ztunnel isn't capturing, STRICT mTLS, default-deny, principals in the wrong trust domain, L7 policy that nothing enforces, ingress that skips the waypoint, istiod HA and multicluster peering.
- **Ingress**: listeners without TLS, certificate Secrets that are missing or not granted, cross-namespace references without a ReferenceGrant, shared gateways open to every namespace, and kgateway auth that fails open.
- **Kubernetes**: privileged pods and host access, capabilities, RBAC wildcards and cluster-admin bindings, Pod Security Admission, NetworkPolicy, exposed Services, requests, probes, replicas and PDBs, unpinned images and supported versions.

Each check lists the objects it looked at, says why it matters and gives the YAML to fix it, with a link to the matching guide on mastertheagent.com or masterthemesh.com. A check whose data wasn't collected, or that has nothing to apply to, is marked not applicable and doesn't count against the score. [Scorecard checks](site/content/desk/scorecard-checks.md) lists every one.

![Scorecard](images/posture.png)

### Timeline

The **Timeline** puts Kubernetes events, container exits, rollouts and bursts of error logs on one UTC axis, so you can see what changed just before things broke. Filter by kind, severity and namespace, drag across the density strip to zoom into a window, and jump from any entry to the object or to the logs around that moment.

![Timeline](images/timeline.png)

### Compare

**Compare** shows what changed between two captures of the same cluster: objects added, removed or changed, image upgrades, and findings that appeared or went away. Pick an earlier bundle from the list (Flightbay looks next to the open bundle, in the current directory and in `~/Downloads`) or paste a path. For a capture of several clusters it also compares one object across them.

## From the terminal

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

## Watch a live cluster

With kubectl access, the desk can mirror a cluster live instead of opening a capture. It lists and watches every type in scope, follows logs, and keeps the UI, the Navigator, search, the Scorecard and Agent reach up to date. It only ever reads (a transport guard refuses anything but GET), and **Save as bundle** writes the current mirror as a normal verified bundle.

```bash
flightbay ui --live --context prod-eu --namespaces 'kagent,agentgateway-system,istio-system'
```

## Compare two captures

```bash
flightbay-desk diff before.tar.zst after.tar.zst       # objects added, removed and changed, image changes, new and resolved findings
flightbay ui after.tar.zst --baseline before.tar.zst   # the same, in the Compare view
```

An object only counts as removed if both captures covered its type and namespace. Anything else is reported as not comparable.

## AI agents

`flightbay-desk mcp` makes the open workspace available to AI agents over MCP. The tools are read-only: search objects and logs, read-only SQL, findings, best-practice scores, objects and proxy configs, plus a `triage` prompt for a guided investigation.

```bash
claude mcp add flightbay -- flightbay-desk mcp
```

The MCP server only talks to your local workspace. Snapshot data reaches a model only if the agent you connect uses a hosted one.

More detail on every page: [The desk](site/content/desk/_index.md).
