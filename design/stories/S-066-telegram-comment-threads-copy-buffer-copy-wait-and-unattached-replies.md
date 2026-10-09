---
id: S-066
title: "Telegram comment Threads: the post copy buffer, waiting for the copy and unattached replies (BE)"
capability: C-14
kind: be
layer: L1
depends_on: [S-042]
covers: [C-14.FR-2, C-14.FR-3, C-14.FR-6, C-14.AC-1, C-14.AC-2, C-14.AC-15, C-14.AC-16, C-14.AC-19, C-11.FR-7, C-11.FR-16, C-11.FR-21, C-12.FR-8, C-01.FR-13]
files_touched:
  - internal/delivery/copies.go
  - internal/delivery/copies_test.go
  - internal/delivery/delivery.go
  - internal/delivery/live_test.go
  - internal/delivery/outcomes.go
  - internal/delivery/outcomes_test.go
  - internal/delivery/query.sql
  - internal/delivery/threads.go
  - internal/delivery/threads_test.go
  - internal/devmode/devmode.go
  - internal/devmode/devmode_test.go
  - internal/fakes/faketelegram/copies.go
  - internal/fakes/faketelegram/faketelegram.go
  - internal/fakes/faketelegram/faketelegram_test.go
  - internal/fakes/faketelegram/updates.go
  - internal/leader/leader_test.go
  - internal/leader/tasks.go
  - internal/metrics/catalogue.go
  - internal/runtime/runtime.go
  - internal/runtime/runtime_test.go
  - internal/telegram/adapter.go
  - internal/telegram/adapter_test.go
  - internal/telegram/copies.go
  - internal/telegram/copies_test.go
  - internal/telegram/updates.go
  - test/e2e/harness.go
  - test/e2e/telegram_test.go
acceptance:
  - "[C-14.AC-1, C-14.FR-3, C-01.FR-13] A new Alert Group creates a channel post; a new Alert creates a reply to the post's automatic copy in the discussion group, which the fake server shows as a comment under the post."
  - "[C-14.AC-2, C-11.FR-16] With the copy withheld, Thread replies wait `telegram.copy_wait`, then go to the discussion group as an unattached chain and the delivery state shows \"Thread not attached\"; after a person comments under the post, later replies attach to the copy."
  - "[C-14.AC-16, C-11.FR-16, C-11.FR-21] With the copy deleted, the next Thread reply is refused with \"message to be replied not found\", is sent again to the discussion group without the reply link, the delivery state shows \"Thread not attached\" with a `thread_not_attached` delivery event, the Destination stays healthy, and later replies go unattached."
  - "[C-14.AC-15] An `edited_message` for the automatic copy, and a copy that arrives with `edit_date` set, change no Root message and add no Timeline entry or delivery event."
  - "[C-14.AC-19, C-14.FR-2] Muster posts nothing to the channel but Root messages and Storm summaries: every later lifecycle event of an Alert Group reaches the fake server as a reply in the discussion group or as an edit of the post."
  - "[C-14.FR-6, C-11.FR-7, C-12.FR-8] Quiet Thread replies carry `disable_notification: true`; Loud ones do not, and only Loud Thread replies carry their Mentions."
  - "[C-14.FR-3] The copy and the answer to `sendMessage` meet in `telegram_post_copies` in either order and on any replica: a copy learned before the Publication completes, after it, or only when a due Thread reply looks it up, attaches the Thread all the same."
  - "[C-01.FR-13] The fake Telegram server reproduces F-003, F-005 to F-008 and F-013 to F-015: the automatic copy of each post to an admin bot after `copy_delay_ms`, `withhold_copies`, the `edited_message` of a copy, comments, deleted copies and the notifications of copies and Thread replies; `faketelegram_test.go` checks each fact."
  - "The `short_lived_pruning` Leader task deletes the rows of `telegram_post_copies` received more than a day ago and counts them in `muster_short_lived_rows_pruned_total{table=\"telegram_post_copies\"}`."
