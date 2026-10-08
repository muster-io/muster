# C-13. Mattermost

[L1 index](../L1.md) · Stage: Shadow · UI: yes · Depends on: C-11, C-12

**Goal.** Deliver Alert Groups to Mattermost channels as editable posts with threads and working buttons, through a bot
account and the REST API. As the first Destination type with a UI, this capability also builds the shared Destination
pages.

Names in the form `area.setting` refer to rows of [defaults.md](defaults.md); ids in the form `F-NNN` refer to
[verified facts](../../facts.md#mattermost).

## Scenarios

1. The Admin creates a Mattermost Connection with the server address and a bot token; the connection check shows the
   bot's name.
2. The Admin creates a Destination for the channel "alerts-prod" through that Connection and adds it to a Route.
3. Acknowledge in the Muster UI edits the post in place. A press of Ack in Mattermost from an account that is not
   linked yet gets a private message in the channel, visible only to that person, with a link to the profile.
4. Muster runs on an internal address. The Connection page and the documentation explain the Mattermost setting that
   lets the server call it, and a test message, whose button Muster presses itself through the bot, shows whether it
   works.
5. The Mattermost server is reachable only through an HTTP proxy, set on the Connection.
6. The bot is removed from "alerts-prod" at night and the Destination becomes Broken. In the morning someone adds the
   bot back; within minutes the Destination check succeeds and the Destination is healthy, though no alert has fired.
7. With the first Destination in place, the Routes page offers a Route that sends Muster's Internal alerts there.

## Functional requirements

- **C-13.FR-1** A Mattermost Connection has a name, the server URL, a bot token (encrypted, write-only), a proxy
  (C-03.FR-19) and its limiter (`connection.mattermost.limiter`). Muster uses only the REST API with the bot account —
  no incoming webhooks, no plugin.
- **C-13.FR-2** The connection check (interactive) verifies the token and shows the bot's name; saving a Destination
  verifies that the bot is a member of the channel and refuses otherwise, naming the failing check. When the bot's
  roles do not grant `create_post_ephemeral` — read with `POST /api/v4/roles/names`, as the server decides it (F-064)
  — the check still passes, with the warning `press_answers_in_thread`: the bot may not make ephemeral messages, so
  answers to button presses will show in the Thread of the Root message, and granting the bot that permission, for
  example the system admin role, shows them in the channel (FR-4, F-063). `muster doctor` prints the same hint as a
  WARN line.
- **C-13.FR-3** A Mattermost Destination has a Connection, a team and channel, its Mention settings (C-12.FR-8) and its
  limiter (`destination.mattermost.limiter`). The Root message is a post whose `message` is a short summary line — the
  status emoji, `#N` and the Alert Group's title — with one attachment, coloured by status, that holds the details of
  C-12.FR-1: the attachment's title is `#N` and the Alert Group's title, linked (`title_link`) to the Alert Group page;
  its text is the body, and under the Alerts one line of links starts with "Open in Muster" and goes on with the links
  of C-12.FR-9, such as the runbook when there is one; the footer of C-12.FR-1 (item 11) is the last line of the text;
  the attachment's own `footer` is "Muster v<version>", with `footer_icon` set to the Muster mark that the server
  serves at `<MUSTER_PUBLIC_URL>/muster-mark-256.png`; and the buttons of
  [reference.md](reference.md#buttons-and-links-by-status) follow. The Mentions of a Loud post go into its `message`,
  after the summary line, never into the attachment: a Mention inside an attachment notifies, but the notification
  shows nothing when `message` is empty (F-056). An edit rewrites the summary line and the attachment; like every
  edit it is Quiet and carries no Mention (F-029). Every link, "Open in Muster" included, is a link in the post, never
  a button: the answer to a press cannot open a URL (F-024). The Thread is the post's replies: Muster posts them with
  the Root message as `root_id`, with their text and Mentions in `message`, so they show in the Thread and raise its
  reply count (F-027). A post's `message` and its attachment text each stay within the server's post length limit of
  16,383 characters (F-057): the adapter declares that limit, so a longer Root message is shortened as C-12.FR-11 says —
  its Alert list is cut and ends with "…and N more" — and the link to Muster always stays.
- **C-13.FR-4** Each button carries only an opaque action id signed with the button-signature key and its key id. A
  press arrives at `MUSTER_INGEST_URL` as a request from the Mattermost server (F-023); Muster verifies the signature
  with the key it names, checks that the press's `post_id` and `channel_id` belong to the Root message or Thread reply
  that the action id was issued for (a mismatch is refused like a bad signature), maps the Mattermost `user_id` to a
  User through its Account link for this Connection (C-18), and dispatches the command with the Transport `mattermost`.
  The callback is `POST /api/v1/callbacks/mattermost/{connection_id}` on the ingest listener; every request is answered
  `200` with a JSON object — also one whose body is not JSON and one for a Connection that does not exist, which get an
  empty object, so the endpoint reveals nothing about which Connections do. The answer never carries `update`, because
  only the delivery worker edits the Root message (C-11.FR-2). Muster answers once the command is dispatched, well
  within the 30 s that Mattermost waits by default (F-032). What the person who pressed must read — a refusal, a
  failure, or for an account without an Account link "Your Mattermost account is not linked to Muster. Link it in your
  profile: {link}" — reaches that person alone in one of two ways (D284). First Muster sends a separate ephemeral post
  (`POST /api/v4/posts/ephemeral`) through the interactive path, within `delivery.interactive_budget`: without
  `root_id` for a press on a Root message, so that it shows at once in the channel view (F-026), and with the Root
  message as `root_id` for a press on a Thread reply, such as a Reminder (C-17.FR-4), whose buttons the server accepts
  like those of a Root message (F-055); the answer is then empty. That call needs the `create_post_ephemeral`
  permission, which a bot with the role Member lacks (F-063). When it is refused with `403` for that permission —
  expected, so not logged as an error — or fails in any other way (a timeout, a `5xx`, no limiter token within the
  budget), the same text goes into the answer's `ephemeral_text`, with `skip_slack_parsing`, which needs no permission
  and which Mattermost shows to that person, from "System", in the Thread of the Root message (F-025, F-062): with
  collapsed reply threads, the default, it is seen only once the Thread is open. So the answer is never lost, and a
  bot with the permission shows it in the channel. A successful press gets an empty answer: the edited Root message
  shows its result. A press from an account without an Account link changes nothing.
- **C-13.FR-5** Response mapping: `429` → RetryAfter for the whole Connection, for the seconds in `Retry-After`, because
  Mattermost counts requests per client address, not per channel (F-030); the body of that `429` is plain text, not
  JSON, so it is classified by its status and headers alone (F-031); `5xx` and timeouts → Transient; `401` and `403`
  (invalid token, bot removed from the channel) and `404` for the channel (channel not found or archived) → Fatal;
  anything else → unknown. A deleted Root message is not answered with `404`: an edit of it is refused with `403` and a
  reply under it with `400` `api.post.create_post.root_id.app_error` (F-058). A reply refused with that `400` means at
  once that the Root message was deleted. When an edit is refused with `403`, Muster reads the post with a plain
  `GET /api/v4/posts/{id}`: a `404` means that the Root message was deleted; a `200` means the `403` is a real
  permission error and stays Fatal; any other answer of the read is classified by this mapping. A deleted Root message
  starts the deleted Root message flow (C-11.FR-13) instead of Fatal or unknown. Muster does not read deleted posts with
  `include_deleted=true`, which may need the system admin role (see [Pending](../../facts.md#pending)).
- **C-13.FR-6** A Connection used by Destinations cannot be deleted; a deleted Destination does not count as using it
  (C-11.FR-14). Deleting the Connection abandons the final edits still pending for its deleted Destinations, which end
  as Not delivered.
- **C-13.FR-7** The documentation covers: creating the bot account and its permissions — the role Member is enough,
  and answers to presses then show in the Thread; `create_post_ephemeral`, for example through the system admin role,
  shows them in the channel instead (FR-4, F-063); allowing the bot to send direct
  messages, which Account links need (F-028); adding the host of `MUSTER_INGEST_URL` to
  `ServiceSettings.AllowedUntrustedInternalConnections` when it is an internal address — otherwise a press shows the
  person only a generic "Action integration error", and only the Mattermost server log says why (F-022) — and that a
  test message of a Destination checks it (C-16.FR-3); Mattermost's
  rate limit, which is off by default and, when an admin turns it on, allows 10 requests per second per client address
  — a bucket that all Connections of one Muster installation to that server usually share, so their limiters together
  must stay below it (F-030); keeping `PostEditTimeLimit` unlimited, its default, because Muster edits a Root message
  for as long as its Alert Group is open (F-032; what Muster does when an edit is refused is
  [L1 open question 12](../L1.md#52-other-questions)); and that edits notify nobody (F-029), while Thread replies notify
  the Thread's followers according to their own settings and a Mention makes the person a follower (F-027), so Quiet
  means "without a Mention".
- **C-13.FR-8** The adapter escapes alert data for Mattermost Markdown (C-12.FR-7). A Loud event is a new post, because
  an edit notifies nobody, even one that adds a Mention (F-029). A Quiet new message carries no Mention; a Loud one
  carries the Mentions the event asks for, in the post's `message` (FR-3, F-056).
- **C-13.FR-9** The shared Destination pages, reused by C-14 and C-15: the Destinations list (all types, with health,
  Routes and the Broken banner), the Destination page with its health, its Routes, the Mention and limiter sections and,
  for types that have a Destination check, "Check"; and the delivery state per Destination on the Alert Group page
  (C-11.FR-16).
- **C-13.FR-10** The Destination check of a Mattermost Destination verifies that the Connection's token works and that
  the bot is a member of the channel. It runs when the Destination is saved (FR-2) and from "Check" on the Destination
  page — both through the interactive path, because a person waits for them — as the Broken probe when nothing is
  waiting (C-11.FR-9), in the delivery client class and never on the interactive path, and in `muster doctor`
  (C-02.FR-14); a successful check of a Broken Destination ends the Broken state.
- **C-13.FR-11** With the first Destination type, the Routes page offers the Route for Internal alerts of C-08.FR-11;
  its Destination picker lists Destinations of every type.
- **C-13.FR-12** The Alert Group list (C-09.FR-13) gains the filter "Delivery problem" — any Destination Not delivered,
  waiting for a Broken Destination, deleted in the messenger or with a Thread not attached — and a mark on such rows.
- **C-13.FR-13** The Mattermost Connection page shows the address that Mattermost calls for button presses — the
  callback of FR-4 at `MUSTER_INGEST_URL`, which the Connection returns as a read-only field — with the hint of
  [reference.md](reference.md#banners-warnings-and-notices) about `AllowedUntrustedInternalConnections`. Muster cannot
  read that setting through the bot; instead, the test message of a Mattermost Destination presses its own button
  through the bot and reports whether the press reached Muster, naming the setting when it did not (C-16.FR-3, F-022,
  F-054).

## UI

Connections → Mattermost (create, edit, check, delete, proxy form, the callback address with its hint); the shared
Destinations list and Destination page with "Check"; the Mattermost Destination form with team and channel pickers
loaded through the bot; the delivery state section of the Alert Group page; the "Delivery problem" filter and mark in
the Alert Group list; the Internal alerts Route suggestion.

## API surface

`connections` of type Mattermost (list, create, read, update, delete; reads carry the callback address);
`connections/{id}/checks` (create → result);
`connections/{id}/channels` (list channels visible to the bot); `destinations` of type Mattermost (create, update);
`destinations/{id}/checks` (create → result, for types with a Destination check); the `delivery_problem` filter on `alert-groups`; the action callback endpoint
`callbacks/mattermost/{connection_id}` on the ingest listener.

## Acceptance

Checked against the fake Mattermost server.

- **C-13.AC-1** A new Alert Group creates a post with buttons; Acknowledge through the API edits it in place; a new
  Alert creates a reply in its thread.
- **C-13.AC-2** A callback with a tampered action id is rejected and answered with an ephemeral error.
- **C-13.AC-3** A `403` from the fake server marks the Destination Broken, raises `MusterDestinationBroken` and shows the
  Broken banner on the Destination and its Routes.
- **C-13.AC-4** A press from a Mattermost account without an Account link changes nothing and gets the ephemeral "not
  linked" reply with the profile link.
- **C-13.AC-5** A label value `@channel <b>x</b>` arrives as literal text that mentions nobody.
- **C-13.AC-6** A Quiet Thread reply carries no Mention; a Loud one carries the Mention configured for its event in
  its `message`, and a Loud Root message carries it in its `message` after the summary line, not in the attachment.
- **C-13.AC-7** With an HTTP proxy on the Connection, every request reaches the fake server only through the fake
  proxy.
- **C-13.AC-8** A new Alert Group meets a `403` because the bot was removed from the channel, and resolves while the
  Destination is Broken, so nothing is waiting. After the bot is added back in the fake server, the next probe — the
  Destination check — ends the Broken state and resolves `MusterDestinationBroken` with no new Alert Group, and its
  requests are counted in `muster_client_requests_total` under the delivery client class, not the interactive one;
  "Check" on the Destination page does the same at once, through the interactive path.
- **C-13.AC-9** With a Mattermost Destination and no Route other than the Default route for `alertname=~"Muster.*"`, the
  Routes page offers the Internal alerts Route; accepting it with that Destination creates the Route at the top of the
  list with that Destination.
- **C-13.AC-10** An Alert Group whose delivery ended as Not delivered is returned by the "Delivery problem" filter and
  marked in the list; after a later successful delivery it is not.
- **C-13.AC-11** A press whose signed action id belongs to one post but arrives with the `post_id` of another is refused
  and changes nothing.
- **C-13.AC-12** A `404` naming the channel makes the Destination Broken; an edit refused with `403` because the Root
  message was deleted — which the plain read `GET /api/v4/posts/{id}` shows by answering `404` — starts the deleted Root
  message flow instead and leaves the Destination healthy, and so does a reply refused with `400` for its `root_id`; an
  edit refused with `403` while that read answers `200` makes the Destination Broken.
- **C-13.AC-13** A new Alert Group creates a post whose `message` is the summary line and which has one attachment
  whose title links to the Alert Group page, whose text has a line of links under the Alerts that starts with "Open in
  Muster", and whose `footer` is "Muster v<version>"; every link is in the attachment, none is a button.
- **C-13.AC-14** A press on a Root message is answered `200` with a JSON object that never carries `update`. When the
  press is refused and the bot has the system admin role in the fake server, the person gets a separate ephemeral post
  in the channel, without `root_id`, and the answer is empty; with the role Member, the fake server refuses that post
  with `403`, like a real one, and the answer carries the same text in `ephemeral_text`. A failed ephemeral post of an
  admin bot falls back to `ephemeral_text` too. A press that ran its command gets an empty answer.
- **C-13.AC-15** A `429` from the fake server with a plain-text body and `Retry-After: 1` delays the next request
  through that Connection, to any of its Destinations, by 1 to 2 seconds and is not counted as an attempt.
- **C-13.AC-16** The connection check of a bot whose roles do not grant `create_post_ephemeral` passes with the warning
  `press_answers_in_thread`, and `muster doctor` prints it as a WARN line; with the system admin role there is no
  warning.

## Related ADRs

ADR-0005, ADR-0011, ADR-0013, ADR-0015.

## Depends on

C-11 — delivery; C-12 — rendering and Mention settings.

## Suggested story split

- **BE** — Connection, adapter (posts, threads, buttons, callbacks, ephemeral answers, error mapping, escaping,
  Destination check), channel listing, the "Delivery problem" filter, fake server extension.
- **FE** — Connection pages with the callback address and its hint, the shared Destination pages with "Check", the
  Mattermost Destination form, the delivery state section, the "Delivery problem" filter, the Internal alerts Route
  suggestion.
