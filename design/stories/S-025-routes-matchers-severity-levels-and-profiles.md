---
id: S-025
title: Routes, Matchers, evaluation order, Severity levels and Route profiles (BE)
capability: C-08
kind: be
layer: L1
depends_on: [S-023]
covers: [C-08.FR-1, C-08.FR-2, C-08.FR-3, C-08.FR-4, C-08.FR-6, C-08.FR-7, C-08.FR-8, C-08.FR-9, C-08.FR-10, C-08.FR-12, C-08.FR-13, C-08.AC-1, C-08.AC-6, C-08.AC-7, C-08.AC-9, C-08.AC-10, C-08.AC-11, C-06.FR-19]
files_touched:
  - internal/routing/routes.go
  - internal/routing/evaluate.go
  - internal/routing/severity.go
  - internal/routing/profiles.go
  - internal/routing/query.sql
  - internal/routing/routes_test.go
  - internal/routing/evaluate_test.go
  - internal/routing/severity_test.go
  - internal/matchers/matchers.go
  - internal/api/routes.go
  - internal/api/routes_test.go
  - internal/ingest/changes.go
  - internal/ingest/alertsview.go
  - internal/ingest/query.sql
  - internal/runtime/bootstrap.go
  - internal/runtime/runtime.go
  - internal/metrics/catalogue.go
  - internal/logging/events.go
  - test/e2e/routing_test.go
acceptance:
  - "[C-08.FR-1, C-08.FR-4] `createRoute` stores the name (unique among Routes that are not deleted, otherwise 409 `name_taken`), description, Matchers, the urgent mark, the Group key and every policy field, and inserts the Route before the Default route; a template other than null answers 422 `unsupported` until S-036, and a Destination id answers 422 while no Destination exists."
  - "[C-08.FR-2, C-08.AC-10] A Matcher with an invalid RE2 expression answers 422 with the pointer `/matchers/<n>/value` and the code `invalid_regex`; `=~` and `!~` are anchored as in Alertmanager, and a missing label counts as an empty value."
  - "[C-08.FR-3, C-08.FR-13, C-08.AC-1, C-06.FR-19] With Routes A (`severity=\"critical\"`) and B (`team=\"x\"`), a newly firing Alert with both labels is taken by whichever is first; after a reorder, the next newly firing Alert with both labels is taken by the other; the Alerts view shows the Route of each, and an Alert matching no Route is taken by the Default route."
  - "[C-08.FR-3, C-08.FR-9, C-08.AC-6] The Default route exists from the first start without Matchers and is always last; deleting it or a reorder that lists it answers 409 `default-route-immutable`; a deleted Route leaves the evaluation order at once."
  - "[C-08.FR-10, C-08.AC-7, C-08.AC-11] A Route edit with a stale `If-Match` and a reorder with a stale list ETag answer 412; a reorder whose Routes differ from the current set answers 422 `route_set_mismatch`."
  - "[C-08.FR-6, C-08.AC-9] Each newly firing Alert gets a Severity level by `organization.severity_mapping`: `severity=\"none\"` is info, `severity=\"P5\"` is warning shown as `P5` (`severity_raw`), and an Alert without the label is info."
  - "[C-08.FR-7, C-08.AC-11] `listRouteProfiles` returns On-call and Informational with the values of defaults.md, including Snooze durations of 1 h / 4 h / 24 h and 1 d / 3 d / 7 d and the ack timeout on and off."
  - "[C-08.FR-8] A Route edit applies at once to the next Alerts routed; an Alert already routed keeps its Route."
  - "[C-08.FR-12] `/metrics` exports `muster_route_info{route,name}` for every Route that is not deleted, the Default route included."
verify: "make ci test-integration e2e"
operator_attention: false
issue: null
---

# S-025. Routes, Matchers, evaluation order, Severity levels and Route profiles (BE)

## Scope

**IN**

- Routes with Matchers, the urgent mark, the Group key and every policy field, stored and returned.
- The Default route, the evaluation order, reordering with the list ETag, soft deletion.
- Routing of newly firing Alerts: the first matching Route and the Severity level, shown in the Alerts view.
- The two Route profiles, the Route metric, Audit log entries and live hints.

**OUT**

