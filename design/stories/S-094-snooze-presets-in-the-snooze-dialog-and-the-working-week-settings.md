---
id: S-094
title: Snooze presets in the Snooze dialog and the bulk Snooze, and the working day and week on the General page (FE)
capability: C-10
kind: fe
layer: L1
depends_on: [S-092, S-033, S-056]
covers: [C-10.FR-18, C-10.AC-24, C-20.FR-9]
files_touched:
  - web/src/components/snooze-dialog.tsx
  - web/src/components/snooze-dialog.test.tsx
  - web/src/components/bulk-actions-bar.tsx
  - web/src/components/bulk-actions-bar.test.tsx
  - web/src/components/command-buttons.test.tsx
  - web/src/lib/commands.ts
  - web/src/routes/admin.organization.general.tsx
  - web/src/routes/profile.tsx
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/snooze-presets.spec.ts
  - web/e2e/organization-settings.spec.ts
  - web/e2e/bulk-commands.spec.ts
  - docs/concepts/statuses-and-commands.md
acceptance:
  - "[C-10.FR-18, C-10.AC-24] The Snooze dialog of an Alert Group offers, after the Route's durations and before \"Until\" and \"No end\", \"Until tomorrow 09:00\" and \"Until Monday 09:00\" — the Organization's `workday_start` and `week_start` — each with \"Ends {date and time}\" in the user's time zone, as `listSnoozePresets` answers it for that zone; choosing one sends `{\"preset\", \"time_zone\"}` with the user's time zone, and the page header then shows the Snooze end (`snooze-dialog.test.tsx`, `snooze-presets.spec.ts`)."
  - "[C-10.FR-18, C-10.AC-24] The bulk Snooze, which has no Route and so no durations, offers the same two presets and sends the chosen one in the `snooze` of `runBulkCommand`; every selected Alert Group then has the same Snooze end (`bulk-actions-bar.test.tsx`, `snooze-presets.spec.ts`)."
  - "[C-10.FR-18] The presets are offered on a Route without Snooze durations too; while `listSnoozePresets` is loading or fails, each preset still shows and can be chosen, without its end (`snooze-dialog.test.tsx`)."
  - "[C-10.AC-24] In Russian the choices read «До завтра 09:00» and «До понедельника 09:00», each with «Закончится {date and time}»; at 360 CSS pixels the dialog, a full-screen sheet, shows both with their ends, and the page does not scroll horizontally, in either language (`snooze-presets.spec.ts`)."
  - "[C-20.FR-9] The General page of the Organization has \"Start of the working day\" (a time input) and \"First day of the week\" (Monday to Sunday) under the time zone, with the hint \"Snooze presets end at this time and on this day.\"; saving 08:30 and Sunday makes the Snooze dialog offer \"Until tomorrow 08:30\" and \"Until Sunday 08:30\"; a value the API refuses shows its message at the field (`organization-settings.spec.ts`)."
  - "[C-10.FR-18] The time zone in the profile has the hint \"Snooze presets pressed in messengers end in this time zone. While it follows the browser, they use the Organization's time zone, {zone}.\" (`snooze-presets.spec.ts`)."
  - "Every new text exists in English and Russian; the documentation page Concepts → Statuses and commands says how the web UI shows the presets and their ends, and `make docs-check` passes."
verify: "make ci e2e docs-check"
operator_attention: false
issue: 297
---

# S-094. Snooze presets in the Snooze dialog and the bulk Snooze, and the working day and week on the General page (FE)

## Scope

**IN**

- The two Snooze presets in the Snooze dialog of an Alert Group and of a bulk Snooze, with the end each would set, read
  from `listSnoozePresets` for the user's time zone (C-10.FR-18).
- "Start of the working day" and "First day of the week" on the General page of the Organization (C-20.FR-9).
- The hint at the profile's time zone about presets pressed in messengers.
- The web UI part of the documentation of the presets.

**OUT**

- The rule, the settings and the API: S-092. Buttons and Text commands in messengers: S-093.
- The quick durations, "Until" and "No end" of the dialog: unchanged (S-033).

## Contracts

