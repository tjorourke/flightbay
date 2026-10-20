---
title: Ambient resilience controls
description: Route-scoped rate limits, circuit isolation and retries, with shared-limiter prerequisites and request-chain boundaries.
weight: 35
---

Configure uses the same **Target → Configurations → Configure → Review** flow for rate limits, circuit breakers and retries. The Live YAML pane renders the exact plan as fields change. Multi-cluster selections produce independent plans and dry-runs before the final apply confirmation.

The global-limit form selects captured namespaces, limiter Services, advertised gRPC ports and explicit quota definitions. If the quota definition was not captured, enter the existing limiter's domain and descriptor reference; Flightbay does not guess a quota or its threshold. Retry conditions and attempt counts have presets, durations use amount/unit controls, and circuit breaking has load profiles and explicit consecutive-error or percentage detection choices. Custom values remain available.

Select several resilience controls for the same API to generate one shared HTTPRoute and waypoint extension, rather than competing routes. Adding another control to an already configured API preserves its existing rate limit, retry settings and circuit-isolation backend. Edit actions retain the API boundary. Undo later edits before removing the earlier change that created their shared resource.

## Supported implementations

Solo Enterprise Istio waypoints support the generated route-scoped EnvoyFilters. Upstream Istio does not support EnvoyFilter on waypoints: Flightbay does not enable this generator just because its CRD exists. It requires captured Enterprise installation and waypoint evidence. Native DestinationRule connection pools and outlier detection remain available for Istio waypoints. HTTPRoute retries require the installed experimental retry schema when the Enterprise extension is unavailable. Agentgateway local rate limits use its own policy API.

The controls below were exercised on Solo Istio 1.30.4 on `kind-mesh1` and `kind-mesh2`. Other installations require their own server dry-run and traffic tests.

## Choose the boundary

- **Whole Service** applies the control to that Service's route configuration.
- **API path** creates a matching HTTPRoute rule. The catch-all preserves the original backend for requests that do not match. Existing routes still compete under Gateway API precedence.
- **Existing HTTPRoute rule** selects a captured rule without replacing its matches or backends. This is available for rate limits and retries.

For circuit isolation, API-path mode also creates a backend Service alias selecting the same application Pods. Its distinct upstream cluster has its own connection pool and endpoint health state. No application Deployment is created. Set different paths and thresholds to configure different APIs independently.

Service and API-path policies also cover captured published global names. Refresh and regenerate after publishing a new alias. Existing-rule mode applies only to the explicitly selected HTTPRoute rule. Local buckets belong to their individual route or virtual host, including DNS aliases; use a shared global descriptor when calls through different DNS names must consume the same quota.

## Rate limiting

### Why the local YAML is an EnvoyFilter

For the selected Solo Enterprise Istio waypoint, the documented local-rate-limit mechanism is Envoy's local filter. Flightbay uses three pieces: install the named filter, configure its token bucket only on the selected route or virtual host, and bind the extension to the selected waypoint Gateway. Local and published Service names have separate route configurations, so both are included when captured.

A filter-level bucket for the whole waypoint is shorter, but also limits other Services sharing that waypoint. The scoped template intentionally avoids that effect. The `flightbay.dev/resilience-request` annotation stores editor settings; it is not part of Envoy's decision. Global quotas use the existing external limiter instead of a local bucket. Agentgateway uses its own higher-level policy API when it is the selected waypoint implementation.

| Customer requirement | Behaviour |
|---|---|
| Limit one API rather than an entire Service | Select API path or an existing HTTPRoute rule. |
| Different limits for APIs in the same Service | Use distinct path/rule policies. Local token buckets are separate. Shared quotas need distinct descriptor values in the rate-limit service. |
| Per-Pod limit | A waypoint local bucket is per **waypoint process**, not per backend application Pod. A Pod-specific budget needs application-side enforcement or another explicitly supported workload-level design. |
| Global limit across Pods | Call an existing external Envoy Rate Limit Service backed by a shared quota store. All requests for the quota use the same domain and descriptor. |
| Exhaust one API without limiting others | Attach the filter only to that route and give it its own descriptor. Unmatched APIs retain their normal route. |
| Consistency across waypoints | All waypoints must call the same logical limiter and shared store. Independently deployed Redis stores do not form a global quota. |

