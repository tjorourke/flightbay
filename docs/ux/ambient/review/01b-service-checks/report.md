# 01b-service-checks

- URL: `http://127.0.0.1:47800/?token=ambient-ui-test#ambient?svc=kind-mesh2/bookinfo/reviews&stab=checks`
- Mockup: `/Users/thomasorourke/code/solo/flightbay/docs/ux/ambient/01b-service-checks.png`
- Side by side: `/Users/thomasorourke/code/solo/flightbay/docs/ux/ambient/ux-review/01b-service-checks/side-by-side.png`

| Result | Severity | Role | Label |
|---|---|---|---|
| ok | must | button | Test service |
| ok | must | button | YAML |
| ok | must | tab | Configuration |
| ok | must | tab | Checks |
| ok | must | tab | Tests |
| ok | must | tab | Request path |
| ok | must | tab | Changes |
| ok | must | toggle | Compare |
| ok | must | toggle | Problems |
| ok | must | toggle | All |
| ok | must | button | Evidence |
| ok | must | button | Fix: block bypass… |
| MISSING | must | chip | flightbay-test-d7c9…-deny-rule · waypoint |
| MISSING | must | chip | No ztunnel AuthorizationPolicy selecting |
| MISSING | must | chip | Test case "waypoint bypass" returned 200 |
| ok | must | button | 5 checks ▸ |
| ok | must | button | 6 checks ▸ |
| OUT OF ORDER | must | button | 4 checks ▸ |
| MISSING | must | button | 3 checks ▸ |

Labels the app shows that are not in the mockup are not detected by this script.
Compare the side-by-side image for extra elements, layout, spacing and visual weight.
