---
title: Agents tab
weight: 96
description: Inspect an agent's network access and observed calls, and generate proposed restrictions.
---

# Agents tab

The **Agents** tab of [Traffic](/desk/traffic/) shows each agent's network access, observed calls and proposed policy changes. Agent reach evaluates captured policies and follows reachable destinations for up to three hops. Its results depend on the data and policy types covered by the capture; see [Limits](#limits).

Open `#traffic?tab=agents&agent=ns/name` for a specific agent. Older `#reach` and `#reach?agent=ns/name` links also open this tab. The Traffic Findings panel groups findings across agents.

The Agents tab is part of [Flightbay Enterprise](/enterprise/). Turn it on with `FLIGHTBAY_ENTERPRISE=1 flightbay ui` or `FLIGHTBAY_FEATURES=reach`; with only `reach` on, the Traffic page shows just this tab. With the Traffic feature on as well (`FLIGHTBAY_ENTERPRISE=1` turns on both), it also shows what each agent used and called.

![Every agent, worst first](/images/reach-list.png)

## Indirect internet access

In August 2026 OpenAI described an incident in which evaluation agents, running in containers inside its own infrastructure, ended up breaching Hugging Face. The agents never broke out of their containers. They were allowed to call an internal package registry, and that registry had its own route to the internet. It accepted writes (PUT, then MKCOL over WebDAV, which the agents used to build a message board between themselves) and it fetched URLs on request. Once an agent could make the registry fetch for it, the registry's reach became the agent's. After a full clean-up, the agents rebuilt their channel four days later.

An agent's egress policy can allow a service that fetches external URLs on its behalf. Agent reach checks for these indirect paths and generates proposed restrictions from the snapshot.

