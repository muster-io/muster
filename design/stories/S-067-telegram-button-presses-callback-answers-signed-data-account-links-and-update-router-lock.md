---
id: S-067
title: "Telegram button presses: callback answers, signed data, Account links and the update router lock (BE)"
capability: C-14
kind: be
layer: L1
depends_on: [S-042]
covers: [C-14.FR-4, C-14.FR-5, C-14.AC-4, C-14.AC-9, C-14.AC-11, C-14.AC-18, C-10.FR-3, C-10.FR-6, C-10.FR-11, C-01.FR-13]
files_touched:
  - internal/archlint/secretleak.go
  - internal/connections/connections.go
  - internal/connections/connections_test.go
  - internal/connections/live_test.go
  - internal/connections/query.sql
  - internal/connections/telegram.go
  - internal/connections/telegram_test.go
  - internal/db/checks.go
  - internal/delivery/delivery.go
  - internal/delivery/live_test.go
  - internal/delivery/presses.go
  - internal/delivery/presses_test.go
  - internal/delivery/query.sql
  - internal/delivery/worker.go
  - internal/delivery/worker_test.go
  - internal/fakes/faketelegram/faketelegram.go
  - internal/fakes/faketelegram/faketelegram_test.go
  - internal/fakes/faketelegram/presses.go
  - internal/logging/events.go
  - internal/messages/texts/en.json
  - internal/messages/texts/ru.json
  - internal/runtime/runtime.go
  - internal/telegram/adapter.go
  - internal/telegram/adapter_test.go
  - internal/telegram/client.go
  - internal/telegram/poller.go
  - internal/telegram/poller_test.go
  - internal/telegram/presses.go
  - internal/telegram/presses_test.go
  - internal/telegram/updates.go
  - internal/telegram/webhook.go
  - internal/telegram/webhook_test.go
  - test/e2e/telegram_test.go
  - design/prd/l1/defaults.md
  - design/prd/L1.md
acceptance:
  - "[C-14.AC-18, C-14.FR-5] A press is answered with `answerCallbackQuery` (at most 200 characters) before the Root message is edited."
  - "[C-14.AC-9, C-10.FR-11] A press from a Telegram account without an Account link changes nothing and is answered \"Your Telegram account is not linked to Muster. Link it in your profile: {link}\"."
  - "[C-14.FR-4, C-10.FR-3, C-10.FR-6] A press from an account linked to a Responder (link rows set up directly) runs the Command — a Snooze for the pressed duration included — with the Transport `telegram`; button data of at most 64 bytes is verified and must belong to the chat and message pressed."
  - "[C-14.AC-4, C-14.FR-4] A press whose update arrives after the Connection received no updates for longer than `telegram.press_max_age` changes nothing and is logged `telegram_press_dropped`."
  - "[C-14.AC-11] In webhook mode a press posted with the right secret token header is processed like a polled one."
  - "[C-01.FR-13] The fake Telegram server creates presses on channel posts and on comments (F-009) and refuses an answer after `answer_deadline_ms` (F-010); `faketelegram_test.go` checks both facts."
  - "The update router takes a transaction advisory lock keyed by the Connection instead of locking its `connections` row: while a save of the Connection holds the row, a handler runs and a second poller or webhook request with the same update waits and then skips it; only the final offset write waits for the save."
  - "[C-14.FR-5, D295] While a press holds the update lock of its Connection, the delivery worker does not wait: an edit of a Root message of the Connection is rescheduled `telegram.press_edit_delay` later, with no call, attempt, error or limiter token, and the round goes on; the edit goes out once the press released the lock."
  - "[D295] A process handles at most `telegram.parallel_updates` updates at a time; the answer to a press takes a token of the Connection's limiter only."
verify: "make ci test-integration e2e"
operator_attention: false
issue: 179
---

# S-067. Telegram button presses: callback answers, signed data, Account links and the update router lock (BE)

## Scope

**IN**

- The handler of `callback_query` updates on the update router of S-041 (`presses.go`): `answerCallbackQuery` first,
  verifying the signed data and binding it to the chat and message pressed, the `telegram.press_max_age` gap rule with
  `telegram_press_dropped`, linked and unlinked accounts, Commands with the Transport `telegram`.
- Presses in the webhook update mode (C-14.AC-11).
- The answer texts in English and Russian.
- The update router lock: a transaction advisory lock keyed by the Connection in place of `FOR NO KEY UPDATE` on its
  `connections` row.
