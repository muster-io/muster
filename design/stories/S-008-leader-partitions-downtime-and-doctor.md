---
id: S-008
title: Leader, partitions and retention, downtime recovery, clock skew and `muster doctor` (BE)
capability: C-02
kind: be
layer: L1
depends_on: [S-007]
covers: [C-02.FR-10, C-02.FR-11, C-02.FR-12, C-02.FR-13, C-02.FR-14, C-02.FR-15, C-02.FR-19, C-02.FR-24, C-02.AC-3, C-02.AC-4, C-02.AC-13]
files_touched:
  - internal/leader/leader.go
  - internal/leader/tasks.go
  - internal/leader/alive.go
  - internal/leader/query.sql
  - internal/leader/leader_test.go
  - internal/leader/alive_test.go
  - internal/leader/leader_integration_test.go
  - internal/partitions/partitions.go
  - internal/partitions/query.sql
  - internal/partitions/partitions_test.go
  - internal/partitions/partitions_integration_test.go
  - internal/organization/notices.go
  - internal/organization/notices_test.go
  - internal/runtime/skew.go
  - internal/runtime/skew_test.go
  - internal/runtime/runtime.go
  - internal/runtime/runtime_test.go
  - internal/runtime/bootstrap.go
  - internal/keyring/replicas.go
  - internal/keyring/query.sql
  - internal/keyring/keyring_test.go
  - internal/doctor/doctor.go
  - internal/doctor/doctor_test.go
  - internal/doctor/doctor_integration_test.go
  - internal/cli/cli.go
  - internal/cli/cli_test.go
  - internal/cli/dev.go
  - internal/cli/doctor.go
  - internal/metrics/catalogue.go
  - internal/logging/events.go
  - internal/archlint/secretleak.go
  - test/e2e/harness.go
  - test/e2e/leader_test.go
  - sqlc.yaml
  - design/prd/l1/defaults.md
  - design/prd/L1.md
  - design/db/schema.md
acceptance:
  - "[C-02.FR-10, C-02.AC-3] With two replicas, cutting the Leader's database traffic makes the other replica export `muster_leader 1` within 60 seconds, and the old Leader logs `leadership_lost` and stops its Leader tasks within `leader.fencing_timeout`."
  - "[C-02.FR-10] Only the Leader runs the Leader tasks, whose list is closed in code; two Leaders overlapping in a test leave no duplicate partitions, marks or downtime records."
  - "[C-02.FR-11] At startup and hourly on the Leader, the daily partitions of Stored Snapshots and their bodies exist for today and the next 7 days, and the monthly partitions of Timeline entries, delivery events and the Audit log for this and the next 2 months; a partition whose whole range is older than its retention period is dropped."
  - "[C-02.FR-12, C-02.AC-4] After Muster was stopped for 10 minutes and started again, the Leader logs `downtime_recorded` with a duration of about 600 seconds, stores the period in `downtime_periods`, and the recovery notice is active for no longer than `recovery.banner_duration` allows."
  - "[C-02.FR-24] While the Leader's alive mark is older than `leader.absence_notice`, the notice \"no replica is leading\" is active; the notice \"recovering after downtime\" is active until `runtime_state.recovery_until`."
  - "[C-02.FR-13] `muster_clock_skew_seconds` reports each replica's skew against the database clock, and `clock_skew` is logged at WARN when it exceeds `process.clock_skew_warning`."
  - "[C-02.FR-14, C-02.AC-13] Against PostgreSQL without TLS and with `MUSTER_DATABASE_SSLMODE=prefer`, `muster doctor` prints WARN lines for both unencrypted connections and exits 0 when every other check passes; with a Keyring that cannot decrypt the canary its `key_canary` line fails and it exits non-zero."
  - "[C-02.FR-14, C-02.FR-15] `muster doctor` prints one line per check, takes no `--actor` and changes nothing in the database."
verify: "make ci test-integration e2e"
operator_attention: false
issue: 8
---

# S-008. Leader, partitions and retention, downtime recovery, clock skew and `muster doctor` (BE)

## Scope

**IN**

- The Leader lock keeper with its bounded lease (ADR-0007) and the closed list of Leader tasks.
- The first Leader tasks: partition maintenance and retention, the alive mark, pruning the records of gone replicas.
- Creating partitions at startup before serving.
- The downtime record and the recovery window after an outage; the state of the two Organization-wide notices.
- Clock skew against the database clock.
- `muster doctor`.

