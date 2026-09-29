---
title: Live mode
weight: 15
description: Watch cluster objects and logs in a read-only workspace, with bundle export.
---

# Live mode

Most of the time the desk opens a bundle: a capture taken earlier, possibly by someone else. Live mode is for when you can reach the cluster yourself. `flightbay-desk live` connects with your kubeconfig, lists everything in scope once, then watches it. Events arrive as they happen and container logs are followed the way `kubectl logs -f` follows them. Every page of the desk works on that mirror: Resources, Search, the Log explorer, SQL, Findings, the Scorecard and Agent reach, and `kubectl` and `k9s` in the terminal, whose watches update as the cluster changes.

```bash
flightbay-desk live --context kind-mesh1 --namespaces kagent,istio-system
flightbay ui --live --context kind-mesh1 --namespaces kagent,istio-system   # the same, from the collector binary
```

## Live or a capture?

| Use live mode when | Use a capture when |
|---|---|
| you have kubectl access and want to watch something happen: a rollout, a crash loop, a policy you just applied | someone else has the access, for example a customer sending you a bundle |
| you want the Scorecard or Agent reach to move as you fix things | you need a record of the cluster at one moment, to keep, compare or send |
| logs are more useful as they arrive than as a window | the cluster is air-gapped or behind a jump host you cannot keep open |

**Save as bundle** exports the current mirror for offline investigation or sharing.

## Read-only access

Live mode uses `get`, `list`, `watch` and `pods/log`. With `--solo-diagnostics`, it also uses port-forward to read Solo diagnostic endpoints.

A guard under client-go checks requests before they leave the process. It blocks write methods, `exec`, `attach` and the API server proxy, including proxy calls made with GET. Request counts appear under `live.requests` in `/api/workspace` and are printed when the desk stops:

```text
Stopping. Requests made to kind-mesh1: discovery 3, get 117, list 272, log 27, watch 272; refused 0
```

The mirror itself is read-only too. `kubectl apply`, `delete` or `edit` in the desk's terminal are answered by the desk, which refuses them. They never reach the cluster.

### RBAC

Live mode needs what `flightbay collect` needs, plus `watch`. Generate it from the same flags:

```bash
flightbay rbac --live --namespaces kagent,istio-system
flightbay rbac --live --discover --context kind-mesh1 --namespaces kagent   # an explicit list of types, from discovery
flightbay-desk live --context kind-mesh1 --namespaces kagent --dry-run     # the plan and the RBAC together
```

With `--namespaces` the output is a Role and RoleBinding per namespace with `get, list, watch` on the selected types and `pods/log`. The Namespace objects in scope are read by name with `get` and polled every minute, so they do not need `watch`. `pods/portforward` with `create` appears only when you pass `--solo-diagnostics`.

## Scope and redaction

Live mode takes the scope flags of `flightbay collect`, registered the same way, and the policy they produce is recorded in the workspace:

| Flag | Effect |
|---|---|
| `--namespaces`, `--exclude-namespaces` | the namespaces mirrored; globs allowed |
| `--resources`, `--exclude-resources` | the types listed and watched |
| `--label-selector` | only namespaced objects with these labels, applied by the API server |
| `--secrets`, `--configmaps` | `omit`, `metadata`, or `keys` / `scrubbed`, as for a capture |
| `--cluster-scoped` | also watch nodes, namespaces and other cluster-scoped types |
| `--profile`, `--config`, `--collect` | the starting profile, a policy file, the categories |
| `--since`, `--log-streams`, `--log-pods`, `--log-containers`, `--no-logs` | which logs are followed and how far back they start |
| `--redact-extra` | extra redaction patterns |
| `--qps`, `--burst` | the client's request rate; the initial list is faster with a higher rate |

Every object, event and log line goes through the collector's redactor before it enters the workspace. Secret values never survive it: in `metadata` mode Secrets are read through the metadata API and their data never arrives; in `keys` mode the redactor keeps key names and lengths and drops the values before anything is stored.

## Logs

For each running pod in scope the desk follows the current container of every container, starting from the last `--log-tail` lines (500 by default) within `--since`. New pods are picked up as they appear and streams stop when their pods go away. The lines already read stay.

| Flag | Default | What it does |
|---|---|---|
| `--max-log-streams` | 100 | streams followed at once |
| `--log-tail` | 500 | history per stream when following starts |
| `--max-log-lines` | 1,000,000 | lines kept in the workspace; the oldest are dropped first |

Over the stream cap, pods that are not Ready or have restarted are followed first. The rest take turns every five minutes, and a stream that comes back resumes from the last line it read. Each stream not followed has a coverage record that says so, which the [Coverage](/desk/coverage/) page lists.

