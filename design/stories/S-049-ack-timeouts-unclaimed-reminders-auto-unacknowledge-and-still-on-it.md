---
id: S-049
title: Ack timeouts, Unclaimed, Reminders, auto-unacknowledge and "Still on it" (BE)
capability: C-17
kind: be
layer: L1
depends_on: [S-066, S-067, S-047]
covers: [C-17.FR-1, C-17.FR-2, C-17.FR-3, C-17.FR-4, C-17.FR-5, C-17.FR-7, C-17.FR-8, C-17.FR-9, C-17.FR-10, C-17.FR-11, C-17.AC-1, C-17.AC-2, C-17.AC-3, C-17.AC-4, C-17.AC-5, C-17.AC-6, C-17.AC-8, C-17.AC-9, C-09.FR-1, C-09.FR-8, C-09.FR-11, C-09.FR-13, C-10.FR-4, C-10.FR-16, C-04.FR-2, C-03.FR-13, C-02.FR-12, C-11.FR-20, C-12.FR-4, C-15.AC-11, C-03.AC-27]
files_touched:
  - api/openapi.yaml
  - internal/api/server.go
  - internal/api/server_test.go
  - internal/api/commands.go
  - internal/api/commands_test.go
  - internal/api/alertgroups.go
  - internal/api/alertgroups_test.go
  - internal/timers/schedule.go
  - internal/timers/schedule_test.go
  - internal/groups/timers.go
  - internal/groups/timers_test.go
  - internal/groups/dispatcher.go
  - internal/groups/dispatcher_test.go
  - internal/groups/commands.go
  - internal/groups/commands_test.go
  - internal/groups/bulk.go
  - internal/groups/system.go
  - internal/groups/events.go
  - internal/groups/events_test.go
  - internal/groups/allowed.go
  - internal/groups/allowed_test.go
  - internal/groups/filters.go
  - internal/groups/read.go
  - internal/groups/list.go
  - internal/groups/list_test.go
  - internal/groups/counts.go
  - internal/groups/query.sql
  - internal/groups/groups_integration_test.go
  - internal/delivery/enqueue.go
  - internal/delivery/worker.go
  - internal/delivery/worker_test.go
  - internal/delivery/outcomes.go
  - internal/delivery/outcomes_test.go
  - internal/delivery/webhookevents.go
  - internal/delivery/webhookevents_test.go
  - internal/delivery/threads.go
  - internal/delivery/threads_test.go
  - internal/delivery/render.go
  - internal/delivery/query.sql
  - internal/delivery/live_test.go
  - internal/messages/replies.go
  - internal/messages/render.go
  - internal/messages/render_test.go
  - internal/messages/query.sql
  - internal/messages/texts/en.json
  - internal/messages/texts/ru.json
  - internal/runtime/runtime.go
  - internal/runtime/runtime_test.go
  - internal/logging/events.go
  - internal/db/migrations/0007_alert_groups_first_published_at_backfill.up.sql
  - internal/db/migrations/0007_alert_groups_first_published_at_backfill.down.sql
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - design/db/schema.md
  - test/e2e/timers_test.go
