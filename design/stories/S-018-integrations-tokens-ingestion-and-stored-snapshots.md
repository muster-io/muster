---
id: S-018
title: Integrations, Integration tokens, the ingestion endpoint and Stored Snapshots (BE)
capability: C-05
kind: be
layer: L1
depends_on: [S-016]
covers: [C-05.FR-1, C-05.FR-2, C-05.FR-3, C-05.FR-4, C-05.FR-5, C-05.FR-6, C-05.FR-7, C-05.FR-8, C-05.FR-10, C-05.FR-11, C-05.FR-12, C-05.AC-1, C-05.AC-2, C-05.AC-3, C-05.AC-5, C-05.AC-6, C-05.AC-7, C-05.AC-8, C-04.FR-4, C-02.AC-8, C-01.FR-13]
files_touched:
  - internal/integrations/integrations.go
  - internal/integrations/tokens.go
  - internal/integrations/snippet.go
  - internal/integrations/query.sql
  - internal/integrations/integrations_test.go
  - internal/integrations/snippet_test.go
  - internal/ingest/handler.go
  - internal/ingest/store.go
  - internal/ingest/snapshots.go
  - internal/ingest/query.sql
  - internal/ingest/handler_test.go
  - internal/api/integrations.go
  - internal/api/storedsnapshots.go
  - internal/api/integrations_test.go
  - internal/server/server.go
  - internal/metrics/catalogue.go
  - internal/logging/events.go
  - internal/archlint/secretleak.go
  - internal/fakes/fakealertmanager/fakealertmanager.go
  - internal/fakes/fakealertmanager/fakealertmanager_test.go
  - internal/devmode/devmode.go
  - test/e2e/ingest_test.go
  - test/load/main.go
  - docs/integrations/alertmanager.md
  - design/prd/l1/C-05-integrations.md
  - design/prd/L1.md
acceptance:
  - "[C-05.FR-1, C-05.FR-6] `createIntegration` stores the name (unique among Integrations that are not deleted, otherwise 409 `name_taken`), the description, the Connection mode `webhook_only`, the Static labels and the duplicate window; `updateIntegration` needs `If-Match` (412 when stale, 428 without); every change writes an Audit log entry with a diff; `heartbeat.enabled: true` answers 422 `unsupported` until S-023."
  - "[C-05.FR-2, C-05.AC-6] `createIntegrationToken` returns, once, a value starting with `mstr_int_` and an `alertmanager_snippet` that contains `send_resolved: true`, `max_alerts: 0`, the ingestion URL under `MUSTER_INGEST_URL` and the token; `listIntegrationTokens` never returns a value, and the database keeps only the SHA-256 of the token."
  - "[C-05.FR-3, C-05.AC-1] A webhook from the fake Alertmanager with a valid token — as `Authorization: Bearer` or in the path — gets 202 after the commit and is listed by `listStoredSnapshots` as `pending` with its body exactly as received; the same request without a token gets 401 and is counted with `outcome=\"unauthorized\"`."
  - "[C-05.FR-4, C-05.AC-2] A body larger than `ingest.body_limit` with a valid token gets 413 and is counted with `outcome=\"too_large\"`; the token is checked first, so an oversized request without a valid token gets 401."
  - "[C-05.FR-4, C-05.AC-7] A request whose token matches no Integration is counted with `integration=\"unknown\"` and `outcome=\"unauthorized\"`."
  - "[C-05.FR-3, C-05.AC-8] A valid token with the body `not json` gets 202 and the Stored Snapshot holds exactly those bytes; a body that is not valid UTF-8 is returned with `body_encoding` `base64`; ingestion never answers 400."
  - "[C-05.FR-8, C-05.AC-3] After `deleteIntegration`, the Integration's token gets 401 on the next request, the Integration is not in `listIntegrations`, and its Stored Snapshots are still listed."
  - "[C-05.FR-3, C-05.AC-5] No log line contains a token sent in the path: lines about ingestion requests name the route pattern `/api/v1/ingest/{ingest_token}`, and the lint-5 probe that posts a known token in the path finds it in no output."
  - "[C-04.FR-4] A Personal access token or a Service account token on the ingestion endpoint gets 401, and an Integration token on the API gets 401."
  - "[C-02.AC-8] With `MUSTER_LISTEN_INGEST` equal to `MUSTER_LISTEN_APP`, a webhook to the shared port gets 202 and the API answers on the same port."
  - "[C-05.FR-7, C-05.FR-10] `listStoredSnapshots` filters by Integration, time range and processing state, pages newest first by cursor and never returns a Snapshot older than `retention.stored_snapshots`; `getStoredSnapshot` returns the body, its content type and encoding, the processing state and error; both answer 403 to a Responder."
  - "[C-05.FR-12, C-05.FR-4] `/metrics` exports `muster_integration_info{integration,name}` for every Integration that is not deleted, `muster_ingest_requests_total{integration,outcome}` and `muster_ingest_request_duration_seconds`."
  - "[C-01.FR-13] The fake Alertmanager registers receivers with a token in the header or the path and sends a JSON payload, any raw body or a body of a given size; `muster dev` starts with the Integration `dev-alertmanager` wired to it."
  - "[C-05.FR-5, C-05.FR-11] The snippet's route sets `repeat_interval` to `snippet.repeat_interval` and its comment explains the 5–15 minute range and that noise is controlled in Muster; docs/integrations/alertmanager.md covers one Integration per Alertmanager cluster, the receiver and route, `--dispatch.start-delay` of at least about 2.5 minutes and a persistent volume for HA pairs."
