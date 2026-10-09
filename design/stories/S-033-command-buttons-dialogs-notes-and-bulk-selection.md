---
id: S-033
title: Command buttons, dialogs, Note box, bulk selection, Owner filters and Snooze durations (FE)
capability: C-10
kind: fe
layer: L1
depends_on: [S-031, S-063]
covers: [C-10.FR-1, C-10.FR-2, C-10.FR-4, C-10.FR-6, C-10.FR-7, C-10.FR-8, C-10.FR-13, C-10.FR-14, C-10.FR-16, C-10.AC-14, C-09.FR-13, C-09.FR-14, C-09.FR-24, C-08.FR-1]
files_touched:
  - web/src/components/command-buttons.tsx
  - web/src/components/command-buttons.test.tsx
  - web/src/components/snooze-dialog.tsx
  - web/src/components/snooze-dialog.test.tsx
  - web/src/components/resolve-dialog.tsx
  - web/src/components/note-box.tsx
  - web/src/components/bulk-actions-bar.tsx
  - web/src/components/bulk-actions-bar.test.tsx
  - web/src/components/bulk-result-dialog.tsx
  - web/src/components/owner-filter.tsx
  - web/src/components/alert-group-table.tsx
  - web/src/components/alert-group-filters.tsx
  - web/src/components/alert-group-header.tsx
  - web/src/components/route-policy-snooze.tsx
  - web/src/components/route-form.tsx
  - web/src/components/statistics-table.tsx
  - web/src/routes/alert-groups.index.tsx
  - web/src/routes/alert-groups.$alertGroupId.tsx
  - web/src/lib/alert-group-search.ts
  - web/src/lib/api.ts
  - web/src/lib/commands.ts
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/commands.spec.ts
  - web/e2e/bulk-commands.spec.ts
  - web/e2e/commands-phone.spec.ts
acceptance:
  - "[C-10.FR-1, C-10.FR-16] The Alert Group page and each list row offer exactly the commands in `allowed_commands` — Acknowledge, Unacknowledge, Resolve, Unresolve, Snooze, Unsnooze — and a Viewer sees none."
  - "[C-10.FR-4] On an Alert Group another user owns, Acknowledge reads \"Take over from {Owner}\" and needs no confirmation; afterwards the Timeline shows the Takeover as Loud, mentioning the previous Owner."
  - "[C-10.FR-6] The Snooze dialog offers the Route's Snooze durations as quick choices, native date and time inputs, and \"No end\", which shows \"This Alert Group stays snoozed until someone unsnoozes it.\" and must be confirmed before \"Snooze\" is enabled."
  - "[C-10.FR-1, C-10.FR-8] The Resolve dialog takes an optional Note; the Note box under the Timeline adds Notes up to `alert_group.note_max_length` characters with a counter, shown with their author and Transport."
  - "[C-10.FR-2, C-10.FR-7] Unresolve is offered only while `allowed_commands` has it, so never for an Alert Group the system resolved; when a newer Alert Group of the same key is open, the page shows \"A newer Alert Group exists: #N\", linked to it, in place of the Unresolve button; a refusal that still happens in a race shows its message, such as \"A newer open Alert Group #N exists.\" with a link, or \"Muster resolved this Alert Group automatically; it reopens by itself when an alert returns.\""
  - "[C-10.FR-14] Selecting Alert Groups in the list — at most `alert_group.bulk_max` — offers Acknowledge, Resolve, Snooze and Unsnooze for all of them; the result lists every Alert Group with its outcome, for example \"skipped: owned by Alice\" and \"already resolved\"."
  - "[C-10.FR-13, C-09.FR-13, C-09.FR-14] The list has the Owner filter (a user, \"Mine\" or \"Nobody\", from the user directory) and \"Snoozed with no end\", kept in the URL, and the columns Owner and Snooze end; the page header shows the Owner, the Snooze end (or \"No end\") and who set the Snooze; a deleted user — who set a Snooze, or a former Owner in the Timeline — reads \"(deactivated)\"."
  - "[C-10.FR-6] The Route editor has a Snooze durations section, pre-filled from the profile on creation."
  - "[C-10.AC-14] For the three Alert Groups of C-10.AC-14 the statistics page shows a median time to acknowledge of 10 min over 2 Alert Groups."
  - "[C-09.FR-24] At 360 CSS pixels the page shows the commands right under the header, the dialogs and the Note box fit the screen, and nothing scrolls horizontally."
verify: "make ci e2e"
operator_attention: false
issue: 33
---

# S-033. Command buttons, dialogs, Note box, bulk selection, Owner filters and Snooze durations (FE)

