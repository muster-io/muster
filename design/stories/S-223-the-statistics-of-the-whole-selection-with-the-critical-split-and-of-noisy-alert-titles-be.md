---
id: S-223
title: The statistics of the whole selection with the critical split, and the statistics of noisy alert titles (BE)
capability: C-40
kind: be
layer: L2
depends_on: [S-029, S-174, S-206, S-111]
covers: [C-40.FR-2, C-40.FR-3, C-40.FR-4, C-40.FR-5, C-40.FR-7, C-40.FR-10, C-40.AC-1, C-40.AC-2, C-40.AC-5, C-40.AC-6]
files_touched:
  - api/openapi.yaml
  - api/README.md
  - internal/groups/query.sql
  - internal/groups/statistics.go
  - internal/groups/statistics_test.go
  - internal/groups/statistics_integration_test.go
  - internal/groups/titlestatistics.go
  - internal/groups/titlestatistics_test.go
  - internal/api/alertgroups.go
  - internal/api/alertgroups_test.go
  - internal/api/server_test.go
  - web/src/routes/statistics.tsx
  - web/src/components/statistics-table.tsx
  - test/e2e/statistics_test.go
  - test/e2e/dashboard_statistics_test.go
acceptance:
  - "[C-40.FR-2, C-40.FR-4, C-40.AC-1, C-40.AC-2] `getAlertGroupStatistics?group_by=all` returns one item for the whole selection — `subject` null — with the number of Alert Groups, the median and the 95th percentile of time to acknowledge and of time to resolve, and `per_day`: of 5 Alert Groups started in the last 7 days, 2 critical, 3 acknowledged after 2, 4 and 6 minutes and 4 resolved after 10, 20, 40 and 60 minutes, the item reads `alert_group_count` 5, a median time to acknowledge of 240 seconds and a median time to resolve of 1800 seconds; the count equals the sum of the counts of the items of `group_by=route`, and with one Route chosen its totals, medians and 95th percentiles equal those of that Route's item (`statistics_integration_test.go`, `test/e2e/dashboard_statistics_test.go`)."
  - "[C-40.FR-5, C-40.AC-1] Each entry of `per_day` of every item carries `critical_count`, the Alert Groups of that day whose Severity level is critical (C-26.FR-8), beside `alert_group_count`, so that the below-critical ones are the difference; a period of one day or less, `to` − `from` ≤ 24 hours, answers `per_hour` instead, 24 entries or fewer, split in the person's time zone, and `per_day` stays for longer periods: of the 5 Alert Groups, the days show 2 critical and 3 below critical on their days (`statistics_integration_test.go`, `statistics_test.go`)."
  - "[C-40.FR-2, C-40.FR-3, C-40.AC-6] `group_by=all` takes `from`, `to`, `time_zone` and every filter of the operation — `route`, `integration`, `severity`, `urgent`, `team` with `mine`, `reopened`, `label` and `first_acknowledged_by` — combined with AND and matched as the other `group_by` values match them; a Viewer's and a Responder's token read it as an Admin's does, and one without `alert-groups:read` gets `403` (`statistics_integration_test.go`, `alertgroups_test.go`)."
  - "[C-40.FR-7, C-40.AC-5] `getAlertTitleStatistics` (`/alert-title-statistics`) lists, for the Alert Groups that started in [`from`, `to`) and match the filters of `getAlertGroupStatistics`, the titles (C-09.FR-23) with more than one Alert Group, up to `dashboard.noisy_alerts` (10), most Alert Groups first, ties by the sum of their `reopen_count`, then by title: of three `KubePodCrashLooping` — one reopened twice, two resolved without an acknowledgement — two `NodeDown` with one Reopen between them, two `DiskFull` with none and one `CertExpiring`, it answers `KubePodCrashLooping` with 3 Alert Groups, 2 Reopens, 2 never acknowledged, then `NodeDown`, then `DiskFull`, and no `CertExpiring` (`titlestatistics_test.go`, `statistics_integration_test.go`)."
  - "[C-40.FR-7] Each title item has `alert_group_count`, `reopen_count`, `never_acknowledged_count` — the Alert Groups resolved with no `first_acknowledged_at` —, `time_to_resolve` (a `DurationStats` of the resolved ones, with null times and count 0 for none) and `routes`, the Routes the Alert Groups came through, by name, each an `EntityRef`; it reads summary rows only and never the Alerts inside Alert Groups, so a title is the Alert Group's `title` as stored and two Alert Groups of one title on two Routes count together (`titlestatistics_test.go`, `statistics_integration_test.go`)."
  - "[C-40.FR-10] Both reads start from the Alert Groups that started in the period through `alert_groups_started_idx`, filter by `org_id` in every query (lint 1) and write nothing; an Alert Group attached to a main one is left out as `getAlertGroupStatistics` leaves it out (C-37, S-206); the plans are checked in S-224 on the data of P-96 (`statistics_integration_test.go`, `make lint-arch`)."
  - "[C-40.FR-3] `getAlertTitleStatistics` has `x-permission: alert-groups:read` and `x-listener: app`; without authentication it answers `401`, with an unknown `team` or `route` `422` `unknown_id`, as `getAlertGroupStatistics` does; the statistics page still builds against the regenerated client and shows the views By route, By integration, By team and By people as before (`alertgroups_test.go`, `make ci`)."