verify: "make ci test-integration e2e"
operator_attention: false
issue: 18
---

# S-018. Integrations, Integration tokens, the ingestion endpoint and Stored Snapshots (BE)

## Scope

**IN**

- Integrations: create, read, list, update and soft delete, with the Connection mode, Static labels, the duplicate
  window and the stored Heartbeat settings.
- Integration tokens with the ready Alertmanager receiver and route snippet, shown once; listing and revoking.
- The ingestion endpoint on the ingest listener: token, then body size, then one insert-only transaction and `202`.
- Stored Snapshots as received: the list and the raw read.
- The ingestion metrics and log events, the fake Alertmanager's receivers and raw sends, the demo Integration of
  `muster dev`, the load test against real ingestion and the Alertmanager page of the documentation.

**OUT**

- Processing Stored Snapshots, the Snapshot count and the `failed` state (S-020).
- The built-in "Muster" Integration and its refusals, the effects of a deletion on Alerts and Internal alerts, the
  Integration warnings (S-021).
- The Heartbeat endpoint, enabling the Heartbeat and the Heartbeat snippet (S-023).
- The Integration pages (S-019).

## Contracts

- **Operations implemented**: `listIntegrations`, `createIntegration`, `getIntegration`, `updateIntegration`,
  `deleteIntegration`, `listIntegrationTokens`, `createIntegrationToken`, `revokeIntegrationToken`,
  `listStoredSnapshots`, `getStoredSnapshot`, `ingestSnapshot`, `ingestSnapshotWithPathToken`. Schemas:
  `Integration(List)`, `IntegrationBase`, `IntegrationInput`, `HeartbeatSettingsInput`, `HeartbeatInfo`,
  `IntegrationToken(List)`, `IntegrationTokenCreate`, `IntegrationTokenCreated`, `StoredSnapshotSummary`,
  `StoredSnapshot`, `StoredSnapshotList`, `SnapshotState`.
