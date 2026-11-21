---
id: S-225
title: The Dashboard section of the Home page with the period, the filters, the figures and the chart per day (FE)
capability: C-40
kind: fe
layer: L2
depends_on: [S-223, S-175, S-159, S-136, S-030]
covers: [C-40.FR-1, C-40.FR-2, C-40.FR-3, C-40.FR-4, C-40.FR-5, C-40.FR-8, C-40.FR-9, C-40.FR-11, C-40.AC-1, C-40.AC-3, C-40.AC-6, C-40.AC-7, C-40.AC-8]
files_touched:
  - web/src/routes/index.tsx
  - web/src/components/home-dashboard.tsx
  - web/src/components/home-dashboard.test.tsx
  - web/src/components/dashboard-part.tsx
  - web/src/components/dashboard-part.test.tsx
  - web/src/components/dashboard-figures.tsx
  - web/src/components/dashboard-figures.test.tsx
  - web/src/components/dashboard-chart.tsx
  - web/src/components/dashboard-chart.test.tsx
  - web/src/components/statistics-filters.tsx
  - web/src/components/statistics-filters.test.tsx
  - web/src/lib/dashboard.ts
  - web/src/lib/dashboard.test.ts
  - web/src/lib/statistics-search.ts
  - web/src/lib/statistics-search.test.ts
  - web/src/test/fixtures/dashboard.ts
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/home-dashboard.spec.ts
  - web/e2e/home.spec.ts
  - web/e2e/phone-width.spec.ts
acceptance:
  - "[C-40.FR-1, C-40.FR-3] The Home page has the section \"Dashboard\" after \"I am an Assignee\" and \"On call now\", which keep their places and are not moved or hidden by it; a User with `alert-groups:read` — an Admin, a Responder and a Viewer alike — sees it, one without sees no dashboard; its header has \"Period\", the filters, \"Refresh\" and \"Open in Statistics\" (`home-dashboard.test.tsx`, `home-dashboard.spec.ts`)."
  - "[C-40.FR-2, C-40.AC-3] \"Period\" offers Last 7 days, the default, Last 30 days, Last 90 days and Custom, a range of whole days; the filters are those of the statistics page — Route, Integration, Severity level, Urgent, Team with \"My Teams\", Reopened, labels as Matchers and \"Acknowledged first by\" — in the same component, in a sheet below 640 CSS pixels; the period and the filters are kept in the URL of the Home page, and are not remembered otherwise: opening `/` again shows the last 7 days without filters; \"Open in Statistics\" opens `/statistics` with the same period and filters (`dashboard.test.ts`, `statistics-filters.test.tsx`, `home-dashboard.spec.ts`)."
  - "[C-40.FR-4, C-40.AC-1] The figures \"Alert Groups\", \"Time to acknowledge\" and \"Time to resolve\" — each with \"median\" and \"95th percentile\" for the times — read `getAlertGroupStatistics?group_by=all`, and show their change \"vs the previous 7 days\" against the period of the same length just before, as \"+150 %\" or \"−2 min\", in a neutral colour; a previous period without Alert Groups shows no change; with the data of C-40.AC-1 the page reads 5, \"+150 %\", 4 minutes and 30 minutes (`dashboard-figures.test.tsx`, `dashboard.test.ts`, `home-dashboard.spec.ts`)."
  - "[C-40.FR-4, C-40.AC-3] \"Open now\" shows Firing, Acknowledged, Snoozed and Unclaimed, from `getAlertGroupCounts` called twice — once for the statuses, once with `unclaimed=true` — with the dashboard's filters, no period and not \"Acknowledged first by\", each a link to the Alert Group list with that tab or the filter Unclaimed and the same filters (`dashboard-figures.test.tsx`, `home-dashboard.spec.ts`)."
  - "[C-40.FR-5, C-40.AC-1] \"Volume and times per day\" is a chart of the Alert Groups per day of start with each day split into critical and below critical, the median time to acknowledge and the median time to resolve of each day beside it, and per hour for a period of one day or less; it is drawn as SVG sized by attributes with no style attribute and no style element under the app listener's Content Security Policy, with a table of its numbers as its text alternative, and no charting dependency is added (`dashboard-chart.test.tsx`, `home-dashboard.spec.ts`, `watchCsp`)."
  - "[C-40.FR-8, C-40.AC-7] Each part reads its numbers separately: a part that answers `500` shows its own error with \"Try again\" and the others show their numbers; the parts are read when the page opens, when the period or a filter changes, again every `dashboard.refresh_interval` (5 minutes) while the page is visible, and on \"Refresh\"; a live-update hint of the stream (C-09.FR-25) reads the counts of open Alert Groups again and none of the other parts (`dashboard-part.test.tsx`, `home-dashboard.spec.ts`)."
  - "[C-40.FR-9, C-40.AC-7] On an empty installation the figures say \"No Alert Groups started in this period.\" and a figure without data shows \"—\", never 0 minutes; every figure and the chart has a short hint of what it counts (`dashboard-figures.test.tsx`, `home-dashboard.spec.ts`)."
  - "[C-40.FR-11, C-40.AC-8] At 360 CSS pixels wide the figures stack in one column, the chart scrolls inside its own region and the Home page does not scroll sideways, in English and in Russian; every new text exists in English and Russian, with Russian plurals, and a missing key fails `make ci` (`phone-width.spec.ts`, `home-dashboard.spec.ts`, the missing-key check of `make ci`)."
