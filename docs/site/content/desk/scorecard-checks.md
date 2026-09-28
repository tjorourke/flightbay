---
title: Scorecard checks
weight: 85
description: Every Scorecard check, with its ID, area, category, severity and title.
---

# Scorecard checks

This is every check the [Scorecard](/desk/scorecard/) runs, grouped by area. It was generated from the check definitions in the source.

There are 130 checks in every build. The five marked "(Enterprise, Agent reach)" only run when the `reach` feature of [Flightbay Enterprise](/enterprise/) is on, which makes 135.

| Area | Checks |
|---|---|
| Agentic | 58 (53, plus 5 with Agent reach) |
| Istio | 37 |
| Ingress | 11 |
| Kubernetes | 29 |

In the desk, filter the Scorecard with `id:` to find one check, for example `id:agentic.mcp-auth-required`. Each check in the desk has the explanation, the objects it looked at, the fix YAML and a link to the matching guide.

Severity weights: critical 10, high 6, medium 3, low 1. See [how the score works](/desk/scorecard/).

## Agentic (58 checks)

| ID | Category | Severity | Title |
|---|---|---|---|
| `agentic.a2a-auth-required` | Authentication | critical | A2A agent endpoints require authentication |
| `agentic.agent-artifacts-pinned` | Supply chain | medium | Agent skills, images and tool packages are pinned |
| `agentic.agent-internet-reach` | Isolation | critical | Agents cannot reach the internet directly (Enterprise, Agent reach) |
| `agentic.agent-least-reach` | Isolation | medium | Agents reach only what they use (Enterprise, Agent reach) |
| `agentic.agent-no-shared-channel` | Isolation | high | Agents do not share an identity, a volume or a writable store (Enterprise, Agent reach) |
| `agentic.agent-references-resolve` | Resilience | high | Agent references resolve and agents are ready |
| `agentic.agent-transitive-fetcher` | Isolation | critical | Agents cannot borrow internet access through a registry or proxy (Enterprise, Agent reach) |
| `agentic.agent-write-sensitive` | Authorization | high | Agents cannot write to data stores, registries or credential services they only need to read (Enterprise, Agent reach) |
| `agentic.agents-in-ambient` | Encryption | high | Agents, MCP servers and gateways are in the ambient mesh |
| `agentic.agents-use-llm-gateway` | Guardrails | high | Agents reach models and tools through the gateway |
| `agentic.audit-access-log` | Observability | medium | Gateways write an access log with caller identity |
| `agentic.auth-mode-enforcing` | Authentication | high | API key and basic authentication reject missing credentials |
| `agentic.authz-identity-source-present` | Authorization | high | Authorization rules that read jwt or apiKey have an authentication policy behind them |
| `agentic.authz-no-broad-rule` | Authorization | high | No authorization rule allows everything |
| `agentic.backend-tls-upstream` | Encryption | high | External MCP upstreams use TLS |
| `agentic.basic-auth-users-in-secret` | Authentication | medium | Basic auth users live in a Secret, hashed with bcrypt |
| `agentic.code-mode-scoped` | Guardrails | high | Code mode backends have a tool allowlist and tight time limits |
| `agentic.cors-safe` | Authorization | high | CORS does not allow credentials from any origin |
| `agentic.credential-refs-resolve` | Identity | high | Credential references name Secrets and keys that exist |
| `agentic.credentials-not-plaintext` | Encryption | medium | Credentials are not sent over plain HTTP |
| `agentic.dynamic-forward-proxy-restricted` | Isolation | critical | Forward proxies authenticate callers and allow only known hosts |
| `agentic.enforcement-fails-closed` | Guardrails | high | Guardrails and external auth fail closed |
| `agentic.gateway-listener-tls` | Encryption | medium | Exposed agentgateway listeners terminate TLS |
| `agentic.gateway-routes-namespace-restricted` | Isolation | high | Agentgateway listeners restrict which namespaces can attach routes |
| `agentic.jwt-issuer-audience-pinned` | Authentication | high | JWT providers pin the issuer and audience |
| `agentic.jwt-strict-mode` | Authentication | high | JWT validation runs in Strict mode |
| `agentic.kagent-accesspolicy-baseline` | Authorization | high | Agent namespaces have a deny-all AccessPolicy baseline |
| `agentic.kagent-cross-namespace-refs` | Authorization | medium | Agents and tool servers limit which namespaces can reference them |
| `agentic.kagent-dedicated-serviceaccount` | Identity | high | Each agent runs under its own service account |
| `agentic.kagent-privilege-separated-toolservers` | Identity | high | Tool servers with write tools are not shared between agents |
| `agentic.kagent-sandbox-egress-scoped` | Isolation | medium | Agent sandboxes allow only named domains |
| `agentic.kagent-tool-scope` | Guardrails | medium | Agents name their tools and gate high-impact ones |
| `agentic.llm-auth-required` | Authentication | critical | LLM routes require caller authentication |
| `agentic.llm-mcp-timeouts` | Resilience | medium | LLM and MCP routes set a request timeout |
| `agentic.llm-spend-controlled` | Cost | medium | LLM spend is capped by a budget or a token rate limit |
| `agentic.mcp-auth-required` | Authentication | critical | MCP endpoints require authentication |
| `agentic.mcp-authn-traffic-phase` | Authentication | low | MCP authentication runs in the traffic phase |
| `agentic.mcp-guardrails-cover-tool-calls` | Guardrails | medium | MCP guardrails inspect tools/call |
| `agentic.mcp-prefix-no-shadowing` | Guardrails | medium | Multi-target MCP backends prefix tool names |
| `agentic.mcp-selector-targets-scoped` | Supply chain | medium | MCP selector targets name the Services they federate |
| `agentic.mcp-server-not-directly-reachable` | Isolation | high | MCP servers are only reachable through the gateway |
| `agentic.mcp-tool-authorization` | Authorization | high | MCP backends have a tool allowlist (rug-pull defence) |
| `agentic.mcp-transport-current` | Resilience | low | MCP targets use Streamable HTTP rather than SSE |
| `agentic.metadata-endpoint-blocked` | Isolation | high | Agent namespaces cannot reach the cloud metadata endpoint |
| `agentic.no-inline-crd-credentials` | Identity | critical | No credentials written inline in agent and gateway resources |
| `agentic.no-literal-credentials` | Identity | critical | No literal credentials in routes or agent env |
| `agentic.no-token-passthrough` | Identity | critical | The caller's token is not passed through to upstreams |
| `agentic.policy-accepted-and-attached` | Resilience | high | Policies are accepted, attached and reference objects that exist |
| `agentic.prompt-guard-request` | Guardrails | high | LLM requests pass through a prompt guard |
| `agentic.prompt-guard-response` | Guardrails | medium | LLM responses are guarded (output gating) |
| `agentic.prompt-guard-streaming` | Guardrails | medium | Response guards also run on streamed responses |
| `agentic.provider-secret-ref` | Identity | high | Upstream credentials come from existing Secrets |
| `agentic.rate-limit` | Resilience | high | LLM and MCP routes are rate limited per caller |
| `agentic.rate-limit-key-trusted` | Cost | high | Rate limit keys come from verified identity, not raw headers |
| `agentic.registry-hardened` | Supply chain | medium | Agentregistry runs with audit, approval and an external database |
| `agentic.retries-idempotent-only` | Cost | medium | Retries on LLM and MCP routes are limited to safe requests |
| `agentic.tracing` | Observability | low | Gateways export traces |
| `agentic.upstream-tls-verified` | Encryption | high | Upstream TLS certificates are verified |

