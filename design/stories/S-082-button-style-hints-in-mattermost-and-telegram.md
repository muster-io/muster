---
id: S-082
title: Button style hints in Mattermost and Telegram (BE)
capability: C-12
kind: be
layer: L1
depends_on: [S-061, S-042, S-068]
covers: []
files_touched:
  - internal/buttons/buttons.go
  - internal/buttons/buttons_test.go
  - internal/messages/message.go
  - internal/messages/render_test.go
  - internal/mattermost/client.go
  - internal/mattermost/layout.go
  - internal/mattermost/layout_test.go
  - internal/mattermost/adapter_test.go
  - internal/telegram/client.go
  - internal/telegram/layout.go
  - internal/telegram/layout_test.go
  - internal/telegram/adapter_test.go
  - internal/fakes/faketelegram/chats.go
  - internal/fakes/faketelegram/faketelegram_test.go
  - test/e2e/button_styles_test.go
  - test/e2e/mattermost_test.go
  - design/facts.md
  - design/stories/README.md
  - design/stories/coverage.md
  - design/stories/S-074-bridge-adapter-envelopes-idempotent-publications-threads-and-edit-windows.md
acceptance:
  - "A new Alert Group's Root message carries styled buttons in both fake servers: in the fake Mattermost server the action `ack` has `style` `primary` and `resolve` `success`, and every `snooze<N>` action has no `style` field; in the fake Telegram server the inline button \"Ack\" has `style` `primary` and \"Resolve\" `success`, and every Snooze button has no `style` field."
  - "After Acknowledge the edited Root message carries \"Unack\" without a `style` field and \"Resolve\" with `success` in both fake servers; a snoozed Alert Group's Root message carries \"Ack\" `primary`, \"Unsnooze\" without a style and \"Resolve\" `success`."
  - "A Reminder's \"Still on it\" carries `style` `primary` and its \"Unack\" no `style` field, in the fake Mattermost server and in the fake Telegram server."
  - "One mapping decides every style, `buttons.StyleOf`: Acknowledge and \"Still on it\" `primary`, Resolve `success`, Unacknowledge, Snooze and Unsnooze `neutral`; both adapters send `primary` and `success` as they are and leave the `style` field out for `neutral`, and never send `danger` or any other value (unit tests of `internal/buttons` and of the layouts and adapters of both messengers)."
  - "The style is not stored: the JSON of a rendered message, which is the Desired state's payload and the input of its hash, has no `style`, so a Root message published before this change keeps its Desired state and shows the styles from its next edit, without a migration or a re-render (`internal/messages` test)."
  - "The fake Telegram server refuses a `sendMessage` or `editMessageText` whose inline button has a `style` other than `primary`, `danger`, `success` or `default`, a non-string included, with `400` `Bad Request: can't parse InlineKeyboardButton: Invalid button style specified` (F-067), and keeps the `style` it accepts in the message's `reply_markup` (`faketelegram_test.go`)."
verify: "make ci e2e"
operator_attention: false
issue: 244
---

# S-082. Button style hints in Mattermost and Telegram (BE)

## Scope

**IN**

- A style hint on every Command button: Acknowledge and "Still on it" `primary`, Resolve `success`, every other button
  `neutral` (D329), decided in one place, `internal/buttons`.
- The style in Mattermost, as the `style` of an attachment action (F-068), and in Telegram, as the `style` of an
  inline keyboard button (F-066), on Root messages, Reminders and Destination test messages alike; `neutral` is no
  `style` field in either messenger.
- The fake Telegram server refusing an unknown style like the Bot API (F-067).
- The facts F-066 to F-068.

**OUT**

