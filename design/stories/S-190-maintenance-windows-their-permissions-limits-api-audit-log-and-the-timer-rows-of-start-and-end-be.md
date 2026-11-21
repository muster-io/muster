---
id: S-190
title: Maintenance windows with their Permissions, limits, API, Audit log and the timer rows of their start and end (BE)
capability: C-34
kind: be
layer: L2
depends_on: [S-115, S-118, S-169]
covers: [C-34.FR-1, C-34.FR-2, C-34.FR-3, C-34.FR-4, C-34.FR-5, C-34.FR-21, C-34.FR-22, C-34.AC-1, C-34.AC-2, C-34.AC-9]
files_touched:
  - api/openapi.yaml
  - api/README.md
  - internal/db/migrations/00NN_maintenance_windows.up.sql
  - internal/db/migrations/00NN_maintenance_windows.down.sql
  - design/db/schema.md
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - internal/publicid/publicid.go
  - internal/publicid/publicid_test.go
  - sqlc.yaml
  - internal/maintenance/windows.go
  - internal/maintenance/windows_test.go
  - internal/maintenance/limits.go
  - internal/maintenance/limits_test.go
  - internal/maintenance/timers.go
  - internal/maintenance/timers_test.go
  - internal/maintenance/query.sql
  - internal/maintenance/maintenance_integration_test.go
  - internal/api/maintenancewindows.go
  - internal/api/maintenancewindows_test.go
  - internal/api/integrations.go
  - internal/api/routes.go
  - internal/api/teams.go
  - internal/api/problem.go
  - internal/api/server.go
  - internal/api/server_test.go
  - internal/integrations/integrations.go
  - internal/integrations/query.sql
  - internal/routing/routes.go
  - internal/routing/query.sql
  - internal/teams/teams.go
  - internal/teams/query.sql
  - internal/leader/tasks.go
  - internal/leader/query.sql
  - internal/runtime/runtime.go
  - internal/runtime/runtime_test.go
  - internal/logging/events.go
  - internal/auth/auth_integration_test.go
  - internal/tools/refgen/main.go
  - design/prd/l1/reference.md
  - design/prd/l1/defaults.md
  - design/prd/L2.md
  - design/prd/l2/C-34-maintenance-windows.md
  - docs/install/versions.md
  - test/e2e/maintenance_windows_test.go
acceptance:
  - "[C-34.FR-2, C-34.FR-5, C-34.AC-1] A Service account whose Role is Responder opens a window on a Team with `createMaintenanceWindow`, a Responder who is not in the Team closes it with `endMaintenanceWindow`, and the Audit log records `maintenance_window.created` and `maintenance_window.updated` with a before/after diff, the actor and the Transport; a Viewer reads windows with `maintenance:read` and gets `403` on `createMaintenanceWindow` (`maintenancewindows_test.go`, `test/e2e/maintenance_windows_test.go`)."
  - "[C-34.FR-3, C-34.AC-1] A window whose end is not after its start or is in the past, whose planned start is in the past, that lasts longer or starts further ahead than `maintenance.limits` (7 days, 90 days), or whose object is the built-in \"Muster\" Integration is refused with `422` at the pointer of the field; a missing or stale `If-Match` gets `428` and `412` (`windows_test.go`, `limits_test.go`, `maintenancewindows_test.go`)."
  - "[C-34.FR-1, C-34.FR-3, C-34.AC-2] A planned window changes in every field but its object and is deleted; an active one changes its end, its reason and `maintenance.urgent_passes` only (a change of its start gets `422`), is closed with `endMaintenanceWindow`, which sets its end to now, and is refused deletion with `409`; an ended one refuses every change with `409`; the object never changes (`windows_test.go`, `maintenancewindows_test.go`)."
  - "[C-34.FR-3, C-34.AC-2] Deleting an Integration, a Route or a Team closes its active windows and deletes its planned ones in the same transaction, each recorded in the Audit log; an ended window stays (`maintenance_integration_test.go`)."
  - "[C-34.FR-1, C-34.AC-9] A window is one row; its start and its end are `timers` rows of the kinds `maintenance_window_start` and `maintenance_window_end` that any replica claims with its lease, so two replicas handle each once, on the business clock, and the development clock moves them; the state of a window (`planned`, `active`, `ended`) is read from its start and end at the business clock; a change of a planned or active window moves its timer rows, and closing or deleting removes them (`timers_test.go`, `maintenance_integration_test.go`, `test/e2e/maintenance_windows_test.go` with `E2E_REPLICAS=2`)."
  - "[C-34.FR-22, C-34.AC-9] The handler of each timer logs `maintenance_window_started` and `maintenance_window_ended` (INFO) once, with the window's `public_id`, its object and the number of Alert Groups kept back or released, which is 0 until S-191 wires `maintenance.Effects`; each is logged once with two replicas (`timers_test.go`, `test/e2e/maintenance_windows_test.go`)."
  - "[C-34.FR-1, C-34.FR-21] `listMaintenanceWindows` pages by cursor and filters by `object_type`, `object_id` and `state`; `getIntegration`, `getRoute` and `getTeam` read `maintenance` {`windows`: active windows, `kept_back_count`} (0 until S-191); the ended windows are pruned by the Leader after `retention.audit_log` (`maintenancewindows_test.go`, `maintenance_integration_test.go`)."
  - "[C-34.FR-2] The migration adds the Permissions `maintenance:read` (Admin, Responder, Viewer) and `maintenance:write` (Admin, Responder), and reference.md moves them from the table of L2 into the main tables, so `TestIntegrationRoleMatrix` counts them (`auth_integration_test.go`)."
