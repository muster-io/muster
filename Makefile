# SPDX-License-Identifier: AGPL-3.0-only
# Copyright The Muster Authors

.DEFAULT_GOAL := help

GO ?= go
MODULE := github.com/muster-io/muster
BIN := bin
TOOLS := $(BIN)/tools

# Tools are built with the project's toolchain (go.mod), not the one their own go.mod asks for, so that they
# understand this module's Go version. The file name carries the tool version, so a version bump re-installs it.
GO_TOOLCHAIN := $(shell $(GO) env GOVERSION)
GOLANGCI_LINT_VERSION := v2.14.0
GOLANGCI_LINT := $(TOOLS)/golangci-lint-$(GOLANGCI_LINT_VERSION)
GO_TEST_COVERAGE_VERSION := v2.20.0
GO_TEST_COVERAGE := $(TOOLS)/go-test-coverage-$(GO_TEST_COVERAGE_VERSION)

VERSION ?= 0.0.0-dev
COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w -X $(MODULE)/internal/buildinfo.Version=$(VERSION) -X $(MODULE)/internal/buildinfo.Commit=$(COMMIT)

COVER_PROFILE := $(BIN)/cover.out

.PHONY: help fmt lint lint-arch test test-race generate build ci clean

help: ## List the targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-z-]+:.*## / {printf "  %-12s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

fmt: $(GOLANGCI_LINT) ## Format Go code (gofmt, goimports)
	$(GOLANGCI_LINT) fmt

lint: $(GOLANGCI_LINT) ## Check licence headers and run golangci-lint
	$(GO) run ./internal/tools/licensecheck
	$(GOLANGCI_LINT) run ./...

# Rules 1-4 on the tree, the fixtures of all eight rules with the rule 5 probes, then rules 6-8 on the tree. Every
# step runs even when an earlier one fails, so one run names every broken rule.
lint-arch: $(GOLANGCI_LINT) ## Run the architecture lints
	@failed=0; \
	for step in "$(GO) run -tags lint ./cmd/muster-archlint" \
		"GOLANGCI_LINT=$(abspath $(GOLANGCI_LINT)) $(GO) test -tags lint -count=1 ./internal/archlint/..." \
		"$(GOLANGCI_LINT) run --enable-only=depguard,forbidigo ./..."; do \
		echo "$$step"; sh -c "$$step" || failed=1; \
	done; \
	exit $$failed

test: $(GO_TEST_COVERAGE) ## Run unit tests with the coverage gate
	@mkdir -p $(BIN)
	$(GO) test -coverprofile=$(COVER_PROFILE) -covermode=atomic ./...
	$(GO_TEST_COVERAGE) --config=.testcoverage.yml

test-race: $(GO_TEST_COVERAGE) ## Run unit tests with the race detector and the coverage gate
	@mkdir -p $(BIN)
	$(GO) test -race -coverprofile=$(COVER_PROFILE) -covermode=atomic ./...
	$(GO_TEST_COVERAGE) --config=.testcoverage.yml

generate: ## Run every code generator
	@echo "generate: no generators yet"

build: ## Build bin/muster with the version and commit
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/muster ./cmd/muster

ci: lint lint-arch test-race build ## Run the pull-request tier locally

clean: ## Remove build output and installed tools
	rm -rf $(BIN)

$(GOLANGCI_LINT):
	@mkdir -p $(TOOLS)/.install-golangci-lint
	GOTOOLCHAIN=$(GO_TOOLCHAIN) GOBIN=$(abspath $(TOOLS))/.install-golangci-lint \
		$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	mv $(TOOLS)/.install-golangci-lint/golangci-lint $@
	rmdir $(TOOLS)/.install-golangci-lint

$(GO_TEST_COVERAGE):
	@mkdir -p $(TOOLS)/.install-go-test-coverage
	GOTOOLCHAIN=$(GO_TOOLCHAIN) GOBIN=$(abspath $(TOOLS))/.install-go-test-coverage \
		$(GO) install github.com/vladopajic/go-test-coverage/v2@$(GO_TEST_COVERAGE_VERSION)
	mv $(TOOLS)/.install-go-test-coverage/go-test-coverage $@
	rmdir $(TOOLS)/.install-go-test-coverage
