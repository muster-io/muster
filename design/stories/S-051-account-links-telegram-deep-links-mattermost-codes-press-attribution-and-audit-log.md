---
id: S-051
title: "Account links: Telegram deep links, Mattermost codes, press attribution and Audit log (BE)"
capability: C-18
kind: be
layer: L1
depends_on: [S-068]
covers: [C-18.FR-1, C-18.FR-2, C-18.FR-3, C-18.FR-4, C-18.FR-5, C-18.FR-6, C-18.FR-7, C-18.FR-8, C-18.FR-9, C-18.FR-10, C-18.FR-11, C-18.AC-1, C-18.AC-2, C-18.AC-3, C-18.AC-4, C-18.AC-5, C-18.AC-6, C-18.AC-7, C-14.FR-8, C-10.FR-11, C-03.AC-14, C-03.FR-13, C-03.FR-14, C-12.FR-12, C-17.FR-10]
files_touched:
  - api/openapi.yaml
  - internal/accountlinks/accountlinks.go
  - internal/accountlinks/telegram.go
  - internal/accountlinks/mattermost.go
  - internal/accountlinks/lookup.go
  - internal/accountlinks/query.sql
  - internal/accountlinks/accountlinks_test.go
  - internal/accountlinks/telegram_test.go
  - internal/accountlinks/mattermost_test.go
  - internal/accountlinks/live_test.go
  - internal/api/accountlinks.go
  - internal/api/accountlinks_test.go
  - internal/api/middleware.go
  - internal/api/server.go
  - internal/api/server_test.go
  - internal/api/live.go
  - internal/api/problem.go
  - internal/db/notify.go
  - internal/delivery/interactive.go
  - internal/delivery/presses_test.go
  - internal/telegram/client.go
  - internal/telegram/start.go
  - internal/telegram/start_test.go
  - internal/telegram/updates.go
  - internal/connections/telegram.go
  - internal/connections/telegram_test.go
  - internal/db/checks.go
  - internal/mattermost/client.go
  - internal/mattermost/callback_test.go
  - internal/messages/texts/en.json
  - internal/messages/texts/ru.json
  - internal/users/admin.go
  - internal/users/admin_test.go
  - internal/live/hub.go
  - internal/live/live_test.go
  - internal/fakes/fakemattermost/fakemattermost.go
  - internal/fakes/fakemattermost/fakemattermost_test.go
  - internal/fakes/faketelegram/faketelegram.go
  - internal/fakes/faketelegram/updates.go
  - internal/fakes/faketelegram/faketelegram_test.go
  - internal/runtime/runtime.go
  - internal/runtime/runtime_test.go
  - internal/leader/tasks.go
  - internal/metrics/catalogue.go
  - internal/logging/events.go
  - internal/archlint/secretleak.go
  - docs/messengers/account-links.md
  - design/db/schema.md
  - test/e2e/account_links_test.go
  - test/e2e/telegram_connection_test.go
  - design/stories/S-052-messenger-accounts-in-the-profile-and-account-links-on-the-user-page.md
