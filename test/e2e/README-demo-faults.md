# Toggle local mesh1 demo failures

Run this from your own terminal with your normal kubeconfig, not Flightbay's read-only terminal. The script accepts only `kind-mesh1`, a loopback API server and nodes named `mesh1-*`. It never changes mesh2.

```bash
# Show all three demo fault states.
test/e2e/mesh1-demo-faults.sh status

# Enable Bookinfo's broken listener attachment.
test/e2e/mesh1-demo-faults.sh on bookinfo-route

# Repair it and wait for current Accepted=True and ResolvedRefs=True.
test/e2e/mesh1-demo-faults.sh off bookinfo-route
```

The other selectable faults are:

| Fault | On | Off |
|---|---|---|
| `bookinfo-route` | `reviews-mesh-split` points at `bookinfo-waypoint/wrong-listener` | Service parent `reviews:9080` |
| `petstore-route` | `petstore-prod` points at `agentgateway-system/missing-gateway` | Gateway parent `agentgateway-system/ar-ingress` |
| `petstore-global` | Removes `solo.io/service-scope` from `petstore-prod/petstore` | Sets `solo.io/service-scope=global` |

```bash
test/e2e/mesh1-demo-faults.sh on petstore-route
test/e2e/mesh1-demo-faults.sh off petstore-route
test/e2e/mesh1-demo-faults.sh on petstore-global
test/e2e/mesh1-demo-faults.sh off petstore-global

# Toggle all three together.
test/e2e/mesh1-demo-faults.sh on all
test/e2e/mesh1-demo-faults.sh off all
```

`on` means **enable the failure**. `off` means **repair the failure**. Without a fault name, on/off use `bookinfo-route`; status uses all.

For a custom Bookinfo namespace, prefix the command with `BOOKINFO_NAMESPACE=bookinfo-demo`. The listener fault detects Reviews' Service-specific waypoint or namespace default, so it also works with custom waypoint names and dedicated-per-Service mode.

Only the first parent reference or the publication label is changed. Route rules, backend weights and additional parent entries are preserved. Route patches use a resourceVersion precondition. Off installs the documented healthy demo parent; it does not restore arbitrary earlier custom parents. Active fault annotations are removed when a route is repaired.

After toggling, use Refresh in Investigate or trace the path again. Request-path results deliberately stay unchanged while you read them. For Bookinfo, try From application `bookinfo/productpage-v1`, To Service `bookinfo/reviews`, port `9080`, and optionally `GET /reviews/0`.
