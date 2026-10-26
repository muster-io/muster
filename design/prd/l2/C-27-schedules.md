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
2. Olga gives her Shift of Wednesday 11:00–19:00 to Petr: an Override makes Petr On-call from 11:00 to 19:00 on
   Wednesday. Nothing else changes, nobody gets a message, and the Audit log records who made the Override.
3. Sergey is disabled. In the upper Rotation layer someone else is on Shift during working hours, so his Shifts in the
   lower one leave a gap only outside working hours, from Monday 10:00; the Schedule shows the gap and "Disabled:
   @sergey in Rotation layer 1", and an Internal alert reports it up to seven days ahead. Petr picks the segments of the
   gap in the row "Result" and takes them: the form makes an Override for each segment — the nights and the weekend — so
   the working hours of the upper Rotation layer stay as they are, and the alert resolves.
4. "payments" also has the Schedule "Second line" with one Rotation layer; an Escalation step Pages its On-call, while
   the Team's On-call stays that of its main Schedule "Main".
5. The Home page shows "On call now": devops — Ivan, until Monday 10:00; payments — Petr, "unreachable in Mattermost",
   "next: gap"; search — "No Schedule".
6. Ivan subscribes his calendar application to his iCal feed and sees the times when he is On-call, Overrides included,
   and not the hours when the upper Rotation layer has someone else; after he issues a new link, the old one stops
   working.

## Functional requirements

- **C-27.FR-1** A Schedule belongs to one Team (C-25.FR-1) and has a name, unique in the Team, a time zone
  (`schedule.time_zone`), its Rotation layers in order and its Overrides. A Team may have several Schedules, such as a
  first and a second line; one of them is its main Schedule — the first one created, until an Admin marks another one
  main (_provisional_, P-90) — and the Team's On-call is the On-call of its main Schedule. A Team without a main
  Schedule has no On-call.
- **C-27.FR-2** A Rotation layer has a name, a position — a higher Rotation layer overrides the lower ones wherever it
  has a Shift — and the people who take Shifts in turn, in order. A Shift lasts a day, a week, or a length of its own in
  whole hours; the handover is at a time of day and, for weekly Shifts, on a day of the week; the Rotation layer starts
  at a date with the first person of its list. It may be limited to some days of the week and a time of day, such as
  Monday to Friday 11:00–19:00: outside the limits it has no Shift, and the person whose turn it is takes only the
  limited times. A time of day whose end is earlier than its start crosses midnight and runs until the end on the next
  day, such as 19:00–09:00; with days of the week, each window belongs to the day it starts on, so Friday 19:00–09:00
  runs until Saturday 09:00. Times are wall-clock times in the Schedule's time zone, by its rules on each day, so a handover at
  10:00 stays at 10:00 across a change of the clocks. Saving a Rotation layer with a Viewer, a disabled User or a User
  who is not in the Team is refused with `422` and a pointer to the person; the warnings of FR-5 are for people who
  became one of these later.
- **C-27.FR-3** An Override says that from one time to another one person is On-call, on top of every Rotation layer:
  within its span it wins over all of them, and so it also closes gaps. It may name the person whose Shift it takes,
  which the Schedule shows, but that changes nothing in the On-call. The two modes of the form — "I take the Shift of…"
  and "I give my Shift to…" — work on the Result of the Schedule (FR-4): the person picks a segment of the Result, in
  which one person, or nobody in a gap, is On-call, and the Override covers that segment, or a part of it, and nothing
  more. Where Overrides overlap, the one created last wins (_provisional_, P-65). An Override may not end in the past
  (_provisional_, P-91).
- **C-27.FR-4** The On-call of a Schedule at a moment is the person of its Override then, when there is one and that
  person may be Paged; otherwise the person on Shift in the highest Rotation layer that has a Shift then with a person
  who may be Paged (FR-5). A person who may not be Paged is skipped, and the next Rotation layer below decides. The
  Result of a Schedule is its time cut into segments, each with the person On-call in it or with nobody — a gap; the
  view, the warnings, the iCal feed and the modes of the Override form work on it. The Schedule computes the On-call on
  the business clock (C-02), so the development clock moves it.