- **Integrations** (C-05.FR-1, FR-6, FR-8; `integrations`): the name is unique among Integrations that are not deleted
  (`409 name_taken`); `connection_mode` accepts only `webhook_only` (`integration.connection_mode`); Static labels are
  label names of the Prometheus form (`[a-zA-Z_][a-zA-Z0-9_]*`, otherwise `422 invalid_format` at
  `/static_labels/<name>`) with string values; `duplicate_window_seconds` is at least 1 (the forms pre-fill
  `integration.duplicate_window`). The Heartbeat settings are stored: `enabled: false` with `timeout_seconds`
  (default `integration.heartbeat_timeout`); `enabled: true` answers `422` with `errors[].code = unsupported` at
  `/heartbeat/enabled` until S-023. Reads carry `ingest_url` (`MUSTER_INGEST_URL` + `/api/v1/ingest`), `heartbeat`
  (`state` `not_configured`, `url` = `MUSTER_INGEST_URL` + `/api/v1/heartbeat`), `last_snapshot_at` (the newest
  `received_at` of its Stored Snapshots, read through `stored_snapshots_list_idx`), `snapshot_count` (maintained by
  processing from S-020; `0` before), `warnings` (empty until S-021 and S-023), `open_alert_group_count` (`0` until
  S-029) and `builtin` (`false`; the built-in Integration arrives with S-021). Updates use `If-Match` from `version`;
  deletion takes an optional `If-Match`, sets `deleted_at`, removes the Integration from every list and from token
  lookups at once — `getIntegration` and `updateIntegration` then answer `404` — and keeps its Stored Snapshots, still
  listed by its id, until retention. Writes send the live hint `integration` with the id.
- **Audit log actions** (C-03.FR-14): `integration.created`, `integration.updated`, `integration.deleted`,
  `integration_token.created`, `integration_token.revoked`; a token value never appears in an entry.
- **Integration tokens** (C-05.FR-2, ADR-0011; `integration_tokens`): `mstr_int_` followed by 32 random bytes; only the
  SHA-256 is stored and the value is returned once, in `IntegrationTokenCreated`; `name` is optional; `last_used_at` is
  refreshed at most once a minute and off the request path, so ingestion stays insert-only; a revoked token answers
  `401` from the next request. `heartbeat_snippet` is `null` until S-023.
- **Snippet** (C-05.FR-5): YAML with an Alertmanager receiver `muster-<integration name>` whose `webhook_configs` entry
  has `url: <MUSTER_INGEST_URL>/api/v1/ingest`, `send_resolved: true`, `max_alerts: 0` and
  `http_config.authorization` (`type: Bearer`, `credentials: <token>`), and a child route for that receiver with
  `continue: true`, to be placed first, and `repeat_interval` set to `snippet.repeat_interval`, under a comment that
  recommends 5–15 minutes and says that noise is controlled in Muster, not by long repeat intervals.
