---
id: S-021
title: Internal alert mechanism, built-in Integration, replay, Integration deletion and learned Alertmanager routes (BE)
capability: C-06
kind: be
layer: L1
depends_on: [S-020]
covers: [C-06.FR-6, C-06.FR-14, C-06.FR-15, C-06.FR-16, C-06.FR-17, C-06.FR-18, C-06.FR-19, C-06.FR-20, C-06.AC-3, C-06.AC-5, C-06.AC-6, C-06.AC-7, C-06.AC-10, C-06.AC-11, C-05.FR-8, C-02.FR-15]
files_touched:
  - internal/internalalerts/registry.go
  - internal/internalalerts/raise.go
  - internal/internalalerts/query.sql
  - internal/internalalerts/internalalerts_test.go
  - sqlc.yaml
  - internal/tools/refgen/main.go
  - internal/tools/refgen/refgen_test.go
  - internal/db/migrations/0002_stored_snapshots_replayed_at.up.sql
  - internal/db/migrations/0002_stored_snapshots_replayed_at.down.sql
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - design/db/schema.md
  - internal/ingest/process.go
  - internal/ingest/truncation.go
  - internal/ingest/deletion.go
  - internal/ingest/replay.go
  - internal/ingest/routes.go
  - internal/ingest/retention.go
  - internal/ingest/worker.go
  - internal/ingest/alertsview.go
  - internal/ingest/query.sql
  - internal/ingest/internal_test.go
  - internal/ingest/replay_test.go
  - internal/ingest/worker_test.go
  - internal/ingest/internal_integration_test.go
  - internal/ingest/ingest_integration_test.go
  - internal/ingest/facts_test.go
  - internal/integrations/integrations.go
  - internal/integrations/tokens.go
  - internal/integrations/query.sql
  - internal/integrations/integrations_test.go
  - internal/runtime/bootstrap.go
  - internal/runtime/runtime.go
  - internal/runtime/runtime_test.go
  - internal/api/integrations.go
  - internal/api/integrations_test.go
  - internal/api/problem.go
  - internal/api/server.go
  - internal/archlint/secretleak.go
  - internal/cli/cli.go
  - internal/cli/ingest.go
  - internal/cli/ingest_test.go
  - internal/leader/tasks.go
  - internal/leader/leader_test.go
  - internal/logging/events.go
  - test/e2e/internal_alerts_test.go
  - test/e2e/ingest_test.go
acceptance:
  - "[C-06.FR-14] The built-in Integration \"Muster\" exists from the first start, is listed with `builtin: true`, has no tokens and no Heartbeat, and `updateIntegration`, `deleteIntegration` and `createIntegrationToken` on it answer 409 `builtin_immutable`."
  - "[C-06.FR-14] Internal alerts form a closed registry in code with a generated reference page that `make generate-check` keeps current; a raise and a resolve are synthetic Stored Snapshots of the built-in Integration, marked `internal`, processed in order with its other Snapshots, listed by `listStoredSnapshots`, and never resolved as Gone or Stale."
  - "[C-06.FR-6, C-06.AC-5] A truncated Snapshot raises `MusterSnapshotTruncated` (severity `warning`, labels `integration` and `integration_name`) as a firing Alert of the built-in Integration while any `groupKey` of the Integration is truncated; the next untruncated Snapshot of the same `groupKey` resolves it; raising it again while it fires changes nothing."
  - "[C-06.AC-11] Renaming an Integration whose `MusterSnapshotTruncated` fires changes that Alert's `integration_name` and keeps its fingerprint; no second Internal alert is raised."
  - "[C-06.FR-16, C-06.FR-15, C-05.FR-8, C-06.AC-7, C-06.AC-10] Deleting an Integration with two open Alerts resolves both with the reason `integration_deleted` and the text \"Integration {name} deleted\", increases `muster_alerts_resolved_total{reason=\"integration_deleted\"}` by two and resolves its `MusterSnapshotTruncated`; Snapshots it accepted before the deletion are processed first."
  - "[C-06.FR-17, C-06.FR-20, C-06.AC-6, C-02.FR-15] `muster ingest replay --since 1h --actor ops-alice` reprocesses the Stored Snapshots of the last hour and writes `ingest.replayed` to the Audit log; replaying the same hour twice changes no Alert and records no Alert change the second time; a Snapshot that failed is processed again; without `--actor` the command exits 2 and changes nothing."
  - "[C-06.FR-18, C-06.AC-3] `listAlertmanagerRoutes` returns each Alertmanager route seen with its learned repeat interval and the time to resolve by absence; for a route whose Snapshots repeat every 5 minutes the interval is 5 minutes; a route above `processing.long_repeat_warning` carries `long_interval_warning` and a recommended route snippet; each Integration in `listIntegrations` carries the warnings `snapshot_truncated` and `long_repeat_interval` while they apply."
  - "[C-06.FR-19] Alerts resolved longer than `retention.alert_details` ago leave the Alerts view: the Leader deletes them in batches."
