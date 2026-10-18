---
id: S-087
title: "Check a Connection before saving it, for Mattermost, Telegram and Bridge Connections (BE)"
capability: C-13
kind: be
layer: L1
depends_on: [S-077, S-076]
covers: [C-13.FR-2, C-13.FR-14, C-13.AC-17, C-14.FR-11, C-14.AC-6, C-14.AC-20, C-22.FR-1, C-22.FR-2, C-22.AC-26]
files_touched:
  - api/openapi.yaml
  - internal/api/connections.go
  - internal/api/connections_test.go
  - internal/api/server.go
  - internal/connections/draft.go
  - internal/connections/draft_test.go
  - internal/connections/connections.go
  - internal/connections/connections_test.go
  - internal/connections/telegram.go
  - internal/connections/telegram_test.go
  - internal/connections/bridge.go
  - internal/connections/bridge_test.go
  - internal/delivery/interactive.go
  - internal/delivery/limiter_test.go
  - internal/mattermost/client.go
  - internal/mattermost/check.go
  - internal/mattermost/check_test.go
  - internal/telegram/check.go
  - internal/telegram/check_test.go
  - internal/bridge/client.go
  - internal/bridge/check.go
  - internal/bridge/check_test.go
  - internal/fakes/fakemattermost/fakemattermost.go
  - internal/fakes/fakemattermost/fakemattermost_test.go
  - internal/archlint/secretleak.go
  - test/e2e/connection_drafts_test.go
  - web/src/components/connection-check.tsx
  - web/src/components/connection-check.test.tsx
  - web/src/components/connection-form.tsx
  - web/src/components/connection-form.test.tsx
  - web/src/components/telegram-connection-fields.test.tsx
  - web/src/components/bridge-connection-fields.test.tsx
  - web/src/routes/connections.new.tsx
  - web/src/routes/connections.$connectionId.tsx
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/connection-checks.spec.ts
  - web/e2e/telegram-connection.spec.ts
  - docs/messengers/mattermost.md
  - docs/messengers/telegram.md
  - docs/messengers/bridge.md
  - docs/messengers/telegram-restricted-networks.md
  - design/facts.md
  - design/prd/l1/C-13-mattermost.md
acceptance:
  - "[C-13.FR-14] `checkConnectionDraft` (`POST /connections/checks`, `connections:write`, interactive) checks a draft of each Connection type with the steps and step codes of `checkConnection` and stores nothing: after passing draft checks of a new Mattermost, Telegram and Bridge Connection, `listConnections` lists what it listed before and the Audit log has no new entry; a draft check of a saved Connection leaves its `bot_username`, its `info` and its `etag` unchanged (`internal/api/connections_test.go`, `draft_test.go`, the Verification)."
  - "[C-13.FR-14, C-14.FR-11, C-22.FR-1] Which token goes where, one case per rule and per type (`draft_test.go`): a token typed in the draft is sent to the address typed in the draft; with no token typed, the stored token of `connection_id` is used only when the draft's address, after the normalization a save applies, and its proxy type and address are the saved ones; otherwise only the dry probe of the type runs and the other steps are skipped; a stored proxy password is used only with the saved proxy type and address. The probe `connection_drafts` of `secretleak.go` checks a draft that changes the address and one that changes the proxy: the stored token reaches neither fake, no log line and no returned error."
  - "[C-13.FR-2, C-13.AC-17] The dry probe of a Mattermost Connection is `GET <server>/api/v4/system/ping` without an `Authorization` header: it passes on `200` JSON whose `status` is `OK`, and fails with `wrong_path_prefix` on `404`, `not_mattermost` on an answer that is not JSON or a `200` whose `status` is not `OK`, `unexpected_status` on another status with JSON and the network codes of `delivery.FailureCode` (`internal/mattermost/check_test.go`); the fake Mattermost server answers the ping without a token (`fakemattermost_test.go`); in `connection_drafts_test.go` the edit draft of a saved Mattermost Connection with only the server URL changed to the second fake server makes only that probe there, and with the token typed again the step `token` with the typed token."
  - "[C-22.FR-2, C-22.AC-26] The dry probe of a Bridge Connection is `GET <base>/v1/info` without an `Authorization` header: it passes on `401` with an `application/problem+json` body, and fails with `not_bridge` on an answer that is not a problem, `wrong_path_prefix` on `404` and `unexpected_status` on another status with a problem (`internal/bridge/check_test.go`); `connection_drafts_test.go` checks a new Bridge Connection's draft against the fake bridge — `info` and `event_delivery`, the `info` returned and not stored — and an edit draft with only the base URL changed, which makes only the dry probe and fails with `not_bridge` against the fake Mattermost server's address."
  - "[C-14.FR-11, C-14.AC-6, C-14.AC-20] A Telegram draft with a typed token runs the dry probe, `getMe` and `getWebhookInfo` against the draft's base URL with that token; an edit draft with a changed base URL and no token, or with only the proxy changed and no token, runs only the dry probe and skips the other two (`internal/telegram/check_test.go`, `connection_drafts_test.go`, which reads the fake Telegram server's recorded requests to show that the stored token reached no other address)."
  - "[C-13.FR-14] A draft that names a saved Connection takes its limiter token from that Connection and answers `503` with `Retry-After` when none frees within `delivery.interactive_budget`; a draft of a new Connection takes no limiter token (`limiter_test.go`); a `connection_id` that names no Connection, a deleted one or one of another type is refused with `422` `unknown_id` at `/connection_id`, and an address in the wrong format with `422` `invalid_format` at its pointer (`internal/api/connections_test.go`)."
  - "[C-13.FR-14, C-13.AC-17, C-14.AC-20, C-22.AC-26] The create form of every Connection type has \"Check connection\", which shows the steps of the draft before anything is saved; the edit form checks the draft while it holds unsaved changes and the saved Connection otherwise, and with a changed address or proxy and no token typed it says that only the dry probe ran and the token must be typed again, or the changes saved, to check the rest (`connection-check.test.tsx`, `connection-form.test.tsx`, `connection-checks.spec.ts`, `telegram-connection.spec.ts`)."
  - "Every new text exists in English and Russian, the Connection forms show no horizontal scrolling at 360 px wide in either language, and no Content Security Policy violation is reported (`connection-checks.spec.ts`)."
