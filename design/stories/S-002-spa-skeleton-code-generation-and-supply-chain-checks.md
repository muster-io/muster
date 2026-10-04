---
id: S-002
title: SPA skeleton, code generation, dependency licences and security scanning
capability: C-01
kind: infra
layer: L1
depends_on: [S-001]
covers: [C-01.FR-4, C-01.FR-5, C-01.FR-7, C-01.FR-11, C-01.AC-1]
files_touched:
  - web/package.json
  - web/tsconfig.json
  - web/vite.config.ts
  - web/index.html
  - web/src/main.tsx
  - web/src/routes/__root.tsx
  - web/src/routes/index.tsx
  - web/orval.config.ts
  - web/.oxlintrc.json
  - web/embed.go
  - web/embed_test.go
  - web/dist/.gitkeep
  - web/scripts/check-licenses.mjs
  - api/codegen-server.yaml
  - api/codegen-client.yaml
  - .redocly.yaml
  - go.mod
  - Makefile
  - .github/workflows/ci.yml
  - .github/workflows/nightly.yml
  - .github/workflows/codeql.yml
  - .github/workflows/scorecard.yml
  - .github/renovate.json5
  - .gremlins.yaml
  - SECURITY.md
acceptance:
  - "[C-01.FR-4, C-01.AC-1] Adding a GPL-licensed Go module to the binary's imports, or a GPL-licensed npm package to the SPA's production dependencies, makes `make licenses` and the CI `licenses` job fail, naming the package and its licence; a package licensed `MPL-2.0 OR Apache-2.0` passes."
  - "[C-01.FR-4] A build-tool dependency under an OSI-approved licence outside the shipped list, or a data-only package under CC-BY-4.0, is reported by `make licenses` and does not fail it."
  - "[C-01.FR-5] `make build` builds the SPA and embeds it in the binary; the embedded file system of a binary built from a clean checkout contains the SPA's index.html."
  - "[C-01.FR-5] `make generate` regenerates the Go server, the Go client, the TypeScript client and the route tree; a change to api/openapi.yaml without regenerating makes `make generate-check` and CI fail, naming the stale files."
  - "[C-01.FR-7] The nightly workflow produces the mutation-testing report of the core packages as an artifact and does not block anything."
  - "[C-01.FR-11] SECURITY.md describes private vulnerability reporting and the supported release; CI runs govulncheck, pnpm audit, CodeQL and OpenSSF Scorecard; Renovate opens dependency update pull requests."
  - "[C-01.FR-5, NFR-13] `make lint` lints api/openapi.yaml with Redocly CLI and reports no errors, so the spec that generation reads is valid."
verify: "make ci licenses"
operator_attention: true
issue: 2
---

# S-002. SPA skeleton, code generation, dependency licences and security scanning

## Scope

**IN**

- A minimal SPA in `web/` on the stack of ADR-0009, built by `make build` and embedded in the binary.
- `make generate` for the code generated from `api/openapi.yaml` — the strict server, the Go client, the TypeScript
  client and the route tree — with a CI check that the checked-in output is current.
- Redocly lint of the spec and an oasdiff report on pull requests.
- The dependency licence checks of ADR-0001 for the binary and the SPA bundle, blocking, and for tooling, reporting.
- Security scanning: SECURITY.md, govulncheck, pnpm audit, CodeQL, OpenSSF Scorecard, Renovate.
- The nightly workflow with the mutation-testing report.

**OUT**

- The application shell, translations and the first real pages (S-014).
- `sqlc` generation (S-007, with the first queries) and the generated reference pages (S-005).
- Trivy on the image and everything about releases (S-003); the nightly load test and the two-replica end-to-end run
  (S-004).

## Contracts

- **SPA stack** (ADR-0009): TypeScript 7, Vite 8, pnpm, React 19, TanStack Router with file-based routes, TanStack
  Query 5, oxlint and oxfmt. Versions are pinned. The index route shows a placeholder page with the text "Muster".
- **Embedding.** Package `web` (`web/embed.go`) embeds `web/dist` with `go:embed`; a checked-in `web/dist/.gitkeep`
  keeps the pattern valid before the SPA is built, so `go build` and `go test` work in any checkout. `make build` runs the
  SPA build, then the Go build.
- **Code generation** (C-01.FR-5, ADR-0008), all run by `make generate`:

  | Generator | Input | Output (generated, checked in) |
  |---|---|---|
  | oapi-codegen v2.8 `std-http-server` + `strict-server` + `models` (`go tool`) | `api/openapi.yaml`, `api/codegen-server.yaml` | `internal/api/gen/` |
  | oapi-codegen v2.8 `client` + `models` | `api/openapi.yaml`, `api/codegen-client.yaml` | `pkg/apiclient/` |
  | orval (fetch client, TanStack Query hooks, Zod schemas, MSW mocks) | `api/openapi.yaml`, `web/orval.config.ts` | `web/src/api/gen/` |
  | TanStack Router | `web/src/routes/` | `web/src/routeTree.gen.ts` |

  The oapi-codegen configuration follows the spike recorded in `api/README.md`: `skip-prune: true`,
  `nullable-type: true`, `exclude-tags: [Outgoing webhooks, Health and metrics]`. `make generate-check` regenerates
  everything and fails on any difference; it is part of `make ci` and of CI.
