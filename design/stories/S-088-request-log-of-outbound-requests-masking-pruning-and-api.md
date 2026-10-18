---
id: S-088
title: "Request log of outbound requests: recording in the outbound package, masking, pruning and the API (BE)"
capability: C-23
kind: be
layer: L1
depends_on: [S-087]
covers: [C-23.FR-1, C-23.FR-2, C-23.FR-3, C-23.FR-4, C-23.FR-5, C-23.FR-7, C-23.AC-1, C-23.AC-2, C-23.AC-3, C-23.AC-4, C-23.AC-5]
files_touched:
  - api/openapi.yaml
  - api/README.md
  - internal/db/migrations/0013_request_log.up.sql
  - internal/db/migrations/0013_request_log.down.sql
  - internal/db/migrate_test.go
  - design/db/schema.md
  - design/architecture.md
  - design/prd/l1/reference.md
  - internal/requestlog/requestlog.go
  - internal/requestlog/query.sql
  - internal/requestlog/requestlog_test.go
  - internal/requestlog/mask.go
  - internal/requestlog/mask_test.go
  - internal/requestlog/requestlog_integration_test.go
  - internal/outbound/client.go
  - internal/outbound/record.go
  - internal/outbound/record_test.go
  - internal/buttons/buttons.go
  - internal/buttons/buttons_test.go
  - internal/mattermost/client.go
  - internal/telegram/client.go
  - internal/bridge/client.go
  - internal/webhooks/adapter.go
  - internal/connections/connections.go
  - internal/connections/telegram.go
  - internal/connections/bridge.go
  - internal/accountlinks/mattermost.go
  - internal/accountlinks/bridge.go
  - internal/delivery/worker.go
  - internal/api/requestlog.go
  - internal/api/requestlog_test.go
  - internal/leader/tasks.go
  - internal/runtime/runtime.go
  - internal/logging/events.go
  - internal/metrics/catalogue.go
  - internal/archlint/secretleak.go
  - test/e2e/request_log_test.go
acceptance:
  - "[C-23.FR-1, C-23.FR-2, C-23.AC-1] A Publication to the fake Mattermost server writes one outbound record: the operation `createPost`, `POST` and the URL, the request headers and the body Muster sent, the status `201`, the headers and body the fake answered, the latency, the path `direct`, the attempt `1`, the Connection, the Destination and the Alert Group; `listRequestLog?connection=` lists it first, `listRequestLog?destination=&alert_group=` lists it, and `getRequestRecord` returns it with its bodies (`request_log_test.go`, the Verification)."
  - "[C-23.FR-1, C-23.AC-2] A Telegram `getUpdates` whose answer carries an update is recorded with that answer; one that ends without updates is not, and neither is a Bridge `pollEvents` without events; a failed poll is recorded, each attempt of the background class as a record of its own (`record_test.go`, `request_log_test.go`)."
  - "[C-23.FR-1, C-23.AC-5] An outgoing webhook Destination whose fake endpoint answers `500` with a body has one record per attempt, each with that body and the error class `transient`, under the Destination; a request that got no answer has no status and carries the code of `delivery.FailureCode` and the masked error text (`record_test.go`, `request_log_test.go`)."
  - "[C-23.FR-3, C-23.AC-3] No record holds a secret (`mask_test.go`, `record_test.go`, and the probe `request_log` of `secretleak.go`, which drives each case through the outbound package into the database and searches every stored column for the secret): the Telegram token, in the URL path too; an outgoing webhook's Secret in a header and in the URL's query; the values of `Authorization`, `Proxy-Authorization`, `Cookie`, `Set-Cookie` and the webhook's signature header whatever they hold; the Telegram webhook's secret token in `setWebhook`; the signed action ids of a Mattermost post, a Telegram keyboard and a Bridge envelope; a `/start <token>` in a `getUpdates` answer; the code of an Account link in a Mattermost or Bridge direct message — each stored as `[redacted]`; the path prefix of a base URL is `[redacted]` in the URL as in errors."
  - "[C-23.FR-4, C-23.AC-4] With 510 records of one Connection, `listRequestLog` returns the newest 500 over all its pages and `request_log_pruning` leaves 500 rows; records of an outgoing webhook Destination and of no owner are bounded alike; after the business clock moves 8 days no older record is listed, and the task, woken by the move, deletes them; a 40 KB body is stored as the first 16 KB, cut at a character boundary, marked truncated with its full size, and a body that is not UTF-8 is stored with its bytes replaced (`requestlog_integration_test.go` on PostgreSQL 14 and 17, `mask_test.go`)."
  - "[C-23.FR-4] A record that cannot be written — the database refuses it, or its write runs past its timeout — leaves the request it records unchanged, is logged as `request_log_write_failed` without its content and counted in `muster_request_log_write_failures_total`; a written one is counted in `muster_request_log_records_total` (`requestlog_test.go`); `make load-test` stays within its thresholds with the Request log on."
  - "[C-23.FR-5] `listRequestLog` takes exactly one of `connection` and `destination` (`422` `one_of_required` otherwise), answers `404` for an unknown or deleted owner, needs `connections:read` or `destinations:read` for the owner it names, pages by cursor newest first without `OFFSET`, and filters by `direction`, `status_class` (`2xx`, `3xx`, `4xx`, `5xx`, `none`), `operation` and `alert_group`; `getRequestRecord` answers `404` for a record past either bound and checks the Permission of the record's owner (`internal/api/requestlog_test.go`)."
  - "[C-23.FR-7] The log events `request_log_write_failed` and `request_log_pruned` and the metrics `muster_request_log_records_total` (`direction`), `muster_request_log_write_failures_total` (`direction`) and `muster_request_log_pruned_total` are registered, in the catalogue of reference.md, and on their generated reference pages."