- Style hints of Bridge buttons in the envelope (S-074), which take the same mapping (C-22.FR-5).
- Buttons that do nothing (Telegram's `DisabledButton`) and custom emoji on buttons: not used.
- Any setting for the styles: there is none.

## Contracts

- **Operations, tables, settings, metrics, log events**: none new. No migration: the style is not stored.
- **Style hint** (`internal/buttons`): `Style` with the values `primary`, `success` and `neutral` — the names of the
  Bridge protocol's `ButtonStyle` in [`api/bridge.yaml`](../../api/bridge.yaml), which also has `danger`, unused by
  Muster — and `StyleOf(command)`: `CommandAcknowledge` and `CommandStillOnIt` `primary`, `CommandResolve` `success`,
  `CommandUnacknowledge`, `CommandSnooze`, `CommandUnsnooze` and anything else `neutral`. Unresolve is never a button
  (reference.md), and `danger` is not used (D329).
- **Button model** (`internal/messages`): `Button.Style()` is `buttons.StyleOf` of the button's Command. It is a method,
  not a field: the JSON of a `Message` — the Desired state's payload, whose SHA-256 is its hash (`delivery/render.go`)
  — does not change, so no Desired state changes when this story deploys, and a Root message published before it
  shows the styles from its next edit. A Reminder reply, never edited, keeps the look it was sent with.
- **Mattermost** (`client.go`, `layout.go`): the attachment `action` gets `style` (`omitempty`); `actions` sets it to
  `primary` or `success` from `Button.Style()` and leaves it empty for `neutral`. Mattermost tints the label and the
  background (F-068); a button without `style` stays plain.
- **Telegram** (`client.go`, `layout.go`): `inlineButton` gets `style` (`omitempty`); `keyboard` sets it to `primary`
  or `success` and leaves it empty for `neutral` and for any value Telegram does not document, because an unknown
  style fails the whole send or edit (F-067). Every edit already carries the whole keyboard (C-14.FR-15), so an edit
  changes the styles with the buttons (F-066).
- **Fake servers**: the fake Telegram server checks `style` in `contentOf`, shared by `sendMessage` and
  `editMessageText`: absent, `primary`, `danger`, `success` or `default` pass, anything else answers `400`
  `Bad Request: can't parse InlineKeyboardButton: Invalid button style specified`; the accepted keyboard, `style`
  included, is kept in the message and listed by `/_fake/messages`. The fake Mattermost server already keeps a post's
  `props` as they came, `style` included, and accepts any value, as Mattermost 11.2.2 does (F-068): no change.
- **Built-in texts**: unchanged.

## Steps

1. Add `Style`, `StyleOf` and `Button.Style()`. Check: `buttons_test.go` covers every Command; the `internal/messages`
   test shows a rendered Root message's JSON without `style`.
2. Send the style in both adapters. Check: `layout_test.go` and `adapter_test.go` of both messengers assert the JSON
   of a firing, an acknowledged and a snoozed Root message and of a Reminder: `style` present on Ack, Resolve and
   "Still on it" only.
3. Extend the fake Telegram server. Check: `faketelegram_test.go` sends and edits with each documented value, with
   `default`, with an unknown string and with a number.
4. Write `test/e2e/button_styles_test.go`. Check: Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-068: the Admin's Personal access token A, bob's BPAT in B, the Integration "lab" with FIRE and ADV, the
# On-call policy in P, the Route "t" with the Mattermost Destination "alerts" (D, channel ch-alerts) and the Telegram
# Destination "alerts" (TD, channel CH with the discussion group GR).
API=localhost:8080/api/v1; FMM=127.0.0.1:18065/_fake; FTG=127.0.0.1:18081/_fake; CH=-1001000000001; GR=-1001000000002
MMROOT() { curl -s $FMM/posts | jq -c --arg g "$1" '[.[] | select(.root_id == "" and (.props.attachments[0].title_link | test($g)))] | last | [.props.attachments[0].actions[] | {id, style}]'; }
TGROOT() { curl -s "$FTG/messages?chat=$CH" | jq -c '[.[] | select(.reply_markup.inline_keyboard != null)] | last | .reply_markup.inline_keyboard | map(map({text, style}))'; }

# a new Alert Group: Ack primary, Resolve success, Snooze plain
G=$(FIRE r1 HostDown t); sleep 1
MMROOT $G   # [{"id":"ack","style":"primary"},{"id":"resolve","style":"success"},{"id":"snooze0","style":null},…]
TGROOT      # [[{"text":"Ack","style":"primary"},{"text":"Resolve","style":"success"}],[{"text":"Snooze 1 h","style":null},…]]

# acknowledged: Unack plain, Resolve success
curl -s "${B[@]}" -X POST $API/alert-groups/$G/acknowledge > /dev/null; sleep 1
MMROOT $G   # [{"id":"unack","style":null},{"id":"resolve","style":"success"},{"id":"snooze0","style":null},…]
TGROOT      # [[{"text":"Unack","style":null},{"text":"Resolve","style":"success"}],[…]]

# a Reminder: "Still on it" primary, Unack plain
ADV 14400; sleep 1
curl -s $FMM/posts | jq -c '[.[] | select(.root_id != "" and ((.props.attachments[0].actions // []) | length) == 2)] | last | [.props.attachments[0].actions[] | {id, style}]'
# [{"id":"stillonit","style":"primary"},{"id":"unack","style":null}]
curl -s "$FTG/messages?chat=$GR" | jq -c '[.[] | select(.reply_markup.inline_keyboard != null)] | last | .reply_markup.inline_keyboard | map(map({text, style}))'
# [[{"text":"Still on it","style":"primary"},{"text":"Unack","style":null}]]

# the fake Telegram server refuses an unknown style (F-067)
curl -s -X POST "127.0.0.1:18081/bot123456:dev-telegram-token/sendMessage" -H 'Content-Type: application/json' \
  -d "{\"chat_id\":$CH,\"text\":\"x\",\"reply_markup\":{\"inline_keyboard\":[[{\"text\":\"a\",\"callback_data\":\"a\",\"style\":\"neutral\"}]]}}" | jq -c '{ok, error_code, description}'
# {"ok":false,"error_code":400,"description":"Bad Request: can't parse InlineKeyboardButton: Invalid button style specified"}
```

The last call uses the published token of the development Telegram bot (`devmode.TelegramConnectionBotToken`).
`test/e2e/button_styles_test.go` repeats these steps and adds a snoozed Alert Group: "Ack" `primary`, "Unsnooze" plain
and "Resolve" `success` in both messengers.

## Open questions

None.

## Notes

- Suggested commit: `feat(delivery): style the acknowledge, resolve and still-on-it buttons`.
- The maintainer's mapping (D329), the same as the style hints of Bridge buttons in C-22.FR-5 and reference.md:
  Acknowledge `primary`, Resolve `success`, "Still on it" in a Reminder `primary`; Snooze, Unacknowledge, Unsnooze and
  the Reminder's Unack plain; `danger` is not used. "Neutral" is no `style` field in both messengers: in Telegram any
  value outside the documented three fails the request (F-067), and in Mattermost `default` colours the label in the
  channel's text colour, while no field leaves the button plain (F-068).
- This story covers no FR or AC. The button rendering belongs to C-12.FR-1 (item 12), C-13.FR-3, C-14.FR-15 and
  C-17.FR-4, and none of them, nor any other requirement of L1, says how a button looks in Mattermost or Telegram; only
  C-22.FR-5 gives Bridge buttons their style hints. The acceptance statements therefore name no IDs. A one-line mention
  of the styles of Mattermost and Telegram in reference.md ("Buttons and links by status") is proposed in the pull
  request for the maintainer to decide.
- The look was checked by eye on the test stand (D329): in Telegram the styled buttons are filled blue and green; in
  Mattermost the label and a light background take the colour. Mattermost 12 draws attachments as Mattermost Blocks;
  after the stand is upgraded F-068 is checked again (facts, Pending).
- Added after the Bridge stories were written (D320, D329): the Bridge protocol brought in style hints, and the native
  messengers waited for their facts from the test environment.

## Coverage

| ID | Covered | Note |
|---|---|---|
| — | — | no FR or AC of L1 speaks about the look of buttons in Mattermost and Telegram; see Notes |
