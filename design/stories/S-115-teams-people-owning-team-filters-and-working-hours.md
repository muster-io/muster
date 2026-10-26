---
id: S-115
title: Teams, people added by hand, the owning Team of Routes and Destinations, the filters by Team, and Working hours (BE)
capability: C-25
kind: be
layer: L2
depends_on: []
covers: [C-25.FR-1, C-25.FR-2, C-25.FR-3, C-25.FR-9, C-25.FR-10, C-25.FR-11, C-25.FR-12, C-25.AC-6, C-25.AC-8, C-25.AC-9, C-25.AC-11, C-25.AC-12, C-03.FR-13, C-08.FR-1, C-20.FR-1, C-20.FR-9]
files_touched:
  - api/openapi.yaml
  - api/README.md
  - internal/db/migrations/0023_teams.up.sql
  - internal/db/migrations/0023_teams.down.sql
  - design/db/schema.md
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - internal/publicid/publicid.go
  - internal/teams/teams.go
  - internal/teams/workinghours.go
  - internal/teams/query.sql
  - internal/teams/teams_test.go
  - internal/teams/teams_integration_test.go
  - internal/api/teams.go
  - internal/api/teams_test.go
  - internal/api/server.go
  - internal/runtime/runtime.go
  - internal/routing/routes.go
  - internal/routing/query.sql
  - internal/routing/routes_test.go
  - internal/api/routes.go
  - internal/destinations/destinations.go
  - internal/destinations/write.go
  - internal/destinations/query.sql
  - internal/destinations/write_test.go
  - internal/api/destinations.go
  - internal/organization/organization.go
  - internal/organization/settings.go
  - internal/organization/defaults.go
  - internal/organization/query.sql
  - internal/organization/organization_test.go
  - internal/api/organization.go
  - internal/users/admin.go
  - internal/users/admin_integration_test.go
  - internal/metrics/catalogue.go
  - design/prd/l1/reference.md
  - design/prd/l1/defaults.md
  - design/prd/L2.md
  - test/e2e/teams_test.go
  - docs/concepts/teams.md
  - mkdocs.yml
  - docs/install/versions.md
acceptance:
  - "[C-25.FR-1, C-25.FR-11] `createTeam` makes a Team with a name and a slug, each unique among the Teams that are not deleted (`409` `name_taken` or `slug_taken`), a description and Working hours; a slug outside `team.slug_format` — `Payments`, `-x`, 64 characters — is refused with `422` `invalid_format` at `/slug`; `getTeam`, `updateTeam` with `If-Match` (`412` when stale, `428` without) and `deleteTeam` work as for Routes; every change is an Audit log entry `team.created`, `team.updated` or `team.deleted` with a before/after diff, and `muster_team_info{team,name}` has one series per Team that is not deleted (`teams_test.go`, `teams_integration_test.go`)."
  - "[C-25.FR-3, C-25.AC-12, C-03.FR-13] `addTeamUser` adds a User to a Team and `removeTeamUser` removes them, each recorded as `team.user_added` and `team.user_removed`; a User may be in several Teams; `listTeamUsers` lists each person with their Role, the source `by_hand` and their linked messengers; deleting a User takes them out of every Team, and disabling one keeps them there (`admin_integration_test.go`)."
  - "[C-25.FR-1, C-25.AC-6] A Responder or a Viewer who is in a Team gets `403` on `updateTeam`, `addTeamUser` and `deleteTeam`, and reads Teams with `teams:read`; a Viewer in a Team is refused Acknowledge with `403` as any Viewer is, so a Team grants and limits nothing (`teams_test.go`)."
  - "[C-25.FR-2, C-25.AC-11] Routes and every Destination type take `owning_team_id`, recorded in their Audit log diff and read back as `owning_team` with the Team's id, name and slug; an unknown or deleted Team is refused with `422` `unknown_id`; `listRoutes` and `listDestinations` take `team` — a Team's `public_id`, or `mine` for the Teams of the calling User (`422` for a Service account token) — and return only what those Teams own (`routes_test.go`, `write_test.go`, `teams_test.go`)."
  - "[C-25.FR-9, C-25.AC-8, C-20.FR-9] `getOrganization` carries `working_days` (Monday to Friday) and `workday_end` (`18:00`) and the derived read-only `working_hours` {days, start = `workday_start`, end, time_zone = `time_zone`}; a Team without its own Working hours reads the Organization's, so a change of `workday_start` moves their start; a Team with `own_working_hours` true takes days, start, end and an IANA time zone; an end not later than the start on the same day is refused with `422` `out_of_range` at `/working_hours/end`, on a Team and on the Organization (`organization_test.go`, `teams_test.go`)."
  - "[C-25.FR-10, C-25.AC-9] Deleting a Team removes its people and its Working hours, leaves the Routes and Destinations it owned without an owner, frees its slug for a new Team and keeps the row, marked deleted, for the names that point at it; the Audit log records `team.deleted` (`teams_integration_test.go`)."
  - "[C-25.FR-12] The documentation has the page Concepts → Teams with what a Team owns and what it does not grant, people added by hand, and Working hours, in the navigation; `make docs-check` passes."
  - "The migration adds the Permissions `teams:read` (Admin, Responder, Viewer) and `teams:write` (Admin), and reference.md moves them, and `muster_team_info`, from their tables of L2 into the main tables; the provisional values P-63 (Working hours end later than they start) and P-74 (the slug format) are confirmed in defaults.md and L2.md §5.3 (Notes)."
