---
id: S-163
title: Values of the Team label without a Team, and the evaluation of open Alert Groups when a Team or the Team label is saved (BE)
capability: C-25
kind: be
layer: L2
depends_on: [S-118, S-111]
covers: [C-25.FR-5, C-25.FR-7, C-25.FR-13, C-25.AC-1, C-25.AC-13]
files_touched:
  - api/README.md
  - api/openapi.yaml
  - internal/db/migrations/0026_team_evaluation.up.sql
  - internal/db/migrations/0026_team_evaluation.down.sql
  - design/db/schema.md
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - internal/teams/labelvalues.go
  - internal/teams/labelvalues_test.go
  - internal/teams/teams.go
  - internal/teams/query.sql
  - internal/teams/teams_integration_test.go
  - internal/api/teams.go
  - internal/api/teams_test.go
  - internal/organization/organization.go
  - internal/organization/organization_test.go
  - internal/groups/updates.go
  - internal/groups/updates_test.go
  - internal/groups/teams.go
  - internal/groups/teams_test.go
  - internal/groups/query.sql
  - test/e2e/team_evaluation_test.go
  - docs/concepts/teams.md
acceptance:
  - "[C-25.FR-7, C-25.AC-1] `listTeamLabelValues` (`teams:read`) lists the values of the Organization's Team label that Alerts last seen within `organization.team_values_window` carried, with their Static labels, and that name no Team that is not deleted — each with when it was last seen, how many Alerts carried it and whether it is a valid slug (`team.slug_format`); with the Team \"payments\", Alerts with `team=\"checkout\"` and `team=\"Payments\"` make it list `checkout` (a valid slug) and `Payments` (not one), not `payments` (`labelvalues_test.go`)."
  - "[C-25.FR-13, C-25.FR-5] Creating a Team, changing a Team's slug or matchers, and changing the Organization's Team label write, in the transaction of the save, one `open_group_updates` row of the kind `team_evaluation` (with the Team, or none for the Team label); a worker on any replica claims it with a lease and evaluates the firing Alerts of every open Alert Group again, adding to `alert_teams` the Teams they now belong to and, through the dispatcher, the Teams that join each Alert Group with `team_joined`; a crash after the save loses nothing, and a second run of the row adds nothing (`updates_test.go`, `teams_test.go`)."
  - "[C-25.FR-13, C-25.AC-13] With an open, firing Alert Group of `team=\"checkout\"` and no such Team, creating the Team \"checkout\" adds it to the Alert Group with `team_joined`, re-renders its Root messages Quietly and records no other event; the value `checkout` leaves `listTeamLabelValues`; changing a Team matcher so that it no longer picks an Alert leaves the Team in its Alert Group and in `alert_teams` (`team_evaluation_test.go`)."
  - "[C-25.FR-13] Saving a Team without a change of slug or matchers — a new name, description, people or Working hours — writes no evaluation row (`teams_test.go`)."
  - "Concepts → Teams says that saving a Team or the Team label evaluates the open Alert Groups again and only adds, with \"Create Team\" from a value without a Team as the example; `make docs-check` passes."
verify: "make ci test-integration e2e-go docs-check"
operator_attention: false
issue: 452
---

# S-163. Values of the Team label without a Team, and the evaluation of open Alert Groups when a Team or the Team label is saved (BE)

## Scope

**IN**

- `team-label-values`: the values of the Team label seen without a Team over `organization.team_values_window`
  (C-25.FR-7).
- The evaluation on save of C-25.FR-13: a durable `open_group_updates` row of the kind `team_evaluation`, claimed by any
  replica with a lease, that evaluates the firing Alerts of open Alert Groups again and only adds Teams.

**OUT**

- The Destinations that a Team brings, which receive the Alert Group Quietly (the second half of C-25.FR-13 and of
  C-25.AC-13): S-121.
- Escalation chains, which the evaluation never starts: S-169 keeps it so.
- The Teams page with "Unknown values of {team label}" and "Create Team": S-120.

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated): `listTeamLabelValues` (`GET /team-label-values`, tag
  Teams, `teams:read`, session or token, `x-listener: app`) → `TeamLabelValueList` {`label` (the Team label),
  `window_days`, `items` — `TeamLabelValue` {`value`, `last_seen_at`, `alert_count`, `valid_slug`}}, the most recently
  seen first, at most 500, like `listSeverityValues`. `window_days` keeps the name and unit of L1's
  `SeverityValueList.window_days`, which it mirrors (plan review, Q4): the window is a whole number of days, as the
  retention periods of `api/README.md`'s exception are, and a second spelling for the same window in two sibling lists
  would be worse; this story widens that exception's wording in `api/README.md` to "retention periods and the windows
  of value lists".
- **Schema** (migration `0026_team_evaluation`, expand only; `schema.md` §4.15 and §5): `open_group_updates.team_id`
  (nullable foreign key to `teams`) and `open_group_updates_kind_v3_check` with every kind of `_v2` and
  `team_evaluation`, `NOT VALID` and validated in a second transaction. The values are read from `alerts` through
  `alerts_org_last_seen_idx`, as the severity values are; no table is added for them.
- **Teams** (`internal/teams`): `labelvalues.go` — the values, their last time seen, their count and `valid_slug`,
  leaving out the slugs of Teams that are not deleted. `createTeam`, and `updateTeam` when the slug or the matchers
  change, insert the `team_evaluation` row with the Team in the save's transaction.
