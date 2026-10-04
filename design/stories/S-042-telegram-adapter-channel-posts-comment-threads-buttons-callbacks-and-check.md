---
id: S-042
title: "Telegram adapter: channel posts, comment Threads, buttons, callbacks and Destination check (BE)"
capability: C-14
kind: be
layer: L1
depends_on: [S-041]
covers: [C-14.FR-2, C-14.FR-3, C-14.FR-4, C-14.FR-5, C-14.FR-6, C-14.FR-7, C-14.FR-9, C-14.FR-12, C-14.FR-13, C-14.FR-14, C-14.FR-15, C-14.FR-16, C-14.AC-1, C-14.AC-2, C-14.AC-4, C-14.AC-7, C-14.AC-8, C-14.AC-9, C-14.AC-11, C-14.AC-12, C-14.AC-13, C-14.AC-14, C-14.AC-15, C-14.AC-16, C-14.AC-17, C-14.AC-18, C-14.AC-19, C-11.FR-7, C-11.FR-9, C-11.FR-16, C-11.FR-18, C-11.FR-21, C-12.FR-1, C-12.FR-7, C-12.FR-8, C-12.FR-11, C-10.FR-3, C-10.FR-6, C-10.FR-11, C-02.FR-14, C-01.FR-13, C-11.FR-8]
files_touched:
  - internal/telegram/adapter.go
  - internal/telegram/layout.go
  - internal/telegram/threads.go
  - internal/telegram/copies.go
  - internal/telegram/classify.go
  - internal/telegram/destcheck.go
  - internal/telegram/presses.go
  - internal/telegram/updates.go
  - internal/telegram/query.sql
  - internal/telegram/adapter_test.go
  - internal/telegram/layout_test.go
  - internal/telegram/threads_test.go
  - internal/telegram/presses_test.go
  - internal/destinations/write.go
  - internal/destinations/write_test.go
  - internal/delivery/threads.go
  - internal/api/alertgroups.go
  - internal/doctor/doctor.go
  - internal/leader/tasks.go
  - internal/fakes/faketelegram/faketelegram.go
  - internal/fakes/faketelegram/chats.go
  - internal/fakes/faketelegram/updates.go
  - internal/fakes/faketelegram/faketelegram_test.go
  - internal/runtime/runtime.go
  - internal/logging/events.go
  - internal/archlint/secretleak.go
  - docs/messengers/telegram.md
  - test/e2e/telegram_test.go
