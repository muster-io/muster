---
id: S-004
title: "`muster dev` with fake servers, end-to-end and load-test harness"
capability: C-01
kind: infra
layer: L1
depends_on: [S-002]
covers: [C-01.FR-7, C-01.FR-13, C-01.FR-15, C-01.AC-4]
files_touched:
  - internal/cli/cli.go
  - internal/cli/dev.go
  - internal/cli/dev_test.go
  - internal/devmode/devmode.go
  - internal/devmode/devmode_test.go
  - internal/fakes/fakeserver/fakeserver.go
  - internal/fakes/fakeserver/fakeserver_test.go
  - internal/fakes/fakealertmanager/fakealertmanager.go
  - internal/fakes/fakealertmanager/fakealertmanager_test.go
  - internal/fakes/fakemattermost/fakemattermost.go
  - internal/fakes/faketelegram/faketelegram.go
  - deploy/dev/docker-compose.yml
  - test/e2e/harness.go
  - test/e2e/smoke_test.go
  - test/load/main.go
  - Makefile
  - .testcoverage.yml
  - .golangci.yml
  - .github/workflows/ci.yml
  - .github/workflows/nightly.yml
  - CONTRIBUTING.md
acceptance:
  - "[C-01.FR-13] `muster dev` starts the fake Alertmanager, Mattermost and Telegram servers on 127.0.0.1:19093, 127.0.0.1:18065 and 127.0.0.1:18081, prints their addresses and runs until interrupted."
  - "[C-01.FR-13] Every fake server records each request and answers scripted faults (status, Retry-After, delay) set through its `/_fake/` control endpoints."
  - "[C-01.FR-13, C-01.AC-4] `make dev` starts the development PostgreSQL with Docker Compose and runs `muster dev`, so only Docker is needed; `muster dev` starts on a machine whose only network is loopback (and, from S-006 on, the development database)."
  - "[C-01.FR-13] `muster dev <subcommand>` runs another subcommand — `muster dev doctor`, `muster dev admin reset-password …` — with the development defaults, the development key included; a `MUSTER_*` variable that is set replaces its default, so a `MUSTER_SECRET_KEYS` given to `muster dev` is the whole Keyring."
  - "[C-01.FR-7] `muster dev --replica` starts no fake server and, from S-006 on, runs Muster as an additional replica on its own ports (`:9080`, `:9081`, `:9082` unless `MUSTER_LISTEN_*` say otherwise) against the same database, accepting the development key; `E2E_REPLICAS=2` starts `muster dev` and `muster dev --replica` side by side."
  - "[C-01.FR-13] In the end-to-end harness's mode with the fakes in the test process, the fake servers run in the test process at their fixed addresses and Muster runs as `muster dev --replica` processes (from S-006 on) that a test can stop and start again while the fakes keep their recorded requests and scripted faults."
  - "[C-01.FR-7] The pull-request tier runs `make e2e`; the nightly workflow runs the load test and the end-to-end suite with two replicas, each running what the merged capabilities provide."
  - "[C-01.FR-15] The nightly load-test job runs the load test against `muster dev` with the fake servers, then starts the compose example, runs the load test against it and reports the peak memory working set of the Muster and PostgreSQL containers."
verify: "make ci e2e"
operator_attention: false
issue: 4
---

# S-004. `muster dev` with fake servers, end-to-end and load-test harness

## Scope

**IN**

- The subcommand `muster dev` and its development defaults; `muster dev --replica` for additional replicas;
  `muster dev <subcommand>` for the CLI against the development database.
- A shared fake-server harness and the first fake servers of Alertmanager, Mattermost and Telegram, with only the
  behaviour that the harness itself needs.
- A development PostgreSQL (`make dev-db`) and `make dev`.
- The end-to-end harness in Go (`make e2e`) with its mode for the fakes in the test process, its CI job and the
  nightly two-replica run.
- The load-test harness of NFR-1 and NFR-2 and the footprint report of NFR-3, run nightly.

**OUT**

- Running Muster inside `muster dev` (S-006 adds the runtime; each later capability adds its demo configuration).
- The fake OIDC server and the fake proxies (S-013, S-009); the Alertmanager, Mattermost and Telegram protocols beyond
  the skeletons (C-05/C-06, C-13, C-14 extend their fakes).
- Playwright and the browser end-to-end tests (S-014, the first frontend story).
- The full load profile — 10,000 Alerts in 1,000 open Alert Groups delivered to the fake Mattermost server — and the
  thresholds of NFR-1, NFR-2 and P-44 that make the load test fail (S-061).

## Contracts

