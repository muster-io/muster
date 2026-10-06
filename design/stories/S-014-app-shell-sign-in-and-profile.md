---
id: S-014
title: Application shell, sign-in, password setup, TOTP and profile (FE)
capability: C-03
kind: fe
layer: L1
depends_on: [S-013, S-062]
covers: [C-03.FR-7, C-03.FR-10, C-03.FR-12, C-03.FR-18, C-03.FR-24, C-03.FR-25, C-03.FR-26, C-03.FR-28, C-03.FR-29, C-03.AC-2, C-03.AC-9, C-03.AC-20, C-03.AC-22]
files_touched:
  - web/package.json
  - web/vitest.config.ts
  - web/playwright.config.ts
  - web/i18next.config.ts
  - web/src/main.tsx
  - web/src/i18n.ts
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/src/lib/api.ts
  - web/src/lib/live.ts
  - web/src/lib/time.ts
  - web/src/components/ui/**
  - web/src/components/app-shell.tsx
  - web/src/components/notice-banners.tsx
  - web/src/components/user-menu.tsx
  - web/src/components/qr-code.tsx
  - web/src/components/app-shell.test.tsx
  - web/src/routes/__root.tsx
  - web/src/routes/index.tsx
  - web/src/routes/sign-in.tsx
  - web/src/routes/sign-in.totp.tsx
  - web/src/routes/totp-enrolment.tsx
  - web/src/routes/password-setup.tsx
  - web/src/routes/profile.tsx
  - web/src/components/profile-totp.tsx
  - web/src/components/profile-sessions.tsx
  - web/e2e/sign-in.spec.ts
  - web/e2e/profile.spec.ts
  - NOTICE
  - Makefile
  - .github/workflows/ci.yml
acceptance:
  - "[C-03.FR-24, C-03.FR-25] The sign-in page shows the local form and, while OIDC is enabled, the button \"Sign in with Dev IdP\"; signing in as the development Admin lands on the home page with \"admin\" in the user menu."
  - "[C-03.FR-7, C-03.FR-28] After a refused OIDC sign-in the page shows \"You have no access to Muster. Contact your administrator.\" for `no_access` and \"An account with this login already exists. Sign in with it and link OIDC in your profile.\" for `login_taken`."
  - "[C-03.AC-2] With \"TOTP required: everyone\", a new local user who signs in reaches only the TOTP enrolment page: every other address leads back to it until the enrolment is confirmed."
  - "[C-03.FR-10] The enrolment page shows a QR code and the secret, accepts a code and shows the recovery codes once; a user with TOTP is asked for a code or a recovery code after the password."
  - "[C-03.FR-26] The password setup page reads the token from the URL fragment, sets the password, and shows its texts for an expired and a used link."
  - "[C-03.FR-18, C-03.AC-9] While the recovery notice is active every page shows \"Muster is recovering after downtime. Data may be incomplete until HH:MM.\" with the time in the user's time zone, and the banner disappears without a reload when the notice ends; Admins also see \"No replica is leading: Telegram polling, Heartbeat and Stale checks are paused.\" while it applies."
  - "[C-03.FR-18] The navigation shows only the entries whose Permission the user holds."
  - "[C-03.FR-12] The profile shows and edits the name, time zone and language (English or Russian; the UI switches at once), changes the password, lists sessions with \"Sign out everywhere\", manages TOTP and shows the sign-in method."
  - "[C-03.FR-29, C-03.AC-20] For an account with a password, \"Link OIDC\" first shows \"After linking you will sign in only through OIDC; your password will be removed.\"; after linking, the profile shows the sign-in method OIDC."
  - "[C-03.AC-22] When the IdP refuses the user at a background re-check, the next action in the SPA lands on the sign-in page."
  - "[NFR-8] Every text exists in English and Russian, Russian plural forms included, and `make lint` fails on a missing translation."
verify: "make ci e2e"
operator_attention: false
issue: 14
---

# S-014. Application shell, sign-in, password setup, TOTP and profile (FE)

## Scope

**IN**

- The application shell every later page lives in: navigation filtered by Permissions, the user menu, the banner area
  fed by system notices and live hints, language and time zone.
- The sign-in page with the local form, the OIDC button and the callback errors; the second-factor page; the TOTP
  enrolment page; the password setup page.
- The profile: details, time zone, language, password, sessions, TOTP, the sign-in method and "Link OIDC".
- Translations (English and Russian) with the CI check; Playwright end-to-end tests on `muster dev`.
- The first shadcn/ui components, copied under their own licence.

**OUT**

- Users, OIDC settings, Organization → Security and the Audit log pages (S-015).
- Personal access tokens in the profile (S-017) and Account links (S-052).
- The Alert Group list as the home page (S-030); until then the home page is a placeholder.

## Contracts

- **API used**: `getSignInOptions`, `createSession`, `getCurrentSession`, `deleteCurrentSession`, `submitSessionTotp`,
  `startOidcSignIn` (by navigation), `completePasswordSetup`, `getMe`, `updateMe`, `changePassword`, `listMySessions`,
  `deleteMySessions`, `getMyTotp`, `beginTotpEnrolment`, `confirmTotpEnrolment`, `regenerateTotpRecoveryCodes`,
  `removeTotp`, `startOidcLink`, `listSystemNotices`, `streamLiveUpdates`, through the generated orval client.
- **Client glue** (`web/src/lib/api.ts`): sends `X-CSRF-Token` from the current `Session` on mutating requests, maps a
  `Problem` and its `errors[]` onto form fields, and sends the browser to `/sign-in` on a `401` (with "Your session
  ended. Sign in again." for `session_expired` and `oidc_session_ended`).
- **Live hints** (`web/src/lib/live.ts`): one `EventSource` on `/api/v1/live-updates`; a hint invalidates the matching
  queries (`system-notices`, `organization`; later stories register theirs); after a reconnect every query is
  invalidated; a `401` closes it.
- **Shell** (C-03.FR-18): pages register their navigation entry with the Permission it needs, and the shell shows an
  entry only when the session holds it. The banner area shows the texts of
  [reference.md](../prd/l1/reference.md#banners-warnings-and-notices) for `recovering_after_downtime` (everyone) and
  `no_replica_leading` (returned only to Admins). Times are shown in the profile's time zone or the browser's.
- **Routes and texts**:

  | Route | Content |
  |---|---|
  | `/sign-in` | Login and password, "Sign in"; "Sign in with {display_name}" while OIDC is enabled; `?error=` texts: `no_access` and `login_taken` as quoted in C-03.FR-7 and FR-28; `account_disabled`, `oidc_disabled`, `invalid_request` and `idp_error` with texts written in this story; a wrong login, password or code shows "Wrong login, password or code."; `429` shows "Too many attempts. Try again in N seconds." |
  | `/sign-in/totp` | a code or a recovery code, then on to `return_to` |
  | `/totp-enrolment` | QR code and secret of the `otpauth://` URI, the code, then the recovery codes shown once |
  | `/password-setup` | reads `#token=` from the fragment and posts it only with the new password; texts for `link_expired` ("This link has expired. Ask your administrator for a new one.") and `link_used` ("This link was already used. Ask your administrator for a new one.") |
  | `/profile` | details, time zone, language, password (accounts with a password), sessions with "Sign out everywhere", TOTP (status, enrol, regenerate codes, remove with password or code), the sign-in method, "Link OIDC" with the warning of C-03.FR-29 and `?error=identity_linked_elsewhere` |
  | `/` | a placeholder home page until S-030 |

- **Limited sessions**: in the state `totp_required` every route leads to `/sign-in/totp`; in `totp_enrolment_required`
  to `/totp-enrolment` (C-03.AC-2).
- **Translations** (NFR-8): i18next with hierarchical keys in `web/src/locales/en.json` and `ru.json`, Russian plural
  forms through i18next's plural rules; `i18next-cli` in `make lint` fails on a key missing in either language.
- **Components**: shadcn/ui on Base UI copied into `web/src/components/ui/`, keeping their MIT header, excluded from the
  licence header check by path and listed in NOTICE (ADR-0001). A QR code library under the shipped licence list
  renders the enrolment code.
- **Tests**: Vitest in browser mode for the shell and the banner; Playwright specs against `muster dev` at desktop width
  and at 360 px (`make e2e`, now running Go and Playwright suites; CI installs Chromium).

## Steps

1. Add the client glue, translations, the shell with navigation, user menu and banners. Check: the component test shows
   the banner appear and disappear on a hint; `make lint` fails after deleting one Russian key.
2. Add the sign-in, second-factor and password setup pages. Check: Playwright signs in locally, through the fake IdP and
   with a second factor, and completes a setup link.
3. Add the enrolment page and the limited-session redirects. Check: Playwright runs the C-03.AC-2 scenario.
4. Add the profile with its sections and "Link OIDC". Check: Playwright edits each section and sees the link warning.
5. Add the copied components to NOTICE and wire Playwright into `make e2e` and CI. Check: `make ci e2e` passes.

## Verification

Run `make dev`, then in Playwright (desktop, 1280 × 800):

1. Open `http://localhost:8080/` → the URL becomes `/sign-in`; visible: "Sign in", "Sign in with Dev IdP".
2. Fill "Login" with `admin@example.org` and "Password" with `muster-dev-password`, click "Sign in" → the user menu shows
   "admin".
3. Run `psql "$MUSTER_DATABASE_URL" -c "UPDATE runtime_state SET recovery_until = now() + interval '1 minute'"` → within
   5 seconds, without a reload, the page shows "Muster is recovering after downtime. Data may be incomplete until"
   followed by the time; about a minute later the banner is gone, still without a reload.
4. Profile → "Language" → "Русский" → the navigation shows "Профиль"; switch back to "English".
5. Set "TOTP required" to everyone (`PUT /api/v1/organization` as in S-012), create the user `carol` through the API,
   open her setup link → "Set password" with `carol-password-1` → sign in as `carol` → the URL is `/totp-enrolment`;
   open `/profile` → back on `/totp-enrolment`; enter the code from `oathtool --totp -b <secret shown>` → visible "Save
   these recovery codes" with 10 codes → "Continue" → the home page.
6. Sign out; script the fake IdP's next user with the groups `["contractors"]`; click "Sign in with Dev IdP" → visible
   "You have no access to Muster. Contact your administrator."
7. Create the local user `Alice` and script the fake IdP's next user as `alice`; click "Sign in with Dev IdP" → visible
   "An account with this login already exists. Sign in with it and link OIDC in your profile."; sign in as Alice with
   her password → Profile → "Link OIDC" → visible "After linking you will sign in only through OIDC; your password will
   be removed." → "Continue" → back on the profile, which shows the sign-in method "OIDC".
8. As Alice (now OIDC), disable her at the fake IdP and make her re-check due
   (`UPDATE oidc_checks SET deadline = now()`) → click "Profile" → the URL becomes `/sign-in` with "Your session ended.
   Sign in again."
9. At 360 × 740 the sign-in page, the enrolment page and the profile scroll vertically only.

`make e2e` runs steps 1–8 as `web/e2e/sign-in.spec.ts` and `web/e2e/profile.spec.ts`.

## Open questions

1. The PRD quotes the texts for `no_access` and `login_taken` only. The story writes the English originals for
   `account_disabled`, `oidc_disabled`, `invalid_request`, `idp_error`, `identity_linked_elsewhere`, the expired and
   used setup links and the ended session, and lists them in the pull request for review.

## Notes

- Suggested commit: `feat(web): add application shell, sign-in, password setup, totp and profile pages`.
- Texts quoted in the PRD are copied exactly; the Russian versions are written in the same pull request.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-03.FR-7 | full | together with S-013 |
| C-03.FR-10 | full | together with S-012 and S-013 |
| C-03.FR-12 | partial | the profile page; tokens are S-017, Account links S-052 |
| C-03.FR-18 | full | together with S-012 |
| C-03.FR-24 | full | together with S-010, S-012 and S-013 |
| C-03.FR-25 | full | together with S-013 |
| C-03.FR-26 | full | together with S-011 |
| C-03.FR-28 | full | together with S-013 |
| C-03.FR-29 | partial | the profile side, on the API of S-062; conversion on the Users page is S-015 |
| C-03.AC-2 | full | |
| C-03.AC-9 | full | |
| C-03.AC-20 | full | together with S-013 and S-062 |
| C-03.AC-22 | partial | the SPA part, after the re-checks of S-062; the token part is S-016 |
