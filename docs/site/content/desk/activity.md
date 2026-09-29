---
title: Activity
weight: 92
description: Who called which MCP tool, model or agent through agentgateway, as whom, what was refused and why, what it cost in tokens, and which tools changed since a baseline.
---

# Activity

Activity shows what agents actually did through agentgateway: who called which MCP tool, model or agent, as whom, how often, what was refused and why, and what it cost in tokens. [Agent reach](/desk/agent-reach/) answers what an agent *could* reach; Activity answers what it *did*.

Activity is part of [Flightbay Enterprise](/enterprise/). Turn it on with `FLIGHTBAY_ENTERPRISE=1 flightbay ui` or `FLIGHTBAY_FEATURES=activity`.

![Callers on the left, the tools, models and agents they called on the right](/images/activity-map.png)

## Where the data comes from

Everything is read from agentgateway request logs in the capture: the `request` lines agentgateway writes for every request, from gateways and from agentgateway waypoints, in text or JSON. The fields it reads are the ones agentgateway's request logger writes:

| What | Log fields |
|---|---|
| Caller | `src.identity` (waypoints log the caller's SPIFFE ID), `src.addr`, `jwt.sub` |
| MCP | `mcp.method.name`, `mcp.target`, `gen_ai.tool.name` (and `mcp.method`, `mcp.tool` from older builds) |
| LLM | `gen_ai.provider.name`, `gen_ai.request.model`, `gen_ai.response.model`, `gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens`, the cache token counts, `agw.ai.usage.cost.total` |
| A2A | `a2a.method` |
| Outcome | `http.status`, `reason`, `error` |
| Where | `gateway`, `listener`, `route`, `http.host` |

A line under the numbers lists the gateways that logged requests, with counts, so an empty page can be told apart from a gateway whose logs were not collected.

## Callers

Each request is put down to one caller, in this order:

1. The workload behind the SPIFFE ID agentgateway logged, when only one workload uses that service account.
2. The workload that owns the source pod address.
3. The service account, when several workloads share it.
4. The JWT subject.
5. "Outside the cluster, through node …" when the address is a node's, or the `.1` gateway address of a pod range (where traffic from outside arrives once it is SNATed).
6. The address itself.

JWT subjects seen on a caller's requests are listed with it. A caller whose requests carry neither a mesh identity nor a JWT subject is marked **no identity**.

## Targets

| Kind | A request is this when |
|---|---|
| Tool | an MCP `tools/call` with a tool name. Named `server/tool`, where the server is the MCP target, or the route when there is no target. |
| MCP session | any other MCP request: `initialize`, `tools/list`, notifications, and calls refused before the tool name was read |
| Model | the log names a provider or a model, or the protocol is `llm` |
| Agent | an A2A request |
| Other HTTP route | anything else through the gateway. Hidden on the map by default. |

## Refused and failed

A request is **refused** when agentgateway gives a policy reason (`JwtAuth`, `Oidc`, `BasicAuth`, `APIKeyAuth`, `ExtAuth`, `Authorization`, `RateLimit`, `Guardrail`), or, with no reason, a 401, 403 or 429. It **failed** when the reason is `MCP`, `UpstreamFailure`, `Timeout`, `NoHealthyBackend`, `NotFound`, `Internal` or `ExtProc`, or the status is 5xx.

The **Refused** tab groups refusals by caller, target, gateway, route and reason. Each group says what the reason means, shows one logged error, lists the agentgateway policies (OSS and Enterprise) attached to the route, the gateway, or the Service a waypoint serves, with the sections each one sets, and marks as **likely** the ones with a section that produces that reason: `traffic.jwtAuthentication` for `JwtAuth`, `backend.mcp.authorization` or `traffic.authorization` for `Authorization`, and so on. Click a policy to open it.

## Tool lists

kagent reports the tools each MCP server offers in `status.discoveredTools` on `RemoteMCPServer`, `MCPServer` and `ToolServer`. A called tool is checked against a server's list only when the call can be matched to that server, by the server's name, its URL host, or the Service in its URL. The **Tools** tab says for each tool called whether its server lists it: yes, **no**, or unknown when no captured list matches. It also lists the tools servers offer that were never called.

A declarative kagent `Agent` names the tools it may use (`toolNames`). A call from that agent to a tool outside the list is flagged. The list only filters what the model is offered; it is not enforced on the wire.

## Tool changes

With a baseline loaded (on the [Compare](/desk/compare/) page, or `flightbay-desk open --baseline`), the **Tool changes** tab compares the two captures:

| Change | Meaning |
|---|---|
| Description changed | Same server, same tool, different description. A tool's description is part of the prompt the model reads, so a change without a new release is how a tool is turned against its callers (a rug pull). A high finding. |
| Added | A server lists a tool it did not list in the baseline. Only said for servers whose list was captured both times. |
| Removed | The reverse. |
| Called now, not called in the baseline | A tool with calls in this capture and none in the baseline |
| Caller and target pairs not in the baseline | A caller calling a tool, model or agent it did not call in the baseline |

Calls are compared only when the baseline's logs have some tool or model calls; otherwise the tab says so, since two captures with log windows that do not overlap would make every call look new.

## Findings

| Finding | Severity |
|---|---|
| Tools, models or agents answered calls that carried no identity | high |
| Agents called tools their Agent spec does not list | high |
| MCP tools changed their description since the baseline | high |
| Tools were called that their MCP server does not list | medium |
| MCP servers offer tools that were not there in the baseline | medium |
| LLM spend that cannot be put down to a caller | medium |
| Calls were refused by gateway policy | low |

## Using the page

The numbers across the top are the totals: requests, tool calls, tokens, refused, failed, and calls with no identity.

**Map.** Callers on the left, what they called on the right. The chips choose which kinds of target to draw; the filter matches callers, targets and gateways. Line width follows the number of calls. Red lines are calls outside an agent's tool list, to a tool its server does not list, or where every call was refused; amber lines have no identity or some refusals; dashed lines have refusals. Hover a box to trace its lines. Click a box or a line for the detail underneath: a caller's identity and every call it made, a target's description, routes and gateways, or one caller-target pair's statuses, reasons, tokens, first and last time, and a logged error. **Log lines** opens the Log explorer on the requests behind a pair.

![A caller's detail](/images/activity-caller.png)

**Calls**, **Tools**, **Models** and **Refused** are the same data as tables. A caller that is a workload has **Agent reach** and **Traffic map** buttons, and `#activity?caller=kagent/tom-demo` opens the page on one caller. A refusal at an agentgateway waypoint has a **Why?** link to the mesh checks between the caller and the Service; the agentgateway policy listed with it is checked after them. **Models** has tokens and cost per model and per caller.

## In the terminal and over MCP

```bash
fbactivity                 # caller -> tool, model or agent, with findings
fbactivity tools           # every tool called, and whether its server lists it
fbactivity models          # tokens per model and per caller
fbactivity denials         # refused calls and the policies that could have refused them
fbactivity changes         # tools added, removed or re-described since the baseline
```

The MCP `agent_activity` tool returns the report; `view` trims it to `calls`, `tools`, `models`, `denials` or `changes`.

## Limits

- Only what agentgateway logged is here. Traffic that does not go through an agentgateway gateway or waypoint, such as an agent calling an MCP server directly, is on the [Traffic map](/desk/traffic/) instead, without tool names.
- agentgateway logs `jwt.sub` only when a JWT policy validated a token. A request with no JWT policy on its route shows no subject even if it carried a token.
- Token counts and cost come from the gateway's own accounting. Cost is only logged when a cost table is configured.
- Tool lists come from kagent. MCP servers outside kagent have no list to check against, so their tools show as unknown.
