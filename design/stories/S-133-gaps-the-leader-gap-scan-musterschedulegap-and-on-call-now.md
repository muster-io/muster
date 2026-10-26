---
id: S-133
title: Gaps of Schedules, the Leader's gap scan with MusterScheduleGap, its chart rule and runbook, and On call now (BE)
capability: C-27
kind: be
layer: L2
depends_on: [S-118, S-131]
covers: [C-27.FR-7, C-27.FR-9, C-27.FR-14, C-27.AC-5, C-27.AC-8, C-25.FR-5]
files_touched:
  - api/openapi.yaml
  - internal/db/migrations/0030_schedule_gap_alert.up.sql
  - internal/db/migrations/0030_schedule_gap_alert.down.sql
  - design/db/schema.md
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - internal/schedules/schedules.go
  - internal/schedules/gaps.go
  - internal/schedules/gaps_test.go
  - internal/schedules/scan.go
  - internal/schedules/scan_integration_test.go
  - internal/schedules/oncallnow.go
  - internal/schedules/oncallnow_test.go
  - internal/schedules/query.sql
  - internal/api/schedules.go
  - internal/api/schedules_test.go
  - internal/internalalerts/registry.go
  - internal/internalalerts/raise.go
  - internal/internalalerts/internalalerts_test.go
  - internal/leader/tasks.go
  - internal/leader/leader_test.go
  - internal/runtime/runtime.go
  - internal/metrics/catalogue.go
  - internal/logging/events.go
  - deploy/helm/muster/files/rules.yaml
  - deploy/helm/muster/values.yaml
  - deploy/helm/muster/values.schema.json
  - deploy/helm/muster/tests/rules/MusterScheduleGap.yaml
  - docs/operations/runbooks/MusterScheduleGap.md
  - docs/operations/runbooks/index.md
  - docs/operations/monitoring.md
  - docs/concepts/schedules.md
  - mkdocs.yml
  - design/prd/l1/reference.md
  - test/e2e/schedule_gaps_test.go
acceptance:
  - "[C-27.FR-7] `Schedule` carries `gap_alert` — `by_default`, `on` or `off`, `by_default` on a new Schedule and after the migration — and the derived read-only `gap_alert_active`, true for `on`, and for `by_default` while the Schedule is its Team's main one; `updateSchedule` changes it, recorded in the diff of `schedule.updated` (`schedules_test.go`)."
  - "[C-27.FR-7] `getScheduleOnCall` lists a warning `gap` with its `start` and `end` for each gap of the period, and `Schedule.next_gap` is the first gap within `schedule.gap_view` from the business clock's now, or null (`gaps_test.go`)."
  - "[C-27.FR-7, C-27.AC-5, C-25.FR-5] With a gap 5 days ahead in a Team's main Schedule, the next run of the Leader task `schedule_gap_scan` raises `MusterScheduleGap` with the labels `schedule`, `schedule_name`, `team_id`, `team_name` and, under the Organization's Team label, the Team's slug outside the fingerprint, and the annotation of when the nearest gap starts; `muster_schedule_gap{schedule}` is 1; the Alert belongs to the Team by the Team label and its Alert Group records no `unknown_team_value`; a run of the task twice in a row raises it once (`scan_integration_test.go`)."
  - "[C-27.FR-7, C-27.AC-5] Once the gap has started the alert stays raised; an Override that closes the gap resolves it at the next run, and so does the end of the gap; a gap 8 days ahead raises nothing yet; a second Schedule of the Team with a gap raises nothing and exports `muster_schedule_gap` 0 until an Admin sets its `gap_alert` to `on` (`scan_integration_test.go`, `schedule_gaps_test.go`)."
  - "[C-27.AC-8] With `gap_alert` `by_default` on two Schedules of a Team, making the second one main moves the gap alert to it at the next run and resolves the first one's; with `on` on the first one it stays raised there (`scan_integration_test.go`)."
  - "[C-27.FR-7] Renaming a Schedule or its Team updates the name labels of the open `MusterScheduleGap`, a changed slug or Team label its Team label, and deleting a Schedule resolves its alert, without a second Alert (`scan_integration_test.go`)."
  - "[C-27.FR-9] `getOnCallNow` lists for every Team — or with `teams=mine` the Teams of the calling User — the main Schedule with the On-call now and until when, the next segment (its person, or a gap) and `no_schedule` true for a Team without a main Schedule, sorted by the Team's name (`oncallnow_test.go`)."
  - "[C-27.FR-7] The chart renders the rule `MusterScheduleGap` with its expression, severity, `description` and `runbook_url`, its threshold and `for` as chart values; its rule unit test fires on `muster_schedule_gap` 1 and not on 0; reference.md moves `MusterScheduleGap` and `muster_schedule_gap` into the main tables; the runbook page says how to fill a gap; `make rules-test helm-check docs-check` pass."
  - "[C-27.FR-14] Concepts → Schedules has \"Gaps and the Internal alert\": the people whose Shifts are skipped, `schedule.gap_alert`, and that a Schedule which covers only part of the day, such as a second line in working hours, keeps it off; `make docs-check` passes."
