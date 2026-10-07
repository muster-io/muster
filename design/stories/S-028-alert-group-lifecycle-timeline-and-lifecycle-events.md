---
id: S-028
title: "Alert Group lifecycle: grouping, state machine, system transitions, Timeline and lifecycle events (BE)"
capability: C-09
kind: be
layer: L1
depends_on: [S-026]
covers: [C-09.FR-1, C-09.FR-2, C-09.FR-23, C-09.FR-3, C-09.FR-4, C-09.FR-5, C-09.FR-6, C-09.FR-7, C-09.FR-9, C-09.FR-10, C-09.FR-11, C-09.FR-12, C-09.FR-18, C-09.FR-19, C-09.FR-22, C-09.FR-14, C-09.AC-1, C-09.AC-2, C-09.AC-4, C-09.AC-6, C-09.AC-7, C-09.AC-8, C-09.AC-9, C-09.AC-10, C-09.AC-11, C-09.AC-12, C-09.AC-14, C-09.AC-20, C-09.AC-21, C-09.AC-22, C-08.FR-4, C-08.FR-6, C-08.FR-8, C-08.FR-9, C-06.FR-3, C-06.FR-4, C-06.FR-19, C-05.FR-8, C-02.FR-12, C-06.FR-12]
files_touched:
  - internal/groups/dispatcher.go
  - internal/groups/grouping.go
  - internal/groups/transitions.go
  - internal/groups/events.go
  - internal/groups/title.go
  - internal/groups/move.go
  - internal/groups/downtime.go
  - internal/groups/read.go
  - internal/groups/query.sql
  - internal/groups/dispatcher_test.go
  - internal/groups/grouping_test.go
  - internal/groups/transitions_test.go
  - internal/groups/events_test.go
  - internal/groups/title_test.go
  - internal/groups/groups_integration_test.go
  - internal/timers/worker.go
  - internal/timers/query.sql
  - internal/timers/worker_test.go
  - internal/ingest/changes.go
  - internal/ingest/alertsview.go
  - internal/ingest/alertsview_test.go
  - internal/ingest/query.sql
  - internal/ingest/worker.go
  - internal/ingest/worker_test.go
  - internal/ingest/deletion.go
  - internal/ingest/stale.go
  - internal/routing/routes.go
  - internal/routing/query.sql
  - internal/routing/routes_test.go
  - internal/api/alertgroups.go
  - internal/api/alertgroups_test.go
  - internal/api/integrations.go
  - internal/api/routes.go
  - internal/api/server.go
  - internal/api/problem.go
  - internal/leader/alive.go
  - internal/leader/alive_test.go
  - internal/leader/tasks.go
  - internal/leader/leader_test.go
  - internal/metrics/catalogue.go
  - internal/logging/events.go
  - internal/devmode/devmode.go
  - internal/devmode/devmode_test.go
  - internal/runtime/runtime.go
  - internal/runtime/runtime_test.go
  - api/openapi.yaml
  - sqlc.yaml
  - test/e2e/lifecycle_test.go
  - test/e2e/routing_test.go
