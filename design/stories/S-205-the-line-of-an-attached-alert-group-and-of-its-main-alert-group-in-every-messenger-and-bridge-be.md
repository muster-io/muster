---
id: S-205
title: The line of an attached Alert Group and of its main Alert Group in every messenger and bridge, with the Thread replies and the answers to presses (BE)
capability: C-37
kind: be
layer: L2
depends_on: [S-203, S-126, S-168, S-113, S-081, S-075, S-100, S-067]
covers: [C-37.FR-1, C-37.FR-7, C-37.FR-10, C-37.FR-11, C-37.FR-15, C-37.AC-1, C-37.AC-5, C-37.AC-6, C-37.AC-7, C-37.AC-8]
files_touched:
  - internal/messages/layout.go
  - internal/messages/layout_test.go
  - internal/messages/render.go
  - internal/messages/render_test.go
  - internal/messages/replies.go
  - internal/messages/replies_test.go
  - internal/messages/texts/en.json
  - internal/messages/texts/ru.json
  - internal/groups/dispatcher.go
  - internal/groups/attach.go
  - internal/groups/commands.go
  - internal/groups/commands_test.go
  - internal/delivery/render.go
  - internal/delivery/render_test.go
  - internal/delivery/enqueue.go
  - internal/delivery/enqueue_test.go
  - internal/delivery/threads.go
  - internal/delivery/threads_test.go
  - internal/delivery/rerender_integration_test.go
  - internal/buttons/buttons_test.go
  - internal/bridge/delivery.go
  - internal/bridge/delivery_test.go
  - internal/bridge/commands_test.go
  - internal/bridge/presses.go
  - internal/bridge/presses_test.go
  - internal/mattermost/callback.go
  - internal/mattermost/callback_test.go
  - internal/telegram/presses.go
  - internal/telegram/presses_test.go
  - internal/slack/presses.go
  - internal/slack/presses_test.go
  - test/e2e/attach_messages_test.go
  - test/e2e/bridge_layout_test.go
acceptance:
  - "[C-37.FR-10, C-37.AC-1] Attaching `#1288` to `#1287` edits the Root message of `#1288` in the fake Mattermost server to \"Attached to #1287 PostgresPrimaryDown\" in the Route's language, with a link to the page of `#1287` in Muster and without buttons, Quietly, and posts one Quiet Thread reply \"Attached to #1287 by @ivan; it follows the status of #1287.\" with no Mention; the same holds in the fake Telegram server (`disable_notification`) and the fake Slack server, and the `layout` the fake bridge receives carries the same line as its one notice and the link to `#1287`, with no fields, labels, Alerts or people (`render_test.go`, `layout_test.go`, `delivery_test.go`, `test/e2e/attach_messages_test.go`, `test/e2e/bridge_layout_test.go`)."
  - "[C-37.FR-10, C-37.AC-6] Detaching `#1290` while `#1287` is acknowledged by Ivan edits its Root message back to the layout of its status with the buttons of that status and of its Destination's button set (Unack, Resolve and Snooze), and posts a Quiet Thread reply \"Detached from #1287 by @ivan\", or for a system detach the reason \"#1287 resolved by itself\"; a Bridge Root message past its edit window is republished as on a change of status (`render_test.go`, `enqueue_test.go`, `threads_test.go`, `test/e2e/attach_messages_test.go`)."
  - "[C-37.FR-10, C-37.FR-7, C-37.AC-7] Every other event recorded on an attached Alert Group changes only its line in the Root message and posts no Thread reply; a Reminder or ack timeout notice already queued for it is dropped and never sent; a Destination it reaches meanwhile receives the line Quietly, with its `destination_joined` Quiet (`enqueue_test.go`, `threads_test.go`, `test/e2e/attach_messages_test.go`)."
  - "[C-37.FR-10, C-37.AC-5] The Root message of an Alert Group that the main Alert Group's resolve resolved is edited to the layout of its resolved status with \"Resolved with #1287\", and a `status_followed` on a resolve shows no Thread reply (`render_test.go`, `test/e2e/attach_messages_test.go`)."
  - "[C-37.FR-11, C-37.AC-8] The Root messages of a main Alert Group show, Quietly, the line \"Attached: #1288, #1290\" with up to five numbers and \"+{K} more\", without links, and nothing is written in its Thread for an attachment; a press of Ack on the Root message of `#1287` in the fake Mattermost, Telegram and Slack servers and through the fake bridge acknowledges `#1288` too, and the private answer says \"Acknowledged #1287 and 1 attached Alert Group\" in the language of the Route, with the Russian plural (`render_test.go`, `replies_test.go`, `callback_test.go`, `presses_test.go`, `test/e2e/attach_messages_test.go`)."
  - "[C-37.FR-1] A messenger has no button and no Text command for Attach or Detach: the button sets, the Root message of any status and the Text commands of Bridges and Telegram offer none, and a Text command `attach` is answered as an unknown command (`buttons_test.go`, `commands_test.go`)."
  - "[C-37.FR-15] The messenger column of the table of FR-15 is as it says — `attached` a Root message edit and a Thread reply, `alert_group_attached` a Root message update, `status_followed` an update of its line and on a resolve the Root message of its resolved status, `detached` an edit back and a Thread reply, `alert_group_detached` a Root message update — checked row by row in a table-driven test (`enqueue_test.go`)."
