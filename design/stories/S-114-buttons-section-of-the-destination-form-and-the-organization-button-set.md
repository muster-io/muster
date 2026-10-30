---
id: S-114
title: The Buttons section of the Destination form with its preview, and the Organization's button set (FE)
capability: C-28
kind: fe
layer: L2
depends_on: [S-113, S-112]
covers: [C-28.FR-8, C-28.FR-10, C-28.FR-11, C-28.AC-9, C-13.FR-9, C-20.FR-1]
files_touched:
  - web/src/components/destination-form.tsx
  - web/src/components/destination-form.test.tsx
  - web/src/components/button-set-editor.tsx
  - web/src/components/profile-picker.tsx
  - web/src/components/button-set-editor.test.tsx
  - web/src/lib/button-sets.ts
  - web/src/lib/button-sets.test.ts
  - web/src/routes/admin.organization.alert-handling.tsx
  - web/src/components/audit-diff.tsx
  - web/src/components/audit-diff.test.tsx
  - web/src/lib/docs-links.json
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/destinations.spec.ts
  - web/e2e/organization-settings.spec.ts
  - web/e2e/docs-links-forms.spec.ts
  - web/e2e/screenshots/organization.spec.ts
  - web/e2e/screenshots/mattermost.spec.ts
  - docs/concepts/statuses-and-commands.md
  - docs/configure/organization.md
acceptance:
  - "[C-28.FR-10, C-13.FR-9] The Destination form of every messenger type has the section \"Buttons\" with \"Which buttons the Root message shows. Hidden actions stay in Muster and in the Text commands of Bridges.\" and the choice \"As in the Organization\" or \"Own for this Destination\"; \"Own\" shows a tick box for Acknowledge, Resolve, each distinct Snooze duration of the Organization's Routes and both Snooze presets, and saving sends `buttons` as `DestinationButtonSet` (`button-set-editor.test.tsx`, `destination-form.test.tsx`)."
  - "[C-28.FR-8, C-28.AC-9] The preview \"What {Destination} shows\" lists, in the order of a Root message, the buttons of a firing and of an acknowledged Root message for the chosen set, and follows each tick box at once, in English and in Russian (`button-set-editor.test.tsx`, `destinations.spec.ts`)."
  - "[C-28.FR-8, C-20.FR-1] Organization → Alert handling has \"Buttons of Root messages\" with \"All buttons\" (the default) or a tick box per button, and the same preview; saving sends `buttons` as `OrganizationButtonSet`, and a Destination that follows the Organization then shows the new set on its next re-render in the fake Mattermost server (`organization-settings.spec.ts`)."
  - "[C-28.FR-10] The Audit log names `/buttons` \"Buttons\" in the diffs of `destination.updated` and `organization.updated`; a refused set shows the server's message at the field."
  - "[C-28.FR-11] The documentation describes the sets on the Destination form and on Alert handling, with screenshots, and \"Learn more\" next to both sections opens the section \"Button sets\"; `make docs-check` passes."
verify: "make ci e2e docs-check"
operator_attention: false
issue: 403
---

# S-114. The Buttons section of the Destination form with its preview, and the Organization's button set (FE)

## Scope

**IN**

- The section "Buttons" of the Destination form of every messenger type, with "As in the Organization" or its own tick
  boxes and the preview "What {Destination} shows".
- The Organization's button set on Organization → Alert handling, with the same preview.
- The Audit log names of the field, the "Learn more" links, the documentation steps and screenshots.

**OUT**

- The API, the filter and the re-render: S-113.
- The Mentions grid of the Destination form, its warnings and "What each messenger supports": S-143.
- Outgoing webhooks, which have no buttons (S-113, Open question 1).

## Contracts

