---
title: The desk
weight: 30
description: Open a bundle as a local workspace with a web UI, a terminal, SQL and an MCP server, and start investigating while it loads.
---

# The desk

`flightbay-desk` turns a bundle into a workspace on your machine. You get a web UI, a terminal where `kubectl`, `k9s` and `stern` work against the snapshot, a set of helper commands, read-only SQL, and an MCP server for AI agents. Everything runs locally. Nothing is sent anywhere.

## Opening a bundle

```bash
flightbay ui flightbay-01234567-20260927T1012Z.tar.zst
```

`flightbay ui` finds `flightbay-desk` (next to `flightbay`, then on `PATH`) and runs `flightbay-desk open` with the same arguments, so every flag below works with either command.

| Command | Opens |
|---|---|
| `flightbay ui` | The newest `flightbay-*.tar.zst` or `.tar.zst.age` in the current directory |
| `flightbay ui ~/Downloads` | The newest bundle in that directory |
| `flightbay ui BUNDLE` | That bundle |
| `flightbay collect -o - \| flightbay-desk open -` | A capture streamed straight in |

The desk prints where everything is and opens your browser:

```text
Flightbay desk is ready while the bundle loads.
  UI:          http://127.0.0.1:8484/?token=4f1c...
  kubectl:     export KUBECONFIG=/Users/you/.flightbay/workspaces/ws-123/kubeconfig   (snapshot API on https://127.0.0.1:55949)
  Shell:       flightbay-desk shell   (kubectl, k9s, stern, fbq, fbsearch, fbsql, fbfind, fbpc, fbflows)
  Press Ctrl+C to close the workspace.
```

If the browser does not open, use the `UI:` address, token included. Press Ctrl+C to close. The workspace is deleted when you do, unless you asked to keep it.

## Options

| Flag | Default | Effect |
|---|---|---|
| `-i FILE`, `--identity FILE` | | age identity for an encrypted bundle. Repeat for several. |
| `--baseline BUNDLE` | | An earlier bundle to compare against, loaded in the background. See [Compare](/desk/compare/). |
| `--flows FILE[@CLUSTER]` | | Add flow logs, marked as added by the reviewer. See [Observed traffic](/collect/observed-traffic/). |
| `--keep` | off | Keep the workspace directory (DuckDB file, kubeconfig) after closing. |
| `--workdir DIR` | `~/.flightbay/workspaces/ws-*` | Where the workspace lives. A directory you name is not deleted on close. |
| `--listen ADDR` | `127.0.0.1:8484` | Address for the UI and API. Must be loopback unless `--allow-remote`. If 8484 is taken, a free port is used. |
| `--allow-remote` | off | Allow a non-loopback `--listen`, for containers. The token is still required. |
| `--no-browser` | off | Do not open a browser. |
| `--no-terminal` | off | Disable the in-browser terminal. |
| `--shell PATH` | `/bin/bash` | Shell for the terminal. |
| `--defer CATEGORIES` | | Load `objects`, `logs`, `events` or `diagnostics` later, from the UI. |
| `--throttle RATE` | | Limit import speed, e.g. `512KiB`, to rehearse a slow upload. |
| `--verify-key FILE` | `~/.flightbay/trusted-keys/*.pub` | Public keys to check the bundle's signature against. See [Signing](/collect/signing/). |
| `--review` | off | Review mode: opens on **Review and send** to choose what to export. `flightbay review` runs this. See [Reviewing before you send](/collect/review/). |
| `--sign KEY` | | Review mode: sign the exported bundle with this private key. |
| `--encrypt-to age1...` | | Review mode: encrypt the exported bundle. |
| `--reviewer NAME` | | Review mode: who reviewed it, recorded in the ledger. |
| `--review-out FILE` | `NAME-reviewed.tar.zst` | Review mode: where the export goes. |

Workspace directories left behind by a desk that was killed rather than closed are removed after seven days.

