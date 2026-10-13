---
id: S-070
title: "Connection and Destination labels, the Telegram link row and the duplicate-chat warning (FE)"
capability: C-18
kind: fe
layer: L1
depends_on: [S-069]
covers: [C-13.FR-1, C-14.FR-1, C-13.FR-9, C-08.FR-1, C-13.FR-11, C-11.FR-16, C-09.FR-11, C-09.FR-14, C-11.FR-1, C-18.FR-2, C-18.FR-4, C-18.FR-6, C-18.FR-10]
files_touched:
  - api/openapi.yaml
  - internal/destinations/query.sql
  - internal/destinations/destinations.go
  - internal/destinations/write.go
  - internal/destinations/destinations_test.go
  - internal/destinations/write_test.go
  - internal/api/destinations.go
  - internal/api/destinations_test.go
  - web/src/components/destination-label.ts
  - web/src/components/destination-label.test.ts
  - web/src/components/route-destinations.tsx
  - web/src/components/route-destinations.test.tsx
  - web/src/components/route-suggestions.tsx
  - web/src/components/alert-group-deliveries.tsx
  - web/src/components/alert-group-deliveries.test.tsx
  - web/src/components/timeline.tsx
  - web/src/components/timeline.test.tsx
  - web/src/components/account-links-table.tsx
  - web/src/components/account-links-table.test.tsx
  - web/src/components/profile-account-links.tsx
  - web/src/components/link-telegram-dialog.tsx
  - web/src/routes/destinations.index.tsx
  - web/src/routes/connections.index.tsx
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/several-connections.spec.ts
  - web/e2e/account-links.spec.ts
  - web/e2e/destinations.spec.ts
  - web/e2e/timers.spec.ts
  - docs/messengers/account-links.md
  - design/stories/README.md
  - design/stories/coverage.md
  - design/stories/S-070-connection-and-destination-labels.md
acceptance:
  - "[C-13.FR-9] The Destinations list has a column \"Connection · channel\": a Mattermost Destination shows its Connection's name and `#<channel name>`, a Telegram Destination its Connection's name and the channel title (or the channel as entered while no check has found the title), an outgoing webhook the host of its URL, or nothing when the URL's host is not literal text (`destination-label.test.ts`, `several-connections.spec.ts`)."
  - "[C-08.FR-1, C-13.FR-11] Each Destination row of the Route editor and each option of its \"Add a Destination\" picker and of the Internal-alerts suggestion picker reads \"<name> · <messenger> · via <Connection> · <channel>\" (an outgoing webhook: \"<name> · Outgoing webhook · <host>\"), and the rows keep their Health (`route-destinations.test.tsx`, `several-connections.spec.ts`)."
  - "[C-13.FR-1, C-14.FR-1] The Connections list shows the host of a Mattermost Connection's server URL and the `@<bot username>` of a Telegram Connection once a Connection check has found it, next to its name and type (`several-connections.spec.ts`)."
  - "[C-11.FR-16, C-09.FR-11, C-09.FR-14] The delivery section of the Alert Group page and the Timeline's delivery entries show the Destination's name only; its full label is the element's `title` and its accessible description, and a Destination missing from the list keeps its name with no description (`alert-group-deliveries.test.tsx`, `timeline.test.tsx`, `several-connections.spec.ts`)."
  - "[C-18.FR-2, C-18.FR-4, C-18.FR-10, C-18.FR-6] In the profile and on an Admin's user page a Telegram Account link reads \"Telegram · @<username> · for all Telegram bots\" with the secondary line \"linked via <Connection>\"; with a Telegram link the profile's button reads \"Relink Telegram\"; Mattermost links stay one row per Connection, and the profile says that Mattermost links are per Connection (`account-links-table.test.tsx`, `account-links.spec.ts`, `several-connections.spec.ts`)."
  - "[C-11.FR-1] When two or more of a Route's Destinations deliver to one chat — one Telegram chat id, or one Mattermost server and channel — the Route editor shows the warning \"Two Destinations deliver to the same chat: <names>. Each message will appear there twice.\" once per chat, and saving the Route stays allowed; Mattermost channels with one id on two servers are two chats (`route-destinations.test.tsx`, `several-connections.spec.ts`)."
  - "[C-11.FR-1] `getDestination` and `listDestinations` return a Telegram Destination's `channel_chat_id`, the numeric chat id its Destination check found, or null before one (`internal/api/destinations_test.go`, `internal/destinations/destinations_test.go`, the Verification)."
  - "Every new text exists in English and Russian, the pages of the acceptance statements show no horizontal scrolling at 360 px wide in either language, and no Content Security Policy violation is reported (`several-connections.spec.ts`)."
verify: "make ci e2e"
operator_attention: false
issue: 209
---

# S-070. Connection and Destination labels, the Telegram link row and the duplicate-chat warning (FE)

## Scope

**IN**

- A shared label for a Destination, built in the SPA from `listDestinations` and `listConnections`: its Connection and
  its channel (Mattermost `#<channel name>`, Telegram the channel title or the channel as entered), or the host of an
  outgoing webhook's URL.