- **Ingestion** (C-05.FR-3, FR-4, ADR-0002; ingest listener): `POST /api/v1/ingest` with `Authorization: Bearer` and
  `POST /api/v1/ingest/{ingest_token}`. Order: the token — only `mstr_int_` tokens of an Integration that is not
  deleted and a token that is not revoked; any other credential, an API token included, is `401` — then the size: a
  `Content-Length` above `ingest.body_limit` is refused before reading, and a body read past the limit is refused, both
  `413` (`payload-too-large`). Then one short transaction: `INSERT … ON CONFLICT DO NOTHING` into `snapshot_bodies`
  (SHA-256 per Organization and UTC day), the `stored_snapshots` row (`source = 'webhook'`, `received_at` from Muster's
  clock, the request's `Content-Type`, `state = 'pending'`), `NOTIFY` for the processing worker of S-020, commit, then
  `202` with an empty body. Nothing is parsed, the Integration row is not updated, there is no request validation and
  no rate limit; the only `5xx` is a failed write (`500`).
- **Stored Snapshots** (C-05.FR-7, FR-10; `stored_snapshots`, `snapshot_bodies`): `listStoredSnapshots` needs the
  `integration` parameter and filters by `from`, `to` and `state`, newest first with a cursor on `(received_at, id)`;
  `getStoredSnapshot` finds the row by `public_id`. Both hide everything older than `retention.stored_snapshots`; the
  body is returned as text with `body_encoding` `utf8`, or as `base64` when the bytes are not valid UTF-8. Partition
  retention itself is S-008. Permission `stored-snapshots:read`.
- **Metrics** (C-05.FR-4, FR-12): `muster_ingest_requests_total{integration,outcome}` (`accepted`, `unauthorized`,
  `too_large`; `integration` is the id of the token's Integration, or `unknown` when the token matches no Integration
  that is not deleted — P-10, open question 1), `muster_ingest_request_duration_seconds` (`le` buckets from 1 ms to
  10 s), `muster_integration_info{integration,name}` for every Integration that is not deleted.
- **Log events**: `snapshot_accepted` (INFO: `integration`, `stored_snapshot`, `size_bytes`), `ingest_rejected` (INFO:
  `integration`, `outcome`, `route_pattern`, `client_address`). Every line about an ingestion request names the route
  pattern, never the path (C-05.FR-3); the lint-5 probe of S-005 gains a request with a known token in the path.
- **Listeners** (C-02.FR-3): the ingest handler of S-006 gets the two ingestion routes; with `MUSTER_LISTEN_INGEST`
  equal to `MUSTER_LISTEN_APP` the shared port routes `/api/v1/ingest` to it.
- **Fake Alertmanager** (C-01.FR-13, `127.0.0.1:19093`): `POST /_fake/receivers`
  `{"name", "url", "token", "token_in": "header"|"path"}` registers a receiver; `POST /_fake/send` sends one request to
  a receiver with `{"payload": <JSON>}`, `{"raw": "<text>", "content_type"}`, or `{"size_bytes": N}` (a filler body of
  that size), optionally `"token": "<other>"` or `"no_token": true`, and answers `{"status": <code>}`. The load mode of
  S-004 sends to a registered receiver.
- **Development mode**: `MUSTER_INGEST_URL` defaults to `http://localhost:8081` in `muster dev`; at start it ensures the
  Integration `dev-alertmanager` (Static label `cluster=dev`) with a token and registers it with the fake Alertmanager
  as the receiver `muster`. `make load-test` now drives real ingestion.
- **Documentation** (C-05.FR-11): `docs/integrations/alertmanager.md` — one Integration per Alertmanager cluster (an HA
  pair is one), the receiver and route of the snippet, `--dispatch.start-delay` of at least twice the longer of the
  rule evaluation interval and the rule evaluator's resend delay plus a margin (about 2.5 minutes or more at a 1-minute
  evaluation interval, F-043), a persistent volume for the data of HA pairs (F-042), token rotation with a second
  token, and the ingest address behind a reverse proxy.

## Steps

1. Write the Integrations domain with validation, soft deletion, the Audit log and the live hint. Check: tests cover
   `name_taken`, `If-Match`, the Static label names, the `unsupported` Heartbeat and the deleted Integration's absence.
2. Write tokens and the snippet. Check: tests show the value once, only the hash stored, and a golden snippet.
3. Write the ingestion handler, the insert-only transaction, the metrics and log events, and mount the routes. Check:
   tests cover both token forms, the order token-then-size, `413` without reading, a revoked token, an API token, the
   shared port and the lint-5 probe.
4. Write the Stored Snapshot API. Check: tests cover the filters, the cursor, retention and `base64`.
5. Extend the fake Alertmanager, `muster dev`, the load test and the documentation. Check: Verification below and
   `make load-test` reports accepted requests.
6. Confirm or change P-10. Check: C-05.FR-4 and the open questions of L1.md say so.

## Verification