When a container restarts, its previous log is read once, the way `kubectl logs --previous` would read it. `kubectl logs --previous` in the terminal then shows it. Containers that had already restarted when live mode started get the same treatment.

Lines are buffered per stream and stored about once a second. If a stream produces more than 10,000 lines between two flushes, the oldest of those are dropped and the stream's coverage record counts them. When the workspace passes `--max-log-lines`, the oldest lines go and a retention record says how many.

## Solo diagnostics

Proxy dumps and control-plane debug output are not streamed. With `--solo-diagnostics`, live mode reads them once when the mirror is ready, exactly as `flightbay collect --solo-diagnostics` would: the same endpoints, the same namespace filtering, the same redaction. After that, **Refresh diagnostics** on the [Proxy configs](/desk/proxy-configs/) page reads the selected pod again over port-forward and replaces its files. The API behind it is `POST /api/live/refresh-diagnostics?pod=namespace/name`.

Without `--solo-diagnostics` there is no port-forward at all, and the guard refuses one if anything tries.

## Save as bundle

**Save as bundle** in the banner writes the mirror as it is now to a normal bundle in `--save-dir` (the current directory by default) and shows the path. It is the collector's layout rebuilt from the workspace: objects per type, events, logs per stream, diagnostics, coverage, the collection log, the redaction report and a manifest with a checksum for every member. `bundle.json` records the source as `live` and the context. The desk reads the file back and checks it before it tells you it is saved.

```bash
flightbay inspect flightbay-live-kind-mesh1-20260928T170412Z.tar.zst
flightbay-desk open flightbay-live-kind-mesh1-20260928T170412Z.tar.zst
```

Nothing is asked of the cluster when you save. What is in the bundle is what the mirror held, redacted on the way in. The API is `POST /api/live/save`.

## Export and sign

**Export and sign…** in the banner opens [Review and send](/collect/review/) on the live mirror. It freezes the mirror, lets you untick what you don't want to send (everything is selected to start with, with **Select all** and **Deselect all**), signs the export with a key you choose or create on the page, and writes the reviewed bundle to `--save-dir`. `--sign KEY` starts with that key chosen.

## The banner and refreshing

The banner that says **Offline snapshot** for a bundle reads, in live mode:

```text
● Live · kind-mesh1 · 199 objects · 16 log streams · last change 3s ago     Save as bundle   Export and sign…
```

It is green while every watch is connected and amber while any is reconnecting. A watch that drops resumes from the last resourceVersion it saw. A watch whose version has expired (410 Gone) lists again and reconciles, so a missed deletion is still applied.

Pages refresh themselves when the mirror changes, at most once every three seconds. A refresh updates the page in place: only what changed is redrawn, so where you have scrolled to (in the page, a table or a YAML block), open findings, graph zoom and the tab you are on stay put. Refreshes wait while you are typing, while you have text selected on the page, while a drawer or dialog is open, and while the tab is hidden. The Log explorer keeps its own **Follow incoming data** switch: off, it tells you how many new lines match; on, it keeps the newest in view.

Ages in tables, certificate lifetimes and `since:` in log queries count from now, not from a capture time.

## Terminal, kubectl and k9s

The kubeconfig the desk writes has one context, `<context>@live`, pointing at the desk's read-only API. That API serves the mirror, and its watches deliver `ADDED`, `MODIFIED` and `DELETED` as the mirror changes, so:

```bash
kubectl get pods -n demo -w          # new pods, status changes and deletions scroll past
k9s -n demo                          # updates as the cluster changes
kubectl logs -f deploy/web           # follows the lines live mode is following
kubectl logs web-7d9f --previous     # the crashed container's last lines
```

The `fb*` helpers work the same way: `fbq` searches the logs as they arrive, `fbscore` scores the cluster as it is now.

## Limits

- One cluster per desk. Run a second `flightbay-desk live` on another port for a second cluster.
- The initial list reads every selected type once in each namespace in scope. On a cluster with a lot of CRDs, narrow `--resources` or raise `--qps` to reach the live phase sooner.
- Only running containers are followed, and only regular containers. Init and ephemeral containers are not followed.
- Previous logs are read once per restart and appended, so `--previous` shows every previous container seen since live mode started, oldest first.
- Log lines of pods that are gone are kept until `--max-log-lines` pushes them out. The workspace database lives in `--workdir` and is deleted on exit unless you pass `--keep`.
- Findings and scores recompute when you look at them after a change. On a busy cluster that can mean a recompute on every page refresh.
- Flow logs are not attached live. Flows parsed from ztunnel, Envoy, agentgateway and CoreDNS logs are.
