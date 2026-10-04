---
id: S-052
title: Messenger accounts in the profile and Account links on the user page (FE)
capability: C-18
kind: fe
layer: L1
depends_on: [S-015, S-051]
covers: [C-18.FR-2, C-18.FR-3, C-18.FR-6, C-18.FR-10, C-03.FR-12]
files_touched:
  - web/src/components/account-links-table.tsx
  - web/src/components/profile-account-links.tsx
  - web/src/components/link-telegram-dialog.tsx
  - web/src/components/link-mattermost-dialog.tsx
  - web/src/components/link-mattermost-dialog.test.tsx
  - web/src/routes/profile.tsx
  - web/src/routes/admin.users.$userId.tsx
  - web/src/lib/live.ts
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/account-links.spec.ts
acceptance:
  - "[C-18.FR-10, C-03.FR-12] The profile has a \"Messenger accounts\" section listing the user's links with the messenger, Connection, username and last use, and \"Link Telegram\" and \"Link Mattermost\" for the Connections of each type, taken from `listLinkableConnections`, which needs no Permission."
  - "[C-18.FR-2] \"Link Telegram\" for a chosen Telegram Connection shows the `t.me` link as a button and a QR code with the time it expires; when the bot links the account, the dialog closes and the list shows the link without a reload."
  - "[C-18.FR-3] \"Link Mattermost\" asks for the Connection and the `@username`, then for the code the bot sent, with the attempts left; a wrong code shows \"The code is wrong or no longer valid. Request a new code.\", an account linked to another user shows \"This account is linked to another user. Its owner or an admin can unlink it.\", and the limit shows \"Too many codes were requested. Try again in {minutes} minutes.\""
  - "[C-18.FR-6] \"Unlink\" removes one of the user's own links after a confirmation; an Admin's user page lists that user's Account links with \"Remove\" and offers no way to create one."
verify: "make ci e2e"
operator_attention: false
issue: 52
---

# S-052. Messenger accounts in the profile and Account links on the user page (FE)

## Scope

**IN**

- The "Messenger accounts" section of the profile: the list, linking Telegram through the deep link, linking Mattermost
  with the code, unlinking.
- The Account links section of the user page for Admins.

**OUT**

- The linking itself, the bots and the presses (S-051).

## Contracts

- **API used**: `listMyAccountLinks`, `deleteMyAccountLink`, `startTelegramLink`, `startMattermostLink`,
  `confirmMattermostLink`, `listUserAccountLinks`, `deleteUserAccountLink`, `listLinkableConnections` (no Permission;
  only the id, the name and the messenger of each Connection, for the Connection pickers), the `account-links` hint.
  The profile never calls `listConnections`.
- **List** (C-18.FR-10; `account-links-table.tsx`, shared by both pages): columns Messenger, Connection, Username
  ("@{username}", or the external id without one), Last used (relative, "Never" when empty), Linked (date); the profile
  adds "Unlink", the user page "Remove", both behind a confirmation ("Unlink @{username} from Muster? Presses from this
  account will no longer act as you." / "Remove this link? Presses from @{username} will no longer act as {user}.").
- **Telegram** (C-18.FR-2; `link-telegram-dialog.tsx`): pick the Connection (skipped when there is one), call
  `startTelegramLink`, show "Open Telegram" linking to `deep_link`, the QR code of the same URL (the component of S-014)
  and "This link works once and expires at {time}."; the `account-links` hint closes the dialog and refreshes the list;
  after `expires_at` the dialog offers "Get a new link".
- **Mattermost** (C-18.FR-3; `link-mattermost-dialog.tsx`): step 1 — Connection and "Your Mattermost username"; step 2
  — "Enter the code the bot sent you in Mattermost" with "{n} attempts left" and "Request a new code". Problem codes:
  `link_code_invalid` → "The code is wrong or no longer valid. Request a new code."; `account_linked_elsewhere` → "This
  account is linked to another user. Its owner or an admin can unlink it."; `messenger_user_not_found` → "No user
  @{username} on this Mattermost server."; `429` → "Too many codes were requested. Try again in {minutes} minutes." from
  `Retry-After`; `503` → "The messenger is busy. Try again in a few seconds."
- **User page** (C-18.FR-6; `admin.users.$userId.tsx`): an "Account links" section with `users:read`, "Remove" with
  `users:write`; no "Link" control exists there.
- **Phone width**: the section and both dialogs fit 360 pixels.

## Steps

1. Write the shared list and the profile section with unlinking. Check: Playwright step 1 and step 5.
2. Write the Telegram dialog with the hint. Check: Playwright step 2.
3. Write the Mattermost dialog with its errors. Check: `link-mattermost-dialog.test.tsx` maps every problem code;
   Playwright steps 3 and 4.
4. Add the section of the user page. Check: Playwright step 6.

## Verification

Run `make dev`, sign in as `admin@example.org` / `muster-dev-password`; create the Responder "alice" (display name
"Alice Smith") and the Mattermost Connection "mm" of S-039 from a terminal as in S-051. Then in Playwright, signed in as
"alice":

1. Profile → "Messenger accounts" shows "No messenger accounts linked yet." and "Link Telegram", "Link Mattermost".
2. "Link Telegram" → "Dev Telegram" → the dialog shows "Open Telegram" with a link that starts with
   `https://t.me/muster_dev_bot?start=` and "This link works once and expires at {time}." → from a terminal, `PRIV 7001
   alice_t "/start <token from the link>"` (S-051) → the dialog closes and the list shows "Telegram · Dev Telegram ·
   @alice_t · Never".
3. "Link Mattermost" → "mm", "@alice" → "Send code" → "Enter the code the bot sent you in Mattermost" and "5 attempts
   left" → "22222222" → "The code is wrong or no longer valid. Request a new code." and "4 attempts left" → the code from
   `curl -s 127.0.0.1:18065/_fake/dms | jq -r '.[-1].message'` → the list shows "Mattermost · mm · @alice".
4. "Link Mattermost" → "mm", "@bob" while "bob" is linked to the Responder "bob" (S-051) → "This account is linked to
   another user. Its owner or an admin can unlink it."
5. "Unlink" on the Mattermost row → confirm → the row disappears.
6. As the Admin: Users → "alice" → "Account links" lists "Telegram · Dev Telegram · @alice_t" with "Remove" and no
   "Link" control → "Remove" → confirm → "No messenger accounts linked."; as "alice", the profile list is empty without a
   reload.
7. At 360 × 740 pixels the profile section and both dialogs fit, and `document.documentElement.scrollWidth` equals the
   viewport width.

`make e2e` runs these steps as `web/e2e/account-links.spec.ts`, sending `/start` and reading the code through the fake
servers' control endpoints.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): add messenger accounts to the profile and account links to the user page`.
- The profile never sees a token or a code it did not receive from the API or from the user; the deep link is shown
  only in the open dialog and is not stored in the page's state after it closes.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-18.FR-2 | partial | the profile; with S-051 complete |
| C-18.FR-3 | partial | the profile; with S-051 complete |
| C-18.FR-6 | partial | the profile and the user page; with S-051 complete |
| C-18.FR-10 | partial | the profile list; with S-051 complete |
| C-03.FR-12 | partial | Account links join the profile |
