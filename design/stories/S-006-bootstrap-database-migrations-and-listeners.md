---
id: S-006
title: Bootstrap settings, database connections, migrations, listeners and health (BE)
capability: C-02
kind: be
layer: L1
depends_on: [S-003, S-004, S-005]
covers: [C-02.FR-1, C-02.FR-2, C-02.FR-3, C-02.FR-4, C-02.FR-5, C-02.FR-6, C-02.FR-14, C-02.FR-15, C-02.FR-16, C-02.FR-19, C-02.AC-2, C-02.AC-5, C-02.AC-7, C-02.AC-8, C-01.FR-12, C-01.FR-13, C-01.AC-2]
files_touched:
  - internal/config/config.go
  - internal/config/config_test.go
  - internal/db/db.go
  - internal/db/checks.go
  - internal/db/migrate.go
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - internal/db/dbtest/dbtest.go
  - internal/db/migrations/0001_init.up.sql
  - internal/db/migrations/0001_init.down.sql
  - internal/clock/clock.go
  - internal/clock/clock_test.go
  - internal/server/server.go
  - internal/server/health.go
  - internal/server/server_test.go
  - internal/runtime/runtime.go
  - internal/runtime/runtime_test.go
  - internal/cli/cli.go
  - internal/cli/cli_test.go
  - internal/cli/serve.go
  - internal/cli/migrate.go
  - internal/cli/dev.go
  - internal/cli/dev_test.go
  - internal/devmode/devmode.go
  - internal/devmode/devmode_test.go
  - internal/logging/events.go
  - internal/logging/logger.go
  - internal/logging/logger_test.go
  - internal/metrics/catalogue.go
  - internal/metrics/metrics.go
  - internal/metrics/metrics_test.go
  - internal/tools/refgen/main.go
  - internal/archlint/secretleak.go
  - test/e2e/harness.go
  - deploy/helm/muster/templates/deployment.yaml
  - deploy/helm/muster/ci/all-options-values.yaml
  - deploy/compose/docker-compose.yml
  - Makefile
  - .github/workflows/ci.yml
  - .golangci.yml
  - go.mod
  - AGENTS.md
  - design/architecture.md
  - design/db/schema.md
  - design/prd/l1/defaults.md
  - design/prd/L1.md
acceptance:
  - "[C-02.FR-1, C-02.AC-7] With only the `MUSTER_DATABASE_*` fields set and the password from `MUSTER_DATABASE_PASSWORD_FILE`, Muster connects and becomes ready; with the URL and the fields both set it uses the URL and logs `database_settings_conflict` at WARN naming both; with `MUSTER_DATABASE_PORT=abc` it exits non-zero with an error naming `MUSTER_DATABASE_PORT`."
  - "[C-02.FR-1, C-02.FR-2] Outside development mode a missing `MUSTER_PUBLIC_URL` or `MUSTER_SECRET_KEYS` stops startup naming the variable, while `muster dev` and `muster dev <subcommand>` take both from the development defaults of S-004; `MUSTER_INGEST_URL` defaults to `MUSTER_PUBLIC_URL`; `HTTP_PROXY`, `HTTPS_PROXY` and `NO_PROXY` change nothing."
  - "[C-02.FR-5, C-02.AC-2] Against PostgreSQL 13 startup fails naming the version and the minimum of 14; with the session connection going through a transaction-pooling PgBouncer it fails naming `MUSTER_DATABASE_SESSION_URL`; with `MUSTER_DATABASE_SESSION_URL` pointing at PostgreSQL directly it becomes ready."
  - "[C-02.FR-6, C-02.FR-15, C-02.AC-5] `muster migrate` applies the embedded migrations under the migration advisory lock and logs `migrations_applied`; on a schema newer than the binary knows, `muster migrate` and the server both refuse, naming both versions; `up → down → up` of every migration passes on PostgreSQL 14 and 17 in CI."
  - "[C-02.FR-3, C-02.FR-4, C-02.FR-19, C-01.FR-12] The internal listener serves `/health/live` (`getHealthLive`: 200, no checks), `/health/ready` (`getHealthReady`: 200 while the database answers, 503 otherwise) and `/metrics` (`getMetrics`) with `muster_build_info` and the database pool metrics; the app listener serves the SPA."
  - "[C-02.FR-3, C-02.AC-8] With `MUSTER_LISTEN_INGEST` equal to `MUSTER_LISTEN_APP`, one port serves both: ingest paths reach the ingest handler and every other path the app handler (integration test with stub handlers; the live check with the real ingestion route is part of S-018)."
  - "[C-02.FR-14] At startup the TLS state of each database connection is logged as `database_connection_security`, at WARN when the connection is unencrypted."
  - "[C-02.FR-16] On SIGTERM Muster logs `shutdown_requested` at WARN, readiness answers 503, and the process exits 0 within `process.shutdown_grace`."
  - "[C-02.FR-4, C-02.FR-6, C-01.AC-2] The chart runs `muster migrate` in an init container and defines startup, readiness and liveness probes, and a chart installed with an existing Secret against a reachable PostgreSQL becomes ready; the compose example sets `MUSTER_MIGRATE_ON_START=true` and one shared app and ingest port."
  - "[C-01.FR-13] `muster dev` runs Muster in the same process against the development database, migrating it on start."
