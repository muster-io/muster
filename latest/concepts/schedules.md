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
- its **Overrides**.

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
