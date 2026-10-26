---
id: S-145
title: The role gateway in the Bridge protocol, sendNotification and notification_acknowledged, the check of the role and the fake gateway (BE)
capability: C-30
kind: be
layer: L2
depends_on: [S-166]
covers: [C-30.FR-1, C-30.FR-2, C-30.FR-5, C-30.FR-10, C-30.AC-1, C-22.FR-2, C-22.FR-3, C-22.FR-11, C-22.FR-21]
files_touched:
  - api/bridge.yaml
  - api/openapi.yaml
  - internal/bridge/info.go
  - internal/bridge/info_test.go
  - internal/bridge/client.go
  - internal/bridge/client_test.go
  - internal/bridge/delivery.go
  - internal/bridge/delivery_test.go
  - internal/bridge/events.go
  - internal/bridge/events_test.go
  - internal/bridge/conformance.go
  - internal/bridge/conformance_test.go
  - internal/delivery/delivery.go
  - internal/delivery/interactive.go
  - internal/archlint/archlint.go
  - internal/archlint/archlint_test.go
  - internal/archlint/testdata/rule3/internal/gateways/calls.go
  - internal/cli/bridge.go
  - internal/cli/bridge_test.go
  - internal/connections/bridge.go
  - internal/connections/bridge_test.go
  - internal/destinations/write.go
  - internal/destinations/write_test.go
  - internal/api/connections.go
  - internal/fakes/fakebridge/fakebridge.go
  - internal/fakes/fakebridge/notifications.go
  - internal/fakes/fakebridge/events.go
  - internal/fakes/fakebridge/fakebridge_test.go
  - internal/devmode/devmode.go
  - internal/devmode/devmode_test.go
  - internal/logging/events.go
  - web/src/test/fixtures/connections.ts
  - test/e2e/bridge_gateway_test.go
  - AGENTS.md
  - docs/messengers/bridge.md
acceptance:
  - "[C-30.FR-2, C-22.FR-2, C-22.FR-11] `api/bridge.yaml` gains, additively in protocol version 1: `Info.roles` (an array of `messenger` and `gateway`, unique, at least one; `messenger` when omitted); the operation `sendNotification` (`POST /v1/notifications`, `Idempotency-Key` required) with `NotificationInput` {`contact`, `text` (plain text, at most 1,000 code points), `language` (`en`, `ru`), `action` (`NotificationAction` {`data`, at most 64 ASCII bytes, and `label`}, or absent for a confirmation code)} answering `201` `NotificationSent` {`notification_id`, `replayed`} and the classes of every other call; the event `NotificationAcknowledgedEvent` {`type` `notification_acknowledged`, `notification_id`, `data`} in the `Event` union; `AnswerShown.shown_as` gains `notification`; `make lint` passes Redocly and oasdiff reports no breaking change, and `api/bridge_test.go` finds `BridgeInfo` of `openapi.yaml` equal to `Info`."
  - "[C-30.FR-1, C-30.AC-1, C-22.FR-3] A Bridge Connection whose stored `info` declares only `gateway` reads `roles` `[\"gateway\"]`; `createDestination` and `updateDestination` naming it are refused with `422` `gateway_only` at `/connection_id`; a Connection whose `info` declares both roles reads `[\"messenger\", \"gateway\"]` and takes a Destination; one without `roles` in `info` reads `[\"messenger\"]`; `listConnections` takes `role` (`messenger`, `gateway`): `messenger` lists every Mattermost, Telegram and Slack Connection and the Bridge Connections whose stored `info` has it, `gateway` only the Bridge Connections whose stored `info` has it (`bridge_test.go`, `write_test.go`)."
  - "[C-30.FR-2, C-22.FR-21] `muster bridge check` reads the roles: a bridge without `gateway` skips the gateway steps; with `gateway`, the step `notification` sends one `sendNotification` to the contact given by `--contact` (skipped without it) with a test text and test action data, and passes on `201`; a repeated call with the same key must answer `replayed` true; in interactive mode the step `notification_acknowledged` waits for that event carrying the same `data` and answers it with `answerEvent`; against the fake bridge's gateway bot every step passes (`conformance_test.go`, `bridge_test.go` of `internal/cli`)."
  - "[C-30.FR-5] `delivery.Gateway` is an interface with the one method `Notify`, which the Bridge adapter implements by `sendNotification` through the Connection's limiter; lint 3 fails a call of `Notify` outside `internal/delivery/interactive.go` (and, from S-148, the gateway sender), with its fixture `testdata/rule3/internal/gateways/calls.go` (`archlint_test.go`)."
  - "[C-30.FR-2, C-22.FR-11] `DecodeEvent` reads `notification_acknowledged` and keeps it for its handler, which until S-173 logs `bridge_event_unhandled` — a new log event of the registry — and acknowledges the event; Bridge polling takes the events of a Connection whose only role is `gateway` like any other's (`events_test.go`)."
  - "[C-30.FR-2] The fake bridge serves the bot `muster-dev-gateway-token` whose `info` declares only `gateway`; `/_fake/config` sets the roles of any bot; `sendNotification` records each request in `/_fake/notifications` with its key and answers a repeated key as replayed; `/_fake/faults` scripts its answers like those of messages; `POST /_fake/acknowledge` with a recorded `notification_id` queues `notification_acknowledged` with that request's `data` and records the answer of `answerEvent` in `/_fake/answers` (`fakebridge_test.go`)."
  - "[C-30.FR-1] `muster dev` creates the demo Connection \"Example Gateway\" of the fake bridge's gateway bot in poll mode, checked at start so that it reads `roles` `[\"gateway\"]` (`devmode_test.go`); `web/src/test/fixtures/connections.ts` gives Bridge Connection reads `roles` `[\"messenger\"]`, so the TypeScript check of the web tests passes."
  - "[C-30.FR-10] A request to a gateway is recorded in the Request log like any request to a bridge, and `sendNotification` logs `gateway_notification_sent` with the Connection, the key and the outcome class, never the contact or the text; the log event is in the registry and its reference page."
