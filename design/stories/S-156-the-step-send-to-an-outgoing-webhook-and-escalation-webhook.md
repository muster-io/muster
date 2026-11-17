---
id: S-156
title: The step "Send to an outgoing webhook" and the event escalation_webhook (BE)
capability: C-32
kind: be
layer: L2
depends_on: [S-169]
covers: [C-32.FR-3, C-32.FR-13, C-32.FR-14, C-32.AC-9, C-32.AC-11, C-15.FR-2, C-09.FR-22]
files_touched:
  - api/openapi.yaml
  - internal/escalation/webhook.go
  - internal/escalation/webhook_test.go
  - internal/escalation/engine.go
  - internal/groups/escalation.go
  - internal/groups/events.go
  - internal/groups/events_test.go
  - internal/delivery/webhookevents.go
  - internal/delivery/webhookevents_test.go
  - internal/delivery/query.sql
  - internal/webhooks/body.go
  - internal/webhooks/body_test.go
  - test/e2e/escalation_webhook_test.go
  - docs/outgoing-webhooks/events.md
  - docs/concepts/escalation-chains.md
  - internal/delivery/enqueue.go
  - internal/delivery/enqueue_test.go
  - internal/groups/query.sql
  - internal/groups/read.go
  - internal/api/alertgroups.go
  - web/src/components/timeline.tsx
  - internal/escalation/query.sql
  - internal/escalation/runs.go
  - internal/groups/dispatcher.go
  - internal/escalation/webhook_integration_test.go
  - internal/delivery/webhookevents_integration_test.go
  - internal/delivery/reach_integration_test.go
  - internal/api/escalationruns_test.go
  - design/db/schema.md
acceptance:
  - "[C-32.FR-13, C-32.AC-9, C-32.FR-3] A `webhook` step sends one event `escalation_webhook` — the Alert Group as in every event, `chain` and `step` — to the outgoing webhook Destination it names, in events mode or both, whether or not the Alert Group is delivered there; that Destination, on none of the Alert Group's Routes or Teams, receives no other event of the Alert Group; the event is signed with its Signing secret, ordered by the Alert Group's event sequence and delivered at least once like the other events of C-15.FR-2 (`webhook_test.go`, `webhookevents_test.go`, `escalation_webhook_test.go`)."
  - "[C-32.FR-13] A Destination that is also one of the Alert Group's receives `escalation_webhook` among its other events, in order (`webhookevents_test.go`)."
  - "[C-32.FR-13, C-32.AC-11, C-09.FR-22, C-15.FR-2] The step records the Quiet `escalation_webhook` (Timeline kind `escalation`, nothing in a messenger) with the chain, the step and the Destination; outgoing webhooks in events mode other than the step's do not receive it; the table-driven test of the L2 lifecycle event table covers every row from here on (`events_test.go`, `body_test.go`, `webhookevents_test.go`)."
  - "[C-32.FR-14] A `webhook` step whose Destination was deleted, or is no longer an outgoing webhook in events mode or both, is skipped with `escalation_step_skipped` and the reason `destination_deleted` or `webhook_events_required` (`webhook_test.go`)."
  - "[C-32.FR-13] Outgoing webhooks → Events documents `escalation_webhook` with an example body, and Concepts → Escalation chains the step; `make docs-check` passes."
verify: "make ci test-integration e2e-go docs-check"
operator_attention: false
issue: 445
---

# S-156. The step "Send to an outgoing webhook" and the event escalation_webhook (BE)

## Scope

**IN**

- The `webhook` step: the event `escalation_webhook` to the outgoing webhook Destination the step names, signed,
  ordered and delivered at least once, and to no other Destination (C-32.FR-13; C-15.FR-2 revised).
- Its Timeline entry, and the skips of a step whose Destination is gone or no longer takes events (C-32.FR-14, S-154's
  Open question 1).

**OUT**

- The step's configuration: S-154. Its form: S-158.

## Contracts

