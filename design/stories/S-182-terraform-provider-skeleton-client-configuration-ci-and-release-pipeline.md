---
id: S-182
title: "The Terraform provider's repository, its generated client, the provider configuration with the version check, CI with acceptance tests against muster dev, and the release pipeline"
capability: C-33
kind: infra
layer: L2
repository: muster-io/terraform-provider-muster
depends_on: [S-181]
covers: [C-33.FR-1, C-33.FR-2, C-33.FR-3, C-33.FR-4, C-33.FR-8, C-33.FR-17, C-33.FR-18, C-33.FR-19, C-33.FR-20, C-33.AC-1, C-33.AC-15]
files_touched:
  - README.md
  - CONTRIBUTING.md
  - SECURITY.md
  - AGENTS.md
  - go.mod
  - main.go
  - Makefile
  - .golangci.yml
  - .gitignore
  - .goreleaser.yml
  - terraform-registry-manifest.json
  - release-please-config.json
  - .release-please-manifest.json
  - .github/renovate.json5
  - .github/pull_request_template.md
  - .github/workflows/ci.yml
  - .github/workflows/acceptance.yml
  - .github/workflows/release-please.yml
  - .github/workflows/release.yml
  - .github/workflows/codeql.yml
  - .github/workflows/scorecard.yml
  - api/openapi.yaml
  - api/MUSTER_VERSION
  - api/codegen.yaml
  - internal/client/client.go
  - internal/client/client_test.go
  - internal/client/problem.go
  - internal/client/problem_test.go
  - internal/provider/provider.go
  - internal/provider/provider_test.go
  - internal/provider/version.go
  - internal/provider/version_test.go
  - internal/provider/resourcekit.go
  - internal/provider/resourcekit_test.go
  - internal/acctest/acctest.go
  - test/muster-dev.sh
  - templates/index.md.tmpl
  - examples/provider/provider.tf
acceptance:
  - "[C-33.FR-1] `go.mod` declares `github.com/muster-io/terraform-provider-muster`; `main.go` serves the provider with terraform-plugin-framework over protocol version 6 at the address `registry.terraform.io/muster-io/muster`; every Go file, workflow and the Makefile carry `SPDX-License-Identifier: MIT` and `Copyright The Muster Authors`, which `make lint` checks; CONTRIBUTING.md takes contributions under MIT without a CLA; `make licenses` fails on a dependency whose licence is not on the list of the Contracts."
  - "[C-33.FR-2, C-33.AC-15] `api/MUSTER_VERSION` names the Muster version and `api/openapi.yaml` is that version's specification byte for byte, whose `info.license` is MIT; `make spec-check` downloads the specification of that tag and fails naming the difference — a copy with one byte changed fails it — and `make generate-check` fails when the client generated with oapi-codegen from the copy is not current; the CI jobs `spec` and `generate` run them."
  - "[C-33.FR-3] The provider schema has `endpoint` and `token` (sensitive), both optional, falling back to `MUSTER_ENDPOINT` and `MUSTER_TOKEN`; the configuration wins over the environment; neither set is an error naming both (`provider_test.go`)."
  - "[C-33.FR-3, C-33.FR-4, C-33.AC-1] Against a test server whose `getSystemStatus` answers `0.1.4`, configuring fails with \"Muster 0.1.4 is older than 0.2.0, the oldest version this provider works with\"; `0.2.0` and `0.9.3` pass; `0.2.1-5-gabc1234` and `dev` pass with a warning that the version cannot be compared; `401` fails naming `MUSTER_TOKEN` or `token` as configured; `403` fails saying that the token needs the Admin Role (`version_test.go`, `provider_test.go`)."
  - "[C-33.AC-1] The acceptance test `TestAccProvider_configure` plans a configuration with only the provider block against `muster dev` with the token of a Service account with the Admin Role, and fails as above with the token of a Service account with the Responder Role."
  - "[C-33.FR-8, C-33.FR-17] The client sends `Authorization: Bearer` and `User-Agent: terraform-provider-muster/<version>`; an answer `412` becomes the diagnostic of C-33.FR-8 naming the object; a problem `422` becomes a diagnostic at the attribute path of each `errors` pointer, with the problem's title, detail and code; `429` is retried after `Retry-After`, at most five times; a `GET` that fails with a transient error or `5xx` is retried with back-off, and no other method is; two writers of one shared object run one after the other, each sending the ETag of its own fresh read, and a writer whose part changed since its prior state gets the `412` error without writing (`client_test.go`, `problem_test.go`, `resourcekit_test.go`)."
  - "[C-33.FR-19] `make testacc` starts PostgreSQL and `muster dev` from `ghcr.io/muster-io/muster:<api/MUSTER_VERSION>` (`MUSTER_IMAGE` overrides it), creates the Service account and its token through the API, and runs `go test` with `TF_ACC=1`; the workflow `acceptance.yml` runs it on pull requests and pushes to `master` with the lowest and the newest supported Terraform and OpenTofu, and daily with `MUSTER_IMAGE=ghcr.io/muster-io/muster:edge`; its job `old-cli` runs the `_oldCLI` tests on Terraform 1.10 and OpenTofu 1.10."
  - "[C-33.FR-18] `make docs` generates `docs/` with tfplugindocs from the schema, `templates/` and `examples/`, and `make docs-check`, run in CI, fails when `docs/` is not current."
  - "[C-33.FR-20] `goreleaser check` passes on `.goreleaser.yml`, which builds the platforms of the Contracts into `terraform-provider-muster_<version>_<os>_<arch>.zip` with `terraform-provider-muster_<version>_SHA256SUMS`, its detached GPG signature and `terraform-provider-muster_<version>_manifest.json` from `terraform-registry-manifest.json` (protocol version 6.0); `goreleaser release --snapshot --clean --skip=sign` builds them in CI; release-please keeps a release pull request with `0.x` versions, and its tag starts `release.yml`."
