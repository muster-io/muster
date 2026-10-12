---
id: S-068
title: Reminder buttons in Mattermost and Telegram (BE)
capability: C-17
kind: be
layer: L1
depends_on: [S-049]
covers: [C-17.FR-4, C-17.FR-6, C-17.FR-10, C-17.AC-7, C-13.FR-4, C-14.FR-4]
files_touched:
  - internal/buttons/buttons.go
  - internal/buttons/buttons_test.go
  - internal/delivery/render.go
  - internal/delivery/render_test.go
  - internal/delivery/outcomes.go
  - internal/delivery/outcomes_test.go
  - internal/delivery/presses.go
  - internal/delivery/presses_test.go
  - internal/delivery/query.sql
  - internal/messages/replies.go
  - internal/messages/render_test.go
  - internal/messages/texts/en.json
  - internal/messages/texts/ru.json
  - internal/mattermost/layout.go
  - internal/mattermost/layout_test.go
  - internal/mattermost/adapter.go
  - internal/mattermost/callback.go
  - internal/mattermost/callback_test.go
  - internal/telegram/layout.go
  - internal/telegram/layout_test.go
  - internal/telegram/adapter.go
  - internal/telegram/presses.go
  - internal/telegram/presses_test.go
  - test/e2e/reminder_buttons_test.go
acceptance:
  - "[C-17.FR-4, C-17.AC-7, C-13.FR-4, C-14.FR-4] Reminder Thread replies carry the buttons \"Still on it\" and \"Unack\" in the fake Mattermost server (one attachment on a post whose `root_id` is the Root message) and in the fake Telegram server (an inline keyboard on one row, on the reply in the discussion group); each reply's `message_id` and `button_key_id` are stored with it, and reply buttons are never re-rendered."
  - "[C-17.FR-10, C-13.FR-4, C-14.FR-4] A press on \"Still on it\" from the Owner's linked account records `reminder_answered` for that User with the Transport `mattermost` or `telegram` and does not move the next Reminder; a press on \"Unack\" unacknowledges the Alert Group; the Telegram callback answers are \"Done: Still on it\" and \"Done: Unacknowledge\"."
  - "[C-17.AC-7, C-13.FR-4] In Mattermost every private answer to a press on a Reminder reply — a refusal, the \"not linked\" text, or \"Done: Still on it\", the only successful press that gets an answer — first goes as an ephemeral post whose `root_id` is the Root message; when that post is refused with `403` (a bot without `create_post_ephemeral`, the fake server's default) or fails, the same text goes into the answer's `ephemeral_text`, which the server shows to that person in the Thread of the Root message (F-025, F-062, D284)."
  - "[C-13.FR-4, C-14.FR-4] A press on a reply is bound to it: in Mattermost `post_id` must be the `message_id` of a `reminder` reply of that Alert Group to a Destination of this Connection and `channel_id` that Destination's channel; in Telegram the press must come from that reply's chat and message; a mismatch is answered like a bad signature and changes nothing."
  - "[C-17.FR-6] A press on a Reminder button whose key is not in the Keyring is answered privately with \"This button has expired; use the buttons on the Alert Group's message\" and changes nothing, while a Root message button in the same case keeps the answer \"This button could not be verified; nothing was changed.\" (tests with a Keyring that lacks the signing key; S-055 repeats it live after a real rotation)."
verify: "make ci test-integration e2e"
operator_attention: true
issue: 197
---

# S-068. Reminder buttons in Mattermost and Telegram (BE)

## Scope

**IN**

- The buttons "Still on it" and "Unack" on Reminder Thread replies in Mattermost and Telegram, signed when the reply is
  rendered, with the reply's `message_id` and `button_key_id` stored.
- Presses on Thread replies: their binding to the reply, "Still on it" and Unacknowledge as the linked User, and the
  private answers, in Mattermost inside the Thread of the Root message.
- The answer to a Reminder button whose key has left the Keyring.

**OUT**

- The Reminders themselves, their schedule, the answer rule and `answerReminder` (S-049).
- Creating Account links (S-051), which verifies these presses with links made through the profile; here presses come
  from link rows set up directly, as in S-061 and S-067.
