---
id: S-203
title: Attached Alert Groups follow their main Alert Group, and the attachment ends on a resolve (BE)
capability: C-37
kind: be
layer: L2
depends_on: [S-202, S-199, S-173, S-169, S-063]
covers: [C-37.FR-3, C-37.FR-5, C-37.FR-6, C-37.FR-7, C-37.FR-9, C-37.FR-15, C-37.AC-3, C-37.AC-4, C-37.AC-5, C-37.AC-7, C-09.FR-1, C-09.FR-4, C-09.FR-6, C-09.FR-9, C-09.FR-22, C-10.FR-1, C-26.FR-5, C-30.FR-6]
files_touched:
  - api/openapi.yaml
  - web/src/test/fixtures/alert-groups.ts
  - internal/groups/follow.go
  - internal/groups/follow_test.go
  - internal/groups/attach.go
  - internal/groups/attach_test.go
  - internal/groups/commands.go
  - internal/groups/commands_test.go
  - internal/groups/dispatcher.go
  - internal/groups/dispatcher_test.go
  - internal/groups/grouping.go
  - internal/groups/transitions.go
  - internal/groups/transitions_test.go
  - internal/groups/loudness.go
  - internal/groups/loudness_test.go
  - internal/groups/release.go
  - internal/groups/release_test.go
  - internal/groups/snooze.go
  - internal/groups/timers.go
  - internal/groups/timers_test.go
  - internal/groups/updates.go
  - internal/groups/events.go
  - internal/groups/events_test.go
  - internal/groups/query.sql
  - internal/groups/groups_integration_test.go
  - internal/groups/walks_integration_test.go
  - internal/api/commands.go
  - internal/api/commands_test.go
  - internal/api/problem.go
  - internal/messages/texts/en.json
  - internal/messages/texts/ru.json
  - test/e2e/attach_follow_test.go
  - test/e2e/gateway_acknowledge_test.go
acceptance:
  - "[C-37.FR-5, C-37.AC-3] Acknowledge, a Takeover, Snooze and Unsnooze of `#1287` through the API each record `status_followed` on `#1288` with the same status and Owner and the person behind it, as a system transition of the dispatcher with Muster as the actor and the Transport `system`, and no Audit log entry for `#1288`; Unacknowledge of `#1287` makes `#1288` firing without an Owner and starts none of its Escalation chains (`follow_test.go`, `groups_integration_test.go`, `test/e2e/attach_follow_test.go`)."
  - "[C-37.FR-5] The other changes of the main Alert Group are followed too — the end of a Snooze, auto-unacknowledge, a rise to critical that removes the acknowledgement (C-09.FR-9), new Alerts that remove it (C-36.FR-6) and an Owner disabled or deleted — each as one `status_followed`; following an acknowledgement sets the first acknowledgement of the attached Alert Group, when it has none, to the main Alert Group's Owner, and following never starts its own Escalation chains, ack timeout or Reminders whatever status it takes (`follow_test.go`, `release_test.go`, `timers_test.go`, `test/e2e/attach_follow_test.go`)."
  - "[C-37.FR-6, C-37.AC-5] Resolving `#1287` by hand resolves `#1288` with the same person as the resolver, records `status_followed` with that person, sets `resolved_with_id`, ends the attachment, and groups the Alerts of it that still fire again after the Route's Grace period as after any manual resolve (`follow_test.go`, `groups_integration_test.go`, `test/e2e/attach_follow_test.go`)."
  - "[C-37.FR-6, C-37.FR-15, C-37.AC-5] When the system resolves `#1287` while an Alert of `#1290` still fires, `#1290` records `detached` with the reason \"#1287 resolved by itself\" and `#1287` records `alert_group_detached`, `#1290` keeps its status, is no longer attached and lives on its own; an attached Alert Group whose own Alerts all resolve is resolved by the system as any other (C-09.FR-3), the attachment ends, and `#1287` records `alert_group_detached` and no longer lists it; an Unresolve or a Reopen of either never attaches again (`follow_test.go`, `groups_integration_test.go`, `test/e2e/attach_follow_test.go`)."
  - "[C-37.FR-7, C-37.AC-7] A new critical Alert joining an attached, acknowledged warning Alert Group records Quiet entries only, without Mentions, the Owner's included, ends no Snooze, removes no acknowledgement and leaves it acknowledged; its Severity level and Teams follow its Alerts, and the Severity level, Urgent flag, Teams, Destinations and Escalation chains of the main Alert Group do not take its Alerts into account; `destination_joined` into a Destination it reaches meanwhile is Quiet (`loudness_test.go`, `transitions_test.go`, `groups_integration_test.go`)."
  - "[C-37.FR-9, C-37.AC-4] While an Alert Group is attached, Acknowledge, Unacknowledge, Resolve, Snooze and Unsnooze on it, from any Transport, are refused with `409` `alert_group_attached` and the message \"#1288 follows #1287; act on #1287, or detach it first\"; Add Note and Detach stay allowed; an acknowledgement reported by a Notification gateway is answered as one for an Alert Group someone owns (`commands_test.go`, `test/e2e/attach_follow_test.go`, `test/e2e/gateway_acknowledge_test.go`)."
  - "[C-37.FR-3] Every Command and system transition that touches a main Alert Group or an attached one locks the main Alert Group first and then its attached Alert Groups in the order of their ids, and following runs in the transaction of the change of the main Alert Group: an Acknowledge of the main Alert Group, a Detach of one attached and a resolve by the system, run concurrently, end without a deadlock and with consistent status and attachments (`walks_integration_test.go`, `groups_integration_test.go`)."
