---
id: S-226
title: The tables "Who was Paged" and "Noisy alerts" of the dashboard with their empty states (FE)
capability: C-40
kind: fe
layer: L2
depends_on: [S-225, S-223, S-224, S-158, S-159]
covers: [C-40.FR-1, C-40.FR-3, C-40.FR-6, C-40.FR-7, C-40.FR-8, C-40.FR-9, C-40.FR-11, C-40.AC-3, C-40.AC-4, C-40.AC-5, C-40.AC-6, C-40.AC-7, C-40.AC-8]
files_touched:
  - web/src/components/home-dashboard.tsx
  - web/src/components/home-dashboard.test.tsx
  - web/src/components/dashboard-paged-table.tsx
  - web/src/components/dashboard-paged-table.test.tsx
  - web/src/components/dashboard-noisy-table.tsx
  - web/src/components/dashboard-noisy-table.test.tsx
  - web/src/lib/dashboard.ts
  - web/src/lib/dashboard.test.ts
  - web/src/lib/alert-group-search.ts
  - web/src/test/fixtures/dashboard.ts
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/home-dashboard.spec.ts
  - web/e2e/phone-width.spec.ts
acceptance:
  - "[C-40.FR-6, C-40.AC-4] \"Who was Paged\" reads `getPageStatistics` for the dashboard's period and filters and shows a row for each User with \"Paged\", \"Calls\", \"Outside Working hours\", \"Not reached\" and \"Acknowledged first\"; rows go by \"Paged\" plus \"Calls\", most first, then by name, and every column sorts by its header, ascending or descending, in the page's own state, with the current sort shown to assistive technology; with the data of C-40.AC-4 Petr reads Paged 2, Calls 2, Outside Working hours 2, Not reached 1, and Acknowledged first 1 after he acknowledges one (`dashboard-paged-table.test.tsx`, `home-dashboard.spec.ts`)."
  - "[C-40.FR-6] The hint of the table says what it counts — Pages by Mention and calls through a Notification gateway that named a person, whenever made, none for a Team or the whole chat, \"Calls\" one per Page however many Alert Groups it combined, Working hours those of the chain's owning Team else the Organization's, recorded when the Page was made — and, from `outside_working_hours_since`, that the column counts Pages made before that moment as inside Working hours; it says that time On-call from the Schedules is not shown and why (`dashboard-paged-table.test.tsx`, `home-dashboard.spec.ts`)."
  - "[C-40.FR-7, C-40.AC-5] \"Noisy alerts\" reads `getAlertTitleStatistics` and shows each title with \"Alert Groups\", \"Reopens\", \"Never acknowledged\", \"Median time to resolve\" (\"—\" for none) and \"Routes\"; with the data of C-40.AC-5 it lists `KubePodCrashLooping` first with 3, 2 and 2, then `NodeDown` before `DiskFull`, and not `CertExpiring`; the hint says that the figures come from the summary rows and that the search also matches Alert Groups whose title or `summary` only contains the title and those whose lifetime overlaps the period (`dashboard-noisy-table.test.tsx`, `home-dashboard.spec.ts`)."
  - "[C-40.FR-7, C-40.AC-5] A title is a link to the Alert Group list searched by that title with the dashboard's filters, without \"Acknowledged first by\", and a time range covering the period; in a real browser the link of `KubePodCrashLooping` opens the list showing those 3 Alert Groups; the list gains no filter by exact title (`dashboard.test.ts`, `dashboard-noisy-table.test.tsx`, `home-dashboard.spec.ts`)."
  - "[C-40.FR-9, C-40.AC-7] Empty states: nobody Paged — \"Nobody was Paged in this period.\", and while no Escalation chain exists \"No Escalation chain Pages anyone yet.\" with a link to Escalation chains shown only to a User with `escalation-chains:write`; no title with more than one Alert Group — \"No alert started more than one Alert Group in this period.\"; on an empty installation every part of the dashboard shows its empty state (`dashboard-paged-table.test.tsx`, `dashboard-noisy-table.test.tsx`, `home-dashboard.spec.ts`)."
  - "[C-40.FR-8, C-40.AC-7] The two tables are parts of S-225's kind: each reads separately and shows its own error and \"Try again\" when its request answers `500`, with the other parts unchanged; both follow the period, the filters and \"Refresh\" and read again every `dashboard.refresh_interval`, and a live-update hint reads neither of them (`home-dashboard.test.tsx`, `home-dashboard.spec.ts`)."
  - "[C-40.FR-1, C-40.FR-3, C-40.AC-3, C-40.AC-6] A Viewer sees both tables with every row, as everyone with `alert-groups:read` sees By people; with the Team \"payments\" chosen both count only its Alert Groups and the URL carries the filter (`home-dashboard.spec.ts`)."
  - "[C-40.FR-11, C-40.AC-8] At 360 CSS pixels wide each table scrolls inside its own region and the Home page does not scroll sideways, in English and in Russian; every new text exists in English and Russian with Russian plurals (`phone-width.spec.ts`, the missing-key check of `make ci`)."
