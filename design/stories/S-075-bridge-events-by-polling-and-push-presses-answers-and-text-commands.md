---
id: S-075
title: "Bridge events by polling and push: presses, answers, Text commands and deleted messages (BE)"
capability: C-22
kind: be
layer: L1
depends_on: [S-081]
covers: [C-22.FR-10, C-22.FR-11, C-22.FR-12, C-22.FR-13, C-22.FR-14, C-22.FR-17, C-22.FR-24, C-22.AC-3, C-22.AC-12, C-22.AC-13, C-22.AC-14, C-22.AC-15, C-22.AC-16, C-22.AC-17]
files_touched:
  - api/openapi.yaml
  - internal/db/migrations/0011_bridge_events.up.sql
  - internal/db/migrations/0011_bridge_events.down.sql
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - design/db/schema.md
  - design/architecture.md
  - internal/bridge/events.go
  - internal/bridge/events_test.go
  - internal/bridge/poller.go
  - internal/bridge/poller_test.go
  - internal/bridge/push.go
  - internal/bridge/push_test.go
  - internal/bridge/presses.go
  - internal/bridge/presses_test.go
  - internal/bridge/commands.go
  - internal/bridge/commands_test.go
  - internal/bridge/client.go
  - internal/connections/bridge.go
  - internal/connections/bridge_test.go
  - internal/connections/query.sql
  - internal/connections/connections.go
  - internal/connections/connections_test.go
  - internal/connections/live_test.go
  - internal/accountlinks/lookup.go
  - internal/accountlinks/query.sql
  - internal/delivery/delivery.go
  - internal/delivery/presses.go
  - internal/delivery/presses_test.go
  - internal/delivery/publication.go
  - internal/delivery/interactive.go
  - internal/delivery/query.sql
  - internal/delivery/lockorder_integration_test.go
  - internal/audit/audit.go
  - internal/leader/tasks.go
  - internal/leader/leader_test.go
  - internal/metrics/catalogue.go
  - design/prd/l1/reference.md
  - internal/messages/texts/en.json
  - internal/messages/texts/ru.json
  - internal/messages/message.go
  - internal/runtime/runtime.go
  - internal/fakes/fakebridge/events.go
  - internal/fakes/fakebridge/fakebridge.go
  - internal/fakes/fakebridge/messages.go
  - internal/fakes/fakebridge/fakebridge_test.go
  - internal/logging/events.go
  - internal/archlint/secretleak.go
  - test/e2e/bridge_test.go
