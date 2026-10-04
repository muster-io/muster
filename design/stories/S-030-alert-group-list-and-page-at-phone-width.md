---
id: S-030
title: Alert Group list and Alert Group page at phone width (FE)
capability: C-09
kind: fe
layer: L1
depends_on: [S-017, S-029]
covers: [C-09.FR-7, C-09.FR-10, C-09.FR-11, C-09.FR-13, C-09.FR-14, C-09.FR-16, C-09.FR-17, C-09.FR-20, C-09.FR-24, C-09.FR-25, C-09.AC-4, C-09.AC-13, C-09.AC-15, C-09.AC-16, C-09.AC-18, C-09.AC-19]
files_touched:
  - web/src/routes/index.tsx
  - web/src/routes/alert-groups.index.tsx
  - web/src/routes/alert-groups.$alertGroupId.tsx
  - web/src/components/alert-group-table.tsx
  - web/src/components/alert-group-filters.tsx
  - web/src/components/status-tabs.tsx
  - web/src/components/time-range-picker.tsx
  - web/src/components/label-columns-picker.tsx
  - web/src/components/new-alert-groups-banner.tsx
  - web/src/components/alert-group-header.tsx
  - web/src/components/alert-group-notices.tsx
  - web/src/components/alert-group-alerts.tsx
  - web/src/components/alert-group-labels.tsx
  - web/src/components/timeline.tsx
  - web/src/components/related-alert-groups.tsx
  - web/src/components/relative-time.tsx
  - web/src/lib/alert-group-search.ts
  - web/src/lib/live.ts
  - web/src/components/alert-group-filters.test.tsx
  - web/src/components/timeline.test.tsx
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/alert-group-list.spec.ts
  - web/e2e/alert-group-page.spec.ts
  - web/e2e/phone-width.spec.ts
acceptance:
  - "[C-09.FR-13] The Alert Group list is the home page: tabs Open (the default: firing, acknowledged and snoozed), Firing, Acknowledged, Snoozed, Resolved and All, each with its count for the other filters; filters for Route, Integration, Severity level, Urgent, resolved by, Reopened and label Matchers; time range presets and a custom range (7 days by default); search by `#N` or text; sorting by start or last change; \"Load more\"."
  - "[C-09.FR-13, C-09.AC-15] Tab, filters, range, search, chosen label columns and sorting live in the URL: opening it in another browser shows the same view with the same columns."
  - "[C-09.FR-25, C-09.AC-13] With the list open on the Firing tab, a new Alert Group appears as \"1 new\" above the list within seconds, without a reload, and the Firing count grows by one; rows that change update in place, and the list never moves until \"1 new\" is clicked."
  - "[C-09.FR-14, C-09.FR-10] The Alert Group page shows the header (status, `#N`, title, Severity level, Urgent, linked Route, Integrations, start and duration, Reopen count, the system's resolution reason), the notices, the Alerts — firing first, resolved struck through with their reason, annotations expandable, filterable — the group labels, common labels and common annotations, and the Timeline."
  - "[C-09.FR-11] The Timeline shows each entry with its time, actor and Transport, Loud or Quiet and whom it asked to mention, newest or oldest first, filtered by kind; `muster_unavailable` reads \"Muster was unavailable from {from} to {to}\"."
  - "[C-09.FR-7, C-09.AC-4] After a Replacement the page shows \"Alerts were replaced because `pod` changed. Consider removing Instance labels from the rule, for example `without(pod, instance)`.\""
  - "[C-09.FR-20, C-09.AC-16] The page lists the previous Alert Groups of the same Route and key with number, status, start, duration and who resolved them, each linked."
  - "[C-09.FR-16, C-09.AC-18] An Alert Group whose details were removed opens with \"Alerts and Timeline of this Alert Group were removed after 90 days; only its summary and Notes are kept.\""
  - "[C-09.FR-17] Times are in the user's time zone, durations are relative (\"2 h 14 min\") with the absolute time on hover."
  - "[C-09.FR-24, C-09.AC-19] In a browser 360 CSS pixels wide the list shows compact rows (status, `#N`, title, Urgent mark, duration) with the filters in a panel, the page shows the header first, and neither scrolls horizontally; a responder picks the Firing tab, opens an Alert Group and reads its Alerts and Timeline."
