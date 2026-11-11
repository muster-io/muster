# What critical changes

An Alert Group whose Severity level is **critical** is **Urgent**. Nothing else makes an Alert Group Urgent, and there
is no separate switch: since 0.2.0 the urgent mark of a Route and the Organization's "critical is Urgent" no longer act
([Urgent](routes-and-group-keys.md#urgent)). This page lists everything that Urgent changes, and then what the
[Minimum severity](routes-and-group-keys.md#minimum-severity) of a Route adds, so that you know what a critical Alert,
or a Route that raises its Alert Groups to critical, makes Muster do.

The Severity level of an Alert Group is the highest level of its firing Alerts, raised to the Minimum severity of its
Route. It follows the Alerts as they fire and resolve, so an Alert Group may become Urgent and stop being Urgent again.

## What an Urgent Alert Group changes

The order of the delivery queue
:   The delivery queue of a Destination takes Urgent Alert Groups first. They never pass the rate limit of the
    Destination: an Urgent Alert Group waits for its place within the limit like any other, only before the rest.
    Among the Urgent ones and among the rest, the edits that only a change of configuration made, such as a new
    button set, come last ([Organization](../configure/organization.md#alert-handling)).
    After an outage of a Destination, the Alert Groups that were not published are published Urgent first
    ([Broken Destinations](broken-destinations.md#what-is-sent-after-recovery)).

Individual publication during a Storm
:   When a Route creates more new Alert Groups in a minute than its Storm threshold, each of its Destinations gets one
    Storm summary instead of a message per Alert Group. An Urgent Alert Group is not held back by it: it is published
    on its own, at once and ahead of the queue ([Storm summaries](loud-and-quiet.md#storm-summaries)). The same holds
    for a Destination that an Alert Group joins during the Storm: unless the Alert Group is Urgent, the Destination
    counts it in its Storm summary ([Routing to Teams](routing-to-teams.md#one-alert-group-one-status)).

The end of a Snooze or of an acknowledgement on a rise
:   When a rise in Severity level makes an Alert Group Urgent, it ends a Snooze, unless the Snooze was set while the
    Alert Group was already Urgent. It also removes the acknowledgement where the Route's setting "Rise to Urgent
    removes the acknowledgement" (`route.urgent_rise_removes_ack`) is on, which it is by default. The Alert Group is
    Firing again, and the change is Loud, with the reason. It Mentions the Owner it removed and the people of the
    `rise_to_urgent` Mention setting of each Destination ([Loud and Quiet](loud-and-quiet.md#which-changes-are-loud),
    [Statuses and commands](statuses-and-commands.md#transitions-muster-makes-on-its-own)). A rise that changes
    nothing else is Quiet.

The filter and the column of the list
:   The list of [Alert Groups](alert-groups.md#in-the-web-ui) has the filter **Urgent**, which shows only the Urgent
    Alert Groups or only those that are not, and the column **Urgent**, which marks each Urgent Alert Group. The API
    takes the parameter `urgent` of `listAlertGroups`.

`urgent` in outgoing webhook events
:   An event in events mode carries the Alert Group with `alert_group.urgent`, true exactly when its Severity level is
    critical ([Events mode](../outgoing-webhooks/events.md)), and a template reads it as `.AlertGroup.Urgent`
    ([Templates](../outgoing-webhooks/templates.md#what-the-templates-read)).

The count in Storm summaries
:   The Storm summary of a Route says how many Alert Groups the Storm holds and how many of them are Urgent, such as
    "Storm on payments: 24 Alert Groups, 3 urgent", and a template reads `.Storm.UrgentCount`.

Urgent is not a status, and no person sets it: the Root message of an Alert Group shows its Severity level, and the
page of the Alert Group marks it Urgent.

## What the Minimum severity adds

The Minimum severity of a Route raises the Severity level of its Alert Groups, never that of their Alerts. With the
Minimum severity critical, every Alert Group of the Route is critical, and so Urgent, from its first Alert, even where
every Alert says `warning`: everything above applies to it, and the Destinations of the Route with the Severity
condition `critical` receive it ([Severity conditions](routing-to-teams.md#severity-conditions)). With the Minimum
severity `warning`, an Alert Group of `info` Alerts is a warning one, which is not Urgent, and a Destination with the
condition `below_critical` still receives it.

A change of the Minimum severity applies at once to the open Alert Groups of the Route, and is gentler than a rise of
an Alert:

- It never removes an acknowledgement and never ends a Snooze, also where it makes an Alert Group Urgent: the Timeline
  records a Quiet `urgency_raised` with the reason "Minimum severity of the Route", and nothing else.
- It starts no Escalation chain, and the Root messages are edited Quietly.
- The Alert Groups it raises into the Severity condition of a Destination reach that Destination Quietly, with no
  Mention ([When you change a Route or a Team](routing-to-teams.md#when-you-change-a-route-or-a-team)).
- A lowered Minimum severity records nothing and lowers the level of the Alert Groups, as a falling level does. The
  messages that an Alert Group already has stay where they are.

The Alerts keep their own level: the Alerts view, the Alerts of the Alert Group page and `alerts[]` of outgoing webhook
events show what each Alert says.