- The Group key preview and Route suggestions (S-026); the pages (S-027).
- Grouping by the Group key, urgency of Alert Groups, and refusing to delete a Route with open Alert Groups (S-028).
- The behaviour of each policy field, which arrives with its capability: Reopen window, Grace period and the rise to
  Urgent (S-028), Snooze durations (S-032), Thread batching and Storms (S-034, S-035), language and templates (S-036),
  ack timeout, Reminders and auto-unacknowledge (S-049); Destinations (S-039 on).

## Contracts

- **Operations implemented**: `listRoutes`, `createRoute`, `getRoute`, `updateRoute`, `deleteRoute`, `reorderRoutes`,
  `listRouteProfiles`. Schemas: `Route`, `RouteInput`, `RouteList`, `RouteOrder`, `RoutePolicy`, `RouteTemplates`,
  `AckTimeoutPolicy`, `RemindersPolicy`, `RouteProfile(List)`, `Matcher`, `SeverityLevel`; the Alerts view
  (`IntegrationAlert`) gains `route`, `severity_level` and `severity_raw`.
- **Routes** (C-08.FR-1, FR-4; `routes`, `route_matchers`): the name is unique among Routes that are not deleted (`409
  name_taken`); Group key entries are label names, unique (`422 duplicate`); every `RoutePolicy` field is stored in its
  typed column with its `CHECK` and returned as stored, its behaviour arriving with its capability (C-08.FR-1).
  `policy.templates` other than null answers `422` `unsupported` at `/policy/templates/<name>` until S-036 dry-runs
  templates. A Destination id that names no Destination answers `422` `unknown_id` at `/destination_ids/<n>`. Reads
  carry `destinations` `[]`, `storm_active` `false`, `template_error` absent and `open_alert_group_count` `0` until
  their stories. Matchers are replaced as a whole on update.
- **Default route** (C-08.FR-3, FR-9): a start-up ensure step creates "Default", `is_default`, without Matchers, with
  the values of the On-call profile. It is always last, may be edited except for Matchers (`422` `unsupported` at
  `/matchers`), and deleting it answers `409` with the problem type `default-route-immutable`.
- **Order** (C-08.FR-3, FR-10): evaluation is `ORDER BY is_default, position, id`; a new Route takes the last position
  before the Default route. `listRoutes` carries the list ETag from `organizations.route_order_version`, which
  creating, deleting and reordering bump. `reorderRoutes` needs `If-Match` with that ETag (`412` when stale, `428`
  without); its `route_ids` must be exactly the Routes that are not deleted except the Default route (`422`
  `route_set_mismatch`), and a list containing the Default route answers `409` `default-route-immutable`; positions are
  rewritten in one transaction.
- **Edits and deletion** (C-08.FR-8, FR-9, FR-10): `updateRoute` needs `If-Match` from `version` (`412`, `428`) and
  applies at once; `deleteRoute` sets `deleted_at`, so the Route leaves the evaluation order at once; S-028 adds the
  refusal while open Alert Groups remain.
- **Matchers** (C-08.FR-2): the structured `Matcher` of the API, compiled by `internal/matchers` of S-020 when saved:
  `=`, `!=`, `=~`, `!~`, RE2 anchored as in Alertmanager (`^(?:…)$`), combined with AND, a missing label counting as an
  empty value; an expression that does not compile answers `422` with `errors[].code = invalid_regex` at
  `/matchers/<n>/value`.
- **Evaluation** (C-08.FR-3, FR-13): routing attaches to the Alert change sink of S-020. For each `fired` change it
  evaluates the Routes in order on the Alert's labels (Static labels applied) and stores the first match in
  `alerts.route_id` with `severity_level` and `severity_raw`; the Snapshot's `stored_snapshots.route_ids` collects the
  Routes that took its Alerts (C-12 dry runs). Other changes are not routed; from S-028 an Alert that lives in an open
  Alert Group is not routed again. The ordered list with compiled Matchers is cached per replica and reloaded on a
  `route` hint.
- **Severity levels** (C-08.FR-6): the value of `organization.severity_label` is looked up in
  `organization.severity_mapping`; by default `critical` → critical, `warning` → warning, `info` and `none` → info; a
  value without a mapping is warning with the value kept in `severity_raw`; an Alert without the label is info.
- **Route profiles** (C-08.FR-7): `listRouteProfiles` serves `on_call` ("On-call") and `informational`
  ("Informational") built from the "Routing and lifecycle" table of defaults.md, read-only.
