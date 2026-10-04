---
id: S-020
title: "Snapshot processing: Alerts, resolves, Gone, truncation, Continuation, staleness bookkeeping and the Alerts view (BE)"
capability: C-06
kind: be
layer: L1
depends_on: [S-018]
covers: [C-06.FR-1, C-06.FR-2, C-06.FR-3, C-06.FR-4, C-06.FR-21, C-06.FR-5, C-06.FR-6, C-06.FR-7, C-06.FR-8, C-06.FR-10, C-06.FR-11, C-06.FR-12, C-06.FR-13, C-06.FR-15, C-06.FR-19, C-06.FR-20, C-06.AC-1, C-06.AC-2, C-06.AC-4, C-06.AC-8, C-06.AC-9, C-06.AC-10, C-06.AC-12, C-06.AC-13, C-06.AC-14, C-06.AC-15, C-05.FR-4, C-05.FR-6, C-05.FR-7, C-05.AC-8, C-01.FR-13]
files_touched:
  - internal/ingest/worker.go
  - internal/ingest/payload.go
  - internal/ingest/process.go
  - internal/ingest/presence.go
  - internal/ingest/truncation.go
  - internal/ingest/repeat.go
  - internal/ingest/changes.go
  - internal/ingest/alertsview.go
  - internal/ingest/query.sql
  - internal/ingest/worker_test.go
  - internal/ingest/payload_test.go
  - internal/ingest/process_test.go
  - internal/ingest/presence_test.go
  - internal/ingest/repeat_test.go
  - internal/ingest/facts_test.go
  - internal/matchers/matchers.go
  - internal/matchers/matchers_test.go
  - internal/api/integrations.go
  - internal/api/integrations_test.go
  - internal/clock/clock.go
  - internal/clock/clock_test.go
  - internal/server/server.go
  - internal/devmode/devmode.go
  - internal/devmode/query.sql
  - internal/devmode/devmode_test.go
  - internal/fakes/fakealertmanager/fakealertmanager.go
  - internal/fakes/fakealertmanager/groups.go
  - internal/fakes/fakealertmanager/scenarios.go
  - internal/fakes/fakealertmanager/fakealertmanager_test.go
  - internal/leader/tasks.go
  - internal/metrics/catalogue.go
  - internal/logging/events.go
  - internal/runtime/runtime.go
  - test/e2e/processing_test.go
