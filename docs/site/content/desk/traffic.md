---
title: Traffic map
weight: 94
description: Every connection ztunnel logged, with its mTLS state, the waypoint that carried it and what the captured policy says, and an explainer for why any connection was allowed or denied.
---

# Traffic map

The Traffic map draws every connection ztunnel logged between workloads and Services: whether it went over mutual TLS or in plaintext, which waypoint carried it, and what the captured policy says about it. The **Why?** explainer takes one source and one destination and shows every check on the way, in order, with the policy each one read.

The verdicts come from the same evaluator as [Agent reach](/desk/agent-reach/), so the map, the explainer and the reach report always agree.

The Traffic map is part of [Flightbay Enterprise](/enterprise/). Turn it on with `FLIGHTBAY_ENTERPRISE=1 flightbay ui` or `FLIGHTBAY_FEATURES=traffic`.

![Connections, coloured by what needs attention](/images/traffic-map.png)

## Where the data comes from

ztunnel access logs in the capture: `access` lines (one per connection) and, in Solo builds, `http_access` lines (one per HTTP request). A connection is counted once when both ztunnels logged it. The map uses:

| What | Log fields |
|---|---|
| Source | `src.workload`, `src.namespace`, `src.identity` |
| Destination | `dst.service`, `dst.workload`, `dst.namespace`, `dst.identity` |
| Transport | `dst.hbone_addr`, or `dst.addr` on port 15008, means HBONE (mutual TLS between ztunnels). Anything else is plaintext. |
| Outcome | `response_code` 401 or 403, or an `error` that mentions policy, counts as refused |
| Volume | `bytes_sent`, `bytes_recv`, `method`, `path` (without the query string) |

Sources are resolved to workloads by pod name, then SPIFFE identity. Destinations are resolved to the captured Service by host name, else to the pod. A connection to a waypoint is drawn to the Service it serves, "via" the waypoint, and the waypoint's own connection onward is folded into that edge.

## What each edge says

| Field | Meaning |
|---|---|
| Transport | **mTLS** (all HBONE), **plaintext**, or **mixed** |
| Policy | The evaluator's verdict for the source and the Service: allowed, denied or unknown, and at L4 or L7 |
| Policed | Some NetworkPolicy or AuthorizationPolicy looks at this traffic |
| Via | The waypoint that carried it |

Edges are flagged:

| Flag | When |
|---|---|
| Plaintext in the mesh | Plaintext between two workloads that are both enrolled in ambient. They should be on HBONE; usually a workload opted out, a sidecar and ambient mix, or an excluded port. Red. |
| Plaintext from outside the mesh | A workload that is not enrolled reached one that is. It has no identity for policy to match, and PERMISSIVE mode let it in. |
| No policy | No NetworkPolicy or AuthorizationPolicy applies. |
| Refused | Some connections were refused. |
| Logs and policy disagree | Every connection was refused although policy allows it at L4, or some got through although policy denies it. The policy may be newer than the traffic, or apply differently from how it reads. |

![The totals are filters](/images/traffic-stats.png)

## Why? The explainer

Pick an edge on the map or the Edges tab, or use the **Why? Explain a connection** tab with any workload and Service in the capture, not only pairs seen in the logs. The explainer runs every check traffic meets, in order, and shows what each decided and the policy it read:

1. **NetworkPolicy egress** from the source. In ambient, between two mesh pods this is port 15008 (HBONE), not the Service port.
2. **NetworkPolicy ingress** to the destination. With a waypoint, the path is source to waypoint to destination, and each leg is checked.
3. **mTLS**: whether both ends are in the mesh, and whether a STRICT PeerAuthentication refuses a caller outside it.
4. **ztunnel authorization (L4)**: AuthorizationPolicies selecting the destination, DENY before ALLOW. An ALLOW rule with HTTP attributes matches nothing at ztunnel.
5. **Waypoint authorization (L7)**: AuthorizationPolicies attached to the Service or its waypoint with `targetRefs`, where methods and paths apply.

Each step shows **allowed**, **denied**, **unknown** (the data was not captured), or **nothing applies**, with links to the policies. A Service with several pods is checked pod by pod until one admits the caller.

![Refused at ztunnel authorization, with the fix](/images/traffic-explain.png)

Under the checks:

- **Not evaluated from a snapshot**: conditions the evaluator cannot judge, such as JWT claims, `when:` clauses, `ipBlocks`, external authorisation (CUSTOM), and agentgateway policies on a waypoint.
- **In the access logs**: connections, refusals, HBONE and plaintext seen for the pair, and whether that agrees with the verdict.
- **To allow it**: when the call is refused only because no ALLOW rule admits it, the smallest AuthorizationPolicy that would (by selector at ztunnel, or `targetRefs` at a waypoint), with the trust domain read from the access logs. A NetworkPolicy refusal gets an egress rule. A DENY rule, or STRICT mTLS for a caller outside the mesh, gets advice instead of YAML: those need a decision.

A Service with no pods whose waypoint answers for it (an agentgateway waypoint in front of a backend outside the cluster, for example) is evaluated to the waypoint.

`#traffic?from=agents/eval-agent&to=tools/postgres` opens the explainer on that pair.

## Using the page

The numbers across the top are the totals, and clicking one keeps only those edges. The chips do the same, and the namespace picker and filter narrow the map further.

**Map.** Sources on the left, destinations on the right. Box colour is green in the mesh, amber not in the mesh, grey outside the capture. Line colour is red for plaintext in the mesh; amber for no policy, plaintext from outside the mesh, or disagreement; green for mutual TLS with a policy looking at it. Dashed lines have refusals or a denied verdict. Hover a box to trace its lines; click a box for its edges, or a line for its detail and explanation.

**Edges.** The same as a table, with a **Why?** button per row.

## In the terminal and over MCP

```bash
fbtraffic                          # every edge, worst first
fbtraffic --flag plaintext         # plaintext between mesh workloads
fbtraffic --flag unpoliced --fix   # with the default-deny YAML
fbtraffic --namespace kagent
fbexplain kagent/tom-demo kagent/github-mcp
fbexplain agents/eval-agent postgres.tools.svc.cluster.local
```

`fbexplain` takes a workload as `namespace/name`, a pod, or a SPIFFE ID, and a Service as `namespace/name` or a host name. A name that does not exist gets suggestions.

The MCP tools are `traffic_map` (with `flag` to keep only those edges) and `explain_connection` (`from`, `to`, and `cluster` when there are several).

## Limits

- ztunnel logs only pods it captures. Traffic between pods outside ambient is not on the map.
- A capture's logs cover its log window. An edge absent from the map was not seen in that window, which is not the same as never happening.
- The verdict is the captured policy's. Policy the customer excluded from the bundle shows as unknown, never as allowed or denied.
