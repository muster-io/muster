---
id: S-118
title: The Team label and Team matchers, the Teams of Alerts and Alert Groups with team_joined, and unknown values (BE)
capability: C-25
kind: be
layer: L2
depends_on: [S-115]
covers: [C-25.FR-1, C-25.FR-2, C-25.FR-5, C-25.FR-6, C-25.FR-7, C-25.FR-10, C-25.FR-11, C-25.FR-12, C-25.AC-1, C-25.AC-2, C-25.AC-5, C-25.AC-9, C-25.AC-11, C-06.FR-19, C-09.FR-1, C-09.FR-11, C-09.FR-13, C-09.FR-22, C-11.FR-20, C-15.FR-2, C-20.FR-1]
files_touched:
  - api/openapi.yaml
  - internal/db/migrations/0025_team_label_and_matchers.up.sql
  - internal/db/migrations/0025_team_label_and_matchers.down.sql
  - design/db/schema.md
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - internal/teams/teams.go
  - internal/teams/classify.go
  - internal/teams/classify_test.go
  - internal/teams/query.sql
  - internal/teams/teams_integration_test.go
  - internal/api/teams.go
  - internal/api/teams_test.go
  - internal/routing/evaluate.go
  - internal/routing/evaluate_test.go
  - internal/routing/query.sql
  - internal/groups/grouping.go
  - internal/groups/grouping_test.go
  - internal/groups/teams.go
  - internal/groups/teams_test.go
  - internal/groups/events.go
  - internal/groups/events_test.go
  - internal/groups/filters.go
  - internal/groups/read.go
  - internal/groups/query.sql
  - internal/api/alertgroups.go
  - internal/api/alertgroups_test.go
  - internal/ingest/alertsview.go
  - internal/ingest/query.sql
  - internal/delivery/enqueue.go
  - internal/delivery/enqueue_test.go
  - internal/webhooks/body.go
  - internal/webhooks/body_test.go
  - internal/organization/organization.go
  - internal/organization/settings.go
  - internal/organization/query.sql
  - internal/organization/organization_test.go
  - internal/api/organization.go
  - internal/internalalerts/registry.go
  - internal/archlint/archlint.go
  - AGENTS.md
  - web/src/test/fixtures/alert-groups.ts
  - test/e2e/team_label_test.go
  - docs/concepts/teams.md
  - docs/outgoing-webhooks/events.md
acceptance:
  - "[C-25.FR-5, C-20.FR-1] `getOrganization` carries `team_label` `team`; `updateOrganization` changes it, recorded in the diff of `organization.updated`, and refuses with `422` `reserved_label` at `/team_label` the name of a label that an Internal alert sets itself — `alertname`, `severity` and the entity and name labels of the registry, `schedule` and `team_id` among them (`organization_test.go`)."
  - "[C-25.FR-1, C-25.FR-5, C-25.FR-11] A Team takes `matchers` in the syntax of Route Matchers, saved with `If-Match` and recorded in the diff of `team.updated`; an invalid regular expression is refused with `422` `invalid_regex` at its pointer (`teams_test.go`)."
  - "[C-25.FR-5, C-25.AC-1, C-25.AC-2] When an Alert starts firing, Muster records on it the Teams it belongs to: by the Team label, with the Static labels of its Integration, equal to a Team's slug exactly, or by any one of a Team's matchers, with the rule — `label`, or `matcher` with its text; with the Team \"payments\" a newly firing Alert with `team=\"payments\"` belongs to it by the label and `team=\"Payments\"` to no Team; a Team with `namespace=~\"checkout|billing\"` and `pod=~\"checkout-.*\"` gets an Alert with `namespace=\"billing\"` and one with `pod=\"checkout-api-1\"`, each with its matcher, and not one with `namespace=\"search\"`; an Alert that the slug of one Team and a matcher of another pick belongs to both (`classify_test.go`, `evaluate_test.go`, `team_label_test.go`)."
  - "[C-25.FR-6, C-25.AC-5, C-09.FR-1, C-09.FR-22, C-11.FR-20] The Teams of an Alert Group are those of all the Alerts that ever joined it and only grow: each Team that joins records the Quiet lifecycle event `team_joined` (Timeline kind `alerts`) naming the Team and the rule; an Alert Group of \"payments\" later joined by an Alert of \"search\" lists both with two `team_joined` entries, and still lists both after the Alert of \"search\" resolves (`teams_test.go`, `team_label_test.go`)."
  - "[C-25.FR-7, C-25.AC-1] A value of the Team label that names no Team adds nothing and records the Quiet `unknown_team_value` (Timeline kind `system`) with the value, once per value per Alert Group; the Alert Group goes to its Route's Destinations as before (`teams_test.go`)."
  - "[C-25.FR-6, C-06.FR-19, C-09.FR-11, C-09.FR-13] `getAlertGroup` and `listAlertGroups` carry `teams` — each with the Team, the rule, the matcher and when it joined, a deleted Team marked `deleted` with its name; `listAlertGroupAlerts` and `listIntegrationAlerts` carry the `teams` of each Alert; `listAlertGroups` and `getAlertGroupCounts` take `team` (a Team's `public_id` or `mine`) and match the Alert Groups that have it among their Teams (`alertgroups_test.go`)."
  - "[C-25.FR-2, C-25.AC-11] With `team=mine`, a User in \"payments\" and not in \"search\" lists the Alert Groups that have \"payments\" among their Teams and not those of \"search\"."
  - "[C-25.FR-10, C-25.AC-9] After a Team is deleted, an Alert Group that had it among its Teams still lists it by name, with `deleted` true, and its slug no longer gives Alerts that Team (`teams_integration_test.go`)."
  - "[C-15.FR-2, C-09.FR-22] Outgoing webhooks in events mode receive `team_joined` and `unknown_team_value` one to one — `webhook_events_event_v2_check` accepts both — and the `alert_group` of every event carries `teams` with the id, name and slug of each Team (`body_test.go`, `db_test.go`)."
  - "[C-25.FR-12] Concepts → Teams has the sections \"Which Alerts belong to a Team\" — the Team label, Team matchers on any label, test clusters with one shared namespace — and \"Values without a Team\", and the page on outgoing webhook events names the new events and `teams`; `make docs-check` passes."
