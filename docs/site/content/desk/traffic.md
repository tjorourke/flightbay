---
title: Traffic
weight: 94
description: Every connection ztunnel logged and every call through agentgateway on one map, with mTLS state, waypoints, policy verdicts, tools, models, tokens and refusals, and an explainer for why any connection was allowed or denied.
---

# Traffic

The Traffic page shows what happened on the cluster, in two layers on one map:

| Layer | From | Shows |
|---|---|---|
| **Network** | ztunnel access logs | Every connection between workloads and Services: mutual TLS or plaintext, the waypoint that carried it, refusals, and what the captured policy says about it |
| **Gateway calls** | agentgateway request logs | Every call through an agentgateway gateway or waypoint: which MCP tool, model or agent was called, as whom, how often, what was refused and why, and the tokens used. See [Gateway calls](/desk/activity/) for how these are read. |

The **Why?** explainer takes one source and one destination and shows every check on the way, in order, with the policy each one read. The verdicts come from the same evaluator as the [Agents](/desk/agent-reach/) page, so the map, the explainer and each agent's reach always agree.

For one agent at a time (what it may reach, what it used, and the policy that would lock it down to that), open the **Agents** tab, or click an agent's name in a finding. See [Agents tab](/desk/agent-reach/).

Traffic is part of [Flightbay Enterprise](/enterprise/). It is on by default; `FLIGHTBAY_FEATURES=-traffic` turns it off (`activity`, the name of the gateway layer's old page, still works as a name). `AGENT=1` opens the desk on this page in [agent mode](/desk/agent-mode/).

![Traffic from the captured network connections and gateway calls](/images/traffic-map.png)

## Findings

The Findings panel has the network's findings (plaintext, traffic no policy looks at), the gateways' (calls with no identity, refusals, tools outside an agent's list) and every agent's reach findings, grouped so each is listed once with the agents it applies to (`Reaches the internet directly · 7 agents`). Click an agent's name in a finding to open it on the Agents tab, where its fix is. The five most serious show first; **Show all** lists the rest.

![Totals, the grouped findings, and the tabs](/images/traffic-findings.png)

## Found by

**Found by** above the map chooses what it draws, by where it comes from, with a count for each:

| Choice | Draws |
|---|---|
| Any | Everything: connections, gateway calls, and every tool behind each Service |
| ztunnel connections | The network layer only |
| agentgateway calls | The gateway layer only: a box per tool, model and agent called |
| Tools called | Services with only the tools that were called behind them |
| Tools allowed | Only the tools an allowlist allows (every tool of a server without one), called or not |
| Tools configured | Only the tools named in an agent's `toolNames` or `ALLOWED_TOOLS` |

The menu also lists the other flow sources in the capture (Hubble, VPC flow logs and so on), which each agent's reach uses on the Agents tab. The Connections and Gateway calls totals set it too.

## Layers

**Found by** (above) chooses the layer: ztunnel connections, agentgateway calls, or both. A workload that does both appears once on the left.

With both layers on, the map has three columns: sources, the Services they reached, and **Tools**, every tool of the MCP servers behind each Service, called or not. The Service box counts them against the allowlist (`4 of 5 tools allowed · 2 called`: the fifth is not allowed to anyone; a tool can be allowed and called, so the two numbers overlap), and each tool is coloured by what the captured allowlists say:

| Tool | Means |
|---|---|
| green, solid | Called, and allowed for every caller that called it (or the server has no allowlist) |
| red | Called by a caller the allowlist does not allow (`called, not allowed for some` when others are allowed): the policy is probably newer than the calls |
| amber | Allowed, and the server was reached through Code Mode (`run_code`), whose logs do not say which tools ran: it may have run there |
| amber, solid | On a server **no allowlist covers**: in an agent's configuration, but not enforced. The Service box says `no allowlist · N in agents' config`, and the other tools read `not in any config, not blocked`. The Tools column header gives a line per server when there are one or two, for example `github-mcp · 13 allowed · 1 called`, and totals otherwise. With a single agent selected it leaves out servers no allowlist covers; the amber boxes and the finding still show them |
| faded, dashed | Allowed, not called; or not allowed at all |

An agent's own tool list (kagent `toolNames`, or `ALLOWED_TOOLS` on a BYO agent) only filters what the model is offered. It is enforced only where an agentgateway allowlist (`backend.mcp.authorization`) covers the server and the agent's calls go through agentgateway (a gateway route or an agentgateway waypoint). When they do not, the Findings panel has **An agent's tool list is not enforced**, with the agent, the server, how many of its tools the list names, and what to add.

An agent's configured MCP servers are drawn even when the capture has no call to them: a dashed line to the Service its own configuration reaches the server at (`MCP_SERVERS_CONFIG`, or a declarative Agent's RemoteMCPServer), and the tools it is configured for or allowed in the Tools column, kept out of the folded box. So an agent given GitHub and Kubernetes SRE tools shows both, even if it only used GitHub. An agent that has not called anything yet, such as one deployed a moment ago, is drawn the same way: on a live mirror its servers and tools appear as soon as its Agent is created.

`run_code` and `get_tool`, the meta-tools of agentgateway's Code and Search modes, get boxes of their own beside the real tools. See [Tool modes](/desk/activity/#tool-modes-code-and-search).

**Found by** chooses which to draw (Tools called, allowed or configured); the same choice applies when following one agent. A server with more than 20 unused tools (a kagent server lists all of its own) folds them into one box. Hover an agent to trace it through its Service to the tools it called; hover a tool to see who called it; click a tool for who may call it (the allowlist and the agents configured for it) and who did, with a button to see every tool one of them may call.

The middle column holds Services only. A call through agentgateway is drawn on the Service it went through (the waypoint's Service, or the gateway's), and the box says how many tools were called on it (`kagent/github-mcp · Service · 2 tools called`). Hover the line for the tools and counts, or click the Service for a table of every call through it. When ztunnel also logged the connection, the calls are added to that line; a caller whose connections are not in the capture gets a line of its own. Choose **Gateway calls** for a box per tool, model and agent.

### What a caller may call

Click a source on the left and the map redraws with only what it reaches: its destinations and the tools behind them it called or may call, with the column headers counting just those. Every source stays in the left column, so you can click through agents one after another. Click it again, click the background, or press Esc for the whole map. Clicking any other box keeps its lines lit (the same trace hover shows) while you scroll or zoom. A pinned agent lights its Service and the tools it called; a pinned tool lights everyone who called it. Clicking also opens the box's detail under the map.

**Show only its traffic** in an agent's panel follows it. The map then draws a box for every tool it has access to on each MCP server it uses: called tools first (green; red when the captured allowlist would not allow it), then tools in its own configuration, then the rest it is allowed and did not call (faded). A server with more than 16 such tools folds the unused ones into one box; click it to open it up. The Services it may reach on the network and did not use are behind **Show Services it may reach and did not use** in the bar above the map.

![Gateway calls with an agent selected](/images/tool-map.png)

Clicking a tool box, or a caller's line to a Service, shows the same as a table under the map, **Tools … may call**.

- When an agentgateway allowlist (`backend.mcp.authorization` with action `Allow`) covers the server, the caller may call the tools a clause names for it, and those named for any caller. The allowlist is named. A caller it does not name may call none. A rule of `false`, which is how an approval flow starts, covers the server and allows nothing, so an agent that has not been approved shows no tools.
- With no allowlist, the caller may call every tool the server offers: its tool list for a kagent server, or, for a server outside the cluster, the tools known from calls, other allowlists and agent configuration, with a note that it may offer more. The server is drawn only when the network lets the agent reach the Service its configuration points at; when a NetworkPolicy or mesh authorization blocks that path, the line to the Service is red and dashed and no tools are listed.

Flightbay reads the rule as CEL, not by pattern: `||` and `&&` with brackets, `!`, `true` and `false`, `field == "value"` and `field in ["a", "b"]`. A clause admits:

| The clause tests | Shown as |
|---|---|
| `source.identity.namespace` and `source.identity.serviceAccount` | `kagent/tom2-agent` |
| `source.identity.serviceAccount` alone | `service account tom2-agent`, and a finding: any namespace with a service account of that name passes |
| `source.identity.namespace` alone | `namespace kagent` |
| `jwt.sub`, or another JWT claim | `JWT subject alice`, `JWT team sre` |
| none of these | `any caller` |

An agent's configuration (`MCP_SERVERS_CONFIG`) often names a server its own way, such as `site-daylight` for a Service whose waypoint route sends to the backend target `daylight`. Flightbay follows the Service's route to the target, so the agent's configured tools, the allowlist and the calls land on one server. A rule that names both `x` and a prefixed spelling (`site_daylight_x`) counts as one tool.

A clause with no `mcp.tool.name` test gives its callers every tool of the server. Anything else a clause tests (a method call such as `jwt.groups.exists(...)`, a negation, another field) cannot be decided from a snapshot: the caller is shown with `(if ...)` and counted as allowed. Rules with action `Deny` take tools away from the callers they name. A policy whose `Accepted` or `Attached` condition is `False` is left out, with a note, because the gateway leaves it out too. An expression Flightbay cannot parse at all is listed under the server, and what it allows is not shown.

Each tool says **allowed, called**, **allowed, not called**, or **called, not allowed** (called although the captured allowlist does not give it to this caller: the policy may be newer than the calls), with the calls and whether the agent's own configuration lists it.

![An agent's tools and models from the captured configuration and calls](/images/tool-access.png)

![The gateway layer: callers and the tools, models and agents they called](/images/activity-map.png)

## Where the data comes from

ztunnel access logs in the capture: `access` lines (one per connection) and, in builds with HTTP access-log support, `http_access` lines (one per HTTP request). A connection is counted once when both ztunnels logged it. The map uses:

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

![The totals: each one is a filter](/images/traffic-stats.png)

The totals across the top cover both layers: connections, gateway calls (with tool and LLM calls and tokens), plaintext in the mesh, traffic no policy looks at, refusals by the mesh and by the gateways, and calls with no identity. Each is a filter or opens its tab.

## Why? The explainer

Pick an edge on the map or the Edges tab, or use the **Why? Explain a connection** tab with any workload and Service in the capture, not only pairs seen in the logs. The explainer runs every check traffic meets, in order, and shows what each decided and the policy it read:

1. **NetworkPolicy egress** from the source. In ambient, between two mesh pods this is port 15008 (HBONE), not the Service port.
2. **NetworkPolicy ingress** to the destination. With a waypoint, the path is source to waypoint to destination, and each leg is checked.
3. **mTLS**: whether both ends are in the mesh, and whether a STRICT PeerAuthentication refuses a caller outside it.
4. **ztunnel authorization (L4)**: AuthorizationPolicies selecting the destination, DENY before ALLOW. An ALLOW rule with HTTP attributes matches nothing at ztunnel.
5. **Waypoint authorization (L7)**: AuthorizationPolicies attached to the Service or its waypoint with `targetRefs`, where methods and paths apply.

Each step shows **allowed**, **denied**, **unknown** (the data was not captured), or **nothing applies**, with links to the policies. A Service with several pods is checked pod by pod until one admits the caller.

![Explaining the connection from an agent to an MCP Service](/images/traffic-explain.png)

Under the checks:

- **Not evaluated from a snapshot**: conditions the evaluator cannot judge, such as JWT claims, `when:` clauses, `ipBlocks`, external authorisation (CUSTOM), and agentgateway policies on a waypoint.
- **In the access logs**: connections, refusals, HBONE and plaintext seen for the pair, and whether that agrees with the verdict.
- **To allow it**: when the call is refused only because no ALLOW rule admits it, the smallest AuthorizationPolicy that would (by selector at ztunnel, or `targetRefs` at a waypoint), with the trust domain read from the access logs. A NetworkPolicy refusal gets an egress rule. A DENY rule, or STRICT mTLS for a caller outside the mesh, gets advice instead of YAML: those need a decision.

A Service with no pods whose waypoint answers for it (an agentgateway waypoint in front of a backend outside the cluster, for example) is evaluated to the waypoint.

`#traffic?from=agents/eval-agent&to=tools/postgres` opens the explainer on that pair.

## Using the page

| Tab | Shows |
|---|---|
| Map | Sources on the left, destinations and what was called on the right. Box colour is green in the mesh, amber not in the mesh, grey outside the capture. For connections, line colour is red for plaintext in the mesh, amber for no policy, plaintext from outside the mesh or disagreement, green for mutual TLS with a policy looking at it. For gateway calls, red is a call outside an agent's tool list, to a tool its server does not list, or with every call refused; amber is no identity or some refusals. Dashed lines have refusals. Hover a box to trace its lines; click a box or a line for its detail underneath. |
| Connections | The network layer as a table, with a **Why?** button per row |
| Calls | The gateway layer as a table: caller, target, calls, refusals, failures, tokens, gateway |
| Agents | Every agent, MCP server and agent gateway, worst first; click one for its reach, what it did, its tools, the lock-down and its findings. See [Agents tab](/desk/agent-reach/). |
| Tools and models | Every tool the capture names, per MCP server: how often it was called and by whom, who an agentgateway allowlist allows to call it, which agents are configured for it, and whether the server lists it. Then tokens and cost per model and per caller. See [Gateway calls](/desk/activity/#tool-lists). |
| Refused | Refused gateway calls, with the agentgateway policies that produce each reason, and connections refused by the mesh |
| Tool changes | With a baseline loaded: tools added, removed or whose description changed |
| Why? | The explainer for any workload and Service |

Clicking a workload that made gateway calls shows both sides: its connections, and its calls with identity, JWT subjects and tokens. **Show only its traffic** follows it (see below).

## Filtering

The filter box takes free text or `key:value` terms, with suggestions from the capture as you type. Values match as a case-insensitive prefix, a leading `-` excludes, and terms combine. A key only one layer has keeps that layer's edges: `tool:list_` shows gateway calls, `flag:unpoliced` shows connections.

| Key | Matches | Example |
|---|---|---|
| `from:` (also `caller:`) | Source: workload, service account or address | `from:kagent/tom-demo` |
| `to:` | Destination Service or workload, or the tool, model or agent called | `to:kagent/github-mcp` |
| `layer:` | `network` or `gateway` | `layer:gateway` |
| `tool:`, `server:`, `model:`, `gateway:`, `route:`, `user:`, `state:` | Gateway calls; see [Gateway calls](/desk/activity/#filtering) | `server:github state:refused` |
| `ns:` | Namespace at either end | `ns:kagent` |
| `flag:` | `plaintext`, `plaintext-in`, `unpoliced`, `denied`, `mismatch` | `flag:unpoliced` |
| `tls:` | `mtls`, `plaintext`, `mixed` | `tls:plaintext` |
| `verdict:` | `allowed`, `denied`, `unknown` | `verdict:denied` |
| `via:` | The waypoint that carried it | `via:github-mcp-waypoint` |
| `kind:` | `agent` for a source that is an agent; `service`, `workload` or `external` for the destination | `kind:agent` |

## Gone agents and workloads

The page shows what is running now. A caller whose workload no longer exists is left off: a service account that called through agentgateway but has no workload in the capture, a pod address no pod holds any more (in a node's pod range, or the /24 of the capture's pods when nodes were not collected), and a pod ztunnel logged by name that has since been deleted. Their calls, refusals, finding items and the tools known only from their calls go with them. **Show gone N** in the chips brings them back, for when you are looking for what a deleted agent did. On a live mirror this follows the cluster: delete an agent and it drops off the map at the next refresh.

## Following one agent

**Agents only** keeps the traffic of agents, MCP servers and agent gateways, and of service accounts that called MCP tools, models or agents through agentgateway. Those are agents whose workload was not captured; ones whose workload is gone are hidden unless **Show gone** is on. Click a workload and **Show only its traffic**, or open `#traffic?agent=kagent/tom-demo` (the **Traffic** button on the [Agents](/desk/agent-reach/) page does this), to follow one: its connections and its gateway calls.

For an agent, the map then also draws what policy allows it to reach and it did not use, faded and dashed and folded by namespace. Click a namespace to open it up, and a Service to explain why it is allowed. The bar above the map counts both, and links to the agent on the Agents page.

![The traffic map focused on an agent](/images/traffic-focus.png)

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
