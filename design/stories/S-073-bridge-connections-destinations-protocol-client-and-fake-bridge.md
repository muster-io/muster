---
id: S-073
title: "Bridge Connections, the protocol client with its error classes, `info` and the fake bridge (BE)"
capability: C-22
kind: be
layer: L1
depends_on: [S-069, S-071, S-059]
covers: [C-22.FR-1, C-22.FR-2, C-22.FR-8, C-22.FR-20, C-22.FR-24, C-22.AC-1, C-22.AC-3, C-01.FR-13]
files_touched:
  - api/openapi.yaml
  - api/codegen-bridge.yaml
  - api/bridge_test.go
  - api/README.md
  - Makefile
  - AGENTS.md
  - CONTRIBUTING.md
  - internal/tools/licensecheck/main.go
  - internal/db/migrations/0008_bridge_connections.up.sql
  - internal/db/migrations/0008_bridge_connections.down.sql
  - design/db/schema.md
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - internal/keyring/rotate.go
  - internal/keyring/query.sql
  - internal/keyring/rotate_test.go
  - internal/keyring/rotate_integration_test.go
  - internal/bridge/client.go
  - internal/bridge/client_test.go
  - internal/bridge/info.go
  - internal/bridge/info_test.go
  - internal/bridge/classify.go
  - internal/bridge/classify_test.go
  - internal/connections/connections.go
  - internal/connections/connections_test.go
  - internal/connections/bridge.go
  - internal/connections/bridge_test.go
  - internal/connections/query.sql
  - internal/connections/doctor.go
  - internal/accountlinks/query.sql
  - internal/api/connections.go
  - internal/api/connections_test.go
  - internal/fakes/fakebridge/fakebridge.go
  - internal/fakes/fakebridge/fakebridge_test.go
  - internal/devmode/devmode.go
  - internal/devmode/devmode_test.go
  - internal/cli/dev.go
  - internal/cli/dev_test.go
  - internal/runtime/runtime.go
  - test/e2e/harness.go
  - internal/archlint/secretleak.go
  - internal/logging/events.go
  - web/src/routes/connections.index.tsx
  - web/src/routes/connections.$connectionId.tsx
  - web/src/components/connection-form.tsx
  - web/src/components/connection-check.tsx
  - web/src/locales/en.json
  - web/src/locales/ru.json
