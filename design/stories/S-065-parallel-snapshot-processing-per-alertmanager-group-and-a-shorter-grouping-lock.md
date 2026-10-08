---
id: S-065
title: Parallel Snapshot processing per Alertmanager group and a shorter grouping lock (BE)
capability: C-06
kind: be
layer: L1
depends_on: [S-061]
covers: [C-06.FR-1, C-09.FR-3]
files_touched:
  - internal/ingest/lanes.go
  - internal/ingest/worker.go
  - internal/ingest/process.go
  - internal/ingest/repeat.go
  - internal/ingest/truncation.go
  - internal/ingest/query.sql
  - internal/ingest/lanes_test.go
  - internal/ingest/worker_test.go
  - internal/ingest/internal_test.go
  - internal/ingest/lanes_integration_test.go
  - internal/groups/grouping.go
  - internal/groups/dispatcher.go
  - internal/groups/query.sql
  - internal/groups/grouping_test.go
  - internal/groups/dispatcher_test.go
  - internal/groups/groups_integration_test.go
  - internal/db/migrations/0004_thread_replies_pending_index.up.sql
  - internal/db/migrations/0004_thread_replies_pending_index.down.sql
  - internal/db/migrate_test.go
  - internal/db/db_test.go
  - internal/logging/events.go
  - internal/delivery/query.sql
  - internal/delivery/live_test.go
  - internal/timers/query.sql
  - internal/db/db.go
  - internal/db/dbtest/plans.go
  - test/load/main.go
  - .github/workflows/nightly.yml
  - web/e2e/integrations.spec.ts
  - design/prd/l1/C-06-snapshot-processing.md
  - design/prd/l1/defaults.md
  - design/prd/L1.md
  - design/adr/0002-webhook-only-ingestion-with-snapshot-semantics.md
  - design/adr/0006-postgresql-only-storage-and-queues.md
  - design/architecture.md
  - design/db/schema.md
  - design/stories/S-020-snapshot-processing-and-alerts-view.md
acceptance:
  - "[NFR-1, NFR-2] `make load-test` with the full profile against `muster dev` — 50 webhooks per second for one minute from one Integration, 10,000 Alerts in 1,000 Alert Groups, delivered to the fake Mattermost server — accepts all 3,000 webhooks and rejects none, with ingestion's 99th percentile at most 1 s (P-44) and the delivery latency's 95th percentile at most 5 s; the nightly workflow's load test passes on Linux."
  - "[C-06.FR-1] Stored Snapshots of one Alertmanager group are processed in arrival order and never two at a time, also with two replicas; Snapshots of different Alertmanager groups of one Integration are processed at the same time, up to `processing.parallel_groups` per Integration."
  - "[C-06.FR-1] Two Snapshots of different Alertmanager groups that list a common fingerprint are processed in arrival order, and the deletion marker of an Integration is processed after every Snapshot of that Integration received before it and before every one received after it."
  - "[C-06.FR-1, C-09.FR-3] Two Alertmanager groups whose Alerts join one Alert Group, processed at the same time, give one Alert Group with one number, every Alert a member once, and its Timeline entries; no transaction fails on a deadlock or a unique violation, under the race detector."
  - "[C-06.FR-1, C-06.FR-20] A Snapshot whose transaction did not commit because its replica was killed stays pending and is processed once, by another replica after the lease runs out; the later Snapshots of its Alertmanager group wait for it, other Alertmanager groups do not, and the Integration's `snapshot_count` counts each Snapshot once. A deadlock or serialization failure in one lane retries that Snapshot in its lane without pausing the Integration's other lanes."
  - "[C-09.FR-3] A Snapshot whose Alerts join Alert Groups that are already open does not take the Organization's `alert_group_counters` row; one that creates or reopens an Alert Group does, and two that create an Alert Group of the same Route and Group key values at the same time make one."
verify: "make ci test-integration e2e"
operator_attention: false
issue: null
---

# S-065. Parallel Snapshot processing per Alertmanager group and a shorter grouping lock (BE)

