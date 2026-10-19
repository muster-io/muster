---
id: S-103
title: "Slack Connection and Destination pages, the check before saving, and Slack in the profile (FE)"
capability: C-24
kind: fe
layer: L1
depends_on: [S-102, S-105, S-070]
covers: [C-24.FR-1, C-24.FR-2, C-24.FR-3, C-24.FR-12, C-24.FR-13, C-24.FR-7, C-24.AC-2, C-24.AC-9, C-24.AC-16, C-24.AC-18, C-13.FR-14]
files_touched:
  - web/src/components/slack-connection-fields.tsx
  - web/src/components/slack-connection-fields.test.tsx
  - web/src/components/slack-destination-fields.tsx
  - web/src/components/slack-destination-fields.test.tsx
  - web/src/components/connection-form.tsx
  - web/src/components/connection-form.test.tsx
  - web/src/components/connection-check.tsx
  - web/src/components/callback-hint.tsx
  - web/src/components/destination-form.tsx
  - web/src/components/destination-form.test.tsx
  - web/src/components/destination-check.tsx
  - web/src/components/destination-label.ts
  - web/src/components/destination-preview-panel.tsx
  - web/src/components/destination-preview-panel.test.tsx
  - web/src/components/destination-test-panel.tsx
  - web/src/components/destination-test-panel.test.tsx
  - web/src/components/link-slack-dialog.tsx
  - web/src/components/link-slack-dialog.test.tsx
  - web/src/components/profile-account-links.tsx
  - web/src/components/account-links-table.tsx
  - web/src/components/timeline.tsx
  - web/src/components/template-preview.tsx
  - web/src/routes/connections.new.tsx
  - web/src/routes/connections.$connectionId.tsx
  - web/src/routes/destinations.new.tsx
  - web/src/routes/destinations.$destinationId.tsx
  - web/src/routes/admin.audit-log.tsx
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/slack-connection.spec.ts
  - web/e2e/slack-destination.spec.ts
  - web/e2e/account-links.spec.ts
  - web/e2e/connection-checks.spec.ts
  - web/e2e/support.ts
acceptance:
  - "[C-24.FR-2, C-24.AC-2, C-13.FR-14] \"Check connection\" in the Slack create form, with the fake server's tokens, shows the steps `token`, `scopes` and `app_token` and the bot, the workspace and the app, and afterwards no Connection exists; in the edit form of a saved Slack Connection with only the proxy changed it shows the step `dry_probe` alone, and with the tokens typed again the three steps through the new proxy — through `checkConnectionDraft` of S-105 (`web/e2e/connection-checks.spec.ts`)."
  - "[C-24.FR-1, C-24.FR-2, C-24.AC-18] Connections → New → Slack shows the name, the bot token, the event mode — Socket Mode with the app-level token, or HTTP with the Slack signing secret, the callback address and the hint of reference.md — the proxy form and the limiter, with \"Check connection\" before saving; a saved Slack Connection's page shows the same with its secrets write-only, the step-by-step check with the bot, the workspace and the app, the Socket Mode hint, and while the Leader's last `hello` counted another connection the warning of reference.md; Delete is offered while no Destination uses it."
  - "[C-24.FR-3, C-24.AC-18, C-24.AC-9] Destinations → New → Slack picks the Connection, then a channel from the bot's channels with a filter by name, or takes a channel id typed by hand; a refused save shows the check's text — for a channel without the bot \"Invite the bot to the channel: /invite @{bot}\" — and a saved Destination shows `#` and its channel's name; \"Check\" on its page runs the Destination check and ends a Broken state at once."
  - "[C-24.FR-13, C-24.AC-16] The test panel of a Slack Destination shows, beside the result of its one step, the note of reference.md that Slack does not let a bot press its own button; the preview panel draws the message Muster would send — the colour bar, the summary, the sections as text, the buttons with `primary` shown green and the Snooze menu — from its JSON, every text as plain text."
  - "[C-24.FR-12, C-24.AC-18] Profile → Messenger accounts offers \"Link Slack\" for each Slack Connection: a field \"Your Slack email or name\", \"Send code\", the code entry with the attempts left, and the refusals of S-102 as their texts; the list shows the Slack link with the Connection, the display name and its last use, and unlinks it; the user page of an Admin shows and removes it."
  - "[C-24.FR-7, C-24.FR-1] The Route editor's template preview offers Slack's mrkdwn beside Mattermost and Telegram; the Timeline names the Transport `slack` \"Slack\"; the Audit log labels the fields of a Slack Connection and names its secrets without values; the labels of Connections and Destinations (S-070) name the Slack type and the workspace."
  - "[C-24.AC-18] Every text above exists in English and Russian, `make lint` finds no missing key, and the Slack Connection page, the Slack Destination form and the profile's link dialog show no horizontal scrollbar at 360 px wide (Playwright)."
verify: "make ci e2e"
operator_attention: false
issue: null
---

# S-103. Slack Connection and Destination pages, the check before saving, and Slack in the profile (FE)

## Scope

**IN**

- The Slack Connection form and page: the event mode with its secret, the callback address and its hints, the
  step-by-step check, the warning about a shared app.
- "Check connection" in the Slack forms, on `checkConnectionDraft` of S-105.
- The Slack Destination form with the channel picker, the check, the test panel's note and the preview of a Slack
  message.
- "Link Slack" in the profile, the Slack link in the lists, the Transport and the labels, and `mrkdwn` in the Route
  editor's preview.

**OUT**

- The documentation page (S-104).

## Contracts