verify: "make ci test-integration e2e-go docs-check"
operator_attention: true
issue: null
---

# S-115. Teams, people added by hand, the owning Team of Routes and Destinations, the filters by Team, and Working hours (BE)

## Scope

**IN**

- The new domain package `internal/teams`: Teams with a name, a slug, a description and Working hours; people added by
  hand; deletion with the owned objects left without an owner (C-25.FR-1, FR-3, FR-9, FR-10, FR-11).
- The owning Team of Routes and Destinations, and the filter by Team and "My Teams" of their lists (C-25.FR-2).
- The Organization's Working hours (`organization.working_hours`) beside `organization.workday_start` (C-25.FR-9,
  C-20.FR-1 revised).
- The Permissions `teams:read` and `teams:write`, the metric `muster_team_info`, the public id prefix `TM` and the live
  hint `team`.
- A User's deletion that takes them out of every Team (C-03.FR-13 revised).
- The first sections of the documentation page on Teams.

**OUT**

- Identity provider entries and membership from the identity provider: S-117 (after the spike S-116).
- The Team label, Team matchers, the Teams of Alerts and Alert Groups, unknown values and the filter of the Alert Group
  list by Team: S-118.
- The Destinations of a Team: S-121. Team mentions: S-139. Schedules, with the refusal `team_in_use` for a Team that
  has them: S-131; the same for Escalation chains and their owning Team: S-154.
- The Teams pages, the filters and the owning Team on the forms, the Working hours on the General page: S-119.
- The rest of the documentation page on Teams: S-117, S-118 and S-124.

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated; a new tag "Teams"):
  - Operations: `listTeams` (`GET /teams`, `teams:read`; `cursor`, `limit`, `q` on name and slug), `createTeam`
    (`POST /teams`, `teams:write`), `getTeam`, `updateTeam` (`PUT` with `If-Match`), `deleteTeam` (optional
    `If-Match`) on `/teams/{team_id}`; `listTeamUsers` (`GET /teams/{team_id}/users`, `teams:read`), `addTeamUser`
    (`POST /teams/{team_id}/users`, `teams:write`, body `TeamUserAdd` {`user_id`}), `removeTeamUser` (`DELETE
    /teams/{team_id}/users/{user_id}`, `teams:write`). Session or token, `x-listener: app`; the path parameter
    `TeamId`.
  - Schemas: `TeamInput` {`name`, `slug`, `description`, `own_working_hours` (boolean), `working_hours`
    (`WorkingHours`, required when `own_working_hours`)}; `Team` = `TeamInput` + `id`, `user_count`, `created_at`,
    `etag`, with `working_hours` always the effective ones; `TeamList`; `TeamRef` {`id`, `name`, `slug`, `deleted`};
    `TeamUser` {`user` (`UserRef`), `role` (`RoleName`), `sources` (array of `by_hand`, and `idp` from S-117),
    `linked_messengers` (array of {`type`: `ConnectionType`, `connection`: `EntityRef`})}; `TeamUserList`;
    `TeamUserAdd`; `WorkingHours` {`days` (`Weekday`[], at least one), `start`, `end` (`HH:MM`), `time_zone`}.
  - `OrganizationBase` gains `working_days` (`Weekday`[]) and `workday_end` (`HH:MM`), both optional (omitted keeps);
    `Organization` requires them and gains the read-only `working_hours` (`WorkingHours`).
  - `RouteInput` gains `owning_team_id` (`NullablePublicId`, optional; `null` clears); `Route` gains `owning_team`
    (`TeamRef` or null). Every messenger `*DestinationInput` and `WebhookDestinationBase` gain `owning_team_id`, and the
    reads `owning_team`. The name is `owning_team_id`, not `team_id`, because `MattermostDestinationInput.team_id` is the
    Mattermost team.
  - `listRoutes` and `listDestinations` gain the query parameter `team` (a Team `public_id` or `mine`).
  - `Permission` gains `teams:read` and `teams:write`; `HintEvent.type` gains `team`; `PublicId` and `api/README.md`
    gain the prefix `TM` (Team).
  - Problem codes: `slug_taken` (`409` `conflict`, the slug of a Team that is not deleted); `team_in_use` (`409`
    `conflict`, declared here for S-131 and S-154, which raise it); `invalid_format` at `/slug`; `out_of_range` at
    `/working_hours/end` and `/workday_end`; `unknown_id` at `/owning_team_id` and `/user_id`.
