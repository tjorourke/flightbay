---
title: Ambient investigation and configuration
weight: 17
description: Trace Service-to-waypoint attachment, explain controller errors, generate L4/L7 configuration and explicitly apply a test plan to a selected real cluster.
---

## Open an investigation

Open a capture normally, then choose **Ambient** in the sidebar, or open a Service in Resources and choose **Investigate ambient**.

```bash
flightbay ui ~/Downloads/flightbay-objects.tar.zst
```

Choose the cluster and Service. The **Guided investigation** tab traces the configuration for Service-addressed traffic. Choose a particular HTTPRoute if its intended Service is ambiguous, or leave **Related routes** selected.

![Guided ambient attachment investigation](/images/ambient-investigation.png)

The investigation checks:

- ambient namespace enrolment, pod opt-outs and sidecars;
- Service waypoint overrides and namespace inheritance;
- missing waypoints, waypoint traffic types and cross-namespace enrolment;
- Gateway `Accepted` and `Programmed` status;
- route parent kind, API group, namespace and Service port;
- per-parent, per-controller `Accepted` and `ResolvedRefs` conditions;
- stale `observedGeneration` values and missing controller status;
- Gateway listener names, ports, protocols, allowed route kinds, namespace restrictions and hostname intersection;
- backend Service existence, Service ports versus container targetPorts, and cross-namespace ReferenceGrants;
- L7 fields on workload-selector AuthorizationPolicies that ztunnel cannot enforce;
- unsupported ambient `PeerAuthentication` DISABLE settings;
- ready/eligible endpoints and availability of captured waypoint proxy diagnostics.

Every result is **Pass**, **Failed**, **Check**, **Unknown** or **Not applicable**. Click the evidence to inspect the original captured object. Missing, excluded, trimmed or reviewed-out data does not prove that a resource is absent.

### A waypoint Gateway is not always a wrong route parent

A Service parent scopes a route to that Service:

```yaml
parentRefs:
- group: ""
  kind: Service
  name: checkout
  port: 8080
```

Gateway parents can be valid for ingress or waypoint-wide routing, depending on the controller and version. Flightbay explains that scope rather than rejecting every Gateway parent. A missing `kind` defaults to Gateway; a missing `group` defaults to `gateway.networking.k8s.io`. Service parents need both fields explicitly set as above.

For a route pointing to the selected waypoint Gateway, Flightbay offers a downloadable **suggested Service-parent change**. It is a merge snippet for review, not a complete replacement manifest or an automatically applied fix. Other parent references are retained; route rules are not changed. The selected frontend Service is not inferred from the backend name.

Resource acceptance is not proof of delivered proxy configuration or successful traffic. If diagnostics were collected, follow **Inspect delivered proxy config** to compare the waypoint's actual configuration and xDS state. Without them this part of the investigation is unknown. Root-namespace policies and configuration outside the capture can also affect traffic.

## Collect enough evidence

An objects-only capture supports configuration investigation. Include the application namespace and any other namespace containing its waypoint, policies, or cross-namespace backends. Nodes are needed to discover endpoint regions for locality failover.

```bash
flightbay collect --context kind-mesh1 \
  --namespaces payments,istio-system \
  --collect objects --no-logs --no-portforward \
  --out ~/Downloads/flightbay-ambient.tar.zst
```

For logs and proxy configuration, use a normal capture with `--diagnostics` instead of the objects-only/no-portforward options. Captures remain redacted and scoped to the chosen namespaces.

## Configuration wizard

Choose a deployed Service and open **Configuration wizard**. Capabilities depend on the selected waypoint class and the API types visible in the capture. Unavailable tasks stay visible with an explanation.

| Task | Enforcement/configuration |
|---|---|
| Deny by default | ztunnel workload-selector AuthorizationPolicy |
| Allow a source namespace | ztunnel AuthorizationPolicy for a direct ambient path without a selected waypoint |
| Require mutual TLS | Workload-selector STRICT PeerAuthentication |
| Allow HTTP methods | Service-targeted AuthorizationPolicy on an Istio waypoint |
| Balance between Services | Service-parent HTTPRoute with explicit backend weights |
| Request timeout | HTTPRoute request timeout |
| Endpoint load balancing | Istio-waypoint DestinationRule: ROUND_ROBIN, LEAST_REQUEST or RANDOM |
| Eject unhealthy endpoints | Istio-waypoint DestinationRule outlier detection |
| Region failover | DestinationRule locality failover with outlier detection; requires captured endpoint regions |
| Local request rate limit | Service-targeted AgentgatewayPolicy or EnterpriseAgentgatewayPolicy on the matching agentgateway waypoint |

