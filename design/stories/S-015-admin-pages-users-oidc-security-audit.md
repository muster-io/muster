---
id: S-015
title: Users, OIDC settings, Organization security and Audit log pages (FE)
capability: C-03
kind: fe
layer: L1
depends_on: [S-014]
covers: [C-03.FR-3, C-03.FR-5, C-03.FR-6, C-03.FR-8, C-03.FR-11, C-03.FR-13, C-03.FR-15, C-03.FR-18, C-03.FR-19, C-03.FR-20, C-03.FR-21, C-03.FR-29, C-03.FR-32, C-03.AC-7, C-03.AC-26, C-02.FR-22]
files_touched:
  - web/src/components/data-table.tsx
  - web/src/components/secret-field.tsx
  - web/src/components/proxy-form.tsx
  - web/src/components/proxy-form.test.tsx
  - web/src/components/user-create-dialog.tsx
  - web/src/components/setup-link-dialog.tsx
  - web/src/components/group-mapping-editor.tsx
  - web/src/components/audit-diff.tsx
  - web/src/routes/admin.users.tsx
  - web/src/routes/admin.users.$userId.tsx
  - web/src/routes/admin.oidc.tsx
  - web/src/routes/admin.organization.security.tsx
  - web/src/routes/admin.audit-log.tsx
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/users.spec.ts
  - web/e2e/oidc-settings.spec.ts
  - web/e2e/audit-log.spec.ts
acceptance:
  - "[C-03.FR-3] The Users page lists users with Role, source, sign-in method, last sign-in, TOTP and status, filtered by a search and by Role, status and source; creating a user shows the password setup link once, with a copy button."
  - "[C-03.FR-3, C-03.FR-11, C-03.FR-13, C-03.FR-29] A user's page offers Role change, disable, enable, delete (confirming that the name becomes `deleted-user-<id>`), reset TOTP, convert to local (showing the new setup link) and a new setup link; without `users:write` none of them is shown."
  - "[C-03.FR-5, C-03.FR-6] The OIDC settings page edits every setting, with a group mapping editor; the client secret shows only whether it is set and when it changed and can be replaced; with an empty mapping and no Role for unmatched users the page shows \"Nobody will be able to sign in through OIDC.\""
  - "[C-03.AC-7] With a client secret expiry date 10 days ahead the OIDC settings page shows \"The client secret expires on {date}. Issue a new one in the identity provider and enter it here.\"; with a date 30 days ahead it does not."
  - "[C-03.FR-32, C-03.AC-26] While the OIDC settings carry `last_admin_kept`, the OIDC settings page shows \"{user} stays Admin: they are the last active Admin, and the identity provider maps them to {role}. Make another user Admin first.\" with the user's name and the Role, and the warning disappears once another Admin is active and the user signed in again."
  - "[C-03.FR-8, C-03.FR-19] \"Check connection\" shows the result with its path (direct or through the proxy) and latency; the proxy form — use a proxy, type, address, username and a write-only password — is a shared component."
  - "[C-03.FR-20] Organization → Security edits the TOTP policy (nobody, local users, everyone); saving over a newer version shows a conflict message instead of overwriting it."
  - "[C-03.FR-15] The Audit log page lists entries newest first for a time range (by default the last 7 days) with actor, action and resource filters kept in the URL, and shows the actor, the Transport and the diff, in which a Secret appears only as changed."
  - "[C-03.FR-21] No page shows a secret value; every secret field is a write-only input."
  - "[C-03.FR-18] A user without the Permissions of these pages sees none of their navigation entries, and opening one of their addresses shows \"You do not have permission to see this page.\""
verify: "make ci e2e"
operator_attention: false
issue: 15
---

# S-015. Users, OIDC settings, Organization security and Audit log pages (FE)

## Scope

**IN**

- The Users list and the user page with every administrative action of C-03.
- The OIDC settings page with the group mapping editor, the client secret expiry and its warning, the warnings of
  C-03.FR-6, the proxy form and the connection check.
- Organization → Security with the TOTP policy.
- The Audit log page with its filters.
- Shared components later pages reuse: the data table with cursor pagination and URL filters, the write-only secret
  field and the proxy form.

**OUT**

- Account links on the user page (S-052); Service accounts (S-017).
- The token grace period and the Keyring on the Security page (S-056); the other Organization settings (S-054, S-056).

## Contracts

- **API used**: `listUsers`, `createUser`, `getUser`, `updateUser`, `deleteUser`, `disableUser`, `enableUser`,
  `resetUserTotp`, `convertUserToLocal`, `createPasswordSetupLink`, `listRoles`, `getOidcSettings`,
  `updateOidcSettings`, `checkOidcSettings`, `getOrganization`, `updateOrganization`, `listAuditLog`.
- **Routes and navigation entries** (shown only with the Permission):

  | Route | Permission | Content |
  |---|---|---|
  | `/admin/users` | `users:read` | the list with filters in the URL; "Create user" (`users:write`) |
  | `/admin/users/$userId` | `users:read` | details and the actions of C-03.FR-3, FR-11, FR-13 and FR-29 (`users:write`) |
  | `/admin/oidc` | `oidc:read` | the settings form (`oidc:write`), warnings, "Check connection" |
  | `/admin/organization/security` | `organization:write` | the TOTP policy |
  | `/admin/audit-log` | `audit-log:read` | the list with filters in the URL |

