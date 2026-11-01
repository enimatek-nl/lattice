# Lattice — build, test and development targets.
#
# `make` on its own prints the target list. Every setting below can be
# overridden on the command line, e.g. `make run ADDR=127.0.0.1:9000`.

GO      ?= go
BIN_DIR ?= bin
BINARY  ?= lattice

SPEC      ?= SPECS.md
ENDPOINTS ?= ENDPOINTS.md
SWAG_DIR  ?= docs/swagger

# Development server: the flags README.md uses for the local try-it run.
ADDR        ?= 127.0.0.1:8008
SERVER_NAME ?= localhost:8008
DB          ?= /tmp/lattice.db
JWT_SECRET  ?= dev

.DEFAULT_GOAL := help

.PHONY: help build test race vet fmt lint endpoints run swagger clean

help: ## List the available targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  %-10s %s\n", $$1, $$2}'

build: ## Build the homeserver into bin/lattice
	$(GO) build -o $(BIN_DIR)/$(BINARY) ./cmd/server

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

endpoints: ## Regenerate ENDPOINTS.md from the endpoint index in SPECS.md
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
