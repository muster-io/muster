---
id: S-135
title: The Schedule page with its two-week view, Rotation layers, Overrides with the Override form, and warnings (FE)
capability: C-27
kind: fe
layer: L2
depends_on: [S-119, S-133, S-164]
covers: [C-27.FR-1, C-27.FR-2, C-27.FR-3, C-27.FR-4, C-27.FR-5, C-27.FR-6, C-27.FR-7, C-27.FR-8, C-27.FR-13, C-27.AC-2, C-27.AC-6, C-27.AC-9, C-25.FR-2]
files_touched:
  - web/src/routes/schedules.index.tsx
  - web/src/routes/schedules.new.tsx
  - web/src/routes/schedules.$scheduleId.tsx
  - web/src/components/schedule-form.tsx
  - web/src/components/schedule-form.test.tsx
  - web/src/components/rotation-layers-editor.tsx
  - web/src/components/rotation-layers-editor.test.tsx
  - web/src/components/schedule-view.tsx
  - web/src/components/schedule-view.test.tsx
  - web/src/components/schedule-on-call-now.tsx
  - web/src/components/schedule-warnings.tsx
  - web/src/components/schedule-warnings.test.tsx
  - web/src/components/schedule-overrides.tsx
  - web/src/components/override-form.tsx
  - web/src/components/override-form.test.tsx
  - web/src/lib/schedule-time.ts
  - web/src/lib/schedule-time.test.ts
  - web/src/components/team-filter.tsx
  - web/src/routes/teams.index.tsx
  - web/src/components/audit-diff.tsx
  - web/src/lib/docs-links.json
  - web/src/components/docs-link.tsx
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - design/prd/l1/reference.md
  - web/e2e/schedules.spec.ts
  - web/e2e/docs-links-forms.spec.ts
acceptance:
  - "[C-27.FR-4, C-27.FR-5, C-27.FR-7, C-27.AC-9] In a real browser the Schedule page shows the header \"Team payments · Schedule Main\" with \"main\", and a two-week view with one row per Rotation layer, a row of Overrides and a row \"Result — who is On-call\", a gap in red, the Shifts of a disabled person hatched, a mark for now, and \"Previous week\", \"Today\" and \"Next week\"; \"Warnings\" lists \"Gap: {from} – {to}\" and \"Disabled: @sergey in Around the clock\", in English and in Russian (`schedule-view.test.tsx`, `schedule-warnings.test.tsx`, `schedules.spec.ts`)."
  - "[C-27.FR-3, C-27.FR-6, C-27.AC-2, C-27.AC-9] \"Add Override\" in the mode \"I give my Shift to…\" on olga's segment Wednesday 11:00–19:00 of the row \"Result\", with petr as the person who takes it, shows \"Choose a segment in the row 'Result' and the person who takes it; from 11:00 to 19:00 they are On-call on top of every Rotation layer.\" and saves an Override of exactly that segment, which the row of Overrides then shows with who made it; \"I take the Shift of…\" makes the caller the person who takes it; from and to cannot leave the segment; a refusal of the server — `not_in_team`, `not_pageable` — shows its message in the form; the section's hint reads \"Any Responder of the Team; no messages.\" (`override-form.test.tsx`, `schedules.spec.ts`)."
  - "[C-27.FR-8, C-27.AC-6] \"On call now\" on the Schedule page shows the person, the Rotation layer and until when, \"Unreachable in Mattermost: no linked account.\" for an On-call without a Mattermost Account link in a Team with a Mattermost Destination, and \"Next: …\" with the next person or the gap (`schedules.spec.ts`)."
  - "[C-27.FR-1, C-27.FR-2, C-27.FR-13] An Admin creates a Schedule from \"New Schedule\" with its name, time zone and Rotation layers — name, people in turn, Shift length (a day, a week or hours), handover time and day, start date and the optional limits of days and time — edits them under \"Rotation layers\" (top first, each with its limits, Shift length, handover and people in turn) with \"Add Rotation layer\", marks it main and sets the gap alert \"By default\", \"On\" or \"Off\"; a person the server refuses as `not_pageable` is shown at the person; a Responder sees the page without these controls (`rotation-layers-editor.test.tsx`, `schedule-form.test.tsx`, `schedules.spec.ts`)."
  - "[C-25.FR-2] The Schedules list, reached from the Teams page, shows every Schedule with its Team and \"main\", and has the filter \"Team\" with every Team and \"My Teams\" (`schedules.spec.ts`)."
  - "Every new text exists in English and Russian; the Schedule page has \"Guide\" to Concepts → Schedules, the Overrides section \"Learn more\" to its section \"Overrides\" and the gap alert \"Learn more\" to \"Gaps and the Internal alert\"; `make e2e` passes."