verify: "make ci test-integration"
operator_attention: false
issue: 266
---

# S-088. Request log of outbound requests: recording in the outbound package, masking, pruning and the API (BE)

## Scope

**IN**

- The table `request_log` (migration `0013_request_log`) and its domain package `internal/requestlog`: writing a
  record, the shape masking and truncation every record goes through, reading and pruning.
- Recording every outbound request of a Connection or an outgoing webhook Destination in `internal/outbound`, each
  attempt a record, with the Secrets of the client and of the request masked, except long polls that ended without
  events (C-23.FR-1, D354).
- The links of delivery requests to their Destination and Alert Group.
- The Leader task `request_log_pruning` and the bounds `request_log.max_records`, `request_log.max_age`,
  `request_log.body_max`.
- The operations `listRequestLog` and `getRequestRecord`; the log events and metrics.

**OUT**

- Inbound requests at the callbacks of Mattermost, Telegram and Bridges, with Muster's answers and their masking
  (S-089).
- The kinds and runs of checks and tests — `connection_check`, `draft_check`, `destination_check`, `test`, `probe` —
  the records of checks before saving, and the run on the check and test results (S-089). Until then the requests of
  checks are recorded under their Connection like any other request, without a kind; those of a check before saving of
  a new Connection, which has no Connection yet, are not recorded.
- The "Request log" tab, the record view and the links from the Alert Group page and the check panels (S-090).

## Contracts

