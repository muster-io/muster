---
id: S-080
title: "Bridge Destinations, the channel check, `muster doctor` and the Mention settings of Bridges (BE)"
capability: C-22
kind: be
layer: L1
depends_on: [S-073]
covers: [C-22.FR-3, C-22.AC-2, C-22.AC-3, C-22.AC-20]
files_touched:
  - api/openapi.yaml
  - internal/db/migrations/0009_bridge_destinations.up.sql
  - internal/db/migrations/0009_bridge_destinations.down.sql
  - design/db/schema.md
  - internal/bridge/client.go
  - internal/bridge/check.go
  - internal/bridge/check_test.go
  - internal/destinations/destinations.go
  - internal/destinations/destinations_test.go
  - internal/destinations/write.go
  - internal/destinations/write_test.go
  - internal/destinations/query.sql
  - internal/mentions/mentions.go
  - internal/mentions/mentions_test.go
  - internal/connections/doctor.go
  - internal/connections/doctor_test.go
  - internal/api/destinations.go
  - internal/api/destinations_test.go
  - internal/runtime/runtime.go
  - internal/fakes/fakebridge/channels.go
  - internal/fakes/fakebridge/fakebridge_test.go
  - internal/archlint/secretleak.go
  - web/src/components/destination-label.ts
  - web/src/components/destination-health.tsx
acceptance:
  - "[C-22.FR-3, C-22.AC-2] `createDestination` of type `bridge` takes a Bridge Connection, the channel id, Mention settings and its limiter (`destination.bridge.limiter`) and runs `checkChannel`; a channel the fake answers `forbidden` for is refused with 422 `destination_check_failed` at `/channel_id` with the bridge's `detail`, and the fake records no `postMessage`; a passing check stores `channel_name`, which `getDestination` and `listDestinations` return with `connection_id` and `channel_id`; a Connection of another type is refused with 422 at `/connection_id`."
  - "[C-22.FR-3, C-22.AC-20] `mentions.Validate` takes nobody and chosen users for a Bridge Destination and refuses everyone in the chat and messenger groups with 422 `unsupported` at their pointer (`mentions_test.go`, a case per kind)."
  - "[C-22.FR-3] `checkDestination` runs `checkChannel` through the interactive path and returns one item `channel`; a success on a Broken Destination ends the Broken state; `muster doctor` checks every Bridge Connection with `getInfo` and every Bridge Destination with `checkChannel`, printing an OK line for each and a FAIL line naming a failing one."
  - "[C-22.AC-3] With an HTTP proxy on the Connection, every request of the Destination check reaches the fake bridge only through the fake proxy; with the fake closing the connection, the token appears in no log line and no error returned by the API (`secretleak.go` probe `bridge_connections`, the Destination check added)."
  - "[C-22.FR-3] With `bridge` in the `Destination` union the SPA's type check passes: `destinationPlace` shows a Bridge Destination's channel name, or its channel id, and `destinationTypeName` names it \"Bridge\" until S-077 adds the messenger's name."
verify: "make ci test-integration"
operator_attention: false
issue: 238
---

# S-080. Bridge Destinations, the channel check, `muster doctor` and the Mention settings of Bridges (BE)

## Scope

**IN**

- The `bridge` Destination type: Connection, channel id, Mention settings (nobody or chosen users), limiter; its read
  model with the channel's name.
- The Destination check through `checkChannel` on save and from `checkDestination`, and `muster doctor` for Bridge
  Connections and Destinations.
- The Bridge rules of Mention settings.
- The channels of the fake bridge.

**OUT**

- The Broken probe through the same check, and everything that posts (S-074).
- Mentions of linked people and their names in footers (S-076).
- The Destination form and its labels (S-077).
- Interim states until later stories: a Bridge Destination on a Route gets no messages until S-074; a Mention of a User
  is their display name as text, which mentions nobody, until S-076; the SPA names the type "Bridge" without the
  messenger's name until S-077.

## Contracts

- **Spec** (`api/openapi.yaml`, changed here first, then generated): `Destination`, `DestinationInput` and the type
  filters gain `bridge`; new schemas `BridgeDestinationInput` (`connection_id`, `channel_id`, `mentions`, `limiter`) and
  `BridgeDestination` (with `channel_name`), joined to the discriminators; `DestinationCheckItem.name` gains `channel`,
  the one check item of a Bridge Destination.
- **Operations implemented for the type**: `listDestinations`, `getDestination`, `createDestination`,
  `updateDestination`, `deleteDestination` (`destinations:read` / `:write`), `checkDestination` (`destinations:test`,
  interactive).
- **Schema** (migration `0009_bridge_destinations`, expand only, `schema.md`): `destinations.type` and the type-checked
  foreign key to `connections (id, type)` accept `bridge`; new columns `bridge_channel_id` (required for the type) and
  `bridge_channel_name`; `destinations_type_fields_check` gains its branch. The CHECKs are widened `NOT VALID`, then
  validated; old replicas never write a Bridge row, and every row they write satisfies the new checks.
- **Read model** (`internal/destinations/destinations.go`): the type's fields — `connection_id`, `channel_id`,
  `channel_name` — in `getDestination`, `listDestinations` and the labels' data of S-070.
- **Write path** (C-22.FR-3; `internal/destinations/write.go`): the shared write path validates Mention settings through
  `mentions.Validate`, and the type runs `checkChannel` on save: `forbidden` → `422 destination_check_failed` at
  `/channel_id` with the `detail`; another class → the same problem with the class named; `200` →
  `bridge_channel_name` stored. The Connection must be a Bridge Connection that is not deleted. The default limiter is
  `destination.bridge.limiter`.
