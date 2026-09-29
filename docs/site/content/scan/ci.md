---
title: Using it in CI
weight: 10
description: Fail a pull request when it adds a failing Scorecard check, and show the results in code scanning.
---

# Using it in CI

With `--fail-on`, `flightbay scan` is a CI gate. It renders the repository, runs the [Scorecard](/desk/scorecard/) in the same process on the result, and exits 1 when a check at or above the severity you chose fails. Agent reach checks are included when `FLIGHTBAY_FEATURES` includes `reach` or `FLIGHTBAY_ENTERPRISE=1` is set.

```bash
flightbay scan . --fail-on high
flightbay scan . --fail-on critical --format sarif -o flightbay.sarif
flightbay scan . --fail-on high --new-only --base-ref origin/main --format github
```

No cluster, no credentials and no network are needed. The collector is a single static binary.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Nothing fails at the `--fail-on` severity or above (or `--fail-on` was not given). |
| 1 | At least one check at or above that severity fails. With a baseline, at least one such failure is new. |
| 2 | The scan could not run: a bad flag, an unreadable path, or a baseline that could not be scanned. |

A chart or overlay that fails to render does not stop the scan. It is listed on stderr and in coverage, and its objects are missing from the result.

## Report formats

| `--format` | Output |
|---|---|
| `text` | Area scores, then each failing object as `file:line  object: detail`, grouped by check. The default. |
| `json` | Scores, every check with its status and reason, and every failing object with its file, line and guide link. |
| `sarif` | SARIF 2.1.0. One rule per check, with the check's title, why it matters, the fix and a `helpUri` to the Solo product docs page for it (the guide where there is none). The help text links both the field guide and the docs page. One result per failing object, located at the file and line that declares it (the template, for a Helm chart). Levels: critical and high are `error`, medium is `warning`, low is `note`. |
| `github` | GitHub workflow commands: `::error file=...,line=...::message`. Failures at the `--fail-on` severity are errors, others are warnings or notices, so they show inline on the pull request diff. |
| `codequality` | A GitLab Code Quality report. |

`-o FILE` writes the report to a file; progress stays on stderr. The bundle is still written (`--bundle DIR|FILE`, default the working directory) unless you pass `--no-bundle`. Keep it as a build artefact: `flightbay-desk open` shows the rendered repository the checks saw.

## Only new failures

Use either option below to report and gate only failures introduced by a change:

| Flags | What it compares with |
|---|---|
| `--new-only --base-ref origin/main` | The same paths and flags at the base commit. The scan checks it out into a temporary `git worktree`, renders it, scores it and removes the worktree. Your working tree is not touched. |
| `--baseline-scan old.tar.zst` | A bundle from an earlier scan, for example the artefact from the last run on `main`. |

A failure is the same failure when the check and the object (kind, namespace and name) match. Everything the base already had is left out of the report and does not fail the job. The text report says how many are new out of the total.

The base-ref scan needs the base commit in the clone: use `fetch-depth: 0` on GitHub and `GIT_DEPTH: 0` on GitLab.

## GitHub Actions

[`examples/ci/github-actions.yml`](https://github.com/tjorourke/flightbay/blob/main/examples/ci/github-actions.yml) is a complete workflow. Copy it to `.github/workflows/flightbay.yml`. On a pull request it:

1. installs `flightbay` from the latest release (or builds it from source);
2. runs `flightbay scan --new-only --base-ref origin/<base> --fail-on high --format sarif`;
3. uploads the SARIF with `github/codeql-action/upload-sarif`, so results appear in the Security tab and on the diff;
4. writes the same results as workflow annotations;
5. comments on the pull request with the new failures, and updates that comment on the next push;
6. keeps the bundle as an artefact, and fails the job when something new fails.

On pushes to `main` it uploads the full SARIF without gating, so code scanning tracks what is open. Change `FAIL_ON`, `SCAN_PATHS` and `SCAN_FLAGS` at the top.

The pull request comment reads like this:

```text
Declared state from payments-deploy at commit 5c1d9e07a2b1
  Agentic (agentgateway, kagent, MCP)            57  D  (6 pass, 1 warn, 6 fail, 40 not applicable)

Compared with origin/main: 2 new failures (10 in total)

[CRITICAL] MCP endpoints require authentication (agentic.mcp-auth-required)
    deploy/mcp.yaml:8  AgentgatewayBackend demo/tools: OPEN: exposed via HTTPRoute demo/mcp on agw with no JWT or external auth (no policy applies)
[HIGH] LLM and MCP routes are rate limited per caller (agentic.rate-limit)
    deploy/mcp.yaml:8  AgentgatewayBackend demo/tools: no rate limit applies (no policy applies)

FAIL: 1 failing item at critical or above
```

## GitLab CI

[`examples/ci/gitlab-ci.yml`](https://github.com/tjorourke/flightbay/blob/main/examples/ci/gitlab-ci.yml) uses the **Code Quality** report format (`--format codequality`), published through `artifacts:reports:codequality`. It works on every GitLab tier and shows new and resolved findings in the merge request widget and on the diff. The SAST report format needs GitLab Ultimate and a vulnerability schema meant for code, so the example does not use it. The SARIF file is kept as a plain artefact.

In merge requests the job scans the target branch as the base, so it reports and gates only what the merge request adds.

| Code Quality field | From |
|---|---|
| `check_name` | The check ID, such as `k8s.probes` |
| `description` | The check title, the object and the detail |
| `severity` | critical is `blocker`, high is `critical`, medium is `major`, low is `minor` |
| `location.path`, `location.lines.begin` | The source file and line |
| `fingerprint` | A hash of the check and the object, stable across runs |

## Limits

- The gate only sees what the scanned paths declare. A policy applied from another repository counts as missing.
- A scan cannot know whether a controller accepts a policy. It catches a policy that targets a route that is not declared, but not one the controller would reject for another reason.
- Line numbers are exact for plain manifests and for objects from Kustomize bases. For Helm, the location is the template file, line 1.