## Scope

**IN**

- The processing worker processes Stored Snapshots of different Alertmanager groups of one Integration at the same
  time, in arrival order within each Alertmanager group, so that one Integration's burst meets NFR-2 under the NFR-1
  load (D283).
- The locks that would serialize those transactions anyway: the claim row, the Alertmanager route row, the
  Integration row of `CountSnapshot`, the order of the Alert row locks, and the Organization's counter row, which
  grouping takes today for every placement rather than only to create or reopen an Alert Group.
- The design documents that state the order per Integration (C-06.FR-1, ADR-0002, ADR-0006, `architecture.md`,
  `schema.md`), changed as the maintainer decided (D285).
- The index of pending Thread replies per delivery (migration 0004): the load test after the lanes showed the claim of
  due Thread replies scanning every pending reply for each candidate, which slowed each claim to more than a second
  once replies queued, and starved delivery (Notes).
- The claims (`ClaimDueDeliveries`, `ClaimDueReplies`, `ClaimBrokenProbes`, `ClaimDueTimers`, `ClaimIntegrations`)
  as materialized CTEs: the nightly run on Linux showed the choice, written as a subquery in `FROM`, planned as the
  inner side of a nested loop over every row and run once per row (Notes).
- The default size of the main pool, max(10, CPUs) when `MUSTER_DATABASE_URL` sets no `pool_max_conns` (P-50, D286):
  pgx's max(4, CPUs) left four connections on the 4-CPU runner, which processing, delivery and ingestion saturated.
- A breakdown in the load test — progress every 5 s, processing lag, delivery attempts by kind, waits for the main
  pool, the host's CPUs — and, in the nightly job, the runner's CPUs, memory, PostgreSQL settings and WAL flush
  latency, the pool size, and the durations of processing and delivery attempts even after a failed run.

**OUT**

- Coalescing superseded Snapshots of one Alertmanager group: it would lose observable behaviour (Notes).
- The load test, its profile and its thresholds, which S-061 set.
- The delivery worker, which delivers one attempt at a time per replica and is now what bounds NFR-2 under the full
  profile (Notes); parallel delivery across Alert Groups is a story of its own (C-11).
- Cutting round trips in rendering and delivery bookkeeping beyond what the grouping lock needs.

## Contracts

- **Ordering** (C-06.FR-1, D285): a Snapshot's lane is its Alertmanager group, named by
  its `groupKey`; the worker that holds an Integration's lease reads its pending Stored Snapshots in arrival order
  (`received_at`, `id`), parses them, and hands each to the lane of its `groupKey`. A Snapshot starts only when every
  earlier pending Snapshot of the same Integration that has the same `groupKey`, or lists a fingerprint it lists, has
  finished; a deletion marker (`internalalerts.DeletionGroupKey`) waits for every earlier one and holds back every
  later one. A body that does not parse is failed at once (C-06.FR-20) and holds back nothing. A Snapshot read late
  whose receipt is earlier than held ones goes before those that have not started (`run.add`).
- **Setting**: `processing.parallel_groups`, built in, provisional — 2 lanes per Integration (P-49 in L1 §5.3,
  `defaults.md`), measured against 4, 5 and 8 (Notes), and never more than half the connections of the replica's main
  database pool (`lanesFor`), at least one; a gate keeps the lanes of all the Integrations a replica works on together
  to half the pool as well. The worker's existing limit of 4 Integrations at a time per replica stays.
- **Lease** (`ingest_claims`): the lease is renewed by the run that holds the Integration, every `Lease` / 3, not by
  every Snapshot's transaction (the Stale scan still renews in its own transaction); each Snapshot's transaction, and
  `fail`'s, checks with `CheckIngestLease` that this replica holds a lease that has not run out on the real clock, with
  `FOR KEY SHARE` on the claim row instead of the `UPDATE` that serialized every transaction of the Integration. `FOR KEY SHARE` does not block the holder's own
  renewal, and a replica that wants the lease skips the row while a lane holds it (`ClaimIntegrations` uses
  `SKIP LOCKED`).
