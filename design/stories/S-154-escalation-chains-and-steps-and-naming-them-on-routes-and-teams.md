---
id: S-154
title: Escalation chains and their steps, naming them on Routes and Teams with Severity conditions, and the refusals that protect them (BE)
capability: C-32
kind: be
layer: L2
depends_on: [S-140, S-148, S-152]
covers: [C-32.FR-1, C-32.FR-2, C-32.FR-3, C-32.FR-17, C-32.FR-19, C-32.AC-15, C-30.FR-9, C-25.FR-1, C-25.FR-2, C-25.FR-9, C-25.FR-10, C-27.FR-12, C-27.AC-8, C-08.FR-1]
files_touched:
  - api/openapi.yaml
  - api/README.md
  - internal/db/migrations/0040_escalation_chains.up.sql
  - internal/db/migrations/0040_escalation_chains.down.sql
  - design/db/schema.md
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - internal/publicid/publicid.go
  - internal/escalation/chains.go
  - internal/escalation/chains_test.go
  - internal/escalation/steps.go
  - internal/escalation/steps_test.go
  - internal/escalation/warnings.go
  - internal/escalation/warnings_test.go
  - internal/escalation/query.sql
  - internal/escalation/chains_integration_test.go
  - internal/api/escalationchains.go
  - internal/api/escalationchains_test.go
  - internal/api/server.go
  - internal/routing/routes.go
  - internal/routing/query.sql
  - internal/routing/routes_test.go
  - internal/api/routes.go
  - internal/teams/teams.go
  - internal/teams/query.sql
  - internal/teams/teams_test.go
  - internal/api/teams.go
  - web/src/test/fixtures/teams.ts
  - internal/schedules/schedules.go
  - internal/schedules/schedules_integration_test.go
  - internal/runtime/runtime.go
  - internal/metrics/catalogue.go
  - design/prd/l1/reference.md
  - design/prd/l1/defaults.md
  - design/prd/L2.md
  - test/e2e/escalation_chains_test.go
  - docs/concepts/escalation-chains.md
  - mkdocs.yml
  - internal/auth/auth_integration_test.go
