---
id: S-035
title: Error classes, Broken Destinations and recovery, Storms, deleted messages and duplicates (BE)
capability: C-11
kind: be
layer: L1
depends_on: [S-034]
covers: [C-11.FR-6, C-11.FR-8, C-11.FR-9, C-11.FR-10, C-11.FR-11, C-11.FR-12, C-11.FR-13, C-11.FR-14, C-11.FR-16, C-11.FR-17, C-11.FR-18, C-11.FR-19, C-11.FR-20, C-11.FR-21, C-11.AC-3, C-11.AC-4, C-11.AC-5, C-11.AC-6, C-11.AC-7, C-11.AC-8, C-11.AC-9, C-11.AC-10, C-11.AC-11, C-11.AC-12, C-11.AC-13, C-09.FR-19, C-08.FR-1]
files_touched:
  - internal/api/destinations.go
  - internal/api/routes.go
  - internal/api/server.go
  - internal/api/destinations_test.go
  - internal/api/routes_test.go
  - internal/db/checks.go
  - internal/delivery/broken.go
  - internal/delivery/delivery.go
  - internal/delivery/deliverytest/recorder.go
  - internal/delivery/enqueue.go
  - internal/delivery/events.go
  - internal/delivery/membership.go
  - internal/delivery/outcomes.go
  - internal/delivery/publication.go
  - internal/delivery/query.sql
  - internal/delivery/recovery.go
  - internal/delivery/storm.go
  - internal/delivery/threads.go
  - internal/delivery/worker.go
  - internal/delivery/broken_test.go
  - internal/delivery/enqueue_test.go
  - internal/delivery/export_test.go
  - internal/delivery/live_test.go
  - internal/delivery/membership_test.go
  - internal/delivery/outcomes_test.go
  - internal/delivery/publication_test.go
  - internal/delivery/storm_test.go
  - internal/delivery/threads_test.go
  - internal/delivery/worker_test.go
  - internal/destinations/delete.go
  - internal/destinations/destinations.go
  - internal/destinations/query.sql
  - internal/destinations/delete_test.go
  - internal/groups/dispatcher.go
  - internal/groups/dispatcher_test.go
  - internal/internalalerts/registry.go
  - internal/internalalerts/internalalerts_test.go
  - internal/leader/tasks.go
  - internal/logging/events.go
  - internal/metrics/catalogue.go
  - internal/routing/query.sql
  - internal/routing/routes.go
  - internal/routing/routes_test.go
  - internal/runtime/runtime.go
  - internal/runtime/runtime_test.go