verify: "make ci e2e-web"
operator_attention: false
issue: null
---

# S-225. The Dashboard section of the Home page with the period, the filters, the figures and the chart per day (FE)

## Scope

**IN**

- The section "Dashboard" on the Home page: the period, the filters of the statistics page, "Refresh", "Open in
  Statistics", the URL, and the parts "Alert Groups", "Time to acknowledge", "Time to resolve", "Open now" and "Volume
  and times per day" (C-40.FR-1 to FR-5).
- The behaviour shared by every part: one request per part, its own error and "Try again", the refresh every
  `dashboard.refresh_interval`, the hints of the live stream for the counts of open Alert Groups, the empty states of
  these parts and the phone layout (C-40.FR-8, FR-9, FR-11).

**OUT**

- The tables "Who was Paged" and "Noisy alerts" with their empty states: S-226, which mounts into the section this story
  leaves with a place for them.
- The API: S-222 to S-224. The documentation and its link: S-227.

## Contracts

- **API used**: `getAlertGroupStatistics` with `group_by=all` (twice: the period and the period of the same length just
  before it), `getAlertGroupCounts` (twice, as above), `listRoutes`, `listIntegrations`, `listTeams` and the label
  Matchers' suggestions for the filters, as the statistics page reads them. The generated client carries the widened
  types of S-223 (`subject` null, `critical_count`, `per_hour`).
- **Page**: `web/src/routes/index.tsx` gets `validateSearch` with the period and the filter keys of
  `statisticsSearchSchema` (no `by`, no sort), so a view is a link; the section mounts under "On call now" for a User
  who `useCan("alert-groups:read")`. The statistics page and the Home page share one description of the period and the
  filters.
- **Components**: `home-dashboard.tsx` — the section, its header, the URL and the "Refresh"; `dashboard-part.tsx` — the
  wrapper of one part: its heading and hint, a skeleton, the error with "Try again", and its own query with the refresh
  interval, so that one failing part leaves the others (`useQuery` with `refetchInterval` from the built-in
  `dashboard.refresh_interval`, paused while the document is hidden); `dashboard-figures.tsx` — the three figures with
  their change and "Open now" with its four links; `dashboard-chart.tsx` — the SVG chart and its table, drawn like
  `statistics-chart.tsx`, with the critical and below-critical bars, and the two medians; `statistics-filters.tsx` is
  reused and, where it needs a prop (the exclusion of "Acknowledged first by" for the counts and the links), extended
  with it and its test. `web/src/lib/dashboard.ts` holds the change against the previous period, the formatting of
  "+18 %" and "−2 min" with the neutral colour, the previous period's `from`/`to`, and the search of the links to the
  Alert Group list; `statistics-search.ts` exports the pieces both pages share.
- **Hints** (`web/src/lib/live.ts`): `registerHint` for the counts' two queries with the same types the list's counts
  use, so a Storm runs no aggregation again; nothing else subscribes.
- **Texts** (EN / RU; `en.json` and `ru.json` are the real files; a missing key fails CI): "Dashboard" / «Дашборд»;
  "Period" / «Период»; "Last 7 days" / «Последние 7 дней»; "Refresh" / «Обновить»; "Open in Statistics" / «Открыть в
  статистике»; "Alert Groups" / «Группы алертов»; "Time to acknowledge" / «Время до подтверждения»; "Time to resolve" /
  «Время до решения»; "median" / «медиана»; "95th percentile" / «95-й процентиль»; "vs the previous {period}" / «к
  предыдущим {period}»; "Open now" / «Открыто сейчас»; "Firing", "Acknowledged", "Snoozed", "Unclaimed" as the list's
  words; "Volume and times per day" / «Объём и время по дням»; "No Alert Groups started in this period." / «За этот
  период не начато ни одной группы алертов.»; "Try again" / «Повторить»; the hints of each part. Russian plurals for the
  counts and the units of the changes.
- **Permissions**: `alert-groups:read`; nothing is added (C-40.FR-3).
- **Fixtures**: `web/src/test/fixtures/dashboard.ts` — builders of the statistics of the whole selection, of the counts
  and, for S-226, of titles and Pages.

## Steps

1. Write `web/src/lib/dashboard.ts` and the shared pieces of `statistics-search.ts`. Check: `dashboard.test.ts` (the
   change against the previous period, "+150 %", "−2 min", no change without a previous count),
   `statistics-search.test.ts`.
