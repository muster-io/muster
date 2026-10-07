---
id: S-026
title: Group key preview and Route suggestions (BE)
capability: C-08
kind: be
layer: L1
depends_on: [S-025]
covers: [C-08.FR-5, C-08.FR-11, C-08.AC-2, C-08.AC-8, C-08.AC-11, C-08.AC-12]
files_touched:
  - internal/routing/preview.go
  - internal/routing/preview_test.go
  - internal/routing/suggestions.go
  - internal/routing/suggestions_test.go
  - internal/routing/routes.go
  - internal/routing/routes_test.go
  - internal/routing/routing_integration_test.go
  - internal/routing/query.sql
  - internal/ingest/snapshots.go
  - internal/ingest/snapshots_test.go
  - internal/ingest/store.go
  - internal/ingest/handler_test.go
  - internal/ingest/export_test.go
  - internal/ingest/ingest_integration_test.go
  - internal/ingest/query.sql
  - internal/internalalerts/raise.go
  - internal/api/routes.go
  - internal/api/routes_test.go
  - internal/api/server.go
  - internal/api/problem.go
  - internal/runtime/runtime.go
  - internal/logging/events.go
  - test/e2e/routing_preview_test.go
  - api/openapi.yaml
  - design/prd/l1/defaults.md
  - design/prd/L1.md
acceptance:
  - "[C-08.FR-5, C-08.AC-2] `previewGroupKey` for a saved Route counts the Alert Groups that the Stored Snapshots of the period would have produced with its current and with the proposed Group key, with examples and `truncated` `false` while the whole period was read; Alerts missing the `cluster` label group with the others missing it."
  - "[C-08.FR-5, C-08.AC-12] With the Matchers of an unsaved Route and a proposed key, the preview returns only the proposed side for the Snapshots of the period; with neither a Route nor Matchers it answers 422 `one_of_required`, and a period longer than `retention.stored_snapshots` answers 422 `out_of_range`."
  - "[C-08.FR-11, C-08.AC-8] While an Integration has its Heartbeat on and no Route other than the Default route would take its `MusterHeartbeatLost`, `listRouteSuggestions` offers `heartbeat_lost`; accepting it creates the suggested Route at the top of the list, and the suggestion is no longer offered."
  - "[C-08.FR-11, C-08.AC-11] Accepting a suggestion that no longer applies — accepted already, or `internal_alerts` while no Destination exists — answers 409 `suggestion_obsolete`."
  - "[C-08.FR-11] Dismissing a suggestion hides it for the calling user only; a Service account token gets 403 `service_account_not_allowed`."
verify: "make ci test-integration e2e"
operator_attention: false
issue: 26
---

# S-026. Group key preview and Route suggestions (BE)

## Scope

**IN**

- The Group key preview over the Stored Snapshots of a period, for a saved Route, for other Matchers on a saved Route,
  and for an unsaved Route.
- Route suggestions: listing, accepting and dismissing; the `heartbeat_lost` suggestion.

**OUT**

- The pages (S-027).
- The `internal_alerts` suggestion, which needs a Destination (S-061, C-13.FR-11).

## Contracts

- **Operations implemented**: `previewGroupKey` (`routes:write`), `listRouteSuggestions`, `acceptRouteSuggestion`,
  `dismissRouteSuggestion`. Schemas: `GroupKeyPreviewRequest`, `GroupKeyPreview`, `GroupKeyPreviewSide`,
  `GroupKeyPreviewExample`, `RouteSuggestion(List)`, `RouteSuggestionId`, `RouteSuggestionAcceptance`.
