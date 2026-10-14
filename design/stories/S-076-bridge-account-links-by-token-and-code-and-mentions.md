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
  - internal/db/migrations/0011_bridge_account_links.up.sql
  - internal/db/migrations/0011_bridge_account_links.down.sql
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
  - internal/api/accountlinks.go
  - internal/api/accountlinks_test.go
  - internal/api/problem.go
  - internal/messages/texts/en.json
  - internal/messages/texts/ru.json
  - internal/runtime/runtime.go
  - internal/fakes/fakebridge/people.go
  - internal/fakes/fakebridge/fakebridge_test.go
  - internal/logging/events.go
  - internal/archlint/secretleak.go
  - docs/messengers/account-links.md
  - test/e2e/account_links_test.go
acceptance:
  - "[C-22.FR-15, C-22.AC-18] `createBridgeAccountLinkToken` for a Bridge Connection returns a single-use token of 43 base64url characters, valid for `account_link.bridge_token_ttl`, and `link_url` built from the bridge's `link_url_template` (null without one); it accepts the web session only (403 `session_required` with an API token)."
  - "[C-22.FR-15, C-22.FR-13, C-22.AC-18] When the fake bridge reports an `account_link` event with that token, the sender's account is linked to the User — `external_id`, `display_name` and the handle the bridge reported — and the event is answered \"Linked to Muster user {name} ({login})\"; the same token again, an expired one and a token of another Connection link nothing, are answered \"This link is invalid or expired. Start again from your Muster profile.\" and are recorded as `account_link.link_rejected`."
  - "[C-22.FR-15, C-22.AC-19] With `user_lookup` `email` and `direct_messages.bot_can_start`, `requestBridgeAccountLinkCode` with a known address sends the code through `sendDirectMessage` with \"You requested a link to Muster. Do not share this code.\", and `confirmBridgeAccountLink` with it links the account; after five wrong codes the sixth attempt is refused even with the right code; an address the bridge does not find is refused with 422 `handle_not_found` at `/handle`; code requests follow `account_link.bridge_code_requests`."
  - "[C-22.FR-15, C-22.AC-19] A Bridge Connection whose `info` has `user_lookup` `none`, or no `direct_messages` with `bot_can_start`, refuses a code request with 409 `code_not_offered`; `listLinkableConnections` returns for each Bridge Connection its link methods (`token`, and `code` where offered) and the lookup kind."
  - "[C-22.FR-15] Linking rules of C-18 hold for Bridges: one account per User per Bridge Connection, an account linked to another User refused, unlinking in the profile and by an Admin, the Audit log with the messenger `bridge`, and the Identity space `bridge:<connection>`; two Bridge Connections to the same bridge need two links."
  - "[C-22.FR-16, C-22.AC-20] A Bridge Destination that mentions Alice for new Alert Groups posts a Loud Root message whose `mentions` hold `ref`, the `external_id` and the `display_name` of her link, and whose body starts with `[@Alice Smith](muster-mention:<ref>)`; Bob, without a link, is named as text with no marker; the footer of an Alert Group Alice acknowledged names her by her link's `display_name`."
  - "[C-22.FR-23] The page Messengers → Account links explains linking through a Bridge by token and by code, the lookup kind the bridge declares, and that Bridge links are per Connection."
verify: "make ci test-integration e2e"
operator_attention: false
issue: null
---

# S-076. Bridge Account links by a token sent to the bot or a code by direct message, and Mentions of linked people (BE)

## Scope

**IN**

- Account links through Bridge Connections: the token sent to the bot, always; the code by direct message, where the
  bridge can look people up and start a direct chat; the rules, limits and Audit log of C-18.
- Handling `account_link` events and answering them; `findUsers` and `sendDirectMessage`.
- Mentions of linked people in envelopes, and their names in footers.

**OUT**

- The profile and the user page (S-077); presses and Text commands, which already map accounts through these links
  (S-075).

## Contracts

- **Spec** (`api/openapi.yaml`): `createBridgeAccountLinkToken` (`POST /me/account-links/bridge`, `{connection_id}` →
  `{token, link_url, expires_at}`), `requestBridgeAccountLinkCode` (`POST /me/account-links/bridge/codes`,
  `{connection_id, handle}` → `202 {expires_at}`) and `confirmBridgeAccountLink` (`POST
  /me/account-links/bridge/confirmation`, `{connection_id, code}` → `AccountLink`), web session only (C-03.FR-27);
  `AccountLink.messenger` and `LinkableConnection.messenger` gain `bridge`; `LinkableConnection` gains, for a Bridge,
  `link_methods` (`token`, `code`) and `handle_kind` (`email`, `login`, `username`, `user_id`); problem codes
  `handle_not_found` and `code_not_offered`.
- **Schema** (migration `0011_bridge_account_links`, `schema.md`): `account_links.messenger` and
  `account_link_requests.messenger` accept `bridge`; `account_links.identity_space` becomes `'telegram'` for Telegram
  and `<messenger>:<connection_id>` otherwise — a generated column cannot change its expression before PostgreSQL 17, so
  the migration adds the new column, moves the two unique constraints to it and drops the old one; `account_links`
  gains `display_name`, which Bridges fill; `account_link_requests` gains `method` (`token`, `code`), and the unique
  token index covers Telegram and Bridge tokens (`method = 'token'`).
- **Linking** (`internal/accountlinks/bridge.go`): tokens as `account_link.telegram_token_ttl` does for Telegram, bound
  to the User and the Connection; codes as for Mattermost, with `account_link.bridge_code` and
  `account_link.bridge_code_requests`; the conflict, replacement and unlink rules and the Audit log entries of S-051 with
  the messenger `bridge`; `account_link.request_prune_after` prunes Bridge requests too.
