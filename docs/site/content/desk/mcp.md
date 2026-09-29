---
title: MCP server
weight: 150
description: Give an AI agent read-only access to an open workspace over the Model Context Protocol, with tools, resources, completions and investigation prompts for objects, logs, findings, scores, reach and proxy configs.
---

# MCP server

`flightbay-desk mcp` makes the open workspace available to AI agents over the Model Context Protocol. The agent gets read-only tools for everything the desk can do: search objects and logs, read-only SQL, findings, best-practice scores, agent reach, objects, proxy configs, the timeline, coverage and comparisons. Every object, log stream, proxy dump, finding and Scorecard check is also a `flightbay://` resource that you can attach to a conversation, with completions for the names in each address. Seven prompts walk the agent through common investigations.

Use it when you want an assistant to do the legwork on a case: gather the evidence behind the findings, follow a trace across pods, or check whether a problem started with a change.

## Connecting an agent

Open the bundle, then point your agent at `flightbay-desk mcp`. It speaks MCP over stdin and stdout. It looks for the open desks on every call, through the registry in `~/.flightbay/desks/`, so the agent can be started before the bundle is opened, and it follows a desk that is closed and opened again without restarting the agent. `FLIGHTBAY_URL` and `FLIGHTBAY_TOKEN` pin it to one desk instead.

Claude Code:

```bash
flightbay ui flightbay-01234567-20260927T1012Z.tar.zst      # in one terminal
claude mcp add flightbay -- flightbay-desk mcp               # once
```

opencode, in `opencode.json`:

```json
{
  "mcp": {
    "flightbay": {"type": "local", "command": ["flightbay-desk", "mcp"]}
  }
}
```

Any other MCP client works the same way: the command is `flightbay-desk mcp` with no arguments. The tools read the most recently opened desk that is still running. With more than one desk open, the `workspaces` tool lists them and switches between them.

### The agent only reads the snapshot

A bundle is usually captured in a customer's environment, and the clusters in it are not reachable from your machine. The server tells the agent so when it connects: answer from the tools and resources only, and never run `kubectl`, a cloud CLI or anything else against a cluster, or call the desk's HTTP API, in their place. When no desk is open, or the desk has closed, every tool returns an error that says the same thing and asks the agent to have you open the bundle, rather than looking for another way in. The desk token is handled by the server; you never sign in on the agent's behalf.

### Over HTTP

The desk also serves MCP at `/mcp` on its own address, for agents that connect over HTTP (for example a kagent `RemoteMCPServer`). It uses the streamable HTTP transport with plain JSON responses: POST one JSON-RPC message, get one back. It needs the desk token as `Authorization: Bearer`, like the rest of the API.

## Tools

Every tool is read-only and marked as such.

| Tool | Arguments | Returns |
|---|---|---|
| `workspaces` | `use` | The desks open on this machine, most recently opened first, and which one the other tools read. `use` (a list number, bundle file name or live context) switches desk. Only offered by `flightbay-desk mcp`, not by a desk's `/mcp`, which always reads its own workspace. |
| `workspace_summary` | none | Case, clusters, capture time, the collection policy (what the customer allowed), import and verification state, and finding counts. Agents are told to call this first. |
| `list_findings` | `cluster`, `severity` | [Findings](/desk/findings/) with evidence, a log query and a next step. Links each finding and the object it is about as resources. |
| `timeline` | `cluster`, `kind` (`event`, `termination`, `rollout`, `error-spike`), `since` (RFC 3339) | The [Timeline](/desk/timeline/): events, container exits, rollouts and error spikes in order, each with its object and a log query |
| `coverage` | `cluster`, `state` (e.g. `failed`, `excluded`), `type` (`resource`, `events`, `logs`, `diagnostic`, `flows`) | What each collection produced and its state with the reason, the collection log and the redaction report. With a filter, only the log entries that did not succeed. |
| `search_logs` | `query` (required), `cluster`, `namespace`, `pod`, `limit` (50) | Matching log lines, newest first, with IDs, a total and facets. Uses the [log query language](/desk/logs/). An empty result says whether logs were excluded or are still loading. |
| `log_context` | `id` (required), `lines` (10) | The lines around one log line, in its own stream |
| `search_objects` | `query` (required), `cluster`, `limit` (50) | Objects matching the [object search syntax](/desk/search/), ranked, with snippets and counts per kind |
| `run_sql` | `query` (required), `limit` (200) | One read-only DuckDB statement. See [SQL](/desk/sql/). |
| `best_practices` | `area`, `status` | The [Scorecard](/desk/scorecard/): scores and grades per area, and every check with its status, objects, reason and fix YAML. With Flightbay Enterprise, also `priorities`. |
| `agent_reach` | `agent` (namespace/name) | [Agent reach](/desk/agent-reach/): every agent, or one in full. Needs Flightbay Enterprise. |
| `agent_activity` | `view` (`calls`, `tools`, `models`, `denials`, `changes`) | [Gateway calls](/desk/activity/): callers, tools, models and agents called through agentgateway, refusals with the policies behind them, tokens, and tool changes. Needs Flightbay Enterprise. |
| `traffic_map` | `flag` (`plaintext`, `plaintext-in`, `unpoliced`, `denied`, `mismatch`) | The [Traffic](/desk/traffic/) network layer: every ztunnel edge with transport, waypoint, verdict and flags. Needs Flightbay Enterprise. |
| `explain_connection` | `from`, `to` (required), `cluster` | Why a workload can or cannot reach a Service: every check in order, the policies each read, the logs, and a fix. Needs Flightbay Enterprise. |
| `list_resource_types` | `cluster` | Captured types with counts and collection state (complete, excluded, failed, truncated, loading) |
| `list_objects` | `resource` (required), `group`, `cluster`, `namespace`, `filter` | Objects of one type, with the columns `kubectl get` prints |
| `get_object` | `cluster`, `resource`, `name` (required), `group`, `namespace` | One object as captured (redacted YAML), its related objects, its events and, for pods, its log streams. Links the log streams and related objects as resources. |
| `proxy_config` | `pod`, `namespace`, `cluster`, `file` | With no pod, what was captured. With a pod, its summary tables. With `file`, that raw file. |
| `compare_baseline` | none | When the desk was opened with a baseline, what changed. See [Compare](/desk/compare/). |

