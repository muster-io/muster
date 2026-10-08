---
id: S-061
title: "Mattermost adapter: posts, Threads, buttons, callbacks, the Delivery problem filter and the Internal alerts suggestion (BE)"
capability: C-13
kind: be
layer: L1
depends_on: [S-039]
covers: [C-13.FR-2, C-13.FR-3, C-13.FR-4, C-13.FR-5, C-13.FR-7, C-13.FR-8, C-13.FR-10, C-13.FR-11, C-13.FR-12, C-13.AC-1, C-13.AC-2, C-13.AC-3, C-13.AC-4, C-13.AC-5, C-13.AC-6, C-13.AC-8, C-13.AC-9, C-13.AC-10, C-13.AC-11, C-13.AC-12, C-13.AC-13, C-13.AC-14, C-13.AC-15, C-13.AC-16, C-11.AC-1, C-11.AC-5, C-11.AC-7, C-12.AC-3, C-11.FR-2, C-11.FR-7, C-11.FR-9, C-12.FR-1, C-12.FR-7, C-12.FR-8, C-12.FR-11, C-10.FR-3, C-10.FR-6, C-10.FR-11, C-09.FR-13, C-08.FR-11, C-02.FR-14, C-01.FR-7, C-11.FR-8]
files_touched:
  - .github/workflows/nightly.yml
  - api/openapi.yaml
  - design/facts.md
  - design/prd/l1/C-08-routing.md
  - design/prd/l1/C-13-mattermost.md
  - docs/messengers/mattermost.md
  - internal/accountlinks/live_test.go
  - internal/accountlinks/lookup.go
  - internal/accountlinks/lookup_test.go
  - internal/accountlinks/query.sql
  - internal/api/alertgroups.go
  - internal/api/alertgroups_test.go
  - internal/api/connections.go
  - internal/api/connections_test.go
  - internal/archlint/secretleak.go
  - internal/connections/connections.go
  - internal/connections/connections_test.go
  - internal/connections/doctor.go
  - internal/connections/doctor_test.go
  - internal/connections/live_test.go
  - internal/connections/query.sql
  - internal/delivery/delivery.go
  - internal/delivery/interactive.go
  - internal/delivery/live_test.go
  - internal/delivery/presses.go
  - internal/delivery/presses_test.go
  - internal/delivery/query.sql
  - internal/delivery/worker_test.go
  - internal/doctor/doctor.go
  - internal/doctor/doctor_integration_test.go
  - internal/doctor/doctor_test.go
  - internal/fakes/fakemattermost/fakemattermost.go
  - internal/fakes/fakemattermost/fakemattermost_test.go
  - internal/fakes/fakemattermost/posts.go
  - internal/groups/counts.go
  - internal/groups/dispatcher_test.go
  - internal/groups/filters.go
  - internal/groups/groups_integration_test.go
  - internal/groups/list.go
  - internal/groups/list_test.go
  - internal/groups/query.sql
  - internal/groups/read.go
  - internal/logging/events.go
  - internal/mattermost/adapter.go
  - internal/mattermost/adapter_test.go
  - internal/mattermost/callback.go
  - internal/mattermost/callback_test.go
  - internal/mattermost/check_test.go
  - internal/mattermost/classify.go
  - internal/mattermost/client.go
  - internal/mattermost/export_test.go
  - internal/mattermost/layout.go
  - internal/mattermost/layout_test.go
  - internal/messages/texts/en.json
  - internal/messages/texts/ru.json
  - internal/routing/evaluate.go
  - internal/routing/query.sql
  - internal/routing/routes.go
  - internal/routing/routes_test.go
  - internal/routing/routing_integration_test.go
  - internal/routing/suggestions.go
  - internal/routing/suggestions_test.go
  - internal/runtime/runtime.go
  - internal/runtime/runtime_test.go
  - internal/server/server.go
  - internal/server/server_test.go
  - sqlc.yaml
  - test/e2e/alert_group_list_test.go
  - test/e2e/harness.go
  - test/e2e/mattermost_test.go
  - test/load/main.go
