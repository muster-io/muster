---
id: S-218
title: The preview on real Alert Groups in the message layout editor, with the four messengers and the test message (FE)
capability: C-38
kind: fe
layer: L2
depends_on: [S-217, S-215, S-216, S-048]
covers: [C-38.FR-11, C-38.FR-12, C-38.FR-17, C-38.AC-7, C-38.AC-8, C-38.AC-10, C-38.AC-12]
files_touched:
  - web/src/components/message-layout-preview-pane.tsx
  - web/src/components/message-layout-preview-pane.test.tsx
  - web/src/components/message-layout-group-picker.tsx
  - web/src/components/message-layout-group-picker.test.tsx
  - web/src/components/message-layout-payload.tsx
  - web/src/components/message-layout-payload.test.tsx
  - web/src/components/message-layout-test-message.tsx
  - web/src/components/message-layout-test-message.test.tsx
  - web/src/components/message-layout-preview.tsx
  - web/src/components/message-layout-preview.test.tsx
  - web/src/components/message-layout-editor.tsx
  - web/src/components/message-layout-editor.test.tsx
  - web/src/test/fixtures/message-layout.ts
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/route-message-layout-preview.spec.ts
  - web/e2e/phone-width.spec.ts
acceptance:
  - "[C-38.FR-11, C-38.AC-7] The picker \"Preview on real Alert Groups\" offers the Route's most recent Alert Groups — open ones first, then resolved ones, each newest first, at most 20 (`message.layout_preview_groups`) — each with its number, title, status and number of Alerts, from two calls of `listAlertGroups` filtered by the Route; \"Find by number\" finds an Alert Group of any Route and renders it as if this Route had taken it; a Route with no Alert Group renders the example marked \"Example\" until one is found by number; the first offered one is shown by default (`message-layout-group-picker.test.tsx`, `route-message-layout-preview.spec.ts`)."
  - "[C-38.FR-12, C-38.AC-10] One tab per messenger — Mattermost, Slack, Telegram, Bridge — draws the message as the messenger draws it, from the `layout` and the payload of `previewTemplate` for the unsaved draft, with \"Show the payload\" revealing the exact payload (the Mattermost attachment, the Slack blocks, the Telegram HTML with the inline keyboard, the Bridge body with its `layout` and `buttons`) in a box that scrolls inside itself; the preview follows each edit of the draft without saving and the language and \"Buttons as in\" (the Route's Destinations and \"The Organization's set\") change the render (`message-layout-preview-pane.test.tsx`, `message-layout-payload.test.tsx`, `route-message-layout-preview.spec.ts`)."
  - "[C-38.FR-12, C-38.AC-8] Each tab shows the length against its limit — \"Length: 812 / 4,096 (Telegram)\", \"Blocks: 9 / 50 (Slack)\", the 16,383 of Mattermost, and for a Bridge the Destination's `max_body_length` or none — and, when the message is over it, \"Shortened: the description and 4 Alerts are cut\" from `cut`, the description first with it shown and the Alert list first with it hidden (`message-layout-preview.test.tsx`, `route-message-layout-preview.spec.ts`)."
  - "[C-38.FR-17, C-38.AC-12] \"Send test message\" with a chosen Destination of the Route calls `testDestination` with the unsaved layout, the shown Alert Group and the Route, and shows the steps and \"Sent to {Destination}\"; it is available only with `destinations:test`, and a failed step shows its class; the Route's open Root messages are not edited and the saved layout is unchanged (`message-layout-test-message.test.tsx`, `route-message-layout-preview.spec.ts`)."
  - "[C-38.AC-10] In a real browser, in English and in Russian, the Admin switches the preview between two Alert Groups and the four tabs, edits the draft and sees the preview change before saving, applies the layout and sees the Mattermost message in the fake server change; at 360 px the preview follows the blocks, the page has no horizontal scrolling and the payload scrolls inside its own box (`route-message-layout-preview.spec.ts`, `phone-width.spec.ts`)."
  - "[C-38.AC-10] Every new text exists in English and Russian, in the Route's language where it is a message text, the missing-key check of `make ci` passes (`message-layout-editor.test.tsx`)."
verify: "make ci e2e-web"
operator_attention: false
issue: null
---

# S-218. The preview on real Alert Groups in the message layout editor, with the four messengers and the test message (FE)

## Scope

**IN**

- The right side of the editor: the Alert Group picker with "Find by number", the language, "Buttons as in", the four
  messenger tabs with the payload and the length line, the cut blocks, and "Send test message" (C-38 UI, FR-11,
  FR-12, FR-17).

**OUT**

- The blocks, the fields, the save and the read-only form: S-217. The API: S-215, S-216.
- The Redaction marks in the preview: they come with the capability that specifies the rules (S-215, Open questions).

## Contracts

- **API used**: `previewTemplate` (`kind` `root_message`, the unsaved `message_layout`, `route_id`, `alert_group_id`,
  `destination_id`, `language`, `format` of the tab; the result's `layout`, `payload`, `length`, `messenger_limit`,
  `slack_block_count`, `cut`, `sample`), `listAlertGroups` (`route`, `status`, `number`, `limit`), `getRoute` and the
  Route's Destinations (`listDestinations`) for "Buttons as in", `testDestination` (S-216).
