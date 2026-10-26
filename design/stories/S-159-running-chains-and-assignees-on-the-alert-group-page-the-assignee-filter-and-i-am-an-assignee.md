---
id: S-159
title: Running chains and Assignees on the Alert Group page at phone width, the Assignee filter, and "I am an Assignee" on the Home page (FE)
capability: C-32
kind: fe
layer: L2
depends_on: [S-158, S-168]
covers: [C-32.FR-18, C-32.AC-10, C-32.AC-14, C-09.FR-11, C-09.FR-13, C-09.FR-14]
files_touched:
  - web/src/routes/alert-groups.$alertGroupId.tsx
  - web/src/routes/alert-groups.index.tsx
  - web/src/components/alert-group-header.tsx
  - web/src/components/alert-group-header.test.tsx
  - web/src/components/alert-group-chains.tsx
  - web/src/components/alert-group-chains.test.tsx
  - web/src/components/timeline.tsx
  - web/src/components/timeline.test.tsx
  - web/src/components/alert-group-table.tsx
  - web/src/components/alert-group-filters.tsx
  - web/src/components/alert-group-filters.test.tsx
  - web/src/lib/alert-group-search.ts
  - web/src/routes/index.tsx
  - web/src/components/i-am-an-assignee.tsx
  - web/src/components/i-am-an-assignee.test.tsx
  - web/src/test/fixtures/alert-groups.ts
  - web/src/lib/docs-links.json
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/alert-group-page.spec.ts
  - web/e2e/alert-group-list.spec.ts
  - web/e2e/home.spec.ts
  - web/e2e/phone-width.spec.ts
acceptance:
  - "[C-32.FR-18, C-32.AC-14, C-09.FR-11, C-09.FR-14] In a real browser the Alert Group page shows its running chains — each with its name, its current step and when its next step is due, or \"ran out\" or \"stopped\" — and its Assignees with their Teams, \"Assignees: @ivan (devops), @petr (payments)\", and after an acknowledgement \"Acknowledged by @ivan (devops) · Escalation chains stopped\"; at 360 CSS pixels wide the page has no horizontal scrolling; in English and in Russian (`alert-group-chains.test.tsx`, `alert-group-header.test.tsx`, `alert-group-page.spec.ts`, `phone-width.spec.ts`)."
  - "[C-32.FR-18, C-09.FR-14] The Timeline shows each Page — \"Step 1 · payments: Paged @petr, normal\", a call through \"Example Gateway\" —, each undelivered Page with its reason, each skipped step with its reason, the Assignees' changes and `escalation_webhook`, under the kind filter \"Escalation chains\", and an acknowledgement with the Transport `gateway` as \"from a call through Example Gateway\" (`timeline.test.tsx`)."
  - "[C-32.FR-18, C-09.FR-13] The Alert Group list gains the column Assignees and the filter Assignee — a User or \"me\" — kept in the URL; the filter returns the Alert Groups where that User is an Assignee; at 360 CSS pixels the list with the column and the filter has no horizontal scrolling of the page (NFR-15) (`alert-group-filters.test.tsx`, `alert-group-list.spec.ts`, `phone-width.spec.ts`)."
  - "[C-32.FR-18, C-32.AC-10] The Home page lists, under \"I am an Assignee\" with \"Alert Groups that a running Escalation chain Paged you about. They leave this list once acknowledged.\", the Alert Groups where the signed-in User is an Assignee, with their status, Assignees, the chain and its step, and \"Acknowledge\" and \"Snooze…\"; acknowledging one removes it from the list; the section fits 360 CSS pixels (`i-am-an-assignee.test.tsx`, `home.spec.ts`)."
  - "Every new text exists in English and Russian; the running chains have \"Learn more\" to Concepts → Escalation chains; `make e2e` passes."
verify: "make ci e2e"
operator_attention: false
issue: null
---

# S-159. Running chains and Assignees on the Alert Group page at phone width, the Assignee filter, and "I am an Assignee" on the Home page (FE)

## Scope

**IN**

- The Alert Group page: its running chains with the current step and the next one due, its Assignees with their Teams,
  the Owner's Team, and the Timeline entries of Pages, undelivered Pages, skipped steps, Assignees and the Transport
  `gateway` — at 360 CSS pixels, as the list with its new column is (C-32.FR-18, NFR-15; C-09.FR-11, FR-14 revised).
- The Alert Group list's column Assignees and filter Assignee (C-32.FR-18; C-09.FR-13 revised).
- "I am an Assignee" on the Home page of S-136 (C-32.FR-18, L2.md §5.2).

**OUT**

- The API: S-169 (running chains), S-168 (Assignees, filter). The chain page: S-158. The documentation: S-161.

## Contracts