verify: "make ci test-integration e2e-go"
operator_attention: false
issue: null
---

# S-205. The line of an attached Alert Group and of its main Alert Group in every messenger and bridge, with the Thread replies and the answers to presses (BE)

## Scope

**IN**

- The Root message of an attached Alert Group as one line that names its main Alert Group, with a link and no buttons, in
  Mattermost, Telegram, Slack and Bridges, its `layout` for bridges, and its edit back on Detach (C-37.FR-10).
- The Thread replies of Attach and Detach, the dropping of Reminders and ack timeout notices of an attached Alert Group,
  and what every other event on it shows: only an update of its line (C-37.FR-7, FR-10).
- The line "Attached: #1288, #1290" on the Root messages of a main Alert Group, the buttons that act through it, and the
  private answer to a press (C-37.FR-11).
- The messenger column of the table of FR-15, and that messengers offer no Attach or Detach (C-37.FR-1, FR-15).

**OUT**

- The state, the Commands, following and the endings on a resolve: S-202, S-203. The stopping of chains and timers: S-204.
- The list filter, bulk Attach, statistics and webhook events: S-206. The pages: S-207. The documentation: S-208.

## Contracts

- **Message** (`internal/messages/render.go`, `layout.go`, `replies.go`, `texts/en.json`, `texts/ru.json`): the Root
  message of an attached Alert Group is a Message of the kind Root with one body line, "Attached to #{M} {title of the
  main Alert Group}" in the language of its Route, and a link to the main Alert Group's page in Muster, with no buttons
  and no Route template applied; the Root message of a main Alert Group has, besides its layout, one Quiet line
  "Attached: #1288, #1290" with up to five numbers, then "+{K} more", without links. The texts of the replies — "Attached
  to #{M} by @{login}; it follows the status of #{M}.", "Detached from #{M} by @{login}", the system's "#{M} resolved by
  itself" and the "Resolved with #{M}" notice — are in both language files with the Russian plurals. `HasLayout` returns
  true for the attached line, so a bridge gets a `layout`.
- **Layout for bridges** (no change to `api/bridge.yaml`; see the Open question): the `layout` of an attached Root
  message has `number` and `title` of the attached Alert Group, its `status_word`, one notice with the line and one link
  to the main Alert Group, and no fields, labels, Alerts, people or summary; the bridge libraries need no change.
