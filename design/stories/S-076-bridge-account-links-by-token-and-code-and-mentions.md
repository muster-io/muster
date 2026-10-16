---
id: S-076
title: "Bridge Account links by a token sent to the bot or a code by direct message, and Mentions of linked people (BE)"
capability: C-22
kind: be
layer: L1
depends_on: [S-075]
covers: [C-22.FR-13, C-22.FR-15, C-22.FR-16, C-22.FR-23, C-22.AC-18, C-22.AC-19, C-22.AC-20]
files_touched:
  - api/openapi.yaml
  - internal/db/migrations/0012_bridge_account_link_requests.up.sql
  - internal/db/migrations/0012_bridge_account_link_requests.down.sql
  - design/db/schema.md
  - internal/accountlinks/accountlinks.go
  - internal/accountlinks/bridge.go
  - internal/accountlinks/bridge_test.go
  - internal/accountlinks/lookup.go
  - internal/accountlinks/query.sql
  - internal/accountlinks/live_test.go
  - internal/bridge/links.go
  - internal/bridge/links_test.go
  - internal/bridge/client.go
  - internal/bridge/envelope.go
  - internal/bridge/envelope_test.go
  - internal/mentions/mentions.go
  - internal/mentions/mentions_test.go
  - internal/mentions/query.sql
  - internal/api/accountlinks.go
  - internal/api/accountlinks_test.go
  - internal/api/problem.go
  - internal/api/server.go
  - internal/messages/texts/en.json
  - internal/messages/texts/ru.json
  - internal/runtime/runtime.go
  - internal/fakes/fakebridge/people.go
  - internal/fakes/fakebridge/fakebridge_test.go
  - internal/logging/events.go
  - internal/archlint/secretleak.go
  - docs/messengers/account-links.md
  - test/e2e/account_links_test.go
  - test/e2e/several_connections_test.go
acceptance:
  - "[C-22.FR-15, C-22.AC-18] `startBridgeLink` for a Bridge Connection returns a single-use token of 43 base64url characters, valid for `account_link.bridge_token_ttl`, and `link_url` built from the bridge's `link_url_template` when it is an `http` or `https` URL (null without one); it accepts the web session only (403 `session_required` with an API token); the operation is in the implemented operations of `internal/api/server.go`, so it no longer answers 501."
  - "[C-22.FR-15, C-22.FR-13, C-22.AC-18] When the fake bridge reports an `account_link` event with that token, the sender's account is linked to the User — `external_id`, `display_name` and the handle the bridge reported — and the event is answered \"Linked to Muster user {name} ({login})\" in the User's profile language; the same token again, an expired one and a token of another Connection link nothing, are answered \"This link is invalid or expired. Start again from your Muster profile.\", in English, and are recorded as `account_link.link_rejected`."
  - "[C-22.FR-15, C-22.AC-19] With `user_lookup` `email` and `direct_messages.bot_can_start`, `startBridgeCodeLink` with a known address sends the code through `sendDirectMessage` with \"You requested a link to Muster. Do not share this code.\" and answers 202 with `request_id`, `expires_at` and `attempts_remaining` 5, and `confirmBridgeLink` with that `request_id` and the code links the account; a wrong code is refused with 422 `link_code_invalid` at `/code` and `attempts_remaining` one lower, and after five wrong codes the sixth attempt is refused even with the right code; an address the bridge does not find is refused with 422 `handle_not_found` at `/handle`, and one the bridge answers `forbidden` for when sending the code with 422 `direct_message_refused` at `/handle`; code requests follow `account_link.bridge_code_requests`, and one more is refused with 429 `link_code_requests_exceeded` and `retry_after_seconds`."
  - "[C-22.FR-15, C-22.AC-19] A Bridge Connection whose `info` has `user_lookup` `none`, or no `direct_messages` with `bot_can_start`, refuses a code request with 409 `code_not_offered`; `listLinkableConnections` now returns Bridge Connections, each with its link methods (`token`, and `code` where offered) and the lookup kind, and only `token` with no lookup kind while Muster has not read the Connection's `info`; `TestSeveralConnectionsRoute` lists the demo Connection \"Dev Bridge\" among them."
  - "[C-22.FR-15] Linking rules of C-18 hold for Bridges: one account per User per Bridge Connection, an account linked to another User refused, unlinking in the profile and by an Admin, the Audit log with the messenger `bridge`, and the Identity space `bridge:<connection>`; two Bridge Connections to the same bridge need two links; `AccountLink` returns `display_name`, an optional member like `LinkableConnection.link_methods` and `handle_kind`, so the SPA's fixtures of Mattermost and Telegram links still type-check and the key check of `account_links_test.go` admits them on Bridge rows only."
  - "[C-22.FR-16, C-22.AC-20] A Bridge Destination that mentions Alice for new Alert Groups posts a Loud Root message whose `mentions` hold `ref`, the `external_id` and the `display_name` of her link, and whose body starts with `[@Alice Smith](muster-mention:<ref>)`; Bob, without a link, is named as text with no marker; the footer of an Alert Group Alice acknowledged names her by her link's `display_name`, also when the link has no username."
  - "[C-22.FR-23] The page Messengers → Account links explains linking through a Bridge by token and by code, the lookup kind the bridge declares, and that Bridge links are per Connection; `make docs-check` passes."