verify: "make ci"
operator_attention: true
issue: 700
---

# S-182. The Terraform provider's repository, its generated client, the provider configuration with the version check, CI with acceptance tests against muster dev, and the release pipeline

## Scope

**IN**

- The repository `muster-io/terraform-provider-muster` with its contributor files and agent guide, its Go module, the
  provider served over protocol version 6, and the copy of Muster's specification with the client generated from it.
- The provider's configuration, `endpoint` and `token`, and the version check of C-33.FR-3 and C-33.FR-4.
- The shared machinery of every resource (`resourcekit`): the ETag in the state and `If-Match`, the `412` diagnostic,
  problems as diagnostics, retries, `managed_by` `terraform` on writes, import by `public_id`, `404` on a read.
- The acceptance-test harness against `muster dev` from Muster's image, the CI, the documentation generation, the
  GoReleaser configuration with signing and the Registry manifest, release-please, Renovate, CodeQL and Scorecard.

**OUT**

- Every resource and data source (S-183 to S-187); the guides, the Registry accounts, the signing key and the first
  release (S-188); Muster's own page on configuration as code (S-189).

## Contracts

- **Repository** (D757): created by the maintainer, MIT (`LICENSE` already there), default branch `master`, squash
  merges only, branches deleted after merge, protected as `muster-io/muster-bridge-go`; the check `ci` becomes required
  once this story's CI exists. Every Go file, workflow and the Makefile start with `SPDX-License-Identifier: MIT` and
  `Copyright The Muster Authors`; generated files, lock files and `api/openapi.yaml`, which states its licence in
  `info.license`, are exempt. No CLA: CONTRIBUTING.md accepts contributions under MIT, asks for conventional commits on
  one line without AI attribution, and points to Muster's `design/stories/` for the stories of this repository.
  SECURITY.md asks for private reports through GitHub's private vulnerability reporting. AGENTS.md is the guide for
  agents and people working here: the Makefile as the entry point, the copy of the specification that is never edited
  by hand, the stories in Muster's repository with `repository: muster-io/terraform-provider-muster`, the pull request
  with `Part of muster-io/muster#<issue>`, `Story: S-NNN` and its evidence table, and the rules of this contract.
