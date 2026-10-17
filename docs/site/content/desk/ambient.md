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

Use the searchable dropdown to choose a Namespace, Service, Pod, Gateway, HTTPRoute or ServiceEntry. Type a name, namespace, cluster or kind; arrow keys and Enter work, and the object-type filter narrows the suggestions. Ambient enrolment is not limited to Services: namespaces provide defaults, Pods can have dataplane/waypoint overrides, Gateways provide waypoint infrastructure, and ServiceEntries describe external destinations.

**Clear selection** resets the object type, search value and draft. **New configuration** also switches to Configure and starts at the target step. Neither action applies or undoes cluster changes; any active tests remain available in **Active tests**. Selecting an incompatible object type clears the old target instead of leaving hidden selection state behind.

The **Investigate** tab adapts to the object type. Service investigations trace Service-addressed traffic. Pod investigations distinguish workload/Pod-IP waypoint handling from Service traffic, and ServiceEntries are not diagnosed using Kubernetes EndpointSlice readiness. Related-object buttons let you move between these scopes. Choose a particular HTTPRoute if its intended Service is ambiguous, or leave **Related routes** selected.

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

Failures open first; other checks are compact, expandable rows. Raw fields are inside **View evidence**. Each step includes labelled **Official docs** links to the relevant upstream or Enterprise guide. Current conditions no longer carry a misleading stale-status warning; that warning appears only when the observed generation is actually stale or missing.

## Detected implementation and official documentation

The implementation strip shows the selected waypoint/controller and the policy YAML kind that matches it. **Detection details** explains the evidence: captured GatewayClasses, deployed Gateways, discovered policy APIs and recognised Istio control-plane/ztunnel images. Missing evidence stays unknown. A cluster can contain OSS and Enterprise components together; it is not assigned one blanket API shape.

For agentgateway tasks, the selected controller determines `AgentgatewayPolicy` versus `EnterpriseAgentgatewayPolicy`, and the discovered API supplies the version. Installing both CRDs does not mean both controllers are active. A new waypoint can only use a supported class actually observed in the selected cluster. Refresh or collect the missing GatewayClasses/Gateways if no class is offered.

Task cards, configuration options and debugging steps link to official documentation in context. Istio OSS, Enterprise Istio, agentgateway OSS and Enterprise agentgateway sources are labelled; shared APIs link to Kubernetes or Gateway API references. These guides describe their published versions, so compare their version selector with your installed build. Links are explanatory, not a declaration that every documented feature is supported by your cluster. Server dry-run is still required before applying a generated test.

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

Agentgateway waypoint controllers can declare an HTTP mesh listener on port 15088 and render a separate HBONE transport listener on 15008. Flightbay recognises this controller-specific form and checks the captured generated Service ports; it does not require the Gateway declaration itself to contain an HBONE listener in that case.

## Attachment map and existing configuration

The **Attachment map** tab shows namespace defaults, selected waypoints, routes, backend Services, policies and selected workloads. Cards open the captured objects. Backend weights and calculated percentages are shown per route rule; a 90/10 split on one rule is not added to weights on another rule. This is a configuration relationship diagram, not observed traffic.

For a **Namespace** or **Gateway**, the map opens a compact Service overview with the total captured count and a filter. All captured Services are included, including gateway infrastructure. Cards show ready/eligible endpoint counts, the selected waypoint and related-route count. **Explore connections** opens the individual Service map; **Configure** starts its wizard. A Service with missing endpoint evidence says so explicitly, rather than receiving a generic Unknown badge by default.

![Namespace overview with all captured Services and real endpoint counts](/images/ambient-namespace.png)

![Service attachment map with routes, policies and backend weights](/images/ambient-map.png)

The map and its companion table have **Edit in wizard** actions for supported existing HTTPRoute rules, DestinationRule settings, single-operation method policies and local request-rate buckets. The wizard loads the actual captured values. Route edits preserve other rules, matches, filters, annotations and attachment references. Unsupported shapes remain evidence-only, with an explanation; they are not flattened into a different configuration.