- **Organization**: a changed `team_label` inserts the `team_evaluation` row without a Team.
- **Groups** (`updates.go`, `teams.go`): the handler of `team_evaluation` walks the open Alert Groups by id from the
  row's cursor in batches of 50; for each, in one dispatcher transaction (system transition `TeamsEvaluated`, actor
  Muster, Transport `system`), it classifies the labels of its firing Alerts with the current classification of S-118,
  inserts the missing `alert_teams` rows (never deletes one), adds the missing Teams to `alert_group_teams` with one
  Quiet `team_joined` each, and lets the re-render step update the Root messages Quietly. It never records
  `unknown_team_value` and starts nothing. An Alert Group without a new Team is not touched, so a second run records
  nothing.
- **Settings**: `organization.team_values_window` (P-75, provisional, left to the test environment),
  `organization.team_label`, `team.slug_format`.
- **Log events, metrics**: none new; `open_group_update_done` names the kind.
- **Documentation**: Concepts → Teams — the evaluation on save, under "Values without a Team".

## Steps

1. Write the migration and `schema.md`. Check: `make test-integration` runs up, down and up.
2. Write `team-label-values`. Check: `labelvalues_test.go` covers the window, Static labels, a deleted Team's slug and
   `valid_slug`; `make generate-check`.
3. Insert the rows on save and write the handler. Check: `teams_test.go` covers which saves insert a row;
   `updates_test.go` covers a Team created for a value, a new matcher, a removed matcher, a new Team label, a resolved
   Alert left alone, two workers and a second run.
4. Write `test/e2e/team_evaluation_test.go` and the documentation. Check: the Verification below; `make docs-check`.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-118: A, the Integration "sev" with NOTIFY, PUTA, AG and GET, the Team "payments" (TP).
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake
curl -s -X PUT $FAM/groups/u1 -d '{"receiver":"sev","route":"{}","labels":{"alertname":"Latency"}}' > /dev/null
PUTA u1 a '{"labels":{"team":"checkout","severity":"warning"}}'
PUTA u1 b '{"labels":{"team":"Payments","severity":"warning","cluster":"x"}}'
NOTIFY u1 '{"reason":"first notification"}'

# C-25.AC-1: the values without a Team
curl -s "${A[@]}" $API/team-label-values | jq -c '{label, window_days, v: [.items[] | {value, valid_slug}] | sort_by(.value)}'
# {"label":"team","window_days":14,"v":[{"value":"Payments","valid_slug":false},{"value":"checkout","valid_slug":true}]}

# C-25.FR-13, C-25.AC-13: creating "checkout" brings its open Alert Group
G=$(AG 'team%3D%22checkout%22'); GET $G | jq -c '.teams'                                                   # []
TC=$(curl -s "${A[@]}" $API/teams -d '{"name":"checkout","slug":"checkout","own_working_hours":false}' | jq -r .id); sleep 1
GET $G | jq -c '[.teams[] | {s: .team.slug, rule}]'                                                         # [{"s":"checkout","rule":"label"}]
curl -s "${A[@]}" "$API/alert-groups/$G/timeline?limit=1" | jq -c '.items[0] | {event, loudness}'        # {"event":"team_joined","loudness":"quiet"}
psql "$MUSTER_DATABASE_URL" -Atc "SELECT count(*) FROM open_group_updates"                                 # 0
curl -s "${A[@]}" $API/team-label-values | jq -c '[.items[].value]'                                        # ["Payments"]

# a new slug that no longer picks the Alert leaves the Team where it is (the e2e test does the same with a matcher)
E=$(curl -s "${A[@]}" $API/teams/$TC | jq -r .etag)
curl -s "${A[@]}" -X PUT -H "If-Match: $E" $API/teams/$TC -d '{"name":"checkout","slug":"checkout-x","own_working_hours":false}' > /dev/null; sleep 1
GET $G | jq -c '[.teams[].team.slug]'                                                                      # ["checkout-x"]
grep -c '"event":"open_group_update_done".*"kind":"team_evaluation"' dev.log                               # 2
```

`test/e2e/team_evaluation_test.go` repeats these checks, adds a Team matcher `cluster="x"` that brings the second Alert
Group, changes the Team label to `service_team` and back, and kills the replica between the save and the run with
`muster dev --replica` to show that the other one runs the row.

## Open questions

None.

## Notes

- Suggested commit: `feat(teams): list unknown team label values and evaluate open alert groups on save`.
- Split from S-118 when the contracts were written: with these parts S-118 touched about 50 files. It follows S-118 in
  the story map, and S-120 and S-121 depend on it.
- The evaluation only adds (C-25.FR-13): a Team stays in an Alert Group, and on its Alerts, after a change that would no
  longer bring it, as the Teams of an Alert Group only grow (C-25.FR-6).
- Depends on S-111 for `open_group_updates`.
- The evaluation must also cover Alerts that S-118's Router gave no Teams: an Alert that routing took up late (a listed
  Alert without a Route), and one that first fired on a 0.1 replica during a rolling update.
- Migration `0026` follows the story map; a story merged out of that order renumbers its migration to the next free
  number in its own pull request and re-versions the CHECKs it replaces, listing every value present
  when it merges (S-131's Notes).

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-25.FR-5 | partial | saving re-evaluates the Teams recorded on Alerts |
| C-25.FR-7 | partial | the values in the API; the Teams page is S-120 |
| C-25.FR-13 | partial | the evaluation and `team_joined`; the Destinations it adds are S-121 |
| C-25.AC-1 | partial | `team-label-values`; the Teams of the Alert are S-118 |
| C-25.AC-13 | partial | `team_joined` without a chain, the matcher change; the Quiet publication in Team Destinations is S-121 |