- **Queries changed** (`internal/ingest/query.sql`): `RenewIngestLease` split into the renewal and a shared check;
  `NextPendingSnapshot` becomes `ListPendingSnapshots` (the oldest pending Snapshots of an Integration, a bounded
  page); `ListSnapshotAlerts` locks Alerts `ORDER BY a.id`, which is required, not optional: the presences of other
  `groupKey`s (`active_elsewhere`, `EndPresences` without a `groupKey`) are safe only under those row locks; a unique
  violation of `InsertAlerts` on a fingerprint another lane inserted meanwhile is treated as transient and retries the
  Snapshot, never adopted, since the engine decided a first firing; `UpsertAlertmanagerRoute`
  no longer locks the Alertmanager route row, which every Alertmanager group of a route shares, when only
  `last_seen_at` changes; the repeat-interval ring of the route (`UpdateRepeatInterval`) is locked, read, appended and
  written as one of the last statements of the transaction, so that two lanes of one route lose no observation and
  hold the row only through the commit; `CountSnapshot` stays the last statement for the same reason; the Integration
  row is locked only when a Snapshot changes the truncation of a `groupKey` (`MusterSnapshotTruncated`, C-06.FR-6),
  so that two lanes never raise and resolve it from counts that miss each other. `truncationChanged` moves from before
  the Sink to just before `FinishSnapshot`, so that the Integration row is always the last lock a lane takes, and its
  raise or resolve is stamped with the business clock's now under that lock instead of the Snapshot's `received_at`:
  lanes commit out of `received_at` order, and the built-in Integration must see the raise and the resolve in the
  order they were decided. A replayed Snapshot (`replayed_at` set, read by `ListPendingSnapshots`) keeps its
  `received_at`, so that a second replay changes nothing (C-06.AC-6).
- **Lock order** of one Snapshot's transaction, the same in every lane: claim row (`FOR KEY SHARE`), Alertmanager
  group row, Alert rows by id, Muster Routes (`FOR SHARE`), counter row when it is taken, Alert Group rows by id,
  Storm rows, then the Alertmanager route row and the Integration row at the end. Alert Groups fed by several
  Alertmanager groups stay serialized by `LockGroups` from the lock to the commit, as today; a Storm (`JoinStorm`,
  `StartStorm`) is touched only on creation and stays under the counter row. Two exceptions, both safe: a join that
  looks an Alert Group up again after its first locks takes the new one out of id order, which a deadlock retry covers
  now that no counter row orders joiners; and the first transaction to create an Alertmanager route makes the other
  lanes of that route wait for its commit, once per route.
- **Retry in a lane**: a deadlock or serialization failure (class `40`) or a unique violation of
  `alerts_integration_id_fingerprint_key` leaves the Snapshot pending and retries it in its lane after a short,
  growing, jittered pause, up to 5 times, holding back only the Snapshots that wait for it; only then does the
  Integration's run stop and pause as before (`settle`). Other transient errors (a lost connection) stop the run as
  before.
