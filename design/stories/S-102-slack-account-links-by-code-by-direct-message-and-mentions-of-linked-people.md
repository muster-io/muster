---
id: S-102
title: "Slack Account links by a code sent by direct message, and Mentions of linked people (BE)"
capability: C-24
kind: be
layer: L1
depends_on: [S-101]
covers: [C-24.FR-6, C-24.FR-12, C-24.AC-14, C-24.AC-15, C-18.FR-4, C-12.FR-12]
files_touched:
  - api/openapi.yaml
  - internal/accountlinks/slack.go
  - internal/accountlinks/slack_test.go
  - internal/accountlinks/accountlinks.go
  - internal/accountlinks/accountlinks_test.go
  - internal/accountlinks/query.sql
  - internal/accountlinks/live_test.go
  - internal/slack/links.go
  - internal/slack/links_test.go
  - internal/slack/client.go
  - internal/slack/layout.go
  - internal/slack/layout_test.go
  - internal/mentions/mentions.go
  - internal/mentions/mentions_test.go
  - internal/mentions/query.sql
  - internal/messages/texts/en.json
  - internal/messages/texts/ru.json
  - internal/api/accountlinks.go
  - internal/api/accountlinks_test.go
  - internal/api/server.go
  - internal/connections/slack.go
  - internal/runtime/runtime.go
  - internal/fakes/fakeslack/people.go
  - internal/fakes/fakeslack/fakeslack.go
  - internal/fakes/fakeslack/fakeslack_test.go
  - internal/logging/events.go
  - internal/archlint/secretleak.go
  - design/db/schema.md
  - test/e2e/account_links_test.go
acceptance:
  - "[C-24.FR-12, C-24.AC-14] `startSlackLink` with a Slack Connection and an email finds the account with `users.lookupByEmail`, whatever the address's letter case, sends it by direct message — `chat.postMessage` to its user id — the code of `account_link.slack_code` with \"You requested a link to Muster. Do not share this code.\", and answers `202` with the request's id, its expiry and `attempts_remaining`; `confirmSlackLink` with the right code links the account, storing its user id, `name` and display name, records `link` in the Audit log with the messenger `slack`, and the bot sends \"Linked to Muster user {name}\" in the direct message."
  - "[C-24.FR-12, C-24.AC-14] A name is found with `users.list` page by page, compared with `name` and `profile.display_name` regardless of case, leaving out deleted accounts, bots and Slackbot — a name on the second page links the same way; no match is refused with 422 `handle_not_found` at `/handle` and \"No Slack account with this email or name was found.\", two matches with 422 `handle_ambiguous` and \"Several Slack accounts have this name. Enter the email instead.\"; Slack refusing the direct message (`cannot_dm_bot`, `user_not_found`) with 422 `direct_message_refused`."
  - "[C-24.FR-12, C-24.AC-14, C-18.FR-4] After five wrong codes the sixth attempt is refused with 422 `link_code_invalid` at `/code` and `attempts_remaining` 0, even with the right code; requests are limited by `account_link.slack_code_requests` per requesting User and per target account (`429` `link_code_requests_exceeded` with `Retry-After`); an account linked to another User is refused with `409` `account_linked_elsewhere`; a second link of the same User through the same Connection replaces the first; with an API token, an Admin's included, both operations answer `403` `session_required`."
  - "[C-24.FR-12] `listLinkableConnections` offers Slack Connections that are not deleted, with the messenger `slack`; `listAccountLinks` returns Slack links with their `display_name`; the requests' codes are stored as hashes, the code is masked in the Request log of the direct message and reaches no log line (`secretleak.go` probe `slack_links`)."
  - "[C-24.FR-6, C-24.AC-15, C-12.FR-12] A Destination that mentions Alice for new Alert Groups posts a Loud Root message whose `text` and top `section` carry `<@U0FAKEALICE>`, her Slack link through the Destination's Connection, and whose attachment carries no Mention; a User linked only through another Slack Connection is named by their display name as text; an Alert Group acknowledged by Alice shows \"Acknowledged by Alice Smith\", the display name of her link, in the footer."
  - "[C-24.AC-14] A press from the linked account runs its Command as Alice with the Transport `slack`, and a press after she unlinks it in the profile is answered with the \"not linked\" text (`test/e2e/account_links_test.go`)."