verify: "make ci test-integration e2e"
operator_attention: false
issue: null
---

# S-066. Telegram comment Threads: the post copy buffer, waiting for the copy and unattached replies (BE)

## Scope

**IN**

- The adapter's `Reply` into the comment Thread: a reply to the automatic copy of the post in the discussion group,
  or, while the Thread is not attached, a link in the unattached chain.
- The copy buffer `telegram_post_copies`: learning the copy from the automatic forward and from a person's comment,
  meeting the answer to `sendMessage` in either order, and pruning after a day.
- The handler of chat messages on the update router of S-041.
- The Thread states `waiting_for_copy`, `attached` and `unattached`, `telegram.copy_wait`, the lost Thread
  (`thread_lost`) with its resend and the `thread_not_attached` delivery event.
- Ignoring `edit_date` and `edited_message` of a copy (C-14.AC-15).
- The fake Telegram server's automatic copies, their edits, comments, deleted copies and the notifications of copies
  and Thread replies.

**OUT**

- Telegram Destinations, the Destination check, the layout, `Publish`, `Update`, the response mapping (including the
  `thread_lost` class) and the documentation (S-042).
- Button presses and the update router lock (S-067); presses on Thread replies and Reminders (S-049).
- "Thread not attached to the Root message" on the Alert Group page (S-043).

## Contracts

- **Operations**: `listAlertGroupDeliveries` shows `thread_not_attached: true` for a delivery whose `thread_state` is
  `unattached` (the mapping exists since S-061); S-061's `delivery_problem` counts it.