verify: "make ci test-integration e2e-go"
operator_attention: true
issue: null
---

# S-190. Maintenance windows with their Permissions, limits, API, Audit log and the timer rows of their start and end (BE)

## Scope

**IN**

- The window as an object: one row covering one Integration, one Route or one Team, a start, an end, a reason and
  `maintenance.urgent_passes`; planned, active and ended by the business clock; created from now or planned, changed,
  closed and deleted by the rules of C-34.FR-3, with `maintenance.limits`, `If-Match` and the refusal of a window on the
  built-in "Muster" Integration (C-34.FR-1, FR-3, FR-4).
- The Permissions `maintenance:read` and `maintenance:write`, the Audit log entries and the API resource
  `maintenance-windows` with `public_id` prefix `MW`, with its `end` operation (C-34.FR-2, FR-5, FR-21).
- The cascade of a deleted Integration, Route or Team, and the active windows on the reads of the three (C-34.FR-3,
  FR-21).
- The timer rows of the start and the end, the log events `maintenance_window_started` and `maintenance_window_ended`,
  and the Leader's pruning of ended windows (C-34.FR-1, FR-22).

**OUT**

- Which Alert Groups a window keeps back, and what happens to them at its start and end: S-191 (the rules and the
  state), S-192 (delivery), S-193 (chains, Notification rules, calls, the ack timeout and Reminders). Until S-191 the
  handlers of the timer rows decide nothing and log a count of 0.
- The `kept_back` filter and fields of Alert Groups: S-191. The metrics: S-191 and S-192.
- The pages: S-194 and S-195. The documentation: S-196. The Terraform resource: specified with the Terraform provider
  (L2 §1.5).

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated): tag `maintenance-windows` with `listMaintenanceWindows`
  (`GET /maintenance-windows`, `maintenance:read`; `cursor`, `limit`, `object_type`, `object_id`, `state`),
  `createMaintenanceWindow` (`POST`, `maintenance:write`), `getMaintenanceWindow`, `updateMaintenanceWindow` (`PUT`,
  `If-Match`), `deleteMaintenanceWindow` (planned only) and `endMaintenanceWindow`
  (`POST /maintenance-windows/{maintenance_window_id}/end`). Schemas `MaintenanceWindow` {`id`, `object` ({`type`:
  `integration`, `route` or `team`; `id`; `name`}), `start`, `end` (RFC 3339 with an offset in, UTC out), `reason`,
  `urgent_passes`, `state`, `created_by` (`ActorRef`), `kept_back_count`}, `MaintenanceWindowInput`,
  `MaintenanceWindowRef`; `Integration`, `Route` and `Team` gain `maintenance` {`windows`, `kept_back_count`};
  `PublicId` and `api/README.md` gain the prefix `MW`; the problem codes of the refusals (`maintenance_window_ended`,
  `maintenance_window_active`, `maintenance_object_builtin`, `out_of_range` at `/start` and `/end`) join
  `internal/api/problem.go`. The Permissions join the `Permission` schema.
