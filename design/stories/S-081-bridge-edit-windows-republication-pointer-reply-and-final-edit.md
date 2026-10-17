---
id: S-081
title: "Bridge edit windows: republication on a change of status, the pointer reply and the final edit as a Thread reply (BE)"
capability: C-22
kind: be
layer: L1
depends_on: [S-074]
covers: [C-22.FR-8, C-22.FR-9, C-22.FR-23, C-22.FR-24, C-22.AC-8, C-22.AC-9]
files_touched:
  - api/openapi.yaml
  - internal/db/migrations/0010_bridge_edit_windows.up.sql
  - internal/db/migrations/0010_bridge_edit_windows.down.sql
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - design/db/schema.md
  - design/prd/l1/reference.md
  - internal/bridge/delivery.go
  - internal/bridge/delivery_test.go
  - internal/delivery/delivery.go
  - internal/delivery/destinationtest.go
  - internal/delivery/enqueue.go
  - internal/delivery/membership_test.go
  - internal/delivery/live_test.go
  - internal/delivery/webhookevents.go
  - internal/delivery/outcomes.go
  - internal/delivery/outcomes_test.go
  - internal/delivery/worker.go
  - internal/delivery/worker_test.go
  - internal/delivery/editwindow.go
  - internal/delivery/editwindow_test.go
  - internal/delivery/events.go
  - internal/delivery/query.sql
  - internal/delivery/lockorder_integration_test.go
  - internal/api/alertgroups.go
  - internal/api/alertgroups_test.go
  - internal/messages/texts/en.json
  - internal/messages/texts/ru.json
  - internal/fakes/fakebridge/fakebridge_test.go
  - internal/logging/events.go
  - docs/messengers/bridge.md
  - test/e2e/bridge_test.go
  - web/src/components/alert-group-deliveries.test.tsx
acceptance:
  - "[C-22.FR-9, C-22.AC-8] With `edit_max_age_seconds` 600, once the development clock is 11 minutes past the Publication, Acknowledge posts a new Root message Quietly, replies \"This message is no longer updated. The current one: {url}\" in the old Thread, and sends the next Thread reply under the new Root message; a new Alert past the window adds a Thread reply and makes no edit and no Publication; `listAlertGroupDeliveries` shows `past_edit_window` true meanwhile, with the state `delivered`."
  - "[C-22.FR-8, C-22.FR-9, C-22.AC-9] An edit refused as `gone` with the `reason` `edit_window_closed` before the declared window ends as `OutcomeEditWindowClosed`, not the deleted Root message flow: it marks the Root message as past its window, and the next change of status republishes as above."
  - "[C-22.FR-9] The final edit (C-11.FR-14) of a Root message past its window is a Thread reply with the same text, key `final-<Destination public_id>-<delivery id>`; the pointer reply has the key `pointer-<Destination public_id>-<delivery id>-<publications>` and is not retried; a republished delivery keeps the ids of its older Root messages in `superseded_message_ids`, which S-075 binds presses and Text commands to."
  - "[C-22.FR-9] The window is measured on the business clock from `published_at`, less `bridge.edit_window_margin`; a Desired state whose status equals the actual one makes no call past the window, and the delivery stays delivered (`editwindow_test.go`, with a manual clock)."
  - "[C-22.FR-9] `past_edit_window` is a required member of `AlertGroupDelivery`, and the SPA's type check passes: the fixture `delivery()` of `alert-group-deliveries.test.tsx` sets it to false (`make lint`)."
  - "[C-22.FR-8] Every `switch` over `OutcomeKind` names `OutcomeEditWindowClosed` — `errorClass` and `metricOutcome` count it as neither an error nor a delivery, `ErrorClass` of the Destination test as `unknown`, the events mode of outgoing webhooks as unknown — so `golangci-lint`'s `exhaustive` passes."
  - "[C-22.FR-24] Each republication past an edit window is the delivery event `republished_past_edit_window` and logs `bridge_republished_past_edit_window` with the Destination, the Alert Group and the old and new message ids, registered with its reference page."
  - "[C-22.FR-23] Messengers → Bridge explains edit windows: when Muster stops editing, republication on a change of status, the pointer reply and the old message that keeps working; `make docs-check` passes."
verify: "make ci test-integration e2e docs-check"
operator_attention: false
issue: 239
---

# S-081. Bridge edit windows: republication on a change of status, the pointer reply and the final edit as a Thread reply (BE)

