# C-22. Bridge

[L1 index](../L1.md) · Stage: Shadow · UI: yes · Depends on: C-11, C-12, C-13, C-16, C-17, C-18

**Goal.** Reach a messenger that Muster has no native support for through a Bridge: a separate service, written by
anyone for their messenger, that Muster talks to over the Bridge protocol — with the same Root messages, Threads,
Commands, Account links and Broken handling as Mattermost and Telegram, and with Text commands in the Thread where the
messenger has no buttons. Muster ships the protocol, a fake bridge and a conformance check, and the project publishes
bridge libraries for Go and Python with an example bridge, so that a bridge can be written and checked without
Muster's maintainers.

Names in the form `area.setting` refer to rows of [defaults.md](defaults.md). The protocol is
[`api/bridge.yaml`](../../../api/bridge.yaml); operation names such as `postMessage` are its `operationId`s, and
"`info`" is the answer of its `getInfo`. The decisions behind it are
[ADR-0017](../../adr/0017-bridge-protocol-for-messengers-without-native-support.md).

## Scenarios

1. A team's messenger has a bot API but no Muster support. An engineer adds the bridge library for their language — Go
   or Python — to a small service, writes the calls to that API, and checks the bridge with `muster bridge check`; the
   bridge stays in the team's own repository.
2. The Admin creates a Bridge Connection with the bridge's address and token; the check shows the bridge, the
   messenger's name, the protocol version and what the bridge offers.
3. The Admin creates a Destination for a channel by its id; the check asks the bridge whether the bot can post and edit
   there, without posting, and shows the channel's name.
4. A new Alert Group appears as a Root message with buttons; Acknowledge edits it; new Alerts go to its Thread.
5. The messenger has no buttons. The Root message links to Muster and names the commands; a responder replies `ack` in
   the Thread, and the bot answers "Done: Acknowledged #42".
6. Alice copies the token from her profile and sends it to the bot through the link the bridge offers; the bot answers
   with her Muster name. Bob, on a bridge that finds people by email, enters his email; the bot sends him a code by
   direct message, and he types it into his profile.
7. The messenger lets a bot edit a message for ten minutes only. An Alert Group acknowledged an hour after it fired
   gets a new Root message, and its old Thread gets a reply that points to it.
8. Someone deletes a Root message; the bridge reports the deletion, and Muster publishes the Alert Group again once.
9. The bot is removed from the channel at night; the bridge answers `forbidden`, and the Destination becomes Broken.
   In the morning the bot is back, and the next probe ends the Broken state.
10. The bridge cannot reach Muster, so its Connection uses polling; another installation lets its bridge push events to
    Muster's ingest address, signed with a secret both sides know.

## Functional requirements

- **C-22.FR-1** A Bridge Connection has a name, the bridge's base URL — an absolute `http` or `https` URL without
  query, fragment or user information; a path prefix is allowed and a trailing `/` is normalized; requests go to
  `<base>/v1/…` — a token (`bot_token`, encrypted, write-only, as for the other Connection types: one token is one
  bot), the event delivery mode (`connection.bridge.event_delivery`: `poll` or `push`), with `push` a push secret
  (encrypted, write-only, the Standard Webhooks `whsec_` form), a proxy (C-03.FR-19) and its limiter
  (`connection.bridge.limiter`). Muster sends the token as `Authorization: Bearer` on every
  request, only to the saved base URL; the base URL is an ordinary target under the outbound address policy and the
  Connection's proxy (ADR-0015). The token and the push secret never appear in a log line, an error shown in the UI, the
  Timeline or a metric (C-02.FR-20), and a key rotation re-encrypts both like every other Secret (C-20.FR-7). The
  Connection returns, read-only, the callback address of FR-11 for push and the last `info` it read. Deleting a Bridge
  Connection follows C-13.FR-6.
- **C-22.FR-2** The connection check (interactive) calls `getInfo` and shows, with the latency and path (direct or
  through the proxy) of the call, the bridge's name and version, the messenger's name, `protocol_version`, what the
  bridge offers, its limits, its kind of user lookup and its event delivery modes. It fails, naming the cause, when the
  token is refused ("The bridge refused the token."), when `protocol_version` is not one Muster speaks — `1` — when the
  answer does not match the protocol or is larger than 64 KiB, or when the Connection's event delivery mode is not one
  the bridge offers. Muster keeps the last `info` on the Connection, reads it again on every check and, before a call
  that depends on it, when it is older than `bridge.info_max_age`, so a change in the bridge reaches Muster without a
  restart.
