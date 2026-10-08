---
id: S-039
title: "Mattermost Connections and Destinations, the Destination check and the fake Mattermost server (BE)"
capability: C-13
kind: be
layer: L1
depends_on: [S-037]
covers: [C-13.FR-1, C-13.FR-2, C-13.FR-3, C-13.FR-6, C-13.FR-10, C-13.FR-13, C-13.AC-7, C-11.FR-2, C-11.FR-18, C-12.FR-8, C-01.FR-13, C-11.FR-14]
files_touched:
  - internal/connections/connections.go
  - internal/connections/query.sql
  - internal/connections/connections_test.go
  - internal/connections/live_test.go
  - internal/destinations/write.go
  - internal/destinations/query.sql
  - internal/destinations/write_test.go
  - internal/destinations/delete.go
  - internal/delivery/interactive.go
  - internal/delivery/broken.go
  - internal/delivery/broken_test.go
  - internal/delivery/limiter_test.go
  - internal/delivery/live_test.go
  - internal/delivery/deliverytest/unlimited.go
  - internal/delivery/deliverytest/unlimited_test.go
  - internal/mattermost/client.go
  - internal/mattermost/check.go
  - internal/mattermost/check_test.go
  - internal/api/connections.go
  - internal/api/destinations.go
  - internal/api/connections_test.go
  - internal/api/destinations_test.go
  - internal/api/server.go
  - internal/api/problem.go
  - internal/fakes/fakeserver/fakeserver.go
  - internal/fakes/fakeserver/fakeserver_test.go
  - internal/fakes/fakemattermost/fakemattermost.go
  - internal/fakes/fakemattermost/posts.go
  - internal/fakes/fakemattermost/presses.go
  - internal/fakes/fakemattermost/fakemattermost_test.go
  - internal/devmode/devmode.go
  - internal/devmode/devmode_test.go
  - internal/runtime/runtime.go
  - internal/runtime/runtime_test.go
  - internal/archlint/secretleak.go
  - sqlc.yaml
  - api/openapi.yaml
  - test/e2e/smoke_test.go
acceptance:
  - "[C-13.FR-1, C-13.FR-2] A Mattermost Connection is created with a name, the server URL, a write-only bot token, a proxy and its limiter (`connection.mattermost.limiter`); `checkConnection` on the interactive path returns the bot's name, and a revoked token fails the check."
  - "[C-13.FR-13] A Mattermost Connection returns the read-only `callback_url`, `MUSTER_INGEST_URL/api/v1/callbacks/mattermost/<public_id>`."
  - "[C-13.FR-2, C-13.FR-3, C-13.FR-10] `createDestination` of type `mattermost` takes the Connection, team and channel, Mention settings and its limiter (`destination.mattermost.limiter`, by default the Connection's) and runs the Destination check; a channel without the bot is refused with 422 `destination_check_failed` at `/channel_id` and nothing is saved."
  - "[C-12.FR-8, C-11.FR-18] A saved Mattermost Destination returns its team and channel names, its Mention settings as validated by S-037 — a `user_ids` entry naming no User is refused with 422 `unknown_id` — and its limiter."
  - "[C-13.FR-10] `checkDestination` runs the Destination check through the interactive path and returns each check with its result: with the bot removed from the channel `bot_in_channel` fails with \"The bot is not a member of this channel.\"; a success on a Broken Destination (set up directly) ends the Broken state."
  - "[C-11.FR-2] The Connection check, the channel list and the Destination check run in the interactive client class on the limiter of the Connection, and answer 503 with `Retry-After` when no token is free in time."
  - "[C-13.AC-7] With an HTTP proxy on the Connection, every request of the Connection check, the channel list and the Destination check reaches the fake server only through the fake proxy."
  - "[C-13.FR-6] Deleting a Connection used by a Destination that is not deleted answers 409 `in_use`; once the Destination is deleted, the Connection can be deleted and the final edits still pending for it end as Not delivered (delivery rows set up directly; S-061 publishes real posts)."
  - "[C-01.FR-13] The fake Mattermost server reproduces the facts F-022 to F-032 and F-054 to F-058 one by one, and `muster dev` starts with the demo Connection \"Dev Mattermost\"."
