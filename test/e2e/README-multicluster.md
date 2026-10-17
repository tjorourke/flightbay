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
