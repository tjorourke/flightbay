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
| 03 | "Callers this applies to" has the single option Everyone | The methods task has no caller field in the plan request. Callers are set with Allow or deny requests |
| 03 | The resource name is not an input; the plan request accepts `name` and the default is `<service>-<task>` | The mockup shows no input, only the sentence "Name is editable" |
| 03 | The Check button stays enabled in read-only and reports the missing connection inline | Lets a reader draft and download YAML |
| 03b | "Service UID unchanged" is only appended to the identity row when editing a resource | The check only has a UID for existing resources |
