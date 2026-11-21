# C-39. Theme switch

[L2 index](../L2.md) · Release: 0.2.x · UI: yes · Depends on: C-03

**Goal.** Each person chooses how the web UI looks — System, Light or Dark — and the choice follows them to every
browser they sign in from (issue #370, journal D462). Today the UI follows only the browser's `prefers-color-scheme`;
System stays the default, so nobody who chooses nothing sees a change.

## Scenarios

1. Olga's laptop is set to light, but she works at night: in her profile she chooses Dark, and the page turns dark at
   once.
2. The next morning she opens Muster on her phone, whose system theme is light; after sign-in the UI is dark, as her
   profile says.
3. She reloads the Alert Group page on the laptop: it is dark from the first frame, never briefly light.
4. She signs out; the sign-in page on that laptop stays dark. On a browser she never used, the sign-in page follows the
   system.
5. Ivan never chose: the UI follows his system and turns dark when the system does at sunset, without a reload.

## Functional requirements

- **C-39.FR-1** A User's theme is `system`, `light` or `dark`; it is `system` until the User chooses, as the profile's
  language and time zone have their defaults in C-03.FR-12. With `system` the UI follows the browser's
  `prefers-color-scheme` as before and follows a change of it while the page is open; with `light` or `dark` it ignores
  it.
- **C-39.FR-2** The theme is a setting of the profile (C-03.FR-12), stored on the server with the User's name, time zone
  and language, and changed only by the User themselves from a web session (C-03.FR-27), like the rest of the profile,
  whose Audit log action `user.profile_updated` records it. A change applies at once, without a reload, in the tab that
  made it; other open tabs and other browsers apply it the next time they read the profile.
- **C-39.FR-3** The browser keeps a copy of the last theme it applied, so that a page load applies the theme before the
  first paint — without a frame in the other theme — under the Content Security Policy of the app listener
  (C-03.FR-16, S-010), whose `script-src 'self'` and `style-src 'self'` allow no inline script or style: the copy is
  in the browser's `localStorage`, and a small script of the SPA's own origin, loaded before its bundle, reads it and
  sets the theme on the document; the server does not rewrite `index.html` per person. Once the profile is read, its
  theme wins and replaces the copy. The sign-in, TOTP, password setup and error pages, which have no profile to read,
  use the copy and otherwise follow the system. Signing out keeps the copy; it holds only the theme, nothing that
  identifies the person.
- **C-39.FR-4** Both themes cover every page, dialog, menu, chart and native control (through `color-scheme`), and the
  status colours of Alert Groups and Severity levels keep their meaning in both. The switch works on the current colour
  tokens and sets no new contrast bar: the dark theme keeps the contrast its tokens were chosen for. The UI design of
  the later layers (journal D315) changes the tokens, not the switch, and brings an automated contrast check — for
  example axe-core, MPL-2.0, as a test dependency with its licence statement — together with an NFR row.
- **C-39.FR-5** The theme changes only the web UI: messenger messages, the documentation site, which keeps its own
  look, and the API are unaffected.
- **C-39.FR-6** A Service account has no profile and no theme; its API answers are unaffected.
- **C-39.FR-7** The documentation's page on the profile describes the switch. The documentation screenshots stay in the
  light theme whatever the theme of the User who takes them (journal D418), so that `make docs-screenshots` gives the
  same images on any machine.

## UI

Profile, section "Theme": the hint "Applies at once, on this device and in your profile.", three buttons side by side
— "System", "Light", "Dark" — with the chosen one pressed (`aria-pressed`), and under them "As in the system: now
light." (or "now dark."), "Always light." or "Always dark.". The user menu also has "Theme" with the same three
choices, one click from any page, as issue #370 asks; it re-reads the profile before saving, so that it never overwrites
a newer name, time zone or language. Every text in English and Russian. At 360 px the three buttons fit on one line
without horizontal scrolling.

## API surface

`theme` (`system`, `light`, `dark`) at the top level of `Me`, which `getMe` returns — not on `User`, which the list of
Users also returns to Admins — and in `updateMe`; an update that omits it keeps it, and an unknown value is refused by
request validation with `400` `invalid_format`. No new Permission, metric, log event or Internal alert.

## Acceptance

- **C-39.AC-1** `GET /me` of a User who never chose gives `theme` `system`; `PUT /me` with `dark` stores it and a
  later `GET /me` from another session gives `dark`; `PUT /me` without `theme` keeps it; `sepia` gets `400`
  `invalid_format`; a Personal access token gets `403` `session_required`.
- **C-39.AC-2** In a real browser emulating `prefers-color-scheme: light`, a User with `dark` sees the Alert Group list
  dark after sign-in; a reload with the SPA's bundle held back (Playwright delaying `/assets/*.js`) shows the document
  dark — its computed `color-scheme` and background — before the bundle runs; choosing Light in the profile turns the
  page light at once, and choosing Dark in the user menu turns it dark again; the answer to `index.html` is the same
  for every person.
- **C-39.AC-3** With `system`, switching the emulated `prefers-color-scheme` from light to dark turns the open page dark
  without a reload.
- **C-39.AC-4** After sign-out, the sign-in page keeps the theme last applied in that browser; in a fresh browser
  context it follows the emulated system theme.
- **C-39.AC-5** The pages of the Alert Group list and page, the profile, the Route page and the statistics page render
  in both themes at 1280 px and 360 px, in English and in Russian, with no horizontal scrolling at 360 px.
- **C-39.AC-6** With the screenshot User's theme set to `dark`, `make docs-screenshots` still produces light
  screenshots.

## Related ADRs

ADR-0009.

## Depends on

C-03 — the profile, the web-session rule and the Content Security Policy.

## Suggested story split

- **FE** with its small backend part — `theme` in `Me` and `updateMe`, the switch in the profile and the user menu, the
  theme applied before the first paint, the dark variants checked on every page, the documentation and the screenshots
  kept light.