- **Spec** (`api/openapi.yaml`): `LifecycleEvent` and `WebhookEventName` gain `escalation_webhook`;
  `OutgoingWebhookEvent` carries on that event S-169's `escalation_step` (`WebhookEscalationStep`: `chain`, `step`,
  `reason` null — `EscalationSkipReason` is null for a step that was made — and `users` empty), and S-121's optional
  `destination` {`id`, `name`, `type`} — the step's Destination;
  `TimelineEscalationEntry` gains `destination` (`DestinationRef`), which `internal/groups/read.go` builds from S-121's
  `timeline_entries.destination_id` column, as S-121 does for `TimelineSystemEntry`; `EscalationSkipReason` gains
  `destination_deleted` and `webhook_events_required`.
- **Schema**: none new — the Timeline accepts the event since `_v4` and `webhook_events` since S-148; a row of
  `webhook_events` for a Destination that has no delivery of the Alert Group is allowed as it is.
- **Packages**: as S-169 and S-155 state them; `escalation` reads the Destination through its own query and imports
  neither `delivery` nor `webhooks`.
- **Escalation** (`internal/escalation/webhook.go`, `engine.go`): the engine hands a `webhook` step to it, which reads
  the Destination without a lock (`GetStepDestination`) and hands the webhook to `groups`' `ChainStep` in its
  `ChainProgress`, which records the event through the system transition `chainSteps`; a Destination that is gone or
  not in events mode or both skips the step.
- **Groups** (`internal/groups/escalation.go`, `events.go`): the row of `escalation_webhook` — Quiet, no Mentions, kind
  `escalation`, nothing in a messenger, and, unlike every other event, sent only to the step's Destination.
- **Delivery** (`internal/delivery/webhookevents.go`, `query.sql`): the event is enqueued for the step's Destination
  only, with the Alert Group's lifecycle event `sequence`, as every event carries, and the events-mode fan-out skips it
  for every other Destination; no event is inserted for a deleted Destination; its event does not make the step's
  Destination one the Alert Group reached (`ListGroupDestinations`), and its delivery marks no first Publication.
- **Timeline read and SPA** (`internal/groups/query.sql`, `read.go`, `internal/api/alertgroups.go`,
  `web/src/components/timeline.tsx`): the `destination` of the entry read into `TimelineEscalationEntry`, and a case
  for `escalation_webhook` in `entryText` with S-148's generic text.
- **Delivery's event table** (`internal/delivery/enqueue.go`): the row of `escalation_webhook`, sent only to its
  Destination; `enqueue_test.go` counts it.
- **Webhooks** (`internal/webhooks/body.go`): the body of `escalation_webhook` — the Alert Group as in every event —
  with `escalation_step` and `destination`; S-168 adds the Assignees to the `alert_group` of every event, this one
  included.
- **Log events, metrics**: none new; the deliveries of webhook events keep their own.
- **Documentation**: `docs/outgoing-webhooks/events.md` — `escalation_webhook` with an example and the note that only
  the step's Destination receives it; Concepts → Escalation chains — the step.

## Steps

1. Add the event to the spec and its row to the closed table. Check: `make generate-check`; `events_test.go`.
2. Write the step with its checks and skips. Check: `webhook_test.go`.
3. Enqueue the event for the step's Destination only. Check: `webhookevents_test.go`, `body_test.go`.
4. Write `test/e2e/escalation_webhook_test.go` and the documentation. Check: the Verification below; `make docs-check`.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-169: A, the Route "Infrastructure" (R) with the chain "devops" (CD), the Integration "sev" with NOTIFY, PUTA,
# AG; the outgoing webhook Destination "ops-bot" (WH) in events mode, on no Route, posting to
# http://127.0.0.1:18093/hook/ops-bot of the fake receiving endpoint of muster dev, whose Signing secret is
# registered there with PUT /_fake/secrets/ops-bot (S-044).
API=localhost:8080/api/v1; FWH=127.0.0.1:18093/_fake
E=$(curl -s "${A[@]}" $API/escalation-chains/$CD | jq -r .etag)
curl -s "${A[@]}" -X PUT -H "If-Match: $E" $API/escalation-chains/$CD -d "$(curl -s "${A[@]}" $API/escalation-chains/$CD |
  jq -c "{name, description, owning_team_id: .owning_team.id, steps: ([{kind: \"webhook\", destination_id: \"$WH\"}] +
  [.steps[] | {kind, importance, schedule_id: .schedule.id, wait_seconds}])}")" > /dev/null
