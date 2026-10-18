---
id: S-089
title: "Request log of inbound requests at the callbacks, and the kinds and runs of checks and tests (BE)"
capability: C-23
kind: be
layer: L1
depends_on: [S-088]
covers: [C-23.FR-1, C-23.FR-2, C-23.FR-3, C-23.FR-5, C-23.AC-2, C-23.AC-3, C-23.AC-5]
files_touched:
  - api/openapi.yaml
  - internal/requestlog/inbound.go
  - internal/requestlog/inbound_test.go
  - internal/requestlog/requestlog.go
  - internal/requestlog/query.sql
  - internal/requestlog/requestlog_test.go
  - internal/mattermost/callback.go
  - internal/mattermost/callback_test.go
  - internal/telegram/webhook.go
  - internal/telegram/webhook_test.go
  - internal/bridge/push.go
  - internal/bridge/push_test.go
  - internal/runtime/runtime.go
  - internal/connections/connections.go
  - internal/connections/telegram.go
  - internal/connections/bridge.go
  - internal/connections/draft.go
  - internal/connections/draft_test.go
  - internal/destinations/write.go
  - internal/destinations/test.go
  - internal/delivery/broken.go
  - internal/api/connections.go
  - internal/api/destinations.go
  - internal/api/requestlog.go
  - internal/api/requestlog_test.go
  - internal/archlint/secretleak.go
  - test/e2e/request_log_test.go
acceptance:
  - "[C-23.FR-1, C-23.FR-2, C-23.AC-2] In the webhook mode, an update posted to `callbacks/telegram/{id}` with the right secret token is recorded as an inbound `update` with its body, Muster's answer `200`, the latency and the Connection; the same with a wrong secret token is recorded with the answer `401`; a request for a Connection id that does not exist is not recorded (`webhook_test.go`, `inbound_test.go`, `request_log_test.go`)."
  - "[C-23.FR-1, C-23.FR-2] A Mattermost press is recorded as an inbound `press` with Muster's JSON answer, the Connection and, once the press was matched to its post, the Destination and the Alert Group; a press for an unknown or deleted Connection, which Muster answers `200` `{}` too, is not recorded (`callback_test.go`)."
  - "[C-23.FR-1, C-23.AC-5] A pushed Bridge event is recorded as an inbound `push` with its answer `204`; a push signed with another secret and a push to a Connection in poll mode are recorded with their answer `401`; a push for a Connection that does not exist is not recorded (`push_test.go`, `request_log_test.go`)."
  - "[C-23.FR-3, C-23.AC-3] Inbound records hold no secret: the values of `X-Telegram-Bot-Api-Secret-Token`, `webhook-signature` and `Authorization` are `[redacted]` whatever they hold, the signed action id in a press's `context`, a `/start <token>` in a webhook update and the `token` of a pushed `account_link` event are `[redacted]` (`inbound_test.go`; the probe `request_log_inbound` of `secretleak.go` pushes each through the callbacks into the database and searches every stored column)."
  - "[C-23.FR-1] The answer to a callback is not delayed by its record: the middleware flushes Muster's answer before it writes, and a record that cannot be written leaves the answer as it was (`inbound_test.go`)."
  - "[C-23.FR-2, C-23.FR-5, C-23.AC-5] Checks and tests carry their kind and run: `checkConnection` records `connection_check` and returns the run as `request_log_run`; `checkConnectionDraft` with a `connection_id` records `draft_check` under that Connection, and without one records `draft_check` with no owner, listed by `listRequestLog?run=`; `checkDestination` and the check of a Destination being saved record `destination_check` with the Destination, `testDestination` records `test`, and each Broken probe records `probe` with a run of its own; `listRequestLog?run=` alone needs `connections:read` and lists only that run (`draft_test.go`, `internal/api/requestlog_test.go`, `request_log_test.go`)."
verify: "make ci test-integration"
operator_attention: false
issue: null
---

# S-089. Request log of inbound requests at the callbacks, and the kinds and runs of checks and tests (BE)

## Scope

**IN**

- Recording the requests that reach a Connection's addresses on the ingest listener — Mattermost presses, Telegram
  updates in the webhook mode, pushed Bridge events — with Muster's answers, refused ones included, through one
  middleware shared by the three callbacks (C-23.FR-1, D354).