acceptance:
  - "[C-11.FR-6, C-11.FR-17, C-11.AC-3] With 30 new Alert Groups within a minute on a Route with `route.storm_threshold` 20, the first 20 get Root messages; the 21st starts a Storm, so each Destination of the Route gets one Storm summary, Loud with `new_alert_group`, and of the last 10 only the Urgent ones are published, first; after `delivery.storm_calm_period` below the threshold the summary gets its final state in one Quiet edit, the last 10 still open are published as Quiet new messages within the limits, and the resolved ones are never published; `muster_storm_active{route}` and `Route.storm_active` show the Storm while it lasts."
  - "[C-11.FR-13, C-11.AC-4] A `gone` outcome for an open Alert Group's Root message republishes it once, Quietly, with \"The previous message was deleted at HH:MM\" and a new Thread; a `gone` for that republished message marks the pair `deleted_in_messenger` and nothing more is sent; for a resolved Alert Group only the mark is recorded."
  - "[C-11.FR-12, C-11.AC-5] A worker stopped after the adapter accepted a Publication and before the record leaves `publication_started_at` set; the next attempt publishes again, sets `possible_duplicate` and records a `possible_duplicate` delivery event that the Alert Group's Timeline shows."
  - "[C-11.FR-8, C-11.FR-9, C-11.FR-17, C-11.FR-18, C-11.AC-6] `Transient` outcomes are retried with `delivery.transient_backoff` until either budget of `delivery.transient_budget` runs out; then the Destination is Broken as unavailable with the reason \"unavailable after repeated failures: {last error}\", `MusterDestinationBroken` fires, `muster_destination_broken{destination}` is 1, and the delivery stays `pending` (waiting), not Not delivered; a `Fatal` outcome makes the Destination Broken at once."
  - "[C-11.FR-19, C-11.FR-16, C-11.AC-7] While a Destination is Broken, Alert Group A starts and keeps firing, B — published before — is acknowledged and gets a new Alert, and C starts and resolves; after a successful probe A is published as a Loud new message, B's Root message is edited once to its current state with no Thread reply, and C is never published, its delivery `withheld`."
  - "[C-11.FR-10, C-11.FR-16, C-11.FR-21, C-11.AC-8, C-11.AC-13] An `unknown` outcome ends that delivery as `not_delivered` with the error, adds exactly one `not_delivered` row to `delivery_events`, changes no Alert Group table, leaves the Destination healthy, and `getAlertGroupTimeline` returns it as the kind `delivery` in time order among the Alert Group's entries; a later change of the Desired state starts a new delivery."
  - "[C-11.FR-11, C-11.AC-9, C-11.AC-12] An Alert Group resolved while its first Publication waited for a `RetryAfter`, or for `Transient` retries within the budget, is published Quietly with \"Delivered late: started HH:MM, resolved HH:MM while this Destination was unavailable.\" and a `delivered_late` delivery event; one covered by a Storm summary, or whose Destination became Broken first, is not."
  - "[C-11.FR-9, C-11.AC-11] With a Destination Broken and nothing waiting, the probe after `delivery.broken_probe_interval` runs the adapter's Destination check in the delivery client class: a failing check keeps the Destination Broken with that error as the reason; a passing one makes it healthy, resolves `MusterDestinationBroken` and records `destination_recovered`, with no Alert Group activity; with a delivery waiting, the probe attempts the oldest one instead."
  - "[C-11.FR-8] A `markup_rejected` outcome resends the same text without markup in the same attempt, counts `outcome=\"markup_rejected\"` and records a `markup_rejected` delivery event."
  - "[C-08.FR-1, C-11.FR-14] An `updateRoute` that changes only `destination_ids` is saved with a new version and records `route.updated` with `/destination_ids` in its diff; an id that names no Destination, or a deleted one, answers 422 `unknown_id` at its pointer."
  - "[C-11.FR-14] Adding a Destination to a Route publishes the Route's open Alert Groups there Quietly; removing it from the Route, or deleting it, gives each of its open Root messages one final Quiet edit \"No longer updated here; current state in Muster: {link}\" and nothing after; `deleteDestination` answers 204, removes the Destination from every Route, wipes its secrets once the final edits are done and keeps the row, which `listDestinations` no longer lists."
  - "[C-09.FR-19, C-11.FR-14, C-11.FR-20, C-11.AC-10] An Alert Group moved to the Default route gets the final edit in the Destinations it leaves and a Quiet Publication in the Default route's Destinations; every row of the Loud/Quiet table of delivery events produces the new message, edit or nothing it names (a table-driven test)."
verify: "make ci test-integration"
operator_attention: false
issue: 35
---

# S-035. Error classes, Broken Destinations and recovery, Storms, deleted messages and duplicates (BE)

## Scope

**IN**

- The rules for every adapter outcome: `Transient` with its budgets, `Fatal`, unknown (Not delivered), rejected markup,
  `gone` (deleted Root message).
- Broken Destinations: the state, the warning data, the metric, the Internal alert `MusterDestinationBroken`, the probe
  and recovery to the current state.
- Storms per Route with Storm summaries, held Publications, the calm period and the gradual Quiet publication.
- Late Publications, possible duplicates, Destinations joining and leaving Routes, deleting a Destination, and the move
  of an Alert Group to the Default route.
- The remaining delivery events and the Loud/Quiet table of delivery events.

**OUT**