![Generated ambient configuration plan](/images/ambient-wizard.png)

Choose the desired tasks, Service port and parameters, then **Generate YAML**. You can copy or download it without connecting to any cluster. Backend weights must total 100, refer to distinct Services in the same namespace and use their Service ports.

These controls have different semantics:

- **ztunnel is L4.** It cannot enforce HTTP methods, paths or request-rate limits. A selector policy affects the selected Pods, including other Services that select those Pods.
- **Waypoint L7 controls need traffic to traverse the waypoint.** For example, allowing an original caller namespace at ztunnel is not the same as allowing it at a waypoint: the destination-side ztunnel may see the waypoint's identity.
- **Local rate limits are per proxy.** They are not a shared quota across replicas. Stock Istio waypoints do not have a built-in rate-limit template in this wizard.
- **Outlier detection ejects endpoints.** It does not establish primary/backup priority between different Service names. A zero-weight HTTPRoute backend is not a standby.
- **Existing policies and routes matter.** ALLOW policies are additive, existing routes may take precedence, and another DestinationRule can conflict with the generated one.

Primary/backup service failover and shared/global rate limiting are shown as unavailable until a supported integration and its required configuration are provided. No EnvoyFilter workaround is generated for waypoints.

## Apply for testing

Generation and investigation are offline. Test actions use a **separate real kubeconfig connection**, not the read-only snapshot API or live mirror transport.

1. Review the exact generated YAML and its impact notes.
2. Explicitly choose a local kubeconfig **context**, shown with its API server address. There is no automatic current-context selection.
3. Choose **Validate with server dry-run**. Flightbay checks that the Service UID, selector, ports and waypoint configuration still match the runtime capture. It also checks the installed API schemas, admission and your create permissions using Kubernetes dry-run requests. A changed preview must be regenerated.
4. Choose **Apply for testing**. Only that validated plan can be created, and validation expires after ten minutes. Existing objects are never overwritten.
5. Use **Refresh attachment status** and exercise traffic separately. A successful create does not mean a route was attached or the intended traffic behaviour occurred.
6. Choose **Remove these test resources**. Cleanup reads back the objects and deletes only the recorded Flightbay-owned UIDs, then verifies that they are absent. A replacement object is not deleted.

Created resources have `flightbay-test-…` names and ownership labels. If applying a multi-resource plan fails, Flightbay attempts to remove the resources it created and reports any cleanup still needed.

Test records are stored with private file permissions under `~/.flightbay/ambient-tests` (or `$FLIGHTBAY_HOME/ambient-tests`). **Active test resources** can therefore be inspected and cleaned up after reopening a capture or restarting the desk. Applying a new plan still requires validation in the new session. Cluster credentials are not stored in the records.

For a non-default kubeconfig when opening a file:

```bash
flightbay ui capture.tar.zst --test-kubeconfig ~/kubeconfigs/test.yaml
```

In live mode the wizard uses the kubeconfig passed to `--kubeconfig`, but still requires an explicit context and separate dry-run/apply actions. Repository scans without runtime Service UIDs can generate YAML but cannot be used as authority to apply to an unrelated cluster.

## Validation

`python3 test/e2e/ambient.py --context kind-mesh1` creates a dedicated namespace on an existing local kind ambient cluster. It proves rejected-parent diagnosis, weighted routing, a real timeout response, L7 method enforcement, ztunnel access control, unhealthy endpoint ejection and, when an agentgateway waypoint class is installed, actual HTTP 429 responses. It applies the wizard's generated plans and uses the wizard's cleanup endpoint, then deletes its namespace and reads the state back. It does not change existing application namespaces.

The live run on 16 October 2026 passed all 16 checks with Istio 1.30.4 and Enterprise agentgateway 2026.9.2. Browser testing also covered generation, server dry-run, apply, attachment status, recovery after reload and cleanup. Other installed versions must pass their own API server dry-run before a test plan can be applied.
