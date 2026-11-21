---
id: S-191
title: Which Alert Groups a window keeps back — the rules of the three objects, Urgent, the Internal alerts of an Integration, and maintenance_started and maintenance_ended (BE)
capability: C-34
kind: be
layer: L2
depends_on: [S-190, S-111, S-118, S-121, S-169]
covers: [C-34.FR-6, C-34.FR-7, C-34.FR-8, C-34.FR-9, C-34.FR-10, C-34.FR-5, C-34.FR-13, C-34.FR-16, C-34.FR-21, C-34.FR-22, C-34.AC-3, C-34.AC-4, C-34.AC-7, C-34.AC-8, C-34.AC-9, C-34.AC-11, C-34.AC-12, C-34.AC-15, C-34.AC-17, C-34.AC-18]
files_touched:
  - api/openapi.yaml
  - internal/db/migrations/00NN_alert_groups_kept_back.up.sql
  - internal/db/migrations/00NN_alert_groups_kept_back.down.sql
  - design/db/schema.md
  - internal/db/migrate_test.go
  - internal/maintenance/keepback.go
  - internal/maintenance/keepback_test.go
  - internal/maintenance/effects.go
  - internal/maintenance/effects_test.go
  - internal/maintenance/query.sql
  - internal/maintenance/keepback_integration_test.go
  - internal/groups/maintenance.go
  - internal/groups/maintenance_test.go
  - internal/groups/dispatcher.go
  - internal/groups/events.go
  - internal/groups/events_test.go
  - internal/groups/transitions.go
  - internal/groups/grouping.go
  - internal/groups/move.go
  - internal/groups/teams.go
  - internal/groups/read.go
  - internal/groups/list.go
  - internal/groups/filters.go
  - internal/groups/list_test.go
  - internal/groups/query.sql
  - internal/groups/groups_integration_test.go
  - internal/ingest/process.go
  - internal/ingest/changes.go
  - internal/api/alertgroups.go
  - internal/api/alertgroups_test.go
  - internal/api/maintenancewindows.go
  - internal/api/server.go
  - internal/runtime/runtime.go
  - internal/runtime/runtime_test.go
  - internal/delivery/enqueue.go
  - internal/delivery/enqueue_test.go
  - internal/logging/events.go
  - internal/metrics/catalogue.go
  - internal/metrics/metrics_test.go
  - web/src/test/fixtures/alert-groups.ts
  - design/prd/l1/reference.md
  - test/e2e/maintenance_keep_back_test.go