verify: "make ci test-integration e2e-go"
operator_attention: true
issue: 311
---

# S-102. Slack Account links by a code sent by direct message, and Mentions of linked people (BE)

## Scope

**IN**

- Account links through a Slack Connection by a code by direct message (C-24.FR-12): the lookup by email or name, the
  code, its limits and confirmation, unlinking, the Audit log.
- Mentions of linked Users as `<@U…>`, and the footer's names of linked Users.
- People in the fake Slack server.

**OUT**

- The profile's "Link Slack" dialog and the user page (S-103).
- Using a Slack account's time zone for the profile: not in L1.

## Contracts

- **Spec** (`api/openapi.yaml`): `startSlackLink` (`POST /me/account-links/slack`, `SlackLinkRequest` with
  `connection_id` and `handle`, an email or a name; `202` `SlackLinkStarted` with `request_id`, `expires_at` and
  `attempts_remaining`) and `confirmSlackLink` (`POST /me/account-links/slack/confirmation`, `SlackLinkConfirm` with
  `request_id` and `code`; `201` `AccountLink`), both `x-permission: authenticated`, the web session only, on the
  interactive path, with the refusals of the acceptance; the problem codes `handle_ambiguous` (new) and
  `handle_not_found`, `direct_message_refused` and `link_code_invalid` described for Slack too; `LinkableConnection`
  and `AccountLink.display_name` described for Slack.
- **Schema**: none new — the messenger CHECKs and the per-target index came with S-100's `0018`; `schema.md`
  describes Slack requests and links.
- **Account links** (`internal/accountlinks/slack.go`): `StartSlack` and `ConfirmSlack` as Mattermost's codes are, with
  `account_link.slack_code` (8 characters without look-alikes, 10 min, 5 attempts) and
  `account_link.slack_code_requests`; the request keeps `target_external_id`, `target_username` and
  `target_display_name`, which the link takes on confirmation; `link_rejected` in the Audit log for a wrong code, an
  expired request and an account linked elsewhere; `query.sql` drops the filter of S-097 from
  `listLinkableConnections`.
- **Slack calls** (`internal/slack/links.go`): `FindUser` — an input with `@` and a dot after it is an email for
  `users.lookupByEmail`; anything else, a leading `@` dropped, a name for `users.list` with `limit=200` page by page
  until a match is certain, `429` answered as `503` with `Retry-After`; `SendCode` and `SendLinked` —
  `chat.postMessage` to the user's id through the interactive path (C-18.FR-11), the code registered as a secret of the
  request so that the Request log and the logs mask it.
- **Mentions** (`internal/mentions`, `internal/slack/layout.go`): a chosen User with a Slack link for the
  Destination's Connection renders as `<@external_id>`, in `text` and the top `section` only; the footer names a User
  linked for the Connection by the link's `display_name`, else by their Muster display name.
- **Log events**: `account_link_linked` and the events of link refusals name Slack among their messengers.
- **Fake Slack server** (`internal/fakes/fakeslack/people.go`): per app, `U0FAKEALICE` "alice" "Alice Smith"
  `alice@example.org`, `U0FAKEBOB` "bob", two accounts whose display name is "Sam" and a deleted one and a bot, over
  three pages of `users.list`; `users.lookupByEmail`, `users.info`, `users.list` with `next_cursor`,
  `conversations.open`; `chat.postMessage` to a `U…` id lands in that user's direct chat, listed at `/_fake/dms`.
- **Secrets** (lint 5): probe `slack_links` pushes a code through a direct message and a failed one.
- **Defaults**: `account_link.slack_code`, `account_link.slack_code_requests`, `account_link.request_prune_after`.