- **C-22.FR-3** A Bridge Destination has a Connection, a channel id — the channel as its messenger names it, an opaque
  string — its Mention settings (C-12.FR-8) and its limiter (`destination.bridge.limiter`). Its Destination check calls
  `checkChannel`, which posts nothing, as its one check item `channel`: it passes when the bridge answers `200`, and
  stores and shows the channel's name; it fails with the bridge's `detail` when the bridge answers `forbidden`, as
  `proxy_refused` when the Connection's proxy refuses its credentials (FR-8), and with the error class otherwise. The
  check runs when the Destination is saved, which refuses a failing one, from "Check" on the Destination page — both
  through the interactive path — as the Broken probe when nothing is waiting (C-11.FR-9), in the delivery client class
  and never on the interactive path, and in `muster doctor` (C-02.FR-14); a successful check of a Broken Destination
  ends the Broken state. Mention settings take nobody or chosen users, as in Telegram; everyone in the chat and
  messenger groups are refused.
- **C-22.FR-4** Every message Muster posts through a bridge is an envelope (`MessageInput`): the kind (`root`,
  `storm_summary`, `test`), the status with its colour and emoji (item 1 of C-12.FR-1), the title — `#N` and the
  Alert Group's title (item 2) — the link to the Alert Group page as "Open in Muster", the body, the buttons, the
  Mentions and `notify`, which is `true` for a Loud message. The body holds items 3 to 11 of C-12.FR-1 in CommonMark
  with strikethrough: a Route's Root message template and line template render items 3 to 8 in that markup
  (C-12.FR-2, a new markup `commonmark` beside Markdown, HTML and plain), and Muster adds the links, notices and footer.
  Alert data is escaped for CommonMark and `@` is neutralized (C-12.FR-7), so nothing in alert data becomes markup, a
  link or a Mention. The bridge renders the envelope in its messenger's own markup; Muster never writes a messenger's
  markup for a bridge. Thread replies, Storm summaries and test messages use the same body rules.