acceptance:
  - "[C-14.FR-2, C-14.FR-14, C-14.AC-13, C-11.FR-18] `createDestination` of type `telegram` takes only the Connection and the channel; the Destination check finds the discussion group through `getChat` (`linked_chat_id`) and the Destination shows it read-only; a channel without a linked group is refused with \"Comments are not enabled for this channel. Enable comments in the channel settings in Telegram; this creates its discussion group.\", and a group where the bot is not an admin with \"The bot is not an admin of the discussion group {group}. Make the bot an admin there, allowed to post messages.\""
  - "[C-14.AC-1, C-14.FR-3, C-01.FR-13] A new Alert Group creates a channel post; a new Alert creates a reply to the post's automatic copy in the discussion group, which the fake server shows as a comment under the post."
  - "[C-14.AC-17, C-14.FR-16, C-14.FR-13, C-12.FR-1, C-12.FR-7] The Root message is sent with `parse_mode` HTML; its label sections are inside `<blockquote expandable>`, its Alerts are lines of a list, it contains no table, and alert data are HTML-escaped."
  - "[C-14.AC-8, C-12.FR-7, C-12.FR-11] A label value `@channel <b>x</b>` arrives as literal text; an Alert Group with 25 Alerts fits in 4,096 characters with its title, status, footer, buttons and the link to Muster."
  - "[C-14.AC-14, C-14.FR-15] After Acknowledge the edit of the Root message carries the keyboard of the acknowledged status, and the fake server's message keeps its buttons through every later edit."
  - "[C-14.AC-2, C-11.FR-16] With the copy withheld, Thread replies wait `telegram.copy_wait`, then go to the discussion group as an unattached chain and the delivery state shows \"Thread not attached\"; after a person comments under the post, later replies attach to the copy."
  - "[C-14.AC-16, C-11.FR-16, C-11.FR-21] With the copy deleted, the next Thread reply is refused with \"message to be replied not found\", is sent again to the discussion group without the reply link, the delivery state shows \"Thread not attached\" with a `thread_not_attached` delivery event, the Destination stays healthy, and later replies go unattached."
  - "[C-14.AC-15] An `edited_message` for the automatic copy, and a copy that arrives with `edit_date` set, change no Root message and add no Timeline entry or delivery event."
  - "[C-14.AC-19, C-14.FR-2] Muster posts nothing to the channel but Root messages and Storm summaries: every later lifecycle event of an Alert Group reaches the fake server as a reply in the discussion group or as an edit of the post."
  - "[C-14.FR-6, C-11.FR-7, C-12.FR-8] Quiet new messages carry `disable_notification: true`; Loud ones do not, and only Loud Thread replies carry their Mentions."
  - "[C-14.AC-18, C-14.FR-5] A press is answered with `answerCallbackQuery` (at most 200 characters) before the Root message is edited."
  - "[C-14.AC-9, C-10.FR-11] A press from a Telegram account without an Account link changes nothing and is answered \"Your Telegram account is not linked to Muster. Link it in your profile: {link}\"."
  - "[C-14.FR-4, C-10.FR-3, C-10.FR-6] A press from an account linked to a Responder (link rows set up directly) runs the Command — a Snooze for the pressed duration included — with the Transport `telegram`; button data of at most 64 bytes is verified and must belong to the chat and message pressed."
  - "[C-14.AC-4, C-14.FR-4] A press whose update arrives after the Connection received no updates for longer than `telegram.press_max_age` changes nothing and is logged `telegram_press_dropped`."
  - "[C-14.AC-11] In webhook mode a press posted with the right secret token header is processed like a polled one."
  - "[C-14.FR-7, C-11.FR-8] Response mapping: `429` with `retry_after` waits exactly for the Destination; \"message is not modified\" is success; \"message to edit not found\" starts the deleted Root message flow; `401`, a kicked bot or missing rights make the Destination Broken; `ok: false` in a `200` is classified the same way; a non-JSON answer is transient; anything else ends that delivery as Not delivered."
  - "[C-14.AC-12, C-11.FR-9] With the bot's admin rights removed in the channel and nothing waiting, the probe keeps the Destination Broken with the missing right as the reason; after the rights are restored, the next probe ends the Broken state with no Alert Group activity, its `getChat` and `getChatMember` requests counted under `client=\"delivery\"`."
  - "[C-14.AC-7, C-14.FR-12] A network error on a delivery request leaves the token in no log line, no delivery error in the Timeline and no Destination reason."
  - "[C-02.FR-14] `muster doctor` prints one line per Telegram Destination with the result of its check."
  - "[C-14.FR-9] The documentation covers creating the bot, enabling comments, adding the bot as an admin to the channel and to the discussion group, long polling versus webhook, joining the discussion group and muting the channel for on-call people, and the limitation that a Quiet Root message rings members of the discussion group."
verify: "make ci test-integration e2e"
operator_attention: true
issue: 42
---

# S-042. Telegram adapter: channel posts, comment Threads, buttons, callbacks and Destination check (BE)

## Scope

**IN**

- Telegram Destinations: the channel only, the discussion group found by the Destination check, the check on save, as
  "Check", as the Broken probe and in `muster doctor`.
- The adapter: Root messages as HTML channel posts with expandable label sections and inline keyboards, edits that
  always carry the keyboard, Quiet messages, HTML escaping and the 4,096-character limit, the response mapping.