- Their masking: the credential headers and the shape masking of S-088.
- The kinds and runs of checks and tests on their records — `connection_check`, `draft_check`, `destination_check`,
  `test`, `probe` — the records of the checks before saving of S-087, and the run on the check and test results.

**OUT**

- The table, outbound recording, pruning and the operations `listRequestLog` and `getRequestRecord` (S-088).
- The pages (S-090).

## Contracts

- **Spec** (`api/openapi.yaml`, changed here first, then generated): `listRequestLog` gains the query `run` (a
  check run, `CR…`), alone — needing `connections:read` and listing only that run, which is how the records of a check
  before saving of a new Connection are read — or beside `connection` or `destination`; `ConnectionCheckResult`,
  `DestinationCheckResult` and `DestinationTestResult` gain `request_log_run` (string, the run of their requests).
- **Inbound recording** (`internal/requestlog/inbound.go`): `requestlog.Inbound(operation, next)` wraps a callback
  handler: it keeps the request's headers and the first `request_log.body_max` bytes of its body while the handler
  reads it, with its full size; it keeps Muster's answer — status, headers, body — as the handler writes it; it measures
  the handler's time on the real clock. A handler that finds the Connection of the path, whether or not it then refuses
  the request, says so with `requestlog.Annotate(ctx, links)` — the Connection, and the Destination and the Alert Group
  once it has matched the press or event to a delivery. After the handler returns, the middleware flushes the answer,
  then masks the record — the values of `outbound.MaskedHeaders` replaced whatever they hold — and hands it to the
  `Writer` of S-088, which applies the shape masking and truncation; without an annotation, nothing is recorded, so a
  request for an unknown or deleted Connection leaves no trace. Inbound records have the direction `inbound`, the path
  `direct`, the attempt `1` and no error class: Muster's answer is their status. Operations: `press`
  (`callbacks/mattermost/{connection_id}`), `update` (`callbacks/telegram/{connection_id}`), `push`
  (`callbacks/bridge/{connection_id}`).
- **Handlers** (`internal/mattermost/callback.go`, `internal/telegram/webhook.go`, `internal/bridge/push.go`): each
  annotates where it has read its Connection — Mattermost after the lookup that answers an unknown Connection with
  `{}`, Telegram and Bridge before they check the secret token or the signature, so that a refusal of a known
  Connection is recorded — and adds the Destination and the Alert Group where it matched them. `internal/runtime`
  wraps the three in `callbacks()`.
- **Kinds and runs** (`internal/connections/*.go`, `internal/destinations/write.go`, `test.go`,
  `internal/delivery/broken.go`, the API handlers): a check run is a `public_id` with the prefix `CR`, made for each
  run of a check or a test and set on the context with its kind (`outbound.WithLinks`) where the run starts —
  `connection_check` in `Service.Check`, `draft_check` in `Service.CheckDraft`, `destination_check` in the Destination
  check of a save and of `checkDestination`, `test` in `testDestination`, `probe` in each Broken probe of the delivery
  worker — with the Destination where there is one. The API handlers return the run as `request_log_run`.
- **Checks before saving** (`internal/connections/draft.go`): the clients of a draft are built with an owner and the
  `Writer`: the Connection of `connection_id` when the draft names one, and no owner otherwise, so that its records are
  read by their run and bounded by `request_log.max_records` per Organization (S-088). Their dry probes are recorded
  too.
- **Secrets** (lint 5): the probe `request_log_inbound` posts to the three callbacks, through the runtime's mux, a
  Telegram update with a wrong secret token and a `/start <token>`, a Mattermost press with a signed action id and a
  Bridge push of an `account_link` event with its signature, then searches every text and `jsonb` column of
  `request_log` and the log for each secret.
- **Defaults**: `request_log.body_max`, `request_log.max_records`.

## Steps

1. Write the middleware and the annotation, and wrap the callbacks. Check: `inbound_test.go`, the handlers' tests.
2. Add the kinds and runs of checks and tests, the draft clients' owner, the `run` filter and `request_log_run` on the
   results. Check: `draft_test.go`, `internal/api/requestlog_test.go`, `make generate-check`.
3. Add the secret probe and extend the end-to-end test. Check: `make lint-arch`, `request_log_test.go`, then
   Verification.

## Verification

