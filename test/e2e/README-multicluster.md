# Global-Service map fixture

Requires a working Solo ambient multicluster mesh with shared root trust and
east-west peering. The test does not install or upgrade the mesh platform.

On mesh1 and mesh2, apply `multicluster-demo.yaml`, then set the server identity:

```bash
kubectl --context kind-mesh1 apply -f test/e2e/multicluster-demo.yaml
kubectl --context kind-mesh1 -n flightbay-global-demo set env deployment/global-echo CLUSTER_NAME=mesh1
kubectl --context kind-mesh2 apply -f test/e2e/multicluster-demo.yaml
kubectl --context kind-mesh2 -n flightbay-global-demo set env deployment/global-echo CLUSTER_NAME=mesh2
kubectl --context kind-mesh1 -n flightbay-global-demo rollout status deployment/global-echo
kubectl --context kind-mesh2 -n flightbay-global-demo rollout status deployment/global-echo
bash test/e2e/multicluster-check.sh
```

The global hostname is `global-echo.flightbay-global-demo.mesh.internal` on the
default Segment. Override `HOST` for a different installed naming configuration.
Each response includes the configured mesh identity and actual Pod name. The
check temporarily scales down only this fixture's local server, proves a remote
response in each direction, then restores the exact initial replica counts.
An EXIT trap also restores them if a check fails.

Keep the app deployed to explore the map. Include `flightbay-global-demo`,
`istio-system` and `istio-eastwest` in both contexts' capture/live scope. The
map needs the generated ServiceEntries and mesh config, as well as Gateways and
Services, to establish the relationships from evidence.

The fixture namespace is separate from Bookinfo. To remove the app afterwards,
delete only `flightbay-global-demo` in the two contexts. That does not remove
shared mesh peering gateways.

## Keep petstore running while testing publication

`petstore-mesh2.sh` creates a matching, initially unpublished `petstore-prod/petstore`
Deployment and Service on mesh2 from an existing dependency-free mesh1 app:

```bash
bash test/e2e/petstore-mesh2.sh up
bash test/e2e/petstore-mesh2.sh status
```

It uses the source container image and loads a `localhost:5001` image into the
target kind cluster before rollout. The local Docker image must already exist.
It does not copy source publication labels, credentials or environment/volume
dependencies, and refuses a pre-existing namespace or resource it does not own.
The target is expected to use mesh network `mesh2`; `SOURCE` and `TARGET` can
override the corresponding kubeconfig context names.

In Istio Ambient, select this Service, choose **Publish a global Service**, select
the participating clusters, review their independent YAML and explicitly choose
the real contexts. Dry-run first. Cancel the Apply confirmation to check that no
label was written, then apply the mesh2 plan and inspect its generated global
hostname. **Active tests → Undo changes and remove test resources** removes only
the reviewed publication change: the application remains running for another test.
Do not change an existing source cluster's publication or NetworkPolicies.

The 17 October 2026 browser run validated both context-specific plans, rejected a
deliberately mismatched context, cancelled both single-row and batch confirmation,
applied and undid the mesh2 label, and verified the mesh2 Deployment stayed Ready.
Backend port-forward requests to local/global HTTP authorities returned 200;
these bypass mesh routing. A separate request from the existing mesh2 client to
the local Service also returned `ok`.

Only if the fixture application itself should be removed:

```bash
bash test/e2e/petstore-mesh2.sh remove-app
```

That explicit action deletes its owned Deployment and Service, leaving the
namespace and shared mesh infrastructure in place. It is separate from undoing
publication.
