---
id: S-157
title: Pages through a Notification gateway from Escalation steps and Notification rules, queued as calls (BE)
capability: C-32
kind: be
layer: L2
depends_on: [S-149, S-156, S-168, S-173]
covers: [C-32.FR-3, C-32.FR-10, C-32.FR-11, C-32.FR-12, C-32.FR-14, C-32.AC-7, C-30.FR-4, C-30.FR-7, C-30.FR-9, C-31.FR-3, C-31.AC-2, C-31.AC-6]
files_touched:
  - api/openapi.yaml
  - internal/db/migrations/0044_gateway_pages.up.sql
  - internal/db/migrations/0044_gateway_pages.down.sql
  - design/db/schema.md
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - internal/escalation/calls.go
  - internal/escalation/calls_test.go
  - internal/escalation/engine.go
  - internal/notificationrules/runs.go
  - internal/notificationrules/runs_test.go
  - internal/gateways/calls.go
  - internal/gateways/sender.go
  - internal/gateways/calls_integration_test.go
  - internal/gateways/query.sql
  - internal/groups/pages.go
  - internal/groups/assignees.go
  - internal/groups/assignees_test.go
  - internal/groups/events_test.go
  - internal/runtime/runtime.go
  - test/e2e/gateway_pages_test.go
  - docs/concepts/escalation-chains.md
  - docs/concepts/notification-rules.md
acceptance:
  - "[C-31.FR-3, C-31.AC-2, C-31.AC-6, C-30.FR-4] An important Page of a person whose rule is \"Call\" Mentions them in the Thread and calls their confirmed contact through the fake gateway at once; a person without a confirmed contact at that gateway gets the Mentions, and the Timeline records the call action skipped with the reason `no_confirmed_contact` (`runs_test.go`, `gateway_pages_test.go`)."
  - "[C-32.FR-3, C-30.FR-4] A `call` step calls the On-call of its Schedule, or each person it names, through its gateway, as a call of the queue of S-148 with combining; each call records the Quiet `paged` (Timeline kind `escalation`, nothing in a messenger) naming the person and the gateway, and writes nothing in any Thread (`calls_test.go`)."
  - "[C-32.FR-14, C-30.FR-9] A `call` step or a rule's call action whose gateway was deleted is skipped with `gateway_deleted`, and a person without a confirmed contact there with `no_confirmed_contact`; the chain goes on with its next step at once (`calls_test.go`, `runs_test.go`)."
  - "[C-30.FR-4] Right before its first attempt a call drops the Alert Groups whose chain or rule run that Paged has stopped — acknowledged, snoozed, resolved or started over — and a call left without any is not sent; once every Alert Group of a call being retried has stopped, its retries end (`calls_integration_test.go`)."
  - "[C-32.FR-12, C-32.FR-10, C-30.FR-7] A call that the gateway accepted makes the person an Assignee of the chain, like a delivered Mention; a call that ends Not delivered records `page_not_delivered` with the chain and the step and leaves the Assignees as they were, and the chain and the rules go on; calls depend on no Root message and keep working while a messenger is down or a Destination Broken (`assignees_test.go`, `calls_integration_test.go`)."
  - "[C-32.AC-7] A Route with a chain and no Destination starts the chain when the Alert Group is created; its Mention steps record `page_not_delivered` `no_root_message`, and its `call` step calls the fake gateway (`gateway_pages_test.go`)."
  - "[C-32.FR-11] Pages through a gateway count in `muster_pages_total` with `kind` `call`; Concepts → Escalation chains and Notification rules describe the calls of steps and rules, combined per person and gateway, and the row \"Notification gateway\" of C-32.FR-11; `make docs-check` passes."
verify: "make ci test-integration e2e-go docs-check"
operator_attention: false
issue: null
---

# S-157. Pages through a Notification gateway from Escalation steps and Notification rules, queued as calls (BE)

## Scope

**IN**

- The `call` step and the call actions of Notification rules, queued as calls of S-148 with their stop checks
  (C-32.FR-3, C-31.FR-3, C-30.FR-4).
- `paged` for a call, its outcome as a Page — an Assignee when accepted, `page_not_delivered` when Not delivered — and
  calls that work without a Root message or a messenger (C-32.FR-10, FR-12; C-30.FR-7).
- The skips of a gateway or a confirmed contact that is gone (C-32.FR-14, C-30.FR-9).

**OUT**

- The queue, combining and retries: S-148; the Acknowledge through a gateway: S-173. Broken gateways: S-149.
- The pages: S-158, S-159. The documentation of "How an alert reaches people": S-161.

## Contracts

- **Spec** (`api/openapi.yaml`): `EscalationSkipReason` gains `gateway_deleted` and `no_confirmed_contact`;
  `EscalationRun.steps[].pages[]` gains the `kind` `call` with its `gateway`; `TimelineEscalationEntry` names the
  `gateway` of a call's `paged`.
- **Schema** (migration `0044_gateway_pages`, expand only; `schema.md` §4.17, §4.18): `gateway_call_items` gains
  `escalation_run_id` (FK, `ON DELETE SET NULL`), `notification_rule_run_id` (FK, `ON DELETE SET NULL`) and `page_id`
  (FK `pages`, cascade); `pages` gains `gateway_connection_id`. The down migration drops them.
- **Packages**: as S-169 and S-155 state them; `escalation` and `notificationrules` import `gateways`, which imports
  `groups` and `delivery` and none of them.
- **Escalation** (`internal/escalation/calls.go`, `engine.go`): the `call` step through the `StepPages` of S-169 — the
  On-call of its Schedule at the business clock's now or each person it names who may still be Paged; for each, the
  Command `Page` of the kind `call`, which records the Quiet `paged`, and `gateways.Page` with the run's stop check:
  the run is still running in the same generation.