verify: "make ci test-integration e2e docs-check"
operator_attention: false
issue: 234
---

# S-076. Bridge Account links by a token sent to the bot or a code by direct message, and Mentions of linked people (BE)

## Scope

**IN**

- Account links through Bridge Connections: the token sent to the bot, always; the code by direct message, where the
  bridge can look people up and start a direct chat; the rules, limits and Audit log of C-18.
- Handling `account_link` events and answering them; `findUsers` and `sendDirectMessage`.
- Bridge Connections in `listLinkableConnections`, which S-073 left out.
- Mentions of linked people in envelopes, and their names in footers.

**OUT**

- The profile and the user page (S-077); presses and Text commands, which already map accounts through these links
  (S-075).
- Interim states until S-077: the profile offers no "Link through" entry for a Bridge Connection, and the Account link
  tables of the SPA name a Bridge link's messenger "Mattermost"; a link made through the API shows in them with that
  label.

## Contracts

- **Spec** (`api/openapi.yaml`): `startBridgeLink` (`POST /me/account-links/bridge`, `{connection_id}` →
  `{token, link_url, expires_at}`), `startBridgeCodeLink` (`POST /me/account-links/bridge/codes`,
  `{connection_id, handle}` → `202 {request_id, expires_at, attempts_remaining}`, the members of
  `MattermostLinkStarted`) and `confirmBridgeLink` (`POST /me/account-links/bridge/confirmation`, `{request_id, code}`
  → `201 AccountLink`), web session only (C-03.FR-27), named like `startTelegramLink`, `startMattermostLink` and
  `confirmMattermostLink`, whose code flow they mirror; `Messenger`, `AccountLink.messenger` and
  `LinkableConnection.messenger` gain `bridge`; `AccountLink` gains `display_name`; `LinkableConnection` gains, for a
  Bridge, `link_methods` (`token`, `code`) and `handle_kind` (`email`, `login`, `username`, `user_id`, or null); the
  three new members are optional, not required, so the SPA's fixtures of the other messengers' links stay valid and
  the key check of `test/e2e/account_links_test.go` admits them on Bridge rows only. Problem codes: the new
  `handle_not_found` and `direct_message_refused` (422 at `/handle`) and `code_not_offered` (409); the existing
  `link_code_invalid` with `attempts_remaining` (422 at `/code`) and `link_code_requests_exceeded` (429 with
  `retry_after_seconds`), whose descriptions, and that of `attempts_remaining`, name `confirmBridgeLink`,
  `startBridgeCodeLink` and `account_link.bridge_code_requests` beside the Mattermost ones. The three operations join
  the implemented operations of `internal/api/server.go`.
