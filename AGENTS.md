# AGENTS.md

The guide for coding agents, and for humans, who implement Muster: a self-hosted alert grouping and on-call service
that sits after Alertmanager, an open alternative to Grafana IRM. Read it before every task. It summarises the rules
and points to the documents that define them; when a detail matters, read the document, not this summary.

## Precedence

- This file and the documents it points to **override** any instructions from directories above this repository or
  from global agent configuration about code style, frameworks or architecture.
- Do **not** use Gin, GORM or layered "clean architecture" templates (domain/application/infrastructure/interface
  directories, use cases, ports and adapters everywhere). The layout is
  [ADR-0016](design/adr/0016-flat-domain-packages-command-layer-and-architecture-lints.md): flat domain packages, flat
  CRUD, and one command layer for Alert Groups.
- Where these documents are silent, ask in the pull request instead of importing conventions from elsewhere.

## Sources of truth

In this order; an earlier source wins over a later one. A contradiction is a defect: fix the later document in the
same pull request, or raise it there when the fix belongs in an ADR or the PRD.

1. [`CONTEXT.md`](CONTEXT.md), the glossary. Its vocabulary is binding in identifiers, API fields, UI texts, log
   events, docs and commit messages; never use the words it lists under _Avoid_.
2. The [ADRs](design/adr/README.md): every technical decision and its reasons.
3. The PRD: [`L1.md`](design/prd/L1.md) (conventions, non-functional requirements, open questions), one capability
   file per `C-NN` in [`design/prd/l1/`](design/prd/l1/) with its `C-NN.FR-n` and `C-NN.AC-n`,
   [`reference.md`](design/prd/l1/reference.md) (shared tables), [`defaults.md`](design/prd/l1/defaults.md) (every
   setting, cited as `area.setting` or `MUSTER_*`) and [`facts.md`](design/facts.md) (verified behaviour of
   Alertmanager, Mattermost and Telegram, `F-NNN`).
4. The story contracts in [`design/stories/`](design/stories/README.md); [`coverage.md`](design/stories/coverage.md)
   maps every FR and AC to its stories.
5. [`api/openapi.yaml`](api/openapi.yaml), **spec-first**: the API changes in the spec, then code is generated from it.
   Conventions and the `public_id` format are in [`api/README.md`](api/README.md).
6. The designed migration [`0001_init`](design/db/migrations/0001_init.up.sql), explained in
   [`schema.md`](design/db/schema.md). S-006 moves it unchanged to `internal/db/migrations/`.

[`architecture.md`](design/architecture.md) draws the system from these documents and adds no decisions; read it for
the flows and state machines.

## Working a story

A story is one pull request that delivers one observable behaviour and proves it on the running binary. Format,
lifecycle and story map: [`design/stories/README.md`](design/stories/README.md).

1. **Pick** a story by its ID or its issue (`S-NNN: <title>`). Every story in `depends_on` must be merged.
2. **Branch** from `master` as `s-NNN-<slug>`, with the slug of the story file name.
3. **Read the contract in full.** Frontmatter: `capability`, `kind` (`be`, `fe`, `infra`, `docs`), `depends_on`,
   `covers`, `files_touched`, `acceptance`, `verify`, `operator_attention`, `issue`. Body: **Scope** (IN, and OUT with
   the stories that own it), **Contracts** (operations, tables, metrics, log events, settings, CLI commands and pages,
   referenced by name and defined in the spec, the schema and the PRD), **Steps**, **Verification**, **Open
   questions**, **Notes**, **Coverage**. Then read the capability file for every ID in `covers` and the ADRs it cites.
4. **Stop on open questions.** An unresolved Open question is settled by the maintainer before the code depending on
   it is written. With `operator_attention: true` the Notes say what the maintainer does (accounts, repository
   settings, product values); do not attempt those steps; list them in the pull request.
5. **Stay within `files_touched`.** It lists every hand-written file, tests included; generated files are never listed.
   If the contract is wrong or incomplete, correct the story file in the same pull request, before the code that
   relies on the correction, and say why. Past about 30–40 files, propose a split instead.
6. **Work the Steps in order**; each ends with a check, so run it.
7. **Give evidence for every `acceptance` statement**: a named test, or a live check with its output.
8. **Verify live** against the running binary as the Verification section says: `curl`, `psql` and the CLI for the
   backend; Playwright with the expected visible text for the frontend, at 360 px wide where the story asks. Unit tests
   alone never count. Trigger the path that creates the data (a webhook, a command, a scan) and advance the development
   clock for anything time-based; never wait for a timer. S-009, S-034 and S-035 (and parts of S-036), which have no API
   or messenger of their own, use `go test -tags integration` against the development database as their live check.