- **Spec** (`api/openapi.yaml`, changed here first, then generated; tag Request log, which `api/README.md` lists):
  - `listRequestLog`, `GET /request-log`, `x-permission: [connections:read, destinations:read]` — the handler checks
    the one of the owner it reads. Query: `connection` or `destination` (a `public_id`, exactly one), `direction`
    (`outbound`, `inbound`), `status_class` (`2xx`, `3xx`, `4xx`, `5xx`, `none`), `operation`, `alert_group`,
    `cursor`, `limit`. `200` `RequestLogList` (`items` of `RequestRecordSummary`, `next_cursor`), `400`, `401`, `403`,
    `404`, `422`, `429`. With `destination`, an outgoing webhook's records are all its own; a messenger Destination's
    are the records of its Connection that name it.
  - `getRequestRecord`, `GET /request-log/{request_record_id}`, the same Permissions: `200` `RequestRecord`, `401`,
    `403`, `404`, `429`.
  - `RequestRecordSummary`: `id` (`RQ…`), `at`, `direction`, `operation`, `method`, `url`, `status` (nullable),
    `latency_ms`, `via` (`ConnectionPath`), `attempt`, `error_class` (nullable: `retry_after`, `transient`, `fatal`,
    `unknown`, `blocked`, `redirect`), `error_code` (nullable, the values of `NullableConnectionCheckCode` that name a
    failure without an answer: `dns_failed`, `connect_failed`, `tls_failed`, `timeout`, `proxy_refused`, `blocked`,
    `request_failed`), `check` (nullable: `connection_check`, `draft_check`, `destination_check`, `test`, `probe`,
    set from S-089), `check_run` (nullable, from S-089), `connection_id` (nullable), `destination` (`DestinationRef`,
    nullable), `alert_group` (`AlertGroupRef`, nullable), `request_body_truncated`, `response_body_truncated`. `RequestRecord` adds `request_headers` and
    `response_headers` (objects of string arrays), `request_body`, `request_body_bytes`, `response_body`,
    `response_body_bytes` and `error` (masked, untrusted). Bodies and headers are untrusted text.
  - Two `public_id` prefixes join the table of `api/README.md`: `RQ` (a record of the Request log, `request_log`) and
    `CR` (a check run, which has no table of its own; generated from S-089).
- **Schema** (migration `0013_request_log`, expand only, `schema.md` §2, §4.11 and the retention table of §6): the table
  `request_log`, **not partitioned**: its size is bounded per owner, so it is short-lived state removed by batched
  deletes, like the other short-lived tables (§6); time partitions would not spare the deletes the count bound needs,
  and ADR-0006 keeps partitions for tables whose size grows with their retention period. Columns: `id` (identity),
  `org_id`, `public_id` (`RQ`, `CHECK` of prefix and alphabet, unique), `at` (business clock), `direction` (`outbound`,
  `inbound`), `operation` (1 to 64 characters), `check_kind` (nullable, `CHECK` of the five kinds) and `check_run`
  (nullable, a `CR` id; both written from S-089), `method`, `url`, `request_headers` (`jsonb`), `request_body`,
  `request_body_bytes`, `request_body_truncated`, `status` (nullable), `response_headers` (`jsonb`, nullable),
  `response_body`, `response_body_bytes`, `response_body_truncated`, `latency_ms`, `via` (`direct`, `proxy`), `attempt`,
  `error_class`, `error_code`, `error` (nullable), `connection_id` and `destination_id` (nullable foreign keys, `ON
  DELETE CASCADE`; a check's `FOR KEY SHARE` that a save of the Connection or Destination, `FOR NO KEY UPDATE`, does not
  wait for), `alert_group_id` (nullable, no foreign key, kept by the single writer: records go long before their Alert
  Group). Indexes: `request_log_connection_idx (org_id, connection_id, at DESC, id DESC) WHERE connection_id IS NOT
  NULL`, `request_log_destination_idx (org_id, destination_id, at DESC, id DESC) WHERE destination_id IS NOT NULL` and
  `request_log_at_idx (org_id, at)` for the age bound; every list reads at most the newest `request_log.max_records` of
  an owner, so its other filters scan no more than that. The `org_id` of every query comes from a parameter (lint 1).
- **Write volume** (the Notes of `schema.md` §4.11): one row per request — each Publication, edit and Thread reply,
  answer, check and non-empty poll — typically a few thousand to a few tens of thousands a day per installation, at
  most the sum of the limiters at a peak (a Mattermost Connection 5 requests per second, a Telegram Connection 15, a
  Bridge Connection 10, an outgoing webhook 5); each row at most two bodies of `request_log.body_max` and their
  headers. The table holds at most `request_log.max_records` rows per owner plus what arrived since the last pruning.
