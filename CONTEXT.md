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
One notification from Alertmanager that lists the current Alerts of one Alertmanager group. It is input, not an
Alert.
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

**Alerts view**:
The list of an Integration's current Alerts with their state, Route and Alert Group.

**Static labels**:
Labels an Integration adds to every Alert before routing, such as `cluster=prod`.

**Connection mode**:
How an Integration learns about Alerts from Alertmanager: webhook-only, pull or agent.

**Stored Snapshot**:
A Snapshot kept exactly as received, for debugging, previews and replay.
_Avoid_: Raw webhook, raw body

**Truncated Snapshot**:
A Snapshot from which Alertmanager left out some Alerts because of its `max_alerts` limit.

**Heartbeat**:
A regular signal from an Integration that proves the path from the alert source to Muster works.
_Avoid_: Ping, Watchdog

**Heartbeat lost**:
The state of an Integration whose Heartbeat has stopped arriving in time.

**Duplicate window**:
How close in time two Snapshots of the same Alertmanager group must arrive to count as one.

### Routing

**Route**:
A rule that picks Alerts by their labels and decides how their Alert Groups are handled and where they are delivered.
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
The organization's normalized severity of an Alert, such as critical or warning.
_Avoid_: Priority

**Urgent**:
An Alert Group that must not wait: its Route is marked urgent or its Severity level is critical.
_Avoid_: Critical (as a synonym), High urgency

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

**Reopen**:
Muster bringing a Resolved Alert Group back on its own because an Alert with the same Route and Group key values fired
shortly after it closed.
_Avoid_: Unresolve

**Command**:
A request from a person or automation to change an Alert Group, such as Acknowledge or Snooze.
_Avoid_: Operation

**Transport**:
The way a Command reaches Muster: the web UI, the API, Mattermost or Telegram, or Muster itself (`system`) for
transitions it starts on its own.
_Avoid_: Channel

### Handling an Alert Group

**Owner**:
The user who acknowledged an Alert Group and is handling it.
_Avoid_: Assignee, Acknowledger, Responder

**Takeover**:
An acknowledgement by another user, which moves ownership of the Alert Group to them.
_Avoid_: Reassign, Steal

**Ack timeout**:
How long a Firing Alert Group may wait for someone to take it before Muster calls for attention again.
_Avoid_: Escalation

**Unclaimed**:
An Alert Group that is still Firing after every ack timeout notice has been sent.
_Avoid_: Abandoned, Orphaned

**Reminder**:
A periodic nudge to the Owner that an Alert Group they acknowledged is still open.
_Avoid_: Ack timeout, Escalation

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

**Assignee** _(reserved for a later layer)_:
A person put on an Alert Group by a schedule or an escalation before anyone has acknowledged it.
_Avoid_: Owner

**Schedule** _(reserved for a later layer)_:
A plan of who is on call and when.
_Avoid_: Rota, Calendar

**Escalation** _(reserved for a later layer)_:
A sequence of steps that calls more people when an Alert Group stays Firing.
_Avoid_: Ack timeout, Reminder

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
The credentials Muster uses to talk to one messenger: a bot on a Mattermost server or a Telegram bot.
_Avoid_: Notification integration, Provider, Bot (on its own)

**Destination**:
A place where Alert Groups are delivered: a messenger channel reached through a Connection, or an outgoing webhook.
_Avoid_: Receiver, Recipient, Channel, Notification channel

**Outgoing webhook**:
A Destination that sends Alert Group events, or requests built from templates, to any HTTP endpoint.

**Test message**:
A message sent to a Destination from its settings to check how it looks, without touching any Alert Group.

**Signing secret**:
The secret of one Outgoing webhook that Muster uses to sign every request it sends there.
_Avoid_: Webhook key, Token

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

**Destination check**:
A test of whether Muster can still reach and post to a Destination, without sending a message.

**Delivery event**:
Something that happens while delivering to a Destination, such as a failed attempt or the Destination becoming Broken.
It is not a Lifecycle event.

**Broken**:
A Destination that keeps failing or has used up its retry budget. Muster probes it and sends the current state once it
recovers.
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
The scope in which one messenger account means one person for Account links: all of Telegram, or one Mattermost
Connection.

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