- **API used**: `listSnoozePresets` (`time_zone` = the user's time zone of `useTimeFormat`, the profile's or the
  browser's), `snoozeAlertGroup` and `runBulkCommand` with `{"preset", "time_zone"}`, `getOrganization` and
  `updateOrganization` (`workday_start`, `week_start`), `getMe` (the profile's `time_zone`).
- **Snooze dialog** (`snooze-dialog.tsx`, `lib/commands.ts`): the choices are the Route's durations, then the presets
  `tomorrow` and `week_start`, then "Until" and "No end". A preset's label is "Until tomorrow {start}" or "Until {day}
  {start}" from the Organization's settings — the day in Russian in the genitive, as on messenger buttons (reference.md)
  — and under it "Ends {date and time}" from `listSnoozePresets`, read when the dialog opens and formatted in the user's
  time zone like every absolute time of the UI (C-09.FR-17). The request is `{"preset": …, "time_zone": …}`; the server
  computes the end at the moment of the command on the business clock, so the shown end is what the Snooze sets unless
  the start of the working day passes while the dialog is open. Test ids: `snooze-choice-tomorrow`,
  `snooze-choice-week_start`, and `snooze-preset-end-tomorrow`, `snooze-preset-end-week_start` for the ends.
- **Bulk Snooze**: the same dialog with `bulk`; it now offers the presets besides "Until" and "No end".
- **General page** (`admin.organization.general.tsx`): a time input (`HH:MM`, 24-hour) and a select of the seven days
  starting with Monday, saved with the rest of the page through `organizationInput`, which already sends every field it
  read; a `400` or `422` with the pointer `/workday_start` or `/week_start` shows at its field.
- **Profile** (`profile.tsx`): the hint under the time zone, naming the Organization's `time_zone`.
- **Texts** (EN / RU): "Until tomorrow {{time}}" / «До завтра {{time}}», "Until {{day}} {{time}}" / «До {{day}}
  {{time}}», "Ends {{time}}" / «Закончится {{time}}», the seven days in the genitive for Russian labels, "Start of the
  working day" / «Начало рабочего дня», "First day of the week" / «Первый день недели», "Snooze presets end at this time
  and on this day." / «В это время и в этот день заканчиваются пресеты откладывания.», and the profile hint of
  reference.md / «Пресеты откладывания, нажатые в мессенджерах, заканчиваются в этом часовом поясе. Пока он берётся из
  браузера, используется часовой пояс Организации — {{zone}}.».
- **Documentation**: the web UI paragraph of Concepts → Statuses and commands.

## Steps

1. Add the presets to the Snooze dialog and its bulk form. Check: `snooze-dialog.test.tsx` covers the labels from the
   settings, the ends from a mocked `listSnoozePresets`, the request, a Route without durations, a failed read and the
   Russian genitive; `bulk-actions-bar.test.tsx` covers the bulk request.
2. Add the two fields to the General page and the hint to the profile. Check: `organization-settings.spec.ts` saves
   08:30 and Sunday, and shows a refused value at its field.
3. Write `snooze-presets.spec.ts`, the texts and the documentation. Check: the Verification below; `make lint` (the
   translations) and `make docs-check`.

## Verification

Run `make dev`, sign in as `admin@example.org` / `muster-dev-password` in a browser whose time zone is Europe/Berlin
(the Playwright context's `timezoneId`), with the profile following the browser; create the Responder "bob" and two
firing Alert Groups on the On-call Route "ops" from a terminal as in S-063, and move the development clock to the next
02:00 Berlin time (`AT` of S-092 with the zone's offset). Then in Playwright, at 1280 × 800 and at 360 × 740, in English
and in Russian:

1. Open the first Alert Group → "Snooze" → the choices "1 h", "4 h", "24 h", "Until tomorrow 09:00", "Until Monday
   09:00", "Until", "No end"; "Until tomorrow 09:00" shows "Ends" with today's date and 09:00, because at 02:00 the
   next start of the working day is the same morning; "Until Monday 09:00" shows the next Monday at 09:00.
2. Choose "Until tomorrow 09:00" → "Snooze" → the header shows "Snoozed by admin" and the end 09:00; from a terminal,
   `snooze_until` of the Alert Group is 07:00Z or 08:00Z — 09:00 in Berlin.
3. List → select both Alert Groups → "Snooze" → "Until Monday 09:00" → "Snooze" → both rows show the same Snooze end.
4. Organization → General → "Start of the working day" 08:30, "First day of the week" Sunday → "Save"; back on an
   Alert Group, the Snooze dialog offers "Until tomorrow 08:30" and "Until Sunday 08:30"; "Start of the working day"
   cleared → "Save" → the message at the field.
5. Profile → the hint under the time zone names the Organization's time zone, "UTC".
6. In Russian: the dialog reads «До завтра 08:30» and «До воскресенья 08:30» with «Закончится …».
7. At 360 × 740: the dialog is a full-screen sheet with both presets and their ends in view, and
   `expectNoHorizontalScroll` passes on the Alert Group page with the dialog open, in both languages.

Screenshots of steps 1, 4 and 7 in both languages go into the pull request. `make e2e` runs these steps as
`web/e2e/snooze-presets.spec.ts` and `organization-settings.spec.ts`.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): offer the snooze presets in the snooze dialog and set the working week`.
- The ends come from the server, not from the browser's clock, because the rule lives in one place (S-092) and the
  development clock moves it; the quick durations keep counting from the browser's clock, as S-033 made them.
- The dialog shows the end under each preset because "Until tomorrow 09:00" ends the same morning before 09:00
  (C-10.FR-17): the person sees what they choose.
- Added after all stories of L1 had merged (D379); see S-092.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-10.FR-18 | partial | the Snooze dialog and the bulk Snooze; the API is S-092, messengers S-093 |
| C-10.AC-24 | full | |
| C-20.FR-9 | partial | the General page; the settings in the API are S-092 |
