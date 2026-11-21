---
id: S-207
title: The Attach dialog, the notice and the section on the Alert Group page, and the mark, filter and bulk Attach of the list (FE)
capability: C-37
kind: fe
layer: L2
depends_on: [S-202, S-203, S-204, S-206, S-159, S-158]
covers: [C-37.FR-1, C-37.FR-12, C-37.FR-13, C-37.FR-14, C-37.AC-12, C-09.FR-13, C-09.FR-14]
files_touched:
  - web/src/routes/alert-groups.$alertGroupId.tsx
  - web/src/routes/alert-groups.index.tsx
  - web/src/components/attach-dialog.tsx
  - web/src/components/attach-dialog.test.tsx
  - web/src/components/attached-alert-groups.tsx
  - web/src/components/attached-alert-groups.test.tsx
  - web/src/components/alert-group-notices.tsx
  - web/src/components/alert-group-notices.test.tsx
  - web/src/components/alert-group-header.tsx
  - web/src/components/alert-group-header.test.tsx
  - web/src/components/command-buttons.tsx
  - web/src/components/command-buttons.test.tsx
  - web/src/components/alert-group-alerts.tsx
  - web/src/components/alert-group-table.tsx
  - web/src/components/alert-group-filters.tsx
  - web/src/components/alert-group-filters.test.tsx
  - web/src/components/bulk-actions-bar.tsx
  - web/src/components/bulk-actions-bar.test.tsx
  - web/src/components/bulk-result-dialog.tsx
  - web/src/components/timeline.tsx
  - web/src/components/timeline.test.tsx
  - web/src/lib/alert-group-search.ts
  - web/src/lib/commands.ts
  - web/src/test/fixtures/alert-groups.ts
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/support.ts
  - web/e2e/attach-detach.spec.ts
  - web/e2e/alert-group-list.spec.ts
  - web/e2e/phone-width.spec.ts
  - design/prd/l1/reference.md
acceptance:
  - "[C-37.FR-1, C-37.FR-12, C-37.AC-12] In a real browser, Ivan opens a critical Alert Group, presses \"Attach to #…\" and gets the dialog \"Attach #{N} to the main Alert Group\" with the search \"Number or title\", which offers only open Alert Groups that may be a main one — not itself, not resolved, not attached — and, once one is chosen, lists what changes: the status it will take, the Escalation chains that stop, and \"#{N} is critical and #{M} is not: its Escalation chains for critical stop.\"; pressing \"Attach\" attaches it (`attach-dialog.test.tsx`, `attach-detach.spec.ts`)."
  - "[C-37.FR-12, C-37.FR-14, C-37.AC-12] The page of the attached Alert Group shows the notice \"This Alert Group is attached to #{M} and follows its status.\" with the link and \"Detach\", offers no command but Add Note, and shows \"Resolved with #{M}\" once the main Alert Group resolved it; the page of the main Alert Group has a section \"Attached Alert Groups\" with the number, title, Route, status, firing and total Alerts and \"Detach\" for each, and its Alerts section lists the Alerts of each attached Alert Group under its number after its own (`alert-group-notices.test.tsx`, `attached-alert-groups.test.tsx`, `command-buttons.test.tsx`, `attach-detach.spec.ts`)."
  - "[C-37.FR-13, C-37.AC-12] The Alert Group list shows an attached Alert Group with the mark \"→ #{M}\", a main one with how many are attached, and the filter \"Attached\" — all (the default), only attached, none attached — kept in the URL and combined with the other filters; the filter \"only attached\" shows the Alert Group with \"→ #1287\" (`alert-group-filters.test.tsx`, `alert-group-header.test.tsx`, `alert-group-list.spec.ts`)."
  - "[C-37.FR-13] The bar of a selection offers \"Attach to #…\" with the same search for the selected Alert Groups, each through the dispatcher with its own outcome in the result dialog, and a bulk Acknowledge that meets an attached Alert Group shows its refusal naming the main one; every control needs `alert-groups:attach` or its own Permission and a Viewer sees none (`bulk-actions-bar.test.tsx`, `attach-detach.spec.ts`)."
  - "[C-37.FR-12, C-37.AC-12] \"Detach\" on the page, the section or the notice detaches it and brings its commands back with the status it kept; the Timeline names the main Alert Group in the entries `attached`, `status_followed` and `detached` with a link to it (`timeline.test.tsx`, `attach-detach.spec.ts`)."
  - "[C-37.AC-12] Everything works at 360 CSS pixels without horizontal scrolling of the page — the dialog, the notice, the section, the list with its mark and the bulk bar — and every new text exists in English and Russian (`attach-detach.spec.ts`, `phone-width.spec.ts`, the missing-key check of `make ci`)."
  - "[C-37.FR-12] `reference.md` has the notice in \"Banners, warnings and notices\" with its texts, which the page's test checks against the table (`alert-group-notices.test.tsx`)."