acceptance:
  - "[C-22.FR-11, C-22.AC-13] In poll mode the Leader's task `bridge_polling` calls `pollEvents` with the stored cursor and `wait` `bridge.poll_wait`, gives each call `bridge.poll_wait` plus 15 s, handles the batch, then stores `next_cursor`; a press queued in the fake bridge is handled within one poll; an event returned again by an overlapping poll is handled once; a cursor refused as `invalid` is dropped; a failing poll backs off from 1 s to 1 min per call in the background client class, logs `bridge_poll_failed` and makes no Destination Broken; losing leadership or stopping the replica cancels a poll in flight at once (`poller_test.go`)."
  - "[C-22.FR-11, C-22.AC-14] `bridgeEvent` at `MUSTER_INGEST_URL/api/v1/callbacks/bridge/{connection_id}` handles an event with a valid Standard Webhooks signature and answers 204, and the same `webhook-id` again 204 without handling it twice; a wrong signature, a `webhook-timestamp` 6 minutes from the real clock, an unknown Connection and a Connection in poll mode are each answered 401 and change nothing, logged as `bridge_event_rejected`; a body that is no event is answered 400, and an event whose handling fails is answered 500 and is not recorded as seen."
  - "[C-22.FR-11] Two copies of one event handled at the same time — a push retried while the first is running — run one after the other under the claim's advisory lock and dispatch one Command; a crash between the Command and the record of the id is simulated in `events_test.go` and runs the Command a second time on redelivery, which the dispatcher answers as a refusal or a no-op; an event of a type the Connection's `info` does not declare is handled like any other."
  - "[C-22.FR-12, C-22.AC-15] A press from an account linked to a Responder (the link row set up directly; S-076 makes links) runs its Command with the Transport `bridge`, recorded in the Timeline and the Audit log, and is not answered; a press with tampered data, or with data issued for another message, changes nothing and is answered \"This button could not be verified; nothing was changed.\"; a press from an account without a link changes nothing and is answered \"Your account is not linked to Muster. Link it in your profile: {link}\"; a press older than `bridge.event_max_age` changes nothing, is not answered and logs `bridge_press_dropped`; a press on a message Muster did not post through this Connection changes nothing and is not answered; a press on the older Root message of a delivery republished past its edit window runs its Command."
  - "[C-22.FR-13] Answers go through `answerEvent` on the interactive path with the key `answer-<event id>`, take a token of the Connection's limiter ahead of deliveries and fail fast within `delivery.interactive_budget`; a failed answer is logged and not retried; an answer is in the profile language of the linked User, and in English for an account without a link (`presses_test.go`, a Russian-speaking User)."
  - "[C-22.FR-14, C-22.AC-16] In the Thread of a firing Alert Group, `ack` from a linked Responder acknowledges it and is answered \"Done: Acknowledged #N\"; `/snooze 4h` on a Route with the default durations snoozes it for 4 hours, and `snooze 2h` is answered \"Snooze for one of: 1h, 4h, 24h\"; `решено` resolves it; `still on it` answers the open Reminder; a Viewer's `resolve` is refused with C-10's text; \"looking into it\" changes nothing and is not answered; a Text command older than `bridge.event_max_age` changes nothing and is not answered; every answer is shown in the Thread; each Text command logs `bridge_text_command`."
  - "[C-22.FR-10, C-22.AC-17] A `message_deleted` event for the Root message of an open Alert Group publishes it again at once, Quietly, with the deletion notice; for the republished message it marks the pair deleted in the messenger; for any other message it changes nothing."
  - "[C-22.FR-17, C-22.AC-12] A press on a test message is answered \"This is a test message; nothing was changed\" and changes nothing."
  - "[C-22.FR-24] `muster_commands_total` counts Commands with `transport=\"bridge\"`, and the metrics catalogue of reference.md lists the value; `Transport` in the API spec, `internal/audit` and the database accepts `bridge`."
  - "[C-22.AC-3] A pushed event with a wrong signature, and a failing poll, leave the push secret and the token in no log line (`secretleak.go` probe `bridge_events`)."
verify: "make ci test-integration e2e docs-check"
operator_attention: false
issue: 233
---

# S-075. Bridge events by polling and push: presses, answers, Text commands and deleted messages (BE)

## Scope

**IN**

- Receiving events: the Leader's poller per Connection in poll mode, and the signed push callback on the ingest
  listener; the claim of each event id; cursors.
- Presses: verification of the signed data against the pressed message, attribution through Account links, the
  Command with the Transport `bridge`, dropped old presses, presses on test messages.
- Answers through `answerEvent` on the interactive path.
- Text commands in Threads, with their English and Russian words and the Route's Snooze durations.
- `message_deleted` events.
- The Transport `bridge` in the database, the API, the Audit log and the metric catalogue; the `account_links` columns
  that Bridge links need.

**OUT**

- `account_link` events, the token and the code, and Mentions of linked people (S-076); until then link rows are set
  up directly in the checks.
- The pages (S-077); the example bridge and `muster bridge check` (S-078).
- Interim states until later stories: the Timeline and the Audit log of the SPA show the Transport `bridge` with the
  label of `system` until S-077 adds its own; an `account_link` event is acknowledged and changes nothing until S-076.

## Contracts

