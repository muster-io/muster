---
id: S-206
title: The attached filter and counts of the Alert Group list, bulk Attach, the Alerts of attached Alert Groups, statistics and webhook events (BE)
capability: C-37
kind: be
layer: L2
depends_on: [S-203, S-174, S-168, S-044]
covers: [C-37.FR-9, C-37.FR-12, C-37.FR-13, C-37.FR-14, C-37.FR-15, C-37.AC-4, C-37.AC-9, C-37.AC-10, C-37.AC-11]
files_touched:
  - api/openapi.yaml
  - internal/groups/bulk.go
  - internal/groups/bulk_test.go
  - internal/groups/list.go
  - internal/groups/list_test.go
  - internal/groups/filters.go
  - internal/groups/read.go
  - internal/groups/statistics.go
  - internal/groups/statistics_test.go
  - internal/groups/statistics_integration_test.go
  - internal/groups/query.sql
  - internal/groups/groups_integration_test.go
  - internal/groups/list_integration_test.go
  - internal/api/alertgroups.go
  - internal/api/alertgroups_test.go
  - internal/api/commands.go
  - internal/api/commands_test.go
  - internal/webhooks/body.go
  - internal/webhooks/body_test.go
  - internal/delivery/webhookevents.go
  - internal/delivery/webhookevents_test.go
  - internal/delivery/webhookevents_integration_test.go
  - web/src/test/fixtures/alert-groups.ts
  - test/e2e/attach_api_test.go
  - test/e2e/alert_group_list_test.go
  - test/e2e/statistics_test.go
  - test/e2e/webhook_events_test.go
acceptance:
  - "[C-37.FR-13, C-37.AC-11] `runBulkCommand` takes `attach` with `main_alert_group_id` as its argument and the Permission `alert-groups:attach`, checked once: a bulk Attach of three Alert Groups to `#1287`, one of them resolved, attaches two and refuses one with `already_resolved`, each item through the dispatcher with its own Audit log entry and outcome; with `alert_group.attach_max` reached the next item gets `attach_limit_reached` (`bulk_test.go`, `commands_test.go`, `test/e2e/attach_api_test.go`)."
  - "[C-37.FR-9, C-37.AC-4] A bulk Acknowledge that includes an attached Alert Group refuses that item with the outcome `refused` and the code `alert_group_attached` and acknowledges the others (`bulk_test.go`, `test/e2e/attach_api_test.go`)."
  - "[C-37.FR-13] `listAlertGroups` takes `attached` (`any`, the default, `only`, `none`) and `attached_to={id}`, kept in the cursor of the list and combined with every other filter by AND; each item carries `attached_to` and `attached_count`, so a main Alert Group shows how many are attached to it, and `attached_to={id}` lists the Alert Groups of a main one with their Route, status and firing and total Alerts (`list_test.go`, `list_integration_test.go`, `alertgroups_test.go`, `test/e2e/alert_group_list_test.go`)."
  - "[C-37.FR-12] `listAlertGroupAlerts` of a main Alert Group lists its own Alerts and then those of each attached Alert Group, each with the `alert_group` it belongs to, unless `include_attached` is false; the Severity level, Urgent flag, Teams and Destinations of the main Alert Group do not count them (`alertgroups_test.go`, `groups_integration_test.go`)."
  - "[C-37.FR-14, C-37.AC-10] On a statistics period with `#1287` and `#1288` resolved together by hand while attached, By Route counts one Alert Group and By people credits Ivan once, and the summary keeps `resolved_with` on `#1288`; after a Detach and a later resolve of its own, `#1288` counts on its own, and one resolved by the system while attached counts as any other (`statistics_test.go`, `statistics_integration_test.go`, `test/e2e/statistics_test.go`)."
  - "[C-37.FR-15, C-37.AC-9] A table-driven test over every row of FR-15 checks that it records exactly one Timeline entry with its `event`, kind, `loudness` and `mentions`, and that an outgoing webhook Destination in events mode receives each one to one with `alert_group.attached_to` set to the `public_id` and number of the main Alert Group, and null on every other event; a Destination that chose events by name can choose the five (`body_test.go`, `webhookevents_test.go`, `webhookevents_integration_test.go`, `test/e2e/webhook_events_test.go`)."
verify: "make ci test-integration e2e-go"
operator_attention: false
issue: null
---

# S-206. The attached filter and counts of the Alert Group list, bulk Attach, the Alerts of attached Alert Groups, statistics and webhook events (BE)

## Scope

**IN**

- The filter `attached`, the filter `attached_to`, the count of attached Alert Groups on every item, and the bulk Attach
  with its per-item outcomes and bulk refusal of an attached Alert Group (C-37.FR-13, FR-9).
