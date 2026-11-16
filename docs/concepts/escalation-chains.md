# Escalation chains

An **Escalation chain** Pages more and more people while an Alert Group stays firing. Its steps Page the On-call of a
Schedule, chosen people or a whole Team, call through a [Notification gateway](../messengers/notification-gateways.md),
wait, send to an outgoing webhook and repeat. Routes and Teams name their chains. Chains are managed through the API under
`/api/v1/escalation-chains`, listed in the [API reference](../reference/api.md).

In this version a chain is configuration only: it is saved, checked and named on Routes and Teams, and runs nothing
yet. Where no chain runs, the ack timeout of a Route keeps working as before.

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
