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
SQLC_PKG := github.com/sqlc-dev/sqlc/cmd/sqlc
# renovate: datasource=go depName=github.com/sqlc-dev/sqlc
SQLC_VERSION := v1.31.1
SQLC := $(TOOLS)/sqlc-$(SQLC_VERSION)
PROMTOOL_PKG := github.com/prometheus/prometheus/cmd/promtool
# renovate: datasource=go depName=github.com/prometheus/prometheus
PROMTOOL_VERSION := v0.315.0
PROMTOOL := $(TOOLS)/promtool-$(PROMTOOL_VERSION)
# The tools above, as their install rules name them; make licenses reports their licences.
INSTALLED_TOOLS := $(GOLANGCI_LINT_PKG)@$(GOLANGCI_LINT_VERSION) $(GO_TEST_COVERAGE_PKG)@$(GO_TEST_COVERAGE_VERSION) \
	$(GO_LICENSES_PKG)@$(GO_LICENSES_VERSION) $(GOVULNCHECK_PKG)@$(GOVULNCHECK_VERSION) \
	$(GREMLINS_PKG)@$(GREMLINS_VERSION) $(HELM_PKG)@$(HELM_VERSION) $(KUBECONFORM_PKG)@$(KUBECONFORM_VERSION) \
	$(SQLC_PKG)@$(SQLC_VERSION) $(PROMTOOL_PKG)@$(PROMTOOL_VERSION)

VERSION ?= 0.0.0-dev
COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w -X $(MODULE)/internal/buildinfo.Version=$(VERSION) -X $(MODULE)/internal/buildinfo.Commit=$(COMMIT)

COVER_PROFILE := $(BIN)/cover.out

# Stands for the installed SPA dependencies: pnpm keeps its install state in this file, and the rule touches it.
WEB_DEPS := web/node_modules/.modules.yaml

