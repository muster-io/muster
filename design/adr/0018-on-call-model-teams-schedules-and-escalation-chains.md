# 0018. On-call model: Teams, Schedules and Escalation chains

- Status: Accepted
- Date: 2026-10-26

## Context

In L1 a Route delivers its Alert Groups to its Destinations, each Destination mentions whom it is set to mention on Loud
events, and an Alert Group that nobody takes gets up to three ack timeout notices. Muster does not know who is on call,
cannot call further people when nobody reacts, and has no notion of a team. A common setup shows what that costs: five
product teams, each with one channel for critical alerts and one for the rest, and two operations channels that receive
every technical alert, whichever team it concerns, while business alerts go only to their team. With first-match Routes
(ADR-0003) that takes four Routes per team, and one problem becomes one Alert Group per Route, each acknowledged on its
own (journal D551).

The model has to fit what earlier decisions fixed:

- An Alert Group belongs to one Route, chosen by first match, and has one status and one Owner (ADR-0003, ADR-0004).
- Messenger messages change only through delivery: the delivery worker reconciles Root messages and sends Thread
  replies, and the interactive path answers people who wait (ADR-0005). A direct message that wakes a person is neither,
  and nobody waits for it.
- Roles are global and fixed — Admin, Responder and Viewer; custom Roles come later.
- The default Group key is `alertname`, `severity`, `cluster`, without any team label, so one Alert Group often holds
  Alerts of several teams.
- Messengers differ in what they can mention: Mattermost has groups, Slack has user groups behind a scope of their own,
  Telegram has neither, and a bridge has what it declares.
- A Mention in a Thread wakes a person only if the messenger pushes it to their phone; whether Mattermost, Slack and
  Telegram do that for Thread replies is to be verified before the stories (journal D532). Muster itself cannot place a
  phone call or send an SMS.

## Decision

**Teams own; they do not isolate.** A Team is a unit of ownership: Routes, Destinations and Escalation chains may belong
to one, every Schedule belongs to one, and the UI filters by "my Teams". A Team grants no access: Roles stay global, and
isolation and Roles within a Team wait for custom Roles (journal D464). Ownership is otherwise optional, and the objects
of L1 keep working without a Team. A Team may exist with nobody in it and no Schedule — only a name for its Alerts, its
own Destinations and its Team mention — which is enough for a team that wants its own channels but keeps no Schedule in
Muster (journal D476). Two relations are kept apart: the Team that owns an object, which only organizes and filters, and
the Destinations of a Team, which receive the Alert Groups of its Alerts (journal D525). A Route's own Team therefore
never adds delivery.

**Who is in a Team.** A User may be in several Teams. An Admin adds people by hand in the UI or through the API; a Team
may also name groups of the identity provider, and its membership then follows them at every sign-in and token refresh,
with the same matching as the mapping of groups to Roles, while sync never removes the people added by hand (journal
D465). Every entry of either mapping, to a Role or to a Team, may name the claim it reads, the Organization's groups
claim by default, so that any identity provider works without a preset; the UI suggests the claims the provider lists in
its discovery document, and the highest Role found wins (journal D466). A claim is read from the ID token and then from
userinfo; with a provider that gives no refresh token, membership changes only at sign-in (journal D519). An entry
`a/b/*` matches `a/b` and every group below it, at `/` boundaries only, so `a/bx` does not match; every other entry
matches exactly, and there are no globs or regular expressions (journal D467).

**Which Teams an Alert belongs to.** An Alert belongs to a Team when the value of its Team label — an Organization
setting, `team` by default — is the Team's slug, or when one of the Team's Team matchers picks it. Team matchers use the
syntax of Route Matchers on any label, for Alerts whose rules set no team; the Timeline and the filters show by which of
the two an Alert reached its Team (journal D538). A value of the Team label that names no Team adds nothing: the Alert
Group goes to its Route's Destinations, its Timeline records the unknown value, and the Teams page lists the values seen
without a Team (journal D468, D525). Where teams share a namespace, as on shared staging clusters, matchers on other
labels or a team label on the workloads tell them apart, and a Route that picks those clusters first keeps their Alert
Groups away from Escalation chains (journal D539).

