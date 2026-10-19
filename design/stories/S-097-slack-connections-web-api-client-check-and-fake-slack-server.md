---
id: S-097
title: "Slack Connections, the Web API client with its error classes, the Connection check and the fake Slack server (BE)"
capability: C-24
kind: be
layer: L1
depends_on: [S-087, S-089, S-071]
covers: [C-24.FR-1, C-24.FR-2, C-24.FR-8, C-24.FR-14, C-24.AC-1, C-24.AC-3, C-01.FR-13, C-23.FR-2]
files_touched:
  - api/openapi.yaml
  - internal/db/migrations/0016_slack_connections.up.sql
  - internal/db/migrations/0016_slack_connections.down.sql
  - design/db/schema.md
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - internal/keyring/rotate.go
  - internal/keyring/query.sql
  - internal/keyring/rotate_test.go
  - internal/keyring/rotate_integration_test.go
  - internal/slack/client.go
  - internal/slack/client_test.go
  - internal/slack/classify.go
  - internal/slack/classify_test.go
  - internal/slack/check.go
  - internal/slack/check_test.go
  - internal/connections/slack.go
  - internal/connections/slack_test.go
  - internal/connections/connections.go
  - internal/connections/connections_test.go
  - internal/connections/query.sql
  - internal/connections/doctor.go
  - internal/accountlinks/query.sql
  - internal/api/connections.go
  - internal/api/connections_test.go
  - internal/archlint/secretleak.go
  - internal/fakes/fakeslack/fakeslack.go
  - internal/fakes/fakeslack/fakeslack_test.go
  - internal/devmode/devmode.go
  - internal/devmode/devmode_test.go
  - internal/cli/dev.go
  - internal/cli/dev_test.go
  - internal/runtime/runtime.go
  - test/e2e/harness.go
  - AGENTS.md
  - CONTRIBUTING.md
  - web/src/routes/connections.index.tsx
  - web/src/routes/connections.$connectionId.tsx
  - web/src/components/connection-form.tsx
  - web/src/components/connection-check.tsx
  - web/src/components/destination-label.ts
  - web/src/locales/en.json
  - web/src/locales/ru.json
