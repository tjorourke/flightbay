---
title: Flightbay Enterprise
weight: 40
description: What Flightbay Enterprise adds to the desk, and how to turn it on.
---

# Flightbay Enterprise

The collector and the desk work fully without Enterprise. Flightbay Enterprise adds four features to the desk, for Solo support and, later, licensed customers.

| Feature | Name | Adds |
|---|---|---|
| Priorities | `priorities` | The Scorecard's priority view: severity tags coloured by weight, a ranked **Fix these first** list, and the points each fix gives back. `fbscore --top N`. `priorities` in the MCP `best_practices` tool. See [Scorecard](/desk/scorecard/). |
| Agent reach | `reach` | The **Agent reach** page, `fbreach`, the MCP `agent_reach` tool, and five agentic Scorecard checks built on it. See [Agent reach](/desk/agent-reach/). |
| Activity | `activity` | The **Activity** page: who called which MCP tool, model or agent through agentgateway, refusals and the policies behind them, tokens, and tool changes since a baseline. `fbactivity`, the MCP `agent_activity` tool. See [Activity](/desk/activity/). |
| Traffic map | `traffic` | The **Traffic map** page: ambient connections with mTLS state, waypoint and policy verdict, and the **Why?** explainer for any source and Service. `fbtraffic`, `fbexplain`, the MCP `traffic_map` and `explain_connection` tools. See [Traffic map](/desk/traffic/). |

The collector has no Enterprise features. A customer never needs Enterprise to capture a bundle.

## Turning it on

Features are off by default. They are read from the environment of the desk process:

```bash
FLIGHTBAY_ENTERPRISE=1 flightbay ui                         # every Enterprise feature
FLIGHTBAY_FEATURES=priorities flightbay ui                  # just this one
FLIGHTBAY_FEATURES=priorities,reach flightbay ui            # these
FLIGHTBAY_ENTERPRISE=1 FLIGHTBAY_FEATURES=-reach flightbay ui   # everything except reach
```

| Variable | Values |
|---|---|
| `FLIGHTBAY_ENTERPRISE` | `1`, `true`, `yes` or `on` turns every feature on |
| `FLIGHTBAY_FEATURES` | A comma-separated list of feature names. `all` or `enterprise` turns them all on; a leading `-` turns one off. |

Set them on the process that opens the bundle (`flightbay ui` or `flightbay-desk open`). The helpers and `flightbay-desk mcp` ask that desk, so they follow its setting.

## Checking what is on

```bash
flightbay-desk features
```

```text
activity     off  Activity: who called which MCP tool, model or agent through agentgateway, refusals, token spend and tool changes
priorities   on   Best practices: fix-first ranking, points recovered per fix, severity colours
reach        off  Agent reach: every agent's blast radius, the network paths it has and what to lock down
traffic      off  Traffic map: ambient connections with mTLS, waypoints and policy verdicts, and why a connection was denied
```

Run it with the same environment as the desk.

## What you see without it

- The Scorecard shows plain severity tags and "Most serious problems" instead of "Fix these first", with no points. `fbscore --top` is refused.
- **Agent reach** is not in the sidebar or in ⌘K. Going to `#reach` directly shows a page saying it is an Enterprise feature and how to turn it on. `fbreach` and the MCP `agent_reach` tool return an error saying the same.
- **Activity** and **Traffic map** are hidden the same way; `fbactivity`, `fbtraffic`, `fbexplain` and their MCP tools say they need Enterprise.
- The Scorecard runs its 130 standard checks, without the five reach checks.

Pages, tools and helpers that show Enterprise features carry an **Enterprise** label.

## A product switch, not a security boundary

These variables decide what the desk shows. They do not protect anything: the desk runs on your machine, and anyone who can run it can set them. Anything that must be protected (a hosted service, uploads) enforces its own authentication and authorisation.
