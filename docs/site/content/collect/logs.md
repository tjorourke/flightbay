---
title: Logs
weight: 20
description: Control how much container log data a capture takes, from which pods and containers, and over what time window.
---

# Logs

Container logs can account for much of a bundle's size. Use these options to limit their volume and select the pods and containers to include.

Logs are part of the `standard` profile. They are not in `minimal`, and `custom` has none until you add the `logs` category.

## Defaults

With the `standard` profile Flightbay reads, for every container of every pod in scope:

- the **current** container's log, and the **previous** container's log if it has restarted;
- lines from the **last 24 hours**;
- up to **20 MiB per stream**, keeping the newest lines when a stream is longer.

Logs are read through `pods/log` with `timestamps=true`, so every line carries the kubelet's timestamp. They are redacted line by line as they are read.

## Options

| Flag | Config field | Default (`standard`) | Effect |
|---|---|---|---|
| `--since 2h` | `logs.since` | `24h` | Only lines newer than this. Any Go duration: `30m`, `6h`, `72h`. |
| `--max-log-bytes 10485760` | `logs.maxBytes` | `20971520` (20 MiB) | Per-stream limit in bytes. The newest lines are kept. |
| `--log-streams current,previous` | `logs.streams` | `current,previous` | Which containers and streams to read, see below. |
| `--log-pods 'checkout-*,istiod-*'` | `logs.pods` | all | Only pods whose name matches one of these globs. |
| `--log-containers 'istio-proxy,app'` | `logs.containers` | all | Only containers whose name matches one of these globs. |
| `--no-logs` | (drop `logs` from `categories`) | | No container logs at all. |
| `--log-concurrency 8` | `logs.concurrency` | `8` | Log streams read at once. |
| `--log-qps 50` | `logs.qps` | `50` | Log requests per second, limited separately from list requests. See [Large clusters](/collect/large-clusters/). |

`--max-log-bytes` takes a plain number of bytes. `10MiB` is not accepted.

## Streams

| Value | What it adds |
|---|---|
| `current` | The running container's log. |
| `previous` | The log of the container before its last restart. Only read for containers that have restarted. This is usually where a crash loop's stack trace is. |
| `init` | Init containers. Their logs are stored as the `current` (and `previous`) stream of that container. |
| `ephemeral` | Ephemeral debug containers added with `kubectl debug`. Stored the same way. |

```bash
flightbay collect --log-streams current,previous,init
```

## Examples

```bash
# The last two hours only
flightbay collect --since 2h

# Smaller streams for a very large cluster
flightbay collect --max-log-bytes 5242880

# Only the mesh data plane and control plane
flightbay collect --namespaces istio-system,payments --log-containers 'istio-proxy,discovery'

# Objects and events, no logs
flightbay collect --no-logs
```

## What gets recorded

Every stream has a coverage record in the bundle saying how many lines it holds and whether it is complete, truncated (the byte limit was reached, with the limit named) or failed (with the reason, such as a container still waiting to start). The [Coverage](/desk/coverage/) page in the desk shows these, so a missing stream never looks like a quiet container.

When the kubelet returns an error instead of log lines (it does this with HTTP 200 while a container is restarting), Flightbay retries, and if it still fails records the stream as failed rather than storing the error as if the application printed it.

## Logs that feed other features

The desk also parses logs for analysis:

- **ztunnel, agentgateway, Envoy and CoreDNS logs** become observed traffic for [Agent reach](/desk/agent-reach/) and `fbflows`. See [Observed traffic](/collect/observed-traffic/).
- **Error lines** feed the repeated-error finding and error-spike entries on the [Timeline](/desk/timeline/).

If you limit logs with `--log-pods` or `--log-containers`, keep `ztunnel` and your gateways in if you want to see who called whom.
