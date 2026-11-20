# C-40. A dashboard on the Home page

[L2 index](../L2.md) · Release: a 0.2.x patch release after 0.2.0 · UI: yes · Depends on: C-09, C-17, C-25, C-26,
C-27, C-30, C-31, C-32

**Goal.** The Home page shows at a glance how alerting goes: how many Alert Groups started, how fast people acknowledge
and resolve them, which alerts make the most noise and how much Paging falls on each person — the load of On-calls —
for a period and the filters of the statistics page, so that a team lead sees the trend without building a report
(journal D540). It complements the statistics page of C-09, which keeps the full tables, and replaces nothing on it.

Names in the form `area.setting` refer to rows of [defaults.md](../l1/defaults.md). This capability is a draft: where
its decisions are still open, a requirement names the [open question](#open-questions) that settles it.

## Scenarios

1. On Monday morning Ivan, a team lead, signs in and lands on the Home page. Under "I am an Assignee" and "On call now"
   the dashboard shows the last 7 days: 142 Alert Groups, 18 % more than the 7 days before; a median time to
   acknowledge of 4 minutes; a median time to resolve of 37 minutes; and, open now, 3 Firing, 2 Acknowledged, 1 Snoozed
   and 1 Unclaimed.
2. Under "Noisy alerts" `KubePodCrashLooping` leads with 31 Alert Groups, 12 Reopens and 26 never acknowledged. Ivan
   follows the title to the Alert Group list, searched by that title with the dashboard's filters.
3. He chooses the Team "payments" with the period "Last 30 days": every part follows the filter, and "Who was Paged"
   shows that Petr was Paged by Mention 23 times and called 4 times, 2 Pages did not reach him, and he acknowledged 15
   Alert Groups first.
4. Ivan copies the address and sends it to a colleague, who opens the same view; "Open in Statistics" opens the
   statistics page with the same period and filters.
5. On a phone 360 CSS pixels wide the figures stack in one column, and the page does not scroll sideways.
6. On a new installation without Escalation chains the dashboard says "No Alert Groups started in this period." and
   "No Escalation chain Pages anyone yet."; a Viewer sees the same dashboard as an Admin.

## Functional requirements

### Where and for whom

- **C-40.FR-1** The Home page (C-27.FR-9, C-32.FR-18) gains a section "Dashboard" after "I am an Assignee" and "On call
  now", which keep their places, since they ask for action. The section has the period and filters of FR-2 and the
  parts of FR-4 to FR-7, each with a short hint of what it counts.
- **C-40.FR-2** The period is one of the presets of the statistics page — the last 7 days, the default, 30 days and 90
  days — or a custom range of whole days, and it selects the Alert Groups that started in it, as the statistics page
  does (C-09.FR-15, FR-30). The filters are those of the statistics page (C-09.FR-30): Route, Integration, Severity
  level, Urgent, Team with "My Teams", Reopened, labels as Matchers and "Acknowledged first by", each matched as there,
  combined, and applied to every part of the dashboard, with two exceptions: the counts of open Alert Groups (FR-4)
  take the filters but no period, and they and the links to the Alert Group list leave out "Acknowledged first by",
  which only the statistics take (C-09.FR-30). Days and hours are split in the person's time zone (C-09.FR-17). The
  period and the filters are kept in the URL, so a view can be shared as a link, and "Open in Statistics" opens the
  statistics page with the same period and filters. Whether a person's last choice is remembered is
  [open question 5](#open-questions).
- **C-40.FR-3** Everyone who reads Alert Groups (`alert-groups:read`: Admin, Responder and Viewer) sees the whole
  dashboard, the per-person table of FR-6 included, as everyone sees By people on the statistics page (C-09.FR-30,
  journal D688); no Permission is added and Teams limit nothing anyone sees (C-25.FR-1). A signed-in identity without
  `alert-groups:read` sees no dashboard. Whether the per-person table needs more than that is
  [open question 3](#open-questions).

### What it shows

- **C-40.FR-4** Figures, each as a large number with its label:
  - **Alert Groups** — the number that started in the period;
  - **Time to acknowledge** — the median, with the 95th percentile beside it, as C-09.FR-15 counts it from the start to
    the first acknowledgement, over the Alert Groups acknowledged at least once;
  - **Time to resolve** — the median, with the 95th percentile beside it, over the resolved ones;
  - **Open now** — the numbers of Firing, Acknowledged and Snoozed Alert Groups, and of Unclaimed ones (C-17), that
    match the filters now, whatever the period, each a link to the Alert Group list with that tab or the filter
    Unclaimed and the dashboard's filters (C-09.FR-13).

  The first three show their change against the period of the same length just before it, as "+18 %" or "−2 min"; a
  previous period without Alert Groups shows no change ([open question 4](#open-questions)).
- **C-40.FR-5** **Volume and times per day** — a chart of the Alert Groups per day of start, each day split into
  critical and below critical (C-26.FR-8), with the medians of time to acknowledge and time to resolve per day beside
  it; a period of one day or less is split by hour. It is drawn like the chart of the statistics page: SVG sized by
  attributes, under the app listener's Content Security Policy, with a table of its numbers as its text alternative, and
  no charting dependency is added.
- **C-40.FR-6** **Who was Paged** — the load of On-calls: a table with a row for each User whom a Page named (C-32.FR-9,
  FR-19) on an Alert Group of the selection, whenever the Page was made — the On-call of a Schedule, a chosen person or
  a person called through a Notification gateway, by a step or by a later action of their Notification rule
  (C-31.FR-3): "Paged", the Pages by Mention that named them; "Calls", the Pages by a call through a Notification
  gateway that named them, one per Page however many Alert Groups its call combined (C-30.FR-4); "Not reached", the
  Pages of both kinds that ended without reaching them (C-32.FR-10); and "Acknowledged first", the Alert Groups of the
  selection whose first acknowledgement they made (C-09.FR-27). Pages of a Team or of everyone in the chat name nobody
  and are not counted. Rows go by "Paged" plus "Calls", most first, then by name; every column sorts. Pages are kept
  with their Alert Group's summary row (`retention.alert_group_summaries`), so the table reaches as far back as the
  other parts. Whether it also counts Pages outside Working hours, or time On-call from the Schedules, is
  [open question 2](#open-questions).
- **C-40.FR-7** **Noisy alerts** — the 10 titles (C-09.FR-23) with the most Alert Groups in the period, each with: the
  number of Alert Groups; the Reopens, the sum of their Reopen counts; "Never acknowledged", those resolved without any
  acknowledgement; the median time to resolve; and the Routes they came through. A title opens the Alert Group list
  searched by that title with the dashboard's filters and a time range covering the period; the hint says that the
  search also matches Alert Groups whose title or `summary` only contains the title, and those whose lifetime overlaps
  the period (C-09.FR-13). What makes an alert noisy is [open question 1](#open-questions).

### Behaviour

- **C-40.FR-8** Each part reads its numbers separately, so one slow or failed part shows its own error with "Try again"
  and leaves the others. The figures of FR-4 to FR-7 are read when the page opens, when the period or a filter
  changes, and again every 5 minutes while the page is visible; the counts of open Alert Groups follow the hints of
  the live-updates stream (C-09.FR-25) as the list's counts do. "Refresh" reads everything again. How often the
  figures refresh is [open question 6](#open-questions).
- **C-40.FR-9** Empty states: a period without Alert Groups — "No Alert Groups started in this period."; nobody Paged —
  "Nobody was Paged in this period.", and, while no Escalation chain exists, "No Escalation chain Pages anyone yet.",
  with a link to Escalation chains for those who may change them; no title with more than one Alert Group — "No alert
  started more than one Alert Group in this period.". A figure without data shows "—", never 0 minutes.
- **C-40.FR-10** On large installations each part answers within the budget of [open question 7](#open-questions) for
  the default period and for 90 days. Every read starts from the Alert Groups that started in the period, through the
  index the statistics already use (`alert_groups_started_idx`), filters by `org_id` (lint 1) and reads summary rows,
  never the Alerts inside Alert Groups; "Who was Paged" reads the Pages of those Alert Groups and their people. The
  story measures each plan on the load-test data, adds an index where a plan needs one, and records both in
  [schema.md](../../db/schema.md). The figures are computed on request and not stored.
- **C-40.FR-11** The dashboard works from a viewport 360 CSS pixels wide without horizontal scrolling of the page: the
  figures stack, and the chart and the two tables scroll inside their own regions, as on the statistics page. Every
  text is in English and Russian, with Russian plurals.
- **C-40.FR-12** The documentation's section on statistics in Concepts → Alert Groups describes the dashboard — what
  each part counts, the period and the filters, and which Pages "Who was Paged" counts — with a screenshot of the Home
  page (C-21.FR-9).

## UI

Home page, after "I am an Assignee" and "On call now": "Dashboard", with "Period" (Last 7 days, Last 30 days, Last 90
days, Custom), the filters of the statistics page in a sheet below 640 CSS pixels, "Refresh" and "Open in Statistics";
the figures "Alert Groups", "Time to acknowledge", "Time to resolve" — "median", "95th percentile", "vs the previous
{period}" — and "Open now" with "Firing", "Acknowledged", "Snoozed" and "Unclaimed"; "Volume and times per day"; "Who
was Paged" with "Paged", "Calls", "Not reached" and "Acknowledged first"; "Noisy alerts" with "Alert Groups",
"Reopens", "Never acknowledged", "Median time to resolve" and "Routes".

## API surface

Proposed, to be specified in `api/openapi.yaml` by the backend story, each with `x-permission: alert-groups:read`, the
period `from`/`to`, `time_zone` and the filters of `getAlertGroupStatistics`:
`alert-group-statistics` gains `group_by=all` — one item for the whole selection, with totals and the per-day series
split by critical and below critical, and per hour for a period of a day or less; `alert-title-statistics` (read) —
the noisy alerts of FR-7, at most 10 items; `page-statistics` (read) — the table of FR-6, per User, complete and not
paginated, like `alert-group-actor-statistics`. The counts of open Alert Groups use `alert-group-counts` twice: once
for the statuses and once with `unclaimed=true`, since its answer has no count of Unclaimed.

## Acceptance

Checked with the fake Alertmanager, a fake gateway and the development clock, through the API and in a real browser.

- **C-40.AC-1** With 5 Alert Groups started in the last 7 days — 2 critical; 3 acknowledged after 2, 4 and 6 minutes;
  4 resolved after 10, 20, 40 and 60 minutes — and 2 in the 7 days before, the figures read 5 Alert Groups, "+150 %",
  a median time to acknowledge of 4 minutes and a median time to resolve of 30 minutes; the per-day chart shows 2
  critical and 3 below critical on their days.
- **C-40.AC-2** For the same period and filters, the count of `alert-group-statistics?group_by=all` equals the sum of
  the counts of the items of `group_by=route`, since an Alert Group has one Route, and with one Route chosen its
  totals, medians and 95th percentiles equal those of that Route's item.
- **C-40.AC-3** With the filter Team "payments", every part counts only the Alert Groups of payments, the counts of open
  Alert Groups included, and the URL carries the filter; "Open in Statistics" opens the statistics page with it; with
  "Acknowledged first by" set, the counts of open Alert Groups ignore it and the links to the list do not carry it.
- **C-40.AC-4** An Escalation chain Pages Petr importantly on two Alert Groups, his rule a Mention, a wait of 5 minutes
  and a call, advanced with the development clock; on one of them his Destination's Connection has no Account link of
  his: "Who was Paged" shows Petr with Paged 2, Calls 2 and Not reached 1; after he acknowledges one first,
  Acknowledged first 1. A step that Pages the whole Team adds no row.
- **C-40.AC-5** Three Alert Groups titled `KubePodCrashLooping`, one of them reopened twice and two resolved without an
  acknowledgement, and one titled `NodeDown`: "Noisy alerts" lists `KubePodCrashLooping` first with 3, Reopens 2 and
  Never acknowledged 2; its link opens the Alert Group list searched by `KubePodCrashLooping`, which shows those 3.
- **C-40.AC-6** A Viewer sees every part; a Personal access token without `alert-groups:read` gets `403` on each
  operation; every query of the dashboard passes `make lint-arch`.
- **C-40.AC-7** On an empty installation every part shows its empty state and no figure reads 0 minutes; with one part
  answering `500`, that part shows its error and "Try again", and the others their numbers.
- **C-40.AC-8** In a real browser 360 CSS pixels wide the Home page with the dashboard has no horizontal scrolling of
  the page, in English and in Russian.
- **C-40.AC-9** On the load-test data, each operation answers within the budget of open question 7 for 7 and for 90
  days, and its plan starts from `alert_groups_started_idx`.

## Open questions

Each is for the maintainer to settle before the story that depends on it; the recommendation is the agent's.

1. **What makes an alert noisy?** Recommendation: rank titles by their number of Alert Groups in the period, ties by
   Reopens, and show "Never acknowledged" beside them, from the summary rows, which are kept for two years; do not count
   Alerts inside Alert Groups, which are pruned after `retention.alert_details` and are expensive to read.
2. **Pages outside Working hours, and time On-call.** A count of the Pages that came outside Working hours is what
   "load" usually means, but [L2.md §4](../L2.md#4-out-of-scope-for-l2) leaves counts at night or outside Working hours
   out of the statistics by people (journal D688), and computing it later from today's Working hours would change past
   counts whenever they change. Recommendation: add the column, record on each Page when it is made whether it came
   outside the Working hours of its chain's owning Team, else the Organization's (C-32.FR-3) — a new column of `pages`
   in an expand step —, and amend L2.md §4 in the same pull request. Leave out time On-call from the Schedules: the
   Result of a past period is computed from today's Rotation layers and Overrides (C-27), so it would rewrite history
   whenever a Schedule changes.
3. **Who sees the per-person table?** Recommendation: everyone with `alert-groups:read`, as By people (journal D688,
   C-09.FR-30), with no new Permission; Teams give no access (C-25.FR-1).
4. **Comparison with the previous period.** Recommendation: yes, for the three figures of the period, with no colour
   that judges a change as good or bad, since more Alert Groups may mean better coverage.
5. **Is the choice of period and filters remembered?** Recommendation: no — the URL keeps it, and the Home page opens
   on the last 7 days without filters, like the statistics page.
6. **Refresh.** Recommendation: on open, on a change, every 5 minutes while visible and by "Refresh"; only the counts of
   open Alert Groups follow live updates, so that a Storm does not re-run the aggregations on every hint.
7. **Performance budget.** Recommendation: each operation answers within 1 second at the 95th percentile for 7 days and
   within 3 seconds for 90 days on the load-test data extended to 500,000 summary rows and 50,000 Pages, as a
   provisional value to be confirmed in the test environment.
8. **An exact-title filter for the list.** The link of FR-7 uses the list's search, which also matches parts of words
   and the `summary`. Recommendation: keep the search and its hint; add an exact `title` filter to the list only if the
   mismatch confuses people in use.

## Related ADRs

ADR-0006, ADR-0009, ADR-0014, ADR-0018.

## Depends on

C-09 — statistics, the first acknowledgement and the filters; C-17 — Unclaimed; C-25 — Teams; C-26 — critical and
below critical; C-27 — the Home page and "On call now"; C-30, C-31 — calls and Notification rules; C-32 — Pages and
"I am an Assignee".

## Suggested story split

- **BE** — `group_by=all` with the critical split and hours, `alert-title-statistics`, `page-statistics`, the
  measurements on the load-test data and any index they call for.
- **FE** — the dashboard on the Home page with its figures, chart, tables, empty states and phone layout.
- **docs** — the dashboard in the section on statistics, with the screenshot.
