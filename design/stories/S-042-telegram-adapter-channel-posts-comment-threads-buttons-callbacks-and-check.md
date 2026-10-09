---
id: S-042
title: "Telegram Destinations, the Destination check, Root message posts and edits (BE)"
capability: C-14
kind: be
layer: L1
depends_on: [S-041]
covers: [C-14.FR-2, C-14.FR-6, C-14.FR-7, C-14.FR-9, C-14.FR-12, C-14.FR-13, C-14.FR-14, C-14.FR-15, C-14.FR-16, C-14.AC-7, C-14.AC-8, C-14.AC-12, C-14.AC-13, C-14.AC-14, C-14.AC-17, C-11.FR-7, C-11.FR-8, C-11.FR-9, C-11.FR-18, C-12.FR-1, C-12.FR-7, C-12.FR-8, C-12.FR-11, C-10.FR-6, C-02.FR-14, C-01.FR-13]
files_touched:
  - design/facts.md
  - design/prd/L1.md
  - design/prd/l1/C-14-telegram.md
  - docs/messengers/telegram.md
  - internal/api/destinations.go
  - internal/api/destinations_test.go
  - internal/archlint/secretleak.go
  - internal/connections/connections.go
  - internal/connections/connections_test.go
  - internal/connections/doctor.go
  - internal/connections/doctor_test.go
  - internal/connections/live_test.go
  - internal/connections/query.sql
  - internal/connections/telegram.go
  - internal/connections/telegram_test.go
  - internal/destinations/query.sql
  - internal/destinations/write.go
  - internal/destinations/write_test.go
  - internal/doctor/doctor.go
  - internal/doctor/doctor_integration_test.go
  - internal/doctor/doctor_test.go
  - internal/fakes/faketelegram/chats.go
  - internal/fakes/faketelegram/faketelegram.go
  - internal/fakes/faketelegram/faketelegram_test.go
  - internal/logging/events.go
  - internal/runtime/runtime.go
  - internal/runtime/runtime_test.go
  - internal/telegram/adapter.go
  - internal/telegram/adapter_test.go
  - internal/telegram/classify.go
  - internal/telegram/client.go
  - internal/telegram/destcheck.go
  - internal/telegram/destcheck_test.go
  - internal/telegram/layout.go
  - internal/telegram/layout_test.go
  - test/e2e/harness.go
  - test/e2e/telegram_test.go
