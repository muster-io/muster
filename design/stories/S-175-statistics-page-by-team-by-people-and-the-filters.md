---
id: S-175
title: The views By Team and By people and the filters of every view on the statistics page (FE)
capability: C-25
kind: fe
layer: L2
depends_on: [S-174, S-120]
covers: [C-09.FR-15, C-09.FR-28, C-09.FR-29, C-09.FR-30, C-09.AC-27, C-09.AC-28, C-09.AC-31, C-09.AC-32, C-09.AC-33]
files_touched:
  - web/src/routes/statistics.tsx
  - web/src/lib/statistics-search.ts
  - web/src/lib/statistics-search.test.ts
  - web/src/components/statistics-filters.tsx
  - web/src/components/statistics-filters.test.tsx
  - web/src/components/statistics-table.tsx
  - web/src/components/statistics-table.test.tsx
  - web/src/components/statistics-people-table.tsx
  - web/src/components/statistics-people-table.test.tsx
  - web/src/components/alert-group-filters.tsx
  - web/src/components/team-filter.tsx
  - web/src/components/owner-filter.tsx
  - web/src/test/fixtures/statistics.ts
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/statistics.spec.ts
  - web/e2e/support.ts
acceptance:
  - "[C-09.FR-15, C-09.FR-28, C-09.FR-29] \"Show\" offers \"By route\", \"By integration\", \"By team\" and \"By people\" («По маршрутам», «По интеграциям», «По командам», «По людям»); By route and By integration look as before (`statistics-table.test.tsx`, `statistics.spec.ts`)."
  - "[C-09.FR-28, C-09.AC-28] By people is the table \"Statistics by people\" with the columns \"Name\", \"Acknowledged first\", \"Resolved\" and \"Time to acknowledge\" with its median and 95th percentile; a Service account carries the mark \"Service account\" («Сервисный аккаунт»), a deleted one \"(deactivated)\"; with the data of C-09.AC-28 it reads alice 3 · 0 · 4 min · 27 min, bob 1 · 2 · 10 min · 10 min, terraform Service account 0 · 1 · — · —, first by \"Acknowledged first\" descending; pressing \"Resolved\" sorts bob, terraform, alice, pressing it again reverses the order, and every column sorts with `aria-sort` on its header (`statistics-people-table.test.tsx`, `statistics.spec.ts`)."
  - "[C-09.FR-28] The row \"Unknown\" («Неизвестно») is always last, whatever the sort, with the hint \"Acknowledged before Muster recorded who acknowledged first.\" and \"—\" under \"Resolved\"; with no row at all the view says \"Nobody acknowledged or resolved an Alert Group in this period.\" (`statistics-people-table.test.tsx`)."
  - "[C-09.FR-29, C-09.AC-31] By team has the table \"Statistics by team\" with the values and days of By route; the name of a Team is a link that opens By people with that Team in the filter \"Team\", the period and the other filters kept, and the button \"Per day\" («По дням») of the row shows its days; with the data of C-09.AC-31 and the filter \"Labels\" `cluster=~\"t[1-3]\"` the link of \"search\" leads to By people whose rows are the people of the Alert Group the two Teams share (`statistics-table.test.tsx`, `statistics.spec.ts`)."
  - "[C-09.FR-30, C-09.AC-32] The filters \"Routes\", \"Integrations\", \"Severity level\", \"Urgent\", \"Team\" with \"My Teams\", \"Reopened\", \"Labels\" and \"Acknowledged first by\" (\"Me\" and the Users of the user directory) apply to whichever view is shown and stay when the view changes; with the data of C-09.AC-32, critical and `namespace=\"payments\"` show 2 Alert Groups in By route, By integration and By team and alice 1 and bob 1 in By people, and \"Acknowledged first by\" \"Me\" as alice leaves 1; the page has no status tabs and no search box (`statistics-filters.test.tsx`, `statistics.spec.ts`)."
  - "[C-09.FR-30, C-09.AC-32] The view, the period, the filters and the sorting of By people are in the URL, with the parameter names of the Alert Group list and of the API (`by`, `period`, `from`, `to`, `route`, `integration`, `severity`, `urgent`, `team`, `reopened`, `label`, `first_acknowledged_by`, `sort`); the URL opened in a new browser context shows the same view, values and filters (`statistics-search.test.ts`, `statistics.spec.ts`)."
  - "[C-09.AC-27, C-09.AC-33] Signed in as a Viewer, By team and By people show what the Admin sees; after the Takeover, Unacknowledge and Reopen of C-09.AC-27 By people still gives alice 1 under \"Acknowledged first\" with 5 min, and has no row for bob or carol (`statistics.spec.ts`)."
  - "At 1280 × 800 and at 360 × 740, in English and in Russian, every view with the filters open has no horizontal scrolling of the page (`statistics.spec.ts`, as today); every new text exists in English and Russian."
