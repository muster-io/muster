# Verified facts

- Status: Living document
- Date: 2026-11-28

External behaviour that Muster's design relies on and that was checked against the real system rather than taken from
its documentation: how Telegram, Mattermost, Slack, Alertmanager and Grafana actually behave. Each fact has a stable id
`F-NNN`, never reused, followed by the fact itself, the date it was checked, the method and the version of the external
system where it is known, and the requirements that rely on it. When a later check contradicts a fact, the entry is
corrected with the new date and the requirements that cite it are revisited. Facts still to be checked are listed under
[Pending](#pending); the test-environment questions of the first release are in the
[L1 open questions](prd/L1.md#51-test-environment-facts).

## Telegram

Unless an entry says otherwise, the facts were checked on 2026-10-03 against Telegram's cloud Bot API 10.3 (the latest
version at the time, released 2026-08-24) on a test stand: a test bot, a test channel with comments enabled and the
discussion group Telegram linked to it, the bot an admin of both. Updates were read with `getUpdates` and logged as raw
JSON. Notifications were observed on the clients of two test accounts: a member of the discussion group and a subscriber
of the channel who did not join the discussion group.

### Channel and discussion group

**F-001. A bot joins a channel only as an admin.** Adding a bot to a channel without admin rights does not make it a
member: no `my_chat_member` update arrives and its status stays `left`; after it is made an admin, `my_chat_member`
arrives with its rights. — _2026-10-03 · added, then promoted, on the stand · Bot API 10.3_ · Used in C-14.FR-9.

**F-002. `linked_chat_id` appears only with comments enabled.** `getChat` on a channel without comments has no
`linked_chat_id`; once comments are enabled, it names the discussion group. — _2026-10-03 · `getChat` before and after
enabling comments · Bot API 10.3_ · Used in C-14.FR-2, C-14.FR-14.

**F-003. Only an admin bot receives the automatic copy.** A bot that is not in the discussion group gets
`403 bot is not a member of the supergroup chat` from `getChatMember` there. A bot that is an ordinary member, with
privacy mode on (the default), does not receive the automatic copy of its own channel post; an admin does. —
_2026-10-03 · the same post with the bot as a member and as an admin · Bot API 10.3_ · Used in C-14.FR-3, C-14.FR-14.

**F-004. A channel admin sees new subscribers.** A bot that is an admin of the channel receives a `chat_member` update
when someone subscribes, if `chat_member` is in its `allowed_updates`. — _2026-10-03 · a test account subscribed ·
Bot API 10.3_ · Not used in L1.

### Comment threads

**F-005. The automatic copy of a post.** It arrives as a `message` update in the discussion group about 4 s after the
answer to `sendMessage`, with `from.id` 777000 (Telegram), `sender_chat` set to the channel, `is_automatic_forward:
true` and `forward_origin.message_id` equal to the channel post's id. It carries an `edit_date` equal to its `date` and
no inline keyboard. — _2026-10-03 · raw updates after a bot post · Bot API 10.3_ · Used in C-14.FR-3.

**F-006. Edits of a post reach its copy.** When the bot edits a channel post, Telegram edits the copy itself, and the
bot receives an `edited_message` for the copy within the same second. The bot receives no updates about its own channel
posts or their edits. — _2026-10-03 · raw updates after an edit · Bot API 10.3_ · Used in C-14.FR-3.

**F-007. A reply to the copy is a comment.** A message sent to the discussion group with `reply_parameters.message_id`
set to the copy's id appears under the channel post as a comment (the post's comment counter grows) and carries
`message_thread_id` equal to the copy's id. — _2026-10-03 · `sendMessage` to the group, seen in the clients · Bot API
10.3_ · Used in C-14.FR-3.

**F-008. A deleted copy loses the thread.** After the copy is deleted, a reply to it fails with
`400 message to be replied not found`; with `allow_sending_without_reply` the same message goes to the group as an
ordinary message outside any thread. Unpinning the copy does not break the thread. — _2026-10-03 · copy deleted and
unpinned by hand · Bot API 10.3_ · Used in C-14.FR-3, C-14.FR-7.

### Buttons

**F-009. Presses on posts and on comments.** A press on a channel post's button arrives as a `callback_query` with
`from` set to the person and `message.chat.type` `channel`. Buttons on a reply in a comment thread work as well; there
`message.chat` is the discussion group and `message_thread_id` is set. — _2026-10-03 · presses from a test account ·
Bot API 10.3_ · Used in C-14.FR-4, C-17.FR-4.

**F-010. A press must be answered within about 15 seconds.** `answerCallbackQuery` succeeded 2.6 to 14.6 s after the
update arrived; at about 15.7 s it failed with `400 query is too old and response timeout expired or query ID is
invalid`. — _2026-10-03 · answers delayed on purpose · Bot API 10.3_ · Used in C-14.FR-5.

**F-011. An edit without a keyboard removes the buttons.** An edit sent without `reply_markup` leaves the message with
no inline keyboard. — _2026-10-03 · observed when editing a Rich Message (F-019); not checked separately for plain
messages · Bot API 10.3_ · Used in C-14.FR-15.

**F-066. Inline buttons take a colour.** An `InlineKeyboardButton` may carry `style`: `danger` (red), `success`
(green) or `primary` (blue); without it the app's own style is used ("Must be one of “danger” (red), “success” (green)
or “primary” (blue). If omitted, then an app-specific style is used"), added in Bot API 9.4 on 2026-02-09. `style` does
not set the type of a button, so it goes with `callback_data`. `sendMessage` to the channel with one button of each
style and one without answered `ok` and returned `style` in the message's `reply_markup`, and the clients showed the
styled buttons filled with their colour; `editMessageReplyMarkup` changed a button's style on the sent message, and
`editMessageText` with `reply_markup` sets the styles of its new keyboard too: after Acknowledge, Muster's edit of the
Root message showed "Unack" plain and "Resolve" green. — _2026-10-15 · one channel post with four buttons on the test
stand, its keyboard edited and restored, viewed in the client; the Bot API reference and changelog; 2026-10-16 · an
Alert Group acknowledged on the development stand, its Root message edited by Muster, viewed in the client · Bot API
10.3_ · Used in S-082.

**F-067. An unknown button style fails the request.** A `style` outside the three documented values, such as
`neutral` or a number, fails the request with
`400 Bad Request: can't parse InlineKeyboardButton: Invalid button style specified`; the error comes from reading the
keyboard, so the whole send or edit fails, not only the button. The undocumented `default` is accepted and means no
style (an edit that only added it answered `message is not modified`). A plain button is therefore sent without
`style`. — _2026-10-15 · edits of the keyboard of the post of F-066 on the test stand · Bot API 10.3_ · Used in
S-082.

### Notifications

**F-012. `disable_notification` means without sound.** A message sent with `disable_notification` still produces a
push, without sound (the client shows a muted bell). Editing a post produces no push. — _2026-10-03 · clients of both
test accounts · Bot API 10.3_ · Used in C-11.FR-7, C-14.FR-6.

**F-013. The copy rings the discussion group.** Members of the discussion group get a second push, with sound, for the
automatic copy of every channel post — even when the post itself was sent with `disable_notification`. — _2026-10-03 ·
client of the group member · Bot API 10.3_ · Used in C-14.FR-9.

**F-014. Comment threads reach only the discussion group.** A subscriber who did not join the discussion group is
notified about channel posts but not about a reply in a comment thread, even a Loud one that mentions them through a
`tg://user?id=` link. A member of the discussion group is notified about the copy of every post and about every reply
in its threads. — _2026-10-03 · two test accounts on the desktop client · Bot API 10.3_ · Used in C-14.FR-2,
C-14.FR-9.

**F-015. A reply inside the channel is a new post.** A bot message sent to the channel with `reply_parameters`
pointing at a channel post works: it is a new post in the channel feed that notifies subscribers, and it reaches the
discussion group as a separate copy, like any other post. — _2026-10-03 · two test accounts · Bot API 10.3_ · Used in
C-14.FR-2.

### Limits

**F-016. Sends and edits share one budget per chat.** In the discussion group, 20 sends passed about 1.5 s apart and the
21st, within 32 s, got `429` with `retry_after` 28. Alternating sends and edits, 10 of each passed and the 21st request,
within about 31 s, got `429` with `retry_after` 29: sends and edits count against one budget of about 20 per minute per
chat. — _2026-10-03 · bursts against the group · Bot API 10.3_ · Used in C-11.FR-3, C-14.FR-2.

**F-017. Editing one channel post fast.** 19 edits of one channel post passed within about 28 s, one every 1.6 s; the
20th got `429` with `retry_after` 14. An edit that changes nothing fails with `400 message is not modified`. —
_2026-10-03 · edits in a loop · Bot API 10.3_ · Used in C-11.FR-1, C-14.FR-7.

