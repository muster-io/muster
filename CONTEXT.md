# Muster

Muster is a self-hosted alert grouping and on-call service that sits after Alertmanager. It groups alerts into
problems people can act on, keeps one editable message per problem in messengers, and lets people acknowledge,
resolve and snooze them.

## Language

### Alerts and groups

**Alert**:
One firing problem reported by a source, identified by its full set of labels (its fingerprint), such as one pod,
node or domain. An Alert Group contains Alerts.
_Avoid_: Target, Sub-alert, Instance

**Snapshot**:
One notification from Alertmanager — Grafana's built-in one included, through a webhook contact point — that lists the
current Alerts of one Alertmanager group. It is input, not an Alert.
_Avoid_: Alert, Event

**Alert Group**:
A set of alerts that Muster treats as one problem. It has its own number and status and is shown as a single
message wherever it is delivered.
_Avoid_: Group (on its own, where it can be confused with other groups), Incident, Issue, Event

**Alertmanager group**:
The set of alerts that Alertmanager notifies about together under one `groupKey`. It is an input to Muster, not an
Alert Group.
_Avoid_: Alert Group, aggregation group

**Alertmanager route**:
A routing rule inside Alertmanager that decides which receiver gets an alert and how often it is repeated. It is not
a Route.
_Avoid_: Route (for this)

**Alertmanager receiver**:
The Alertmanager configuration that sends alerts to Muster. It is not a Destination.
_Avoid_: Receiver (on its own), Destination

**Instance labels**:
Labels that change when the process reporting an Alert restarts, such as `pod` or `instance`.

**Incident** _(reserved for a later layer)_:
A coordinated response to a problem that may span several Alert Groups.
_Avoid_: using it for a single Alert Group

### Inputs

**Integration**:
An input through which one alert source, such as the Alertmanager of one cluster, sends alerts to Muster. It has its
own token and its own heartbeat.
_Avoid_: Source, Input, Receiver

**Integration token**:
The secret an Integration uses to send alerts to Muster. It allows nothing else.
_Avoid_: API key

**Inbound webhook**:
An Integration that takes Alerts in the format of Alertmanager's API from senders without Alertmanager, such as
scripts, CI jobs or a rule evaluator. A request reports Alerts one by one, not as a Snapshot: each Alert stays open
for as long as its last report says, and then becomes Stale.
_Avoid_: Event, Generic webhook, Snapshot (for its requests)

**Alerts view**:
The list of an Integration's current Alerts with their state, Route and Alert Group.

**Static labels**:
Labels an Integration adds to every Alert before routing, such as `cluster=prod`.

**Connection mode**:
How an Integration learns about Alerts from Alertmanager: webhook-only, pull or agent. An Inbound webhook has none.

**Stored Snapshot**:
A Snapshot, or a request to an Inbound webhook, kept exactly as received, for debugging, previews and replay.
_Avoid_: Raw webhook, raw body

**Truncated Snapshot**:
A Snapshot from which Alertmanager left out some Alerts because of its `max_alerts` limit (Max alerts in Grafana).

**Heartbeat**:
A regular signal from an Integration that proves the path from the alert source to Muster works.
_Avoid_: Ping, Watchdog

**Heartbeat lost**:
The state of an Integration whose Heartbeat has stopped arriving in time.

**Duplicate window**:
How close in time two Snapshots of the same Alertmanager group must arrive to count as one.

### Routing

**Route**:
A rule that picks Alerts by their labels and decides how their Alert Groups are handled and where they are delivered:
to its own Destinations and, where the Route asks for it, to those of the Teams its Alerts belong to. A Route may
belong to a Team, which owns it but adds no delivery.
_Avoid_: Policy, Rule, Direction

**Default route**:
The Route that takes every Alert no other Route took. It always exists.
_Avoid_: Fallback, Catch-all

**Matcher**:
A condition on one label that a Route uses to pick Alerts.
_Avoid_: Filter, Selector

**Group key**:
The labels whose values split a Route's Alerts into Alert Groups.
_Avoid_: Group by, Fingerprint

**Severity level**:
The organization's normalized severity of an Alert, such as critical or warning. An Alert Group's Severity level is
the highest of its firing Alerts, raised to its Route's Minimum severity.
_Avoid_: Priority

**Minimum severity**:
The lowest Severity level a Route gives its Alert Groups, such as critical on a Route whose every Alert needs someone
at once. It raises the Severity level of the Alert Group, never that of its Alerts.
_Avoid_: Urgent mark, Severity override

**Urgent**:
An Alert Group whose Severity level, with its Route's Minimum severity, is critical; it must not wait.
_Avoid_: High urgency

**Route profile**:
A starting set of Route settings for a kind of traffic, such as On-call or Informational.
_Avoid_: Route type