acceptance:
  - "[C-06.FR-1] Stored Snapshots of one Integration are processed in arrival order and never two at a time, also with two replicas; a Snapshot of another Integration is not held up by them."
  - "[C-06.FR-2, C-06.FR-13] A Snapshot is split into Alerts by fingerprint, and the identical copy of an HA pair, or the same Snapshot sent again, changes nothing (`fingerprint + status + startsAt`)."
  - "[C-06.FR-3, C-05.FR-6, C-06.AC-8] Static labels are added to every Alert; an Alert carrying `cluster=\"a\"` on an Integration with the Static label `cluster=b` keeps `a` and lists `cluster` in `static_label_warnings` of the Alerts view."
  - "[C-06.FR-5, C-06.FR-10, C-06.AC-1, C-06.AC-2] An Alert missing from one Snapshot stays firing; Snapshots of one `groupKey` within the duplicate window count as one, so a copy 20 seconds later without the Alert proves nothing; missing from a later window too, received at least `processing.gone_min_absence` after the first miss, it resolves as `gone` with the reason text of C-06.FR-10."
  - "[C-06.FR-5, C-06.AC-15] Snapshots at T, T+60 s and T+120 s listing 3, 9 and 10 of 10 firing Alerts, Alert A missing from the first two, leave every Alert firing; with A missing at T, T+60 s and T+6 min it stays firing until T+6 min and is Gone then."
  - "[C-06.FR-21, C-06.AC-13] With 20 Alerts firing in one `groupKey` and one in another, a Snapshot with `status: resolved` and `notification_reason` `all alerts resolved` that lists 6 of the 20 resolves all 20 and increases `muster_alerts_resolved_total{reason=\"resolved\"}` by 20; the Alert of the other `groupKey` still fires."
  - "[C-06.FR-4, C-06.AC-10, C-06.AC-14] A `resolved` for a fingerprint that fires nowhere changes no Alert and increases `muster_ingest_resolved_dropped_total` by one; the same `resolved` of an Alert already resolved, arriving again alone and inside firing Snapshots, changes nothing and is not counted; a `resolved` with an older `startsAt` leaves a newer firing of the fingerprint firing."
  - "[C-06.FR-11, C-06.AC-4] A new `startsAt` without `resolved` on a firing Alert is a Continuation: the Alert stays firing with the new `startsAt` and no new firing (`episode`) is recorded; after the Alert has resolved, the next firing is a new one."
  - "[C-06.FR-12] A change in annotations updates the Alert and is recorded as an Alert change of its own."
  - "[C-06.FR-6, C-06.FR-7] A Snapshot with `truncatedAlerts > 0` marks its `groupKey` truncated until its next untruncated Snapshot, counts in `muster_ingest_truncated_snapshots_total`, and is never evidence of absence; an Alert listed in two `groupKey`s is not Gone while one of them still lists it."
  - "[C-06.FR-8] The repeat interval of an Alertmanager route is learned as the median of the recent gaps that end in a `repeat interval elapsed` Snapshot — or, without `notification_reason`, in a Snapshot identical to the previous one — with near-duplicates ignored; each listing stores the Integration's liveness clock for the Stale scan of S-023."
  - "[C-06.FR-20, C-06.AC-12, C-05.AC-8, C-05.FR-4] A Stored Snapshot whose body is `not json` is marked `failed` with an error, writes one `snapshot_failed` line, increases `muster_ingest_failed_snapshots_total` by one, is not retried, and the next valid Snapshot of the Integration is processed."
  - "[C-06.FR-15, C-06.AC-9] One webhook with 200 Alerts writes exactly one `snapshot_processed` line; the processing delay, the backlog (Leader only) and resolutions by reason are exported."
  - "[C-06.FR-19, C-05.FR-7] `listIntegrationAlerts` returns the Alerts of an Integration with labels, state, the resolution reason and time, `startsAt`, time last seen, the `groupKey`s listing it and Static label warnings, filtered by state, Matchers and text and sorted by last seen or `startsAt`; the Integration's `snapshot_count` grows with each processed Snapshot."
  - "[C-01.FR-13] The development clock is one for all replicas of a database: advancing it through one replica moves the clock of the other within a second, and a replica started afterwards starts at the advanced time."
  - "[C-01.FR-13] The fake Alertmanager reproduces each Alertmanager fact F-033 to F-053 that processing relies on, and a table-driven test runs every one of them through processing with a manual clock."
verify: "make ci test-integration e2e"
operator_attention: false
issue: 20
---

# S-020. Snapshot processing: Alerts, resolves, Gone, truncation, Continuation, staleness bookkeeping and the Alerts view (BE)

## Scope

**IN**

- The processing worker: claim per Integration, arrival order, one transaction per Snapshot, failed Snapshots.
- Snapshot semantics of ADR-0002: Alerts by fingerprint with Static labels, idempotency, explicit and repeated
  resolves, resolved Alertmanager groups, the duplicate window, Gone with its minimum absence, truncation,
  Continuations, annotation changes, Alerts in several `groupKey`s.
- Learning repeat intervals per Alertmanager route and the staleness bookkeeping (liveness clock values), without the
  Stale scan.
- Alert changes handed to a sink that routing (S-025) and grouping (S-028) consume.
- The Alerts view API, its Matcher filter and the processing metrics and log events.
- The fake Alertmanager's group model and the scenario library of the verified facts; the development clock of
  `muster dev`.

**OUT**

- Internal alerts, `MusterSnapshotTruncated`, the built-in Integration, replay, the effects of deleting an Integration,
  the learned routes API and the warnings (S-021).
- The Heartbeat, the advancing liveness clock, the Stale scan and the time-based end of truncation (S-023).
- Routing (S-025) and Alert Groups (S-028).

## Contracts

- **Operation implemented**: `listIntegrationAlerts` (`alerts:read`). Schemas: `IntegrationAlert`,
  `IntegrationAlertList`, `AlertState`, `NullableResolveReason`; parameters `LabelMatchers`, `state`, `q`, `sort`.