verify: "make ci test-integration e2e-go"
operator_attention: false
issue: null
---

# S-203. Attached Alert Groups follow their main Alert Group, and the attachment ends on a resolve (BE)

## Scope

**IN**

- Following: every change of a main Alert Group's status applied to each attached Alert Group in the same transaction as
  a system transition recording `status_followed`, with the first acknowledgement and the guard that following starts no
  chain, ack timeout or Reminder of its own (C-37.FR-5).
- The end of an attachment when either Alert Group resolves, in its three cases, with `resolved_with_id`
  (C-37.FR-6).
- What an Alert Group does with its own Alerts while attached, and the rule that every event recorded on it is Quiet and
  carries no Mentions (C-37.FR-7).
- The refusal of the other Commands on an attached Alert Group, and the lock order of a main Alert Group with its attached
  ones (C-37.FR-3, FR-9).

**OUT**

- The state, Attach and Detach themselves, the refusals of FR-2 and the limit: S-202.
- Stopping and starting over chains and timers, including the detached Alert Group's start-over after a system resolve
  of its main one: S-204. Until it merges, following sets `noChains` and nothing else starts or stops a timer.
- The line, the Thread replies and the "Resolved with" Root message in messengers: S-205.
- The `attached` list filter, bulk Attach and bulk refusals, statistics and webhook events: S-206.
- The pages: S-207. The documentation: S-208.

## Contracts

- **Spec** (`api/openapi.yaml`): the problem code `alert_group_attached` (`409`) is added to the Commands that it refuses
  and to the catalogue in `internal/api/problem.go`; `AlertGroup` gains the required `resolved_with` (an `AlertGroupRef`
  or null, the read of `resolved_with_id`), which the builder of `web/src/test/fixtures/alert-groups.ts` sets to null so
  that the TypeScript check stays green after `make generate`, as S-202's fields do. S-202 added the other fields and the
  five events.