verify: "make ci test-integration e2e-go"
operator_attention: false
issue: null
---

# S-223. The statistics of the whole selection with the critical split, and the statistics of noisy alert titles (BE)

## Scope

**IN**

- `group_by=all` of `getAlertGroupStatistics` with the critical split of each day and the hours of a period of a day or
  less (C-40.FR-2, FR-4, FR-5).
- The new read `getAlertTitleStatistics`, the noisy alerts of C-40.FR-7, with the setting `dashboard.noisy_alerts`.

**OUT**

- The Pages and their people, `page-statistics`: S-224; the recording of Pages outside Working hours: S-222.
- The counts of open Alert Groups, which the dashboard takes from `getAlertGroupCounts` twice (C-40 API surface): no
  change of that operation.
- The measurements of the plans on the data of P-96 and any index they call for: S-224.
- The page: S-225, S-226.

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated): `getAlertGroupStatistics` gains `group_by=all`, whose
  one item has `subject` null — `AlertGroupStatisticsItem.subject` becomes nullable and its description says so —
  `StatisticsDay` gains `critical_count`, and `AlertGroupStatisticsItem` gains `per_hour`, present for a period of a day
  or less and then in place of `per_day`: a list of entries shaped like `StatisticsDay` with an hour in place of the
  date (a new schema `StatisticsHour`). The new operation `getAlertTitleStatistics`
  (`GET /alert-title-statistics`, tag Alert groups, `x-permission: alert-groups:read`, `x-listener: app`) takes the
  parameters of `getAlertGroupStatistics` that choose the selection (`StatisticsRoute`, `StatisticsIntegration`,
  `AgSeverity`, `AgUrgent`, `AgTeam`, `AgReopened`, `LabelMatchers`, `AgFirstAcknowledgedBy`, `From`, `To`) and
  answers the schema `AlertTitleStatistics` — `from`, `to` and `items` of `AlertTitleStatisticsItem`: `title`,
  `alert_group_count`, `reopen_count`, `never_acknowledged_count`, `time_to_resolve` (`DurationStats`) and `routes`
  (`EntityRef` list). The descriptions say that the answer is complete up to `dashboard.noisy_alerts` and not
  paginated, that titles with one Alert Group are not listed, and what a tie is. `api/README.md` names the two additions
  in its list of statistics.
- **Package `groups`** (ADR-0016, flat CRUD, `org_id` in every query, lint 1): `statistics.go` runs `group_by=all` as
  the existing statistics over the selection without a subject — the same `selection` and filters, so that C-40.AC-2
  holds by construction — and adds the count of critical ones; `titlestatistics.go` reads one grouped query over the
  summary rows of the selection (`query.sql`, `TitleStatistics`) with the order of FR-7 and the limit from
  `dashboard.noisy_alerts`, a built-in value of `defaults.md`. "Critical" is the Organization's Severity level that
  C-26.FR-8 calls critical. Days and hours are split in the person's time zone (C-09.FR-17).
- **API handlers** (`internal/api/alertgroups.go`): map `group_by=all` and the new fields; `GetAlertTitleStatistics`.
  The statistics page (`web/src/routes/statistics.tsx`, `statistics-table.tsx`) only needs to keep building where the
  generated types widen (`subject` nullable, the enum): no visible change.
- **Settings**: `dashboard.noisy_alerts` (10; `defaults.md`, decided); no metric, log event or Internal alert is added:
  the reads are requests that the existing request metrics count.

## Steps

1. Write the spec parts and `api/README.md`. Check: `make generate-check`, `make lint` (Redocly).
2. Add `group_by=all` and the critical split to `statistics.go` and its queries. Check: `statistics_test.go`,
   `statistics_integration_test.go` (the five Alert Groups of AC-1, the equality with `group_by=route`).
3. Write `titlestatistics.go` and its query. Check: `titlestatistics_test.go` (the order, the ties, the limit, the titles
   with one Alert Group left out).