- **Operations used**: `createConnection`, `updateConnection`, `getConnection`, `deleteConnection`,
  `checkConnection`, `checkConnectionDraft`, `listConnectionChannels`, `createDestination`, `updateDestination`,
  `checkDestination`, `testDestination`, `previewDestination`, `previewTemplate` (`mrkdwn`), `listLinkableConnections`,
  `startSlackLink`, `confirmSlackLink`, `listAccountLinks`, `deleteAccountLink`, `listUserAccountLinks`.
- **Pages and components**: `slack-connection-fields.tsx` (the mode switch shows only the secret the mode needs; the
  callback address read-only with a copy button and the hints of reference.md, through `callback-hint.tsx`);
  `slack-destination-fields.tsx` (the Connection, the channel picker over `listConnectionChannels` with `q` and the
  "more channels than shown" note when `truncated`, the id field, Mention settings without `all` and groups, the
  limiter); `link-slack-dialog.tsx`; the narrowing branches of S-097 and S-098 replaced by the real forms; the check
  panels show the new steps and codes; the test panel's note; the preview panel's Slack message drawn element by
  element from the request body, never as markup.
- **Texts**: every string through i18next in `en.json` and `ru.json`, the texts of reference.md among them.
- **End-to-end** (`web/e2e/`): `slack-connection.spec.ts` (create in both modes, check, the shared warning by the fake's
  `extra_connections`, the check before saving), `slack-destination.spec.ts` (picker, refusal, save, Check, test with
  its note, preview), `account-links.spec.ts` (Link Slack by email and code), `connection-checks.spec.ts` (the Slack
  draft), each at 360 px for the pages of the acceptance; `support.ts` gains the fake Slack server's address.

## Steps

1. Build the Connection form and page, with "Check connection" on the draft. Check: `slack-connection-fields.test.tsx`,
   `connection-form.test.tsx`.
2. Build the Destination form, the check, test and preview panels. Check: the component tests.
3. Build the profile's dialog, the lists, the Timeline and Audit log labels and the template preview option, and
   translate. Check: `make lint`, `make test-web`.
4. Write the Playwright specs. Check: `make e2e`, then Verification.

## Verification

Playwright against `muster dev` at 1280 and 360 px wide, signed in as the Admin:

1. Connections → New → Slack: name "ui", bot token `xoxb-muster-dev-slack-token-b`, Socket Mode, app-level token
   `xapp-muster-dev-slack-token-b`; "Check connection" shows "token ✓ · scopes ✓ · app_token ✓" and "Fake bot B",
   "Fake workspace B"; Save. Switch to HTTP: the app-level token field is gone, "Slack signing secret" and "Slack sends
   button presses to http://localhost:8081/api/v1/callbacks/slack/CN…" appear. Expected visible text in Russian with
   the language switched: «Проверить подключение», «Режим событий».
2. With `extra_connections` 1 on the fake server and a refresh of the socket, the page of "ui" in Socket Mode shows
   "Another program is connected to this Slack app through Socket Mode".
3. The edit form with only the proxy changed to the fake HTTP proxy: "Check connection" shows the step "dry_probe ✓"
   alone; the fake records no request with `xoxb-muster-dev-slack-token-b` (S-087's pattern).
4. Destinations → New → Slack, Connection "ui": the picker lists "alerts" and not "other"; typing `C0FAKEOTHER` and
   Save shows "Invite the bot to the channel: /invite @Fake bot B"; picking "alerts" saves "#alerts"; "Check" shows
   the item channel ✓; "Send test message" shows the step message and "Slack does not let a bot press its own button,
   so this test does not check presses."; Preview shows a red bar, "Ack" in green, "Resolve" and "Snooze…".
5. Profile → Messenger accounts → Link Slack → "ui": `alice@example.org`, "Send code"; the code from
   `/_fake/dms?user=U0FAKEALICE`; the list shows "Slack · ui · Alice Smith"; Unlink removes it. "nobody" shows "No
   Slack account with this email or name was found."
6. At 360 px: the Slack Connection page, the Destination form and the link dialog have no horizontal scrollbar
   (`document.documentElement.scrollWidth <= 360`).

Live, against the maintainer's test workspace, without typing a secret: the Connection "test workspace" (L) and the
Destination "live-alerts" (LD) of S-098, made through the API, show on their pages "muster", the workspace's name and
"#alerts"; "Check" on both passes; the profile of the Admin shows the Slack link of S-102.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): add slack connection and destination pages and linking slack accounts`.
- The operation of the check before saving is S-105, a backend story, so that this one stays a frontend story; the
  panel here only sends the form's draft.
- With 34 files the story stays under the size signal; the Connection and Destination halves share the check panels
  and the labels, which splitting would touch twice.
- The live part types no token: Playwright traces record what is typed, so a real token never goes through a form in
  a recorded run.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-24.FR-1 | partial | the pages; the API is S-097 |
| C-24.FR-2 | partial | the page and "Check connection" in the forms; the operation is S-105 |
| C-24.FR-3 | partial | the form and its picker |
| C-24.FR-7 | partial | `mrkdwn` in the Route editor's preview |
| C-24.FR-12 | partial | the profile's dialog and the lists |
| C-24.FR-13 | partial | the test panel's note and the preview panel |
| C-24.AC-2 | partial | the forms; the API is S-105 |
| C-24.AC-9 | partial | "Check" on the page; the probe is S-099 |
| C-24.AC-16 | partial | the panel's note; the message S-099, the press S-100 |
| C-24.AC-18 | full | |
| C-13.FR-14 | partial | the forms' check of a Slack draft; the operation is S-105 |
