# 0017. Bridge protocol for messengers without native support

- Status: Accepted
- Date: 2026-10-14
- Amended: 2026-10-14 — after the review of the Bridge stories (journal D323): the status mapping of problems without
  a `class` is the bridge's own, never ADR-0005's; idempotency keys are scoped per token; an inbound event's id is
  claimed before its Command runs, and a crash can repeat that Command once; `muster bridge check` is the one sender
  outside delivery; a deep-link template is an `http` or `https` URL; CI fails on a breaking change of the
  specification
- Amended: 2026-10-16 — after the second review of the Bridge stories (journal D335): lint 3 checks calls of the
  delivery adapter's methods, not the protocol client, so `muster bridge check` needs no lint entry; CI fails on a
  breaking change of the specification once it is published on the documentation site, not before
- Amended: 2026-10-17 — bridge libraries (journal D343): libraries for Go and Python, in repositories of their own
  under MIT without a CLA, do the protocol's part of a bridge; the example bridge moves into the Go library's
  repository and is built on it; each library keeps a copy of the specification that its CI compares with Muster's and
  runs `muster bridge check` from Muster's development image; Muster's end-to-end test runs a released image of the
  example; the libraries are 0.x until Muster's first release, then follow the protocol's major version
- Amended: 2026-10-18 — a Bridge Connection can be checked before it is saved (journal D353): a token typed in the
  same request may go to the base URL typed with it, the stored token still only to the saved base URL, and a changed
  base URL without a typed token gets a dry probe, `GET <base>/v1/info` without the token, that passes on `401` with a
  problem
- Amended: 2026-10-21 — library versions (journal D395): the libraries stay `0.x` until Muster 1.0 instead of moving to
  `1.x` with Muster's first release; every library release speaks the protocol versions Muster supports at that time,
  today protocol version 1, and a breaking change of the protocol is a new protocol version, not a new major version of
  a library
- Amended: 2026-10-26 — for L2 (journal D479, D505, D516, D517, D532, D542): `info` declares the bridge's roles,
  `messenger` by default; a bridge in the role `gateway` is a Notification gateway, implements `info`,
  `POST /v1/notifications`, the answer to the person and the event `notification_acknowledged`, and on its own cannot be
  a Destination; a bridge may declare group mentions in `info`, which Muster sends as tokens the bridge renders; Muster
  drops its copy of `info` when a bridge answers again after being unreachable, and the Leader re-reads every bridge's
  `info` once its copy is older than `bridge.info_max_age`

## Context

Muster speaks to two messengers natively, Mattermost and Telegram, and to anything else only through outgoing webhooks,
which send messages but cannot carry button presses, Thread messages or Account links back. Many teams use other
messengers: hosted chat services, self-hosted servers, and corporate messengers whose bot API is published but whose
servers outsiders cannot run. A native adapter in Muster for each of them is not realistic: the maintainers cannot test
against servers they cannot reach, every quirk would need a Muster release, and a team whose messenger is internal
could only keep its adapter in a fork, which the AGPL then obliges it to publish.

What Muster needs from a messenger is small and the same everywhere (ADR-0005, ADR-0013): post a Root message, edit
it, reply in its Thread, check that a channel is usable without posting, carry signed button data, report who pressed,
send a direct message, and answer a person. A survey of the bot APIs of eleven widely used messengers, from their
published documentation and source code, shows how they differ in the details that matter for that contract:

- Every one of them lets a bot edit its own message, but some only within a window — ten minutes by default in one,
  a setting of the workspace in others.
- Only a few accept an idempotency key when posting; in the rest, a retried send is a second message.
- A Thread is the id of the parent post in some, a separate object created by a call in others, a topic or an
  address in the rest; replying "in the Thread of message X" means different calls everywhere.
- A press must be confirmed to the messenger within 3 to 30 seconds, or the person sees an error.
- Several have no buttons at all, or none an external service can use.
- A deleted message shows up as an event in most, but in some only as an error on the next edit, and the error says
  different things: a `403` in one, a generic `400` in another, a successful read with a deletion date in a third.
- Errors, rate limits, private answers, mentions and user lookups each have their own shape.

Muster also already uses two ways to receive events from a messenger: Telegram offers long polling or a webhook, and
installations without an address the messenger can reach choose polling.

## Decision

