# C-34. Maintenance windows

[L2 index](../L2.md) · Release: a 0.2.x patch release · UI: yes · Depends on: C-03, C-05, C-08, C-09, C-11, C-15, C-17,
C-25, C-26, C-30, C-31, C-32

**Status.** Draft. The behaviour that ADR-0018 and journal D493, D511, D512, D523, D530 and D535 decided is written as
requirements; what they leave open is listed under [Open questions](#open-questions), each with a recommended answer,
and every requirement that depends on one names it. The questions are settled by the maintainer before the stories of
this capability are written; the answers then move into L2 §5.2 and §5.3, and the rows of the shared tables — the
lifecycle events, the Permissions, the metrics and the banners of [reference.md](../l1/reference.md) — are added with
them, since most of those rows depend on the answers.

**Goal.** Let people say ahead of a deploy or an upgrade that the Alerts of an Integration, a Route or a Team are
expected: while a Maintenance window keeps an Alert Group back, Muster goes on ingesting and grouping and its status
changes as usual, but nothing is delivered for it and no Escalation chain Pages anyone; when the window ends, the Alert
Groups still firing are published with a mark, and those that resolved meanwhile never are. One team's window never
keeps back an Alert Group that another team shares (ADR-0018).

Names in the form `area.setting` refer to rows of [defaults.md](../l1/defaults.md); the lifecycle events are rows of the
[L2 lifecycle event table](../l1/reference.md#lifecycle-events-added-in-l2) once the open questions are settled.

## Scenarios

1. Before a deploy of payments, a CI pipeline opens a Maintenance window on the Team "payments" for 30 minutes through
   the API, with the token of a Service account whose Role is Responder. `PaymentsAPIDown` fires during the deploy: its
   Alert Group is created and its Alerts are on its page, but nothing appears in `payments-critical` or in the
   operations Destinations, and no Escalation chain Pages anyone. The deploy succeeds, the Alerts resolve, the pipeline
   ends the window early, and the Alert Group is never posted.
2. The same deploy fails, and `PaymentsAPIDown` still fires when the window ends: its Alert Group is published in every
   Destination it reaches, marked "Published after a Maintenance window that ended at 14:30", and the chains of payments
   start at that Publication.
3. `KubePodCrashLooping` fires in production with Alerts of payments and of analytics in one Alert Group. The window of
   payments does not keep it back, because analytics is not in a window: analytics is Paged as usual.
4. An Admin plans a window on the Integration "am-prod" for Saturday 02:00–04:00, an upgrade of Alertmanager. The
   Integration page lists it as planned; from 02:00 it shows "In a Maintenance window until 04:00: 3 Alert Groups kept
   back.", and the Alert Group page of each of them says why it is kept back.
5. During a window on the Route "Databases", a critical Alert Group arrives; it passes through the window and is
   published at once only where the setting that lets Urgent Alert Groups through is on ([open question
   1](#open-questions)).

## Functional requirements

### What a window is and who manages it

- **C-34.FR-1** A Maintenance window covers exactly one Integration, one Route or one Team, from a start to an end, with
  a reason as free text and the setting that lets Urgent Alert Groups through, wherever that setting lives ([open
  question 1](#open-questions)). It is one row with a start and an end, which any replica handles like a timer row
  (ADR-0007; journal D535): a window that starts now has its start at the time it is created, and a planned one starts
  later, such as Saturday 02:00–04:00 (ADR-0018; journal D493). Windows do not repeat ([open question
  2](#open-questions)); whether several windows may cover one object at once is [open question 13](#open-questions). A
  window is planned before its start, active from its start until its end, and ended afterwards, by the business clock
  (C-02), so that the development clock moves windows as it moves every other domain time. A window always has an end:
  it covers its object "from now until a time, or over a planned span" (ADR-0018).
- **C-34.FR-2** Admins and Responders open, change and end windows with the Permission `maintenance:write`, and a CI
  pipeline uses a Service account whose Role is Responder (ADR-0018; journal D535). Permissions stay global: a Responder
  may open a window on any Team, whether or not they are in it, since only Overrides check who is in a Team (ADR-0018).
  Which Permission reads windows is [open question 14](#open-questions).
- **C-34.FR-3** A window is opened from now or planned, changed and ended through the API and the UI. Its end is later
  than its start. Which fields may change once it is active, whether a window may be deleted, what ending early does to
  its end, what happens to its windows when an Integration, a Route or a Team is deleted, and the limits on its length
  and on how far ahead it may be planned are [open questions 11 and 13](#open-questions). Windows use optimistic locking
  (`If-Match`), as every configuration does.
- **C-34.FR-4** The API takes the start and the end as RFC 3339 times with an offset and returns them in UTC; the UI
  enters and shows them in the time zone of the person's profile (C-03.FR-12), with the date where it is not today.
- **C-34.FR-5** Opening and closing a window are recorded in the Audit log (ADR-0018; journal D493), and so is every
  other change of a window, with a before/after diff, the actor and the Transport, as every change of configuration is.
  The start and the end that the business clock reaches are not actions of anyone: they are log events (FR-21) and
  lifecycle events of the Alert Groups the window keeps back (FR-13).

### Which Alert Groups a window keeps back

- **C-34.FR-6** A window of a Route keeps back every open Alert Group of that Route (ADR-0018). A window of an
  Integration keeps back every open Alert Group whose Alerts all come from that Integration; whether an Alert Group with
  Alerts from several Integrations (C-09.FR-1) is kept back while only one of them is in a window is [open question
  6](#open-questions). An Alert Group moved to the Default route (C-09.FR-19) is covered by the windows of the Default
  route from then on.
- **C-34.FR-7** A window of a Team keeps an Alert Group back — in all its Destinations, the operations Destinations
  included — only while every Team of the Alert Group (C-25.FR-6) is in a window; the Team that owns its Route is not
  counted, and an Alert Group without a Team is never kept back by the window of a Team (ADR-0018; journal D523, D530).
  A Team that joins an Alert Group (C-25.FR-6) may make it no longer kept back, when the new Team is not in a window,
  but never stops the Escalation chains already running on it (ADR-0018; journal D530).
- **C-34.FR-8** An Alert Group is kept back while at least one rule of FR-6 and FR-7 holds for it and FR-9 does not let
  it through. Whether it is kept back is decided again whenever something these rules read changes: it is created, an
  Alert joins it, one of its Teams joins, it moves to another Route, its urgency changes, and a window that covers it
  starts, ends, is changed or goes away. An open Alert Group that a window covers when the window starts is kept back
  from then, whether or not it was already published (ADR-0018); what then happens to its running chains, ack timeout
  and Reminders and to its Root messages is [open questions 3 and 4](#open-questions).
- **C-34.FR-9** An Urgent Alert Group (C-26.FR-8) passes through a window that lets Urgent Alert Groups through, and an
  Alert Group that such a window keeps back stops being kept back by it as soon as it becomes Urgent, whether by its
  Alerts or by its Route's Minimum severity (ADR-0018; journal D493, D511). Where the setting lives, how windows with
  different settings combine, and whether an Alert Group whose Severity level falls below critical is kept back again,
  are [open question 1](#open-questions).

### While an Alert Group is kept back

- **C-34.FR-10** Ingestion and grouping go on: Snapshots are processed, the Alerts view and the Alert Group page show
  every Alert, Alerts join and resolve, the Reopen window and the Grace period apply, the system resolves the Alert
  Group, its Severity level and its Teams follow its Alerts, and unknown values of the Team label are recorded (C-06,
  C-09, C-25). Its status changes as usual (ADR-0018; journal D523): a Snooze ends, a rise to critical ends a Snooze or
  an acknowledgement as C-09.FR-9 says, and people run Commands from the UI, the API and the buttons of Root messages
  that already exist, answered on the interactive path (ADR-0005). Every change is in its Timeline.
- **C-34.FR-11** Nothing is delivered for it (ADR-0018): no Root message is published, `destination_joined` waits for
  the end of the window (ADR-0005, C-26.FR-5; journal D512), and no Thread reply is sent — new Alerts, Reopens, the end
  of a Snooze, a rise to Urgent, ack timeout notices, Reminders, Pages and the replies of a resolution by the system —
  so nobody is mentioned. Whether the Root messages it already has are still edited is [open question
  4](#open-questions); whether outgoing webhooks in events mode receive its events is [open question
  8](#open-questions); and whether it counts toward its Route's Storm is [open question 7](#open-questions). The
  Destination test (C-16), Destination checks and the direct messages of Account links are not about an Alert Group
  and go on.
- **C-34.FR-12** Its timers and its chains wait (ADR-0004, ADR-0018; journal D507): no Escalation chain starts on it
  and no step is made, no action of a Notification rule (C-31) and no call through a Notification gateway (C-30) is made
  for it, no `escalation_webhook` is sent, its ack timeout sends no notice and it gets no Reminder, so no
  auto-unacknowledge follows. An Alert Group created while kept back has had no Publication, so its ack timeout and its
  chains have not started (C-17.FR-1, C-32.FR-5); one created while kept back with no Destination that can publish it —
  none, or only Broken ones — does not start its chains at its creation either, as C-32.FR-5 would otherwise have it.
  What happens to the chains, the ack timeout and the Reminders that were running when it became kept back is [open
  question 3](#open-questions).
- **C-34.FR-13** Becoming kept back records the lifecycle event `maintenance_started`, naming the windows that keep it
  back; no longer being kept back records `maintenance_ended`, naming the cause: a window ended or was closed early or
  changed, the Alert Group became Urgent, a Team joined, or it moved to another Route. Both are of the Timeline kind
  `system` and are system transitions of the dispatcher with Muster as the actor (ADR-0016; journal D513). Their
  loudness, Mentions and how they appear in a messenger are [open question 5](#open-questions).

### When it stops being kept back

- **C-34.FR-14** When the business clock reaches a window's end, or a person closes it early, every Alert Group that the
  window kept back is decided again (FR-8), on any replica (ADR-0007): one that another window still keeps back stays
  so until that one ends.
- **C-34.FR-15** An Alert Group that stops being kept back after it resolved is not published (ADR-0018): the delivery
  state of each Destination where it was never published says "Not posted: resolved during a Maintenance window", and
  its Timeline has the whole story.
- **C-34.FR-16** An Alert Group that stops being kept back while firing and was never published is published in every
  Destination it reaches now, the Destinations it reached meanwhile included, as part of that Publication and without a
  `destination_joined` of its own (C-26.FR-5), with the mark "Published after a Maintenance window that ended at
  {time}" on its Root messages (ADR-0018; journal D493). The Publications go through the limiters, Urgent Alert Groups
  first (C-11.FR-3). This Publication is its first Publication: where nobody owns it, its ack timeout and its chains
  start at it, as C-17.FR-1 and C-32.FR-5 say; where no Destination can publish it, its chains start when it stops
  being kept back. Whether an acknowledged or snoozed one is published, and how loud each Publication is, are [open
  question 5](#open-questions).
- **C-34.FR-17** For an Alert Group that was already published when it became kept back, each Destination that it
  reached meanwhile — by a rise in Severity level or a Team that joined — records `destination_joined` once it stops
  being kept back: Loudly, with the Mentions of the entries for `new_alert_group`, if it is firing; Quietly if it is
  acknowledged or snoozed (ADR-0005, ADR-0018, C-26.FR-5; journal D512). Its Root messages are brought to its current
  state. Whether anything else is sent, and whether its chains or its ack timeout start over, are [open questions 3 and
  5](#open-questions).

### UI, API and observability

- **C-34.FR-18** The pages of an Integration, a Route and a Team have a section "Maintenance windows": "Start
  maintenance", which opens a window from now until a time chosen from presets or entered ([open question
  11](#open-questions)), "Plan a window", the reason, the setting of [open question 1](#open-questions), and the active
  and planned windows with "End now" and "Change". While a window is active, the page shows "In a Maintenance window
  until {time}: {N} Alert Groups kept back." (journal D102, D493). The Team section says "Keeps back an Alert Group only
  while every Team of its Alerts is in a Maintenance window: Alert Groups shared with Teams outside a window are still
  delivered." A page "Maintenance windows" lists the active, planned and recently ended windows of the Organization
  with their object, start, end, reason and who opened them, filtered by state and by object type.
- **C-34.FR-19** The Alert Group page of a kept-back Alert Group shows "Kept back by the Maintenance window of {object}
  until {time}: nothing is delivered and no Escalation chain runs." with a link to the window; the Alert Group list
  gains the filter and the badge "Kept back". Both work 360 CSS pixels wide without horizontal scrolling (NFR-15). Every
  text exists in English and Russian (NFR-8).
- **C-34.FR-20** The API has the resource `maintenance-windows` with the `public_id` prefix `MW`: list, with cursor
  pagination and filters by object and state; create; read; update, with `If-Match`; and end now. The read of an Alert
  Group says whether it is kept back and by which windows, and its list takes the filter `kept_back`; the reads of an
  Integration, a Route and a Team name their active windows. The spec changes in the story that implements this
  capability (`api/openapi.yaml`), with the problem codes of its refusals.
- **C-34.FR-21** Muster logs `maintenance_window_started` and `maintenance_window_ended` (INFO) with the window, its
  object and the number of Alert Groups it kept back or stopped keeping back; counts the Alert Groups that became kept
  back in `muster_maintenance_kept_back_total{route}` and those that stopped being kept back in
  `muster_maintenance_released_total{route,outcome}`, where `outcome` is `published` (FR-16), `updated` (FR-17) or
  `resolved` (FR-15); and labels entities by `public_id` only (ADR-0014). The stories register the log events and the
  metrics. The Features category of the opt-in telemetry reports whether Maintenance windows are used (ADR-0019).
- **C-34.FR-22** Maintenance windows add no Internal alert. How they apply to the Internal alerts of the built-in
  "Muster" Integration, and to `MusterHeartbeatLost` of an Integration in a window, is [open question
  9](#open-questions). How the statistics count kept-back Alert Groups is [open question 10](#open-questions).
- **C-34.FR-23** The documentation has a page on Maintenance windows: the three objects, the rule of every Team with the
  worked example of scenario 3, Urgent Alert Groups, what happens at the end, and a CI example that opens and closes a
  window with a Service account token. The Terraform provider manages windows through the same API, with a resource
  that its own capability, specified later in L2 (§1.5), adds.

## UI

Integration, Route and Team pages → "Maintenance windows" (FR-18): "Start maintenance", "Plan a window", "Until", the
presets, "Reason", "End now", "Change", the banner "In a Maintenance window until {time}: {N} Alert Groups kept back.",
and on the Team page the hint of FR-18. Configuration → "Maintenance windows": the list of FR-18. Alert Group page →
the banner of FR-19; Alert Group list → the filter and badge "Kept back". Root message → the mark "Published after a
Maintenance window that ended at {time}" (FR-16). The approved mockups of the core show the windows on the Team and
Route pages (L2 §5.2).

## API surface

`maintenance-windows` (list, create, read, update, and delete if [open question 13](#open-questions) keeps it) and
`maintenance-windows/{id}/end`; `kept_back` with its windows on `alert-groups` and the list filter `kept_back`; the
active windows on `integrations`, `routes` and `teams`; the Permission `maintenance:write` and the read Permission of
[open question 14](#open-questions); the lifecycle events `maintenance_started` and `maintenance_ended` in the Timeline
and, subject to [open question 8](#open-questions), in outgoing webhook events.

## Acceptance

Checked with the development clock, the fake messenger servers, the fake bridge as a Notification gateway and the
recording test adapter of C-11. The statements cover what is decided; the stories add those of the answers to the open
questions.

- **C-34.AC-1** A Service account with the Role Responder opens a window on a Team through the API, a Responder closes
  it from the UI, and the Audit log records both; a Viewer gets `403` on opening one; a window whose end is not after
  its start gets `422`.
- **C-34.AC-2** During a window of a Route, an Alert Group of that Route is created from a webhook: its Alerts are on
  its page and in the Alerts view, its Timeline has `created` and `maintenance_started`, the fake messenger servers
  receive nothing, and no step of the Route's chain is recorded.
- **C-34.AC-3** The Alerts of that Alert Group resolve and the window is closed early: the Alert Group records
  `maintenance_ended`, is never posted, and the delivery state of each Destination reads "Not posted: resolved during
  a Maintenance window".
- **C-34.AC-4** An Alert Group still firing when its window ends is published in every Destination it reaches, with the
  mark "Published after a Maintenance window that ended at {time}"; its chain starts at that Publication, and where no
  chain applies its first ack timeout notice comes `route.ack_timeout` after it.
- **C-34.AC-5** A firing Alert Group of a Route with a chain and no Destination, created during a window of that Route,
  starts no chain while kept back; its chain starts when the window ends, and its gateway step calls the fake gateway.
- **C-34.AC-6** During a window of an Integration, an Alert Group whose Alerts all come from that Integration is kept
  back, and one of the same Route from another Integration is published as usual.
- **C-34.AC-7** With windows on payments only, an Alert Group with Alerts of payments and analytics is not kept back
  and its chains Page as usual; an Alert Group of payments alone is kept back; when an Alert of analytics joins it, it
  stops being kept back with `maintenance_ended` naming the joined Team. A Team in a window that joins an Alert Group
  whose chains run leaves them running, with no step skipped.
- **C-34.AC-8** The Team that owns a Route does not count: a window on that Team keeps back no Alert Group of the Route
  whose Alerts belong to no Team.
- **C-34.AC-9** A planned window starts and ends at the times set when the development clock passes them, on whichever
  replica claims its row (two replicas in the Go end-to-end suite); at its start, an open Alert Group of its Route that
  was already published records `maintenance_started` and gets no Thread reply about the Alerts that join it while kept
  back; the window's start and end are logged once each.
- **C-34.AC-10** A Snooze that ends while its Alert Group is kept back makes it firing, with `snooze_ended` in the
  Timeline and no Thread reply; Acknowledge from the UI during the window acknowledges it.
- **C-34.AC-11** A rise to critical of an Alert Group kept back by a window that lets Urgent Alert Groups through stops
  keeping it back, and it is published at once; the same rise under a window that does not let them through keeps it
  back, and nothing is delivered for it.
- **C-34.AC-12** An Alert Group never published before the window, which reaches a Destination by a rise in Severity
  level during the window, is published there at the end as part of its first Publication, without a
  `destination_joined` of its own; an Alert Group published before the window, which reaches a Destination by the same
  rise during it, records a Loud `destination_joined` there at the end if it is firing, and a Quiet one if it is
  acknowledged.
- **C-34.AC-13** Moving the open Alert Groups of a Route in a window to the Default route (C-09.FR-19) stops keeping
  them back, with `maintenance_ended`, unless a window of the Default route covers them.
- **C-34.AC-14** In a real browser, the Team page starts a window from a preset and ends it, the Integration page shows
  the banner with the count, and the Alert Group page shows why its Alert Group is kept back, in English and in Russian,
  360 CSS pixels wide without horizontal scrolling of the page.
- **C-34.AC-15** `muster_maintenance_kept_back_total` and `muster_maintenance_released_total` count the Alert Groups of
  AC-2, AC-3 and AC-4 by Route and outcome, and `maintenance_window_started` and `maintenance_window_ended` are in the
  log with the window's `public_id`.

## Open questions

Each question has a recommended answer; the maintainer decides, and the answer then replaces the reference in the
requirements. Answers that change what an ADR says come with an amendment of that ADR.

1. **Where the setting that lets Urgent Alert Groups through lives** (ADR-0018 leaves it to this PRD). Options: on each
   window, on the Route, or on the Organization. _Recommended:_ on each window, "Let Urgent Alert Groups through", off
   by default, since the person who opens the window knows whether critical Alerts are expected — a deploy that
   restarts a service fires them — and ADR-0003 already speaks of "a Maintenance window that lets Urgent ones through".
   An Urgent Alert Group passes only when every window that would keep it back lets Urgent ones through; turning the
   setting on in an active window stops keeping back its Urgent Alert Groups at once. An Alert Group whose Severity
   level falls below critical again is not kept back again: it was already published, and a fall changes nothing else
   either (ADR-0018).
2. **Repeating windows.** ADR-0018 names "Saturday 02:00–04:00" as a planned span, and a window is one row (journal
   D535). _Recommended:_ no repetition in this capability; a weekly window is opened by a scheduled CI job or by
   Terraform through the API, and repetition comes back only if people ask for it.
3. **Chains, the ack timeout, Reminders and Notification rule runs already running when an Alert Group becomes kept
   back.** "Its timers and chains wait" (ADR-0004, ADR-0018) does not say whether they pause or stop. _Recommended:_
   the running chains stop, as on a start-over, and so do the Notification rule runs they started (C-31), so the stop
   check of C-30.FR-4 drops their waiting calls; the ack timeout stops; the Assignees and Unclaimed stay as they are.
   When the Alert Group stops being kept back while firing and nobody owns it, its chains — or its ack timeout where no
   chain applies — start over at their first step, as at the points of C-32.FR-5. Reminders keep their schedule, but
   one that comes due while it is kept back is not sent and does not count as unanswered for auto-unacknowledge
   (C-17.FR-5). Pausing and resuming with the time left would keep more state for little gain. ADR-0004 and ADR-0018
   then say "stop and start over" instead of "wait".
4. **The Root messages an Alert Group already has while it is kept back.** "Nothing is delivered" could also stop
   edits. _Recommended:_ edits go on, Quietly — an edit never rings (ADR-0005) — so the status shown stays true and a
   person who presses a button sees the result; the Root message shows "In a Maintenance window until {time}" while it
   is kept back. New messages and Thread replies are kept back.
5. **Loud or Quiet, and what is published, when an Alert Group starts and stops being kept back.** ADR-0018 says that
   Alert Groups still firing are published "as at the end of a Storm", and Publications after a Storm are Quiet; but a
   Storm summary already rang, while a window rang nothing. It says nothing of acknowledged or snoozed ones.
   _Recommended:_ `maintenance_started` is Quiet, without Mentions, a Root message update where there is one.
   `maintenance_ended` is Loud, with the Mentions of the entries for `new_alert_group`, when the Alert Group is firing
   and nobody owns it — the new Root message of FR-16 where it was never published, and one Thread reply "The
   Maintenance window ended: still firing, {N} alerts" where it was — and Quiet otherwise. An acknowledged or snoozed
   Alert Group that was never published is published too, Quietly, in its status, so that every Destination shows its
   open problems and their Owners. The Publications go gradually within the limiters, as after a Storm. Every cause of
   FR-13 behaves the same, including a change of configuration such as a raised Minimum severity, which otherwise
   publishes Quietly and starts no chain (ADR-0018).
6. **An Alert Group with Alerts from several Integrations** (C-09.FR-1) when one of them is in a window. ADR-0018 says a
   window of an Integration keeps back "all their Alert Groups". _Recommended:_ the rule of Teams — kept back only while
   every Integration of its Alerts is in a window — so that maintenance of one Alertmanager never hides a problem that
   another one reports. Such Alert Groups are rare with the default Group key.
7. **Storms.** _Recommended:_ an Alert Group created while kept back does not count toward its Route's Storm threshold
   and is in no Storm summary, and the end of a window never starts a Storm: its Publications go through the limiters,
   Urgent first. A Storm of the Route that is still active when the window ends holds those Publications as it holds
   any non-Urgent Alert Group (C-11.FR-6). The count of the Storm threshold then leaves kept-back Alert Groups out.
8. **Outgoing webhooks in events mode**, which Storms do not hold back (C-11.FR-6). _Recommended:_ the events mode is
   not kept back either: it receives every event, `maintenance_started` and `maintenance_ended` included, with
   `kept_back` in `alert_group`, so the receiver decides; the template mode is kept back like a messenger. A delivered
   `created` event of a kept-back Alert Group does not count as its Publication (C-17.FR-1, C-32.FR-5), so a Route whose
   only Destination is such a webhook starts no ack timeout and no chain during the window. The alternative — holding
   the events and sending them at the end — keeps their order but delays a receiver that may need them, such as a
   ticket system.
9. **Internal alerts.** _Recommended:_ a window on the built-in "Muster" Integration is refused with `422`, so that
   Muster's own health is never kept back; windows of Routes and Teams keep back Internal alerts like any other Alerts,
   by the Admin's choice; and a window of an Integration also keeps back the Internal alerts about that Integration —
   `MusterHeartbeatLost` and `MusterSnapshotTruncated` with its `integration` label — because an upgrade of
   Alertmanager is the usual reason for the window and would otherwise raise `MusterHeartbeatLost`.
10. **Statistics.** Time to acknowledge runs from the start of an Alert Group (C-09.FR-15), so a kept-back one that is
    acknowledged after the window looks slow. _Recommended:_ no change in this capability; the documentation says so,
    and the question returns with the dashboard of the Home page (journal D540).
11. **Limits and presets.** _Recommended:_ a window lasts at most 7 days and starts at most 90 days ahead, as the
    setting `maintenance.limits`, provisional until confirmed on the test environment; "Start maintenance" offers 30
    minutes, 1 hour, 2 hours, 4 hours and a time entered. A P-number is given when L2 §5.3 lists them.
12. **How long ended windows are kept.** _Recommended:_ as long as the Audit log (`retention.audit_log`), pruned by the
    Leader's hourly retention task (`internal/leader`).
13. **Changing, closing and deleting windows, and windows that overlap.** _Recommended:_ several windows may cover one
    object and overlap, so that a CI pipeline never has to look for an existing one; a planned window may change in
    every field or be deleted; an active one may change its end, its reason and the setting of question 1, and is
    closed rather than deleted, so that the Alert Groups it kept back keep a window to name; closing it early sets its
    end to now, and an end set in the past is refused with `422`; an ended one no longer changes. Its object never
    changes: another object takes a new window. Deleting an Integration, a Route or a Team closes its active windows and
    deletes its planned ones, each recorded in the Audit log.
14. **The Permission that reads windows.** ADR-0018 names only `maintenance:write`. _Recommended:_ a new Permission
    `maintenance:read` for Admins, Responders and Viewers, as each other kind of configuration has its read Permission
    (reference.md, Roles and Permissions).

## Related ADRs

ADR-0003, ADR-0004, ADR-0005, ADR-0007, ADR-0014, ADR-0016, ADR-0018, ADR-0019.

## Depends on

C-03 — Roles and Permissions; C-05 — Integrations; C-08 — Routes; C-09 — the lifecycle and the Timeline; C-11 —
delivery, limiters and Storms; C-15 — outgoing webhook events; C-17 — the ack timeout and Reminders; C-25 — Teams and
the Teams of an Alert Group; C-26 — Urgent, the Minimum severity and `destination_joined`; C-30, C-31, C-32 — calls,
Notification rules and Escalation chains, which a window keeps waiting.

## Suggested story split

- **BE** — windows with their Permissions, Audit log and API; the timer rows of their start and end.
- **BE** — keeping Alert Groups back: the rules of the three objects, Urgent, `maintenance_started` and
  `maintenance_ended`, what waits in delivery, timers and chains, and what is published at the end.
- **FE** — the sections on the Integration, Route and Team pages, the page "Maintenance windows", the banner of the
  Alert Group page and the filter of the list.
- **Docs** — the page of FR-23.
- In `muster-io/terraform-provider-muster` — the resource for windows, specified with the Terraform provider.
