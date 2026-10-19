# C-23. Request log

[L1 index](../L1.md) · Stage: Shadow · UI: yes · Depends on: C-11, C-13, C-14, C-15, C-16, C-22

**Goal.** Show exactly what Muster sent to a messenger, a bridge or an outgoing webhook and what came back, and what
reached a Connection's addresses and what Muster answered — bodies included, secrets masked — so that a delivery, a
check or a press that went wrong can be understood without a packet capture. Delivery events (C-11.FR-21) say what
happened to an Alert Group; the Audit log (C-03) says who changed what; the Request log keeps the requests themselves,
for a short time.

Names in the form `area.setting` refer to rows of [defaults.md](defaults.md).

## Scenarios

1. A Root message looks wrong in Mattermost. The Admin opens the Alert Group page, follows "Requests" from its delivery
   to that Destination and sees the `createPost` Muster sent, with its body, and what the server answered.
2. A bridge refuses a Publication as `invalid`. The Connection's Request log shows the `postMessage`, the problem the
   bridge answered with its `detail`, and the latency.
3. Presses in Mattermost do nothing. The Connection's Request log shows no inbound `press` at all, so the server does
   not reach Muster; or it shows a `press` that Muster answered with the "not linked" text.
4. The Admin checks a new Bridge Connection before saving it; the check fails, and "Requests of this check" shows that
   `GET /v1/info` was answered with an HTML page by a reverse proxy.
5. A bridge pushes events signed with an old secret; the Request log shows each push answered `401`.
6. An outgoing webhook's endpoint answers `500`; the Destination's Request log shows each attempt with the endpoint's
   body.

## Functional requirements

- **C-23.FR-1** Muster records every HTTP request it sends for a Connection or an outgoing webhook Destination, and
  every request that reaches a Connection's addresses on the ingest listener, successful or not, each attempt a record
  of its own. Outbound: Publications, edits, Thread replies, final edits, Storm summaries, deletions, test messages and
  the press a Mattermost test makes through the bot (C-16.FR-3), answers to people (ephemeral posts, callback answers,
  `answerEvent`), the requests of Account links (finding people, direct messages), channel lists, `info` reads, webhook
  registration, Connection checks — of saved Connections and before saving (C-13.FR-14), marked as such — Destination
  checks and Broken probes, and event polling (Telegram `getUpdates`, Bridge `pollEvents`), except a long poll that
  ended without events. Inbound: Mattermost button presses (`callbacks/mattermost/{connection_id}`), Telegram updates in
  the webhook mode (`callbacks/telegram/{connection_id}`), pushed Bridge events (`callbacks/bridge/{connection_id}`) and
  Slack presses in the HTTP event mode (`callbacks/slack/{connection_id}`), each with Muster's answer, refused ones
  included — a bad signature, a wrong secret token, a Connection in the other mode — and the envelopes a Slack
  Connection receives over Socket Mode, each with Muster's confirmation (C-24.FR-14). A request whose path names no
  Connection, or a deleted one, is not recorded. Not recorded in L1: OIDC requests, the outgoing heartbeat, and the
  requests of `muster doctor` and `muster bridge check`, which run outside the server.
- **C-23.FR-2** A record holds: the time; the direction, `outbound` or `inbound`; the operation as its protocol names it
  — the Bot API method for Telegram (`sendMessage`, `getUpdates`), the `operationId` of `api/bridge.yaml` for a bridge
  (`postMessage`, `pollEvents`), the operation of Mattermost's REST API in lower camel case (`createPost`, `patchPost`,
  `getPing`), the Web API method for Slack (`chat.postMessage`, `apps.connections.open`), `event` or the request of the
  template mode (`create`, `update`, `open_thread`, `reply_in_thread`) for an outgoing webhook, and `press`, `update`,
  `push` or `interactive` for inbound requests and Slack's envelopes; the method and the URL; the request headers and
  body; the answer's status, headers and body, none for a request that got no answer; the latency in milliseconds; the
  path, direct or through the proxy; the attempt; when the request failed, its error class (the outcomes of the outbound
  package: `retry_after`, `transient`, `fatal`, `unknown`, `blocked`, `redirect`), its code (the codes of the checks,
  such as `dns_failed` or `timeout`) and the masked error text; for a request of a check or a test, its kind —
  `connection_check`, `draft_check` (a check before saving), `destination_check`, `test` or `probe` (the Broken probe) —
  and its check run, an id that the requests of one check or test share; and links — the Connection or the outgoing
  webhook Destination, and the Destination and the Alert Group when the request concerns one. A body longer than
  `request_log.body_max` is cut at a character boundary and marked truncated with its full size; bodies are stored as
  text, bytes that are not UTF-8 replaced.