The field guide describes the incident and relevant controls: [The OpenAI and Hugging Face incident: zero trust for agents on Kubernetes](https://mastertheagent.com/solo/zero-trust-agents-kubernetes/).

## What counts as an agent

| Type | Found from |
|---|---|
| kagent agent | kagent `Agent` resources, and the workloads they own |
| MCP server | kmcp `MCPServer` resources, targets of agentgateway MCP backends, `RemoteMCPServer` URLs that point into the cluster, Services with an `appProtocol` containing `/mcp` or `/a2a`, and workloads whose name or image looks like an MCP server |
| agentgateway | The data plane pods of an agentgateway Gateway |
| possible agent | A workload whose name or image looks like an agent (agent, langgraph, langchain, crewai, autogen, openhands, codex, claude-code and similar) |

Infrastructure is left out even when its name contains "agent": agentgateway controllers, monitoring agents (Datadog, OpenTelemetry, Fluent and so on), controllers, operators, registries, databases, UIs, gateways, and anything in `kube-*` or `istio-system`.

"Possible agent" is a guess from names, and the page says so ("name or image looks like an agent; confirm"). There is no way yet to confirm or reject one from the customer side.

## The agent list

The list shows every agent, worst first:

| Column | Means |
|---|---|
| Agent | Namespace and name, with its type |
| Radius | `open`, `wide`, `narrow` or `contained` (below) |
| Internet | Whether it can reach the internet directly: `allowed`, `denied` or `unknown` |
| Direct | Services it can reach in one hop, of all the Services in the capture |
| Total | Services reachable within the three-hop analysis limit |
| Write | Direct destinations it can change data on |
| Findings | Critical and high findings |
| Summary | One sentence, such as "demo-agent reaches the internet in 2 hops through artifactory, which fetches on request" |

Chips filter by type. The filter box takes free text and these keys, with typeahead:

| Key | Matches | Example |
|---|---|---|
| `type:` | `kagent-agent`, `mcp-server`, `agentgateway` or `possible-agent` | `type:mcp-server` |
| `ns:` | The agent's namespace | `ns:kagent` |
| `radius:` | `open`, `wide`, `narrow` or `contained` | `radius:open` |
| `internet:` | `allowed`, `denied` or `unknown` | `internet:allowed` |

Notes above the list say what could not be evaluated, for example "no NetworkPolicy or AuthorizationPolicy exists in this capture: the network is flat", or "CiliumNetworkPolicy objects exist but are not evaluated yet".

## Radius

| Radius | When |
|---|---|
| open | It can reach the internet directly, or it has a critical finding |
| wide | It can reach half the Services in the cluster or more, in any number of hops |
| narrow | It can reach more than three Services |
| contained | Three Services or fewer |

## Direct and total reach

**Direct** reach counts Services allowed by the evaluated NetworkPolicy, ambient mTLS and AuthorizationPolicy rules. Kubernetes API access is listed separately under Identity using the agent's token and RBAC. Waypoint Services are excluded from the count; their forwarding paths are evaluated against the bound Services.

**Total** reach follows the chain: what each destination's pods can reach, and what those can reach, up to three hops. A destination that can reach the internet makes the path out a finding, even if the agent itself cannot.

**Internet** is evaluated against a public address, and **cloud metadata** against `169.254.169.254`. Only Kubernetes NetworkPolicy can stop either: ambient does not police traffic to destinations outside the mesh unless it goes through an egress gateway. On kind clusters there is no metadata endpoint, so it is reported as "would reach" at a lower severity.

## Services that fetch URLs

Some destinations fetch from elsewhere on request: package registries and proxies, HTTP forward proxies, scrapers and headless browsers, URL fetch tools, LLM proxies. If an agent can call one, and that service can reach the internet, the agent can reach the internet through it, whatever its own egress says. This is the Hugging Face path, and it is reported as a critical finding: "Inherits internet access through ...".

Fetchers are recognised by name and image (Artifactory, JFrog, Nexus, Harbor, Verdaccio, devpi, pypiserver, Athens, Squid, tinyproxy, forward and egress proxies, Playwright, Puppeteer, Selenium, Browserless, Firecrawl, SearXNG, LiteLLM, `mcp-fetch` and others), by being configured as an agent's HTTP proxy, and by an `ExternalName` Service that points at a registry.

## Destinations outside the cluster

The incident's registry was not in the cluster. So external hosts are destinations too: every host an agent is configured for, was seen calling, or can reach through a gateway.

They are found in:

- the agent's environment (URLs and hosts in env vars);
- package manager configuration in ConfigMaps (pip, npm and similar);
- kagent `ModelConfig` and `RemoteMCPServer`;
- agentgateway backends the agent can route to;
- Istio `ServiceEntry` and `ExternalName` Services;
- observed flows and DNS lookups;
- image references (image pulls, shown dimmed, because the node pulls images, not the agent).

Each host is classified:

| Class | Examples |
|---|---|
| registry | JFrog Artifactory, Sonatype Nexus, PyPI, npm, Go module proxy, Maven, Hugging Face Hub, Docker Hub, GHCR, Quay, ECR, Artifact Registry |
| proxy | Hosts named like HTTP proxies |
| llm | OpenAI, Anthropic, Azure OpenAI, Amazon Bedrock, Gemini, Vertex AI, Mistral, Cohere, Groq, OpenRouter |
| git | GitHub (including Copilot's remote MCP), GitLab, Bitbucket |
| storage | S3, Google Cloud Storage, Azure Blob, R2 and other object stores, managed databases |
| exfil | Paste sites, request capture services, tunnels, file drops, chat webhooks |
| metadata | Cloud instance metadata endpoints |
| unknown | Anything else |

And for each host, how it is reached:

| Reached | Means |
|---|---|
| direct egress | The agent calls it itself |
| through agentgateway | A gateway route leads there, with whether the route needs authentication and whether it only admits reads, and whether direct egress is also open (so the gateway can be bypassed) |
| egress waypoint | An Istio `ServiceEntry` bound to an egress waypoint |
| HTTP proxy | Through a proxy the agent is configured to use |
| blocked | Egress to it is denied |
| image pulls (node) | Only referenced as an image |

Credential names the agent holds for the host (a Secret name, an env var name) are listed, and so are credentials the gateway adds on the way. Values are never collected, so only names appear.

## The agent page

Click an agent to open its page. The header has its summary, why it is listed, which flow sources cover it, and tiles:

| Tile | Shows |
|---|---|
| Radius | Its radius and what that means |
| Internet | Direct internet egress, and cloud metadata |
| Direct | Services reached in one hop, of the total |
| Total | Services reachable within the three-hop analysis limit |
| Write | Destinations it can change data on |
| Used | Destinations it actually called, and which logs saw it. A dash when no flow source covers the agent. |
| Not observed | Allowed, not configured, and not seen in the captured flow data. Requires flow coverage. |
| External | Hosts outside the cluster, and how many go through the gateway |

Buttons open the agent's object, its logs, or `fbreach` for it in the terminal.

### Tabs

| Tab | Shows |
|---|---|
| Blast radius | The graph, and the paths that matter |
| Findings | What to lock down, each with what it affects and the fix |
| Paths | Chains from the agent to the internet, cloud metadata, fetchers and stores |
| Destinations | Every Service it can reach in one hop |
| External | Every host outside the cluster |
| Identity | Service account, credentials, RBAC and host access |

### The graph

![Blast radius graph](/images/reach-graph.png)

The graph reads left to right: the agent, what it reaches directly, what those reach, then the way out (internet and cloud metadata), and hosts outside the cluster in the last column. When the first column is long it wraps into a grid inside a frame, with one line from the agent into the frame. Lines between neighbouring columns run along a short trunk per source so a wide fan-out stays readable.

| Colour | Means |
|---|---|
| red | Open: write access, the internet, or no request-level rule on something sensitive |
| amber | Allowed with no request-level rule, or could not be evaluated |
| green | Read-only or restricted |
| grey | Cluster DNS, blocked or not applicable |
| dashed line | Denied, or an unknown verdict |

Views:

| View | Draws |
|---|---|
| Risky | Red destinations and anything leading out of the cluster; the rest folded into one box per namespace. The default when there are more than 14 direct destinations. |
| By namespace | One box per namespace; click one to open it up |
| Everything | Every destination as its own box |

**Fold namespaces** closes any you opened.

| To | Do |
|---|---|
| Move | Drag |
| Zoom | Pinch, Ctrl or ⌘ and scroll, or the − and + buttons |
| Fit to the panel | **Fit**, or `0` |
| Full screen | **Full screen**, or `f`; Esc to leave. In full screen, scrolling pans. |
| Zoom with keys | `+` and `-` when the graph has focus |
| Open a destination | Click its box |

Hover a line for the verdict, the policies that decided it, the methods allowed and how many calls were seen.

### Destinations

Services the agent can reach in one hop, sorted by risk. Chips: **All**, **Risky**, **Used**, **Not observed**, and a filter box.

| Column | Shows |
|---|---|
| Risk | red, amber, green or grey |
| Destination | The Service, and "not observed" where that applies |
| Verdict | allowed, denied or unknown, and L4 or L7 |
| Methods | "any method", or the methods an L7 rule allows, and whether it can write |
| Called | Requests seen, methods, denials, MCP tools, and which sources saw them |
| Configured | Whether the agent is configured to call it (env URL, ModelConfig, tool server) |
| Why | The reason for the risk, and the policies that decided the verdict |

If the policies deny a destination but the capture shows requests getting through, the row is kept and marked amber: the policy changed while the capture ran, or something is in play that is not evaluated.

### External

![External destinations](/images/reach-external.png)

Every host outside the cluster. Chips: **All**, **Registries**, **LLM**, **Direct**, **Through gateway**, **Observed**. Columns: host, what it is, how it is reached, credentials, what was observed, where it is configured, and risk.

### Findings and fixes

Findings are one line each; open one for the detail, a table of what it affects (with what each target is, the methods allowed, and which other agents can reach it too), advice that is not YAML, the fix YAML with **Copy YAML**, and a link to the guide.

| Finding | Severity |
|---|---|
| Reaches the internet directly | critical |
| Inherits internet access through a fetcher | critical |
| Can reach an external registry that fetches from the internet | high, critical if it holds a credential or writes were seen |
| Can reach paste, webhook or tunnel services | critical |
| Runs with host access | critical |
| Can write to a sensitive service (data store, credential service, fetcher) | high |
| Shares writable stores with other agents | high |
| Mounts a volume other agents also mount | high |
| Shares its identity with other agents | high |
| Can reach N of M Services | high |
| Can reach the cloud metadata endpoint | high |
| Not in the mesh | high |
| Service account can escalate (for example create pods, read Secrets, bind roles) | high |
| Calls external services directly, not through the gateway | high |
| Holds a credential for a host that probably allows writes | high |
| Can use the gateway's credential for a host without authenticating | high |
| Egress is not limited to the gateway's backends (agent gateways) | high |
| N allowed destinations were not observed | medium |
| Reaches external hosts it is not configured for | medium |
| Would reach a cloud metadata endpoint (kind) | medium |
| Internet access could not be determined | medium |
| Mounts an API token it has no use for | medium |
| Holds a cloud identity | medium |
| Can read Secrets | low |
| Shares the node kernel (no sandbox runtime) | low |

The fixes are generated with the agent's real names filled in:

- an egress NetworkPolicy for the agent that allows DNS, HBONE to its waypoints and the Services it needs;
- a read-only AuthorizationPolicy at a destination's waypoint (GET and HEAD for the agent's principal), with the steps to add a waypoint if there is none;
- a callers-only policy for a data store;
- a block on the cloud metadata endpoint;
- a dedicated service account;
- `automountServiceAccountToken: false`;
- a sandbox runtime class.

### Identity

| Field | Shows |
|---|---|
| Service account | And its mesh principal |
| In mesh | Whether ztunnel or a sidecar captures it |
| Token mounted | Whether an API token is mounted |
| RBAC rules | What the service account is granted, and whether RBAC was fully captured |
| Dangerous rules | Escalation paths among them |
| Cloud role | IRSA, Workload Identity and similar annotations |
| Secrets | Secrets it mounts or references |
| Host access | Host network, PID, IPC, paths, privileged |
| Sandbox | Its runtime class, when it is a sandbox (gVisor, Kata, Firecracker and similar) |
| Shared volumes | PersistentVolumeClaims other agents also mount |

## Observed traffic

Reach is what the policies allow. Observed traffic is what the capture shows actually happened. Flightbay uses every source it has:

- ztunnel access logs, for agents in the ambient mesh;
- agentgateway request logs (including the MCP method and tool);
- Envoy access logs from waypoints, sidecars and gateways;
- CoreDNS query logs (the `log` plugin is off by default);
- Hubble, Calico, AWS, GCP and Azure flow logs, and generic CSV or JSON, attached at capture or added when the bundle is opened.

"Used" and "Not observed" apply only when a flow source covers the agent. The page lists those sources. Absence from the captured window does not establish that a destination is never used. See [Observed traffic](/collect/observed-traffic/) for supported sources.

## Allowed and used

Agent reach shows what policy allows. When the capture has traffic for the agent (ztunnel access logs, agentgateway request logs or flow logs), it also shows what the agent used, so you can see the gap.

The graph has three layers:

| Layer | Draws |
|---|---|
| Allowed | Everything policy allows, as described above |
| Used | Only what the agent was seen using, and the hosts outside the cluster it called |
| Both | Allowed destinations, with observed calls drawn solid and unobserved paths faded and dashed. Red lines show observed traffic denied by the captured policy. |

Nothing is faded when no flow source covers the agent. Without flow logs there is no way to tell used from unused, so every box keeps its full colour and the header's **Used** tile reads "no flow logs cover this agent".

![Both layers: the used namespaces stand out, the rest is faded](/images/reach-layers.png)

![Observed connections from tom-demo to two Services](/images/reach-used.png)

Hover a box to trace its lines, or click it to keep them lit (click another box or close its panel to move on). Hover a line for what went over it: connections and whether they were mutual TLS, calls through agentgateway, the tools called and the tokens used. Click a box for the same under the graph, with a **Why?** button that opens the [Traffic](/desk/traffic/) explainer for the agent and that Service. The **Traffic** button in the agent's header opens the Traffic page following the agent.

### What it does

The **What it does** tab has the agent's activity over the log window, and every destination with its state.

![Activity over time, and the first time each destination and tool was seen](/images/reach-timeline.png)

**Over time** draws one lane each for ztunnel connections, calls through agentgateway, tool and LLM calls, refusals and failures, with one bar per slice of the window. A marker on top shows the first time a destination, tool, model or refusal was seen, and the table under it lists them.

**Allowed and used** lists every destination:

| State | Meaning |
|---|---|
| used | It connected or called, and some of it got through |
| allowed, not used | Policy allows it and the logs show nothing |
| tried, refused | Every attempt was refused |
| used, policy denies | It got through although the captured policy denies it: the policy may be newer than the traffic |
| used, not in reach | Something outside what Agent reach knows, such as an address outside the capture |

"Allowed, not used" counts every allowed destination. The **Not observed** tile at the top leaves out the ones the agent is configured to call.

### Tools and models

With the Traffic feature on, the agent list gains **Used** (destinations it was seen using, of those it may reach directly), **Gateway calls** and, when any were logged, **Tokens**. The agent's **Tools and models** tab lists every call it made through agentgateway: the tools with their server, calls, refusals, failures, gateway and last time, and whether the tool is in its server's list and in the Agent's `toolNames`; the models with tokens and cost; the agents it called over A2A; its refused calls with the reason; and MCP sessions and other requests.

![One agent's tools and MCP sessions](/images/agents-tools.png)

**Tools … may call** comes first: per MCP server, every tool the agent has access to (from the agentgateway allowlist for its service account, or every tool the server offers when there is no allowlist), whether it called each one, and whether its own configuration lists it. Tools marked **allowed, not called** are candidates to take away; **called, not allowed** means the captured allowlist would refuse a call the logs show, so the policy is probably newer than the calls. See [What a caller may call](/desk/traffic/#what-a-caller-may-call).

The **Findings** tab adds **From the gateways**: the gateway findings that name this agent, with only its rows.

### Lock down to what it uses

The **Lock down** tab proposes the least privilege the capture supports:

- an egress NetworkPolicy for the agent that allows cluster DNS and only the Services it used, with HBONE to their waypoints in ambient; and
- for each MCP server it called tools on through agentgateway, a `backend.mcp.authorization` rule that allows every caller seen on that route only the tools it called, by mesh identity (`source.identity.namespace` and `serviceAccount`) or JWT subject. Other callers are kept, so the rule does not cut them off. Calls with no identity get no rule, and the comment says so.

Flightbay then runs Agent reach again with the proposal in place of the agent's egress policies, and shows radius, internet, direct and total reach, write access and critical and high findings before and after. It checks that every Service the agent used is still reachable, and lists anything that would be cut. Hosts outside the cluster it called directly are listed as breaking: the policy has no rule for them, and the page says to route them through agentgateway.

![Before and after, what it keeps and removes, and the YAML](/images/reach-lockdown.png)

Nothing is applied; copy or download the YAML. The proposal is only as complete as the log window: something the agent does rarely may not be in it.

## How verdicts are worked out

Everything is computed from captured objects: Services and their pods, NetworkPolicies, Istio AuthorizationPolicies, PeerAuthentications and waypoints, agentgateway routes and policies, ServiceAccounts, RBAC, Secrets and PVCs. Where a type was not captured, the verdict is **unknown**, never assumed to be allowed or denied. A Service with no pods of its own whose waypoint answers for it, such as an agentgateway waypoint in front of an MCP server outside the cluster, is evaluated to the waypoint. The [Traffic](/desk/traffic/)'s **Why?** explainer shows every check behind any one verdict.

For each Service, for each pod behind it:

1. **NetworkPolicy, both sides.** The agent's egress policies and the destination's ingress policies, with pod and namespace selectors, `ipBlock`, ports, `endPort` and named ports, and the default `policyTypes`.
2. **Ambient HBONE.** When both ends are in the ambient mesh, the traffic NetworkPolicy sees is HBONE on port 15008, not the Service port. When the Service uses a waypoint, the path is agent to waypoint, then waypoint to destination, and all four legs (agent egress, waypoint ingress, waypoint egress, destination ingress) are checked on 15008.
3. **STRICT mTLS.** If the destination's PeerAuthentication (workload, then namespace, then mesh-wide) is STRICT and the agent is not in the mesh, it is denied.
4. **AuthorizationPolicy at ztunnel.** Policies with a selector, namespace-wide, or mesh-wide in the root namespace. DENY first, then ALLOW. Principals (ignoring the trust domain), namespaces and ports are evaluated. At ztunnel an ALLOW rule with HTTP attributes matches nothing, and that is noted; a DENY rule with them denies at L4.
5. **AuthorizationPolicy at the waypoint.** Policies targeting the Service, the waypoint Gateway, or the waypoint GatewayClass from the root namespace. Method and path rules make the edge L7, with the allowed methods listed. Waypoint policies for a Service that has no waypoint are noted as not applying.

Conditions that cannot be judged from a snapshot are listed on the edge rather than guessed: JWT requirements (`requestPrincipals`), `ipBlocks`, and CUSTOM (external) authorisation.

An edge can **write** when it is allowed with any method, or with a method such as PUT, POST, PATCH, DELETE or MKCOL.

## Limits

- **CNI-specific policies are not evaluated yet.** `CiliumNetworkPolicy`, `CiliumClusterwideNetworkPolicy`, Calico `NetworkPolicy` and `GlobalNetworkPolicy`, `AdminNetworkPolicy` and `BaselineAdminNetworkPolicy` are not read. When they exist the page says so, and reach may look wider than it is.
- **Classification is by name.** Fetchers, stores, credential services, LLMs and possible agents are recognised from names and images. An unusual name can be missed, or a harmless one caught.
- **Possible agents cannot be confirmed yet.** There is no customer-supplied agent selector.
- **The trust domain is ignored** when matching principals, because the capture does not always say what it is.
- **It is a snapshot.** Policies that changed while the capture ran can disagree with the traffic seen; those edges are flagged.

## From the terminal and agents

```bash
fbreach                          # every agent, MCP server and agent gateway
fbreach kagent/demo-agent        # its paths, identity, findings and destinations
fbreach kagent/demo-agent --fix  # and the YAML that locks each finding down
```

```text
AGENT                           TYPE            RADIUS  INTERNET  DIRECT  TOTAL  WRITE  EXTERNAL  CRIT  HIGH
agentdesktop/agentdesktop       possible-agent  open    allowed   57/64   58     56     3         1     6
kagent/demo-agent               kagent-agent    open    allowed   57/64   58     56     3         1     5
```

The MCP `agent_reach` tool returns the list, or one agent in full when you pass `agent: namespace/name`. The [Scorecard](/desk/scorecard/) gains five agentic checks built on the same analysis.

## End-to-end test

`test/e2e/reach.sh` (or `make e2e-reach`) recreates the incident on a kind cluster running Istio ambient: two agents sharing a PVC, a registry that accepts PUT and MKCOL and can reach the internet, and a database.

1. **Before**, it proves with real requests that the agent can PUT and MKCOL to the registry and reach the internet, captures the cluster, and checks that Flightbay agrees: internet allowed, radius open, the registry edge allowed with write access, and the shared PVC reported.
2. It applies the fix YAML Flightbay generated, exactly as generated, including the waypoint steps from the fix comments.
3. **After**, it proves the traffic is blocked: PUT and MKCOL now 403, GET still 200, the second agent refused, the internet unreachable. It captures again and checks Flightbay agrees: internet denied, no critical findings, the registry edge GET and HEAD only and green, no path to the internet, and observed traffic on the registry edge from the access logs.

```bash
test/e2e/reach.sh            # create a kind cluster, run, delete it
KEEP=1 test/e2e/reach.sh     # keep the cluster for another run
```

It needs `go`, `kind`, `kubectl`, `docker`, `istioctl`, `python3` and `curl`. The test uses its own kubeconfig.