**A Bridge is a Connection type.** Muster talks to a separate service, the bridge, over the **Bridge protocol**, an
HTTP protocol specified in [`api/bridge.yaml`](../../api/bridge.yaml) (OpenAPI 3.1); the bridge talks to its
messenger. A Bridge Connection has the bridge's base URL, a token, an event delivery mode, a proxy and a limiter; a
Bridge Destination names one channel by the id its messenger uses. Muster ships no messenger-specific code for bridged
messengers: everything a messenger does differently is the bridge's job, and the protocol says only what Muster needs.
Mattermost and Telegram stay native.

**Versioning.** Every path is under `/v1/`. `GET /v1/info` returns `protocol_version` (`"1"`), the bridge's name and
version, the messenger's name, the optional features the bridge offers and its limits. Additive changes — a new optional
field or feature — keep version `1`; a change that breaks existing bridges is a new major path, and CI compares every
change of `api/bridge.yaml` with its base and fails on a breaking one. That check starts when the protocol reference is
published on the documentation site: until then nothing is released, no bridge depends on the protocol, and the stories
that implement it for the first time may still correct it. Muster refuses a Connection whose bridge speaks no version it
knows, and re-reads `info` on every Connection check and when its copy is older than a few minutes. From L2 it also
drops its copy when a bridge answers again after being unreachable, and the Leader re-reads the `info` of every Bridge
Connection once its copy is older than `bridge.info_max_age`, so that a Connection's page is current without deliveries;
Muster does not re-read `info` and retry a call that a bridge refused for a limit; the next scheduled read picks up new
limits. The guide for bridge authors says when Muster reads `info` (journal D479).

**What every bridge in the role `messenger` implements**: `info`; posting a message to a channel; editing it; replying
in its Thread; checking a channel without posting (whether the bot can post and edit there); answering the person behind
an event; and inbound events in at least one of the two delivery modes below, Account link requests at least. **What a
bridge may offer**, declared in `info`: buttons (with their count limits, the `data_max_bytes` they carry — at least 64
— and the style hints its messenger can show); forwarding people's Thread messages to Muster (all of them, or only those
that mention the bot); direct messages from the bot, and whether the bot may start a direct chat; private answers
(ephemeral messages or pop-ups); native mentions; Quiet messages; a status colour; deleting its own messages, unused by
L1; and `message_deleted` events. `info` also declares `max_body_length`, `edit_max_age_seconds` (`null` when edits
never expire), the kind of handle it looks people up by (`email`, `login`, `username`, `user_id` or `none`), the event
delivery modes it offers, an optional deep-link template for Account links, and optional rate hints.

**Content.** Muster sends a JSON envelope and lets the bridge render it in its messenger's markup: the kind of message,
the status with its colour and emoji, the title (`#N` and the Alert Group's title), the link to the Alert Group page,
a body in CommonMark, the buttons, the Mentions and whether the message is Loud. The body holds what C-12.FR-1 puts
between the heading and the buttons — the environment, labels, summary, Alerts, links, notices and footer — rendered
from the Route's templates in CommonMark, with alert data escaped for CommonMark (output safety stays Muster's, as in
ADR-0005). Muster shortens the body below `max_body_length`, counted in code points; the bridge makes the final cut
safely for its markup and says when it did.

**Buttons.** Each button has a label, a style hint — `primary`, `danger`, `neutral` or `success`, which a bridge maps to
the closest style its messenger has or ignores — and opaque data of at most 64 ASCII bytes: the action id, the key id
and the signature with the button-signature key (ADR-0011). The bridge returns the data unchanged with the press, and
Muster verifies it and checks that it belongs to the pressed message, as for Mattermost and Telegram.

**Mentions** name linked people, as `{external_id, display_name}` in the envelope, each placed in the body by a marker
that reads as the plain name where the bridge cannot mention natively. From L2 a bridge may also declare in `info` the
group mentions its messenger has; Muster then sends a Team mention (ADR-0018) as a group token that the bridge renders
itself (journal D516). Mentions never appear in an edit (ADR-0005).

**Idempotency.** Every request that creates a message carries an `Idempotency-Key`; the bridge stores the key with its
successful result and answers a repeated key with that result, marked as replayed, without posting again — whatever
the repeated request carries — and passes the key to its messenger where the messenger has a mechanism of its own. A
failed request stores nothing, and a repeated key whose first request is still running is `transient`. A retry of a
Publication reuses its key, so a Publication that was cut off by a crash or a timeout is not posted twice; because the
retry may carry a newer Desired state than the lost request, a replayed Publication is followed by an edit to the
current state. Muster still records a possible duplicate (ADR-0005) when a retry after a started Publication gets an
answer that is not a replay: the bridge lost the key, and a second Root message may exist. Keys are scoped per token:
the same key under another Connection's token is another request, so two Muster installations, or two Connections, on
one bridge never see each other's results.