- The expired-button answer after a real key rotation (S-055).

## Contracts

- **Operations**: none new. Presses arrive through the Mattermost callback of S-061 (`mattermostAction`, ingest
  listener) and the Telegram updates of S-067; both run the Commands of the dispatcher of S-032 and S-049 with the
  Transport of the messenger.
- **Tables**: `thread_replies` (`message_id`, `button_key_id` of `reminder` replies), `deliveries` (the Root message
  that a reply belongs to), `account_links` (rows set up directly). No migration is needed; an optional index for the
  binding lookup by a reply's `message_id` is migration `0007` (or the next free number, if S-049 took it), with
  `schema.md` updated and the file added to this list.
- **Reminder buttons** (C-17.FR-4, FR-6, ADR-0011; `internal/buttons`, `delivery/render.go`, `messages/replies.go`): a
  Reminder reply has the buttons "Still on it" (`button.stillOnIt`) and "Unack" (`button.unacknowledge`), whose action
  ids carry the subject kind `reply` (`buttons.SubjectReply`), the Alert Group's `public_id` and the command
  `still_on_it` or `unacknowledge`, signed with the active key when the reply is rendered. The delivery worker stores
  the reply's `message_id` (already recorded by `RecordReplySent`) and the `button_key_id` of its signature in
  `thread_replies` with its outcome (`outcomes.go`). Reply buttons are never re-rendered: a key rotation re-signs Root
  messages only (S-055).
- **Expired buttons** (C-17.FR-6): `buttons.Verify` tells an action id whose key prefix names no key of the Keyring
  (`ErrExpired`) from one whose signature does not match or that is malformed (`ErrInvalid`). A `reply` action id with
  `ErrExpired` is answered "This button has expired; use the buttons on the Alert Group's message" (`press.expired`,
  new in `texts/en.json` and `texts/ru.json`); a `root` action id, and every `ErrInvalid`, keeps S-061's "This button
  could not be verified; nothing was changed." The key id prefix is part of what is signed, so an action id whose key
  id was edited fails as `ErrInvalid`, not as expired: the tests use a Keyring that lacks the signing key.
- **Mattermost** (C-13.FR-4; `layout.go`, `adapter.go`, `callback.go`): a Reminder reply is a post with `root_id` and
  one attachment holding the text and the two buttons (the action ids `stillonit` and `unack` of `layout.go`, the same
  `integration` URL and `context` as on Root messages); the Owner's Mention stays in `message`. A press on a reply is
  bound to it (`delivery/presses.go`, beside `PressBinding` of S-061): `post_id` must be the `message_id` of a
  `reminder` reply of that Alert Group to a Destination of this Connection, and `channel_id` that Destination's
  channel. Every private answer to a press on a reply — a refusal, the "not linked" text, a failure, or "Done: Still on
  it" (`press.done` with the new `command.stillOnIt`, since nothing else shows that answer) — goes first as a
  `POST /api/v4/posts/ephemeral` with the Root message as `root_id`, through the interactive path within
  `delivery.interactive_budget`; when that post is refused with `403` (`create_post_ephemeral`, F-063) or fails, the
  same text goes into the answer's `ephemeral_text` with `skip_slack_parsing`, which the server shows to that person,
  from System, in the Thread of the Root message (F-025, F-062; D284). A successful Unack gets an empty answer, as in
  S-061: the edited Root message shows it.
- **Telegram** (C-14.FR-4; `layout.go`, `adapter.go`, `presses.go`): a Reminder reply in the discussion group carries
  an inline keyboard with "Still on it" and "Unack" on one row; a press on it must come from that reply's chat and
  `message_id` (`delivery.TelegramPressBinding` extended to replies); `answerCallbackQuery` answers "Done: Still on
  it", "Done: Unacknowledge" (`press.done` with `command.unacknowledge`), the refusal or the expiry text.
- **Fake servers**: no change; the fake Mattermost server accepts presses on Thread replies (F-055) and shows an
  answer's `ephemeral_text` in the Thread, and the fake Telegram server presses any message of the discussion group.
