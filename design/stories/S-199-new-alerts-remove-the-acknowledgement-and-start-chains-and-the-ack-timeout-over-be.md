---
id: S-199
title: New Alerts remove the acknowledgement, and start Escalation chains and the ack timeout over (BE)
capability: C-36
kind: be
layer: L2
depends_on: [S-198, S-169, S-155, S-173, S-174]
covers: [C-36.FR-2, C-36.FR-6, C-36.FR-7, C-36.AC-3, C-36.AC-5, C-36.AC-6, C-36.AC-7, C-32.FR-5, C-09.FR-4, C-09.FR-6, C-09.FR-9, C-09.FR-22, C-17.FR-1]
files_touched:
  - api/openapi.yaml
  - internal/groups/transitions.go
  - internal/groups/transitions_test.go
  - internal/groups/events.go
  - internal/groups/events_test.go
  - internal/groups/loudness.go
  - internal/groups/loudness_test.go
  - internal/groups/grouping.go
  - internal/groups/dispatcher.go
  - internal/groups/escalation.go
  - internal/groups/timers.go
  - internal/groups/timers_test.go
  - internal/groups/read.go
  - internal/groups/query.sql
  - internal/groups/groups_integration_test.go
  - internal/escalation/runs_integration_test.go
  - internal/delivery/enqueue.go
  - internal/delivery/enqueue_test.go
  - internal/messages/replies.go
  - internal/messages/replies_test.go
  - internal/messages/texts/en.json
  - internal/messages/texts/ru.json
  - internal/api/alertgroups.go
  - internal/api/alertgroups_test.go
  - web/src/components/timeline.tsx
  - web/src/components/timeline.test.tsx
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - test/e2e/new_alerts_remove_ack_test.go
  - test/e2e/gateway_acknowledge_test.go
acceptance:
  - "[C-36.FR-6, C-36.FR-2, C-36.AC-5] With `new_alerts_remove_ack` on, a new Alert joining an Alert Group acknowledged by Alice makes it firing without an Owner and records `alerts_added` (removes) of kind `alerts` with the reason \"New Alerts arrived\", Loud with the Mentions `[owner, new_alerts]`; the Thread reply names the reason, mentions Alice and lists the new Alerts, the Root message shows Ack again, and the first acknowledgement is still Alice's (`transitions_test.go`, `groups_integration_test.go`, `test/e2e/new_alerts_remove_ack_test.go`)."
  - "[C-36.FR-6, C-36.AC-5] With `new_alerts` off in `loud_events` as well, the same entry is Loud with `[owner]` only, and the Thread reply still mentions Alice; with `new_alerts_remove_ack` off, new Alerts in an acknowledged Alert Group stay the Quiet `alerts_added` of L1 and the acknowledgement stays (`loudness_test.go`, `events_test.go`, `test/e2e/new_alerts_remove_ack_test.go`)."
  - "[C-36.FR-7, C-36.AC-5] The removal is a system transition of the dispatcher with Muster as the actor and the Transport `system`, in the Timeline and not in the Audit log; where no chain applies the next ack timeout notice comes 15 minutes later (`route.ack_timeout`) on the development clock, no Reminder follows, the count of unanswered Reminders ends, and the Alert Group can become Unclaimed again (`timers_test.go`, `groups_integration_test.go`, `test/e2e/new_alerts_remove_ack_test.go`)."
  - "[C-36.FR-7, C-36.AC-6, C-32.FR-5] With a chain named on the Route, the removal starts the chain over at step 1 with `started_over` on the runs it replaces, and the Assignees, cleared by the acknowledgement, are filled again by the Pages that follow; an Acknowledge by Alice afterwards records `acknowledged`, not `takeover`, and an Acknowledge through a Notification gateway applies again (`runs_integration_test.go`, `test/e2e/new_alerts_remove_ack_test.go`, `test/e2e/gateway_acknowledge_test.go`)."
  - "[C-36.FR-6, C-36.AC-7] With `new_alerts_remove_ack` on, a Replacement, a Continuation and a new Alert in a snoozed Alert Group leave the status as it was, and so do the Alerts of a Reopen into acknowledged, which keeps its Owner; a critical Alert joining an acknowledged warning Alert Group with `urgent_rise_removes_ack` on records `alerts_added` (acknowledged) and `urgency_raised` (removes) and no `alerts_added` (removes); with `urgent_rise_removes_ack` off it records `alerts_added` (removes) (`transitions_test.go`, `groups_integration_test.go`)."
  - "[C-36.FR-6] The reply of the removal is sent at once and opens the Thread batching window; further new Alerts within it go into one reply when it closes; the entry is of the kind `alerts` on `alert-groups/{id}/timeline` with `from_status`, `to_status`, `previous_owner` and `reason_code` `new_alerts`, which the web Timeline shows as \"New Alerts arrived\" in English and Russian (`enqueue_test.go`, `alertgroups_test.go`, `timeline.test.tsx`)."
  - "[C-36.AC-3] The table-driven test of S-198 gains the removal row: Loud with `[owner]` whatever `loud_events` holds, plus `new_alerts` when the Route lists it (`loudness_test.go`)."