**OUT**

- Exposing the notices through the API and the UI (S-012, S-014).
- The Timeline entry "Muster was unavailable" on open Alert Groups (C-09.FR-18, S-028) and collapsed overdue timers
  (S-028, S-049).
- The other Leader tasks, added by their capabilities: Heartbeat lost and Stale checks (S-023), Telegram polling
  (S-041), the outgoing heartbeat and the OIDC secret expiry check (S-053); batched deletes of unpartitioned tables
  (with the capabilities that own them).
- Connection and Destination checks in `muster doctor` (S-061, S-041).

## Contracts

- **Lock keeper** (C-02.FR-10, ADR-0007): a dedicated session connection sets `idle_session_timeout` to
  `leader.server_bound` and TCP keepalive parameters, so that PostgreSQL ends a silent session within that bound. A
  replica tries `pg_try_advisory_lock` with a constant key every `leader.ping_interval`. The Leader pings over the same
  session every `leader.ping_interval`; when a ping fails, or none has succeeded for `leader.fencing_timeout`, it
  cancels every Leader task at once, closes the connection, logs `leadership_lost` (WARN) and competes again later. `leadership_acquired`
  (INFO) on taking the lock. `muster_leader` is 1 on the Leader and 0 on other replicas. The lease's intervals run on the
  real clock of S-006, so the development clock never fences a Leader.
- **Leader tasks** (`internal/leader/tasks.go`, closed list): started when the lock is taken, cancelled when it is lost,
  each safe to run twice. Added here: partition maintenance and retention (hourly), the alive mark (every
  `leader.alive_mark_interval`), pruning replica records not refreshed for `replica.prune_after` (hourly). Gauges
  computed from the database are exported only while leading (the Leader-only flag of the metric registry). Leader
  tasks, like every background worker, run per Organization: a task iterates over the Organizations (one in L1) and
  passes `org_id` to every query on an organization-scoped table (lint 1), and retention reads its periods from that
  Organization. Partition DDL and the installation-level tables (`replicas`, `runtime_state`, `downtime_periods`) have
  no `org_id`.
- **Partitions** (C-02.FR-11, `design/db/schema.md` §6): at startup inside the migration lock, before serving, and then
  hourly on the Leader, create the daily partitions of `stored_snapshots` and `snapshot_bodies` for today and the next 7
  days and the monthly partitions of `timeline_entries`, `delivery_events` and `audit_log` for this and the next 2
  months, named `<table>_pYYYYMMDD` and `<table>_pYYYYMM` with UTC bounds, using `IF NOT EXISTS` under
  `lock_timeout = '2s'`. Retention detaches (`CONCURRENTLY`) and drops a partition whose whole range is older than
  `retention.stored_snapshots` (daily tables), `retention.alert_details` (Timeline entries, delivery events) or
  `retention.audit_log` (Audit log). `partitions_maintained` (INFO: created, dropped); a failure is logged as
  `partition_maintenance_failed` (WARN) and retried next run.
- **Alive mark and downtime** (C-02.FR-12): the Leader writes `runtime_state.alive_at`, `leader_replica_id` and
  `leader_since` on the business clock of S-006, which downtime and the recovery window use too. A new Leader whose predecessor's mark is older than
  `leader.absence_notice` records the gap in `downtime_periods`. The gap starts at the later of that mark and the last
  key-record refresh of any other replica that ran across it (started no later than `leader.absence_notice` after the
  mark, so that replicas starting together after an outage do not hide it), so that a period while replicas ran
  without a Leader is not downtime, and is recorded only when it is longer than `leader.absence_notice`. The new Leader logs `downtime_recorded` (WARN: `started_at`, `ended_at`, `duration_seconds`) and sets
  `runtime_state.recovery_until` to now plus `recovery.banner_duration`: until the longest repeat interval learned in
  `alertmanager_routes` has passed, at most 1 h, or 15 min when nothing is learned (P-01).
- **Notices** (C-02.FR-24, `internal/organization/notices.go`): `recovering_after_downtime` (audience all; `since` the
  end of the downtime, `until` = `recovery_until`) and `no_replica_leading` (audience Admins; `since` = the last alive
  mark), the `SystemNotice` kinds of the API. S-012 serves them.