- **Schema** (a new migration, expand only; the number is assigned by the orchestrator at dispatch; `schema.md` §4 and
  §5): `maintenance_windows` — `id`, `org_id`, `public_id` (`CHECK ~ '^MW[0-9A-HJKMNP-TV-Z]{12}$'`), `object_type`,
  `integration_id`, `route_id`, `team_id` (exactly one set, by a CHECK; each FK `ON DELETE RESTRICT`, since the owning
  domain closes or deletes the windows first), `starts_at`, `ends_at` (`CHECK (ends_at > starts_at)`), `reason`,
  `urgent_passes boolean NOT NULL DEFAULT false`, `created_by_user_id`, `created_at`, `closed_at`, `version` for the
  ETag; indexes by object and by `(org_id, ends_at)` for the pruning. `timers`: `kind` gains
  `maintenance_window_start` and `maintenance_window_end`, and the table gains `maintenance_window_id` (FK, `ON DELETE
  CASCADE`); the migration replaces `timers_kind_vN_check` and `timers_subject_vN_check` — each added `NOT VALID`,
  validated in a second transaction and the old one dropped in a third, as `0047` and `0049` do — with the next
  versions in the order the stories merge, the subject of these kinds being the window (`alert_group_id`, `storm_id`,
  `escalation_run_id` null) and a unique partial index keeping one timer per window and kind. The Permissions are seeded
  by the migration as `0025` and `0046` do for theirs. The down migration deletes the timers of the new kinds first.
- **Package** (`internal/maintenance`, ADR-0016: flat CRUD, `org_id` in every query): `windows.go` — the rules of
  C-34.FR-3 and the Audit log with a before/after diff; `limits.go` — `maintenance.limits`; `timers.go` — the handlers
  of the two kinds and the interface `maintenance.Effects` (`Started`, `Ended`, `Changed`), which S-191 implements and
  `internal/runtime` wires, returning the number of Alert Groups for the log. The packages `integrations`, `routing` and
  `teams` call `maintenance.CloseOnDelete` in the transaction of their delete; none of them is imported by
  `maintenance`.
- **Settings**: `maintenance.limits` (P-92), `maintenance.presets` (read by the UI), `maintenance.urgent_passes`,
  `retention.audit_log` (now also the ended windows).
- **Log events** (`internal/logging/events.go`; the reference pages regenerate through `internal/tools/refgen`):
  `maintenance_window_started`, `maintenance_window_ended` (INFO).
- **Leader** (`internal/leader/tasks.go`): the retention task deletes ended windows past `retention.audit_log`; it is safe
  to run twice (ADR-0007).
- **Documents**: reference.md moves the two Permissions into the Permissions tables; defaults.md and L2.md §5.3 confirm
  P-92 after the live check below; `docs/install/versions.md` notes the migration.

## Steps

1. Write the migration, `schema.md` and the public id prefix. Check: `make test-integration` runs up, down and up;
   `migrate_test.go` finds the CHECK versions and the Permissions.
2. Write the domain package: rules, limits, state by clock, the Audit log. Check: `windows_test.go`, `limits_test.go`
   with a manual clock (a planned start in the past, a length over 7 days, a start over 90 days ahead, the built-in
   Integration).
3. Write the timer rows and their handlers, and wire `Effects` and the cascade of the three owning domains. Check:
   `timers_test.go`; `maintenance_integration_test.go` closes and deletes on the delete of a Team, a Route and an
   Integration.
4. Add the spec, the handlers and the reads of the three objects. Check: `make generate-check`;
   `maintenancewindows_test.go`.
5. Add the Leader's pruning, the log events and the documents. Check: `make ci test-integration`; `make docs-check`
   is not needed, the reference pages are checked by `make generate-check`.
6. Write `test/e2e/maintenance_windows_test.go`. Check: the Verification below with two replicas.

## Verification

