---
title: Solo diagnostics
weight: 40
description: Add Envoy, ztunnel and agentgateway config dumps, istiod debug output and metrics to a capture, filtered to the namespaces you chose.
---

# Solo diagnostics

Diagnostics capture the configuration loaded by Envoy, ztunnel and agentgateway, plus istiod's sync status and debug output. Compare these with Kubernetes objects when investigating configuration that has not reached a proxy.

These endpoints are only reachable on each pod's localhost admin port, so Flightbay reads them through port-forward. Diagnostics are off by default.

## Turning them on

```bash
flightbay collect --context mesh1 --solo-diagnostics
```

`--solo-diagnostics` adds three categories (`proxies`, `control-plane` and `metrics`) and allows the port-forward they need. You can also pick them one at a time:

```bash
flightbay collect --collect objects,events,logs,proxies --portforward
flightbay collect --solo-diagnostics --no-proxy-dumps      # control plane and metrics, no proxy dumps
```

| Flag | Effect |
|---|---|
| `--solo-diagnostics` | Adds `proxies`, `control-plane`, `metrics` and allows port-forward |
| `--portforward` | Allows port-forward for whichever diagnostic categories you selected |
| `--no-portforward` | Never port-forward. Diagnostic categories become "not collected" with the reason. |
| `--no-proxy-dumps` | Drops the `proxies` category |

The dry run says whether diagnostics will run, and the RBAC it prints includes `pods/portforward`.

## What is read

Components are recognised by their container image among the pods in scope. Only pods in the selected namespaces are touched.

| Component | Category | Port | Path |
|---|---|---|---|
| ztunnel | proxies | 15000 | `/config_dump` |
| ztunnel | metrics | 15020 | `/metrics` |
| Envoy (sidecars, Istio gateways and waypoints) | proxies | 15000 | `/config_dump?include_eds`, `/clusters?format=json`, `/listeners?format=json`, `/server_info`, `/certs`, `/stats` |
| Envoy | metrics | 15020 | `/stats/prometheus` |
| agentgateway (gateways and waypoints) | proxies | 15000 | `/config_dump` |
| agentgateway | metrics | 15020 | `/metrics` |
| istiod | control-plane | 8080 | `/debug/syncz`, `/debug/configz`, `/debug/registryz`, `/debug/endpointz`, `/debug/mesh`, `/debug/connections` |
| istiod | metrics | 15014 | `/metrics` |
| agentgateway and kgateway controllers | metrics | 9092 | `/metrics` |

Each port-forward binds to `127.0.0.1` on an ephemeral port and is closed as soon as that pod's endpoints have been read. It uses WebSockets first and falls back to SPDY for older API servers, the same way `kubectl` does.

## Filtered to your namespaces

ztunnel, agentgateway and istiod describe the whole mesh, not just the namespaces you selected. Before anything is written:

- entries in ztunnel, agentgateway and istiod output that name a namespace outside the selection are removed;
- each object in istiod's `/debug/configz` is checked against the resource, namespace and cluster-scoped selection;
- `/debug/endpointz` entries for other namespaces are removed.

The bundle records how many entries were removed from each file, and `flightbay inspect` shows it:

```text
kind-flightbay-e2e  config_dump.json   1 items   complete: 13 entries naming namespaces outside the selection removed
kind-flightbay-e2e  endpointz.json     1 items   complete: 2 entries naming namespaces outside the selection removed
```

## Resource exclusions

If you exclude a resource type, diagnostics that would reveal it are cut down or skipped:

| Endpoint | When excluded |
|---|---|
| ztunnel `/config_dump` | `policies` removed if `authorizationpolicies` is excluded; `services` removed if `services` is; `workloads` removed if `pods` is |
| Envoy `/config_dump`, `/clusters` | skipped if `services` is excluded |
| agentgateway `/config_dump` | `services` and `workloads` removed as for ztunnel |
| istiod `/debug/registryz` | skipped if `services` is excluded |
| istiod `/debug/endpointz` | skipped if `services` or `endpointslices` is excluded |

Envoy SDS secret sections are always removed from config dumps, whatever the policy. Certificate details for expiry checks come from `/certs`, which holds no keys.

## Excluded controller snapshots

The agentgateway and kgateway controllers are built on a library called krt, which keeps an in-memory copy of every Kubernetes object the controller watches, including the Secrets it reads (TLS keys, API keys for LLM providers). The admin port can dump all of it as one document. It covers every namespace and contains Secret values, so it cannot honour a namespace limit or "never Secret values". Flightbay never reads it, and collects the controllers' logs, metrics and objects instead.

## Where it shows up in the desk

- [Proxy configs](/desk/proxy-configs/): every component, with summary tables and the raw files.
- [Findings](/desk/findings/): proxies that have not acknowledged config, NACKs, expired or expiring certificates, version skew between istiod and ztunnel, and between agentgateway proxies and their controller.
- `fbpc` in the [terminal](/desk/terminal/).

If a capture has no diagnostics, the desk says they were not selected. It never treats that as evidence about the proxies.
