# Muster stories

- Status: Draft
- Date: 2026-10-04

A story is the unit of implementation of Muster: one pull request that delivers one observable piece of a capability
and proves it on the running binary. Each story is a **contract file** `S-NNN-<slug>.md` in this directory. The file
says what the story delivers, which contracts it implements — OpenAPI operations, tables, metrics, log events, settings,
CLI commands — which files it touches, how its acceptance is checked and what is still open. It does not contain the
code: the implementation writes the code in the repository against compiler and test feedback, and the contract is what
the review checks the code against.

Stories implement the [L1 capabilities](../prd/L1.md#13-capability-map) `C-01`…`C-24` and the [core of
L2](../prd/L2.md#14-capability-map), `C-25`…`C-32`, with the statistics by people and by Team that L2 adds to
`C-09`, and the capabilities of L2 specified after its core, from `C-33`, among them those of the 0.2.x patch
releases: `C-34`, `C-36`, `C-37`, `C-38`, `C-39`, `C-40`, `C-41` and `C-42`. Every functional requirement (`C-NN.FR-n`)
and acceptance statement (`C-NN.AC-n`) of a capability is covered by its stories; the [coverage table](coverage.md)
shows which story covers which ID. Terms follow [`CONTEXT.md`](../../CONTEXT.md); the reasons behind technical choices
are in the [ADRs](../adr/README.md).

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
| `kind` | `be` (backend: Go, SQL, OpenAPI), `fe` (the web UI), `infra` (build, CI, release, deployment) or `docs` (the documentation site). A spike — a check on the test environment that a later story relies on — is `docs` with `operator_attention: true`: the maintainer runs it, it records the verified facts in `design/facts.md` and corrects the contracts that relied on them, and its title starts with "Spike:". |
| `layer` | `L1` or `L2`: the layer of the PRD the story implements. |
| `repository` | Optional, after `layer`: the GitHub repository, `OWNER/NAME`, of a story whose pull request goes to another repository than this one — the bridge libraries of ADR-0017 and the Terraform provider of C-33. Its `files_touched` are paths in that repository, its `verify` runs that repository's Makefile, and its pull request there says `Part of muster-io/muster#<issue>`: a pull request in another repository does not close an issue here, so the issue is closed by hand once that pull request merges. The contract and the issue stay in this repository. Without the key the story is for this repository. |
| `depends_on` | Stories that must be merged first. The graph is acyclic. A dependency usually has a smaller ID; a part split off later takes the next free ID (section 6) but keeps its place in the phase order, so stories with smaller IDs may depend on it — S-061, split from S-039, comes before S-040. The story map lists stories in an order that respects every dependency. A frontend story depends on the backend story of its capability. |
| `covers` | The FR and AC IDs the story covers, fully or in part; the body's coverage table says which. Required by the [L1 conventions](../prd/L1.md#2-conventions) and those of [L2](../prd/L2.md#2-conventions). |
| `files_touched` | Every hand-written file the story creates or changes, tests included. Generated files are never listed ([section 2](#2-repository-layout)). A path ending in `/**` stands for a directory of small fixtures and counts as one entry. |
| `acceptance` | Testable statements. Each starts with the FR or AC IDs it proves, in brackets, and is checked by a test or by the live checks of the body. A story whose `covers` is empty, because no requirement speaks about what it changes, says why in its Notes, and its statements name no IDs. |
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
| `internal/<domain>/` | One package per domain: `users`, `auth`, `totp`, `oidc`, `tokens`, `audit`, `organization`, `integrations`, `ingest`, `heartbeat`, `matchers`, `routing`, `groups`, `timers`, `delivery`, `destinations`, `connections`, `mattermost`, `telegram`, `slack`, `bridge`, `webhooks`, `messages`, `templates`, `links`, `mentions`, `buttons`, `accountlinks`, `internalalerts`, `outgoingheartbeat`, `systemstatus`; from L2 also `teams`, `schedules`, `gateways`, `notificationrules` and `escalation`. |
| `internal/<domain>/query.sql` | The domain's hand-written SQL queries, read by `sqlc`. |
| `internal/api/` | Hand-written API handlers, one file per OpenAPI tag, and the API middleware; each handler calls its domain package. |
| `internal/{logging,metrics,outbound,proxyconf,db,keyring,publicid,leader,partitions,server,live,runtime,config,clock,cli,buildinfo,doctor,devmode}/` | Shared infrastructure: the domain logger and log event registry, the metric registry, the outbound HTTP package and the proxy settings, database access, the Keyring, `public_id`, the Leader and partition maintenance, the listeners, the live-update hub, process wiring, bootstrap settings, the clock, the CLI, build information, `muster doctor` and the development mode. |
| `internal/archlint/` | The rules and fixtures of the architecture lints that `cmd/muster-archlint` runs. |
| `internal/db/migrations/` | Migrations in golang-migrate format, embedded in the binary; the schema `sqlc` reads. |
| `internal/fakes/<name>/` | Fake servers of external systems (Alertmanager, Mattermost, Telegram, Slack, a bridge, OIDC, proxies) for tests and `muster dev`. |
| `internal/tools/` | Generators run by `make generate`; never part of the binary. |
| `api/` | `openapi.yaml`, its generator configuration and the package that embeds the spec for `/api/v1/openapi.yaml`; `bridge.yaml`, the Bridge protocol (MIT, ADR-0017). |
| `web/` | The SPA: `src/routes/` (file-based routes), `src/components/`, `src/locales/`, `e2e/` (Playwright). |
| `deploy/helm/muster/`, `deploy/compose/`, `deploy/dev/` | The Helm chart, the docker-compose example and the development database. |
| `docs/` | The documentation site; `docs/reference/` holds generated reference pages. |
| `test/e2e/`, `test/load/`, `test/docs/` | The end-to-end harness, the load test and the check of the documented quick start. |

The bridge libraries for Go and Python and the example bridge are not in this repository: they live in
`muster-io/muster-bridge-go` and `muster-io/muster-bridge-python`, MIT without a CLA (ADR-0017), and the stories that
build them name their repository in `repository` (S-083, S-084, S-085) and describe its layout in their contracts;
the generated files there — each library's models and lock file — are never listed either. Nor is the Terraform
provider, which lives in `muster-io/terraform-provider-muster` under MIT without a CLA (ADR-0001, C-33); its stories
(S-182 to S-188) name that repository, and its generated client, its `docs/` and its lock file are never listed.

**Generated files are never listed** and are checked in: `internal/*/dbgen/` (sqlc), `internal/api/gen/` (the
oapi-codegen strict server), `internal/bridge/gen/` (the models of the Bridge protocol, from S-073), `pkg/apiclient/`
(the Go client), `web/src/api/gen/` (the orval client), `web/src/routeTree.gen.ts` (the route tree), the reference
pages under `docs/reference/`, and lock files (`go.sum`, `web/pnpm-lock.yaml`). CI fails when any of them is not
current. The images that `make demo` and `make docs-screenshots` make — `docs/assets/demo.gif` and the screenshots
under `docs/assets/screenshots/` — are generated and checked in too, and never listed; CI does not make them again, and
`make docs-check` fails on a screenshot that a page references but does not exist, or that no page references.

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
  (C-05 – C-10), shadow (C-11 – C-16), actions (C-17, C-18) and operations (C-19 – C-21), then the Bridge (C-22), the
  check of a Connection before saving it (S-087), the Request log (C-23) and the last documentation stories, S-060 and
  S-091, then the pre-release stories S-092 to S-096, then the Slack stories S-097 to S-105, and last the guides with
  screenshots and the documentation links of the web UI, S-106 to S-110. After the phases of L1, the L2 core follows
  in the three stages of its adoption ([L2 §1.3](../prd/L2.md#13-adoption)): "L2 Teams" (C-25, C-26, the buttons of
  C-28 and C-29), "L2 On call" (C-27, Team mentions and the Mention entries) and "L2 Paging" (C-30 – C-32); beside
  them, "L2 Statistics" (S-174 to S-176, for C-09) needs only the Teams of Alert Groups and runs in parallel. The
  capabilities specified after the core follow, the Terraform provider and then those of "L2 0.2.x", which start after
  the tag of 0.2.0 and ship in its patch releases (C-34 from S-190, C-36 from S-197, C-37 from S-202, C-38 from S-209, C-39 from S-220, C-40 from S-222, C-41 from S-228, C-42 from S-231). The
  architecture lints of ADR-0016 exist from the first story that contains code.

## 4. Shared contracts

- **Database schema.** The designed schema of L1 is one initial migration (`0001_init`, explained in
  [`design/db/schema.md`](../db/schema.md)). The first database story moves it into `internal/db/migrations/`
  unchanged; later stories write queries against it. A story that has to change the schema adds a new expand/contract
  migration and updates `schema.md` in the same pull request. Migration numbers follow the story map; a story merged
  out of that order renumbers its migration to the next free number in its own pull request, with its tests and
  `schema.md`, and re-versions every CHECK it replaces (`_v2`, `_v3`, …) so that the new version lists every value
  present when it merges — a CHECK that two stories replace, such as that of `open_group_updates.kind` (S-113, S-163,
  S-121, S-125), takes its versions in the order the stories merge. Rows that must exist from the first start — the
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
(S-061, S-042, S-066, S-067).

A story for another repository (`repository`) runs its `verify` and its live checks in that repository; where they
need Muster, they use the development image `ghcr.io/muster-io/muster:edge` or, for a check by hand, `muster dev`
from a checkout of this one.

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
title of every capability in `design/prd/l1/` and `design/prd/l2/`, then:

- creates the labels `story`, `capability`, `be`, `fe`, `infra`, `docs` and `operator-attention`, and the milestone of
  each story's layer (`L1`, `L2`), when they are missing; each issue gets the milestone of its layer;
- keeps one issue per capability, `C-NN: <title>`, and one per story, `S-NNN: <title>`, labelled with its kind and, when
  set, `operator-attention`, with the acceptance statements as a task list and `verify`, and the repository of a story
  that names one; each issue links to its file at the HEAD commit, each story issue is a sub-issue of its capability
  issue, and each `depends_on` entry is a "blocked by" link;
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

All of L1, the core of L2 and the capabilities of L2 specified after it, in phase order. Contracts were written in
batches, and every story of the map now has its
contract file; a later split takes the next free IDs (section 6). Kinds: `be` backend, `fe` frontend, `infra` build and
deployment, `docs` documentation. A story for another repository names it after its title.

| Phase | Capabilities | Stories | Count |
|---|---|---|---|
| Foundation | C-01 – C-04 | S-001 – S-017, S-062 | 18 |
| Observation | C-05 – C-10 | S-018 – S-033, S-063 | 17 |
| Shadow | C-11 – C-16 | S-034 – S-048, S-061, S-064 – S-067 | 20 |
| Actions | C-17, C-18 | S-049 – S-052, S-068 – S-070 | 7 |
| Operations | C-19 – C-21 | S-053 – S-059, S-071, S-072, S-082 | 10 |
| Bridge | C-22, C-23, S-087 of C-13, and S-060 and S-091 of C-21 | S-073 – S-081, S-083 – S-090, S-060, S-091 | 19 |
| Pre-release | S-092 – S-094 of C-10, S-095 of C-09, S-096 of C-06 | S-092 – S-096 | 5 |
| Slack | C-24 | S-097 – S-105 | 9 |
| Guides | S-106 – S-110 of C-21 | S-106 – S-110 | 5 |
| **L1** | 24 | | **110** |

| Phase | Capabilities | Stories | Count |
|---|---|---|---|
| L2 Teams | C-25, C-26, the buttons of C-28, C-29 | S-111 – S-130, S-162, S-163, S-166, S-170 | 24 |
| L2 On call | C-27, the Team mentions of C-25, the Mention entries of C-28 | S-131 – S-144, S-164, S-165, S-171 | 17 |
| L2 Paging | C-30 – C-32, the chains' part of C-27.FR-8 | S-145 – S-161, S-167 – S-169, S-172, S-173, S-177, S-178 | 24 |
| L2 Statistics | the statistics by people and by Team that L2 adds to C-09 | S-174 – S-176 | 3 |
| **L2 core** | 8 | | **68** |
| L2 Terraform provider | C-33 | S-179 – S-189 | 11 |
| L2 0.2.x | C-34, C-36, C-37, C-38, C-39, C-40, C-41, C-42 | S-190 – S-239 | 50 |
| **L2** | 17 | | **129** |

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
| [S-042](S-042-telegram-adapter-channel-posts-comment-threads-buttons-callbacks-and-check.md) | C-14 | Telegram Destinations, the Destination check, Root message posts and edits (BE) | be | S-041 |
| [S-066](S-066-telegram-comment-threads-copy-buffer-copy-wait-and-unattached-replies.md) | C-14 | Telegram comment Threads: the post copy buffer, waiting for the copy and unattached replies (BE) | be | S-042 |
| [S-067](S-067-telegram-button-presses-callback-answers-signed-data-account-links-and-update-router-lock.md) | C-14 | Telegram button presses: callback answers, signed data, Account links and the update router lock (BE) | be | S-042 |
| [S-043](S-043-telegram-connection-pages-step-by-step-check-and-destination-form.md) | C-14 | Telegram Connection pages with the step-by-step check and the Telegram Destination form (FE) | fe | S-064, S-042, S-066 |
| [S-044](S-044-outgoing-webhook-events-mode-signing-secret-secrets-and-error-mapping.md) | C-15 | Outgoing webhook events mode, Signing secret, Secrets and error mapping (BE) | be | S-039 |
| [S-045](S-045-outgoing-webhook-template-mode-extraction-threads-storm-summaries-and-mentions.md) | C-15 | Outgoing webhook template mode: extraction, threads, Storm summaries and Mentions as data (BE) | be | S-044 |
| [S-046](S-046-outgoing-webhook-destination-form-request-builders-secrets-and-signing-secret.md) | C-15 | Outgoing webhook Destination form, request builders, Secrets and Signing secret actions (FE) | fe | S-064, S-043, S-045 |
| [S-047](S-047-destination-test-and-preview-for-every-destination-type.md) | C-16 | Destination test and preview for every Destination type (BE) | be | S-067, S-045 |
| [S-048](S-048-test-and-preview-panels-on-the-destination-page.md) | C-16 | Test and Preview panels on the Destination page (FE) | fe | S-043, S-046, S-047 |

### Actions

| ID | Capability | Title | Kind | Depends on |
|---|---|---|---|---|
| [S-049](S-049-ack-timeouts-unclaimed-reminders-auto-unacknowledge-and-still-on-it.md) | C-17 | Ack timeouts, Unclaimed, Reminders, auto-unacknowledge and "Still on it" (BE) | be | S-066, S-067, S-047 |
| [S-068](S-068-reminder-buttons-in-mattermost-and-telegram.md) | C-17 | Reminder buttons in Mattermost and Telegram (BE) | be | S-049 |
| [S-050](S-050-route-timer-fields-unclaimed-filter-and-badge-next-notice-and-still-on-it.md) | C-17 | Route policy fields, Unclaimed filter and badge, next notice and "Still on it" (FE) | fe | S-033, S-038, S-064, S-049 |
| [S-051](S-051-account-links-telegram-deep-links-mattermost-codes-press-attribution-and-audit-log.md) | C-18 | Account links: Telegram deep links, Mattermost codes, press attribution and Audit log (BE) | be | S-068 |
| [S-052](S-052-messenger-accounts-in-the-profile-and-account-links-on-the-user-page.md) | C-18 | Messenger accounts in the profile and Account links on the user page (FE) | fe | S-015, S-051 |
| [S-069](S-069-several-connections-of-one-messenger.md) | C-18 | Several Connections of one messenger: two Mattermost servers and two Telegram bots (BE) | be | S-061, S-066, S-067, S-068, S-043, S-044, S-064, S-051, S-052 |
| [S-070](S-070-connection-and-destination-labels.md) | C-18 | Connection and Destination labels, the Telegram link row and the duplicate-chat warning (FE) | fe | S-069 |

### Operations

| ID | Capability | Title | Kind | Depends on |
|---|---|---|---|---|
| [S-053](S-053-oidc-secret-expiry-alert-outgoing-heartbeat-system-status-and-metric-catalogue-check.md) | C-19 | OIDC secret expiry alert, outgoing heartbeat, System status, runbook URLs and the metric catalogue check (BE) | be | S-045, S-049 |
| [S-054](S-054-system-status-page-and-outgoing-heartbeat-settings.md) | C-19 | System status page and outgoing heartbeat settings (FE) | fe | S-015, S-053 |
| [S-055](S-055-organization-settings-outbound-policy-editing-keyring-status-and-key-rotation.md) | C-20 | Organization settings, values seen and outbound policy editing (BE) | be | S-047, S-051, S-053 |
| [S-071](S-071-keyring-status-key-rotation-and-re-render-after-rotation.md) | C-20 | Keyring status, key rotation and re-render after rotation (BE) | be | S-055, S-069 |
| [S-056](S-056-organization-settings-pages.md) | C-20 | Organization settings pages (FE) | fe | S-015, S-055, S-071 |
| [S-072](S-072-compact-top-navigation.md) | C-03 | Compact top navigation: Access entry, Profile in the user menu, Audit log under Organization (FE) | fe | S-056 |
| [S-057](S-057-documentation-site-build-and-generated-references.md) | C-21 | Documentation site, its build and the generated references | docs | S-053, S-051 |
| [S-058](S-058-runbooks-telegram-in-restricted-networks-and-the-readme-demo.md) | C-21 | Runbooks, Telegram in restricted networks and the README demo | docs | S-056, S-057, S-054 |
| [S-059](S-059-chart-alert-rules-with-rule-unit-tests-and-the-grafana-dashboard.md) | C-19 | Chart alert rules with rule unit tests and the Grafana dashboard | infra | S-053, S-058 |
| [S-082](S-082-button-style-hints-in-mattermost-and-telegram.md) | C-12 | Button style hints in Mattermost and Telegram (BE) | be | S-061, S-042, S-068 |

### Bridge

| ID | Capability | Title | Kind | Depends on |
|---|---|---|---|---|
| [S-073](S-073-bridge-connections-destinations-protocol-client-and-fake-bridge.md) | C-22 | Bridge Connections, the protocol client with its error classes, `info` and the fake bridge (BE) | be | S-069, S-071, S-059 |
| [S-080](S-080-bridge-destinations-channel-check-doctor-and-mention-settings.md) | C-22 | Bridge Destinations, the channel check, `muster doctor` and the Mention settings of Bridges (BE) | be | S-073 |
| [S-074](S-074-bridge-adapter-envelopes-idempotent-publications-threads-and-edit-windows.md) | C-22 | Bridge delivery: envelopes in CommonMark, idempotent Publications, Thread replies, error classes, test messages and the Broken probe (BE) | be | S-080, S-082 |
| [S-081](S-081-bridge-edit-windows-republication-pointer-reply-and-final-edit.md) | C-22 | Bridge edit windows: republication on a change of status, the pointer reply and the final edit as a Thread reply (BE) | be | S-074 |
| [S-075](S-075-bridge-events-by-polling-and-push-presses-answers-and-text-commands.md) | C-22 | Bridge events by polling and push: presses, answers, Text commands and deleted messages (BE) | be | S-081 |
| [S-076](S-076-bridge-account-links-by-token-and-code-and-mentions.md) | C-22 | Bridge Account links by a token sent to the bot or a code by direct message, and Mentions of linked people (BE) | be | S-075 |
| [S-077](S-077-bridge-connection-and-destination-pages-and-profile-links.md) | C-22 | Bridge Connection and Destination pages, Account links through a Bridge in the profile, and labels (FE) | fe | S-076, S-070 |
| [S-078](S-078-example-bridge-and-the-bridge-conformance-check.md) | C-22 | The conformance check `muster bridge check` against the fake bridge (BE) | be | S-076 |
| [S-083](S-083-go-bridge-library.md) | C-22 | The Go bridge library `muster-bridge-go` with its CI and releases (BE) — in `muster-io/muster-bridge-go` | be | S-078 |
| [S-087](S-087-check-a-connection-before-saving-it.md) | C-13 | Check a Connection before saving it, for Mattermost, Telegram and Bridge Connections (BE) | be | S-077, S-076 |
| [S-088](S-088-request-log-of-outbound-requests-masking-pruning-and-api.md) | C-23 | Request log of outbound requests: recording in the outbound package, masking, pruning and the API (BE) | be | S-087 |
| [S-089](S-089-request-log-of-inbound-requests-and-the-runs-of-checks.md) | C-23 | Request log of inbound requests at the callbacks, and the kinds and runs of checks and tests (BE) | be | S-088 |
| [S-090](S-090-request-log-tab-record-view-and-links.md) | C-23 | Request log tab, the record view, and the links from the Alert Group page and the check panels (FE) | fe | S-089 |
| [S-085](S-085-example-bridge-on-the-go-library.md) | C-22 | The example bridge on the Go bridge library, with its chat page and image (BE) — in `muster-io/muster-bridge-go` | be | S-083 |
| [S-086](S-086-end-to-end-test-against-the-released-example-bridge.md) | C-22 | End-to-end test against the example bridge's image, pinned by digest | infra | S-085 |
| [S-079](S-079-write-a-bridge-and-the-bridge-protocol-reference.md) | C-22 | "Write a bridge" and the Bridge protocol reference on the documentation site | docs | S-086 |
| [S-084](S-084-python-bridge-library.md) | C-22 | The Python bridge library `muster-bridge` with its CI and PyPI releases (BE) — in `muster-io/muster-bridge-python` | be | S-078, S-083 |
| [S-060](S-060-quick-start-install-concepts-operations-and-remaining-documentation.md) | C-21 | Remaining documentation sections: Quick start, Install, Alertmanager, Sign-in, Concepts and Operations | docs | S-055, S-071, S-059, S-054, S-077, S-079, S-084 |
| [S-091](S-091-documentation-for-ai-agents-llms-txt-and-markdown-pages.md) | C-21 | Documentation for AI agents: llms.txt, the full text and a Markdown version of every page | docs | S-060 |

### Pre-release

| ID | Capability | Title | Kind | Depends on |
|---|---|---|---|---|
| [S-092](S-092-snooze-presets-in-the-command-layer-and-the-api.md) | C-10 | Snooze presets in the command layer and the API, with the start of the working day and of the week (BE) | be | S-063, S-055 |
| [S-093](S-093-snooze-presets-on-messenger-buttons-and-in-text-commands.md) | C-10 | Snooze presets on the buttons of Mattermost, Telegram and Bridges, and in Text commands (BE) | be | S-092, S-075, S-082 |
| [S-094](S-094-snooze-presets-in-the-snooze-dialog-and-the-working-week-settings.md) | C-10 | Snooze presets in the Snooze dialog and the bulk Snooze, and the working day and week on the General page (FE) | fe | S-092, S-033, S-056 |
| [S-095](S-095-live-updates-mark-in-the-header.md) | C-09 | The mark of the live updates in the header — connecting, live, reconnecting and offline (FE) | fe | S-030, S-072 |
| [S-096](S-096-grafana-direct-intake.md) | C-06 | Snapshots directly from Grafana Alerting's webhook contact point (BE) | be | S-020, S-028, S-060, S-091 |

### Slack

| ID | Capability | Title | Kind | Depends on |
|---|---|---|---|---|
| [S-097](S-097-slack-connections-web-api-client-check-and-fake-slack-server.md) | C-24 | Slack Connections, the Web API client with its error classes, the Connection check and the fake Slack server (BE) | be | S-087, S-089, S-071 |
| [S-098](S-098-slack-destinations-channel-check-channel-list-doctor-and-mention-settings.md) | C-24 | Slack Destinations, the channel check, the channel list, `muster doctor` and the Mention settings of Slack (BE) | be | S-097 |
| [S-099](S-099-slack-delivery-messages-in-blocks-buttons-edits-threads-deleted-messages-and-tests.md) | C-24 | Slack delivery: messages in blocks, mrkdwn, buttons and the Snooze menu, edits, Thread replies, deleted Root messages, error classes and test messages (BE) | be | S-098, S-093 |
| [S-100](S-100-slack-presses-over-http-signed-callback-answers-and-reminder-buttons.md) | C-24 | Slack presses over HTTP: the signed callback, presses and their attribution, answers to people and Reminder buttons (BE) | be | S-099 |
| [S-101](S-101-slack-socket-mode-held-by-the-leader-envelopes-refresh-and-shared-app-warning.md) | C-24 | Slack Socket Mode held by the Leader: the WebSocket through the outbound package, confirmed envelopes, refresh and back-off, and the warning about a shared app (BE) | be | S-100 |
| [S-102](S-102-slack-account-links-by-code-by-direct-message-and-mentions-of-linked-people.md) | C-24 | Slack Account links by a code sent by direct message, and Mentions of linked people (BE) | be | S-101 |
| [S-105](S-105-check-a-slack-connection-before-saving-it.md) | C-24 | Check a Slack Connection before saving it, with the dry probe without a token (BE) | be | S-101 |
| [S-103](S-103-slack-connection-and-destination-pages-check-before-saving-and-profile-links.md) | C-24 | Slack Connection and Destination pages, the check before saving, and Slack in the profile (FE) | fe | S-102, S-105, S-070 |
| [S-104](S-104-connect-slack-documentation-with-the-app-manifests.md) | C-24 | Connect Slack on the documentation site, with the app manifests, and Slack on the Messengers page | docs | S-103 |

### Guides

| ID | Capability | Title | Kind | Depends on |
|---|---|---|---|---|
| [S-106](S-106-documentation-screenshots-tutorial-and-messenger-guides.md) | C-21 | Screenshots for the documentation, the image check, the tutorial "First alert in a messenger" and step-by-step sections on the messenger pages | docs | S-104 |
| [S-107](S-107-step-by-step-guides-with-screenshots-on-the-other-pages.md) | C-21 | Step-by-step sections with screenshots on the pages of Integrations, Routes, templates, Account links, access, Outgoing webhooks, the Organization and Alert Groups | docs | S-106 |
| [S-108](S-108-documentation-links-version-link-component-anchor-check-and-page-titles.md) | C-21 | Documentation links in the web UI: the documentation version, the link component, the check of linked anchors, and links next to the titles of the Alert Group, Integration, Route, Connection and Destination pages (FE) | fe | S-106 |
| [S-109](S-109-learn-more-and-how-to-fix-links-on-integration-route-connection-and-destination-pages.md) | C-21 | "Learn more" links next to complex fields and "How to fix" links next to checks, warnings and Broken states on the Integration, Route, Connection, Destination and Alert Group pages (FE) | fe | S-108 |
| [S-110](S-110-documentation-links-on-statistics-profile-access-and-organization-pages.md) | C-21 | Documentation links on the Statistics, profile, Access and Organization pages: page titles, fields and problems (FE) | fe | S-109, S-107 |

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
load profile of S-061 measures it. S-066 and S-067 were split from S-042 before its implementation, because with the
files its contract had missed it touched about 63 files. S-042 keeps the Telegram Destinations, the Destination check,
`muster doctor`, the Root message posts and edits with the response mapping, and the documentation page; S-066 takes
the comment Threads with the copy buffer, the copy wait and the lost Thread, and S-067 the button presses with the
update router lock. Both follow S-042 in the Shadow phase, before S-043, which needs "Thread not attached" from S-066;
S-047 and S-049 follow S-067. S-042 keeps its file name. S-068 was split from S-049 by the plan review of the
Actions phase, before its implementation, because with the files its contract had missed S-049 touched about 58 files
(D298). S-049 keeps the ack timeout, Unclaimed, Reminders as plain Thread replies, auto-unacknowledge, the collapse,
`next_notice`, the `unclaimed` filter, `answerReminder` and the lifecycle rows; S-068 takes the buttons on Reminder
Thread replies, the presses on them and the expired-button answer. S-068 follows S-049, S-050 depends on S-049 only,
and S-051 follows S-068, whose buttons its Reminder presses need. S-069 was added after the maintainer made several
Connections of one messenger a supported setup (D305): it adds a second fake Mattermost server and bot and a second
Telegram bot, and covers two Connections of each messenger, two Destinations in one chat and the Identity spaces of
Account links end to end. It belongs to C-18 and follows S-052 in the Actions phase, because its checks need Account
links made through the profile and the pages of S-052. S-070 was added with S-069 (D305, D309): it labels Connections
and Destinations across the UI, rewords the Telegram Account link and warns about two Destinations in one chat; it
follows S-069. S-071 was split from S-055 by the plan review of the Operations phase, before its implementation,
because with the files that review added S-055 touched about 40 files (D311). S-055 keeps the Organization settings,
the values seen and the outbound policy; S-071 takes the Keyring status, `muster secrets rotate-key`, the Leader's
re-render after a rotation and the repeated expiry of Reminder buttons. S-071 follows S-055 in the Operations phase,
and S-056, whose Keyring page needs it, and S-060 follow it. S-055 keeps its file name. S-072 was added after S-056
(D314): it cuts the main navigation to eight entries — "Access" takes Users, Service accounts and OIDC as a
sub-navigation like that of Organization, the Audit log moves under Organization and Profile stays in the user menu
only. It belongs to C-03, whose application shell it changes, but follows S-056 in the Operations phase, because it
extends S-056's sub-navigation. S-073 to S-079 were added with C-22 after S-059 (D317, D319, D321): a Bridge
Connection reaches any messenger through a separate service that speaks the Bridge protocol of ADR-0017. They come
after the Operations phase because they build on the Account links, Reminder buttons, labels and key rotation of the
earlier phases and on the documentation site, and S-060 moves after them, so that its Quick start and Concepts name
Bridges; S-060 keeps its file name and its capability, C-21. S-080 and S-081 were split from S-073 and S-074 by the plan
review of the Bridge phase, before their implementation, because with the files that review added they touched about
50 and 45 files (D323). S-073 keeps the Bridge Connection, the protocol client with its error classes, `info` and the
fake bridge, and S-080 takes Destinations, the Destination check, `muster doctor` and the Mention settings of Bridges;
S-074 keeps Bridge delivery — envelopes, idempotent Publications, edits, Thread replies, error classes, test messages
and the Broken probe — and S-081 takes the edit windows. S-080 follows S-073 and S-081 follows S-074, so S-074 depends
on S-080 and S-075 on S-081; S-073 and S-074 keep their file names. Style hints on the buttons of Mattermost and
Telegram (D320), which the Bridge protocol brought in, became S-082 once their facts were checked on the test
environment (F-066 to F-068, D329). It belongs to C-12, whose buttons it styles, but follows S-059 at the end of the
Operations phase, before the Bridge phase; S-074 takes its mapping as the style hints of Bridge buttons, so it depends
on S-082. S-083 to S-086 were added when the maintainer decided that bridge libraries for Go and Python, in
repositories of their own under MIT without a CLA, do the protocol's part of a bridge, and that the example bridge
moves into the Go library's repository (D343). S-078 keeps `muster bridge check`, tested against the fake bridge, and
its file name; it comes first, because the libraries' CI runs the check from Muster's development image. S-083 builds
the Go library and S-085 the example bridge on it, both in `muster-io/muster-bridge-go` and split because together they
touched about 46 files; S-086 then runs Muster's end-to-end test against the example's image, pinned by digest —
`edge` until the libraries are released with L1, then the release (D366) — which completes C-22.AC-22 and covers
C-22.AC-23. S-079 follows with Write a bridge, which leads with the Go library; S-084, the Python library in
`muster-io/muster-bridge-python`, depends only on S-078, whose check its CI runs, and S-083, whose design it follows,
and was built beside S-079 (D369); S-060, which follows both, adds the Python library's section of Write a bridge.

S-087 to S-090 were added after S-083 merged, when the maintainer tried a Bridge on the test environment (D353, D354).
S-087 adds the check of a Connection before saving it, for every Connection type, with a dry probe for Mattermost and
Bridges and the rule that a stored token goes only to its saved address; it belongs to C-13, whose FR-14 states it for
all three types, and its operation and form panel are one pull request, because neither half is useful alone and
together they touch 40 files, at the edge of the size signal. S-088 to S-090 build the Request log of C-23, in three
parts because together they touched about 80 files: S-088 the table, recording in the outbound package, masking, pruning
and the API; S-089 the inbound requests at the callbacks and the kinds and runs of checks, the checks before saving of
S-087 included; S-090 the pages. They follow S-083 and come before S-085 in the maintainer's queue; S-085 still depends
only on S-083, since nothing of theirs is in the library's repository.

S-091 was added when the maintainer decided that the site also serves documentation for AI agents — `llms.txt`, the
text of the whole site in one file and a Markdown version of every page (D371). It belongs to C-21 and follows S-060,
the last story that adds pages, so that its map covers the whole site.

S-092 to S-095 were added after every other story of L1 had merged, when the maintainer took two features into L1
before its release (D379): Snooze presets — "Until tomorrow 09:00" and "Until Monday 09:00", from the Organization's
start of the working day and first day of the week, in the person's time zone (C-10.FR-17, FR-18, C-20.FR-9) — and a
mark in the header that says whether the live updates are connected (C-09.FR-26). The presets are three stories,
because together they touched about 60 files: S-092 the rule, the settings and the API; S-093 the buttons of
Mattermost, Telegram and Bridges and the Text commands, on the messengers of the earlier phases; S-094 the Snooze
dialog, the bulk Snooze and the General page. S-095, the mark, is one frontend story with no change to the server.
They come last, in that order; S-093 and S-094 each need only S-092, and S-095 needs none of them.

S-096 was added when the maintainer took Grafana Alerting's webhook contact point into L1 as a second source (D383),
after a live test of Grafana OSS 13.2.4 showed that its built-in Alertmanager keeps what Snapshot processing relies on
— a `groupKey` that names the route and the group, the whole group in every Snapshot, repeats at the
`repeat_interval` — and found two cases that Muster read wrongly: the resolve that the other replica of a Grafana HA
pair sends with an older `startsAt`, and the `status` of a truncated Snapshot, which Grafana computes from the alerts it
lists (F-105 to F-112). It belongs to C-06, whose rules it changes, and is one backend story with the page Connect
Grafana. It comes after S-095 and needs none of S-092 to S-095.

S-097 to S-105 were added when the maintainer took native Slack into L1 before its release (D381, D382, D385), after a
test on a Slack workspace established the facts F-070 to F-104: a Slack Connection is the bot of one Slack app in one
workspace, receives presses over Socket Mode — a connection that only the Leader holds — or over HTTP at the ingest
listener, and links accounts by a code sent by direct message. They form C-24 and come last, as a phase of their own,
because they build on every messenger story before them: the check before saving of S-087, the Request log of S-088
and S-089, key rotation, Reminder buttons and the Snooze presets of S-093. Their order follows the Bridge's: the
Connection, the client and the fake server (S-097); Destinations and their check (S-098); delivery, with test
messages and the Broken probe (S-099); presses over HTTP, with the answers, the Transport and the schema for Slack's
Account links (S-100); Socket Mode on the Leader (S-101); Account links and Mentions (S-102); the check before saving
of a Slack Connection (S-105), split from the pages by the review of these contracts so that the frontend story stays
one; the pages (S-103); and the page "Connect Slack" (S-104). S-105 keeps its place before S-103 although its ID is
larger. S-097 touches 43 files, at the edge of the size signal, because the type, the client and the fake server are
each needed for the live check of the others.

S-106 to S-110 were added before the release of 0.1.0, when the maintainer decided that the documentation site gets
step-by-step guides with screenshots of the English UI and that the web UI links each page, complex field and problem
to the documentation of the version it runs (D417 to D422; C-21.FR-9 to FR-12). The screenshots are taken by a
Playwright suite on `muster dev`, by hand like the README demo, and `make docs-check` keeps the pages' references and the
images in step. S-106 builds the suite, the image check, the tutorial "First alert in a messenger" and the messenger
pages; S-107 the sections of every other page. The links were planned as one story, S-108, and split into three when
their contracts were written, because placing them touches about 100 files: S-108 the documentation version that the
server gives the session, the link component, the check of every linked anchor against the built site and the links
next to the titles of the main pages; S-109 the links of the fields and problems of those pages; S-110 those of the
Statistics, profile, Access and Organization pages. They come last, as a phase of their own, because they document and
link pages of every earlier phase. S-108 needs only S-106, whose sections its links open, and may be built beside
S-107; S-109 links only to sections that exist after S-106, and S-110 needs S-107's sections and follows S-109, since
both change the list of links and the translations. S-110 ends by taking every screenshot again, so that the guides
show the links.

### L2 Teams

| ID | Capability | Title | Kind | Depends on |
|---|---|---|---|---|
| [S-166](S-166-shared-fixtures-of-destination-and-connection-reads-in-the-web-tests.md) | C-28 | Shared fixtures of Destination, Connection and Alert Group reads in the web tests | infra | — |
| [S-111](S-111-minimum-severity-and-urgent-as-critical.md) | C-26 | Minimum severity, Urgent as critical, and the expand step of the urgent mark and of "critical is Urgent" (BE) | be | — |
| [S-162](S-162-the-route-profile-paging.md) | C-26 | The Route profile Paging, formerly On-call, with the id paging in the API, the UI and every client (BE) | be | S-111 |
| [S-112](S-112-minimum-severity-on-the-route-form.md) | C-26 | Minimum severity on the Route form, and the urgent mark and "critical is Urgent" taken off the pages (FE) | fe | S-111, S-162 |
| [S-113](S-113-button-sets-of-the-organization-and-the-destinations.md) | C-28 | Button sets of the Organization and the Destinations (BE) | be | S-111, S-166 |
| [S-114](S-114-buttons-section-of-the-destination-form-and-the-organization-button-set.md) | C-28 | The Buttons section of the Destination form with its preview, and the Organization's button set (FE) | fe | S-113, S-112 |
| [S-115](S-115-teams-people-owning-team-filters-and-working-hours.md) | C-25 | Teams, people added by hand, the owning Team of Routes and Destinations, the filters by Team, and Working hours (BE) | be | S-166 |
| [S-116](S-116-spike-gitlab-role-claims.md) | C-25 | Spike: GitLab's role claims — which token carries them, inherited memberships and discovery | docs | — |
| [S-117](S-117-claims-of-mapping-entries-subtrees-and-membership-from-the-identity-provider.md) | C-25 | The claim of every mapping entry and subtrees for Roles and Teams, and Team membership from the identity provider (BE) | be | S-115, S-116 |
| [S-118](S-118-team-label-team-matchers-and-the-teams-of-alerts-and-alert-groups.md) | C-25 | The Team label and Team matchers, the Teams of Alerts and Alert Groups with team_joined, and unknown values (BE) | be | S-115 |
| [S-163](S-163-team-label-values-and-the-evaluation-of-open-alert-groups-on-save.md) | C-25 | Values of the Team label without a Team, and the evaluation of open Alert Groups when a Team or the Team label is saved (BE) | be | S-118, S-111 |
| [S-121](S-121-severity-conditions-delivery-to-teams-and-destination-joined.md) | C-26 | Severity conditions on the Destinations of Routes and Teams, delivery to the Teams of an Alert Group, and destination_joined (BE) | be | S-111, S-118, S-163 |
| [S-170](S-170-changes-of-route-and-team-destinations-and-the-final-edits.md) | C-26 | Changes of the Destinations of Routes and Teams that publish open Alert Groups Quietly, and the final edits of Destinations no entry names any more (BE) | be | S-121 |
| [S-122](S-122-the-preview-where-alerts-go.md) | C-26 | The preview "Where alerts go" for the unsaved settings of a Route (BE) | be | S-121 |
| [S-119](S-119-teams-list-and-team-page-people-identity-provider-groups-and-working-hours.md) | C-25 | Teams list and Team page with people, identity provider groups and Working hours, the claims of the Role mapping, and the filters by Team (FE) | fe | S-115, S-117, S-118, S-121, S-112 |
| [S-120](S-120-which-alerts-belong-unknown-values-team-label-and-teams-of-alert-groups.md) | C-25 | "Which Alerts belong" on the Team page, unknown values with "Create Team", the Team label setting, and the Teams of Alert Groups and Alerts on their pages (FE) | fe | S-118, S-163, S-119 |
| [S-123](S-123-route-form-destinations-teams-preview-and-delivery-state-by-entry.md) | C-26 | The Route form with Destination conditions, the Teams from the label and the preview, the Destinations of a Team, and the delivery state by entry (FE) | fe | S-112, S-120, S-122 |
| [S-124](S-124-documentation-teams-routing-to-teams-and-what-critical-changes.md) | C-26 | Documentation: Teams with a worked example, Routing to Teams, and "What critical changes" | docs | S-123, S-170 |
| [S-125](S-125-compact-layout-in-mattermost-slack-and-telegram-with-labels-in-the-message.md) | C-29 | The compact message layout in Mattermost, Slack and Telegram, with "Labels in the message" (BE) | be | S-113, S-118 |
| [S-126](S-126-layout-in-the-bridge-protocol-the-bridge-adapter-and-the-fake-bridge.md) | C-29 | The structured layout in the Bridge protocol, the Bridge adapter, muster bridge check and the fake bridge (BE) | be | S-125 |
| [S-127](S-127-go-bridge-library-types-for-layout.md) | C-29 | Types for the message layout in the Go bridge library (BE) — in `muster-io/muster-bridge-go` | be | S-126 |
| [S-128](S-128-python-bridge-library-types-for-layout.md) | C-29 | Types for the message layout in the Python bridge library (BE) — in `muster-io/muster-bridge-python` | be | S-126 |
| [S-129](S-129-labels-in-the-message-and-the-preview-for-every-markup.md) | C-29 | "Labels in the message" on the Route form, and the preview of the compact layout for every markup (FE) | fe | S-123, S-126 |
| [S-130](S-130-documentation-message-layout-per-messenger-and-layout-for-bridge-authors.md) | C-29 | Documentation: the message layout in each messenger, "Labels in the message", and layout for bridge authors | docs | S-129, S-127, S-128 |

### L2 On call

| ID | Capability | Title | Kind | Depends on |
|---|---|---|---|---|
| [S-131](S-131-schedules-rotation-layers-overrides-the-on-call-and-the-result.md) | C-27 | Schedules, Rotation layers, Overrides with their Permission and precondition, the On-call and the Result (BE) | be | S-115 |
| [S-132](S-132-spike-telegram-muted-discussion-groups-and-getchatmember.md) | C-27 | Spike: Telegram's muted discussion groups and getChatMember for people | docs | — |
| [S-133](S-133-gaps-the-leader-gap-scan-musterschedulegap-and-on-call-now.md) | C-27 | Gaps of Schedules, the Leader's gap scan with MusterScheduleGap, its chart rule and runbook, and On call now (BE) | be | S-118, S-131 |
| [S-164](S-164-warnings-about-unreachable-on-calls.md) | C-27 | Warnings about unreachable On-calls, with the members of a Telegram discussion group (BE) | be | S-121, S-132, S-133 |
| [S-134](S-134-the-personal-ical-feed-with-its-token.md) | C-27 | The personal iCal feed of On-call times with its token (BE) | be | S-131 |
| [S-135](S-135-schedule-page-two-week-view-rotation-layers-overrides-and-warnings.md) | C-27 | The Schedule page with its two-week view, Rotation layers, Overrides with the Override form, and warnings (FE) | fe | S-119, S-133, S-164 |
| [S-136](S-136-schedules-on-the-team-page-on-call-now-and-the-ical-section.md) | C-27 | Schedules on the Team page, On call now on the Home page, the Team page and the Teams list, and the iCal section (FE) | fe | S-134, S-135 |
| [S-137](S-137-documentation-schedules.md) | C-27 | Documentation: Schedules with Rotation layers, several Schedules, Overrides, gaps and the iCal feed | docs | S-136 |
| [S-138](S-138-spike-mattermost-groups-and-slack-user-groups.md) | C-25 | Spike: Mattermost groups and Slack user groups — reading them, mentioning them, and everyone from a bot | docs | — |
| [S-139](S-139-team-mentions-with-the-connections-checks-and-connection-groups.md) | C-25 | Team mentions per Connection, with the groups a Connection reads and the group mentions a bridge declares (BE) | be | S-115, S-126, S-138 |
| [S-140](S-140-mention-entries-their-resolution-the-migration-of-the-l1-settings-and-storm-started.md) | C-28 | Mention entries with their Loud events and their resolution, the migration of the L1 Mention settings, and storm_started (BE) | be | S-121, S-131, S-139 |
| [S-171](S-171-group-and-everyone-mentions-in-slack-and-through-bridges.md) | C-28 | Group mentions in Slack, and group mentions and mentions of everyone through bridges, with the kind of a Mention in the Bridge protocol (BE) | be | S-140 |
| [S-165](S-165-undelivered-mentions-in-the-timeline-and-the-destination-warnings.md) | C-28 | Undelivered Mentions in the Timeline and the warnings of a Destination (BE) | be | S-140 |
| [S-141](S-141-go-bridge-library-group-and-everyone-mentions.md) | C-28 | Group mentions and mentions of everyone in the chat in the Go bridge library (BE) — in `muster-io/muster-bridge-go` | be | S-171, S-127 |
| [S-142](S-142-python-bridge-library-group-and-everyone-mentions.md) | C-28 | Group mentions and mentions of everyone in the chat in the Python bridge library (BE) — in `muster-io/muster-bridge-python` | be | S-171, S-128 |
| [S-143](S-143-mentions-grid-of-the-destination-form-and-how-to-mention-the-team.md) | C-28 | The Mentions grid of the Destination form with its warnings and What each messenger supports, and How to mention the Team (FE) | fe | S-114, S-119, S-140, S-165 |
| [S-144](S-144-documentation-whom-a-message-mentions-and-team-mentions.md) | C-28 | Documentation: whom a message mentions, with the entries, what each messenger supports and Team mentions | docs | S-132, S-143 |

### L2 Paging

| ID | Capability | Title | Kind | Depends on |
|---|---|---|---|---|
| [S-145](S-145-the-role-gateway-in-the-bridge-protocol-the-check-and-the-fake-gateway.md) | C-30 | The role gateway in the Bridge protocol, sendNotification and notification_acknowledged, the check of the role and the fake gateway (BE) | be | S-166 |
| [S-167](S-167-contacts-at-notification-gateways-confirmed-by-a-code.md) | C-30 | Contacts at Notification gateways confirmed by a code, erased with their User and masked in the Request log (BE) | be | S-145 |
| [S-146](S-146-go-bridge-library-the-role-gateway.md) | C-30 | The role gateway in the Go bridge library (BE) — in `muster-io/muster-bridge-go` | be | S-145, S-141 |
| [S-147](S-147-python-bridge-library-the-role-gateway.md) | C-30 | The role gateway in the Python bridge library (BE) — in `muster-io/muster-bridge-python` | be | S-145, S-142 |
| [S-148](S-148-the-queue-of-calls-through-notification-gateways-with-combining-and-retries.md) | C-30 | The queue of calls through Notification gateways with combining and retries (BE) | be | S-167 |
| [S-173](S-173-the-acknowledge-of-the-transport-gateway.md) | C-30 | The Acknowledge of the Transport gateway, only while nobody owns the Alert Group, and its answers in the call (BE) | be | S-148 |
| [S-149](S-149-broken-notification-gateways-with-their-internal-alert-chart-rule-and-runbook.md) | C-30 | Broken Notification gateways with MusterNotificationGatewayBroken, its chart rule and runbook, and the probe (BE) | be | S-148 |
| [S-150](S-150-contact-for-calls-in-the-profile-roles-and-health-of-a-gateway-and-a-users-contacts.md) | C-30 | The contact for calls and SMS in the profile, the roles and health of a Notification gateway, and a User's contacts (FE) | fe | S-136, S-149 |
| [S-151](S-151-documentation-connecting-a-notification-gateway-and-writing-a-gateway-bridge.md) | C-30 | Documentation: connecting a Notification gateway, confirming a contact and writing a gateway bridge | docs | S-130, S-146, S-147, S-150, S-173 |
| [S-152](S-152-notification-rules-their-defaults-and-the-switch.md) | C-31 | Notification rules, the Organization's defaults, the four states and the switch that adds a call (BE) | be | S-167 |
| [S-153](S-153-notification-rules-in-the-profile-on-a-users-page-and-the-organizations-defaults.md) | C-31 | Notification rules in the profile with the switch, on a User's page, and the Organization's defaults (FE) | fe | S-150, S-152 |
| [S-154](S-154-escalation-chains-and-steps-and-naming-them-on-routes-and-teams.md) | C-32 | Escalation chains and their steps, naming them on Routes and Teams with Severity conditions, and the refusals that protect them (BE) | be | S-140, S-148, S-152 |
| [S-169](S-169-the-lifecycle-of-escalation-chains-with-the-ack-timeout-unclaimed-and-skipped-steps.md) | C-32 | The lifecycle of Escalation chains on Alert Groups, with the ack timeout, Unclaimed, waits, repeats and skipped steps (BE) | be | S-154 |
| [S-155](S-155-pages-by-mention-in-the-threads-of-their-chains-notification-rule-runs-and-undelivered-pages.md) | C-32 | Pages by Mention in the Threads of their own chains, the runs of Notification rules, and undelivered Pages (BE) | be | S-152, S-164, S-165, S-169, S-171 |
| [S-177](S-177-pages-in-telegram-through-bridges-and-to-outgoing-webhooks.md) | C-32 | Pages by Mention in Telegram, through Bridges and to outgoing webhooks (BE) | be | S-155 |
| [S-178](S-178-the-pages-of-chain-runs-and-of-the-timeline-in-the-api.md) | C-32 | The Pages of the runs of Escalation chains and of the Timeline in the API, and paged in the Timeline of the SPA (BE) | be | S-155 |
| [S-172](S-172-warnings-about-on-calls-whom-the-destinations-of-their-chains-cannot-mention.md) | C-27 | Warnings about On-calls whom the Destinations of their chains cannot mention, with the Telegram discussion group check for them (BE) | be | S-155, S-177 |
| [S-168](S-168-assignees-in-the-root-message-and-the-layout-the-owners-team-and-the-assignee-filter.md) | C-32 | Assignees — the people the chains Paged last — in the Root message and the layout, and the Assignee filter (BE) | be | S-126, S-155 |
| [S-156](S-156-the-step-send-to-an-outgoing-webhook-and-escalation-webhook.md) | C-32 | The step "Send to an outgoing webhook" and the event escalation_webhook (BE) | be | S-169 |
| [S-157](S-157-pages-through-a-notification-gateway-from-escalation-steps-and-notification-rules.md) | C-32 | Pages through a Notification gateway from Escalation steps and Notification rules, queued as calls (BE) | be | S-149, S-156, S-168, S-173, S-178 |
| [S-158](S-158-the-chain-page-with-how-it-went-and-the-chains-on-the-route-form-and-the-team-page.md) | C-32 | The Escalation chain page with its steps and "How it went", and the chains on the Route form and the Team page (FE) | fe | S-123, S-150, S-157, S-178 |
| [S-159](S-159-running-chains-and-assignees-on-the-alert-group-page-the-assignee-filter-and-i-am-an-assignee.md) | C-32 | Running chains and Assignees on the Alert Group page at phone width, the Assignee filter, and "I am an Assignee" on the Home page (FE) | fe | S-158, S-168, S-178 |
| [S-160](S-160-spike-push-of-mentions-in-thread-replies.md) | C-32 | Spike: push of Mentions in Thread replies to a phone in Mattermost, Slack and Telegram | docs | — |
| [S-161](S-161-documentation-escalation-chains-notification-rules-and-how-an-alert-reaches-people.md) | C-32 | Documentation: Escalation chains, Notification rules and How an alert reaches people | docs | S-124, S-137, S-151, S-153, S-157, S-159, S-160, S-177 |

S-162, S-163, S-166 and S-170 were split or added when the contracts of the stage "L2 Teams" were written and reviewed.
S-162, the Route profile Paging, was split from S-111, because the profile's id appears in thirteen web end-to-end specs
and together with the Minimum severity the story touched about 55 files; it follows S-111, and S-112 depends on it.
S-163 was split from S-118, which with it touched about 50 files: S-118 keeps the Team label, the Team matchers and the
Teams of Alerts and Alert Groups, and S-163 takes the values of the Team label seen without a Team and the evaluation of
open Alert Groups when a Team is saved; it follows S-118, and S-121 and S-120 depend on it. S-170 was split from S-121
by the review of the contracts: S-121 keeps the Destination entries, the reach, `destination_joined` and the Storms with
Team Destinations, and S-170 takes the changes of configuration that move the reach of open Alert Groups — a Route's
entries, conditions and option, a Team's Destinations and its deletion — with their Quiet Publications and the final
edits; it follows S-121, needs no migration of its own, and S-124 depends on it. S-121 and S-122 come before S-119,
whose Team page shows the Destinations of a Team. S-166 was added by the review of those contracts: the web tests build
Destination, Connection and Alert Group reads by hand, so every backend story that adds a required field to them would
break the TypeScript check of its own pull request; S-166 moves those reads into shared builders that later stories
extend, and it comes first. In the stage "L2 On call", S-164 was split from S-133 at the contract boundary of C-27.FR-8
— the gaps, the gap scan and "On call now" need no messenger, while the warnings about unreachable On-calls need every
Destination type — and S-165 from S-140, at the boundary between whom a message mentions and what is recorded when it
cannot; each follows the story it was split from. S-171 was split from S-140 by the review of the contracts: S-140 keeps
the entries, their migration and their resolution, and S-171 takes writing group mentions in Slack, and group mentions
and mentions of everyone through bridges, with the `kind` of a Mention in `api/bridge.yaml`; it follows S-140, needs no
migration of its own, and the bridge libraries S-141 and S-142, which copy `api/bridge.yaml`, and S-155, which writes
Team mentions and tokens in the Threads of chains, depend on it.

In the stage "L2 Paging", S-167, S-168 and S-169 were split when the contracts were written, each because its story
would have touched about 45 to 50 files. S-167 was split from S-145: S-145 keeps the protocol of the role `gateway`, its
check and the fake gateway, which the bridge libraries need, and S-167 takes the contacts with their code, their limits,
their erasure with a User and their masking in the Request log; it follows S-145 and comes before the libraries, S-146
and S-147, which need only S-145. S-169 was split from S-154: S-154 keeps the chains as configuration — their steps,
naming them on Routes and Teams and the refusals that protect them — and S-169 takes their lifecycle on Alert Groups,
with the ack timeout, Unclaimed, waits, repeats and skipped steps; it follows S-154. S-168 was split from S-155: S-155
keeps the Pages by Mention, the runs of Notification rules and undelivered Pages, and S-168 takes the Assignees, item 7
of the Root message and `layout`, the Owner named without a Team and the Assignee filter; it follows S-155, and S-157 and S-159 depend
on it. The runs of Notification rules, which the first plan gave S-152, moved to S-155 (Mentions) and S-157 (calls),
because a run only Pages people once Escalation steps Page them; S-152 keeps the rules, their defaults and their states.
S-172 was split from S-155 by the review of the contracts, when the Telegram discussion group check had to cover the
people that chains Page and the Timeline read path joined S-155, which would then have touched about 47 files: S-155
keeps the Pages, and S-172 takes the chains' Destinations in the warnings about unreachable On-calls and that check; it
follows S-155. S-173 was split from S-148 in the same review, when the Timeline read path and the exhaustive switches of
the SPA joined S-148, which would then have touched about 47 files: S-148 keeps the queue of calls, and S-173 takes the
Acknowledge of the Transport `gateway` with its answers; it follows S-148, and S-151 and S-157 depend on it. S-177 and
S-178 were split from S-155 before it was implemented, when its Pages in every kind of Destination and their reads
would have touched about 80 files: S-155 keeps the Pages in Mattermost and Slack, the runs of Notification rules,
undelivered Pages and the metric; S-177 takes the Pages in Telegram, through Bridges and to outgoing webhooks, and
S-178 the Pages of the runs of chains and the fields of a Page in the Timeline of the API. Both follow S-155; S-172
follows S-177, whose Telegram Destinations it checks, S-161 follows it for the table of C-32.FR-11, and S-157, S-158 and
S-159 follow S-178, whose reads they extend or show. The spike
on the push of Mentions, S-160, depends on nothing and may run at any time; only the documentation of S-161 waits for
it.

The plan review of the L2 core against the code removed two edges that tied much of the plan to the maintainer's spikes:
with them, the spike S-116 held up 33 stories and S-132 23; without them, 18 and 15. S-139 no longer depends on S-119:
the builder of Team reads in the web tests (`web/src/test/fixtures/teams.ts`), for which alone it waited, is created by
S-115 with the Team read, and S-117, S-118 and S-121 extend it. S-167 no longer depends on S-164: `gateway_reaches` on
the warnings about unreachable On-calls, for which alone it waited, moved with its Go code to S-150, which shows it and
already follows both S-164 (through S-136) and S-167 (through S-149); S-155 still follows S-164, and S-139 still waits
for the spike S-138.

### L2 Statistics

| ID | Capability | Title | Kind | Depends on |
|---|---|---|---|---|
| [S-174](S-174-first-acknowledged-by-and-statistics-by-team-and-by-people.md) | C-25 | Who made the first acknowledgement, filled from the Timeline, and the statistics By Team and By people with the filters of every view (BE) | be | S-118 |
| [S-175](S-175-statistics-page-by-team-by-people-and-the-filters.md) | C-25 | The views By Team and By people and the filters of every view on the statistics page (FE) | fe | S-174, S-120 |
| [S-176](S-176-documentation-statistics-by-team-and-by-people.md) | C-25 | Documentation: statistics By Team and By people, the first acknowledgement and the filters | docs | S-174, S-175 |

S-174 to S-176 were added for 0.2.0 from production use (journal D688), after the contracts of the L2 core: the first
acknowledgement records who made it, and the statistics page gains the views By Team and By people and the list's
filters Route, Integration, Severity level, Urgent, Team, Reopened and labels, with "Acknowledged first by". They extend
C-09, whose new requirements are marked as added for L2, and are filed under C-25, since the stories of one capability
share one layer and milestone (`scripts/stories-sync`), as S-117 covers C-03.FR-5 from C-25. Of the L2 core they need
only the Teams of Alert Groups and the filter by Team, S-118 for the API and S-120 for the list's filter on the page, so
a lead runs them beside the remaining chains of the stages above. S-174's migration takes the next free number when it
starts, as section 4 says for a story merged out of the map's order.

### L2 Terraform provider

| ID | Capability | Title | Kind | Depends on |
|---|---|---|---|---|
| [S-179](S-179-the-specification-under-mit-and-managed-by-on-routing-teams-and-chains.md) | C-33 | The API specification under MIT, and the managed_by mark on Integrations, Routes, Teams, Schedules, Escalation chains, Lookup tables and Link rules (BE) | be | — |
| [S-180](S-180-managed-by-on-connections-destinations-and-the-settings-objects.md) | C-33 | The managed_by mark on Connections, Destinations, the Organization's settings, the Outbound address policy and the OIDC settings (BE) | be | S-179 |
| [S-181](S-181-the-managed-by-mark-and-the-edit-warnings-on-the-configuration-pages.md) | C-33 | The "Managed by" mark and the warnings before a change on every configuration page (FE) | fe | S-180, S-158 |
| [S-182](S-182-terraform-provider-skeleton-client-configuration-ci-and-release-pipeline.md) | C-33 | The Terraform provider's repository, its generated client, the provider configuration with the version check, CI with acceptance tests against muster dev, and the release pipeline — in `muster-io/terraform-provider-muster` | infra | S-181 |
| [S-183](S-183-terraform-provider-organization-outbound-policy-and-oidc-settings.md) | C-33 | The Terraform resources of the objects that exist once: the Organization's settings with the default Notification rules, the Outbound address policy and the OIDC settings with the Role mapping (BE) — in `muster-io/terraform-provider-muster` | be | S-182 |
| [S-184](S-184-terraform-provider-integrations-integration-tokens-and-connections.md) | C-33 | The Terraform resources of Integrations, Integration tokens and the Connections of each type, and the data source muster_connection (BE) — in `muster-io/terraform-provider-muster` | be | S-182 |
| [S-185](S-185-terraform-provider-destinations-lookup-tables-and-link-rules.md) | C-33 | The Terraform resources of the Destinations of each type with the Secrets of outgoing webhooks, Lookup tables and Link rules (BE) — in `muster-io/terraform-provider-muster` | be | S-184 |
| [S-186](S-186-terraform-provider-teams-members-team-destinations-schedules-and-the-user-and-team-data-sources.md) | C-33 | The Terraform resources of Teams, Team members, a Team's Destinations and Schedules with their Rotation layers, and the data sources muster_user and muster_team (BE) — in `muster-io/terraform-provider-muster` | be | S-185 |
| [S-187](S-187-terraform-provider-escalation-chains-routes-the-default-route-and-the-route-order.md) | C-33 | The Terraform resources of Escalation chains, a Team's chains, Routes, the Default route and the Route order, with a complete example (BE) — in `muster-io/terraform-provider-muster` | be | S-183, S-186 |
| [S-188](S-188-terraform-provider-guides-signing-key-and-publication-in-both-registries.md) | C-33 | The Terraform provider's guides, its signing key, and its first release in the Terraform Registry and the OpenTofu Registry — in `muster-io/terraform-provider-muster` | infra | S-187 |
| [S-189](S-189-documentation-configuration-as-code-with-the-terraform-provider.md) | C-33 | Documentation: configuration as code with the Terraform provider, and the link from the "Managed by" mark | docs | S-181, S-188 |

S-179 to S-189 were written when the maintainer settled the Terraform provider (journal D492, D506, D757), the first
item of [L2 §1.5](../prd/L2.md#15-specified-later-in-l2) to be specified. Three are Muster's: the specification under
MIT and the mark `managed_by` on the configuration objects, split into S-179 and S-180 at the boundary between two
groups of domains because together they touched about 45 files, and the mark with its warnings on the pages, S-181,
which follows S-158 because it changes the Escalation chain page that S-158 rebuilds. S-179 adds the one migration for
all twelve tables, 0055. The three ship in 0.2.0, as the maintainer decided, which makes 0.2.0 the provider's minimum
version. S-182 to S-188 are worked in `muster-io/terraform-provider-muster` and start after the
tag of 0.2.0, which `depends_on` cannot name: S-182 builds the repository, the client, the provider's configuration,
CI with the acceptance tests against `muster dev` from Muster's image, and the release pipeline; S-183 to S-187 add the
resources by group, in the order in which they reference each other — the objects that exist once beside the rest,
then Integrations and Connections, Destinations, Teams and Schedules, and last Escalation chains and Routes with the
complete example; S-188 writes the guides and publishes the first release in both Registries, with the maintainer's
signing key and accounts. S-189, Muster's page on configuration as code, follows the release so that its links to the
Registries resolve.

### L2 0.2.x

| ID | Capability | Title | Kind | Depends on |
|---|---|---|---|---|
| [S-190](S-190-maintenance-windows-their-permissions-limits-api-audit-log-and-the-timer-rows-of-start-and-end-be.md) | C-34 | Maintenance windows with their Permissions, limits, API, Audit log and the timer rows of their start and end (BE) | be | S-115, S-118, S-169 |
| [S-191](S-191-which-alert-groups-a-window-keeps-back-the-rules-the-state-and-maintenance-started-and-ended-be.md) | C-34 | Which Alert Groups a window keeps back — the rules of the three objects, Urgent, the Internal alerts of an Integration, and maintenance_started and maintenance_ended (BE) | be | S-190, S-111, S-118, S-121, S-169 |
| [S-192](S-192-nothing-new-delivered-for-a-kept-back-alert-group-and-its-publication-with-the-mark-at-the-end-be.md) | C-34 | Nothing new delivered for a kept-back Alert Group, and its publication with the mark when the window ends (BE) | be | S-191, S-121, S-140, S-169, S-155 |
| [S-193](S-193-chains-notification-rules-calls-the-ack-timeout-and-reminders-while-an-alert-group-is-kept-back-be.md) | C-34 | Escalation chains, Notification rules, calls, the ack timeout and Reminders while an Alert Group is kept back (BE) | be | S-192, S-169, S-155, S-156, S-157, S-168 |
| [S-194](S-194-the-maintenance-windows-sections-of-the-integration-route-and-team-pages-and-the-list-of-windows-fe.md) | C-34 | The Maintenance windows sections of the Integration, Route and Team pages, and the list of windows (FE) | fe | S-190, S-191, S-123, S-120, S-158 |
| [S-195](S-195-the-kept-back-banner-badge-and-filter-on-the-alert-group-page-and-list-and-the-marks-in-the-timeline-fe.md) | C-34 | The "Kept back" banner, badge and filter on the Alert Group page and list, and the marks in the Timeline and delivery states (FE) | fe | S-194, S-192, S-159 |
| [S-196](S-196-documentation-maintenance-windows.md) | C-34 | Documentation: Maintenance windows | docs | S-193, S-194, S-195 |
| [S-197](S-197-the-loud-and-quiet-settings-of-a-route-with-their-profiles-migration-and-api-be.md) | C-36 | The Loud and Quiet settings of a Route with their profiles, migration and API (BE) | be | S-162, S-140, S-154 |
| [S-198](S-198-the-loudness-of-the-rows-that-a-routes-loud-events-govern-be.md) | C-36 | The loudness of the rows that a Route's Loud events govern (BE) | be | S-197, S-140, S-121, S-169, S-155, S-044 |
| [S-199](S-199-new-alerts-remove-the-acknowledgement-and-start-chains-and-the-ack-timeout-over-be.md) | C-36 | New Alerts remove the acknowledgement, and start Escalation chains and the ack timeout over (BE) | be | S-198, S-169, S-155, S-173, S-174 |
| [S-200](S-200-the-loud-and-quiet-section-of-the-route-form-fe.md) | C-36 | The "Loud and Quiet" section of the Route form (FE) | fe | S-197, S-198, S-199, S-112, S-123, S-158 |
| [S-201](S-201-documentation-loud-and-quiet-per-route-and-new-alerts-that-remove-the-acknowledgement.md) | C-36 | Documentation: Loud and Quiet per Route, and new Alerts that remove the acknowledgement | docs | S-198, S-199, S-200 |
| [S-202](S-202-attach-and-detach-commands-with-their-permission-refusals-limit-state-and-audit-log-be.md) | C-37 | The Commands Attach and Detach with their Permission, refusals, limit, state and Audit log (BE) | be | S-198, S-169, S-168, S-174, S-173, S-197 |
| [S-203](S-203-attached-alert-groups-follow-their-main-alert-group-and-the-attachment-ends-on-a-resolve-be.md) | C-37 | Attached Alert Groups follow their main Alert Group, and the attachment ends on a resolve (BE) | be | S-202, S-199, S-173, S-169, S-063 |
| [S-204](S-204-chains-notification-rules-calls-the-ack-timeout-and-reminders-stop-on-attach-and-start-over-on-detach-be.md) | C-37 | Escalation chains, Notification rules, calls, the ack timeout and Reminders stop on Attach and start over on Detach (BE) | be | S-203, S-169, S-155, S-157, S-168 |
| [S-205](S-205-the-line-of-an-attached-alert-group-and-of-its-main-alert-group-in-every-messenger-and-bridge-be.md) | C-37 | The line of an attached Alert Group and of its main Alert Group in every messenger and bridge, with the Thread replies and the answers to presses (BE) | be | S-203, S-126, S-168, S-113, S-081, S-075, S-100, S-067 |
| [S-206](S-206-the-attached-filter-bulk-attach-the-alerts-of-attached-alert-groups-statistics-and-webhook-events-be.md) | C-37 | The attached filter and counts of the Alert Group list, bulk Attach, the Alerts of attached Alert Groups, statistics and webhook events (BE) | be | S-203, S-174, S-168, S-044 |
| [S-207](S-207-the-attach-dialog-the-notice-and-section-on-the-alert-group-page-and-the-mark-filter-and-bulk-attach-of-the-list-fe.md) | C-37 | The Attach dialog, the notice and the section on the Alert Group page, and the mark, filter and bulk Attach of the list (FE) | fe | S-202, S-203, S-204, S-206, S-159, S-158 |
| [S-208](S-208-documentation-attach-and-detach.md) | C-37 | Documentation: Attach and Detach | docs | S-204, S-205, S-206, S-207, S-201 |
| [S-209](S-209-spike-slack-fields-in-one-section-and-where-a-mattermost-attachment-draws-its-fields.md) | C-38 | Spike: how many fields Slack takes in one section, and where a Mattermost attachment draws its fields | docs | — |
| [S-210](S-210-the-message-layout-of-a-route-with-its-checks-migration-api-audit-log-and-the-rerender-be.md) | C-38 | The message layout of a Route with its checks, migration, API, Audit log and the re-render row (BE) | be | S-209, S-125, S-162, S-123 |
| [S-211](S-211-the-root-message-follows-the-message-layout-in-mattermost-slack-and-telegram-with-the-shortening-and-templates-be.md) | C-38 | The Root message follows the message layout in Mattermost, Slack and Telegram, with the shortening and the templates (BE) | be | S-210, S-209, S-125, S-168, S-113 |
| [S-212](S-212-the-order-of-the-blocks-in-the-bridge-layout-the-bridge-adapter-muster-bridge-check-and-the-fake-bridge-be.md) | C-38 | The order of the blocks in the Bridge layout, the Bridge adapter, muster bridge check and the fake bridge (BE) | be | S-211, S-126, S-171 |
| [S-213](S-213-the-order-of-the-message-blocks-in-the-go-bridge-library-be.md) | C-38 | The order of the message blocks in the Go bridge library (BE) — in `muster-io/muster-bridge-go` | be | S-212, S-127 |
| [S-214](S-214-the-order-of-the-message-blocks-in-the-python-bridge-library-be.md) | C-38 | The order of the message blocks in the Python bridge library (BE) — in `muster-io/muster-bridge-python` | be | S-212, S-128 |
| [S-215](S-215-the-preview-of-an-unsaved-message-layout-on-an-alert-group-that-arrived-with-the-payload-of-each-messenger-be.md) | C-38 | The preview of an unsaved message layout on an Alert Group that arrived, with the payload of each messenger (BE) | be | S-211, S-212, S-129, S-113, S-036 |
| [S-216](S-216-the-test-message-with-the-routes-layout-or-an-unsaved-one-built-from-any-alert-group-the-preview-shows-be.md) | C-38 | The test message with the Route's layout or an unsaved one, built from any Alert Group the preview shows (BE) | be | S-211, S-215, S-047, S-113 |
| [S-217](S-217-the-message-layout-editor-page-with-the-blocks-the-fields-and-the-templates-of-the-route-fe.md) | C-38 | The message layout editor page with the blocks, the fields and the Route's templates (FE) | fe | S-210, S-211, S-129, S-123 |
| [S-218](S-218-the-preview-on-real-alert-groups-in-the-message-layout-editor-with-the-four-messengers-and-the-test-message-fe.md) | C-38 | The preview on real Alert Groups in the message layout editor, with the four messengers and the test message (FE) | fe | S-217, S-215, S-216, S-048 |
| [S-219](S-219-documentation-the-message-layout-editor-the-blocks-that-always-show-templates-and-the-preview.md) | C-38 | Documentation: the message layout editor, the blocks that always show, templates and the preview | docs | S-217, S-218, S-213, S-214, S-130 |
| [S-220](S-220-the-theme-of-a-person-in-the-profile-with-its-migration-and-api-be.md) | C-39 | The theme of a person in the profile, with its migration and API (BE) | be | S-010, S-014 |
| [S-221](S-221-the-theme-switch-in-the-profile-and-the-user-menu-applied-before-the-first-paint-with-the-documentation-fe.md) | C-39 | The theme switch in the profile and the user menu, applied before the first paint, with the documentation (FE) | fe | S-220, S-014, S-072, S-010 |
| [S-222](S-222-the-pages-record-whether-they-were-made-outside-working-hours-with-the-migration-be.md) | C-40 | The Pages record whether they were made outside Working hours, with the migration (BE) | be | S-154, S-155, S-157, S-169, S-177 |
| [S-223](S-223-the-statistics-of-the-whole-selection-with-the-critical-split-and-of-noisy-alert-titles-be.md) | C-40 | The statistics of the whole selection with the critical split, and the statistics of noisy alert titles (BE) | be | S-029, S-174, S-206, S-111 |
| [S-224](S-224-the-statistics-of-the-people-paged-the-load-test-data-of-the-dashboard-and-the-measured-indexes-be.md) | C-40 | The statistics of the people Paged, the load-test data of the dashboard and the measured indexes (BE) | be | S-222, S-223, S-178, S-157, S-174, S-004 |
| [S-225](S-225-the-dashboard-section-of-the-home-page-with-the-period-the-filters-the-figures-and-the-chart-per-day-fe.md) | C-40 | The Dashboard section of the Home page with the period, the filters, the figures and the chart per day (FE) | fe | S-223, S-175, S-159, S-136, S-030 |
| [S-226](S-226-who-was-paged-and-noisy-alerts-on-the-dashboard-with-the-empty-states-fe.md) | C-40 | The tables "Who was Paged" and "Noisy alerts" of the dashboard with their empty states (FE) | fe | S-225, S-223, S-224, S-158, S-159 |
| [S-227](S-227-documentation-the-dashboard-on-the-home-page.md) | C-40 | Documentation: the dashboard on the Home page | docs | S-225, S-226, S-176 |
| [S-228](S-228-the-editor-of-whole-notification-rule-sequences-in-the-profile-with-its-checks-warnings-and-preview-fe.md) | C-41 | The editor of whole Notification rule sequences in the profile, with its checks, warnings and preview (FE) | fe | S-153, S-152, S-157, S-150, S-167 |
| [S-229](S-229-the-notification-rules-editor-on-a-users-page-and-on-organization-alert-handling-with-the-managed-by-warning-fe.md) | C-41 | The Notification rules editor on a User's page and on Organization → Alert handling, with the Managed by warning (FE) | fe | S-228, S-153, S-180, S-181, S-150 |
| [S-230](S-230-documentation-the-editor-of-notification-rule-sequences.md) | C-41 | Documentation: the editor of Notification rule sequences, custom rules and the Organization's default rules | docs | S-228, S-229, S-161 |
| [S-231](S-231-test-runs-of-a-destination-with-their-steps-the-people-a-step-would-mention-expiry-and-the-audit-log-for-mattermost-be.md) | C-42 | Test runs of a Destination with their steps, the people a step would mention, expiry and the Audit log, for Mattermost (BE) | be | S-047, S-061, S-114, S-125, S-140, S-049, S-068, S-198 |
| [S-232](S-232-the-steps-of-a-test-run-for-telegram-and-slack-destinations-be.md) | C-42 | The steps of a test run for Telegram and Slack Destinations (BE) | be | S-231, S-042, S-066, S-099, S-171, S-177 |
| [S-233](S-233-the-steps-of-a-test-run-for-bridge-and-outgoing-webhook-destinations-be.md) | C-42 | The steps of a test run for Bridge and outgoing webhook Destinations (BE) | be | S-231, S-074, S-081, S-126, S-044, S-045, S-177, S-212 |
| [S-234](S-234-the-presses-on-the-test-message-of-a-run-in-every-messenger-and-text-commands-in-the-thread-of-a-bridges-test-message-be.md) | C-42 | The presses on the Test message of a run in every messenger, and Text commands in the Thread of a Bridge's Test message (BE) | be | S-231, S-232, S-233, S-067, S-075, S-100, S-061 |
| [S-235](S-235-the-accelerated-run-of-an-escalation-chain-as-a-computation-over-schedules-notification-rules-and-account-links-be.md) | C-42 | The accelerated run of an Escalation chain as a computation over Schedules, Notification rules and Account links (BE) | be | S-169, S-155, S-157, S-156, S-131, S-152, S-154, S-047, S-164 |
| [S-236](S-236-the-chains-a-destination-would-page-in-and-playing-them-all-on-one-timeline-from-a-severity-level-and-a-time-be.md) | C-42 | The chains a Destination would Page in, and playing them all on one timeline from a Severity level and a time (BE) | be | S-235, S-231, S-111, S-112, S-121, S-154, S-162, S-123 |
| [S-237](S-237-the-step-by-step-test-on-the-destination-page-with-its-steps-requests-the-people-a-step-would-mention-and-the-presses-fe.md) | C-42 | The step-by-step test on the Destination page with its steps, requests, the people a step would mention and the presses (FE) | fe | S-231, S-232, S-233, S-234, S-048, S-090 |
| [S-238](S-238-the-escalation-chains-timeline-of-the-destination-page-with-the-chains-offered-the-start-and-play-fe.md) | C-42 | The Escalation chains timeline of the Destination page with the chains offered, the start and "Play" (FE) | fe | S-236, S-235, S-237, S-158, S-135 |
| [S-239](S-239-documentation-testing-a-destination.md) | C-42 | Documentation: testing a Destination — the Test message, the step-by-step test and the accelerated run of the chains | docs | S-237, S-238, S-234, S-233, S-232 |

The stories of "L2 0.2.x" specify the items of [L2 §1.5](../prd/L2.md#15-specified-later-in-l2) that the maintainer
settled after the core: C-34, C-36, C-37, C-38, C-39, C-40, C-41 and C-42, written in four batches. All of them start
after the tag of 0.2.0, which `depends_on` cannot name, and ship in its patch releases; their `depends_on` name the
stories of 0.2.0 whose code they build on. The contracts of C-38 to C-42 were written against the open pull requests
that add their capability files (#672 for C-38 and C-39, #673 for C-40 to C-42), before they merged, and cite those
files at their final paths. Each capability is cut at the contract boundaries of its behaviour, so that no story passes
about 30 to 40 hand-written files and each is checkable live when it merges: the backend in the order its parts depend
on one another, then the frontend, which follows the backend story it shows, then a page of documentation with its
documentation links, so that `docslinkcheck` finds the page; where a capability needs no API (C-41) it has no backend
story. A fact that a story's contract rests on and that only a messenger can settle goes first as a spike, as S-116
does for GitLab; and the pieces of a capability that belong to the bridge libraries are worked in their own
repositories, as S-127 and S-128 are.

S-190 to S-196 specify C-34, the first of them. The behaviour is cut at the contract boundaries of a window's life. S-190 is the object — the table, the Permissions, the limits, the API, the Audit
log and the timer rows of the start and the end, whose handlers decide nothing yet. S-191 is the decision: which Alert
Groups a window keeps back by the rules of the three objects, Urgent and the Internal alerts of an Integration, and the
events `maintenance_started` and `maintenance_ended` that record it. S-192 builds delivery on that state — nothing new
sent, the marks, Storms, events mode, and the publication at the end — and S-193 the chains, Notification rules, calls,
ack timeout and Reminders, which stop and start over; the order keeps each story checkable live, with the interim on
`edge` named in the Notes of S-191 to S-193. The frontend is two stories after the backend they show: S-194 the
sections of the Integration, Route and Team pages and the list, S-195 the Alert Group page, list and Timeline, which
follows S-192 for the delivery state. S-196 documents the capability and adds the documentation links of the web UI.

S-197 to S-201 specify C-36 and S-202 to S-208 specify C-37. C-36 is cut where the behaviour changes its meaning.
S-197 is the setting — two columns with the migration that gives every existing Route the loudness of L1, the profiles
and the Route API. S-198 reads it: which of the rows of the
lifecycle event tables a Route's Loud events govern, decided when an entry is recorded and read by delivery, Storms and
events-mode webhooks from the entry; S-199 adds the removal of an acknowledgement by new Alerts with the starting over of
chains and the ack timeout, which needs S-198's list of the rows about the Owner. S-200 is the section of the Route form
and S-201 the page on Loud and Quiet, which follows the form so that its screenshot shows the finished section. C-37 is
cut at the lives of an attachment. S-202 is the state with its Commands, Permission, ordered refusals, limit and Audit
log, and S-203 the following of the main Alert Group, the end of an attachment on a resolve, the Quiet rule and the
refusal of other Commands on an attached Alert Group, which builds on S-199 for the removal that it follows. S-204 takes
the chains, Notification rules, calls and timers that stop on Attach and start over on Detach, S-205 the line, the Thread
replies, the bridges' `layout` and the answers to presses in every messenger, and S-206 the list filter, the bulk Attach,
the Alerts of attached Alert Groups, the statistics and the webhook events. S-207 is the frontend after the backend it
shows and S-208 the documentation, with the documentation links of the dialog and the notice, so that `docslinkcheck`
finds the page. C-37 depends on C-36 through S-198 and S-199.

S-209 to S-219 specify C-38 and S-220 and S-221 specify C-39. C-38 is cut at the contract boundaries of the layout's life.
S-209 is the spike on the two messenger facts that P-94 and the Mattermost hint rely on, which the capability's split puts first, as
S-116 does for GitLab. S-210 is the setting — the column, the Route API with Muster's default read back resolved, the
checks, the Audit log and the re-render row — and S-211 the renderer: the fields, the order, the shortening and the
templates in Mattermost, Slack and Telegram, the two together being too big for one pull request. S-212 adds `order` to
the Bridge `layout`, the Bridge adapter, the check and the fake bridge, and S-213 and S-214 the types in the Go and the
Python library, worked in their own repositories as S-127 and S-128 are. S-215 is the preview of an unsaved layout on
any Alert Group with each messenger's payload, which exports the adapters' payload builders so that it cannot drift from
delivery, and S-216 the Destination test with the Route's or an unsaved layout. The frontend is two stories after the
backend they show: S-217 the editor's blocks, fields and templates, which already saves and resets, S-218 the preview
with the four tabs and the test message, which follows S-215 and S-216; S-219 documents the capability, extends the
guide for bridge authors with `order` and adds the editor's documentation link so that `docslinkcheck` finds its page.
C-39 is a small backend part, S-220, with the migration and the `theme` of `Me`, and S-221, the frontend with its
documentation: the switch, the script of the SPA's own origin that applies the theme before the first paint under the
Content Security Policy, the dark variants on every page and screenshots that stay light.

S-222 to S-227 specify C-40, S-228 to S-230 C-41 and S-231 to S-239 C-42. C-40 is cut at what each part of the
dashboard reads. S-222 is the recording on each Page of whether it was made outside Working hours, which must exist
before the first Page that the dashboard counts and so comes first and alone; S-223 is the statistics of the whole
selection with the critical split and of the noisy titles, which extend the reads of the statistics page; S-224 the
people Paged, with the load-test data of P-96 and the measurement of all four reads against their budgets, which needs
every read to exist and so follows them. The frontend is two stories after the backend it shows: S-225 the section
with the period, the filters in the URL, the figures and the chart, which sets up the one-request-per-part behaviour,
and S-226 the two tables that mount into it; S-227 documents the dashboard in the statistics section and adds its
documentation link. C-41 adds no API — the API takes any sequence from 0.2.0 — so it is frontend and documentation
only, and its frontend does not depend on a backend story of its own: S-228 is the editor in the profile with its
checks, warnings and preview, the part the other places reuse, S-229 mounts it on a User's page and on Organization →
Alert handling with the `managed_by` warning of S-181 and corrects the one description in the spec that says the
Organization's rules change only through the API, and S-230 documents it and adds the revision marker that C-31.FR-5
asks for. C-42 is two features that share a page and nothing else. The step-by-step test is cut by what each Destination
type sends: S-231 is the run — its table, four operations, the eight steps' order, expiry, "Would mention:" and the
Audit log — carried end to end for Mattermost, so that the shape of a stepper is fixed once; S-232 adds Telegram and
Slack and S-233 a Bridge and an outgoing webhook in both modes, and S-234 the presses on the Test message of a run in
every messenger and the Text commands of a Bridge, with its own table. The accelerated run is a computation: S-235 is
the computation of one chain over Schedules, Notification rules and Account links, with the decisions of a step shared
with the running engine, proved through the operation for named chains, and S-236 the offers of the chains a Destination
would Page in and the one timeline of all of them. S-237 is the step-by-step panel, which follows the four backend
stories that make its steps, and S-238 the timeline of the chains, after S-236; S-239 is the new page on testing a
Destination, linked from the messengers' and the webhooks' pages.

Migrations are ten, and an eleventh if the measurements of S-224 call for indexes; each takes the number the
orchestrator assigns at dispatch, with each replaced CHECK re-versioned in the order the stories merge: C-34's three, in
S-190 to S-192; S-197's, with no CHECK to re-version; S-202's, which replaces the event CHECKs of `timeline_entries`,
`webhook_events` and `thread_replies` as C-34's do; S-210's `routes.message_layout`, which reuses the kind
`route_message_settings` of `open_group_updates` and so replaces no CHECK; S-220's `users.theme`; S-222's nullable
`pages.outside_working_hours`; and C-42's two, `destination_test_runs` in S-231 and `destination_test_presses` in S-234.
Each story adds its spec parts, settings and log events in its own pull request, as every story does.

The backend stories of a stage follow the dependencies between capabilities in the
[capability map](../prd/L1.md#13-capability-map): snapshot processing before routing, routing before the Alert Group
lifecycle, the lifecycle before commands, commands before the delivery engine, the delivery engine and messages before
the messengers, the messengers before timers and Account links. Every frontend story depends on the application shell
(S-014, through S-015 or S-017) and on the backend story of its capability.
