---
id: S-131
title: Schedules, Rotation layers, Overrides with their Permission and precondition, the On-call and the Result (BE)
capability: C-27
kind: be
layer: L2
depends_on: [S-115]
covers: [C-27.FR-1, C-27.FR-2, C-27.FR-3, C-27.FR-4, C-27.FR-5, C-27.FR-6, C-27.FR-11, C-27.FR-12, C-27.FR-13, C-27.FR-14, C-27.AC-1, C-27.AC-2, C-27.AC-3, C-27.AC-4, C-27.AC-8, C-27.AC-10, C-25.FR-1, C-25.FR-2, C-25.FR-10, C-25.AC-9]
files_touched:
  - api/openapi.yaml
  - api/README.md
  - internal/db/migrations/0025_schedules.up.sql
  - internal/db/migrations/0025_schedules.down.sql
  - design/db/schema.md
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - internal/publicid/publicid.go
  - internal/publicid/publicid_test.go
  - sqlc.yaml
  - internal/schedules/schedules.go
  - internal/schedules/rotation.go
  - internal/schedules/rotation_test.go
  - internal/schedules/rotation_oracle_test.go
  - internal/schedules/result.go
  - internal/schedules/result_test.go
  - internal/schedules/overrides.go
  - internal/schedules/overrides_test.go
  - internal/schedules/query.sql
  - internal/schedules/schedules_test.go
  - internal/schedules/schedules_integration_test.go
  - internal/api/schedules.go
  - internal/api/schedules_test.go
  - internal/api/problem.go
  - internal/api/server.go
  - internal/api/server_test.go
  - internal/runtime/runtime.go
  - internal/runtime/runtime_test.go
  - internal/teams/teams.go
  - internal/teams/query.sql
  - internal/teams/teams_test.go
  - internal/metrics/catalogue.go
  - internal/metrics/metrics.go
  - internal/metrics/metrics_test.go
  - internal/tools/refgen/main.go
  - internal/auth/auth_integration_test.go
  - design/prd/l1/reference.md
  - design/prd/l1/defaults.md
  - design/prd/L2.md
  - design/prd/l2/C-25-teams.md
  - design/prd/l2/C-27-schedules.md
  - docs/install/versions.md
  - test/e2e/schedules_test.go
  - docs/concepts/schedules.md
  - mkdocs.yml