**Delivery to the Teams of an Alert Group.** Routes stay first-match and keep their Alert Groups, statuses and timers
(ADR-0003). A Route may also deliver to the Teams of its Alerts, a Route setting that is off by default. An Alert Group
of such a Route goes to the Route's Destinations together with the Destinations of every Team its Alerts belong to; a
Destination named more than once gets one Root message (journal D468, D532). Each Destination of a Route or a Team may
be limited to some Severity levels, such as critical only or everything below critical, checked against the Alert
Group's Severity level; where a Destination is named more than once, its conditions combine with OR (journal D468,
D532). The Teams of an Alert Group grow with its Alerts: an Alert of another Team adds that Team's Destinations and
starts that Team's Escalation chains, just as a rise in Severity level adds the Destinations whose condition it now
meets. Destinations are only ever added: a Severity level that falls again, or a Team whose Alerts resolved, leaves its
Root message in place and keeps it current (journal D469, D510). Every Destination shows the same Alert Group with one
status and one list of Alerts: a team sees the Alerts of other teams that share its Alert Group, and an Alert Group that
is already acknowledged reaches a joining Team acknowledged and starts no chain there. Teams that want to handle such
problems apart add the Team label to the Group key. The documentation and the hints of the Route settings explain this
with a worked example and say that the feature is optional: many installations need nothing but two operations channels
(journal D510).

**Joining a Destination later.** When an Alert Group reaches a Destination after its first Publication elsewhere —
because its Severity level rose into the Destination's condition or because a Team joined — the Lifecycle event
`destination_joined` publishes it there: Loudly, with the Mentions of a new Alert Group, if it is firing; Quietly if it
is acknowledged or snoozed; and only once a Maintenance window that holds it has ended. An Admin who changes a Severity
condition adds Destinations the way a Destination added to a Route does, Quietly, and removes none (ADR-0005; journal
D512).

**Mentions of Teams.** A Destination's Mentions become one list of whom to mention — everyone in the chat, a person, a
messenger group, a Team or the On-call of a Team — each entry with the Loud events it applies to, all of them by
default. The Team in an entry is either named or taken from the Team label, so that one Destination serves the Alert
Groups of every team (journal D477, D509). The list offers only what the Destination's Connection can mention and shows
the rest as unavailable, with the reason (journal D540). A Team is mentioned by its Team mention for that Connection — a
Mattermost group, a Slack user group once the Connection may read them, or a group mention that a bridge declares in
`info` — and where it has none, nobody is mentioned and the message alone is enough, as in L1. A Team mention never
falls back to the On-call; the On-call of a Team is mentioned only where an entry names it (journal D509, D516).
Telegram has no group mentions. A Team's own Destinations mention the Team by default, which rings only where a Team
mention is set. An entry may also apply to `storm_started`, the first publication of a Storm summary; a Storm summary
starts no Escalation chain (journal D533).

**Schedules.** A Schedule belongs to a Team and has Rotation layers — the people of a Rotation layer take Shifts in
turn, a day, a week or a length of its own, with a handover time, optional limits such as "weekdays 9–18", and the
Schedule's time zone, and a higher Rotation layer overrides the lower ones wherever it has a Shift — with Overrides on
top, set in the UI or through the API, and an iCal feed of each person's own Shifts (journal D470). A weekly 24×7
Rotation layer under a "working hours 11–19" Rotation layer with other people in it is one Schedule (journal D526). A
Team may have several Schedules, such as a first and a second line; one of them is its main Schedule, which names the
Team's On-call, and an Escalation step may Page the On-call of any Schedule (journal D526). Schedules are not imported
from iCal.

**Gaps.** A time in which no Rotation layer has a Shift is a gap. A step that would Page the On-call of a Schedule with
nobody on Shift is skipped with a Timeline entry, and the chain goes on with its next step. The Schedule page shows the
gaps of the next two weeks, and an Internal alert reports a coming gap (journal D482, D525). The Shifts of people who
may not be Paged count as gaps too: a Viewer, a person no longer in the Team — for example after the groups of the
identity provider changed — and a disabled User; the Schedule warns about each (journal D514).

