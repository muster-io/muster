# C-27. Schedules

[L2 index](../L2.md) · Release: 0.2.0 · UI: yes · Depends on: C-18, C-19, C-25, C-26

**Goal.** A Team says who is On-call and when: Schedules of Rotation layers with Overrides on top, a main Schedule that
names the Team's On-call, gaps found ahead of time, warnings about people who cannot be Paged, an "On call now" view and
a personal iCal feed — without messages about Shifts.

Names in the form `area.setting` refer to rows of [defaults.md](../l1/defaults.md).

## Scenarios

1. The Schedule "Main" of "payments" has two Rotation layers: below, "Around the clock", weekly Shifts handed over on
   Monday at 10:00 among Petr, Sergey and Olga; above, "Working hours 11–19", weekly Shifts on weekdays from 11:00 to
   19:00 among Olga and Petr. On a Wednesday at 15:00 the On-call is the person of the upper Rotation layer; at 21:00,
   the person of the lower one.
2. Olga gives her Shift of Wednesday 11:00–19:00 to Petr; Petr is On-call then, nothing else changes, nobody gets a
   message, and the Audit log records who made the Override.
3. Sergey is disabled. His Shifts from Monday 10:00 are a gap outside working hours; the Schedule shows the gap and
   "Disabled: @sergey in Rotation layer 1", and an Internal alert reports the gap a day ahead. Petr takes Sergey's week
   with an Override, and the alert resolves.
4. "payments" also has the Schedule "Second line" with one Rotation layer; an Escalation step Pages its On-call, while
   the Team's On-call stays that of its main Schedule "Main".
5. The Home page shows "On call now": devops — Ivan, until Monday 10:00; payments — Petr, "unreachable in Mattermost",
   "next: gap"; search — "No Schedule".
6. Ivan subscribes his calendar application to his iCal feed and sees his Shifts, Overrides included; after he issues a
   new link, the old one stops working.

## Functional requirements

- **C-27.FR-1** A Schedule belongs to one Team (C-25.FR-1) and has a name, unique in the Team, a time zone
  (`schedule.time_zone`), its Rotation layers in order and its Overrides. A Team may have several Schedules, such as a
  first and a second line; one of them is its main Schedule — the first one created, until an Admin chooses another
  (_provisional_, P-90) — and the Team's On-call is the On-call of its main Schedule. A Team without a main Schedule has
  no On-call.
- **C-27.FR-2** A Rotation layer has a name, a position — a higher Rotation layer overrides the lower ones wherever it
  has a Shift — and the people who take Shifts in turn, in order. A Shift lasts a day, a week, or a length of its own in
  whole hours; the handover is at a time of day and, for weekly Shifts, on a day of the week; the Rotation layer starts
  at a date with the first person of its list. It may be limited to some days of the week and a time of day, such as
  Monday to Friday 11:00–19:00: outside the limits it has no Shift, and the person whose turn it is takes only the
  limited times. Times are wall-clock times in the Schedule's time zone, by its rules on each day, so a handover at
  10:00 stays at 10:00 across a change of the clocks.
- **C-27.FR-3** An Override names the person who takes the Shift, the person it replaces and a span from one time to
  another. Within the span, wherever the Rotation layers make the replaced person On-call, the person who takes it is
  On-call instead; elsewhere it changes nothing. Overrides apply in the order in which they were created, each to the
  result of the earlier ones (_provisional_, P-65). An Override may not end in the past (_provisional_, P-91).
- **C-27.FR-4** The On-call of a Schedule at a moment is the person whom the highest Rotation layer with a Shift then
  has on Shift, after the Overrides; it is nobody when no Rotation layer has a Shift then, or when that person may not
  be Paged (FR-5). The Schedule computes the On-call on the business clock (C-02), so the development clock moves it.
- **C-27.FR-5** A time when a Schedule's On-call is nobody is a gap. People who may not be Paged are Viewers, Users who
  are not in the Team — after the groups of the identity provider changed, for example (C-25.FR-4) — and disabled Users;
  their Shifts are gaps, and the Schedule shows a warning for each: "Disabled: @{login} in {Rotation layer}", "@{login}
  is a Viewer", "@{login} is not in the Team". Muster keeps them in the Rotation layers; an Admin removes them.