- Comment Threads: the copy buffer, the copy wait, learning the copy from a comment, unattached chains and the lost
  Thread, the "Thread not attached" delivery state.
- Button presses: verification, the chat and message binding, the age limit, `answerCallbackQuery` first, Commands with
  the Transport `telegram`.
- The documentation page and the fake Telegram server's chats, copies, presses, limits and notifications.

**OUT**

- `/start` for Account links (S-051); Reminders and their buttons on Thread replies (S-049); test messages (S-047);
  the pages (S-043); "Telegram in restricted networks" (S-058).

## Contracts

- **Operations implemented**: `createDestination`, `updateDestination` and `checkDestination` for the type `telegram`;
  `listAlertGroupDeliveries` fills `thread_not_attached`. Schemas: `TelegramDestination(Input)`
  (`discussion_group_id`, `discussion_group_title`, `channel_title`), `DestinationCheckItem` (`channel_exists`,
  `discussion_group`, `bot_rights_channel`, `bot_rights_group`).
- **Telegram Destinations** (C-14.FR-2; `destinations.telegram_*`): `connection_id` (a Telegram Connection),
  `channel_id` (a chat id or `@username`, stored as entered), `mentions` (validated by S-037: `everyone` `none`, no
  groups), `limiter` (default `destination.telegram.limiter`, which counts sends and edits in the channel and in its
  discussion group alike, F-016). Saving runs the Destination check through the interactive path and refuses a failing
  one with `422 destination_check_failed` (S-039); a passing check stores `telegram_channel_chat_id`,
  `telegram_discussion_chat_id` and the two titles.