**Threads.** "Reply in the Thread of message X" is one operation. The bridge creates the Thread if its messenger needs
that and returns an opaque `thread_ref`; Muster stores it with the Root message and sends it with every later reply, so
a bridge needs no store of its own to map posts to Threads.

**Inbound events, two modes chosen per Connection**, among those the bridge offers: **poll** — the Leader calls
`GET /v1/events` with the last cursor and a wait, handles the batch, then stores the next cursor; **push** — the bridge
posts each event to Muster's callback at `MUSTER_INGEST_URL`, signed as Standard Webhooks do (`webhook-id`,
`webhook-timestamp`, `webhook-signature`, HMAC-SHA256 with the Connection's push secret). Every event has a stable id;
Muster handles each id once and acknowledges only what it handled, so delivery is at least once in both modes. It claims
the id under a transaction-scoped advisory lock — so two copies of one event, from overlapping polls or retried pushes,
run one after the other — runs the event's Command through the dispatcher in the Command's own transaction, then records
the id and answers. A crash between the Command and the record leaves the id unrecorded: the event comes again and its
Command runs a second time, which the dispatcher's preconditions turn into a refusal or a no-op, so the worst case is a
second answer to the person. Recording the id inside the Command's transaction would need a hook in the dispatcher for
one transport; the claim keeps the dispatcher as it is. The event types are a press, a Thread message, an Account link
request, a deleted message and, from a Notification gateway, `notification_acknowledged`; Muster handles every type it
knows whatever `info` declares, so an event sent just after a bridge switched a feature on is not lost. Each Connection
has a token of its own, and a bridge that serves several Connections keeps their events apart by token; Muster ignores,
without answering, an event about a message it did not post through that Connection.

**Trust.** Muster authenticates to the bridge with the Connection's token as a bearer token; the bridge authenticates
pushed events with the signature above; button data is signed by Muster. The token and the push secret are Secrets,
encrypted under the Keyring and write-only in the API (ADR-0011), and are never logged.

**Errors** are RFC 9457 problems with a `class` the bridge chooses, because only the bridge knows its messenger's codes:
`transient` (Muster backs off), `rate_limited` with `Retry-After` (Muster waits exactly that long, for the channel or,
when the problem says so, for the whole bot), `gone` (the message no longer exists: the deleted Root message flow of
ADR-0005), `forbidden` (the channel is missing, the bot was removed or lacks a right: the Destination becomes Broken)
and `invalid` (the request cannot succeed as sent: Not delivered). A missing channel is `forbidden`, never `gone`, so
that Muster does not republish into a chat that no longer exists. A `Retry-After` longer than a bounded maximum, or a
`429` without one, counts as `transient`, so a bridge that asks for a day's pause makes the Destination Broken instead
of stalling it unseen. A problem without a class, or an answer that is not a problem, is classified by the bridge
protocol's own mapping, not by ADR-0005's mapping of outgoing webhook answers, whose `404` and `410` make a Destination
Broken: `401` and `403` are `forbidden`; `408` is `transient`; `429`, and `503` with `Retry-After`, are `rate_limited`;
other `5xx`, timeouts and network errors are `transient`; `404`, `410`, every other answer and a redirect are `invalid`.
Muster never concludes from a status code that a message is `gone`: only the bridge knows whether a `404` means a
deleted message, a missing channel or a wrong path.

**Presses.** The bridge confirms a press to its messenger itself, within 3 seconds, before Muster sees it, and then
reports the press as an event. Muster dispatches the Command with the Transport `bridge` and, when the person must read
something — a refusal, a failure, "not linked" — answers through a separate call naming the event. The bridge shows
the answer to that person privately when its messenger allows it, otherwise in the Thread or the direct chat the event
came from. Answers go through the interactive path and never touch a Root message (ADR-0005).