```sh
make dev > dev.log 2>&1 &
# the Admin's session (`jar`, `H`) as in S-011; "Dev Telegram" (T) in the webhook mode, its update U and its secret
# token as in S-041; "Dev Bridge" (C) in the push mode with the secret SECRET as in S-075
API=localhost:8080/api/v1; FB=127.0.0.1:18094/_fake

# C-23.AC-2: Telegram updates in the webhook mode, a wrong secret token, an unknown Connection
curl -s -o /dev/null -X POST -H 'X-Telegram-Bot-Api-Secret-Token: wrong' localhost:8081/api/v1/callbacks/telegram/$T -d "$U"
curl -s -o /dev/null -X POST -H 'X-Telegram-Bot-Api-Secret-Token: x' localhost:8081/api/v1/callbacks/telegram/CN000000000000 -d "$U"
curl -s -b jar "$API/request-log?connection=$T&direction=inbound&limit=1" | jq -c '.items[0] | {operation, status}'
# {"operation":"update","status":401}
R=$(curl -s -b jar "$API/request-log?connection=$T&direction=inbound&limit=1" | jq -r '.items[0].id')
curl -s -b jar "$API/request-log/$R" | jq -c '.request_headers["X-Telegram-Bot-Api-Secret-Token"]'           # ["[redacted]"]
psql "$MUSTER_DATABASE_URL" -Atc "SELECT count(*) FROM request_log WHERE url LIKE '%CN000000000000%'"        # 0

# C-23.AC-5: Muster's push secret changes while the fake bridge keeps signing with SECRET — its next push gets 401
ETAG=$(curl -s -b jar $API/connections/$C | jq -r .etag)
curl -s "${H[@]}" -X PUT -H "If-Match: $ETAG" $API/connections/$C -d '{"type":"bridge","name":"Dev Bridge",
  "base_url":"http://127.0.0.1:18094","event_delivery":"push","push_secret":"whsec_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
  "proxy":{"enabled":false},"limiter":{"limit":10,"per_seconds":1}}' > /dev/null
curl -s -X POST $FB/thread-message -d '{"message_id":"m-unknown","user_id":"u-bob","text":"hello"}' > /dev/null; sleep 2
curl -s -b jar "$API/request-log?connection=$C&operation=push&limit=1" | jq -c '.items[0] | {status}'          # {"status":401}

# C-23.FR-5: the run of a check, and a check before saving of a new Connection
RUN=$(curl -s "${H[@]}" -X POST $API/connections/$C/checks | jq -r .request_log_run)
curl -s -b jar "$API/request-log?run=$RUN" | jq -c '[.items[] | {operation, check}]'
# [{"operation":"getInfo","check":"connection_check"}]
RUN=$(curl -s "${H[@]}" $API/connections/checks -d '{"type":"bridge","base_url":"http://127.0.0.1:18094",
  "event_delivery":"poll","bot_token":"muster-dev-bridge-token-b"}' | jq -r .request_log_run)
curl -s -b jar "$API/request-log?run=$RUN" | jq -c '[.items[] | {operation, check, owner: .connection_id}]'
# [{"operation":"getInfo","check":"draft_check","owner":null}]
grep -c 'whsec_\|muster-dev-bridge-token' dev.log                                                             # 0
```

The control requests of the fakes are those of S-041 and S-075; where they differ, the pull request shows the commands
it ran.

## Open questions

None.

## Notes

- Suggested commit: `feat(requestlog): record callback requests and the runs of checks`.
- A request whose path names a Connection is recorded even when Muster refuses it, so a push secret or a webhook secret
  token that does not match is visible to the Admin; whoever knows a Connection's callback address can fill its
  Request log, which `request_log.max_records` bounds.
- The middleware records after the answer is flushed, so a bridge waiting for `204` and Telegram waiting for `200` see
  no change.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-23.FR-1 | partial | inbound requests and the checks before saving of new Connections; outbound ones in S-088 |
| C-23.FR-2 | partial | the inbound record and the check kind and run; the rest in S-088 |
| C-23.FR-3 | partial | the inbound side; outbound and the shape masking in S-088 |
| C-23.FR-5 | partial | the `run` filter and the run on check and test results; the operations in S-088 |
| C-23.AC-2 | partial | the webhook mode; polling in S-088 |
| C-23.AC-3 | partial | pushes, presses and webhook updates; outbound requests in S-088 |
| C-23.AC-5 | partial | the checks before saving and refused pushes; the outgoing webhook's attempts in S-088 |
