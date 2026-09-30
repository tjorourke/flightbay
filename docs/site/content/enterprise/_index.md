---
title: Flightbay Enterprise
weight: 40
description: What Flightbay Enterprise adds to the desk, how to turn it off, and agent mode.
---

# Flightbay Enterprise

The collector and the desk work fully without Enterprise. Flightbay Enterprise adds three features to the desk, for Solo support and, later, licensed customers.

| Feature | Name | Adds |
|---|---|---|
| Priorities | `priorities` | The Scorecard's priority view: severity tags coloured by weight, a ranked **Fix these first** list, and the points each fix gives back. `fbscore --top N`. `priorities` in the MCP `best_practices` tool. See [Scorecard](/desk/scorecard/). |
| Agent reach | `reach` | The **Agents** tab of the Traffic page, `fbreach`, the MCP `agent_reach` tool, and five agentic Scorecard checks built on it. See [Agent reach](/desk/agent-reach/). |
| Traffic | `traffic` | The **Traffic** page: ztunnel connections (mTLS, waypoints, policy verdicts) and agentgateway calls (tools, models, tokens, refusals, tool changes) on one map, and the **Why?** explainer. On the Agents page, what each agent used and called, and the lock-down built from it. `fbtraffic`, `fbactivity`, `fbexplain`, the MCP `traffic_map`, `agent_activity` and `explain_connection` tools. `activity` still works as a name for it. See [Traffic](/desk/traffic/). |

The collector has no Enterprise features. A customer never needs Enterprise to capture a bundle.

## Turning it off

Every feature is on by default. They are read from the environment of the desk process:

```bash
flightbay ui                                                # every Enterprise feature
FLIGHTBAY_FEATURES=-reach flightbay ui                      # everything except reach
FLIGHTBAY_ENTERPRISE=0 flightbay ui                         # none of them
FLIGHTBAY_ENTERPRISE=0 FLIGHTBAY_FEATURES=traffic flightbay ui   # only traffic
```

| Variable | Values |
|---|---|
| `FLIGHTBAY_ENTERPRISE` | `0`, `false`, `no` or `off` turns every feature off; anything else, or unset, leaves them on |
| `FLIGHTBAY_FEATURES` | A comma-separated list of feature names. `all` or `enterprise` turns them all on; a leading `-` turns one off. |

Set them on the process that opens the bundle (`flightbay ui` or `flightbay-desk open`). The helpers and `flightbay-desk mcp` ask that desk, so they follow its setting.

## Agent mode

`AGENT=1` (or `FLIGHTBAY_AGENT=1`, or `flightbay ui --agent`) trims the desk to what matters for agents: **Traffic** (opened first), the **Scorecard** with only the agentic area, **Resources** and the **Log explorer**. The rest of the navigation, Save as bundle and Export and sign are hidden, and links to other pages land on Traffic. It works on a bundle and on a live cluster:

```bash
AGENT=1 flightbay ui --live --context kind-mesh1
flightbay ui --agent flightbay-DEMO-20260927T1842Z.tar.zst
```

## Checking what is on

```bash
flightbay-desk features
```

```text
priorities   on   Best practices: fix-first ranking, points recovered per fix, severity colours
reach        on   Agent reach: every agent's blast radius, the network paths it has and what to lock down
traffic      on   Traffic: ztunnel connections (mTLS, waypoints, policy verdicts) and agentgateway calls (tools, models, tokens, refusals, tool changes), and why a connection was denied
```

Run it with the same environment as the desk.

## What you see without it

- The Scorecard shows plain severity tags and "Most serious problems" instead of "Fix these first", with no points. `fbscore --top` is refused.
- **Agents** is not in the sidebar or in ⌘K. Going to `#reach` directly shows a page saying it is an Enterprise feature and how to turn it on. `fbreach` and the MCP `agent_reach` tool return an error saying the same.
- **Traffic** is hidden the same way; `fbtraffic`, `fbactivity`, `fbexplain` and their MCP tools say they need Enterprise.
- The Scorecard runs its 130 standard checks, without the five reach checks.

Pages, tools and helpers that show Enterprise features carry an **Enterprise** label.

## A product switch, not a security boundary

These variables decide what the desk shows. They do not protect anything: the desk runs on your machine, and anyone who can run it can set them. Anything that must be protected (a hosted service, uploads) enforces its own authentication and authorisation.
