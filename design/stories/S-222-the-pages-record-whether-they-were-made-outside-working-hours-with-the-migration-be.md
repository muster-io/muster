---
id: S-222
title: The Pages record whether they were made outside Working hours, with the migration (BE)
capability: C-40
kind: be
layer: L2
depends_on: [S-154, S-155, S-157, S-169, S-177]
covers: [C-40.FR-6, C-40.AC-4]
files_touched:
  - internal/db/migrations/00NN_pages_outside_working_hours.up.sql
  - internal/db/migrations/00NN_pages_outside_working_hours.down.sql
  - internal/db/migrate_test.go
  - design/db/schema.md
  - internal/groups/query.sql
  - internal/groups/pages.go
  - internal/groups/pages_test.go
  - internal/groups/groups_integration_test.go
  - internal/escalation/pages.go
  - internal/escalation/engine.go
  - internal/escalation/calls.go
  - internal/escalation/pages_test.go
  - internal/escalation/pages_integration_test.go
  - internal/escalation/calls_test.go
  - test/e2e/pages_test.go
acceptance:
  - "[C-40.FR-6, C-40.AC-4] Every Page written from now on carries `pages.outside_working_hours`, true or false, set when the Page is made with the Working hours of that moment — those of the owning Team the run of its chain had when it started, else the Organization's (C-25.FR-9, P-73) — for a Page by Mention of a step, a later Mention of a Notification rule, and a call, whether of a call step or of a call action of a rule: the same Page made at Saturday 03:00 and at Monday 11:00 of the Team's time zone reads true and false (`pages_test.go`, `pages_integration_test.go`, `calls_test.go`)."
  - "[C-40.FR-6, C-40.AC-4] A later change of the Working hours of the Team or of the Organization changes no stored value: the Pages already made keep theirs, and a Page made after the change takes the new hours (`pages_integration_test.go`, `test/e2e/pages_test.go`)."
  - "[C-40.FR-6] A Page of a Team or of everyone in the chat, a `page_team` step, is written with the value as well, which no statistic reads; a Page made by a binary without this migration, or before it, has null, which the dashboard reads as inside Working hours; the migration adds the nullable column, fills nothing and replaces no CHECK, and down drops it (`migrate_test.go`)."
  - "[C-40.FR-6] The Page's Timeline entry, the Thread replies, the Assignees, the metric `muster_pages_total` and the API of the Pages (`getAlertGroup`, the chain runs) are unchanged: nothing reads the column yet (`pages_test.go`, `make lint-arch`)."
verify: "make ci test-integration e2e-go"
operator_attention: false
issue: null
---

# S-222. The Pages record whether they were made outside Working hours, with the migration (BE)

## Scope

**IN**