- The Alerts of the attached Alert Groups in the Alerts view of a main one (C-37.FR-12, the API half).
- The statistics that leave out an Alert Group resolved with its main one (C-37.FR-14).
- `attached_to` in the `alert_group` of every outgoing webhook event, and the five events of FR-15 in events mode
  (C-37.FR-15).

**OUT**

- The Commands, the state and the events: S-202, S-203. Chains and timers: S-204. Messengers: S-205.
- The page, the dialog, the list mark and the bulk bar: S-207. The documentation of statistics and of webhook events:
  S-208.

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated): `listAlertGroups` gains `attached` (`any`, `only`,
  `none`) and `attached_to` (a `PublicId`); `BulkCommandRequest` gains `main_alert_group_id`, required with `attach` and
  refused with `422` for any other command, and its `command` enum `attach`; `x-permission` of `runBulkCommand` names
  `alert-groups:attach` for it; `listAlertGroupAlerts` gains `include_attached` (default true) and its items the
  `alert_group` (an `AlertGroupRef`, null for an Alert of the Alert Group itself); `HintEvent` `alert-groups` already
  covers a list change. The webhook event schema's `alert_group` gains `attached_to` (`id`, `number`, or null).
- **Filters and list** (`internal/groups/filters.go`, `list.go`, `query.sql`; ADR-0016 flat CRUD, `org_id` in every
  query): `attached=only` is `attached_to_id IS NOT NULL`, `none` its negation, and `attached_to` an equality on the main
  Alert Group's id looked up by `public_id`; the count is a subquery over `(org_id, attached_to_id)` — S-202's index —
  and the cursor of the list carries the filter as the other filters, no `OFFSET`.
- **Bulk** (`internal/groups/bulk.go`): `BulkRequest` gains `Main`; the case `attach` runs `Service.Attach` per item in the
  order of the request with its own transaction, Audit log and Timeline entries, and a refusal of one never stops the
  others (C-10.FR-14); outcomes `done`, `refused` (with the code of C-37.FR-2) and `failed`; the Permission is checked
  once before the arguments and any Alert Group; `alert_group.bulk_max` applies. Items that name an attached Alert Group
  in Acknowledge, Resolve, Snooze or Unsnooze get `refused` with `alert_group_attached` from S-203's precondition; bulk
  Unacknowledge and Detach have no bulk form, as before.
- **Alerts view** (`internal/groups/read.go`): own Alerts first, then those of each attached Alert Group in the order of
  their numbers, each item naming its Alert Group; a main Alert Group's own aggregates (Severity level, Urgent, Teams,
  Destinations) read only its own Alerts and are untouched.
- **Statistics** (`internal/groups/statistics.go`, `query.sql`): the resolved counts and durations of every view leave out
  Alert Groups with `resolved_with_id` set; one that was detached, or resolved on its own while attached, counts as any
  other. By people credits an acknowledgement through `first_acknowledged_at` as C-09.FR-27 says; an Alert Group resolved
  with its main one is no longer counted, so the acknowledgement that following set on it is not credited a second
  time, while that of a detached one is (see the Open question). The same row filter applies to
  `alert-group-actor-statistics` and the By Team view.
- **Webhooks** (`internal/webhooks/body.go`, `internal/delivery/webhookevents.go`): the body of every event carries
  `alert_group.attached_to`; `attached`, `alert_group_attached`, `status_followed`, `detached` and
  `alert_group_detached` are delivered one to one in events mode, loud `notify` false, to every Destination of the
  Alert Group's Route as the other events are; the selectable event names of a Destination include them (S-202 added the
  CHECK).
- **Web fixture** (`web/src/test/fixtures/alert-groups.ts`): the list builder and the Alert item carry the new fields,
  so that the TypeScript check stays green; S-207 shows them.
- **Settings**: `alert_group.attach_max`, `alert_group.bulk_max`.

## Steps

1. Add the filters, the count and the Alerts view. Check: `list_test.go`, `list_integration_test.go`,
   `alertgroups_test.go`, `groups_integration_test.go`.
2. Add the bulk Attach and its outcomes. Check: `bulk_test.go`, `commands_test.go`.
3. Leave resolved-with Alert Groups out of the statistics. Check: `statistics_test.go`,
   `statistics_integration_test.go`.
4. Add `attached_to` to the webhook bodies and the five events. Check: `body_test.go`, `webhookevents_test.go`,
   `webhookevents_integration_test.go`.
