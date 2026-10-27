---
id: S-148
title: The queue of calls through Notification gateways with combining and retries (BE)
capability: C-30
kind: be
layer: L2
depends_on: [S-167]
covers: [C-30.FR-2, C-30.FR-4, C-30.FR-5, C-30.FR-7, C-30.FR-10, C-30.AC-3, C-30.AC-7, C-11.FR-2, C-09.FR-22, C-12.FR-3, C-15.FR-2, C-32.FR-19]
files_touched:
  - api/openapi.yaml
  - api/README.md
  - internal/db/migrations/0037_gateway_calls.up.sql
  - internal/db/migrations/0037_gateway_calls.down.sql
  - design/db/schema.md
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - internal/publicid/publicid.go
  - internal/gateways/calls.go
  - internal/gateways/calls_test.go
  - internal/gateways/sender.go
  - internal/gateways/sender_test.go
  - internal/gateways/calls_integration_test.go
  - internal/gateways/query.sql
  - internal/buttons/buttons.go
  - internal/buttons/buttons_test.go
  - internal/groups/pages.go
  - internal/groups/events.go
  - internal/groups/events_test.go
  - internal/webhooks/body.go
  - internal/webhooks/body_test.go
  - internal/messages/texts/en.json
  - internal/messages/texts/ru.json
  - internal/archlint/archlint.go
  - internal/metrics/catalogue.go
  - internal/logging/events.go
  - internal/runtime/runtime.go
  - internal/devmode/devmode.go
  - internal/devmode/devmode_test.go
  - design/prd/l1/reference.md
  - test/e2e/gateway_calls_test.go
  - AGENTS.md
  - internal/delivery/enqueue.go
  - internal/delivery/enqueue_test.go
  - internal/groups/query.sql
  - internal/groups/read.go
  - internal/api/alertgroups.go
  - web/src/components/timeline.tsx
  - web/src/locales/en.json
  - web/src/locales/ru.json
acceptance:
  - "[C-30.FR-4, C-30.AC-3] A Page through a gateway to a person with a confirmed contact there is a row of `gateway_calls` with a `public_id`, its Alert Groups and `next_attempt_at`; the first call to a person through a gateway goes at once; of three Pages to one person within `gateway.combine_window`, the first reaches the fake gateway at once and the other two as one call with both Alert Groups once the window after the first one ended has passed; when the second Alert Group is acknowledged meanwhile, that call is about the third only; a call whose Alert Groups were all acknowledged before its first attempt is not sent and ends `dropped`; after a call that ends Not delivered, the next one goes once the window after it has passed; calls about critical Alert Groups are claimed first; Mentions are never joined (`calls_test.go`, `calls_integration_test.go`)."
  - "[C-30.FR-4, C-30.AC-3, C-11.FR-2] A call that the fake gateway answers `transient` is retried with the same `Idempotency-Key` and the same text within `delivery.transient_backoff` and `delivery.transient_budget` until it is accepted, through the Connection's limiter, on any replica that claims it; once every Alert Group of a call being retried has stopped, its retries end and it ends `dropped`; a call takes no Alert Group after its first attempt (`sender_test.go`)."
  - "[C-30.FR-2, C-30.FR-5, C-30.AC-3, C-12.FR-3] The text of a call names each Alert Group by number, title and Severity level and says how to acknowledge, in the language of the person's profile, in English and Russian; its action data names the call — the subject `call` with the call's `public_id`, signed with the button-signature key — and is at most 64 bytes for a call about any number of Alert Groups; lint 3 allows `Notify` in `internal/gateways/sender.go` (`buttons_test.go`, `sender_test.go`)."
  - "[C-30.FR-7, C-30.AC-7, C-09.FR-22, C-15.FR-2, C-32.FR-19] A call that the fake gateway answers with the class `invalid`, an unknown answer or a used-up budget ends Not delivered and records, for each of its Alert Groups, the Quiet `page_not_delivered` (Timeline kind `escalation`) naming the person in `paged_users` and the gateway, with the reason; outgoing webhooks in events mode receive it (`pages.go` via `events_test.go`, `body_test.go`)."
  - "[C-30.FR-10] Every ended attempt of a call counts in `muster_gateway_calls_total{connection,outcome}` with `accepted`, `retry_after`, `transient`, `fatal` or `unknown`; reference.md moves `muster_gateway_calls_total` into the catalogue (`catalogue.go`, `metrics_test.go`)."
  - "[C-30.FR-4] Under `muster dev`, `POST localhost:8082/_dev/gateway-calls` with a User, a gateway and Alert Groups queues their Pages as a Notification rule's call would, and is refused outside development mode (`devmode_test.go`)."
