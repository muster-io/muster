---
id: S-187
title: "The Terraform resources of Escalation chains, a Team's chains, Routes, the Default route and the Route order, with a complete example (BE)"
capability: C-33
kind: be
layer: L2
repository: muster-io/terraform-provider-muster
depends_on: [S-183, S-186]
covers: [C-33.FR-5, C-33.FR-7, C-33.FR-8, C-33.FR-9, C-33.FR-13, C-33.FR-14, C-33.FR-16, C-33.FR-17, C-33.FR-19, C-33.AC-2, C-33.AC-5, C-33.AC-7, C-33.AC-11, C-33.AC-13, C-33.AC-14]
files_touched:
  - internal/provider/provider.go
  - internal/provider/resource_escalation_chain.go
  - internal/provider/resource_escalation_chain_test.go
  - internal/provider/resource_team_escalation_chains.go
  - internal/provider/resource_team_escalation_chains_test.go
  - internal/provider/route_common.go
  - internal/provider/resource_route.go
  - internal/provider/resource_route_test.go
  - internal/provider/resource_default_route.go
  - internal/provider/resource_default_route_test.go
  - internal/provider/resource_route_order.go
  - internal/provider/resource_route_order_test.go
  - internal/provider/complete_test.go
  - examples/complete/main.tf
  - examples/resources/muster_escalation_chain/resource.tf
  - examples/resources/muster_escalation_chain/import.sh
  - examples/resources/muster_team_escalation_chains/resource.tf
  - examples/resources/muster_team_escalation_chains/import.sh
  - examples/resources/muster_route/resource.tf
  - examples/resources/muster_route/import.sh
  - examples/resources/muster_default_route/resource.tf
  - examples/resources/muster_default_route/import.sh
  - examples/resources/muster_route_order/resource.tf
  - examples/resources/muster_route_order/import.sh
acceptance:
  - "[C-33.FR-5, C-33.FR-13, C-33.FR-19, C-33.AC-14] `muster_escalation_chain`, written with `managed_by` `terraform`, owned by a Team, with the steps Page the On-call of a `muster_schedule`, wait 10 minutes, Page two Users found by `data.muster_user`, Page the Team found by `data.muster_team`, call through the fake gateway's Connection found by `data.muster_connection`, and repeat from step 1 twice, applies; a second plan shows no change; `referrers`, `destinations` and `warnings` are not attributes (`TestAccEscalationChain_*`)."
  - "[C-33.FR-7, C-33.AC-13] A wait step of 1,441 minutes is refused by the server and the diagnostic is at `steps[1].wait_seconds` with the code `out_of_range`; the provider itself accepts any whole number there (`TestAccEscalationChain_bounds`)."
  - "[C-33.FR-8, C-33.FR-16, C-33.AC-11] `muster_team_escalation_chains` names a chain owned by the same Team, with the condition `critical`, in the same apply as the chain, without a cycle; together with `muster_team_destinations` of S-186 and a rename by `muster_team`, all three changes hold after one apply, run ten times with `-parallelism=10` (`TestAccTeamEscalationChains_*`)."
  - "[C-33.FR-5, C-33.AC-14] `muster_route` with Matchers, the Minimum severity `critical`, a Group key, a Destination entry, a chain entry, `deliver_to_teams`, an owning Team, \"Labels in the message\" and a `root_message` template applies after the Destination and the chain it names; the policy fields that the configuration leaves out take the values of its `profile` (`paging`), and a second plan shows no change; `urgent` and `destination_ids` are not attributes (`TestAccRoute_*`)."
  - "[C-33.AC-5] A Route renamed through the API, and a Matcher added to it through the API, show in the next plan and are undone by the apply (`TestAccRoute_drift`)."
  - "[C-33.FR-17] Destroying a Route that has an open Alert Group fails with a diagnostic carrying `route-has-open-alert-groups` and its count, and the Route stays in the state (`TestAccRoute_openAlertGroups`)."
  - "[C-33.FR-14] `muster_default_route` adopts the Default route, sets its Destination entries and keeps what the configuration leaves out; its destroy changes nothing but its `managed_by` and warns that it stays (`TestAccDefaultRoute_*`)."
  - "[C-33.FR-16, C-33.AC-11] `muster_route_order` applies the order of three Routes created in the same apply, without a `412`, and reads it back; a fourth Route created through the API makes the next apply fail with an error that names it; the Default route in the list is refused by the provider's validation (`TestAccRouteOrder_*`)."
  - "[C-33.AC-2] `examples/complete/main.tf` — the provider block, `muster_organization`, `muster_outbound_policy`, `muster_oidc_settings`, an Integration with its token, one Connection and Destination of each type, a Lookup table and a Link rule, two Teams with members, Destinations and chains, a Schedule, an Escalation chain, two Routes, the Default route, the Route order and the three data sources — applies from an empty `muster dev`; a second plan shows no change; `terraform destroy` deletes every object it created and leaves the objects that exist once with their settings and without `managed_by` (`TestAccComplete`)."
  - "[C-33.FR-9, C-33.AC-7] Each resource of this story imports — by `public_id`, the Team's for `muster_team_escalation_chains`, any id for the Default route and the Route order — and plans without changes (`TestAcc*_import`)."