acceptance:
  - "[C-24.FR-1] A Slack Connection is created with a name, a write-only `bot_token`, the event mode (`connection.slack.event_mode`, `socket` by default), a write-only `app_token` required in `socket` mode, a write-only `slack_signing_secret` required in `http` mode, a proxy and its limiter (`connection.slack.limiter`); reads return `bot_token_status`, `app_token_status`, `slack_signing_secret_status`, the read-only `callback_url` `MUSTER_INGEST_URL/api/v1/callbacks/slack/<public_id>`, and `bot_name`, `bot_user_id`, `team_id`, `team_name` and `app_id`, null before the first successful check and again after the bot token is replaced; the Connection has no address field."
  - "[C-24.FR-1] `createConnection` and `updateConnection` refuse with 422 — `required`, or `invalid_format` for a prefix — at the field's pointer a Slack Connection in `socket` mode without an app-level token, stored or typed, one in `http` mode without a Slack signing secret, a `bot_token` that does not start with `xoxb-` and an `app_token` that does not start with `xapp-`; a change of the event mode keeps the other mode's stored secret; the Audit log records `secret_changed` for each of the three secrets without its value."
  - "[C-24.FR-1] `muster secrets rotate-key` re-encrypts the app-level token and the Slack signing secret like every Secret: `encrypted_values` lists `connections.slack_app_token` and `connections.slack_signing_secret`, the registry of `internal/keyring/rotate.go` has both, and `TestIntegrationRotation` rotates a Slack Connection's three secrets; the bot token keeps the field `connections.bot_token`."
  - "[C-24.FR-1] With `slack` in the `Connection` union the SPA's type check passes: the Connections list names a Slack Connection \"Slack\" from a locale key and its Connection page shows its name and Delete but no form, until S-103 adds the form; `addressChanged` of the Connection form has a `slack` branch that compares the proxy only."
  - "[C-24.FR-2, C-24.AC-1] `checkConnection` of a Slack Connection runs through the interactive path the steps `token` — `auth.test` with the bot token, which returns `bot_name` and the workspace — `scopes`, which reads the `x-oauth-scopes` of that answer and makes no request, and in `socket` mode `app_token` — `auth.test` with the app-level token and `bots.info` with the bot token, whose `app_id`s must be equal — each with its latency and path; a passing check stores what it learned on the Connection, and the result's `slack` names the workspace, the bot's user id and the app's id."
  - "[C-24.FR-2, C-24.AC-1] Against the fake Slack server: a revoked bot token fails `token` with `token_invalid` and \"Slack refused the token.\"; a user token (an `auth.test` answer without `bot_id`) fails it with `wrong_token_type` and \"This is not a bot token.\"; a bot without `users:read.email` fails `scopes` with `missing_scope` and \"The bot token lacks the scopes users:read.email.\"; an app-level token of the second app fails `app_token` with `app_mismatch` and \"The app-level token belongs to another Slack app than the bot token.\"; a Connection in `http` mode has no step `app_token`; `checkCodeText` translates the four codes in English and Russian."
  - "[C-24.FR-8] `slack.Classify` maps every answer as C-24.FR-8 says — a case per `error` of the list, `429` with and without `Retry-After`, a `5xx`, a timeout and an `ok: false` answer with HTTP `200` — keeps `needed` of `missing_scope` in the reason, and keeps Slack's `error` as masked, untrusted text (`classify_test.go`)."
  - "[C-24.FR-1] The client sends a stored token only to `https://slack.com/api/<method>`, write methods with a JSON body and read methods form-encoded, `Authorization: Bearer` on each, through the outbound package with the Connection's proxy in the caller's client class; under `muster dev` a Connection whose bot token is one of the fake Slack server's published tokens is served by the fake server instead, and any other by Slack (`client_test.go`)."
  - "[C-24.FR-14, C-23.FR-2] The Request log records each request of the check with the method as its operation (`auth.test`, `bots.info`) and the bot token and the app-level token replaced by `[redacted]` in its headers."
  - "[C-24.AC-3] With an HTTP proxy on the Connection, every request of the check reaches the fake Slack server only through the fake proxy; with the fake answering after the check stopped waiting, the bot token, the app-level token and the Slack signing secret appear in no log line and no error returned by the API (`secretleak.go` probe `slack_connections`)."
  - "[C-01.FR-13] The fake Slack server answers `auth.test`, `bots.info` and `apps.connections.open` for two apps with published tokens, sets `x-oauth-scopes` and `x-accepted-oauth-scopes`, answers errors as `200` `ok: false` and `429` with `Retry-After` when told to; `muster dev` starts it on `127.0.0.1:18095`, prints its address before \"fake Telegram\" and creates the demo Connection \"Dev Slack\" in `socket` mode; the end-to-end harness waits for it; `listLinkableConnections` leaves Slack Connections out until S-102 and `muster doctor` skips them until S-098."
verify: "make ci test-integration"
operator_attention: false
issue: null
---

# S-097. Slack Connections, the Web API client with its error classes, the Connection check and the fake Slack server (BE)

## Scope

**IN**

- The `slack` Connection type: the bot token, the event mode, the app-level token, the Slack signing secret, the proxy,
  the limiter, the read-only callback address and what the check learned; the three secrets in key rotation.
- The Web API client over the outbound package — Slack's fixed address, JSON and form bodies, `ok: false` answers and
  `429` — with the classification of C-24.FR-8 that every later Slack story uses.
- The Connection check: `token`, `scopes` and, in `socket` mode, `app_token` without `apps.connections.open`.
- The fake Slack server with two apps and the demo Connection of `muster dev`, and the routing of the fake's tokens to
  it under `muster dev`.

**OUT**

- Slack Destinations, the Destination check, the channel list, `muster doctor` and the Mention settings (S-098).
- Messages, buttons, edits, Thread replies, deleted Root messages, test messages and previews (S-099).
- Presses over HTTP and the answers to people (S-100); Socket Mode, `apps.connections.open` in the check, the warning
  `another_client_connected` and the masking of the socket's `ticket` (S-101).
- Account links and Mentions of linked people (S-102).
- The check before saving of a Slack Connection and its dry probe (S-103, with the form that uses it), the pages and
  labels (S-103), the documentation page (S-104).
- Interim states until later stories: the profile offers no Slack Connection for linking (S-102); a Slack Connection's
  page has no form and no check (S-103); `muster doctor` skips Slack Connections (S-098); "Dev Slack" receives no
  presses (S-101).

## Contracts