- The texts in both languages, in the Organization's time zone and inside the real message layout (S-036): until then
  the minimal renderer of S-034 shows the Storm summary and the notes of this story as fixed English lines.
- The Destination check of each type (S-039 and S-061, S-042) and the next-delivery probe of outgoing webhooks (S-044).
- The Connection API that deletes Connections (S-039); the events mode of outgoing webhooks, which keeps every event
  through a Broken period and a deletion of its own (S-044).
- Warnings on the Destination and Route pages (S-064).

## Contracts

- **Operations implemented**: `deleteDestination` (it joins the implemented-operations map of
  `internal/api/server.go`, and the runtime wires the probe, the Storm calm timer and the secret wipe);
  `createRoute` and `updateRoute` accept `destination_ids`; `DestinationHealth` gains `since` and `reason`; `Route.storm_active`,
  `Route.storm` (`since`, `alert_group_count` of the active Storm) and `DestinationRef.health` take their values;
  `DeliveryState` gains `waiting_for_broken_destination`, `not_delivered`, `deleted_in_messenger`, `withheld` and
  `retired`, and `AlertGroupDelivery` its `possible_duplicate` and `error`. `listAlertGroupDeliveries` shows a
  `withheld` row with `message_url` null and a `retired` row with the link to its last message.
- **Outcomes** (C-11.FR-8, FR-10; `outcomes.go`):

  | Outcome | Rule |
  |---|---|
  | `ok` | delivered (S-034) |
  | `retry_after` | wait exactly, not an attempt (S-034) |
  | `transient` | `attempts` + 1, `first_failed_at` set on the first one; next attempt after `delivery.transient_backoff` (exponential with jitter); when `attempts` reaches the attempt budget or now − `first_failed_at` exceeds the time budget of `delivery.transient_budget`, the Destination becomes Broken with `broken_cause` `unavailable` and the row stays `pending` |
  | `fatal` | the Destination becomes Broken at once with `broken_cause` `fatal`; the row stays `pending` |
  | `unknown` | the row ends `not_delivered` with `last_error_class` and the masked `last_error`; a `not_delivered` delivery event; the Destination stays healthy; the next change of its Desired state makes it `pending` again |
  | `markup_rejected` | the adapter resends the same text without markup in the same attempt; counted and recorded as `markup_rejected` |
  | `gone` | the deleted Root message flow (below) |
  | `thread_lost` | Telegram's lost Thread (S-066) |

  A successful call resets `attempts` and `first_failed_at`.
- **Broken** (C-11.FR-9, FR-18; `destinations.health`, `broken_since`, `broken_cause`, `broken_reason`,
  `next_probe_at`): becoming Broken sets them in one transaction with `next_probe_at` = now +
  `delivery.broken_probe_interval`, records a `destination_broken` delivery event, raises `MusterDestinationBroken`
  (registered here: critical, labels `destination` and `destination_name`, through the mechanism of S-021) and sends the
  hint `destination`. The reason is the masked error, or "unavailable after repeated failures: {last error}". While
  Broken, the claim of S-034 skips the Destination's deliveries, which show `waiting_for_broken_destination`; new
  Thread replies are created `dropped`; and a delivery never published there whose Alert Group resolves becomes
  `withheld` at once, so that it no longer waits and the probe can use the Destination check (C-11.FR-19). `muster_destination_broken{destination}` is a Leader gauge.
- **Probe** (C-11.FR-9; `broken.go`): every replica claims Broken Destinations whose `next_probe_at` has passed, moving
  `next_probe_at` by `delivery.broken_probe_interval` as the lease; like every background claim it runs per
  Organization and passes `org_id` (lint 1, `design/db/schema.md` §5). With a `pending` delivery it attempts the oldest one
  through the worker's path; with nothing waiting it calls the adapter's `Check`; for a type without `Check` it marks
  the Destination so that its next due delivery is attempted at once (used by S-044). Both run in the **delivery**
  client class, never on the interactive path. A failure keeps the Destination Broken with the new reason; a success
  makes it healthy (`destination_recovered` delivery event, Internal alert resolved, hint) and starts recovery. A
  successful `Check` started by a person (S-039, S-042) or a Destination test (S-047) calls the same `MarkHealthy`.