- **Texts**: the OIDC warnings of [reference.md](../prd/l1/reference.md#banners-warnings-and-notices) — "Nobody will be
  able to sign in through OIDC.", "The client secret expires on {date}. Issue a new one in the identity provider and
  enter it here." and, for `last_admin_kept`, "{user} stays Admin: they are the last active Admin, and the identity
  provider maps them to {role}. Make another user Admin first."; a stale save (`412`) shows "Someone else changed these
  settings. Reload to see them."; a missing Permission shows "You do not have permission to see this page."
- **Shared components**: `data-table` (TanStack Table with `next_cursor` paging and typed search parameters in the URL);
  `secret-field` (shows "Set, changed {time}" or "Not set", with "Replace" and "Clear"; never shows a value);
  `proxy-form` (C-03.FR-19: use a proxy, type `http`/`https`/`socks5`, address, username, password through
  `secret-field`), reused by Connections, outgoing webhooks and the outgoing heartbeat.
- **Forms** follow ADR-0009: React Hook Form with the generated Zod schemas; `Problem` errors map onto fields; updates
  send `If-Match` from the item's `etag`.

## Steps

1. Write the shared table, secret field and proxy form. Check: the component test covers the proxy form's states and
   that a stored password is never shown.
2. Write the Users list, the create dialog and the user page with its actions. Check: Playwright runs the users scenario
   of Verification.
3. Write the OIDC settings page. Check: Playwright sees each warning appear and disappear and the check through the
   proxy.
4. Write Organization → Security and the Audit log page. Check: Playwright saves the policy, sees the conflict message
   and filters the Audit log.

## Verification

Run `make dev`, sign in as `admin@example.org` / `muster-dev-password`, then in Playwright:

1. The navigation shows "Users", "OIDC", "Security" and "Audit log".
2. Users → "Create user" → Name "Dana", Login "dana", Role "Viewer" → "Create" → a dialog shows "Password setup link"
   with a URL that starts with `http://localhost:8080/password-setup#token=` and "This link is shown once." → close →
   the list has a row "dana" with Role "Viewer" and source "Local".
3. Open "dana" → "Disable" → status "Disabled" → "Enable" → status "Active" → "Delete" → the confirmation says the name
   becomes "deleted-user-…" → "Delete" → the list shows "deleted-user-SR…".
4. OIDC → set "Client secret expires on" to a date 10 days ahead → "Save" → visible "The client secret expires on" with
   that date; set it 30 days ahead → "Save" → the warning is gone.
5. OIDC → remove every group mapping and set "Role for users without a matching group" to "None" → "Save" → visible
   "Nobody will be able to sign in through OIDC."; restore the mapping.
6. OIDC → Proxy → "Use a proxy", type "SOCKS5", address `127.0.0.1:18092` → "Save" → "Check connection" → visible
   "Connected through the proxy" with a latency in ms.
7. Security → "TOTP required" → "Everyone" → "Save" → visible "Saved". In a second browser context change it to
   "Nobody"; back in the first, change it again and "Save" → visible "Someone else changed these settings. Reload to
   see them."
8. Sign in through the fake IdP as "ada", the only active Admin, while it maps her to `oncall` (set up as in S-013) →
   OIDC shows "ada stays Admin: they are the last active Admin, and the identity provider maps them to Responder. Make
   another user Admin first." → Users → give "admin@example.org" the Role "Admin" → sign "ada" in again → sign in as
   "admin@example.org" → OIDC no longer shows the warning, and Users lists "ada" as "Responder".
9. Audit log → action `oidc_settings.updated` → the newest row shows the actor "admin" and, in its diff, "Client
   secret: changed" without a value; the URL carries the filter.
10. Sign in as a Responder → none of the four entries is in the navigation; opening `/admin/users` shows "You do not
    have permission to see this page."

`make e2e` runs these steps as `web/e2e/users.spec.ts`, `oidc-settings.spec.ts` and `audit-log.spec.ts`.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): add users, oidc settings, security and audit log pages`.
- The user page is the place where S-052 adds the user's Account links.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-03.FR-3 | full | together with S-010 and S-011 |
| C-03.FR-5 | full | together with S-013; the Internal alert is S-053 |
| C-03.FR-6 | full | together with S-013 |
| C-03.FR-8 | full | together with S-013 |
| C-03.FR-11 | full | together with S-011, S-012 and S-013 |
| C-03.FR-13 | partial | the page; the effects on tokens, links, Reminders and Owners come with S-016, S-051, S-049 and S-032 |
| C-03.FR-15 | full | together with S-011 |
| C-03.FR-18 | partial | the Permission-filtered entries of these pages; the shell is S-014 |
| C-03.FR-19 | full | together with S-013 |
| C-03.FR-20 | full | together with S-012 |
| C-03.FR-21 | full | together with S-013 |
| C-03.FR-29 | full | together with S-013 and S-014 |
| C-03.FR-32 | full | together with S-013 |
| C-03.AC-7 | full | together with S-013 |
| C-03.AC-26 | full | together with S-013 |
| C-02.FR-22 | partial | the shared proxy form |
