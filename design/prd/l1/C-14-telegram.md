# C-14. Telegram

[L1 index](../L1.md) · Stage: Shadow · UI: yes · Depends on: C-11, C-12, C-13

**Goal.** Deliver Alert Groups to Telegram channels as editable posts with comment threads and buttons, even when Muster
has no public address and even where Telegram is blocked — through a proxy or another Bot API address — without ever
leaking or misdirecting the bot token.

Names in the form `area.setting` refer to rows of [defaults.md](defaults.md); ids in the form `F-NNN` refer to
[verified facts](../../facts.md#telegram).

## Scenarios

1. The Admin creates a Telegram Connection with a bot token; Muster starts long polling.
2. The Admin creates a Destination by entering only the channel; Muster finds the channel's discussion group itself,
   and the check confirms that the bot is an admin in both. For a channel without comments, the check says to enable
   them; when the bot is not an admin of the group, it says to make it one there.
3. Acknowledge in the Muster UI edits the channel post. A press of Ack in Telegram from an account that is not linked
   yet gets a short private answer with a link to the profile.
4. A second Muster installation polls the same bot by mistake; Telegram answers `409`, Muster backs off, nothing becomes
   Broken.
5. The automatic copy of a post never reaches the bot — for example, the bot has lost its admin rights in the
   discussion group; Thread replies attach to the post once someone comments under it.
6. Telegram is blocked where Muster runs. The Admin points the Connection at a SOCKS5 proxy abroad; another installation
   sets the Bot API base URL to its own reverse proxy, `https://tg.example.org/k3x9/`, whose check first makes a dry
   probe without the token.
7. An on-call responder joins the channel's discussion group and mutes the channel itself. Each new Alert Group reaches
   them once — through its copy in the group — and so do its Thread replies; a colleague who only subscribed to the
   channel sees the Root messages but is not notified about the Thread.
8. Someone deletes the copy of a post in the discussion group. The next Thread reply goes to the group without the link
   to the post, and the delivery state shows "Thread not attached".

## Functional requirements

- **C-14.FR-1** A Telegram Connection has a name, a bot token (encrypted, write-only), a Bot API base URL
  (`connection.telegram.bot_api_base_url`), a proxy (C-03.FR-19), its limiter (`connection.telegram.limiter`) and an
  update mode (`connection.telegram.update_mode`): long polling with `getUpdates`, run only by the Leader, or a webhook
  at `MUSTER_INGEST_URL` that requires the secret token header. Muster requests the update types it needs explicitly. A
  `409 Conflict` from a second poller (F-018) only makes the poller back off; it never counts against the Connection.
  Deleting a Telegram Connection follows C-13.FR-6.
- **C-14.FR-2** A Telegram Destination in L1 is a channel with comments, and its settings name only the channel.
  Comments on a channel post are the thread of the post's automatic copy in the channel's linked discussion group;
  enabling comments on the channel in Telegram creates and links such a group. The discussion group is required. Muster
  finds it itself, through `getChat` on the channel (`linked_chat_id`, F-002), and shows it on the Destination,
  read-only; a channel without one fails the Destination check (FR-14). The channel carries only Root messages, Storm
  summaries and test messages; everything else about an Alert Group goes to its Thread (FR-3). Muster never replies
  inside the channel: such a reply is a new post that notifies every subscriber and reaches the discussion group as one
  more copy (F-015). Plain group chats and forum topics are not in L1. Saving runs the Destination check (FR-14) and
  refuses a Destination that fails it. Its limiter is `destination.telegram.limiter`, which counts sends and edits
  alike, in the channel and in its discussion group, because Telegram counts both against one budget per chat (F-016).
- **C-14.FR-3** The Root message is a channel post with an inline keyboard. The Thread is the post's comment thread:
  replies to the automatic copy of the post in the discussion group (F-007). Muster recognizes the copy as a message in
  the discussion group with `is_automatic_forward` whose `forward_origin` names the channel and the post id, and matches
  it to the Root message by that id; the copy reaches only a bot that is an admin of the discussion group, usually a few
  seconds after the post (F-003, F-005). The copy is buffered because it may arrive before or after the answer to the
  send call. A copy is never taken for an edit: neither the `edit_date` it carries from the start nor the
  `edited_message` that Telegram sends when it mirrors an edit of the post onto the copy changes anything (F-005,
  F-006). Thread replies wait up to `telegram.copy_wait` for the copy; if it does not come, Muster takes the copy's id
  from the first comment a person writes under the post; until then, Thread replies go to the discussion group as a
  chain of replies not attached to the post, and the delivery state shows "Thread not attached". When Telegram refuses
  a Thread reply because the copy has been deleted ("message to be replied not found", F-008), the Thread is lost:
  Muster sends that reply to the discussion group without the link to the copy, later Thread replies follow as a chain
  not attached to the post, and the delivery state shows "Thread not attached".
- **C-14.FR-4** Button data (at most 64 bytes) carries an opaque action id, the key id and a signature; Muster verifies
  it and checks that it belongs to the chat and message of the press: the channel for a Root message, the discussion
  group for a Thread reply such as a Reminder (F-009). Presses older than `telegram.press_max_age` are dropped and
  logged. Telegram does not say when a press was made — a `callback_query` carries only the date of the pressed
  message — so a press counts as too old when the update that carries it arrives after a gap longer than
  `telegram.press_max_age` in which the Connection received no updates: no successful long poll, or, in webhook mode,
  Muster not running. Presses that Telegram kept during a long outage are thus dropped, while presses after a quiet
  period are not.
- **C-14.FR-5** Every press is answered with `answerCallbackQuery` (at most 200 characters) at once, before the Root
  message is edited — Telegram refuses an answer about 15 seconds after the press (F-010): success, the refusal of
  C-10, or — for an account without an Account link — "Your Telegram account is not linked to Muster. Link it in your
  profile: {link}"; nothing changes on such a press.
- **C-14.FR-6** Quiet new messages carry `disable_notification`, which in Telegram silences the push but does not
  remove it (F-012).
- **C-14.FR-7** Response mapping: `429` with `retry_after` → RetryAfter for the Destination, and for the whole Connection
  when two of its Destinations get `429` within 60 seconds; "message is not modified" → success (F-017); "message to edit not found"
  → the deleted Root message flow; "message to be replied not found" on a Thread reply → the lost Thread of FR-3, not an
  error of the Destination; `401` and "bot was kicked" or missing rights → Fatal; an error inside a `200` body
  (`ok: false`) is classified the same way; a non-JSON answer is a transport error (Transient); anything else → unknown.
- **C-14.FR-8** The bot answers `/start <token>` for Account links (C-18).
- **C-14.FR-9** The documentation covers creating the bot, enabling comments on the channel (which creates its
  discussion group), adding the bot as an admin to the channel — Telegram adds a bot to a channel only as an admin
  (F-001) — and to that group, long polling versus webhook, and Telegram in restricted networks as C-21.FR-7 describes.
  It tells on-call people to join the discussion group and mute the channel itself: each Root message then reaches them
  once, through its copy in the group, and only members of the discussion group receive Thread replies, even replies
  that mention them (F-014). It names a known limitation: a Quiet Root message reaches members of the discussion group
  with sound, because Telegram notifies them about the automatic copy (F-013).
- **C-14.FR-10** The Bot API base URL is an absolute `https` or `http` URL without query, fragment or user information;
  a path prefix is allowed and a trailing `/` is normalized; requests go to `<base>/bot<token>/<method>`. An `http` base
  URL is accepted with the warning of [reference.md](reference.md#banners-warnings-and-notices). The base URL is an
  ordinary target under the outbound address policy and the Connection's proxy. The field carries the hint about
  self-hosted Bot API servers from the same table.
- **C-14.FR-11** The connection check works in steps and shows the latency and path (direct or through the proxy) of
  each: (1) a dry probe `GET <base>/bot0:x/getMe` without the token, which passes on a `401` JSON answer and otherwise
  names what answered (DNS, TLS, timeout, proxy `407`, not JSON — "this is not a Bot API", `404` — wrong path prefix);
  (2) `getMe` with the token — the bot's username; (3) `getWebhookInfo` — whether a webhook is set (which makes long
  polling fail with `409`) and how many updates are pending. Steps 2 and 3 run only against the saved base URL: while
  the form holds an unsaved base URL, only the dry probe runs. Muster never sends the token to any address other than
  the saved base URL — no check, metric or background task does.
- **C-14.FR-12** The bot token never appears in a log line, an error shown in the UI, the Timeline or a metric,
  including errors that embed the request URL; logs show only the scheme and host of the base URL (C-02.FR-20).
- **C-14.FR-13** The adapter escapes alert data for Telegram HTML (C-12.FR-7) and declares a length limit of 4,096
  characters (C-12.FR-11).
- **C-14.FR-14** The Destination check of a Telegram Destination calls `getChat` for the channel, takes the discussion
  group from its `linked_chat_id`, and calls `getChatMember` for the bot in the channel and in that group; it passes
  when the channel exists, has a linked discussion group, and the bot is an admin allowed to post and edit messages in
  the channel and to post in the group. Two failures are reported separately, with the texts of
  [reference.md](reference.md#banners-warnings-and-notices): the channel has no linked group — comments are not enabled
  for the channel, and the person is told to enable them; the bot is not an admin of the discussion group — the person
  is told to make the bot an admin there. It runs when the Destination is saved (FR-2) and from "Check" on the
  Destination page (C-13.FR-9) — both through the interactive path, because a person waits for them — as the Broken
  probe when nothing is waiting (C-11.FR-9), in the delivery client class and never on the interactive path, and in
  `muster doctor` (C-02.FR-14); a successful check of a Broken Destination ends the Broken state.
- **C-14.FR-15** Every edit of a Telegram message carries the whole inline keyboard of its new state, even when only
  the text changed: an edit without `reply_markup` removes the buttons (F-011).
- **C-14.FR-16** The Root message is a regular message with Telegram HTML markup. The Alerts are a list, one line per
  Alert, never a table: tables break words on phones (F-020). The label sections of C-12.FR-1 (items 4 to 6) are inside
  an expandable blockquote (`<blockquote expandable>`), collapsed until a person opens it. Rich Messages
  (`sendRichMessage`, F-019) are not used in L1.

## UI

Connections → Telegram (create, edit, base URL with its hint and warning, step-by-step check, update mode, proxy form,
delete); the Telegram Destination form with the channel only and a check that shows the discussion group found;
"Thread not attached" in the delivery state.

## API surface

`connections` of type Telegram (list, create, read, update, delete); `connections/{id}/checks` (create → result);
`destinations` of type Telegram (create, update); the webhook endpoint `callbacks/telegram/{connection_id}` on the ingest listener (it answers `401` to a missing
or wrong secret token header and to an unknown Connection alike).

## Acceptance

Checked against the fake Telegram server.

- **C-14.AC-1** A new Alert Group creates a channel post; a new Alert creates a reply to the automatic copy in the
  discussion group.
- **C-14.AC-2** With the copy withheld, Thread replies go to the discussion group unattached until a comment under the
  post arrives, then attach.
- **C-14.AC-3** A `409` from `getUpdates` causes back-off and no Broken state.
- **C-14.AC-4** A press that arrives after the Connection received no updates for longer than `telegram.press_max_age`
  changes nothing.
- **C-14.AC-5** The dry probe passes against a server answering `401` JSON to `/bot0:x/getMe` and fails with "this is
  not a Bot API" against one answering HTML; neither request carries the real token.
- **C-14.AC-6** With a changed but unsaved base URL, the check makes only the dry probe; no request with the real token
  reaches the new address.
- **C-14.AC-7** A network error on a request to the fake server leaves the token in no log line and no error shown in
  the UI.
- **C-14.AC-8** A label value `@channel <b>x</b>` arrives as literal text; an Alert Group with 25 Alerts fits in 4,096
  characters with its title, status, footer, buttons and link.
- **C-14.AC-9** A press from a Telegram account without an Account link changes nothing and gets the "not linked"
  answer with the profile link.
- **C-14.AC-10** With a base URL that has a path prefix, every request goes to `<base>/bot<token>/<method>` on the fake
  server; with a SOCKS5 proxy, only through the fake proxy.
- **C-14.AC-11** With the update mode set to webhook, a request to the webhook endpoint without the secret token header,
  or with a wrong one, is refused and changes nothing; the same press with the right header is processed.
- **C-14.AC-12** With the bot's admin rights removed in the channel of the fake server and nothing waiting, the probe
  keeps the Destination Broken with the missing right as the reason; after the rights are restored, the next probe ends
  the Broken state with no Alert Group activity. The probe's `getChat` and `getChatMember` requests are counted in
  `muster_client_requests_total` under the delivery client class, not the interactive one.
- **C-14.AC-13** A Destination saved with only a channel whose comments are enabled shows the discussion group that
  `getChat` links to it; for a channel without a linked group the check fails with "comments are not enabled" and the
  Destination is not saved; with the bot not an admin of the group the check fails with "the bot is not an admin of the
  discussion group".
- **C-14.AC-14** After Acknowledge, the edit of the Root message carries the keyboard of the acknowledged status, and
  the fake server's message keeps its buttons through every later edit.
- **C-14.AC-15** An `edited_message` for the automatic copy of a post, and a copy that arrives with `edit_date` set,
  change no Root message and add no Timeline entry or delivery event.
- **C-14.AC-16** With the copy deleted on the fake server, the next Thread reply is refused with "message to be replied
  not found", is sent again to the discussion group without the reply link, and the delivery state shows "Thread not
  attached"; the Destination stays healthy, and later Thread replies go to the group unattached.
- **C-14.AC-17** A Root message is sent with HTML markup; its label sections are inside `<blockquote expandable>`, its
  Alerts are lines of a list, and it contains no table.
- **C-14.AC-18** A press on Ack is answered with `answerCallbackQuery` before the Root message is edited.
- **C-14.AC-19** Muster posts nothing to the channel but Root messages, Storm summaries and test messages: every
  lifecycle event of an Alert Group after its Publication reaches the fake server as a reply in the discussion group or
  as an edit of the Root message.

## Related ADRs

ADR-0005, ADR-0007, ADR-0011, ADR-0013, ADR-0015.

## Depends on

C-11 — delivery; C-12 — rendering; C-13 — the shared Destination pages.

## Suggested story split

- **BE** — Connection with base URL and dry probe, polling and webhook modes, adapter (posts in HTML with expandable
  labels, comment threads and the lost Thread, buttons kept on every edit, callbacks, error mapping, escaping, token
  redaction, Destination check), fake server extension.
- **FE** — Connection pages with the step-by-step check, the Telegram Destination form, the "Thread not attached" mark.