acceptance:
  - "[C-27.FR-1, C-27.FR-13, C-25.FR-1, C-25.FR-2] `createSchedule` makes a Schedule of a Team with a name unique in that Team (`409` `name_taken`), a time zone — the Organization's when omitted (`schedule.time_zone`) — and its Rotation layers; the first Schedule of a Team reads `main` true and a later one `main` false; `updateSchedule` with `main` true makes it the Team's main Schedule and the previous one not main in one transaction; `getSchedule`, `updateSchedule` with `If-Match` (`412` when stale, `428` without) and `deleteSchedule` work as for Teams; every change is an Audit log entry `schedule.created`, `schedule.updated` or `schedule.deleted` with a before/after diff, and `muster_schedule_info{schedule,name}` has one series per Schedule (`schedules_test.go`, `schedules_integration_test.go`)."
  - "[C-27.FR-2] A Rotation layer limited to Friday 19:00–09:00 has its Shift from Friday 19:00 to Saturday 09:00, which belongs to Friday: its person is On-call on Friday at 23:00 and on Saturday at 08:00, nobody on Saturday at 10:00 and on Thursday at 23:00; a window whose end equals its start is refused with `422` `out_of_range` at `/rotation_layers/{i}/limits/end` (`rotation_test.go`, `schedules_test.go`)."
  - "[C-27.FR-2, C-27.AC-1] With the two Rotation layers of scenario 1 in Europe/Berlin — below, weekly Shifts handed over on Monday at 10:00 among petr, sergey and olga; above, weekly Shifts limited to Monday to Friday 11:00–19:00 among olga and petr — `getScheduleOnCall` gives the upper Rotation layer's person on a Wednesday at 15:00 and the lower one's at 21:00 and on the Saturday at 15:00; the lower Rotation layer's handover is at 09:00Z on the Monday before the last Sunday of March and at 08:00Z on the Monday after it, 10:00 in Berlin both times (`rotation_test.go`, `schedules_test.go`)."
  - "[C-27.FR-2, C-27.AC-3] Saving a Rotation layer that names a Viewer, a disabled User or a User who is not in the Team is refused with `422` `not_pageable` at the person's pointer, `/rotation_layers/{i}/user_ids/{j}`, with the `reason` `viewer`, `disabled` or `not_in_team`, for the people the save adds to the Rotation layer; a person already in it whom the stored version keeps — one disabled since — does not refuse the save, so a rename of the Schedule still saves; a Shift length other than a day or a week takes `shift_seconds`, whole hours from one hour — a multiple of 3600 from 3600; `422` `out_of_range` at `/rotation_layers/{i}/shift_seconds` otherwise, 5400 included (`schedules_test.go`)."
  - "[C-27.FR-3, C-27.FR-6, C-27.AC-2, C-27.AC-10] An Override that makes petr On-call on a Wednesday 11:00–21:00 makes him On-call from 11:00 to 19:00 over the upper Rotation layer and from 19:00 to 21:00 over the lower one, and leaves the On-call of 10:00 and 22:00 unchanged; it records `schedule_override.created` with its people and span, and the fake Mattermost, Telegram, Slack and bridge servers record no new message or direct message; of two Overrides that overlap, the person of the one created last is On-call in the overlap; an Override with `replaces_user_id` changes nothing in the On-call (`result_test.go`, `overrides_test.go`, `schedules_test.go`)."
  - "[C-27.FR-6, C-27.AC-3] `createScheduleOverride` by a Responder who is not in the Team gets `409` `not_in_team`, by a Viewer `403`; an Override whose person who takes the Shift is a Viewer, disabled or outside the Team gets `422` `not_pageable` at `/user_id`, while one whose `replaces_user_id` names a disabled User or someone outside the Team is saved; an Admin who is not in the Team creates one; `updateScheduleOverride` with a stale `If-Match` gets `412`; an Override that ends before the business clock's now, or whose end is not after its start, gets `422` `out_of_range` at `/end`; changing or deleting an Override that has ended gets `409` `override_ended` (`overrides_test.go`, `schedules_test.go`)."
  - "[C-27.FR-4, C-27.FR-5, C-27.AC-4] After a User of the upper Rotation layer is disabled — or turned into a Viewer, or taken out of the Team — the On-call during their Shifts is the person of the lower Rotation layer, `getScheduleOnCall` lists the warning `disabled`, `viewer` or `not_in_team` with the person and the Rotation layer, and where no Override and no other Rotation layer has a Shift with someone who may be Paged the segment has no person: a gap; an Override whose person may not be Paged is skipped the same way (`result_test.go`)."
  - "[C-27.FR-13, C-27.AC-10] A Responder gets `403` on `createSchedule`, `updateSchedule` and `deleteSchedule`; a Viewer reads Schedules, Overrides and the On-call with `schedules:read` (`schedules_test.go`)."
  - "[C-27.FR-12, C-27.AC-8] Making another Schedule main changes the Team's On-call that `getScheduleOnCall` of the new main Schedule gives; deleting the main Schedule leaves the Team without one — no Schedule reads `main` true — until an Admin marks another; `deleteSchedule` checks the `schedule_in_use` refusal through `schedules.Referrers`, which names no chain until S-154 (`schedules_integration_test.go`)."
  - "[C-25.FR-10, C-25.AC-9] `deleteTeam` of a Team with a Schedule is refused with `409` `team_in_use` naming the Schedule in `in_use_by`; after the Schedule is deleted the Team is deleted (`teams_test.go`)."
  - "[C-27.FR-11] Creating, changing and deleting Schedules, Rotation layers and Overrides sends nothing to any messenger or outgoing webhook: the fake servers record no new message, direct message or webhook request (`test/e2e/schedules_test.go`)."
  - "[C-27.FR-14] The documentation has the page Concepts → Schedules with \"What a Schedule is\", \"Rotation layers\", \"Several Schedules\" and \"Overrides\", in the navigation; `make docs-check` passes."
  - "The migration adds the Permissions `schedules:read` (Admin, Responder, Viewer), `schedules:write` (Admin) and `schedule-overrides:write` (Admin, Responder), and reference.md moves them, and `muster_schedule_info`, from their tables of L2 into the main tables, so that `TestIntegrationRoleMatrix`, which reads the matrix from reference.md and the seeded allocation, counts 36, 15 and 10 Permissions for Admin, Responder and Viewer instead of S-115's 33, 13 and 9 (`auth_integration_test.go`); P-64, P-65, P-66, P-77, P-90 and P-91 are confirmed in defaults.md and L2.md §5.3 (Notes)."
verify: "make ci test-integration e2e-go docs-check"
operator_attention: true
issue: 420
---

# S-131. Schedules, Rotation layers, Overrides with their Permission and precondition, the On-call and the Result (BE)

## Scope

**IN**

- The new domain package `internal/schedules`: Schedules of a Team with a time zone and a main Schedule, Rotation
  layers with their Shifts and limits, Overrides on top (C-27.FR-1 to FR-3).
- The On-call of a Schedule at a moment and its Result over a period — segments with a person or a gap — on the
  business clock, with the people who may not be Paged skipped and the warnings about them (C-27.FR-4, FR-5).
- Overrides with the Permission `schedule-overrides:write` and the precondition "in the Team or an Admin"; no messages
  for Shifts and Overrides (C-27.FR-6, FR-11).
- Schedule deletion with P-66 and the code `schedule_in_use` declared for S-154; the refusal `team_in_use` of a Team
  that has Schedules (C-27.FR-12, C-25.FR-10).
- The Permissions `schedules:read`, `schedules:write`, `schedule-overrides:write`, the metric `muster_schedule_info`,
  the public id prefixes `SC`, `RY` and `VR`, the live hint `schedule`, and the first sections of the documentation page
  on Schedules (C-27.FR-13, FR-14).

**OUT**

- Gaps of the next `schedule.gap_view` on the Schedule, the Leader's scan with `MusterScheduleGap`, `schedule.gap_alert`
  and "On call now": S-133. Warnings about unreachable On-calls: S-164. The iCal feed: S-134.
