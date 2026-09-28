---
title: Log explorer
weight: 50
description: Search every captured log line with a query language, a zoomable time axis, field facets and context around any line.
---

# Log explorer

The log explorer searches every captured log line, across every pod and cluster in the bundle, in the way Kibana or OpenSearch Dashboards do. Use it to find the error, see when it started, and follow a request across pods.

![Log explorer](/images/logs.png)

It is fast on large captures: searching 10 million lines takes 20 to 250 ms for structured queries and under a second for free text.

## Query syntax

Terms are separated by spaces and all of them must match. `OR` between two terms matches either.

| Term | Matches | Example |
|---|---|---|
| word | The raw line contains it, case-insensitive (for JSON lines, the decoded message too) | `timeout` |
| `"phrase"` | The exact text, spaces included | `"connection reset"` |
| `level:` | Normalised level: `error`, `warn`, `info`, `debug`, `trace`. Common spellings map onto these (`fatal`, `critical` and `panic` are `error`; `warning` is `warn`). | `level:error` |
| `ns:` (also `namespace:`) | Namespace | `ns:payments` |
| `pod:` | Pod name | `pod:checkout*` |
| `container:` | Container name | `container:istio-proxy` |
| `workload:` (also `deploy:`, `deployment:`) | The Deployment, DaemonSet, StatefulSet or Job the pod belongs to, so you read a controller's logs across all its pods | `workload:istiod` |
| `stream:` | `current` or `previous` (the container before its last restart) | `stream:previous` |
| `node:` | Node the pod ran on | `node:worker-1` |
| `scope:` | Logger scope, from Istio, klog and JSON logs | `scope:xds` |
| `cluster:` | Cluster in the bundle | `cluster:mesh1` |
| `since:` | Only lines in this window before the **end of the capture**, not before now | `since:15m` |
| any other `key:` | A structured field parsed from the line | `trace_id:a4e98f02`, `status:503` |
| `-term` | Excludes lines the term matches | `-healthz`, `-level:info` |
| `a OR b` | Either term | `level:warn OR level:error` |

Values match exactly, ignoring case. A trailing `*` matches a prefix: `pod:checkout*`, `status:5*`. There is no wildcard in the middle or at the start.

```text
level:error ns:payments "timed out"
pod:checkout* stream:previous
trace_id:a4e98f02
status:5* -path:/healthz
"connection refused" OR "connection reset" OR "timed out"
level:error ns:istio-system since:2h "connection reset"
```

### Fields parsed from lines

Each line is parsed when the bundle is imported. The parser recognises, in this order:

| Format | What it extracts |
|---|---|
| JSON | Level (`level`, `lvl`, `severity`, `log.level`), message (`msg`, `message`, `log`, `text`), scope (`scope`, `logger`, `component`, `target`) and every other top-level field |
| Istio and ztunnel text | Level, scope, message, and `key=value` pairs such as `src.workload` and `dst.addr` |
| Envoy access log | `method`, `path`, `status` and `response_flags`; 5xx is an error, 4xx a warning |
| klog (Kubernetes components) | Level, scope, message, `key=value` pairs |
| logfmt | Every `key=value` pair |
| Anything else | Level guessed from words such as ERROR, Exception, WARN, DEBUG |

A JSON line with no level but a `status` of 500 or more counts as an error.

## Examples

The buttons above the search box fill in common queries:

| Button | Query |
|---|---|
| Errors | `level:error` |
| Warnings and errors | `level:warn OR level:error` |
| Crashed containers | `stream:previous` |
| HTTP 5xx | `status:5*` |
| Connection problems | `"connection refused" OR "connection reset" OR "timed out"` |
| Certificates | `certificate OR cert` |
| istiod pushes | `pod:istiod* push` |
| Denied / forbidden | `denied OR forbidden OR 403` |

## Time

The chart above the results shows matching lines over time, in UTC.

| To | Do |
|---|---|
| Zoom into a range | Drag across the chart with your mouse or trackpad. Keep dragging to drill down. |
| Zoom around a point | Pinch, or scroll, over the chart |
| Zoom out | Double-click the chart, or press **Zoom out** |
| Back to everything | **Whole capture** |
| Pick a relative range | The quick range menu: last 5 minutes, 15 minutes, hour, 6 hours or 24 hours **of the capture** |
| Pick exact times | The from and to boxes (UTC), then **Apply** |

Ranges are relative to the end of the capture, because "the last 15 minutes" of a snapshot taken yesterday means the 15 minutes before it was taken.

## Facets and fields

The left panel narrows the search without typing:

- **Level**, **Namespace**, **Workload**, **Pod** (with a box to find a pod when there are many), **Container**, **Stream**, and **Cluster** when the bundle has several. Each value shows how many matching lines it has. Click to filter, click again to clear.
- **Fields** lists structured fields parsed from the matching lines, such as `trace_id`, `status`, `method`, `path`, `src.workload` and `dst.addr`, each with its most common values. Click a value to add it to the query.

Active facet filters show as chips above the chart; click × to remove one. **Clear filters**, next to the Search button, resets the query, every facet and the time range in one go. It appears whenever anything is set.

## Context and following a request

Click a line (or focus it and press Enter) to open it in place:

- the three lines before and after it in the same container stream;
- **Inspect pod** opens the pod in the object drawer;
- **All lines from this stream** filters to that pod, container and stream;
- **Follow trace_id=... across pods** appears for any trace, request, span, correlation or connection ID on the line, and searches for that ID in every pod;
- the line's parsed fields as JSON;
- **Remove this line from the bundle** (bundles opened from a file): the line is left out of the bundle you export.

Trace, request and connection IDs are also shown inline on each line.

## Removing lines before you send

When the bundle was opened from a file, the header has **Remove matching lines**. It leaves every line the current search and filters match out of the exported bundle, after asking you to confirm. Up to 20,000 lines at a time; for more, add a pattern under **Drop log lines** on the Review page. An empty search is refused: to remove every line of a stream, remove the stream on the Review page. Picked lines are listed on the Review page's **Picked items** tab. See [Reviewing before you send](/collect/review/).

## While the bundle loads

Results cover what has loaded so far and say "bundle still loading". When more matching lines arrive, a banner offers to show them. Tick **Follow incoming data** to refresh automatically.

## Completion

The search box completes keys and values from the capture:

- `level:`, `ns:`, `pod:`, `container:`, `workload:`, `stream:`, `node:`, `scope:` and `cluster:` list values with line counts, narrowed by the other field terms in the box. `ns:` with `level:error` in the box lists namespaces that have errors.
- `since:` offers 5m, 15m, 30m, 1h, 6h and 24h.
- A bare word lists the fixed keys and the most common structured fields in the capture.

See [Search](/desk/search/) for the keys that drive it.

## From the terminal and agents

```bash
fbq 'level:error ns:istio-system since:2h "connection reset"'
fbq 'trace_id:a4e98f02' --limit 500
```

`fbq` prints the newest matching lines first (100 by default). The MCP `search_logs` and `log_context` tools use the same query language. For counting and grouping, use [SQL](/desk/sql/): the same queries compile to SQL over the `log_lines` table.