verify: "make ci test-integration e2e-go"
operator_attention: false
issue: 437
---

# S-148. The queue of calls through Notification gateways with combining and retries (BE)

## Scope

**IN**

- Calls as rows of their own — a person, a gateway, the Alert Groups it is about, a lease — claimed by any replica and
  sent at least once under an `Idempotency-Key` through the gateway Connection's limiter, critical first, classified and
  retried in the delivery client class, outside reconciliation (C-30.FR-4, ADR-0005; C-11.FR-2 revised).
- Combining: the first call goes at once, later Pages join the next call, which waits for `gateway.combine_window`
  after the previous one ended; Alert Groups that stopped are dropped before the first attempt, and retries end once
  all have stopped (C-30.FR-4).
- The text of a call and its signed action data naming the call (C-30.FR-2, FR-5).
- `page_not_delivered` for a call that ends Not delivered (C-30.FR-7), and the expand step of the Timeline for the
  events of C-30 and C-32 (C-32.FR-19).
- The metrics and log events of calls (C-30.FR-10), and a development hook that queues a call for the live check.

**OUT**

- `notification_acknowledged` acting on Alert Groups, the Acknowledge of the Transport `gateway` and its answers:
  S-173.
- Broken gateways, their probe and the calls due while Broken: S-149.
- The calls that Notification rules and Escalation steps make, `paged` for a call, and what "stopped" means for a
  chain or a rule that started over: S-157. Until then the hook of `muster dev` and the tests queue calls directly, as
  C-30's acceptance says, and an Alert Group "stops" when it is acknowledged, snoozed or resolved.
- The Assignees a call makes: S-157 with S-168.

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated):
  - `LifecycleEvent` gains `page_not_delivered` (`x-event-kinds` `escalation`) and the
    Timeline kind `escalation`; `TimelineEscalationEntry` {`paged_users` (`UserRef`[]), `gateway` (`EntityRef` or null),
    `reason` (`PageNotDeliveredReason`: `call_refused`, `call_unknown`, `call_budget_used`, `no_confirmed_contact`, more
    from S-149, S-155 and S-157)}; `WebhookEventName` gains `page_not_delivered`, and `OutgoingWebhookEvent` gains the
    optional `page` {`users`, `gateway`, `reason`}, set on that event only, as `test` is on its own.
  - `PublicId` and `api/README.md` gain the prefix `CA` (call).