5. Add the spec and write the e2e tests. Check: `make generate-check`; the Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
API=localhost:8080/api/v1   # A: Admin; I: Ivan; AG1287 acknowledged by Ivan, AG1288, AG1289 (resolved), AG1290 firing
curl -s -H "$I" -X POST $API/alert-groups/bulk-commands -d "{\"command\":\"attach\",\"main_alert_group_id\":\"$AG1287\",\"alert_group_ids\":[\"$AG1288\",\"$AG1289\",\"$AG1290\"]}" | jq -c '[.items[] | {n: .number, o: .outcome, c: .code}]'
# [{"n":1288,"o":"done","c":null},{"n":1289,"o":"refused","c":"already_resolved"},{"n":1290,"o":"done","c":null}]
curl -s -H "$A" "$API/alert-groups?attached=only" | jq -c '[.items[].number]'                 # [1288,1290]
curl -s -H "$A" "$API/alert-groups?attached=none" | jq -c '[.items[].number] | index(1288)'  # null
curl -s -H "$A" "$API/alert-groups?attached_to=$AG1287" | jq '.items | length'                # 2
curl -s -H "$A" "$API/alert-groups?number=1287" | jq -c '.items[0].attached_count'            # 2
curl -s -H "$A" "$API/alert-groups/$AG1287/alerts" | jq -c '[.items[] | .alert_group.number]' # own Alerts null first, then 1288, 1290
curl -s -H "$I" -X POST $API/alert-groups/bulk-commands -d "{\"command\":\"acknowledge\",\"alert_group_ids\":[\"$AG1288\",\"$AG1291\"]}" | jq -c '[.items[] | {n: .number, o: .outcome, c: .code}]'
# [{"n":1288,"o":"refused","c":"alert_group_attached"},{"n":1291,"o":"done","c":null}]
# statistics: #1287 and #1288 resolved together by hand
curl -s -H "$I" -X POST $API/alert-groups/$AG1287/resolve -d '{}' > /dev/null
curl -s -H "$A" "$API/alert-group-statistics?group_by=route&from=...&to=..." | jq -c '[.items[] | .resolved_count] | add'   # 1 for #1287 and #1288 together
curl -s -H "$A" "$API/alert-group-actor-statistics?from=...&to=..." | jq -c '[.items[] | select(.user.login=="ivan") | .acknowledged_first_count]'   # [1]
# the outgoing webhook receives the events: a fake webhook Destination in events mode on the Route
curl -s 127.0.0.1:19095/_fake/requests | jq -c '[.[].body | fromjson | select(.event=="attached") | {a: .alert_group.number, to: .alert_group.attached_to.number}]'   # [{"a":1288,"to":1287}]
curl -s 127.0.0.1:19095/_fake/requests | jq -c '[.[].body | fromjson | select(.event=="created") | .alert_group.attached_to] | unique'   # [null]
```

`test/e2e/attach_api_test.go` repeats these checks and adds the bulk Attach over `alert_group.attach_max`, the Alert Groups
filter with every other filter at once, and the table of FR-15's rows with the outgoing webhook; the three existing e2e
files are extended for the list, the statistics and the events.

## Open questions

1. **One acknowledgement credited once.** C-37.FR-14 says that one acknowledgement is credited once (C-09.FR-27, FR-28),
   and that an Alert Group that was detached counts as any other. An Alert Group attached to an acknowledged main one
   gets a first acknowledgement by following (C-37.FR-5), then is detached and resolved on its own: it counts on its own
   and, by `first_acknowledged_at`, credits the Owner of its main Alert Group a second time for the one acknowledgement.
   The contract follows the text literally and does not suppress it. The maintainer decides whether a first
   acknowledgement set by following should be marked, and left out of By people, until the Alert Group is acknowledged on
   its own.
2. **`include_attached` default.** The PRD says the Alerts section lists the attached Alerts after its own, so the default
   is true; a client of 0.2.0 reading an Alert Group that has attached ones then sees extra items. No Alert Group has any
   before 0.2.x, so this is safe on upgrade; the maintainer confirms the name and the default.

## Notes

- Suggested commit: `feat(groups): filter the alert group list by attachment, attach in bulk and leave resolved-with alert groups out of the statistics`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases. No migration:
  `attached_to_id` and `resolved_with_id` are S-202's.
- `alert_groups.attached_to_id` is indexed by S-202 with `org_id` first, so lint 1 holds for every new query.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-37.FR-9 | full | the Commands' refusal from any Transport is S-203's; the bulk item here |
| C-37.FR-12 | partial | the API for the page's section and Alerts; the page is S-207 |
| C-37.FR-13 | partial | the filter, the count and the bulk Attach in the API; the mark, the filter control and the bar are S-207 |
| C-37.FR-14 | full | `resolved_with` on the read is S-203's |
| C-37.FR-15 | full | the Timeline rows are S-202, S-203, the messenger column S-205 |
| C-37.AC-4 | full | the refusals of FR-2 and `403` S-202, `alert_group_attached` S-203 |
| C-37.AC-9 | full | |
| C-37.AC-10 | full | |
| C-37.AC-11 | full | the limit is S-202's |
