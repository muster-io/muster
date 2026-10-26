---
id: S-121
title: Severity conditions on the Destinations of Routes and Teams, delivery to the Teams of an Alert Group, and destination_joined (BE)
capability: C-26
kind: be
layer: L2
depends_on: [S-111, S-118, S-163]
covers: [C-26.FR-1, C-26.FR-2, C-26.FR-3, C-26.FR-4, C-26.FR-5, C-26.FR-6, C-26.FR-7, C-26.FR-9, C-26.FR-13, C-26.AC-1, C-26.AC-2, C-26.AC-3, C-26.AC-4, C-26.AC-5, C-26.AC-6, C-26.AC-10, C-26.AC-11, C-25.FR-10, C-25.FR-13, C-25.AC-13, C-08.FR-1, C-09.FR-22, C-11.FR-6, C-11.FR-14, C-11.FR-20, C-15.FR-2]
files_touched:
  - api/openapi.yaml
  - internal/db/migrations/0027_destination_entries_and_teams.up.sql
  - internal/db/migrations/0027_destination_entries_and_teams.down.sql
  - design/db/schema.md
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - internal/routing/routes.go
  - internal/routing/query.sql
  - internal/routing/routes_test.go
  - internal/api/routes.go
  - internal/teams/destinations.go
  - internal/teams/teams.go
  - internal/teams/query.sql
  - internal/teams/teams_test.go
  - internal/api/teams.go
  - internal/groups/reach.go
  - internal/groups/reach_test.go
  - internal/groups/dispatcher.go
  - internal/groups/events.go
  - internal/groups/query.sql
  - internal/groups/updates.go
  - internal/groups/updates_test.go
  - internal/delivery/enqueue.go
  - internal/delivery/enqueue_test.go
  - internal/delivery/membership.go
  - internal/delivery/membership_test.go
  - internal/delivery/storm.go
  - internal/delivery/storm_test.go
  - internal/delivery/query.sql
  - internal/api/alertgroups.go
  - internal/api/destinations.go
  - internal/destinations/destinations.go
  - internal/destinations/query.sql
  - internal/fakes/fakemattermost/fakemattermost.go
  - internal/fakes/fakemattermost/fakemattermost_test.go
  - test/e2e/routing_teams_test.go
  - web/src/test/fixtures/destinations.ts
  - docs/concepts/routing-to-teams.md
  - mkdocs.yml
  - docs/install/versions.md
