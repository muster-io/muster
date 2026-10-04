---
id: S-034
title: Delivery reconciliation, limiters, interactive path and Threads (BE)
capability: C-11
kind: be
layer: L1
depends_on: [S-032]
covers: [C-11.FR-1, C-11.FR-2, C-11.FR-3, C-11.FR-4, C-11.FR-5, C-11.FR-7, C-11.FR-8, C-11.FR-15, C-11.FR-16, C-11.FR-17, C-11.FR-18, C-11.FR-20, C-11.FR-21, C-11.AC-1, C-11.AC-2, C-11.AC-10, C-11.AC-14, C-09.FR-11, C-09.FR-14, C-08.FR-1]
files_touched:
  - internal/delivery/delivery.go
  - internal/delivery/enqueue.go
  - internal/delivery/worker.go
  - internal/delivery/limiter.go
  - internal/delivery/interactive.go
  - internal/delivery/threads.go
  - internal/delivery/events.go
  - internal/delivery/render.go
  - internal/delivery/query.sql
  - internal/delivery/deliverytest/recorder.go
  - internal/delivery/enqueue_test.go
  - internal/delivery/worker_test.go
  - internal/delivery/limiter_test.go
  - internal/delivery/threads_test.go
  - internal/delivery/live_test.go
  - internal/destinations/destinations.go
  - internal/destinations/query.sql
  - internal/destinations/destinations_test.go
  - internal/groups/dispatcher.go
  - internal/groups/read.go
  - internal/groups/query.sql
  - internal/routing/routes.go
  - internal/api/destinations.go
  - internal/api/alertgroups.go
  - internal/api/destinations_test.go
  - internal/archlint/callrules.go
  - internal/leader/tasks.go
  - internal/metrics/catalogue.go
  - internal/logging/events.go
  - internal/runtime/runtime.go
acceptance:
  - "[C-11.FR-1, C-11.AC-1] Ten Alerts in one Snapshot and an Acknowledge within 2 seconds produce, through the recording test adapter, one Publication and at most one edit; changes made while a call is pending collapse into the next call, which carries the latest Desired state."
  - "[C-11.FR-1] A delivery whose actual hash equals the desired hash makes no call, and a \"not modified\" outcome counts as delivered."
  - "[C-11.FR-3, C-11.AC-14] With a Destination limiter of N calls per minute, N calls in any mix of sends and edits pass and the next one — send or edit — waits for a token; a delivery also waits for its Connection's limiter; with two workers on two replicas the adapter still sees at most N calls per minute; Urgent Alert Groups are claimed first but never pass a limiter."
  - "[C-11.FR-8, C-11.AC-2] A `RetryAfter` of 7 seconds delays the next call to the same Destination by 7 to 8 seconds and leaves `attempts` unchanged; a `RetryAfter` scoped to the Connection delays every Destination of that Connection the same way."
  - "[C-11.FR-2] A call on the interactive path takes the next free token ahead of waiting deliveries and never queues; when no token frees within `delivery.interactive_budget` it returns `limited` with the time to wait and sends nothing."
  - "[C-11.FR-4, C-11.FR-5] The first new Alerts of a firing Alert Group reach the Thread at once; new Alerts arriving within `route.thread_batching_window` form one reply when the window closes; after a quiet period longer than the window the next ones are immediate again; a reply about more than `delivery.thread_alerts_listed` new Alerts lists that many and \"…and K more — open in Muster\"."
  - "[C-11.FR-7, C-11.FR-20, C-11.AC-10] Every row of the lifecycle event tables of C-09.FR-22, C-10.FR-15 and C-17.FR-11 except `moved_to_default_route` — rows keyed by the event and its variant, so that `unacknowledged` by a Command and by the system releasing an Owner are two rows — produces a new message, an edit or nothing, Loud or Quiet and with the row's symbolic Mentions, as the row's last column says (a table-driven test; the rows of C-17 are set up directly); a Loud event is always a new message and an edit is always Quiet."
  - "[C-11.FR-15] Delivery rows and Thread replies are claimed with `FOR UPDATE SKIP LOCKED` and a lease by any replica; a row whose lease ran out is claimed again; adapter calls are made outside any database transaction, in the delivery client class."
  - "[C-11.FR-16, C-11.FR-18] `listAlertGroupDeliveries` returns one item per Destination of the Alert Group with its state (`pending` or `delivered`) and `message_url`; `listDestinations` and `getDestination` return Destinations of every type with their health and Routes, filtered by `type`, `health` and `route`; `getRoute` carries its Destinations with their health; `muster_destination_info{destination,name}` is exported for each Destination."
  - "[C-11.FR-21, C-09.FR-11, C-09.FR-14] A Publication records one `publication` row in `delivery_events` and changes no Alert Group table; `getAlertGroupTimeline` merges the delivery events of the Alert Group with its entries by time as the kind `delivery`, and `kind=delivery` returns only them."
  - "[C-11.FR-17] `muster_delivery_attempts_total{destination,kind,outcome}`, `muster_delivery_latency_seconds{destination}` (from the receipt of the Snapshot behind a change to the adapter call) and the Leader gauge `muster_delivery_queue{destination}` are exported; every attempt writes one `delivery_attempt` line."
