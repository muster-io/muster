---
id: S-043
title: Telegram Connection pages with the step-by-step check and the Telegram Destination form (FE)
capability: C-14
kind: fe
layer: L1
depends_on: [S-040, S-042]
covers: [C-14.FR-1, C-14.FR-2, C-14.FR-10, C-14.FR-11, C-14.FR-14, C-14.AC-6, C-14.AC-7, C-14.AC-13, C-14.AC-16]
files_touched:
  - web/src/components/telegram-connection-fields.tsx
  - web/src/components/telegram-connection-fields.test.tsx
  - web/src/components/connection-form.tsx
  - web/src/components/connection-check.tsx
  - web/src/components/telegram-destination-fields.tsx
  - web/src/components/destination-form.tsx
  - web/src/components/destination-check.tsx
  - web/src/components/alert-group-deliveries.tsx
  - web/src/routes/connections.new.tsx
  - web/src/routes/destinations.new.tsx
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/telegram-connection.spec.ts
  - web/e2e/telegram-destination.spec.ts
acceptance:
  - "[C-14.FR-1, C-14.FR-10] The Telegram Connection form takes the name, the bot token (write-only), the Bot API base URL with the hint \"A self-hosted Bot API server keeps the bot on that server only. Do not call api.telegram.org with this token from anywhere else: even getMe moves the bot back to Telegram's cloud, and presses and comments start to go missing without an error.\", the update mode (\"Long polling\" or \"Webhook\"), the proxy form and the limiter; with an `http` base URL the page shows \"The bot token travels in clear text over http. Use https unless this path is a private network or a tunnel.\""
  - "[C-14.FR-11] \"Check connection\" shows the three steps — \"Dry probe without the token\", \"getMe\", \"getWebhookInfo\" — each with ok or its message, its latency and \"direct\" or \"through the proxy\"; a set webhook shows \"A webhook is set: long polling fails with 409 while it stays.\""
  - "[C-14.AC-6, C-14.FR-11] While the base URL field holds an unsaved value, \"Check connection\" runs only the dry probe against it and shows the other steps as \"Skipped: save the address first\"; the fake server receives no request with the real token at that address."
  - "[C-14.AC-7] A check against an address that refuses connections shows its message without the bot token."
  - "[C-14.FR-2, C-14.FR-14, C-14.AC-13] The Telegram Destination form asks only for the Connection and the channel (\"@username or chat id\"); after \"Save\" the page shows the discussion group found, read-only; a channel without comments shows \"Comments are not enabled for this channel. Enable comments in the channel settings in Telegram; this creates its discussion group.\" and a group without the bot as an admin shows \"The bot is not an admin of the discussion group {group}. Make the bot an admin there, allowed to post messages.\", and nothing is saved."
  - "[C-14.FR-14] \"Check\" on a Telegram Destination lists the checks \"Channel exists\", \"Discussion group\", \"Bot rights in the channel\" and \"Bot rights in the discussion group\" with ok or their messages."
  - "[C-14.AC-16] The Delivery section of an Alert Group whose Thread lost its copy shows \"Thread not attached to the post\" for that Destination."
verify: "make ci e2e"
operator_attention: false
issue: 43
---

# S-043. Telegram Connection pages with the step-by-step check and the Telegram Destination form (FE)

## Scope

**IN**

- The Telegram variant of the Connection pages of S-040: base URL with its hint and warning, update mode, proxy, the
  step-by-step check with the unsaved-address rule.
- The Telegram variant of the Destination form: the channel only, the discussion group shown after the check, the two
  failure texts, the check's steps on "Check".
- "Thread not attached to the post" in the Delivery section.

**OUT**

- The shared pages, the Mention section and the health banners (S-040); Test and Preview (S-048); Account links in the
  profile (S-052).

## Contracts

- **API used**: `createConnection`, `updateConnection`, `getConnection`, `checkConnection` (with `base_url` for an
  unsaved address), `createDestination`, `updateDestination`, `getDestination`, `checkDestination`,
  `listAlertGroupDeliveries`.
- **Connection form, Telegram** (`telegram-connection-fields.tsx`, in the type slot of `connection-form.tsx`): bot token
  (secret field of S-015), "Bot API base URL" (default `https://api.telegram.org`) with the hint of
  [reference.md](../prd/l1/reference.md#banners-warnings-and-notices) under it, "Update mode" (`long_polling`,
  `webhook`), the proxy form of S-015 and the limiter. `warnings: ["base_url_uses_http"]` shows the http warning; a
  `422` at `/bot_api_base_url` shows its message next to the field.
