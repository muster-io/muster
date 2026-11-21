---
id: S-217
title: The message layout editor page with the blocks, the fields and the Route's templates (FE)
capability: C-38
kind: fe
layer: L2
depends_on: [S-210, S-211, S-129, S-123]
covers: [C-38.FR-2, C-38.FR-5, C-38.FR-9, C-38.FR-15, C-38.AC-10]
files_touched:
  - web/src/routes/routes.$routeId_.message-layout.tsx
  - web/src/components/message-layout-editor.tsx
  - web/src/components/message-layout-editor.test.tsx
  - web/src/components/message-layout-blocks.tsx
  - web/src/components/message-layout-blocks.test.tsx
  - web/src/components/message-layout-fields.tsx
  - web/src/components/message-layout-fields.test.tsx
  - web/src/lib/message-layout.ts
  - web/src/lib/message-layout.test.ts
  - web/src/components/route-policy-message.tsx
  - web/src/components/audit-diff.tsx
  - web/src/components/audit-diff.test.tsx
  - web/src/test/fixtures/message-layout.ts
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/route-message-layout.spec.ts
  - web/e2e/phone-width.spec.ts
acceptance:
  - "[C-38.FR-2, C-38.AC-10] Route → \"Message\" has \"Message layout\", which opens a page of its own, \"Message layout · Route {name}\", with the hint \"Applies to every Destination of the Route\"; on its left \"Blocks from top to bottom\" lists the fixed title \"#N title and the status as a word\", the four movable blocks — Fields, Text, Key labels, Alert list — each with a switch and a handle, and the fixed blocks \"Notices\", \"Assignees / Acknowledged by\", \"Buttons — the set of each Destination\" and \"Links\" with \"Open in Muster\" always on and the switch \"Other links\" (`message-layout-blocks.test.tsx`, `route-message-layout.spec.ts`)."
  - "[C-38.FR-2, C-38.AC-10] In a real browser the Admin adds the field \"Firing / total\" and the label `cluster` from \"+ field\", which offers the catalogue and the labels of recent Alert Groups of the Route with an example value; removes a chip; moves the Alert list up with \"Move up\" by keyboard and by the drag handle; hides the description; turns \"Other links\" off; each field appears once and at most 10 are accepted, the 11th \"+ field\" being unavailable with the limit named; and \"Apply to the Route\" saves it, the Route's ETag in `If-Match` (`message-layout-fields.test.tsx`, `message-layout-blocks.test.tsx`, `route-message-layout.spec.ts`)."
  - "[C-38.FR-2] Under Fields the hint \"In Mattermost the fields always show under the text\" is shown; under Key labels \"Labels in the message\" are the editor of S-129 in place, saving through `own_message_labels` and not duplicated; under Alert list \"Up to {N} values\" accepts 1 to 20 and refuses 0 and 21 at the field before a save, showing the server's `out_of_range` or `too_long` there if it refuses anyway (`message-layout-editor.test.tsx`, `route-message-layout.spec.ts`)."
  - "[C-38.FR-5, C-38.AC-10] \"Reset to default\" sends `own_message_layout` false and the page shows Muster's default read back, the layout of C-29; \"Labels in the message\" keep their own reset; leaving with unsaved changes asks for confirmation; a refused save (`412`) shows \"Reload the Route\", which drops the unsaved layout (`message-layout-editor.test.tsx`, `route-message-layout.spec.ts`)."
  - "[C-38.FR-9] On a Route with a Root message template the blocks it renders — Text, Key labels, Alert list — show \"Rendered by the Route's template\" with a link to the template's editor, the fields, their order and \"Other links\" still apply, and \"Remove the template\" saves the layout and the removal of the template in one `updateRoute` (`message-layout-editor.test.tsx`, `route-message-layout.spec.ts`)."
  - "[C-38.FR-15] A User with `routes:read` and not `routes:write` sees the layout read-only with the same blocks and no switch, handle, \"Apply to the Route\" or \"Reset to default\"; a route whose own layout was saved reads `own_message_layout` true, and one that follows Muster's default shows \"Muster's default\" (`message-layout-editor.test.tsx`, `route-message-layout.spec.ts`)."
  - "[C-38.AC-10] The Audit log shows the change as `/message_layout` \"Message layout\" with a before and after diff, in English and in Russian; every new text exists in English and Russian; at 360 px the page has no horizontal scrolling and the blocks fill the width (`audit-diff.test.tsx`, `route-message-layout.spec.ts`, `phone-width.spec.ts`, the missing-key check of `make ci`)."