- **Schema** (migration `0012_bridge_account_link_requests`, expand only, `schema.md`): `account_link_requests.messenger`
  accepts `bridge`, with the type-checked foreign key to `connections`; `account_link_requests` gains `method`
  (`token`, `code`), nullable, because old replicas write Telegram and Mattermost requests without it, with a check
  that a Bridge request has one and that a Bridge code names its `target_external_id`; a new partial unique index
  `account_link_requests_bridge_token_key` on `secret_hash` where `messenger = 'bridge' AND method = 'token'`, beside the
  Telegram one, which stays as it is so that old replicas keep their plans. The `account_links` columns came with S-075.
- **Linking** (`internal/accountlinks/bridge.go`): tokens as `account_link.telegram_token_ttl` does for Telegram, bound
  to the User and the Connection, found by their hash with the Connection checked; codes as for Mattermost, with
  `account_link.bridge_code` and `account_link.bridge_code_requests`; the conflict, replacement and unlink rules and the
  Audit log entries of S-051 with the messenger `bridge`; `account_link.request_prune_after` prunes Bridge requests
  too. `listLinkableConnections` drops S-073's filter; the link methods come from the Connection's stored `info` —
  only `token`, and no lookup kind, while `bridge_info` is null.
- **Events and calls** (`internal/bridge/links.go`, `internal/accountlinks/accountlinks.go`): `internal/accountlinks`
  declares the interface it consumes, `BridgeBot` (`FindUser`, `SendDirectMessage`, `Answer`), which `internal/bridge`
  implements, so neither package imports the other's internals; `account_link` events from S-075's handler link and are
  answered through `answerEvent`, in the profile language of the User the token is bound to, and in English when no
  User is found; `findUsers` and `sendDirectMessage` (key `dm-<request public_id>`) go through the interactive path
  (C-18.FR-11); a bridge answering `forbidden` to the direct message refuses the code request with
  `direct_message_refused`, which the SPA shows as "The bot cannot send you a direct message. Send it the token
  instead." A code request is a row of `account_link_requests` found by its `public_id` (`request_id`), as for
  Mattermost: its attempts count down on each wrong code, and a request without attempts left refuses the right code
  too. The link URL is `link_url_template` with the token in place of `{token}`, kept only when `messages.SafeURL`
  accepts the result.
- **Mentions and footers** (`internal/mentions`, `internal/accountlinks/lookup.go`, `internal/bridge/envelope.go`):
  `mentions.IdentitySpace("bridge", connection)` is `bridge:<connection>`; `ListMentionUsers` returns the link's
  `display_name`, and `ListFooterUsernames` names a linked User by `coalesce(username, display_name)`, so a Bridge link
  without a username still names its User; a Mention target of a User with a link for the Destination's Connection
  carries `external_id` and `display_name`; the envelope lists it in `mentions` with a `ref` (`m1`, `m2`, …) and puts
  the markers on the first line of the body of a Loud message; a User without a link is their display name as text.
  Footers name a linked User by `display_name` (C-12.FR-12).
- **Fake bridge** (`people.go`): `findUsers` by the kind in `/_fake/config` — `email` (`alice@example.org`,
  `bob@example.org`), `login` or `none`; `sendDirectMessage` recorded at `/_fake/direct-messages`, `forbidden` for
  users set through `/_fake/config` `dm_blocked`; `/_fake/link` `{user_id, token}`: a person sends the token to the
  bot, which queues an `account_link` event.
- **Log events**: the existing `account_link_linked`, `account_link_rejected` and `account_link_unlinked` with the
  messenger `bridge`.
- **Secrets** (lint 5): probe `bridge_links` pushes a link token and a code through the request, event and answer
  paths.
- **Defaults**: `account_link.bridge_token_ttl`, `account_link.bridge_code`, `account_link.bridge_code_requests`,
  `account_link.request_prune_after`.

## Steps

1. Add the migration and the linking rules. Check: `accountlinks/bridge_test.go`, `make test-integration`.
2. Handle `account_link` events and the code path through the bridge. Check: `bridge/links_test.go`.
3. Add the API operations and the linkable Bridge Connections. Check: `internal/api/accountlinks_test.go`.
4. Add Mentions and footers. Check: `mentions_test.go`, `envelope_test.go`.
5. Extend the fake, the probe, the page and the end-to-end tests. Check: Verification.

## Verification

