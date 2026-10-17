# make            build both binaries into ./bin (VERSION=0.1.0 make stamps a version)
# make install    install flightbay and flightbay-desk into $(go env GOPATH)/bin
# make test       unit tests;  make e2e  kind end-to-end
# make e2e-reach  agent reach on kind + Istio ambient: fixes applied, traffic proven blocked
.PHONY: build install test e2e e2e-reach
VERSION ?=
LDFLAGS := $(if $(VERSION),-ldflags "-X main.version=$(VERSION)")
build:
	CGO_ENABLED=0 go build $(LDFLAGS) -o bin/ ./cmd/flightbay
	CGO_ENABLED=1 go build $(LDFLAGS) -o bin/ ./cmd/flightbay-desk
install:
	CGO_ENABLED=0 go install $(LDFLAGS) ./cmd/flightbay
	CGO_ENABLED=1 go install $(LDFLAGS) ./cmd/flightbay-desk
	@echo "Installed to $$(go env GOPATH)/bin. Try: flightbay ui"
test:
	go test ./...
	node --test test/*.test.cjs
e2e: build
	test/e2e/e2e.sh
e2e-reach: build
	test/e2e/reach.sh
