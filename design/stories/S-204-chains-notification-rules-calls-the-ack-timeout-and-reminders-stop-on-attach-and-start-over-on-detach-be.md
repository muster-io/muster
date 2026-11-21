---
id: S-204
title: Escalation chains, Notification rules, calls, the ack timeout and Reminders stop on Attach and start over on Detach (BE)
capability: C-37
kind: be
layer: L2
depends_on: [S-203, S-169, S-155, S-157, S-168]
covers: [C-37.FR-4, C-37.FR-5, C-37.FR-8, C-37.AC-2, C-37.AC-5, C-37.AC-6]
files_touched:
  - internal/groups/escalation.go
  - internal/groups/escalation_test.go
  - internal/groups/timers.go
  - internal/groups/timers_test.go
  - internal/groups/attach.go
  - internal/groups/attach_test.go
  - internal/groups/follow.go
  - internal/groups/follow_test.go
  - internal/groups/snooze.go
  - internal/groups/dispatcher.go
  - internal/groups/assignees.go
  - internal/groups/groups_integration_test.go
  - internal/escalation/engine.go
  - internal/escalation/engine_test.go
  - internal/escalation/runs.go
  - internal/escalation/runs_test.go
  - internal/escalation/calls.go
  - internal/escalation/runs_integration_test.go
  - internal/notificationrules/runs.go
  - internal/notificationrules/runs_test.go
  - internal/gateways/calls.go
  - internal/gateways/sender.go
  - internal/gateways/sender_test.go
  - internal/gateways/calls_integration_test.go
  - internal/timers/worker.go
  - docs/concepts/escalation-chains.md
  - test/e2e/attach_chains_test.go
acceptance:
  - "[C-37.FR-4, C-37.AC-2] A firing `#1288` with a running chain, attached to a firing `#1287`: its running runs stop, so no later step is recorded, a queued call through a Notification gateway is not made, the runs of Notification rules for its Pages stop, no ack timeout notice comes for it with the clock advanced by an hour, its Assignees stay as the chain left them and it stays not Unclaimed; once `#1287` is acknowledged, the Assignees of `#1288` are cleared with `assignees_changed` (`escalation_test.go`, `runs_integration_test.go`, `sender_test.go`, `calls_integration_test.go`, `test/e2e/attach_chains_test.go`)."
  - "[C-37.FR-4] Attach also ends the Alert Group's own ack timeout, its Reminders and the end of its Snooze, and its count of unanswered Reminders: an acknowledged `#1288` with Reminders running, attached to a firing `#1287`, sends none, and nothing of its own auto-unacknowledges it; the timer rows are removed and no timer of an attached Alert Group fires with the clock advanced (`timers_test.go`, `test/e2e/attach_chains_test.go`)."
  - "[C-37.FR-5] Following never starts the attached Alert Group's own Escalation chains, ack timeout or Reminders, whatever status it takes: Unacknowledge of the main Alert Group makes the attached one firing and starts none of them, and the main Alert Group's own chains and ack timeout run for it (`follow_test.go`, `escalation_test.go`, `test/e2e/attach_chains_test.go`)."
  - "[C-37.FR-8, C-37.AC-6] Detaching `#1290` while `#1287` is acknowledged by Ivan keeps `#1290` acknowledged, and its Reminders start for Ivan from the Detach: the first Reminder comes 4 hours after it on the development clock; detaching a firing Alert Group starts its chains over at their first step, and where none applies its ack timeout starts over; detaching a snoozed one ends its Snooze at the end it kept (`attach_test.go`, `timers_test.go`, `test/e2e/attach_chains_test.go`)."
  - "[C-37.FR-6, C-37.AC-5] An Alert Group that the system detaches when its main Alert Group resolved by itself, and still has firing Alerts, is firing and its chain starts at step 1 and its ack timeout, where no chain applies, starts over (`attach_test.go`, `test/e2e/attach_chains_test.go`)."
  - "[C-32.FR-5] The table-driven test of the points that start, start over and stop the chains has the rows of an attachment: Attach stops, Detach of a firing Alert Group starts over, Detach of an acknowledged or snoozed one starts none, following starts none (`escalation_test.go`)."
verify: "make ci test-integration e2e-go"
operator_attention: false
issue: null
---

# S-204. Escalation chains, Notification rules, calls, the ack timeout and Reminders stop on Attach and start over on Detach (BE)