verify: "make ci test-integration e2e-go"
operator_attention: false
issue: null
---

# S-199. New Alerts remove the acknowledgement, and start Escalation chains and the ack timeout over (BE)

## Scope

**IN**

- The new row `alerts_added` (removes) and the decision in the snapshot transition that an Alert newly firing in an
  acknowledged Alert Group removes the acknowledgement when the Route says so, in the order of the rise to Urgent
  (C-36.FR-6).
- The system transition's effects: the Escalation chains and the ack timeout start over, the Reminders and their count
  end, Unclaimed and Assignees follow, the first acknowledgement stays (C-36.FR-7).
- The Thread reply and the Root message update of the removal, with the batching window, the API fields of the entry and
  the web Timeline's text for its reason.

**OUT**

- The fields, the migration and the profiles: S-197. The loudness of the other rows and the delivery changes that read
  the recorded loudness: S-198, which this story builds on.
- New Alerts in an attached Alert Group never remove an acknowledgement, since that Alert Group follows its main Alert
  Group: S-203 (C-37).
- The checkbox on the Route form: S-200. The documentation: S-201.

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated): `TimelineAlertsEntry` gains optional `from_status`,
  `to_status` and `previous_owner`, present only on the removal, as `TimelineStatusEntry` has them;
  `NullableStatusChangeReason` gains `new_alerts` with the text "New Alerts arrived". `LifecycleEvent` already has
  `alerts_added`, so the removal is a row of an existing event told apart by the status it changed, as the PRD says
  (C-36.FR-6); no operation and no new event name.
- **Schema**: none. `timeline_entries` already stores `from_status`, `to_status` and `previous_owner_user_id` on entries
  of any kind, and the CHECKs on events, kinds and Mentions already allow `alerts_added` of the kind `alerts` with the
  Mentions `owner` and `new_alerts`; `thread_replies.event` already holds `alerts_added`.
- **Table** (`internal/groups/events.go`): the row `{EventAlertsAdded, VariantRemoves, KindAlerts, Loud, [owner,
  new_alerts]}` — `VariantRemoves` is the variant `urgency_raised` already uses — added to the list of rows about the
  Owner of S-198's `loudness.go`, so it is Loud with `owner` on every Route and carries `new_alerts` only where the
  Route lists it.
- **The transition** (`internal/groups/transitions.go`, `snapshotChange`): after `rise`, if Alerts newly fired in the
  batch and were recorded as `alerts_added` (acknowledged), and the Route's `new_alerts_remove_ack` is on, and the
  Alert Group is still acknowledged — a rise to Urgent that removed the acknowledgement has made it firing already, and
  with `urgent_rise_removes_ack` off the Alert Group is still acknowledged here — the entry takes `VariantRemoves`, the
  status becomes firing without an Owner, `previous_owner` is the Owner before, `reason` is "New Alerts arrived" with the
  code `new_alerts`, and `c.reason` is set as the rise sets it. A Replacement, a Continuation, the Alerts of a Reopen
  (which `reopen` keeps as it was with its Owner) and Alerts in a snoozed Alert Group never reach it. `routeInfo` in
  `grouping.go` carries the setting from `GetRoutePolicy`. The transition is the dispatcher's system transition with
  Muster as the actor and the Transport `system`; nothing goes to the Audit log (ADR-0016).