verify: "make ci test-integration e2e-go docs-check"
operator_attention: false
issue: 407
---

# S-118. The Team label and Team matchers, the Teams of Alerts and Alert Groups with team_joined, and unknown values (BE)

## Scope

**IN**

- The Organization's Team label (`organization.team_label`) with its refusal of the labels Internal alerts set
  (C-25.FR-5, C-20.FR-1 revised), and Team matchers.
- The Teams of an Alert, decided when it starts firing and recorded on it with the rule that matched, and shown in the
  Alerts view (C-25.FR-5, C-06.FR-19 revised).
- The Teams of an Alert Group, which only grow, with the lifecycle event `team_joined`, and the event
  `unknown_team_value` for a value that names no Team (C-25.FR-6, FR-7, C-09.FR-22 revised).
- The Teams in the reads of Alert Groups, the filter by Team and "My Teams" of the list and counts, and the Teams in
  outgoing webhook events (C-09.FR-13, C-15.FR-2 revised).
- A deleted Team kept by name in the Alert Groups that had it (C-25.FR-10).

**OUT**

- The list of values without a Team (`team-label-values`) and the evaluation of open Alert Groups when a Team or the
  Team label is saved: S-163.
- The Destinations of a Team and delivery to the Teams of an Alert Group, which `team_joined` then publishes in: S-121.
- The Teams on the pages: S-120. The field "Teams" of the Root message: S-125.
- The Team's slug under the Team label on Internal alerts about a Team's object: S-133, with `MusterScheduleGap`.

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated):
  - `OrganizationBase.team_label` (optional, omitted keeps; default `team`); `Organization` requires it. New problem
    code `reserved_label` (`validation-failed`, in `errors[].code`).
  - `TeamInput` gains `matchers` (`Matcher`[]).
  - New schemas `TeamMembership` {`team` (`TeamRef`), `rule` (`label`, `matcher`), `matcher` (string or null)} and
    `AlertGroupTeam` = `TeamMembership` + `joined_at`. `AlertGroup` gains `teams` (`AlertGroupTeam`[], required);
    `AlertGroupAlert` and `IntegrationAlert` gain `teams` (`TeamMembership`[]).
  - The parameter `AgTeam` (`team`: a Team's `public_id`, or `mine` for the Teams of the calling User, `422` for a
    Service account token) on `listAlertGroups` and `getAlertGroupCounts`.
  - `LifecycleEvent` gains `team_joined` (`x-event-kinds` `alerts`) and `unknown_team_value` (`system`);
    `WebhookEventName` gains both; `TimelineAlertsEntry` gains `team` (`TeamRef`), `team_rule` and `team_matcher`;
    `TimelineSystemEntry` gains `team_value`; `WebhookAlertGroup` gains `teams` ({`id`, `name`, `slug`}[]).