**Text commands.** Where a bridge forwards Thread messages, a reply in a Thread whose whole text is a command — `ack`,
`unack`, `resolve`, `snooze 4h`, `unsnooze`, `still on it`, or a Russian synonym — is a Command for that Alert Group by
the writer's Account link, through the same dispatcher, answered "Done: …" or with the refusal in the Thread. Any other
text is ignored, and so are Text commands and presses older than a bounded age, such as those a bridge kept while Muster
was down. Without buttons, the Root message keeps its link to Muster and names the commands. Text commands exist only
for Bridges: Mattermost and Telegram have buttons.

**Account links** follow ADR-0013 — started only in the signed-in web session, the secret travelling from that session
to the messenger account — with the Identity space of one Bridge Connection. Every bridge supports the token: the
profile shows a single-use token, and a link built from the bridge's deep-link template when it has one — an `http` or
`https` URL, checked like every link Muster shows; the person
sends the token to the bot, and the bridge reports it as an Account link request with the sender's id and name. A
bridge that can start direct messages and look people up by a handle also offers the code: the person enters their
handle, labelled by the kind the bridge declares; Muster looks it up through the bridge and sends a code by direct
message, which the person types into the profile. The link keeps the account's external id and display name, which
Mentions need.

**Edit windows.** Past `edit_max_age_seconds`, less a safety margin, Muster no longer edits a Root message. When its
status changes after that, Muster publishes a new Root message Quietly, replies in the old Thread that the current
message is elsewhere, with a link to it, and continues the Thread under the new message. Other changes — Alerts,
counters — do not republish: they reach the Thread as the Loud/Quiet table says, and the old Root message shows the
last status until the next change of status. The old Root message keeps working: a press on its buttons and a Text
command in its Thread act on the Alert Group as before. An edit refused as `gone` with the reason
`edit_window_closed` — a window shorter than the bridge declared — is handled the same way.

**Roles (L2).** `info` declares the bridge's roles: `messenger`, the default, which is everything above, and `gateway`,
which makes the bridge a Notification gateway (ADR-0018) that reaches people outside messengers — by phone call or SMS,
for example. A bridge may have both. A gateway implements `info`; `POST /v1/notifications`, which takes the person's
contact, a text, signed action data like a button's and an `Idempotency-Key`; answering the person behind an event,
which during a call needs the push mode, so that the answer comes while the person is still on the line; and the event
`notification_acknowledged`, which returns the action data when the person acknowledges. Muster takes the actor from its
own signature on that data, never from the contact, so a gateway needs no Identity space. A Bridge Connection whose only
role is `gateway` cannot be a Destination. Muster checks a gateway through its `info`, and a gateway that keeps failing
becomes Broken and raises an Internal alert. Its events in the poll mode come through Bridge polling, its calls go
through a queue of their own (ADR-0005), and the confirmation code of a contact goes out through the same
`POST /v1/notifications` (ADR-0013). The bridge libraries for Go and Python support the role in releases of their own
(journal D505, D517, D532, D542).

**Placement in Muster.** Bridge delivery implements the delivery adapter interface, so posts, edits and Thread replies
through a bridge are made only by the delivery worker and the interactive path; answers and direct messages are
interactive calls. As for Mattermost and Telegram, lint 3 (ADR-0016) checks the calls of the adapter's methods, not the
protocol client beneath them. The one exception is `muster bridge check`: a person runs it against a bridge they name,
outside any Muster installation and without a database, Desired state or delivery row, so the rule of ADR-0005 that only
delivery and the interactive path send has nothing to protect there. The command posts, edits and replies through the
protocol client directly, which this decision permits; since lint 3 does not check the client, it needs no entry for it.
All requests go through the outbound package (ADR-0015) — the delivery class for deliveries and the Broken probe, the
interactive class for checks started by a person, answers and messages during linking, the background class for polling
— under the outbound address policy and the Connection's proxy, with the stored token sent only to the saved base URL
and a token typed into a check before saving only to the base URL typed with it (ADR-0015). Polling
a bridge joins the closed list of Leader tasks (ADR-0007): it is safe to run twice, because an event polled twice is
handled once by its id.

**Licensing.** The protocol specification, `api/bridge.yaml`, is **licensed MIT**, as an exception to the
AGPL-3.0-only of ADR-0001, so that anyone may generate code from it and keep their bridge closed: a bridge for an
internal messenger often cannot be published. It states its licence in `info.license`, and `NOTICE` names it. The
bridge libraries and the example bridge are MIT too, in repositories of their own (below). A bridge talks to Muster
only over HTTP, so it is a separate program whatever its licence. Contributions to `api/bridge.yaml` fall under the CLA
like any other file of this repository.