- **Preview** (C-08.FR-5, ADR-0003): `period_seconds` defaults to `routing.group_key_preview_period` (P-12) and may not
  exceed `retention.stored_snapshots` (`422 out_of_range` at `/period_seconds`); `route_id`, `matchers` or both are
  required (`422 one_of_required`); an unknown `route_id` answers `404`. The preview reads the Stored Snapshots of every
  Integration received in the period from the newest back, once per distinct body (webhook Snapshots that parse; failed
  ones skipped), applies each Integration's current Static labels, and keeps the distinct fingerprints that were firing
  in them — each with its labels from the newest Snapshot listing it — and that the Route takes in the current
  evaluation order: a saved Route at its position with its own Matchers, or with the given `matchers` when both are
  sent; `matchers` alone stand for a new Route just before the Default route, where `createRoute` puts it. It stops
  after `routing.group_key_preview_max_alerts` fingerprints; `truncated` is `true` when Snapshots of the period, or
  Alerts the Route takes, were then left unread, otherwise `false`. Each side groups those fingerprints by its Group key, a missing label counting as an empty value:
  `alert_group_count` is the number of distinct key values and `examples` the `routing.group_key_preview_examples`
  largest groups with their `group_key_values` and `alert_count`. `current` uses the saved Route's Group key and is
  absent without `route_id`; `proposed` uses `proposed_group_key`. The count is of distinct key values: Reopen windows
  and Grace periods, which can split one key into several Alert Groups over time, are not modelled.
- **Suggestions** (C-08.FR-11; `route_suggestion_dismissals`): computed on each read.
  - `heartbeat_lost` applies while an Integration that is not deleted has its Heartbeat on and its `MusterHeartbeatLost`
    — with the labels S-023 gives it — would be taken by no Route other than the Default route. The suggested
    `RouteInput`: name "Muster: Heartbeat lost", the Matcher `alertname="MusterHeartbeatLost"`, urgent, Group key
    `alertname`, `integration`, no Destinations, the policy of the On-call profile.
  - `internal_alerts` never applies in this story; S-061 adds it once Destinations exist.
  - `listRouteSuggestions` (`routes:read`) returns the suggestions that apply and that the calling user has not
    dismissed. `acceptRouteSuggestion` (`routes:write`) creates the suggested Route — with `destination_ids` from the
    body where given — at the top of the evaluation order (bumping the list ETag), writes `route.created` with the
    suggestion id and sends the `route` hint; a suggestion that no longer applies answers `409 suggestion_obsolete`,
    and another Route with its name, which does not take the alert, answers `409 name_taken` as on `createRoute`.
    `dismissRouteSuggestion` (`routes:read`) records the dismissal for the calling User; a Service account gets `403
    service_account_not_allowed`; an obsolete suggestion answers `409 suggestion_obsolete`.
- **Log event**: `group_key_previewed` (INFO: `route`, `period_seconds`, `snapshots_read`, `truncated`, `duration_ms`).
- **Defaults**: P-12 is confirmed or changed; `routing.group_key_preview_examples` (5) and
  `routing.group_key_preview_max_alerts` (10,000) are built in, as in defaults.md.

## Steps

1. Write the Snapshot reader for a period and the preview. Check: tests cover the saved Route at its position, other
   Matchers, an unsaved Route, missing labels, the limits and the errors.
2. Write the suggestions with acceptance and dismissal. Check: tests cover applying, accepting at the top, obsolete,
   dismissal per user and a Service account.
3. Confirm or change P-12. Check: defaults.md and L1.md say so.

## Verification

