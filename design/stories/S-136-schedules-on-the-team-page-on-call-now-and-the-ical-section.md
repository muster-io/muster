---
id: S-136
title: Schedules on the Team page, On call now on the Home page, the Team page and the Teams list, and the iCal section (FE)
capability: C-27
kind: fe
layer: L2
depends_on: [S-134, S-135]
covers: [C-27.FR-1, C-27.FR-8, C-27.FR-9, C-27.FR-10, C-27.AC-6, C-27.AC-9]
files_touched:
  - web/src/routes/index.tsx
  - web/src/routes/teams.index.tsx
  - web/src/routes/teams.$teamId.tsx
  - web/src/routes/schedules.$scheduleId.tsx
  - web/src/components/on-call-now.tsx
  - web/src/components/on-call-now.test.tsx
  - web/src/components/team-schedules.tsx
  - web/src/components/team-schedules.test.tsx
  - web/src/components/ical-subscription.tsx
  - web/src/components/ical-subscription.test.tsx
  - web/src/components/app-shell.tsx
  - web/src/components/app-shell.test.tsx
  - web/src/lib/docs-links.json
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/home.spec.ts
  - web/e2e/teams.spec.ts
  - web/e2e/schedules.spec.ts
  - web/e2e/navigation.spec.ts
  - web/e2e/phone-width.spec.ts
acceptance:
  - "[C-27.FR-9, C-27.AC-9] In a real browser the Home page shows \"On call now\" with \"All Teams\" and \"By the main Schedule of each Team\": devops — @ivan, \"Main\", until Monday 10:00; payments — @petr with \"unreachable in Mattermost\" and \"next: gap\"; search — \"No Schedule\"; \"My Teams\" shows only the Teams of the signed-in User, in English and in Russian (`on-call-now.test.tsx`, `home.spec.ts`)."
  - "[C-27.FR-9, C-27.FR-8, C-27.AC-6] The Team page shows \"On call now\" for its Team with the same marks, and the Teams list shows each Team's On-call now, or \"No Schedule\" (`teams.spec.ts`)."
  - "[C-27.FR-1] The Team page's \"Schedules\" lists each Schedule with \"main\", its Rotation layers, its On-call now and its warnings, \"New Schedule\" for Admins, and the hint \"The main Schedule names the Team's On-call shown under 'On call now'; Escalation steps may Page the On-call of any of them.\" (`team-schedules.test.tsx`, `teams.spec.ts`)."
  - "[C-27.FR-10] The Schedule page's \"iCal subscription\" with the hint \"A personal link for your calendar application. It is shown once; a revoked link stops working at once.\" shows after \"New link\" the link once with \"Copy the link now: it will not be shown again\", which reloading the page does not show again; \"Revoke\" ends it, and the section then offers \"New link\" only (`ical-subscription.test.tsx`, `schedules.spec.ts`)."
  - "The Home page is the first entry of the main navigation; at 360 CSS pixels it has no horizontal scrolling; every new text exists in English and Russian; \"On call now\" has \"Learn more\" to Concepts → Schedules and the iCal section to its section \"iCal feed\"; `make e2e` passes."
verify: "make ci e2e"
operator_attention: false
issue: null
---

# S-136. Schedules on the Team page, On call now on the Home page, the Team page and the Teams list, and the iCal section (FE)

## Scope

**IN**

- "On call now" (C-27.FR-9) as a section of the Home page — "All Teams" or "My Teams", "By the main Schedule of each
  Team." — and of each Team's page, with the unreachable marks and the gap that follows; each Team's On-call now on the
  Teams list (C-25 UI, from C-27 on).
- The Team page's "Schedules" section with "New Schedule" for Admins (C-27 UI).
- The iCal section of the Schedule page: "New link", the link shown once, "Revoke" (C-27.FR-10).
- The Home page as a page of its own at `/` (Open question 1).

**OUT**

- The Schedule page itself: S-135. The API: S-133, S-134, S-164. The documentation: S-137.
- "I am an Assignee" on the Home page: S-159. A dashboard on the Home page: specified later in L2 (L2.md §1.5).

## Contracts

- **API used**: `getOnCallNow` (`teams=all` or `mine`), `listSchedules` (`team`), `getScheduleOnCall` (the current
  segment and warnings of each Schedule on the Team page), `getMyIcalFeed`, `issueMyIcalFeed`, `revokeMyIcalFeed`,
  `listTeams`, `getMe`.
- **Routes**: `/` (`index.tsx`) becomes the Home page instead of the redirect to `/alert-groups`; "Home" is the first
  entry of `app-shell.tsx`, before "Alert Groups"; the Teams list and the Team page gain their sections; the Schedule
  page gains "iCal subscription".