acceptance:
  - "[C-22.FR-1] A Bridge Connection is created with a name, the base URL, a write-only `bot_token`, the event delivery mode (`connection.bridge.event_delivery`), a write-only `push_secret` required in push mode, a proxy and its limiter (`connection.bridge.limiter`); reads return `bot_token_status`, `push_secret_status`, the read-only `callback_url` `MUSTER_INGEST_URL/api/v1/callbacks/bridge/<public_id>`, and `info` with `info_read_at`, null before the first check."
  - "[C-22.FR-1] A base URL with a query, a fragment or user information is refused with 422 at `/base_url`; a trailing `/` is dropped; `updateConnection` with a new `base_url` and no `bot_token` is refused with 422 `required` at `/bot_token`, so the stored token is never sent to another address."
  - "[C-22.FR-1] `muster secrets rotate-key` re-encrypts the push secret like every Secret: `encrypted_values` lists `connections.bridge_push_secret`, the registry of `internal/keyring/rotate.go` has the field, and `TestIntegrationRotation` in `rotate_integration_test.go` rotates a Bridge Connection's token and push secret; the token keeps the field `connections.bot_token`."
  - "[C-22.FR-1] With `bridge` in the `Connection` union the SPA's type check passes: the Connections list, the Connection page and `addressChanged` of the Connection form narrow by type; a Bridge Connection is listed by its name with the type \"Bridge\" («Мост») from a locale key, and its Connection page shows its name and Delete but no form, until S-077 adds its labels and its form."
  - "[C-22.FR-2, C-22.AC-1] `checkConnection` calls `getInfo` through the interactive path and returns the steps `info` and `event_delivery` with their latency and path, and the bridge's name, messenger, `protocol_version` `1` and features, which it stores on the Connection; with the fake answering `protocol_version` `2` the `info` step fails with the code `protocol_version_unknown` and \"The bridge speaks protocol version 2; this Muster speaks 1.\", and with a wrong token with `token_invalid` and \"The bridge refused the token.\""
  - "[C-22.FR-2] A Connection in push mode whose bridge offers only `poll` fails the `event_delivery` step with `event_delivery_not_offered` and \"The bridge does not offer push event delivery.\"; an `info` larger than 64 KiB fails the `info` step with `protocol_mismatch` as an answer that does not match the protocol and is not stored; a call that depends on `info` reads it again when Muster's copy is older than `bridge.info_max_age` (`info_test.go`, with a manual clock)."
  - "[C-22.FR-2] A step that fails without a Bridge code of its own carries the code of `delivery.FailureCode` — a network code such as `timeout` for a request without an answer, `blocked` for an address the outbound address policy refuses, `request_failed` for any other error answer (`bridge_test.go`); `checkCodeText` translates `protocol_version_unknown`, `protocol_mismatch` and `event_delivery_not_offered` in English and Russian, so the type check and the translation check of `make lint` pass."
  - "[C-22.FR-8] The client classifies every answer by the problem's `class`, and without one by the protocol's own mapping — `401` and `403` forbidden, `408` transient, `429` and `503` with `Retry-After` rate limited, other `5xx`, timeouts and network errors transient, `404`, `410`, redirects and everything else invalid, never gone — and keeps the problem's `detail` as masked, untrusted text (`classify_test.go`, a case per status)."
  - "[C-22.AC-3] With an HTTP proxy on the Connection, every request of the Connection check reaches the fake bridge only through the fake proxy; with the fake closing the connection, the token and the push secret appear in no log line and no error returned by the API (`secretleak.go` probe `bridge_connections`)."
  - "[C-22.FR-20] The models of `api/bridge.yaml` are generated into `internal/bridge/gen/bridge.gen.go` by `make generate` (models only, no client or server), and `make generate-check` fails when they are stale; `api/bridge_test.go` fails when a schema of `openapi.yaml` marked `x-bridge-schema` differs from that schema of `api/bridge.yaml`, comparing each `$ref` and discriminator mapping by the `x-bridge-schema` of its target, so `BridgeInfo` with `BridgeCapabilities`, `BridgeLimits` and `BridgeButtonStyle` equals `Info` with `Capabilities`, `Limits` and `ButtonStyle`."
  - "[C-22.FR-24] A successful `info` step logs `bridge_info_read` with the Connection, the protocol version and the names of the features; the log event is in the registry and its reference page."
  - "[C-01.FR-13] The fake bridge answers `getInfo` as `api/bridge.yaml` specifies and checks the bearer token; `muster dev` starts it on `127.0.0.1:18094`, prints its address before the last line, \"fake Telegram\", and creates the demo Connection \"Dev Bridge\" in poll mode, without Destinations; the end-to-end harness waits for it like the other fakes; `listLinkableConnections` leaves Bridge Connections out until S-076, so `TestSeveralConnectionsRoute` passes unchanged; `muster doctor` skips Bridge Connections until S-080, so `muster dev doctor` reports nothing for \"Dev Bridge\"."
verify: "make ci test-integration docs-check"
operator_attention: false
issue: 231
---

# S-073. Bridge Connections, the protocol client with its error classes, `info` and the fake bridge (BE)

## Scope

**IN**

- The `bridge` Connection type: base URL, token, event delivery mode, push secret, proxy, limiter, the read-only
  callback address and the last `info`; its Connection check through `getInfo`; the push secret in key rotation.
- The protocol client over the outbound package, with the models generated from `api/bridge.yaml`, the answer
  classification of C-22.FR-8 and `info` with its refresh.
- The fake bridge with `info` and the token check, and the demo Connection of `muster dev`.

**OUT**

- Bridge Destinations, the Destination check, `muster doctor` and the Mention settings of Bridges (S-080).
- The CI check that refuses a breaking change of `api/bridge.yaml` (S-079, which publishes the protocol).
- Bridge delivery — envelopes, posts, edits, Thread replies, idempotency, test messages, the Broken probe — and the
  Messengers → Bridge page (S-074); edit windows (S-081).