verify: "make ci e2e"
operator_attention: false
issue: 30
---

# S-030. Alert Group list and Alert Group page at phone width (FE)

## Scope

**IN**

- The Alert Group list as the home page: tabs with counts, filters, time range, search, sorting, label columns, paging
  and live updates, all kept in the URL.
- The Alert Group page: header, notices, Alerts, labels and annotations, Timeline, previous Alert Groups.
- Both at phone width.

**OUT**

- Commands, the Note box, bulk selection and the Owner filters and columns (S-033); links (S-038); delivery state
  (S-040); the next notice or Reminder and Unclaimed (S-050).
- The statistics page (S-031).

## Contracts

- **API used**: `listAlertGroups`, `getAlertGroupCounts`, `getAlertGroup`, `listAlertGroupAlerts`,
  `getAlertGroupTimeline`, `listRelatedAlertGroups`, `listRoutes`, `listIntegrations`, `streamLiveUpdates`.
- **Routes and navigation**: `/` redirects to `/alert-groups`, which replaces the placeholder of S-014; navigation
  entry "Alert Groups", first, with `alert-groups:read`; `/alert-groups/$alertGroupId` is the page.
- **List** (C-09.FR-13):
  - tabs Open (default), Firing, Acknowledged, Snoozed, Resolved, All; counts from `getAlertGroupCounts` (Open is the
    sum of the first three);
  - filters — Route and Integration (multiple), Severity level, Urgent, resolved by (a person, or the system with a
    reason), Reopened, labels through the Matcher input of S-022 — in a side panel on a desktop and a sheet on a phone;
  - time range — "Last hour", "Last 24 hours", "Last 7 days" (default, `alert_group.list_range`), "Last 30 days",
    "Custom"; search — a `#N` or text; sorting — start time or last change, either direction;
  - desktop columns — status, `#N`, title, Severity level, Urgent, Route, Integrations, firing and total Alerts, start
    and duration, last change, Reopen count, and the label columns picked by the user (`label_columns`, none by
    default), "Load more" by cursor;
  - the URL holds tab, filters, range, search, label columns and sorting as typed search parameters
    (`web/src/lib/alert-group-search.ts`).
- **Live updates** (C-09.FR-25): `alert-group` hints refresh that Alert Group's row and page in place; `alert-groups`
  hints refresh the counts and fetch the first page in the background; Alert Groups that are not shown yet are announced
  as "N new" above the list and added when it is clicked; after a reconnect everything is re-read.