```sh
make dev > dev.log 2>&1 &
API=localhost:8080/api/v1; CLK=localhost:8082/_dev/clock   # A: the admin's token; S: a Service account (Responder) token; V: a Viewer's
NOW=$(curl -s $CLK | jq -r .now)
# a Service account opens a window on the Team "payments" (TP) for 30 minutes
W=$(curl -s -H "$S" -X POST $API/maintenance-windows -d "{\"object\":{\"type\":\"team\",\"id\":\"$TP\"},\"start\":\"$NOW\",\"end\":\"$(date -u -d "$NOW +30 min" +%FT%TZ)\",\"reason\":\"deploy\"}" | jq -r .id)
curl -s -H "$A" $API/maintenance-windows/$W | jq -c '{state, reason, urgent_passes}'   # {"state":"active","reason":"deploy","urgent_passes":false}
curl -s -H "$A" $API/teams/$TP | jq -c '.maintenance.windows | length'                     # 1
curl -s -o /dev/null -w '%{http_code}\n' -H "$V" -X POST $API/maintenance-windows -d '{}'  # 403
curl -s -H "$V" $API/maintenance-windows | jq '.items | length'                            # 1
# refusals
curl -s -H "$A" -X POST $API/maintenance-windows -d "{\"object\":{\"type\":\"team\",\"id\":\"$TP\"},\"start\":\"$NOW\",\"end\":\"$(date -u -d "$NOW +8 days" +%FT%TZ)\"}" | jq -c '{status, code}'   # {"status":422,"code":"out_of_range"}
curl -s -H "$A" -X POST $API/maintenance-windows -d "{\"object\":{\"type\":\"integration\",\"id\":\"$MUSTER_INTEGRATION\"},\"start\":\"$NOW\",\"end\":\"$(date -u -d "$NOW +1 hour" +%FT%TZ)\"}" | jq -c '{status, code}'  # {"status":422,"code":"maintenance_object_builtin"}
# an active window refuses deletion and a change of its start; it is closed, never deleted
curl -s -o /dev/null -w '%{http_code}\n' -H "$A" -X DELETE $API/maintenance-windows/$W   # 409
# a planned window starts and ends at its times, once each, on either replica
P=$(curl -s -H "$A" -X POST $API/maintenance-windows -d "{...a Route R, start now+10 min, end now+20 min...}" | jq -r .id)
curl -s -X POST $CLK -d '{"advance_seconds": 660}' > /dev/null; sleep 2
curl -s -H "$A" $API/maintenance-windows/$P | jq -r .state                                   # active
grep -c "\"event\":\"maintenance_window_started\".*$P" dev.log                               # 1
curl -s -X POST $CLK -d '{"advance_seconds": 700}' > /dev/null; sleep 2
curl -s -H "$A" $API/maintenance-windows/$P | jq -r .state                                   # ended
grep -c "\"event\":\"maintenance_window_ended\".*$P" dev.log                                 # 1
# the Responder closes the first window; the Audit log has both entries
curl -s -H "$A" -X POST $API/maintenance-windows/$W/end | jq -r .state                       # ended
curl -s -H "$A" "$API/audit-log?limit=10" | jq -c '[.items[].action] | map(select(startswith("maintenance_window")))'
# ["maintenance_window.updated","maintenance_window.created", ...]
# deleting the Team closes an active and deletes a planned window
```

`test/e2e/maintenance_windows_test.go` repeats these checks, adds the delete of a Team, a Route and an Integration with
an active and a planned window, a stale `If-Match`, the pruning past `retention.audit_log` with the development clock,
and `E2E_REPLICAS=2` for the timer rows.

## Open questions

1. **Columns.** The PRD names the table and its fields in prose only; this contract names the columns above. The
   maintainer confirms them in review of the story, since S-191 builds on them.
2. **`kept_back_count` before S-191.** It reads 0 on the edge image between this story and S-191; nothing is kept back
   yet, so this is true.

## Notes

- Suggested commit: `feat(maintenance): add maintenance windows with their api, permissions and timer rows`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases. The migration number
  is assigned by the orchestrator at dispatch; it replaces the CHECKs `timers_kind_vN_check` and
  `timers_subject_vN_check` with the next versions in the order the stories merge.
- `operator_attention` is true because the maintainer confirms P-92 (the 7 and 90 days) after the live check, as
  S-131 does for its provisional values.
- Windows never repeat (C-34.FR-1): the documentation of S-196 shows a scheduled CI job.
- A window on the Default route is allowed: it is a Route.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-34.FR-1 | full | |
| C-34.FR-2 | full | |
| C-34.FR-3 | full | |
| C-34.FR-4 | partial | the API's times; the UI's time zone is S-194 |
| C-34.FR-5 | partial | opening, closing, changing and deleting; the start and end as log events here, as lifecycle events of Alert Groups S-191 |
| C-34.FR-21 | partial | the resource and the reads of the three objects; `kept_back` on Alert Groups is S-191, in webhooks S-192 |
| C-34.FR-22 | partial | the log events; the metrics are S-191 and S-192, the opt-in telemetry see Open questions of S-191 |
| C-34.AC-1 | full | |
| C-34.AC-2 | full | |
| C-34.AC-9 | partial | the times and once-only logging; the effects on an Alert Group are S-191 and S-193 |