verify: "make ci e2e"
operator_attention: false
issue: null
---

# S-175. The views By Team and By people and the filters of every view on the statistics page (FE)

## Scope

**IN**

- The views By team and By people of the statistics page (C-09.FR-28, C-09.FR-29), with the sorting of By people and
  the row "Unknown".
- The filters of every view (C-09.FR-30), with "Acknowledged first by", in the URL.
- The link from a Team of By team to By people filtered by that Team.

**OUT**

- The API, the first acknowledgement and its backfill: S-174.
- The documentation's section on statistics and its screenshots: S-176.
- Out of scope for good (journal D688): exporting as CSV, a split by day of By people, acknowledgements at night or
  outside Working hours, statistics limited by Team.

## Contracts

- **API used**: `getAlertGroupStatistics` (`group_by` `route`, `integration` or `team`, and the filters),
  `getAlertGroupActorStatistics`, `listRoutes`, `listIntegrations`, `listTeams` (the choices of the filters, as on the
  Alert Group list), `listUserDirectory` (the Users of "Acknowledged first by"), `getMe` (who "Me" is).
- **URL** (`lib/statistics-search.ts`, the Zod schema of the route's search and its mapping to the two requests):
  `by` (`route` by default, `integration`, `team`, `people`), `period`, `from`, `to` as today; the list's `route`,
  `integration`, `severity`, `urgent`, `team`, `reopened` and `label`; `first_acknowledged_by` (a `public_id` or
  `me`); and `sort` of By people, `acknowledged_first`, `resolved`, `ack_median`, `ack_p95` or `name`, with `-` for
  descending (`-acknowledged_first` by default, left out of the URL). A value the schema cannot read is dropped, as on
  the list. The chosen Routes and Integrations are no longer kept only for their own view: they are filters of every
  view (C-09.FR-15, revised).
- **Filters** (`statistics-filters.tsx`): the pickers of the Alert Group list reused from `alert-group-filters.tsx`
  (Routes, Integrations, Severity level, Urgent, Reopened), `team-filter.tsx` ("Team" with "All", "My Teams" and each
  Team), the Matcher input of the list (`label-matchers-input.tsx`), and "Acknowledged first by" built on the User
  picker of `owner-filter.tsx` with "Anyone", "Me" and the Users; it offers no Service accounts, since the field names a
  User only (C-09.FR-27, C-09.FR-30). The list's other filters — resolved by, Owner, Assignee, snoozed with no end,
  Delivery problem and Unclaimed — are not offered. At phone width the filters move into a panel, as on the list.
- **By team** (`statistics-table.tsx`): the table of By route with the Teams, captioned "Statistics by team", the
  column "Team", the empty text "There are no teams to show."; the name is a link (`/statistics?by=people&team=<id>`
  with the current period and filters) and the days open with "Per day", since the name no longer opens them.
- **By people** (`statistics-people-table.tsx`): the table "Statistics by people" with the sortable headers and the
  row "Unknown" of C-09.FR-28; durations as the other views show them; the Service account mark and "(deactivated)".
- **Texts** (EN / RU): "By team" / «По командам», "By people" / «По людям», "Statistics by team" / «Статистика по
  командам», "Statistics by people" / «Статистика по людям», "Team" / «Команда», "Name" / «Имя», "Acknowledged
  first" / «Подтверждено первым», "Resolved" / «Закрыто», "Service account" / «Сервисный аккаунт», "Unknown" /
  «Неизвестно», "Acknowledged before Muster recorded who acknowledged first." / «Подтверждено до того, как Muster
  начал записывать, кто подтвердил первым.», "Nobody acknowledged or resolved an Alert Group in this period." / «За
  этот период никто не подтверждал и не закрывал группы алертов.», "There are no teams to show." / «Нет команд для
  показа.», "Per day" / «По дням», "Acknowledged first by" / «Первым подтвердил», "Anyone" / «Кто угодно», "Me" /
  «Я»; the filter names as on the Alert Group list. The page's hint adds "The filters apply to every view." / «Фильтры
  действуют на все виды.».
- **Documentation link**: "Guide" next to the title stays `page.statistics` (`concepts/alert-groups.md#statistics`);
  no new entry.

## Steps

1. Write `lib/statistics-search.ts` and move the page onto it. Check: `statistics-search.test.ts` covers every
   parameter, the defaults and a URL from before this story (`by=route&route=…`), which still opens the same view.
2. Write the filters. Check: `statistics-filters.test.tsx`; `make lint`.
3. Write By team and By people. Check: `statistics-table.test.tsx`, `statistics-people-table.test.tsx` with the
   builders of `web/src/test/fixtures/statistics.ts`.
4. Write the end-to-end steps. Check: the Verification below; `make e2e`.

## Verification

Run `make dev`, sign in as `admin@example.org` / `muster-dev-password`; from a terminal, make the data of S-174's
Verification — the Users alice, bob and carol and the Service account terraform, the Alert Groups of C-09.AC-27,
C-09.AC-28, C-09.AC-31 and C-09.AC-32 with the development clock, leaving out the `psql` step of C-09.AC-30. Then in
Playwright at 1280 × 800, in English, and again in Russian:

1. Statistics → "By people" → "Labels" `cluster=~"s[1-4]"` → the table "Statistics by people": alice 3, 0, 4 min,
   27 min; bob 1, 2, 10 min, 10 min; terraform with "Service account", 0, 1, —, —. With "Labels" `cluster="s0"` the
   Alert Group of C-09.AC-27 counts for alice and nobody else.
2. Press "Resolved" → bob, terraform, alice; press again → alice, terraform, bob; the URL carries `sort=-resolved`,
   then `sort=resolved`.
3. "By team" with "Labels" `cluster=~"t[1-3]"` → "payments" 2 and "search" 1 for the Alert Groups of C-09.AC-31; "Per
   day" of "payments" shows its day; the link "search" → "By people" with "Team" "search" and the rows of the shared
   Alert Group only.
4. Filters "Severity level" critical and "Labels" `namespace="payments"` → 2 in "By route", "By integration" and
   "By team"; "By people" → alice 1 and bob 1.
5. Sign in as alice → the same URL with "Acknowledged first by" "Me" → 1 Alert Group in every view.
6. Copy the URL, open it in a new browser context as the Viewer → the same view, period, filters and values.
7. At 360 × 740, each view with the filter panel open passes `expectNoHorizontalScroll`.

`make e2e` runs these steps in `statistics.spec.ts`.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): show statistics by team and by people with the filters of the alert group list`.
- "By team" follows the case of "By route" and "By integration"; the PRD names the view By Team.
- The name of a Route or an Integration still opens its days, as today; only a Team's name links to By people, as
  C-09.FR-29 asks, so its days have the button "Per day".

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-09.FR-15 | partial | revised for L2: the views and filters on the page; the API is S-174 |
| C-09.FR-28 | partial | the view; the API is S-174 |
| C-09.FR-29 | partial | the view and the link to By people; the API is S-174 |
| C-09.FR-30 | partial | the filters on the page and the URL; the API is S-174 |
| C-09.AC-27 | partial | the page; the row is S-174 |
| C-09.AC-28 | partial | the table and its sorting; the values are S-174 |
| C-09.AC-31 | partial | the link to By people; the values are S-174 |
| C-09.AC-32 | partial | the page and the URL in another browser; the values are S-174 |
| C-09.AC-33 | partial | the page as a Viewer; the API is S-174 |