verify: "make ci e2e-go docs-check"
operator_attention: false
issue: null
---

# S-145. The role gateway in the Bridge protocol, sendNotification and notification_acknowledged, the check of the role and the fake gateway (BE)

## Scope

**IN**

- The roles `messenger` and `gateway` in `info`, `sendNotification` and the event `notification_acknowledged` in the
  Bridge protocol, additively in version 1 (C-30.FR-2, ADR-0017 as amended; C-22.FR-2 and C-22.FR-11 revised).
- The roles of a Bridge Connection in its read and the list filter, and the refusal `gateway_only` of a Destination of a
  Connection whose only role is `gateway` (C-30.FR-1; C-22.FR-3 revised).
- The protocol client's `SendNotification`, the Bridge adapter's `Notify` behind the interface `delivery.Gateway`, and
  lint 3 for it (C-30.FR-5, ADR-0016 as amended).
- `muster bridge check` for the role (C-22.FR-21 revised).
- The fake bridge as a gateway — a bot of its own, recorded notifications, scripted faults, acknowledgements — and the
  demo Connection "Example Gateway" of `muster dev`.

**OUT**

- Contacts, their code and its limits, their masking in the Request log and the gateway note of the unreachable
  warnings: S-167.
- The queue of calls, their text, combining, retries, `notification_acknowledged` acting on Alert Groups and the
  Transport `gateway`: S-148 and S-173. Broken gateways: S-149.
- The roles and health on the Connection page and the gateway-only Connections left out of the Destination form: S-150.
- The libraries: S-146 (Go), S-147 (Python). The documentation for gateway authors: S-151.

## Contracts

- **Protocol** (`api/bridge.yaml`, MIT; `protocol_version` stays 1):
  - `Info` gains the optional `roles` (`Role`[]: `messenger`, `gateway`; unique, at least one; `messenger` when
    omitted). A bridge whose only role is `gateway` still sends the required members of `Info`: `messenger.name` names
    its provider, `capabilities` its features (none of a messenger's), `user_lookup` `none`, `event_delivery` its modes,
    `limits` its limits; the descriptions say so.
  - `sendNotification` (`POST /v1/notifications`, bearer token, `Idempotency-Key` required): `NotificationInput`
    {`contact` (string, 1–200), `text` (plain text, 1–1,000 code points; the bridge reads it out or sends it as it can),
    `language` (`en`, `ru`, the person's), `action` (`NotificationAction` {`data` — signed action data like a button's,
    at most 64 ASCII bytes; `label` — how the person acknowledges, such as "Press 1 to acknowledge"}, absent for a
    confirmation code, which is nothing to acknowledge)}; answers `201` `NotificationSent` {`notification_id`,
    `replayed`}, and the problems with the classes of ADR-0017 (`transient`, `rate_limited`, `forbidden`, `invalid`).
  - `NotificationAcknowledgedEvent` {`type` `notification_acknowledged`, `notification_id`, `data` — the `action.data`
    unchanged} joins `Event` and its discriminator; `answerEvent` answers it; `AnswerShown.shown_as` gains
    `notification` ("told in the call or in the reply to the message"). The description of the role says that a gateway
    implements `info`, `sendNotification`, `answerEvent` — during a call in the push mode, so that the answer comes
    while the person is on the line — and events in at least one mode.