- The Schedule page, the Override form with its two modes and the Schedules list: S-135. Schedules on the Team page and
  "On call now" on the pages: S-136. The rest of the documentation: S-137.
- Escalation steps that name a Schedule, which raise `schedule_in_use`, and the owning Team's chains: S-154.

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated; a new tag "Schedules"):
  - Operations: `listSchedules` (`GET /schedules`, `schedules:read`; `cursor`, `limit`, `team` — a Team's
    `public_id` or `mine`, `422` for a Service account token — and `q` on the name), `createSchedule` (`POST
    /schedules`, `schedules:write`), `getSchedule`, `updateSchedule` (`PUT` with `If-Match`), `deleteSchedule`
    (optional `If-Match`) on `/schedules/{schedule_id}`; `getScheduleOnCall` (`GET /schedules/{schedule_id}/on-call`,
    `schedules:read`; `from` and `to`, RFC 3339, by default the business clock's now and `schedule.gap_view` after it,
    each at most 10 years from now, else `422` `out_of_range` at `/query/from` or `/query/to`, and at most the length
    of `ical.window` apart, `422` `out_of_range` at `/query/to` otherwise); `listScheduleOverrides` (`GET
    /schedules/{schedule_id}/overrides`, `schedules:read`; `from`, `to`, `cursor`, `limit`), `createScheduleOverride`
    (`POST`, `schedule-overrides:write`), `updateScheduleOverride` (`PUT` with `If-Match`) and `deleteScheduleOverride`
    (`DELETE` with optional `If-Match`) on `/schedules/{schedule_id}/overrides/{override_id}`. Session or token,
    `x-listener: app`; the path parameters `ScheduleId` and `OverrideId`.
  - Schemas: `ScheduleInput` {`team_id` (required on create, fixed afterwards: a different one on update is refused
    with `422` `unsupported` at `/team_id`, settled 4; `main` false on the main Schedule is refused the same way at
    `/main`, settled 5), `name`, `time_zone` (optional on
    create), `main` (optional), `rotation_layers` (`RotationLayerInput`[], from the lowest to the highest)};
    `Schedule` = `ScheduleInput` + `id`, `team` (`TeamRef`), `rotation_layers` (`RotationLayer`[]), `created_at`,
    `etag`; `ScheduleList`; `ScheduleRef` {`id`, `name`, `team` (`TeamRef`)}; `RotationLayerInput` {`id` (optional, an
    existing Rotation layer's, which keeps its identity), `name`, `user_ids` (`PublicId`[], at least one, in turn),
    `shift_length` (`day`, `week`, `hours`), `shift_seconds` (integer, a multiple of 3600 and at least 3600, required
    with `hours` and refused with the others — durations are `*_seconds` in the API, `api/README.md`), `handover_time`
    (`HH:MM`), `handover_day` (`Weekday`, required with `week`), `start_date` (a date), `limits` (`RotationLimits` or
    null)}; `RotationLimits` {`days` (`Weekday`[], at least one), `start` and `end` (`HH:MM`, optional together; an
    `end` earlier than `start` crosses midnight, and an `end` equal to `start` is refused)};
    `RotationLayer` = `RotationLayerInput` + `id`, `number` (1 for the lowest), `users` (`UserRef`[]);
    `ScheduleOverrideInput` {`user_id` (who takes the Shift), `replaces_user_id` (nullable), `start`, `end`};
    `ScheduleOverride` = it + `id`, `user`, `replaces_user` (`UserRef` or null), `created_by` (`ActorRef`),
    `created_at`, `etag`; `ScheduleOverrideList`; `ScheduleOnCall` {`from`, `to`, `segments` (`OnCallSegment`[]),
    `rotation_layers` ({`rotation_layer` (`EntityRef`), `shifts` ({`start`, `end`, `user`, `skipped` — null, or the
    reason `viewer`, `disabled`, `not_in_team`})[]})[], `overrides` (`ScheduleOverride`[] in the period), `warnings`
    (`ScheduleWarning`[])}; `OnCallSegment` {`start`, `end`, `user` (`UserRef` or null for a gap), `source` — `override`
    with its id, or `rotation_layer` with its ref, or null in a gap}; `ScheduleWarning` {`kind` — `disabled`, `viewer`,
    `not_in_team` here, `gap` from S-133, `unreachable` from S-164; `user` (`UserRef` or null), `rotation_layer`
    (`EntityRef` or null), `override_id` (or null)}. The warnings of this story list each person of a Rotation layer or
    of an Override in the period who may not be Paged, once per person and Rotation layer.
  - `Permission` gains `schedules:read`, `schedules:write`, `schedule-overrides:write`; `HintEvent.type` gains
    `schedule`; `PublicId` and `api/README.md` gain the prefixes `SC` (Schedule), `RY` (Rotation layer) and `VR`
    (Override), and the tag list "Schedules".
  - Problem codes: `not_pageable` (`validation-failed`, in `errors[].code`, with `reason` — `viewer`, `disabled`,
    `not_in_team` — in the error item; reused by the Escalation steps of S-154); `not_in_team` (`409` `conflict`);
    `schedule_in_use` (`409` `conflict`, with `in_use_by`, declared here and raised by S-154). `in_use_by` is a new
    optional property of `Problem`: `{type, id, name}[]`, the kind of each object that holds the resource (`schedule`
    here, `escalation_chain` from S-154), its `public_id` and its name; `team_in_use` (declared
    in S-115) raised for a Team with Schedules, naming them in `in_use_by`; `name_taken`; `out_of_range` at `/end`,
    `/query/to`, `/rotation_layers/{i}/shift_seconds` and `/rotation_layers/{i}/limits/end`; `override_ended` (`409`
    `conflict`) for a change or a deletion of an Override that has ended.
  - `internal/api/problem.go`: `problemFor` maps the errors of `schedules` — not found, `name_taken`, a stale version,
    `not_in_team`, `override_ended`, `schedule_in_use` and `team_in_use` with `in_use_by` — and `Problem` gains the
    member `InUseBy` that `writeProblem` writes as `in_use_by`; the L1 `inUseProblem` of Lookup tables generalizes to
    it, its `LinkRules` staying as they are; the error item of `not_pageable` gains `reason` through `gen.ProblemError`.
- **Schema** (migration `0025_schedules`, expand only; `schema.md` §2, a new §4.16 Schedules, §4.15):
  - `schedules`: `id`, `org_id`, `public_id` (`CHECK (public_id ~ '^SC[0-9A-HJKMNP-TV-Z]{12}$')`), `team_id` (FK
    `teams`), `name`, `time_zone`, `main boolean NOT NULL DEFAULT false`, `version`, `created_at`, `updated_at`;
    unique `(team_id, lower(name))`; unique partial index `(team_id) WHERE main` — one main Schedule per Team.
  - `rotation_layers`: `id`, `org_id`, `public_id` (`RY`), `schedule_id` (FK, `ON DELETE CASCADE`), `position` (1 for
    the lowest), `name`, `shift_length` (`CHECK IN ('day', 'week', 'hours')`), `shift_seconds` (set exactly with
    `hours`, a positive multiple of 3600), `handover_time` (`HH:MM` pattern), `handover_day` (set exactly with `week`),
    `start_date date`, `limit_days text[]`, `limit_start`, `limit_end` (`limit_end <> limit_start` when both are set —
    an end earlier than the start crosses midnight); unique `(schedule_id, position)`.
  - `rotation_layer_users`: `org_id`, `rotation_layer_id` (FK, cascade), `position`, `user_id` (FK `users`); primary
    key `(rotation_layer_id, position)`.
  - `schedule_overrides`: `id`, `org_id`, `public_id` (`VR`), `schedule_id` (FK, cascade), `user_id`,
    `replaces_user_id` (nullable), `starts_at`, `ends_at` (`CHECK (ends_at > starts_at)`), `created_by_user_id`,
    `created_by_service_account_id` (exactly one, by a CHECK), `version`, `created_at`, `updated_at`; index
    `(org_id, schedule_id, ends_at)` for the period reads.
  - `permissions` gains the three Permissions and `role_permissions` their allocation — reference data, as in `0001`
    and `0022`. The down migration drops all of it.
- **Schedules** (`internal/schedules`, a flat domain package, ADR-0016): validates, writes through its queries,
  records the Audit log (`schedule.created`, `schedule.updated`, `schedule.deleted`, `schedule_override.created`,
  `schedule_override.updated`, `schedule_override.deleted`) and sends the hint `schedule`:
  - `rotation.go` — the Shifts of a Rotation layer over a period: the first Shift starts on `start_date` at the handover
    time with the first person and each lasts a day, a week (handed over on `handover_day`) or `shift_seconds` of
    wall-clock time; times are wall-clock times in the Schedule's time zone, so a handover at 10:00 stays at 10:00
    across a change of the clocks; the limits cut each Shift to their days and time of day: a window whose end is
    earlier than its start runs until the end on the next day, and with days of the week each window belongs to the day
    it starts on, so Friday 19:00–09:00 runs from Friday 19:00 to Saturday 09:00 (C-27.FR-2). A wall-clock time that
    the clocks skip when they move forward is the instant they move, and one that happens twice when they move back is
    its first occurrence (RFC 5545), in every zone. The arithmetic is in whole seconds and calendar days, and every
    loop is bounded by the length of the period.
  - `result.go` — `OnCallAt(schedule, t)` and `Result(schedule, from, to)`: the Override of the moment, the one
    created last where they overlap (P-65), when its person may be Paged; otherwise the highest Rotation layer with a
    Shift whose person may be Paged; else nobody (a gap). "May be Paged" is read once per call from the current state:
    the User is active, not a Viewer and in the Team (`team_users`); a deleted User is `not_in_team`, whatever their
    Role was. Adjacent segments of the same person and source are joined. The Override of each segment comes from a
    sweep over the boundaries with a heap ordered by id, O((S+N) log N) for S segments and N Overrides. The business
    clock (`clock.Business`) is the only time it reads (C-02).
  - `overrides.go` — the precondition, checked after the Permission: the caller is in the Team or is an Admin, else
    `not_in_team`; the person who takes the Shift may be Paged, else `not_pageable`. An Override may not rewrite the
    past (maintainer's decision on PR #489): a `start` more than 5 minutes before the business clock's now is `422`
    `out_of_range` at `/start`; the end is after the start and after now (P-91) and at most 31 days after the start,
    else `422` `out_of_range` at `/end`, while the start may be any time ahead; checked on create and change. Of an
    Override that has begun only the end may change: another `start`, `user_id` or `replaces_user_id` is `422`
    `unsupported` at it. Deleting an Override that has begun ends it at now — its past part stays in the Result —
    and records `schedule_override.deleted` with the change of `/end`; one that has not begun is removed. An Override
    that has ended can be neither changed nor deleted, `409` `override_ended` (settled 3). A span that would overlap
    1000 other Overrides of the Schedule is `422` `out_of_range` at `/end`. A change that changes nothing writes
    nothing and keeps the version.
  - `schedules.go` — the people of a Rotation layer are validated against the stored version (settled 6, C-27.FR-2):
    the people a save adds must exist, not be deleted and be able to be Paged; those it keeps need not, deleted ones
    included. A Schedule has at most 20 Rotation layers (`/rotation_layers`) and a Rotation layer at most 100 people
    (`/rotation_layers/{i}/user_ids`); `start_date` is from 2000-01-01 to 2100-12-31 and `shift_seconds` at most
    2419200, 28 days — each `422` `out_of_range` at its pointer, and CHECKs in the migration for the last two. The
    spec states these bounds in descriptions: a `maximum` or `maxItems` would make the request validation answer `400`
    before the domain's `422`;
  - `schedules.go` — the main Schedule: the first Schedule of a Team is created main (P-90); `main` true on another
    clears it on the previous one in the same transaction, each with its Audit log entry; deleting the main Schedule
    leaves the Team without one (P-66); `Referrers(schedule)` lists what names it — empty until S-154 adds the
    Escalation steps — and `deleteSchedule` answers `schedule_in_use` while it is not; `muster_schedule_info` is
    exported and refreshed as the Routes' info is (`internal/runtime`).
- **Teams** (`internal/teams`): `deleteTeam` refuses a Team with Schedules with `team_in_use` naming them, in the same
  transaction as the read (P-64). The Team read gains nothing, so the Team fixtures of the web tests stay as they are.
- **Metric** (`internal/metrics/catalogue.go`): `muster_schedule_info{schedule,name}`, C-27, moved in reference.md from
  "Metrics added in L2" into the catalogue.
- **Shared tables**: reference.md moves `schedules:read`, `schedules:write` and `schedule-overrides:write` from
  "Permissions added in L2" into the main table and the `Permission` schema; defaults.md marks `schedule.time_zone`
  decided; L2.md §5.3 moves P-64, P-65, P-66, P-77, P-90 and P-91 to "Decided since they were first listed".
- **Settings**: `schedule.time_zone` (P-77), `schedule.gap_view`, `organization.time_zone`, `ical.window` (P-81, the
  longest period of `getScheduleOnCall`, `ICalWindowBack` plus `ICalWindowAhead`).
- **Log events**: none new; the Audit log records every change.
- **Documentation**: the new page `docs/concepts/schedules.md` — "What a Schedule is", "Rotation layers", "Several
  Schedules" and "Overrides" (who may make them, that they send no message) — under Concepts in `mkdocs.yml`.

## Steps

1. Write the migration and `schema.md`. Check: `make test-integration` runs up, down and up on PostgreSQL 14 and 17;
   the Permissions of each Role read from `role_permissions` as in the acceptance.
2. Write the Shifts of a Rotation layer and the Result. Check: `rotation_test.go` covers a day, a week and 12 hours,
   limits with and without a time, a window across midnight with and without days of the week, a start date in the
   future, both changes of the clocks in Europe/Berlin and a zone without them; `result_test.go` covers every row of
   C-27.FR-4 and FR-5 with a manual clock.
3. Write the Schedules and Overrides with their Permissions, precondition, Audit log, metric and hint; add the spec.
   Check: `make generate-check`; `schedules_test.go`, `overrides_test.go`, `schedules_integration_test.go`.
4. Refuse the deletion of a Team with Schedules. Check: `teams_test.go`.
5. Move the shared rows and update the counts of `TestIntegrationRoleMatrix`, confirm the provisional values, write the
   documentation and `test/e2e/schedules_test.go`. Check: `make docs-check`; the Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-115: the Admin's Personal access token A; the Responders petr, sergey, olga and bob with their tokens P, S, O
# and B, and the Viewer vic with V; the Team "payments" (TP) with petr, sergey, olga and vic added by hand, not bob.
API=localhost:8080/api/v1; CLK=localhost:8082/_dev/clock
uid() { curl -s "${A[@]}" "$API/users?q=$1" | jq -r '.items[0].id'; }
PETR=$(uid petr); SERGEY=$(uid sergey); OLGA=$(uid olga); BOB=$(uid bob); VIC=$(uid vic)
NOW=$(curl -s $CLK | jq -r .now)
MON=$(date -u -d "$(date -u -d "$NOW" +%F) +$(( 15 - $(date -u -d "$NOW" +%u) )) days" +%F)   # a Monday 8 to 14 days ahead
AT() { date -u -d "TZ=\"Europe/Berlin\" $1" +%FT%TZ; }                                     # Berlin wall time to UTC
DAY() { date -u -d "$MON +$1 days" +%F; }
LAYERS="[{\"name\":\"Around the clock\",\"user_ids\":[\"$PETR\",\"$SERGEY\",\"$OLGA\"],\"shift_length\":\"week\",
  \"handover_time\":\"10:00\",\"handover_day\":\"monday\",\"start_date\":\"$MON\",\"limits\":null},
 {\"name\":\"Working hours 11-19\",\"user_ids\":[\"$OLGA\",\"$PETR\"],\"shift_length\":\"week\",\"handover_time\":\"11:00\",
  \"handover_day\":\"monday\",\"start_date\":\"$MON\",\"limits\":{\"days\":[\"monday\",\"tuesday\",\"wednesday\",\"thursday\",\"friday\"],
  \"start\":\"11:00\",\"end\":\"19:00\"}}]"
S=$(curl -s "${A[@]}" $API/schedules -d "{\"team_id\":\"$TP\",\"name\":\"Main\",\"time_zone\":\"Europe/Berlin\",\"rotation_layers\":$LAYERS}")
SC=$(jq -r .id <<<"$S"); jq -c '{main, n: [.rotation_layers[].number]}' <<<"$S"            # {"main":true,"n":[1,2]}
WHO() { curl -s "${A[@]}" "$API/schedules/$SC/on-call?from=$1&to=$(date -u -d "$1 +1 minute" +%FT%TZ)" | jq -r '.segments[0].user.login // "gap"'; }

# C-27.AC-1: week 1 — lower petr, upper olga
WHO $(AT "$(DAY 2) 15:00"); WHO $(AT "$(DAY 2) 21:00"); WHO $(AT "$(DAY 5) 15:00")       # olga  petr  petr
# the handover stays at 10:00 Berlin across the change of the clocks in March
Y=$(( $(date -u -d "$NOW" +%Y) + 1 )); LS=$(date -u -d "$Y-03-31 -$(( $(date -u -d "$Y-03-31" +%u) % 7 )) days" +%F)
curl -s "${A[@]}" "$API/schedules/$SC/on-call?from=$(date -u -d "$LS -7 days" +%F)T00:00:00Z&to=$(date -u -d "$LS +2 days" +%F)T00:00:00Z" |
  jq -r '[.rotation_layers[0].shifts[].start] | map(select(test("T0[89]:00"))) | .[]'
# <the Monday before>T09:00:00Z
# <the Monday after>T08:00:00Z

# C-27.FR-2: a Rotation layer of Friday 19:00–09:00 crosses midnight; its window belongs to Friday
SN=$(curl -s "${A[@]}" $API/schedules -d "{\"team_id\":\"$TP\",\"name\":\"Nights\",\"time_zone\":\"Europe/Berlin\",\"rotation_layers\":[
  {\"name\":\"Friday night\",\"user_ids\":[\"$OLGA\"],\"shift_length\":\"week\",\"handover_time\":\"10:00\",\"handover_day\":\"monday\",
  \"start_date\":\"$MON\",\"limits\":{\"days\":[\"friday\"],\"start\":\"19:00\",\"end\":\"09:00\"}}]}" | jq -r .id)
WHON() { curl -s "${A[@]}" "$API/schedules/$SN/on-call?from=$1&to=$(date -u -d "$1 +1 minute" +%FT%TZ)" | jq -r '.segments[0].user.login // "gap"'; }
WHON $(AT "$(DAY 4) 23:00"); WHON $(AT "$(DAY 5) 08:00"); WHON $(AT "$(DAY 5) 10:00"); WHON $(AT "$(DAY 3) 23:00")   # olga olga gap gap

# C-27.FR-2, C-27.AC-3: a Viewer in a Rotation layer
curl -s "${A[@]}" $API/schedules -d "{\"team_id\":\"$TP\",\"name\":\"Bad\",\"rotation_layers\":[{\"name\":\"x\",\"user_ids\":[\"$VIC\"],
  \"shift_length\":\"day\",\"handover_time\":\"09:00\",\"start_date\":\"$MON\",\"limits\":null}]}" | jq -c '{status, e: .errors[0] | {pointer, code, reason}}'
# {"status":422,"e":{"pointer":"/rotation_layers/0/user_ids/0","code":"not_pageable","reason":"viewer"}}

# C-27.AC-2: an Override on Wednesday of week 3 (both Rotation layers olga) makes petr On-call 11:00–21:00
OV() { curl -s "$@" $API/schedules/$SC/overrides -d "$(cat)"; }
FAKES() { for u in 127.0.0.1:18065/_fake/posts 127.0.0.1:18065/_fake/dms 127.0.0.1:18081/_fake/notifications \
  127.0.0.1:18097/_fake/messages 127.0.0.1:18097/_fake/dms 127.0.0.1:18094/_fake/messages; do curl -s $u | jq length; done | paste -sd' '; }
BEFORE=$(FAKES)
echo "{\"user_id\":\"$PETR\",\"start\":\"$(AT "$(DAY 16) 11:00")\",\"end\":\"$(AT "$(DAY 16) 21:00")\"}" | OV "${P[@]}" | jq -r .user.login   # petr
WHO $(AT "$(DAY 16) 10:00"); WHO $(AT "$(DAY 16) 15:00"); WHO $(AT "$(DAY 16) 20:00"); WHO $(AT "$(DAY 16) 22:00")   # olga petr petr olga
curl -s "${A[@]}" "$API/audit-log?action=schedule_override.created" | jq -r '.items[0].actor.name'                # petr
[ "$(FAKES)" = "$BEFORE" ] && echo "no messenger request"                                                       # no messenger request

# C-27.AC-3: the precondition
echo "{\"user_id\":\"$OLGA\",\"start\":\"$(AT "$(DAY 17) 11:00")\",\"end\":\"$(AT "$(DAY 17) 12:00")\"}" | OV "${B[@]}" | jq -c '{status, code}'   # {"status":409,"code":"not_in_team"}
echo "{\"user_id\":\"$OLGA\",\"start\":\"$(AT "$(DAY 17) 11:00")\",\"end\":\"$(AT "$(DAY 17) 12:00")\"}" | OV "${V[@]}" | jq .status                  # 403
echo "{\"user_id\":\"$BOB\",\"start\":\"$(AT "$(DAY 17) 11:00")\",\"end\":\"$(AT "$(DAY 17) 12:00")\"}" | OV "${O[@]}" | jq -c '[.status, .errors[0].reason]'   # [422,"not_in_team"]
echo "{\"user_id\":\"$OLGA\",\"replaces_user_id\":\"$BOB\",\"start\":\"$(AT "$(DAY 17) 11:00")\",\"end\":\"$(AT "$(DAY 17) 12:00")\"}" | OV "${O[@]}" | jq -r .replaces_user.login   # bob
echo "{\"user_id\":\"$OLGA\",\"start\":\"$(AT "$(DAY 17) 13:00")\",\"end\":\"$(AT "$(DAY 17) 14:00")\"}" | OV "${A[@]}" | jq -r .user.login     # olga
echo "{\"user_id\":\"$OLGA\",\"start\":\"2020-01-01T00:00:00Z\",\"end\":\"2020-01-01T01:00:00Z\"}" | OV "${O[@]}" | jq -c '[.status, .errors[0].pointer]'   # [422,"/end"]

# C-27.AC-4: petr, upper Rotation layer of week 2, disabled — the lower one's sergey takes his Shifts
curl -s "${A[@]}" -X POST $API/users/$PETR/disable > /dev/null
WHO $(AT "$(DAY 9) 15:00")                                                                                       # sergey
curl -s "${A[@]}" "$API/schedules/$SC/on-call?from=$(DAY 0)T00:00:00Z&to=$(DAY 21)T00:00:00Z" |
  jq -c '[.warnings[] | select(.kind == "disabled") | {kind, u: .user.login, l: .rotation_layer.name}] | unique'
# [{"kind":"disabled","u":"petr","l":"Around the clock"},{"kind":"disabled","u":"petr","l":"Working hours 11-19"}]

# C-27.AC-10: a Responder may not change Schedules
curl -s -o /dev/null -w '%{http_code}\n' "${O[@]}" -X DELETE $API/schedules/$SC                                   # 403

# C-25.AC-9, C-27.AC-8: the Team with a Schedule; the main Schedule deleted
curl -s "${A[@]}" -X DELETE $API/teams/$TP | jq -c '{status, code, n: [.in_use_by[].name] | sort}'   # {"status":409,"code":"team_in_use","n":["Main","Nights"]}
curl -s -o /dev/null -w '%{http_code}\n' "${A[@]}" -X DELETE $API/schedules/$SC                                   # 204
curl -s "${A[@]}" "$API/schedules?team=$TP" | jq '[.items[] | select(.main)] | length'                           # 0
curl -s localhost:8082/metrics | grep -c '^muster_schedule_info{'                                                 # 1   ("Nights" is left)
```

`test/e2e/schedules_test.go` repeats these checks and adds a second Schedule made main, two overlapping Overrides
(C-27.AC-10), a person turned into a Viewer and one taken out of the Team (C-27.AC-4), a stale `If-Match` on an
Override and an Admin outside the Team who makes one.

## Open questions

None open. Settled:

1. **Settled: a Schedule's time zone is stored when it is created** — the Organization's zone of that moment when none
   is given — and does not follow later changes of the Organization's (P-77).
2. **Settled: a Rotation layer's time window may cross midnight** (maintainer's decision): an end earlier than the start
   runs until the next day, and with days of the week the window belongs to the day it starts on, so Friday 19:00–09:00
   runs until Saturday 09:00 (C-27.FR-2, amended). P-63 is about Working hours and stays as it is.
3. **Settled: an Override that has ended may be neither changed nor deleted** (`409` `override_ended`), and creating
   and changing check the new end (P-91), so the past Result stays as it was.
4. **Settled by the maintainer (review of PR #399): a Schedule stays in its Team**: `team_id` is set on creation only,
   and a different one on update is refused with `422` `unsupported` at `/team_id`.
5. **Settled: the main Schedule cannot be unmarked**: `main` false on it is refused with `422` `unsupported` at `/main`;
   an Admin marks another Schedule main, and a Team is without one only after its main Schedule is deleted (P-66).
6. **Settled: a save validates only the people it adds to a Rotation layer**, compared with the stored version, and
   keeps the others with their warnings (C-27.FR-2: "the warnings of FR-5 are for people who became one of these
   later").

## Notes

- Suggested commit: `feat(schedules): add schedules with rotation layers, overrides and the on-call`.
- Migration numbers follow the story map: 0029 to 0035 for S-131, S-133, S-164, S-134, S-139, S-140 and S-165. A story
  merged out of that order renumbers its migration to the next free number in its own pull request, with its tests and
  `schema.md`, as S-092 did. S-131 is merged first of them and takes `0025_schedules`, the number after
  `0024_team_label_and_matchers` of S-118 on `master` (journal D585).
- Corrected with the implementation: `files_touched` names the migration `0025_schedules`, and adds the files that
  carry what the Contracts ask for — `sqlc.yaml` (the new package's generated queries), `internal/publicid/publicid_test.go`
  (the new prefixes), `internal/api/server_test.go` (the Permission enum of 36 values), `internal/runtime/runtime_test.go`
  (the fake database's Schedules store), and `internal/metrics/metrics.go`, `metrics_test.go` and
  `internal/tools/refgen/main.go` (the entity label `schedule` joins the label policy of ADR-0014 with the first metric
  that carries it). The refusal of a period of `getScheduleOnCall` is at `/query/to`, the pointer of a query parameter
  (`api/README.md`), not `/to`. `C-25-teams.md` and `C-27-schedules.md` drop the marks _provisional_ of the values this
  story confirms, and `docs/install/versions.md` lists migration 0025 among the changes of 0.2.0.
- The bounds of the Contracts — `from` and `to` within 10 years, `start_date` from 2000 to 2100, `shift_seconds` up
  to 28 days, the caps of 20 Rotation layers, 100 people and 1000 overlapping Overrides, and the rules of Overrides
  about the past — were added in review and confirmed by the maintainer, so that every computation of Shifts ends and
  no read of a Schedule grows without bound. Overrides that overlap are ordered by id, which every replica takes from
  one sequence, not by `created_at` (P-65). `rotation_oracle_test.go` checks the Shifts against plain wall-clock
  arithmetic and the Result against its definition at random, with a fixed seed.
- Confirmed by the maintainer with this story: P-64, deleting a Team with Schedules is refused (the rest of P-64 was
  implemented by S-115 and S-118); P-65, the Override created last wins; P-66, deleting the main Schedule leaves the
  Team without one; P-77, a Schedule's time zone defaults to the Organization's, stored at creation; P-90, a Team's
  first Schedule is its main one; P-91, an Override may not end in the past.
- The live hint `schedule` is not added to `resyncTypes` in `internal/live/hub.go`; that is left for the story of the
  Schedule pages (S-135), which first holds Schedules in a client.
- "May be Paged" is the current state of the person, not their state at the time of a past segment: the Result of a
  week after someone was disabled shows the Rotation layer below for the whole week, which is what the warnings say.
- The only check that looks at a Team (C-25.FR-1) is the precondition of Overrides; every other check is the Role's.
- A Shift length in hours is `shift_seconds` in the API, a multiple of 3600, as every duration of the API is in
  seconds (`api/README.md`, maintainer's decision Q4 of the plan review); C-27.FR-2 speaks of whole hours, which the
  multiple keeps, and the Schedule page (S-135) shows and edits it in hours.
- `getScheduleOnCall` gives the FE the Shifts of each Rotation layer and the Overrides as well as the Result, which is
  what the two-week view draws (S-135).

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-27.FR-1 | partial | the API; the pages are S-135 and S-136 |
| C-27.FR-2 | partial | the API; the editor is S-135 |
| C-27.FR-3 | partial | the API; the two modes of the form are S-135 |
| C-27.FR-4 | partial | the API; the view is S-135 |
| C-27.FR-5 | partial | the skipped people and their warnings; the page is S-135 |
| C-27.FR-6 | partial | the API; the form is S-135 |
| C-27.FR-11 | full | |
| C-27.FR-12 | partial | P-66 and the code; steps that name a Schedule raise it from S-154 |
| C-27.FR-13 | partial | the API and `muster_schedule_info`; the pages are S-135 |
| C-27.FR-14 | partial | the first sections; S-133, S-164, S-134 and S-137 complete the page |
| C-27.AC-1 | full | |
| C-27.AC-2 | partial | the Override through the API; "I give my Shift to…" is S-135 |
| C-27.AC-3 | full | |
| C-27.AC-4 | full | |
| C-27.AC-8 | partial | another main Schedule; the gap alert that moves is S-133, `schedule_in_use` S-154 |
| C-27.AC-10 | full | |
| C-25.FR-1 | partial | the Schedules of a Team |
| C-25.FR-2 | partial | Schedules belong to a Team; the filter of their list |
| C-25.FR-10 | partial | the refusal `team_in_use` for Schedules; chains are S-154 |
| C-25.AC-9 | partial | the Team with a Schedule; S-115 and S-118 cover the rest |