## Istio (37 checks)

| ID | Category | Severity | Title |
|---|---|---|---|
| `ambient.authz-references-resolve` | Authorization | medium | Authorization policies refer to things that exist |
| `ambient.dedicated-service-accounts` | Identity | medium | Workloads that policies name have their own service account |
| `ambient.default-deny` | Authorization | high | Each ambient namespace starts from default-deny |
| `ambient.egress-controlled` | Isolation | high | Outbound traffic from mesh namespaces is controlled |
| `ambient.egress-default-deny` | Isolation | high | The ztunnel egress gateway starts from default-deny |
| `ambient.extension-hygiene` | Supply chain | low | No EnvoyFilter, and security Wasm fails closed |
| `ambient.ingress-use-waypoint` | Authorization | high | Ingress traffic goes through the waypoint that holds the policy |
| `ambient.l7-needs-waypoint` | Authorization | high | L7 policy is attached to a waypoint |
| `ambient.mtls-strict` | Encryption | high | mTLS is STRICT |
| `ambient.namespace-enrolled` | Encryption | high | Namespaces with workloads are enrolled in ambient |
| `ambient.no-allow-all` | Authorization | critical | No ALLOW policy admits any source |
| `ambient.no-opt-out` | Isolation | medium | No workload opts out of an ambient namespace |
| `ambient.no-sidecar-mix` | Isolation | medium | No sidecar injection alongside ambient |
| `ambient.no-tls-disable` | Encryption | medium | No DestinationRule disables TLS or its verification |
| `ambient.policy-enforced` | Authorization | high | Mesh policies are bound to something that enforces them |
| `ambient.principal-trust-domain` | Authorization | high | Principals use the mesh trust domain |
| `ambient.request-auth-enforced` | Authentication | high | RequestAuthentication is backed by a policy requiring a token |
| `ambient.serviceentry-waypoint` | Isolation | high | External ServiceEntries are bound to a waypoint |
| `ambient.telemetry` | Observability | medium | Mesh access logging is configured |
| `ambient.vs-source-match` | Routing | medium | VirtualServices on waypoint hosts do not match on the source |
| `ambient.waypoint-earns-keep` | Cost | low | Every waypoint is used |
| `ambient.waypoint-no-bypass` | Authorization | medium | Workloads behind a waypoint only accept traffic from it |
| `ambient.waypoint-wiring` | Authorization | high | Waypoints referenced by use-waypoint exist |
| `ambient.workloads-captured` | Encryption | critical | Pods in ambient namespaces are captured by ztunnel |
| `ambient.ztunnel-hardening` | Resilience | medium | ztunnel and istio-cni run as node-critical with requests |
| `mesh.control-plane-metrics-clean` | Health | medium | istiod reports no config conflicts |
| `mesh.global-service-authz` | Authorization | high | Global services restrict who may call them |
| `mesh.istiod-ha` | Resilience | high | istiod runs with at least two replicas |
| `mesh.jwks-fetch-restricted` | Authentication | medium | istiod cannot be pointed at internal JWKS URLs |
| `mesh.mesh-ports-not-public` | Network exposure | high | Control-plane and admin ports are not exposed |
| `mesh.no-remote-secrets` | Multicluster | high | Peering does not also use remote secrets |
| `mesh.peer-gateway-config` | Multicluster | high | Peer gateways are fully described |
| `mesh.plugged-in-ca` | Identity | high | The mesh uses a plugged-in CA |
| `mesh.revision-resolves` | Config hygiene | high | istio.io/rev labels name a revision that exists |
| `mesh.serviceentry-scope` | Isolation | medium | ServiceEntries are scoped tightly |
| `mesh.shared-root` | Identity | critical | Peered clusters share a root and have their own intermediates |
| `mesh.solo-distribution` | Supply chain | high | Mesh components run Solo's Istio build |