- Follow-ups accepted in D295: the edit that a press causes is rescheduled instead of waiting for the press; a cap on
  the updates a process handles at a time; press answers on the Connection's limiter only.
- The fake Telegram server's presses (`/_fake/press`), answers and the answer deadline.

**OUT**

- Telegram Destinations, the keyboard and its action ids, `Publish`, `Update` and the response mapping (S-042).
- Comment Threads and the copy buffer (S-066).
- Presses on Thread replies such as Reminders, and "Still on it" (S-049); creating Account links with `/start` (S-051);
  presses on test messages (S-047).

## Contracts

- **Presses** (C-14.FR-4, FR-5; `internal/telegram/presses.go`): the handler of `telegram.KindCallbackQuery`. A
  `callback_query` is answered at once through `delivery.Interactive` on the Connection's limiter only with
  `answerCallbackQuery` (`text` at most 200 characters), before any edit — Telegram refuses an answer after about 15
  seconds (F-010). An answer posts nothing to the channel, so it spends no token of the Destination's limiter (D295),
  and a `429` on an answer holds the whole Connection. The data is verified with `internal/buttons` (at most 64 bytes: the compact action
  id, the key id and the signature of S-036); `message.chat.id` and `message.message_id` must be the Root message's
  channel post of a Telegram Destination of the Connection (`delivery.TelegramPressBinding`, by the Destination's
  `telegram_channel_chat_id` and `deliveries.message_id`), or, from S-049, a Thread reply's message in the discussion
  group (F-009). A press that cannot be verified or bound is answered "This button could not be verified; nothing was
  changed." on the Connection's limiter and changes nothing.
- **Answer before the edit** (C-14.FR-5, AC-18, D295): the handler runs the Command and answers within `HandleOnce`,
  under the Connection's update lock. The delivery worker, after it has read the Desired state of an edit of a Telegram
  Root message, tries the same lock shared without waiting (`pg_try_advisory_xact_lock_shared`, delivery's
  `TryShareUpdateLock`, before it takes limiter tokens): while an update of the Connection is being handled it
  reschedules the edit `telegram.press_edit_delay` later — not an attempt, no error, no token — and goes on with the
  rest of its round. The Command commits its Desired state while its handler holds the lock, until the press is
  answered, so an edit that finds the lock free carries a state from before the press or one whose press was
  answered: the edit follows the answer on any replica, and a slow press never holds the worker. The adapter's
  `Update` never waits. Without a limiter token within `delivery.interactive_budget` the Command has run all the
  same, unanswered, and `telegram_press` names why.
- **Concurrent updates** (D295; `internal/telegram/updates.go`): the router handles at most
  `telegram.parallel_updates` updates at a time per process, over all its Connections and both update modes; the others
  wait for a slot, and one whose context ends while it waits is left unconfirmed. An update being routed holds a
  connection of the main pool for its update lock while its handler takes one more at a time.
- **Press age** (C-14.FR-4, C-14.AC-4): a press older than `telegram.press_max_age` is dropped without an answer and
  logged `telegram_press_dropped` (INFO: `connection`, `gap_seconds`). A `callback_query` carries no press time, so a
  press counts as too old when the Connection had received no updates — no successful long poll, or, in webhook mode,
  Muster not running — for longer than `telegram.press_max_age` before the update that carries it. The gap is measured
  in memory, on the business clock, without a migration (open question 1, D293), and handed to the handler as
  `Update.Gap`:
  - long polling (`poller.go`): the time since the end of the last successful poll of the Connection in this process;
    a failed poll receives nothing, and a full batch of 100 passes its gap on to the next poll, so that every update
    Telegram kept gets it. For a Connection that a polling run polls from its first listing on and that the process
    has not polled yet, the gap starts at the start of the outage just before the run (`connections.Service.Outage`:
    the latest `downtime_periods` row when it ended no earlier than `leader.AbsenceNotice` before the run, or the alive
    mark of `runtime_state` when it is older than that, before the new Leader recorded the downtime), and otherwise at
    the run's start; a Connection that enters long polling later in the run, such as one switched from the webhook
    mode, starts when its poller does, and one that leaves long polling is forgotten. A Bot API outage that spans a
    Leader handover is counted only from the new Leader's start.
  - webhook mode (`webhook.go`): a quiet period is no gap. The first update of the Connection that a replica receives,
    within `telegram.press_max_age` of the replica's start, gets as its gap the outage before that start, read the
    same way, from the outage's start to the replica's start, since the replica could receive from then on; a press
    dropped for it receives nothing, so that the kept presses that follow are dropped too until another update of the
    Connection arrives.
