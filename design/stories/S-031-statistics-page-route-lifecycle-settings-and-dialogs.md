---
id: S-031
title: Statistics page, Route lifecycle settings and the delete dialog additions (FE)
capability: C-09
kind: fe
layer: L1
depends_on: [S-019, S-027, S-030]
covers: [C-09.FR-15, C-09.FR-19, C-09.FR-21, C-09.FR-4, C-09.FR-5, C-09.FR-9, C-09.AC-17, C-06.FR-19, C-08.FR-1]
files_touched:
  - web/src/routes/statistics.tsx
  - web/src/components/statistics-table.tsx
  - web/src/components/statistics-chart.tsx
  - web/src/components/statistics-table.test.tsx
  - web/src/components/route-policy-lifecycle.tsx
  - web/src/components/route-form.tsx
  - web/src/components/route-delete-dialog.tsx
  - web/src/components/integration-delete-dialog.tsx
  - web/src/components/integration-alerts.tsx
  - web/src/components/alert-group-filters.tsx
  - web/src/components/app-shell.tsx
  - web/src/components/app-shell.test.tsx
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/statistics.spec.ts
  - web/e2e/route-delete.spec.ts
  - web/e2e/integration-page.spec.ts
  - web/e2e/integrations.spec.ts
acceptance:
  - "[C-09.FR-15, C-09.AC-17] The statistics page shows, per Route or per Integration and for a chosen period, the number of Alert Groups and the median and 95th percentile of time to acknowledge and time to resolve, as totals and per day; for a Route whose three Alert Groups resolved 10, 20 and 30 minutes after they started it shows 3 and a median time to resolve of 20 min, and the same Alert Groups under their Integration."
  - "[C-09.FR-19] Deleting a Route with open Alert Groups shows \"This route has N open Alert Groups. Move them to the Default route to delete it.\"; \"Move and delete\" moves them and deletes the Route, and each moved Alert Group's Timeline shows the move."
  - "[C-09.FR-21] The Integration delete dialog says how many open Alert Groups will be resolved: \"N open Alert Groups will be resolved.\""
  - "[C-09.FR-4, C-09.FR-5, C-09.FR-9] The Route editor has a Lifecycle section with the Reopen window, the Grace period and \"A rise to Urgent removes the acknowledgement\", pre-filled from the profile on creation."
  - "[C-06.FR-19] The Integration's Alerts view shows the Alert Group of each Alert as `#N`, linked to its page."
verify: "make ci e2e"
operator_attention: false
issue: 31
---

# S-031. Statistics page, Route lifecycle settings and the delete dialog additions (FE)

## Scope

**IN**

- The statistics page per Route or per Integration.
- The Lifecycle section of the Route editor.
- The move dialog when deleting a Route with open Alert Groups, and the open Alert Group count in the Integration
  delete dialog.
- The Alert Group column of the Integration's Alerts view.

**OUT**

- Time to acknowledge filled by real acknowledgements (S-032, checked on this page by S-033).

## Contracts

- **API used**: `getAlertGroupStatistics`, `listRoutes`, `listIntegrations`, `getRoute`, `updateRoute`, `deleteRoute`,
  `moveOpenAlertGroups`, `getIntegration`, `deleteIntegration`, `listIntegrationAlerts`.
