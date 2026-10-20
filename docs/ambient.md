# Istio ambient

The **Ambient** page investigates and configures Istio ambient from a capture or a live cluster. It appears when the captured Istio APIs and evidence of an ambient installation are in the selected cluster scope.

![Ambient attachment map](images/ambient-map.png)

## What you can do

- **Investigate.** Search Namespace, Service, Pod, Gateway, HTTPRoute and ServiceEntry entry points, and follow failed checks into an evidence-aware recovery wizard.
- **Change.** Edit captured routing weights and policies, or set up shared waypoints.
- **Trace a request path.** Choose a caller and destination Service across captured clusters. Steps and Map show reconstructed dataplane hops, with amber east-west gateways; address/routing configuration, traffic and policy evidence remain inspectable separately.
- **Deploy the Bookinfo demo.** Review namespace preparation and workloads separately, follow readiness, recover recorded installations and remove only installer-owned resources after confirmation.
- **Apply safely.** Preview the YAML and field diffs, dry-run, then confirm Apply to an explicitly selected real context. Scoped test changes can be undone.

Configuration runs **Target → Configurations → Configure → Review**. Current configuration cards load actual resource/rule values. Shared breadcrumbs and a rule switcher retain separate drafts within the selected target; switching configuration requires a fresh review and validation. Detected controller and API evidence selects the matching OSS or Enterprise policy shape, and labelled **Official docs** links accompany tasks and debugging steps.

Configure preserves page position and open sections on same-step refreshes. The live banner's **Pause ingestion** freezes incoming workspace data across connected clusters; **Resume ingestion** catches up watches. Log buffering remains bounded during a pause.

**Set circuit breakers** configures connection-pool limits and can isolate an API's backend cluster. Solo Enterprise Istio waypoints also support route-scoped local rate limits and retries through generated EnvoyFilters. Global limits connect to an existing rate-limit service backed by shared Redis; ambient setup owns Redis, the limiter and Redis authentication. Flightbay does not deploy them. See [Ambient resilience controls](site/content/desk/ambient-resilience.md) for the exact scope and request-chain limitations.

All shared Configure tasks offer cluster selection when matching targets are captured in several contexts. Flightbay regenerates each cluster's plan and uses that cluster's resource identity. An existing policy is updated where present; a missing counterpart is created. The review lists independent dry-run and apply results, with undo per cluster.

![The configuration wizard](images/ambient-wizard.png)

## The attachment map

Namespace maps show every captured Service and its endpoint counts. The map groups Services inside draggable, expandable cluster-context boxes with Kubernetes icons and independent card and list views. Captured east-west peering and global-Service publications have evidence-backed connection lines. Zoom, pan, fit, reset or go full screen; the top search focuses resources on the map, and **View object** and **View YAML** open the actual resources. Multicluster bundles show every captured context without assuming that the clusters are peered.

## Global services

Service cards show cluster and published global DNS names. Configure generates independent global-publication label patches for selected clusters where the matching Service already exists, with separate contexts, dry-runs and status. Apply requires confirmation of the actual contexts, API servers and YAML, and successful changes can be undone.

Publication distinguishes the export label from generated alias/address evidence and successful traffic. A remote cluster can consume a global Service without deploying its own application; contributing endpoints requires the application and Service there. Version weights and cross-cluster locality are separate settings.

## Testing a service

**Test service** runs saved HTTP-header profiles from Flightbay's native client, directly or through a temporary port-forward to existing Pods, or uses curl in an explicitly selected existing client Pod. Native reachable-endpoint requests need no Kubernetes connection; optional read-only metadata lookup identifies existing application responders. No helper workloads are deployed.

Both clients support up to 1,000 sequential requests with streaming Results, Stop and Run again. Results distinguish the request origin, destination and actual authority, and group identified responders by cluster and version. Ambiguous or missing identities stay unverified. Backend-only port-forward bypasses mesh routing; existing-Pod mode uses cluster DNS and the mesh path. **Back to HTTP test** restores the originating map or wizard, profile and results without rerunning the request. Investigation status badges filter the checks.

HTTP 503 results surface an observed `no healthy upstream` response directly. In-mesh tests read ready EndpointSlices for Service backend references and flag weighted backends with no ready endpoints. A backend with no endpoints can still be selected by its weight; weighted routing does not provide primary/backup failover.

![HTTP test results](images/ambient-http-results.png)

More detail: [Ambient investigation and configuration](site/content/desk/ambient.md).