verify: "make ci test-integration e2e"
operator_attention: false
issue: 21
---

# S-021. Internal alert mechanism, built-in Integration, replay, Integration deletion and learned Alertmanager routes (BE)

## Scope

**IN**

- The built-in "Muster" Integration and its refusals.
- The closed Internal alert registry, its reference page, and raising and resolving through synthetic Stored
  Snapshots; `MusterSnapshotTruncated`.
- What deleting an Integration does to its Alerts and Internal alerts.
- `muster ingest replay`.
- The learned Alertmanager routes API and the Integration warnings for truncation and long repeat intervals.
- Retention of the Alerts view.

**OUT**

- `MusterHeartbeatLost` and the Heartbeat warnings (S-023); later Internal alerts with their capabilities
  (`MusterDestinationBroken` S-035, `MusterTemplateError` S-036 and S-045, `MusterOIDCSecretExpiring` S-053).
- Routing Internal alerts (S-025) and their Alert Groups (S-028).
- The Integration page sections (S-022).

## Contracts

- **Operations implemented**: `listAlertmanagerRoutes`; `listIntegrations`, `getIntegration`, `updateIntegration`,
  `deleteIntegration`, `createIntegrationToken` and `revokeIntegrationToken` gain the built-in rules, the warnings and
  the deletion effects. Schemas: `AlertmanagerRoute(List)`, `IntegrationWarning`.
- **Built-in Integration** (C-06.FR-14; `integrations.builtin`): a start-up ensure step
  (`internal/runtime/bootstrap.go`, S-007) creates it once per Organization — name "Muster", `webhook_only`, no Static
  labels, the Heartbeat off. It is listed with the others, marked `builtin`; changing, deleting it or giving it a token
  answers `409` with `builtin_immutable`.
- **Registry** (C-06.FR-14, ADR-0014; `internal/internalalerts`): the closed list of Internal alerts — name, severity,
  the entity label (`integration`, `route` or `destination`) with its name label, any extra labels, the condition, the
  runbook page. This story registers `MusterSnapshotTruncated` (severity `warning`; labels `integration`,
  `integration_name`). `make generate` writes `docs/reference/internal-alerts.md` from it through `refgen`.
- **Raise and resolve** (C-06.FR-14): inside the caller's transaction, a synthetic Stored Snapshot of the built-in
  Integration with `source = 'internal'`: an Alertmanager-format body with the `groupKey`
  `{}/{muster="internal"}:{alertname="<name>"}` and one Alert — labels `alertname`, `severity`, the entity's id label
  and name label and the extra labels; annotations `summary`, `description` and `runbook_url`; `status`; `startsAt` the
  time of the raise; `fingerprint` computed from `alertname` and the id labels only. The worker of S-020 processes it in
  order with the built-in Integration's other Snapshots, with three differences: an internal Snapshot is never evidence
  of absence and feeds no repeat learning, a raise of an Internal alert that already fires is the same firing whatever
  its `startsAt`, and a raise whose labels differ only in a name label updates the labels in place as an
  `annotations_changed` Alert change. Callers raise and resolve on transitions of their condition, not on every check.