### Status of an Alert Group

**Firing**:
The Alert Group has a live problem and nobody has taken it.

**Acknowledged**:
A person has taken the Alert Group and is handling it.
_Avoid_: Assigned, In progress

**Resolved**:
The Alert Group is closed, either by a person or because its alerts are gone.
_Avoid_: Closed, Done

**Snoozed**:
The Alert Group is set aside until a chosen time, or with no end; Muster stays quiet about it meanwhile.
_Avoid_: Silenced, Muted

**Alertmanager silence**:
A silence created in Alertmanager. It is not a Snooze and Muster does not create one on its own.
_Avoid_: Silence (on its own, for anything in Muster)

### Actions on an Alert Group

**Acknowledge** / **Unacknowledge** (short: Ack / Unack):
Take an Alert Group and become its Owner / give it back so that it is Firing again.

**Resolve** / **Unresolve**:
Close an Alert Group by hand / bring back an Alert Group that a person closed by mistake.
_Avoid_: Reopen (for the manual action), Firing (as an action name)

**Snooze** / **Unsnooze**:
Set an Alert Group aside until a chosen time / end that early.
_Avoid_: Silence, Mute

**Snooze preset**:
A Snooze that ends at the next start of the working day ("Until tomorrow 09:00") or at the start of the working day on
the first day of the week ("Until Monday 09:00"), in the person's time zone; the Organization sets both.
_Avoid_: Calendar snooze, Snooze template

**Attach** / **Detach**:
Put an Alert Group under another one that stands for the same problem, so that it follows that Alert Group's status /
let it live on its own again.
_Avoid_: Merge, Link

**Reopen**:
Muster bringing a Resolved Alert Group back on its own because an Alert with the same Route and Group key values fired
shortly after it closed.
_Avoid_: Unresolve

**Command**:
A request from a person or automation to change an Alert Group, such as Acknowledge or Snooze.
_Avoid_: Operation

**Transport**:
The way a Command reaches Muster: the web UI, the API, the MCP server, Mattermost, Telegram, Slack, a Bridge or a
Notification gateway, or Muster itself (`system`) for transitions it starts on its own.
_Avoid_: Channel

**Text command**:
A Command written as a reply in a Thread, such as `ack` or `snooze 4h`, for messengers whose bridge has no buttons.
Only Bridge Destinations take them.
_Avoid_: Slash command, Chat command

### Handling an Alert Group

**Owner**:
The user who acknowledged an Alert Group and is handling it.
_Avoid_: Assignee, Acknowledger, Responder

**Takeover**:
An acknowledgement by another user, which moves ownership of the Alert Group to them.
_Avoid_: Reassign, Steal

**Ack timeout**:
How long a Firing Alert Group may wait for someone to take it before Muster calls for attention again. Where an
Escalation chain runs, the chain takes its place.
_Avoid_: Escalation

**Unclaimed**:
An Alert Group that is still Firing after every ack timeout notice has been sent, or after every Escalation chain
started on it has run out.
_Avoid_: Abandoned, Orphaned

**Reminder**:
A periodic nudge to the Owner that an Alert Group they acknowledged is still open.
_Avoid_: Ack timeout, Escalation chain

**Auto-unacknowledge**:
Muster removing an acknowledgement after the Owner leaves Reminders unanswered, where the Route allows it.
_Avoid_: Ack expiry

**Timeline**:
The ordered record of everything that happened to an Alert Group.
_Avoid_: History, Activity

**Lifecycle event**:
Something that happens to an Alert Group and is recorded in its Timeline, such as being acknowledged or reopened. Each
one is Loud or Quiet and may carry Mentions.
_Avoid_: Notification, Activity

**Note**:
Text a person adds to a Timeline.
_Avoid_: Comment

**Assignees**:
The people whom the Escalation chains running on an Alert Group have Paged last by name, one or more for each chain,
while nobody has acknowledged it. Acknowledge, Snooze and Resolve clear them; there may be several Assignees, but only
one Owner.
_Avoid_: Responder, Assigned

### Teams and on-call

**Team**:
A set of Users that owns Routes, Destinations, Schedules and Escalation chains, and to which Alerts belong by their
Team label or its Team matchers. It gives no access of its own: Roles stay global. A User may be in several Teams,
added by hand or through the groups of the identity provider; a Team may have nobody in it and serve only as a name
for Alerts, with its own Destinations and Team mention.
_Avoid_: Squad, Group

**Team label**:
The label whose value names the Team an Alert belongs to, by the Team's slug. The Organization chooses the label,
`team` by default.
_Avoid_: Team tag

**Team matcher**:
A Matcher of a Team that makes an Alert belong to the Team without the Team label, for Alerts whose rules do not set
it.
_Avoid_: Team filter, Team selector