```sh
make dev > dev.log 2>&1 &
# as in S-075: the session (Alice is the Admin "admin@example.org" here, renamed "Alice Smith", login "alice"), the
# Integration "lab", NOTIFY, ADV and AG, the Route to the Destination "alerts" (D) of the Connection "br" (C, poll mode)
API=localhost:8080/api/v1; FB=127.0.0.1:18094/_fake
curl -s -X PUT $FB/config -d '{"link_url_template":"http://127.0.0.1:18094/bot?start={token}","user_lookup":"email"}' > /dev/null
curl -s "${H[@]}" -X POST $API/connections/$C/checks > /dev/null      # reads info again
curl -s -b jar $API/me/account-links/connections | jq -c '[.items[] | select(.messenger == "bridge") | .name]'   # ["Dev Bridge","br"]
curl -s -b jar $API/me/account-links/connections | jq -c '.items[] | select(.name == "br") | {link_methods, handle_kind}'
# {"link_methods":["token","code"],"handle_kind":"email"}

# C-22.AC-18: a token sent to the bot
T=$(curl -s "${H[@]}" $API/me/account-links/bridge -d "{\"connection_id\":\"$C\"}")
jq -c '{l: (.token | length), u: (.link_url | startswith("http://127.0.0.1:18094/bot?start="))}' <<<"$T"   # {"l":43,"u":true}
curl -s -X POST $FB/link -d "{\"user_id\":\"u-alice\",\"token\":\"$(jq -r .token <<<"$T")\"}" > /dev/null; sleep 2
curl -s $FB/answers | jq -r '.[-1].text'                                    # Linked to Muster user Alice Smith (alice)
curl -s -b jar $API/me/account-links | jq -c '[.items[] | select(.messenger == "bridge") | {external_id, display_name}]'
# [{"external_id":"u-alice","display_name":"Alice Smith"}]
curl -s -X POST $FB/link -d "{\"user_id\":\"u-alice\",\"token\":\"$(jq -r .token <<<"$T")\"}" > /dev/null; sleep 2
curl -s $FB/answers | jq -r '.[-1].text'           # This link is invalid or expired. Start again from your Muster profile.
curl -s -b jar "$API/audit-log?action=account_link.link_rejected&limit=1" | jq -r '.items[0].details.messenger'   # bridge
curl -s -X PUT $FB/config -d '{"link_url_template":"ftp://127.0.0.1/bot?start={token}"}' > /dev/null
curl -s "${H[@]}" -X POST $API/connections/$C/checks | jq -r '.steps[0].ok'   # false: info does not match the protocol
curl -s -X PUT $FB/config -d '{"link_url_template":"http://127.0.0.1:18094/bot?start={token}"}' > /dev/null
curl -s "${H[@]}" -X POST $API/connections/$C/checks > /dev/null

# C-22.AC-19: a code by direct message, for bob (session "jarb")
curl -s -b jarb -H "X-CSRF-Token: $(cat csrfb)" $API/me/account-links/bridge/codes \
  -d "{\"connection_id\":\"$C\",\"handle\":\"nobody@example.org\"}" | jq -c '[.status, .errors[0].code]'   # [422,"handle_not_found"]
curl -s -X PUT $FB/config -d '{"dm_blocked":["u-bob"]}' > /dev/null
curl -s -b jarb -H "X-CSRF-Token: $(cat csrfb)" $API/me/account-links/bridge/codes \
  -d "{\"connection_id\":\"$C\",\"handle\":\"bob@example.org\"}" | jq -c '[.status, .errors[0].code]'   # [422,"direct_message_refused"]
curl -s -X PUT $FB/config -d '{"dm_blocked":[]}' > /dev/null
REQ=$(curl -s -b jarb -H "X-CSRF-Token: $(cat csrfb)" $API/me/account-links/bridge/codes \
  -d "{\"connection_id\":\"$C\",\"handle\":\"bob@example.org\"}")
jq -c '{r: (.request_id | test("^[A-Z]{1,2}[0-9A-Z]{12}$")), a: .attempts_remaining}' <<<"$REQ"   # {"r":true,"a":5}
CODE=$(curl -s $FB/direct-messages | jq -r '.[-1].body' | grep -oE '[A-Z2-9]{8}')
curl -s $FB/direct-messages | jq -r '.[-1] | .user_external_id, (.body | test("Do not share this code"))'   # u-bob true
curl -s -b jarb -H "X-CSRF-Token: $(cat csrfb)" $API/me/account-links/bridge/confirmation \
  -d "{\"request_id\":\"$(jq -r .request_id <<<"$REQ")\",\"code\":\"22222222\"}" | jq -c '[.status, .errors[0].code, .attempts_remaining]'
# [422,"link_code_invalid",4]
curl -s -b jarb -H "X-CSRF-Token: $(cat csrfb)" $API/me/account-links/bridge/confirmation \
  -d "{\"request_id\":\"$(jq -r .request_id <<<"$REQ")\",\"code\":\"$CODE\"}" | jq -c '{messenger, external_id, display_name}'
# {"messenger":"bridge","external_id":"u-bob","display_name":"Bob Brown"}
curl -s -X PUT $FB/config -d '{"user_lookup":"none"}' > /dev/null; curl -s "${H[@]}" -X POST $API/connections/$C/checks > /dev/null
curl -s "${H[@]}" $API/me/account-links/bridge/codes -d "{\"connection_id\":\"$C\",\"handle\":\"x\"}" | jq -c '[.status, .code]'
# [409,"code_not_offered"]

# C-22.AC-20: a Mention of Alice on a new Alert Group, and her name in the footer
AL=$(curl -s -b jar $API/me | jq -r .id); ETAG=$(curl -s -b jar $API/destinations/$D | jq -r .etag)
curl -s "${H[@]}" -X PUT -H "If-Match: $ETAG" $API/destinations/$D -d "$(curl -s -b jar $API/destinations/$D |
  jq -c --arg u "$AL" '{type, name, connection_id, channel_id, limiter, mentions: (.mentions | .new_alert_group.user_ids = [$u])}')" > /dev/null
curl -s -X PUT 127.0.0.1:19093/_fake/groups/m1 -d '{"receiver":"lab","route":"{}","labels":{"alertname":"Mention"}}' > /dev/null
curl -s -X PUT 127.0.0.1:19093/_fake/groups/m1/alerts/x1 -d '{"labels":{"team":"br","pod":"x1"}}' > /dev/null
NOTIFY m1 '{"reason":"first notification"}'; sleep 1; GM=$(AG 'pod%3D%22x1%22')
curl -s $FB/messages | jq -c '[.[] | select(.envelope.title | endswith("Mention"))][0].envelope |
  {m: .mentions, first: (.body | split("\n")[0])}'
# {"m":[{"ref":"m1","external_id":"u-alice","display_name":"Alice Smith"}],"first":"[@Alice Smith](muster-mention:m1)"}
curl -s "${H[@]}" -X POST $API/alert-groups/$GM/acknowledge > /dev/null; sleep 1
curl -s $FB/messages | jq -r '[.[] | select(.envelope.title | endswith("Mention"))][0].edits[-1].body' | grep -c 'Alice Smith'   # 1
```

`test/e2e/several_connections_test.go` expects the demo Connection "Dev Bridge" among the linkable Connections, and
`test/e2e/account_links_test.go` repeats the token and the code paths with two Bridge Connections to the fake bridge.

## Open questions

None.

## Notes

- Suggested commit: `feat(bridge): link messenger accounts through bridges and mention linked people`.
- A token is the same secret for Telegram and Bridges in form and lifetime, but it is bound to one Connection, and a
  token of a Telegram Connection sent to a bridge's bot links nothing.
- The operation names follow those of Telegram and Mattermost (`start…Link`, `confirm…Link`); the paths are those of
  the C-22 API surface.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-22.FR-13 | partial | answers to Account link requests |
| C-22.FR-15 | partial | the API, events and rules; the profile is S-077 |
| C-22.FR-16 | full | |
| C-22.FR-23 | partial | Messengers → Account links |
| C-22.AC-18 | partial | the API returns the link; the profile shows it in S-077 |
| C-22.AC-19 | full | |
| C-22.AC-20 | partial | refusing Mention settings that name everyone in the chat is S-080 |
