# Contributing to Muster

Thank you for helping. This guide covers how work is proposed, how a pull request is built and what it must prove.
[`AGENTS.md`](AGENTS.md) is the detailed guide for everyone who writes code for Muster, people and coding agents alike;
read it before your first pull request. Terms such as Alert Group, Route or Destination have the meaning given in
[`CONTEXT.md`](CONTEXT.md).

## Proposing work

Open an issue with one of the forms:

- **Bug** — something does not work as documented.
- **Feature** — a change in behaviour you would like to see.
- **Integration request** — an alert source or a messenger Muster should talk to.
- **Story proposal** — a piece of work described as a problem, a proposed capability and ideas for its acceptance.

Implementation work is planned as story contracts in [`design/stories/`](design/stories/README.md). A story is one pull
request that delivers one observable behaviour; its issue, titled `S-NNN: <title>`, is generated from the story file.

## Development setup

You need Go 1.27, Node 24 (see `.node-version`), pnpm 12, GNU make and git. With the default `GOTOOLCHAIN=auto`, the
`go` command fetches the toolchain that `go.mod` asks for. Install pnpm with `npm install -g pnpm@12`: it switches to
the exact version that `packageManager` in `web/package.json` pins. The Makefile runs pnpm from the repository root,
which has no `package.json`, so with corepack also run `corepack install -g pnpm@<that version>`; otherwise corepack
starts its default pnpm, which refuses the project. The Makefile installs its pinned tools (golangci-lint,
go-test-coverage, go-licenses, govulncheck, gremlins, helm, kubeconform) into `bin/tools/` on first use, built with the
project's toolchain. `make compose-check` also needs the Docker CLI with the compose plugin, but no running daemon;
`make dev-db` and `make dev` need Docker with the compose plugin and a running daemon.

The Makefile is the only entry point:

| Target | Does |
|---|---|
| `make help` | lists the targets (the default goal) |
| `make fmt` | formats Go code (gofmt and goimports through golangci-lint) and the SPA (oxfmt) |
| `make lint` | checks licence headers, runs golangci-lint, Redocly on the spec, TypeScript type check, oxlint and oxfmt check |
| `make lint-arch` | runs the architecture lints (see AGENTS.md) |
| `make test` | runs the unit tests and the coverage gate: 95 % for `internal/groups`, `routing`, `delivery` and `timers`, 80 % for every other package |
| `make test-race` | the same with the race detector |
| `make generate` | runs code generators for the OpenAPI server and clients and the route tree |
| `make generate-check` | regenerates and fails if generated files are not current |
| `make build` | builds the SPA, then `bin/muster` with the version and the commit |
| `make licenses` | checks dependency licences for shipped artifacts and reports tooling licences |
| `make vulncheck` | finds vulnerable Go code and high SPA advisories |
| `make mutation` | runs mutation testing over the core packages |
| `make helm-check` | lints the chart, renders it with default and all-options values, validates both with kubeconform |
| `make compose-check` | checks the compose example with `docker compose config` |
| `make dev-db` | starts the development PostgreSQL on `127.0.0.1:55432` with Docker Compose |
| `make dev` | starts the development PostgreSQL, builds the binary and runs `muster dev` |
| `make e2e` | builds the binary and runs the end-to-end suite on `muster dev`; `E2E_REPLICAS=2` runs two replicas |
| `make load-test` | runs the load test against a running `muster dev`; `LOAD_TEST_FLAGS` passes `-rate` and `-duration` |
| `make ci` | runs the pull-request tier locally: `lint`, `lint-arch`, `generate-check`, `licenses`, `test-race`, `build` |
| `make clean` | removes `bin/`: the binary, the coverage profile and the installed tools |

`muster dev` starts the fake Alertmanager, Mattermost and Telegram servers on `127.0.0.1:19093`, `127.0.0.1:18065`
and `127.0.0.1:18081` and prints their addresses; each records its requests at `/_fake/requests` and takes scripted
faults at `/_fake/faults`. Muster itself joins them with the runtime, which a later story adds: from the runtime on,
`muster dev` also listens on `:8080` (app), `:8081` (ingest) and `:8082` (internal) and connects to the development
database, and `muster dev --replica` runs an additional replica on `:9080`, `:9081` and `:9082`, without fake servers,
against the same database; until then the replica only prints that it started and waits. Every `MUSTER_*` variable
with a development default takes it unless the variable is set, even empty, and `muster dev <command>` runs another
command with the same defaults. The database default also yields to any field of the main connection
(`MUSTER_DATABASE_HOST`, `_PORT`, `_NAME`, `_USER`, `_PASSWORD`, `_PASSWORD_FILE`, `_SSLMODE`), and the master key and
the Admin password to their `_FILE` variables. `docker compose -f deploy/dev/docker-compose.yml down -v` removes the
development database with its data.