verify: "make ci e2e-web"
operator_attention: false
issue: null
---

# S-207. The Attach dialog, the notice and the section on the Alert Group page, and the mark, filter and bulk Attach of the list (FE)

## Scope

**IN**

- The command "Attach to #…" with its dialog, the notice and "Detach" on an attached Alert Group's page, the section
  "Attached Alert Groups" and the Alerts of the attached ones on a main one's page (C-37.FR-12).
- The mark "→ #{M}" and the count in the list, the filter "Attached" in the URL, and "Attach to #…" in the bulk bar
  (C-37.FR-13).
- "Resolved with #{M}" and the links of the Timeline entries to the main Alert Group (C-37.FR-14, the page's half).
- Texts in English and Russian, at 360 pixels.

**OUT**

- The Commands, the state and the API: S-202, S-203, S-206. Chains and timers: S-204. Messengers: S-205.
- The documentation page and the "Learn more" links: S-208.

## Contracts

- **API used**: `attachAlertGroup`, `detachAlertGroup`, `runBulkCommand` with `attach`, `listAlertGroups` with `attached`,
  `attached_to` and the text search, `getAlertGroup` (`attached_to`, `attached_count`, `resolved_with`,
  `allowed_commands`, `escalation_chains`, `severity_level`), `listAlertGroupAlerts` with `alert_group` on its items,
  `listAlertGroupTimeline`.
- **Components**: `attach-dialog.tsx` — the dialog for one Alert Group and, with a list of ids, for the bulk bar: the
  search by number or title over `listAlertGroups` limited to open, not attached Alert Groups other than the ones being
  attached, the preview of what changes (the main Alert Group's status, the running chains of the Alert Group from its
  read, and the critical warning when its `severity_level` is critical and the main one's is not), and the refusals of S-202
  shown at the field; `attached-alert-groups.tsx` — the section with Detach per row; `alert-group-notices.tsx` — the
  notice of an attached Alert Group, with the link and Detach; `alert-group-header.tsx` — "Resolved with #{M}" and the
  mark; `command-buttons.tsx` — "Attach to #…" and "Detach" by `allowed_commands`, and on an attached Alert Group only
  Add Note; `alert-group-alerts.tsx` — own Alerts then those of each attached one under its number;
  `alert-group-table.tsx` — the mark and the count; `alert-group-filters.tsx` and `lib/alert-group-search.ts` — the
  filter `attached` (`any`, `only`, `none`) in the URL; `bulk-actions-bar.tsx` and `bulk-result-dialog.tsx` — the
  bulk Attach and its outcomes; `timeline.tsx` — the main Alert Group of an entry as a link.
- **Routes**: the two Alert Group routes gain the parts above; no new route.
- **Documentation links**: none here; the page they name does not exist until S-208, and `docslinkcheck` fails on an
  entry whose page is missing, so S-208 adds `page.attach_detach` and the links from the dialog and the notice.
- **Texts** (EN / RU; `en.json` and `ru.json` are the real files; a missing key fails CI): "Attach to #…" / «Присоединить
  к #…»; the title "Attach #{N} to the main Alert Group" / «Присоединить #{N} к основной группе алертов»; "Number or
  title" / «Номер или заголовок»; "Attach" / «Присоединить»; "Detach" / «Отсоединить»; the notice "This Alert Group is
  attached to #{M} and follows its status." / «Эта группа алертов присоединена к #{M} и следует её статусу.»; "Attached
  Alert Groups" / «Присоединённые группы алертов»; the list mark "→ #{M}"; the filter "Attached" / «Присоединение» with
  "All" / «Все», "Only attached" / «Только присоединённые», "None attached" / «Без присоединённых»; "Resolved with
  #{M}" / «Закрыта вместе с #{M}»; the warning "#{N} is critical and #{M} is not: its Escalation chains for critical
  stop." / «#{N} критическая, а #{M} нет: её цепочки эскалации для критических останавливаются.»; the preview lines for
  the status it will take and the chains that stop, with the Russian plurals.
- **Reference** (`design/prd/l1/reference.md`): the notice joins "Banners, warnings and notices" with its texts, as the
  PRD asks the implementing story to do.
- **Permissions**: `alert-groups:attach` shows the command, the section's Detach and the bulk control; a Viewer sees none.
- **Fixture** (`web/src/test/fixtures/alert-groups.ts`): builders for an attached Alert Group and a main one.

## Steps

1. Write `attach-dialog.tsx` with the search, the preview and the refusals. Check: `attach-dialog.test.tsx`.
2. Write the notice, the section, the header and the commands on the page. Check: `alert-group-notices.test.tsx`,
   `attached-alert-groups.test.tsx`, `alert-group-header.test.tsx`, `command-buttons.test.tsx`.
3. Write the list mark, the filter and the bulk Attach. Check: `alert-group-filters.test.tsx`,
   `bulk-actions-bar.test.tsx`.
4. Add the Timeline links, the Alerts under numbers, the texts and `reference.md`. Check: `timeline.test.tsx`; `make ci`
   (missing keys).
5. Write `attach-detach.spec.ts` and extend the list and phone-width specs. Check: the Verification below.

## Verification

Playwright against `make dev` (as `web/e2e/support.ts` sets it up), in English and then Russian; the 360 px runs use
`phone-width.spec.ts`'s viewport.

1. Sign in as Ivan (Responder). Webhooks create `#1287 PostgresPrimaryDown` (warning) and `#1288 CheckoutLatencyHigh`
   (critical) with a chain on the Route of `#1288`. Open `#1288`. Expect the command "Attach to #…".
2. Press it, type `1287` in "Number or title". Expect `#1287 PostgresPrimaryDown` offered and `#1288` itself not; choose
   it. Expect the preview with the status it will take, the chains that stop and "#1288 is critical and #1287 is not:
   its Escalation chains for critical stop."; press "Attach".
3. Expect the page of `#1288` with the notice "This Alert Group is attached to #1287 and follows its status." with a link
   and "Detach", and no Acknowledge, Resolve or Snooze.
4. Open `#1287`. Expect the section "Attached Alert Groups" with `#1288`, its Route, status, Alerts and "Detach", and its
   Alerts listed under `#1288` after the Alerts of `#1287`; acknowledge `#1287` and expect `#1288` acknowledged by Ivan.
5. Open the list, choose the filter "Only attached". Expect `#1288` with "→ #1287"; "None attached" shows `#1287`
   with "1 attached" and not `#1288`; reload and expect the filter kept in the URL.
6. Select two other Alert Groups, press "Attach to #…" in the bar, choose `#1287`. Expect the result dialog with an
   outcome per Alert Group; a resolved one is refused with its reason.
7. Press "Detach" on `#1288`. Expect its commands back with its status and Owner kept, "Detached" in its Timeline with a
   link to `#1287`. Resolve `#1287` by hand with another attached Alert Group and expect that one to read "Resolved with
   #1287".
8. Sign in as a Viewer. Expect no "Attach to #…", no "Detach" and no bulk control.
9. Repeat 2 to 5 at 360 px; expect no horizontal scrolling of the page. Switch to Russian and expect the Russian texts and
   plurals («Присоединить #1288 к основной группе алертов», «1 присоединённая»).

## Open questions

None.

## Notes

- Suggested commit: `feat(web): add attach and detach to the alert group page and list`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases.
- The dialog and the notice link to the documentation in S-208 (C-37.FR-17), for the reason in Contracts.
- Mobile: the dialog is a full-width sheet at 360 pixels, as the Snooze dialog is.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-37.FR-1 | partial | the UI's Attach and Detach; the API S-202, the messengers' lack of them S-205 |
| C-37.FR-12 | partial | the dialog, the notice and the section; the page's API is completed by S-206 |
| C-37.FR-13 | partial | the mark, the filter control and the bar; the filter, the count and the bulk Attach in the API are completed by S-206 |
| C-37.FR-14 | partial | "Resolved with #{M}" on the page; the statistics are S-206's |
| C-37.AC-12 | full | |
| C-09.FR-13 | partial | revised for L2: the mark "→ #{M}", the count and the filter Attached in the list; the API is S-206's |
| C-09.FR-14 | partial | revised for L2: the notice, the section of attached Alert Groups and their Alerts on the page; the API is S-206's, the Root message's line S-205's |