acceptance:
  - "[C-32.FR-1, C-32.FR-19, C-25.FR-2] `createEscalationChain` makes a chain with a name unique in the Organization (`409` `name_taken`), a description, an optional `owning_team_id` and its ordered steps; `getEscalationChain`, `updateEscalationChain` with `If-Match` (`412` when stale, `428` without) and `deleteEscalationChain` work as for Teams; `listEscalationChains` filters by `team` — a Team's `public_id` or `mine` — and `q`; every change is an Audit log entry `escalation_chain.created`, `escalation_chain.updated` or `escalation_chain.deleted` with a before/after diff, and `muster_escalation_chain_info{chain,name}` has one series per chain (`chains_test.go`, `chains_integration_test.go`)."
  - "[C-32.FR-3] A step is one of `page_on_call` (a Schedule of any Team, `normal` or `important`), `page_people` (`user_ids`, `normal` or `important`), `page_team` (a Team — the chain's owning Team when omitted — or `everyone` with `channel`, `here`, `all` or `everyone`), `call` (`call_target` `on_call` with a Schedule or `people` with `user_ids`, and `gateway_id` when more than one Notification gateway exists), `wait` (`minutes`), `webhook` (an outgoing webhook Destination in events mode or both) and `repeat` (`repeat_from`, an earlier step's number, and `repeat_times`); a step that Pages or sends takes `working_hours_only` and a `severity_condition`; other fields with a kind answer `422` at their pointer (`steps_test.go`)."
  - "[C-32.FR-3, C-32.AC-15] Saving a step that names a Viewer or a disabled User — in `page_people` or `call` — answers `422` `not_pageable` at `/steps/{i}/user_ids/{j}` with the `reason` `viewer` or `disabled`, for the people the save adds, as S-131 does for Rotation layers; more than 20 steps, a wait outside 1 to 1,440 minutes, `repeat_times` above 10 or a `repeat_from` that is not an earlier step answer `422` `out_of_range` (`escalation.limits`); a `webhook` step naming a Destination that is not an outgoing webhook in events mode or both answers `422` `webhook_events_required` (`steps_test.go`)."
  - "[C-30.FR-9, C-32.FR-3] A `call` step answers `422` `gateway_required` while no Notification gateway exists, and `422` `required` at `gateway_id` while several do; a chain whose `call` step names a gateway deleted since reads the warning `gateway_deleted` on that step (`steps_test.go`, `warnings_test.go`)."
  - "[C-32.FR-2, C-08.FR-1] Routes and Teams take `chain_entries` — each a chain with a `severity_condition`, `any` by default — in their inputs, reads and Audit log diffs, with `routes:write` and `teams:write`; the chain reads its `referrers` (each Route and Team with its condition) and `destinations` (those of its Routes and Teams, where it writes); `deleteEscalationChain` of a named chain answers `409` `chain_in_use` naming them in `in_use_by` (`routes_test.go`, `teams_test.go`, `chains_test.go`)."
  - "[C-32.FR-17] A chain reads `warnings`: `everyone_unsupported` for a `page_team` step whose Team mention or token one of its Destinations' Connections cannot mention, with the Destination and the reason of `MentionUnavailableReason` — such as a Telegram Destination — and `gateway_deleted` and `destination_deleted` for steps whose gateway or webhook Destination is gone (`warnings_test.go`)."
  - "[C-32.AC-15] A Responder gets `403` on `createEscalationChain`, `updateEscalationChain` and `deleteEscalationChain`; a Viewer reads chains with `escalation-chains:read`; naming a chain on a Route needs `routes:write` (`escalationchains_test.go`)."
  - "[C-25.FR-10, C-25.FR-1, C-25.FR-9] `deleteTeam` of a Team that owns a chain answers `409` `team_in_use` naming the chain in `in_use_by` (`type` `escalation_chain`); a Team's chains are in its read as `chains`; a step limited to Working hours uses those of the chain's owning Team, else the Organization's (`teams_test.go`, `steps_test.go`)."
  - "[C-27.FR-12, C-27.AC-8] `deleteSchedule` of a Schedule that a step names answers `409` `schedule_in_use` naming the chain, through `schedules.Referrers` (`schedules_integration_test.go`)."
  - "The migration adds the Permissions `escalation-chains:read` (every Role) and `escalation-chains:write` (Admin), so that `TestIntegrationRoleMatrix` counts 38, 16 and 11 Permissions for Admin, Responder and Viewer instead of S-131's 36, 15 and 10 — no story between them adds one (`auth_integration_test.go`); reference.md moves them and `muster_escalation_chain_info` into the main tables; P-84 is confirmed in defaults.md and L2.md §5.3; Concepts → Escalation chains has \"What an Escalation chain is\", \"Steps\" and \"Naming chains on Routes and Teams\" in the navigation; `make docs-check` passes."
verify: "make ci test-integration e2e-go docs-check"
operator_attention: true
issue: null
---

# S-154. Escalation chains and their steps, naming them on Routes and Teams with Severity conditions, and the refusals that protect them (BE)

## Scope

**IN**

- The new domain package `internal/escalation`: Escalation chains with a name, a description, an optional owning Team
  and their steps within `escalation.limits`, as configuration (C-32.FR-1, FR-3, FR-19).
- Chains named by Routes and Teams with Severity conditions, as Destinations are; the refusals `chain_in_use`,
  `team_in_use` for owned chains and `schedule_in_use` for Schedules that steps name (C-32.FR-2, C-25.FR-10,
  C-27.FR-12).
- The checks of the people a step names, of the gateway of a call and of the Destination of a webhook step (C-32.FR-3,
  C-30.FR-9), and the chain's warnings (C-32.FR-17).
