---
title: Agent mode
weight: 16
description: A desk trimmed to agents, MCP servers and agentgateway, opened on Traffic, on a bundle or a live cluster.
---

# Agent mode

Agent mode is the desk for someone who only cares about the agents on a cluster: which agents there are, what they reached, which MCP tools they called or may call, and what the agentgateway allowlists and the mesh let through. It hides everything else.

```bash
flightbay ui --agent --live --context kind-mesh1     # a live cluster, read-only
AGENT=1 flightbay ui --live --context kind-mesh1     # the same
flightbay ui --agent flightbay-DEMO-20260927T1842Z.tar.zst
```

`--agent` is a `flightbay ui` flag. `AGENT=1` or `FLIGHTBAY_AGENT=1` does the same for `flightbay ui` and for `flightbay-desk open`, `live` and `start`. The start page keeps it, so a live cluster or a bundle chosen there opens in agent mode too.

![Agent mode: Traffic first, and only Scorecard, Resources and the Log explorer beside it](/images/agent-mode.png)

## What changes

| | Agent mode |
|---|---|
| Navigation | **Traffic**, **Scorecard**, **Resources** and **Log explorer**, in that order. The other pages are gone from the sidebar, and a link or bookmark to one of them lands on Traffic. |
| First page | [Traffic](/desk/traffic/), instead of the Overview. The brand reads **flightbay agent** and links to Traffic. |
| Scorecard | Only the agentic area: agentgateway, kagent, MCP, A2A and Agent Registry. The fix-first list keeps only agentic problems and is numbered from 1 again. See [Scorecard](/desk/scorecard/). |
| Live mirror | **Save as bundle** and **Export and sign** are hidden. The mirror is read-only either way. |

The object search at the top of the page, the terminal, the helpers and SQL are unchanged.

The Scorecard trim is done by the desk, not the page, so `fbscore` in the terminal and the MCP `best_practices` tool see the agentic area alone as well. The other MCP tools and helpers answer as they do in the full desk.

## Working through the agents

Traffic opens with every source on the left, the Services they reached in the middle and the MCP tools behind them on the right. **Agents only** in the chips keeps the traffic of agents, MCP servers and agent gateways.

Click an agent and the map redraws with only what that agent reaches: its destinations and the tools it called or may call, with the column headers counting just those. The other sources stay on the left, so the next agent is one click away. Click the agent again, click the background, or press Esc for the whole map.

![kagent/demo-agent selected: its two MCP Services and the two tools it called, which the captured allowlists do not allow](/images/agent-mode-select.png)

The panel under the map has the agent's connections and gateway calls, and **Show only its traffic**, which follows it: every tool it has access to on each MCP server, called or not. The **Agents** tab has each agent's reach, the policy that would lock it down, and its findings. See [Traffic](/desk/traffic/#what-a-caller-may-call) and [Agents tab](/desk/agent-reach/).

## What it needs

Traffic is part of [Flightbay Enterprise](/enterprise/), which is on by default. Agent mode is built around it, so do not combine it with `FLIGHTBAY_ENTERPRISE=0`.

The map is only as good as the capture. Tool calls come from agentgateway request logs, connections from ztunnel access logs, and allowlists and agent configuration from the captured objects. A live mirror has all of them as long as the credentials can read the logs. See [Coverage](/desk/coverage/) for what a bundle holds.