acceptance:
  - "[C-13.AC-13, C-13.FR-3, C-12.FR-1, C-12.FR-11] A new Alert Group creates one post whose `message` is the summary line — the status emoji, `#N` and the title — with one attachment coloured by status, whose title `#N` and the title links to the Alert Group page, whose text has the line of links under the Alerts starting with \"Open in Muster\" and ends with the footer line, whose `footer` is \"Muster v<version>\" with the logo URL as `footer_icon`, and whose buttons follow; every link is in the attachment, none is a button; the post of an Alert Group of 600 Alerts keeps its `message` and its attachment text within 16,383 characters, its Alert list is cut to the distinct values of S-036, each label ending with \"+N more\", with \"Full list in Muster\", and the link to Muster stays."
  - "[C-13.AC-1] Acknowledge through the API edits that post in place to the acknowledged colour, footer and buttons; a new Alert creates a reply with the post as `root_id`, which raises its reply count."
  - "[C-13.AC-5, C-13.FR-8, C-12.FR-7] A label value `@channel <b>x</b>` arrives as literal text and the fake server records no notification for it."
  - "[C-13.AC-6, C-13.FR-8, C-11.FR-7, C-12.FR-8] A Quiet Thread reply carries no Mention; a Loud one carries the Mention configured for its event — `@channel` for `new_alerts` — in the post's message, and the fake server records the notification; a Loud Root message carries its Mentions in its `message` after the summary line, never in the attachment; an edit never notifies."
  - "[C-13.AC-14, C-13.FR-4, C-11.FR-2] A press on a Root message is answered 200 with a JSON object that never carries `update`, `{}` when the Command ran. A refused press first gets one ephemeral post through `POST /api/v4/posts/ephemeral` in the channel, without `root_id`, through the interactive path: with `bot_system_admin` on the fake server it is made and the answer is `{}`; by default the fake refuses it with `403` `api.context.permissions.app_error` (F-063) and the answer is `{\"ephemeral_text\": <text>, \"skip_slack_parsing\": true}`, which the fake shows to the person alone, from System, in the Thread of the Root message (F-025, F-062); a failed post of an admin bot — `5xx`, no limiter token, slower than `delivery.interactive_budget` — falls back the same way (D284)."
  - "[C-13.AC-4, C-10.FR-11] A press from a Mattermost account without an Account link changes nothing and gets the ephemeral \"Your Mattermost account is not linked to Muster. Link it in your profile: {link}\", as a post in the channel or as the answer's `ephemeral_text`."
  - "[C-13.FR-4, C-10.FR-3, C-10.FR-6] A press from an account linked to a Responder (link rows set up directly; S-051 creates them) runs Acknowledge, Resolve or a Snooze for the pressed duration as that User with the Transport `mattermost`; the delivery worker, not the answer, edits the post."
  - "[C-13.AC-16, C-13.FR-2] The Connection check of a bot whose roles, read with `POST /api/v4/roles/names`, do not grant `create_post_ephemeral` passes with the warning `press_answers_in_thread`, and `muster doctor` prints \"WARN connection <name>: \" and the hint; with `bot_system_admin` on the fake server there is no warning (F-064)."
  - "[C-13.AC-2] A callback whose action id was changed changes nothing and gets the ephemeral \"This button could not be verified; nothing was changed.\"; a callback for a Connection that does not exist, and one whose body is not JSON or does not match `MattermostActionRequest`, is answered `200` `{}` the same way by the callback handler on the ingest listener."
  - "[C-13.AC-11] A press whose signed action id belongs to one post but arrives with the `post_id` of another is refused and changes nothing."
  - "[C-13.AC-3, C-11.FR-9] A `403` on a post makes the Destination Broken, raises `MusterDestinationBroken` and shows the Broken health on the Destination and in its Routes' `destinations`."
  - "[C-13.AC-8, C-13.FR-10] With the bot removed from the channel, a new Alert Group meets a `403` and resolves while the Destination is Broken; after the bot is added back the next probe — the Destination check, as the adapter's `Check` — ends the Broken state and resolves `MusterDestinationBroken` with no new Alert Group, its requests counted under `client=\"delivery\"`; `checkDestination` does the same at once under `client=\"interactive\"`."
  - "[C-13.AC-12, C-13.FR-5] A `404` naming the channel makes the Destination Broken; an edit refused with `403` for a deleted Root message — confirmed by the plain read `GET /api/v4/posts/{id}` answering `404` — republishes the Root message once with \"The previous message was deleted at HH:MM\" and leaves the Destination healthy, and a reply refused with `400` `api.post.create_post.root_id.app_error` does the same without the read; an edit refused with `403` while the read answers `200` makes the Destination Broken."
  - "[C-13.AC-15, C-13.FR-5, C-11.FR-8] A `429` with a plain-text body and `Retry-After: 1` delays the next request through that Connection, to any of its Destinations, by 1 to 2 seconds and is not counted as an attempt."
  - "[C-13.FR-11, C-13.AC-9, C-08.FR-11] With a Mattermost Destination and no Route but the Default one for `alertname=~\"Muster.*\"`, `listRouteSuggestions` offers `internal_alerts`; accepting it with that Destination creates the Route at the top of the list — directly below a Route that already takes an Internal alert, when one does (D268) — with that Destination."
  - "[C-13.FR-12, C-13.AC-10, C-09.FR-13] An Alert Group whose delivery ended as Not delivered is returned by `listAlertGroups?delivery_problem=true` with `delivery_problem: true`; after a later successful delivery it is not."
  - "[C-11.AC-1, C-11.AC-5, C-11.AC-7, C-12.AC-3] Against the fake server: ten Alerts and an Acknowledge within 2 seconds give one post and at most one edit; stopping Muster — a `muster dev --replica` process of the harness mode with the fakes in the test process — while the server holds the answer to a new post gives a second post and a \"possible duplicate\" entry after the restart; recovery after a Broken period publishes, edits and withholds as C-11.AC-7 says; a failing Route template produces the Fallback template in the post."
  - "[C-02.FR-14] `muster doctor` prints one line per Mattermost Connection check and per Mattermost Destination check."
  - "[C-13.FR-7] The documentation covers the bot account and its permissions, direct messages, `AllowedUntrustedInternalConnections` with the \"Action integration error\" symptom, the rate limit, `PostEditTimeLimit`, and that edits notify nobody while a Mention makes a person follow the Thread."
  - "[C-01.FR-7] The load test signs in with the bootstrap Admin and creates its own Personal access token, Integration, Route and Destination; with `-destination=none` it creates no Destination, which the nightly run against the compose example uses."
  - "[C-01.FR-7] The nightly load test against `muster dev` brings 10,000 Alerts into 1,000 open Alert Groups with 50 webhooks per second for one minute, delivered to the fake Mattermost server, and fails when a webhook is rejected or lost, when ingestion's 99th percentile exceeds 1 s (P-44) or when the delivery latency's 95th percentile exceeds 5 s (NFR-2)."
verify: "make ci test-integration e2e"
operator_attention: true
issue: 61
---

# S-061. Mattermost adapter: posts, Threads, buttons, callbacks, the Delivery problem filter and the Internal alerts suggestion (BE)

## Scope

**IN**

- The Mattermost adapter of the delivery engine: the Root message layout, edits, Thread replies, Quiet and Loud posts,
  escaping, the response mapping, and the Destination check of S-039 as its `Check` for the Broken probe.
- Button presses: the callback handler on the ingest listener, signatures, the message binding, Account link lookup,
  dispatching Commands with the Transport `mattermost`, and the answers to the person: an ephemeral post through the
  interactive path, or the `ephemeral_text` of the callback's answer when that post is not made (D284); the handler
  answers `200` with a JSON object for every body, one that does not decode included.
- The warning of the Connection check and `muster doctor` when the bot may not make ephemeral posts.
- The "Delivery problem" filter, the `internal_alerts` Route suggestion, `muster doctor` checks, the documentation.
- The full profile of the load test and its thresholds (NFR-1, NFR-2, P-44), now that delivery reaches a messenger.

**OUT**

- Connections, Destinations, the Destination check on save and through the API, and the fake Mattermost server
  (S-039).
- The pages (S-040 for Connections, S-064 for Destinations and delivery state); the Telegram type (S-041, S-042);
  outgoing webhooks (S-044, S-045).
- The test message and the bot's own press (S-047); Reminders and their buttons on Thread replies (S-049); creating
  Account links, with which S-051 verifies the answers to Viewers and disabled Users.

## Contracts

- **Operations implemented**: `mattermostAction` (ingest listener, served by the callback handler below, not by the
  API server); `listAlertGroups` and `getAlertGroupCounts` accept
  `delivery_problem` and `AlertGroup.delivery_problem` is filled (the `422 unsupported` of S-029 is removed, and its
  tests in `list_test.go` and `alertgroups_test.go` change with it); `listRouteSuggestions` and `acceptRouteSuggestion`
  handle `internal_alerts`. Schemas: `MattermostActionRequest`, `MattermostActionContext`, `MattermostActionAnswer`
  (empty, or `ephemeral_text` with `skip_slack_parsing`).
