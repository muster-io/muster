# C-35. Telegram chats and topics

[L2 index](../L2.md) · Release: a 0.2.x patch release · Status: Draft · UI: yes · Depends on: C-11, C-12, C-13, C-14,
C-18, C-26, C-27, C-28, C-32

**Goal.** Let a Telegram Destination be any chat that the bot can post to, not only a channel with comments: a group
chat, a supergroup, a person's private chat with the bot, or one topic of a forum, so that one forum carries a topic
per Team or per Severity. Root messages, Threads, buttons, Mentions and Pages work in each kind as they do in a channel,
within the budget Telegram gives each chat, which all the Destinations of one Connection in that chat share (FR-8;
issue #371; ADR-0005, ADR-0018, journal D499, D519, D554).

Names in the form `area.setting` refer to rows of [defaults.md](../l1/defaults.md); ids in the form `F-NNN` refer to
[verified facts](../../facts.md#telegram). Behaviour of Telegram that this file relies on and that no fact records yet
is listed under [Facts to verify](#facts-to-verify); the requirements that rely on it are marked "(to verify)" and are
confirmed or corrected by a spike before their stories, as S-132 did for discussion groups. This file is a draft: the
questions under [Open questions](#open-questions) are settled by the maintainer before the stories are written, and the
requirements that depend on one name it.

## Scenarios

1. A team already has a Telegram group chat where its people talk. The Admin creates a Destination in the mode "Chat",
   enters the group chat's id, and the check shows "Group · Payments team · the bot may send messages". A new Alert
   Group appears in the group chat as a message with buttons; its new Alerts, the ack timeout notice and the Reminders
   are replies to that message in the same chat.
2. A forum supergroup "Alerts" has the topics "Payments · critical", "Payments · notice" and "Search". The Admin creates
   one Destination per topic, names each on the Team "payments" or "search" with its Severity condition (C-26.FR-1), and
   enters the forum and the topic's link. Root messages and their Thread replies stay in their topic. During a Storm the
   three Destinations wait for one budget, the forum's, and Telegram never answers `429`.
3. Ivan, a developer, wants the Alert Groups of his own service in his private chat with the bot. He pressed Start in
   the bot when he linked his account (C-18.FR-2). The Admin creates a Destination in the mode "Chat" with Ivan's chat,
   and a Route of his service delivers there. It is a Destination like any other, not a direct message that Pages
   (ADR-0018).
4. Someone deletes the topic "Search". The next send to it is refused, the Destination becomes Broken with "The topic
   no longer exists", and `MusterDestinationBroken` reaches the admins through another Destination. The Admin points
   the Destination at a new topic (what happens to its open Alert Groups is [question 4](#open-questions)).
5. A basic group chat grows and its owner turns it into a supergroup, which gives it a new chat id. The next send to
   the old id is refused; the Destination shows "The group became a supergroup with the id -100…" and the Admin saves
   it with the new id ([question 4](#open-questions)).
6. An Escalation step Pages the On-call of "payments" in the topic "Payments · critical": the Page is a reply to the
   Root message in that topic that mentions Petr by his Telegram Account link (C-32.FR-9).

## Functional requirements

### Destination kinds

- **C-35.FR-1** A Telegram Destination has a mode. **Channel** is the Destination of L1 (C-14.FR-2): a channel with
  comments, whose Thread lives in its discussion group; every Telegram Destination saved before this capability is a
  channel Destination and keeps working unchanged. **Chat** takes any chat that the bot may send messages to: a group
  chat, a supergroup, a person's private chat with the bot, or a forum supergroup with an optional topic (journal
  D554). The kind of a chat Destination — group, supergroup, private chat, forum or forum topic — is not chosen by the
  Admin; the Destination check reads it from Telegram (FR-11) and the Destination shows it, read-only.
- **C-35.FR-2** A chat Destination's settings name the chat, as a numeric chat id or a public `@username`, and, for a
  forum, optionally one topic. Saving runs the Destination check (FR-11) and refuses a Destination that fails it, as
  C-14.FR-2 does; the check stores the chat's numeric id, its title, and the topic's id, and Muster sends to the stored
  numeric ids. A chat in the mode "Chat" may not be a channel: a channel is the channel mode, since a reply inside a
  channel is a new post (F-015). A private chat is named by its numeric id, which is the person's Telegram user id; a
  person's `@username` does not name their private chat (both to verify).
- **C-35.FR-3** In a forum, a Destination names one topic, and the Destinations of one forum may name different topics
  or the same one (journal D519). How a topic is entered and who creates it is [question 2](#open-questions). A forum
  Destination without a topic posts to the forum's General topic (to verify). Topics of a person's private chat with
  the bot are not part of this capability.

### Messages and Threads

- **C-35.FR-4** The Root message of a chat Destination is a message in the chat — in the Destination's topic, for a
  forum topic — with the inline keyboard of its status and the button set of C-28.FR-8; Storm summaries and test
  messages go there too. Its Thread is the chain of replies to it in the same chat and topic: every Thread reply — new
  Alerts, ack timeout notices, Reminders with their buttons, Takeovers, Pages — is sent with `reply_parameters` naming
  the Root message, and in a forum topic with that topic's `message_thread_id` as well (journal D499). Unlike a channel,
  a chat has no automatic copy to wait for: no `telegram.copy_wait`, and no "Thread not attached".
- **C-35.FR-5** In a group chat, a supergroup and a forum topic, Thread replies are messages of the chat that every
  member sees, not a comment thread that only the discussion group follows (compare F-014); the documentation says so
  (FR-17). Loud and Quiet follow C-14.FR-6: a Quiet new message, Root message or Thread reply, carries
  `disable_notification`, which silences its push without removing it (F-012, to verify outside channels and
  discussion groups).
- **C-35.FR-6** Edits follow L1: every edit of a Root message carries its whole inline keyboard (C-14.FR-15), and a bot
  edits its own messages at any age (F-065, to verify in basic group chats and private chats).
- **C-35.FR-7** When a Thread reply in a chat Destination is refused with "message to be replied not found" (F-008, to
  verify in a basic group chat and a private chat), or an edit of its Root message is refused as in C-14.FR-7, the Root
  message is deleted, and the deleted Root message flow of C-11.FR-13 applies: an open Alert Group is posted again
  once, Quietly, and its Thread starts over. There is no lost Thread as in a channel (C-14.FR-3), because the Thread is
  made of replies to the Root message itself. What becomes of the refused reply is [question 8](#open-questions).

### Limits

- **C-35.FR-8** The Telegram Destinations of one Connection that post into one chat share one limiter for that chat,
  because Telegram's budget is per chat (F-016, ADR-0005, journal D519): the topics of a forum, and two Destinations of
  one group chat that a Team names with different Severity conditions (C-26.FR-1), wait for one budget. The limiter of
  a chat is keyed by the Connection and the chat's numeric id, so that two Connections — two bots — in one chat keep
  their own budgets (to verify, see [Pending](../../facts.md#pending), "several bots"). A delivery waits for the
  chat's limiter and the Connection's (C-11.FR-3), and every call takes a token of each, a send and an edit alike.
  Whether the chat's limiter replaces the Destination's own, how its limit is set, and whether a channel Destination's
  discussion group is a chat of its own is [question 1](#open-questions).
- **C-35.FR-9** A `429` with `retry_after` on a call to a chat holds that chat's limiter, so every Destination of the
  chat waits as asked; the rule of C-14.FR-7 that two Destinations of a Connection getting `429` within 60 seconds hold
  the whole Connection counts Destinations of different chats only.

### Buttons and Account links

- **C-35.FR-10** Presses on a chat Destination's messages follow C-14.FR-4 and C-14.FR-5: the button data is verified
  and must belong to the chat and the message of the press — for a Root message and for a Thread reply alike, the
  Destination's chat — and every press is answered with `answerCallbackQuery` before the Root message is edited. A press
  from an account without an Account link changes nothing and gets the "not linked" answer, in a person's private chat
  as anywhere else. Presses reach a bot that is an ordinary member of a group chat with privacy mode on (to verify).
  The bot keeps answering `/start <token>` in private chats (C-18.FR-2); in a private chat that is a Destination, every
  other message of the person is ignored, as today.

### The Destination check

- **C-35.FR-11** The Destination check of a chat Destination calls `getChat` on the chat and `getChatMember` for the
  bot there, and passes when:
  - a group chat or a supergroup: the bot is a member that may send messages — an admin, or a member whom neither the
    chat's permissions nor a restriction keep from sending (to verify);
  - a forum: the same, and `getChat` says that the chat is a forum (`is_forum`); with a topic named, and only if the
    spike finds a way to tell without sending a message ([question 3](#open-questions)), the topic exists and the bot
    may post in it;
  - a private chat: the chat exists and is a private chat; whether the person has blocked the bot or never started it
    is known only from a send unless question 3 finds a way (to verify).

  A check that passes but finds another chat than the stored numeric id — a `@username` that now names another chat —
  fails with `changed_since_saved`, as for a channel, so that the Broken probe does not end a Broken state that the
  next send would start again. The failures are reported separately, each with a code and a text of
  [reference.md](../l1/reference.md#banners-warnings-and-notices): "The chat was not found, or the bot cannot see it."
  (`chat_not_found`); "This chat is a channel. Use the mode Channel." (`chat_is_channel`); "The bot is not a member of
  the chat." (`bot_not_member`); "The bot may not send messages in the chat." (`bot_may_not_send`); "This chat is not a
  forum; it has no topics." (`not_a_forum`); "The topic no longer exists." (`topic_not_found`); "The topic is closed,
  and the bot may not post in closed topics." (`topic_closed`); "The person has blocked the bot, or has not started
  it." (`bot_blocked`); and "The group became a supergroup with the id {id}. Save the Destination with the new id."
  (`chat_upgraded`). The last four may come from a send instead of the check (FR-13). The steps are `chat_exists`,
  `bot_rights_chat` and `topic`. The check runs where C-14.FR-14 runs it — on save, from "Check", as the Broken probe
  when nothing is waiting (C-11.FR-9) on the delivery client class, and in `muster doctor` — and a check that passes
  ends the Broken state.
- **C-35.FR-12** The step that reads which people are outside the discussion group — `discussion_group_members` of the
  Telegram Destination check, bounded by `destination.telegram.group_people_budget` (C-27.FR-8) — runs for a group
  chat, a supergroup and a forum Destination against the chat itself, as the step `chat_members`, with the answers of
  F-128 (to verify in a basic group chat). For a private chat it makes no request: the person whose chat it is, is the
  User whose Telegram Account link has the chat's id, and every other person is outside it; with no such User, every
  person is outside it ([question 5](#open-questions)).

### Changes in Telegram

- **C-35.FR-13** Telegram's answer that a send or an edit is refused because the bot was removed from the chat, may no
  longer send there, the topic was deleted, or the person blocked the bot, is Fatal (C-14.FR-7) with the text of FR-11
  as the reason, so the Destination becomes Broken (C-11.FR-9) and its deliveries wait; the exact answers Telegram gives
  are to be verified. A closed topic is [question 7](#open-questions). Renaming a topic or the chat changes nothing for
  delivery, since Muster sends to the stored ids; the Destination shows the title of the chat that the last check read,
  and the topic's name when Telegram has given it to Muster (to verify).
- **C-35.FR-14** When Telegram answers that a group chat became a supergroup and names the new chat id
  (`migrate_to_chat_id`, to verify), the Destination becomes Broken with the reason `chat_upgraded` and its page shows
  the new id; Muster does not change the Destination's chat by itself (ADR-0010). What a save with the new id does with
  the Root messages already in the old group chat is [question 4](#open-questions).

### Mentions and Pages

- **C-35.FR-15** Mentions in every kind of chat Destination are those of a Telegram Destination (C-28.FR-2): linked
  people and the On-call, by a `tg://user?id=` link; never everyone in the chat, a messenger group or a Team, which are
  refused with `422` `mention_unsupported`. A Page by Mention in a chat Destination is a Thread reply of FR-4
  (C-32.FR-9), so a step that Pages a Team or everyone in the chat makes no reply there, as in a channel. A Mention
  reaches a person who has a Telegram Account link and is a member of the chat — of the topic's forum, for a topic
  (to verify; F-014 checked a discussion group); in a private chat, only the person whose chat it is (FR-12). The
  Schedule's warning about an unreachable On-call (C-27.FR-8) uses the members that FR-12 reads, and the Destination
  page of a private chat warns about each Mention entry that names someone else: "Only {name}, whose private chat this
  is, can be reached here", with the chat's title as {name} when no User has its id.

### Settings and documentation

- **C-35.FR-16** The mode and the chat of a Destination change through the API and the Destination form like any other
  setting, recorded in the Audit log with a before/after diff and with optimistic locking (C-28.FR-10). A request for a
  Telegram Destination without a mode is a channel Destination, so that 0.1 clients keep working.
- **C-35.FR-17** The documentation's Telegram page describes the two modes and when each fits: a channel keeps the feed
  to Root messages and puts the Threads in its discussion group; a group chat or a supergroup puts everything in one
  feed that rings every member; a forum gives each Team or Severity a topic of its own, sharing one budget; a private
  chat suits one person and is not how Muster Pages people (ADR-0018). It says which rights the bot needs in each kind,
  that a person must start the bot before their private chat can be a Destination, how to find a chat's id and a
  topic's link, what a group chat's change into a supergroup does, and that the known limitation of C-14.FR-9 — the
  automatic copy ringing the discussion group — does not apply to chats.

## UI

The Telegram Destination form gains "Where": "Channel with comments" (the L1 form, unchanged) or "Chat — a group, a
supergroup, a forum topic or a person's private chat with the bot". In the mode "Chat": the field "Chat" ("Chat id or
@username"), and "Topic" ("For a forum only: the topic's id or a link to it; empty for the General topic"); after a
save or "Check", the page shows the kind and title of the chat, the topic, and each failed step with its text and a link
to the documentation; the limiter field says "Shared with {n} other Destinations in this chat" when there are any. The
Destination list and page show the kind of a chat Destination beside its type.

## API surface

`destinations` of type Telegram (create, update, read): the mode `channel` or `chat`; in the mode `chat` the inputs
chat (an id or `@username`) and topic (optional), and the read-only kind, numeric chat id, chat title and topic name;
the fields of the mode `channel` unchanged, among them the read-only `channel_chat_id`. `channel_id`, which
`TelegramDestinationInput` requires today, becomes required in the mode `channel` only. The names are fixed in
`api/openapi.yaml` by the backend story, spec-first. The Destination check gains the steps and codes of FR-11. The
fake Telegram server, which already has supergroups and private chats, gains basic group chats, forums with open,
closed and deleted topics and the General topic, the change of a group chat into a supergroup, and a person who blocked
the bot.

## Acceptance

Checked against the fake Telegram server.

- **C-35.AC-1** A chat Destination saved with a basic group chat's id shows the kind "group" and the chat's title; a
  new Alert Group sends a message with buttons to the group chat, and a new Alert sends a reply to that message in the
  same chat, with no wait for an automatic copy.
- **C-35.AC-2** A forum Destination with a topic sends its Root message and every Thread reply with the topic's
  `message_thread_id`, the replies also with `reply_parameters` naming the Root message; one without a topic sends to
  the General topic as the spike records it.
- **C-35.AC-3** A private-chat Destination with a person's Telegram user id receives Root messages and Thread replies;
  a press there by that person with an Account link acknowledges the Alert Group, and a press by an account without
  one changes nothing and gets the "not linked" answer; `/start <token>` in that chat still links the account.
- **C-35.AC-4** The mode "Chat" refuses a channel with `chat_is_channel`, a topic for a chat that is not a forum with
  `not_a_forum`, and a group chat where the bot is not a member with `bot_not_member`, and nothing is saved; a
  Destination whose `@username` the fake server moves to another chat fails its check with `changed_since_saved`.
- **C-35.AC-5** Three Destinations in three topics of one forum and one Connection, with the Storm threshold raised,
  together never send more requests to the forum within a minute than the limit of the chat's limiter
  ([question 1](#open-questions)); a fourth Destination in another chat is not slowed by them; a `429` from the forum
  holds the three and not the fourth.
- **C-35.AC-6** A Thread reply refused with "message to be replied not found" in a group chat posts the open Alert
  Group again once, Quietly, with "The previous message was deleted at HH:MM", and later Thread replies reply to the new
  Root message.
- **C-35.AC-7** With the topic deleted on the fake server, the next send makes the Destination Broken with
  `topic_not_found`; saving the Destination with another topic runs the check, which ends the Broken state.
- **C-35.AC-8** With the fake group chat changed into a supergroup, the next send makes the Destination Broken with
  `chat_upgraded` and the new id on its page; the Destination's chat is unchanged until an Admin saves it.
- **C-35.AC-9** With the person of a private-chat Destination blocking the bot on the fake server, the next send makes
  the Destination Broken with `bot_blocked`.
- **C-35.AC-10** A chat Destination refuses Mention entries of the kinds `everyone`, `group`, `team` and
  `teams_from_label` with `422` `mention_unsupported`; a Page of the On-call in a forum topic is a reply in that topic
  with a `tg://user?id=` link; a step that Pages a Team, in a chain whose only Destinations are chat Destinations,
  records `escalation_step_skipped`.
- **C-35.AC-11** The Schedule warns that an On-call who is not a member of a group chat Destination's chat is
  unreachable there; a private-chat Destination with a Mention entry for another person shows "Only {name}, whose
  private chat this is, can be reached here".
- **C-35.AC-12** A Telegram Destination created through the API without a mode is a channel Destination, and every
  channel Destination saved before the upgrade delivers exactly as before.
- **C-35.AC-13** A Quiet Thread reply in a group chat carries `disable_notification`, and every edit of a Root message
  in a chat Destination carries its inline keyboard.
- **C-35.AC-14** In a real browser, the Telegram Destination form switches between "Channel with comments" and "Chat",
  shows the topic field with its hint, the kind and title the check found, the failed steps with their texts, and the
  shared limiter note, in English and in Russian.

## Open questions

The recorded decisions do not settle these; each has a recommendation for the maintainer.

1. **The chat's limiter and the Destination's own, its limit, and channel Destinations.** ADR-0005 gives each
   Destination a limiter, and `destination.telegram.limiter` is a Destination setting; when several Destinations share
   one chat (FR-8), does the chat's limiter replace their own, and whose value applies? A channel Destination's limiter
   counts its channel and its discussion group together (C-14.FR-2): if a chat Destination posts into that discussion
   group, do they share? _Recommendation:_ the chat's limiter replaces the Destination's own for Telegram; key every
   limiter by the chat each call goes to — a channel Destination's Root messages take the channel's tokens and its
   Thread replies the discussion group's — so that a chat Destination in that discussion group shares with it; the
   limit of a chat is the lowest among the Destinations that post there, and the form says it is shared. Confirm with
   the spike that a channel and its discussion group have separate budgets.
2. **Who creates the topics, and how one is entered.** _Recommendation:_ the Admin creates them in Telegram and names
   one per Destination; Muster never calls `createForumTopic` or renames, closes or deletes a topic, so that the bot
   needs no right to manage topics. The topic field takes the topic's id or any `t.me` link to the topic or to a
   message in it, from which Muster takes the topic's id.
3. **Checking a topic and a private chat without sending a message.** The Bot API has no read of a topic, and a person
   who blocked the bot is seen only on a send. _Recommendation:_ if the spike shows that `sendChatAction` with the
   topic's `message_thread_id`, or in the private chat, is refused for a deleted or closed topic and for a person who
   blocked the bot, the check uses it — a chat action is not a message, and the Broken probe runs it only while the
   Destination is Broken; otherwise the check reads what it can and the first send reports the rest (FR-13).
4. **A chat Destination that moves: a group chat that becomes a supergroup, another chat or topic, another mode.** The
   Root messages of open Alert Groups stay where they were, which after a change into a supergroup the bot probably
   can no longer edit. _Recommendation:_ the Destination becomes Broken with the new id (FR-14); the page offers "Use
   the supergroup", a save recorded in the Audit log. Whenever a save changes the chat, the topic or the mode of a
   Destination, each old Root message gets the final edit "No longer updated here" where Telegram still allows it, and
   the open Alert Groups are posted in the new place Quietly, as a newly added Destination receives them (C-11.FR-14).
5. **Choosing a person's private chat, and whose chat it is.** A private chat's id is the person's Telegram user id,
   which Admins rarely know, and a person may be linked through another bot than the Destination's. _Recommendation:_
   the Chat field also offers the Users with a Telegram Account link and fills in their Telegram user id; the check
   tells whether the person started this bot where question 3 allows. The Destination is not tied to that User: the
   person whose chat it is, is whichever User's Telegram Account link has the chat's id (FR-12), and deleting the User
   changes only the warnings of FR-15.
6. **The Account link reply in a private chat that is a Destination.** The bot's answer to `/start <token>` takes only
   the Connection's limiter today. _Recommendation:_ it also takes a token of that chat's limiter, on the interactive
   path, so that the chat's budget covers every message the bot sends there.
7. **A closed topic.** Is it Broken, which a person ends by reopening the topic, or does delivery wait as for a
   Transient error until it reopens? _Recommendation:_ Fatal and Broken with `topic_closed`, like a deleted topic: a
   closed topic is a person's choice that another person must undo, and Broken raises `MusterDestinationBroken`.
8. **The Thread reply refused because its Root message was deleted.** C-11.FR-13 starts the Thread over but does not
   say what becomes of the reply that found the Root message gone, which may be a Page. _Recommendation:_ it is sent
   again as a reply to the new Root message, so that a Page is not lost.

## Facts to verify

Telegram behaviour that the requirements rely on and that no entry of [verified facts](../../facts.md) records; a
spike on the test stand, with Bot API 10.4 or later, records each as an `F-NNN` before the stories that rely on it (L2
§5.1). Most are described in the Bot API documentation but were never observed.

1. A basic group chat that becomes a supergroup: the answer to a send or an edit in the old group chat (expected `400`
   "group chat was upgraded to a supergroup chat" with `parameters.migrate_to_chat_id`), the service messages
   `migrate_to_chat_id` and `migrate_from_chat_id` in updates, whether the bot can still edit, reply to and receive
   presses on its messages in the old group chat, and which ids those messages have in the supergroup.
2. Forum topics: that `getChat` reports `is_forum`; that a send with a topic's `message_thread_id` and
   `reply_parameters` stays in the topic; how the General topic is addressed (no `message_thread_id`, or `1`), and
   whether a hidden or closed General topic accepts the bot's messages; the answers to a send, a reply and an edit in a
   deleted topic (expected "message thread not found") and in a closed topic (expected `TOPIC_CLOSED`), for an admin
   bot with and without the right to manage topics; whether `sendChatAction` with a topic is refused in those cases;
   whether the bot, with privacy mode on, receives `forum_topic_created`, `forum_topic_edited`, `forum_topic_closed` and
   `forum_topic_reopened`, and whether the answer to a send names the topic; what happens to topic messages when an
   admin turns the forum off.
3. The budget of a forum: that its topics share one budget of about 20 requests per minute (F-016 measured a discussion
   group), and whether a channel and its discussion group have separate budgets.
4. Basic group chats and supergroups: that a group chat with a public `@username` is always a supergroup; that a bot
   that is an ordinary member with privacy mode on receives `callback_query` for presses on its messages; which of
   `getChatMember`'s fields, together with the chat's `permissions`, say that such a bot may send messages; that
   `disable_notification` silences the push there as in F-012; that the bot edits its own messages in a basic group
   chat at any age (F-065 checked a channel and a supergroup); that a reply to a deleted message there is refused with
   "message to be replied not found" (F-008 checked a discussion group); what `getChatMember` answers in a basic group
   chat about a person who was never in it (F-128 answered `PARTICIPANT_ID_INVALID` in a supergroup); and whether a
   Mention in a reply rings a member who muted the group chat, and does not reach a person outside it (F-014 and F-129
   checked a discussion group).
5. Private chats: that a private chat's id is the person's Telegram user id; what `getChat` answers for a person who
   never started the bot, and whether `@username` names a person's private chat; the answers to a send after the
   person blocked the bot (expected `403` "bot was blocked by the user"), never started it, or deleted their account,
   and to a reply to a deleted message; whether `sendChatAction` is refused in those cases; the budget of a private
   chat; and whether the bot edits its messages there at any age.

## Revisions to other capabilities

When the stories of this capability merge, these get the marker of [L2 §2](../L2.md#2-conventions) and rows in
[L2 §6](../L2.md#6-revisions-to-l1): C-14.FR-2 (group chats and forum topics, which L1 leaves out), C-14.FR-3,
C-14.FR-4, C-14.FR-7, C-14.FR-9, C-14.FR-14, the C-14 UI and API surface (the chat mode beside the channel);
C-11.FR-3 and C-14.FR-2 (one limiter per chat), with `destination.telegram.limiter` in defaults.md; C-27.FR-8 (members
of a chat, not only of a discussion group); C-32.FR-11 (the Telegram row: a reply in the chat or topic); reference.md,
Loud and Quiet (in a chat, Thread replies reach every member and there is no automatic copy) and the texts and codes of
FR-11; and `design/stories/coverage.md`, which gains the rows of this file with its stories. ADR-0018's sentence about
people "not in the discussion group of a Telegram channel" is proposed to say "not in the chat" for the maintainer to
decide.

## Related ADRs

ADR-0005, ADR-0010, ADR-0013, ADR-0015, ADR-0018.

## Depends on

C-11 — delivery, limiters and the deleted Root message flow; C-12 — rendering; C-13 — the shared Destination pages and
checks; C-14 — the Telegram Connection and the channel Destination; C-18 — Account links; C-26 — Severity conditions
of the Destinations of Routes and Teams; C-27 — the warning about unreachable On-calls; C-28 — Mentions and button sets;
C-32 — Pages by Mention.

## Suggested story split

- **Spike** — the [Facts to verify](#facts-to-verify), recorded in `design/facts.md`.
- **BE** — the chat mode with its check, Root messages and Threads as replies, forum topics, the limiter per chat, the
  Fatal answers and the change into a supergroup, Mentions, Pages and members of a chat; the fake server extension.
- **FE** — the Destination form with "Where", the topic field, the check's result and the shared limiter note.
- **Docs** — the Telegram page of FR-17.
