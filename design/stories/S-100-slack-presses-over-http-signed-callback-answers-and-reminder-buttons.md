---
id: S-100
title: "Slack presses over HTTP: the signed callback, presses and their attribution, answers to people and Reminder buttons (BE)"
capability: C-24
kind: be
layer: L1
depends_on: [S-099]
covers: [C-24.FR-5, C-24.FR-9, C-24.FR-10, C-24.FR-11, C-24.FR-13, C-24.FR-14, C-24.FR-17, C-24.AC-11, C-24.AC-12, C-24.AC-13, C-24.AC-16, C-24.AC-17, C-24.AC-19, C-10.FR-3, C-18.FR-8, C-23.FR-1, C-23.FR-2, C-23.FR-3]
files_touched:
  - api/openapi.yaml
  - internal/db/migrations/0018_slack_presses_and_links.up.sql
  - internal/db/migrations/0018_slack_presses_and_links.down.sql
  - design/db/schema.md
  - design/architecture.md
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - internal/slack/callback.go
  - internal/slack/callback_test.go
  - internal/slack/presses.go
  - internal/slack/presses_test.go
  - internal/slack/answers.go
  - internal/slack/answers_test.go
  - internal/slack/client.go
  - internal/connections/slack.go
  - internal/connections/slack_test.go
  - internal/connections/query.sql
  - internal/accountlinks/lookup.go
  - internal/accountlinks/query.sql
  - internal/delivery/presses.go
  - internal/delivery/presses_test.go
  - internal/delivery/interactive.go
  - internal/audit/audit.go
  - internal/metrics/catalogue.go
  - design/prd/l1/reference.md
  - internal/messages/texts/en.json
  - internal/messages/texts/ru.json
  - internal/requestlog/inbound.go
  - internal/requestlog/inbound_test.go
  - internal/requestlog/mask.go
  - internal/requestlog/mask_test.go
  - internal/runtime/runtime.go
  - internal/fakes/fakeslack/presses.go
  - internal/fakes/fakeslack/fakeslack.go
  - internal/fakes/fakeslack/fakeslack_test.go
  - internal/logging/events.go
  - internal/archlint/secretleak.go
  - test/e2e/slack_test.go
  - web/src/routes/admin.audit-log.tsx
  - web/src/components/account-links-table.tsx
  - web/src/locales/en.json
  - web/src/locales/ru.json
