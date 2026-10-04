---
id: S-029
title: Alert Group list, search, counts, related Alert Groups, statistics, retention and live hints (BE)
capability: C-09
kind: be
layer: L1
depends_on: [S-028]
covers: [C-09.FR-13, C-09.FR-23, C-09.FR-20, C-09.FR-15, C-09.FR-16, C-09.FR-21, C-09.FR-25, C-09.AC-5, C-09.AC-13, C-09.AC-15, C-09.AC-16, C-09.AC-17, C-09.AC-18, C-09.AC-20, C-09.AC-23, C-09.AC-24, C-09.AC-25]
files_touched:
  - internal/groups/list.go
  - internal/groups/filters.go
  - internal/groups/counts.go
  - internal/groups/related.go
  - internal/groups/statistics.go
  - internal/groups/retention.go
  - internal/groups/hints.go
  - internal/groups/read.go
  - internal/groups/query.sql
  - internal/groups/list_test.go
  - internal/groups/statistics_test.go
  - internal/groups/retention_test.go
  - internal/api/alertgroups.go
  - internal/api/alertgroups_test.go
  - internal/api/integrations.go
  - internal/live/hub.go
  - internal/leader/tasks.go
  - internal/logging/events.go
  - test/e2e/alert_group_list_test.go
acceptance:
  - "[C-09.FR-13, C-09.AC-5] `listAlertGroups` without a status returns open Alert Groups, and without a time range those whose lifetime overlaps the last `alert_group.list_range`, including an open Alert Group that started earlier; sorting by start or last change in either direction pages by cursor."
  - "[C-09.FR-13, C-09.AC-15, C-09.AC-25] Filtering by `label=namespace=\"payments\"` and `urgent=true` over the last 30 days returns exactly the matching Alert Groups: Label Matchers apply to the common labels, so an Alert Group whose Alerts all carry the value matches and one in which only some do does not."
  - "[C-09.FR-13, C-09.FR-23, C-09.AC-20] Searching `postgres` finds the Alert Group of `KubePodCrashLooping` Alerts whose first `summary` is \"Pod postgres-0 is crash looping\", case-insensitively and inside words; `q=#N` and `number` find one Alert Group whatever the time range."
  - "[C-09.AC-23] Searching by the number of an Alert Group resolved 60 days ago finds it with the default range; `getAlertGroupStatistics?group_by=route` returns one item per Route."
  - "[C-09.FR-13] `getAlertGroupCounts` returns the counts per status tab for the same filters; `label_columns` returns the shared values in `label_values`; the filters `owner`, `snoozed_no_end`, `delivery_problem` and `unclaimed` answer 422 `unsupported` until their stories."
  - "[C-09.FR-20, C-09.AC-16] `listRelatedAlertGroups` lists the earlier Alert Groups of the same Route with the same Group key values with number, status, start, duration and who resolved them."
  - "[C-09.FR-15, C-09.AC-17] For a Route whose three Alert Groups in the period resolved 10, 20 and 30 minutes after they started, `getAlertGroupStatistics` returns 3 Alert Groups with a median time to resolve of 1200 seconds, split by the days they started in the requested time zone; with `group_by=integration` the same Alert Groups are counted under their Integration."
  - "[C-09.FR-16, C-09.AC-18, C-09.AC-25] Once `retention.alert_details` has passed for a resolved Alert Group, the Leader removes its Alerts, its Timeline returns only its Notes, `getAlertGroup` carries `details_removed` and the notice with the period, and search by title and the label filter still find it; summary rows go after `retention.alert_group_summaries`."
  - "[C-09.FR-25, C-09.AC-13] `live-updates` sends an `alert-groups` hint within seconds of a new Alert Group or a Reopen, and an `alert-group` hint with its id after every change of an Alert Group."
  - "[C-09.FR-25, C-09.AC-24] With a session, `live-updates` starts with `retry: 3000`; signing out closes the stream, and the next attempt gets 401."
  - "[C-09.FR-21] Every Integration carries `open_alert_group_count`, the open Alert Groups with an Alert from it."