- **Spec** (`api/openapi.yaml`, changed here first, then generated): `ConnectionType`, `Connection`,
  `ConnectionInput` and the type filters gain `slack`; new schemas `SlackEventMode` (`socket`, `http`),
  `SlackConnectionBase` (`type`, `name`, `event_mode`, `limiter`), `SlackConnection` (`id`, `bot_token_status`,
  `app_token_status`, `slack_signing_secret_status`, `callback_url`, `bot_name`, `bot_user_id`, `team_id`,
  `team_name`, `app_id`, `proxy`, `destination_count`, `created_at`, `etag`) and `SlackConnectionInput` (`bot_token`,
  `app_token` and `slack_signing_secret`, write-only, and `proxy`), joined to the discriminators; the secret fields
  follow the `<field>` / `<field>_status` pattern; `ConnectionCheckStep.name` gains `scopes` and `app_token`;
  `NullableConnectionCheckCode` gains `wrong_token_type`, `missing_scope`, `app_mismatch` and `not_slack` (for the dry
  probe of S-103), and its description names them; `ConnectionCheckResult` gains `slack` (`team_id`, `team_name`,
  `bot_user_id`, `app_id`), null for the other types.
- **Operations implemented for the type**: `listConnections`, `createConnection`, `getConnection`,
  `updateConnection`, `deleteConnection` (`connections:read` / `:write`), `checkConnection` (`connections:write`,
  interactive). `listConnectionChannels` answers `409` for a Slack Connection until S-098; `checkConnectionDraft`
  answers `422` at `/type` for a Slack draft until S-103. `listLinkableConnections` leaves out `type = 'slack'` until
  S-102 (`internal/accountlinks/query.sql`).
- **Schema** (migration `0016_slack_connections`, expand only, `schema.md`): `connections.type` accepts `slack`; new
  columns `slack_event_mode` (`socket`, `http`), `slack_app_token_ciphertext`, `_key_id`, `_updated_at`,
  `slack_signing_secret_ciphertext`, `_key_id`, `_updated_at`, the learned `slack_team_id`, `slack_team_name`,
  `slack_team_url`, `slack_app_id` and `slack_bot_id` (the bot's user id and name take `bot_user_id` and
  `bot_username`), and `slack_socket_hello_at` and `slack_socket_other_connections`, written by the Leader from S-101;
  the bot token uses the `bot_token_*` columns; `connections_type_fields_check` gains the Slack branch (the event mode
  set, the other types' columns null, an app-level token in `socket` mode and a Slack signing secret in `http` mode
  until the Connection is deleted), and two checks keep each new secret's ciphertext and key id together;
  `encrypted_values` gains both fields. The CHECKs are replaced `NOT VALID` and validated in a second transaction, as in
  `0008`, so a replica of the previous version keeps working.
- **Key rotation** (`internal/keyring/rotate.go`, `query.sql`): `encryptedFields` gains
  `connections.slack_app_token` and `connections.slack_signing_secret` with their list and re-encrypt queries.
- **Connections** (`internal/connections/slack.go`): the rules of C-24.FR-1 — the token prefixes, the secret each mode
  requires, a stored secret kept across a change of mode — the three secrets through the secret-field helper of S-013
  with the associated data of their fields; Audit log entries as for the other types; a new bot token forgets the
  learned bot, workspace and app, as a new Mattermost token forgets the bot; `deleteConnection` follows S-039.
  `muster doctor` (`doctor.go`) skips Slack Connections until S-098.
- **Client** (`internal/slack/client.go`, `classify.go`): `Call(ctx, method, args)` to `https://slack.com/api/<method>`
  with `Authorization: Bearer`, JSON for the write methods (`chat.*`, `conversations.open`) and
  `application/x-www-form-urlencoded` for the others, through `internal/outbound` with the Connection's proxy, in the
  caller's client class, a connect timeout of 5 s and a call timeout of 10 s; it decodes `ok`, `error`, `needed`,
  `warning` and `response_metadata`, keeps the `x-oauth-scopes` header, registers each token for redaction and names
  the method as the Request log operation. The address is a constant; `muster dev` passes the client a resolver that
  sends the requests of the fake's published tokens to the fake server. `Classify` returns RetryAfter (scope
  Connection), Transient, Fatal (with the reason, `needed` named), MarkupRejected, Gone-candidate (`message_not_found`)
  or Unknown, for S-099 and later to use.
