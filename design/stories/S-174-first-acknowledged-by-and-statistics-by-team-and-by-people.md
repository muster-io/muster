---
id: S-174
title: Who made the first acknowledgement, filled from the Timeline, and the statistics By Team and By people with the filters of every view (BE)
capability: C-25
kind: be
layer: L2
depends_on: [S-118]
covers: [C-09.FR-15, C-09.FR-27, C-09.FR-28, C-09.FR-29, C-09.FR-30, C-09.AC-27, C-09.AC-28, C-09.AC-29, C-09.AC-30, C-09.AC-31, C-09.AC-32, C-09.AC-33]
files_touched:
  - api/openapi.yaml
  - internal/db/migrations/0045_first_acknowledged_by.up.sql
  - internal/db/migrations/0045_first_acknowledged_by.down.sql
  - internal/db/migrate_test.go
  - internal/db/db_test.go
  - design/db/schema.md
  - internal/groups/query.sql
  - internal/groups/dispatcher.go
  - internal/groups/dispatcher_test.go
  - internal/groups/commands.go
  - internal/groups/commands_test.go
  - internal/groups/filters.go
  - internal/groups/statistics.go
  - internal/groups/statistics_test.go
  - internal/groups/statistics_integration_test.go
  - internal/groups/list_test.go
  - internal/groups/list_integration_test.go
  - internal/api/alertgroups.go
  - internal/api/alertgroups_test.go
  - internal/api/server.go
  - web/src/routes/statistics.tsx
  - web/src/components/statistics-table.tsx
  - test/e2e/statistics_test.go
  - docs/install/versions.md
acceptance:
  - "[C-09.FR-27, C-09.AC-27] The first Acknowledge of an Alert Group writes `first_acknowledged_by_user_id` in the transaction that sets `first_acknowledged_at`; a Takeover, an Unacknowledge, a later Acknowledge by someone else, a Reopen into acknowledged and an Unresolve change neither `first_acknowledged_at` nor `first_acknowledged_by_user_id`: Alice acknowledges 5 minutes after the start, Bob takes over and unacknowledges, Carol acknowledges, the system resolves and the Alert Group reopens, and the row still names Alice at the time of her acknowledgement (`commands_test.go`, `statistics_test.go`, `test/e2e/statistics_test.go`)."
  - "[C-09.FR-27, C-09.AC-30] Migration `0045_first_acknowledged_by` adds `alert_groups.first_acknowledged_by_user_id` with its foreign key to `users` and `alert_groups_first_acknowledged_by_check` (none without `first_acknowledged_at`), added `NOT VALID` and validated in a second transaction, and fills it in a third from the earliest `acknowledged` Timeline entry of each Alert Group that has `first_acknowledged_at` and whose Timeline is still whole, with its `created` entry: over a database at the version before it, an Alert Group that Alice acknowledged and Bob took over gets Alice; one whose Timeline partitions were all dropped gets nobody; one that Alice acknowledged in a dropped month and Carol again, after an Unacknowledge, in a kept month gets nobody, not Carol; and one never acknowledged stays empty; down drops the column and the CHECK, and up again fills it again (`migrate_test.go`)."
  - "[C-09.FR-28, C-09.AC-28] `getAlertGroupActorStatistics` returns, for Alice with three Alert Groups acknowledged first after 2, 4 and 30 minutes, Bob with one after 10 minutes, Bob resolving two of Alice's, the Service account terraform resolving Bob's and the system Alice's third: Alice `acknowledged_first_count` 3, `resolved_count` 0, `median_seconds` 240, `p95_seconds` 1644; Bob 1, 2, 600 and 600; terraform with `actor.kind` `service_account`, 0, 1 and `time_to_acknowledge.count` 0 with null times; in that order, by `acknowledged_first_count`, most first, then by name (`statistics_integration_test.go`)."
  - "[C-09.FR-28, C-09.FR-30, C-09.AC-30] The Alert Groups that have `first_acknowledged_at` and no `first_acknowledged_by_user_id` make one item with `actor` null, last, with their count and their own times and `resolved_count` 0; the sum of `acknowledged_first_count` over the items equals the sum of `time_to_acknowledge.count` of `getAlertGroupStatistics?group_by=route` for the same period and filters; with `first_acknowledged_by` set there is no item with `actor` null (`statistics_integration_test.go`, `migrate_test.go`)."
  - "[C-09.FR-15, C-09.FR-28, C-09.AC-29] Both operations select the Alert Groups that started in [`from`, `to`): an Alert Group that started on the last day of the period and was first acknowledged a day after `to` counts for whoever acknowledged it first, with a time to acknowledge of more than a day; one that started before `from` and was acknowledged inside the period counts for nobody (`statistics_integration_test.go`)."
  - "[C-09.FR-29, C-09.AC-31] `getAlertGroupStatistics?group_by=team` gives one item per Team that is not deleted and per deleted Team with Alert Groups in the period, with the values and days of `group_by=route`; an Alert Group with the Teams payments and search counts for both, one without a Team for none; with `team` set the items are only that Team, or the caller's Teams for `mine` (`statistics_integration_test.go`)."
  - "[C-09.FR-30, C-09.AC-32] Both operations take `route`, `integration`, `severity`, `urgent`, `team`, `reopened`, `label` and `first_acknowledged_by` (a User's `public_id`, or `me` for the calling User), combined with AND and matched as `listAlertGroups` matches them, the Matchers `!=`, `=~` and `!~` included; `route` with `group_by=integration` and `integration` with `group_by=route` are accepted as filters instead of answering `422`; of four Alert Groups, `severity=critical&label=namespace%3D%22payments%22` counts 2 in every `group_by` and in the actor statistics, and adding `first_acknowledged_by=me` as Alice counts 1; a `first_acknowledged_by` that names no User, a Service account's id included, or an unknown `team` is `422` `unknown_id` (`statistics_integration_test.go`, `alertgroups_test.go`)."
  - "[C-09.FR-30, C-09.AC-33] A Viewer's and a Responder's token read both operations with the values an Admin's reads; `team=mine` and `first_acknowledged_by=me` from a Service account token are `422` `unsupported`, as `team=mine` and `owner=me` are on the list; without authentication both answer `401` (`alertgroups_test.go`)."
  - "The web UI builds and type-checks against the regenerated client, with `group_by` widened, and the statistics page still shows By route and By integration as before; the views By Team and By people are S-175's."
