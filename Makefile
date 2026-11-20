# Lattice — build, test and development targets.
#
# `make` on its own prints the target list. Every setting below can be
# overridden on the command line, e.g. `make run ADDR=127.0.0.1:9000`.

GO      ?= go
BIN_DIR ?= bin
BINARY  ?= lattice

SPEC      ?= internal/coverage/SPECS.md
ENDPOINTS ?= ENDPOINTS.md
SWAG_DIR  ?= docs/swagger

# Development server: the flags README.md uses for the local try-it run. Loopback
# by default, because a dev homeserver on 0.0.0.0 is a homeserver the street can
# register on. The Complement container gets its own address from the entrypoint.
ADDR        ?= 127.0.0.1:8008
SERVER_NAME ?= localhost:8008
DB          ?= /tmp/lattice.db
JWT_SECRET  ?= dev

.DEFAULT_GOAL := help

.PHONY: help build image test race vet fmt lint endpoints run swagger clean complement-image complement complement-both complement-fast

help: ## List the available targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  %-17s %s\n", $$1, $$2}'

build: ## Build the homeserver into bin/lattice
	$(GO) build -o $(BIN_DIR)/$(BINARY) ./cmd/server

# The image a release publishes: the Dockerfile at the root of the repository, which
# .github/workflows/image.yml builds and pushes to ghcr.io when a tag is pushed.
# Building it locally is how a Dockerfile change is found before a release is.
IMAGE ?= lattice:latest

image: ## Build the container image that a release publishes
	docker build --build-arg REVISION="$(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)" -t $(IMAGE) .

test: ## Run all tests: unit plus the compatibility suite
	$(GO) test ./...

race: ## Run all tests under the race detector (the long polls make it slow)
	$(GO) test -race ./...

vet: ## go vet across every package
	$(GO) vet ./...

fmt: ## gofmt -s across every Go file
	gofmt -s -w cmd internal tests

lint: ## golangci-lint across every package
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo "golangci-lint is not installed: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest"; \
		exit 1; \
	}
	golangci-lint run ./...

endpoints: ## Regenerate ENDPOINTS.md from internal/coverage/SPECS.md (generated, not committed)
	$(GO) run ./cmd/endpoints -spec $(SPEC) -out $(ENDPOINTS)

run: build ## Run a development server (see the variables above)
	$(BIN_DIR)/$(BINARY) --addr $(ADDR) --server-name $(SERVER_NAME) \
		--db $(DB) --jwt-secret $(JWT_SECRET)

# swag is not a module dependency: this downloads it on demand. The output is
# generated, gitignored, never committed, and deliberately JSON/YAML only — a
# generated docs.go inside the module would not compile without the swaggo
# runtime package. Run `make clean` to drop it again.
swagger: ## Generate OpenAPI docs with swag into docs/swagger
	$(GO) run github.com/swaggo/swag/cmd/swag@latest init \
		--parseDependency --parseInternal \
		--dir ./internal/api --generalInfo api.go \
		--output $(SWAG_DIR) --outputTypes json,yaml

clean: ## Remove build output and generated documentation
	rm -rf $(BIN_DIR) $(SWAG_DIR)
	rmdir docs 2>/dev/null || true
	rm -f coverage.out

# Complement is the upstream Matrix test suite: it starts homeservers in
# containers and drives them over the client-server API, so it needs an image
# rather than a process. deploy/complement holds its Dockerfile and entrypoint,
# and the entrypoint is what turns the CA Complement mounts at /complement/ca
# into a certificate named for the server. The binary is built outside the image
# on purpose - the image is rebuilt for every run - and it is built with CGO off,
# because the image is Alpine (musl) and a dynamically linked binary inside it
# dies with the unhelpful message "not found". So this target builds its own
# binary rather than trusting `make build`.
COMPLEMENT       ?= $(HOME)/complement
COMPLEMENT_IMAGE ?= localhost/lattice-complement:latest
COMPLEMENT_PKG   ?= ./tests/csapi
COMPLEMENT_RUN   ?=
COMPLEMENT_TIMEOUT ?= 45m

complement-image: ## Build the Complement container image for this binary
	CGO_ENABLED=0 $(GO) build -trimpath -o $(BIN_DIR)/$(BINARY) ./cmd/server
	docker build -t $(COMPLEMENT_IMAGE) -f deploy/complement/Dockerfile .

# `make complement` runs the whole client-API suite (106 tests, about 7 minutes here); add
# COMPLEMENT_RUN to narrow it, e.g.
#   make complement COMPLEMENT_RUN='TestRoomAlias|TestRoomCanonicalAlias'
# and add COMPLEMENT_PKG=./tests for the server-server suite (92 tests, about 4 minutes; the
# slowest case is 11 seconds). Complement keeps its federation tests sequential, so the cost is
# the sum of the cases: each one stands up two containers. -timeout below is a ceiling, not an
# estimate - a run that reaches it has hung, and the output shows which case it hung in.
# The suite runs from the Complement checkout, which has to be present; it is not
# a module dependency because Complement's own go.mod owns the test binaries.
complement: complement-image ## Run the Complement client-API suite against the image
	cd $(COMPLEMENT) && COMPLEMENT_BASE_IMAGE=$(COMPLEMENT_IMAGE) $(GO) test -timeout=40m $(COMPLEMENT_PKG) \
		$(if $(COMPLEMENT_RUN),-run '$(COMPLEMENT_RUN)',)

# Two ways to stop paying for the serial 409 seconds, both measured 2026-11-20 on a
# 28-core machine. `complement-both` runs the client-API package and the server-server
# package as two processes: safe, because each package owns a Complement namespace and
# therefore its own containers, and it costs the slower suite rather than the sum. It is
# also the run that keeps both recorded tallies honest at once, which is what the
# two-day-old server-server number was waiting for.
#
# `complement-fast` leaves out the names docs/TODO.md records as blocked on a decision
# or a feature: 82 green in about 160 seconds instead of 106 tests in 409, which is the
# difference between a regression check you run and one you skip. Nothing is weakened —
# `complement` stays the compliance run, and the skip list is a copy of the red table
# (deploy/complement/blocked-csapi.txt says so in its header).
#
# And one thing not to try: splitting a single package across processes. Complement names
# blueprint containers by package namespace and every process cleans up by that label at
# startup, so lanes of one package delete each other's homeservers — measured, 63 red
# instead of 22, with a summary line that looks fine. deploy/complement/run-suites.sh
# carries the details.
complement-both: complement-image ## Run the client-API and server-server suites side by side
	COMPLEMENT=$(COMPLEMENT) COMPLEMENT_BASE_IMAGE=$(COMPLEMENT_IMAGE) COMPLEMENT_SUITES='csapi fed' \
		COMPLEMENT_TIMEOUT=$(COMPLEMENT_TIMEOUT) sh deploy/complement/run-suites.sh

complement-fast: complement-image ## Run the client-API suite without the names docs/TODO.md lists as blocked
	COMPLEMENT=$(COMPLEMENT) COMPLEMENT_BASE_IMAGE=$(COMPLEMENT_IMAGE) COMPLEMENT_SUITES='csapi' \
		COMPLEMENT_SKIP_BLOCKED=1 COMPLEMENT_TIMEOUT=$(COMPLEMENT_TIMEOUT) sh deploy/complement/run-suites.sh