- The Permissions, the metric and the first sections of the documentation page.

**OUT**

- Running chains: when they start, start over and stop, the ack timeout, Unclaimed, waits, repeats and skipped steps:
  S-169. What Page steps do: S-155 (by Mention), S-157 (calls); the webhook step: S-156.
- The chain page, the chains on the Route form and the Team page, and the Route's warning without a Destination: S-158.
- Which Destinations a running chain writes in for one Alert Group: S-155. The `destinations` of the read are the
  configuration's.

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated; a new tag "Escalation chains"):
  - Operations: `listEscalationChains` (`GET /escalation-chains`, `escalation-chains:read`; `cursor`, `limit`, `team` —
    a Team's `public_id` or `mine`, `422` for a Service account token — and `q`), `createEscalationChain` (`POST`,
    `escalation-chains:write`), `getEscalationChain`, `updateEscalationChain` (`PUT` with `If-Match`),
    `deleteEscalationChain` (optional `If-Match`) on `/escalation-chains/{escalation_chain_id}`. Session or token,
    `x-listener: app`; the path parameter `EscalationChainId`.
  - Schemas: `EscalationChainInput` {`name`, `description`, `owning_team_id` (`NullablePublicId`), `steps`
    (`EscalationStepInput`[])}; `EscalationChain` = it + `id`, `owning_team` (`TeamRef` or null), `steps`
    (`EscalationStep`[], each with `number` from 1 and the referenced `schedule`, `users`, `team`, `gateway`,
    `destination`), `referrers` ({`routes` ({`route`: `EntityRef`, `severity_condition`}[]), `teams` ({`team`:
    `TeamRef`, `severity_condition`}[])}), `destinations` (`DestinationRef`[]), `warnings` (`EscalationChainWarning`[]
    {`kind` — `everyone_unsupported`, `gateway_deleted`, `destination_deleted` —, `step` (number), `destination`,
    `reason`}), `created_at`, `etag`; `EscalationChainList`; `EscalationChainRef` {`id`, `name`, `owning_team`};
    `EscalationStepKind` (`page_on_call`, `page_people`, `page_team`, `call`, `wait`, `webhook`, `repeat`);
    `PageImportance` (`normal`, `important`); `EscalationStepInput` {`kind`, `importance`, `schedule_id`, `user_ids`,
    `team_id`, `everyone`, `call_target` (`on_call`, `people`), `gateway_id`, `minutes`, `destination_id`,
    `repeat_from`, `repeat_times`, `working_hours_only` (default false), `severity_condition` (`SeverityCondition` of
    S-121, default `any`)}; `ChainEntry` {`chain_id`, `severity_condition`}.
  - `RouteInput` and `TeamInput` gain `chain_entries` (`ChainEntry`[], optional; omitted on update keeps); `Route` and
    `Team` require `chain_entries` and gain `chains` (`EscalationChainRef`[]).
  - `Permission` gains `escalation-chains:read` and `escalation-chains:write`; `HintEvent.type` gains
    `escalation-chain`; `PublicId` and `api/README.md` gain the prefix `EC`; `Problem.in_use_by[].type` gains
    `escalation_chain`, `route` and `team`.
  - Problem codes: `chain_in_use` (`409` `conflict`, with `in_use_by`); `webhook_events_required` (`validation-failed`);
    `gateway_required` (declared by S-152); `not_pageable` (S-131) at `/steps/{i}/user_ids/{j}`; `name_taken`;
    `out_of_range`; `unknown_id` at the step's reference.