- **Schema**: none new; `alert_groups.resolved_with_id` and `attached_to_id` are S-202's.
- **Following** (`internal/groups/follow.go`, ADR-0016: one command layer): after the transition of a main Alert Group
  has been applied and recorded in a transaction, the dispatcher reads the Alert Groups whose `attached_to_id` is the
  main one, locks them in the order of their ids — the main one has been locked first, as FR-3 says — and applies to
  each the transition `follow`: the main Alert Group's status, Owner and Snooze end, with an entry `status_followed`
  naming the new status, the person or the system change behind it and the main Alert Group. The same code runs for
  every source of a change: the Commands Acknowledge, a Takeover, Unacknowledge, Snooze and Unsnooze, the timer of the
  end of a Snooze, auto-unacknowledge, the rise that removes the acknowledgement (C-09.FR-9), new Alerts that remove it
  (S-199) and the release of a disabled or deleted Owner (`release.go`, which skips an attached Alert Group when it
  walks the Alert Groups of the Owner, since the main one carries it). Following is not in the Audit log; the Command on
  the main Alert Group is. The change of the attached Alert Group sets `noChains` and starts nothing of its own, and the
  guard that no later path starts a chain, the ack timeout or a Reminder for an attached Alert Group is S-204's.
- **Resolve** (`follow.go`, `attach.go`): the main Alert Group resolved by a person resolves each attached one with the
  same resolver and the reason of its status, records `status_followed`, sets `resolved_with_id`, clears
  `attached_to_id`, and for each starts the Grace period of its own Route (C-09.FR-5); the main Alert Group resolved by
  the system detaches each attached one, which still has firing Alerts, with `detached` and the reason "#{M} resolved by
  itself", keeping its followed status, Owner and Snooze end, as after a Detach (C-37.FR-6, FR-8), and the main Alert
  Group records `alert_group_detached` for each, since C-37.FR-15 records it whenever an attached Alert Group is
  detached; an attached Alert Group whose own Alerts all resolve is
  resolved by the system through the existing path, the attachment ends and the main Alert Group records
  `alert_group_detached`. The unresolved or reopened Alert Group never attaches again, since `attached_to_id` is cleared
  when the attachment ends.
- **Own Alerts of an attached Alert Group** (`grouping.go`, `transitions.go`): it still takes Alerts by its Route and
  Group key and its level and Teams change, but the snapshot transition skips the rise's effect on the status — no Snooze
  ends, no acknowledgement is removed by a rise or by `new_alerts_remove_ack` (the guard S-199 left) — and records
  `urgency_raised` as the row that removes nothing. The main Alert Group's own computations read only its own
  Alerts, so nothing changes there; a test proves it.