acceptance:
  - "[C-18.FR-1, C-18.FR-2, C-18.FR-7, C-14.FR-8] `startTelegramLink` from the web session, without re-authentication or TOTP, returns `https://t.me/<bot>?start=<token>` with a 43-character token valid for `account_link.telegram_token_ttl`; `/start <token>` to the bot links that Telegram account, the bot replies \"Linked to Muster user {name} ({login})\", and the user's live-update stream gets an `account-links` hint."
  - "[C-18.AC-1, C-18.FR-2] The second `/start` with the same token links nothing, gets the reply \"This link is invalid or expired. Start again from your Muster profile.\" and is recorded as `account_link.link_rejected` with the reason `token_used`; an unknown token, an expired one and a token of another Connection are rejected the same way, with the same reply."
  - "[C-18.FR-3, C-18.FR-11] `startMattermostLink` with `@bob` finds the user through the API and the bot sends him a direct message with an 8-character code and \"You requested a link to Muster. Do not share this code.\"; `confirmMattermostLink` with the code links the account and the bot confirms \"Linked to Muster user {name}\"; both bot messages go through the interactive path."
  - "[C-18.AC-2] After five wrong codes the sixth attempt is refused even with the correct code, and the request is void; each wrong code is recorded as `account_link.link_rejected` with the reason `wrong_code`."
  - "[C-18.FR-3] Code requests are limited by `account_link.mattermost_code_requests`: the sixth request of one User and the fourth for one target account within an hour answer `429` with `Retry-After`."
  - "[C-18.FR-4, C-18.FR-5] A new link of the same User in the same identity space replaces the previous one; linking an account linked to another User is refused — `409` `account_linked_elsewhere` in the API, \"This account is linked to another user. Its owner or an admin can unlink it.\" from the Telegram bot — and recorded as `link_rejected` with the reason `conflict`."
  - "[C-18.FR-10] `listLinkableConnections` returns every Connection that is not deleted with only `id`, `name` and `messenger`, needs no Permission, and gives a Service account token `403` `service_account_not_allowed`."
  - "[C-18.FR-6, C-18.FR-10, C-18.AC-4, C-03.AC-14] Users list their links with the messenger, Connection, username and last use and unlink them from the web session; with any API token, an Admin's included, starting, confirming and removing one's own link answer `403` `session_required`; Admins list and delete a User's links with `users:read` and `users:write`, and `POST /users/{id}/account-links` answers `405` `method-not-allowed` with `Allow: GET`."
  - "[C-18.FR-8, C-18.AC-3, C-10.FR-11] After linking, a press runs the command as that User with the Transport of the messenger and updates the link's last use; presses from unlinked accounts change nothing and get the \"not linked\" answer with the profile link."
  - "[C-18.AC-5, C-17.FR-10] After linking, pressing \"Still on it\" on a Reminder records `reminder_answered` for that User and pressing \"Unack\" unacknowledges the Alert Group."
  - "[C-18.AC-6] A Viewer's press gets \"You are not permitted to do this\" and a disabled User's press \"Your Muster account is disabled\"; neither changes anything."
  - "[C-18.FR-8, C-03.FR-13] Deleting a User removes their links and their pending link requests, each removal recorded as `account_link.unlink`."
  - "[C-18.FR-9, C-03.FR-14] The Audit log records `account_link.link`, `account_link.unlink` (by whom, from where) and `account_link.link_rejected` (conflict, expired or used token, wrong code) with the User, the messenger, the external id and username, and the Connection."
  - "[C-18.AC-7, C-12.FR-12] After Alice links the Telegram account `alice_t` and no Mattermost account, an Alert Group she acknowledged shows \"Acknowledged by alice_t\" — the username without `@` — in the Telegram Root message and \"Acknowledged by Alice Smith\" in the Mattermost one, and a Telegram Destination that mentions her for new Alert Groups mentions her with a `tg://user?id=7001` link whose text is her display name \"Alice Smith\"."
verify: "make ci test-integration e2e"
operator_attention: false
issue: 51
---

# S-051. Account links: Telegram deep links, Mattermost codes, press attribution and Audit log (BE)

## Scope

**IN**

- Starting a Telegram link from the profile and finishing it with `/start <token>` to the bot.
- Starting a Mattermost link with a username and finishing it with the code from the bot's direct message, with the
  attempt and request limits.
- The linking rules: one account per User per identity space, conflicts, replacement, unlinking by the User and by
  Admins, removal when a User is deleted.
- Press attribution with links made through the profile: last use, Viewers, disabled Users, Reminder buttons.
- The Audit log entries, the `account-links` hint, the documentation page.

**OUT**

- The pages: Messenger accounts in the profile and Account links on the user page (S-052).
- Mattermost OAuth, Telegram Login and linking by email (not in L1).

## Contracts

- **Operations implemented**: `listMyAccountLinks` and `listLinkableConnections` (any signed-in User; a Service account
  gets `403` `service_account_not_allowed`), `deleteMyAccountLink`, `startTelegramLink`, `startMattermostLink`,
  `confirmMattermostLink` (web session with its CSRF token only: a token gets `403` `session_required`, C-03.FR-27),
  `listUserAccountLinks` (`users:read`), `deleteUserAccountLink` (`users:write`; tokens allowed, except on the caller's
  own link, which a token removes with `403` `session_required` as under `/me`, C-03.AC-14). No operation creates a
  link for another User; `/users/{id}/account-links` has no `POST`, which the router answers with `405` (below). The
  eight operations join the `implemented` map of `internal/api/server.go`. Schemas:
  `AccountLink(List)`, `LinkableConnection(List)`, `Messenger`, `TelegramLinkRequest`, `TelegramLinkStart`,
  `MattermostLinkRequest`, `MattermostLinkStarted`, `MattermostLinkConfirm`; hint `account-links`.
- **Method not allowed** (`internal/api/middleware.go`, `api/openapi.yaml`): today every `FindRoute` error of the
  router becomes `404`. The router's `routers.ErrMethodNotAllowed` — a path of the specification without the request's
  method — answers `405` with the new problem type `method-not-allowed` (added to `x-problem-types` of the
  specification) and an `Allow` header listing the methods of that path; every other routing error stays `404`
  `not-found`; the `Allow` header lists only the methods the app listener serves the path with, and a path it serves
  with none stays `404`. `TestNotFoundAndNotImplemented` (`server_test.go`) moves `PATCH /api/v1/me` from its `404`
  cases to a `405` case with `Allow: GET, PUT` (the path has `getMe` and `updateMe`); the title of the problem type is
  "Method not allowed" (`internal/api/problem.go`).
