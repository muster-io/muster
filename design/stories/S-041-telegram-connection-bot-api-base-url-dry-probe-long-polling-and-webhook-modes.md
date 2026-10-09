---
id: S-041
title: "Telegram Connection: Bot API base URL, dry probe, long polling and webhook modes (BE)"
capability: C-14
kind: be
layer: L1
depends_on: [S-061]
covers: [C-14.FR-1, C-14.FR-8, C-14.FR-10, C-14.FR-11, C-14.FR-12, C-14.AC-3, C-14.AC-5, C-14.AC-6, C-14.AC-7, C-14.AC-10, C-14.AC-11, C-02.FR-14, C-01.FR-13, C-02.FR-10]
files_touched:
  - api/openapi.yaml
  - internal/connections/connections.go
  - internal/connections/telegram.go
  - internal/connections/doctor.go
  - internal/connections/query.sql
  - internal/connections/connections_test.go
  - internal/connections/telegram_test.go
  - internal/connections/doctor_test.go
  - internal/connections/live_test.go
  - internal/outbound/client.go
  - internal/outbound/proxy_test.go
  - internal/telegram/client.go
  - internal/telegram/baseurl.go
  - internal/telegram/check.go
  - internal/telegram/poller.go
  - internal/telegram/webhook.go
  - internal/telegram/updates.go
  - internal/telegram/client_test.go
  - internal/telegram/baseurl_test.go
  - internal/telegram/check_test.go
  - internal/telegram/poller_test.go
  - internal/telegram/webhook_test.go
  - internal/api/connections.go
  - internal/api/connections_test.go
  - internal/leader/tasks.go
  - internal/leader/leader_test.go
  - internal/doctor/doctor.go
  - internal/doctor/doctor_test.go
  - internal/fakes/faketelegram/faketelegram.go
  - internal/fakes/faketelegram/updates.go
  - internal/fakes/faketelegram/faketelegram_test.go
  - internal/devmode/devmode.go
  - internal/runtime/runtime.go
  - internal/logging/events.go
  - internal/archlint/secretleak.go
  - test/e2e/telegram_connection_test.go
  - test/e2e/smoke_test.go
acceptance:
  - "[C-14.FR-1] A Telegram Connection is created with a name, a write-only bot token, the Bot API base URL (`connection.telegram.bot_api_base_url`), a proxy, its limiter (`connection.telegram.limiter`) and the update mode (`connection.telegram.update_mode`); deleting one that Destinations use answers 409 `in_use`."
  - "[C-14.FR-10] The base URL must be an absolute `http` or `https` URL without query, fragment or user information; a path prefix is kept and a trailing `/` normalized, so requests go to `<base>/bot<token>/<method>`; an `http` base URL is saved with the warning `base_url_uses_http`."
  - "[C-14.FR-11, C-14.AC-5] The check runs three steps with latency and path: the dry probe `GET <base>/bot0:x/getMe` without the token passes on a `401` JSON answer and against a server answering HTML fails with \"This is not a Bot API.\"; `getMe` returns the bot's username; `getWebhookInfo` reports whether a webhook is set and the pending updates; neither dry probe carries the real token, and a failed dry probe skips the steps that would."
  - "[C-14.FR-11, C-14.AC-6] With an unsaved `base_url` in the check request only the dry probe runs, against that address, the other steps are `skipped`, and no request with the real token reaches it."
  - "[C-14.AC-10] With a base URL with the path prefix `/k3x9/`, every request reaches the fake server at `/k3x9/bot<token>/<method>`; with a SOCKS5 proxy on the Connection, only through the fake proxy."
  - "[C-14.FR-1, C-14.AC-3] In long-polling mode only the Leader polls `getUpdates` with an explicit `allowed_updates` and resumes at the stored offset after a Leader change; a `409 Conflict` from a second poller makes it back off with `telegram_poll_conflict` and nothing becomes Broken or counted against the Connection."
  - "[C-14.FR-1, C-14.AC-11] Switching a Connection to webhook mode sets the webhook at `MUSTER_INGEST_URL/api/v1/callbacks/telegram/<connection>` with a generated secret token; a request to that endpoint without the secret token header, with a wrong one, or for an unknown Connection is answered 401 and changes nothing; with the right header the update is accepted and routed."
  - "[C-14.FR-12, C-14.AC-7] A network error on a request to the Bot API leaves the token in no log line and in no error that the API returns; log lines show only the scheme and host of the base URL."
  - "[C-14.FR-8] Private messages to the bot reach the update router, which hands them to the Account link handler of S-051 (dropped with a log line until then)."
  - "[C-02.FR-14] `muster doctor` prints one line per Telegram Connection with the result of its check."