- **Notification rules** (`internal/notificationrules/runs.go`): the `CallPages` of S-155 is wired to `gateways.Page`
  in the runtime; a call action is a Page of the kind `call` through the
  action's gateway with the rule run's stop check; skipped with its reason when the gateway is gone or the person has no
  confirmed contact there.
- **Gateways** (`internal/gateways/calls.go`, `sender.go`): the stop check of an item is the one its Page passed; an
  accepted call dispatches the Command `PageReached` to `groups` for its Alert Groups' Pages, and a call that ends
  Not delivered `PageNotDelivered` with the chain and the step.
- **Groups** (`internal/groups/pages.go`, `assignees.go`): `paged` of a call (Quiet, kind `escalation`); the Assignees
  of C-32.FR-12 from accepted calls, like delivered Mentions (S-168).
- **Metric**: `muster_pages_total{chain,kind="call",outcome}`, registered by S-155.
- **Settings**: `gateway.combine_window`, `delivery.transient_budget`.
- **Log events**: none new; the queue's are S-148's.
- **Documentation**: Concepts → Escalation chains — the `call` step and the row "Notification gateway" of the table of
  C-32.FR-11; Concepts → Notification rules — calls in rules, the skip without a contact, and combining.

## Steps

1. Write the migration and `schema.md`. Check: `make test-integration` runs up, down and up.
2. Write the `CallPages` of steps and the call actions of rules with their skips. Check: `calls_test.go`,
   `runs_test.go`.
3. Pass the stop checks and the outcomes through the queue; make Assignees of accepted calls. Check:
   `calls_integration_test.go`, `assignees_test.go`, `events_test.go`.
4. Write `test/e2e/gateway_pages_test.go` and the documentation. Check: the Verification below; `make docs-check`.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-155 and S-152: the chain "payments" with step 3 important for critical; petr linked in Mattermost, On-call of
# payments' Main, his contact confirmed at the demo gateway GW and his rules in the state "call"; ivan without a
# contact, rules "organization"; A, CLK.
API=localhost:8080/api/v1; FB=127.0.0.1:18094/_fake; CLK=localhost:8082/_dev/clock
curl -s -X PUT $FAM/groups/c1 -d '{"receiver":"sev","route":"{}","labels":{"alertname":"CheckoutQueueBacklog"}}' > /dev/null
PUTA c1 a '{"labels":{"category":"infra","system":"checkout","team":"payments","severity":"critical"}}'; NOTIFY c1 '{"reason":"first notification"}'
G=$(AG 'system%3D%22checkout%22'); sleep 1
N0=$(curl -s $FB/notifications | jq length)
curl -s -X POST $CLK -d '{"advance_seconds": 600}' > /dev/null; sleep 2           # step 3: petr importantly
curl -s $FB/notifications | jq -c '[.[] | select(.contact == "+10000000002")] | last | {language, a: (.action != null)}'   # {"language":"en","a":true}
curl -s "${A[@]}" "$API/alert-groups/$G/timeline?kind=escalation" | jq -c '[.items[] | select(.event == "paged") | {u: [.paged_users[].login], g: .gateway.name}]'
# [{"u":["petr"],"g":null},{"u":["petr"],"g":"Example Gateway"},…]
curl -s "${A[@]}" $API/alert-groups/$G | jq -c '[.assignees[] | .user.login] | unique'      # ["petr"]
# petr presses 1 in the call (S-148): the Alert Group is his and the chains stop
curl -s -X POST $FB/acknowledge -d "{\"notification_id\":\"$(curl -s $FB/notifications | jq -r 'last.notification_id')\"}"; sleep 2
curl -s "${A[@]}" $API/alert-groups/$G | jq -c '{status, o: .owner.login, s: [.escalation_chains[].state] | unique}'
# {"status":"acknowledged","o":"petr","s":["ran_out","stopped"]}   (devops ran out at +600 s; payments stopped)
curl -s localhost:8082/metrics | grep '^muster_pages_total{' | grep -c 'kind="call"'          # 1
```

`test/e2e/gateway_pages_test.go` repeats these checks and adds ivan, whose rule has no call; a person with "Call" but
no confirmed contact (`no_confirmed_contact`); a `call` step on a Route with no Destination (C-32.AC-7); a call step
whose gateway was deleted (`gateway_deleted`); two Alert Groups Paging petr within the window, combined in one call,
the second dropped once its chain started over; and a call that ends Not delivered leaving the Assignees as they were.

## Open questions

None.

## Notes

- Suggested commit: `feat(escalation): page people through notification gateways from steps and rules`.
- A call writes nothing in a Thread (C-32.FR-9); the Thread reply of the same step, when there is one, already names the
  person.
- Migration `0044` follows the story map; a story merged out of that order renumbers its migration (S-167's Notes).

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-32.FR-3 | partial | the `call` step |
| C-32.FR-10 | partial | calls without a Root message; Mentions are S-155 |
| C-32.FR-11 | partial | the row "Notification gateway"; the push facts are S-160 |
| C-32.FR-12 | partial | Assignees by accepted calls |
| C-32.FR-14 | partial | a gateway or a contact that is gone |
| C-32.AC-7 | partial | the gateway step; S-155 the Mention steps |
| C-30.FR-4 | partial | the calls of rules and steps; the queue is S-148 |
| C-30.FR-7 | partial | the chain and the rules go on |
| C-30.FR-9 | partial | the skip of a deleted gateway |
| C-31.FR-3 | partial | call actions |
| C-31.AC-2 | full | |
| C-31.AC-6 | partial | an important Page calls at once |
