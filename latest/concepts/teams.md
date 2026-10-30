# Teams

A **Team** says who owns what: Routes and Destinations may name the Team that owns them, so that each team finds its
own configuration with a filter. Alerts and Alert Groups belong to Teams by their labels, so that each team finds its
Alert Groups. Teams are managed through the API under `/api/v1/teams`.

## What a Team is

A Team has:

- a **name**, unique in the Organization, compared without regard to case;
- a **slug**, such as `payments`. A slug is 1 to 63 characters — lowercase letters, digits, `-`, `_` and `.` — and
  starts with a letter or a digit (`team.slug_format`), so `Payments` and `-x` are refused. It is unique in the
  Organization and compared exactly;
- a **description**;
- its **Team matchers**, which pick the Alerts that belong to it beside its slug;
- the **people** in it, which may be nobody;
- its **Working hours**: its own, else the Organization's.

What a Team owns
:   A Route or a Destination names its owning Team in the field `owning_team_id`; reads carry it as `owning_team`,
    with the Team's id, name and slug. `listRoutes` and `listDestinations` take the parameter `team`: a Team's id, or
    `mine` for the Teams you are in ("My Teams"). The owner only organizes: owning a Destination does not make it
    deliver to the Team, and a Route's owning Team adds no delivery.

What a Team does not grant
:   A Team grants no Permission and limits none: Roles stay the same for the whole Organization. A Responder in a Team
    cannot change the Team, and a Viewer in a Team is refused Acknowledge as any Viewer is. Only an Admin creates,
    edits and deletes Teams and adds people to them; every Role reads them.

Deleting a Team
:   The people leave it, its Working hours and Team matchers are removed, and the Routes and Destinations it owned
    have no owner from then on, each with a new version. Its slug and name are free at once for a new Team, and its
    slug no longer gives Alerts the Team. The Team itself is kept, marked deleted, for what still names it: the Alert
    Groups that had it among their Teams list it by its name, with `deleted` true.

Every change of a Team and of the people in it is recorded in the Audit log, and the metric `muster_team_info` has
one series per Team that is not deleted, with its name ([Metrics](../reference/metrics.md)). The operations are listed
in the [API reference](../reference/api.md).

## Which Alerts belong to a Team

An Alert belongs to a Team in two ways, and may belong to several Teams.

By the Team label
:   The Team label is an Organization setting, `team` by default (`organization.team_label`, `team_label` of the
    Organization's settings). An Alert whose value of the Team label, with the Static labels of its Integration,
    equals a Team's slug exactly belongs to that Team: with the Team "payments", `team="payments"` belongs to it and
    `team="Payments"` does not. The Team label cannot be a label that an Internal alert sets itself, such as
    `alertname`, `severity`, `integration`, `schedule` or `team_id`: such a label is refused with `422`
    `reserved_label`.

By Team matchers
:   A Team's `matchers` use the syntax of Route Matchers on any label — `=`, `!=`, `=~` and `!~`, regular expressions
    anchored at both ends, a missing label counting as empty — and each is enough on its own: an Alert that any one of
    them picks belongs to the Team. The Team with the matchers `namespace=~"checkout|billing"` and
    `pod=~"checkout-.*"` gets an Alert with `namespace="billing"` and one with `pod="checkout-api-1"`, and not one
    with `namespace="search"`. Matchers are saved with the Team, with `If-Match`, and replaced as a whole; an update
    that omits them keeps them. An expression that is not RE2 is refused with `422` `invalid_regex` at its pointer.
    A matcher with only a negative operator, such as `env!="prod"`, picks every Alert that lacks the label, Internal
    alerts included, because a missing label counts as empty.

Muster decides the Teams of an Alert when it starts firing — its first firing, and each firing after it resolved —
and records them on the Alert with the rule that matched: `label`, or `matcher` with the matcher's text. The Alerts
view of an Integration shows them as `teams`: the Teams of the Alert's latest firing, which replace those of an earlier
one. A Continuation is not a new firing and decides nothing; a Team created or
changed later reaches the Alerts that fire from then on.

The Teams of an Alert Group are those of every Alert that ever joined it. They only grow: a Team stays among them
after its Alerts resolve. Each Team that joins records the Quiet lifecycle event `team_joined` in the Timeline, naming
the Team and the rule that brought it; `getAlertGroup` and `listAlertGroups` carry the Teams as `teams`, and outgoing
webhooks in events mode carry them in `alert_group.teams` ([Events mode](../outgoing-webhooks/events.md)). An event
carries the state of the Alert Group, not a change: each `team_joined` lists every Team the Alert Group has at that
moment, the new one included. The
parameter `team` of `listAlertGroups` and `getAlertGroupCounts` takes a Team's id, or `mine` for the Alert Groups of
the Teams you are in ("My Teams"). The Teams of an Alert Group exist whatever its Route says: they do not change where
it is delivered.

A worked example
:   A rule generator sets `category`, `team` and `system` on every alert rule. With the Team label `team`, each Team's
    slug is the value its rules carry — `payments`, `checkout` — and the Alerts reach their Teams without any matcher.
    A Team without people that only names Alerts is fine: it groups and filters them.

Test clusters with one shared namespace
:   When every team deploys into the same namespace of a test cluster, `namespace` does not tell them apart. Match
    another label instead: the Pod name (`pod=~"checkout-.*"`), an app label the workloads carry
    (`app_kubernetes_io_part_of="checkout"`), or put the Team label itself on the workloads, so that their Alerts carry
    `team="checkout"`.

## Values without a Team

A value of the Team label that names no Team — a typo, a team without a Team yet, or a case that differs, such as
`Payments` — adds nothing to the Alert Group: it is delivered to its Route's Destinations as before. Its Timeline
records the Quiet lifecycle event `unknown_team_value` with the value, once per value and Alert Group, and outgoing
webhooks in events mode receive it. Create a Team with that value as its slug, or add a Team matcher that picks those
Alerts, and the Alerts that start firing from then on belong to it. A value that is not a valid slug, such as
`Payments`, reaches a Team only through a Team matcher.

With the default Team label `team` and no Teams defined yet, every Alert that carries a `team` label records
`unknown_team_value` in its Alert Group's Timeline and sends it to outgoing webhooks in events mode. Create the Teams,
or change the Team label to a label your Alerts do not use for something else, to avoid it.

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

A Team's Working hours
:   A Team's own Working hours, with days, a start, an end and an IANA time zone such as `Europe/Berlin`
    (`own_working_hours`), else the Organization's. A Team without its own moves with the Organization: a new start of
    the working day moves the Team's start too.