- **Schema** (migration `0023_teams`, expand only; `schema.md` §2, a new §4.15 Teams, §4.2, §4.5, §4.8):
  - `teams`: `id`, `org_id`, `public_id` (`CHECK (public_id ~ '^TM[0-9A-HJKMNP-TV-Z]{12}$')`), `name`, `slug`
    (`CHECK (slug ~ '^[a-z0-9][a-z0-9._-]{0,62}$')`), `description`, `own_working_hours boolean NOT NULL DEFAULT
    false`, `working_days text[]`, `working_start`, `working_end`, `working_time_zone` (all set exactly when
    `own_working_hours`, and `working_end > working_start`, by CHECKs), `version`, `created_at`, `updated_at`,
    `deleted_at`; unique indexes on `(org_id, lower(name))` and `(org_id, slug)` `WHERE deleted_at IS NULL`.
  - `team_users`: `org_id`, `team_id`, `user_id`, `by_hand boolean NOT NULL`, `created_at`; primary key
    `(team_id, user_id)`; the index `(org_id, user_id)` serves "My Teams".
  - `routes.owning_team_id` and `destinations.owning_team_id`, nullable foreign keys to `teams`, with partial indexes
    for the filter.
  - `organizations.working_days text[] NOT NULL DEFAULT '{monday,tuesday,wednesday,thursday,friday}'` and
    `organizations.workday_end text NOT NULL DEFAULT '18:00'` with the pattern of `workday_start`, and `CHECK
    (workday_end > workday_start)` added `NOT VALID` and validated in a second transaction. An Organization whose
    `workday_start` is 18:00 or later (P-58 allows any time) gets `workday_end` `23:59` from the up migration before
    the CHECK is validated (Open question 1); afterwards an update that would leave the end not later than the start
    is refused with `422` `out_of_range` at the field it changes, `/workday_start` or `/workday_end`.
  - `permissions` gains `teams:read` and `teams:write`, and `role_permissions` gives `teams:read` to every Role and
    `teams:write` to Admin — reference data, as in `0001`.
  - The down migration drops all of it.
- **Teams** (`internal/teams`, a flat domain package, ADR-0016): validates, writes through its queries, records the
  Audit log (`team.created`, `team.updated`, `team.deleted`, `team.user_added`, `team.user_removed`) and sends the
  hint `team`; `deleteTeam` soft-deletes the row, deletes its `team_users`, sets `owning_team_id` to null on its Routes
  and Destinations in the same transaction (each with its own Audit log entry `route.updated` and
  `destination.updated` by the system) and, until S-131 and S-154, refuses nothing. `workinghours.go` holds
  `WorkingHours`, the validation, `Effective(team, org)` — the Team's own or the Organization's — and `Contains(t)`,
  which the Escalation steps of S-154 use. "My Teams" is resolved from `team_users` of the caller's User.
- **Routing and Destinations**: `owning_team_id` in their inputs, views, Audit log diffs and list filters; neither adds
  delivery (C-26.FR-7).
- **Organization** (`internal/organization`): `working_days` and `workday_end` in the read, update, validation,
  defaults and diff; `working_hours` derived.
- **Users** (`internal/users/admin.go`): deleting a User deletes their `team_users` rows in the same transaction;
  disabling keeps them.
- **Metric** (`internal/metrics/catalogue.go`): `muster_team_info{team,name}`, C-25, moved in reference.md from
  "Metrics added in L2" into the catalogue, which `refgen` and `metrics_test.go` check.
- **Shared tables**: reference.md moves `teams:read` and `teams:write` from "Permissions added in L2" into the main
  table and the `Permission` schema; defaults.md marks `team.slug_format` and the rule of P-63 decided; L2.md §5.3
  moves P-63 and P-74 to "Decided since they were first listed".
- **Settings**: `team.slug_format` (P-74), `team.working_hours`, `organization.working_hours`,
  `organization.workday_start`, `organization.time_zone`.