- **Recording** (`internal/outbound/record.go`, `client.go`):
  - A client whose `Config` has a `Recorder` and an owner — a Connection or an outgoing webhook Destination, by
    internal id, or, from S-089, no owner for the checks before saving of new Connections, whose records the
    Organization's bound covers — records each attempt `Do` makes, after the attempt and before the next one: the
    method, the URL as errors show it (no user information, the base URL's path prefix `[redacted]`), the request
    headers as sent (the `User-Agent` included) and body, the answer's status, headers and body, or none, the latency on
    the real clock, the path, the attempt, and for a failed attempt the outcome as the error class, the network failure
    (`Error.Network`), which the `Writer` turns into the code as `delivery.FailureCode` does, and the redacted error
    text. Clients without a `Recorder` — OIDC,
    the outgoing heartbeat, `muster doctor`, `muster bridge check`, and until S-089 the clients of checks before
    saving — record nothing.
  - Each `Request` names its `Operation`. Operations are named as their protocol names them: Telegram the Bot API
    method; Bridge the `operationId` of `api/bridge.yaml`; Mattermost the operation of its REST API reference in lower
    camel case (`getMe`, `getPing`, `getRolesByNames`, `getTeamsForUser`, `getChannelsForTeamForUser`, `getChannel`,
    `getChannelMember`, `createDirectChannel`, `createPost`, `createPostEphemeral`, `patchPost`, `getPost`,
    `doPostAction`); an outgoing webhook `event`, or the template request `create`, `update`, `open_thread` or
    `reply_in_thread`.
  - A `Request` may carry a test of its answer that skips the record: `getUpdates` skips an answer whose `result` is
    empty, `pollEvents` one whose `events` are empty; a failed poll is recorded.
  - The context of a request carries its links (`outbound.WithLinks`): the delivery worker sets the Destination and the
    Alert Group of each attempt — none for a Storm summary — before it calls the adapter; S-089 adds the kind and run
    of checks. `outbound.WithSecrets` adds Secrets for one request: the Account link code of a Mattermost or Bridge
    direct message (`internal/accountlinks`), and the Telegram webhook's secret token of `setWebhook`.
  - Masking before the record leaves the package: every Secret of the client's `Config.Secrets` (the bot token, the
    proxy password, an outgoing webhook's Secrets) and of the request is replaced by `[redacted]` in the URL, the
    headers and both bodies, in every escaped form the redactor knows; the values of `Authorization`,
    `Proxy-Authorization`, `Cookie`, `Set-Cookie`, `X-Telegram-Bot-Api-Secret-Token`, `webhook-signature` and the
    outgoing webhook's signature header (`outbound.MaskedHeaders`, which S-089 reuses) are replaced whatever they hold.
  - Recording never fails, retries or delays the request beyond its own write: the `Recorder` gets the record after
    the attempt has its outcome.
- **Writing** (`internal/requestlog/requestlog.go`, `mask.go`): `Writer.Record` applies the shape masking, truncates,
  and inserts one row with the business time, on the main pool outside any transaction of the caller, under a context
  that does not end with the caller's and times out after 2 seconds. The shape masking replaces, in JSON bodies and in
  text: every string that `buttons.LooksSigned` recognizes as a signed action id — the structure of an action id,
  checked without its key — a 43-character base64url word after `/start `, and the `token` member of an `account_link`
  event; then each body is cut to `request_log.body_max` bytes at a UTF-8 boundary, invalid bytes replaced, with its
  full size and the truncated mark. A failed write is logged as `request_log_write_failed` (WARN: direction,
  operation, the owner's `public_id` and the database error, never a header or a body) and counted.
- **Reading** (`requestlog.go`, `query.sql`): newest first by `(at, id)`, cursor pagination; only records newer than
  `request_log.max_age` and among the newest `request_log.max_records` of their owner; a messenger Destination's view
  within its Connection's bound.
- **Pruning** (`internal/leader/tasks.go`, `requestlog.go`): the Leader task `request_log_pruning` (ADR-0007, amended)
  runs every minute and when the development clock moves; per Organization it deletes records older than
  `request_log.max_age` and, for each Connection, each outgoing webhook Destination and the records without an owner,
  those past the newest `request_log.max_records`, 5,000 rows per statement until a batch comes back short. It deletes
  only what is past a bound, so two overlapping runs are harmless. It logs `request_log_pruned` (INFO, the count) when
  it deleted anything and counts `muster_request_log_pruned_total`.
- **Wiring** (`internal/runtime/runtime.go`, `internal/connections/*.go`, `internal/webhooks/adapter.go`): the
  Connection clients (Mattermost, Telegram, Bridge, also those of checks and polling) and the outgoing webhook clients
  are built with their owner and the process's `Writer`.
- **Registries**: log events `request_log_write_failed` (WARN) and `request_log_pruned` (INFO); metrics
  `muster_request_log_records_total` (counter, `direction`), `muster_request_log_write_failures_total` (counter,
  `direction`), `muster_request_log_pruned_total` (counter), added to the catalogue of `reference.md` (C-23) with the
  generated pages.
- **Secrets** (lint 5): the probe `request_log` sends, through clients of each type with a recording `Writer` on the
  development database, requests that carry each secret of the acceptance statement, then searches every text and
  `jsonb` column of `request_log` and the log for the secret's value.
- **Architecture** (`design/architecture.md`): the Request log in the components of §3.1 and the data model of §6.3.
- **Defaults**: `request_log.max_records`, `request_log.max_age`, `request_log.body_max` (provisional, P-55 to P-57).

## Steps

1. Add the migration with `schema.md` and its migration test. Check: `go test -tags integration ./internal/db/...` on
   PostgreSQL 14 and 17.
2. Write `internal/requestlog`: the writer, the shape masking with `buttons.LooksSigned`, the reads and the pruning.
   Check: `mask_test.go`, `buttons_test.go`, `requestlog_test.go`, `requestlog_integration_test.go`.
3. Record in `internal/outbound` with the masking of Secrets and headers, name the operations of every client, skip the
   empty polls and set the links in the delivery worker and the Secrets of the Account link codes. Check:
   `record_test.go`, the adapters' tests.
4. Add the spec operations and the handlers; the Leader task; the registries. Check: `internal/api/requestlog_test.go`,
   `make generate-check`, `make docs-check`.
5. Add the secret probe and the end-to-end test. Check: `make lint-arch`, `request_log_test.go`, then Verification and
   `make load-test`.

## Verification

```sh
make dev > dev.log 2>&1 &
# the Admin's session (`jar`, `H`) and a Personal access token `PAT` made before the clock moves, as in S-011 and
# S-016; an Integration "lab" with its fake receiver and NOTIFY as in S-020; the Destination "alerts" on ch-alerts (D)
# through "Dev Mattermost" and a Route "db" for team=db to it, as in S-061
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake
MM=$(curl -s -b jar "$API/connections?type=mattermost" | jq -r '.items[] | select(.name=="Dev Mattermost") | .id')

# C-23.AC-1: a Publication is recorded with its bodies and links
curl -s -X PUT $FAM/groups/r1 -d '{"receiver":"lab","route":"{}","labels":{"alertname":"RequestLogCheck"}}' > /dev/null
curl -s -X PUT $FAM/groups/r1/alerts/i1 -d '{"labels":{"team":"db","cluster":"a"}}' > /dev/null
NOTIFY r1 '{"reason":"first notification"}'; sleep 1
curl -s -b jar "$API/request-log?connection=$MM&operation=createPost&limit=1" | jq -c '.items[0] | {direction, operation,
  method, status, via, attempt, d: .destination.id, g: (.alert_group != null)}'
# {"direction":"outbound","operation":"createPost","method":"POST","status":201,"via":"direct","attempt":1,"d":"DS…","g":true}
R=$(curl -s -b jar "$API/request-log?destination=$D&operation=createPost&limit=1" | jq -r '.items[0].id')
curl -s -b jar "$API/request-log/$R" | jq -c '{auth: .request_headers.Authorization, ua: .request_headers["User-Agent"][0],
  body: (.request_body | fromjson | .channel_id != null), resp: (.response_body | fromjson | .id != null)}'
# {"auth":["[redacted]"],"ua":"Muster/0.0.1-SNAPSHOT-…","body":true,"resp":true}
curl -s -b jar "$API/request-log/$R" | grep -c 'muster-dev-mattermost-token'                                  # 0
curl -s -b jar "$API/request-log/$R" | jq -r .request_body | jq -r '[.. | .context?.action? // empty] | unique[]'
# [redacted]

# C-23.AC-2: Telegram polls without updates leave no record, and no record shows the token in its URL
TG=$(curl -s -b jar "$API/connections?type=telegram" | jq -r '.items[] | select(.name=="Dev Telegram") | .id')
curl -s -b jar "$API/request-log?connection=$TG&operation=getUpdates" | jq '.items | length'
# the number of polls that returned updates — 0 on an idle fake, never one per long poll
curl -s -b jar "$API/request-log?connection=$TG" | jq -r '.items[].url' | grep -c '/bot[0-9]' || true          # 0

# C-23.FR-5: refusals
curl -s -b jar "$API/request-log" | jq -c '[.status, .errors[0].code]'                                         # [422,"one_of_required"]
curl -s -b jar "$API/request-log?connection=CN0000000000AA" | jq .status                                      # 404

# C-23.AC-4: the age bound with the development clock (eight days)
curl -s -X POST localhost:8082/_dev/clock -d '{"advance_seconds": 691200}'
curl -s -H "Authorization: Bearer $PAT" "$API/request-log?connection=$MM" | jq '.items | length'               # 0
sleep 5; psql "$MUSTER_DATABASE_URL" -Atc "SELECT count(*) FROM request_log r JOIN connections c ON c.id = r.connection_id
  WHERE c.public_id = '$MM'"                                                                                   # 0
grep -c '"event":"request_log_pruned"' dev.log                                                                 # 1 or more
curl -s localhost:8082/metrics | grep -E '^muster_request_log_(records|pruned)_total'
```

`go test -tags integration ./internal/requestlog/...` shows the count bound and truncation on PostgreSQL 14 and 17,
and `make load-test` its thresholds; their output goes into the pull request.

## Open questions

None.

## Notes

- Suggested commit: `feat(requestlog): record outbound requests with their answers`.
- Synchronous writes are the simplest that works: one `INSERT` per request on the main pool, after the answer. If the
  load test shows them breaching NFR-2, this story batches them per replica instead — an in-memory queue flushed
  every second, dropped and counted when full — without changing the table or the API.
- The development clock moves the business clock, so records written after an advance carry the advanced time; the
  age bound reads the business clock too.
- The masking of signed action ids matters most for Mattermost, whose callback is authenticated only by the action id
  in its body: a Viewer who could read one could forge a press. `buttons.LooksSigned` checks the structure only, so a
  string that merely looks like one is masked too — the safe side.
- The Request log keeps headers as sent, so `User-Agent: Muster/<version>` of #264 is visible in each record.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-23.FR-1 | partial | outbound requests; inbound ones and the checks before saving of new Connections in S-089 |
| C-23.FR-2 | partial | the record without the check kind and run, which S-089 adds |
| C-23.FR-3 | partial | outbound masking and the shape masking of every record; the inbound side in S-089 |
| C-23.FR-4 | full | |
| C-23.FR-5 | partial | the list and the record; the check run on check and test results in S-089 |
| C-23.FR-7 | full | |
| C-23.AC-1 | partial | the record and the API; "Requests" on the Alert Group page in S-090 |
| C-23.AC-2 | partial | polling; the webhook mode's inbound updates in S-089 |
| C-23.AC-3 | partial | outbound requests and the `/start` of polled updates; pushes, presses and webhook updates in S-089 |
| C-23.AC-4 | full | |
| C-23.AC-5 | partial | the outgoing webhook's attempts; checks before saving and refused pushes in S-089 |
