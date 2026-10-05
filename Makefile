# SPDX-License-Identifier: AGPL-3.0-only
# Copyright The Muster Authors

.DEFAULT_GOAL := help

GO ?= go
PNPM ?= pnpm
MODULE := github.com/muster-io/muster
BIN := bin
TOOLS := $(BIN)/tools

# Tools are built with the project's toolchain (go.mod), not the one their own go.mod asks for, so that they
# understand this module's Go version. The file name carries the tool version, so a version bump re-installs it.
# Renovate updates each version through the comment above it.
GO_TOOLCHAIN := $(shell $(GO) env GOVERSION)
GOLANGCI_LINT_PKG := github.com/golangci/golangci-lint/v2/cmd/golangci-lint
# renovate: datasource=go depName=github.com/golangci/golangci-lint/v2
GOLANGCI_LINT_VERSION := v2.14.0
GOLANGCI_LINT := $(TOOLS)/golangci-lint-$(GOLANGCI_LINT_VERSION)
GO_TEST_COVERAGE_PKG := github.com/vladopajic/go-test-coverage/v2
# renovate: datasource=go depName=github.com/vladopajic/go-test-coverage/v2
GO_TEST_COVERAGE_VERSION := v2.20.0
GO_TEST_COVERAGE := $(TOOLS)/go-test-coverage-$(GO_TEST_COVERAGE_VERSION)
GO_LICENSES_PKG := github.com/google/go-licenses/v2
# renovate: datasource=go depName=github.com/google/go-licenses/v2
GO_LICENSES_VERSION := v2.0.1
GO_LICENSES := $(TOOLS)/go-licenses-$(GO_LICENSES_VERSION)
GOVULNCHECK_PKG := golang.org/x/vuln/cmd/govulncheck
# renovate: datasource=go depName=golang.org/x/vuln
GOVULNCHECK_VERSION := v1.8.0
GOVULNCHECK := $(TOOLS)/govulncheck-$(GOVULNCHECK_VERSION)
GREMLINS_PKG := github.com/go-gremlins/gremlins/cmd/gremlins
# renovate: datasource=go depName=github.com/go-gremlins/gremlins
GREMLINS_VERSION := v0.6.0
GREMLINS := $(TOOLS)/gremlins-$(GREMLINS_VERSION)
HELM_PKG := helm.sh/helm/v4/cmd/helm
# renovate: datasource=go depName=helm.sh/helm/v4
HELM_VERSION := v4.3.0
HELM := $(TOOLS)/helm-$(HELM_VERSION)
KUBECONFORM_PKG := github.com/yannh/kubeconform/cmd/kubeconform
# renovate: datasource=go depName=github.com/yannh/kubeconform
KUBECONFORM_VERSION := v0.8.0
KUBECONFORM := $(TOOLS)/kubeconform-$(KUBECONFORM_VERSION)
# The tools above, as their install rules name them; make licenses reports their licences.
INSTALLED_TOOLS := $(GOLANGCI_LINT_PKG)@$(GOLANGCI_LINT_VERSION) $(GO_TEST_COVERAGE_PKG)@$(GO_TEST_COVERAGE_VERSION) \
	$(GO_LICENSES_PKG)@$(GO_LICENSES_VERSION) $(GOVULNCHECK_PKG)@$(GOVULNCHECK_VERSION) \
	$(GREMLINS_PKG)@$(GREMLINS_VERSION) $(HELM_PKG)@$(HELM_VERSION) $(KUBECONFORM_PKG)@$(KUBECONFORM_VERSION)

VERSION ?= 0.0.0-dev
COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w -X $(MODULE)/internal/buildinfo.Version=$(VERSION) -X $(MODULE)/internal/buildinfo.Commit=$(COMMIT)

COVER_PROFILE := $(BIN)/cover.out

# Stands for the installed SPA dependencies: pnpm keeps its install state in this file, and the rule touches it.
WEB_DEPS := web/node_modules/.modules.yaml

# The output of make generate, checked in.
GENERATED := internal/api/gen pkg/apiclient web/src/api/gen web/src/routeTree.gen.ts

# The licences shipped artifacts may depend on (ADR-0001).
SHIPPED_LICENSES := MIT,MIT-0,BSD-2-Clause,BSD-3-Clause,Apache-2.0,ISC,0BSD,Unlicense,CC0-1.0,MPL-2.0

