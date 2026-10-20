# Deviations from the mockups

Every difference that is not data and is accepted must be listed here (PLAN.md §0.5 step 10).

| Screen | Difference | Reason |
|---|---|---|
| all | App shell (sidebar, top bar, snapshot banner, native cluster select, theme toggle) differs from the mockup's simplified shell | Out of scope: the plan changes the Ambient page only |
| all | The comparison renders the light theme. The dark theme maps the same classes onto the dark tokens | The app ships both themes; the mockups are light |
| 00 | Namespace chips are data (top three namespaces by Service count, then `+ N`), so `kagent`, `agentgateway-system` and `+ 3` in the contract do not match every fixture | Chips are generated from the namespaces in the capture |
| 00 | Needs attention rows come one per failed check and the detail text is the check's own sentence | Data |