- **What delivery reads** (`internal/groups` → `internal/delivery/render.go`, `enqueue.go`): `groups.Rendering` and the
  view of an Alert Group carry `AttachedTo` (number, title and public id of the main Alert Group), the numbers of its
  attached Alert Groups, and `ResolvedWith`; the re-render after `attached`, `detached`, `status_followed`,
  `alert_group_attached` and `alert_group_detached` is queued for the Alert Group and for its main one; `Table` in
  `enqueue.go` takes the forms of FR-15 (`FormReplyAndUpdate` for `attached` and `detached`, `FormUpdate` for the other
  three, a `FormUpdate` that is the Root message of the resolved status for a `status_followed` on a resolve) in place
  of S-202's `FormNothing`. A Root message in a Destination reached meanwhile is a Quiet Publication of the line
  (C-26.FR-5 with C-37.FR-7).
- **Threads** (`internal/delivery/threads.go`): a Thread reply that is due for an Alert Group found attached — a
  Reminder, an ack timeout notice, an earlier Page — is dropped (`thread_replies.state` `dropped`) and never sent; the
  replies of `attached` and `detached` are never dropped for that reason.
- **Buttons** (`internal/buttons`, `internal/delivery/render.go`): an attached Alert Group's Root message has none, whatever
  its status was; the Root message of a main Alert Group keeps those of its status, and a press acts on the main Alert
  Group and, through S-203's following, on the attached ones. Attach and Detach have no code in `buttons.go` and no Text
  command in `internal/bridge/commands.go` or Telegram's command handling.
- **Answers to a press** (`internal/messages/replies.go`, `internal/mattermost/callback.go`,
  `internal/telegram/presses.go`, `internal/slack/presses.go`, `internal/bridge/presses.go`): `groups.Result` of a
  Command carries the count of attached Alert Groups that followed it, and `messages.Done` becomes `DoneFor`, which
  answers "Acknowledged #1287 and 2 attached Alert Groups" when the count is above zero and "Done: Acknowledge" as before
  otherwise; the four adapters call it with the count.
- **Fakes**: the fake Mattermost, Telegram, Slack and bridge servers already record posts, edits and layouts; no change.
- **Settings**: none. **Log events and metrics**: none new.

## Steps

1. Write the line, the layout and the texts. Check: `render_test.go`, `layout_test.go`, `replies_test.go` in English
   and Russian, with the Russian plurals of "{K} more" and "attached Alert Groups".
2. Carry the attachment through the re-render and the forms of the table. Check: `enqueue_test.go` (row by row),
   `rerender_integration_test.go`.
3. Drop the due replies of an attached Alert Group; remove the buttons. Check: `threads_test.go`, `buttons_test.go`.
4. Make the four adapters answer a press with the count. Check: `callback_test.go`, `presses_test.go` of the three
   adapters and `bridge/presses_test.go`.