- **Module** (`go.mod`, `main.go`): `github.com/muster-io/terraform-provider-muster`, the Go release Muster's `go.mod`
  names; terraform-plugin-framework, terraform-plugin-go, terraform-plugin-testing, oapi-codegen's runtime and what
  they need — HashiCorp's plugin libraries are MPL-2.0 and used unmodified, which the list allows. `make licenses`
  (go-licenses) allows MIT, MIT-0, BSD-2-Clause, BSD-3-Clause, Apache-2.0, ISC, 0BSD, Unlicense, CC0-1.0 and MPL-2.0;
  each dependency is stated in the pull request with its licence, latest release and maintenance status (ADR-0001).
  `main.go` serves `providerserver.NewProtocol6` with the address `registry.terraform.io/muster-io/muster` and a
  `-debug` flag.
- **Specification and client** (`api/openapi.yaml`, `api/MUSTER_VERSION`, `api/codegen.yaml`): `MUSTER_VERSION`
  holds one version, `0.2.0` at first (C-33.FR-4); `make spec-check` downloads
  `https://raw.githubusercontent.com/muster-io/muster/v<version>/api/openapi.yaml` (`MUSTER_SPEC_URL` overrides it)
  and compares it byte for byte; `make spec-sync VERSION=x.y.z` replaces the copy and the version and runs
  `make generate`. oapi-codegen writes the types and the client into `internal/client/gen/` (generated, never listed).
  A daily CI job compares the copy with Muster's `master` and reports the difference in its summary without failing.
- **Client** (`internal/client`): wraps the generated client with the base URL `<endpoint>/api/v1`, the bearer token,
  the `User-Agent`, a timeout of 60 seconds per request, the retries of C-33.FR-17, and `Problem` decoding of
  `application/problem+json` with `code`, `title`, `detail` and `errors`. The token is never logged; `TF_LOG` shows
  requests with `Authorization` as `[redacted]`.
- **Provider** (`provider.go`, `version.go`): the attributes `endpoint` (URL) and `token` (sensitive); `Configure`
  calls `getSystemStatus` and compares its `version` as semver with the minimum, the constant `0.2.0` in
  `version.go`, the release that carries `managed_by`. Errors and the warning are those
  of the acceptance statements; the messages name the variable or the attribute that was used.
- **Resource machinery** (`resourcekit.go`), used by every resource of S-183 to S-187:
  - `etag`, a computed attribute of every resource that stores the object's ETag from each read and write; updates and
    deletes send it as `If-Match`; `412` → the error "{Kind} "{name}" changed in Muster since Terraform last read it.
    Run terraform plan again, review the change and apply." with no retry.
  - For an object that two resources write (C-33.FR-8, FR-16): a lock per object `public_id` (or per list, for the
    Route order) held by the provider process from the fresh read to the write; the fresh read; the comparison of the
    part the resource manages with its prior state, which ends in the `412` error above when they differ; and the
    write with the ETag of that read.
  - `managed_by` `terraform` set on every create and update body, under OpenTofu too; the value is not configurable
    (C-33.FR-13).
  - Import by `public_id` with `ImportStatePassthroughID`, and the parsing of composite ids `<a>/<b>`.
  - A `404` on a read removes the resource from the state.
  - Problems → diagnostics: a `422` error pointer `/a/0/b` → the attribute path `a[0].b` where the resource maps
    that field, else a diagnostic on the resource with the pointer in its detail.
  - The pair of Secret attributes of C-33.FR-10 — `<name>` sensitive, `<name>_wo` write-only with
    `<name>_wo_version` — with the validator that allows at most one of the two and the plan modifier that sends the
    write-only value only when its version changes; the error on a CLI without write-only support comes from the
    framework's client capabilities and names Terraform 1.11 and OpenTofu 1.11. The first resource with a Secret
    (S-183) proves it end to end.