- **Statistics** (C-09.FR-15; route `/statistics`, navigation entry "Statistics" with `alert-groups:read`, added to
  the entries of `app-shell.tsx` after "Alert Groups"): "By route"
  or "By integration"; the items to show (all by default); the period ("Last 7 days" by default, "Last 30 days", "Last
  90 days", "Custom"); the request passes the profile's time zone, so days split where the user's days do. A table per
  item: "Alert Groups", "Time to acknowledge" and "Time to resolve" with the median and the 95th percentile as durations
  ("—" without data); expanding an item shows its days as a bar chart of the count with the medians beside it. The bars
  are drawn as SVG by the page itself, with no chart library; a library added later must be under the shipped licence
  list, be listed in NOTICE and work under the Content Security Policy of the app listener, `style-src 'self'`
  (`internal/server/spa.go`): it injects no `<style>` element and sets no `style` attribute from markup — a library
  that needs either is not used; bars drawn as SVG or with Tailwind classes are fine. `statistics.spec.ts` collects
  violations with `watchCsp` (`web/e2e/support.ts`) and expects none. The period and the choice are kept in the URL.
- **Lifecycle section** (C-09.FR-4, FR-5, FR-9): in `route-form.tsx`, "Reopen window" and "Grace period" in minutes
  (`policy.reopen_window_seconds`, `policy.grace_period_seconds`) with the hints "An alert with the same key firing
  this soon after Muster resolved the Alert Group reopens it." and "After a person resolves an Alert Group, alerts that
  still fire this long start a new one.", and the switch "A rise to Urgent removes the acknowledgement"
  (`policy.urgent_rise_removes_ack`).
- **Route delete dialog** (C-09.FR-19): with `open_alert_group_count` above zero, "This route has N open Alert Groups.
  Move them to the Default route to delete it." with "Move and delete" (`moveOpenAlertGroups`, then `deleteRoute`) and
  "Cancel"; a `409` at deletion because new Alert Groups started meanwhile shows the dialog again with the new count.
- **Integration delete dialog** (C-09.FR-21): adds "N open Alert Groups will be resolved." from
  `open_alert_group_count`, or nothing at zero.
- **Alerts view column** (C-06.FR-19): "Alert Group" with `alert_group.number` as `#N`, linked to
  `/alert-groups/$alertGroupId`. `web/e2e/integration-page.spec.ts` reads the cells of that table by position
  (`getByRole("cell").nth(…)`); the new column shifts them, so the spec reads them by column name instead.

## Steps

1. Write the statistics page with its table and chart, and its navigation entry. Check: the component test formats
   durations and empty values; Playwright reads the numbers of Verification with no CSP violation.
2. Add the Lifecycle section. Check: Playwright saves new values and reads them back.
3. Extend both delete dialogs and the Alerts view. Check: Playwright moves and deletes a Route and reads the counts.

## Verification

Run `make dev`, sign in as `admin@example.org` / `muster-dev-password`; create the three Alert Groups of the Route "st"
resolved after 10, 20 and 30 minutes from a terminal as in S-029, and the open Alert Groups of the Route "db" of S-028.
Then in Playwright:

1. Statistics → "By route" → the row "st" shows "3", "Time to resolve" median "20 min" → expand → one day with 3;
   `watchCsp` recorded no violation.
2. "By integration" → the row of the Integration shows the same three among its Alert Groups.
3. Routes → open "st" → "Lifecycle" shows "Reopen window 15" and "Grace period 15" minutes and the switch on → set the
   Reopen window to 30 → "Save" → reload → 30.
4. Routes → "db" → "Delete" → "This route has 2 open Alert Groups. Move them to the Default route to delete it." →
   "Move and delete" → the list no longer has "db"; open one of the moved Alert Groups → its Route is "Default" and the
   Timeline shows the move.
5. Integrations → the Integration of step 4 → its Alerts view shows "#N" links in the "Alert Group" column → "Delete"
   → the dialog reads "2 open Alert Groups will be resolved." → "Cancel".

`make e2e` runs these steps as `web/e2e/statistics.spec.ts` and `route-delete.spec.ts`.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): add the statistics page, route lifecycle settings and delete dialog additions`.
- Time to acknowledge stays "—" until S-032; S-033 checks it on this page (C-10.AC-14).

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-09.FR-15 | full | together with S-029; time to acknowledge is checked with data by S-033 |
| C-09.FR-19 | full | together with S-028 |
| C-09.FR-21 | full | together with S-029 |
| C-09.FR-4 | partial | the Reopen window setting |
| C-09.FR-5 | partial | the Grace period setting |
| C-09.FR-9 | partial | the setting of `route.urgent_rise_removes_ack` |
| C-09.AC-17 | full | together with S-029 |
| C-06.FR-19 | full | together with S-020, S-021, S-022, S-025, S-027 and S-028 |
| C-08.FR-1 | partial | the lifecycle section of the Route editor |