verify: "make ci e2e-web"
operator_attention: false
issue: null
---

# S-217. The message layout editor page with the blocks, the fields and the Route's templates (FE)

## Scope

**IN**

- The page of the editor under the Route, its left side — the blocks, the fields, "Labels in the message" in place,
  the Alert list, the other links, the Route's templates — with "Apply to the Route" and "Reset to default"
  (C-38 UI, FR-2, FR-5, FR-9, FR-15).
- Draft handling: the leave confirmation, the `412` and "Reload the Route", and the read-only form.

**OUT**

- The right side, "Preview on real Alert Groups", the messenger tabs and "Send test message": S-218. This story leaves
  the place for the preview empty on a wide screen and below the blocks at 360 px, so that S-218 mounts it without
  moving the blocks.
- The API: S-210 to S-216. The documentation and its links: S-219.

## Contracts

- **API used**: `getRoute`, `updateRoute` (`If-Match`; `own_message_layout`, `message_layout`, `own_message_labels`,
  `policy.templates.root_message`), `listRouteLabelSuggestions` (the labels with an example value), `listAuditLog`. The
  generated client carries `RouteMessageLayout` (S-210); `message_layout` is read as the effective layout and written
  only with `own_message_layout` true.
- **Route and page**: `web/src/routes/routes.$routeId_.message-layout.tsx` is the page at `/routes/{id}/message-layout`;
  the trailing underscore keeps it out of the layout of `routes.$routeId.tsx`, which is a page of its own and not a
  parent (as `integrations.$integrationId.edit.tsx` is a sibling of its index). The route tree is generated, never
  listed. `route-policy-message.tsx` gets the "Message layout" link in the section "Message".
- **Components**: `message-layout-editor.tsx` — the page's form: the draft, `dirty`, the save with `If-Match`, the
  leave guard (the router's blocker plus the browser's `beforeunload`), `412` handling; `message-layout-blocks.tsx` —
  the list of the four movable blocks and the fixed ones, each movable block a row with a switch, a drag handle and
  "Move up" and "Move down" buttons, with the order announced to assistive technology; dragging uses the
  browser's drag events, with no new dependency (see Notes); `message-layout-fields.tsx` — the chips in their order,
  "+ field" as a disclosure panel offering the catalogue and the suggested labels, the limit
  `message.layout_fields` shown from the count; `web/src/lib/message-layout.ts` — the built-in constants of
  `message.layout_fields` (10) and of `route.message_layout_alerts_listed` (1 to 20), the rows of `defaults.md` that
  S-210 takes as built-in values too, so that "+ field" is unavailable at the eleventh and "Up to {N}" is checked
  before the save, the server's `too_long` and `out_of_range` still shown at the field when a stale build disagrees;
  the draft, conversions to and from `RouteMessageLayout`, the catalogue, the move and toggle operations, shared by
  S-218; `audit-diff.tsx` — the label of `/message_layout`.
- **Texts** (EN / RU; `en.json` and `ru.json` are the real files; a missing key fails CI): "Message layout" / «Макет
  сообщения»; "Message layout · Route {name}" / «Макет сообщения · маршрут {name}»; "Applies to every Destination of the
  Route" / «Применяется ко всем местам доставки маршрута»; "Apply to the Route" / «Применить к маршруту»; "Reset to
  default" / «Сбросить к умолчанию»; "Blocks from top to bottom" / «Блоки сверху вниз»; "Fields" / «Поля»; "Text" /
  «Текст»; "Key labels" / «Ключевые метки»; "Alert list" / «Список алертов»; "Move up" / «Выше»; "Move down" / «Ниже»;
  "+ field" / «+ поле»; "In Mattermost the fields always show under the text" / «В Mattermost поля всегда показываются
  под текстом»; "Up to {N} values" / «До {N} значений»; "Other links" / «Прочие ссылки»; "Rendered by the Route's
  template" / «Рендерится шаблоном маршрута»; "Remove the template" / «Убрать шаблон»;
  "Reload the Route" / «Перезагрузить
  маршрут»; the field names of the catalogue, whose names in messages are built-in texts in the Route's language
  (C-12.FR-3). Russian plurals are written for "{N} values".
