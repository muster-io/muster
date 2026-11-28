# C-35. Telegram chats and topics

[L2 index](../L2.md) · Release: a 0.2.x patch release · Status: Draft · UI: yes · Depends on: C-11, C-12, C-13, C-14,
C-18, C-26, C-27, C-28, C-32

**Goal.** Let a Telegram Destination be a chat where people work together, not only a channel with comments: a group
chat, a supergroup, or one topic of a forum, so that one forum carries a topic per Team or per Severity. Root messages,
Threads, buttons, Mentions and Pages work in each kind as they do in a channel, within the budget Telegram gives each
chat, which all the Destinations of one Connection in that chat share (FR-9; issue #371; ADR-0005, ADR-0018, journal
D499, D519, D554, and the maintainer's decisions on this draft).

Names in the form `area.setting` refer to rows of [defaults.md](../l1/defaults.md); ids in the form `F-NNN` refer to
[verified facts](../../facts.md#telegram). Behaviour of Telegram that this file relies on and that no fact records yet
is listed under [Facts to verify](#facts-to-verify); the requirements that rely on it are marked "(to verify)" and are
confirmed or corrected by a spike with the maintainer before the stories, as S-132 did for discussion groups.

## Scenarios

1. A team already has a Telegram group chat where its people talk. The Admin creates a Destination in the mode "Chat",
   enters the group chat's id, and the check shows "Group · Payments team · the bot may send messages". A new Alert
   Group appears in the group chat as a message with buttons; its new Alerts, the ack timeout notice and the Reminders
   are replies to that message in the same chat.
2. A forum supergroup "Alerts" has the topics "Payments · critical" and "Payments · notice". The Admin creates one
   Destination per topic, names each on the Team "payments" with its Severity condition (C-26.FR-1), and enters the
   forum and the topic's link. Root messages and their Thread replies stay in their topic.
3. For the Team "search" the Admin creates a third Destination in the same forum and chooses "Create a topic" with the
   name "Search"; the bot, an admin allowed to manage topics, creates it on save. During a Storm the three Destinations
   wait for one budget, the forum's, and Telegram never answers `429`.
4. Someone deletes the topic "Search". The next send to it is refused, the Destination becomes Broken with "The topic
   no longer exists", and `MusterDestinationBroken` reaches the admins through another Destination. The Admin points
   the Destination at a new topic; the old Root messages are gone with the topic, and the open Alert Groups are posted
   in the new topic Quietly.
5. A basic group chat grows and its owner turns it into a supergroup, which gives it a new chat id. The next send to
   the old id is refused; the Destination becomes Broken with "The group became a supergroup", and the Admin presses
   "Use the supergroup". The old Root messages get "No longer updated here", edited through the old chat id, and the
   open Alert Groups are posted Quietly in the supergroup.
6. An Escalation step Pages the On-call of "payments" in the topic "Payments · critical": the Page is a reply to the
   Root message in that topic that mentions Petr by his Telegram Account link (C-32.FR-9).

## Functional requirements

### Destination kinds

- **C-35.FR-1** A Telegram Destination has a mode. **Channel** is the Destination of L1 (C-14.FR-2): a channel with
  comments, whose Thread lives in its discussion group; every Telegram Destination saved before this capability is a
  channel Destination and keeps working unchanged. **Chat** takes a group chat, a supergroup, or a forum supergroup
  with one topic or none. The kind of a chat Destination — group, supergroup, forum or forum topic — is not chosen by
  the Admin; the Destination check reads it from Telegram (FR-12) and the Destination shows it, read-only. A person's
  private chat with the bot is not a Destination: Muster reaches a person in their direct messages through a
  Notification rule action that L2 specifies later ([L2 §1.5](../L2.md#15-specified-later-in-l2)).
- **C-35.FR-2** A chat Destination's settings name the chat, as a numeric chat id or a public `@username`, and, for a
  forum, its topic (FR-3). Saving runs the Destination check (FR-12) and refuses a Destination that fails it, as
  C-14.FR-2 does; the check stores the chat's numeric id, its title and the topic's id, and Muster sends to the stored
  numeric ids. The mode "Chat" refuses a channel — a channel is the mode Channel, since a reply inside a channel is a
  new post (F-015) — and a private chat.
- **C-35.FR-3** In a forum, a Destination posts to one topic, and the Destinations of one forum may post to different
  topics or the same one (journal D519). The Admin either names an existing topic, by its id or by a `t.me` link to the
  topic or to a message in it, from which Muster takes the topic's id; or asks Muster to create one with a given name;
  or names none, and the Destination posts to the forum's General topic (to verify).
- **C-35.FR-4** A topic to create is created on save with `createForumTopic`, as the last step after the rest of the
  check passed, and its id is stored with the Destination and recorded in the Audit log. It needs a bot that is an
  admin of the forum allowed to manage topics — `can_manage_topics` in the bot's `getChatMember`, read before the
  call; without that right the save is refused with `bot_may_not_manage_topics` — "The bot may not create topics in
  this forum. Allow it to manage topics, or name an existing topic." — and nothing is created. `createForumTopic` is not idempotent, so a save that fails after it leaves the new topic in Telegram,
  where the Admin may name it on the next save; the documentation says so. Once created, the topic is like one the
  Admin named: Muster never renames, closes or deletes a topic, renaming the Destination does not rename its topic,
  and deleting the Destination leaves the topic and its messages in Telegram, after the final edits of C-11.FR-14.
  Renaming or deleting the topic in Telegram has the effect of FR-14.

### Messages and Threads

- **C-35.FR-5** The Root message of a chat Destination is a message in the chat — in the Destination's topic, for a
  forum — with the inline keyboard of its status and the button set of C-28.FR-8; Storm summaries and test messages go
  there too. Its Thread is the chain of replies to it in the same chat and topic: every Thread reply — new Alerts, ack
  timeout notices, Reminders with their buttons, Takeovers, Pages — is sent with `reply_parameters` naming the Root
  message, and in a forum with the topic's `message_thread_id` as well (journal D499). Unlike a channel, a chat has no
  automatic copy to wait for: no `telegram.copy_wait`, and no "Thread not attached".
- **C-35.FR-6** In a group chat, a supergroup and a forum topic, Thread replies are messages of the chat that every
  member sees, not a comment thread that only the discussion group follows (compare F-014); the documentation says so
  (FR-19). Loud and Quiet follow C-14.FR-6: a Quiet message — a Root message or a Thread reply — carries
  `disable_notification`, which silences its push without removing it (F-012, to verify outside channels and
  discussion groups).
- **C-35.FR-7** Edits follow L1: every edit of a Root message carries its whole inline keyboard (C-14.FR-15), and a bot
  edits its own messages at any age (F-065, to verify in a basic group chat).
- **C-35.FR-8** When a Thread reply in a chat Destination is refused with "message to be replied not found" (F-008, to
  verify in a basic group chat), or an edit of its Root message is refused as in C-14.FR-7, the Root message is
  deleted, and the deleted Root message flow of C-11.FR-13 applies: an open Alert Group is posted again once, Quietly,
  and its Thread starts over. The refused Thread reply, a Page included, is then sent as a reply to the new Root
  message. There is no lost Thread as in a channel (C-14.FR-3), because the Thread is made of replies to the Root
  message itself.

### Limits

- **C-35.FR-9** Telegram Destinations have one limiter per Connection and chat, in place of a limiter per Destination,
  because Telegram's budget is per chat (F-016, ADR-0005, journal D519). Every call takes a token of the limiter of the
  chat it goes to, a send and an edit alike, and of the Connection's (C-11.FR-3): a chat Destination's calls take the
  chat's; a channel Destination's Root messages, Storm summaries and test messages take the channel's, and its Thread
  replies the discussion group's. So the topics of a forum, the Destinations of one group chat that a Team names with
  different Severity conditions (C-26.FR-1), and a chat Destination in a channel's discussion group together with that
  channel's Destination, wait for one budget. Two Connections — two bots — in one chat keep their own limiters (to
  verify, see [Pending](../../facts.md#pending), "several bots").
- **C-35.FR-10** The limit of a chat's limiter is the lowest `destination.telegram.limiter` among the Destinations of
  that Connection that post into the chat — a channel Destination posts into its channel and its discussion group — and
  follows them as they are saved and deleted. The Destination form says "Shared with {n} other Destinations in this
  chat" when there are any. A `429` with `retry_after` on a call to a chat holds that chat's limiter; the rule of
  C-14.FR-7 that two Destinations of a Connection getting `429` within 60 seconds hold the whole Connection counts
  `429`s from different chats only.

### Buttons and Account links

- **C-35.FR-11** Presses on a chat Destination's messages follow C-14.FR-4 and C-14.FR-5: the button data is verified
  and must belong to the chat and the message of the press — for a Root message and for a Thread reply alike, the
  Destination's chat — and every press is answered with `answerCallbackQuery` before the Root message is edited. A press
  from an account without an Account link changes nothing and gets the "not linked" answer. Presses reach a bot that is
  an ordinary member of a group chat with privacy mode on (to verify). Account links stay as C-18.FR-2 says: the bot
  answers `/start <token>` in a person's private chat with it.

### The Destination check

- **C-35.FR-12** The Destination check of a chat Destination calls `getChat` on the chat and `getChatMember` for the
  bot there, and passes when:
  - a group chat or a supergroup: the bot is a member that may send messages — an admin, or a member whom neither the
    chat's permissions nor a restriction keep from sending (to verify);
  - a forum: the same, and `getChat` says that the chat is a forum (`is_forum`). Whether a named topic exists and is
    open is not probed: `sendChatAction` with the topic answers `true` for a deleted and for a closed topic alike
    ([F-138](../../facts.md#forum-topics)), so the first send tells it (FR-14), and the check of a Destination Broken
    with `topic_not_found` or `topic_closed` does not pass until a send to the topic succeeds (journal D765).

  A check that passes but finds another chat than the stored numeric id — a `@username` that now names another chat —
  fails with `changed_since_saved`, as for a channel, so that the Broken probe does not end a Broken state that the
  next send would start again. The failures are reported separately, each with a code and a text of
  [reference.md](../l1/reference.md#banners-warnings-and-notices): "The chat was not found, or the bot cannot see it."
  (`chat_not_found`); "This chat is a channel. Use the mode Channel." (`chat_is_channel`); "A private chat cannot be a
  Destination." (`chat_is_private`); "The bot is not a member of the chat." (`bot_not_member`); "The bot may not send
  messages in the chat." (`bot_may_not_send`); "This chat is not a forum; it has no topics." (`not_a_forum`); "The
  topic no longer exists." (`topic_not_found`); "The topic is closed." (`topic_closed`); and "The group became a
  supergroup. Use the supergroup." (`chat_upgraded`), with FR-4's `bot_may_not_manage_topics` on a save that creates a
  topic. `bot_not_member`, `bot_may_not_send`, `topic_not_found`, `topic_closed` and `chat_upgraded` may also come
  from a send (FR-14, FR-15). The steps are `chat_exists`, `bot_rights_chat`
  and `topic`. The check runs where C-14.FR-14 runs it — on save, from "Check", as the Broken probe when nothing is
  waiting (C-11.FR-9) on the delivery client class, and in `muster doctor` — and a check that passes ends the Broken
  state.
- **C-35.FR-13** The step that reads which people are outside the discussion group — `discussion_group_members` of the
  Telegram Destination check, bounded by `destination.telegram.group_people_budget` (C-27.FR-8) — runs for a chat
  Destination against the chat itself, as the step `chat_members`, with the answers of F-128 (to verify in a basic
  group chat).

### Changes in Telegram

- **C-35.FR-14** Telegram's answer that a send or an edit is refused because the bot was removed from the chat or may
  no longer send there, or because the topic was deleted or is closed, is Fatal (C-14.FR-7) with the text of FR-12 as
  the reason, so the Destination becomes Broken (C-11.FR-9) and its deliveries wait; a closed topic is Broken with
  `topic_closed` as a deleted one is with `topic_not_found`, and reopening the topic lets the probe end it. The exact
  answers Telegram gives are to be verified. Renaming a topic or the chat changes nothing for delivery, since Muster
  sends to the stored ids; the Destination shows the title of the chat that the last check read, and the topic's name
  that Muster gave it or that Telegram has given Muster (to verify).
- **C-35.FR-15** When Telegram answers that a group chat became a supergroup and names the new chat id
  (`migrate_to_chat_id`, to verify), the Destination becomes Broken with `chat_upgraded`; Muster does not change the
  Destination's chat by itself (ADR-0010). Its page shows the new id and offers "Use the supergroup", which saves the
  Destination with the new chat id like any other save — the check, the Audit log and the move of FR-16.
- **C-35.FR-16** A save that changes the chat, the topic or the mode of a Telegram Destination moves it: each open
  Root message in the old place gets the final edit "No longer updated here; current state in Muster: {link}" of
  C-11.FR-14 where Telegram still lets the bot edit it — in a deleted topic it does not — and the open Alert Groups are
  posted in the new place Quietly, as a newly added Destination receives them (C-11.FR-14). After a group chat became a
  supergroup, the final edit of a Root message posted before the change goes through the old chat id, where the message
  keeps its id; the new chat id does not reach it ([F-142](../../facts.md#basic-group-chats); journal D765).

### Mentions and Pages

- **C-35.FR-17** Mentions in a chat Destination are those of a Telegram Destination (C-28.FR-2): linked people and the
  On-call, by a `tg://user?id=` link; never everyone in the chat, a messenger group or a Team, which are refused with
  `422` `mention_unsupported`. A Page by Mention in a chat Destination is a Thread reply of FR-5 (C-32.FR-9), so a step
  that Pages a Team or everyone in the chat makes no reply there, as in a channel. A Mention reaches a person who has a
  Telegram Account link and is a member of the chat — of the forum, for a topic (to verify; F-014 checked a discussion
  group). The Schedule's warning about an unreachable On-call (C-27.FR-8) uses the members that FR-13 reads.

### Settings and documentation

- **C-35.FR-18** The mode, the chat and the topic of a Destination change through the API and the Destination form
  like any other setting, recorded in the Audit log with a before/after diff and with optimistic locking (C-28.FR-10).
  A request for a Telegram Destination without a mode is a channel Destination, so that 0.1 clients keep working.
- **C-35.FR-19** The documentation's Telegram page describes the two modes and when each fits: a channel keeps the feed
  to Root messages and puts the Threads in its discussion group; a group chat or a supergroup puts everything in one
  feed that rings every member; a forum gives each Team or Severity a topic of its own, sharing one budget. It says
  which rights the bot needs in each kind, the right to manage topics only for topics Muster creates, how to find a
  chat's id and a topic's link, that a topic created by a save that then failed stays in Telegram, what a group chat's
  change into a supergroup and a move do, that the limiter of a chat is shared, and that the known limitation of
  C-14.FR-9 — the automatic copy ringing the discussion group — does not apply to chats.

## UI

The Telegram Destination form gains "Where": "Channel with comments" (the L1 form, unchanged) or "Chat — a group, a
supergroup or a forum topic". In the mode "Chat": the field "Chat" ("Chat id or @username"), and "Topic", for a forum
only: "General topic", "Existing topic" with the topic's id or link, or "Create a topic" with its name. After a save or
"Check", the page shows the kind and title of the chat, the topic and its name, and each failed step with its text and
a link to the documentation; a Destination Broken with `chat_upgraded` shows "Use the supergroup"; the limiter field
says "Shared with {n} other Destinations in this chat" when there are any. The Destination list and page show the kind
of a chat Destination beside its type.

## API surface

`destinations` of type Telegram (create, update, read): the mode `channel` or `chat`; in the mode `chat` the inputs
chat (an id or `@username`) and topic — none, an existing topic's id or link, or a name to create — and the read-only
kind, numeric chat id, chat title, topic id and topic name; the fields of the mode `channel` unchanged, among them the
read-only `channel_chat_id`. `channel_id`, which `TelegramDestinationInput` requires today, becomes required in the
mode `channel` only. "Use the supergroup" is an update with the new chat id. The names are fixed in `api/openapi.yaml`
by the backend story, spec-first. The Destination check gains the steps and codes of FR-12. The fake Telegram server,
which already has supergroups, gains basic group chats, forums with open, closed and deleted topics, the General topic
and `createForumTopic` with the right to manage topics, the change of a group chat into a supergroup with edits
through the old chat id (F-142), and `sendChatAction` answering `true` in deleted and closed topics (F-138).

## Acceptance

Checked against the fake Telegram server.

- **C-35.AC-1** A chat Destination saved with a basic group chat's id shows the kind "group" and the chat's title; a
  new Alert Group sends a message with buttons to the group chat, and a new Alert sends a reply to that message in the
  same chat, with no wait for an automatic copy. A press on its buttons from a linked account acknowledges the Alert
  Group; a press whose button data belongs to another chat changes nothing.
- **C-35.AC-2** A forum Destination with a topic sends its Root message and every Thread reply with the topic's
  `message_thread_id`, the replies also with `reply_parameters` naming the Root message; one without a topic sends to
  the General topic as the spike records it.
- **C-35.AC-3** A save with "Create a topic" named "Search" creates one topic on the fake server and stores its id and
  name, recorded in the Audit log; with a bot not allowed to manage topics the save is refused with
  `bot_may_not_manage_topics` and no topic is created; renaming the Destination renames no topic, and deleting it
  deletes none.
- **C-35.AC-4** The mode "Chat" refuses a channel with `chat_is_channel`, a private chat with `chat_is_private`, a
  topic for a chat that is not a forum with `not_a_forum`, and a group chat where the bot is not a member with
  `bot_not_member`, and nothing is saved; a Destination whose `@username` the fake server moves to another chat fails
  its check with `changed_since_saved`.
- **C-35.AC-5** Three Destinations in three topics of one forum and one Connection, with the Storm threshold raised and
  different `destination.telegram.limiter` values, together never send more requests to the forum within a minute than
  the lowest of those values; a fourth Destination in another chat is not slowed by them; a `429` from the forum holds
  the three and not the fourth. A channel Destination's Thread replies and a chat Destination in its discussion group
  take tokens of one limiter.
- **C-35.AC-6** A Thread reply refused with "message to be replied not found" in a group chat posts the open Alert
  Group again once, Quietly, with "The previous message was deleted at HH:MM", and the refused reply, then later Thread
  replies, reply to the new Root message.
- **C-35.AC-7** With the topic deleted on the fake server, the next send makes the Destination Broken with
  `topic_not_found`; with a topic closed, with `topic_closed`, and reopening it lets the probe end the Broken state.
- **C-35.AC-8** With the fake group chat changed into a supergroup, the next send makes the Destination Broken with
  `chat_upgraded` and the new id on its page; the Destination's chat is unchanged until "Use the supergroup" saves it,
  after which each open Root message posted before the change gets the final edit through the old chat id and its
  open Alert Groups are posted Quietly in the supergroup.
- **C-35.AC-9** A save that moves a Destination from one topic to another makes the final edit "No longer updated
  here" of each open Root message in the old topic and posts the open Alert Groups Quietly in the new one; a resolved
  Alert Group is neither edited nor posted. A change of mode from Channel to Chat moves a Destination likewise.
- **C-35.AC-10** A chat Destination refuses Mention entries of the kinds `everyone`, `group`, `team` and
  `teams_from_label` with `422` `mention_unsupported`; a Page of the On-call in a forum topic is a reply in that topic
  with a `tg://user?id=` link; a step that Pages a Team, in a chain whose only Destinations are chat Destinations,
  records `escalation_step_skipped`.
- **C-35.AC-11** The Schedule warns that an On-call who is not a member of a chat Destination's chat is unreachable
  there.
- **C-35.AC-12** A Telegram Destination created through the API without a mode is a channel Destination, and every
  channel Destination saved before the upgrade delivers exactly as before.
- **C-35.AC-13** A Quiet Thread reply in a group chat carries `disable_notification`, and every edit of a Root message
  in a chat Destination carries its inline keyboard.
- **C-35.AC-14** In a real browser, the Telegram Destination form switches between "Channel with comments" and "Chat",
  offers the General topic, an existing topic and a topic to create, shows the kind and title the check found, the
  failed steps with their texts, "Use the supergroup" on a Destination Broken with `chat_upgraded`, and the shared
  limiter note, in English and in Russian.

## Facts to verify

Telegram behaviour that the requirements rely on and that no entry of [verified facts](../../facts.md) records; a
spike with the maintainer on the test stand, with Bot API 10.4 or later, records each as an `F-NNN` before the stories
(L2 §5.1). Most are described in the Bot API documentation but were never observed.

1. A basic group chat that becomes a supergroup: the answer to a send or an edit in the old group chat (expected `400`
   "group chat was upgraded to a supergroup chat" with `parameters.migrate_to_chat_id`), the service messages
   `migrate_to_chat_id` and `migrate_from_chat_id` in updates, whether the bot can still edit, reply to and receive
   presses on its messages in the old group chat, and which ids those messages have in the supergroup.
2. Forum topics: that `getChat` reports `is_forum`; that a send with a topic's `message_thread_id` and
   `reply_parameters` stays in the topic; how the General topic is addressed (no `message_thread_id`, or `1`), and
   whether a hidden or closed General topic accepts the bot's messages; the answers to a send, a reply and an edit in a
   deleted topic (expected "message thread not found") and in a closed topic (expected `TOPIC_CLOSED`), for an admin
   bot with and without the right to manage topics; whether `sendChatAction` with a topic is refused for a deleted and
   for a closed topic, and shows anything to the members; what `createForumTopic` answers with and without the right
   to manage topics, and what it returns; whether the bot, with privacy mode on, receives `forum_topic_created`,
   `forum_topic_edited`, `forum_topic_closed` and `forum_topic_reopened`, and whether the answer to a send names the
   topic; what happens to topic messages when an admin turns the forum off.
3. Budgets: that the topics of a forum share one budget of about 20 requests per minute (F-016 measured a discussion
   group), and that a channel and its discussion group have separate budgets.
4. Basic group chats and supergroups: that a group chat with a public `@username` is always a supergroup; what
   `getChat` answers for a private chat, so that the mode "Chat" can refuse it; that a bot that is an ordinary member
   with privacy mode on receives `callback_query` for presses on its messages; which of `getChatMember`'s fields,
   together with the chat's `permissions`, say that such a bot may send messages; that `disable_notification`
   silences the push there as in F-012; that the bot edits its own messages in a basic group chat at any age (F-065
   checked a channel and a supergroup); that a reply to a deleted message there is refused with "message to be replied
   not found" (F-008 checked a discussion group); what `getChatMember` answers in a basic group chat about a person who
   was never in it (F-128 answered `PARTICIPANT_ID_INVALID` in a supergroup); and whether a Mention in a reply rings a
   member who muted the group chat, and does not reach a person outside it (F-014 and F-129 checked a discussion
   group).

## Revisions to other capabilities

When the stories of this capability merge, these get the marker of [L2 §2](../L2.md#2-conventions) and rows in
[L2 §6](../L2.md#6-revisions-to-l1): C-14.FR-2 (group chats and forum topics, which L1 leaves out), C-14.FR-3,
C-14.FR-4, C-14.FR-7, C-14.FR-9, C-14.FR-14, the C-14 UI and API surface (the mode Chat beside the channel);
C-11.FR-3 and C-14.FR-2 (a limiter per Connection and chat instead of per Destination), with
`destination.telegram.limiter` in defaults.md; C-11.FR-14 (a move of a Destination, FR-16); C-27.FR-8 (members of a
chat, not only of a discussion group); C-32.FR-11 (the Telegram row: a reply in the chat or topic); reference.md, Loud
and Quiet (in a chat, Thread replies reach every member and there is no automatic copy) and the texts and codes of
FR-4 and FR-12; and `design/stories/coverage.md`, which gains the rows of this file with its stories. Proposed for the
maintainer: ADR-0005's "Each Destination has a limiter" to say that Telegram Destinations share a limiter per chat;
and ADR-0018's sentences that a Telegram Destination may be a person's private chat and that people "not in the
discussion group of a Telegram channel" are unreachable, to say "not in the chat". ADR-0018's "Pages, not direct
messages" and its rejected option of Paging by direct messages are superseded by the Notification rule action of
[L2 §1.5](../L2.md#15-specified-later-in-l2), which revises them, not this capability.

## Related ADRs

ADR-0005, ADR-0010, ADR-0013, ADR-0015, ADR-0018.

## Depends on

C-11 — delivery, limiters and the deleted Root message flow; C-12 — rendering; C-13 — the shared Destination pages and
checks; C-14 — the Telegram Connection and the channel Destination; C-18 — Account links; C-26 — Severity conditions
of the Destinations of Routes and Teams; C-27 — the warning about unreachable On-calls; C-28 — Mentions and button sets;
C-32 — Pages by Mention.

## Suggested story split

- **Spike** — the [Facts to verify](#facts-to-verify), with the maintainer, recorded in `design/facts.md`.
- **BE** — the mode Chat with its check, Root messages and Threads as replies, forum topics named or created, the
  limiter per Connection and chat, the Fatal answers, the change into a supergroup and moves, Mentions, Pages and
  members of a chat; the fake server extension.
- **FE** — the Destination form with "Where", the topic choice, the check's result, "Use the supergroup" and the shared
  limiter note.
- **Docs** — the Telegram page of FR-19.
