---
id: S-193
title: Escalation chains, Notification rules, calls, the ack timeout and Reminders while an Alert Group is kept back (BE)
capability: C-34
kind: be
layer: L2
depends_on: [S-192, S-169, S-155, S-156, S-157, S-168]
covers: [C-34.FR-7, C-34.FR-14, C-34.FR-15, C-34.FR-18, C-34.FR-19, C-34.AC-3, C-34.AC-5, C-34.AC-6, C-34.AC-8, C-34.AC-9, C-34.AC-10, C-34.AC-14, C-32.FR-5]
files_touched:
  - internal/groups/escalation.go
  - internal/groups/escalation_test.go
  - internal/groups/timers.go
  - internal/groups/timers_test.go
  - internal/groups/maintenance.go
  - internal/groups/maintenance_test.go
  - internal/groups/dispatcher.go
  - internal/groups/assignees.go
  - internal/groups/groups_integration_test.go
  - internal/escalation/engine.go
  - internal/escalation/engine_test.go
  - internal/escalation/runs.go
  - internal/escalation/runs_test.go
  - internal/escalation/pages.go
  - internal/escalation/calls.go
  - internal/escalation/webhook.go
  - internal/escalation/webhook_test.go
  - internal/escalation/runs_integration_test.go
  - internal/notificationrules/runs.go
  - internal/notificationrules/runs_test.go
  - internal/gateways/calls.go
  - internal/gateways/sender.go
  - internal/gateways/sender_test.go
  - internal/gateways/calls_integration_test.go
  - internal/timers/worker.go
  - internal/runtime/runtime.go
  - internal/runtime/runtime_test.go
  - docs/concepts/escalation-chains.md
  - test/e2e/maintenance_chains_test.go
acceptance:
  - "[C-34.FR-14, C-34.AC-3, C-34.AC-6] While an Alert Group is kept back no Escalation chain starts on it and no step is made, no action of a Notification rule and no call through a Notification gateway is made for it, no `escalation_webhook` is sent and its ack timeout sends no notice (the Alert Group of C-34.AC-3 records no step of its Route's chain); a firing Alert Group of a Route with a chain and no Destination, created during a window of that Route, starts no chain at its creation, as C-32.FR-5 would otherwise have it, and its chain starts when the window ends, its gateway step calling the fake gateway (`escalation_test.go`, `engine_test.go`, `test/e2e/maintenance_chains_test.go`)."
  - "[C-34.FR-15, C-34.AC-9] When an Alert Group becomes kept back, the Escalation chains running on it stop as at a start-over, and so do the runs of the Notification rules they started, so that the stop check of C-30.FR-4 drops their waiting calls: a call that the chain queued is not sent; its running ack timeout stops; its Assignees and Unclaimed stay as they are (`escalation_test.go`, `runs_integration_test.go`, `sender_test.go`, `calls_integration_test.go`)."
  - "[C-34.FR-18, C-34.FR-19, C-34.AC-9, C-34.AC-5] When it stops being kept back while firing and nobody owns it, its chains — or its ack timeout where no chain applies — start over at their first step, once, at the same moment as the Publication or the Thread reply of S-192; for one never published they start at its first Publication, and where no Destination can publish it when it stops being kept back; its first ack timeout notice comes `route.ack_timeout` after it; one acknowledged or snoozed during the window starts none and gets its Reminders (`escalation_test.go`, `timers_test.go`, `test/e2e/maintenance_chains_test.go`)."
  - "[C-34.FR-7, C-34.AC-8] A Team that joins an Alert Group whose chains run leaves them running with no step skipped, whether or not the Team is in a window; an Alert Group that is no longer kept back because a Team joined starts its chains at that Publication or Thread reply only when it is firing and nobody owns it (`escalation_test.go`, `test/e2e/maintenance_chains_test.go`)."
  - "[C-34.FR-15, C-34.AC-10] An acknowledged Alert Group kept back past its next Reminder gets no Reminder then; with auto-unacknowledge on, that Reminder does not count as unanswered, and the next one after the window is sent as scheduled; the Reminders keep their schedule (`timers_test.go`, `test/e2e/maintenance_chains_test.go`)."
  - "[C-34.FR-14, C-34.AC-14] A Route whose only Destination is an outgoing webhook in events mode starts no ack timeout and no chain while the Alert Group is kept back, since a delivered `created` event is not its Publication (`timers_test.go`, `test/e2e/maintenance_chains_test.go`)."
  - "[C-32.FR-5] The table-driven test of the points that start, start over and stop the chains has the rows of a Maintenance window: becoming kept back stops, ending while firing and unowned starts over, ending while acknowledged or snoozed starts none, a Team that joins stops none (`escalation_test.go`)."