- **Events and calls** (`internal/bridge/links.go`): `account_link` events from S-075's dispatcher link and are answered
  through `answerEvent`; `LinkBots` makes `findUsers` and `sendDirectMessage` (key `dm-<request public id>`) through the
  interactive path (C-18.FR-11); a bridge answering `forbidden` to the direct message is reported to the person as
  "The bot cannot send you a direct message. Send it the token instead."
- **Mentions and footers** (`internal/mentions`, `internal/accountlinks/lookup.go`, `internal/bridge/envelope.go`): a
  Mention target of a User with a link for the Destination's Connection carries `external_id` and `display_name`; the
  envelope lists it in `mentions` with a `ref` (`m1`, `m2`, …) and puts the markers on the first line of the body of a
  Loud message; a User without a link is their display name as text. Footers name a linked User by `display_name`
  (C-12.FR-12).
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

1. Add the migration and the linking rules. Check: `accountlinks/bridge_test.go`.
2. Handle `account_link` events and the code path through the bridge. Check: `bridge/links_test.go`.
3. Add the API operations. Check: `internal/api/accountlinks_test.go`.
4. Add Mentions and footers. Check: `mentions_test.go`, `envelope_test.go`.
5. Extend the fake, the probe, the page and the end-to-end test. Check: Verification.

## Verification

```sh
make dev &
# as in S-075: the session (Alice is the Admin "admin@example.org" here, renamed "Alice Smith", login "alice"), the
# Integration "lab", NOTIFY, ADV and AG, the Route to the Destination "alerts" (D) of the Connection "br" (C, poll mode)
API=localhost:8080/api/v1; FB=127.0.0.1:18094/_fake
curl -s -X PUT $FB/config -d '{"link_url_template":"http://127.0.0.1:18094/bot?start={token}","user_lookup":"email"}' > /dev/null
curl -s "${H[@]}" -X POST $API/connections/$C/checks > /dev/null      # reads info again

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

# C-22.AC-19: a code by direct message, for bob (session "jarb")
curl -s -b jarb -H "X-CSRF-Token: $(cat csrfb)" $API/me/account-links/bridge/codes \
  -d "{\"connection_id\":\"$C\",\"handle\":\"nobody@example.org\"}" | jq -c '[.status, .errors[0].code]'   # [422,"handle_not_found"]
curl -s -b jarb -H "X-CSRF-Token: $(cat csrfb)" $API/me/account-links/bridge/codes \
  -d "{\"connection_id\":\"$C\",\"handle\":\"bob@example.org\"}" -o /dev/null -w '%{http_code}\n'   # 202
CODE=$(curl -s $FB/direct-messages | jq -r '.[-1].body' | grep -oE '[A-Z2-9]{8}')
curl -s $FB/direct-messages | jq -r '.[-1] | .user_external_id, (.body | test("Do not share this code"))'   # u-bob true
curl -s -b jarb -H "X-CSRF-Token: $(cat csrfb)" $API/me/account-links/bridge/confirmation \
  -d "{\"connection_id\":\"$C\",\"code\":\"$CODE\"}" | jq -c '{messenger, external_id}'   # {"messenger":"bridge","external_id":"u-bob"}
curl -s -X PUT $FB/config -d '{"user_lookup":"none"}' > /dev/null; curl -s "${H[@]}" -X POST $API/connections/$C/checks > /dev/null
curl -s "${H[@]}" $API/me/account-links/bridge/codes -d "{\"connection_id\":\"$C\",\"handle\":\"x\"}" | jq -c '[.status, .code]'
# [409,"code_not_offered"]

# C-22.AC-20: a Mention of Alice on a new Alert Group
AL=$(curl -s -b jar $API/me | jq -r .id); ETAG=$(curl -s -b jar $API/destinations/$D | jq -r .etag)
curl -s "${H[@]}" -X PUT -H "If-Match: $ETAG" $API/destinations/$D -d "$(curl -s -b jar $API/destinations/$D |
  jq -c --arg u "$AL" '{type, name, connection_id, channel_id, limiter, mentions: (.mentions | .new_alert_group.user_ids = [$u])}')" > /dev/null
curl -s -X PUT 127.0.0.1:19093/_fake/groups/m1 -d '{"receiver":"lab","route":"{}","labels":{"alertname":"Mention"}}' > /dev/null
curl -s -X PUT 127.0.0.1:19093/_fake/groups/m1/alerts/x1 -d '{"labels":{"team":"br","pod":"x1"}}' > /dev/null
NOTIFY m1 '{"reason":"first notification"}'; sleep 1
curl -s $FB/messages | jq -c '[.[] | select(.envelope.title | endswith("Mention"))][0].envelope |
  {m: .mentions, first: (.body | split("\n")[0])}'
# {"m":[{"ref":"m1","external_id":"u-alice","display_name":"Alice Smith"}],"first":"[@Alice Smith](muster-mention:m1)"}
```

## Open questions

None.

## Notes

- Suggested commit: `feat(bridge): link messenger accounts through bridges and mention linked people`.
- A token is the same secret for Telegram and Bridges in form and lifetime, but it is bound to one Connection, and a
  token of a Telegram Connection sent to a bridge's bot links nothing.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-22.FR-13 | partial | answers to Account link requests |
| C-22.FR-15 | partial | the API, events and rules; the profile is S-077 |
| C-22.FR-16 | full | |
| C-22.FR-23 | partial | Messengers → Account links |
| C-22.AC-18 | partial | the API returns the link; the profile shows it in S-077 |
| C-22.AC-19 | full | |
| C-22.AC-20 | partial | refusing Mention settings that name everyone in the chat is S-073 |
