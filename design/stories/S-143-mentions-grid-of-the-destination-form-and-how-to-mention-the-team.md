---
id: S-143
title: The Mentions grid of the Destination form with its warnings and What each messenger supports, and How to mention the Team (FE)
capability: C-28
kind: fe
layer: L2
depends_on: [S-114, S-119, S-140, S-165]
covers: [C-28.FR-1, C-28.FR-2, C-28.FR-3, C-28.FR-10, C-28.AC-9, C-25.FR-8, C-25.AC-10, C-12.FR-3, C-12.FR-8, C-13.FR-9]
files_touched:
  - web/src/components/destination-form.tsx
  - web/src/components/destination-form.test.tsx
  - web/src/components/mention-entries-editor.tsx
  - web/src/components/mention-entries-editor.test.tsx
  - web/src/components/mention-target-picker.tsx
  - web/src/components/mention-support-table.tsx
  - web/src/components/mention-support-table.test.tsx
  - web/src/components/mention-warnings.tsx
  - web/src/components/mention-settings.tsx
  - web/src/components/mention-settings.test.tsx
  - web/src/lib/mention-entries.ts
  - web/src/lib/mention-entries.test.ts
  - web/src/components/team-mentions-editor.tsx
  - web/src/components/team-mentions-editor.test.tsx
  - web/src/routes/teams.$teamId.tsx
  - web/src/routes/teams.index.tsx
  - web/src/components/timeline.tsx
  - web/src/components/timeline.test.tsx
  - web/src/components/connection-check.tsx
  - web/src/components/audit-diff.tsx
  - web/src/lib/docs-links.json
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - design/prd/l1/reference.md
  - web/e2e/destinations.spec.ts
  - web/e2e/telegram-destination.spec.ts
  - web/e2e/teams.spec.ts
  - web/e2e/alert-group-page.spec.ts
  - web/e2e/docs-links-forms.spec.ts
acceptance:
  - "[C-28.FR-1, C-28.FR-10, C-12.FR-8, C-13.FR-9] The Destination form of every type has the section \"Mentions\" — \"Whom to mention and on which Loud events. A new entry is ticked for every event.\" — with one row per entry, its target and under it what it resolves to now (\"now: @ivan\", \"payments — @payments_team; devops and search — nobody\"), a tick box per Loud event (\"New Alert Group\", \"New Alerts\", \"Reopen\", \"Ack timeout\", \"End of Snooze\", \"Rise to critical\", \"Start of a Storm\") and \"Remove\"; \"Add\" offers `@channel`, `@here`, `@all`, `@everyone`, \"Group…\", \"Person…\", \"Team…\", \"Team from the label\", \"On-call of a Team…\" and \"On-call of the Team from the label\", a new row ticked for every event; with no entry it reads \"Nobody is mentioned: messages arrive without Mentions.\"; the three notes of C-28's UI are under the grid (`mention-entries-editor.test.tsx`, `destinations.spec.ts`)."
  - "[C-28.FR-2, C-28.AC-9] In a real browser the form of a Telegram Destination shows `@channel`, \"Group…\" and the Team targets unavailable with \"Telegram cannot mention everyone or groups\", a Slack one shows groups and Teams unavailable with \"Needs the scope usergroups:read\" until its Connection's check found the scope, a Bridge one \"The bridge declares no group mentions\"; the table \"What each messenger supports\" shows the table of C-28.FR-2; and the Buttons preview follows the tick boxes, in English and in Russian (`mention-support-table.test.tsx`, `telegram-destination.spec.ts`)."
  - "[C-28.FR-3, C-12.FR-3] Above the grid the Destination's warnings read \"Mention not delivered: @bob, On-call of the Team from the label, has no linked Mattermost account\" or \"… the bridge cannot mention people\"; the Alert Group's Timeline shows the delivery event \"Mention not delivered\" with the person, the entry, the event and the reason (`timeline.test.tsx`, `alert-group-page.spec.ts`)."
  - "[C-25.FR-8, C-25.AC-10] The Team page's \"How to mention the Team\" has one field per Connection — the Mattermost group's name, the Slack user group picked from the Connection's groups, the bridge's group name — or the reason it has none (\"needs the scope usergroups:read\", \"the bridge declares no group mentions\", \"Telegram cannot mention everyone or groups\"), with the hint \"The entry 'Team' of a Destination's Mentions is this Team mention. Without one, nobody is mentioned, and the On-call is not mentioned instead.\"; the Teams list shows each Team's Team mentions or \"no Team mention\", in English and in Russian (`team-mentions-editor.test.tsx`, `teams.spec.ts`)."
  - "[C-28.FR-2] When a Connection loses an ability later — a Slack Connection whose check no longer finds `usergroups:read` — its Destination's user group entry stays, is shown greyed with \"Needs the scope usergroups:read\" and mentions nobody, and the Destination page warns about it above the grid (`mention-entries-editor.test.tsx`, `destinations.spec.ts`)."
  - "[C-28.FR-2] The check of a Bridge Connection lists its group mentions and the mentions of everyone in the chat among the features of `info` (`destinations.spec.ts`)."
  - "Every new text exists in English and Russian; \"Mentions\" has \"Learn more\" to Concepts → Whom a message mentions and \"How to mention the Team\" to Concepts → Teams, section \"Team mentions\"; `make e2e` passes."