verify: "make ci e2e"
operator_attention: false
issue: 424
---

# S-135. The Schedule page with its two-week view, Rotation layers, Overrides with the Override form, and warnings (FE)

## Scope

**IN**

- The Schedule page (C-27 UI): its header, "On call now" with the unreachable mark and what comes next, "Warnings", the
  two-week view with the row "Result", "Rotation layers" with their editor for Admins, "Overrides" with the Override
  form in its two modes, and the gap alert setting.
- The new Schedule form, and the Schedules list with the filter by Team and "My Teams" (C-25 UI).

**OUT**

- Schedules on the Team page, "On call now" on the Home and Team pages and the Teams list, and the iCal section: S-136.
- The API: S-131, S-133, S-164. The documentation: S-137.
- "A call through the Notification gateway works." after the unreachable mark: S-150.

## Contracts

- **API used**: `listSchedules` (`team`), `createSchedule`, `getSchedule`, `updateSchedule` (`name`, `time_zone`,
  `main`, `gap_alert`, `rotation_layers`), `deleteSchedule`, `getScheduleOnCall` (`from`, `to`: the two weeks shown),
  `listScheduleOverrides`, `createScheduleOverride`, `updateScheduleOverride`, `deleteScheduleOverride`, `getTeam`,
  `listTeamUsers` (the people offered), `listTeams` (the filter), `getMe` (the Permissions that show or hide the
  controls, and whether the caller is in the Team).
- **Routes** (file-based): `/schedules` (`schedules.index.tsx`, with the search parameter `team`), `/schedules/new`
  (`schedules.new.tsx`, with `team`), `/schedules/$scheduleId` (`schedules.$scheduleId.tsx`); the Teams page links to
  the list as "All Schedules" (settled 1).