- **Validation answers**: a `connection_id` that is not a live Connection of the right type answers `422` `unknown_id`
  at `/connection_id` in `startTelegramLink` and `startMattermostLink`; `link_code_invalid` (a wrong, expired or void
  code, never saying which) and `messenger_user_not_found` (no such username on the Mattermost server) are the
  `x-problem-codes` of the Mattermost operations below.
- **Tables**: `account_links`, `account_link_requests` (`secret_hash`, `target_external_id`, `target_username`,
  `attempts`, `expires_at`, `consumed_at`, `voided_at`), `users`, `audit_log`. The story adds
  `account_link_requests` to the `short_lived_pruning` Leader task (a `PruneAccountLinks` field of `leader.Work` and
  the table in `metrics.ShortLivedTables`): a request is deleted once its `expires_at` is
  `account_link.request_prune_after` (24 h) past, consumed and voided ones included, which keeps the last hour that
  `account_link.mattermost_code_requests` counts and answers a late `/start` with `token_expired` for a day. No
  migration is needed: the existing indexes serve the hourly counts, and the batched prune scans the small table
  (`0007` is taken by S-049, and no index is added); `schema.md` notes the pruning.
- **Telegram** (C-18.FR-2, C-14.FR-8; `accountlinks/telegram.go`, `telegram/start.go`, ADR-0013):
  - `startTelegramLink {connection_id}`: a Telegram Connection that is not deleted. The token is 32 bytes from
    `crypto/rand` in base64url without padding (43 characters); its SHA-256 is stored with the User, the Connection
    and `expires_at` = now + `account_link.telegram_token_ttl`. Answer `201` with `deep_link`
    `https://t.me/<bot_username>?start=<token>` and `expires_at`; the bot's username comes from the last Connection
    check, or from `getMe` through the interactive path when none is stored (`503` `Limited` when no token is free,
    `409` `conflict` when Telegram refuses the call — the `409` is added to the operation in the specification).
  - The update router of S-041 hands private messages to `telegram.Start`, which runs, like every update, under the
    update lock of its Connection (`db.TelegramUpdateLockClass`, `0x6d75_0004`, S-067) and within
    `telegram.parallel_updates` (P-52). A text other than `/start <token>` is ignored. The token is looked up by its
    hash alone (the unique partial index); it must be unconsumed, not void, not expired and bound to the Connection
    whose bot received it, else `account_link.link_rejected` with the reason `token_unknown`, `token_used`,
    `token_expired` or `wrong_connection`, and the bot replies "This link is invalid or expired. Start again from your
    Muster profile." (reference.md), the same text for every reason, with `sendMessage` to the private chat through
    `delivery.Interactive` on the Connection's limiter. `sendMessage` is unexported in `internal/telegram/client.go` and
    only the adapter sends (lint 3), so the adapter gains a send to a private chat and
    `internal/delivery/interactive.go` a new Op, `DirectOp`, for a bot's message to one person — the replies to `/start`
    and the Mattermost direct messages below — that changes no Root message and no Thread reply.
  - If the Telegram account (`from.id`) is linked to another User: `link_rejected` with `conflict` and the reply "This
    account is linked to another user. Its owner or an admin can unlink it." Otherwise, in one transaction: the User's
    previous Telegram link is deleted (`account_link.unlink` with `replaced`), the link is inserted (`external_id`
    `from.id`, `username` `from.username`) — or, when the account is already linked to this User, the link is kept with
    its id and takes the new username and Connection, nothing replaced — the request is consumed, `account_link.link`
    (with what changed) is recorded, and the
    `account-links` hint goes to that User; after the commit the bot replies "Linked to Muster user {display name}
    ({login})" with `sendMessage` to the private chat through `delivery.Interactive` on the Connection's limiter.
