---
id: S-050
title: Route policy fields, Unclaimed filter and badge, next notice and "Still on it" (FE)
capability: C-17
kind: fe
layer: L1
depends_on: [S-033, S-038, S-040, S-049]
covers: [C-17.FR-2, C-17.FR-3, C-17.FR-9, C-17.FR-10, C-17.AC-6, C-17.AC-8, C-09.FR-13, C-09.FR-14, C-10.FR-16, C-08.FR-1]
files_touched:
  - web/src/components/route-policy-timers.tsx
  - web/src/components/route-policy-timers.test.tsx
  - web/src/components/route-form.tsx
  - web/src/components/alert-group-filters.tsx
  - web/src/components/alert-group-table.tsx
  - web/src/components/alert-group-header.tsx
  - web/src/components/alert-group-notices.tsx
  - web/src/components/command-buttons.tsx
  - web/src/components/timeline.tsx
  - web/src/lib/alert-group-search.ts
  - web/src/lib/commands.ts
  - web/src/routes/alert-groups.index.tsx
  - web/src/routes/alert-groups.$alertGroupId.tsx
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - web/e2e/timers.spec.ts
acceptance:
  - "[C-17.FR-2, C-08.FR-1] The Route editor has an \"Ack timeout and Reminders\" section: \"Ack timeout\" on or off with \"First notice after\" in minutes and the computed times (\"Notices at 15, 45 and 105 minutes\"), \"Reminders\" on or off with \"First Reminder after\" and \"At most every\" in hours, and \"Auto-unacknowledge\" with its explanation; a new Route pre-fills them from the chosen profile."
  - "[C-17.FR-3, C-17.AC-6, C-09.FR-13] The Alert Group list has the filter \"Unclaimed\", kept in the URL, and the column \"Unclaimed\" with the badge \"Unclaimed\"; after the third notice the filter lists the Alert Group and its row shows the badge."
  - "[C-17.FR-3, C-17.AC-8] The Alert Group page shows the badge \"Unclaimed\" next to the status and the banner \"⚠ Nobody has taken this\" while the Alert Group is Unclaimed; after Acknowledge both disappear without a reload."
  - "[C-17.FR-9, C-09.FR-14] The page header shows the next scheduled notice — \"Next ack timeout notice at {time}\" or \"Next Reminder at {time}\" in the user's time zone — and nothing when none is scheduled."
  - "[C-17.FR-10, C-10.FR-16] The Owner sees \"Still on it\" while a Reminder waits; pressing it adds \"Still on it\" to the Timeline and the button disappears; other users never see it, and the refusals `not_owner` and `no_reminder_pending` show \"Only the Owner can answer a Reminder.\" and \"There is no Reminder to answer.\""
  - "[C-09.FR-14] The Timeline shows the timer entries: \"Ack timeout notice {n} of 3\", \"Unclaimed\", \"Reminder {n}\", \"Still on it\", \"{count} notices missed\" and \"Unacknowledged automatically: No answer to the last two Reminders\", each with its loudness and Mentions."
verify: "make ci e2e"
operator_attention: false
issue: null
---

# S-050. Route policy fields, Unclaimed filter and badge, next notice and "Still on it" (FE)

## Scope

**IN**

- The "Ack timeout and Reminders" section of the Route editor.
- The Unclaimed filter, column and badge in the Alert Group list; the badge and the banner on the Alert Group page.
- The next scheduled notice or Reminder in the page header.
- "Still on it" for the Owner and the timer entries of the Timeline.
- All of it at phone width.

**OUT**

- The behaviour behind it (S-049); the ack timeout notice template, edited in the Message section since S-038.
- Account links in the profile (S-052).

## Contracts

- **API used**: `getRoute`, `createRoute`, `updateRoute`, `listRouteProfiles`, `listAlertGroups` (`unclaimed`),
  `getAlertGroupCounts`, `getAlertGroup` (`unclaimed`, `next_notice`, `allowed_commands`), `answerReminder`,
  `getAlertGroupTimeline` (`TimelineTimersEntry`, `auto_unacknowledged`).
- **Route editor** (`route-policy-timers.tsx` in `route-form.tsx`): "Ack timeout" — a switch for
  `policy.ack_timeout.enabled` and "First notice after" in minutes for `first_interval_seconds`, with the three times it
  gives (F, 3F, 7F) shown below; "Reminders" — a switch, "First Reminder after" in hours for `first_interval_seconds`
  and "At most every" in hours for `cap_seconds`, with the first five Reminder times shown below; "Auto-unacknowledge"
  — a switch for `policy.auto_unacknowledge`, explained as "Take the Alert Group back when its Owner leaves two
  Reminders in a row unanswered.", disabled while Reminders are off. The fields are hidden when their switch is off; a
  new Route takes them from the chosen profile.
- **List** (C-17.FR-3, C-09.FR-13): the filter "Unclaimed" (`unclaimed=true`, in the URL) next to the Owner filters of
  S-033 and in the counts; the column "Unclaimed" shows the badge "Unclaimed" on Unclaimed rows and nothing on the
  others.