verify: "make ci test-integration e2e-go docs-check"
operator_attention: false
issue: 567
---

# S-174. Who made the first acknowledgement, filled from the Timeline, and the statistics By Team and By people with the filters of every view (BE)

## Scope

**IN**

- "First acknowledged by" on the summary row (C-09.FR-27): a column, `first_acknowledged_by_user_id`, written with
  `first_acknowledged_at` by the Acknowledge of the dispatcher and never changed afterwards, and the migration that
  fills it from the Timeline.
- The statistics per Team (`group_by=team`, C-09.FR-29) and the statistics By people, a new operation
  (C-09.FR-28), with the row "Unknown" and its rule.
- The filters of every statistics view (C-09.FR-30): those of the list (Route, Integration, Severity level, Urgent,
  Team with `mine`, Reopened, label Matchers) and `first_acknowledged_by`.
- Access: `alert-groups:read` only (C-09.FR-30); no Permission, no Team scoping.

**OUT**

- The views By Team and By people, the filters and the URL on the statistics page: S-175.
- The documentation's section on statistics with its screenshots: S-176.
- Out of scope for good (journal D688): exporting statistics as CSV, a split by day of By people, counts of
  acknowledgements at night or outside Working hours, statistics limited by Team, and counts of Takeovers, Snoozes and
  Notes.

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated):
  - `getAlertGroupStatistics`: `group_by` gains `team` (in the parameter and in `AlertGroupStatistics.group_by`). The
    parameters `route` and `integration` become filters of the Alert Groups for every `group_by`; with the `group_by`
    of their own kind they also choose the items, as today. It gains the parameters `AgSeverity`, `AgUrgent`,
    `AgTeam` (with `group_by=team` also the items: the Team, or the caller's Teams for `mine`), `AgReopened`,
    `LabelMatchers` and the new `AgFirstAcknowledgedBy`. Its description says that the period selects the Alert Groups
    that started in it.
  - `AgFirstAcknowledgedBy` (`first_acknowledged_by`, string): a User's `public_id`, deleted Users included, or `me`,
    the calling User, as `AgOwner` takes them; `422` `unknown_id` for an id that names no User, a Service account's
    included, and `422` `unsupported` for `me` from a Service account token. Only Users acknowledge (C-04.FR-2).
  - New `getAlertGroupActorStatistics` (`GET /alert-group-actor-statistics`, tag Alert groups, `alert-groups:read`,
    session or token, `x-listener: app`): the parameters of `getAlertGroupStatistics` without `group_by` and
    `time_zone`; `200` → `AlertGroupActorStatistics` {`from`, `to`, `items`}, each `AlertGroupActorStatisticsItem`
    {`actor` (`ActorRef`, or `null` for the row "Unknown"), `acknowledged_first_count`, `resolved_count`,
    `time_to_acknowledge` (`DurationStats`, whose `count` is `acknowledged_first_count`)}; `400`, `401`, `403`, `422`,
    `429`. The items are complete, not paginated: by `acknowledged_first_count`, most first, then by name regardless
    of case, then by the exact name, then by `public_id`, with `actor` null last. An actor with neither count is not
    an item.
  - `ActorRef`'s description adds "or is a row of the statistics by people"; a Service account is one only through
    what it resolved.
- **Schema** (migration `0045_first_acknowledged_by`; the orchestrator assigns `NN`, the next free number when the story
  starts, and the files, `migrate_test.go`, `schema.md` and `versions.md` take it): `alert_groups` gains
  `first_acknowledged_by_user_id bigint` (foreign key to `users`), nullable and without a default, so the `ADD COLUMN`
  changes no row, and `alert_groups_first_acknowledged_by_check`: `first_acknowledged_by_user_id IS NULL OR
  first_acknowledged_at IS NOT NULL`. There is no Service account column: only Users acknowledge (C-04.FR-2). Three
  transactions, each with `SET LOCAL lock_timeout = '5s'` and `statement_timeout = '5min'`, as `schema.md` §1 asks and
  0027 does: the column with the foreign key and the CHECK `NOT VALID`; their `VALIDATE`; the backfill, one `UPDATE`
  from the earliest (`at`, `id`) `acknowledged` entry of `timeline_entries` per `(org_id, alert_group_id)`
  (`timeline_entries_group_idx`), of the Alert Groups with `first_acknowledged_at` and no
  `first_acknowledged_by_user_id` whose Timeline is still whole — their `created` entry is still kept, and since
  retention drops whole monthly partitions, oldest first, so is every later entry; this leaves out an Alert Group whose
  first acknowledgement fell in a dropped month and a later one in a kept month — setting the entry's `actor_user_id`
  (the entry's `actor_kind` is `user`, as every acknowledgement's is). The entry's `at` is not matched with
  `first_acknowledged_at`: the transition and the record of the dispatcher read the clock separately. The repository's
  backfills are single statements in their own transaction (0007, 0021); the update touches at most the Alert Groups
  whose Timeline is kept, `retention.alert_details`. No index is added: every statistics query starts from the period on
  `alert_groups_started_idx`. Down drops the CHECK and the column. `schema.md`: the column in the Status row of
  `alert_groups`, the invariant, the paragraph on `first_acknowledged_at`, and the migration in the list at the top.
- **Groups** (`internal/groups`):
  - `dispatcher.go`, `query.sql`: the Group reads and writes the column with the row.
  - `commands.go`: the Acknowledge that sets `FirstAcknowledgedAt` also sets the User; nothing else writes either, so
    Takeover, Unacknowledge, Reopen, Unresolve and Auto-unacknowledge keep them.
  - `filters.go`: `Filter.FirstAcknowledgedBy`, resolved like the Owner filter (a User's `public_id`, or `me` for the
    calling User; a Service account is refused), and one resolution of the filters that the list, the counts and the
    statistics share.
  - `statistics.go`, `query.sql`: `ByTeam` with `TeamStatistics` over `alert_group_teams` and `ListStatisticsTeams` (as
    `ListStatisticsRoutes`); `ActorStatistics` aggregating the selected Alert Groups twice — by the first
    acknowledgement (count, median and 95th percentile by `percentile_cont`, as the other statistics) and by the
    resolver of a Resolve by a User or a Service account (`resolved_by_kind = 'user'`) — joined by actor, with the row
    of the Alert Groups that have `first_acknowledged_at` and no `first_acknowledged_by_user_id`; the names from `users`
    and `service_accounts`, deleted ones marked `deactivated`. Every query filters by `org_id` (lint 1). The `=`
    Matchers go into SQL by containment; when a Matcher with `!=`, `=~` or `!~` is given, the Alert Groups of the period
    that pass the other filters are read with their common labels, the Matchers are applied in Go as `counts.go` applies
    them, and the aggregation runs over the ids that pass, so both paths give the same medians.
- **API** (`internal/api/alertgroups.go`, `server.go`): `GetAlertGroupStatistics` passes the filters and `team`;
  `GetAlertGroupActorStatistics` joins the operations the build serves, else it answers `501`. `me` and `mine` come
  from the caller of the request.
- **Web** (`statistics.tsx`, `statistics-table.tsx`): only what keeps `make lint` green once the generated client
  widens `group_by` — the type of the subject accepts `team`; no text and no view is added.
- **Settings**: `retention.alert_details`, `retention.alert_group_summaries`, `alert_group.list_range` (the default
  period), `organization.time_zone`. None new.
- **Log events, metrics, Internal alerts**: none new. **Fake servers**: none; the story talks to no external system.
- **Documentation**: `docs/install/versions.md` — the upgrade note of the migration (what it fills, that older Alert
  Groups and those first acknowledged by a 0.1 replica during the rolling update count as "Unknown", the three
  transactions and how to recover from a failure, as for 0027).

## Steps

1. Write the migration, `migrate_test.go` and `schema.md`. Check: `make test-integration` runs up, down and up and the
   backfill cases.
2. Write the column into the Group and the Acknowledge. Check: `commands_test.go` and `dispatcher_test.go` cover the
   first Acknowledge and every later transition of C-09.AC-27.
3. Change the spec and run `make generate`; widen the subject type of the statistics page. Check: `make
   generate-check`, `make lint`.
4. Write the shared filters, `ByTeam` and `ActorStatistics`. Check: `statistics_test.go`,
   `statistics_integration_test.go` with the numbers of C-09.AC-28, the period of C-09.AC-29, the Teams of C-09.AC-31
   and the filters of C-09.AC-32, a `=~` Matcher among them.
5. Write the handlers and `test/e2e/statistics_test.go`, and the upgrade note. Check: the Verification below; `make
   docs-check`.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-118: the Admin's token A, the Integration "sev" with its fake receiver, NOTIFY, PUTA, AG and GET, the Teams
# payments (TP) and search (TS). As S-032 makes bob, the Admin creates the Users alice, bob and carol (Responder, each
# with that name and login) and a Viewer, with the Personal access tokens AL, BO, CA and V; as in S-016 the Service
# account terraform (Responder) with its token SAT. Make the tokens before the clock moves.
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake; CLK=localhost:8082/_dev/clock
CLOCK() { curl -s -X POST $CLK -d "{\"advance_seconds\": $1}" > /dev/null; }
NEW() { curl -s -X PUT $FAM/groups/$1 -d "{\"receiver\":\"sev\",\"route\":\"{}\",\"labels\":{\"alertname\":\"$1\"}}" > /dev/null
        PUTA $1 a "{\"labels\":{\"cluster\":\"$1\",\"severity\":\"${2:-warning}\"${3:+,$3}}}"; NOTIFY $1 '{"reason":"first notification"}'
        AG "cluster%3D%22$1%22"; }
DO() { curl -s "${@:3}" -X POST "$API/alert-groups/$1/$2" > /dev/null; }
PEOPLE() { curl -s "${@:2}" "$API/alert-group-actor-statistics?$1" | jq -c '[.items[] | {n: .actor.name, a: .acknowledged_first_count, r: .resolved_count}]'; }
FROM=$(curl -s $CLK | jq -r '.now | @uri')
FIRST() { psql "$MUSTER_DATABASE_URL" -Atc "SELECT u.login, round(extract(epoch FROM g.first_acknowledged_at - g.created_at))::int
  FROM alert_groups g JOIN users u ON u.id = g.first_acknowledged_by_user_id WHERE g.public_id = '$1'"; }

# C-09.AC-27: the first acknowledgement stays
G0=$(NEW s0); CLOCK 300; DO $G0 acknowledge "${AL[@]}"; DO $G0 acknowledge "${BO[@]}"; DO $G0 unacknowledge "${BO[@]}"
DO $G0 acknowledge "${CA[@]}"
FIRST $G0                                                                                     # alice|300
curl -s -X DELETE $FAM/groups/s0/alerts/a > /dev/null; NOTIFY s0 '{"reason":"resolved"}'
PUTA s0 a '{"labels":{"cluster":"s0","severity":"warning"}}'; NOTIFY s0 '{"reason":"new alerts"}'
GET $G0 | jq -c '{status, o: .owner.login, reopen_count}'                                    # {"status":"acknowledged","o":"carol","reopen_count":1}
FIRST $G0                                                                                     # alice|300
PEOPLE "label=cluster%3D%22s0%22&from=$FROM" "${A[@]}"                                       # [{"n":"alice","a":1,"r":0}]

# C-09.AC-28: By people
G1=$(NEW s1); CLOCK 120;  DO $G1 acknowledge "${AL[@]}"
G2=$(NEW s2); CLOCK 240;  DO $G2 acknowledge "${AL[@]}"
G3=$(NEW s3); CLOCK 1800; DO $G3 acknowledge "${AL[@]}"
G4=$(NEW s4); CLOCK 600;  DO $G4 acknowledge "${BO[@]}"
DO $G1 resolve "${BO[@]}"; DO $G2 resolve "${BO[@]}"; DO $G4 resolve "${SAT[@]}"
curl -s -X DELETE $FAM/groups/s3/alerts/a > /dev/null; NOTIFY s3 '{"reason":"resolved"}'
S="label=cluster%3D~%22s%5B1-4%5D%22&from=$FROM"
curl -s "${A[@]}" "$API/alert-group-actor-statistics?$S" | jq -c '[.items[] | {n: .actor.name, k: .actor.kind,
  a: .acknowledged_first_count, r: .resolved_count, m: .time_to_acknowledge.median_seconds, p: .time_to_acknowledge.p95_seconds}]'