# The core packages of the mutation report; those that do not exist yet are skipped.
MUTATION_PKGS ?= internal/groups internal/routing internal/delivery internal/timers
MUTATION_DIR := $(BIN)/mutation

# make helm-check renders the chart for this Kubernetes version and validates it against its schemas; the Gateway API
# and ServiceMonitor schemas come from a pinned commit of the CRDs catalog.
CHART := deploy/helm/muster
HELM_CHECK_KUBE_VERSION := 1.33.0
HELM_CHECK_SECRET_ERROR := the master keys are never generated: create a Secret and set existingSecret
KUBECONFORM_FLAGS := -strict -summary -kubernetes-version $(HELM_CHECK_KUBE_VERSION) -schema-location default \
	-schema-location 'https://raw.githubusercontent.com/datreeio/CRDs-catalog/4c8dc296d32b06d15ccde9668ff136c951f4d539/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json'

# make compose-check needs the Docker CLI with the compose plugin, not a running daemon; make dev-db and make dev
# need the daemon too.
DOCKER ?= docker
DEV_COMPOSE := deploy/dev/docker-compose.yml

# make e2e runs one replica; E2E_REPLICAS=2 adds muster dev --replica beside muster dev.
E2E_REPLICAS ?= 1
# Flags of the load test, such as LOAD_TEST_FLAGS="-rate 50 -duration 1m".
LOAD_TEST_FLAGS ?=

.PHONY: help fmt lint lint-arch test test-race generate generate-check build licenses vulncheck mutation helm-check \
	compose-check dev-db dev e2e load-test ci clean

help: ## List the targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-z0-9-]+:.*## / {printf "  %-16s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

fmt: $(GOLANGCI_LINT) $(WEB_DEPS) ## Format Go code (gofmt, goimports) and the SPA (oxfmt)
	$(GOLANGCI_LINT) fmt
	$(PNPM) --dir web run fmt

lint: $(GOLANGCI_LINT) $(WEB_DEPS) ## Check licence headers, Go code, the spec (Redocly) and the SPA (tsc, oxlint, oxfmt)
	$(GO) run ./internal/tools/licensecheck
	$(GOLANGCI_LINT) run ./...
	$(PNPM) --dir web run lint:spec
	$(PNPM) --dir web run typecheck
	$(PNPM) --dir web run lint
	$(PNPM) --dir web run fmt:check

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

