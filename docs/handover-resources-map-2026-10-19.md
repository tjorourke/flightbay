# Handover: Resources map + global Service configure view (2026-10-19)

**Status: done.** Tasks 1-4 shipped to `main` as 6025692 (map picker + namespace waypoints) and
4180050 (global Service current configuration). Only the other session's RBAC edits remain uncommitted.
CI's gofmt step fails on `internal/collect/collect_test.go` and `internal/rbac/*.go`; that predates
these commits (8fd2321 failed the same way).

The original notes follow.

## Working tree: who owns what

| File | Owner | Notes |
|---|---|---|
| `web/resources-map.js` | this work | Full rewrite onto the ambient map anatomy |
| `web/app.js` | this work | Map tab subtitle, Refresh action, dead `after.resources` removed, `morphKids` skips `data-morph="keep"` |
| `web/ambient.css` | this work | `.rm-*` block replaced with lane/card/chip rules |
| `test/resources-map.test.cjs` | this work | Bridging, out-of-namespace count, uses de-dup tests |
| `internal/resmap/graph.go` | this work (check) | JSON tags on `Graph`/`Box`/`Node`/`Edge`. Confirm with `git diff` before committing |
| `internal/rbac/rbac.go`, `web/rbac.js`, `web/index.html`, `web/start.html`, `web/start.js` | **other session** | Do not stage |

Commit only the "this work" files, from an isolated worktree if the RBAC edits are still in flight.
Unknown artefact: `bin/flightbay-live-kind-mesh1_kind-mesh2-20261019T100814Z.tar.zst` appeared 11:08, not from this work. Leave it.

Dev instance: proxy pid 6645 serving `web/` from disk over `flightbay-desk live` pid 5509,
`http://127.0.0.1:54590/?token=<local-access-token>#resources?tab=map`.
Kill both when done (`kill 6645 5509`).

Last known state: 62/62 node tests, `go test ./web/... ./internal/resmap/... ./internal/server/...` pass, `make build` ok.

## What is already done (Resources > Map)

- Same shell as the Istio Ambient attachment map: Topology head (− zoom % +, Fit, Reset layout, Full screen),
  tools row (cluster, namespace, name filter, Kinds menu All/None/Defaults, "Show unlinked objects"),
  relationship chip row, dotted viewport, legend.
- `.am-canvas-card` cards: kind badge, name, one-line summary, red note for broken links, View object / YAML.
  Cards | Compact toggle; Compact default above 40 objects.
- Lanes: Policies → Gateways & ingress → Routes → Services → Endpoints → Pods → ReplicaSets & jobs →
  Workloads → Config & identity. Orthogonal routing via `amRoute` (121 cards / 81 edges ≈ 30 ms).
- Hidden intermediaries bridged (Deployment → Pod without ReplicaSet; Service → Pod without EndpointSlice).
  `uses` edges drawn once from the top-most owner.
- Chip highlight, hover trace, broken-link chip + red outline, anchored zoom, ⌘/Ctrl+wheel, drag pan,
  width-based Fit, CSS full screen with Escape. State on `S.res.map`.
- Live refresh refetches behind the drawn map; repaints only when the namespace changed and the user
  is not panning / hovering / in the Kinds menu.

## Tasks

### 1. Map: pick any objects, not just the ingress → service spine (user feedback)

Feedback: "it isn't just ingress/gw → services. What if I want to see jobs/pods/HTTPRoutes etc? I need some
way of selecting objects (good UX, multi select) and showing them in the graph too."

Today the Kinds menu defaults to 6/22 kinds and the graph is "everything of those kinds in one namespace".
Wanted:

- **Object picker with multi-select.** A searchable list (kind + name, grouped by kind, with counts) where the
  user ticks individual objects, shift-click ranges, select-all-of-kind, and sees the selection as removable
  chips. Keyboard: type to filter, space to toggle, Enter to apply.