- **Recovery** (C-11.FR-19; `recovery.go`): in one pass over the Destination's deliveries — Thread replies that came due
  while it was Broken stay `dropped`; an Alert Group never published there that resolved meanwhile is `withheld`;
  one never published and still open is published, Urgent first, `publication_loud` true with `new_alert_group` when it
  is firing at that moment and false otherwise; a published Root message gets one edit to its current state.
- **Storms** (C-11.FR-6; `storms`, `timers` kind `storm_calm_check`, `deliveries.storm_id`, `held_by_storm_id`): the
  dispatcher's `created` events are counted per Route over the last 60 seconds. The one that makes the count exceed
  `route.storm_threshold` starts a Storm (`storms` row, `storm_started` log line): for each Destination of the Route a
  Storm summary delivery (`storm_id`) is created, Loud with `new_alert_group` at its first Publication; from then on the
  Publication of a new non-urgent Alert Group of the Route is held (`held_by_storm_id`, not claimed), while an Urgent one
  is published at once, ahead in the queue. The summary's counts are updated as Alert Groups join and its edits are
  Quiet. When the count falls to the threshold or below, `calm_since` is set and a `storm_calm_check` timer is due after
  `delivery.storm_calm_period`; the timer ends the Storm only if the count stayed at or below the threshold, otherwise it
  is pushed back. At the end: the summary gets its final state (one Quiet edit), held deliveries of still open Alert
  Groups become `pending` with `publication_loud` false and are published gradually within the limiters, held deliveries
  of resolved ones become `withheld`; `storm_ended` log line. A Storm summary has no ack timeout; an Alert Group's ack
  timeout starts at its own Publication (`published_at`, read by S-049). `muster_storm_active{route}` (Leader gauge) and
  `Route.storm_active` with `Route.storm`. The events mode of outgoing webhooks is not subject to Storms (S-044).
- **Late Publication** (C-11.FR-11; `deliveries.late_note`): when an Alert Group resolves while its first Publication is
  still `pending` because of a `RetryAfter` or `Transient` retries within the budget, the Publication stays due,
  becomes Quiet and carries `late_note`; the message shows "Delivered late: started HH:MM, resolved HH:MM while this
  Destination was unavailable." and a `delivered_late` delivery event is recorded. A held (Storm) delivery, or one whose
  Destination became Broken before the late Publication succeeded, follows the Storm or the recovery rule instead.
- **Possible duplicate** (C-11.FR-12): `publication_started_at` is recorded in its own committed transaction before the
  adapter call. A claim of a delivery that has it but no `message_id` publishes again — never skipped, never searched for
  in the messenger — sets `possible_duplicate`, records a `possible_duplicate` delivery event and logs
  `delivery_possible_duplicate` (WARN).
- **Deleted Root message** (C-11.FR-13): `gone` on an edit or a Thread reply of an open Alert Group: if
  `republished_after_delete` is false, the delivery is reset to publish again Quietly with the note "The previous message
  was deleted at HH:MM", its Thread starts over (`thread_state` `none`, Thread anchors cleared, pending replies kept for
  the new Root message), and `republished` is recorded; if it is already true, the delivery becomes
  `deleted_in_messenger` (event `deleted_in_messenger`) and is never claimed again. For a resolved Alert Group only the
  mark is recorded.
- **Route edits of `destination_ids`** (C-08.FR-1, C-11.FR-14; `internal/routing/routes.go`, `query.sql`): today a
  Route edit that changes only its Destinations is dropped — `Update` never copies `DestinationIDs` into the new state,
  and the Audit log view (`view`, `viewOf`) has no `destination_ids`, so the diff is empty and nothing is written — and
  `check()` refuses every Destination id because no Destination type existed. This story adds `DestinationIDs` to the
  view (the diff shows `/destination_ids`), copies them in `Update`, replaces the refusal with a check that each id names
  a Destination that is not deleted (`422 unknown_id` at `/destination_ids/<i>` otherwise), and writes
  `route_destinations` in the Route's transaction — created, kept with its `added_at`, or removed — with new queries in
  `internal/routing/query.sql`. The fake store of `routes_test.go` and the tests of `internal/api/routes_test.go` that
  expect the refusal change with it.