## Scope

**IN**

- Command buttons on the Alert Group page and in list rows, with Takeover wording.
- The Snooze, Resolve and Unresolve dialogs and the refusal messages.
- The Note box under the Timeline.
- Selection in the list with bulk actions and the per-row result.
- The Owner filters and columns, the Owner and Snooze in the page header, the Snooze durations section of the Route
  editor.
- All of it at phone width.

**OUT**

- "Still on it", the next notice and Unclaimed (S-050); commands in messengers (S-061, S-067).

## Contracts

- **API used**: `acknowledgeAlertGroup`, `unacknowledgeAlertGroup`, `resolveAlertGroup`, `unresolveAlertGroup`,
  `snoozeAlertGroup`, `unsnoozeAlertGroup`, `createAlertGroupNote`, `runBulkCommand`,
  `listUserDirectory`, `getRoute`, `updateRoute`, `listAlertGroups` (`owner`, `snoozed_no_end`),
  `getAlertGroupStatistics`.
- **Buttons** (C-10.FR-1, FR-4, FR-16): built from `allowed_commands` only: "Acknowledge" — "Take over from {Owner}"
  when another user owns the Alert Group — "Unacknowledge", "Resolve", "Unresolve", "Snooze", "Unsnooze". On the page
  they follow the header; in a row they sit in a "…" menu. After a command the page and row refresh from the
  `CommandResult` and the live hint.