- **Components**: `message-layout-group-picker.tsx` — the picker as a select with the number, title, status and Alerts
  of each offer, two list calls (open statuses, then `resolved`), merged and cut at 20, and "Find by number" with the
  `number` filter without the `route` filter; `message-layout-preview-pane.tsx` — the right side: it calls
  `previewTemplate` for the editor's draft with a short debounce and shows the tabs; `message-layout-preview.tsx`
  (S-129) draws a `MessageLayout` as each messenger lays it out and now also the buttons from the payload and the
  Slack block count; `message-layout-payload.tsx` — "Show the payload": the attachment, blocks, HTML with keyboard or
  body with `layout` and `buttons`, as text in a box with its own scroll, never as markup; `message-layout-test-message.tsx`
  — "Send test message": the Destination select, the steps of the result, shown only with `destinations:test`;
  `message-layout-editor.tsx` (S-217) mounts the pane to the right of the blocks and below them at 360 px.
- **Texts** (EN / RU): "Preview on real Alert Groups" / «Превью на реальных группах алертов»; "Find by number" / «Найти
  по номеру»; "Example" / «Пример»; "Buttons as in" / «Кнопки как в»; "The Organization's set" / «Набор организации»;
  "Show the payload" / «Показать содержимое»; "Length: {n} / {limit} ({messenger})" / «Длина: {n} / {limit}
  ({messenger})»; "Blocks: {n} / {limit} (Slack)" / «Блоки: {n} / {limit} (Slack)»; "Shortened: the description and
  {n} Alerts are cut" / «Сокращено: отрезано описание и {n} алертов», with Russian plurals; "Send test message" /
  «Отправить тестовое сообщение»; "Sent to {Destination}" / «Отправлено в {Destination}»; the tab names are the
  messengers' own.
- **Permissions**: `templates:preview` for the pane (without it the pane says so and the editor still works);
  `destinations:test` for the test message.
- **Fixtures**: the builders of S-217 are extended with a `previewTemplate` result per messenger and a list of Alert
  Groups.

## Steps

1. Write the picker with its two calls and "Find by number". Check: `message-layout-group-picker.test.tsx` (the order,
   the cut at 20, the example on an empty Route).
2. Write the payload box and extend the layout preview with the buttons and the block count. Check:
   `message-layout-payload.test.tsx`, `message-layout-preview.test.tsx` (the length lines, "Shortened…").
3. Write the pane and the test message and mount them in the editor. Check: `message-layout-preview-pane.test.tsx`,
   `message-layout-test-message.test.tsx`, `message-layout-editor.test.tsx`.
4. Write `route-message-layout-preview.spec.ts` and extend `phone-width.spec.ts`. Check: the Verification below.

## Verification

Playwright against `make dev` (as `web/e2e/support.ts` sets it up), in English and then Russian, with the Route
"Infrastructure" with two Alert Groups — #… `KubernetesContainerOOMKilled` firing and #… `NodeDiskPressure`
acknowledged with twelve Alerts — and one resolved, and the Route "Certificates" with none.

1. As the Admin, open the editor of "Infrastructure". Expect the picker to list the two open Alert Groups, then the
   resolved one, each with its number, title, status and number of Alerts, and the first shown in the Mattermost tab.
2. Add the field "Firing / total" and "cluster"; switch the picker to `NodeDiskPressure`. Expect both fields in the grid,
   the Alert list ending with "…and 7 more" and the footer "Acknowledged by @…"; the Slack tab "Blocks: 9 / 50 (Slack)",
   the Telegram tab "Length: … / 4,096 (Telegram)", the Bridge tab with the `layout` and `buttons`; "Show the payload"
   shows the exact payload in a box that scrolls by itself.
3. Turn "Description" on and make the message long (twelve Alerts, a long description): expect "Shortened: the description
   … are cut"; turn the description off: expect the Alert list reported first. Nothing is saved: `curl -s -H "$A"
   $API/routes/$R | jq .own_message_layout` is unchanged.
4. "Find by number" with the number of an Alert Group of another Route: expect it rendered in this Route's layout and
   language. Open "Certificates": expect the example marked "Example".
5. "Buttons as in" → the Telegram Destination, then "The Organization's set": expect the keyboard to follow. Choose the
   language Russian: expect the message texts in Russian.
6. "Send test message" to the Mattermost Destination: expect "Sent to …", and in the fake Mattermost server one "🧪 Test
   message" with the unsaved layout (`curl -s $FMM/posts | jq …`, as in S-216) and the open Root messages not edited
   (`curl -s $FMM/requests | jq '[.[] | select(.method=="PUT")] | length'` unchanged). As a Viewer without
   `destinations:test`: no button.
7. "Apply to the Route": expect the open Root message in the fake Mattermost server to change within a few seconds. At
   360 px: no horizontal scrolling, the preview below the blocks. Switch to Russian: the Russian texts, «Длина: …».

## Open questions

None.

## Notes

- Suggested commit: `feat(web): preview a message layout on real alert groups in every messenger from the editor`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases.
- The preview and the Destination test are two calls on purpose: the first draws, the second proves what the messenger
  itself does with the layout (C-38.FR-17), and only the second needs `destinations:test`.
- The pane never stores what it previews; an Alert Group found by number keeps its own number and title in the picker
  while its message is laid out as this Route would.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-38.FR-11 | partial | the picker, "Find by number" and the example; the sample is S-215 |
| C-38.FR-12 | partial | the tabs, the payload box, the length and the cut; the result is S-215 |
| C-38.FR-17 | partial | the button and its Destination; the operation is S-216 |
| C-38.AC-7 | partial | the offered order and the example in the UI; the preview's `403` is S-215, the Viewer's read S-210 |
| C-38.AC-8 | partial | the length and cut lines in the UI; the reports are S-215 |
| C-38.AC-10 | partial | the preview, its tabs and the apply; the blocks, fields, keyboard move and reset are S-217 |
| C-38.AC-12 | partial | the editor's test message; the operation is S-216, the one save S-210 |