- **Schema** (migration `0025_team_label_and_matchers`, expand only; `schema.md` §4.2, §4.7, §4.9, §4.15):
  - `organizations.team_label text NOT NULL DEFAULT 'team'` and `organizations.teams_version bigint NOT NULL DEFAULT
    0`, which every change of a Team or of the Team label increments, so that each replica's cached classification
    (below) sees the change in the Snapshot's own transaction, as the Routes' stamp does.
  - `team_matchers`: `org_id`, `team_id`, `label`, `op` (`=`, `!=`, `=~`, `!~`), `value`, `position`; replaced as a
    whole.
  - `alert_teams`: `org_id`, `alert_id`, `team_id`, `rule` (`label`, `matcher`), `matcher`; primary key
    `(alert_id, team_id)`; written by routing when the Alert starts firing (replaced at each new firing) and, from
    S-163, added to by the evaluation; `alerts.unknown_team_value text`, the value of the Team label that named no Team
    at that firing.
  - `alert_group_teams`: `org_id`, `alert_group_id`, `team_id`, `rule`, `matcher`, `joined_at`; primary key
    `(alert_group_id, team_id)`; index `(org_id, team_id, alert_group_id)` for the filter; written only by `groups`;
    `alert_groups.unknown_team_values text[] NOT NULL DEFAULT '{}'` for "once per value".
  - `timeline_entries` gains `team_id`, `team_rule`, `team_matcher` and `team_value`; its CHECKs of events, event kinds,
    system events and the system rule are replaced by `_v2` ones with `team_joined` (kind `alerts`) and
    `unknown_team_value` (kind `system`, with the same `system_event`), each `NOT VALID` and validated in a second
    transaction, the old ones dropped in a third, as `0018` does for its transport CHECKs.
  - `webhook_events`: the CHECK of `event` from `0001` is replaced by `webhook_events_event_v2_check` with
    `team_joined` and `unknown_team_value`, in the same three transactions, so that events mode can store them; the
    down migration restores it after deleting the rows of the two events.
- **Teams** (`internal/teams`): the matchers in the Team's input, view and diff, compiled with `internal/matchers`;
  `classify.go` — the Organization's Team label, the slugs of the Teams that are not deleted and their matchers,
  compiled and cached per replica under `teams_version` — answers for a set of labels the Teams with their rules and
  the value of the Team label that names no Team.
- **Routing** (`internal/routing/evaluate.go`): the Router, which already routes each newly firing Alert, also
  classifies it and writes `alert_teams` and `alerts.unknown_team_value` in the Snapshot's transaction. "Starts
  firing" (C-25.FR-5) is a fired change of Snapshot processing: the first firing of an Alert and each firing after a
  resolve, which starts a new episode. A Continuation is not a new firing (C-06), so it decides no Teams, and neither
  `alert_continued` nor `annotations_changed` adds a Team to an Alert Group; Teams created or changed meanwhile reach
  such Alerts only through the evaluation on save of S-163.
- **Groups** (`internal/groups/teams.go`, `grouping.go`): when Alerts join an Alert Group — `created`, `alerts_added`,
  `reopened`, a Replacement — the dispatcher adds the Teams of the joining Alerts that the Alert Group lacks to
  `alert_group_teams` and records one Quiet `team_joined` per Team, and one Quiet `unknown_team_value` per value not yet
  in `unknown_team_values`; both are rows of the closed table of `events.go` and of delivery's `Table` (`team_joined`: a
  Root message update; `unknown_team_value`: nothing) — `Enqueue` refuses an event without a row, and `enqueue_test.go`
  counts the rows of `Table`, so both change with them; `TestLifecycleEventTable` of `events_test.go` fails on a row of
  `events.go` without its case, so it gains both events. The list, counts and reads join `alert_group_teams` and `teams`
  (`filters.go`, `read.go`, `query.sql`); "My Teams" is the caller's `team_users`. The table joins the list of lint 2
  (`internal/archlint/archlint.go`, AGENTS.md).
- **Alerts view** (`internal/ingest/alertsview.go`): the Teams of each Alert from `alert_teams`.
- **Web test fixtures** (`web/src/test/fixtures/alert-groups.ts`, S-166): the builders of Alert Group and Alert reads
  set `teams` to `[]`, so that `make generate` keeps the TypeScript check of the web tests green.
- **Outgoing webhooks** (`internal/webhooks/body.go`): `alert_group.teams`; the two events one to one in events mode.
- **Organization**: `team_label` in the read, update, defaults and diff; the reserved names come from
  `internalalerts.ReservedLabels()` — `alertname`, `severity` and every entity and name label of the registry, with
  those of the L2 Internal alerts (`schedule`, `schedule_name`, `team_id`, `team_name`, `connection`,
  `connection_name`) — so that a later Internal alert extends it.