acceptance:
  - "[C-26.FR-1, C-08.FR-1] Routes take `destination_entries` — each a Destination with a `severity_condition`, `any` by default, `critical` or `below_critical` — and Teams the same as the Destinations of the Team; a Destination may be named by several Routes and Teams, each with its own condition; `destination_ids` stays on Routes, derived from the entries, and a request with only `destination_ids` keeps the stored conditions of the Destinations it keeps and gives new ones `any`; every change is in the Audit log diff (`routes_test.go`, `teams_test.go`)."
  - "[C-26.FR-2, C-26.AC-1, C-26.AC-2] With the Route \"Infrastructure\" naming `infra-critical` for critical and `infra-notice` below critical with `deliver_to_teams` on, and the Team \"payments\" naming `payments-critical` and `payments-notice` the same way, a warning Alert of `team=\"payments\"` makes one Alert Group with one Root message in `infra-notice` and one in `payments-notice` and none in the critical channels; Acknowledge from `payments-notice` edits both with the same Owner; with `deliver_to_teams` off the same Alert reaches only `infra-notice` (`routing_teams_test.go`)."
  - "[C-26.FR-3, C-26.AC-3] A Destination named by the Route for `critical` and by a Team for `below_critical` receives a warning and a critical Alert Group, each with one Root message; `listAlertGroupDeliveries` names for each Destination the entries that reached it — the Route, or the Team — with their conditions (`reach_test.go`)."
  - "[C-26.FR-4, C-26.FR-5, C-26.AC-4, C-26.AC-5, C-09.FR-22] An Alert of \"search\" joining a firing, unowned Alert Group of \"payments\" records `destination_joined` (Timeline kind `system`) for `search-notice` with `loudness` `loud` and `mentions` `[new_alert_group]` and a new Root message there; joining an acknowledged one records it Quiet with a Quiet Root message; a rise from warning to critical publishes the Alert Group in the critical Destinations with `destination_joined` and keeps the Root messages below critical, which stay and are edited to the current state after the critical Alert resolves; a Destination that a Team or a rise adds before the first Publication is part of it (`enqueue_test.go`, `routing_teams_test.go`)."
  - "[C-26.FR-5, C-11.FR-6] During a Storm of the Route, a Team Destination counts a non-Urgent Alert Group in its own Storm summary, and an Urgent one is published there on its own (`storm_test.go`)."
  - "[C-26.FR-6, C-26.AC-6, C-11.FR-14, C-25.FR-10] Changing a Route Destination's condition from `critical` to `any` publishes the Route's open warning Alert Groups there Quietly, within its limiters, and records no `destination_joined`; changing it back removes none; naming a Destination of a Team or turning `deliver_to_teams` on publishes the open Alert Groups it now reaches Quietly through an `open_group_updates` row of the kind `team_destinations` or the Route's membership hook; a Destination taken off a Route or a Team, or reached no longer because `deliver_to_teams` was turned off, gets the final edit \"No longer updated here\" for each open Alert Group for which no entry that still exists — of its Route or, with `deliver_to_teams`, of its Teams — names that Destination, met or not; deleting a Team does the same for the Team's Destinations; a Destination whose remaining entry is named but not met now keeps its Root message, current until the final state (`membership_test.go`, `updates_test.go`)."
  - "[C-26.FR-7, C-26.AC-10] A Route owned by \"payments\" without `deliver_to_teams` delivers an Alert Group of `team=\"payments\"` only to its own Destinations (`reach_test.go`)."
  - "[C-26.FR-9, C-26.AC-11, C-25.FR-13, C-25.AC-13] Raising a Route's Minimum severity to critical publishes its open warning Alert Groups Quietly in its Destinations limited to critical, without `destination_joined`; creating the Team \"checkout\" for an open Alert Group of `team=\"checkout\"` on a Route with `deliver_to_teams` publishes it Quietly in the Team's Destinations, without `destination_joined` (`updates_test.go`, `routing_teams_test.go`)."
  - "[C-15.FR-2] Outgoing webhooks in events mode receive `destination_joined` one to one, with the Destination it joined; `webhook_events_event_v3_check` accepts it (`db_test.go`, `routing_teams_test.go`)."
  - "[C-26.FR-13] The documentation has the page Concepts → Routing to Teams with the operations and team channels by Severity, the note that the feature is optional, one status in every channel, the Team label in the Group key for teams that want their own Alert Groups, and a Route for test clusters; the upgrade notes of 0.2.0 say that `destination_ids` is derived from `destination_entries` until 0.3.0; `make docs-check` passes."
verify: "make ci test-integration e2e-go docs-check"
operator_attention: false
issue: null
---

# S-121. Severity conditions on the Destinations of Routes and Teams, delivery to the Teams of an Alert Group, and destination_joined (BE)

## Scope

**IN**

- Destination entries with a Severity condition on Routes and on Teams (the Destinations of a Team), and the Route
  options "Also to the Teams from the label" (`route.deliver_to_teams`) and "Run the Teams' Escalation chains"
  (`route.run_team_chains`, stored; it acts from S-169) (C-26.FR-1, FR-2).
- The Destinations an Alert Group reaches: one Root message per Destination, Destinations that only grow, and the
  lifecycle event `destination_joined` (C-26.FR-3 to FR-5, C-09.FR-22 revised).
- Storms per Route and Destination, Team Destinations included (C-11.FR-6, revised).
- Configuration changes that add Destinations Quietly and remove none by a condition, and the final edit where an entry
  is taken away (C-26.FR-6, C-11.FR-14 revised), including those that S-111's Minimum severity and S-163's evaluation
  bring about.
- The entries that reached each Destination in the delivery state of an Alert Group, and the Teams that name a
  Destination.
- The documentation page "Routing to Teams" in a first form, which S-124 completes.

**OUT**

- The Route form with the entries, the option and the preview; the Destinations of the Team on its page; the delivery
  state by entry on the Alert Group page: S-123. The preview "Where alerts go": S-122.
