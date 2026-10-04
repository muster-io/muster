# C-11. Delivery engine

[L1 index](../L1.md) · Stage: Shadow · UI: none · Depends on: C-10

**Goal.** Keep one current Root message per Alert Group and Destination, deliver Thread replies, respect every limit,
survive failures, restarts and long outages of a messenger, and make phones ring only by the fixed Loud/Quiet table.

Names in the form `area.setting` refer to rows of [defaults.md](defaults.md).

## Scenarios

1. Ten Alerts arrive and someone acknowledges within seconds; the Destination gets one new message and at most one edit.
2. Telegram answers `429` with `retry_after: 30`; Muster waits exactly 30 seconds and then sends the latest state.
3. The bot is removed from a channel. The Destination becomes Broken, its page and its Routes show a warning, and
   `MusterDestinationBroken` reaches the admins through another Destination; a probe notices when the bot is back —
   through the Destination check when nothing is waiting — and the current state is sent.
4. The messenger is unreachable for two hours. After the retry budget runs out, the Destination becomes Broken as
   unavailable. When it recovers, the Alert Groups still open are published or brought up to date; those that started
   and ended during the outage stay in Muster only.
5. A cluster goes down and one Route produces 300 new Alert Groups in a minute. Each of its Destinations gets one Storm
   summary; Urgent Alert Groups are still posted individually, first. After the calm period the summary shows its final
   state and the remaining open Alert Groups are posted gradually and quietly.
6. Someone deletes a Root message of an open Alert Group; it is posted again once, quietly, with a note.
7. Muster crashes right after the messenger accepted a new message; after the restart the message is posted again and
   the Alert Group's Timeline shows a possible duplicate.

## Functional requirements

- **C-11.FR-1** For each Alert Group and Destination, Muster stores the Desired state of the Root message (rendered
  text, buttons, hash). A delivery worker brings the actual message to the latest Desired state with one call; changes
  made while a call is pending collapse into the next one; after any wait the latest state is sent, never an outdated
  one; a "not modified" answer counts as success.
- **C-11.FR-2** Only the delivery worker sends and edits Root messages and Thread replies. Answers to button presses,
  ephemeral messages, account-linking messages, connection and Destination checks that a person starts, and test
  messages go through the interactive path, which calls the messenger at once, takes limiter tokens ahead of queued
  deliveries, never queues, and fails within `delivery.interactive_budget` with a message to the person when no token is
  free. The interactive path serves only calls a person waits for; the Broken probe never uses it (FR-9). Through the API
  such a call answers `503` with `Retry-After`; a Destination test records the step with the error class `limited`
  (C-16).
