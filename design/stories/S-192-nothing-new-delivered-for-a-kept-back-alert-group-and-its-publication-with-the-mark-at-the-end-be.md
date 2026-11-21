---
id: S-192
title: Nothing new delivered for a kept-back Alert Group, and its publication with the mark when the window ends (BE)
capability: C-34
kind: be
layer: L2
depends_on: [S-191, S-121, S-140, S-169, S-155]
covers: [C-34.FR-10, C-34.FR-11, C-34.FR-12, C-34.FR-13, C-34.FR-17, C-34.FR-18, C-34.FR-19, C-34.FR-21, C-34.FR-22, C-34.AC-3, C-34.AC-4, C-34.AC-5, C-34.AC-9, C-34.AC-11, C-34.AC-12, C-34.AC-13, C-34.AC-14, C-34.AC-17, C-26.FR-5]
files_touched:
  - api/openapi.yaml
  - internal/db/migrations/00NN_deliveries_maintenance.up.sql
  - internal/db/migrations/00NN_deliveries_maintenance.down.sql
  - design/db/schema.md
  - internal/db/migrate_test.go
  - internal/delivery/enqueue.go
  - internal/delivery/enqueue_test.go
  - internal/delivery/publication.go
  - internal/delivery/publication_test.go
  - internal/delivery/storm.go
  - internal/delivery/storm_test.go
  - internal/delivery/worker.go
  - internal/delivery/worker_test.go
  - internal/delivery/outcomes.go
  - internal/delivery/outcomes_test.go
  - internal/delivery/membership.go
  - internal/delivery/membership_test.go
  - internal/delivery/pages.go
  - internal/delivery/threads.go
  - internal/delivery/threads_test.go
  - internal/delivery/render.go
  - internal/delivery/render_test.go
  - internal/delivery/webhookevents.go
  - internal/delivery/webhookevents_test.go
  - internal/delivery/events.go
  - internal/delivery/hints.go
  - internal/delivery/query.sql
  - internal/delivery/maintenance_integration_test.go
  - internal/groups/maintenance.go
  - internal/groups/maintenance_test.go
  - internal/groups/read.go
  - internal/groups/query.sql
  - internal/messages/texts/en.json
  - internal/messages/texts/ru.json
  - internal/messages/render.go
  - internal/messages/render_test.go
  - internal/messages/layout.go
  - internal/messages/layout_test.go
  - internal/messages/replies.go
  - internal/messages/default_test.go
  - internal/messages/message.go
  - internal/webhooks/body.go
  - internal/webhooks/body_test.go
  - internal/metrics/catalogue.go
  - internal/metrics/metrics_test.go
  - internal/runtime/runtime.go
  - docs/outgoing-webhooks/events.md
  - test/e2e/maintenance_delivery_test.go
