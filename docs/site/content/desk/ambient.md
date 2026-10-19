---
title: Istio Ambient
weight: 17
description: Trace Service-to-waypoint attachment, explain controller errors, generate L4/L7 configuration and explicitly apply a test plan to a selected real cluster.
---

## Open an investigation

Open a capture normally, then choose **Istio Ambient** in the sidebar, or open a Service in Resources and choose **Investigate Istio Ambient**.

The entry appears only when the selected scope contains served Istio APIs and ambient evidence: an ambient-labelled Namespace or a ztunnel Pod. Istio shares its CRDs between sidecar and ambient installations; Gateway API CRDs alone do not establish an ambient installation. Missing capture data stays unconfirmed. With **All clusters** selected, one confirmed cluster is sufficient; choosing an individual cluster checks that cluster's evidence. Repository-declared configuration does not establish a running installation.

Use this page for setup, attachments and reviewed changes. [Traffic](/desk/traffic/) shows observed mesh and gateway requests; [Proxy diagnostics](/desk/proxy-configs/) shows delivered configuration and debug output.

The landing page shows **Explore your ambient mesh**, followed by **Needs attention**. Confirmed failed checks are listed across the selected clusters without choosing a resource first. Each row names its cluster and resource, explains the failure and offers **Investigate and fix**. **Findings** includes the same ambient failures and links to their investigation. Missing capture data is not promoted to a confirmed failure.

Failed checks include suggested repairs and verification steps, with a catalogue of 22 common problem types available on the landing page. A missing HTTPRoute parent can be repaired by explicitly choosing a captured Gateway or frontend Service, then reviewing the affected parent entry. Other parents and route rules are preserved.

Live investigation updates and the **Refresh** button retain expanded checks, evidence sections and scroll position for the same target. **All issues** returns from a target to the landing page.

**Inspect this resource** provides **Copy command** and **Run in Flightbay terminal**. Built-in commands use the read-only `cluster@live` or `cluster@snapshot` context. The separately labelled **Your own terminal** command uses the original cluster context and your own kubeconfig.

```bash
flightbay ui ~/Downloads/flightbay-objects.tar.zst
```

Use **Select target** or **Change target** in the compact target bar to open the searchable picker for Namespace, Service, Pod, Gateway, HTTPRoute or ServiceEntry. Type a name, namespace, cluster or kind; arrow keys and Enter work, and the object-type filter narrows the suggestions. The picker closes after selection so the map or form gets the space. Ambient enrolment is not limited to Services: namespaces provide defaults, Pods can have dataplane/waypoint overrides, Gateways provide waypoint infrastructure, and ServiceEntries describe external destinations.

**Clear selection** resets the object type, search value and draft. **New configuration** also switches to Configure and starts at the target step. Neither action applies or undoes cluster changes; any active tests remain available in **Active tests**. Selecting an incompatible object type clears the old target instead of leaving hidden selection state behind.

The target bar shows the resource and context once, with **View object** and **YAML** actions. View object opens the resource details; YAML opens its complete captured, redacted YAML, with Copy YAML available. **Related resources** lists each resource with separate Investigate, View object and YAML actions. Diagnostic fields are not a substitute for the resource itself.

