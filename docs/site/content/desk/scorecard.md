---
title: Scorecard
weight: 80
description: Score a snapshot against Solo's best practices for agentic workloads, Istio, ingress and Kubernetes, with the evidence and the fix for every problem.
---

# Scorecard

The Scorecard checks the snapshot against Solo's best practices and says what to change. Findings tell you what is broken; the Scorecard tells you what is risky or fragile even when everything works: MCP endpoints anyone can call, namespaces without STRICT mTLS, privileged pods, a single istiod.

![Scorecard summary](/images/scorecard-summary.png)

The checks are drawn from the Solo field guides, the agentgateway, kagent, Istio and kgateway source, and Kubernetes hardening guidance. There are 130 of them, plus five more built on [Agent reach](/desk/agent-reach/) when that Enterprise feature is on. Every check is listed in [Scorecard checks](/desk/scorecard-checks/).

## The four areas

| Area | Checks | Scored when the capture has |
|---|---|---|
| Agentic (agentgateway, kagent, MCP) | 53, plus 5 with Agent reach | agentgateway policies or backends, Enterprise agentgateway resources, kagent Agents, or an agentgateway Gateway |
| Istio (ambient and control plane) | 37 | ztunnel pods, or a namespace labelled for ambient |
| Ingress (Gateway API, kgateway) | 11 | Gateway API ingress or kgateway objects |
| Kubernetes (workload and platform hygiene) | 29 | Pods with full object detail |

An area the capture has nothing for is not scored, and the page says why ("no Istio ambient in this capture").

### What each area looks at

**Agentic.** MCP and A2A endpoints without authentication, API-key and basic auth in optional modes, the caller's token passed through to MCP servers, missing tool allowlists, code mode without limits, guardrails and external auth that fail open, LLM spend without a budget, credentials held inline or pointing at Secrets that do not exist, policies that were not accepted, and agents that call models and tools without going through the gateway. For example:

- `agentic.mcp-auth-required`: MCP endpoints require authentication (critical)
- `agentic.no-token-passthrough`: the caller's token is not passed through to upstreams (critical)
- `agentic.mcp-tool-authorization`: MCP backends have a tool allowlist (high)
- `agentic.llm-spend-controlled`: LLM spend is capped by a budget or a token rate limit (medium)

**Istio.** Namespace enrolment, pods ztunnel is not capturing, STRICT mTLS, default-deny, principals in the wrong trust domain, L7 policy that nothing enforces, ingress that skips the waypoint, istiod HA, a plugged-in CA, multicluster peering and remote secrets, and exposed control-plane ports. For example:

- `ambient.workloads-captured`: pods in ambient namespaces are captured by ztunnel (critical)
- `ambient.mtls-strict`: mTLS is STRICT (high)
- `ambient.l7-needs-waypoint`: L7 policy is attached to a waypoint (high)
- `mesh.istiod-ha`: istiod runs with at least two replicas (high)

**Ingress.** Listeners without TLS, certificate Secrets that are missing or not granted, cross-namespace references without a ReferenceGrant, shared gateways open to every namespace, client IP policy that sees the wrong address, and kgateway auth that fails open. For example:

- `gateway.tls-secret-valid`: listener certificates point at usable Secrets (critical)
- `gateway.referencegrants`: cross-namespace references have a ReferenceGrant (high)

**Kubernetes.** Privileged pods and host access, capabilities (knowing what ztunnel and istio-cni legitimately need), RBAC wildcards and cluster-admin bindings, Pod Security Admission, NetworkPolicy, exposed Services, requests, probes, replicas and PodDisruptionBudgets, unpinned images, supported versions, failed Helm releases and expiring certificates. For example:

- `k8s.no-privileged`: no privileged containers (critical)
- `k8s.cluster-admin-bindings`: cluster-admin is not granted to workloads or groups (critical)
- `k8s.image-pinned`: images are pinned by digest (high)

## How the score works

Each check looks at a set of objects and gives each one a status: **pass**, **warn** or **fail**. A check's score is the share of credit it earns: a pass is worth 1, a warning 0.5 and a failure 0. The check's own status is its worst object's.

An area's score is the average of its checks' scores, weighted by severity:

| Severity | Weight |
|---|---|
| critical | 10 |
| high | 6 |
| medium | 3 |
| low | 1 |

Each area also shows a score per category (Authentication, Isolation, Resilience and so on), worked out the same way.

| Score | Grade |
|---|---|
| 90 and above | A |
| 75 to 89 | B |
| 60 to 74 | C |
| 40 to 59 | D |
| below 40 | F |

## Not applicable

A check is **not applicable** (N/A) when:

- the data it needs was not captured, for example RBAC checks without `--cluster-scoped`, or a type the customer excluded; or
- there is nothing for it to apply to, for example waypoint checks in a cluster with no waypoints.

Not applicable checks carry their reason ("clusterrolebindings.rbac.authorization.k8s.io was not captured (excluded)") and do not count towards the score. A check never fails on data the customer chose not to collect. A CRD that is not installed at all counts as "there are none", which is a real answer, not missing data.

## Using the page

The top of the page has a card per scored area: the score, grade, counts of failing, warning, passing and N/A checks, and a bar per category. Click a card to open that area. Below the cards, a line says how many checks ran across how many objects, and which areas were not scored and why.

### Tabs

| Tab | Shows |
|---|---|
| Summary | The most serious problems: critical and high checks that fail or warn, up to ten. With Flightbay Enterprise, the ranked **Fix these first** list instead. |
| One per scored area | That area's checks, with a count of those that need attention |
| All checks | Every check, grouped by area |

### Filtering

Status chips: **Needs attention** (the default: failing and warning), **Failing**, **Warnings**, **Passing**, **Not applicable** and **All**. The filter box takes free text and these keys, with typeahead:

| Key | Matches | Example |
|---|---|---|
| `status:` | `fail`, `warn`, `pass` or `na` (overrides the chips) | `status:fail` |
| `severity:` (also `sev:`) | `critical`, `high`, `medium` or `low` | `severity:critical` |
| `area:` | `agentic`, `ambient`, `gateway` or `kubernetes` | `area:agentic` |
| `category:` | A category of checks | `category:identity` |
| `kind:` | The kind of an object a check looked at | `kind:AgentgatewayPolicy` |
| `ns:` (also `namespace:`) | The namespace of an object a check looked at | `ns:kagent` |
| `id:` | Check ID | `id:agentic.mcp` |

Values match as a case-insensitive prefix, and a leading `-` excludes.

### A check

Click a check to open it. You get:

- why it matters, in a sentence or two;
- every object it looked at, with its status and what was found (click an object to open it);
- **How to fix**: the YAML to apply, with **Copy YAML**;
- **Read the guide**: the matching lab or article on mastertheagent.com or masterthemesh.com;
- **Solo docs**: the page of the official Solo product documentation on docs.solo.io for the feature the check is about. A few checks on plain Kubernetes link to kubernetes.io instead (**Kubernetes docs**).

## From the terminal

```bash
fbscore                    # failing and warning checks, with the YAML to fix them
fbscore --area agentic     # one area: agentic, ambient, gateway or kubernetes
fbscore --all              # every check, including passing and not applicable
fbscore --top 10           # the ten fixes that give back the most score (Enterprise)
```

`fbscore` prints each area's score, grade and category scores first. `fbscore in terminal` on the page runs it for you.

The MCP `best_practices` tool returns the same report as JSON, filtered by `area` and `status` if you ask.

## The Enterprise priority view

With the `priorities` feature of [Flightbay Enterprise](/enterprise/), the Scorecard adds:

- **Severity tags coloured by weight**: critical solid red, high red outline, medium amber, low grey.
- **Fix these first** on the Summary tab: every failing and warning check, ranked. Each row shows the rank, the points it gives back, its status and severity, how many objects fail of how many, and the first few objects to change. Click a row to open that check.
- **+N pts** on each failing or warning check: the area score it would give back if every object passed.

The ranking is by **impact**: severity weight times the share of credit missing. Ties go to the higher severity, then more points, then failing before warning. Points are worked out as 100 × weight × (1 − score) ÷ the area's total weight.

Ranking by points alone would be misleading. An area's points depend on how many checks it has, so a critical check in the large agentic area gives back fewer points than a high one in a small area. Impact does not depend on the area, which keeps an open MCP endpoint near the top where it belongs.

The same ranking is available as:

```bash
fbscore --top 10
```

and as `priorities` in the MCP `best_practices` response. Without the feature, `fbscore --top` is refused and the UI shows none of it.

With the `reach` feature, the Summary tab also links to [Agent reach](/desk/agent-reach/), and the agentic area gains five checks built on it:

| Check | Severity |
|---|---|
| `agentic.agent-internet-reach`: agents cannot reach the internet directly | critical |
| `agentic.agent-transitive-fetcher`: agents cannot borrow internet access through a registry or proxy | critical |
| `agentic.agent-write-sensitive`: agents cannot write to data stores, registries or credential services they only need to read | high |
| `agentic.agent-no-shared-channel`: agents do not share an identity, a volume or a writable store | high |
| `agentic.agent-least-reach`: agents reach only what they use | medium |
