---
id: S-186
title: "The Terraform resources of Teams, Team members, a Team's Destinations and Schedules with their Rotation layers, and the data sources muster_user and muster_team (BE)"
capability: C-33
kind: be
layer: L2
repository: muster-io/terraform-provider-muster
depends_on: [S-185]
covers: [C-33.FR-5, C-33.FR-6, C-33.FR-7, C-33.FR-8, C-33.FR-9, C-33.FR-13, C-33.FR-15, C-33.FR-16, C-33.FR-17, C-33.FR-19, C-33.AC-7, C-33.AC-10, C-33.AC-11, C-33.AC-14]
files_touched:
  - internal/provider/provider.go
  - internal/provider/resource_team.go
  - internal/provider/resource_team_test.go
  - internal/provider/teamlist.go
  - internal/provider/teamlist_test.go
  - internal/provider/resource_team_destinations.go
  - internal/provider/resource_team_destinations_test.go
  - internal/provider/resource_team_member.go
  - internal/provider/resource_team_member_test.go
  - internal/provider/resource_schedule.go
  - internal/provider/resource_schedule_test.go
  - internal/provider/data_source_user.go
  - internal/provider/data_source_user_test.go
  - internal/provider/data_source_team.go
  - internal/provider/data_source_team_test.go
  - examples/resources/muster_team/resource.tf
  - examples/resources/muster_team/import.sh
  - examples/resources/muster_team_destinations/resource.tf
  - examples/resources/muster_team_destinations/import.sh
  - examples/resources/muster_team_member/resource.tf
  - examples/resources/muster_team_member/import.sh
  - examples/resources/muster_schedule/resource.tf
  - examples/resources/muster_schedule/import.sh
  - examples/data-sources/muster_user/data-source.tf
  - examples/data-sources/muster_team/data-source.tf
acceptance:
  - "[C-33.FR-5, C-33.FR-7, C-33.FR-13, C-33.FR-19] `muster_team` creates, updates and deletes a Team, each write with `managed_by` `terraform`, with its slug, description, Working hours of its own, Team matchers, identity provider entries (`claim`, `group`, subtrees `a/b/*` included) and a Team mention on the second Mattermost Connection of S-184; a second plan shows no change; `destination_entries`, `chain_entries`, `user_count`, `mention_connections` and the derived reads are not attributes, and the resource never sends `destination_entries` or `chain_entries` (`TestAccTeam_*`)."
  - "[C-33.FR-8, C-33.FR-16, C-33.AC-11] A `muster_mattermost_destination` owned by the Team (`owning_team_id`) and named in the Team's `muster_team_destinations` with the condition `critical` applies in one apply without a cycle; a Destination entry added to the Team through the API shows in the next plan and is removed by the apply; renaming the Team by `muster_team` and changing its list in the same apply keeps both, run ten times with `-parallelism=10`; destroying `muster_team_destinations` empties the list and leaves the Team (`TestAccTeamDestinations_*`)."
  - "[C-33.FR-15, C-33.AC-10] With alice added by `muster_team_member` and bob in the Team through an identity provider entry matched at his sign-in with the fake OIDC server: an apply and a destroy of alice's resource leave bob in the Team; alice, also matched by the identity provider, stays with the source `idp` after her resource is destroyed; carol, added by hand through the API, is untouched; a `muster_team_member` for a User already added by hand takes the addition over without a change; removing alice's addition through the API makes the next plan add her again (`TestAccTeamMember_*`)."
  - "[C-33.FR-5, C-33.AC-14] `muster_schedule` with two Rotation layers — weekly and daily, with people from `data.muster_user` — applies; a change of the second layer's people keeps both layers' ids; an Override created through the API stays after the next apply and shows no change; `main` makes it the Team's main Schedule; `next_gap`, `gap_alert_active` and the On-call are not attributes (`TestAccSchedule_*`)."
  - "[C-33.FR-17] Destroying a `muster_team` whose Schedule is not managed by Terraform is refused by the server, and the diagnostic carries its code (`TestAccTeam_refusedDelete`)."
  - "[C-33.FR-6] `data.muster_user` finds alice by `login`, by `email` and by `id`, answering `id`, `name`, `login`, `email`, `role` and `status`; `data.muster_team` finds a Team by `slug` and by `id`; a value that matches nothing, or more than one User, is an error that names it (`TestAccDataSourceUser`, `TestAccDataSourceTeam`)."
  - "[C-33.FR-9, C-33.AC-7] `muster_team` and `muster_schedule` import by `public_id`, `muster_team_destinations` by the Team's, and `muster_team_member` by `<team id>/<user id>`; each plans without changes after its import (`TestAcc*_import`)."
