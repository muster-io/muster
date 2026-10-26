# C-24. Slack

[L1 index](../L1.md) · Stage: Shadow · UI: yes · Depends on: C-11, C-12, C-13, C-16, C-17, C-18, C-23

**Goal.** Deliver Alert Groups to Slack channels as editable messages with Threads and working buttons, natively —
through the bot of a Slack app in one workspace and Slack's Web API — and receive presses either over Socket Mode,
which needs no address of Muster that Slack can reach, or over HTTP at Muster's ingest address, without ever leaking a
token or losing an answer to the person who pressed.

Names in the form `area.setting` refer to rows of [defaults.md](defaults.md); ids in the form `F-NNN` refer to
[verified facts](../../facts.md#slack).

## Scenarios

1. The Admin creates a Slack app from the manifest in the documentation, installs it in the workspace and creates a
   Slack Connection with the bot token and the app-level token. The check shows the bot, the workspace and that both
   tokens belong to one app; the Leader opens the Socket Mode connection, and Muster needs no public address.
2. Another installation has a public ingest address and no outgoing WebSocket: its Connection uses HTTP. The Admin
   copies the Connection's callback address into the app's Interactivity Request URL and enters the app's Slack signing
   secret.
3. The Admin creates a Destination for `#alerts-prod`, picking it from the channels the bot is a member of; for a
   channel without the bot, the check says to invite it.
4. A firing Alert Group appears with a red bar, a green Ack, Resolve and a "Snooze…" menu; Acknowledge in Muster edits
   the message in place, and new Alerts go to its Thread.
5. Carol presses Ack before linking her account; only she sees "Your Slack account is not linked to Muster. Link it in
   your profile: {link}". Bob enters his Slack email in his profile; the bot sends him a code by direct message, and he
   types it into the profile.
6. Someone deletes a Root message; the next edit finds it gone, and Muster posts the Alert Group again once.
7. Another Connection, of the same or a second Muster installation, is connected to the same Slack app in Socket Mode
   by mistake. The Connection page and `muster doctor` warn that presses are shared between them at random.
8. The bot is removed from `#alerts-prod` at night, and the Destination becomes Broken. In the morning someone invites
   it back; the next probe ends the Broken state, though no alert has fired.

## Functional requirements

- **C-24.FR-1** A Slack Connection is the bot of one Slack app installed in one workspace. It has a name, the bot token
  (`xoxb-…`), the event mode (`connection.slack.event_mode`: `socket` or `http`), the app-level token (`xapp-…`, with
  the scope `connections:write`), required in `socket` mode, the Slack signing secret, required in `http` mode, a proxy
  (C-03.FR-19) and its limiter (`connection.slack.limiter`). The two tokens and the Slack signing secret are Secrets:
  encrypted, write-only, re-encrypted by a key rotation (C-20.FR-7), and never in a log line, an error shown in the UI,
  the Timeline or a metric (C-02.FR-20) — nor is the one-time `ticket` in the address of a Socket Mode connection
  (F-100). Muster talks to Slack only through the Web API at `https://slack.com/api/` with the bot token, through Socket
  Mode with the app-level token, and through the `response_url` of a press when it answers the person that way (FR-11):
  no Events API, no incoming webhooks, no user tokens. The address is fixed, so a Slack Connection has no address field;
  a stored token goes only there, through the Connection's saved proxy (C-13.FR-14). The bot needs the scopes
  `chat:write`, `channels:read`, `groups:read`, `im:write`, `users:read` and `users:read.email`, and no other. A read
  returns, read-only, the callback address of FR-9 and what the last successful check learned: the bot's name and user
  id, the workspace's name and id and the app's id. One Slack app serves one Slack Connection: in Socket Mode, two
  Connections on one app, of one installation or of two, would share its presses at random (FR-9), and in `http` mode
  an app has one Request URL. Deleting a Slack Connection follows C-13.FR-6.
  _Revised for L2 (2026-10-26):_ the bot may also have the scope `usergroups:read`, which only Team mentions and user
  group entries need (C-25.FR-8, C-28.FR-2); without it they are unavailable and nothing else changes. The manifest of
  the documentation adds it, and the upgrade notes of 0.2.0 say that an installed app is reinstalled to get it.
- **C-24.FR-2** The connection check (interactive) works in steps and shows the latency and path (direct or through the
  proxy) of each: `token` — `auth.test` with the bot token, which shows the bot's name and the workspace (F-072) and
  fails with `token_invalid` for `invalid_auth`, `not_authed`, `account_inactive`, `token_revoked` and `token_expired`,
  and with `wrong_token_type` when Slack answers `not_allowed_token_type` or the answer names no `bot_id`, so the token
  is not a bot's (F-070, F-072); `scopes` — the `x-oauth-scopes` header of that answer must name every scope of FR-1,
  else `missing_scope` names the missing ones (F-071); and in `socket` mode `app_token` — `auth.test` with the app-level
  token answers the app's `app_id`, which must equal the `bot.app_id` that `bots.info` answers for the bot's `bot_id`,
  else `app_mismatch` ("The app-level token belongs to another Slack app than the bot token."), and
  `apps.connections.open` must answer `ok` (F-072, F-100), else `token_invalid`, or `wrong_token_type` for a bot token
  typed as the app-level token; the check opens no WebSocket. In `socket` mode the check passes with the warning
  `another_client_connected` while the last `hello` the Leader received for the Connection counted more connections of
  the app than the Leader holds (F-102, FR-9); `muster doctor` prints the same as a WARN line. In `http` mode no request
  can show that Slack reaches the callback address or that the Slack signing secret is right, because a bot cannot press
  its own button (F-094): the first press does, and the Connection page says so. When the check may not send a token — a
  changed but unsaved proxy and no token typed again (C-13.FR-14) — it makes only a dry probe, `POST
  https://slack.com/api/auth.test` without `Authorization` through the draft's proxy, which passes on a `200` JSON
  answer `ok: false` with the `error` `not_authed` (F-070) and otherwise names what answered: `not_slack` (an answer
  that is not that JSON), `unexpected_status`, and the network codes of the other steps.
  _Revised for L2 (2026-10-26):_ the step `scopes` reports `usergroups:read` as present or missing without failing, and
  the Connection keeps the answer for the forms that need it.
- **C-24.FR-3** A Slack Destination has a Connection, a channel id — a public or private channel, `C…` or `G…`; any
  other id, such as a direct chat's `D…` or a user's `U…`, to which a message would go as a direct message (F-096), is
  refused — its Mention settings (C-12.FR-8) and its limiter (`destination.slack.limiter`), which counts sends and
  edits alike. Its Destination check, the item `channel`, calls `conversations.info` (F-098): it fails with
  `channel_not_found` for an unknown or deleted channel and for a private channel the bot is not in, with
  `channel_archived`, and with `bot_not_in_channel` and the text "Invite the bot to the channel: /invite @{bot}"; it
  passes otherwise and stores and shows the channel's name. It runs when the Destination is saved, which refuses a
  failing one, and from "Check" on the Destination page — both through the interactive path — as the Broken probe when
  nothing is waiting (C-11.FR-9), in the delivery client class and never on the interactive path, and in
  `muster doctor` (C-02.FR-14); a successful check of a Broken Destination ends the Broken state. The Destination form
  offers the channels the bot is a member of, public and private and not archived, read with `conversations.list` page
  by page (rate limit tier 2, F-099), and takes an id typed by hand.
- **C-24.FR-4** Every message Muster sends to Slack carries its content in blocks and a short `text`, which is only the
  fallback of a notification: a long `text` would be split into several messages, only the last of which Muster could
  edit (F-077), and an edit with a `text` over 4,000 bytes in Slack's stored form fails (F-078). The Root message of
  C-12.FR-1: its `text`, and a `section` at the top, hold the summary line — the status emoji of item 1, `#N` and the
  title — followed by the Mentions of a Loud message, outside the attachment; one attachment in the colour of item 1
  (F-075) holds blocks — a `section` with the heading of item 2 linked to the Alert Group page; a `section` with items 3
  to 11: the body, one line of links that starts with "Open in Muster" and goes on with the links of C-12.FR-9, the
  notices and the footer; a `context` with "Muster v<version>", without an image, which Slack would have to fetch from
  `MUSTER_PUBLIC_URL`, often an internal address; and an `actions` block with the buttons of FR-5. Every link, "Open in
  Muster" included, is a link in the text, never a button. The `section` of items 3 to 11 holds at most 3,000 code
  points of mrkdwn as sent, entities and link markup included (F-076): the adapter declares that limit, so a longer
  message is shortened as C-12.FR-11 says, by its Alert list first, always keeping the link to Muster; the body never
  goes into an attachment's `text`, which Slack cuts without a warning (F-080). In `text` the title is cut to 60 code
  points and the Mentions that do not fit in 1,000 bytes are left out, though they stay in the top `section`, so `text`
  stays under 4,000 bytes even when every character becomes an entity or an emoji's shortcode (F-078, F-079). Every send
  and every edit names both `blocks` and `attachments`, an empty list where there are none, because an edit without them
  keeps or replaces what was there (F-085), and sets `unfurl_links` and `unfurl_media` to `false` (F-083). Muster never
  sends a message made only of `rich_text` blocks, which could not be edited back into sections (F-086), and does not
  use the `markdown` block, which the server turns into `rich_text` (F-081). Thread replies and Storm summaries follow
  the same rules — their text in a `section`, a short `text`, no attachment, and a Reminder's buttons in an `actions`
  block — and a test message is laid out as a Root message.
  _Revised for L2 (2026-10-26):_ from 0.2.0 the blocks follow the compact layout of C-29.FR-4, with the fields of its
  item 2 as the `fields` of one `section`.
- **C-24.FR-5** A Root message carries the buttons of its status
  ([reference.md](reference.md#buttons-and-links-by-status)), a Reminder "Still on it" and "Unack" (C-17.FR-4). Slack
  has two button styles, `primary` (filled green) and `danger` (filled red), and asks for one `primary` per set (F-074):
  Ack is `primary` on a firing and on a snoozed Root message, Resolve on an acknowledged one, "Still on it" on a
  Reminder; every other button is plain, and `danger` is never used. The Snooze durations, then the Snooze presets
  (C-10.FR-18), are the options of one `static_select` menu "Snooze…" — the web client shows only five buttons in a row
  and folds the rest (F-082). Each button's `value`, and each option's `value`, is the opaque action id signed with the
  button-signature key and its key id (ADR-0011), at most 150 bytes so that it fits an option (F-076); each `action_id`
  names the command and is unique within its block. Labels stay within 75 characters.
- **C-24.FR-6** An edit is `chat.update` of the Root message's `ts` with the whole message of the new state — Quiet,
  without Mentions (ADR-0005); a bot edits its own messages, and an edit to the same content is accepted as well
  (F-084). A Thread reply is `chat.postMessage` with the Root message's `ts` as `thread_ts` (F-089), never with
  `reply_broadcast`. Slack cannot send a message without a notification: whom a new message notifies depends on each
  member's settings for the channel and the Thread, so Quiet means "without a Mention", as in Mattermost (C-13.FR-8),
  and a Loud event is a new message — a Root message or a Thread reply — with the Mentions it asks for. A Mention of a
  User with an Account link for the Destination's Connection is `<@U…>` (F-097); a User without one is named by their
  display name as text, which notifies nobody; everyone in the chat is `<!channel>` or `<!here>`, only as the
  Destination's Mention settings ask. A Slack Destination takes nobody, `channel`, `here` and chosen users, and refuses
  `all` and messenger groups. The footer names a User linked for the Connection by `<@U…>`, in edits and new messages
  alike (C-12.FR-12).
  _Revised for L2 (2026-10-26):_ a Slack Destination also takes `everyone` (`<!everyone>`) and, with `usergroups:read`,
  user groups and Team mentions as `<!subteam^ID>` (C-28.FR-2).
- **C-24.FR-7** The adapter escapes alert data for Slack's mrkdwn (C-12.FR-7): `&`, `<` and `>` become `&amp;`,
  `&lt;` and `&gt;`, so that nothing in alert data becomes a link, a Mention, `<!channel>` or `<!here>` (F-097); `@`
  gets the zero-width space of C-12.FR-7; and since mrkdwn has no escape for `*`, `_`, `~` and `` ` `` (F-097), each of
  them in alert data is followed by a zero-width space, so that it does not pair into emphasis, strikethrough or code. A
  Route's Root message template and line template render items 3 to 8 for a Slack Destination in a markup of their own,
  `mrkdwn` (C-12.FR-2), which template previews offer beside the others.
- **C-24.FR-8** Response mapping, by the `error` of an `ok: false` answer, never by the status alone (F-070): `429` →
  RetryAfter for the whole Connection, for the seconds of `Retry-After`, because Slack limits each method per app and
  workspace (F-099), not counted as an attempt, and Transient when `Retry-After` is missing; `5xx`, timeouts, network
  errors, `internal_error`, `fatal_error`, `service_unavailable` and `request_timeout` → Transient; `invalid_auth`,
  `not_authed`, `account_inactive`, `token_revoked`, `token_expired`, `not_allowed_token_type` and `missing_scope` —
  whose reason names the scope in `needed` — → Fatal; `channel_not_found`, `not_in_channel`, `is_archived` and
  `restricted_action` → Fatal; `msg_too_long`, `invalid_blocks`, `invalid_attachments`, `no_text` and `block_mismatch`
  (F-076, F-078, F-086) → the markup was rejected (C-11.FR-8), and Muster sends the same message as plain `text` alone,
  within 3,000 bytes, counted and recorded as a delivery event; `edit_window_closed`, not seen for a bot's message
  (F-084), and `cant_update_message` → unknown, as an edit refused by Mattermost's edit time limit is ([L1 open question
  12](../L1.md#52-other-questions)); anything else → unknown. A deleted Root message is found by its edit: `chat.update`
  answers `message_not_found` (F-087), and Muster then reads the channel with `conversations.info` — when the bot is a
  member and the channel is not archived, the Root message was deleted and the deleted Root message flow starts
  (C-11.FR-13); otherwise the answer is Fatal. A Root message deleted after it had replies stays as a tombstone, under
  which Thread replies still go (F-090); its next edit starts the flow. A Root message deleted before any reply takes no
  reply: Slack posts a reply to it into the channel as an ordinary message and answers `ok` without `message.thread_ts`
  (F-091), so Muster deletes that message at once with `chat.delete` and starts the deleted Root message flow.
  `chat.getPermalink` is not a test of a deletion: it answers `ok` for a tombstone (F-088).
- **C-24.FR-9** A Slack Connection receives presses in its event mode. **Socket Mode** (`socket`): the Leader holds one
  WebSocket connection for each Slack Connection in `socket` mode, as it polls Telegram and Bridges (ADR-0007) — through
  the outbound package and the Connection's proxy, an HTTP or HTTPS proxy by CONNECT, as F-103 saw through an HTTP
  proxy, or a SOCKS5 proxy — in the background client class (ADR-0015), and only to a `wss` address under `slack.com`
  (outside `muster dev`). Each connection starts with a new `apps.connections.open` (F-100) and waits up to
  `slack.hello_timeout` for `hello`; on `disconnect` with the reason `warning` or `refresh_requested` Muster opens the
  next connection before it closes the old one (F-101); a failure, `too_many_websockets` (F-102) or `link_disabled`
  backs off by `slack.socket_backoff`, is logged, never counts against the Connection and makes no Destination Broken.
  Every envelope is confirmed at once on its socket with its `envelope_id`, before its press is handled (F-093). When a
  `hello` counts more connections of the app than the Leader holds, another program — usually another Connection or
  installation — is connected to the same app and would take some of its presses (F-102): Muster logs it, keeps the
  count on the Connection for its check (FR-2) and page, and goes on; right after a Leader crashed, Slack may still
  count the old Leader's connection until it notices that it is gone, so the count is taken again at every `hello`. A
  Leader change closes the connection, and the presses made until the next Leader connects are lost: the person sees
  Slack's error, and Muster does not count on Slack to send them again ([Pending](../../facts.md#pending)); an
  installation that cannot lose a press uses HTTP. **HTTP** (`http`): Slack posts each press to
  `MUSTER_INGEST_URL/api/v1/callbacks/slack/{connection_id}` on the ingest listener; Muster verifies `X-Slack-Signature`
  over the raw body with the Connection's Slack signing secret and refuses an `X-Slack-Request-Timestamp` further than
  `slack.request_tolerance` from its real clock (F-104); it answers `401` to a missing or wrong signature, a stale
  timestamp, an unknown or deleted Connection and a Connection in `socket` mode alike, so the endpoint reveals nothing
  about which Connections exist, `400` to a body without a `payload`, and `200` with an empty body to every other
  request at once, before the press is handled (F-093). Slack sends presses only to the sockets while the app has Socket
  Mode on (F-102), so a Connection receives presses only in its own mode.
- **C-24.FR-10** A press is a `block_actions` payload (F-093); other kinds of interaction are confirmed and ignored. The
  action's `value` — or `selected_option.value` for an option of the Snooze menu — carries the signed action id: Muster
  verifies it with the key it names, checks that the press's `container.channel_id` and `container.message_ts` belong to
  the Root message or Thread reply the action id was issued for, of a Destination of this Connection (a mismatch is
  refused like a bad signature), maps `user.id` to a User through their Account link for this Connection (FR-12) and
  dispatches the Command with the Transport `slack` (C-10). A replica handles at most `slack.parallel_presses` presses
  at a time; the others wait, already confirmed, up to `slack.max_waiting_presses` of them in a process; a press beyond
  that, and a press still waiting or running at the end of the shutdown's grace period, fails without an answer and is
  logged. A successful press is not answered:
  the edited Root message shows its result, except "Still on it", which changes no message and is answered privately as
  C-17 says. A refusal, a failure, a press from an account without an Account link ("Your Slack account is not linked to
  Muster. Link it in your profile: {link}"), a Viewer's or a disabled User's press (C-18.FR-8), a press whose action id
  cannot be verified ("This button could not be verified; nothing was changed.") and a press on a test message ("This is
  a test message; nothing was changed") are answered by FR-11. A press from an account without an Account link changes
  nothing.
- **C-24.FR-11** Muster answers the person who pressed with `chat.postEphemeral` to that person in the channel of the
  press — with the Root message's `ts` as `thread_ts` for a press on a Thread reply, such as a Reminder — which needs no
  more than `chat:write` (F-092), through the interactive path within `delivery.interactive_budget`. When that call
  fails — the person is no longer in the channel, Slack refuses it, or no limiter token is free within the budget — the
  same text goes to the press's `response_url` as an ephemeral answer, with `replace_original` `false` (F-093), and only
  to an `https` address on `hooks.slack.com` (outside `muster dev`). Muster never edits or deletes a Root message
  through `response_url`: only the delivery worker edits it (C-11.FR-2). Answers are in the profile language of the User
  the account is linked to, and in English for an account without a link, or for a press whose deadline,
  `slack.press_deadline` from its hand-over, ended while it waited for a slot; an answer that fails both ways is logged
  and not retried.
- **C-24.FR-12** Account links through a Slack Connection follow C-18 — started only from the signed-in web session
  (C-18.FR-1), one account per User per Slack Connection (the Identity space is the Connection), refusals, unlinking and
  the Audit log with the messenger `slack` — by a code sent by direct message, as for Mattermost (C-18.FR-3): the user
  chooses a Slack Connection in the profile and enters their Slack email or name. An email is looked up with
  `users.lookupByEmail`, which ignores case (F-095); a name with `users.list`, page by page, compared with `name` and
  `profile.display_name` regardless of case, leaving out deleted accounts, bots and Slackbot. No match is refused with
  "No Slack account with this email or name was found.", several with "Several Slack accounts have this name. Enter
  the email instead." The bot then sends the code by direct message — `chat.postMessage` to the account's user id
  (F-096) — with the warning "You requested a link to Muster. Do not share this code."; the user types the code into the
  profile, and after it is confirmed the bot sends "Linked to Muster user {name}". Codes follow
  `account_link.slack_code` and are limited by `account_link.slack_code_requests`, per requesting User and per target
  account; a wrong code is refused as for Mattermost, with the attempts left. The link stores the account's user id, its
  name and its display name — `profile.display_name`, else `profile.real_name` — which names the account where Muster
  shows the link; Mentions and the footer write it as `<@U…>` (FR-6). The
  link-making messages are interactive calls (C-18.FR-11). The app's Messages tab stays on and read-only, so the person
  sees the code and cannot write to the bot (F-096); Slack documents that with the tab off it refuses the direct message
  (`messages_tab_disabled`; not yet seen on the stand, [Pending](../../facts.md#pending)), and then the request is
  refused with the problem `messages_tab_disabled` (`409`), still counted toward the limits above.
- **C-24.FR-13** The Destination test and preview of C-16 apply. "Send test message" posts one message marked "🧪 Test
  message" with the buttons of its status, through the interactive path. It shows that delivery works and cannot show
  that presses reach Muster, because a bot cannot press its own button in Slack (F-094); the result says so, and that
  pressing a button on the test message checks it — answered "This is a test message; nothing was changed" once the
  press reaches Muster. "Preview" shows the message as Muster would send it.
- **C-24.FR-14** The Request log (C-23) records Slack's requests like those of the other messengers: outbound Web API
  calls with the method as Slack names it (`chat.postMessage`, `apps.connections.open`), the presses that reach the
  callback of FR-9 in `http` mode (`interactive`) with Muster's answer, refused ones included, and, in `socket` mode,
  each envelope that carries an `envelope_id` as an inbound record of its `type` (`interactive`), with the socket's
  host as its URL, the envelope as its request body and Muster's confirmation as its answer, without a status; `hello`,
  `disconnect` and pings are not recorded. Masked as everywhere: the bot token and the app-level token, the Slack
  signing secret and the value of `X-Slack-Signature`, the legacy verification `token` of a Slack payload, the `url`
  that `apps.connections.open` answers with its `ticket`, `response_url`, the signed action ids in `value` and
  `selected_option.value`, and the code in a direct message.
- **C-24.FR-15** A Slack Destination becomes Broken and recovers as C-11.FR-9 says, after a Fatal answer or when the
  Transient budget runs out; the probe is the Destination check of FR-3. A Slack Connection whose token was revoked does
  not become Broken itself: its Destinations do, through their deliveries and probes, and its check shows the cause.
- **C-24.FR-16** The documentation covers, under Messengers, connecting Slack: creating the app from the manifest — the
  bot named Muster in the app's settings, since a bot without `chat:write.customize` posts under that name (F-073), the
  Messages tab on and read-only for the codes of Account links, interactivity on, Socket Mode on, the scopes of FR-1 and
  what each is for — with the second manifest for HTTP, whose Interactivity Request URL is the Connection's callback
  address; installing the app and creating the app-level token with `connections:write`; inviting the bot to each
  channel; Socket Mode or HTTP — no address of Muster needed against no press lost during a Leader change; one Slack app
  per Connection; the proxy, which must allow CONNECT to `slack.com`, to the hosts under `slack.com` of the
  socket and to `hooks.slack.com`; Loud and Quiet in Slack, where Quiet means "without a Mention"; Mentions and Account
  links by code; the limiters and Slack's rate limits; edits and deleted Root messages, including the reply that
  appears in the channel for a moment when a Root message was deleted before any reply (FR-8); the test message, which
  cannot press its own button; and the Request log. The Messengers page's table of what each Destination type can do
  gains Slack.
- **C-24.FR-17** The Slack stories register their log events — at least `slack_press`, `slack_press_rejected`,
  `slack_socket_connected`, `slack_socket_shared` and `slack_socket_failed` — and extend the metric catalogue: `slack`
  joins the `transport` values of `muster_commands_total`; requests to Slack, the Socket Mode connection included, are
  counted in `muster_client_requests_total` by client class like every outbound request. Slack adds no Internal alert:
  its Destinations raise `MusterDestinationBroken`.

## UI

Connections → Slack (create, edit, the event mode with the app-level token for Socket Mode or the Slack signing secret
and the callback address for HTTP, the step-by-step check with the bot, the workspace and the app — also before saving
(C-13.FR-14) — the warning that another program shares the app, proxy form, delete); the Slack Destination form with
the channel picker and an id field, the check that shows the channel's name, Mention settings with nobody, `@channel`,
`@here` or chosen users; the test panel's note that a test cannot press its own button; Profile → Messenger accounts →
"Link Slack" per Connection, with the email or name field and the code entry; the labels of Connections and
Destinations across the UI (S-070) for the Slack type; the `mrkdwn` markup in the template previews of the Route editor.

## API surface

In `api/openapi.yaml`, added by the stories that implement them: `connections` of type `slack` (list, create, read,
update, delete; reads carry the callback address and what the last check learned); `connections/{id}/checks`;
`connections/checks` for a draft of the type (C-13.FR-14); `connections/{id}/channels` (the channels the bot is a member
of); `destinations` of type `slack` (create, update); `destinations/{id}/checks` with the item `channel`, `tests` and
`previews` for the type; `me/account-links/slack` (`startSlackLink` → code sent) and
`me/account-links/slack/confirmation` (`confirmSlackLink`, with the code); the callback
`callbacks/slack/{connection_id}` on the ingest listener; `slack` in the `Transport` and `Messenger` enums, and `mrkdwn`
in the markups of `previewTemplate`.

## Acceptance

Checked against the fake Slack server (C-01.FR-13), unless an acceptance statement names the test workspace.

- **C-24.AC-1** The Connection check against the fake Slack server shows the bot's name, the workspace and the app;
  with a revoked bot token it fails at `token` with `token_invalid`; with a bot token that lacks `users:read.email` it
  fails at `scopes` with `missing_scope` naming that scope; with an app-level token of another app it fails at
  `app_token` with `app_mismatch`; in `http` mode it has no step `app_token`.
- **C-24.AC-2** "Check connection" in the create form, with the fake server's tokens, runs `token`, `scopes` and
  `app_token` with the typed tokens and saves no Connection and no Audit log entry. In the edit form of a saved
  Connection with only the proxy changed and no token typed, the check makes only the dry probe `auth.test` without
  `Authorization`, through the new proxy, and no request that carries a stored token is made.
- **C-24.AC-3** With an HTTP proxy on the Connection, every Web API request and the Socket Mode connection reach the
  fake Slack server only through the fake proxy, the socket by CONNECT; a network error leaves the bot token, the
  app-level token, the Slack signing secret and the socket's `ticket` in no log line and no error shown in the UI.
- **C-24.AC-4** Saving a Destination for a channel the bot is not in is refused with `bot_not_in_channel` and "Invite
  the bot to the channel: /invite @{bot}", for an archived one with `channel_archived`, for an unknown one with
  `channel_not_found`, and for an id that starts with `D` or `U` with `422`; a channel the bot is in is saved with its
  name, and the form lists the bot's channels over several pages of `conversations.list`.
- **C-24.AC-5** A new Alert Group creates a message whose `text` and top `section` are the summary line, with one
  attachment in the firing colour whose blocks are the heading linked to the Alert Group page, the body with the line
  of links that starts with "Open in Muster", the context "Muster v<version>", and the actions Ack (`primary`), Resolve
  and the menu "Snooze…" with the Route's durations and then the Snooze presets. Acknowledge through the API edits it
  with Unack, Resolve (`primary`) and the menu, naming both `blocks` and `attachments`; a new Alert creates a Thread
  reply with the Root message's `ts` as `thread_ts`.
- **C-24.AC-6** A label value `@channel <!here> <b>x</b> *y* <https://evil.example|z> &` reaches the fake server as
  literal text: `&`, `<` and `>` as entities, a zero-width space after `@` and after `*`, and no Mention, link or
  emphasis.
- **C-24.AC-7** An Alert Group with 40 Alerts reaches the fake server with a body `section` of at most 3,000 code points
  that keeps the line of links, the notices and the footer; an Alert Group whose title is 200 emoji has a `text` that
  stays under 4,000 bytes when every emoji is counted as its shortcode.
- **C-24.AC-8** A `429` with `Retry-After: 1` delays the next request through that Connection, to any of its
  Destinations, by 1 to 2 seconds and is not counted as an attempt; `not_in_channel` makes the Destination Broken at
  once; `invalid_blocks` sends the message as plain `text` and records the rejected markup; `chat.update` answered
  `message_not_found` while the bot is still a member publishes the Alert Group again once, Quietly, with "The previous
  message was deleted at HH:MM", and leaves the Destination healthy, and with the bot no longer a member makes it
  Broken; a Thread reply answered without `message.thread_ts` is deleted from the channel and the Alert Group is
  published again once; `edit_window_closed` ends the edit as Not delivered.
- **C-24.AC-9** A new Alert Group meets `not_in_channel` because the bot was removed in the fake server, and resolves
  while the Destination is Broken. After the bot is added back, the next probe — the Destination check — ends the
  Broken state and resolves `MusterDestinationBroken` with no new Alert Group, its `conversations.info` counted in
  `muster_client_requests_total` under the delivery client class; "Check" on the Destination page does the same at
  once, through the interactive path.
- **C-24.AC-10** In `socket` mode the Leader opens one connection per Slack Connection, after a new
  `apps.connections.open`; a press the fake server sends through the socket is confirmed with its `envelope_id` before
  its Command runs, and acknowledges the Alert Group as the linked User with the Transport `slack`; after
  `disconnect` with `refresh_requested` the next connection is open before the old one closes; a `hello` that counts two
  connections while the Leader holds one makes the check pass with `another_client_connected`, logs
  `slack_socket_shared` and makes `muster doctor` print a WARN line; with two replicas, only the Leader holds a
  connection, and after it stops the new Leader opens one.
- **C-24.AC-11** In `http` mode a press signed with the Connection's Slack signing secret is answered `200` with an
  empty body at once and is handled; a wrong signature, a timestamp 6 minutes old, an unknown Connection and a
  Connection in `socket` mode are each answered `401` and change nothing.
- **C-24.AC-12** A press with a tampered `value`, or with a `value` issued for another message, changes nothing and is
  answered "This button could not be verified; nothing was changed."; a press from an account without an Account link
  changes nothing and is answered with the "not linked" text and the profile link; a Viewer's and a disabled User's
  presses get their refusals; the option "4h" of the Snooze menu snoozes the Alert Group for 4 hours and the option of
  the `tomorrow` preset until the end of C-10.FR-17; "Still on it" on a Reminder answers it.
- **C-24.AC-13** A refusal reaches the person through `chat.postEphemeral` in the channel of the press — with
  `thread_ts` for a press on a Reminder; with the fake server failing `chat.postEphemeral`, the same text goes to the
  press's `response_url` with `replace_original` `false`; no request ever carries `replace_original` `true`.
- **C-24.AC-14** A code request for a Slack email in other letter case finds the account, the bot sends the code by
  direct message, and the right code links the account; after five wrong codes the sixth attempt is refused even with
  the right code; a name found on the second page of `users.list` links the same way; a name two accounts share is
  refused with "Several Slack accounts have this name. Enter the email instead."; an account already linked to another
  User is refused; a press from the linked account runs as its User.
- **C-24.AC-15** A Destination that mentions Alice for new Alert Groups posts a Loud Root message whose `text` and top
  `section` carry `<@U…>` of her link and whose attachment carries no Mention; without a link she is named as text; a
  Destination that mentions `here` carries `<!here>`; a Slack Destination whose Mention settings name `all` or a group
  is refused; an Alert Group acknowledged by Alice shows "Acknowledged by {her display name}".
- **C-24.AC-16** A test message reaches the fake server as one message with "🧪 Test message" and changes no Alert
  Group's Timeline; the test's result says that it cannot press its own button; a press on its button is answered "This
  is a test message; nothing was changed".
- **C-24.AC-17** The Request log of a Slack Connection shows `chat.postMessage` with its body and answer, a press at the
  callback with Muster's `200`, and an envelope of the socket with its confirmation; none of its records holds a token,
  the Slack signing secret, a `ticket`, a `response_url` or a signed action id.
- **C-24.AC-18** The Slack Connection page shows the app-level token field in Socket Mode, and the Slack signing secret
  field with the callback address and its hint in HTTP mode, the check with the bot, the workspace and the app, and the
  warning that another program shares the app; the Destination form picks a channel and shows its name from the check;
  the profile links a Slack account by email or name and a code — all in English and Russian, without horizontal
  scrolling at 360 px wide.
- **C-24.AC-19** On the test workspace, with the maintainer's app: in `socket` mode a new Alert Group appears in the
  test channel with its colour bar, the green Ack and the Snooze menu, and a person's press of Ack, from a Slack account
  linked to a Responder, acknowledges it as that User; in `http` mode, with the app's Request URL set to a public
  address of the ingest listener, the same press does the same.
- **C-24.AC-20** The documentation site has the page "Connect Slack" with both manifests, linked from the navigation;
  the Messengers page's table has a Slack column; and `make docs-check` passes.

## Related ADRs

ADR-0001, ADR-0005, ADR-0007, ADR-0011, ADR-0013, ADR-0014, ADR-0015, ADR-0016.

## Depends on

C-11 — delivery; C-12 — rendering, templates and Mention settings; C-13 — the shared Destination pages and the check
before saving; C-16 — the Destination test; C-17 — Reminder buttons; C-18 — Account links; C-23 — the Request log.
C-24 was added after the other capabilities, when the maintainer took native Slack into L1 before its release (journal
D381, D382, D385): a team on Slack needs it from the Shadow stage on, and it builds on everything the earlier
messengers built.

## Suggested story split

- **BE** — Connections, the Web API client with its error classes, the Connection check and the check before saving,
  key rotation of the three Secrets, and the fake Slack server.
- **BE** — Destinations, the Destination check, the channel list, `muster doctor` and the Mention settings of Slack.
- **BE** — delivery: messages in blocks with the status colour, mrkdwn and its escaping, buttons and the Snooze menu,
  edits, Thread replies, deleted Root messages, error classes, test messages, previews and the Broken probe.
- **BE** — presses over HTTP: the signed callback, presses, ephemeral answers and Reminder buttons.
- **BE** — Socket Mode held by the Leader: the WebSocket through the outbound package, confirmation of envelopes,
  refresh and back-off, and the warning about a shared app.
- **BE** — Account links by a code by direct message, and Mentions of linked people.
- **FE** — the Slack Connection and Destination pages, the profile's link dialog, labels and the `mrkdwn` preview.
- **Docs** — Connect Slack with the manifests, and Slack on the Messengers page.