**Overrides and permissions.** Only an Admin changes the structure: Teams, Rotation layers, Escalation chains and
Routes. An Override needs the Permission `schedule-overrides:write`, which Responders and Admins have, and a
precondition: the caller is in the Team or is an Admin, and both people of the Override are in the Team and are not
Viewers. Overrides need no approval and send no message; the Audit log records them, and the UI, its "on call now" view
and the iCal feed show them (journal D483, D514, D534). Shifts send no messages either: who is on call is shown in the
UI and the iCal feed, and no messenger command answers it (journal D504, D515).

**Escalation chains.** An Escalation chain is an object of its own, attached to Routes and to Teams; a Team's chains run
on the Alert Groups delivered to that Team. Each attachment may be limited to some Severity levels. Several chains may
run on one Alert Group at the same time, and any Acknowledge stops them all (journal D471). Its steps are (journal
D472): Page the On-call of a Schedule; Page chosen people; Page a whole Team or everyone in the chat; call a person
through a Notification gateway (journal D540, D542); wait N minutes; send the event `escalation_webhook` to an outgoing
webhook Destination named in the step, which receives only that event, whether or not the Alert Group is delivered there
(journal D513); and repeat from step K up to N times. A step may be limited to Working hours and to some Severity
levels. Working hours are the Organization's — days, hours and a time zone — unless the Team that owns the chain sets
its own; holidays come later (journal D525, D541). The step-by-step test of a Destination also plays a chain in
accelerated time and shows whom each step would Page and where, without calling anyone (journal D548).

**When a chain runs.** A chain runs where an ack timeout runs, with the same points of start, restart and stop
(ADR-0004): it starts at the Alert Group's Publication, starts over whenever the Alert Group becomes firing again — a
Reopen into firing, Unacknowledge, Unsnooze, the end of a Snooze, Unresolve, a rise to Urgent that ends an
acknowledgement or a Snooze, an auto-unacknowledge — and stops on Acknowledge, Snooze and Resolve (journal D507). An
Alert Group with no Destination that can publish it — none at all, or only Broken ones — starts its chains when it is
created (journal D528). In a Storm, the chain of an Alert Group waits for that Alert Group's own Publication, and a
Maintenance window holds chains. A rise in Severity level of a firing Alert Group restarts no running chain; it only
starts the chains whose condition it now meets. When the first chain of an Alert Group starts, its running ack timeout
stops and sends no more notices: the steps take their place, while an Alert Group without a chain keeps the ack timeout
of L1 (journal D487, D535). The Alert Group is Unclaimed once every chain started on it has run out without an
acknowledgement; a chain that starts later, for a Team that joins, starts as usual (journal D507, D535).

**Where a chain Pages.** A Route's chain writes in the Threads of the Route's Destinations; a Team's chain writes in the
Threads of that Team's Destinations or, where the Alert Group is delivered to none of them, in those of the Route's
Destinations. A person is mentioned only where they have an Account link in the Identity space of that Destination's
Connection, and a Team's step that Pages everyone in the chat never rings another team's chat (journal D529). A Page by
Mention is a Thread reply that delivery sends like any other (ADR-0005). A Mention step with no Root message to reply
to, or whose Thread reply ends Not delivered or is held back, records in the Timeline that the Page was not delivered
and does not make the person an Assignee. Steps through a Notification gateway do not depend on a Root message and keep
working while a messenger is unreachable. A Route with a chain and no Destinations carries a warning that only a
Notification gateway can reach anyone (journal D528).

**Pages, not direct messages.** Muster Pages a person only by a Mention in the Alert Group's Thread or through a
Notification gateway, never by a direct message; direct messages stay for Account links and similar system messages
(journal D503). The Schedule warns that its On-call is unreachable where the Destinations its Team's chains write to
cannot mention them: they have no Account link in the Identity space of those Connections, a bridge there has no native
mentions, an outgoing webhook there has no reply in a Thread, or they are not in the discussion group of a Telegram
channel (journal D503, D528). Until the facts about push for Mentions in Threads are verified, the documentation says
that at night only a Notification gateway is reliable (journal D532).

