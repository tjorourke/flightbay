# make            build both binaries into ./bin
# make install    install flightbay and flightbay-desk into $(go env GOPATH)/bin
# make test       unit tests;  make e2e  kind end-to-end
# make e2e-reach  agent reach on kind + Istio ambient: fixes applied, traffic proven blocked
.PHONY: build install test e2e e2e-reach
build:
	CGO_ENABLED=0 go build -o bin/ ./cmd/flightbay
	CGO_ENABLED=1 go build -o bin/ ./cmd/flightbay-desk
install:
	CGO_ENABLED=0 go install ./cmd/flightbay
	CGO_ENABLED=1 go install ./cmd/flightbay-desk
	@echo "Installed to $$(go env GOPATH)/bin. Try: flightbay ui"
test:
	go test ./...
e2e: build
	test/e2e/e2e.sh
e2e-reach: build
	test/e2e/reach.sh
