# Contributing to Flightbay

Thanks for helping improve Flightbay. For larger changes, open an issue first so we can agree on the behaviour and scope. Send changes as a pull request against `main`.

## Development setup

Flightbay requires Go 1.27. Building `flightbay-desk` also requires a C compiler because it links DuckDB through cgo.

```bash
git clone https://github.com/tjorourke/flightbay
cd flightbay
make install                                  # both binaries into $(go env GOPATH)/bin; make sure it is on your PATH
make build                                    # ./bin/flightbay and ./bin/flightbay-desk
make test                                     # unit tests
go vet ./...
```

Run `make install` again to install changes from your local checkout. Publishing a new `vX.Y.Z` tag builds release archives and updates the Homebrew formula; pushing to `main` alone does not publish a new binary.

For changes to the live collector or desk, run the relevant kind end-to-end test as well:

```bash
test/e2e/e2e.sh                               # kind cluster with planted faults: collector, UI, kubectl, k9s, search, SQL
MESH_CONTEXT=kind-mesh1 test/e2e/mesh.sh      # proxy diagnostics against an existing ambient cluster (read-only)
test/e2e/reach.sh                             # agent reach on kind + Istio ambient: applies the generated fixes, proves traffic blocked (make e2e-reach)
python3 test/e2e/ambient.py --context kind-mesh1   # ambient investigation, generated policies/routing/rate limits, live traffic and cleanup in a dedicated namespace
FLIGHTBAY_SCALE=10000000 go test ./internal/store -run TestScale -v -timeout 60m
docker build -t flightbay-desk .              # UI image with kubectl, k9s, istioctl, stern, jq and yq
go run ./scripts/screenshots -mesh BUNDLE     # retake docs/images from the sample bundle and a capture with agents (own headless Chrome)
```

Build the documentation site with `hugo --source docs/site`. The command references are generated from the CLI and checked by tests; update them with `UPDATE_DOCS=1 go test ./cmd/...`. Regenerate the Scorecard checks page with `go run ./scripts/scorecard-checks` after changing a check. The guides in `docs/*.md` are the GitHub entry points to the same material; keep them in step when behaviour changes.

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
