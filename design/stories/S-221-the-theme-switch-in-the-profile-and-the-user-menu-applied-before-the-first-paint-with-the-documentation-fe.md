---
id: S-221
title: The theme switch in the profile and the user menu, applied before the first paint, with the documentation (FE)
capability: C-39
kind: fe
layer: L2
depends_on: [S-220, S-014, S-072, S-010]
covers: [C-39.FR-1, C-39.FR-2, C-39.FR-3, C-39.FR-4, C-39.FR-5, C-39.FR-7, C-39.AC-2, C-39.AC-3, C-39.AC-4, C-39.AC-5, C-39.AC-6]
files_touched:
  - web/index.html
  - web/public/theme-init.js
  - web/src/lib/theme.ts
  - web/src/lib/theme.test.ts
  - web/src/components/theme-switch.tsx
  - web/src/components/theme-switch.test.tsx
  - web/src/components/user-menu.tsx
  - web/src/components/app-shell.test.tsx
  - web/src/routes/__root.tsx
  - web/src/routes/profile.tsx
  - web/src/styles.css
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/theme.spec.ts
  - web/e2e/profile.spec.ts
  - web/e2e/phone-width.spec.ts
  - web/e2e/screenshots/capture.ts
  - web/e2e/screenshots/access.spec.ts
  - docs/sign-in/index.md
acceptance:
  - "[C-39.FR-1, C-39.FR-2, C-39.AC-2] Profile → \"Theme\" shows the hint \"Applies at once, on this device and in your profile.\", three buttons side by side — \"System\", \"Light\", \"Dark\" — with the chosen one `aria-pressed`, and under them \"As in the system: now light.\" (or \"now dark.\"), \"Always light.\" or \"Always dark.\"; choosing a button saves it with `updateMe` and turns the page that way at once without a reload, and the user menu's \"Theme\" has the same three choices, one click from any page, re-reading the profile before it saves so that it never overwrites a newer name, time zone or language (`theme-switch.test.tsx`, `theme.spec.ts`, `profile.spec.ts`)."
  - "[C-39.FR-3, C-39.AC-2] A page load applies the theme before the first paint: `web/public/theme-init.js`, a classic script of the SPA's own origin loaded from `index.html` before the bundle and under `script-src 'self'` and `style-src 'self'` with no inline script or style, reads the copy in `localStorage` and sets the theme on the document; in a real browser emulating `prefers-color-scheme: light`, a User with `dark` reloads the Alert Group list with the bundle held back (Playwright delaying `/assets/*.js`) and the document is dark — its computed `color-scheme` and background — before the bundle runs; the answer to `index.html` is byte for byte the same for two people (`theme.spec.ts`, `theme.test.ts`)."
  - "[C-39.FR-3, C-39.AC-4] Once the profile is read its theme wins and replaces the copy; the sign-in, TOTP, password setup and error pages use the copy and otherwise follow the system; signing out keeps the copy, which holds only the theme and nothing that identifies the person: after sign-out the sign-in page keeps the theme last applied in that browser, and in a fresh browser context it follows the emulated system theme (`theme.test.ts`, `theme.spec.ts`)."
  - "[C-39.FR-1, C-39.AC-3] With `system`, switching the emulated `prefers-color-scheme` from light to dark turns the open page dark without a reload, and with `light` or `dark` the system is ignored (`theme.test.ts`, `theme.spec.ts`)."
  - "[C-39.FR-4, C-39.AC-5] Both themes cover every page, dialog, menu, chart and native control through `color-scheme`, and the status colours of Alert Groups and the Severity levels keep their meaning in both; the pages of the Alert Group list and page, the profile, the Route page and the statistics page render in both themes at 1280 px and 360 px, in English and in Russian, with no horizontal scrolling at 360 px and no text or control lost to the background (`theme.spec.ts`, `phone-width.spec.ts`)."
  - "[C-39.FR-4] The switch works on the current colour tokens: `styles.css` takes the dark tokens from the document's theme and not from the media query alone, `dark:` variants follow the same switch, and no new colour or contrast bar is introduced; an automated contrast check is left to the design pass with its NFR row (`styles.css`, `theme.spec.ts`)."
  - "[C-39.FR-5, C-39.FR-7, C-39.AC-6] The theme changes only the web UI: a Root message in the fake Mattermost server is the same under any theme, the documentation site keeps its own look; with the screenshot User's theme set to `dark`, `make docs-screenshots` still produces light screenshots, because the capture helper answers `getMe` and `updateMe` with `light` and the browser context emulates the light scheme; Your profile in the documentation describes the switch, with the screenshot of the profile that already exists (`capture.ts`, `access.spec.ts`, `make docs-check`)."
  - "[C-39.FR-2] Every new text exists in English and Russian, and a change of the theme in one tab reaches another open tab, or another browser, the next time it reads the profile (`theme-switch.test.tsx`, `theme.spec.ts`, the missing-key check of `make ci`)."