- The Mention entry "Team {Team}" that naming a Destination of the Team adds (C-28.FR-6): S-140.
- The Escalation chains of Teams: S-154; `run_team_chains` acting on them: S-169.
- How a Maintenance window delays `destination_joined`: specified later in L2.

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated):
  - New schemas: `SeverityCondition` (`any`, `critical`, `below_critical`); `DestinationEntry` {`destination_id`,
    `severity_condition`}; `DeliveryEntry` {`kind` (`route`, `team`), `team` (`TeamRef` or null),
    `severity_condition`}.
  - `RouteInput` gains `destination_entries` (`DestinationEntry`[], optional), `deliver_to_teams` (boolean, optional,
    default false) and `run_team_chains` (boolean, optional, default true); `Route` requires the three;
    `destination_ids` becomes `deprecated: true`, "derived from `destination_entries`; when a request has no
    `destination_entries`, it sets them, keeping the stored condition of each Destination it keeps and `any` for a new
    one; removed in 0.3.0".
  - `TeamInput` gains `destination_entries`; `Team` gains `destinations` (`DestinationRef`[] of its entries).
  - `DestinationReadFields` gains `teams` (`TeamRef`[]): the Teams that name it as a Destination of the Team.
  - `AlertGroupDelivery` gains `entries` (`DeliveryEntry`[], required).
  - `LifecycleEvent` gains `destination_joined` (`x-event-kinds` `system`); `WebhookEventName` gains it;
    `TimelineSystemEntry` gains `destination` (`DestinationRef`).
- **Schema** (migration `0027_destination_entries_and_teams`, expand only; `schema.md` §4.8, §4.9, §4.11, §4.15):
  - `route_destinations.severity_condition text NOT NULL DEFAULT 'any' CHECK (severity_condition IN ('any',
    'critical', 'below_critical'))`; `routes.deliver_to_teams boolean NOT NULL DEFAULT false` and
    `routes.run_team_chains boolean NOT NULL DEFAULT true`.
  - `team_destinations`: `org_id`, `team_id`, `destination_id`, `severity_condition` (same CHECK), `added_at`; primary key
    `(team_id, destination_id)`, index `(org_id, destination_id)`.
  - `deliveries.entries jsonb NOT NULL DEFAULT '[]'` — the entries that reached the delivery, appended, never removed:
    `{"kind": "route"}` or `{"kind": "team", "team_id": N}`, with the condition at that time.
  - `timeline_entries.destination_id`; the CHECKs of events, event kinds, system events and the system rule are
    replaced by `_v3` ones with `destination_joined` (kind `system`, the same `system_event`), as S-118's `_v2`.
  - `webhook_events_event_v3_check` adds `destination_joined` to S-118's `_v2`, `NOT VALID`, validated, the old one
    dropped, so that events mode can store it.
  - The CHECK of `open_group_updates.kind` gains `team_destinations` (with the Team); `NOT VALID`, then validated.
- **Reach** (`internal/groups/reach.go`): the Destinations an Alert Group reaches now — its Route's entries whose
  condition its Severity level meets and, with `deliver_to_teams`, the entries of each of its Teams (C-25.FR-6) whose
  condition it meets — keyed by Destination, with the entries of each. The owning Team of the Route adds nothing
  (C-26.FR-7). `groups` reads `route_destinations`, `team_destinations` and `deliveries` for it and writes none of them.