**Bridge libraries.** Most of a bridge is the same for every messenger: the HTTP server for the operations of the
protocol with the validation of requests and their limits, the token check, idempotency keys and their replays,
`info`, problems with their classes, the event queue behind `pollEvents`, and signed pushes with their retries. The
project publishes that part as two libraries, so that a bridge author writes only the calls to their messenger — post,
edit, reply in a Thread, check a channel, answer a person and, where the messenger offers them, find a person, send a
direct message and delete — and reports to the library what people do there: presses, Thread messages, deleted
messages and the direct messages that carry an Account link token.

- `muster-io/muster-bridge-go`: the Go module `github.com/muster-io/muster-bridge-go`, package `bridge`, an
  `http.Handler` on the standard library.
- `muster-io/muster-bridge-python`: the PyPI package `muster-bridge`, module `muster_bridge`, for Python 3.11 or newer:
  asynchronous, an ASGI application on Starlette that mounts into any ASGI application, FastAPI included, and runs
  under uvicorn, with pydantic v2 models and httpx for pushes.

Each library has a repository of its own, **licensed MIT without a CLA**: neither is part of Muster's binary, image or
chart, the project has no reason to offer MIT code under other terms, and a person who improves the part every bridge
copies should not have to sign an agreement for it. Separate repositories keep each language's tooling, release
cadence and package registry apart from Muster's, and let a library be released when the protocol or the library
changes, not when Muster does. The **example bridge** — a small chat server with its own web page that offers every
feature of the protocol — lives in the Go library's repository and is built on it, so that it shows the library in
use; its image `ghcr.io/muster-io/muster-example-bridge` is published from there.

**The protocol reaches the libraries as a copy.** Each library keeps a copy of `api/bridge.yaml` and generates its
types from it, and its CI fails, on every pull request and on a daily schedule, when the copy differs from the one on
Muster's `master`; the fix is a pull request that takes the new copy and regenerates. The copy check makes every change
of the protocol visible in each library, and the compatibility check of `api/bridge.yaml` above keeps such changes
additive once the protocol is published. Each library's CI also runs `muster bridge check` from Muster's development
image, `ghcr.io/muster-io/muster:edge`, against a bridge built on it, so a library that the newest check fails shows
it at once. In the other direction, Muster's end-to-end tests run a released image of the example bridge, pinned by
digest and updated by Renovate, so a change of Muster that breaks a released library fails Muster's CI.

**Versions.** The libraries stay `0.x` until Muster 1.0, and every library release speaks the protocol versions Muster
supports at that time, today protocol version 1; a breaking change of the protocol is a new protocol version, under a
new major path, not a new major version of a library. Releases come from release-please, as Muster's do: a Go release is
a tag, which the Go module proxy serves; a Python release is published to PyPI by its workflow through trusted
publishing, without a stored token. Renovate, CodeQL, OpenSSF Scorecard and Actions pinned by commit SHA are set up as
in Muster's repository.

**Settled with the maintainer** (journal D322): the specification is MIT like the example; one `answerEvent` call
answers presses, Text commands and Account link requests; the Russian Text command words and `unsnooze` are those of
`bridge.text_commands`; the Bridge limiters default to 10 requests per second per Connection and 30 per minute per
Destination, or half of the rates the bridge declares; an edit refused because the window closed earlier than declared
is `gone` with the `reason` `edit_window_closed`, not a class of its own; and a requested wait longer than
`bridge.retry_after_max` counts as a temporary failure.

**Delivered with the protocol**: the specification, a fake bridge among the fake servers of `muster dev`, a
conformance check `muster bridge check <url>` that runs the protocol against a bridge without a Muster database, and a
"Write a bridge" page on the documentation site; in repositories of their own, the bridge libraries for Go and Python
and the example bridge.

## Consequences

- A team can reach any messenger with a bot API by writing one small service, in any language, without forking Muster
  and without publishing it.
- Muster's core stays free of messenger quirks: only `gone`, `forbidden` and the features in `info` reach it.
- Bridge authors carry real work: confirming presses in time, safe truncation, deciding `gone` and, without a library,
  idempotency keys, the event queue and signed pushes. The bridge libraries take the protocol's part of that work in
  Go and Python; the conformance check and the example bridge make the rest checkable.
- Three repositories move together. A change of the protocol lands in Muster first; each library's copy check then
  fails until the library takes it, and Muster's end-to-end test of the released example shows when Muster breaks a
  released library.
