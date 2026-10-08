# Muster stories

- Status: Draft
- Date: 2026-10-04

A story is the unit of implementation of Muster: one pull request that delivers one observable piece of a capability
and proves it on the running binary. Each story is a **contract file** `S-NNN-<slug>.md` in this directory. The file
says what the story delivers, which contracts it implements — OpenAPI operations, tables, metrics, log events, settings,
CLI commands — which files it touches, how its acceptance is checked and what is still open. It does not contain the
code: the implementation writes the code in the repository against compiler and test feedback, and the contract is what
the review checks the code against.

Stories implement the [L1 capabilities](../prd/L1.md#13-capability-map) `C-01`…`C-21`. Every functional requirement
(`C-NN.FR-n`) and acceptance statement (`C-NN.AC-n`) of a capability is covered by its stories; the
[coverage table](coverage.md) shows which story covers which ID. Terms follow [`CONTEXT.md`](../../CONTEXT.md); the
reasons behind technical choices are in the [ADRs](../adr/README.md).

Contents: [1. File format](#1-file-format) · [2. Repository layout](#2-repository-layout) ·
[3. Sizing and slicing](#3-sizing-and-slicing) · [4. Shared contracts](#4-shared-contracts) ·
[5. Verification](#5-verification) · [6. IDs](#6-ids) · [7. Lifecycle](#7-lifecycle) · [8. Story map](#8-story-map)

---

## 1. File format

A story file is Markdown with a YAML frontmatter block.

### 1.1 Frontmatter

```yaml
---
id: S-042
title: Heartbeat endpoint, states, Leader checks and staleness pause (BE)
capability: C-07
kind: be
layer: L1
depends_on: [S-021]
covers: [C-07.FR-1, C-07.FR-3, C-07.AC-1]
files_touched:
  - internal/heartbeat/heartbeat.go
  - internal/heartbeat/query.sql
acceptance:
  - "[C-07.AC-1] A Heartbeat request with a valid Integration token answers 204 and makes the state live."
verify: "make ci"
operator_attention: false
issue: null
---
```

| Field | Meaning |
|---|---|
| `id` | `S-NNN`, see [IDs](#6-ids). |
| `title` | What the story delivers, in a few words; ends with `(BE)` or `(FE)` for backend and frontend stories. |
| `capability` | The capability `C-NN` the story belongs to. |
| `kind` | `be` (backend: Go, SQL, OpenAPI), `fe` (the web UI), `infra` (build, CI, release, deployment) or `docs` (the documentation site). |
| `layer` | `L1` for every story of the first release. |
| `depends_on` | Stories that must be merged first. The graph is acyclic. A dependency usually has a smaller ID; a part split off later takes the next free ID (section 6) but keeps its place in the phase order, so stories with smaller IDs may depend on it — S-061, split from S-039, comes before S-040. The story map lists stories in an order that respects every dependency. A frontend story depends on the backend story of its capability. |
| `covers` | The FR and AC IDs the story covers, fully or in part; the body's coverage table says which. Required by the [L1 conventions](../prd/L1.md#2-conventions). |
| `files_touched` | Every hand-written file the story creates or changes, tests included. Generated files are never listed ([section 2](#2-repository-layout)). A path ending in `/**` stands for a directory of small fixtures and counts as one entry. |
| `acceptance` | Testable statements. Each starts with the FR or AC IDs it proves, in brackets, and is checked by a test or by the live checks of the body. |
| `verify` | The automated check of the story; it always starts with `make`. |
| `operator_attention` | `true` when the operator has to act or decide during the story: accounts and settings outside the repository, or a product value the story confirms. The Notes say why. |
| `issue` | The GitHub issue that tracks the story. `null` until the script that creates issues fills it; never edited by hand. |

The file has no status field. Whether a story is open, in progress or done lives only in its issue and on the project
board.

### 1.2 Body

| Section | Content |
|---|---|
| **Scope** | **IN** — what the story delivers; **OUT** — what it leaves to other stories, with their IDs. |
| **Contracts** | The OpenAPI `operationId`s and schemas it implements, the tables it reads and writes, metrics, log events, settings (`area.setting`, `MUSTER_*`), CLI commands and pages. Contracts are referenced by name, never re-specified: the source of truth is `api/openapi.yaml`, `design/db/`, the PRD and its [defaults](../prd/l1/defaults.md). |
| **Steps** | The order of work. Each step ends with a check that shows it is done. |
| **Verification** | Live checks against the running binary: commands with their expected output for the backend (`curl`, `psql`, CLI), Playwright steps with the expected visible text for the frontend. Unit tests alone never count as verification. |
| **Open questions** | What the contract could not settle, including gaps or contradictions found in the PRD, the ADRs, the API specification or the schema. They are resolved before the story is implemented, or by the operator during it. |
| **Notes** | A suggested commit message — one line, conventional-commit style, no story or phase IDs — and anything else the implementer should know. |
| **Coverage** | A table of the FR and AC IDs in `covers`, each marked full or partial, with the story that completes a partial one. |

## 2. Repository layout

`files_touched` uses these paths. The layout follows ADR-0016 (flat domain packages, a command layer for Alert Groups)
and ADR-0009 (the SPA in `web/`); the foundation stories create it.

| Path | Content |
|---|---|
| `cmd/muster/` | The single binary: the server and its CLI subcommands. |
| `cmd/muster-archlint/` | The architecture lints, built only with the `lint` build tag. |
| `internal/<domain>/` | One package per domain: `users`, `auth`, `totp`, `oidc`, `tokens`, `audit`, `organization`, `integrations`, `ingest`, `heartbeat`, `matchers`, `routing`, `groups`, `timers`, `delivery`, `destinations`, `connections`, `mattermost`, `telegram`, `webhooks`, `messages`, `templates`, `links`, `mentions`, `buttons`, `accountlinks`, `internalalerts`, `outgoingheartbeat`, `systemstatus`. |
| `internal/<domain>/query.sql` | The domain's hand-written SQL queries, read by `sqlc`. |
| `internal/api/` | Hand-written API handlers, one file per OpenAPI tag, and the API middleware; each handler calls its domain package. |
| `internal/{logging,metrics,outbound,proxyconf,db,keyring,publicid,leader,partitions,server,live,runtime,config,clock,cli,buildinfo,doctor,devmode}/` | Shared infrastructure: the domain logger and log event registry, the metric registry, the outbound HTTP package and the proxy settings, database access, the Keyring, `public_id`, the Leader and partition maintenance, the listeners, the live-update hub, process wiring, bootstrap settings, the clock, the CLI, build information, `muster doctor` and the development mode. |
| `internal/archlint/` | The rules and fixtures of the architecture lints that `cmd/muster-archlint` runs. |
| `internal/db/migrations/` | Migrations in golang-migrate format, embedded in the binary; the schema `sqlc` reads. |
| `internal/fakes/<name>/` | Fake servers of external systems (Alertmanager, Mattermost, Telegram, OIDC, proxies) for tests and `muster dev`. |
| `internal/tools/` | Generators run by `make generate`; never part of the binary. |
| `api/` | `openapi.yaml`, its generator configuration and the package that embeds the spec for `/api/v1/openapi.yaml`. |
| `web/` | The SPA: `src/routes/` (file-based routes), `src/components/`, `src/locales/`, `e2e/` (Playwright). |
| `deploy/helm/muster/`, `deploy/compose/`, `deploy/dev/` | The Helm chart, the docker-compose example and the development database. |
| `docs/` | The documentation site; `docs/reference/` holds generated reference pages. |
| `test/e2e/`, `test/load/`, `test/docs/` | The end-to-end harness, the load test and the check of the documented quick start. |

**Generated files are never listed** and are checked in: `internal/*/dbgen/` (sqlc), `internal/api/gen/` (the
oapi-codegen strict server), `pkg/apiclient/` (the Go client), `web/src/api/gen/` (the orval client),
`web/src/routeTree.gen.ts` (the route tree), the reference pages under `docs/reference/`, and lock files (`go.sum`,
`web/pnpm-lock.yaml`). CI fails when any of them is not current.

## 3. Sizing and slicing

- **Vertical slices.** A story delivers one observable behaviour end to end: from the database through the domain
  package to the API — or from the merged API to the page — with a live check.
- **Backend first, then frontend.** A capability with a UI has at least two stories: a backend story (queries, domain
  logic, the API operations with their part of the spec, a live `curl` check), then a frontend story built on the merged
  API (pages, a live browser check) that depends on it. A capability without a UI (C-01, C-02, C-11) has backend or
  infrastructure stories only.
- **Size signal: `files_touched`.** More than about 30–40 hand-written files, or a backend and a frontend part that
  cannot be checked live together, means the story is split further at a contract boundary — a group of operations, a
  table group, a page group — into stories that follow each other. Lines of code are not a signal: a contract is short
  whatever the size of the code.
- **An acceptance statement belongs to the story whose merge makes it checkable.** A statement about a page belongs to
  the frontend story. A statement that needs a later capability is covered in part by the earlier story and completed by
  the later one; the coverage table names both.
- **Phase order.** Stories are implemented in the order of the rollout stages: foundation (C-01 – C-04), observation
  (C-05 – C-10), shadow (C-11 – C-16), actions (C-17, C-18) and operations (C-19 – C-21). The architecture lints of
  ADR-0016 exist from the first story that contains code.

## 4. Shared contracts

- **Database schema.** The designed schema of L1 is one initial migration (`0001_init`, explained in
  [`design/db/schema.md`](../db/schema.md)). The first database story moves it into `internal/db/migrations/`
  unchanged; later stories write queries against it. A story that has to change the schema adds a new expand/contract
  migration and updates `schema.md` in the same pull request. Rows that must exist from the first start — the
  Organization, its Default route, the built-in Integration and Link rule, the Keyring state, the bootstrap Admin — are
  created by idempotent start-up steps that each capability adds, never by a migration.
- **API specification.** `api/openapi.yaml` already describes all of L1. A story implements the operations it names;
  if the contract turns out to be wrong, the story changes the spec in the same pull request. Until a story implements
  an operation, the server answers it with `501`.
- **Defaults and provisional values.** Stories cite settings by their `area.setting` or `MUSTER_*` name from
  [defaults.md](../prd/l1/defaults.md). A story that changes a value, or confirms a provisional one (`P-NN`), updates
  defaults.md and the [open questions of L1](../prd/L1.md#53-provisional-values-and-behaviours) in the same pull request.
- **Registries.** Each story registers its own log events, metrics and Internal alerts in the closed registries of
  ADR-0014, and the generated reference pages change with them.
- **Fake servers.** Each story that talks to an external system extends that system's fake server, which tests and
  `muster dev` share.

## 5. Verification

`verify` runs the automated checks; `make ci` is the full pull-request tier (format, lints, architecture lints, licence
checks, generated code, unit and integration tests with the race detector, the build). Frontend stories add `make e2e`.

The **Verification** section is the live proof, and the pull request carries its evidence — commands with their output,
Playwright traces or screenshots — in its table of acceptance statements. Unless a story says otherwise, live checks run
against `muster dev` (S-004 onward) on a developer machine:

```sh
make dev           # starts PostgreSQL with Docker Compose on 127.0.0.1:55432 (`make dev-db` starts only that),
                   # builds the binary and runs `muster dev`: app on :8080, ingest on :8081, internal on :8082
```

`muster dev` uses the development defaults of S-004: a fixed development master key and the bootstrap Admin
`admin@example.org` with the password `muster-dev-password`. A check that needs data first triggers the path that
creates it (a webhook, a command, a scan); it never waits for a timer to happen to fire. A check that depends on time —
a window, a timeout, a Snooze, retention — advances the development clock of `muster dev` (`POST
localhost:8082/_dev/clock`, S-020) instead of waiting; an advance longer than `auth.session_idle_timeout` ends sessions,
so such a check calls the API with a Personal access token made before it, or signs in again. A CLI subcommand against
the development database runs as `muster dev <subcommand>` — `./bin/muster dev doctor` — with the same defaults. A
`MUSTER_*` variable that is set replaces its development default: a `MUSTER_SECRET_KEYS` given to `muster dev` is the
whole Keyring and lists the development key while the database still needs it.

Three backend stories have no API or messenger of their own to show their behaviour through: S-009 (the outbound HTTP
package) and S-034 and S-035 (the delivery engine before the first messenger). Their live checks are integration tests,
`go test -tags integration`, that run the real runtime against the development database and the development clock —
with a recording test adapter in place of a messenger — and their pull requests carry that output; S-036 uses the same
path for the parts of messages that need delivery. The messenger stories repeat the key checks against the fake servers
(S-061, S-042).

## 6. IDs

Story IDs are `S-NNN`, allocated in sequence as stories are written: `S-001`, `S-002`, … An ID is never reused. When a
story is split, the original keeps its ID and the new parts take the next free IDs; when a story is dropped, its ID stays
unused and the story map says what replaced it. The slug in the file name may change; the ID does not.

## 7. Lifecycle

1. **Planning.** Stories are written in batches — a capability or a stage at a time — in a pull request that changes
   only `design/stories/` (and the Russian copies). A fresh reviewer checks the diff; further passes are new commits in
   the same pull request. Merging the pull request accepts the contracts.
2. **Ready.** When the design is complete and approved, a script creates one issue per capability and one sub-issue per
   story, titled `S-NNN: <title>`; `depends_on` becomes "blocked by", a frontend story is blocked by its backend story,
   and the issue number is written back into `issue`.
3. **Implementation.** A story is implemented on its own branch. If the contract turns out to be wrong, the story file
   is corrected in the same pull request as the code, before the code that depends on the correction.
4. **Pull request.** The template asks for `Closes #<issue>`, `Story: S-NNN`, a table with evidence for every
   `acceptance` statement and the output of `verify`.
5. **Done.** Merging closes the issue, and the project board moves it to Done. Splitting or dropping a story happens in
   a pull request; the script closes the issue of a dropped story as not planned.

### 7.1 The sync script

`scripts/stories-sync` is the script of step 2, and it keeps the issues in step with the files afterwards. It needs
Python 3 (standard library only) and an authenticated `gh`. It reads the frontmatter of every `S-NNN` file and the
title of every capability in `design/prd/l1/`, then:

- creates the labels `story`, `capability`, `be`, `fe`, `infra`, `docs` and `operator-attention` and the milestone
  `L1` when they are missing;
- keeps one issue per capability, `C-NN: <title>`, and one per story, `S-NNN: <title>`, labelled with its kind and,
  when set, `operator-attention`, with the acceptance statements as a task list and `verify`; each issue links to its
  file at the HEAD commit, each story issue is a sub-issue of its capability issue, and each `depends_on` entry is a
  "blocked by" link;
- finds existing issues by their title prefix, open or closed, and changes only what differs, so it can run again
  after any change to the files; it closes the issue of a story whose file is gone as not planned, with a comment;
- writes each issue number into the `issue:` line of the story file; that change is then committed.

| Flag | Effect |
|---|---|
| `--dry-run` | The default: reads GitHub and prints the plan with its counts; changes nothing. |
| `--apply` | Makes the changes. Refuses to run when a story or capability file differs from HEAD (apart from `issue:` lines) or HEAD is not on GitHub, because issue bodies link to HEAD. |
| `--only S-001,S-002` | Limits the run to these stories and their capability issues; no issue is closed. |
| `--repo OWNER/NAME` | Another repository; the default is the `origin` remote. |
| `--delay SECONDS` | The pause between write requests, 1 by default; a rate-limit answer is retried after the wait GitHub asks for. |
| `--show-body` | Prints the body of every issue the run creates or updates. |

Issue titles and bodies are generated, and the next run overwrites edits made on GitHub; a ticked acceptance box
stays ticked while its statement is unchanged. Edit the files, not the issues.

## 8. Story map

All of L1, in phase order. Contracts were written in batches, and every story of the map now has its contract file; a
later split takes the next free IDs (section 6). Kinds: `be` backend, `fe` frontend, `infra` build and deployment,
`docs` documentation.

| Phase | Capabilities | Stories | Count |
|---|---|---|---|
| Foundation | C-01 – C-04 | S-001 – S-017, S-062 | 18 |
| Observation | C-05 – C-10 | S-018 – S-033, S-063 | 17 |
| Shadow | C-11 – C-16 | S-034 – S-048, S-061, S-064, S-065 | 18 |
| Actions | C-17, C-18 | S-049 – S-052 | 4 |
| Operations | C-19 – C-21 | S-053 – S-060 | 8 |
| **L1** | 21 | | **65** |

### Foundation

| ID | Capability | Title | Kind | Depends on |
|---|---|---|---|---|
| [S-001](S-001-go-module-ci-gates-and-architecture-lints.md) | C-01 | Go module, Makefile, CI gates, architecture lints and contributor files | infra | — |
| [S-002](S-002-spa-skeleton-code-generation-and-supply-chain-checks.md) | C-01 | SPA skeleton, code generation, dependency licences and security scanning | infra | S-001 |
| [S-003](S-003-release-pipeline-image-chart-and-compose.md) | C-01 | Release pipeline, container image, Helm chart and compose example | infra | S-002 |
| [S-004](S-004-dev-mode-fake-servers-and-test-harness.md) | C-01 | `muster dev` with fake servers, end-to-end and load-test harness | infra | S-002 |
| [S-005](S-005-domain-logger-and-registries.md) | C-02 | Domain logger, log event and metric registries (BE) | be | S-002 |
| [S-006](S-006-bootstrap-database-migrations-and-listeners.md) | C-02 | Bootstrap settings, database connections, migrations, listeners and health (BE) | be | S-003, S-004, S-005 |
| [S-007](S-007-keyring-and-organization-defaults.md) | C-02 | Keyring, key canary, replica key records and Organization defaults (BE) | be | S-006 |
| [S-008](S-008-leader-partitions-downtime-and-doctor.md) | C-02 | Leader, partitions and retention, downtime recovery, clock skew and `muster doctor` (BE) | be | S-007 |
| [S-009](S-009-outbound-http-package.md) | C-02 | Outbound HTTP package with the outbound address policy and proxies (BE) | be | S-007 |
| [S-010](S-010-api-server-local-sign-in-and-sessions.md) | C-03 | API server, local sign-in, sessions and the bootstrap Admin (BE) | be | S-008 |
| [S-011](S-011-user-administration-and-audit-log.md) | C-03 | User administration, password setup links and the Audit log (BE) | be | S-010 |
| [S-012](S-012-totp-notices-and-live-updates.md) | C-03 | TOTP, the TOTP policy, system notices and live updates (BE) | be | S-011 |
| [S-013](S-013-oidc-sign-in-linking-and-proxy-settings.md) | C-03 | OIDC settings, proxy settings and OIDC sign-in (BE) | be | S-009, S-012 |
| [S-062](S-062-oidc-linking-conversion-and-background-rechecks.md) | C-03 | OIDC account linking, conversion to local, the offline token and background re-checks (BE) | be | S-013 |
| [S-014](S-014-app-shell-sign-in-and-profile.md) | C-03 | Application shell, sign-in, password setup, TOTP and profile (FE) | fe | S-013, S-062 |
| [S-015](S-015-admin-pages-users-oidc-security-audit.md) | C-03 | Users, OIDC settings, Organization security and Audit log pages (FE) | fe | S-014 |
| [S-016](S-016-api-tokens-and-service-accounts.md) | C-04 | Personal access tokens, Service accounts, token authentication and rate limits (BE) | be | S-013, S-062 |
| [S-017](S-017-api-tokens-pages.md) | C-04 | Personal access tokens and Service accounts pages (FE) | fe | S-015, S-016 |

### Observation

| ID | Capability | Title | Kind | Depends on |
|---|---|---|---|---|
| [S-018](S-018-integrations-tokens-ingestion-and-stored-snapshots.md) | C-05 | Integrations, Integration tokens, the ingestion endpoint and Stored Snapshots (BE) | be | S-016 |
| [S-019](S-019-integrations-pages-token-dialog-and-snapshot-viewer.md) | C-05 | Integrations list, form, token dialog, Stored Snapshot viewer and delete dialog (FE) | fe | S-017, S-018 |
| [S-020](S-020-snapshot-processing-and-alerts-view.md) | C-06 | Snapshot processing: Alerts, resolves, Gone, truncation, Continuation, staleness bookkeeping and the Alerts view (BE) | be | S-018 |
| [S-021](S-021-internal-alerts-builtin-integration-replay-and-learned-routes.md) | C-06 | Internal alert mechanism, built-in Integration, replay, Integration deletion and learned Alertmanager routes (BE) | be | S-020 |
| [S-022](S-022-integration-page-learned-routes-warnings-and-alerts-view.md) | C-06 | Integration page: learned Alertmanager routes, warnings and Alerts view (FE) | fe | S-019, S-021 |
| [S-023](S-023-heartbeat-endpoint-states-leader-checks-and-staleness.md) | C-07 | Heartbeat endpoint, states, Leader checks, Internal alert and staleness pause (BE) | be | S-021 |
| [S-024](S-024-heartbeat-settings-badges-and-banners.md) | C-07 | Heartbeat settings, badges and banners (FE) | fe | S-022, S-023 |
| [S-025](S-025-routes-matchers-severity-levels-and-profiles.md) | C-08 | Routes, Matchers, evaluation order, Severity levels and Route profiles (BE) | be | S-023 |
| [S-026](S-026-group-key-preview-and-route-suggestions.md) | C-08 | Group key preview and Route suggestions (BE) | be | S-025 |
| [S-027](S-027-routes-pages-matcher-builder-and-group-key-preview.md) | C-08 | Routes list, Route editor, Matcher builder, Group key preview and suggestions (FE) | fe | S-022, S-026 |
| [S-028](S-028-alert-group-lifecycle-timeline-and-lifecycle-events.md) | C-09 | Alert Group lifecycle: grouping, state machine, system transitions, Timeline and lifecycle events (BE) | be | S-026 |
| [S-029](S-029-alert-group-list-search-statistics-and-live-hints.md) | C-09 | Alert Group list, search, counts, related Alert Groups, statistics, retention and live hints (BE) | be | S-028 |
| [S-030](S-030-alert-group-list-and-page-at-phone-width.md) | C-09 | Alert Group list and Alert Group page at phone width (FE) | fe | S-017, S-029 |
| [S-031](S-031-statistics-page-route-lifecycle-settings-and-dialogs.md) | C-09 | Statistics page, Route lifecycle settings and the delete dialog additions (FE) | fe | S-019, S-027, S-030 |
| [S-032](S-032-commands-notes-snooze-timers-and-bulk-commands.md) | C-10 | Commands, Takeover, refusals, allowed commands and bulk commands (BE) | be | S-016, S-029 |
| [S-063](S-063-notes-snooze-ends-user-directory-owner-filters-and-owner-release.md) | C-10 | Notes, Snooze ends, the user directory, Owner filters and the release of disabled and deleted Owners (BE) | be | S-032 |
| [S-033](S-033-command-buttons-dialogs-notes-and-bulk-selection.md) | C-10 | Command buttons, dialogs, Note box, bulk selection, Owner filters and Snooze durations (FE) | fe | S-031, S-063 |

### Shadow

| ID | Capability | Title | Kind | Depends on |
|---|---|---|---|---|
| [S-034](S-034-delivery-reconciliation-limiters-interactive-path-and-threads.md) | C-11 | Delivery reconciliation, limiters, interactive path and Threads (BE) | be | S-063 |
| [S-035](S-035-error-classes-broken-destinations-recovery-storms-deleted-messages-and-duplicates.md) | C-11 | Error classes, Broken Destinations and recovery, Storms, deleted messages and duplicates (BE) | be | S-034 |
| [S-036](S-036-default-message-built-in-texts-template-sandbox-previews-and-fallback.md) | C-12 | Default message, built-in texts, template sandbox, previews and the Fallback template (BE) | be | S-035 |
| [S-037](S-037-lookup-tables-link-rules-and-mention-settings.md) | C-12 | Lookup tables, Link rules and Mention settings (BE) | be | S-036 |
| [S-038](S-038-route-message-editors-lookup-tables-and-link-rules-pages-links-block.md) | C-12 | Route message editors, Lookup tables and Link rules pages, links block (FE) | fe | S-027, S-030, S-033, S-037 |
| [S-039](S-039-mattermost-connections-destinations-check-and-fake-server.md) | C-13 | Mattermost Connections and Destinations, the Destination check and the fake Mattermost server (BE) | be | S-037 |
| [S-061](S-061-mattermost-adapter-posts-threads-buttons-callbacks-and-delivery-problem-filter.md) | C-13 | Mattermost adapter: posts, Threads, buttons, callbacks, the Delivery problem filter and the Internal alerts suggestion (BE) | be | S-039 |
| [S-065](S-065-parallel-snapshot-processing-per-alertmanager-group-and-a-shorter-grouping-lock.md) | C-06 | Parallel Snapshot processing per Alertmanager group and a shorter grouping lock (BE) | be | S-061 |
| [S-040](S-040-mattermost-connection-pages-shared-destination-pages-delivery-state-and-delivery-problem-filter.md) | C-13 | Mattermost Connection pages with the check, the callback address and its hint (FE) | fe | S-038, S-061 |
| [S-064](S-064-destination-pages-route-destinations-delivery-state-and-delivery-problem-filter.md) | C-13 | Destination pages, the Route's Destinations and delivery sections, delivery state and the Delivery problem filter (FE) | fe | S-040 |
| [S-041](S-041-telegram-connection-bot-api-base-url-dry-probe-long-polling-and-webhook-modes.md) | C-14 | Telegram Connection: Bot API base URL, dry probe, long polling and webhook modes (BE) | be | S-061 |
| [S-042](S-042-telegram-adapter-channel-posts-comment-threads-buttons-callbacks-and-check.md) | C-14 | Telegram adapter: channel posts, comment Threads, buttons, callbacks and Destination check (BE) | be | S-041 |
| [S-043](S-043-telegram-connection-pages-step-by-step-check-and-destination-form.md) | C-14 | Telegram Connection pages with the step-by-step check and the Telegram Destination form (FE) | fe | S-064, S-042 |
| [S-044](S-044-outgoing-webhook-events-mode-signing-secret-secrets-and-error-mapping.md) | C-15 | Outgoing webhook events mode, Signing secret, Secrets and error mapping (BE) | be | S-039 |
| [S-045](S-045-outgoing-webhook-template-mode-extraction-threads-storm-summaries-and-mentions.md) | C-15 | Outgoing webhook template mode: extraction, threads, Storm summaries and Mentions as data (BE) | be | S-044 |
| [S-046](S-046-outgoing-webhook-destination-form-request-builders-secrets-and-signing-secret.md) | C-15 | Outgoing webhook Destination form, request builders, Secrets and Signing secret actions (FE) | fe | S-064, S-043, S-045 |
| [S-047](S-047-destination-test-and-preview-for-every-destination-type.md) | C-16 | Destination test and preview for every Destination type (BE) | be | S-042, S-045 |
| [S-048](S-048-test-and-preview-panels-on-the-destination-page.md) | C-16 | Test and Preview panels on the Destination page (FE) | fe | S-043, S-046, S-047 |

### Actions

| ID | Capability | Title | Kind | Depends on |
|---|---|---|---|---|
| [S-049](S-049-ack-timeouts-unclaimed-reminders-auto-unacknowledge-and-still-on-it.md) | C-17 | Ack timeouts, Unclaimed, Reminders, auto-unacknowledge and "Still on it" (BE) | be | S-042, S-047 |
| [S-050](S-050-route-timer-fields-unclaimed-filter-and-badge-next-notice-and-still-on-it.md) | C-17 | Route policy fields, Unclaimed filter and badge, next notice and "Still on it" (FE) | fe | S-033, S-038, S-064, S-049 |
| [S-051](S-051-account-links-telegram-deep-links-mattermost-codes-press-attribution-and-audit-log.md) | C-18 | Account links: Telegram deep links, Mattermost codes, press attribution and Audit log (BE) | be | S-049 |
| [S-052](S-052-messenger-accounts-in-the-profile-and-account-links-on-the-user-page.md) | C-18 | Messenger accounts in the profile and Account links on the user page (FE) | fe | S-015, S-051 |

### Operations

| ID | Capability | Title | Kind | Depends on |
|---|---|---|---|---|
| [S-053](S-053-oidc-secret-expiry-alert-outgoing-heartbeat-system-status-and-metric-catalogue-check.md) | C-19 | OIDC secret expiry alert, outgoing heartbeat, System status, runbook URLs and the metric catalogue check (BE) | be | S-045, S-049 |
| [S-054](S-054-system-status-page-and-outgoing-heartbeat-settings.md) | C-19 | System status page and outgoing heartbeat settings (FE) | fe | S-015, S-053 |
| [S-055](S-055-organization-settings-outbound-policy-editing-keyring-status-and-key-rotation.md) | C-20 | Organization settings, outbound policy editing, Keyring status and key rotation (BE) | be | S-047, S-051, S-053 |
| [S-056](S-056-organization-settings-pages.md) | C-20 | Organization settings pages (FE) | fe | S-015, S-055 |
| [S-057](S-057-documentation-site-build-and-generated-references.md) | C-21 | Documentation site, its build and the generated references | docs | S-053 |
| [S-058](S-058-runbooks-telegram-in-restricted-networks-and-the-readme-demo.md) | C-21 | Runbooks, Telegram in restricted networks and the README demo | docs | S-056, S-057 |
| [S-059](S-059-chart-alert-rules-with-rule-unit-tests-and-the-grafana-dashboard.md) | C-19 | Chart alert rules with rule unit tests and the Grafana dashboard | infra | S-053, S-058 |
| [S-060](S-060-quick-start-install-concepts-operations-and-remaining-documentation.md) | C-21 | Remaining documentation sections: Quick start, Install, Alertmanager, Sign-in, Concepts and Operations | docs | S-055, S-059 |

S-059 and S-060 were split from S-053 and S-058 when their contracts were written; S-053 and S-058 keep their IDs. The
chart rules follow the runbook pages, so that the check of rendered rules against the pages can run as soon as the
rules exist. S-061 was split from S-039 after the review of the contracts: S-039 keeps Connections, Destinations, the
Destination check and the fake server, S-061 takes the adapter and everything that needs posts; it follows S-039 in the
Shadow phase, before S-040. S-062 was split from S-013 before its implementation, because S-013 touched about 50
files: S-013 keeps the OIDC settings, the proxy settings object and sign-in, S-062 takes linking, conversion to local,
the offline token and the background re-checks with the shared claim helper; it follows S-013 in the Foundation phase,
before S-014. S-063 and S-064 were split from S-032 and S-040 by the plan review of the next phase, before their
implementation, because with the wiring files the review added each touched about 40 files. S-032 keeps the Commands,
their refusals, `allowed_commands` and bulk commands, and S-063 takes Notes, the Snooze end timer, the user directory,
the Owner filters and the release of disabled and deleted Owners; it follows S-032 in the Observation phase, before
S-033, and the delivery engine (S-034) follows it. S-040 keeps the Connection pages, and S-064 takes the Destination
pages, the Route editor's Destinations and delivery sections, the delivery state and the "Delivery problem" filter; it
follows S-040 in the Shadow phase, and the Telegram and outgoing webhook pages and the timer fields (S-043, S-046,
S-050) follow it. Both keep their file names. S-065 was added after S-061's load test showed that one Integration's
burst misses NFR-2 (D283): it belongs to C-06 but follows S-061 in the Shadow phase, before S-040, because only the full
load profile of S-061 measures it.

The backend stories of a stage follow the dependencies between capabilities in the
[capability map](../prd/L1.md#13-capability-map): snapshot processing before routing, routing before the Alert Group
lifecycle, the lifecycle before commands, commands before the delivery engine, the delivery engine and messages before
the messengers, the messengers before timers and Account links. Every frontend story depends on the application shell
(S-014, through S-015 or S-017) and on the backend story of its capability.
