---
title: Gateway calls
weight: 95
description: The gateway layer of the Traffic page - who called which MCP tool, model or agent through agentgateway, as whom, what was refused and why, what it cost in tokens, and which tools changed since a baseline.
---

# Gateway calls

Gateway calls are what agents actually did through agentgateway: who called which MCP tool, model or agent, as whom, how often, what was refused and why, and what it cost in tokens. They are the **Gateway calls** layer of the [Traffic](/desk/traffic/) page, and each agent's share is on its **Tools and models** tab on the [Agents](/desk/agent-reach/) page. This page explains how they are read.

They were a page of their own, called Activity; `#activity` links now open Traffic with the gateway layer on, and `#activity?caller=kagent/tom-demo` filters it to that caller.

Gateway calls are part of [Flightbay Enterprise](/enterprise/), with the Traffic feature (`FLIGHTBAY_FEATURES=traffic`).

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

The **Tools and models** tab of the Traffic page lists, per MCP server, every tool the capture names, from four places:

| Source | Read from |
|---|---|
| Called | agentgateway request logs (`gen_ai.tool.name`) |
| Listed | kagent `RemoteMCPServer`, `MCPServer` and `ToolServer` status (`discoveredTools`) |
| Allowed for | agentgateway MCP authorization rules (`backend.mcp.authorization`, OSS and Enterprise): each `\|\|` clause that names tools (`mcp.tool.name in [...]` or `== "..."`), with the caller it names (`source.identity.serviceAccount`, with `source.identity.namespace` when given, or `jwt.sub`). The server is the MCP target of the backend the rule targets, and a tool spelt with the target prefix (`github_list_issues`) counts as the same tool. |
| Configured on | Not an enforcement: the agent's own list only filters what the model is offered. A declarative kagent Agent's `toolNames`, and an `ALLOWED_TOOLS` variable (a JSON list) on a BYO agent. With several servers in its `MCP_SERVERS_CONFIG`, a tool goes on the one server known to offer it, or under "(server not known)". |

![GitHub's tools: called, allowed per service account by an allowlist, and configured on an agent](/images/traffic-tools.png)

**MCP servers** chooses which servers to list: a searchable list with a tick per server (most called first, with its tool count), and **Select all** and **Clear**. The first time, the servers with calls are ticked. Beside it, a line per server says where its tools come from; with more than three ticked, a summary does.

A server outside the cluster, such as GitHub's at `api.githubcopilot.com` behind an agentgateway backend, has no full list in the capture: it is not a kagent MCP server (only those report one), and agentgateway does not log the tools a server offers. Its tools are the ones seen in the capture: called, in an allowlist, or in an agent's configuration. It may offer more.

## Tool modes: Code and Search

An agentgateway MCP backend can expose its tools four ways (`toolMode`), and what the logs say about which tool ran depends on it:

| Mode | The agent sees | The request log records | The allowlist is checked |
|---|---|---|---|
| Standard | the server's tools | the tool called | for each tool |
| Search | `get_tool` and `invoke_tool` (progressive disclosure) | `get_tool` for a lookup (not which tool it looked up); `invoke_tool` as the tool it invoked | for each tool |
| Code | one tool, `run_code`: its JavaScript calls the server's tools | `run_code` only, under the target `code_mode` | for each tool the code calls |
| CodeSearch | `get_tool` and `run_code` | as Search and Code | for each tool the code calls |

So in every mode an agent may call only the tools the allowlist gives it, and Flightbay shows all of them, but in Code mode the logs cannot say which of those the code used. Flightbay handles that this way:

- `run_code` and `get_tool` are **meta-tools**, not the server's tools. Each is put on the server behind its route (the route's agentgateway backend and that backend's MCP targets) instead of a pretend server called `code_mode`, and drawn as its own box: **Code Mode entry point** or **Search mode lookup**, with its calls.
- On a server reached through `run_code`, every tool the caller is allowed and did not call directly shows as **allowed, may have run in run_code** (amber), not "not called". A tool called directly (Standard or Search mode) shows as called.
- The Tools and models tab says each server's current `toolMode`. When the calls show Code Mode but the backend is in Standard mode now, it says the calls were made before it changed.
- An agent's **Tools … may call** table lists its `run_code` and `get_tool` calls first, then every tool it may call, with the same states.

`code_mode` in a raw log line is the target agentgateway gives `run_code`; it is not an MCP server.

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

On the [Traffic](/desk/traffic/) page, choose **Gateway calls** (or leave **Both** on). The chips choose which kinds of target to draw: tools, models, agents (A2A), MCP sessions, and other HTTP routes (off by default). Line width follows the number of calls. Click a caller, a target or a line for its detail underneath: a caller's identity, JWT subjects and every call it made; a target's description, routes and gateways; or one caller-target pair's statuses, reasons, tokens, first and last time, and a logged error. **Log lines** opens the Log explorer on the requests behind a pair.

![A caller's detail](/images/activity-caller.png)

The **Calls**, **Tools and models**, **Refused** and **Tool changes** tabs of the Traffic page are the same data as tables. A caller that is an agent has **Open on Agents**; a refusal at an agentgateway waypoint has a **Why?** link to the mesh checks between the caller and the Service, and the agentgateway policy listed with it is checked after them.

## Filtering

These are the gateway keys of the Traffic page's filter box. It takes free text or `key:value` terms, with suggestions from the capture as you type. Values match as a case-insensitive prefix, a leading `-` excludes, and terms combine. `caller:` is the same as `from:`.

| Key | Matches | Example |
|---|---|---|
| `caller:` | Who called: workload, service account, or address | `caller:kagent/tom-demo` |
| `tool:` | MCP tool name | `tool:list_` |
| `server:` | MCP server (target) or its route | `server:github` |
| `model:` | LLM model or provider | `model:claude` |
| `kind:` | `tool`, `model`, `agent`, `mcp` or `http`. Overrides the kind chips. | `kind:tool` |
| `gateway:` | agentgateway gateway or waypoint | `gateway:kagent/` |
| `route:` | Gateway route | `route:kagent/github-mcp` |
| `user:` | JWT subject | `user:alice` |
| `state:` | `ok`, `refused`, `failed`, `noidentity`, `outsidelist` | `state:refused` |

```text
server:github state:refused
tool:delete -caller:kagent/
kind:model state:noidentity
```

On the Tools and models tab, the tools servers list and no one called match on `tool:`, `server:` and text only, and are shown open when the filter leaves 30 or fewer.

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