- **Accounts** (C-10.FR-11): `from.id` is mapped through `accountlinks.Lookup("telegram", from.id)` (one identity
  space for all of Telegram). Without a link the answer is "Your Telegram account is not linked to Muster. Link it in
  your profile: {MUSTER_PUBLIC_URL}/profile" and nothing changes; a disabled User and a Viewer get the refusals of
  S-061's texts. With a link the Command runs as the User through the dispatcher of `internal/groups` with the
  Transport `telegram` — Acknowledge, Unacknowledge, Resolve, Snooze for the pressed duration (C-10.FR-6), Unsnooze —
  and the answer is "Done: {command}" or the refusal's text, cut to 200 characters, in the language of the Route.
  The edit of the Root message follows through delivery, never from the handler. Log event `telegram_press` (INFO:
  `connection`, `group`, `command`, `outcome`, and `error` when the press failed or its answer was not made, such as
  Telegram's refusal of a late answer). The update is confirmed whatever the press led to, so that a press never runs
  twice; only a cancelled context leaves it unconfirmed.
- **Texts** (`internal/messages/texts/en.json`, `ru.json`): the Telegram "not linked" answer (`press.telegramNotLinked`),
  "Done: {command}" (`press.done`) and the names of the five Commands (`command.*`) in both languages; the other
  refusals reuse S-061's keys, and the refusals of the dispatcher stay in English, as in Mattermost.
- **Webhook mode** (C-14.AC-11): the webhook endpoint of S-041 hands a press with the right secret token header to the
  same router and handler; nothing differs from a polled press.
- **Update router lock** (`internal/connections/telegram.go`, `query.sql`; `internal/telegram/updates.go`):
  `HandleOnce` takes `pg_advisory_xact_lock(class, hashint8(connection id))` with a new lock class in
  `internal/db/checks.go`, `TelegramUpdateLockClass` (`0x6d75_0004`, D294), next to `RouteMembershipLockClass`, so it
  cannot collide with the Leader's and the
  migration's session locks; a transaction lock works through a pooler in transaction mode. It then reads
  `telegram_update_offset` without a row lock, skips an update before it, runs the handler and raises the offset with
  the existing `GREATEST` update, which is the only statement that touches the `connections` row; that update changes
  nothing when a save set a new bot token meanwhile (it matches the `bot_token_updated_at` read with the offset), so
  that the forgotten offset of the old bot never lands on the new one. No migration is needed. A second poller of a frozen old Leader, or a webhook request with the same update, still waits for the lock
  and then skips the update. The `Handler` contract in `updates.go` changes accordingly: a handler runs under the
  Connection's update lock, not under its row lock. Remaining gap: the final offset write can still wait up to 10
  seconds while a save of the Connection holds the row during `setWebhook`.
- **Client** (`client.go`): `answerCallbackQuery` in the interactive class; its errors pass through the client's
  redaction, and a lint-5 probe covers the press path.
- **Fake Telegram** (C-01.FR-13), extending S-042:
  - `POST /_fake/press` `{chat, message_id, from, button}` creates a `callback_query` with `from`, `message` (the
    channel post, or a comment with `message_thread_id`) and the button's `data` (F-009); with `data_from` set to
    another message id it takes the button's data from that message, to press a valid button on the wrong message;
    `data` forges the data itself, and `pressed_ms_ago` dates the press back, as an update that arrives late;
  - `answerCallbackQuery` fails with `400 query is too old and response timeout expired or query ID is invalid` after
    `answer_deadline_ms` (default 15,000) (F-010); answers (`GET /_fake/answers`) and edits are recorded with their
    times.
- **Defaults**: `telegram.press_max_age`; `telegram.press_edit_delay` (P-51) and `telegram.parallel_updates` (P-52),
  from D295.

## Steps

1. Extend the fake server with presses, answers and the answer deadline. Check: `faketelegram_test.go` reproduces
   F-009 and F-010.
2. Move the update router to the advisory lock. Check: `telegram_test.go` and `live_test.go` of `internal/connections`
   show a handler running while a save holds the row, an update handled once by two concurrent callers, and the final
   offset write waiting for the save.
3. Write the press binding and the handler with the answer first, the age rule, linked and unlinked accounts and the
   texts. Check: `presses_test.go` of `internal/telegram` and `internal/delivery` cover C-14.AC-4, AC-9, AC-18 and the
   binding.
4. Register the handler, add the secret probe and extend the end-to-end test. Check: Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
# as in S-042: API, FAM, FTG, CH, the session (`jar`, `H`), NOTIFY, AG, MSGS, the Responder "bob", the Telegram
# Destination "alerts" (D) on the Route "tg" of the Integration "lab"
curl -s -X PUT $FAM/groups/t1 -d '{"receiver":"lab","route":"{}","labels":{"alertname":"CertExpiry"}}' > /dev/null
curl -s -X PUT $FAM/groups/t1/alerts/a -d '{"labels":{"team":"tg","domain":"a.example.org"}}' > /dev/null
NOTIFY t1 '{"reason":"first notification"}'; sleep 2; G=$(AG 'domain%3D%22a.example.org%22')
POST=$(MSGS $CH '.[0].message_id')

# C-14.AC-18, FR-4: answer first, then the edit with the keyboard of the new state
psql "$MUSTER_DATABASE_URL" -qc "INSERT INTO account_links (org_id, public_id, user_id, messenger, connection_id, external_id, username, created_at)
  SELECT 1, 'AK0000000000T1', u.id, 'telegram', c.id, '5001', 'bob_tg', now() FROM users u, connections c WHERE u.login = 'bob' AND c.name = 'Dev Telegram'"
curl -s -X POST $FTG/press -d "{\"chat\":$CH,\"message_id\":$POST,\"from\":{\"id\":5001,\"username\":\"bob_tg\"},\"button\":\"Ack\"}" > /dev/null; sleep 2
curl -s $FTG/answers | jq -c '.[-1] | {text, ok}'                            # {"text":"Done: Acknowledge","ok":true}
curl -s $FTG/requests | jq -r '[.[] | select(.path | test("answerCallbackQuery|editMessageText")) | .path | sub(".*/"; "")] | .[-2:] | join(" < ")'
# answerCallbackQuery < editMessageText
MSGS $CH '.[0].reply_markup.inline_keyboard | flatten | map(.text)'          # ["Unack","Resolve","Snooze 1 h","Snooze 4 h","Snooze 24 h"]
curl -s -b jar "$API/alert-groups/$G/timeline?limit=1" | jq -c '.items[0] | {event, t: .actor.transport, u: .actor.name}'
# {"event":"acknowledged","t":"telegram","u":"bob"}

# C-10.FR-6: a Snooze button runs a Snooze for its duration
curl -s -X POST $FTG/press -d "{\"chat\":$CH,\"message_id\":$POST,\"from\":{\"id\":5001},\"button\":\"Snooze 4 h\"}" > /dev/null; sleep 1
curl -s -b jar $API/alert-groups/$G | jq -r .status                         # snoozed

# C-14.AC-9: an account without an Account link
curl -s -X POST $FTG/press -d "{\"chat\":$CH,\"message_id\":$POST,\"from\":{\"id\":6001,\"username\":\"carol\"},\"button\":\"Unsnooze\"}" > /dev/null; sleep 1
curl -s $FTG/answers | jq -r '.[-1].text'
# Your Telegram account is not linked to Muster. Link it in your profile: http://localhost:8080/profile
curl -s -b jar $API/alert-groups/$G | jq -r .status                         # snoozed

# the binding: a valid button pressed on another message changes nothing
curl -s -X POST $FTG/press -d "{\"chat\":$CH,\"message_id\":999999,\"from\":{\"id\":5001},\"button\":\"Unsnooze\",\"data_from\":$POST}" > /dev/null; sleep 1
curl -s $FTG/answers | jq -r '.[-1].text'                                   # This button could not be verified; nothing was changed.
grep -c '"event":"telegram_press"' dev.log                                   # 4
```

`test/e2e/telegram_test.go` repeats these steps and adds C-14.AC-4 (a press older than `telegram.press_max_age`, by
the rule of C-14.FR-4, with the development clock), C-14.AC-11 (the Connection "Dev Telegram" switched to webhook mode
and the Ack press posted to the webhook endpoint with the secret token header), and a press answered just before the
fake's 15-second deadline.

**Optional manual check against a real server** (the operator's test bot and channel with comments enabled): press Ack
from an unlinked account and see the short answer on the button.

## Open questions

1. _Resolved (D293):_ option (b), no migration. The poller keeps the time of its last successful poll in memory, and
   the existing downtime records cover "Muster not running", as Press age under Contracts describes.
   **Where the press-age gap is measured.** C-14.FR-4 drops a press whose update arrives after the Connection received
   no updates for longer than `telegram.press_max_age`, and no stored value says when it last did. Two ways:
   - (a) A new nullable column `connections.telegram_updates_at` (expand migration `0006`, `schema.md`), raised by the
     router's final offset write and, in long polling, by a successful poll that brought no update at most once a
     minute; the gap is the business-clock time since it, before the update is handled. It covers a Leader handover in
     the middle of an outage; in webhook mode a quiet period also leaves it old, so webhook mode measures Muster's
     downtime instead (`downtime_periods` of S-008).
   - (b) No migration: in long polling, the poller keeps the time of its last successful poll in memory and passes the
     gap with the updates of the next one; Muster not running is read from `downtime_periods` in both modes. A Bot API
     outage that spans a Leader handover is then counted only from the new Leader's start.

   Recommendation: (b), the simpler one, since the case it misses needs an outage longer than an hour that spans a
   Leader handover. The maintainer decides before the code of step 3 is written; (a) adds the migration, `schema.md`
   and `internal/telegram/poller.go` to `files_touched`, (b) adds `poller.go`, `poller_test.go` and the downtime read.

## Notes

- Suggested commit: `feat(telegram): add button presses with callback answers and the update router lock`.
- `operator_attention: false` — open question 1 was settled in D293.
- Known limits of the update lock as first shipped, resolved in a follow-up (D295): an edit of a Telegram Root message
  waited in the adapter while a press of its Connection was handled, and the delivery worker of a replica handles one
  delivery at a time, so a slow press (a limiter wait of up to `delivery.interactive_budget`, a slow
  `answerCallbackQuery`, or a final offset write behind a save during `setWebhook`) held that replica's deliveries for
  up to about 25 seconds; the edit is now rescheduled after `telegram.press_edit_delay` instead. And each update being
  routed holds one pool connection while its handler takes more, so many Connections routing at once could wait on the
  pool; `telegram.parallel_updates` now caps them at 2 per process: at most 4 connections of the default 10, next to
  the 5 that the processing lanes may take. In the same follow-up, press answers stopped spending a token of the
  Destination's limiter (10 per minute), since they post nothing to the channel. The Russian answer "Готово:
  Подтвердить" stays as it is (D295).
- In the webhook mode, Telegram posts the updates it kept during an outage one at a time; a replica drops the presses
  among them until another update of the Connection reaches it, so a kept comment or channel message between two kept
  presses lets the second through. Telling the kept updates apart for certain would need the time of each update,
  which Telegram does not send.
- The update router of S-041 locked the Connection's row while a handler ran, and a save of the Connection holds the
  same row during `setWebhook`, which can take up to 10 seconds; a press, which waits for its answer and a Command,
  would have made a save and a poller wait for each other (raised in the review of S-041). The advisory lock above
  leaves only the final offset write behind a save (D289, D292): that write can still wait up to 10 seconds while a
  save holds the row during `setWebhook`, and the next update of the Connection waits behind it.
- Presses do not need comment Threads: a Root message is a channel post, so this story depends on S-042 only. Presses
  on Thread replies arrive with the Reminders of S-049, which depends on S-066 and this story.
- Linked presses are tested with link rows set up directly; S-051 creates them.
- Split from S-042 together with S-066 before its implementation; S-047 and S-049 depend on this story.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-14.FR-4 | full | linked presses tested with link rows set up directly; S-051 creates them; presses on Thread replies are S-049 |
| C-14.FR-5 | full | |
| C-14.AC-4 | full | by the press-age rule of C-14.FR-4 |
| C-14.AC-9 | full | |
| C-14.AC-11 | partial | processing the press; with S-041 complete |
| C-14.AC-18 | full | |
| C-10.FR-3 | partial | the Transport `telegram` |
| C-10.FR-6 | partial | Snooze durations pressed in Telegram; the buttons are S-042 |
| C-10.FR-11 | partial | Telegram presses and their private answers |
| C-01.FR-13 | partial | the fake Telegram server's presses, answers and the answer deadline |
