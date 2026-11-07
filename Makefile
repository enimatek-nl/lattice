# Go Matrix homeserver — development entry points.
#
# The protocol contract, not the Makefile, is the source of truth: `make verify`
# runs everything that must hold before any progress is claimed.

GO      ?= go
GOFLAGS ?=
BIN     := bin/lattice
PKG     := ./cmd/... ./internal/...
TESTS   := ./tests/...
COVER   := coverage.out
# VERSION is read from the one place the number lives (internal/app.Version), so a
# release image tag and the version the server reports cannot drift apart.
VERSION := $(shell sed -n 's/^const Version = "\(.*\)".*/\1/p' internal/app/app.go)
# The label names the log and the tally lists a full run writes, so that a number quoted
# in docs/compliance-matrix.md can be traced back to the run it came from.
COMPLEMENT_LABEL ?= $(shell date -u +%Y%m%dT%H%M%SZ)
IMAGE   ?= complement-lattice:latest

.DEFAULT_GOAL := verify

.PHONY: verify
verify: fmt-check vet test-race ## formatting, vet and race tests

.PHONY: build
build: ## build the server binary into bin/
	@mkdir -p bin
	$(GO) build $(GOFLAGS) -o $(BIN) ./cmd/lattice

.PHONY: run
run: build ## run the server with default configuration (data/ in the working dir)
	./$(BIN)

.PHONY: test
test: ## unit and integration tests
	$(GO) test $(GOFLAGS) -count=1 $(TESTS)

.PHONY: test-race
test-race: ## tests under the race detector
	$(GO) test $(GOFLAGS) -race -count=1 -timeout 15m $(TESTS)

.PHONY: test-unit
test-unit: ## only the unit suite
	$(GO) test $(GOFLAGS) -count=1 ./tests/unit

.PHONY: test-integration
test-integration: ## only the integration suite (real listeners, TLS included)
	$(GO) test $(GOFLAGS) -count=1 ./tests/integration

.PHONY: cover
cover: ## coverage report over the whole module
	$(GO) test -count=1 -coverprofile=$(COVER) -covermode=atomic $(PKG) $(TESTS)
	$(GO) tool cover -func=$(COVER) | tail -1

.PHONY: vet
vet: ## go vet across the module
	$(GO) vet ./...

.PHONY: fmt
fmt: ## gofmt every Go file
	@gofmt -l -w $(shell find . -name '*.go' -not -path './vendor/*') >/dev/null

.PHONY: fmt-check
fmt-check: ## fail if anything is unformatted
	@out=$$(gofmt -l $(shell find . -name '*.go' -not -path './vendor/*')); \
		if [ -n "$$out" ]; then echo "unformatted files:"; echo "$$out"; exit 1; fi

.PHONY: lint
lint: ## golangci-lint when available, go vet otherwise
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run ./...; else \
		echo "golangci-lint not installed; running go vet"; $(GO) vet ./...; fi

.PHONY: tidy
tidy: ## resolve and record dependencies
	$(GO) mod tidy

.PHONY: image
image: ## build the release image: one static binary, no shell and no compiler in it
	docker build --build-arg REVISION="$$(git rev-parse --short HEAD 2>/dev/null || echo unknown)" -t lattice:$(VERSION) .

.PHONY: docker
docker: ## build the Complement-compatible image
	docker build -t $(IMAGE) -f deploy/complement/Dockerfile .

.PHONY: complement
complement: ## run the M1-relevant Complement suites against the built image
	COMPLEMENT_BASE_IMAGE=$(IMAGE) scripts/complement.sh

.PHONY: complement-all
complement-all: ## the whole Complement suite; the log and the pass/failure lists are recorded under .agent-work/
	COMPLEMENT_BASE_IMAGE=$(IMAGE) RUN='.*' COMPLEMENT_LABEL=$(COMPLEMENT_LABEL) scripts/complement.sh

.PHONY: generate-config
generate-config: ## print a documented default configuration
	$(GO) run ./cmd/lattice generate-config

.PHONY: clean
clean: ## remove build artefacts
	rm -rf bin $(COVER) coverage.html