acceptance:
  - "[C-17.FR-1, C-17.FR-2, C-17.AC-1, C-12.FR-4] On a Route with the On-call profile a firing Alert Group gets Loud ack timeout notices — Thread replies with the Route's ack timeout notice, Mentions by each Destination's `ack_timeout` setting and only links — at exactly 15, 45 and 105 minutes after its first Publication, and no fourth."
  - "[C-17.FR-3, C-17.AC-6, C-09.FR-1, C-09.FR-13] After the third notice the Alert Group has `unclaimed: true`, records `unclaimed` (Quiet), its Root message shows \"⚠ Nobody has taken this\", and `listAlertGroups?unclaimed=true` and `getAlertGroupCounts?unclaimed=true` return it."
  - "[C-17.FR-1, C-17.AC-2] Acknowledging at minute 20 removes the pending notice; Unacknowledge at minute 30 starts the ack timeout over, so the next notice comes at minute 45; Reopen into firing, Unsnooze, Unresolve, a Snooze ending, a rise to Urgent, auto-unacknowledge and the release of a disabled or deleted Owner start it over the same way, and an Alert Group with no Publication yet has no ack timeout."
  - "[C-17.FR-1] The ack timeout starts at the `published_at` of the Alert Group's first Publication, set by a system transition after the delivery outcome has committed; a republication after the Root message was deleted does not start it over."
  - "[C-09.FR-8] When a Snooze ends while Alerts still fire, the Alert Group is firing without an Owner, `snooze_ended` is Loud, and the next ack timeout notice is due one first interval later."
  - "[C-17.FR-4, C-17.AC-3, C-10.FR-4] Reminders — Thread replies mentioning the Owner — come at 4, 12, 28 and 52 hours after the acknowledgement whether or not \"Still on it\" was answered; a Takeover at hour 13 moves the next Reminder to hour 17 and mentions the new Owner; a snoozed Alert Group gets none."
  - "[C-17.AC-4] A Route created with the Informational profile gives its Alert Groups no `next_notice`, no ack timeout notice and no Reminder."
  - "[C-17.FR-5, C-17.AC-5] With auto-unacknowledge on and no answer to the Reminders at hours 4 and 12, there is no Reminder at hour 28; the Alert Group is firing without an Owner, the Timeline has `auto_unacknowledged` with the reason \"No answer to the last two Reminders\", `loudness` `loud` and `mentions` `[owner]`, the Destinations get a Thread reply mentioning the previous Owner, and the next ack timeout notice comes 15 minutes later; with a Note by the Owner at hour 5 the Reminder at hour 28 is sent."
  - "[C-17.FR-5] A Reminder that reached no Destination does not count as unanswered; a `reminder` event delivered to an events-mode outgoing webhook counts as reached."
  - "[C-17.FR-1, C-15.AC-11] On a Route whose only Destination is an events-mode outgoing webhook, the ack timeout starts when the `created` event is delivered, at its `delivered_at`."
  - "[C-17.FR-10, C-10.FR-16, C-04.FR-2] `answerReminder` by the Owner while a Reminder waits records `reminder_answered` (Quiet) and does not move the next Reminder; it is refused with `409` `not_owner` for another user, `no_reminder_pending` without a waiting Reminder and `owner_must_be_user` for a Service account token, and `allowed_commands` offers `still_on_it` only to the Owner while a Reminder waits."
  - "[C-17.AC-8] An Unclaimed Alert Group stops being Unclaimed — `unclaimed: false` and the mark gone from the Root message — when it is acknowledged, snoozed or resolved; after Unacknowledge it is not Unclaimed again until the third notice of the new ack timeout."
  - "[C-17.FR-7, C-02.FR-12] Overdue notices of one Alert Group, such as after downtime, collapse into one Loud Thread reply and one `notices_missed` entry with `missed_count`."
  - "[C-17.FR-8, C-03.FR-13, C-03.AC-27] Disabling the Owner of an acknowledged Alert Group whose Reminders run releases it (S-063): its `reminder` timer is deleted, so no Reminder and no auto-unacknowledge follow; the ack timeout starts over at the release, so notice 1 comes F later and the third notice makes it Unclaimed again; the Thread in the fake Mattermost gets the Loud reply \"The owner was disabled — this Alert Group has no owner now.\"; enabling the User again brings back neither the acknowledgement nor the Reminders. Deleting the Owner does the same with \"The owner was deleted — this Alert Group has no owner now.\""
  - "[C-17.FR-9] `getAlertGroup` returns `next_notice` with the kind and time of the next ack timeout notice or Reminder."
  - "[C-17.FR-11, C-17.AC-9, C-09.FR-11, C-11.FR-20] Every row of the lifecycle event table of C-17.FR-11 records exactly one Timeline entry with the row's `event`, kind, `loudness` and `mentions`, and reaches messengers as the row's last column says (a table-driven test)."
  - "[C-15.AC-11] With an events-mode outgoing webhook Destination on the Route, the running timers produce exactly one events-mode request per timer row — `ack_timeout`, `unclaimed`, `reminder`, `auto_unacknowledged`, `notices_missed`, and `reminder_answered` from \"Still on it\" — with the row's name as `event` and its loudness as `notify` (`test/e2e/timers_test.go`)."
verify: "make ci test-integration e2e"
operator_attention: false
issue: 49
---

# S-049. Ack timeouts, Unclaimed, Reminders, auto-unacknowledge and "Still on it" (BE)

## Scope

**IN**

- The ack timeout: start at the first Publication, the three notices, starting over and stopping, Unclaimed.
- Reminders: the doubling schedule, Takeover, Reopen into acknowledged, and their end when a disabled or deleted Owner
  is released.
- Auto-unacknowledge with the answer rule, including Reminders that reached no Destination.
- What counts for an events-mode outgoing webhook: its delivered `created` event as the first Publication (or the
  first delivered event when no `created` was delivered), its delivered `reminder` event as a Reminder that reached
  it (D298).
- Migration `0007`, which fills `first_published_at` of the Alert Groups published before this story.
- "Still on it" through the API (`answerReminder`).
- Collapsed overdue notices; `next_notice`; the `unclaimed` filter and field; the lifecycle events of C-17.FR-11.
- The C-09.FR-8 part left by S-063: the ack timeout starts over when a Snooze ends.

**OUT**

- The pages: Route policy fields, the Unclaimed filter, column and badge, the next notice and "Still on it" on the
  Alert Group page (S-050).
- The buttons "Still on it" and "Unack" on Reminder Thread replies, presses on them and the expired-button answer
  (S-068, split from this story). Here a Reminder reaches the messengers as a plain Thread reply mentioning the Owner.
- Creating Account links (S-051).
- Buttons signed with a key that a real rotation removed (S-055).

## Contracts

- **Operations implemented**: `answerReminder` (`alert-groups:acknowledge`); `listAlertGroups` and
  `getAlertGroupCounts` accept `unclaimed` (the `422 unsupported` of S-029 is removed); `getAlertGroup` and list items
  fill `unclaimed` and `next_notice`; `allowed_commands` gains `still_on_it`; `getAlertGroupTimeline` returns
  `TimelineTimersEntry` (`notice_number`, `missed_count`) and `auto_unacknowledged` as a `TimelineStatusEntry` with
  `reason` and `previous_owner`. Schemas: `NextNotice`, `AckTimeoutPolicy`, `RemindersPolicy`, `CommandName`,
  `CommandResult`, `TimelineTimersEntry`. The policy fields `ack_timeout`, `reminders` and `auto_unacknowledge` are
  stored since S-025; this story gives them their behaviour. `AnswerReminder` joins the `implemented` map of
  `internal/api/server.go`.