- **API used**: `getDestination`, `updateDestination`, `createDestination` (`buttons`), `getOrganization`,
  `updateOrganization` (`buttons`, `workday_start`, `week_start`), `listRoutes` (the distinct
  `policy.snooze_durations_seconds` of the Organization's Routes, offered as tick boxes in ascending order, with any
  duration the stored set names that no Route has, so that saving never drops it), `listRouteProfiles` (the Paging
  profile's durations, offered and previewed when no Route has any, so that a fresh Organization has tick boxes).
- **Components**: `button-set-editor.tsx` — the choice (the radio "As in the Organization" / "Own for this
  Destination" on a Destination, "All buttons" / a list on the Organization), the tick boxes and the preview;
  `lib/button-sets.ts` computes what a set shows, as `internal/buttons` does: for a firing Root message Ack, Resolve,
  the shown durations and presets; for an acknowledged one Unack, Resolve and the same; the labels "Ack", "Unack",
  "Resolve", "Snooze {duration}" and "Until tomorrow {start}" / "Until {day} {start}" from the Organization's
  `workday_start` and `week_start`, as the messenger labels of reference.md read. The preview uses the Paging profile's
  durations when the Organization has no Route with durations. Test ids `button-set-source`, `button-set-item-<key>`
  and `button-set-preview-firing`, `button-set-preview-acknowledged`.
- **Destination form** (`destination-form.tsx`): the section after the type's fields, for Mattermost, Telegram, Bridge
  and Slack; a refused item shows its `errors[].pointer` under the section.
- **Alert handling** (`admin.organization.alert-handling.tsx`): the section "Buttons of Root messages" after the
  Instance labels; the page's save sends the set with the rest.
- **Audit log** (`audit-diff.tsx`): `/buttons` → "Buttons".
- **Documentation links**: `field.buttons` → `concepts/statuses-and-commands.md#button-sets`, next to both sections.
- **Texts** (EN / RU), from C-28's UI and the approved mockups: "Buttons" / «Кнопки», the hint / «Какие кнопки
  показывать в корневом сообщении. Скрытые действия остаются в Muster и в текстовых командах мостов.», "As in the
  Organization" / «Как в Организации», "Own for this Destination" / «Свои для этого места доставки», "What
  {{destination}} shows" / «Так увидят в {{destination}}», "Buttons of Root messages" / «Кнопки корневых сообщений»,
  "All buttons" / «Все кнопки», "Firing" and "Acknowledged" as the status words of the preview.
- **Documentation**: Statuses and commands — steps for the Destination's set with a screenshot; Organization settings
  — the set under Alert handling with its screenshot. The screenshot specs `organization.spec.ts` and
  `mattermost.spec.ts` take the two images.

## Steps

1. Write `lib/button-sets.ts` and the editor. Check: `button-sets.test.ts` covers every status and set, a Route without
   durations and the presets' labels; `button-set-editor.test.tsx` covers the choice, the tick boxes and the preview.
2. Put the editor on the Destination form and Alert handling, and name the field in the Audit log. Check:
   `destination-form.test.tsx`, `audit-diff.test.tsx`; `make lint`.
3. Write the end-to-end specs, the documentation and the screenshot specs. Check: the Verification below; `make e2e`;
   `make docs-screenshots GUIDES="organization mattermost"`; `make docs-check`.

## Verification

Run `make dev`, sign in as `admin@example.org` / `muster-dev-password`, and from a terminal create, as in S-113, the
Paging Route "t" with the Mattermost Destinations "alerts" (`ch-alerts`) and "alerts-prod" (`ch-alerts-prod`) and one
firing Alert Group. Then in Playwright at 1280 × 800, in English and in Russian:

1. Destinations → "alerts" → "Buttons": "As in the Organization" is chosen; the preview shows for Firing "Ack",
   "Resolve", "Snooze 1 h", "Snooze 4 h", "Snooze 24 h", "Until tomorrow 09:00", "Until Monday 09:00".
2. "Own for this Destination" → untick everything but Acknowledge, "24 h" and "Until tomorrow" → the preview reads
   Firing "Ack", "Snooze 24 h", "Until tomorrow 09:00" and Acknowledged "Unack", "Snooze 24 h", "Until tomorrow 09:00"
   → "Save".
3. From a terminal: the open Root message in `ch-alerts` of the fake Mattermost server now has the actions `Ack`,
   `Snooze 24 h`, `Until tomorrow 09:00` (the `MMROOT` of S-113).
4. Organization → Alert handling → "Buttons of Root messages" → untick "Resolve" → "Save"; the Root message in
   `ch-alerts-prod` loses "Resolve" within a few seconds.
5. Organization → Audit log → the newest `organization.updated` shows "Buttons".
6. In Russian the section reads «Кнопки» with «Как в Организации» and «Так увидят в alerts».

`make e2e` runs these steps in `destinations.spec.ts` and `organization-settings.spec.ts`.

## Open questions

None; the outgoing webhook question is S-113's.

## Notes

- Suggested commit: `feat(web): choose the buttons of root messages per destination`.
- The preview is computed in the browser from the set and the Organization's settings, so that it follows the tick
  boxes before saving; the server's filter of S-113 is the rule, and `button-sets.test.ts` mirrors `set_test.go`.
- Depends on S-112 only to come after it in the story map, since both change the Alert handling page.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-28.FR-8 | partial | the form and the preview; the rule is S-113 |
| C-28.FR-10 | partial | the button set on the forms; the Mentions are S-143 |
| C-28.FR-11 | partial | the page on buttons; the page on Mentions is S-144 |
| C-28.AC-9 | partial | the Buttons preview; the Telegram Mentions are S-143 |
| C-13.FR-9 | partial | revised for L2: the button set on the Destination page; the Mention entries are S-143 |
| C-20.FR-1 | partial | revised for L2: the button set on Alert handling |
