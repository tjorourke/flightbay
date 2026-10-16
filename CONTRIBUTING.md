# Contributing to Flightbay

Thanks for helping improve Flightbay. For larger changes, open an issue first so we can agree on the behaviour and scope. Send changes as a pull request against `main`.

## Development setup

Flightbay requires Go 1.27. Building `flightbay-desk` also requires a C compiler because it links DuckDB through cgo.

```bash
make build
make test
go vet ./...
```

For changes to the live collector or desk, run the relevant kind end-to-end test as well:

```bash
test/e2e/e2e.sh
MESH_CONTEXT=kind-mesh1 test/e2e/mesh.sh
test/e2e/reach.sh
```

Build the documentation site with `hugo --source docs/site`. The command references are generated from the CLI and checked by tests; update them with `UPDATE_DOCS=1 go test ./cmd/...`. Regenerate the Scorecard checks page with `go run ./scripts/scorecard-checks` after changing a check.

## Keep customer data out of the repository

Never commit a customer bundle, kubeconfig, credentials, private keys, unredacted logs, or screenshots containing customer data. Use synthetic fixtures or the checked-in `examples/sample-bundle.tar.zst`. Before adding a new bundle or screenshot, verify that names, addresses, tokens and other identifying values are synthetic or removed. CI currently allowlists only the sample bundle; update that allowlist when adding another synthetic archive fixture.

Changes to collection or redaction must include tests that show excluded namespaces stay out of the bundle and secret values are not written. The sample bundle is intentionally synthetic; do not replace it with a live cluster capture.

## Pull requests

- Explain the user-visible change and any compatibility impact.
- Add or update tests and documentation for changed behaviour.
- Run `gofmt`, `go test ./...` and `go vet ./...` before requesting review.
- Keep generated documentation and screenshots in sync with their source.
- Do not include unrelated changes in the pull request.

Report security vulnerabilities through GitHub's private vulnerability reporting for this repository rather than opening a public issue.