- **Spec fix**: `answerReminder` gains `x-permission-check: dispatcher` in `api/openapi.yaml`, like the Commands of
  S-032, and `TestDispatcherPermissionCheck` (`server_test.go`) lists it among the marked operations.
- **Tables**: `alert_groups` (timer columns: `first_published_at`, `ack_timeout_started_at`,
  `ack_timeout_notices_sent`, `unclaimed`, `reminders_sent`, `reminders_unanswered`, `last_reminder_at`,
  `owner_answered_at`), `timers` (kinds `ack_timeout` and `reminder`, `notice_number`), `timeline_entries`,
  `thread_replies` (`state` of `reminder` replies), `webhook_events` (`created` and `reminder`, `delivered_at`),
  `deliveries.published_at`. The columns exist since `0001_init`; migration
  `0007_alert_groups_first_published_at_backfill` (data only, `schema.md` updated) fills `first_published_at` of the
  Alert Groups published before this story with their earliest `deliveries.published_at` or delivered
  `webhook_events.delivered_at`. Without it the first delivered call after the upgrade would mark them at that old
  time, and every open firing Alert Group would collapse its overdue notices into one Loud reply at once. It sets no
  `ack_timeout_started_at` and writes no timer: such an Alert Group gets an ack timeout on its next transition into
  firing. Its down migration changes nothing.
- **Schedules** (`internal/timers/schedule.go`, pure functions over the policy): ack timeout notice `n` (1 to 3) is
  due at start + (2ⁿ − 1)·F with F = `ack_timeout.first_interval_seconds`, that is F, 3F and 7F; Reminder `k` (1, 2, …)
  is due one interval after Reminder `k − 1` (or after the acknowledgement for `k` = 1), the interval being
  min(B·2ᵏ⁻¹, C) with B = `reminders.first_interval_seconds` and C = `reminders.cap_seconds` — 4, 12, 28, 52, 76 … hours
  for the On-call profile. At each firing the worker reads the Route's current policy; a policy turned off since the
  timer was set removes the timer without a notice. A policy edit never changes a status.