- **C-22.FR-5** When `info` declares `buttons`, a Root message carries the buttons of its status
  ([reference.md](reference.md#buttons-and-links-by-status)), with Snooze durations as separate buttons, and a Reminder
  carries "Still on it" and "Unack" (C-17.FR-4). Each button has a label, a style hint — Ack and "Still on it"
  `primary`, Resolve `success`, every other `neutral` — and data of at most 64 printable ASCII bytes that carries the
  opaque action id, the key id and the signature with the button-signature key (ADR-0011). Muster sends at most
  `buttons.max_per_message` buttons, dropping Snooze durations from the longest first. Without `buttons`, a message
  carries none; when the bridge declares `thread_messages`, the body of a Root message that is not resolved ends with
  the line "Reply in the Thread: ack · resolve · snooze {first duration}" for its status (C-22.FR-14) — "Reply in the
  Thread, mentioning the bot: …" when the bridge forwards only messages that mention the bot — in the Route's language;
  without either, the link to Muster is the only way to act.
- **C-22.FR-6** A Publication is `postMessage` with an `Idempotency-Key` that names the delivery and its Publication and
  stays the same across the retries of that Publication, so a Publication cut off by a crash or a timeout is posted
  once. The answer's `message_id` names the Root message, and its `url`, when present, is the message's link on the
  Alert Group page. A `replayed` answer is followed by an edit to the current Desired state, because the retry may
  carry a newer state than the request whose answer was lost; a retry after a started Publication whose answer is not
  `replayed` records a possible duplicate (C-11.FR-12): the bridge did not know the key. An edit is `editMessage` with
  the whole envelope of the new state — Quiet, without Mentions (ADR-0005). A Thread reply is `replyInThread` under the
  Root message with its own `Idempotency-Key`, the last `thread_ref` Muster received for that Root message — from
  `postMessage` or an earlier reply, `null` without one — and `notify`; Muster stores the `thread_ref` each answer
  returns. Loud new messages carry their Mentions (FR-16); a Quiet new message carries none, and the bridge sends it
  without a notification where it declares `quiet_messages`.
- **C-22.FR-7** Bridge delivery shortens each body to `limits.max_body_length` of the Connection's `info`, in code
  points, as C-12.FR-11 shortens a message to its messenger's limit — by its Alert list first, always keeping its
  title, status, footer, buttons and link — reading `info` again when it is older than `bridge.info_max_age`, so the
  limit is that of the Destination's Connection, not one constant of the messenger type. The bridge makes the final cut
  for its rendered markup and reports it with `truncated`.
- **C-22.FR-8** Response mapping, by the problem's `class`: `transient` → Transient; `rate_limited` → RetryAfter for
  the seconds of `Retry-After`, for the Destination or, with `scope` `bot`, for every Destination of the Connection —
  Transient when `Retry-After` is missing or longer than `bridge.retry_after_max`;
  `gone` on an edit or a Thread reply → the deleted Root message flow (C-11.FR-13), or with the `reason`
  `edit_window_closed` on an edit → FR-9; `forbidden` → Fatal, with the problem's `detail` as the reason; `invalid` →
  unknown, which ends that delivery as Not delivered. A problem without a class, or an answer that is not a problem, is
  classified by the protocol's own mapping (ADR-0017), not by that of outgoing webhooks (C-15.FR-6): `401` and `403` →
  Fatal; `408` → Transient; `429`, and `503` with `Retry-After`, → RetryAfter as above, Transient without it; other
  `5xx`, timeouts and network errors → Transient; `404`, `410`, a redirect and everything else → unknown. Muster never
  decides that a message is gone from a status code. A `407` of the Connection's proxy is no answer of the bridge: it
  counts as a request without an answer — Transient — and the checks of FR-2 and FR-3 report it as `proxy_refused`, as
  the Telegram check does (C-14.FR-11).
- **C-22.FR-9** When `info` declares `limits.edit_max_age_seconds`, Muster does not edit a Root message once it is older
  than that less `bridge.edit_window_margin`, measured from its Publication on the business clock. When the status of
  its Alert Group changes after that, Muster publishes a new Root message Quietly, sends a Thread reply under the old one
  — "This message is no longer updated. The current one: {link}", where the link is the new message's `url` when the
  bridge returned one and the Alert Group page otherwise — and sends later Thread replies under the new Root message.
  Any other change leaves the old Root message as it is and reaches the Thread as the
  [Loud/Quiet table](reference.md#loud-and-quiet) says; the delivery state shows "Past the edit window: updated on the
  next change of status". An edit refused as `gone` with the `reason` `edit_window_closed` marks the Root message as
  past its window and is handled the same way. The older Root message keeps working: a press on its buttons and a Text
  command in its Thread act on the Alert Group as on the current one. A final edit (C-11.FR-14) of a Root message past
  its window is sent as a Thread reply with the same text. Each republication is a delivery event (C-11.FR-21).
- **C-22.FR-10** When `info` declares `deletion_events`, a `message_deleted` event for a Root message starts the deleted
  Root message flow (C-11.FR-13) at once; for any other message of the bot it changes nothing. Without it, Muster learns
  of the deletion from `gone` on the next edit or Thread reply.
- **C-22.FR-11** Events — `press`, `thread_message`, `account_link` and `message_deleted` — carry a stable `id`, their
  `type`, `occurred_at` and their data, and Muster handles each `id` of a Connection once within
  `bridge.event_dedup_window`: it claims the id before the event's Command runs and records it afterwards, so two copies
  of one event run one after the other, and a crash between the Command and the record repeats the Command once on
  redelivery, which its preconditions turn into a refusal or a no-op (ADR-0017). Muster handles every event type of the
  protocol, whatever its copy of `info` declares. Each Connection has its own token, and a bridge serving several keeps
  their events apart by token; a press or a Thread message about a message Muster did not post through that Connection
  is ignored without an answer. Muster answers an event (FR-13), when it does, before it acknowledges it. A Connection
  receives events in its event delivery mode. **Poll**: the Leader calls `pollEvents` with the stored cursor and
  `bridge.poll_wait`, waits for the answer up to `bridge.poll_wait` plus 15 seconds, handles the batch, then stores
  `next_cursor` on the Connection; a cursor the bridge refuses as `invalid` is dropped and the next poll starts without
  one; a failed poll backs off in the background client class (ADR-0015) and never counts against the Connection or
  makes a Destination Broken. **Push**: the bridge posts each event to
  `MUSTER_INGEST_URL/api/v1/callbacks/bridge/{connection_id}` on the ingest listener with the Standard Webhooks
  headers; Muster verifies `webhook-signature` with the Connection's push secret and refuses a `webhook-timestamp`
  further than `bridge.push_tolerance` from its real clock; it answers `204` once the event is handled or was handled
  before, `400` to a body that is no event, and `401` to a bad signature, an unknown or deleted Connection and a
  Connection in poll mode alike, so the endpoint reveals nothing about which Connections exist.
- **C-22.FR-12** A `press` names the message whose button was pressed, the person (`external_id`, `display_name`) and
  the button's data. Muster verifies the signature with the key it names, checks that the data was issued for that
  message — a Root message or a Thread reply of a Destination of this Connection, a mismatch refused like a bad
  signature — maps the person to a User through their Account link for this Connection (FR-15), and dispatches the
  Command with the Transport `bridge` (C-10). The bridge has already confirmed the press to its messenger (ADR-0017). A
  press whose `occurred_at` is older than `bridge.event_max_age` by Muster's real clock is dropped, unanswered, and
  logged. A successful press is not answered — the edited Root message shows its result; a refusal, a failure, a press
  from an account without an Account link ("Your account is not linked to Muster. Link it in your profile: {link}"),
  a Viewer's or a disabled User's press (C-18.FR-8) and a press on a test message ("This is a test message; nothing was
  changed") are answered by FR-13.
- **C-22.FR-13** Muster answers the person behind an event with `answerEvent` — the event's `id` and a plain text of at
  most 500 characters — through the interactive path within `delivery.interactive_budget`; the bridge shows an answer
  to a press privately where its messenger allows, otherwise in the Thread; an answer to a Text command in its Thread;
  an answer to an Account link request in the direct chat it came from. Answers are in the profile language of the User
  the account is linked to, and in English for an account without a link, as the Mattermost and Telegram bots answer
  during linking. Answers never go through delivery and never touch a Root message (ADR-0005); an answer that fails is
  logged and not retried.
- **C-22.FR-14** When `info` declares `thread_messages`, a `thread_message` under a Root message of the Connection is a
  Text command when its whole text — trimmed, case-insensitive, with an optional leading `/` — is one of `ack`, `unack`,
  `resolve`, `snooze <duration>`, `unsnooze` and `still on it`, or one of their Russian synonyms
  (`bridge.text_commands`); `<duration>` is one of the Route's Snooze durations (`route.snooze_durations`), written as
  minutes, hours or days (`30m`, `4h`, `1d`; `30м`, `4ч`, `1д`). Muster maps the writer through their Account link and
  dispatches the Command for that Alert Group with the Transport `bridge`, exactly as a press; `still on it` answers the
  open Reminder (C-17.FR-10). It answers through FR-13 with "Done: {command} #N", with the refusal of C-10.FR-2, with
  the answers of FR-12 for unlinked, Viewer and disabled accounts, and for a duration that is not one of the Route's with
  "Snooze for one of: {durations}". Any other text is ignored and answered by nothing, and so is a Text command whose
  `occurred_at` is older than `bridge.event_max_age`. Only Bridge Destinations take Text commands.
- **C-22.FR-15** Account links through a Bridge Connection follow C-18 — started only from the signed-in web session
  (C-18.FR-1), one account per User per Connection (the Identity space is the Connection), refusals, unlinking, the
  Audit log with the messenger `bridge` — in two ways:
  1. **Token**, offered by every bridge: the profile shows, for a chosen Bridge Connection, a token — 32 random bytes in
     base64url (43 characters), single use, valid for `account_link.bridge_token_ttl`, stored as a hash and bound to the
     User and the Connection — with "Send this token to the bot in a direct message", and a link built from
     `link_url_template` when `info` has one, an `http` or `https` URL checked like every link Muster shows. When the
     bridge reports an `account_link` event with the token, Muster links the sender's account and answers through
     FR-13 with "Linked to Muster user {name} ({login})"; a token that is unknown, used, expired or bound to another
     Connection links nothing and is answered "This link is invalid or expired. Start again from your Muster
     profile."
  2. **Code**, offered when `info` declares `direct_messages` with `bot_can_start` and a `user_lookup` other than
     `none` — never while Muster has not read `info` yet, when only the token is offered: the user enters their
     handle in a field labelled by the lookup kind — Email, Login, Username or User id; Muster finds the account
     with `findUsers`, sends it a code with `sendDirectMessage` and the warning "You requested a link to Muster. Do
     not share this code.", and the user types the code into the profile. Codes follow
     `account_link.bridge_code` and are limited by `account_link.bridge_code_requests`, per requesting User and per
     target account. A handle the bridge does not find is refused with "No account with this {kind} was found." The
     link-making messages are interactive calls (C-18.FR-11).

  A link stores the account's `external_id` and `display_name`, and its handle when the bridge reports one.
- **C-22.FR-16** A Mention of a User with an Account link for the Destination's Connection is an entry of the
  envelope's `mentions` — `external_id` and `display_name` — and a marker `[@{display_name}](muster-mention:{ref})` in
  the body; a bridge with `native_mentions` renders it as its messenger's mention, any other as the plain name. The
  Mentions of a Loud message form the first line of its body. A User without such a link is named by their display name
  as text, which mentions nobody. The footer names a linked User by the link's `display_name` (C-12.FR-12).
- **C-22.FR-17** The Destination test and preview of C-16 apply: "Send test message" makes one `postMessage` of the
  kind `test`, marked "🧪 Test message", through the interactive path, and a press on it is answered "This is a test
  message; nothing was changed"; "Preview" shows the envelope as Muster would post it.
- **C-22.FR-18** The Connection and Destination forms propose half of `limits.send_per_second` and
  `limits.send_per_second_per_channel` as limiters when `info` has them, and the defaults otherwise; `rate_limited`
  answers stay the truth (FR-8).
- **C-22.FR-19** A Bridge Destination becomes Broken and recovers as C-11.FR-9 says: after a Fatal answer, with the
  bridge's `detail` as the reason, or when the Transient budget runs out — including when the bridge itself does not
  answer; the probe is the Destination check of FR-3. A Bridge Connection whose bridge cannot be reached does not become
  Broken itself; its Destinations do, through their deliveries and probes, and its Connection check shows the cause.
- **C-22.FR-20** `api/bridge.yaml` is the contract of the protocol: OpenAPI 3.1, linted by `make lint`, versioned by
  its `/v1/` paths and `protocol_version` `1`. An additive change keeps version 1; a change that breaks bridges takes a
  new major path, and CI fails a pull request that breaks it (oasdiff against the base branch). It is published on the
  documentation site as a rendered reference, and it is licensed MIT.
- **C-22.FR-21** `muster bridge check <url>` runs the protocol against a bridge, without a Muster database, with the
  token from `--token-file` and a channel from `--channel`: `getInfo`; `checkChannel`; `postMessage`, and the same
  request again with its `Idempotency-Key`, which must return the same `message_id` with `replayed`; `editMessage`;
  `replyInThread` twice, the second with the first's `thread_ref`; `answerEvent`, for an event the bridge reported or,
  without one, for an unknown event id, which must answer `gone`; events in each mode the bridge offers — polling, and a
  push to a listener the command opens for the run, with a push secret it generates and prints for the bridge's
  configuration, or one it is given in a file; `checkChannel` of a channel given by `--forbidden-channel`, which must
  answer `forbidden`; and the optional operations the bridge declares. It prints one line per step — passed, failed with
  the reason, or skipped because the bridge does not offer it — and exits `1` when a step failed. Steps that need a
  person, such as a press, wait for one with a prompt, or are skipped with `--no-interactive`. It is the one sender
  outside delivery and the interactive path, an exception ADR-0017 allows because it runs outside any installation.
- **C-22.FR-22** The example bridge, in Go under the MIT licence, lives in the repository of the Go bridge library
  (FR-25) and is built on it — never in Muster's repository, binary, image or chart (ADR-0017): a small chat server with
  its own web page — channels, Threads, buttons, direct messages, users found by login — that offers every feature of
  the protocol, switchable one by one so that a bridge without buttons can be tried, and both event delivery modes. It
  passes `muster bridge check`. Its image `ghcr.io/muster-io/muster-example-bridge` is published for each commit on
  that repository's `master` and for each of its releases. Muster's end-to-end tests run a released image of it, pinned
  by digest and updated by Renovate, so that a change of Muster that breaks a released bridge library fails Muster's CI.
- **C-22.FR-23** The documentation covers: under Messengers, setting up a Bridge — the Connection with its token and
  event delivery mode (polling when the bridge cannot reach Muster, push with its callback address and secret
  otherwise), Destinations by channel id, what the Connection check shows, Text commands, Account links by token and
  by code, edit windows and republication, and Broken Destinations; and "Write a bridge" — the bridge libraries for Go
  and Python first, then the protocol step by step for a bridge written without them, what every bridge implements
  and what is optional, rendering the envelope and its CommonMark, Mention markers,
  idempotency keys, confirming presses within 3 seconds, `thread_ref`, error classes and who decides `gone`, safe
  truncation, both event delivery modes and their signatures, testing with `muster bridge check` and the fake bridge,
  the example bridge, and the licence.
- **C-22.FR-24** The Bridge stories register their log events — at least `bridge_press`, `bridge_press_dropped`,
  `bridge_text_command`, `bridge_event_rejected`, `bridge_event_failed`, `bridge_poll_failed` and
  `bridge_republished_past_edit_window` — and extend the metric catalogue: `bridge` joins the `transport` values of
  `muster_commands_total`; requests to bridges are counted in `muster_client_requests_total` by client class like every
  outbound request. Bridges add no Internal alert: their Destinations raise `MusterDestinationBroken`.
- **C-22.FR-25** Two bridge libraries, each in a repository of its own under the MIT licence and without a CLA, do the
  protocol's part of a bridge (ADR-0017): `muster-bridge-go` (the Go module `github.com/muster-io/muster-bridge-go`,
  package `bridge`) and `muster-bridge-python` (the PyPI package `muster-bridge`, module `muster_bridge`, for Python 3.11
  or newer, asynchronous, an ASGI application). A bridge author writes the calls to their messenger — posting, editing,
  replying in a Thread, checking a channel and answering a person, and where the messenger offers them finding a
  person, sending a direct message and deleting — and reports presses, Thread messages, deleted messages and direct
  messages to the library. The library serves every operation of `api/bridge.yaml` with its validation and limits,
  checks the token of each bot, keeps idempotency keys per token and replays their results, builds `info` from the
  features and limits it is given, answers problems with their classes, queues events for `pollEvents` and pushes them
  signed, with retries, and turns a direct message that carries an Account link token into an `account_link` event.
  Each library keeps a copy of `api/bridge.yaml`, generates its types from it and fails its CI when the copy differs
  from the one on Muster's `master`; its CI runs `muster bridge check` from Muster's development image against a bridge
  built on it. The libraries are `0.x` until Muster's first release, then `1.x` for protocol version 1; a Go release is
  a tag, a Python release is published to PyPI.

## UI

Connections → Bridge (create, edit, the event delivery mode with the push secret and the callback address for push,
the check with what the bridge offers, proxy form, delete); the Bridge Destination form with the channel id, the
check that shows the channel's name, Mention settings with nobody or chosen users, and limiters proposed from `info`;
"Past the edit window" in the delivery state; Profile → Messenger accounts → "Link through {Connection}" with the
token, the bridge's link and, where offered, the handle field labelled by its kind and the code entry; the labels of
Connections and Destinations across the UI (S-070) for the Bridge type, with the messenger's name from `info`.

## API surface

In `api/openapi.yaml`, added by the stories that implement them: `connections` of type `bridge` (list, create, read,
update, delete; reads carry the callback address and the last `info`); `connections/{id}/checks`; `destinations` of type
`bridge` (create, update); `destinations/{id}/checks` with the check item `channel`, `tests` and `previews` for the
type; `me/account-links/bridge` (`startBridgeLink` → token and link), `me/account-links/bridge/codes`
(`startBridgeCodeLink` → code sent) and `me/account-links/bridge/confirmation` (`confirmBridgeLink`, with the code);
`display_name` on Account links; the push callback `callbacks/bridge/{connection_id}` on the ingest listener; `bridge`
in the `Transport` and `Messenger` enums, `commonmark` in the template formats of `previewTemplate`, the flag
`past_edit_window` on Alert Group deliveries and the delivery event `republished_past_edit_window`. The protocol itself
is `api/bridge.yaml`.

## Acceptance

Checked against the fake bridge, unless an acceptance statement names the example bridge or a bridge library.

- **C-22.AC-1** The Connection check against the fake bridge shows its name, the messenger's name, `protocol_version`
  `1` and the features it declares; with the fake answering `protocol_version` `2` the check fails naming the version,
  and with a wrong token it fails with "The bridge refused the token."
- **C-22.AC-2** Saving a Destination for a channel the fake bridge answers `forbidden` for is refused with the bridge's
  `detail`, and the fake records no message; a channel it answers `200` for is saved with the channel's name.
- **C-22.AC-3** With an HTTP proxy on the Connection, every request reaches the fake bridge only through the fake
  proxy; a network error leaves the token and the push secret in no log line and no error shown in the UI.
- **C-22.AC-4** A new Alert Group posts one envelope with the status `firing`, its colour and emoji, the title `#N`,
  the link to the Alert Group page, a CommonMark body and the buttons of its status with their style hints;
  Acknowledge through the API edits it with the buttons of the acknowledged status; a new Alert creates a Thread reply
  with `thread_ref` `null`, and the next one carries the `thread_ref` the first returned.
- **C-22.AC-5** A label value `@all *x* <b>y</b> [z](http://evil)` reaches the fake bridge as literal text in the
  body: no emphasis, no link, no HTML and no Mention.
- **C-22.AC-6** When the fake bridge posts the first Publication but times out before answering, the retry carries the
  same `Idempotency-Key`, the fake holds one message, the answer is `replayed`, no possible duplicate is recorded, and
  an edit follows that brings the message to the current state — acknowledged, when Acknowledge came during the wait;
  with the fake's key store cleared before the retry, a second message appears and a possible duplicate is recorded.
- **C-22.AC-7** `rate_limited` with `Retry-After: 7` delays the next call to that Destination by 7 to 8 seconds and is
  not counted as an attempt, and with `scope` `bot` it delays every Destination of the Connection; `forbidden` makes the
  Destination Broken at once with the bridge's `detail` as the reason; `invalid` ends the delivery as Not delivered and
  leaves the Destination healthy; `gone` on an edit and `gone` on a Thread reply each publish the Alert Group again once,
  Quietly, with "The previous message was deleted at HH:MM".
- **C-22.AC-8** With the fake declaring `edit_max_age_seconds` 600, after the development clock moves 11 minutes past
  a Publication, Acknowledge publishes a new Root message Quietly, the old Thread gets "This message is no longer
  updated. The current one: {link}" with the new message's `url`, and the next Thread reply goes under the new Root
  message; a new Alert past the window adds a Thread reply and neither edits nor republishes.
- **C-22.AC-9** An edit refused with `gone` and the `reason` `edit_window_closed` inside the declared window is not the
  deleted Root message flow: the Root message is marked past its window, and the next change of status republishes it as
  in C-22.AC-8.
- **C-22.AC-10** With `max_body_length` 1,000, an Alert Group with 40 Alerts reaches the fake bridge with a body of at
  most 1,000 code points that keeps its link line, notices and footer, and its title, link and buttons in the envelope.
- **C-22.AC-11** With the fake declaring neither `buttons` nor `thread_messages`, a Root message carries no buttons and
  no commands line; with `thread_messages` and no `buttons` it ends with the commands line of its status.
- **C-22.AC-12** A test message reaches the fake bridge as one `postMessage` of the kind `test` with "🧪 Test message",
  and no Alert Group's Timeline changes; a press on its button is answered "This is a test message; nothing was
  changed".
- **C-22.AC-13** In poll mode, a press queued in the fake bridge is handled within one poll of the Leader and the
  cursor is stored; the same event returned again by an overlapping poll is not handled twice; with the fake failing
  polls, the poller backs off and no Destination becomes Broken.
- **C-22.AC-14** In push mode, an event with a valid signature is handled and answered `204`, and the same `webhook-id`
  again is answered `204` without being handled twice; a wrong signature, a `webhook-timestamp` 6 minutes old, an
  unknown Connection and a Connection in poll mode are each answered `401` and change nothing.
- **C-22.AC-15** A press from a linked account runs the Command as that User with the Transport `bridge`, recorded in
  the Audit log; a press with tampered data, or with data issued for another message, changes nothing and is answered
  with the refusal; a press from an account without an Account link changes nothing and is answered with the "not
  linked" text and the profile link; a press older than `bridge.event_max_age` changes nothing, is not answered and is
  logged as `bridge_press_dropped`; a press on a message of another Connection changes nothing and is not answered.
- **C-22.AC-16** In the Thread of a firing Alert Group, `ack` from a linked Responder acknowledges it and is answered
  "Done: Acknowledged #N"; `snooze 4h` on a Route with the default durations snoozes it for 4 hours and `snooze 2h` is
  answered with the Route's durations; a Russian synonym of `resolve` resolves it; `still on it` answers the open
  Reminder; a Viewer's `resolve` changes nothing and is answered with the refusal; "looking into it" changes nothing and
  is not answered.
- **C-22.AC-17** A `message_deleted` event for the Root message of an open Alert Group publishes it again at once,
  Quietly, with the deletion notice, before any edit is due; a second deletion marks the pair "deleted in the messenger".
- **C-22.AC-18** A profile token sent to the fake bot links the sender's account, which is answered "Linked to Muster
  user {name} ({login})"; the same token again links nothing, is answered "This link is invalid or expired. Start
  again from your Muster profile." and is recorded as `link_rejected`, and so is a token of another Connection; with
  `link_url_template` in `info`, the profile offers the link built from it.
- **C-22.AC-19** With `user_lookup` `email`, a code request for a known address sends a direct message with the code
  through the fake bridge, the right code links the account, and after five wrong codes the sixth attempt is refused
  even with the right code; a bridge with `user_lookup` `none` or without `direct_messages` offers no code, and the code
  request is refused.
- **C-22.AC-20** A Destination that mentions Alice for new Alert Groups posts a Loud Root message whose `mentions` hold
  the `external_id` and `display_name` of her link and whose body starts with her marker; without a link she is named as
  text with no marker; a Bridge Destination whose Mention settings name everyone in the chat is refused.
- **C-22.AC-21** The Bridge Connection page shows the check with what the bridge offers, the push secret field and the
  callback address only in push mode; the Bridge Destination form saves a channel id and shows the channel's name from
  its check; the profile links an account through a Bridge Connection by token and, where offered, by a code with the
  handle field labelled as the bridge declares — all in English and Russian, without horizontal scrolling at 360 px wide.
- **C-22.AC-22** `muster bridge check` passes every step against the fake bridge with all its features and skips the
  optional ones when they are switched off, and does the same against the released image of the example bridge;
  against the fake bridge with replays turned off it fails the idempotency step and exits `1`.
- **C-22.AC-23** On `muster dev` with a Bridge Connection to the released image of the example bridge, a new Alert
  Group appears on the example's web page; pressing Ack there acknowledges it in Muster; with the example's buttons
  switched off, `ack` written in the Thread acknowledges it.
- **C-22.AC-24** The documentation site has the Bridge and "Write a bridge" pages and the protocol reference rendered
  from `api/bridge.yaml`, linked from the navigation, and `make docs-check` passes.
- **C-22.AC-25** In each library's repository, a bridge built on the library — the example bridge in Go, the library's
  small in-memory example in Python, installed with `pip install` from the built wheel — passes every step of
  `muster bridge check` run from `ghcr.io/muster-io/muster:edge` with all its features, and skips the optional ones when
  it offers only `thread_messages`; a copy of `api/bridge.yaml` that differs by one byte from Muster's fails the
  library's CI.

## Related ADRs

ADR-0001, ADR-0005, ADR-0007, ADR-0011, ADR-0013, ADR-0015, ADR-0016, ADR-0017.

## Depends on

C-11 — delivery; C-12 — rendering, templates and Mention settings; C-13 — the shared Destination pages; C-16 — the
Destination test; C-17 — Reminder buttons and "Still on it"; C-18 — Account links. C-22 was added after the other
capabilities: a team whose messenger only a bridge reaches needs it from the Shadow stage on, and it builds on the
timers and Account links of the Act stage.

## Suggested story split

- **BE** — Connections, the protocol client with its error classes, `info`, the fake bridge and `muster dev`.
- **BE** — Destinations, the Destination check, `muster doctor` and the Mention settings of Bridges.
- **BE** — Bridge delivery: envelopes and CommonMark, buttons, idempotent Publications, edits, Thread replies, error
  classes, test messages, the Broken probe; the Messengers → Bridge page.
- **BE** — edit windows: republication on a change of status, the pointer reply, the final edit as a Thread reply.
- **BE** — events by polling and push, presses and answers, Text commands, deleted messages.
- **BE** — Account links by token and by code, Mentions of linked people.
- **FE** — the Bridge Connection and Destination pages, Account links in the profile, labels.
- **BE** — `muster bridge check`, checked against the fake bridge.
- **BE** — the Go bridge library, in its own repository.
- **BE** — the example bridge on the Go library, in the same repository.
- **Infra** — Muster's end-to-end test against the example's released image.
- **Docs** — "Write a bridge" and the protocol reference on the documentation site.
- **BE** — the Python bridge library, in its own repository.
