---
id: S-194
title: The Maintenance windows sections of the Integration, Route and Team pages, and the list of windows (FE)
capability: C-34
kind: fe
layer: L2
depends_on: [S-190, S-191, S-123, S-120, S-158]
covers: [C-34.FR-4, C-34.FR-20, C-34.AC-1, C-34.AC-16]
files_touched:
  - web/src/routes/maintenance-windows.index.tsx
  - web/src/components/app-shell.tsx
  - web/src/components/app-shell.test.tsx
  - web/src/routes/integrations.$integrationId.index.tsx
  - web/src/routes/routes.$routeId.tsx
  - web/src/routes/teams.$teamId.tsx
  - web/src/components/maintenance-windows-section.tsx
  - web/src/components/maintenance-windows-section.test.tsx
  - web/src/components/maintenance-window-form.tsx
  - web/src/components/maintenance-window-form.test.tsx
  - web/src/components/maintenance-window-banner.tsx
  - web/src/components/maintenance-window-banner.test.tsx
  - web/src/components/maintenance-window-list.tsx
  - web/src/components/maintenance-window-list.test.tsx
  - web/src/lib/maintenance.ts
  - web/src/lib/maintenance.test.ts
  - web/src/components/audit-diff.tsx
  - web/src/components/audit-diff.test.tsx
  - web/src/test/fixtures/maintenance-windows.ts
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/support.ts
  - web/e2e/maintenance-windows.spec.ts
  - web/e2e/phone-width.spec.ts
acceptance:
  - "[C-34.FR-20, C-34.AC-16] In a real browser the Team page has a section \"Maintenance windows\" with \"Start maintenance\", which opens a window from now until a preset of `maintenance.presets` (30 minutes, 1 hour, 2 hours, 4 hours) or a time entered, \"Plan a window\", \"Reason\", \"Let Urgent Alert Groups through\", and the active and planned windows with \"End now\", \"Change\" and, for planned ones, \"Delete\"; starting a window from a preset shows the banner \"In a Maintenance window until {time}: {N} Alert Groups kept back.\", and \"End now\" removes it; the Route and Integration pages have the same section (`maintenance-windows-section.test.tsx`, `maintenance-window-form.test.tsx`, `maintenance-window-banner.test.tsx`, `maintenance-windows.spec.ts`)."
  - "[C-34.FR-20] The Team section says \"Keeps back an Alert Group only while every Team of its Alerts is in a Maintenance window: Alert Groups shared with Teams outside a window are still delivered.\"; the Integration page shows the banner \"In a Maintenance window until {time}: {N} Alert Groups kept back.\" with the count of the Alert Groups kept back, and while a window is planned lists it as planned; a Responder who is not in the Team closes the active window from the Team page with \"End now\", which the Audit log records (C-34.AC-1) (`maintenance-windows-section.test.tsx`, `maintenance-windows.spec.ts`)."
  - "[C-34.FR-4] The form takes and shows the start and the end in the time zone of the profile, with the date where it is not today, and sends RFC 3339 with an offset; a window refused with `422` shows the refusal at its field (`maintenance.ts` tests, `maintenance-window-form.test.tsx`)."
  - "[C-34.FR-20] The page \"Maintenance windows\", reached from the main navigation, lists the active, planned and ended windows of the Organization with their object, start, end, reason and who opened them, filtered by state and by object type, with a link to the object; a Viewer sees the list and none of the buttons, since `maintenance:write` is missing (`maintenance-window-list.test.tsx`, `maintenance-windows.spec.ts`)."
  - "[C-34.AC-16] The sections and the list work at 360 CSS pixels without horizontal scrolling of the page, in English and in Russian, and every new text exists in both (`maintenance-windows.spec.ts`, `phone-width.spec.ts`); the Audit log shows the entries `maintenance_window.*` with their diff (`audit-diff.test.tsx`)."
verify: "make ci e2e-web"
operator_attention: false
issue: null
---

# S-194. The Maintenance windows sections of the Integration, Route and Team pages, and the list of windows (FE)

## Scope

**IN**

- The section "Maintenance windows" on the pages of an Integration, a Route and a Team, with "Start maintenance", "Plan a
  window", the form, the active and planned windows and their actions, the banner with the count, and on the Team page the
  hint about every Team of an Alert Group (C-34.FR-20).
- The page "Maintenance windows", with its entry in the main navigation and its filters; the Audit log entries of windows; the time
  zone of the profile in the form (C-34.FR-4, FR-20).
- Texts in English and Russian.

**OUT**

- The API: S-190, S-191. The banner and the "Kept back" badge on the Alert Group page and list: S-195.
- The documentation: S-196. Screenshots: S-196.

## Contracts

- **API used**: `listMaintenanceWindows`, `createMaintenanceWindow`, `getMaintenanceWindow`,
  `updateMaintenanceWindow` (with `If-Match`), `deleteMaintenanceWindow`, `endMaintenanceWindow`, `getIntegration`,
  `getRoute`, `getTeam` (their `maintenance` field), `getMe` (the time zone of the profile), `listAuditLog`.
- **Routes**: `/maintenance-windows` (the list). The main navigation (`NAVIGATION` and `NavLabel` in
  `web/src/components/app-shell.tsx`, S-072) has no group named "Configuration" and no entry for Escalation chains; the
  PRD's "Configuration → Maintenance windows" is the group of its entries for Integrations, Routes, Connections and
  Destinations, so the list gets an entry of its own, `maintenanceWindows`, after Destinations, shown with
  `maintenance:read` (its label under `nav.maintenanceWindows` in both locale files, and the `navLabel` switch). The
  three object pages gain the section; the list links to them.