acceptance:
  - "[C-09.FR-3, C-09.AC-6, C-08.FR-4] Alerts with the same Group key values on one Route join one Alert Group, a missing label counting as an empty value; an Alert with another `cluster` starts a second one."
  - "[C-09.FR-1, C-09.FR-2] Each Alert Group gets the next `#N` of the Organization in the transaction that creates it — without gaps, also when that transaction rolls back — and an opaque `public_id`; `getAlertGroup` returns its status, Route, Integrations, title, `summary`, Severity level, Urgent flag, start, counts and Reopen count."
  - "[C-09.FR-23, C-09.AC-20] The title is the shared `alertname`; in a Route whose Group key lacks `alertname`, an Alert with another `alertname` joining changes the title once to the Group key values, and later Alerts and resolutions leave it; the `summary` is the first `summary` annotation and never changes."
  - "[C-09.FR-4, C-09.AC-1, C-09.AC-22] After the system resolves an Alert Group, an Alert with the same Route and Group key values firing within `route.reopen_window` — one of its own or another fingerprint — reopens the same `#N` as firing with Reopen count 1 and a `reopened` entry with `loudness` `loud` and `mentions` `[reopen]`; no new Alert Group starts."
  - "[C-09.AC-2] The same after the Reopen window has passed starts a new `#N`, firing."
  - "[C-09.FR-7, C-09.AC-4] A new Alert differing from a firing one only in `pod` is recorded as `alert_replaced` naming `pod`, with `loudness` `quiet`, and the Alert Group carries the notice `replacement` with the label."
  - "[C-09.FR-11, C-09.AC-10, C-06.FR-3] A new `startsAt` without `resolved` on an open Alert Group records `alert_continued` and no Reopen or new firing; an annotation change records `annotations_changed`; a Static label conflict is listed on the `alerts_added` entry."
  - "[C-09.FR-9, C-08.FR-6, C-09.AC-12] In a Route whose Group key lacks `severity`, a critical Alert joining a firing warning Alert Group makes it critical and Urgent and records a Quiet `urgency_raised`; a rise that leaves urgency unchanged records a Quiet `severity_raised`."
  - "[C-09.FR-10, C-09.FR-3, C-06.FR-4] When its last Alert resolves the system resolves the Alert Group with that Alert's reason, shown in `resolution`; a `resolved` applies to the latest Alert Group in which the fingerprint fires."
  - "[C-08.FR-8, C-09.AC-7, C-09.AC-8] Changing a Route's Matchers leaves an already grouped fingerprint in its open Alert Group, and marking the Route urgent changes the status of none of its open Alert Groups."
  - "[C-09.FR-19, C-08.FR-9, C-09.AC-9] Deleting a Route with open Alert Groups answers 409 `route-has-open-alert-groups` with their count; `moveOpenAlertGroups` moves them to the Default route with a `moved_to_default_route` entry each, and the deletion then succeeds; a moved Alert Group takes no new Alert and stays open beside an Alert Group of the Default route with the same key values."
  - "[C-09.FR-19, C-08.FR-9] A Route deletion racing a Snapshot whose Alert starts an Alert Group on that Route ends one of two ways and never leaves an open Alert Group on a deleted Route: the Snapshot locks the Route first and the deletion answers 409 `route-has-open-alert-groups`, or the deletion commits first and the Alert is grouped on the Default route (an integration test with two connections)."
  - "[C-09.FR-18, C-02.FR-12, C-09.AC-11] After Muster was stopped for 10 minutes and started again, every open Alert Group's Timeline shows a `muster_unavailable` entry with the period."
  - "[C-09.FR-3, C-05.FR-8, C-09.AC-14] Deleting an Integration resolves its open Alert Groups with the reason \"Integration {name} deleted\"."
  - "[C-09.FR-22, C-09.AC-21] Every row of the lifecycle event table of C-09.FR-22 that this capability can produce records exactly one Timeline entry with the row's `event`, kind, `loudness` and `mentions` (a table-driven test that also covers the rows reached only through Acknowledge and Snooze, set up directly)."
  - "[C-09.FR-14, C-09.FR-11, C-06.FR-19] `listAlertGroupAlerts` lists firing Alerts first, then resolved ones with their reason; `getAlertGroupTimeline` returns the entries newest first or oldest first, filtered by kind, each with its actor, Transport and, for a lifecycle event, `event`, `loudness` and `mentions`; the Alerts view names the Alert Group of each Alert."
  - "[C-09.FR-6] New Alerts joining a firing Alert Group record one `alerts_added` per Snapshot with `loudness` `loud` and `mentions` `[new_alerts]`; joining an acknowledged or snoozed one records it Quiet (rows set up directly here, verified with Acknowledge and Snooze in S-032)."
  - "[C-09.FR-5] Within the Grace period of a person-resolved Alert Group an Alert with the same key starts a new Alert Group at once, and Alerts still firing when it ends join that open Alert Group, or else start one marked as firing again after the manual resolve (rows set up directly here, verified with Resolve in S-032)."
  - "[C-09.FR-12] Reopen window and Grace period ends are timer rows that any replica claims; after downtime each overdue one fires once; in development mode an advance of the development clock past a deadline fires it at once on every replica, without waiting for the worker's next wake."
verify: "make ci test-integration e2e"
operator_attention: false
issue: 28
---

# S-028. Alert Group lifecycle: grouping, state machine, system transitions, Timeline and lifecycle events (BE)

## Scope

**IN**

- The dispatcher of ADR-0016 with the system transitions, and the Timeline entries of the lifecycle event table.
- Grouping of newly firing Alerts by Route and Group key, Reopen, the Grace period rules, Replacements, Continuations,
  annotation changes, Severity levels and urgency of Alert Groups, the title and `summary`.
- Resolution by the system, also when an Integration is deleted; Timeline entries for Muster's downtime.
- The timer worker with the Reopen window and Grace period ends.
- Refusing to delete a Route with open Alert Groups, and moving them to the Default route.
- Reading one Alert Group, its Alerts and its Timeline; the Alert Group of each Alert in the Alerts view.

**OUT**

- The list, search, counts, related Alert Groups, statistics, retention and live hints (S-029); the pages (S-030).
- Commands and `allowed_commands` (S-032); Notes and Snooze ends (S-063); the acknowledged and snoozed variants of
  Reopen, new Alerts and the rise to Urgent are written here and verified with Acknowledge and Snooze in S-032.
- Delivery, its re-render and its Timeline entries (S-034 on); ack timeouts, Reminders and Unclaimed (S-049).