verify: "make ci e2e-web docs-check"
operator_attention: false
issue: null
---

# S-221. The theme switch in the profile and the user menu, applied before the first paint, with the documentation (FE)

## Scope

**IN**

- The switch in the profile and in the user menu, the application of the theme without a reload, and its copy in the
  browser that applies it before the first paint (C-39.FR-1 to FR-3).
- The dark and light themes on every page through the existing tokens, and the check of the five pages at two widths
  (C-39.FR-4).
- The documentation of the switch and the screenshots that stay light (C-39.FR-7).

**OUT**

- The `theme` of the profile in the API: S-220.
- A new palette, an automated contrast check and the design pass of the later layers (journal D315): the switch is
  unchanged by them.

## Contracts

- **API used**: `getMe` (`theme` at the top level), `updateMe` (`name`, `time_zone`, `language`, `theme`).
- **Script** (`web/public/theme-init.js`, a classic script, with the licence header in its own comment syntax):
  reads `muster-theme` (`light` or `dark` or absent) from `localStorage` in a try/catch, resolves `system` with
  `matchMedia("(prefers-color-scheme: dark)")`, and sets `data-theme` on `<html>`; `index.html` loads it with
  `<script src="/theme-init.js"></script>` in `<head>`, before the module script. `script-src 'self'` allows it, and
  the server does not rewrite `index.html` per person (C-39.FR-3). The copy holds the theme only.
- **Library** (`web/src/lib/theme.ts`): `readCopy`, `writeCopy`, `resolve(theme, systemDark)`, `applyTheme`, and a
  subscription to the media query that re-applies `system` while the page is open; the root route applies the
  profile's theme when `getMe` answers and writes the copy; sign-in and the pages without a profile apply the copy
  alone.
- **Components**: `theme-switch.tsx` — three buttons with `aria-pressed` and the line under them, used by the profile's
  section "Theme" and by the user menu's "Theme" (`user-menu.tsx`); the menu's choice reads the profile with
  `getMe` before it calls `updateMe`, so that it sends the newest name, time zone and language and never overwrites
  them (C-39 UI); a change in one tab reaches the others on their next read of the profile.
- **Styles** (`web/src/styles.css`): `@custom-variant dark` follows `:root[data-theme="dark"]` instead of the media
  query alone, the dark tokens sit under `:root[data-theme="dark"]` with the media query kept as the fallback of
  `:root:not([data-theme])`, and `color-scheme` follows the theme (`light`, `dark`, or `light dark` for `system`), so
  that native controls, scrollbars and menus follow it. No token value changes.
- **Capture** (`web/e2e/screenshots/capture.ts`, `access.spec.ts`): every context of the screenshots emulates the light
  scheme, clears the copy and answers `getMe` and `updateMe` with `theme` `light` whatever the screenshot User has,
  so that the images are the same on any machine (C-39.FR-7).
