---
id: S-072
title: "Compact top navigation: Access entry, Profile in the user menu, Audit log under Organization (FE)"
capability: C-03
kind: fe
layer: L1
depends_on: [S-056]
covers: [C-03.FR-18, C-20.FR-1]
files_touched:
  - web/src/components/section-nav.tsx
  - web/src/components/organization-nav.tsx
  - web/src/components/organization-nav.test.tsx
  - web/src/components/access-nav.tsx
  - web/src/components/access-nav.test.tsx
  - web/src/components/app-shell.tsx
  - web/src/components/app-shell.test.tsx
  - web/src/routes/admin.access.tsx
  - web/src/routes/admin.users.tsx
  - web/src/routes/admin.service-accounts.tsx
  - web/src/routes/admin.oidc.tsx
  - web/src/routes/admin.audit-log.tsx
  - web/src/routes/admin.organization.index.tsx
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/support.ts
  - web/e2e/navigation.spec.ts
  - web/e2e/phone-width.spec.ts
  - web/e2e/profile.spec.ts
  - web/e2e/tokens.spec.ts
  - web/e2e/users.spec.ts
  - web/e2e/service-accounts.spec.ts
  - web/e2e/oidc-settings.spec.ts
  - web/e2e/audit-log.spec.ts
  - web/e2e/organization-settings.spec.ts
  - design/prd/l1/C-03-sign-in-and-access.md
  - design/prd/l1/C-04-api-tokens.md
  - design/prd/l1/C-20-organization-settings-and-security.md
  - design/stories/README.md
  - design/stories/coverage.md
  - design/stories/S-072-compact-top-navigation.md
acceptance:
  - "[C-03.FR-18] An Admin's main navigation shows exactly eight entries, in this order: \"Alert Groups\", \"Statistics\", \"Integrations\", \"Routes\", \"Connections\", \"Destinations\", \"Access\", \"Organization\"; \"Profile\", \"Users\", \"Service accounts\", \"OIDC\" and \"Audit log\" are no longer main entries (`app-shell.test.tsx`, `navigation.spec.ts`)."
  - "[C-03.FR-18] \"Access\" opens a sub-navigation named \"Access\" with \"Users\", \"Service accounts\" and \"OIDC\", each shown only with `users:read`, `service-accounts:read` and `oidc:read`. \"Access\" is shown when the session holds any of them and leads through `/admin/access` to the first sub-entry the session may open (`/admin/users` for an Admin). It is marked active on `/admin/users`, `/admin/users/{id}`, `/admin/service-accounts`, `/admin/service-accounts/{id}` and `/admin/oidc`, and each of these pages shows the sub-navigation above it (`access-nav.test.tsx`, `app-shell.test.tsx`, `navigation.spec.ts`, `service-accounts.spec.ts`)."
  - "[C-20.FR-1] The sub-navigation of Organization ends with \"Audit log\" (`audit-log:read`), twelve entries for an Admin; `/admin/audit-log` shows that sub-navigation above the Audit log and marks \"Organization\" active, and \"Organization\" is shown to a session that holds only `audit-log:read` (`organization-nav.test.tsx`, `app-shell.test.tsx`, `organization-settings.spec.ts`, `navigation.spec.ts`)."
  - "[C-03.FR-18] \"Profile\" opens from the user menu for an Admin, a Responder and a Viewer and leads to `/profile` with the heading \"Profile\" (`navigation.spec.ts`, `profile.spec.ts`, `tokens.spec.ts`)."
  - "[C-03.FR-18, C-20.FR-1] A Responder and a Viewer see seven entries, the eight without \"Access\", and under \"Organization\" only \"Lookup tables\" and \"Link rules\"; for them `/admin/access`, `/admin/users`, `/admin/service-accounts`, `/admin/oidc` and `/admin/audit-log` show \"You do not have permission to see this page.\" (`app-shell.test.tsx`, `navigation.spec.ts`, `oidc-settings.spec.ts`)."
  - "[C-03.FR-18] At 1280 px wide the Admin's main navigation fits without clipping in English and in Russian: its `scrollWidth` is at most its `clientWidth` (`navigation.spec.ts`)."
  - "[C-03.FR-18, C-20.FR-1] At 360 px wide the page does not scroll sideways, in English or Russian; the main navigation scrolls inside itself, \"Access\" and \"Organization\" can be scrolled into view and opened, and both sub-navigations wrap so that every entry is in view without scrolling (`phone-width.spec.ts`, `navigation.spec.ts`)."
  - "[C-03.FR-18] The addresses `/admin/users`, `/admin/users/{id}`, `/admin/service-accounts`, `/admin/service-accounts/{id}`, `/admin/oidc`, `/admin/audit-log` and `/profile` open their pages as before (`navigation.spec.ts`, `users.spec.ts`, `service-accounts.spec.ts`, `oidc-settings.spec.ts`, `audit-log.spec.ts`, `profile.spec.ts`)."
  - "Every new text exists in English and Russian, and no Content Security Policy violation is reported (`navigation.spec.ts`)."
