# Deviations from the mockups

Every difference that is not data and is accepted must be listed here (PLAN.md §0.5 step 10).

| Screen | Difference | Reason |
|---|---|---|
| all | App shell (sidebar, top bar, snapshot banner, native cluster select, theme toggle) differs from the mockup's simplified shell | Out of scope: the plan changes the Ambient page only |
| all | The comparison renders the light theme. The dark theme maps the same classes onto the dark tokens | The app ships both themes; the mockups are light |
| 00 | Namespace chips are data (top three namespaces by Service count, then `+ N`), so `kagent`, `agentgateway-system` and `+ 3` in the contract do not match every fixture | Chips are generated from the namespaces in the capture |
| 00 | Needs attention rows come one per failed check and the detail text is the check's own sentence | Data |
| 01, 01c, 01d | Add links and Edit stay visible in read-only (snapshot or no matching write context); the strip says "Read-only: open with --kubeconfig to change this" and the drawer disables Apply. PLAN 1.5 says disable and 3.2 says hide: the plan contradicts itself, and keeping them lets a reader draft and download YAML | Plan inconsistency; keeps layout identical to the mockup in the preview |
| 01 | The Summary multicluster footer does not carry a "Publish as a global Service" link | The mockup footer has only Compare and Copy; the task is reachable from All tasks |
| 01 | Add links use the task short labels from the mockup ("Allow or block methods"), not PLAN 3.2's example "Allow only some methods" | Mockup is the authority |
| 01c, 01d | Unavailable-task reasons show the first sentence; the full reason is in the Why? popover (no docs link yet) | One line per the mockup |
| 03 | "Callers this applies to" loads existing caller restrictions and offers captured service accounts and an explicit Everyone choice | Tom approved caller controls for the wizard fixes; restricted policies must not load as Everyone |
| 03 | The resource name is not an input; the plan request accepts `name` and the default is `<service>-<task>` | The mockup shows no input, only the sentence "Name is editable" |
| 03 | The Check button stays enabled in read-only and reports the missing connection inline | Lets a reader draft and download YAML |
| 03b | "Service UID unchanged" is only appended to the identity row when editing a resource | The check only has a UID for existing resources |
| 01b | The expanded problem row also holds a collapsed "Official troubleshooting commands" disclosure and docs links | PLAN 5.6 moves them into the expanded row; dropping them would lose the runbook |
| 01b | Header meta and strip differ from 01 (workload count, "in ambient", Compare in the strip, Problems | All) | The two mockups draw them differently; each screen follows its own mockup |
| 01b | DNS shows "DNS not tested" with the captured names | The capture has no resolution result; Flightbay does not invent one |
| 02 | Scope chips: selected scopes show "ns ✕"; with none selected one "All namespaces ▾" chip opens the list | The mockup shows only the selected chip |
| 00, 01b, 02, 04, 05 | The comparison fixture is a capture of two kind clusters, not the mockup data. Contract entries that exist only because of the mockup data (namespace chips, evidence chips, the Diff and Explain buttons, Undo, "Other policies" heading) are reported MISSING by `ux_compare.py` | They are data, not structure. Each was checked against the live clusters or the unit tests |
| 01 | Overview "⋯" menu (DNS names, Implementation details, View object, Bookinfo, Refresh) from PLAN 1.3 and 1.4 is not built; DNS is on Checks, YAML on the header and the Bookinfo demo has its own header button (next row), as well as the Overview empty-state link | The mockups have no such menu |
| 03b | The Tests line also says "N could not be checked" (and a case that could not be checked shows "?" with an amber callout) when Flightbay could not tell a result, for example which backend answered; such a run clears the change's verdict instead of recording Failed | Tom, 2026-10-22: an unattributed `/health` read as "1 did not match what we expected" and marked a working split Failed |
| 03 | Check on a draft that changes nothing shows a neutral note ("Nothing to change: kind-mesh1 already has this configuration.") instead of the server's refusal in red, and a cluster that already matches is left out of a multi-cluster check | Tom, 2026-10-22: reopening Edit split after applying it showed "this preview contains no changes to apply" as an error |
| header | A **Bookinfo demo** button sits left of Docs ↗ on every home tab and is highlighted while the demo page is open. The Namespaces button, Bookinfo demo and Docs ↗ wrap together, right-aligned, when the tabs fill the row | Tom, 2026-10-22: with mesh Services captured the demo had no entry point at all, because the "⋯" menu was never built. He chose a header button over the menu |
| 01 | The Configuration item rows show the action pill only; Live and Verified are on the Changes tab | The mockup item row has no state pill |
| 03 | The "Edit other fields" link (PLAN 4.9) is not built | Not in the mockup |
| 00, 02 | Namespace chips and Scope chips are replaced by one **Namespaces** picker at the right of the home tabs, shared by Overview, Policies & routes, Map and Request path | Real data volume (27 namespaces): Tom could not multi-select or leave a namespace with the chips (REVIEW-2026-10-20 R1) |
| 00 | Needs attention has one row per Service with a check count, is paged (5 rows by default) and uses an 8 px status dot instead of the ✕ pill | Real data volume: 18 rows of red ✕ pills, with one fault reported twice (R2) |
| 00 | The Services table is paged at 25 rows and, when the scope holds more than 25 Services, hides those with nothing configured behind a checkbox | Real data volume (139 Services) (R2.6, R2.7) |
| 02 | The filter bar is search, one Type dropdown, "Problems only" and "A column per cluster". The Type and Show pills, "Only drift", "Managed by Flightbay" and the By object / By Service toggle are gone | Too many controls on one row (R3.2) |
| 02 | No group header rows; one dense row per object sorted by what it applies to. One Clusters cell with a dot per cluster replaces a column per cluster (shown again by "A column per cluster"). Enforced by moves to the object header | Real data volume: 20 one-object group headers (R3.3) |
| 02 | Waypoint Gateways are not listed; the subtitle links to the Map | Tom: waypoints belong on the Map (R3.1) |
| 02 | Per-cluster words are Live, Applied, Failed, Not enforced, Differs and Not here (mockup shows Applied, Accepted, Programmed, Missing) | PLAN §0.6 vocabulary, applied consistently (R3.7) |
| header | The Policies & routes tab has no count | A number that changed meaning between pages (R9.4) |
| Map | Scoped, compact cards, collapsed namespaces, waypoint nodes and an in-map search box replace "Search objects ⌘K" | Real data volume (R4) |
| 04, 05 | The object page tabs are Summary · Tests · History; "Explain a request" is folded into Tests as a collapsible **Try a request** panel | It was a weaker copy of Add case and did not explain anything (R7) |
| 04, 05 | Case rows carry Edit, Duplicate and Delete (on hover); "+ Add case" opens a side panel rather than a modal | Full case editor (R6.1) |
| 04, 05 | Run becomes Stop while a run streams; the observed bars grow during the run and a progress bar shows requests sent | Live results (R6.3) |
| 04 | "Other policies that also decide" always shows, with a "None" sentence when there are none | Restored block (R6.5) |
| 05 | "Run on" chips also appear on the HTTPRoute page, under Other checks | Lets a reader switch to the cluster where the route exists |
| 00, 02 | Every table has sortable column names, a filter row under the names and a pager with a rows-per-page choice; the Services "Filter Services…" box and the policies name box are replaced by the column filters | Tom asked to sort and filter on column names and to page rather than "Show all" |
| 02 | Rows for ambient enrolment, waypoint use and global publication (label-based configuration) and inherited mesh-wide policies are listed, with Configure / Settings actions | Tom: "where is configuration?" |
| Map | Waypoint lines are drawn only after **Show its Services** on the waypoint card | Always-on lines ran round the cluster boxes and made the map hard to read |
| 03 | "Write to" shows the live connection for the cluster, or only the kubeconfig contexts that reach a connected cluster | It listed every context in the kubeconfig |
| 01, 01c, 01d, 03 | Two new tasks: Allow one service account, and Mesh-wide STRICT mTLS (also in Namespace settings) | Tom approved both tasks for the config wizard bug specification |
| 03 | JWT token subjects and optional workload principal/service-account controls; explicit append/full-policy replacement choice and existing-policy picker; exact before/after drawer diff | Tom approved the UI required by the config wizard bug specification; existing components and flow are retained |
| 01b, 03, 03b, 04 | Policy summaries qualify additive ALLOW behaviour; checks warn about downstream waypoint identity; denial tests can show connection refused or reset instead of 403 | Correct enforcement-layer semantics required by the approved bug specification |
| all affected screens | Visual comparison commands, per-screen approval gates and independent screenshot review were skipped | Explicit instruction from Tom for this job. Visual verification remains UNVERIFIED |
| 01b | All-tasks cards add an icon tile per task, an L4/L7 tag, a gradient surface and button-style Add / Edit / Test links | Tom: the cards looked plain. Same data and wording; visual weight only. Not compared against the mockup |
| 03 | The drawer says "Copy configuration of … from X to Y" and shows the Write to connection of the cluster(s) being written, not the source | Copying to another cluster showed the source cluster's connection |