verify: "make ci test-integration e2e"
operator_attention: false
issue: null
---

# S-029. Alert Group list, search, counts, related Alert Groups, statistics, retention and live hints (BE)

## Scope

**IN**

- The Alert Group list with its filters, time range, search, sorting, label columns and cursor; the counts per status.
- Related Alert Groups, statistics per Route or Integration.
- Retention of Alert Group details and summary rows, and reads of an Alert Group whose details are gone.
- The live hints for Alert Groups, and the open Alert Group count of an Integration.

**OUT**

- The pages (S-030, S-031).
- The filters Owner and "snoozed with no end" (S-032), "Delivery problem" (S-061) and Unclaimed (S-049); time to
  acknowledge gets data from S-032.

## Contracts

- **Operations implemented**: `listAlertGroups`, `getAlertGroupCounts`, `listRelatedAlertGroups`,
  `getAlertGroupStatistics`; `getAlertGroup` gains `details_removed`, the notice `details_removed` and `label_values`;
  `Integration.open_alert_group_count`. Schemas: `AlertGroupList`, `AlertGroupCounts`, `RelatedAlertGroup(List)`,
  `AlertGroupStatistics`, `AlertGroupStatisticsItem`, `StatisticsDay`, `DurationStats`, `HintEvent`; parameters
  `AgStatus`, `AgRoute`, `AgIntegration`, `AgSeverity`, `AgUrgent`, `AgResolvedBy`, `AgResolveReason`, `AgReopened`,
  `LabelMatchers`, `From`, `To`, `AgNumber`, `AgQuery`, `AgSort`, `AgLabelColumns`.
- **List** (C-09.FR-13; `alert_groups` and its indexes): status defaults to firing, acknowledged and snoozed. The time
  range defaults to the last `alert_group.list_range` and selects lifetimes that overlap it (`created_at < to AND
  (resolved_at IS NULL OR resolved_at >= from)`). Filters: `route` and `integration` (`public_id`s; `integration_ids`
  overlap), `severity`, `urgent`, `resolved_by` (`user` or `system`), `resolve_reason` (with `resolved_by=system`),
  `reopened` (Reopen count above zero), `label` (Matchers of `internal/matchers` applied to `common_labels`: `=` by
  `jsonb` containment, the others row by row after the other conditions; a label the Alerts do not share counts as
  absent). `number` and a `q` of the form `#N` select that Alert Group and ignore the time range; any other `q` searches
  the title and `summary` case-insensitively inside words (trigram `ILIKE`). `sort` by `started_at` or
  `last_changed_at`, either direction, cursor on `(value, id)`, `limit` per `api.page_size`. `label_columns` returns,
  for each item, the value of each named label in `common_labels` (`label_values`). `owner`, `snoozed_no_end`,
  `delivery_problem` and `unclaimed` answer `422` with `unsupported` at `/query/<name>` until S-032, S-061 and S-049.
- **Counts** (`getAlertGroupCounts`): the same filters without `status` and `number`, counted per status and in total.
- **Related** (C-09.FR-20): Alert Groups of the same Route with the same `group_key_sha256`, the Alert Group itself
  excluded, newest first by cursor: number, status, start, `duration_seconds` (to the resolution; null while open) and
  `resolution`.
- **Statistics** (C-09.FR-15): `group_by` `route` or `integration` (required); `route` or `integration` ids narrow the
  matching kind (the other kind answers `422 unsupported`); `from` and `to` default to the last
  `alert_group.list_range`; `time_zone` (IANA, default `organization.time_zone`, otherwise `422 invalid_format`). From
  the summary rows that started in the period: per item — every Route or Integration that is not deleted, and deleted
  ones with Alert Groups in the period — `alert_group_count`, time to resolve (resolved ones, `resolved_at −
  created_at`) and time to acknowledge (`first_acknowledged_at − created_at`, only Alert Groups that were acknowledged)
  as count, median and 95th percentile in seconds, and the same per day of start in the time zone. An Alert Group with
  Alerts from several Integrations counts for each.