## Contracts

- **Operations implemented**: `getAlertGroup`, `listAlertGroupAlerts`, `getAlertGroupTimeline`, `moveOpenAlertGroups`;
  `deleteRoute` gains the refusal and `Route.open_alert_group_count` its value; `IntegrationAlert.alert_group` is
  filled. Schemas: `AlertGroup`, `AlertGroupStatus`, `ResolvedBy`, `ResolverKind`, `AlertGroupNotice`,
  `AlertGroupAlert(List)`, `TimelineEntry` with `TimelineStatusEntry`, `TimelineAlertsEntry`, `TimelineSystemEntry`
  (and `TimelineNoteEntry`, `TimelineDeliveryEntry` merged from their tables, empty until S-063 and S-034),
  `TimelineEntryList`, `TimelineActor`, `LifecycleEvent`, `Loudness`, `MentionName`, `TimelineKind`,
  `MovedAlertGroups`.
- **Dispatcher** (ADR-0004, ADR-0016; `internal/groups`): every change of an Alert Group passes through one function
  with the steps precondition → transition → Audit log → Timeline → re-render. It runs on Alert Group rows its caller
  locked (`SELECT … FOR UPDATE`), all of a transaction's rows in id order, and in the caller's transaction
  (processing's per Snapshot, a timer's, an API request's). System
  transitions run as the actor `system` with the Transport `system`, need no Permission and write no Audit log entry;
  S-032 adds the Permission and Audit log steps of Commands. Re-render is a hook that delivery fills from S-034. Only
  `groups` writes `alert_groups`, `alert_group_alerts`, `timeline_entries` and `alert_group_counters` (lint 2).
- **Ingest seam** (`internal/ingest/changes.go`, `worker.go`; `internal/runtime/runtime.go`): today the processing
  worker's `ingest.Sink` is the bare Router (`runtime.go`, `configureWorker`, for the worker and the Stale scan alike).
  This story wires a Sink chain there: routing first (`routing.Router.AlertChanges`, which stamps `alerts.route_id`),
  then grouping (`groups`), both in the Snapshot's transaction, so an error of either rolls the Snapshot back.
  `ingest.Routed` gains the Alert Groups the Snapshot created or changed, as their `#N`, which `snapshot_processed`
  logs as `alert_groups` (`worker.go`), and `Committed`, which counts and logs what grouping did once the Snapshot's
  transaction committed; `deletion.go` keeps the result of the Sink and the Stale scan (`stale.go`) runs `Committed`
  after its commit. The recording Sink and the assertions on `Routed` in `worker_test.go` change with the new fields.
- **Grouping** (C-09.FR-3, FR-4, FR-5, FR-6; ADR-0003): processing hands the Alert changes of a Snapshot, after
  routing, to `groups`. Before it creates or joins an Alert Group on Route R, grouping locks R with `SELECT … FROM
  routes WHERE org_id = … AND id = … AND deleted_at IS NULL FOR SHARE` (`internal/groups/query.sql`). The Router read
  its routing stamp without a lock (`internal/routing/evaluate.go`), and inserting `alert_groups.route_id` only takes
  a key-share lock, which never waits for `LockRoute`'s `FOR NO KEY UPDATE`; the share lock does, so a concurrent
  `deleteRoute` either waits for the Snapshot's transaction and then sees its Alert Group, or commits first. When the
  lock finds no row — R was deleted meanwhile — the Alert is grouped on the Default route, as if it had been routed
  there, and its `alerts.route_id` stamp is set to the Default route in the same transaction through routing. A `fired` Alert on Route R with key values V — R's Group key evaluated on its labels now, a
  missing label as `""`, `group_key_sha256` over the labels and values — goes, in order:
  1. into the open Alert Group of (R, V) if there is one (`alert_groups_open_key`, which leaves out Alert Groups moved
     to the Default route);
  2. else into a system-resolved Alert Group of (R, V) whose `reopen_deadline` is still ahead: a Reopen into its
     `prior_status` (FR-4) — acknowledged with the same Owner, snoozed while the Snooze end is still ahead, otherwise
     firing — with the Reopen count increased;
  3. else into a new Alert Group with the next `#N`, a new `public_id`, status firing. `#N` comes from
     `alert_group_counters` in the same transaction: `INSERT … ON CONFLICT (org_id) DO NOTHING` creates the
     Organization's row on first use — no start-up step creates it, since only `groups` writes the table (lint 2) —
     and `UPDATE … SET last_number = last_number + 1 … RETURNING` takes the number.
  Locks are taken in one order: the Routes `FOR SHARE`, then the counter row `FOR UPDATE` whenever Alerts are grouped
  (so creations and Reopens, which make an Alert Group open, never race for one key), then the Alert Groups in id
  order; a target that a concurrent change resolved or moved after the lookup is looked up again.
  A person-resolved Alert Group of (R, V) inside its Grace period is never reopened: rule 3 starts a new one at once
  (FR-5). An Alert that lives in an open Alert Group (`alert_group_alerts` row `firing`) is neither routed nor grouped
  again (C-08.FR-8); its later changes apply there. Alerts of one Snapshot that start or join the same Alert Group are
  recorded together, as one `created` or one `alerts_added` entry listing their fingerprints and Static label conflicts
  (`label_conflicts`).
- **Resolution** (C-09.FR-3, FR-10; C-06.FR-4): a `resolved` change ends the Alert's firing membership in the latest
  Alert Group where it fires (`alert_resolved`, Quiet, while others still fire). When the last firing Alert resolves,
  the system resolves the Alert Group with that Alert's reason and text (`resolved`, Quiet), ending any Snooze, and
  records `prior_status` with the Owner and Snooze, `reopen_deadline` = now + `route.reopen_window` and a
  `reopen_window_end` timer — except on an Alert Group moved to the Default route, which gets no Reopen window. The
  reason `integration_deleted` of S-021 resolves with "Integration {name} deleted".
- **Grace period** (C-09.FR-5): when S-032's Resolve closes an Alert Group, the Alerts still firing stay as `firing`
  memberships (the "N alerts still firing" count), `grace_deadline` = now + `route.grace_period`, and a
  `grace_period_end` timer is set. When it fires and some of them still fire, they are grouped again on the Alert
  Group's Route under its current Group key, in the order of the grouping rules above, and leave (`moved`) for the
  result: the open Alert Group of (R, V) — typically one that a new Alert started within the Grace period — as
  `alerts_added`; a Reopen of a system-resolved one inside its window; or a new Alert Group marked
  `firing_again_after_id`, `created`, Loud. A Continuation is never a new firing.
- **Changes of members** (C-09.FR-7, FR-11; C-06.FR-11, FR-12): a Continuation updates the membership's `starts_at` and
  records `alert_continued`; an annotation change records `annotations_changed`; both Quiet, the annotation change
  first when one Snapshot has both. A new Alert that differs
  from a firing Alert of the same Alert Group only in labels of `organization.instance_labels` records `alert_replaced`
  naming the differing label (`replaced_label`) instead of `alerts_added`.
- **Severity and urgency** (C-08.FR-6, C-09.FR-9): an Alert Group's Severity level is the highest of its firing
  Alerts — lowered with the `alert_resolved` that resolves the highest, and taken from the new Alerts on a Reopen; it
  is Urgent when its Route is urgent, or its level is critical and `organization.critical_is_urgent` is on, judged on
  the Route and settings as they are at the rise. A rise that leaves urgency unchanged records `severity_raised`; a rise
  that makes it Urgent records `urgency_raised`:
  Quiet when it removes nothing; when snoozed — unless `snoozed_while_urgent` — it ends the Snooze, and when
  acknowledged with `route.urgent_rise_removes_ack` on it removes the Owner, both Loud with `[owner, rise_to_urgent]`.
  Configuration edits never change a status.
- **Title and summary** (C-09.FR-23; `title`, `title_from_group_key`, `summary`): the title is the `alertname` the
  Alerts share; the first Alert with another `alertname` switches it once to the Group key values
  (`cluster=prod, namespace=payments`); the `summary` is the `summary` annotation of the first Alert that has one, set
  once. `common_labels`, `common_annotations` and `integration_ids` are recomputed when an Alert joins.
- **Lifecycle events** (C-09.FR-22): the table of C-09.FR-22 is a closed list in `events.go` — event, Timeline kind,
  loudness and Mentions per variant — and every transition records exactly one entry with `event_seq` taken from
  `alert_groups.event_seq`; the `CHECK`s of `timeline_entries` back it. Mentions are symbolic.
- **Timers** (C-09.FR-12; `timers`, `internal/timers`): a worker on every replica claims due rows with
  `FOR UPDATE SKIP LOCKED` and a lease through the claim helper of S-062, waking at the earliest deadline of a free row
  (business clock) or the end of the earliest held lease (real clock) and on `NOTIFY`; overdue rows after downtime
  fire once. A worker claims only the kinds it has handlers for, and a failed timer fires again after its lease;
  `timer_failed` logs the failure. The worker, like the downtime step below, runs per Organization: it
  iterates over the Organizations (one in L1) and passes `org_id` to every claim and query (lint 1). Kinds here: `reopen_window_end` (clears `reopen_deadline` and the
  `prior_*` columns, no entry) and `grace_period_end`. S-063, S-035 and S-049 register their kinds. The worker also
  wakes when the development clock moves: today a clock move wakes only the ingest worker (the single `changed`
  callback of `devmode.NewClock`) and, through `leader.Wakes`, the Heartbeat check and the Stale scan. This story turns
  the callback into a fan-out — `devmode.NewClock` takes any number of wake functions, called after each change of the
  offset on every replica — and `runtime.go` registers the ingest worker and the timers worker; S-034 adds the
  delivery worker. A worker woken this way re-reads its earliest deadline on the business clock.
- **Downtime** (C-09.FR-18, C-02.FR-12): when the Leader records a downtime (S-008), every open Alert Group gets a
  `system` entry `muster_unavailable` with `period_from` and `period_to`, in the takeover's transaction
  (`leader.Alive.OnDowntime`, `alive.go`).
- **Route deletion and the move** (C-09.FR-19, C-08.FR-9): `Route.open_alert_group_count` counts its open Alert Groups;
  `deleteRoute` of a Route with any answers `409` `route-has-open-alert-groups` with `open_alert_group_count`.
  `routing.Service.Delete` counts them inside its transaction after `LockRoute` (a count query on `alert_groups` in
  `internal/routing/query.sql`, read-only, so lint 2 holds) and returns a domain error carrying the count, which
  `internal/api/problem.go` maps to the problem type with `open_alert_group_count`; counting after the lock is what
  makes the grouping lock above effective. The fake store of `internal/routing/routes_test.go` gains the count.
  `moveOpenAlertGroups` (`routes:write`) moves each of them to the Default route as a system transition
  `moved_to_default_route` (Quiet, kind `system`) and writes one Audit log entry `route.open_alert_groups_moved` with
  the count. A moved Alert Group keeps its Group key labels, values and `group_key_sha256` and gets
  `moved_from_route_id`, which leaves it out of `alert_groups_open_key` and of grouping: it never collides with an open
  Alert Group of the Default route, never takes new Alerts and never gets a Reopen window, and its own Alerts stay in
  it until they resolve (C-09.FR-19, `design/db/schema.md` §4.9).
- **Reads** (C-09.FR-14, FR-10, FR-11; `alerts:read` for the Alerts, `alert-groups:read` otherwise): `getAlertGroup`
  with `resolution` (by, actor, reason, reason code), `group_labels`, `common_labels`, `common_annotations` and
  `notices` — `alerts_still_firing` (the count after a person's resolve), `replacement` (the label of the latest
  `alert_replaced`), `firing_again_after_manual_resolve` (the earlier `#N`); `owner`, `snooze_until`, `snoozed_by` and
  `allowed_commands` from S-032, `details_removed` and `label_values` from S-029, `delivery_problem` and `unclaimed`
  `false` and `links` empty until their stories. `listAlertGroupAlerts`: firing first, then resolved, each with its
  Integration, labels, annotations of the membership, `starts_at`, reason, last seen, Alertmanager groups and
  `source_url` (`generatorURL`), filtered by `state`. `getAlertGroupTimeline`: newest first by default, `order=asc`,
  `kind` filter, cursor on `(at, id)`, `notes` and `delivery_events` merged by time.
- **Metrics** (reference.md, C-09): `muster_alert_groups{route,status}` (Leader gauge),
  `muster_alert_groups_created_total{route}`, `muster_alert_groups_reopened_total{route}`,
  `muster_alert_groups_resolved_total{route,by}`, `muster_alert_group_time_to_resolve_seconds{route}` (`le` buckets
  1 minute to 24 hours, observed at each resolution from the start).
- **Log events**: `alert_group_status_changed` (INFO: `group` as `#N`, `route`, `from`, `to`, `reason`, `transport`),
  `alert_continued` (INFO: `group`, `fingerprint`), `timer_failed` (WARN: `kind`, `error`); `snapshot_processed` gains
  `alert_groups` (the `#N` touched). `muster_alert_groups` is the Leader task `alert_group_gauges` (`tasks.go`).
- **Wiring** (`internal/api/server.go`, `sqlc.yaml`): the four operations join the implemented-operations map, and the
  API `Config` and `Server` gain the `groups` service; `sqlc.yaml` gains the entries for `internal/groups/query.sql`
  and `internal/timers/query.sql`. The Alerts view reads the Alert Group of each Alert with its own read-only query
  (`internal/ingest/query.sql`), which `integrationAlertOf` maps (`internal/api/integrations.go`).
- **Specification** (`api/openapi.yaml`): `TimelineStatusEntry` gains `fingerprints` and `label_conflicts`, which the
  `created`, `reopened` and `resolved` entries carry (one entry per Snapshot lists its Alerts).

## Steps

1. Write the dispatcher, the lifecycle event table and the Timeline writer. Check: `events_test.go` covers every row of
   C-09.FR-22 with its kind, loudness and Mentions.
2. Write grouping, `#N`, the title and the Alert Group's labels. Check: tests cover joining, a second key, a missing
   label, a rolled-back creation and the title switch.
3. Write the system transitions: resolution, Reopen into each prior status, Replacement, Continuation, annotation
   changes, severity and urgency. Check: transition tests with a manual clock and rows set up directly.
4. Write the timer worker, the Grace period end and the Reopen window end, and the wake fan-out of the development
   clock. Check: tests with two workers claim each row once and fire overdue rows once; a clock advance wakes the
   worker.
5. Write the downtime entries, the deletion effects, the Route lock, the Route refusal and the move. Check: tests cover
   C-09.AC-9, AC-11 and AC-14, and the integration test races a deletion against a Snapshot.
6. Write the reads, the Alerts view field, the metrics and log events. Check: Verification below.

## Verification

```sh
make dev &
# as in S-020 and S-025: the session `H`, an Integration "grp" without Static labels (id INT) with its fake receiver
# "grp", the helpers VIEW, ADV and NOTIFY, and the On-call policy in P
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake
AG() { VIEW "label=$1" | jq -r '.items[0].alert_group.id'; }
LAST() { curl -s -b jar "$API/alert-groups/$1/timeline?limit=1" | jq -c '.items[0] | {event, loudness, mentions}'; }
ROUTE() { curl -s "${H[@]}" $API/routes -d "{\"name\":\"$1\",\"matchers\":[{\"label\":\"team\",\"op\":\"=\",\"value\":\"$2\"}],
  \"urgent\":false,\"group_key\":$3,\"destination_ids\":[],\"policy\":$P}" | jq -r .id; }
RDB=$(ROUTE db db '["alertname","cluster"]'); RNET=$(ROUTE net net '["cluster"]')
PUTA() { curl -s -X PUT "$FAM/groups/$1/alerts/$2" -d "$3" > /dev/null; }

# C-09.AC-6: one Alert Group per key; a missing label is an empty value
curl -s -X PUT $FAM/groups/d1 -d '{"receiver":"grp","route":"{}","labels":{"alertname":"DiskFull"}}' > /dev/null
PUTA d1 i1 '{"labels":{"team":"db","cluster":"a","pod":"i1","severity":"warning"},"annotations":{"summary":"Disk on i1 is full"}}'
PUTA d1 i2 '{"labels":{"team":"db","cluster":"a","pod":"i2","severity":"warning"}}'
PUTA d1 j1 '{"labels":{"team":"db","cluster":"b","pod":"j1","severity":"warning"}}'
NOTIFY d1 '{"reason":"first notification"}'
G1=$(AG 'pod%3D%22i1%22'); G2=$(AG 'pod%3D%22j1%22')
[ "$G1" = "$(AG 'pod%3D%22i2%22')" ] && [ "$G1" != "$G2" ] && echo ok                                     # ok
curl -s -b jar "$API/alert-groups/$G1" | jq -c '{number, title, summary, status, severity_level, urgent, r: .route.name, f: .firing_alert_count}'
# {"number":1,"title":"DiskFull","summary":"Disk on i1 is full","status":"firing","severity_level":"warning","urgent":false,"r":"db","f":2}
curl -s -b jar "$API/alert-groups/$G1/timeline" | jq -c '[.items[] | {event, loudness, mentions, n: (.fingerprints | length)}]'
# [{"event":"created","loudness":"loud","mentions":["new_alert_group"],"n":2}]

# C-09.AC-4: a Replacement differing only in pod
curl -s -X DELETE $FAM/groups/d1/alerts/i2 > /dev/null
PUTA d1 i2b '{"labels":{"team":"db","cluster":"a","pod":"i2b","severity":"warning"}}'
NOTIFY d1 '{"reason":"new alerts added"}'
curl -s -b jar "$API/alert-groups/$G1/timeline?limit=1" | jq -c '.items[0] | {event, loudness, replaced_label}'
# {"event":"alert_replaced","loudness":"quiet","replaced_label":"pod"}
curl -s -b jar "$API/alert-groups/$G1" | jq -c '[.notices[] | {kind, label}]'                     # [{"kind":"replacement","label":"pod"}]

# C-09.AC-10: a Continuation
PUTA d1 i1 '{"labels":{"team":"db","cluster":"a","pod":"i1","severity":"warning"},"starts_at":"now"}'
NOTIFY d1 '{"reason":"repeat interval elapsed"}'
LAST $G1                                                       # {"event":"alert_continued","loudness":"quiet","mentions":[]}

# C-09.AC-12: a rise to critical makes the firing Alert Group Urgent, quietly
PUTA d1 i3 '{"labels":{"team":"db","cluster":"a","pod":"i3","severity":"critical"}}'
NOTIFY d1 '{"reason":"new alerts added"}'
curl -s -b jar "$API/alert-groups/$G1" | jq -c '{severity_level, urgent, status}'               # {"severity_level":"critical","urgent":true,"status":"firing"}
LAST $G1                                                       # {"event":"urgency_raised","loudness":"quiet","mentions":[]}

# C-09.AC-20: the title switches once in a Route whose key lacks alertname
curl -s -X PUT $FAM/groups/n1 -d '{"receiver":"grp","route":"{}","labels":{"alertname":"LinkDown"}}' > /dev/null
curl -s -X PUT $FAM/groups/n2 -d '{"receiver":"grp","route":"{}","labels":{"alertname":"HighLatency"}}' > /dev/null
PUTA n1 l1 '{"labels":{"team":"net","cluster":"x","if":"eth0"},"annotations":{"summary":"eth0 is down"}}'
NOTIFY n1 '{"reason":"first notification"}'; GN=$(AG 'if%3D%22eth0%22')
curl -s -b jar "$API/alert-groups/$GN" | jq -r .title                                        # LinkDown
PUTA n2 h1 '{"labels":{"team":"net","cluster":"x","if":"eth1"}}'; NOTIFY n2 '{"reason":"first notification"}'
curl -s -b jar "$API/alert-groups/$GN" | jq -c '{title, summary}'                            # {"title":"cluster=x","summary":"eth0 is down"}

# C-09.AC-1, AC-22, AC-2: Reopen by the same and by another fingerprint, then a new #N after the window
N2=$(curl -s -b jar "$API/alert-groups/$G2" | jq .number)
PUTA d1 j1 '{"labels":{"team":"db","cluster":"b","pod":"j1","severity":"warning"},"status":"resolved"}'
NOTIFY d1 '{"reason":"some alerts resolved"}'
curl -s -b jar "$API/alert-groups/$G2" | jq -c '{status, r: .resolution.by, why: .resolution.reason_code}'   # {"status":"resolved","r":"system","why":"resolved"}
ADV 480; PUTA d1 j1 '{"labels":{"team":"db","cluster":"b","pod":"j1","severity":"warning"},"status":"firing","starts_at":"now"}'
NOTIFY d1 '{"reason":"new alerts added"}'
curl -s -b jar "$API/alert-groups/$G2" | jq -c '{number, status, reopen_count}'               # {"number":N2,"status":"firing","reopen_count":1}
LAST $G2                                                       # {"event":"reopened","loudness":"loud","mentions":["reopen"]}
PUTA d1 j1 '{"labels":{"team":"db","cluster":"b","pod":"j1","severity":"warning"},"status":"resolved"}'; NOTIFY d1 '{"reason":"some alerts resolved"}'
ADV 60; PUTA d1 j2 '{"labels":{"team":"db","cluster":"b","pod":"j2","severity":"warning"}}'; NOTIFY d1 '{"reason":"new alerts added"}'
[ "$(AG 'pod%3D%22j2%22')" = "$G2" ] && curl -s -b jar "$API/alert-groups/$G2" | jq -c '{status, reopen_count}'   # {"status":"firing","reopen_count":2}
for x in j1 j2; do PUTA d1 $x "{\"labels\":{\"team\":\"db\",\"cluster\":\"b\",\"pod\":\"$x\",\"severity\":\"warning\"},\"status\":\"resolved\"}"; done
NOTIFY d1 '{"reason":"some alerts resolved"}'; ADV 960
PUTA d1 j3 '{"labels":{"team":"db","cluster":"b","pod":"j3","severity":"warning"}}'; NOTIFY d1 '{"reason":"new alerts added"}'
curl -s -b jar "$API/alert-groups/$(AG 'pod%3D%22j3%22')" | jq -c '{number: (.number > '"$N2"'), status}'    # {"number":true,"status":"firing"}

# C-09.AC-7 and C-09.AC-8: new Matchers and the urgent mark leave open Alert Groups alone
R=$(curl -s -b jar "$API/routes/$RDB")
curl -s "${H[@]}" -X PUT -H "If-Match: $(jq -r .etag <<<"$R")" "$API/routes/$RDB" \
  -d "$(jq -c '{name, matchers: [{label: "team", op: "=", value: "dba"}], urgent: true, group_key, destination_ids: [], policy}' <<<"$R")" > /dev/null
NOTIFY d1 '{"reason":"repeat interval elapsed"}'
[ "$(AG 'pod%3D%22i1%22')" = "$G1" ] && curl -s -b jar "$API/alert-groups/$G1" | jq -r .status                # firing

# C-09.AC-11: downtime, simulated as in S-008 (the development clock offset survives the restart)
kill %1; wait %1
psql "$MUSTER_DATABASE_URL" -qc "UPDATE runtime_state SET alive_at = alive_at - interval '10 minutes'"
make dev & sleep 10
curl -s -b jar "$API/alert-groups/$G1/timeline?kind=system" | jq -c '.items[0] | {system_event, f: .period_from, t: .period_to}'
# {"system_event":"muster_unavailable","f":"…","t":"…"}                (about 10 minutes apart)

# C-09.AC-9: a Route with open Alert Groups
curl -s "${H[@]}" -X DELETE "$API/routes/$RDB" | jq -c '{status, t: (.type | sub(".*/"; "")), open_alert_group_count}'
# {"status":409,"t":"route-has-open-alert-groups","open_alert_group_count":2}
curl -s "${H[@]}" -X POST "$API/routes/$RDB/move-open-alert-groups" | jq .moved                    # 2
curl -s -b jar "$API/alert-groups/$G1" | jq -r .route.name                                      # Default
LAST $G1                                                       # {"event":"moved_to_default_route","loudness":"quiet","mentions":[]}
curl -s -o /dev/null -w '%{http_code}\n' "${H[@]}" -X DELETE "$API/routes/$RDB"                     # 204

# C-09.AC-14: deleting the Integration resolves its open Alert Groups
curl -s -o /dev/null "${H[@]}" -X DELETE "$API/integrations/$INT"; sleep 1
curl -s -b jar "$API/alert-groups/$G1" | jq -c '{status, why: .resolution.reason}'              # {"status":"resolved","why":"Integration grp deleted"}

# #N without gaps
psql "$MUSTER_DATABASE_URL" -Atc "SELECT max(number) = count(*) FROM alert_groups"                # t
```

## Open questions

None.

## Notes

- Suggested commit: `feat(groups): add the alert group lifecycle, the timeline and lifecycle events`.
- Alerts of one Snapshot form one entry per Alert Group: a new Alert Group and its first Alerts are one `created`, not a
  `created` followed by `alerts_added`, so delivery (C-11) does not post a Thread reply right after the Publication.
- `events_test.go` is the evidence for C-09.AC-21; the pull request lists the rows it covers.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-09.FR-1 | partial | the attributes of this capability; Owner and Snooze come with S-032, Unclaimed with S-049 |
| C-09.FR-2 | full | |
| C-09.FR-23 | partial | title and `summary`; search by them is S-029 |
| C-09.FR-3 | full | |
| C-09.FR-4 | partial | all three prior statuses; acknowledged and snoozed are verified in S-032 (C-10.AC-6) |
| C-09.FR-5 | partial | the grouping rules and the timer; the person's resolve is S-032 (C-10.AC-8) |
| C-09.FR-6 | partial | firing; the acknowledged and snoozed cases are verified in S-032 (C-10.AC-9) |
| C-09.FR-7 | partial | the entry and the notice; the hint on the page is S-030 |
| C-09.FR-9 | partial | the rules; the Loud cases are verified in S-032 (C-10.AC-12, C-10.AC-13) |
| C-09.FR-10 | partial | the reason in the API and the Timeline; the page is S-030, messages are C-11 |
| C-09.FR-11 | partial | the entries of this capability; Commands S-032, Notes S-063, delivery S-034, timers S-049, the page S-030 |
| C-09.FR-12 | partial | Reopen window and Grace period ends; Snooze ends are S-063 |
| C-09.FR-18 | full | |
| C-09.FR-19 | partial | the API; the move dialog is S-031 |
| C-09.FR-22 | partial | the mechanism and the rows of this capability; the `unacknowledged` row of a released Owner is S-063 |
| C-09.FR-14 | partial | the reads; the page is S-030 |
| C-09.AC-1 | full | |
| C-09.AC-2 | full | |
| C-09.AC-4 | partial | the Timeline and the notice; the page shows the hint in S-030 |
| C-09.AC-6 | full | |
| C-09.AC-7 | full | |
| C-09.AC-8 | full | |
| C-09.AC-9 | full | |
| C-09.AC-10 | full | |
| C-09.AC-11 | full | |
| C-09.AC-12 | full | |
| C-09.AC-14 | full | |
| C-09.AC-20 | partial | the title switch; the search by `summary` is S-029 |
| C-09.AC-21 | full | |
| C-09.AC-22 | full | |
| C-08.FR-4 | full | together with S-025 |
| C-08.FR-6 | full | together with S-025 |
| C-08.FR-8 | full | together with S-025 |
| C-08.FR-9 | full | together with S-025; the dialog is S-031 |
| C-06.FR-3 | full | together with S-020 |
| C-06.FR-4 | full | together with S-020 |
| C-06.FR-19 | partial | the Alert Group of each Alert; its column is S-031 |
| C-05.FR-8 | full | together with S-018, S-019 and S-021 |
| C-02.FR-12 | partial | Timeline entries for downtime; collapsed timers are S-049 |
| C-06.FR-12 | partial | the Quiet `annotations_changed` Timeline entry; the Alert change is S-020 |
