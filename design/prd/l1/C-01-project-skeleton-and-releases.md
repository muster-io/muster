# C-01. Project skeleton and releases

[L1 index](../L1.md) · Stage: Foundation · UI: none · Depends on: —

**Goal.** A repository that builds, checks licenses and invariants, and ships signed, installable artifacts from the
first story, so every later capability lands behind the same gates.

## Scenarios

1. A contributor opens a pull request. CI checks the license header of every file, the CLA signature, formatting,
   linters, architecture lints, tests, generated code and dependency licenses; the pull request cannot be merged while
   any check fails.
2. A maintainer merges the release pull request that release-please keeps open. The tag produces binaries, a multi-arch
   container image with an SBOM and a signature, and a Helm chart in an OCI registry.
3. Someone evaluating Muster copies the docker-compose example, replaces the master key placeholder and has Muster with
   PostgreSQL running in five minutes (the running part arrives with C-02).
4. A developer runs `make dev`, which starts PostgreSQL with Docker Compose and Muster in `muster dev`, wired to fake
   Alertmanager and messenger servers; only Docker is needed.

## Functional requirements

- **C-01.FR-1** The repository is `github.com/muster-io/muster` with default branch `master`; the Go module path is the
  same; the image is `ghcr.io/muster-io/muster`; the Helm chart is published as an OCI artifact in the same registry.
- **C-01.FR-2** Every source file carries `SPDX-License-Identifier: AGPL-3.0-only` and a copyright line; CI fails on a
  file without them. Files copied under a permissive license keep their own header, are excluded from the check by path
  and are listed in `NOTICE` (ADR-0001).
- **C-01.FR-3** Pull requests require a CLA signature collected by cla-assistant.io; the CLA text is `CLA.md`.
- **C-01.FR-4** CI blocks any dependency of a shipped artifact (binary, image, embedded SPA bundle, chart) whose license
  is outside the allowlist of ADR-0001, including transitive dependencies; for build tooling and the documentation site
  it only reports.
- **C-01.FR-5** The Makefile is the single entry point: `make fmt`, `make lint`, `make lint-arch`, `make test`,
  `make generate`, `make build`. `make build` builds the SPA and embeds it in the binary. Generated code (database
  queries, OpenAPI server and clients, UI route tree) is checked in, and CI fails if it is not current.
- **C-01.FR-6** The architecture lints of ADR-0016 run as a separate CI step from the first story.
- **C-01.FR-7** CI runs in tiers: on push, a fast run without the race detector; on pull requests and releases, the full
  run (race detector, integration tests, end-to-end tests); nightly, a mutation-testing report, the load test of NFR-1
  and NFR-2, and an end-to-end run with two replicas. Each nightly job runs what the merged capabilities provide.
- **C-01.FR-8** CI enforces test coverage of at least 95 % for the core — Alert Group lifecycle, grouping, routing,
  delivery and timers — and at least 80 % elsewhere.
- **C-01.FR-9** Releases follow semantic versioning, with a changelog from conventional commits. A release produces
  binaries for linux and darwin on amd64 and arm64, a multi-arch distroless image running as non-root, an SBOM, keyless
  cosign signatures and the Helm chart.
- **C-01.FR-10** The Helm chart defaults `image.tag` to the chart's `appVersion` and `replicas` to 1; reads the database
  settings, master keys and the bootstrap admin password from an existing Secret; renders objects that a cluster may lack
  (monitoring CRDs, ServiceMonitor, NetworkPolicy, dashboard resources) only behind conditions or flags; offers a
  PodDisruptionBudget and two replicas as options; and exposes the app and ingest listeners through optional
  `ingress.*` and `httpRoute.*` values while always creating Services. CI renders the chart with `helm template` and
  validates it with kubeconform.
- **C-01.FR-11** The repository contains `SECURITY.md` (private vulnerability reporting through GitHub; the latest minor
  release is supported) and CI runs govulncheck, npm audit, Trivy on the image, CodeQL, Renovate and OpenSSF Scorecard.
- **C-01.FR-12** `muster version` prints the version and commit; the metric `muster_build_info{version,commit}` exposes
  them once metrics exist (C-02).
- **C-01.FR-13** `muster dev` starts Muster against built-in fake Alertmanager, Mattermost and Telegram servers with a
  demo configuration that exists only in that mode. `make dev` starts PostgreSQL with Docker Compose and runs
  `muster dev` against it, so a developer needs only Docker. End-to-end tests and the nightly load test run on
  `muster dev`; `muster dev <subcommand>` runs any other subcommand with the same development defaults, so the CLI works
  against the development database. Each later capability that talks to an external system extends its fake server. From
  C-06 on, `muster dev` also serves a development clock on the internal listener: `POST /_dev/clock` with the seconds to
  advance moves Muster's clock forward — the virtual clock of acceptance checks — and `GET /_dev/clock` reads it;
  outside `muster dev` the path does not exist.
- **C-01.FR-14** The repository root contains `README.md` (with a roadmap section), `LICENSE` (the AGPL-3.0 text),
  `CONTRIBUTING.md`, `CLA.md`, `CHANGELOG.md`, `SECURITY.md`, `NOTICE`, `CONTEXT.md` and `AGENTS.md`; issue forms for
  bugs, features, integration requests and story proposals; and a pull request template with an evidence table per
  acceptance statement. The wiki and Discussions are off. The README's short demo animation needs a user interface to
  record, so it is added after the first frontend story, not with this capability.
- **C-01.FR-15** The docker-compose example caps PostgreSQL's memory: a container memory limit and PostgreSQL settings
  that fit inside it (`shared_buffers`, `work_mem`, `max_connections`), sized so that the footprint of NFR-3 can hold;
  the nightly load test measures the footprint with these settings (_provisional_, P-43).

## UI

None.

## API surface

None; the CLI gains `muster version` and `muster dev`.

## Acceptance

- **C-01.AC-1** A pull request adding a file without the SPDX header, or a dependency under GPL into the binary, fails
  CI.
- **C-01.AC-2** A release tag yields images for amd64 and arm64 that pass `cosign verify`, an SBOM, and a chart that
  renders and installs from the OCI registry with only an existing Secret supplied.
- **C-01.AC-3** The docker-compose example passes `docker compose config`, and the image it references prints its
  version and commit with `muster version`.
- **C-01.AC-4** On a machine that has only Docker, `make dev` starts PostgreSQL with Docker Compose and runs
  `muster dev` against it; `muster dev` reaches nothing but loopback and that database.
- **C-01.AC-5** The docker-compose example sets a memory limit on the PostgreSQL container, and PostgreSQL started from
  it reports `shared_buffers`, `work_mem` and `max_connections` from the example rather than its built-in defaults.

## Related ADRs

ADR-0001, ADR-0006, ADR-0009, ADR-0016.

## Depends on

Nothing.

## Suggested story split

BE only — one story for the repository, CI tiers and gates; a second for releases, the image, the chart skeleton and
`muster dev` if the first grows too large.