- **Settings**: `organization.team_label`, `team.slug_format`, `organization.instance_labels` (unchanged).
- **Log events, metrics**: none new.
- **Documentation**: `docs/concepts/teams.md` — "Which Alerts belong to a Team" and "Values without a Team";
  `docs/outgoing-webhooks/events.md` — the event names and `teams`.

## Steps

1. Write the migration and `schema.md`. Check: `make test-integration` runs up, down and up; the `_v2` CHECKs accept
   every event of 0024 and the two new ones.
2. Write the Team label, the matchers and the classification. Check: `organization_test.go`; `classify_test.go` covers
   the exact slug, Static labels, each operator, several Teams, a deleted Team and the cache across `teams_version`.
3. Record the Teams of Alerts in routing and of Alert Groups in the dispatcher. Check: `evaluate_test.go`,
   `teams_test.go`, `grouping_test.go` cover a Replacement, a Reopen, a resolved Alert and "once per value".
4. Add the reads, filters, the Alerts view, outgoing webhooks and lint 2. Check: `make generate-check`;
   `alertgroups_test.go`, `body_test.go`; `make lint-arch`.
5. Write `test/e2e/team_label_test.go` and the documentation. Check: the Verification below; `make docs-check`.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-115: the Admin's Personal access token A and bob's token B; the Integration "sev" (its id in INT) with its fake
# receiver "sev", NOTIFY, PUTA, AG, GET as in S-111; the Default route takes every Alert; an Alert is resolved by
# removing it from its fake Alertmanager group, as in S-020.
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake
TEAM() { curl -s "${A[@]}" $API/teams -d "$1" | jq -r .id; }
TP=$(TEAM '{"name":"payments","slug":"payments","own_working_hours":false}')
TC=$(TEAM '{"name":"checkout","slug":"checkout-team","own_working_hours":false,"matchers":[
  {"label":"namespace","op":"=~","value":"checkout|billing"},{"label":"pod","op":"=~","value":"checkout-.*"}]}')
TS=$(TEAM '{"name":"search","slug":"search","own_working_hours":false}')
BOB=$(curl -s "${A[@]}" "$API/users?q=bob" | jq -r '.items[0].id'); curl -s "${A[@]}" $API/teams/$TP/users -d "{\"user_id\":\"$BOB\"}" > /dev/null