verify: "make ci test-integration"
operator_attention: false
issue: 39
---

# S-039. Mattermost Connections and Destinations, the Destination check and the fake Mattermost server (BE)

## Scope

**IN**

- Connections: the Connection API with the Mattermost type, its check and its channel list; deleting a Connection.
- The shared Destination write path, and creating and updating Mattermost Destinations with the Destination check on
  save, Mention settings and limiter.
- The Destination check of a Mattermost Destination, from `checkDestination` and as the code that the adapter of S-061
  calls as its `Check`.
- The REST client of the Mattermost type that the adapter of S-061 builds on.
- The fake Mattermost server with every verified fact this capability relies on — posts, notifications and presses
  included, which S-061 drives through the adapter — and the demo Connection of `muster dev`.

**OUT**

- The adapter (posts, edits, Thread replies, Mentions, escaping, the response mapping and the Broken probe through
  `Check`), button presses and callbacks, the Delivery problem filter, the `internal_alerts` Route suggestion, the
  `muster doctor` checks, the documentation and the full load profile (S-061).
- The pages (S-040 for Connections, S-064 for Destinations); the Telegram type (S-041, S-042); outgoing webhooks
  (S-044, S-045).
- The test message and the bot's own press (S-047).

## Contracts

- **Operations implemented**: `listConnections`, `createConnection`, `getConnection`, `updateConnection`,
  `deleteConnection` (`connections:read` / `:write`; the Telegram variant answers `422 unsupported` at `/type` until
  S-041), `checkConnection` and `listConnectionChannels` (`connections:write`, interactive), `createDestination` and
  `updateDestination` for `mattermost` (`destinations:write`; other types `422 unsupported` at `/type` until S-042 and
  S-044), `checkDestination` (`destinations:test`, interactive). Schemas: `Connection`, `ConnectionInput`,
  `MattermostConnection(Base, Input)`, `ConnectionList`, `ConnectionType`, `ConnectionCheckRequest`,
  `ConnectionCheckStep`, `ConnectionCheckResult`, `ConnectionPath`, `MattermostChannel(List)`, `DestinationInput`,
  `MattermostDestination(Input)`, `DestinationCreated`, `DestinationCheckItem`, `DestinationCheckResult`,
  `ProxyConfig(Input)`.
- **Connections** (C-13.FR-1, FR-6; `connections`, `internal/connections`): name unique among Connections that are not
  deleted (`409 name_taken`), `server_url` (absolute `http(s)`, no query or fragment), `bot_token` write-only through
  the secret-field helper of S-013 (`bot_token_status`), `proxy` through `internal/proxyconf`, `limiter` (default
  `connection.mattermost.limiter`), `destination_count` of Destinations that are not deleted, and the read-only
  `callback_url` — `MUSTER_INGEST_URL/api/v1/callbacks/mattermost/<public_id>` — for the Connection page
  (C-13.FR-13); the callback itself is S-061's. Audit log entries `connection.created`, `.updated`, `.deleted` with
  diffs (the token as `secret_changed`); hint `connection`. `deleteConnection` answers `409 in_use` while a
  Destination that is not deleted uses it; otherwise it soft-deletes the Connection, wipes its secrets and calls
  `delivery.AbandonConnection` of S-035.
- **REST client** (`client.go`): the calls of the Mattermost REST API v4 with the bot token as a bearer token, through
  the outbound package with the Connection's proxy, in the client class of the caller. This story uses the reads below;
  S-061 adds posts, patches, the plain post read and ephemeral posts.
- **Connection check** (C-13.FR-2; `checkConnection`): through `delivery.Interactive` on the Connection's limiter, in the
  interactive client class through the Connection's proxy: `GET /api/v4/users/me` with the bot token → step `token`
  with `latency_ms` and `via` (`direct` or `proxy`); `bot_name` is the bot's username, stored in
  the columns `bot_username` and `bot_user_id` of `connections` and returned as `MattermostConnection.bot_username`.
  `503` with `Retry-After` when no token is free in time.
- **Channels** (`listConnectionChannels`): the bot's teams and, per team, the channels the bot belongs to
  (`GET /api/v4/users/me/teams`, `GET /api/v4/users/me/teams/{team_id}/channels`), filtered by `team_id` and `q`,
  through the interactive path; `409` for a Telegram Connection.