- **Components**:
  - `schedule-view.tsx` — the two-week view from `getScheduleOnCall`: columns per day in the Schedule's time zone, a row
    per Rotation layer (top first) with its Shifts and skipped people hatched, a row of Overrides, the row "Result — who
    is On-call" whose gaps are red and whose segments are buttons that the Override form takes, a mark for now, and
    "Previous week", "Today", "Next week"; keyboard reachable, each segment with an accessible name "{person}, {from} –
    {to}" or "Gap, {from} – {to}".
  - `schedule-on-call-now.tsx` — the person, the Rotation layer, "until {time}", the unreachable marks and "Next:
    {person} from {time}" or "Next: gap from {time}".
  - `schedule-warnings.tsx` — the texts of reference.md: "Gap: {from} – {to}", "Disabled: @{login} in {Rotation layer}",
    "@{login} is a Viewer", "@{login} is not in the Team", "Unreachable in {messenger}: no linked account.", and the
    texts of settled 2 for the other reasons.
  - `rotation-layers-editor.tsx` — each Rotation layer with name, people in turn (ordered, from the Team), Shift length
    (a day, a week, or a number of hours, which the form sends as `shift_seconds`, hours × 3600, and shows back in
    hours), handover time and day, start date, limits (days, from, to — a "to" earlier than "from" reads
    "until {to} the next day" / «до {to} следующего дня», C-27.FR-2); "Add Rotation layer"; the order top first.
  - `schedule-form.tsx` — name, time zone (the picker of `time-zone-picker.tsx`), "main", the gap alert "By default"
    ("On while this is the Team's main Schedule"), "On", "Off".
  - `schedule-overrides.tsx` — "Overrides" with the hint "Any Responder of the Team; no messages.", each with who gives
    what to whom ("@olga → @petr", with the person whose Shift it takes when named), when and who made it; change and
    delete for the people who may.
  - `override-form.tsx` — "Add Override": "I take the Shift of…" or "I give my Shift to…" on a segment picked in the row
    "Result", then who takes it, and from and to within that segment, with the hint "Choose a segment in the row
    'Result' and the person who takes it; from {from} to {to} they are On-call on top of every Rotation layer.", "Save"
    and "Cancel"; it sends `user_id`, `replaces_user_id` (the segment's person, if any) and the span.
  - `lib/schedule-time.ts` — wall-clock times of the Schedule's zone with date-fns 4, shared by the view and the form.
- **Shown by Permission**: `schedules:write` — "New Schedule", the editor, "main", the gap alert, delete;
  `schedule-overrides:write` and being in the Team or an Admin — "Add Override" and the changes of Overrides.
- **Documentation links** (`docs-links.json`): `page.schedule` → `concepts/schedules.md`, `field.overrides` →
  `concepts/schedules.md#overrides`, `field.gap_alert` → `concepts/schedules.md#gaps-and-the-internal-alert`,
  `field.unreachable_on_call` → `concepts/schedules.md#unreachable-on-calls`; the anchors exist from S-131, S-133 and
  S-164. `GUIDE_ABOUT` of `web/src/components/docs-link.tsx` (`satisfies Record<PageDocsKey, …>`) gains
  `page.schedule` with the text `docs.guides.schedule` ("Schedules" / «Расписания») in both locales, so that the
  TypeScript check passes.
- **Audit log names** (`audit-diff.tsx`): `/rotation_layers` "Rotation layers", `/main` "Main Schedule", `/gap_alert`
  "Gap alert", `/time_zone` "Time zone".
- **Texts** (EN / RU), from C-27's UI and the approved mockups: "Schedules" / «Расписания», "New Schedule" / «Новое
  расписание», "Team {{team}} · Schedule {{name}}" / «Команда {{team}} · Расписание {{name}}», "main" / «главное»,
  "{{count}} Rotation layers: the upper, narrower one wins. Overrides send no messages." with plural forms / «{{count}}
  слоя ротации: верхний, более узкий, побеждает. Замены не рассылают уведомлений.», "On call now" / «Сейчас дежурит»,
  "until {{time}}" / «до {{time}}», "Next: {{who}}" / «Дальше: {{who}}», "Warnings" / «Предупреждения», "Gap: {{from}} –
  {{to}}" / «Дыра: {{from}} – {{to}}» («дыра» is the one Russian word for a gap, as in "next: gap" / «дальше дыра» of
  S-136), "Result — who is On-call" / «Итог — кто дежурит», "Previous week" / «Предыдущая неделя», "Today" / «Сегодня»,
  "Next week" / «Следующая неделя», "Rotation layers" / «Слои ротации», "Add Rotation layer" / «Добавить слой»,
  "Overrides" / «Замены», "Add Override" / «Добавить замену», "Any Responder of the Team; no messages." / «Любой
  Исполнитель команды; уведомлений нет.», "I take the Shift of…" / «Возьму смену за…», "I give my Shift to…" / «Отдам
  смену…», the hint of the form / «Выберите отрезок в строке „Итог“ и того, кто его берёт; с {{from}} до {{to}} он
  дежурит поверх всех слоёв.», "All Schedules" / «Все расписания».

## Steps

1. Write the time helpers and the two-week view. Check: `schedule-time.test.ts` covers a change of the clocks;
   `schedule-view.test.tsx` covers gaps, hatched Shifts, an Override and the mark for now.
2. Write the Schedule page with "On call now", "Warnings", "Rotation layers" and the form. Check:
   `schedule-warnings.test.tsx`, `rotation-layers-editor.test.tsx`, `schedule-form.test.tsx`.
3. Write the Overrides and the Override form. Check: `override-form.test.tsx`.
4. Add the list, the routes, the links and the texts. Check: `make lint`.
5. Write `web/e2e/schedules.spec.ts`. Check: the Verification below; `make e2e`.

## Verification