- The column `pages.outside_working_hours` and its value on every Page the dispatcher records: the first Mentions of a
  step, the later Mentions of a Notification rule, and the calls (C-40.FR-6, "Whether a Page came outside Working hours
  is recorded on the Page when it is made").
- The Working hours of that moment, read where the chain's step is made, so that a later change of the hours never
  changes past counts (C-40.AC-4).

**OUT**

- Reading the column: the operation `page-statistics` and its "since" is S-224; the column "Outside Working hours" of
  the dashboard is S-226. Until S-224 merges nothing reads the column, which is why this story comes first.
- A backfill: a Page made before the migration is never classified (C-40.FR-6); null stands for it.

## Contracts

- **Schema** (a new migration, expand only; the number is assigned by the orchestrator at dispatch; `schema.md` §4.18,
  the entry of `pages`): `pages.outside_working_hours boolean`, null for a Page made before the version that records it
  or by a replica of an earlier binary during a rolling update, true or false otherwise. No CHECK replaces another and
  nothing is backfilled: the dashboard reads null as inside Working hours, so a null is a Page the hint of the column
  says "counts as inside" (S-224 gives the dashboard the moment from which values exist). A replica of the earlier
  version neither reads nor writes the column, so its Pages keep null. The down migration drops the column.
- **Package `groups`** (ADR-0016, lint 2: `groups` is the only writer of `pages`): `Page` and `StepPage` gain
  `OutsideWorkingHours bool`; `InsertPage` in `query.sql` writes it; `chainSteps.page` and `chainSteps.call` pass it
  through. `groups` never computes it, since it knows no package of Paging (S-169); the callers fill it.
- **Package `escalation`**: `walk.workingHours` (reads the Working hours of the run's chain once per firing) already
  decides the skips of "only during Working hours" (S-169); the same value, tested with `WorkingHours.Contains` at the
  moment of the Page on the business clock, fills `StepPage.OutsideWorkingHours` in `Pages.MakeStep`, in `FireRule` for
  the later Mentions and calls of a rule (the run's owning Team is read from the rule run's escalation run), and in the
  calls of `calls.go`. The Page of a `page_team` step carries it too.
- **Clock**: the business clock, injected (AGENTS.md "Two clocks"): the Working hours are domain time and follow the
  development clock.
- **Documents**: `schema.md` §4.18 (the column and what null means).

## Steps

1. Write the migration and `schema.md`. Check: `make test-integration` runs up, down and up; `migrate_test.go` finds
   existing Pages with a null value.
2. Add the field to `groups.Page`/`StepPage` and `InsertPage`. Check: `pages_test.go`, `make generate-check`.
3. Fill it in `escalation` for the three paths. Check: `pages_test.go`, `calls_test.go`, `pages_integration_test.go`
   (inside, outside, after a change of the hours).
4. Extend `test/e2e/pages_test.go` with the Saturday/Monday case. Check: the Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
API=localhost:8080/api/v1   # A: the Admin's header array, as in S-155's setup
# the Team "payments" with Working hours Monday to Friday 09:00 to 18:00 in Europe/Berlin and a chain Paging petr importantly at once,
# a Schedule of one person, the fake Mattermost server with petr's Account link (S-155's setup)
curl -s localhost:8082/_dev/clock | jq -r .now   # read it, then advance it to the Saturday 03:00 of the Team's zone
curl -s -X POST localhost:8082/_dev/clock -d "{\"advance_seconds\": $TO_SATURDAY_0300}"
# C-40.AC-4: a Page outside the hours, then one inside
FAM=127.0.0.1:19093/_fake
curl -s -X PUT $FAM/groups/p1 -d '{"receiver":"sev","route":"{}","labels":{"alertname":"DiskFull"}}' > /dev/null
PUTA p1 a '{"labels":{"team":"payments","severity":"warning"}}'; NOTIFY p1 '{"reason":"first notification"}'   # PUTA and NOTIFY as in S-155
psql "$MUSTER_DB" -c "select kind, outside_working_hours from pages order by id"   # mention | t
curl -s -X POST localhost:8082/_dev/clock -d '{"advance_seconds": 187200}'         # Monday 11:00
curl -s -X PUT $FAM/groups/p2 -d '{"receiver":"sev","route":"{}","labels":{"alertname":"NodeDown"}}' > /dev/null
PUTA p2 a '{"labels":{"team":"payments","severity":"warning"}}'; NOTIFY p2 '{"reason":"first notification"}'          # a second Alert Group of payments
psql "$MUSTER_DB" -c "select kind, outside_working_hours from pages order by id"   # mention | t, mention | f
# a change of the Team's hours (updateTeam with If-Match: Monday to Friday 12:00 to 20:00) changes nothing stored
psql "$MUSTER_DB" -c "select outside_working_hours from pages order by id"          # t, f as before
# a later Mention of the rule and a call take the value of their own moment
psql "$MUSTER_DB" -c "select kind, again, outside_working_hours from pages where kind in ('mention','call') order by id"
# a Page written before the migration: migrate down one step on a copy, insert a Page, migrate up: the column is null
```

`test/e2e/pages_test.go` repeats the first three with the fake gateway for the call, and `migrate_test.go` the last.

## Open questions

None.

## Notes

- Suggested commit: `feat(escalation): record on each page whether it was made outside working hours`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases. The migration number
  is assigned by the orchestrator at dispatch; the migration replaces no CHECK and needs no re-versioning.
- The column is nullable, not `NOT NULL DEFAULT false`, because C-40.FR-6 says that the hint of the column "says from
  when it counts": the earliest Page with a value is that moment, which S-224 reads (`since`), and a default of false
  would make the Pages of before indistinguishable from the first inside Working hours. The two readings are equal for
  the counts, as the PRD requires.
- The value is recorded for Pages of a Team too, though "Who was Paged" never counts them, so that a later statistic of
  Teams would not need another migration; this adds one boolean to a row that already exists.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-40.FR-6 | partial | the recording of each Page's outside-Working-hours value; the table, the counts and the hint are S-224, S-226 |
| C-40.AC-4 | partial | the value at the moment of the Page and its stability; the table's numbers are S-224 |