```sh
make dev &
# as in S-020 and S-025: the session `H`, the helpers NOTIFY, an Integration "pv" without Static labels (id INT) with
# its fake receiver "pv", and the On-call policy in P
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake
curl -s -X PUT $FAM/groups/k1 -d '{"receiver":"pv","route":"{}","labels":{"alertname":"Disk"}}' > /dev/null
for x in a1 a2; do curl -s -X PUT $FAM/groups/k1/alerts/$x -d "{\"labels\":{\"cluster\":\"a\",\"node\":\"$x\"}}" > /dev/null; done
for x in m1 m2; do curl -s -X PUT $FAM/groups/k1/alerts/$x -d "{\"labels\":{\"node\":\"$x\"}}" > /dev/null; done
NOTIFY k1 '{"reason":"first notification"}'

# C-08.AC-12 and C-08.AC-2: an unsaved Route, then a saved one
curl -s "${H[@]}" $API/group-key-previews \
  -d '{"matchers":[{"label":"alertname","op":"=","value":"Disk"}],"proposed_group_key":["alertname","cluster"]}' \
  | jq -c '{current, n: .proposed.alert_group_count, ex: [.proposed.examples[] | [.group_key_values.cluster, .alert_count]]}'
# {"current":null,"n":2,"ex":[["",2],["a",2]]}
RD=$(curl -s "${H[@]}" $API/routes -d "{\"name\":\"disk\",\"matchers\":[{\"label\":\"alertname\",\"op\":\"=\",\"value\":\"Disk\"}],
  \"urgent\":false,\"group_key\":[\"alertname\"],\"destination_ids\":[],\"policy\":$P}" | jq -r .id)
curl -s "${H[@]}" $API/group-key-previews -d "{\"route_id\":\"$RD\",\"proposed_group_key\":[\"alertname\",\"cluster\"]}" \
  | jq -c '{c: .current.alert_group_count, p: .proposed.alert_group_count, period: .period_seconds, t: .truncated}'
# {"c":1,"p":2,"period":86400,"t":false}
curl -s "${H[@]}" $API/group-key-previews -d '{"proposed_group_key":["alertname"]}' | jq -r '.errors[0].code'           # one_of_required
curl -s "${H[@]}" $API/group-key-previews -d "{\"route_id\":\"$RD\",\"proposed_group_key\":[],\"period_seconds\":2000000}" \
  | jq -r '.errors[0].code'                                                                                             # out_of_range

# C-08.AC-8 and C-08.AC-11: the Integration "hb" of S-023 has its Heartbeat on
curl -s -b jar $API/route-suggestions | jq -c '[.items[] | {id, name: .route.name, m: .route.matchers}]'
# [{"id":"heartbeat_lost","name":"Muster: Heartbeat lost","m":[{"label":"alertname","op":"=","value":"MusterHeartbeatLost"}]}]
curl -s "${H[@]}" -X POST $API/route-suggestions/heartbeat_lost/accept | jq -r .name                                  # Muster: Heartbeat lost
curl -s -b jar $API/routes | jq -r '.items[0].name'                                                                   # Muster: Heartbeat lost
curl -s -b jar $API/route-suggestions | jq '.items | length'                                                          # 0
curl -s "${H[@]}" -X POST $API/route-suggestions/heartbeat_lost/accept | jq -r .code                                  # suggestion_obsolete
curl -s "${H[@]}" -X POST $API/route-suggestions/internal_alerts/accept -d '{"destination_ids":[]}' | jq -r .code     # suggestion_obsolete

# dismissal per user: delete the Route again, dismiss as the Admin, the Responder still sees it
curl -s -o /dev/null "${H[@]}" -X DELETE "$API/routes/$(curl -s -b jar $API/routes | jq -r '.items[0].id')"
curl -s -o /dev/null -w '%{http_code}\n' "${H[@]}" -X POST $API/route-suggestions/heartbeat_lost/dismiss             # 204
curl -s -b jar $API/route-suggestions | jq '.items | length'                                                          # 0
curl -s -b resp $API/route-suggestions | jq '.items | length'                                                         # 1  (the Responder of S-018)
# SAT: a token of a Service account with the Role Admin, created as in S-016
curl -s -H "Authorization: Bearer $SAT" -X POST $API/route-suggestions/heartbeat_lost/dismiss | jq -r .code          # service_account_not_allowed
```

## Open questions

None.

## Notes

- Suggested commit: `feat(routing): add the group key preview and route suggestions`.
- The preview reads each distinct body once (bodies are de-duplicated per day, and repeats are byte-identical); the
  nightly load test measures a 24-hour preview at the NFR-1 rate.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-08.FR-5 | partial | the API; the editor's preview is S-027 |
| C-08.FR-11 | partial | `heartbeat_lost` with accept and dismiss; the page is S-027, `internal_alerts` is C-13.FR-11 (S-061) |
| C-08.AC-2 | full | |
| C-08.AC-8 | partial | the API; "the Routes page offers one" is S-027 |
| C-08.AC-11 | full | together with S-025 |
| C-08.AC-12 | full | |