- **Dispatcher** (`dispatcher.go`, `events.go`): after a transition caused by Alerts — `alerts_added`, a rise of the
  Severity level, a Replacement, a Reopen — the dispatcher records one `destination_joined` per reached Destination that
  has no delivery yet, when the Alert Group was already published elsewhere (`first_published_at` set): Loud with
  `new_alert_group` when it is firing, Quiet when it is acknowledged or snoozed; the reach goes to the re-render step in
  `Rendering`. A system transition of a configuration change (S-111's `MinimumSeverityChanged`, S-163's
  `TeamsEvaluated`, this story's `team_destinations`) records none.
- **Delivery** (`enqueue.go`, `membership.go`, `storm.go`): `Enqueue` delivers to the reached Destinations instead of
  the Route's list: a missing delivery is created as the Publication of `destination_joined` (Loud or Quiet by its
  row), as part of the first Publication, or as a Quiet Publication of a configuration change, within the limiters,
  and its `entries` are appended; a Destination no longer reached keeps its delivery and is kept current until its
  final state (C-26.FR-4). The Route's membership hook (`RouteDestinationsChanged`) also handles changed conditions and
  `deliver_to_teams`: open Alert Groups now reached are published Quietly, and a Destination no entry reaches any more
  because the entry or the option went gets the final edit of C-11.FR-14; a condition that is no longer met removes
  nothing. "No other entry still sends there" (C-26.FR-6) is decided per delivery: the deliveries whose `entries` hold
  the entry taken away are the candidates, and each gets the final edit only when no entry that still exists names its
  Destination for the Alert Group — an entry of its Route, or, while `deliver_to_teams` is on, of one of its Teams —
  whether or not that entry's Severity condition is met now; a delivery that such an entry still names stays and is
  kept current until its final state (C-26.FR-4). Storms: the summary of a Route's Storm is rendered in each Destination its Alert Groups reach, Team
  Destinations included, and an Urgent Alert Group is published there on its own (C-11.FR-6). Events-mode outgoing
  webhooks reached through a Team receive their events.
- **Teams** (`internal/teams/destinations.go`): the entries in the Team's input, view and Audit log diff; a save that
  adds, removes or changes an entry writes an `open_group_updates` row `team_destinations`, whose handler runs the
  dispatcher's re-render without a lifecycle event for each open Alert Group that has the Team, and then the final edits
  of C-26.FR-6 where a Destination is reached no more, by the rule above. From this story on, `deleteTeam` takes the
  Team's entries away in its transaction (C-25.FR-10) and writes the same row, so the Destinations of a deleted Team
  get their final edit wherever no other entry still names them; the row keeps the Team's id, since the soft-deleted
  row stays, and the handler finds its Alert Groups through `alert_group_teams`.
- **Fake Mattermost server** (`internal/fakes/fakemattermost`): the channels `ch-infra-critical`, `ch-infra-notice`,
  `ch-payments-critical`, `ch-payments-notice` and `ch-search-notice`, with the bot a member, for the checks of this
  capability.
- **Web test fixtures** (`web/src/test/fixtures/destinations.ts`, S-166): the builders of Destination reads set the neutral value of `teams` (`[]`), so `make generate` leaves the TypeScript check of the web tests green.
- **Settings**: `route.deliver_to_teams`, `route.run_team_chains`, the Severity condition of a Route's or a Team's
  Destination (`any`), `route.storm_threshold`.
- **Log events, metrics**: none new.
- **Documentation**: the new page `docs/concepts/routing-to-teams.md` in the navigation, with the sections that the
  Route form links to ("Also to the Teams from the label", "Severity conditions", "One Alert Group, one status"); S-124
  adds the worked example and the test clusters; `docs/install/versions.md` — `destination_ids` in the upgrade notes.

## Steps

1. Write the migration and `schema.md`. Check: `make test-integration` runs up, down and up; existing
   `route_destinations` rows read `any`.
2. Add the entries and options to Routes and Teams, with the spec. Check: `make generate-check`; `routes_test.go` and
   `teams_test.go` cover `destination_ids` alone, `destination_entries` alone and both.
3. Write the reach and `destination_joined` in the dispatcher. Check: `reach_test.go` covers every condition at every
   level, Teams with and without `deliver_to_teams`, a Destination named twice and the owning Team.
4. Deliver to the reach: Publications, the joined Destinations, Storms and the membership changes. Check:
   `enqueue_test.go`, `storm_test.go`, `membership_test.go`, `updates_test.go` — among them a Destination named by
   the Route for `critical` and by a Team for `below_critical`, whose Team entry is removed while the Alert Group is
   warning: the Route's unmet entry still names it, so it gets no final edit and its Root message stays current; and
   a deleted Team whose Destinations get the final edit.
5. Extend the fake Mattermost server, write the end-to-end test and the documentation. Check: the Verification below;
   `make docs-check`.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-118: A, the Integration "sev" with NOTIFY, PUTA, AG and GET; the Paging policy in P; the demo Mattermost