4. Add the handlers and keep the statistics page compiling. Check: `alertgroups_test.go`, `make ci`.
5. Write `test/e2e/dashboard_statistics_test.go` and extend `statistics_test.go`. Check: the Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
API=localhost:8080/api/v1   # A: the Admin's header array; V: a Viewer's; as in S-174
CLK=localhost:8082/_dev/clock; FAM=127.0.0.1:19093/_fake
# the data of C-40.AC-1 through the fake Alertmanager: 5 Alert Groups in the last 7 days (2 critical), acknowledged after 2, 4 and 6 minutes
# and resolved after 10, 20, 40 and 60 minutes with the development clock, 2 more in the 7 days before (the helper of test/e2e/statistics_test.go)
F='from='$(date -u -v-7d +%FT%TZ)'&to='$(date -u +%FT%TZ)
curl -s "${A[@]}" "$API/alert-group-statistics?group_by=all&$F" | jq -c '.items[0] | {s: .subject, n: .alert_group_count, a: .time_to_acknowledge.median_seconds, r: .time_to_resolve.median_seconds, c: ([.per_day[].critical_count] | add)}'
# {"s":null,"n":5,"a":240,"r":1800,"c":2}
# C-40.AC-2: the sum over the Routes equals it, and one Route's item equals the item of all with that Route chosen
curl -s "${A[@]}" "$API/alert-group-statistics?group_by=route&$F" | jq '[.items[].alert_group_count] | add'   # 5
curl -s "${A[@]}" "$API/alert-group-statistics?group_by=all&route=$R&$F" | jq -c '.items[0] | [.alert_group_count, .time_to_acknowledge.p95_seconds]'
curl -s "${A[@]}" "$API/alert-group-statistics?group_by=route&route=$R&$F" | jq -c '.items[0] | [.alert_group_count, .time_to_acknowledge.p95_seconds]'   # the same two numbers
# a period of one day: hours instead of days
curl -s "${A[@]}" "$API/alert-group-statistics?group_by=all&from=$(date -u -v-1d +%FT%TZ)&to=$(date -u +%FT%TZ)" | jq -c '.items[0] | {d: (.per_day | length), h: (.per_hour | length)}'
# {"d":0,"h":24}  (or the hours of the period)
# C-40.AC-5: three KubePodCrashLooping (one reopened twice, two resolved without acknowledgement), two NodeDown (one Reopen), two DiskFull, one CertExpiring
curl -s "${A[@]}" "$API/alert-title-statistics?$F" | jq -c '[.items[] | [.title, .alert_group_count, .reopen_count, .never_acknowledged_count]]'
# [["KubePodCrashLooping",3,2,2],["NodeDown",2,1,0],["DiskFull",2,0,0]]
# C-40.AC-6: a Viewer reads, a token without alert-groups:read does not, no credentials is 401
curl -s -o /dev/null -w '%{http_code}\n' "${V[@]}" "$API/alert-title-statistics?$F"    # 200
curl -s -o /dev/null -w '%{http_code}\n' "${NOPERM[@]}" "$API/alert-title-statistics?$F"   # 403
curl -s -o /dev/null -w '%{http_code}\n' "$API/alert-title-statistics?$F"              # 401
curl -s -o /dev/null -w '%{http_code}\n' "${A[@]}" "$API/alert-title-statistics?team=TM0000000000X&$F"   # 422 (unknown_id)
make lint-arch
```

`test/e2e/dashboard_statistics_test.go` repeats these through the generated client, and checks that the page of
statistics still shows By route with the numbers of S-174's fixtures.

## Open questions

- **The shape of the additions.** The PRD names `group_by=all`, the per-day split by critical and below critical and the
  hours of a period of a day or less, but not the field names; this story adds `critical_count` to `StatisticsDay` for
  every `group_by`, `per_hour` beside `per_day`, and a nullable `subject`. Say if the review wants the split only on
  `all` or other names, before the generated clients carry them (the Terraform provider and the statistics page read
  this schema).
- **Which Severity level is "critical".** C-40.FR-5 refers to C-26.FR-8 for the split; this story takes the
  Organization's level that C-26 names critical, as the Minimum severity of a Route does. Confirm that no second level
  counts as critical for the dashboard.

## Notes

- Suggested commit: `feat(groups): add statistics of the whole selection with the critical split and of noisy alert titles`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases. No migration: the
  columns the reads need (`title`, `reopen_count`, `first_acknowledged_at`, `severity_level`) exist, and an index that
  the measurements of S-224 call for comes with that story.
- The counts of open Alert Groups are two calls of `getAlertGroupCounts` (the statuses, then `unclaimed=true`), as C-40
  says; the dashboard adds no operation for them, and neither period nor `first_acknowledged_by` is sent to it (S-225).
- An Alert Group resolved together with its main one is left out of the statistics (S-206); the dashboard inherits it,
  which the documentation of S-227 names.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-40.FR-2 | partial | the period and filters accepted by the new reads; the controls and the URL are S-225 |
| C-40.FR-3 | partial | `alert-groups:read` on the reads; the page's rule is S-225, S-226 |
| C-40.FR-4 | partial | the totals, medians and 95th percentiles of `all`; the figures and the counts of open Alert Groups are S-225 |
| C-40.FR-5 | partial | the per-day and per-hour series with the critical split; the chart is S-225 |
| C-40.FR-7 | partial | the titles with their counts; the table and the link to the list are S-226 |
| C-40.FR-10 | partial | the shape of the queries (index, `org_id`, summary rows); the budgets are measured in S-224 |
| C-40.AC-1 | partial | the figures through the API; the page shows them in S-225 |
| C-40.AC-2 | full | |
| C-40.AC-5 | partial | the order and the counts through the API; the link that opens the list is S-226 |
| C-40.AC-6 | partial | the Permission, the Viewer, `401` and `403` of both reads; the Pages' read is S-224, the page is S-225, S-226 |