- **Retention** (C-09.FR-16; ADR-0006, `design/db/schema.md` §6): an hourly Leader task deletes, in batches of at most
  5,000 rows, `alert_group_alerts` rows that ended more than `retention.alert_details` ago and `alert_groups` resolved
  more than `retention.alert_group_summaries` ago (their Notes, timers and delivery rows go by cascade); Timeline months
  are dropped by S-008. The task runs per Organization: it iterates over the Organizations (one in L1), reads that
  Organization's periods and passes `org_id` to every query (lint 1). Reads hide what is past its period: an Alert Group
  resolved more than `retention.alert_details` ago has `details_removed: true` and the notice `details_removed` with
  `retention_days`, `listAlertGroupAlerts` returns nothing and the Timeline returns only Notes. List, search, counts and
  statistics use the summary rows.
- **Live hints** (C-09.FR-25, ADR-0009): the dispatcher sends, in the transaction of each change, `NOTIFY` for the hint
  `alert-group` with the `public_id`; a new Alert Group and a Reopen also send `alert-groups` with `null`. The hub of
  S-012 gains both types.
- **Integration count** (C-09.FR-21): `open_alert_group_count` on every Integration read — the open Alert Groups whose
  `integration_ids` contain it.
- **Log event**: `alert_groups_purged` (INFO: `details`, `summaries`).

## Steps

1. Write the filters, the time range, search and sorting, and the counts. Check: tests with 200 summary rows cover each
   filter, the overlap rule, `#N` without range, the common-labels rule and the cursor; the plans use the indexes listed
   in `design/db/schema.md` §4.9.
2. Write related Alert Groups and statistics. Check: tests cover C-09.AC-17 with a manual clock, days in a time zone and
   an Alert Group of two Integrations.
3. Write retention and the reads of removed details. Check: tests with a manual clock cover C-09.AC-18 and Notes
   surviving the details.
4. Add the hints and the Integration count. Check: Verification below.

## Verification

