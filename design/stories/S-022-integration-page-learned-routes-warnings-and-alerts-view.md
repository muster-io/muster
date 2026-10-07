---
id: S-022
title: "Integration page: learned Alertmanager routes, warnings and Alerts view (FE)"
capability: C-06
kind: fe
layer: L1
depends_on: [S-019, S-021]
covers: [C-06.FR-14, C-06.FR-18, C-06.FR-19, C-06.AC-3, C-06.AC-8, C-05.FR-7]
files_touched:
  - web/src/routes/integrations.index.tsx
  - web/src/routes/integrations.$integrationId.index.tsx
  - web/src/components/alertmanager-routes.tsx
  - web/src/components/integration-warnings.tsx
  - web/src/components/integration-alerts.tsx
  - web/src/components/integration-alerts.test.tsx
  - web/src/components/integration-alerts-search.ts
  - web/src/components/integration-form.tsx
  - web/src/components/data-table.tsx
  - web/src/lib/time.ts
  - web/src/components/label-matchers-input.tsx
  - web/src/components/label-matchers-input.test.tsx
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/integration-page.spec.ts
  - web/e2e/support.ts
  - web/e2e/sign-in.spec.ts
acceptance:
  - "[C-06.FR-18, C-06.AC-3, C-05.FR-7] The Integration page lists the Alertmanager routes seen with their learned repeat interval (\"Not learned yet\" before one is) and the time to resolve by absence; a route that repeats every 5 minutes shows \"5 min\"."
  - "[C-06.FR-18] A route above `processing.long_repeat_warning` shows \"Alertmanager route {route} repeats every {interval}; Muster can resolve its alerts by absence only after {3 × interval}. Use a repeat interval of 5–15 minutes on the route to Muster.\" with the recommended snippet and a copy button; while a `groupKey` is truncated the page shows \"Alertmanager truncates Snapshots for N groups. Set `max_alerts: 0` on the Alertmanager receiver.\"; the Integrations list marks both warnings."
  - "[C-06.FR-19, C-06.AC-8] The Alerts view lists the Integration's Alerts with labels, state (firing, or resolved with its reason and time), `startsAt`, time last seen and the Alertmanager groups listing them, filtered by state, Matchers and text; an Alert with a conflicting Static label shows \"Static label cluster not applied: the alert has its own value.\""
  - "[C-06.FR-14] The built-in Integration \"Muster\" appears in the list marked \"Built-in\", its page explains it and offers no edit, delete or token controls, and its Alerts view shows the Internal alerts."
  - "[C-06.FR-19] An invalid Matcher in the filter shows the server's error under the field and keeps the previous results."
verify: "make ci e2e"
operator_attention: false
issue: 22
---

# S-022. Integration page: learned Alertmanager routes, warnings and Alerts view (FE)

## Scope

**IN**

- The "Alertmanager routes" section with learned intervals, the long-interval warning and its snippet.
- The truncation and long-interval warnings on the page and in the Integrations list.
- The Alerts view with its filters, and the Matcher input other pages reuse.
- The built-in Integration in the list and on its page.

**OUT**

- The Heartbeat badges and banners (S-024).
- The Route and Severity level columns of the Alerts view (S-027) and its Alert Group column (S-031).

## Contracts

- **API used**: `listIntegrations`, `getIntegration`, `listAlertmanagerRoutes`, `listIntegrationAlerts`.
- **Integration page sections** (C-05.FR-7, C-06.FR-18, FR-19), after the details of S-019: warnings, "Alertmanager
  routes", "Alerts"; tokens and Stored Snapshots stay as in S-019.
