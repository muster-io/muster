---
id: S-098
title: "Slack Destinations, the channel check, the channel list, `muster doctor` and the Mention settings of Slack (BE)"
capability: C-24
kind: be
layer: L1
depends_on: [S-097]
covers: [C-24.FR-3, C-24.FR-6, C-24.AC-3, C-24.AC-4, C-24.AC-15, C-12.FR-8, C-02.FR-14]
files_touched:
  - api/openapi.yaml
  - internal/db/migrations/0017_slack_destinations.up.sql
  - internal/db/migrations/0017_slack_destinations.down.sql
  - design/db/schema.md
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - internal/slack/client.go
  - internal/slack/channels.go
  - internal/slack/channels_test.go
  - internal/destinations/destinations.go
  - internal/destinations/destinations_test.go
  - internal/destinations/write.go
  - internal/destinations/write_test.go
  - internal/destinations/query.sql
  - internal/destinations/delete.go
  - internal/destinations/test.go
  - internal/mentions/mentions.go
  - internal/mentions/mentions_test.go
  - internal/connections/slack.go
  - internal/connections/slack_test.go
  - internal/connections/connections_test.go
  - internal/connections/doctor.go
  - internal/connections/doctor_test.go
  - internal/connections/query.sql
  - internal/doctor/doctor_test.go
  - internal/api/destinations.go
  - internal/api/destinations_test.go
  - internal/api/connections.go
  - internal/api/connections_test.go
  - internal/runtime/runtime.go
  - internal/fakes/fakeslack/fakeslack.go
  - internal/fakes/fakeslack/channels.go
  - internal/fakes/fakeslack/fakeslack_test.go
  - internal/archlint/secretleak.go
  - web/src/components/destination-label.ts
  - web/src/components/destination-health.tsx
  - web/src/routes/destinations.$destinationId.tsx
  - web/src/components/destination-test-panel.tsx
  - web/src/components/channel-picker.tsx
  - web/src/components/connection-check.tsx
  - web/src/locales/en.json
  - web/src/locales/ru.json
acceptance:
  - "[C-24.FR-3, C-24.AC-4] `createDestination` of type `slack` takes a Slack Connection, the channel id, Mention settings and its limiter (`destination.slack.limiter`) and runs the Destination check; a channel the bot is not in is refused with 422 `destination_check_failed` at `/channel_id`, the `check_code` `bot_not_in_channel` and \"Invite the bot to the channel: /invite @{bot}\" with the bot's name; an archived channel with `channel_archived`, an unknown one with `channel_not_found`; an id that is not `C…` or `G…`, such as `D…` or `U…`, with 422 `invalid_format` at `/channel_id` by the domain before any request; a Connection of another type with 422 at `/connection_id`; a passing check stores `channel_name`, which `getDestination` and `listDestinations` return with `connection_id` and `channel_id`."
  - "[C-24.FR-3] `checkDestination` of a Slack Destination calls `conversations.info` through the interactive path and returns one item `channel`, whose code is `token_invalid` for a refused token, `missing_scope` with the scope named, `channel_not_found`, `channel_archived` or `bot_not_in_channel`, the code of `delivery.FailureCode` for a request without an answer or one the outbound address policy refuses, and `request_failed` with Slack's `error` otherwise (`channels_test.go`, a case per answer); a success on a Broken Destination ends the Broken state."
  - "[C-24.FR-3] `listConnectionChannels` of a Slack Connection returns the public and private channels that the bot is a member of and that are not archived, read with `conversations.list` (`types=public_channel,private_channel`, `exclude_archived=true`, `limit=200`) page by page up to 5 pages, with `truncated` true when more were left, filtered by `q` on the name; a `429` on a page is answered `503` with `Retry-After`."
  - "[C-24.FR-6, C-24.AC-15, C-12.FR-8] `mentions.Validate` takes for a Slack Destination nobody, `channel`, `here` and chosen users, and refuses `all` and messenger groups with 422 `unsupported` at their pointer (`mentions_test.go`, a case per kind)."
  - "[C-02.FR-14] `muster doctor` checks every Slack Connection with the steps of its check and every Slack Destination with `conversations.info`, printing an OK line for each and a FAIL line naming a failing one; the skip of S-097 is gone."
  - "[C-24.AC-3] With an HTTP proxy on the Connection, every request of the Destination check and of the channel list reaches the fake Slack server only through the fake proxy; with the fake answering after the check stopped waiting, the bot token appears in no log line and no error returned by the API (`secretleak.go` probe `slack_connections`, the Destination check added)."
  - "[C-24.FR-3] With `slack` in the `Destination` union the SPA's type check passes: `destinationPlace` shows a Slack Destination's channel name, else its channel id, with `#` before it, `destinationTypeName` names it \"Slack\", and the Destination page shows its name, Broken banner and Routes but no form until S-103; `checkCodeText` translates `missing_scope` for the Destination check."