- **Components**: `maintenance-windows-section.tsx` — the active and planned windows of one object and the buttons by
  `maintenance:write`; `maintenance-window-form.tsx` — "Start maintenance" (a preset or "Until") and "Plan a window"
  (start and end), "Reason", "Let Urgent Alert Groups through", with the refusals of S-190 at their fields;
  `maintenance-window-banner.tsx` — "In a Maintenance window until {time}: {N} Alert Groups kept back." from
  `maintenance.windows` and `kept_back_count`, live through the hint of `alert-group` and the windows;
  `maintenance-window-list.tsx` — the page's table with the filters in the URL; `lib/maintenance.ts` — the presets, the
  time zone of the profile and the state of a window in words; `audit-diff.tsx` — the entries `maintenance_window.*`.
- **Documentation links**: none here; the page they name does not exist until S-196, and `docslinkcheck` fails on an
  entry whose page is missing, so S-196 adds `page.maintenance_windows` and `field.maintenance_urgent` and the Guide
  link beside the page title.
- **Texts** (EN / RU), from C-34's UI and the Banners table of reference.md: "Maintenance windows" / «Окна
  обслуживания», "Start maintenance" / «Начать обслуживание», "Plan a window" / «Запланировать окно», "Until" / «До»,
  "Reason" / «Причина», "Let Urgent Alert Groups through" / «Пропускать срочные группы алертов», "End now" / «Закончить
  сейчас», "Change" / «Изменить», "Delete" / «Удалить», the banner / «В окне обслуживания до {time}: удержано групп
  алертов — {N}.» with the Russian plurals, the Team hint / «Удерживает группу алертов, только пока в окне
  обслуживания все команды её алертов: группы, общие с командами вне окна, по-прежнему доставляются.», the filters
  "Active", "Planned", "Ended" / «Идёт», «Запланировано», «Закончилось». `en.json` and `ru.json` are the real files;
  a missing key fails CI.
- **Permissions**: `maintenance:read` shows the list and the section; `maintenance:write` the buttons.

## Steps

1. Write `lib/maintenance.ts`, the form and its time-zone handling. Check: `maintenance.test.ts`,
   `maintenance-window-form.test.tsx`.
2. Write the section, the banner and add them to the three pages. Check: `maintenance-windows-section.test.tsx`,
   `maintenance-window-banner.test.tsx`.
3. Write the list page and the Audit log entries. Check: `maintenance-window-list.test.tsx`, `audit-diff.test.tsx`.
4. Add the texts, the links and the end-to-end specs. Check: the Verification below; `make e2e`.

## Verification

Playwright against `make dev` (as `web/e2e/support.ts` sets it up), in English and then Russian; the 360 px runs use
`phone-width.spec.ts`'s viewport.

1. Sign in as the Admin and open the Team "payments". Expect the section "Maintenance windows" and the hint "Keeps back
   an Alert Group only while every Team of its Alerts is in a Maintenance window: Alert Groups shared with Teams outside
   a window are still delivered."
2. Press "Start maintenance", choose "30 minutes", type "deploy" in "Reason" and press the button. Expect the banner "In a
   Maintenance window until {time}: 0 Alert Groups kept back." and one active window in the list with "End now" and
   "Change".
3. Send a webhook for a payments Alert Group (the fake Alertmanager's `PUTA` and `NOTIFY`). Expect the banner to read "…: 1
   Alert Groups kept back." live, without a reload.
4. Sign in as a Responder who is not in the Team "payments" and open its page. Expect the active window with "End now";
   press it. Expect the banner to disappear and the window to move to "Ended" on the page "Maintenance windows"; the Audit
   log (step 9) names the Responder.
5. Open the Integration "am-prod", press "Start maintenance" with "1 hour" and an Alert Group of it kept back through
   the fake Alertmanager. Expect the banner "In a Maintenance window until {time}: 1 Alert Groups kept back." on the
   Integration page, and end the window. Then press "Plan a window", enter a start next Saturday 02:00 and an end
   04:00. Expect the window listed as planned with "Delete"; press "Delete" and expect it gone.
6. Enter an end before the start. Expect the refusal at the end field.
7. Sign in as a Viewer. Expect the list and the sections without "Start maintenance" and the other buttons.
8. Repeat 1 to 2 and the list at 360 px; expect no horizontal scrolling of the page. Switch to Russian and expect the
   Russian texts and the plural of the banner («удержано групп алертов — 1»).
9. Open the Audit log. Expect `maintenance_window.created` and `maintenance_window.updated` with the diff.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): add maintenance windows to the integration, route and team pages and a list of windows`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases.
- The Guide link and the field link of "Let Urgent Alert Groups through" are S-196's, for the reason in Contracts.
- The approved mockups of the core show the windows on the Team and Route pages (L2 §5.2); the design follows them.
- The PRD's "Configuration" is not a group of the navigation as it stands; see Contracts for the entry this story adds.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-34.FR-4 | partial | the time zone of the profile in the form; the API's times (S-190) complete it |
| C-34.AC-1 | partial | "a Responder closes it from the UI"; the API half is S-190's |
| C-34.FR-20 | partial | the sections, the banner and the list; the Alert Group page and list are S-195 |
| C-34.AC-16 | partial | the Team, Integration and list pages; the Alert Group page is S-195 |
