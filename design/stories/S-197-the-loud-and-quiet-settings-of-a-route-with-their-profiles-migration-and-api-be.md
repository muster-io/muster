---
id: S-197
title: The Loud and Quiet settings of a Route with their profiles, migration and API (BE)
capability: C-36
kind: be
layer: L2
depends_on: [S-162, S-140, S-154]
covers: [C-36.FR-1, C-36.FR-8, C-36.FR-9, C-36.FR-10, C-36.AC-1, C-36.AC-8]
files_touched:
  - api/openapi.yaml
  - internal/db/migrations/00NN_route_loud_events.up.sql
  - internal/db/migrations/00NN_route_loud_events.down.sql
  - design/db/schema.md
  - internal/db/migrate_test.go
  - internal/routing/routes.go
  - internal/routing/routes_test.go
  - internal/routing/profiles.go
  - internal/routing/profiles_test.go
  - internal/routing/query.sql
  - internal/routing/routing_integration_test.go
  - internal/api/routes.go
  - internal/api/routes_test.go
  - internal/api/server_test.go
  - test/e2e/route_loud_events_test.go
acceptance:
  - "[C-36.FR-1, C-36.AC-8] `createRoute`, `updateRoute`, `getRoute` and `listRoutes` read and write `policy.loud_events` (any of `new_alert_group`, `new_alerts`, `reopen`, `ack_timeout`, `snooze_ended`, `rise_to_urgent`, `storm_started`) and `policy.new_alerts_remove_ack`; a name outside the list gets `400` from request validation, a Loud event listed twice gets `422` with the pointer `/policy/loud_events/{i}`, a missing or stale `If-Match` gets `428` and `412`, and the edit is recorded in the Audit log with a before/after diff of both fields (`routes_test.go`, `routing_integration_test.go`, `test/e2e/route_loud_events_test.go`)."
  - "[C-36.FR-8, C-36.AC-1] `listRouteProfiles` returns Paging with all seven Loud events and Informational with `new_alert_group` only, both with `new_alerts_remove_ack` false; a Route created from the Informational profile stores them (`profiles_test.go`, `routes_test.go`, `test/e2e/route_loud_events_test.go`)."
  - "[C-36.FR-10, C-36.AC-8] After the migration every existing Route, the Default route included, reads all seven Loud events and `new_alerts_remove_ack` false whatever profile it was created with; an update without the two fields keeps the stored values, a create without them takes the values of the Paging profile, so that a client of 0.2.0 keeps working (`migrate_test.go`, `routing_integration_test.go`, `routes_test.go`)."
  - "[C-36.FR-9] `loud_events` keeps `ack_timeout` while the Route's ack timeout is off: turning `policy.ack_timeout.enabled` off or on changes no stored Loud event, and the API reads the value as stored (`routes_test.go`, `routing_integration_test.go`)."
  - "[C-36.FR-1] The names in `loud_events` are the values of the one `MentionEvent` schema that a Destination's Mention entries use, and the domain validates against `mentions.Events`, so a name added there cannot drift from the list of a Route (`routes_test.go`, `make generate-check`)."
verify: "make ci test-integration e2e-go"
operator_attention: false
issue: null
---

# S-197. The Loud and Quiet settings of a Route with their profiles, migration and API (BE)

## Scope

**IN**

- The two Route policy fields, `route.loud_events` and `route.new_alerts_remove_ack`: columns, the migration that gives
  every existing Route the values of L1, the Route API with its validation, `If-Match` and Audit log diff, and the Route
  profiles that pre-fill them (C-36.FR-1, FR-8, FR-10).
- That `ack_timeout` keeps its stored value while the ack timeout is off (C-36.FR-9, the API half).

**OUT**

- What a Loud event governs and how a Quiet row is recorded and delivered: S-198. Until it merges the two fields are
  stored and returned and change nothing in any Alert Group.
- The removal of an acknowledgement by new Alerts: S-199.
- The section of the Route form, with "Ack timeout" unavailable while the ack timeout is off and ticked when it is turned
  on: S-200. The documentation page and the release notes: S-201.

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated): `RoutePolicy` gains `loud_events` (an array of
  `MentionEvent`, the schema of C-28.FR-1, unique items by value) and `new_alerts_remove_ack` (boolean). Both are
  properties of the shared schema and not in its `required` list: optional in the input of `createRoute` and
  `updateRoute` (C-36.FR-10), always present in a read. `RouteProfile.policy` carries both, which is what
  `listRouteProfiles` returns. A duplicate in `loud_events` is refused with `422` `duplicate` at
  `/policy/loud_events/{i}` by the domain with the existing code `duplicate` (`routing.CodeDuplicate`), since OpenAPI's
  `uniqueItems` yields no pointer.
- **Schema** (a new migration, expand only; the number is assigned by the orchestrator at dispatch; `schema.md` §4.8):
  `routes.loud_events text[] NOT NULL DEFAULT` the seven names, `routes.new_alerts_remove_ack boolean NOT NULL DEFAULT
  false`, with a CHECK that `loud_events` holds only the seven names and no name twice. The defaults are what the
  migration gives every existing Route (C-36.FR-10); the application writes both fields on every create, so the defaults
  matter only to a replica of the earlier version, which records every event with the loudness of L1 whatever a Route
  edited meanwhile on a new replica says — C-36.FR-10 accepts this for a rolling update, which
  `docs/operations/upgrades.md` already limits to replicas of two adjacent minor versions. No Alert Group, Timeline entry or Root message changes. The down migration drops
  both columns.