```sh
make dev &
# as in S-023: a Personal access token in A=(-H "Authorization: Bearer $PAT" -H 'Content-Type: application/json'),
# because the clock jumps by days below; an Integration "lst" (id INT) with its fake receiver "lst"; ADV and NOTIFY;
# the On-call policy in P
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake
LIST() { curl -s "${A[@]}" "$API/alert-groups?$1"; }
ROUTE() { curl -s "${A[@]}" $API/routes -d "{\"name\":\"$1\",\"matchers\":[{\"label\":\"team\",\"op\":\"=\",\"value\":\"$1\"}],
  \"urgent\":false,\"group_key\":$2,\"destination_ids\":[],\"policy\":$P}" | jq -r .id; }
PUTA() { curl -s -X PUT "$FAM/groups/$1/alerts/$2" -d "$3" > /dev/null; }
RK=$(ROUTE k8s '["alertname","namespace"]'); RM=$(ROUTE mix '["alertname"]'); RS=$(ROUTE st '["alertname","n"]')
curl -sN "${A[@]}" $API/live-updates > /tmp/sse & SSE=$!

# two Alert Groups: "pay" (both Alerts in payments, critical, Urgent) and "mix" (one in payments, one in billing)
curl -s -X PUT $FAM/groups/c1 -d '{"receiver":"lst","route":"{}","labels":{"alertname":"KubePodCrashLooping"}}' > /dev/null
PUTA c1 p0 '{"labels":{"team":"k8s","namespace":"payments","pod":"postgres-0","severity":"critical"},"annotations":{"summary":"Pod postgres-0 is crash looping"}}'
PUTA c1 p1 '{"labels":{"team":"k8s","namespace":"payments","pod":"postgres-1","severity":"critical"}}'
PUTA c1 m0 '{"labels":{"team":"mix","namespace":"payments","pod":"a"}}'
PUTA c1 m1 '{"labels":{"team":"mix","namespace":"billing","pod":"b"}}'
NOTIFY c1 '{"reason":"first notification"}'
grep -c '"type":"alert-groups"' /tmp/sse                                                          # 1 or more   (C-09.AC-13)

# C-09.AC-15 and C-09.AC-25: common labels decide
FROM=$(date -u -v-30d +%FT%TZ 2>/dev/null || date -u -d '-30 days' +%FT%TZ)
LIST "label=namespace%3D%22payments%22&urgent=true&from=$FROM" | jq -c '[.items[] | .route.name]'  # ["k8s"]
LIST 'label=namespace%3D%22payments%22' | jq -c '[.items[] | .route.name]'                         # ["k8s"]
LIST 'label_columns=namespace&route='"$RM" | jq -c '.items[0].label_values'                         # {}

# C-09.AC-20: search inside words; #N ignores the range
PAY=$(LIST 'q=postgres' | jq -r '.items[0].id'); NPAY=$(LIST 'q=postgres' | jq -r '.items[0].number')
LIST 'q=POSTGRES' | jq '.items | length'                                                           # 1
curl -s "${A[@]}" "$API/alert-group-counts?label=namespace%3D%22payments%22" | jq -c .
# {"firing":1,"acknowledged":0,"snoozed":0,"resolved":0,"all":1}
LIST 'owner=me' | jq -r '.errors[0].code'                                                          # unsupported
curl -s "${A[@]}" "$API/integrations/$INT" | jq .open_alert_group_count                            # 2

# C-09.AC-17: three Alert Groups resolved 10, 20 and 30 minutes after they started
curl -s -X PUT $FAM/groups/s1 -d '{"receiver":"lst","route":"{}","labels":{"alertname":"Stat"}}' > /dev/null
for n in 1 2 3; do PUTA s1 x$n "{\"labels\":{\"team\":\"st\",\"n\":\"$n\"}}"; done
NOTIFY s1 '{"reason":"first notification"}'
for n in 1 2 3; do ADV 600; PUTA s1 x$n "{\"labels\":{\"team\":\"st\",\"n\":\"$n\"},\"status\":\"resolved\"}"; NOTIFY s1 '{"reason":"some alerts resolved"}'; done
curl -s "${A[@]}" "$API/alert-group-statistics?group_by=route&route=$RS&time_zone=Europe/Berlin" \
  | jq -c '.items[0] | {n: .alert_group_count, med: .time_to_resolve.median_seconds, days: [.per_day[].alert_group_count]}'
# {"n":3,"med":1200,"days":[3]}
curl -s "${A[@]}" "$API/alert-group-statistics?group_by=integration&integration=$INT" | jq '.items[0].alert_group_count'   # 5

# C-09.AC-16: the earlier Alert Group with the same key
PUTA c1 p0 '{"labels":{"team":"k8s","namespace":"payments","pod":"postgres-0","severity":"critical"},"status":"resolved"}'
PUTA c1 p1 '{"labels":{"team":"k8s","namespace":"payments","pod":"postgres-1","severity":"critical"},"status":"resolved"}'
NOTIFY c1 '{"reason":"some alerts resolved"}'; ADV 1200
PUTA c1 p2 '{"labels":{"team":"k8s","namespace":"payments","pod":"postgres-2","severity":"critical"}}'; NOTIFY c1 '{"reason":"new alerts added"}'
NEW=$(LIST "route=$RK" | jq -r '.items[0].id')
curl -s "${A[@]}" "$API/alert-groups/$NEW/related" | jq -c "[.items[] | {number, status, by: .resolution.by}]"
# [{"number":NPAY,"status":"resolved","by":"system"}]

# C-09.AC-5: an open Alert Group that started 10 days ago is in the default view
ADV 864000; LIST '' | jq --arg i "$NEW" '[.items[].id] | index($i) != null'                         # true

# C-09.AC-23: 60 days after its resolution, the number still finds it
ADV $((50 * 86400)); LIST "number=$NPAY" | jq -r '.items[0].id' | grep -c "$PAY"                    # 1
[ "$(curl -s "${A[@]}" "$API/alert-group-statistics?group_by=route" | jq '.items | length')" = \
  "$(curl -s "${A[@]}" $API/routes | jq '.items | length')" ] && echo "one item per Route"              # one item per Route

# C-09.AC-18 and C-09.AC-25: details removed after 90 days, the summary and the filters stay
ADV $((31 * 86400))
curl -s "${A[@]}" "$API/alert-groups/$PAY" | jq -c '{details_removed, n: [.notices[] | select(.kind == "details_removed") | .retention_days]}'
# {"details_removed":true,"n":[90]}
curl -s "${A[@]}" "$API/alert-groups/$PAY/alerts" | jq '.items | length'                            # 0
LIST "q=KubePodCrashLooping&status=resolved&from=$(date -u -v-200d +%FT%TZ 2>/dev/null || date -u -d '-200 days' +%FT%TZ)&to=2100-01-01T00:00:00Z" \
  | jq --arg i "$PAY" '[.items[].id] | index($i) != null'                                           # true
kill $SSE

# C-09.AC-24, with a new session of the Admin (the jumps above ended the old one)
curl -s -c jar -H 'Content-Type: application/json' -d '{"login":"admin@example.org","password":"muster-dev-password"}' \
  $API/sessions > /dev/null
CSRF=$(curl -s -b jar $API/sessions/current | jq -r .csrf_token); H=(-b jar -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json')
curl -sN -b jar $API/live-updates > /tmp/sse2 & S2=$!
sleep 1; head -1 /tmp/sse2                                                                        # retry: 3000
curl -s -o /dev/null "${H[@]}" -X DELETE $API/sessions/current; sleep 1
kill -0 $S2 2>/dev/null || echo closed                                                            # closed
curl -s -o /dev/null -w '%{http_code}\n' -b jar $API/live-updates                                  # 401
```

