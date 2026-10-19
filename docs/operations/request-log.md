# Request log

The **Request log** keeps the recent HTTP requests Muster sent to a messenger, a bridge or an outgoing webhook, and
those that reached a Connection's addresses, each with its answer and with secrets masked. It shows what Muster sent
and what came back when a Root message looks wrong, a check fails or a button press does nothing, without a packet
capture. It is neither the Audit log, which says who changed what, nor the Delivery events, which say what happened to
an Alert Group's delivery: the Request log keeps the requests themselves, for a short time.

## What is recorded

Each attempt of a request is a record of its own.

**Requests Muster sends** for a Connection or an outgoing webhook Destination: Root messages, their edits, Thread
replies, final edits, Storm summaries and deletions; test messages, and the button press a Mattermost test makes
through the bot; answers to people, such as ephemeral messages and callback answers; the requests of Account links,
such as finding a person and sending a direct message; channel lists, bridge info and webhook registration; event
polling (Telegram `getUpdates`, Bridge `pollEvents`); and the requests of checks and tests — the Connection check, the
check before saving, also of a Connection that does not exist yet, the Destination check, the test message and the
probe of a Broken Destination.

**Requests that reach Muster** on the ingest listener, each with the answer Muster gave, refused ones included — a bad
signature, a wrong secret token, a Connection in the other event delivery mode:

`press`
`/api/v1/callbacks/mattermost/{connection_id}`
:   A button press in Mattermost.

`update`
`/api/v1/callbacks/telegram/{connection_id}`
:   A Telegram update in the webhook mode.

`push`
`/api/v1/callbacks/bridge/{connection_id}`
:   An event a bridge pushes.

A record holds:

- the time, and the direction: **Sent** (→) or **Received** (←);
- the operation as its protocol names it: the method of the Telegram Bot API (`sendMessage`, `editMessageText`,
  `getUpdates`), the operation of Mattermost's REST API in lower camel case (`createPost`, `patchPost`, `getPing`), the
  operation of the Bridge protocol (`getInfo`, `postMessage`, `pollEvents`), `event` or the request of a template
  (`create`, `update`, `open_thread`, `reply_in_thread`) for an outgoing webhook, and `press`, `update` or `push` for
  requests that reached Muster;
- the method, the URL, the request headers and body, and the answer's status, headers and body — none for a request
  that got no answer;
- the latency in milliseconds, the path — direct or through the proxy — and the attempt;
- for a request that failed, its error class, what failed — the codes of the checks, such as a DNS lookup that failed
  or no answer in time — and the error text;
- for a request of a check or a test, its kind — **Connection check**, **Check before saving**, **Destination check**,
  **Test message** or **Broken probe** — and its check run, which the requests of one check or test share;
- the Connection or the outgoing webhook Destination it belongs to, and the Destination and the Alert Group it
  concerns, when it concerns one.

## What is not recorded

- A long poll that ended without events: an empty `getUpdates` of Telegram or `pollEvents` of a bridge.
- A request to a callback address whose path names no Connection, or a deleted one.
- The requests of OIDC sign-in and of the outgoing heartbeat.
- The requests of `muster doctor` and `muster bridge check`, which run outside the server.

## Bounds

The bounds are built in; nothing configures them.

`request_log.max_records`
:   The newest 500 records of each Connection, of each outgoing webhook Destination, and of the checks before saving of
    new Connections in an Organization.

`request_log.max_age`
:   7 days on the business clock.

`request_log.body_max`
:   16 KB (16,384 bytes) of each request or answer body.

- A record past either bound is neither listed nor shown, even before it is deleted. The Leader deletes such records
  every minute (the task `request_log_pruning`).
- A longer body is cut at a character boundary and marked with its full size: the record shows "Cut at 16 KB of
  {size} KB". Bodies are stored as text; bytes that are not UTF-8 are replaced.
- A URL is kept up to 16 KB and the names and values of a request's or an answer's headers up to 64 KB in all; what is
  cut ends with "…".
- The body of a request that reached Muster, or of Muster's answer, that is larger than 256 KB, or that was not read to
  the end, is kept as its size only: the record shows "Not kept: {size} KB, too large or not read to the end".
- Writing a record never fails or delays the request it records beyond the write itself, which may take at most 2
  seconds. A record that cannot be written is dropped, logged as `request_log_write_failed` and counted in
  `muster_request_log_write_failures_total`; see [Log events](../reference/log-events.md) and
  [Metrics](../reference/metrics.md).