- **Step-by-step check** (`connection-check.tsx`): a row per `ConnectionCheckStep` with the names "Dry probe without the
  token", "getMe" (with "Bot: @{bot_name}"), "getWebhookInfo" (with the pending updates), ok or the step's message,
  `latency_ms` and `via`; `webhook_set: true` adds "A webhook is set: long polling fails with 409 while it stays.".
  While the base URL field differs from the saved value, the button sends `{"base_url": <field>}` and shows the skipped
  steps as "Skipped: save the address first".
- **Destination form, Telegram** (`telegram-destination-fields.tsx`): Connection (Telegram Connections) and "Channel"
  (`@username` or chat id) only, with the help text "Enable comments on the channel and make the bot an admin of the
  channel and of its discussion group."; after saving, the page shows "Discussion group: {title} ({id})" and "Channel:
  {title}" read-only. `destination_check_failed` shows each failing check's text next to "Channel". The Mention section
  of S-040 offers no "everyone" choice and no groups for Telegram.
- **Check** (`destination-check.tsx`): the names of the Telegram checks — `channel_exists` "Channel exists",
  `discussion_group` "Discussion group", `bot_rights_channel` "Bot rights in the channel", `bot_rights_group` "Bot rights
  in the discussion group".
- **Delivery section** (`alert-group-deliveries.tsx`): `thread_not_attached` shows "Thread not attached to the post"
  next to the state.

## Steps

1. Add the Telegram Connection fields, the hint, the warning and the step-by-step check. Check:
   `telegram-connection-fields.test.tsx` and Playwright steps 1 to 4.
2. Add the Telegram Destination fields and check names. Check: steps 5 to 7.
3. Add "Thread not attached to the post". Check: step 8.

## Verification

Run `make dev`, sign in as `admin@example.org` / `muster-dev-password`; the fake Telegram server of S-041 and S-042 runs
with its channels. Then in Playwright:

1. Connections → "Create connection" → Telegram → the base URL field shows `https://api.telegram.org` and the hint
   starting "A self-hosted Bot API server keeps the bot on that server only." → name "tg", token
   `777010:ui-token`, base URL `http://127.0.0.1:18081` → "Save" → "The bot token travels in clear text over http. Use
   https unless this path is a private network or a tunnel."
2. "Check connection" → three rows "Dry probe without the token", "getMe" with "Bot: @muster_dev_bot",
   "getWebhookInfo", each "ok", "direct" and a latency.
3. Change the base URL to `http://127.0.0.1:18081/other/` without saving → "Check connection" → "Dry probe without the
   token" fails with "Wrong path prefix: the server answered 404." and the two other rows read "Skipped: save the
   address first"; from a terminal, `GET 127.0.0.1:18081/_fake/requests` lists no path under `/other/` with the token.
4. Base URL `http://127.0.0.1:1` → "Save" → "Check connection" → the dry probe's message does not contain `ui-token`.
5. Destinations → "Create destination" → Telegram → Connection "Dev Telegram" → channel `@no_comments` → "Save" →
   "Comments are not enabled for this channel. Enable comments in the channel settings in Telegram; this creates its
   discussion group."
6. Make the bot a plain member of the group from a terminal (as in S-042) → channel `@muster_alerts` → "Save" → "The bot
   is not an admin of the discussion group Muster alerts Chat. Make the bot an admin there, allowed to post messages."
   → make it an admin again → "Save" → the page shows "Discussion group: Muster alerts Chat (-1001000000002)".
7. "Check" → four rows "Channel exists", "Discussion group", "Bot rights in the channel", "Bot rights in the discussion
   group", all ok → "Check passed".
8. Add the Destination to a Route, send an Alert Group, delete its copy in the fake server and send a new Alert from a
   terminal (as in S-042, C-14.AC-16) → the Alert Group page → Delivery shows "Thread not attached to the post".

`make e2e` runs these steps as `web/e2e/telegram-connection.spec.ts` and `telegram-destination.spec.ts`.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): add telegram connection pages and the telegram destination form`.
- The form never sends the token anywhere but the saved base URL: the unsaved-address rule is enforced by the API; the
  page only explains it.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-14.FR-1 | partial | the page; with S-041 complete |
| C-14.FR-2 | partial | the form; with S-042 complete |
| C-14.FR-10 | partial | the hint and the warning; with S-041 complete |
| C-14.FR-11 | partial | the step-by-step view; with S-041 complete |
| C-14.FR-14 | partial | the check on the page; with S-042 complete |
| C-14.AC-6 | partial | the form's unsaved address; with S-041 complete |
| C-14.AC-7 | partial | the error shown in the UI; with S-041 and S-042 complete |
| C-14.AC-13 | partial | the form; with S-042 complete |
| C-14.AC-16 | partial | the delivery state on the page; with S-042 complete |
