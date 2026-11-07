---
id: S-145
title: The role gateway in the Bridge protocol, sendNotification and notification_acknowledged, the check of the role and the fake gateway (BE)
capability: C-30
kind: be
layer: L2
depends_on: [S-166]
covers: [C-30.FR-1, C-30.FR-2, C-30.FR-5, C-30.FR-10, C-30.AC-1, C-22.FR-2, C-22.FR-3, C-22.FR-11, C-22.FR-21, C-18.FR-10]
files_touched:
  - api/bridge.yaml
  - api/bridge-oasdiff-ignore.txt
  - .github/workflows/ci.yml
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
  - internal/delivery/limiter_test.go
  - internal/archlint/archlint.go
  - internal/archlint/archlint_test.go
  - internal/archlint/callrules.go
  - internal/archlint/secretleak.go
  - internal/archlint/testdata/rule3/internal/delivery/adapter.go
  - internal/archlint/testdata/rule3/internal/delivery/interactive.go
  - internal/archlint/testdata/rule3/internal/gateways/calls.go
  - internal/cli/bridge.go
  - internal/cli/bridge_test.go
  - internal/connections/bridge.go
  - internal/connections/bridge_test.go
  - internal/connections/connections.go
  - internal/connections/connections_test.go
  - internal/connections/query.sql
  - internal/destinations/write.go
  - internal/destinations/write_test.go
  - internal/api/connections.go
  - internal/accountlinks/accountlinks.go
  - internal/accountlinks/query.sql
  - internal/accountlinks/live_test.go
  - internal/fakes/fakebridge/fakebridge.go
  - internal/fakes/fakebridge/notifications.go
  - internal/fakes/fakebridge/events.go
  - internal/fakes/fakebridge/fakebridge_test.go
  - internal/devmode/devmode.go
  - internal/devmode/devmode_test.go
  - internal/runtime/runtime.go
  - internal/logging/events.go
  - web/src/test/fixtures/connections.ts
  - test/e2e/bridge_gateway_test.go
  - test/e2e/several_connections_test.go
  - test/e2e/bridge_check_test.go
  - test/e2e/bridge_example_test.go
  - AGENTS.md
  - docs/messengers/bridge.md
  - docs/messengers/write-a-bridge.md