- **Schema** (migration `0040_escalation_chains`, expand only; `schema.md` §2, §4.8, §4.15 and §4.18):
  - `escalation_chains`: `id`, `org_id`, `public_id` (`CHECK (public_id ~ '^EC[0-9A-HJKMNP-TV-Z]{12}$')`), `name`,
    `description`, `owning_team_id` (FK `teams`, nullable), `version`, `created_at`, `updated_at`; unique `(org_id,
    lower(name))`; index `(org_id, owning_team_id)`.
  - `escalation_steps`: `id`, `org_id`, `chain_id` (FK, `ON DELETE CASCADE`), `position`, `kind` (`CHECK` of the seven
    kinds), `importance`, `schedule_id` (FK `schedules`), `team_id` (FK `teams`), `everyone` (the four names),
    `call_target`, `gateway_connection_id` (FK `connections`), `minutes`, `destination_id` (FK `destinations`),
    `repeat_from`, `repeat_times`, `working_hours_only boolean NOT NULL DEFAULT false`, `severity_condition` (the CHECK
    of S-121); a CHECK that each kind has exactly its fields; unique `(chain_id, position)`; indexes on `schedule_id`,
    `team_id`, `gateway_connection_id` and `destination_id`.
  - `escalation_step_users`: `org_id`, `step_id` (FK, cascade), `position`, `user_id` (FK `users`); primary key
    `(step_id, position)`.
  - `route_escalation_chains` and `team_escalation_chains`: `org_id`, `route_id` or `team_id`, `chain_id`,
    `severity_condition`, `added_at`; primary keys `(route_id, chain_id)` and `(team_id, chain_id)`; index `(org_id,
    chain_id)`.
  - `permissions` gains the two Permissions and `role_permissions` their allocation — reference data, as `0029` does.
    The down migration drops all of it.
- **Escalation** (`internal/escalation`, a flat domain package, ADR-0016): `chains.go` — create, read, update, delete
  with optimistic locking, the Audit log, the hint `escalation-chain`, `referrers` and `destinations`, the refusal
  `chain_in_use` in the transaction of the delete; `steps.go` — the kinds, their fields and their validation within
  `escalation.limits`, the people a save adds checked against the current state (Viewer, disabled), the gateway of a
  `call` through `gateways` (S-167), the mode of a `webhook` step's Destination, and `WorkingHoursOf(chain)` through
  `teams.Effective` (S-115); `warnings.go` — the warnings of the read, the abilities of each Destination's Connection
  read from `mentions` (S-140, C-28.FR-2). `muster_escalation_chain_info` is exported and refreshed as the Routes'
  info is.
- **Routing and Teams**: `chain_entries` in the inputs, views and diffs of Routes and Teams; `deleteTeam` refuses a Team
  that owns a chain with `team_in_use`, naming it (P-64 holds); a deleted Team's `team_escalation_chains` rows go with
  it.
- **Web test fixtures** (`web/src/test/fixtures/teams.ts`, S-119): the builders of Team reads set `chain_entries` and
  `chains` to `[]`, so that `make generate` keeps the TypeScript check of the web tests green.
- **Schedules** (`internal/schedules/schedules.go`): `Referrers` lists the chains whose steps name the Schedule, so
  `deleteSchedule` answers `schedule_in_use` (S-131).
- **Metric** (`internal/metrics/catalogue.go`): `muster_escalation_chain_info{chain,name}` (gauge, C-32), moved in
  reference.md from "Metrics added in L2".
- **Shared tables**: reference.md moves `escalation-chains:read` and `escalation-chains:write` from "Permissions added
  in L2" into the main table and the `Permission` schema; defaults.md marks `escalation.limits` decided; L2.md §5.3
  moves P-84 to "Decided since they were first listed".
- **Settings**: `escalation.limits` (P-84, confirmed here), `team.working_hours`, `organization.working_hours`.
- **Log events**: none new; the Audit log records every change.
- **Documentation**: the new page `docs/concepts/escalation-chains.md` — "What an Escalation chain is", "Steps" and
  "Naming chains on Routes and Teams" — under Concepts in `mkdocs.yml`; S-169, S-155, S-168 and S-161 continue it.

## Steps

1. Write the migration and `schema.md`. Check: `make test-integration` runs up, down and up; the Permissions of each
   Role read from `role_permissions`.
