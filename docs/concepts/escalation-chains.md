# Escalation chains

An **Escalation chain** Pages more and more people while an Alert Group stays firing. Its steps Page the On-call of a
Schedule, chosen people or a whole Team, call through a [Notification gateway](../messengers/notification-gateways.md),
wait, send to an outgoing webhook and repeat. Routes and Teams name their chains. Chains are managed through the API under
`/api/v1/escalation-chains`, listed in the [API reference](../reference/api.md).

Chains run on Alert Groups at the points described under [When chains run](#when-chains-run): their steps follow one
another with their waits and repeats, and a step is skipped where it cannot be made. A webhook step sends its event
to its outgoing webhook ([Webhook steps](#webhook-steps)). A step that Pages people or a Team writes a Mention in the
Threads of its chain's own Destinations ([Where chains Page](#where-chains-page)); the calls through a Notification
gateway follow in a later version. Where no chain runs, the ack timeout of a Route keeps working as before.

## What an Escalation chain is

An Escalation chain has:

- a **name**, unique in the Organization, compared without regard to case;
- a **description**;
- an optional **owning Team**, which organizes the chains — the list filters by Team, `team=<public_id>` or `team=mine`
  — and whose Working hours its steps use;
- its **steps**, in their order, numbered from 1.

Only Admins create, change and delete chains (`escalation-chains:write`); everyone reads them
(`escalation-chains:read`). A change is saved with `If-Match`, like every configuration, and is recorded in the Audit log
as `escalation_chain.created`, `escalation_chain.updated` or `escalation_chain.deleted` with the values before and after.
The metric `muster_escalation_chain_info` has one series per chain, with its name ([Metrics](../reference/metrics.md)).

A chain's read also lists the Routes and Teams that name it (`referrers`), the Destinations it writes in — those of
those Routes and Teams (`destinations`) — and its `warnings`, what the chain or a step cannot do:

- `pages_nobody`: no step of the chain Pages anyone — it has no step that Pages the On-call, chosen people or a Team,
  or calls, such as an empty chain or one of waits, repeats and webhooks. It concerns the whole chain, so its `step` is
  null, and it comes first. The chain is saved all the same;
- `everyone_unsupported`: a step "Page the whole Team" whose Team mention, or word for everyone in the chat, the
  Connection of one of the chain's Destinations cannot mention, such as a Telegram Destination, with the Destination and
  the reason;
- `gateway_deleted`: a call step whose Notification gateway was deleted, or is no Notification gateway any more;
- `destination_deleted`: a webhook step whose Destination was deleted;
- `team_deleted`: a step "Page the whole Team" whose Team was deleted since it was saved, instead of its
  `everyone_unsupported` warnings.

A chain that a Route or a Team names cannot be deleted: `deleteEscalationChain` answers `409` `chain_in_use`, naming
them in `in_use_by`. A Team that owns a chain cannot be deleted either (`409` `team_in_use`), and a Schedule that a step
names cannot be deleted (`409` `schedule_in_use`).

## Steps

A chain has at most 20 steps (`escalation.limits`). Each step is of one kind and takes the fields of its kind only; a
field of another kind is refused with `422` `unsupported` at it, and a missing one with `required`:

| Kind | What it does | Its fields |
|---|---|---|
| `page_on_call` | Pages the On-call of a Schedule of any Team | `schedule_id`, `importance` |
| `page_people` | Pages chosen people | `user_ids`, `importance` |
| `page_team` | Pages a whole Team by its Team mention, or everyone in the chat | `team_id` or `everyone` |
| `call` | Calls the On-call of a Schedule or chosen people through a Notification gateway | `call_target`, `schedule_id` or `user_ids`, `gateway_id` |
| `wait` | Waits a number of whole minutes | `wait_seconds` |
| `webhook` | Sends to an outgoing webhook Destination | `destination_id` |
| `repeat` | Goes back to an earlier step, a number of times | `repeat_from`, `repeat_times` |

- **Importance.** A Page is `normal` or `important`, `normal` when omitted; it runs the person's
  [Notification rule](notification-rules.md) of that name.
- **People.** A step names people who may be Paged: a Viewer or a disabled User that a save adds is refused with `422`
  `not_pageable` at `/steps/{i}/user_ids/{j}`, with the `reason` `viewer` or `disabled`. A person the stored steps
  already name is kept when the chain is saved again, even if they have become a Viewer or been disabled since; a
  running chain will skip them.
- **The whole Team.** Without `team_id` and `everyone`, the step Pages the chain's owning Team; a chain without an
  owning Team must name the Team (`required` at `team_id`). `everyone` is `channel`, `here`, `all` or `everyone`.
- **Calls.** `call_target` is `on_call`, with `schedule_id`, or `people`, with `user_ids`. A call step needs a
  Notification gateway: without one it is refused with `422` `gateway_required`; with one, `gateway_id` may be omitted
  and the step takes that gateway; with several, `gateway_id` is required.
- **Waits.** A wait lasts from 1 to 1,440 minutes, given in seconds: `wait_seconds` is a multiple of 60 from 60 to
  86400, so a wait of 10 minutes is `{"kind": "wait", "wait_seconds": 600}`. Another value is refused with `422`
  `out_of_range` at it.
- **Webhooks.** The Destination of a webhook step is an outgoing webhook in the mode `events` or `both`; another one is
  refused with `422` `webhook_events_required`.
- **Repeats.** `repeat_from` is the number of an earlier step, and `repeat_times` from 1 to 10.
- **Limits.** Every step but `wait` and `repeat` takes `working_hours_only` — the step is made only in the Working
  hours of the chain's owning Team, else the Organization's — and a `severity_condition`, `any` by default, `critical`
  or `below_critical`.

A gateway or a webhook Destination that a stored step names may be deleted; the chain warns about it and can still be
saved.

### Webhook steps

A step "Send to an outgoing webhook" (`webhook`) sends the event `escalation_webhook` to the outgoing webhook
Destination it names when the step is made. The event carries the Alert Group, as every event does, with the chain and
the step. The Destination receives it whether or not the Alert Group is delivered there, so an outgoing webhook on
none of the Alert Group's Routes can be called at a chosen step of a chain. Such a Destination receives no other event
of the Alert Group, and the events-mode Destinations of the Alert Group's Routes do not receive `escalation_webhook`.
A Destination that is also one of the Alert Group's receives it once, in order among its other events. The event is
signed, ordered and delivered at least once like every event
([escalation_webhook](../outgoing-webhooks/events.md#escalation_webhook)).

The Timeline records the Quiet `escalation_webhook`, of the kind `escalation`, with the chain, the step and the
Destination; nothing appears in a messenger. Like every step that sends, a webhook step can be limited to Working hours
and to a Severity condition. A running chain skips a webhook step, recording `escalation_step_skipped`, when its
Destination:

- was deleted since the step was saved: `destination_deleted`;
- is no longer an outgoing webhook in the mode `events` or `both`, such as one switched to the template mode:
  `webhook_events_required`.

## Naming chains on Routes and Teams

Routes and Teams name their chains in `chain_entries`, as they name their Destinations: each entry is a chain with a
Severity condition — `any`, the default, `critical` or `below_critical` — checked against the Severity level of the
Alert Group. An entry list replaces the stored one as a whole, and an update that omits it keeps it. Reads give the
entries and the chains by name, `chains`, and the Audit log of the Route or the Team records the change.

```json
{"chain_entries": [{"chain_id": "ECK7M3QX9P2RTA", "severity_condition": "critical"}]}
```

Naming a chain on a Route needs `routes:write`, and on a Team `teams:write`. A chain Pages only in the Threads of its
own Destinations: a chain of a Route in those of the Route's Destinations that have the Alert Group's Root message; a
chain of a Team in those of the Team's Destinations that have it, and, where none has, in those of the Route's
Destinations. So a step that Pages everyone in the chat never rings another team's chat.

## When chains run

The chains that run on an Alert Group are those its Route names and — where the Route has "Also to the Teams from the
label" (`deliver_to_teams`) and "Run the Teams' Escalation chains" (`run_team_chains`) on — those that each of its Teams
names, each whose Severity condition its Severity level meets. Several chains may run on one Alert Group at the same
time. A chain that the Route and a Team both name, or several Teams, runs once, with all of them as its sources. With
"Run the Teams' Escalation chains" off, the Teams' Destinations still receive the Alert Group, but their chains do not
start.

A chain runs only while the Alert Group is firing, and only Alerts and Commands start chains:

| Point | What happens to the chains |
|---|---|
| The Alert Group's first Publication; for one that a Storm summary stood for, its own Publication; for an outgoing webhook in events mode, its delivered `created` event | The chains that apply start at their first step |
| The Alert Group is created while no Destination reaches it, or only Broken ones | The chains that apply start at once |
| It becomes firing again: a Reopen into firing, Unacknowledge, Unsnooze, the end of a Snooze, Unresolve, a rise to critical that ends an acknowledgement or a Snooze, an auto-unacknowledge, its Owner disabled or deleted | The chains that apply start over at their first step |
| Its Severity level rises by its Alerts while it is firing and nobody owns it | The chains whose condition it now meets start; running chains go on |
| A Team joins it by a new Alert while it is firing and nobody owns it | That Team's chains start, where the Route runs the chains of Teams |
| A change of configuration: saving a Team, turning on "Also to the Teams from the label" or "Run the Teams' Escalation chains", naming a chain on a Route or a Team or changing its condition, changing the Minimum severity | No chain starts, and the running chains go on |
| Acknowledge, Snooze, Resolve | Every chain stops |

A chain whose condition the Alert Group no longer meets after its Severity level falls goes on. A chain that a change of
configuration would apply to starts at the next point that starts or starts over the chains, such as an Unacknowledge.
A held Alert Group that a raised Minimum severity releases is published Quietly and starts its ack timeout, but no
chain: that release is a change of configuration.

Each run keeps the steps its chain had when it started, until it stops or starts over: a change of the chain applies to
the runs that start after it. A step that Pages or sends, limited to Working hours, is skipped outside the Working hours
of the chain's owning Team, else the Organization's; a step whose Severity condition the Alert Group does not meet, a
step that Pages the On-call of a Schedule with a gap now, and each person a step names who has become a Viewer or been
disabled since the step was saved are skipped too. Each skip records the Quiet lifecycle event
`escalation_step_skipped` in the Timeline, of the kind `escalation`, with the chain, the step and the reason —
`outside_working_hours`, `severity`, `schedule_gap`, `viewer` or `disabled`, for a webhook step
`destination_deleted` or `webhook_events_required` ([Webhook steps](#webhook-steps)), and for a step "Page the whole
Team" that none of its chain's Destinations can mention `no_mention_possible` ([Where chains Page](#where-chains-page))
— and the chain goes on with its
next step at once. A wait delays the next step by its length on the clock of Muster, and "Repeat from step 3, up to 2 times" makes
steps 3 to the repeat three times in all, then goes on. Outgoing webhooks in the mode events receive
`escalation_step_skipped` with the chain, the step and the reason ([Events](../outgoing-webhooks/events.md)).

Steps are timer rows that any replica claims, and each step is a change of its Alert Group made by Muster, so a replica
that stops never stops a chain. `getAlertGroup` gives the runs of the chains since they last started in
`escalation_chains`: each with its chain, its sources, its state — `running`, `ran_out` once it passed its last step,
`stopped` by Acknowledge, Snooze or Resolve —, the step it is at and `next_step_at`, when its next step is due.
`listEscalationChainRuns` (`GET /api/v1/escalation-chains/{escalation_chain_id}/runs`) lists the recent runs of a chain,
newest first: each with its Alert Group, the steps it made and skipped with their reasons, and what stopped it — the
Command and the person who ran it — or that it ran out. An Alert Group whose chains started over has a run per start.

## Chains and the ack timeout

When the first chain of an Alert Group starts, its running ack timeout stops and sends no more notices: the steps take
their place. When the Alert Group becomes firing again, its ack timeout starts over only if none of its chains starts.
An Alert Group on which no chain applies keeps the ack timeout of its Route, as in L1
([Statuses and commands](statuses-and-commands.md#ack-timeouts-reminders-and-auto-unacknowledge)).

## Where chains Page

A chain Pages only in the Threads of its own Destinations, so that a step that Pages everyone in the chat never rings
another team's chat:

- a chain of the Route, in those of the Route's Destinations that have the Alert Group's Root message;
- a chain of a Team, in those of the Team's Destinations that have it, and, where none of them has, in those of the
  Route's Destinations;
- a chain that the Route and a Team both name, which runs once, in the Threads of both.

A Destination whose first Publication of the Alert Group is still on its way counts as one with the Root message: the
chains start with the first Publication in one Destination, and the Page waits there for the Root message to reply
under. One that a Storm holds does not.

Which Destinations a Route or a Team reaches is the rule of [Routing to Teams](routing-to-teams.md): the Severity
condition of each entry against the Alert Group's Severity level. A Destination is the Route's or a Team's when that
Route's or Team's own entry reached it, at a level the Alert Group had, and still names it. Destinations only grow, so
a Destination stays the Route's or the Team's after the level falls: when the critical Alerts resolve and the level
falls to warning, the chain of a Team still Pages in the Team's critical channel, which has the Root message, and does
not fall back to the Route's. A channel that several name belongs only to those whose entry reached it: when Team
devops names a shared channel for any level and Team payments names it for critical only, a warning Alert Group that
was never critical has its Root message there by devops' entry, so the chain of payments does not Page there, and its
`@here` never rings devops' channel. A Page is a Thread reply that delivery sends like any
other: Loud, due at once without the Thread batching window, never combined with another reply. It says which step
and which chain it comes from:

```text
Step 1: @ivan, you are needed
Chain of the Route Infrastructure · On-call of devops Main · normal
```

- **The people.** A step "Page the On-call of a Schedule" Pages the On-call of that moment, and a step "Page people"
  each person it names; each person by their [Notification rule](notification-rules.md) of the step's importance. The
  first Mentions of the rules that come due together are one reply per Destination naming them all. An important
  Page says "important, again in 5 min" when the person's rule Mentions them again — for a Page naming several people,
  only when every one of their rules does after the same time — and that later Mention repeats the first line with
  "· important, again". The rule stops when the Alert Group is acknowledged, snoozed or resolved, or
  when its chain starts over, and a Mention that comes due after that is not made.
- **A whole Team or everyone in the chat.** A step "Page the whole Team" writes the Team's Team mention for the
  Destination's Connection — the chain's owning Team when the step names none, the owning Team the chain had when its
  run started — or the word for everyone in the chat, `@here` for example. Where a Destination's Connection can
  mention neither, the step makes no reply there; where it can reply in none of the chain's Destinations, as when the
  only one is a Telegram channel, it records `escalation_step_skipped` with `no_mention_possible`.
- **The Timeline.** Each Page records the Loud lifecycle event `paged`, of the kind `escalation`, with its people,
  its chain and its step; outgoing webhooks in events mode receive it as every event, with its people as data
  ([Events](../outgoing-webhooks/events.md)).

A Page by Mention replies in Mattermost, Slack and Telegram Destinations and through Bridges; in Telegram the reply
is a comment under the channel post in the discussion group, as every Thread reply. An outgoing webhook in the template
mode takes the Page through its "reply in thread" request, and one without that request takes none. One in the events
mode has no Root message and takes no reply: it receives `paged` as an event, and only when the Page replies in
another Destination of the chain. A chain whose only Destinations are outgoing webhooks in the events mode records no
`paged`: a Page of people records `page_not_delivered` with `no_root_message`, and a step "Page the whole Team"
`escalation_step_skipped` with `no_mention_possible`. A step "Page the whole Team" never replies in Telegram, which
mentions no group and no one for everyone in the chat, and through a Bridge only when its bridge declares group
mentions, for a Team mention, or the word for everyone in its `everyone_mentions`.

## How a Page reaches a person

A Page names each person as a Mention only where they have an Account link in the Identity space of that Destination's
Connection ([Account links](../messengers/account-links.md)); a person without one is named in the line "Page not
delivered: @petr has no linked Mattermost account" instead, which mentions nobody.

| Where | A Page is | It reaches the person when |
|---|---|---|
| Mattermost | a Thread reply with `@username` | they have an Account link for the Connection |
| Slack | a Thread reply with `<@U…>` | they have an Account link for the Connection |
| Telegram | a comment in the discussion group with a `tg://user?id=` link | they have a Telegram Account link and are in the discussion group |
| Bridge | a Thread reply with a Mention marker | they have an Account link for the Connection and the bridge declares `native_mentions` |
| Outgoing webhook, events mode | the event `paged`, with the people as `mentions` data, when the Page replies in another Destination of the chain | the receiver acts on it |
| Outgoing webhook, template mode | the "reply in thread" request, when it has one | the receiver acts on it |
| Notification gateway | a call to the confirmed contact, from a later version | the gateway reaches them, whatever the messengers do |

In Telegram the reply reads "Step 1: Ivan, you are needed" with Ivan as a link that mentions him, and in a bridge
"Step 1: [@Ivan](muster-mention:m1), you are needed" with his entry in the envelope's `mentions`
([Rendering the envelope](../messengers/write-a-bridge.md#rendering-the-envelope)). A Mention in Telegram rings only a
member of the discussion group: the creator of the group, anonymous in it, who muted it is not rung. A bridge without
`native_mentions` shows a linked person's marker as their plain name, which rings nobody, so the Page is not delivered
to them through that Destination: when no other Destination of the chain reaches them, it records
`page_not_delivered` with `not_delivered`. An outgoing webhook takes every person of the Page as data, linked or not,
and the Team of a step "Page the whole Team" by its slug and name; the template mode gives them to the "reply in
thread" request as `.Mentions`, with `.Event` `paged` ([Template mode](../outgoing-webhooks/templates.md#mentions)).
One whose "reply in thread" request was removed after the Page was queued sends nothing, and that reply ends Not
delivered.

!!! warning "At night, only a Notification gateway is reliable"
    Whether a Mention in a Thread pushes a notification to a phone is still to be verified for Mattermost and Slack.
    Until it is, rely on a call through a Notification gateway for Pages at night.

## Undelivered Pages

A Page that reaches its person nowhere records the Quiet `page_not_delivered` with the person and the reason:

- `no_root_message`: none of the chain's Destinations has the Alert Group's Root message — such as a Route with a
  chain and no Destination, whose chains start when the Alert Group is created;
- `no_account_link`: the person has no Account link in the Identity space of any of the chain's Destinations;
- `not_delivered`: every Thread reply that would mention them ended Not delivered, was withheld while its
  Destination was [Broken](broken-destinations.md), or was never sent because its Root message — a first Publication,
  or one published again after it was deleted — ended Not delivered; a reply withheld while Broken is not sent after
  the Destination recovers.

A Page counts in `muster_pages_total{chain,kind,outcome}` once it ended: `delivered` once a delivered reply mentioned
one of its people — for a Team or everyone in the chat, once one of its replies was delivered — and `not_delivered`
otherwise; a Page whose chain was deleted before it ended is not counted ([Metrics](../reference/metrics.md)).

## Assignees

Until someone acknowledges, an Alert Group shows its **Assignees**: for each chain started on it, running or run out,
the people its latest step Paging people by name — the On-call of a Schedule or chosen people — reached, each with the
owning Team of that chain. A person is reached once a delivered Thread reply mentions them, as the table of
[How a Page reaches a person](#how-a-page-reaches-a-person) says for each messenger — a bridge's reply that names them
by the plain name reaches nobody; a person a Page did not reach (see [Undelivered Pages](#undelivered-pages)) never
becomes an Assignee.

How they change, for each chain on its own:

- a later step that reaches someone replaces the chain's Assignees, a repeated step included;
- another reply of the same step, and the later Mentions of that step's rules, add to them; the later Mentions of an
  earlier step's rules, and a reply of an earlier step that comes late, change nothing;
- a step that reaches none of its people leaves them as they were, and a step that Pages a whole Team or everyone in
  the chat changes nothing;
- a chain that runs out keeps its Assignees, so an Unclaimed Alert Group still shows whom it Paged;
- Acknowledge, Snooze and Resolve clear them all.

Each change records the Quiet `assignees_changed` in the Timeline, of the kind `escalation`, with the Assignees after
it, and edits the Root messages without notifying anyone. Nobody sets Assignees by hand, and being one gives no right
of its own: any Responder may acknowledge, and whoever does becomes the Owner.

Before an acknowledgement, item 7 of the Root message names them — "Assignees: @ivan (devops), @petr (payments)" —
each as a Mention where their account is linked in the Destination's Identity space, by name otherwise. After an
acknowledgement that stopped running chains it reads "Acknowledged by @ivan · Escalation chains stopped": the Owner is
named without a Team. The `layout` that a bridge receives names the Assignees in `assignees` with the Team in `team`,
and with the `ref` of their Mention entry in a new message to a bridge with `native_mentions`.

The API reads them in `assignees` of an Alert Group (`user`, `team`, `chain`, `since`), and
`GET /api/v1/alert-groups?assignee=me` lists the open Alert Groups where the calling User is an Assignee, by a User's
`public_id` for anyone else, and `GET /api/v1/alert-group-counts` counts them with the same parameter; a Service
account token gets `422` for `me`. Outgoing webhook events carry them in the
`assignees` of `alert_group`.

## Unclaimed

A chain has run out when it has passed its last step. An Alert Group with chains is Unclaimed once every chain started
on it has run out without an acknowledgement: it records `unclaimed`, and the Root message and the UI show "Nobody has
taken this", as after the last ack timeout notice. A chain that starts later on an Unclaimed Alert Group — for a Team
that joins, or a rise — makes it no longer Unclaimed while that chain runs, and it is Unclaimed again, recording
`unclaimed` again, once that chain has run out too. Acknowledge, Snooze and Resolve end Unclaimed.