- **Chains and timers**: it is a point at which the Alert Group becomes firing again (ADR-0004, C-17.FR-1, C-32.FR-5):
  `syncChains` (`escalation.go`) already starts every chain that applies over, with `started_over` on the runs it
  replaces, once the status is firing and the chains did not run; where no chain applies, the ack timeout of
  `timers.go` starts from `route.ack_timeout` as for any return to firing. The Reminders of the former Owner and the
  count of unanswered Reminders for auto-unacknowledge end with the acknowledgement; Unclaimed is re-evaluated as at any
  return to firing, and the Assignees refill from the Pages that follow, not by this transition. The first
  acknowledgement and the time to acknowledge do not change (C-09.FR-27). The tests prove each; the code changes only
  where a test finds that the existing paths of the rise do not cover this one.
- **Delivery** (`internal/delivery/enqueue.go`): the removal is a Thread reply together with a Root message update
  (`FormReplyAndUpdate`, as `urgency_raised` that removes), queued at once and not collected into an open batch of new
  Alerts, and it opens the Thread batching window so that later Alerts within it go into one reply (C-11.FR-4): the
  batching in `queueReply` keys on the row and not on the event name alone.
- **Texts** (`internal/messages/replies.go`, `texts/en.json`, `texts/ru.json`): the reply "New Alerts arrived; the
  acknowledgement of {owner} is removed" and its list of new Alerts, with the Mention of the Owner as the Destination
  makes it; the Route's language chooses the file, as for every built-in reply.
- **Web** (`web/src/components/timeline.tsx` and the two locale files): the entry's `reason_code` `new_alerts` shows
  "New Alerts arrived" / «Пришли новые алерты», so that the exhaustive switch over reason codes keeps the type check
  green; the dialog and the form are S-200's.
- **Settings**: `route.new_alerts_remove_ack`, `route.urgent_rise_removes_ack`, `route.ack_timeout`,
  `route.reminders`.

## Steps

1. Add the row, the variant's loudness and the decision in `snapshotChange` with its order against the rise. Check:
   `transitions_test.go` covers the four neighbours of FR-6 (Replacement, Continuation, snoozed, Reopen) and the
   two cases of the rise; `events_test.go`, `loudness_test.go`.
2. Prove the starting over: chains, ack timeout, Reminders, Unclaimed, Assignees, first acknowledgement. Check:
   `timers_test.go`, `groups_integration_test.go`, `runs_integration_test.go`.
3. Make delivery send the reply at once and open the window; add the texts. Check: `enqueue_test.go`,
   `replies_test.go`.
4. Add the spec, the API mapping and the web text. Check: `make generate-check`; `alertgroups_test.go`,
   `timeline.test.tsx`.
5. Write `test/e2e/new_alerts_remove_ack_test.go` and extend `gateway_acknowledge_test.go`. Check: the Verification
   below.

## Verification