verify: "make ci test-integration e2e-go docs-check"
operator_attention: false
issue: null
---

# S-193. Escalation chains, Notification rules, calls, the ack timeout and Reminders while an Alert Group is kept back (BE)

## Scope

**IN**

- A kept-back Alert Group starts no chain and makes no step, no Notification rule action, no call, no
  `escalation_webhook`, no ack timeout notice (C-34.FR-14), including the creation with no Destination that can publish
  it.
- Becoming kept back stops the running chains, the runs of their Notification rules, the queued calls and the ack timeout,
  and leaves the Assignees and Unclaimed alone (C-34.FR-15).
- Reminders keep their schedule but are not sent while kept back and do not count as unanswered (C-34.FR-15).
- At the end the chains or the ack timeout start over for a firing Alert Group nobody owns, at the points of C-32.FR-5
  (C-34.FR-18, FR-19), and a joining Team never stops running chains (C-34.FR-7).
- The row of the table of `C-32.FR-5` for a Maintenance window, in the documentation of Escalation chains.

**OUT**

- The state and events: S-191. What is published and sent: S-192. The pages: S-194, S-195.

## Contracts

- **Groups** (`internal/groups/escalation.go`, `timers.go`, `maintenance.go`, `dispatcher.go`): the dispatcher's
  `syncChains` and `syncTimers` read the Alert Group's `kept_back` (S-191) and after each transition either start, start
  over, stop or leave the chains, the ack timeout and the Reminders as the rows of C-32.FR-5 and C-34.FR-14 to FR-19 say:
  a transition that makes it kept back asks `ChainRuns` to stop every running chain and the runs of the Notification
  rules they started, and deletes the ack timeout timer; one that ends it while firing and unowned asks them to start
  over (with `chain_generation` raised as at any start-over), or the ack timeout to start; `startUnreached` does not start
  chains for a kept-back Alert Group created with no Destination that can publish it, and does at its release.
  `groups` still imports none of `escalation`, `notificationrules`, `gateways` or `delivery`; it uses the interfaces
  S-169 declared.
- **Reminders** (`internal/groups/timers.go`): `reminderDue.apply` skips the Alert Group while it is kept back, reschedules
  nothing beyond its schedule and does not mark the Reminder unanswered for auto-unacknowledge (`unanswered`).
- **Escalation, Notification rules, gateways** (`internal/escalation/engine.go`, `runs.go`, `pages.go`, `calls.go`,
  `webhook.go`; `internal/notificationrules/runs.go`; `internal/gateways/calls.go`, `sender.go`): a step due for a
  kept-back Alert Group is not made — its run is already stopped by the transition, and the engine, locking the Alert
  Group through the dispatcher, does nothing when it finds it kept back or the run no longer `running`; a waiting call of
  a stopped run is dropped by the stop check of C-30.FR-4; `escalation_webhook` is not sent. This extends the stop checks.
  The runs that become kept back stop with the existing stop command `started_over` (C-34.FR-15: "as at a start-over"),
  as S-204 does for an attach: `escalation_runs.stop_command` and the spec enum `EscalationRunStop.command` need no new
  value, so there is no migration and no change to `api/openapi.yaml`.
- **Timers** (`internal/timers/worker.go`, `internal/runtime/runtime.go`): no new kind; the handlers read the state.
- **Log events**: the existing `escalation_run_stopped` and `escalation_run_started` of S-169 are used as they are: a
  run stopped by becoming kept back logs the stop command `started_over`, and a start over at the end of a window logs
  `escalation_run_started` as any other start over; no event is added.
- **Documentation**: Concepts → Escalation chains gains the row "A Maintenance window" in "When chains run" and the
  sentence on the ack timeout in "Chains and the ack timeout"; the page for the whole capability is S-196's.

## Steps

1. Add the rows to the table-driven test of the points that start, restart and stop chains. Check: `escalation_test.go`
   fails first, then passes.
2. Wire the dispatcher: stopping at becoming kept back, starting over at the end, the creation with no Destination, the
   Team that joins. Check: `escalation_test.go`, `timers_test.go`, `groups_integration_test.go`.
3. Make the engine, the Notification rules, the gateways' sender and `escalation_webhook` do nothing for a kept-back Alert
   Group, and drop waiting calls. Check: `engine_test.go`, `runs_test.go`, `sender_test.go`,
   `calls_integration_test.go`.