- **API used**: `getAlertGroup` (`escalation_chains`, `assignees`, `owner_teams`), `listAlertGroups` (`assignee`),
  `getAlertGroupTimeline` (`kind=escalation`), `acknowledgeAlertGroup`, `snoozeAlertGroup`, `getMe`.
- **Components**: `alert-group-chains.tsx` — the running chains of the page, linked to their chain pages;
  `alert-group-header.tsx` — the Assignees or the Owner with their Teams; `timeline.tsx` — the kind "Escalation chains"
  and its entries, the Transport `gateway`; `alert-group-table.tsx` and `alert-group-filters.tsx` — the column and the
  filter, with `lib/alert-group-search.ts` keeping `assignee` in the URL; `i-am-an-assignee.tsx` — the section of the
  Home page, with the command buttons of S-033; `web/src/test/fixtures/alert-groups.ts` — builders with chains and
  Assignees for the tests.
- **Documentation links** (`docs-links.json`): `field.alert_group_chains` → `concepts/escalation-chains.md#assignees`;
  the section exists from S-168.
- **Texts** (EN / RU), from C-32's UI and the approved Home and Lifecycle mockups: "I am an Assignee" / «Позвали меня»,
  "Alert Groups that a running Escalation chain Paged you about. They leave this list once acknowledged." / «Группы,
  куда вас позвала идущая цепочка. После подтверждения они уходят отсюда.», "Assignees" / «Позваны», "Chain" /
  «Цепочка», "step {{n}}" / «шаг {{n}}», "next step at {{time}}" / «следующий шаг в {{time}}», "ran out" /
  «закончилась», "stopped" / «остановлена», "Escalation chains" / «Цепочки эскалации» (the Timeline kind `escalation`),
  "Paged" / «Позвала», "Page not delivered" / «Призыв не доставлен», "skipped" / «пропущен», "Escalation chains stopped"
  / «Цепочки остановлены», "me" / «я».

## Steps

1. Write the running chains and the Assignees of the page. Check: `alert-group-chains.test.tsx`,
   `alert-group-header.test.tsx`.
2. Add the Timeline entries and the kind filter. Check: `timeline.test.tsx`.
3. Add the column and the filter. Check: `alert-group-filters.test.tsx`.
4. Add "I am an Assignee" to the Home page. Check: `i-am-an-assignee.test.tsx`.
5. Add the links, texts and end-to-end specs. Check: the Verification below; `make e2e`.

## Verification

Run `make dev` and set up scenario 2 of C-32 from a terminal as in S-168's Verification, so that ivan (devops) and petr
(payments) are Assignees of a firing `KubernetesContainerOOMKilled` Alert Group. In Playwright, in English and in
Russian:

1. As ivan at 1280 × 800: Home shows "I am an Assignee" with its hint and the row "#… KubernetesContainerOOMKilled",
   "Firing", "@ivan (devops), @petr (payments)", the chain "devops" and its step, "Acknowledge" and "Snooze…".
2. Alert Groups → the filter Assignee "me" → the same Alert Group; the column Assignees shows both.
3. Its page: the running chains "devops" and "payments" with their steps and "next step at …"; "Assignees: @ivan
   (devops), @petr (payments)"; the Timeline filtered by "Escalation chains" shows "Step 1 · payments", "Page not
   delivered: @petr has no linked Mattermost account" where it applies, and "Step 3 skipped: only for critical".
4. "Acknowledge" → the header reads "Acknowledged by @ivan (devops) · Escalation chains stopped", the chains "stopped",
   and Home no longer lists the Alert Group.
5. At 360 × 740 the Alert Group list with the Assignees column and the filter, the Alert Group page and Home have no
   horizontal scrolling.

`make e2e` runs these steps in `home.spec.ts`, `alert-group-list.spec.ts` and `alert-group-page.spec.ts`, and
`phone-width.spec.ts` checks the list, the Alert Group page and Home at 360 pixels.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): show running escalation chains and assignees and list what pages me`.
- The approved Home mockup says «Назначено мне»; the English text is "I am an Assignee", since `CONTEXT.md` avoids
  "Assigned" (L2.md §5.2), and the Russian follows the vocabulary of S-162's Open question 1.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-32.FR-18 | partial | the pages; the API is S-169 and S-168 |
| C-32.AC-10 | partial | the Home page; the filter's API is S-168 |
| C-32.AC-14 | partial | the Alert Group page at 360 pixels; the chain page is S-158 |
| C-09.FR-11 | full | revised for L2: the Assignees and running chains on the page |
| C-09.FR-13 | full | revised for L2: the Assignee filter |
| C-09.FR-14 | full | revised for L2: the running chains and the escalation entries of the Timeline |