```sh
make dev > dev.log 2>&1 &
API=localhost:8080/api/v1; CLK=localhost:8082/_dev/clock   # A: Admin; AL: Alice's token; the Route "payments-critical" has new_alerts_remove_ack true and a chain
PUTA e1 a '{"labels":{"alertname":"PaymentsDown","severity":"warning","service":"api"}}'; NOTIFY e1 '{"reason":"first notification"}'
G=$(AG 'alertname%3D%22PaymentsDown%22')
curl -s -H "$AL" -X POST $API/alert-groups/$G/acknowledge | jq -r '.status'                                   # acknowledged
PUTA e1 b '{"labels":{"alertname":"PaymentsDown","severity":"warning","service":"web"}}'; NOTIFY e1 '{"reason":"another service"}'; sleep 2
curl -s -H "$A" $API/alert-groups/$G | jq -c '{status, owner}'                                                 # {"status":"firing","owner":null}
psql "$MUSTER_DB" -c "select first_acknowledged_by_user_id = (select id from users where login = 'alice') as alice from alert_groups where public_id = '$G'"   # t: the first acknowledgement stays Alice's
curl -s -H "$A" "$API/alert-groups/$G/timeline?kind=alerts" | jq -c '[.items[] | select(.event=="alerts_added")] | last | {l: .loudness, m: .mentions, f: .from_status, t: .to_status, p: .previous_owner.login, r: .reason_code}'
# {"l":"loud","m":["owner","new_alerts"],"f":"acknowledged","t":"firing","p":"alice","r":"new_alerts"}
curl -s 127.0.0.1:18065/_fake/requests | jq -r '[.[] | select(.path | test("/posts$"))] | last | .body.message'   # "New Alerts arrived; the acknowledgement of @alice is removed ..."
curl -s -H "$A" "$API/alert-groups/$G/timeline?kind=escalation" | jq -c '[.items[].event]'                    # the chain starts again at step 1: a new paged
curl -s -H "$A" "$API/audit-log?limit=5" | jq -c '[.items[].action] | map(select(startswith("alert_group")))'    # ["alert_group.acknowledged"]: no entry for the removal
# Alice acknowledges again: an Acknowledge, not a Takeover
curl -s -H "$AL" -X POST $API/alert-groups/$G/acknowledge > /dev/null
curl -s -H "$A" "$API/alert-groups/$G/timeline?kind=status" | jq -c '[.items[].event] | last'                  # "acknowledged"
# a Replacement, a Continuation and a snoozed Alert Group leave the status as it was
# the ack timeout without a chain: advance 15 minutes, the notice comes once
curl -s -X POST $CLK -d '{"advance_seconds": 900}' > /dev/null; sleep 2
curl -s -H "$A" "$API/alert-groups/$G/timeline?kind=timers" | jq -c '[.items[].event]'                         # [...,"ack_timeout"]
# critical joins an acknowledged warning group: urgent_rise_removes_ack on records urgency_raised (removes), none of alerts_added (removes)
```

`test/e2e/new_alerts_remove_ack_test.go` repeats these checks and adds the setting off, the Mentions with `new_alerts` off,
the Reminders that end, the first acknowledgement still Alice's, the Assignees refilled by the next Page, and the
Acknowledge through the fake gateway that applies again.

## Open questions

None.

## Notes

- Suggested commit: `feat(groups): let new alerts remove the acknowledgement of an alert group where the route says so`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases. No migration.
- The Root message update of the removal is the ordinary re-render of the new status, with the buttons of firing; the
  Quiet Root message update is not a separate row.
- This story changes the web Timeline only for the reason code, so that the type check passes after the spec changes;
  the Route form is S-200.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-36.FR-2 | partial | the removal row joins the table of S-198, which has the other rows |
| C-36.FR-6 | full | |
| C-36.FR-7 | full | |
| C-32.FR-5 | partial | the row of the table of points that start chains over: new Alerts that remove an acknowledgement, where the Route says so; the Maintenance-window rows are S-193's, the Attach and Detach rows S-204's |
| C-36.AC-3 | partial | the removal row of the table-driven test; the other rows are S-198 |
| C-36.AC-5 | full | |
| C-36.AC-6 | full | |
| C-36.AC-7 | full | |
| C-09.FR-4 | partial | revised for L2: a Reopen into acknowledged keeps its Owner and its Alerts remove nothing; the Reopen as a Loud event is S-198's, a Reopen never attaches again S-203's |
| C-09.FR-6 | partial | revised for L2: the removal of an acknowledgement by new Alerts, never in a snoozed Alert Group; new Alerts as a Loud event is S-198's, in an attached Alert Group S-203's |
| C-09.FR-9 | partial | revised for L2: the order of the rise to critical against new Alerts; `rise_to_urgent` as a Loud event is S-198's |
| C-09.FR-22 | partial | revised for L2: the row `alerts_added` (removes); the loudness of the other rows is S-198's |
| C-17.FR-1 | partial | revised for L2: the ack timeout and the chains start over when new Alerts remove an acknowledgement; the first chain stops it (S-169), they stop on Attach and start over on Detach (S-204) |
| reference.md Loud and Quiet | partial | revised for L2: the removal of an acknowledgement; the Loud events of a Route are S-198's |
