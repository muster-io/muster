---
id: S-173
title: The Acknowledge of the Transport gateway, only while nobody owns the Alert Group, and its answers in the call (BE)
capability: C-30
kind: be
layer: L2
depends_on: [S-148]
covers: [C-30.FR-2, C-30.FR-6, C-30.FR-10, C-30.AC-4, C-03.FR-14, C-10.FR-1, C-10.FR-3, C-12.FR-3, C-22.FR-11]
files_touched:
  - api/openapi.yaml
  - internal/gateways/acknowledge.go
  - internal/gateways/acknowledge_test.go
  - internal/bridge/events.go
  - internal/bridge/events_test.go
  - internal/groups/commands.go
  - internal/groups/commands_test.go
  - internal/audit/audit.go
  - internal/audit/audit_test.go
  - internal/messages/texts/en.json
  - internal/messages/texts/ru.json
  - internal/metrics/catalogue.go
  - internal/logging/events.go
  - internal/runtime/runtime.go
  - design/prd/l1/reference.md
  - web/src/components/timeline.tsx
  - web/src/routes/admin.audit-log.tsx
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - test/e2e/gateway_acknowledge_test.go
acceptance:
  - "[C-30.FR-6, C-30.AC-4, C-10.FR-1, C-30.FR-2] `notification_acknowledged` whose data verifies runs Acknowledge with the Transport `gateway` as the person the call was for, for each Alert Group of the call: a firing or snoozed one becomes acknowledged, with `gateway` in the Timeline and the Audit log, and the answer through `answerEvent` names what was acknowledged; one that another User or the same person owns stays as it is, the refusal `owned_by_other` is logged as a refused Command and the answer says \"Already taken by\" that User; a resolved one is answered \"Already resolved\"; a Takeover is never made; a disabled person or a Viewer is refused as for a press (`acknowledge_test.go`, `commands_test.go`, `gateway_acknowledge_test.go`)."
  - "[C-03.FR-14] `internal/audit` accepts the Transport `gateway` (`TransportGateway`): an Acknowledge through a gateway writes its Audit log entry `alert_group.acknowledged` with the transport `gateway`, and an entry with a transport outside the closed list is still refused (`audit_test.go`)."
  - "[C-22.FR-11] The event is handled once per id under the claim of C-22.FR-11: replaying it changes nothing and answers once; it is no longer logged as `bridge_event_unhandled` (`events_test.go`)."
  - "[C-12.FR-3] The answers — \"Acknowledged: #12, #13\", \"Already taken by {name}\", \"Already resolved\" and the refusals — exist in English and Russian and follow the language of the person's profile (`acknowledge_test.go`)."
  - "[C-30.FR-10, C-03.FR-14, C-10.FR-3] An Acknowledge through the gateway counts in `muster_commands_total{transport=\"gateway\"}`; `Transport` gains `gateway` in the API, and the Timeline and the Audit log show it — the Timeline as \"from a call through {gateway}\", the Audit log's transport column as \"Notification gateway\" / «шлюз оповещений», whose exhaustive switches gain the value so that the TypeScript check passes after `make generate`; reference.md adds `gateway` to the `transport` values of the row of `muster_commands_total` and drops the sentence about it under \"Metrics added in L2\" (`metrics_test.go`)."
verify: "make ci test-integration e2e-go"
operator_attention: false
issue: 462
---

# S-173. The Acknowledge of the Transport gateway, only while nobody owns the Alert Group, and its answers in the call (BE)

## Scope

**IN**

- `notification_acknowledged` acting as the person the call was for — taken from Muster's signature and the call it
  names, never from the contact — with the Transport `gateway` and a precondition of its own, answered on the
  interactive path (C-30.FR-6, ADR-0013; C-10.FR-1, C-10.FR-3, C-03.FR-14, C-22.FR-11 revised).
- The Transport `gateway` in the API, the metric and the SPA's exhaustive switches; the Timeline and the Audit log
  accept it since S-148's migration.

**OUT**

- The queue of calls, their text and their signed action data: S-148. The calls of rules and steps: S-157.

## Contracts

- **Spec** (`api/openapi.yaml`): `Transport` gains `gateway`.
- **Schema**: none new; `timeline_entries_transport_v4_check` and `audit_log_transport_v4_check` take `gateway` since
  S-148.
- **Packages**: as S-148 and S-169 state them; `bridge` hands the event to `bridge.NotificationAcks`, an interface
  declared there that `gateways` implements and `internal/runtime` wires, so that `bridge` does not import `gateways`.
- **Gateways** (`internal/gateways/acknowledge.go`): the data is verified with the key it names and must be a call's
  (`buttons.SubjectCall`, S-148) of this Connection; the actor is the call's User, never the contact (ADR-0013); for
  each Alert Group the call carried from its first attempt — as C-30.FR-6 says, also one whose chain stopped meanwhile —
  the dispatcher runs Acknowledge with the Transport `gateway`; the answer goes through `answerEvent` on the interactive
  path, in the person's language.