- **Spec** (`api/openapi.yaml`): the operation `bridgeEvent` (`POST /callbacks/bridge/{connection_id}`, tag Messenger
  callbacks, `x-listener: ingest`, `x-permission: none`, `security: []`, the three Standard Webhooks headers, the body
  `BridgeEvent` copied from `api/bridge.yaml`'s `Event` with `x-bridge-schema: Event`, together with the schemas it
  refers to — `BridgeEventBase`, `BridgePressEvent`, `BridgeThreadMessageEvent`, `BridgeAccountLinkEvent`,
  `BridgeMessageDeletedEvent` and `BridgePerson`, each with the `x-bridge-schema` of its original and the
  discriminator mapping naming the copies — so the test of S-073 checks them; answers `204`, `400`, `401`, `500`),
  mounted on the callback mux of the ingest listener (`internal/runtime`) and
  excluded from request validation like `mattermostAction`, so that the handler decides every answer; `Transport`
  gains `bridge`.
- **Schema** (migration `0011_bridge_events`, `schema.md`):
  - the `transport` checks of `timeline_entries` and `audit_log` accept `bridge`. Both tables are partitioned: each new
    CHECK is added to the parent `NOT VALID` and then validated, which recurses to every partition on PostgreSQL 14
    and 17 alike, and the old CHECK is dropped after it; `make test-integration` runs `up → down → up` on both;
  - `account_links.messenger` accepts `bridge`, with the type-checked foreign key to `connections`; `identity_space`
    becomes `'telegram'` for Telegram and `<messenger>:<connection_id>` otherwise, so a Bridge link's space is
    `bridge:<connection_id>`. A generated column cannot change its expression before PostgreSQL 17, so the migration
    adds the new generated column, drops the two unique constraints and the old column, renames the new one to
    `identity_space` and adds the constraints again under their old names, `account_links_account_key` and
    `account_links_user_space_key`; the values of Mattermost and Telegram links do not change, so old replicas read and
    write the column and catch the constraints by name as before;
  - `account_links` gains `display_name` (nullable), which Bridge links fill;
  - a short-lived table `bridge_events_seen` (`org_id`, `connection_id`, `event_id`, `seen_at`, primary key on the
    first three), pruned by the Leader's `short_lived_pruning` after `bridge.event_dedup_window` on the business clock
    (S-020's clock table). Its claim, insert and prune queries live in `internal/connections/query.sql`, beside the
    cursor's, since the table belongs to the Connection; the prune joins `short_lived_pruning` as a field of
    `internal/leader/tasks.go` for the domain, and the table's name joins `metrics.ShortLivedTables`.
  Old replicas never write a Bridge row, and every row they write satisfies the widened checks.
- **Package boundary**: `internal/connections` imports `internal/bridge` since S-073, so `internal/bridge` never imports
  `internal/connections`. The poller, the push handler and the claim reach the Connection — its push secret and mode,
  its cursor, the claim and record of an event id — through an interface `internal/bridge` declares, which
  `internal/connections/bridge.go` implements and `internal/runtime` wires.
- **Events** (`internal/bridge/events.go`): decoding with the generated models; one handler for both modes. Each event
  is claimed in a transaction that takes `pg_advisory_xact_lock` on the Connection and the event id and looks the id up
  in `bridge_events_seen`; an id already there is acknowledged without handling. Otherwise the handler dispatches the
  event — the Command runs through the dispatcher in its own transaction — then inserts the id and commits the claim,
  then answers (FR-13), then acknowledges. Two copies of one event therefore run one after the other, and a crash after
  the Command and before the insert repeats the Command once on redelivery, which the dispatcher's preconditions turn
  into a refusal or a no-op (ADR-0017); the claim holds no row lock that the dispatcher takes. The claim holds a
  connection of the main pool while the Command takes one more, so at most `bridge.parallel_events` events are handled
  at a time per replica, over all Bridge Connections and both modes, as `telegram.parallel_updates` bounds Telegram
  updates; a pushed event that finds no free slot waits for one. A crash after the insert
  and before the answer leaves the person without an answer, as a failed answer does. An event of a type the
  Connection's `info` does not declare is handled like any other; an `account_link` event is acknowledged and changes
  nothing until S-076.
- **Poller** (`poller.go`, Leader task `bridge_polling`, ADR-0007): one loop per Bridge Connection in poll mode,
  started and stopped as Connections change; `pollEvents` in the background client class with the Connection's proxy
  and a deadline of `bridge.poll_wait` plus 15 s per call, and a client whose call timeout is no shorter, so a long poll
  is never cut off; the background class's own retries stay within that deadline. After the batch is handled,
  `bridge_events_cursor` is written; `invalid` drops the cursor; a failed call backs off from 1 s to 1 min, counted per
  call, and logs `bridge_poll_failed` at the first failure and every tenth. Each call runs under the task's context, so
  losing leadership or stopping the replica cancels a poll in flight at once, as `telegram_polling` does, and a
  replica of `muster dev`, which polls "Dev Bridge", stops within the end-to-end harness's stop timeout.
- **Push** (`push.go`, `internal/connections/bridge.go`): the handler on the ingest listener reads the Connection by
  its `public_id`, verifies `webhook-signature` (`v1,` HMAC-SHA256 over `id.timestamp.body` with the decoded push
  secret, any of several space-separated signatures) in constant time, checks `webhook-timestamp` against the real
  clock within `bridge.push_tolerance`, requires `webhook-id` to equal the event's `id`, and answers as C-22.FR-11 says;
  `bridge_event_rejected` (info: the reason, never the signature or the secret). An event whose handling fails is not
  recorded as seen: in poll mode the cursor stays and the batch is polled again, in push mode the answer is `500` and
  the bridge retries; `bridge_event_failed` (warn: the Connection, the event id and type, the error).
- **Presses** (`presses.go`, `internal/delivery/presses.go`): the data is verified by the button signer of S-036 with
  the key it names; `delivery.BridgePressBinding(connection, message_id)` finds the Root message or the Thread reply the
  message id belongs to among the Connection's Destinations — its current Root message, a Thread reply, or an older Root
  message of a delivery republished past its edit window (`superseded_message_ids` of S-081) — and the action id must
  belong to it; a message id the Connection does not know is ignored without an answer; the person is mapped through
  `accountlinks.Lookup` in the space `accountlinks.SpaceBridge(connection)` (`internal/accountlinks/lookup.go`); the
  Command goes through the dispatcher with the actor's User and the Transport `bridge`; `occurred_at` older than
  `bridge.event_max_age` by the real clock is dropped; a press on a test message (S-074) is recognised by its action id.
  `bridge_press` (info: the Connection, the action, the outcome, the account's external id).
- **Answers** (`delivery/interactive.go`): `AnswerOp` makes `answerEvent` with the key `answer-<event id>`, before the
  event is acknowledged — before the `204` of a push, and before the cursor after it is stored; texts from
  `internal/messages/texts` in the profile language of the User the account is linked to, and in English for an
  account without a link, as S-051 answers during linking.
- **Text commands** (`commands.go`): the grammar of C-22.FR-14 and `bridge.text_commands`; a `thread_message` is matched
  to its Alert Group by `message_id` (the Root message, or an older one of a republished delivery) through
  `delivery.BridgeThreadBinding`, and an unknown message id is ignored; a Text command older than `bridge.event_max_age`
  is dropped and logged; durations must be one of the Route's (`route.snooze_durations`); `still on it` calls
  `answerReminder` (C-17.FR-10); answers as C-22.FR-14 says; `bridge_text_command` (info: the Connection, the command,
  the outcome).
- **Deleted messages**: `message_deleted` for a Root message calls the deleted Root message flow of S-035 at once
  through `delivery.RootDeleted`, a new exported entry in `internal/delivery/publication.go` to the flow `rootGone`
  that a `gone` answer starts; other messages are ignored.
- **Lock order** (D307; `lockorder_integration_test.go`): `RootDeleted` from an event locks the delivery row only, as
  the flow does after a `gone`, never the Alert Group before it; the test gains a `message_deleted` event against an
  acknowledgement that holds the Alert Group and then waits for the delivery in `Enqueue`.
- **Transport**: `bridge` joins `audit.Transport` and the closed `transport` values of `muster_commands_total` in
  `catalogue.go` and in the metrics catalogue of reference.md (C-19.FR-2).
- **Fake bridge** (`events.go`): `pollEvents` with a cursor that is the index of the next event, `wait` honoured;
  push mode with a target and a secret set through `/_fake/config`, retrying until `204`; `/_fake/press` `{message_id,
  user_id, button}` (presses the button of that message, with its data unchanged), `/_fake/thread-message` `{message_id,
  user_id, text}`, both with an optional `occurred_at` for the checks of `bridge.event_max_age`,
  `DELETE /_fake/messages/{id}` emitting `message_deleted` when `deletion_events` is on,
  `/_fake/events` (the queue), `/_fake/answers` (each answer and where it was shown); the users `alice` (`u-alice`,
  "Alice Smith"), `bob` (`u-bob`, "Bob Brown") and `vera` (`u-vera`, "Vera Viewer").
- **Secrets** (lint 5): probe `bridge_events`.
- **Architecture** (`design/architecture.md`): Bridge delivery beside the messengers in §3.1, the Bridge event handler
  among the inbound adapters, the Leader's Bridge polling in flow h, and a flow of a Bridge press from the messenger
  through the bridge, the poll or the push, the claim, the dispatcher and the answer.
- **Defaults**: `bridge.poll_wait`, `bridge.push_tolerance`, `bridge.event_dedup_window`, `bridge.parallel_events`,
  `bridge.event_max_age`, `bridge.text_commands`, `delivery.interactive_budget`.

## Steps

1. Add the migration, the Transport `bridge`, and event decoding with the claim. Check: `events_test.go`,
   `connections/bridge_test.go`, `make test-integration`.
2. Write the poller as a Leader task and the push handler. Check: `poller_test.go` (the cancelled poll included),
   `push_test.go` (signatures, tolerance, the 401 cases).
3. Write presses and answers. Check: `presses_test.go`, `delivery/presses_test.go`.
4. Write Text commands and `message_deleted`. Check: `commands_test.go`,
   `go test -tags integration -run LockOrder ./internal/delivery/`.
5. Extend the fake, the probe and the end-to-end test. Check: Verification.

## Verification

```sh
make dev > dev.log 2>&1 &
# as in S-074: the session, the Integration "lab", NOTIFY, ADV and AG, the Route to the Destination "alerts" (D) of the
# Connection "br" (C, poll mode); the Responder "bob" and the Viewer "vera", created with `createUser` as in S-011
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake; FB=127.0.0.1:18094/_fake
LINK() { psql "$MUSTER_DATABASE_URL" -qc "INSERT INTO account_links (org_id, public_id, user_id, messenger, connection_id,
  external_id, display_name, created_at) SELECT 1, '$3', u.id, 'bridge', c.id, '$2', u.name, now()
  FROM users u, connections c WHERE u.login = '$1' AND c.public_id = '$C'"; }
LINK bob u-bob AK0000000000B7; LINK vera u-vera AK0000000000V7
psql "$MUSTER_DATABASE_URL" -tAc "SELECT DISTINCT identity_space FROM account_links WHERE messenger = 'bridge'" | grep -c '^bridge:'   # 1
curl -s -X PUT $FAM/groups/e1 -d '{"receiver":"lab","route":"{}","labels":{"alertname":"Ev"}}' > /dev/null
curl -s -X PUT $FAM/groups/e1/alerts/p1 -d '{"labels":{"team":"br","pod":"p1"}}' > /dev/null
NOTIFY e1 '{"reason":"first notification"}'; sleep 1; G=$(AG 'pod%3D%22p1%22')
ROOT=$(curl -s $FB/messages | jq -r '[.[] | select(.envelope.title | endswith("Ev"))][0].id')

# C-22.AC-13, AC-15: poll mode, a press from bob; the long poll returns as soon as the fake queues an event
curl -s -X POST $FB/press -d "{\"message_id\":\"$ROOT\",\"user_id\":\"u-bob\",\"button\":\"Ack\"}" > /dev/null; sleep 2
curl -s -b jar "$API/alert-groups/$G/timeline?limit=1" | jq -c '.items[0] | {event, t: .actor.transport, u: .actor.name}'
# {"event":"acknowledged","t":"bridge","u":"bob"}
psql "$MUSTER_DATABASE_URL" -tAc "SELECT bridge_events_cursor IS NOT NULL FROM connections WHERE public_id = '$C'"   # t
# an unlinked account, then tampered data
curl -s -X POST $FB/press -d "{\"message_id\":\"$ROOT\",\"user_id\":\"u-alice\",\"button\":\"Resolve\"}" > /dev/null; sleep 2
curl -s $FB/answers | jq -r '.[-1].text'
# Your account is not linked to Muster. Link it in your profile: http://localhost:8080/profile
curl -s -X POST $FB/press -d "{\"message_id\":\"$ROOT\",\"user_id\":\"u-bob\",\"button\":\"Resolve\",\"data\":\"a9.k1.AAAAAAAAAAAA\"}" > /dev/null; sleep 2
curl -s $FB/answers | jq -r '.[-1].text'                                        # This button could not be verified; nothing was changed.
curl -s -b jar $API/alert-groups/$G | jq -r .status                              # acknowledged

# C-22.AC-16: Text commands
curl -s -X POST $FB/thread-message -d "{\"message_id\":\"$ROOT\",\"user_id\":\"u-bob\",\"text\":\"unack\"}" > /dev/null
curl -s -X POST $FB/thread-message -d "{\"message_id\":\"$ROOT\",\"user_id\":\"u-bob\",\"text\":\"snooze 2h\"}" > /dev/null
curl -s -X POST $FB/thread-message -d "{\"message_id\":\"$ROOT\",\"user_id\":\"u-bob\",\"text\":\"/Snooze 4h\"}" > /dev/null
curl -s -X POST $FB/thread-message -d "{\"message_id\":\"$ROOT\",\"user_id\":\"u-vera\",\"text\":\"resolve\"}" > /dev/null
curl -s -X POST $FB/thread-message -d "{\"message_id\":\"$ROOT\",\"user_id\":\"u-bob\",\"text\":\"looking into it\"}" > /dev/null
sleep 2; curl -s $FB/answers | jq -r '.[-4:][] | .text'
# Done: Unacknowledged #1
# Snooze for one of: 1h, 4h, 24h
# Done: Snoozed #1
# You are not permitted to do this
curl -s -b jar $API/alert-groups/$G | jq -c '{status, s: (.snooze_until != null)}'   # {"status":"snoozed","s":true}
curl -s -X POST $FB/thread-message -d "{\"message_id\":\"$ROOT\",\"user_id\":\"u-bob\",\"text\":\"Решено\"}" > /dev/null; sleep 2
curl -s -b jar $API/alert-groups/$G | jq -r .status                              # resolved
grep -c 'bridge_text_command' dev.log                                             # 5

# C-22.AC-14: push mode
SECRET=whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw; CB=localhost:8081/api/v1/callbacks/bridge/$C
ETAG=$(curl -s -b jar $API/connections/$C | jq -r .etag)
curl -s "${H[@]}" -X PUT -H "If-Match: $ETAG" $API/connections/$C -d "{\"type\":\"bridge\",\"name\":\"br\",\"base_url\":\"http://127.0.0.1:18094\",
  \"event_delivery\":\"push\",\"push_secret\":\"$SECRET\",\"proxy\":{\"enabled\":false},\"limiter\":{\"limit\":10,\"per_seconds\":1}}" > /dev/null
SIGN() { ts=${2:-$(date +%s)}; sig=$(printf '%s.%s.%s' "$1" "$ts" "$3" | openssl dgst -sha256 -mac HMAC \
  -macopt "hexkey:$(printf '%s' "${SECRET#whsec_}" | base64 -d | xxd -p -c 256)" -binary | base64)
  curl -s -o /dev/null -w '%{http_code}\n' $CB -H "webhook-id: $1" -H "webhook-timestamp: $ts" -H "webhook-signature: v1,$sig" -d "$3"; }
EV='{"id":"ev-1","type":"message_deleted","occurred_at":"2026-10-14T12:00:00Z","message_id":"nope"}'
SIGN ev-1 "" "$EV"                                                  # 204
SIGN ev-1 "" "$EV"                                                  # 204 (handled once)
SIGN ev-1 $(( $(date +%s) - 360 )) "$EV"                            # 401
curl -s -o /dev/null -w '%{http_code}\n' $CB -H 'webhook-id: ev-1' -H "webhook-timestamp: $(date +%s)" -H 'webhook-signature: v1,AAAA' -d "$EV"   # 401
SIGN ev-2 "" 'not json'                                             # 400
grep -c 'whsec_MfKQ\|muster-dev-bridge-token-b' dev.log                           # 0

# C-22.AC-17: a deleted Root message, reported by the fake through push
curl -s -X PUT $FB/config -d "{\"push\":{\"url\":\"http://$CB\",\"secret\":\"$SECRET\"}}" > /dev/null
curl -s -X PUT $FAM/groups/e2 -d '{"receiver":"lab","route":"{}","labels":{"alertname":"Del"}}' > /dev/null
curl -s -X PUT $FAM/groups/e2/alerts/d1 -d '{"labels":{"team":"br","pod":"d1"}}' > /dev/null
NOTIFY e2 '{"reason":"first notification"}'; sleep 1
R2=$(curl -s $FB/messages | jq -r '[.[] | select(.envelope.title | endswith("Del"))][0].id')
curl -s -X DELETE $FB/messages/$R2; sleep 2
curl -s $FB/messages | jq -c '[.[] | select(.envelope.title | endswith("Del")) | {deleted, n: .envelope.notify, note: (.envelope.body | test("previous message was deleted"))}]'
# [{"deleted":true,"n":true,"note":false},{"deleted":false,"n":false,"note":true}]

curl -s localhost:8082/metrics | grep 'muster_commands_total{.*transport="bridge"' | head -1   # a line with a value above 0
```

## Open questions

None.

## Notes

- Suggested commit: `feat(bridge): receive bridge events by polling and push and run text commands`.
- A press is handled on the replica that receives it in push mode, and on the Leader in poll mode; both paths call the
  same handler, so the tests run it once per mode.
- The claim keeps the dispatcher unchanged (D323): recording the id inside the Command's transaction would need a hook
  in the dispatcher for one transport, and a repeated Command is already harmless.
- The `account_links` changes come here rather than with Account links (S-076), because the presses and Text commands
  of this story map accounts through them; the changes to `account_link_requests` stay in S-076.
- S-075 lists 39 files. If it grows past about 40 while it is worked, Text commands — `commands.go`,
  `delivery.BridgeThreadBinding` and their texts — become a story of their own after it.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-22.FR-10 | full | |
| C-22.FR-11 | full | |
| C-22.FR-12 | partial | links made through the profile are S-076; the Transport's label in the SPA is S-077 |
| C-22.FR-13 | partial | answers to presses and Text commands; answers to Account link requests are S-076 |
| C-22.FR-14 | full | |
| C-22.FR-17 | partial | the press on a test message |
| C-22.FR-24 | partial | `bridge_press`, `bridge_press_dropped`, `bridge_text_command`, `bridge_event_rejected`, `bridge_event_failed`, `bridge_poll_failed`; the `transport` value |
| C-22.AC-3 | partial | the push secret and the token on the event paths |
| C-22.AC-12 | partial | the press on a test message |
| C-22.AC-13 | full | |
| C-22.AC-14 | full | |
| C-22.AC-15 | full | |
| C-22.AC-16 | full | |
| C-22.AC-17 | full | |