verify: "make ci test-integration"
operator_attention: false
issue: null
---

# S-034. Delivery reconciliation, limiters, interactive path and Threads (BE)

## Scope

**IN**

- The `delivery` package: the Desired state per Alert Group and Destination, its re-render from the dispatcher, the
  delivery worker that reconciles the actual message with one call, and the adapter interface that the Destination
  types implement.
- Turning lifecycle events into Publications, Root message updates and Thread replies, with their loudness and symbolic
  Mentions (C-11.FR-20).
- Limiters per Destination and per Connection, shared by all replicas through `rate_limit_buckets`; `RetryAfter`.
- The interactive path and its budget.
- Thread replies with the Thread batching window and the list cap.
- The delivery-events table for Publications, and the Timeline merge of delivery events.
- Reading Destinations, their health and Routes, and the delivery state of an Alert Group.
- The recording test adapter that C-11 to C-16 test against.

**OUT**

- `Transient`, `Fatal`, unknown and rejected-markup outcomes, Broken Destinations, probes and recovery, Storms, late
  Publications, possible duplicates, deleted Root messages, Destinations added to or removed from a Route, deleting a
  Destination and the `moved_to_default_route` row (S-035).
- The real message: default layout, built-in texts, templates and buttons (S-036), links and Mention settings (S-037);
  until then a minimal renderer stands in (see Contracts).
- The adapters, the Destination checks and every type-specific create and update (S-039, S-061, S-041, S-042, S-044, S-045);
  the delivery state on the Alert Group page (S-040).

## Contracts

- **Operations implemented**: `listAlertGroupDeliveries`, `listDestinations`, `getDestination`; `getAlertGroupTimeline`
  merges `TimelineDeliveryEntry`; `Route.destinations` and `listDestinations?route=` are filled. Schemas:
  `AlertGroupDelivery(List)`, `DeliveryState` (`pending` and `delivered` in this story), `Destination` with its three
  variants for reading, `DestinationList`, `DestinationRef`, `DestinationHealth`, `HealthState`, `Limiter`,
  `MentionSettings`, `TimelineDeliveryEntry`, `DeliveryEventKind`.
- **Desired state** (C-11.FR-1, ADR-0005; `deliveries`): the dispatcher's re-render step (the hook of S-028) calls
  `delivery.Enqueue` inside the dispatcher's transaction with the Alert Group and the lifecycle event it recorded. For
  each Destination of the Alert Group's Route that is not deleted, `Enqueue` creates the `deliveries` row if missing,
  renders the Root message through the renderer, and — when `desired_hash` changes — increments `desired_version`,
  stores `desired_text`, `desired_payload` and `desired_hash`, sets `desired_received_at` to the receipt time of the
  Stored Snapshot behind the change unless an older one is still undelivered, sets `state` `pending`, `urgent` from the
  Alert Group and `next_attempt_at` now, and sends `NOTIFY delivery`. Only `delivery` writes `deliveries`, `thread_replies` and
  `delivery_events`, and lint 2 keeps delivery out of the Alert Group tables. The hash covers
  what the message shows — text, buttons and colour — never Mentions.