- Contributions to the libraries come under MIT alone, without the CLA, so the project cannot offer them under other
  terms; for code that every bridge copies, nothing is lost.
- A bridge is one more service to run and monitor; when it is down, its Destinations become Broken like any messenger.
- Where a messenger limits edits, a long-lived Alert Group may get several Root messages, one per change of status
  after the window, each linked from the Thread before it.
- Messengers without buttons are usable through text commands and the link to Muster, at the cost of typing.
- Two more Secrets per Bridge Connection — the token and, for push, the push secret — that the Admin also configures in
  the bridge.
- The protocol is a public contract from its first release; breaking it means a new major path, so its first version
  has to be right in the parts every bridge implements.
- Polling adds one more Leader task, paused like Telegram polling while no replica leads.
- From L2 a provider of phone calls or SMS is reached by writing a gateway bridge; Muster ships none.

## Alternatives considered

- **A native adapter per messenger, in the repository or in a fork.** Each needs a Muster release, a test server the
  maintainers can reach and long-term care; an internal messenger's adapter could only live in a fork, which the AGPL
  requires to be published.
- **In-process plugins** (Go plugins, WebAssembly modules, or a child process speaking gRPC). Go plugins must be built
  with the exact toolchain and dependencies of the binary; WebAssembly sandboxes make network access and messenger SDKs
  awkward; a child process ties the bridge to Muster's lifecycle and replicas. All of them blur the licence boundary
  that HTTP keeps clear, and none works across languages as simply as HTTP.
- **Outgoing webhooks only.** Already available for one-way delivery, but presses, Thread messages and Account links
  cannot come back, error classes are guessed from HTTP codes, and every installation writes messenger markup in Go
  templates.
- **A persistent connection (WebSocket or gRPC streaming) for events.** Needs reconnection, heartbeats and replica
  handover logic on both sides; polling and signed pushes cover both network directions with plain HTTP.
- **Only push, or only polling.** Push needs an address of Muster that the bridge can reach; polling adds a Leader loop
  and some latency. Offering both, as Telegram does, lets each installation choose.
- **Muster renders the messenger's markup**, with templates per bridge. Moves messenger knowledge back into Muster and
  into every Route; CommonMark is the common ground every bridge can translate.
- **Muster classifies errors by HTTP status.** Messengers answer a deleted message with `403`, `400` or `200`; only the
  bridge can tell. A missing channel treated as `gone` would republish forever.
- **When an edit window closes, stop editing and post changes only in the Thread**, or report it as a new error class.
  The Root message would keep an outdated status for good; republishing on a change of status keeps the part people act
  on current while bounding the number of new messages.
- **Answers to presses as Thread replies sent by Muster.** Thread replies belong to delivery and its Desired state
  (ADR-0005); one interactive answer call that the bridge places privately or in the Thread keeps that rule.
- **Linking by matching email addresses.** Rejected in ADR-0013 for the same reasons; the handle lookup only finds the
  account that receives the code.
- **Text commands for every messenger.** Mattermost and Telegram have buttons; commands are kept where they are needed.
- **The protocol and the example under the AGPL.** A company could then hesitate to derive a closed bridge from them;
  MIT removes the question for the parts a bridge author copies.
- **The libraries and the example in Muster's repository**, as modules of their own. Contributions would need the CLA
  and Muster's toolchain and CI, a library would be released with Muster rather than with the protocol, a Python
  package would live in a Go repository, and Muster's licence checks would need exceptions for every library file.
- **Libraries generated from the specification alone**, with an OpenAPI generator. A generator gives types and a
  server stub, not the behaviour — idempotency, the event queue, signed pushes, error classes — that is most of a
  bridge author's work; the libraries generate their types and write the behaviour once.
- **Libraries that read the specification from Muster when they are built.** A build would depend on the network and on
  whatever `master` holds at that moment; a checked-in copy with a check turns each change into a reviewed pull
  request.
- **Libraries for more languages from the start.** Go and Python cover the bridges the maintainers expect; a bridge in
  any other language speaks the protocol directly, as "Write a bridge" describes, and a library for it can follow the
  same pattern later.
- **A Connection type of its own for Notification gateways** (L2), with a second specification. A gateway needs the
  transport, the idempotency, the signed data and the events that a bridge already has; a role keeps one protocol and
  one pair of libraries (journal D505).
