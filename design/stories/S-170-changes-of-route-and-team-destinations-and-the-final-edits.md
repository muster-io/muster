---
id: S-170
title: Changes of the Destinations of Routes and Teams that publish open Alert Groups Quietly, and the final edits of Destinations no entry names any more (BE)
capability: C-26
kind: be
layer: L2
depends_on: [S-121]
covers: [C-26.FR-6, C-26.FR-13, C-26.AC-6, C-26.AC-10, C-25.FR-10, C-11.FR-14]
files_touched:
  - internal/routing/routes.go
  - internal/routing/routes_test.go
  - internal/routing/query.sql
  - internal/delivery/membership.go
  - internal/delivery/membership_test.go
  - internal/delivery/query.sql
  - internal/delivery/enqueue.go
  - internal/delivery/enqueue_test.go
  - internal/delivery/lockorder_integration_test.go
  - internal/delivery/worker.go
  - internal/db/checks.go
  - internal/teams/destinations.go
  - internal/teams/teams.go
  - internal/teams/query.sql
  - internal/teams/teams_test.go
  - internal/groups/updates.go
  - internal/groups/updates_test.go
  - internal/groups/dispatcher.go
  - internal/groups/query.sql
  - internal/runtime/runtime.go
  - test/e2e/routing_changes_test.go
  - docs/concepts/routing-to-teams.md
acceptance:
  - "[C-26.FR-6, C-26.AC-6, C-11.FR-14] Changing a Route Destination's condition from `critical` to `any` publishes the Route's open warning Alert Groups there Quietly, within its limiters, and records no `destination_joined`; changing it back removes none; a Destination added to a Route with the condition `critical` receives only its open critical Alert Groups, Quietly; turning `deliver_to_teams` on publishes the Route's open Alert Groups Quietly in the Destinations of their Teams whose condition they meet (`membership_test.go`, `routing_changes_test.go`)."
  - "[C-26.FR-6, C-11.FR-14] Saving a Team whose Destination entries were added, removed or changed writes, in the save's transaction, one `open_group_updates` row of the kind `team_destinations` (the kind of S-121's migration); a worker on any replica claims it with a lease and runs the dispatcher's re-render, without a lifecycle event, for each open Alert Group that has the Team, which publishes it Quietly in the Destinations it now reaches; a second run of the row changes nothing (`updates_test.go`)."
  - "[C-26.FR-6, C-26.AC-10, C-11.FR-14] A Destination taken off a Route or a Team, or reached no longer because `deliver_to_teams` was turned off, gets the final edit \"No longer updated here\" for each open Alert Group for which no entry that still exists names that Destination — an entry of its Route or, while `deliver_to_teams` is on, of one of its Teams — whether or not that entry's condition is met now; the candidates are the deliveries whose `entries` hold the entry taken away; a Destination named by the Route for `critical` and by a Team for `below_critical`, whose Team entry is removed while the Alert Group is warning, keeps its Root message, current until its final state (`membership_test.go`, `updates_test.go`)."
  - "[C-26.FR-6, C-11.FR-14] A change of a Team's Destination entries and an Enqueue of an Alert Group that has the Team serialize on the Team's membership lock: a save that adds a Destination to the Team and commits while an Alert of the Team is being grouped leaves the Destination neither out of that Enqueue nor out of the run of its `team_destinations` row, and the lock order of `worker.go` admits no deadlock (`enqueue_test.go`, `lockorder_integration_test.go`)."
  - "[C-25.FR-10] Deleting a Team takes its Destination entries away in the transaction of `deleteTeam` and writes the same `team_destinations` row, so the Destinations of the deleted Team get the final edit for each open Alert Group that had the Team, wherever no other entry still names them (`teams_test.go`, `updates_test.go`)."
  - "[C-26.FR-13] Routing to Teams has the section \"When you change a Route or a Team\": a new Destination, condition or Team adds Root messages Quietly and removes none by a condition, and a Destination taken away gets the final edit; `make docs-check` passes."
verify: "make ci test-integration e2e-go docs-check"
operator_attention: false
issue: 459
---

# S-170. Changes of the Destinations of Routes and Teams that publish open Alert Groups Quietly, and the final edits of Destinations no entry names any more (BE)

## Scope

**IN**

- The changes of configuration that move the reach of the open Alert Groups without a transition of their own
  (C-26.FR-6, C-11.FR-14 revised): a Route's Destination entries, their Severity conditions and
  `route.deliver_to_teams`, through the Route's membership hook; a Team's Destination entries, through the
  `open_group_updates` kind `team_destinations`.
