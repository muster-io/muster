# Schedules

A **Schedule** says who in a Team is On-call and when. It is made of Rotation layers, in which people take Shifts in
turn, with Overrides on top. Schedules are managed through the API under `/api/v1/schedules`; nothing about a Schedule
sends a message.

## What a Schedule is

A Schedule belongs to one Team and has:

- a **name**, unique in the Team, compared without regard to case;
- a **time zone**, such as `Europe/Berlin`. A Schedule created without one takes the Organization's time zone of that
  moment and keeps it when the Organization's changes later (`schedule.time_zone`);
- its **Rotation layers**, from the lowest to the highest;
- its **Overrides**;
- its **gap alert**, whether it reports its gaps as an Internal alert (see
  [Gaps and the Internal alert](#gaps-and-the-internal-alert)).

The **On-call** of a Schedule at a moment is the person of its Override then; otherwise the person on Shift in the
highest Rotation layer that has a Shift then; otherwise nobody, which is a **gap**. `getScheduleOnCall` gives the
**Result** of a period — by default from now to 14 days ahead (`schedule.gap_view`), at most 67 days — cut into
segments, each with the person On-call or nobody, together with the Shifts of each Rotation layer and the Overrides.
Muster computes it on its own clock, so the development clock moves it.

Only Admins create, change and delete Schedules (`schedules:write`); everyone reads them (`schedules:read`). Every
change is recorded in the Audit log, and the metric `muster_schedule_info` has one series per Schedule, with its name
([Metrics](../reference/metrics.md)). The operations are listed in the [API reference](../reference/api.md).

## Rotation layers

A Schedule has at most 20 Rotation layers. A Rotation layer has a name and the people who take Shifts in turn, in
order, at most 100. A Shift lasts:

- a **day**, handed over every day at the handover time;
- a **week**, handed over on a day of the week at the handover time;
- a number of **whole hours**, given as `shift_seconds`, a multiple of 3600 such as 43200 for 12 hours, up to 28 days.

The first Shift starts on the start date, a day from 2000-01-01 to 2100-12-31, at the handover time, with the first
person of the list. Times are wall-clock times in the Schedule's time zone, so a handover at 10:00 stays at 10:00 when
the clocks change. A time that the clocks skip when they move forward, such as 02:30 on the night of the change, is
the moment they move; a time that happens twice when they move back is its first occurrence.

A Rotation layer may be **limited** to some days of the week and, optionally, a time of day, such as Monday to Friday
11:00–19:00. Outside the limits it has no Shift. A time of day whose end is earlier than its start crosses midnight,
and each window belongs to the day it starts on: Friday 19:00–09:00 runs from Friday 19:00 to Saturday 09:00.

A higher Rotation layer wins wherever it has a Shift. A common Schedule has two:

Around the clock, below
:   Weekly Shifts handed over on Monday at 10:00 among Petr, Sergey and Olga.

Working hours 11–19, above
:   Weekly Shifts on weekdays from 11:00 to 19:00 among Olga and Petr.

On a Wednesday at 15:00 the On-call is the person of the upper Rotation layer; at 21:00 and at the weekend, the person
of the lower one.

A Rotation layer may only add people who can be Paged: Users in the Team who are neither Viewers nor disabled. Adding
anyone else is refused with `422` `not_pageable` at the person, with the reason `viewer`, `disabled` or `not_in_team`.
Someone who becomes one of these later stays in the Rotation layer: their Shifts are skipped — the Rotation layer
below, or a gap, takes the time — and the Result lists a warning about them until an Admin removes them.

## Several Schedules

A Team may have several Schedules, such as a first and a second line. One of them is its **main Schedule**, whose
On-call is the Team's On-call: the first Schedule of a Team is its main one, and an Admin makes another one main with
`main` set to true, which takes the mark off the previous one. The main Schedule cannot be unmarked on its own; deleting
it leaves the Team without one until an Admin marks another.

A Team that has Schedules cannot be deleted: `deleteTeam` answers `409` `team_in_use` and names them.

## Overrides

An **Override** makes one person On-call from one time to another, on top of every Rotation layer: within its span it
wins over all of them, and so it also closes a gap. It may name the person whose Shift it takes, which changes nothing
in the On-call. Where Overrides overlap, the one created last wins.

Who may make them
:   Responders and Admins (`schedule-overrides:write`). A Responder must be in the Schedule's Team, else `409`
    `not_in_team`; an Admin need not. The person who takes the Shift must be able to be Paged, else `422`
    `not_pageable` at `user_id`; the person whose Shift it takes may be anyone.

When
:   An Override may be planned any time ahead, but it may not rewrite the past. It starts at most 5 minutes before now
    (`422` `out_of_range` at `start`), ends after its start and after now, and lasts at most 31 days (`422`
    `out_of_range` at `end`). A span that would overlap 1000 other Overrides of the Schedule is refused the same way.

Changing and deleting
:   Before an Override begins, everything in it may change, and deleting it removes it. Once it has begun, only its end
    may change — another start or person is refused with `422` `unsupported` — and deleting it ends it now: the part
    that has passed stays in the Schedule. Once it has ended, it can be neither changed nor deleted (`409`
    `override_ended`), so the past stays as it was.

No messages
:   Overrides need no approval and send no message, and neither do Shifts: who is On-call is shown by Muster and its
    API, and every Override is recorded in the Audit log with its people and span.

## iCal feed

Each User may subscribe a calendar application to a personal **iCal feed** of the times when they are On-call. The
feed has one event for each span in which the User is On-call in a Schedule of any of their Teams, named
"{Team} · {Schedule}", from 7 days back to 60 days ahead (`ical.window`), counted in whole days in UTC:

- It follows the Result: where a higher Rotation layer or someone else's Override has the time, it is not in the feed.
  With the two Rotation layers above, a week in which Petr has the lower Rotation layer shows only its nights and its
  weekend.
- An Override that makes the User On-call is an event of its own span; adjacent spans of one Schedule are otherwise
  one event, so a Rotation layer limited to Friday 19:00–09:00 is one event from Friday 19:00 to Saturday 09:00.
- Times are in UTC, so a calendar application shows them in its own time zone, across the changes of the clocks too.

The link
:   `issueMyIcalFeed` (`POST /api/v1/me/ical-feed`) gives the link, `MUSTER_PUBLIC_URL/ical/{token}.ics`, from the web
    session only. It is shown once: Muster keeps only a hash of its token (`ical.token`, starting with `mstr_cal_`), so
    copy it at once. `getMyIcalFeed` tells whether a feed exists and when it was issued.

A new link revokes the old one
:   Each User has one feed. Issuing a new link stops the old one at once, and `revokeMyIcalFeed` stops the current one;
    a stopped link, and the link of a disabled or deleted User, answers `404`. Deleting a User deletes their feed.
    After a link stops, calendar applications keep showing the copy they last fetched.

An Admin revokes the feed of a User
:   If a link leaked, an Admin revokes it for the User: "Revoke iCal feed" on the User's page, or `revokeUserIcalFeed`
    (`DELETE /api/v1/users/{user_id}/ical-feed`, permission `users:write`). The link stops at once on every replica,
    the Audit log records `ical_feed.revoked` with the reason `revoked_by_admin`, and the User can issue a new link
    from the profile.

Counting the fetches
:   `muster_ical_feed_requests_total{outcome}` counts the fetches: `served`, `not_found` (a malformed, unknown or
    revoked token, or a disabled or deleted User's) and `failed`. It has no label for the token or the User.

Calendar applications fetch the link without signing in, so treat it as a password: anyone who has it sees the times.
Muster never writes it to its logs, which show the path as `/ical/[redacted].ics`; a reverse proxy or ingress in
front of Muster records the real path in its own access log, so keep that log private or mask `/ical/` there.
Schedules are not imported from iCal: the feed only shows what the Schedules say.

## Gaps and the Internal alert

A **gap** is a time when no Override and no Rotation layer has a Shift with a person who may be Paged, so nobody is
On-call. The people whose Shifts are skipped are Viewers, disabled Users and Users who are no longer in the Team — after
the groups of the identity provider changed, for example: the Rotation layer below takes their time, or a gap does.
`getScheduleOnCall` lists a warning `gap` with its `start` and `end` for each gap of the period, besides the warnings
`disabled`, `viewer` and `not_in_team` about those people, and every Schedule carries `next_gap`, the first gap of the
next 14 days (`schedule.gap_view`), or null. A Rotation layer limited to Friday 19:00–09:00 alone has its gaps from
Saturday 09:00 to Friday 19:00; the night from Friday to Saturday is no gap.

A Schedule may report its gaps as the Internal alert `MusterScheduleGap` (`schedule.gap_alert`):

`by_default`
:   The default: on while the Schedule is its Team's main one, off otherwise. When an Admin makes another Schedule
    main, the alert follows it at the next scan.

`on`
:   On whether or not the Schedule is main.

`off`
:   Never.

`gap_alert_active` says whether it is on now. Every 5 minutes (`schedule.gap_scan_interval`) the Leader looks at the
next 7 days (`schedule.gap_alert_horizon`) of each Schedule whose gap alert is active. A gap now, or one that starts
within those 7 days, raises `MusterScheduleGap` with the Schedule's and its Team's id and name, the Team's slug under
the Organization's Team label, so that the alert belongs to the Team, and the start of the nearest gap in the annotation
`gap_start`. The alert stays raised while the gap lasts and resolves at the next scan once no gap is current or within
the 7 days — an Override or a change of the Rotation layers closed it, or it is past — and when the gap alert is turned
off or the Schedule is deleted. Renaming the Schedule or its Team updates the open alert. The metric
`muster_schedule_gap` is 1 for each Schedule with such a gap and 0 for the others, and the chart rule of the same name
is the backup for the Internal alert; its [runbook](../operations/runbooks/MusterScheduleGap.md) says how to fill a gap.

A Schedule that covers only part of the day on purpose, such as a second line in working hours, has gaps every evening:
keep its gap alert off. With `by_default` it already is, as long as it is not its Team's main Schedule.

## On call now

`getOnCallNow` lists every Team by name — or with `teams=mine` the Teams of the caller — with the On-call of its main
Schedule now: the person and the Rotation layer whose Shift it is, until when, and what follows, a person or a gap,
within the next 14 days. A Team without a main Schedule has `no_schedule` true.