- **Package** (`internal/routing`, ADR-0016: flat CRUD, `org_id` in every query): `Policy` gains `LoudEvents []string`
  and `NewAlertsRemoveAck bool`; `Profiles()` fills them — Paging all seven names in the order of the PRD, Informational
  `new_alert_group` — and the Default route, which takes the Paging profile, gets the same; create without the fields
  takes the Paging values and update without them keeps the stored ones; validation sits with the other policy fields
  (`routes.go`) and reads the closed list `mentions.Events` that Mention entries already use, so there is one list of
  the seven names; the Audit log diff of a Route includes both fields.
- **API handlers** (`internal/api/routes.go`): map the two fields in both directions and add their pointers to the
  problems; no new operation.
- **Settings**: `route.loud_events`, `route.new_alerts_remove_ack` in `defaults.md` (decided; no change needed).
- **Documents**: none beyond `schema.md`; the note on C-32.FR-5 in `coverage.md` already names S-199, which writes the
  removal that the row waited for.

## Steps

1. Write the migration and `schema.md`. Check: `make test-integration` runs up, down and up; `migrate_test.go` finds the
   existing Routes with the seven names and `false`, and the CHECK refusing an eighth name and a repeated one.
2. Write the policy fields, the profiles and the validation. Check: `routes_test.go`, `profiles_test.go`.
3. Add the spec and the handlers. Check: `make generate-check`; `routes_test.go` for the refusals and the
   create/update without the fields.
4. Write `test/e2e/route_loud_events_test.go`. Check: the Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
API=localhost:8080/api/v1   # A: the Admin's token
curl -s -H "$A" $API/route-profiles | jq -c '[.items[] | {id: .id, e: .policy.loud_events, r: .policy.new_alerts_remove_ack}]'
# [{"id":"paging","e":["new_alert_group","new_alerts","reopen","ack_timeout","snooze_ended","rise_to_urgent","storm_started"],"r":false},
#  {"id":"informational","e":["new_alert_group"],"r":false}]
# the Default route and a Route made before the migration read the table of L1
curl -s -H "$A" $API/routes | jq -c '[.items[] | {n: .name, e: (.policy.loud_events | length), r: .policy.new_alerts_remove_ack}]'
# every item: e 7, r false
# a Route from the Informational profile stores its values
R=$(curl -s -H "$A" -X POST $API/routes -d '{"name":"certificates","matchers":[...],"policy":{...Informational policy from the profile...}}' | jq -r .id)
curl -s -H "$A" $API/routes/$R | jq -c '.policy | {loud_events, new_alerts_remove_ack}'   # {"loud_events":["new_alert_group"],"new_alerts_remove_ack":false}
# an update without the two fields keeps them: the body is the Route's policy with both keys removed
E=$(curl -si -H "$A" $API/routes/$R | awk -F': ' 'tolower($1)=="etag"{print $2}' | tr -d '\r')
curl -s -H "$A" -H "If-Match: $E" -X PUT $API/routes/$R -d "{...the same Route, policy without loud_events and new_alerts_remove_ack...}" | jq -c '.policy.loud_events'   # ["new_alert_group"]
# refusals: an unknown name is 400, a repeated name 422 with its pointer
curl -s -o /dev/null -w '%{http_code}\n' -H "$A" -H "If-Match: $E" -X PUT $API/routes/$R -d "{...\"loud_events\":[\"new_alert_group\",\"shout\"]...}"   # 400
curl -s -H "$A" -H "If-Match: $E" -X PUT $API/routes/$R -d "{...\"loud_events\":[\"reopen\",\"reopen\"]...}" | jq -c '{status, pointer: .errors[0].pointer}'   # {"status":422,"pointer":"/policy/loud_events/1"}
# turning the ack timeout on or off does not touch the stored Loud events
curl -s -H "$A" -H "If-Match: $E" -X PUT $API/routes/$R -d "{...\"loud_events\":[\"new_alert_group\"],\"ack_timeout\":{\"enabled\":true,...}...}" | jq -c '.policy.loud_events'   # ["new_alert_group"]
# the Audit log shows the diff
curl -s -H "$A" "$API/audit-log?limit=3" | jq -c '.items[0] | {action, diff: (.diff | keys)}'   # {"action":"route.updated", ...} with policy.loud_events in the diff
psql "$MUSTER_DB" -c "select name, loud_events, new_alerts_remove_ack from routes order by id"   # the table agrees with the API
```

`test/e2e/route_loud_events_test.go` repeats these checks, adds a Route created from each profile and the migration case
(a Route inserted by the earlier schema before the migration runs, in the migration test), and checks that the Audit log
entry names both fields.

## Open questions

None.

## Notes

- Suggested commit: `feat(routing): add the loud events and the removal of an acknowledgement by new alerts to the route policy`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases. The migration number is
  assigned by the orchestrator at dispatch; the migration replaces no CHECK and needs no re-versioning.
- The fields are stored before they act (S-198, S-199): an operator who edits them between S-197 and S-198 on the `edge`
  image sees no effect yet, which is true to the capability's order of delivery.
- `docs/operations/upgrades.md` on a rolling update covers the version skew; S-201 adds the suggestion to untick events
  on Informational Routes after the upgrade.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-36.FR-1 | partial | the fields, the API and the Audit log; that edits apply at once and use the settings of the Alert Group's Route is S-198 |
| C-36.FR-8 | full | |
| C-36.FR-9 | partial | the stored value; the form's unavailable box that ticks on is S-200 |
| C-36.FR-10 | partial | the migration, the defaults and the optional input; the upgrade note's suggestion to untick events is S-201 |
| C-36.AC-1 | partial | the profiles and the stored Route; the form filled from the profile is S-200 |
| C-36.AC-8 | full | the rows of the lifecycle event tables record what they recorded, since S-198 starts from all seven Loud events |