- **Check** (`internal/slack/check.go`, C-24.FR-2): through `delivery.Interactive` on the Connection's limiter, steps
  `token`, `scopes` (no request; `latency_ms` null) and, in `socket` mode, `app_token`; failures with the texts of
  [reference.md](../prd/l1/reference.md#banners-warnings-and-notices) and their codes; any step without a code of its
  own carries the code of `delivery.FailureCode`, or `request_failed` with Slack's masked `error`. The required scopes
  are the six of C-24.FR-1. A passing check stores `slack_team_id`, `slack_team_name`, `slack_team_url` (the `url` of
  `auth.test`), `slack_app_id`, `slack_bot_id`, `bot_user_id` and `bot_username` unless the Connection changed meanwhile
  (`SetSlackIdentity`, `version` compared).
- **SPA type check** (`connections.index.tsx`, `connections.$connectionId.tsx`, `connection-form.tsx`,
  `connection-check.tsx`, and `destination-label.ts`, whose `connectionMessengerName` the list uses): the new union
  member gets explicit branches — the name "Slack" («Slack») from `connections.types.slack`, no address, the bot's name
  as the account; the Connection page renders its form for Mattermost, Telegram and Bridge only, so no other form saves
  a Slack Connection; `checkCodeText` and the step names gain the new codes and steps, translated: "Slack refused the
  token." is `token_invalid`'s existing text; "This is not a bot token.", "The bot token lacks the scopes {scopes}." («У
  токена бота нет прав {scopes}.»), "The app-level token belongs to another Slack app than the bot token." and "The
  address answered, but not as Slack's API." for `not_slack`.
- **Fake Slack server** (`internal/fakes/fakeslack`, `127.0.0.1:18095`): two apps, each a workspace, a bot and an
  app-level token — the published `xoxb-muster-dev-slack-token` with `xapp-muster-dev-slack-token`, used by the demo
  Connection, and `xoxb-muster-dev-slack-token-b` with `xapp-muster-dev-slack-token-b`, for which `muster dev` creates
  no Connection — and the published Slack signing secrets `muster-dev-slack-signing-secret` and `…-b`; `/api/auth.test`,
  `/api/bots.info` and `/api/apps.connections.open` (answering a `ws://127.0.0.1:18095/link/?ticket=…` address that
  S-101 serves), each with `x-oauth-scopes` and `x-accepted-oauth-scopes`; errors as `200` `ok: false`; control
  endpoints `/_fake/config` (scopes per app, revoked tokens, a user token, the app of each token), `/_fake/requests`
  and `/_fake/faults` (an error answer, a `429` with `Retry-After`, a delay) of the fake-server harness.
  `fakeslack_test.go` checks every answer against the shapes of F-070 to F-072. AGENTS.md's development mode,
  `muster dev`'s usage and CONTRIBUTING.md list the fake Slack server and its tokens; `muster dev` prints its address
  before "fake Telegram", which stays the last address line; `test/e2e/harness.go` gains it in `FakeURLs` and the wait
  loop. Later stories add channels (S-098), messages (S-099), presses (S-100), the socket (S-101) and people (S-102).
- **Secrets** (lint 5): probe `slack_connections` pushes the three secrets through create, check and a failed request.
- **Defaults**: `connection.slack.limiter` (P-60), `connection.slack.event_mode`, `delivery.interactive_budget`.

## Steps

1. Write the client with `Classify`, the dev resolver and the check. Check: `client_test.go`, `classify_test.go`,
   `check_test.go` against `httptest` servers, a redirect and a `429` included.
2. Add the migration, key rotation of the two new secrets and the Connection type. Check: `slack_test.go`,
   `connections_test.go`, `go test -tags integration ./internal/keyring/... ./internal/db/...`.
3. Narrow the SPA by type and translate the type's name and the new codes. Check: `make lint`.
4. Write the fake Slack server, the demo Connection, the usage and CONTRIBUTING.md, the harness, the doctor skip, the
   linkable filter and the secret probe. Check: `fakeslack_test.go`, then Verification.

## Verification