- **Destinations of a Route** (C-11.FR-14; `route_destinations.added_at`, `deliveries.desired_retire`): `createRoute`,
  `updateRoute` and the accepted suggestions that add a Destination enqueue Quiet Publications of the Route's open Alert
  Groups there; removing a Destination from a Route sets `desired_retire` on the deliveries of the Route's open Alert
  Groups there: the next call is one Quiet edit "No longer updated here; current state in Muster: {link}", after which the
  row is `retired` (event `final_edit`) and receives nothing more. An Alert Group whose Root message was never published
  there is simply `withheld`.
- **Move to the Default route** (C-09.FR-19, C-11.FR-20): `moveOpenAlertGroups` of S-028 calls the same membership
  rules per moved Alert Group — final edits in the Destinations it leaves, Quiet Publications in the Default route's
  Destinations it joins, nothing for a Destination in both.
- **Deleting a Destination** (C-11.FR-14; `deleteDestination`, `destinations:write`, optional `If-Match`): always `204`;
  in one transaction `deleted_at` is set, its `route_destinations` rows are removed, the deliveries of open Alert Groups
  get `desired_retire`, and an Audit log entry `destination.deleted` is written. Its secrets — `signing_secret_*`,
  `previous_signing_secret_*`, `proxy_password_*` and its `destination_secrets` rows — are wiped as soon as no delivery of
  it is `pending`, by the worker after the last final edit or at once when there is none. A deleted Destination is not
  listed, reads as `404`, and no longer counts as using its Connection. `delivery.AbandonConnection` ends the final
  edits still pending for the deleted Destinations of a Connection as `not_delivered` with the error "the Connection was
  deleted"; `deleteConnection` calls it from S-039.