verify: "make ci e2e"
operator_attention: false
issue: 220
---

# S-072. Compact top navigation: Access entry, Profile in the user menu, Audit log under Organization (FE)

## Scope

**IN**

- The main navigation is cut to eight entries. Users, Service accounts and OIDC move into one "Access" entry with a sub-navigation built like that of Organization. The Audit log moves into the sub-navigation of Organization. Profile is no longer a main entry; it stays in the user menu.
- One shared sub-navigation component that both Organization and Access use. The `/admin/access` address leads to the first sub-entry the session may open.
- The main navigation fits at 1280 px in English and Russian, and nothing scrolls sideways at 360 px.

**OUT**

- The contents of the moved pages: Users, OIDC settings and Audit log (S-015), Service accounts (S-017), Profile and the user menu (S-014), the Organization pages (S-056).
- Any change of address: every existing address keeps working; `/admin/access` is the only new one.

## Contracts

- **Main navigation** (`NAVIGATION`, `app-shell.tsx`). In order; each entry is shown only with its Permission. An entry with a sub-navigation is shown when the session holds the Permission of any of its sub-entries, and is marked active on its address and on every sub-entry:

  | Entry (EN / RU) | Route | Permission | Active on |
  |---|---|---|---|
  | Alert Groups / Группы алертов | `/alert-groups` | `alert-groups:read` | |
  | Statistics / Статистика | `/statistics` | `alert-groups:read` | |
  | Integrations / Интеграции | `/integrations` | `integrations:read` | |
  | Routes / Маршруты | `/routes` | `routes:read` | |
  | Connections / Подключения | `/connections` | `connections:read` | |
  | Destinations / Места доставки | `/destinations` | `destinations:read` | |
  | Access / Доступ | `/admin/access` | any of the Access sub-entries | `/admin/access` and every Access sub-entry |
  | Organization / Организация | `/admin/organization` | any of the Organization sub-entries, now with `audit-log:read` | `/admin/organization/*`, `/admin/system-status`, `/admin/audit-log` |

  "Profile" (`/profile`) stays only in the user menu (`user-menu.tsx`, unchanged), for every signed-in user.
- **Access sub-navigation** (`access-nav.tsx`): a `navigation` landmark named "Access" («Доступ»). The layout routes `admin.users.tsx` and `admin.service-accounts.tsx` render it above their pages and their detail pages. `admin.oidc.tsx` renders it itself, as System status does for Organization. `admin.access.tsx` leads to the first sub-entry the session may open; a session that may open none sees "No access" and "You do not have permission to see this page.".

  | Entry (EN / RU) | Route | Permission | Page |
  |---|---|---|---|
  | Users / Пользователи | `/admin/users`, `/admin/users/{id}` | `users:read` | S-015 |
  | Service accounts / Сервисные аккаунты | `/admin/service-accounts`, `/admin/service-accounts/{id}` | `service-accounts:read` | S-017 |
  | OIDC / OIDC | `/admin/oidc` | `oidc:read` | S-015 |