```sh
make dev > dev.log 2>&1 &
# the Admin's session as in S-011: cookie jar `jar`, CSRF token in `CSRF`,
# H=(-b jar -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json')
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake

INT=$(curl -s "${H[@]}" -d '{"name":"prod-eu","connection_mode":"webhook_only","static_labels":{"cluster":"prod-eu"},
  "duplicate_window_seconds":45,"heartbeat":{"enabled":false}}' $API/integrations | jq -r .id)
curl -s "${H[@]}" -d '{"name":"rotation-1"}' "$API/integrations/$INT/tokens" | tee /tmp/tok.json | jq -r '.value[0:9]'
# mstr_int_
TOK=$(jq -r .value /tmp/tok.json)
jq -r .alertmanager_snippet /tmp/tok.json | grep -E 'url:|send_resolved|max_alerts|credentials|continue|repeat_interval'
#       - url: http://localhost:8081/api/v1/ingest
#         send_resolved: true
#         max_alerts: 0
#             credentials: mstr_int_…
#       continue: true
#       repeat_interval: 10m
curl -s -b jar "$API/integrations/$INT/tokens" | jq '[.items[] | has("value")] | any'
# false
psql "$MUSTER_DATABASE_URL" -Atc "SELECT octet_length(token_hash) FROM integration_tokens WHERE name = 'rotation-1'"
# 32

# C-05.AC-1: the fake Alertmanager sends with the token; without one it is refused
curl -s -X POST $FAM/receivers -d "{\"name\":\"prod-eu\",\"url\":\"http://127.0.0.1:8081/api/v1/ingest\",\"token\":\"$TOK\"}"
curl -s -X POST $FAM/send -d '{"receiver":"prod-eu","payload":{"version":"4","groupKey":"{}:{alertname=\"T\"}","status":"firing","alerts":[]}}' | jq .status
# 202
curl -s -b jar "$API/stored-snapshots?integration=$INT" | jq -c '.items[0] | {state, size_bytes}'
# {"state":"pending","size_bytes":…}
curl -s -X POST $FAM/send -d '{"receiver":"prod-eu","raw":"{}","no_token":true}' | jq .status
# 401
curl -s -o /dev/null -w '%{http_code}\n' -X POST -d '{}' "localhost:8081/api/v1/ingest/$TOK"            # 202
curl -s -o /dev/null -w '%{http_code}\n' -X POST -H 'Authorization: Bearer mstr_int_nope' -d '{}' localhost:8081/api/v1/ingest
# 401
curl -s localhost:8082/metrics | grep -E 'muster_ingest_requests_total\{integration="unknown",outcome="unauthorized"\}'
# muster_ingest_requests_total{integration="unknown",outcome="unauthorized"} 2

# C-05.AC-2: too large, and the token before the size
curl -s -X POST $FAM/send -d '{"receiver":"prod-eu","size_bytes":17000000}' | jq .status                 # 413
curl -s -X POST $FAM/send -d '{"receiver":"prod-eu","size_bytes":17000000,"no_token":true}' | jq .status  # 401
curl -s localhost:8082/metrics | grep "muster_ingest_requests_total{integration=\"$INT\",outcome=\"too_large\"}"
# muster_ingest_requests_total{integration="NT…",outcome="too_large"} 1

# C-05.AC-8: any body is stored as received
curl -s -X POST $FAM/send -d '{"receiver":"prod-eu","raw":"not json","content_type":"text/plain"}' | jq .status   # 202
SS=$(curl -s -b jar "$API/stored-snapshots?integration=$INT&limit=1" | jq -r '.items[0].id')
curl -s -b jar "$API/stored-snapshots/$SS" | jq -c '{body, body_encoding, content_type, state}'
# {"body":"not json","body_encoding":"utf8","content_type":"text/plain","state":"pending"}
printf '\xff\xfe' | curl -s -o /dev/null -X POST -H "Authorization: Bearer $TOK" --data-binary @- localhost:8081/api/v1/ingest
curl -s -b jar "$API/stored-snapshots/$(curl -s -b jar "$API/stored-snapshots?integration=$INT&limit=1" | jq -r '.items[0].id')" | jq -r .body_encoding
# base64

# C-04.FR-4: API tokens on ingestion, and the Integration token on the API
PAT=$(curl -s "${H[@]}" -d '{"name":"p","permissions":["integrations:read"]}' $API/me/personal-access-tokens | jq -r .value)
curl -s -o /dev/null -w '%{http_code}\n' -X POST -H "Authorization: Bearer $PAT" -d '{}' localhost:8081/api/v1/ingest   # 401
curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $TOK" $API/integrations                           # 401

# C-05.AC-5: the path token reaches no log line
grep -c "$TOK" dev.log                                                                   # 0
grep -m1 '"route_pattern":"/api/v1/ingest/{ingest_token}"' dev.log | jq -r .event          # snapshot_accepted

# Stored Snapshots are Admin-only (log in as a Responder created as in S-011, cookie jar `resp`)
curl -s -o /dev/null -w '%{http_code}\n' -b resp "$API/stored-snapshots?integration=$INT"                             # 403

# C-05.AC-3: deletion
curl -s -o /dev/null -w '%{http_code}\n' "${H[@]}" -X DELETE "$API/integrations/$INT"                                  # 204
curl -s -X POST $FAM/send -d '{"receiver":"prod-eu","raw":"{}"}' | jq .status                                          # 401
curl -s -b jar $API/integrations | jq --arg i "$INT" '[.items[].id] | index($i)'                                     # null
curl -s -b jar "$API/stored-snapshots?integration=$INT" | jq '.items | length'                                        # 4
curl -s localhost:8082/metrics | grep -c "muster_integration_info{integration=\"$INT\""                                # 0

# C-02.AC-8: one port for app and ingest
kill %1; MUSTER_LISTEN_INGEST=:8080 make dev > dev2.log 2>&1 &
# create an Integration and a token TOK2 on this instance as above
curl -s -o /dev/null -w '%{http_code}\n' -X POST -H "Authorization: Bearer $TOK2" -d '{}' localhost:8080/api/v1/ingest  # 202
curl -s -o /dev/null -w '%{http_code}\n' -b jar localhost:8080/api/v1/integrations                                    # 200
```