- **Snooze dialog** (C-10.FR-6): the Route's `policy.snooze_durations_seconds` as buttons ("1 h", "4 h", "24 h"…),
  "Until" with native date and time inputs (`<input type="date">` and `type="time"`, D250) in the user's time zone
  (sent as `until` in UTC), and "No end", which shows the
  warning of [reference.md](../prd/l1/reference.md#banners-warnings-and-notices) and a checkbox "I understand" that
  enables "Snooze" (sent as `{"no_end": true}`).
- **Resolve dialog** (C-10.FR-1): "Add a note (optional)", sent as `note`. **Unresolve** (C-10.FR-7) asks "Bring this
  Alert Group back as firing without an Owner?"; it exists only in the UI and the API, and only while
  `allowed_commands` lists `unresolve`. On a person-resolved Alert Group with the notice `newer_alert_group_exists`
  (S-032) the button's place holds the link "A newer Alert Group exists: #N" to `related_alert_group`; the UI never
  offers a command it knows would be refused.
- **Refusal messages** (C-10.FR-2; `web/src/lib/commands.ts`): `already_resolved` "This Alert Group is already
  resolved."; `not_acknowledged` "This Alert Group is not acknowledged."; `not_snoozed` "This Alert Group is not
  snoozed."; `all_alerts_resolved` "All alerts of this Alert Group have already resolved."; `resolved_automatically`
  "Muster resolved this Alert Group automatically; it reopens by itself when an alert returns.";
  `newer_alert_group_exists` "A newer open Alert Group #N exists." linked to `related_alert_group`;
  `owner_must_be_user` "A Service account cannot be an Owner."; `403` "You are not permitted to do this."
- **Note box** (C-10.FR-8): under the Timeline with `alert-groups:note`; a counter against `alert_group.note_max_length`
  (4,000); Notes appear in the Timeline (kind `notes`) with their author, the Transport and the time.
- **Bulk** (C-10.FR-14): "Select" shows checkboxes on rows; the bar shows the count and "Acknowledge", "Resolve",
  "Snooze" (the Snooze dialog without Route durations — "Until" or "No end") and "Unsnooze", each only with its
  Permission; more than `alert_group.bulk_max` selected disables them with "Select at most 100 Alert Groups." The result
  dialog lists each Alert Group as "#N: done", "#N: {refusal message}" or "#N: skipped: owned by {Owner}".
- **Owner** (C-10.FR-13): filters "Owner" (a user picked from `listUserDirectory` with search, sent as its id), "Mine"
  (`owner=me`), "Nobody" (`owner=none`) and "Snoozed with no end" (`snoozed_no_end`), in the URL; columns "Owner" and
  "Snooze end"; the page header shows "Owner: {name}", "Snoozed until {time}" or "Snoozed with no end", and "Snoozed by
  {name}"; a deactivated user is shown as "{name} (deactivated)".
- **Route editor** (C-10.FR-6): "Snooze durations" — a list of durations with add and remove, sent as
  `policy.snooze_durations_seconds`.
- **Statistics** (C-10.AC-14): the table gains the column "Acknowledged" — `time_to_acknowledge.count`, the Alert
  Groups the time to acknowledge measures — so that "10 min over 2 Alert Groups" reads on the page.
- **Phone width** (C-09.FR-24): the commands form a bar right under the header; dialogs open as full-screen sheets; the
  Note box and the bulk bar fit 360 pixels.

## Steps

1. Write the command buttons and the refusal messages. Check: the component test renders each `allowed_commands` set
   and the Takeover wording.
2. Write the Snooze and Resolve dialogs, Unresolve and the Note box. Check: the component test keeps "Snooze" disabled
   until "No end" is confirmed; Playwright snoozes, resolves with a Note and unresolves.
3. Write selection, the bulk bar and the result dialog. Check: Playwright runs step 6 of Verification.
4. Add the Owner filters, columns and header fields, and the Snooze durations section. Check: Playwright filters by
   "Mine" and edits the durations.
5. Check the phone layout. Check: `commands-phone.spec.ts` at 360 pixels.

## Verification

Run `make dev`, sign in as `admin@example.org` / `muster-dev-password`; create the Responder "bob" and Alert Groups on
the Route "ops" from a terminal as in S-032 and S-063. Then in Playwright:

1. Open a firing Alert Group → buttons "Acknowledge", "Resolve", "Snooze" → "Acknowledge" → the header shows "Owner:
   admin".
2. In a second context signed in as "bob" → the same page shows "Take over from admin" → click → "Owner: bob"; back as
   the Admin, the Timeline shows "Taken over" with "Loud" and "Mentions: previous_owner".
3. "Snooze" → the quick choices "1 h", "4 h", "24 h" → "No end" → the warning "This Alert Group stays snoozed until
   someone unsnoozes it." and "Snooze" disabled → "I understand" → "Snooze" → "Snoozed with no end" and "Snoozed by
   admin".
4. "Resolve" → Note "Rolled back the deploy." → "Resolve" → the Timeline shows the Note with "admin" and "ui" →
   "Unresolve" → confirm → "firing".
5. On an Alert Group resolved by the system → no "Unresolve"; on one whose later Alert Group is open (S-032 step for
   C-10.AC-8) → no "Unresolve", and in its place "A newer Alert Group exists: #N" → click → the page of that newer
   Alert Group. (The refusal message of a race is covered by `command-buttons.test.tsx` with a mocked `409`.)
6. List → "Select" → three firing rows, one owned by "admin" → as "bob": "Acknowledge" → the result shows two "done"
   and "#N: skipped: owned by admin".
7. "Mine" → only Alert Groups owned by the signed-in user, and the URL carries `owner=me`; snooze another Alert Group
   with "No end" → "Snoozed with no end" lists it alone.
8. Routes → "ops" → "Snooze durations" → add "2 h" → "Save" → the Snooze dialog of an "ops" Alert Group offers "2 h".
9. Statistics → "By route" → after the three Alert Groups of C-10.AC-14 prepared from a terminal (S-032) → "Time to
   acknowledge" median "10 min" over 2.
10. As a Viewer → the page shows no command buttons and no Note box.
11. At 360 × 740 pixels → the commands are right under the header, "Snooze" opens a full-screen sheet, the Note box
    fits, and `document.documentElement.scrollWidth` equals the viewport width.

`make e2e` runs these steps as `web/e2e/commands.spec.ts`, `bulk-commands.spec.ts` and `commands-phone.spec.ts`.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): add command buttons, dialogs, the note box and bulk selection`.
- The buttons never compute what is allowed: `allowed_commands` from the API is the single source.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-10.FR-1 | full | together with S-032 and S-063 |
| C-10.FR-2 | full | together with S-032; messenger answers are S-061 and S-067 |
| C-10.FR-4 | full | together with S-032; Reminders for the new Owner are S-049 |
| C-10.FR-6 | full | together with S-032 and S-063; messenger durations are S-061 and S-042 |
| C-10.FR-7 | full | together with S-032 |
| C-10.FR-8 | full | together with S-063 |
| C-10.FR-13 | full | together with S-063 |
| C-10.FR-14 | full | together with S-032 |
| C-10.FR-16 | full | together with S-032 and S-063; `still_on_it` is S-049 and S-050 |
| C-10.AC-14 | full | together with S-032 |
| C-09.FR-13 | full | the Owner filters; "Delivery problem" and Unclaimed come with S-064 and S-050 |
| C-09.FR-14 | full | the Owner, Snooze and Note box; later sections come with S-038, S-064 and S-050 |
| C-09.FR-24 | full | together with S-030 |
| C-08.FR-1 | partial | the Snooze durations of the Route editor |