- **Organization sub-navigation** (`organization-nav.tsx`): S-056's eleven entries, then one more. `/admin/audit-log` keeps its address and renders the sub-navigation above the page, as `/admin/system-status` does:

  | Entry (EN / RU) | Route | Permission | Page |
  |---|---|---|---|
  | Audit log / Журнал аудита | `/admin/audit-log` | `audit-log:read` | S-015 |

- **Shared sub-navigation** (`section-nav.tsx`). It provides:
  - the entry type (address, label, one Permission) and the filter by Permissions;
  - the first page the session may open, and the redirect a section's address makes to it;
  - the landmark, which wraps onto several lines on a narrow screen;
  - the layout that puts the sub-navigation above a page.

  `organization-nav.tsx` and `access-nav.tsx` only list their entries and texts. A sub-navigation with no entry the session may open is not rendered.
- **What each Role sees** (Permissions of [reference.md](../prd/l1/reference.md#roles-and-permissions)):
  - **Admin:** all eight entries. Access shows Users, Service accounts and OIDC and opens Users. Organization shows its twelve entries and opens General.
  - **Responder and Viewer:** seven entries (no Access). Organization shows Lookup tables and Link rules only and opens Lookup tables.
- **Fit at 1280 px:** the header row (`app-shell.tsx`) takes `max-w-7xl` (from `max-w-6xl`), so at 1280 px it spans the whole width and the main navigation gains about 128 px; the page content below keeps `max-w-6xl`. The entries keep their spacing, and the word "Muster" next to the mark stays.
- **Texts:**
  - New: `nav.access`, `access.nav.label`, `access.nav.users`, `access.nav.serviceAccounts`, `access.nav.oidc`, `organization.nav.auditLog`.
  - Removed, as no code uses them any more: `nav.users`, `nav.serviceAccounts`, `nav.oidc`, `nav.auditLog`.
  - Kept: `nav.profile`, which the user menu uses.
- **Specs.** Specs that clicked a moved entry now open "Access" or "Organization" first and then the sub-entry; specs that clicked "Profile" open it from the user menu through a helper in `support.ts`.

## Steps

1. Write the shared sub-navigation, rebuild Organization's on it with "Audit log" last, let `admin.organization.index.tsx` use the shared redirect and render the sub-navigation on the Audit log page. Check: `organization-nav.test.tsx`.
2. Write the Access sub-navigation, `/admin/access`, the two layout routes and the sub-navigation on OIDC, with the texts. Check: `access-nav.test.tsx`; `pnpm --dir web generate` nests the users and service-accounts pages under their layouts at unchanged addresses, and `pnpm --dir web typecheck` passes.
3. Cut `NAVIGATION` to the eight entries, widen the header row and remove the unused texts. Check: `app-shell.test.tsx`; `pnpm --dir web lint:i18n`.
4. Update the specs, write `navigation.spec.ts` and extend `phone-width.spec.ts`. Check: Verification steps 1–7.
5. Update the UI paragraphs of C-03, C-04 and C-20, the story map and the coverage table. Check: the links resolve and the Operations count reads 10.

## Verification

Run `make dev` and sign in as `admin@example.org` / `muster-dev-password`. Then in Playwright, at 1280 × 800 unless stated:

1. **Main navigation (EN).** The main navigation shows exactly "Alert Groups", "Statistics", "Integrations", "Routes", "Connections", "Destinations", "Access", "Organization", and its `scrollWidth` ≤ `clientWidth`. User menu "admin" → "Profile" → `/profile` with the heading "Profile".
2. **Access.**
   - "Access" → `/admin/users`, with "Access" marked as the current page.
   - The sub-navigation "Access" shows "Users", "Service accounts", "OIDC".
   - "admin" in the Users table → `/admin/users/{id}`, with the sub-navigation shown and "Users" active.
   - "Service accounts" → `/admin/service-accounts`; "OIDC" → `/admin/oidc` with the heading "OIDC".
   - `/admin/access` → `/admin/users`.
3. **Audit log under Organization.** "Organization" → `/admin/organization/general`; its sub-navigation ends with "System status", "Audit log". "Audit log" → `/admin/audit-log` with the heading "Audit log", and "Organization" marked as the current page.
4. **Old addresses.** Typed into the address bar, `/admin/users`, `/admin/service-accounts`, `/admin/oidc`, `/admin/audit-log` and `/profile` show "Users", "Service accounts", "OIDC", "Audit log" and "Profile".
5. **Russian.**
   - Profile → Language "Русский". The main navigation «Основная» shows «Группы алертов», «Статистика», «Интеграции», «Маршруты», «Подключения», «Места доставки», «Доступ», «Организация», and its `scrollWidth` ≤ `clientWidth`.
   - «Доступ» shows «Пользователи», «Сервисные аккаунты», «OIDC». «Организация» ends with «Журнал аудита».
   - At 360 px, `/admin/users` and `/admin/audit-log` do not scroll sideways.
   - Language back to English.
6. **Responder and Viewer.** As a Responder (`nav-rita`) and a Viewer (`nav-vera`), created with setup links:
   - The main navigation shows the seven entries without "Access".
   - "Organization" shows only "Lookup tables" and "Link rules".
   - `/admin/access`, `/admin/users`, `/admin/service-accounts`, `/admin/oidc` and `/admin/audit-log` show "You do not have permission to see this page.".
   - User menu → "Profile" → `/profile`.
7. **360 × 740, Admin, EN** (`phone-width.spec.ts`).
   - The page does not scroll sideways, and the main navigation scrolls inside itself.
   - "Access" scrolls into view and opens `/admin/users`. Its sub-navigation does not scroll and its three entries are in view; "OIDC" → no sideways scroll.
   - "Organization" → its twelve entries are in view. "Audit log" → `/admin/audit-log`, no sideways scroll.

`make e2e` runs steps 1–6 as `web/e2e/navigation.spec.ts` and step 7 in `web/e2e/phone-width.spec.ts`; the updated specs pass.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): compact the top navigation with access and organization groups`.
- **D314 (binding).** The main navigation becomes Alert Groups, Statistics, Integrations, Routes, Connections, Destinations, Access, Organization, so that it fits at 1280 px:
  - Profile is reached from the user menu.
  - Users, Service accounts and OIDC go under "Access", like Organization.
  - The Audit log goes under Organization.

  An Admin's main navigation goes from 12 entries to 8, and a Responder's or Viewer's from 8 to 7.
- **Fit estimate at 1280 px.** With the header row at `max-w-6xl` the navigation has about 865 px between the mark and the user menu "admin": English needs about 720–790 px, Russian about 835 px with Liberation Sans metrics and about 930 px with DejaVu Sans, the likely font of the Linux CI. At `max-w-7xl` it has about 990 px, enough for both; `navigation.spec.ts` measures it.
- **Choices made in this story** (small and reversible):
  - "Audit log" comes last in Organization, after System status.
  - "Access" keeps the order of the old entries.
  - Sub-entry labels have their own texts (`access.nav.*`, `organization.nav.auditLog`), as in S-056.
  - The header row is widened to `max-w-7xl` rather than tightening the entries or hiding the word "Muster".
  - The old `nav.*` texts are removed, because `lint:i18n` keeps exactly the keys the code uses.
- The UI paragraphs of C-03, C-04 and C-20 list where these pages live, so they are updated to match D314 in this pull request.
- `docs/sign-in/oidc-keycloak.md` names the pages ("OIDC settings", "Users → Create"), not the navigation, and stays as it is.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-03.FR-18 | partial | the main navigation of eight entries with Access, and Profile in the user menu; with S-012, S-014 and S-015 complete |
| C-20.FR-1 | partial | the Audit log in the sub-navigation of Organization; with S-055 and S-056 complete |