acceptance:
  - "[C-34.FR-6, C-34.AC-3, C-34.AC-15] A window of a Route keeps back every open Alert Group of the Route; during one, an Alert Group created from a webhook has its Alerts on its page and in the Alerts view and a Timeline with `created` and a Quiet `maintenance_started` of the kind `system` naming the window; moving the open Alert Groups of the Route to the Default route (C-09.FR-19) records `maintenance_ended` with the cause `moved`, unless a window of the Default route covers them (`maintenance_integration_test`, `keepback_test.go`, `test/e2e/maintenance_keep_back_test.go`)."
  - "[C-34.FR-6, C-34.AC-7] A window of an Integration keeps back an Alert Group only while every Integration of its Alerts is in a window; an Alert Group whose Alerts all come from it is kept back, and so are the `MusterHeartbeatLost` and `MusterSnapshotTruncated` of that Integration, matched by their `integration` label, because the window of an Integration covers the Internal alerts about it; one with Alerts from it and from an Integration outside a window is not (`keepback_test.go`, `test/e2e/maintenance_keep_back_test.go`)."
  - "[C-34.FR-7, C-34.AC-8] A window of a Team keeps back an Alert Group only while every Team of the Alert Group is in a window; an Alert Group of payments alone is kept back, one of payments and analytics is not, and when an Alert of analytics joins the first it records `maintenance_ended` with the cause `team_joined` naming the Team; the Team that owns the Route is not counted, and an Alert Group without a Team is never kept back by a Team's window; a Team that joins an Alert Group never makes it kept back (`keepback_test.go`, `groups_integration_test.go`)."
  - "[C-34.FR-8, C-34.FR-16, C-34.AC-9] Only creation, a move to another Route and the start of a window that covers it make an Alert Group kept back; the start covers an open Alert Group whether or not it was published, and records `maintenance_started` once; a joining Alert or Team, becoming Urgent, and the end, change or deletion of a window only release; an extended end or `urgent_passes` turned off again takes back nothing already released; at the end or the early close every Alert Group the window kept back is decided again, and one that another window still keeps back stays so until that one ends, on any replica (`keepback_test.go`, `effects_test.go`, `test/e2e/maintenance_keep_back_test.go` with `E2E_REPLICAS=2`)."
  - "[C-34.FR-9, C-34.AC-12] A new window keeps back Urgent Alert Groups; a rise to critical of an Alert Group kept back by a window with `urgent_passes` stops keeping it back with `maintenance_ended` (cause `urgent`), and so does raising its Route's Minimum severity and turning the setting on in an active window; under two overlapping windows of which only one lets them through the same rise keeps it back; when the Severity level falls again it is not kept back again (`keepback_test.go`)."
  - "[C-34.FR-10, C-34.AC-11] While it is kept back an Alert Group is ingested and grouped as usual: Alerts join and resolve, the Reopen window and the Grace period apply, the system resolves it, its Severity level and Teams follow its Alerts; a Snooze that ends makes it firing with `snooze_ended` and Acknowledge from a Command acknowledges it, each in the Timeline (`maintenance_test.go`, `groups_integration_test.go`)."
  - "[C-34.FR-13, C-34.AC-18] `maintenance_started` is Quiet without Mentions and `maintenance_ended` records its cause (`window_ended`, `window_closed`, `window_changed`, `window_deleted`, `urgent`, `team_joined`, `moved`) and is Loud with the Mentions of the entries for `new_alert_group` when the Alert Group is firing and nobody owns it, Quiet otherwise, each of the Timeline kind `system` and a system transition of the dispatcher with Muster as the actor; a table-driven test checks that every row of the L2 lifecycle event table for these events produces exactly one Timeline entry with its `event`, kind, `loudness` and `mentions` (`events_test.go`)."
  - "[C-34.FR-21, C-34.AC-4] `getAlertGroup` reads `kept_back` and the `maintenance_windows` that keep it back; `listAlertGroups` takes `kept_back`; a window closed early after the Alerts resolved records a Quiet `maintenance_ended` (cause `window_closed`) and nothing else here (`alertgroups_test.go`, `list_test.go`)."
  - "[C-34.FR-22, C-34.AC-17] `muster_maintenance_kept_back_total{route}` counts the Alert Groups that became kept back, by Route `public_id`; the log events of S-190 now carry the number of Alert Groups kept back or released (`metrics_test.go`, `effects_test.go`)."
verify: "make ci test-integration e2e-go"
operator_attention: false
issue: null
---

# S-191. Which Alert Groups a window keeps back — the rules of the three objects, Urgent, the Internal alerts of an Integration, and maintenance_started and maintenance_ended (BE)

## Scope

**IN**

- The state "kept back" of an Alert Group and the windows that keep it back, and the rules that decide it: the Route,
  the Integration with every Integration of its Alerts, the Team with every Team of its Alerts and the Route's Team not
  counted, the Internal alerts of an Integration, the Urgent pass of each window, overlapping windows (C-34.FR-6 to
  FR-9).
- The moments that decide it: creation, a move to another Route, the start, end, early close, change and deletion of a
  window, a joining Alert or Team, a rise to critical and a change of the Minimum severity (C-34.FR-8, FR-16).
- Ingestion and grouping going on while kept back (C-34.FR-10).
- The lifecycle events `maintenance_started` and `maintenance_ended` as system transitions of the dispatcher, with the
  Timeline and the Loud and Quiet rules (C-34.FR-13).