## Masking

Nothing secret is stored. Muster masks a record before it writes it: the requests it sends where they are made, which
knows each request's secrets, and the requests that reach it in one place shared by the three callback addresses.
Every masked value reads `[redacted]`.

- **Secrets a request carries**, wherever they appear — the URL, the headers and both bodies: a bot token, also inside
  a Telegram URL, the proxy password, an outgoing webhook's Secrets and Signing secret, a bridge's push secret, a
  secret in a query.
- **Headers whose values are always masked**, whatever they hold: `Authorization`, `Proxy-Authorization`, `Cookie`,
  `Set-Cookie`, `X-Telegram-Bot-Api-Secret-Token` and `webhook-signature`, the signature header of outgoing webhooks
  and of pushed Bridge events.
- **The URL** keeps no user information, and the path of a Connection's base URL, which may be a secret — such as the
  path of a bridge behind a reverse proxy — is shown as `[redacted]`, as in errors.
- **Values that authenticate a person**: the signed action ids of buttons, which authenticate a Mattermost press, and
  the tokens and codes of Account links — `/start <token>` in a Telegram update, the `token` of an `account_link`
  event, the code in a direct message.

Headers and bodies are untrusted text. The record view shows them as plain text and never renders HTML or Markdown.

## Who may read it

To list the records of a Connection, or of a check run
:   needs: `connections:read`

To list the records of a Destination
:   needs: `destinations:read`

To open a record of a Connection, or of a check before saving
:   needs: `connections:read`

To open a record of an outgoing webhook Destination
:   needs: `destinations:read`

A messenger Destination's requests are its Connection's: its list shows the records of its Connection that concern
it, and opening one of them needs `connections:read`. The built-in Roles — Admin, Responder and Viewer — hold both
Permissions; a Personal access token may hold fewer.

## Where to find it

- **Connections → a Connection → Request log**, for every type, and **Destinations → an outgoing webhook → Request
  log**. A messenger Destination's page has no tab of its own. The list shows, newest first, the time in your time
  zone, the direction arrow, the operation with the kind of check below it, the status — or, without an answer, what
  failed — and the latency; **Load more** shows older records. Filters by direction, status class (`2xx`, `3xx`,
  `4xx`, `5xx` or **No answer**) and operation are kept in the address, such as
  `?tab=request-log&direction=inbound&status_class=4xx`, so a link to a filtered list can be shared.
- **The Alert Group page → Delivery → Requests** on each Destination's row: what was sent to that Destination for this
  Alert Group and what came back.
- **Requests of this check** under the result of the Connection check, of the check before saving and of the
  Destination's check and test: the requests that check or test made.
- **A record** opens with the request — method, URL, headers, body — and the answer — status, headers, body — side by
  side on a wide screen and one under the other on a narrow one; a JSON body is indented. **Copy URL**, **Copy
  headers** and **Copy body** copy each, the body as it is stored rather than indented. A record links to its
  Destination, its Alert Group and the other requests of its check.

The API reads the same records: `listRequestLog` and `getRequestRecord` in the [API reference](../reference/api.md),
with the check run in the results of the Connection check, the check before saving, the Destination check and the
Destination test.

## Examples

- **Button presses in Mattermost do nothing.** Filter the Connection's Request log by **Received**. No `press` at all
  means that the Mattermost server does not reach Muster: see
  [Let button presses reach Muster](../messengers/mattermost.md#let-button-presses-reach-muster). A `press` that Muster
  answered shows the answer the person got, such as the text that their account is not linked.
- **A Root message looks wrong.** On the Alert Group page, open **Requests** of that Destination: the `createPost`,
  `sendMessage` or `postMessage` shows the body Muster sent and what the messenger answered.
- **A bridge refuses a message.** The Connection's Request log shows the `postMessage`, the problem the bridge answered
  with its detail, and the latency.
- **A check before saving fails.** **Requests of this check** shows what the address answered, such as an HTML page
  of a reverse proxy instead of the bridge's info.
- **Pushed events are refused.** A bridge that signs its pushes with an old secret shows each `push` answered `401`.
- **An outgoing webhook fails.** The Destination's Request log, filtered by `5xx`, shows each attempt with the
  endpoint's status and body.