verify: "make ci test-integration"
operator_attention: false
issue: 307
---

# S-098. Slack Destinations, the channel check, the channel list, `muster doctor` and the Mention settings of Slack (BE)

## Scope

**IN**

- The `slack` Destination type: a Slack Connection, the channel id with its name learned by the check, the Mention
  settings and the limiter.
- The Destination check with its one item `channel` (`conversations.info`), on save, from "Check" and in
  `muster doctor`, and `muster doctor` for Slack Connections.
- The channel list for the form (`conversations.list`), and the Mention settings a Slack Destination takes.
- Channels in the fake Slack server.

**OUT**

- Messages, the Broken probe and test messages (S-099); presses (S-100, S-101); Mentions of linked people (S-102).
- The Destination form with the channel picker (S-103).
- Interim state until S-103: the Destination page of a Slack Destination shows no form.

## Contracts

- **Spec** (`api/openapi.yaml`): `DestinationType`, `Destination`, `DestinationInput` and the type filters gain `slack`;
  new schemas `SlackDestinationInput` (`type`, `name`, `connection_id`, `channel_id`, a string without a pattern in the
  spec, so that the domain refuses an id that is not `C…` or `G…` with 422 `invalid_format` instead of request
  validation's `400`, `mentions`, `limiter`) and `SlackDestination` (with `channel_name`), joined to the discriminators;
  `DestinationCheckItem.name` describes `channel` for Slack too; `NullableDestinationCheckCode` gains `missing_scope`;
  `listConnectionChannels` answers for a Slack Connection `SlackChannelList` (`items` of `id`, `name`, `is_private`, and
  `truncated`), its `200` a `oneOf` of `MattermostChannelList` and `SlackChannelList`, and takes `q`;
  `MentionSetting.everyone` describes what Slack takes.
- **Operations implemented for the type**: `createDestination`, `updateDestination`, `getDestination`,
  `listDestinations`, `deleteDestination` (`destinations:read` / `:write`), `checkDestination` (`destinations:write`,
  interactive), `listConnectionChannels` (`connections:read`, interactive). `testDestination` and
  `previewDestination` answer `409` for a Slack Destination until S-099.
- **Schema** (migration `0017_slack_destinations`, expand only, `schema.md`): `destinations.type` accepts `slack`;
  new columns `slack_channel_id` and `slack_channel_name`; the type CHECK gains the Slack branch (a Connection and a
  channel id, the other types' columns null); the composite foreign key `(connection_id, type)` already binds a Slack
  Destination to a Slack Connection.
- **Check** (`internal/slack/channels.go`): `CheckChannel` — `conversations.info` with the bot token, through the
  caller's client class: the interactive path for a save and for "Check", the delivery class for the probe of S-099; the
  codes of C-24.FR-3, the bot's name from the Connection in the text of `bot_not_in_channel`. `Channels` — the list of
  C-24.FR-3, which honours `429` by answering it to the caller instead of waiting.
- **Destinations** (`internal/destinations`): the type in the validation, the write path, the check on save, the
  delete path and the dispatch of checks; the channel id kept as entered, upper case.
- **Mentions** (`internal/mentions/mentions.go`): the rule of C-24.FR-6 in `Validate`; rendering Slack Mentions is
  S-099 and S-102.
- **Doctor** (`internal/connections/doctor.go`): Slack Connections and Destinations as the acceptance says, in the
  doctor's own client class, as for the other types.
- **SPA type check** (`destination-label.ts`, `destination-health.tsx`, `destinations.$destinationId.tsx`,
  `connection-check.tsx`, `destination-test-panel.tsx`, whose `testHint` switches over the type, and
  `channel-picker.tsx`, which reads `listConnectionChannels` as Mattermost's list and narrows the new `oneOf` to it):
  explicit `slack` branches; the name "Slack" from `destinations.types.slack`; the new code translated: "The bot token
  lacks a scope this check needs: {scope}."
- **Fake Slack server**: channels per app in `/_fake/config` (`id`, `name`, `member`, `archived`, `private`), merged by
  id, so one call changes one channel, with `C0FAKEALERTS` "alerts" and `C0FAKEOTHER` "other" for both apps, the bot a
  member of "alerts" only; `/api/conversations.info` and `/api/conversations.list` with `next_cursor` paging, `limit`
  and a `page_size` in `/_fake/config` that forces small pages, answering as F-098 says; `not_in_channel` and
  `channel_not_found` for the messages of S-099.
- **Secrets** (lint 5): the probe `slack_connections` gains the Destination check and the channel list.
- **Defaults**: `destination.slack.limiter` (P-61), `destination.mentions`.

## Steps

1. Add the migration, the type, the check and the Mention rule. Check: `channels_test.go`, `mentions_test.go`,
   `write_test.go`, `go test -tags integration ./internal/db/...`.
2. Add the channel list and `muster doctor`. Check: `doctor_test.go`, `connections_test.go`.
3. Narrow the SPA. Check: `make lint`.
4. Add channels to the fake server and extend the secret probe. Check: `fakeslack_test.go`, then Verification.

## Verification

```sh
make dev > dev.log 2>&1 &
# the Admin's session (`jar`, `H`) as in S-011; the Connection "sl" (C) as in S-097, in `socket` mode, no proxy
API=localhost:8080/api/v1; FS=127.0.0.1:18097/_fake
M='{"new_alert_group":{"everyone":"none","user_ids":[],"groups":[]},"new_alerts":{"everyone":"none","user_ids":[],"groups":[]},
"reopen":{"everyone":"none","user_ids":[],"groups":[]},"ack_timeout":{"everyone":"none","user_ids":[],"groups":[]},
"snooze_ended":{"everyone":"none","user_ids":[],"groups":[]},"rise_to_urgent":{"everyone":"none","user_ids":[],"groups":[]}}'
MKD() { curl -s "${H[@]}" $API/destinations -d "{\"type\":\"slack\",\"name\":\"$1\",\"connection_id\":\"$2\",\"channel_id\":\"$3\",
  \"mentions\":${4:-$M},\"limiter\":{\"limit\":30,\"per_seconds\":60}}"; }

# C-24.FR-3, AC-4: Destinations
curl -s -X DELETE $FS/requests
MKD other $C C0FAKEOTHER | jq -c '{status, code: .errors[0].code, p: .errors[0].pointer, d: .errors[0].detail, cc: .errors[0].check_code}'
# {"status":422,"code":"destination_check_failed","p":"/channel_id","d":"Invite the bot to the channel: /invite @Fake bot B","cc":"bot_not_in_channel"}
curl -s $FS/requests | jq '[.[] | select(.path == "/api/chat.postMessage")] | length'   # 0
MKD dm $C D0FAKEDM | jq -c '[.status, .errors[0].code, .errors[0].pointer]'           # [422,"invalid_format","/channel_id"]
MKD gone $C C0FAKEGONE | jq -c '.errors[0].check_code'                                  # "channel_not_found"
MKD all $C C0FAKEALERTS "$(jq -c '.new_alert_group.everyone = "all"' <<<"$M")" | jq -c '[.status, .errors[0].code]'   # [422,"unsupported"]
MKD grp $C C0FAKEALERTS "$(jq -c '.new_alerts.groups = ["oncall"]' <<<"$M")" | jq -c '[.status, .errors[0].code]'    # [422,"unsupported"]
D=$(MKD alerts $C C0FAKEALERTS "$(jq -c '.new_alert_group.everyone = "here"' <<<"$M")" | jq -r .destination.id)
curl -s -b jar $API/destinations/$D | jq -c '{type, channel_id, channel_name, e: .mentions.new_alert_group.everyone}'
# {"type":"slack","channel_id":"C0FAKEALERTS","channel_name":"alerts","e":"here"}
curl -s -X PUT $FS/config -d '{"channels":{"b":{"C0FAKEARCH":{"name":"old","member":true,"archived":true}}}}' > /dev/null
MKD old $C C0FAKEARCH | jq -c '.errors[0].check_code'                                  # "channel_archived"

# C-24.FR-3: the channel list, over several pages
curl -s -X PUT $FS/config -d '{"page_size":1,"channels":{"b":{"C0FAKEPRIV":{"name":"private-ops","member":true,"private":true}}}}' > /dev/null
curl -s -b jar "$API/connections/$C/channels" | jq -c '{n: [.items[].name] | sort, t: .truncated}'   # {"n":["alerts","private-ops"],"t":false}
curl -s $FS/requests | jq '[.[] | select(.path == "/api/conversations.list")] | length > 1'          # true
curl -s -X PUT $FS/config -d '{"page_size":0}' > /dev/null

# C-24.FR-3: the check and the end of Broken; muster doctor
psql "$MUSTER_DATABASE_URL" -qc "UPDATE destinations SET health = 'broken', broken_since = now(), broken_cause = 'fatal',
  broken_reason = 'test', next_probe_at = now() + interval '1 hour' WHERE public_id = '$D'"
curl -s "${H[@]}" -X POST $API/destinations/$D/checks | jq -c '{ok, i: [.checks[].name], h: .health.state}'
# {"ok":true,"i":["channel"],"h":"healthy"}
./bin/muster dev doctor | grep -E '^OK +(connection (Dev Slack|sl)|destination alerts):' | wc -l   # 3
curl -s -X PUT $FS/config -d '{"channels":{"b":{"C0FAKEALERTS":{"name":"alerts","member":false}}}}' > /dev/null
./bin/muster dev doctor | grep FAIL | grep -c alerts                              # 1
curl -s -X PUT $FS/config -d '{"channels":{"b":{"C0FAKEALERTS":{"name":"alerts","member":true}}}}' > /dev/null

# C-24.AC-3: through the fake HTTP proxy, and no secret in an error
ETAG() { curl -s -b jar $API/connections/$C | jq -r .etag; }
curl -s "${H[@]}" -X PUT -H "If-Match: $(ETAG)" $API/connections/$C -d '{"type":"slack","name":"sl","event_mode":"socket",
  "proxy":{"enabled":true,"type":"http","address":"127.0.0.1:18091"},"limiter":{"limit":50,"per_seconds":60}}' > /dev/null
curl -s -X DELETE 127.0.0.1:18091/_fake/requests; curl -s -X DELETE $FS/requests
curl -s "${H[@]}" -X POST $API/destinations/$D/checks > /dev/null; curl -s -b jar "$API/connections/$C/channels" > /dev/null
echo $(( $(curl -s $FS/requests | jq '[.[] | select(.path | startswith("/api/conversations."))] | length') - $(curl -s 127.0.0.1:18091/_fake/requests | jq length) ))   # 0
curl -s -X POST $FS/faults -d '{"path":"/api/conversations.info","delay_ms":12000,"times":1}' > /dev/null
curl -s "${H[@]}" -X POST $API/destinations/$D/checks | grep -c 'muster-dev-slack'   # 0
grep -c 'xoxb-muster-dev-slack-token-b' dev.log                                    # 0

# Live, on the test workspace: the Connection "test workspace" (L) as in S-097; nothing is posted
LMKD() { curl -s "${H[@]}" $API/destinations -d "{\"type\":\"slack\",\"name\":\"$1\",\"connection_id\":\"$L\",\"channel_id\":\"$2\",
  \"mentions\":$M,\"limiter\":{\"limit\":30,\"per_seconds\":60}}"; }
# SLACK_ALERTS and SLACK_NOBOT: the ids of the test workspace's #alerts and of a channel without the bot, which the
# maintainer gives the lead (they stay out of the repository)
LD=$(LMKD live-alerts "$SLACK_ALERTS" | jq -r .destination.id)
curl -s -b jar $API/destinations/$LD | jq -r .channel_name                       # alerts
LMKD live-nobot "$SLACK_NOBOT" | jq -r '.errors[0].check_code'                    # bot_not_in_channel
curl -s -b jar "$API/connections/$L/channels" | jq -r '[.items[].name] | sort | join(",")'   # alerts,alerts-2
```

## Open questions

None.

## Notes

- Suggested commit: `feat(slack): add slack destinations and their channel check`.
- A channel id is the one input that decides where Muster posts; the pattern refuses a user's or a direct chat's id,
  to which Slack would deliver a message as a direct message (F-096). Private channels are `C…` today, `G…` on older
  workspaces.
- The channel list reads at most five pages, because `conversations.list` has the low rate limit of tier 2 (F-099); a
  workspace with more channels than that still works through the id field.
- The live block saves Destinations on the maintainer's test workspace and posts nothing; delete them afterwards, or
  keep `live-alerts` for S-099.
- Groups are refused because the bot's scopes cannot list Slack's user groups or check an id typed by hand; adding
  them later needs the scope `usergroups:read`, a decision for the maintainer.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-24.FR-3 | partial | on save, from "Check", the channel list and `muster doctor`; the probe is S-099, the form S-103 |
| C-24.FR-6 | partial | the Mention settings a Slack Destination takes; rendering them is S-099 and S-102 |
| C-24.AC-3 | partial | the Destination check and the channel list; the socket is S-101 |
| C-24.AC-4 | partial | the API, the list over several pages included; the form's list is S-103 |
| C-24.AC-15 | partial | `all` and groups refused; `<!here>` and unlinked names are S-099, linked Mentions S-102 |
| C-12.FR-8 | partial | Slack's choices |
| C-02.FR-14 | partial | Slack Connections and Destinations in `muster doctor` |