- **`MusterSnapshotTruncated`** (C-06.FR-6): processing of S-020 raises it when an Integration's count of truncated
  `groupKey`s goes from 0 to 1 and resolves it when the count returns to 0 (after S-023, also when truncation ends with
  time). Rename (C-06.AC-11): `updateIntegration` that changes the name raises again every open Internal alert carrying
  `integration=<id>`, with the new `integration_name`, and every one whose raise still waits for processing; processing
  reads the Integration's name for a raise under a key-share lock, so a rename in progress is never missed.
  The same holds for any entity: `updateDestination` that changes the name does it for `destination=<id>` and `destination_name`
  in its own transaction (delivery's rename hook), so every Internal alert that carries a name label keeps the fingerprint
  and shows the current name.
- **Deletion** (C-05.FR-8, C-06.FR-16, ADR-0003): `deleteIntegration` also inserts, in its transaction, an internal
  Stored Snapshot of the deleted Integration that marks the deletion. The worker keeps processing a deleted
  Integration's pending Snapshots in order; the marker resolves every Alert of the Integration that still fires with the
  reason `integration_deleted` and the text "Integration {name} deleted" (Alert changes `resolved`, counted in
  `muster_alerts_resolved_total{reason="integration_deleted"}`) and resolves the Integration's open Internal alerts
  (`MusterSnapshotTruncated`; `MusterHeartbeatLost` joins in S-023). Like its Stored Snapshots, its Alerts stay readable
  through `listIntegrationAlerts` by its id until retention. The marker is an Alertmanager-format body with the
  `groupKey` `{}/{muster="internal"}:{event="integration_deleted"}`, no Alerts, and the Integration's id and name in
  `commonLabels`. The marker resolves the Internal alerts about the Integration that fire or whose raise still waits
  in the built-in Integration's queue, and ends the truncation of its `groupKey`s; a deleted Integration raises no
  Internal alert afterwards, so a replayed marker resolves nothing again. A webhook that authenticated before the
  deletion but was stored after it (`received_at` at or after `deleted_at`) is marked `failed` and fires nothing.
- **Replay** (C-06.FR-17, C-02.FR-15): `muster ingest replay --since <duration> [--integration <name>] --actor <name>`
  runs against the database like the other CLI commands; without `--actor` it exits 2. In one transaction it sets the
  Stored Snapshots received within the period (and within `retention.stored_snapshots`) back to `pending`, clearing
  `processed_at` and `processing_error`, sends `NOTIFY`, writes `ingest.replayed` (actor kind `cli`, Transport `cli`,
  details: `since`, `integration`, `count`) and prints the count. The workers process them in arrival order as late
  Snapshots (S-020): only their `resolved` rows apply, under the `startsAt` rule, so Snapshots already processed change
  nothing, while a Snapshot that failed is processed with the fixed code. A replayed Stored Snapshot is not counted in
  `snapshot_count` again (D257): replay also sets `stored_snapshots.replayed_at` (migration
  `0002_stored_snapshots_replayed_at`, `design/db/schema.md`), and processing counts a Stored Snapshot only when it
  leaves `pending` for the first time.
- **Learned routes** (C-06.FR-18; `alertmanager_routes`, `alertmanager_groups`): `listAlertmanagerRoutes` returns the
  Integration's routes ordered by route path (none for the built-in Integration, whose synthetic Snapshots teach
  nothing): `route_path`, `learned_repeat_interval_seconds` (null before learning),
  `resolve_by_absence_after_seconds` (`stale_after`: `processing.stale_after_factor` times the interval, or
  `processing.stale_after_unlearned`), `truncated_group_count`, `long_interval_warning` (interval above
  `processing.long_repeat_warning`) and, with it, `recommended_snippet` — an Alertmanager route fragment for that route
  path with `repeat_interval` set to `snippet.repeat_interval`; the route path and every matcher are written as
  double-quoted ASCII YAML scalars, because the `groupKey` is received text.
- **Warnings** (C-06.FR-18): every read of an Integration carries `snapshot_truncated` (with `truncated_group_count`)
  while any of its `groupKey`s is truncated and one `long_repeat_interval` (with `route_path` and
  `repeat_interval_seconds`) per route above the threshold.
- **Alerts view retention** (C-06.FR-19): an hourly Leader task deletes, in batches of at most 5,000 rows, `alerts` rows
  resolved longer than `retention.alert_details` ago, their presences going with them (`design/db/schema.md` §6). It
  runs per Organization: it iterates over the Organizations (one in L1) and passes `org_id` to every query (lint 1).
- **Log events**: `internal_alert_raised` and `internal_alert_resolved` (INFO: `alertname`, `fingerprint`, the entity),
  `ingest_replayed` (INFO: `actor`, `since`, `integration`, `count`).

## Steps

1. Write the built-in Integration ensure step and its refusals. Check: tests cover the three `409` answers and a second
   start creating nothing.
2. Write the registry, the reference page and raise/resolve with their processing rules. Check: tests cover a raise, a
   repeated raise, a rename and a resolve, and that a synthetic Snapshot never makes anything Gone.