- **Mattermost Destinations** (C-13.FR-2, FR-3, C-11.FR-18, C-12.FR-8; `destinations`,
  `internal/destinations/write.go`): the shared create and update path validates the common fields — name unique among
  Destinations that are not deleted, `mentions` through `mentions.Validate` of S-037, `limiter` — and hands the type's
  fields to its type. Mattermost: `connection_id` (a Mattermost Connection, `422 unknown_id` otherwise), `team_id`,
  `channel_id`; the default limiter is the Connection's. Saving runs the Destination check through the interactive path
  and refuses a failing one with `422 destination_check_failed`: one `errors[]` item per failing check at the field it
  concerns — `/connection_id` for `token`, `/channel_id` for `bot_in_channel` — with the check's message as `detail`;
  a passing check stores `mattermost_team_name` and `mattermost_channel_name`, returned as `team_name` and
  `channel_name`. Audit log entries `destination.created`, `.updated`; hint `destination`. `createDestination` returns
  `DestinationCreated` with `signing_secret` null.
- **Destination check** (C-13.FR-10; `check.go`): `GET /api/v4/users/me` (`token`, failing with "The bot token is not
  valid.") and `GET /api/v4/channels/{channel_id}/members/me` (`bot_in_channel`, failing with "The bot is not a member
  of this channel."). It runs on save and from `checkDestination` through the interactive path in the interactive
  class; a success on a Broken Destination calls `MarkHealthy` of S-035. S-061 uses the same function as the adapter's
  `Check` for the Broken probe in the delivery class, and in `muster doctor`.
- **Secrets** (C-03.FR-21, lint 5): the bot token is registered with the outbound client for redaction and pushed
  through the Connection, channel and check paths by a probe in `internal/archlint/secretleak.go`; S-061 extends the
  probe to the adapter and callback paths.
- **Fake Mattermost** (C-01.FR-13; `127.0.0.1:18065`) reproduces the verified facts this capability relies on; this
  story proves each one against the fake alone, and S-061 drives posts, notifications, presses and the rate limit
  through the adapter:
  - a team `dev` (`team-dev`) with the channels `alerts` (`ch-alerts`), `alerts-prod` (`ch-alerts-prod`) and `no-bot`
    (`ch-nobot`); the bot `muster-dev-bot` (`musterdevbotuserfake000000`, the `BotUsername` and `BotUserID` the fake already has)
    is a member of the first two; the users `alice` (`u-alice`) and `bob`
    (`u-bob`); any non-empty bot token works except those revoked through `/_fake/config` (`401`);
  - the REST calls of this story and of S-061; a post by the bot with `root_id` raises the root's reply count (F-027);
    a post to a channel without the bot answers `403`, to an unknown or archived channel `404` naming the channel; an
    edit of an unknown post `404` "post not found"; for a deleted post (F-058) an edit answers `403`
    `api.context.permissions.app_error`, a reply with it as `root_id` `400` `api.post.create_post.root_id.app_error`
    and `GET /api/v4/posts/{id}` `404`, while that read of a live post answers `200`; a post whose `message` is longer
    than 16,383 characters answers `400` `model.post.is_valid.message_length.app_error`, and
    `GET /api/v4/config/client?format=old` reports `MaxPostSize` `16383` (F-057);
  - notifications (`GET /_fake/notifications`): a new post or reply with `@channel`, `@all`, `@here` or `@<user>` in its
    `message` notifies, a Mention makes the user a follower of the Thread (F-027); an edit never notifies, even one that
    adds a Mention (F-029); a Mention inside an attachment's text notifies as well, recorded with an empty `preview`
    when the post's `message` is empty (F-056);
  - presses (`POST /_fake/press` `{post_id, action_id, user_id}`): the server posts the press to the button's
    `integration.url` with `user_id`, `user_name`, `channel_id`, `channel_name`, `team_id`, `team_domain`, `post_id`,
    `trigger_id`, `type`, `data_source` and the button's `context` (F-023), waiting up to 30 s (F-032), and answers the
    control request with `{status, answer}` of that call; an answer with `update` edits the post and marks it "Edited"
    (F-023); an answer's `ephemeral_text` becomes an ephemeral reply in the post's Thread from "System" (F-025); any
    other field of the answer is ignored (F-024); when the URL's host is a private or loopback address not listed in
    `AllowedUntrustedInternalConnections` (`PUT /_fake/config`), the press fails with `400` "Action integration error"
    for the person and a server log line (`GET /_fake/server-log`) saying the address is forbidden (F-022);
  - `POST /api/v4/posts/ephemeral` records an ephemeral post for the user, shown in the channel view when it has no
    `root_id` and only in the Thread when it has one (F-025, F-026; `GET /_fake/ephemeral`);
  - the rate limit, off by default (F-030); `PUT /_fake/config {"rate_limit": {"enabled": true}}` allows 10 requests per
    second with a burst of 100 per client address, answering `429` `text/plain` "limit exceeded" with `Retry-After: 1`
    and `X-Ratelimit-Limit`, `-Remaining`, `-Reset` (F-031);
  - control endpoints under `/_fake/`: `posts`, `DELETE posts/{id}` (a person deletes a post), `PUT` and `DELETE
    channels/{id}/members/{user_id}`, `POST channels/{id}/archive`, `press`, `presses` (each press with the request
    sent to the integration URL and its answer), `ephemeral`, `notifications`, `config`, `server-log`, and the
    harness's `requests` (each with its arrival time `at_ms`, its `query` and the `status` it was answered with) and
    `faults`, which gain `content_type`, `times` (how many requests a fault hits), a `*` in `path`, and `delay_ms` that
    holds the answer after the post was created.

  `muster dev` sets the fake's `AllowedUntrustedInternalConnections` to `localhost 127.0.0.1` at start and adds a demo
  Connection "Dev Mattermost" to it, without Destinations.
  The fake answers every call it does not know with `501`; `test/e2e/smoke_test.go` asserts that for
  `POST /api/v4/posts`, which this story implements, so the smoke test takes a call the fake still does not serve.
- **Wiring** (`internal/api/server.go`, `internal/api/problem.go`, `sqlc.yaml`): the operations join the
  implemented-operations map and the API `Config` gains `connections` and the Destination write path; `problem.go`
  maps the domain errors of Connections and Destinations (`name_taken`, `in_use`, `unknown_id`,
  `destination_check_failed`); `sqlc.yaml` gains the entry for `internal/connections/query.sql`.
- **Defaults**: `connection.mattermost.limiter`, `destination.mattermost.limiter`, `delivery.interactive_budget`.

## Steps

1. Extend the fake server with the facts above. Check: `fakemattermost_test.go` reproduces F-022 to F-032 and F-054 to
   F-058 one by one.
2. Write the REST client, Connections, the check and the channel list. Check: `connections_test.go` covers `in_use`, the
   revoked token and the proxy path.
3. Write the shared Destination write path and the Mattermost type with the check on save. Check: `write_test.go` and
   `check_test.go`.
4. Add `checkDestination`, the demo Connection and the secret probe. Check: Verification below.

## Verification

```sh
make dev &
# the Admin's session (`jar`, `H`) as in S-011
API=localhost:8080/api/v1; FMM=127.0.0.1:18065/_fake

# C-13.FR-1, FR-2: Connection and check
C=$(curl -s "${H[@]}" $API/connections -d '{"type":"mattermost","name":"mm","server_url":"http://127.0.0.1:18065",
  "bot_token":"mm-dev-token","proxy":{"enabled":false},"limiter":{"limit":5,"per_seconds":1}}' | jq -r .id)
curl -s "${H[@]}" -X POST $API/connections/$C/checks | jq -c '{ok, bot_name, s: [.steps[] | {name, ok, via}]}'
# {"ok":true,"bot_name":"muster-dev-bot","s":[{"name":"token","ok":true,"via":"direct"}]}
curl -s "${H[@]}" "$API/connections/$C/channels?q=alerts" | jq -c '[.items[] | {id, name, team_name}]'
# [{"id":"ch-alerts","name":"alerts","team_name":"dev"},{"id":"ch-alerts-prod","name":"alerts-prod","team_name":"dev"}]
curl -s -b jar $API/connections/$C | jq -c '{t: .bot_token_status.set, cb: (.callback_url | test("/api/v1/callbacks/mattermost/CN[0-9A-Z]{12}$"))}'
# {"t":true,"cb":true}                                                    C-13.FR-13
curl -s -b jar $API/connections | jq -r '.items[].name' | sort | tr '\n' ' '   # Dev Mattermost mm

# C-13.FR-2, FR-3, C-12.FR-8: a channel without the bot is refused, a channel with it is saved
M='{"new_alert_group":{"everyone":"none","user_ids":[],"groups":[]},"new_alerts":{"everyone":"channel","user_ids":[],"groups":[]},
"reopen":{"everyone":"none","user_ids":[],"groups":[]},"ack_timeout":{"everyone":"none","user_ids":[],"groups":[]},
"snooze_ended":{"everyone":"none","user_ids":[],"groups":[]},"rise_to_urgent":{"everyone":"none","user_ids":[],"groups":[]}}'
MKD() { curl -s "${H[@]}" $API/destinations -d "{\"type\":\"mattermost\",\"name\":\"$1\",\"connection_id\":\"$3\",\"team_id\":\"team-dev\",
  \"channel_id\":\"$2\",\"mentions\":${4:-$M},\"limiter\":{\"limit\":5,\"per_seconds\":1}}"; }
MKD nobot ch-nobot $C | jq -c '{status, code: .errors[0].code, p: .errors[0].pointer}'
# {"status":422,"code":"destination_check_failed","p":"/channel_id"}
MKD bad ch-alerts $C "$(jq -c '.new_alerts.user_ids = ["SR0000000000ZZ"]' <<<"$M")" | jq -c '[.status, .errors[0].code]'
# [422,"unknown_id"]
D=$(MKD alerts ch-alerts $C | jq -r .destination.id)
curl -s -b jar $API/destinations/$D | jq -c '{team_name, channel_name, n: .mentions.new_alerts.everyone, l: .limiter.limit}'
# {"team_name":"dev","channel_name":"alerts","n":"channel","l":5}

# C-13.FR-10: the check from the API, failing and passing
curl -s -X DELETE $FMM/channels/ch-alerts/members/musterdevbotuserfake000000
curl -s "${H[@]}" -X POST $API/destinations/$D/checks | jq -c '{ok, c: [.checks[] | {name, ok}], m: .checks[1].message}'
# {"ok":false,"c":[{"name":"token","ok":true},{"name":"bot_in_channel","ok":false}],"m":"The bot is not a member of this channel."}
curl -s -X PUT $FMM/channels/ch-alerts/members/musterdevbotuserfake000000
psql "$MUSTER_DATABASE_URL" -qc "UPDATE destinations SET health = 'broken', broken_since = now(), broken_cause = 'fatal',
  broken_reason = 'test', next_probe_at = now() + interval '1 hour' WHERE public_id = '$D'"
curl -s "${H[@]}" -X POST $API/destinations/$D/checks | jq -c '{ok, h: .health.state}'   # {"ok":true,"h":"healthy"}

# C-13.AC-7: through the fake HTTP proxy
curl -s "${H[@]}" -X PUT -H "If-Match: $(curl -s -b jar $API/connections/$C | jq -r .etag)" $API/connections/$C \
  -d '{"type":"mattermost","name":"mm","server_url":"http://127.0.0.1:18065","proxy":{"enabled":true,"type":"http","address":"127.0.0.1:18091"},"limiter":{"limit":5,"per_seconds":1}}' > /dev/null
curl -s -X DELETE 127.0.0.1:18091/_fake/requests; curl -s -X DELETE $FMM/requests
curl -s "${H[@]}" -X POST $API/connections/$C/checks | jq -r '.steps[0].via'  # proxy
curl -s "${H[@]}" "$API/connections/$C/channels" > /dev/null; curl -s "${H[@]}" -X POST $API/destinations/$D/checks > /dev/null
curl -s 127.0.0.1:18091/_fake/requests | jq -c '[.[].target] | unique'      # ["127.0.0.1:18065"]
echo $(( $(curl -s $FMM/requests | jq length) - $(curl -s 127.0.0.1:18091/_fake/requests | jq length) ))   # 0

# C-13.FR-6: a used Connection, then a deleted Destination (a second Connection, so that C and D stay for S-061)
C2=$(curl -s "${H[@]}" $API/connections -d '{"type":"mattermost","name":"mm-old","server_url":"http://127.0.0.1:18065",
  "bot_token":"mm-old-token","proxy":{"enabled":false},"limiter":{"limit":5,"per_seconds":1}}' | jq -r .id)
D2=$(MKD old ch-alerts-prod $C2 | jq -r .destination.id)
curl -s "${H[@]}" -X DELETE $API/connections/$C2 | jq -c '[.status, .code]'  # [409,"in_use"]
curl -s "${H[@]}" -X DELETE $API/destinations/$D2 -o /dev/null -w '%{http_code}\n'   # 204
curl -s "${H[@]}" -X DELETE $API/connections/$C2 -o /dev/null -w '%{http_code}\n'    # 204
curl -s -b jar $API/connections | jq '[.items[] | select(.id == "'$C2'")] | length'   # 0
```

`internal/connections/connections_test.go` adds the final edits of C-13.FR-6: delivery rows of a Destination set up
directly, then the Destination and the Connection deleted — the pending final edits end as Not delivered without a
request to the server. `internal/mattermost/check_test.go` covers the `503` of C-11.FR-2 with an exhausted limiter.

## Open questions

None.

## Notes

- Suggested commit: `feat(mattermost): add mattermost connections, destinations and the destination check`.
- The fake server is the largest part of this story; the facts it reproduces are its contract with S-061, S-047 and
  S-049.
- Corrections made while implementing:
  - `delivery.Interactive.Do` takes an `Op` instead of a function (journal D277): `PublishOp`, `UpdateOp` and
    `ReplyOp` make the adapter's send or edit inside `interactive.go`, so lint 3 stays strict; `CheckOp` runs an
    adapter's `Check`; `ReadOp` carries a read such as this story's checks and channel list. Each HTTP request of a
    check or a listing takes its own limiter token. `deliverytest.Unlimited` is an interactive path without a
    database for the tests of its callers.
  - Once the token and the membership pass, the Destination check also reads the channel and its team
    (`GET /api/v4/channels/{channel_id}`, `GET /api/v4/teams/{team_id}`) for the names it stores; a channel of
    another team fails `bot_in_channel` with "The channel is not in this team.", and an archived one with "The
    channel is archived." (posts to it would answer `404`, which is Fatal).
  - `updateConnection` refuses a new `server_url` without the `bot_token` (`422 required` at `/bot_token`), so that
    the stored token is never sent to another server.
  - `api/openapi.yaml`: `MattermostConnection.bot_username` (read-only, the check stores it) was missing, and
    `createDestination` and `updateDestination` lacked the `503` their Destination check on the interactive path
    answers.
  - The Destination write path lives in `destinations` with its type check behind an interface that
    `connections` implements, so `destinations/delete.go` gains the save's queries and settings; a passing save of a
    Broken Destination also ends the Broken state.
  - No log event, metric or Internal alert is new: the Audit log records the changes, and the final edits that the
    deletion of a Connection abandons log `delivery_not_delivered`.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-13.FR-1 | partial | the API; the page is S-040 |
| C-13.FR-2 | partial | the API; the pages are S-040 and S-064 |
| C-13.FR-3 | partial | the Destination's fields; the Root message layout is S-061 |
| C-13.FR-6 | full | |
| C-13.FR-10 | partial | on save and through the API; the probe and `muster doctor` are S-061, the "Check" button S-064 |
| C-13.FR-13 | partial | the callback address in the API; the page is S-040, the test press S-047 |
| C-13.AC-7 | full | |
| C-11.FR-2 | partial | the first callers: the Connection check, the channel list and the Destination check |
| C-11.FR-18 | partial | the Mattermost fields |
| C-12.FR-8 | partial | Mention settings stored on Mattermost Destinations; their syntax in posts is S-061 |
| C-01.FR-13 | partial | the fake Mattermost server |
| C-11.FR-14 | partial | `deleteConnection` abandons the final edits still pending (`delivery.AbandonConnection`) |