**Notification rules.** How a person is Paged is up to their Notification rules: a normal and an important sequence of
Mentions in the Thread and calls through a Notification gateway, with waits between them; a step that Pages the On-call
or chosen people Pages each of them by one of the two. By default the normal rule is a Mention in the Thread and the
important rule a Mention repeated after five minutes; the Organization sets the defaults, each person changes their own
rules in the profile, and an Admin sees and may change them (journal D473, D490, D503). Rules and steps that call
through a Notification gateway are offered only while a Bridge in the role `gateway` is connected; otherwise the UI says
that a gateway is needed and links to the documentation (journal D540). Calls through a gateway to the same person
within a short window are combined into one call about several Alert Groups; Mentions are not combined, because every
Alert Group has a Thread of its own (journal D507, D529). The push of a mobile app can later become one more way to Page
in the same rules (journal D472, D473).

**Notification gateways.** Calls and SMS come from bridges in the role `gateway`, called Notification gateways
(ADR-0017; journal D505, D542). A person adds their contact for a gateway in the profile, from their own web session,
and confirms it with a code that Muster sends through that gateway; an Admin sees and may remove the contact but never
sets one (ADR-0013; journal D485, D517). A Page through a gateway carries signed action data, as a button does. When the
person acknowledges in the call or by a reply, the gateway reports it, and Muster acknowledges as that person with the
Transport `gateway`, but only while nobody owns the Alert Group: a firing or snoozed Alert Group becomes acknowledged,
an acknowledged one stays as it is and the gateway tells the person who has it, and a Takeover is only ever an explicit
action in a messenger or the UI (journal D518). A call that nobody answers lets the Notification rules go on. Calls go
out through a queue of their own with retries, outside reconciliation (ADR-0005); a gateway that keeps failing becomes
Broken and raises an Internal alert (journal D532).

**Assignees.** Until someone acknowledges, an Alert Group shows its Assignees: the people each running chain Paged last
by name, one or more per chain — steps that Page a whole Team or everyone in the chat change nothing. The Root message
and the UI list them with their Teams, and the UI filters by the Alert Groups where one is an Assignee (journal D474,
D508). A change of Assignees is a Timeline entry; Acknowledge, Snooze and Resolve clear them, and they change only
through system transitions of the dispatcher (ADR-0016). Nobody adds Assignees by hand, and any Responder may
acknowledge. Assignees are written as Mentions where their account is linked, like the Owner, accepting that a new Quiet
message may notify them (journal D521, D535).

**One status, one acknowledgement.** One Acknowledge acknowledges the Alert Group everywhere and stops every chain on
it; the Root message names the Owner with their Team. The buttons offer the same actions for a status in every
Destination, and which of them a Destination shows is a setting of that Destination (journal D478, D508, D535). An
acknowledgement, Owner and status per Team would change the Alert Group and its state machine (ADR-0004) and is planned
for L3 (journal D508, D535).

**Maintenance windows.** A Maintenance window holds an Integration, a Route or a Team from now until a time, or over a
planned span such as Saturday 02:00–04:00; people open windows in the UI or through the API, for example from a CI
pipeline before a deploy (journal D493). While a window holds an Alert Group, ingestion and grouping go on and its
status changes as usual, but nothing is delivered for it and its timers and chains wait; when the window ends, the Alert
Groups still firing are published with a mark, as at the end of a Storm, and those resolved meanwhile are not. Whether
Urgent Alert Groups pass through is a setting of the window. A window of an Integration or a Route holds all their Alert
Groups. A window of a Team holds an Alert Group — in all its Destinations, the operations channels included — only while
every Team of its Alerts is in a window, the Route's own Team not counted, so one team's deploy never holds back an
Alert Group that another team shares; a Team that joins an Alert Group during its window never stops the chains already
running there (journal D523, D530). Opening and closing a window is recorded in the Audit log, and the Permission
`maintenance:write` belongs to Admins and Responders, so a CI pipeline uses a Service account with the Role Responder
(journal D535). A window is one row with a start and an end, which any replica handles like a timer (ADR-0007; journal
D535).

**Changes to an Alert Group.** Everything these features change on an Alert Group — its Assignees, the Pages, skipped
steps and undelivered Pages in its Timeline, a Team that joins, the start and end of a window — goes through the
dispatcher as a system transition (ADR-0016). The L2 PRD lists their Lifecycle events with their Loud and Quiet rules in
one table (journal D513).

## Consequences

- One Route can serve many teams: the operations channels and each team's own channels show the same Alert Group with
  one status, so no team acknowledges a problem that another team already has.