## Scope

**IN**

- No edits of a Bridge Root message past `edit_max_age_seconds` less `bridge.edit_window_margin`.
- Republication on a change of status, Quietly, with the pointer reply under the old Root message; later Thread replies
  under the new one; the older Root messages kept for presses and Text commands.
- The `edit_window_closed` reason of `gone`, and the final edit of a Root message past its window as a Thread reply.
- The flag `past_edit_window` on Alert Group deliveries and the delivery event `republished_past_edit_window`.
- The section on edit windows of Messengers → Bridge.

**OUT**

- Presses and Text commands on older Root messages (S-075), which read `superseded_message_ids`.
- The text of the flag and of the delivery event on the Alert Group page (S-077).

## Contracts

- **Spec** (`api/openapi.yaml`, changed here first, then generated): `AlertGroupDelivery` gains the boolean
  `past_edit_window` beside `thread_not_attached` and `possible_duplicate`, required like them; `DeliveryEventKind`
  gains `republished_past_edit_window`. `DeliveryState` does not change: a delivery past its window is `delivered` or
  `pending` as any other. The SPA's fixture `delivery()`, typed `AlertGroupDelivery`, in
  `alert-group-deliveries.test.tsx` gains `past_edit_window: false`, since the type check of `make lint` includes the
  tests; the text of the flag on the page is S-077's.
- **Schema** (migration `0010_bridge_edit_windows`, expand only, `schema.md`): `deliveries` gains `actual_status`,
  `past_edit_window` (boolean, default false) and `superseded_message_ids` (`text[]`, default empty), with a partial GIN
  index on `superseded_message_ids` where it is not empty, so that S-075 finds the delivery of an older Root message
  without scanning the Connection's deliveries; the `kind` CHECK of `delivery_events` gains
  `republished_past_edit_window`. `delivery_events` is partitioned: the new CHECK is added to the parent `NOT VALID`
  and then validated, which recurses to every partition on PostgreSQL 14 and 17 alike, and the old CHECK is dropped
  after it; the migration's `lock_timeout` bounds the wait for the parent's lock, and `make test-integration` runs
  `up → down → up` on both versions. Old replicas keep working: they never write the new kind, and the new columns
  have defaults.
- **Outcome** (`delivery.go`, `outcomes.go`, `destinationtest.go`, `webhookevents.go`): a kind
  `OutcomeEditWindowClosed`, which the Bridge delivery returns for `gone` with `reason` `edit_window_closed` on an edit.
  Each `switch` over `OutcomeKind` names it: `errorClass` and `metricOutcome` give it no class and no metric outcome,
  `ErrorClass` of the Destination test answers `unknown`, the events mode of outgoing webhooks treats it as unknown,
  and the outcome of an edit sets `past_edit_window` and applies the rule below to the Desired state it was carrying.