- **C-27.FR-5** A gap is a time when no Override and no Rotation layer has a Shift with a person who may be Paged.
  People who may not be Paged are Viewers, Users who are not in the Team — after the groups of the identity provider
  changed, for example (C-25.FR-4) — and disabled Users; their Shifts are skipped (FR-4), and the Schedule shows a
  warning for each: "Disabled: @{login} in {Rotation layer}", "@{login} is a Viewer", "@{login} is not in the Team".
  Muster keeps them in the Rotation layers; an Admin removes them.
- **C-27.FR-6** An Override needs the Permission `schedule-overrides:write`, which Responders and Admins have, and a
  precondition: the caller is in the Team or is an Admin, refused otherwise with `409` and the code `not_in_team`; the
  person who takes the Shift is in the Team and is neither a Viewer nor disabled, refused otherwise with `422` and a
  pointer to the person. The person whose Shift it takes, if named, may be anyone. The same holds for changing and
  deleting an Override, which uses optimistic locking (`If-Match`). Overrides need no approval and send no message; each
  is recorded in the Audit log with its people and span, and shows in the Schedule, in "On call now" and in the iCal
  feed.
- **C-27.FR-7** The Schedule page shows the gaps of the next `schedule.gap_view`. A Schedule may report its gaps as an
  Internal alert (`schedule.gap_alert`): "by default", which is on while the Schedule is the Team's main one and off
  otherwise, so that it follows when another Schedule becomes main, or on, or off, as an Admin sets it. The Leader looks
  for gaps every `schedule.gap_scan_interval` and raises `MusterScheduleGap` for each Schedule with a gap now or one
  that starts within `schedule.gap_alert_horizon`. Its labels are the ids and current names `schedule`, `schedule_name`,
  `team_id` and `team_name`, and, under the Organization's Team label (`organization.team_label`), the Team's slug,
  which is a name label outside the fingerprint, so that the alert belongs to its Team (C-25.FR-5) and can reach the
  Team's own Destinations; an annotation says when the nearest gap starts
  ([reference.md](../l1/reference.md#internal-alerts-added-in-l2)). The alert stays raised while the gap lasts and
  resolves once no gap is current or within the horizon — an Override or a change of the Rotation layers closed it, or
  it is past. A second scan finds the same gaps and the Internal alert is deduplicated by its fingerprint, so the scan
  is safe to run twice (ADR-0007). The Leader exports `muster_schedule_gap{schedule}` — 1 while there is a gap now or
  within the horizon — for the chart rule of the same name, and the runbook page of `MusterScheduleGap` says how to fill
  a gap.
- **C-27.FR-8** The Schedule warns that its On-call, now or in a segment of its Result in the next `schedule.gap_view`,
  is unreachable where the Destinations of their Team (C-26.FR-1) — and, from C-32 on, all the Destinations in whose
  Threads the Escalation chains that Page this Schedule write (C-32.FR-8) — cannot mention them: the person has no
  Account link in the Identity space of a Destination's Connection; a Bridge Destination's bridge does not declare
  native mentions; an outgoing webhook Destination has no "reply in thread" in template mode; or the person is not in
  the discussion group of a Telegram channel, as the Destination check sees it. From C-30 on, the warning says whether a
  call through a Notification gateway still reaches them (C-30.FR-3).
- **C-27.FR-9** "On call now" shows, for each Team — all Teams by default, or the Teams of the signed-in User — the
  On-call of its main Schedule with the Schedule's name and until when, marks for an unreachable On-call (FR-8) and a
  gap that follows, and "No Schedule" for the Teams without one. It is a section of the Home page and of each Team's
  page.
- **C-27.FR-10** Each User may issue a personal iCal feed of the times when they are On-call in all Schedules, from
  `ical.window` back to its end ahead: one event per segment of a Schedule's Result (FR-4) in which they are On-call,
  Overrides included, named after its Team and Schedule, adjacent segments of one Schedule joined into one event — so a
  weekly Shift of a lower Rotation layer shows only the hours that no higher Rotation layer and no Override takes. The
  feed is served at `MUSTER_PUBLIC_URL/ical/{token}.ics`, with the token in the path that calendar applications fetch;
  Muster issues the token: hashed, with a prefix of its own (`ical.token`), shown once, revocable, and masked as
  `[redacted]` wherever the path would be logged (ADR-0011). Issuing a new link revokes the previous one, and a revoked
  token, or the token of a disabled or deleted User, gets `404`. The feed is served on the app listener without a
  session; issuing and revoking need the web session (C-03.FR-27). Schedules are not imported from iCal.
- **C-27.FR-11** Shifts and Overrides send no messages: who is On-call is shown in the UI and the iCal feed, and no
  messenger command answers it.
- **C-27.FR-12** Deleting a Schedule that an Escalation step names is refused with `409` and the code `schedule_in_use`,
  naming the chains. Deleting a Team's main Schedule leaves the Team without one until an Admin chooses another
  (_provisional_, P-66).
- **C-27.FR-13** Only Admins change Schedules and Rotation layers and mark a Team's main Schedule (`schedules:write`);
  everyone reads them (`schedules:read`). Changes use optimistic locking and are recorded in the Audit log with a
  before/after diff. Each Schedule exports `muster_schedule_info{schedule,name}`; the stories register their log events.
- **C-27.FR-14** The documentation has a page on Schedules: Rotation layers with the example of a weekly Rotation layer
  around the clock under a working-hours Rotation layer with other people, several Schedules for a first and a second
  line, Overrides and who may make them, gaps and the people whose Shifts are skipped, the Internal alert — and that a
  Schedule that covers only part of the day, such as a second line in working hours, keeps it off — and the iCal feed.

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
with who gives what to whom, when and who made it, and "Add Override": "I take the Shift of…" or "I give my Shift to…"
on a segment picked in the row "Result", then who takes it, from and to within that segment ("Choose a segment in the
row 'Result' and the person who takes it; from {from} to {to} they are On-call on top of every Rotation layer."), Save
and Cancel; "iCal subscription" — "A personal link for your calendar application. It is shown once; a revoked link stops
working at once." — with the link shown once ("Copy the link now: it will not be shown again"), "New link" and "Revoke".
Home page: "On call now" with "All Teams" and "By the main Schedule of each Team".

## API surface

`schedules` (list, create, read, update, delete; filter by Team; `main` as a field of `schedules`, which marks the
Team's main Schedule); Rotation layers as fields of `schedules`; `schedules/{id}/overrides` (list, create, update,
delete, with `If-Match`); `schedules/{id}/on-call` (read the Result of a period — its segments with the On-call, gaps
and warnings); `on-call-now` (read for the Teams of the caller or all); `me/ical-feed` (issue, revoke; read whether one
exists); the feed itself at `MUSTER_PUBLIC_URL/ical/{token}.ics`.

## Acceptance

Checked through the API and the UI on a virtual clock, with the development clock live.

- **C-27.AC-1** With the two Rotation layers of scenario 1, the On-call is the upper Rotation layer's person on a
  Wednesday at 15:00 and the lower one's at 21:00 and on Saturday at 15:00; the weekly handover stays at Monday 10:00 in
  the Schedule's time zone across a change of the clocks.
- **C-27.AC-2** An Override that makes Petr On-call on Wednesday 11:00–21:00 makes him On-call then — from 11:00 to
  19:00 over the upper Rotation layer and from 19:00 to 21:00 over the lower one — and leaves the On-call of 10:00 and
  22:00 unchanged; it records an Audit log entry and no message reaches the fake messenger servers. "I give my Shift
  to…" on Olga's segment Wednesday 11:00–19:00 of the Result makes an Override of exactly that segment.
- **C-27.AC-3** A Responder who is not in the Team gets `409` `not_in_team` on an Override; a Viewer gets `403`; an
  Override whose person who takes the Shift is a Viewer, disabled or outside the Team gets `422` naming that person,
  while one that takes the Shift of a disabled User or of someone outside the Team is saved; an Admin who is not in the
  Team creates an Override; a change of an Override with a stale `If-Match` gets `412`; saving a Rotation layer with a
  Viewer, a disabled User or a User outside the Team gets `422`.
- **C-27.AC-4** After a User of the upper Rotation layer is disabled — or turned into a Viewer, or taken out of the Team
  by the identity provider — the On-call during their Shifts is the person of the lower Rotation layer, and the Schedule
  shows the matching warning; where no Override and no other Rotation layer has a Shift with someone who may be Paged,
  their Shifts are gaps.
- **C-27.AC-5** With a gap 5 days ahead in a Team's main Schedule, the next scan raises `MusterScheduleGap` with the
  Schedule's id and name, `team_id`, the Team's slug under the Team label and the start of the gap in its annotation,
  and `muster_schedule_gap` is 1; the alert belongs to the Team and records no `unknown_team_value`; once the gap has
  started it stays raised; an Override that closes the gap resolves it on the next scan, and so does the end of the gap;
  running the scan twice raises it once; a gap 8 days ahead raises nothing yet; a second Schedule of the Team with a gap
  raises nothing until an Admin turns `schedule.gap_alert` on for it.
- **C-27.AC-6** An On-call without a Mattermost Account link, in a Team with a Mattermost Destination, is marked
  unreachable in Mattermost on the Schedule and in "On call now".
- **C-27.AC-7** A User's iCal feed lists, in `ical.window`, one event for each segment of the Result in which they are
  On-call, adjacent segments of one Schedule joined — with the two Rotation layers of scenario 1, in a week when Petr
  has the lower Rotation layer and Olga the upper one, Petr's week shows as its nights and weekend only — and their
  Overrides; the feed is served at `/ical/{token}.ics`; the token is returned once, its prefix is that of `ical.token`,
  the database holds only its hash, a request log line shows it masked; after "New link" the old URL gets `404`, and so
  does the new one once the User is disabled.
- **C-27.AC-8** Deleting a Schedule named by an Escalation step gets `409` `schedule_in_use`; making another Schedule
  main changes the Team's On-call, and with `schedule.gap_alert` "by default" on both, the gap alert moves to the new
  main Schedule, while one an Admin set on stays on.
- **C-27.AC-9** In a real browser, the Schedule page shows the two-week view with the row "Result", the gap in red and
  the warnings; "Add Override" in the mode "I give my Shift to…" saves an Override; the Home page shows "On call now",
  in English and in Russian.
- **C-27.AC-10** Two Overrides that overlap make the person of the one created last On-call in the overlap; a Responder
  gets `403` on creating, changing or deleting a Schedule or a Rotation layer.

## Related ADRs

ADR-0007, ADR-0011, ADR-0013, ADR-0014, ADR-0018.

## Depends on

C-18 — Account links, for the unreachable warning; C-19 — Internal alerts and chart rules; C-25 — Teams; C-26 — the
Destinations of a Team. The warning covers the Threads of Escalation chains from C-32 on and calls from C-30 on.

## Suggested story split

- **BE** — Schedules, Rotation layers, Overrides with their Permission and precondition, the On-call and the Result.
- **BE** — gaps, the Leader's scan with `MusterScheduleGap`, its chart rule and runbook, the warnings about unreachable
  On-calls.
- **BE** — the iCal feed with its token.
- **FE** — the Schedule page with its two-week view and the Override form, the Schedules of the Team page, "On call now"
  on the Home and Team pages, the iCal section.