- `kept_back` in the Alert Group's read and the list filter; `maintenance.Effects` of S-190 implemented; the metric
  `muster_maintenance_kept_back_total` and the counts in the window's log events.

**OUT**

- What is delivered, the publication with its mark and the Thread reply at the end, the Storms and events mode: S-192.
  Until it merges, an Alert Group kept back is still delivered, and `maintenance_ended` is recorded Loud with its
  Mentions but sends nothing.
- The chains, Notification rules, calls, ack timeout and Reminders: S-193.
- The window objects and their API: S-190. The pages: S-194, S-195.

## Contracts

- **Spec**: `AlertGroup` gains the required `kept_back` and `maintenance_windows` (`MaintenanceWindowRef`[]), in the
  list, the read and the result of a Command alike; `listAlertGroups` gains the filter `kept_back`; `LifecycleEvent`
  gains `maintenance_started` and `maintenance_ended` (`x-event-kinds` `system`), `WebhookEventName` too;
  `TimelineSystemEntry` gains `maintenance_window` (`MaintenanceWindowRef`, null once deleted) and `cause`
  (`MaintenanceEndedCause`); `HintEvent` `alert-group` covers a change of `kept_back`.
- **Schema** (a new migration, expand only; the number is assigned by the orchestrator at dispatch; `schema.md`):
  `alert_groups.kept_back boolean NOT NULL DEFAULT false`, `alert_group_maintenance_windows` (`org_id`,
  `alert_group_id` FK cascade, `maintenance_window_id` FK, primary key both) — the windows that keep it back, so the
  banner and the cause name them — and an index of the kept-back open Alert Groups by Route. The `timeline_entries`
  event CHECK (`timeline_entries_event_vN_check`) gains the two events and, since their kind is `system`, the kind
  CHECK needs no change; the migration replaces the event CHECK, `webhook_events` of `destinations.webhook_events`
  (the CHECK of the event names, as S-118 and S-121 extend it) and `open_group_updates_kind_vN_check` if the re-render
  of an Alert Group's mark needs a kind of its own, each with the next version in the order the stories merge and
  every value present when it merges. The Timeline kind `system` already exists.
- **Packages and their imports** (ADR-0016; as S-169): `groups` imports none of `maintenance` and declares the
  interface it consumes, `groups.KeepBack` (`Decide(ctx, tx, g) (Decision, error)`), which `maintenance` implements and
  `internal/runtime` wires; `maintenance` imports `groups`, `routing`, `teams` and `integrations` for the lookups.
  `Decision` carries whether the Alert Group is kept back and by which windows. The rules sit in
  `maintenance/keepback.go` as pure functions over the Alert Group's Route, the Integrations and Teams of its Alerts
  (and the `integration` label of an Internal alert), the windows active at the business clock and their
  `urgent_passes`; `groups/maintenance.go` holds the transitions `MaintenanceStarted` and `MaintenanceEnded`, which the
  dispatcher runs after every transition that can change the result — creation, move, Alerts that join, a Team that
  joins, a rise — and which a window's start, end, close, change and deletion run for each Alert Group in id order
  under the lock order of grouping (`maintenance.Effects`, which returns the counts).
- **Order of the rules** (C-34.FR-8): the dispatcher asks `KeepBack` for the current state; becoming kept back happens
  only on creation, a move or the start of a covering window, so a joining Alert, a joining Team and a rise ask only
  whether it is still kept back; the result is stored with its windows, `maintenance_started` and `maintenance_ended`
  are recorded once on the change, and the cause of a release is the first of: the window's end, close, change or
  deletion, a rise to Urgent, a joining Team, a move.
- **Delivery's view** (`internal/delivery/enqueue.go`): the re-render after the change carries `kept_back` and the
  cause in `groups.Rendering`, which S-192 reads; this story adds the fields and a test that the table of events knows
  the two rows (`enqueue_test.go`).
- **Metrics** (`internal/metrics/catalogue.go`): `muster_maintenance_kept_back_total{route}`; the reference pages
  regenerate. `muster_maintenance_released_total` is S-192's.