4. Reminders and auto-unacknowledge. Check: `timers_test.go`.
5. Write the documentation row and `test/e2e/maintenance_chains_test.go`. Check: the Verification below;
   `make docs-check`.

## Verification

```sh
make dev > dev.log 2>&1 &
API=localhost:8080/api/v1; CLK=localhost:8082/_dev/clock; FBR=127.0.0.1:18094/_fake
# as in S-169: the chain "payments" (CP: page the On-call, wait 10, a gateway call) named by the Route R; a published,
# firing, unowned G with its chain at step 2
curl -s -H "$A" $API/alert-groups/$G | jq -c '[.escalation_chains[] | {s: .step.number, st: .state}]'   # [{"s":2,"st":"running"}]
W=$(curl -s -H "$A" -X POST $API/maintenance-windows -d '{...Route R, now+5 min, end now+30 min...}' | jq -r .id)
curl -s -X POST $CLK -d '{"advance_seconds": 330}' > /dev/null; sleep 2
curl -s -H "$A" $API/alert-groups/$G | jq -c '[.escalation_chains[].state] | unique'                      # ["stopped"]
curl -s -H "$A" $API/alert-groups/$G | jq -r .unclaimed                                                  # false (Assignees stay)
curl -s $FBR/notifications | jq length                                                                    # the queued call was not sent: 0
# an Alert joins: no Thread reply; the ack timeout has stopped
curl -s -X POST $CLK -d '{"advance_seconds": 1500}' > /dev/null; sleep 2                                 # the window ended
curl -s -H "$A" $API/alert-groups/$G | jq -c '[.escalation_chains[] | {s: .step.number, st: .state}]'     # [{"s":1,"st":"running"}] started over
curl -s -H "$A" "$API/alert-groups/$G/timeline?kind=escalation" | jq '[.items[] | select(.event == "paged")] | length'   # the first step again
# an Alert Group created during a window on a Route with a chain and no Destination: no chain until the end, then its gateway step calls the fake gateway
curl -s -H "$A" $API/alert-groups/$G2 | jq -c '[.escalation_chains | length]'                           # [0] while kept back, then [1]
curl -s $FBR/notifications | jq length                                                                    # 1 after the end
# an acknowledged Alert Group past its Reminder: none sent; advance the clock again: the next one is sent
```

`test/e2e/maintenance_chains_test.go` repeats these checks and adds: the Team joining an Alert Group whose chain runs with
no step skipped (C-34.AC-8), the Reminder and auto-unacknowledge (C-34.AC-10), the events-mode webhook as the only
Destination (C-34.AC-14), the chain of a Team and of a Route on one Alert Group, and `E2E_REPLICAS=2`.

## Open questions

None.

## Notes

- Suggested commit: `feat(escalation): stop escalation chains and timers of kept-back alert groups and start them over at the end of a window`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases. No migration.
- With S-191 to S-193 merged the behaviour of C-34 is complete in the backend; before it, on `edge`, do not open windows
  on an installation that relies on chains (S-191 Notes).
- The check of a Notification gateway's stop (C-30.FR-4) is existing behaviour that this story extends; it adds no
  setting.
- C-32.FR-5's table gains its Maintenance-window rows here; the half of that FR about new Alerts that remove an
  acknowledgement is C-36's, a later batch.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-34.FR-7 | partial | a joining Team never stops chains; the rule is completed by S-191 |
| C-34.FR-14 | full | |
| C-34.FR-15 | full | |
| C-34.FR-18 | partial | the chains and the ack timeout; the Publication with the mark is completed by S-192 |
| C-34.FR-19 | partial | the chains starting over; the Thread reply and `destination_joined` are completed by S-192 |
| C-34.AC-3 | partial | no step of the Route's chain is recorded; the rest is completed by S-191 and S-192 |
| C-34.AC-5 | partial | the chain and the ack timeout notice; the publications and the Storm are completed by S-192 |
| C-34.AC-6 | full | |
| C-34.AC-8 | partial | the chains left running; the rule and the event are completed by S-191 |
| C-34.AC-9 | partial | the chain; the times (S-190), the decision (S-191), the Root message and Thread reply (S-192) complete it |
| C-34.AC-10 | full | |
| C-34.AC-14 | partial | no chain until the end; the events are completed by S-192 |
| C-32.FR-5 | partial | the Maintenance-window rows of its table |