![Existing route weights loaded into the appropriate editor](/images/ambient-edit.png)

Existing edits produce before/after field previews and scoped merge snippets rather than a competing new route. Test application updates the original resource using its UID and an atomic resourceVersion check. Undo preserves unrelated changes and refuses to overwrite a field edited outside the test.

## From a failed step to a remedy

Genuine waypoint listener failures include a **Suggested next step**, a **Review waypoint fix** button and the matching official waypoint guide. The button opens waypoint selection/deployment and a binding preview; it does not immediately edit the Gateway. When agentgateway's generated Service lacks port 15008, the guidance points to controller reconciliation rather than manually modifying that managed Service. Missing capture evidence prompts a refresh or collection before making changes.

Known actionable checks offer a wizard button. For example, **Ready endpoints → Investigate and fix endpoints** shows collection state, matching Pods, readiness, declared ports and matching Deployments.

![Evidence-aware endpoint recovery](/images/ambient-recovery.png)

Choose an appropriate action:

- **Missing capture data:** copy a narrowly scoped capture command. No fake EndpointSlice or speculative workload configuration is generated.
- **Incorrect selector:** explicitly choose the intended captured Pod, review stable selector labels and generate a Service selector change. The selector must match that Pod.
- **Matching Deployment scaled down:** explicitly choose the Deployment and desired replicas. Its template must match the Service selector.
- **Incorrect targetPort:** choose a selected Pod and one of its declared container ports. Other Service ports are retained.
- **No healthy matching workload:** inspect Pod startup, readiness, scheduling and events. Changing a selector or replica count is not presented as a cure for an unhealthy application.

Parent-reference checks can open a route-parent wizard with the affected route already selected. The intended frontend Service and Service port remain explicit; other parents and route rules are retained.

## Select, deploy or share a waypoint

Use the waypoint remedy on an investigation, or **Select or deploy a waypoint** on a Service. Namespace, Gateway and ServiceEntry entry points also offer enrolment setup.

![Existing waypoint selection with explicit binding scope and implementation-specific documentation](/images/ambient-waypoint.png)

Choose **existing** or **new**, then the binding scope:

- **Only this object:** change the selected Service or ServiceEntry binding.
- **Namespace default:** set the namespace waypoint labels. Existing object overrides remain effective unless the clear-override option is explicitly selected.
- **Shared:** choose the namespaces to enrol. There is no automatic global default; only those namespaces are changed.

A new waypoint uses a selected supported GatewayClass and an HBONE listener. Its access selector names the permitted namespaces. Testing verifies that the real GatewayClass exists and waits for the new Gateway to report Programmed before changing bindings.

For an existing waypoint, the planner checks namespace access. An explicitly requested access change can extend a Same/explicit-name HBONE allowlist, preserving already allowed names. General namespace selectors are not silently broadened or replaced with All. Namespace ambient enrolment and clearing an object's override are separate explicit options.

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

Choose a deployed Service and open **Configure**. Capabilities depend on the selected waypoint class and the API types visible in the capture. Unavailable tasks are grouped under an expandable explanation.

The Configure tab follows **Target → Task → Configure → Review**. Supported tasks appear as short cards with official documentation links. Unavailable tasks are kept under **Other tasks · why unavailable?** rather than filling the main screen. Back preserves your choices. The review opens on a concise change summary; **Generated YAML** is a separate tab. Cluster testing is an optional expandable section after review.

![Supported task cards with contextual official documentation](/images/ambient-tasks.png)

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

Choose the desired tasks, Service port and supported parameters, then **Generate YAML**. You can copy or download it without connecting to any cluster. Backend references must name distinct Services in the same namespace and use their Service ports.

Options are task-specific:

- Service distribution can use **default/equal weights**, which omits explicit weights, or **explicit weights**. Percentages total 100; existing relative-weight rules can retain their original relative scale.
- Changing a backend Service updates its port choices to that Service's captured ports; it does not retain a port from the previous backend.
- Endpoint load balancing can keep the **controller default** or select ROUND_ROBIN, LEAST_REQUEST or RANDOM. Choosing the default alone produces a no-change preview.
- Method policies can ALLOW or DENY the selected methods.
- Ambient mTLS supports STRICT or PERMISSIVE; DISABLE is rejected.
- Timeouts expose request and optional backend timeouts, including supported zero-duration semantics.
- Outlier detection exposes failure threshold, check interval, base ejection duration and maximum ejection percentage.
- Local rate limiting exposes request count, time unit and independent burst capacity. Existing bucket keys and other fields are preserved when editing.

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
4. Choose **Apply for testing**. Only that validated plan can be applied, and validation expires after ten minutes. New resources are created without overwriting existing objects; existing edits update only their reviewed fields with UID/resourceVersion checks.
5. Use **Refresh attachment status** and exercise traffic separately. A successful create does not mean a route was attached or the intended traffic behaviour occurred.
6. Choose **Undo changes and remove test resources** (or **Remove these test resources** for a create-only plan). Existing fields are restored only while they still equal this test's admitted values. Unrelated fields are preserved. Created objects are deleted only with matching recorded UIDs and ownership, and absence is verified. A replacement object is not deleted.

Generated traffic-policy resources have `flightbay-test-…` names; new waypoint names are explicitly chosen. Created resources carry ownership labels. If applying a multi-resource plan fails, Flightbay attempts to undo existing-field changes and remove created resources. Bindings are restored before a created waypoint is removed; an undo conflict retains the Gateway and reports the remaining work rather than disrupting a binding it cannot restore.

Test records are stored with private file permissions under `~/.flightbay/ambient-tests` (or `$FLIGHTBAY_HOME/ambient-tests`). **Active test resources** can therefore be inspected and cleaned up after reopening a capture or restarting the desk. Applying a new plan still requires validation in the new session. Cluster credentials are not stored in the records.

For a non-default kubeconfig when opening a file:

```bash
flightbay ui capture.tar.zst --test-kubeconfig ~/kubeconfigs/test.yaml
```

In live mode the wizard uses the kubeconfig passed to `--kubeconfig`, but still requires an explicit context and separate dry-run/apply actions. Repository scans without runtime Service UIDs can generate YAML but cannot be used as authority to apply to an unrelated cluster.

## Validation

`python3 test/e2e/ambient.py --context kind-mesh1` creates a dedicated namespace on an existing local kind ambient cluster. It proves rejected-parent diagnosis, weighted routing, a real timeout response, L7 method enforcement, ztunnel access control, unhealthy endpoint ejection and, when an agentgateway waypoint class is installed, actual HTTP 429 responses. It applies the wizard's generated plans and uses the wizard's cleanup endpoint, then deletes its namespace and reads the state back. It does not change existing application namespaces.

The expanded live run on 17 October 2026 passed all 29 checks with Istio 1.30.4 and Enterprise agentgateway 2026.9.2. It additionally proved existing-route edits/undo, equal/default weighting, selector remediation, scaling, existing waypoint enrolment and a new waypoint shared by two explicitly selected namespaces. Browser testing covered typeahead selection, map-to-editor navigation, actual editing and undo, endpoint recovery and namespace-level waypoint deployment. Other installed versions must pass their own API server dry-run before a test plan can be applied.

The subsequent UI review checked reset/type-change flows, namespace filtering, back-navigation retaining weights, generated Enterprise YAML and responsive light/dark layouts against captured Bookinfo data. Unit tests cover OSS, Enterprise, mixed and unknown implementation evidence and missing APIs/classes. `FLIGHTBAY_CHECK_AMBIENT_DOCS=1 go test ./internal/ambient -run TestAmbientOfficialLinksLive` checks the official documentation URLs when network access is available.