acceptance:
  - "[C-24.FR-9, C-24.AC-11] `slackInteraction` at `MUSTER_INGEST_URL/api/v1/callbacks/slack/{connection_id}` verifies `X-Slack-Signature` — `v0=` and the hex HMAC-SHA256 of `v0:<timestamp>:<raw body>` with the Connection's Slack signing secret, in constant time — and refuses an `X-Slack-Request-Timestamp` further than `slack.request_tolerance` from the real clock; a correct request is answered `200` with an empty body before its press is handled (the answer's time is under 100 ms while the Command takes longer, `callback_test.go`); a wrong signature, a timestamp 6 minutes old, an unknown Connection, a deleted one and a Connection in `socket` mode are each answered `401`, change nothing and log `slack_press_rejected` with the reason; a body without `payload` is answered `400`; Slack's example vector of F-104 passes the verification (`callback_test.go`)."
  - "[C-24.FR-10, C-24.AC-12, C-18.FR-8] A `block_actions` press from an account linked to a Responder (the link row set up directly; S-102 makes links) runs its Command with the Transport `slack`, recorded in the Timeline and the Audit log, and is not answered; the option \"4h\" of the Snooze menu snoozes for 4 hours and the option of the `tomorrow` preset until the end of C-10.FR-17 in the User's profile time zone; a press whose `value` is tampered with, or whose `container.message_ts` or `container.channel_id` is not the message the value was issued for, changes nothing and is answered \"This button could not be verified; nothing was changed.\"; a press from an account without a link changes nothing and is answered \"Your Slack account is not linked to Muster. Link it in your profile: {link}\"; a Viewer's and a disabled User's press get their refusals; another `type` of interaction is answered `200` and ignored; each press logs `slack_press` with its outcome."
  - "[C-24.FR-5, C-24.FR-10] A press of \"Still on it\" on a Reminder answers it and one of \"Unack\" unacknowledges the Alert Group; a press on a Reminder whose button was signed by a key no longer in the Keyring is answered that the button has expired, as in Mattermost and Telegram; a replica handles at most `slack.parallel_presses` presses at a time and the others wait, already answered `200` (`presses_test.go`)."
  - "[C-24.FR-11, C-24.AC-13] A refusal reaches the person through `chat.postEphemeral` with the press's channel and `user`, without `thread_ts` for a press on a Root message and with the Root message's `ts` for a press on a Reminder, through the interactive path within `delivery.interactive_budget`; with the fake failing `chat.postEphemeral` (`user_not_in_channel`), the same text is posted to the press's `response_url` with `response_type` `ephemeral` and `replace_original` `false`; a `response_url` that is not `https` on `hooks.slack.com` is never called outside `muster dev`; no request carries `replace_original` `true`; answers are in the linked User's profile language and in English without a link (`answers_test.go`, a Russian-speaking User)."
  - "[C-24.FR-13, C-24.AC-16] A press on a test message is answered \"This is a test message; nothing was changed\" and changes nothing."
  - "[C-24.FR-14, C-24.AC-17, C-23.FR-1, C-23.FR-2, C-23.FR-3] The Request log of the Connection records each request to the callback as `inbound` with the operation `interactive` and Muster's answer, refused ones included, and each answer as `chat.postEphemeral` or `response_url`; the value of `X-Slack-Signature`, the `response_url` — in the form body as it arrived, encoded, and in the request to it — and the signed action ids are `[redacted]` (`mask_test.go`, `inbound_test.go`)."
  - "[C-24.FR-17, C-10.FR-3] `muster_commands_total` counts Commands with `transport=\"slack\"`, which the metric catalogue lists; `Transport` and `Messenger` in the API spec, `internal/audit` and the database accept `slack`; the log events `slack_press` and `slack_press_rejected` are in the registry and its reference page."
  - "[C-24.AC-19] On the test workspace, with the Connection in `http` mode and the app's Interactivity Request URL set to a public address of the ingest listener of `muster dev`, a person's press of Ack on a Root message in `#alerts`, from a Slack account linked to a Responder, acknowledges the Alert Group as that User, and a press from an account without a link is answered privately with the \"not linked\" text."
verify: "make ci test-integration e2e-go"
operator_attention: true
issue: null
---

# S-100. Slack presses over HTTP: the signed callback, presses and their attribution, answers to people and Reminder buttons (BE)

## Scope

**IN**

- The callback of the `http` event mode with Slack's signature, answered at once.
- Presses: the `block_actions` payload, the signed action id and its message, the Account link of the person, the
  Command with the Transport `slack`, the Snooze menu's options and Reminder buttons; shared with Socket Mode (S-101).
- Answers to people with `chat.postEphemeral` and the fallback to `response_url`.
- The Transport `slack` and the messenger `slack` in the schema and the API; the Request log of the callback.
- Presses in the fake Slack server, sent to a Request URL and signed.

**OUT**

- Socket Mode, its envelopes and their Request log (S-101).
- Making Account links, Mentions of linked people and the footer's names (S-102).
- The Connection page with the callback address and its hint, and the Timeline's name of the Transport (S-103).

## Contracts

- **Spec** (`api/openapi.yaml`): the operation `slackInteraction` (`POST /callbacks/slack/{connection_id}`,
  `x-listener: ingest`, `x-permission: none`, `security: []`), its body `application/x-www-form-urlencoded` with
  `payload`, the headers `X-Slack-Signature` and `X-Slack-Request-Timestamp`, the answers `200` (empty), `400` and
  `401`, and the description of C-24.FR-9 — request validation does not answer here, the handler decides; `Transport`
  and `Messenger` gain `slack`.
- **Schema** (migration `0018_slack_presses_and_links`, expand only, `schema.md`): `slack` in the transport CHECKs of
  `timeline_entries` and `audit_log` — added `NOT VALID` to the partitioned parents, validated in a second transaction
  and the old ones dropped in a third, as `0011` did — and in the messenger CHECKs of `account_links` and
  `account_link_requests`, whose identity space is already `<messenger>:<connection_id>`; a Slack request requires its
  `target_external_id`, as a Mattermost one does, and `account_link_requests_slack_target_idx` backs the per-target
  limit of `account_link.slack_code_requests` for S-102. An earlier binary writes no Slack row.