- **Ack timeout** (C-17.FR-1, FR-2; `internal/groups/timers.go`, ADR-0016): every change goes through the dispatcher as
  a system transition with the Transport `system`.
  - *Start* (D277, D298): the first Publication of an Alert Group to any Destination starts it. The delivery worker
    does not change the Alert Group inside its outcome transaction, which locks `deliveries` before `alert_groups`
    while the dispatcher, through `Enqueue`, locks them the other way round. After the transaction that recorded a
    successful Publication has committed (`internal/delivery/outcomes.go`, through its post-commit list, and the
    worker's `Published` hook in `worker.go`, which the runtime wires to `groups.MarkPublished`), the worker calls
    `groups.MarkPublished(alert_group, at)` with `at` = that delivery's `published_at`. `MarkPublished` is a system
    transition of the dispatcher in its own transaction and idempotent: it changes nothing unless
    `first_published_at IS NULL`; otherwise it sets `first_published_at` = `at` and, when the Alert Group is firing and
    the Route's ack timeout is on, sets `ack_timeout_started_at` = `at`, resets `ack_timeout_notices_sent` and
    `unclaimed`, and writes the `ack_timeout` timer for notice 1 at `at` + F. A republication after the Root message
    was deleted (S-035) finds `first_published_at` set and never starts the ack timeout over. A Storm summary is not a
    Publication of the Alert Groups it covers (S-035). For an events-mode outgoing webhook, which has no Root message,
    the delivered `created` event counts as the Publication, with `at` = its `delivered_at`
    (`internal/delivery/webhookevents.go`); while `first_published_at` is empty, any other delivered event of the Alert
    Group calls it too, with the `created` event's `delivered_at` when that was delivered there and otherwise its own
    `delivered_at` — a webhook added to a Route with open Alert Groups sends no `created` for them, and a `created`
    event may end `not_delivered`; in template mode the "create" request is the Publication as for a
    messenger. A replica that stops between the commit and the call leaves `first_published_at` empty: every later
    delivered call or event of that Alert Group repeats the call while it is empty.
  - *Start over*: every transition into firing of an Alert Group that has `first_published_at` — Reopen into firing,
    Unacknowledge, Unsnooze, Unresolve, a Snooze ending, a rise to Urgent that removes the acknowledgement or ends a
    Snooze, auto-unacknowledge, the release of a disabled or deleted Owner (S-063) — does the same with the
    transition's time. Starting over and stopping are not hooks in each transition: the dispatcher compares the status,
    the Owner and `first_published_at` before and after every change and keeps the timers with them in one place
    (`syncTimers` in `internal/groups/timers.go`, beside `syncSnoozeTimer`), so `transitions.go`, `snooze.go`,
    `release.go` and `commands.go` need no code of their own.
  - *Stop*: Acknowledge (Takeover included), Snooze and Resolve, by a person or the system, delete the `ack_timeout`
    timer and clear `unclaimed`.
  - *Notice*: the timer records `ack_timeout` (Loud, Mentions `[ack_timeout]`, kind `timers`, `notice_number`), and
    reschedules itself for the next notice; after notice 3 it records `unclaimed` (Quiet, no Mentions), sets
    `unclaimed`, and is deleted. `unclaimed` re-renders the Root message with the notice "⚠ Nobody has taken this",
    which `messages/default.go` already shows for an Unclaimed Alert Group (S-036) and the next status change removes.
- **Reminders** (C-17.FR-4, FR-8): Acknowledge from firing or snoozed, and a Takeover, reset `reminders_sent`,
  `reminders_unanswered`, `last_reminder_at` and `owner_answered_at` and write the `reminder` timer at the transition's
  time + B; Unacknowledge, Snooze, Resolve, auto-unacknowledge and the release of a disabled or deleted Owner delete
  it. A Reopen into acknowledged continues the sequence: the counters are kept and the next Reminder is due one
  interval of the next step after the Reopen. The timer records `reminder` (Loud, Mentions `[owner]`, kind `timers`,
  `notice_number` = k), sets `last_reminder_at` and reschedules itself. Disabling or deleting the Owner releases the
  Alert Group (S-063) and so, through the same timer sync of the dispatcher, deletes the timer: no Reminder and no
  auto-unacknowledge follow, and the
  release's Loud Thread reply carries the text of S-036; enabling the User again writes no timer, and the next
  Acknowledge starts the Reminders over. A `reminder` timer that comes due on an Alert Group that is no longer
  acknowledged, or whose Route's Reminders are off, is deleted with `reminder_skipped`. There are no quiet hours.
- **Answers and auto-unacknowledge** (C-17.FR-5): `owner_answered_at` is set when the Owner answers "Still on it",
  runs any command on the Alert Group, or adds a Note to it: the dispatcher sets it for every change that writes made
  by the User who owned the Alert Group before the change (`markAnswered` in `timers.go`; `notes.go` needs no code of
  its own), and an Acknowledge by the current Owner, which changes nothing, does not count. When a Reminder is due, the
  previous one counts as unanswered if it reached at least one Destination — a `thread_replies` row of that `reminder`
  event in state `sent`, or a `webhook_events` row of it in state `delivered` for an events-mode outgoing webhook — and
  `owner_answered_at` is not later than it; `reminders_unanswered` is then incremented, otherwise set to 0. The check is
  `delivery.ReminderReached` (`threads.go`), which the runtime injects into the dispatcher as a function, because
  `internal/delivery` imports `internal/groups` and not the other way round. When `route.auto_unacknowledge` is on and
  `reminders_unanswered` reaches `timers.auto_unacknowledge_after`, the due Reminder is not sent: the Alert Group
  becomes firing without an Owner with `auto_unacknowledged` (Loud, Mentions `[owner]` — the Owner who loses it,
  C-09.FR-22 — kind `status`, `reason` "No answer to the last two Reminders", `previous_owner_user_id`), and the ack
  timeout starts over. Without a Destination, or when every Destination is Broken, Reminders never reach anyone and
  auto-unacknowledge never happens.
- **"Still on it"** (C-17.FR-10, C-04.FR-2; `answerReminder`): a command of the dispatcher, marked
  `x-permission-check: dispatcher`, so the dispatcher checks the Permission `alert-groups:acknowledge`; refused as
  `409 command-refused` with `owner_must_be_user` for a Service account (checked first), `not_owner` when the caller is
  not the Owner, and `no_reminder_pending` unless `reminders_sent` > 0 and `owner_answered_at` is before
  `last_reminder_at`. Done: `reminder_answered` (Quiet, kind `timers`), `owner_answered_at`, the Audit log entry
  `alert_group.reminder_answered`, `CommandResult` with `done`, counted by `muster_commands_total` with
  `command="still_on_it"`; the Reminder schedule does not move. A Personal access token acts as its User.
  `allowed_commands` contains `still_on_it` exactly when the caller is the Owner and a Reminder waits; a Service
  account never gets it. S-068 runs the same command from the Reminder buttons.
- **Collapse after downtime** (C-17.FR-7, C-02.FR-12): when a timer fires and more than one notice or Reminder of it is
  already due — after downtime, or a replica that stalled past its lease — the dispatcher records one `notices_missed`
  (Loud, kind `timers`, Mentions `[ack_timeout]` for ack timeout notices and `[owner]` for Reminders, `missed_count`)
  instead of the single entries, advances the counters by that number, records `unclaimed` when the third notice is
  among them, and schedules the next one from now. A collapsed Reminder reply counts as one Reminder for
  auto-unacknowledge. `timer_fired` logs `missed`.
- **Lifecycle events** (C-17.FR-11, C-11.FR-20): the six rows join the closed table of `groups/events.go`, which now
  owns their `groups.Event` constants; the constants that S-034 declared in `internal/delivery/enqueue.go` move there,
  and delivery's table keeps mapping them: `ack_timeout`, `reminder`, `auto_unacknowledged` and `notices_missed` become
  Thread replies, `unclaimed` a Root message update, `reminder_answered` nothing. `events_test.go` checks every row.
- **Hints**: every timer transition — a notice, Unclaimed, a Reminder, auto-unacknowledge, a collapse,
  `reminder_answered` — and `MarkPublished` send the `alert-group` hint of the Alert Group, and a change of `unclaimed`
  also `alert-groups`, so that the pages of S-050 follow without a reload.
- **Messages** (C-12.FR-3, FR-4; `messages/replies.go`): an ack timeout notice renders the Route's `ack_timeout_notice`
  template (S-036) in the sandbox, or the built-in one, in the Route's language with the data of the Root message (the
  notice number is in the Timeline only), and carries only links; a failing template falls back as for the Root
  message (`muster_template_errors_total{template="ack_timeout_notice"}`, `MusterTemplateError`) and records
  `fallback_template_used` through `groups.RecordSystemEntry` (`groups/system.go`) in the transaction that prepares the
  reply. A Reminder reply carries the Owner's Mention and the text "Reminder: {user}, are you still on
  it?"; S-068 adds its buttons. `auto_unacknowledged` gives the reason with "The acknowledgement was released: nobody
  answered the Reminders."; `notices_missed` says "{count} notices were missed while Muster was unavailable."
