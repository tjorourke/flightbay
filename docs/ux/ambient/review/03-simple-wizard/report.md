# 03-simple-wizard

- URL: `http://127.0.0.1:47800/?token=ambient-ui-test#ambient?svc=kind-mesh2/bookinfo/reviews&stab=configuration&drawer=add:methods`
- Mockup: `/Users/thomasorourke/code/solo/flightbay/docs/ux/ambient/03-simple-wizard.png`
- Side by side: `/Users/thomasorourke/code/solo/flightbay/docs/ux/ambient/ux-review/03-simple-wizard/side-by-side.png`

| Result | Severity | Role | Label |
|---|---|---|---|
| ok | must | tab | Configuration |
| ok | must | tab | Checks |
| ok | must | tab | Tests |
| ok | must | tab | Request path |
| ok | must | tab | Changes |
| ok | must | heading | Allow or block methods |
| ok | must | button | Switch task ▾ |
| ok | must | button | ✕ |
| ok | must | step | Choose settings |
| ok | must | step | Check against the cluster |
| ok | must | step | Apply and test |
| ok | must | step | Draft saved · nothing written |
| ok | must | label | What should happen? |
| ok | must | label | Methods |
| ok | must | chip | ✓ GET |
| ok | must | chip | ✓ HEAD |
| ok | must | chip | POST |
| ok | must | chip | PUT |
| ok | must | chip | PATCH |
| ok | must | chip | DELETE |
| ok | must | chip | OPTIONS |
| ok | must | label | Port |
| ok | must | label | Callers this applies to |
| ok | must | heading | What this does to reviews |
| ok | must | heading | Tests that will run after apply |
| ok | must | button | Cancel |
| ok | must | button | Download YAML |
| ok | must | button | Check against cluster → |

Labels the app shows that are not in the mockup are not detected by this script.
Compare the side-by-side image for extra elements, layout, spacing and visual weight.