**F-065. A bot edits its own messages at any age.** The bot edited 48 of 48 of its own messages that were 124 to 144
hours old — in the channel and in the discussion group, with and without an inline keyboard. The TDLib source agrees:
`td/telegram/MessagesManager.cpp` sets `has_edit_time_limit = !(is_bot && m->is_outgoing)`, which exempts a bot's
outgoing messages from the edit time limit. The 48-hour limit of the
[Bot API documentation](https://core.telegram.org/bots/api) applies to `deleteMessage` in groups and to business
messages not sent by the bot. `400 Bad Request: Message can't be edited`, which Telegram also answers for forwarded
messages, reply keyboards and other users' messages, therefore does not mean that a Root message is too old: Muster
classifies it as gone. — _2026-10-09 · a live test with the test bot, and the TDLib source · cloud Bot API_ · Used in
C-14.FR-7, L1 open question 3.

**F-018. A second poller interrupts the first.** A second `getUpdates` on the same token ends the long poll already
running with `409 Conflict: terminated by other getUpdates request`; after that the two pollers take turns. —
_2026-10-03 · two pollers on one token · Bot API 10.3_ · Used in C-14.FR-1.

**F-131. `allowed_updates` stays with the bot until a `getUpdates` changes it.** A `getUpdates` with `allowed_updates`
`["message","edited_message"]`, made to wait for the automatic copy of a channel post, narrowed what the bot received
afterwards: the later `getUpdates` calls, which named no `allowed_updates`, returned no `callback_query`, so three
presses on a button of the bot's private message, about 4 to 9 minutes later, were never delivered, while a press in a
group chat about 5 minutes before that call had arrived. After the list was restored by hand with a `getUpdates` that
named every type (`message`, `edited_message`, `channel_post`, `edited_channel_post`, `my_chat_member`, `chat_member`
and `callback_query`), presses arrived again. The [documentation of `getUpdates`](https://core.telegram.org/bots/api#getupdates)
says so: "If not specified, the previous setting will be used". The setting belongs to the bot, so a tool that narrows
it narrows it for every later poller of the token; Muster's client sends its own list with every `getUpdates` and
`setWebhook`. — _2026-11-28 · presses before the narrowing call, after it and after the list was restored, during the
checks of F-132 to F-142 · Bot API 10.4 or later_ · Used in C-14.FR-1.

### Message format

**F-019. Rich Messages.** `sendRichMessage` sends a message from `rich_message.markdown` (or `html`, or `blocks`);
`disable_notification` and `reply_markup` work, and the answer returns the parsed blocks. The desktop client shows
headings, tables, collapsible sections and rules well; the copy in the discussion group is rich too, without buttons.
`editMessageText` with `rich_message` edits it and can switch between rich and plain text. Older clients were not
checked. — _2026-10-03 · posts viewed on desktop and phone · Bot API 10.3 (Rich Messages since 10.1)_ · Used in
C-14.FR-16 (not in L1).

**F-020. Tables do not fit a phone.** On a phone, a three-column table wraps words in the middle ("KubePodCrashL
ooping", "resol ved"); the desktop client shows the same table well. — _2026-10-03 · a Rich Message table viewed on
both · Bot API 10.3_ · Used in C-12.FR-1, C-14.FR-16.

**F-021. Numbers become links.** Clients show numbers such as `1791048401.81` as links, taking them for phone numbers.
Rich Messages can turn this off with `skip_entity_detection`. — _2026-10-03 · a label value in a test post · Bot API
10.3_ · Not addressed in L1.

### Members and Mentions in the discussion group

The entries of this section were checked on 2026-11-15 against Telegram's cloud Bot API 10.4 (the latest version at the
time, released 2026-11-09) on another test stand: a new test bot without a webhook, an admin of a test channel with
comments enabled and of its discussion group; and two test accounts, `alice`, who created the channel and the group, and
`bob`, a subscriber of the channel who joined the group, left it and joined it again during the check. The Bot API was
called with `curl` and its answers were logged as raw JSON, the bot token removed. Both accounts were signed in to one
Telegram Desktop client for macOS, and `bob` also to a phone client; the client versions were not recorded. Not checked:
a person the bot never saw, as no third account was at hand; a person removed or banned from the group; `alice` on a
phone; the clients' notification exceptions for a chat; and a comment sent with `disable_notification`.

**F-128. `getChatMember` in the discussion group answers an error for a subscriber of the channel who was never in it,
and `left` only a while after a leave.** As the bot, with the discussion group's chat id: `bob`, a subscriber of the
channel who had never been in the group, was answered HTTP `400` with
`{"ok":false,"error_code":400,"description":"Bad Request: PARTICIPANT_ID_INVALID"}`, not `left`, while the channel
answered `200` with `status` `member`. Once he had joined, the group answered `member`. He then left with "Leave group":
about 20 s later the group still answered `member`, and about 70 s later `200` with `status` `left`, which it kept. The
time of the leave was noted by hand, so the check cannot tell a late answer from a leave that took effect late. The
channel answered `member` for `bob` in every state of the group. `alice`, the creator, was answered `creator` with
`is_anonymous` `true` in the group, and `creator` with `is_anonymous` `false` in the channel. After `alice` changed her
privacy setting Groups & Channels (who may add her to groups and channels) from its default to My Contacts, the answers
about her were the same; no one else's privacy setting was changed. — _2026-11-15 · `getChatMember` in the group and in
the channel before `bob` joined, after he joined, about 20 s and about 70 s after he left, and after the privacy change
· Bot API 10.4_ · Used in C-27.FR-8, C-28.

**F-129. A Mention rings a member who muted the discussion group, but did not ring its anonymous creator.** The bot
posted in the channel and, under the automatic copy in the discussion group, commented with `sendMessage` (`parse_mode`
HTML, `reply_parameters` set to the copy, no `disable_notification`) whose text held `<a href="tg://user?id=…">…</a>`;
the answer returned it as a `text_mention` entity with the person's id, and the copy's id as `message_thread_id`. `bob`,
an ordinary member again by then, with the group muted (Mute forever): the comment that mentioned him made a
notification on the desktop client and on his phone, and the chat list showed the group's `@` badge. `alice`, the
creator and anonymous in the group (F-128), with the group muted on the desktop: the comment that mentioned her made no
notification and no `@` badge, only the grey unread counter. The check cannot tell whether she was not rung for being
anonymous or for being the creator. With the group not muted, a comment that mentioned `alice` made a notification on
the desktop; since a member is notified of every comment in a group not muted (F-014), this shows only that the client
showed her notifications, nothing about Mentions. With the group muted by `alice`, the automatic copy of the channel
post did not ring her, while the post in the channel rang on the desktop client (compare F-013); for `bob` the copy was
not watched. A separate setting to mute a chat except for Mentions was not looked for, since muting let `bob`'s Mention
through; whether the client can turn off Mentions in muted chats was not checked. Not checked: `alice` on a phone, an
admin who is not anonymous or not the creator, the automatic copy for an ordinary member who muted the group, the
clients' notification exceptions for a chat, and a comment with `disable_notification`. — _2026-11-15 · one channel post
and three comments under its copy, watched in the clients · Bot API 10.4, Telegram Desktop for macOS and a phone client,
versions not recorded_ · Used in C-27.FR-8, C-28, C-32.FR-11.

**F-130. A Mention rings a member's phone through a muted channel and discussion group, and a comment without one does
not.** On 2026-11-28, on the channel and the discussion group of this section, the bot posted in the channel with
`disable_notification` and, under the automatic copy, commented as in F-129, without `disable_notification`: once with
`<a href="tg://user?id=…">…</a>` of an ordinary member of the group, not its creator, and once without a Mention. The
member's phone was locked, with Telegram for iOS 13.3 (37637) in the background. With neither the channel nor the group
muted, both comments pushed at once and showed their text, as every comment does for a member (F-014). With both
muted, the comment without a Mention made no push, and the comment that mentioned the member pushed at once and showed
its text. A first pair of runs, with the group muted but not the channel, is not counted: the push of the channel post
was taken for that of the comment. F-129 saw a Mention ring a member who muted the group, on the desktop and a phone;
this check adds the muted channel, the comment without a Mention as a control, and the client's version. Not checked
for comments: the anonymous creator on a phone, a comment with `disable_notification`, the clients' notification
exceptions for a chat, and a phone in a Focus mode (F-134 is a private chat in one). — _2026-11-28 · two channel posts and four comments under their copies, watched on the locked
phone · Bot API 10.4 or later, Telegram for iOS 13.3 (37637)_ · Used in C-32.FR-11, C-32.FR-20.

### Private chats

The entries of this section and of the next two were checked on 2026-11-28 against Telegram's cloud Bot API — 10.4 or
later, since `getMe` does not tell the version — with a test bot without a webhook and with privacy mode on (`getMe`
answered `can_read_all_group_messages` `false`). The chats were a forum supergroup, in which the bot was first an admin
allowed to manage topics, then an admin without that right, then an ordinary member; a basic group chat, created as a
group without topics or a public link, in which the bot was an ordinary member; and the channel and discussion group of
the section above. Two test accounts took part: `alice`, an ordinary member of the chats who had pressed Start in the
bot, with Telegram for iOS 13.3 (37637) on a locked phone; and `bob`, the creator of the basic group chat, who pressed
Start only where an entry says so. The names follow the roles of this check, not those of F-128 and F-129, where `alice`
is the creator of the channel and its group. People made their changes in Telegram for macOS 13.2.0.283494. The Bot API was called
with `curl`, its answers were logged as JSON with the token removed, and updates were read with `getUpdates`.

**F-132. What a private chat answers before Start, after a block and for an unknown id.** `sendMessage` to `bob`, who
had never pressed Start in the bot, answered `403` `Forbidden: bot can't initiate conversation with a user`. Yet
`getChat` with his id answered `200` with `type` `private`, and `getChatMember` with his id as the chat and as the user
`200` with `status` `member`; he shared a group chat with the bot, so neither read tells whether a person pressed Start.
After `bob` pressed Start and then blocked the bot, `sendMessage` and `sendChatAction` both answered `403`
`Forbidden: bot was blocked by the user`, so a chat action tells a block without sending a message. An id that is no
chat answered `400` `Bad Request: chat not found`. Not checked: a deleted account. — _2026-11-28 · the test bot and the
two accounts · Bot API 10.4 or later_ · Used in C-43.FR-12, C-43.FR-13, C-43.FR-17.

**F-133. Buttons, edits and copies in a private chat.** A press of `alice` on an inline button of the bot's private
message to her arrived as a `callback_query` with `message.chat.type` `private`, `from` set to her, a `chat_instance`
and the button's `callback_data`, and `answerCallbackQuery` answered `true`; three earlier presses were lost to F-131.
`editMessageText` without `reply_markup` left the message without buttons, and with `reply_markup` gave them back
(compare F-011). The buttons stay on the message the bot sent: `forwardMessage` of it into a group answered a message
without `reply_markup`, and `copyMessage`, which answers only the new `message_id`, made a copy that the client showed
without buttons, as it showed the forwarded one; a copy that `alice` forwarded by hand had none either. Not checked: an edit of a private message days old (F-065 checked a channel and a
group). — _2026-11-28 · private messages with two buttons, presses on the phone, two edits, a forward and a copy by the
bot and a forward by hand · Bot API 10.4 or later_ · Used in C-43.FR-7, C-43.FR-8, C-43.FR-14.

**F-134. The push of a private message.** The bot sent private messages to `alice` without `disable_notification`,
her phone locked. With the app killed, the push came about 10 s after the send and showed the text; with the chat
muted in the app, no push came; with a Focus mode of the phone on, the push came at once, without sound. An edit of a
private message made no push, as F-012 found in a channel. — _2026-11-28 · three messages and one edit, watched on the
phone · Bot API 10.4 or later, Telegram for iOS 13.3 (37637)_ · Used in C-43.FR-8, C-43.FR-13.

**F-135. A private chat passed about 1.4 requests a second for 30 s.** One client, sending one request after another,
each with `disable_notification`, for 30 s: 43 `sendMessage` to one private chat and 39 `editMessageText` of one message
there (about 1.4 and 1.3 a second), and 41 sends alternating between the private chats of `alice` and `bob` (about 0.7
a second each), all answered `200`, none `429`. One chat thus took more than the one message a second that Telegram's
[Bots FAQ](https://core.telegram.org/bots/faq#my-bot-is-hitting-limits-how-do-i-avoid-this) asks for in one chat. The
limit was not reached, so the budget of a private chat, and whether edits count in it as in a group (F-016), are not
known; the budget across chats was not approached. — _2026-11-28 · three bursts of 30 s · Bot API 10.4 or later_ ·
Used in C-43.FR-10, C-43.FR-11, P-98.

### Forum topics

**F-136. Sending into a forum, and its General topic.** `getChat` on the forum answered `type` `supergroup` with
`is_forum` `true`; for the discussion group and the basic group chat it has no `is_forum`. A `sendMessage` with a
topic's `message_thread_id` landed in the topic, and its answer carried `message_thread_id` and `is_topic_message`
`true`; a reply with `reply_parameters` landed in the topic of the message it answered, with or without
`message_thread_id`, and its answer named the topic too; so did the answer to `editMessageText` of a topic message. A
`sendMessage` without `message_thread_id` landed in the General topic, and its answer had neither field.
`message_thread_id` `1` was refused with `400` `Bad Request: message thread not found` whether General was open, closed
(`closeGeneralForumTopic`) or hidden (`hideGeneralForumTopic`), while a send without `message_thread_id` was accepted in
each of these states by the bot as an admin allowed to manage topics; where the client showed the sends with General
closed or hidden was not recorded clearly. `editGeneralForumTopic` with the name that General already had answered `400`
`Bad Request: TOPIC_NOT_MODIFIED`. `sendChatAction` with a topic's `message_thread_id` answered `true`, and the client
showed "typing" in that topic. — _2026-11-28 · three runs of the same sends, looked at in the client; General closed,
reopened, hidden and shown again by the bot · Bot API 10.4 or later_ · Used in C-35.FR-2, C-35.FR-3, C-35.FR-5.

**F-137. Creating a topic needs the right to manage topics.** `createForumTopic` by the bot as an admin whose
`getChatMember` had `can_manage_topics` `true` answered a result with `message_thread_id`, `name` and `icon_color`; as an
admin without that right, and as an ordinary member, it answered `400` `Bad Request: not enough rights to create a
topic`. With the right, `closeForumTopic` and `deleteForumTopic` answered `true`. — _2026-11-28 · the bot in its three
roles · Bot API 10.4 or later_ · Used in C-35.FR-4.

**F-138. A deleted topic, and a closed one.** After the bot deleted a topic with `deleteForumTopic`, a send and a reply
with its `message_thread_id` answered `400` `Bad Request: message thread not found`, a reply with only `reply_parameters`
to a message in it `400` `Bad Request: message to be replied not found`, and `editMessageText` of such a message `400`
`Bad Request: message to edit not found`; the answers were the same with the bot an admin with and without the right to
manage topics and an ordinary member, and the client showed none of those messages. A topic that the bot had created
and then closed with `closeForumTopic` took the bot's sends, replies and edits in all three roles, and they landed in it.
In a topic that a person created and closed, the bot as an ordinary member got `400` `Bad Request: TOPIC_CLOSED` for a
send and for a reply. As far as the check tells, the difference is who created the topic; the bot as an admin was not
tried in a topic that a person closed. `sendChatAction` with the `message_thread_id` answered `true` for the deleted
topic and for both closed ones, so a chat action tells neither a deleted nor a closed topic. — _2026-11-28 · one live,
one closed and one deleted topic, probed in each role of the bot, and a topic a person created and closed · Bot API 10.4
or later_ · Used in C-35.FR-12, C-35.FR-14.

**F-139. Topic service messages reach a bot with privacy mode on.** When a person created, renamed, closed and reopened
a topic, the bot received `forum_topic_created`, `forum_topic_edited`, `forum_topic_closed` and `forum_topic_reopened`
as `message` updates, both as an admin and as an ordinary member; a person's ordinary message in the forum reached the
bot as an admin and not as an ordinary member, as privacy mode says. — _2026-11-28 · the same changes by hand with the
bot an admin and a member · Bot API 10.4 or later_ · Used in C-35.FR-14.

**F-140. Turning topics off.** After a person turned Topics off for the forum, `getChat` still answered `type`
`supergroup`; a `sendMessage` with a former topic's `message_thread_id` and one without it both answered `200` without
`message_thread_id`; a reply to a message of a former topic answered `200` with that topic's `message_thread_id` and
`is_topic_message` `true`, and so did `editMessageText` of such a message. The client showed all the messages in one
stream; when Topics was turned on again, the topics came back. — _2026-11-28 · sends, a reply and an edit after Topics
was turned off · Bot API 10.4 or later_ · Used in C-35.FR-14, C-35.FR-16.

### Basic group chats

**F-141. A basic group chat with the bot an ordinary member.** `getChat` answered `type` `group` with no `is_forum`, no
`username` and the chat's `permissions` (`can_send_messages` `true`, and the others), and `getChatMember` of the bot
`{"status":"member"}` with no other field, so whether a member bot may send comes from the chat's `permissions`; a
restricted bot was not tried. For a private chat, `getChat` answered `type` `private` with no `permissions`. A press of
`alice` on a button of the bot's message reached the bot, with privacy mode on, as a `callback_query` with
`message.chat.type` `group`. After the bot deleted one of its messages, a reply to it answered `400`
`Bad Request: message to be replied not found`, and with `allow_sending_without_reply` the reply was sent as an
ordinary message, as F-008 found in a discussion group. A message with `disable_notification` pushed to `alice`'s phone
at once and showed its text, without sound (F-012); with the group muted by her, a message that mentioned her through
`tg://user?id=` pushed at once and showed its text, in a single run with no control message without a Mention. Not
checked: an edit of a message days old (F-065); what `getChatMember` answers about a person who was never in the group,
since the account meant for it was the group's creator; and a Mention of a person outside the group. — _2026-11-28 ·
reads, a press, a deleted message, two messages watched on the phone · Bot API 10.4 or later, Telegram for iOS 13.3
(37637)_ · Used in C-35.FR-6, C-35.FR-8, C-35.FR-11, C-35.FR-12, C-35.FR-17.

**F-142. A basic group chat that becomes a supergroup.** A person turned the basic group chat into a supergroup by
giving it a public link. The bot received two `message` updates: one in the old chat with `migrate_to_chat_id` set to
the new chat's id, and one in the new chat with `migrate_from_chat_id` set to the old one. A `sendMessage` to the old
chat id answered `400` `Bad Request: group chat was upgraded to a supergroup chat` with `parameters.migrate_to_chat_id`
set to the new id. `editMessageText` of the bot's earlier message through the old chat id and its message id answered
`200`, with `chat.type` `group`; through the new chat id with the same message id it answered `400`
`Bad Request: message to edit not found`, and a reply in the new chat to that message id `400`
`Bad Request: message to be replied not found`: the bot reaches its old messages only through the old chat id. The bot
stayed an ordinary member of the new chat, whose `getChat` answered `type` `supergroup` with a public `username`. A
press on the old message's button after the change was not checked: the edit through the old chat id, sent without
`reply_markup`, had removed the button first (F-011). — _2026-11-28 · two messages before the change, then a send,
edits, a reply and reads after it · Bot API 10.4 or later_ · Used in C-35.FR-14, C-35.FR-15, C-35.FR-16.

## Mattermost

Unless an entry says otherwise, the facts were checked on 2026-10-04 against a test Mattermost 11.2.2 server
(Enterprise Edition without a license, default configuration unless an entry says otherwise). A test bot with the system
admin role used the REST API v4 in a test channel; a small HTTP receiver on a private address logged button presses as
raw JSON and answered them. Presses and notifications were made and observed in the client of a test user.

F-143 to F-148 were checked on 2026-11-22 and 2026-11-28 on the test stand's second Mattermost server, also 11.2.2
(build 20860305195), an Enterprise build under the Entry licence that F-121 read on 2026-11-15 (not read again here),
now with push notifications on through Mattermost's hosted
push notification service in the EU (`https://eu.push.mattermost.com`) and `PushNotificationContents` `full`. A test
bot with the role Member (`system_user`) called the API; the test person `alice` had the Mattermost app on an iPhone,
its version not recorded, with her desktop client closed; and a small HTTP receiver on a private address that the server
allows (F-022) logged presses.

### Buttons

**F-068. Attachment buttons take a style, which tints them.** A button in `props.attachments[].actions[]` of a post
made through `POST /api/v4/posts` may carry `style`: `default`, `primary`, `success`, `good`, `warning` or `danger`,
or a hex colour or a theme variable, which the documentation discourages. The server returned every value in the
created post. It checks the value but, in 11.2.2, only logs "Invalid post props. In a future version this will result
in an error" and keeps the post. The clients do not fill the button: the label takes the colour, and the background the
same colour at 8 % opacity; on the web `good` and `success` are the same green (`#339970`), `primary` is the theme's
button colour, `danger` its error colour and `default` its text colour, and a button without `style` stays plain; the
mobile app shows a button without `style` as `default`. An edit of the post sets the styles of its new buttons: after
Acknowledge, Muster's edit of the Root message (`PUT /api/v4/posts/{post_id}/patch`) showed "Unack" plain and "Resolve"
tinted green. — _2026-10-15 · one post with seven buttons on the test stand, viewed in the client; the interactive
messages and message attachments documentation; `PostAction.IsValid` and `Post.ValidateProps` in `server/public/model`
and `action_button.tsx` of the v11.2.2 source, and `app/utils/message_attachment.ts` of the mobile app; 2026-10-16 · an
Alert Group acknowledged on the development stand, its Root message edited by Muster, viewed in the client · Mattermost
11.2.2_ · Used in S-082.

### Button presses and answers

**F-022. A private callback address must be allowed.** The server calls a button's URL on a private address only if
the address is listed in `ServiceSettings.AllowedUntrustedInternalConnections`. Otherwise the person sees only a generic
`400` "Action integration error", and only the server log says why ("address forbidden … in a reserved range and not
in AllowedUntrustedInternalConnections"). — _2026-10-04 · presses before and after allowing the receiver's address ·
Mattermost 11.2.2_ · Used in C-13.FR-7, C-13.FR-13, C-16.FR-3.

**F-023. What a press sends.** The server posts JSON with `user_id`, `user_name`, `channel_id`, `channel_name`,
`team_id`, `team_domain`, `post_id`, `trigger_id`, `type`, `data_source` and the button's `context`. An answer with
`update` (a post with `props.attachments`) edits the post at once and marks it "Edited". — _2026-10-04 · requests
logged by the receiver; Ack, Unack, Resolve and Unresolve answered with `update` · Mattermost 11.2.2_ · Used in
C-13.FR-4.

**F-024. No `goto_location`.** The answer to a press has only `update`, `ephemeral_text` and `skip_slack_parsing`;
nothing in it can open a URL, so links have to be in the post. — _2026-10-04 · `PostActionIntegrationResponse` in the
server source of 11.2.2 · Mattermost 11.2.2_ · Used in C-13.FR-3.

**F-025. `ephemeral_text` goes to the thread.** The server posts an answer's `ephemeral_text` as an ephemeral reply in
the thread of the pressed post, from "System" and without a push. With collapsed reply threads (`CollapsedThreads`
`always_on`, the default) it is not shown in the channel view, only in the open thread. — _2026-10-04 · presses
answered with `ephemeral_text`; the server source · Mattermost 11.2.2_ · Used in C-13.FR-4.

**F-026. A separate ephemeral post shows in the channel.** `POST /api/v4/posts/ephemeral` for the person, without
`root_id`, appears at once in the channel view, marked "(Only visible to you)". Ephemeral posts in a thread stay in the
client until the page is reloaded: a thread opened later still showed them. — _2026-10-04 · ephemeral posts with and
without `root_id` · Mattermost 11.2.2_ · Used in C-13.FR-4. The test bot had the system admin role; a bot without it
is refused (F-063), so Muster then answers with `ephemeral_text` (F-062).

**F-062. `ephemeral_text` needs no permission of the bot.** An integration may answer a press with `ephemeral_text`,
which Mattermost shows only to the person who pressed ("the integration can choose to update the original post, and/or
respond with an ephemeral message"; `skip_slack_parsing: true` keeps the text from the Slack-compatibility parsing).
The server posts it as an ephemeral post of that person in the channel of the press, with the pressed post as `root_id`
— or that post's `root_id` for a Thread reply — through its internal `SendEphemeralPost`, after the integration
answered and without any permission check, so it works for a bot with the role Member; where it shows is F-025. —
_2026-10-08 · Mattermost's
[interactive messages documentation](https://docs.mattermost.com/developers/integrate/plugins/interactive-messages)
("Integration response to button press"); `DoPostActionWithCookie` in `server/channels/app/integration_action.go` of
the v11.2.2 source · Mattermost 11.2.2_ · Used in C-13.FR-4.

**F-063. A bot without the system admin role cannot make ephemeral posts.** `POST /api/v4/posts/ephemeral` needs the
`create_post_ephemeral` permission, which by default only the system admin role holds. The test stand's bot, which
has only the system user role as in F-060, asked for an ephemeral post in a channel it is a member of, targeted at its own user id, and
got `403` `api.context.permissions.app_error` "You do not have the appropriate permissions."; the server checks the
permission after reading the body and before it looks at the channel. A press answered only with a separate ephemeral
post therefore never reaches the person. — _2026-10-08 · one request with the bot token on the test stand; the check in
`createEphemeralPost` of `server/channels/api4/post.go`, and in `server/public/model` of the v11.2.2 source the
permission among those only `system_admin` gets · Mattermost 11.2.2_ · Used in C-13.FR-2, C-13.FR-4, C-13.FR-7.

**F-064. A bot can read whether its roles grant a permission.** The server decides `create_post_ephemeral` for a
request with `SessionHasPermissionTo`, which asks `RolesGrantPermission` whether any of the session's system roles that
is not deleted lists the permission; a bot token's session carries the bot's roles, which `GET /api/v4/users/me`
returns in `roles` (`system_user` for the test stand's bot, read on 2026-10-08). `POST /api/v4/roles/names` with those
names reads the same roles through the same `GetRolesByNames`, and needs only a session, no permission, so the bot can
make the server's decision itself without trying an ephemeral post. On the test stand, `POST /api/v4/roles/names` with
`["system_user"]` and the bot's token answered `200` with one role, not deleted, whose 13 permissions do not include
`create_post_ephemeral`, and the Connection check of that bot passed with the warning `press_answers_in_thread`. The
warning names the bot it checked and the roles it read, since they are those of the account the token belongs to;
after `mmctl roles system-admin` of that bot, `GET /api/v4/users/me` read `system_user system_admin` and the check
passed with no warning. —
_2026-10-08 · `SessionHasPermissionTo` and `RolesGrantPermission` in `server/channels/app/authorization.go` and
`getRolesByNames` in `server/channels/api4/role.go` of the v11.2.2 source; `GET /api/v4/users/me` and
`POST /api/v4/roles/names` with the bot token on the test stand, and Muster's Connection check against it;
2026-10-22 · the same reads and check on the test stand again, after `mmctl roles system-admin` of the bot (#365) ·
Mattermost 11.2.2_ · Used in C-13.FR-2.

**F-054. A bot can press its own button.** `POST /api/v4/posts/{post_id}/actions/{action_id}` with the bot's token
answers `200` with `status` `OK` and a `trigger_id`, and the server then calls the button's URL as for any press, with
the bot's `user_name`. — _2026-10-04 · live test, a press on a bot post through the API · Mattermost 11.2.2_ · Used in
C-13.FR-13, C-16.FR-3.

**F-055. Presses on Thread replies are accepted.** The server accepts a press on a button of a post with `root_id` and
calls the button's URL as for a root post. How the client shows such buttons was not checked by eye. — _2026-10-04 ·
live test, a press on a Thread reply through the API · Mattermost 11.2.2_ · Used in C-13.FR-4, C-17.FR-4.

### Threads and direct messages

**F-027. Thread replies and followers.** A bot post with `root_id` appears in the thread and raises the root post's
reply count; a reply that mentions a user makes that user a follower of the thread. — _2026-10-04 · bot replies with
and without a mention · Mattermost 11.2.2_ · Used in C-13.FR-3, C-13.FR-7.

**F-028. Direct messages by user id.** `POST /api/v4/channels/direct` with the bot's and a user's ids returns their
direct channel, and a bot post there reaches the user. — _2026-10-04 · a direct message to the test user · Mattermost
11.2.2_ · Used in C-13.FR-7, C-18.FR-3.

**F-144. A person's press in a direct channel.** The bot made its direct channel with `alice` (`POST
/api/v4/channels/direct`, `201`, `type` `D`) and posted an attachment with one button whose integration URL was the
receiver's. Each of her four presses made the server call that URL with the keys of F-023 — `user_id`, `user_name`,
`channel_id`, `channel_name`, `team_id`, `team_domain`, `post_id`, `trigger_id`, `type`, `data_source` and the button's
`context` — where `channel_id` was the direct channel, `channel_name` the two users' ids joined by `__`, `user_id` hers
and `post_id` the bot's post, and `team_id`, `team_domain`, `type` and `data_source` were empty strings. The receiver
answered with `ephemeral_text`; the client showed no error, and she saw neither a change of the post nor the ephemeral
text in the direct channel (where `ephemeral_text` goes: F-025). With the role Member, the bot edited its own post there
with `PUT /api/v4/posts/{id}/patch` (`200`). — _2026-11-22 · four presses, logged by the receiver · Mattermost 11.2.2_ ·
Used in C-43.FR-13, C-43.FR-14, C-43.FR-16.

**F-145. A direct channel with a deactivated or unknown user.** For a deactivated user, `POST /api/v4/channels/direct`
answered `201` with the direct channel, and a post there `201`: nothing in the answers tells that the person will not
read it. For an id that names no user it answered `400` `api.channel.create_direct_channel.invalid_user.app_error`
("Invalid user ID for direct channel creation."), and a post with a malformed `channel_id` `403`
`api.context.permissions.app_error`. Not checked: a well-formed `channel_id` that names no channel, and a bot whose role
lacks the permission to create direct channels. —
_2026-11-28 · the bot with the role Member · Mattermost 11.2.2_ · Used in C-43.FR-12, C-43.FR-17.

**F-146. The push of a direct message.** An edit of the bot's direct post with `PUT /api/v4/posts/{id}/patch` made no
push to `alice`'s locked phone, as F-029 found for a Mention added by an edit, and a new direct post made none while Do
Not Disturb was on (the record does not say whether her Mattermost status or the phone's). A direct post with Do Not
Disturb off was sent, but what the phone showed was not recorded, and `generic` was not tried. — _2026-11-22 and
2026-11-28 · one edit and two posts, watched on the phone · Mattermost 11.2.2, `PushNotificationContents` `full`_ · Used
in C-43.FR-8, C-43.FR-13.

### Edits and notifications

**F-029. Edits never notify; a new post with a Mention does.** An edit with `PUT /api/v4/posts/{id}/patch` that added
an `@`-mention of a user did not notify that user, in two tries; a new post with the same mention did. Phone push was
not checked: the test server had push notifications off. — _2026-10-04 · two edits and a control post, watched in the
mentioned user's client · Mattermost 11.2.2_ · Used in C-11.FR-7, C-13.FR-8.

**F-056. A Mention inside an attachment notifies, but the notification shows nothing.** A bot post whose `@`-mention
of a user was only in the text of its attachment, with the post's `message` empty, notified that user ("mentioned
you"), but the notification showed no content. A post with the same mention in its `message` above the attachment
notified with the text and the attachment shown. Phone push was not checked. — _2026-10-04 · two posts, the
notifications received by email by the mentioned user · Mattermost 11.2.2_ · Used in C-12.FR-1, C-13.FR-3,
C-13.FR-8.

**F-113. `@here`, `@channel`, `@all` and a user mention inside an attachment all notify.** In a channel with the bot
and a test user whose status was online, the bot made posts, each with an empty `message` and one attachment, and the
test user's `mention_count` was read with `GET /api/v4/channels/{channel_id}/members/{user_id}` after each: the
attachment text with `@here` raised it from 0 to 1, with `@channel` from 1 to 2, with `@<username>` of the test user
from 2 to 3, and a control post without a mention left it at 3; in a second run, the attachment text with `@all`
raised it from 0 to 1. `@here` notifies only the members who are online, so the online status matters for it. A group
mention (`@<group>`) inside an attachment was not checked here; F-123 checked it later. What the notifications showed
was not checked: an email of such a post shows no content (F-056), and push and desktop notifications show the
attachment's `fallback` (F-115). —
_2026-10-22 · bot posts in one channel, the mention count read after each · Mattermost 11.2.2_ · Used in C-12.FR-1,
C-13.FR-3, C-13.FR-8.

**F-114. An edit with an empty `message` clears the post's text.** A bot post created with `message` "🔴 #1 …" and an
attachment was edited with `PUT /api/v4/posts/{id}/patch` and the body `{"message": "", "props": {"attachments":
[…]}}`; the server answered `200`, and `GET /api/v4/posts/{id}` then gave `message` "" with the new attachment text
and an `edit_at` above 0. So the next edit of a Root message posted with a summary line in its `message` removes that
line. — _2026-10-22 · one post created and edited by the bot, read back · Mattermost 11.2.2_ · Used in C-13.FR-3.

**F-115. Notifications of a post with an empty `message` show the attachment's `fallback`, except email.** Read in
the source of Mattermost v11.2.2, not measured. Push: `buildFullPushNotificationMessage`
(`server/channels/app/notification_push.go:853-864`) starts from the post's `message` with its Markdown stripped and
adds a new line and the `fallback` of every attachment that has one; under `PushNotificationContents` `full`,
`getPushNotificationMessage` (`:269-274`) sends the sender's name, `: ` and that text, and under `generic`
(`:276-310`) a fixed text such as "<sender> notified the channel." or "<sender> mentioned you.", with no content of the
post either way. Desktop: `getNotificationBody` in the web app
(`webapp/channels/src/actions/notification_actions.tsx:216-251`) shows the post's `message`, and when it is empty the
first attachment's `fallback`, otherwise its `pretext`, otherwise its `text`; without a `fallback`, a Loud Root
message's notification would show its Mentions line and the body. Email: the body of the email comes from the post's
`message` (`server/channels/app/notification_email.go:97-101`), and the attachments are added only when that is not
empty (`:355-369`), so an email of a post with an empty `message` has no content whatever its `fallback`, as F-056
measured. The channel does not draw `fallback`. — _2026-10-22 · read in the source at tag v11.2.2 · Mattermost 11.2.2
source_ · Used in C-12.FR-1, C-13.FR-3.

**F-060. A username without `@` does not notify.** A bot post whose `message` and attachment text both ended with
"Acknowledged by <username>" — the username of a channel member, written without `@` and mentioned nowhere else, as
the footer of a Root message named a person before C-12.FR-12 was revised to write a Mention — left that member's
`mention_count` at `0`: it was read with `GET /api/v4/channels/{channel_id}/members/{user_id}` before the post and
again 3 s after. The member kept their own notification settings; a person who adds their username to their own
mention keywords would still be notified. — _2026-10-08 · one post in a public channel with a bot without the system
admin role, deleted afterwards · Mattermost 11.2.2_ · Used in C-12.FR-12, C-13.FR-3.

**F-143. A Mention in a bot's Thread reply pushes to the phone, whether the person follows the Thread or not.** In a
channel `alice` is a member of, the bot posted a root post with `@alice`; then, under a root post that did not mention
her, a Thread reply with `@alice` while she did not follow the Thread, a second one after she had replied in it, and a
Thread reply without a Mention while she followed it. Her locked phone, the app in the background, showed a push for
each at once, with the text of the post, under `PushNotificationContents` `full`; so once she follows a Thread, a reply
that does not mention her pushes too. Not measured: `generic`, whose push shows a fixed text instead of the post
(F-115, read in the source), since the stand stays on `full`; the app killed; and Do Not Disturb, under which a direct
message did not push (F-146). — _2026-11-22 · two root posts and three Thread replies, watched on the phone ·
Mattermost 11.2.2, the hosted push notification service_ · Used in C-32.FR-11, C-32.FR-20.

### Rate limit and server settings

**F-030. The rate limit is off by default and counted per client address.** `RateLimitSettings.Enable` is `false` by
default, and turning it on takes a server restart: before the restart, 180 requests in 8.8 s got no `429`. Once on, it
allows 10 requests per second with a burst of 100 per client address (`VaryByRemoteAddr`), so all requests from one
address share one bucket: of 300 parallel requests, 110 got `200` and 190 got `429` within 1.2 s. — _2026-10-04 ·
bursts with the limit off and on · Mattermost 11.2.2_ · Used in C-13.FR-5, C-13.FR-7,
`connection.mattermost.limiter`, `destination.mattermost.limiter`.

**F-031. The `429` answer.** It is `text/plain` "limit exceeded", not JSON, with `Retry-After: 1`,
`X-Ratelimit-Limit: 101`, `X-Ratelimit-Remaining` and `X-Ratelimit-Reset`; `200` answers carry the same
`X-Ratelimit-*` headers. — _2026-10-04 · the burst of F-030 · Mattermost 11.2.2_ · Used in C-13.FR-5.

**F-032. Server defaults around presses and edits.** The server waits for the answer to a press for up to
`OutgoingIntegrationRequestsTimeout`, 30 s by default. `PostEditTimeLimit` is `-1` by default, so posts can be edited at
any age; an admin can set a limit, and what an edit of an older post then gets was not checked. A bot token works with
`EnableUserAccessTokens` off, its default. — _2026-10-04 · the server configuration; calls with the bot token ·
Mattermost 11.2.2_ · Used in C-13.FR-4, C-13.FR-7, [L1 open question 12](prd/L1.md#52-other-questions).

**F-147. 239 posts in 15 s to one direct channel.** The bot posted to its direct channel with `alice` one post after
another for 15 s, from one client address: all 239 answered `201`, none `429`, about 16 a second — which the limit of
F-030, if it were on, would also allow with its burst of 100. The stand's rate limit settings were not read. —
_2026-11-28 · one burst · Mattermost 11.2.2_ · Used in C-43.FR-10, C-43.FR-11.

### Requests without a token

**F-069. The system ping answers without a token.** `GET /api/v4/system/ping` without an `Authorization` header answers
`200` with `Content-Type: application/json` and an object whose `status` is `OK`, with other fields such as
`ActiveSearchBackend`; `GET /api/v4/users/me` without a token answers `401`. — _2026-10-18 · both Mattermost servers of
the test stand, without a token · Mattermost 11.2_ · Used in C-13.FR-2.

### Post length and deleted posts

**F-057. The post length limit.** The server's longest post `message` is 16,383 characters; it reports the limit as
`MaxPostSize` in `GET /api/v4/config/client?format=old`. A post whose `message` has 16,384 characters is refused with
`400` and the id `model.post.is_valid.message_length.app_error`. — _2026-10-04 · the client configuration, then a post of
16,384 characters · Mattermost 11.2.2_ · Used in C-13.FR-3.

**F-058. What a deleted root post answers.** After a root post is deleted, a reply with its id as `root_id` is refused
with `400` `api.post.create_post.root_id.app_error` ("Invalid RootId parameter"), and an edit of it with `403`
`api.context.permissions.app_error` — not `404`. `GET /api/v4/posts/{id}` answers `404` (`app.post.get.app_error`); with
`?include_deleted=true` it answers `200` with the post and its `delete_at`. Reading deleted posts probably needs the
system admin role, which the test bot had, so Muster relies only on the `404` of the plain read (see
[Pending](#pending)). — _2026-10-04 · a deleted root post, then a reply, an edit and both reads with the test bot's
token (the bot has the system admin role) · Mattermost 11.2.2_ · Used in C-13.FR-5, C-13.AC-12.

**F-061. A bot without the system admin role gets `404` for its deleted post.** After the bot of F-060, which has
only the system user role, deleted its own post, the plain read `GET /api/v4/posts/{id}` of it answered `404`, as in
F-058. — _2026-10-08 · the post of F-060, deleted through the API and read again · Mattermost 11.2.2_ · Used in
C-13.FR-5.

### Attachment fields

**F-148. An attachment draws its fields under its text, whatever their order in the request.** The bot made three posts
with an empty `message` and one attachment each, built as text so that the request kept the order of the properties: A
with `fields` before `text` and B with `text` before `fields`, each with two short fields, `CPU` and `Memory`, and one
that is not short, `Disk`; and C with `fields` first and five fields, `a` short, `b` not, `c` and `d` short, `e` not.
It then edited B with `PUT /api/v4/posts/{id}/patch` to `fields` before a changed `text` (`200`, `edit_at` above 0).
`GET /api/v4/posts/{id}` returned each attachment with its properties in one order, whatever the request's: `id`,
`fallback`, `color`, `pretext`, `author_name`, `author_link`, `author_icon`, `title`, `title_link`, `text`, `fields`,
`image_url`, `thumb_url`, `footer`, `footer_icon`, `ts`. The web client, 1,280 and 360 px wide, and the iPhone app drew
A, B and the edited B alike — `CPU` and `Memory` side by side in two columns, `Disk` on a row of its own — and C as `a`,
then `b`, then `c` and `d` side by side, then `e`: a short field shares a row only with the next short field. The
observer recorded the columns, not where the fields stood against the text; that comes from the web app's source:
`message_attachment.tsx` at tag v11.2.2 renders the pretext, the author, the title, the text, the image, the fields, the
footer and the buttons in that order, and its `getFieldsTable` starts a new row after two short fields and before and
after a field that is not short. So neither the order of the properties in a request nor an edit moves the fields, and
the source draws them under the text. — _2026-11-22 · three posts and one edit,
looked at in the web client and the phone app; the web app's source at tag v11.2.2 · Mattermost 11.2.2, the client
versions not recorded_ · Used in C-38.FR-6, S-211.

### Groups

The entries of this section were checked on 2026-11-15 against a test Mattermost 11.2.2 server with the Entry licence,
unlike the section's other entries (Enterprise Edition without a licence, a bot with the system admin role). A test bot
with the role Member (`system_user`) only, as Muster's bot is created, used the REST API v4 in a test channel with
`curl`; an admin created a custom group `payments_team` whose one member was a test account, and that account's
`mention_count` in the channel was read with `GET /api/v4/channels/{channel_id}/members/{user_id}` before each post and
4 s after it. The posts were looked at in the web client in a browser, with collapsed threads always on. Not checked:
LDAP-synced groups (LDAP was off on the server), phones (push notifications were off on the server), a channel member
outside the group, and desktop notification banners (none was recorded).

**F-121. Custom groups exist under the Entry licence; LDAP groups were not checked.** The server answered
`BuildEnterpriseReady` `true` in `GET /api/v4/config/client?format=old` and, in the client licence, `IsLicensed` `true`,
`SkuShortName` `entry`, `Cloud` `false`, `LDAP` `true` and `LDAPGroups` `true`; its System Console describes the Entry
licence as Enterprise Advanced capabilities with limits. Under it, an admin created a custom group with
`POST /api/v4/groups` (`source` `custom`, `allow_reference` `true`), answered `201`. LDAP was in the licence but off in
the server's configuration (`LdapSettings.Enable` `false`), so whether LDAP-synced groups exist and behave as custom
groups do was not checked; nor were Team Edition, an Enterprise build without a licence, or the Professional and
Enterprise licences. — _2026-11-15 · the client configuration and licence, read three times, and the group's creation ·
Mattermost 11.2.2, Entry licence_ · Used in C-25.FR-8, C-28.FR-2.

**F-122. A bot with the role Member reads the groups and their members.** With the bot's token, and no permission added
to it: `GET /api/v4/groups?per_page=10` answered `200` with an array whose group has `id`, `name`, `display_name`,
`description`, `source` (`custom`), `remote_id` (null), `create_at`, `update_at`, `delete_at` (`0`), `has_syncables`,
`allow_reference` (`true`) and `member_ids` (null), and no `member_count`; `?filter_allow_reference=true` answered `200`
with the same group; `?source=custom&include_member_count=true` answered `200` with `member_count` `1`; and
`GET /api/v4/groups/{group_id}/members?per_page=10` answered `200` with the member.
`?source=ldap&include_member_count=true` also answered `200` with the custom group, so with LDAP off the `source` filter
did not filter, and a reader goes by each group's `source`. `GET /api/v4/groups/{group_id}/stats` was refused with `403`
`api.context.permissions.app_error`, which names no permission. The bot's role `system_user`, built in and managed by
the permission scheme, held by default `create_custom_group`, `edit_custom_group`, `delete_custom_group`,
`restore_custom_group` and `manage_custom_group_members` (`POST /api/v4/roles/names`). What these reads answer to a bot
whose role lacks those permissions, or on a server without groups, was not checked. — _2026-11-15 · three runs of the
same calls, the same answers · Mattermost 11.2.2, Entry licence_ · Used in C-25.FR-8, C-28.FR-2.

**F-123. `@<group>` from a bot mentions a custom group's members, in an attachment and in a Thread reply.** A bot post
with an empty `message` and one attachment whose text starts with `@payments_team` raised the member's `mention_count`
from 0 to 1, put the badge 1 on the channel, and the web client highlighted the mention inside the attachment as a
mention of the member. A Thread reply under a bot post without a mention, its `message` starting with `@payments_team`,
raised the member's `mention_count` from 1 to 2 and showed under Threads (the badge 1, the Thread among the followed
ones), highlighted, though the channel's badge did not change, as a Thread reply goes to Threads when collapsed threads
are on. The bot post without a mention left the count at 1. `allow_reference` cannot be turned off for a custom group:
an admin's `PUT /api/v4/groups/{group_id}/patch` with `{"allow_reference": false}` was refused with `400` and a
localized message that `allow_reference` must be `true` for custom groups, and the group kept `true`; so it never stops
a custom group's mention, and for LDAP groups it was not checked. — _2026-11-15 · three bot posts in one channel, the
count read after each, and the patch with an admin's token · Mattermost 11.2.2, Entry licence, web client_ · Used in
C-25.FR-8, C-28.FR-2, C-13.FR-8.

**F-124. `@channel`, `@here` and `@all` from a bot mention in `message` and in an attachment alike (adds to F-113).**
With the test account online in the web client, the bot posted each of `@channel`, `@here` and `@all` once as its
`message` and once at the start of an attachment's text with an empty `message`: each of the six posts raised the
account's `mention_count` by one, from 0 to 6, the channel's badge showed 6, and the web client highlighted all six as
mentions of the account. — _2026-11-15 · six bot posts in one channel, the count read after each · Mattermost 11.2.2,
Entry licence, web client_ · Used in C-28.FR-2, C-13.FR-8.

## Alertmanager

Unless an entry says otherwise, the facts were checked on 2026-10-03 in a live test against a test Alertmanager v0.34.1
HA pair (two instances with persistent volumes, peer timeout 15 s) and a single instance without a cluster or a
persistent volume, all with the same `--web.external-url`. Alerts came from Prometheus v3.15.0 and vmalert v1.153.0,
both evaluating every minute; Prometheus kept its default resend delay of 1 minute. Routes grouped by `alertname` and
one more label, with `group_wait` 30 s and `group_interval` 1 min; entries name the `repeat_interval` where it matters.
A small HTTP receiver logged every webhook body with its arrival time. Position 0 is the instance that sends first.

### Repeats and reloads

**F-033. A repeat comes on the first tick after `repeat_interval`.** With `group_interval` 1 min, all 52 gaps between
`repeat interval elapsed` Snapshots were 359.98–360.10 s, for `repeat_interval` 5 min and 5 min 30 s alike: in the
steady state never more than `repeat_interval + group_interval`. In a steady HA pair only position 0 sends. —
_2026-10-03 · live test, four groups on each setup · Alertmanager v0.34.1_ · Used in C-06.FR-8.

**F-034. In an HA pair, repeat gaps vary.** When the ticks of the two instances are out of phase, gaps fall anywhere in
(`repeat_interval`, `repeat_interval + group_interval`]: after one instance restarted they alternated 345 s and 315 s
instead of 360 s, and a race of 4 ms made position 1 skip a repeat that position 0 sent 15 s later (gaps of 195 s and
225 s instead of 240 s). — _2026-10-03 · live test · Alertmanager v0.34.1_ · Used in C-06.FR-8.

**F-035. A reload can stretch a gap.** A configuration reload re-creates the Alertmanager groups, which notify at once,
outside their usual tick, and restart their ticks: one gap was 364 s at `repeat_interval` 5 min 30 s. A gap can reach
`repeat_interval + 2 × group_interval`. — _2026-10-03 · live test · Alertmanager v0.34.1_ · Used in C-06.FR-8.

**F-036. New route matchers mean a new `groupKey`.** Each of three reloads that changed a route's matchers sent at once
a `first notification` with all 20 alerts under the new `groupKey`, never a subset; the old `groupKey` never sent
anything again, not even a resolve. — _2026-10-03 · live test · Alertmanager v0.34.1_ · Used in C-06.FR-7.

### HA pairs

**F-037. The copies of an HA pair are identical.** In seven pairs of duplicates, `groupKey`, `notification_reason`,
`status`, `alerts` (in the same order) and `externalURL` were the same byte for byte; `externalURL` matched because both
instances had the same `--web.external-url`. — _2026-10-03 · live test · Alertmanager v0.34.1_ · Used in C-06.FR-2.

**F-038. A slow answer brings a copy one peer timeout later.** When the receiver answered after 20 s or 16 s, position 0
sent its copy 15.0 s (14.998–15.006 s) after position 1, for first notifications, new alerts and resolves alike; answers
after 10 s and 14 s brought no copy. — _2026-10-03 · live test · Alertmanager v0.34.1_ · Used in NFR-2,
`integration.duplicate_window`.

**F-039. A failure longer than the peer timeout is delivered twice.** While the receiver answered `500` for 45 s,
position 1 retried with exponential backoff and jitter (pauses of 0.3 to 11 s), position 0 joined 15.0 s later, and
after recovery both delivered, 0.9 s apart. A failure of 2.8 s was delivered by position 1 alone. — _2026-10-03 · live
test · Alertmanager v0.34.1_ · Used in `integration.duplicate_window`.

**F-040. A split cluster sends everything twice.** With the cluster port blocked, both instances counted a cluster of
one within 46 s and both sent every notification and repeat, 0–1 ms apart; once the port reopened they rejoined within
6 s and only one sent again. — _2026-10-03 · live test · Alertmanager v0.34.1_ · Used in `integration.duplicate_window`.

### Restarts and resolves

**F-041. A restarted HA instance can send a partial Snapshot.** An instance with a persistent volume restarted and
rejoined as position 1. A batch of 5 re-sent alerts re-created its group, which was flushed after the first of them:
15 s later it sent `repeat interval elapsed` with 1 of 10 alerts, and the other instance sent the full Snapshot 15 s
after that. — _2026-10-03 · live test · Alertmanager v0.34.1_ · Used in C-06.FR-5.

**F-042. A single instance without a persistent volume starts with partial Snapshots.** After a start, one group listed
3 of 10 alerts, then 9 of 10 a minute later — one firing alert missing from both — and all 10 after two minutes; an
Alertmanager silence created before the restart was lost. — _2026-10-03 · live test · Alertmanager v0.34.1_ · Used in
C-05.FR-11, C-06.FR-5.

**F-043. Prometheus re-sends firing alerts every 2 minutes.** With a 1-minute evaluation interval and resend delay, its
strict resend condition waits for the second evaluation; vmalert sends on every one. With `--dispatch.start-delay=90s`
the first Snapshot after a start listed 5 of 10 alerts; it was complete only when both batches came within the delay. —
_2026-10-03 · live test · Prometheus v3.15.0, vmalert v1.153.0, Alertmanager v0.34.1_ · Used in C-05.FR-11, C-06.FR-5.

**F-044. A full HA restart can leave resolves unsent.** Both instances restarted while alerts resolved:
`all alerts resolved` listed 6 of 20 alerts of one group and 1 of 10 of another, and the other resolves were never sent,
because the group's last notification no longer held a firing alert. — _2026-10-03 · live test · Alertmanager v0.34.1_ ·
Used in C-06.FR-21.

**F-045. A wholly muted group sends nothing.** An Alertmanager silence of the whole group (5 to 21 min), an inhibition
of it and a mute time interval each stopped every notification, repeats included; after a long mute the group returned
as `first notification`. A silence on one alert of three removed it only from the next Snapshot sent, usually the
repeat. — _2026-10-03 · live test · Alertmanager v0.34.1_ · Used in C-06.FR-8.

**F-046. Resolves come back for about 15 minutes.** Prometheus re-sends a resolved alert every 2 minutes for 14 minutes,
and each send puts it back into its group, so the same resolve arrives several times, inside later firing Snapshots too.
— _2026-10-03 · live test · Prometheus v3.15.0, Alertmanager v0.34.1_ · Used in C-06.FR-4.

**F-047. A resolve during a mute arrives if the mute ends soon.** An alert that resolved while its group was silenced,
inhibited or muted was sent as resolved once the mute ended 4 to 14.5 minutes later; after a 21-minute silence the
resolve never came. — _2026-10-03 · live test with Prometheus · Alertmanager v0.34.1_ · Used in C-06.FR-8.

### Payload

**F-048. `max_alerts` keeps the first alerts.** With `max_alerts: 2`, every Snapshot listed the first two alerts in
Alertmanager's sort order; `truncatedAlerts` counted resolved alerts too, and a resolved alert in the cut tail never
appeared. `status` is the whole group's: a Snapshot listing a resolved and a firing alert was `firing`, and the last one
`resolved`, `all alerts resolved`, with `truncatedAlerts: 2`. — _2026-10-03 · live test · Alertmanager v0.34.1_ · Used
in C-06.FR-6, C-06.FR-21.

**F-049. Sibling routes with the same matchers share a `groupKey`.** Two routes with identical matchers and
`continue: true` gave one `groupKey` and one notification record: both sent the first notification and
`all alerts resolved`, 3–23 ms apart, but only the route with the shorter `repeat_interval` repeated. — _2026-10-03 ·
live test · Alertmanager v0.34.1_ · Used in C-06.FR-7.

**F-050. `routeLabels` and `notification_reason`.** Every Snapshot carried `routeLabels` — `{}` for a route without
`labels` — which are not part of `groupKey`. All five values of `notification_reason` occurred; an alert returning after
its whole group resolved comes as `first notification`. — _2026-10-03 · live test · Alertmanager v0.34.1_ · Used in
C-06.FR-8; `routeLabels` is not used in L1.

### Rule evaluator restarts

**F-051. `endsAt` is the last send plus 4 minutes**, for Prometheus and vmalert alike. — _2026-10-04 · live test ·
Prometheus v3.15.0, vmalert v1.153.0_ · Used in C-06.FR-11.

**F-052. A Prometheus restart can produce a Continuation.** After a 3-minute stop whose first evaluation fell exactly on
`endsAt`, Alertmanager replaced the alerts with ones carrying a new `startsAt`, with no resolve between; it showed only
in the next repeat. A shorter stop kept `startsAt`; a first evaluation after `endsAt` gave a resolve, then
`first notification`. — _2026-10-04 · live test · Prometheus v3.15.0, Alertmanager v0.34.1_ · Used in C-06.FR-11.

**F-053. vmalert gives no Continuation.** vmalert aligns `startsAt` down to the minute, so after a 3-minute stop the new
`startsAt` came before the old `endsAt` and Alertmanager merged the alerts, keeping the old `startsAt`; a 10-minute stop
gave a resolve and `first notification`. — _2026-10-04 · live test · vmalert v1.153.0, Alertmanager v0.34.1_ · Used in
C-06.FR-11.

### Routing

**F-059. A first child route without matchers silences the top-level receiver.** Alertmanager sends an alert to the
receiver of the top-level route only when no child route matches it; `continue: true` on a matching child route only
lets the next siblings be tried. A first child route without matchers matches every alert, so an alert that no later
child route matches goes to that route's receiver alone. With the Muster route of the snippet first, `amtool config
routes test` returned only `muster` for a plain alert and the stand's default webhook got nothing; a last child route
`- receiver: <default receiver>` without matchers made it return both, and the default webhook got the alerts again.
This matches the [routing documentation](https://prometheus.io/docs/alerting/latest/configuration/#route) of
Alertmanager: an alert enters the tree at the top-level route, which matches every alert, and traverses the child
nodes; when no child matches, the alert is handled by the current node's configuration. — _2026-10-06/07 · live check
on a test stand with `amtool config routes test` and a webhook default receiver; `amtool` v0.34.1 against the rendered
snippet with and without the catch-all; Alertmanager routing documentation_ · Used in C-05.FR-5.

## Grafana

Unless an entry says otherwise, the facts were checked on 2026-10-19 in a live test on the development stand against
Grafana OSS 13.2.4 — a single instance on SQLite and an HA pair of two replicas on a shared PostgreSQL 17 with the
default peer timeout of 15 s — each sending through webhook contact points. Grafana-managed rules over Prometheus
v3.15.0 were evaluated every 10 s with `for: 0s`; the notification policy grouped by `alertname` and `team`, with
`group_wait` 10 s, `group_interval` 30 s and `repeat_interval` 2 min, and a child policy sent one group also to a
contact point with `maxAlerts: 5`. A small HTTP receiver logged every body with its arrival time.

### Payload and grouping

**F-105. The webhook contact point sends Alertmanager's webhook format, with `version` `"1"` and additions.** A body
has `receiver`, `status`, `alerts`, `groupLabels`, `commonLabels`, `commonAnnotations`, `externalURL` (Grafana's
`root_url`), `groupKey` and `truncatedAlerts` like Alertmanager's, `version` `"1"` instead of `"4"`, no
`notification_reason` and no `routeLabels`, and adds `orgId` (the organization's id), `title`, `message`, `state`
(`alerting` or `ok`) and `appVersion`. Each alert has `status`, `labels`, `annotations`, `startsAt`, `endsAt`,
`generatorURL` and `fingerprint`, and adds `silenceURL`, `dashboardURL`, `panelURL`, `ruleUID`, `values` (an object,
`null` in a resolve), `valueString` and `orgId`. The labels carry `alertname`, the rule's title, and `grafana_folder`,
its folder, without Grafana's hidden labels such as `__alert_rule_uid__`. `startsAt` is an evaluation tick
(`07:27:00Z` at a 10-s interval); `endsAt` is the zero time while the alert fires and the time of its resolve
afterwards — a tick, or the moment the rule was paused, to the nanosecond (`07:42:04.013602845Z`). `generatorURL` is
the rule's page in Grafana, `<root_url>/alerting/grafana/<rule uid>/view?orgId=1`. The request carries
`User-Agent: Grafana` and an `Authorization` header built from `authorization_scheme` and
`authorization_credentials`. The bodies of Alertmanager v0.34.1 in the test of the section above have none of the
additions. — _2026-10-19 · 182 bodies logged on the development stand · Grafana OSS 13.2.4_ · Used in C-06.FR-8,
C-06.FR-21, C-05.FR-11.

**F-106. `groupKey` has Alertmanager's format and names the notification policy.** Its path is the chain of
Grafana's notification policies, followed by the group labels: `{}/{}:{alertname="SpikeCPU", team="web"}` from a
catch-all child policy, `{}/{suite="bulk"}:{alertname="SpikeBulk", team="ops"}` from a child policy matching
`suite=bulk`, `{}/{suite="mix"}:{suite="mix"}` from one with `group_by [suite]`. It was the same byte for byte from
both replicas of the HA pair, and before and after restarts of the single instance and of a replica. With simplified
routing — the contact point chosen in the rule (`notification_settings`) — the path is
`{}/{__grafana_autogenerated__="true"}/{__grafana_receiver__="main"}/{__grafana_route_settings_hash__="645bb2f943c9e52a"}`,
and Grafana added `grafana_folder` to the rule's `group_by [alertname]`. The hash covers the rule's notification
settings, so a change of them names another route, as new matchers do in Alertmanager (F-036); and `orgId` is not part
of the key, so two organizations with the same policies give the same keys. Both follow from the format and were not
tried on the stand, which had one organization. — _2026-10-19 · live test on the development stand · Grafana OSS
13.2.4_ · Used in C-06.FR-7, C-05.FR-11.

**F-107. The `fingerprint` includes the rule's hidden uid.** It is Alertmanager's FNV-1a hash of the labels together
with the hidden label `__alert_rule_uid__`, so it cannot be computed from the labels of the body: for `SpikeMem` on host
`m`, the visible labels hash to `d8970f7629fa46ba`, and with `__alert_rule_uid__="spike-mem"` to `0b7e255d753c35de`,
the `fingerprint` of the body. — _2026-10-19 · the hash recomputed from the labels of the stand's bodies · Grafana
OSS 13.2.4_ · Used in C-06.FR-2.

### Repeats, resolves and truncation

**F-108. Repeats come as in Alertmanager, without `notification_reason`.** A repeat goes out on the first
`group_interval` tick after `repeat_interval`, as in F-033: the gaps between identical Snapshots of the single instance
were 120.0 to 150.0 s, and 131.0 s once after a reload of provisioning, as in F-035; in the HA pair, whose replicas
ticked 18.6 s out of phase, they were 121.4 to 148.6 s, as in F-034. No Snapshot carries `notification_reason`, so a
repeat shows only as a Snapshot identical to the one before. — _2026-10-19 · live test on the development stand ·
Grafana OSS 13.2.4_ · Used in C-06.FR-8.

**F-109. Every end of an alert is an explicit resolve.** A value below the threshold resolved at the next evaluation;
a series that disappeared resolved after two evaluations without it, with the annotation `grafana_state_reason`
`MissingSeries`; a paused rule resolved its alerts at once, `Paused`, with `endsAt` the moment of the pause; a deleted
rule resolved them with `RuleDeleted`. A rule with `noDataState: NoData` whose query returned nothing resolved its
alert as `MissingSeries` and fired a separate alert, `alertname` `DatasourceNoData` with the labels `rulename`,
`datasource_uid` and `ref_id` besides the rule's labels, in a group of its own; when the data returned, the rule's alert
fired with a new `startsAt` and `DatasourceNoData` resolved. A resolved alert stays in its group for 15 minutes
(`resolved_alert_retention`) and comes again in later Snapshots, as in F-046. Once every alert of a group resolved,
the Snapshot had `status: resolved` and `state: ok`, and the group was silent until an alert fired again. A graceful
restart sent no resolve. — _2026-10-19 · live test on the development stand · Grafana OSS 13.2.4_ · Used in
C-06.FR-4, S-096.

**F-110. Max alerts keeps the first alerts, and `status` follows the alerts listed.** A contact point's `maxAlerts`
(Max alerts; 0, no limit, by default) kept the first alerts in sort order and counted the others, resolved ones
included, in `truncatedAlerts`, as Alertmanager's `max_alerts` does (F-048). Unlike Alertmanager, Grafana computes
`status` and `state` from the alerts it lists: with 10 of a group's 30 alerts resolved and 20 firing, the contact point
with `maxAlerts: 5` sent `status: resolved` and `state: ok` with 5 resolved alerts and `truncatedAlerts: 25`, and again
with every repeat while the 20 fired, while the untruncated contact point sent `status: firing` in the same
millisecond. — _2026-10-19 · live test on the development stand · Grafana OSS 13.2.4_ · Used in C-06.FR-21,
C-06.FR-6.

### HA pairs and restarts

**F-111. The replicas of an HA pair evaluate the rules each on its own.** Alerts that fired before a replica's first
evaluation had different `startsAt` in the two replicas' copies, `07:27:00` and `07:27:10`, while alerts that began
with both replicas running had the same; every other field and the order of the alerts were the same. A replica
restarted later loaded the state from the shared database and sent identical copies. Normally one replica sends; with
the receiver answering after 20 s, the other replica's copy came 18.6, 6.4, 3.0 and 2.0 s later, and the receiver got
the notifications of both replicas in turn. — _2026-10-19 · live test on the development stand · Grafana OSS 13.2.4_ ·
Used in C-06.FR-4, `integration.duplicate_window`.

**F-112. After a restart, a rule's alerts return only with its first evaluation.** A graceful restart of the single
instance sent no notification and kept the repeats, 140, 142 and 150 s apart around it, and the alerts' `startsAt`,
which Grafana loads from its database; but until a rule is evaluated again, its alerts are missing from Grafana's
Alertmanager. A group of two rules evaluated every 10 s and every 8 min, restarted just after the slow rule's
evaluation, sent Snapshots without the slow rule's alert for about 7 minutes — four, from 08:15:35 to 08:21:18 — and
listed it again, with its old `startsAt`, at 08:22:48. Grafana has no setting like Alertmanager's
`--dispatch.start-delay`. On a first start, provisioned policies take effect at the first poll of the Alertmanager
configuration, `alertmanager_config_poll_interval` (60 s). — _2026-10-19 · live test on the development stand ·
Grafana OSS 13.2.4_ · Used in C-05.FR-11.

## Slack

Unless an entry says otherwise, the facts were checked on 2026-10-19 on a test stand in Slack's cloud: a free
workspace, an app installed in it with the bot scopes `chat:write`, `channels:read`, `groups:read`, `im:write`,
`users:read` and `users:read.email`, an app-level token with `connections:write`, interactivity and Socket Mode on, two
channels the bot is a member of and two it is not, and one person, the workspace's owner. The Web API was called with
`curl` at `https://slack.com/api/` with a JSON body and `Authorization: Bearer`; Socket Mode with a small Go client on
`github.com/coder/websocket` v1.8.15, directly and through a local HTTP proxy; messages were looked at in the web
client in a browser, at a window about 1,250 px wide, without pressing anything. No person pressed a button during the
test: what Slack sends for a press is taken from its documentation and a local simulation, and the entries that rely on
it say so. Every message of the test was deleted afterwards.

F-149 to F-154 were checked on 2026-11-22 on a second free test workspace: an app with the bot scopes `chat:write`,
`channels:read` and `im:write`, interactivity and Socket Mode on, and its Messages tab in App Home on and read-only
except where an entry says otherwise; a public channel with the bot and the test person `alice`, who had the Slack app
on an iPhone, its version not recorded, and quit her desktop app for the checks of a push. Presses were read in Socket
Mode by a small Python client that logged their payloads.

### Tokens, errors and scopes

**F-070. Errors are `200` with `ok: false`.** A wrong token is answered `{"ok":false,"error":"invalid_auth"}`, a request
without a token `not_authed`, a token of the wrong kind `not_allowed_token_type` (an app-level token on
`chat.postMessage`, a bot token on `apps.connections.open`), all with HTTP `200`. A missing scope is `missing_scope`
with the fields `needed` and `provided`: `conversations.history` needs
`channels:history,groups:history,mpim:history,im:history`, `conversations.join` `channels:join`, `conversations.list`
with `types=im` `im:read`. An answer is classified by its `error` field, never by its status; the only error status is
the `429` of F-099. — _2026-10-19 · calls with wrong, missing and other tokens on the stand_ · Used in C-24.FR-2,
C-24.FR-8.

**F-071. Every answer names the token's scopes.** The header `x-oauth-scopes`, on every call `auth.test` included,
lists the scopes of the token — here `chat:write,im:write,channels:read,groups:read,users:read,users:read.email` — and
`x-accepted-oauth-scopes` those the method accepts: `users:read` for `users.list`, `users:read.email` for
`users.lookupByEmail`, `channels:read,groups:read,mpim:read,im:read,read` for `conversations.info`, `chat:write` for
`chat.update`, `channels:write,groups:write,mpim:write,im:write,post` for `conversations.open`. One `auth.test` tells
which required scopes a token lacks. — _2026-10-19 · headers of each call on the stand_ · Used in C-24.FR-2.

**F-072. `auth.test` with a bot token and with an app-level token.** With the bot token it answers `ok`, `team`,
`team_id`, `user_id` (the bot user), `bot_id` and the workspace's `url`. With the app-level token it answers `ok` and
`app_id`, without `team_id` or `bot_id`. The bot's `app_id` is in `bots.info` (`bot.app_id`, scope `users:read`) and in
`users.info` (`profile.api_app_id`), so a check can tell whether both tokens belong to one app. A wrong app-level token
on `apps.connections.open` answers `invalid_auth`. — _2026-10-19 · both tokens of the stand's app_ · Used in
C-24.FR-2.

**F-073. Without `chat:write.customize` a custom name is ignored.** `chat.postMessage` with `username` answered `ok`,
and the message came from the bot under its own name, without `message.username`. The bot's name is the display name
set in the app's settings. — _2026-10-19 · a message with `username` on the stand_ · Used in C-24.FR-16.

### Sending, Block Kit and limits

**F-074. Block Kit and the styles of buttons.** A message of `header`, `section` (mrkdwn), `context` and `actions`
blocks was accepted. A button takes `style` `primary` or `danger` only; `success`, `default` or an empty string refuse
the whole message with `invalid_blocks` and `errors: ["must be a valid enum value
[json-pointer:/blocks/0/elements/0/style]"]`. A button without `style` is plain. The web client draws `primary` as a
filled green button, `danger` as a filled red one and a plain button white with a border (the documentation describes
`primary` as a green outline). A button with `url` and no `value` is a link button. The documentation: "`primary`
should only be used for one button within a set", and `danger` is for destructive actions. — _2026-10-19 · messages
and a screenshot of the web client;
[button element](https://docs.slack.dev/reference/block-kit/block-elements/button-element)_ · Used in C-24.FR-5.

**F-075. A colour bar needs an attachment, and blocks and buttons work inside it.** `attachments: [{color, fallback,
blocks: [section, context, actions]}]` is drawn with a coloured bar on the left, the buttons inside the attachment shown
and styled. `color` takes a hex value; `good` is stored as `2eb886`. The legacy fields of an attachment (`title`,
`title_link`, `text`, `footer`, `footer_icon`, `ts`, `callback_id` and legacy `actions`) were accepted too. The
documentation calls attachments "legacy secondary message attachments" whose legacy fields "may be subject to
reductions in visibility or functionality", and recommends `blocks` inside them. — _2026-10-19 · messages and a
screenshot of the web client;
[legacy attachments](https://docs.slack.dev/legacy/legacy-messaging/legacy-secondary-message-attachments)_ · Used in
C-24.FR-4.

**F-076. Block limits count the code points of the string as sent.** Exact bounds, where N passes and N+1 is refused
with `invalid_blocks` and a `json-pointer`: a `section` text 3,000 ("must be less than 3001 characters"), a `header`
(plain text) 150, a button's text 75, a button's `value` 2,000, an `action_id` 255, 50 blocks in a message ("no more
than 50 items allowed"), 25 elements in an `actions` block and 10 in a `context` block, and in a `static_select` an
option's text 75 and its `value` 150. Code points are counted: 3,000 "я" and 3,000 "🔥" pass, 3,001 do not, and a family
emoji of five code points counts five. The raw string is counted: `&amp;` is five characters and `<https://e.org|x>`
counts all its markup. An `action_id` is unique within its block (a second one in the block is refused with
"`action_id` "same" already exists") and may repeat in other blocks; a `block_id` is unique in the message; without an
`action_id` Slack makes up a random one. — _2026-10-19 · bounds probed one by one on the stand_ · Used in C-24.FR-4,
C-24.FR-5.

**F-149. A `section` takes at most 10 `fields`.** `chat.postMessage` with one `section` block of 10 `fields` was
accepted; with 11 or 12 it was refused with `invalid_blocks` and `errors`
`["[ERROR] no more than 10 items allowed [json-pointer:/blocks/0/fields]"]`. `chat.update` of a one-field message to 10
`fields` was accepted, and to 11 or 12 refused the same way. The text of one field takes 2,000 characters; 2,001 are
refused with `invalid_blocks` and "must be less than 2001 characters [json-pointer:/blocks/0/fields/N/text]" for each
such field. The desktop client drew the 10 fields in two columns, the iPhone app in one. — _2026-11-22 · posts and edits
in the channel · the free workspace_ · Used in C-38.FR-2, C-38.FR-6, P-94, S-210, S-211.

**F-077. A long `text` is split into several messages, and only the last one's `ts` is returned.** `chat.postMessage`
with a `text` of 12,000 characters and no blocks made three separate messages of 4,000; 40,000 "я" made ten. The answer
held the `ts` and `message` of the last piece only; the others had earlier `ts`, about 20 ms apart, and
`chat.delete` of the returned `ts` deleted only that piece. 4,000 and 4,001 characters made one message. Beyond
40,000 the text is cut to 40,000 with `warning: "message_truncated"`. Next to blocks, `text` is only the notification's
fallback: blocks with a `text` of 8,000 made one message. — _2026-10-19 · messages on the stand, the pieces seen in the
web client_ · Used in C-24.FR-4.

**F-078. `chat.update` refuses a `text` longer than 4,000 bytes, even with blocks.** A `text` of 4,000 ASCII characters
passes and 4,001 are refused with `msg_too_long`; 2,000 "я" (4,000 bytes of UTF-8) pass and 2,001 do not; 3,999 ASCII
characters and one "я" (4,001 bytes) do not. The stored form is counted: `&` is stored as `&amp;` (1,000 "&" were
refused), `<` as `&lt;` (900 passed, 1,001 did not), an emoji as its shortcode (1,000 "🔥", stored as `:fire:`, were
refused). A fallback `text` of 8,000 next to blocks is refused the same way. Blocks are not limited like this: 20
sections of 3,000 characters passed both in a send and in an edit. — _2026-10-19 · edits on the stand_ · Used in
C-24.FR-4.

**F-079. Emoji in `text` are stored as shortcodes.** A `text` sent as "🔥 …" came back in `message.text` as
":fire: …", "✅" as ":white_check_mark:". — _2026-10-19 · answers of `chat.postMessage` on the stand_ · Used in
C-24.FR-4.

**F-080. An attachment's text is cut to 8,000 characters silently.** `attachments[0].text` of 9,000 characters was
stored as 8,000 and "…", without a warning, in a send and in an edit; the client shows its start and "Show more". —
_2026-10-19 · the stand_ · Used in C-24.FR-4.

**F-081. The `markdown` block.** A block `{"type":"markdown","text":…}` with standard Markdown, tables included, is
turned by the server into `rich_text` (and `table`) blocks; 12,000 characters passed as one message and 12,001 were
refused with `msg_too_long`. It is accepted inside an attachment too. — _2026-10-19 · the stand_ · Used in C-24.FR-4.

**F-082. The web client folds buttons beyond five in a row.** An `actions` block of seven buttons was shown as the
first five and "+ 2 more", one of 25 as five and "+ 20 more". A button with a label of 75 characters was cut to about
38 with "…" (the documentation: it "may truncate with ~30 characters"). A `static_select` and an `overflow` menu next
to buttons were drawn in the row. — _2026-10-19 · screenshots of the web client, a window about 1,250 px wide; the
mobile clients were not looked at_ · Used in C-24.FR-5.

**F-083. `unfurl_links: false` and `unfurl_media: false` are accepted,** and no message of the test unfurled a link. —
_2026-10-19 · the stand_ · Used in C-24.FR-4.

### Edits and deletions

**F-084. A bot edits its own messages.** `chat.update` with `text` and `blocks` answered `ok` with
`message.edited = {user: <the bot>, ts}`. The same edit again answered `ok` too: there is no "not modified" answer. The
oldest message of the test, about 35 minutes old, was edited. The documented error `edit_window_closed` ("The message
cannot be edited due to the team message edit settings") was not seen; whether the workspace's edit window applies to
bots was not checked, for lack of an older message. — _2026-10-19 · the stand;
[chat.update](https://docs.slack.dev/reference/methods/chat.update)_ · Used in C-24.FR-6, C-24.FR-8.

**F-085. What `chat.update` keeps.** With `text` and no `blocks`, the earlier blocks are replaced by `rich_text` made
from the text; without `attachments` the earlier attachments are kept, with `attachments: []` they are removed; with
`blocks: []` the blocks are removed. The documentation: "If you don't include `blocks`, the message's previous `blocks`
will only be retained if the `text` argument is not provided… If you don't include `attachments`, the message's previous
`attachments` will be retained". — _2026-10-19 · edits on the stand; chat.update_ · Used in C-24.FR-4.

**F-086. `block_mismatch`.** A message sent only of `rich_text` blocks cannot be edited into blocks without
`rich_text`: the edit is refused with `block_mismatch` ("Rich-text blocks cannot be replaced with non-rich-text
blocks"). A mixed message (`section` and `rich_text`) can be edited into `[section]`, a `section` into `rich_text`, and
a plain `text` message, which the server stores as `rich_text`, into a `section`. — _2026-10-19 · edits on the stand_
· Used in C-24.FR-4, C-24.FR-8.

**F-087. A deleted or foreign message.** `chat.update` and `chat.delete` of a deleted or unknown `ts`, or of a `ts` of
another channel, answer `message_not_found`; of an unknown channel `channel_not_found`; of someone else's message — a
system message, Slackbot's or a person's — `cant_update_message` and `cant_delete_message`. A second `chat.delete`
answers `message_not_found`. — _2026-10-19 · the stand_ · Used in C-24.FR-8.

**F-088. `chat.getPermalink` needs no scope and is no test of a deletion.** Its `x-accepted-oauth-scopes` is empty; it
answers `ok` and `permalink` for an existing `ts` (for a Thread reply, with `?thread_ts=…&cid=…`), and
`message_not_found` for a deleted or unknown one and for a channel the bot is not in; an unknown channel is
`channel_not_found`. A deleted root that has replies, the tombstone of F-090, answers `ok`. — _2026-10-19 · the stand_
· Used in C-24.FR-8.

### Threads

**F-089. A reply in a Thread.** `thread_ts` set to the root's `ts` gives `message.thread_ts` of the root and
`parent_user_id` of its author; a `thread_ts` that names a reply is normalized to its root. `reply_broadcast: true`
gives `subtype: thread_broadcast`, an answer with `root` (`reply_count`, `latest_reply`, `reply_users`), and "replied to
a thread: …" in the channel. Buttons on a Thread reply are sent as on any message. — _2026-10-19 · the stand_ · Used
in C-24.FR-6.

**F-090. A deleted root with replies stays as a tombstone, and its Thread goes on.** After a root with a reply was
deleted, the channel showed "This message was deleted." and "2 replies"; the next `chat.postMessage` with its
`thread_ts` answered `ok`, with the same `thread_ts` and `parent_user_id: "USLACKBOT"`; `chat.update` of the root
answered `message_not_found`, and `chat.getPermalink` of it `ok`. — _2026-10-19 · the stand, a screenshot_ · Used in
C-24.FR-8.

**F-091. A reply to a deleted root without replies goes to the channel, silently.** `chat.postMessage` with the
`thread_ts` of a deleted message that had no replies — or of an unknown one — answered `ok`, without
`message.thread_ts`, and the message appeared in the channel as an ordinary one. Mattermost (F-058) and Telegram (F-008)
refuse such a reply; Slack does not. — _2026-10-19 · the stand, a screenshot_ · Used in C-24.FR-8.

**F-150. A Mention in a bot's Thread reply pushes to the phone, whether the person follows the Thread or not.** With
`alice`'s notification settings at their defaults and her locked phone's app in the background, a root post with `<@U…>`
of her pushed about 5 s after the send. Under a root post that did not mention her, a Thread reply with `<@U…>` pushed
about 5 s after the send while she did not follow the Thread, and again after she had replied in it; and a Thread reply
without a Mention, in the Thread she followed, pushed too. Each Mention was in the text of a `section` block, while the
message's `text`, its fallback, was "Muster spike test" without one, as Muster sends its messages. Earlier runs, made
while the phone's Slack notifications were paused, are not counted. Not checked: the channel set to notify about
Mentions only, a reply with `reply_broadcast`, and the app killed. — _2026-11-22 · two root posts and three Thread replies, watched on the phone ·
the free workspace, the iPhone app_ · Used in C-32.FR-11, C-32.FR-20.

### Answers and presses

**F-092. `chat.postEphemeral`.** It needs `chat:write` (rate limit tier 4) and answers `{"ok":true,"message_ts":"…"}`,
in a channel, with `thread_ts` and with blocks alike. In a channel the client shows it at once in the channel view as
"Only visible to you". When the bot is not in the channel it answers `not_in_channel`, and when the person is unknown
or not in the channel `user_not_in_channel`. An ephemeral message cannot be deleted. The documentation: delivery is not
guaranteed — the person must be active and in the channel — and in a Thread it is "only shown if there is already an
active thread". No permission is needed beyond the scope, unlike Mattermost's `create_post_ephemeral` (F-063). —
_2026-10-19 · the stand, a screenshot;
[chat.postEphemeral](https://docs.slack.dev/reference/methods/chat.postEphemeral)_ · Used in C-24.FR-11.

**F-093. What a press sends, and how fast it must be confirmed (from the documentation).** In HTTP mode Slack posts to
the app's Interactivity Request URL an `application/x-www-form-urlencoded` body whose field `payload` is JSON with
`type: block_actions`, `user.id`, `team.id`, `api_app_id`, `container` (`type` `message` or `message_attachment`,
`message_ts`, `channel_id`, `is_ephemeral`), `channel`, `message`, `trigger_id`, `response_url`
(`https://hooks.slack.com/actions/…`) and `actions[]` (`action_id`, `block_id`, `value`, `type`, `action_ts`; a select
has `selected_option.value`). The answer must be HTTP `200` **within 3 seconds**, or the person sees an error.
`response_url` takes up to five answers within 30 minutes, ephemeral by default (`response_type: ephemeral`);
`replace_original: true` edits the original message and `delete_original` deletes it, and it "will bypass any channel
posting permissions". In Socket Mode the same payload comes in an envelope `{"envelope_id", "type": "interactive",
"payload", "accepts_response_payload"}`, confirmed with `{"envelope_id": …}` on the same socket, also within 3 seconds.
No press was seen live. — _documentation:
[handling user interaction](https://docs.slack.dev/interactivity/handling-user-interaction),
[block_actions payload](https://docs.slack.dev/reference/interaction-payloads/block_actions-payload),
[Socket Mode](https://docs.slack.dev/apis/events-api/using-socket-mode); a local simulation of the handler_ · Used in
C-24.FR-9, C-24.FR-10, C-24.FR-11.

**F-094. A bot cannot press its own button.** The Web API has no method that presses a button — nothing like
Mattermost's `POST /api/v4/posts/{id}/actions/{action_id}` (F-054) — and no interaction reaches an app without a person.
A test message cannot prove by itself that presses reach Muster. — _2026-10-19 · the method reference, and no
`interactive` envelope on the socket during the whole test_ · Used in C-24.FR-2, C-24.FR-13.

**F-151. A person's press in the app's direct chat, as received.** A press of `alice` on a button of the bot's message
in the app's direct chat arrived over Socket Mode as a `block_actions` payload with `actions`, `api_app_id`, `channel`
(`id` the `D…` of the direct chat, `name` `directmessage`), `container` (`type` `message`, `message_ts` the message's
`ts`, `channel_id` and `is_ephemeral` `false`), `enterprise` `null`, `is_enterprise_install` `false`, `message`,
`response_url` (`https://hooks.slack.com/actions/…`), `state` (with empty `values`), `team` (`id`, `domain`), `token`,
`trigger_id`, `type` and `user` (`id`, `username`, `name`, `team_id`); each of the `actions` had `action_id`,
`block_id`, `type`, `action_ts`, the button's `text`, and `value` when the button had one. A `POST` to `response_url`
answered `200`, and its text appeared in the direct chat for her. Both presses were made with the Messages tab
read-only, where the input says "Sending messages to this app has been turned off" (F-096). This is the payload that
F-093 took from the documentation, seen live in Socket Mode; HTTP mode was not tried. — _2026-11-22 · two presses, their payloads logged by
the client · the free workspace_ · Used in C-24.FR-9, C-24.FR-10, C-43.FR-14, C-43.FR-16.

### Users and direct messages

**F-095. Finding a person.** `users.lookupByEmail` (scope `users:read.email`) ignores the case of the address (an
address with capitals found the same `U…`); an unknown address answers `users_not_found`. `users.info` answers
`user_not_found` for an unknown id, and has `tz` (an IANA zone name), `deleted`, `is_bot` and `profile.email`, and
`locale` (`en-US`) with `include_locale=true`. `users.list` (tier 2) returns `USLACKBOT` and bots (`is_bot: true`) too,
with `profile.email` when the token has `users:read.email`. There is no search by name: only `users.list` page by page,
comparing `name` and `profile.display_name`. — _2026-10-19 · the stand_ · Used in C-24.FR-12.

**F-096. The bot's direct messages.** `conversations.open {users: U…}` (scope `im:write`) answers
`{"ok":true,"no_op":true,"already_open":true,"channel":{"id":"D…"}}`; for the bot itself `cannot_dm_bot`, for an
unknown user `user_not_found`. `chat.postMessage` to the `D…` channel is delivered, and so is `chat.postMessage` with
`channel` set to the `U…` id, into the same direct chat, without `conversations.open`. The messages show in the app's
Messages tab; with the tab read-only, the input says "Sending messages to this app has been turned off", so the person
cannot answer. `chat.delete` works in the direct chat; `conversations.info` of it needs `im:read`. — _2026-10-19 · two
direct messages to the stand's owner, deleted afterwards, and a screenshot_ · Used in C-24.FR-3, C-24.FR-12.

**F-097. Mentions and escaping.** `<@U…>` in `text` and in a `section` becomes a Mention, shown highlighted (stored as
a `rich_text` element `{"type":"user","user_id":…}`). `&lt;!channel&gt;` and `&lt;b&gt;` are shown literally, as
"<!channel> <b>x</b>", and `&amp;` as "&". mrkdwn emphasis (`*`, `_`, `~`, `` ` ``) has no escape: `*not bold?*` was
bold. The `text` elements of a `rich_text` block are literal: "*not bold* _x_ ~y~ `z` <!channel> <@U…> <https://e.org|t>
&" was kept and shown as it was, without Mentions or links. A bare `@channel`, `<!channel>` and `<!here>` were not sent,
so as not to notify the workspace. — _2026-10-19 · the stand;
[formatting](https://docs.slack.dev/messaging/formatting-message-text)_ · Used in C-24.FR-6, C-24.FR-7.

**F-152. `messages_tab_disabled`, and a person who does not exist.** With the app's Messages tab turned off in App
Home, `chat.postMessage` to `alice`'s `U…` id and to the `D…` of the direct chat, which `conversations.open` had opened
before, both answered `messages_tab_disabled`, while `conversations.open` with her id still answered `ok` with the same
`D…`: only a send tells that the tab is off. `chat.postMessage` with `channel` set to a `U…` id that names no user
answered `channel_not_found`, and `conversations.open` with it, or with a malformed id, `user_not_found`. Not checked: a
deactivated user. — _2026-11-22 · the tab turned off in App Home and back to read-only afterwards · the free
workspace_ · Used in C-43.FR-12, C-43.FR-17.

**F-153. The push of a direct message, and of its edit.** A `chat.update` of the bot's message in the direct chat made
no push to `alice`'s locked phone. With her notifications paused in the app (Slack's Do Not Disturb), a new direct
message made no push; with them on and the app killed, one pushed about 5 s after the send and showed its text. —
_2026-11-22 · one edit and two messages, watched on the phone · the free workspace, the iPhone app_ · Used in
C-43.FR-8, C-43.FR-13.

### Channels

**F-098. Checking a channel.** `conversations.info` (scopes `channels:read` and `groups:read`) answers
`channel.{id, name, is_member, is_archived, is_private, is_channel}`; a channel without the bot answers `ok` with
`is_member: false`; an unknown id, of a `D…` direct chat or a `C…` channel, `channel_not_found`. Sending to a channel
without the bot answers `not_in_channel` (and so does `chat.postEphemeral`); to an unknown channel, or to a channel
named `#…`, `channel_not_found`; without any text `no_text`. `conversations.list` with `limit=2` returned a cursor in
`next_cursor`, and with `types=public_channel,private_channel&exclude_archived=false` all five channels. An archived
channel, a bot removed from a channel and a private channel were not tried: the stand has no `channels:manage`, and a
bot is removed only by a person. — _2026-10-19 · the stand_ · Used in C-24.FR-3.

### Rate limits

**F-099. `429` is a real HTTP status, with JSON and `Retry-After`.** `users.list` (tier 2), 100 requests 50 at a time:
90 × `200` and 10 × `429` with `Retry-After: 30` and the body `{"ok":false,"error":"ratelimited"}` as
`application/json`. `chat.update` of one message: 60 and 150 requests at once all passed; then 400 within 3 s, 100 at a
time, gave 120 × `200` and 280 × `429` with `Retry-After: 10`, and about 10 s later an edit and a send to another
channel passed. `chat.delete` without pauses, about 100 a minute, got 3 `ratelimited` out of about 115.
`chat.postMessage` to one channel, 40 in a row (about two a second for 22 s) and 30 at once, got no `429`. The
documentation: limits are per method, per app and per workspace (`Retry-After` asks to wait "before attempting to call
`conversations.info` with any token awarded to your app from this workspace"); `chat.postMessage` allows "one message
per second per channel" with short bursts and a workspace-wide limit, and "if you attempt bursts, there is no guarantee
that messages will be stored or displayed"; `chat.update`, `chat.delete` and `apps.connections.open` are tier 3 (50 or
more a minute), `chat.postEphemeral` tier 4, `users.list` and `conversations.list` tier 2. — _2026-10-19 · the stand;
[rate limits](https://docs.slack.dev/apis/web-api/rate-limits)_ · Used in C-24.FR-3, C-24.FR-8.

**F-154. One client in a row met no `429` in a direct chat.** `chat.postMessage` to one direct chat, one request after
another for 60 s, passed 76 times and, in a second run, 75 times, with no `429`; `chat.update` of one message there, the
same way, passed 76 times with no `429` — about 1.25 a second, above the "one message per second per channel" of
`chat.postMessage` (F-099), without a refusal. The workspace had one person, so `chat.postMessage` to many direct chats
at once was not measured. — _2026-11-22 · three bursts of 60 s · the free workspace_ · Used in C-43.FR-10,
C-43.FR-11, P-98.

### Socket Mode

**F-100. Opening a Socket Mode connection.** `apps.connections.open` with the app-level token answers
`{"ok":true,"url":"wss://wss-primary.slack.com/link/?ticket=…&app_id=…"}`; the URL carries a single-use `ticket`, a
secret. The WebSocket answers `101`, and its first frame is
`{"type":"hello","num_connections":1,"debug_info":{"host":"applink-2","build_number":28,"approximate_connection_time":18060},"connection_info":{"app_id":…}}`,
about five hours before a planned refresh. A second connection with the same URL got `101` but no `hello` within 3 s:
every connection needs a new `apps.connections.open`. — _2026-10-19 · the Go client on the stand_ · Used in C-24.FR-1,
C-24.FR-9.

**F-101. The planned refresh.** With `&debug_reconnects=true`, `approximate_connection_time` was 360; the frame
`{"type":"disconnect","reason":"warning"}` came after about 303 to 310 s and
`{"type":"disconnect","reason":"refresh_requested"}` after about 370 s, about 60 s after the warning (the documentation
promises the warning "about 10 seconds before"). `link_disabled`, sent when Socket Mode is turned off in the app's
settings, is known from the documentation only. — _2026-10-19 · two runs on the stand; Socket Mode documentation_ ·
Used in C-24.FR-9.

**F-102. Up to ten connections, and an event goes to any of them.** Of twelve connections opened at once,
`hello.num_connections` grew from 1 to 10; the eleventh and twelfth got `101` and at once
`{"type":"disconnect","reason":"too_many_websockets"}` without `hello`. `num_connections` counts every connection of the
app, a parallel test's included (it showed 2 with one connection of the client's own). The documentation: "up to 10
open WebSocket connections", "each payload may be sent to any of the connections… don't assume any particular pattern",
and with Socket Mode on, events and presses go only to the sockets, never over HTTP. — _2026-10-19 · the stand;
Socket Mode documentation_ · Used in C-24.FR-2, C-24.FR-9.

**F-103. Socket Mode through an HTTP proxy.** A Go `http.Transport` with `Proxy` set and `coder/websocket`'s `Dial`
with that client took both `apps.connections.open` and the WebSocket through a local CONNECT proxy — its log showed
`CONNECT slack.com:443`, then `CONNECT wss-primary.slack.com:443` — and `hello` arrived. A proxy that answers CONNECT
with `403` already breaks `apps.connections.open`. — _2026-10-19 · the stand_ · Used in C-24.FR-9.

### HTTP mode

**F-104. Slack's request signature.** `X-Slack-Signature` is `v0=` and the hex HMAC-SHA256, keyed with the app's Slack
signing secret, of `"v0:" + X-Slack-Request-Timestamp + ":" + the raw body`. A Go implementation reproduced the
documentation's example, `v0=a2114d57b48eac39b9ad189dd8316235a7b4a8d21a10bd27519666489c69b503` for the secret
`8f742231b10e8888abcd99yyyzzz85a5`, the timestamp `1531420618` and its example body. The window is five minutes, and the
raw body is signed before the form is parsed. The stand's Slack signing secret is 32 hex characters. In a local
simulation a correct request was answered `200`, a changed body `401` and a timestamp older than 301 s `401`. The Events
API's `url_verification` and its retries (three, at once, after 1 and after 5 minutes, with `x-slack-retry-num` and
`x-slack-retry-reason`) are documented but not used. No request from Slack itself was received: the stand had no Request
URL. — _2026-10-19 · the documentation's example vector in Go; [verifying
requests](https://docs.slack.dev/authentication/verifying-requests-from-slack), [Events
API](https://docs.slack.dev/apis/events-api)_ · Used in C-24.FR-9.

### User groups and mentions of everyone

The entries of this section were checked on 2026-11-15 on another test workspace in Slack's cloud, on the Pro plan
during its trial: an app installed with the bot scopes `chat:write`, `channels:read` and `users:read`, to which
`usergroups:read` was added during the check; the workspace's general channel and one other public channel, the bot a
member of both; and one person, a member of the user group `payments-team`, who watched in the Slack desktop client for
macOS (its version not recorded) and installed the mobile client during the last step. The Web API was called with
`curl` with a JSON body, except `usergroups.users.list` and `conversations.info`, which refused one and were sent with
form arguments (F-125). Not checked: a channel member outside the user group, other people than the one person, a
disabled user group, and the notifications themselves — no banner appeared on the desktop in any step, nor on the phone
once installed, for a reason in the clients' settings that was not established, so the client's highlighting, its badges
and its Activity are the evidence.

**F-125. User groups, the scope `usergroups:read` and a reinstall.** The workspace allowed only Workspace Admins and
Owners, not Members, to create and change user groups; the person, being an Admin or Owner, created one with the handle
`payments-team`. Without the scope, `usergroups.list` answered
`{"ok":false,"error":"missing_scope","needed":"usergroups:read","provided":"chat:write,channels:read,users:read"}` with
`x-accepted-oauth-scopes: usergroups:read`. With the scope added to the app's bot token scopes in its settings (OAuth &
Permissions) and the app not reinstalled, the settings asked for a reinstall, `x-oauth-scopes` of `auth.test` stayed
`chat:write,channels:read,users:read` and `usergroups.list` still answered `missing_scope`. After the reinstall
`x-oauth-scopes` was `chat:write,channels:read,users:read,usergroups:read`, and the bot token was the same: the token
used before the reinstall carried the new scope. `usergroups.list`, with `include_count` and `include_disabled` in a
JSON body, then answered `ok` with each user group's `id` (`S…`), `handle`, `name`, `date_delete` (`0`) and `user_count`
(`1`, right), beside `team_id`, `description`, `is_external`, `date_create`, `date_update`, `created_by`, `updated_by`,
`deleted_by`, `prefs` and `channel_count`; `usergroups.users.list` answered `users` with the member's `U…`.
`usergroups.users.list` with a JSON body answered `invalid_arguments` ("missing required field: usergroup"), and so did
`conversations.info`; with form arguments both answered, `conversations.info` with the channel's `name`, `is_general`,
`is_private` and `is_member` among its fields. — _2026-11-15 · `auth.test` and `usergroups.list` before the scope, after
adding it and after the reinstall · Slack Pro (trial)_ · Used in C-24.FR-1, C-24.FR-2, C-25.FR-8, C-28.FR-2.

**F-126. `<!subteam^ID>` from a bot mentions the user group's members, in a Root message and in a Thread reply.** A
bot's `chat.postMessage` in the other channel, with `text` "Muster spike test" and one `section` block whose mrkdwn
starts with `<!subteam^S…>`, was drawn as `@payments-team`, highlighted for the member as a mention of them, with the
red badge 1 on the channel and a badge on the app's icon in the macOS Dock. A Thread reply with the same block under a
bot message without a mention was drawn and highlighted alike and counted too: the Dock badge went to 2. The person's
Activity listed each of the last three Root messages as "Mention in #<channel>" and each of the three Thread replies as
"Mention in #<channel> thread", by the times written into the posts. — _2026-11-15 · a Root message and a Thread reply,
repeated four and three times; the Activity seen in a screenshot · Slack Pro (trial), desktop client_ · Used in
C-24.FR-6, C-25.FR-8, C-28.FR-2.

**F-127. `<!everyone>` from a bot mentions only in the general channel.** The bot's `chat.postMessage` with a `section`
block starting with `<!everyone>` was answered `ok` in both channels. In the general channel (`is_general` `true` in
`conversations.info`) it was drawn as `@everyone`, highlighted, and the person's Activity listed it as "Mention in
#<channel>". In the other channel (`is_general` `false`) it was drawn the same, highlighted, with no error or warning,
yet it was missing from the person's Activity: it did not mention the person, a member of that channel, and a reader
cannot tell the two apart. `<!channel>` was listed as "Mention in #<channel>" in both channels; `<!here>` was listed in
Activity in both, without the label "Mention". The workspace set no limit on who may use them, only its setting that
warns the sender before such a mention in a channel of six or more people. — _2026-11-15 · `<!everyone>`, `<!here>` and
`<!channel>` in each channel · Slack Pro (trial), desktop client_ · Used in C-24.FR-6, C-28.FR-2.

## GitLab

Unless an entry says otherwise, the facts were checked on 2026-10-31 against a self-managed GitLab 18.8.2-ee (revision
75b6aa72896, the Enterprise Edition binary) with the groups `muster-spike/platform` and `muster-spike/platform/infra` and
three users: `alice`, a direct Owner of `…/platform` (so an inherited Owner of `…/infra`), `bob`, a direct Maintainer of
`…/platform/infra`, and `carol`, a direct Developer of `…/platform`. Tokens were obtained through the API with the
password grant of an instance OAuth application (scopes `openid profile email`, then `read_user` added), the ID token
decoded and userinfo called with the access token; the records kept hold claim names and values, never a token; signatures were not checked.
`gitlab.com` and the authorization code flow with PKCE were not tried.

**F-116. Discovery lists all three role claims.** `/.well-known/openid-configuration` has `claims_supported` with
`https://gitlab.org/claims/groups/owner`, `…/maintainer` and `…/developer`, and also `groups` and `groups_direct`.
`scopes_supported` includes `openid`, `profile`, `email` and `read_user`. — _2026-10-31 · discovery of the test GitLab ·
GitLab 18.8.2-ee_ · Used in C-03.FR-5, C-25.FR-4.

**F-117. The role claims and `groups` come only from userinfo; the ID token has only `groups_direct`.** With the scopes
`openid profile email` the ID token carried `groups_direct` and no `https://gitlab.org/claims/…` key and no `groups`;
userinfo carried `groups` (the person's groups with the inherited ones, F-118) and the person's role claim and no
`groups_direct`, which in the ID token held the groups they are a direct member of. A sign-in that reads the ID token alone
never sees a role claim. — _2026-10-31 · the three users, ID token and userinfo compared · GitLab 18.8.2-ee_ · Used in
C-03.FR-5, C-25.FR-4.

**F-118. A role claim lists only the groups where the person holds that role directly.** `alice`, an Owner of
`muster-spike/platform`, had `…/owner` = [`muster-spike/platform`], without the inherited `muster-spike/platform/infra`;
that group appears for her, and for `carol`, only in `groups` (userinfo), which lists `platform` and `platform/infra`.
`bob`'s `…/maintainer` and `groups` were both [`muster-spike/platform/infra`]. An entry for a group below a direct
membership therefore matches a role claim only as a subtree entry (`muster-spike/platform/*`). — _2026-10-31 · the three
users · GitLab 18.8.2-ee_ · Used in C-03.FR-5, C-25.FR-4.

**F-119. A person has only the claim of the role they hold (users with one role each).** `alice` had `…/owner` and not `…/maintainer` or
`…/developer`; `bob` only `…/maintainer`; `carol` only `…/developer`. A role claim a person has no group for is absent,
not an empty list, and an Owner is not repeated under the lower roles; a person with different roles in different
groups was not tried. — _2026-10-31 · the three users · GitLab
18.8.2-ee_ · Used in C-03.FR-5, C-25.FR-4.

**F-120. The scope `read_user` changes nothing.** With `read_user` added to `openid profile email` the granted scope was
echoed, and the claim names and values of the ID token and of userinfo were identical for all three users. — _2026-10-31
· the three users, with and without the scope · GitLab 18.8.2-ee_ · Used in C-03.FR-5, C-25.FR-4.

## Pending

Before the stories of L2, these spikes check what L2 relies on:

- **Telegram** (C-27.FR-8, C-28, C-35, C-43). Left open by F-128 to F-142: what `getChatMember` answers in the
  discussion group about a person the bot never saw and about one removed or banned from it, and how soon after a leave
  it answers `left`; whether a Mention in a muted discussion group rings an admin who is not anonymous or not the
  creator, and the anonymous creator on a phone; whether the automatic copy rings an ordinary member who muted the
  group; the clients' notification exceptions for a chat; and a comment with `disable_notification`. For C-35: the
  budgets of the topics of one forum and of a channel beside its discussion group; what `getChatMember` answers in a
  basic group chat about a person who was never in it; whether a Mention there reaches a person outside the chat;
  whether the bot edits its messages in a basic group chat and in a private chat days later; whether a press on the
  button of a message sent before a basic group chat became a supergroup reaches the bot; a closed topic that a person
  created, for the bot as an admin with and without the right to manage topics; where the client shows a send without
  `message_thread_id` while General is closed or hidden (F-136); and a restricted member bot. For C-32: a comment that
  mentions a person whose phone is in a Focus mode (F-130). For C-43: a deleted account, and the budgets of a private
  chat and across chats, which F-135 did not reach.
- **Direct messages** (C-43), in Mattermost and Slack: the push of a Mattermost direct post with Do Not Disturb off and
  under `PushNotificationContents` `generic` (F-146); what `POST /api/v4/channels/direct` answers to a bot whose role
  lacks the permission to create direct channels (F-145); a deactivated Slack user (F-152); the workspace's edit window
  for a direct message hours or days old (F-084); and `chat.postMessage` to many direct chats of one workspace at once,
  which F-154 could not try with one person. The bridges' part of C-43 is checked by its stories with the fake bridge.

Earlier pending checks:

- **Telegram and Mattermost, several bots** (S-069). Two admin bots of one channel and its discussion group: whether
  each receives the automatic copies of the other's posts, their `edited_message` and people's comments, as the fake
  Telegram server assumes from F-003 and F-005 (the copy comes from Telegram, not from a bot), and that neither receives
  the other's messages (Bot API FAQ, "Why doesn't my bot see messages from other bots?"); whether a Mattermost bot with
  the role Member is refused an edit of another bot's post, which the fake Mattermost server answers `403`; and whether
  the budget of F-016 is each bot's own in a chat, as the fake assumes from Telegram's limits being a bot's. To check on
  the test stand with a second bot.
- **Mattermost.** How the client shows buttons on Thread replies, which the server accepts (F-055,
  [L1 open question 6](prd/L1.md#51-test-environment-facts)), and how phones are notified under
  `PushNotificationContents` `generic` and with the app killed ([question 4](prd/L1.md#51-test-environment-facts)): the
  first test server has push notifications off, and the second pushes under `full` (F-143, F-146). Whether a bot
  without the system admin role gets the same answers about a deleted root post as in F-058 — its plain read answered
  `404` on 2026-10-08 (F-061); `403` to an edit and `400` to a reply are still to be seen — and whether it may read the
  post with `?include_deleted=true`, which probably needs that role (not verified; Muster does not use that read).
  What push (under `PushNotificationContents` `full` and `generic`) and desktop notifications of a post with an empty
  `message` and an attachment with a `fallback` show, measured on the test stand: F-115 is read in the source only,
  since the first server pins `PushNotificationContents` to `generic` by an environment variable, and F-143 measured
  posts with a `message`. Groups (C-25.FR-8,
  C-28.FR-2): LDAP-synced groups on a server with LDAP on — whether a bot reads them, whether `@<group>` mentions their
  members and what `allow_reference` turned off does (F-121, F-123); the editions and licences other than Entry
  (F-121); what the reads of F-122 answer to a bot whose role lacks the custom-group permissions; that a channel member
  outside a group is not mentioned; and what the phone and desktop notifications of a group mention and of `@channel`,
  `@here` and `@all` from a bot show (F-123, F-124).
- **Mattermost 12.** Mattermost 12.0.0 draws message attachments as Mattermost Blocks; whether Root messages, their
  colour and the button styles of F-068 look the same is to be checked once the test stand is upgraded from 11.2.2.
  Since a Root message, a Storm summary and a test message hold all their content in their one attachment (C-13.FR-3),
  this covers the whole of what they show, their `fallback` in notifications included.
- **Alertmanager.** A restart of an HA instance without a persistent volume, and `--dispatch.start-delay` on an HA pair
  (tested on the single instance only); `externalURL` of instances without `--web.external-url`; a resolve during a mute
  with vmalert as the source; how often tick races of an HA pair cause duplicates over hours of operation — none in
  about 80 minutes of steady state ([L1 open question 7](prd/L1.md#51-test-environment-facts)).
- **Grafana.** Retries of the webhook contact point after a `5xx` or a timeout; `ha_single_node_evaluation`, which
  should give the copies of an HA pair the same `startsAt`; Grafana's silences and mute timings, by construction those
  of Alertmanager (F-045, F-047); a crash, and whether the notification log survives it; `for` above 0 and
  `keep_firing_for`; `DatasourceError`; several organizations (F-106); a custom payload and HMAC signing; an
  always-firing rule sent to the Heartbeat URL through a webhook contact point.
- **Slack.** A person's press in HTTP mode, and the envelope of F-093 in Socket Mode (F-151 logged the payload and
  `response_url` in Socket Mode), what the person sees when a press is not confirmed within 3 seconds, and whether
  Slack ever sends an unconfirmed `interactive` envelope or an interaction it could not deliver over HTTP again; which
  notifications a Mention added by an edit and an ephemeral answer in a Thread make (F-150 and F-153 saw a Thread
  reply and a direct message on a phone, and that an edit made no push); whether a Mention in a Thread reply pushes
  with the channel set to notify about Mentions only, in a reply with `reply_broadcast`, and with the app killed
  (F-150); what the phone
  and desktop notifications of `<!channel>` and `<!here>` from a bot show, which F-127 saw only in the client's
  Activity; whether the workspace's edit window applies to the bot's messages hours or days old (`edit_window_closed`,
  F-084); what `chat.update`, `chat.postMessage` and `conversations.info` answer after the bot is removed from a
  channel, and in an archived or a private channel (F-098); whether a zero-width space after `*`, `_`, `~` or `` ` ``
  keeps mrkdwn from pairing them into emphasis (C-24.FR-7); what `apps.connections.open` answers with Socket Mode off,
  and `link_disabled` (F-101); how long `num_connections` keeps counting a socket that went away without a close, such
  as a crashed Leader's (F-102); Socket Mode through a SOCKS5 or an HTTPS proxy (F-103); a user token given as the bot
  token; and the mobile clients — the colour bar, buttons beyond five in a row and `static_select` (F-075, F-082;
  F-149 saw the fields of a `section` on a phone); that a channel member
  outside a user group is not mentioned by `<!subteam^ID>`, what the phone and desktop notifications of `<!subteam^ID>`
  and `<!everyone>` show (F-126, F-127), and the `date_delete` of a disabled user group (F-125). To check on the test
  stand with a person, by the Slack stories.
