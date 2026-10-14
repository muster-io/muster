---
id: S-073
title: "Bridge Connections and Destinations, the protocol client, the Destination check and the fake bridge (BE)"
capability: C-22
kind: be
layer: L1
depends_on: [S-069, S-071, S-059]
covers: [C-22.FR-1, C-22.FR-2, C-22.FR-3, C-22.FR-8, C-22.FR-20, C-22.FR-24, C-22.AC-1, C-22.AC-2, C-22.AC-3, C-22.AC-20, C-01.FR-13]
files_touched:
  - api/openapi.yaml
  - api/codegen-bridge.yaml
  - Makefile
  - AGENTS.md
  - internal/tools/licensecheck/main.go
  - internal/db/migrations/0008_bridge_connections_and_destinations.up.sql
  - internal/db/migrations/0008_bridge_connections_and_destinations.down.sql
  - design/db/schema.md
  - internal/bridge/client.go
  - internal/bridge/client_test.go
  - internal/bridge/info.go
  - internal/bridge/info_test.go
  - internal/bridge/check.go
  - internal/bridge/check_test.go
  - internal/bridge/classify.go
  - internal/bridge/classify_test.go
  - internal/connections/connections.go
  - internal/connections/bridge.go
  - internal/connections/bridge_test.go
  - internal/connections/query.sql
  - internal/connections/doctor.go
  - internal/connections/doctor_test.go
  - internal/destinations/write.go
  - internal/destinations/query.sql
  - internal/destinations/write_test.go
  - internal/api/connections.go
  - internal/api/connections_test.go
  - internal/api/destinations.go
  - internal/api/destinations_test.go
  - internal/api/problem.go
  - internal/fakes/fakebridge/fakebridge.go
  - internal/fakes/fakebridge/channels.go
  - internal/fakes/fakebridge/fakebridge_test.go
  - internal/devmode/devmode.go
  - internal/devmode/devmode_test.go
  - internal/runtime/runtime.go
  - internal/archlint/secretleak.go
  - internal/logging/events.go
acceptance:
  - "[C-22.FR-1] A Bridge Connection is created with a name, the base URL, a write-only token, the event delivery mode (`connection.bridge.event_delivery`), a write-only push secret required in push mode, a proxy and its limiter (`connection.bridge.limiter`); reads return `token_status`, `push_secret_status`, the read-only `callback_url` `MUSTER_INGEST_URL/api/v1/callbacks/bridge/<public_id>`, and `info` with `info_read_at`, null before the first check."
  - "[C-22.FR-1] A base URL with a query, a fragment or user information is refused with 422 at `/base_url`; a trailing `/` is dropped; `updateConnection` with a new `base_url` and no `token` is refused with 422 `required` at `/token`, so the stored token is never sent to another address."
  - "[C-22.FR-2, C-22.AC-1] `checkConnection` calls `getInfo` through the interactive path and returns the steps `info` and `event_delivery` with their latency and path, and the bridge's name, messenger, `protocol_version` `1` and features, which it stores on the Connection; with the fake answering `protocol_version` `2` the `info` step fails with \"The bridge speaks protocol version 2; this Muster speaks 1.\", and with a wrong token with \"The bridge refused the token.\""
  - "[C-22.FR-2] A Connection in push mode whose bridge offers only `poll` fails the `event_delivery` step with \"The bridge does not offer push event delivery.\"; a call that depends on `info` reads it again when Muster's copy is older than `bridge.info_max_age`."
  - "[C-22.FR-3, C-22.AC-2] `createDestination` of type `bridge` takes the Connection, the channel id, Mention settings and its limiter (`destination.bridge.limiter`) and runs `checkChannel`; a channel the fake answers `forbidden` for is refused with 422 `destination_check_failed` at `/channel_id` with the bridge's `detail`, and the fake records no `postMessage`; a passing check stores and returns `channel_name`."
  - "[C-22.FR-3, C-22.AC-20] Mention settings of a Bridge Destination that name everyone in the chat or messenger groups are refused with 422 `unsupported` at their pointer; nobody and chosen users are saved."
  - "[C-22.FR-3] `checkDestination` runs `checkChannel` through the interactive path; a success on a Broken Destination ends the Broken state; `muster doctor` checks every Bridge Connection with `getInfo` and every Bridge Destination with `checkChannel`, printing a FAIL line naming the failing one."
  - "[C-22.FR-8] The client classifies every answer by the problem's `class`, and without one by its status — `401` and `403` forbidden, `429` and `503` with `Retry-After` rate limited, `429` without it, other `5xx`, timeouts and network errors transient, redirects and everything else invalid — and keeps the problem's `detail` as masked, untrusted text (`classify_test.go`)."
  - "[C-22.AC-3] With an HTTP proxy on the Connection, every request of the Connection check and the Destination check reaches the fake bridge only through the fake proxy; with the fake closing the connection, the token and the push secret appear in no log line and no error returned by the API (`secretleak.go` probe `bridge_connections`)."
  - "[C-22.FR-20] The models of `api/bridge.yaml` are generated into `internal/bridge/gen/` by `make generate` (models only, no client or server), and `make generate-check` fails when they are stale."
  - "[C-22.FR-24] A successful `info` step logs `bridge_info_read` with the Connection, the protocol version and the names of the features; the log event is in the registry and its reference page."
  - "[C-01.FR-13] The fake bridge answers every operation this story uses as `api/bridge.yaml` specifies and checks the bearer token; `muster dev` starts it on `127.0.0.1:18094` and creates the demo Connection \"Dev Bridge\" in poll mode, without Destinations."