- **Worker** (C-06.FR-1, ADR-0006; `ingest_claims`, `stored_snapshots`): every replica runs one worker, woken by the
  `NOTIFY` of S-018 and polling every 5 seconds as a fallback. It works per Organization — it iterates over the
  Organizations (one in L1) and passes `org_id` to every claim and query (lint 1). It finds Integrations with pending
  Snapshots, leases one
  through its `ingest_claims` row (created with the Integration if missing) using the claim helper of S-013, and
  processes that Integration's pending Snapshots in `(received_at, id)` order, one transaction each, renewing the lease.
  Shutdown releases the lease. The Integration's `snapshot_count` and `last_snapshot_at` are updated in the same
  transaction.
- **Payload** (C-06.FR-20, `AlertmanagerWebhook`): `groupKey`, `status` and `alerts` are required, each Alert needs
  `labels`, `status` and `startsAt`; `truncatedAlerts`, `notification_reason` and `fingerprint` are optional. A body
  that is not JSON or lacks these fields, and an error raised by the processing code, mark the Stored Snapshot `failed`
  with `processing_error`; it is never retried by itself and never blocks the Snapshots behind it. A lost database
  connection rolls back and leaves the Snapshot pending. `group_key`, `alert_count` and `truncated_alerts` are written
  back.
- **Alerts** (C-06.FR-2, FR-3, FR-4, FR-11, FR-12; `alerts`): the fingerprint is the payload's, or — when absent —
  computed as Alertmanager does from the labels as received; Static labels are added afterwards, an Alert's own value
  winning and the label named in `static_label_conflicts`. Per Alert, against its row:

  | Listed as | Row state | Result | Alert change |
  |---|---|---|---|
  | firing | none; resolved with an older `startsAt`; resolved as Gone or Stale with the same `startsAt` | a new firing: `episode` + 1, `fired_at` now | `fired` |
  | firing | firing, same `startsAt` | `last_seen_at`, annotations refreshed | `annotations_changed` when they differ |
  | firing | firing, newer `startsAt` | Continuation: `starts_at` updated | `continued` |
  | firing | any, with a newer `startsAt` than listed; resolved by a `resolved` with the same `startsAt` | nothing (an old copy) | — |
  | resolved | firing, `startsAt` not older | resolved, reason `resolved` | `resolved` |
  | resolved | resolved, or firing with a newer `startsAt` | nothing, not counted | — |
  | resolved | no row | dropped, `muster_ingest_resolved_dropped_total` + 1 | — |

  Resolved Alerts never create rows. A Snapshot with `status: resolved` (C-06.FR-21) resolves every Alert still
  firing with an active presence in its `groupKey`, listed or not, as if each were listed as resolved. A late Snapshot
  — received before the current window of its `groupKey` started, as in a replay — applies only the `resolved` rows of
  the table and changes no presence, window or learned interval; this is what makes replay (S-021) change nothing.
- **Presence and the duplicate window** (C-06.FR-5, FR-6, FR-7; `alertmanager_groups`, `alert_presences`): a Snapshot of
  a `groupKey` received at t opens a new window unless t is within `duplicate_window_seconds` of `window_started_at`. A
  listed Alert is `listed` (`last_listed_window`, `last_seen_at`, `last_seen_clock_ms` = the Integration's liveness
  clock at receipt, as S-023 defines it; `missed_since` cleared). Absence is evaluated at receipt, and only when neither
  the Snapshot nor its window is truncated, for each active presence not listed in the current window: `listed` becomes
  `missed` with `missed_since` = the start of the current window; `missed` from an earlier window becomes `gone` once t
  − `missed_since` ≥ `processing.gone_min_absence`. An Alert resolves as Gone (reason `gone`, the text of C-06.FR-10)
  when every presence of its current firing is `gone` or `stale`.
- **Truncation** (C-06.FR-6): `truncatedAlerts > 0` sets `truncated` and `truncated_since` on the `groupKey` and
  `window_truncated` on its window, and counts in `muster_ingest_truncated_snapshots_total{integration}`; the next
  untruncated Snapshot clears them. `last_snapshot_clock_ms` is kept on every Snapshot so that S-023 can keep unlisted
  Alerts of a truncated `groupKey` alive while its Snapshots arrive, and end truncation after `stale_after`.
