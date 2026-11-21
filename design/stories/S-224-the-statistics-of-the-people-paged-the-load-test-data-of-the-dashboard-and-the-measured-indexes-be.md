---
id: S-224
title: The statistics of the people Paged, the load-test data of the dashboard and the measured indexes (BE)
capability: C-40
kind: be
layer: L2
depends_on: [S-222, S-223, S-178, S-157, S-174, S-004]
covers: [C-40.FR-3, C-40.FR-6, C-40.FR-10, C-40.AC-4, C-40.AC-6, C-40.AC-9]
files_touched:
  - api/openapi.yaml
  - api/README.md
  - internal/db/migrations/00NN_dashboard_indexes.up.sql
  - internal/db/migrations/00NN_dashboard_indexes.down.sql
  - internal/db/migrate_test.go
  - design/db/schema.md
  - design/prd/L2.md
  - internal/groups/query.sql
  - internal/groups/pagestatistics.go
  - internal/groups/pagestatistics_test.go
  - internal/groups/statistics_integration_test.go
  - internal/groups/dashboard_plans_integration_test.go
  - internal/api/alertgroups.go
  - internal/api/alertgroups_test.go
  - test/load/main.go
  - test/e2e/dashboard_pages_test.go
acceptance:
  - "[C-40.FR-6, C-40.AC-4] `getPageStatistics` (`/page-statistics`) answers, for the Alert Groups that started in [`from`, `to`) and match the filters of `getAlertGroupStatistics`, one item per User whom a Page named on them — complete, not paginated, by `paged_count` plus `calls_count`, most first, then by name regardless of case, then by `public_id`: with Petr Paged importantly on two Alert Groups, his rule a Mention, a wait of 5 minutes and a call, once inside and once outside the Working hours of the chain's owning Team, and his Destination's Connection having no Account link of his on one of them, the item reads `paged_count` 2, `calls_count` 2, `outside_working_hours_count` 2, `not_reached_count` 1; after the Team's Working hours change the counts stay; after he acknowledges one first, `acknowledged_first_count` 1 (`pagestatistics_test.go`, `statistics_integration_test.go`, `test/e2e/dashboard_pages_test.go`)."
  - "[C-40.FR-6] `paged_count` counts the Pages by Mention that named the User, `calls_count` the Pages by a call through a Notification gateway that named them, one per Page however many Alert Groups its call combined (C-30.FR-4); `outside_working_hours_count` those Pages of both kinds with `outside_working_hours` true, null counting as inside; `not_reached_count` the Pages of both kinds whose person ended `not_delivered` (`page_users.outcome`, C-32.FR-10), and `acknowledged_first_count` the Alert Groups of the selection whose `first_acknowledged_by_user_id` is the User (C-09.FR-27); a Page of a Team or of everyone in the chat names nobody and adds no row; a Page made after `to` on an Alert Group that started in the period counts, whenever it was made (`pagestatistics_test.go`, `statistics_integration_test.go`)."
  - "[C-40.FR-6] The response also carries `outside_working_hours_since`, the time of the earliest Page of the Organization with a recorded `outside_working_hours` — null while none — which the dashboard's hint of that column names, and the Pages before it count as inside; a Page is kept with its Alert Group's summary row (`retention.alert_group_summaries`), so the table reaches as far back as the other parts; deleted Users and Service accounts are not rows, a User's login and `public_id` are (`pagestatistics_test.go`, `statistics_integration_test.go`)."
  - "[C-40.FR-3, C-40.AC-6] `getPageStatistics` has `x-permission: alert-groups:read`: a Viewer and a Responder read it as an Admin does, a token without the Permission gets `403`, without authentication `401`, and every query of the dashboard — the three new reads of S-223 and this one — passes `make lint-arch`, `org_id` in every query (`alertgroups_test.go`, `make lint-arch`)."
  - "[C-40.FR-10, C-40.AC-9] `test/load/main.go` gains a mode that fills a development database with the data of P-96 — 500,000 summary rows with their first acknowledgements, titles, Reopens, Routes, critical and below-critical Severity levels, and 50,000 Pages with their people, both kinds, inside and outside Working hours — and `dashboard_plans_integration_test.go`, behind the build tag `integration` and a variable that names such a database, measures each of the four reads of the dashboard (`group_by=all`, `alert-title-statistics`, `page-statistics`, and the counts of open Alert Groups) at the 95th percentile over 50 runs for the last 7 days and for 90 days against 1 second and 3 seconds, and checks with `EXPLAIN` that each plan starts from `alert_groups_started_idx` (`dashboard_plans_integration_test.go`)."
  - "[C-40.FR-10, C-40.AC-9] Where a plan needs an index the migration adds it — expand only, `CREATE INDEX CONCURRENTLY` in a transaction of its own, as 0053 and 0054 do — and `schema.md` records both the measurement and the index; when none is needed the migration is not written and `schema.md` records the measured times; P-96 in L2 §5.3 is confirmed as decided with the measured values, or the open question says why not (`migrate_test.go`, `design/prd/L2.md`, `design/db/schema.md`)."
