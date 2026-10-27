# C-30. Notification gateways

[L2 index](../L2.md) · Release: 0.2.0 · UI: yes · Depends on: C-10, C-11, C-22, C-23

**Goal.** Reach people outside messengers — by phone call or SMS — through bridges in the role `gateway`, written for
any provider with the bridge libraries: each person adds and confirms their own contact, Muster sends calls through a
queue of their own with retries, and a person who acknowledges in the call acknowledges the Alert Group as themselves,
but only while nobody has it. Muster ships no gateway and never calls or texts anyone by itself (ADR-0017, ADR-0018).

Names in the form `area.setting` refer to rows of [defaults.md](../l1/defaults.md); the lifecycle events are rows of the
[L2 lifecycle event table](../l1/reference.md#lifecycle-events-added-in-l2).

## Scenarios

1. A team writes a bridge for its SMS and voice provider with the Go bridge library, declares the role `gateway` in
   `info` and runs it; the Admin creates a Bridge Connection for it, and its check shows "Notification gateway".
2. Ivan opens his profile, enters his phone number for that gateway and presses "Send code"; the gateway calls or texts
   the code, and Ivan types it in. His contact is confirmed.
3. At 03:25 an Escalation step calls Ivan through the gateway; he presses 1 and the Alert Group is acknowledged by Ivan
   with the Transport `gateway`.
4. Bob presses 1 three minutes after Alice acknowledged the same Alert Group in Mattermost; nothing changes, and the
   call tells him "Already taken by Alice".
5. Three Alert Groups Page Ivan through the gateway within a minute: the call about the first goes at once, and the
   other two wait and go together in one call a minute after it.
6. The provider rejects the bridge's credentials; the gateway's calls fail until the retry budget runs out, the gateway
   becomes Broken and `MusterNotificationGatewayBroken` reaches the admins through another Destination.
7. An installation without a gateway shows, where a call could be chosen, "Unavailable: a Notification gateway is needed
   (a bridge for calls and SMS)" with a link to the documentation.

## Functional requirements

- **C-30.FR-1** A bridge declares its roles in `info`: `messenger`, the default when it declares none, and `gateway`,
  which makes its Bridge Connection a Notification gateway; a bridge may have both. A Bridge Connection whose only role
  is `gateway` cannot be a Destination: creating a Destination for it is refused with `422` and the code `gateway_only`.
  Nor is it offered for Account links, Team mentions or Mention entries, which need a messenger. The Connection page
  and its check show the roles.
- **C-30.FR-2** The Bridge protocol gains, additively in version 1 (ADR-0017): `sendNotification` (`POST
  /v1/notifications`), which takes the person's contact, a plain text, signed action data like a button's (C-22.FR-5)
  and an `Idempotency-Key`; and the event `notification_acknowledged`, which returns that action data when the person
  acknowledges. The action data names the call (FR-4) — a subject of its own kind with the call's `public_id` — so that
  it stays within the 64 bytes of a button's data however many Alert Groups the call is about. A gateway implements
  `info`, `sendNotification`, `answerEvent` — during a call that needs the push mode, so that the answer comes while the
  person is still on the line — and events in at least one delivery mode (C-22.FR-11); Bridge polling takes the events
  of a gateway in the poll mode. `muster bridge check` (C-22.FR-21) checks the role, the fake bridge of `muster dev`
  serves a gateway bot, and the bridge libraries for Go and Python support the role in releases of their own.
- **C-30.FR-3** A person adds, in their profile and from their own web session (C-03.FR-27), one contact for each
  Notification gateway — a phone number or another address the gateway understands, as text — and confirms it: "Send
  code" sends a code (`gateway.contact_code`) through that gateway with `sendNotification`, without action data since
  there is nothing to acknowledge, on the interactive path, since the person waits for it; the person types the code
  back into the same session. Code requests are limited by `gateway.contact_code_requests`. Only a confirmed contact is
  called. An Admin sees and removes the contacts of any User, never sets one, so nobody can tie a contact to another
  person's User (ADR-0013). A contact is personal data, not a Secret: the person, Admins and the gateway read it; the
  Request log masks it (C-23); deleting the User erases it, and the Audit log shows it as `[erased]` in earlier diffs
  (C-03.FR-13). Adding, confirming and removing a contact are recorded in the Audit log.
- **C-30.FR-4** A call — from an Escalation step (C-32.FR-3) or a Notification rule (C-31.FR-3) — is a row per person
  and gateway, with a `public_id`, the Alert Groups it is about, `next_attempt_at` and a lease, which any replica
  claims; it is sent at least once under an `Idempotency-Key` through the gateway Connection's limiter
  (`connection.bridge.limiter`), calls about critical Alert Groups first. Its failures are classified and retried in the
  delivery client class within `delivery.transient_backoff` and `delivery.transient_budget`, as deliveries are
  (C-11.FR-8). Calls are not reconciled: a call has no Root message and no Desired state (ADR-0005). The first call to a
  person through a gateway goes at once. A Page through the same gateway to the same person that comes due while a call
  to them is waiting, being retried, or within `gateway.combine_window` after the previous call ended — accepted by the
  gateway or Not delivered — joins the next call, which goes once that window has passed, as one call about all of its
  Alert Groups. Right before its first attempt, Muster drops from a call the Alert Groups on which the chain or the rule
  that Paged has stopped — acknowledged, snoozed, resolved or started over — and a call left without any is not sent.
  From its first attempt on, a call keeps its Alert Groups and its text, which its `Idempotency-Key` stands for: it
  takes no more, and when every Alert Group of a call being retried has stopped, its retries end. Mentions are never
  combined.
- **C-30.FR-5** The text of a call names the Alert Groups — number, title and Severity level — and how to acknowledge,
  in the language of the person's profile. Only the gateway sender and the interactive path call `sendNotification`
  (ADR-0016, lint 3).
- **C-30.FR-6** A `notification_acknowledged` event whose action data verifies runs Acknowledge with the Transport
  `gateway` as the person the call was for — taken from Muster's signature and the call it names, never from the contact
  (ADR-0013) — for each Alert Group of that call, under a precondition of its own (ADR-0004): a firing or snoozed Alert
  Group becomes acknowledged; one that someone owns, the same person included, stays as it is, and the gateway's answer
  to the person says "Already taken by {name}"; a resolved one stays resolved, answered "Already resolved". A Takeover
  is never made through a gateway. The person's Role and state apply as for a press (C-18.FR-8). An Acknowledge that
  runs is recorded in the Audit log and the Timeline like any Acknowledge; one that its precondition refuses changes
  nothing and is logged as a refusal, like any refused Command (C-10.AC-4). The answer goes through `answerEvent` on the
  interactive path. Events are handled once per id (C-22.FR-11).
- **C-30.FR-7** A call the person does not acknowledge lets the Escalation chain and the Notification rules go on. A
  call that ends Not delivered — refused, an unknown answer, or the retry budget used up — records the lifecycle event
  `page_not_delivered` naming the person and the gateway.
- **C-30.FR-8** A Notification gateway that keeps failing — a Fatal answer, or a call that used up its retry budget —
  becomes Broken: its Connection page shows it with the reason, the Leader's gauge
  `muster_notification_gateway_broken{connection}` is 1, and the Internal alert `MusterNotificationGatewayBroken`
  ([reference.md](../l1/reference.md#internal-alerts-added-in-l2)) is raised, with a chart rule and a runbook page of
  the same name; the suggested Route for Internal alerts (C-08.FR-11) gains `connection` in its Group key, so that each
  Broken gateway has its own Alert Group, and the upgrade notes of 0.2.0 tell an installation that accepted it in 0.1 to
  add `connection` too. Muster probes it through `info` every `delivery.broken_probe_interval`, and a successful probe
  or check ends the state. Calls that come due while it is Broken end Not delivered and are not sent after it recovers,
  as Pages that a Broken Destination could not deliver are not (ADR-0018). A gateway's failures never make a Destination
  Broken, nor do a Destination's make its gateway Broken, even for a bridge with both roles.
- **C-30.FR-9** Escalation steps and Notification rule actions that call through a gateway are offered only while a
  Bridge Connection in the role `gateway` exists; otherwise the UI shows "Unavailable: a Notification gateway is needed
  (a bridge for calls and SMS)" with a link to the documentation, and a step or action whose gateway was deleted is
  skipped with its reason (C-32.FR-14).
- **C-30.FR-10** The Request log (C-23) records the requests to a gateway like those to any bridge, with contacts
  masked. `gateway` joins the `transport` values of `muster_commands_total`, and calls are counted by
  `muster_gateway_calls_total{connection,outcome}`; the stories register their log events.
- **C-30.FR-11** The documentation covers writing a gateway bridge — the role, `sendNotification`, the answer during a
  call in the push mode, `notification_acknowledged` — setting it up in Muster, confirming a contact, and that until the
  push of Mentions in Threads is verified ([facts, Pending](../../facts.md#pending)), only a Notification gateway is
  reliable at night.

## UI

Connections → Bridge: the roles from `info`, "Notification gateway" with its health and Broken banner. Profile →
"Contact for calls and SMS": "Muster itself does not call, send SMS or check phone numbers: a Notification gateway (a
bridge for calls and SMS) does, and it does not depend on a messenger. An Admin sees the contact and can remove it, but
not change it." — the phone field, "Send code", "Code sent through the Notification gateway to {contact}.", the code
field, "Confirm" and "Another number"; a confirmed contact with "calls and SMS", "confirmed" and "Remove"; the note
"From a call you can acknowledge an Alert Group only while nobody has it; otherwise the call answers 'Already taken by
{name}'." Without a gateway: "Unavailable: a Notification gateway is needed (a bridge for calls and SMS)", "How to
connect a Notification gateway" and, for Admins, "Connect a bridge". Users → a User's page: their contacts, with
"Remove".

## API surface

The roles in the read of a Bridge Connection; `me/gateway-contacts` (list, create, send the code, confirm, delete);
`users/{id}/gateway-contacts` (list, delete); `gateway` in the `Transport` enum; in `api/bridge.yaml`, the roles of
`info`, `sendNotification` and the event `notification_acknowledged`.

## Acceptance

Checked with the fake bridge in the role `gateway` and a virtual clock; until C-31 and C-32 make calls, the tests queue
them through the call sender directly.

- **C-30.AC-1** A Bridge Connection whose `info` declares only `gateway` is shown as a Notification gateway and refuses
  a Destination with `422` `gateway_only`; one with both roles takes a Destination.
- **C-30.AC-2** A contact is confirmed only with the code the fake gateway received through `sendNotification`; a wrong
  code is refused with the attempts left; an Admin's request to set a contact for another User gets `403`, and the Admin
  removes it; the Request log shows the contact masked; deleting the User shows it as `[erased]` in the Audit log.
- **C-30.AC-3** A call that the fake gateway answers `transient` is retried with the same `Idempotency-Key` and the same
  text until it is accepted, and its retries end once all its Alert Groups are acknowledged; of three Pages to one
  person within `gateway.combine_window`, the first reaches the fake gateway at once and the other two as one call when
  the window after it has passed — and when the second Alert Group is acknowledged meanwhile, that call is about the
  third only; a call whose Alert Groups were all acknowledged before it went is not sent; after a call that ends Not
  delivered, the next one goes once the window after it has passed. The action data of a call about two Alert Groups is
  at most 64 bytes, and acknowledging it acknowledges both.
- **C-30.AC-4** `notification_acknowledged` for a firing Alert Group acknowledges it as the person the call was for,
  with the Transport `gateway` in the Timeline and the Audit log; for an Alert Group another User owns it changes
  nothing, is logged as a refusal, and the gateway's answer says "Already taken by" that User; replaying the event
  changes nothing.
- **C-30.AC-5** A gateway that answers a call with `401` becomes Broken, raises `MusterNotificationGatewayBroken` and
  sets its gauge; the Destinations of the same bridge stay healthy; a successful probe resolves the alert.
- **C-30.AC-6** In a real browser, without a gateway the profile shows the unavailable note and no phone field; with the
  fake gateway, the profile confirms a contact with the code, in English and in Russian.
- **C-30.AC-7** A call that the fake gateway answers with the class `invalid` ends Not delivered and records
  `page_not_delivered` naming the person and the gateway; an Acknowledge through the gateway counts in
  `muster_commands_total{transport="gateway"}`, and the calls in `muster_gateway_calls_total` by outcome.

## Related ADRs

ADR-0004, ADR-0005, ADR-0011, ADR-0013, ADR-0015, ADR-0016, ADR-0017, ADR-0018.

## Depends on

C-10 — Acknowledge and the dispatcher; C-11 — classification, retries and limiters; C-22 — the Bridge protocol and
Bridge Connections; C-23 — the Request log.

## Suggested story split

- **BE** — the role in `info`, `sendNotification` and `notification_acknowledged` in `api/bridge.yaml` and the fake
  bridge, contacts with the code and its limits.
- **BE** — the queue of calls with combining, the Acknowledge of the Transport `gateway`; the calls themselves come from
  C-31 and C-32.
- **BE** — Broken gateways with their Internal alert, chart rule and runbook.
- **FE** — the contact in the profile, the roles and health on the Connection page.
- In `muster-io/muster-bridge-go` and `muster-io/muster-bridge-python` — the role `gateway`.