- **Schema** (migration `0037_gateway_calls`, expand only; `schema.md` §4.9, §4.13, §4.17, §5):
  - `gateway_calls`: `id`, `org_id`, `public_id` (`CHECK (public_id ~ '^CA[0-9A-HJKMNP-TV-Z]{12}$')`), `connection_id`,
    `user_id`, `contact_id` (FK `gateway_contacts`, `ON DELETE SET NULL`, so that S-167's erasure and
    `deleteMyGatewayContact` leave the call's record; set at the first attempt), `state` (`CHECK IN ('waiting',
    'sending', 'accepted', 'not_delivered', 'dropped')`), `critical boolean`, `language`, `text`, `not_before`,
    `next_attempt_at`, `lease_owner`, `lease_until`, `attempts`, `first_attempt_at`, `first_failed_at`,
    `last_error_class` (the classes of deliveries), `last_error`, `notification_id`, `reason`, `ended_at`, `created_at`;
    a CHECK on the lease; a unique partial index `(connection_id, user_id) WHERE state = 'waiting'` — at most one call
    waits per person and gateway; the claim index `(org_id, next_attempt_at) WHERE state IN ('waiting', 'sending')`.
    `not_before`, `next_attempt_at`, `ended_at` and the combine window are times of the business clock, which the
    development clock moves, like the delivery worker's `next_attempt_at`; `lease_until` is on the real clock, like
    every row lease (S-020).
  - `gateway_call_items`: `org_id`, `call_id` (FK, cascade), `alert_group_id` (FK, cascade), `queued_at`,
    `dropped_at`; primary key `(call_id, alert_group_id)`.
  - `timeline_entries`: the CHECKs of events, event kinds and mentions, and the `kind` CHECK, are replaced by `_v4` ones
    that add the kind `escalation` with every event of the [L2 lifecycle event
    table](../prd/l1/reference.md#lifecycle-events-added-in-l2) of that kind — `paged`, `page_not_delivered`,
    `escalation_step_skipped`, `assignees_changed`, `escalation_webhook` — and the symbolic Mention `paged`, `NOT
    VALID`, validated in a second transaction, the old ones dropped in a third, as S-121's `_v3`; the later stories of
    C-32 record their events without a new version. New columns: `paged_user_ids bigint[]` (the people a Page names,
    C-32.FR-19) and `gateway_connection_id`.
  - `timeline_entries_transport_v4_check` and `audit_log_transport_v4_check` add `gateway`, as `0018` added `slack`,
    for the Acknowledge of S-173, so that it needs no migration.
  - `webhook_events_event_v4_check` adds the five `escalation` events above to S-121's `_v3`, in the same three
    transactions.
  - The down migration drops the tables and columns and restores the previous CHECKs after deleting the rows they
    refuse.
- **Gateways** (`internal/gateways`):
  - `calls.go` — `Page(ctx, tx, PageCall{user, gateway, alert_group, stop StopCheck})`: joins the waiting call of that
    person and gateway, or creates one whose `not_before` is the end of the previous call plus `gateway.combine_window`,
    now when there is none or it ended longer ago, and unset while the previous call is still being sent (set when it
    ends); `StopCheck` is declared here, and until S-157 the only one is "the Alert Group is no longer firing".
  - `sender.go` — a worker on every replica (NFR-4) that claims due calls with `FOR UPDATE SKIP LOCKED` and a lease,
    critical first, then by `next_attempt_at`; before the first attempt it drops stopped Alert Groups and ends a call
    with none `dropped`; at the first attempt it fixes `contact_id`, the text and the key (`calls/<public_id>`); it
    calls `delivery.Gateway.Notify` through the Connection's limiter in the delivery client class and records the
    outcome: `accepted`; retried within `delivery.transient_backoff` and `delivery.transient_budget`; `dropped` once
    every Alert Group stopped; Not delivered with its reason, which records `page_not_delivered` for each Alert Group
    through the dispatcher. A person without a confirmed contact at the first attempt ends the call Not delivered with
    `no_confirmed_contact`. Calls are never reconciled (ADR-0005).
- **Timeline read** (`internal/groups/query.sql`, `read.go`, `internal/api/alertgroups.go`): the kind `escalation`,
  `paged_user_ids` and `gateway_connection_id` are read into `TimelineEscalationEntry` by
  `getAlertGroupTimeline`.
- **Delivery's event table** (`internal/delivery/enqueue.go`): the row of `page_not_delivered` (nothing in a
  messenger), since `Enqueue` refuses an event without a row; `enqueue_test.go` counts `delivery.Table` again.
- **SPA** (`web/src/components/timeline.tsx`, locales): `entryText` gains the kind `escalation`, whose branch has a case
  for each of its events that exists, here `page_not_delivered`, with the generic text `timeline.escalation.event`
  "{{event}}" / «{{event}}», so that the TypeScript check passes after `make generate`; S-155, S-156, S-168 and S-169
  add a case per event they add, and S-159 renders them fully.
- **Buttons** (`internal/buttons`): the subject `call` (code `c`), whose public id is a call's; `LooksSigned` knows it.
- **Groups** (`internal/groups`): `pages.go` — the system transition `PageNotDelivered` that records
  `page_not_delivered`; `events.go` — its row (Quiet, no Mentions, kind `escalation`, nothing in a messenger).
- **Packages**: `gateways` imports `groups` and `delivery` and none of the packages that import it; `groups` imports
  no package of Paging (S-169 states the direction for all of them).
- **Webhooks** (`internal/webhooks/body.go`): `page_not_delivered` in events mode, with `page`.
- **Texts** (`internal/messages/texts`): the call ("Muster: {count} Alert Group(s) need you.", "#{number} {title},
  {severity}", "Press 1 to acknowledge."), in English and Russian with their plurals (C-12.FR-3
  revised).
- **Lint 3** (`internal/archlint/archlint.go`): `internal/gateways/sender.go` joins the files allowed to call
  `delivery.Gateway.Notify`.
- **Metrics** (`internal/metrics/catalogue.go`): `muster_gateway_calls_total{connection,outcome}` (counter, C-30);
  reference.md moves it into the catalogue.
- **Log events** (`internal/logging/events.go`): `gateway_call_queued` (debug), `gateway_call_attempted` (info; the
  call, the Connection, the outcome class), `gateway_call_not_delivered` (warn; the reason), `gateway_call_dropped`
  (info), C-30; none carries the contact or the text.
- **Development mode** (`internal/devmode/devmode.go`): `POST /_dev/gateway-calls` {`user_id`, `connection_id`,
  `alert_group_ids`} on the internal listener, served only under `muster dev` like `/_dev/clock`, calls `gateways.Page`
  for each Alert Group with the stop check of this story.
- **AGENTS.md**: the paragraph on development mode names the hook `POST /_dev/gateway-calls` of `muster dev`.
- **Settings**: `gateway.combine_window` (P-87, which stays provisional until the test environment confirms it),
  `connection.bridge.limiter`, `delivery.transient_backoff`, `delivery.transient_budget`, `delivery.interactive_budget`.

## Steps

1. Write the migration and `schema.md`. Check: `make test-integration` runs up, down and up; the `_v4` CHECKs accept
   every row of `_v3` and the new kind and events.
2. Write the queue with combining and the stop check. Check: `calls_test.go` with a manual clock covers the first call,
   joining a waiting call, the window after an accepted and after a Not delivered call, a call being retried, and the
   drop before the first attempt.
3. Write the sender, the text and the signed data. Check: `sender_test.go`, `buttons_test.go`; `make lint-arch`.
4. Record `page_not_delivered`, the webhook event, the metrics and the log events; add the hook of `muster dev`. Check:
   `events_test.go`, `body_test.go`, `devmode_test.go`; `calls_integration_test.go` runs two senders on two replicas.
5. Write `test/e2e/gateway_calls_test.go`. Check: the Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-167: A, petr's confirmed contact +10000000002 at the demo gateway GW; as in S-118: the Integration "sev" with
# NOTIFY, PUTA and AG; a Route with a Mattermost Destination; CLK; ivan, another Responder, with his token I.
API=localhost:8080/api/v1; FB=127.0.0.1:18094/_fake; FAM=127.0.0.1:19093/_fake; CLK=localhost:8082/_dev/clock
PETR=$(curl -s "${A[@]}" "$API/users?q=petr" | jq -r '.items[0].id')
for n in 1 2 3; do curl -s -X PUT $FAM/groups/g$n -d "{\"receiver\":\"sev\",\"route\":\"{}\",\"labels\":{\"alertname\":\"Disk$n\"}}" > /dev/null
  PUTA g$n a "{\"labels\":{\"svc\":\"gw$n\",\"severity\":\"critical\"}}"; NOTIFY g$n '{"reason":"first notification"}'; done; sleep 1
G1=$(AG 'svc%3D%22gw1%22'); G2=$(AG 'svc%3D%22gw2%22'); G3=$(AG 'svc%3D%22gw3%22')
CALL() { curl -s -X POST localhost:8082/_dev/gateway-calls -d "{\"user_id\":\"$PETR\",\"connection_id\":\"$GW\",\"alert_group_ids\":[\"$1\"]}"; }
CALL $G1; sleep 1; curl -s $FB/notifications | jq length                                     # 1
CALL $G2; CALL $G3; curl -s "${I[@]}" -X POST $API/alert-groups/$G2/acknowledge > /dev/null
curl -s -X POST $CLK -d '{"advance_seconds": 61}' > /dev/null; sleep 1
curl -s $FB/notifications | jq -r 'last.text'
# Muster: 1 Alert Group needs you. #… Disk3, critical. Press 1 to acknowledge.
curl -s $FB/notifications | jq -r 'last.action.data | length <= 64'                            # true

# a call refused as invalid ends Not delivered
curl -s -X POST $FB/faults -d '{"path":"/v1/notifications","status":400,"times":1,
  "body":"{\"type\":\"about:blank\",\"title\":\"Bad number\",\"status\":400,\"class\":\"invalid\"}"}' > /dev/null
curl -s -X POST $CLK -d '{"advance_seconds": 61}' > /dev/null; CALL $G1; sleep 2
curl -s "${A[@]}" "$API/alert-groups/$G1/timeline?kind=escalation" | jq -c '[.items[] | {event, u: [.paged_users[].login], g: .gateway.name, reason}]'
# [{"event":"page_not_delivered","u":["petr"],"g":"Example Gateway","reason":"call_refused"}]
curl -s localhost:8082/metrics | grep -c '^muster_gateway_calls_total{'                        # 2 or more
```