- **Acceptance harness** (`internal/acctest`, `test/muster-dev.sh`): `test/muster-dev.sh up` runs `postgres:17` on
  `127.0.0.1:55432` and `docker run --network host $MUSTER_IMAGE dev`, waits for `/health/ready` on `:8082`, signs in
  as `admin@example.org` with `muster-dev-password`, creates the Service accounts `terraform-acc` (Admin) and
  `terraform-responder` (Responder) with a token each, and prints `MUSTER_ENDPOINT`, `MUSTER_TOKEN` and
  `MUSTER_RESPONDER_TOKEN` for `make testacc`; `down` removes both containers. The fake servers of `muster dev` serve
  the Connections of the acceptance tests; the development clock is not used. `acctest.go` gives the
  `ProtoV6ProviderFactories`, a `PreCheck` that needs the variables, and helpers that call the API directly to change
  an object behind Terraform's back for the drift and `412` tests.
- **CI** (`ci.yml`, `acceptance.yml`): `ci.yml` on pull requests, pushes to `master`, daily and by hand — jobs `lint`
  (golangci-lint, the header check), `test` (`go test -race`, coverage at least 80 % of `internal/` without
  `internal/client/gen/`), `generate`, `spec`, `docs`, `licenses`, `vulncheck`, `goreleaser` (`check` and a snapshot
  build), and `ci`, which passes only when all of them did. `acceptance.yml` — on pull requests and pushes to
  `master`, a matrix of Terraform `1.11.*` and the newest `1.x`, and OpenTofu `1.11.*` and the newest `1.x`, against
  the image of `api/MUSTER_VERSION`; daily, the newest of each against `edge`; a job `old-cli` with Terraform `1.10.*`
  and OpenTofu `1.10.*` runs only the tests named `TestAcc*_oldCLI`, which check that those CLIs refuse a write-only
  attribute and accept the sensitive one; its jobs `acceptance` and `old-cli` join `ci`'s required checks. Terraform
  1.11 and OpenTofu 1.11 are the lowest supported versions because the write-only Secrets need them; the sensitive
  attributes would work on older ones, which the documentation says without testing them beyond `old-cli`.
- **Documentation** (`templates/index.md.tmpl`, `examples/provider/provider.tf`): tfplugindocs (MPL-2.0, tooling)
  generates `docs/`, which the Registries read; the index shows the provider block, the variables, the Admin Service
  account and the minimum Muster version. Every later resource adds `examples/resources/<name>/resource.tf` and
  `import.sh`, every data source `examples/data-sources/<name>/data-source.tf`.
- **Releases** (`.goreleaser.yml`, `terraform-registry-manifest.json`, `release-please*`, `release.yml`):
  release-please with the release type `go`, `bump-minor-pre-major` and `bump-patch-for-minor-pre-major`, the changelog
  sections of the bridge libraries and the manifest at `0.0.0`, run with the GitHub App's token as in Muster's
  repository. Its tag `vX.Y.Z` starts `release.yml`, which imports the key from the secrets `GPG_PRIVATE_KEY` and
  `PASSPHRASE` and runs GoReleaser: `CGO_ENABLED=0`, `-trimpath`, `-X main.version`, for `linux`, `darwin`, `windows`
  and `freebsd` on `amd64` and `arm64`, and `linux` on `arm` and `386`; zip archives, `SHA256SUMS` signed with
  `gpg --batch --detach-sign`, the manifest as a release asset. Without the secrets the workflow fails before it builds
  anything, naming them; they are S-188's.
- **Renovate, CodeQL, Scorecard**: as in `muster-io/muster-bridge-go` (S-083), with Terraform and OpenTofu versions
  of the acceptance matrix and the Muster image of the daily job left to the maintainer, not to Renovate. Every Action
  is pinned by commit SHA.

## Steps

1. Add the module, `main.go`, the Makefile, the copy of the specification with its version and the generated client.
   Check: `make generate-check`, `make spec-check`.
2. Write the client and the problems with their tests. Check: `client_test.go`, `problem_test.go`.
3. Write the provider, the version check and `resourcekit` with their tests. Check: `go test ./internal/...`.
4. Write the acceptance harness and `TestAccProvider_configure`. Check: `make testacc`.
5. Add the documentation generation, GoReleaser, release-please, the workflows and the contributor files. Check:
   `goreleaser check`, `make docs-check`, and the pull request's checks, all green.

## Verification

In a clone of `muster-io/terraform-provider-muster`:

```sh
make ci                                                   # lint, generate-check, spec-check, docs-check, licenses, test
eval "$(test/muster-dev.sh up)"                           # MUSTER_ENDPOINT, MUSTER_TOKEN, MUSTER_RESPONDER_TOKEN
make testacc TESTARGS='-run TestAccProvider'               # PASS
mkdir -p /tmp/tfp && cd /tmp/tfp && printf 'provider "muster" {}\n' > main.tf
cat > ~/.terraformrc <<EOF
provider_installation { dev_overrides { "registry.terraform.io/muster-io/muster" = "$HOME/go/bin" } direct {} }
EOF
(cd - && go install .) && terraform plan                  # No changes. Your infrastructure matches the configuration.
MUSTER_TOKEN=$MUSTER_RESPONDER_TOKEN terraform plan 2>&1 | grep -c 'needs the Admin Role'                    # 1
MUSTER_TOKEN=wrong terraform plan 2>&1 | grep -c MUSTER_TOKEN                                                # 1
cd - && test/muster-dev.sh down
MUSTER_IMAGE=ghcr.io/muster-io/muster:0.1.4 test/muster-dev.sh up > /tmp/env && eval "$(cat /tmp/env)"
cd /tmp/tfp && terraform plan 2>&1 | grep -o 'Muster 0.1.4 is older than 0.2.0.*'
# Muster 0.1.4 is older than 0.2.0, the oldest version this provider works with
cd - && test/muster-dev.sh down
cp api/openapi.yaml /tmp/spec.yaml && printf ' ' >> api/openapi.yaml && make spec-check; echo "exit=$?"   # … exit=2
cp /tmp/spec.yaml api/openapi.yaml
goreleaser release --snapshot --clean --skip=sign && ls dist/*.zip | wc -l                                   # 10
grep -L 'SPDX-License-Identifier: MIT' $(git ls-files '*.go' ':!internal/client/gen/*')                       # (nothing)
```

The pull request's checks show `ci` and `acceptance` green on the four CLI versions; its evidence table carries the
output above, and its body says `Part of muster-io/muster#<issue of S-182>`. The Service account of the 0.1.4 run is
made with the API of 0.1.4, which has Service accounts (C-04).

## Open questions

None.

## Notes

- Suggested commit: `feat: add the provider skeleton with its client, configuration and release pipeline`.
- Starts after Muster's tag `v0.2.0` (journal D757), which carries S-179 to S-181: `depends_on` names S-181, the last
  of them, since it cannot name a tag, and the tag is the maintainer's to announce. `api/MUSTER_VERSION` starts at
  `0.2.0`.
- **Maintainer** (operator attention): install on this repository the GitHub App that release-please uses in Muster's
  repository and give it the variable `RELEASE_PLEASE_APP_CLIENT_ID` and the secret `RELEASE_PLEASE_APP_PRIVATE_KEY`;
  enable Renovate; after the first push, require the checks `ci` and `acceptance` on `master`; turn on private
  vulnerability reporting. The signing key and the Registries are S-188's; leave the first release pull request open
  until then.
- The provider is a client written for Terraform, not copied from Muster: Muster's code is AGPL and cannot move into an
  MIT repository; only the MIT specification is copied.
- `muster dev` refuses its published master key outside development mode, so the acceptance tests never run against
  a production-like configuration; that is intended — they test the provider, not Muster.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-33.FR-1 | full | |
| C-33.FR-2 | partial | the copy, its check and the client; the licence in Muster is S-179 |
| C-33.FR-3 | full | |
| C-33.FR-4 | full | |
| C-33.FR-8 | partial | the machinery; each resource uses it in S-183 to S-187 |
| C-33.FR-17 | partial | the machinery; the refusals of each kind are shown by S-183 to S-187 |
| C-33.FR-18 | partial | the generation; the pages come with each resource and the guides with S-188 |
| C-33.FR-19 | partial | the harness and CI; each resource adds its acceptance tests |
| C-33.FR-20 | partial | the pipeline; the key, the Registries and the first release are S-188 |
| C-33.AC-1 | full | |
| C-33.AC-15 | full | |