### Ambient setup owns Redis and the limiter

```text
Waypoint → existing rate-limit service → shared Redis
```

**Flightbay does not deploy Redis or the rate-limit service.** Ambient setup must provide them and configure the quota thresholds. Configure connects the waypoint client to the existing limiter's Service port, domain and API descriptor.

Redis authentication belongs on the limiter-to-Redis connection. Use a Kubernetes Secret reference for its password, and configure the Redis endpoint and any required TLS in that existing service. Redis credentials are not copied into the EnvoyFilter or Live YAML. The waypoint does not connect directly to Redis.

Direct mode generates a plaintext HTTP/2 gRPC connection to the captured limiter Service. Use it only with a service configured for that transport. For a limiter already published through the peered ambient mesh, select **Use an already-published global mesh Service**. Every selected caller cluster must have captured ServiceEntry evidence for its hostname and port. This mode reuses Istio's delivered upstream cluster and mesh/HBONE transport, allowing both meshes to use the same limiter rather than creating separate quota stores.

**Fail closed** controls what happens to matching routes when the limiter is unavailable. It does not convert unrelated routes into rate-limited routes. Quota exhaustion returns HTTP 429; an unavailable fail-closed limiter can return an error instead.

## Circuit breaking

Connection pools limit connections, pending requests, concurrent requests and outstanding retries. An overflow can reject an excess request with HTTP 503. Outlier detection temporarily ejects failing endpoints from that upstream cluster.

| Customer requirement | Behaviour |
|---|---|
| Circuit isolation for one API | API-path mode gives the API a separate Service alias and upstream cluster. |
| Different thresholds for different APIs | Create separate API-path policies. |
| Consecutive 5xx errors | Set the consecutive-error threshold, detection interval and ejection duration. |
| 5xx error percentage | Enterprise cluster patches support failure-percentage detection, with a minimum of 50 requests per interval and one upstream host. This is not an instantaneous percentage over an arbitrary rolling time window. |
| Latency-triggered opening | No native latency-threshold breaker is generated. Use request timeouts and application or external health logic for that requirement. |
| Keep unrelated APIs working | Their requests use the original backend cluster, not the isolated alias. |

These controls are per upstream cluster in each waypoint. They are not a globally shared closed/open/half-open state machine. Ejection, concurrency rejection and timeout handling have different semantics; the preview notes identify which settings are generated.

## Retries across A → B → C

Each proxy hop applies its own route's retry policy and time budget. The Enterprise route editor exposes retry conditions, retry attempts, a total hop timeout and a per-attempt timeout. A retry count of two permits up to three attempts at that hop.

An application must propagate the remaining deadline when it makes another request. Configuring A's proxy timeout does not automatically transfer that deadline to the independent request B makes to C. The same applies to application cancellation.

Envoy stops its own retries when the downstream stream is reset or its total request timeout expires. If A has timed out but B continues an independently created request to C, that request and its retries do not stop just because A has stopped waiting. Deadline and cancellation propagation must cover the application calls as well as the proxy configuration.

To prevent retry amplification, choose one retry layer for a request chain where possible, keep attempt counts small, bound total and per-attempt time, and account for application/client retries. Multiple retry layers can multiply the number of attempts reaching C. Connection-pool retry limits cap outstanding retries at that upstream cluster; they are not a request-chain-wide budget.

API-path and existing-rule modes keep retry settings route-scoped. Consider whether the operation can safely be repeated: retrying a non-idempotent operation can repeat application work.

## Verification

`test/e2e/resilience.py` exercises separately installed test fixtures. It checks local API quotas, retry attempts, isolated connection overflow and endpoint ejection on both meshes, plus one shared quota across different waypoints and across the peered meshes. Unrelated APIs must continue returning HTTP 200. The external test Redis requires authentication; its password is supplied through Secret references.

The test fixtures in `test/e2e/resilience-fixture.yaml` and `test/e2e/resilience-rls-fixture.yaml` are installed separately with kubectl. Their infrastructure is not part of a generated Flightbay configuration plan.