- **Adapter** (C-11.FR-7, C-11.FR-9; `adapter.go`): the adapter of S-034 for the type `mattermost`, built on the REST
  client of S-039 and called in the **delivery** client class: `Publish`, `Update` and `Reply` from the delivery
  worker, and `Check` — the Destination check of S-039 — from the Broken probe of S-035, never on the interactive path.
- **Root message layout** (C-12.FR-1, C-13.FR-3; `layout.go`, Markdown): `POST /api/v4/posts` with `channel_id`,
  `message` — the summary line: the status emoji, `#N` and the title, escaped like alert data, followed on a Loud post by
  its Mentions — and `props.attachments` of exactly one attachment: `color` by status (firing `#d32f2f`, acknowledged
  `#f57c00`, resolved `#388e3c`, snoozed `#9e9e9e`), `title` `#N` and the title, `title_link` the Alert Group page,
  `text` the sections 3 to 8 of S-036, then one line of links starting with "[Open in Muster](…)" followed by the links
  of S-037, then the notices, then the footer line as the last line; `footer` "Muster v<version>", `footer_icon`
  `<MUSTER_PUBLIC_URL>/muster-mark-256.png` (the mark in `web/public/`); `actions` the buttons of the status, each `type: button`
  with a stable `id` (`ack`, `unack`, `resolve`, `unsnooze`, `snooze0`…) and `integration` `{url:
  <MUSTER_INGEST_URL>/api/v1/callbacks/mattermost/<connection public_id>, context: {action, key_id}}`. Mentions never
  go into the attachment: there they would notify with an empty notification (F-056). A trusted Mention token of a
  Route template shows in the attachment as text that notifies nobody, and on a Loud Publication its Mention joins the
  others in `message`; the Owner token stays text, since the message carries no username of the Owner. The adapter declares `Markup()`
  `markdown` and `LengthLimit()` 16,383 characters, the server's `MaxPostSize` (F-057), which the layout applies to the
  `message` and to the attachment text; a longer Root message is shortened by S-036 — its Alert list is cut and ends
  with the distinct values of the labels that differ, each ending with "+N more", and "Full list in Muster" — and
  the link to Muster stays.
- **Edits and replies** (C-13.FR-3, FR-8): an update is `PUT /api/v4/posts/{id}/patch` with `message` the current
  summary line, without Mentions, and the new `props`; a Thread reply is `POST /api/v4/posts` with `root_id` the Root
  message and the reply text (S-036) in `message`. A Quiet new message carries no Mention; a Loud one carries the
  resolved targets of S-037 — `@channel`, `@all`, `@here`, `@<group>`, or `@<username>` of a user's Account link in
  `mattermost:<connection>` (otherwise the display name as plain text). Edits are always Quiet.
- **Escaping** (C-12.FR-7, C-13.FR-8): alert data are escaped for Mattermost Markdown with the escaper of S-036 — the
  Markdown control characters backslash-escaped, `<` and `>` among them, so that an HTML tag arrives as `\<b\>` and
  Mattermost shows it as the literal text — and `@` is neutralized, so `@channel <b>x</b>` shows as literal text and
  notifies nobody; trusted Mention tokens become real Mentions only in the `message` of a Loud post.
- **Response mapping** (C-13.FR-5, C-11.FR-8; `classify.go`): `429` → `retry_after` scoped to the Connection for the
  seconds of `Retry-After`, classified by status and headers alone because the body is `text/plain` (F-031); `5xx`,
  timeouts and network errors → `transient`; `401`, `403` and a `404` on creating a post (the channel is unknown or
  archived) → `fatal`; a `404` on an edit → `gone`; anything else → `unknown`. The provider's `message` and `id` are kept
  as the masked, untrusted error text. A deleted Root message answers an edit with `403`
  `api.context.permissions.app_error` and a reply with `400` `api.post.create_post.root_id.app_error`, not `404`
  (F-058). A reply refused with that `400` is `gone` at once (the deleted Root message flow of S-035, the Destination
  stays healthy). An edit refused with `403` is followed, before classifying, by the plain read
  `GET /api/v4/posts/{root id}` in the same client class: `404` makes the outcome `gone`; `200` keeps the `403` `fatal`,
  a real permission error; any other answer of the read is classified by the mapping above. The adapter never reads
  with `include_deleted=true`, which may need the system admin role (F-058).
- **Callback seam** (`internal/server/server.go`, `internal/runtime/runtime.go`): `/api/v1/callbacks/*` is already an
  ingest path (`server.IsIngestPath`), but `server.Ingest` hands everything except the Heartbeat to the ingest handler,
  which answers `404` for any path but ingestion (`internal/ingest/handler.go`), and the API server mounts only the
  operations with `x-listener: app` (`internal/api/middleware.go`), so no strict-server handler or request-error
  handler ever sees a callback. This story gives `server.Ingest` a third handler for the callbacks, a mux under
  `/api/v1/callbacks/` that `runtime.go` builds: `POST /api/v1/callbacks/mattermost/{connection_id}` goes to the
  Mattermost callback handler of `internal/mattermost/callback.go`, and S-041 and S-042 add the Telegram webhook to the
  same mux. The merged single-port mode (`server.Merge`) needs no change, since it already routes ingest paths to
  `server.Ingest`.