- **Mattermost** (C-18.FR-3; `accountlinks/mattermost.go`, `mattermost/client.go`):
  - `startMattermostLink {connection_id, username}`: a leading `@` is dropped. The limits of
    `account_link.mattermost_code_requests` are counted over `account_link_requests` of the last hour — per requesting
    User, and per Connection and target account — and refused with `429` `rate-limited` and `Retry-After` until the
    oldest counted request is an hour old. Both are counted in the transaction that stores the request, under the lock
    of the User's row and a transaction advisory lock of the target account (`db.AccountLinkCodeTargetLockClass`,
    `0x6d75_0005`), so that concurrent requests cannot pass them; the User's count is also checked before the lookup,
    which spends no messenger call on a refused request. Through the interactive path: `GET /api/v4/users/username/{username}` (`404`
    → `422` `messenger_user_not_found` at `/username`). An account linked to another User in this Connection's identity
    space answers `409` `account_linked_elsewhere` and records `link_rejected` (`conflict`). Otherwise the User's
    earlier pending Mattermost requests for this Connection are voided, a code of `account_link.mattermost_code` — 8
    characters from `23456789ABCDEFGHJKMNPQRSTUVWXYZ`, valid 10 minutes — is stored as its SHA-256, and the bot opens
    the direct channel (`POST /api/v4/channels/direct` with the bot's and the user's ids, F-028; the bot's id is
    `connections.bot_user_id`, learned by the Connection check of S-039, or `GET /api/v4/users/me` through the
    interactive path when none is stored) and posts "You requested a link to Muster. Do not share this code." with
    the code, both through `DirectOp`. Answer `202` with `request_id`, `expires_at` and
    `attempts_remaining` (5). The limiter token is taken before anything is stored: `503` `Limited` changes nothing.
  - `confirmMattermostLink {request_id, code}`: a request of another User is `404`. A void, consumed or expired request,
    and a wrong code, answer `422` `link_code_invalid` at `/code` and record `link_rejected` (`wrong_code` or
    `token_expired`); a wrong code increments `attempts`, and the fifth wrong one voids the request, so a sixth attempt is
    refused even with the right code (C-18.AC-2). A right code links as for Telegram — conflict check, replacement,
    `link`, the hint — consumes the request and answers `201` with the `AccountLink`; the bot then sends "Linked to
    Muster user {display name}" in the direct channel. The limiter token is taken before the code is checked: `503`
    counts no attempt.
- **Rules** (C-18.FR-4 – FR-7, ADR-0013): the unique constraints of `account_links` carry one account per User per
  identity space and one User per account; nothing moves silently; linking needs no re-authentication and no TOTP
  beyond the signed-in session. A concurrent link of the same account by another User resolves to the same conflict:
  the insert runs in a savepoint and a violation of `account_links_account_key` is `conflict`, the request untouched.
- **Lists and removal** (C-18.FR-6, FR-10): `listMyAccountLinks` and `listUserAccountLinks` return `messenger`,
  `connection` (id and name), `external_id`, `username`, `last_used_at`, `created_at`. `listLinkableConnections` returns
  every Connection that is not deleted with only `id`, `name` and `messenger`, ordered by messenger and name, and needs
  no Permission, so a Role without `connections:read` can still pick a Connection in the profile. `deleteMyAccountLink` and
  `deleteUserAccountLink` delete the link, record `account_link.unlink` with the actor, the Transport and the client
  address, and send the hint to the link's User. Nothing unlinks from chat.
- **Presses** (C-18.FR-8, C-10.FR-11; `accountlinks/lookup.go`): `Lookup(identity space, external id)` returns the User
  with its status and sets `last_used_at` at most once a minute. The press handlers of S-061 and S-067 already answer: no
  link — the "not linked" text with `{MUSTER_PUBLIC_URL}/profile`; a disabled User — "Your Muster account is disabled",
  without running the command; a missing Permission from the dispatcher — "You are not permitted to do this". Deleted
  Users have no links.
- **Deleting a User** (C-03.FR-13; `users/admin.go`): in the same transaction, the User's links are deleted, each with
  `account_link.unlink` (`reason` `user_deleted`), and their pending requests are voided.
- **Audit log** (C-18.FR-9, C-03.FR-14): `account_link.link`, `account_link.unlink`, `account_link.link_rejected`, each
  with `details` `{messenger, external_id, username, connection, reason, replaced}`; the resource is the link (or the
  Connection for a rejection). A rejection that comes from a messenger has the actor `system` with the Transport
  `telegram` or `mattermost` and names the User when the token or request is known.