verify: "make ci testacc"
operator_attention: false
issue: 704
---

# S-186. The Terraform resources of Teams, Team members, a Team's Destinations and Schedules with their Rotation layers, and the data sources muster_user and muster_team (BE)

## Scope

**IN**

- `muster_team`, `muster_team_member`, `muster_team_destinations` with the shared read-then-write of a Team's lists in
  `teamlist.go`, which `muster_team_escalation_chains` (S-187) reuses.
- `muster_schedule` with its Rotation layers, never its Overrides.
- `data.muster_user` and `data.muster_team`.

**OUT**

- `muster_team_escalation_chains`, Escalation chains and Routes (S-187).
- Overrides, the On-call, gaps and iCal feeds: operations and personal settings (C-33.FR-5).

## Contracts

- **`muster_team`** (Team operations): `name`, `slug`, `description`, `own_working_hours`, `working_hours`,
  `matchers`, `idp_entries` (`claim`, `group`), `team_mentions` (`connection_id`, `group`); computed `id`, `etag`.
  An update sends these fields and leaves out `destination_entries` and `chain_entries`, which the API then keeps.
  Because the Team's lists write the same object, its update and delete use the shared-object path of `resourcekit`
  (S-182): the Team's lock, a fresh read, the comparison of these fields with the prior state, and the ETag of the
  read.
- **`muster_team_destinations`** (`getTeam`, `updateTeam`): `team_id` (forces a replacement), `entries` — a list of
  `destination_id` and `severity_condition` (`any` by default) — authoritative; computed `etag` for the plan only.
  `teamlist.go`: Create, Update and Delete take the Team's lock, read the Team, compare its list with the prior state
  (an Update whose list changed in Muster since the refresh gets the error of C-33.FR-8), replace only
  `destination_entries` — with an empty list on Delete — and send the ETag of that read. Read compares the Team's list
  with the state.
- **`muster_team_member`** (`addTeamUser`, `listTeamUsers`, `removeTeamUser`): `team_id`, `user_id`, both forcing a
  replacement; no `etag`. Read lists the Team's users and keeps the resource while the User has the source `by_hand`;
  Delete removes that source, and `404` means it is already gone.
- **`muster_schedule`** (Schedule operations): `team_id` (forces a replacement, since the API refuses a change),
  `name`, `time_zone`, `main`, `gap_alert`, `rotation_layers` — each `name`, `user_ids`, `shift_length`,
  `shift_seconds`, `handover_time`, `handover_day`, `start_date`, `limits`, with the computed `id` that the next update
  sends back so that a layer keeps its identity; a layer is matched to the state by its position. Computed `id`,
  `etag`. `main` false on the main Schedule is refused by the server (C-27.FR-12), as Muster gives it.
- **`data.muster_user`** (`listUsers` with `q`, then an exact match; `getUser`): one of `login`, `email`, `id`.
  **`data.muster_team`** (`listTeams`, `getTeam`): one of `slug`, `id`.

## Steps

1. Write `muster_team` and the data sources. Check: their acceptance tests.
2. Write `teamlist.go` and `muster_team_destinations`. Check: their tests, the cycle-free apply among them.
3. Write `muster_team_member` with the fake OIDC sign-in of bob. Check: its tests.
4. Write `muster_schedule`. Check: its tests, the Override among them.
5. Add the examples and the documentation. Check: `make docs-check`, `make ci testacc`.