- **`muster dev`** (C-01.FR-13): starts the fake servers on fixed loopback addresses and prints them:

  | Fake | Address | Skeleton behaviour in this story |
  |---|---|---|
  | Alertmanager | `127.0.0.1:19093` | sends an Alertmanager webhook body to a target URL on request, once or at a rate (load mode) |
  | Mattermost | `127.0.0.1:18065` | REST API v4: `GET /api/v4/users/me` answers a bot user; every other call is recorded and answered `501` |
  | Telegram Bot API | `127.0.0.1:18081` | `getMe` answers a bot; every other method is recorded and answered `{"ok": false, "error_code": 501}` |

  The development defaults exist only in this mode and each can be overridden by its `MUSTER_*` variable:
  `MUSTER_PUBLIC_URL=http://localhost:8080`,
  `MUSTER_DATABASE_URL=postgres://muster:muster@127.0.0.1:55432/muster?sslmode=disable`, the fixed development master
  key `MUSTER_SECRET_KEYS=bXVzdGVyLWRldi1vbmx5LWtleS1ub3QtYS1zZWNyZXQ=` (the base64 of
  `muster-dev-only-key-not-a-secret`, a published constant of `internal/devmode`; the server command refuses it outside
  development mode, S-007), and the bootstrap Admin `admin@example.org` with the password `muster-dev-password`
  (S-010). A `MUSTER_*` variable that is set replaces its default and never adds to it — the database default gives way
  to `MUSTER_DATABASE_URL` or to any `MUSTER_DATABASE_*` field, and the key and the Admin password to their `_FILE`
  variables too: a `MUSTER_SECRET_KEYS` given to `muster dev` is the whole Keyring, so it must list the development key
  for as long as the database's key canary needs it (S-055 sets both keys explicitly). The demo configuration —
  Integrations, Routes and Destinations wired to the fakes — is added by the capabilities that own those resources.
- **CLI against the development database** (C-01.FR-13): `muster dev <subcommand>` runs another subcommand of the
  binary — `muster dev doctor`, `muster dev admin reset-password --actor …`, `muster dev ingest replay …`,
  `muster dev secrets rotate-key …` — with the same development defaults and the same replacement rule, the development
  key included; it starts no fake server and no runtime, and the subcommand's own flags and refusals are unchanged.
  Live checks run the CLI this way: a bare `muster <subcommand>` has no development defaults.
- **Additional replicas** (C-01.FR-7, NFR-4): `muster dev --replica` is the development mode without the fake servers,
  for a second replica on the same development database. It keeps every development default — the development master
  key, which this mode accepts too (S-007), the bootstrap Admin and the demo configuration, whose start-up steps are
  idempotent — but starts no fake server and listens on its own ports: `MUSTER_LISTEN_APP=:9080`,
  `MUSTER_LISTEN_INGEST=:9081` and `MUSTER_LISTEN_INTERNAL=:9082` unless set otherwise. Its Connections reach the fakes
  of the first `muster dev` at their fixed addresses. It prints `muster dev: additional replica, no fake servers` and,
  until S-006 adds the runtime, only waits. The development clock of S-020 lives in the database, so every replica of
  one database sees the same time.
- **Fake-server harness** (`internal/fakes/fakeserver`): an HTTP server that records every request and answers from
  per-path scripts; control endpoints under `/_fake/`: `GET /_fake/requests` (recorded requests as JSON),
  `DELETE /_fake/requests`, `POST /_fake/faults` (`{"path", "status", "retry_after_seconds", "delay_ms", "body"}`),
  `DELETE /_fake/faults`. The same servers run in Go tests (in-process) and in `muster dev`.
- **Development database**: `deploy/dev/docker-compose.yml` runs PostgreSQL 17 on `127.0.0.1:55432` with user,
  password and database `muster`; `make dev-db` starts it alone, and `make dev` starts it the same way, then builds and
  runs `bin/muster dev`, so a developer needs only Docker (C-01.FR-13).
- **End-to-end harness** (`test/e2e`, build tag `e2e`): builds the binary and runs Muster in development mode in one of
  two modes. By default it starts `muster dev` in a child process, waits until the fakes (and, from S-006,
  `/health/ready`) answer, and gives tests the addresses. `make e2e` runs it; the pull-request tier of `ci.yml` gains an
  `e2e` job. `E2E_REPLICAS=2` (from S-006 on) starts `muster dev` and then `muster dev --replica` on the same database
  and gives tests the addresses of both; a test sends webhooks, API calls and clock advances to either replica as it
  chooses. The fake servers live in the first process, so a test that stops a replica stops the second one. A test that
  stops and restarts Muster uses the second mode, **fakes in the test process** (`harness.FakesInProcess`): the harness
  starts the fake servers in the test process at their fixed addresses and runs each replica as a `muster dev --replica`
  child process (from S-006 on) on a fresh database, with the listen addresses and the extra environment the test gives
  — its own `MUSTER_SECRET_KEYS`, a database URL through a proxy; `Stop`, `Start` and `Restart` act on one replica while
  the fakes keep their recorded requests and scripted faults. Both modes run Muster as `muster dev`, as C-01.FR-13 asks.