- **Destination check** (C-14.FR-14; `destcheck.go`): `getChat` on the channel (`channel_exists`: it exists and is a
  channel); its `linked_chat_id` (`discussion_group`: missing → the "Comments are not enabled …" text of
  [reference.md](../prd/l1/reference.md#banners-warnings-and-notices), F-002); `getChatMember` for the bot in the
  channel (`bot_rights_channel`: an admin allowed to post and edit messages; otherwise "The bot is not an admin of the
  channel.", "The bot may not post messages in the channel." or "The bot may not edit messages in the channel.", F-001); `getChatMember` for the bot in the group (`bot_rights_group`: an admin allowed to post; a `403` "bot is not a
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
  message carries an empty keyboard on purpose (F-011).
- **Quiet and Loud** (C-14.FR-6, C-11.FR-7): a Quiet new message carries `disable_notification: true` (F-012); Thread
  replies carry their Mentions only when Loud.
- **Channel and Threads** (C-14.FR-2, FR-3; `threads.go`, `copies.go`, `deliveries.thread_*`,
  `telegram_post_copies`): the channel carries only Root messages and Storm summaries (and test messages, S-047); a
  Thread reply is `sendMessage` to the discussion group with `reply_parameters.message_id` = the copy (F-007).
  - The update router of S-041 hands group `message` updates here. One with `is_automatic_forward: true` whose
    `forward_origin` is the channel is the automatic copy: `(connection, channel, forward_origin.message_id) → copy id`
    is stored with `learned_from` `automatic_forward` (F-005); its `edit_date` and any later `edited_message` for it
    change nothing (F-006). A message whose `reply_to_message` is such a copy is a person's comment and teaches the copy
    id the same way (`learned_from` `comment`).
  - When a Publication completes, the worker looks the copy up; whichever arrives second sets `thread_anchor_id` and
    `thread_state` `attached`. Until then `thread_state` is `waiting_for_copy` and a due Thread reply waits — at most
    `telegram.copy_wait` from the Publication; after that it is sent to the group as a chain (a reply to
    `thread_chain_last_id` when there is one), `thread_state` becomes `unattached` and a `thread_not_attached` delivery
    event is recorded. A copy learned later makes the following replies attach.
  - A reply refused with "message to be replied not found" (F-008) is the outcome `thread_lost`: the same reply is sent
    to the group without `reply_parameters`, `thread_state` becomes `unattached` with a `thread_not_attached` event, and
    the Destination stays healthy.
  - `listAlertGroupDeliveries` shows `thread_not_attached: true` for `unattached`; S-061's `delivery_problem` counts it.
    The buffer is pruned after a day by a Leader task.
- **Response mapping** (C-14.FR-7; `classify.go`): `429` with `parameters.retry_after` → `retry_after` for the
  Destination, and for the whole Connection when a second Destination of the same Connection gets a `429` within 60
  seconds (C-14.FR-7); "message is not modified" → `ok` (F-017); "message to edit not found" → `gone`;
  "message to be replied not found" on a reply → `thread_lost`; "can't parse entities" → `markup_rejected` (resent
  without `parse_mode`); `401`, "bot was kicked", "not enough rights", "chat not found", "need administrator rights" →
  `fatal`; a non-JSON answer, `5xx`, timeouts → `transient`; anything else → `unknown`. An error in a `200` body
  (`ok: false`) is classified the same way.
- **Presses** (C-14.FR-4, FR-5; `presses.go`): a `callback_query` is answered at once through `delivery.Interactive` on
  the Connection's and the Destination's limiters with `answerCallbackQuery` (`text` at most 200 characters), before
  any edit — Telegram refuses an answer after about 15 seconds (F-010). The data is verified with `internal/buttons`;
  `message.chat.id` and `message.message_id` must be the Root message's channel post (or, from S-049, a Thread reply's
  message in the discussion group, F-009). A press older than `telegram.press_max_age` is dropped without an answer and
  logged `telegram_press_dropped`; since a `callback_query` carries no press time, a press counts as too old when the
  Connection had received no updates — no successful poll, or Muster not running — for longer than
  `telegram.press_max_age` before the update that carries it (C-14.FR-4). `from.id` is mapped through `accountlinks.Lookup("telegram",
  from.id)`: without a link the answer is "Your Telegram account is not linked to Muster. Link it in your profile:
  {MUSTER_PUBLIC_URL}/profile" and nothing changes; with one the Command runs as the User with the Transport
  `telegram` and the answer is "Done: {command}" or the refusal's text, cut to 200 characters. Log event
  `telegram_press` (INFO: `connection`, `group`, `command`, `outcome`).
- **Token redaction** (C-14.FR-12): delivery errors stored in `deliveries.last_error`, `delivery_events.error` and
  `destinations.broken_reason` pass through the client's redaction; a lint-5 probe covers the adapter's error paths.
- **Fake Telegram** (C-01.FR-13), extending S-041, reproduces the verified facts this capability relies on:
  - chats: the channel `@muster_alerts` (`-1001000000001`, "Muster alerts") with comments enabled and its discussion
    group `-1001000000002` ("Muster alerts Chat"), the channel `@no_comments` (`-1001000000003`) without; the bot an
    admin allowed to post and edit in both; `PUT /_fake/chats/{chat}` and `PUT /_fake/chats/{chat}/members/{user}`
    change them;
  - `getChat` names `linked_chat_id` only with comments enabled (F-002); `getChatMember` for a bot outside the group
    answers `403 bot is not a member of the supergroup chat` (F-003);
  - after a channel post, its automatic copy arrives in the group as a `message` update from `777000` with
    `sender_chat` the channel, `is_automatic_forward: true`, `forward_origin` naming the channel and the post id,
    `edit_date` equal to `date` and no keyboard, `copy_delay_ms` later (default 1,000 in `muster dev`; about 4 s on the
    real service) — only to a bot that is an admin of the group (F-003, F-005); `PUT /_fake/config
    {"withhold_copies": true}` keeps them back;
  - an edit of a channel post edits the copy and sends an `edited_message` for it within the same second; the bot gets
    no updates about its own channel posts (F-006);
  - a group message with `reply_parameters` to the copy is a comment with `message_thread_id` = the copy (F-007); after
    the copy is deleted (`DELETE /_fake/chats/{chat}/messages/{id}`) such a reply fails with `400 Bad Request: message
    to be replied not found` (F-008);
  - `POST /_fake/press` `{chat, message_id, from, button}` creates a `callback_query` with `from`, `message` (the
    channel post, or a comment with `message_thread_id`) and the button's `data` (F-009); `answerCallbackQuery` fails
    with `400 query is too old and response timeout expired or query ID is invalid` after `answer_deadline_ms`
    (default 15,000) (F-010); answers and edits are recorded with their times;
  - an `editMessageText` without `reply_markup` removes the keyboard (F-011); an edit that changes nothing fails with
    `400 message is not modified` (F-017);
  - sends and edits share one budget of 20 per minute per chat; the 21st answers `429` with `retry_after` (F-016);
  - notifications (`GET /_fake/notifications`) for two accounts — `member` (in the discussion group) and `subscriber`
    (channel only): a channel post notifies both, without sound with `disable_notification` (F-012); its automatic copy
    notifies `member` with sound (F-013); a reply in a Thread notifies only `member`, even when it mentions
    `subscriber` (F-014); a reply inside the channel would be a new post and a new copy (F-015);
  - `POST /_fake/comment` `{post_id, from, text}` posts a person's comment under a channel post (a reply to its copy,
    delivered to the bot even when the copy was withheld); `GET /_fake/messages?chat=` lists messages with `text`,
    `parse_mode`, `reply_markup`, `reply_parameters`, `message_thread_id`, `disable_notification` and edits.
- **Documentation** (C-14.FR-9; `docs/messengers/telegram.md`): creating the bot; enabling comments on the channel,
  which creates its discussion group (F-002); adding the bot as an admin to the channel — a bot joins a channel only as
  an admin (F-001) — and to the discussion group (F-003); long polling versus webhook and the `409` of a second
  poller; a pointer to "Telegram in restricted networks"; on-call people join the discussion group and mute the channel,
  so each Root message reaches them once through its copy and only group members get Thread replies (F-014); the
  limitation that a Quiet Root message reaches group members with sound (F-013).
- **Defaults**: `destination.telegram.limiter`, `telegram.copy_wait` (P-30, measured in the test environment),
  `telegram.press_max_age`, `connection.telegram.limiter` (P-28).

## Steps

1. Extend the fake server with chats, copies, presses, limits and notifications. Check: `faketelegram_test.go`
   reproduces F-001 to F-018 one by one (F-004 is not used).
2. Write the Telegram Destination type and its check. Check: `write_test.go` covers C-14.AC-13.
3. Write the layout, keyboard and the adapter's sends and edits with the response mapping. Check: `layout_test.go` and
   `adapter_test.go` cover C-14.AC-8, AC-14, AC-17 and FR-7.
4. Write the copy buffer and the Thread rules. Check: `threads_test.go` covers C-14.AC-2, AC-15, AC-16 with a manual
   clock.
5. Write presses with the answer first. Check: `presses_test.go` covers C-14.AC-4, AC-9, AC-18 and the binding.
6. Add doctor lines, the pruning task, the secret probe, the documentation and the end-to-end test. Check:
   Verification below.

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

# C-14.AC-1, AC-17, AC-8: a channel post in HTML, then a reply to its copy
curl -s -X PUT $FAM/groups/t1 -d '{"receiver":"lab","route":"{}","labels":{"alertname":"CertExpiry"}}' > /dev/null
curl -s -X PUT $FAM/groups/t1/alerts/a -d '{"labels":{"team":"tg","domain":"a.example.org","note":"@channel <b>x</b>"}}' > /dev/null
NOTIFY t1 '{"reason":"first notification"}'; sleep 2; G=$(AG 'domain%3D%22a.example.org%22')
MSGS $CH '[.[] | {parse_mode, bq: (.text | test("<blockquote expandable>")), table: (.text | test("<table|<pre>")), lit: (.text | test("@\u200bchannel &lt;b&gt;x&lt;/b&gt;")), kb: (.reply_markup.inline_keyboard | flatten | map(.text))}]'
# [{"parse_mode":"HTML","bq":true,"table":false,"lit":true,"kb":["Ack","Resolve","Snooze 1 h","Snooze 4 h","Snooze 24 h"]}]
curl -s -X PUT $FAM/groups/t1/alerts/b -d '{"labels":{"team":"tg","domain":"b.example.org"}}' > /dev/null
NOTIFY t1 '{"reason":"new alerts added"}'; sleep 1
MSGS $GR '[.[] | select(.from.id == 123456) | {reply: (.reply_parameters.message_id != null), thread: .message_thread_id != null}] | last'   # {"reply":true,"thread":true}

# C-14.AC-14, AC-18: answer first, then the edit with the keyboard of the new state
psql "$MUSTER_DATABASE_URL" -qc "INSERT INTO account_links (org_id, public_id, user_id, messenger, connection_id, external_id, username, created_at)
  SELECT 1, 'AK0000000000T1', u.id, 'telegram', c.id, '5001', 'bob_tg', now() FROM users u, connections c WHERE u.login = 'bob' AND c.name = 'Dev Telegram'"
POST=$(MSGS $CH '.[0].message_id')
curl -s -X POST $FTG/press -d "{\"chat\":$CH,\"message_id\":$POST,\"from\":{\"id\":5001,\"username\":\"bob_tg\"},\"button\":\"Ack\"}" > /dev/null; sleep 2
curl -s $FTG/answers | jq -c '.[-1] | {text, ok}'                            # {"text":"Done: Acknowledge","ok":true}
curl -s $FTG/requests | jq -r '[.[] | select(.path | test("answerCallbackQuery|editMessageText")) | .path | sub(".*/"; "")] | .[-2:] | join(" < ")'
# answerCallbackQuery < editMessageText
MSGS $CH '.[0].reply_markup.inline_keyboard | flatten | map(.text)'          # ["Unack","Resolve","Snooze 1 h","Snooze 4 h","Snooze 24 h"]
curl -s "${H[@]}" -X POST $API/alert-groups/$G/snooze -d '{"until":"2030-01-01T00:00:00Z"}' > /dev/null; sleep 1
MSGS $CH '.[0].reply_markup.inline_keyboard | flatten | map(.text)'          # ["Ack","Unsnooze","Resolve"]

# C-14.AC-15: each edit made the fake send an edited_message for the copy; none of them changed anything
curl -s -b jar "$API/alert-groups/$G/timeline?kind=delivery" | jq -c '[.items[].delivery_event]'   # ["publication"]
curl -s $FTG/requests | jq '[.[] | select(.path | endswith("/editMessageText"))] | length'          # 2   (Acknowledge and Snooze)

# C-14.AC-9: an account without an Account link
curl -s -X POST $FTG/press -d "{\"chat\":$CH,\"message_id\":$POST,\"from\":{\"id\":6001,\"username\":\"carol\"},\"button\":\"Ack\"}" > /dev/null; sleep 1
curl -s $FTG/answers | jq -r '.[-1].text'
# Your Telegram account is not linked to Muster. Link it in your profile: http://localhost:8080/profile
curl -s -b jar $API/alert-groups/$G | jq -r .status                         # snoozed

# C-14.AC-19, FR-6: every later event went to the group or edited the post; Quiet messages are silent
MSGS $CH 'length'                                                            # 1
MSGS $GR '[.[] | select(.from.id == 123456) | .disable_notification] | unique'   # [false]   (the reply about b was Loud)

# C-14.AC-2: the copy is withheld; replies go unattached until a person comments
curl -s -X PUT $FTG/config -d '{"withhold_copies":true}' > /dev/null
curl -s -X PUT $FAM/groups/t2 -d '{"receiver":"lab","route":"{}","labels":{"alertname":"DiskSlow"}}' > /dev/null
curl -s -X PUT $FAM/groups/t2/alerts/a -d '{"labels":{"team":"tg","disk":"sda"}}' > /dev/null
NOTIFY t2 '{"reason":"first notification"}'; G2=$(AG 'disk%3D%22sda%22')
curl -s -X PUT $FAM/groups/t2/alerts/b -d '{"labels":{"team":"tg","disk":"sdb"}}' > /dev/null
NOTIFY t2 '{"reason":"new alerts added"}'; ADV 61; sleep 1                  # past telegram.copy_wait
curl -s -b jar $API/alert-groups/$G2/deliveries | jq -c '.items[0] | {state, thread_not_attached}'   # {"state":"delivered","thread_not_attached":true}
P2=$(MSGS $CH '.[-1].message_id'); curl -s -X POST $FTG/comment -d "{\"post_id\":$P2,\"from\":{\"id\":7001},\"text\":\"looking\"}" > /dev/null
curl -s -X PUT $FAM/groups/t2/alerts/c -d '{"labels":{"team":"tg","disk":"sdc"}}' > /dev/null
NOTIFY t2 '{"reason":"new alerts added"}'; ADV 61; sleep 1
MSGS $GR '[.[] | select(.from.id == 123456)] | last | .reply_parameters.message_id != null'   # true
curl -s -X PUT $FTG/config -d '{"withhold_copies":false}' > /dev/null

# C-14.AC-16: the copy is deleted; the reply goes unattached, the Destination stays healthy
COPY=$(MSGS $GR "[.[] | select(.is_automatic_forward and .forward_origin.message_id == $POST)][0].message_id")
curl -s -X DELETE $FTG/chats/$GR/messages/$COPY
curl -s "${H[@]}" -X POST $API/alert-groups/$G/unsnooze > /dev/null
curl -s -X PUT $FAM/groups/t1/alerts/c -d '{"labels":{"team":"tg","domain":"c.example.org"}}' > /dev/null
NOTIFY t1 '{"reason":"new alerts added"}'; ADV 61; sleep 1
curl -s -b jar $API/alert-groups/$G/deliveries | jq -c '.items[0] | {thread_not_attached}'   # {"thread_not_attached":true}
curl -s -b jar "$API/alert-groups/$G/timeline?kind=delivery&limit=1" | jq -r '.items[0].delivery_event'   # thread_not_attached
curl -s -b jar $API/destinations/$D | jq -r .health.state                   # healthy

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

# C-14.AC-11: in webhook mode a press arrives through the endpoint with the header
# (test/e2e/telegram_test.go switches "Dev Telegram" to webhook mode and repeats the Ack press of C-14.AC-18)

# C-14.AC-7: no token in logs or stored errors
grep -c "dev-telegram-token" dev.log                                         # 0
psql "$MUSTER_DATABASE_URL" -Atc "SELECT count(*) FROM delivery_events WHERE error LIKE '%dev-telegram-token%'"   # 0

# C-02.FR-14
./bin/muster dev doctor | grep '^destination alerts'                        # destination alerts: ok
```

`test/e2e/telegram_test.go` repeats these steps and adds C-14.AC-4 (a press older than `telegram.press_max_age`, by
the rule of C-14.FR-4, with the development clock), C-14.AC-11 in webhook mode, the per-chat limit (`429` with
`retry_after` after 20 calls in a minute, waited exactly) and the notification model of the fake (a Quiet post notifies
both accounts without sound; its copy notifies `member` with sound; a Thread reply notifies `member` only).

**Optional manual check against a real server** (the operator's test bot and channel with comments enabled): create the
Destination with the channel only and see the discussion group found; publish an Alert Group, acknowledge it in Muster
and see the post edited with its buttons kept; see new Alerts appear as comments; press Ack from an unlinked account and
see the short answer; delete the copy in the group and see the next reply arrive outside the Thread. Record the delay of
the automatic copy (P-30) and, if measured, the bot-wide rate (P-28) in the pull request.

## Open questions

1. **Editing old messages.** Whether the bot can still edit a channel post, and its own message in the discussion
   group, 48 hours and 7 days after sending it is not verified yet: it is on the Pending list of `design/facts.md`,
   with a post sent on 2026-10-03 edited after 2026-10-05 and again after 2026-10-10 (L1 open question 3). The answer
   decides how the adapter classifies the error Telegram returns for an edit of a message that is too old: either as
   `gone`, so that the Root message is republished as in the deleted Root message flow, with its note, and the old
   post stays as it was; or as a final refusal of that edit, so that the delivery ends as Not delivered and the Alert
   Group page shows it. The operator decides once both tests have run, before this story is implemented; until then
   the contract keeps the mapping above, in which that error falls under "anything else".

## Notes

- Suggested commit: `feat(telegram): add the telegram adapter with comment threads and button presses`.
- The copy may arrive before the answer to `sendMessage` and on another replica; `telegram_post_copies` is the meeting
  point, so the order never matters.
- P-28 and P-30 stay provisional: only the test environment can confirm them.
- `operator_attention: true` — the operator runs the edit tests of the Pending list and settles open question 1.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-14.FR-2 | partial | the API; the form is S-043 |
| C-14.FR-3 | full | |
| C-14.FR-4 | full | linked presses tested with link rows set up directly; S-051 creates them |
| C-14.FR-5 | full | |
| C-14.FR-6 | full | |
| C-14.FR-7 | full | |
| C-14.FR-9 | partial | the Telegram page; "Telegram in restricted networks" is S-058 |
| C-14.FR-12 | partial | the adapter's errors; with S-041 complete |
| C-14.FR-13 | full | |
| C-14.FR-14 | partial | the check, the probe and doctor; "Check" on the page and the form are S-043 |
| C-14.FR-15 | full | |
| C-14.FR-16 | full | |
| C-14.AC-1 | full | |
| C-14.AC-2 | full | |
| C-14.AC-4 | full | by the press-age rule of C-14.FR-4 |
| C-14.AC-7 | partial | delivery errors; with S-041 complete, the page S-043 |
| C-14.AC-8 | full | |
| C-14.AC-9 | full | |
| C-14.AC-11 | partial | processing the press; with S-041 complete |
| C-14.AC-12 | full | |
| C-14.AC-13 | partial | the API; the form is S-043 |
| C-14.AC-14 | full | |
| C-14.AC-15 | full | |
| C-14.AC-16 | partial | the API; "Thread not attached" on the page is S-043 |
| C-14.AC-17 | full | |
| C-14.AC-18 | full | |
| C-14.AC-19 | full | |
| C-11.FR-7 | partial | Quiet messages in Telegram |
| C-11.FR-9 | partial | the Telegram Destination check |
| C-11.FR-16 | partial | "Thread not attached" |
| C-11.FR-18 | partial | the Telegram fields |
| C-11.FR-21 | partial | `thread_not_attached` |
| C-12.FR-1 | partial | the Telegram layout |
| C-12.FR-7 | partial | HTML escaping |
| C-12.FR-8 | partial | Mentions in Telegram |
| C-12.FR-11 | partial | the 4,096-character limit |
| C-10.FR-3 | partial | the Transport `telegram` |
| C-10.FR-6 | partial | Snooze durations as Telegram buttons |
| C-10.FR-11 | partial | Telegram presses and their private answers |
| C-02.FR-14 | partial | Telegram Destination checks in `muster doctor` |
| C-01.FR-13 | partial | the fake Telegram server's chats, copies, presses, limits and notifications |
| C-11.FR-8 | partial | the Telegram response mapping |