- Events by polling and push, presses, answers, Text commands and deleted messages (S-075).
- Account links and Mentions of linked people (S-076); the pages and labels (S-077); the example bridge and
  `muster bridge check` (S-078); "Write a bridge" and the protocol reference on the site (S-079).
- Interim states until later stories: the profile offers no Bridge Connection for linking (S-076 lifts the filter);
  the SPA lists a Bridge Connection by its name only, without the messenger's name or the bridge's host, and its
  Connection page has no form and no check (S-077); `muster doctor` skips Bridge Connections (S-080).

## Contracts

- **Spec** (`api/openapi.yaml`, changed here first, then generated): `Connection`, `ConnectionInput` and the type
  filters gain `bridge`; new schemas `BridgeConnectionBase` (`type`, `name`, `base_url`, `event_delivery`, `limiter`),
  `BridgeConnection` (`id`, `bot_token_status`, `push_secret_status`, `callback_url`, `info`, `info_read_at`, `proxy`,
  `destination_count`, `created_at`, `etag`), `BridgeConnectionInput` (`bot_token` and `push_secret`, write-only, and
  `proxy`), `BridgeEventDelivery` (`poll`, `push`) and `BridgeInfo`, the `Info` of `api/bridge.yaml` copied with
  `x-bridge-schema: Info` together with the schemas it refers to, `BridgeCapabilities`, `BridgeLimits` and
  `BridgeButtonStyle`, each marked with the `x-bridge-schema` of its original, so that no generic name such as `Limits`
  enters Muster's API; the new schemas are joined to the `Connection` and `ConnectionInput` discriminators; the secret
  fields follow the `<field>` / `<field>_status` pattern of `bot_token` and `signing_secret`; `ConnectionCheckStep.name`
  gains `info` and `event_delivery`; `ConnectionCheckResult` returns `info` for a Bridge; `NullableConnectionCheckCode`
  gains the codes of the Bridge steps below, and its description names them, `token_invalid` also for `info`.
- **Operations implemented for the type**: `listConnections`, `createConnection`, `getConnection`,
  `updateConnection`, `deleteConnection` (`connections:read` / `:write`), `checkConnection` (`connections:write`,
  interactive). `listConnectionChannels` answers `409` for a Bridge Connection: the protocol has no channel list, the
  Admin enters the channel id. `listLinkableConnections` leaves out `type = 'bridge'` until S-076
  (`internal/accountlinks/query.sql`), because `LinkableConnection.messenger` has no `bridge` before then.