- **Audit log actions** (C-03.FR-14): `route.created`, `route.updated`, `route.deleted`, `route.reordered`, with diffs.
- **Live hints**: `route` with the id after a change, `route` with `null` after a reorder.
- **Metric and log fields** (C-08.FR-12): `muster_route_info{route,name}`; `snapshot_processed` gains `routes` (the ids
  of the Routes that took Alerts of the Snapshot).

## Steps

1. Write Routes with validation, the Default route, soft deletion, the Audit log and hints. Check: tests cover
   `name_taken`, the policy `CHECK`s, `unsupported` templates and Default route Matchers, and `If-Match`.
2. Write the order and `reorderRoutes`. Check: tests cover the list ETag, `route_set_mismatch` and
   `default-route-immutable`.
3. Write evaluation and Severity levels on the Alert change sink. Check: table tests cover each operator, anchoring,
   missing labels, first match and each mapping case.
4. Write the profiles, the metric, the Alerts view fields and `muster dev`. Check: Verification below.

## Verification

```sh
make dev &
# as in S-020: the session `H`, an Integration "lab" (id INT) with its fake receiver, and the helpers VIEW and NOTIFY
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake
curl -s -b jar $API/route-profiles | jq -c '.items[] | {id, s: .policy.snooze_durations_seconds, ack: .policy.ack_timeout.enabled, r: .policy.reminders.enabled}'
# {"id":"on_call","s":[3600,14400,86400],"ack":true,"r":true}
# {"id":"informational","s":[86400,259200,604800],"ack":false,"r":false}
P=$(curl -s -b jar $API/route-profiles | jq -c '.items[] | select(.id == "on_call") | .policy')
MK() { curl -s "${H[@]}" $API/routes -d "{\"name\":\"$1\",\"matchers\":$2,\"urgent\":false,\"group_key\":[\"alertname\"],\"destination_ids\":[],\"policy\":$P}"; }
RA=$(MK A '[{"label":"severity","op":"=","value":"critical"}]' | jq -r .id)
RB=$(MK B '[{"label":"team","op":"=","value":"x"}]' | jq -r .id)
curl -s -b jar $API/routes | jq -r '[.items[] | "\(.name):\(.is_default)"] | join(",")'   # A:false,B:false,Default:true

# C-08.AC-1: first match, then reordered
curl -s -X PUT $FAM/groups/r1 -d '{"receiver":"lab","route":"{}","labels":{"alertname":"Both"}}' > /dev/null
curl -s -X PUT $FAM/groups/r1/alerts/one -d '{"labels":{"severity":"critical","team":"x","n":"1"}}' > /dev/null
NOTIFY r1 '{"reason":"first notification"}'
VIEW 'label=alertname%3D%22Both%22&label=n%3D%221%22' | jq -r '.items[0].route.name'                                   # A
ET=$(curl -s -D - -o /dev/null -b jar $API/routes | awk 'tolower($1)=="etag:"{print $2}' | tr -d '\r')
IDS=$(curl -s -b jar $API/routes | jq -c --arg a "$RA" --arg b "$RB" '[.items[] | select(.is_default | not) | .id] | map(if . == $a then $b elif . == $b then $a else . end)')
curl -s "${H[@]}" -X PUT -H "If-Match: $ET" $API/route-order -d "{\"route_ids\":$IDS}" | jq -r '[.items[].name] | join(",")'
# B,A,Default
curl -s -X PUT $FAM/groups/r1/alerts/two -d '{"labels":{"severity":"critical","team":"x","n":"2"}}' > /dev/null
NOTIFY r1 '{"reason":"new alerts added"}'
VIEW 'label=alertname%3D%22Both%22&label=n%3D%222%22' | jq -r '.items[0].route.name'                                   # B
VIEW 'label=alertname%3D%22Both%22&label=n%3D%221%22' | jq -r '.items[0].route.name'                                   # A   (already routed)

# C-08.AC-7, C-08.AC-11, C-08.AC-6
curl -s "${H[@]}" -X PUT -H "If-Match: $ET" $API/route-order -d "{\"route_ids\":$IDS}" | jq -r .status          # 412
DEF=$(curl -s -b jar $API/routes | jq -r '.items[] | select(.is_default) | .id')
ET2=$(curl -s -D - -o /dev/null -b jar $API/routes | awk 'tolower($1)=="etag:"{print $2}' | tr -d '\r')
curl -s "${H[@]}" -X PUT -H "If-Match: $ET2" $API/route-order -d "{\"route_ids\":$(jq -c --arg d "$DEF" '. + [$d]' <<<"$IDS")}" \
  | jq -r '"\(.status) \(.type | sub(".*/"; ""))"'                                         # 409 default-route-immutable
curl -s "${H[@]}" -X PUT -H "If-Match: $ET2" $API/route-order -d "{\"route_ids\":[\"$RA\"]}" | jq -r '.errors[0].code'
# route_set_mismatch
curl -s "${H[@]}" -X DELETE "$API/routes/$DEF" | jq -r '"\(.status) \(.type | sub(".*/"; ""))"'               # 409 default-route-immutable
R=$(curl -s -b jar "$API/routes/$RA"); BODY=$(jq -c '{name, matchers, urgent: true, group_key, destination_ids: [], policy}' <<<"$R")
curl -s "${H[@]}" -X PUT -H "If-Match: $(jq -r .etag <<<"$R")" "$API/routes/$RA" -d "$BODY" | jq -r .urgent       # true
curl -s "${H[@]}" -X PUT -H "If-Match: $(jq -r .etag <<<"$R")" "$API/routes/$RA" -d "$BODY" | jq -r .status       # 412

# C-08.AC-10, templates and Destinations
MK C '[{"label":"pod","op":"=~","value":"api-("}]' | jq -c '[.status, .errors[0].pointer, .errors[0].code]'
# [422,"/matchers/0/value","invalid_regex"]
curl -s "${H[@]}" $API/routes -d "{\"name\":\"T\",\"matchers\":[],\"urgent\":false,\"group_key\":[],\"destination_ids\":[],\"policy\":$(jq -c '.templates.root_message = "{{ .Title }}"' <<<"$P")}" \
  | jq -c '[.status, .errors[0].pointer, .errors[0].code]'
# [422,"/policy/templates/root_message","unsupported"]

# C-08.AC-9: Severity levels
curl -s -X PUT $FAM/groups/r2 -d '{"receiver":"lab","route":"{}","labels":{"alertname":"Sev"}}' > /dev/null
curl -s -X PUT $FAM/groups/r2/alerts/n -d '{"labels":{"severity":"none","k":"none"}}' > /dev/null
curl -s -X PUT $FAM/groups/r2/alerts/p -d '{"labels":{"severity":"P5","k":"p5"}}' > /dev/null
curl -s -X PUT $FAM/groups/r2/alerts/m -d '{"labels":{"k":"missing"}}' > /dev/null
NOTIFY r2 '{"reason":"first notification"}'
VIEW 'label=alertname%3D%22Sev%22' | jq -c '[.items[] | {k: .labels.k, l: .severity_level, raw: .severity_raw, r: .route.name}] | sort_by(.k)'
# [{"k":"missing","l":"info","raw":null,"r":"Default"},{"k":"none","l":"info","raw":null,"r":"Default"},{"k":"p5","l":"warning","raw":"P5","r":"Default"}]

# C-08.FR-12
curl -s localhost:8082/metrics | grep -c '^muster_route_info{'                           # 3   (A, B and Default)
```