9. **Run `verify`** (it always starts with `make`) until it passes.
10. **Open the pull request** as described under [Commits and pull requests](#commits-and-pull-requests).

In the same pull request as the code: register the story's log events, metrics and Internal alerts and regenerate
their reference pages; extend the fake server of every external system the story talks to; a changed setting or a
confirmed provisional value updates `defaults.md` and its `P-NN` in [L1 §5.3](design/prd/L1.md#53-provisional-values-and-behaviours);
a schema change is a new expand/contract migration plus an update of `schema.md`; a wrong API contract is fixed in
`api/openapi.yaml`.

Issues are generated from the story files by `scripts/stories-sync`. Edit the files, never the issues, and never edit
the `issue:` field by hand.

## Stack

- **Go 1.27**, module `github.com/muster-io/muster`, one binary `muster`: the server and its CLI subcommands.
- **HTTP**: the standard library `net/http` `ServeMux` with the oapi-codegen v2.8 `std-http-server` + `strict-server`
  (ADR-0008); request validation by `oapi-codegen/nethttp-middleware`; middleware are plain
  `func(http.Handler) http.Handler`. No router or web framework.
- **Data**: PostgreSQL 14 or newer, nothing else stateful (ADR-0006). `pgx` + `sqlc`, with hand-written SQL in
  `internal/<domain>/query.sql`; golang-migrate migrations embedded in the binary. No ORM, no Redis, no queue library:
  ingestion, delivery and timers are rows claimed with `FOR UPDATE SKIP LOCKED` and a lease, woken by `LISTEN/NOTIFY`.
- **Logs** through the domain logger over `log/slog`; **metrics** with VictoriaMetrics/metrics (ADR-0014).
- **Bootstrap settings** with `caarlos0/env` + `go-playground/validator`, for `MUSTER_*` variables only (ADR-0010).
- **Templates**: Go templates in the sprout sandbox with explicitly registered functions only (ADR-0012).
- **SPA** in `web/` (ADR-0009): TypeScript 7, Vite 8, pnpm, React 19, TanStack Router (file-based routes), TanStack
  Query 5, TanStack Table 9, shadcn/ui on Base UI, Tailwind CSS 4, the orval client (fetch, Query hooks, Zod schemas,
  MSW mocks), React Hook Form 7 with Zod 4, i18next, date-fns 4, oxlint and oxfmt, Vitest browser mode and Playwright.
  `make build` builds it and embeds it in the binary with `go:embed`.

## Layout

| Path | Content |
|---|---|
| `cmd/muster/` | the binary; `main.go` only calls `internal/cli` |
| `cmd/muster-archlint/`, `internal/archlint/` | the architecture lints and their fixtures (build tag `lint`) |
| `internal/<domain>/` | one flat package per domain (`groups`, `routing`, `ingest`, `delivery`, `destinations`, …) with its `query.sql` |
| `internal/api/` | hand-written API handlers, one file per OpenAPI tag, and the middleware |
| `internal/{logging,metrics,outbound,keyring,db,clock,runtime,cli,devmode,…}/` | shared infrastructure |
| `internal/db/migrations/` | the migrations, also the schema `sqlc` reads |
| `internal/fakes/<name>/` | fake Alertmanager, Mattermost, Telegram, OIDC and proxy servers |
| `api/`, `web/`, `deploy/`, `docs/`, `test/` | the spec; the SPA; chart, compose and dev database; the docs site; e2e, load and docs checks |

The full table, with every domain package and the list of generated paths, is
[§2 of the stories README](design/stories/README.md#2-repository-layout).

## Architecture rules

- **Flat CRUD.** A handler in `internal/api` calls its domain package, which validates, writes through its generated
  queries and records the Audit log with a before/after diff; nothing sits in between. An interface is declared by its
  consumer, and only where a second implementation exists or is planned.
- **Alert Groups change only through the dispatcher in `internal/groups`**: permission → precondition → transition →
  Audit log → Timeline → re-render. Transports (UI, API, Mattermost, Telegram) are thin adapters that build a Command;
  system transitions use the same dispatcher with Muster as the actor and the Transport `system`.
- **Messenger messages change only through delivery** (ADR-0005): the delivery worker reconciles Root messages to the
  Desired state, and the interactive path answers people. Nothing else sends or edits.
- **Configuration lives in the database and changes only through the API** (ADR-0010). Never add an environment
  variable for a product setting; `HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY` are never read. Data needed at first start
  (the Organization and its built-ins, the Keyring state, the bootstrap Admin) comes from idempotent start-up steps,
  never from a migration.
- **`org_id` in every query** on a table that has it, worker claims, Leader scans and retention included. The only
  exemption is an `-- archlint:org-exempt <reason>` comment on the lookups by hash or by Connection that `schema.md`
  names.
- **Two clocks** in `internal/clock`, always injected: never `time.Now()` in domain code, never SQL `now()`. The
  **business clock** is domain time (timestamps, timers, windows, retention, sessions) and follows the development
  clock; the **real clock** is for what must agree with the outside world (TOTP, ID-token times, webhook signatures,
  row and Leader leases, clock skew, replica records). Which consumer takes which clock is the table in S-020.
- **Outbound HTTP only through `internal/outbound`** (ADR-0015): clients per client class, the outbound address policy
  checked after name resolution, no redirects, per-client proxies, secrets redacted from logs and errors.
- **Secrets only through `internal/keyring`** (ADR-0011): AES-256-GCM with a key id, write-only in the API. Tokens
  Muster issues are hashed, not encrypted. A secret in a log line is a `logging.Secret`, written as `[redacted]`; new
  code paths that carry secrets get probes in the lint-5 harness.
- **Log events, metrics and Internal alerts come only from their closed registries**: `internal/logging/events.go`,
  `internal/metrics/catalogue.go`, `internal/internalalerts`. Metric labels name entities by `public_id`, never by
  alert labels, users, Alert Group numbers or the Organization.
- **Leader work** is a closed list, and every Leader task is safe to run twice (ADR-0007).
- **API conventions**: `public_id` in URLs, RFC 9457 problems with catalogued codes, cursor pagination with no `OFFSET`,
  ETag with `If-Match` on configuration, durations in `*_seconds`, snake_case enums; details in
  [`api/README.md`](api/README.md). An operation that no story has implemented answers `501`.

### Architecture lints (`make lint-arch`)

| # | Fails when |
|---|---|
| 1 | a query on a table with `org_id` does not filter by it |
| 2 | `alert_groups`, `alert_group_alerts`, `timeline_entries`, `notes` or `alert_group_counters` is written outside `internal/groups` |
| 3 | a messenger send or edit is called outside the delivery worker and the interactive path |
| 4 | an `http.Client` or `http.Transport`, `http.DefaultClient` or `http.Get`/`Post`/`Head`/`PostForm` is used outside `internal/outbound` (fakes, dev mode, the load test, the generated client and tests excepted) |
| 5 | a known secret value reaches a log line or a returned error |
| 6 | `context.Background()` is used outside `cmd/`, `internal/runtime`, `internal/cli`, `test/load/main.go` and tests |
| 7 | `log`, `log/slog`, `fmt.Print*` or direct writes to stdout and stderr appear outside `internal/logging` (`internal/cli`, `internal/devmode`, `test/load` and the build tooling `cmd/muster-archlint` and `internal/tools` may print) |
| 8 | a VictoriaMetrics `vmrange` histogram is constructed; only Prometheus `le` histograms are allowed |

A false positive is fixed in the lint, never worked around in the code.

## Make targets

The Makefile is the only entry point. Targets appear as the stories that define them land; the Makefile is
authoritative.

| Target | Does | From |
|---|---|---|
| `make fmt` | gofmt and goimports, later oxfmt | S-001 |
| `make lint` | golangci-lint and the licence headers; later Redocly on the spec, oxlint and translations | S-001 |
| `make lint-arch` | the architecture lints | S-001 |
| `make test`, `make test-race` | unit tests with the coverage gate; the same with the race detector | S-001 |
| `make generate`, `make generate-check` | run every generator; fail on stale generated files | S-001, S-002 |
| `make build` | the SPA, then `bin/muster` with version and commit | S-001, S-002 |
| `make ci` | the pull-request tier, locally | S-001 |
| `make licenses` | the dependency licence check | S-002 |
| `make helm-check`, `make compose-check` | render and validate the chart; check the compose example | S-003 |
| `make dev-db`, `make dev` | the development PostgreSQL; PostgreSQL, build and `muster dev` | S-004 |
| `make e2e` | the end-to-end suite; `E2E_REPLICAS=2` runs two replicas | S-004 |
| `make load-test` | the load test against `muster dev` | S-004 |
| `make test-integration` | integration tests on PostgreSQL 14 and 17 | S-006 |

### Development mode

- `make dev` starts PostgreSQL on `127.0.0.1:55432` with Docker Compose, builds the binary and runs `muster dev`: app
  on `:8080`, ingest on `:8081`, internal on `:8082` (health, `/metrics`, `/_dev/`). The fake Alertmanager, Mattermost
  and Telegram servers listen on `127.0.0.1:19093`, `:18065` and `:18081`; each records requests at
  `/_fake/requests` and takes scripted faults at `/_fake/faults`.
- Development defaults: the bootstrap Admin `admin@example.org` with the password `muster-dev-password`, and a fixed,
  published master key that the server refuses outside development mode. A `MUSTER_*` variable that is set replaces
  its default.
- `muster dev <subcommand>` runs any CLI subcommand with the same defaults, for example `./bin/muster dev doctor`. A
  bare `muster <subcommand>` has no development defaults. `muster dev --replica` adds a replica on `:9080`, `:9081` and
  `:9082` without fakes.
- The development clock (from S-020): `curl -s -X POST localhost:8082/_dev/clock -d '{"advance_seconds": 600}'`, and
  `GET` to read it. It moves the business clock of every replica of the database, never the real clock. An advance
  past `auth.session_idle_timeout` ends sessions, so create a Personal access token before it.

## Quality bar

- `make fmt` and `golangci-lint` are clean; every `//nolint` states its reason.
- Coverage of at least 95 % in `internal/groups`, `routing`, `delivery` and `timers`, and 80 % elsewhere; pull requests
  run the race detector.
- Generated code (`internal/*/dbgen/`, `internal/api/gen/`, `pkg/apiclient/`, `web/src/api/gen/`,
  `web/src/routeTree.gen.ts`, `docs/reference/`, lock files) is checked in and current: change the input, run
  `make generate`, commit the output.
- Shipped artifacts (binary, image, SPA bundle, chart) depend only on MIT, MIT-0, BSD-2-Clause, BSD-3-Clause,
  Apache-2.0, ISC, 0BSD, Unlicense, CC0-1.0 or unmodified MPL-2.0 code; anything else needs an ADR first. Build tooling
  may use any OSI-approved licence. A pull request that adds a dependency states its licence, latest release date and
  maintenance status (ADR-0001).
- Never copy code from copyleft projects, Grafana OnCall included; read them for ideas only. Permissively licensed
  code may be ported with attribution in `NOTICE`.
- Every source file starts with the licence header in its own comment syntax, after a shebang line if it has one:

  ```go
  // SPDX-License-Identifier: AGPL-3.0-only
  // Copyright The Muster Authors
  ```

  Markdown, JSON, lock files, generated files and fixtures are exempt; copied third-party files keep their own header
  and are listed in `NOTICE`.
- Every user-facing text goes through i18n, in English and Russian (`web/src/locales/`), Russian plurals included; a
  missing key fails CI. Built-in message texts exist in both languages.
- The Alert Group list and page work 360 px wide without horizontal scrolling (NFR-15).
- GitHub Actions are pinned by commit SHA.

## Commits and pull requests

- Conventional commits on one line, `<type>(<scope>): <description>`, with `feat`, `fix`, `docs`, `build`, `ci`,
  `test`, `refactor`, `perf` or `chore`. Say what changed; no story or phase IDs. The story's Notes suggest a message.
- No AI attribution: no `Co-Authored-By` trailers for tools, no "Generated with" lines, in commits or pull requests.
- Pull requests are squash-merged, so the title is the commit message, and release-please writes the changelog from it.
- The pull request body has `Closes #<issue>`, `Story: S-NNN`, a table with every `acceptance` statement and its
  evidence (test name, command with output, Playwright trace or screenshot), the output of `verify`, and every change
  to the story file with its reason.
- Contributions are accepted under the CLA in `CLA.md`, signed once through the cla-assistant check on the pull
  request; a pull request without a signature cannot be merged.

## Don't

- Edit generated files by hand.
- Widen the scope beyond the story: no drive-by refactors or extra features. Propose a new story instead.
- Change an ADR, the PRD, the spec or the schema silently. The spec and the schema change in the story's pull request
  when the contract is wrong (the schema through a new migration, never by editing an applied one); a change to an ADR
  or to the PRD is proposed in the pull request for the maintainer to decide.
- Add telemetry, analytics or email: Muster sends none (NFR-7).
- Add a dependency without the licence statement above, or any web framework, router, ORM, cache or message queue.
- Log, return or print a secret, a token or a URL that carries one.
- Wait for real time in tests or checks: use a manual clock in tests and the development clock live.
- Call a story done on unit tests alone, or report a check that was not run.
- Edit the `issue:` field of a story file, or edit the generated issues on GitHub.
