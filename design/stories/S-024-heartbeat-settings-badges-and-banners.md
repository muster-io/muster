---
id: S-024
title: Heartbeat settings, badges and banners (FE)
capability: C-07
kind: fe
layer: L1
depends_on: [S-022, S-023]
covers: [C-07.FR-2, C-07.FR-3, C-07.FR-5, C-07.FR-6, C-07.AC-5, C-05.FR-1, C-05.FR-7]
files_touched:
  - web/src/components/integration-form.tsx
  - web/src/components/heartbeat-badge.tsx
  - web/src/components/integration-warnings.tsx
  - web/src/components/integration-token-dialog.tsx
  - web/src/routes/integrations.index.tsx
  - web/src/routes/integrations.$integrationId.index.tsx
  - web/src/components/heartbeat-badge.test.tsx
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/heartbeat.spec.ts
acceptance:
  - "[C-07.FR-2, C-05.FR-1] The Integration form has a Heartbeat section — on or off, the timeout pre-filled with `integration.heartbeat_timeout`, and the Heartbeat URL with how to pass the token."
  - "[C-07.FR-3, C-05.FR-7] The Integrations list and the Integration page show the Heartbeat state as a badge: \"Not configured\", \"Waiting\", \"Live\" or \"Lost\"."
  - "[C-07.FR-5, C-07.AC-5] While the Heartbeat is lost the Integration page shows \"No contact with Alertmanager since HH:MM. Nothing is resolved as Stale until contact returns.\" with the time in the user's time zone; without a Heartbeat it shows \"No Heartbeat: Muster will not notice when Alertmanager goes quiet, and Stale resolution is off.\", and before the first signal \"Waiting for the first Heartbeat signal. Stale resolution is off until it arrives.\""
  - "[C-07.FR-6] With the Heartbeat on, the token dialog also shows the Heartbeat snippet with a copy button, once; with it off, the dialog says how to get it."
verify: "make ci e2e"
operator_attention: false
issue: 24
---

# S-024. Heartbeat settings, badges and banners (FE)

## Scope

**IN**

- The Heartbeat section of the Integration form.
- The Heartbeat state badge in the list and on the page, and the three Heartbeat banners.
- The Heartbeat snippet in the token dialog.

**OUT**

- The Route suggestion for `MusterHeartbeatLost` on the Routes page (S-027).

## Contracts

- **API used**: `listIntegrations`, `getIntegration`, `createIntegration`, `updateIntegration`,
  `createIntegrationToken`.
- **Form** (C-07.FR-2): "Heartbeat" with a switch (`heartbeat.enabled`), "Timeout" in minutes (sent as
  `timeout_seconds`, pre-filled from `integration.heartbeat_timeout`), and, read-only, the Heartbeat URL
  (`heartbeat.url`) with "Send one of this Integration's tokens as `Authorization: Bearer`, or append it as `/<token>`."
- **Badge** (C-07.FR-3): `not_configured` "Not configured" (neutral), `waiting` "Waiting", `live` "Live", `lost` "Lost"
  (the colour of a problem); on the list as a column, on the page next to the name; hidden for the built-in Integration.
- **Banners** (C-07.FR-5): `IntegrationWarning` kinds `heartbeat_not_configured`, `heartbeat_waiting` and
  `heartbeat_lost` render the texts of [reference.md](../prd/l1/reference.md#banners-warnings-and-notices) on the
  Integration page, `HH:MM` from `since` in the user's time zone with the date when it is not today.
- **Token dialog** (C-07.FR-6): when `heartbeat_snippet` is set, a second block "Heartbeat configuration" with "Copy";
  otherwise "Turn the Heartbeat on and create a token to get its configuration."
- **Live hints**: `integration` hints, which S-023 sends on every state change, refresh the badge and the banners.

## Steps

1. Add the Heartbeat section to the form. Check: Playwright turns it on and sees the URL.
2. Write the badge and render the Heartbeat banners. Check: the component test covers the four states; Playwright sees
   each banner.
3. Add the Heartbeat snippet to the token dialog. Check: Playwright copies it.

## Verification

Run `make dev`, sign in as `admin@example.org` / `muster-dev-password`, then in Playwright, with the fake Alertmanager
and the development clock driven from a terminal as in S-023:

1. Integrations → the list shows "dev-alertmanager" with "Live" (its fake Heartbeat sends every minute).
2. "Create integration" → Name "edge" → "Create" → the page shows "Not configured" and "No Heartbeat: Muster will not
   notice when Alertmanager goes quiet, and Stale resolution is off."
3. Edit → "Heartbeat" on → the timeout shows `5` minutes and the URL `http://localhost:8081/api/v1/heartbeat` → "Save"
   → "Waiting" and "Waiting for the first Heartbeat signal. Stale resolution is off until it arrives."
4. Tokens → "Create token" → the dialog shows "Heartbeat configuration" containing `vector(1)` and
   `/api/v1/heartbeat` → "Copy" → close.
5. From the terminal send one signal with the new token → the badge turns "Live" without a reload.
6. Advance the development clock by 6 minutes → the badge turns "Lost" and the page shows "No contact with Alertmanager
   since" with the time of the signal of step 5.
7. Send a signal → "Live" and the banner is gone.

`make e2e` runs these steps as `web/e2e/heartbeat.spec.ts`.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): add heartbeat settings, state badges and banners`.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-07.FR-2 | full | together with S-023 |
| C-07.FR-3 | full | together with S-023 |
| C-07.FR-5 | full | together with S-023 |
| C-07.FR-6 | full | together with S-023 |
| C-07.AC-5 | full | together with S-023 |
| C-05.FR-1 | full | together with S-018, S-019 and S-023 |
| C-05.FR-7 | full | together with S-018, S-019, S-020 and S-022 |