- **Texts** (EN / RU; `en.json` and `ru.json` are the real files; a missing key fails CI): "Theme" / «Тема»; "System" /
  «Как в системе»; "Light" / «Светлая»; "Dark" / «Тёмная»; "Applies at once, on this device and in your profile." /
  «Применяется сразу, на этом устройстве и в вашем профиле.»; "As in the system: now light." / «Как в системе: сейчас
  светлая.»; "As in the system: now dark." / «Как в системе: сейчас тёмная.»; "Always light." / «Всегда светлая.»;
  "Always dark." / «Всегда тёмная.».
- **Documentation** (`docs/sign-in/index.md`, "Your profile"): the switch, the three choices, that System is the
  default and follows the system, that the choice follows the person to every browser they sign in from, that a new
  browser first follows the system until the profile is read, and that the documentation site and the messengers'
  messages are unaffected.

## Steps

1. Write `theme-init.js`, `theme.ts` and the `index.html` line, and the root route's application. Check: `theme.test.ts`
   (the copy, `system` following the media query, a storage that throws); `make build` serves `/theme-init.js`.
2. Switch the styles to the document's theme. Check: `theme.spec.ts` renders the five pages in both themes.
3. Write the switch, mount it in the profile and the user menu, and add the texts. Check: `theme-switch.test.tsx`,
   `app-shell.test.tsx`; `make ci` (missing keys).
4. Make the capture helper light and write the page of the documentation. Check: `make docs-screenshots
   GUIDES=access` and `make docs-check`.
5. Write `theme.spec.ts` and extend `profile.spec.ts` and `phone-width.spec.ts`. Check: the Verification below.

## Verification

Playwright against `make dev` (as `web/e2e/support.ts` sets it up), the browser emulating `prefers-color-scheme: light`,
in English and then Russian.

1. Sign in as the Admin and open the profile. Expect the section "Theme" with the hint, three buttons and "As in the
   system: now light.". Press "Dark": expect the page dark at once and "Always dark."; `curl -s -b jar $API/me | jq -r
   .theme` answers `dark`.
2. Open the Alert Group list, then reload with `/assets/*.js` delayed: expect the document dark — its computed
   `color-scheme` `dark` and a dark background — before the bundle runs. `curl -s localhost:8080/ | sha256sum` is the
   same with and without the session cookie.
3. In the user menu choose "Light": expect the page light at once; choose "Dark" again. In another tab, change the
   name in
   the profile; choose a theme in the menu of the first tab: expect the new name unchanged afterwards.
4. Open the Alert Group page, the Route page and the statistics page at 1280 px and 360 px: expect both themes without
   horizontal scrolling, the status colours legible and the dialogs, menus and charts in the theme.
5. Sign out: expect the sign-in page dark. In a fresh context: expect it light; switch the emulated scheme to dark:
   expect it dark without a reload. For a User with `system`, switch the emulated scheme while the Alert Group list is
   open: expect it to follow without a reload.
6. `make docs-screenshots GUIDES=access` with the screenshot User on `dark`: expect `05-profile.png` light.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): add the theme switch to the profile and the user menu, applied before the first paint`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases. No migration.
- The copy is a convenience, not state (the rule for `localStorage`): a browser that blocks it follows the system until
  the profile is read, and a profile read always wins.
- The key of the copy, `muster-theme`, is shared by the script and `theme.ts`; the test that reads both keeps them
  equal.
- A contrast check, such as axe-core (MPL-2.0, a test dependency with its licence statement), comes with the design
  pass of the later layers and its NFR row, as C-39.FR-4 says; this story adds none.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-39.FR-1 | partial | `system` following the media query, `light` and `dark` ignoring it; the stored value is S-220 |
| C-39.FR-2 | partial | the switch, the immediate application and the other tabs; the profile setting is S-220 |
| C-39.FR-3 | full | |
| C-39.FR-4 | full | |
| C-39.FR-5 | full | |
| C-39.FR-7 | full | |
| C-39.AC-2 | full | the API half is S-220's AC-1 |
| C-39.AC-3 | full | |
| C-39.AC-4 | full | |
| C-39.AC-5 | full | |
| C-39.AC-6 | full | |