- A team sees the Alerts of other teams in a shared Alert Group; teams that do not want that put the Team label into the
  Group key.
- Paging by Mention reaches people only as well as their messenger pushes Mentions in Threads. A Notification gateway is
  the only path that does not depend on a messenger, and installations that need to reach people at night need one.
- Muster ships no gateway for calls or SMS: a team writes or runs a bridge for its provider, with the bridge libraries.
- Pages that a Broken Destination could not deliver are recorded and not replayed after recovery; the Alert Group's
  other Destinations and its gateway steps still reach people.
- Paging depends on Account links in every Identity space the Destinations use; the Schedule shows whom a chain cannot
  reach.
- Escalation chains replace ack timeout notices only where they run, so installations without Schedules keep the
  behaviour of L1.
- Permissions stay global: a Responder of one Team may acknowledge another Team's Alert Groups, and only Overrides check
  who is in a Team.
- A shared Alert Group may still Page people during one team's deploy, because a Team's window holds it only when all
  its Teams are in a window.
- Several Schedules per Team cover a first and a second line without a concept of their own.
- The push of a mobile app can join the Notification rules later as one more way to Page.

## Alternatives considered

- **Non-exclusive subscriptions**, in which every Destination or Team picks Alerts with Matchers of its own and an Alert
  goes wherever one matches. The example setup needs twelve such rules, and an Alert caught by several of them still
  needs one Alert Group with one status (journal D468).
- **Alertmanager's `continue` on Routes.** Already rejected in ADR-0003: one Alert would live in several Alert Groups
  with independent statuses (journal D468).
- **The Team of an Alert Group taken from its first Alert**, or only from the labels all its Alerts share. A second team
  in the Alert Group would never learn about it, or nobody would once the values differ (journal D510).
- **A Team mention that falls back to the On-call.** A team with a messenger group could then never have only its
  On-call mentioned, and a Mention would hide which of the two it means; two explicit entries say it (journal D488,
  D509).
- **An acknowledgement, Owner and status per Team.** Teams handling their parts of one problem apart is a change of the
  Alert Group's state machine; L2 keeps one status, and the per-Team state is planned for L3 (journal D508, D535).
- **Paging by direct messages.** A direct message is neither a Root message nor a Thread reply, so it would need a third
  send path with a queue of its own, a choice of Connection for people linked in several messengers, and a rule for a
  person who blocked the bot; a Mention in the Thread uses delivery as it is (journal D503).
- **Direct messages about Shifts and Overrides, and a bot command that says who is on call.** The messages need the same
  third path; a command needs setup on every Mattermost server, another Slack scope and new Telegram update types. The
  UI and the iCal feed show the same (journal D504, D515, D534).
- **Backup Destinations on a Route**, used while its Destinations are Broken. The Internal alert
  `MusterDestinationBroken`, routed to another messenger, already reaches people, and gateway steps work without a
  messenger (journal D495).
- **A Connection type of its own for gateways**, with a second protocol. Bridges already have the transport,
  idempotency, signed data and events; a role keeps one protocol and one pair of libraries (journal D505).
- **One Assignee: whoever the last step Paged.** With several chains the value would jump between teams, and the filter
  of a person's Alert Groups would lose people; a set kept per chain stays stable (journal D508).
- **Adding Assignees by hand.** Not needed while every Responder may acknowledge (journal D474).
- **Starting every chain when its Alert Group is created.** Would Page people about Alert Groups that wait in a Storm or
  that no Destination shows yet; the start of the ack timeout already marks when a person can see the problem (journal
  D507).
- **Every chain writing in every Thread of its Alert Group.** A person would get one Mention per Destination, and a
  team's step for everyone in the chat would ring the operations channels (journal D529).
- **A Team's window holding every Alert Group of that Team.** One team's deploy would stop the Escalation chains of
  another team whose Alerts share an Alert Group (journal D523, D530).
- **Importing Schedules from iCal**, and **Overrides that the other person must approve.** More moving parts than teams
  need; Overrides take effect at once and are audited (journal D470, D483).
- **Teams isolated by Roles of their own now.** Waits for custom Roles; global Roles plus one membership check for
  Overrides cover L2 (journal D464).