## It opens while it loads

The bundle is designed to be read as a stream. `bundle.json` comes first, logs early and bulky diagnostics last, so the UI opens as soon as the header has arrived and fills in as the rest loads.

While it loads:

- the header bar shows a progress bar and running counts of objects, log lines, events and bytes;
- searches and lists work on what has arrived and say "bundle still loading" when the answer could change;
- findings are marked **provisional**, and any rule that depends on something being absent waits until that data has fully loaded.

When the last file has arrived and every checksum matches, the bar says **Verified**. If the import stops (a truncated file, a bad checksum), the workspace stays open with what arrived, clearly marked as unverified.

`--defer logs` (or objects, events, diagnostics) skips a category on the first pass. The header bar then shows what was deferred and a **Load now** button that reads it from the bundle file. That does not work for a streamed bundle, which cannot be read twice.

## The workspace

Each open bundle gets its own directory:

| File | What it is |
|---|---|
| `workspace.duckdb` | Logs, objects, events and flows, in DuckDB. About 90 bytes per log line on disk: 10 million lines is 0.8 GB and loads in about 35 seconds. |
| `kubeconfig` | Points `kubectl` at the snapshot API, one context per cluster, named `<cluster>@snapshot`. |
| `bin/` | The `fb*` helpers. |
| `bashrc` | The terminal's rc file: `k` for `kubectl`, completion. |
| `desk.json` | URL, token, kubeconfig path and PID of this desk. |

`~/.flightbay/current.json` records the most recently opened desk, which is how `flightbay-desk shell`, the helpers and `flightbay-desk mcp` find it from any terminal. `FLIGHTBAY_HOME` moves `~/.flightbay` elsewhere.

## Finding your way round

The sidebar has every view:

| View | For |
|---|---|
| [Review and send](/collect/review/) | Review mode only: choose what to send to Solo and export a signed bundle |
| [Overview](/desk/overview/) | Where to start: counts, the top findings, clusters and provenance |
| [Search](/desk/search/) | Every object, by name, label or anything in spec and status |
| [Navigator](/desk/navigator/) | A keyboard-first, k9s-style browser |
| [Resources](/desk/resources/) | Browse objects by type, with YAML, relations, events and logs |
| [Log explorer](/desk/logs/) | Every log line, with a time axis |
| [Timeline](/desk/timeline/) | Events, exits, rollouts and error spikes in order |
| [Findings](/desk/findings/) | Rules that point at likely problems |
| [Scorecard](/desk/scorecard/) | Solo best practices, scored |
| [Agent reach](/desk/agent-reach/) | What each agent can reach (Enterprise) |
| [Compare](/desk/compare/) | What changed between captures |
| [Proxy configs](/desk/proxy-configs/) | Envoy, ztunnel, agentgateway and istiod as captured |
| [Coverage](/desk/coverage/) | What was allowed, collected and verified |

The terminal sits along the bottom of every page. See [Terminal](/desk/terminal/).

### Keys

| Key | Does |
|---|---|
| ⌘K or Ctrl+K | Jump to any object or view |
| Ctrl+Backtick | Open or close the terminal |
| `/` | Focus the filter or search box on the current page |
| Esc | Close a drawer or dialog |

⌘K searches object names, labels and content as you type, and lists matching views. Enter opens the object; the last row searches everything on the Search page.

### Cluster picker

A bundle can hold several clusters. The picker at the top right limits every view to one of them, or shows all.

### Light and dark

The button at the top right switches between light and dark. The choice is remembered in the browser. The first time, it follows your system setting.

## Also in this section

- [Terminal](/desk/terminal/): `kubectl`, `k9s`, `stern` and the `fb*` helpers
- [SQL](/desk/sql/): read-only DuckDB over the workspace
- [MCP server](/desk/mcp/): hand the workspace to an AI agent
- [flightbay-desk CLI](/reference/cli-flightbay-desk/): every command and flag