- **Alertmanager routes** (C-06.FR-18): a table with the route path, "Repeat interval" (the learned interval as a
  duration, or "Not learned yet"), "Resolves by absence after" (`resolve_by_absence_after_seconds`) and "Truncated
  groups". A row with `long_interval_warning` shows the long-interval text of
  [reference.md](../prd/l1/reference.md#banners-warnings-and-notices) and `recommended_snippet` with "Copy".
- **Warnings** (`IntegrationWarning`): `snapshot_truncated` and `long_repeat_interval` render the texts of
  reference.md on the page; the list shows a warning mark with the same text on hover and in the row's details. The
  component handles the Heartbeat kinds too, rendered by S-024.
- **Alerts view** (C-06.FR-19): state tabs Firing, Resolved and All; the Matcher input (a list of Alertmanager-syntax
  Matchers, each a chip, sent as repeated `label`); text search (`q`); sorting by last seen or `startsAt`; "Load more"
  by cursor; filters kept in the URL. Columns: labels (as chips, `alertname` first), state ("Firing", or "Resolved:
  {reason}" with the time; the reason's full text on hover), `startsAt`, last seen, Alertmanager groups (on expand),
  and the Static label warning. Reasons: `resolved` → "Resolved by Alertmanager", `gone` → "Gone", `stale` → "Stale",
  `integration_deleted` → "Integration deleted".
- **Matcher input** (`label-matchers-input`): checks the syntax as typed (`name="value"`, `!=`, `=~`, `!~`) and shows a
  `Problem` for `label` from the server under the field; reused by the Alert Group list (S-030).
- **Built-in Integration** (C-06.FR-14): marked "Built-in" in the list; its page reads "Muster raises its Internal
  alerts through this Integration. It has no tokens and cannot be changed." and shows only the Alerts view and the
  Stored Snapshots.

## Steps

1. Write the warnings and the Alertmanager routes section. Check: Playwright sees the learned interval and the
   long-interval warning with its snippet.
2. Write the Matcher input and the Alerts view. Check: the component tests cover the syntax check and the server error;
   Playwright filters by a Matcher and by state.
3. Mark the built-in Integration and add the list warnings. Check: Playwright sees "Built-in" and the truncation mark.

## Verification

Run `make dev`, sign in as `admin@example.org` / `muster-dev-password`; drive the fake Alertmanager and the development
clock as in S-020 and S-021 for the Integration "lab". Then in Playwright:

1. Integrations → the list shows "Muster" with "Built-in" and "lab" → open "Muster" → "Muster raises its Internal
   alerts through this Integration." and no "Edit", "Delete" or "Create token".
2. Send group g6 four times 300 s apart (S-020) → open "lab" → "Alertmanager routes" shows `{}/{team="web"}` with
   "5 min" and "Resolves by absence after 15 min".
3. Send group g7 with a 2-hour gap (S-021) → the page shows "Alertmanager route {}/{kind="info"} repeats every 2 h;
   Muster can resolve its alerts by absence only after 6 h." and a snippet with `repeat_interval: 10m` → "Copy".
4. Send group g2 with `max_alerts: 2` → reload "lab" → "Alertmanager truncates Snapshots for 1 group. Set
   `max_alerts: 0` on the Alertmanager receiver." (the singular form) → the Integrations list shows the warning mark on
   "lab" → open "Muster" → its Alerts view lists `MusterSnapshotTruncated` as "Firing".
5. Alerts → type `instance="db-a"` → the view shows one row with `cluster` = `a` and "Static label cluster not applied:
   the alert has its own value."
6. Resolved tab → the row of `instance="db-c"` shows "Resolved: Gone" and, on hover, "Alertmanager no longer reports
   this alert — …".
7. Type `pod=~"[` → the field shows the syntax error as typed ("Close the quoted value with a quote.") → complete it
   to `pod=~"["`, whose regular expression does not compile → "Add matcher" → the field shows the server's error and
   the rows stay.

`make e2e` runs these steps as `web/e2e/integration-page.spec.ts`.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): add learned alertmanager routes, warnings and the alerts view`.
- The plural forms of "N groups" go through i18next's plural rules (NFR-8).

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-06.FR-14 | full | together with S-021 |
| C-06.FR-18 | full | together with S-021 |
| C-06.FR-19 | partial | the view; Route and Severity level columns are S-027, the Alert Group column S-031 |
| C-06.AC-3 | partial | the page; "never Stale without a Heartbeat" is completed by S-023 |
| C-06.AC-8 | full | together with S-020 |
| C-05.FR-7 | partial | the learned routes, warnings and Alerts view; the Heartbeat state is S-024 |