verify: "make ci test-integration e2e-go rules-test helm-check docs-check"
operator_attention: false
issue: null
---

# S-133. Gaps of Schedules, the Leader's gap scan with MusterScheduleGap, its chart rule and runbook, and On call now (BE)

## Scope

**IN**

- Gaps in the Result of a Schedule as warnings, and the nearest gap of the next `schedule.gap_view` (C-27.FR-7).
- The setting `schedule.gap_alert` of a Schedule, and the Leader task that looks for gaps every
  `schedule.gap_scan_interval` and raises and resolves `MusterScheduleGap` within `schedule.gap_alert_horizon`, with the
  Team's slug under the Team label so that the alert belongs to the Team (C-27.FR-7, C-25.FR-5).
- The Leader gauge `muster_schedule_gap`, the chart rule of the same name with its rule unit test, and the runbook page.
- "On call now" for the Home and Team pages: the On-call of each Team's main Schedule, until when, and what follows
  (C-27.FR-9).

**OUT**

- The marks of an unreachable On-call, in the warnings and in "On call now": S-164.
- The pages: S-135 (the Schedule page with its gaps and warnings) and S-136 ("On call now").
- The provisional thresholds of the chart rules of L2 (P-88) and the scan interval (P-80), which the test environment
  confirms; both stay provisional.

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated):
  - `ScheduleInput` gains `gap_alert` (`ScheduleGapAlert`: `by_default`, `on`, `off`; optional, omitted keeps, default
    `by_default`); `Schedule` gains `gap_alert`, the read-only `gap_alert_active` and `next_gap` ({`start`, `end`} or
    null).
  - `ScheduleWarning.kind` gains `gap`, with `start` and `end`.
  - `getOnCallNow` (`GET /on-call-now`, `schedules:read`, session or token; `teams` — `all` by default, or `mine`,
    `422` for a Service account token): `OnCallNow` {`items` (`TeamOnCall`[])}; `TeamOnCall` {`team` (`TeamRef`),
    `schedule` (`ScheduleRef` or null), `no_schedule` (boolean), `user` (`UserRef` or null — null in a gap),
    `rotation_layer` (`EntityRef` or null), `until` (the end of the current segment, or null), `next` ({`user` or
    null for a gap, `start`, `end`} or null)}; S-164 adds `unreachable`.
- **Schema** (migration `0030_schedule_gap_alert`, expand only; `schema.md` §4.16):
  `schedules.gap_alert text NOT NULL DEFAULT 'by_default' CHECK (gap_alert IN ('by_default', 'on', 'off'))`. The down
  migration drops it.