- **Loud/Quiet table of delivery events** (C-11.FR-20, [reference.md](../prd/l1/reference.md#loud-and-quiet)): a closed
  table in `enqueue.go` next to the lifecycle event rows of S-034 — Publication into a newly added Destination (Quiet),
  Storm summary first publication (Loud, `new_alert_group`), its updates and final state (Quiet edits), gradual
  Publication after a Storm (Quiet), delivered late (Quiet), Publication after recovery (Loud and `new_alert_group` if
  firing, else Quiet), the update after recovery (one Quiet edit), Thread replies while Broken (not sent), resolved while
  Broken and never published (not published), republication after deletion (Quiet), final edit (Quiet).
- **Delivery events** (C-11.FR-21): this story records `possible_duplicate`, `not_delivered`, `delivered_late`,
  `deleted_in_messenger`, `republished`, `markup_rejected`, `destination_broken`, `destination_recovered`,
  `storm_summary` and `final_edit`, each with its loudness and Mentions from the table above.
- **Metrics** (C-11.FR-17): `muster_delivery_attempts_total` gains `kind` `storm_summary`, `final_edit` and `outcome`
  `transient`, `fatal`, `unknown`, `markup_rejected`; `muster_destination_broken{destination}` and
  `muster_storm_active{route}` (Leader gauges).
- **Log events**: `destination_broken` (WARN: `destination`, `cause`, `reason`), `destination_recovered` (INFO:
  `destination`, `broken_for_s`), `delivery_possible_duplicate` (WARN: `destination`, `group`), `delivery_not_delivered`
  (WARN: `destination`, `group`, `kind`, `error_class`), `storm_started` and `storm_ended` (INFO: `route`,
  `alert_groups`, `urgent`).
- **Defaults**: `delivery.transient_backoff` and `delivery.transient_budget`, `delivery.broken_probe_interval`,
  `delivery.storm_calm_period`, `route.storm_threshold`.

## Steps

1. Write the outcome rules and the budgets. Check: `outcomes_test.go` covers every row of the outcome table with a
   manual clock.
2. Write Broken, the probe and recovery, and register `MusterDestinationBroken`. Check: `broken_test.go` covers
   C-11.AC-6, AC-7 and AC-11 through the recorder.
3. Write Storms with the calm timer. Check: `storm_test.go` covers C-11.AC-3 and a Storm that does not calm.
4. Write late Publications, possible duplicates and the deleted Root message flow. Check: `publication_test.go` covers
   C-11.AC-4, AC-5, AC-9 and AC-12.
5. Write the Route edits of `destination_ids`, Route membership, the move to the Default route and
   `deleteDestination`. Check: `routes_test.go` covers an edit that changes only `destination_ids` and its Audit log
   diff; `membership_test.go` and `delete_test.go`, including the wiped secrets.
6. Extend `live_test.go` and add the metrics and log events. Check: Verification below.

## Verification

As in S-034, C-11 is checked through the recording test adapter on the real delivery runtime with the development
database and clock; S-061 repeats C-11.AC-3, AC-4, AC-5, AC-6, AC-7, AC-8 and AC-11 on `muster dev` against the fake
Mattermost server.

```sh
make dev-db
go test -tags integration -run TestLive -v ./internal/delivery/...
# === RUN   TestLive/storm                              (C-11.AC-3)
#     route threshold 20, 30 new Alert Groups in 50 s (3 Urgent among the last 10):
#     publish=20 before the Storm; storm summary: publish=1 loud [new_alert_group]; urgent publish=3
#     after the calm period: summary update=1 quiet ("Storm over: 5 Alert Groups still open"); quiet publish=5; withheld=2
# === RUN   TestLive/deleted_root                       (C-11.AC-4)
#     gone → quiet republish=1 with "The previous message was deleted at 10:42"; gone again → deleted_in_messenger; no further call
# === RUN   TestLive/duplicate_after_crash              (C-11.AC-5)
#     worker stopped after the adapter accepted; second worker: publish=2, possible_duplicate=true, timeline delivery_event=possible_duplicate
# === RUN   TestLive/transient_budget                   (C-11.AC-6)
#     10 transient attempts → broken (unavailable after repeated failures: 503); MusterDestinationBroken firing; state pending
# === RUN   TestLive/fatal_is_broken
# === RUN   TestLive/recovery_current_state             (C-11.AC-7)
#     A: publish loud [new_alert_group]; B: update=1, replies=0 (2 dropped); C: withheld
# === RUN   TestLive/unknown_not_delivered              (C-11.AC-8, AC-13)
#     state not_delivered; delivery_events +1; Alert Group tables unchanged; destination healthy; timeline kind delivery in order
# === RUN   TestLive/late_publication_retry_after       (C-11.AC-9)
#     resolved while waiting 30 s: quiet publish with "Delivered late: started 10:00, resolved 10:00 while this Destination was unavailable."
# === RUN   TestLive/late_publication_transient         (C-11.AC-12)
# === RUN   TestLive/probe_check                        (C-11.AC-11)
#     check fails → broken, reason "not a member"; check passes → healthy, MusterDestinationBroken resolved, no Alert Group touched
# === RUN   TestLive/markup_rejected
# === RUN   TestLive/destination_added_and_removed      (C-11.FR-14)
#     added: quiet publish=2; removed: final edit=2 "No longer updated here; current state in Muster: http://localhost:8080/alert-groups/AG…"
# === RUN   TestLive/moved_to_default_route
# === RUN   TestLive/delivery_event_rows                (C-11.AC-10)
#     11 rows of the Loud/Quiet table of delivery events: each as its row says
# --- PASS: TestLive (…)
```

On `muster dev`:

```sh
make dev &
# the Admin's session (`jar`, `H`) as in S-011
API=localhost:8080/api/v1
curl -s -o /dev/null -w '%{http_code}\n' "${H[@]}" -X DELETE $API/destinations/DS000000000000   # 404
curl -s -b jar $API/routes | jq -c '[.items[] | {name, storm_active}]'      # [{"name":"Default","storm_active":false}]
grep -A3 'MusterDestinationBroken' docs/reference/internal-alerts.md | head -4
# | `MusterDestinationBroken` | critical | a Destination is Broken | `destination`, `destination_name` |
curl -s localhost:8082/metrics | grep -c '^muster_storm_active{'             # 1 (the Default route; no TYPE lines are exposed)
```

## Open questions

None.

## Notes

- Suggested commit: `feat(delivery): add error classes, broken destinations and recovery, storms and republication`.
- `groups.Rendering` gains `After`, the dispatcher's after-commit queue, so that `storm_started` is logged only once the
  change that started the Storm committed.
- An Enqueue and a change of a Route's Destinations (an edit of `destination_ids`, or the deletion of a Destination)
  serialize on a transaction advisory lock of the Route (`db.RouteMembershipLockClass`), shared and exclusive, not on
  the Route's row: the move to the Default route holds that row and then the Alert Groups, which a Command holds
  before its Enqueue.
- A call in flight never revives a row that ended meanwhile; a Publication that created a message on such a row keeps
  it: a Storm summary is retired with it, an Alert Group whose Destination was deleted or left its Route gets the final
  edit of it, any other row is kept current (`RecordDelivered`).
- The Storm rule counts `created` events, not Alerts; a Reopen is not a new Alert Group and does not count.
- `live_test.go` is the evidence for C-11.AC-3 to AC-13; S-061 shows the same behaviour against a messenger fake.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-11.FR-6 | partial | messengers through the adapter interface; the template mode of outgoing webhooks is S-045, the events mode's exemption S-044 |
| C-11.FR-8 | partial | completes the classes with S-034; each adapter maps its responses (S-061, S-042, S-044) |
| C-11.FR-9 | partial | Broken, probe and recovery; the checks of each type are S-061 and S-042, the webhook probe S-044, the warnings on pages S-064 |
| C-11.FR-10 | full | the Alert Group page shows it from S-064 |
| C-11.FR-11 | full | |
| C-11.FR-12 | full | |
| C-11.FR-13 | full | |
| C-11.FR-14 | partial | Routes, deletion, wiping and abandoning; `deleteConnection` calls it from S-039, the events mode is S-044 |
| C-11.FR-16 | partial | waiting, Not delivered, deleted in the messenger, withheld, retired, possible duplicate; "Thread not attached" is S-066 |
| C-11.FR-17 | partial | completes the metrics with S-034 |
| C-11.FR-18 | partial | health; the type fields come with S-039, S-042 and S-044 |
| C-11.FR-19 | partial | messengers; the events-mode exception is S-044 |
| C-11.FR-20 | partial | the delivery-event rows and `moved_to_default_route`; completes FR-20 with S-034 |
| C-11.FR-21 | partial | every kind but `thread_not_attached` (S-066) |
| C-11.AC-3 | full | repeated against the fake Mattermost server in S-061 |
| C-11.AC-4 | full | repeated in S-061 (C-13.AC-12) |
| C-11.AC-5 | full | repeated in S-061 with a restart of Muster |
| C-11.AC-6 | full | repeated in S-061 |
| C-11.AC-7 | full | repeated in S-061 |
| C-11.AC-8 | full | repeated in S-061 (C-13.AC-10) |
| C-11.AC-9 | full | |
| C-11.AC-10 | partial | the delivery-event rows; completes AC-10 with S-034 |
| C-11.AC-11 | full | repeated in S-061 (C-13.AC-8) and S-042 (C-14.AC-12) |
| C-11.AC-12 | full | |
| C-11.AC-13 | full | |
| C-09.FR-19 | partial | the delivery side of the move |
| C-08.FR-1 | partial | the Route's Destinations written through the API; their section of the Route form is S-064 |
