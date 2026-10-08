# Verified facts

- Status: Living document
- Date: 2026-10-08

External behaviour that Muster's design relies on and that was checked against the real system rather than taken from
its documentation: how Telegram, Mattermost and Alertmanager actually behave. Each fact has a stable id `F-NNN`, never
reused, followed by the fact itself, the date it was checked, the method and the version of the external system where
it is known, and the requirements that rely on it. When a later check contradicts a fact, the entry is corrected with
the new date and the requirements that cite it are revisited. Facts still to be checked are listed under
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

**F-018. A second poller interrupts the first.** A second `getUpdates` on the same token ends the long poll already
running with `409 Conflict: terminated by other getUpdates request`; after that the two pollers take turns. —
_2026-10-03 · two pollers on one token · Bot API 10.3_ · Used in C-14.FR-1.

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

## Mattermost

Unless an entry says otherwise, the facts were checked on 2026-10-04 against a test Mattermost 11.2.2 server
(Enterprise Edition without a license, default configuration unless an entry says otherwise). A test bot with the system
admin role used the REST API v4 in a test channel; a small HTTP receiver on a private address logged button presses as
raw JSON and answered them. Presses and notifications were made and observed in the client of a test user.

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
`create_post_ephemeral`, and the Connection check of that bot passed with the warning `press_answers_in_thread`. —
_2026-10-08 · `SessionHasPermissionTo` and `RolesGrantPermission` in `server/channels/app/authorization.go` and
`getRolesByNames` in `server/channels/api4/role.go` of the v11.2.2 source; `GET /api/v4/users/me` and
`POST /api/v4/roles/names` with the bot token on the test stand, and Muster's Connection check against it ·
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

**F-060. A username without `@` does not notify.** A bot post whose `message` and attachment text both ended with
"Acknowledged by <username>" — the username of a channel member, written without `@` and mentioned nowhere else, as
the footer of a Root message names a person — left that member's `mention_count` at `0`: it was read with
`GET /api/v4/channels/{channel_id}/members/{user_id}` before the post and again 3 s after. The member kept their own
notification settings; a person who adds their username to their own mention keywords would still be notified. —
_2026-10-08 · one post in a public channel with a bot without the system admin role, deleted afterwards · Mattermost
11.2.2_ · Used in C-12.FR-12, C-13.FR-3.

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

## Pending

- **Editing old messages (Telegram).** Whether the bot can still edit a channel post, and its own message in the
  discussion group, 48 hours and 7 days after sending it, with and without an inline keyboard. A post sent on 2026-10-03
  is edited after 2026-10-05 and again after 2026-10-10. If edits stop working, long-lived Root messages need
  republishing ([L1 open question 3](prd/L1.md#51-test-environment-facts)).
- **Mattermost.** How the client shows buttons on Thread replies, which the server accepts (F-055,
  [L1 open question 6](prd/L1.md#51-test-environment-facts)), and how phones are notified
  ([question 4](prd/L1.md#51-test-environment-facts)): the test server had push notifications off. Whether a bot
  without the system admin role gets the same answers about a deleted root post as in F-058 — its plain read answered
  `404` on 2026-10-08 (F-061); `403` to an edit and `400` to a reply are still to be seen — and whether it may read the
  post with `?include_deleted=true`, which probably needs that role (not verified; Muster does not use that read).
- **Alertmanager.** A restart of an HA instance without a persistent volume, and `--dispatch.start-delay` on an HA pair
  (tested on the single instance only); `externalURL` of instances without `--web.external-url`; a resolve during a mute
  with vmalert as the source; how often tick races of an HA pair cause duplicates over hours of operation — none in
  about 80 minutes of steady state ([L1 open question 7](prd/L1.md#51-test-environment-facts)).
