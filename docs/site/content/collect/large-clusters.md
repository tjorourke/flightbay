---
title: Large clusters
weight: 62
description: How a capture stays fast without putting load on the API server, what the defaults are, and what to change on a very large or busy cluster.
---

# Large clusters

A capture reads every selected object type and every selected log stream once. On a large cluster that is thousands of requests, and most of the time goes on waiting for the API server and the kubelets, not on work in the collector. Flightbay reads in parallel to cut that waiting, within limits that keep the API server safe.

## What protects the API server

| Guard | Default | Why |
|---|---|---|
| Built-in types are read as protobuf | when the cluster is no newer than the collector | Cheaper for the API server to encode than JSON and fewer bytes; custom resources, and every type on a newer cluster, are read as JSON so no field is lost |
| Every list is paginated | 500 objects a page | No request asks the API server to build one huge response, including events and the pod list used for logs |
| List and get requests in flight | 4 (`--concurrency`) | Bounds the heavy lists running at once against the API server and etcd |
| List and get request rate | 50 a second, burst 100 (`--qps`, `--burst`) | The same client-side limit kubectl uses |
| Log streams in flight | 8 (`--log-concurrency`) | Bounds the load on the kubelets |
| Solo diagnostics in flight | 4 pods at a time (at most `--concurrency`) | Each is a port-forward through the API server to a kubelet, and a proxy dump can be tens of MB |
| Log request rate | 50 a second (`--log-qps`) | Limited separately, because a log request is proxied to a kubelet rather than served from etcd or the watch cache |
| Only reads | always | Get, list and pod logs, plus port-forward only with `--solo-diagnostics` |

On top of this, the API server's own priority and fairness queues Flightbay's requests with every other client's, and client-go honours a `429` and its `Retry-After`. A capture cannot starve the controllers or other users.

All list and get requests share one rate limiter, so `--qps` is the budget for the whole capture, not per API client.

Protobuf is only used when it cannot lose data: client-go decodes protobuf into typed structs, which would drop fields a newer Kubernetes added. When the cluster is newer than the Kubernetes version the collector was built against, every type is read as JSON instead, and the collection log says which encoding was used and why. The objects are the same either way: on a 1.35 kind cluster, all 5,554 objects matched between a JSON and a protobuf capture apart from fields that changed between the two runs (lease renewals, node heartbeats). An empty `status: {}` is dropped in both, because whether it appears depends on the encoding and the versions and it holds nothing. With the API server compressing responses as it does by default, protobuf sent about 22% fewer bytes for the same lists (0.7 MiB against 0.9 MiB) and the lists took about 18% less time.

CRD definitions are read one per selected custom type, in parallel under the same limits. A single list of all CRDs would move the same bytes (their OpenAPI schemas are large) in one response, and would need wider RBAC.

## What is quick and what is not

The limit on a big capture is nearly always the number of log streams. Each container, and each previous container that restarted, is one request, and its logs have to come back through the API server from the kubelet. 4,000 pods with two containers each is 8,000 log requests, which at the default 50 a second is close to three minutes before any bytes are counted.

Objects are cheaper: 200,000 objects is about 400 list requests.

Inside the collector, redaction checks each line for the literal text its rules need (`authorization`, `eyJ`, `PRIVATE KEY` and so on) before running any regular expression, so the millions of lines that carry no credential cost almost nothing. The output is the same as running every rule on every line, and a test holds it to that.

## Making a big capture quicker

In order of effect:

1. **Narrow the logs.** `--since 1h` instead of the 24 hour default, `--log-pods` and `--log-containers` for the workloads that matter, or `--log-streams current` to skip previous containers.
2. **Narrow the scope.** `--namespaces`, `--include-resources` or `--label-selector`.
3. **Raise the log rate on a cluster that can take it.** `--log-concurrency 16 --log-qps 100` doubles log throughput. Kubelets serve the logs, so watch node load on small nodes.

## Being gentler

On a production cluster that is already under pressure, halve everything:

```bash
flightbay collect --context prod --concurrency 2 --qps 20 --burst 40 --log-concurrency 4 --log-qps 20
```

The capture takes longer, and the API server sees at most two lists and four log streams from it at a time.

With an approved policy file (`--policy-key`), these flags are allowed because they change how fast Flightbay reads, not what it reads. They can also go in the policy file: `concurrency`, `qps`, `burst`, `logs.concurrency` and `logs.qps`.

## Measured

A kind cluster with 3 worker nodes, 300 pods (two containers each, both writing logs), 5,000 ConfigMaps and about 6,000 events, captured with `--cluster-scoped --since 5m`: 616 log streams and 1.9 million lines, a 23 MiB bundle. The API server's own metrics were read before and after each run.

| | Before these limits (1 request at a time, QPS 20) | Defaults | `--log-concurrency 16 --log-qps 100` |
|---|---|---|---|
| Time | 4 min 15 s | 1 min 44 s | 1 min 20 s |
| Throttled by the API server (`429`) | 0 | 0 | 0 |
| Rejected or queued by priority and fairness | 0 | 0 | 0 |
| Read requests in flight at the API server (peak) | 2 | 5 | 4 |

The bundles are the same: 616 streams, the same collections, and every file verified.

A synthetic cluster of 1,000 pods and 4 million log lines, with the same client-side limits and a 15 ms round trip on every request, took 1 min 57 s before and 44 s with the defaults. Collector CPU for that run fell from 70 s to 7 s, most of it from checking for credential literals before running the redaction rules.

## Opening a big bundle

The desk parses log chunks on up to 8 workers, each committing about 25,000 rows at a time to its database. About 4.3 million log lines load in 6 seconds on an 18-core laptop, and search is interactive while the rest arrives. Uncommitted rows are bounded by the worker count, not the bundle size, so a larger bundle takes longer, not more memory for loading. Findings are worked out once for each state of the workspace and shared by the Overview, Findings and the workspace summary; the repeated-error rule alone reads up to 200,000 error lines, which took about 2 seconds on every page load of a 4 million line bundle. While a bundle is still loading, findings are refreshed every few seconds rather than on every chunk.

Objects are kept in memory as well as in the database, because the Scorecard, Agent reach and the snapshot API read them; 200,000 objects use about 600 MiB.
