---
id: S-195
title: The "Kept back" banner, badge and filter on the Alert Group page and list, and the marks in the Timeline and delivery states (FE)
capability: C-34
kind: fe
layer: L2
depends_on: [S-194, S-192, S-159]
covers: [C-34.FR-20, C-34.FR-13, C-34.FR-17, C-34.AC-16]
files_touched:
  - web/src/routes/alert-groups.$alertGroupId.tsx
  - web/src/routes/alert-groups.index.tsx
  - web/src/components/alert-group-kept-back.tsx
  - web/src/components/alert-group-kept-back.test.tsx
  - web/src/components/alert-group-header.tsx
  - web/src/components/alert-group-header.test.tsx
  - web/src/components/alert-group-table.tsx
  - web/src/components/alert-group-filters.tsx
  - web/src/components/alert-group-filters.test.tsx
  - web/src/components/alert-group-deliveries.tsx
  - web/src/components/alert-group-deliveries.test.tsx
  - web/src/components/timeline.tsx
  - web/src/components/timeline.test.tsx
  - web/src/lib/alert-group-search.ts
  - web/src/lib/commands.ts
  - web/src/test/fixtures/alert-groups.ts
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/alert-group-page.spec.ts
  - web/e2e/alert-group-list.spec.ts
  - web/e2e/maintenance-windows.spec.ts
  - web/e2e/phone-width.spec.ts
acceptance:
  - "[C-34.FR-20, C-34.AC-16] In a real browser the Alert Group page of a kept-back Alert Group shows \"Kept back by the Maintenance window of {object} until {time}: nothing is delivered and no Escalation chain runs.\" with a link to the window; the banner goes when the window ends, in English and in Russian, 360 CSS pixels wide without horizontal scrolling of the page (`alert-group-kept-back.test.tsx`, `alert-group-header.test.tsx`, `maintenance-windows.spec.ts`)."
  - "[C-34.FR-20, C-34.AC-16] The Alert Group list has the filter \"Kept back\" (the URL's `kept_back`) and the badge \"Kept back\" on each such row, at 360 px without horizontal scrolling (`alert-group-filters.test.tsx`, `alert-group-list.spec.ts`, `phone-width.spec.ts`)."
  - "[C-34.FR-13] The Timeline shows `maintenance_started` and `maintenance_ended` with the window and the cause (a window ended, was closed early, was changed or deleted with its object, the Alert Group became Urgent, a Team joined, it moved to another Route) in English and Russian (`timeline.test.tsx`)."
  - "[C-34.FR-17] The delivery state of a Destination where an Alert Group resolved during a window was never posted reads \"Not posted: resolved during a Maintenance window\" (`alert-group-deliveries.test.tsx`, `maintenance-windows.spec.ts`)."
verify: "make ci e2e-web"
operator_attention: false
issue: null
---

# S-195. The "Kept back" banner, badge and filter on the Alert Group page and list, and the marks in the Timeline and delivery states (FE)

## Scope

**IN**

- The banner of a kept-back Alert Group with the link to its window, the badge and the filter of the list (C-34.FR-20).
- The Timeline's texts for `maintenance_started` and `maintenance_ended` with their causes (C-34.FR-13) and the delivery
  state "Not posted: resolved during a Maintenance window" (C-34.FR-17).
- English and Russian texts; the 360 px layout of the page and the list.

**OUT**

- The sections of the Integration, Route and Team pages and the list of windows: S-194. The API: S-190 to S-192.

## Contracts

- **API used**: `getAlertGroup` (`kept_back`, `maintenance_windows`), `listAlertGroupDeliveries` (the `withheld_reason`
  `resolved_in_maintenance` of an `AlertGroupDelivery`, added by S-192), `listAlertGroups` (`kept_back`),
  `getAlertGroupTimeline` (the entries with `maintenance_window` and `cause`).
- **Components**: `alert-group-kept-back.tsx` — the banner of the page, from `maintenance_windows`, with the window's
  object, end and a link to the object's page; `alert-group-header.tsx` shows it and the badge; `alert-group-table.tsx`
  and `alert-group-filters.tsx` — the badge and the filter, with `alert-group-search.ts` for the URL;
  `alert-group-deliveries.tsx` — the state's text; `timeline.tsx` — the generic text of S-148 gains the two events and the
  seven causes of S-191 (`window_ended`, `window_closed`, `window_changed`, `window_deleted`, `urgent`, `team_joined`,
  `moved`); `lib/commands.ts` is unchanged except that a Command on a kept-back Alert Group stays enabled (they work,
  C-34.FR-10).
- **Texts** (EN / RU), from the Banners table of reference.md: "Kept back by the Maintenance window of {object} until
  {time}: nothing is delivered and no Escalation chain runs." / «Удержано окном обслуживания {object} до {time}: ничего
  не доставляется, цепочки эскалации не работают.», "Kept back" / «Удержано», "Not posted: resolved during a Maintenance
  window" / «Не опубликовано: закрыто во время окна обслуживания», the Timeline texts for the two events and their
  causes. `en.json` and `ru.json` are the real files.
- **Live updates**: the existing hint `alert-group` refreshes the page and the list.

## Steps

1. Add the banner, the badge and the filter. Check: `alert-group-kept-back.test.tsx`, `alert-group-filters.test.tsx`.
2. Add the Timeline texts and the delivery state. Check: `timeline.test.tsx`, `alert-group-deliveries.test.tsx`.
3. Add the texts and the specs. Check: the Verification below; `make e2e`.

## Verification

Playwright against `make dev`, in English and then Russian, and at 360 px for 1 and 2.

1. Start a window on the Team "payments" (the Team page of S-194), send a payments-only Alert Group. Open its page.
   Expect "Kept back by the Maintenance window of payments until {time}: nothing is delivered and no Escalation chain
   runs." with a link that opens the Team page.
2. Open the Alert Group list. Expect the badge "Kept back" on the row; choose the filter "Kept back" and expect only
   that row, and the URL to carry `kept_back=true`.
3. Press "End now" on the Team page; return to the Alert Group page. Expect the banner gone and, in the Timeline, "The
   Maintenance window ended" with the cause "window closed" (or its Russian text).
4. Resolve an Alert Group during a second window and close the window. Expect its delivery row to read "Not posted:
   resolved during a Maintenance window".
5. Expect no horizontal scrolling of the page at 360 px in each case.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): show kept-back alert groups on their page and in the list`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases.
- Placed after S-192 so that the delivery state exists in the API; it depends on S-194 for the specs' shared helpers.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-34.FR-13 | partial | the Timeline texts; the events are S-191, S-192 |
| C-34.FR-17 | partial | the delivery state's text; the rule is S-192 |
| C-34.FR-20 | partial | the Alert Group page and list; the Integration, Route and Team pages and the list of windows are completed by S-194 |
| C-34.AC-16 | partial | the Alert Group page and list at 360 px; the other pages are completed by S-194 |