verify: "make ci test-integration e2e"
operator_attention: false
issue: 41
---

# S-041. Telegram Connection: Bot API base URL, dry probe, long polling and webhook modes (BE)

## Scope

**IN**

- The Telegram type of the Connection API: the bot token, the Bot API base URL with its rules and warning, the proxy,
  the limiter and the update mode.
- The Bot API client: URLs with the token in the path, the `ok: false` classification, token redaction.
- The step-by-step connection check with the dry probe.
- Receiving updates: long polling on the Leader with a stored offset and `409` back-off, or the webhook endpoint with
  its secret token; the update router.
- `muster doctor` for Telegram Connections; the fake Telegram server's Bot API surface for all of it.

**OUT**

- Telegram Destinations, the adapter, presses and the copy buffer (S-042); `/start` for Account links (S-051); the
  pages (S-043); "Telegram in restricted networks" in the documentation (S-058).

## Contracts

- **Operations implemented**: `createConnection`, `updateConnection`, `getConnection`, `listConnections`,
  `deleteConnection` and `checkConnection` for the type `telegram` (the `422 unsupported` of S-039 is removed);
  `telegramWebhook` (ingest listener, `telegramSecretToken`). Schemas: `TelegramConnection(Base, Input)`,
  `TelegramUpdateMode`, `TelegramUpdate`, `ConnectionCheckRequest` (`base_url`), `ConnectionCheckStep` (`dry_probe`,
  `get_me`, `get_webhook_info`), `ConnectionCheckResult` (`webhook_set`, `pending_updates`).
- **Connection fields** (C-14.FR-1, FR-10; `connections`): `bot_api_base_url` (default
  `connection.telegram.bot_api_base_url`), validated as an absolute `http(s)` URL without query, fragment or user
  information (`422 invalid_format` at `/bot_api_base_url`), stored without the trailing `/`; `update_mode`
  (`long_polling` or `webhook`); `limiter` (default `connection.telegram.limiter`); `warnings` `base_url_uses_http` for
  an `http` base URL; `bot_username` from the last successful `getMe`. Deleting follows S-039 (`409 in_use`).
- **Bot API client** (`client.go`): `<base>/bot<token>/<method>` through `internal/outbound` with the Connection's
  proxy and the outbound address policy; the token is registered for redaction (C-14.FR-12, ADR-0015), so every error
  and log line carries `bot[redacted]`, and the client logs the base URL as scheme and host only. A `200` with
  `ok: false` is classified by `error_code` and `description` like a status; `retry_after` in `parameters` is the exact
  delay; a non-JSON answer is `transient`; `409` is `unknown` and never retried, so that the poller sees it; a `429` to
  the dry probe is `transient`, never a RetryAfter that would hold the Connection's limiter; an update whose known
  parts are not the expected JSON is kept by its `update_id` and routed as `other`, so that it never stalls the
  Connection. S-042 adds
  the delivery methods and their mapping. To name what failed when no answer came (DNS, TLS, timeout, a proxy that
  refused the credentials), `outbound.Error` carries `Network` and its redacted `Detail`.