- **Spec** (`api/openapi.yaml`): `BridgeInfo` gains `roles` (copy of `Info`); `BridgeConnection` gains the required,
  read-only `roles` (`BridgeRole`[], from the stored `info`, `["messenger"]` while `info` is null); `listConnections`
  gains `role`; problem code `gateway_only` (`validation-failed`, in `errors[].code`) at `/connection_id`.
- **Bridge** (`internal/bridge`): `info.go` — `Info.Roles` and `HasRole`; `client.go` — `SendNotification(ctx, conn,
  key, NotificationInput)` with the classification of `classify.go`; `delivery.go` — the Bridge adapter's `Notify`,
  through the Connection's limiter (`connection.bridge.limiter`) in the client class of its caller; `events.go` —
  `TypeNotificationAcknowledged`, decoded and validated (the `data` at most 64 bytes), handed to a handler that S-173
  registers; `conformance.go` — the steps `notification` and `notification_acknowledged`.
- **Delivery** (`internal/delivery`): `delivery.go` declares `Gateway` {`Notify(ctx, Call, Notification) Outcome`},
  which `*bridge.Adapter` implements; `interactive.go` gains `Notify` on the interactive path, for the confirmation
  codes of S-167.
- **Lint 3** (`internal/archlint/archlint.go`): the configuration gains the interface `delivery.Gateway` with the method
  `Notify`, allowed in `internal/delivery/interactive.go`; S-148 adds the sender. The messenger clients beneath stay
  unchecked, so `muster bridge check` needs no exception (ADR-0016 as amended).
- **CLI** (`internal/cli/bridge.go`): `muster bridge check` gains `--contact` (the contact the step `notification`
  calls); its output names the roles.
- **Connections** (`internal/connections/bridge.go`): `roles` in the read and the filter `role`.
- **Destinations** (`internal/destinations/write.go`): the refusal `gateway_only` on create and update, from the stored
  `info`; a Destination saved before its bridge became gateway-only stays and keeps its delivery (Notes).
- **Fake bridge** (`internal/fakes/fakebridge`): `GatewayToken` `muster-dev-gateway-token`, a bot whose `info` declares
  `roles: [gateway]`, poll and push, `messenger.name` "Example Gateway"; `notifications.go` — `sendNotification` with
  the idempotency of the other calls, `/_fake/notifications` (each request with its key, contact, text, language and
  `action`), `POST /_fake/acknowledge` {`notification_id`} that queues `notification_acknowledged` for the bot;
  `/_fake/config` gains `roles` per token; `/_fake/faults` applies to `sendNotification` like to the other calls.
- **Development mode** (`internal/devmode/devmode.go`): the demo Bridge Connection "Example Gateway" with the gateway
  bot in poll mode, checked at start; AGENTS.md's paragraph on development mode names the third bot and its Connection,
  and its row of lint 3 names `delivery.Gateway.Notify` as ADR-0016 does.
- **Web test fixtures** (`web/src/test/fixtures/connections.ts`, S-166): `roles` `["messenger"]` in the Bridge
  Connection builders.
- **Log events** (`internal/logging/events.go`): `gateway_notification_sent` (info; the Connection, the key, the class;
  never the contact or the text), C-30; `bridge_event_unhandled` (info; the Connection, the event id and type: an
  event of a type this binary reads but no handler takes yet, recorded as handled), C-22.
- **Settings**: `connection.bridge.limiter`, `bridge.info_max_age`.
- **Documentation**: Connect a messenger through a bridge (`docs/messengers/bridge.md`) — "Roles", saying that a bridge
  in the role `gateway` is a Notification gateway and cannot be a Destination; the page on Notification gateways is
  S-167's.

## Steps

1. Add the roles, `sendNotification` and the event to `api/bridge.yaml`, and their copies to `openapi.yaml`. Check:
   `make generate-check`; `make lint`.
2. Read the roles, write the client call, the adapter's `Notify`, the interface and lint 3. Check: `info_test.go`,
   `client_test.go`, `delivery_test.go`, `archlint_test.go`.
3. Decode the event; add the roles to the Connection read and the refusal to Destinations. Check: `events_test.go`,
   `bridge_test.go`, `write_test.go`; `make generate-check`.
