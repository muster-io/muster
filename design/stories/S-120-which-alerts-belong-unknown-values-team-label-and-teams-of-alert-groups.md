---
id: S-120
title: "\"Which Alerts belong\" on the Team page, unknown values with \"Create Team\", the Team label setting, and the Teams of Alert Groups and Alerts on their pages (FE)"
capability: C-25
kind: fe
layer: L2
depends_on: [S-118, S-163, S-119]
covers: [C-25.FR-2, C-25.FR-5, C-25.FR-6, C-25.FR-7, C-25.AC-10, C-25.AC-11, C-06.FR-19, C-09.FR-11, C-09.FR-13, C-09.FR-14, C-20.FR-1]
files_touched:
  - web/src/routes/teams.index.tsx
  - web/src/routes/teams.new.tsx
  - web/src/routes/teams.$teamId.tsx
  - web/src/components/team-alert-rules.tsx
  - web/src/components/team-alert-rules.test.tsx
  - web/src/components/team-label-values.tsx
  - web/src/components/team-label-values.test.tsx
  - web/src/routes/admin.organization.general.tsx
  - web/src/components/alert-group-header.tsx
  - web/src/components/alert-group-header.test.tsx
  - web/src/components/alert-group-table.tsx
  - web/src/components/alert-group-filters.tsx
  - web/src/components/alert-group-filters.test.tsx
  - web/src/lib/alert-group-search.ts
  - web/src/components/alert-group-alerts.tsx
  - web/src/components/timeline.tsx
  - web/src/components/timeline.test.tsx
  - web/src/components/integration-alerts.tsx
  - web/src/components/integration-alerts.test.tsx
  - web/src/components/audit-diff.tsx
  - web/src/lib/docs-links.json
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/teams.spec.ts
  - web/e2e/alert-group-list.spec.ts
  - web/e2e/alert-group-page.spec.ts
  - web/e2e/integration-page.spec.ts
  - web/e2e/phone-width.spec.ts
  - web/e2e/organization-settings.spec.ts
acceptance:
  - "[C-25.FR-7, C-25.AC-10] Above the Teams list, the Team label with the hint \"An Alert belongs to a Team by the value of the Team label or by the Team's matchers. Teams do not limit access: Roles are the same for the whole Organization.\" and a link to change it in Organization → General; the section \"Unknown values of team\" (named after the Team label) lists `checkout` and `Payments` with when each was last seen and \"Create Team\"; \"Create Team\" next to `checkout` opens the new Team form with the slug `checkout`, and next to `Payments` with the slug empty and the note that a Team gets these Alerts only through a Team matcher such as `team=\"Payments\"`, in English and in Russian (`team-label-values.test.tsx`, `teams.spec.ts`)."
  - "[C-25.FR-5] The Team page's \"Which Alerts belong to this Team\" shows the Team label with the Team's slug as its value, and the Team matchers with the matcher input of Routes, the hints \"A matcher may look at any label of the Alert, not only `namespace`. Any one matcher is enough.\" and \"On test clusters with a shared namespace, match by `pod` or `app`, or put the team label on the pods\", and an example of how an Alert Group shows it; both are kept, with no switch between them (`team-alert-rules.test.tsx`)."
  - "[C-25.FR-5, C-20.FR-1] Organization → General has \"Team label\" (`team` by default); a label that an Internal alert sets is refused with the server's message at the field (`organization-settings.spec.ts`)."
  - "[C-25.FR-6, C-09.FR-11, C-09.FR-14] The Alert Group page's header shows \"Teams: payments — by the Team label\", or \"— by the matcher {matcher}\", a deleted Team marked as deleted; the Timeline shows `team_joined` and `unknown_team_value` with the Team and its rule or the value; the Alerts of the Alert Group show the Teams of each (`alert-group-header.test.tsx`, `timeline.test.tsx`, `alert-group-page.spec.ts`)."
  - "[C-25.FR-2, C-25.FR-6, C-25.AC-11, C-09.FR-13] The Alert Group list has the column \"Teams\", the filter \"Team\" and the quick filter \"My Teams\", which keep their values in the URL; with \"My Teams\" a User in \"payments\" and not in \"search\" sees the Alert Groups of \"payments\" and not those of \"search\" (`alert-group-filters.test.tsx`, `alert-group-list.spec.ts`)."
  - "[C-06.FR-19] The Alerts view of an Integration shows the Teams of each Alert with their rule (`integration-alerts.test.tsx`, `integration-page.spec.ts`)."
  - "At 360 CSS pixels the Alert Group list with \"Teams\" and the Alert Group page with its Teams do not scroll horizontally, in either language (`phone-width.spec.ts`); every new text exists in English and Russian."