`test/e2e/gateway_calls_test.go` repeats these checks and adds a call retried after `transient` with the same key and
text, the retries that end once its Alert Groups were acknowledged, a call whose Alert Groups were all acknowledged
before its first attempt, critical calls claimed first, and two replicas (`E2E_REPLICAS=2`) sending each call once.

## Open questions

None.

## Notes

- Suggested commit: `feat(gateways): queue calls through notification gateways with combining and retries`.
- The Acknowledge through a gateway was split off into S-173 when the review added the Timeline read path and the
  SPA's switches to this story; S-173 follows it.
- The Timeline's `_v4` takes all five `escalation` events at once, because they are one closed group of the L2 event
  table; S-152 to S-157 add only the columns they need.
- `paged` for a call (Quiet), the call's Assignees and "stopped" for a chain or a rule that started over come with
  S-157, which also passes its own `StopCheck` to `gateways.Page`.
- The hook of `muster dev` is a development aid like the clock; it is no product behaviour and is never served outside
  `muster dev`.
- P-87 stays provisional; the test environment confirms it.
- Migration `0037` follows the story map; a story merged out of that order renumbers its migration (S-167's Notes).

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-30.FR-2 | partial | the action data that names the call; the protocol is S-145, the event's handling S-173 |
| C-30.FR-4 | partial | the queue, combining, retries and the drop; the calls of rules and steps are S-157 |
| C-30.FR-5 | partial | the text and the sender's place under lint 3; the interface is S-145 |
| C-30.FR-7 | partial | `page_not_delivered` for a call; that the chain and the rules go on is S-157 |
| C-30.FR-10 | partial | `muster_gateway_calls_total`; masking is S-167, the Transport in the metric S-173 |
| C-30.AC-3 | full | |
| C-30.AC-7 | full | |
| C-32.FR-19 | partial | the expand step of the Timeline: the kind `escalation`, `paged` and the field of its people |
| C-11.FR-2 | partial | revised for L2: calls have a sender of their own; Pages as Thread replies are S-155 |
| C-09.FR-22 | partial | revised for L2: `page_not_delivered` of a call; the other events of C-32 come with S-155, S-156, S-168, S-169 |
| C-12.FR-3 | partial | revised for L2: the texts of calls; their answers are S-173 |
| C-15.FR-2 | partial | revised for L2: `page_not_delivered` in events mode |