- **Callback** (`internal/slack/callback.go`): the raw body read up to 1 MiB before the form is parsed, the signature
  and the tolerance on the real clock (S-020's clock table), the Connection looked up by `public_id` together with its
  mode and Slack signing secret; every refusal answers the same `401` and logs `slack_press_rejected` with the reason
  (`bad_signature`, `stale`, `unknown_connection`, `wrong_mode`); a valid press is handed to `Presses` with a context of
  the process, not of the request, and the `200` written at once. Mounted in `runtime.Callbacks` under the Request log's
  inbound recorder with the operation `interactive`.
- **Presses** (`internal/slack/presses.go`): `Handle(ctx, conn, payload)` — the one path for both event modes —
  parses `block_actions`, takes the value of the first action (`value`, or `selected_option.value` for the menu),
  verifies it with the key it names (`internal/buttons`), binds it to the Root message or Thread reply of a Destination
  of this Connection with the press's `container.channel_id` and `container.message_ts` (`internal/delivery/presses.go`,
  a lookup by channel and `ts`), maps `user.id` through `accountlinks.Lookup` for this Connection, and dispatches the
  Command with the Transport `slack`, as the Mattermost callback does; a semaphore of `slack.parallel_presses` per
  replica bounds the presses in flight.
- **Answers** (`internal/slack/answers.go`): the texts of
  [reference.md](../prd/l1/reference.md#banners-warnings-and-notices) in the person's language, sent by
  `chat.postEphemeral` through `delivery.Interactive` on the Connection's limiter, and on any failure to the press's
  `response_url` with `{"response_type":"ephemeral","replace_original":false,"text":…}` in the interactive class, only
  to `https://hooks.slack.com/` — or to the fake server under `muster dev`; the `response_url` is registered as a secret
  of that request.
- **Request log** (`internal/requestlog`): the inbound operation `interactive`; masking by shape of
  `X-Slack-Signature` values and of `response_url` addresses, plain and percent-encoded, beside the signed action ids
  that are already masked; outbound answers recorded as `chat.postEphemeral` and `response_url`.
- **Log events**: `slack_press` (info: the Connection, the Alert Group and the Command once verified, the outcome —
  `done`, `unchanged`, `refused`, `forbidden`, `not_linked`, `disabled`, `not_verified`, `expired`, `test`, `ignored`,
  `failed` — how it was answered, `ephemeral` or `response_url`, and the masked error) and `slack_press_rejected`
  (warn: the Connection when known, the reason).
- **Metrics**: `slack` in the closed `transport` set of `muster_commands_total` (`internal/metrics/catalogue.go`) and
  in the metric catalogue of reference.md, which `make generate` checks against the registry (C-24.FR-17).
- **Fake Slack server** (`internal/fakes/fakeslack/presses.go`): `/_fake/press` takes a channel, a message `ts`, an
  action (`ack`, `unack`, `resolve`, `unsnooze`, `still_on_it`, or `snooze` with an option index), a user id and, for
  the negative cases, a wrong signature, a timestamp offset or another message's `ts`; it builds the payload of F-093
  from the stored message — `response_url` pointing at its own `/actions/<id>` — and, for an app whose `request_url` is
  set in `/_fake/config`, posts it signed with the app's Slack signing secret, recording Muster's status;
  `/_fake/ephemeral` and `/_fake/responses` list what Muster answered; users `U0FAKEALICE`, `U0FAKEBOB` and
  `U0FAKECAROL` are members of both apps' channels; `/_fake/press` takes the app (`b` by default) and, with `transport`
  `http`, posts to the app's `request_url` even while the app has a socket.
- **SPA type check** (`admin.audit-log.tsx`, `account-links-table.tsx`, the locales): `Transport` and `Messenger`
  gain `slack`, and the switches over them end in `unreachable`, so each gets a `slack` branch named "Slack" from a
  locale key; the pages of Slack come with S-103.
- **Architecture** (`design/architecture.md`): the flow of a Slack press over HTTP beside those of Mattermost and
  Telegram.
- **Secrets** (lint 5): the probe `slack_presses` sends presses with a wrong signature and a failing answer and checks
  that the Slack signing secret and the `response_url` reach no log line.
- **Defaults**: `slack.request_tolerance`, `slack.parallel_presses`, `delivery.interactive_budget`.

## Steps

1. Add the migration and the enums. Check: `go test -tags integration ./internal/db/...`, `make generate-check`.
2. Write the callback with the signature. Check: `callback_test.go` with F-104's vector, a wrong body, a stale and a
   future timestamp, each refusal answered alike.
3. Write the presses and the answers on the shared path, with the binding by channel and `ts`. Check:
   `presses_test.go`, `answers_test.go`, `go test ./internal/delivery/...`.
4. Mask and record, register the log events and the metric value, and extend the fake. Check: `mask_test.go`,
   `inbound_test.go`, `fakeslack_test.go`, `make e2e-go`, then Verification.

## Verification

```sh
make dev > dev.log 2>&1 &
# as in S-099: the session, FAM, NOTIFY, ADV, AG, FIRE and the Route "sl" with the Destination "alerts" (D) of the
# Connection "sl" (C); the Responder "alice" and the Viewer "vic", created with `createUser` as in S-011
API=localhost:8080/api/v1; FS=127.0.0.1:18095/_fake; ING=localhost:8081/api/v1
ETAG() { curl -s -b jar $API/connections/$C | jq -r .etag; }
curl -s "${H[@]}" -X PUT -H "If-Match: $(ETAG)" $API/connections/$C -d '{"type":"slack","name":"sl","event_mode":"http",
  "slack_signing_secret":"muster-dev-slack-signing-secret-b","proxy":{"enabled":false},"limiter":{"limit":50,"per_seconds":60}}' > /dev/null
curl -s -X PUT $FS/config -d "{\"request_url\":{\"b\":\"http://localhost:8081/api/v1/callbacks/slack/$C\"}}" > /dev/null
# Account links set up directly (S-102 makes them through the profile)
LINK() { psql "$MUSTER_DATABASE_URL" -qc "INSERT INTO account_links (org_id, public_id, user_id, messenger, connection_id,
  external_id, username, display_name, created_at) SELECT 1, '$4', u.id, 'slack', c.id, '$2', '$3', u.name, now()
  FROM users u, connections c WHERE u.login = '$1' AND c.public_id = '${5:-$C}'"; }
LINK alice U0FAKEALICE alice AK0000000000S1; LINK vic U0FAKEBOB bob AK0000000000S2
PRESS() { curl -s -X POST $FS/press -d "$1" | jq -c '{status}'; }
FIRE p1 DiskFull q1; G=$(AG 'pod%3D%22q1%22'); TS=$(curl -s "$FS/messages?channel=C0FAKEALERTS" | jq -r '[.[] | select(.text | contains("DiskFull"))][0].ts')

# C-24.FR-9, AC-11: a press over HTTP, answered at once, run as Alice
PRESS "{\"channel\":\"C0FAKEALERTS\",\"ts\":\"$TS\",\"action\":\"ack\",\"user\":\"U0FAKEALICE\"}"   # {"status":200}
sleep 1; curl -s -b jar $API/alert-groups/$G | jq -c '{status, owner: .owner.login}'             # {"status":"acknowledged","owner":"alice"}
curl -s -b jar "$API/alert-groups/$G/timeline" | jq -r '[.items[] | select(.kind == "acknowledged")][0].transport'   # slack
curl -s localhost:8082/metrics | grep -c 'muster_commands_total{command="acknowledge",outcome="done",transport="slack"}'   # 1
# refusals: a wrong signature, a stale timestamp, an unknown Connection, a Connection in socket mode
PRESS "{\"channel\":\"C0FAKEALERTS\",\"ts\":\"$TS\",\"action\":\"resolve\",\"user\":\"U0FAKEALICE\",\"signature\":\"wrong\"}"   # {"status":401}
PRESS "{\"channel\":\"C0FAKEALERTS\",\"ts\":\"$TS\",\"action\":\"resolve\",\"user\":\"U0FAKEALICE\",\"timestamp_offset_seconds\":-360}"   # {"status":401}
curl -s -X POST $ING/callbacks/slack/CN0000000000ZZ -o /dev/null -w '%{http_code}\n'    # 401
DS=$(curl -s -b jar $API/connections | jq -r '.items[] | select(.name == "Dev Slack") | .id')   # in socket mode
curl -s -X PUT $FS/config -d "{\"request_url\":{\"a\":\"http://localhost:8081/api/v1/callbacks/slack/$DS\"}}" > /dev/null
curl -s -X POST $FS/press -d '{"app":"a","channel":"C0FAKEALERTS","ts":"1.000001","action":"ack","user":"U0FAKEALICE","transport":"http"}' | jq -c '{status}'   # {"status":401}
grep -c '"event":"slack_press_rejected"' dev.log                                  # 4 or more
curl -s -b jar $API/alert-groups/$G | jq -r .status                               # acknowledged

# C-24.FR-10, FR-11, AC-12, AC-13: the menu, a Viewer, an unlinked account, a value of another message
PRESS "{\"channel\":\"C0FAKEALERTS\",\"ts\":\"$TS\",\"action\":\"snooze\",\"option\":1,\"user\":\"U0FAKEALICE\"}" > /dev/null; sleep 1
curl -s -b jar $API/alert-groups/$G | jq -r '.status'                              # snoozed
PRESS "{\"channel\":\"C0FAKEALERTS\",\"ts\":\"$TS\",\"action\":\"resolve\",\"user\":\"U0FAKEBOB\"}" > /dev/null
PRESS "{\"channel\":\"C0FAKEALERTS\",\"ts\":\"$TS\",\"action\":\"resolve\",\"user\":\"U0FAKECAROL\"}" > /dev/null
PRESS "{\"channel\":\"C0FAKEALERTS\",\"ts\":\"$TS\",\"action\":\"resolve\",\"user\":\"U0FAKEALICE\",\"value_of_ts\":\"1.000001\"}" > /dev/null; sleep 1
curl -s $FS/ephemeral | jq -r '.[-3:][] | "\(.user) \(.thread_ts // "-") \(.text)"'
# U0FAKEBOB - You are not permitted to do this
# U0FAKECAROL - Your Slack account is not linked to Muster. Link it in your profile: http://localhost:8080/profile
# U0FAKEALICE - This button could not be verified; nothing was changed.
curl -s -b jar $API/alert-groups/$G | jq -r .status                               # snoozed
curl -s -X POST $FS/faults -d '{"path":"/api/chat.postEphemeral","status":200,"content_type":"application/json","body":"{\"ok\":false,\"error\":\"user_not_in_channel\"}","times":1}' > /dev/null
PRESS "{\"channel\":\"C0FAKEALERTS\",\"ts\":\"$TS\",\"action\":\"resolve\",\"user\":\"U0FAKECAROL\"}" > /dev/null; sleep 1
curl -s $FS/responses | jq -c '.[-1] | {response_type, replace_original}'           # {"response_type":"ephemeral","replace_original":false}
curl -s $FS/requests | jq '[.[] | select((.body // "") | contains("\"replace_original\":true"))] | length'   # 0

# C-24.FR-13, AC-16: a press on a test message
curl -s "${H[@]}" -X POST $API/destinations/$D/tests -d '{}' > /dev/null
TT=$(curl -s "$FS/messages?channel=C0FAKEALERTS" | jq -r '[.[] | select(.text | contains("Test message"))][-1].ts')
PRESS "{\"channel\":\"C0FAKEALERTS\",\"ts\":\"$TT\",\"action\":\"ack\",\"user\":\"U0FAKEALICE\"}" > /dev/null; sleep 1
curl -s $FS/ephemeral | jq -r '.[-1].text'                                       # This is a test message; nothing was changed

# C-24.FR-14, AC-17: the Request log, masked
curl -s -b jar "$API/request-log?connection=$C&direction=inbound&limit=1" | jq -c '.items[0] | {operation, status}'   # {"operation":"interactive","status":200}
R=$(curl -s -b jar "$API/request-log?connection=$C&direction=inbound&limit=1" | jq -r '.items[0].id')
curl -s -b jar "$API/request-log/$R" | jq -c '.request_headers["X-Slack-Signature"]'   # ["[redacted]"]
curl -s -b jar "$API/request-log?connection=$C&limit=50" | jq -r '.items[].id' | while read -r I; do curl -s -b jar "$API/request-log/$I"; done |
  grep -c 'hooks.slack.com%2Factions\|/_fake/actions/\|muster-dev-slack-signing-secret'   # 0
```

Live, on the maintainer's test workspace, with `operator_attention` — Slack must reach the ingest listener, so the
operator opens a tunnel to `localhost:8081` (any HTTPS tunnel), sets the app's Interactivity Request URL to
`<tunnel>/api/v1/callbacks/slack/<L>` and turns Socket Mode off for this check, and on again afterwards:

```sh
# the Connection "test workspace" (L) and "live-alerts" (LD) as in S-099, switched to http with the real Slack signing secret
curl -s "${H[@]}" -X PUT -H "If-Match: $(curl -s -b jar $API/connections/$L | jq -r .etag)" $API/connections/$L -d "$(jq -n \
  --rawfile s ~/.config/muster-spike/slack-signing-secret '{type:"slack",name:"test workspace",event_mode:"http",
  slack_signing_secret:($s|rtrimstr("\n")),proxy:{enabled:false},limiter:{limit:50,per_seconds:60}}')" > /dev/null
# SLACK_PERSON: the operator's Slack user id, which the maintainer gives the lead (it stays out of the repository)
LINK alice "$SLACK_PERSON" operator AK0000000000S3 $L    # the operator's Slack account, linked to alice
FIRE live2 LiveHttp r1; LG=$(AG 'pod%3D%22r1%22')
# the operator presses Ack on the message in #alerts
until curl -s -b jar $API/alert-groups/$LG | jq -e '.status == "acknowledged"' > /dev/null; do sleep 2; done
curl -s -b jar $API/alert-groups/$LG | jq -c '{status, owner: .owner.login}'      # {"status":"acknowledged","owner":"alice"}
curl -s -b jar "$API/request-log?connection=$L&direction=inbound&limit=1" | jq -c '.items[0] | {operation, status}'   # {"operation":"interactive","status":200}
# the link row removed, the operator presses Resolve and sees, only for them, the "not linked" text with the profile link
```

At the end the Alert Group is resolved, the messages are deleted with the bot token, the link row is removed, and the
operator sets the app back to Socket Mode.

## Open questions

None.

## Notes

- Suggested commit: `feat(slack): receive slack button presses over http and answer them`.
- `operator_attention` is true: the real press needs a person, and the HTTP mode needs a public address and a change of
  the app's settings that only the maintainer makes; the live check of Socket Mode follows in S-101. When no tunnel is
  available, the pull request says so, and the HTTP path stands on F-104's vector and the fake server.
- A press is answered `200` before it is handled because Slack waits 3 seconds (F-093), less than
  `delivery.interactive_budget`; a crash between the answer and the Command loses that press, as a press made while
  Muster is down is lost.
- Muster expects Slack not to send an interaction again (a Pending fact), so presses are not deduplicated; a press that
  did come twice, like a press repeated by a person, runs again and meets its precondition.
- `response_url` is a secret of its own: anyone holding it may post to the channel for 30 minutes (F-093), so it is
  masked everywhere and called only on Slack's host.
- The lookup of a press binds by channel and `ts`, the identity of a Slack message; a value issued for another message
  is refused like a bad signature, as in Mattermost (C-13.FR-4).

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-24.FR-5 | partial | presses on the buttons and the menu, Reminders included; the layout is S-099 |
| C-24.FR-9 | partial | the `http` mode; Socket Mode is S-101 |
| C-24.FR-10 | full | the shared path; Socket Mode hands its envelopes to it in S-101 |
| C-24.FR-11 | full | |
| C-24.FR-13 | partial | the press on a test message; the panel's note is S-103 |
| C-24.FR-14 | partial | the callback and the answers; envelopes S-101 |
| C-24.FR-17 | partial | `slack_press`, `slack_press_rejected` and the `transport` value; the socket's events S-101 |
| C-24.AC-11 | full | |
| C-24.AC-12 | full | |
| C-24.AC-13 | full | |
| C-24.AC-16 | partial | the press on a test message; the panel's note S-103 |
| C-24.AC-17 | partial | the callback and the answers; envelopes S-101 |
| C-24.AC-19 | partial | the `http` mode on the test workspace; Socket Mode S-101 |
| C-10.FR-3 | partial | the Transport `slack` |
| C-18.FR-8 | partial | presses from Slack accounts |
| C-23.FR-1 | partial | Slack's callback |
| C-23.FR-2 | partial | the inbound operation `interactive` |
| C-23.FR-3 | partial | the Slack signing secret's header, `response_url` and the action values of Slack |