# The Go outputs are removed first, as orval cleans its own, so that a file the generators no longer write goes away.
generate: $(WEB_DEPS) ## Run every code generator
	rm -f internal/api/gen/*.gen.go pkg/apiclient/*.gen.go
	$(GO) tool oapi-codegen -config api/codegen-server.yaml api/openapi.yaml
	$(GO) tool oapi-codegen -config api/codegen-client.yaml api/openapi.yaml
	$(PNPM) --dir web run generate

# Copies the generated paths aside, regenerates and compares file by file, so that changed, added and removed files
# are all named, whatever the state of git.
generate-check: ## Fail when generated files are not current, naming them; leaves the regenerated output in the tree
	@snap=$$(mktemp -d) || exit 1; \
	trap 'rm -rf "$$snap"' EXIT; \
	for p in $(GENERATED); do \
		if [ -e "$$p" ]; then mkdir -p "$$snap/$$(dirname "$$p")" && cp -R "$$p" "$$snap/$$p" || exit 1; fi; \
	done; \
	$(MAKE) --no-print-directory generate || exit 1; \
	stale=$$( { (cd "$$snap" && find $(GENERATED) -type f 2>/dev/null); find $(GENERATED) -type f 2>/dev/null; } | \
		sort -u | while IFS= read -r f; do cmp -s "$$snap/$$f" "$$f" || echo "$$f"; done ); \
	if [ -n "$$stale" ]; then \
		echo "stale generated files:"; echo "$$stale" | sed 's/^/  /'; \
		echo "run make generate and commit the result"; \
		exit 1; \
	fi; \
	echo "generated files are current"

build: $(WEB_DEPS) ## Build the SPA, then bin/muster with the version and commit
	$(PNPM) --dir web run build
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN)/muster ./cmd/muster

# Shipped artifacts block: the Go binary (Muster's own module is AGPL and skipped) and the SPA's production
# dependencies, after the tests of the SPA's licence script. Every step runs even when an earlier one fails.
# The Go build tools are only reported, classified by the SPA's licence script: the tool directives of go.mod, and the
# tools this Makefile installs, which go install builds outside go.mod, so they are resolved in a throwaway module
# with the project's toolchain. A failure there is shown and never fails the target.
licenses: $(GO_LICENSES) $(WEB_DEPS) ## Check dependency licences: shipped artifacts block, build tools are reported
	@failed=0; \
	for step in "$(GO_LICENSES) check ./cmd/muster --ignore $(MODULE) --allowed_licenses=$(SHIPPED_LICENSES)" \
		"$(PNPM) --dir web run test:scripts" \
		"$(PNPM) --dir web run licenses"; do \
		echo "$$step"; sh -c "$$step" || failed=1; \
	done; \
	echo "Go build tools (go.mod's tool directives and the tools this Makefile installs), reported only:"; \
	if tmp=$$(mktemp -d); then \
		trap 'rm -rf "$$tmp"' EXIT; \
		$(GO_LICENSES) report $$($(GO) list tool) >"$$tmp/go-mod.csv" 2>"$$tmp/go-mod.log" || \
			{ echo "the report of go.mod's tools failed:"; cat "$$tmp/go-mod.log"; }; \
		( mkdir "$$tmp/mod" && cd "$$tmp/mod" && export GOTOOLCHAIN=$(GO_TOOLCHAIN) && \
			$(GO) mod init muster-build-tools && $(GO) get -tool $(INSTALLED_TOOLS) && \
			$(abspath $(GO_LICENSES)) report $$($(GO) list tool) ) >"$$tmp/installed.csv" 2>"$$tmp/installed.log" || \
			{ echo "the report of the installed tools failed:"; cat "$$tmp/installed.log"; }; \
		node web/scripts/check-licenses.mjs --go-tools "$$tmp/go-mod.csv" "$$tmp/installed.csv" || \
			echo "the classification of the Go build tools failed"; \
	else \
		echo "cannot create a temporary directory, the Go build tools are not reported"; \
	fi; \
	exit $$failed

vulncheck: $(GOVULNCHECK) ## Find vulnerable code that is called (govulncheck) and high SPA advisories (pnpm audit)
	@failed=0; \
	for step in "$(GOVULNCHECK) ./..." "$(PNPM) --dir web audit --prod --audit-level high"; do \
		echo "$$step"; sh -c "$$step" || failed=1; \
	done; \
	exit $$failed

# One JSON report and one log per package and a summary.txt in $(MUTATION_DIR). There are no thresholds: only a
# gremlins error fails the target. A mutant's timeout is a multiple of the coverage run's duration, so -count=1 keeps
# that run out of the test cache, which would shrink the timeout to almost nothing.
mutation: $(GREMLINS) ## Run mutation testing over the core packages, report only
	@rm -rf $(MUTATION_DIR); mkdir -p $(MUTATION_DIR); \
	summary=$(MUTATION_DIR)/summary.txt; : >$$summary; failed=0; ran=0; \
	for pkg in $(MUTATION_PKGS); do \
		if [ ! -d "$$pkg" ]; then echo "$$pkg: does not exist yet, skipped" >>$$summary; continue; fi; \
		ran=1; name=$$(echo "$$pkg" | tr / -); \
		echo "$(GREMLINS) unleash --config .gremlins.yaml --output $(MUTATION_DIR)/$$name.json ./$$pkg"; \
		if GOFLAGS="$(GOFLAGS) -count=1" $(GREMLINS) unleash --config .gremlins.yaml \
			--output $(abspath $(MUTATION_DIR))/$$name.json \
			$(CURDIR)/$$pkg >$(MUTATION_DIR)/$$name.log 2>&1; then \
			echo "$$pkg:" >>$$summary; \
			grep -E '^(Killed|Timed out|Test efficacy|Mutator coverage):' $(MUTATION_DIR)/$$name.log | \
				sed 's/^/  /' >>$$summary; \
		else \
			failed=1; echo "$$pkg: gremlins failed, see $(MUTATION_DIR)/$$name.log" >>$$summary; \
		fi; \
	done; \
	if [ $$ran -eq 0 ]; then echo "no core package exists yet, nothing to mutate" >>$$summary; fi; \
	cat $$summary; \
	exit $$failed

# Lints and renders the chart with the default values plus an existingSecret and with the all-options values, and
# validates both renderings with kubeconform; rendering without existingSecret must fail with the master keys message.
# Every step runs even when an earlier one fails.
helm-check: $(HELM) $(KUBECONFORM) ## Lint, render and validate the chart; check the existingSecret error
	@tmp=$$(mktemp -d) || exit 1; \
	trap 'rm -rf "$$tmp"' EXIT; \
	failed=0; \
	for values in "--set existingSecret=muster" "-f $(CHART)/ci/all-options-values.yaml"; do \
		echo "helm lint, helm template and kubeconform with $$values"; \
		$(HELM) lint --strict --kube-version $(HELM_CHECK_KUBE_VERSION) $$values $(CHART) || \
			{ echo "helm lint failed with $$values"; failed=1; }; \
		if $(HELM) template muster $(CHART) --kube-version $(HELM_CHECK_KUBE_VERSION) $$values >"$$tmp/chart.yaml"; then \
			$(KUBECONFORM) $(KUBECONFORM_FLAGS) "$$tmp/chart.yaml" || { echo "kubeconform failed with $$values"; failed=1; }; \
		else \
			echo "helm template failed with $$values"; failed=1; \
		fi; \
	done; \
	echo "helm template without existingSecret"; \
	if out=$$($(HELM) template muster $(CHART) --set publicURL=https://muster.example.org 2>&1); then \
		echo "rendered without existingSecret"; failed=1; \
	elif echo "$$out" | grep -qF '$(HELM_CHECK_SECRET_ERROR)'; then \
		echo "fails as it should: $(HELM_CHECK_SECRET_ERROR)"; \
	else \
		echo "$$out"; echo "the error does not say: $(HELM_CHECK_SECRET_ERROR)"; failed=1; \
	fi; \
	exit $$failed

# Without a .env next to the compose file its variables are unset, which compose reports as warnings.
compose-check: ## Check the compose example with docker compose config, without and with .env.example
	$(DOCKER) compose -f deploy/compose/docker-compose.yml config --quiet
	$(DOCKER) compose -f deploy/compose/docker-compose.yml --env-file deploy/compose/.env.example config --quiet

dev-db: ## Start the development PostgreSQL on 127.0.0.1:55432
	$(DOCKER) compose -f $(DEV_COMPOSE) up -d --wait

dev: dev-db build ## Start the development PostgreSQL, build and run muster dev
	./$(BIN)/muster dev

e2e: build ## Run the end-to-end suite; E2E_REPLICAS=2 runs two replicas
	MUSTER_E2E_BINARY=$(abspath $(BIN)/muster) E2E_REPLICAS=$(E2E_REPLICAS) \
		$(GO) test -tags e2e -count=1 -timeout 10m ./test/e2e/...

load-test: ## Run the load test against a running muster dev
	$(GO) run ./test/load $(LOAD_TEST_FLAGS)

ci: lint lint-arch generate-check licenses test-race build ## Run the pull-request tier locally

clean: ## Remove build output and installed tools
	rm -rf $(BIN)

# Installs the SPA dependencies exactly as locked; every target that runs pnpm depends on it.
$(WEB_DEPS): web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml
	$(PNPM) --dir web install --frozen-lockfile
	@touch $@

# $(call go-install,<package>@<version>,<binary>) installs a tool as $@ with the project's toolchain.
define go-install
@mkdir -p $(TOOLS)/.install-$(2)
GOTOOLCHAIN=$(GO_TOOLCHAIN) GOBIN=$(abspath $(TOOLS))/.install-$(2) $(GO) install $(1)
mv $(TOOLS)/.install-$(2)/$(2) $@
rmdir $(TOOLS)/.install-$(2)
endef

$(GOLANGCI_LINT):
	$(call go-install,$(GOLANGCI_LINT_PKG)@$(GOLANGCI_LINT_VERSION),golangci-lint)

$(GO_TEST_COVERAGE):
	$(call go-install,$(GO_TEST_COVERAGE_PKG)@$(GO_TEST_COVERAGE_VERSION),go-test-coverage)

$(GO_LICENSES):
	$(call go-install,$(GO_LICENSES_PKG)@$(GO_LICENSES_VERSION),go-licenses)

$(GOVULNCHECK):
	$(call go-install,$(GOVULNCHECK_PKG)@$(GOVULNCHECK_VERSION),govulncheck)

$(GREMLINS):
	$(call go-install,$(GREMLINS_PKG)@$(GREMLINS_VERSION),gremlins)

$(HELM):
	$(call go-install,$(HELM_PKG)@$(HELM_VERSION),helm)

$(KUBECONFORM):
	$(call go-install,$(KUBECONFORM_PKG)@$(KUBECONFORM_VERSION),kubeconform)