3. Raise and resolve `MusterSnapshotTruncated` from processing. Check: an integration test runs C-06.AC-5 and AC-11.
4. Write the deletion marker and its processing. Check: tests cover pending Snapshots processed before the marker and
   C-06.AC-7.
5. Write replay and the CLI. Check: tests replay twice and see no Alert change the second time, and a fixed failed
   Snapshot processed.
6. Write the learned routes API, the warnings and the retention task. Check: Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
# as in S-020: the session `H`, the Integration "lab" (id INT) with its fake receiver, and the helpers VIEW, ADV, NOTIFY,
# with the fake groups g2 (10 Alerts on route "{}") and g6 (route {}/{team="web"}) already sent
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake
BI=$(curl -s -b jar $API/integrations | jq -r '.items[] | select(.builtin) | .id')
curl -s -b jar "$API/integrations/$BI" | jq -c '{name, builtin, heartbeat: .heartbeat.state}'
# {"name":"Muster","builtin":true,"heartbeat":"not_configured"}
curl -s "${H[@]}" -X DELETE "$API/integrations/$BI" | jq -r .code                                   # builtin_immutable
curl -s "${H[@]}" -d '{}' "$API/integrations/$BI/tokens" | jq -r .code                              # builtin_immutable

# C-06.AC-5: truncation raises MusterSnapshotTruncated (an untruncated Snapshot first, so the group starts untruncated)
NOTIFY g2 '{"reason":"repeat interval elapsed"}'
NOTIFY g2 '{"reason":"repeat interval elapsed","max_alerts":2}'; sleep 1
curl -s -b jar "$API/integrations/$BI/alerts?state=firing" | jq -c '.items[] | .labels'
# {"alertname":"MusterSnapshotTruncated","integration":"NT…","integration_name":"lab","severity":"warning"}
curl -s -b jar "$API/integrations/$INT" | jq -c '[.warnings[] | {kind, truncated_group_count}]'
# [{"kind":"snapshot_truncated","truncated_group_count":1}]
curl -s -b jar "$API/stored-snapshots?integration=$BI&limit=1" | jq -c '.items[0] | {state, group_key}'
# {"state":"processed","group_key":"{}/{muster=\"internal\"}:{alertname=\"MusterSnapshotTruncated\"}"}
NOTIFY g2 '{"reason":"repeat interval elapsed","max_alerts":2}'; sleep 1
curl -s -b jar "$API/stored-snapshots?integration=$BI" | jq '.items | length'                         # 1  (no new raise)

# C-06.AC-11: rename keeps the fingerprint
FP=$(curl -s -b jar "$API/integrations/$BI/alerts?state=firing" | jq -r '.items[0].fingerprint')
I=$(curl -s -b jar "$API/integrations/$INT")
curl -s "${H[@]}" -X PUT -H "If-Match: $(jq -r .etag <<<"$I")" "$API/integrations/$INT" \
  -d "$(jq -c '{name: "lab-eu", description, connection_mode, static_labels, duplicate_window_seconds, heartbeat: {enabled: false}}' <<<"$I")" > /dev/null
sleep 1; curl -s -b jar "$API/integrations/$BI/alerts?state=firing" | jq -c '[.items[] | {fingerprint, n: .labels.integration_name}]'
# [{"fingerprint":"<FP>","n":"lab-eu"}]
NOTIFY g2 '{"reason":"repeat interval elapsed"}'; sleep 1
curl -s -b jar "$API/integrations/$BI/alerts?state=firing" | jq '.items | length'                    # 0

# C-06.AC-3 and C-06.FR-18: learned routes, and a long interval
curl -s -b jar "$API/integrations/$INT/alertmanager-routes" \
  | jq -c '.items[] | select(.route_path == "{}/{team=\"web\"}") | {learned_repeat_interval_seconds, resolve_by_absence_after_seconds}'
# {"learned_repeat_interval_seconds":30x,"resolve_by_absence_after_seconds":9xx}
curl -s -X PUT $FAM/groups/g7 -d '{"receiver":"lab","route":"{}/{kind=\"info\"}","labels":{"alertname":"CertExpiry"}}' > /dev/null
curl -s -X PUT $FAM/groups/g7/alerts/c -d '{"labels":{"domain":"example.org"}}' > /dev/null
NOTIFY g7 '{"reason":"first notification"}'; ADV 7200; NOTIFY g7 '{"reason":"repeat interval elapsed"}'
curl -s -b jar "$API/integrations/$INT/alertmanager-routes" | jq -c '.items[] | select(.long_interval_warning) | {route_path, learned_repeat_interval_seconds}'
# {"route_path":"{}/{kind=\"info\"}","learned_repeat_interval_seconds":720x}
curl -s -b jar "$API/integrations/$INT/alertmanager-routes" | jq -r '.items[] | select(.long_interval_warning) | .recommended_snippet' | grep repeat_interval
#       repeat_interval: 10m