- **Repeat learning** (C-06.FR-8; `alertmanager_routes`): the route path is the part of `groupKey` before the group
  labels, parsed with quoted values respected. When a window opens with a Snapshot whose `notification_reason` is
  `repeat interval elapsed` — or, when the field is absent, whose fingerprints and statuses hash to the previous
  window's (`last_content_sha256`) — the gap since the previous window of that `groupKey` enters the route's ring of
  the last `processing.repeat_samples` gaps, and `learned_repeat_interval_ms` becomes their median. `stale_after` is
  `processing.stale_after_factor` times the interval, or `processing.stale_after_unlearned` before one is learned.
- **Alert changes** (C-06.FR-3): `fired`, `resolved` (reason and text), `continued` and `annotations_changed`, each with
  the Alert and the Stored Snapshot, go to a sink inside the Snapshot's transaction; in this story the sink does
  nothing, so the changes are recorded on `alerts` only. S-025 and S-028 attach routing and grouping.
- **Clock** (C-06.FR-13): windows, absence and gaps use `received_at`; `startsAt` and `endsAt` only order events of the
  same source and are shown.
- **Alerts view** (C-06.FR-19, `listIntegrationAlerts`): the Integration's `alerts` rows, firing and those resolved
  within `retention.alert_details`, with `alertmanager_groups` from their presences; `state`; `label` Matchers in
  Alertmanager syntax matched against each Alert's labels; `q` (case-insensitive over label values); sorted by
  `-last_seen_at` (default) or `starts_at` with a cursor on `(value, id)`. `route`, `severity_level` and `alert_group`
  stay absent until S-025 and S-028.
- **Matchers** (`internal/matchers`): parsing the Alertmanager matcher syntax (`=`, `!=`, `=~`, `!~`, RE2 anchored as in
  Alertmanager, a missing label counting as an empty value) and matching label sets; routing reuses it.
- **Metrics** (C-06.FR-15): `muster_ingest_processing_delay_seconds{integration}` (`le` buckets 10 ms to 10 min),
  `muster_ingest_backlog` (Leader: pending Snapshots), `muster_ingest_truncated_snapshots_total{integration}`,
  `muster_ingest_failed_snapshots_total{integration}`, `muster_alerts_resolved_total{integration,reason}` (all four
  reasons declared; `stale` from S-023, `integration_deleted` from S-021),
  `muster_ingest_resolved_dropped_total{integration}`.
- **Log events** (C-06.FR-15): exactly one per Snapshot — `snapshot_processed` (INFO: `integration`,
  `stored_snapshot`, `group_key`, `alerts`, `fired`, `resolved`, `gone`, `continued`, `dropped`, `truncated`,
  `duration_ms`) or `snapshot_failed` (WARN: `integration`, `stored_snapshot`, `error`).
- **Development clock**: in development mode only (`muster dev`, also with `--replica`), the internal listener serves
  `GET /_dev/clock` and `POST /_dev/clock` with `{"advance_seconds": N}`, both answering `{"now", "offset_seconds"}`.
  The business clock of `internal/clock` (S-006) is then real time plus the offset, and `now` is its time; the real
  clock is never offset. The offset lives in the database
  (`runtime_state.dev_clock_offset_seconds`, `internal/devmode/query.sql`), so every replica of one database runs on
  the same clock (the nightly two-replica run of S-004). A `POST` first runs the partition maintenance step itself — it
  is idempotent — so that rows written at the new time have their partitions, then adds N to the stored offset (an
  upsert of the singleton row) and sends `NOTIFY dev_clock` in the same transaction. Every replica in development mode
  listens on that channel through the hub of S-012 and reads the offset at start and on each notification, then wakes
  the workers, timers and Leader tasks that wait on the clock. The `POST` answers once its own replica runs on the new
  offset; a check that drives two replicas reads `GET /_dev/clock` on the other one until it shows the same
  `offset_seconds`. Outside development mode the path does not exist (`404`) and the column stays 0.