## Scope

**IN**

- What stops when an Alert Group is attached: its running Escalation chains, the runs of Notification rules for its Pages,
  its waiting calls through Notification gateways, its ack timeout, Reminders, the end of its Snooze and its count of
  unanswered Reminders; and that its Assignees and Unclaimed change only as its status does (C-37.FR-4).
- What starts again on Detach, by the status it keeps: chains or the ack timeout for a firing one, Reminders for an
  acknowledged one, the end of the Snooze for a snoozed one (C-37.FR-8), also for an Alert Group that the system
  detaches (C-37.FR-6).
- The guard that following starts none of these (C-37.FR-5).

**OUT**

- The state, the Commands and their refusals: S-202. Following and the endings on a resolve: S-203.
- The line and the Thread replies in messengers: S-205. Statistics, the list filter, bulk Attach and webhook events:
  S-206.
- The pages: S-207. The documentation of the escalation page: this story adds one row to
  `docs/concepts/escalation-chains.md`; the capability's own page is S-208.

## Contracts

- **Stopping** (`internal/groups/escalation.go`, `internal/escalation/runs.go`, `engine.go`, `calls.go`,
  `internal/notificationrules/runs.go`, `internal/gateways/calls.go`, `sender.go`): Attach stops every running run of
  the Alert Group in its transaction as at a start-over — the stop command is the existing `started_over`, so
  `escalation_runs.stop_command` needs no new CHECK value — with the entry `attached` as the event that stopped them
  (`ChainStop.EventSeq`); the runs of Notification rules that those runs started stop with them, so that the stop check
  of C-30.FR-4 drops the waiting calls and a call the chain queued is not sent; the engine's check before each step also
  reads that the Alert Group is attached, so a step due at the moment of the Attach is not made. Its Assignees and
  Unclaimed stay as they are; they change when the status does (S-202, S-203).
- **Timers** (`internal/groups/timers.go`, `snooze.go`, `internal/timers/worker.go`): the timer rows of the ack timeout,
  the Reminders and the end of a Snooze of an attached Alert Group are removed by the Attach and not set while it is
  attached; a timer that fires for an Alert Group found attached does nothing and is not claimed again (as a timer whose
  subject changed, `errNotDue`); the count of unanswered Reminders and `reminders_sent` are zeroed, so that auto-
  unacknowledge counts nothing from before.
- **Following** (`follow.go`): the transitions it applies set `noChains` and the timer code skips an attached Alert
  Group, so that whatever status the attached Alert Group takes it starts no chain, ack timeout or Reminder of its own;
  the main Alert Group's own chains and ack timeout run for it as they do.
- **Detach** (`attach.go`): a Detach, by a person or by the system, is a point at which the Alert Group becomes firing
  again when it is firing (ADR-0004, C-17.FR-1, C-32.FR-5): `syncChains` starts the chains that apply over at their first
  step (a new `chain_generation`), and where none applies the ack timeout starts over from `route.ack_timeout`; kept
  acknowledged it sets `acknowledged_at` to the Detach, so that its Reminders count from it and the first comes
  `route.reminders` first interval (4 hours) later; kept snoozed it sets the end-of-Snooze timer at the end it kept, or
  none for a Snooze without an end. Unclaimed is evaluated as at any return to firing.
- **Interplay with C-34**: an Alert Group that is also kept back by a Maintenance window starts nothing on Detach until
  the window ends, as S-193 says; the two guards are one predicate in `escalation.go` and `timers.go`, whichever of S-193
  and this story merges second extends it.
- **Documentation**: `docs/concepts/escalation-chains.md` gains the row "An attached Alert Group" in the table of what
  stops the chains, pointing to the page of S-208.
- **Settings**: `route.ack_timeout`, `route.reminders`, `route.auto_unacknowledge`.
- **Log events and metrics**: none new.

## Steps

1. Make the stopping of runs, rules and calls part of the Attach transition and the engine's step check. Check:
   `escalation_test.go`, `runs_test.go`, `engine_test.go`, `sender_test.go`, `calls_integration_test.go`.
2. Remove and refuse the timers of an attached Alert Group. Check: `timers_test.go`.
3. Make following start nothing. Check: `follow_test.go`.
4. Write the three Detach cases and the system detach. Check: `attach_test.go`, `groups_integration_test.go`,
   `runs_integration_test.go`.
