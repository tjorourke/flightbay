---
title: MCP server
weight: 150
description: Give an AI agent read-only access to an open workspace over the Model Context Protocol, with tools for search, logs, SQL, findings, scores, reach and proxy configs.
---

# MCP server

`flightbay-desk mcp` makes the open workspace available to AI agents over the Model Context Protocol. The agent gets read-only tools for everything the desk can do: search objects and logs, read-only SQL, findings, best-practice scores, agent reach, objects, proxy configs and comparisons, plus a `triage` prompt that walks it through an investigation.

Use it when you want an assistant to do the legwork on a case: gather the evidence behind the findings, follow a trace across pods, or check whether a problem started with a change.

## Connecting an agent

Open the bundle first, then point your agent at `flightbay-desk mcp`. It speaks MCP over stdin and stdout, and finds the open desk through `~/.flightbay/current.json` (or `FLIGHTBAY_URL` and `FLIGHTBAY_TOKEN`).

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

Any other MCP client works the same way: the command is `flightbay-desk mcp` with no arguments. The server always talks to the most recently opened desk.

### Over HTTP

The desk also serves MCP at `/mcp` on its own address, for agents that connect over HTTP (for example a kagent `RemoteMCPServer`). It uses the streamable HTTP transport with plain JSON responses: POST one JSON-RPC message, get one back. It needs the desk token as `Authorization: Bearer`, like the rest of the API.

## Tools

Every tool is read-only and marked as such.

| Tool | Arguments | Returns |
|---|---|---|
| `workspace_summary` | none | Case, clusters, capture time, the collection policy (what the customer allowed), import and verification state, and finding counts. Agents are told to call this first. |
| `list_findings` | `cluster`, `severity` | [Findings](/desk/findings/) with evidence, a log query and a next step |
| `search_logs` | `query` (required), `cluster`, `namespace`, `pod`, `limit` (50) | Matching log lines, newest first, with IDs, a total and facets. Uses the [log query language](/desk/logs/). An empty result says whether logs were excluded or are still loading. |
| `log_context` | `id` (required), `lines` (10) | The lines around one log line, in its own stream |
| `search_objects` | `query` (required), `cluster`, `limit` (50) | Objects matching the [object search syntax](/desk/search/), ranked, with snippets and counts per kind |
| `run_sql` | `query` (required), `limit` (200) | One read-only DuckDB statement. See [SQL](/desk/sql/). |
| `best_practices` | `area`, `status` | The [Scorecard](/desk/scorecard/): scores and grades per area, and every check with its status, objects, reason and fix YAML. With Flightbay Enterprise, also `priorities`. |
| `agent_reach` | `agent` (namespace/name) | [Agent reach](/desk/agent-reach/): every agent, or one in full. Needs Flightbay Enterprise. |
| `agent_activity` | `view` (`calls`, `tools`, `models`, `denials`, `changes`) | [Activity](/desk/activity/): callers, tools, models and agents called through agentgateway, refusals with the policies behind them, tokens, and tool changes. Needs Flightbay Enterprise. |
| `traffic_map` | `flag` (`plaintext`, `plaintext-in`, `unpoliced`, `denied`, `mismatch`) | The [Traffic map](/desk/traffic/): every ztunnel edge with transport, waypoint, verdict and flags. Needs Flightbay Enterprise. |
| `explain_connection` | `from`, `to` (required), `cluster` | Why a workload can or cannot reach a Service: every check in order, the policies each read, the logs, and a fix. Needs Flightbay Enterprise. |
| `list_resource_types` | `cluster` | Captured types with counts and collection state (complete, excluded, failed, truncated, loading) |
| `list_objects` | `resource` (required), `group`, `cluster`, `namespace`, `filter` | Objects of one type, with the columns `kubectl get` prints |
| `get_object` | `cluster`, `resource`, `name` (required), `group`, `namespace` | One object as captured (redacted YAML), its related objects, its events and, for pods, its log streams |
| `proxy_config` | `pod`, `namespace`, `cluster`, `file` | With no pod, what was captured. With a pod, its summary tables. With `file`, that raw file. |
| `compare_baseline` | none | When the desk was opened with a baseline, what changed. See [Compare](/desk/compare/). |

Responses longer than 60,000 characters are cut, with a note asking the agent to narrow the request.

## The triage prompt

The server offers one prompt, `triage` ("Triage this snapshot"). It tells the agent to:

1. call `workspace_summary`, note what the customer excluded, what is still loading and whether the bundle verified, and never treat missing data as evidence of absence;
2. call `list_findings` and `best_practices`, starting from critical findings;
3. gather evidence for each promising finding with `search_logs`, `log_context`, `get_object`, `proxy_config`, `search_objects` and `run_sql`;
4. follow trace, request and connection IDs across pods;
5. check `compare_baseline` for a change that lines up.

It asks for a reply with the most likely root cause and a confidence level, evidence that cites where each item came from (finding, object, log line ID with pod and time, or proxy file), what the snapshot cannot tell you and what to ask the customer for, and next steps. It tells the agent not to invent data.

In Claude Code, run it with `/mcp__flightbay__triage`.

## Where the data goes

The MCP server only talks to your local desk. Snapshot data reaches a model only if the agent you connect uses a hosted one. If the customer's data must stay on your machine, connect an agent that runs a local model, or do not connect one.