- **Documentation**: the new page `docs/concepts/teams.md` — "What a Team is", "People" and "Working hours" — in the
  navigation under Concepts; `docs/install/versions.md` — the end of the working day that 0.2.0 sets where the start
  is 18:00 or later.

## Steps

1. Write the migration and `schema.md`. Check: `make test-integration` runs up, down and up on PostgreSQL 14 and 17;
   the Permissions of each Role read from `role_permissions` as in the acceptance; `TestIntegrationTeams` migrates an
   Organization whose `workday_start` is `20:00` and finds `workday_end` `23:59` and the CHECK validated.
2. Write `internal/teams` with the operations, the Audit log, the metric and the hint. Check: `teams_test.go` covers
   every validation, conflict and Permission, and the effective Working hours; `make generate-check`.
3. Add the owning Team and the filters to Routes and Destinations, and the Working hours to the Organization. Check:
   `routes_test.go`, `write_test.go`, `organization_test.go`.
4. Take deleted Users out of their Teams. Check: `admin_integration_test.go`.
5. Move the shared rows, confirm the provisional values, write the documentation and `test/e2e/teams_test.go`. Check:
   `make docs-check`; the Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-032: the Admin's Personal access token A, the Responder "bob" with his token B, and a Viewer "vic" with the
# token V; the Paging policy in P as in S-111.
API=localhost:8080/api/v1
BOB=$(curl -s "${A[@]}" "$API/users?q=bob" | jq -r '.items[0].id')
T=$(curl -s "${A[@]}" $API/teams -d '{"name":"Payments","slug":"payments","description":"","own_working_hours":false}')
TP=$(jq -r .id <<<"$T"); jq -c '{slug, own_working_hours, w: .working_hours}' <<<"$T"
# {"slug":"payments","own_working_hours":false,"w":{"days":["monday","tuesday","wednesday","thursday","friday"],"start":"09:00","end":"18:00","time_zone":"UTC"}}
curl -s "${A[@]}" $API/teams -d '{"name":"Payments B","slug":"Payments","own_working_hours":false}' | jq -c '{status, p: .errors[0].pointer, c: .errors[0].code}'
# {"status":422,"p":"/slug","c":"invalid_format"}
curl -s "${A[@]}" $API/teams -d '{"name":"Other","slug":"payments","own_working_hours":false}' | jq -c '{status, code}'
# {"status":409,"code":"slug_taken"}

# C-25.FR-3: people by hand; C-25.AC-6: a Team grants nothing
curl -s "${A[@]}" $API/teams/$TP/users -d "{\"user_id\":\"$BOB\"}" > /dev/null
curl -s "${A[@]}" $API/teams/$TP/users | jq -c '[.items[] | {u: .user.name, role, sources}]'   # [{"u":"bob","role":"responder","sources":["by_hand"]}]
curl -s -o /dev/null -w '%{http_code}\n' "${B[@]}" -X DELETE $API/teams/$TP                        # 403
curl -s -o /dev/null -w '%{http_code}\n' "${V[@]}" $API/teams                                      # 200

# C-25.FR-9, C-25.AC-8: Working hours
TS=$(curl -s "${A[@]}" $API/teams -d '{"name":"Search","slug":"search","own_working_hours":true,
  "working_hours":{"days":["monday","friday"],"start":"19:00","end":"11:00","time_zone":"Europe/Berlin"}}')
jq -c '{status, p: .errors[0].pointer, c: .errors[0].code}' <<<"$TS"   # {"status":422,"p":"/working_hours/end","c":"out_of_range"}
O=$(curl -s "${A[@]}" $API/organization)
curl -s "${A[@]}" -X PUT -H "If-Match: $(jq -r .etag <<<"$O")" $API/organization -d "$(jq -c 'del(.id, .etag, .working_hours) | .workday_start = "08:00"' <<<"$O")" > /dev/null
curl -s "${A[@]}" $API/teams/$TP | jq -r .working_hours.start                                     # 08:00

# C-25.FR-2, C-25.AC-11: the owning Team and "My Teams"
R=$(curl -s "${A[@]}" $API/routes -d "{\"name\":\"pay\",\"matchers\":[{\"label\":\"team\",\"op\":\"=\",\"value\":\"payments\"}],
  \"owning_team_id\":\"$TP\",\"group_key\":[\"alertname\"],\"destination_ids\":[],\"policy\":$P}" | jq -r .id)
curl -s "${B[@]}" "$API/routes?team=mine" | jq -c '[.items[].name]'                               # ["pay"]
curl -s "${V[@]}" "$API/routes?team=mine" | jq -c '[.items[].name]'                               # []
curl -s "${A[@]}" $API/routes/$R | jq -c .owning_team                    # {"id":"TM…","name":"Payments","slug":"payments","deleted":false}