verify: "make ci test-integration"
operator_attention: false
issue: null
---

# S-073. Bridge Connections and Destinations, the protocol client, the Destination check and the fake bridge (BE)

## Scope

**IN**

- The `bridge` Connection type: base URL, token, event delivery mode, push secret, proxy, limiter, the read-only
  callback address and the last `info`; its Connection check through `getInfo`.
- The `bridge` Destination type: Connection, channel id, Mention settings (nobody or chosen users), limiter; the
  Destination check through `checkChannel` on save, from `checkDestination` and in `muster doctor`.
- The protocol client over the outbound package, with the models generated from `api/bridge.yaml`, the answer
  classification of C-22.FR-8 and `info` with its refresh.
- The fake bridge with channels, `info` and the token check, and the demo Connection of `muster dev`.

**OUT**

- The adapter — envelopes, posts, edits, Thread replies, idempotency, edit windows, test messages, the Broken probe
  through `Check` — and the Messengers → Bridge page (S-074).
- Events by polling and push, presses, answers, Text commands and deleted messages (S-075).
- Account links and Mentions of linked people (S-076); the pages (S-077); the example bridge and
  `muster bridge check` (S-078); "Write a bridge" and the protocol reference on the site (S-079).

## Contracts

- **Spec** (`api/openapi.yaml`, changed here first, then generated): `ConnectionType` and `DestinationType` gain
  `bridge`; new schemas `BridgeConnectionBase`, `BridgeConnection` (with `token_status`, `push_secret_status`,
  `callback_url`, `info` — the `Info` of `api/bridge.yaml`, copied as `BridgeInfo` — `info_read_at`,
  `destination_count`, `proxy`, `etag`), `BridgeConnectionInput` (`token`, `push_secret`, write-only),
  `BridgeEventDelivery` (`poll`, `push`), `BridgeDestinationInput` (`connection_id`, `channel_id`) and
  `BridgeDestination` (`channel_name`), joined to the `Connection`, `ConnectionInput`, `Destination` and
  `DestinationInput` discriminators; `ConnectionCheckStep.name` gains `info` and `event_delivery`;
  `ConnectionCheckResult` returns `info` for a Bridge.
- **Operations implemented for the type**: `listConnections`, `createConnection`, `getConnection`,
  `updateConnection`, `deleteConnection` (`connections:read` / `:write`), `checkConnection` (`connections:write`,
  interactive), `createDestination`, `updateDestination` (`destinations:write`), `checkDestination`
  (`destinations:test`, interactive). `listConnectionChannels` answers `409` for a Bridge Connection: the protocol has
  no channel list, the Admin enters the channel id.
- **Schema** (migration `0008_bridge_connections_and_destinations`, expand only, `schema.md`): `connections.type` and
  the type-checked foreign keys accept `bridge`; new columns `bridge_base_url`, `bridge_event_delivery` (`poll`, `push`),
  `bridge_events_cursor` (written by S-075), `bridge_push_secret_ciphertext`, `_key_id`, `_updated_at`, `bridge_info`
  (`jsonb`) and `bridge_info_read_at`; the token uses the `bot_token_*` columns; `connections_type_fields_check` gains
  the Bridge branch (base URL and mode set, the other types' columns null, a push secret in push mode until the
  Connection is deleted). `destinations.type` accepts `bridge` with `bridge_channel_id` (required) and
  `bridge_channel_name`; `destinations_type_fields_check` gains its branch.