verify: "make ci testacc"
operator_attention: false
issue: 705
---

# S-187. The Terraform resources of Escalation chains, a Team's chains, Routes, the Default route and the Route order, with a complete example (BE)

## Scope

**IN**

- `muster_escalation_chain` with its steps, and `muster_team_escalation_chains` on `teamlist.go` (S-186).
- `muster_route` and `muster_default_route`, sharing their attributes in `route_common.go`, and `muster_route_order`.
- `examples/complete/main.tf`, the example of C-33.AC-2, run by `TestAccComplete` and shown by the guides of S-188.

**OUT**

- The guides and the publication (S-188); the page on Muster's documentation site (S-189).
- Moving open Alert Groups before a Route is destroyed (`moveOpenAlertGroups`): an operation; the destroy reports
  Muster's `409`, as the maintainer decided (C-33.FR-17).

## Contracts

- **`muster_escalation_chain`** (Escalation chain operations): `name`, `description`, `owning_team_id`, `steps` — each
  `kind` and the fields of `EscalationStepInput`: `importance`, `schedule_id`, `user_ids`, `team_id`, `everyone`,
  `call_target`, `gateway_id`, `wait_seconds`, `destination_id`, `repeat_from`, `repeat_times`, `working_hours_only`,
  `severity_condition`; computed `id`, `etag`. Bounds are the server's (`escalation.limits`, C-33.FR-7).
- **`muster_team_escalation_chains`**: as `muster_team_destinations`, over `chain_entries` (`chain_id`,
  `severity_condition`).
- **`muster_route`** (Route operations): `name`, `description`, `matchers`, `minimum_severity`, `group_key`,
  `destination_entries`, `chain_entries`, `deliver_to_teams`, `run_team_chains`, `owning_team_id`,
  `own_message_labels`, `message_labels`, `policy` (the fields of `RoutePolicy`, `templates` with `root_message`,
  `line` and `ack_timeout_notice` among them), and `profile` (`informational` by default, or `paging`), which is not
  stored in Muster: the policy fields and the Group key that the configuration leaves out take the values of that
  profile from `listRouteProfiles`, on create and on every plan; a changed `profile` changes only those values.
  Computed `id`, `etag`; not `position`, which changes whenever another Route is created or moved, `urgent`,
  `destination_ids`, `chains`, `destinations`, `storm*`, counts or `template_error`. Its create and delete take the
  lock of the Route list, so that they never run between the fresh read and the write of `muster_route_order`.
- **`muster_default_route`**: the attributes of `muster_route` but `name`, `matchers` and `profile`, each optional and
  computed, on the Route whose `is_default` is true, with the adopt-and-forget lifecycle of `singleton.go` (S-183).
- **`muster_route_order`** (`listRoutes`, `reorderRoutes`): `route_ids`, in order, every Route but the Default route.
  The ETag of `listRoutes` covers the order, which every create and delete of a Route changes, so the resource takes
  the shared-object path of `resourcekit` (S-182) with the lock of the Route list, which `muster_route` takes too on
  create and delete: it reads `listRoutes` right before `reorderRoutes`, stops with the error of C-33.FR-8 when the
  Routes of its prior state stand in Muster in another order than that state's, and sends the ETag of that read.
  `422` `route_set_mismatch` becomes an error that names the Routes missing from the list or unknown to Muster; Delete
  forgets the order and changes nothing.