- **Edit windows** (C-22.FR-9; `editwindow.go`, `worker.go`): before an edit of a Bridge Root message, the worker
  compares `published_at` with `edit_max_age_seconds` of the Connection's `info`, less `bridge.edit_window_margin`, on
  the business clock (S-020's clock table). Past it, a Desired state whose status equals `actual_status` makes no call
  and leaves the row delivered with `past_edit_window`; a new status publishes a new Root message Quietly with a new
  Publication key (`pub-…-<publications + 1>` of S-074), then replies under the old one with the pointer text and the
  new message's `url` when `messages.SafeURL` accepts it, or the Alert Group page otherwise, stores the new message id
  and its `thread_ref`, appends the old id to `superseded_message_ids`, resets `past_edit_window` and records the
  delivery event. The pointer reply (`pointer-<Destination public_id>-<delivery id>-<publications>`) is part of the
  republication, not a lifecycle event: it has no `thread_replies` row and is not retried, and its failure is recorded
  with the event. A final edit (C-11.FR-14) of a row past its window is sent as a Thread reply with the same text
  (`final-<Destination public_id>-<delivery id>`).
- **Edit window details** (where the rules above are silent): `actual_status` is the status of the Desired state that
  a delivered Publication, edit or republication showed (`firing`, `acknowledged`, `snoozed`, `resolved`, `storm`);
  null — rows from before 0010, or after a replayed answer — counts as a change of status. `past_edit_window` is set
  lazily, by the first call due past the window, or at once by `edit_window_closed`; the window is read outside any
  transaction, before tokens are taken, and an `info` that cannot be read gives no window, without a log line, so that
  the bridge's own refusal decides. A republication counts toward `publications` and dates `published_at` from its
  outcome, a replayed one too; it records no possible duplicate, and one that fails leaves the flag unset. The pointer
  key carries the count before the republication; the pointer reply is sent after a replayed republication too and is
  counted as a `thread_reply` attempt. The republication releases the old buttons' key. A Storm summary past its window
  is never republished. `edit_window_closed` on a Publication, a republication or a final edit as a Thread reply is
  Not delivered.
- **Loud/Quiet table** (`reference.md`, `enqueue.go`): the republication is the row `republication_past_edit_window`,
  Quiet, without Mentions; the row of the final edit gains S-074's Bridge exception, a Publication whose answer was
  lost before the Destination left, posted again with its key and the final text (C-22.FR-6).
- **Lock order** (D307; `lockorder_integration_test.go`): a republication locks the delivery row only, as a
  Publication does, never its Alert Group before it; the test gains a republication against an acknowledgement that
  holds the Alert Group and then waits for the delivery in `Enqueue`.
- **Read model** (`internal/api/alertgroups.go`): `listAlertGroupDeliveries` and the Alert Group page's deliveries carry
  `past_edit_window`.
- **Texts**: the pointer reply "This message is no longer updated. The current one: {link}" in English and Russian.
- **Fake bridge** (unchanged; `fakebridge_test.go`): `/_fake/config` `limits.edit_max_age_seconds` is declared in
  `info`; Muster's window runs on the development clock, so the fake never needs to enforce it, and a harness fault
  with the problem `{"class":"gone","reason":"edit_window_closed"}` refuses an edit earlier than declared (C-22.AC-9).
- **Log events**: `bridge_republished_past_edit_window` (info: Destination, Alert Group public id, old and new message
  ids).
- **Page** (`docs/messengers/bridge.md`): the section "Edit windows".
- **Defaults**: `bridge.edit_window_margin`, `bridge.info_max_age`.

## Steps

1. Add the migration, the outcome kind and its exhaustive switches, and the SPA's fixture. Check: `golangci-lint run`,
   `outcomes_test.go`, `make lint`.
2. Write the edit window rules, the pointer reply and the final edit as a reply. Check: `editwindow_test.go`,
   `worker_test.go` with a manual clock, `go test -tags integration -run LockOrder ./internal/delivery/`.
3. Extend the fake, the read model, the page and the end-to-end test. Check: Verification.

## Verification

```sh
make dev > dev.log 2>&1 &
# as in S-074: the session, the Integration "lab", NOTIFY, ADV and AG, the Route "br" to the Destination "alerts" (D) of
# the Connection "br" (C)
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake; FB=127.0.0.1:18094/_fake
MSG() { curl -s $FB/messages | jq -c "$1"; }
# a problem answered by the fake: path, status, the problem's members, and Retry-After seconds
FAULT() { jq -cn --arg p "$1" --argjson s "$2" --argjson b "$3" --argjson ra "${4:-0}" '{path: $p, status: $s,
  retry_after_seconds: $ra, content_type: "application/problem+json", body: ($b | tojson), times: 1}' |
  curl -s -X POST $FB/faults -d @- > /dev/null; }

# C-22.AC-8: the edit window
curl -s -X PUT $FB/config -d '{"limits":{"max_body_length":4000,"edit_max_age_seconds":600}}' > /dev/null
curl -s "${H[@]}" -X POST $API/connections/$C/checks > /dev/null                   # reads info again
curl -s -X PUT $FAM/groups/w1 -d '{"receiver":"lab","route":"{}","labels":{"alertname":"Latency"}}' > /dev/null
curl -s -X PUT $FAM/groups/w1/alerts/k1 -d '{"labels":{"team":"br","pod":"k1"}}' > /dev/null
NOTIFY w1 '{"reason":"first notification"}'; sleep 1; G3=$(AG 'pod%3D%22k1%22'); ADV 660
# a new Alert past the window: a Thread reply, no edit, no Publication
curl -s -X PUT $FAM/groups/w1/alerts/k2 -d '{"labels":{"team":"br","pod":"k2"}}' > /dev/null
NOTIFY w1 '{"reason":"new alerts added"}'; ADV 61; sleep 1
MSG '[.[] | select(.envelope.title | endswith("Latency"))] | {n: length, e: (.[0].edits | length), r: (.[0].replies | length)}'   # {"n":1,"e":0,"r":1}
curl -s -b jar "$API/alert-groups/$G3/deliveries" | jq -c '.items[0] | {state, past_edit_window}'   # {"state":"delivered","past_edit_window":true}
# a change of status past the window: a new Root message, Quiet, and the pointer reply under the old one
curl -s "${H[@]}" -X POST $API/alert-groups/$G3/acknowledge > /dev/null; sleep 1
MSG '[.[] | select(.envelope.title | endswith("Latency"))] | {n: length, s: [.[].envelope.status], notify: .[1].envelope.notify,
  p: (.[0].replies[-1].input.body | test("no longer updated. The current one: http://127.0.0.1:18094/m/"))}'
# {"n":2,"s":["firing","acknowledged"],"notify":false,"p":true}
curl -s -b jar "$API/alert-groups/$G3/timeline" | jq '[.items[] | select(.kind == "delivery" and .delivery_event == "republished_past_edit_window")] | length'   # 1
grep -c 'bridge_republished_past_edit_window' dev.log                              # 1
# the next Thread reply goes under the new Root message
curl -s -X PUT $FAM/groups/w1/alerts/k3 -d '{"labels":{"team":"br","pod":"k3"}}' > /dev/null
NOTIFY w1 '{"reason":"new alerts added"}'; ADV 61; sleep 1
MSG '[.[] | select(.envelope.title | endswith("Latency"))] | [.[].replies | length]'   # [2,1]

# C-22.AC-9: the window closes earlier than declared; a new Alert's edit is refused, then Acknowledge republishes
curl -s -X PUT $FAM/groups/w2 -d '{"receiver":"lab","route":"{}","labels":{"alertname":"Early"}}' > /dev/null
curl -s -X PUT $FAM/groups/w2/alerts/e1 -d '{"labels":{"team":"br","pod":"e1"}}' > /dev/null
NOTIFY w2 '{"reason":"first notification"}'; sleep 1; G4=$(AG 'pod%3D%22e1%22')
FAULT '/v1/messages/*' 404 '{"class":"gone","reason":"edit_window_closed"}'
curl -s -X PUT $FAM/groups/w2/alerts/e2 -d '{"labels":{"team":"br","pod":"e2"}}' > /dev/null
NOTIFY w2 '{"reason":"new alerts added"}'; ADV 61; sleep 1
curl -s -b jar "$API/alert-groups/$G4/deliveries" | jq -c '.items[0] | {state, past_edit_window}'   # {"state":"delivered","past_edit_window":true}
curl -s -b jar "$API/alert-groups/$G4/timeline" | jq '[.items[] | select(.kind == "delivery" and .delivery_event == "republished")] | length'   # 0
curl -s "${H[@]}" -X POST $API/alert-groups/$G4/acknowledge > /dev/null; sleep 1
MSG '[.[] | select(.envelope.title | endswith("Early"))] | [.[].envelope.status]'   # ["firing","acknowledged"]
```

`test/e2e/bridge_test.go` covers the final edit of a Root message past its window — the Destination leaves the Route —
as a Thread reply, and a failed pointer reply, against the fake bridge with the development clock.

## Open questions

None.

## Notes

- Suggested commit: `feat(bridge): republish bridge root messages past the messenger's edit window`.
- S-081 was split from S-074 by the plan review of the Bridge phase (D323), because S-074 touched about 45 files. It
  follows S-074 and comes before S-075, whose presses and Text commands on older Root messages read
  `superseded_message_ids`.
- `past_edit_window` is a flag beside the delivery state, not a state of its own: a delivery past its window is
  delivered, or pending while a republication is due, like any other.
- Changes to this file in the pull request: `files_touched` gains the migration tests of `internal/db`, `enqueue.go`
  (the Loud/Quiet row), the membership and live tests that count those rows, and `reference.md`, and loses
  `internal/fakes/fakebridge/messages.go`, which needed no change; the Verification reads a reply's body at
  `.input.body`, as `/_fake/messages` lists it; the Contracts record the details above where the rules were silent.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-22.FR-8 | partial | `gone` with `edit_window_closed` on an edit |
| C-22.FR-9 | partial | the behaviour; the flag on the page is S-077 |
| C-22.FR-23 | partial | the edit windows section of Messengers → Bridge |
| C-22.FR-24 | partial | `bridge_republished_past_edit_window` |
| C-22.AC-8 | full | |
| C-22.AC-9 | full | |