verify: "make ci test-integration e2e-go"
operator_attention: false
issue: null
---

# S-224. The statistics of the people Paged, the load-test data of the dashboard and the measured indexes (BE)

## Scope

**IN**

- The new read `getPageStatistics`: "Who was Paged", the load of On-calls per User (C-40.FR-6), with the moment from
  which Pages outside Working hours are recorded.
- The data of P-96 in the load test, the measurement of every read of the dashboard against its budget, and the indexes
  that the measurements call for, with `schema.md` (C-40.FR-10, AC-9).

**OUT**

- The column and its value on each Page: S-222. The other two reads, `group_by=all` and `alert-title-statistics`: S-223.
- The table on the page, its sorting and hints: S-226.
- Time On-call from the Schedules, which C-40.FR-6 leaves out on purpose.

## Contracts

- **Spec** (`api/openapi.yaml`): `getPageStatistics` (`GET /page-statistics`, tag Alert groups,
  `x-permission: alert-groups:read`, `x-listener: app`) takes the parameters of `getAlertGroupActorStatistics`
  (`StatisticsRoute`, `StatisticsIntegration`, `AgSeverity`, `AgUrgent`, `AgTeam`, `AgReopened`, `LabelMatchers`,
  `AgFirstAcknowledgedBy`, `From`, `To`) and answers `PageStatistics`: `from`, `to`,
  `outside_working_hours_since` (date-time or null) and `items` of `PageStatisticsItem` — `user` (`ActorRef`),
  `paged_count`, `calls_count`, `outside_working_hours_count`, `not_reached_count`, `acknowledged_first_count`. The
  descriptions say who is a row, what each count counts as C-40.FR-6 says, that the items are complete and ordered, and
  what null counts as. `api/README.md` lists it with the other statistics.
- **Package `groups`** (flat CRUD; `internal/groups` already owns the Pages and the statistics): `pagestatistics.go`
  with its query in `query.sql`: from the selection's Alert Groups (`alert_groups_started_idx`), join `pages`
  (`pages_group_idx`) and `page_users`, count `kind = 'mention'` as `paged`, `kind = 'call'` as `calls` — one per Page —,
  `coalesce(outside_working_hours, false)` as outside, `page_users.outcome = 'not_delivered'` as not reached, and the
  Alert Groups' `first_acknowledged_by_user_id` for the last column; a Page without a User row (`page_team`) is not
  joined. `outside_working_hours_since` is the minimum `pages.created_at` of the Organization where the column is not
  null.
- **Load test** (`test/load/main.go`): a mode that writes the data of P-96 straight into a development database, with
  generic titles, Routes, Teams and people (`payments`, `search`, `example.org`), in batches; it writes nothing a
  production code path would not.
- **Measurement and indexes**: `dashboard_plans_integration_test.go` (tag `integration`, skipped without the database
  variable) runs the reads over the filled database and fails over budget; `schema.md` §4.9 and §4.18 get the plans and
  any index; the migration, if one is needed, is `00NN_dashboard_indexes` (the number is assigned by the orchestrator at
  dispatch) and replaces no CHECK. Candidates the PRD does not decide and the measurement does: an index on
  `pages (org_id, alert_group_id, id)` covering the counts, and one on `pages (org_id, created_at) WHERE
  outside_working_hours IS NOT NULL` for `outside_working_hours_since`.