- **Connections** (`internal/connections/bridge.go`): the base URL rules of C-22.FR-1; the token and the push secret
  through the secret-field helper of S-013; a change of `event_delivery` to `push` requires a `push_secret`; Audit log
  entries as for the other types (`secret_changed` for both secrets); `deleteConnection` follows S-039.
- **Client** (`internal/bridge/client.go`, `info.go`, `classify.go`): `getInfo`, `checkChannel`, and the request
  plumbing every later story uses — `<base>/v1/<path>`, `Authorization: Bearer <token>`, JSON in and out with the
  generated models, `application/problem+json` decoded into `Problem` — through `internal/outbound` with the
  Connection's proxy, in the client class of the caller (ADR-0015); each `message_id` and `channel_id` is
  percent-encoded as one path segment. The token is registered for redaction. `info` reads `protocol_version` before it
  validates the rest, so a newer bridge fails with the version named, not with a schema error. `Classify` maps an answer
  to `transient`, `rate_limited` (with `Retry-After` and `scope`), `gone` (with `reason`), `forbidden` or `invalid` as
  C-22.FR-8 says. `InfoCache` keeps `info` per Connection and re-reads it through the caller's client class when
  `bridge_info_read_at` is older than `bridge.info_max_age`.
- **Connection check** (C-22.FR-2): through `delivery.Interactive` on the Connection's limiter (`ReadOp`), steps `info`
  (latency, `via`) and `event_delivery`; failures with the texts of
  [reference.md](../prd/l1/reference.md#banners-warnings-and-notices); a passing `info` step stores `bridge_info` and
  `bridge_info_read_at`, also when `event_delivery` fails.
- **Destinations** (C-22.FR-3; `internal/destinations/write.go`): the shared write path validates Mention settings
  through `mentions.Validate` with the Bridge rules (nobody and chosen users only, as for Telegram), and the type runs
  `checkChannel` on save: `forbidden` → `422 destination_check_failed` at `/channel_id` with the `detail`; another
  class → the same problem with the class named; `200` → `bridge_channel_name` stored. The default limiter is
  `destination.bridge.limiter`.
- **Destination check** (`internal/bridge/check.go`): one check `channel`, from save and `checkDestination` through the
  interactive path, and from `muster doctor` (`internal/connections/doctor.go`); a success on a Broken Destination
  calls `MarkHealthy`. S-074 uses the same function as the adapter's `Check` for the Broken probe.
- **Fake bridge** (`internal/fakes/fakebridge`, `127.0.0.1:18094`): two bots, as the protocol's "one token, one bot"
  says, with the published tokens `muster-dev-bridge-token`, used by the demo Connection, and
  `muster-dev-bridge-token-b`, for which `muster dev` creates no Connection and which the checks of the Bridge stories
  use; each bot has its own messages and events. `getInfo` with the messenger "Fake chat", every feature on and both
  delivery modes by default; `checkChannel` for `ch-alerts` ("alerts")
  and `ch-alerts-prod` ("alerts-prod") and `forbidden` "The bot is not a member of this channel." for `ch-nobot` and
  unknown ids; `401` for any other token and for a revoked one; control endpoints `/_fake/config` (protocol version,
  features, limits, user lookup, delivery modes, revoked tokens), `/_fake/requests` and `/_fake/faults` of the
  fake-server harness. AGENTS.md's development mode lists the fake bridge and its two tokens. Later stories add
  messages (S-074), events (S-075) and people (S-076).
- **Generated models**: `api/codegen-bridge.yaml` (oapi-codegen, `models` only) writes `internal/bridge/gen/`; `make
  generate` and `generate-check` run it; the Makefile's `GENERATED` list, the generated globs of the licence check and
  the generated paths of AGENTS.md gain the directory.
- **Log events**: `bridge_info_read` (info: the Connection, the protocol version, the features' names).
- **Secrets** (lint 5): probe `bridge_connections` pushes the token and the push secret through create, check,
  Destination check and a failed request.
- **Defaults**: `connection.bridge.limiter`, `destination.bridge.limiter`, `connection.bridge.event_delivery`,
  `bridge.info_max_age`, `delivery.interactive_budget`.

## Steps

1. Generate the models and write the client with `Classify` and `info`. Check: `client_test.go`, `classify_test.go`,
   `info_test.go` against `httptest` servers, a redirect included.
2. Add the migration, the Connection type and its check. Check: `bridge_test.go`, `connections_test.go`.
3. Add the Destination type and its check, and `muster doctor`. Check: `write_test.go`, `check_test.go`,
   `doctor_test.go`.
4. Write the fake bridge, the demo Connection and the secret probe. Check: `fakebridge_test.go`, then Verification.

## Verification

```sh
make dev &
# the Admin's session (`jar`, `H`) as in S-011
API=localhost:8080/api/v1; FB=127.0.0.1:18094/_fake

# C-22.FR-1, FR-2, AC-1: a Connection and its check
C=$(curl -s "${H[@]}" $API/connections -d '{"type":"bridge","name":"br","base_url":"http://127.0.0.1:18094/",
  "token":"muster-dev-bridge-token-b","event_delivery":"poll","proxy":{"enabled":false},"limiter":{"limit":10,"per_seconds":1}}' | jq -r .id)
curl -s -b jar $API/connections/$C | jq -c '{base_url, t: .token_status.set, info, cb: (.callback_url | test("/api/v1/callbacks/bridge/CN[0-9A-Z]{12}$"))}'
# {"base_url":"http://127.0.0.1:18094","t":true,"info":null,"cb":true}
curl -s "${H[@]}" -X POST $API/connections/$C/checks | jq -c '{ok, s: [.steps[] | {name, ok, via}], v: .info.protocol_version, m: .info.messenger.name}'
# {"ok":true,"s":[{"name":"info","ok":true,"via":"direct"},{"name":"event_delivery","ok":true,"via":"direct"}],"v":"1","m":"Fake chat"}
curl -s -X PUT $FB/config -d '{"protocol_version":"2"}' > /dev/null
curl -s "${H[@]}" -X POST $API/connections/$C/checks | jq -r '.steps[0].message'
# The bridge speaks protocol version 2; this Muster speaks 1.
curl -s -X PUT $FB/config -d '{"protocol_version":"1","revoked":["muster-dev-bridge-token-b"]}' > /dev/null
curl -s "${H[@]}" -X POST $API/connections/$C/checks | jq -r '.steps[0].message'   # The bridge refused the token.
curl -s -X PUT $FB/config -d '{"revoked":[],"event_delivery":["poll"]}' > /dev/null

# C-22.FR-1: push needs a secret and a bridge that offers push
ETAG() { curl -s -b jar $API/connections/$C | jq -r .etag; }
curl -s "${H[@]}" -X PUT -H "If-Match: $(ETAG)" $API/connections/$C -d '{"type":"bridge","name":"br","base_url":"http://127.0.0.1:18094",
  "event_delivery":"push","proxy":{"enabled":false},"limiter":{"limit":10,"per_seconds":1}}' | jq -c '[.status, .errors[0].pointer]'
# [422,"/push_secret"]
curl -s "${H[@]}" -X PUT -H "If-Match: $(ETAG)" $API/connections/$C -d '{"type":"bridge","name":"br","base_url":"http://127.0.0.1:18094",
  "event_delivery":"push","push_secret":"whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw","proxy":{"enabled":false},"limiter":{"limit":10,"per_seconds":1}}' > /dev/null
curl -s "${H[@]}" -X POST $API/connections/$C/checks | jq -r '.steps[1].message'   # The bridge does not offer push event delivery.
curl -s -X PUT $FB/config -d '{"event_delivery":["poll","push"]}' > /dev/null
curl -s "${H[@]}" -X PUT -H "If-Match: $(ETAG)" $API/connections/$C -d '{"type":"bridge","name":"br","base_url":"http://127.0.0.1:9",
  "event_delivery":"poll","proxy":{"enabled":false},"limiter":{"limit":10,"per_seconds":1}}' | jq -c '[.status, .errors[0].pointer]'
# [422,"/token"]

# C-22.FR-3, AC-2: Destinations
M='{"new_alert_group":{"everyone":"none","user_ids":[],"groups":[]},"new_alerts":{"everyone":"none","user_ids":[],"groups":[]},
"reopen":{"everyone":"none","user_ids":[],"groups":[]},"ack_timeout":{"everyone":"none","user_ids":[],"groups":[]},
"snooze_ended":{"everyone":"none","user_ids":[],"groups":[]},"rise_to_urgent":{"everyone":"none","user_ids":[],"groups":[]}}'
MKD() { curl -s "${H[@]}" $API/destinations -d "{\"type\":\"bridge\",\"name\":\"$1\",\"connection_id\":\"$C\",\"channel_id\":\"$2\",
  \"mentions\":${3:-$M},\"limiter\":{\"limit\":30,\"per_seconds\":60}}"; }
curl -s -X DELETE $FB/requests
MKD nobot ch-nobot | jq -c '{status, code: .errors[0].code, p: .errors[0].pointer, d: .errors[0].detail}'
# {"status":422,"code":"destination_check_failed","p":"/channel_id","d":"The bot is not a member of this channel."}
curl -s $FB/requests | jq '[.[] | select(.path == "/v1/messages")] | length'      # 0
MKD all ch-alerts "$(jq -c '.new_alert_group.everyone = "channel"' <<<"$M")" | jq -c '[.status, .errors[0].code]'
# [422,"unsupported"]
D=$(MKD alerts ch-alerts | jq -r .destination.id)
curl -s -b jar $API/destinations/$D | jq -c '{channel_id, channel_name, l: .limiter.limit}'
# {"channel_id":"ch-alerts","channel_name":"alerts","l":30}
psql "$MUSTER_DATABASE_URL" -qc "UPDATE destinations SET health = 'broken', broken_since = now(), broken_cause = 'fatal',
  broken_reason = 'test', next_probe_at = now() + interval '1 hour' WHERE public_id = '$D'"
curl -s "${H[@]}" -X POST $API/destinations/$D/checks | jq -c '{ok, h: .health.state}'   # {"ok":true,"h":"healthy"}
./bin/muster dev doctor | grep -i bridge                                          # OK lines for "br" and "alerts"

# C-22.AC-3: through the fake HTTP proxy, and no secret in an error
curl -s "${H[@]}" -X PUT -H "If-Match: $(ETAG)" $API/connections/$C -d '{"type":"bridge","name":"br","base_url":"http://127.0.0.1:18094",
  "event_delivery":"poll","proxy":{"enabled":true,"type":"http","address":"127.0.0.1:18091"},"limiter":{"limit":10,"per_seconds":1}}' > /dev/null
curl -s -X DELETE 127.0.0.1:18091/_fake/requests; curl -s -X DELETE $FB/requests
curl -s "${H[@]}" -X POST $API/connections/$C/checks | jq -r '.steps[0].via'   # proxy
curl -s "${H[@]}" -X POST $API/destinations/$D/checks > /dev/null
echo $(( $(curl -s $FB/requests | jq length) - $(curl -s 127.0.0.1:18091/_fake/requests | jq length) ))   # 0
curl -s -X POST $FB/faults -d '{"path":"/v1/info","close":true,"times":1}' > /dev/null
curl -s "${H[@]}" -X POST $API/connections/$C/checks | grep -c 'muster-dev-bridge-token-b'   # 0
grep -c 'muster-dev-bridge-token-b\|whsec_MfKQ' muster-dev.log                     # 0

# C-01.FR-13: the demo Connection
curl -s -b jar $API/connections | jq -r '.items[] | select(.type == "bridge") | .name' | sort | tr '\n' ' '   # Dev Bridge br
```

## Open questions

None.

## Notes

- Suggested commit: `feat(bridge): add bridge connections, destinations and the protocol client`.
- The fake bridge is the contract of the Bridge stories with each other: every story extends it with the operations it
  uses, and `fakebridge_test.go` checks each against `api/bridge.yaml` with a request and response validator built
  from the spec.
- `bridge_info` is the whole `info` as the bridge sent it, so a field added in a later minor version of the protocol is
  kept even before Muster reads it.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-22.FR-1 | partial | the API; the page is S-077 |
| C-22.FR-2 | partial | the API; the page is S-077 |
| C-22.FR-3 | partial | on save, through the API and in `muster doctor`; the probe is S-074, the page S-077 |
| C-22.FR-8 | partial | the classification; delivery's use of it is S-074 |
| C-22.FR-20 | partial | the generated models; the published reference is S-079 |
| C-22.FR-24 | partial | `bridge_info_read`; S-074 and S-075 add theirs |
| C-22.AC-1 | full | |
| C-22.AC-2 | full | |
| C-22.AC-3 | partial | the push secret through pushed events is S-075 |
| C-22.AC-20 | partial | Mention settings that name everyone in the chat are refused; Mentions of linked people are S-076 |
| C-01.FR-13 | partial | the fake bridge and the demo Bridge Connection; S-074 to S-076 extend the fake |