- **Gaps** (`internal/schedules/gaps.go`): the gaps of a period from the Result (S-131); `next_gap`; the warnings `gap`.
- **Scan** (`internal/schedules/scan.go`), a Leader task `schedule_gap_scan` at the end of the closed list of
  `internal/leader/tasks.go`, every `schedule.gap_scan_interval`, woken when the development clock moves, run per
  Organization:
  - for each Schedule whose `gap_alert` is active, the Result from the business clock's now to
    `schedule.gap_alert_horizon` ahead; a gap that has started, or one that starts within the horizon, raises
    `MusterScheduleGap` for the Schedule, with the annotation of the nearest gap's start; a Schedule without such a gap,
    or whose alert is no longer active, or that was deleted, has its open alert resolved;
  - the raise is skipped when the alert is firing with the same labels and annotation, and the Internal alert is
    deduplicated by its fingerprint anyway, so a second run raises nothing new (ADR-0007);
  - `muster_schedule_gap{schedule}` is set to 1 for each Schedule with such a gap, and to 0 for every other Schedule,
    those whose alert is not active included (Open question 1); the series of a deleted Schedule is removed;
  - the log events `schedule_gap_detected` (warn, with `schedule`, `team` and the gap's start) when a Schedule's alert
    is raised and `schedule_gap_cleared` (info) when it is resolved, C-27, in `internal/logging/events.go`.
  The Leader adds only this task and the gauge (NFR-2, NFR-4).
- **Internal alert** (`internal/internalalerts`): `MusterScheduleGap` joins the registry — severity `warning`, entity
  `schedule` with `schedule_name`, the extra labels `team_id` and `team_name`, and a new `TeamLabel` flag: the label
  named by `organization.team_label`, with the Team's slug, is added outside the fingerprint, like a name label, so that
  a renamed Schedule or Team, a changed slug or a changed Team label updates the open alert instead of starting a new
  one; `ReservedLabels()` reads the registry, so the Team label refusal of S-118 keeps covering it. Its summary is "No
  one is On-call in Schedule {name} of {team}", and its description names the nearest gap's start and the runbook.
- **Chart** (`deploy/helm/muster/files/rules.yaml`, `values.yaml`, `values.schema.json`): the rule `MusterScheduleGap`
  of reference.md — `max by (schedule) (muster_schedule_gap{job="muster"}) == 1`, `for` `0m`, severity `warning`
  (P-88) — with its threshold and `for` as chart values and its `runbook_url`; `tests/rules/MusterScheduleGap.yaml`.
- **Metric** (`internal/metrics/catalogue.go`): `muster_schedule_gap{schedule}` (gauge, Leader), C-27.
- **On call now** (`internal/schedules/oncallnow.go`): per Team, sorted by name, the main Schedule's current segment of
  the Result and the next one; Teams the caller is in for `mine`.
- **Shared tables**: reference.md moves `MusterScheduleGap` from "Internal alerts added in L2" and "Chart rules added in
  L2" into the main tables, and `muster_schedule_gap` from "Metrics added in L2" into the catalogue.
- **Settings**: `schedule.gap_alert`, `schedule.gap_view`, `schedule.gap_alert_horizon`, `schedule.gap_scan_interval`
  (P-80), `organization.team_label`; the chart values of the rule (P-88).
- **Documentation**: `docs/concepts/schedules.md` — "Gaps and the Internal alert"; the runbook page
  `docs/operations/runbooks/MusterScheduleGap.md` — what the alert means, how to find the gap on the Schedule page and
  fill it with an Override or a change of the Rotation layers, and when to turn the alert off — in the runbook index and
  the navigation; `docs/operations/monitoring.md` lists it among the Internal alerts with a chart rule.

## Steps

1. Write the migration and `schema.md`. Check: `make test-integration` runs up, down and up; existing Schedules read
   `by_default`.
2. Write the gaps, `next_gap`, the warnings and "On call now"; add the spec. Check: `make generate-check`;
   `gaps_test.go`, `oncallnow_test.go`.
3. Register the Internal alert with the Team label, write the scan, the Leader task, the gauge and the log events.
   Check: `internalalerts_test.go`, `leader_test.go`, `scan_integration_test.go`.
4. Add the chart rule, its test, the runbook and the documentation; move the shared rows. Check: `make rules-test
   helm-check docs-check`.
5. Write `test/e2e/schedule_gaps_test.go`. Check: the Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-131: A; olga in the Team "payments" (TP); CLK. The Integration "Muster" is built in; the Default route takes
# its Alerts.
API=localhost:8080/api/v1; CLK=localhost:8082/_dev/clock; OLGA=$(curl -s "${A[@]}" "$API/users?q=olga" | jq -r '.items[0].id')
NOW=$(curl -s $CLK | jq -r .now); TODAY=$(date -u -d "$NOW" +%F)
GAPDAY=$(date -u -d "$TODAY +5 days" +%A | tr A-Z a-z)                       # the weekday 5 days ahead is left out
DAYS=$(jq -nc --arg g "$GAPDAY" '["monday","tuesday","wednesday","thursday","friday","saturday","sunday"] - [$g]')
S=$(curl -s "${A[@]}" $API/schedules -d "{\"team_id\":\"$TP\",\"name\":\"Main\",\"time_zone\":\"UTC\",\"rotation_layers\":[
  {\"name\":\"Days\",\"user_ids\":[\"$OLGA\"],\"shift_length\":\"day\",\"handover_time\":\"00:00\",\"start_date\":\"$TODAY\",\"limits\":{\"days\":$DAYS}}]}")
SC=$(jq -r .id <<<"$S"); jq -c '{gap_alert, gap_alert_active, g: .next_gap.start}' <<<"$(curl -s "${A[@]}" $API/schedules/$SC)"
# {"gap_alert":"by_default","gap_alert_active":true,"g":"<today +5 days>T00:00:00Z"}
TICK() { curl -s -X POST $CLK -d '{"advance_seconds": 1}' > /dev/null; sleep 2; }  # wakes the Leader's scan
TO() { local n; n=$(curl -s $CLK | jq -r .now)                                        # moves the business clock to $1 exactly
  curl -s -X POST $CLK -d "{\"advance_seconds\": $(( $(date -u -d "$1" +%s) - $(date -u -d "$n" +%s) ))}" > /dev/null; sleep 2; }
GAPALERT() { curl -s "${A[@]}" "$API/alert-groups?status=firing&label=alertname%3D%22MusterScheduleGap%22" | jq '.items | length'; }

# C-27.AC-5: raised 5 days ahead, with the Team label
TICK; GAPALERT                                                                                      # 1
G=$(curl -s "${A[@]}" "$API/alert-groups?label=alertname%3D%22MusterScheduleGap%22" | jq -r '.items[0].id')
curl -s "${A[@]}" "$API/alert-groups/$G/alerts" | jq -c '.items[0].labels | {schedule, schedule_name, team_id, team_name, team}'
# {"schedule":"SC…","schedule_name":"Main","team_id":"TM…","team_name":"payments","team":"payments"}
curl -s "${A[@]}" "$API/alert-groups/$G" | jq -c '[.teams[].team.slug]'                            # ["payments"]
curl -s "${A[@]}" "$API/alert-groups/$G/timeline?limit=20" | jq '[.items[] | select(.event == "unknown_team_value")] | length'   # 0
curl -s localhost:8082/metrics | grep "^muster_schedule_gap{schedule=\"$SC\"}"                      # … 1
TICK; curl -s "${A[@]}" "$API/alert-groups/$G/alerts" | jq '.items | length'                         # 1   (a second scan raises nothing new)

# an Override closes the gap: resolved at the next scan; the next gap is 12 days ahead, beyond the horizon
GS=$(date -u -d "$TODAY +5 days" +%FT00:00:00Z); GE=$(date -u -d "$TODAY +6 days" +%FT00:00:00Z)
curl -s "${A[@]}" $API/schedules/$SC/overrides -d "{\"user_id\":\"$OLGA\",\"start\":\"$GS\",\"end\":\"$GE\"}" > /dev/null
TICK; GAPALERT                                                                                      # 0
curl -s localhost:8082/metrics | grep "^muster_schedule_gap{schedule=\"$SC\"}"                      # … 0

# the weekday recurs: its next gap is 12 days ahead and the one after 19 days ahead, which an Override closes now
curl -s "${A[@]}" $API/schedules/$SC/overrides -d "{\"user_id\":\"$OLGA\",\"start\":\"$(date -u -d "$TODAY +19 days" +%FT00:00:00Z)\",
  \"end\":\"$(date -u -d "$TODAY +20 days" +%FT00:00:00Z)\"}" > /dev/null
# a gap 8 days ahead raises nothing; 6 days ahead raises; a started gap stays raised; its end resolves
G2=$(date -u -d "$TODAY +12 days" +%FT00:00:00Z)                                    # the start of the next gap
TO "$(date -u -d "$G2 -8 days +1 hour" +%FT%TZ)"; GAPALERT                           # 0   (it starts in 7 days 23 h)
TO "$(date -u -d "$G2 -6 days" +%FT%TZ)"; GAPALERT                                   # 1   (it starts in 6 days)
TO "$(date -u -d "$G2 +2 hours" +%FT%TZ)"; GAPALERT                                  # 1   (inside the gap)
TO "$(date -u -d "$G2 +1 day +1 hour" +%FT%TZ)"; GAPALERT                            # 0   (past it; the next one, at TODAY +26 days, is 13 days out)

# C-27.FR-9: On call now
curl -s "${A[@]}" "$API/on-call-now" | jq -c '[.items[] | {t: .team.slug, s: .schedule.name, u: .user.login, no_schedule}]'
# [{"t":"payments","s":"Main","u":"olga","no_schedule":false}, … Teams without a Schedule with "no_schedule":true]

# the chart rule and its test
make rules-test 2>&1 | tail -1                                                                      # SUCCESS
```

`test/e2e/schedule_gaps_test.go` replays the sequence with exact times, and adds the second Schedule with `gap_alert`
`on` (C-27.AC-5), the move of the main Schedule (C-27.AC-8), a renamed Team and a deleted Schedule. The development
clock moves past `auth.session_idle_timeout`, which is why the checks use the Personal access token A.

## Open questions

1. **`muster_schedule_gap` of a Schedule whose gap alert is off.** C-27.FR-7 says the gauge is 1 "while there is a gap
   now or within the horizon", for the chart rule of the same name, which is the backup of the Internal alert. Options:
   (a) the gauge is 1 only for Schedules whose `gap_alert` is active, so that the chart rule follows the same setting
   and a second line in working hours does not page through the chart; (b) the gauge is 1 for every Schedule with a
   gap, and the chart rule fires for Schedules whose Internal alert is off. Recommended: (a); the contract follows it.

## Notes

- Suggested commit: `feat(schedules): report gaps of schedules with an internal alert and show who is on call now`.
- Migration `0030` follows the story map; a story merged out of that order renumbers its migration to the next free
  number in its own pull request (S-131's Notes).
- The Team label on the alert is what makes `MusterScheduleGap` reach the Team's own Destinations through a Route that
  delivers to the Teams from the label (C-25.FR-5, C-26.FR-2); the slug stays outside the fingerprint, so a renamed
  Team never starts a second alert.
- The scan computes the Result for one horizon per active Schedule; it reads, and writes only Internal alerts.
- "On call now" with `teams=mine` uses the caller's Teams, as "My Teams" does (S-115).

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-27.FR-7 | partial | the API, the scan, the alert, the gauge, the chart rule and the runbook; the page is S-135 |
| C-27.FR-9 | partial | the API without the unreachable marks (S-164); the pages are S-136 |
| C-27.FR-14 | partial | "Gaps and the Internal alert" |
| C-27.AC-5 | full | |
| C-27.AC-8 | partial | the gap alert that moves; `schedule_in_use` is S-154 |
| C-25.FR-5 | partial | the Team's slug under the Team label on the Internal alert about a Team's object |
