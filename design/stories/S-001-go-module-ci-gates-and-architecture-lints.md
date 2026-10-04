---
id: S-001
title: Go module, Makefile, CI gates, architecture lints and contributor files
capability: C-01
kind: infra
layer: L1
depends_on: []
covers: [C-01.FR-1, C-01.FR-2, C-01.FR-3, C-01.FR-5, C-01.FR-6, C-01.FR-7, C-01.FR-8, C-01.FR-12, C-01.FR-14, C-01.AC-1]
files_touched:
  - go.mod
  - cmd/muster/main.go
  - internal/buildinfo/buildinfo.go
  - internal/cli/cli.go
  - internal/cli/version.go
  - internal/cli/cli_test.go
  - cmd/muster-archlint/main.go
  - internal/archlint/archlint.go
  - internal/archlint/sqlrules.go
  - internal/archlint/callrules.go
  - internal/archlint/secretleak.go
  - internal/archlint/archlint_test.go
  - internal/archlint/testdata/**
  - Makefile
  - .golangci.yml
  - .testcoverage.yml
  - .editorconfig
  - .gitignore
  - .github/workflows/ci.yml
  - .github/pull_request_template.md
  - .github/ISSUE_TEMPLATE/bug.yml
  - .github/ISSUE_TEMPLATE/feature.yml
  - .github/ISSUE_TEMPLATE/integration-request.yml
  - .github/ISSUE_TEMPLATE/story-proposal.yml
  - .github/ISSUE_TEMPLATE/config.yml
  - LICENSE
  - NOTICE
  - README.md
  - CONTRIBUTING.md
  - CLA.md
  - CHANGELOG.md
  - AGENTS.md
  - CLAUDE.md
acceptance:
  - "[C-01.FR-1] The repository is github.com/muster-io/muster with the default branch `master`, and go.mod declares the module path `github.com/muster-io/muster`."
  - "[C-01.FR-2, C-01.AC-1] `make lint` fails and names the file when a source file lacks `SPDX-License-Identifier: AGPL-3.0-only` or the copyright line; paths excluded in NOTICE are skipped; a pull request adding such a file fails the `lint` check."
  - "[C-01.FR-3] A pull request whose author has not signed the CLA shows a failing cla-assistant check and cannot be merged."
  - "[C-01.FR-5] `make fmt`, `make lint`, `make lint-arch`, `make test`, `make generate` and `make build` exist and pass on a clean checkout; `make ci` runs the pull-request tier locally."
  - "[C-01.FR-6] `make lint-arch` is a separate CI step, and each of the eight rules of ADR-0016 reports its bad fixture under internal/archlint/testdata and accepts its good fixture."
  - "[C-01.FR-7] A push runs the fast tier without the race detector; a pull request runs the full tier with the race detector."
  - "[C-01.FR-8] `make test` fails when a core package (groups, routing, delivery, timers) is below 95 % statement coverage or any other package is below 80 %."
  - "[C-01.FR-12] `muster version` prints the version and the commit injected at build time."
  - "[C-01.FR-14] The root holds README.md with a roadmap section, CONTRIBUTING.md, CLA.md, CHANGELOG.md, NOTICE, LICENSE, CONTEXT.md and AGENTS.md, and CLAUDE.md is one line that points to AGENTS.md; issue forms exist for bugs, features, integration requests and story proposals; the pull request template has an evidence table per acceptance statement; the wiki and Discussions are off."
verify: "make ci"
operator_attention: true
issue: null
---

# S-001. Go module, Makefile, CI gates, architecture lints and contributor files

## Scope

**IN**

- The Go module and the single binary `muster` with the subcommand `muster version`; version and commit injected at
  build time.
- The Makefile as the only entry point, with the targets of C-01.FR-5 and `make ci`.
- golangci-lint with an explicit list of linters, the licence header check and the coverage gate.
- The architecture lints of ADR-0016 — all eight rules, each proven on fixtures — as `make lint-arch` and a separate CI
  step, before any domain code exists.
- The CI workflow with the push tier and the pull-request tier.
- The licence, notice, contributor and community files, issue forms and the pull request template.
- Repository settings: default branch, CLA check, branch protection, wiki and Discussions off.

**OUT**

- The SPA skeleton, code generation, dependency licence checks, security scanners, `SECURITY.md` and the nightly tier
  (S-002).
- Releases, the image, the Helm chart and the compose example (S-003); `muster dev`, the end-to-end and load-test
  harness (S-004).
- The domain logger and the metric registry that lints 7 and 8 protect (S-005); `muster_build_info` (S-005, S-006).
- The README demo animation, which needs a running UI: it comes after the first frontend story (C-01.FR-14), planned
  in S-058.

## Contracts

- **Module and binary.** `github.com/muster-io/muster`, Go 1.27. `cmd/muster/main.go` only calls `internal/cli`.
  `muster version` prints `muster <version> (commit <commit>)`; both values come from `-ldflags -X` into
  `internal/buildinfo`, with `0.0.0-dev` and `unknown` as defaults.
- **Make targets** (C-01.FR-5): `fmt` (gofmt and goimports, later also oxfmt), `lint` (golangci-lint and the licence
  header check), `lint-arch`, `test` (unit tests with the coverage gate, no race detector), `test-race`, `generate` (a
  no-op until S-002 adds generators), `build` (`bin/muster` with the version and commit), `ci` (`lint lint-arch
  test-race build` and what later stories add).
- **golangci-lint**: `default: none` with an explicit list of enabled linters; `nolintlint` requires an explanation on
  every `//nolint`.
- **Licence header** (C-01.FR-2, ADR-0001): every `*.go`, `*.sql`, `*.ts`, `*.tsx`, `*.js`, `*.mjs`, `*.css`, `*.sh`,
  `Makefile`, `Dockerfile`, chart template and workflow file starts with `SPDX-License-Identifier: AGPL-3.0-only` and
  `Copyright The Muster Authors`. Markdown, JSON, lock files, generated files and fixtures are exempt; third-party files
  keep their own header and are excluded by path, and NOTICE lists those paths.
- **Architecture lints** (ADR-0016), code behind the `lint` build tag:

  | # | Rule | Mechanism |
  |---|---|---|
  | 1 | A query on a table with `org_id` filters by it | SQL analyzer over `internal/**/query.sql`; tables with `org_id` are read from the migrations. A query may be exempt only with an `-- archlint:org-exempt <reason>` comment, used for the lookups by hash and by Connection that `design/db/schema.md` names. Background queries — worker claims, Leader scans, retention and pruning — are not exempt: they run per Organization and pass `org_id` |
  | 2 | Only `internal/groups` writes Alert Group tables | SQL analyzer: `INSERT`, `UPDATE` or `DELETE` on `alert_groups`, `alert_group_alerts`, `timeline_entries`, `notes` or `alert_group_counters` in a query file outside `internal/groups` |
  | 3 | Messenger sends and edits only from the delivery worker and the interactive path | Go analyzer over call sites of the adapter's send and edit methods; the adapter package and the two allowed packages are configured in the lint (they arrive with C-11) |
  | 4 | HTTP clients and transports only in `internal/outbound` | Go analyzer: `http.Client` or `http.Transport` values, `http.DefaultClient`, `http.Get`, `http.Post`, `http.Head`, `http.PostForm` outside `internal/outbound`, `internal/fakes`, `internal/devmode`, the load test `test/load`, the generated client `pkg/apiclient` and tests |
  | 5 | No known secret value in a log line or returned error | A test harness (`go test -tags lint ./internal/archlint/...`) with probes that push known secrets through code paths and scan logs and errors; each later story registers probes for its paths |
  | 6 | `context.Background()` only in `cmd/`, the wiring packages `internal/runtime` and `internal/cli`, the load test `test/load/main.go`, and tests | `forbidigo` with path exceptions |
  | 7 | Logging only through the domain logger | `depguard` denies `log` and `log/slog` outside `internal/logging`; `forbidigo` denies `fmt.Print*` and direct writes to `os.Stdout`/`os.Stderr` outside `internal/logging`, `internal/cli`, `internal/devmode` (the addresses `muster dev` prints) and `test/load` (its report) |
  | 8 | Only Prometheus-compatible `le` histograms | `forbidigo` denies the `vmrange` histogram constructors of VictoriaMetrics/metrics (`NewHistogram`, `GetOrCreateHistogram` and the `Set` methods of the same names) |

- **Coverage gate** (C-01.FR-8): `.testcoverage.yml` sets 95 % for `internal/groups`, `internal/routing`,
  `internal/delivery` and `internal/timers` (the core: Alert Group lifecycle, grouping, routing, delivery, timers) and
  80 % for every other package; `cmd/`, `internal/fakes/`, `internal/tools/` and generated code are excluded.
- **CI** (C-01.FR-7): `.github/workflows/ci.yml`. On push: `lint`, `lint-arch`, `test` (no race), `build`. On pull
  request: the same jobs with `test-race`; S-004 and S-006 add the end-to-end and integration jobs. Actions are pinned
  by commit SHA.
- **Contributor files** (C-01.FR-14): README.md (what Muster is, the development status, a roadmap section with the
  layers L1–L4, links to the design), CONTRIBUTING.md (Makefile targets, conventional commits, the CLA, the story
  process of `design/stories/`, the dependency rule of ADR-0001: a pull request that adds a dependency states its
  licence, its latest release date and its maintenance status), CLA.md (modelled on the Apache Individual Contributor
  License Agreement: a licence grant, no copyright assignment), CHANGELOG.md (header only; release-please writes it from
  S-003 on), NOTICE, LICENSE (the AGPL-3.0 text), AGENTS.md (instructions for coding agents: layout, Make targets, story
  contracts, live verification, the dependency rule), CLAUDE.md (one line that includes AGENTS.md). CONTEXT.md already
  exists.
- **Issue forms**: `bug.yml`, `feature.yml`, `integration-request.yml`, `story-proposal.yml` (Problem, Proposed
  capability, Acceptance ideas; label `story-proposal`), `config.yml` (blank issues off). **Pull request template**:
  `Closes #<issue>`, `Story: S-NNN`, a table of every acceptance statement with its evidence, the output of `verify`,
  and "story file changed in this pull request: why".
