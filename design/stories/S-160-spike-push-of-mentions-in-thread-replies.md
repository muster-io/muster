---
id: S-160
title: "Spike: push of Mentions in Thread replies to a phone in Mattermost, Slack and Telegram"
capability: C-32
kind: docs
layer: L2
depends_on: []
covers: [C-32.FR-11]
files_touched:
  - design/facts.md
  - design/stories/S-161-documentation-escalation-chains-notification-rules-and-how-an-alert-reaches-people.md
acceptance:
  - "[C-32.FR-11] `design/facts.md` has a new entry F-NNN that says whether a bot's Thread reply with an `@username` Mention of a member reaches that member's phone as a push in the Mattermost mobile app through the push proxy, with `PushNotificationContents` `full` and with `generic`, for a member who follows the Thread and one who does not, what the push shows in each case, and the server, app and proxy versions."
  - "[C-32.FR-11] `design/facts.md` has a new entry F-NNN that says whether a bot's Thread reply with `<@U…>` reaches the member's phone as a push in the Slack mobile app, with the member's default notification settings and with \"Mentions\" only for the channel, and the app version."
  - "[C-32.FR-11] `design/facts.md` has a new entry F-NNN that says whether the bot's comment in a channel's discussion group with a `tg://user?id=` Mention reaches a member's phone as a push, when the member did not mute the group and when they muted it — citing S-132's entry where it already settled the muted case — with the client versions."
  - "[C-32.FR-11] The item \"Push of Mentions in Threads\" leaves Pending in `design/facts.md`; S-161's contract says, for each messenger the facts confirm, that a Page by Mention reaches a phone at night, and keeps \"only a Notification gateway is reliable at night\" for each one they do not, citing the entries in the pages it completes; any requirement the facts contradict is proposed for change in the pull request."
verify: "make lint"
operator_attention: true
issue: null
---

# S-160. Spike: push of Mentions in Thread replies to a phone in Mattermost, Slack and Telegram

## Scope

**IN**

- The check of L2.md §5.1 and of Pending in facts.md that the documentation of Escalation chains relies on: whether a
  Mention in a Thread reply pushes to a person's phone in Mattermost (push proxy, `PushNotificationContents` `full` and
  `generic`), Slack and Telegram, a muted discussion group included, recorded as facts on the maintainer's test stands
  with a phone.
- The corrections of S-161's contract that the facts call for; S-161 then brings them into the documentation pages.

**OUT**

- Telegram's `getChatMember` for people: S-132, which may already have recorded the muted discussion group; this spike
  cites that entry instead of repeating it.
- The documentation itself: S-161. Pages by Mention: S-155.

## Contracts

- **Facts** (`design/facts.md`): new entries in the sections Mattermost, Slack and Telegram, numbered after the highest
  `F-NNN` when the spike merges, each with the date, the method, the server and client versions, and "Used in
  C-32.FR-11, C-32.FR-20"; the item "Push of Mentions in Threads" leaves Pending.
- **Story contracts**: S-161 states, per messenger, whether a Page by Mention can be relied on at night, and cites the
  entries in "How a Page reaches a person" and in Messengers → Notification gateways. Until it merges, the pages say
  that at night only a Notification gateway is reliable (C-30.FR-11, C-32.FR-20).

## Steps

1. The maintainer runs the checks of Verification with a phone, with scripts an agent may prepare outside the
   repository. Check: the observations of each step are written down — what the lock screen shows, or that nothing
   arrived — with the tokens removed.
2. Write the facts and move the item out of Pending. Check: `make lint`.
3. Correct S-161's contract. Check: `scripts/stories-sync --dry-run` parses the files.

## Verification

On the maintainer's test stands, with one phone signed in as a test person (`alice`) and a second account (`bob`) that
posts nothing; the phone locked, its notification settings at their defaults unless a step says otherwise; record every
version:

1. **Mattermost**, with push notifications on through the push proxy and `PushNotificationContents` `full`: the bot
   posts a Root message in a channel where `alice` is a member, then a Thread reply `@alice you are needed`. Record what
   the phone shows. Then `alice` replies once in that Thread (so that she follows it), and the bot replies again; record
   it.
2. Repeat step 1 with `PushNotificationContents` `generic`; record what the push shows instead of the text.
3. **Slack**: the bot posts a Root message in a channel where `alice` is a member, then a Thread reply with `<@U…>` of
   `alice`; record the push with her default settings, then with the channel set to "Mentions" only, and with the Thread
   followed and not followed.
4. **Telegram**: the bot posts in a test channel with comments on; under its automatic copy in the discussion group,
   the bot comments with `<a href="tg://user?id=…">Alice</a>`; record the push with the group not muted. For the muted
   group, cite S-132's entry if it exists; otherwise mute it and record the push too.
5. For each step, record whether the push arrived within a minute, what it shows, and whether `bob` (not mentioned)
   received one.

The record of each step goes into the facts; tokens, phone numbers and the accounts' real names never go into the
repository.

## Open questions

None; the spike answers them.

## Notes

- Suggested commit: `docs: record whether mentions in thread replies push to phones`.
- **Maintainer** (operator attention): the checks need the Mattermost test stand with the push proxy and both settings
  of `PushNotificationContents`, a Slack test workspace, the Telegram test channel with its discussion group, and a
  phone with the three apps; only the maintainer can run them. An agent may write the scripts that post the messages
  outside the repository and run them on the maintainer's instructions.
- `covers` names C-32.FR-11 in part: the spike settles its "push to phones is still to be verified"; the documentation
  that depends on it is S-161's.
- No example in the repository names a real person; the test accounts are `alice` and `bob`.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-32.FR-11 | partial | the push facts; the table in the documentation is S-155 and S-161 |
