# ADLC build targets. Run `make help` for a list.

BIN      := bin/adlc
PKG      := github.com/roman-ambinder/agentic-framework/internal/buildinfo
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -s -w -X $(PKG).Version=$(VERSION) -X $(PKG).Commit=$(COMMIT) -X $(PKG).Date=$(DATE)

# Use exactly this Go version; the go command downloads it if missing.
export GOTOOLCHAIN := go1.26.11

TOOL     := go tool -modfile=tooling/go.mod
BUF      := $(TOOL) buf
LINT     := $(TOOL) golangci-lint
# Baseline for the schema compatibility check: a git ref or a directory.
AGAINST  ?= .git\#branch=main

.PHONY: help build test test-race test-docker lint fmt tidy tidy-check ci clean \
        proto proto-lint proto-check proto-breaking fixtures-test bugs-check evals-check

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-15s %s\n", $$1, $$2}'

build: ## Build bin/adlc with version info
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/adlc

test: ## Run unit tests
	go test ./...

test-race: ## Run unit tests with the race detector
	go test -race ./...

test-docker: ## Run tests that need a Docker daemon
	go test -race -tags docker -count=1 ./internal/sandbox/...

fixtures-test: ## Test the eval fixture repositories
	cd evals/fixtures/ledger && go vet ./... && go test -count=1 ./...

bugs-check: ## Check every seeded bug (bug applies, hidden test fails, fix works)
	@for d in evals/suites/go-bugs/*/; do evals/scripts/check-bug.sh "$$(basename "$$d")" || exit 1; done

evals-check: ## Check every seeded bug: hidden tests fail on the bug, pass on the fix
	go test -tags evals -count=1 -run 'TestSeededBugsBehave' ./internal/evals/

lint: ## Run golangci-lint
	$(LINT) run --allow-parallel-runners ./...

fmt: ## Format code
	$(LINT) fmt ./...

# tooling/go.mod is deliberately NOT tidied: `go mod tidy` there pulls in the
# newest versions of modules used only by the tools' own tests, which can
# require a newer Go than this repo uses. Change tool pins with
# `cd tooling && go get -tool <module>@<version>` instead.
tidy: ## Tidy the main module's go.mod / go.sum
	go mod tidy

tidy-check: ## Fail if go.mod / go.sum are not tidy, or tool pins don't verify
	go mod tidy -diff
	cd tooling && go mod verify

proto: ## Regenerate Go code from contracts/proto into gen/go
	$(BUF) generate

proto-lint: ## Lint the .proto files
	$(BUF) lint

proto-check: ## Fail if gen/go is out of date with contracts/proto
	rm -rf bin/_proto-check
	$(BUF) generate -o bin/_proto-check
	@diff -r gen bin/_proto-check/gen || (echo "gen/go is stale: run 'make proto'"; exit 1)
	@echo "gen/go is up to date"

proto-breaking: ## Fail on breaking schema changes vs AGAINST (default: main branch)
	$(BUF) breaking --against '$(AGAINST)'

ci: tidy-check proto-lint proto-check lint test-race fixtures-test build ## Everything CI runs (proto-breaking runs on PRs only)

clean: ## Remove build output
	rm -rf bin