# [{"n":"alice","k":"user","a":3,"r":0,"m":240,"p":1644},{"n":"bob","k":"user","a":1,"r":2,"m":600,"p":600},
#  {"n":"terraform","k":"service_account","a":0,"r":1,"m":null,"p":null}]
curl -s "${V[@]}" "$API/alert-group-actor-statistics?$S" | jq -c '[.items[].acknowledged_first_count]'   # [3,1,0]

# C-09.AC-30: the row "Unknown" adds up with By Route
psql "$MUSTER_DATABASE_URL" -c "UPDATE alert_groups SET first_acknowledged_by_user_id = NULL WHERE public_id = '$G4'"
PEOPLE "$S" "${A[@]}"
# [{"n":"alice","a":3,"r":0},{"n":"bob","a":0,"r":2},{"n":"terraform","a":0,"r":1},{"n":null,"a":1,"r":0}]
curl -s "${A[@]}" "$API/alert-group-statistics?group_by=route&$S" | jq '[.items[].time_to_acknowledge.count] | add'   # 4
PEOPLE "$S&first_acknowledged_by=me" "${AL[@]}"                                               # [{"n":"alice","a":3,"r":0},{"n":"bob","a":0,"r":2}]

# C-09.AC-31: By Team, with an Alert Group of both Teams, one of payments and one without a Team
G5=$(NEW t1 warning '"team":"payments"'); PUTA t1 b '{"labels":{"cluster":"t1","severity":"warning","team":"search"}}'; NOTIFY t1 '{"reason":"new alerts"}'
G6=$(NEW t2 warning '"team":"payments"'); G7=$(NEW t3); DO $G5 acknowledge "${AL[@]}"; DO $G6 acknowledge "${BO[@]}"
T="label=cluster%3D~%22t%5B1-3%5D%22&from=$FROM"
curl -s "${A[@]}" "$API/alert-group-statistics?group_by=team&$T" | jq -c '[.items[] | {n: .subject.name, c: .alert_group_count}]'
# [{"n":"payments","c":2},{"n":"search","c":1}]
PEOPLE "$T&team=$TS" "${A[@]}"                                                                # [{"n":"alice","a":1,"r":0}]
PEOPLE "$T&team=$TP" "${A[@]}"                                                                # [{"n":"alice","a":1,"r":0},{"n":"bob","a":1,"r":0}]