- **Repository settings** (operator): default branch `master`; wiki and Discussions off; squash merge; branch protection
  on `master` requiring the `lint`, `lint-arch`, `test`, `build` and CLA checks; cla-assistant.io installed (C-01.FR-3).

## Steps

1. Create go.mod, `cmd/muster`, `internal/cli` and `internal/buildinfo` with `muster version`. Check: `go build ./...`
   passes and `go run ./cmd/muster version` prints `muster 0.0.0-dev (commit unknown)`.
2. Add the Makefile, `.golangci.yml`, `.editorconfig`, `.gitignore` and the licence header check. Check: `make fmt lint
   test build` passes; a source file without the header fails `make lint`, naming the file.
3. Add the architecture lints with a good and a bad fixture per rule. Check: `make lint-arch` passes on the tree and
   `go test -tags lint ./internal/archlint/...` shows every bad fixture reported and every good one accepted.
4. Add the coverage gate. Check: a fixture package below its threshold makes the gate fail with the package and the
   percentage.
5. Add `ci.yml`. Check: a push runs the fast tier, a pull request the full tier, both green.
6. Add LICENSE, NOTICE and the contributor files, issue forms and the pull request template. Check: the files render on
   GitHub, and "New issue" offers the four forms.
7. Operator: create the organization and the repository, apply the settings and install cla-assistant.io. Check: a pull
   request from an account without a signature is blocked by the CLA check.