- The Destinations list's column "Connection · channel"; the Route editor's Destination rows, its "Add a Destination"
  picker and the Internal-alerts suggestion picker with the full label; the Connections list with the Mattermost server
  or the Telegram `@bot`.
- The Alert Group page's delivery section and the Timeline's delivery entries: the name stays, the full label is a
  native tooltip (`title`) and an accessible description.
- The Telegram Account link row in the profile and on the user page ("for all Telegram bots", "linked via
  <Connection>"), "Relink Telegram", and the hint that Mattermost links are per Connection.
- The non-blocking duplicate-chat warning in the Route editor.
- A read-only `channel_chat_id` on `TelegramDestination`, so that `@channel` and its numeric id are recognised as one
  chat (a contract fix, below).
- A short section on several Connections of one messenger in `docs/messengers/account-links.md`, which S-069 left to
  this story.

**OUT**

- New PRD requirements: the labels and the warning refine existing pages (C-08, C-09, C-11, C-13, C-14, C-18); no FR or
  AC is added.
- The Destination page's own fields (S-064, S-043, S-046): unchanged.
- Labels for a deleted Destination or Connection on the Alert Group page: they keep the name only (no API change to
  `DestinationRef` or `TimelineDeliveryEntry`).
- Any change to delivery, Identity spaces or linking: S-069 proved them; this story changes texts only.

## Contracts

- **Operations** (existing): `listDestinations`, `getDestination`, `listConnections`, `getRoute`, `updateRoute`,
  `listAlertGroupDeliveries`, `listAlertGroupTimeline`, `listMyAccountLinks`, `listLinkableConnections`,
  `listUserAccountLinks`, `checkConnection`. Every Role has `destinations:read` and `connections:read`, so every page
  of this story may join both lists; each query keeps its permission gate and falls back to the name alone.
- **Contract fix** (`api/openapi.yaml`): `TelegramDestination.channel_chat_id`, `string | null`, read-only — the
  channel's numeric chat id found by the Destination check (`destinations.telegram_channel_chat_id`, already stored by
  S-042), a string like `discussion_group_id`. `ListDestinations` and `GetDestination` select it; create and update
  responses carry it. No migration, no audit change. A Destination check that finds a different chat id for the same
  channel saves the new id with a hint, like a new title; the Audit log is unchanged.
- **Chat identity** for the warning: Telegram — `channel_chat_id`, else the lower-cased `channel_id`; Mattermost — the
  Connection's `server_url` normalised (lower-case scheme and host, path without the trailing `/`) and `channel_id`.
  Outgoing webhooks are never duplicates.
- **Webhook host**: the URL of `events.url` (modes `events`, `both`) or `template.create.url` (`template`); the text
  before the first `{{` parsed as an http(s) URL whose authority is complete literal text; the label shows its host
  (hostname and port), never user information, path or query.
- **Texts** (EN | RU): "Connection · channel" | "Подключение · канал"; "via {{connection}}" | "через {{connection}}";
  "for all Telegram bots" | "для всех Telegram-ботов"; "linked via {{connection}}" | "привязан через {{connection}}";
  "Relink Telegram" | "Перепривязать Telegram"; the Mattermost hint and the warning with Russian plurals. The final
  keys and texts are listed in the pull request.
- **CSP**: no inline styles; the pickers stay native `<select>` (D250); tooltips are the native `title`.

## Steps

1. Add `channel_chat_id` to the spec, select it in `ListDestinations` and `GetDestination` at the same position, map
   it in `internal/destinations` and `internal/api`, `make generate`. Check: the Go tests assert it, `make generate-check`
   is clean.
2. Write `destination-label.ts` (labels, webhook host, chat identity, duplicate groups) with its unit tests. Check:
   `make test-web`.
3. Destinations list, Connections list, Route editor rows and pickers, duplicate warning. Check: component tests.
4. Delivery section and Timeline tooltips. Check: component tests with and without `destinations:read`.
5. Account links row, "Relink Telegram", the Mattermost hint, on the profile and the user page. Check: component test.
6. Locales, docs section, Playwright specs whose texts changed, and `several-connections.spec.ts` extended with the
   checks and screenshots of the Verification. Check: `make ci e2e`.

## Verification

```sh
make dev > dev.log 2>&1 &
# Connections and Destinations as in S-069's Verification (Dev Mattermost and mm-b on server 1, mm-2 on server 2,
# Dev Telegram and tg-b), plus a Connection check of each Telegram Connection, and the Route "s069-multi" with all six
# Destinations.
curl -s "${A[@]}" -X POST $API/connections/$TB/checks | jq -r .ok                           # true
curl -s "${A[@]}" $API/connections/$TB | jq -r .bot_username                               # muster_dev_b_bot
curl -s "${A[@]}" $API/destinations/$TDA | jq -r '.channel_id, .channel_chat_id'
# @muster_alerts
# -1001000000001
curl -s "${A[@]}" "$API/destinations?limit=500" | jq -c '[.items[] | select(.type == "telegram") | .channel_chat_id] | unique'
# ["-1001000000001"]
```