- **Schema** (migration `0008_bridge_connections`, expand only, `schema.md`): `connections.type` accepts `bridge`; new
  columns `bridge_base_url`, `bridge_event_delivery` (`poll`, `push`), `bridge_events_cursor` (written by S-075),
  `bridge_push_secret_ciphertext`, `_key_id`, `_updated_at`, `bridge_info` (`jsonb`) and `bridge_info_read_at`; the
  token uses the `bot_token_*` columns; `connections_type_fields_check` gains the Bridge branch (base URL and mode set,
  the other types' columns null, a push secret in push mode until the Connection is deleted), and a check keeps the push
  secret's ciphertext and key id together; the view `encrypted_values` gains `connections.bridge_push_secret`. Old
  replicas keep working while new ones roll out: the CHECKs are widened (`NOT VALID`, then `VALIDATE`), every row an old
  replica writes satisfies them, and `encrypted_values` is replaced with the same columns.
- **Key rotation** (`internal/keyring/rotate.go`, `query.sql`): `encryptedFields` gains `connections.bridge_push_secret`
  with `ListRotateBridgePushSecret` and `ReencryptBridgePushSecret`, so `muster secrets rotate-key`, the Keyring status
  and `muster doctor` see it; the token needs nothing new, because it is the field `connections.bot_token`.
- **Connections** (`internal/connections/bridge.go`): the base URL rules of C-22.FR-1; the token and the push secret
  through the secret-field helper of S-013, encrypted with the associated data `connections.bot_token` and
  `connections.bridge_push_secret`; a change of `event_delivery` to `push` requires a `push_secret`; Audit log entries
  as for the other types (`secret_changed` for both secrets); `deleteConnection` follows S-039. `muster doctor`
  (`internal/connections/doctor.go`) skips Connections of the type `bridge`, which it would otherwise check as
  Mattermost ones and fail, until S-080 replaces the skip with `getInfo`.
- **Client** (`internal/bridge/client.go`, `info.go`, `classify.go`): `getInfo`, and the request plumbing every later
  story uses — `<base>/v1/<path>`, `Authorization: Bearer <token>`, JSON in and out with the generated models,
  `application/problem+json` decoded into `Problem` — through `internal/outbound` with the Connection's proxy, in the
  client class of the caller (ADR-0015), with a connect timeout of 5 s and a call timeout of 10 s, as the Mattermost
  client has (`pollEvents` takes its own, S-075); each `message_id` and `channel_id` is percent-encoded as one path
  segment. The token is registered for redaction. `info` reads `protocol_version` before it validates the rest, so a
  newer bridge fails with the version named, not with a schema error, and refuses an `info` larger than 64 KiB before
  it parses it.
  `Classify` maps an answer to `transient`, `rate_limited` (with `Retry-After` and `scope`), `gone` (with `reason`),
  `forbidden` or `invalid` by its `class`, and without one by the protocol's mapping of ADR-0017 — `408` is
  `transient`, and no status code is ever `gone`. `InfoCache` keeps `info` per Connection and re-reads it through the
  caller's client class when `bridge_info_read_at` is older than `bridge.info_max_age` on the business clock (S-020's
  clock table).