- **C-27.FR-6** An Override needs the Permission `schedule-overrides:write`, which Responders and Admins have, and a
  precondition: the caller is in the Team or is an Admin, refused otherwise with `409` and the code `not_in_team`; both
  people of the Override are in the Team, are neither Viewers nor disabled, refused otherwise with `422` and a pointer
  to the person. The same holds for changing and deleting an Override. Overrides need no approval and send no message;
  each is recorded in the Audit log with its people and span, and shows in the Schedule, in "On call now" and in the
  iCal feed.
- **C-27.FR-7** The Schedule page shows the gaps of the next `schedule.gap_view`. A Schedule may report its gaps as an
  Internal alert (`schedule.gap_alert`): the Leader looks for gaps every `schedule.gap_scan_interval` and raises
  `MusterScheduleGap` for each Schedule with a gap that starts within `schedule.gap_alert_horizon`, with the labels
  `schedule`, `schedule_name`, `team` and `team_name` — ids and current names, as every Internal alert has
  ([reference.md](../l1/reference.md#internal-alerts-added-in-l2)) — and resolves it once no gap starts within the
  horizon. A second scan finds the same gaps and the Internal alert is deduplicated by its fingerprint, so the scan is
  safe to run twice (ADR-0007). The Leader exports `muster_schedule_gap{schedule}` — 1 while a gap starts within the
  horizon — for the chart rule of the same name, and the runbook page of `MusterScheduleGap` says how to fill a gap.
- **C-27.FR-8** The Schedule warns that its On-call, now or in a Shift of the next `schedule.gap_view`, is unreachable
  where the Destinations of their Team (C-26.FR-1) — and, from C-32 on, all the Destinations in whose Threads the
  Escalation chains that Page this Schedule write (C-32.FR-8) — cannot mention them: the person has no Account link in
  the Identity space of a Destination's Connection; a Bridge Destination's bridge does not declare native mentions; an
  outgoing webhook Destination has no "reply in thread" in template mode; or the person is not in the discussion group
  of a Telegram channel, as the Destination check sees it. From C-30 on, the warning says whether a call through a
  Notification gateway still reaches them (C-30.FR-3).
- **C-27.FR-9** "On call now" shows, for each Team — all Teams by default, or the Teams of the signed-in User — the
  On-call of its main Schedule with the Schedule's name and until when, marks for an unreachable On-call (FR-8) and a
  gap that follows, and "No Schedule" for the Teams without one. It is a section of the Home page and of each Team's
  page.
- **C-27.FR-10** Each User may issue a personal iCal feed of their own Shifts in all Schedules, Overrides included, from
  `ical.window` back to its end ahead, one event per Shift named after its Team and Schedule. The feed is a URL that
  carries a token Muster issues: hashed, with a prefix of its own (`ical.token`), shown once, revocable, and masked
  wherever a URL that carries it would be logged (ADR-0011). Issuing a new link revokes the previous one, and a revoked
  token, or the token of a disabled or deleted User, gets `404`. The feed is served on the app listener without a
  session; issuing and revoking need the web session (C-03.FR-27). Schedules are not imported from iCal.
- **C-27.FR-11** Shifts and Overrides send no messages: who is On-call is shown in the UI and the iCal feed, and no
  messenger command answers it.
- **C-27.FR-12** Deleting a Schedule that an Escalation step names is refused with `409` and the code `schedule_in_use`,
  naming the chains. Deleting a Team's main Schedule leaves the Team without one until an Admin chooses another
  (_provisional_, P-66).
- **C-27.FR-13** Only Admins change Schedules and Rotation layers (`schedules:write`); everyone reads them
  (`schedules:read`). Changes use optimistic locking and are recorded in the Audit log with a before/after diff. Each
  Schedule exports `muster_schedule_info{schedule,name}`; the stories register their log events.
- **C-27.FR-14** The documentation has a page on Schedules: Rotation layers with the example of a weekly Rotation layer
  around the clock under a working-hours Rotation layer with other people, several Schedules for a first and a second
  line, Overrides and who may make them, gaps and the people who count as gaps, the Internal alert and the iCal feed.

## UI

Team page → Schedules: each with "main", its Rotation layers, its On-call now and its warnings, "New Schedule" for
Admins; the hint "The main Schedule names the Team's On-call shown under 'On call now'; Escalation steps may Page the
On-call of any of them." Schedule page: header "Team {Team} · Schedule {name}", "main", "Two Rotation layers: the upper,
narrower one wins. Overrides send no messages."; "On call now" with the person, the Rotation layer and until when, the
unreachable mark ("Unreachable in Mattermost: no linked account. A call through the Notification gateway works.") and
"Next: …"; "Warnings" (gaps and people who may not be Paged); a two-week view with one row per Rotation layer, a row of
Overrides and a row "Result — who is On-call", gaps in red and disabled people hatched, a mark for now, and buttons for
the previous week, today and the next week; "Rotation layers" (top first, each with its limits, Shift length, handover
and people in turn) and "Add Rotation layer" for Admins; "Overrides" — "Any Responder of the Team; no messages." — each
with who gives what to whom, when and who made it, and "Add Override": "I take the Shift of…" or "I give my Shift to…",
who takes it, whom it replaces, from, to ("You can give only your own Shift: choose it in the view and the person who
takes it."), Save and Cancel; "iCal subscription" — "A personal link for your calendar application. It is shown once; a
revoked link stops working at once." — with the link shown once ("Copy the link now: it will not be shown again"), "New
link" and "Revoke". Home page: "On call now" with "All Teams" and "By the main Schedule of each Team".

## API surface

`schedules` (list, create, read, update, delete; filter by Team; the main Schedule as a field of `teams`); Rotation
layers as fields of `schedules`; `schedules/{id}/overrides` (list, create, update, delete); `schedules/{id}/on-call`
(read the On-call and the Shifts of a period, with gaps and warnings); `on-call-now` (read for the Teams of the caller
or all); `me/ical-feed` (issue, revoke; read whether one exists); the feed itself at an address under
`MUSTER_PUBLIC_URL` that carries the token.

## Acceptance

Checked through the API and the UI on a virtual clock, with the development clock live.

- **C-27.AC-1** With the two Rotation layers of scenario 1, the On-call is the upper Rotation layer's person on a
  Wednesday at 15:00 and the lower one's at 21:00 and on Saturday at 15:00; the weekly handover stays at Monday 10:00 in
  the Schedule's time zone across a change of the clocks.
- **C-27.AC-2** An Override from Olga to Petr for Wednesday 11:00–19:00 makes Petr On-call then and leaves the On-call
  of 10:00 and 20:00 unchanged; it records an Audit log entry and no message reaches the fake messenger servers.
- **C-27.AC-3** A Responder who is not in the Team gets `409` `not_in_team` on an Override; a Viewer gets `403`; an
  Override that gives a Shift to a Viewer or to a User outside the Team gets `422` naming that person; an Admin who is
  not in the Team creates it.
- **C-27.AC-4** After a User of a Rotation layer is disabled — or turned into a Viewer, or taken out of the Team by the
  identity provider — their Shifts are gaps and the Schedule shows the matching warning.
- **C-27.AC-5** With a gap 20 hours ahead and `schedule.gap_alert` on, the next scan raises `MusterScheduleGap` with the
  Schedule's id and name and `muster_schedule_gap` is 1; an Override that fills the gap resolves it on the next scan;
  running the scan twice raises it once; with `schedule.gap_alert` off, nothing is raised.
- **C-27.AC-6** An On-call without a Mattermost Account link, in a Team with a Mattermost Destination, is marked
  unreachable in Mattermost on the Schedule and in "On call now".
- **C-27.AC-7** A User's iCal feed lists their Shifts and Overrides in `ical.window` as events; the token is returned
  once, its prefix is that of `ical.token`, the database holds only its hash, a request log line shows it masked; after
  "New link" the old URL gets `404`, and so does the new one once the User is disabled.
- **C-27.AC-8** Deleting a Schedule named by an Escalation step gets `409` `schedule_in_use`; making another Schedule
  main changes the Team's On-call.
- **C-27.AC-9** In a real browser, the Schedule page shows the two-week view with the row "Result", the gap in red and
  the warnings; "Add Override" in the mode "I give my Shift to…" saves an Override; the Home page shows "On call now",
  in English and in Russian.

## Related ADRs

ADR-0007, ADR-0011, ADR-0013, ADR-0014, ADR-0018.

## Depends on

C-18 — Account links, for the unreachable warning; C-19 — Internal alerts and chart rules; C-25 — Teams; C-26 — the
Destinations of a Team. The warning covers the Threads of Escalation chains from C-32 on and calls from C-30 on.

## Suggested story split

- **BE** — Schedules, Rotation layers, Overrides with their Permission and precondition, the On-call and gaps, the gap
  scan with the Internal alert, its chart rule and runbook, the iCal feed.
- **FE** — the Schedule page with its two-week view and the Override form, the Schedules of the Team page, "On call now"
  on the Home and Team pages, the iCal section.
