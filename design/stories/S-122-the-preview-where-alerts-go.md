---
id: S-122
title: The preview "Where alerts go" for the unsaved settings of a Route (BE)
capability: C-26
kind: be
layer: L2
depends_on: [S-121]
covers: [C-26.FR-12, C-26.FR-13]
files_touched:
  - api/openapi.yaml
  - internal/routing/preview.go
  - internal/routing/preview_test.go
  - internal/routing/query.sql
  - internal/routing/routing_integration_test.go
  - internal/api/routes.go
  - internal/api/routes_test.go
  - internal/teams/classify.go
  - internal/teams/classify_test.go
  - test/e2e/routing_preview_test.go
  - docs/concepts/routing-to-teams.md
acceptance:
  - "[C-26.FR-12] `previewGroupKey` takes, besides the Matchers and the proposed Group key, the unsaved `minimum_severity`, `destination_entries` and `deliver_to_teams` of the Route — the saved Route's when a field is absent and `route_id` is given — and answers `delivery`: the `routing.group_key_preview_examples` largest Alert Groups that the Alerts of the period would form under the proposed key, each with its Group key values, its number of Alerts, its Severity level raised to the Minimum severity, its Teams by the Team label and Team matchers as saved, and the Destinations it would reach with the entries that reach each; and `destination_counts`, for every Destination that any Alert Group the Alerts would form reaches — the listed ones and the others — how many of all those Alert Groups reach it (`preview_test.go`)."
  - "[C-26.FR-12] With the settings of S-121's Route \"Infrastructure\" sent unsaved, Alerts of `team=\"payments\"` (warning) and `team=\"devops\"` (critical) in the period, and the Team \"payments\" with `payments-notice` below critical, the preview lists the Alert Group of payments reaching `infra-notice` (the Route) and `payments-notice` (the Team), and that of devops reaching `infra-critical` only; with `deliver_to_teams` false the first reaches `infra-notice` only; the preview writes nothing (`routing_preview_test.go`)."
  - "[C-26.FR-12] A preview without a Team label value in the proposed Group key and `deliver_to_teams` true answers `shared_key_warning` true, so that the form can show the hint of reference.md and offer to add the Team label (`preview_test.go`)."
  - "[C-26.FR-13] Routing to Teams has the section \"Preview where alerts go\"; `make docs-check` passes."
verify: "make ci test-integration e2e-go docs-check"
operator_attention: false
issue: 411
---

# S-122. The preview "Where alerts go" for the unsaved settings of a Route (BE)

## Scope

**IN**

- The delivery part of the Group key preview (C-26.FR-12): for the recent Alerts that a Route would take, the Alert
  Groups they would form under the unsaved settings, with the Teams of each and the Destinations each reaches.
- The flag that the Team label is missing from the Group key while the Route delivers to Teams, for the form's hint.

**OUT**

- The preview on the Route form, the hint and "Add {team label} to the key": S-123.
- Delivery itself: S-121.

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated):
  - `GroupKeyPreviewRequest` gains `minimum_severity` (`MinimumSeverity`), `destination_entries` (`DestinationEntry`[])
    and `deliver_to_teams` (boolean), all optional; absent ones take the saved Route's when `route_id` is given, else
    `none`, no entries and false.
  - `GroupKeyPreview` gains `delivery` (`DeliveryPreview`, absent when neither entries nor `deliver_to_teams` were given
    and no Route was): {`alert_groups` — `DeliveryPreviewGroup`[] {`group_key_values` (`Labels`), `alert_count`,
    `severity_level`, `teams` (`TeamRef`[]), `destinations` — {`destination` (`DestinationRef`), `entries`
    (`DeliveryEntry`[])}[]}; `destination_counts` — {`destination` (`DestinationRef`), `alert_group_count`}[];
    `shared_key_warning` (boolean)}.
  - The description of `previewGroupKey` names the delivery preview; its Permission stays `routes:write`.