Run `make dev`; from a terminal set up, as in S-131 and S-164, the Team "payments" with petr, sergey, olga and alice,
the Schedule "Main" with the two Rotation layers of scenario 1 starting on MON, the Mattermost Destination
`payments-notice` of the Team, petr without a Mattermost Account link, and sergey disabled; then move the development
clock to MON 10:30 Berlin time (`TO` of S-133), in week 1 of the set-up, where the lower Rotation layer is petr's and
the upper one olga's, and sign in afterwards, since the move ends sessions. Then in Playwright at 1280 × 800, in
English and in Russian:

1. Sign in as `admin@example.org`; Teams → "All Schedules" → the list shows "Main" with "payments" and "main"; "Team" →
   "My Teams" → empty for the Admin.
2. Open "Main": the header reads "Team payments · Schedule Main" with "main"; the view shows "Around the clock",
   "Working hours 11-19", "Overrides" and "Result — who is On-call", with the mark for now on Monday 10:30; "Warnings"
   lists "Disabled: @sergey in Around the clock" and "Gap: …" — week 2, in which sergey has the lower Rotation layer,
   falls in the two weeks shown, and its red gaps outside 11:00–19:00 on weekdays and his hatched Shifts are drawn in
   the second week of the view.
3. "Next week" moves the view by seven days; "Today" returns.
4. Sign in as petr, who is On-call now: "On call now" shows "@petr", "Around the clock", "until 11:00" and "Unreachable
   in Mattermost: no linked account.".
5. Sign in as olga: "Add Override" → "I give my Shift to…" → click olga's segment of Wednesday 11:00–19:00 of week 1 in
   "Result" → "petr" → the hint reads "… from 11:00 to 19:00 they are On-call on top of every Rotation layer." → "Save";
   the row of Overrides shows "@olga → @petr", Wednesday 11:00–19:00, made by olga, and "Result" shows petr then.
6. As olga, "Rotation layers" has no "Add Rotation layer" and no "Save".
7. As the Admin, "New Schedule" from the Team page link → name "Second line", one Rotation layer with olga → "Save";
   its page shows no "main" and the gap alert "By default".

`make e2e` runs these steps in `schedules.spec.ts`.

## Open questions

None open. Settled:

1. **Settled: the Schedules list has no entry in the main navigation.** It is at `/schedules`, reached from the Teams
   page as "All Schedules", because C-25's UI gives the lists of Schedules the filter by Team and "My Teams" while the
   approved mockups show no navigation entry for Schedules.
2. **Settled: the texts of the other unreachable reasons go into reference.md in this pull request**, in the row
   "Schedule page, 'On call now'": "Unreachable in {Destination}: the bridge cannot mention people." / «Недостижим в
   {Destination}: мост не умеет упоминать людей.»; "Unreachable in {Destination}: the outgoing webhook has no reply in
   thread." / «Недостижим в {Destination}: у исходящего вебхука нет ответа в тред.»; "Unreachable in {Destination}: not
   in the discussion group." / «Недостижим в {Destination}: не состоит в группе обсуждения.».

## Notes

- Suggested commit: `feat(web): add the schedule page with its two-week view and overrides`.
- The mockup's line "You can give only your own Shift" is not shown: an Override names whoever takes the Shift (L2.md
  §5.2, journal D567).
- The form never sends a span outside the picked segment, but the API takes any span (C-27.AC-2 is an Override across
  two segments), so a script can still make one.
- Depends on S-164 for the unreachable marks and on S-119 for the Team pages and the filter by Team.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-27.FR-1 | partial | the page and the form; the Team page S-136 |
| C-27.FR-2 | partial | the editor |
| C-27.FR-3 | partial | the two modes of the form |
| C-27.FR-4 | partial | the view and the row "Result" |
| C-27.FR-5 | partial | the warnings and the hatched Shifts |
| C-27.FR-6 | partial | who may make an Override, on the page |
| C-27.FR-7 | partial | the gaps on the page and the gap alert setting |
| C-27.FR-8 | partial | the unreachable mark on the Schedule page; "On call now" elsewhere S-136 |
| C-27.FR-13 | partial | the controls for Admins only |
| C-27.AC-2 | partial | "I give my Shift to…" on a segment |
| C-27.AC-6 | partial | the mark on the Schedule page |
| C-27.AC-9 | partial | the Schedule page; the Home page is S-136 |
| C-25.FR-2 | partial | the filter of the Schedules list |