acceptance:
  - "[C-34.FR-11, C-34.AC-3] While an Alert Group is kept back no Root message is published and no Thread reply is sent — new Alerts, Reopens, the end of a Snooze, a rise to Urgent, ack timeout notices, Reminders, Pages and the replies of a resolution by the system — so the fake Mattermost, Telegram, Slack and bridge servers receive nothing for it and nobody is mentioned; `destination_joined` waits for the end of the window; the Destination test, Destination checks and the direct messages of Account links go on (`enqueue_test.go`, `worker_test.go`, `test/e2e/maintenance_delivery_test.go`)."
  - "[C-34.FR-11, C-34.AC-9, C-34.AC-11] The Root messages an Alert Group already has are still edited, Quietly, and show \"In a Maintenance window until {time}\" while it is kept back; a press of Acknowledge on such a message acknowledges it, answered on the interactive path, and the Root message is edited to show it (`render_test.go`, `test/e2e/maintenance_delivery_test.go`)."
  - "[C-34.FR-12, C-34.AC-3] An Alert Group created while kept back counts toward no Storm threshold of its Route and is in no Storm summary (`storm_test.go`)."
  - "[C-34.FR-12, C-34.AC-14] An outgoing webhook Destination in events mode receives `created`, `maintenance_started` and `maintenance_ended` of a kept-back Alert Group with `kept_back` in `alert_group`; the template mode is kept back like a messenger; a delivered `created` event is not a Publication, so the Route whose only Destination is such a webhook starts no ack timeout and no chain until the window ends (`webhookevents_test.go`, `body_test.go`, `test/e2e/maintenance_delivery_test.go`)."
  - "[C-34.FR-17, C-34.AC-4] An Alert Group that stops being kept back after it resolved is not published: the delivery state of each Destination where it was never published reads \"Not posted: resolved during a Maintenance window\", the Root messages it had before the window get their final update, and `muster_maintenance_released_total{outcome=\"resolved\"}` counts it (`publication_test.go`, `maintenance_integration_test.go`)."
  - "[C-34.FR-18, C-34.AC-5] An open Alert Group that stops being kept back and was never published is published in every Destination it reaches now, with the mark \"Published after a Maintenance window that ended at {time}\": Loudly, with the Mentions of the entries for `new_alert_group`, when firing and nobody owns it, Quietly in its status when acknowledged or snoozed, without a `destination_joined` of its own; its first Publication starts its ack timeout and chains as C-17.FR-1 and C-32.FR-5 say (`publication_test.go`, `test/e2e/maintenance_delivery_test.go`)."
  - "[C-34.FR-18, C-34.AC-5] More Alert Groups than `route.storm_threshold` stopping at once are published each on its own through the limiters, Urgent first, and no Storm summary is posted; a Storm of the Route still active holds those that are not Urgent (`storm_test.go`, `maintenance_integration_test.go`)."
  - "[C-34.FR-19, C-34.AC-9, C-34.AC-12] An open Alert Group published before the window has its Root messages brought to their current state with the mark removed; where it is firing and nobody owns it one Loud Thread reply \"The Maintenance window ended: still firing, {N} alerts\" is sent in each of its Destinations and the lifecycle events recorded meanwhile are not sent one by one; one released because it became Urgent is published at once, Loudly (`threads_test.go`, `maintenance_integration_test.go`)."
  - "[C-34.FR-19, C-34.AC-13, C-26.FR-5] A Destination reached meanwhile by a rise in Severity level or a joined Team records `destination_joined` at the end — Loudly with the Mentions of `new_alert_group` if firing, Quietly if acknowledged or snoozed — for an Alert Group published before the window, and is part of the first Publication, with no `destination_joined`, for one never published (`membership_test.go`, `maintenance_integration_test.go`)."
  - "[C-34.FR-13, C-34.FR-21, C-34.FR-22, C-34.AC-17] `maintenance_started` and `maintenance_ended` reach outgoing webhooks in events mode with the field `kept_back`; `muster_maintenance_released_total{route,outcome}` counts the Alert Groups by `published`, `updated` and `resolved` (`body_test.go`, `metrics_test.go`)."
verify: "make ci test-integration e2e-go docs-check"
operator_attention: false
issue: null
---

# S-192. Nothing new delivered for a kept-back Alert Group, and its publication with the mark when the window ends (BE)

## Scope

**IN**

- What delivery does for an Alert Group that S-191 marks kept back: no Root message, no Thread reply, `destination_joined`
  waits; the Root messages it already has are edited Quietly with the mark "In a Maintenance window until {time}"
  (C-34.FR-11).
- Storms: nothing kept back counts or is summarised (C-34.FR-12); events-mode outgoing webhooks receive every event with
  `kept_back`, template mode is kept back (C-34.FR-12).
- The end: resolved meanwhile is "Not posted", never published (C-34.FR-17); never published and open is published with
  the mark, gradually through the limiters, Urgent first, with no Storm of its own (C-34.FR-18); published before gets its
  Root messages brought to date with one Loud Thread reply, and a Destination reached meanwhile records
  `destination_joined` (C-34.FR-19).
- `muster_maintenance_released_total` and the built-in texts of the marks, in English and Russian.

**OUT**

- The state, rules and the events themselves: S-191. The chains, Notification rules, calls, ack timeout and Reminders:
  S-193 (this story only starts them at the first Publication through the existing `MarkPublished`).
- The pages that show the marks and delivery states: S-195.

## Contracts

- **Spec**: `AlertGroupDelivery` (read through `listAlertGroupDeliveries`, `GET /alert-groups/{id}/deliveries`, not
  through `getAlertGroup`) gains `withheld_reason` (a nullable enum `WithheldReason`, whose only value is
  `resolved_in_maintenance`, "Not posted: resolved during a Maintenance window" in the UI; null for every other
  delivery, a Storm's and a Broken Destination's withheld ones included); `AlertGroupWebhookBody` gains `kept_back`;
  `MaintenanceEndedCause` is S-191's.