- **Routing** (`internal/routing/preview.go`): the Alerts of the period that the Route would take, as for the Group key
  preview (C-08.FR-5, the same period, cap and Static labels), grouped under the proposed key; for each group the
  highest Severity level of its Alerts (by the Organization's mapping) raised to the Minimum severity, its Teams by the
  current classification of S-118 (`teams.Classifier`, read outside a Snapshot), and its reach by the rule of S-121 —
  the Route's proposed entries whose condition the level meets and, with `deliver_to_teams`, the saved entries of each
  of its Teams. The preview reads only; it writes nothing, records no Audit log entry and sends no hint. The
  `routing.group_key_preview_examples` largest groups are listed; `destination_counts` covers every Alert Group the
  Alerts would form, not only the listed ones.
- **Teams** (`internal/teams/classify.go`): the classifier answers outside the Snapshot's transaction too, from a
  read of the Team label, slugs and matchers.
- **Settings**: `routing.group_key_preview_period`, `routing.group_key_preview_examples`,
  `routing.group_key_preview_max_alerts`, `organization.team_label`.
- **Log events, metrics**: none new.
- **Documentation**: Routing to Teams — "Preview where alerts go".

## Steps

1. Extend the spec and the request. Check: `make generate-check`; `routes_test.go` covers the mapping and the
   defaults of absent fields.
2. Compute the delivery preview. Check: `preview_test.go` covers each condition, `deliver_to_teams` on and off, an
   Alert of two Teams, a Minimum severity, the warning and the counts; `routing_integration_test.go` runs it on
   Stored Snapshots.
3. Write the end-to-end test and the documentation. Check: the Verification below; `make docs-check`.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-121: A, the Integration "sev", the Destinations IC, IN, PN and SN and the Team "payments" with PC and PN.
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake
curl -s -X PUT $FAM/groups/w1 -d '{"receiver":"sev","route":"{}","labels":{"alertname":"Disk"}}' > /dev/null
PUTA w1 a '{"labels":{"category":"infra","system":"k8s","team":"payments","severity":"warning"}}'
PUTA w1 b '{"labels":{"category":"infra","system":"k8s","team":"devops","severity":"critical"}}'
NOTIFY w1 '{"reason":"first notification"}'
N0=$(psql "$MUSTER_DATABASE_URL" -Atc "SELECT count(*) || '/' || coalesce(sum(version), 0) FROM routes")   # before the previews
PREVIEW() { curl -s "${A[@]}" $API/group-key-previews -d "{\"matchers\":[{\"label\":\"category\",\"op\":\"=\",\"value\":\"infra\"}],
  \"proposed_group_key\":[\"alertname\",\"system\",\"team\"],\"deliver_to_teams\":$1,\"destination_entries\":[
  {\"destination_id\":\"$IC\",\"severity_condition\":\"critical\"},{\"destination_id\":\"$IN\",\"severity_condition\":\"below_critical\"}]}"; }
PREVIEW true | jq -c '[.delivery.alert_groups[] | {t: [.teams[].slug], l: .severity_level, d: [.destinations[] | "\(.destination.name):\(.entries[0].kind)"]}] | sort_by(.l)'
# [{"t":[],"l":"critical","d":["infra-critical:route"]},{"t":["payments"],"l":"warning","d":["infra-notice:route","payments-notice:team"]}]
PREVIEW false | jq -c '[.delivery.alert_groups[] | select(.teams | length > 0) | [.destinations[].destination.name]]'   # [["infra-notice"]]
curl -s "${A[@]}" $API/group-key-previews -d "{\"matchers\":[{\"label\":\"category\",\"op\":\"=\",\"value\":\"infra\"}],
  \"proposed_group_key\":[\"alertname\"],\"deliver_to_teams\":true}" | jq .delivery.shared_key_warning              # true
[ "$(psql "$MUSTER_DATABASE_URL" -Atc "SELECT count(*) || '/' || coalesce(sum(version), 0) FROM routes")" = "$N0" ] && echo unchanged   # unchanged
```

`test/e2e/routing_preview_test.go` repeats these checks.

## Open questions

1. **How many Alert Groups the preview lists.** Settled by the maintainer (review of PR #399): the
   `routing.group_key_preview_examples` largest Alert Groups (5), and `destination_counts` over every Alert Group the
   Alerts would form, listed or not, as C-26.FR-12 now says; the contract above follows it.

## Notes

- Suggested commit: `feat(routing): preview where a route's alerts go`.
- The Teams of the preview use the Team label and matchers as saved: the Route form does not edit Teams, and an unsaved
  Team change would need a preview of its own.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-26.FR-12 | partial | the preview in the API; the form, the hint and "Add {team label} to the key" are S-123 |
| C-26.FR-13 | partial | the section on the preview |