# C-06.AC-6: replay twice, with the development defaults (muster dev <subcommand>, S-004); the development clock moved
# more than 2 hours since the first Snapshots, so the period is 3 hours to reach all of them
./bin/muster dev ingest replay --since 3h --integration lab-eu; echo "exit=$?"
# --actor is required
# exit=2
./bin/muster dev ingest replay --since 3h --integration lab-eu --actor ops-alice
# replayed 1x Stored Snapshots of lab-eu
sleep 5; N0=$(grep -c '"event":"snapshot_processed"' dev.log)
./bin/muster dev ingest replay --since 3h --integration lab-eu --actor ops-alice; sleep 5
grep '"event":"snapshot_processed"' dev.log | tail -n +$((N0 + 1)) \
  | jq -s 'map(.fired + .resolved + .gone + .continued) | add'                                       # 0
curl -s -b jar "$API/audit-log?action=ingest.replayed" | jq -c '.items[0] | {actor: .actor.name, transport}'
# {"actor":"ops-alice","transport":"cli"}

# C-06.AC-7 and C-06.AC-10: deletion resolves the open Alerts and the Internal alert
NOTIFY g2 '{"reason":"repeat interval elapsed","max_alerts":2}'; sleep 1                  # MusterSnapshotTruncated fires again
OPEN=$(curl -s -b jar "$API/integrations/$INT/alerts?state=firing&limit=500" | jq '.items | length')
D0=$(curl -s localhost:8082/metrics | grep "muster_alerts_resolved_total{integration=\"$INT\",reason=\"integration_deleted\"}" | awk '{print $2+0}')
curl -s -o /dev/null -w '%{http_code}\n' "${H[@]}" -X DELETE "$API/integrations/$INT"                 # 204
sleep 1
curl -s -b jar "$API/integrations/$INT/alerts?state=resolved&label=pod%3D%22p0%22" | jq -c '.items[0] | {resolve_reason, resolve_reason_text}'
# {"resolve_reason":"integration_deleted","resolve_reason_text":"Integration lab-eu deleted"}
curl -s localhost:8082/metrics | grep "muster_alerts_resolved_total{integration=\"$INT\",reason=\"integration_deleted\"}" | awk -v d=${D0:-0} -v o=$OPEN '{print (($2 - d) == o)}'
# 1
curl -s -b jar "$API/integrations/$BI/alerts?state=firing" | jq '.items | length'                    # 0
```

## Open questions

None.

## Notes

- Suggested commit: `feat(ingest): add internal alerts, the built-in integration, replay and learned routes`.
- The deletion marker keeps one writer and one order per Integration: the resolutions it causes reach routing and
  grouping (S-025, S-028) through the same Alert changes as any other resolution.
- `muster_alerts_resolved_total{reason="integration_deleted"}` keeps the deleted Integration's id as its label, as every
  metric of an Integration does; its `muster_integration_info` series is gone.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-06.FR-6 | full | together with S-020 and S-023 |
| C-06.FR-14 | full | each later Internal alert is registered by its capability |
| C-06.FR-15 | partial | the `integration_deleted` reason; `stale` is S-023 |
| C-06.FR-16 | partial | Alerts and `MusterSnapshotTruncated`; `MusterHeartbeatLost` is S-023 |
| C-06.FR-17 | full | |
| C-06.FR-18 | partial | the API and the warnings; the page and the list are S-022 |
| C-06.FR-19 | partial | retention of the view; the page is S-022 |
| C-06.FR-20 | full | together with S-020 |
| C-06.AC-3 | partial | the learned interval through the API; the page is S-022, "never Stale without a Heartbeat" with the running Stale scan is S-023 |
| C-06.AC-5 | full | |
| C-06.AC-6 | full | |
| C-06.AC-7 | full | |
| C-06.AC-10 | full | together with S-020 |
| C-06.AC-11 | full | |
| C-05.FR-8 | partial | the effects on Alerts; on Alert Groups S-028 |
| C-02.FR-15 | partial | `muster ingest replay` |