- **Page** (C-09.FR-14, FR-10): the header — status, `#N`, title, Severity level, "Urgent", the Route linked, the
  Integrations linked, start and duration, "🔁 Reopened ×N" when the count is above zero, and for a resolved Alert
  Group "Resolved by {name}" or "Resolved: {reason}"; the notices of `AlertGroupNotice` with the texts of
  [reference.md](../prd/l1/reference.md#banners-warnings-and-notices) — "⚠ N alerts still firing in Alertmanager",
  the Replacement text, the removed-details text, and "Firing again after a manual resolve of #N" linked; the Alerts
  with a Firing/Resolved filter; group labels, common labels, and the common annotations with `summary` and
  `description` shown first; the Timeline; previous Alert Groups (C-09.FR-20).
- **Timeline** (C-09.FR-11): oldest or newest first, kind chips `status`, `alerts`, `notes`, `timers`, `delivery`,
  `system`; each entry shows the time, the actor ("Muster" for `system`) with the Transport, a text per event or system
  entry written in this story, a "Loud" mark and "Mentions: …" listing the symbolic Mentions, and its details
  (fingerprints, the replaced label, the period of `muster_unavailable`).
- **Times** (C-09.FR-17): the profile's or the browser's time zone; durations like "2 h 14 min"; the absolute time in
  a tooltip.
- **Phone width** (C-09.FR-24, NFR-15): from 360 CSS pixels, the list renders compact rows — status, `#N`, title,
  Urgent mark, duration — and the page stacks the header, the Alerts and the Timeline; neither page scrolls
  horizontally (long label values wrap).

## Steps

1. Write the search parameters, tabs, filters, time range and table. Check: the component test round-trips every filter
   through the URL.
2. Add search, sorting, label columns and paging. Check: Playwright reproduces a view from its URL in a new context.
3. Add live updates with "N new". Check: Playwright sees "1 new" after the fake Alertmanager sends a new Alert.
4. Write the page with its sections and the Timeline. Check: the component test renders each event kind; Playwright
   reads a Reopen and a Replacement.
5. Make both pages work at 360 pixels. Check: `phone-width.spec.ts` asserts no horizontal scroll.

## Verification

Run `make dev`, sign in as `admin@example.org` / `muster-dev-password`; drive the fake Alertmanager and the development
clock from a terminal as in S-028 and S-029. Then in Playwright, at desktop width:

1. `/` opens "Alert Groups" on "Open" with counts on every tab.
2. Filter Labels `namespace="payments"`, "Urgent", range "Last 30 days", add the label column `pod` → the list shows the
   "k8s" Alert Group only; copy the URL into a new browser context → the same rows, filters and the `pod` column.
3. Search `postgres` → one row "KubePodCrashLooping"; search `#` with its number → the same row, the range ignored.
4. "Firing" tab → from the terminal send a new Alert of a new key → within 5 seconds "1 new" appears and the Firing
   count grows by one; the rows do not move until "1 new" is clicked.
5. Open the Alert Group of the Replacement of S-028 → "Alerts were replaced because `pod` changed." → the Timeline
   shows "Alert replaced" without the "Loud" mark; the Reopened Alert Group of S-028 shows "🔁 Reopened ×1" and a
   "Reopened" entry with "Loud" and "Mentions: reopen".
6. The page of the Alert Group resolved by the system shows "Resolved: Resolved by Alertmanager", and "Previous Alert
   Groups" lists the earlier `#N` with "resolved" and its duration.
7. The Alert Group whose details were removed (S-029) → "Alerts and Timeline of this Alert Group were removed after 90
   days; only its summary and Notes are kept."
8. Hover a duration → a tooltip with the absolute time in the profile's time zone.

At 360 × 740 pixels:

9. The list shows compact rows and a "Filters" button; `document.documentElement.scrollWidth` equals the viewport width.
10. "Filters" → "Firing" tab → open the first row → the header is at the top, then the Alerts, then the Timeline; no
    horizontal scroll.

`make e2e` runs these steps as `web/e2e/alert-group-list.spec.ts`, `alert-group-page.spec.ts` and `phone-width.spec.ts`.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): add the alert group list and page, usable at phone width`.
- The default tab Open of C-09.FR-13 has no count of its own in `AlertGroupCounts`: it is the sum of the three open
  statuses.
- Later stories add to these pages: commands, Notes, selection and Owner (S-033), links (S-038), delivery state and
  "Delivery problem" (S-040), the next notice, Unclaimed and "Still on it" (S-050).

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-09.FR-7 | full | together with S-028 |
| C-09.FR-10 | partial | the page; messages are C-11 and C-12 |
| C-09.FR-11 | partial | the page; later entry types come with their capabilities |
| C-09.FR-13 | full | together with S-029; the later filters come with S-033, S-040 and S-050 |
| C-09.FR-14 | full | together with S-028; later sections come with S-033, S-038, S-040 and S-050 |
| C-09.FR-16 | partial | the page notice; keeping Notes is S-032 |
| C-09.FR-17 | full | |
| C-09.FR-20 | full | together with S-029 |
| C-09.FR-24 | partial | the list and the page; the commands at phone width are S-033 |
| C-09.FR-25 | full | together with S-012 and S-029 |
| C-09.AC-4 | full | together with S-028 |
| C-09.AC-13 | full | together with S-029 |
| C-09.AC-15 | full | together with S-029 |
| C-09.AC-16 | full | together with S-029 |
| C-09.AC-18 | full | together with S-029 |
| C-09.AC-19 | full | |