- **Defaults**: `delivery.interactive_budget`.

## Steps

1. Add `ErrExpired` and the expiry answer. Check: `buttons_test.go` tells an unknown key from a bad signature; the
   press tests of both adapters answer the expiry text for a `reply` action id and the "could not be verified" text for
   a `root` one.
2. Render and sign the Reminder buttons and store `button_key_id`. Check: `render_test.go`, `layout_test.go` of both
   adapters and `outcomes_test.go`.
3. Bind presses to replies and answer them. Check: `presses_test.go`, `callback_test.go` and the Telegram
   `presses_test.go` cover a wrong post, a wrong channel or chat, the `root_id` of the Mattermost answers with and
   without `create_post_ephemeral`, and both commands.
4. Write `test/e2e/reminder_buttons_test.go`. Check: Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-049: the Admin's Personal access token A, bob's BPAT in B, the Integration "lab" with FIRE and ADV, the
# On-call policy in P, the Route "t" with the Mattermost Destination "alerts" (D, channel ch-alerts) and the Telegram
# Destination "alerts" (TD, discussion group GR); bob's Mattermost link row (u-bob) as in S-061 and his Telegram link
# row (5001) as in S-067, set up directly.
API=localhost:8080/api/v1; FMM=127.0.0.1:18065/_fake; FTG=127.0.0.1:18081/_fake; GR=-1001000000002
G=$(FIRE r1 HostDown t); curl -s "${B[@]}" -X POST $API/alert-groups/$G/acknowledge > /dev/null; ADV 14400; sleep 1
ROOT=$(curl -s $FMM/posts | jq -r --arg g "$G" '.[] | select(.root_id == "" and (.props.attachments[0].title_link | test($g))) | .id')
RP=$(curl -s $FMM/posts | jq -r --arg r "$ROOT" '[.[] | select(.root_id == $r and ((.props.attachments[0].actions // []) | length) == 2)] | last | .id')

# C-17.AC-7, C-17.FR-4: the buttons on the Reminder in Mattermost; the reply's message id and key are stored
curl -s $FMM/posts | jq -c --arg rp "$RP" '.[] | select(.id == $rp) | [.props.attachments[0].actions[] | {id, name}]'
# [{"id":"stillonit","name":"Still on it"},{"id":"unack","name":"Unack"}]
psql "$MUSTER_DATABASE_URL" -Atc "SELECT message_id = '$RP', button_key_id IS NOT NULL FROM thread_replies WHERE event = 'reminder' AND message_id = '$RP'"   # t|t

# C-17.AC-7, C-13.FR-4: an unlinked press, then bob's "Still on it"; the answers go to the Thread of the Root message
curl -s -X POST $FMM/press -d "{\"post_id\":\"$RP\",\"action_id\":\"stillonit\",\"user_id\":\"u-alice\"}" | jq -r '.answer.ephemeral_text | test("not linked")'   # true
curl -s $FMM/ephemeral | jq -c --arg r "$ROOT" '.[-1] | {u: .user_id, from, root: (.root_id == $r)}'   # {"u":"u-alice","from":"System","root":true}
curl -s -X POST $FMM/press -d "{\"post_id\":\"$RP\",\"action_id\":\"stillonit\",\"user_id\":\"u-bob\"}" > /dev/null; sleep 1
curl -s $FMM/ephemeral | jq -c --arg r "$ROOT" '.[-1] | {m: .message, root: (.root_id == $r)}'   # {"m":"Done: Still on it","root":true}
curl -s "${A[@]}" "$API/alert-groups/$G/timeline?kind=timers&limit=1" | jq -c '.items[0] | {event, t: .actor.transport, u: .actor.name}'
# {"event":"reminder_answered","t":"mattermost","u":"bob"}
# an admin bot: the same answer is an ephemeral post made by the bot with the Root message as root_id
curl -s -X PUT $FMM/config -d '{"bot_system_admin":true}' > /dev/null
curl -s -X POST $FMM/press -d "{\"post_id\":\"$RP\",\"action_id\":\"stillonit\",\"user_id\":\"u-alice\"}" | jq -c .answer   # {}
curl -s $FMM/ephemeral | jq -c --arg r "$ROOT" '.[-1] | {from: (.from != "System"), root: (.root_id == $r)}'   # {"from":true,"root":true}
curl -s -X PUT $FMM/config -d '{"bot_system_admin":false}' > /dev/null