- **Clock skew** (C-02.FR-13): every minute each replica compares its real clock (S-006) with the database's
  `clock_timestamp()`, corrected by half the round trip; `muster_clock_skew_seconds`; `clock_skew` (WARN) when the
  absolute skew exceeds `process.clock_skew_warning`.
- **`muster doctor`** (C-02.FR-14, FR-15): runs inside a read-only transaction, takes no `--actor`, prints one line per
  check as `OK|WARN|FAIL <check>: <detail>` and exits 1 if any check fails:

  | Check | Fails when | Warns when |
  |---|---|---|
  | `database` | the main or the session connection is unreachable | |
  | `postgresql_version` | older than 14 | |
  | `session_connection` | advisory locks or `LISTEN` do not work (a transaction pooler) | |
  | `tls_main`, `tls_session` | never | the connection is unencrypted; the line names the mode in effect |
  | `key_canary` | the canary does not decrypt with the environment's Keyring | |
  | `keyring` | the active key is not in the Keyring | an older key is still needed by an encrypted value (`encrypted_values`) |
  | `clock_skew` | | the skew exceeds `process.clock_skew_warning` |

- **Development and tests**: the end-to-end harness starts the second replica with `muster dev --replica` (S-004). The
  failover test (`test/e2e/leader_test.go`) uses the harness mode with the fakes in the test process, so that both
  replicas are `muster dev --replica` processes: it starts replica A with a database URL through a TCP proxy that the
  test can cut and waits until A exports `muster_leader 1` — A is alone, so it takes the lock — and only then starts
  replica B with the direct URL; cutting the proxy fences A, and B becomes the Leader.

## Steps

1. Write the lock keeper and the task runner. Check: integration tests with a manual clock show acquisition, fencing
   after `leader.fencing_timeout` and the server ending a silent session within `leader.server_bound`.
2. Write partition maintenance and retention, and call it at startup. Check: tests create the expected partitions,
   drop only expired ones, and run twice without errors.
3. Write the alive mark, the downtime record, the recovery window and the notices. Check: tests with a manual clock
   cover a normal handover (no downtime), a 10-minute outage and both notices.
4. Write the clock skew check and replica pruning. Check: tests with a skewed clock produce the WARN event.
5. Write `muster doctor`. Check: tests cover each line and the exit codes.
6. Write the two-replica failover test. Check: `make e2e E2E_REPLICAS=2` passes.
7. Confirm or change P-01. Check: defaults.md and L1.md say so.

## Verification

```sh
make dev-db && make build
export MUSTER_DATABASE_URL='postgres://muster:muster@127.0.0.1:55432/muster' MUSTER_PUBLIC_URL=http://localhost:8080 \
       MUSTER_SECRET_KEYS="$(openssl rand -base64 32)" MUSTER_MIGRATE_ON_START=true
./bin/muster &
curl -s localhost:8082/metrics | grep -E '^muster_(leader|clock_skew_seconds) '
# muster_leader 1
# muster_clock_skew_seconds 0.00…
psql "$MUSTER_DATABASE_URL" -Atc "SELECT count(*) FROM pg_inherits WHERE inhparent = 'stored_snapshots'::regclass"
# 8
psql "$MUSTER_DATABASE_URL" -Atc "SELECT count(*) FROM pg_inherits WHERE inhparent = 'audit_log'::regclass"
# 3

# C-02.AC-4, with the 10 minutes simulated by moving the alive mark while Muster is stopped
kill %1; wait %1
psql "$MUSTER_DATABASE_URL" -qc "UPDATE runtime_state SET alive_at = alive_at - interval '10 minutes'"
./bin/muster 2>&1 | grep -m1 downtime_recorded &
# {"level":"WARN","event":"downtime_recorded","duration_seconds":60x,...}
psql "$MUSTER_DATABASE_URL" -Atc "SELECT recovery_until - updated_at <= interval '15 minutes' FROM runtime_state"
# t

# C-02.AC-13
./bin/muster doctor; echo "exit=$?"
# OK   database: main and session connections reachable
# OK   postgresql_version: 17.x
# OK   session_connection: advisory locks and LISTEN work
# WARN tls_main: sslmode=prefer, the connection is not encrypted
# WARN tls_session: sslmode=prefer, the connection is not encrypted
# OK   key_canary: decrypts with key <id>
# OK   keyring: active key <id>, no older key in use
# OK   clock_skew: 0.0xs
# exit=0
MUSTER_SECRET_KEYS="$(openssl rand -base64 32)" ./bin/muster doctor; echo "exit=$?"
# FAIL key_canary: master key does not match the database
# exit=1

# C-02.AC-3, by hand: replica A reaches the database through socat, replica B directly
kill %1
socat TCP-LISTEN:55500,fork,reuseaddr TCP:127.0.0.1:55432 &
MUSTER_DATABASE_URL='postgres://muster:muster@127.0.0.1:55500/muster' MUSTER_LISTEN_APP=:9080 \
  MUSTER_LISTEN_INGEST=:9081 MUSTER_LISTEN_INTERNAL=:9082 ./bin/muster > a.log 2>&1 &
sleep 5; ./bin/muster > b.log 2>&1 &
curl -s localhost:9082/metrics | grep '^muster_leader '        # muster_leader 1   (A leads)
pkill socat; date
grep -m1 leadership_lost a.log                                  # within 15 s of the cut
curl -s localhost:8082/metrics | grep '^muster_leader '        # muster_leader 1   (B, within 60 s of the cut)
```