- **Loudness** (`loudness.go`, S-198's resolver): an entry recorded on an attached Alert Group is Quiet with no Mentions
  whatever its row and its Route's settings say (C-37.FR-7, reference.md Loud and Quiet), including `destination_joined`.
  The order in the resolver is: attached first, then the Route's Loud events.
- **Refusals** (`commands.go`, `internal/api/commands.go`): the preconditions of Acknowledge, Unacknowledge, Resolve,
  Snooze and Unsnooze return `409` `alert_group_attached` with the message "#{N} follows #{M}; act on #{M}, or detach it
  first"; Add Note and Detach pass; a bulk command meets the same precondition per item (tested in S-206); the Command of
  the Transport `gateway` is answered as for an Alert Group someone owns (C-30.FR-6). The message's texts for messengers
  join `internal/messages/texts/{en,ru}.json`.
- **Lock order** (`dispatcher.go`, `grouping.go`): the one function that locks an Alert Group takes, for an attached one,
  its main Alert Group first, and for a main one, its attached Alert Groups after it in id order; the workers that walk
  many Alert Groups (`updates.go`, the timers) take them through it. The walk tests of `walks_integration_test.go` gain
  a main Alert Group with attached ones.
- **Settings**: none.
- **Log events**: none new; a system transition logs as the others do.

## Steps

1. Write the lock function and make the dispatcher, the timers, `release.go` and `updates.go` use it. Check:
   `walks_integration_test.go`, `dispatcher_test.go` (an Acknowledge, a Detach and a system resolve at once).
2. Write `follow.go` and run it from every source of a change. Check: `follow_test.go` with a table over the sources of
   FR-5; `release_test.go`.
3. Write the three endings on a resolve. Check: `follow_test.go`, `groups_integration_test.go` (hand resolve, system
   resolve with a firing Alert, own Alerts resolved).
4. Add the loudness override and the guard on the rise and on new Alerts. Check: `loudness_test.go`,
   `transitions_test.go`.
5. Add the refusals, the problem code and the texts. Check: `commands_test.go`; `make generate-check`.
6. Write `test/e2e/attach_follow_test.go`. Check: the Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
API=localhost:8080/api/v1; CLK=localhost:8082/_dev/clock   # A: Admin; I: Ivan; #1288 and #1290 attached to #1287 as in S-202 (AG1287, AG1288, AG1290)
curl -s -H "$I" -X POST $API/alert-groups/$AG1287/acknowledge > /dev/null
curl -s -H "$A" "$API/alert-groups/$AG1288/timeline?kind=status" | jq -c '[.items[] | select(.event=="status_followed") | {to: .to_status, o: .owner.login, main: .main_alert_group.number, l: .loudness}] | last'
# {"to":"acknowledged","o":"ivan","main":1287,"l":"quiet"}
curl -s -H "$A" "$API/audit-log?limit=5" | jq -c '[.items[] | .details.alert_group // empty] | map(select(. == 1288)) | length'   # 0: no entry for #1288
# the other Commands on an attached Alert Group are refused; a note and Detach are not
curl -s -H "$I" -X POST $API/alert-groups/$AG1288/resolve -d '{}' | jq -c '{status, code, detail}'
# {"status":409,"code":"alert_group_attached","detail":"#1288 follows #1287; act on #1287, or detach it first"}
curl -s -o /dev/null -w '%{http_code}\n' -H "$I" -X POST $API/alert-groups/$AG1288/notes -d '{"body":"same cause"}'   # 201
# Snooze and Unsnooze of the main Alert Group, and the end of a Snooze, are followed
curl -s -H "$I" -X POST $API/alert-groups/$AG1287/snooze -d '{"duration_seconds":3600}' > /dev/null
curl -s -H "$A" $API/alert-groups/$AG1288 | jq -c '{status, snooze_until}'                         # {"status":"snoozed", ...}
curl -s -X POST $CLK -d '{"advance_seconds": 3700}' > /dev/null; sleep 2
curl -s -H "$A" $API/alert-groups/$AG1288 | jq -r .status                                         # firing
# a new critical Alert in the attached Alert Group is Quiet and changes no status
PUTA e1 c '{"labels":{"alertname":"CheckoutLatencyHigh","severity":"critical"}}'; NOTIFY e1 '{"reason":"critical joins"}'; sleep 2
curl -s -H "$A" "$API/alert-groups/$AG1288/timeline" | jq -c '[.items[-3:][] | {e: .event, l: .loudness, m: .mentions}]'   # every item quiet, mentions []
# the main Alert Group is resolved by hand: #1288 resolves with it, #1290 too
curl -s -H "$I" -X POST $API/alert-groups/$AG1287/resolve -d '{}' > /dev/null
curl -s -H "$A" $API/alert-groups/$AG1288 | jq -c '{status, with: .resolved_with.number, by: .resolved_by.login, a: .attached_to}'   # {"status":"resolved","with":1287,"by":"ivan","a":null}
# the system resolves a main Alert Group while an attached one still has an Alert: detached by the system
# (a second main #1295 with #1296 attached; PUTA resolves of #1295's alerts only)
curl -s -H "$A" "$API/alert-groups/$AG1296/timeline?kind=status" | jq -c '[.items[] | select(.event=="detached") | .reason] | last'   # "#1295 resolved by itself"
psql "$MUSTER_DB" -c "select number, status, attached_to_id is not null as attached, resolved_with_id is not null as with from alert_groups order by number"
```

`test/e2e/attach_follow_test.go` repeats these checks and adds the table of the sources of FR-5 (the end of a Snooze,
auto-unacknowledge after two unanswered Reminders on the main Alert Group, a rise to critical that removes the
acknowledgement, `new_alerts_remove_ack`, an Owner disabled), the first acknowledgement set by following, the Grace
period after a hand resolve, the own Alerts of an attached Alert Group that resolve, an Unresolve that attaches nothing,
and the Acknowledge through the fake gateway for an attached Alert Group (`test/e2e/gateway_acknowledge_test.go`).

## Open questions

None.

## Notes

- Suggested commit: `feat(groups): let attached alert groups follow their main alert group and end the attachment on a resolve`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases. No migration.
- Interim on `edge` between this story and S-204: following sets no chain and starts no timer, but the ack timeout and
  Reminders of an attached Alert Group that S-202 left running are stopped only by S-204; a detached Alert Group's chains
  do not yet start over.
- The main Alert Group resolved by the system records `alert_group_detached` for each Alert Group it detaches, and each of
  those records `detached` with the reason "#{M} resolved by itself" (C-37.FR-6, FR-15: whenever an attached Alert Group is
  detached the main one records it); their pages show it.
- A rise to critical that would have ended an attached Alert Group's Snooze changes nothing, as FR-7 says; its Severity
  level still rises and the Quiet `severity_raised` or `urgency_raised` (removes nothing) records it.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-37.FR-3 | partial | the lock order of a main Alert Group with its attached ones; the pair of Attach and Detach is S-202's |
| C-37.FR-5 | partial | following; starting no chain or timer of its own is completed by S-204 |
| C-37.FR-6 | partial | the three endings of an attachment; the chain and the ack timeout of an Alert Group that the system detaches start over in S-204; the Root message edit "Resolved with #{M}" is S-205's, the statistics S-206's |
| C-37.FR-7 | partial | the status, the loudness and the independence of the main Alert Group; the messenger side of the rule — only an update of its line, a Quiet line in a new Destination — is S-205 |
| C-37.FR-9 | partial | the Commands from any Transport; a bulk command's item outcome is S-206 |
| C-37.FR-15 | partial | `status_followed`, and `detached` and `alert_group_detached` when the system ends an attachment (a resolve of the main Alert Group, or of the attached one's own Alerts); the messenger column S-205, webhooks S-206 |
| C-37.AC-3 | full | |
| C-37.AC-4 | partial | `alert_group_attached`; the bulk Acknowledge is S-206 |
| C-37.AC-5 | partial | the statuses and the entries; the Root message "Resolved with #1287" and the chain at step 1 are S-205 and S-204 |
| C-37.AC-7 | partial | the entries, the status and the main Alert Group; the Quiet line in a new Destination is S-205 |
| C-09.FR-1 | partial | revised for L2: following the main Alert Group's status; the attached state is S-202's |
| C-09.FR-4 | partial | revised for L2: a Reopen never attaches again; the Reopen as a Loud event is S-198's, a Reopen into acknowledged S-199's |
| C-09.FR-6 | partial | revised for L2: new Alerts in an attached Alert Group change no status; the removal of an acknowledgement is S-199's |
| C-09.FR-9 | partial | revised for L2: a rise changes no status of an attached Alert Group; `rise_to_urgent` as a Loud event is S-198's |
| C-09.FR-22 | partial | revised for L2: `status_followed` and the system endings of an attachment; the five rows are S-202's, the messenger column S-205's |
| C-10.FR-1 | partial | revised for L2: the refusal of the other Commands on an attached Alert Group; the Commands Attach and Detach are S-202's |
| C-26.FR-5 | partial | `destination_joined` into a Destination an attached Alert Group reaches is Quiet |
| C-30.FR-6 | partial | an acknowledgement for an attached Alert Group is answered as one for an Alert Group someone owns |
| reference.md Loud and Quiet | partial | revised for L2: every event of an attached Alert Group is Quiet; the messenger side is S-205's |
