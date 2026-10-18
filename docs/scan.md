# Scan a repository

`flightbay scan` renders what a repository declares into a normal bundle marked `source: repo`, so the desk, the Scorecard and Agent reach work on it before anything is deployed.

```bash
flightbay scan deploy/ --env prod                                   # manifests, Helm charts, Kustomize overlays, Argo CD and Flux sources
flightbay scan . --fail-on high --new-only --base-ref origin/main --format sarif -o flightbay.sarif   # CI gate
flightbay-desk drift repo-scan.tar.zst cluster.tar.zst              # in Git and not running, running and not in Git, fields changed by hand
```

Checks that need a running cluster say "declared state only (repo scan)" instead of failing. `--fail-on` exits non-zero when a check at or above that severity fails, and `--new-only` with `--base-ref` gates only failures the pull request adds.

`examples/ci/` has [GitHub Actions](../examples/ci/github-actions.yml) and [GitLab CI](../examples/ci/gitlab-ci.yml) workflows.

More detail: [Scanning a repository](site/content/scan/_index.md), [Using it in CI](site/content/scan/ci.md), [Repo compared with the cluster](site/content/scan/drift.md) and [Fix packs](site/content/scan/fix-packs.md).