acceptance:
  - "[C-14.FR-2, C-14.FR-14, C-14.AC-13, C-11.FR-18] `createDestination` of type `telegram` takes only the Connection and the channel; the Destination check finds the discussion group through `getChat` (`linked_chat_id`) and the Destination shows it read-only; a channel without a linked group is refused with \"Comments are not enabled for this channel. Enable comments in the channel settings in Telegram; this creates its discussion group.\", and a group where the bot is not an admin with \"The bot is not an admin of the discussion group {group}. Make the bot an admin there, allowed to post messages.\""
  - "[C-14.AC-17, C-14.FR-16, C-14.FR-13, C-12.FR-1, C-12.FR-7] A new Alert Group creates a channel post sent with `parse_mode` HTML; its label sections are inside `<blockquote expandable>`, its Alerts are lines of a list, it contains no table, and alert data are HTML-escaped."
  - "[C-14.AC-8, C-12.FR-7, C-12.FR-11] A label value `@channel <b>x</b>` arrives as literal text; an Alert Group with 25 Alerts fits in 4,096 characters with its title, status, footer, buttons and the link to Muster."
  - "[C-14.AC-14, C-14.FR-15, C-10.FR-6] The Root message carries one button per action of its status, with one Snooze button per Route Snooze duration; after Acknowledge the edit of the Root message carries the keyboard of the acknowledged status, and the fake server's message keeps its buttons through every later edit."
  - "[C-14.FR-6, C-11.FR-7, C-12.FR-8] A Quiet new Root message carries `disable_notification: true` and a Loud one does not; a Loud Root message renders its Mentions as `tg://user` links for Users with a Telegram Account link and as display names otherwise."
  - "[C-14.FR-7, C-11.FR-8] Response mapping: `429` with `retry_after` waits exactly for the Destination, and for the whole Connection when a second Destination of it gets a `429` within 60 seconds; \"message is not modified\" is success; \"message to edit not found\" and \"message can't be edited\" start the deleted Root message flow, so the Root message is republished; \"can't parse entities\" sends the same text again without markup; `401`, a kicked bot or missing rights make the Destination Broken; `ok: false` in a `200` is classified the same way; a non-JSON answer is transient; anything else ends that delivery as Not delivered."
  - "[C-14.AC-12, C-11.FR-9] With the bot's admin rights removed in the channel and nothing waiting, the probe keeps the Destination Broken with the missing right as the reason; after the rights are restored, the next probe ends the Broken state with no Alert Group activity, its `getChat` and `getChatMember` requests counted under `client=\"delivery\"`."
  - "[C-14.AC-7, C-14.FR-12] A network error on a delivery request leaves the token in no log line, no delivery error in the Timeline and no Destination reason."
  - "[C-02.FR-14] `muster doctor` prints one line per Telegram Destination with the result of its check."
  - "[C-01.FR-13] The fake Telegram server reproduces F-001 to F-003, F-011, F-012, F-016 and F-017 for its channels `@muster_alerts` and `@no_comments` and their discussion group, and `faketelegram_test.go` checks each fact."
  - "[C-14.FR-9] The documentation covers creating the bot, enabling comments, adding the bot as an admin to the channel and to the discussion group, long polling versus webhook, joining the discussion group and muting the channel for on-call people, and the limitation that a Quiet Root message rings members of the discussion group."
  - "Deleting a Telegram Connection in the webhook update mode calls `deleteWebhook` as a best effort: the delete succeeds even when the call fails, and the outcome is logged with a registered log event."
verify: "make ci test-integration e2e"
operator_attention: false
issue: 42
---

# S-042. Telegram Destinations, the Destination check, Root message posts and edits (BE)

## Scope

**IN**

- Telegram Destinations: the channel only, the discussion group found by the Destination check, the check on save, as
  "Check", as the Broken probe and in `muster doctor`.
- The adapter's `Publish` and `Update`: Root messages as HTML channel posts with expandable label sections and inline
  keyboards, edits that always carry the keyboard, Quiet messages, HTML escaping, Mentions and the 4,096-character
  limit, and the response mapping with every error class of C-14.FR-7.
- A plain `Reply` to the discussion group, without `reply_parameters`, so that Thread replies are delivered until
  S-066 attaches them to the post.
- The lint-5 probe of the adapter's error paths.
- The fake Telegram server's channels, discussion group, bot rights, edits, keyboards, `message is not modified`, the
  per-chat limit and the notifications of channel posts.
- The documentation page `docs/messengers/telegram.md`.
- `deleteWebhook` as a best effort when a Telegram Connection in the webhook update mode is deleted.

**OUT**

- Comment Threads: the copy buffer, the copy wait, the unattached chain, the lost Thread and "Thread not attached"
  (S-066).
- Button presses, `answerCallbackQuery`, Account link lookups for presses, `telegram.press_max_age` and the update
  router lock (S-067).
- `/start` for Account links (S-051); Reminders and their buttons on Thread replies (S-049); test messages (S-047);
  the pages (S-043); "Telegram in restricted networks" (S-058).

## Contracts

- **Operations implemented**: `createDestination`, `updateDestination` and `checkDestination` for the type `telegram`
  (the `422 unsupported` at `/type` of S-039 is removed). Schemas: `TelegramDestination(Input)`
  (`discussion_group_id`, `discussion_group_title`, `channel_title`), `DestinationCheckItem` (`channel_exists`,
  `discussion_group`, `bot_rights_channel`, `bot_rights_group`).