- **Hint** (`internal/db/notify.go`, `internal/live/hub.go`, `internal/api/live.go`): `account-links` with a null id
  is delivered only to the streams of the link's User. Neither `db.Hint` nor `live.Subscriber` carries a User today:
  `db.Hint` gains an optional `user` (the User's `public_id`) and `live.Subscriber` the User of the stream, set by the
  live-updates handler; the hub delivers a hint with a `user` only to that User's streams. S-052 relies on it.
- **Secrets** (lint 5): link tokens and codes are hashed before storage and pushed through the handlers and the log by a
  probe; neither appears in a log line or an error.
- **Fake servers** (C-01.FR-13): Mattermost gains the user `dana` (`u-dana`), `GET /api/v4/users/username/{username}`
  and the listing of direct-channel posts by `GET /_fake/dms` (`user_id`, `post_id`, `message`, `create_at`), which
  `GET /_fake/posts` leaves out; it serves `POST /api/v4/channels/direct` since S-039. Telegram
  records `sendMessage` to private chats under `GET /_fake/messages?chat=<user id>` and gains `POST /_fake/private`
  `{token, from, text}`, which queues a private message update for the bot.
- **Documentation** (`docs/messengers/account-links.md`): why links start in the profile, linking Telegram and
  Mattermost step by step, what the bot answers to an invalid or expired link, unlinking, what Admins can do, and the
  answers to unlinked, Viewer and disabled presses.
- **Log events**: `account_link_linked` (INFO: `messenger`, `connection`, `user`, and `error` when the bot's
  confirmation failed), `account_link_rejected` (INFO: `messenger`, `connection`, `reason`, and `error` when the bot's
  answer failed), `account_link_unlinked` (INFO: `messenger`, `connection`, `user`, `reason` — `removed`, `replaced` or
  `user_deleted`).
- **Texts** (`internal/messages/texts`): the bot's messages during linking exist in English and Russian and follow the
  User's profile language, English without one; a rejection whose User is unknown is in English.
- **Defaults**: `account_link.telegram_token_ttl`, `account_link.mattermost_code`,
  `account_link.mattermost_code_requests`, `account_link.request_prune_after`, `delivery.interactive_budget`,
  `telegram.parallel_updates`.

## Steps

1. Extend both fake servers. Check: their tests cover the username lookup, direct messages, private updates and
   replies.
2. Write the Telegram path with its rejections. Check: `telegram_test.go` covers C-18.AC-1, the conflict, the
   replacement and the wrong Connection.
3. Write the Mattermost path with the attempt and request limits. Check: `mattermost_test.go` covers C-18.AC-2 and both
   limits with a manual clock.
4. Write the API, the lists — the Connections to link through included — unlinking, the Audit log, the hint with its
   User target and the removal on user deletion. Check: `accountlinks_test.go`, `api/accountlinks_test.go` (C-18.AC-4,
   C-03.AC-14), a hub test that a hint with a User reaches only that User's streams, and `users/admin_test.go`.
5. Answer `405` for a known path without the method. Check: `TestNotFoundAndNotImplemented` with the `405` case and its
   `Allow` header.
6. Write the documentation, the secret probe and the end-to-end test. Check: Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-049: the Admin's session (`jar`, `H`) and Personal access token A; the Responder "bob" (session `HB`, jar
# `bob`); the Mattermost Connection "mm" (C) with the Destination "alerts" (D); the demo Telegram Connection "Dev
# Telegram" (T) with the Destination "alerts" (TD, discussion group GR); the Route "t" with both Destinations and the
# Integration "lab" with FIRE, ADV and AG. New here: the Responder "Alice Smith" (login alice, session `HA`, jar `alice`)
# and the Viewer "dana" (session `HD`, jar `dana`), created and signed in as in S-011. No link rows are inserted by hand.
API=localhost:8080/api/v1; FMM=127.0.0.1:18065/_fake; FTG=127.0.0.1:18081/_fake; CH=-1001000000001; GR=-1001000000002
PRIV() { curl -s -X POST $FTG/private -d "{\"token\":\"123456:dev-telegram-token\",\"from\":{\"id\":$1,\"username\":\"$2\"},\"text\":\"$3\"}" > /dev/null; sleep 2; }
REJ() { curl -s -b jar "$API/audit-log?action=account_link.link_rejected&limit=1" | jq -c '.items[0].details | {reason, messenger, external_id}'; }
I0=$(curl -s localhost:8082/metrics | grep 'muster_client_requests_total{client="interactive",outcome="ok"}' | awk '{print $2}')

# C-18.FR-2, C-14.FR-8, C-18.AC-1: Telegram deep link, /start, the hint, then the same token again
curl -sN -b alice $API/live-updates > hints.txt & HP=$!
L=$(curl -s "${HA[@]}" $API/me/account-links/telegram -d "{\"connection_id\":\"$T\"}")
jq -r '.deep_link | test("^https://t\\.me/muster_dev_bot\\?start=[A-Za-z0-9_-]{43}$")' <<<"$L"   # true
TOK=$(jq -r '.deep_link | sub(".*start="; "")' <<<"$L")
PRIV 7001 alice_t "/start $TOK"
curl -s "$FTG/messages?chat=7001" | jq -r '.[-1].text'      # Linked to Muster user Alice Smith (alice)
kill $HP; grep -c '"type":"account-links"' hints.txt        # 1
curl -s -b alice $API/me/account-links | jq -c '[.items[] | {messenger, username, c: .connection.name}]'
# [{"messenger":"telegram","username":"alice_t","c":"Dev Telegram"}]
PRIV 7002 mallory "/start $TOK"; curl -s "$FTG/messages?chat=7002" | jq -r '.[-1].text'
# This link is invalid or expired. Start again from your Muster profile.
REJ                                                         # {"reason":"token_used","messenger":"telegram","external_id":"7002"}
curl -s "${HA[@]}" $API/me/account-links/telegram -d "{\"connection_id\":\"$C\"}" | jq -c '[.status, .errors[0].code, .errors[0].pointer]'
# [422,"unknown_id","/connection_id"]                       (a Mattermost Connection)

# C-18.FR-3, C-18.AC-2: Mattermost code; five wrong codes void the request
S=$(curl -s "${HB[@]}" $API/me/account-links/mattermost -d "{\"connection_id\":\"$C\",\"username\":\"@bob\"}"); jq .attempts_remaining <<<"$S"   # 5
curl -s $FMM/dms | jq -c '.[-1] | {u: .user_id, w: (.message | test("You requested a link to Muster. Do not share this code."))}'   # {"u":"u-bob","w":true}
CODE=$(curl -s $FMM/dms | jq -r '.[-1].message | capture("(?<c>[2-9A-HJKMNP-Z]{8})").c'); RID=$(jq -r .request_id <<<"$S")
curl -s "${HB[@]}" $API/me/account-links/mattermost -d "{\"connection_id\":\"$C\",\"username\":\"nobody\"}" | jq -c '[.status, .errors[0].code, .errors[0].pointer]'
# [422,"messenger_user_not_found","/username"]
CONF() { curl -s "${HB[@]}" $API/me/account-links/mattermost/confirmation -d "{\"request_id\":\"$1\",\"code\":\"$2\"}"; }
for i in 1 2 3 4 5; do CONF $RID 22222222 | jq -r '.errors[0].code'; done | uniq -c   # 5 link_code_invalid
CONF $RID $CODE | jq -r '.errors[0].code'                    # link_code_invalid
REJ                                                         # {"reason":"wrong_code","messenger":"mattermost","external_id":"u-bob"}
S=$(curl -s "${HB[@]}" $API/me/account-links/mattermost -d "{\"connection_id\":\"$C\",\"username\":\"bob\"}")
CODE=$(curl -s $FMM/dms | jq -r '.[-1].message | capture("(?<c>[2-9A-HJKMNP-Z]{8})").c')
CONF $(jq -r .request_id <<<"$S") $CODE | jq -c '{messenger, external_id, username}'   # {"messenger":"mattermost","external_id":"u-bob","username":"bob"}
curl -s $FMM/dms | jq -r '.[-1].message' | grep -c '^Linked to Muster user'               # 1
curl -s localhost:8082/metrics | grep 'muster_client_requests_total{client="interactive",outcome="ok"}' | awk -v i=$I0 '{print ($2 - i) >= 7}'   # 1

# C-18.FR-5: an account linked to another User; C-18.FR-3: the limit per target account
curl -s "${HA[@]}" $API/me/account-links/mattermost -d "{\"connection_id\":\"$C\",\"username\":\"bob\"}" | jq -r .code   # account_linked_elsewhere
REJ                                                         # {"reason":"conflict","messenger":"mattermost","external_id":"u-bob"}
for i in 1 2 3; do curl -s -o /dev/null -w '%{http_code} ' "${HD[@]}" $API/me/account-links/mattermost -d "{\"connection_id\":\"$C\",\"username\":\"dana\"}"; done
curl -s -D - -o /dev/null "${HD[@]}" $API/me/account-links/mattermost -d "{\"connection_id\":\"$C\",\"username\":\"dana\"}" | grep -iE '^(HTTP|retry-after)'
# 202 202 202 HTTP/1.1 429 Too Many Requests / Retry-After: 3600 (or a few seconds less, as the clock moved)

# C-18.FR-10: the Connections to link through show only id, name and messenger
curl -s -b dana $API/me/account-links/connections | jq -c '[.items[] | keys] | unique'      # [["id","messenger","name"]]
curl -s -b dana $API/me/account-links/connections | jq -c '[.items[] | select(.name == "mm" or .name == "Dev Telegram") | .messenger]'
# ["mattermost","telegram"]

# C-18.AC-4, C-03.AC-14: tokens cannot start, confirm or remove one's own link; Admins list and remove, never create
curl -s "${A[@]}" $API/me/account-links/telegram -d "{\"connection_id\":\"$T\"}" | jq -c '[.status, .code]'   # [403,"session_required"]
AP=$(curl -s "${HA[@]}" $API/me/personal-access-tokens -d '{"name":"cli","permissions":["alert-groups:read"]}' | jq -r .value)
LID=$(curl -s -b alice $API/me/account-links | jq -r '.items[0].id')
curl -s -H "Authorization: Bearer $AP" -X DELETE $API/me/account-links/$LID | jq -c '[.status, .code]'   # [403,"session_required"]
ADM=$(curl -s "${A[@]}" $API/me | jq -r .user.id)
curl -s "${A[@]}" -X DELETE $API/users/$ADM/account-links/$LID | jq -c '[.status, .code]'   # [403,"session_required"]
BOB=$(curl -s "${A[@]}" "$API/users?q=bob" | jq -r '.items[0].id')
curl -s "${A[@]}" $API/users/$BOB/account-links | jq -c '[.items[].messenger]'          # ["mattermost"]
curl -s -D - "${A[@]}" -X POST $API/users/$BOB/account-links -d '{}' | grep -iE '^(HTTP|allow)|method-not-allowed' | tr -d '\r'
# HTTP/1.1 405 Method Not Allowed / Allow: GET / … "type":"https://muster-io.github.io/muster/problems/method-not-allowed" …

# C-18.AC-3, C-10.FR-11: presses run as the linked User; last use is set
G=$(FIRE l1 LinkTest t); ROOT=$(curl -s $FMM/posts | jq -r --arg g "$G" '.[] | select(.root_id == "" and (.props.attachments[0].title_link | test($g))) | .id')
curl -s -X POST $FMM/press -d "{\"post_id\":\"$ROOT\",\"action_id\":\"ack\",\"user_id\":\"u-bob\"}" > /dev/null; sleep 1
curl -s -b jar "$API/alert-groups/$G/timeline?limit=1" | jq -c '.items[0] | {event, t: .actor.transport, u: .actor.name}'   # {"event":"acknowledged","t":"mattermost","u":"bob"}
curl -s -b bob $API/me/account-links | jq -r '.items[0].last_used_at != null'            # true

# C-18.AC-5: Reminder buttons for the linked User
ADV 14400; RP=$(curl -s $FMM/posts | jq -r --arg r "$ROOT" '[.[] | select(.root_id == $r and ((.props.attachments[0].actions // []) | length) == 2)] | last | .id')
curl -s -X POST $FMM/press -d "{\"post_id\":\"$RP\",\"action_id\":\"stillonit\",\"user_id\":\"u-bob\"}" > /dev/null; sleep 1
curl -s -b jar "$API/alert-groups/$G/timeline?kind=timers&limit=1" | jq -c '.items[0] | {event, u: .actor.name}'   # {"event":"reminder_answered","u":"bob"}
curl -s -X POST $FMM/press -d "{\"post_id\":\"$RP\",\"action_id\":\"unack\",\"user_id\":\"u-bob\"}" > /dev/null; sleep 1
curl -s -b jar $API/alert-groups/$G | jq -r .status          # firing

# C-18.AC-6: a Viewer's press and a disabled User's press (an hour later, past dana's request limit above)
ADV 3600
S=$(curl -s "${HD[@]}" $API/me/account-links/mattermost -d "{\"connection_id\":\"$C\",\"username\":\"dana\"}")
CODE=$(curl -s $FMM/dms | jq -r '.[-1].message | capture("(?<c>[2-9A-HJKMNP-Z]{8})").c')
curl -s "${HD[@]}" $API/me/account-links/mattermost/confirmation -d "{\"request_id\":\"$(jq -r .request_id <<<"$S")\",\"code\":\"$CODE\"}" > /dev/null
curl -s -X POST $FMM/press -d "{\"post_id\":\"$ROOT\",\"action_id\":\"ack\",\"user_id\":\"u-dana\"}" > /dev/null; sleep 1
curl -s $FMM/ephemeral | jq -r '.[-1].message'               # You are not permitted to do this
curl -s "${H[@]}" -X POST $API/users/$BOB/disable > /dev/null
curl -s -X POST $FMM/press -d "{\"post_id\":\"$ROOT\",\"action_id\":\"ack\",\"user_id\":\"u-bob\"}" > /dev/null; sleep 1
curl -s $FMM/ephemeral | jq -r '.[-1].message'               # Your Muster account is disabled
curl -s -b jar $API/alert-groups/$G | jq -r .status          # firing
curl -s "${H[@]}" -X POST $API/users/$BOB/enable > /dev/null

# C-18.AC-7, C-12.FR-12: in Telegram Alice is mentioned by a tg://user link with her display name and named alice_t in
# the footer; in Mattermost, without a link there, she is "Alice Smith"
ALICE=$(curl -s "${A[@]}" "$API/users?q=alice" | jq -r '.items[0].id')
curl -s "${H[@]}" -X PUT -H "If-Match: $(curl -s -b jar $API/destinations/$TD | jq -r .etag)" $API/destinations/$TD \
  -d "$(curl -s -b jar $API/destinations/$TD | jq -c --arg a "$ALICE" '{type, name, connection_id, channel_id, limiter,
       mentions: (.mentions | .new_alert_group = {everyone: "none", user_ids: [$a], groups: []})}')" > /dev/null
