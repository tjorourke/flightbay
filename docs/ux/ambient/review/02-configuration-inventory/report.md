# 02-configuration-inventory

- URL: `http://127.0.0.1:47800/?token=ambient-ui-test#ambient?tab=policies&ns=bookinfo`
- Mockup: `/Users/thomasorourke/code/solo/flightbay/docs/ux/ambient/02-configuration-inventory.png`
- Side by side: `/Users/thomasorourke/code/solo/flightbay/docs/ux/ambient/ux-review/02-configuration-inventory/side-by-side.png`

| Result | Severity | Role | Label |
|---|---|---|---|
| ok | must | tab | Overview |
| ok | must | tab | Policies & routes |
| ok | must | tab | Map |
| ok | must | tab | Request path |
| ok | must | tab | Changes |
| ok | must | button | Docs ↗ |
| ok | must | heading | All policies and routes |
| ok | must | button | Export YAML |
| ok | must | chip | Access |
| ok | must | chip | Routing |
| ok | must | chip | Resilience |
| ok | must | chip | Transport |
| ok | must | chip | Waypoints |
| ok | must | chip | Only problems |
| ok | must | chip | Only drift |
| ok | must | chip | Managed by Flightbay |
| ok | must | toggle | By object |
| ok | must | toggle | By Service |
| ok | must | column | Kind / name |
| ok | must | column | Applies to |
| ok | must | column | Effect |
| ok | must | column | Enforced by |
| ok | must | column | Managed by |
| ok | should | pill | DENY |
| ok | should | pill | Applied |
| OUT OF ORDER | should | pill | Missing |
| ok | must | button | Test |
| ok | must | button | Open |
| MISSING | should | pill | ALLOW |
| MISSING | should | pill | Differs |
| MISSING | must | button | Diff |
| MISSING | must | button | Explain |

Labels the app shows that are not in the mockup are not detected by this script.
Compare the side-by-side image for extra elements, layout, spacing and visual weight.
