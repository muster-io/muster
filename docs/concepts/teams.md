# Teams

A **Team** says who owns what. Routes and Destinations may name the Team that owns them, so that each team finds its
own configuration with a filter, and from 0.2.0 on Teams also gain the Alerts that belong to them, their own
Destinations, Schedules and Escalation chains. A Team is managed under `/api/v1/teams`; its pages in the web UI come
with a later release of 0.2.

## What a Team is

A Team has:

- a **name**, unique in the Organization, compared without regard to case;
- a **slug**, the value of the Team label that names the Team, such as `payments`. A slug is 1 to 63 characters —
  lowercase letters, digits, `-`, `_` and `.` — and starts with a letter or a digit (`team.slug_format`), so
  `Payments` and `-x` are refused. It is unique in the Organization and compared exactly;
- a **description**;
- the **people** in it, which may be nobody;
- its **Working hours**, the Organization's or its own.

What a Team owns
:   A Route or a Destination names its owning Team in the field `owning_team_id`; reads carry it as `owning_team`,
    with the Team's id, name and slug. `listRoutes` and `listDestinations` take the parameter `team`: a Team's id, or
    `mine` for the Teams you are in ("My Teams"). The owner only organizes. Owning a Destination does not make it one
    of the Team's Destinations, and a Route's owning Team adds no delivery.

What a Team does not grant
:   A Team grants no Permission and limits none: Roles stay the same for the whole Organization. A Responder in a Team
    cannot change the Team, and a Viewer in a Team is refused Acknowledge as any Viewer is. Only an Admin creates,
    edits and deletes Teams and adds people to them; every Role reads them.

Deleting a Team
:   The people leave it, its Working hours are removed, and the Routes and Destinations it owned have no owner from
    then on, each with a new version. Its slug and name are free at once for a new Team. The Team itself is kept,
    marked deleted, so that what names it — later, the Alert Groups it had — still shows its name.

Every change of a Team and of the people in it is recorded in the Audit log, and the metric `muster_team_info` has
one series per Team that is not deleted, with its name ([Metrics](../reference/metrics.md)). The operations are listed
in the [API reference](../reference/api.md).

## People

An Admin adds Users to a Team by hand and removes them, through the API (`addTeamUser`, `removeTeamUser`). A User may be
in several Teams. The list of a Team's people shows each person's Role, how they are in the Team — `by_hand` for a
person an Admin added — and the messengers they linked.

- Disabling a User keeps them in their Teams.
- Deleting a User takes them out of every Team.

## Working hours

Working hours are days of the week, a start and an end on the 24-hour clock, and a time zone. The end is later than
the start on the same day: Working hours never span midnight. Holidays are not part of them.

The Organization's Working hours
:   Monday to Friday, from the start of the working day — the time at which the Snooze presets end, `09:00` by
    default — to `18:00`, in the Organization's time zone (`organization.working_hours`). An Admin changes the days
    (`working_days`) and the end (`workday_end`) with the Organization's settings; an end that is not later than the
    start is refused.

A Team's own
:   A Team follows the Organization's Working hours unless it sets its own, with days, a start, an end and an IANA
    time zone such as `Europe/Berlin` (`own_working_hours`). A Team that follows the Organization's moves with it: a
    new start of the working day moves the Team's start too.

Escalation steps limited to Working hours use those of the Team that owns their Escalation chain.
