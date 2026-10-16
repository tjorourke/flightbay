---
title: Flightbay
weight: 1
description: Take a redacted snapshot of a Kubernetes cluster and investigate it offline as if it were live.
---

# Flightbay

Flightbay takes a redacted snapshot of a Kubernetes cluster and lets you investigate it offline as if it were live. You can browse every object, search every log line, run `kubectl` and `k9s` against it, read the Envoy, ztunnel and agentgateway config dumps, and get findings that know about Istio ambient, agentgateway and kgateway.

![Overview of a snapshot](/images/overview.png)

It is built for support cases. The customer runs one command against their cluster and sends a file. The engineer opens that file on their laptop and has the cluster in front of them, without a VPN, a screen share or a round of "can you run this and paste the output".

## The two commands

| Command | Who runs it | What it does |
|---|---|---|
| `flightbay` | The customer, or whoever has access to the cluster | Reads the cluster through a collection policy the customer chooses, redacts in memory, and writes a verified `.tar.zst` bundle, optionally encrypted with age. A single static Go binary with no dependencies. |
| `flightbay-desk` | The engineer investigating | Opens a bundle as a local workspace: a web UI, a terminal where `kubectl`, `k9s` and `stern` work against the snapshot, helper commands, DuckDB SQL and an MCP server for AI agents. |

`flightbay ui` hands over to `flightbay-desk open`, so most people only ever type the first command:

```bash
flightbay collect --context my-cluster      # capture: writes flightbay-<case>-<time>.tar.zst
flightbay ui .                              # open the newest capture here in your browser
```

`flightbay ui` on its own opens a start page, from which you can live stream a cluster, open a bundle or scan a repo. See [Getting started](/getting-started/#the-start-page).

## How the data moves

1. **Choose.** The customer picks what may be read: a profile, flags, a config file or `collect --interactive`. `flightbay collect --dry-run` shows the resolved selection and the RBAC it needs, and reads nothing but API discovery.
2. **Collect.** `flightbay collect` lists objects, reads events and container logs, and (only when asked) reads proxy and control-plane admin endpoints through port-forward. Everything is redacted in memory before it is written. Secret values are never read into the bundle.
3. **Bundle.** The output is a plain tar stream compressed with zstd. `bundle.json` comes first and records the policy; `manifest.json` comes last with a SHA-256 for every file. With `--encrypt-to` the whole thing is wrapped in age encryption.
4. **Check.** `flightbay inspect BUNDLE --redactions` verifies every checksum and shows what is inside, before anything is sent.
5. **Send.** Uploading is a separate step that Flightbay does not do. The customer shares the file through their chosen channel.
6. **Investigate.** `flightbay ui BUNDLE` imports the bundle into a DuckDB workspace in a temporary directory and opens the UI while it is still loading. Nothing leaves the engineer's machine.

## What you can do with it

| Area | Page |
|---|---|
| Install, try the sample bundle, first capture | [Getting started](/getting-started/) |
| Choose what is collected | [Collecting a snapshot](/collect/), [Scope](/collect/scope/), [Logs](/collect/logs/) |
| What is never collected, and how redaction works | [Secrets and redaction](/collect/secrets-and-redaction/) |
| Proxy dumps, istiod debug output and metrics | [Proxy and control-plane diagnostics](/collect/diagnostics/) |
| Who called whom: access logs and flow logs | [Observed traffic](/collect/observed-traffic/) |
| Least-privilege RBAC for the collector | [RBAC](/collect/rbac/) |
| Decide what to send, and take out the rest | [Reviewing before you send](/collect/review/) |
| Sign bundles and approve collection policies | [Signing and approved policies](/collect/signing/) |
| Encryption, verification and the bundle file | [Sending a bundle](/collect/sending/) |
| Build a bundle from a Git repository and score it before it is deployed | [Scanning a repository](/scan/) |
| Fail a pull request on new failing checks, with SARIF | [Using it in CI](/scan/ci/) |
| What Git declares compared with what is running | [Repo compared with the cluster](/scan/drift/) |
| YAML for failing checks, mapped to the source files | [Fix packs](/scan/fix-packs/) |
| Opening a bundle and finding your way round | [The desk](/desk/) |
| Every object, by name, label or content | [Search](/desk/search/), [Navigator](/desk/navigator/), [Resources](/desk/resources/) |
| Every log line | [Log explorer](/desk/logs/) |
| What happened, in order | [Timeline](/desk/timeline/) |
| Rules that point at likely problems | [Findings](/desk/findings/) |
| Security and operational checks, scored | [Scorecard](/desk/scorecard/) and the [list of checks](/desk/scorecard-checks/) |
| What each agent can reach, use and call | [Traffic: Agents tab](/desk/agent-reach/) |
| Every connection and gateway call, and why one was denied | [Traffic](/desk/traffic/) |
| What changed between two captures | [Compare](/desk/compare/) |
| Envoy, ztunnel, agentgateway and istiod as captured | [Proxy configs](/desk/proxy-configs/) |
| What was allowed, collected and verified | [Coverage](/desk/coverage/) |
| `kubectl`, `k9s`, `stern` and the `fb*` helpers | [Terminal](/desk/terminal/) |
| DuckDB SQL over logs, objects, events and flows | [SQL](/desk/sql/) |
| Hand the workspace to an AI agent | [MCP server](/desk/mcp/) |
| Priorities, Agents and Traffic | [Flightbay Enterprise](/enterprise/) |
| What leaves the cluster and what never does | [Security](/security/) |
| Environment variables and the bundle layout | [Reference](/reference/) |
| When something does not work | [Troubleshooting](/troubleshooting/) |

## Who it is for

**Customers collecting.** You decide what Flightbay may read before anything is read. You can limit it to namespaces, exclude resource types, turn logs off, keep Secrets out entirely, and check the result with `flightbay inspect` before you send it. The [collection guide](/collect/) and [security](/security/) pages are written for you and your security team.

**Engineers investigating.** Open a cluster snapshot as a workspace: search, logs with a time axis, findings, a security scorecard, proxy config dumps, and a shell where the usual tools work. The [desk guide](/desk/) is written for you.

## Status

Flightbay is early and unreleased. Everything in these pages is built and tested. Where a feature is partial, the page says so.