G2=$(FIRE l2 AliceTest t); sleep 1
curl -s "$FTG/messages?chat=$CH" | jq -r '.[-1].text' | grep -c 'tg://user?id=7001">Alice Smith</a>'   # 1   (the Mention of the new Alert Group)
curl -s "${HA[@]}" -X POST $API/alert-groups/$G2/acknowledge > /dev/null; sleep 2
curl -s "$FTG/messages?chat=$CH" | jq -r '.[-1].text' | grep -cE 'Acknowledged by alice_t($|[^A-Za-z0-9_])'   # 1
curl -s $FMM/posts | jq -r --arg g "$G2" '.[] | select(.root_id == "" and (.props.attachments[0].title_link | test($g))) | .props.attachments[0].text' | grep -c 'Acknowledged by Alice Smith'   # 1

# C-18.FR-6, C-03.FR-13, C-18.FR-9: unlinking by the User, removal by an Admin, removal on deletion
curl -s -o /dev/null -w '%{http_code}\n' "${HA[@]}" -X DELETE $API/me/account-links/$LID                     # 204
DANA=$(curl -s "${A[@]}" "$API/users?q=dana" | jq -r '.items[0].id')
curl -s "${H[@]}" -X DELETE $API/users/$DANA > /dev/null
curl -s "${A[@]}" $API/users/$DANA/account-links | jq '.items | length'                  # 0
curl -s -b jar "$API/audit-log?action=account_link.unlink&limit=2" | jq -c '[.items[] | .details.reason]'   # ["user_deleted",null]
grep -c -e "$TOK" -e "$CODE" dev.log                                                   # 0
```

`test/e2e/account_links_test.go` repeats these steps and adds: an expired token (`token_expired`, with the development
clock), a token sent to another Telegram Connection's bot (`wrong_connection`), the per-User limit of code requests,
the replacement of a User's earlier Telegram link, an Admin removing a link through `deleteUserAccountLink` with
`account_link.unlink` naming the Admin, a Telegram press by a linked Viewer, and C-18.AC-3 through Telegram.

**Optional manual check against real servers** (the operator's test bot and test Mattermost 11.2.2): link a Telegram
account through the deep link on a phone and a Mattermost account through the code, then press Ack in both and see the
press attributed in the Timeline.

## Open questions

None.

## Notes

- Suggested commit: `feat(accountlinks): link messenger accounts from the profile and attribute presses`.
- The answers to Viewers and disabled Users exist since S-061 and S-067 and are verified here for the first time with
  links made through the profile; if one does not hold, its fix belongs in this pull request.
- The Reminder presses of C-18.AC-5 need the buttons of S-068, hence the dependency (D298).
- How Telegram shows a user (D298): a Mention is a `tg://user?id=` link whose text is the display name, and the footer
  names the user by the bare username, as S-042 renders them; C-18.AC-7 and C-12.FR-12 say so.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-18.FR-1 | full | |
