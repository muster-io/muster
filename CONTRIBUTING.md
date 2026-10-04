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

You need Go 1.27, GNU make and git. With the default `GOTOOLCHAIN=auto`, the `go` command fetches the toolchain that
`go.mod` asks for. The Makefile installs its pinned tools (golangci-lint, go-test-coverage) into `bin/tools/` on first
use, built with the project's toolchain; nothing else needs to be installed.

The Makefile is the only entry point:

| Target | Does |
|---|---|
| `make help` | lists the targets (the default goal) |
| `make fmt` | formats Go code (gofmt and goimports through golangci-lint) |
| `make lint` | checks the licence header of every source file, then runs golangci-lint |
| `make lint-arch` | runs the architecture lints (see AGENTS.md) |
| `make test` | runs the unit tests and the coverage gate: 95 % for `internal/groups`, `routing`, `delivery` and `timers`, 80 % for every other package |
| `make test-race` | the same with the race detector |
| `make generate` | runs every code generator (none yet) |
| `make build` | builds `bin/muster` with the version and the commit |
| `make ci` | runs the pull-request tier locally: `lint`, `lint-arch`, `test-race`, `build` |
| `make clean` | removes `bin/`: the binary, the coverage profile and the installed tools |

A push to any branch runs the fast tier in CI (without the race detector); a pull request runs the full tier.

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

Every source file — `*.go`, `*.sql`, `*.ts`, `*.tsx`, `*.js`, `*.mjs`, `*.css`, `*.sh`, `Makefile`, `Dockerfile`, chart
templates and workflow files — starts with these two lines in its own comment syntax, after a shebang line if it has
one:

```go
// SPDX-License-Identifier: AGPL-3.0-only
// Copyright The Muster Authors
```

Markdown, JSON, lock files, generated files and test fixtures under `testdata` are exempt. A file copied from another
project under a permissive licence keeps its own header, and its path is added to the list in [`NOTICE`](NOTICE), which
the check reads. `make lint` names every file that lacks the header.

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