- **Telegram Destinations** (C-14.FR-2; `destinations.telegram_*`): `connection_id` (a Telegram Connection),
  `channel_id` (a chat id or `@username`, stored as entered), `mentions` (validated by S-037: `everyone` `none`, no
  groups), `limiter` (default `destination.telegram.limiter`, which counts sends and edits in the channel and in its
  discussion group alike, F-016). Saving runs the Destination check through the interactive path and refuses a failing
  one with `422 destination_check_failed` (S-039); a passing check stores `telegram_channel_chat_id`,
  `telegram_discussion_chat_id` and the two titles. `destinations.Service` declares a Telegram checker next to its
  Mattermost one, which `connections` implements with the Connection's client.
- **Destination check** (C-14.FR-14; `destcheck.go`): `getChat` on the channel (`channel_exists`: it exists and is a
  channel); its `linked_chat_id` (`discussion_group`: missing → the "Comments are not enabled …" text of
  [reference.md](../prd/l1/reference.md#banners-warnings-and-notices), F-002); `getChatMember` for the bot in the
  channel (`bot_rights_channel`: an admin allowed to post and edit messages; otherwise "The bot is not an admin of the
  channel.", "The bot may not post messages in the channel." or "The bot may not edit messages in the channel.",
  F-001); `getChatMember` for the bot in the group (`bot_rights_group`: an admin allowed to post; a `403` "bot is not a
  member" or a plain member → the "The bot is not an admin of the discussion group {group} …" text, F-003). As the
  probe it runs in the delivery class; on save and from `checkDestination` in the interactive class; success on a
  Broken Destination calls `MarkHealthy` of S-035. `muster doctor` prints `destination <name>: ok` or the failing check.
- **Layout** (C-12.FR-1, C-14.FR-16, FR-13; `layout.go`): `sendMessage` to the channel with `parse_mode` `HTML`: the
  status emoji and `<b><a href="{Alert Group page}">#N title</a></b>`; the environment and start; the group labels,
  common labels and common annotations inside one `<blockquote expandable>`; the `summary` in `<i>`; the Alerts as one
  line each (resolved ones in `<s>`), never a table; the links line starting with "Open in Muster"; the notices; the
  footer. Alert data are HTML-escaped (`&`, `<`, `>`) with `@` neutralized (S-036); trusted Mention tokens become
  `<a href="tg://user?id={id}">{name}</a>` for users with a Telegram Account link and the display name otherwise.
  `LengthLimit()` is 4,096 characters. Rich Messages are not used (F-019).
- **Keyboard** (C-14.FR-4, FR-15): an inline keyboard with one button per action of the status — Ack and Resolve on one
  row, one Snooze button per `route.snooze_durations` on the next — whose `callback_data` is the compact action id of
  S-036 (at most 64 bytes). Every edit (`editMessageText`) carries the whole keyboard of the new state; a resolved Root
  message carries an empty keyboard on purpose (F-011). Verifying the data of a press is S-067.
- **Quiet and Loud** (C-14.FR-6, C-11.FR-7): a Quiet new message carries `disable_notification: true` (F-012); an edit
  is always Quiet.
- **Channel** (C-14.FR-2): the adapter sends only Root messages and Storm summaries to the channel (test messages come
  with S-047) and never replies inside it (F-015). Until S-066, `Reply` sends the reply to the discussion group
  (`telegram_discussion_chat_id`) as a plain message without `reply_parameters` and leaves `thread_state` alone; S-066
  replaces it with replies to the automatic copy.
- **Response mapping** (C-14.FR-7, C-11.FR-8; `classify.go`): `429` with `parameters.retry_after` → `retry_after` for
  the Destination, and for the whole Connection when a second Destination of the same Connection gets a `429` within 60
  seconds; "message is not modified" → `ok` (F-017); "message to edit not found" and "message can't be edited" →
  `gone`, so that the Root message is republished as in the deleted Root message flow of S-035 (D290; the bot can edit
  its own messages without a time limit, so that answer means the message cannot be edited at all); "message to be
  replied not found" on a reply → `thread_lost`, which S-066 handles; "can't parse entities" → `markup_rejected`
  (resent without `parse_mode`); `401`, "bot was kicked", "not enough rights", "chat not found", "need administrator
  rights" → `fatal`; a non-JSON answer, `5xx`, timeouts → `transient`; anything else → `unknown`. An error in a `200`
  body (`ok: false`) is classified the same way.
- **Client** (`client.go`, extending S-041): `sendMessage`, `editMessageText`, `getChat` and `getChatMember` in the
  delivery and interactive classes, refining the answers of sends and edits by their description.
- **Token redaction** (C-14.FR-12): delivery errors stored in `deliveries.last_error`, `delivery_events.error` and
  `destinations.broken_reason` pass through the client's redaction; a lint-5 probe covers the adapter's error paths.
- **Connection delete** (`connections.go`, `telegram.go`): deleting a Telegram Connection in the webhook update mode
  calls `deleteWebhook` with its saved bot and base URL after the delete commits, in the interactive class, as a best
  effort: it neither blocks nor fails the delete, and its outcome (done, or the error Telegram returned, redacted) is
  logged with a registered log event.
- **Fake Telegram** (C-01.FR-13), extending S-041, reproduces the verified facts this story relies on:
  - chats: the channel `@muster_alerts` (`-1001000000001`, "Muster alerts") with comments enabled and its discussion
    group `-1001000000002` ("Muster alerts Chat"), the channel `@no_comments` (`-1001000000003`) without; the bot an
    admin allowed to post and edit in both; `PUT /_fake/chats/{chat}` and `PUT /_fake/chats/{chat}/members/{user}`
    change them;
  - `getChat` names `linked_chat_id` only with comments enabled (F-002); a bot joins a channel only as an admin
    (F-001); `getChatMember` for a bot outside the group answers `403 bot is not a member of the supergroup chat`
    (F-003);
  - an `editMessageText` without `reply_markup` removes the keyboard (F-011); an edit that changes nothing fails with
    `400 message is not modified` (F-017);
  - sends and edits share one budget of 20 per minute per chat; the 21st answers `429` with `retry_after` (F-016);
  - notifications (`GET /_fake/notifications`) for two accounts — `member` (in the discussion group) and `subscriber`
    (channel only): a channel post notifies both, without sound with `disable_notification` (F-012);
  - `GET /_fake/messages?chat=` lists messages with `text`, `parse_mode`, `reply_markup`, `reply_parameters`,
    `message_thread_id`, `disable_notification` and edits.
- **Documentation** (C-14.FR-9; `docs/messengers/telegram.md`): creating the bot; enabling comments on the channel,
  which creates its discussion group (F-002); adding the bot as an admin to the channel — a bot joins a channel only as
  an admin (F-001) — and to the discussion group (F-003); long polling versus webhook and the `409` of a second
  poller; a pointer to "Telegram in restricted networks"; on-call people join the discussion group and mute the channel,
  so each Root message reaches them once through its copy and only group members get Thread replies (F-014); the
  limitation that a Quiet Root message reaches group members with sound (F-013).
- **Design documents**: `design/facts.md` turns the Pending entry "Editing old messages (Telegram)" into a fact — on
  2026-10-09 the bot edited 48 of 48 of its own messages, 124 to 144 hours old, in the channel and in the group, with
  and without a keyboard; the TDLib source agrees (`td/telegram/MessagesManager.cpp`,
  `has_edit_time_limit = !(is_bot && m->is_outgoing)`), and the 48-hour limit applies to `deleteMessage` in groups and
  to business messages. `design/prd/L1.md` answers open question 3 with that fact, and C-14.FR-7 names "message can't
  be edited" next to "message to edit not found".
- **Defaults**: `destination.telegram.limiter`, `connection.telegram.limiter` (P-28).

## Steps

1. Extend the fake server with chats, bot rights, edits, keyboards, the per-chat limit and the notifications of
   channel posts. Check: `faketelegram_test.go` reproduces F-001 to F-003, F-011, F-012, F-016 and F-017 one by one.
2. Write the Telegram Destination type and its check, with the `connections` checker and the API mapping. Check:
   `destcheck_test.go` and `write_test.go` cover C-14.AC-13 and the three rights messages; `destinations_test.go` of
   `internal/api` covers the Telegram input and output.
3. Write the layout, the keyboard, the client methods and the adapter's `Publish`, `Update` and plain `Reply` with the
   response mapping. Check: `layout_test.go` and `adapter_test.go` cover C-14.AC-8, AC-14, AC-17, FR-6 and every row
   of FR-7, "message can't be edited" → `gone` included.
4. Add the Telegram Destinations to `muster doctor`, the best-effort `deleteWebhook` on delete, the secret probe, the
   documentation, the design documents and the end-to-end test. Check: Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
# the Admin's session (`jar`, `H`), the Responder "bob", the Integration "lab" with NOTIFY, ADV and AG, and the
# On-call policy in P, as in S-061; the demo Telegram Connection "Dev Telegram" of S-041 (id in T)
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake; FTG=127.0.0.1:18081/_fake; CH=-1001000000001; GR=-1001000000002
T=$(curl -s -b jar "$API/connections?type=telegram" | jq -r '.items[] | select(.name == "Dev Telegram") | .id')
M0='{"everyone":"none","user_ids":[],"groups":[]}'
M="{\"new_alert_group\":$M0,\"new_alerts\":$M0,\"reopen\":$M0,\"ack_timeout\":$M0,\"snooze_ended\":$M0,\"rise_to_urgent\":$M0}"
MKD() { curl -s "${H[@]}" $API/destinations -d "{\"type\":\"telegram\",\"name\":\"$1\",\"connection_id\":\"$T\",\"channel_id\":\"$2\",
  \"mentions\":$M,\"limiter\":{\"limit\":10,\"per_seconds\":60}}"; }
MSGS() { curl -s "$FTG/messages?chat=$1" | jq -c "$2"; }

# C-14.AC-13: the discussion group is found; two refusals
MKD nocomments @no_comments | jq -r '.errors[0].detail'
# Comments are not enabled for this channel. Enable comments in the channel settings in Telegram; this creates its discussion group.
curl -s -X PUT $FTG/chats/$GR/members/123456 -d '{"status":"member"}' > /dev/null
MKD alerts @muster_alerts | jq -r '.errors[0].detail'
# The bot is not an admin of the discussion group Muster alerts Chat. Make the bot an admin there, allowed to post messages.
curl -s -X PUT $FTG/chats/$GR/members/123456 -d '{"status":"administrator","can_post_messages":true}' > /dev/null
D=$(MKD alerts @muster_alerts | jq -r .destination.id)
curl -s -b jar $API/destinations/$D | jq -c '{discussion_group_id, discussion_group_title}'
# {"discussion_group_id":"-1001000000002","discussion_group_title":"Muster alerts Chat"}
curl -s "${H[@]}" $API/routes -d "{\"name\":\"tg\",\"matchers\":[{\"label\":\"team\",\"op\":\"=\",\"value\":\"tg\"}],\"urgent\":false,
  \"group_key\":[\"alertname\"],\"destination_ids\":[\"$D\"],\"policy\":$P}" > /dev/null

# C-14.AC-17, AC-8, FR-6: a Loud channel post in HTML with its keyboard
curl -s -X PUT $FAM/groups/t1 -d '{"receiver":"lab","route":"{}","labels":{"alertname":"CertExpiry"}}' > /dev/null
curl -s -X PUT $FAM/groups/t1/alerts/a -d '{"labels":{"team":"tg","domain":"a.example.org","note":"@channel <b>x</b>"}}' > /dev/null
NOTIFY t1 '{"reason":"first notification"}'; sleep 2; G=$(AG 'domain%3D%22a.example.org%22')
MSGS $CH '[.[] | {parse_mode, bq: (.text | test("<blockquote expandable>")), table: (.text | test("<table|<pre>")), lit: (.text | test("@​channel &lt;b&gt;x&lt;/b&gt;")), quiet: (.disable_notification // false), kb: (.reply_markup.inline_keyboard | flatten | map(.text))}]'
# [{"parse_mode":"HTML","bq":true,"table":false,"lit":true,"quiet":false,"kb":["Ack","Resolve","Snooze 1 h","Snooze 4 h","Snooze 24 h"]}]

# C-14.AC-14: each edit carries the keyboard of the new state
curl -s "${H[@]}" -X POST $API/alert-groups/$G/acknowledge > /dev/null; sleep 1
MSGS $CH '.[0].reply_markup.inline_keyboard | flatten | map(.text)'          # ["Unack","Resolve","Snooze 1 h","Snooze 4 h","Snooze 24 h"]
curl -s "${H[@]}" -X POST $API/alert-groups/$G/snooze -d '{"until":"2030-01-01T00:00:00Z"}' > /dev/null; sleep 1
MSGS $CH '.[0].reply_markup.inline_keyboard | flatten | map(.text)'          # ["Ack","Unsnooze","Resolve"]
MSGS $CH 'length'                                                            # 1   (Thread replies went to the group)

# C-14.AC-12: the bot may no longer post; the new Alert Group resolves while Broken, so nothing waits; the probe runs
# the Destination check, keeps Broken with the missing right, then ends it once the right is back
curl -s -X PUT $FTG/chats/$CH/members/123456 -d '{"status":"administrator","can_post_messages":false,"can_edit_messages":true}' > /dev/null
curl -s -X PUT $FAM/groups/t3 -d '{"receiver":"lab","route":"{}","labels":{"alertname":"QueueFull"}}' > /dev/null
curl -s -X PUT $FAM/groups/t3/alerts/a -d '{"labels":{"team":"tg","queue":"q1"}}' > /dev/null
NOTIFY t3 '{"reason":"first notification"}'; sleep 1
curl -s -b jar $API/destinations/$D | jq -r .health.state                   # broken
curl -s -X PUT $FAM/groups/t3/alerts/a -d '{"labels":{"team":"tg","queue":"q1"},"status":"resolved"}' > /dev/null
NOTIFY t3 '{"reason":"all alerts resolved"}'
ADV 300; sleep 2
curl -s -b jar $API/destinations/$D | jq -c '.health | {state, r: (.reason | test("post messages"))}'   # {"state":"broken","r":true}
curl -s -X PUT $FTG/chats/$CH/members/123456 -d '{"status":"administrator","can_post_messages":true,"can_edit_messages":true}' > /dev/null
D0=$(curl -s localhost:8082/metrics | grep 'muster_client_requests_total{client="delivery",outcome="ok"}' | awk '{print $2}')
ADV 300; sleep 2; curl -s -b jar $API/destinations/$D | jq -r .health.state   # healthy
curl -s localhost:8082/metrics | grep 'muster_client_requests_total{client="delivery",outcome="ok"}' | awk -v d=$D0 '{print ($2 - d) >= 3}'   # 1   (getChat and two getChatMember)

# C-14.AC-7: no token in logs or stored errors
grep -c "dev-telegram-token" dev.log                                         # 0
psql "$MUSTER_DATABASE_URL" -Atc "SELECT count(*) FROM delivery_events WHERE error LIKE '%dev-telegram-token%'"   # 0

# C-02.FR-14
./bin/muster dev doctor | grep '^destination alerts'                        # destination alerts: ok

# deleteWebhook on delete: a second Connection in the webhook mode, deleted
W=$(curl -s "${H[@]}" $API/connections -d '{"type":"telegram","name":"hooked","bot_token":"777020:hook-token",
  "bot_api_base_url":"http://127.0.0.1:18081","update_mode":"webhook","proxy":{"enabled":false},
  "limiter":{"limit":15,"per_seconds":1}}' | jq -r .id)
curl -s "${H[@]}" -X DELETE $API/connections/$W -o /dev/null -w '%{http_code}\n'   # 204
curl -s $FTG/requests | jq -r '[.[] | select(.path | endswith("/deleteWebhook"))] | length'   # 1
```

`test/e2e/telegram_test.go` repeats these steps and adds the per-chat limit (`429` with `retry_after` after 20 calls in
a minute, waited exactly), "message can't be edited" through a scripted fault (the Root message is republished with
the note of the deleted Root message flow), the failing `deleteWebhook` (the delete still answers `204`) and the
notifications of channel posts (a Quiet post notifies both accounts without sound).

**Optional manual check against a real server** (the operator's test bot and channel with comments enabled): create the
Destination with the channel only and see the discussion group found; publish an Alert Group, acknowledge it in Muster
and see the post edited with its buttons kept. Record the bot-wide rate (P-28) in the pull request if it was measured.

## Open questions

1. **Editing old messages.** Whether the bot can still edit a channel post, and its own message in the discussion
   group, 48 hours and 7 days after sending it, and how the adapter classifies the error Telegram returns for an edit
   it refuses.
   _Resolved (D290):_ the bot can edit its own messages without a time limit. On 2026-10-09 a live test edited 48 of 48
   messages that were 124 to 144 hours old, in the channel and in the discussion group, with and without a keyboard.
   The TDLib source agrees: `td/telegram/MessagesManager.cpp` sets `has_edit_time_limit = !(is_bot &&
   m->is_outgoing)`. The 48-hour limit applies to `deleteMessage` in groups and to business messages. A `400 Message
   can't be edited` on a Root message is therefore `gone`, and the Root message is republished as in the deleted Root
   message flow. This story's pull request moves the Pending entry of `design/facts.md` into a fact and answers L1 open
   question 3.

## Notes

- Suggested commit: `feat(telegram): add telegram destinations, the destination check and root message posts`.
- `deliveries.message_id` of a Telegram Root message is the channel post's `message_id`; the chat comes from the
  Destination, so the keys are the same for the press binding of S-067 and the copy buffer of S-066.
- P-28 stays provisional: only the test environment can confirm it.
- Deleting a Telegram Connection in the webhook update mode left its webhook set at Telegram, which then answers every
  update with 401 for good; the best-effort `deleteWebhook` above closes that gap (raised in the review of S-041).
- The update router of S-041 holds the Connection's row locked while a handler runs; this story registers no update
  handler, and S-067 replaces the row lock with an advisory lock before its presses run.
- Split from the former S-042 together with S-066 and S-067 before its implementation, because the completed contract
  touched about 63 files.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-14.FR-2 | partial | the API and the channel-only rule for Root messages; Thread replies in the group are S-066, the form S-043 |
| C-14.FR-6 | partial | Quiet Root messages; Quiet Thread replies are S-066 |
| C-14.FR-7 | full | `thread_lost` is classified here and handled by S-066 |
| C-14.FR-9 | partial | the Telegram page; "Telegram in restricted networks" is S-058 |
| C-14.FR-12 | partial | the adapter's errors; with S-041 complete |
| C-14.FR-13 | full | |
| C-14.FR-14 | partial | the check, the probe and doctor; "Check" on the page and the form are S-043 |
| C-14.FR-15 | full | |
| C-14.FR-16 | full | |
| C-14.AC-7 | partial | delivery errors; with S-041 complete, the page S-043 |
| C-14.AC-8 | full | |
| C-14.AC-12 | full | |
| C-14.AC-13 | partial | the API; the form is S-043 |
| C-14.AC-14 | full | |
| C-14.AC-17 | full | |
| C-11.FR-7 | partial | Quiet Root messages in Telegram; Thread replies are S-066 |
| C-11.FR-8 | partial | the Telegram response mapping |
| C-11.FR-9 | partial | the Telegram Destination check |
| C-11.FR-18 | partial | the Telegram fields |
| C-12.FR-1 | partial | the Telegram layout |
| C-12.FR-7 | partial | HTML escaping |
| C-12.FR-8 | partial | Mentions in Telegram Root messages; in Thread replies S-066 |
| C-12.FR-11 | partial | the 4,096-character limit |
| C-10.FR-6 | partial | Snooze durations as Telegram buttons; pressing them is S-067 |
| C-02.FR-14 | partial | Telegram Destination checks in `muster doctor` |
| C-01.FR-13 | partial | the fake Telegram server's chats, bot rights, edits, limits and channel notifications |