- **Renderer** (`render.go`): an interface `Render(AlertGroup, Destination, language) → Message` and
  `RenderReply(reply, AlertGroup, Destination) → Message`, where a `Message` is markup-neutral sections, buttons and a
  colour. This story ships a minimal renderer — status, `#N`, title and the names of the status's buttons from
  [reference.md](../prd/l1/reference.md#buttons-and-links-by-status), and for a reply the event and its fingerprints —
  which S-036 replaces with the real one.
- **Adapter interface** (`delivery.go`, declared by its consumer, ADR-0016): `Publish` (a new Root message),
  `Update` (an edit of one message id), `Reply` (a Thread reply under a Root message), optional `Check` (the Destination
  check, S-035 onward) and `LengthLimit`. Each returns an outcome: `ok` (with message id and URL; "not modified" is
  `ok`), `retry_after` (the exact delay and its scope, `destination` or `connection`), `transient`, `fatal`, `unknown`,
  `markup_rejected`, `gone` (the message no longer exists) and `thread_lost`, with the provider's error text masked and
  marked untrusted. Adapters register per Destination type; the registry is wired in `runtime.go`. This story handles
  `ok` and `retry_after`; any other outcome leaves the delivery `pending` with its next attempt after the first step of
  `delivery.transient_backoff`, until S-035 gives each its rule.
- **Worker** (C-11.FR-1, FR-15; `design/db/schema.md` §5): every replica runs one, woken by `NOTIFY delivery` and by
  the earliest `next_attempt_at`. It works per Organization — it iterates over the Organizations (one in L1) and
  passes `org_id` to every claim and query (lint 1). It claims due `pending` rows of healthy Destinations, Urgent first, with
  `FOR UPDATE SKIP LOCKED` and a lease through the claim helper of S-013, then in a short transaction re-reads the
  latest Desired state: if `actual_hash = desired_hash` it marks the row `delivered` and calls nothing. Otherwise it
  takes the tokens (below), records `publication_started_at` before a first Publication, calls the adapter outside any
  transaction through the outbound package in the **delivery** client class (S-009), and records the outcome in a new
  short transaction: `actual_version`, `actual_hash`, `message_id`, `message_url`, `published_at`, `publications`,
  `last_delivered_at`. If `desired_version` grew meanwhile, the row stays `pending` and the next call carries the newest
  state (the collapse of C-11.FR-1). Thread replies and the Publication of one delivery are attempted in order: a reply
  waits while its delivery has no `message_id`.
- **Loudness** (C-11.FR-7, FR-20): the lifecycle event tables are a closed table in `enqueue.go` keyed by the event and
  its variant: an event whose rows differ by actor or reason has a row per variant. `unacknowledged` by a Command
  (C-10.FR-15) is a Quiet update of the Root message, while `unacknowledged` by the system with the reason
  `owner_disabled` or `owner_deleted` (the release of an Owner, C-09.FR-22, S-032) is a Loud Thread reply with an update
  of the Root message. Each row gives the messenger form — `publication`, `update`, `reply`, `reply_and_update` or
  `nothing` — with the row's loudness and symbolic Mentions; `delivery_events` and `thread_replies` carry them. A Loud
  event is always a new message (a Publication or a Thread reply); an edit is always Quiet. A first Publication is Loud
  with `new_alert_group` when it comes from `created`; S-035 decides `publication_loud` for the other cases. How a
  messenger makes a new message Quiet is the adapter's (S-061, S-042). Symbolic Mentions reach the adapter as names;
  S-037 resolves them per Destination.
- **Threads** (C-11.FR-4, FR-5; `thread_replies`, `deliveries.thread_batch_until`): rows with the form `reply` or
  `reply_and_update` become Thread replies of the delivery, `pending` at once. `alerts_added` is batched: when
  `thread_batch_until` is empty or past, its reply is due at once and `thread_batch_until` becomes now +
  `route.thread_batching_window`; otherwise its fingerprints and `event_seqs` join the delivery's one `collecting` row,
  due at `thread_batch_until`. Sending a new-Alerts reply sets `thread_batch_until` to its due time plus the window, so
  Alerts that keep arriving produce one reply per window and the next ones after a longer quiet period go at once. A
  batch takes the loudness and Mentions of the loudest event it carries. A reply lists at most
  `delivery.thread_alerts_listed` new Alerts and then "…and K more — open in Muster". Replies of one delivery are sent
  in `id` order.
- **Limiters** (C-11.FR-3; `rate_limit_buckets`, `destinations.limiter_*`, `connections.limiter_*`): a token bucket per
  Destination and per Connection with capacity `limit` and refill `limit / per_seconds`, created on first use. One
  statement takes a token from both buckets of a delivery or from neither; a send and an edit cost one token alike.
  Without a token the delivery is rescheduled to the time its tokens are due plus a short fixed margin — never failed.
  An outgoing webhook has only the Destination's bucket.
- **`RetryAfter`** (C-11.FR-8): `next_attempt_at` = now + the delay exactly, `attempts` untouched; the bucket of the
  outcome's scope is emptied until then (`tokens` 0, `refilled_at` in the future), so a Connection-wide `RetryAfter`
  holds every Destination of the Connection.
- **Interactive path** (C-11.FR-2; `interactive.go`): `Interactive.Do(destination or connection, call)` takes a token
  from the same buckets, polling them during `delivery.interactive_budget`; since deliveries without a token wait for
  the margin, the interactive caller takes the next token first. It never queues and never writes `deliveries`. When the
  budget runs out it returns `limited` with the time until a token is due, which API handlers turn into `503`
  `interactive-budget-exhausted` with `Retry-After` (the `Limited` response) and a Destination test into the error class
  `limited` (S-047). Calls run in the **interactive** client class. The Broken probe never uses this path (S-035).
- **Delivery events** (C-11.FR-21; `delivery_events`): `delivery.RecordEvent` writes one row per event with
  `destination_id`, `alert_group_id`, `kind`, `loudness`, `mentions`, `error_class`, masked `error` and `detail`; this
  story records `publication`. `getAlertGroupTimeline` merges the rows of the Alert Group by `(occurred_at, id)` with its
  Timeline entries as `TimelineDeliveryEntry` (`delivery_event`, `destination`, `error`, `loudness`, `mentions`), honours
  `order` and the `kind=delivery` filter, and hides rows older than `retention.alert_details` like the entries.
- **Delivery state** (C-11.FR-16): `listAlertGroupDeliveries` (`alert-groups:read`) returns one `AlertGroupDelivery`
  per delivery row — `destination` (`DestinationRef`), `state` (`pending` or `delivered` here), `thread_not_attached`
  and `possible_duplicate` (`false` until S-035 and S-042), `message_url`, `error`, `updated_at`.
- **Destinations read** (C-11.FR-18; `internal/destinations`): `listDestinations` and `getDestination`
  (`destinations:read`) read every type from `destinations` with `health` (`healthy` or `broken`, `since`, `reason`),
  `routes` (from `route_destinations`) and the type's fields; Secrets appear only as their status. Deleted Destinations
  are not listed and read as `404`. `Route.destinations` lists `DestinationRef`s with health, and
  `listDestinations?route=` filters by a Route's `public_id`.
- **Metrics** (C-11.FR-17, reference.md): `muster_delivery_attempts_total{destination,kind,outcome}` with `kind`
  `publication`, `update`, `thread_reply` and `outcome` `delivered`, `retry_after` here (the closed sets of the catalogue
  are declared in full); `muster_delivery_latency_seconds{destination}` (`le` buckets for messenger APIs), observed at
  the adapter call of a desired version caused by a Snapshot — from `deliveries.desired_received_at`, which a delivered
  version clears — and not for changes made by Commands or timers;
  `muster_delivery_queue{destination}` (Leader gauge: `pending` deliveries and due Thread replies);
  `muster_destination_info{destination,name}`.
- **Log events**: `delivery_attempt` (INFO: `destination`, `group` as `#N`, `kind`, `outcome`, `attempt`,
  `duration_ms`, `retry_after_ms`).
- **Retention**: a Leader task deletes, in batches of at most 5,000 rows, `thread_replies` that are `sent`, `dropped` or
  `not_delivered` and older than `retention.alert_details` (`design/db/schema.md` §6), per Organization with its
  `org_id`.
- **Architecture lint 3** (ADR-0016; `internal/archlint/callrules.go`): the adapter methods `Publish`, `Update` and
  `Reply`, and every method an adapter offers to the interactive path, may be called only from `worker.go`,
  `threads.go` and `interactive.go` of `internal/delivery`; the bad fixture calls `Publish` from `internal/api`.
- **Recording test adapter** (`deliverytest`): records every call with its time, Destination, message and loudness;
  answers from a script per call (`ok`, `retry_after` with a delay and scope, `transient`, `fatal`, `unknown`,
  `markup_rejected`, `gone`, `thread_lost`, a delay, or a hook that runs after it accepted a call); offers a scripted
  `Check`. Every C-11 to C-16 test uses it through the adapter interface.
- **Defaults**: `delivery.interactive_budget`, `delivery.thread_alerts_listed`,
  `route.thread_batching_window`, `delivery.transient_backoff` (first step only, until S-035).

## Steps

1. Write the adapter interface, the outcomes, the registry, the minimal renderer and the recording test adapter.
   Check: unit tests drive the recorder with every outcome.
2. Write `Enqueue` with the lifecycle event table and the dispatcher hook. Check: `enqueue_test.go` covers every row
   named in the acceptance, with its form, loudness and Mentions.
3. Write the limiters and the interactive path. Check: `limiter_test.go` covers the mixed calls, both buckets, the
   empty bucket after a `RetryAfter` and the interactive caller winning the next token.
4. Write the worker with claims, leases, the collapse and the outcome records, and the Thread batching. Check:
   `worker_test.go` and `threads_test.go` with a manual clock.
5. Write the delivery events, the Timeline merge, the Destination reads, the delivery state, the metrics, the log event,
   the retention task and lint 3. Check: `make lint-arch` reports the bad fixture.
6. Write `live_test.go`. Check: Verification below.

## Verification

C-11 has no Destination type of its own: its acceptance is checked through the recording test adapter and a virtual
clock (C-11, Acceptance). The live check therefore runs the real delivery runtime — the dispatcher fed by real Snapshots
through the ingest worker, two delivery workers as two replicas, `LISTEN/NOTIFY`, the shared token buckets and the
development clock — against the development database, with the recording adapter in place of a messenger. S-061 repeats
C-11.AC-1 end to end on `muster dev` against the fake Mattermost server, and C-11.AC-2 through the `429` with
`Retry-After` of C-13.AC-15; C-11.AC-14 is checked here only.

```sh
make dev-db
go test -tags integration -run TestLive -v ./internal/delivery/...
# === RUN   TestLive/burst_collapses                    (C-11.AC-1)
#     recorder: publish=1 update=1; the update carries desired version 2 (acknowledged)
# === RUN   TestLive/not_modified_is_delivered
#     recorder: update answered "not modified"; delivery state delivered, attempts 0
# === RUN   TestLive/retry_after_destination            (C-11.AC-2)
#     retry_after 7s: next call to the Destination after 7.0–8.0 s; attempts 0
# === RUN   TestLive/retry_after_connection
#     retry_after 3s scoped to the Connection: both of its Destinations waited 3.0–4.0 s
# === RUN   TestLive/limiter_mixed_calls                (C-11.AC-14)
#     limiter 6 per 60 s: calls 1–6 passed (3 publish, 3 update); call 7 (update) waited for a token
# === RUN   TestLive/limiter_two_replicas
#     two workers, limiter 6 per 60 s: 6 calls in the first minute, 12 after two minutes
# === RUN   TestLive/urgent_first
#     queue of 5 with one Urgent: the Urgent delivery was called first; the limiter held all of them
# === RUN   TestLive/interactive_ahead_of_queue         (C-11.FR-2)
#     bucket empty, 5 deliveries waiting: the interactive call took the next token
# === RUN   TestLive/interactive_limited
#     no token within 5 s: limited, retry after 7 s; the recorder saw no call
# === RUN   TestLive/thread_batching                    (C-11.FR-4, FR-5)
#     t=0 s reply (1 Alert); t=10–50 s collected; t=60 s reply listing 10 of 12 Alerts and "…and 2 more — open in Muster"; t=200 s reply at once
# === RUN   TestLive/lifecycle_rows                     (C-11.AC-10)
#     31 rows of the lifecycle event tables, keyed by event and variant: each produced its row's form, loudness and Mentions
# === RUN   TestLive/lease_expiry
#     worker A stopped holding a lease; worker B claimed the row after the lease ran out and delivered it once
# --- PASS: TestLive (…)
```

On `muster dev`, the new reads answer for an installation without Destinations:

```sh
make dev &
# the Admin's session (`jar`, `H`) as in S-011; an Alert Group as in S-028, its id in G
API=localhost:8080/api/v1
curl -s -b jar $API/destinations | jq -c .                                   # {"items":[],"next_cursor":null}
curl -s -b jar "$API/destinations?health=broken&type=telegram" | jq -c .items  # []
curl -s -b jar "$API/alert-groups/$G/deliveries" | jq -c .                   # {"items":[]}
curl -s -b jar "$API/alert-groups/$G/timeline?kind=delivery" | jq -c .items  # []
curl -s -b jar $API/routes | jq -c '[.items[] | {name, destinations}]'       # [{"name":"Default","destinations":[]}]
curl -s -b jar $API/destinations/DS000000000000 | jq -r .status              # 404
curl -s localhost:8082/metrics | grep -c '^# TYPE muster_delivery_attempts_total counter'   # 1
make lint-arch && echo ok                                                    # ok
```

## Open questions

None.

## Notes

- Suggested commit: `feat(delivery): reconcile root messages with limiters, an interactive path and batched threads`.
- The minimal renderer exists so that delivery can be built and tested before C-12; S-036 removes it.
- `live_test.go` is the evidence the pull request records for C-11.AC-1, AC-2, AC-10 and AC-14.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-11.FR-1 | full | |
| C-11.FR-2 | partial | the path and its budget; its first callers are S-039 (checks), S-061 (ephemeral posts) and S-047 (`limited`) |
| C-11.FR-3 | full | the default limiter of each type is applied by that type's create path |
| C-11.FR-4 | full | |
| C-11.FR-5 | full | the reply texts are C-12's |
| C-11.FR-7 | partial | the rule and the table; Quiet new messages per messenger are S-061 and S-042 |
| C-11.FR-8 | partial | `RetryAfter`; the other classes are S-035 |
| C-11.FR-15 | full | |
| C-11.FR-16 | partial | `pending` and `delivered`; the other states are S-035 and S-042, the page S-040 |
| C-11.FR-17 | partial | attempts, latency, queue and info; Broken and Storm metrics are S-035 |
| C-11.FR-18 | partial | reading Destinations with health and Routes; health changes are S-035, type fields come with each type |
| C-11.FR-20 | partial | every row but `moved_to_default_route` (S-035); the C-17 rows are produced from S-049 on |
| C-11.FR-21 | partial | the table, `publication` and the Timeline merge; the other kinds are S-035 and S-042 |
| C-11.AC-1 | full | repeated against the fake Mattermost server in S-061 |
| C-11.AC-2 | full | repeated against the fake Mattermost server in S-061 (C-13.AC-15) |
| C-11.AC-10 | partial | the lifecycle event rows; the delivery-event rows and `moved_to_default_route` are S-035 |
| C-11.AC-14 | full | |
| C-09.FR-11 | partial | delivery entries in the Timeline |
| C-09.FR-14 | partial | delivery entries in the Timeline API; the page section is S-040 |
| C-08.FR-1 | partial | the Route's Destinations with their health |