- **Settings**: `maintenance.urgent_passes`; `route.minimum_severity`, whose raise is a configuration change of C-26
  that now releases.
- **Web fixture** (`web/src/test/fixtures/alert-groups.ts`): the builder sets `kept_back` to false and
  `maintenance_windows` to `[]`, so that `make generate` keeps the TypeScript check green.
- **Documents**: reference.md's L2 lifecycle event table is consulted, and changed only where its rows for the two
  events need the causes named.

## Steps

1. Write the migration and `schema.md`. Check: `make test-integration` runs up, down and up.
2. Write the rules and a table of overlapping windows. Check: `keepback_test.go` covers a Route, an Integration with one
   and two Integrations, a Team with one, two and no Team, the Route's Team, the Internal alerts, Urgent with one and
   two windows.
3. Write the transitions and wire them into the dispatcher at creation, move, join and rise; implement
   `maintenance.Effects` for a window's start, end, close, change and deletion. Check: `maintenance_test.go`,
   `effects_test.go`, `keepback_integration_test.go` with two replicas claiming the timer rows.
4. Add the reads, the filter and the spec. Check: `make generate-check`; `alertgroups_test.go`, `list_test.go`.
5. Add the metric and the counts in the log events; write `test/e2e/maintenance_keep_back_test.go`. Check: the
   Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