## Open questions

None.

## Notes

- Suggested commit: `feat(routing): add routes, matchers, evaluation order, severity levels and route profiles`.
- The structured Matchers of Routes and the string Matchers of list filters share one parser and one matcher, so a
  filter and a Route never disagree about a label set.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-08.FR-1 | partial | the model and API; the editor is S-027, later policy sections come with their capabilities |
| C-08.FR-2 | partial | the API; the builder is S-027 |
| C-08.FR-3 | partial | the API; the list with the pinned Default route is S-027 |
| C-08.FR-4 | partial | stored; grouping by it is S-028 |
| C-08.FR-6 | partial | Severity levels of Alerts; the Alert Group's level and urgency are S-028 |
| C-08.FR-7 | partial | the API; choosing a profile is S-027 |
| C-08.FR-8 | partial | edits apply at once; open Alert Groups are S-028 |
| C-08.FR-9 | partial | deletion; Routes with open Alert Groups are S-028 |
| C-08.FR-10 | full | |
| C-08.FR-12 | full | |
| C-08.FR-13 | partial | the API; the columns of the view are S-027 |
| C-08.AC-1 | full | |
| C-08.AC-6 | full | |
| C-08.AC-7 | full | |
| C-08.AC-9 | partial | the API; the page is S-027 |
| C-08.AC-10 | full | |
| C-08.AC-11 | partial | the reorder and the profiles; accepting a suggestion twice is S-026 |
| C-06.FR-19 | partial | the Route and Severity level of each Alert |
