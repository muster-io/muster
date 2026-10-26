---
id: S-129
title: "\"Labels in the message\" on the Route form, and the preview of the compact layout for every markup (FE)"
capability: C-29
kind: fe
layer: L2
depends_on: [S-123, S-126]
covers: [C-29.FR-2, C-29.AC-7]
files_touched:
  - web/src/components/route-policy-message.tsx
  - web/src/components/message-labels-editor.tsx
  - web/src/components/message-labels-editor.test.tsx
  - web/src/components/template-preview.tsx
  - web/src/components/template-preview.test.tsx
  - web/src/components/message-layout-preview.tsx
  - web/src/components/message-layout-preview.test.tsx
  - web/src/components/route-form.tsx
  - web/src/components/audit-diff.tsx
  - web/src/lib/docs-links.json
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/route-message.spec.ts
  - web/e2e/screenshots/routes-and-templates.spec.ts
acceptance:
  - "[C-29.FR-2, C-29.AC-7] Route → \"Message\" has \"Labels in the message\": the effective labels as removable chips, \"+ label\" with suggestions under \"From recent Alert Groups of the Route\", each with an example value, and the hint \"By default: the Group key without `alertname` and the severity label, the Team label and `namespace`. Labels without a value in the Alert Group are not shown.\"; removing or adding a label sends `own_message_labels` true with the list, and \"Reset to default\" sends `own_message_labels` false (`message-labels-editor.test.tsx`, `route-message.spec.ts`)."
  - "[C-29.AC-7] The preview of the Root message shows the compact layout for Mattermost, Slack, Telegram and Bridge — tabs that render the `layout` of `previewTemplate` as each messenger lays it out, the Bridge tab with the `layout` it carries as JSON — and the length against each messenger's limit, warning when it is over, in English and in Russian (`template-preview.test.tsx`, `message-layout-preview.test.tsx`)."
  - "[C-29.FR-2] After adding `cluster` and saving, an open Root message of the Route in the fake Mattermost server shows `cluster=…` within a few seconds (`route-message.spec.ts`); the Audit log names `/message_labels` \"Labels in the message\"."
  - "Every new text exists in English and Russian; \"Learn more\" next to \"Labels in the message\" opens Messages and templates at that section; the screenshot of the Route's message is taken again; `make e2e` passes."
verify: "make ci e2e docs-check"
operator_attention: false
issue: null
---

# S-129. "Labels in the message" on the Route form, and the preview of the compact layout for every markup (FE)

## Scope

**IN**

- "Labels in the message" in the section "Message" of the Route form, with its chips, suggestions and hint (C-29 UI).
- The preview of the Root message in Mattermost, Slack, Telegram and Bridge markup, the Bridge one with its `layout`,
  and the length against each messenger's limit (C-29 UI, C-12 UI revised).

**OUT**

- The API, the renderer and the adapters: S-125, S-126.
- The visual editor of the layout: later in L2. The messages page with the layout per messenger: S-130.

## Contracts

- **API used**: `getRoute`, `updateRoute` (`own_message_labels`, `message_labels`), `listRouteLabelSuggestions`,
  `previewTemplate` (`kind` `root_message`, `format` `markdown`, `mrkdwn`, `html` or `commonmark`, `length_limit` of
  each messenger; the result's `layout`, `length`, `messenger_limit`).
- **Components**: `message-labels-editor.tsx` — the chips, "+ label" with a combobox of the suggestions (each "{label}
  — e.g. {value}") and free text, "Reset to default" while the list is own; `message-layout-preview.tsx` — renders a
  `MessageLayout` as a Mattermost attachment (title, short fields, text, links, buttons), a Slack message (a section
  with fields, context lines, actions), a Telegram message (lines "{name}: {value}", the keyboard) and, for a Bridge,
  the `layout` as formatted JSON beside the CommonMark body; `template-preview.tsx` gains the tabs and the length line
  "{length} of {limit} characters", red above the limit. `route-policy-message.tsx` places the editor above the template
  editors; `route-form.tsx` sends the two fields.
- **Audit log**: `/message_labels` "Labels in the message", `/own_message_labels` "Own labels in the message".
- **Documentation links**: `field.message_labels` → `configure/messages-and-templates.md#labels-in-the-message` (the
  section of S-125).
- **Texts** (EN / RU), from C-29's UI and the approved mockups: "Labels in the message" / «Метки в сообщении», "+ label" /
  «+ метка», "From recent Alert Groups of the Route" / «Из недавних групп маршрута», the hint / «По умолчанию: ключ
  группировки без `alertname` и лейбла важности, лейбл команды и `namespace`. Метки без значения в группе не
  показываются.», "Reset to default" / «Как по умолчанию», "{{length}} of {{limit}} characters" / «{{length}} из
  {{limit}} символов».

## Steps

1. Write the labels editor and put it on the form. Check: `message-labels-editor.test.tsx`; `make lint`.
2. Write the layout preview and the tabs. Check: `message-layout-preview.test.tsx`, `template-preview.test.tsx`.
3. Write the end-to-end spec and take the screenshot again. Check: the Verification below; `make e2e`;
   `make docs-screenshots GUIDES="routes-and-templates"`.

## Verification

Run `make dev`, sign in as `admin@example.org` / `muster-dev-password`; from a terminal, as in S-125, create the Route
"Infrastructure" with the demo Mattermost Destination on `ch-alerts` and fire three `KubernetesContainerOOMKilled` Alerts. Then in
Playwright at 1280 × 800, in English and in Russian:

1. Routes → "Infrastructure" → "Message" → "Labels in the message" shows the chips `system`, `team`, `namespace` and the
   hint.
2. "+ label" → the suggestions list `cluster`, `pod` and others "From recent Alert Groups of the Route" with example
   values → `cluster` → "Save".
3. From a terminal, the open Root message in `ch-alerts` of the fake Mattermost server shows `cluster=` in its
   key labels within a few seconds.
4. The preview's tabs "Mattermost", "Slack", "Telegram" and "Bridge" show the title with "Firing", the fields Severity,
   Route, Teams and Started, the key labels and the line "pod:"; the Bridge tab shows the `layout` JSON; the length
   line reads "… of 4096 characters" on Telegram.
5. "Reset to default" → "Save" → the chips are `system`, `team`, `namespace` again.

`make e2e` runs these steps in `route-message.spec.ts`.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): choose the labels of root messages and preview the layout per messenger`.
- The previews render the layout in the browser from `layout`, so that one request per markup gives all four tabs the
  same content the adapters lay out; the server's length is the authority for the limit.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-29.FR-2 | partial | the form; the setting is S-125 |
| C-29.AC-7 | full | |