acceptance:
  - "[C-30.FR-2, C-22.FR-2, C-22.FR-11] `api/bridge.yaml` gains, additively in protocol version 1: `Info.roles` (an array of `messenger` and `gateway`, unique, at least one; `messenger` when omitted); the operation `sendNotification` (`POST /v1/notifications`, `Idempotency-Key` required) with `NotificationInput` {`contact`, `text` (plain text, at most 1,000 code points), `language` (`en`, `ru`), `action` (`NotificationAction` {`data`, at most 64 ASCII bytes, and `label`}, or absent for a confirmation code)} answering `201` `NotificationSent` {`notification_id`, `replayed`} and the classes of every other call; the event `NotificationAcknowledgedEvent` {`type` `notification_acknowledged`, `notification_id`, `data`} in the `Event` union; `AnswerShown.shown_as` gains `notification`; `make lint` passes Redocly and the CI step \"oasdiff breaking, api/bridge.yaml\" passes with the three role-gated breaking changes that this pull request adds to `api/bridge-oasdiff-ignore.txt`, each with its direction (`forward` or `reverse`), and fails on any other, the push webhook's included, on a line of the wrong direction and, once merged, on a later change that the same lines would name, such as the removal of what this story adds (Step 1); and `api/bridge_test.go` finds `BridgeInfo` of `openapi.yaml` equal to `Info`."
  - "[C-30.FR-1, C-30.AC-1, C-22.FR-3] A Bridge Connection whose stored `info` declares only `gateway` reads `roles` `[\"gateway\"]`; `createDestination` naming it, and `updateDestination` that moves a Destination to it, are refused with `422` `gateway_only` at `/connection_id`, while an update of a Destination saved before its bridge became gateway-only, on that Connection, is not refused with `gateway_only` but fails its Destination check as the bridge answers it, so that it can only be deleted or moved to another Connection; a Connection whose `info` declares both roles reads `[\"messenger\", \"gateway\"]` and takes a Destination; one without `roles` in `info` reads `[\"messenger\"]`; `listConnections` takes `role` (`messenger`, `gateway`): `messenger` lists every Mattermost, Telegram and Slack Connection and the Bridge Connections whose stored `info` has it, `gateway` only the Bridge Connections whose stored `info` has it (`bridge_test.go`, `write_test.go`)."
  - "[C-30.FR-2, C-22.FR-21] `muster bridge check` reads the roles: a bridge without `gateway` skips the gateway steps; with `gateway`, the step `notification` sends one `sendNotification` to the contact given by `--contact` (skipped without it) with a test text and test action data, and passes on `201`; a repeated call with the same key must answer `replayed` true; in interactive mode the step `notification_acknowledged` waits for that event carrying the same `data` and answers it with `answerEvent`; against the fake bridge's gateway bot every step passes (`conformance_test.go`, `bridge_test.go` of `internal/cli`)."
  - "[C-30.FR-5] `delivery.Gateway` is an interface with the one method `Notify`, which the Bridge adapter implements by `sendNotification` through the Connection's limiter; lint 3 fails a call of `Notify` outside `internal/delivery/interactive.go` (and, from S-148, the gateway sender), with its fixture `testdata/rule3/internal/gateways/calls.go` (`archlint_test.go`)."
  - "[C-30.FR-2, C-22.FR-11] `DecodeEvent` reads `notification_acknowledged` and keeps it for its handler, which until S-173 logs `bridge_event_unhandled` — a new log event of the registry — and acknowledges the event; Bridge polling takes the events of a Connection whose only role is `gateway` like any other's (`events_test.go`)."
  - "[C-30.FR-2] The fake bridge serves the bot `muster-dev-gateway-token` whose `info` declares only `gateway`; `/_fake/config` sets the roles of any bot; `sendNotification` records each request in `/_fake/notifications` with its key and answers a repeated key as replayed; `/_fake/faults` scripts its answers like those of messages; `POST /_fake/acknowledge` with a recorded `notification_id` queues `notification_acknowledged` with that request's `data` and records the answer of `answerEvent` in `/_fake/answers` (`fakebridge_test.go`)."
  - "[C-30.FR-1, C-18.FR-10] `listLinkableConnections` (`/me/account-links/connections`) leaves out a Bridge Connection whose stored `info` lists `roles` without `messenger`, so the profile never offers \"Link through Example Gateway\", and keeps one with both roles (`live_test.go` of `internal/accountlinks`, `several_connections_test.go`)."
  - "[C-30.FR-1] `muster dev` creates the demo Connection \"Example Gateway\" of the fake bridge's gateway bot in poll mode, checked at start so that it reads `roles` `[\"gateway\"]` (`devmode_test.go`); `web/src/test/fixtures/connections.ts` gives Bridge Connection reads `roles` `[\"messenger\"]`, so the TypeScript check of the web tests passes."
  - "[C-30.FR-10] A request to a gateway is recorded in the Request log like any request to a bridge, and `sendNotification` logs `gateway_notification_sent` with the Connection, the key and the outcome class, never the contact or the text; the log event is in the registry and its reference page."
verify: "make ci e2e-go docs-check"
operator_attention: false
issue: 434
---

# S-145. The role gateway in the Bridge protocol, sendNotification and notification_acknowledged, the check of the role and the fake gateway (BE)

## Scope

**IN**

- The roles `messenger` and `gateway` in `info`, `sendNotification` and the event `notification_acknowledged` in the
  Bridge protocol, additively in version 1 (C-30.FR-2, ADR-0017 as amended; C-22.FR-2 and C-22.FR-11 revised).
