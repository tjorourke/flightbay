---
title: Scorecard
weight: 80
description: Score a snapshot against security and operational checks for agentic workloads, Istio, ingress and Kubernetes, with the evidence and fix for every problem.
---

# Scorecard

The Scorecard checks the snapshot against security and operational guidance and says what to change. Findings tell you what is broken; the Scorecard tells you what is risky or fragile even when everything works: MCP endpoints anyone can call, namespaces without STRICT mTLS, privileged pods, a single istiod.

Each failing or warning object row includes its **Cluster** and a **Review fix** button. Identical namespace/resource names in different clusters remain separate entries. Review fix opens the YAML, target and guidance for that entry.

Scoped recipes currently cover ambient namespace default-deny, a new namespace STRICT mTLS policy, namespace ingress NetworkPolicy default-deny and explicit caller restrictions for global Services. Caller restrictions require you to enter the intended service-account principals. Existing namespace PeerAuthentication or unsupported waypoint implementations need inspection rather than a competing policy. Other checks show their YAML guidance with an explanation of the missing resource-specific choices; they do not offer Apply for an unresolved template.

For a generated plan, choose a real kubeconfig context and **Validate (dry-run)**. **Apply…** then opens the same separate confirmation used by Istio Ambient, including the exact YAML, cluster and context. Cancelling sends no apply request. Applied changes have an undo action and are recorded with the ambient test plans. Default-deny and STRICT mTLS can reject traffic; the preview describes their scope before you confirm.

![Scorecard summary](/images/scorecard-summary.png)

The checks draw on public field guides, agentgateway, kagent, Istio and kgateway source, and Kubernetes hardening guidance. There are 129 of them, plus five more built on [Agent reach](/desk/agent-reach/) when that Enterprise feature is on. Every check is listed in [Scorecard checks](/desk/scorecard-checks/).

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

Each area also shows a score per category, worked out the same way from only the checks in that category. See [Categories](#categories).

| Score | Grade |
|---|---|
| 90 and above | A |
| 75 to 89 | B |
| 60 to 74 | C |
| 40 to 59 | D |
| below 40 | F |

## Categories

Every check belongs to one category. A category bar on an area card is that area's score for just those checks, so a low bar tells you which kind of problem is pulling the area down. Hover a bar to see what the category covers; click it to open the area filtered to `category:"<name>"`, with every status shown.

| Category | What it covers |
|---|---|
| Authentication | Callers have to prove who they are: JWT, API keys or mTLS required on MCP, A2A, LLM and ingress routes, with issuer and audience pinned and failures closed. |
| Authorization | What an authenticated caller may do: authorization policies, default deny, no allow-all or overly broad rules, per-tool MCP access, and waypoints wired so L7 policy is enforced. |
| Encryption | Traffic is encrypted and certificates are checked: workloads in the mesh with strict mTLS, TLS on listeners, TLS verified to upstreams, valid TLS secrets, credentials not stored in plain text. |
| Guardrails | Controls on what agents and models may do: prompt guards on requests, responses and streams, agents going through the LLM gateway, tool scope, and enforcement that fails closed. |
| Identity | Credentials and who workloads run as: no literal or passed-through tokens, secret references that resolve, dedicated service accounts, RBAC without wildcards, escalation or secret reads, and a shared mesh root of trust. |
| Isolation | What can reach what: egress control, network policies, Pod Security admission, host access, namespace-restricted routes, MCP servers not reachable directly, the cloud metadata endpoint blocked. |
| Resilience | Staying up under failure and load: replicas and HA, disruption budgets, probes, resource requests, spread, timeouts, retries, rate limits, and references and policies that resolve and attach. |
| Supply chain | Where the software comes from: images pinned to a digest or fixed tag, a trusted Istio distribution, agent artifacts pinned, a hardened registry, MCP selectors scoped, extensions kept in check. |
| Workload security | How containers run: not privileged, no privilege escalation, dropped capabilities, non-root, seccomp and a read-only root filesystem. |
| Platform | The cluster underneath: a supported Kubernetes version, healthy nodes, certificates not about to expire, no deprecated APIs, Helm releases in a good state. |
| Cost | Spend and wasted work: LLM spend limits, rate-limit keys that cannot be spoofed, retries only on idempotent calls, waypoints that are doing a job. |
| Observability | Seeing what happened: tracing, audit and access logs, mesh telemetry. |
| Multicluster | Multicluster mesh set-up: peer gateway configuration and no legacy remote secrets. |
| Network exposure | Mesh ports such as 15008 and 15012 not exposed outside the cluster. |
| Health | The control plane reporting clean metrics, with no config rejections or push errors. |
| Config hygiene | Configuration that points at things that exist, such as `istio.io/rev` labels resolving to a running revision. |
| Routing | Routing rules that behave in ambient mode, such as VirtualService source matches. |
| Security | General HTTP security settings on ingress, such as a safe CORS policy. |

Every check, with its category, is listed on [Scorecard checks](/desk/scorecard-checks/).

## Not applicable

A check is **not applicable** (N/A) when:

- the data it needs was not captured, for example RBAC checks without `--cluster-scoped`, or a type the customer excluded;
- the customer removed the type, or some of its objects, in review before sending ("networkpolicies.networking.k8s.io was removed by the customer before sending"); or
- there is nothing for it to apply to, for example waypoint checks in a cluster with no waypoints.

Not applicable checks carry their reason ("clusterrolebindings.rbac.authorization.k8s.io was not captured (excluded)") and do not count towards the score. A check never fails on data the customer chose not to collect. A CRD that is not installed at all counts as "there are none", which is a real answer, not missing data.

## Using the page

The top of the page has a card per scored area: the score, grade, counts of failing, warning, passing and N/A checks, and a bar per category. Click a card to open that area, or a category bar to list the checks in it. Below the cards, a line says how many checks ran across how many objects, and which areas were not scored and why.

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
- **Product docs**: upstream product documentation for the feature the check is about. A few checks on plain Kubernetes link to kubernetes.io instead (**Kubernetes docs**).

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