# C-13.FR-4: a press bound to another post is refused like a bad signature
CTX=$(curl -s $FMM/posts | jq -c --arg rp "$RP" '.[] | select(.id == $rp) | .props.attachments[0].actions[0].integration.context')
curl -s -X POST localhost:8081/api/v1/callbacks/mattermost/$C -d "{\"user_id\":\"u-bob\",\"channel_id\":\"ch-alerts\",\"post_id\":\"$ROOT\",\"context\":$CTX}" | jq -r .ephemeral_text
# This button could not be verified; nothing was changed.

# C-14.FR-4: the same Reminder in the Telegram discussion group; bob's Unack is answered
TR=$(curl -s "$FTG/messages?chat=$GR" | jq -c '[.[] | select(.reply_markup.inline_keyboard != null)] | last')
jq -c '.reply_markup.inline_keyboard | map(map(.text))' <<<"$TR"   # [["Still on it","Unack"]]
curl -s -X POST $FTG/press -d "{\"chat\":$GR,\"message_id\":$(jq .message_id <<<"$TR"),\"from\":{\"id\":5001},\"button\":\"Unack\"}" > /dev/null; sleep 1
curl -s $FTG/answers | jq -r '.[-1].text'                   # Done: Unacknowledge
curl -s "${A[@]}" $API/alert-groups/$G | jq -r .status       # firing
```

`test/e2e/reminder_buttons_test.go` repeats these steps and adds: "Still on it" through Telegram ("Done: Still on it"
and `reminder_answered` with the Transport `telegram`), a Telegram press whose data comes from the Reminder of another
Alert Group (`data_from`), refused like a bad signature, and C-17.FR-6: the harness restarts the runtime with a Keyring
that no longer holds the key of a Reminder sent before, and a press on that Reminder is answered "This button has
expired; use the buttons on the Alert Group's message" in both messengers, while a press on a Root message signed with
the same key is answered "This button could not be verified; nothing was changed."

**Optional manual check against a real server** (the operator's test Mattermost 11.2.2): with a Destination on the test
channel, let one Reminder reach the channel and confirm by eye that its buttons show in the Thread and that a press on
"Still on it" shows the answer inside the Thread. With the test stand's bot, which has the role Member and whose
Connection check warns `press_answers_in_thread` (F-064), the answer is the `ephemeral_text` from System. Record the
result in the pull request; it closes the Mattermost part of
[L1 open question 6](../prd/L1.md#51-test-environment-facts) and the Pending entry of the facts.

## Open questions

None.

## Notes

- Suggested commit: `feat(timers): add still on it and unack buttons to reminders in mattermost and telegram`.
- Split from S-049 by the plan review of the phase (D298), before either was implemented, because with the files its
  contract had missed S-049 touched about 58 files. S-049 keeps the Reminders as plain Thread replies and
  `answerReminder`; this story adds the buttons and the presses. S-051 depends on this story for the Reminder presses
  of C-18.AC-5.
- A Connection whose check warns `press_answers_in_thread` (C-13.FR-2, F-064) answers every press in the Thread of the
  Root message; for a press on a Reminder that is the Thread the reply is in, so the warning changes nothing here.
- `operator_attention`: the optional check of Reminder buttons in a real Mattermost client.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-17.FR-4 | partial | the buttons "Still on it" and "Unack" on Reminders; the Reminders are S-049 |
| C-17.FR-6 | full | the expiry rule with a Keyring that lacks the key; S-055 repeats it after a real rotation |
| C-17.FR-10 | partial | "Still on it" from the messenger buttons; the API is S-049, the page S-050 |
| C-17.AC-7 | full | |
| C-13.FR-4 | partial | presses on Thread replies and their answers in the Thread |
| C-14.FR-4 | partial | presses on Thread replies in the discussion group |