**Team mention**:
The Mention that stands for a whole Team in the messenger of one Connection, such as a Mattermost group or a Slack
user group. Mentioning a Team that has none for that Connection mentions nobody; it never falls back to the Team's
On-call.
_Avoid_: Team tag

**Working hours**:
The days and hours, in a time zone, that count as working time: the Organization's, or a Team's own. An Escalation
step may be limited to the Working hours of the Team that owns its chain.
_Avoid_: Business hours

**Schedule**:
A Team's plan of who is On-call and when: Rotation layers with Overrides on top. A Team may have several; its main
Schedule names the Team's On-call. A time in which nobody has a Shift is a gap.
_Avoid_: Rota, Calendar

**Rotation layer**:
One layer of a Schedule in which people take Shifts in turn, with a Shift length, a handover time and optional limits
such as weekdays 9–18. Where a higher Rotation layer has a Shift, it overrides those below.
_Avoid_: Rotation (on its own), Layer (on its own)

**Shift**:
A span of time during which one person is On-call in a Schedule.
_Avoid_: Duty

**Override**:
A one-off change of a Schedule that puts another person On-call for a span of time, on top of its Rotation layers.
_Avoid_: Swap

**On-call**:
The person who has the current Shift of a Schedule. A Team's On-call is the one of its main Schedule.
_Avoid_: Responder (that is a Role)

### Paging

**Escalation chain**:
An ordered list of Escalation steps that Pages more and more people while an Alert Group stays Firing. It is attached
to a Route or to a Team, optionally for some Severity levels; several may run on one Alert Group, and acknowledging it
stops them all.
_Avoid_: Escalation policy, Escalation (on its own)

**Escalation step**:
One step of an Escalation chain: Page the On-call of a Schedule, chosen people, a whole Team or everyone in the chat;
call a person through a Notification gateway; wait; send an event to an outgoing webhook; or repeat earlier steps. A
step may be limited to Working hours and to some Severity levels.
_Avoid_: Escalation rule

**Page**:
A call for attention made by an Escalation step: a Mention in the Alert Group's Thread, or a call through a
Notification gateway.
_Avoid_: Alert (as a verb)

**Notification rules**:
A person's own two sequences, normal and important, that say how to Page them, such as a Mention in the Thread and a
call through a Notification gateway five minutes later. An Escalation step Pages a person by one of the two.
_Avoid_: Notification policy, Contact method

**Notification gateway**:
A Bridge in the role `gateway` that Pages people outside messengers, by phone call or SMS for example, at the contact
each person confirmed in their profile, and reports when one of them acknowledges. It is never a Destination.
_Avoid_: Notifier, Pager

### When alerts stop or return

**Reopen window**:
How long after Muster resolves an Alert Group an Alert with the same Route and Group key values reopens it instead of
starting a new one.

**Grace period**:
How long after a person resolves an Alert Group its still-firing Alerts are tolerated before a new Alert Group starts.

**Gone**:
An Alert that Alertmanager has stopped listing in its Snapshots. Muster resolves it.
_Avoid_: Disappeared

**Stale**:
An Alert that Muster has heard nothing about for too long. Muster resolves it.
_Avoid_: Expired

**Continuation**:
An open Alert that is reported again with a new start time but without a resolve in between. Muster keeps it as the
same Alert.
_Avoid_: Reopen, Refire

**Replacement**:
A new Alert that differs from a firing one only in Instance labels, such as the same problem reported by a restarted
pod.
_Avoid_: Twin, Duplicate

### Delivery

**Connection**:
The credentials Muster uses to talk to one messenger: a bot on a Mattermost server, a Telegram bot, the bot of a Slack
app in one workspace, or a Bridge.
_Avoid_: Notification integration, Provider, Bot (on its own)

**Bridge**:
A Connection through which Muster reaches a messenger it has no native support for, or a Notification gateway: Muster
talks to a separate service, the bridge, over the Bridge protocol, and the bridge talks to the messenger or places the
calls. Anyone can write a bridge for their messenger or gateway.
_Avoid_: Plugin, Adapter, Custom messenger, Integration

**Bridge protocol**:
The HTTP protocol between Muster and a bridge, specified in `api/bridge.yaml`: what a bridge offers, how Muster posts,
edits and replies through it, and how it reports presses, Thread messages, Account link requests and acknowledgements
from a Notification gateway back.
_Avoid_: Bridge API, Plugin API

**Destination**:
A place where Alert Groups are delivered: a messenger channel reached through a Connection, or an outgoing webhook.
Routes and Teams name the Destinations they deliver to, each optionally for some Severity levels.
_Avoid_: Receiver, Recipient, Channel, Notification channel

**Outgoing webhook**:
A Destination that sends Alert Group events, or requests built from templates, to any HTTP endpoint.