- **Groups** (`internal/groups/commands.go`): Acknowledge with the Transport `gateway` has the precondition of
  C-30.FR-6: firing or snoozed → acknowledged; acknowledged by anyone → refused with the existing code `owned_by_other`
  and the Owner; resolved → refused `already_resolved`; never a Takeover; the Role and state of the person as for a
  press (C-18.FR-8).
- **Audit log** (`internal/audit/audit.go`): `TransportGateway` (`gateway`) joins the closed list of transports that
  the validation of an entry accepts, which refuses any other as "unknown transport".
- **Bridge** (`internal/bridge/events.go`): `notification_acknowledged` is handed to `NotificationAcks` once per event
  id under the claim of C-22.FR-11, in place of the `bridge_event_unhandled` of S-145.
- **Texts** (`internal/messages/texts`): the answers, in English and Russian with their plurals (C-12.FR-3 revised).
- **SPA** (`web/src/components/timeline.tsx`, `web/src/routes/admin.audit-log.tsx`, locales): the exhaustive switches
  over `Transport` gain `gateway` — "Notification gateway" / «шлюз оповещений».
- **Metrics** (`internal/metrics/catalogue.go`): `gateway` among the `transport` values of `muster_commands_total`;
  reference.md adds it to the row of that metric and drops the sentence about it under "Metrics added in L2".
- **Log events** (`internal/logging/events.go`): `gateway_acknowledge_refused` (info; the call, the reason), C-30.
- **Settings**: `delivery.interactive_budget`.

## Steps

1. Add the Transport to the spec and the SPA's switches. Check: `make generate-check`; the web tests.
2. Write the precondition and the handler with its answers. Check: `commands_test.go`, `acknowledge_test.go`,
   `events_test.go`.
3. Add the metric value and the log event; write `test/e2e/gateway_acknowledge_test.go`. Check: the Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-148: A, PETR with a confirmed contact at GW, CALL, the Alert Groups G1 and G3 of the fake Alertmanager, FB;
# ivan, another Responder, with his token I.
API=localhost:8080/api/v1; FB=127.0.0.1:18094/_fake
CALL $G3; sleep 1; N=$(curl -s $FB/notifications | jq -r 'last.notification_id')
curl -s -X POST $FB/acknowledge -d "{\"notification_id\":\"$N\"}"; sleep 2
curl -s "${A[@]}" $API/alert-groups/$G3 | jq -c '{status, o: .owner.login}'                   # {"status":"acknowledged","o":"petr"}
curl -s "${A[@]}" "$API/alert-groups/$G3/timeline?limit=1" | jq -r '.items[0].transport'      # gateway
# an Alert Group already owned: its call acknowledged after ivan took it
curl -s -X POST localhost:8082/_dev/clock -d '{"advance_seconds": 61}' > /dev/null; CALL $G1; sleep 1
curl -s "${I[@]}" -X POST $API/alert-groups/$G1/acknowledge > /dev/null
curl -s -X POST $FB/acknowledge -d "{\"notification_id\":\"$(curl -s $FB/notifications | jq -r 'last.notification_id')\"}"; sleep 2
curl -s $FB/answers | jq -r 'last.text'                                                       # Already taken by Ivan
curl -s "${A[@]}" $API/alert-groups/$G1 | jq -r .owner.login                                  # ivan
curl -s localhost:8082/metrics | grep -E '^muster_commands_total\{.*transport="gateway"'      # … 1
```

`test/e2e/gateway_acknowledge_test.go` repeats these checks and adds a resolved Alert Group answered "Already
resolved", a replayed event answered once, a Viewer's and a disabled person's acknowledgement refused, and a call about
two Alert Groups acknowledging both.

## Open questions

None.

## Notes

- Suggested commit: `feat(gateways): acknowledge alert groups from a call through a notification gateway`.
- Split from S-148 when the review added the Timeline read path and the SPA's switches there, which would have made it
  about 47 files; it follows S-148 in the story map.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-30.FR-2 | partial | the event acts on Alert Groups; the protocol is S-145, the action data S-148 |
| C-30.FR-6 | full | |
| C-30.FR-10 | partial | `gateway` in `muster_commands_total`; `muster_gateway_calls_total` is S-148, masking S-167 |
| C-30.AC-4 | full | |
| C-03.FR-14 | full | revised for L2: the Transport `gateway` in the Audit log |
| C-10.FR-1 | partial | revised for L2: the Acknowledge through a gateway; chains stop from S-169, Assignees clear from S-168 |
| C-10.FR-3 | full | revised for L2 |
| C-12.FR-3 | partial | revised for L2: the answers to a person in a call |
| C-22.FR-11 | partial | revised for L2: `notification_acknowledged` handled once per id; S-145 reads it |