![Open the selected Gateway's full YAML from its resource header](/images/ambient-resource-yaml.png)

The **Investigate** tab adapts to the object type. Service investigations trace Service-addressed traffic. Pod investigations distinguish workload/Pod-IP waypoint handling from Service traffic, and ServiceEntries are not diagnosed using Kubernetes EndpointSlice readiness. Choose a particular HTTPRoute if its intended Service is ambiguous, or leave **Related routes** selected.

![Guided ambient attachment investigation](/images/ambient-investigation.png)

The investigation checks:

- ambient namespace enrolment, pod opt-outs and sidecars;
- Service waypoint overrides, namespace inheritance and the Service's `istio.io/WaypointBound` condition;
- missing waypoints, waypoint traffic types and cross-namespace enrolment;
- Gateway `Accepted` and `Programmed` status;
- route parent kind, API group, namespace and Service port;
- per-parent, per-controller `Accepted` and `ResolvedRefs` conditions;
- stale `observedGeneration` values and missing controller status;
- Gateway listener names, ports, protocols, allowed route kinds, namespace restrictions and hostname intersection;
- backend Service existence, Service ports versus container targetPorts, and cross-namespace ReferenceGrants;
- L7 fields on workload-selector AuthorizationPolicies that ztunnel cannot enforce;
- JWT `requestPrincipals` rules and their captured Service or Gateway RequestAuthentication attachment;
- unsupported ambient `PeerAuthentication` DISABLE settings;
- ready/eligible endpoints and availability of captured waypoint proxy diagnostics.

Every result is **Pass**, **Failed**, **Check**, **Unknown** or **Not applicable**. Click the evidence to inspect the original captured object. Missing, excluded, trimmed or reviewed-out data does not prove that a resource is absent.

The status badges are filters. Choose **Failed**, **Check**, **Unknown**, **Pass** or **Not applicable** to show only those checks; **All** restores the full list. Clicking the active filter also resets it. Counts remain totals for the selected resource, and a zero-count filter shows an explicit empty state. Object/YAML access and related resources remain available while filtering checks.

![Pass filter showing only passing checks while retaining resource access](/images/ambient-status-filter.png)

Failures open first; other checks are compact, expandable rows. Raw fields used by a check are inside **Check details · captured fields**. Each step includes labelled **Official docs** links to the relevant upstream or Enterprise guide. A stale-status warning appears only when the observed generation is actually stale or missing.

**Official troubleshooting commands** beside a failed check provides copyable commands using the original cluster context and captured resource names. It maps `istioctl analyze` to resource/controller conditions, `ztunnel-config service` and `workload` to ztunnel configuration, `proxy-status` to istiod sync state, `proxy-config all` to the waypoint dump, and `kubectl logs deploy/<waypoint>` to Log explorer. Commands requiring a Pod or Deployment are omitted when that identity was not captured. They run in your own terminal, not against the snapshot API.

The separately labelled `istioctl pc log --level debug` command changes live Envoy logging. Flightbay provides it for copying and does not execute it. Record and restore the previous level after investigating. Agentgateway waypoints do not receive Envoy-specific commands.

## Detected implementation and official documentation

Open **Implementation details** in the target bar to see the selected waypoint/controller and detection evidence. In Configure, **Generated policy type** identifies the kind a policy template will produce, not the kind of the resource currently selected. The details include captured GatewayClasses, deployed Gateways, discovered policy APIs and recognised Istio control-plane/ztunnel images. Missing evidence stays unknown. A cluster can contain OSS and Enterprise components together; it is not assigned one blanket API shape.

For agentgateway tasks, the selected controller determines `AgentgatewayPolicy` versus `EnterpriseAgentgatewayPolicy`, and the discovered API supplies the version. Installing both CRDs does not mean both controllers are active. A new waypoint can only use a supported class actually observed in the selected cluster. Refresh or collect the missing GatewayClasses/Gateways if no class is offered.

Enterprise agentgateway classes require the corresponding Enterprise policy API. Enterprise Istio templates also require its detected CRD evidence, including the Segment API, alongside the captured Istio installation. Images or class names alone do not enable Enterprise YAML.

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

## Deploy the packaged Bookinfo demo

Choose **Deploy Bookinfo demo** in the Ambient header, or open `#ambient?tab=demo`. **Create a new namespace** accepts `bookinfo` or another fresh application namespace such as `bookinfo-demo`. The first confirmation creates it in each selected cluster. System namespaces are rejected, and existing namespaces/resources are not adopted or overwritten.

**Create new waypoints** explicitly chooses **one new waypoint per namespace** or **one new waypoint per main Service**. The first creates one new Gateway and then reviews setting it as the namespace default. The second creates four new Gateways and explicitly binds productpage, details, ratings and reviews, without a namespace default. Version-only reviews-v1/v2 backends bypass additional waypoint selection. Enter the shared name or dedicated name prefix; the preview lists all resulting names. Each cluster's **New waypoint type (GatewayClass)** dropdown selects the implementation, not an existing waypoint. Scope/name choices must match namespace preparation; choose a fresh namespace for a different prepared scope.

The installer has two stages because Kubernetes cannot server-dry-run a Deployment in a namespace that does not yet exist:

1. **Review installation**, inspect the namespace previews, then choose **Review and apply…**. Flightbay runs the server dry-runs and opens the final confirmation to create only the namespaces. **Dry-run only** is available for checks without applying.
2. The installer automatically prepares the separate workload review once the namespace appears in the live capture. Inspect the full per-cluster YAML and choose **Review and apply…** again for the separate workload confirmation. **Continue to Bookinfo workloads** remains available if the capture takes longer to catch up.

Each confirmation names the real kubeconfig context and exact YAML. Cluster identity is checked against the captured `kube-system` Namespace UID. Waypoint classes are checked live before applying. Resources are created with ownership records rather than force-applied over existing objects.

Bookinfo's four main Services are productpage, details, ratings and reviews. Reviews has v1/v2 backends and a healthy Service-parent HTTPRoute on port 9080 with an equal split. The selected controller creates the waypoint; Istio uses HBONE 15008, while agentgateway uses its HTTP mesh declaration on 15088. Optional demo traffic calls Productpage every five seconds. Optional global publication labels the four main Services in each installed cluster; existing peering and captured Enterprise Istio support are required. The installer does not establish peering or guarantee cross-cluster traffic from dry-run alone.

The YAML files ship inside Flightbay and can be downloaded under **Bundled YAML and image sources**. Application images are pinned `docker.io/istio/examples-bookinfo-*:1.20.2`; the optional client uses `docker.io/curlimages/curl:8.12.1`. Kubernetes pulls those public Docker Hub images. Waypoint images come from the installed Gateway controller. No public ingress or cloud LoadBalancer is created.

During and after Apply, each cluster automatically reports recorded resources and live readiness. Namespace preparation is labelled separately from deploying Bookinfo. Deployment readiness requires the current rollout and all requested replicas; Gateway and route conditions must match their current generation. Scheduling blockers are shown when Kubernetes reports them. New waypoints must report Programmed before namespace binding changes are applied. Reopening the page recovers recorded installations and resumes checks; the last selected application namespace is remembered in this browser. Automatic checks pause after ten minutes, with **Check deployment readiness** available to resume. Readiness does not establish request connectivity. Use **Open demo request path** to inspect Productpage to Reviews.

**Remove Bookinfo…** appears on clusters with installer-owned Bookinfo resources, including after reopening the page or connecting from another laptop. Bob can install the demo and Sarah can remove it without copying Bob's local history. The preview uses live namespace ownership labels and resource UIDs, with local records available for older installations. It lists the Bookinfo resources and demo namespace and requires confirmation. Before removal, the server checks cluster and namespace identities, a fixed Bookinfo kind/name allowlist, live ownership labels and waypoint consumers. A replaced namespace or resource invalidates the preview. Removal is refused if a demo waypoint also serves an application in another namespace.

Confirmed Bookinfo teardown removes the verified demo resources, then deletes the namespace Flightbay created for the demo, including policies or global-split resources added inside it while testing. A namespace name alone never establishes ownership. Customer namespaces, mesh infrastructure outside the demo namespace and other clusters are retained. Generic configuration cleanup never deletes namespaces; this exception belongs only to the separately confirmed Bookinfo teardown.

Both loading the removal preview and confirmed removal show a spinner and elapsed time. Removal waits for Kubernetes to confirm that the resources and demo namespace are absent before reporting completion. The final result remains visible in the dialog and on the page. A failed or interrupted request reports that removal could not be confirmed, since some resources may already have been removed; preview again before retrying. Closing the dialog does not cancel an in-flight removal.

## Request path

Open **Request path** beside Investigate and Attachment map. **From application** is the application sending the request; **To Service** is what it should reach. Each is a searchable dropdown with its cluster shown. The source dropdown initially lists applications, with Pods and service accounts available inside it. Destination search also matches captured local/global DNS names. **Address to use** offers known cluster/mesh DNS names or **Enter another hostname**. The port is filled from the selected Service; change it when more than one is exposed. HTTP method and path are under **Optional request details**. **Trace request path** is a read-only reconstruction; it sends no request and changes no cluster resources.

The numbered **Dataplane hops** list contains caller Pods, source ztunnel, selected waypoint, cross-cluster transport, destination ztunnel and backend Pods where the capture establishes them. DNS/address records, route attachments, peer objects and unused waypoint bindings stay in **Address & routing** rather than appearing as extra network hops. When a captured source-global waypoint handles a remote request, the path shows that source waypoint rather than adding a second destination-waypoint traversal. Click a hop for its captured resource YAML and diagnostics. **Configured**, **Needs attention**, **Not established** and **Not selected** describe configuration evidence, not proof of traversal.

Switch between **Steps** and **Map** above the result. Steps keeps the detailed ordered list; Map draws connected nodes with the same states and evidence selection. East-west gateways use amber transport styling in both themes, distinct from waypoints, with separate failure and unknown markers. Links distinguish HBONE, TCP, interception and unestablished transport. The map supports pan, zoom, Fit and full screen like Traffic. Dashed links show configured relationships, not observed traversal. Node labels are kept inside their boxes; full names remain in the tooltip and evidence panel.

The adjacent **Configuration**, **Traffic** and **Policies** tabs keep different evidence separate. Traffic records are matched to captured caller identities/addresses, destination and available request fields. L4 records cannot establish HTTP method or path filters, and DNS query records are not application traffic. Record counts are not deduplicated end-to-end request counts. Policy analysis is shown per caller Pod for same-cluster paths; cross-cluster policy decisions and full HTTP route matching are not inferred. A global hostname can select multiple clusters, so choosing a destination cluster investigates a scenario rather than predicting endpoint selection.

Related failed checks link back to their investigation and suggested repair. **Trace request path** also appears on Service investigations, attachment-map cards, Service Findings and supported Traffic connection panels, prefilled with available endpoints. **Copy path link** retains endpoint/request choices; opening it does not automatically run an analysis. Refresh or trace again when the capture changes. The page deliberately does not auto-refresh an open result while you read it.

Both themes use high-contrast text, borders and labelled states. Narrow layouts stack the hop list and evidence panel instead of squeezing them into pale, unreadable cards.

## Attachment map and existing configuration

The **Attachment map** tab shows namespace defaults, selected waypoints, routes, backend Services, policies and selected workloads. Cards open the captured objects. Backend weights and calculated percentages are shown per route rule; a 90/10 split on one rule is not added to weights on another rule. This is a configuration relationship diagram, not observed traffic.

The map opens a **cluster canvas** with a labelled box for every context in the capture, including contexts with no matching Services. The header distinguishes the kubeconfig context from the captured mesh cluster/network identity. A selected object's namespace limits the Service cards by default; **All namespaces** expands the scope. The map can also be opened without selecting an object to inspect the whole capture. The canvas includes all captured contexts even when the workspace filter selects one cluster.

Every cluster header uses the Kubernetes icon and has **Expand / Compact** controls: Expand increases the box width and gives the card view another column. **Cards / List** switches that cluster independently between a card grid and compact resource rows. **Full screen** expands the canvas; Escape or Exit full screen returns to the page.

Drag a cluster header to move its whole box, or a resource handle to move the resource inside its cluster. Focus a handle and use arrow keys for keyboard movement; Shift increases the step. Drag empty canvas space to pan, scroll to explore, or use **− / +**, Ctrl/Command plus mouse wheel, **Fit**, and **Reset layout**. Layout changes stay in the current browser session and do not modify Kubernetes resources. They survive navigation within the desk, but not a page reload.

All captured Services in the scope are included, including gateway infrastructure. Cards show their context, namespace, endpoint counts and waypoint selection. Global Services appear first after the gateways. Use the normal **top search / ⌘K** to find a resource: selecting a supported Ambient resource while the map is open focuses it on the map instead of opening a separate drawer. There is no separate Service-name filter in the canvas. **Connections** opens individual Service relationships below the canvas; **Configure** starts its wizard, and **YAML** opens the resource directly.

Captured `istio-eastwest` and `istio-remote` Gateways have distinct transport/peer cards with listener ports, addresses and current Programmed status. East-west cards use amber styling in both themes. Remote mesh identities resolve to context names only when the captured identity uniquely matches. Teal directed lines connect a remote-peer Gateway to the destination east-west Gateway when the advertised addresses match. An unresolved peer stays labelled as such.

Purple dashed lines connect global Services using captured publication evidence: reciprocal peer configuration, the same generated ServiceEntry hostname, explicit parent-Service labels, and remote workload identities matching the captured trust domains and namespaces. Export labels or identical Service names alone do not create a line. Include the generated ServiceEntries and the `istio-system/istio` mesh ConfigMap in the capture; missing identity evidence is not guessed from a context name.

Click a connection chip to highlight its endpoints and frame the two cluster boxes. Lines use rounded bends and choose clear side, top or bottom ports rather than squeezing arrowheads into a narrow gap. Arrowheads have an explicit size independent of highlighted line thickness, and stop short of the destination border. Lines reroute around resource cards when boxes or cards move; if an overlapping layout blocks a clear route, the map reports it instead of drawing through a resource. Connection colours identify configuration relationships, not measured request traffic.

![East-west peer connections and a global Service published by mesh1 and mesh2](/images/ambient-cluster-connections.png)

The runnable fixture is `test/e2e/multicluster-demo.yaml`, with setup notes in `test/e2e/README-multicluster.md`. `bash test/e2e/multicluster-check.sh` temporarily stops only the fixture's local server, proves remote responses in both directions, and restores the exact initial replica counts. It passed on mesh1 and mesh2 with Solo Istio 1.30.4 on 17 October 2026. Bookinfo is outside that fixture namespace.

Namespace setup in the Configure tab also retains the compact Service overview with endpoint counts.

### Service DNS names

Service cards show both **Cluster DNS** and **Global mesh DNS**. The target bar's **DNS names** menu exposes the full names with Copy actions. The cluster suffix comes from captured istiod `--domain` arguments or a captured CoreDNS Kubernetes zone. If neither is available, `cluster.local` is explicitly marked as an unconfirmed default; conflicting captured suffixes are not guessed. Trust domains are not used as DNS suffixes.

Global names come from controller-generated ServiceEntries whose labels identify the selected Service. A global label alone does not invent a hostname. This demo publishes `global-echo.flightbay-global-demo.mesh.internal`; another installation might publish `mesh.local`, a Segment domain or custom aliases. Displaying a configured name does not prove it resolves from the workstation.

### Publish an existing Service globally

Configure plans reconcile resources already created by Flightbay: missing resources are created, changed fields appear as updates, and matching settings produce no changes. Ownership and resource identities are checked before applying. This uses the captured cluster state rather than local installer history. Use the existing configuration's editor to change it; adding a separate policy remains a separate configuration.

The global weighted-split editor loads the current cluster weights. Reapplying the same weights is a no-op; changing them patches the existing HTTPRoute while retaining its UID, ownership, matches and other unedited settings. Switching to a non-weighted distribution removes the owned split route and helper Service in the reviewed plan.

In **Configure**, choose **Publish a global Service**. The generator reviews the metadata label `solo.io/service-scope: global` on the existing Service. It preserves other labels, selectors, ports and service-takeover settings, and does not create an application or edit generated ServiceEntries. Legacy `global-only` publication is preserved.

This template requires captured Solo Enterprise Istio evidence, the ServiceEntry API and an ambient-enrolled namespace with a selector-based application Service. An Enterprise-level Istio licence and working peering are runtime prerequisites. API-server dry-run validates the patch, not end-to-end publication or connectivity.

**Clusters contributing local endpoints** lists the captured contexts for the same namespace and Service name. Select the existing instances that should contribute endpoints. An already-global instance needs no label change. A remote cluster can consume the published global name without running its own copy of the application; peering, generated DNS and access policies still need to permit that path.

For a cluster without a captured local instance, **Set up Service participation** explains both cases and provides a read-only command for that exact context, namespace and Service. Check live state before assuming the application is absent. To contribute local endpoints, deploy the application through its normal manifests, Helm release or GitOps pipeline, then refresh the capture and select that instance. Copying a Service alone does not deploy its workloads. This guidance applies to any application; the separate Bookinfo installer is a demo, not the generic missing-Service setup mechanism.

The review gives each cluster its own YAML, real kubeconfig context, dry-run and apply result. A live context is reused only when its name and API server match the captured live connection. **Review and apply** runs the required dry-runs, then opens a confirmation containing every selected context, API server and YAML; Cancel writes nothing. **Dry-run only** validates without applying. You can also operate on a single row. Each cluster is applied separately: a later failure stops the batch, leaves earlier successful changes in place and records them under **Active tests** for undo. There is no cross-cluster transaction.

**Stop publishing from every cluster** reviews the whole global configuration for the selected Service. In each selected cluster it removes the publication label, takeover label and traffic-distribution annotation, and deletes that Service's Flightbay-owned weighted-split HTTPRoute and per-cluster Service. This works with any customer Service and namespace, not just Bookinfo. The application Service, workloads and namespace are retained. Namespace deletion is reserved for the separately confirmed Bookinfo demo teardown. Already-local Services with leftover global routing offer **Remove leftover global routing**. Deletions appear explicitly in the review, are dry-run against the API server, and check ownership, UID and configuration before applying. Undo recreates deleted resources only if their names are free.

An export label is a publication request, not proof that the global Service works. The cluster rows distinguish **export label set**, **alias evidence missing**, **alias addresses missing**, and **alias addresses observed · traffic unverified**. This uses captured generated ServiceEntry address evidence; it does not assert successful proxy delivery or failover. Refresh after reconciliation, then test the actual global hostname. A local-preference policy can keep all healthy traffic in the source cluster. HTTPRoute version weights and cross-cluster locality are separate controls.

If an alias exists in Kubernetes but requests fail, compare the generated resources with delivered proxy configuration and istiod's registry. Repeated peering-controller `already exists` errors can leave generated resources absent from the active registry. Do not edit generated ServiceEntries or automatically restart a customer's control plane. Preserve the evidence and have the operator perform the appropriate controller recovery, then repeat the same request. A stopped partial run is not a completed batch; HTTP 2xx from an identified remote Pod is evidence for those recorded responses only.

![Independent publication plans with explicit kubeconfig contexts and successful dry-runs](/images/ambient-global-review.png)

For metadata-only changes, the downloaded YAML is a **scoped merge snippet for an existing resource**. Use Flightbay's reviewed dry-run/apply/undo flow, or `kubectl patch service NAME -n NAMESPACE --context CONTEXT --type merge --patch-file FILE.yaml`. Weighted-split cleanup includes commented `DELETE` entries for review; use Flightbay's apply flow to execute those deletions. Refresh afterwards to see the actual global DNS names.

### Test a Service with HTTP header profiles

Use **Test service** in the target bar or **Test** on a Service card. The modal supports saved, named profiles containing the HTTP method, path/query, headers, optional body and per-request timeout. Header values such as Authorization and cookies use masked inputs and are redacted from returned results. Profiles are stored locally in owner-only files beneath Flightbay's Ambient state directory, not in captured Kubernetes resources or exported bundles.

The **Request** view groups the traffic origin/connection, method/path/port and request count/timeout into aligned rows. Saved profiles, headers/body and connection details expand when needed. Starting a test opens **Results** automatically. The close control and Run/Stop actions stay visible while the content scrolls. You can switch back to Request without losing responses; inputs are locked while a run is active. **Edit request** returns to the setup, and **Run again** uses the current request settings. Results keep configured routes, response samples and measurement details collapsed below the traffic graph.

**Responder** means the existing application Pod that answered, not a helper deployed by the tester. Native modes generate HTTP inside Flightbay itself. They support batches and the same streaming Results, Stop and Run again controls without creating Pods or Deployments. A reachable endpoint needs no Kubernetes exec permission; optional cluster metadata reads enrich attribution but are not required to send native HTTP. Backend/gateway tunnelling needs access to port-forward the selected existing resource. The separate existing-Pod mode requires `pods/exec` on a selected Ready curl container and uses in-cluster DNS and caller identity. Neither path deploys a responder, client Pod, route or Service.

**Identify existing application responders using read-only cluster metadata** controls native attribution. Reachable-endpoint mode starts with it off, so sending HTTP makes no cluster connection. Enable it to read existing Pod inventories through verified cluster connections; the metadata lookup has a fifteen-second limit. When no kubeconfig contexts can be read, the dialog opens in reachable-endpoint mode and attribution remains unavailable. Successful responses without verified identities still appear in Results.

The connection determines what is tested. When a Ready existing curl client is captured, the dialog starts in in-mesh mode and prefers an ambient-enrolled client in the Service's namespace. The target cluster's live kubeconfig connection is reused automatically.

| Mode | Connection and meaning |
|---|---|
| In-mesh · existing client | Runs curl in an existing captured Ready client Pod, using Service DNS and the normal mesh/waypoint path. Choose 1–1,000 requests total across selected names. Requests are sequential and each uses a new connection. |
| Flightbay · reachable endpoint | Flightbay's native HTTP client requests an HTTP(S) origin directly. Set path and query in the profile. Workstation DNS and connectivity must reach it. |
| Flightbay · backend-only port-forward | Flightbay forwards to a Ready existing Pod selected by the Service and generates the batch natively. Selected local/global DNS names are HTTP authorities on the same backend connection, not DNS or mesh-routing tests. |
| Flightbay · existing gateway port-forward | Flightbay sends native HTTP through a captured HTTP ingress gateway's existing Pod. Its matching route must target the global Service to exercise east-west routing. Missing captured route references are reported in the result. |

No test Pods, routes or Services are deployed. Port-forwarding needs the selected context's `pods/portforward` permission and a live Service UID matching the capture. The port-forward closes after the requests. Redirects are returned rather than followed; response bodies are capped at 32 KiB. Methods other than GET/HEAD have the application's normal HTTP semantics.

The tester has separate **Request** and **Results** tabs. Native and existing-Pod runs switch to Results and stream the completed count, HTTP errors, response-share graph and backend distribution. **Stop test** retains completed responses; **Run again** starts another run. Native modes start with one request when first selected; increase the visible count for a batch, up to 1,000 total requests.

Results lead with **request origin → identified application responders**, and show the actual destination and HTTP authority sent. **Responding clusters** has a cluster-level count; the graph and backend breakdown group by **cluster plus Service/version**, so the same version on mesh1 and mesh2 is not merged. Response samples include the responder cluster and open that Pod's logs. Responder names come from response JSON `podname` or `X-Pod-Name`, correlated with live Pod inventories read at test start. Additional clusters are queried only through matching recorded live connections and checked against their captured kube-system Namespace UIDs. Duplicate names across clusters, missing identities or incomplete global attribution stay unverified. This is application-identity correlation, not a packet trace. A native request can succeed without exposing a responder identity; successful HTTP is not automatically attributed to the selected cluster.

**Configured routes** lists cluster-scoped route rules and weights read at test start, with an overlap notice when several Service-parent routes apply. The graph measures observed responses, not which route won precedence. If no application responder is identified, it shows an explanatory empty state rather than a misleading 100% backend-share line. When comparing local and global paths, check both the selected DNS name and the actual HTTP authority: `cluster.local` addresses the local Service, while a generated global alias is a different destination. Explicit Host headers can select a different L7 route and remain supported for deliberate virtual-host tests.

In-mesh testing requires `pods/exec` on the explicitly selected existing curl client. A live desk recognises its original context only when both context name and API server match; other captures require an explicit context choice. The client and Service UIDs are checked against live objects before testing. Bookinfo demo Reviews defaults to `/reviews/0`, not `/`.

A profile's explicit `Host` header overrides the selected HTTP authority; results show the authority actually sent. Batches are limited to eight names. Workstation and port-forward batches have a two-minute overall timeout; in-mesh batches have a three-minute overall timeout and at most 1,000 requests.

Each result shows status, latency, connection address, HTTP authority, destination Pod where known, response headers/body and an `X-Flightbay-Test-ID`. **Logs around this request** opens the selected cluster, destination namespace/Pod and the test's time window. Exact ID correlation requires the application or gateway to log that header; a snapshot without those logs cannot show a new request. HTTP 200 through a backend port-forward is not proof of working global DNS or east-west routing.

Use **Back to HTTP test** in Log explorer, or the browser's Back button, to return to the original map or wizard with its draft, unsaved header profile, results and position retained. Returning does not resend the requests. Closing the restored modal leaves it closed during normal live refreshes.

![HTTP results distinguish authorities from the actual port-forward connection and link to logs](/images/ambient-http-results.png)

![Service attachment map with routes, policies and backend weights](/images/ambient-map.png)

The map and its companion table have **Edit in wizard** actions for supported existing HTTPRoute rules, DestinationRule settings, single-operation method policies and local request-rate buckets. The wizard loads the actual captured values. Route edits preserve other rules, matches, filters, annotations and attachment references. Unsupported shapes remain evidence-only, with an explanation; they are not flattened into a different configuration.

**Service → Configure** starts with **Current configuration** cards showing the original resources, individual route rules, configured weights, matches and policy settings. Choose the relevant Edit action to load those captured fields and review an update to that resource. If several routes or policies apply, choose the intended resource explicitly. **Add another capability** is collapsed when existing resources are present; capabilities already installed are edited from their cards rather than creating competing resources. Unsupported shapes explain why they cannot be edited. After Apply, the view refreshes its captured configuration and offers **Retest traffic**; it reports when the live capture has not caught up yet.

Every Configure workflow has the same breadcrumb in its options and Review screens: **All configurations → target → resource/rule or task → Configure/Review**. **All configurations** and the **Configurations** progress step return to the selected target's full list. **Switch configuration** lists captured resources and individual route rules, with backend weights or policy settings, so you can move directly between split rules. Rule drafts are retained separately while navigating within the same target; reopening a rule resumes its draft. **Resume configuration draft** restores a new task, waypoint setup or repair after returning to the list. Switching targets, starting a new configuration or refreshing inventory clears saved drafts. Returning to the list or switching rules clears any generated preview and validation; review and validate the chosen draft again before applying it.

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

Each unavailable task explains its actual blocker and the next step: a different waypoint implementation, a missing captured API, insufficient region evidence, a caller-identity limitation, or a generator not yet implemented in Flightbay. An unavailable template is not a claim that the product lacks the feature. Installing more CRDs alone does not make an Istio/Envoy template work with agentgateway, and installing a global rate-limit service alone does not add a Flightbay generator.

The Configure tab follows **Target → Configurations → Configure → Review**. Supported tasks appear as short cards with official documentation links. Unavailable tasks are kept under **Other tasks · why unavailable?** rather than filling the main screen. Back preserves your choices. The review opens on a concise change summary; **Generated YAML** is a separate tab. Cluster testing is an optional expandable section after review.

The selected task names the options panel, with a separate **This change** summary explaining its scope and next step. For mutual TLS, choose **Require mesh mTLS** or **Allow non-mesh callers too**; the generated values remain STRICT and PERMISSIVE. Workload-wide L4 tasks do not show a misleading Service-port selector. The compact target bar, stronger selected states and dark panel headings in light mode keep the active form distinct from surrounding context.

![Compact mutual TLS configuration with explicit choices and change scope](/images/ambient-options-compact.png)

![Supported task cards with contextual official documentation](/images/ambient-tasks.png)

| Task | Enforcement/configuration |
|---|---|
| Deny by default | ztunnel workload-selector AuthorizationPolicy |
| Allow a source namespace | ztunnel AuthorizationPolicy for a direct ambient path without a selected waypoint |
| Require the selected waypoint | ztunnel workload-selector ALLOW permitting only the captured waypoint service account principal |
| Require mutual TLS | Workload-selector STRICT PeerAuthentication |
| Allow HTTP methods | Service-targeted AuthorizationPolicy on an Istio waypoint |
| Require a JWT | Service-targeted RequestAuthentication plus AuthorizationPolicy on an Istio waypoint |
| Balance between Services | Service-parent HTTPRoute with explicit backend weights |
| Request timeout | HTTPRoute request timeout |
| Retry failed requests | HTTPRoute `retry.attempts`, only with an Istio waypoint and the field present in the captured installed CRD schema |
| Mirror requests | HTTPRoute RequestMirror to another captured Service, on an Istio waypoint |
| Route by HTTP header | Exact HTTPRoute header matches, on an Istio or agentgateway waypoint with captured schema evidence |
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
- Require waypoint reads the waypoint's actual Pod service account and the mesh trust domain from captured mesh configuration or identities. Missing or conflicting identity evidence keeps the task unavailable. The policy is workload-wide and other ALLOW policies remain additive.
- JWT configuration takes an issuer, either an HTTPS JWKS URI or inline public JWKS, and explicit `issuer/subject` or `issuer/*` request principals. Optional paths, methods and exact header values narrow the ALLOW rule. RequestAuthentication validates presented tokens; the accompanying authorization requires a matching principal. A snapshot cannot verify a request's signature, claims or JWKS retrieval.
- Header routing takes an exact header name/value JSON object and a captured backend Service port. All supplied headers must match. A catch-all rule retains the selected Service's original backend for non-matching requests. Mirroring copies requests to another captured Service and ignores its response.

New captures retain the HTTPRoute CRD schema so these tasks can distinguish installed fields from API discovery alone. Older captures without that schema leave schema-dependent tasks unavailable. Retries remain unavailable on the tested kind clusters because their installed HTTPRoute schemas omit `retry`. Agentgateway JWT, mirroring and retry mappings are marked **UNVERIFIED** and unavailable until the generator and waypoint behaviour are verified; installing an Enterprise CRD does not enable an unverified mapping.

Traffic's **To allow it → Review in Ambient wizard** opens the same Review step with a server-regenerated repair, not arbitrary pasted YAML. Generated ztunnel repairs include numeric destination workload ports; waypoint repairs include Service ports and paths. **Edit options** can narrow the repair to captured ports and selected waypoint paths. ztunnel path restrictions are rejected. The normal explicit-context dry-run, Apply confirmation and undo controls follow.

These controls have different semantics:

- **ztunnel is L4.** It cannot enforce HTTP methods, paths or request-rate limits. A selector policy affects the selected Pods, including other Services that select those Pods.
- **Waypoint L7 controls need traffic to traverse the waypoint.** For example, allowing an original caller namespace at ztunnel is not the same as allowing it at a waypoint: the destination-side ztunnel may see the waypoint's identity.
- **Local rate limits are per proxy.** They are not a shared quota across replicas. Stock Istio waypoints do not have a built-in rate-limit template in this wizard.
- **Outlier detection ejects endpoints.** It does not establish primary/backup priority between different Service names. A zero-weight HTTPRoute backend is not a standby.
- **Existing policies and routes matter.** ALLOW policies are additive, existing routes may take precedence, and another DestinationRule can conflict with the generated one.

Primary/backup service failover and shared/global rate limiting are shown as unavailable until a supported integration and its required configuration are provided. No EnvoyFilter workaround is generated for waypoints.

## Apply

Generation and investigation are offline. Test actions use a **separate real kubeconfig connection**, not the read-only snapshot API or live mirror transport.

1. Review the exact generated YAML and its impact notes.
2. In a live desk, Flightbay reuses the original context for the target cluster and shows its API server address. You do not select the same mesh again. This is shared by all configuration tasks, per-cluster global publication, Scorecard fixes, and Bookinfo deployment/removal. HTTP tests also start with the target cluster's live connection. For an offline capture or a context whose API server has changed, choose a real context manually. Flightbay does not fall back to kubeconfig's unrelated current context or the read-only mirror.
3. Choose **Review and apply…**. Flightbay automatically runs any required server dry-run, checking that the Service UID, selector, ports and waypoint configuration still match the runtime capture. It also checks installed API schemas, admission and create permissions. Failed validation stops before confirmation or writes. **Dry-run only** remains available for checks without applying. A changed preview must be regenerated.
4. Inspect the confirmation's exact context, API server and complete YAML, then choose its red **Apply** button to write the change, or **Cancel** to leave the cluster unchanged. Only that validated plan can be applied. Current validations are reused; expired validations are refreshed before confirmation. New resources are created without overwriting existing objects; existing edits update only their reviewed fields with UID/resourceVersion checks.
5. Use **Refresh attachment status** and exercise traffic separately. A successful create does not mean a route was attached or the intended traffic behaviour occurred.
6. Choose **Undo changes and remove test resources** (or **Remove these test resources** for a create-only plan). Existing fields are restored only while they still equal this test's admitted values. Unrelated fields are preserved. Created objects are deleted only with matching recorded UIDs and ownership, and absence is verified. A replacement object is not deleted.

Generated traffic-policy resources have `flightbay-test-…` names; new waypoint names are explicitly chosen. Created resources carry ownership labels. If applying a multi-resource plan fails, Flightbay attempts to undo existing-field changes and remove created resources. Bindings are restored before a created waypoint is removed; an undo conflict retains the Gateway and reports the remaining work rather than disrupting a binding it cannot restore.

Dry-run results have explicit **Dry-run passed** or **Dry-run failed** labels and success/failure icons as well as colour. A passing dry-run means the API server accepted the reviewed requests without saving them. It is not approval, evidence of working traffic or a replacement for your deployment/GitOps process.

![Apply confirmation showing the real context, API server, reviewed YAML and Cancel action](/images/ambient-apply-confirmation.png)

Test records are stored with private file permissions under `~/.flightbay/ambient-tests` (or `$FLIGHTBAY_HOME/ambient-tests`). **Active test resources** can therefore be inspected and cleaned up after reopening a capture or restarting the desk. Applying a new plan still requires validation in the new session. Cluster credentials are not stored in the records.

For a non-default kubeconfig when opening a file:

```bash
flightbay ui capture.tar.zst --test-kubeconfig ~/kubeconfigs/test.yaml
```

In live mode the wizard uses the kubeconfig passed to `--kubeconfig`, but still requires an explicit context and separate dry-run/apply actions. Repository scans without runtime Service UIDs can generate YAML but cannot be used as authority to apply to an unrelated cluster.

## Validation

`python3 test/e2e/ambient.py --context kind-mesh1` creates a dedicated namespace on an existing local kind ambient cluster. It proves rejected-parent diagnosis, weighted routing, a real timeout response, L7 method enforcement, ztunnel access control, unhealthy endpoint ejection and, when an agentgateway waypoint class is installed, actual HTTP 429 responses. It applies the wizard's generated plans and uses the wizard's cleanup endpoint, then deletes its namespace and reads the state back. It does not change existing application namespaces.

The P1 run on 18 October 2026 passed 45 checks on `kind-mesh1`. It additionally proved waypoint-only access with a blocked direct path, explainer repair apply/undo with restricted paths, exact header routing with a default backend on Istio and Enterprise agentgateway waypoints, request mirroring observed in backend logs, and JWT enforcement for valid, missing and invalid tokens plus path, method and header restrictions. It checked copyable runbook commands and refusal of retries absent from the installed schema. Experimental retries and agentgateway JWT/mirroring/retry behaviour were not exercised and remain unverified on these clusters.

A subsequent run on 18 October passed all 45 checks on **both `kind-mesh1` and `kind-mesh2`**, including existing-route updates and undo. The mirror proof now sends traffic while waiting for backend-log evidence, since a successful primary response can arrive before the new mirror configuration reaches the waypoint. Both runs removed their fixture namespaces and verified their absence. Browser checks covered Service Configure's existing-resource picker, captured weights, an update-only preview, separate creation, JWT, mirroring, waypoint-only access and agentgateway header-routing forms. Read-only runbook commands were executed against live ztunnel and Istio waypoint Pods; the live logging-level change was not executed.

The expanded live run on 17 October 2026 passed all 29 checks with Istio 1.30.4 and Enterprise agentgateway 2026.9.2. It additionally proved existing-route edits/undo, equal/default weighting, selector remediation, scaling, existing waypoint enrolment and a new waypoint shared by two explicitly selected namespaces. Browser testing covered typeahead selection, map-to-editor navigation, actual editing and undo, endpoint recovery and namespace-level waypoint deployment. Other installed versions must pass their own API server dry-run before a test plan can be applied.

The subsequent UI review checked reset/type-change flows, namespace filtering, back-navigation retaining weights, generated Enterprise YAML and responsive light/dark layouts against captured Bookinfo data. Unit tests cover OSS, Enterprise, mixed and unknown implementation evidence and missing APIs/classes. `FLIGHTBAY_CHECK_AMBIENT_DOCS=1 go test ./internal/ambient -run TestAmbientOfficialLinksLive` checks the official documentation URLs when network access is available.
