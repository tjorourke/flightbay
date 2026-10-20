# Flightbay: rebuild the Istio Ambient tab UX

A handover plan for a model or engineer who has not seen the earlier conversation. Read the whole thing before starting. Work through the phases in order. Each task has the files to touch, the mockup to match, and acceptance criteria that must pass before the task is ticked.

- **Repo:** `/Users/thomasorourke/code/solo/flightbay`
- **Mockups:** `~/Downloads/flightbay-ux-mockups/`, copied to `docs/ux/ambient/` in task 0.5. After that, use only the repo copy. **Read §0.5 (Mockup fidelity contract) before any UI work.** Open `index.html` for the overview. Each screen exists as `NN-name.html` (source, open it in a browser) and `NN-name.png` (render). **Ignore the `v1/` subfolder**; it is a superseded draft.
- **Source recording:** `~/Desktop/Screen Recording 2026-10-20 at 11.47.53.mov` (60 s; Tom configuring `bookinfo/reviews` on `kind-mesh1`).
- **Scope:** only the **Istio Ambient** page (sidebar entry `data-view="ambient"`, `web/index.html:28`). Do not change the sidebar, Overview, Resources, RBAC, Log explorer, Timeline, Traffic, Findings, Scorecard, Compare, Proxy diagnostics or Coverage. Where Ambient currently links out to those pages (for example Traffic's "Review in Ambient wizard →", `web/traffic.js:947`, `1113`, or Scorecard Fix, `web/app.js:1078-1093`), keep the links working by pointing them at the new routes. Do not redesign those pages.

---

## 0. Read this first

### 0.1 Why we are doing this

Tom finds the Ambient area confusing. From the recording and the code:

1. **Too many navigation layers for one job.** Sidebar → five page tabs (Investigate, Attachment map, Request path, Configure, Changes) → a target bar → a breadcrumb (`All configurations › bookinfo/reviews › …` with `Switch configuration · 2`) → a four-step stepper (Target, Configurations, Configure, Review). Several buttons on one screen look primary: New configuration, Test current traffic, Configure selected tasks.
2. **The stepper is not a sequence.** Step 2 "Configurations" is a read-only overview of the Service. Adding new tasks is under a collapsed "Add another capability" at the bottom of step 2. Editing an existing route skips step 2.
3. **Existing config and available tasks are in separate places.** Policy and Routing cards at the top, a 10-card task grid lower down, and "7 tasks not available · why?" in a third place. Nothing says "split is set, timeout is not, retries is impossible here" in one view.
4. **Picking a target starts again from nothing.** The Configure tab opens with a combobox over every Gateway and Service in every cluster.
5. **Investigate and Configure are separate flows on the same object.** Remedies jump into a wizard "repair mode".
6. **No explanation of effect or interaction.** Adding an ALLOW for some methods on a Service that already has a DENY changes the default for every other method. Review shows a resource diff, not "POST now returns 403".
7. **Apply is buried and states blur.** Review and apply sits under the summary with Dry-run only, a context box, a second "Review and apply…" button, then a modal. Dry-run passed, applied and working look alike.
8. **Testing is per Service and ad hoc.** "Test this rule" pre-fills one request. Nothing is saved, nothing has an expected result, and HTTPRoutes get only the generic HTTP batch.
9. **Cluster scope is split three ways:** the global `#cluster` select, the target's cluster, and a separate write context.
10. **Likely bug:** in the recording, *Deny by default* and *Control HTTP methods* were both ticked, but Configure and Review only showed *Control HTTP methods* ("1 to create"). The server would generate both (`internal/ambient/plan.go:330`), so the UI probably dropped a task. Reproduce it in Phase 0.

Many of these are already listed in `docs/ambient-follow-up-tasks.md` (items 1 to 10). This plan closes items 2, 3, 4, 6, 8, 9 and 10, and partly closes 1 and 5. Tick them in that file as they land.

### 0.2 Target information architecture

```
Istio Ambient (sidebar, unchanged)
├── Ambient home
│   ├── Overview            ← new landing (replaces Investigate landing + Configure Target step)
│   ├── Policies & routes   ← new inventory
│   ├── Map                 ← today's Attachment map, kept as it is (small fixes only)
│   ├── Request path        ← today's tracer, kept as it is
│   └── Changes             ← existing, restyled status words
└── Service page            (opened from any of the above; breadcrumb "Istio Ambient › Services › ns/name")
    ├── Configuration       ← replaces Configure tab (four question cards)
    ├── Checks              ← replaces Investigate for a target
    ├── Tests               ← new; suites for this Service's policies and routes
    ├── Request path        ← the same tracer as the home tab, with this Service pre-filled as the destination
    └── Changes             ← existing Changes, filtered to this Service
    └── Object page         (AuthorizationPolicy / HTTPRoute opened from Configuration)
        ├── Summary · Tests · Explain a request · History
Task drawer (right-hand side, over the Service page) ← replaces the 4-step wizard
```

Non-Service targets:
- **Namespace:** settings open from the namespace footer on Overview (`00`), in the same drawer pattern (tasks `waypoint`, `enrol-namespace`).
- **Gateway, HTTPRoute, ServiceEntry:** open the object page. An HTTPRoute opens the HTTPRoute object page (`05`). A waypoint Gateway opens a simple object page listing the Services it serves and the policies it enforces. That page has no mockup: reuse the `05` layout with a Services table.
- **Pod:** investigation only today. Keep Pod checks reachable from the Service page's Checks tab through workload rows. Do not add a Pod page.

### 0.3 Mockup index

| ID | File | What it shows | Used by phase |
|---|---|---|---|
| 00 | `00-ambient-overview.png` | Ambient home · Overview: summary cards, Needs attention, Services table, namespace footer | 2 |
| 01 | `01-service-configuration.png` | Service page · Configuration · **Summary** (default): slim cluster strip, Summary/All tasks toggle, four question cards (answer, configured items, one hint, Add links, locked tasks), multicluster footer | 3 |
| 01c | `01c-all-tasks-cards.png` | Service page · Configuration · **All tasks**, Cards: all 20 tasks in five groups | 3 |
| 01d | `01d-all-tasks-list.png` | Service page · Configuration · **All tasks**, List: the same 20 tasks as rows | 3 |
| 01b | `01b-service-checks.png` | Service page · Checks: problems first, grouped passes, evidence chips, Fix | 5 |
| 02 | `02-configuration-inventory.png` | Ambient home · Policies & routes: inventory with Applies to, per-cluster status, Managed by | 6 |
| 03 | `03-simple-wizard.png` | Task drawer, phase 1: intent options, fields, effect panel, generated tests, footer | 4 |
| 03b | `03b-apply-and-test.png` | Task drawer, phase 3: progress list, test results, What now, status vocabulary | 4, 7 |
| 04 | `04-authorizationpolicy-tests.png` | AuthorizationPolicy object page · Tests | 7 |
| 05 | `05-httproute-tests.png` | HTTPRoute object page · Tests: expected vs observed weights, match cases | 7 |

The yellow numbered circles and the yellow "notes" boxes in the mockups are annotations. **Do not build them.** The data in the mockups is illustrative. The failing "color: Red" test in 03b and 04 is invented to show what a suite would catch.

### 0.4 Constraints and conventions

- **Stack:** vanilla JS with no framework and no bundler. Global state `S` (Ambient state is `S.ambient`, usually aliased `A`), template-literal HTML, `render()` plus `after[view]()` wiring. Static files are embedded into the Go binary (`web/embed.go`) and loaded in order from `web/index.html:77-94`. Add new scripts there.
- **Keep the existing server pipeline.** `POST api/ambient/plan` → `ambient.Generate` → `ReconcilePlan`. `POST api/ambient/test` handles `validate`, `apply`, `status` and `cleanup`, with preflight, dry-run, the 10-minute validation window, rollback and the `~/.flightbay/ambient-tests` records. The UI is being rebuilt, not the write path. Do not weaken preflight or the confirmation step.
- **Start from current `main`.** The resilience and circuit-breaker work from another session has been committed, so pull first; line numbers in this plan may have moved because of it. Work on a branch (`git switch -c ambient-ux`) and rebase onto `main` regularly.
- **Feature flag.** Build the new UI behind `S.ambient.v2`, enabled by `?amv2=1` (persisted in `localStorage['flightbay.amv2']`) until Phase 10. The old UI must keep working until then. Both share the same API.
- **UI copy rules (Tom's voice):** UK English, no em dashes, no marketing words, no analogies. Headings label a section plainly or ask a plain question ("Who can call it?"). Use exactly the status vocabulary in §0.6.
- **Tests:** `make test` runs `go test ./...` and `node --test test/*.test.cjs`. Browser test: `internal/server/ambient_browser_test.go`. Live kind: `test/e2e/live.sh`, `test/e2e/mesh.sh`, `test/e2e/multicluster-check.sh`. Every phase adds node tests for its pure functions, following the pattern of `test/ambient-edit.test.cjs` and `test/configure-clusters.test.cjs`.
- **Docs:** update `docs/site/content/desk/ambient.md` (the most detailed UX spec, 394 lines) and `docs/ambient.md` when behaviour changes. Fix the contradiction that `docs/ambient.md` says "No helper workloads are deployed" when the temporary Pod mode does deploy one.

### 0.5 Mockup fidelity contract (read before every screen)

Earlier hand-offs drifted away from the mockups. These rules exist to stop that. They apply to every task that renders UI.

1. **The mockups are the spec.** Order of authority: mockup HTML source, then this plan's text, then existing Flightbay code and CSS. If the plan text and a mockup disagree, stop and ask Tom. Do not pick one quietly.
2. **Read the HTML, not only the PNG.** For each screen, open `docs/ux/ambient/<screen>.html` (the source) and view `<screen>.png`. The HTML has the exact wording, element order and CSS classes. Re-open both at the start of every screen and after any context compaction. Do not work from memory.
3. **Build the new markup; reuse the old logic.** Port `docs/ux/ambient/mock.css` into `web/ambient-v2.css` (prefix classes `am2-`, map colours onto the tokens in `web/app.css`) and keep the mockup's structure: the same containers, the same order, the same element types (segmented control, card, text link, pill). Reuse existing JS for data, plans and applies. Do **not** wrap new data in old components such as `.panel`, `.am-*` cards or the old stepper because they are already there.
4. **Copy the text exactly.** Headings, tabs, buttons, links, column headers, chips, toggles and status words are copied verbatim from the mockup. Data (Service names, numbers, rule summaries) comes from the API and will differ. That is expected.
5. **Add nothing and drop nothing.** No extra buttons, badges, columns, helper text, icons or explainer panels that are not in the mockup. If you think something is missing, finish the screen as drawn and list the suggestion in the PR description.
6. **Ignore the annotations.** Yellow numbered circles and the yellow "notes" boxes explain the design. Never build them.
7. **Verify every screen with the comparison loop.** It is repeated until it passes:
   1. Start the preview from source:
      `FLIGHTBAY_AMBIENT_PREVIEW=$PWD/flightbay-mesh1-objects.tar.zst go test ./internal/server -run TestAmbientBrowserPreview -v -timeout 20m`
      (it serves `web/` from disk, disables real cluster actions and prints `AMBIENT_PREVIEW=<url>`).
   2. Set the routes in `docs/ux/ambient/tools/routes.json` to objects that exist in the fixture.
   3. Run `python3 docs/ux/ambient/tools/ux_compare.py --base '<url without #…>' --screen <screen>`.
   4. Open `ux-review/<screen>/report.md` and **view** `ux-review/<screen>/side-by-side.png` (mockup on the left, app on the right).
   5. Write down every visible difference that is not data: missing or extra elements, wrong order, wrong wording, wrong control type, layout, spacing, visual weight (for example a table where the mockup has cards, or a primary button where the mockup has a text link).
   6. Fix them and go back to step 3. Stop only when the report has no `MUST` failures and the only differences left in the image are data.
8. **Get an independent review.** When the loop passes, start a fresh sub-agent with no project context. Give it only the mockup PNG, `ux-review/<screen>/app.png` and `ux-specs/<screen>.json`, and ask: "List every difference between these two screens other than the data values. Be strict." Fix what it finds, then rerun the loop.
9. **One screen at a time, with a gate.** Finish, verify and commit one screen before starting the next. Put the side-by-side PNG and the final report in the commit or PR description, and pause for Tom's approval before moving to the next screen.
10. **Record deviations.** If a difference must stay (a technical limit, or Tom agreed to it), list it in `docs/ux/ambient/DEVIATIONS.md` with the screen, the difference and the reason. An unlisted difference is a defect.

`ux-specs/*.json` is generated from the mockups by `tools/extract_specs.py`. Do not hand-edit it. If a mockup changes, rerun the extractor. The text check catches missing and reordered labels; it cannot see layout or extra elements, which is why steps 7.4 to 7.5 and 8 are mandatory.

### 0.6 Status vocabulary (use everywhere)

| Word | Meaning | Pill class in mock.css |
|---|---|---|
| Draft | Only in this browser, nothing sent | `mut` |
| Checked | Server dry-run passed, nothing written | `info` |
| Applied | Written to the cluster, status not yet confirmed | `tl` |
| Live | Accepted by the enforcing component (waypoint `WaypointAccepted`, route `Accepted` + `ResolvedRefs`, or Programmed) and visible in the live mirror | `ok` |
| Verified | Live, and its saved tests pass | `ok` with ✓ |
| Failed | Apply failed and was rolled back, or tests fail | `bad` |

Task statuses (used in the API and the drawer; the Configuration tab only shows them as item pills, Add links or the unavailable popover): **Configured**, **Not set**, **Inherited**, **Recommended**, **Needs waypoint**, **Unavailable**, **Differs** (multicluster only).

---

## Phase 0: Preparation

- [ ] **0.1 Set up the branch and baseline.** Pull `main` and create the branch, run `make build` and `make test`, and record any tests that already fail in `docs/ambient-ux-rebuild-notes.md`. You will delete that file at the end.
- [ ] **0.2 Capture the current UI.** Start `flightbay-desk live --context kind-mesh1,kind-mesh2` (or open `flightbay-mesh1-objects.tar.zst` from the repo root) and screenshot every Ambient tab for `bookinfo/reviews`. These are the "before" images for the PR.
- [ ] **0.3 Reproduce the dropped-task bug.** Tick *Deny by default* and *Control HTTP methods*, then *Configure selected tasks*. Record the payload of `POST api/ambient/plan` (`tasks` array). If `l4-default-deny` is missing, find where it is lost: start at `amTaskCards()` (`web/ambient.js:142`), the selection state, then `amWizard()` (`:200-233`) and `amRead()`. Fix it in the old UI with a regression test, because the drawer in Phase 4 will not carry the bug forward but the old UI ships until Phase 10.
  - *Acceptance:* both tasks reach the plan, Review shows 2 to create, and a node test covers it.
- [ ] **0.4 Inventory the code you will reuse.** Read and note signatures for:
  - `web/ambient.js`: `ambientPage` (`:50-81`), `amInvestigation` (`:83`), `amKnownIssues` (`:100`), `amServiceOverview` (`:114`), `amTaskText` (`:121-141`), `amTaskCards` (`:142`), `amWizard` (`:200-233`), `amWaypoint` (`:234`), `amRecovery` (`:249`), `amPlanPanel` (`:264`), `amTests` (`:271`), `amCapture` (`:283`), `amRead` (`:307`), `amWirePicker` (`:353`), `amAccessRuleMarkup` (`:516`), `amLivePane`/`amLiveRender` (`:676-689`), `amContextControl` (`:13`).
  - `web/ambient-configure.js`: `amConfigurationHome` (`:42`), `amRefreshAppliedConfiguration` (`:56`), `amPoliciesPanel` (`:105`), `amWaypointPanel` (`:135`), `amTestAccessRule` (`:155`), `amRoutingPanel` (`:169`).
  - `web/global-publication.js`: `amGlobalBatchPanel` (`:105`), `amConfirmApply` (`:174`).
  - `web/configure-clusters.js` (whole file). `web/http-test.js:9` and `web/http-mesh-test.js` (the runners). `web/request-path.js:19`. `web/ambient-map.js` (`amClusterCanvas` `:5`, `amMapPolicies` `:79`, `amGraph` `:115`, `amEditors` `:161`).
  - Server: `internal/server/ambient.go` (`ambientPlan` `:131`, `ambientContexts` `:143`, `ambientLiveCluster` `:165`, `ambientConnect` `:184`, `ambientPreflight` `:246`, `ambientTest` `:498-672`), `internal/ambient/plan.go` (`Request` `:27`, `Capabilities()` `:154-246`), `internal/ambient/context.go` (`View` `:58`, `NewView` `:105`, `serviceGraph` `:361`), `internal/ambient/waypoint_binding.go` (`servicePolicies` `:210-316`), `internal/ambient/edit.go` (`editableConfigurations` `:19`), `internal/ambient/topology.go:84`, `internal/ambient/reconcile.go:19`, `internal/diff/drift.go` (field diff, reusable for cross-cluster drift), `internal/flows/envoy.go` (`ParseEnvoy` `:134`, RBAC denial detection `:220`), `internal/reach/explain.go` (`Explain` `:116`).
  - Line numbers are from the working tree on 2026-10-20 and will drift. Search by function name.

- [ ] **0.5 Put the mockups in the repo.** Copy `~/Downloads/flightbay-ux-mockups/` to `docs/ux/ambient/` (skip `v1/` and `rejected/`), commit it, and from now on use only the repo copy. Run `python3 docs/ux/ambient/tools/extract_specs.py` once and commit `ux-specs/`. Add `ux-review/` to `.gitignore`.
- [ ] **0.6 Add the contract to the repo's agent instructions.** Create `CLAUDE.md` and `AGENTS.md` at the repo root (same content) containing §0.5 word for word, plus "The plan for the Ambient rebuild is `docs/ux/ambient/PLAN.md`." Copy this plan to `docs/ux/ambient/PLAN.md`. Every new session then picks up the rules automatically, whichever model runs it.
- [ ] **0.7 Prove the loop works before building anything.** Start the preview, run `ux_compare.py` for `01-service-configuration` against today's UI, and check that it produces `app.png`, `side-by-side.png` and a report full of `MISSING`. That is the baseline.
---

## Phase 1: Navigation shell and routing

Goal: the new two-level structure exists and is navigable, with placeholder bodies, behind the flag.

- [ ] **1.1 Add the feature flag.** Add `S.ambient.v2` (read from `?amv2=1` and `localStorage`). `ambientPage()` dispatches to `amV2Page()` when it is set.
  - *Files:* `web/ambient.js`, plus a new file `web/ambient-v2.js` loaded after `ambient.js` in `web/index.html`.
  - *Acceptance:* without the flag nothing changes, and `make test` stays green.
- [ ] **1.2 Define the new hash routes** in `parseHash` (`web/app.js:70-83`) and the `hashchange` handler (`:2814`).

  | Route | Screen |
  |---|---|
  | `#ambient` or `#ambient?tab=overview` | Overview (00) |
  | `#ambient?tab=policies[&ns=&kind=&problems=1&drift=1&group=service]` | Policies & routes (02) |
  | `#ambient?tab=map` | Map (today's canvas, unchanged look) |
  | `#ambient?tab=path[&from=&to=]` | Request path (today's tracer, unchanged look) |
  | `#ambient?tab=changes` | Changes |
  | `#ambient?svc=<cluster>/<ns>/<name>[&stab=configuration\|checks\|tests\|path\|changes]` | Service page (01, 01b) |
  | `#ambient?obj=<cluster>/<Kind>/<ns>/<name>[&otab=summary\|tests\|explain\|history]` | Object page (04, 05) |
  | `…&drawer=add:<taskId>` or `…&drawer=edit:<editKey>` or `…&drawer=fix:<checkId>` | Task drawer over the current page (03, 03b) |

  - **Back-compat redirects**, applied with or without the flag once Phase 10 lands:
    - `tab=investigate&kind=Service&…` → `svc=…&stab=checks`
    - `tab=wizard&kind=Service&…` → `svc=…&stab=configuration`
    - `tab=wizard&…&route=` → `obj=…/HTTPRoute/…`
    - `tab=path` → `tab=path` (unchanged)
    - `tab=map` → `tab=map`
    - `tab=demo` stays as it is
  - *Acceptance:* a new `test/ambient-v2-routes.test.cjs` covers parse and serialise round-trips and every redirect. Browser back and forward move between tabs and drawer states.
- [ ] **1.3 Ambient home header.** Title "Istio Ambient", tabs Overview · Policies & routes (count) · Map · Request path · Changes (count), and on the right **Docs ↗**. Remove **New configuration**, **Refresh** and **Deploy Bookinfo demo** from the header. Move Bookinfo to an empty-state link on Overview when no mesh Services exist, and to Overview's "⋯" menu otherwise. Refresh becomes automatic in live mode; in snapshot mode it goes into the "⋯" menu.
  - *Mockup:* 00, 02 (top of page). CSS classes `.amhead`, `.amtabs` from `~/Downloads/flightbay-ux-mockups/mock.css`. Port them into `web/ambient.css` using the existing tokens (`--teal`, `--accent-bg`, etc. are already in `web/app.css`).
- [ ] **1.4 Service page shell.**
  - Breadcrumb `Istio Ambient › Services › ns/name`.
  - Object header: kind badge, name, meta line (port(s), workload versions, "in ambient", waypoint name plus readiness pill).
  - Right-hand buttons: **Test service** and **YAML**. There is no header Add button (see 3.5).
  - Tabs: Configuration (n) · Checks (status) · Tests (n) · Request path · Changes (n).
  - *Mockup:* 01 and 01b, header and tabs.
  - Replaces the target bar (`web/ambient.js:78`) and `amConfigurationNav` (`:44`) in v2. The target bar's DNS names, Implementation details and View object move into the Overview tab's "⋯" menu on the header. Keep them accessible but out of the primary row.
- [ ] **1.5 Cluster strip (shared component).** `amClusterStrip({clusters, reading, writeContext, compare})` renders:
  - A segmented control with one segment per cluster where the object exists, plus **Compare** when there are two or more.
  - "Reading the live mirror · updated Ns ago", or "Reading snapshot <name>".
  - Right-aligned: "Writes go to **<cluster>** via kubeconfig context `<ctx>`", built from `GET api/ambient/contexts` with the `liveCluster` match (`internal/server/ambient.go:143-165`).
  - If no write context matches, say "No write access to <cluster>. Open with --kubeconfig to change it" and disable every + Add and Edit.
  - The segment selects which cluster's data the page shows. It does **not** change the global `#cluster` select. Initialise it from the global select when that is set to a cluster.
  - *Mockup:* 01, the strip under the tabs.
  - *Acceptance:* closes follow-up #6. The write context can never point at a different API server from the cluster shown, and a mismatch is shown as a block, not a choice.

---

## Phase 2: Ambient Overview

Mockup **00**.

**UX gate:** follow §0.5 for `00-ambient-overview`. A task in this phase is not done until its screen passes the comparison loop and the independent review.

- [ ] **2.1 Summary cards (4):**
  - Clusters: count, names, ambient on each.
  - Namespaces in ambient: n of m, and how many have a waypoint.
  - Policies and routes: count, and how many differ between clusters.
  - Needs attention: count of Services with failed checks (red).
  - *Data:* `GET api/ambient` (`ambientInventory`). Add whatever fields are missing to `Inventory` in `internal/ambient`. Each card links to the relevant tab and filter.
- [ ] **2.2 Needs attention panel.** Reuse `S.ambient.inventory.issues` (the `amKnownIssues` data, `web/ambient.js:100`).
  - Each row shows: status pill, `ns/name`, cluster, a one-line failed check, then **Open checks** (→ `svc=…&stab=checks`) and primary **Fix…** (→ `…&drawer=fix:<checkId>`).
  - Sort failed above warning. Hide the panel entirely when empty and show "No failed checks" in the summary card instead.
  - *Acceptance:* closes follow-up #4.
- [ ] **2.3 Services table.**
  - Columns: Service, Waypoint, Access, Routing, Resilience, one column per cluster, Checks, Open.
  - Badges: Access (`1 DENY`, `1 ALLOW`, `L4 lock`, `JWT`, or "open"), Routing (`split 80/20`, `header match`, `mirror`, `timeout`, or "backend of X" when the Service is only a backendRef), Resilience (`outlier`, `circuit breaker`, `retries`, `rate limit`).
  - Per-cluster cell: ✓ present, `differs`, `missing`, `no endpoints`.
  - Filter box plus namespace chips. The row click target is the whole row.
  - *Backend:* add `GET api/ambient/services?namespace=` returning `[]ServiceRow{ref, perCluster{cluster→{present, endpointsReady, waypoint, checks{fail,warn}}}, access[], routing[], resilience[], drift bool}`. Build it from `topology.clusters[].services[].policies` (`internal/ambient/topology.go:84`) plus `editableConfigurations` per Service. Put the badge derivation in Go (`internal/ambient/summary.go`) with table tests.
  - *Acceptance:* the bookinfo fixture on kind-mesh1 shows the reviews row with `1 DENY` and `split 80/20`. Clicking it opens `svc=kind-mesh1/bookinfo/reviews&stab=configuration`.
- [ ] **2.4 Namespace footer.**
  - Under the table for the selected namespace chip: "Namespace **bookinfo**: in ambient · waypoint `bookinfo-waypoint` for all Services", and a **Namespace settings** button.
  - That button opens the drawer with namespace tasks: *Enrol in ambient* (`enrol-namespace`) and *Set up a waypoint* (`waypoint`, with the existing scope options from `amWaypoint`, `web/ambient.js:234`).
  - This replaces the landing intent picker "What would you like to configure?" (`amLanding`, `:49`) and its "Namespace setup" and "Waypoint connections" cards.
- [ ] **2.5 Remove the target picker in v2.** The Object type select and the global combobox (`amPicker`, `:39`) are not rendered in v2. The global ⌘K search still finds Services; make a Service result open `#ambient?svc=…` when Ambient is available.

---

## Phase 3: Service · Configuration

Mockup **01**. This is the core of the rebuild.

**UX gate:** follow §0.5 for `01-service-configuration`, `01c-all-tasks-cards` and `01d-all-tasks-list`. A task in this phase is not done until its screen passes the comparison loop and the independent review.

- [ ] **3.1 Backend: one endpoint for the tab.** Add `GET api/ambient/service-config?cluster&namespace&name`, returning:
  ```jsonc
  {
    "subject": Ref,
    "answers": { "access": "...", "routing": "...", "transport": "...", "resilience": "..." }, // plain English, see 3.3
    "tasks": [{
      "id": "methods",                 // same IDs as Capabilities()
      "title": "Control HTTP methods",  // from one Go table (move amTaskText here)
      "group": "access|routing|resilience|multicluster",
      "layer": "L4|L7|",
      "status": "configured|not-set|inherited|recommended|needs-waypoint|unavailable|differs",
      "reason": "…",                    // for unavailable/recommended/inherited/differs
      "resources": [{ "ref": Ref, "rule": 0, "summary": "Deny when request.headers[color] is red",
                      "action": "DENY", "state": "Accepted", "editKey": "…", "editable": true,
                      "readOnlyReason": "", "managedBy": "flightbay|helm|argocd|flux|kubectl|unknown" }],
      "enforcedBy": "waypoint|ztunnel|",
      "hint": "Adds a rule to the same HTTPRoute"
    }],
    "clusters": ["kind-mesh1","kind-mesh2"]
  }
  ```
  - *Implementation:* new `internal/ambient/service_config.go`. Compose it from `Capabilities()` (evidence gating gives unavailable and needs-waypoint, with reasons), `NewView(...).Editable` (gives configured resources; map each `Editable.Tasks` to the task id) and `View.Policies` (classification gives inherited from namespace-wide or mesh-wide scope).
  - **Recommended rules** (start with these and keep them in one function):
    1. `require-waypoint` when the Service has any L7 policy enforced by a waypoint and no ztunnel ALLOW restricted to the waypoint SA.
    2. `strict-mtls` when an L4 policy uses principals but mTLS is PERMISSIVE.
  - **Managed by:** check first whether the store keeps `metadata.managedFields`. `internal/collect/inventory.go:36` mentions it, and `internal/ambient/changes.go:91` strips it for diffs. If it is kept, use the manager names. Otherwise fall back to labels and annotations:
    - `app.kubernetes.io/managed-by` (`flightbay` → Flightbay, `Helm` → Helm)
    - `meta.helm.sh/release-name` → Helm
    - `argocd.argoproj.io/tracking-id` or `argocd.argoproj.io/instance` → Argo CD
    - `kustomize.toolkit.fluxcd.io/name` → Flux
    - otherwise kubectl if a `kubectl.kubernetes.io/last-applied-configuration` annotation exists
    - otherwise Unknown
  - **Differs:** for a configured resource, compare it with the same kind/ns/name on the other connected clusters using the field comparison in `internal/diff/drift.go`, ignoring metadata noise. Report `missing`, `differs` or `same` per cluster. This is reused in Phase 6 and the multicluster footer.
  - *Tests:* `internal/ambient/service_config_test.go`, table-driven over the bookinfo fixture:
    - reviews with a DENY gives Allow-or-deny = configured and Block-bypass = recommended
    - a mesh-wide PeerAuthentication gives mTLS = inherited
    - Gateway API without retry in the schema gives retries = unavailable, with the reason text
- [ ] **3.2 Layout: four question cards, not a table.** Mockup **01**. The v1 design (an answer-card row plus a 20-row task table with six columns) was rejected as too busy. The v1 image is in `~/Downloads/flightbay-ux-mockups/v1/01-service-configuration.png`; do not build it.
  - Under the Service header and cluster strip, show a 2×2 grid of cards, in this order:

    | Card heading | Task group | Answer source |
    |---|---|---|
    | **Who can call it?** | access (methods, access-rule, jwt, l4-default-deny, l4-allow-namespace, require-waypoint) | `answers.access` |
    | **Where does traffic go?** | routing (weighted-routing, header-routing, mirroring, load-balancing) | `answers.routing` |
    | **Is traffic encrypted?** | transport (strict-mtls) | `answers.transport` |
    | **What happens under load or failure?** | resilience and limits (timeouts, retries, endpoint-failover, circuit-breaker, rate-limit, global-rate-limit) | `answers.resilience` |

    Full task-to-group mapping (all 20 tasks from `Capabilities()`; the All tasks view in 3.6 uses the same groups plus a fifth):

    | Group | Tasks (id → short label) |
    |---|---|
    | Who can call it? | `access-rule` Allow or deny requests · `methods` Allow or block methods · `jwt` Require a JWT · `l4-default-deny` Deny by default · `require-waypoint` Block traffic that skips the waypoint · `l4-allow-namespace` Allow a namespace |
    | Where does traffic go? | `weighted-routing` Split traffic · `header-routing` Route by header or path · `mirroring` Mirror requests · `load-balancing` Balance endpoints · `locality-failover` Fail over between regions · `service-failover` Primary and backup Services |
    | Is traffic encrypted? | `strict-mtls` Require mTLS |
    | What happens under load or failure? | `timeouts` Set a timeout · `endpoint-failover` Eject failing endpoints · `circuit-breaker` Circuit breakers · `retries` Retry failed requests · `rate-limit` Limit request rate · `global-rate-limit` Shared rate limit |
    | Is it available in other clusters? (All tasks view only; Summary uses the multicluster footer) | `global-service` Publish as a global Service |

    Keep this mapping and the short labels in one Go table (`internal/ambient/tasks.go`) returned by the API, so Summary, All tasks, the drawer's Switch task menu and Policies & routes all agree. When a new task is added to `Capabilities()`, a test must fail until it has a group and a label.

  - Below 1280 px the grid becomes one column, in the same order.
  - **Card anatomy, top to bottom:**
    1. Small uppercase heading (the question).
    2. The answer: one sentence, about 16 px, plain English (see 3.3). When nothing is set, the sentence says what that means in practice and is greyed ("Nothing is set. Requests wait as long as the caller does and failing Pods keep getting traffic.").
    3. Routing card only: one weight bar with backend labels under it.
    4. One **item row per configured or inherited resource**: a status pill (`DENY`, `ALLOW`, `Split`, `Inherited`, …), a short human label ("Block header color = red", "All paths on port 9080"), a grey mono sub-line `Kind · name`, and text links on the right (**Test** and **Edit** for editable resources, **Open** for inherited or read-only ones). If one rule of a resource is shown, the label names the rule.
    5. At most **one recommendation hint** per card (amber, one line, one link), placed under the item it relates to. Example: "Callers can skip this rule by going straight to the Pods. **Block that →**" opens the drawer `add:require-waypoint`.
    6. An **Add** footer: the group's Not set tasks as short text links (at most three or four, ordered by how common they are), then **More…** if there are more. Use short action labels, not the catalogue titles: "Allow only some methods", "Require a JWT", "Set a timeout", "Require mTLS for reviews". Each link opens the drawer `add:<taskId>`.
    7. **Locked tasks stay visible.** Under the Add row, list the group's unavailable tasks as greyed labels with a lock ("🔒 Limit request rate", "🔒 Retry failed requests") and one muted "Not available on this waypoint · Why?" link. Why? opens a popover with each task's exact reason from `Capabilities()` and its docs link (reuse `amTaskLinks`/`amOfficial`). Rate limiting must never disappear just because the current waypoint cannot do it; the reason (for example "Needs an agentgateway waypoint or Solo Enterprise Istio") is the useful part.
  - **Do not show on this tab:** layer tags (L4/L7), Enforced by, Managed by, filter chips, status columns or descriptions of tasks that are not set. They belong in the drawer (Phase 4) and on the object page (Phase 7). Managed by appears in the drawer header when editing, and on Policies & routes (Phase 6).
  - **Multicluster footer:** a single full-width line under the grid, shown only when the configuration differs between connected clusters: "The DENY and the split only exist on kind-mesh1. On kind-mesh2, reviews has no configuration." with **Compare** (side-by-side per-cluster state with field diffs, reusing the Before/After rendering from `amPlanPanel`) and **Copy to <cluster> →** (opens the drawer in phase 2 for that cluster, using the `configure-clusters.js` per-cluster plan regeneration). The global-service task lives here as a link, "Publish as a global Service", when it is available.
  - **Cluster strip:** the slim strip from 1.5 (cluster segment, freshness, "Changes are written to <cluster>"). Compare lives in the multicluster footer, not in the strip.
  - **Edge cases:**
    - A task with several resources shows one item row each.
    - Resources that match no task (an EnvoyFilter not owned by Flightbay, an unrecognised policy shape) appear as an item row in the closest card with **Open** only and the label "Other configuration".
    - If more than one routing rule targets the Service, show the existing overlap warning from `amRoutingPanel` as that card's single hint.
    - Read-only (snapshot, or no write context): hide the Add footers and Edit links, and show "Read-only: open with --kubeconfig to change this" once in the strip.
  - *Acceptance:*
    - Every resource listed today by "Security policies for this Service" and "Routing for this Service" appears as an item row.
    - Every task in `Capabilities()` is reachable from exactly one card, as an item, an Add link, through More…, or as a locked label.
    - With bookinfo/reviews on kind-mesh1, the page fits in one 1600×1000 screen without scrolling.
    - Nothing on this tab writes to the cluster.
  - *Files:* new `web/ambient-service.js`. Retire `amConfigurationHome`, `amPoliciesPanel`, `amRoutingPanel`, `amTaskCards` and the "Add another capability" disclosure in v2.
- [ ] **3.3 Answer sentences.**
  - Generate them in Go (`internal/ambient/answers.go`) from policies and editable routes:
    - access: ALLOW and DENY combined into "Anyone in the mesh, except requests with `color: red`", "Only GET and HEAD from the mesh", "Only callers with a valid JWT from <issuer>", "Only the waypoint can reach the Pods"
    - routing: "80% to v1, 20% to v2" (name backends by their version suffix when they share the Service prefix; otherwise use the full name), "All traffic goes to reviews", "Requests with header x-test go to v2; everything else to v1"
    - transport: "Yes between mesh workloads. Non-mesh callers are still accepted." (PERMISSIVE), "Yes. Only mesh workloads can connect." (STRICT)
    - resilience: list what is set ("Times out after 5s. Failing Pods are ejected after 3 errors. Limited to 100 requests a second."), or the greyed "Nothing is set. No timeout, no limits, and failing Pods keep getting traffic."
  - Keep each sentence under about 90 characters. Code values go in `<code>`. No jargon such as "additive", "targetRefs" or "selector".
  - Also return a short `label` for each resource item ("Block header color = red", "All paths on port 9080", "mTLS permissive, from the mesh default").
  - *Tests:* sentence snapshots for at least eight combinations (open, ALLOW only, DENY only, ALLOW+DENY, L4 lock, JWT, split, nothing set).
- [ ] **3.4 Waypoint.**
  - The waypoint shows only in the header meta line (name plus readiness pill). Clicking the name opens a small popover: implementation, how it is selected, and **Change waypoint…** (the drawer `add:waypoint`).
  - When no waypoint is in use, the L7 Add links in each card are shown greyed with a lock icon, and the Access card's hint reads "L7 rules need a waypoint. **Set one up →**". After the waypoint drawer completes, return to the task the user first clicked.
- [ ] **3.5 No header Add button.** The Service header keeps **Test service** and **YAML** only. Adding happens from the card footers. **More…** opens the All tasks view (3.6) scrolled to that group.
- [ ] **3.6 Summary | All tasks toggle.** Mockups **01c** (Cards) and **01d** (List).
  - A segmented control above the cards: **Summary** (default, mockup 01) | **All tasks · N**. The choice is in the URL (`&cview=summary|all`) and remembered.
  - **All tasks** shows every task from `Capabilities()`, grouped under the five groups in the mapping table in 3.2. Each group heading is the question plus its one-line answer in grey.
  - Toolbar: "Find a task…" (filters by label and description), **Hide unavailable** chip, and **Show as Cards | List** on the right (remembered in `localStorage`).
  - **Cards:** three per row (two below 1280 px). Each card shows label, status pill (Configured, Not set, Recommended, Inherited, 🔒 Not available), a one-line description, for configured or recommended tasks one line of what is set now or why it is recommended, and actions (**Test** and **Edit**, **Add →**, **Override →**, or **Why?**). Configured cards have a teal left edge, the recommended card an amber one, and unavailable cards are greyed with their one-line reason.
  - **List:** one row per task with four columns: label plus description, status pill, what is set now (or the reason, greyed), actions.
  - No layer tags, Enforced by, Managed by or docs links in either view; they live in the drawer.
  - This is the "look at all config tasks" view, and it replaces today's task card grid (`amTaskCards`) and the "N tasks not available · why?" disclosure.
  - *Acceptance:* all 20 tasks appear exactly once in each view, in the same group as on Summary. Hide unavailable removes only locked tasks. Every Add, Edit or Override opens the task drawer.
- [ ] **3.7 What every Add, Edit and Override link opens.** All of them open the one-task drawer from Phase 4 (mockup **03**) over the current page, never a new page or the old stepper:
  - **Add →** / Add links → `drawer=add:<taskId>`, phase 1 "Choose settings" with defaults filled in. Example: "Allow or block methods" opens 03.
  - **Edit** → `drawer=edit:<editKey>` with the existing values loaded.
  - **Override →** → `drawer=add:<taskId>` pre-filled from the inherited value (for example STRICT instead of the mesh PERMISSIVE).
  - Hints such as **Block that →** → `drawer=add:<followUpTask>`.
  - Closing the drawer returns to the same view (Summary or All tasks) at the same scroll position, with the changed item updated.

---

## Phase 4: Task drawer (replaces the 4-step wizard)

Mockups **03** and **03b**.

**UX gate:** follow §0.5 for `03-simple-wizard` and `03b-apply-and-test` (03b needs a demo state; see `routes.json`). A task in this phase is not done until its screen passes the comparison loop and the independent review.

- [ ] **4.1 Drawer frame.**
  - A right-hand panel about 1060 px wide (full width under 1280 px) over the current page, with a shaded backdrop.
  - Header: "Add configuration to **ns/name** · cluster" (or "Edit …"), the task title, **Switch task ▾** (every task for this Service, grouped like the four Configuration cards) and ✕.
  - Phase bar: `1 Choose settings · 2 Check against the cluster · 3 Apply and test`, with the right-aligned state text "Draft saved · nothing written" / "Checked · nothing written" / "Applied to <cluster> at hh:mm:ss".
  - Body: two columns, a form on the left and an effect panel (about 440 px) on the right.
  - Footer: **Cancel**, a "Write to" cluster select plus "+ <other cluster>" chips, **Download YAML**, and one primary button whose label depends on the phase.
  - The drawer state is in the URL (`drawer=`). Closing it keeps the draft (reuse `amSaveConfigurationDraft`/`amResumeTaskDraft`), and reopening the same task resumes it.
  - *Files:* new `web/ambient-drawer.js`, CSS in `web/ambient.css`.
- [ ] **4.2 One task per drawer.**
  - Refactor the per-task fieldsets out of `amWizard()` (`web/ambient.js:200-233`) into a registry: `amTaskForms[taskId] = { render(F, svc), capture(F) }`. Reuse `amAccessRuleMarkup`, `amRouteMatchMarkup`, `amCircuitBreakerFields`, `amMethodChips`, `amHeaderRows`, resilience scope (`web/resilience.js`), and the global-service flow (`web/global-publication.js`) as they are.
  - `amRead()` still builds the same `ambient.Request`, with `tasks: [taskId]`, so the server is untouched.
  - The old multi-select grid and "Configure selected tasks" are not ported. If a task needs another (for example L7 without a waypoint), the drawer shows a callout with a button that opens the prerequisite task's drawer and returns afterwards.
  - *Acceptance:* every task in `Capabilities()` plus the modes `waypoint`, `enrol-namespace`, `endpoints` and `route-parent` opens in the drawer, and generates byte-identical plan YAML to the old wizard for the same inputs. Add a node test that compares `amRead()` output from both paths for each task's defaults.
- [ ] **4.3 Intent-first wording.** Each form's first control is a plain choice. Examples:
  - methods (drawer title "Allow or block methods"): "Only allow the methods I pick" (ALLOW) / "Block the methods I pick" (DENY), each with a one-line consequence
  - mTLS: "Require mesh mTLS" / "Allow non-mesh callers too"
  - weighted-routing: "Split by percentage" / "Send equal traffic"

  Rename technical options such as "additive ALLOW policy" into the sub-line. Keep the port selector, but when the Service has one port, show it pre-selected with "Only port on this Service".
- [ ] **4.4 Generated YAML.** Collapsed under "Generated YAML · N new object(s) · apiVersion" and kept live through `amLivePane`/`amLiveRender` (350 ms debounce). Make the resource name editable (default `<service>-<task>`, not `flightbay-test-<hash>-…`), and keep the `flightbay.dev/test-id` and `app.kubernetes.io/managed-by: flightbay` labels for ownership and undo.
  - *Backend:* `Request` gets an optional `name` field. `Generate` uses it if valid and unique, and `ReconcilePlan` keeps refusing name clashes with resources Flightbay does not own.
  - *Acceptance:* the generated name for a new methods policy on reviews is `reviews-methods`, a clash produces a clear inline error, and the follow-up #7 edition kind (AgentgatewayPolicy vs EnterpriseAgentgatewayPolicy) shows in the YAML summary line.
- [ ] **4.5 Effect panel: "What this does to <name>".**
  - *Backend:* extend the plan response with `effects[]` (plain sentences) and `interactions[]` (`{level: info|warn, text, followUpTask?}`), computed in a new `internal/ambient/effects.go` from the request plus the Service's current `View.Policies` and editable routes.
  - **Access rules to implement first:**
    - first ALLOW on a target → warn "Once it exists, anything it does not match is denied", and note that "Deny by default" is then unnecessary for L7
    - DENY evaluated before ALLOW → info naming the existing DENY
    - L7 policy without a ztunnel bypass block → info with `followUpTask: "require-waypoint"`
    - policy targets the waypoint but the Service does not use it → warn
  - **Routing rules:**
    - a new rule on an existing route → "Adds rule N to <route>"
    - overlap with another route → warn
    - a backend with no ready endpoints → warn
  - Render effects as a bullet list. Render interactions as callouts with a button when `followUpTask` is set (it opens that task's drawer).
  - *Tests:* `internal/ambient/effects_test.go` covering each rule.
- [ ] **4.6 Generated test preview.** Under the effect panel, "Tests that will run after apply", listing the cases from Phase 7's generator (`expect 200` / `expect 403` pills, method and path, short note). If Phase 7 is not built yet, hide this block. Do not fake it.
- [ ] **4.7 Phase 2: Check against the cluster.**
  - The primary button **Check against cluster →** runs `POST api/ambient/test {operation:"validate"}` for each selected cluster. That is preflight plus dry-run (`internal/server/ambient.go:498-561`).
  - Show a progress list: "Cluster identity matches", "Server dry-run passed · N create · N update · N delete".
  - On success the state becomes **Checked** and the primary button becomes **Apply to <cluster> →**. On failure, show the server message inline and keep the user in phase 1 with the form intact.
  - Apply still opens the existing confirmation modal (`amConfirmApply`, `web/global-publication.js:174`), which keeps context, API server, plan id and YAML.
  - *Acceptance:* closes follow-up #2. There is only ever one apply button on screen, and Checked looks visibly different from Applied.
- [ ] **4.8 Phase 3: Apply and test** (mockup **03b**).
  - After `operation:"apply"`, the progress list continues: "Applied", "Waypoint picked it up" or "Route accepted" (poll `operation:"status"` and the live mirror through `amRefreshAppliedConfiguration`), then "Tests N/M" once Phase 7 exists.
  - The left column shows the test results table: Request, Expected, Got, Decided by, ✓/✕. Failures get a red callout with links to the relevant policy and log line.
  - The right column, "Done. What now?", has:
    - **Apply the same to <other cluster> →** (reruns phase 2 for that cluster through the `configure-clusters.js` per-cluster plan regeneration)
    - recommended follow-up task(s) from `interactions[]`
    - **Run the tests again**
    - **Undo this change** (`operation:"cleanup"`)
  - Footer: **Close**, **View change in Changes**, primary **Back to <service>**.
  - For the UX comparison loop only: in the browser preview (where applies are disabled), `&dphase=3&demo=1` renders phase 3 with canned progress and test results so it can be screenshotted. Never enable `demo=1` outside the preview harness.
  - Multi-cluster: apply clusters one at a time as today, and show a strip with one row per cluster (Applied / Pending / Failed). If cluster 1 applied and cluster 2 failed, say so at the top. Do not hide it in a sentence.
  - *Acceptance:* closes follow-up #2's partial-apply concern. When the drawer is closed, the item row on the Configuration tab shows the new state (Live, or Verified if tests passed).
- [ ] **4.9 Edit mode.**
  - `drawer=edit:<editKey>` loads the editable entry (`edit: {ref, rule, uid, fingerprint}`), shows only that task's fields pre-filled, and labels the header "Edit <task> · <resource> rule N".
  - The effect panel shows field-level before and after, reusing the `changes[]` JSON-pointer diff from the plan.
  - Keep "Edit other fields" (from the recording) as a link that expands the remaining fields of the same resource.
- [ ] **4.10 Fix mode.** `drawer=fix:<checkId>` maps a failed check's remedy (`data-amremedy`, `web/ambient.js:444`) onto a task id or mode (`endpoints`, `route-parent`, `waypoint`, `require-waypoint`, …) and opens it pre-filled. Remedies with no task mapping open the Checks tab with the runbook commands instead.

---

## Phase 5: Service · Checks (was Investigate)

Mockup **01b**.

**UX gate:** follow §0.5 for `01b-service-checks`. A task in this phase is not done until its screen passes the comparison loop and the independent review.

- [ ] **5.1 Reuse the investigation data** from `GET api/ambient/investigate` (`amInvestigation`, `web/ambient.js:83`), rendered in the new layout inside the Service page. The cluster strip comes from 1.5.
- [ ] **5.2 Problems first.** A segmented control `Problems N | All N`, defaulting to Problems (Failed, Check, Unknown). Each problem row shows:
  - status pill, title, a one-sentence detail
  - area (Access · ztunnel, Waypoint, Routing, Data plane, DNS)
  - **Evidence** and a primary **Fix: <short task label>…** (→ drawer `fix:`), or **Runbook** when no task maps
- [ ] **5.3 Evidence chips.** An expanded problem shows "What Flightbay looked at" as a row of chips (object kind plus name, or a short fact), built from the step's evidence. This replaces the separate "View evidence / Check details · captured fields" screen. Keep the full field view one click away from a chip.
- [ ] **5.4 Grouped passes.** Under "PASSED · N checks, grouped", show one row per area: Data plane, Waypoint, Access, Routing, DNS. Each has a one-line summary and "N checks ▸" to expand.
  - *Backend:* if steps carry no area, add `area` to the investigation step struct in `internal/ambient/investigate.go`.
- [ ] **5.5 DNS verdict.** One DNS row with a single state: Resolves, Not tested or Fails, with the address. Use the same chip anywhere DNS appears in v2. *Acceptance:* closes follow-up #8.
- [ ] **5.6 Official troubleshooting commands and docs.** Keep "Run in Flightbay terminal" and the docs links, but move them into the expanded problem row, not a separate section.
- [ ] **5.7 Endpoint recovery** (follow-up #5). In the endpoints remedy, split Pods into "Matches selector" and "Does not match", and do not offer "Collect missing evidence" when that evidence is already present.

---

## Phase 6: Ambient · Policies & routes

Mockup **02**.

**UX gate:** follow §0.5 for `02-configuration-inventory`. A task in this phase is not done until its screen passes the comparison loop and the independent review.

- [ ] **6.1 Backend.** Add `GET api/ambient/policies?namespace=&type=&problems=&drift=&managedBy=&group=object|service`. Each row:
  - `ref`, `kind`
  - `appliesTo` (a plain phrase plus a structured target: Service, waypoint, Pods selector, Namespace, Whole mesh, or Targets nothing with a reason)
  - `effect` (short phrase plus an action pill)
  - `enforcedBy`
  - `perCluster{cluster→applied|accepted|programmed|missing|differs|not-enforced}`
  - `managedBy`
  - `services[]` (which Services it reaches)

  **Kinds:** AuthorizationPolicy, PeerAuthentication, RequestAuthentication, HTTPRoute, DestinationRule, waypoint Gateways, AgentgatewayPolicy/EnterpriseAgentgatewayPolicy that target a waypoint, and Flightbay-owned EnvoyFilters.

  Reuse the classification in `servicePolicies()` (`internal/ambient/waypoint_binding.go:210-316`) and the drift comparison from 3.1. Add a "Targets nothing" detector for targetRefs that point at a missing Service or Gateway, and for selectors that match no Pods.
  - *Tests:* `internal/ambient/policies_test.go`.
- [ ] **6.2 UI.**
  - Filter bar: text filter, Scope chips (namespaces), Type chips (Access, Routing, Resilience, Transport, Waypoints), Show chips (Only problems, Only drift, Managed by Flightbay), and the segmented control By object | By Service.
  - Table columns: Kind/name, Applies to, Effect, Enforced by, one column per cluster, Managed by, actions (**Test** for AuthorizationPolicy/HTTPRoute, **Diff** when Differs, **Explain** when Not enforced, **Open** always).
  - Rows are grouped by Service. Namespace-wide and mesh-wide objects go in their own groups. A group header opens the Service page.
  - **Export YAML** downloads the filtered set.
  - *Acceptance:* replaces the "Existing configuration" table hidden inside the Attachment map (`amEditors`, `web/ambient-map.js:161`). Every object `servicePolicies()` returns for any Service appears here.

---

## Phase 7: Tests per AuthorizationPolicy and HTTPRoute

Mockups **04**, **05**, and the test parts of **03** and **03b**.

**UX gate:** follow §0.5 for `04-authorizationpolicy-tests` and `05-httproute-tests`. A task in this phase is not done until its screen passes the comparison loop and the independent review.

- [ ] **7.1 Suite storage.**
  - Files live at `~/.flightbay/ambient-suites/<cluster>/<group>/<Kind>/<ns>/<name>.json`:
    ```jsonc
    { "target": Ref, "source": {"kind":"workload|ephemeral|external","ref":Ref}, "cases": [{
        "id":"…","label":"Rule matches","generated":true,
        "request":{"method":"GET","path":"/reviews/0","headers":{"color":"red"},"port":9080,"direct":false},
        "expect":{"status":403} | {"rule":1,"backend":"reviews-v1"} | {"weights":{"reviews-v1":80,"reviews-v2":20},"tolerance":5,"samples":200}
      }], "lastRun": {...} }
    ```
  - Endpoints: `GET api/ambient/suites?ref=`, `POST api/ambient/suites` (save), `POST api/ambient/suites/run` (run, returns results).
  - *Files:* `internal/server/ambient_suites.go`, `internal/ambient/suites.go`.
- [ ] **7.2 Case generator for AuthorizationPolicy** (`internal/ambient/testgen_authz.go`). From each rule, generate:
  - one request that matches (expect 403 for DENY, 200 for ALLOW)
  - near misses: a different header value, the header absent, another method, another path
  - one interaction case per other policy on the same target (for example a DENY combined with a method the ALLOW rejects)
  - a **waypoint bypass** case (`direct: true`, sent to a Pod IP), whose expected result depends on whether a ztunnel lock exists

  Supported matchers in v1: `to.operation.methods/paths/ports`, `when request.headers[...]`, `from.source.principals/namespaces`. For principals and namespaces, pick a source workload that matches and one that does not, from the captured workloads. Anything unsupported produces no case, plus a note "Not generated: <field>".
  - *Tests:* table tests per matcher.
- [ ] **7.3 Case generator for HTTPRoute** (`internal/ambient/testgen_route.go`).
  - Per rule: one request per match. Expect "rule N", backend set.
  - A negative case: the same path to a backend Service directly. Expect "not this route".
  - For weighted rules: a weights case (200 samples, ±5 %).
  - Timeouts, retries and mirrors each add a case where they can be measured (timeout: only if the backend supports a delay header; otherwise mark it "not generated").
- [ ] **7.4 Runner.** Reuse the existing runners in `internal/server/http_mesh_probe.go` and `http_mesh_ephemeral.go` (in-mesh curl from an existing Pod, or a temporary Pod) through `api/ambient/http-mesh-test`.
  - **Identify the backend** from response headers or body when available, otherwise from the responding Pod name (today's results already group responders by cluster and version; reuse that code).
  - **Decided by**, best effort, in this order:
    1. waypoint access-log RBAC details (`rbac_access_denied_matched_policy[ns[...]-policy[...]-rule[...]]`, parsed with `internal/flows/envoy.go`) from the live log stream within ±5 s of the request
    2. `reach.Explain` (`internal/reach/explain.go:116`)
    3. otherwise "unknown"
  - Never block a result on Decided by.
- [ ] **7.5 Object page** for AuthorizationPolicy and HTTPRoute (`obj=` route). Header: kind badge, name, action pill, applies to, enforced by, status pills, managed by, then **YAML**, **Edit** (drawer), **Undo** (only when Flightbay owns it). Tabs: Summary · Tests (n) · Explain a request · History.
  - **AuthorizationPolicy Tests (04):**
    - left column: "The rule, in words" (reuse 3.3 sentence code), "Other policies that also decide for <service>", the "Send test requests from" source chips, "Run on" cluster chips, and a note that suites re-run after changes
    - right column: a Test cases table (status, Request, Why this case, Expect, Got, Decided by, Log link) with **+ Add case**, **Generate from rule** and primary **▶ Run all**
    - failures get callouts with **Edit rule** and **Mark <got> as expected**
  - **HTTPRoute Tests (05):**
    - a per-rule card with an expected vs observed bar per backend, a sample-size select and **▶ Run**
    - a Match cases table
    - "Other checks on this route": backendRefs resolve with ready endpoints, no overlapping route, present in other clusters (with **Copy →** to apply there), and hints for adding timeout or retry
  - *Acceptance:*
    - "Test" on an item row (3.2) or in Policies & routes (6.2) opens this tab.
    - The existing "Test this rule" pre-fill (`amTestAccessRule`, `web/ambient-configure.js:155`) becomes a generated case.
    - Results persist across reloads.
- [ ] **7.6 Service · Tests tab.** Lists every suite for objects that affect the Service: name, kind, last run, pass count, **Run**. Add **Run all**. An empty state explains that suites are created when you add configuration or click Generate on a policy or route.
- [ ] **7.7 Auto-run after apply.**
  - After a successful apply from the drawer, run the suite for the changed object and every other suite on the same Service, and show the results in drawer phase 3 (03b).
  - The Changes record gets `verified: true|false` and a link to the run.
  - The Configuration tab and Changes show **Verified** or **Failed** accordingly.
  - Let the user opt out with a checkbox in the drawer footer ("Run tests after apply"), which stays on by default.
- [ ] **7.8 Link the drawer preview.** 4.6 shows the cases the generator will create for the drafted resource (call the generator on the plan's objects through `POST api/ambient/suites/preview`).

---

## Phase 8: Map and Request path: keep what exists

Tom prefers today's Attachment map and today's Request path tracer to any redesign. **Do not redesign either of them.** Keep their layout, visuals and controls. The only work here is wiring them into the new structure and fixing known bugs.

- [ ] **8.1 Map tab.** Keep `amClusterCanvas` (`web/ambient-map.js:5`), `amGraph` (`:115`) and every existing control (Cards/List, Expand/Compact, zoom, Fit, Reset layout, Full screen, All namespaces), the east-west and peer gateway cards, the connection chips and the "Routes, policies and workloads for X" disclosure with its **Existing configuration** table (`amEditors`, `:161`).
  - Rename the tab label from "Attachment map" to **Map**.
  - Retarget the Service card buttons to the new Service page: **Configure** → `svc=…&stab=configuration`, **Investigate** → `svc=…&stab=checks` (relabel the button **Checks**), **Test** → `svc=…&stab=tests`, **Request path** → `#ambient?tab=path&to=<service>`. **YAML** stays.
  - **Edit existing** in the Existing configuration table opens the task drawer (`drawer=edit:<editKey>`) over the map rather than jumping to the old wizard.
  - Fix the dropped-line behaviour in `amRoute` (follow-up #3). Never omit a relationship because two cards overlap; offset or reroute it instead.
  - *Acceptance:* the Map looks the same as today apart from the tab label and the Checks button label. No relationship is dropped at any zoom level. Every card action lands on the new Service page or drawer.
- [ ] **8.2 Request path tab.** Keep `request-path.js` (`:19`) and `request-path-map.js` exactly as they look today: "How does one application reach another?", From application → To Service, Address to use, Port, Optional request details, **Trace request path**, the Steps / Map result views and the Configuration, Traffic and Policies evidence tabs.
  - It stays a top-level Ambient tab (`#ambient?tab=path`).
  - The Service page's **Request path** tab renders the same component with **To Service** pre-filled with that Service. It is not a different design.
  - Buttons that today say "Trace request path →" keep doing that, routed to `tab=path&to=…`.
  - Where the trace finds a policy or route problem, add a link to open the task drawer for that Service (`drawer=fix:` or `drawer=edit:`), next to the existing evidence. This is the only visible addition.
  - *Acceptance:* a before and after screenshot of a bookinfo trace look the same apart from the added drawer link.

---

## Phase 9: Changes and HTTP results

- [ ] **9.1 Changes (home and Service tabs).**
  - Restyle `amTests` (`web/ambient.js:271`) with the §0.6 vocabulary, and add a Verified/Failed column linked to the test run.
  - The Service tab filters to that Service.
  - Keep Undo, Status, Forget and bulk select.
  - Show partial multi-cluster applies as one change with per-cluster rows.
- [ ] **9.2 HTTP results** (follow-up #1). In the Test service modal and the suite results, lead with the connection actually tested ("Backend Pod only · mesh path not taken" for port-forward modes) before the hostname.

---

## Phase 10: Switch over, clean up, document

- [ ] **10.1 Make v2 the default.** Remove the flag. Apply the back-compat redirects from 1.2 permanently. Point Traffic's "Review in Ambient wizard →" (`web/traffic.js:947`, `1113`) and Scorecard Fix (`web/app.js:1078-1093`) at `#ambient?svc=…&drawer=…`. Do not change those pages otherwise.
- [ ] **10.2 Delete the dead code:** `amLanding`, `amPicker`, `amSteps`, `amConfigurationNav`, `amTaskCards`, the phase logic in `amWizard`, `amConfigurationHome`, `amPoliciesPanel`, `amRoutingPanel`, `amWaypointPanel`, and the "Add another capability" markup, plus any CSS that only they used. `web/ambient.js` should shrink noticeably. Keep functions the drawer reuses.
- [ ] **10.3 Update the tests.**
  - Update or replace node tests that assert on old markup: `ambient-p1`, `ambient-edit`, `review-apply`, `configure-clusters`, `global-publication`, `global-service-setup`, `resilience-controls`, `http-policy-intent`.
  - Update `internal/server/ambient_browser_test.go` to drive the v2 flow: Overview → reviews → Configuration → + Add methods → Check → Apply → tests pass → Undo.
- [ ] **10.4 Update the docs:** `docs/site/content/desk/ambient.md` (rewrite the UX sections and the task table to match the question cards and drawer), `docs/ambient.md` (fix the helper-workload contradiction), `docs/site/content/desk/ambient-resilience.md` (the drawer instead of the wizard). Tick the closed items in `docs/ambient-follow-up-tasks.md`.
- [ ] **10.5 Live verification on kind-mesh1 and kind-mesh2:**
  - every task applies and undoes from the drawer
  - a split shows correct observed weights
  - a DENY suite passes
  - the bypass case behaves correctly with and without `require-waypoint`
  - Compare shows drift when a resource exists on only one cluster
  - snapshot mode (an opened bundle) shows everything read-only, with write actions disabled and explained

  Record before and after screenshots of each mockup screen next to the mockup PNG in the PR description.

---

## Definition of done (whole project)

- [ ] Every screen in `docs/ux/ambient/` (00, 01, 01b, 01c, 01d, 02, 03, 03b, 04, 05) passes `ux_compare.py --all` with no MUST failures, has a side-by-side PNG in its PR, and has been through the independent review. Every remaining difference other than data is listed in `DEVIATIONS.md`. Pixel-perfect is not required; the element order, control types, wording and visual weight are.
- [ ] From the Ambient sidebar entry, a user can reach the Configuration tab of any mesh Service in **two clicks** (Overview → row) and apply a change in **one drawer**, with one apply button.
- [ ] For any Service, the Configuration tab shows every task in `Capabilities()` exactly once, plus every live resource that affects it, with Managed by and per-cluster state.
- [ ] Every AuthorizationPolicy and HTTPRoute has a Tests tab with generated cases, expected results, pass/fail and saved suites. Suites re-run after Flightbay applies to that Service.
- [ ] The status words in §0.6 are used consistently, and Checked never looks like Applied.
- [ ] No change to non-Ambient pages beyond retargeting links into Ambient.
- [ ] `make test` is green, the browser test drives the v2 flow, and the live kind checks in 10.5 pass.
- [ ] Follow-up items 2, 3, 4, 6, 7, 8 and 10 in `docs/ambient-follow-up-tasks.md` are ticked, and items 1, 5 and 9 are ticked or have a written note on what remains.

## Suggested order and sizing

| Phase | Depends on | Rough size |
|---|---|---|
| 0 Prep, bug fix | none | S |
| 1 Shell and routing | 0 | M |
| 2 Overview | 1 | M |
| 3 Configuration tab | 1 | L (backend + UI) |
| 4 Task drawer | 3 | XL (refactor of `amWizard`) |
| 5 Checks | 1, 4.10 | M |
| 6 Policies & routes | 3.1 (drift, managedBy) | M |
| 7 Tests | 4, 6 | XL |
| 8 Map and Request path: keep, small fixes | 1 | S |
| 9 Changes, HTTP results | 4, 7 | S |
| 10 Switch over | all | M |

Ship each phase as its own PR behind the flag, so Tom can try it with `?amv2=1` while the old UI keeps working.