Responses longer than 60,000 characters are cut, with a note asking the agent to narrow the request.

Where a tool result names things the agent is likely to open next, it adds `resource_link` items after the text: `list_findings` links each finding and its object, and `get_object` links a pod's log streams and the related objects. A client that supports links can open them without another tool call.

## Resources

Resources are the snapshot as documents. A client can list them, read them and attach them to a conversation, the way you would attach a file. In Claude Code, type `@` and pick one, for example `@flightbay:flightbay://findings/f1`.

`resources/list` returns the fixed documents first, then every finding, every failing or warning Scorecard check, every captured proxy or control-plane file and every captured log stream:

| Resource | Contents |
|---|---|
| `flightbay://workspace` | The workspace summary, the same as `workspace_summary` |
| `flightbay://findings` | Every finding |
| `flightbay://findings/{id}` | One finding |
| `flightbay://scorecard` | Area scores, priorities, and an index of the failing and warning checks with their reason, the number of affected objects and each check's address |
| `flightbay://scorecard/{check}` | One check in full: affected objects, why it matters and the fix YAML |
| `flightbay://coverage` | Coverage, the collection log and the redaction report |
| `flightbay://timeline` | The timeline |

Three templates address anything else in the snapshot:

| Template | Contents |
|---|---|
| `flightbay://objects/{cluster}/{group}/{resource}/{namespace}/{name}` | One object as captured (redacted YAML), then its events. `group` is `core` for core types and `namespace` is `-` for cluster-scoped objects, for example `flightbay://objects/prod/core/pods/shop/checkout-7d9f` or `flightbay://objects/prod/rbac.authorization.k8s.io/clusterroles/-/admin`. |
| `flightbay://logs/{cluster}/{namespace}/{pod}/{container}/{stream}` | The newest 2,000 lines of one log stream (`current` or `previous`), oldest first, each with its time and log line ID for `log_context`. A note at the top says when there are more; `search_logs` reads further back. |
| `flightbay://proxies/{cluster}/{kind}/{namespace}/{pod}/{file}` | One captured diagnostic file as it was captured. `kind` is `proxies` or `control-plane`. |

Each part of an address is URL path-escaped. Resources are limited to 60,000 characters, like tool results, which is why the Scorecard resource is an index and each check has its own address.

## Completions

The server completes template and prompt arguments from the snapshot, and narrows them by the arguments already chosen: a namespace narrows the pods offered, a pod narrows the containers and streams, a resource type narrows the names. Clusters, groups and resource types come from what was captured; log stream parts only include streams that have lines. The `agent` argument of `agent_review` completes from Agent reach when Flightbay Enterprise is on.

## Prompts

Each prompt gives the agent a plan that uses the tools above and asks for a reply that cites its evidence (a finding ID, an object, a log line ID with pod and time, or a proxy file), says what the snapshot cannot tell you and what to ask the customer for, and does not invent data. Some embed resources, so the agent starts with them already read.

| Prompt | Arguments | What it does | Embeds |
|---|---|---|---|
| `triage` | `symptom` | Root-cause investigation, focused on the symptom when you give one: workspace summary, findings and Scorecard, evidence from logs, objects and proxy dumps, identifiers followed across pods, and a check against the baseline | workspace, findings |
| `pod_debug` | `namespace`, `pod` (required), `cluster` | Why one pod is unhealthy: status and exit codes, events, previous and current logs, findings for it and its owner, the Services and policies in front of it, and what the timeline shows came first | |
| `explain_denial` | `from`, `to` (required) | Why a call was denied: the rule that decided (`explain_connection`), confirmed in ztunnel, waypoint and agentgateway logs, and the smallest policy change that allows it | |
| `security_review` | none | The Scorecard and Agent reach turned into a ranked table of fixes with YAML, each claim checked against the objects and coverage | scorecard, coverage |
| `agent_review` | `agent` (required) | One agent, MCP server or gateway: what it can reach, its credentials, what it actually called, and the paths to close | |
| `what_changed` | none | Changes since the baseline bundle, lined up with the timeline and new findings, or the timeline alone when there is no baseline | |
| `incident_timeline` | `since` | A table of what happened when, from the timeline, the logs in each spike and ids followed across pods, with the first failure marked | timeline |

In Claude Code, run them as `/mcp__flightbay__triage`, `/mcp__flightbay__pod_debug shop checkout-7d9f` and so on.

## Where the data goes

The MCP server only talks to your local desk. Snapshot data reaches a model only if the agent you connect uses a hosted one. If the customer's data must stay on your machine, connect an agent that runs a local model, or do not connect one.