- **Focus mode.** Draw the selected objects plus their neighbours to a chosen depth (1 / 2 / all), across
  lanes, so picking a Job shows its Pods, ServiceAccount, ConfigMaps; picking an HTTPRoute shows its
  parent Gateway, backend Services, Pods.
- **Add from the canvas.** On a card: "Add neighbours" / "Remove" / "Focus on this". From the Browse tab and
  object detail: "Show on map" that opens the map with that object selected.
- Make sure Jobs, CronJobs, StatefulSets, DaemonSets, ReplicaSets, Pods, HTTPRoutes/GRPCRoutes, policies are
  all reachable from the Kinds menu and lanes (check the lane assignment table in `web/resources-map.js`).
- Persist selection in `S.res.map` and in the hash (`#resources?tab=map&sel=...`) so it survives refresh and
  is shareable.
- Server side: `internal/resmap/graph.go` `Build(ws, cluster, namespace, kinds)` filters by kind only. Either
  add an `ids`/`depth` parameter or keep it client-side over the full namespace graph (check payload size on
  large namespaces first; kagent is 273 objects).
- Tests in `test/resources-map.test.cjs` for neighbour expansion depth and selection persistence.

### 2. Namespace-level `istio.io/use-waypoint` is ignored

`internal/resmap/graph.go:538-562` reads `istio.io/use-waypoint` (and `-namespace`) only from the object's own
labels. bookinfo sets the waypoint on the **namespace**, so no waypoint links are drawn for its Services/Pods.

Fix: resolve the effective waypoint in Istio order: object label, then namespace label
(`istio.io/use-waypoint`, `istio.io/use-waypoint-namespace` on the Namespace object), with `none` on the object
opting out. Mark namespace-inherited edges (e.g. `Label: "via namespace label"`) so the card summary and
legend can say so. Draw one edge from the Namespace box or per Service, whichever stays readable; per Service
with a "(namespace)" label is the simplest. Add a Go test with a namespace-labelled fixture and a `none`
override. Check the ambient map code for an existing resolver to reuse before writing a new one.

### 3. Global Service configure step looks like "start over" when it is already global

Screenshot: Istio Ambient → bookinfo/reviews → Publish a global Service → Configure. Both kind-mesh1 and
kind-mesh2 say "Already global · no label change required" and "Export label set", yet the page still reads as
a new setup (checkbox selection, "Publication change: sets solo.io/service-scope: global", "Review
configuration →").

User wants: when the Service is already published, show the **current configuration** and let them **view and
edit the YAML**, not a wizard that implies starting again.

Where: `web/global-publication.js` (line ~19 renders the cluster rows, ~41 plans, ~56-88 batch/review rows);
task registered in `web/ambient.js:127` (`'global-service'`).

Wanted:

- If every captured instance is already global, switch the Configure body to a "Current configuration" view:
  per cluster, the live label (`solo.io/service-scope: global`), the alias/published names and address
  evidence (already computed via `amPublicationEvidenceLabel` / `globalAliases`), endpoint contribution, and
  View object / YAML for the Service in each cluster.
- Offer actual edits rather than re-publish: stop contributing endpoints from a cluster (remove the label),
  add a cluster that has a local Service but is not global, change scope/defaults under "Scope and defaults".
  Each edit goes through the existing plan → dry-run → apply path.
- Header copy and the "This change" side panel should say "Already published in N clusters" and the primary
  button should be "Edit configuration" / "Review changes" (disabled until something differs).
- Mixed case (one global, one not): keep the wizard, but the global row shows current state, not a checkbox
  that suggests it will be changed.
- Check whether other ambient tasks have the same problem (task already satisfied but wizard says
  "configure"); the "Switch configuration · 2" control suggests a shared pattern worth fixing once.

### 4. Commit and publish the map work

After 1 and 2 are done (or before, if the user wants the current map landed first): run node tests,
`go test` for web/resmap/server, `make build`, stage only the "this work" files, commit, push.