## Verification

```sh
make build && ./bin/muster version
# muster 0.0.0-dev (commit 3f2a9c1d0b7e)        <- 12 hex characters of the current commit

printf 'package cli\n' > internal/cli/zz_noheader.go; make lint; echo "exit=$?"
# internal/cli/zz_noheader.go: missing "SPDX-License-Identifier: AGPL-3.0-only"
# exit=2
rm internal/cli/zz_noheader.go

make lint-arch && echo lint-arch-ok
# lint-arch-ok
go test -tags lint -run TestRules -v ./internal/archlint/... | grep -E -- '--- (PASS|FAIL)'
# --- PASS: TestRules/1_org_scope
# ... one PASS line per rule, 1 to 8; each sub-test asserts the bad fixture is reported
```

On GitHub (operator, with evidence in the pull request):

- a draft pull request that adds a file without the header fails the `lint` check;
- a pull request from an account that has not signed shows the cla-assistant check as pending or failed and the merge
  button blocked;
- Settings show the wiki and Discussions switched off; "New issue" lists Bug, Feature, Integration request and Story
  proposal.

## Open questions

1. cla-assistant.io reads the agreement from a GitHub Gist, while C-01.FR-3 names `CLA.md` as the text. The gist must
   stay identical to CLA.md; decide who owns the gist and whether CI compares the two.
2. Lints 1 and 2 need a PostgreSQL parser. A pure-Go parser is preferred so the lint step needs no cgo; it is tooling
   behind the `lint` tag, never part of the binary, so the tooling licence rule of ADR-0001 applies.

## Notes

- Suggested commit: `build: add Go module, Makefile, CI gates and architecture lints`.
- `operator_attention: true` — the GitHub organization `muster-io`, the repository settings, branch protection and
  cla-assistant.io are set up by the operator.
- AGENTS.md is the canonical guide for coding agents; CLAUDE.md holds a single line that points to it, for tools that
  read only that file. C-01.FR-14 lists AGENTS.md alone because CLAUDE.md has no content of its own.
- Rules 1–3 have no real code to check yet; their fixtures prove them, and they bite from the first query (S-006) and
  the first adapter (C-11) on. A false positive is fixed in the lint, never worked around in the code.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-01.FR-1 | partial | repository, branch and module path; the image and chart names are S-003 |
| C-01.FR-2 | full | |
| C-01.FR-3 | full | |
| C-01.FR-5 | partial | Make targets; the SPA build and generated code are S-002 |
| C-01.FR-6 | full | |
| C-01.FR-7 | partial | push and pull-request tiers; the nightly tier is S-002 and S-004 |
| C-01.FR-8 | full | |
| C-01.FR-12 | partial | `muster version`; `muster_build_info` is S-005 and S-006 |
| C-01.FR-14 | full | the demo animation comes after the first frontend story (S-058) |
| C-01.AC-1 | partial | the header part; the GPL dependency part is S-002 |