verify: "make ci e2e"
operator_attention: true
issue: 265
---

# S-087. Check a Connection before saving it, for Mattermost, Telegram and Bridge Connections (BE)

## Scope

**IN**

- The operation `checkConnectionDraft`: the check of a draft Connection of any type — the type, the address, the token
  typed in the form, the proxy and, for a Bridge, the event delivery mode — without saving it (C-13.FR-14, D353).
- The token rule of C-13.FR-14: a token typed in the request may go to the address typed in the request; a stored
  token goes only to the saved address through the saved proxy, and with a changed address or proxy and no token typed
  only the dry probe runs.
- A dry probe for Mattermost (`GET /api/v4/system/ping`) and for Bridges (`GET /v1/info` without the token), beside the
  Telegram dry probe of S-041.
- "Check connection" in the create form of every type and in the edit form while it holds unsaved changes.
- The fake Mattermost server's ping; the messenger pages of the documentation; the fact the Mattermost dry probe relies
  on.

**OUT**

- Recording the requests of a draft check in the Request log, marked as draft checks, and the check run its result
  names (S-089, C-23.FR-1, C-23.FR-5); "Requests of this check" in the check panel (S-090).
- The check of a saved Connection (`checkConnection`), unchanged except that it is shown from the edit form when the
  form holds no changes (S-039, S-040, S-041, S-043, S-073, S-077).

## Contracts