curl -s -X PUT $FAM/groups/w1 -d '{"receiver":"sev","route":"{}","labels":{"alertname":"NodeDown"}}' > /dev/null
PUTA w1 a '{"labels":{"category":"infra","system":"k8s","severity":"critical"}}'; NOTIFY w1 '{"reason":"first notification"}'; sleep 2
curl -s $FWH/received/ops-bot | jq -c '[.[] | .body | {event, chain: .escalation_step.chain.name, step: .escalation_step.step}]'
# [{"event":"escalation_webhook","chain":"devops","step":1}]
curl -s $FWH/received/ops-bot | jq -r '.[0].signatures_valid'                               # 1
```

`test/e2e/escalation_webhook_test.go` repeats these checks and adds a Destination that is also one of the Route's,
which receives the event in order among the others, a Destination deleted after the step was saved (skipped
`destination_deleted`), and the Timeline entry.

## Open questions

None.

## Notes

- Corrected in the pull request (2026-11-17):
  - The chain and the step of `escalation_webhook` travel in S-169's `escalation_step`, not in a new `escalation`
    field: S-169 shipped `escalation_step` on `escalation_step_skipped`, and one field for the escalation events keeps
    them consistent. Its `reason` stays required and is null on `escalation_webhook`.
  - The Verification reads `.body` as it is, since the fake receiving endpoint records the body parsed as JSON, and
    `.escalation_step`.
  - `files_touched`: the Destination is read through escalation's own query (`internal/escalation/query.sql`,
    `GetStepDestination`, a read without a lock, which would close cycles with the delivery worker and with a Team
    save), which the run's queries in `runs.go` declare, with its integration test
    (`internal/escalation/webhook_integration_test.go`); `internal/groups/dispatcher.go` documents
    that a recorded event's Destination is also that of `escalation_webhook`; `internal/groups/query.sql` changes only
    the comment of `ListTimelineDestinations`, as the Timeline already reads `timeline_entries.destination_id` for every
    entry.
  - Tests added beyond the list: `internal/delivery/webhookevents_integration_test.go` (`InsertWebhookEvent` on
    PostgreSQL) and a case in `reach_integration_test.go` (`ListGroupDestinations`), and the API mapping of the entry's
    `destination` in `internal/api/escalationruns_test.go`.
  - `design/db/schema.md` names the exception to "a deleted Destination's pending events end as `not_delivered`": the
    `escalation_webhook` of a step whose Destination's deletion commits while the step runs may stay pending, never
    claimed, until its Alert Group is removed.
  - `internal/delivery/query.sql`: `ListGroupDestinations` leaves out the events of `escalation_webhook`, without which
    the step's Destination would receive every later event of the Alert Group; `InsertWebhookEvent` inserts nothing
    for a deleted Destination, which narrows a deletion that races the step (at worst the `escalation_webhook` events
    of that one firing stay pending, never claimed).
  - There is no Command `EscalationWebhook`: the Escalation contract says the event is recorded through `ChainStep`,
    by the system transition `chainSteps`.
  - The Delivery contract said "the Alert Group's next sequence of that Destination"; the event carries the Alert
    Group's lifecycle event `sequence`, as every event does.
- Suggested commit: `feat(escalation): send escalation_webhook from a step to its outgoing webhook`.
- The fake receiving endpoint is the one `muster dev` already serves (S-044); this story adds nothing to it.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-32.FR-3 | partial | the `webhook` step |
| C-32.FR-13 | full | |
| C-32.FR-14 | partial | a webhook step whose Destination is gone or takes no events |
| C-32.AC-9 | full | |
| C-32.AC-11 | partial | `escalation_webhook`; with S-155 and S-168 every row of the table is covered |
| C-15.FR-2 | partial | revised for L2: `escalation_webhook` only to its own Destination |
| C-09.FR-22 | partial | revised for L2: `escalation_webhook` |