# C-25.AC-1, C-25.AC-2: by label, unknown value, by matcher, two Teams
curl -s -X PUT $FAM/groups/k1 -d '{"receiver":"sev","route":"{}","labels":{"alertname":"OOMKilled"}}' > /dev/null
PUTA k1 a '{"labels":{"team":"payments","namespace":"billing","severity":"warning"}}'
PUTA k1 b '{"labels":{"team":"Payments","pod":"x-1","severity":"warning"}}'
PUTA k1 c '{"labels":{"namespace":"search","severity":"warning"}}'
NOTIFY k1 '{"reason":"first notification"}'
curl -s "${A[@]}" "$API/integrations/$INT/alerts?label=team%3D%22payments%22" | jq -c '[.items[0].teams[] | {s: .team.slug, rule, matcher}] | sort_by(.s)'
# [{"s":"checkout-team","rule":"matcher","matcher":"namespace=~\"checkout|billing\""},{"s":"payments","rule":"label","matcher":null}]
curl -s "${A[@]}" "$API/integrations/$INT/alerts?label=namespace%3D%22search%22" | jq -c '.items[0].teams'   # []
G=$(AG 'team%3D%22payments%22')
curl -s "${A[@]}" "$API/alert-groups/$G/timeline?limit=10" | jq -c '[.items[] | select(.event == "team_joined" or .event == "unknown_team_value") | {event, loudness, t: .team.slug, v: .team_value}]
  | sort_by(.event, .t // .v)'
# sorted with | sort_by(.event, .t // .v):
# [{"event":"team_joined","loudness":"quiet","t":"checkout-team","v":null},{"event":"team_joined","loudness":"quiet","t":"payments","v":null},
#  {"event":"unknown_team_value","loudness":"quiet","t":null,"v":"Payments"}]

# C-25.AC-5: a Team joins later and stays after its Alerts resolve
PUTA k1 d '{"labels":{"team":"search","severity":"warning"}}'; NOTIFY k1 '{"reason":"new alerts"}'
GET $G | jq -c '[.teams[].team.slug] | sort'                                       # ["checkout-team","payments","search"]
curl -s -X DELETE $FAM/groups/k1/alerts/d > /dev/null; NOTIFY k1 '{"reason":"resolved"}'
GET $G | jq -c '[.teams[].team.slug] | sort'                                       # ["checkout-team","payments","search"]

# C-25.AC-11: "My Teams"
curl -s "${B[@]}" "$API/alert-groups?team=mine" | jq -c '[.items[].id] | index("'$G'") != null'                 # true
curl -s "${A[@]}" "$API/alert-groups?team=$TS" | jq -r '.items | length'                                         # 1

# C-25.FR-5: the Team label may not be a label of an Internal alert
O=$(curl -s "${A[@]}" $API/organization)
curl -s "${A[@]}" -X PUT -H "If-Match: $(jq -r .etag <<<"$O")" $API/organization -d "$(jq -c 'del(.id, .etag, .working_hours) | .team_label = "team_id"' <<<"$O")" \
  | jq -c '{status, p: .errors[0].pointer, c: .errors[0].code}'                   # {"status":422,"p":"/team_label","c":"reserved_label"}

# C-25.AC-9: a deleted Team stays by name
curl -s "${A[@]}" -X DELETE $API/teams/$TS > /dev/null
GET $G | jq -c '.teams[] | select(.team.slug == "search") | .team.deleted'          # true
```

`test/e2e/team_label_test.go` repeats these checks and an outgoing webhook in events mode against the fake webhook
receiver, which receives `team_joined` with `alert_group.teams`.

## Open questions

None.

## Notes

- Suggested commit: `feat(teams): assign alerts and alert groups to teams by label and matchers`.
- The Teams of an Alert are decided once, when it starts firing (C-25.FR-5), by the Router that already routes it;
  later changes of Teams reach open Alert Groups only through the evaluation on save of S-163.
- `alert_group_teams` is written only by `groups`, like the Alert Group's other tables, so it joins lint 2.
- `team_joined` and `unknown_team_value` are Quiet and never ring anyone; `team_joined` re-renders the Root message,
  which shows Teams from S-125 on.
- With 45 files the story is at the edge of the size signal; the list of Team label values and the evaluation on save
  were split into S-163 when the contract was written.
- Migration `0025` follows the story map; a story merged out of that order renumbers its migration to the next free
  number in its own pull request and re-versions the CHECKs it replaces, listing every value present
  when it merges (S-131's Notes).

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-25.FR-1 | partial | Team matchers |
| C-25.FR-2 | partial | the filter of the Alert Group list; the pages are S-120 |
| C-25.FR-5 | partial | the API; the evaluation on save is S-163, the pages S-120 |
| C-25.FR-6 | partial | the Teams of an Alert Group in the API and webhooks; the page and list S-120, the Root message S-125 |
| C-25.FR-7 | partial | `unknown_team_value`; the list of values and "Create Team" are S-163 and S-120 |
| C-25.FR-10 | partial | Alert Groups keep a deleted Team by name |
| C-25.FR-11 | partial | the matchers' locking and Audit log |
| C-25.FR-12 | partial | the sections on the Team label, matchers and values without a Team |
| C-25.AC-1 | partial | the Teams and `unknown_team_value`; `team-label-values` is S-163 |
| C-25.AC-2 | full | |
| C-25.AC-5 | full | |
| C-25.AC-9 | partial | the deleted Team in an Alert Group; the Schedule is S-131 |
| C-25.AC-11 | partial | the Alert Groups in the API; the pages are S-119 and S-120 |
| C-06.FR-19 | partial | revised for L2: the Teams of each Alert in the API; the page is S-120 |
| C-09.FR-1 | partial | revised for L2: the Teams of an Alert Group |
| C-09.FR-11 | partial | revised for L2: `team_joined` and `unknown_team_value` in the Timeline; the page is S-120 |
| C-09.FR-13 | partial | revised for L2: the Teams filter in the API; the column and filter on the page are S-120 |
| C-09.FR-22 | partial | revised for L2: `team_joined`, `unknown_team_value`; `destination_joined` is S-121, the events of C-32 later |
| C-11.FR-20 | partial | revised for L2: delivery turns `team_joined` into a Root message update and `unknown_team_value` into nothing; the entries' Mentions are S-140, the events of C-30 and C-32 S-148, S-169, S-155, S-168 and S-156 |
| C-15.FR-2 | partial | revised for L2: the Teams and the two events; `destination_joined` S-121, Assignees and `escalation_webhook` later |
| C-20.FR-1 | partial | revised for L2: the Team label in the API; the General page is S-120 |
