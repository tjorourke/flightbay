---
title: Proxy configs
weight: 110
description: Read what each Envoy, ztunnel and agentgateway had loaded, and istiod's view of the mesh, as captured.
---

# Proxy configs

Proxy configs shows what each proxy had actually loaded when the snapshot was taken: listeners, routes, clusters, endpoints, workloads, policies and certificates. For istiod it shows which proxies are in sync and what configuration it holds. Use it when the objects look right and the traffic does not behave: the answer is usually in the difference between what was applied and what the proxy has.

![Proxy configs](/images/proxies.png)

This page needs [Solo diagnostics](/collect/solo-diagnostics/) in the capture. Without them it says the diagnostics were not selected, and that their absence says nothing about the proxies.

## Finding a component

The list on the left groups the captured components: **ztunnel**, **Envoy** (sidecars, Istio gateways and waypoints), **agentgateway**, **istiod** and **Controller** (agentgateway and kgateway controllers). Each pod shows a dot for whether every file collected completely, and a marker if a certificate has expired or is close to expiry.

Tags above the list summarise each component per cluster: version and pod count, with the image on hover.

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