5. Write `test/e2e/attach_messages_test.go` and extend `bridge_layout_test.go`. Check: the Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
API=localhost:8080/api/v1   # A: Admin; I: Ivan; the Routes "databases" and "payments" deliver to the fake Mattermost, Telegram and Slack and the fake bridge; AG1287, AG1288, AG1290 as in S-202
curl -s -H "$I" -X POST $API/alert-groups/$AG1288/attach -d "{\"main_alert_group_id\":\"$AG1287\"}" > /dev/null; sleep 2
curl -s 127.0.0.1:18065/_fake/requests | jq -r '[.[] | select(.path | test("/posts/.+$")) | select(.method == "PUT")] | last | .body.message'
# "Attached to #1287 PostgresPrimaryDown"  (and no "actions" in its props: no buttons)
curl -s 127.0.0.1:18065/_fake/requests | jq -r '[.[] | select(.path | test("/posts$"))] | last | .body.message'   # "Attached to #1287 by @ivan; it follows the status of #1287."
curl -s 127.0.0.1:18081/_fake/requests | jq -c '[.[] | select(.method=="editMessageText" or .method=="sendMessage")] | last | {t: .body.text, silent: .body.disable_notification}'
curl -s 127.0.0.1:18094/_fake/messages | jq -c '[.[] | select(.layout.number == 1288)] | last | .layout | {n: .notices, links: (.links | length), f: (.fields | length)}'
# {"n":["Attached to #1287 PostgresPrimaryDown"],"links":1,"f":0}
# the main Alert Group shows its attached ones, without a Thread reply
curl -s 127.0.0.1:18065/_fake/requests | jq -r '[.[] | select(.method=="PUT")] | map(select(.body.message | contains("1287"))) | last | .body.message' | grep -o 'Attached: #1288'
# press Ack on the main Root message through the press endpoint of the fake Mattermost server (as test/e2e/mattermost_test.go does): the private answer
# the callback's answer carries "Acknowledged #1287 and 1 attached Alert Group"
# Detach: the Root message is back with its buttons, and a Quiet reply names Ivan
curl -s -H "$I" -X POST $API/alert-groups/$AG1288/detach > /dev/null; sleep 2
curl -s 127.0.0.1:18065/_fake/requests | jq -r '[.[] | select(.path | test("/posts$"))] | last | .body.message'                # "Detached from #1287 by @ivan"
# resolving the main Alert Group by hand: the attached Root message reads "Resolved with #1287"
# a new Destination reached while attached gets the line, Quietly; a Reminder due for an attached Alert Group is never sent
# the Russian run: set the Route's language to ru and repeat: «Присоединена к #1287 …», «Подтверждены #1287 и 1 присоединённая группа алертов»
```

`test/e2e/attach_messages_test.go` repeats these checks in Mattermost, Telegram and Slack, adds the Bridge past its edit
window, the line with more than five attached Alert Groups, a Reminder that was queued before an Attach, a main Alert
Group acknowledged by a Text command of a bridge, and the absence of a Text command `attach` in each messenger that
has Text commands.

## Open questions

1. **How a bridge learns that a message is a line.** C-37.FR-10 says the `layout` sent to bridges carries the same line
   (C-29.FR-8) but the `MessageLayout` of `api/bridge.yaml` has no field for it. This contract uses `notices` and
   `links` of the existing layout, so a bridge renders the line as it renders any notice and the libraries S-127 and
   S-128 need no change; a bridge cannot tell the line from a notice, and one that drops buttons with `layout` gets none
   anyway. The maintainer confirms, or asks for a field `attached_to` on `MessageLayout`, which would add a story in
   `muster-io/muster-bridge-go` and `muster-io/muster-bridge-python`.
2. **The private answer for a Takeover.** FR-11's text names Acknowledge; this contract uses the same count for a
   Takeover ("Took over #1287 and 1 attached Alert Group"), the Snooze buttons and Unack alike, with the Command's own
   word from `command.*`. The maintainer confirms the wording.

## Notes

- Suggested commit: `feat(delivery): show an attached alert group as a line naming its main alert group in every messenger`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases. No migration.
- The line of an attached Alert Group is not rendered from the Route's `root_message` template: the template is a
  Route's own text, and the line is Muster's (ADR-0005 allows the Root message to follow its Desired state alone).
- Reminders and ack timeout notices of an attached Alert Group stop at their source (S-204); the dropping of a reply
  already queued is this story's, since delivery owns the Thread.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-37.FR-1 | full | the Commands and Permission are S-202's |
| C-37.FR-7 | full | the status, loudness and independence of the main Alert Group are S-203's |
| C-37.FR-10 | full | |
| C-37.FR-11 | full | |
| C-37.FR-15 | partial | the messenger column; the state's rows S-202, S-203; webhooks S-206 |
| C-37.AC-1 | full | the Timeline, Audit log and API half is S-202's |
| C-37.AC-5 | partial | the Root message "Resolved with #1287"; the statuses S-203, the chain S-204 |
| C-37.AC-6 | partial | the Root message edited back; the Reminder 4 hours later is S-204 |
| C-37.AC-7 | partial | the line in a new Destination; the entries S-203 |
| C-37.AC-8 | full | |
