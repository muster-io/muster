# Whom a message mentions

Mention entries say who is mentioned. Each Destination has one list of them, each entry one target and the Loud events
it applies to, so that one Destination can mention `@here` for a new Alert Group and the On-call of the Alert Group's
Team on every Loud event. A Quiet message mentions nobody, and an edit never adds a Mention.

## Mention entries

The targets of an entry (`mention_entries[].target.kind`):

`everyone`
:   Everyone in the chat, with the word of the messenger: `channel`, `here`, `all` or `everyone`.

`user`
:   A person, by User. The message mentions them through their linked account in the Destination's messenger; a
    person without a linked account there is not mentioned.

`group`
:   A messenger group by its name, such as a Mattermost group; in Slack a user group with its id (`group_id`).

`team`
:   A [Team](teams.md), by its [Team mention](teams.md#team-mentions) for the Destination's Connection.

`teams_from_label`
:   Each Team of the Alert Group, by its Team mention.

`on_call_of_team`
:   The On-call of a Team: the person whom the Team's main [Schedule](schedules.md) has on Shift at the moment of the
    event.

`on_call_of_teams_from_label`
:   The On-call of each Team of the Alert Group.

The Loud events an entry applies to (`events`), all of them for a new entry:

`new_alert_group`
:   A new Alert Group; it also applies when an Alert Group joins the Destination Loudly (`destination_joined`).

`new_alerts`, `reopen`, `snooze_ended`, `rise_to_urgent`
:   New Alerts, a Reopen, the end of a Snooze and a rise to critical.

`ack_timeout`
:   The ack timeout notice.

`storm_started`
:   The first publication of a [Storm](#start-of-a-storm) summary.

A Destination without entries mentions nobody. Each person and group is mentioned once in a message, however many
entries name them. The rules for the Owner stay as they were: Reminders and auto-unacknowledge always mention the
Owner, a Takeover the previous Owner, and a Reopen into acknowledged only the Owner.

The API saves the entries in order with `If-Match`, and the Audit log shows the change in the diff of
`destination.updated`. A read gives each entry with `available`, whether the Connection can mention it now, and
`resolves_to`, what it mentions now: for the targets from the label, one item per Team.

```json
"mention_entries": [
  {"target": {"kind": "on_call_of_teams_from_label"}},
  {"target": {"kind": "everyone", "everyone": "here"}, "events": ["new_alert_group", "storm_started"]}
]
```

## What each messenger supports

A Destination takes only the targets its Connection can mention. Any other target is refused with `422` and the code
`mention_unsupported` at `/mention_entries/{i}/target`:

| Destination type | Everyone in the chat | Groups and Teams | People and the On-call |
|---|---|---|---|
| Mattermost | `channel`, `here`, `all` | Mattermost groups | linked people |
| Slack | `channel`, `here` | user groups, with the scope `usergroups:read` | linked people |
| Telegram | none | none | linked people |
| Bridge | the words the bridge declares | when the bridge declares group mentions | linked people |
| Outgoing webhook | every target, as data | every target, as data | every target, as data |

Slack has no `everyone`: a bot's `<!everyone>` mentions only in the workspace's general channel, where `<!channel>`
reaches the same people. A Slack Connection takes user groups and Teams once its check found the optional scope
`usergroups:read` ([Slack](../messengers/slack.md)). A bridge declares in its `info` the mentions of everyone it has
(`everyone_mentions`) and whether it has group mentions (`group_mentions`).

An entry stays when its Connection later loses an ability, such as a Slack app reinstalled without the scope: it reads
`available: false` with the reason in `unavailable_reason`, and mentions nobody. An entry whose User or Team was deleted
stays too, shows them as deleted and mentions nobody.

In Slack a user group or a Team mention is written as `<!subteam^…>` of the user group's id, which Slack shows as its
handle and which mentions its members. A bridge receives a group and a mention of everyone as entries of `mentions`
of their own kind ([What Muster posts](../messengers/bridge.md#what-muster-posts)).

An outgoing webhook receives the targets as data in `mentions` and `.Mentions`: a Team as `{"type": "team", "slug":
…, "name": …}`, one per Team of the Alert Group for "Team from the label", and the On-call as a `user`.

## Teams and the On-call

"Team payments" mentions the Team mention of payments for the Destination's Connection, such as `@payments_team` in
Mattermost. When the Team has no Team mention for that Connection, the entry mentions nobody: Muster never mentions the
On-call instead. To reach the On-call, add "On-call of a Team" or "On-call of the Team from the label".

The On-call is taken from the Team's main Schedule at the moment of the event, also when the message is sent later.
A Team without a main Schedule, or one with a gap at that moment, gives nobody. A Team mention that is set later, or an
On-call who links an account later, is mentioned from the next Loud event on.

When a Destination becomes a Destination of a Team, Muster adds the entry "Team {Team}" for every Loud event, unless
the Destination already has an entry naming that Team or has 100 entries, the most a Destination has. A Telegram
Destination gets none, since Telegram cannot mention Teams. An Admin may remove the entry; naming the Destination
again later adds it again only when it has none.

## Start of a Storm

The first publication of a Storm summary mentions the Destination's entries for `storm_started`. "Team from the label"
and "On-call of the Team from the label" take the Teams of the Alert Groups the Storm counts. The summary's later edits
mention nobody, and the summary starts no Escalation chain.

## Upgrading from 0.1

Version 0.1 kept one Mention setting per kind of Loud event: nobody, everyone in the chat, chosen Users or messenger
groups. The 0.2.0 migration turns them into entries with the same effect: each target chosen for a kind becomes one
entry for the kinds it was chosen for. An entry for `new_alert_group` also applies to `storm_started`, since a 0.1
Storm summary mentioned the targets of a new Alert Group. A Destination that mentioned nobody has no entries.

For example, "`@channel` for new Alert Groups, Alice for the ack timeout" becomes two entries: `channel` for
`new_alert_group` and `storm_started`, and Alice for `ack_timeout`.

Until 0.3.0 the API still returns `mentions`, the 0.1 form, computed from the entries that 0.1 could express on the
Destination's type, and still takes it in a request without `mention_entries`. Such a request replaces only those
entries and keeps the others, and a request that sends back the `mentions` it read changes nothing, so that 0.1 clients
keep working. The 0.1 form read back has the same Users and groups for each kind, but lists them in the order of the
first kind that lists each, since the entries are one list. 0.3.0 removes the field. The
[upgrade notes](../install/versions.md#upgrade-notes) describe the migration.