- **Connection check** (C-14.FR-11; `check.go`, interactive path and class, the Connection's limiter): steps with
  `latency_ms` and `via` (`direct` or `proxy`):
  1. `dry_probe` — `GET <base>/bot0:x/getMe` without the token; passes on a `401` answer with a JSON body; otherwise
     the message names what answered: "DNS lookup failed: {host}", "TLS handshake failed: {error}", "No answer within
     {n} s", "The proxy refused the connection (407)", "This is not a Bot API." (a non-JSON answer), "Wrong path prefix:
     the server answered 404." (a `404`);
  2. `get_me` — with the token; `bot_name` is the bot's username, stored with `bot_user_id`;
  3. `get_webhook_info` — `webhook_set` (and its URL's host in the message) and `pending_updates`.
  Steps 2 and 3 run only against the saved base URL; with `base_url` in the request, step 1 runs against that address
  and steps 2 and 3 are `skipped`. A dry probe that fails skips steps 2 and 3 too, so that the token never goes to a
  server that is not a Bot API, and a `getMe` that fails skips step 3. No check, metric or background task sends the
  token to another address. A new base URL on an update needs the bot token again, as a new server URL does for
  Mattermost (S-039), so that the stored token never reaches another server.
- **Update mode** (C-14.FR-1; `poller.go`, `webhook.go`):
  - `long_polling`: a Leader task per Telegram Connection that is not deleted (C-02.FR-10, `internal/leader/tasks.go`)
    calls `getUpdates` with `offset` from `connections.telegram_update_offset`, a long-poll `timeout` and
    `allowed_updates` `["message", "edited_message", "channel_post", "callback_query", "my_chat_member"]`, in the
    background client class; the offset is stored after the updates are handed to the router, so a new Leader resumes
    where the old one stopped. A `409 Conflict` backs off with jitter and logs `telegram_poll_conflict` (INFO); it is
    never a delivery outcome and never makes anything Broken. The task runs per Organization: it iterates over the
    Organizations (one in L1) and passes `org_id` to every query (lint 1).
  - `webhook`: saving the mode generates a random secret token (stored like a Secret in
    `telegram_webhook_secret_*`) and calls `setWebhook` with `url` = `MUSTER_INGEST_URL/api/v1/callbacks/telegram/
    <connection public_id>`, `secret_token`, the same `allowed_updates` and `max_connections` 1, so that updates
    arrive in order; switching back to `long_polling` calls `deleteWebhook` with the bot and base URL saved before the
    switch. A new base URL or bot token in the webhook mode sets the webhook again with a new secret token. A new bot
    token forgets `telegram_update_offset`, whose ids belong to the old bot. `telegram_webhook_set` is logged once the
    save committed. The calls run in the interactive client class inside the
    save's transaction, before it commits; their failures answer `422` `webhook_call_failed` at `/update_mode` with the
    step's message, and nothing is saved. If the commit fails after `setWebhook` succeeded, the poller logs
    `telegram_poll_conflict` until the next save; Muster never calls `deleteWebhook` on its own, which would break
    another installation's webhook.
- **Webhook endpoint** (C-14.AC-11; `telegramWebhook`, in `webhook.go`, mounted on the ingest listener's callback mux
  beside the Mattermost callback and, like it, outside the generated server): `401` unless the Connection exists, is in
  `webhook` mode and the header `X-Telegram-Bot-Api-Secret-Token` equals its secret (constant-time comparison);
  otherwise `200` after the update was handed to the router; a router that failed answers `500` and logs
  `telegram_update_failed`, and Telegram sends the update again.
- **Update router** (`updates.go`): one entry for both modes; ignores an `update_id` it has already handled for the
  Connection: it holds the Connection's `telegram_update_offset` row locked while the update is handled and raises the
  offset after it in the same transaction, so that a second poller of a frozen old Leader, or a webhook request with
  the same update, waits and then skips it; a handler's error stores nothing, so the update comes again. A handler
  therefore runs under the row lock: it must be short, must not lock that row itself and must not wait for a save of
  the Connection (S-042, S-051). A poll that brings no update waits 1 s before the next. It hands
  `callback_query` and the `message` and `edited_message` updates of channels and groups (and `channel_post`) to the
  handler that S-042 registers, and private messages (`/start <token>`, C-14.FR-8) to the handler that S-051 registers;
  without a handler it logs `telegram_update_dropped` (INFO: `connection`, `kind`: `callback_query`, `chat_message`,
  `private_message`, `my_chat_member` or `other`).
- **`muster doctor`** (C-02.FR-14): one line per Telegram Connection — `connection <name>: ok` or the failing step with
  its message.
- **Fake Telegram** (C-01.FR-13; `127.0.0.1:18081`), extending S-004:
  - `getMe` for any token but those revoked through `PUT /_fake/config` (`401`), and always `401`
    `{"ok":false,"error_code":401,"description":"Unauthorized"}` for `bot0:x`; the bot is `muster_dev_bot`
    (`id` 123456);
  - `PUT /_fake/config` `{"path_prefix": "/k3x9", "mode": "html"}`: with a prefix the fake answers only under it and
    `404` elsewhere; in `html` mode every answer is an HTML page with `200`;
  - `getUpdates` with `offset`, `timeout` and `allowed_updates` (recorded); a second concurrent `getUpdates` on the
    same token ends the first with `409 Conflict: terminated by other getUpdates request` (F-018); with a webhook
    set, `getUpdates` answers `409`;
  - `setWebhook`, `deleteWebhook`, `getWebhookInfo` (`url`, `pending_update_count`); with a webhook set, each update is
    posted to its URL with the header `X-Telegram-Bot-Api-Secret-Token`; `setWebhook` ends a running long poll with
    `409 Conflict: terminated by setWebhook request`, and `GET /_fake/webhooks` lists the webhooks set;
  - `POST /_fake/updates` `{token, update}` enqueues a raw update for a bot, delivered through `getUpdates` or the
    webhook; `POST /_fake/conflict` `{token}` ends that bot's running long poll with `409`, or the next one when none
    runs;
  - the harness's `requests`, with each request's path and `at_ms`, and `faults`.
- **Development mode**: `muster dev` adds a demo Telegram Connection "Dev Telegram" to the fake server with the token
  `123456:dev-telegram-token`, in long-polling mode, without Destinations.
- **Secrets** (lint 5): the bot token and the webhook secret token are pushed through the client, the check and the
  poller by a probe, including the error of a refused connection.
- **Log events**: `telegram_poll_conflict` (INFO: `connection`, `backoff_ms`), `telegram_poll_failed` (WARN:
  `connection`, `error`), `telegram_update_dropped` (INFO: `connection`, `kind`), `telegram_update_failed` (WARN:
  `connection`, `error`; the webhook endpoint answered `500`), `telegram_webhook_set` (INFO: `connection`, `host`).
- **Problem code**: `webhook_call_failed` (`422` at `/update_mode`), added to `x-problem-codes`.
- **Defaults**: `connection.telegram.bot_api_base_url`, `connection.telegram.update_mode`,
  `connection.telegram.limiter` (P-28, measured in the test environment).

## Steps

1. Extend the fake server. Check: `faketelegram_test.go` covers the dry probe, the prefix, HTML mode, the `409` of a
   second poller and webhook delivery.
2. Write the base URL rules and the client with redaction and `ok: false` classification. Check: `baseurl_test.go` and
   `client_test.go`, including a refused connection whose error carries `bot[redacted]`.
3. Write the Telegram Connection fields and the step-by-step check. Check: `check_test.go` covers each failure message
   and the unsaved base URL.
4. Write the poller, the webhook mode and endpoint, and the router. Check: `poller_test.go` covers the offset across a
   Leader change and the `409` back-off.
5. Add doctor, the secret probe, the demo Connection and the end-to-end test. Check: Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
# the Admin's session (`jar`, `H`) as in S-011
# each Connection below polls its own token: the demo Connection already polls 123456:dev-telegram-token
API=localhost:8080/api/v1; FTG=127.0.0.1:18081/_fake; TOKEN=777001:verify-token
TG() { curl -s "${H[@]}" $API/connections -d "{\"type\":\"telegram\",\"name\":\"$1\",\"bot_token\":\"$TOKEN\",\"bot_api_base_url\":\"$2\",
  \"update_mode\":\"long_polling\",\"proxy\":$3,\"limiter\":{\"limit\":15,\"per_seconds\":1}}"; }
CHECK() { curl -s "${H[@]}" -X POST $API/connections/$1/checks -d "${2:-{\}}" | jq -c '{ok, s: [.steps[] | {name, ok, skipped, via, message}]}'; }

# C-14.FR-10: base URL rules and the http warning
TG bad 'https://user:pw@api.example.org/?x=1' '{"enabled":false}' | jq -c '[.status, .errors[0].pointer]'   # [422,"/bot_api_base_url"]
T=$(TG tg 'http://127.0.0.1:18081/' '{"enabled":false}')
jq -c '{b: .bot_api_base_url, w: .warnings}' <<<"$T"                         # {"b":"http://127.0.0.1:18081","w":["base_url_uses_http"]}
T=$(jq -r .id <<<"$T")

# C-14.FR-11, AC-5: the three steps, and a server that is not a Bot API
CHECK $T
# {"ok":true,"s":[{"name":"dry_probe","ok":true,"via":"direct",…},{"name":"get_me","ok":true,…},{"name":"get_webhook_info","ok":true,…}]}
curl -s "${H[@]}" -X POST $API/connections/$T/checks | jq -c '{bot_name, webhook_set, pending_updates}'
# {"bot_name":"muster_dev_bot","webhook_set":false,"pending_updates":0}
curl -s -X PUT $FTG/config -d '{"mode":"html"}' > /dev/null
CHECK $T | jq -c '.s[0] | {ok, message}'                                     # {"ok":false,"message":"This is not a Bot API."}
curl -s -X PUT $FTG/config -d '{"mode":"bot_api"}' > /dev/null
curl -s $FTG/requests | jq -r '[.[] | select(.path | endswith("/getMe")) | .path | select(test("bot(0:x|777001)"))] | unique | .[]'
# /bot0:x/getMe
# /bot777001:verify-token/getMe                    (only the steps against the saved base URL carry the token)

# C-14.AC-6: an unsaved base URL runs only the dry probe there
curl -s -X DELETE $FTG/requests
CHECK $T '{"base_url":"http://127.0.0.1:18081/other/"}' | jq -c '[.s[] | {name, ok, skipped}]'
# [{"name":"dry_probe","ok":false,"skipped":false},{"name":"get_me","ok":false,"skipped":true},{"name":"get_webhook_info","ok":false,"skipped":true}]
curl -s $FTG/requests | jq -r '[.[].path] | unique | .[]'                    # /other/bot0:x/getMe

# C-14.AC-10: a path prefix, and a SOCKS5 proxy
curl -s -X PUT $FTG/config -d '{"path_prefix":"/k3x9"}' > /dev/null; curl -s -X DELETE $FTG/requests
P=$(TOKEN=777002:prefix-token TG pref 'http://127.0.0.1:18081/k3x9/' '{"enabled":true,"type":"socks5","address":"127.0.0.1:18092"}' | jq -r .id)
CHECK $P | jq -r '.s[1].via'                                                # proxy
curl -s $FTG/requests | jq -r '[.[].path | select(test("777002")) | sub("bot[^/]+/"; "bot…/")] | unique | .[]'
# /k3x9/bot…/getMe
# /k3x9/bot…/getUpdates
# /k3x9/bot…/getWebhookInfo
curl -s 127.0.0.1:18092/_fake/requests | jq -c '[.[].target] | unique'       # ["127.0.0.1:18081"]
curl -s -X PUT $FTG/config -d '{"path_prefix":""}' > /dev/null

# C-14.AC-3: a second poller interrupts; Muster backs off and nothing is Broken
curl -s -X POST $FTG/conflict -d "{\"token\":\"$TOKEN\"}" > /dev/null; sleep 2
grep -c '"event":"telegram_poll_conflict"' dev.log                          # 1 or more
curl -s -b jar "$API/destinations?health=broken" | jq '.items | length'      # 0

# C-14.FR-1, AC-11: webhook mode and its secret token header
ET=$(curl -s -b jar $API/connections/$T | jq -r .etag)
curl -s "${H[@]}" -X PUT -H "If-Match: $ET" $API/connections/$T -d '{"type":"telegram","name":"tg","bot_api_base_url":"http://127.0.0.1:18081",
  "update_mode":"webhook","proxy":{"enabled":false},"limiter":{"limit":15,"per_seconds":1}}' | jq -r .update_mode   # webhook
curl -s "127.0.0.1:18081/bot$TOKEN/getWebhookInfo" | jq -r '.result.url'             # http://localhost:8081/api/v1/callbacks/telegram/CN…
U='{"update_id":9001,"message":{"message_id":1,"chat":{"id":42,"type":"private"},"text":"hello"}}'
curl -s -o /dev/null -w '%{http_code}\n' -X POST localhost:8081/api/v1/callbacks/telegram/$T -d "$U"                                   # 401
curl -s -o /dev/null -w '%{http_code}\n' -X POST -H 'X-Telegram-Bot-Api-Secret-Token: wrong' localhost:8081/api/v1/callbacks/telegram/$T -d "$U"   # 401
curl -s -o /dev/null -w '%{http_code}\n' -X POST -H 'X-Telegram-Bot-Api-Secret-Token: x' localhost:8081/api/v1/callbacks/telegram/CN000000000000 -d "$U"   # 401
curl -s -X POST $FTG/updates -d "{\"token\":\"$TOKEN\",\"update\":$U}" > /dev/null; sleep 1   # the fake posts it with the right header
grep -c '"event":"telegram_update_dropped".*"kind":"private_message"' dev.log  # 1   (the /start handler arrives with S-051)

# C-14.AC-7: a network error leaves the token nowhere
N=$(TOKEN=777003:down-token TG down 'http://127.0.0.1:1/' '{"enabled":false}' | jq -r .id)
curl -s "${H[@]}" -X POST $API/connections/$N/checks | jq -r '.steps[0].message'   # No answer … (or the refused connection, without the token)
curl -s "${H[@]}" -X POST $API/connections/$N/checks | grep -c "down-token"   # 0
sleep 5; grep -c "down-token" dev.log                                        # 0   (the poller's failures included)

# C-02.FR-14
./bin/muster dev doctor | grep 'connection tg:'                              # OK   connection tg: ok
```

**Optional manual check against a real server** (the operator's test bot): create a Connection with the real token and
the default base URL, run the check and see the bot's username; set a webhook from another tool and see
`webhook_set: true` and the `409` from polling; then, through an HTTPS reverse proxy with a path prefix and through a
SOCKS5 proxy, see the dry probe pass and every request take the configured path (L1 open question 8 of the test
environment).

## Open questions

None.

## Notes

- Suggested commit: `feat(telegram): add telegram connections with the dry probe, long polling and webhooks`.
- The fake's HTML mode stands for any web server that is not a Bot API, such as a reverse proxy pointed at the wrong
  upstream.
- The Verification block is written for zsh: in bash, `"${2:-{\}}"` keeps the backslash and the check body is not JSON.
- Telegram's cloud Bot API accepts only `https` webhook URLs on the ports 443, 80, 88 and 8443; a `MUSTER_INGEST_URL`
  it cannot reach makes `setWebhook` fail with Telegram's description, answered as `webhook_call_failed`. A self-hosted
  Bot API server in local mode accepts `http`.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-14.FR-1 | partial | the API, both update modes and `409`; the page is S-043 |
| C-14.FR-8 | partial | private messages reach the router; the `/start` handler is S-051 |
| C-14.FR-10 | partial | the rules and the warning in the API; the hint and the warning on the page are S-043 |
| C-14.FR-11 | partial | the API; the step-by-step view is S-043 |
| C-14.FR-12 | partial | the client and the check; the adapter's errors are S-042 |
| C-14.AC-3 | full | |
| C-14.AC-5 | full | |
| C-14.AC-6 | partial | the API; the form's unsaved address is S-043 |
| C-14.AC-7 | partial | the check and the logs; the delivery errors are S-042, the page S-043 |
| C-14.AC-10 | full | the delivery requests of S-042 use the same client |
| C-14.AC-11 | partial | the secret token header; processing a press is S-042 |
| C-02.FR-14 | partial | Telegram Connection checks in `muster doctor` |
| C-01.FR-13 | partial | the fake Telegram server's Bot API surface |
| C-02.FR-10 | partial | the Leader task of long polling |