verify: "make ci test-integration"
operator_attention: false
issue: 6
---

# S-006. Bootstrap settings, database connections, migrations, listeners and health (BE)

## Scope

**IN**

- Parsing and validating the bootstrap environment of C-02.FR-1 and FR-2.
- The main database pool and the session connection, with the startup checks of C-02.FR-5 and the TLS report.
- The designed schema as the first migration; `muster migrate`, `MUSTER_MIGRATE_ON_START`, the migration lock and the
  refusal of a newer schema.
- The three listeners, merged when ingest and app share an address; health endpoints and `/metrics`.
- The default server command, the start-up order and graceful shutdown on SIGTERM.
- The two clocks of `internal/clock`, business and real, that code uses instead of SQL `now()`.
- The chart's init container and probes, the compose settings, and Muster inside `muster dev`.

**OUT**

- The Keyring, the key canary and the Organization defaults (S-007).
- The Leader, partitions, the downtime record, clock skew and `muster doctor` (S-008).
- The API, security headers on the SPA and the bootstrap Admin (S-010).
- The ingestion route (S-018).

## Contracts

- **Environment** (C-02.FR-1, FR-2, ADR-0010): every `MUSTER_*` variable of the Environment table of
  [defaults.md](../prd/l1/defaults.md#environment), parsed with caarlos0/env and validated with go-playground/validator.
  Secrets also come from `*_FILE` variables (`MUSTER_DATABASE_PASSWORD_FILE`, `MUSTER_SECRET_KEYS_FILE`,
  `MUSTER_BOOTSTRAP_ADMIN_PASSWORD_FILE`). An invalid or missing required value stops startup with an error that names
  the variable; nothing falls back silently. When `MUSTER_DATABASE_URL` and the fields are both set, the URL wins and
  `database_settings_conflict` is logged at WARN. `MUSTER_PUBLIC_URL` is an absolute `http` or `https` URL;
  `MUSTER_INGEST_URL` defaults to it. A database URL names its host, user and database; the port (5432) and `sslmode`
  (`prefer`) it does not name are written into it, and the password, connect timeout and application name come only
  from it, so that the `PG*` variables and the password file pgx would read change nothing. A
  `MUSTER_DATABASE_PASSWORD_FILE` that the URL makes ignored is not read. The bootstrap Admin variables are parsed here
  and used by S-010. `HTTP_PROXY`,
  `HTTPS_PROXY` and `NO_PROXY` are never read. In development mode — `muster dev`, `muster dev --replica` and
  `muster dev <subcommand>` (S-004) — the development defaults fill every variable that is not set; a variable that is
  set replaces its default.
- **Connections**: the main pool (pgx) for every query; the session connection — `MUSTER_DATABASE_SESSION_URL`, or
  `MUSTER_DATABASE_SESSION_HOST` and `_PORT` with the other fields inherited, or else the main settings — for the
  migration lock here, the Leader lock (S-008) and `LISTEN` (S-012).
- **Startup checks** (C-02.FR-5): `server_version_num` of at least 140000, otherwise "PostgreSQL 13.x is not
  supported: Muster needs PostgreSQL 14 or newer". On the session connection, take and release a session advisory lock
  and complete a `LISTEN`/`NOTIFY` round trip within 5 s, the `NOTIFY` sent over the main pool; a failure stops startup
  with an error that says the connection does not keep session state, probably because of a transaction pooler, and
  names `MUSTER_DATABASE_SESSION_URL`, `MUSTER_DATABASE_SESSION_HOST` and `MUSTER_DATABASE_SESSION_PORT`.
- **TLS report** (C-02.FR-14, P-04): the default `MUSTER_DATABASE_SSLMODE` is `prefer`; at startup each connection's
  state from `pg_stat_ssl` is logged as `database_connection_security` (`connection`, `sslmode`, `encrypted`), INFO when
  encrypted and WARN when not. `muster doctor` reports the same in S-008.
- **Migrations** (C-02.FR-6, ADR-0006): `design/db/migrations/0001_init.{up,down}.sql` move unchanged to
  `internal/db/migrations/` (embedded; also the schema `sqlc` reads from S-007 on) and the links in
  `design/db/schema.md` follow. golang-migrate runs them over the session connection that holds Muster's migration
  advisory lock, through a small golang-migrate database driver of Muster's own over that pgx connection: golang-migrate's
  `pgx/v5` driver imports `jackc/pgerrcode`, which is also under the PostgreSQL License, a licence ADR-0001 does not
  list. It keeps golang-migrate's `schema_migrations` table. Migrations run from `muster migrate` (no `--actor`) or at
  startup when `MUSTER_MIGRATE_ON_START` is on. Applied versions are logged as `migrations_applied` (`from`, `to`), and
  a run with nothing to apply as `migrations_current` (`version`). A database whose version is newer than the newest
  embedded migration makes both `muster migrate` and the server stop with `schema_too_new` naming both versions; a
  dirty version stops them too (`schema_dirty`), and so does, for a server that does not migrate on start, a version
  older than the binary needs (`schema_too_old`).
- **Listeners** (C-02.FR-3, P-03): `MUSTER_LISTEN_APP` (`:8080`), `MUSTER_LISTEN_INGEST` (`:8081`),
  `MUSTER_LISTEN_INTERNAL` (`:8082`). When app and ingest have the same address, one server routes the ingest paths
  (`/api/v1/ingest`, `/api/v1/heartbeat`, `/api/v1/callbacks/`) to the ingest handler and every other path to the app
  handler. In this story the app handler serves the embedded SPA and the ingest handler has no routes yet.
- **Health and metrics** (C-02.FR-4, FR-19; operations `getHealthLive`, `getHealthReady`, `getMetrics`): on the
  internal listener, `GET /health/live` answers `200 ok` without checks; `GET /health/ready` answers `200 ok` when
  `SELECT 1` on the main pool returns within 1 s, `503 database unavailable` otherwise and `503 shutting down` once the
  shutdown has begun; `GET /metrics` serves the registry of S-005. New metrics: the database pool
  gauges and counters `muster_db_pool_connections{state}` (`acquired`, `idle`, `constructing`),
  `muster_db_pool_max_connections`, `muster_db_pool_acquires_total` and `muster_db_pool_acquire_wait_seconds_total`,
  read from the pool at every scrape (the registry gains `Counter.Func` for a count a library keeps).
- **Server command and start-up order**: `muster` without a subcommand runs the server: settings → logger
  (`MUSTER_LOG_LEVEL`) → connections and checks → migrations when enabled → schema version check → (S-007: Keyring,
  canary, Organization defaults) → listeners → ready. `process_started` is logged with version and commit.
- **Shutdown** (C-02.FR-16, P-02): on SIGTERM, `shutdown_requested` (WARN), readiness turns 503, workers registered by
  later stories stop claiming and finish or release their rows, HTTP servers drain — app and ingest first, the internal
  listener last, so that readiness keeps answering 503 and metrics stay scrapable meanwhile — and everything ends within
  `process.shutdown_grace` (20 s), closing what has not drained (`shutdown_grace_exceeded`, WARN); `process_stopped`
  (INFO); exit status 0. A signal during startup, such as while waiting for the migration lock, stops the server the
  same way with status 0; `muster migrate` stops with status 1. After the first signal, a second one kills the process.
- **Other log events**: `database_settings_conflict` (WARN: `used`, `ignored`), `database_connection_security` (INFO or
  WARN: `connection`, `sslmode`, `encrypted`; the logger gains `LogAt` for an event declared at more than one level, and
  the reference page lists both), `listeners_started` (INFO: `app`, `ingest`, `internal`), `listener_failed` (ERROR:
  `listener`, `error`) and `startup_failed` (ERROR: `error`). Errors before the logger exists, such as an invalid
  variable, go to stderr only.
- **Clocks** (`internal/clock`): two clocks, each with a manual implementation that drives tests. The **business clock**
  is the time of the domain — domain timestamps, timers, windows, retention, sessions, the alive mark and downtime; in
  development mode it is real time plus the development offset of S-020. The **real clock** is always the system time,
  for what must agree with the world outside the database or with other processes — TOTP steps, ID-token `exp` and
  `iat`, outgoing webhook signatures and their timestamps, row leases (`lease_until`) and the Leader lease, the clock
  skew check and replica records. Each consumer takes the clock it needs by injection; S-020 lists which consumer uses
  which. No query calls SQL `now()` (ADR-0006): it is given the time of its consumer's clock.
- **Chart** (C-02.FR-4, FR-6): an init container runs `muster migrate` with the environment of the main container;
  probes on the internal port — startup (`/health/ready`, up to 5 minutes), readiness (`/health/ready`), liveness
  (`/health/live`), each with a 2 s timeout above the 1 s of the readiness check; `terminationGracePeriodSeconds: 30`.
  The chart needs no new values. `ci/all-options-values.yaml` sets `MUSTER_LOG_LEVEL=warn`, since `debug` is not a
  level of the log event registry and is now refused.
- **Compose**: `MUSTER_MIGRATE_ON_START=true`; `MUSTER_LISTEN_APP` and `MUSTER_LISTEN_INGEST` both `:8080`; the image
  can be overridden with `MUSTER_IMAGE` for local builds.
- **Development mode** (C-01.FR-13): `muster dev` starts the runtime in the same process with the development defaults
  of S-004 and migrations on start, after the fake servers; `muster dev --replica` runs it as an additional replica;
  `muster dev migrate` runs the migrations with the same defaults; the end-to-end harness waits for `/health/ready` and
  gives each run a fresh database on the development PostgreSQL, which `make e2e` starts (`make dev-db`).
- **Secret leaks** (lint 5): a probe gives secrets to the bootstrap settings and to refused database URLs and runs the
  server and `muster migrate` against an unreachable database; neither the log nor the errors may carry them.
- **Integration tests**: `make test-integration` (build tag `integration`) runs against PostgreSQL 14 and 17 started by
  testcontainers with pinned image digests (`internal/db/dbtest`); CI runs it in the pull-request tier.

## Steps

1. Write `internal/config` with every variable and the `*_FILE` rule. Check: table tests cover precedence, the conflict
   warning, missing required values and invalid values, each error naming its variable.
2. Write the connections and startup checks. Check: integration tests against PostgreSQL 14 and 17 pass; a stub that
   refuses advisory locks produces the pooler error.
3. Move the migration, write the runner and `muster migrate`. Check: `up → down → up` passes on both versions; a forced
   newer version is refused with both numbers.
4. Write the listeners, health, `/metrics`, the server command, the start-up order, shutdown and the two clocks. Check:
   the server tests cover merged listeners and readiness without a database; SIGTERM exits 0 within the grace period;
   the clock tests show the business clock following an offset while the real clock does not.
5. Update the chart, the compose example, `muster dev` and the end-to-end harness. Check: `make helm-check` and
   `make compose-check` pass and `make e2e` starts Muster.
6. Confirm or change P-02, P-03 and P-04. Check: defaults.md and the open questions of L1.md say so.

## Verification

```sh
make dev-db && make build
export MUSTER_DATABASE_URL='postgres://muster:muster@127.0.0.1:55432/muster?sslmode=disable' \
       MUSTER_PUBLIC_URL=http://localhost:8080 MUSTER_SECRET_KEYS="$(openssl rand -base64 32)"
./bin/muster migrate
# {"time":"…","level":"INFO","event":"migrations_applied","from":0,"to":1}
./bin/muster &
curl -s -o /dev/null -w '%{http_code}\n' localhost:8082/health/ready      # 200
curl -s localhost:8082/metrics | grep '^muster_build_info'
# muster_build_info{version="0.0.0-dev",commit="<sha>"} 1
curl -s localhost:8082/metrics | grep -c '^muster_db_pool_'                # 4 or more
curl -s -o /dev/null -w '%{http_code}\n' localhost:8080/                    # 200 (the SPA)
docker compose -f deploy/dev/docker-compose.yml stop postgres
curl -s -o /dev/null -w '%{http_code}\n' localhost:8082/health/ready      # 503
curl -s -o /dev/null -w '%{http_code}\n' localhost:8082/health/live       # 200
docker compose -f deploy/dev/docker-compose.yml start postgres
kill -TERM %1; wait %1; echo "exit=$?"
# {"level":"WARN","event":"shutdown_requested",...}
# exit=0                                                                  (within 20 s)

# C-02.AC-5: a newer schema is refused
psql "$MUSTER_DATABASE_URL" -c 'UPDATE schema_migrations SET version = 999'
./bin/muster migrate; echo "exit=$?"
# ... "event":"schema_too_new" ... database schema version 999 is newer than this binary knows (1)
# exit=1
psql "$MUSTER_DATABASE_URL" -c 'UPDATE schema_migrations SET version = 1'

# C-02.AC-7: fields, password file, conflict, invalid port
printf muster > /tmp/muster-db-pw
env -u MUSTER_DATABASE_URL MUSTER_DATABASE_HOST=127.0.0.1 MUSTER_DATABASE_PORT=55432 MUSTER_DATABASE_NAME=muster \
  MUSTER_DATABASE_USER=muster MUSTER_DATABASE_PASSWORD_FILE=/tmp/muster-db-pw MUSTER_DATABASE_SSLMODE=disable ./bin/muster &
curl -s -o /dev/null -w '%{http_code}\n' localhost:8082/health/ready      # 200
kill %1
MUSTER_DATABASE_HOST=127.0.0.1 ./bin/muster 2>&1 | grep -m1 database_settings_conflict
# {"level":"WARN","event":"database_settings_conflict","used":"MUSTER_DATABASE_URL","ignored":["MUSTER_DATABASE_HOST"]}
# (the server keeps running after grep exits; stop it)
MUSTER_DATABASE_PORT=abc ./bin/muster; echo "exit=$?"
# invalid MUSTER_DATABASE_PORT: "abc" is not a port number
# exit=1

# C-02.AC-2: PostgreSQL 13 and a transaction pooler
docker run -d --rm --name pg13 -e POSTGRES_PASSWORD=x -p 55413:5432 postgres:13
MUSTER_DATABASE_URL='postgres://postgres:x@127.0.0.1:55413/postgres?sslmode=disable' ./bin/muster; echo "exit=$?"
# PostgreSQL 13.x is not supported: Muster needs PostgreSQL 14 or newer
# exit=1
# PgBouncer in transaction pool mode on 127.0.0.1:56432 in front of the development database
# (the pull request records the image and its settings)
MUSTER_DATABASE_URL='postgres://muster:muster@127.0.0.1:56432/muster?sslmode=disable' ./bin/muster; echo "exit=$?"
# the session connection does not keep session state (advisory locks, LISTEN) ... transaction pooler ...
# set MUSTER_DATABASE_SESSION_URL (or MUSTER_DATABASE_SESSION_HOST and MUSTER_DATABASE_SESSION_PORT)
# exit=1
MUSTER_DATABASE_URL='postgres://muster:muster@127.0.0.1:56432/muster?sslmode=disable' \
  MUSTER_DATABASE_SESSION_URL='postgres://muster:muster@127.0.0.1:55432/muster?sslmode=disable' ./bin/muster &
curl -s -o /dev/null -w '%{http_code}\n' localhost:8082/health/ready      # 200

# C-02.FR-14: sslmode prefer against a server without TLS
MUSTER_DATABASE_URL='postgres://muster:muster@127.0.0.1:55432/muster' ./bin/muster 2>&1 | grep database_connection_security
# {"level":"WARN","event":"database_connection_security","connection":"main","sslmode":"prefer","encrypted":false}
# {"level":"WARN","event":"database_connection_security","connection":"session","sslmode":"prefer","encrypted":false}

# merged listeners
MUSTER_LISTEN_APP=:8080 MUSTER_LISTEN_INGEST=:8080 ./bin/muster &
lsof -nP -iTCP -sTCP:LISTEN -a -p $! | awk 'NR>1{print $9}'
# *:8080
# *:8082
```

Chart: on a kind cluster with a PostgreSQL reachable from it, `helm install` of the chart built from this branch with an
existing Secret and `database.*` values leaves the pod `Ready`, and its init container log shows
`migrations_applied`.

## Open questions

None.

## Notes

- Suggested commit: `feat(runtime): add bootstrap settings, database connections, migrations and listeners`.
- P-02 (shutdown grace), P-03 (listener addresses) and P-04 (`prefer`) are confirmed or changed here; either way
  defaults.md and the open questions of L1.md are updated in the same pull request.
- The pooler check is functional (a lock and a `LISTEN`/`NOTIFY` round trip): PgBouncer has no SQL that reports its
  mode.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-02.FR-1 | full | the bootstrap Admin variables are used by S-010 |
| C-02.FR-2 | full | the URLs' users (snippets, callbacks, OIDC) arrive with their capabilities |
| C-02.FR-3 | full | ingest routes arrive with C-05, C-07, C-13 and C-14 |
| C-02.FR-4 | full | |
| C-02.FR-5 | full | |
| C-02.FR-6 | full | |
| C-02.FR-14 | partial | the startup report; `muster doctor` is S-008 |
| C-02.FR-15 | partial | `muster migrate`; `doctor` is S-008, the other subcommands come with C-03, C-06 and C-20 |
| C-02.FR-16 | full | later workers register with the shutdown |
| C-02.FR-19 | partial | `/metrics` served and the pool metrics; Leader-only gauges are S-008 |
| C-02.AC-2 | full | |
| C-02.AC-5 | full | |
| C-02.AC-7 | full | |
| C-02.AC-8 | partial | stub handlers; completed live by S-018 |
| C-01.FR-12 | full | together with S-001 and S-005 |
| C-01.FR-13 | partial | Muster inside `muster dev`; demo configuration grows with each capability |
| C-01.AC-2 | full | re-checked: the pod becomes ready |