# C-25.FR-10, C-25.AC-9: deletion
curl -s -o /dev/null -w '%{http_code}\n' "${A[@]}" -X DELETE $API/teams/$TP                       # 204
curl -s "${A[@]}" $API/routes/$R | jq -c .owning_team                                             # null
curl -s "${A[@]}" $API/teams -d '{"name":"Payments","slug":"payments","own_working_hours":false}' | jq -r .slug   # payments

# C-25.AC-12: deleting a User takes them out of every Team
TP2=$(curl -s "${A[@]}" "$API/teams?q=payments" | jq -r '.items[0].id'); curl -s "${A[@]}" $API/teams/$TP2/users -d "{\"user_id\":\"$BOB\"}" > /dev/null
curl -s "${A[@]}" -X POST $API/users/$BOB/disable > /dev/null; curl -s "${A[@]}" $API/teams/$TP2 | jq .user_count   # 1
curl -s "${A[@]}" -X DELETE $API/users/$BOB > /dev/null; curl -s "${A[@]}" $API/teams/$TP2 | jq .user_count         # 0
curl -s localhost:8082/metrics | grep -c '^muster_team_info{'                                                       # 1
```

`test/e2e/teams_test.go` repeats these checks.

## Open questions

1. **The end of the Organization's working day where the start is late.** `organization.workday_start` may already be
   any time (P-58), but the Organization's Working hours must end later than they start (P-63), and their end is new.
   Options: (a) the migration sets `workday_end` to `23:59` where the start is 18:00 or later, and later updates must
   keep the end after the start, refused with `422` `out_of_range` otherwise; (b) no rule across the two fields, and
   Working hours whose end is not after their start end at midnight; (c) the migration sets the end nine hours after
   the start, capped at `23:59`. Recommended: (a), the plain reading of P-63; the contract above follows it, and the
   upgrade notes of 0.2.0 name the Organizations it changes.

## Notes

- Suggested commit: `feat(teams): add teams with people, owners and working hours`.
- The maintainer confirms two provisional values with this story (operator attention): P-63, Working hours end later
  than they start on the same day, with no span across midnight; and P-74, the slug format of `team.slug_format`. P-64,
  the deletion rule, is confirmed by S-131, once a Team with Schedules is refused.
- `team.deleted` keeps the row because Alert Groups name the Teams they had (S-118); the slug is unique only among the
  Teams that are not deleted, so it becomes free at once.
- The owning Team is called `owning_team_id` throughout the API — Routes, Destinations and, from S-154, Escalation
  chains — because `team_id` is taken by the Mattermost team of a Mattermost Destination.
- A Team grants no Permission and limits none (ADR-0018): no check of this story looks at a Team; the only one that
  ever will is the precondition of Overrides (S-131).
- With 42 files the story is at the edge of the size signal; the Working hours of the Organization and of a Team are
  one rule (C-25.FR-9), and the owning Team of Routes and Destinations is needed by the filter that checks it.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-25.FR-1 | partial | the Team, its people by hand and Working hours; identity provider entries S-117, Team matchers S-118, Team mentions S-139 |
| C-25.FR-2 | partial | the owning Team of Routes and Destinations and their filters; the Alert Group list S-118, the pages S-119, chains S-154 |
| C-25.FR-3 | partial | the API; the page is S-119 |
| C-25.FR-9 | partial | the API; the pages are S-119, the Escalation steps S-154 |
| C-25.FR-10 | partial | deletion and the owned objects; Alert Groups that had the Team S-118, the refusal for Schedules S-131 and chains S-154 |
| C-25.FR-11 | partial | locking, the Audit log and `muster_team_info`; identity provider entries S-117, Team matchers S-118, Team mentions S-139 |
| C-25.FR-12 | partial | the first sections of the page; S-117, S-118, S-124 and S-144 complete it |
| C-25.AC-6 | full | |
| C-25.AC-8 | full | |
| C-25.AC-9 | partial | deletion and the Route without owner; the Alert Group S-118, the Schedule S-131 |
| C-25.AC-11 | partial | Routes and Destinations in the API; Alert Groups S-118, the pages S-119 and S-120 |
| C-25.AC-12 | full | |
| C-03.FR-13 | partial | revised for L2: out of every Team; gateway contacts are S-145 |
| C-08.FR-1 | partial | revised for L2: the owning Team |
| C-20.FR-1 | partial | revised for L2: the Working hours in the API; the General page is S-119 |
| C-20.FR-9 | partial | revised for L2: the start of the working day starts the Organization's Working hours; the General page is S-119 |