- **Grouping** (`internal/groups`): the restamp of Alerts of a deleted Route, made before the savepoint, is still
  reported (the Default route in `Routed`) when the join falls back. When every slot finds an open Alert Group, `place` locks them in id order
  (`LockGroups`) without the counter row and re-reads where the Alerts fire after the locks, as `unplace` does today;
  the join commits if `lockAll`'s checks hold. A slot that needs a new or a reopened Alert Group from the start takes
  the counter row before any Alert Group lock, as today. When a join finds, after its locks, that an Alert Group it
  found open was resolved, or that an Alert it places fires somewhere already, the transaction rolls back to the
  savepoint (`SavepointGrouping`, taken just before the join's locks) and groups again with the counter row first, so that no lookup after an Alert Group lock
  ever needs the counter row (which would deadlock against a lane that holds it, or fail `InsertGroup` on
  `alert_groups_open_key`). `schema.md` already says the row "serializes creations per Organization"; the comments
  that rely on "the counter row, which every grouping takes" change with it. `EndGracePeriod` and `Unresolve` keep
  taking it.
- **Log events**: `snapshot_processed` gains `lane` (0 to `processing.parallel_groups` − 1) so that a reader can see the
  parallelism; no new event, metric or Internal alert.
- **Main pool** (`internal/db`): `pool_max_conns` of `MUSTER_DATABASE_URL` defaults to max(`MinPoolConns` = 10,
  CPUs), P-50 in L1 §5.3 and `defaults.md`; a URL that sets it wins. The session connections stay single connections
  (the LISTEN connection, the Leader's, partition maintenance's), so a replica opens at most the pool and about 3 more;
  the compose example's `max_connections = 30` holds two replicas. With the default, the lanes' half of the pool is 5,
  above the 2 lanes of P-49.
- **Claims**: every `UPDATE … FROM (SELECT … LIMIT … FOR UPDATE SKIP LOCKED)` claim takes its rows in a
  `WITH … AS MATERIALIZED` CTE, run once per call; `dbtest.LockRowsLoops` checks it from the executed plan
  (auto_explain), with hash and merge joins off. The retention deletes (`DELETE … WHERE id IN (SELECT … LIMIT … FOR
  UPDATE SKIP LOCKED)`) keep their form: they are batches of the Leader, not claims of the hot path.
- **Schema**: migration `0004_thread_replies_pending_index` adds `thread_replies_pending_idx`
  `(delivery_id, id) WHERE state = 'pending'`, expand only; `schema.md` lists it. It is built without `CONCURRENTLY`,
  like the other migrations, in a transaction with a lock timeout: nothing is released before L1 is complete, and the
  table holds only the replies of open and recent Alert Groups.

## Steps

1. Settle the Open questions; update C-06.FR-1, ADR-0002, ADR-0006, `architecture.md`, `schema.md`, `defaults.md` and
   L1 §5.3 as decided, and the C-06.FR-1 acceptance of S-020. Check: the documents say the same order.
2. Change the lease, the claim of pending Snapshots and the lanes (`lanes.go`, `worker.go`), with the queries above.
   Check: `lanes_test.go` (the ordering rule, the deletion barrier, a body that does not parse, order within a lane,
   a shared fingerprint, retries in a lane, the lease and its renewal) and `lanes_integration_test.go` (two replicas
   on PostgreSQL 14 and 17, a replica killed mid-transaction), all with `-race`.
3. Remove the serializing row locks in processing (route row, Integration row, Alert lock order, inserts of the same
   fingerprint). Check: the tests of step 2 and `worker_test.go`, `internal_test.go`.
4. Take the counter row only for creations and reopens in `grouping.go`, with the savepoint fallback. Check:
   `grouping_test.go` and `groups_integration_test.go` with two transactions joining and creating the same Alert Group
   at the same time, and a join whose open Alert Group is resolved by a person before its lock while another
   transaction creates the next one.
5. Run the full load profile before and after with 2, 4, 5 and 8 lanes, keep the best, and add the index the
   profile then showed missing. Check: Verification below, and a manual run of the nightly workflow.

## Verification

```sh
make dev &
# the full NFR-1 profile, delivered to the fake Mattermost server (S-061)
make load-test
# load test: accepted 3000, rejected 0, failed 0 of 3000 sent
# load test: ingest p99 …s (≤ 1s, 3000 requests)
# load test: delivery p95 …s (≤ 5s, 3000 root message changes, 0 still waiting)
# load test: PASS
# the same with the main pool of a 4-CPU runner: MUSTER_DATABASE_URL=…&pool_max_conns=4 and
# MUSTER_DATABASE_SESSION_URL without it
jq -r 'select(.event == "snapshot_processed") | .lane' dev.log | sort -u | wc -l     # more than 1
psql "$MUSTER_DATABASE_URL" -Atc "SELECT count(*) FILTER (WHERE state = 'processed'), count(*) FROM stored_snapshots"
# 3000|3000 (plus the development demo's own)
gh workflow run nightly --ref <branch>   # the load-test job passes on Linux
make ci test-integration e2e
E2E_REPLICAS=2 make e2e
```

## Open questions

- ~~**The order of Snapshot processing (D283).** C-06.FR-1 says Stored Snapshots are processed "in arrival order per
  Integration, at most one per Integration at a time"; ADR-0002 ("At most one Stored Snapshot per Integration is
  processed at a time, which preserves order within an Integration") and ADR-0006 ("order per Integration") say the
  same, and so do `architecture.md` and `schema.md`. With that order NFR-2 cannot hold for one Integration under the
  NFR-1 load (Notes), so this story changes a documented guarantee. Recommendation: order per Alertmanager group, as
  under Contracts — C-06.FR-1 becomes "A worker woken by `LISTEN/NOTIFY` processes the Stored Snapshots of each
  Alertmanager group of an Integration in arrival order, at most one per Alertmanager group at a time, and up to
  `processing.parallel_groups` Alertmanager groups of an Integration at the same time; two Snapshots that list a
  common fingerprint keep their arrival order, and the deletion marker of an Integration waits for every earlier
  Snapshot of it and holds back every later one"; ADR-0002 and ADR-0006 say "order per Alertmanager group" with this
  reason. The Snapshot semantics are per pair of Alert and `groupKey` already (ADR-0002), the HA copies of one
  notification share a `groupKey`, and Alert Groups are serialized by the dispatcher's row locks, so what changes is
  only the relative order of two Alertmanager groups that do not share a listed fingerprint. An Alert that one
  Alertmanager group stops listing while another lists it may then be decided in either order; both orders are valid
  outcomes of the rules (at worst a Gone and a new firing instead of nothing), and Gone needs at least
  `processing.gone_min_absence`, far longer than the reordering. The alternatives: narrow NFR-1 to "50 webhooks per
  second over several Integrations" (with two Integrations at 25 per second each the local delivery p95 was still
  7.9 s), or halve the round trips of one Snapshot's transaction, which touches routing, grouping, rendering and
  delivery bookkeeping and still depends on the database's round-trip time.~~
  _Resolved (D285):_ as recommended; C-06.FR-1, ADR-0002, ADR-0006, `architecture.md` and `schema.md` say so.
