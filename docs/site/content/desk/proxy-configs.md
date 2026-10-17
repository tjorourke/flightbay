---
title: Proxy diagnostics
weight: 110
description: Read what each Envoy, ztunnel and agentgateway had loaded, and istiod's view of the mesh, as captured.
---

# Proxy diagnostics

Proxy diagnostics shows what each proxy had actually loaded when the snapshot was taken: listeners, routes, clusters, endpoints, workloads, policies and certificates. For istiod it shows which proxies are in sync and what configuration it holds. This read-only page covers Istio and agentgateway. Use [Istio Ambient](/desk/ambient/) for setup and reviewed changes, or [Traffic](/desk/traffic/) for observed requests.

![Proxy configs](/images/proxies.png)

This page needs [Proxy and control-plane diagnostics](/collect/diagnostics/) in the capture. Without them it says the diagnostics were not selected, and that their absence says nothing about the proxies.

## Finding a component

The list on the left groups the captured components: **ztunnel**, **Envoy** (sidecars, Istio gateways and waypoints), **agentgateway**, **istiod** and **Controller** (agentgateway and kgateway controllers). Each pod shows a dot for whether every file collected completely, and a marker if a certificate has expired or is close to expiry.

Tags above the list summarise each component per cluster: version and pod count, with the image on hover.

**Component family** narrows the list to **Istio** (Envoy, ztunnel and istiod), **Agentgateway** (proxies and controller diagnostics), or **All components**. If a family has no captured components, **Show all component families** clears the filter. The controller group can also contain kgateway controllers.

## Summary tab

Counts at the top (workloads, services, listeners, clusters, routes, stale proxies and so on, depending on the component), notes about anything that could not be read, then:

| Component | Tables |
|---|---|
| ztunnel | Workloads (namespace, name, IPs, service account, node, protocol, waypoint), Services (hostname, VIPs, endpoints, waypoint), Authorization policies (action, scope), certificates |
| Envoy | Listeners (address, filter chains), Clusters (type, endpoints), Routes (route config, virtual host, domains), certificates |
| agentgateway | Listeners and routes (bind, listener, protocol, route, matches, backends), Policies, Backends |
| istiod | Connected proxies from syncz (proxy, type, version, sync state; STALE shown in red), Configuration from configz (kind, namespace, name) |

The certificates table lists each identity, whether it is a root or workload certificate, its state, expiry and time left, measured at snapshot time. Workload certificates are short-lived by design, so a few hours left is normal; an expired one, or a root with under 30 days, is highlighted.

The filter box narrows every table to matching rows.

Policy names have resource and **YAML** actions when their captured kind, namespace and name resolve uniquely in the same cluster. A policy present only in the delivered dump, or without an unambiguous captured object, stays labelled as dump-only. A similarly named policy in another namespace is not substituted.

![Agentgateway delivered policies with links to captured resource YAML](/images/proxy-policy-links.png)

## Files tab

Every file captured for the pod, with its size, state and any note (for example "SDS secret sections removed" or "13 entries naming namespaces outside the selection removed"). Click one to read it; JSON is pretty-printed. The filter box shows only matching lines. Files are shown as captured: redacted and namespace-filtered at collection.

## From the terminal and agents

```bash
fbpc                                  # list captured components
fbpc ztunnel-5b4bw                    # workloads, services, policies, certificates
fbpc ai-gateway --section routes      # only tables whose title contains "routes"
fbpc istiod --file syncz.json | jq    # a raw captured file
fbpc --cluster mesh1 -n istio-system ztunnel-5b4bw
```

A pod name prefix is enough when it is unique. The button at the top of the page runs `fbpc` for the selected pod. The MCP `proxy_config` tool does the same for agents.

## Related findings

[Findings](/desk/findings/) reads the same data for proxies that have not acknowledged config (`xds.stale`), NACKs (`xds.nack`), expired and expiring certificates, and version skew.