- **Mention settings** (`internal/mentions/mentions.go`): `everyoneOffered` and the group rule treat `bridge` as
  `telegram` — nobody or chosen users only — with a refusal text that names Bridges; `IdentitySpace("bridge", …)`
  returns no space until S-076, so a Mention of a User resolves to their display name as text.
- **Destination check** (`internal/bridge/check.go`, `client.go`): the client of S-073 gains `checkChannel`; one check
  item `channel`, from save and `checkDestination` through the interactive path, and from `muster doctor`
  (`internal/connections/doctor.go`), which also runs `getInfo` for every Bridge Connection; a success on a Broken
  Destination calls `MarkHealthy`. S-074 uses the same function as the Bridge delivery's `Check` for the Broken probe.
- **SPA type check** (`destination-label.ts`, `destination-health.tsx`): `destinationPlace` and `destinationTypeName`
  get an explicit `bridge` branch instead of falling through to the outgoing webhook's; no new text, which S-077 adds.
- **Fake bridge** (`channels.go`): `checkChannel` for `ch-alerts` ("alerts") and `ch-alerts-prod` ("alerts-prod") and
  `forbidden` "The bot is not a member of this channel." for `ch-nobot` and unknown ids; `/_fake/config` `channels`
  adds or forbids channels.
- **Secrets** (lint 5): probe `bridge_connections` also runs the Destination check and a failed one.
- **Defaults**: `destination.bridge.limiter`, `delivery.interactive_budget`.

## Steps

1. Add the migration, the Destination type with its read model and the Mention rules. Check: `write_test.go`,
   `destinations_test.go`, `mentions_test.go`.
2. Add the channel check and `muster doctor`. Check: `check_test.go`, `doctor_test.go`.
3. Narrow the SPA by type. Check: `make lint`.
4. Extend the fake and the secret probe. Check: `fakebridge_test.go`, then Verification.

## Verification

```sh
make dev > dev.log 2>&1 &
# the Admin's session (`jar`, `H`) as in S-011; the Connection "br" (C) as in S-073, in poll mode without a proxy
API=localhost:8080/api/v1; FB=127.0.0.1:18094/_fake
ETAG() { curl -s -b jar $API/connections/$C | jq -r .etag; }

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
curl -s -b jar $API/destinations/$D | jq -c '{type, channel_id, channel_name, l: .limiter.limit}'
# {"type":"bridge","channel_id":"ch-alerts","channel_name":"alerts","l":30}

# C-22.FR-3: the check and the end of Broken; muster doctor
psql "$MUSTER_DATABASE_URL" -qc "UPDATE destinations SET health = 'broken', broken_since = now(), broken_cause = 'fatal',
  broken_reason = 'test', next_probe_at = now() + interval '1 hour' WHERE public_id = '$D'"
curl -s "${H[@]}" -X POST $API/destinations/$D/checks | jq -c '{ok, i: [.checks[].name], h: .health.state}'
# {"ok":true,"i":["channel"],"h":"healthy"}
./bin/muster dev doctor | grep -i bridge                                          # OK lines for "Dev Bridge", "br" and "alerts"
curl -s -X PUT $FB/config -d '{"channels":{"ch-alerts":null}}' > /dev/null
./bin/muster dev doctor | grep FAIL | grep -c alerts                              # 1
curl -s -X PUT $FB/config -d '{"channels":{"ch-alerts":"alerts"}}' > /dev/null

# C-22.AC-3: the Destination check through the fake HTTP proxy, and no secret in an error
curl -s "${H[@]}" -X PUT -H "If-Match: $(ETAG)" $API/connections/$C -d '{"type":"bridge","name":"br","base_url":"http://127.0.0.1:18094",
  "event_delivery":"poll","proxy":{"enabled":true,"type":"http","address":"127.0.0.1:18091"},"limiter":{"limit":10,"per_seconds":1}}' > /dev/null
curl -s -X DELETE 127.0.0.1:18091/_fake/requests; curl -s -X DELETE $FB/requests
curl -s "${H[@]}" -X POST $API/destinations/$D/checks > /dev/null
echo $(( $(curl -s $FB/requests | jq length) - $(curl -s 127.0.0.1:18091/_fake/requests | jq length) ))   # 0
curl -s -X POST $FB/faults -d '{"path":"/v1/channels/*","delay_ms":8000,"times":1}' > /dev/null   # longer than the check waits
curl -s "${H[@]}" -X POST $API/destinations/$D/checks | grep -c 'muster-dev-bridge-token-b'   # 0
grep -c 'muster-dev-bridge-token-b' dev.log                                       # 0
curl -s "${H[@]}" -X PUT -H "If-Match: $(ETAG)" $API/connections/$C -d '{"type":"bridge","name":"br","base_url":"http://127.0.0.1:18094",
  "event_delivery":"poll","proxy":{"enabled":false},"limiter":{"limit":10,"per_seconds":1}}' > /dev/null
```

## Open questions

None.

## Notes

- Suggested commit: `feat(bridge): add bridge destinations and their channel check`.
- S-080 was split from S-073 by the plan review of the Bridge phase (D323), because S-073 touched about 50 files. It
  follows S-073 and comes before S-074, whose Broken probe and posts need a Bridge Destination.
- A Bridge Destination's check has one item, `channel`, because the bridge answers one question — can the bot post,
  edit and reply there — and names the reason when it cannot.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-22.FR-3 | partial | on save, through the API and in `muster doctor`; the probe is S-074, the page S-077 |
| C-22.AC-2 | full | |
| C-22.AC-3 | partial | the Destination check; the Connection check is S-073, the event paths S-075 |
| C-22.AC-20 | partial | Mention settings that name everyone in the chat are refused; Mentions of linked people are S-076 |