`make load-test` against `muster dev` reports 3,000 accepted requests and no rejections; the pull request records its
summary.

## Open questions

1. P-10: the contract labels a request with the id of the token's Integration when the token is known — a revoked
   token included — and with `unknown` when it matches no Integration or only a deleted one, so that
   `MusterIngestRejected` names an Integration whose old token is still in use. Confirm, and close P-10 in L1.md and
   C-05.FR-4.

## Notes

- Suggested commit: `feat(ingest): add integrations, integration tokens and the ingestion endpoint`.
- The ingestion path is insert-only on purpose (`design/db/schema.md` §4.6): the Snapshot count arrives with
  processing in S-020; until then `snapshot_count` is `0` while `last_snapshot_at` already shows the newest Snapshot.
- In production `MUSTER_INGEST_URL` defaults to `MUSTER_PUBLIC_URL`; with separate listeners the reverse proxy routes
  `/api/v1/ingest` and `/api/v1/heartbeat` to the ingest port, which the documentation page says.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-05.FR-1 | partial | the model and API; the form and the precision text are S-019, the Heartbeat settings S-023 |
| C-05.FR-2 | partial | the API; the dialog is S-019 |
| C-05.FR-3 | full | |
| C-05.FR-4 | partial | the endpoint and its counters; a body that cannot be processed fails in S-020 |
| C-05.FR-5 | partial | the snippet; the dialog showing it is S-019 |
| C-05.FR-6 | partial | stored; applied to every Alert by processing in S-020 |
| C-05.FR-7 | partial | last Snapshot time and the Stored Snapshot API; the count and the processing states S-020, the page S-019 |
| C-05.FR-8 | partial | the API; the dialog is S-019, the effects on Alerts and Alert Groups S-021 and S-028 |
| C-05.FR-10 | full | together with the partition retention of S-008 |
| C-05.FR-11 | full | |
| C-05.FR-12 | full | |
| C-05.AC-1 | full | |
| C-05.AC-2 | full | |
| C-05.AC-3 | partial | the API; the list page is S-019 |
| C-05.AC-5 | full | |
| C-05.AC-6 | partial | the API; the dialog that shows them once is S-019 |
| C-05.AC-7 | full | |
| C-05.AC-8 | partial | stored as received; the state `failed` is S-020 |
| C-04.FR-4 | full | together with S-016 |
| C-02.AC-8 | full | together with S-006 |
| C-01.FR-13 | partial | the fake Alertmanager's receivers and raw sends, the demo Integration |