In CI, the `ci` workflow runs the jobs `lint`, `lint-arch`, `test`, `build`, `generate` (`make generate-check`),
`licenses`, `vulncheck`, `helm-check`, `compose-check` (`make compose-check`, then the example's PostgreSQL settings)
and `image-scan` (a snapshot release whose images are checked and scanned with Trivy) on every pull request, with
`make test-race` in `test`, and on every push to `master`, with `make test`. A pull request also runs the `e2e` job
(`make e2e`) and gets the non-blocking `oasdiff` report of breaking changes to the API spec, and the `codeql` workflow
analyses the Go and TypeScript code. The `nightly` workflow runs `mutation` (`make mutation`, its report kept as an
artifact), `vulncheck`, `e2e-two-replicas` (`make e2e E2E_REPLICAS=2`) and `load-test`: the load test against
`muster dev`, then against the compose example started with the build under test, with the peak memory of its Muster
and PostgreSQL containers in the job summary.
Pushes to other branches run nothing: open a pull request, a draft one if the work is not ready, to get the checks.

## Releases

release-please keeps a release pull request open, with the changelog written from the conventional commits on
`master`. Merging it creates a draft release and the `vX.Y.Z` tag. The tag starts the `release` workflow: goreleaser
builds the binaries, the archives with their SBOMs and the checksums, and the multi-arch image
`ghcr.io/muster-io/muster`, signed keylessly with cosign; the workflow then pushes the chart to
`oci://ghcr.io/muster-io/charts/muster` and publishes the release.

## Working a story

1. Every story in its `depends_on` is merged.
2. Branch from `master` as `s-NNN-<slug>`, with the slug of the story file name.
3. Read the contract in full, and the capability files and ADRs it cites. An open question is settled by the
   maintainers before the code that depends on it is written.
4. Stay within `files_touched`. If the contract is wrong or incomplete, correct the story file in the same pull request
   and say why.
5. Give evidence for every `acceptance` statement and run `verify` until it passes.

### Live verification

Unit tests alone never prove a story. Check the behaviour on the running binary, as the story's Verification section
says, and put the commands and their output in the pull request. Never wait for real time: tests use a manual clock,
and live checks advance the development clock.

## Commits and pull requests

- Commit messages are [conventional commits](https://www.conventionalcommits.org/) on one line:
  `<type>(<scope>): <description>`, with `feat`, `fix`, `docs`, `build`, `ci`, `test`, `refactor`, `perf` or `chore`.
  Say what changed; leave out story and phase IDs.
- Pull requests are squash-merged, so the title becomes the commit message, and the changelog is written from it.
- The pull request template asks for the issue it closes, the story ID, a table with every acceptance statement and its
  evidence, the output of `verify`, every change to the story file with its reason, and a dependency statement.

## Licence header

Every source file — `*.go`, `*.sql`, `*.ts`, `*.tsx`, `*.js`, `*.mjs`, `*.css`, `*.sh`, `*.py`, `Makefile`,
`Dockerfile`, chart templates, workflow files and any script that starts with a shebang line — starts with these two
lines in its own comment syntax, after the shebang line if it has one:

```go
// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors
```

Markdown, JSON, lock files, generated files (marked `Code generated … DO NOT EDIT.`, or under a generated path that
AGENTS.md lists) and test fixtures under `testdata` are exempt. A file copied from another project under a permissive
licence keeps its own header, and its path is added to the list in [`NOTICE`](NOTICE), which the check reads.
`make lint` names every file that lacks the header.

## Dependencies and third-party code

- Everything Muster ships — the binary, the image, the web UI bundle and the chart — may depend only on code under MIT,
  MIT-0, BSD-2-Clause, BSD-3-Clause, Apache-2.0, ISC, 0BSD, Unlicense, CC0-1.0 or unmodified MPL-2.0. Anything else
  needs an architecture decision first. Build tooling may use any OSI-approved licence
  ([ADR-0001](design/adr/0001-agpl-license-cla-and-dependency-policy.md)).
- A pull request that adds a dependency states its **licence**, its **latest release date** and its **maintenance
  status**.
- Never copy code from copyleft projects, Grafana OnCall included; read them for ideas only. Permissively licensed code
  may be ported with attribution in `NOTICE`.

## Contributor License Agreement

Contributions are accepted under the [Contributor License Agreement](CLA.md). It is a licence grant, modelled on the
Apache Individual Contributor License Agreement; you keep the copyright in your work. You sign it once, through the
cla-assistant check that appears on your first pull request; a pull request without a signature cannot be merged.

The agreement lets the project offer Muster under terms other than the AGPL in the future. The same reason is behind
the rules on copied code and dependencies above.