# C-09.AC-32: the filters reach every view, a Matcher =~ included; all four are Alert Groups of payments
G8=$(NEW f1 critical '"namespace":"payments","team":"payments"'); G9=$(NEW f2 critical '"namespace":"payments","team":"payments"')
NEW f3 warning '"namespace":"payments","team":"payments"' > /dev/null; NEW f4 critical '"namespace":"search","team":"payments"' > /dev/null
DO $G8 acknowledge "${AL[@]}"; DO $G9 acknowledge "${BO[@]}"
F="severity=critical&label=namespace%3D%22payments%22&label=cluster%3D~%22f.%22&from=$FROM"
for b in route integration team; do curl -s "${A[@]}" "$API/alert-group-statistics?group_by=$b&$F" | jq '[.items[].alert_group_count] | add'; done   # 2, 2, 2
PEOPLE "$F" "${A[@]}"                                                                         # [{"n":"alice","a":1,"r":0},{"n":"bob","a":1,"r":0}]
for b in route integration team; do curl -s "${AL[@]}" "$API/alert-group-statistics?group_by=$b&$F&first_acknowledged_by=me" | jq '[.items[].alert_group_count] | add'; done   # 1, 1, 1
PEOPLE "$F&first_acknowledged_by=me" "${AL[@]}"                                               # [{"n":"alice","a":1,"r":0}]
curl -s "${A[@]}" "$API/alert-group-statistics?group_by=route&first_acknowledged_by=SR0000000000ZZ" | jq -c '{status, c: .errors[0].code}'   # {"status":422,"c":"unknown_id"}
curl -s "${SAT[@]}" "$API/alert-group-actor-statistics?team=mine" | jq -c '{status, c: .errors[0].code}'                                  # {"status":422,"c":"unsupported"}
curl -s "${SAT[@]}" "$API/alert-group-actor-statistics?first_acknowledged_by=me" | jq -c '{status, c: .errors[0].code}'                    # {"status":422,"c":"unsupported"}
SA=$(curl -s "${A[@]}" "$API/service-accounts" | jq -r '.items[] | select(.name == "terraform") | .id')
curl -s "${A[@]}" "$API/alert-group-actor-statistics?first_acknowledged_by=$SA" | jq -c '{status, c: .errors[0].code}'                    # {"status":422,"c":"unknown_id"}
curl -s -o /dev/null -w '%{http_code}\n' "$API/alert-group-actor-statistics"                                                             # 401
```

Medians and percentiles may be off by a second for the real time between the calls. The `psql` update stands in, for the
live check only, for an Alert Group acknowledged before the upgrade whose Timeline was already removed;
`migrate_test.go` builds that case for real. `test/e2e/statistics_test.go` repeats the other checks with the development
clock, and the period of C-09.AC-29: an Alert Group started on the last day of a 7-day period and acknowledged a day
after it counts in it, one started the day before it does not.

## Open questions

None.

## Notes

- Suggested commit: `feat(statistics): record who acknowledged first and add statistics by team and by people`.
- The migration's number is not fixed here: the orchestrator gives the next free one when the story starts (stories
  README §4), and `migrate_test.go`, `schema.md` and `versions.md` use it.
- "First acknowledged by" is a User only, as the maintainer decided when approving the plan: only Users acknowledge
  (C-04.FR-2, C-10.FR-1). Service accounts are rows of By people only through what they resolved.
- The statistics page already depends on the `group_by` enum (`StatisticsSubject`); widening it in the spec would fail
  the TypeScript check of this pull request, so the story widens the type and nothing more.
- Choosing a Route or an Integration that the period has no Alert Groups for keeps it as an item with zeros, as today.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-09.FR-15 | partial | revised for L2: the filters and `group_by=team` in the API; the page is S-175 |
| C-09.FR-27 | full | |
| C-09.FR-28 | partial | the API; the view is S-175 |
| C-09.FR-29 | partial | the API; the view and the link to By people are S-175 |
| C-09.FR-30 | partial | the API; the filters on the page and the URL are S-175 |
| C-09.AC-27 | partial | the row and the API; the page is S-175 |
| C-09.AC-28 | partial | the API; the sorting on the page is S-175 |
| C-09.AC-29 | full | |
| C-09.AC-30 | full | |
| C-09.AC-31 | partial | the API; the name that opens By people is S-175 |
| C-09.AC-32 | partial | the API; the URL in another browser is S-175 |
| C-09.AC-33 | partial | the API; the page is S-175 |