- **Button presses** (C-13.FR-4; `callback.go`, `mattermostAction`): the callback handler owns the promise that the
  callback always answers `200` with a JSON object: it reads the body up to `ingest.body_limit` and answers `200` `{}`
  to a body that cannot be read, is not JSON or does not match `MattermostActionRequest`, to an unknown method on the path and to any
  error of its own, logging the cause; nothing in front of it validates the request. The handler looks up the
  Connection (unknown or deleted: nothing more), verifies the action id with
  `internal/buttons` and the key named by `context.key_id`, and checks that `post_id` is the `message_id` of the Alert
  Group's delivery to a Destination of this Connection and that `channel_id` is that Destination's channel; any failure
  is a refusal, answered only when `post_id` is the `message_id` of a delivery to a Destination of this Connection in
  that `channel_id` — otherwise the answer is `{}`, since such a callback did not come from a Muster button, and the
  empty answer tells it nothing about which Connections and posts exist. It maps `user_id` through `accountlinks.Lookup("mattermost:<connection>", user_id)`; without a link it
  sends "Your Mattermost account is not linked to Muster. Link it in your profile: {MUSTER_PUBLIC_URL}/profile". With
  a link it dispatches the Command — `acknowledge`, `unacknowledge`, `resolve`, `unsnooze`, or `snooze` until now plus
  the pressed duration of `route.snooze_durations` — as the User with the Transport `mattermost`, and answers after the
  dispatch, well within the 30 s the server waits (F-032). Every text for the person — the refusal of C-10 (its
  `detail`), "This button could not be verified; nothing was changed.", "Muster could not run this command; nothing
  was changed." for a failure once the press is bound to its post, the texts of C-18.FR-8 for Viewers and disabled
  Users — reaches that person in one of two ways (D284). First it goes as one `POST /api/v4/posts/ephemeral`
  (`user_id`, `post: {channel_id, message}`) through `delivery.Interactive` on the limiters of the Destination and its
  Connection, bounded with its limiter wait by `delivery.interactive_budget`, without `root_id` for a press on a Root
  message so that it shows in the channel view (F-026), and with the Root message as `root_id` for a press on a Thread
  reply (F-055, used from S-049); the answer is then `{}`. That call needs `create_post_ephemeral`, which a bot with the
  role Member lacks (F-063). When it is refused with `403` `api.context.permissions.app_error` — expected, so not an
  error — or fails any other way (a timeout, a `5xx`, no limiter token in the budget, logged as `error`), the text
  becomes the answer's `ephemeral_text` with `skip_slack_parsing: true`, which needs no permission and which Mattermost
  shows to the person alone, from System, in the Thread of the Root message (F-025, F-062). Nothing is sent on success,
  and the answer never carries `update` (F-023). The callback's answer comes within the budget after the dispatch, so
  within the server's wait. A panic while handling a press is
  logged and answered `200` `{}`. A Snooze button names the index of its duration, which is read from the Route at the press. Log
  events `mattermost_press` (INFO: `connection`, `group`, `command`, `outcome`, `answer` — `ephemeral_post` or
  `ephemeral_text`, how the person was answered — and `error` when the press failed or its ephemeral post failed other
  than with the `403` of the missing permission).
- **Connection check warning** (C-13.FR-2, C-13.AC-16; `internal/connections`, `internal/mattermost/client.go`,
  `internal/doctor`): once the token works, the check reads the bot's roles — the `roles` of `GET /api/v4/users/me`,
  then `POST /api/v4/roles/names` with them — and, when no role that is not deleted lists `create_post_ephemeral`,
  passes with the warning `press_answers_in_thread` in `ConnectionCheckResult.warnings`, the same decision the server
  makes for the bot's requests (F-064); a failed read of the roles adds nothing. `muster doctor` prints the Connection
  as `WARN connection <name>: ` with the hint that answers to button presses show in the Thread of the Root message
  and that the permission, for example the system admin role, shows them in the channel; a WARN fails nothing. S-040
  shows the warning on the Connection page.
- **Account link lookup** (`internal/accountlinks`): `Lookup(identity_space, external_id) → User` over `account_links`,
  read-only; S-051 adds everything else.
- **Delivery problem** (C-13.FR-12; `internal/groups/filters.go`, `read.go`): an Alert Group has `delivery_problem`
  when one of its deliveries is `not_delivered`, `deleted_in_messenger`, waits for a Broken Destination, or has a Thread
  not attached (from S-042); `delivery_problem=true` filters the list and the counts.
- **Internal alerts suggestion** (C-13.FR-11, C-08.FR-11; `internal/routing/suggestions.go`): `internal_alerts` applies
  while a Destination exists and some Internal alert of the closed registry (`internal/internalalerts`), with the
  labels it is raised with, would be taken by no Route other than the Default route in the current evaluation order —
  the test `heartbeatLostApplies` already makes for `MusterHeartbeatLost`, run over every Internal alert. The Route
  "Muster: Heartbeat lost" that the `heartbeat_lost` suggestion of S-026 creates matches only
  `alertname="MusterHeartbeatLost"`, so it takes that one alert and leaves the suggestion standing for the others;
  both suggestions can apply at once. Accepting `internal_alerts` creates the Route "Muster internal alerts" with the
  Matcher `alertname=~"Muster.*"`, the chosen `destination_ids` (required: without them `422` `required` at
  `/destination_ids`), not Urgent, the Group key `[alertname, integration, destination, route]` and the On-call
  profile, directly below the last Route other than the Default route that takes an Internal alert, otherwise at the
  top (D268). The labels an Internal alert is raised with are tried with every Integration that is not deleted (its
  Static labels included), every Destination that is not deleted and every Route, and once without an entity.
- **`muster doctor`** (C-02.FR-14): one line per Mattermost Connection (`connection <name>: ok` or the failing step) and
  per Mattermost Destination (`destination <name>: ok` or the failing check), with the checks of S-039, read-only, in
  the background class.
- **Secrets** (C-03.FR-21, lint 5): the probe of S-039 in `internal/archlint/secretleak.go` is extended to the adapter,
  the callback and the answers to presses.
- **Documentation** (C-13.FR-7; `docs/messengers/mattermost.md`): the bot account and its permissions — the role
  Member is enough, and answers to presses then show in the Thread of the Root message (F-025, F-063); `create_post_ephemeral`, for
  example through the system admin role, shows them in the channel, and the Connection check says which applies;
  allowing the bot
  to send direct messages (F-028); `ServiceSettings.AllowedUntrustedInternalConnections` with the host of
  `MUSTER_INGEST_URL` when it is internal, the "Action integration error" symptom and that only the server log says
  why (F-022), and that a test message checks it (S-047); the rate limit off by default, 10 requests per second per
  client address when on, shared by all Connections of one installation (F-030); keeping `PostEditTimeLimit` unlimited
  (F-032); edits notify nobody (F-029), Thread replies notify followers by their settings and a Mention makes a person
  a follower (F-027), so Quiet means "without a Mention".