## Verification

In a clone of the provider, with `eval "$(test/muster-dev.sh up)"` and the provider installed as in S-182; the Users
alice and bob created through the API, and bob signed in once through the fake OIDC server with the group `payments`
(`/_fake/next-user`), as in S-117's Verification:

```sh
cat > main.tf <<'EOF'
data "muster_user" "alice" { login = "alice" }
data "muster_connection" "mm" { name = "Example Mattermost" }
resource "muster_team" "payments" {
  name        = "Payments"
  slug        = "payments"
  idp_entries = [{ claim = "groups", group = "payments" }]
}
resource "muster_team_member" "alice" {
  team_id = muster_team.payments.id
  user_id = data.muster_user.alice.id
}
resource "muster_mattermost_destination" "payments" {
  name           = "payments-critical"
  connection_id  = data.muster_connection.mm.id
  team_id        = "team-1"
  channel_id     = "ch-payments-critical"
  owning_team_id = muster_team.payments.id
}
resource "muster_team_destinations" "payments" {
  team_id = muster_team.payments.id
  entries = [{ destination_id = muster_mattermost_destination.payments.id, severity_condition = "critical" }]
}
resource "muster_schedule" "main" {
  team_id   = muster_team.payments.id
  name      = "Main"
  time_zone = "Europe/Berlin"
  rotation_layers = [{
    name = "Weekly", user_ids = [data.muster_user.alice.id], shift_length = "week",
    handover_time = "10:00", handover_day = "monday", start_date = "2026-11-23"
  }]
}
EOF
terraform apply -auto-approve && terraform plan -detailed-exitcode; echo "exit=$?"                     # exit=0
A=(-H "Authorization: Bearer $MUSTER_TOKEN"); API=$MUSTER_ENDPOINT/api/v1
T=$(terraform state show -no-color muster_team.payments | awk '$1=="id"{print $3}' | tr -d '"')
curl -s "${A[@]}" $API/teams/$T/users | jq -c '[.items[] | {l: .user.login, s: .sources}]'
# [{"l":"alice","s":["by_hand"]},{"l":"bob","s":["idp"]}]
terraform destroy -target=muster_team_member.alice -auto-approve > /dev/null
curl -s "${A[@]}" $API/teams/$T/users | jq -c '[.items[].user.login]'                                  # ["bob"]
curl -s "${A[@]}" $API/teams/$T | jq -c '{d: [.destination_entries[] | .severity_condition], m: .managed_by}'
# {"d":["critical"],"m":"terraform"}
```

The Mattermost team id is the fake server's, as in S-185. The pull request carries this output and the acceptance run.

## Open questions

None.

## Notes

- Suggested commit: `feat: manage teams, team members, team destinations and schedules`.
- A Team's Destinations are a resource of their own because a Destination owned by the Team and the Team's list of it
  would otherwise reference each other (C-33.FR-16); the same holds for its Escalation chains in S-187. The maintainer
  chose these separate resources over leaving `owning_team_id` out of the provider.
- Membership is never authoritative (journal D757): the identity provider and people added by hand in the web UI stay
  as they are.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-33.FR-5 | partial | these resources |
| C-33.FR-6 | partial | `muster_user` and `muster_team`; with S-184 complete |
| C-33.FR-7 | partial | these resources |
| C-33.FR-8 | partial | the read-then-write of a Team's lists |
| C-33.FR-9 | partial | these resources |
| C-33.FR-13 | partial | these resources |
| C-33.FR-15 | full | |
| C-33.FR-16 | partial | `muster_team_destinations`; the chains and the Route order in S-187 |
| C-33.FR-17 | partial | the refused delete of a Team |
| C-33.FR-19 | partial | these resources' acceptance tests |
| C-33.AC-7 | partial | these resources |
| C-33.AC-10 | full | |
| C-33.AC-11 | partial | the Team's Destinations; the Route order and the chains in S-187 |
| C-33.AC-14 | partial | the Schedule and the data sources; the chain and the Route in S-187 |