- **Spec** (`api/openapi.yaml`, changed here first, then generated):
  - New operation `checkConnectionDraft`, `POST /connections/checks`, tag Connections, `x-permission:
    connections:write` (it calls out with a bot token, as `checkConnection` and `listConnectionChannels` do), session
    with CSRF or an API token, interactive. Body `ConnectionDraft`, a `oneOf` with the discriminator `type`:
    `MattermostConnectionDraft` (`server_url`), `TelegramConnectionDraft` (`bot_api_base_url`, `update_mode`) and
    `BridgeConnectionDraft` (`base_url`, `event_delivery`), each with an optional `bot_token` (write-only), an optional
    `proxy` (`ProxyConfigInput`; omitted is no proxy for a new Connection and the saved proxy for a saved one) and an
    optional `connection_id` (`PublicId`, the saved Connection the edit form holds, whose stored token and proxy
    password the rule may use). Answers `200` `ConnectionCheckResult` — `ok` false when a step failed —, `400`, `401`,
    `403`, `422`, `429` and `503` (`Limited`).
  - `NullableConnectionCheckCode` gains `not_mattermost` (the dry probe of a Mattermost Connection: the address
    answered, but not as a Mattermost server) and `not_bridge` (the dry probe of a Bridge: the address answered, but not
    with the protocol's problem); its description names the dry probe of each type. `ConnectionCheckStep.name` keeps its
    values: every type's dry probe is `dry_probe`.
  - The description of `checkConnection` and of `ConnectionCheckRequest.base_url` drop "the token is never sent anywhere
    but the saved base URL" for the rule of C-13.FR-14; `base_url` stays for API clients, and the SPA no longer sends
    it.
- **The draft check** (`internal/connections/draft.go`): `Service.CheckDraft` validates the draft as a save would —
  the address of the type, the proxy — then decides the token and the proxy password:
  1. a `bot_token` in the draft is used against the draft's address;
  2. without one, and with a `connection_id`, the stored token is used when the draft's address equals the saved one
     after the normalization a save applies (the address as `checkAddress` normalizes it: white space trimmed, and a
     trailing `/` dropped from a Telegram or Bridge base URL) and the draft's proxy has the saved `enabled`, `type` and
     `address` — two disabled proxies count as the same;
  3. otherwise there is no token: the check runs the type's dry probe and reports its other steps skipped.

  The stored proxy password is used only when the draft's proxy has the saved type and address and the draft carries no
  `password`; a `password` in the draft is used as typed; `null` means none. It builds clients for the draft that are
  never cached and never kept, runs the steps of the type — Mattermost `token` with the roles read of its warning
  (C-13.FR-2), Telegram through `telegram.RunCheck` (C-14.FR-11), Bridge `info` and `event_delivery` (C-22.FR-2) —
  and writes nothing: no `SetBotIdentity`, no stored `info`, no hint, no Audit log entry. A check changes no
  configuration, and the Audit log records changes of configuration and Commands; what a draft check sent is recorded
  in the Request log from S-089.
- **Dry probes** (`internal/mattermost/check.go`, `client.go`; `internal/bridge/check.go`, `client.go`): a request to
  the draft's address through the draft's proxy with no `Authorization` header, on a client built without a token.
  Mattermost: `GET /api/v4/system/ping` — `200` JSON with `status` `OK` passes; `404` is `wrong_path_prefix`; an
  answer that is not JSON is `not_mattermost` with its status and content type named, and so is a `200` whose `status`
  is not `OK`; another status with JSON is `unexpected_status`. Bridge: `GET /v1/info` — `404` is `wrong_path_prefix`,
  checked before the problem, so that a reverse proxy's page still names the prefix; an answer that is not a problem is
  `not_bridge` with its status and content type named; `401` with `application/problem+json` passes; another status
  with a problem is `unexpected_status`. A request without an answer, or one the outbound address policy refuses,
  takes the code of `delivery.FailureCode`, as every other step does; a `407` of the proxy is `proxy_refused`. A
  failing dry probe never leads to a step with a token. `telegram.RunCheck` takes the step path of a skipped step from
  the probe's client, so that a draft without any token needs no client with one.
- **Interactive path** (`internal/delivery/interactive.go`): a draft that names a saved Connection takes its token from
  that Connection's limiter bucket; a draft of a new Connection has no bucket and takes no token — a `Subject` that
  says so explicitly, never an empty one, which stays an error. It waits for no token, so it never waits past
  `delivery.interactive_budget`; each request is bounded by its client's call timeout, and the caller by
  `api.rate_limit`. A draft never holds a limiter: a `RetryAfter` answered to it reaches the interactive path as
  `Transient`, so that an address that is not saved never pauses the deliveries of the Connection whose bucket the
  draft takes its token from; the step keeps its failure.
- **Refusals**: a `connection_id` that names no Connection, a deleted one or one whose type differs from the draft's is
  `422` `unknown_id` at `/connection_id`; an address that a save would refuse is `422` `invalid_format` at its pointer
  (`/server_url`, `/bot_api_base_url`, `/base_url`); a proxy that a save would refuse, its errors at `/proxy/...`.
- **SPA** (`connection-form.tsx`, `connection-check.tsx`, the two routes): the create form of every type shows the check
  panel with "Check connection", enabled once the address is filled in; it sends the form's values through the
  generated `useCheckConnectionDraft`. On a saved Connection the panel checks the saved Connection with
  `checkConnection` while the form holds no changes, and the draft with `connection_id` while it does; a changed address
  or proxy with no token typed shows the dry probe and, on the skipped steps, "Skipped: type the bot token again to
  check the new address, or save first" («Пропущено: введите токен бота заново, чтобы проверить новый адрес, или
  сохраните изменения»); a new Connection's draft without a token says "Skipped: type the bot token to check it"
  («Пропущено: введите токен бота, чтобы проверить его»); after a passed dry probe the summary names the type ("The
  address answers as a Mattermost server.", "… as a Bot API.", "… as a bridge."); a failed dry probe keeps "Skipped:
  an earlier step failed"; a change of the form makes an earlier result stale and clears it. The texts of the codes
  `not_mattermost` ("The address answers, but not as a Mattermost server.") and `not_bridge` ("The address answers,
  but not as a bridge: a bridge answers a request without its token with 401 and a problem.") join `checkCodeText`,
  and the text of `unexpected_status` becomes one for every type ("The address answered with an unexpected status.");
  the hints of the three types say that the form's values are checked before saving. The Telegram panel's
  `unsavedBaseUrl` path is replaced by the draft check.
- **Fake Mattermost server** (`fakemattermost.go`): `GET /api/v4/system/ping` answers `200` `{"status":"OK"}`
  without a token, as a Mattermost server does; every other API path still needs one.
- **Secrets** (lint 5): the probe `connection_drafts` runs a draft that changes the address and one that changes the
  proxy of a saved Connection, against fakes that record their requests, and fails when the stored token appears in a
  request, a log line or a returned error.
- **Documentation** (`docs/messengers/mattermost.md`, `telegram.md`, `bridge.md`): "Check before saving" — what each
  check does with the form's values, and that a stored token is sent only to the saved address, so a changed address
  needs the token typed again for the full check.
- **Fact** (`design/facts.md`, `C-13-mattermost.md`): the Mattermost entry of Pending becomes a fact, `F-069`, once the
  maintainer has seen the unauthenticated ping on the test stand (Notes), and C-13.FR-2 cites it.
- **Defaults**: `delivery.interactive_budget`, `api.rate_limit`.

## Steps

1. Add the operation, the draft schemas and the codes to the spec; generate. Check: `make generate`, `make lint`.
2. Add the dry probes of Mattermost and Bridges, the ping of the fake Mattermost server and the probe client of the
   Telegram check without a token. Check: `check_test.go` of the three packages, `fakemattermost_test.go`.
3. Add the draft check with the token rule and the interactive call without a bucket, then the handler. Check:
   `draft_test.go`, `limiter_test.go`, `internal/api/connections_test.go`.
4. Add the secret probe and the end-to-end test. Check: `make lint-arch`, `connection_drafts_test.go`.
5. Add the panel to the forms, the texts and the Playwright tests; update the documentation pages. Check:
   `make test-web`, `make e2e-web`, then Verification.
6. Hand the ping fact to the maintainer (Notes). Check: `F-069` in `design/facts.md`.

## Verification

```sh
make dev > dev.log 2>&1 &
# the Admin's session (`jar`, `H`) as in S-011
API=localhost:8080/api/v1
before=$(curl -s -b jar "$API/connections?limit=500" | jq '.items | length')
audits=$(curl -s -b jar "$API/audit-log?limit=500" | jq '.items | length')

# C-13.FR-14: drafts of new Connections of each type; nothing is saved
curl -s "${H[@]}" $API/connections/checks -d '{"type":"mattermost","server_url":"http://127.0.0.1:18066",
  "bot_token":"muster-dev-mattermost-token-b"}' | jq -c '{ok, s: [.steps[] | {name, ok}], bot_name}'
# {"ok":true,"s":[{"name":"token","ok":true}],"bot_name":"muster-dev-bot-b"}
curl -s "${H[@]}" $API/connections/checks -d '{"type":"telegram","bot_api_base_url":"http://127.0.0.1:18081",
  "update_mode":"long_polling","bot_token":"234567:dev-telegram-token-b"}' | jq -c '[.steps[] | {name, ok}]'
# [{"name":"dry_probe","ok":true},{"name":"get_me","ok":true},{"name":"get_webhook_info","ok":true}]
curl -s "${H[@]}" $API/connections/checks -d '{"type":"bridge","base_url":"http://127.0.0.1:18094","event_delivery":"poll",
  "bot_token":"muster-dev-bridge-token-b"}' | jq -c '{ok, s: [.steps[].name], m: .info.messenger.name}'
# {"ok":true,"s":["info","event_delivery"],"m":"Fake chat"}
curl -s -b jar "$API/connections?limit=500" | jq --argjson b "$before" '(.items | length) == $b'        # true
curl -s -b jar "$API/audit-log?limit=500" | jq --argjson a "$audits" '(.items | length) == $a'           # true

# C-13.FR-2, C-13.AC-17: an edit draft of "Dev Mattermost" with only the server URL changed — the dry probe only
MM=$(curl -s -b jar "$API/connections?type=mattermost" | jq -r '.items[] | select(.name=="Dev Mattermost") | .id')
curl -s -X DELETE 127.0.0.1:18066/_fake/requests
curl -s "${H[@]}" $API/connections/checks -d "{\"type\":\"mattermost\",\"connection_id\":\"$MM\",
  \"server_url\":\"http://127.0.0.1:18066\"}" | jq -c '[.steps[] | {name, ok, skipped}]'
# [{"name":"dry_probe","ok":true,"skipped":false},{"name":"token","ok":false,"skipped":true}]
curl -s 127.0.0.1:18066/_fake/requests | jq -c '[.[] | {path, auth: ((.headers.Authorization // []) | length)}]'
# [{"path":"/api/v4/system/ping","auth":0}]

# C-22.FR-2, C-22.AC-26: an edit draft of "Dev Bridge" pointed at an address that is not a bridge
BR=$(curl -s -b jar "$API/connections?type=bridge" | jq -r '.items[] | select(.name=="Dev Bridge") | .id')
curl -s "${H[@]}" $API/connections/checks -d "{\"type\":\"bridge\",\"connection_id\":\"$BR\",
  \"base_url\":\"http://127.0.0.1:18065\",\"event_delivery\":\"poll\"}" | jq -c '[.steps[] | {name, code, skipped}]'
# [{"name":"dry_probe","code":"not_bridge","skipped":false},{"name":"info","code":null,"skipped":true},
#  {"name":"event_delivery","code":null,"skipped":true}]
curl -s 127.0.0.1:18065/_fake/requests | jq '[.[] | select(.headers.Authorization != null and
  (.headers.Authorization[0] | contains("muster-dev-bridge-token")))] | length'                      # 0

# C-13.FR-14: an unknown connection_id
curl -s "${H[@]}" $API/connections/checks -d '{"type":"bridge","connection_id":"CN0000000000AA",
  "base_url":"http://127.0.0.1:18094","event_delivery":"poll"}' | jq -c '[.status, .errors[0].pointer, .errors[0].code]'
# [422,"/connection_id","unknown_id"]
grep -c 'muster-dev-mattermost-token\|muster-dev-bridge-token\|dev-telegram-token' dev.log                 # 0
```

In the browser (`connection-checks.spec.ts`, at 1280 px and 360 px, English and Russian): Connections → New →
Mattermost, the second fake server and its token, "Check connection" shows "Connected as muster-dev-bot-b" and the
Connections list still has its earlier rows; the same for Telegram and Bridge with their steps; on the page of "Dev
Telegram", a base URL with another path prefix shows the failed dry probe and the other steps skipped, and the fake
Telegram server records no request with the stored token under that prefix; with only the proxy changed to the
development HTTP proxy and no token, the dry probe passes through the proxy and `getMe` and `getWebhookInfo` show
"Skipped: type the bot token again to check the new address, or save first". A fake server records no host, so another
address of the same fake cannot be told apart from the saved one. Screenshots of both widths go into the pull request.

## Open questions

None. The Mattermost dry probe relies on Mattermost's public ping, seen on the test stand on 2026-10-18 (F-069).

## Notes

- Suggested commit: `feat(connections): check a connection before saving it`.
- **Maintainer** (operator attention): the maintainer saw the ping without a token answer `200` with `"status":"OK"`
  on both Mattermost servers of the test stand on 2026-10-18; the entry moved from Pending to `F-069` with the date,
  the version and C-13.FR-2, in this pull request.
- One pull request for the operation and the forms (D353): the backend half alone has nothing a person can use, and
  together they touch 40 files, at the edge of the size signal. The story is `be` because the format has one kind per
  story, as S-070 and S-073 carry parts of the other half.
- A draft check stores nothing on purpose: the bot name and `info` of a saved Connection come only from checks of what
  is saved, so the Connection page never shows the bot of an address that was never saved.
- The rule covers the proxy as well as the address, a refinement of D353: through an unsaved HTTP proxy an `http`
  target's token would reach the proxy, so a changed proxy without a typed token gets the dry probe too. The pull
  request lists it for the maintainer.
- The draft check is not a way around the outbound address policy: the draft's address and proxy are checked by it
  like every target (ADR-0015).

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-13.FR-2 | partial | the Mattermost dry probe; the check itself in S-039, S-040, S-061, S-064 |
| C-13.FR-14 | full | |
| C-13.AC-17 | full | |
| C-14.FR-11 | partial | the typed token of a draft; the steps in S-041, S-043 |
| C-14.AC-6 | partial | with no token typed again; the saved check in S-041, S-043 |
| C-14.AC-20 | full | |
| C-22.FR-1 | partial | the token rule; the Connection in S-073, S-077 |
| C-22.FR-2 | partial | the Bridge dry probe and the draft check; the check in S-073, S-074, S-077 |
| C-22.AC-26 | full | |