## Open questions

None.

## Notes

- Suggested commit: `feat(groups): add the alert group list, search, counts, statistics, retention and live hints`.
- The `from`/`to` of the last check use the development clock's time; the pull request records the values used.
- The plans of the list queries are checked against 200,000 summary rows in an integration test, like the spike of
  `design/db/schema.md` §8.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-09.FR-13 | partial | the API; the page is S-030, the later filters S-032, S-061, S-049 |
| C-09.FR-23 | full | together with S-028 |
| C-09.FR-20 | partial | the API; the page is S-030 |
| C-09.FR-15 | partial | the API; the page is S-031, time to acknowledge gets data from S-032 |
| C-09.FR-16 | partial | retention and the reads; the page notice is S-030, Notes S-032 |
| C-09.FR-21 | partial | the count; the dialog is S-031 |
| C-09.FR-25 | partial | the Alert Group hints; the client is S-030 |
| C-09.AC-5 | full | |
| C-09.AC-13 | partial | the hint; "1 new" in the list is S-030 |
| C-09.AC-15 | partial | the API; the URL reproducing the view in another browser is S-030 |
| C-09.AC-16 | partial | the API; the page is S-030 |
| C-09.AC-17 | partial | the API; the statistics page is S-031 |
| C-09.AC-18 | partial | the API; the page notice is S-030 |
| C-09.AC-20 | full | together with S-028 |
| C-09.AC-23 | full | |
| C-09.AC-24 | full | re-checked with the hints of this story; the stream is S-012's |
| C-09.AC-25 | full | |