- **C-23.FR-3** Nothing secret is stored. The URL keeps no user information, and the path prefix of a base URL, which
  may be a secret, is shown as `[redacted]`, as in errors (ADR-0015). Every Secret a request carries — a bot token, also
  inside a Telegram URL, a proxy password, an outgoing webhook's Secrets and Signing secret, a push secret, a Slack
  app-level token and Slack signing secret, a secret in a query — is replaced by `[redacted]` in the URL, the headers
  and both bodies; the values of `Authorization`, `Proxy-Authorization`, `Cookie`, `Set-Cookie`,
  `X-Telegram-Bot-Api-Secret-Token`, `webhook-signature`, `X-Slack-Signature` and of the outgoing webhook's signature
  header are replaced whatever they hold, and so are the `url` of a Socket Mode connection, with its one-time `ticket`,
  and a Slack press's `response_url`; the signed action ids of buttons (ADR-0011), which authenticate a Mattermost press
  and are the `value` of a Slack button or menu option, and the tokens and codes of Account links — `/start <token>` in
  a Telegram update, the `token` of an `account_link` event, the code in a direct message — are replaced too. Masking
  happens before the record is written: for outbound requests in the outbound package, which knows each request's
  Secrets, and for inbound requests in one place shared by the callbacks and the Socket Mode connection. The secret lint
  of ADR-0016 covers both (C-01.FR-6).
- **C-23.FR-4** Muster keeps the newest `request_log.max_records` records of each Connection and of each outgoing
  webhook Destination, and as many of the checks before saving of new Connections per Organization, none older than
  `request_log.max_age`; a Leader task deletes the rest and is safe to run twice (ADR-0007), and reads never show a
  record past either bound. Writing a record never delays an answer to a person beyond the write itself and never
  fails the request it records: a record that cannot be written is dropped, logged and counted.
- **C-23.FR-5** The API lists the records of a Connection, or of a Destination — all requests of an outgoing webhook,
  and for a messenger Destination the requests of its Connection that concern it — newest first, with cursor
  pagination and filters by direction, status class (`2xx`, `3xx`, `4xx`, `5xx`, or `none` for no answer), operation,
  Alert Group and check run, and reads one record with its headers and bodies. Reading a Connection's records needs
  `connections:read`, a Destination's `destinations:read`; the records of a check before saving of a new Connection are
  listed by their check run with `connections:read`. The results of the Connection check, the check before saving, the
  Destination check and the Destination test name their check run.
- **C-23.FR-6** The Connection page of every type and the outgoing webhook Destination page have a "Request log" tab: a
  list of the time, a direction arrow, the operation, the status or the error code, and the latency, with filters by
  direction, status class and operation. A record opens with the request and the answer, each with its headers and body
  — a JSON body formatted — copy buttons for the URL, the headers and each body, the kind of check, and the links to the
  Destination, the Alert Group and the records of its check run. The Alert Group page's delivery section has "Requests"
  for each Destination: what was sent and what came back for that Alert Group there. The check panels of Connections,
  the check before saving included, and the Destination's check and test panels link to the records of their run.
  Headers and bodies are untrusted text, shown as plain text.