```sh
make dev > dev.log 2>&1 &
# the Admin's session (`jar`, `H`) as in S-011
API=localhost:8080/api/v1; FS=127.0.0.1:18095/_fake

# C-24.FR-1, FR-2, AC-1: a Connection and its check against the fake server (the second app)
C=$(curl -s "${H[@]}" $API/connections -d '{"type":"slack","name":"sl","event_mode":"socket",
  "bot_token":"xoxb-muster-dev-slack-token-b","app_token":"xapp-muster-dev-slack-token-b",
  "proxy":{"enabled":false},"limiter":{"limit":50,"per_seconds":60}}' | jq -r .id)
curl -s -b jar $API/connections/$C | jq -c '{event_mode, t: .bot_token_status.set, a: .app_token_status.set, bot_name, cb: (.callback_url | test("/api/v1/callbacks/slack/CN[0-9A-Z]{12}$"))}'
# {"event_mode":"socket","t":true,"a":true,"bot_name":null,"cb":true}
curl -s "${H[@]}" -X POST $API/connections/$C/checks | jq -c '{ok, s: [.steps[] | {name, ok, via}], team: .slack.team_name}'
# {"ok":true,"s":[{"name":"token","ok":true,"via":"direct"},{"name":"scopes","ok":true,"via":"direct"},{"name":"app_token","ok":true,"via":"direct"}],"team":"Fake workspace B"}
curl -s -b jar $API/connections/$C | jq -c '[.bot_name != null, .app_id != null, .team_id != null]'   # [true,true,true]
CHK() { curl -s "${H[@]}" -X POST $API/connections/$C/checks | jq -r '[.steps[] | select(.ok == false)][0] | "\(.name) \(.code): \(.message)"'; }
curl -s -X PUT $FS/config -d '{"scopes":{"b":"chat:write,channels:read,groups:read,im:write,users:read"}}' > /dev/null; CHK
# scopes missing_scope: The bot token lacks the scopes users:read.email.
curl -s -X PUT $FS/config -d '{"scopes":{"b":null},"app_of":{"xapp-muster-dev-slack-token-b":"a"}}' > /dev/null; CHK
# app_token app_mismatch: The app-level token belongs to another Slack app than the bot token.
curl -s -X PUT $FS/config -d '{"app_of":{},"revoked":["xoxb-muster-dev-slack-token-b"]}' > /dev/null; CHK
# token token_invalid: Slack refused the token.
curl -s -X PUT $FS/config -d '{"revoked":[]}' > /dev/null

# C-24.FR-1: the secret each mode requires
ETAG() { curl -s -b jar $API/connections/$C | jq -r .etag; }
curl -s "${H[@]}" -X PUT -H "If-Match: $(ETAG)" $API/connections/$C -d '{"type":"slack","name":"sl","event_mode":"http",
  "proxy":{"enabled":false},"limiter":{"limit":50,"per_seconds":60}}' | jq -c '[.status, .errors[0].pointer]'
# [422,"/slack_signing_secret"]
curl -s "${H[@]}" -X PUT -H "If-Match: $(ETAG)" $API/connections/$C -d '{"type":"slack","name":"sl","event_mode":"http",
  "slack_signing_secret":"muster-dev-slack-signing-secret-b","proxy":{"enabled":false},"limiter":{"limit":50,"per_seconds":60}}' > /dev/null
curl -s "${H[@]}" -X POST $API/connections/$C/checks | jq -c '[.steps[].name]'   # ["token","scopes"]
curl -s -b jar $API/connections/$C | jq -c '[.app_token_status.set, .slack_signing_secret_status.set]'   # [true,true]
psql "$MUSTER_DATABASE_URL" -tAc "SELECT field FROM encrypted_values WHERE field LIKE 'connections.slack%' ORDER BY 1" | sort -u
# connections.slack_app_token
# connections.slack_signing_secret

# C-24.FR-14: the Request log of the check, tokens masked
curl -s -b jar "$API/request-log?connection=$C&limit=5" | jq -r '.items[].operation' | sort -u | tr '\n' ' '   # auth.test bots.info
curl -s -b jar "$API/request-log?connection=$C&limit=5" | grep -c 'xoxb-muster-dev-slack-token-b\|xapp-muster-dev-slack-token-b'   # 0

# C-24.AC-3: through the fake HTTP proxy, and no secret in an error
curl -s "${H[@]}" -X PUT -H "If-Match: $(ETAG)" $API/connections/$C -d '{"type":"slack","name":"sl","event_mode":"socket",
  "proxy":{"enabled":true,"type":"http","address":"127.0.0.1:18091"},"limiter":{"limit":50,"per_seconds":60}}' > /dev/null
curl -s -X DELETE 127.0.0.1:18091/_fake/requests; curl -s -X DELETE $FS/requests
curl -s "${H[@]}" -X POST $API/connections/$C/checks | jq -r '.steps[0].via'      # proxy
echo $(( $(curl -s $FS/requests | jq '[.[] | select(.path | startswith("/api/"))] | length') - $(curl -s 127.0.0.1:18091/_fake/requests | jq length) ))   # 0
curl -s -X POST $FS/faults -d '{"path":"/api/auth.test","delay_ms":12000,"times":1}' > /dev/null
curl -s "${H[@]}" -X POST $API/connections/$C/checks | grep -c 'muster-dev-slack'   # 0
grep -c 'xoxb-muster-dev-slack-token-b\|xapp-muster-dev-slack-token-b\|muster-dev-slack-signing-secret' dev.log   # 0

# C-01.FR-13: the demo Connection; not offered for linking yet; skipped by doctor
curl -s -b jar $API/connections | jq -r '.items[] | select(.type == "slack") | .name' | sort | tr '\n' ' '   # Dev Slack sl
curl -s -b jar $API/me/account-links/connections | jq -r '[.items[].messenger] | unique | join(",")'   # bridge,mattermost,telegram
./bin/muster dev doctor | grep -c 'Dev Slack'                                    # 0
grep -n 'fake Slack\|fake Telegram' dev.log | tail -2 | cut -d' ' -f3-4           # fake Slack, then fake Telegram

# Live, on the test workspace: the real app's tokens, read from files and never printed; Slack's own answers
SLK=~/.config/muster-spike
L=$(curl -s "${H[@]}" $API/connections -d "$(jq -n --rawfile b $SLK/slack-bot-token --rawfile a $SLK/slack-app-token \
  '{type:"slack",name:"test workspace",event_mode:"socket",bot_token:($b|rtrimstr("\n")),app_token:($a|rtrimstr("\n")),
    proxy:{enabled:false},limiter:{limit:50,per_seconds:60}}')" | jq -r .id)
curl -s "${H[@]}" -X POST $API/connections/$L/checks | jq -c '{ok, s: [.steps[].name], bot: .bot_name}'
# {"ok":true,"s":["token","scopes","app_token"],"bot":"muster"}
curl -s -b jar "$API/request-log?connection=$L&limit=5" | jq -r '.items[0].url'   # https://slack.com/api/…
curl -s "${H[@]}" -X DELETE -H "If-Match: $(curl -s -b jar $API/connections/$L | jq -r .etag)" $API/connections/$L -o /dev/null -w '%{http_code}\n'   # 204
```