- **Existing scaffolding** (reuse and extend, do not duplicate): the texts `reply.unclaimed`,
  `reply.reminder`, `reply.autoUnacknowledged`, `reply.noticesMissed_*` and `notice.unclaimed`; the reply cases of
  `messages/replies.go`; `buttons.CommandStillOnIt`, `buttons.SubjectReply` and the Mattermost action id `stillonit`
  (used by S-068); the `command` value `still_on_it` of `muster_commands_total`; `CommandNameStillOnIt` in the
  generated API.
- **Read** (C-17.FR-9, C-09.FR-1, C-09.FR-13; `groups/read.go`, `list.go`, `counts.go`, `filters.go`): `unclaimed`
  from the column;
  `next_notice` from the Alert Group's `ack_timeout` or `reminder` timer (`kind`, `at` = its deadline), absent when
  there is none; `unclaimed=true` filters the list and the counts.
- **Log events and metrics**: `timer_fired` (INFO: `kind`, `group`, `notice`, `missed`), `reminder_skipped` (INFO:
  `group`, `reason` — `not_acknowledged` or `policy_off`); `muster_commands_total` with the `command` value
  `still_on_it` of the [metrics catalogue](../prd/l1/reference.md#metrics-catalogue).
- **Defaults**: `route.ack_timeout`, `route.reminders`, `route.auto_unacknowledge`, `timers.auto_unacknowledge_after`.

## Steps

1. Write the schedules. Check: `schedule_test.go` gives 15, 45 and 105 minutes and 4, 12, 28, 52 and 76 hours, and the
   collapsed counts for a late firing.
2. Move the timer event constants into `groups/events.go`; write the timer kinds, `MarkPublished` with its post-commit
   call from the delivery outcome and from a delivered `created` event, starting over and stopping in every transition,
   Unclaimed and the lifecycle events. Check: `timers_test.go` with a manual clock covers C-17.AC-1, AC-2, AC-8,
   C-09.FR-8, a second call of `MarkPublished` and a republication after deletion; `outcomes_test.go` shows the call
   made after the commit and not inside it; `events_test.go` covers C-17.AC-9.
3. Write Reminders, the answer rule with `ReminderReached`, auto-unacknowledge, the end of Reminders on an Owner's
   release and the collapse. Check: `timers_test.go` covers C-17.AC-3, AC-5, C-17.FR-8, a Reminder that reached only a
   Broken Destination, a Reminder delivered only as an events-mode `reminder` event, and a collapse of three notices.
4. Write "Still on it", `allowed_commands`, the `unclaimed` filter and `next_notice`. Check: `commands_test.go` covers
   the three refusals and a Personal access token; `TestDispatcherPermissionCheck` lists `answerReminder`.
5. Write the ack timeout notice rendering and the plain Reminder replies. Check: `render_test.go` covers the notice
   template, its fallback and the Reminder reply.
6. Write `test/e2e/timers_test.go`. Check: Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-061 and S-032: the Admin's session (`jar`, `H`), the Responder "bob" with his session (`HB`), the Service
# account token SAT (Role responder), the Integration "lab" (INT) with NOTIFY, ADV, AG and PUTA, and the On-call policy
# in P; the Mattermost Connection "mm" (C), the Destination "alerts" (D) of S-039 with `ack_timeout` set to `@channel`;
# the Telegram Destination "alerts" (TD) of S-042 with its discussion group GR. No Account link is needed: every
# command below goes through the API. The clock moves by hours and days below, longer than `auth.session_idle_timeout`,
# so every call after this point uses a Personal access token made before the clock moves: the Admin's in A, as in
# S-023, and bob's in B.
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake; FMM=127.0.0.1:18065/_fake; FTG=127.0.0.1:18081/_fake
CLK=localhost:8082/_dev/clock; GR=-1001000000002
PAT=$(curl -s "${H[@]}" -d "{\"name\":\"verify\",\"permissions\":$(curl -s -b jar $API/me | jq -c .permissions)}" \
  $API/me/personal-access-tokens | jq -r .value)
BPAT=$(curl -s "${HB[@]}" -d "{\"name\":\"verify\",\"permissions\":$(curl -s -b bob $API/me | jq -c .permissions)}" \
  $API/me/personal-access-tokens | jq -r .value)
A=(-H "Authorization: Bearer $PAT" -H 'Content-Type: application/json')
B=(-H "Authorization: Bearer $BPAT" -H 'Content-Type: application/json')
VIEW() { curl -s "${A[@]}" "$API/integrations/$INT/alerts?$1"; }
TL() { curl -s "${A[@]}" "$API/alert-groups/$1/timeline?kind=timers&order=asc" | jq -c "[.items[] | {event, n: .notice_number, loudness, mentions}]$2"; }
DUE() { curl -s "${A[@]}" "$API/alert-groups/$1" | jq -c --arg now "$(curl -s $CLK | jq -r .now)" \
  '{kind: .next_notice.kind, min: (if .next_notice then (((.next_notice.at | .[0:19] + "Z" | fromdate) - ($now | .[0:19] + "Z" | fromdate)) / 60 | round) else null end), unclaimed}'; }
ROUTE() { curl -s "${A[@]}" $API/routes -d "{\"name\":\"$1\",\"matchers\":[{\"label\":\"team\",\"op\":\"=\",\"value\":\"$1\"}],\"urgent\":false,
  \"group_key\":[\"alertname\",\"host\"],\"destination_ids\":[\"$D\",\"$TD\"],\"policy\":$2}" | jq -r .id; }
FIRE() { curl -s -X PUT $FAM/groups/$1 -d "{\"receiver\":\"lab\",\"route\":\"{}\",\"labels\":{\"alertname\":\"$2\"}}" > /dev/null
  PUTA $1 a "{\"labels\":{\"team\":\"$3\",\"host\":\"$1\"}}"; NOTIFY $1 '{"reason":"first notification"}'; AG "host%3D%22$1%22"; }
ROUTE t "$P" > /dev/null

# C-17.AC-1, AC-6, FR-3, FR-9: notices at 15, 45 and 105 minutes, then Unclaimed
G1=$(FIRE g1 DiskFull t); DUE $G1                          # {"kind":"ack_timeout","min":15,"unclaimed":false}
ADV 900;  TL $G1 '| last'                                   # {"event":"ack_timeout","n":1,"loudness":"loud","mentions":["ack_timeout"]}
DUE $G1                                                     # {"kind":"ack_timeout","min":30,"unclaimed":false}
curl -s $FMM/posts | jq -c '[.[] | select(.root_id != "")] | last | {m: (.message | startswith("@channel")), b: (.props.attachments[0].actions // [] | length)}'
# {"m":true,"b":0}                                          (Loud, Mention by the Destination's setting, links only)
ADV 1800; DUE $G1                                           # {"kind":"ack_timeout","min":60,"unclaimed":false}
ADV 3600; TL $G1 '| map(.event)'                            # ["ack_timeout","ack_timeout","ack_timeout","unclaimed"]
DUE $G1                                                     # {"kind":null,"min":null,"unclaimed":true}
curl -s "${A[@]}" "$API/alert-groups?unclaimed=true" | jq -r '.items[].id' | grep -c "$G1"   # 1
curl -s "${A[@]}" "$API/alert-group-counts?unclaimed=true" | jq .all                         # 1
curl -s $FMM/posts | jq -r "[.[] | select(.root_id == \"\")] | first | .props.attachments[0].text" | grep -c 'Nobody has taken this'   # 1

# C-17.AC-8: acknowledging ends Unclaimed; after Unacknowledge it comes back only after the third new notice
curl -s "${B[@]}" -X POST $API/alert-groups/$G1/acknowledge > /dev/null; sleep 1; DUE $G1   # {"kind":"reminder","min":240,"unclaimed":false}
curl -s $FMM/posts | jq -r "[.[] | select(.root_id == \"\")] | first | .props.attachments[0].text" | grep -c 'Nobody has taken this'   # 0
curl -s "${B[@]}" -X POST $API/alert-groups/$G1/unacknowledge > /dev/null
ADV 900; ADV 1800; DUE $G1                                  # {"kind":"ack_timeout","min":60,"unclaimed":false}
ADV 3600; DUE $G1                                           # {"kind":null,"min":null,"unclaimed":true}

# C-17.AC-2: acknowledge at minute 20, unacknowledge at minute 30 -> next notice at minute 45
G2=$(FIRE g2 QueueFull t); ADV 1200; TL $G2 '| length'      # 1
curl -s "${B[@]}" -X POST $API/alert-groups/$G2/acknowledge > /dev/null; ADV 600
curl -s "${B[@]}" -X POST $API/alert-groups/$G2/unacknowledge > /dev/null; DUE $G2   # {"kind":"ack_timeout","min":15,"unclaimed":false}

# C-09.FR-8: a Snooze that ends starts the ack timeout over
G9=$(FIRE g9 CertExpiry t)
curl -s "${A[@]}" -X POST $API/alert-groups/$G9/snooze -d "{\"until\":\"$(curl -s $CLK | jq -r '.now | .[0:19] + "Z" | fromdate + 3600 | todate')\"}" > /dev/null
ADV 3600; curl -s "${A[@]}" "$API/alert-groups/$G9/timeline?limit=1" | jq -c '.items[0] | {event, loudness}'   # {"event":"snooze_ended","loudness":"loud"}
DUE $G9                                                     # {"kind":"ack_timeout","min":15,"unclaimed":false}

# C-17.AC-3, FR-10: Reminders at 4, 12, 28 and 52 hours; "Still on it" at hour 5 does not move them
G3=$(FIRE g3 HostDown t); curl -s "${B[@]}" -X POST $API/alert-groups/$G3/acknowledge > /dev/null
curl -s "${B[@]}" -X POST $API/alert-groups/$G3/still-on-it | jq -r .code       # no_reminder_pending
ADV 14400; TL $G3 '| map(select(.event == "reminder")) | last'   # {"event":"reminder","n":1,"loudness":"loud","mentions":["owner"]}
curl -s "${B[@]}" $API/alert-groups/$G3 | jq -c '.allowed_commands | index("still_on_it") != null'   # true
curl -s "${A[@]}" $API/alert-groups/$G3 | jq -c '.allowed_commands | index("still_on_it") != null'   # false
curl -s "${A[@]}" -X POST $API/alert-groups/$G3/still-on-it | jq -r .code         # not_owner
curl -s -H "Authorization: Bearer $SAT" -X POST $API/alert-groups/$G3/still-on-it | jq -r .code   # owner_must_be_user
ADV 3600; curl -s "${B[@]}" -X POST $API/alert-groups/$G3/still-on-it | jq -r .outcome   # done
DUE $G3                                                     # {"kind":"reminder","min":420,"unclaimed":false}
ADV 25200; ADV 57600; ADV 86400; TL $G3 '| map(select(.event == "reminder") | .n)'   # [1,2,3,4]
curl -s $FMM/posts | jq -c '[.[] | select(.root_id != "" and (.message | test("bob, are you still on it")))] | last | (.props.attachments[0].actions // []) | length'
# 0                                                          (the Reminder at hour 52 mentions the Owner; its buttons are S-068)
curl -s "$FTG/messages?chat=$GR" | jq '[.[] | select(.text // "" | test("are you still on it"))] | length > 0'   # true

# C-17.AC-3, C-10.FR-4: a Takeover at hour 13 moves the next Reminder to hour 17
G4=$(FIRE g4 HostDown2 t); curl -s "${A[@]}" -X POST $API/alert-groups/$G4/acknowledge > /dev/null
ADV 14400; ADV 28800; ADV 3600; curl -s "${B[@]}" -X POST $API/alert-groups/$G4/acknowledge > /dev/null
DUE $G4                                                     # {"kind":"reminder","min":240,"unclaimed":false}

# C-17.AC-4: the Informational profile gives no notices and no Reminders
PI=$(curl -s "${A[@]}" $API/route-profiles | jq -c '.items[] | select(.id == "informational") | .policy'); ROUTE info "$PI" > /dev/null
G6=$(FIRE g6 Info info); DUE $G6                            # {"kind":null,"min":null,"unclaimed":false}
ADV 7200; curl -s "${B[@]}" -X POST $API/alert-groups/$G6/acknowledge > /dev/null; ADV 18000; TL $G6 '| length'   # 0

# C-17.AC-5: auto-unacknowledge after two unanswered Reminders; a Note at hour 5 keeps the Reminder at hour 28
ROUTE au "$(jq -c '.auto_unacknowledge = true' <<<"$P")" > /dev/null
G7=$(FIRE g7 Disk au); G8=$(FIRE g8 Disk2 au)
for g in $G7 $G8; do curl -s "${B[@]}" -X POST $API/alert-groups/$g/acknowledge > /dev/null; done
ADV 14400; ADV 3600
curl -s "${B[@]}" $API/alert-groups/$G8/notes -d '{"body":"Still checking the disk."}' > /dev/null
ADV 25200; ADV 57600
curl -s "${A[@]}" $API/alert-groups/$G7 | jq -c '{status, owner}'          # {"status":"firing","owner":null}
curl -s "${A[@]}" "$API/alert-groups/$G7/timeline?kind=status&limit=1" | jq -c '.items[0] | {event, reason, loudness, mentions}'
# {"event":"auto_unacknowledged","reason":"No answer to the last two Reminders","loudness":"loud","mentions":["owner"]}
curl -s $FMM/posts | jq -r '[.[] | select(.root_id != "")] | map(.message) | map(select(test("nobody answered the Reminders"))) | last' | grep -c 'bob'   # 1
DUE $G7                                                     # {"kind":"ack_timeout","min":15,"unclaimed":false}
TL $G8 '| map(select(.event == "reminder") | .n)'           # [1,2,3]

# C-17.FR-7: notices overdue together collapse into one reply
G5=$(FIRE g5 Overdue t)
ROOT5=$(curl -s $FMM/posts | jq -r --arg g "$G5" '.[] | select(.root_id == "" and (.props.attachments[0].title_link | test($g))) | .id')
ADV 7200; curl -s "${A[@]}" "$API/alert-groups/$G5/timeline?kind=timers&order=asc" | jq -c '[.items[] | {event, missed_count}]'
# [{"event":"notices_missed","missed_count":3},{"event":"unclaimed","missed_count":null}]
curl -s $FMM/posts | jq --arg r "$ROOT5" '[.[] | select(.root_id == $r)] | length'   # 1
grep '"event":"timer_fired"' dev.log | grep "\"group\":\"$G5\"" | grep -c '"missed":3'   # 1
```

`test/e2e/timers_test.go` repeats these steps and adds: C-17.FR-8 and C-03.AC-27 (an Owner who is disabled, and one
whose account is deleted: the Alert Group is firing without an Owner, the Loud reply is in the fake Mattermost Thread,
no Reminder follows and notice 1 of the new ack timeout comes F after the release; enabling the User again brings back
neither the acknowledgement nor the Reminders), C-17.FR-5 (with every Destination of the Route Broken, Reminders at 4
and 12 hours reach nobody and no auto-unacknowledge follows), `muster_commands_total{command="still_on_it",outcome="done"}`
after an answer, the start over after Reopen into firing, Unsnooze, Unresolve and a rise to Urgent, a Reopen into
acknowledged that continues the Reminder steps, an Alert Group on a Route without Destinations that has no ack timeout
until a Destination is added and it is published, a Root message deleted in the fake Mattermost and republished
without the ack timeout starting over, a Route whose only Destination is an events-mode outgoing webhook (the ack
timeout starts at the delivered `created`; a delivered `reminder` counts as reached, so auto-unacknowledge follows two
unanswered ones), and C-17.AC-9 (one entry per row, with its kind, loudness and Mentions).

## Open questions

None.

## Notes

- Suggested commit: `feat(timers): add ack timeouts, unclaimed, reminders and auto-unacknowledge`.
- Split (D298): the plan review of the phase counted about 58 hand-written files with those the contract had missed,
  so the Reminder buttons, the presses on Thread replies and the expired-button answer moved to S-068; this story keeps
  55 hand-written files: the contract had missed the fakes and integration tests that existing tests needed for the new
  columns and queries, the list and count reads, `bulk.go` (an exhaustive switch over the commands), `system.go`,
  delivery's `worker.go` and `render.go`, and the message query and texts; the review added migration `0007`, its two
  version tests and `schema.md`, so that an upgrade does not collapse the notices of every open Alert Group at once.
  S-050 depends on this story only; S-051
  depends on S-068.
- `MarkPublished` runs after the delivery outcome has committed (D298): the outcome transaction locks `deliveries`
  before `alert_groups`, and a dispatcher transition locks `alert_groups` before `deliveries` (`Enqueue`), so doing both
  in one transaction can deadlock.
- The worker never sends anything itself: every notice is a lifecycle event of the dispatcher, and delivery turns it
  into replies, so a Broken Destination drops the replies that came due meanwhile (C-11.FR-19) like any other.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-17.FR-1 | full | the first Publication of a messenger, of template mode and, for events mode, the delivered `created` event (or the first delivered event without one) |
| C-17.FR-2 | partial | the behaviour; the Route editor fields are S-050 |
| C-17.FR-3 | partial | the Root message, the field and the filter in the API; the list filter, column and badge are S-050 |
| C-17.FR-4 | partial | the Reminders and their schedule; the buttons on them are S-068 |
| C-17.FR-5 | full | |
| C-17.FR-7 | full | |
| C-17.FR-8 | full | Reminders end and the ack timeout starts over when a disabled or deleted Owner is released |
| C-17.FR-9 | partial | `next_notice` in the API; the page is S-050 |
| C-17.FR-10 | partial | the API; the messenger buttons are S-068, the page S-050, links made through the profile S-051 |
| C-17.FR-11 | full | |
| C-17.AC-1 | full | |
| C-17.AC-2 | full | |
| C-17.AC-3 | full | |
| C-17.AC-4 | full | |
| C-17.AC-5 | full | |
| C-17.AC-6 | partial | the API filter; the list page is S-050 |
| C-17.AC-8 | partial | the Root message and the API; the UI is S-050 |
| C-17.AC-9 | full | |
| C-09.FR-1 | partial | the Unclaimed flag |
| C-09.FR-8 | full | together with S-063: the ack timeout starts over when a Snooze ends |
| C-09.FR-11 | partial | the timer entries |
| C-09.FR-13 | partial | the `unclaimed` filter in the API; the list page is S-050 |
| C-10.FR-4 | partial | Reminders start over for the new Owner |
| C-10.FR-16 | partial | `still_on_it`; the button is S-050 |
| C-04.FR-2 | full | together with S-016 and S-032: "Still on it" refused for Service accounts |
| C-03.FR-13 | partial | the ack timeout and Reminders after the release of a disabled or deleted Owner; the release is S-063 |
| C-02.FR-12 | partial | collapsed overdue notices and Reminders |
| C-11.FR-20 | partial | the C-17 rows are produced |
| C-12.FR-4 | partial | the ack timeout notice template rendered at runtime |
| C-15.AC-11 | partial | the timer rows, produced by the running timers; every row in the table-driven test of S-044 |
| C-03.AC-27 | partial | the Loud Thread reply in the fake Mattermost and the ack timeout starting over; the release and its Timeline entry are S-063 |