# The output of make generate, checked in; sqlc writes one dbgen directory per package with a query.sql.
GENERATED := internal/api/gen internal/bridge/gen pkg/apiclient web/src/api/gen web/src/routeTree.gen.ts docs/reference \
	$(patsubst %/query.sql,%/dbgen,$(wildcard internal/*/query.sql))

# The licences shipped artifacts may depend on (ADR-0001).
SHIPPED_LICENSES := MIT,MIT-0,BSD-2-Clause,BSD-3-Clause,Apache-2.0,ISC,0BSD,Unlicense,CC0-1.0,MPL-2.0
# Modules whose licence go-licenses cannot classify, checked by hand: oapi-codegen/nullable is Apache-2.0, but its
# LICENSE file holds only the short Apache notice. Re-check an entry when its version changes.
LICENSES_CHECKED_BY_HAND := github.com/oapi-codegen/nullable

# The core packages of the mutation report; those that do not exist yet are skipped.
MUTATION_PKGS ?= internal/groups internal/routing internal/delivery internal/timers
MUTATION_DIR := $(BIN)/mutation

# make helm-check renders the chart for this Kubernetes version and validates it against its schemas; the Gateway API,
# ServiceMonitor, PrometheusRule, VMRule and GrafanaDashboard schemas come from a pinned commit of the CRDs catalog.
# It also packages the chart with a release appVersion, as release.yml does with the tag, to check its runbook_urls.
CHART := deploy/helm/muster
HELM_CHECK_KUBE_VERSION := 1.33.0
HELM_CHECK_SECRET_ERROR := the master keys are never generated: create a Secret and set existingSecret
HELM_CHECK_DASHBOARD_ERROR := dashboard.grafanaDashboard.enabled needs dashboard.configMap.enabled
HELM_CHECK_RELEASE_APP := v1.4.0-rc.1
KUBECONFORM_FLAGS := -strict -summary -kubernetes-version $(HELM_CHECK_KUBE_VERSION) -schema-location default \
	-schema-location 'https://raw.githubusercontent.com/datreeio/CRDs-catalog/4c8dc296d32b06d15ccde9668ff136c951f4d539/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json'

# The documentation toolchain (Zensical, pinned with hashes in docs/requirements.txt) lives in a virtual environment
# under bin/, made with Python 3.11 or newer and re-made when the requirements change. make docs renders the API
# reference from api/openapi.yaml and the Bridge protocol reference from api/bridge.yaml into docs/, where git ignores
# them, so that the site carries them.
PYTHON ?= python3
DOCS_VENV := $(BIN)/docs-venv
DOCS_DEPS := $(DOCS_VENV)/.installed
ZENSICAL := $(DOCS_VENV)/bin/zensical
API_REFERENCE := docs/reference/api/spec/index.html
BRIDGE_REFERENCE := docs/reference/bridge/spec/index.html

# The third-party scripts of the site, served from the site itself so that a page loads nothing from another host but
# GitHub (NFR-7): Mermaid, which the theme otherwise loads from unpkg.com on a page with a diagram, and Redoc, which the
# API and Bridge protocol references otherwise load from cdn.redocly.com, each with its licence. make docs fetches every
# file once into bin/docs-vendor/, named by its SHA-256, which it checks, and copies it under docs/assets/vendor/, where
# git ignores it. MERMAID_VERSION stays on the major the theme asks for (mermaid@11); REDOC_VERSION is the Redoc that
# the Redocly CLI of web/package.json links, and docs-api fails when the CLI links another.
MERMAID_VERSION := 11.17.2
REDOC_VERSION := 2.5.4
DOCS_VENDOR := docs/assets/vendor
DOCS_VENDOR_CACHE := $(BIN)/docs-vendor
SHA256SUM ?= $(shell command -v sha256sum >/dev/null 2>&1 && echo sha256sum || echo shasum -a 256)
define DOCS_VENDOR_FILES
mermaid/mermaid.min.js https://cdn.jsdelivr.net/npm/mermaid@$(MERMAID_VERSION)/dist/mermaid.min.js 581ed7d74bd9048d0e3a91363927d72ef22942d7722546b27f7cc29e35390eb8
mermaid/LICENSE https://cdn.jsdelivr.net/npm/mermaid@$(MERMAID_VERSION)/LICENSE ec9fb67dcb25eccc416ed56e1aab819222c805a2a4bfe4cb19e7556bf2ffde80
redoc/redoc.standalone.js https://cdn.jsdelivr.net/npm/redoc@$(REDOC_VERSION)/bundles/redoc.standalone.js dcaf76612bc4a3fbcc923a8966dee2f6146a5f32e5ce1b6f02dd60cbbf89500b
redoc/redoc.standalone.js.LICENSE.txt https://cdn.jsdelivr.net/npm/redoc@$(REDOC_VERSION)/bundles/redoc.standalone.js.LICENSE.txt eed602ec32e00b2b9df1984fbcc26a687b1b3193ec01961389c4b8439e6edf73
redoc/LICENSE https://cdn.jsdelivr.net/npm/redoc@$(REDOC_VERSION)/LICENSE d3026d549cf68ab7355bcfa85877bf8f845b3334a7efbfdc63936432fb34ff0e
endef
export DOCS_VENDOR_FILES
# A script or a stylesheet of a built page that comes from another host.
REMOTE_ASSET_RE := <script[^>]* src="(https?:)?//|<link[^>]* rel="(stylesheet|preconnect)"[^>]* href="(https?:)?//
REMOTE_ASSET_RE := $(REMOTE_ASSET_RE)|<link[^>]* href="(https?:)?//[^"]*"[^>]* rel="(stylesheet|preconnect)"

# make compose-check needs the Docker CLI with the compose plugin, not a running daemon; make dev-db and make dev
# need the daemon too.
DOCKER ?= docker
DEV_COMPOSE := deploy/dev/docker-compose.yml

# The Go end-to-end suite runs one replica; E2E_REPLICAS=2 adds muster dev --replica beside muster dev.
E2E_REPLICAS ?= 1
# Flags of `playwright install chromium`, such as --with-deps on a CI runner that lacks the browser's libraries.
PLAYWRIGHT_INSTALL_FLAGS ?=
# Flags of the load test, such as LOAD_TEST_FLAGS="-rate 50 -duration 1m".
LOAD_TEST_FLAGS ?=
# make quickstart-check: the image the compose example runs and the Git ref its download reads, in place of the release
# image and the release tag that Quick start names, such as MUSTER_IMAGE=ghcr.io/muster-io/muster:edge
# QUICKSTART_REF=master. Left empty, the page is checked exactly as written.
MUSTER_IMAGE ?=
QUICKSTART_REF ?=

.PHONY: help fmt lint lint-arch test test-race test-web test-integration generate generate-check build licenses \
	vulncheck mutation helm-check rules-test compose-check quickstart-check dev-db dev e2e e2e-go e2e-web load-test ci clean \
	web-browsers docs docs-serve docs-check docs-api docs-vendor demo

help: ## List the targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-z0-9-]+:.*## / {printf "  %-16s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

fmt: $(GOLANGCI_LINT) $(WEB_DEPS) ## Format Go code (gofmt, goimports) and the SPA (oxfmt)
	$(GOLANGCI_LINT) fmt
	$(PNPM) --dir web run fmt

# lint:i18n fails when the translation files and the keys of the code differ, or a key lacks a text in English or
# Russian (Russian plural forms included).
lint: $(GOLANGCI_LINT) $(WEB_DEPS) ## Check licence headers, Go code, the spec (Redocly), the SPA (tsc, oxlint, oxfmt) and translations
	$(GO) run ./internal/tools/licensecheck
	$(GOLANGCI_LINT) run ./...
	$(PNPM) --dir web run lint:spec
	$(PNPM) --dir web run typecheck
	$(PNPM) --dir web run lint
	$(PNPM) --dir web run fmt:check
	$(PNPM) --dir web run lint:i18n

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

test: $(GO_TEST_COVERAGE) test-web ## Run unit tests with the coverage gate, and the SPA's component tests
	@mkdir -p $(BIN)
	$(GO) test -coverprofile=$(COVER_PROFILE) -covermode=atomic ./...
	$(GO_TEST_COVERAGE) --config=.testcoverage.yml

test-race: $(GO_TEST_COVERAGE) test-web ## Run unit tests with the race detector and the coverage gate, and the SPA's tests
	@mkdir -p $(BIN)
	$(GO) test -race -coverprofile=$(COVER_PROFILE) -covermode=atomic ./...
	$(GO_TEST_COVERAGE) --config=.testcoverage.yml

# Vitest in browser mode: the SPA's component tests in Chromium.
test-web: $(WEB_DEPS) web-browsers ## Run the SPA's component tests (Vitest in Chromium)
	$(PNPM) --dir web run test

# The Chromium of the pinned Playwright, in Playwright's cache; a no-op when it is there.
web-browsers: $(WEB_DEPS)
	$(PNPM) --dir web exec playwright install $(PLAYWRIGHT_INSTALL_FLAGS) chromium

# The tests with the integration tag, on PostgreSQL 14 and 17 in containers that testcontainers starts from pinned
# images (internal/db/dbtest); they need a running Docker daemon. The unit tests run with them.
test-integration: ## Run the integration tests on PostgreSQL 14 and 17 (needs Docker)
	$(GO) test -tags integration -count=1 -timeout 15m ./...

# The Go outputs and the reference pages are removed first, as orval cleans its own, so that a file the generators no
# longer write goes away.
generate: $(WEB_DEPS) $(SQLC) ## Run every code generator
	rm -f internal/api/gen/*.gen.go internal/bridge/gen/*.gen.go pkg/apiclient/*.gen.go internal/*/dbgen/*.go
	$(SQLC) generate -f sqlc.yaml
	$(GO) tool oapi-codegen -config api/codegen-server.yaml api/openapi.yaml
	$(GO) tool oapi-codegen -config api/codegen-client.yaml api/openapi.yaml
	$(GO) tool oapi-codegen -config api/codegen-bridge.yaml api/bridge.yaml
	$(PNPM) --dir web run generate
	rm -f docs/reference/*.md
	$(GO) run ./internal/tools/refgen -out docs/reference

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
# with the project's toolchain. The packages of the documentation toolchain are reported with the licences of their
# metadata. A failure in a report is shown and never fails the target.
licenses: $(GO_LICENSES) $(WEB_DEPS) ## Check dependency licences: shipped artifacts block; build tools and the docs toolchain are reported
	@failed=0; \
	for step in "$(GO_LICENSES) check ./cmd/muster --ignore $(MODULE) $(addprefix --ignore ,$(LICENSES_CHECKED_BY_HAND)) --allowed_licenses=$(SHIPPED_LICENSES)" \
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
		echo "Documentation toolchain ($(DOCS_VENV), from docs/requirements.txt), reported only:"; \
		if $(MAKE) --no-print-directory $(DOCS_DEPS) >"$$tmp/docs-venv.log" 2>&1; then \
			$(DOCS_VENV)/bin/python -I -c "$$DOCS_LICENSES_PY" || echo "the report of the documentation toolchain failed"; \
		else \
			echo "the documentation toolchain could not be installed:"; cat "$$tmp/docs-venv.log"; \
		fi; \
	else \
		echo "cannot create a temporary directory, the build tools are not reported"; \
	fi; \
	exit $$failed

# Prints each package of the documentation toolchain with its licence: the License-Expression of its metadata, else
# its licence classifiers, else the first line of its License field.
define DOCS_LICENSES_PY
import importlib.metadata as md
rows = []
for d in md.distributions():
    m = d.metadata
    classifiers = [c.split(" :: ")[-1] for c in m.get_all("Classifier") or [] if c.startswith("License :: ")]
    licence = m.get("License-Expression") or ", ".join(classifiers) or (m.get("License") or "").strip().split("\n")[0]
    rows.append((m["Name"].lower(), d.version, licence or "unknown"))
for name, version, licence in sorted(rows):
    print(f"  {name} {version}: {licence}")
print(f"documentation toolchain: {len(rows)} packages, reported only (ADR-0001)")
endef
export DOCS_LICENSES_PY

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
# validates both renderings with kubeconform. The default values render no rule and no dashboard; chartcheck checks the
# rules and the dashboard of the all-options rendering, and of the chart packaged with a release appVersion, against
# reference.md and the metric catalogue. Rendering without existingSecret must fail with the master keys message, and a
# GrafanaDashboard without its ConfigMap with its own. Every step runs even when an earlier one fails.
helm-check: $(HELM) $(KUBECONFORM) ## Lint, render and validate the chart; check its rules and dashboard; check the existingSecret error
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
	echo "no rule and no dashboard with the default values"; \
	if $(HELM) template muster $(CHART) --set existingSecret=muster >"$$tmp/default.yaml"; then \
		if grep -qE '^kind: (PrometheusRule|VMRule|GrafanaDashboard)$$|muster\.json:' "$$tmp/default.yaml"; then \
			echo "the default values render a rule resource or the dashboard"; failed=1; \
		fi; \
	else \
		echo "helm template failed with the default values"; failed=1; \
	fi; \
	echo "chartcheck with the all-options values"; \
	if $(HELM) template muster $(CHART) --kube-version $(HELM_CHECK_KUBE_VERSION) \
		-f $(CHART)/ci/all-options-values.yaml >"$$tmp/all.yaml"; then \
		$(GO) run ./internal/tools/chartcheck -manifest "$$tmp/all.yaml" || failed=1; \
	else \
		echo "helm template failed with the all-options values"; failed=1; \
	fi; \
	echo "chartcheck with the all-options values and the appVersion $(HELM_CHECK_RELEASE_APP)"; \
	if $(HELM) package $(CHART) --version $(HELM_CHECK_RELEASE_APP:v%=%) --app-version $(HELM_CHECK_RELEASE_APP) \
		--destination "$$tmp/release" >/dev/null && \
		$(HELM) template muster "$$tmp/release/muster-$(HELM_CHECK_RELEASE_APP:v%=%).tgz" \
		--kube-version $(HELM_CHECK_KUBE_VERSION) -f $(CHART)/ci/all-options-values.yaml >"$$tmp/release.yaml"; then \
		$(GO) run ./internal/tools/chartcheck -manifest "$$tmp/release.yaml" || failed=1; \
	else \
		echo "packaging and rendering the chart with the appVersion $(HELM_CHECK_RELEASE_APP) failed"; failed=1; \
	fi; \
	echo "helm template without existingSecret"; \
	if out=$$($(HELM) template muster $(CHART) --set publicURL=https://muster.example.org 2>&1); then \
		echo "rendered without existingSecret"; failed=1; \
	elif echo "$$out" | grep -qF '$(HELM_CHECK_SECRET_ERROR)'; then \
		echo "fails as it should: $(HELM_CHECK_SECRET_ERROR)"; \
	else \
		echo "$$out"; echo "the error does not say: $(HELM_CHECK_SECRET_ERROR)"; failed=1; \
	fi; \
	echo "helm template with the GrafanaDashboard and without its ConfigMap"; \
	if out=$$($(HELM) template muster $(CHART) --set existingSecret=muster \
		--set dashboard.grafanaDashboard.enabled=true 2>&1); then \
		echo "rendered the GrafanaDashboard without its ConfigMap"; failed=1; \
	elif echo "$$out" | grep -qF '$(HELM_CHECK_DASHBOARD_ERROR)'; then \
		echo "fails as it should: $(HELM_CHECK_DASHBOARD_ERROR)"; \
	else \
		echo "$$out"; echo "the error does not say: $(HELM_CHECK_DASHBOARD_ERROR)"; failed=1; \
	fi; \
	exit $$failed

# Renders the chart with the PrometheusRule, extracts its rule group into a plain rule file with chartcheck, which also
# checks that every rule has its unit test and every test a rule, checks the file with promtool, which prints only on
# failure, and runs every unit test of $(RULE_TESTS) against it, one SUCCESS line per test file. promtool reads a test's
# rule_files next to the test, so the tests run from copies beside the extracted rules; $(RULE_TESTS) never holds a
# rules.yaml. RULES_TEST_FLAGS passes more values to helm template, such as a changed threshold.
RULE_TESTS := $(CHART)/tests/rules
RULES_TEST_FLAGS ?=
rules-test: $(HELM) $(PROMTOOL) ## Render the chart's alert rules and run their unit tests with promtool
	@tmp=$$(mktemp -d) || exit 1; \
	trap 'rm -rf "$$tmp"' EXIT; \
	$(HELM) template muster $(CHART) --set existingSecret=muster --set alerting.prometheusRule.enabled=true \
		$(RULES_TEST_FLAGS) >"$$tmp/chart.yaml" || exit 1; \
	$(GO) run ./internal/tools/chartcheck -manifest "$$tmp/chart.yaml" -extract-rules "$$tmp/rules.yaml" \
		-tests $(RULE_TESTS) || exit 1; \
	out=$$($(PROMTOOL) check rules "$$tmp/rules.yaml" 2>&1) || { echo "$$out"; exit 1; }; \
	cp $(RULE_TESTS)/*.yaml "$$tmp/" || exit 1; \
	$(PROMTOOL) test rules $(addprefix "$$tmp"/,$(notdir $(wildcard $(RULE_TESTS)/*.yaml)))

# Without a .env next to the compose file its variables are unset, which compose reports as warnings.
compose-check: ## Check the compose example with docker compose config, without and with .env.example
	$(DOCKER) compose -f deploy/compose/docker-compose.yml config --quiet
	$(DOCKER) compose -f deploy/compose/docker-compose.yml --env-file deploy/compose/.env.example config --quiet

# Runs the commands of docs/quick-start.md in an empty directory, waits for /health/ready and signs in as the bootstrap
# Admin, then removes the compose project with its volume; it needs Docker with the compose plugin, curl and openssl,
# and port 8080 free. The nightly workflow runs it on a fresh runner.
quickstart-check: ## Run the commands of Quick start with Docker, wait for ready and sign in as the bootstrap Admin
	MUSTER_IMAGE="$(MUSTER_IMAGE)" QUICKSTART_REF="$(QUICKSTART_REF)" ./test/docs/quickstart.sh

dev-db: ## Start the development PostgreSQL on 127.0.0.1:55432
	$(DOCKER) compose -f $(DEV_COMPOSE) up -d --wait

dev: dev-db build ## Start the development PostgreSQL, build and run muster dev
	./$(BIN)/muster dev

# Each run gets a fresh database on the development PostgreSQL, which it drops at the end: the Go suite, then the
# Playwright specs of the SPA (web/e2e) on a muster dev of their own. make e2e runs both, the second even when the first
# fails; e2e-go and e2e-web run one each. Both use the fixed ports of muster dev, so one machine runs one at a time; CI
# runs them as two jobs side by side.
E2E_GO = MUSTER_E2E_BINARY=$(abspath $(BIN)/muster) E2E_REPLICAS=$(E2E_REPLICAS) $(GO) test -tags e2e -count=1 -timeout 20m ./test/e2e/...
E2E_WEB = MUSTER_E2E_BINARY=$(abspath $(BIN)/muster) $(PNPM) --dir web run e2e

e2e: dev-db build web-browsers ## Run the end-to-end suites (Go, then Playwright); E2E_REPLICAS=2 for the Go suite
	@failed=0; \
	for step in "$(E2E_GO)" "$(E2E_WEB)"; do \
		echo "$$step"; sh -c "$$step" || failed=1; \
	done; \
	exit $$failed

e2e-go: dev-db build ## Run the Go end-to-end suite; E2E_REPLICAS=2 runs it with two replicas
	$(E2E_GO)

e2e-web: dev-db build web-browsers ## Run the Playwright specs of the SPA (web/e2e)
	$(E2E_WEB)

load-test: ## Run the load test against a running muster dev
	$(GO) run ./test/load $(LOAD_TEST_FLAGS)

# The README demo: web/e2e/demo.spec.ts alone on a muster dev of its own (the global setup of make e2e), recorded by
# Playwright as a 1280 x 720 video under bin/demo, then converted by ffmpeg into docs/assets/demo.gif, at most 30 seconds
# and 3 MB, with a palette of its own. It is run by hand when the UI changes and the GIF is checked in; never in CI.
DEMO_DIR := $(BIN)/demo
DEMO_GIF := docs/assets/demo.gif
DEMO_FILTER := fps=8,scale=960:-1:flags=lanczos,split[a][b];[a]palettegen=max_colors=96:stats_mode=diff[p];[b][p]paletteuse=dither=bayer:bayer_scale=5:diff_mode=rectangle

demo: dev-db build web-browsers ## Record the README demo into docs/assets/demo.gif (needs ffmpeg); run by hand, never in CI
	@command -v ffmpeg >/dev/null || { echo "make demo needs ffmpeg"; exit 1; }
	rm -rf $(DEMO_DIR)
	MUSTER_E2E_BINARY=$(abspath $(BIN)/muster) $(PNPM) --dir web exec playwright test --config playwright.demo.config.ts
	@video=$$(find $(DEMO_DIR) -name '*.webm' | head -n 1); \
	test -n "$$video" || { echo "no video under $(DEMO_DIR)"; exit 1; }; \
	mkdir -p $(dir $(DEMO_GIF)); \
	ffmpeg -loglevel error -y -t 30 -i "$$video" -vf "$(DEMO_FILTER)" -loop 0 $(DEMO_GIF); \
	size=$$(wc -c <$(DEMO_GIF)); \
	test $$size -le 3145728 || { echo "$(DEMO_GIF) is $$size bytes, more than 3 MB"; exit 1; }; \
	echo "$(DEMO_GIF): $$size bytes"

# The API reference and the Bridge protocol reference, each rendered by the Redocly CLI that make lint runs;
# .redocly.yaml keeps its telemetry off, the environment its update check quiet, and --disableGoogleFont the page from
# loading fonts from Google (NFR-7). Each page then loads Redoc from docs-vendor's copy instead of cdn.redocly.com, with
# the CLI's integrity hash unchanged, and takes a Content-Security-Policy that allows images only from the site, so that
# Redoc's sidebar does not load the Redocly logo from cdn.redoc.ly; the logo hides itself when it cannot load. Both
# pages lie equally deep under docs/, so the same relative path reaches the vendored copy from either.
# $(call render-reference,<specification>,<page>) renders one of them.
define render-reference
@mkdir -p $(dir $(2))
REDOCLY_SUPPRESS_UPDATE_NOTICE=true $(PNPM) --dir web exec redocly build-docs ../$(1) \
	--config ../.redocly.yaml --disableGoogleFont --output ../$(2)
@cdn='<script src="https://cdn.redocly.com/redoc/v$(REDOC_VERSION)/bundles/redoc.standalone.js"'; \
grep -qF "$$cdn" $(2) || \
	{ echo "$(2) does not load Redoc $(REDOC_VERSION): update REDOC_VERSION and its SHA-256"; exit 1; }; \
sed -e "s|$$cdn|<script src=\"../../../assets/vendor/redoc/redoc.standalone.js\"|" \
	-e "s|<meta charset=\"utf8\" />|&<meta http-equiv=\"Content-Security-Policy\" content=\"img-src 'self' data:\">|" \
	$(2) >$(2).tmp && mv $(2).tmp $(2); \
grep -qF 'http-equiv="Content-Security-Policy"' $(2) || \
	{ echo "$(2): the Content-Security-Policy could not be added"; exit 1; }
endef

docs-api: $(WEB_DEPS)
	$(call render-reference,api/openapi.yaml,$(API_REFERENCE))
	$(call render-reference,api/bridge.yaml,$(BRIDGE_REFERENCE))

# Fetches each file of DOCS_VENDOR_FILES into the cache unless it is there, fails on a SHA-256 other than the pinned
# one, and copies it under docs/.
docs-vendor:
	@printf '%s\n' "$$DOCS_VENDOR_FILES" | while read -r file url sum; do \
		cached="$(DOCS_VENDOR_CACHE)/$$sum"; \
		if [ ! -f "$$cached" ]; then \
			mkdir -p $(DOCS_VENDOR_CACHE) && curl -fsSL --retry 3 -o "$$cached.tmp" "$$url" || exit 1; \
			got=$$($(SHA256SUM) "$$cached.tmp" | cut -d ' ' -f 1); \
			[ "$$got" = "$$sum" ] || { echo "$$url: SHA-256 $$got, expected $$sum"; rm -f "$$cached.tmp"; exit 1; }; \
			mv "$$cached.tmp" "$$cached"; \
		fi; \
		mkdir -p "$$(dirname "$(DOCS_VENDOR)/$$file")" && cp "$$cached" "$(DOCS_VENDOR)/$$file" || exit 1; \
	done

# Before the build, runbookcheck fails when the runbook pages under docs/operations/runbooks/ differ from the Internal
# alerts, the chart rules of reference.md and the rules of the chart rendered with its all-options values, when a
# rendered rule's runbook_url does not end in its page, or a page lacks one of its four sections; its commands are not
# echoed, so that its own lines come first. Zensical checks the links to pages and their anchors, but not those to other
# files and not the navigation, so after the build the rendered references are checked, then that every page the nav
# of mkdocs.yml names exists under docs/ and that every page under docs/, except those exclude_docs names, is in the
# nav, and last that no page loads a script or a stylesheet from another host and every page with a diagram loads
# Mermaid from the site.
docs: generate docs-api docs-vendor $(DOCS_DEPS) $(HELM) ## Generate, render the API and Bridge protocol references and build the site strictly into site/
	@tmp=$$(mktemp -d) && trap 'rm -rf "$$tmp"' EXIT && \
		$(HELM) template muster $(CHART) -f $(CHART)/ci/all-options-values.yaml >"$$tmp/chart.yaml" && \
		$(GO) run ./internal/tools/runbookcheck -manifest "$$tmp/chart.yaml"
	$(ZENSICAL) build --strict --config-file mkdocs.yml
	@test -s site/$(API_REFERENCE:docs/%=%) || \
		{ echo "site/$(API_REFERENCE:docs/%=%) is missing: the API page links to it"; exit 1; }
	@test -s site/$(BRIDGE_REFERENCE:docs/%=%) || \
		{ echo "site/$(BRIDGE_REFERENCE:docs/%=%) is missing: the Bridge protocol page links to it"; exit 1; }
	@nav=$$(awk '/^nav:/ { n = 1; next } /^[^ #]/ { n = 0 } n' mkdocs.yml | grep -oE '[^ :]+\.md' | sort -u); \
	excluded=$$(awk '/^exclude_docs:/ { n = 1; next } /^[^ #]/ { n = 0 } n' mkdocs.yml | grep -oE '[^ ]+\.md'); \
	pages=$$(cd docs && find . -name '*.md' | sed 's|^\./||' | grep -vxF "$$excluded" | sort); \
	failed=0; \
	for p in $$nav; do \
		test -f "docs/$$p" || { echo "mkdocs.yml: the nav names docs/$$p, which does not exist"; failed=1; }; \
	done; \
	for p in $$pages; do \
		echo "$$nav" | grep -qxF "$$p" || { echo "docs/$$p is not in the nav of mkdocs.yml"; failed=1; }; \
	done; \
	exit $$failed
	@remote=$$(grep -rlE --include='*.html' '$(REMOTE_ASSET_RE)' site); \
	test -z "$$remote" || { echo "these pages load a script or a stylesheet from another host:"; echo "$$remote"; exit 1; }; \
	for f in $$(grep -rlF --include='*.html' 'class="mermaid"' site); do \
		grep -qF 'assets/vendor/mermaid/mermaid.min.js' "$$f" || { echo "$$f has a diagram but loads no Mermaid of the site"; exit 1; }; \
	done

docs-serve: generate docs-api docs-vendor $(DOCS_DEPS) ## Build the site and serve it on localhost:8000, rebuilding on changes
	$(ZENSICAL) serve --config-file mkdocs.yml

# generate-check runs first and on its own, so that the generate step of docs cannot hide a stale page.
docs-check: ## Fail on stale generated files, check the runbook pages, then build the site strictly (broken links and anchors fail)
	$(MAKE) --no-print-directory generate-check
	$(MAKE) --no-print-directory docs

ci: lint lint-arch generate-check licenses test-race build ## Run the pull-request tier locally

clean: ## Remove build output and installed tools
	rm -rf $(BIN)

# Installs the SPA dependencies exactly as locked; every target that runs pnpm depends on it.
$(WEB_DEPS): web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml
	$(PNPM) --dir web install --frozen-lockfile
	@touch $@

# The documentation toolchain, installed exactly as pinned: only wheels whose hashes docs/requirements.txt lists.
$(DOCS_DEPS): docs/requirements.txt
	rm -rf $(DOCS_VENV)
	$(PYTHON) -m venv $(DOCS_VENV)
	$(DOCS_VENV)/bin/python -m pip install --quiet --disable-pip-version-check --require-hashes --only-binary=:all: \
		-r docs/requirements.txt
	@touch $@

# $(call go-install,<package>@<version>,<binary>) installs a tool as $@ with the project's toolchain. A failed install
# runs again after 10 and then 20 seconds, so that a transient error of the module proxy or the checksum database
# does not fail the build; checksums are still verified.
define go-install
@mkdir -p $(TOOLS)/.install-$(2)
for attempt in 1 2 3; do \
	GOTOOLCHAIN=$(GO_TOOLCHAIN) GOBIN=$(abspath $(TOOLS))/.install-$(2) $(GO) install $(1) && break; \
	if [ $$attempt -eq 3 ]; then exit 1; fi; \
	echo "go install $(1) failed, trying again in $$((attempt * 10)) seconds"; sleep $$((attempt * 10)); \
done
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

# Without cgo, sqlc parses SQL with the WebAssembly build of the PostgreSQL parser, so no C compiler is needed.
$(SQLC): export CGO_ENABLED := 0
$(SQLC):
	$(call go-install,$(SQLC_PKG)@$(SQLC_VERSION),sqlc)

# go install refuses promtool@version: the prometheus module's go.mod has replace directives. It is built the same way
# in a throwaway module, with the versions its go.mod requires and the checksums verified, retried like go-install.
$(PROMTOOL): export CGO_ENABLED := 0
$(PROMTOOL):
	@mkdir -p $(TOOLS)
	for attempt in 1 2 3; do \
		tmp=$$(mktemp -d) || exit 1; \
		( cd "$$tmp" && export GOTOOLCHAIN=$(GO_TOOLCHAIN) && $(GO) mod init muster-promtool && \
			$(GO) get $(PROMTOOL_PKG)@$(PROMTOOL_VERSION) && $(GO) build -o $(abspath $@) $(PROMTOOL_PKG) ); \
		status=$$?; rm -rf "$$tmp"; [ $$status -eq 0 ] && break; \
		if [ $$attempt -eq 3 ]; then exit 1; fi; \
		echo "building promtool failed, trying again in $$((attempt * 10)) seconds"; sleep $$((attempt * 10)); \
	done