- **Clock consumers** (S-006): which of the two clocks each consumer reads, and so what an advance of the development
  clock moves. A story that adds a consumer of time follows this table.

  | Clock | Consumers |
  |---|---|
  | Business: moved by the development clock | domain timestamps (`received_at`, `fired_at`, Timeline and Audit log entries, `created_at`, `updated_at`); timers and every due time (`timers.deadline` for Snoozes, notices and Reminders, `oidc_checks.deadline`, the delivery worker's `next_attempt_at` and its limiter buckets); windows (duplicate, Reopen, Grace period, Thread batching, Storm, copy wait); retention and partition maintenance; sessions and the expiry of tokens and links; the Leader's alive mark, downtime and the recovery window |
  | Real: never moved | TOTP steps (S-012); ID-token `exp` and `iat` (S-013); outgoing webhook signatures and `webhook-timestamp` (S-044); row leases (`lease_until` of the claim helper, S-013) and the Leader lease (S-008); the clock skew check (S-008); replica key records and their liveness (S-007); HTTP timeouts and other waits on an external system |
- **Fake Alertmanager** (C-01.FR-13): a group model on top of the receivers of S-018:
  - `PUT /_fake/groups/{group}` `{"receiver", "route", "labels"}` defines an Alertmanager group and answers its
    `group_key` (`<route>:{<labels>}`, rendered as Alertmanager does);
  - `PUT /_fake/groups/{group}/alerts/{alert}` `{"labels", "annotations", "status", "starts_at", "ends_at",
    "fingerprint"}` sets an Alert (group labels merged in; `starts_at` defaults to now at its first firing and accepts
    `now`; a resolve sets `endsAt` to now); `DELETE` stops listing it without a resolve;
  - `POST /_fake/groups/{group}/notify` `{"reason", "list", "max_alerts", "status", "copies", "copy_delay_ms",
    "omit_reason"}` sends the group's Snapshot — all its Alerts in Alertmanager's order or only `list`, cut to
    `max_alerts` with `truncatedAlerts` counting what was left out (resolved Alerts included), `status` firing while any
    Alert of the group fires unless given, `copies` identical bodies — and answers `{"group_key", "sent": [{"status",
    "listed", "truncated"}]}`. Bodies carry `version`, `receiver`, `groupLabels`, `commonLabels`,
    `commonAnnotations`, `externalURL` and `routeLabels`.
  - `scenarios.go` holds one scenario per verified fact the processing relies on — F-033 to F-044, F-046 and F-048 to
    F-053 (F-045 and F-047 need the Heartbeat and join with S-023) — as timed request sequences; `facts_test.go` runs
    each through processing with a manual clock and asserts the requirement that cites the fact.
- **Defaults**: `processing.repeat_samples` (9 gaps, built in), as in defaults.md.

## Steps

1. Write the Matcher package and the payload parser. Check: unit tests cover the syntax, anchoring, missing labels and
   each payload error.
2. Write the worker with the claim, ordering, failure handling and counters. Check: integration tests with two workers
   show per-Integration order and exclusivity, and a failed Snapshot not blocking the next.
3. Write the per-Alert rules, resolved groups, Continuations and annotation changes. Check: a table test covers every
   row of the Alerts table above.
4. Write presence, the duplicate window, Gone, truncation and repeat learning. Check: tests with a manual clock cover
   C-06.AC-1, AC-2, AC-15 and the learned median.
5. Write the fake Alertmanager's group model, the scenario library and `facts_test.go`. Check: every listed fact passes.
6. Write the Alerts view, the metrics, the log events and the development clock. Check: Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
# the Admin's session as in S-011 (`jar`, `H`); an Integration "lab" with the Static label cluster=b, its token
# registered with the fake Alertmanager as the receiver "lab", as in S-018; its id in INT
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake; CLK=localhost:8082/_dev/clock
VIEW() { curl -s -b jar "$API/integrations/$INT/alerts?$1"; }
ADV() { curl -s -X POST $CLK -d "{\"advance_seconds\":$1}" > /dev/null; }
NOTIFY() { curl -s -X POST "$FAM/groups/$1/notify" -d "$2" > /dev/null; sleep 1; }

curl -s -X PUT $FAM/groups/g1 -d '{"receiver":"lab","route":"{}/{team=\"db\"}","labels":{"alertname":"DiskFull"}}' | jq -r .group_key
# {}/{team="db"}:{alertname="DiskFull"}
curl -s -X PUT $FAM/groups/g1/alerts/a -d '{"labels":{"instance":"db-a","cluster":"a"}}' > /dev/null
for x in b c; do curl -s -X PUT $FAM/groups/g1/alerts/$x -d "{\"labels\":{\"instance\":\"db-$x\"}}" > /dev/null; done
NOTIFY g1 '{"reason":"first notification","copies":2}'
VIEW 'state=firing' | jq -c '[.items[] | {i: .labels.instance, c: .labels.cluster, w: .static_label_warnings}] | sort_by(.i)'
# [{"i":"db-a","c":"a","w":["cluster"]},{"i":"db-b","c":"b","w":[]},{"i":"db-c","c":"b","w":[]}]     C-06.AC-8
psql "$MUSTER_DATABASE_URL" -Atc "SELECT count(*), max(episode) FROM alerts WHERE integration_id = (SELECT id FROM integrations WHERE name = 'lab')"
# 3|1                                                                  (the HA copy changed nothing)

# C-06.AC-2 and C-06.AC-1: a near-duplicate proves nothing; Gone needs a second window and the minimum absence
ADV 20;  NOTIFY g1 '{"reason":"repeat interval elapsed","list":["a","b"]}'
ADV 60;  NOTIFY g1 '{"reason":"repeat interval elapsed","list":["a","b"]}'
VIEW 'state=firing&label=instance%3D%22db-c%22' | jq '.items | length'                           # 1
ADV 300; NOTIFY g1 '{"reason":"repeat interval elapsed","list":["a","b"]}'
VIEW 'state=resolved&label=instance%3D%22db-c%22' | jq -c '.items[0] | {resolve_reason, resolve_reason_text}'
# {"resolve_reason":"gone","resolve_reason_text":"Alertmanager no longer reports this alert — it resolved without notice, was silenced or inhibited in Alertmanager, or the Alertmanager routing changed"}

# C-06.AC-15: a restarted Alertmanager lists 3, then 9, then 10 of 10
curl -s -X PUT $FAM/groups/g2 -d '{"receiver":"lab","route":"{}","labels":{"alertname":"PodDown"}}' > /dev/null
for i in $(seq 0 9); do curl -s -X PUT $FAM/groups/g2/alerts/x$i -d "{\"labels\":{\"pod\":\"p$i\"}}" > /dev/null; done
NOTIFY g2 '{"reason":"first notification"}'
L9='["x0","x1","x2","x3","x5","x6","x7","x8","x9"]'
ADV 600; NOTIFY g2 '{"reason":"first notification","list":["x0","x1","x2"]}'
ADV 60;  NOTIFY g2 "{\"reason\":\"new alerts added\",\"list\":$L9}"
ADV 60;  NOTIFY g2 '{"reason":"new alerts added"}'
VIEW 'state=firing&label=alertname%3D%22PodDown%22' | jq '.items | length'                       # 10
ADV 600; NOTIFY g2 "{\"reason\":\"repeat interval elapsed\",\"list\":$L9}"                         # T
ADV 60;  NOTIFY g2 "{\"reason\":\"repeat interval elapsed\",\"list\":$L9}"                         # T+60 s
VIEW 'state=firing&label=pod%3D%22p4%22' | jq '.items | length'                                  # 1
ADV 300; NOTIFY g2 "{\"reason\":\"repeat interval elapsed\",\"list\":$L9}"                         # T+6 min
VIEW 'state=resolved&label=pod%3D%22p4%22' | jq -r '.items[0].resolve_reason'                   # gone

# C-06.AC-4: Continuation
curl -s -X PUT $FAM/groups/g1/alerts/a -d '{"labels":{"instance":"db-a","cluster":"a"},"starts_at":"now"}' > /dev/null
NOTIFY g1 '{"reason":"repeat interval elapsed","list":["a","b"]}'
VIEW 'label=instance%3D%22db-a%22' | jq -r '.items[0].state'                                     # firing
psql "$MUSTER_DATABASE_URL" -Atc "SELECT episode FROM alerts WHERE labels->>'instance' = 'db-a'"  # 1

# C-06.AC-13: a resolved Alertmanager group resolves all its Alerts, listed or not
R0=$(curl -s localhost:8082/metrics | grep "muster_alerts_resolved_total{integration=\"$INT\",reason=\"resolved\"}" | awk '{print $2}')
curl -s -X PUT $FAM/groups/g3 -d '{"receiver":"lab","route":"{}","labels":{"alertname":"Batch"}}' > /dev/null
curl -s -X PUT $FAM/groups/g4 -d '{"receiver":"lab","route":"{}","labels":{"alertname":"Other"}}' > /dev/null
for i in $(seq 0 19); do curl -s -X PUT $FAM/groups/g3/alerts/y$i -d "{\"labels\":{\"pod\":\"q$i\"}}" > /dev/null; done
curl -s -X PUT $FAM/groups/g4/alerts/z -d '{"labels":{"pod":"z"}}' > /dev/null
NOTIFY g3 '{"reason":"first notification"}'; NOTIFY g4 '{"reason":"first notification"}'
for i in $(seq 0 19); do curl -s -X PUT $FAM/groups/g3/alerts/y$i -d "{\"labels\":{\"pod\":\"q$i\"},\"status\":\"resolved\"}" > /dev/null; done
NOTIFY g3 '{"reason":"all alerts resolved","list":["y0","y1","y2","y3","y4","y5"]}'
VIEW 'state=firing&label=alertname%3D%22Batch%22' | jq '.items | length'                         # 0
VIEW 'state=firing&label=alertname%3D%22Other%22' | jq '.items | length'                         # 1
curl -s localhost:8082/metrics | grep "muster_alerts_resolved_total{integration=\"$INT\",reason=\"resolved\"}" | awk -v r=$R0 '{print $2 - r}'
# 20

# C-06.AC-14 and C-06.AC-10: repeated resolves change nothing; an unknown fingerprint is dropped and counted
D0=$(curl -s localhost:8082/metrics | grep "muster_ingest_resolved_dropped_total{integration=\"$INT\"}" | awk '{print $2}')
NOTIFY g3 '{"reason":"all alerts resolved","list":["y0"]}'; NOTIFY g3 '{"reason":"all alerts resolved","list":["y0"]}'
curl -s -X PUT $FAM/groups/g3/alerts/y20 -d '{"labels":{"pod":"q20"}}' > /dev/null
NOTIFY g3 '{"reason":"new alerts added","list":["y0","y20"]}'; NOTIFY g3 '{"reason":"repeat interval elapsed","list":["y0","y20"]}'
VIEW 'label=pod%3D%22q0%22' | jq -r '.items[0].state'                                            # resolved
curl -s -X PUT $FAM/groups/g3/alerts/ghost -d '{"labels":{"pod":"ghost"},"status":"resolved"}' > /dev/null
NOTIFY g3 '{"reason":"some alerts resolved","list":["ghost","y20"]}'
curl -s localhost:8082/metrics | grep "muster_ingest_resolved_dropped_total{integration=\"$INT\"}" | awk -v d=$D0 '{print $2 - d}'
# 1

# C-06.AC-12: a body that is not JSON fails, alone
curl -s -X POST $FAM/send -d '{"receiver":"lab","raw":"not json","content_type":"text/plain"}' > /dev/null; sleep 1
curl -s -b jar "$API/stored-snapshots?integration=$INT&state=failed" | jq -c '.items[0] | {state, processing_error}'
# {"state":"failed","processing_error":"the body is not valid JSON: …"}
grep -c '"event":"snapshot_failed"' dev.log                                                      # 1
NOTIFY g4 '{"reason":"repeat interval elapsed"}'
curl -s -b jar "$API/stored-snapshots?integration=$INT&limit=1" | jq -r '.items[0].state'        # processed

# C-06.AC-9: 200 Alerts, one line
curl -s -X PUT $FAM/groups/g5 -d '{"receiver":"lab","route":"{}","labels":{"alertname":"Wide"}}' > /dev/null
for i in $(seq 1 200); do curl -s -X PUT $FAM/groups/g5/alerts/w$i -d "{\"labels\":{\"n\":\"$i\"}}" > /dev/null; done
N0=$(grep -c '"event":"snapshot_processed"' dev.log); NOTIFY g5 '{"reason":"first notification"}'
echo $(( $(grep -c '"event":"snapshot_processed"' dev.log) - N0 ))                                # 1

# truncation and repeat learning (the API for both is S-021)
NOTIFY g2 '{"reason":"repeat interval elapsed","max_alerts":2}'
psql "$MUSTER_DATABASE_URL" -Atc "SELECT truncated FROM alertmanager_groups WHERE group_key LIKE '%PodDown%'"   # t
curl -s -X PUT $FAM/groups/g6 -d '{"receiver":"lab","route":"{}/{team=\"web\"}","labels":{"alertname":"Slow"}}' > /dev/null
curl -s -X PUT $FAM/groups/g6/alerts/s -d '{"labels":{"host":"web-1"}}' > /dev/null
NOTIFY g6 '{"reason":"first notification"}'
for k in 1 2 3; do ADV 300; NOTIFY g6 '{"reason":"repeat interval elapsed"}'; done
psql "$MUSTER_DATABASE_URL" -Atc "SELECT learned_repeat_interval_ms / 1000 FROM alertmanager_routes WHERE route_path = '{}/{team=\"web\"}'"
# 30x                                                                  (300 s plus the seconds the commands took)
curl -s -b jar "$API/integrations/$INT" | jq '.snapshot_count > 0'                               # true
```

`make test-integration` runs `facts_test.go`; the pull request records which fact each scenario reproduces.

## Open questions

None.

## Notes

- Suggested commit: `feat(ingest): process snapshots into alerts with gone, truncation and continuation semantics`.
- The development clock is the "virtual clock" that the L1 conventions allow in acceptance checks; later stories use
  it for Stale, Reopen windows, Grace periods, Snoozes and retention. Advancing it by more than
  `auth.session_idle_timeout` ends sessions, so long checks use a Personal access token.
- `processing.repeat_samples` is the size of the ring that `design/db/schema.md` §4.7 calls "a short ring of observed
  gaps".

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-06.FR-1 | full | |
| C-06.FR-2 | full | |
| C-06.FR-3 | partial | Static labels and Alert changes; the warning in the Timeline is S-028 |
| C-06.FR-4 | partial | resolves and drops; resolving in the latest Alert Group is S-028 |
| C-06.FR-21 | full | |
| C-06.FR-5 | full | |
| C-06.FR-6 | partial | truncation by Snapshot and its counter; the Internal alert is S-021, the time-based end S-023 |
| C-06.FR-7 | partial | several `groupKey`s; Stale in each of them is S-023 |
| C-06.FR-8 | partial | learning and bookkeeping; the Stale scan is S-023 |
| C-06.FR-10 | partial | the Gone reason; the Stale reason is S-023 |
| C-06.FR-11 | full | |
| C-06.FR-12 | partial | the Alert change; the Quiet Timeline entry is S-028, the Root message update follows from C-11.FR-20 |
| C-06.FR-13 | full | |
| C-06.FR-15 | partial | the log line and metrics; the reasons `stale` and `integration_deleted` are counted by S-023 and S-021 |
| C-06.FR-19 | partial | the API; Route and Severity level S-025, Alert Group S-028, the page S-022 |
| C-06.FR-20 | partial | failed Snapshots; replay is S-021 |
| C-06.AC-1 | full | |
| C-06.AC-2 | full | |
| C-06.AC-4 | full | |
| C-06.AC-8 | partial | the API; the page is S-022 |
| C-06.AC-9 | full | |
| C-06.AC-10 | partial | the dropped `resolved`; the deletion count is S-021 |
| C-06.AC-12 | full | |
| C-06.AC-13 | full | |
| C-06.AC-14 | full | |
| C-06.AC-15 | full | |
| C-05.FR-4 | full | together with S-018 |
| C-05.FR-6 | full | together with S-018 |
| C-05.FR-7 | partial | the count and the processing states; the later sections are S-022 and S-024 |
| C-05.AC-8 | full | together with S-018 |
| C-01.FR-13 | partial | the fake Alertmanager's group model, the fact scenarios and the development clock |