- **Load test** (`test/load`, `make load-test`): runs the fake Alertmanager in its own process and drives it to send 50
  webhooks per second for one minute to `MUSTER_INGEST_URL` (NFR-1), and reports accepted, rejected and failed requests
  with latency percentiles; until ingestion exists (S-018) it reports "skipped: ingestion is not available". It
  prepares its own data, so it needs nothing but the target's URLs and its bootstrap Admin: it signs in with
  `MUSTER_LOAD_ADMIN_EMAIL` and `MUSTER_LOAD_ADMIN_PASSWORD` — by default the bootstrap Admin of the development
  defaults — creates a Personal access token and an Integration for the run and registers the Integration's token with
  its fake. S-061 gives it the full profile of NFR-1, the `-destination=none` switch and the checks that make it fail
  when NFR-1, NFR-2 or P-44 is not met.
- **Nightly jobs** (C-01.FR-7, C-01.FR-15): `load-test` runs the load test twice — against `muster dev` with the fake
  servers (the figures of NFR-1 and NFR-2), then against the compose example started with the build under test and a
  bootstrap Admin that the job writes into its `.env` and passes to the load test, with `-destination=none` (S-061),
  recording the peak memory working set of both containers (`docker stats`, NFR-3) in the job summary;
  `e2e-two-replicas` runs `make e2e E2E_REPLICAS=2`.

## Steps

1. Write the fake-server harness. Check: its tests show recording, scripted statuses, `Retry-After` and delays.
2. Write the three skeleton fakes, `muster dev`, `--replica` and `muster dev <subcommand>`. Check: `./bin/muster dev`
   prints the three addresses, `./bin/muster dev --replica` starts none, `./bin/muster dev version` prints the version,
   and the curl checks of Verification pass.
3. Add `deploy/dev/docker-compose.yml`, `make dev-db` and `make dev`. Check: `make dev-db` leaves PostgreSQL accepting
   connections on 127.0.0.1:55432.
4. Add the end-to-end harness with both modes, `make e2e` and the CI job. Check: `make e2e` passes locally and in a
   pull request, and a harness test in the mode with the fakes in the test process sees a fake's recorded requests
   survive a restart of the replica.
5. Add the load test and the nightly jobs. Check: a manual run of the nightly workflow shows the load-test summary with
   both containers' peak memory and a green two-replica job.

## Verification

```sh
make build
./bin/muster dev &
# muster dev: fake Alertmanager http://127.0.0.1:19093
# muster dev: fake Mattermost http://127.0.0.1:18065
# muster dev: fake Telegram http://127.0.0.1:18081

curl -s http://127.0.0.1:18081/bot123:abc/getMe | jq -c '{ok, bot: .result.is_bot}'
# {"ok":true,"bot":true}
curl -s http://127.0.0.1:18081/_fake/requests | jq -r '.[0].path'
# /bot123:abc/getMe

curl -s -X POST http://127.0.0.1:18065/_fake/faults \
  -d '{"path":"/api/v4/users/me","status":429,"retry_after_seconds":3}'
curl -si http://127.0.0.1:18065/api/v4/users/me | grep -E '^(HTTP|Retry-After)'
# HTTP/1.1 429 Too Many Requests
# Retry-After: 3
kill %1

# C-01.FR-13: another subcommand with the development defaults
./bin/muster dev version
# muster 0.0.0-dev (commit <12 hex characters>)

# C-01.FR-7: an additional replica starts no fake server
./bin/muster dev --replica &
# muster dev: additional replica, no fake servers
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18065/_fake/requests   # 000
kill %1

# C-01.FR-13: make dev needs only Docker
make dev &                                     # starts the development PostgreSQL, then muster dev
docker compose -f deploy/dev/docker-compose.yml ps --format '{{.Service}} {{.State}}'
# postgres running
kill %1

# C-01.AC-4: no network besides loopback
CGO_ENABLED=0 GOOS=linux go build -o /tmp/muster-linux ./cmd/muster
docker run --rm --network none -v /tmp/muster-linux:/muster gcr.io/distroless/static-debian12:nonroot /muster dev &
# muster dev: fake Alertmanager http://127.0.0.1:19093   (and the two other lines; the process keeps running)

make e2e
# ok  github.com/muster-io/muster/test/e2e
```

## Open questions

None.

## Notes

- Suggested commit: `test: add muster dev with fake servers and the end-to-end and load-test harness`.
- The fake ports and the ports of `muster dev --replica` are part of the contract: later stories and their live checks
  rely on them.
- The fakes are test infrastructure, outside the outbound package and exempt from lint 4 by path.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-01.FR-7 | partial | nightly load test and two-replica run; tiers are S-001, mutation report S-002, the full load profile S-061 |
| C-01.FR-13 | partial | the mode and the fakes; Muster in the mode is S-006, each capability extends its fake and the demo configuration |
| C-01.FR-15 | partial | the nightly measurement; the capped PostgreSQL is S-003 |
| C-01.AC-4 | full | re-checked with the database by S-006 |