Playwright (`web/e2e/several-connections.spec.ts`, from `make e2e`, at 1280 and at 360 px, in English and in Russian):

- `/connections`: "Dev Mattermost" and "mm-b" with "127.0.0.1:18065", "mm-2" with "127.0.0.1:18066", "Dev Telegram"
  with "@muster_dev_bot", "tg-b" with "@muster_dev_b_bot".
- `/destinations`: the header "Connection · channel" ("Подключение · канал"); "mm-a-alerts" — "Dev Mattermost ·
  #alerts", "mm-2-alerts" — "mm-2 · #alerts", "tg-a-alerts" — "Dev Telegram · <channel title>", "tg-b-alerts" —
  "tg-b · <channel title>", "s069-hook" — "127.0.0.1:18093".
- The Route editor of "s069-multi": the row "tg-b-alerts · Telegram · via tg-b · <channel title>" with its Health; the
  picker's options in the same form; two warnings — one naming "mm-a-alerts" and "mm-b-alerts", one naming
  "tg-a-alerts" and "tg-b-alerts" — and none naming "mm-2-alerts"; Save stays enabled and saves.
- The Alert Group page: the delivery row "tg-b-alerts" whose `title` is the full label; a Timeline delivery entry
  likewise.
- The profile with a Telegram link: "Telegram · @<username> · for all Telegram bots", "linked via tg-b" (the
  Responder linked through the second bot), the button "Relink Telegram", the Mattermost hint; the Admin's user page:
  the same Telegram row.
- At 360 px no horizontal scrolling on each page in both languages; no CSP violation in any test.

**Screenshots** (live-verification artifacts, not committed): `make build`, then from `web/`
`MUSTER_E2E_SCREENSHOTS=../bin/live-shots/s070 pnpm exec playwright test e2e/several-connections.spec.ts` saves the
same screens as S-069 (`connections`, `destinations`, `destination-tg-b`, `route-editor-destinations`,
`alert-group-deliveries`, `delivery-problem-filter`, `profile-messenger-accounts`, `admin-user-account-links`), plus
`alert-group-timeline`, each as `-desktop.png`, `-360.png`, `-ru-desktop.png` and `-ru-360.png`, into
`bin/live-shots/s070/`. The pull request compares them with `bin/live-shots/s069/`.

## Open questions

None. The labels and the warning were settled by the maintainer (D305, D309); see Notes.

## Notes

- D309 (binding, from S-069's screenshots): Destinations list "Connection · channel" (webhook: host); Route editor and
  "Add a Destination": "name · messenger · via <Connection> · #channel"; Connections list: server (Mattermost) or
  `@bot` (Telegram); delivery section and Timeline: the name, the full label in a tooltip; the profile per D305; the
  duplicate-chat warning in the Route editor.
- D305 (binding): several Connections of one messenger are normal; Mattermost Identity spaces are per Connection,
  the Telegram Identity space is all of Telegram; one chat through two Connections is two Destinations, allowed, with a
  warning in the Route editor.
- **Choices made in this story** (small, reversible): the warning also covers two Destinations of one Connection in one
  chat, since their messages appear twice too; one warning per chat, naming all its Destinations; a Telegram Connection
  never checked shows no `@bot`; the Telegram row moves the last use to its secondary line; the suggestion picker uses
  the same label; tooltips are the native `title` with a screen-reader description, as `DeliveryProblemMark` does.
- **Vocabulary.** "channel" in "Connection · channel" and "Telegram bots" name the messenger's own things, as the
  definitions of Destination and Connection in `CONTEXT.md` do; a Destination is never called a channel, nor a
  Connection a bot.
- **Kind.** `fe` with a one-field contract fix (AGENTS.md: a wrong API contract is fixed in `api/openapi.yaml` in the
  story's pull request): without the numeric chat id, `@muster_alerts` and `-1001000000001` look like two chats.
- Suggested commit: `feat(web): label connections and destinations and warn about duplicate chats`.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-13.FR-1 | partial | the Connections list shows the Mattermost server |
| C-14.FR-1 | partial | the Connections list shows the Telegram bot |
| C-13.FR-9 | partial | the Destinations list's "Connection · channel" column |
| C-08.FR-1 | partial | the Route editor's Destination rows, picker and duplicate-chat warning |
| C-13.FR-11 | partial | the Internal-alerts suggestion picker's labels |
| C-11.FR-16 | partial | the delivery section's tooltips |
| C-09.FR-11 | partial | the Timeline's delivery entries' tooltips |
| C-09.FR-14 | partial | the Alert Group page's delivery section |
| C-11.FR-1 | partial | two Destinations in one chat are warned about in the Route editor; `channel_chat_id` |
| C-18.FR-2 | partial | "Relink Telegram" |
| C-18.FR-4 | partial | the Identity spaces explained: Telegram for all Telegram bots, Mattermost per Connection |
| C-18.FR-6 | partial | the user page's Telegram row |
| C-18.FR-10 | partial | the profile's Telegram row and the Mattermost hint |