# as in S-190: A, the Team payments (TP) and analytics (TA), the Integration "sev" with NOTIFY, PUTA and AG, the Route R
# for its Alerts, CLK; the fake Mattermost at 127.0.0.1:18065 still receives, as delivery is S-192's
API=localhost:8080/api/v1
W=$(curl -s -H "$A" -X POST $API/maintenance-windows -d '{...Route R, now, +30 min...}' | jq -r .id)
curl -s -X PUT 127.0.0.1:19093/_fake/groups/e1 -d '{"receiver":"sev","route":"{}","labels":{"alertname":"PaymentsAPIDown"}}' > /dev/null
PUTA e1 a '{"labels":{"team":"payments","severity":"warning"}}'; NOTIFY e1 '{"reason":"first notification"}'
G=$(AG 'alertname%3D%22PaymentsAPIDown%22'); sleep 1
curl -s -H "$A" $API/alert-groups/$G | jq -c '{kept_back, w: [.maintenance_windows[].id] == ["'$W'"]}'   # {"kept_back":true,"w":true}
curl -s -H "$A" "$API/alert-groups/$G/timeline?kind=system" | jq -c '[.items[] | {e: .event, l: .loudness}]'
# [{"e":"maintenance_started","l":"quiet"}]
curl -s -H "$A" "$API/alert-groups?kept_back=true" | jq '.items | length'                                 # 1
# a Team of the Alert Group not in a window releases it: window on payments only
curl -s -H "$A" -X POST $API/maintenance-windows/$W/end > /dev/null; sleep 1
curl -s -H "$A" $API/alert-groups/$G | jq -r .kept_back                                                   # false
curl -s -H "$A" "$API/alert-groups/$G/timeline?kind=system" | jq -c '[.items[] | {e: .event, c: .cause, l: .loudness}] | last'
# {"e":"maintenance_ended","c":"window_closed","l":"loud"}   (firing, nobody owns it)
curl -s localhost:8082/metrics | grep 'muster_maintenance_kept_back_total'                                # route="RT..."} 1
grep -c '"event":"maintenance_window_ended"' dev.log                                                       # 1, with the count 1
# the Team rule: window on payments; an Alert Group of payments and analytics is not kept back; of payments alone is
# an Alert of analytics joins: maintenance_ended with cause team_joined
# an Internal alert: window on the Integration "sev"; its MusterHeartbeatLost group is kept back (advance the clock past the Heartbeat timeout)
# Urgent: a window with urgent_passes; PUTA a critical severity; the group is released with cause urgent
```

`test/e2e/maintenance_keep_back_test.go` repeats these checks and adds: scenario 3 of C-34 (payments and analytics in one
group), the Internal alerts of an Integration, two overlapping windows of which one lets Urgent through, the move to the
Default route (C-34.AC-15), a Snooze that ends and an Acknowledge from a Command while kept back (C-34.AC-11), a
window extended and a window whose `urgent_passes` is turned off again, the table of C-34.AC-18, and `E2E_REPLICAS=2`.

## Open questions

1. **Telemetry.** C-34.FR-22 says the Features category of the opt-in telemetry reports whether Maintenance windows are
   used (ADR-0019), but the telemetry has no capability or story yet. This story adds nothing for it; the story that
   builds the Features category reads `count(maintenance_windows) > 0`. Reported to the maintainer as a PRD gap.
2. **Columns of the state.** As in S-190, the PRD names no columns; `alert_groups.kept_back` and
   `alert_group_maintenance_windows` are named here for the maintainer to confirm in review.
3. **Internal alerts and the Integration rule.** The `integration` label of `MusterHeartbeatLost` and
   `MusterSnapshotTruncated` is the immutable id of an Integration (C-06.FR-14); the rule reads it as "the Integration
   of the Alert" for the Alert Groups of the built-in Integration. The PRD says "the Internal alerts about that
   Integration" only for these two; other Internal alerts with an `integration` label, if the registry has more, follow
   the same rule. The maintainer confirms this in review.

## Notes

- Suggested commit: `feat(maintenance): keep back the alert groups of a window and record its start and end on them`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases. The migration number
  is assigned by the orchestrator at dispatch; it replaces `timeline_entries_event_vN_check`, the `webhook_events`
  event CHECK and, if needed, `open_group_updates_kind_vN_check`, each with the next version in merge order.
- Interim on `edge` until S-192 and S-193 merge: an Alert Group kept back is still delivered and its chains still run,
  and `maintenance_ended` sends nothing, so do not open windows on an installation that relies on them until all three
  merge; the end-to-end tests of this story use their own data.
- The kept-back state is part of the Alert Group's read under `alert-groups:read`; no new Permission.
- A Team that joins never makes an Alert Group kept back (C-34.FR-7, FR-8); the test for it names the unchanged chains
  S-193 owns.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-34.FR-6 | full | |
| C-34.FR-7 | partial | the rule and the release; the chains left running are S-193 |
| C-34.FR-8 | full | |
| C-34.FR-9 | full | |
| C-34.FR-10 | partial | ingestion, status and Commands from the API; the buttons of existing Root messages answered on the interactive path are S-192 |
| C-34.FR-13 | partial | the events and the Timeline; the messenger effects are S-192 |
| C-34.FR-5 | partial | the start and the end as lifecycle events of the Alert Groups kept back; the Audit log of windows is S-190 |
| C-34.FR-16 | full | every Alert Group decided again at the end; what is published is S-192's, under FR-17 to FR-19 |
| C-34.FR-21 | partial | `kept_back` on Alert Groups; in webhook events S-192 |
| C-34.FR-22 | partial | `muster_maintenance_kept_back_total` and the counts; `muster_maintenance_released_total` is S-192 |
| C-34.AC-3 | partial | everything but "the fake servers receive nothing" and the Storm, S-192, and the chain, S-193 |
| C-34.AC-4 | partial | the Timeline entry; the delivery state is S-192 |
| C-34.AC-7 | full | |
| C-34.AC-8 | partial | the rule and `maintenance_ended` naming the Team; the chains left running are S-193 |
| C-34.AC-9 | partial | the start and end deciding Alert Groups; the Root message, Thread reply and chain are S-192 and S-193 |
| C-34.AC-11 | partial | the Snooze and the Command from the API; the button of a Root message is S-192 |
| C-34.AC-12 | partial | the release and its event; the published Alert Group is S-192 |
| C-34.AC-15 | full | |
| C-34.AC-17 | partial | `muster_maintenance_kept_back_total` and the log events; `muster_maintenance_released_total` is S-192 |
| C-34.AC-18 | full | |