verify: "make ci e2e-web"
operator_attention: false
issue: null
---

# S-226. The tables "Who was Paged" and "Noisy alerts" of the dashboard with their empty states (FE)

## Scope

**IN**

- The two tables of the dashboard with their sorting, hints, links, empty states and phone scrolling (C-40.FR-6,
  FR-7, FR-9), mounted into the section S-225 built.
- The check, with them, of the whole page against C-40.AC-3 to AC-8.

**OUT**

- The section, the period, the filters, the figures and the chart: S-225. The reads: S-223, S-224.
- A filter of the Alert Group list by exact title: C-40.FR-7 says the list gains none.
- The documentation and its link: S-227.

## Contracts

- **API used**: `getPageStatistics` and `getAlertTitleStatistics` with the period and the filters of the section; the
  generated client of S-223 and S-224; `listEscalationChains` (a limit of 1, to decide "while no Escalation chain
  exists"). The links go to the Alert Group list (`/alert-groups`) with the search of the
  list's own parameters: `web/src/lib/alert-group-search.ts` builds the search from a title, the filters and the period
  (`from`, `to` covering it; the hint says the range is of the lifetime), adding no new filter.
- **Components**: `dashboard-paged-table.tsx` — the table of people, sorted in its own state by every column (the
  default `paged + calls` descending, then name), the hint, and the empty states; `dashboard-noisy-table.tsx` — the
  table of titles, each a link, its hint and empty state. Both are wrapped in S-225's `dashboard-part.tsx`, scroll
  inside their own region at phone width, and use `wrap-anywhere` for titles and Route names. `home-dashboard.tsx`
  mounts them after the chart. `web/src/lib/dashboard.ts` gains the sort of the people table and the search of the
  title link.
- **Permissions**: `alert-groups:read` for both tables; the link to Escalation chains in the empty state needs
  `escalation-chains:write`, checked with `useCan`, so a Viewer reads the sentence without the link.
- **Texts** (EN / RU; a missing key fails CI): "Who was Paged" / «Кого вызывали»; "Paged" / «Вызовов»; "Calls" /
  «Звонков»; "Outside Working hours" / «Вне рабочих часов»; "Not reached" / «Не дошли»; "Acknowledged first" /
  «Подтвердил первым»; "Noisy alerts" / «Шумные алерты»; "Reopens" / «Переоткрытий»; "Never acknowledged" / «Ни разу не
  подтверждены»; "Median time to resolve" / «Медианное время до решения»; "Routes" / «Маршруты»; "Nobody was Paged in
  this period." / «За этот период никого не вызывали.»; "No Escalation chain Pages anyone yet." / «Ни одна цепочка
  эскалации пока никого не вызывает.»; "No alert started more than one Alert Group in this period." / «За этот период ни
  один алерт не начал больше одной группы.»; the hints.
- **Fixtures**: `web/src/test/fixtures/dashboard.ts` gains the builders of titles and Pages.

## Steps

1. Extend `web/src/lib/dashboard.ts` and `alert-group-search.ts` with the sort and the title link. Check:
   `dashboard.test.ts`.
2. Write the two tables with their hints and empty states. Check: `dashboard-paged-table.test.tsx` (sorting, the
   hint with `outside_working_hours_since`, the link by Permission), `dashboard-noisy-table.test.tsx` (the order of the
   fixture, the link's search, "—").
3. Mount them in `home-dashboard.tsx` and add the texts. Check: `home-dashboard.test.tsx` (one failing part leaves the
   others), `make ci` (missing keys).
4. Extend `home-dashboard.spec.ts` and `phone-width.spec.ts`. Check: the Verification below.

## Verification

Playwright against `make dev`, in English and then Russian, with the data of S-225 plus those of C-40.AC-4 (an
Escalation chain Paging Petr importantly on two Alert Groups, his rule a Mention, a wait of 5 minutes and a call through
the fake gateway, once inside and once outside the Working hours of the Team, advanced with the development clock, one
without an Account link of his; as `test/e2e/dashboard_pages_test.go` sets it up) and those of C-40.AC-5 (the titles).

1. Sign in as the Admin, open `/`. Expect, after the chart, "Who was Paged" with the row Petr — Paged 2, Calls 2,
   Outside Working hours 2, Not reached 1, Acknowledged first 0 — compared with `curl -s "${A[@]}"
   "$API/page-statistics?…" | jq '.items[0]'`. Acknowledge one of the two Alert Groups as Petr through the API and press
   "Refresh": Acknowledged first 1. Press the header "Calls": the order flips; press again: back.
2. Read the hint: expect the sentence about the Pages made before the moment of `outside_working_hours_since`.
3. Expect "Noisy alerts": `KubePodCrashLooping` 3, 2, 2 first, then `NodeDown`, then `DiskFull`, no `CertExpiring`,
   with the Routes of each. Press `KubePodCrashLooping`: expect `/alert-groups` searched by it, the dashboard's Team and
   a time range covering the period, showing exactly 3 Alert Groups.
4. Choose the Team "payments": both tables count only its Alert Groups and the URL carries the Team. Sign in as a
   Viewer:
   expect both tables, the same rows.
5. On an empty database: expect "Nobody was Paged in this period.", "No Escalation chain Pages anyone yet." with the
   link "Escalation chains" for the Admin and without it for a Viewer, and "No alert started more than one Alert Group
   in
   this period.". Make `page-statistics` answer `500` (Playwright route): expect that table's error and "Try again" and
   the other parts with their numbers.
6. Acknowledge an Alert Group through the API without pressing "Refresh": expect the counts of "Open now" to change
   from the live hint and the network log to show no request of the two tables.
7. At 360 px: both tables scroll inside their own regions, the page does not scroll sideways. Switch to Russian: the
   Russian texts, headers and plurals.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): add the tables of who was paged and of noisy alerts to the dashboard`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases.
- "Acknowledged first" is the count of the Alert Groups of the selection whose first acknowledgement the person made;
  a person whom no Page named is no row here, even with acknowledgements, while By people on the statistics page lists
  every person who acknowledged. The hint says so, so that the two tables are not read as one.
- The link of a title searches, it does not filter by exact title (C-40.FR-7), so the list may show Alert Groups whose
  title only contains the title; the hint says it.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-40.FR-1 | partial | the tables of the section; the rest is S-225 |
| C-40.FR-3 | partial | the Viewer's tables; the rest is S-224, S-225 |
| C-40.FR-6 | partial | the table, its sorting and hint; the numbers are S-224, the recording S-222 |
| C-40.FR-7 | partial | the table and the link; the numbers are S-223 |
| C-40.FR-8 | partial | the tables as parts; the others are S-225 |
| C-40.FR-9 | partial | the empty states of the tables; the others are S-225 |
| C-40.FR-11 | partial | the scrolling regions of the tables; the figures are S-225 |
| C-40.AC-3 | partial | the filter Team on the tables; the figures, the URL and "Open in Statistics" are S-225 |
| C-40.AC-4 | partial | the table in the browser; the numbers through the API are S-224 |
| C-40.AC-5 | partial | the table and its link in the browser; the order through the API is S-223 |
| C-40.AC-6 | partial | the Viewer's tables; the rest is S-223, S-224, S-225 |
| C-40.AC-7 | partial | the tables' empty states and errors; the figures' are S-225 |
| C-40.AC-8 | partial | 360 px for the tables; the figures are S-225 |