- **Complete example** (`examples/complete/main.tf`): every resource and data source of C-33.FR-5 and C-33.FR-6, on the
  fake servers of `muster dev`, with `bot_token_wo` for one Connection and the sensitive way for the others.

## Steps

1. Write `muster_escalation_chain` and `muster_team_escalation_chains`. Check: their acceptance tests.
2. Write `route_common.go`, `muster_route` with the profile defaults, and `muster_default_route`. Check: their tests.
3. Write `muster_route_order`. Check: its tests.
4. Write the complete example and `TestAccComplete`, then the examples and the documentation. Check:
   `make docs-check`, `make ci testacc` on the four CLI versions.

## Verification

In a clone of the provider, with `eval "$(test/muster-dev.sh up)"` and the provider installed as in S-182:

```sh
cd examples/complete
terraform init > /dev/null && terraform apply -auto-approve > apply.log; tail -1 apply.log
# Apply complete! Resources: N added, 0 changed, 0 destroyed.
terraform plan -detailed-exitcode > /dev/null; echo "exit=$?"                                         # exit=0
A=(-H "Authorization: Bearer $MUSTER_TOKEN"); API=$MUSTER_ENDPOINT/api/v1
curl -s "${A[@]}" $API/routes | jq -c '[.items[] | {name, position, managed_by}]'
# the two Routes in the order of muster_route_order, then the Default route, each with "managed_by":"terraform"
R=$(curl -s "${A[@]}" $API/routes | jq -r '.items[0].id'); E=$(curl -s "${A[@]}" $API/routes/$R | jq -r .etag)
curl -s -o /dev/null "${A[@]}" -H 'Content-Type: application/json' -H "If-Match: $E" -X PUT \
  -d "$(curl -s "${A[@]}" $API/routes/$R | jq -c '{name: "renamed by hand", matchers, group_key}')" $API/routes/$R
terraform plan -no-color | grep -c '"renamed by hand" ->'                                              # 1
terraform apply -auto-approve > /dev/null && curl -s "${A[@]}" $API/routes/$R | jq -r .name             # the configured name
terraform destroy -auto-approve > destroy.log; grep -c 'stays in Muster' destroy.log                   # 4
curl -s "${A[@]}" $API/routes | jq -c '[.items[] | {is_default, managed_by}]'                         # [{"is_default":true,"managed_by":null}]
```

The four objects that exist once — the Organization's settings, the Outbound address policy, the OIDC settings and
the Default route — warn on destroy. The pull request carries this output, `TestAccComplete` and the acceptance run.

## Open questions

None. Destroying a `muster_route` never moves its open Alert Groups: it reports Muster's `409`
`route-has-open-alert-groups`, as the maintainer decided, and the Route's documentation names `moveOpenAlertGroups`.

## Notes

- Suggested commit: `feat: manage escalation chains, routes, the default route and the route order`.
- `profile` exists only in the provider: the API does not default the policy of a new Route, and a configuration that
  had to spell out every policy field would be long and would break when a `0.2.x` release adds one.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-33.FR-5 | partial | with S-183 to S-186 complete |
| C-33.FR-7 | partial | with S-183 to S-186 complete |
| C-33.FR-8 | partial | the Route order and a Team's chains as shared objects; with S-182, S-184 and S-186 complete |
| C-33.FR-9 | partial | with S-183 to S-186 complete |
| C-33.FR-13 | partial | with S-183 to S-186 complete |
| C-33.FR-14 | partial | the Default route; with S-183 and S-185 complete |
| C-33.FR-16 | partial | the Route order and a Team's chains; with S-186 complete |
| C-33.FR-17 | partial | the refusal of a Route with open Alert Groups; with S-182, S-184 and S-186 complete |
| C-33.FR-19 | partial | these resources and the complete example |
| C-33.AC-2 | partial | the complete example; with S-183 complete |
| C-33.AC-5 | partial | Routes; with S-183 and S-184 complete |
| C-33.AC-7 | partial | with S-183 to S-186 complete |
| C-33.AC-11 | partial | the Route order and a Team's chains; with S-186 complete |
| C-33.AC-13 | partial | the server's bound; the refusal `in_use` is S-184's |
| C-33.AC-14 | partial | the chain and the Route; with S-186 complete |