## Steps

1. Write the Slack calls with the fake's people. Check: `links_test.go`, `fakeslack_test.go`.
2. Write the account-link operations with their limits and refusals. Check: `slack_test.go`,
   `accountlinks_test.go`, `go test -tags integration ./internal/accountlinks/...`.
3. Render linked Mentions and the footer's names. Check: `mentions_test.go`, `layout_test.go`.
4. Extend the end-to-end test of Account links. Check: `make e2e-go`, then Verification.

## Verification

```sh
make dev > dev.log 2>&1 &
# as in S-101: the Connection "sl" (C) in socket mode, the Destination "alerts" (D), the Route "sl", FIRE, AG, PRESS;
# Alice's session in `ajar` (`AH`), as the Admin's in S-011; the link rows of S-100 removed
API=localhost:8080/api/v1; FS=127.0.0.1:18097/_fake
psql "$MUSTER_DATABASE_URL" -qc "DELETE FROM account_links WHERE messenger = 'slack'"
curl -s -b ajar $API/me/account-links/connections | jq -r '[.items[] | select(.messenger == "slack") | .name] | sort | join(",")'   # Dev Slack,sl

# C-24.FR-12, AC-14: by email, in other letter case
RQ=$(curl -s "${AH[@]}" $API/me/account-links/slack -d "{\"connection_id\":\"$C\",\"handle\":\"Alice@Example.ORG\"}" | jq -r .request_id)
CODE=$(curl -s "$FS/dms?user=U0FAKEALICE" | jq -r '.[-1].text' | grep -oE '[A-Z0-9]{8}')
curl -s "$FS/dms?user=U0FAKEALICE" | jq -r '.[-1].text' | grep -c 'Do not share this code'   # 1
curl -s "${AH[@]}" $API/me/account-links/slack/confirmation -d "{\"request_id\":\"$RQ\",\"code\":\"$CODE\"}" | jq -c '{messenger, external_id, username, display_name}'
# {"messenger":"slack","external_id":"U0FAKEALICE","username":"alice","display_name":"Alice Smith"}
curl -s "$FS/dms?user=U0FAKEALICE" | jq -r '.[-1].text'                          # Linked to Muster user Alice Smith
curl -s -b jar "$API/audit-log?action=link&limit=1" | jq -c '.items[0] | {action, m: .details.messenger}'   # {"action":"link","m":"slack"}
# by name: refusals
curl -s "${AH[@]}" $API/me/account-links/slack -d "{\"connection_id\":\"$C\",\"handle\":\"Sam\"}" | jq -c '[.status, .errors[0].code, .errors[0].detail]'
# [422,"handle_ambiguous","Several Slack accounts have this name. Enter the email instead."]
curl -s "${AH[@]}" $API/me/account-links/slack -d "{\"connection_id\":\"$C\",\"handle\":\"nobody\"}" | jq -c '[.status, .errors[0].code]'   # [422,"handle_not_found"]
# five wrong codes, then the right one
RQ=$(curl -s "${AH[@]}" $API/me/account-links/slack -d "{\"connection_id\":\"$C\",\"handle\":\"bob\"}" | jq -r .request_id)
CODE=$(curl -s "$FS/dms?user=U0FAKEBOB" | jq -r '.[-1].text' | grep -oE '[A-Z0-9]{8}')
for i in 1 2 3 4 5; do curl -s "${AH[@]}" $API/me/account-links/slack/confirmation -d "{\"request_id\":\"$RQ\",\"code\":\"WRONGCDE\"}" > /dev/null; done
curl -s "${AH[@]}" $API/me/account-links/slack/confirmation -d "{\"request_id\":\"$RQ\",\"code\":\"$CODE\"}" | jq -c '[.status, .errors[0].code, .attempts_remaining]'
# [422,"link_code_invalid",0]
curl -s -b jar "$API/request-log?connection=$C&operation=chat.postMessage&limit=5" | jq -r '.items[].id' | while read -r I; do curl -s -b jar "$API/request-log/$I"; done | grep -c "$CODE"   # 0

# C-24.FR-6, AC-15: Alice mentioned and named
M=$(curl -s -b jar $API/destinations/$D | jq -c --arg a "$(curl -s -b ajar $API/me | jq -r .id)" '.mentions | .new_alert_group.user_ids = [$a] | .new_alert_group.everyone = "none"')
curl -s "${H[@]}" -X PUT -H "If-Match: $(curl -s -b jar $API/destinations/$D | jq -r .etag)" $API/destinations/$D \
  -d "$(curl -s -b jar $API/destinations/$D | jq -c --argjson m "$M" '{type, name, connection_id, channel_id, limiter, mentions: $m}')" > /dev/null
FIRE n1 MentionMe u1; G=$(AG 'pod%3D%22u1%22')
curl -s "$FS/messages?channel=C0FAKEALERTS" | jq -c '[.[] | select(.text | contains("MentionMe"))][0] | {t: (.text | contains("<@U0FAKEALICE>")),
  s: (.blocks[0].text.text | contains("<@U0FAKEALICE>")), a: (.attachments | tostring | contains("<@"))}'   # {"t":true,"s":true,"a":false}
TS=$(curl -s "$FS/messages?channel=C0FAKEALERTS" | jq -r '[.[] | select(.text | contains("MentionMe"))][0].ts')
PRESS "{\"channel\":\"C0FAKEALERTS\",\"ts\":\"$TS\",\"action\":\"ack\",\"user\":\"U0FAKEALICE\"}" > /dev/null; sleep 1
curl -s "$FS/messages?channel=C0FAKEALERTS" | jq -r '[.[] | select(.text | contains("MentionMe"))][0].attachments[0].blocks[1].text.text' | grep -c 'Acknowledged by Alice Smith'   # 1
```