- Their Quiet Publications, within the limiters, and the final edit of a Destination that no entry names any more,
  with the rule that a condition alone never removes a Root message.
- Deleting a Team, which takes the Destinations of the Team away with the same final edits (C-25.FR-10).
- The section of the routing page on changes of a Route or a Team.

**OUT**

- The Destination entries, the reach, `destination_joined`, the Storms and the Quiet Publications that the system
  transitions of S-111 (a changed Minimum severity) and S-163 (the evaluation on save) bring: S-121.
- The Route form and the Team page that make these changes: S-123.
- The Escalation chains that a configuration change never starts: S-169 keeps it so.

## Contracts

- **Schema**: none. The kind `team_destinations` of `open_group_updates`, with its Team, is in S-121's migration
  `0027`, which this story's handler uses; `deliveries.entries` (S-121) names the entries that reached each delivery.
- **Routing** (`internal/routing/routes.go`): `updateRoute` calls the membership hook of delivery, in its transaction,
  with the entries before and after the save and `deliver_to_teams` before and after, instead of the Destinations added
  and removed only; an unchanged save calls nothing. The hook's type `routing.Membership` changes with it, and
  `internal/runtime/runtime.go`, which wires `routes.SetMembership` to `delivery.RouteDestinationsChanged`, follows.