5. Write `test/e2e/attach_chains_test.go` and the escalation page's row. Check: the Verification below;
   `make docs-check` for the row.

## Verification

```sh
make dev > dev.log 2>&1 &
API=localhost:8080/api/v1; CLK=localhost:8082/_dev/clock   # A: Admin; I: Ivan; the Route "payments" has a chain (page ivan, wait 5 min, page the Team) and the fake gateway; #1287 on "databases"
# #1288 (firing, chain step 1 made, a call queued) is attached to #1287 (firing)
curl -s -H "$I" -X POST $API/alert-groups/$AG1288/attach -d "{\"main_alert_group_id\":\"$AG1287\"}" > /dev/null
curl -s -H "$A" "$API/alert-groups/$AG1288" | jq -c '{chains: [.escalation_chains[] | .state], unclaimed}'   # {"chains":["stopped"],"unclaimed":false}
curl -s -X POST $CLK -d '{"advance_seconds": 3600}' > /dev/null; sleep 3
curl -s -H "$A" "$API/alert-groups/$AG1288/timeline?kind=escalation,timers" | jq -c '[.items[].event]'   # no step, no ack_timeout after the attach
curl -s 127.0.0.1:18097/_fake/notifications | jq 'length'                                        # the calls made before the attach only
psql "$MUSTER_DB" -c "select kind from timers where alert_group_id = (select id from alert_groups where number = 1288)"   # no ack_timeout, reminder or snooze_end row
# the main Alert Group is acknowledged: the Assignees of #1288 are cleared
curl -s -H "$I" -X POST $API/alert-groups/$AG1287/acknowledge > /dev/null
curl -s -H "$A" "$API/alert-groups/$AG1288/timeline?kind=escalation" | jq -c '[.items[].event] | last'   # "assignees_changed"
# Unacknowledge of the main Alert Group starts none of #1288's chains
curl -s -H "$I" -X POST $API/alert-groups/$AG1287/unacknowledge > /dev/null; sleep 1
curl -s -H "$A" "$API/alert-groups/$AG1288" | jq -c '{status, chains: [.escalation_chains[] | .state]}'   # {"status":"firing","chains":["stopped"]}
# Detach while the main one is acknowledged: kept acknowledged, Reminders from the Detach
curl -s -H "$I" -X POST $API/alert-groups/$AG1287/acknowledge > /dev/null
curl -s -H "$I" -X POST $API/alert-groups/$AG1290/detach | jq -c '{status, owner: .owner.login}'   # {"status":"acknowledged","owner":"ivan"}
curl -s -X POST $CLK -d '{"advance_seconds": 14500}' > /dev/null; sleep 3
curl -s -H "$A" "$API/alert-groups/$AG1290/timeline?kind=timers" | jq -c '[.items[].event]'        # ["reminder"]  (first, 4 hours after the Detach)
# Detach of a firing Alert Group: chains start at step 1
curl -s -H "$I" -X POST $API/alert-groups/$AG1288/detach > /dev/null
curl -s -H "$A" "$API/alert-groups/$AG1288/timeline?kind=escalation" | jq -c '[.items[] | select(.event=="paged")] | last | .step'   # 1
```

`test/e2e/attach_chains_test.go` repeats these checks and adds the Notification rule run and the waiting call dropped on
Attach, a Snooze without an end kept through Detach, the system detach starting a chain at step 1, auto-unacknowledge
counting nothing from before an Attach, and a chain with `E2E_REPLICAS=2`.

## Open questions

None.

## Notes

- Suggested commit: `feat(groups): stop the chains and timers of an attached alert group and start them over on detach`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases. No migration: the stop
  command `started_over` already exists.
- The chain page's "How it went" shows a run stopped by an Attach as stopped at the `attached` entry, with the existing
  rendering of `started_over`; S-207 adds nothing for it.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-37.FR-4 | full | the status taken is S-202's |
| C-37.FR-5 | partial | the chains, ack timeout and Reminders never start by following; the status itself is S-203's |
| C-37.FR-8 | full | |
| C-37.AC-2 | full | |
| C-37.AC-5 | partial | the chain at step 1 after a system detach; the statuses and the Root message are S-203 and S-205 |
| C-37.AC-6 | partial | Ivan keeps the Alert Group and the first Reminder comes 4 hours after the Detach; the Root message edited back is S-205 |
