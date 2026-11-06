# Routing to Teams

One Route can serve many teams. Each Destination of a Route, and each Destination of a Team, may be limited by
Severity level, and a Route may also deliver its Alert Groups to the Destinations of the Teams of their Alerts. Every
Destination then shows the same Alert Group with one status.

The feature is optional. Many installations need only two operations channels — one for critical Alert Groups and one
for the rest — and a Route that names both with a Severity condition each. Teams and their Destinations come in when
teams want their own channels.

## Severity conditions

A Route names its Destinations in `destination_entries`, each with a `severity_condition`, and a Team names the
Destinations of the Team the same way:

`any`
:   Every Severity level, the default.

`critical`
:   Only an Alert Group whose Severity level is critical.

`below_critical`
:   Every level but critical.

The condition is checked against the Alert Group's Severity level: the highest level of its firing Alerts, raised to
the Route's Minimum severity. A Destination may be named by several Routes and Teams, each with its own condition, and
is reached when the condition of any of its entries is met.

Until 0.3.0 Routes still read and take `destination_ids`, derived from `destination_entries`: a request that sends only
`destination_ids` keeps the condition of each Destination it keeps and gives a new one `any`.

## Also to the Teams from the label

With "Also to the Teams from the label" (`deliver_to_teams`) on, an Alert Group of the Route also goes to the
Destinations of each of its Teams — the Teams its Alerts belong to by the Team label or by Team matchers
([Teams](teams.md#which-alerts-belong-to-a-team)) — whose condition its Severity level meets. With it off, the Alert
Group goes only to the Route's own Destinations. The owning Team of a Route adds nothing: it only organizes and
filters.

"Run the Teams' Escalation chains" (`run_team_chains`) is stored with the option; it acts once Escalation chains run.

### Operations and team channels by Severity

Operations has `infra-critical` and `infra-notice`, and the Team `payments` has `payments-critical` and
`payments-notice`. The Route "Infrastructure" takes `category="infra"`, names `infra-critical` for `critical` and
`infra-notice` for `below_critical`, and has "Also to the Teams from the label" on. The Team `payments` names
`payments-critical` for `critical` and `payments-notice` for `below_critical`.

A warning Alert of `team="payments"` makes one Alert Group with one Root message in `infra-notice` and one in
`payments-notice`, and none in the critical channels. If a critical Alert joins it, the Alert Group is also published
in `infra-critical` and `payments-critical`; its messages in the notice channels stay and keep being updated, also
after the critical Alert resolves. The Destinations of an Alert Group only grow, and only when a Team joins it or its
Severity level rises: a level that falls again, or a new Alert that brings neither, adds no Destination below critical
where the Alert Group was not published before.

## One Alert Group, one status

An Alert Group has one status and one list of Alerts in every channel it is in. Acknowledge it from `payments-notice`
and its message in `infra-notice` shows the same Owner; resolve it anywhere and every message shows it resolved. The
delivery state of the Alert Group (`listAlertGroupDeliveries`) names, for each Destination, the entries that reached
it — the Route, or a Team — with their conditions.

When an Alert Group reaches a Destination after its first Publication elsewhere — a new Alert raised its Severity level
into the Destination's condition, or brought a Team — the lifecycle event `destination_joined` publishes it there:
Loudly, with the Mentions of a new Alert Group, while it is firing; Quietly while it is acknowledged or snoozed. The
other Destinations see only an update of their Root message. During a Storm of the Route the Destination counts it in
its Storm summary instead, unless it is Urgent.

A change of configuration publishes Quietly and never with `destination_joined`: a raised Minimum severity, or a new
Team whose Alerts are already in an open Alert Group, adds that Alert Group to the Destinations it now reaches without
a Mention.

### Teams that want their own Alert Groups

In a Route whose Group key lacks the Team label, Alerts of different Teams with the same key share one Alert Group,
shown in the channels of all of them: an Alert of "search" that joins a firing Alert Group of "payments" is published
in `search-notice` with `destination_joined`. Teams that want their own Alert Groups add the Team label, `team` by
default, to the Group key of the Route.

## A Route for test clusters

A Route "Test clusters" placed first, with `cluster=~".*-stage|.*-preprod"`, no Destination of its own, "Also to the
Teams from the label" on and "Run the Teams' Escalation chains" off sends each team its test alerts in its own channels
and Pages nobody.