- **Schema** (a new migration, expand only; the number is assigned by the orchestrator at dispatch; `schema.md`):
  the mark's time comes from the window, so the migration adds
  `deliveries.published_after_maintenance_at timestamptz` (the end that released it, for the mark on its Root message),
  `deliveries.held_by_maintenance boolean NOT NULL DEFAULT false` (kept back by a window, like `held_by_storm_id` for
  Storms; the Alert Group's own `kept_back` decides, the column keeps the queue index) and
  `deliveries.withheld_reason text` with `deliveries_withheld_reason_check CHECK (withheld_reason IN
  ('resolved_in_maintenance'))` (null passes; the reason is cleared when a delivery comes back from `withheld`). The
  `thread_replies` event CHECK `thread_replies_event_v2_check` (of `0049`) gains `maintenance_ended`, the event of the
  Loud Thread reply of the end, so the migration replaces it with the next version in the order the stories merge,
  added `NOT VALID`, validated in a second transaction and the old one dropped in a third, with every value present when
  it merges. The queue index of due deliveries skips `held_by_maintenance`.
- **Delivery** (`internal/delivery`): `enqueue.go` reads `groups.Rendering.KeptBack` (S-191) and enqueues the Root
  message update only, Quietly, with the mark, for a Destination that has a Root message, and nothing otherwise; the
  creation of a kept-back Alert Group records its deliveries `pending` and `held_by_maintenance`, as a Storm's held ones,
  so that the end only releases them; `membership.go` and `enqueue.go`'s `withJoined` skip `destination_joined` while
  kept back and record it at the end for an Alert Group published before; `worker.go` and `pages.go` send no Thread reply
  and no Page of a kept-back Alert Group — a reply due while kept back is dropped, not delayed, and the Timeline has the
  story — and the interactive path (`interactive.go`) answers presses as usual; `storm.go` excludes a kept-back Alert
  Group from the count and the summaries; `publication.go` settles the resolved ones as `withheld` with the reason and
  gives the final update to the Root messages they had; `threads.go` holds the one Loud Thread reply of the end;
  `webhookevents.go` and `internal/webhooks/body.go` send events mode as usual with `kept_back`.
- **Release** (`internal/groups/maintenance.go`, S-191's transitions): `MaintenanceEnded` hands delivery a
  `groups.Rendering` with `Released` (the cause, the window's end, whether it resolved, whether it was ever published); the
  decision of FR-17 to FR-19 is made in `Enqueue` from that, in the dispatcher's transaction. Publications that follow
  go through the existing limiters and `urgent` ordering of C-11.FR-3; a release never calls `joinStorm`.
- **Messages** (`internal/messages/texts/en.json` and `ru.json`, the built-in texts, both languages): the marks "In a
  Maintenance window until {time}" and "Published after a Maintenance window that ended at {time}" as `notice.*` keys
  rendered like the existing notes (`LateNote` and its neighbours in `render.go`, tested in `render_test.go` and
  `default_test.go`), and the Thread reply "The Maintenance window ended: still firing, {N} alerts" as a `reply.*` key
  built in `replies.go`, with the time in the Destination's or the Organization's time zone as other times of a Root
  message. The marks are part of the `layout` a bridge receives (`layout.go`, C-29.FR-8): the Root message's data, not
  only its rendered text, carries them, as S-205 does for the line of an attached Alert Group.
- **Metrics** (`internal/metrics/catalogue.go`): `muster_maintenance_released_total{route,outcome}`; the reference pages
  regenerate.
- **Events documentation** (`docs/outgoing-webhooks/events.md`): the two events and the field `kept_back`.
- **Settings**: `route.storm_threshold`, `route.ack_timeout`, the limiters of the Connections.
- **Live updates**: the `HintEvent` of S-191 triggers the page refresh; nothing new.

## Steps

1. Write the migration and `schema.md`. Check: `make test-integration` runs up, down and up.
2. Enqueue, the edits with the mark, and no replies, Pages or `destination_joined` while kept back. Check:
   `enqueue_test.go`, `worker_test.go`, `render_test.go`, `membership_test.go`.
3. Exclude from Storms and send events mode with `kept_back`. Check: `storm_test.go`, `webhookevents_test.go`,
   `body_test.go`.
4. The release: Not posted, the publication with the mark through the limiters, the Thread reply, `destination_joined`.
   Check: `publication_test.go`, `threads_test.go`, `maintenance_integration_test.go` with more groups than the Storm
   threshold.
5. Texts, metric, events documentation and `test/e2e/maintenance_delivery_test.go`. Check: the Verification below;
   `make docs-check`.

## Verification

```sh
make dev > dev.log 2>&1 &
API=localhost:8080/api/v1; FAM=127.0.0.1:18065/_fake; CLK=localhost:8082/_dev/clock
# a window on the Route R, now +30 min; an Alert Group G1 of R created during it
# G1's alertname is PaymentsAPIDown; the fake records every request, so filter by what only G1 puts in a post
curl -s $FAM/requests | jq '[.[] | select(.path | test("posts")) | select(.body | contains("PaymentsAPIDown"))] | length'   # 0: nothing posted for G1
curl -s -H "$A" $API/alert-groups/$G1/deliveries | jq -c '[.items[] | .state]'   # nothing published; ["pending"]
# G1 still firing at the end: the clock passes the end
curl -s -X POST $CLK -d '{"advance_seconds": 1860}' > /dev/null; sleep 2
curl -s $FAM/requests | jq -r '.[-1].body' | grep -c 'Published after a Maintenance window that ended at'   # 1
curl -s -H "$A" "$API/alert-groups/$G1/timeline?kind=system" | jq -c '[.items[] | {e: .event, l: .loudness}] | last'
# {"e":"maintenance_ended","l":"loud"}  (the Mentions of the entries for new_alert_group are in the post)
# G2 resolved during the window: never posted
curl -s -H "$A" $API/alert-groups/$G2/deliveries | jq -c '[.items[] | {state, withheld_reason}]'   # [{"state":"withheld","withheld_reason":"resolved_in_maintenance"}]
curl -s localhost:8082/metrics | grep 'muster_maintenance_released_total'   # outcome="published" 1, outcome="resolved" 1
# G3, published before the window: its Root message shows the mark, and at the end one Loud Thread reply
curl -s $FAM/requests | jq -r '.[] | select(.path | test("posts")) | .body' | grep -c 'The Maintenance window ended: still firing, 2 alerts'   # 1
# Storm: more groups than route.storm_threshold released at once -> no summary
curl -s $FAM/requests | jq '[.[] | select(.body | test("Storm"))] | length'   # 0
```

`test/e2e/maintenance_delivery_test.go` repeats these checks and adds: an acknowledged Alert Group published Quietly in
its status, a press of Acknowledge on an existing Root message during the window, a webhook Destination in events mode
receiving `created`, `maintenance_started` and `maintenance_ended` with `kept_back` and starting no chain (C-34.AC-14), a
rise in Severity level during the window for an Alert Group never published and one published before (C-34.AC-13), the
release by `urgent_passes`, and the Telegram and Slack fakes.

## Open questions

1. **The mark's time on a Root message.** The mark says "ended at {time}"; for an early close that is the close time. The
   contract of this story takes the window's `ends_at` after the close (S-190 sets it to now); no PRD change.
2. **Columns and the spec field.** `deliveries.published_after_maintenance_at`, `deliveries.held_by_maintenance` and
   `deliveries.withheld_reason` with its value `resolved_in_maintenance`, and the field `withheld_reason` of
   `AlertGroupDelivery` (the existing schema has no reason of the `withheld` state at all), are named here, since the PRD
   names none; the maintainer confirms the names in review of the story, as S-195 reads them.

## Notes

- Suggested commit: `feat(delivery): hold back the messages of kept-back alert groups and publish them with a mark when the window ends`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases. The migration number is
  assigned by the orchestrator at dispatch; it replaces `thread_replies_event_v2_check` with the next version in the
  order the stories merge (it gains `maintenance_ended`) and adds `deliveries_withheld_reason_check`.
- The Thread replies due while kept back are dropped, not delayed (C-34.FR-11), unlike a Storm's held replies; the
  Timeline records what happened.
- Interim until S-193: chains and the ack timeout of an Alert Group published before the window still run while it is kept
  back.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-34.FR-10 | partial | the buttons of existing Root messages answered on the interactive path; the rest is S-191 |
| C-34.FR-11 | full | |
| C-34.FR-12 | full | |
| C-34.FR-13 | partial | the messenger and webhook effects of the events; the events are S-191 |
| C-34.FR-17 | partial | the rule and the reason `withheld_reason`; the delivery state's text in the UI is completed by S-195 |
| C-34.FR-18 | partial | the publication with the mark; the chains that start at it are S-193 |
| C-34.FR-19 | partial | the Thread reply and `destination_joined`; the chains starting over are S-193 |
| C-34.FR-21 | partial | `kept_back` in webhook events |
| C-34.FR-22 | partial | `muster_maintenance_released_total` |
| C-34.AC-3 | partial | nothing delivered and no Storm; the state and Timeline are S-191's, the chain step that does not run is completed by S-193 |
| C-34.AC-4 | partial | "Not posted" and the final update; the Quiet `maintenance_ended` is S-191's |
| C-34.AC-5 | partial | the publications and the Storm; the chain and the ack timeout notice are S-193 |
| C-34.AC-9 | partial | the Root message and Thread reply; the chain is S-193 |
| C-34.AC-11 | partial | the button of a Root message answered on the interactive path; the Snooze and the Command from the API are S-191's |
| C-34.AC-12 | partial | the Loud publication; the release and its event are S-191's |
| C-34.AC-13 | full | |
| C-34.AC-14 | partial | the events; "starts no chain" is checked with S-193 |
| C-34.AC-17 | partial | `muster_maintenance_released_total`; `muster_maintenance_kept_back_total` and the log events are S-191's and S-190's |
| C-26.FR-5 | partial | `destination_joined` waits for the end of a Maintenance window |