verify: "make ci e2e"
operator_attention: false
issue: null
---

# S-120. "Which Alerts belong" on the Team page, unknown values with "Create Team", the Team label setting, and the Teams of Alert Groups and Alerts on their pages (FE)

## Scope

**IN**

- The Team label and its hint above the Teams list, the section "Unknown values of {team label}" with "Create Team"
  (C-25.FR-7, reference.md's banner).
- "Which Alerts belong to this Team" on the Team page: the Team label value and the Team matchers (C-25.FR-5).
- The Team label on Organization → General (C-20.FR-1, revised).
- The Teams of an Alert Group in the header of its page, in its Timeline and its Alerts, as a column, a filter and the
  quick filter "My Teams" of the list; the Teams of each Alert in the Alerts view (C-25.FR-6, C-06.FR-19, C-09.FR-11,
  C-09.FR-13, revised).

**OUT**

- The API: S-118, S-163. The Teams pages themselves: S-119.
- The Destinations of the Team and the delivery state by entry: S-123. "How to mention the Team": S-143.
- The field "Teams" of the Root message: S-125.

## Contracts

- **API used**: `getOrganization` and `updateOrganization` (`team_label`), `listTeamLabelValues`, `updateTeam`
  (`matchers`), `getAlertGroup`, `listAlertGroups` and `getAlertGroupCounts` (`teams`; the parameter `team` with a
  `public_id` or `mine`), `listAlertGroupAlerts` (`teams`), `getAlertGroupTimeline` (`team_joined` with `team`,
  `team_rule`, `team_matcher`; `unknown_team_value` with `team_value`), `listIntegrationAlerts` (`teams`), `listTeams`
  (the filter's choices).
- **Teams list** (`teams.index.tsx`, `team-label-values.tsx`): the Team label line with the hint and the link
  "Change in the Organization settings" to `/admin/organization/general`; the section with the banner text of
  reference.md, each value with its relative and absolute time last seen, and "Create Team" to `/teams/new?slug=<value>`
  for a valid slug or `/teams/new?value=<value>` for another, which shows the note and an empty slug.
- **Team page** (`team-alert-rules.tsx`): the Team label with the slug read-only (changed with the slug), the matchers
  with `label-matchers-input.tsx` and the two hints, and the example line "Teams: {slug} — by the Team label". The
  approved mockup shows the label and the matchers as one switch; L2.md §5.2 keeps both, so the page does too.
- **General** (`admin.organization.general.tsx`): "Team label" under the time zone; `reserved_label` shows the server's
  message at the field.
- **Alert Group page** (`alert-group-header.tsx`, `alert-group-alerts.tsx`, `timeline.tsx`): the header line "Teams:
  {team} — by the Team label" or "— by the matcher {matcher}", one per Team, a deleted Team in its name with "(deleted)";
  the Timeline texts "{team} joined — by the Team label" / "— by the matcher {matcher}" and "No Team has the value
  {value} of the Team label"; each Alert's Teams.
- **Alert Group list** (`alert-group-table.tsx`, `alert-group-filters.tsx`, `lib/alert-group-search.ts`): the column
  "Teams" (hidden first at phone width, as the other optional columns are), the filter "Team" and the quick filter
  "My Teams", in the URL as `team`.
- **Alerts view** (`integration-alerts.tsx`): the column "Teams".
- **Audit log** (`audit-diff.tsx`): `/team_label` "Team label", `/matchers` of a Team "Team matchers".
- **Documentation links**: `field.team_label` → `concepts/teams.md#which-alerts-belong-to-a-team`,
  `fix.unknown_team_values` → `concepts/teams.md#values-without-a-team`; the anchors exist from S-118.
- **Texts** (EN / RU), from C-25's UI, reference.md and the approved mockups: the hint above / «Алерт относится к
  команде по значению лейбла команды или по её матчерам. Доступ команды не ограничивают: роли общие для Организации.»,
  "Unknown values of {{label}}" / «Неизвестные значения {{label}}», the banner / «Алерты приходили с этими значениями,
  но такой команды нет: места доставки команды не добавляются, никого не зовут.», "Create Team" / «Создать команду», "Which
  Alerts belong to this Team" / «Какие алерты — этой команды», the two hints / «Матчер может смотреть на любой лейбл
  алерта, не только на namespace. Подходит любой из матчеров.» and «На стендах с общим namespace матчите по pod / app
  или ставьте метку команды на поды.», "Teams" / «Команды», "My Teams" / «Мои команды», "Team label" / «Лейбл команды».

## Steps

1. Write the Teams list header, the values and the prefilled form. Check: `team-label-values.test.tsx`.
2. Write "Which Alerts belong to this Team" and the Team label on General. Check: `team-alert-rules.test.tsx`;
   `make lint`.
3. Show the Teams on the Alert Group page, its Timeline and Alerts, the list and the Alerts view. Check:
   `alert-group-header.test.tsx`, `timeline.test.tsx`, `alert-group-filters.test.tsx`, `integration-alerts.test.tsx`.
4. Write the end-to-end specs. Check: the Verification below; `make e2e`.

## Verification

Run `make dev`, sign in as `admin@example.org` / `muster-dev-password`; from a terminal, as in S-118, create the Teams
"payments" (with bob in it) and "search", and fire Alerts with `team="payments"`, `team="search"`, `team="checkout"`
and `team="Payments"`. Then in Playwright at 1280 × 800 and at 360 × 740, in English and in Russian:

1. Teams → the Team label "team" with the hint; "Unknown values of team" lists `checkout` and `Payments` with their
   times.
2. "Create Team" next to `checkout` → the form has the slug `checkout` → "Save" → back on Alert Groups, the Alert
   Group of `team="checkout"` lists "checkout" in its Teams within a few seconds.
3. "Create Team" next to `Payments` → the slug is empty and the note about a Team matcher such as `team="Payments"` is
   shown.
4. Teams → "payments" → "Which Alerts belong to this Team" → add the matcher `namespace=~"checkout|billing"` → "Save".
5. Alert Groups → the Alert Group of "payments": the header reads "Teams: payments — by the Team label"; its Timeline
   shows "payments joined — by the Team label".
6. Alert Groups → "My Teams" as bob → only the Alert Groups of "payments"; the URL carries `team=mine`.
7. Integrations → the Integration → Alerts: the column "Teams".
8. Organization → General → "Team label" `team_id` → "Save" → the server's message at the field.
9. At 360 × 740 the list with "My Teams" and the Alert Group page pass `expectNoHorizontalScroll`.

`make e2e` runs these steps in `teams.spec.ts`, `alert-group-list.spec.ts`, `alert-group-page.spec.ts`,
`integration-page.spec.ts`, `phone-width.spec.ts` and `organization-settings.spec.ts`.

## Open questions

None; the conflict between the mockup's switch and the PRD is settled by L2.md §5.2.

## Notes

- Suggested commit: `feat(web): show the teams of alerts and alert groups and the unknown team label values`.
- The section's name follows the Team label: with the label `service_team` it reads "Unknown values of service_team".

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-25.FR-2 | partial | the Alert Group list's filters |
| C-25.FR-5 | partial | the Team page and the General page; the rule is S-118 |
| C-25.FR-6 | partial | the page, the list and the Timeline; the Root message is S-125 |
| C-25.FR-7 | partial | the Teams page; the API is S-118 and S-163 |
| C-25.AC-10 | partial | unknown values and "Create Team"; the people's sources are S-119, the Team mention fields S-143 |
| C-25.AC-11 | partial | the Alert Group list; Routes and Destinations are S-119 |
| C-06.FR-19 | partial | revised for L2: the Teams column of the Alerts view |
| C-09.FR-11 | partial | revised for L2: the Timeline texts of the Team events |
| C-09.FR-14 | partial | revised for L2: the Teams with their rule in the page's header; the delivery state by entry is S-123, the Assignees and running chains S-159 |
| C-09.FR-13 | partial | revised for L2: the Teams column and filter of the list |
| C-20.FR-1 | partial | revised for L2: the Team label on General |