- **Page** (C-17.FR-3, FR-9, C-09.FR-14): in the header, the badge "Unclaimed" next to the status, and "Next ack
  timeout notice at {time}" or "Next Reminder at {time}" from `next_notice` (absolute, in the user's time zone, with
  the relative time on hover); the banner "⚠ Nobody has taken this" of
  [reference.md](../prd/l1/reference.md#banners-warnings-and-notices) with the other notices. Both follow the
  `alert-group` hint.
- **"Still on it"** (C-17.FR-10, C-10.FR-16): a button among the commands, shown only when `allowed_commands` contains
  `still_on_it`; it calls `answerReminder` and refreshes from the `CommandResult`. Refusal messages in
  `web/src/lib/commands.ts`: `not_owner` "Only the Owner can answer a Reminder.", `no_reminder_pending` "There is no
  Reminder to answer."; `owner_must_be_user` keeps S-033's text.
- **Timeline** (C-09.FR-14): the kind `timers` with the texts of the acceptance; `auto_unacknowledged` shows its
  `reason` and the previous Owner; each entry shows "Loud" or "Quiet" and its Mentions like the other entries.
- **Phone width** (C-09.FR-24): the badge, the next notice and "Still on it" fit 360 pixels with the other commands.

## Steps

1. Write the Route editor section. Check: `route-policy-timers.test.tsx` shows 15, 45 and 105 minutes for 15, and 4,
   12, 28, 52 and 76 hours for 4 and 24.
2. Add the filter, the column, the badge, the banner and the next notice. Check: Playwright steps 2 to 5.
3. Add "Still on it", the refusal messages and the Timeline entries. Check: Playwright steps 6 to 8.

## Verification

Run `make dev`, sign in as `admin@example.org` / `muster-dev-password`; create the Responder "bob", the Mattermost
Destination of S-039 and the Integration "lab" from a terminal as in S-049. Prepare the Alert Groups from the terminal
with the development clock (`ADV`, `FIRE` of S-049, which use the Personal access tokens `A` and `B` made there), then
in Playwright:

1. Routes → "New route" with the profile "On-call" → the section "Ack timeout and Reminders" shows "Ack timeout" on,
   "First notice after" 15 and "Notices at 15, 45 and 105 minutes", "Reminders" on with 4 and 24 hours, and
   "Auto-unacknowledge" off → switch to "Informational" → both switches are off → back to "On-call", name "t", Matcher
   `team="t"`, Destination "alerts" → "Create".
2. Terminal: `FIRE g1 DiskFull t` → the Alert Group page shows "Next ack timeout notice at {time 15 minutes ahead}".
3. Terminal: `ADV 900; ADV 1800; ADV 3600` → without a reload the page shows the badge "Unclaimed" and the banner "⚠
   Nobody has taken this"; the Timeline shows "Ack timeout notice 1 of 3", "2 of 3", "3 of 3" (Loud, Mentions:
   ack_timeout) and "Unclaimed" (Quiet).
4. The list → "Unclaimed" → the URL carries `unclaimed=true`, the row of the Alert Group shows the badge "Unclaimed".
5. As "bob" → "Acknowledge" → the badge and the banner disappear, the header shows "Next Reminder at {time 4 hours
   ahead}".
6. Terminal: `ADV 14400` → as "bob" the page shows "Still on it" → click → the Timeline shows "Still on it" with "bob"
   and the button disappears; the header still shows the next Reminder 8 hours after the first one.
7. As the Admin on the same Alert Group → no "Still on it".
8. Terminal: an Alert Group on a Route with auto-unacknowledge acknowledged by "bob", then
   `ADV 14400; ADV 28800; ADV 57600` → the advance outlasts `auth.session_idle_timeout`, so the Admin's and bob's
   browser contexts sign in again → its page shows status "firing", no Owner, and in the Timeline "Unacknowledged
   automatically: No answer to the last two Reminders" (Loud, Mentions: owner).
9. At 360 × 740 pixels the header with the badge, the next notice and "Still on it" fits, and
   `document.documentElement.scrollWidth` equals the viewport width.

`make e2e` runs these steps as `web/e2e/timers.spec.ts`, preparing the data through the API with Personal access tokens
and the development clock, and signing in again after the advance of step 8.

## Open questions

None.

## Notes

- Suggested commit: `feat(web): add ack timeout and reminder settings, unclaimed and still on it`.
- The page never computes a schedule: the next notice is `next_notice`, and the times under the Route editor fields are
  only an explanation of the policy.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-17.FR-2 | partial | the Route editor fields; with S-049 complete |
| C-17.FR-3 | partial | the list filter, column and badge and the page; with S-049 complete |
| C-17.FR-9 | partial | the page; with S-049 complete |
| C-17.FR-10 | partial | the button on the page; with S-049 complete |
| C-17.AC-6 | partial | the list; with S-049 complete |
| C-17.AC-8 | partial | the UI; with S-049 complete |
| C-09.FR-13 | partial | the Unclaimed filter and column |
| C-09.FR-14 | partial | the next notice, the Unclaimed badge and banner, the timer entries |
| C-10.FR-16 | partial | the "Still on it" button |
| C-08.FR-1 | partial | the timer section of the Route editor |