Live, on the maintainer's test workspace, with `operator_attention` — the operator reads the code the bot sends:

```sh
# the Connection "test workspace" (L) as in S-101; the Admin links their own Slack account by email
RQ=$(curl -s "${H[@]}" $API/me/account-links/slack -d "{\"connection_id\":\"$L\",\"handle\":\"<the operator's Slack email>\"}" | jq -r .request_id)
# the operator opens the bot's Messages tab in Slack, reads the code and types it here
read -r CODE; curl -s "${H[@]}" $API/me/account-links/slack/confirmation -d "{\"request_id\":\"$RQ\",\"code\":\"$CODE\"}" | jq -c '{messenger, username}'
# {"messenger":"slack","username":"<the operator's Slack name>"}
```

The operator sees the code and then "Linked to Muster user …" in the Messages tab, and that the tab does not let them
write to the bot (F-096). The link stays for S-103's live check, or is removed in the profile.

## Open questions

None.

## Notes

- Suggested commit: `feat(slack): link slack accounts by a code and mention linked people`.
- `operator_attention` is true: only the operator can read the direct message; the bot has no scope to read it back.
- A name is searched page by page because Slack has no search by name (F-095); an email is the reliable way, which the
  ambiguity refusal says.
- The lookup leaves out deleted accounts and bots so that a name taken by a deactivated colleague does not block the
  person who has it now.
- A Slack account's `tz` (F-095) could fill the profile's time zone for the Snooze presets; that is left for later.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-24.FR-6 | partial | Mentions of linked Users and the footer's names; the rest is S-099 |
| C-24.FR-12 | partial | the API and the bot's messages; the profile's dialog is S-103 |
| C-24.AC-14 | full | |
| C-24.AC-15 | partial | linked Mentions and the footer; the rest is S-098 and S-099 |
| C-18.FR-4 | partial | the Identity space of a Slack Connection |
| C-12.FR-12 | partial | Slack's Mention form and the footer's display names |