verify: "make ci e2e"
operator_attention: false
issue: null
---

# S-143. The Mentions grid of the Destination form with its warnings and What each messenger supports, and How to mention the Team (FE)

## Scope

**IN**

- The section "Mentions" of the Destination form of every type (C-28 UI, C-28.FR-10, C-13.FR-9 revised): the grid of
  entries with their Loud events and what each resolves to now, "Add" with every target and the unavailable ones greyed
  with their reason, the notes, the warnings of undelivered Mentions, and the table "What each messenger supports".
- "How to mention the Team" on the Team page and the Team mentions on the Teams list (C-25 UI).
- The delivery event "Mention not delivered" in the Timeline, and the new features of a bridge's `info` in its check.

**OUT**

- The API: S-139, S-140, S-165. The Buttons section and its preview: S-114. The documentation: S-144.

## Contracts

- **API used**: `getDestination`, `createDestination`, `updateDestination` (`mention_entries`; the reads'
  `mention_entries` with `available`, `unavailable_reason` and `resolves_to`, and `mention_warnings`), `getTeam`,
  `updateTeam` (`team_mentions`; `mention_connections`), `listConnectionGroups` (the pickers of "Group…" on Slack and
  Mattermost and of the Slack Team mention), `listTeams`, `listUserDirectory` ("Person…"), `getConnection` (the Slack
  scope, the bridge's `info`), `getAlertGroupTimeline` (`mention_not_delivered`), `checkConnection`.
- **Components**: `mention-entries-editor.tsx` — the grid, with `mention-target-picker.tsx` for "Add" and
  `lib/mention-entries.ts` for what each Destination type offers and the text of each `MentionUnavailableReason` (S-139)
  — the server's `available` and `unavailable_reason` decide, the form only words them; `mention-support-table.tsx` —
  "What each messenger supports"; `mention-warnings.tsx` — the warnings above the grid; `mention-settings.tsx` keeps
  only `defaultMentions`, the L1 form that reads stay with until 0.3.0, which the existing test fixtures use, and loses
  its editor; `team-mentions-editor.tsx` — "How to mention the Team"; `timeline.tsx` — the delivery event with its
  person, entry, event and reason; `connection-check.tsx` — "Group mentions" and "Mentions of everyone: {tokens}" among
  the bridge's features.
- **Documentation links** (`docs-links.json`): `field.mentions` moves to `concepts/mentions.md#mention-entries`,
  `field.mention_support` → `concepts/mentions.md#what-each-messenger-supports`, `field.team_mentions` →
  `concepts/teams.md#team-mentions`; the anchors exist from S-139 and S-140.
- **Audit log names** (`audit-diff.tsx`): `/mention_entries` "Mentions", `/team_mentions` "How to mention the Team".
- **Texts** (EN / RU), from C-28's and C-25's UI, reference.md and the approved mockups: "Mentions" / «Упоминания»,
  "Whom to mention and on which Loud events. A new entry is ticked for every event." / «Кого упоминать и на каких
  громких событиях. Новая строка отмечена на всех событиях.», the seven event columns / «Новая группа», «Новые алерты»,
  «Переоткрытие», «Нет подтверждения», «Конец отложения», «Рост до critical», «Начало шторма», "Remove" / «Убрать»,
  "Add" / «Добавить», "Group…" / «Группа…», "Person…" / «Человек…», "Team…" / «Команда…», "Team from the label" /
  «Команда из метки», "On-call of a Team…" / «Дежурный команды…», "On-call of the Team from the label" / «Дежурный
  команды из метки», "now: {{who}}" / «сейчас: {{who}}», "Nobody is mentioned: messages arrive without Mentions." /
  «Никого не упоминаем: сообщения приходят без упоминаний.», the notes / «„Команда“ — это упоминание команды для этого
  подключения, если оно задано; иначе никого. Дежурного вместо команды не зовём.», «Человека без привязки не упоминаем;
  об этом скажут хронология группы и эта страница.», «Напоминания и автоматическая отмена подтверждения всегда упоминают
  владельца, а перехват — прежнего владельца.», the warnings of reference.md / «Упоминание не доставлено: @{{login}},
  {{entry}}, нет привязки к {{messenger}}», «… мост не умеет упоминать людей», "What each messenger supports" /
  «Поддержка мессенджерами», the reasons of reference.md / «Нужно право usergroups:read», «Мост не объявил групповые
  упоминания», «В Telegram нет упоминания всех и групп», "How to mention the Team" / «Как упоминать команду», its hint /
  «Строка „Команда“ в упоминаниях места доставки — это упоминание команды для этого подключения. Если его нет, команду
  не упоминают и дежурного вместо неё не зовут.», "no Team mention" / «упоминания нет».

## Steps

1. Write what each type offers, the grid and the picker. Check: `mention-entries.test.ts`,
   `mention-entries-editor.test.tsx`.
2. Write the table, the warnings and the Timeline's event; shrink `mention-settings.tsx`. Check:
   `mention-support-table.test.tsx`, `timeline.test.tsx`, `mention-settings.test.tsx`, `destination-form.test.tsx`.
3. Write "How to mention the Team" and the Teams list's column, and the features of the bridge check. Check:
   `team-mentions-editor.test.tsx`; `make lint`.
4. Write the end-to-end specs. Check: the Verification below; `make e2e`.

## Verification

Run `make dev`; from a terminal set up, as in S-139, S-140 and S-165, the Team "payments" with the Team mention
`payments_team` for the demo Mattermost Connection, the Team "search" without one, the Destination `infra-notice` with
the entry "On-call of the Team from the label", payments' main Schedule with bob On-call without a Mattermost Account
link, and an Alert Group of `team="payments"` delivered there. Then in Playwright at 1280 × 800, in English and in
Russian:

1. Destinations → `infra-notice` → "Mentions": the row "On-call of the Team from the label" shows "now: payments —
   @bob; …" and seven ticked events; above the grid "Mention not delivered: @bob, On-call of the Team from the label,
   has no linked Mattermost account".
2. "Add" → `@here` → a new row with every event ticked; untick all but "New Alert Group" → "Save" → saved.
3. Destinations → the demo Telegram Destination → "Add": `@channel`, `@here`, `@all`, `@everyone`, "Group…", "Team…"
   and "Team from the label" are greyed with "Telegram cannot mention everyone or groups"; "On-call of a Team…" →
   "payments" → "Save" → saved; "What each messenger supports" shows the table; "Buttons" → "Own for this Destination"
   → untick "Resolve" → the preview drops "Resolve".
4. Teams → "payments" → "How to mention the Team": Mattermost `payments_team`; Slack "needs the scope
   usergroups:read"; Telegram "Telegram cannot mention everyone or groups"; the hint as above; Teams list → "payments"
   shows "@payments_team", "search" "no Team mention".
5. The Alert Group's page → Timeline → "Mention not delivered" with @bob, the entry, "New Alert Group" and the reason.
6. From a terminal give the demo Slack app `usergroups:read` on the fake Slack server and check its Connection, as in
   S-139; Destinations → the demo Slack Destination → "Add" → "Group…" → `payments-team` → "Save"; remove the scope
   on the fake server and check the Connection again; reload → the row is greyed with "Needs the scope
   usergroups:read", and a warning above the grid names the entry and the reason.
7. Connections → "Example Bridge" with group mentions declared on the fake bridge → "Check" → the features list "Group
   mentions".

`make e2e` runs these steps in `destinations.spec.ts`, `telegram-destination.spec.ts`, `teams.spec.ts` and
`alert-group-page.spec.ts`; `docs-links-forms.spec.ts` checks the new links.

## Open questions

None open. Settled:

1. **Settled: the texts of the other unavailable targets go into reference.md in this pull request**, in the row "Team
   page, Team mention · Destination form, an unavailable Mention target": "{messenger} has no {token}" / «В {messenger}
   нет {token}» for `messenger_has_no_token` (`@everyone` on Mattermost, `@all` on Slack), and "The bridge does not
   declare {token}" / «Мост не объявил {token}» for `bridge_not_declared`. S-138 may add more reasons and texts.

## Notes

- Suggested commit: `feat(web): edit mention entries per loud event and how each team is mentioned`.
- "What it resolves to now" comes from the server (`resolves_to`), so the form shows the On-call of the moment and the
  Team mentions without computing them; it is recomputed on each read.
- The Destination mockup writes "Page not delivered" into the Thread for an undelivered Mention; the form follows
  C-28.FR-3 instead (L2.md §5.2).

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-28.FR-1 | partial | the grid; the API is S-140 |
| C-28.FR-2 | partial | the unavailable targets and the table on the form |
| C-28.FR-3 | partial | the warnings and the Timeline in the UI |
| C-28.FR-10 | partial | the Mentions on the Destination form |
| C-28.AC-9 | partial | the Telegram Destination's form; the Buttons preview is S-114's, rechecked here |
| C-25.FR-8 | partial | "How to mention the Team" |
| C-25.AC-10 | partial | the Team mention field of each Connection or its reason |
| C-12.FR-3 | partial | revised for L2: the texts of undelivered Mentions in both languages |
| C-12.FR-8 | partial | revised for L2: the entries on the form |
| C-13.FR-9 | partial | revised for L2: the Mention entries on the Destination page |