- **C-23.FR-7** The Request log stories register their log events — at least `request_log_write_failed` and
  `request_log_pruned` — and the metrics `muster_request_log_records_total` (`direction`),
  `muster_request_log_write_failures_total` (`direction`) and `muster_request_log_pruned_total`. The Request log adds no
  Internal alert, and nothing of it leaves the installation (NFR-7).

## UI

Connections → a Connection → "Request log" (every type); Destinations → an outgoing webhook → "Request log"; the
record view with the request, the answer and copy buttons; "Requests" in the delivery section of the Alert Group page;
"Requests of this check" in the Connection check panel, the check before saving included, and in the Destination's
check and test panels.

## API surface

`request-log` (list, by Connection, Destination or check run, with filters; read one record with its headers and
bodies); the check run on the results of `connections/{id}/checks`, `connections/checks`, `destinations/{id}/checks`
and `destinations/{id}/tests`.

## Acceptance

Checked against the fake servers of `muster dev`.

- **C-23.AC-1** A Publication to the fake Mattermost server is recorded as an outbound `createPost` with the body
  Muster sent, the status and body the fake answered, the latency, the Connection, the Destination and the Alert
  Group; the record is listed in the Connection's Request log and under "Requests" of that Destination on
  the Alert Group page.
- **C-23.AC-2** Telegram long polling: a `getUpdates` that returned an update is recorded, one that ended empty is not.
  In the webhook mode an update to the callback is recorded as an inbound `update` with Muster's answer; a request with
  a wrong secret token for that Connection is recorded with its answer `401`; a request for an unknown Connection id is
  not recorded.
- **C-23.AC-3** No record holds a secret: the Telegram token appears neither in the URL path nor anywhere else; an
  outgoing webhook's Secret in a header and in the URL's query, its signature header, a bridge request's
  `Authorization`, a push's `webhook-signature`, the action ids of a Mattermost post and of a press on it, and the
  Account link token of a `/start` update and of an `account_link` event are stored as `[redacted]`.
- **C-23.AC-4** With 510 requests to one Connection, its Request log lists the newest 500, and after the Leader's
  pruning the database holds 500 for it; after the development clock moves 8 days, the records older than
  `request_log.max_age` are neither listed nor kept; a body of 40 KB is stored cut to `request_log.body_max`, marked
  truncated with its full size.
- **C-23.AC-5** A check before saving of a saved Connection is recorded under that Connection with the check kind
  `draft_check`, and the records of a check before saving of a new Connection are listed by the check run its result
  names; a push that the fake bridge signs with a wrong secret is recorded with its answer `401`; an outgoing webhook
  answered `500` is recorded once per attempt with the endpoint's body.
- **C-23.AC-6** The "Request log" tab of a Connection and of an outgoing webhook Destination lists records with the
  direction arrow, operation, status and latency and filters them by direction, status class and operation; a record
  shows the request and the answer with their headers and bodies and copies each; "Requests" on the Alert Group page
  opens the records of that Destination and Alert Group; every text is in English and Russian, and the Alert Group page
  keeps working at 360 px wide without horizontal scrolling.

## Related ADRs

ADR-0005, ADR-0006, ADR-0007, ADR-0011, ADR-0014, ADR-0015, ADR-0016, ADR-0017.

## Depends on

C-11 — delivery and the interactive path; C-13, C-14, C-15 and C-22 — the requests it records and the callbacks it
records at; C-16 — the Destination test and check. C-23 was added after the Bridge: it is needed from the Shadow stage
on, when the first messages go out and a difference from the existing receiver has to be explained.

## Suggested story split

- **BE** — the table and its pruning, recording outbound requests in the outbound package with their masking, the
  links to Destinations and Alert Groups, the API.
- **BE** — recording inbound requests at the three callbacks with Muster's answers, and the kinds and runs of checks
  and tests, the check before saving included, with the run on their results.
- **FE** — the "Request log" tab of Connections and outgoing webhooks, the record view, "Requests" on the Alert Group
  page, the links from the check and test panels.