`go test -tags integration -run TestIntegrationRotation ./internal/keyring/` shows the app-level token and the Slack
signing secret re-encrypted under the new key.

## Open questions

None.

## Notes

- Suggested commit: `feat(slack): add slack connections and the slack web api client`.
- The live check needs the maintainer's test workspace (D382): the tokens are in `~/.config/muster-spike/` and are
  read with `jq --rawfile`, never echoed. It leaves nothing behind: the check posts no message, and the Connection is
  deleted at the end. A machine without access to `slack.com` skips the live block and says so in the pull request.
- The check here proves both tokens and the scopes; `apps.connections.open`, which proves that Socket Mode is on for
  the app, joins the step `app_token` in S-101, together with the masking of the `ticket` its answer carries.
- `scopes` reads the header of the `token` step's answer (F-071), so the check makes no extra request for it.
- The Slack signing secret is never sent anywhere, so a stored one needs no address rule; only presses prove it (S-100).
- The fake server's published tokens follow Slack's prefixes, so the prefix rule of C-24.FR-1 applies to them too.
- With 43 files the story is at the edge of the size signal; its parts — the type, the client and the fake — are each
  needed for the live check of the others. The check before saving moves to S-103, with the form that uses it.
- While S-097 rolls out, a replica of the previous version reads a Slack row as an unknown type and fails its checks;
  this ends when every replica runs the new version.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-24.FR-1 | partial | the API, the client and key rotation; the page is S-103 |
| C-24.FR-2 | partial | `token`, `scopes` and `app_token` without `apps.connections.open`; the socket part and the warning are S-101, the dry probe and the check before saving S-103, the page S-103 |
| C-24.FR-8 | partial | the classification; delivery's use of it is S-099 |
| C-24.FR-14 | partial | the requests of the check; delivery S-099, presses S-100, the socket S-101 |
| C-24.AC-1 | full | |
| C-24.AC-3 | partial | the Web API requests of the check; the Destination check is S-098, the socket S-101 |
| C-01.FR-13 | partial | the fake Slack server and the demo Slack Connection; S-098 to S-102 extend the fake |
| C-23.FR-2 | partial | Slack's Web API methods as operations; `interactive` is S-100 |
