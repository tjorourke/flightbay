# 04-authorizationpolicy-tests

- URL: `http://127.0.0.1:47800/?token=ambient-ui-test#ambient?obj=kind-mesh2/AuthorizationPolicy/bookinfo/flightbay-test-f4a039e06a0956fc-deny-rule&otab=tests`
- Mockup: `/Users/thomasorourke/code/solo/flightbay/docs/ux/ambient/04-authorizationpolicy-tests.png`
- Side by side: `/Users/thomasorourke/code/solo/flightbay/docs/ux/ambient/ux-review/04-authorizationpolicy-tests/side-by-side.png`

| Result | Severity | Role | Label |
|---|---|---|---|
| ok | should | pill | DENY |
| ok | must | button | YAML |
| ok | must | button | Edit |
| MISSING | must | button | Undo |
| ok | must | tab | Summary |
| ok | must | tab | Tests |
| ok | must | tab | Explain a request |
| ok | must | tab | History |
| ok | must | heading | The rule, in words |
| MISSING | must | heading | Other policies that also decide for reviews |
| ok | should | pill | ALLOW |
| ok | must | heading | Send test requests from |
| ok | must | chip | Temporary pod |
| ok | must | chip | Outside the mesh |
| ok | must | heading | Run on |
| ok | must | heading | Test cases |
| ok | must | button | + Add case |
| ok | must | button | Generate from rule |
| ok | must | button | ▶ Run all |
| ok | must | column | Request |
| ok | must | column | Why this case |
| ok | must | column | Expect |
| ok | must | column | Got |
| ok | must | column | Decided by |
| ok | must | button | Log |

Labels the app shows that are not in the mockup are not detected by this script.
Compare the side-by-side image for extra elements, layout, spacing and visual weight.
