---
id: S-132
title: "Spike: Telegram's muted discussion groups and getChatMember for people"
capability: C-27
kind: docs
layer: L2
depends_on: []
covers: [C-27.FR-8]
files_touched:
  - design/facts.md
  - design/stories/S-164-warnings-about-unreachable-on-calls.md
  - design/stories/S-137-documentation-schedules.md
  - design/stories/S-144-documentation-whom-a-message-mentions-and-team-mentions.md
acceptance:
  - "[C-27.FR-8] `design/facts.md` has a new entry F-NNN that says what `getChatMember` answers, called by the bot that is an admin of a channel's discussion group, for a person who is a member of the group, one who left it, one who was never in it but is a subscriber of the channel, and one the bot never saw — the status, the HTTP status and the `description` of each — and whether the answer depends on the person's privacy settings, with the Bot API version."
  - "[C-27.FR-8] `design/facts.md` has a new entry F-NNN that says whether a comment of the bot in the discussion group with a `tg://user?id=` Mention of a member makes a notification on that member's phone and desktop when they muted the discussion group, and when they muted it except for Mentions, if the client offers that, with the client versions."
  - "[C-27.FR-8] The item \"Telegram\" leaves Pending in `design/facts.md` for the two checks above (forum topics and plain group chats stay with the later part of L2), and S-164's contract cites the new entries: its Telegram Destination check and the fake Telegram server answer `getChatMember` for people as the facts say, and any assumption the facts contradict is corrected in it, or proposed as a change of C-27.FR-8 in the pull request; the answer about muted groups is cited by S-137 and S-144."
verify: "make lint"
operator_attention: true
issue: null
---

# S-132. Spike: Telegram's muted discussion groups and getChatMember for people

## Scope

**IN**

- The Telegram checks of L2.md §5.1 and of Pending in facts.md that the core of L2 needs: whether a Mention in a comment
  rings a member who muted the discussion group, and whether the bot can tell with `getChatMember` that a person is not
  in the discussion group, recorded as facts on the maintainer's test stand.
- The corrections of S-164's contract, and the notes of the documentation stories S-137 and S-144, that the facts call
  for.

**OUT**

- The warning about unreachable On-calls, with the Telegram Destination check that reads the discussion group's
  members: S-164.
- Push of Mentions in Thread replies in Mattermost, Slack and Telegram for people who did not mute anything: S-160.
- Forum topics and plain group chats: the later part of L2. Mattermost groups and Slack user groups: S-138.

## Contracts

- **Facts** (`design/facts.md`): new entries in the section Telegram, numbered after the highest `F-NNN` when the spike
  merges, each with the date, the method, the Bot API version and the client versions, and "Used in C-27.FR-8, C-28";
  the two checks leave the item "Telegram" of Pending, which keeps forum topics and plain group chats.
- **Story contracts**: S-164 relies on `getChatMember(discussion_group, user_id)` answering `left` (or an error) for a
  person who is not in the group; if the facts differ — for example, an error for a person the bot never saw, or an
  answer that the person's privacy settings hide — S-164's Telegram step and the fake Telegram server follow the facts,
  and C-27.FR-8 is proposed for change in the pull request if the warning cannot be computed as it says. S-137 and
  S-144 cite whether a muted discussion group still rings for a Mention.

## Steps

1. The maintainer runs the checks of Verification on the test stand, with scripts an agent may prepare outside the
   repository. Check: the observations of each step are written down with the raw Bot API answers, the bot token
   removed.
2. Write the facts and move the two checks out of Pending. Check: `make lint`.
3. Correct S-164's contract and add the citations to S-137 and S-144. Check: `scripts/stories-sync --dry-run` parses
   the files.

## Verification

On the maintainer's test stand: the bot of a Telegram Connection, an admin of a test channel with comments on and of
its discussion group, and three test accounts on two phones and a desktop client (record the Bot API version and the
client versions):

1. `alice` joins the discussion group; `bob` subscribes to the channel and never joins the group; `carol` joins the
   group and leaves it; `dora` has never written to the bot nor joined anything.
2. Call `getChatMember` with the discussion group's chat id and the user id of each, as the bot. Record the HTTP status,
   `ok`, `result.status` or `description` of each answer. Expected to learn whether a person who never joined answers
   `left`, `400 Bad Request: user not found` or something else, and whether `bob`'s subscription to the channel makes
   any difference.
3. Repeat step 2 with `alice`'s privacy setting for groups and channels changed, and record whether anything changes.
4. `alice` mutes the discussion group in her phone's client. The bot posts in the channel; under its automatic copy in
   the discussion group, the bot comments with `<a href="tg://user?id=…">Alice</a>` (`parse_mode` HTML). Record whether
   `alice`'s phone and desktop show a notification, a badge only, or nothing.
5. Where the client offers to mute a chat except for Mentions, set it and repeat step 4; record the result.
6. Repeat step 4 with the group not muted, as the baseline.

The record of each step goes into the facts; tokens, phone numbers and the accounts' real names never go into the
repository.

## Open questions

None; the spike answers them.

## Notes

- Suggested commit: `docs: record how telegram answers getchatmember for people and rings muted discussion groups`.
- The maintainer performs the checks (operator attention): they need the test channel, its discussion group, the bot
  and three test accounts with clients. An agent may write the scripts of step 2 outside the repository and run them on
  the maintainer's instructions.
- `covers` names C-27.FR-8 in part: the spike settles whether the Telegram part of the unreachable warning can be
  computed as it says; S-164 implements it. The answer about muted groups is an input for the documentation of whom a
  message mentions (S-144) and of Escalation chains (S-161), not a requirement of this capability.
- No example in the repository names a real person; the test accounts are `alice`, `bob`, `carol` and `dora`.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-27.FR-8 | partial | the facts of `getChatMember` for people; the warning is S-164 |