2. Write `dashboard-part.tsx` and the figures. Check: `dashboard-part.test.tsx` (error, "Try again", refresh,
   visibility), `dashboard-figures.test.tsx` (the links of "Open now", the neutral colour, "—").
3. Write the chart. Check: `dashboard-chart.test.tsx` (SVG attributes only, the table, hours for a day).
4. Write the section, the route's search and the texts. Check: `home-dashboard.test.tsx`, `statistics-filters.test.tsx`;
   `make ci` (missing keys).
5. Write `home-dashboard.spec.ts`, extend `home.spec.ts` and `phone-width.spec.ts`. Check: the Verification below.

## Verification

Playwright against `make dev` (as `web/e2e/support.ts` sets it up), in English and then Russian, with the data of
C-40.AC-1 — five Alert Groups in the last 7 days, two critical, acknowledged after 2, 4 and 6 minutes and resolved after
10, 20, 40 and 60 minutes with the development clock, and two in the 7 days before — made through the fake
Alertmanager as `test/e2e/dashboard_statistics_test.go` makes them.

1. Sign in as the Admin and open `/`. Expect "I am an Assignee" and "On call now" first, then "Dashboard" with "Last 7
   days"; the figures "Alert Groups" 5 with "+150 %" and "vs the previous 7 days", "Time to acknowledge" 4 min,
   "Time to resolve" 30 min, each with its 95th percentile, and "Open now" with its four numbers; compare each with
   `curl -s "${A[@]}" "$API/alert-group-statistics?group_by=all&…"` and `…/alert-group-counts`.
2. Expect "Volume and times per day" with the bars of two critical and three below critical on their days, and the table
   of its numbers as the chart's text alternative; no Content Security Policy violation (`watchCsp`).
3. Choose the Team "payments" and "Last 30 days": expect every figure to change, the URL to carry the period and the
   Team, and "Open in Statistics" to open `/statistics` with the same period and Team; set "Acknowledged first by" to
   yourself: expect "Open now" to keep its numbers, and its links to carry no `first_acknowledged_by`. Open `/` again
   without the query: the last 7 days, no filters.
4. Press "Open now" → Firing: expect the Alert Group list on the Firing tab with the dashboard's filters.
5. Make `getAlertGroupStatistics` fail for one part (Playwright routes the request to `500`): expect that part's error
   with "Try again" and the other parts with their numbers; press "Try again" with the route removed: the part returns.
   Press "Refresh": expect each part to read once. Send a live-update hint (acknowledge an Alert Group through the API):
   expect the counts of "Open now" to change within a moment and the network log to show no other statistics request.
6. On an empty database (`make dev-db` fresh): expect "No Alert Groups started in this period." and "—" in the times,
   never "0 min". Sign in as a Viewer: expect the same dashboard; as a token without `alert-groups:read` through the
   API: no dashboard section is shown.
7. At 360 px: the figures in one column, the chart scrolls inside its region, no horizontal scrolling of the page; the
   filters open in a sheet. Switch to Russian: the Russian texts and plurals.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): add the dashboard section with the figures and the chart per day to the home page`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases.
- Until S-226 merges the section has the figures and the chart only; the tables follow in the same place.
- The neutral colour of a change is deliberate (C-40.FR-4): more Alert Groups may mean better coverage, so the page
  never paints a change as good or bad.
- No charting dependency: the chart is drawn like the statistics page's, in SVG sized by attributes, because the Content
  Security Policy of the app listener is `style-src 'self'` and a library would need the licence statement of ADR-0001.
- The design pass of the UI belongs to the later layers (journal D315); this section uses the existing components.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-40.FR-1 | partial | the section, its place and the parts of FR-4 and FR-5; the tables are S-226 |
| C-40.FR-2 | partial | the period, the filters, the URL and "Open in Statistics"; the reads accept them in S-223, S-224 |
| C-40.FR-3 | partial | the visibility rule; the Pages' read is S-224 |
| C-40.FR-4 | partial | the figures and the counts of open Alert Groups; the totals are S-223 |
| C-40.FR-5 | partial | the chart; the series are S-223 |
| C-40.FR-8 | partial | the separate reads, the errors, the refresh and the hints for these parts; the tables join them in S-226 |
| C-40.FR-9 | partial | the empty states and "—" of these parts; those of the tables are S-226 |
| C-40.FR-11 | partial | the phone layout of these parts; the tables' scrolling regions are S-226 |
| C-40.AC-1 | partial | the figures and the chart in the browser; the numbers through the API are S-223 |
| C-40.AC-3 | partial | the filter Team on the figures, the URL, "Open in Statistics", "Acknowledged first by" and the reopened page; the tables follow in S-226 |
| C-40.AC-6 | partial | the Viewer's view of these parts; the tables are S-226 |
| C-40.AC-7 | partial | the empty installation and the failing part of these parts; the tables are S-226 |
| C-40.AC-8 | partial | 360 px for these parts; the tables are S-226 |