## Ingress (11 checks)

| ID | Category | Severity | Title |
|---|---|---|---|
| `gateway.client-ip-policy` | Authorization | high | IP-based policy at the gateway sees the real client address |
| `gateway.cors-safe` | Security | medium | CORS does not allow any origin with credentials |
| `gateway.gateway-ha` | Resilience | medium | Gateway and waypoint proxies run more than one replica |
| `gateway.kgw-auth-fails-closed` | Authentication | high | kgateway authentication fails closed |
| `gateway.kgw-public-route-protected` | Authentication | medium | Public kgateway routes are authenticated and rate limited |
| `gateway.listener-tls` | Encryption | high | Exposed gateways serve HTTPS |
| `gateway.referencegrants` | Isolation | high | Cross-namespace references have a ReferenceGrant |
| `gateway.shared-gateway-isolation` | Isolation | medium | Shared gateways limit who can attach routes |
| `gateway.timeouts-retries` | Resilience | low | Ingress routes set timeouts and bounded retries |
| `gateway.tls-secret-valid` | Encryption | critical | Listener certificates point at usable Secrets |
| `gateway.upstream-tls-verified` | Encryption | high | TLS to backends verifies the server |

## Kubernetes (29 checks)

| ID | Category | Severity | Title |
|---|---|---|---|
| `k8s.capabilities` | Workload security | high | Capabilities are dropped, not added |
| `k8s.cert-expiry` | Platform | high | Certificates are not close to expiry |
| `k8s.cluster-admin-bindings` | Identity | critical | cluster-admin is not granted to workloads or groups |
| `k8s.default-serviceaccount` | Identity | medium | Workloads use their own ServiceAccount |
| `k8s.deprecated-apis` | Platform | medium | Applied manifests use current API versions |
| `k8s.ha-replicas` | Resilience | high | Critical components run more than one replica |
| `k8s.helm-releases` | Platform | medium | Helm releases are deployed, not failed or stuck |
| `k8s.host-access` | Isolation | high | No host namespaces, host ports or sensitive host paths |
| `k8s.image-pinned` | Supply chain | high | Images are pinned by digest |
| `k8s.netpol-ambient-compatible` | Isolation | medium | NetworkPolicies in ambient namespaces allow HBONE and kubelet probes |
| `k8s.netpol-default-deny` | Isolation | medium | Application namespaces deny ingress by default |
| `k8s.no-privileged` | Workload security | critical | No privileged containers |
| `k8s.node-health` | Platform | high | Nodes are Ready, without pressure, on a supported kubelet |
| `k8s.pdb` | Resilience | medium | Critical components have a working PodDisruptionBudget |
| `k8s.pod-security-admission` | Isolation | high | Namespaces enforce a Pod Security Standard |
| `k8s.privilege-escalation` | Workload security | high | allowPrivilegeEscalation is false |
| `k8s.probes` | Resilience | medium | Long-running containers have readiness probes |
| `k8s.rbac-escalation` | Identity | high | No RBAC paths to privilege escalation |
| `k8s.rbac-secret-read` | Identity | high | Secret read access is narrow |
| `k8s.rbac-wildcards` | Identity | high | No wildcard RBAC rules |
| `k8s.readonly-rootfs` | Workload security | low | Root filesystems are read-only |
| `k8s.resource-requests` | Resilience | medium | Containers set CPU and memory requests and a memory limit |
| `k8s.run-as-non-root` | Workload security | medium | Containers run as a non-root user |
| `k8s.sa-token-automount` | Identity | medium | API tokens are mounted only where they are used |
| `k8s.seccomp` | Workload security | medium | A seccomp profile is set |
| `k8s.secrets-in-env` | Identity | low | Credentials are not held in environment variables |
| `k8s.service-exposure` | Isolation | high | Only gateways are exposed outside the cluster |
| `k8s.spread` | Resilience | medium | Replicas are spread across nodes and zones |
| `k8s.version-supported` | Platform | high | Kubernetes is a supported release |