2. Write the chains, their steps and validation, with the spec. Check: `make generate-check`; `steps_test.go` covers
   every kind, its fields, the limits, the people, the gateway and the webhook Destination.
3. Name chains on Routes and Teams; add the refusals. Check: `routes_test.go`, `teams_test.go`, `chains_test.go`,
   `schedules_integration_test.go`.
4. Write the warnings. Check: `warnings_test.go` with a Telegram and a Mattermost Destination on one chain.
5. Move the shared rows, confirm P-84, write the documentation and `test/e2e/escalation_chains_test.go`. Check: the
   Verification below; `make docs-check`.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-121 and S-131: A; the Responder R and the Viewer V tokens; the Teams "devops" (TD) and "payments" (TP);
# payments' Schedules "Main" (SM) and "Second line" (SL); devops' "Main" (DM); the Route "Infrastructure" (RI); the
# Viewer vic; the demo gateway GW (S-145); the Team "checkout" (TC) with no Schedule, created for the refusal below.
API=localhost:8080/api/v1
VIC=$(curl -s "${A[@]}" "$API/users?q=vic" | jq -r '.items[0].id')
STEPS="[{\"kind\":\"page_on_call\",\"schedule_id\":\"$SM\",\"importance\":\"normal\"},{\"kind\":\"wait\",\"minutes\":10},
 {\"kind\":\"page_on_call\",\"schedule_id\":\"$SM\",\"importance\":\"important\",\"severity_condition\":\"critical\"},{\"kind\":\"wait\",\"minutes\":15},
 {\"kind\":\"page_on_call\",\"schedule_id\":\"$SL\",\"importance\":\"important\",\"severity_condition\":\"critical\"},
 {\"kind\":\"page_team\",\"working_hours_only\":true},{\"kind\":\"repeat\",\"repeat_from\":3,\"repeat_times\":2}]"
CP=$(curl -s "${A[@]}" $API/escalation-chains -d "{\"name\":\"payments\",\"owning_team_id\":\"$TP\",\"steps\":$STEPS}" | jq -r .id)
curl -s "${A[@]}" $API/escalation-chains/$CP | jq -c '{n: [.steps[].number], team: .owning_team.slug}'   # {"n":[1,2,3,4,5,6,7],"team":"payments"}

# a Viewer in a step; the limits; a call step with a gateway
curl -s "${A[@]}" $API/escalation-chains -d "{\"name\":\"x\",\"steps\":[{\"kind\":\"page_people\",\"user_ids\":[\"$VIC\"],\"importance\":\"normal\"}]}" |
  jq -c '{status, e: .errors[0] | {pointer, code, reason}}'
# {"status":422,"e":{"pointer":"/steps/0/user_ids/0","code":"not_pageable","reason":"viewer"}}
curl -s "${A[@]}" $API/escalation-chains -d '{"name":"y","steps":[{"kind":"wait","minutes":1441}]}' | jq -c '[.status, .errors[0].code]'   # [422,"out_of_range"]

# naming the chain on the Team and devops' chain on the Route; deleting what is named
TE=$(curl -s "${A[@]}" $API/teams/$TP | jq -r .etag)
curl -s "${A[@]}" -X PUT -H "If-Match: $TE" $API/teams/$TP -d "$(curl -s "${A[@]}" $API/teams/$TP |
  jq -c "{name, slug, own_working_hours, matchers, idp_entries, destination_entries: [.destination_entries[]?], chain_entries: [{chain_id: \"$CP\"}]}")" > /dev/null
curl -s "${A[@]}" $API/escalation-chains/$CP | jq -c '{t: [.referrers.teams[].team.slug], d: [.destinations[].name]}'
# {"t":["payments"],"d":["payments-critical","payments-notice"]}
curl -s "${A[@]}" -X DELETE $API/escalation-chains/$CP | jq -c '{status, code, by: [.in_use_by[].name]}'  # {"status":409,"code":"chain_in_use","by":["payments"]}
curl -s "${A[@]}" -X DELETE $API/schedules/$SL | jq -c '{status, code, by: [.in_use_by[].type]}'        # {"status":409,"code":"schedule_in_use","by":["escalation_chain"]}
CC=$(curl -s "${A[@]}" $API/escalation-chains -d "{\"name\":\"checkout\",\"owning_team_id\":\"$TC\",\"steps\":[{\"kind\":\"wait\",\"minutes\":5}]}" | jq -r .id)
curl -s "${A[@]}" -X DELETE $API/teams/$TC | jq -c '{code, by: [.in_use_by[] | .type]}'          # {"code":"team_in_use","by":["escalation_chain"]}
curl -s -o /dev/null -w '%{http_code}\n' "${R[@]}" -X DELETE $API/escalation-chains/$CP              # 403
curl -s "${V[@]}" "$API/escalation-chains?team=$TP" | jq -r '.items[0].name'                        # payments
curl -s localhost:8082/metrics | grep -c '^muster_escalation_chain_info{'                            # 1
```

`test/e2e/escalation_chains_test.go` repeats these checks and adds a `call` step with no gateway (`gateway_required`)
and with two, a `webhook` step naming a Mattermost Destination (`webhook_events_required`), a chain named by a Route
with `critical`, a Telegram Destination of the Team that makes `everyone_unsupported` on step 6, a stale `If-Match`, and
a person disabled after the save, which a rename of the chain still saves.

## Open questions

1. **A `webhook` step whose Destination is deleted.** Settled by the maintainer: `deleteDestination` stays always
   allowed, the step is skipped with the reason `destination_deleted` (S-156), and the chain warns.
2. **People a step names who have since become Viewers or been disabled.** Settled by C-32.FR-14, which skips at run
   time a person who has become a Viewer or been disabled since the step was saved: a save validates only the people it
   adds, as S-131 does for Rotation layers.

## Notes

- Suggested commit: `feat(escalation): add escalation chains with their steps and name them on routes and teams`.
- The maintainer confirms a provisional value with this story (operator attention): P-84, at most 20 steps, waits of 1
  to 1,440 minutes and repeats up to 10 times in a chain.
- No chain runs yet: until S-169 a chain is configuration only, so "starts no Escalation chain" of C-25.AC-13 and
  C-26.FR-9 keeps holding.
- `route.run_team_chains` (S-121) starts to act in S-169.
- Migration `0040` follows the story map; a story merged out of that order renumbers its migration (S-167's Notes).

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-32.FR-1 | partial | the API; the chain page is S-158 |
| C-32.FR-2 | partial | the API; the forms are S-158 |
| C-32.FR-3 | partial | the kinds and their checks; running them is S-169, S-155, S-156 and S-157, the form S-158 |
| C-32.FR-17 | partial | the chain's warnings; the Route's warning and the form are S-158 |
| C-32.FR-19 | partial | locking, the Audit log and `muster_escalation_chain_info`; Pages and their metric are S-155 |
| C-32.AC-15 | partial | `403`, `chain_in_use`, the Viewer refused; the step skipped later is S-169 |
| C-30.FR-9 | partial | a call step needs a gateway; the skip of a deleted one is S-157, the form S-158 |
| C-25.FR-1 | partial | the Escalation chains of a Team |
| C-25.FR-2 | partial | the owning Team of chains and the filter of their list; the page is S-158 |
| C-25.FR-9 | partial | the Working hours that steps use; skipping outside them is S-169 |
| C-25.FR-10 | partial | the refusal `team_in_use` for chains |
| C-27.FR-12 | partial | `schedule_in_use` raised for chains; P-66 is S-131 |
| C-27.AC-8 | partial | `schedule_in_use`; the main Schedule and the gap alert are S-131 and S-133 |
| C-08.FR-1 | partial | revised for L2: the Escalation chains of a Route |