| C-18.FR-2 | partial | the API and the bot; the profile is S-052 |
| C-18.FR-3 | partial | the API and the bot; the profile is S-052 |
| C-18.FR-4 | full | |
| C-18.FR-5 | full | |
| C-18.FR-6 | partial | the API; the profile and the user page are S-052 |
| C-18.FR-7 | full | |
| C-18.FR-8 | full | |
| C-18.FR-9 | full | |
| C-18.FR-10 | partial | the lists in the API, the Connections to link through included; the profile is S-052 |
| C-18.FR-11 | full | |
| C-18.AC-1 | full | |
| C-18.AC-2 | full | |
| C-18.AC-3 | full | |
| C-18.AC-4 | full | |
| C-18.AC-5 | full | |
| C-18.AC-6 | full | |
| C-18.AC-7 | full | |
| C-14.FR-8 | full | together with S-041: the bot answers `/start <token>` |
| C-10.FR-11 | full | together with S-032, S-061 and S-067: refusals of presses without a link, from disabled Users and from Viewers |
| C-03.AC-14 | full | together with S-016: removing an Account link with a token |
| C-03.FR-13 | partial | links removed when a User is deleted |
| C-03.FR-14 | partial | the Account link entry types |
| C-12.FR-12 | partial | checked with real Account links (C-18.AC-7) |
| C-17.FR-10 | partial | Reminder presses from links made through the profile |