4. Add the steps of `muster bridge check`. Check: `conformance_test.go`, `internal/cli/bridge_test.go`.
5. Extend the fake bridge, the demo data, the fixture and AGENTS.md; write `test/e2e/bridge_gateway_test.go` and the
   documentation. Check: the Verification below; `make docs-check`.

## Verification

```sh
make dev > dev.log 2>&1 &
# A: the Admin's Personal access token, as in S-073.
API=localhost:8080/api/v1; FB=127.0.0.1:18094/_fake
GW=$(curl -s "${A[@]}" "$API/connections?role=gateway" | jq -r '.items[] | select(.name == "Example Gateway") | .id')
curl -s "${A[@]}" $API/connections/$GW | jq -c '{type, roles, m: .info.messenger.name}'
# {"type":"bridge","roles":["gateway"],"m":"Example Gateway"}
curl -s "${A[@]}" $API/destinations -d "{\"type\":\"bridge\",\"name\":\"gw\",\"connection_id\":\"$GW\",\"channel_id\":\"ch-alerts\",
  \"limiter\":{\"limit\":30,\"per_seconds\":60}}" | jq -c '{status, e: .errors[0] | {pointer, code}}'
# {"status":422,"e":{"pointer":"/connection_id","code":"gateway_only"}}

# both roles: the demo "Example Bridge" bot takes a Destination after its roles change
curl -s -X PUT $FB/config -d '{"token":"muster-dev-bridge-token","roles":["messenger","gateway"]}' > /dev/null
BR=$(curl -s "${A[@]}" "$API/connections?role=messenger" | jq -r '.items[] | select(.name == "Example Bridge") | .id')
curl -s "${A[@]}" -X POST $API/connections/$BR/checks > /dev/null
curl -s "${A[@]}" $API/connections/$BR | jq -c .roles                                       # ["messenger","gateway"]

# the check of the role, interactive, with the fake pushing to the check's listener
printf %s muster-dev-gateway-token > tok
./bin/muster bridge check http://127.0.0.1:18094 --token-file tok --contact +10000000001 &
sleep 1; curl -s -X POST $FB/acknowledge -d "{\"notification_id\":\"$(curl -s $FB/notifications | jq -r 'last.notification_id')\"}"
wait; echo "exit $?"
# roles: gateway; notification passed; notification_acknowledged passed; exit 0
curl -s $FB/notifications | jq -c 'last | {contact, has_action: (.action != null)}'           # {"contact":"+10000000001","has_action":true}
curl -s "${A[@]}" "$API/request-log?connection=$GW&limit=1" | jq -r '.items[0].path'       # /v1/info
```

`test/e2e/bridge_gateway_test.go` repeats these checks and adds a Connection without `roles` in its `info`, the
`role` filter, a `notification_acknowledged` event polled from the gateway bot and logged as unhandled, a replayed key,
and a fault scripted on `sendNotification` that the check reports.

## Open questions

1. **A Destination whose bridge later declares only `gateway`.** Settled by C-30.FR-1, which refuses only creating a
   Destination for such a Connection: a Destination saved while the bridge was a messenger stays, its deliveries fail as
   the bridge answers, and S-150 warns about it on the Connection page.

## Notes

- Suggested commit: `feat(bridge): add the gateway role with sendNotification and notification_acknowledged`.
- The libraries' copies of `api/bridge.yaml` fail their `make spec-check` once this merges, until S-146 and S-147 take
  the new copy.
- The action data of a call names the call (C-30.FR-2); its subject and signing are S-148's, since the call does not
  exist before it. The check sends test data of the same size, so that a bridge proves it carries 64 bytes.
- No migration: the roles are read from the stored `info` (`connections.bridge_info`).

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-30.FR-1 | partial | the roles and the refusal; the Connection page shows them from S-150 |
| C-30.FR-2 | partial | the protocol, the check and the fake; handling the event is S-173, the libraries S-146 and S-147 |
| C-30.FR-5 | partial | the interface and lint 3; the text of a call and the sender are S-148 |
| C-30.FR-10 | partial | requests to a gateway in the Request log and the log event; masking is S-167, the metrics S-148 and S-173 |
| C-30.AC-1 | partial | the API; "shown as a Notification gateway" on the page is S-150 |
| C-22.FR-2 | partial | revised for L2: the roles of `info`; group mentions are S-139 |
| C-22.FR-3 | partial | revised for L2: no Destination for a gateway-only Connection; the Mention entries are S-140 |
| C-22.FR-11 | partial | revised for L2: the event `notification_acknowledged` is read; S-173 acts on it |
| C-22.FR-21 | partial | revised for L2: the check of the role `gateway`; `layout` is S-126 |