- **Load profile** (C-01.FR-7, NFR-1, NFR-2, P-44; `test/load/main.go`): the load test of S-004 gains its full profile.
  It needs no token from outside: it signs in with the bootstrap Admin (`MUSTER_LOAD_ADMIN_EMAIL` and
  `MUSTER_LOAD_ADMIN_PASSWORD`, by default those of the development defaults) and creates a Personal access token for
  the run, then with it an Integration whose token it registers with its fake Alertmanager, a Route with the Group key
  `[alertname, cluster]` and a Storm threshold above the run's Alert Groups, so that no Storm holds the posts it
  measures, and a Mattermost Destination on the fake server whose limiter (and its Connection's, 1,000 per second) and
  the fake's rate limit, turned off, leave room; it then drives the fake Alertmanager to send 50 webhooks per second for one minute that bring 10,000
  fingerprints into 1,000 Alert Groups. It fails when a webhook is rejected or fails, when fewer than 10,000 Alerts fire
  or fewer than 1,000 Alert Groups are open at the end, when the 99th percentile of
  `muster_ingest_request_duration_seconds` exceeds 1 s (P-44), or when the 95th percentile of
  `muster_delivery_latency_seconds` exceeds 5 s (NFR-2); both are read from the histogram buckets on `/metrics` before
  and after the run. `-destination=none` runs the profile without the Destination and without the delivery threshold.
  The `load-test` job of `nightly.yml` (S-004) passes it on the compose run, with the bootstrap Admin it writes into the
  compose example's `.env`, for the footprint of NFR-3, and fails on a breach.
- **Wiring** (`sqlc.yaml`): the entry for `internal/accountlinks/query.sql`.
- **Defaults**: `delivery.interactive_budget`.

## Steps

1. Write the adapter: layout, escaping, edits, replies, Mentions and the response mapping, with `Check` from S-039.
   Check: `layout_test.go` and `adapter_test.go` against the in-process fake.
2. Write the callback handler with signatures, the binding, the lookup, dispatch and the answers, and mount it
   in `server.Ingest`. Check: `callback_test.go` covers C-13.AC-2, AC-4, AC-11 and AC-14 and a body that is not JSON;
   `internal/server/server_test.go` routes `/api/v1/callbacks/mattermost/…` to the callback handler on the ingest
   listener and in the merged mode.
3. Add the Delivery problem filter, the suggestion, the doctor lines, the documentation, the secret probe and the
   end-to-end test. Check: Verification below.
4. Give the load test its full profile, its own sign-in and data, `-destination=none` and the thresholds, and pass the
   switch in the nightly job. Check: `make load-test` against `muster dev` passes and prints the figures; with every
   post held 6 s by a fake fault (`delay_ms`) it fails on NFR-2; a manual run of the nightly workflow passes both runs.

## Verification

```sh
make dev &
# the Admin's session (`jar`, `H`) as in S-011; the Responder "bob", created with `createUser` and the role responder as
# in S-011; an Integration "lab" with its fake
# receiver, NOTIFY, ADV and AG as in S-020 and S-028; the On-call policy in P as in S-025; the Connection "mm" (C), the
# Mention settings M and the Destination "alerts" on ch-alerts (D) as in S-039
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake; FMM=127.0.0.1:18065/_fake
POSTS() { curl -s $FMM/posts | jq -c "$1"; }
REQ() { curl -s $FMM/requests | jq -c "$1"; }
R=$(curl -s "${H[@]}" $API/routes -d "{\"name\":\"db\",\"matchers\":[{\"label\":\"team\",\"op\":\"=\",\"value\":\"db\"}],\"urgent\":false,
  \"group_key\":[\"alertname\",\"cluster\"],\"destination_ids\":[\"$D\"],\"policy\":$P}" | jq -r .id)

# C-13.AC-13, AC-5: a new Alert Group, with @channel <b>x</b> in a label
curl -s -X PUT $FAM/groups/d1 -d '{"receiver":"lab","route":"{}","labels":{"alertname":"DiskFull"}}' > /dev/null
curl -s -X PUT $FAM/groups/d1/alerts/i1 -d '{"labels":{"team":"db","cluster":"a","pod":"i1","note":"@channel <b>x</b>"}}' > /dev/null
NOTIFY d1 '{"reason":"first notification"}'; sleep 1; G=$(AG 'pod%3D%22i1%22')
POSTS '[.[] | select(.root_id == "") | .props.attachments[0] | {n: (.actions | length), title, tl: (.title_link | test("/alert-groups/AG")), footer, color,
  open: (.text | test("\\[Open in Muster\\]")), lit: (.text | test("@​channel \\\\<b\\\\>x"))}]'
# [{"n":5,"title":"#1 DiskFull","tl":true,"footer":"Muster v…","color":"#d32f2f","open":true,"lit":true}]
POSTS '[.[] | select(.root_id == "") | .message] | first'                    # "🔴 #1 DiskFull"
curl -s $FMM/notifications | jq length                                      # 0

# C-13.AC-1, AC-6: Acknowledge edits; a new Alert is a Thread reply — Quiet now, since the Alert Group is acknowledged
curl -s "${H[@]}" -X POST $API/alert-groups/$G/acknowledge | jq -r .outcome  # done
sleep 1; REQ '[.[] | select(.path | test("/patch$")) | .body | fromjson | .props.attachments[0] | {color, a: [.actions[].name]}] | last'
# {"color":"#f57c00","a":["Unack","Resolve","Snooze 1 h","Snooze 4 h","Snooze 24 h"]}
curl -s -X PUT $FAM/groups/d1/alerts/i2 -d '{"labels":{"team":"db","cluster":"a","disk":"i2"}}' > /dev/null
NOTIFY d1 '{"reason":"new alerts added"}'; sleep 1
POSTS '[.[] | select(.root_id != "") | {m: (.message | test("@")), root: (.root_id != "")}] | last'   # {"m":false,"root":true}
POSTS '[.[] | select(.root_id == "") | .reply_count] | first'                # 1
# a Loud reply: new Alerts in a firing Alert Group mention @channel (the Destination's new_alerts setting)
curl -s "${H[@]}" -X POST $API/alert-groups/$G/unacknowledge > /dev/null
curl -s -X PUT $FAM/groups/d1/alerts/i3 -d '{"labels":{"team":"db","cluster":"a","disk":"i3"}}' > /dev/null
NOTIFY d1 '{"reason":"new alerts added"}'; ADV 60; sleep 1                  # past the Thread batching window
POSTS '[.[] | select(.root_id != "") | .message | startswith("@channel")] | last'   # true
curl -s $FMM/notifications | jq -c '[.[] | .kind] | unique'                # ["channel"]

# C-13.AC-4, AC-14: a press from an account without an Account link
ROOT=$(POSTS '[.[] | select(.root_id == "")][0].id' | tr -d '"')
# a Member bot (the fake's default): the ephemeral post is refused with 403, the answer carries the text (D284)
curl -s -X POST $FMM/press -d "{\"post_id\":\"$ROOT\",\"action_id\":\"ack\",\"user_id\":\"u-alice\"}" | jq -c '{status, answer}'
# {"status":200,"answer":{"ephemeral_text":"Your Mattermost account is not linked to Muster. Link it in your profile: http://localhost:8080/profile","skip_slack_parsing":true}}
curl -s $FMM/ephemeral | jq -c --arg r "$ROOT" '.[-1] | {user_id, from, thread: (.root_id == $r)}'
# {"user_id":"u-alice","from":"System","thread":true}
REQ '[.[] | select(.path == "/api/v4/posts/ephemeral") | .status]'         # [403]
# an admin bot: the ephemeral post is made in the channel view, the answer is empty
curl -s -X PUT $FMM/config -d '{"bot_system_admin":true}' > /dev/null
curl -s -X POST $FMM/press -d "{\"post_id\":\"$ROOT\",\"action_id\":\"ack\",\"user_id\":\"u-alice\"}" | jq -c .answer   # {}
curl -s $FMM/ephemeral | jq -c '.[-1] | {user_id, root_id, shown_in}'      # {"user_id":"u-alice","root_id":"","shown_in":"channel"}
curl -s -X PUT $FMM/config -d '{"bot_system_admin":false}' > /dev/null
curl -s -b jar $API/alert-groups/$G | jq -r .status                          # firing

# C-13.FR-4: a linked Responder (the link row set up directly; S-051 creates links)
psql "$MUSTER_DATABASE_URL" -qc "INSERT INTO account_links (org_id, public_id, user_id, messenger, connection_id, external_id, username, created_at)
  SELECT 1, 'AK0000000000B1', u.id, 'mattermost', c.id, 'u-bob', 'bob', now() FROM users u, connections c WHERE u.login = 'bob' AND c.name = 'mm'"
curl -s -X POST $FMM/press -d "{\"post_id\":\"$ROOT\",\"action_id\":\"ack\",\"user_id\":\"u-bob\"}" > /dev/null; sleep 1
curl -s -b jar "$API/alert-groups/$G/timeline?limit=1" | jq -c '.items[0] | {event, t: .actor.transport, u: .actor.name}'
# {"event":"acknowledged","t":"mattermost","u":"bob"}
curl -s $FMM/presses | jq -c '.[-1].answer'                                 # {}

# C-13.AC-2: a changed action id, an unknown Connection, a body that is not JSON
CTX=$(curl -s $FMM/posts | jq -c "[.[] | select(.id == \"$ROOT\")][0].props.attachments[0].actions[0].integration.context")
curl -s -X POST localhost:8081/api/v1/callbacks/mattermost/$C -d "{\"user_id\":\"u-bob\",\"channel_id\":\"ch-alerts\",\"post_id\":\"$ROOT\",
  \"context\":$(jq -c '.action |= (.[0:-2] + "AA")' <<<"$CTX")}" | jq -r .ephemeral_text
# This button could not be verified; nothing was changed.
curl -s -X POST localhost:8081/api/v1/callbacks/mattermost/CN000000000000 -d '{"user_id":"u-bob","channel_id":"x","post_id":"y","context":{"action":"z","key_id":"k"}}'
# {}
curl -s -X POST localhost:8081/api/v1/callbacks/mattermost/$C -d 'not json' -w ' %{http_code}\n'   # {} 200
# (C-13.AC-11 runs in test/e2e/mattermost_test.go with two Alert Groups: the action id of the first, the post_id of the second → refused, nothing changes)

# C-13.AC-15: 429 text/plain with Retry-After: 1 holds the whole Connection
curl -s -X POST $FMM/faults -d '{"path":"/api/v4/posts","status":429,"retry_after_seconds":1,"content_type":"text/plain","body":"limit exceeded","times":1}'
curl -s -X PUT $FAM/groups/d1/alerts/i4 -d '{"labels":{"team":"db","cluster":"a","disk":"i4"}}' > /dev/null
NOTIFY d1 '{"reason":"new alerts added"}'; ADV 60; sleep 3
REQ '[.[] | select(.path == "/api/v4/posts")] | .[-2:] | (.[1].at_ms - .[0].at_ms) / 1000 | . >= 1 and . < 2'   # true

# C-13.AC-3, AC-8: the bot leaves the channel; a new Alert Group meets 403 and resolves while Broken
curl -s -X DELETE $FMM/channels/ch-alerts/members/musterdevbotuserfake000000
curl -s -X PUT $FAM/groups/d1/alerts/j1 -d '{"labels":{"team":"db","cluster":"b","disk":"j1"}}' > /dev/null
NOTIFY d1 '{"reason":"new alerts added"}'; sleep 1
curl -s -b jar $API/destinations/$D | jq -c '.health | {state, r: (.reason | test("403"))}'   # {"state":"broken","r":true}
curl -s -b jar $API/routes/$R | jq -r '.destinations[0].health.state'        # broken
curl -s -b jar "$API/integrations/$(curl -s -b jar $API/integrations | jq -r '.items[] | select(.builtin) | .id')/alerts?state=firing" \
  | jq -r '.items[].labels.alertname'                                       # MusterDestinationBroken
curl -s -X PUT $FAM/groups/d1/alerts/j1 -d '{"labels":{"team":"db","cluster":"b","disk":"j1"},"status":"resolved"}' > /dev/null
NOTIFY d1 '{"reason":"some alerts resolved"}'
curl -s -X PUT $FMM/channels/ch-alerts/members/musterdevbotuserfake000000
D0=$(curl -s localhost:8082/metrics | grep 'muster_client_requests_total{client="delivery",outcome="ok"}' | awk '{print $2}')
ADV 300; sleep 2
curl -s -b jar $API/destinations/$D | jq -r .health.state                   # healthy
curl -s localhost:8082/metrics | grep 'muster_client_requests_total{client="delivery",outcome="ok"}' | awk -v d=$D0 '{print (($2 - d) >= 2)}'   # 1
# the same through "Check", at once, in the interactive class
curl -s -X DELETE $FMM/channels/ch-alerts/members/musterdevbotuserfake000000
curl -s -X PUT $FAM/groups/d1/alerts/i5 -d '{"labels":{"team":"db","cluster":"a","disk":"i5"}}' > /dev/null
NOTIFY d1 '{"reason":"new alerts added"}'; sleep 1
curl -s -b jar $API/destinations/$D | jq -r .health.state                   # broken
curl -s -X PUT $FMM/channels/ch-alerts/members/musterdevbotuserfake000000
I0=$(curl -s localhost:8082/metrics | grep 'muster_client_requests_total{client="interactive",outcome="ok"}' | awk '{print $2}')
curl -s "${H[@]}" -X POST $API/destinations/$D/checks | jq -c '{ok, h: .health.state}'   # {"ok":true,"h":"healthy"}
curl -s localhost:8082/metrics | grep 'muster_client_requests_total{client="interactive",outcome="ok"}' | awk -v i=$I0 '{print (($2 - i) >= 2)}'   # 1

# C-13.AC-12: a deleted post of an open Alert Group answers the edit with 403; the plain read answers 404, and the
# Root message is republished once, Quietly, with the note; the Destination stays healthy
curl -s -X DELETE $FMM/posts/$ROOT; curl -s "${H[@]}" -X POST $API/alert-groups/$G/unacknowledge > /dev/null; sleep 2
REQ "[.[] | select(.path == \"/api/v4/posts/$ROOT/patch\") | .status] | last"            # 403
REQ "[.[] | select(.path == \"/api/v4/posts/$ROOT\") | .status] | last"                 # 404
POSTS '[.[] | select(.root_id == "" and .delete_at == 0) | .props.attachments[0].text | test("The previous message was deleted at [0-9]{2}:[0-9]{2}")] | last'   # true
curl -s -b jar $API/destinations/$D | jq -r .health.state                   # healthy

# C-13.AC-10: Not delivered, filtered, then delivered again
curl -s -X POST $FMM/faults -d '{"path":"/api/v4/posts/*/patch","status":400,"times":1}'
curl -s "${H[@]}" -X POST $API/alert-groups/$G/acknowledge > /dev/null; sleep 1
curl -s -b jar "$API/alert-groups?delivery_problem=true" | jq -c '[.items[] | {id, delivery_problem}] | map(select(.id == "'$G'"))'
# [{"id":"AG…","delivery_problem":true}]
curl -s -b jar "$API/alert-groups/$G/timeline?kind=delivery&limit=1" | jq -r '.items[0].delivery_event'   # not_delivered
curl -s "${H[@]}" -X POST $API/alert-groups/$G/unacknowledge > /dev/null; sleep 1
curl -s -b jar "$API/alert-groups?delivery_problem=true" | jq '[.items[] | select(.id == "'$G'")] | length'   # 0

# C-13.FR-11, AC-9: the Internal alerts Route suggestion
curl -s -b jar $API/route-suggestions | jq -r '.items[].id'                  # heartbeat_lost (the demo Integration), internal_alerts
curl -s "${H[@]}" -X POST $API/route-suggestions/internal_alerts/accept -d "{\"destination_ids\":[\"$D\"]}" > /dev/null
curl -s -b jar $API/routes | jq -c '.items[0] | {m: .matchers[0].value, d: [.destinations[].id]}'   # {"m":"Muster.*","d":["DS…"]}

# C-02.FR-14
./bin/muster dev doctor | grep -E ' (connection|destination) '
# WARN connection Dev Mattermost: the bot may not make ephemeral messages (create_post_ephemeral), so answers to button presses show in the Thread of the Root message; …
# WARN connection mm: the bot may not make ephemeral messages (create_post_ephemeral), so answers to button presses show in the Thread of the Root message; …
# OK   destination alerts: ok

# C-01.FR-7: the full load profile (NFR-1, NFR-2, P-44)
make load-test                                   # signs in as admin@example.org and creates its own data
# accepted 3000, rejected 0, failed 0; alerts firing 10000; alert groups open 1000
# ingest p99 0.0xs (≤ 1 s); delivery p95 x.xs (≤ 5 s)
# PASS
```

`test/e2e/mattermost_test.go` repeats these steps and adds C-13.AC-11, the C-11 checks against the fake server —
C-11.AC-1 (ten Alerts and an Acknowledge within 2 s: one post, at most one patch), C-11.AC-3 (a Storm), C-11.AC-5 (in
the harness mode of S-004 with the fakes in the test process: the fake holds the answer to a new post with `delay_ms`;
the `muster dev --replica` process is stopped and started again while the fake keeps its records; two posts and a
`possible_duplicate` Timeline entry), C-11.AC-6 and AC-7 (repeated `500`s until Broken, then recovery) — and C-12.AC-3
and AC-5 (a failing Route template gives the Fallback template in the post; a Russian Route gives Russian posts and
replies), C-12.FR-11 with the limit of F-057 (an Alert Group of 600 Alerts whose post keeps its `message` and its
attachment text within 16,383 characters, its Alert list cut to the distinct values ending with "+N more" and the link to Muster kept), a
Thread reply under a deleted Root message (`400` for its `root_id`, F-058) that republishes the Root message without a
read, and an edit refused with `403` while the post still exists (a fault on its patch, the plain read `200`) that makes
the Destination Broken.

**Optional manual check against a real server** (the operator's test Mattermost 11.2.2): create a bot without the
system admin role and a channel, create the Connection and a Destination through the API, send one Alert Group from the
fake Alertmanager, and confirm by eye that the post shows the attachment, the links and the buttons; that Acknowledge
in Muster edits it; that a press from an unlinked account shows the ephemeral message from System in the Thread of the Root message; that a Loud reply
with `@channel` notifies while an edit does not; that a Loud Root message with a Mention notifies with the summary line
shown (F-056); and that after the Root message is deleted in the client, the next edit republishes it with the note
(F-058), which also answers the Mattermost entry of the facts' Pending list for a bot without that role.

## Open questions

- ~~**Where the "Muster internal alerts" Route goes when "Muster: Heartbeat lost" exists.** C-08.FR-11 creates an
  accepted suggestion at the top of the list. Placed above "Muster: Heartbeat lost", the broader `Muster.*` Route
  would take `MusterHeartbeatLost` first, and that alert would lose its urgent Route and its Group key by Integration.
  Recommendation: when a Route other than the Default route already takes an Internal alert, insert the new Route
  directly below the last such Route, otherwise at the top; this changes C-08.FR-11's "at the top of the list" for this
  one case, so the maintainer decides it (and the PRD wording) before the suggestion is implemented.~~
  _Resolved (D268):_ as recommended; C-08.FR-11 says so.

## Notes

- Suggested commit: `feat(mattermost): add the mattermost adapter, button callbacks and the delivery problem filter`.
- C-11 has no messenger of its own; this story's end-to-end test is where its acceptance is seen against a messenger.
- Split from S-039, which keeps Connections, Destinations, the Destination check and the fake server.
- `operator_attention`: the optional manual check against the operator's test Mattermost server.
- The load test is the first place where NFR-1 and NFR-2 can be measured end to end; the nightly job fails on a breach
  from this story on.
- The footer names a user by their Mattermost username without `@` (S-037), on the assumption that a bare username
  never notifies; no fact in `design/facts.md` confirms it. This story verifies it against the real Mattermost on the
  epsilon stand — a root post and a reply whose footer carries the bare username of a user mentioned nowhere else,
  watched in that user's client — records the result as a new `F-NNN` under Mattermost in `design/facts.md`, and, if a
  bare username does notify, changes the footer so that it no longer does (`internal/mattermost/layout.go`).
- The full load profile against `muster dev` on a developer workstation (macOS, PostgreSQL in Docker) fails NFR-2:
  Snapshot processing of one Integration takes about 35 ms each, so 50 webhooks per second build a backlog, and the
  delivery latency, counted from receipt, reaches about 58 s at p95 while delivery itself adds about 6 ms. The
  maintainer decides how NFR-2 is met (faster processing, the profile over several Integrations, or a narrower NFR-2)
  before the nightly job relies on it.
  _Resolved by [S-065](S-065-parallel-snapshot-processing-per-alertmanager-group-and-a-shorter-grouping-lock.md)
  (D283):_ faster processing, by processing the Alertmanager groups of an Integration in parallel and taking the
  grouping lock only to create or reopen an Alert Group; S-065 has the profile.
- Lint 3 stays strict (journal D282). `delivery.PublishOp`, `UpdateOp` and `ReplyOp` are exported, so any package can
  send, but only through `Interactive.Do`, which always takes a limiter token. The Mattermost client's post and patch
  methods therefore stay unexported, or `DefaultConfig` in `internal/archlint/archlint.go` adds them to lint 3's
  method list, so that nothing outside the delivery worker and the interactive path can call them.
- Changed after the merge (2026-10-08, journal D284): the answers to presses fall back to the `ephemeral_text` of the
  callback's answer. On the epsilon stand a press from an unlinked account logged `not_linked` with "the ephemeral
  answer was not sent: Mattermost answered 403", and the person saw nothing: the ephemeral post needs the
  `create_post_ephemeral` permission, which only system admins hold by default, and the bot correctly has the role
  Member (F-063); the spike of F-026 had used a bot with the system admin role. The maintainer chose a hybrid: the
  ephemeral post stays first, because it shows in the channel view when the bot may make it, and its `403` — or any
  other failure — puts the text into `ephemeral_text`, which needs no permission but shows only in the Thread (F-025,
  F-062), so that no answer is lost. `mattermost_press` names the way in `answer`; the Connection check and
  `muster doctor` warn when the bot's roles lack the permission (F-064), so the admin knows where the answers will
  show. The fake server refuses ephemeral posts with `403` unless its bot is made a system admin (`bot_system_admin` in
  `/_fake/config`) and answers `POST /api/v4/roles/names`. Lint 3 is unchanged.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-13.FR-2 | partial | the warning `press_answers_in_thread` of the Connection check and `muster doctor`; the page shows it in S-040 |
| C-13.FR-3 | partial | the Root message layout; the Destination's fields are S-039 |
| C-13.FR-4 | partial | presses on Root messages; presses from linked accounts are tested with link rows set up directly (S-051 creates them); presses on Thread replies are S-049 |
| C-13.FR-5 | full | |
| C-13.FR-7 | full | |
| C-13.FR-8 | full | |
| C-13.FR-10 | partial | the probe through `Check` and `muster doctor`; on save and through the API S-039, the "Check" button S-064 |
| C-13.FR-11 | partial | the suggestion in the API; the page is S-064 |
| C-13.FR-12 | partial | the filter and the field; the list filter and mark are S-064 |
| C-13.AC-1 | full | |
| C-13.AC-2 | full | |
| C-13.AC-3 | partial | Broken, the Internal alert and the health in the API; the banners are S-064 |
| C-13.AC-4 | full | |
| C-13.AC-5 | full | |
| C-13.AC-6 | full | |
| C-13.AC-8 | partial | the probe and the API check; the "Check" button is S-064 |
| C-13.AC-9 | partial | the API; the page is S-064 |
| C-13.AC-10 | partial | the API; the list mark is S-064 |
| C-13.AC-11 | full | |
| C-13.AC-12 | full | |
| C-13.AC-13 | full | |
| C-13.AC-14 | full | |
| C-13.AC-15 | full | |
| C-13.AC-16 | partial | the API and `muster doctor`; the Connection page is S-040 |
| C-11.AC-1 | partial | repeated end to end against the fake Mattermost server; full in S-034 |
| C-11.AC-5 | partial | repeated end to end with a restart of Muster; full in S-035 |
| C-11.AC-7 | partial | repeated end to end; full in S-035 |
| C-12.AC-3 | partial | the Fallback template in a real post; full in S-036 |
| C-11.FR-2 | partial | the ephemeral posts of the answers to presses |
| C-11.FR-7 | partial | Quiet and Loud posts in Mattermost |
| C-11.FR-9 | partial | the Mattermost Destination check as the Broken probe |
| C-12.FR-1 | partial | the Mattermost layout |
| C-12.FR-7 | partial | Markdown escaping |
| C-12.FR-8 | partial | the Mattermost syntax of Mentions in posts; the settings are stored by S-039 |
| C-12.FR-11 | partial | the Mattermost length limit |
| C-10.FR-3 | partial | the Transport `mattermost` |
| C-10.FR-6 | partial | Snooze durations as Mattermost buttons |
| C-10.FR-11 | partial | Mattermost presses and their private answers; refusals of unlinked presses are complete with S-051 |
| C-09.FR-13 | partial | the `delivery_problem` filter |
| C-08.FR-11 | partial | the `internal_alerts` suggestion |
| C-02.FR-14 | partial | Mattermost Connection and Destination checks in `muster doctor` |
| C-01.FR-7 | partial | the full load profile of NFR-1 and NFR-2 with its thresholds; the harness and the nightly job are S-004 |
| C-11.FR-8 | partial | the Mattermost response mapping |