- **Documents**: `design/prd/L2.md` §5.3 (P-96: decided, with the measured values, as P-92's neighbours are).

## Steps

1. Write the spec parts and `api/README.md`. Check: `make generate-check`, `make lint`.
2. Write `pagestatistics.go` and its query; the handler. Check: `pagestatistics_test.go` (the counts, the null as
   inside, the call combined from two Alert Groups counted once, the Team's Page left out),
   `statistics_integration_test.go`.
3. Add the data mode to `test/load/main.go`. Check: `go run ./test/load -mode dashboard-data` against `make dev-db`
   fills the tables (counts below).
4. Write `dashboard_plans_integration_test.go`, measure, add the migration where a plan calls for one, update
   `schema.md` and P-96. Check: the test with the variable set; `make test-integration`.
5. Write `test/e2e/dashboard_pages_test.go` for C-40.AC-4. Check: the Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
API=localhost:8080/api/v1   # A, V and NOPERM as in S-223; the setup of S-155 and S-222: the Team "payments", the chain, petr's rule
CLK=localhost:8082/_dev/clock
# C-40.AC-4: two important Pages of petr, one inside and one outside the Team's Working hours, a call each, one without an Account link
F='from='$(date -u -v-7d +%FT%TZ)'&to='$(date -u -v+1d +%FT%TZ)
curl -s "${A[@]}" "$API/page-statistics?$F" | jq -c '.items[] | select(.user.login=="petr") | {p: .paged_count, c: .calls_count, o: .outside_working_hours_count, n: .not_reached_count, a: .acknowledged_first_count}'
# {"p":2,"c":2,"o":2,"n":1,"a":0}
# the Team's Working hours change (updateTeam with If-Match): the counts stay
curl -s "${A[@]}" "$API/page-statistics?$F" | jq -c '.items[] | select(.user.login=="petr") | .outside_working_hours_count'   # 2
# petr acknowledges one first
curl -s "${A[@]}" "$API/page-statistics?$F" | jq -c '.items[] | select(.user.login=="petr") | .acknowledged_first_count'    # 1
# a step that Pages the whole Team adds no row
curl -s "${A[@]}" "$API/page-statistics?$F" | jq '[.items[].user.login] | index("payments")'                                  # null
curl -s "${A[@]}" "$API/page-statistics?$F" | jq -c '.outside_working_hours_since'   # the time of the first Page of this run, not null
# C-40.AC-6
curl -s -o /dev/null -w '%{http_code}\n' "${V[@]}" "$API/page-statistics?$F"         # 200
curl -s -o /dev/null -w '%{http_code}\n' "${NOPERM[@]}" "$API/page-statistics?$F"    # 403
curl -s -o /dev/null -w '%{http_code}\n' "$API/page-statistics?$F"                   # 401
make lint-arch
# C-40.AC-9: the data of P-96 in a development database, then the measurement
go run ./test/load -mode dashboard-data -summaries 500000 -pages 50000
psql "$MUSTER_DB" -c "select (select count(*) from alert_groups), (select count(*) from pages)"   # 500000 | 50000
MUSTER_TEST_DASHBOARD_DB="$MUSTER_DB" go test -tags integration -run TestDashboardPlans ./internal/groups -v
# --- PASS: each read at p95 within 1 s (7 days) and 3 s (90 days); every plan starts from alert_groups_started_idx
```

The measurement runs on the VM `muster-lab`, not on a laptop (`make dev` there), as the heavy runs do; the pull request
carries the output.

## Open questions

- **From when Pages outside Working hours count.** C-40.FR-6 says that the column's hint says "from when it counts" but
  not how the API gives that moment. This story adds `outside_working_hours_since` to `PageStatistics` (the earliest
  recorded Page), because S-222 leaves older Pages null. Say if the review prefers the release version in the hint
  instead, which the API cannot know.
- **The names of the items.** The PRD names the columns of the table, not the properties; this story names them as
  above (`paged_count`, `calls_count`, `outside_working_hours_count`, `not_reached_count`, `acknowledged_first_count`).

## Notes

- Suggested commit: `feat(groups): add statistics of the people paged and the load-test data of the dashboard`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases. The migration, if the
  measurements call for one, is expand only and its number is assigned by the orchestrator at dispatch.
- The load test data is a development fixture: it lives in `test/load/` and is never part of the shipped binary
  (`cmd/muster/` calls only `internal/cli`).
- "Acknowledged first" counts the Alert Groups of the selection, so a person with no Page at all but a first
  acknowledgement is a row of the By people table of the statistics page and not of this one; the dashboard says so in
  its hint (S-226).
- The measurement is the story's acceptance: if a read cannot reach its budget with an index, the story records the
  measured times in its pull request, leaves P-96 provisional and asks the maintainer, as a story does for any
  provisional value.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-40.FR-3 | partial | `alert-groups:read` on the Pages' read; the page is S-225, S-226 |
| C-40.FR-6 | partial | the read of the table's numbers and the moment of recording; the recording is S-222, the table on the page S-226 |
| C-40.FR-10 | full | the data, the budgets and the indexes; the shape of the other reads is S-223's |
| C-40.AC-4 | partial | the numbers through the API; the table in the browser is S-226 |
| C-40.AC-6 | partial | the Pages' read and the lint; the Viewer's view of the page is S-226 |
| C-40.AC-9 | full | |
