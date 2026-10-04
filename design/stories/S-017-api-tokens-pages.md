---
id: S-017
title: Personal access tokens and Service accounts pages (FE)
capability: C-04
kind: fe
layer: L1
depends_on: [S-015, S-016]
covers: [C-04.FR-1, C-04.FR-2, C-04.FR-3, C-04.FR-6, C-04.FR-7, C-04.FR-8, C-04.AC-2, C-03.FR-12]
files_touched:
  - web/src/components/profile-tokens.tsx
  - web/src/components/permission-picker.tsx
  - web/src/components/token-created-dialog.tsx
  - web/src/components/token-created-dialog.test.tsx
  - web/src/components/service-account-dialog.tsx
  - web/src/routes/profile.tsx
  - web/src/routes/admin.service-accounts.tsx
  - web/src/routes/admin.service-accounts.$serviceAccountId.tsx
  - web/src/routes/admin.audit-log.tsx
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/tokens.spec.ts
  - web/e2e/service-accounts.spec.ts
acceptance:
  - "[C-04.FR-3, C-04.AC-2] Creating a token in the profile shows its value once, with a copy button and \"You will not see this token again.\"; after the dialog closes, the list shows the token without its value."
  - "[C-04.FR-3] A token without an expiry date shows \"This token never expires.\" in the list, and the creation form shows the same warning while no expiry is chosen."
  - "[C-04.FR-1, C-04.FR-7] The Permission picker offers only the Permissions the user holds."
  - "[C-04.FR-3] The list shows when and from which address each token was last used, and \"Revoke\" removes a token after a confirmation."
  - "[C-04.FR-8] For an OIDC account without an offline token, the profile shows the date until which its tokens work without a new OIDC sign-in."
  - "[C-04.FR-2] Admin → Service accounts lists the accounts with Role, status and token count and creates one with a Role; an account's page creates tokens (the value shown once), revokes them, changes the Role, and disables, enables and deletes the account."
  - "[C-04.FR-6] The Audit log page shows an action made with a token as \"{user} via token {name}\", and one made with a Service account token with the Service account and the token."
verify: "make ci e2e"
operator_attention: false
issue: 17
---

# S-017. Personal access tokens and Service accounts pages (FE)

## Scope

**IN**

- Profile → Personal access tokens: list, create with name, expiry and Permissions, the one-time display, revoke, the
  warning for tokens without expiry, and the token date for OIDC accounts without an offline token.
- Admin → Service accounts: list, create, the account page with its tokens and lifecycle actions.
- Token attribution on the Audit log page.

**OUT**

- Everything on the API side (S-016).

## Contracts

- **API used**: `listPersonalAccessTokens`, `createPersonalAccessToken`, `revokePersonalAccessToken`, `getMe`,
  `getOrganization` (`oidc_token_grace_seconds`), `listServiceAccounts`, `createServiceAccount`, `getServiceAccount`,
  `updateServiceAccount`, `deleteServiceAccount`, `disableServiceAccount`, `enableServiceAccount`,
  `listServiceAccountTokens`, `createServiceAccountToken`, `revokeServiceAccountToken`, `listRoles`, `listAuditLog`.
- **Profile → Personal access tokens** (C-04.FR-1, FR-3, FR-7, FR-8): a section of `/profile`, available only in a web
  session. The creation form takes a name, an optional expiry date (quick choices of 30, 90 and 365 days) and the
  Permissions, chosen from the user's own (`Me.permissions`). The created dialog shows the value with a copy button and
  "You will not see this token again."; closing it drops the value from memory. Each row shows the name, the
  Permissions, the expiry or "This token never expires.", the last use and its address, and "Revoke". For an account
  that signs in through OIDC without an offline token (`User.oidc_offline_access` false), the section shows "Your tokens
  work until {date} unless you sign in through OIDC again.", the date being the last sign-in plus
  `auth.oidc_token_grace`.
- **Admin → Service accounts** (C-04.FR-2), navigation entry with `service-accounts:read`:

  | Route | Content |
  |---|---|
  | `/admin/service-accounts` | list with name, Role, status and token count; "Create service account" (name, Role) with `service-accounts:write` |
  | `/admin/service-accounts/$serviceAccountId` | Role change (`If-Match`), disable, enable, delete; tokens: create (name, optional expiry; value shown once), list with last use, revoke |

- **Audit log** (C-04.FR-6): an entry with `token_name` shows "{user} via token {name}" for a user and "{service
  account} · token {name}" for a Service account.

## Steps

1. Write the Permission picker and the created-token dialog. Check: the component test shows the value once and gone
   after closing.
2. Add the tokens section to the profile. Check: Playwright creates, lists and revokes a token and sees the expiry
   warning.
3. Write the Service accounts pages. Check: Playwright runs the Service account scenario of Verification.
4. Show token attribution on the Audit log page. Check: Playwright sees "admin via token …".

## Verification

Run `make dev`, sign in as `admin@example.org` / `muster-dev-password`, then in Playwright:

1. Profile → "Personal access tokens" → "Create token" → Name "laptop-scripts", no expiry → the form shows "This token
   never expires." → Permissions "users:read" and "alert-groups:read" → "Create" → a dialog shows a value starting with
   `mstr_pat_` and "You will not see this token again." → "Copy" → close → the list has "laptop-scripts" with "This
   token never expires." and no value.
2. With the copied value, `curl -s -H "Authorization: Bearer <value>" localhost:8080/api/v1/users -o /dev/null -w
   '%{http_code}'` prints `200`; reload the profile → the row shows a last use a moment ago from `127.0.0.1`.
3. Create a second token "with-expiry" with "90 days" → no warning on that row.
4. "Revoke" on "laptop-scripts" → confirm → the row is gone, and the `curl` above prints `401`.
5. Admin → "Service accounts" → "Create service account" → Name "terraform", Role "Admin" → open it → "Create token"
   "ci" → the value starts with `mstr_sat_` → close → "Disable" → status "Disabled" → "Enable" → "Active".
6. Create a user through the API with a Personal access token, then Audit log → action `user.created` → the newest row
   shows "admin via token <name>".
7. Sign in as the OIDC user `olga` through the fake IdP after `curl -s -X POST localhost:18090/_fake/config -d
   '{"grant_offline_access":false}'` → Profile → "Personal access tokens" shows "Your tokens work until" with the date
   seven days after the sign-in.

`make e2e` runs these steps as `web/e2e/tokens.spec.ts` and `service-accounts.spec.ts`.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): add personal access token and service account pages`.
- The API has no field for "tokens work until"; the page derives it from the last sign-in and the Organization's grace
  period. If S-016 finds a more exact source (`oidc_last_contact_at`), it adds a read-only field to `User`.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-04.FR-1 | full | together with S-016 |
| C-04.FR-2 | partial | the pages; command refusals are S-032 and S-049 |
| C-04.FR-3 | full | together with S-016 |
| C-04.FR-6 | full | together with S-016 |
| C-04.FR-7 | full | together with S-016 |
| C-04.FR-8 | full | together with S-016 |
| C-04.AC-2 | full | together with S-016 |
| C-03.FR-12 | partial | Personal access tokens in the profile |