**Test message**:
A message sent to a Destination from its settings to check how it looks, without touching any Alert Group.

**Signing secret**:
The secret of one Outgoing webhook that Muster uses to sign every request it sends there. It is not a Slack signing
secret.
_Avoid_: Webhook key, Token

**Slack signing secret**:
The secret of a Slack app with which Slack signs the button presses it sends to a Slack Connection's callback address;
Muster checks the signature and never signs anything with it.
_Avoid_: Signing secret (on its own)

**Publication**:
The first delivery of an Alert Group to a Destination, which creates its Root message.

**Desired state**:
What a Root message should look like right now. Muster changes the actual message until it matches.

**Not delivered**:
The final state of a delivery that retrying cannot fix, such as an unknown response or a failed template.

**Thread batching window**:
How long Muster collects new thread messages for an Alert Group before sending them as one.
_Avoid_: Debounce

**Fallback template**:
The built-in message template Muster uses when a configured template fails.

**Root message**:
The one editable message that shows an Alert Group in a Destination.
_Avoid_: Post, Card

**Line template**:
A Route template that renders the line shown for each Alert in a Root message.

**Thread**:
The replies under a Root message.
_Avoid_: Comments

**Loud** / **Quiet**:
Whether a delivery should make people's devices ring, or only update what they see.
_Avoid_: Silent, Ringing

**Mention**:
A deliberate @-reference that Muster adds to a message.
_Avoid_: Ping

**Storm**:
A burst of new Alert Groups too large to post one by one.
_Avoid_: Flood

**Storm summary**:
The single message that stands for the Alert Groups of a Storm.

**Maintenance window**:
A span of time, current or planned, during which Muster delivers nothing about the Alert Groups of an Integration, a
Route or a Team and runs no Escalation chains for them, while ingestion and grouping go on. Alert Groups still firing
when it ends are published with a mark. A Team's window holds an Alert Group only while every Team of its Alerts is in
a window.
_Avoid_: Silence, Mute, Downtime

**Destination check**:
A test of whether Muster can still reach and post to a Destination, without sending a message.

**Delivery event**:
Something that happens while delivering to a Destination, such as a failed attempt or the Destination becoming Broken.
It is not a Lifecycle event.

**Request log**:
The recent HTTP requests Muster sent to a messenger, a bridge or an outgoing webhook, and those that reached a
Connection's addresses, each with its answer and with secrets masked, kept briefly for troubleshooting. It is neither
the Audit log nor the Delivery events.
_Avoid_: Request history, Traffic log, Access log

**Broken**:
A Destination that keeps failing or has used up its retry budget. Muster probes it and sends the current state once it
recovers. A Notification gateway can be Broken too.
_Avoid_: Down, Failed

### Access

**Organization**:
The top-level owner of all users, routes and data in Muster.
_Avoid_: Tenant, Account

**User**:
A person who can sign in to Muster and act on Alert Groups.
_Avoid_: Account, Member

**Role**:
A named set of Permissions given to a User: Admin, Responder or Viewer.
_Avoid_: Group

**Permission**:
One thing a Role allows, such as acknowledging Alert Groups or editing Routes.

**Personal access token**:
A token that lets automation act as a User.
_Avoid_: API key

**Service account**:
A non-person identity for automation, such as Terraform, with its own Role and tokens.
_Avoid_: Bot, System user

**Account link**:
The tie between a User and their account in a messenger.
_Avoid_: Binding, Pairing, Connection

**Identity space**:
The scope in which one messenger account means one person for Account links: all of Telegram, one Mattermost
Connection, one Slack Connection, or one Bridge.

**Secret**:
A setting Muster stores encrypted and never shows again once saved, such as a bot token or an outgoing webhook header.

**Audit log**:
The record of who changed or did what in Muster.
_Avoid_: History, Timeline

### Muster itself

**Outgoing heartbeat**:
Muster's own regular signal to an external dead man's switch, proving that Muster itself is alive.
_Avoid_: Heartbeat (on its own)

**Internal alert**:
An Alert that Muster raises about its own problems through its built-in "Muster" Integration.
_Avoid_: System alert, Self-alert

**Outbound address policy**:
The Organization's rule for which network addresses Muster may call, standard or strict.
_Avoid_: SSRF settings, Firewall

**Leader**:
The one Muster replica that currently does the work that must run only once at a time.
_Avoid_: Master, Primary

**Master key**:
A secret from the environment that protects stored secrets.
_Avoid_: Encryption key (on its own)

**Keyring**:
All master keys Muster knows; one of them is active.

**Link rule**:
A rule that turns an Alert Group or a label value into a link to another tool.

**Lookup table**:
A table of values that Link rules use to build links, such as the Grafana address of each environment.