- **Connection check** (C-22.FR-2): through `delivery.Interactive` on the Connection's limiter (`ReadOp`), steps `info`
  (latency, `via`) and `event_delivery`; failures with the texts of
  [reference.md](../prd/l1/reference.md#banners-warnings-and-notices); a passing `info` step stores `bridge_info` and
  `bridge_info_read_at`, also when `event_delivery` fails. Each failure carries its code (`ConnectionCheckStep.code`)
  next to its English text, as the other types' checks do: `info` → `token_invalid` (the bridge refused the token),
  `protocol_version_unknown`, `protocol_mismatch` (an answer that does not match the protocol, or an `info` larger than
  64 KiB); `event_delivery` → `event_delivery_not_offered`; any step → the code of `delivery.FailureCode` for a request
  without an answer or one the outbound address policy refuses, and `request_failed` for any other error answer, with
  the bridge's masked `detail` as the text. A passing step has no code.
- **SPA type check** (`connections.index.tsx`, `connections.$connectionId.tsx`, `connection-form.tsx`): the spec's new
  union member breaks the narrowing of `connectionTypeName`, `connectionAccount`, `botName` and `addressChanged`; each
  gets an explicit `bridge` branch — the name "Bridge" («Мост») from the locale key `connections.types.bridge`, no
  account, no bot name, the base URL as the address. The Connection page renders its form, with the check, for
  Mattermost and Telegram only: a Bridge Connection shows its name and Delete and no form until S-077 adds its fields,
  so the Mattermost form never saves a Bridge. `checkCodeText` (`connection-check.tsx`) switches over every check code,
  so it gains the three new codes, translated in `en.json` and `ru.json`: "The bridge speaks a protocol version this
  Muster does not.", "The bridge's answer does not match the protocol." and "The bridge does not offer this event
  delivery mode."; the English message, which names the version, the mismatch or the mode, stays the detail.
- **Fake bridge** (`internal/fakes/fakebridge`, `127.0.0.1:18094`): two bots, as the protocol's "one token, one bot"
  says, with the published tokens `muster-dev-bridge-token`, used by the demo Connection, and
  `muster-dev-bridge-token-b`, for which `muster dev` creates no Connection and which the checks of the Bridge stories
  use; each bot has its own messages and events. `getInfo` with the messenger "Fake chat", every feature on and both
  delivery modes by default; `401` for any other token and for a revoked one; control endpoints `/_fake/config`
  (protocol version, features, limits, user lookup, delivery modes, revoked tokens, a padding of `info` for the size
  check), `/_fake/requests` and `/_fake/faults` of the fake-server harness. `fakebridge_test.go` checks every answer
  against `api/bridge.yaml` with a request and response validator built from the spec. AGENTS.md's development mode,
  the usage of `muster dev` (`internal/cli/dev.go`) and CONTRIBUTING.md list the fake bridge and its two tokens.
  `muster dev` prints its address before "fake Telegram", which stays the last address line because both end-to-end
  suites wait for it; `test/e2e/harness.go` gains the fake bridge in `FakeURLs` and in the loop that waits for the
  fakes to answer (the Playwright suite's constants come with S-077, its first user). Later stories add channels
  (S-080), messages (S-074, S-081), events (S-075) and people (S-076).
- **Generated models**: `api/codegen-bridge.yaml` (oapi-codegen, `models` only) writes
  `internal/bridge/gen/bridge.gen.go`, which the `\.gen\.go$` rule of `.testcoverage.yml` already leaves out of the
  coverage gate; `make generate` and `generate-check` run it; the Makefile's `GENERATED` list, the generated globs of the
  licence check and the generated paths of AGENTS.md gain the directory.
- **Copied schemas** (`api/bridge_test.go`): every schema of `openapi.yaml` with `x-bridge-schema: <name>` equals the
  schema `<name>` of `api/bridge.yaml`, apart from descriptions; a `$ref` or a discriminator mapping is compared by the
  `x-bridge-schema` of the schema it names, so `BridgeInfo` referring to `BridgeCapabilities` equals `Info` referring to
  `Capabilities`. S-075's copies of `Event` and its schemas (`BridgeEvent`, `BridgeEventBase`, `BridgePerson`, …) are
  checked the same way without changing the test.
- **Protocol compatibility**: none here. The blocking `oasdiff` check of `api/bridge.yaml` comes with S-079, which
  publishes the protocol; until then the Bridge stories, which implement the protocol for the first time, may still
  correct it (ADR-0017).
- **Log events**: `bridge_info_read` (info: the Connection, the protocol version, the features' names).
- **Secrets** (lint 5): probe `bridge_connections` pushes the token and the push secret through create, check and a
  failed request; S-080 adds the Destination check.
- **Defaults**: `connection.bridge.limiter`, `connection.bridge.event_delivery`, `bridge.info_max_age`,
  `delivery.interactive_budget`.

## Steps

1. Generate the models, add the copied-schema test, and write the client with `Classify` and `info`. Check:
   `client_test.go`, `classify_test.go`, `info_test.go` against `httptest` servers, a redirect included;
   `go test ./api/...`.
2. Add the migration, key rotation of the push secret, the Connection type and its check. Check: `bridge_test.go`,
   `connections_test.go`, `go test -tags integration ./internal/keyring/...`.
3. Narrow the SPA by type and translate the type's name and the new check codes. Check: `make lint`.
4. Write the fake bridge, the demo Connection with its address line, the usage and CONTRIBUTING.md, the harness, the
   skip in `muster doctor`, the linkable filter and the secret probe. Check: `fakebridge_test.go`, then Verification.

## Verification

```sh
make dev > dev.log 2>&1 &
# the Admin's session (`jar`, `H`) as in S-011
API=localhost:8080/api/v1; FB=127.0.0.1:18094/_fake

# C-22.FR-1, FR-2, AC-1: a Connection and its check
C=$(curl -s "${H[@]}" $API/connections -d '{"type":"bridge","name":"br","base_url":"http://127.0.0.1:18094/",
  "bot_token":"muster-dev-bridge-token-b","event_delivery":"poll","proxy":{"enabled":false},"limiter":{"limit":10,"per_seconds":1}}' | jq -r .id)
curl -s -b jar $API/connections/$C | jq -c '{base_url, t: .bot_token_status.set, info, cb: (.callback_url | test("/api/v1/callbacks/bridge/CN[0-9A-Z]{12}$"))}'
# {"base_url":"http://127.0.0.1:18094","t":true,"info":null,"cb":true}
curl -s "${H[@]}" -X POST $API/connections/$C/checks | jq -c '{ok, s: [.steps[] | {name, ok, via}], v: .info.protocol_version, m: .info.messenger.name}'
# {"ok":true,"s":[{"name":"info","ok":true,"via":"direct"},{"name":"event_delivery","ok":true,"via":"direct"}],"v":"1","m":"Fake chat"}
grep -c 'bridge_info_read' dev.log                                                 # 1 or more
curl -s -X PUT $FB/config -d '{"protocol_version":"2"}' > /dev/null
curl -s "${H[@]}" -X POST $API/connections/$C/checks | jq -r '.steps[0] | "\(.code): \(.message)"'
# protocol_version_unknown: The bridge speaks protocol version 2; this Muster speaks 1.
curl -s -X PUT $FB/config -d '{"protocol_version":"1","revoked":["muster-dev-bridge-token-b"]}' > /dev/null
curl -s "${H[@]}" -X POST $API/connections/$C/checks | jq -r '.steps[0] | "\(.code): \(.message)"'
# token_invalid: The bridge refused the token.
curl -s -X PUT $FB/config -d '{"revoked":[],"info_padding_bytes":70000}' > /dev/null
curl -s "${H[@]}" -X POST $API/connections/$C/checks | jq -r '.steps[0] | "\(.code): \(.message)"'
# protocol_mismatch: The bridge's answer does not match the protocol: info is larger than 64 KiB
curl -s -X PUT $FB/config -d '{"info_padding_bytes":0,"event_delivery":["poll"]}' > /dev/null

# C-22.FR-1: push needs a secret and a bridge that offers push
ETAG() { curl -s -b jar $API/connections/$C | jq -r .etag; }
curl -s "${H[@]}" -X PUT -H "If-Match: $(ETAG)" $API/connections/$C -d '{"type":"bridge","name":"br","base_url":"http://127.0.0.1:18094",
  "event_delivery":"push","proxy":{"enabled":false},"limiter":{"limit":10,"per_seconds":1}}' | jq -c '[.status, .errors[0].pointer]'
# [422,"/push_secret"]
curl -s "${H[@]}" -X PUT -H "If-Match: $(ETAG)" $API/connections/$C -d '{"type":"bridge","name":"br","base_url":"http://127.0.0.1:18094",
  "event_delivery":"push","push_secret":"whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw","proxy":{"enabled":false},"limiter":{"limit":10,"per_seconds":1}}' > /dev/null
curl -s "${H[@]}" -X POST $API/connections/$C/checks | jq -r '.steps[1] | "\(.code): \(.message)"'
# event_delivery_not_offered: The bridge does not offer push event delivery.
curl -s -X PUT $FB/config -d '{"event_delivery":["poll","push"]}' > /dev/null
curl -s "${H[@]}" -X PUT -H "If-Match: $(ETAG)" $API/connections/$C -d '{"type":"bridge","name":"br","base_url":"http://127.0.0.1:9",
  "event_delivery":"poll","proxy":{"enabled":false},"limiter":{"limit":10,"per_seconds":1}}' | jq -c '[.status, .errors[0].pointer]'
# [422,"/bot_token"]
psql "$MUSTER_DATABASE_URL" -tAc "SELECT field FROM encrypted_values WHERE field LIKE 'connections.%' ORDER BY 1" | sort -u
# connections.bot_token
# connections.bridge_push_secret

# C-22.AC-3: through the fake HTTP proxy, and no secret in an error
curl -s "${H[@]}" -X PUT -H "If-Match: $(ETAG)" $API/connections/$C -d '{"type":"bridge","name":"br","base_url":"http://127.0.0.1:18094",
  "event_delivery":"poll","proxy":{"enabled":true,"type":"http","address":"127.0.0.1:18091"},"limiter":{"limit":10,"per_seconds":1}}' > /dev/null
curl -s -X DELETE 127.0.0.1:18091/_fake/requests; curl -s -X DELETE $FB/requests
curl -s "${H[@]}" -X POST $API/connections/$C/checks | jq -r '.steps[0].via'   # proxy
# the requests of this Connection's check only: from S-075 on, the fake also records the polls of "Dev Bridge"
echo $(( $(curl -s $FB/requests | jq '[.[] | select(.path == "/v1/info")] | length') - $(curl -s 127.0.0.1:18091/_fake/requests | jq length) ))   # 0
curl -s -X POST $FB/faults -d '{"path":"/v1/info","delay_ms":12000,"times":1}' > /dev/null   # longer than the check waits
curl -s "${H[@]}" -X POST $API/connections/$C/checks | grep -c 'muster-dev-bridge-token-b'   # 0
grep -c 'muster-dev-bridge-token-b\|whsec_MfKQ' dev.log                            # 0

# C-01.FR-13: the demo Connection; not offered for linking yet
curl -s -b jar $API/connections | jq -r '.items[] | select(.type == "bridge") | .name' | sort | tr '\n' ' '   # Dev Bridge br
curl -s -b jar $API/me/account-links/connections | jq -r '[.items[].messenger] | unique | join(",")'      # mattermost,telegram
./bin/muster dev doctor | grep -c 'Dev Bridge'                                    # 0: skipped until S-080
grep -n 'fake bridge\|fake Telegram' dev.log | tail -2 | cut -d' ' -f3-4          # fake bridge, then fake Telegram
```

`go test -tags integration -run TestIntegrationRotation ./internal/keyring/` shows the push secret re-encrypted under
the new key.

## Open questions

None.

## Notes

- Suggested commit: `feat(bridge): add bridge connections and the bridge protocol client`.
- The check codes were added to every Connection and Destination check after this story was written; the Bridge
  steps take the existing codes where the meaning is the same and add three of their own. `connection-check.tsx` and
  the locales are in `files_touched` because `checkCodeText` switches exhaustively over the code enums: a new code
  fails the type check until it is translated.
- S-073 was split by the plan review of the Bridge phase (D323), because it touched about 50 files: S-073 keeps the
  Connection, the client, `info`, the classification and the fake bridge; S-080 takes Destinations, the Destination
  check, `muster doctor` and the Mention settings. The file name is kept.
- The fake bridge is the contract of the Bridge stories with each other: every story extends it with the operations it
  uses, and `fakebridge_test.go` checks each against `api/bridge.yaml`.
- `bridge_info` is the whole `info` as the bridge sent it, so a field added in a later minor version of the protocol is
  kept even before Muster reads it; the 64 KiB cap keeps a broken bridge from filling the row.
- The token is the API field `bot_token`, as for Mattermost and Telegram: a Bridge Connection is one bot, and the
  shared column, its check `connections_bot_token_check` and its key rotation apply unchanged.
- The second plan review of the Bridge phase (D335) kept S-073 whole, at 42 files, and moved the blocking compatibility
  check of `api/bridge.yaml` to S-079: the protocol is frozen once it is published, not while the Bridge stories still
  implement it for the first time.
- While S-073 rolls out, a replica of the previous version reads a Bridge row as a Mattermost Connection
  (`connectionOf`) and fails its checks; this is transient and ends when every replica runs the new version.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-22.FR-1 | partial | the API and key rotation; the page is S-077 |
| C-22.FR-2 | partial | the API; the page is S-077 |
| C-22.FR-8 | partial | the classification; delivery's use of it is S-074 and S-081 |
| C-22.FR-20 | partial | the generated models and the copied-schema test; the published reference and the compatibility check are S-079 |
| C-22.FR-24 | partial | `bridge_info_read`; S-081 and S-075 add theirs |
| C-22.AC-1 | full | |
| C-22.AC-3 | partial | the Connection check; the Destination check is S-080, the delivery paths S-074, the event paths S-075 |
| C-01.FR-13 | partial | the fake bridge and the demo Bridge Connection; S-080, S-074, S-081, S-075 and S-076 extend the fake |
