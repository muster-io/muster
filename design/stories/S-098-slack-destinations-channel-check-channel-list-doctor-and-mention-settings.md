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
  - internal/slack/check.go
  - internal/slack/check_test.go
  - internal/slack/channels.go
  - internal/slack/channels_test.go
  - internal/destinations/destinations.go
  - internal/destinations/destinations_test.go
  - internal/destinations/write.go
  - internal/destinations/write_test.go
  - internal/destinations/query.sql
  - internal/destinations/delete.go
  - internal/mentions/mentions.go
  - internal/mentions/mentions_test.go
  - internal/connections/connections.go
  - internal/connections/slack.go
  - internal/connections/slack_test.go
  - internal/connections/doctor.go
  - internal/connections/doctor_test.go
  - internal/connections/live_test.go
  - internal/connections/query.sql
  - internal/doctor/doctor.go
  - internal/doctor/doctor_test.go
  - internal/api/destinations.go
  - internal/api/destinations_test.go
  - internal/api/connections.go
  - internal/api/connections_test.go
  - internal/api/problem.go
  - internal/runtime/runtime.go
  - internal/fakes/fakeslack/fakeslack.go
  - internal/fakes/fakeslack/channels.go
  - internal/fakes/fakeslack/fakeslack_test.go
  - internal/archlint/secretleak.go
  - AGENTS.md
  - CONTRIBUTING.md
  - web/src/components/destination-label.ts
  - web/src/components/destination-label.test.ts
  - web/src/components/destination-health.tsx
  - web/src/routes/destinations.$destinationId.tsx
  - web/src/components/destination-test-panel.tsx
  - web/src/components/channel-picker.tsx
  - web/src/components/connection-check.tsx
  - web/src/components/connection-check.test.tsx
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - design/stories/S-103-slack-connection-and-destination-pages-check-before-saving-and-profile-links.md
acceptance:
  - "[C-24.FR-3, C-24.AC-4] `createDestination` of type `slack` takes a Slack Connection, the channel id, Mention settings and its limiter (`destination.slack.limiter`) and runs the Destination check; a channel the bot is not in is refused with 422 `destination_check_failed` at `/channel_id`, the `check_code` `bot_not_in_channel` and \"Invite the bot to the channel: /invite @{bot}\" with the bot's name; an archived channel with `channel_archived`, an unknown one with `channel_not_found`; an id that is not `C…` or `G…`, such as `D…` or `U…`, with 422 `invalid_format` at `/channel_id` by the domain before any request; a Connection of another type with 422 at `/connection_id`; a passing check stores `channel_name`, which `getDestination` and `listDestinations` return with `connection_id` and `channel_id`."
  - "[C-24.FR-3] `checkDestination` of a Slack Destination calls `conversations.info` through the interactive path and returns one item `channel`, whose code is `token_invalid` for a refused token, `missing_scope` with the scope named, `channel_not_found`, `channel_archived` or `bot_not_in_channel`, the code of `delivery.FailureCode` for a request without an answer or one the outbound address policy refuses, and `request_failed` with Slack's `error` otherwise (`channels_test.go`, a case per answer); a success on a Broken Destination ends the Broken state."
  - "[C-24.FR-3] `listConnectionChannels` of a Slack Connection returns the public and private channels that the bot is a member of and that are not archived, read with `conversations.list` (`types=public_channel,private_channel`, `exclude_archived=true`, `limit=200`) page by page up to 5 pages, with `truncated` true when more were left, filtered by `q` on the name; a `429` on a page is answered `503` with `Retry-After`."
  - "[C-24.FR-6, C-24.AC-15, C-12.FR-8] `mentions.Validate` takes for a Slack Destination nobody, `channel`, `here` and chosen users, and refuses `all` and messenger groups with 422 `unsupported` at their pointer (`mentions_test.go`, a case per kind)."
  - "[C-02.FR-14] `muster doctor` checks every Slack Connection with the steps of its check and every Slack Destination with `conversations.info`, printing an OK line for each and a FAIL line naming a failing one; the skip of S-097 is gone."
  - "[C-24.AC-3] With an HTTP proxy on the Connection, every request of the Destination check and of the channel list reaches the fake Slack server only through the fake proxy; with the fake answering after the check stopped waiting, the bot token appears in no log line and no error returned by the API (`secretleak.go` probe `slack_connections`, the Destination check added)."
  - "[C-24.FR-3] With `slack` in the `Destination` union the SPA's type check passes: `destinationPlace` shows a Slack Destination's channel name, else its channel id, with `#` before it, `destinationTypeName` names it \"Slack\", and the Destination page shows its name, Broken banner and Routes but no form, check, test or preview until S-103; `checkCodeText` names the scopes of `missing_scopes` for `missing_scope` (D396)."
  - "[C-24.FR-3] A failed `scopes` step of `checkConnection` and a `channel` item of `checkDestination` with `missing_scope` carry `missing_scopes`, the scopes the bot token lacks, which the SPA names in the reader's language instead of reading the English message (D396; `check_test.go`, `channels_test.go`, `connections_test.go` and `destinations_test.go` of the API, `connection-check.test.tsx`)."
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
- Interim states until later stories: a Slack Destination on a Route ends each delivery Not delivered ("no adapter for
  the destination type slack"), and its Broken probe has no adapter to call, until S-099; `testDestination` and
  `previewDestination` answer `501` until S-099; the Destination page of a Slack Destination shows no form, check, test
  or preview until S-103.

## Contracts

- **Spec** (`api/openapi.yaml`): `DestinationType`, `Destination`, `DestinationInput` and the type filters gain `slack`;
  new schemas `SlackDestinationInput` (`type`, `name`, `connection_id`, `channel_id`, a string without a pattern in the
  spec, so that the domain refuses an id that is not `C…` or `G…` with 422 `invalid_format` instead of request
  validation's `400`, `mentions`, `limiter`) and `SlackDestination` (with `channel_name`), joined to the discriminators;
  `DestinationCheckItem.name` describes `channel` for Slack too; `NullableDestinationCheckCode` gains `missing_scope`;
  `listConnectionChannels` answers for a Slack Connection `SlackChannelList` (`items` of `id`, `name`, `is_private`, and
  `truncated`), its `200` the named `ConnectionChannelList`, a `oneOf` of `MattermostChannelList` and
  `SlackChannelList`, and takes `q`; `MattermostChannelList` takes no other member (`additionalProperties: false`), so
  that an empty Slack list matches one branch of the `oneOf` only; its `team_id`, which a Slack Connection ignores, and
  its `503`, which Slack's `429` on a page also gives, say so; `destination_check_failed` puts `missing_scope` at
  `/connection_id`, as `token_invalid`; `MentionSetting.everyone` and `groups` describe what Slack takes.
- **D396: structured scopes instead of parsing the message**: `ConnectionCheckStep` and `DestinationCheckItem` gain an
  optional array `missing_scopes`, set only with the code `missing_scope` — for the step `scopes` the scopes of
  C-24.FR-1 the bot token lacks, in their order, for the item `channel` those Slack's `needed` names — which clients
  name in the reader's language; `message` keeps naming them in English. A refused save names them in the `detail` of
  its problem only.
- **Operations implemented for the type**: `createDestination`, `updateDestination`, `getDestination`,
  `listDestinations`, `deleteDestination` (`destinations:read` / `:write`), `checkDestination` (`destinations:test`,
  interactive), `listConnectionChannels` (`connections:write`, interactive). `testDestination` and
  `previewDestination` answer `501` `not-implemented` for a Slack Destination until S-099, since this build has no
  Tester for the type.
- **Schema** (migration `0017_slack_destinations`, expand only, `schema.md`): `destinations.type` accepts `slack`;
  new columns `slack_channel_id` and `slack_channel_name`; the type CHECK gains the Slack branch (a Connection and a
  channel id, the other types' columns null); the composite foreign key `(connection_id, type)` already binds a Slack
  Destination to a Slack Connection.
- **Check** (`internal/slack/channels.go`): `CheckChannel` — `conversations.info` with the bot token, through the
  caller's client class: the interactive path for a save and for "Check", the background class for `muster doctor`,
  the delivery class for the probe of S-099; the codes of C-24.FR-3, the bot's name from the Connection — the
  `bot_username` its last check stored, "Invite the bot to the channel." without one — in the text of
  `bot_not_in_channel`, and the scopes of Slack's `needed` with `missing_scope`. `Channels` — the list of C-24.FR-3,
  which honours `429` by answering it to the caller instead of waiting.
- **Destinations** (`internal/destinations`): the type in the validation, the write path, the check on save, the
  Slack checker in `WriterConfig` (`delete.go`) and the dispatch of checks; the delete path takes the type unchanged;
  the channel id kept as entered, upper case.
- **Mentions** (`internal/mentions/mentions.go`): the rule of C-24.FR-6 in `Validate`; rendering Slack Mentions is
  S-099 and S-102.
- **Doctor** (`internal/connections/doctor.go`): Slack Connections and Destinations as the acceptance says, in the
  doctor's own client class, as for the other types. `muster dev doctor` (`internal/doctor/doctor.go`) sends a Slack
  Connection's requests where `muster dev` sends them (`devmode.SlackRoute`), so the demo Connection's token never goes
  to `slack.com`.
- **SPA type check** (`destination-label.ts`, `destination-health.tsx`, `destinations.$destinationId.tsx`,
  `connection-check.tsx`, `destination-test-panel.tsx`, whose `testHint` switches over the type, and
  `channel-picker.tsx`, which reads `listConnectionChannels` as Mattermost's list and narrows the new `oneOf` to it):
  explicit `slack` branches; the name "Slack" from `destinations.types.slack`; the Destination page of a Slack
  Destination shows no Test and Preview. `checkCodeText` names the scopes of `missing_scopes` with the existing text
  `checks.codes.missingScope` ("The bot token lacks the scopes {scopes}.") for both checks, and
  `checks.codes.missingScopeUnnamed` without them (D396), so one text serves both and no new code text is needed.
- **Fake Slack server**: channels per app in `/_fake/config` (`id`, `name`, `member`, `archived`, `private`), merged by
  id, so one call changes one channel, with `C0FAKEALERTS` "alerts" and `C0FAKEOTHER` "other" for both apps, the bot a
  member of "alerts" only; `/api/conversations.info` and `/api/conversations.list` with `next_cursor` paging, `limit`
  and a `page_size` in `/_fake/config` that forces small pages, answering as F-098 says; the channels' existence and
  membership, which S-099's `chat.*` methods read to answer `not_in_channel` and `channel_not_found`.
- **Secrets** (lint 5): the probe `slack_connections` gains the Destination check and the channel list.
- **Defaults**: `destination.slack.limiter` (P-61), `destination.mentions`; `defaults.md` does not change: P-61 shows
  as the spec's description of `limiter`, and the form that proposes it is S-103's.

## Steps

1. Add the migration, the type, the channels of the fake server, the check and the Mention rule. Check:
   `fakeslack_test.go`, `channels_test.go`, `check_test.go`, `mentions_test.go`, `write_test.go`,
   `go test -tags integration ./internal/db/...`.
2. Add the channel list and `muster doctor`. Check: `slack_test.go`, `doctor_test.go` of `internal/connections` and of
   `internal/doctor`.
3. Narrow the SPA. Check: `make lint`, `make test-web`.
4. Extend the secret probe. Check: `make lint-arch`, then Verification.

## Verification

```sh
make dev > dev.log 2>&1 &
# the Admin's session (`jar`, `H`) as in S-011; the Connection "sl" (C) as in S-097, in `socket` mode, no proxy, and
# checked, so that it knows its bot's name
API=localhost:8080/api/v1; FS=127.0.0.1:18097/_fake
M='{"new_alert_group":{"everyone":"none","user_ids":[],"groups":[]},"new_alerts":{"everyone":"none","user_ids":[],"groups":[]},
"reopen":{"everyone":"none","user_ids":[],"groups":[]},"ack_timeout":{"everyone":"none","user_ids":[],"groups":[]},
"snooze_ended":{"everyone":"none","user_ids":[],"groups":[]},"rise_to_urgent":{"everyone":"none","user_ids":[],"groups":[]}}'
MKD() { curl -s "${H[@]}" $API/destinations -d "{\"type\":\"slack\",\"name\":\"$1\",\"connection_id\":\"$2\",\"channel_id\":\"$3\",
  \"mentions\":${4:-$M},\"limiter\":{\"limit\":30,\"per_seconds\":60}}"; }

# C-24.FR-3, AC-4: Destinations
curl -s -X DELETE $FS/requests
MKD other $C C0FAKEOTHER | jq -c '{status, code: .errors[0].code, p: .errors[0].pointer, d: .errors[0].detail, cc: .errors[0].check_code}'
# {"status":422,"code":"destination_check_failed","p":"/channel_id","d":"Invite the bot to the channel: /invite @muster-dev-bot-b","cc":"bot_not_in_channel"}
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

# D396: the scopes a check misses, as data
curl -s -X PUT $FS/config -d '{"scopes":{"b":"chat:write,channels:read,groups:read,im:write,users:read"}}' > /dev/null
curl -s "${H[@]}" -X POST $API/connections/$C/checks | jq -c '.steps[1] | [.code, .missing_scopes]'
# ["missing_scope",["users:read.email"]]
curl -s -X PUT $FS/config -d '{"scopes":{"b":"chat:write,groups:read,im:write,users:read,users:read.email"}}' > /dev/null
curl -s "${H[@]}" -X POST $API/destinations/$D/checks | jq -c '.checks[0] | [.code, .missing_scopes]'
# ["missing_scope",["channels:read"]]
curl -s -X PUT $FS/config -d '{"scopes":{"b":null}}' > /dev/null

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
- A Connection that was never checked has no bot name: the text of `bot_not_in_channel` is then "Invite the bot to the
  channel." without a handle, and no extra `auth.test` is made for it. A lower-case channel id is refused with
  `invalid_format`, not folded: the id is kept as entered, and Slack's ids are upper case.
- A `429` on a page of the channel list holds the Connection's limiter, as every `429` does, and is answered `503` with
  the interactive path's detail and Slack's `Retry-After`; on a save or "Check" it is `request_failed`, as in the
  Connection check.
- The problem of a refused save does not carry `missing_scopes`: its `detail` names the scopes in English. S-103 adds
  the field to the problem's items if the form needs it.
- Changes to this contract, each with its reason:
  - `testDestination` and `previewDestination` answer `501`, not `409` as this contract first said: the spec has no
    `409` on them, and `501` is how an operation not yet implemented for a type answers, as for Bridge in S-080.
  - `listConnectionChannels` needs `connections:write` and `checkDestination` `destinations:test`, as the spec and
    `api/README.md` say; this contract first named `connections:read` and `destinations:write`.
  - The text of `bot_not_in_channel` in Verification reads `/invite @muster-dev-bot-b`, not `@Fake bot B`: the fake's
    `auth.test` answers the handle `muster-dev-bot-b`, as Slack answers a handle without spaces, and that is what the
    Connection check stores; S-103's Verification changes with it.
  - `MattermostChannelList` gains `additionalProperties: false`: without it `{"items":[],"truncated":false}` matches
    both branches of the `oneOf`, which the API tests' response validation refuses.
  - D396 replaces the proposed text "The bot token lacks a scope this check needs: {scope}." with the field
    `missing_scopes` and the existing text of `missing_scope`, so that the SPA never parses an English message.
  - `chatKey` gains a Slack branch (its Connection's workspace and the channel id), so that two Slack Destinations of
    one channel get the duplicate-chat warning of S-070 now that the API can create them (review).
  - `files_touched` adds `internal/slack/check.go` and `check_test.go` (D396), `internal/connections/connections.go`
    (the field of D396 on a step, and the messenger a `MessengerError` names), `internal/connections/live_test.go`
    (the Slack queries on PostgreSQL 14 and 17), `internal/doctor/doctor.go` (the route of `muster dev doctor`),
    `internal/api/problem.go` (a refused channel list names Slack, not Mattermost), `connection-check.test.tsx` and
    `destination-label.test.ts` (evidence for the SPA's statements), `AGENTS.md` and `CONTRIBUTING.md` (the fake Slack
    server's `/_fake/config` gains channels and a page size) and S-103's story file (its Verification names the bot as
    the fake does, and its Contracts say how the SPA shows `missing_scopes`). It drops `internal/destinations/test.go`,
    since `501` already comes from `ErrNoTester`, and `internal/connections/connections_test.go`, since the doctor's
    Slack stubs live in `doctor_test.go` and the tests of the channel list in `slack_test.go`.
    `internal/destinations/delete.go` stays: it holds `WriterConfig`, which gains the Slack checker.
- With 51 files the story is at the edge of the size signal; the SPA's narrowing cannot be split off, because the spec
  change breaks the TypeScript type check until it lands, and three of the files are documents whose only change is
  the fake Slack server's configuration or the bot's name.

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