- **Delivery** (`internal/delivery/membership.go`, `query.sql`): `RouteDestinationsChanged` takes the Route's
  membership lock as before and, for its open Alert Groups, publishes Quietly in each Destination that their reach
  (S-121's rule) gains, appending the entry to `entries`, and gives the final edit of C-11.FR-14 to each delivery that
  lost an entry when no entry that still exists names its Destination for the Alert Group — an entry of its Route or,
  while `deliver_to_teams` is on, of one of its Teams — met or not; otherwise the delivery stays and is kept current
  until its final state (C-26.FR-4). A condition that is no longer met removes nothing. After the Route's exclusive
  lock it takes, shared and by Team id, the membership lock of each Team whose entries it reads, so that a Team save
  never commits between its read and its deliveries. The lock order is that of `worker.go`, as for the L1 hook.
- **Team membership lock** (`internal/db/checks.go`, `internal/teams/query.sql`, `internal/delivery/query.sql`,
  `internal/groups/query.sql`, `internal/routing/query.sql`): a new class `db.TeamMembershipLockClass` =
  `0x6d75_0008` — the classes `0x6d75_0001` to `0x6d75_0007` are taken (`internal/auth`, `internal/db`,
  `internal/delivery`'s `postCopyLockClass` and `buttonKeyLockClass`, `internal/connections`' `bridgeEventLockClass`) —
  second key `hashint8` of the Team's id, as `RouteMembershipLockClass` is for a Route (plan review, M3). Who takes it:
  - shared, for each Team of the Alert Group in the order of their ids: the dispatcher before it reads the reach,
    after the Route's lock that S-121 takes there, and `Enqueue` after the Route's lock; the membership hook of a Route
    after the Route's exclusive lock, for the Teams whose entries it reads;
  - exclusively: a Team save that changes its Destination entries, after its own row and before it writes
    `team_destinations`; `deleteTeam`, last — it first updates the Destinations the Team owns, then the Routes it owns
    (S-115; a Team that owns a chain is refused, S-154), and only then takes the lock and deletes its
    `team_destinations`. Taking the lock before those rows would close a cycle: grouping holds a Route's row FOR SHARE
    and then waits for the Team's lock shared, and a Route save holds its row and the Route's membership lock and then
    waits for it too, while `deleteTeam` would hold the lock and wait for that Route's row. The Destinations come before
    the Routes because the deletion of a Destination locks its row before the Routes that name it;
  - exclusively, by the deletion of a Destination (`DestinationDeleted`, `internal/routing/routes.go` and a query of
    `internal/routing/query.sql`): after the Routes' membership locks, in id order, the lock of each Team whose
    `team_destinations` name the Destination, whose rows it then deletes, as the Destination's `route_destinations`
    are, so that no Enqueue reads such an entry and delivers to the deleted Destination while the deletion commits.
  So an Enqueue that reads a Team's entries and a change of them serialize, as L1's lock does for a Route. The order of
  `worker.go` gains it after the Route's membership lock: the Alert Groups, the Route's membership lock, the Teams'
  membership locks by id, the active Storm, the deliveries. S-121 writes the dispatcher's Route lock into the lock-order
  comment of `worker.go` (`:40`), and this story adds the Teams' locks to it; `lockorder_integration_test.go` covers a
  Team save against grouping, `deleteTeam` of a Team that owns a Route against grouping on that Route and against a
  save of it, and a Destination's deletion against an Enqueue that reaches it through a Team.
- **Teams** (`internal/teams/destinations.go`, `teams.go`): a save that adds, removes or changes an entry writes the
  `team_destinations` row with the Team in its transaction; `deleteTeam` deletes the Team's entries in its transaction,
  after nulling its owned Destinations and Routes and taking the lock as above, and writes the same row, which keeps the
  Team's id, since the soft-deleted Team stays.
- **Groups** (`internal/groups/updates.go`, `dispatcher.go`, `query.sql`): the handler of `team_destinations` walks the
  open Alert Groups that have the Team (`alert_group_teams`) by id from the row's cursor in batches of 50; for each,
  one dispatcher transaction runs the re-render step without a lifecycle event — so `Enqueue` publishes Quietly where
  the reach grew — and then asks delivery for the final edits of the Destinations the Team's entries no longer name, by
  the rule above. `groups` cannot import `delivery`, so it asks through a new interface it declares, `FinalEdits`
  (`FinalEditsFor(ctx, tx, group, team)`), set with `SetFinalEdits` in `updates.go`, which delivery implements in
  `membership.go` and `internal/runtime/runtime.go` wires, as it wires `SetRerender`. A second run finds every
  delivery in place and changes nothing. `dispatcher.go` takes the Teams' membership locks where S-121 takes the
  Route's.
- **Delivery** (`internal/delivery/enqueue.go`): `Enqueue` takes the Teams' membership locks shared after the Route's
  and before it reads the Teams' entries.
- **Settings**: `route.deliver_to_teams`, the Severity condition of a Route's or a Team's Destination.
- **Log events, metrics**: none new; `open_group_update_done` names the kind.
- **Documentation**: `docs/concepts/routing-to-teams.md` — "When you change a Route or a Team".

## Steps

1. Pass the entries and the option to the membership hook, and publish and give final edits by the rule. Check:
   `routes_test.go`; `membership_test.go` covers a condition widened and narrowed, a Destination added with `critical`,
   the option on and off, a Destination named twice, and a remaining entry that is not met.
2. Write the Team's membership lock, the Team's row and its handler with the hook of the final edits, deletion
   included, and wire it. Check: `teams_test.go`; `updates_test.go` covers an entry added, removed and changed, a
   deleted Team, two workers and a second run; `enqueue_test.go` and `lockorder_integration_test.go` cover a Team save
   that commits while an Alert of the Team is grouped; `go vet ./...` finds no import cycle.
3. Write `test/e2e/routing_changes_test.go` and the section of the page. Check: the Verification below;
   `make docs-check`.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-121: A, the Integration "sev" with NOTIFY, PUTA and AG, DEST, TPUT, ROOTS and FMM; the Destinations IC, IN,
# PC, PN and SN, the Teams "payments" (TP, with PC critical and PN below critical) and "search" (TS, with SN), and the
# Route "Infrastructure" (R) with IC critical, IN below critical and deliver_to_teams on.
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake; FMM=127.0.0.1:18065/_fake
RPUT() { E=$(curl -s "${A[@]}" $API/routes/$R | jq -r .etag); curl -s "${A[@]}" -X PUT -H "If-Match: $E" $API/routes/$R \
  -d "$(curl -s "${A[@]}" $API/routes/$R | jq -c "del(.id, .etag, .position, .is_default, .destinations, .storm_active,
  .open_alert_group_count, .created_at, .urgent, .destination_ids, .owning_team) | $1")" > /dev/null; sleep 1; }
curl -s -X PUT $FAM/groups/q1 -d '{"receiver":"sev","route":"{}","labels":{"alertname":"Latency"}}' > /dev/null
PUTA q1 a '{"labels":{"category":"infra","system":"api","team":"payments","severity":"warning"}}'; NOTIFY q1 '{"reason":"first notification"}'
G=$(AG 'system%3D%22api%22'); sleep 1; ROOTS $G                                     # ["ch-infra-notice","ch-payments-notice"]

# C-26.AC-6: a condition widened publishes Quietly, without destination_joined; narrowed again, it removes nothing
RPUT ".destination_entries = [{\"destination_id\":\"$IC\",\"severity_condition\":\"any\"},{\"destination_id\":\"$IN\",\"severity_condition\":\"below_critical\"}]"
ROOTS $G                                                     # ["ch-infra-critical","ch-infra-notice","ch-payments-notice"]
curl -s "${A[@]}" "$API/alert-groups/$G/timeline?limit=10" | jq '[.items[] | select(.event == "destination_joined")] | length'   # 0
RPUT ".destination_entries = [{\"destination_id\":\"$IC\",\"severity_condition\":\"critical\"},{\"destination_id\":\"$IN\",\"severity_condition\":\"below_critical\"}]"
curl -s $FMM/posts | jq -r '[.[] | select(.channel_id == "ch-infra-critical") | .props.attachments[0].text // ""] | last | test("No longer updated here")'   # false

# a remaining entry that is not met keeps the Root message: payments-notice is named by the Route for critical too
RPUT ".destination_entries += [{\"destination_id\":\"$PN\",\"severity_condition\":\"critical\"}]"
TPUT $TP "[{\"destination_id\":\"$PC\",\"severity_condition\":\"critical\"}]"; sleep 1
curl -s $FMM/posts | jq -r '[.[] | select(.channel_id == "ch-payments-notice") | .props.attachments[0].text // ""] | last | test("No longer updated here")'   # false

# C-25.FR-10: an Alert of "search" brings search-notice; deleting the Team gives it the final edit
PUTA q1 b '{"labels":{"category":"infra","system":"api","team":"search","severity":"warning"}}'; NOTIFY q1 '{"reason":"new alerts"}'; sleep 1
ROOTS $G                                    # ["ch-infra-critical","ch-infra-notice","ch-payments-notice","ch-search-notice"]
curl -s -o /dev/null -w '%{http_code}\n' "${A[@]}" -X DELETE $API/teams/$TS                                # 204
sleep 1; psql "$MUSTER_DATABASE_URL" -Atc "SELECT count(*) FROM open_group_updates"                         # 0
curl -s $FMM/posts | jq -r '[.[] | select(.channel_id == "ch-search-notice") | .props.attachments[0].text // ""] | last | test("No longer updated here")'   # true

# C-26.AC-10: a rise to critical brings payments-critical through the Team; deliver_to_teams off gives it the final edit
PUTA q1 c '{"labels":{"category":"infra","system":"api","team":"payments","severity":"critical"}}'; NOTIFY q1 '{"reason":"new alerts"}'; sleep 1
RPUT '.deliver_to_teams = false'
curl -s $FMM/posts | jq -r '[.[] | select(.channel_id == "ch-payments-critical") | .props.attachments[0].text // ""] | last | test("No longer updated here")'   # true
curl -s $FMM/posts | jq -r '[.[] | select(.channel_id == "ch-payments-notice") | .props.attachments[0].text // ""] | last | test("No longer updated here")'   # false
```

`test/e2e/routing_changes_test.go` repeats these checks, adds a Destination added to a Route with `critical` that
receives no warning Alert Group, and a Team Destination added while an Alert Group is acknowledged, published Quietly.

## Open questions

None.

## Notes

- Suggested commit: `feat(routing): publish quietly and give final edits when route and team destinations change`.
- Split from S-121 by the review of the contracts: S-121 keeps the reach and `destination_joined`, and this story the
  configuration changes that move the reach of open Alert Groups and the final edits. It keeps its place right after
  S-121 in the story map and needs no migration: S-121's `0027` already adds the kind `team_destinations`.
- One rule decides Loud or Quiet (ADR-0018, C-26.FR-5): Alerts publish with `destination_joined` (S-121); a change of
  configuration publishes Quietly without it and starts no Escalation chain.
- A Destination is taken away only by its last entry: a Root message whose remaining entry is not met now stays and is
  kept current (C-26.FR-4).
- The plan review added the Team's membership lock (M3) and the files that wire the changed hooks (M5): without the
  lock, an Enqueue reading a Team's entries and a save of them could each miss the Destination the other adds, which
  L1's lock prevents for Routes.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-26.FR-6 | full | |
| C-26.FR-13 | partial | the section on changes; S-111, S-121, S-122 and S-124 write the rest |
| C-26.AC-6 | full | |
| C-26.AC-10 | partial | the final edit when `deliver_to_teams` is turned off; the owning Team adding nothing is S-121 |
| C-25.FR-10 | partial | the final edits in the Destinations of a deleted Team |
| C-11.FR-14 | full | revised for L2 |