# Connection (C) of muster dev; the Teams "payments" (TP) and "search" (TS).
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake; FMM=127.0.0.1:18065/_fake
M=$(jq -nc '["new_alert_group","new_alerts","reopen","ack_timeout","snooze_ended","rise_to_urgent"] |
  map({(.): {everyone: "none", user_ids: [], groups: []}}) | add')
DEST() { curl -s "${A[@]}" $API/destinations -d "{\"type\":\"mattermost\",\"name\":\"$1\",\"connection_id\":\"$C\",
  \"team_id\":\"team-dev\",\"channel_id\":\"ch-$1\",\"mentions\":$M,\"limiter\":{\"limit\":30,\"per_seconds\":60}}" | jq -r .id; }
IC=$(DEST infra-critical); IN=$(DEST infra-notice); PC=$(DEST payments-critical); PN=$(DEST payments-notice); SN=$(DEST search-notice)
TPUT() { curl -s "${A[@]}" -X PUT -H "If-Match: $(curl -s "${A[@]}" $API/teams/$1 | jq -r .etag)" $API/teams/$1 \
  -d "$(curl -s "${A[@]}" $API/teams/$1 | jq -c "{name, slug, own_working_hours, matchers, idp_entries} | .destination_entries = $2")" > /dev/null; }
TPUT $TP "[{\"destination_id\":\"$PC\",\"severity_condition\":\"critical\"},{\"destination_id\":\"$PN\",\"severity_condition\":\"below_critical\"}]"
TPUT $TS "[{\"destination_id\":\"$SN\",\"severity_condition\":\"below_critical\"}]"
R=$(curl -s "${A[@]}" $API/routes -d "{\"name\":\"Infrastructure\",\"matchers\":[{\"label\":\"category\",\"op\":\"=\",\"value\":\"infra\"}],
  \"group_key\":[\"alertname\",\"system\"],\"deliver_to_teams\":true,\"destination_entries\":[
  {\"destination_id\":\"$IC\",\"severity_condition\":\"critical\"},{\"destination_id\":\"$IN\",\"severity_condition\":\"below_critical\"}],\"policy\":$P}" | jq -r .id)
ROOTS() { curl -s $FMM/posts | jq -c --arg g "$1" '[.[] | select(.root_id == "" and (.props.attachments[0].title_link // "" | test($g))) | .channel_id] | unique'; }

# C-26.AC-1: one Alert Group, two Root messages
curl -s -X PUT $FAM/groups/o1 -d '{"receiver":"sev","route":"{}","labels":{"alertname":"OOMKilled"}}' > /dev/null
PUTA o1 a '{"labels":{"category":"infra","system":"k8s","team":"payments","severity":"warning"}}'; NOTIFY o1 '{"reason":"first notification"}'
G=$(AG 'team%3D%22payments%22'); sleep 1; ROOTS $G                                    # ["ch-infra-notice","ch-payments-notice"]
curl -s "${A[@]}" "$API/alert-groups/$G/deliveries" | jq -c '[.items[] | {d: .destination.name, e: [.entries[] | .kind]}] | sort_by(.d)'
# [{"d":"infra-notice","e":["route"]},{"d":"payments-notice","e":["team"]}]

# C-26.AC-4: "search" joins a firing, unowned Alert Group
PUTA o1 b '{"labels":{"category":"infra","system":"k8s","team":"search","severity":"warning"}}'; NOTIFY o1 '{"reason":"new alerts"}'; sleep 1
curl -s "${A[@]}" "$API/alert-groups/$G/timeline?limit=5" | jq -c '[.items[] | select(.event == "destination_joined") | {d: .destination.name, loudness, mentions}]'
# [{"d":"search-notice","loudness":"loud","mentions":["new_alert_group"]}]
ROOTS $G                                                                    # ["ch-infra-notice","ch-payments-notice","ch-search-notice"]

# C-26.AC-5: a rise to critical joins the critical Destinations; the others stay
PUTA o1 c '{"labels":{"category":"infra","system":"k8s","team":"payments","severity":"critical"}}'; NOTIFY o1 '{"reason":"new alerts"}'; sleep 1
ROOTS $G         # ["ch-infra-critical","ch-infra-notice","ch-payments-critical","ch-payments-notice","ch-search-notice"]
curl -s -X DELETE $FAM/groups/o1/alerts/c > /dev/null; NOTIFY o1 '{"reason":"resolved"}'; sleep 1
curl -s "${A[@]}" "$API/alert-groups/$G/deliveries" | jq -r '[.items[].state] | unique | join(",")'      # delivered

# C-26.AC-2, C-26.AC-10: the option off gives the final edit in the Team Destinations
E=$(curl -s "${A[@]}" $API/routes/$R | jq -r .etag)
curl -s "${A[@]}" -X PUT -H "If-Match: $E" $API/routes/$R -d "$(curl -s "${A[@]}" $API/routes/$R | jq -c 'del(.id, .etag, .position,
  .is_default, .destinations, .storm_active, .open_alert_group_count, .created_at, .urgent, .destination_ids, .owning_team) | .deliver_to_teams = false')" > /dev/null; sleep 1
curl -s $FMM/posts | jq -r '[.[] | select(.channel_id == "ch-payments-notice") | .props.attachments[0].text // ""] | last | test("No longer updated here")'   # true
```

`test/e2e/routing_teams_test.go` repeats these checks and adds C-26.AC-3 (a Destination named by the Route for
critical and by a Team for below critical), C-26.AC-6 (a condition changed from `critical` to `any` and back), an
acknowledged Alert Group that a Team joins Quietly, a Storm with a Team Destination, the Minimum severity raised to
critical and the Team "checkout" created for an open Alert Group.

## Open questions

None.

## Notes

- Suggested commit: `feat(routing): deliver to the teams of an alert group with severity conditions per destination`.
- One rule decides Loud or Quiet (ADR-0018, C-26.FR-5): Alerts publish in a newly reached Destination with
  `destination_joined`; a change of configuration — a saved Team, a Route option, a condition, the Minimum severity —
  publishes Quietly without it. The dispatcher knows which of the two caused a transition, so it alone records the
  event, and delivery follows it.
- `deliveries.entries` keeps the entries that once reached a Destination, so the delivery state can say why an Alert
  Group is in a channel after its Severity level fell or its Team's Alerts resolved.
- `run_team_chains` is stored and returned now and acts once Escalation chains run (S-169).
- `destination_ids` stays in the API until 0.3.0 (L2.md §2 "Expand and contract").

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-26.FR-1 | partial | the entries in the API; the forms are S-123, the Mention entry of a Team Destination S-140 |
| C-26.FR-2 | partial | delivery to the Teams; the form is S-123, the chains S-169 |
| C-26.FR-3 | full | |
| C-26.FR-4 | full | |
| C-26.FR-5 | partial | `destination_joined` and Storms; the Mentions of the entries for `new_alert_group` are S-140 |
| C-26.FR-6 | full | |
| C-26.FR-7 | partial | the owning Team adds nothing; the hint on the form is S-119 |
| C-26.FR-9 | partial | the Destinations a raised Minimum severity brings; the rest is S-111 |
| C-26.FR-13 | partial | the first form of the routing page; S-124 completes it |
| C-26.AC-1 | full | |
| C-26.AC-2 | full | |
| C-26.AC-3 | full | |
| C-26.AC-4 | full | |
| C-26.AC-5 | full | |
| C-26.AC-6 | full | |
| C-26.AC-10 | full | |
| C-26.AC-11 | partial | the Quiet publication; the Timeline entry is S-111 |
| C-25.FR-10 | partial | the final edits in the Destinations of a deleted Team |
| C-25.FR-13 | partial | the Destinations a saved Team brings, Quietly |
| C-25.AC-13 | partial | the Quiet publication in the Team's Destinations |
| C-08.FR-1 | partial | revised for L2: Destination entries and the option |
| C-09.FR-22 | partial | revised for L2: `destination_joined` |
| C-11.FR-20 | partial | revised for L2: delivery publishes `destination_joined` as a new Root message; its Mentions are S-140 |
| C-11.FR-6 | partial | revised for L2: Storms with Team Destinations; `storm_started` is S-140 |
| C-11.FR-14 | full | revised for L2 |
| C-15.FR-2 | partial | revised for L2: `destination_joined` in events mode |