- The roles of a Bridge Connection in its read and the list filter, and the refusal `gateway_only` of a Destination of a
  Connection whose only role is `gateway` (C-30.FR-1; C-22.FR-3 revised); such a Connection left out of the Connections
  an account is linked through (C-30.FR-1).
- The protocol client's `SendNotification`, the Bridge adapter's `Notify` behind the interface `delivery.Gateway`, and
  lint 3 for it (C-30.FR-5, ADR-0016 as amended).
- `muster bridge check` for the role (C-22.FR-21 revised).
- The fake bridge as a gateway — a bot of its own, recorded notifications, scripted faults, acknowledgements — and the
  demo Connection "Example Gateway" of `muster dev`.

**OUT**

- Contacts, their code and its limits and their masking in the Request log: S-167; the gateway note of the unreachable
  warnings: S-150.
- The queue of calls, their text, combining, retries, `notification_acknowledged` acting on Alert Groups and the
  Transport `gateway`: S-148 and S-173. Broken gateways: S-149.
- The roles and health on the Connection page and the gateway-only Connections left out of the Destination form: S-150.
- The libraries: S-146 (Go), S-147 (Python). The documentation for gateway authors: S-151.

## Contracts

- **Protocol** (`api/bridge.yaml`, MIT; `protocol_version` stays 1):
  - `Info` gains the optional `roles` (`InfoRole`[]: `messenger`, `gateway`; unique, at least one; `messenger` when
    omitted). The schema is `InfoRole`, not `Role`: `openapi.yaml` has a schema `Role` already, and the copies of the
    Bridge protocol's schemas may not take a name of `openapi.yaml` (`api/bridge_test.go`). A bridge whose only role is
    `gateway` still sends the required members of `Info`: `messenger.name` names its provider, `capabilities` its
    features (none of a messenger's), `user_lookup` `none`, `event_delivery` its modes, `limits` its limits; the
    descriptions say so.
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
- **Spec** (`api/openapi.yaml`): `BridgeInfo` gains `roles` (copy of `Info`), `BridgeInfoRole` is the copy of `InfoRole`
  (whose Go constants `x-enum-varnames` names `BridgeInfoRoleMessenger` and `BridgeInfoRoleGateway`, apart from the type
  `Messenger` of `openapi.yaml`; `api/bridge_test.go` leaves that key out of the comparison, and `api/bridge.yaml` has
  none, so its generators name them by their defaults) and `BridgeNotificationAcknowledgedEvent` that of the event, in
  `BridgeEvent`; `BridgeConnection` gains the required, read-only `roles` (`BridgeInfoRole`[], from the stored `info`,
  `["messenger"]` while `info` is null); `listConnections` gains `role`; problem code `gateway_only`
  (`validation-failed`, in `errors[].code`) at `/connection_id`, which the mapping of `destinations.FieldError` in
  `internal/api/problem.go` answers as it is, so that file does not change.
- **Bridge** (`internal/bridge`): `info.go` — `Info.Roles` and `HasRole`; `client.go` — `SendNotification(ctx, conn,
  key, NotificationInput)` with the classification of `classify.go`; `delivery.go` — the Bridge adapter's `Notify`,
  through the Connection's limiter (`connection.bridge.limiter`) in the client class of its caller, with the contact
  and the text masked like Secrets in the Request log's record of the call (`outbound.WithSecrets`) and in its error;
  `events.go` —
  `TypeNotificationAcknowledged`, decoded and validated (the `data` at most 64 bytes), handed to a handler that S-173
  registers; `conformance.go` — the steps `notification` and `notification_acknowledged`.
- **Delivery** (`internal/delivery`): `delivery.go` declares `Gateway` {`Notify(ctx, Call, Notification) Outcome`},
  which `*bridge.Adapter` implements; `interactive.go` gains `Notify` on the interactive path, for the confirmation
  codes of S-167.
- **Bridge protocol check in CI** (`.github/workflows/ci.yml`, `api/bridge-oasdiff-ignore.txt`): the step "oasdiff
  breaking, api/bridge.yaml" writes the markdown report of both comparisons without `--err-ignore`, and takes its
  status from a second run of each with `--format json`: `jq` checks the shape of the answer — a list whose items have
  a numeric `level` and string `id`, `operation`, `path` and `text` — and fails on every ERR (level 3) whose
  `<direction>|<method> <path> <text>` is not one of the lines that the pull request adds to the file compared with
  `origin/<base>` (every line when the base has none); the libraries' own spec checks (S-146, S-147) are not
  affected.
- **Lint 3** (`internal/archlint/archlint.go`, `callrules.go`): the configuration gains the interface
  `delivery.Gateway` with the method `Notify`, allowed in `internal/delivery/interactive.go`; S-148 adds the sender. The
  rule checks each of the two interfaces with its own allowed files, and its fixture's `internal/delivery` declares
  `Gateway` beside `Adapter`, with a call allowed in its `interactive.go`. The messenger clients beneath stay
  unchecked, so `muster bridge check` needs no exception (ADR-0016 as amended).
- **CLI** (`internal/cli/bridge.go`): `muster bridge check` gains `--contact` (the contact the step `notification`
  calls); its output names the roles; `--channel` is needed only by a bridge in the role `messenger`, and its absence
  is a usage error, exit 2, once `info` named that role and before any step of the messenger.
- **Connections** (`internal/connections/bridge.go`, `connections.go`, `query.sql`): `roles` in the read; the filter
  `role` in the query `ListConnections`, so that a page of the cursor pagination is full; `BridgeConnection`, the Bridge
  Connection of a gateway by its id for `bridge.Targets`; `GatewayOnly` for the Destination write path; and
  `CheckDemo`, the check of the demo gateway at start, which ends the start only when the Connection cannot be read
  and logs `demo_check_failed` for a check that fails or cannot run.
- **Destinations** (`internal/destinations/write.go`): the refusal `gateway_only` on create and on an update that moves
  the Destination to another Connection, from the stored `info`; a Destination saved before its bridge became
  gateway-only stays and keeps its delivery as the bridge answers it (Open question 1, Notes), and an update of it on
  that Connection is not refused with `gateway_only` but fails its Destination check, since a gateway alone answers
  `checkChannel` with 501, as the fake's gateway bot does: such a Destination can only be deleted or moved to another
  Connection. The refusal
  comes before the Mention entries of S-140 are validated, so it also keeps such a Connection out of Mention entries
  (maintainer's decision Q2 of the plan review); S-139's `mention_connections` leaves it out by the same condition.
- **Account links** (`internal/accountlinks/query.sql`, `accountlinks.go`): `ListLinkableConnections` leaves out a
  Bridge Connection whose stored `info` has `roles` without `messenger` (Q2): a gateway-only bridge has `user_lookup`
  `none` and never sends `account_link` events, so linking through it could never finish. The Go end-to-end test of
  several Connections keeps its exact list of linkable Connections, now with "Example Gateway" asserted absent.
- **Fake bridge** (`internal/fakes/fakebridge`): `GatewayToken` `muster-dev-gateway-token`, a bot whose `info` declares
  `roles: [gateway]`, poll and push, `messenger.name` "Example Gateway"; `notifications.go` — `sendNotification` with
  the idempotency of the other calls, `/_fake/notifications` (each request with its key, contact, text, language and
  `action`), `POST /_fake/acknowledge` {`notification_id`} that queues `notification_acknowledged` for the bot;
  `/_fake/config` gains `roles` per token; `/_fake/faults` applies to `sendNotification` like to the other calls; a bot
  whose roles lack `messenger` answers 501 to the operations of a messenger (channels, messages and Thread replies,
  people and direct messages) and keeps `info`, events, answers and notifications.
- **Development mode** (`internal/devmode/devmode.go`, `internal/runtime/runtime.go`): the demo Bridge Connection
  "Example Gateway" with the gateway bot in poll mode, ensured and checked at start by the runtime; its base URL has the
  fake's path prefix `/gateway`, which the fake serves too, so that the faults that tests script on `/v1/…` paths for
  the other bots never reach the gateway's polls; AGENTS.md's paragraph on development mode names the third bot and its
  Connection, and its row of lint 3 names `delivery.Gateway.Notify` as ADR-0016 does.
- **Web test fixtures** (`web/src/test/fixtures/connections.ts`, S-166): `roles` `["messenger"]` in the Bridge
  Connection builders.
- **Log events** (`internal/logging/events.go`): `demo_check_failed` (warn; the Connection and what failed), C-01, for
  the check of the demo gateway at start; `gateway_notification_sent` (info; the Connection, the key, the class;
  never the contact or the text), C-30, whose absence of the contact and the text the lint-5 probe
  `gateway_notifications` (`internal/archlint/secretleak.go`) proves; `bridge_event_unhandled` (info; the Connection,
  the event id and type: an event of a type this binary reads but no handler takes yet, recorded as handled), C-22.
- **Settings**: `connection.bridge.limiter`, `bridge.info_max_age`.
- **Documentation**: Connect a messenger through a bridge (`docs/messengers/bridge.md`) — "Roles", saying that a bridge
  in the role `gateway` is a Notification gateway and cannot be a Destination; the page on Notification gateways is
  S-167's.

## Steps

1. Add the roles, `sendNotification` and the event to `api/bridge.yaml`, and their copies to `openapi.yaml`. Check:
   `make generate-check`; `make lint`; the two comparisons of the CI step "oasdiff breaking, api/bridge.yaml"
   (`.github/workflows/ci.yml`) run locally — `oasdiff breaking --fail-on ERR --flatten-allof` of
   `origin/master:api/bridge.yaml` against the new file, and the reverse with the step's `--severity-levels` — report
   no ERR but those the ignore file below names. Run on a draft of this change with oasdiff v1.33.0 in the plan
   review, three changes are ERR: forward, `response-property-one-of-added` (`NotificationAcknowledgedEvent` in the
   events of `GET /v1/events`) and `response-property-enum-value-added` (`notification` in `shown_as` of `POST
   /v1/answers`); reverse, `request-body-one-of-removed` (the same event in the pushed `bridgeEvent`). Each is gated by
   the role: a bridge sends `notification_acknowledged` only for a `sendNotification`, which Muster makes only to a
   bridge that declares `gateway`, and answers `shown_as` `notification` only to the `answerEvent` of such an event, so
   neither a Muster of 0.1 nor a bridge without the role ever meets them. The step therefore accepts them through
   `api/bridge-oasdiff-ignore.txt`, a checked-in file with exactly these three changes, each a line
   `<direction>|<method> <path> <text>` — `forward` for the comparison from the base to the head, `reverse` for the
   other — after a comment with this reason; a new ERR still fails, and the rule's other changes keep their level (the
   levels file stays as it is). oasdiff's `--err-ignore` is not used: v1.33.0 matches its lines by substring, without
   case and without a direction, and only by a path that starts with `/`, so never the change of the webhook,
   `webhook:bridgeEvent`. The step takes its status from a second run of each comparison with `--format json`, whose
   shape `jq` checks before it fails on every ERR (level 3) that no line of the same direction names exactly, and only
   the lines that the pull request adds to the file compared with `origin/<base>` count: once this story is merged,
   its lines accept nothing, so a later pull request that removes `NotificationAcknowledgedEvent` from `Event` or
   `notification` from `shown_as` fails, as does any other change of the webhook, such as a header that becomes
   optional. The markdown report may list the changes the file accepts.
2. Read the roles, write the client call, the adapter's `Notify`, the interface and lint 3. Check: `info_test.go`,
   `client_test.go`, `delivery_test.go`, `archlint_test.go`.
3. Decode the event; add the roles to the Connection read, the refusal to Destinations and the filter to the linkable
   Connections. Check: `events_test.go`, `bridge_test.go`, `write_test.go`, `live_test.go` of `internal/accountlinks`;
   `make generate-check`.
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
M=$(jq -nc '["new_alert_group","new_alerts","reopen","ack_timeout","snooze_ended","rise_to_urgent"]
  | map({key: ., value: {everyone: "none", user_ids: [], groups: []}}) | from_entries')
curl -s "${A[@]}" $API/destinations -d "{\"type\":\"bridge\",\"name\":\"gw\",\"connection_id\":\"$GW\",\"channel_id\":\"ch-alerts\",
  \"mentions\":$M,\"limiter\":{\"limit\":30,\"per_seconds\":60}}" | jq -c '{status, e: .errors[0] | {pointer, code}}'
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
curl -s "${A[@]}" "$API/request-log?connection=$GW" | jq -r '[.items[] | select(.operation == "getInfo")][0].url'
# http://127.0.0.1:18094/[redacted]/v1/info — the path prefix of a base URL is masked, as it may carry a secret
curl -s "${A[@]}" $API/me/account-links/connections | jq -c '[.items[].name] | index("Example Gateway")'   # null
```

`test/e2e/bridge_gateway_test.go` repeats these checks and adds a Connection without `roles` in its `info`, the
`role` filter, a `notification_acknowledged` event polled from the gateway bot and logged as unhandled, a replayed key,
and a fault scripted on `sendNotification` that the check reports.

## Open questions

1. **A Destination whose bridge later declares only `gateway`.** Settled by C-30.FR-1, which refuses only creating a
   Destination for such a Connection: a Destination saved while the bridge was a messenger stays, its deliveries fail as
   the bridge answers, and S-150 warns about it on the Connection page. Saving it again runs its Destination check,
   which a gateway alone fails, so it can only be deleted or moved to another Connection.

## Notes

- Suggested commit: `feat(bridge): add the gateway role with sendNotification and notification_acknowledged`.
- The libraries' copies of `api/bridge.yaml` fail their `make spec-check` once this merges, until S-146 and S-147 take
  the new copy. Their conformance runs of `muster bridge check` against `edge` compare the exact set of `SKIP` lines
  (the `RUN*_SKIP` lists of the Makefile of `muster-bridge-go`, lines 23–24 of that of `muster-bridge-python`), so they
  fail on the two new lines `SKIP notification…` too, until S-146 and S-147 add `notification notification_acknowledged`
  to those lists.
- The action data of a call names the call (C-30.FR-2); its subject and signing are S-148's, since the call does not
  exist before it. The check sends test data of the same size, so that a bridge proves it carries 64 bytes.
- No migration: the roles are read from the stored `info` (`connections.bridge_info`).

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-30.FR-1 | partial | the roles, the refusal and the Connections offered for links and mentions; the Connection page shows them from S-150 |
| C-30.FR-2 | partial | the protocol, the check and the fake; handling the event is S-173, the libraries S-146 and S-147 |
| C-30.FR-5 | partial | the interface and lint 3; the text of a call and the sender are S-148 |
| C-30.FR-10 | partial | requests to a gateway in the Request log and the log event; masking is S-167, the metrics S-148 and S-173 |
| C-30.AC-1 | partial | the API; "shown as a Notification gateway" on the page is S-150 |
| C-22.FR-2 | partial | revised for L2: the roles of `info`; group mentions are S-139 |
| C-22.FR-3 | partial | revised for L2: no Destination for a gateway-only Connection; the Mention entries are S-140 |
| C-22.FR-11 | partial | revised for L2: the event `notification_acknowledged` is read; S-173 acts on it |
| C-22.FR-21 | partial | revised for L2: the check of the role `gateway`; `layout` is S-126 |
| C-18.FR-10 | full | revised for L2: gateway-only Connections are not offered to link an account through |
