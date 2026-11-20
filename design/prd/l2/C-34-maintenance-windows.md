# C-34. Maintenance windows

[L2 index](../L2.md) · Release: a 0.2.x patch release · UI: yes · Depends on: C-03, C-05, C-08, C-09, C-11, C-15, C-17,
C-25, C-26, C-30, C-31, C-32

**Goal.** Let people say ahead of a deploy or an upgrade that the Alerts of an Integration, a Route or a Team are
expected: while a Maintenance window keeps an Alert Group back, Muster goes on ingesting and grouping and its status
changes as usual, but nothing new is delivered for it and no Escalation chain Pages anyone; when the window ends, the
Alert Groups still firing are published with a mark and their chains start over, and those that resolved meanwhile
never are. One team's window never keeps back an Alert Group that another team shares (ADR-0018).

The behaviour comes from ADR-0018 and journal D493, D511, D512, D523, D530 and D535; what they left open was decided by
the maintainer with the review of this capability (2026-11-21), which amended ADR-0004 and ADR-0018. Names in the form
`area.setting` refer to rows of [defaults.md](../l1/defaults.md); the lifecycle events are rows of the [L2 lifecycle
event table](../l1/reference.md#lifecycle-events-added-in-l2).

## Scenarios

1. Before a deploy of payments, a CI pipeline opens a Maintenance window on the Team "payments" for 30 minutes through
   the API, with the token of a Service account whose Role is Responder. `PaymentsAPIDown` fires during the deploy: its
   Alert Group is created and its Alerts are on its page, but nothing appears in `payments-critical` or in the
   operations Destinations, and no Escalation chain Pages anyone. The deploy succeeds, the Alerts resolve, the pipeline
   closes the window early, and the Alert Group is never posted.
2. The same deploy fails, and `PaymentsAPIDown` still fires when the window ends: its Alert Group is published Loudly
   in every Destination it reaches, marked "Published after a Maintenance window that ended at 14:30", and the chains
   of payments start at that Publication.
3. `KubePodCrashLooping` fires in production with Alerts of payments and of analytics in one Alert Group. The window of
   payments does not keep it back, because analytics is not in a window: analytics is Paged as usual.
4. An Admin plans a window on the Integration "am-prod" for Saturday 02:00–04:00, an upgrade of Alertmanager. The
   Integration page lists it as planned; from 02:00 it shows "In a Maintenance window until 04:00: 3 Alert Groups kept
   back.", the Alert Group page of each of them says why it is kept back, and the `MusterHeartbeatLost` of "am-prod"
   that the upgrade raises is kept back too.
5. A window on the Route "Databases" lets Urgent Alert Groups through: a critical Alert Group that arrives during it is
   published at once, while the warnings of the Route wait for the end of the window.

## Functional requirements

### What a window is and who manages it

- **C-34.FR-1** A Maintenance window covers exactly one Integration, one Route or one Team, from a start to an end, with
  a reason as free text and the setting "Let Urgent Alert Groups through" (`maintenance.urgent_passes`, FR-9). It is
  one row with a start and an end, which any replica handles like a timer row (ADR-0007; journal D535): a window that
  starts now has its start at the time it is opened, and a planned one starts later, such as Saturday 02:00–04:00
  (ADR-0018; journal D493). Windows do not repeat: a weekly window is opened by a scheduled CI job or by Terraform
  through the API. Several windows may cover one object, and windows may overlap. A window is planned before its
  start, active from its start until its end, and ended afterwards, by the business clock (C-02), so that the
  development clock moves windows as it moves every other domain time. A window always has an end: it covers its
  object "from now until a time, or over a planned span" (ADR-0018). Its length and how far ahead it may start are
  limited by `maintenance.limits`.
- **C-34.FR-2** Admins and Responders open, plan, change, close and delete windows with the Permission
  `maintenance:write`, and a CI pipeline uses a Service account whose Role is Responder (ADR-0018; journal D535).
  Everyone reads windows with the Permission `maintenance:read`, which Admins, Responders and Viewers hold
  ([reference.md](../l1/reference.md#permissions-added-in-l2)); whether an Alert Group is kept back is part of its read,
  under `alert-groups:read`. Permissions stay global: a Responder may open a window on any Team, whether or not they are
  in it, since only Overrides check who is in a Team (ADR-0018).
- **C-34.FR-3** A window is opened from now or planned, changed, closed and deleted through the API and the UI:
  - its end is later than its start and later than now, a planned start is not in the past, and both are within
    `maintenance.limits`, or the request is refused with `422`;
  - a planned window may change in every field but its object, or be deleted;
  - an active window may change only its end, its reason and `maintenance.urgent_passes`, and is closed, never
    deleted, so that the Alert Groups it kept back keep a window to name; closing it early sets its end to now;
  - an ended window no longer changes;
  - the object of a window never changes: another object takes a new window;
  - deleting an Integration, a Route or a Team closes its active windows and deletes its planned ones;
  - a window on the built-in "Muster" Integration is refused with `422`, so that Muster's own health is never kept back
    (C-06.FR-14).

  Windows use optimistic locking (`If-Match`), as every configuration does. Ended windows are kept for
  `retention.audit_log` and pruned by the Leader's retention task.
- **C-34.FR-4** The API takes the start and the end as RFC 3339 times with an offset and returns them in UTC; the UI
  enters and shows them in the time zone of the person's profile (C-03.FR-12), with the date where it is not today.
- **C-34.FR-5** Opening and closing a window are recorded in the Audit log (ADR-0018; journal D493), and so is every
  other change of a window, its deletion and those of FR-3 included, with a before/after diff, the actor and the
  Transport. The start and the end that the business clock reaches are not actions of anyone: they are log events
  (FR-22) and lifecycle events of the Alert Groups the window keeps back (FR-13).

### Which Alert Groups a window keeps back

- **C-34.FR-6** A window of a Route keeps back every open Alert Group of that Route (ADR-0018). A window of an
  Integration keeps an open Alert Group back only while every Integration of its Alerts (C-09.FR-1) is in a window, so
  that the maintenance of one Alertmanager never hides a problem that another one reports. An Alert Group moved to the
  Default route (C-09.FR-19) is covered by the windows of the Default route from then on.
- **C-34.FR-7** A window of a Team keeps an Alert Group back — in all its Destinations, the operations Destinations
  included — only while every Team of the Alert Group (C-25.FR-6) is in a window; the Team that owns its Route is not
  counted, and an Alert Group without a Team is never kept back by the window of a Team (ADR-0018; journal D523, D530).
  A Team that joins an Alert Group (C-25.FR-6) may make it no longer kept back, when the new Team is not in a window,
  but never stops the Escalation chains already running on it (ADR-0018; journal D530).
- **C-34.FR-8** An Alert Group is kept back while at least one rule of FR-6 and FR-7 holds for it and FR-9 does not let
  it through. Only its creation, a move to another Route and the start of a window that covers it make an Alert Group
  kept back; an open Alert Group that a window covers when the window starts is kept back from then, whether or not it
  was already published (ADR-0018). An Alert or a Team that joins it, its becoming Urgent, and the end, a change or the
  deletion of a window may only make it no longer kept back, never kept back: a joining Team never stops the chains
  already running (FR-7), and a window whose end is extended or whose `maintenance.urgent_passes` is turned off again
  does not take back an Alert Group it already let go.
- **C-34.FR-9** `maintenance.urgent_passes` is a setting of each window, off by default. An Urgent Alert Group
  (C-26.FR-8) passes only when every window that would keep it back lets Urgent Alert Groups through; an Alert Group
  that such windows keep back stops being kept back as soon as it becomes Urgent, whether by its Alerts or by its
  Route's Minimum severity, and turning the setting on in an active window stops keeping back its Urgent Alert Groups
  at once (ADR-0003, ADR-0018; journal D493, D511). An Alert Group whose Severity level falls below critical again is
  not kept back again (FR-8).

### While an Alert Group is kept back

- **C-34.FR-10** Ingestion and grouping go on: Snapshots are processed, the Alerts view and the Alert Group page show
  every Alert, Alerts join and resolve, the Reopen window and the Grace period apply, the system resolves the Alert
  Group, its Severity level and its Teams follow its Alerts, and unknown values of the Team label are recorded (C-06,
  C-09, C-25). Its status changes as usual (ADR-0018; journal D523): a Snooze ends, a rise to critical ends a Snooze or
  an acknowledgement as C-09.FR-9 says, and people run Commands from the UI, the API and the buttons of Root messages
  that already exist, answered on the interactive path (ADR-0005). Every change is in its Timeline.
- **C-34.FR-11** Nothing new is delivered for it (ADR-0018), outside the events mode of FR-12: no Root message is
  published, `destination_joined` waits for the end of the window (ADR-0005, C-26.FR-5; journal D512), and no Thread
  reply is sent — new Alerts, Reopens, the end of a Snooze, a rise to Urgent, ack timeout notices, Reminders, Pages and
  the replies of a resolution by the system — so nobody is mentioned. The Root messages it already has are still
  edited, Quietly — an edit never rings (ADR-0005) — so the status they show stays true and a person who presses a
  button sees the result; they show "In a Maintenance window until {time}" while it is kept back. The Destination test
  (C-16), Destination checks and the direct messages of Account links are not about an Alert Group and go on.
- **C-34.FR-12** An Alert Group created while kept back counts toward no Storm threshold of its Route and is in no Storm
  summary (C-11.FR-6). Outgoing webhooks in events mode are not kept back, as they are not held back by Storms: they
  receive every event, `maintenance_started` and `maintenance_ended` included, with `kept_back` in `alert_group`
  (C-15.FR-2); the template mode is kept back like a messenger. A delivered `created` event of a kept-back Alert Group
  is not its Publication (C-17.FR-1, C-32.FR-5), so a Route whose only Destination is such a webhook starts no ack
  timeout and no chain while it is kept back.
- **C-34.FR-13** Becoming kept back records the lifecycle event `maintenance_started`, Quiet and without Mentions,
  naming the windows that keep it back: a Root message update where the Alert Group has a Root message, nothing
  otherwise. No longer being kept back records `maintenance_ended`, naming the cause: a window ended, was closed early,
  was changed or was deleted with its object, the Alert Group became Urgent, a Team joined, or it moved to another
  Route. `maintenance_ended` is Loud, with the Mentions of the entries for `new_alert_group` (C-28.FR-1), when the
  Alert Group is firing and nobody owns it, and Quiet otherwise; every cause behaves the same, a change of
  configuration such as a raised Minimum severity included (ADR-0018 as amended). Both are of the Timeline kind
  `system` and are system transitions of the dispatcher with Muster as the actor (ADR-0016; journal D513).
- **C-34.FR-14** While it is kept back, no Escalation chain starts on it and no step is made (ADR-0004, ADR-0018), no
  action of a Notification rule (C-31) and no call through a Notification gateway (C-30) is made for it, no
  `escalation_webhook` is sent, and its ack timeout sends no notice. An Alert Group created while kept back has had no
  Publication, so its ack timeout and its chains have not started (C-17.FR-1, C-32.FR-5); one created while kept back
  with no Destination that can publish it — none, or only Broken ones — does not start its chains at its creation
  either, as C-32.FR-5 would otherwise have it.
- **C-34.FR-15** When an Alert Group becomes kept back, the Escalation chains running on it stop, as at a start-over
  (C-32.FR-5), and so do the runs of the Notification rules they started (C-31), so that the stop check of C-30.FR-4
  drops their waiting calls; its running ack timeout stops; its Assignees and Unclaimed stay as they are (ADR-0004 and
  ADR-0018 as amended). Its Reminders keep their schedule, but a Reminder that comes due while it is kept back is not
  sent and does not count as unanswered for auto-unacknowledge (C-17.FR-5).

### When it stops being kept back

- **C-34.FR-16** When the business clock reaches a window's end, or a person closes it early, every Alert Group that
  the window kept back is decided again (FR-8), on any replica (ADR-0007): one that another window still keeps back
  stays so until that one ends.
- **C-34.FR-17** An Alert Group that stops being kept back after it resolved is not published (ADR-0018): the delivery
  state of each Destination where it was never published says "Not posted: resolved during a Maintenance window", the
  Root messages it had before the window get their final update, and its Timeline has the whole story.
- **C-34.FR-18** An open Alert Group that stops being kept back and was never published is published in every
  Destination it reaches now, the Destinations it reached meanwhile included, as part of that Publication and without a
  `destination_joined` of its own (C-26.FR-5), with the mark "Published after a Maintenance window that ended at
  {time}" on its Root messages (ADR-0018; journal D493): Loudly when it is firing and nobody owns it, Quietly in its
  status when it is acknowledged or snoozed, so that every Destination shows its open problems and their Owners
  (FR-13). The Publications go gradually through the limiters, Urgent Alert Groups first (C-11.FR-3), as after a Storm;
  the end of a window never starts a Storm, and a Storm of the Route still active then holds them as it holds any
  Alert Group that is not Urgent (C-11.FR-6). This Publication is its first Publication: where it is firing and nobody
  owns it, its ack timeout and its chains start at it, as C-17.FR-1 and C-32.FR-5 say; where no Destination can
  publish it, its chains start when it stops being kept back.
- **C-34.FR-19** An open Alert Group that was already published when it became kept back has its Root messages brought
  to its current state, with the mark of FR-11 removed. Where it is firing and nobody owns it, one Loud Thread reply
  "The Maintenance window ended: still firing, {N} alerts" (FR-13) is sent in each of its Destinations, and its
  Escalation chains — or its ack timeout where no chain applies — start over at their first step, as at the points of
  C-32.FR-5 that start them over; the lifecycle events recorded meanwhile are not sent one by one. Each Destination
  that it reached meanwhile — by a rise in Severity level or a Team that joined — records `destination_joined` instead:
  Loudly, with the Mentions of the entries for `new_alert_group`, if it is firing; Quietly if it is acknowledged or
  snoozed (ADR-0005, ADR-0018, C-26.FR-5; journal D512).

### UI, API and observability

- **C-34.FR-20** The pages of an Integration, a Route and a Team have a section "Maintenance windows": "Start
  maintenance", which opens a window from now until a time chosen from `maintenance.presets` or entered, "Plan a
  window", the reason, "Let Urgent Alert Groups through", and the active and planned windows with "End now", "Change"
  and, for planned ones, "Delete". While a window is active, the page shows "In a Maintenance window until {time}: {N}
  Alert Groups kept back." (journal D102, D493). The Team section says "Keeps back an Alert Group only while every Team
  of its Alerts is in a Maintenance window: Alert Groups shared with Teams outside a window are still delivered." A
  page "Maintenance windows" lists the active, planned and ended windows of the Organization with their object, start,
  end, reason and who opened them, filtered by state and by object type. The Alert Group page of a kept-back Alert
  Group shows "Kept back by the Maintenance window of {object} until {time}: nothing is delivered and no Escalation
  chain runs." with a link to the window; the Alert Group list gains the filter and the badge "Kept back". Both work
  360 CSS pixels wide without horizontal scrolling (NFR-15). Every text exists in English and Russian (NFR-8); the
  texts are rows of [Banners, warnings and notices](../l1/reference.md#banners-warnings-and-notices).
- **C-34.FR-21** The API has the resource `maintenance-windows` with the `public_id` prefix `MW`: list, with cursor
  pagination and filters by object and state; create; read; update, with `If-Match`; delete, for planned windows only
  (`409` for an active or ended one); and `maintenance-windows/{id}/end`, which closes an active window. The read of an
  Alert Group says whether it is kept back and by which windows, and its list takes the filter `kept_back`; the reads
  of an Integration, a Route and a Team name their active windows; `alert_group` in outgoing webhook events carries
  `kept_back` (FR-12). The spec changes in the story that implements this capability (`api/openapi.yaml`), with the
  problem codes of its refusals.
- **C-34.FR-22** Muster logs `maintenance_window_started` and `maintenance_window_ended` (INFO) with the window, its
  object and the number of Alert Groups it kept back or stopped keeping back, and counts the Alert Groups in
  `muster_maintenance_kept_back_total{route}` and `muster_maintenance_released_total{route,outcome}`
  ([reference.md](../l1/reference.md#metrics-added-in-l2)), labelling entities by `public_id` only (ADR-0014); the
  stories register the log events and the metrics. Maintenance windows add no Internal alert. Windows of Routes and
  Teams keep back Internal alerts like any other Alerts, by the choice of whoever opens them; a window of an
  Integration also keeps back the Internal alerts about that Integration — `MusterHeartbeatLost` and
  `MusterSnapshotTruncated` with its `integration` label — because an upgrade of Alertmanager is the usual reason for
  the window. The Features category of the opt-in telemetry reports whether Maintenance windows are used (ADR-0019).
- **C-34.FR-23** The statistics (C-09.FR-15) count kept-back Alert Groups like any other: their time to acknowledge
  runs from their start, the window included. The documentation has a page on Maintenance windows: the three objects,
  the rule of every Team and of every Integration with the worked example of scenario 3, Urgent Alert Groups, what
  happens at the end, that time to acknowledge includes the window, and a CI example that opens and closes a window
  with a Service account token. The Terraform provider manages windows through the same API, with a resource that its
  own capability, specified later in L2 (§1.5), adds.

## UI

Integration, Route and Team pages → "Maintenance windows" (FR-20): "Start maintenance", "Plan a window", "Until", the
presets, "Reason", "Let Urgent Alert Groups through", "End now", "Change", "Delete", the banner "In a Maintenance window
until {time}: {N} Alert Groups kept back.", and on the Team page the hint of FR-20. Configuration → "Maintenance
windows": the list of FR-20. Alert Group page → the banner of FR-20; Alert Group list → the filter and badge "Kept
back". Root message → "In a Maintenance window until {time}" (FR-11) and "Published after a Maintenance window that
ended at {time}" (FR-18); Thread reply → "The Maintenance window ended: still firing, {N} alerts" (FR-19). The approved
mockups of the core show the windows on the Team and Route pages (L2 §5.2).

## API surface

`maintenance-windows` (list, create, read, update, delete) and `maintenance-windows/{id}/end`; `kept_back` with its
windows on `alert-groups` and the list filter `kept_back`; the active windows on `integrations`, `routes` and `teams`;
the Permissions `maintenance:read` and `maintenance:write`; the lifecycle events `maintenance_started` and
`maintenance_ended` in the Timeline and in outgoing webhook events in events mode, and `kept_back` in their
`alert_group`.

## Acceptance

Checked with the development clock, the fake messenger servers, the fake bridge as a Notification gateway and the
recording test adapter of C-11.

- **C-34.AC-1** A Service account with the Role Responder opens a window on a Team through the API, a Responder closes
  it from the UI, and the Audit log records both; a Viewer reads windows and gets `403` on opening one; a window whose
  end is not after its start or is in the past, or that is longer or further ahead than `maintenance.limits`, gets
  `422`, and so does a window on the "Muster" Integration.
- **C-34.AC-2** A planned window changes in every field and is deleted; an active one changes its end and is closed,
  which sets its end to now, and refuses a change of its start and a deletion; an ended one refuses every change.
  Deleting a Team, a Route or an Integration with an active and a planned window closes the first and deletes the
  second, each in the Audit log.
- **C-34.AC-3** During a window of a Route, an Alert Group of that Route is created from a webhook: its Alerts are on
  its page and in the Alerts view, its Timeline has `created` and a Quiet `maintenance_started`, the fake messenger
  servers receive nothing, no step of the Route's chain is recorded, and it counts toward no Storm of the Route.
- **C-34.AC-4** The Alerts of that Alert Group resolve and the window is closed early: the Alert Group records a Quiet
  `maintenance_ended`, is never posted, and the delivery state of each Destination reads "Not posted: resolved during
  a Maintenance window".
- **C-34.AC-5** An Alert Group still firing when its window ends is published Loudly, with the Mentions of the entries
  for `new_alert_group`, in every Destination it reaches, with the mark "Published after a Maintenance window that
  ended at {time}"; its chain starts at that Publication, and where no chain applies its first ack timeout notice comes
  `route.ack_timeout` after it. One acknowledged during the window is published Quietly, acknowledged, and gets its
  Reminders. When more Alert Groups than the Route's `route.storm_threshold` stop being kept back at once, each is
  published on its own and no Storm summary is posted.
- **C-34.AC-6** A firing Alert Group of a Route with a chain and no Destination, created during a window of that Route,
  starts no chain while kept back; its chain starts when the window ends, and its gateway step calls the fake gateway.
- **C-34.AC-7** During a window of an Integration, an Alert Group whose Alerts all come from it is kept back, and so is
  the `MusterHeartbeatLost` of that Integration; an Alert Group with Alerts from it and from an Integration outside a
  window is not kept back.
- **C-34.AC-8** With windows on payments only, an Alert Group with Alerts of payments and analytics is not kept back
  and its chains Page as usual; an Alert Group of payments alone is kept back; when an Alert of analytics joins it, it
  stops being kept back with `maintenance_ended` naming the joined Team. A Team in a window that joins an Alert Group
  whose chains run leaves them running, with no step skipped. The Team that owns a Route does not count: a window on
  that Team keeps back no Alert Group of the Route whose Alerts belong to no Team.
- **C-34.AC-9** A planned window starts and ends at the times set when the development clock passes them, on whichever
  replica claims its row (two replicas in the Go end-to-end suite), and its start and end are logged once each. At its
  start, a published, firing Alert Group of its Route whose chain runs records `maintenance_started`, its Root message
  shows "In a Maintenance window until {time}", its chain stops and a call that the chain queued is not sent, and the
  Alerts that join it get no Thread reply; at the end, one Loud Thread reply "The Maintenance window ended: still
  firing, {N} alerts" is sent and the chain starts over at its first step.
- **C-34.AC-10** An acknowledged Alert Group kept back past its next Reminder gets no Reminder then; with
  auto-unacknowledge on, that Reminder does not count as unanswered, and the next one after the window is sent as
  scheduled.
- **C-34.AC-11** A Snooze that ends while its Alert Group is kept back makes it firing, with `snooze_ended` in the
  Timeline and no Thread reply; Acknowledge from a button of its Root message during the window acknowledges it, and
  the Root message is edited to show it.
- **C-34.AC-12** A new window keeps back Urgent Alert Groups. A rise to critical of an Alert Group kept back by a window
  that lets Urgent Alert Groups through stops keeping it back, and it is published at once, Loudly, with a Loud
  `maintenance_ended`; so does raising its Route's Minimum severity to critical, a change of configuration; under two
  overlapping windows, of which only one lets them through, the same rise keeps it back; when its Severity level falls
  again, it stays published.
- **C-34.AC-13** An Alert Group never published before the window, which reaches a Destination by a rise in Severity
  level during the window, is published there at the end as part of its first Publication, without a
  `destination_joined` of its own; an Alert Group published before the window, which reaches a Destination by the same
  rise during it, records a Loud `destination_joined` there at the end if it is firing, and a Quiet one if it is
  acknowledged.
- **C-34.AC-14** An outgoing webhook Destination in events mode receives the `created`, `maintenance_started` and
  `maintenance_ended` events of a kept-back Alert Group, with `kept_back` in `alert_group`, and the Route whose only
  Destination it is starts no chain until the window ends.
- **C-34.AC-15** Moving the open Alert Groups of a Route in a window to the Default route (C-09.FR-19) stops keeping
  them back, with `maintenance_ended`, unless a window of the Default route covers them.
- **C-34.AC-16** In a real browser, the Team page starts a window from a preset and ends it, the Integration page shows
  the banner with the count, and the Alert Group page shows why its Alert Group is kept back, in English and in Russian,
  360 CSS pixels wide without horizontal scrolling of the page.
- **C-34.AC-17** `muster_maintenance_kept_back_total` and `muster_maintenance_released_total` count the Alert Groups of
  AC-3, AC-4, AC-5 and AC-9 by Route and outcome, and `maintenance_window_started` and `maintenance_window_ended` are in
  the log with the window's `public_id`.
- **C-34.AC-18** Every row of the L2 lifecycle event table that this capability records produces exactly one Timeline
  entry with its `event`, kind, `loudness` and `mentions` (a table-driven test).

## Related ADRs

ADR-0003, ADR-0004, ADR-0005, ADR-0007, ADR-0014, ADR-0016, ADR-0018, ADR-0019.

## Depends on

C-03 — Roles and Permissions; C-05 — Integrations; C-08 — Routes; C-09 — the lifecycle and the Timeline; C-11 —
delivery, limiters and Storms; C-15 — outgoing webhook events; C-17 — the ack timeout and Reminders; C-25 — Teams and
the Teams of an Alert Group; C-26 — Urgent, the Minimum severity and `destination_joined`; C-30, C-31, C-32 — calls,
Notification rules and Escalation chains, which a window stops.

## Suggested story split

- **BE** — windows with their Permissions, limits, Audit log, retention and API; the timer rows of their start and
  end.
- **BE** — keeping Alert Groups back: the rules of the three objects, Urgent, the Internal alerts of an Integration,
  `maintenance_started` and `maintenance_ended`, what stops in delivery, timers and chains, Storms and events mode, and
  what is published at the end.
- **FE** — the sections on the Integration, Route and Team pages, the page "Maintenance windows", the banner of the
  Alert Group page and the filter of the list.
- **Docs** — the page of FR-23.
- In `muster-io/terraform-provider-muster` — the resource for windows, specified with the Terraform provider.
