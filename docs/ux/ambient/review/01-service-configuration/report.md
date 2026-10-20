# 01-service-configuration

- URL: `http://127.0.0.1:47800/?token=ambient-ui-test#ambient?svc=kind-mesh2/bookinfo/reviews&stab=configuration&cview=summary`
- Mockup: `/Users/thomasorourke/code/solo/flightbay/docs/ux/ambient/01-service-configuration.png`
- Side by side: `/Users/thomasorourke/code/solo/flightbay/docs/ux/ambient/ux-review/01-service-configuration/side-by-side.png`

| Result | Severity | Role | Label |
|---|---|---|---|
| ok | must | button | Test service |
| ok | must | button | YAML |
| ok | must | tab | Configuration |
| ok | must | tab | Checks |
| ok | must | tab | Tests |
| ok | must | tab | Request path |
| ok | must | tab | Changes |
| ok | must | toggle | Summary |
| ok | must | toggle | All tasks |
| ok | must | heading | Who can call it? |
| ok | should | pill | DENY |
| ok | should | link | Test |
| ok | should | link | Edit |
| MISSING | should | link | Block that → |
| ok | should | link | Allow or block methods |
| ok | should | link | Require a JWT |
| ok | should | link | Allow or deny requests |
| ok | should | link | More… |
| ok | must | heading | Where does traffic go? |
| ok | should | pill | Split |
| ok | should | link | Route by header or path |
| ok | should | link | Mirror requests |
| ok | should | link | Balance endpoints |
| ok | must | heading | Is traffic encrypted? |
| MISSING | should | pill | Inherited |
| MISSING | should | link | Open |
| ok | must | heading | What happens under load or failure? |
| ok | should | link | Set a timeout |
| ok | should | link | Eject failing endpoints |
| ok | should | link | Circuit breakers |
| OUT OF ORDER | should | link | Why? |
| ok | should | link | Compare |

Labels the app shows that are not in the mockup are not detected by this script.
Compare the side-by-side image for extra elements, layout, spacing and visual weight.