`make e2e E2E_REPLICAS=2` runs the same failover automatically.

## Open questions

1. Resolved: the downtime starts at the later of the last alive mark and the last key-record refresh of any other
   replica, so a period with live replicas is not downtime (the maintainer accepted the proposal). The question was:
   the alive mark shows only that no Leader wrote it; if replicas keep running without a Leader for longer than
   `leader.absence_notice`, the next Leader would record that as downtime although ingestion and delivery never
   stopped.

## Notes

- Suggested commit: `feat(runtime): add leader election, partition maintenance, downtime recovery and doctor`.
- P-01 (`recovery.banner_duration`) is confirmed or changed here. Until C-06 learns repeat intervals the window is
  always the 15-minute fallback; the rule already reads the learned intervals, so nothing changes when they appear.
- Every Leader task must stay safe to run twice; a task that cannot be is a design error, not a lock problem.
- P-01 is confirmed. Replica records are on the real clock and the alive mark on the business clock: the takeover
  shifts a replica's refresh time by the business clock's offset before comparing them.
- Partition maintenance runs on its own session connection, bounded like the Leader's lock session, under a session
  advisory lock, so that a starting replica and the Leader, or two overlapping Leaders, take turns. The Leader's run
  waits for that lock at most `lock_timeout` and is retried at the next run; a start takes the lock only when a
  partition is missing. A partition that was detached and not dropped is dropped by the next run.
- `muster doctor` accepts the published development key only as `muster dev doctor`, as the server does.
- Known limit of the downtime rule: after an outage of the database itself, while the replicas kept running, a
  replica whose first refresh after the outage comes before the new Leader's takeover hides the outage, because one
  `refreshed_at` cannot tell a replica that ran throughout from one that came back. Raised with the maintainer in the
  pull request.
- `muster doctor` sets both of its connections to read-only transactions (`SET SESSION CHARACTERISTICS AS TRANSACTION
  READ ONLY`), so every statement it runs is read-only. When the database is unreachable it prints only the
  `database` line, since no other check can run. The "no replica is leading" notice is also active, without a
  `since`, on a database that never had a Leader.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-02.FR-10 | partial | the lock and the first tasks; other tasks join with S-023, S-041 and S-053 |
| C-02.FR-11 | full | |
| C-02.FR-12 | partial | downtime record and recovery window; Timeline entries are S-028, collapsed timers S-028 and S-049 |
| C-02.FR-13 | full | |
| C-02.FR-14 | partial | `muster doctor` without Connection and Destination checks (S-061, S-041) |
| C-02.FR-15 | partial | `muster doctor`; the other subcommands come with S-011, S-012, S-021 and S-055 |
| C-02.FR-19 | partial | `muster_leader`, `muster_clock_skew_seconds` and the Leader-only flag |
| C-02.FR-24 | full | served by S-012 |
| C-02.AC-3 | full | |
| C-02.AC-4 | full | |
| C-02.AC-13 | full | |