- **Components**: `on-call-now.tsx` — one row per Team: the Team, the On-call by their login, the Schedule's name and
  "until {time}", "unreachable in {messenger}" for each mark, "next: gap" when a gap follows, "No Schedule"; the switch
  "All Teams" / "My Teams" on the Home page, and the Team's row alone on its page; `team-schedules.tsx` — each Schedule
  with "main", its Rotation layers, its On-call now and its warnings, linked to its page; `ical-subscription.tsx` — the
  section with "New link" (which confirms that the old link stops working when one exists), the link shown once with
  "Copy" and "Copy the link now: it will not be shown again", and "Revoke".
- **Documentation links** (`docs-links.json`): `field.on_call_now` → `concepts/schedules.md`, `field.ical` →
  `concepts/schedules.md#ical-feed`.
- **Texts** (EN / RU), from C-27's UI and the approved mockups: "Home" / «Главная», "On call now" / «Сейчас дежурит»,
  "All Teams" / «Все команды», "My Teams" / «Мои команды», "By the main Schedule of each Team" / «По главному
  расписанию каждой команды», "until {{time}}" / «до {{time}}», "unreachable in {{messenger}}" / «недостижим в
  {{messenger}}», "next: gap" / «дальше дыра», "No Schedule" / «Без расписания», "Schedules" / «Расписания», the hint
  of the Team page / «Главное расписание показывается на главной в „Сейчас дежурит“; шаги цепочки могут звать
  дежурного любого из них.», "iCal subscription" / «Подписка iCal», "A personal link for your calendar application.
  It is shown once; a revoked link stops working at once." / «Личная ссылка для календаря. Показывается один раз;
  отозванная перестаёт работать сразу.», "Copy the link now: it will not be shown again" / «Скопируйте ссылку сейчас —
  больше её не покажем», "New link" / «Новая ссылка», "Revoke" / «Отозвать».

## Steps

1. Write "On call now" and the Home page. Check: `on-call-now.test.tsx`, `app-shell.test.tsx`.
2. Add the sections of the Team page and the Teams list. Check: `team-schedules.test.tsx`.
3. Write the iCal section. Check: `ical-subscription.test.tsx`.
4. Add the links and texts, and the end-to-end specs. Check: the Verification below; `make e2e`.

## Verification

Run `make dev`; from a terminal set up the Teams of scenario 5 of C-27: "devops" whose main Schedule has ivan On-call
until next Monday 10:00, "payments" whose main Schedule has petr — without a Mattermost Account link, with the
Mattermost Destination `payments-notice` of the Team — On-call now and a gap after his Shift, and "search" without a
Schedule; the Admin in "devops" only. Then in Playwright at 1280 × 800, in English and in Russian:

1. Sign in as `admin@example.org`: the first entry of the navigation is "Home"; `/` shows "On call now" with "All Teams",
   "By the main Schedule of each Team", "devops — @ivan · Main · until Mon … 10:00", "payments — @petr", "unreachable
   in Mattermost", "next: gap", and "search — No Schedule".
2. "My Teams" → only "devops".
3. Teams → each row shows the On-call: "@ivan", "@petr", "No Schedule" for search.
4. Teams → "payments" → "On call now" shows its row; "Schedules" shows "Main" with "main", two Rotation layers, "@petr"
   and its warnings, "New Schedule" and the hint.
5. "Main" → "iCal subscription" → "New link" → the link `http://localhost:8080/ical/mstr_cal_…` and "Copy the link now: it
   will not be shown again"; reload → no link, "New link" and "Revoke"; "Revoke" → only "New link".
6. At 360 × 740, the Home page has no horizontal scrolling.

`make e2e` runs these steps in `home.spec.ts`, `teams.spec.ts` and `schedules.spec.ts`; `navigation.spec.ts` checks the
new first entry and `phone-width.spec.ts` the Home page at 360 pixels.

## Open questions

1. **The Home page.** C-27.FR-9 and C-32.FR-18 place sections on "the Home page", and the approved mockups draw it as a
   page of its own, "Home", first in the navigation; in L1, `/` opens the Alert Group list. Options: (a) a Home page at
   `/` with "On call now" (and "I am an Assignee" from S-159), the Alert Group list staying at `/alert-groups`; (b)
   "On call now" as a section above the Alert Group list, which stays the start page. Recommended: (a), as the mockups
   show; the contract follows it.

## Notes

- Suggested commit: `feat(web): show who is on call now and the ical link`.
- The link of the iCal feed is shown once from the answer of `issueMyIcalFeed` and never stored by the SPA.
- "unreachable in Mattermost" on the Home page is the short form of the mark that the Schedule page writes in full.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-27.FR-1 | partial | the Schedules of the Team page |
| C-27.FR-8 | partial | the marks in "On call now" |
| C-27.FR-9 | partial | the pages; the API is S-133 and S-164 |
| C-27.FR-10 | partial | the iCal section; the API is S-134 |
| C-27.AC-6 | partial | the mark in "On call now" |
| C-27.AC-9 | partial | "On call now" on the Home page |