- **Spec checks**: Redocly CLI lints `api/openapi.yaml` with the recommended ruleset in `make lint`; `.redocly.yaml`
  accepts the warnings that `api/README.md` explains (the redirect-only and health operations, the unused
  `AlertmanagerWebhook`). oasdiff `breaking` compares the spec with the base branch on every pull request and posts a
  report; it does not block before 1.0 (ADR-0008).
- **Dependency licences** (C-01.FR-4, ADR-0001), `make licenses` and the CI job `licenses`:
  - shipped artifacts, blocking: `go-licenses check ./cmd/muster` against MIT, MIT-0, BSD-2-Clause, BSD-3-Clause,
    Apache-2.0, ISC, 0BSD, Unlicense, CC0-1.0 and MPL-2.0; the SPA's production dependencies from
    `pnpm licenses list --prod --json` checked by `web/scripts/check-licenses.mjs` against the same list, reading SPDX
    expressions so that `A OR B` passes when either side is allowed;
  - tooling — Go tools, SPA dev dependencies, later the documentation site — reported only: any OSI-approved licence,
    and CC-BY-4.0 for packages that contain only data.
- **Security** (C-01.FR-11): SECURITY.md (private vulnerability reporting through GitHub; the latest minor release is
  supported); govulncheck (fails on a vulnerability in code that is called) and `pnpm audit --prod` (fails on high and
  critical) in CI and nightly; CodeQL for Go and TypeScript; the OpenSSF Scorecard workflow; Renovate with pinned
  versions, action digests and grouped updates.
- **Nightly tier** (C-01.FR-7): `.github/workflows/nightly.yml` runs mutation testing (gremlins, `.gremlins.yaml`) over
  the core packages that exist and uploads the report as an artifact; S-004 adds the load test and the two-replica
  end-to-end run.

## Steps

1. Create the SPA skeleton and the embedding. Check: `make build` succeeds and `go test ./web/...` finds the built
   `index.html` in the embedded file system.
2. Add the generator configuration and `make generate`, `make generate-check`. Check: `make generate` produces the four
   outputs, `go build ./...` and `pnpm --dir web typecheck` pass, and a second `make generate` changes nothing.
3. Add Redocly lint and the oasdiff report. Check: `make lint` reports no spec errors; a pull request that removes an
   operation gets an oasdiff comment and stays mergeable.
4. Add `make licenses` and the licence script. Check: the clean tree passes; the GPL experiments of Verification fail.
5. Add SECURITY.md, the security workflows and Renovate. Check: the workflows run green on a pull request.
6. Add the nightly workflow with gremlins. Check: a manual `workflow_dispatch` run uploads the mutation report.

## Verification

```sh
make build && go test ./web/... -run TestEmbeddedIndex -v
# --- PASS: TestEmbeddedIndex

make generate && git status --porcelain
# (no output)

sed -i.bak 's/which is always shown/which is always shown here/' api/openapi.yaml
make generate-check; echo "exit=$?"
# stale generated files: internal/api/gen/... pkg/apiclient/... web/src/api/gen/...
# exit=2
mv api/openapi.yaml.bak api/openapi.yaml

# GPL experiments on a scratch branch; the pull request records the packages used
go get github.com/sagernet/sing-box@latest   # GPL-3.0-or-later; add a blank import to cmd/muster
make licenses; echo "exit=$?"
# github.com/sagernet/sing-box ... GPL-3.0 ... not in the allowed list
# exit=2
pnpm --dir web add ckeditor5                 # GPL-2.0-or-later, production dependency
make licenses; echo "exit=$?"
# ckeditor5@... GPL-2.0-or-later is not allowed for shipped artifacts
# exit=2
```

In the browser, after `make build` and serving `web/dist` with `pnpm --dir web preview`: the page shows "Muster".

On GitHub (operator): the CodeQL and Scorecard workflows are green, Renovate's onboarding pull request is open, and a
manual run of the nightly workflow has a mutation report artifact.

## Open questions

1. Blocking thresholds of the scanners: govulncheck fails on called vulnerabilities, pnpm audit on high and critical
   advisories in production dependencies; everything else is reported. Confirm.
2. The SPA licence check is a small script because the common npm licence checkers do not evaluate SPDX `OR`
   expressions the way ADR-0001 requires. If a maintained tool that does is found, it replaces the script.

## Notes

- Suggested commit: `build: add SPA skeleton, code generation and supply-chain checks`.
- `operator_attention: true` — installing Renovate, enabling CodeQL and Scorecard and private vulnerability reporting
  are repository settings.
- The first full generation of the strict server is also the first build of every handler interface; from S-010 on,
  an operation that no story has implemented yet answers `501` (`not-implemented`).

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-01.FR-4 | full | the Helm chart has no chart dependencies (S-003) |
| C-01.FR-5 | partial | SPA build and generated code; `sqlc` joins in S-007 |
| C-01.FR-7 | partial | nightly mutation report; load test and two-replica run are S-004 |
| C-01.FR-11 | partial | everything except Trivy on the image (S-003) |
| C-01.AC-1 | partial | the GPL dependency part; the header part is S-001 |