- **Tables**: `deliveries.thread_state`, `thread_anchor_id`, `thread_chain_last_id`; `telegram_post_copies`, whose
  writer is `internal/delivery` ([schema.md](../db/schema.md#2-inventory)), so its queries are in
  `internal/delivery/query.sql` and no package is added to `sqlc.yaml`; `delivery_events` (`thread_not_attached`).
- **Copies** (C-14.FR-3; `internal/telegram/copies.go`): the handler of `telegram.KindChatMessage` that S-041's router
  calls. A group `message` with `is_automatic_forward: true` whose `forward_origin` is a channel is the automatic copy
  of the post `forward_origin.message_id` (F-005); a group `message` whose `reply_to_message` is such a copy is a
  person's comment and teaches the same copy id (`learned_from` `comment`, F-007). A copy's `edit_date`, set from the
  start, and any `edited_message` for it change nothing (F-005, F-006); every other chat message is ignored. The
  handler calls `delivery.LearnCopy` (connection, channel chat id, post id, discussion chat id, copy id, learned from)
  and keeps the update router's rule: it is short and touches no `connections` row.
- **Learning a copy** (`internal/delivery/copies.go`, a new function of delivery, because only `internal/delivery`
  writes `deliveries`): in one transaction it stores `(connection, channel, post) → copy` in `telegram_post_copies`
  (the first learned copy wins; `received_at` on the business clock) and attaches every delivery of a Telegram
  Destination of that Connection and channel whose Root message is that post and whose Thread is not attached yet:
  `thread_anchor_id` = the copy, `thread_state` `attached`. The delivery worker does the same from its side: when a
  Publication completes it looks the copy up, and a due Thread reply in `waiting_for_copy` looks it up again before it
  waits, so that whichever arrives second attaches the Thread and no interleaving of the two transactions leaves it
  unattached (F-005: the copy may arrive before the answer to `sendMessage`, on another replica).
- **Thread states** (C-14.FR-3, C-11.FR-16; `threads.go`, `outcomes.go`): a completed Publication without a known copy
  sets `thread_state` `waiting_for_copy`. A due Thread reply then waits — at most `telegram.copy_wait` from the
  Publication; after that it is sent to the discussion group as a chain (a reply to `thread_chain_last_id` when there is
  one), `thread_state` becomes `unattached`, `thread_chain_last_id` the reply's id, and a `thread_not_attached` delivery
  event is recorded. A copy learned later makes the following replies attach.
- **Lost Thread** (C-14.FR-3, C-11.FR-21): a reply refused with "message to be replied not found" (F-008) comes back
  from S-042's mapping as `thread_lost`. Delivery's rule replaces the interim retry of S-034: the same reply is sent
  again at once to the discussion group without `reply_parameters` (the chain), `thread_state` becomes `unattached`
  with a `thread_not_attached` delivery event, the Destination stays healthy, and later replies follow the chain.
- **Reply** (`internal/telegram/adapter.go`): `sendMessage` to `telegram_discussion_chat_id` with
  `reply_parameters.message_id` = `Root.ThreadAnchorID` when attached, `Root.ChainLastID` in an unattached chain, and
  none for the first link of a chain; `disable_notification: true` when Quiet (F-012); Mentions only when Loud
  (C-12.FR-8, F-014). Muster never replies inside the channel (F-015).
- **Pruning**: `telegram_post_copies` rows received more than a day ago are deleted by the `short_lived_pruning`
  Leader task of S-008 (`internal/leader/tasks.go`), which this story extends with a delivery table, and the table
  joins `metrics.ShortLivedTables`.
- **Fake Telegram** (C-01.FR-13), extending S-042:
  - after a channel post, its automatic copy arrives in the group as a `message` update from `777000` with
    `sender_chat` the channel, `is_automatic_forward: true`, `forward_origin` naming the channel and the post id,
    `edit_date` equal to `date` and no keyboard, `copy_delay_ms` later (default 1,000 in `muster dev`, set by
    `internal/devmode`; about 4 s on the real service) — only to a bot that is an admin of the group (F-003, F-005);
    `PUT /_fake/config {"withhold_copies": true}` keeps them back;
  - an edit of a channel post edits the copy and sends an `edited_message` for it within the same second; the bot gets
    no updates about its own channel posts (F-006);
  - a group message with `reply_parameters` to the copy is a comment with `message_thread_id` = the copy (F-007); after
    the copy is deleted (`DELETE /_fake/chats/{chat}/messages/{id}`) such a reply fails with `400 Bad Request: message
    to be replied not found` (F-008);
  - `POST /_fake/comment` `{post_id, from, text}` posts a person's comment under a channel post (a reply to its copy,
    delivered to the bot even when the copy was withheld);
  - notifications: a post's automatic copy notifies `member` with sound (F-013); a reply in a Thread notifies only
    `member`, even when it mentions `subscriber` (F-014); a reply inside the channel would be a new post and a new copy
    (F-015).
- **Defaults**: `telegram.copy_wait` (P-30, measured in the test environment).

## Steps

1. Extend the fake server with copies, their edits, comments, deletes and notifications. Check: `faketelegram_test.go`
   reproduces F-003, F-005 to F-008 and F-013 to F-015 one by one.
2. Write the copy buffer, `LearnCopy` and the lookups of the worker. Check: `copies_test.go` of `internal/delivery`
   covers the three orders of copy and Publication; `copies_test.go` of `internal/telegram` covers the automatic copy,
   the comment, `edit_date`, `edited_message` and other chat messages.
3. Write the Thread states, the copy wait, the chain and the lost Thread, and the adapter's `Reply`. Check:
   `threads_test.go`, `outcomes_test.go` and `adapter_test.go` cover C-14.AC-2, AC-15, AC-16 and the Quiet and Loud
   replies with a manual clock.
4. Register the handler, extend the pruning task and the end-to-end test. Check: `leader_test.go` covers the pruning;
   Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
# as in S-042: API, FAM, FTG, CH, GR, the session (`jar`, `H`), NOTIFY, ADV, AG, MSGS, the Telegram Destination
# "alerts" (D) on the Route "tg" of the Integration "lab"
# C-14.AC-1: a channel post, then a reply to its copy
curl -s -X PUT $FAM/groups/t1 -d '{"receiver":"lab","route":"{}","labels":{"alertname":"CertExpiry"}}' > /dev/null
curl -s -X PUT $FAM/groups/t1/alerts/a -d '{"labels":{"team":"tg","domain":"a.example.org"}}' > /dev/null
NOTIFY t1 '{"reason":"first notification"}'; sleep 2; G=$(AG 'domain%3D%22a.example.org%22')
POST=$(MSGS $CH '.[0].message_id')
curl -s -X PUT $FAM/groups/t1/alerts/b -d '{"labels":{"team":"tg","domain":"b.example.org"}}' > /dev/null
NOTIFY t1 '{"reason":"new alerts added"}'; sleep 1
MSGS $GR '[.[] | select(.from.id == 123456) | {reply: (.reply_parameters.message_id != null), thread: .message_thread_id != null}] | last'   # {"reply":true,"thread":true}

# C-14.AC-15: two edits made the fake send an edited_message for the copy each; none of them changed anything
curl -s "${H[@]}" -X POST $API/alert-groups/$G/acknowledge > /dev/null; sleep 1
curl -s "${H[@]}" -X POST $API/alert-groups/$G/snooze -d '{"until":"2030-01-01T00:00:00Z"}' > /dev/null; sleep 1
curl -s -b jar "$API/alert-groups/$G/timeline?kind=delivery" | jq -c '[.items[].delivery_event]'   # ["publication"]
curl -s $FTG/requests | jq '[.[] | select(.path | endswith("/editMessageText"))] | length'          # 2   (Acknowledge and Snooze)

# C-14.AC-19, FR-6: every later event went to the group or edited the post; the reply about b was Loud
MSGS $CH 'length'                                                            # 1
MSGS $GR '[.[] | select(.from.id == 123456 and (.text | test("b\\.example\\.org")))][0].disable_notification // false'   # false

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

# pruning: a day later the copies are gone
ADV 90000; sleep 2
psql "$MUSTER_DATABASE_URL" -Atc "SELECT count(*) FROM telegram_post_copies"   # 0
curl -s localhost:8082/metrics | grep -c 'muster_short_lived_rows_pruned_total{table="telegram_post_copies"}'   # 1
```

`test/e2e/telegram_test.go` repeats these steps and adds the order of copy and Publication (a copy delayed past the
answer to `sendMessage`, and one that arrives first), and the notification model of the fake (a post's copy notifies
`member` with sound; a Thread reply notifies `member` only).

**Optional manual check against a real server** (the operator's test bot and channel with comments enabled): see new
Alerts appear as comments under the post; delete the copy in the group and see the next reply arrive outside the
Thread. Record the delay of the automatic copy (P-30) in the pull request.

## Open questions

None.

## Notes

- Suggested commit: `feat(telegram): add comment threads with the copy buffer and unattached replies`.
- The copy may arrive before the answer to `sendMessage` and on another replica; `telegram_post_copies` is the meeting
  point, so the order never matters.
- P-30 stays provisional: only the test environment can confirm it.
- Until S-067 merges, the update router holds the Connection's row locked while this story's handler runs; the
  handler is one short transaction and touches no `connections` row, so a save of the Connection and a poller wait for
  each other at most for that transaction.
- Split from S-042 together with S-067 before its implementation; S-043 depends on this story for "Thread not
  attached".

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-14.FR-2 | partial | everything but Root messages goes to the Thread; the Destination and the channel posts are S-042 |
| C-14.FR-3 | full | |
| C-14.FR-6 | partial | Quiet Thread replies; Quiet Root messages are S-042 |
| C-14.AC-1 | full | |
| C-14.AC-2 | full | |
| C-14.AC-15 | full | |
| C-14.AC-16 | partial | the API; "Thread not attached" on the page is S-043 |
| C-14.AC-19 | full | |
| C-11.FR-7 | partial | Quiet Thread replies in Telegram |
| C-11.FR-16 | partial | "Thread not attached" |
| C-11.FR-21 | partial | `thread_not_attached` |
| C-12.FR-8 | partial | Mentions in Telegram Thread replies |
| C-01.FR-13 | partial | the fake Telegram server's copies, comments, deleted copies and Thread notifications |