- ~~**The number of lanes.** Recommendation: 8 per Integration, built in and provisional (P-49), confirmed by the
  nightly load test; the main pool (`pgxpool`'s default of max(4, CPUs) connections) bounds the real parallelism on a
  small replica, so the implementation measures 4 and 8 and records the result in P-49.~~
  _Resolved (D285):_ as recommended; the implementation measures 4 and 8 lanes and keeps the better one (Notes).

## Notes

- Suggested commit: `perf(ingest): process alertmanager groups of an integration in parallel`.
- **Profile** (macOS, PostgreSQL 17 in Docker, the full profile against `muster dev`, before this story): accepted
  3,000, rejected 0, ingest p99 0.022 s, delivery p95 56.3 s; `snapshot_processed` reports 38.7 ms per Snapshot on
  average (p50 36 ms, p95 54 ms). The nightly run on Linux after S-061 gave ingest p99 0.47 s and delivery p95 56 s.
  - A CPU profile of the replica during the run spends 5.8 s of 10.3 s of samples in `pgx.BeginFunc`, nearly all of it
    in system calls on the socket: processing waits on the database, it does not compute.
  - PostgreSQL's statement log (`log_min_duration_statement = 0`) shows 46 statements in every Snapshot's transaction,
    48 round trips with `BEGIN` and `COMMIT`, executing in 8.8 ms of server time out of 41.9 ms of wall time. No
    statement stands out: the largest are `ListSnapshotAlerts` (1.35 ms), `UpdateAlerts` (0.54 ms) and
    `NextPendingSnapshot` (0.44 ms); there is no N+1 query beyond `ListFiringMemberships`, read 4 times. The 35–40 ms
    are about 0.6 ms of round trip times 48, in strict sequence.
  - The statements: processing (`RenewIngestLease`, `NextPendingSnapshot`, the Alertmanager route and group, the
    Alerts, the presences), routing (`ListAlertLabels`, `GetRoutingStamp`, `SetAlertRoutes`), grouping (from
    `LockRoutes` and the counter to `SaveGroup`), rendering and delivery bookkeeping (from `GetSnapshotReceivedAt` to
    `NotifyDelivery`), then `FinishSnapshot` and `CountSnapshot`.
  - From `LockCounter` to `COMMIT` is 23 ms of the 42 ms (with the statement log on): every Snapshot of the profile
    brings Alerts without an Alert Group, so every one takes the Organization's counter row and holds it through
    rendering. With per-Integration order removed alone, that row would cap processing at roughly 1 / 23 ms, about 45
    Snapshots per second for the Organization, an estimate — hence the grouping part of this story. The first of the
    profile's three passes creates an Alert Group with every Snapshot and stays under the counter row; the other two
    only join open Alert Groups. Should the creations of the first pass still put the 95th percentile above 5 s, the
    next lever is to read what rendering needs of the configuration (`ListRouteDestinations`, `GetRouteDelivery`,
    `ListLinkRules`, `ListLinkRuleMatchers`) before the counter row is taken, or in one pipelined round trip.
  - Two load tests at 25 per second each on two Integrations at the same time (3,000 Snapshots, the same total load)
    gave a delivery p95 of 7.9 s instead of 56 s, at 40.8 ms per Snapshot: the database has room, parallel lanes help.
- **Measurements after the change** (the same machine, the full profile, a fresh database each run; delivery p95 in
  seconds, every run accepted 3,000 webhooks and rejected none, ingestion's p99 stayed between 0.010 and 0.044 s):

  | Lanes | Main pool | Reply index | Delivery p95 |
  |---|---|---|---|
  | 1 (before) | 10 | no | 56.3 |
  | 8 | 10 | no | 273.9, 274.0 |
  | 5 | 10 | no | 274.0, 3.4 |
  | 4 | 10 | no | 0.9, 268.9, 54.6 |
  | 4 | 4 | no | 24.3 |
  | 2 | 4 | no | 4.3, 27.0, 1.0 |
  | 4 | 10 | yes | 27.0, 27.6 |
  | 2 | 10 | yes | 0.9, 1.7, 9.1, 0.9, then with the review fixes 0.2, 0.4 |
  | 2 (4, halved by the pool) | 4 | yes | 1.6, 1.0, 1.3, then with the review fixes 0.5 |

  With lanes, processing keeps up with the burst in every run: all 3,000 Snapshots are processed within the minute of
  sending, at 36 ms each with 2 lanes. What bounds NFR-2 now is delivery. The delivery worker makes one attempt at a
  time per replica (a claim, a transaction that reads and renders, the call, a transaction that records), about 70
  attempts per second here, for 3,000 Root message changes and about 1,200 Thread replies in a minute; every lane
  beyond two takes database time from it, and the counter row, which every Snapshot of the profile's first pass takes
  to create its Alert Group, keeps extra lanes waiting. Once delivery falls behind, `ClaimDueReplies` and
  `NextDeliveryWork` grew from milliseconds to 1.3 s and 200 ms per call (statements over 50 ms logged), because the
  check "an earlier reply of this delivery is pending" scanned every pending reply for each candidate; migration 0004
  indexes it. Two lanes, at most half the pool, is the best setting measured; one run in ten with it still missed
  5 s, so the nightly job may still fail now and then until delivery works on several Alert Groups at the same time.
- **On the Linux runner** (4 CPUs, 16 GB, the development PostgreSQL with `fsync` and `synchronous_commit` on, a
  main pool of 4): processing takes 15–27 ms per Snapshot and its lag stays under 0.1 s at p95, so lanes and the
  index were enough for processing. Three nightly runs of the same head passed at 0.24 s, failed at 25.9 s and passed
  at 0.24 s. The failed one waited 29.4 s in all for the pool, took 53,000 connections instead of 41,000 and sent 1,887
  updates instead of 2,000: delivery fell behind, not processing. `auto_explain` caught the cause locally under the
  same pool and `GOMAXPROCS=4`: `ClaimDueReplies`, an `UPDATE … FROM (SELECT … LIMIT … FOR UPDATE SKIP LOCKED)`,
  was planned, with the statistics of a young table, as a nested loop that scanned every Thread reply and ran the
  choice once for each of them (983 loops, 0.5 s and growing to 7 s per call). As a materialized CTE the choice runs
  once; six local runs after it showed no slow claim, and delivery latency followed processing lag. The other claims
  of the same shape got the same form, with a test that fails the old form (2 loops) and passes the new (1).
  The last failure on the runner after that (run 37826430446) was the main pool: 4 connections, all in use from the
  start, 157 s of waits in all, processing lag p95 8.7 s; hence the default of P-50.
- **Nightly on Linux with the pool default and all claims as CTEs** (head 75a5cc7, 4 CPUs, a main pool of 10): runs
  37833669178, 37833686749 and 37833702723 all passed; delivery p95 0.247 s, 0.317 s and 0.243 s; processing lag p95
  0.155 s, 0.179 s and 0.099 s; ingest p99 0.438 s, 0.009 s and 0.008 s; pool waits 20.0 s, 0.0 s and 0.7 s in all;
  3,000 accepted and none rejected each time.
- **Memory with the larger pool** (NFR-3, the compose example's PostgreSQL: 160 MB cap, `shared_buffers` 32 MB,
  `work_mem` 2 MB, `max_connections` 30, measured locally under the full load): peak 83.4 MiB with a pool of 4 and 6
  backends, 98.3 MiB with the pool of 10 and 12 backends (95.8 MiB delivering to the fake Mattermost server), about
  2.5 MiB per backend; the cap leaves about 60 MiB.
- **Cheap fixes within the current order** are not enough: removing the redundant reads (`GetSnapshotReceivedAt`,
  which processing already knows, the repeated `ListFiringMemberships`, the second `pg_notify`) saves 4 or 5 of 48
  round trips, about 10 %, while one Integration needs at most about 20 ms per Snapshot to keep up with 50 per second.
  The implementation may still take them where it touches those lines.
- **Coalescing superseded Snapshots** of one Alertmanager group is not done: an older pending Snapshot is not covered
  by a newer one. An Alert listed only in the older one would never fire, and an Alert resolved in between would lose
  its resolve and its `[resolved]` Timeline entry; skipping a Snapshot would also break the two-window rule of Gone
  (C-06.FR-5, FR-10), the duplicate window, repeat-interval learning and the count of `snapshot_count`, and need a new
  Stored Snapshot state for replay.
- The deadlock and serialization errors that remain possible (class `40`) are transient (`transient` in
  `worker.go`), but today a transient error backs off the whole Integration for up to a minute; with lanes they
  become routine, hence the retry in a lane under Contracts.
- Every lane's commit sends a `NOTIFY` (delivery, timers, hints), and PostgreSQL serializes commits that notify; this
  bounds throughput only, and the load test measures it.
- The contract was reviewed against the code for shared rows, lock order and the ordering rule before it was
  written down; apart from the truncation alert (now stamped under the Integration row), no interleaving was found that
  yields a state no serial order could.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-06.FR-1 | partial | the order per Alertmanager group and the parallel lanes; the worker, `LISTEN/NOTIFY` and the lease are S-020 |
| C-09.FR-3 | partial | the counter row only for creations and reopens, with joins and creations of one Alert Group at the same time; the grouping rules are S-028 |

The non-functional requirements are not IDs of a capability: the story serves NFR-1 (the burst of one Integration
processed without a backlog) and NFR-2 (the delivery latency under the full profile), whose profile, metric, chart
rule and thresholds are S-004, S-018, S-034, S-059 and S-061; [coverage.md](coverage.md#non-functional-requirements)
lists it under both.