- **Permissions**: `routes:write` edits, `routes:read` reads; the fixed blocks are static text with no switch.
- **Fixtures**: `web/src/test/fixtures/message-layout.ts` — builders of `RouteMessageLayout` and of a Route with and
  without its own layout, used by S-218's tests too.

## Steps

1. Write `web/src/lib/message-layout.ts` with the draft and its operations. Check:
   `message-layout.test.ts` (move, toggle,
   add a field twice, the limit).
2. Write the blocks and the fields components. Check: `message-layout-blocks.test.tsx` (keyboard move, drag, switches),
   `message-layout-fields.test.tsx`.
3. Write the editor, the page and the link from the Route's "Message" section; add the Audit log label and the texts.
   Check: `message-layout-editor.test.tsx` (save, reset, `412`, leave guard, read-only, template hint),
   `audit-diff.test.tsx`; `make ci` (missing keys).
4. Write `route-message-layout.spec.ts` and add the page to `phone-width.spec.ts`. Check: the Verification below.

## Verification

Playwright against `make dev` (as `web/e2e/support.ts` sets it up), in English and then Russian, with the Route
"Infrastructure" and its Alert Group #… `KubernetesContainerOOMKilled` of S-125's setup.

1. Sign in as the Admin, open Routes → Infrastructure → "Message" → "Message layout". Expect the header
   "Message layout ·
   Route Infrastructure", the hint "Applies to every Destination of the Route", the blocks Fields (Severity, Route,
   Teams, Started as chips), Text, Key labels, Alert list in this order, and the fixed ones.
2. "+ field" → "Firing / total", then "+ field" → the label `cluster` (offered with "e.g. prod"). Expect two more chips.
   Focus "Alert list" and press "Move up" twice; expect it above Key labels, then above Text. Turn "Description" off and
   "Other links" off.
3. "Apply to the Route". Expect the page to stay with no unsaved state; `curl -s -H "$A" $API/routes/$R | jq -c
   '{o: .own_message_layout, order: .message_layout.order, d: .message_layout.show_description}'` answers
   `{"o":true,"order":["fields","alerts","text","labels"],"d":false}`, and the open Root message in the fake Mattermost
   server shows the new fields within a few seconds (S-211).
4. Add 7 more fields: expect "+ field" unavailable at 10 with the limit named. Set "Up to {N} values" to 21: expect the
   refusal at the field before any save ("Apply to the Route" unavailable); set 3: expect three values and
   "…and 9 more" for twelve Alerts.
5. Save the Route in another tab, then "Apply to the Route" here: expect `412` as "Reload the Route"; press it,
   expect the
   draft dropped and the other tab's layout shown. Edit and navigate away: expect the confirmation.
6. "Reset to default": expect "Muster's default" with the four fields of C-29 and `own_message_layout` false in the API;
   "Labels in the message" unchanged.
7. On a Route with a Root message template: expect "Rendered by the Route's template" on Text, Key labels and Alert list
   with a link to the template editor, and "Remove the template"; press it, then "Apply to the Route": one Audit log row
   `route.updated` naming `/message_layout` and `/policy/templates/root_message`.
8. Sign in as a Viewer: expect the same blocks read-only, no handles or buttons. At 360 px: no horizontal scrolling, the
   blocks full width. Switch to Russian: the Russian texts, and the Audit log row "Макет сообщения".

## Open questions

None.

## Notes

- Suggested commit: `feat(web): add the message layout editor of a route with its blocks and fields`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases.
- **Dragging.** The handles use the browser's drag events and "Move up" and "Move down" serve the keyboard and touch,
  as C-38 UI asks, so no drag-and-drop library is added; a library would need the licence statement of ADR-0001 and
  would not remove the buttons. If the review wants smoother touch dragging, the dependency is proposed in its own pull
  request.
- Until S-218 merges the page has no preview: an Admin sees the effect in the messenger, which is true to the order of
  delivery.
- The design pass of the UI belongs to the later layers (journal D315); this page uses the existing components.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-38.FR-2 | partial | the editor's blocks and fields; the checks are S-210, the rendering S-211, S-212 |
| C-38.FR-5 | partial | the reset and the dropped draft; the API is S-210 |
| C-38.FR-9 | partial | the hint and "Remove the template"; the one save is S-210 |
| C-38.FR-15 | partial | the read-only editor; the API is S-210 |
| C-38.AC-10 | partial | the blocks, fields, keyboard move, apply, reset and 360 px; switching the preview between Alert Groups and the four tabs is S-218 |