- **C-11.FR-3** Each Destination and each Connection has a limiter; a delivery waits for both (outgoing webhooks have
  only the Destination's). Every call takes one token, a send and an edit alike, since Telegram counts both against one
  budget per chat (C-14.FR-2). The limiters are shared by all replicas: their token buckets live in PostgreSQL, so
  adding a replica never raises the rate. Defaults are set per type (C-13 to C-15), at half of the messenger's
  documented limit where one exists. Urgent Alert Groups go first in the queue but never bypass a limiter. Rate-limit
  pressure delays updates; it never drops them.
- **C-11.FR-4** New Alerts are sent to the Thread with a leading and a trailing edge: the first immediately, opening the
  Thread batching window (`route.thread_batching_window`, a Route policy field added here); everything else that arrives
  within the window as one reply when it closes. After a quiet period longer than the window, the next one is again
  immediate.
- **C-11.FR-5** A Thread reply about more new Alerts than `delivery.thread_alerts_listed` lists that many and "…and K
  more — open in Muster".
- **C-11.FR-6** A Storm is counted per Route: when a Route creates more than its Storm threshold
  (`route.storm_threshold`, a Route policy field added here) of new Alert Groups within one minute, each of its
  Destinations receives one Storm summary for that Route, kept current, and non-urgent Alert Groups are not posted
  individually. Urgent Alert Groups are posted individually, first in the queue. When the rate has stayed below the
  threshold for `delivery.storm_calm_period`, the summary gets its final state, Alert Groups still open are posted
  gradually and Quietly within the limits, and those already resolved are not posted. The summary is Loud only at its
  first publication and has no ack timeout; an Alert Group's ack timeout starts at its own Publication.
  `muster_storm_active{route}` shows the state. Storms apply to messengers and to the template mode of outgoing
  webhooks; the events mode is not subject to them and receives every event (C-15.FR-2).
- **C-11.FR-7** Loudness follows the lifecycle event tables (C-09.FR-22) and the
  [Loud/Quiet table](reference.md#loud-and-quiet) of delivery events. A Loud event is always a new message — a Root
  message or a Thread reply; an edit is always Quiet, since an edit notifies nobody in either messenger
  ([F-012](../../facts.md#notifications), [F-029](../../facts.md#edits-and-notifications)). How each messenger makes a
  new message Quiet is in C-13 and C-14.
- **C-11.FR-8** Adapters classify every failure: `RetryAfter` — wait exactly as asked, not counted as an attempt;
  `Transient` — exponential backoff with jitter (`delivery.transient_backoff`) within the attempt and time budgets of
  `delivery.transient_budget`, and when either runs out the Destination becomes Broken as unavailable; `Fatal` — the
  Destination becomes Broken at once; an unknown response — not retried, that delivery ends as Not delivered, and the
  Destination is not Broken. A messenger that rejects the markup gets the same text without markup, counted and recorded
  as a delivery event (FR-21).
- **C-11.FR-9** A Broken Destination shows a warning with its reason on itself and on its Routes, exports
  `muster_destination_broken{destination} 1` and raises the Internal alert `MusterDestinationBroken`. Its deliveries
  wait instead of failing. Once per `delivery.broken_probe_interval` Muster probes it: with a delivery waiting, it
  attempts the oldest one; with nothing waiting, it runs the Destination check that the adapter offers (C-13.FR-10,
  C-14.FR-14) — and for a type without one, the outgoing webhook, the next delivery that comes due is the probe and is
  attempted at once. The probe, the Destination check included, runs on any replica in the
  delivery client class (C-02.FR-20), never on the interactive path. A successful probe — or a successful Destination
  check run by an Admin (C-13.FR-10, C-14.FR-14) or Destination test (C-16.FR-7) — ends the Broken state and resolves
  the Internal alert; a failed probe keeps it, with the new error as the reason.
- **C-11.FR-10** A delivery ends delivered, or Not delivered when it got an unknown response or its template failed
  (C-15.FR-7); Not delivered is visible on the Alert Group page and in the Timeline with the error. A later change of
  the Desired state starts a new delivery.
- **C-11.FR-11** An Alert Group resolved while its first Publication waited — for a `RetryAfter`, or for `Transient`
  retries within the budget — is still posted, Quietly, with the note "Delivered late: started HH:MM, resolved HH:MM
  while this Destination was unavailable" (ADR-0005). This never applies to Alert Groups covered by a Storm summary or
  to a Destination that became Broken before the late Publication succeeded (FR-19).
- **C-11.FR-12** Before a Publication, the worker records that it started. A retry after that record posts again — in
  Mattermost and in Telegram alike — and a possible duplicate is recorded as a delivery event (FR-21). A Publication is
  never skipped because of the record, and Muster never searches the messenger for an earlier copy.
- **C-11.FR-13** When the messenger reports that a Root message no longer exists, an open Alert Group is posted again
  once, Quietly, with "The previous message was deleted at HH:MM", and its Thread starts over; if that message is
  deleted too, the pair is marked "deleted in the messenger" and left alone. For a resolved Alert Group only the mark is
  recorded.
- **C-11.FR-14** A Destination added to a Route receives the Route's open Alert Groups Quietly, within its limits. A
  Destination removed from a Route — or deleted — receives one final edit of each open Root message — "No longer updated
  here; current state in Muster: {link}" — and nothing after that. Deleting a Destination is a soft delete: Muster makes
  those final edits, then wipes the Destination's secrets, and keeps the Destination itself for history. A deleted
  Destination no longer counts as using its Connection (C-13.FR-6); deleting that Connection abandons the final edits
  still pending for its deleted Destinations, which end as Not delivered. An outgoing webhook in events mode has no
  Root message: deleting it abandons its queued events, which end as Not delivered, sends no final event and wipes its
  secrets at once (C-15.FR-12).
- **C-11.FR-15** Delivery rows carry `next_attempt_at` and a lease, are claimed by any replica, and make HTTP calls
  outside database transactions through the outbound HTTP package.
- **C-11.FR-16** The delivery state of each Alert Group and Destination — pending, delivered (with a link to the message
  where the messenger provides one), waiting for a Broken Destination, Not delivered, deleted in the messenger, Thread
  not attached, withheld (never posted there: the Alert Group resolved while a Storm summary stood for it or while the
  Destination was Broken), and retired (no longer updated there after its final edit, because the Destination left the
  Route or was deleted) — is available through the API; the Alert Group page shows it from C-13 on.
- **C-11.FR-17** Delivery exports the metrics listed for C-11 in the [metrics
  catalogue](reference.md#metrics-catalogue); `muster_delivery_latency_seconds{destination}` measures from webhook
  receipt to the messenger API call and is the service-level indicator of NFR-2.
- **C-11.FR-18** A Destination has a name, a type, its limiter, its Routes (C-08.FR-1) and its health — healthy, or
  Broken since a time with a reason; each type capability adds its own fields.
  `muster_destination_info{destination,name}` is exported for each.
- **C-11.FR-19** When a Broken Destination recovers, reconciliation sends only the current state: open Alert Groups not
  yet published there are published within the limits, Urgent first — Loud if they are firing at that moment, Quiet
  otherwise; published Root messages are edited to their current state; Thread replies that came due while it was
  Broken are not sent; and Alert Groups that resolved while it was Broken without ever being published there are not
  published — they stay in the Timeline and the UI. Outgoing webhook events are the exception (C-15.FR-2).
- **C-11.FR-20** Lifecycle events become Desired-state changes, Thread replies and new messages as the last column of the
  lifecycle event tables says (C-09.FR-22, C-10.FR-15, C-17.FR-11), with the loudness of each row and the Mentions each
  Destination configures for it (C-12.FR-8); delivery's own events follow the
  [Loud/Quiet table](reference.md#loud-and-quiet).
- **C-11.FR-21** Delivery events — what happens while delivering to a Destination, such as a Publication, the rows of
  the [Loud/Quiet table](reference.md#loud-and-quiet), a possible duplicate (FR-12), rejected markup (FR-8), Not
  delivered with its error (FR-10), a late Publication (FR-11), a deleted Root message (FR-13), a Thread not attached
  (C-14.FR-3) and the Destination becoming Broken or healthy again — are recorded by delivery in a delivery-events table
  of its own, never in the Alert Group tables, which only the `groups` package writes (ADR-0016). Each names its
  Destination and, when it concerns one, its Alert Group. The Timeline of an Alert Group merges the delivery events
  about it with its Timeline entries by time, as the kind `delivery` (C-09.FR-11, C-09.FR-14); they are not lifecycle
  events and reach neither delivery nor outgoing webhook events (C-09.FR-22). They are kept for
  `retention.alert_details`, like Timeline entries (C-09.FR-16).

## UI

None; the delivery states and Destination health appear in the UI from C-13 on.

## API surface

`alert-groups/{id}/deliveries` (list delivery state per Destination); `destinations` (list all types with health; read;
delete — always allowed, the Destination leaves its Routes, open Root messages get the final edit of FR-14, and then its
secrets are wiped);
the type-specific create and update operations come with C-13 to C-15.

## Acceptance

Each statement is checked through the adapter interface with a recording test adapter that can be told to answer with
every error class, and a virtual clock; messenger-specific checks are in C-13 to C-15.

- **C-11.AC-1** Ten Alerts and an Acknowledge within 2 seconds produce one Root message and at most one edit.
- **C-11.AC-2** A `RetryAfter` of 7 seconds delays the next call to the same Destination by 7 to 8 seconds and is not
  counted as an attempt.
- **C-11.AC-3** 30 new Alert Groups within a minute on a Route with threshold 20: the first 20 get their own Root
  messages; the 21st starts the Storm, so each Destination gets one Storm summary, and of the last 10 only the Urgent
  ones get Root messages at once; after the calm period those of the last 10 still open arrive as Quiet new messages,
  and the resolved ones not at all.
- **C-11.AC-4** Deleting a Root message leads to exactly one Quiet republication; deleting that one leads to none.
- **C-11.AC-5** Killing the worker between the adapter's acceptance and the record produces a second message and a
  "possible duplicate" delivery event in the Alert Group's Timeline.
- **C-11.AC-6** `Transient` failures until the budget runs out make the Destination Broken as unavailable, raise
  `MusterDestinationBroken` and leave the delivery waiting — not Not delivered.
- **C-11.AC-7** While a Destination is Broken: Alert Group A starts and keeps firing; B, published before, is
  acknowledged and gets a new Alert; C starts and resolves. After a successful probe, A is published as a Loud new
  message, B's Root message is edited once to its current state with no Thread reply, and C is not published.
- **C-11.AC-8** An unknown response ends that delivery as Not delivered with the error in the Timeline, and the
  Destination stays healthy.
- **C-11.AC-9** An Alert Group resolved while its first Publication waited for a `RetryAfter` is published Quietly with
  the late note.
- **C-11.AC-10** Every row of the lifecycle event tables and of the Loud/Quiet table of delivery events produces a new
  message, an edit or nothing, Loud or Quiet, as the row says (a table-driven test).
- **C-11.AC-11** With a Destination Broken and nothing waiting, the probe after `delivery.broken_probe_interval` runs the
  recording test adapter's Destination check: when the check fails, the Destination stays Broken with that error as the
  reason; when it succeeds, the Destination is healthy and `MusterDestinationBroken` is resolved, with no Alert Group
  activity needed.
- **C-11.AC-12** An Alert Group resolved while its first Publication waited for `Transient` retries within the budget is
  published Quietly with the late note once the retry succeeds.
- **C-11.AC-13** A delivery that ends as Not delivered adds one row to the delivery-events table and changes no Alert
  Group table, and `alert-groups/{id}/timeline` returns it as the kind `delivery`, ordered by time among the Alert
  Group's Timeline entries.
- **C-11.AC-14** With a Destination limiter of N calls per minute, N calls in any mix of sends and edits pass, and the
  next call — send or edit — waits for a token.

## Related ADRs

ADR-0005, ADR-0006, ADR-0007, ADR-0015, ADR-0016.

## Depends on

C-10 — commands, so that acceptance can mix Alert changes and status changes; through it, C-09's lifecycle events.

## Suggested story split

BE only, in two stories: reconciliation, limiters, interactive path and Threads; then error classes, Broken and
recovery, Storms, deleted messages and duplicates.
