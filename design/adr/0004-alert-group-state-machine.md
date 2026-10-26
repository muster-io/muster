# 0004. Alert Group state machine

- Status: Accepted
- Date: 2026-10-02
- Amended: 2026-10-03 — Unclaimed ends when the Alert Group stops being firing: acknowledged, snoozed or resolved; a
  Reopen is keyed by the Route and the Group key values, so any Alert with the same key reopens the Alert Group;
  the Owner is always a User — a Service account cannot acknowledge
- Amended: 2026-10-26 — for L2 (journal D487, D507, D508, D518, D528, D535, D546, D547, D555, D556): Escalation chains
  start, start over and stop where the ack timeout does and replace its notices where they run, and an Alert Group is
  Unclaimed once every chain started on it has run out; Acknowledge, Snooze and Resolve clear the Assignees; an
  acknowledgement through a Notification gateway applies only while nobody owns the Alert Group; a Route may let new
  Alerts remove the acknowledgement; an Alert Group attached to another follows its status; Urgent means critical
  (ADR-0003)

## Context

An Alert Group changes because of people (acknowledge, resolve, snooze) and because of its Alerts (new ones arrive, old
ones go away, some come back). Each change can make phones ring, so the rules must be predictable for responders, and
every automatic change must be explainable afterwards. Several real situations stress the design:

- Rule evaluators restart. If a restart lasts longer than an alert's `endsAt` window, Alertmanager declares the alert
  resolved by itself and later receives it again with a new `startsAt`. Seen through a webhook, this cannot be told
  apart from a real resolve followed by a new firing. Resetting acknowledgements on every such blip would page people
  about Alert Groups they already own.
- Informational alerts, such as "certificate expires in 30 days", stay firing for weeks by design. A forced maximum age
  would produce new, loud Alert Groups for nothing.
- A person resolves an Alert Group while its Alerts are still firing in Alertmanager.
- An exporter restarts and reports the same problem with a different `pod` label.

Mechanisms such as flapping detection or delayed publication should be added only when real operation proves they are
needed.

## Decision

**Four statuses, nothing more:** `firing`; `acknowledged` (with its Owner); `resolved` (by a user, or by the system with
a reason); `snoozed` (until a time). Reopen counts, flags and similar facts are attributes and Timeline entries, not
statuses.

**Commands** pass through one dispatcher in the command layer (ADR-0016): permission → precondition → transition → Audit
log (people and automation) → Timeline → re-render. Transports — the web UI, the API, the MCP server, the messengers,
Bridges and Notification gateways — are thin adapters. Transitions that Muster starts itself, on a timer or because of
ingestion, use the same dispatcher with the Transport `system`; they are recorded in the Timeline, while the Audit log
records what people and automation did. Each command has its own Audit log type. A status change made by a person only
edits the Root message and is Quiet (ADR-0005), except where the table says otherwise.

| Command | Allowed from | Effect |
|---|---|---|
| Acknowledge | firing, snoozed | → acknowledged with the caller as Owner, who is always a User: a Personal access token acts as its User, a Service account is refused; ends a Snooze. Refused on resolved; it never reopens. Repeated by the Owner: no-op. By another user: Takeover — ownership moves without confirmation, and a Loud Thread reply says who took it from whom and mentions the previous Owner. From a Notification gateway (L2) it applies only while nobody owns the Alert Group: firing or snoozed → acknowledged; acknowledged → unchanged, and the person is told who has it; a Takeover needs an explicit action in a messenger or the UI. |
| Unacknowledge | acknowledged | → firing without an Owner |
| Resolve | firing, acknowledged, snoozed | → resolved by the user |
| Snooze(until) | firing, acknowledged, snoozed | → snoozed until the given time. Messengers offer the Route's Snooze durations (default 1 h / 4 h / 24 h); the UI and the API accept any time, or no end at all, which the UI marks and filters separately. |
| Unsnooze | snoozed | → firing without an Owner, Quiet like any status change made by a person |
| Unresolve | resolved by a user | → firing without an Owner; the Alerts still firing in the last Snapshot become active again. Refused if none is still firing, if the system resolved the Alert Group (it reopens by itself when an Alert with its key fires again), or if a newer open Alert Group with the same key exists. UI and API only. |

Messenger buttons follow the preconditions of the current status; the exact set per status belongs to the product
specification. Unresolve is never offered in messengers, and a resolved Alert Group has no buttons, only a link to
Muster.

**Resolution by the system.** The system resolves an Alert Group when all its Alerts are resolved: by an explicit
`resolved` from Alertmanager, as Gone, as Stale (ADR-0002), or because the Integration was deleted. A resolve from the
source always wins and ends a Snooze.

**Reopen after a system resolve.** A Reopen is keyed by the Route and the Group key values (ADR-0003), not by the Alert:
if any Alert with the same Group key values fires on the same Route within the Route's Reopen window (default
15 minutes) after the system resolved the Alert Group — one of its own Alerts returning or an Alert it never had — the
same Alert Group reopens, with a visible Reopen count, and **returns to the status it had before**:

- acknowledged → the same Owner; a Loud Thread reply announces the Reopen and mentions only the Owner; Reminders
  continue;
- snoozed, with the Snooze end still ahead → snoozed again, Quiet (Root message and Timeline only);
- firing, or a Snooze that ended in the meantime → firing; a Loud Thread reply announces the Reopen, and the ack timeout
  starts over.

**Manual resolve and the Grace period.** After a person resolves, no Reopen window applies: the next firing starts a new
Alert Group. To make a premature resolve visible, the Root message shows how many Alerts are still firing in
Alertmanager, based on the last Snapshot. If those Alerts are still reported firing once the Route's Grace period
(default 15 minutes) has passed, they are grouped again like newly firing Alerts: they join the open Alert Group with
the same key — such as one that a new Alert started within the Grace period — and otherwise start a new Alert Group,
marked as firing again after a manual resolve. Within the Grace period, an Alert that fires again after Alertmanager
reported it `resolved`, or a new Alert with the same key, starts a new Alert Group immediately: the Grace period only
tolerates the tails of the problem that was resolved. A new `startsAt` without a `resolved` in between is a
continuation, not a new firing (ADR-0002).

**New Alerts in an existing Alert Group.**

- firing → Thread replies collected over the Thread batching window (ADR-0005), Loud;
- acknowledged → a Quiet Thread reply and a Root message update; the acknowledgement stays, unless the Route's Loud and
  Quiet settings let new Alerts remove it, which is off by default (L2, ADR-0005; journal D546): then the Alert Group
  becomes firing without an Owner and the reply is Loud;
- snoozed → a Root message update only; when the Snooze ends, the Thread lists what accumulated;
- **Replacement** — a new Alert that differs from a firing one only in Instance labels (an Organization setting,
  default `pod`, `instance`, `container`, `endpoint`) → a Quiet Thread reply naming the label that differs; the old
  Alert going away is Quiet too, and the UI suggests fixing the source (for example `without(pod, instance)` in the
  rule). Fingerprints and Alert identity are untouched; only loudness and wording change.

**Snooze ends while firing** → firing without an Owner; a Loud Thread reply says that the Snooze ended while the Alert
Group is still firing and lists what accumulated.

**A rise to Urgent.** When a rise in Severity level makes the Alert Group Urgent (ADR-0003), it ends a Snooze — unless
the Snooze was set while the Alert Group was already Urgent — and removes the acknowledgement (a Route setting, on by
default): the Alert Group becomes firing and a Loud Thread reply gives the reason and mentions the Owner. A rise in
Severity level that leaves urgency unchanged, and any edit of the Route — its Minimum severity included — never removes
an acknowledgement or a Snooze.

**Timers.** The ack timeout runs only while the Alert Group is firing. It starts at the Alert Group's publication and
starts over whenever the Alert Group becomes firing again — a Reopen into firing, Unacknowledge, Unsnooze, Unresolve, a
Snooze ending, a rise to Urgent and, from L2, new Alerts that remove the acknowledgement where the Route allows it;
Acknowledge, Snooze and Resolve stop it. Its notices come at doubling intervals — 15, 30 and 60 minutes, that is 15, 45
and 105 minutes after the timer started — and there are at most three; after that the Alert Group is shown as Unclaimed
until it stops being firing — acknowledged, snoozed or resolved. Reminders to the Owner start after 4 hours and double
up to 24 hours; when the Owner answers a Reminder, the interval does not reset; a Takeover starts them over for the new
Owner; snoozed Alert Groups get no Reminders. Timers are rows with deadlines that any replica may claim (ADR-0006,
ADR-0007).

**Escalation chains (L2).** An Escalation chain (ADR-0018) runs only while the Alert Group is firing and shares the ack
timeout's points: it starts at the Alert Group's Publication — or when the Alert Group is created, if no Destination can
publish it because there is none or all are Broken — starts over whenever the Alert Group becomes firing again, on the
same events as the ack timeout, and stops on Acknowledge, Snooze and Resolve; so when a Snooze ends on a firing Alert
Group that nobody owns, its chains start again (journal D487, D507, D528). In a Storm, a chain waits for its Alert
Group's own Publication, and a Maintenance window keeps it waiting. A rise in Severity level of a firing Alert Group
that nobody owns restarts no running chain; it only starts the chains whose Severity condition it now meets, such as
those for critical only (journal D555), and a Team that joins starts its own where the Route runs the chains of Teams.
When the first chain starts, a running ack timeout stops and sends no more notices; an Alert Group without a chain keeps
the ack timeout. The Alert Group is Unclaimed once every chain started on it has run out without an acknowledgement
(journal D535). Steps are timer rows like the ack timeout. The **Assignees** — the people the running chains Paged last
— are cleared by Acknowledge, Snooze and Resolve and change only through system transitions (ADR-0016; journal D508).

**Auto-unacknowledge.** A Route setting, off by default, takes an Alert Group back from an Owner who has stopped
responding. The Owner answers a Reminder by pressing "Still on it", by running any command on the Alert Group or by
adding a Note to it before the next Reminder is due. When the Owner has answered neither of the last two Reminders,
the next Reminder is not sent; instead the system unacknowledges the Alert Group with that reason: it becomes firing
without an Owner, a Loud Thread reply gives the reason and mentions the Owner, and the ack timeout starts over. Like
every transition Muster starts itself, it goes through the dispatcher with the Transport `system`.

**Attaching (L2).** With the commands Attach and Detach, a person puts an Alert Group under another one that stands for
the same problem, its main Alert Group, and lets it live on its own again at any time, from the UI or through the API.
The attached Alert Group follows the main one: Acknowledge, Snooze and Resolve of the main Alert Group apply to it too,
its own Escalation chains and timers stop, its Root messages shrink to a line that names the main Alert Group with a
link and carry no buttons, and its Alerts are shown on the main Alert Group's page. Once detached, it lives on its own
again with the current state of its own Alerts. Following the main Alert Group is a system transition (ADR-0016). The
preconditions of Attach and Detach, and what happens to the Assignees, the ack timeout, the Escalation chains and
Unclaimed when an Alert Group is attached and after it is detached, are set in the L2 PRD. Several teams working on one
problem together is a matter for Incidents in L3 (journal D547, D556).

**Reasons are visible.** Every automatic status change carries its reason in the message or Thread and in the Timeline.

**Clock and downtime.** All windows run on Muster's clock (ADR-0002). After Muster was down, overdue timers fire once,
collapsed into one notice that says how many were missed, and open Alert Groups get a Timeline entry with the period
during which Muster was unavailable.

**Not in L1:** flapping detection, a resolve delay, holding publication back for a while ("hold"), and a maximum Alert
Group age. Each can be added if operation shows the need; a design for hold is kept for that case.

## Consequences

- A short outage of the alerting pipeline does not undo people's work: owned and snoozed Alert Groups stay owned and
  snoozed.
- Without flapping detection, a flapping alert produces Reopens of one Root message and Thread lines, not new Alert
  Groups — as long as each gap is shorter than the Reopen window. Because the Alert Group stands for its key, the same
  holds when the problem comes back as an Alert with another fingerprint but the same Group key values.
- Unresolve and Reopen are deliberately different: a person can undo only a person's resolve; a system resolve undoes
  itself when Alerts with the same key fire again.
- A Snooze ends Loudly when it runs out on its own or when a rise to Urgent ends it; ending it by hand is Quiet,
  because the person who did it is already looking at the Alert Group.
- Only these automatic changes take an acknowledgement away: a rise in Severity level that makes the Alert Group Urgent,
  auto-unacknowledge on a Route that turns it on and, from L2, new Alerts on a Route whose Loud and Quiet settings allow
  it. Configuration edits never do.
- An Alert Group lives as long as any of its Alerts fires; its age is visible from its start time, and Reminders keep
  the Owner aware.
- Restarted exporters stop paging people, while the Alert Group still shows the replaced Alerts.
- From L2 an Alert Group with Escalation chains is Unclaimed only after all of them have run out, and one
  acknowledgement stops them all, whichever Team gave it.

## Alternatives considered

- **Reset the acknowledgement on every Reopen.** Simple, but every long restart of a rule evaluator would page people
  about Alert Groups they already own.
- **Reopen on Acknowledge of a resolved Alert Group** (as Grafana OnCall does), or **a Reopen window after a manual
  resolve.** Both make "I resolved it" unreliable; Unresolve covers mistakes explicitly.
- **One generic "set status" operation.** Loses per-command preconditions, Takeover as an action of its own and distinct
  Audit log types.
- **More statuses** (pending, flapping, reopened). Every consumer — UI, buttons, outgoing webhooks — would have to
  understand them; attributes carry the same information.
- **A Loud Unsnooze, the same as a Snooze running out.** Rings everyone for a deliberate action by someone who is
  already handling the Alert Group.
- **A Route setting that removes the acknowledgement when new Alerts arrive** (L1). Every new Alert in an owned Alert
  Group would page people again; the acknowledgement always stays and new Alerts are Quiet until a configurable
  Loud/Quiet matrix exists in a later layer. From L2 the Route's Loud and Quiet settings offer it, off by default
  (journal D546).
- **Auto-unacknowledge a fixed time after the acknowledgement.** Takes long problems away from Owners who are still
  working on them; counting unanswered Reminders takes back only Alert Groups whose Owner has stopped responding.
- **Remove acknowledgements when a Route is marked urgent** (L1). A configuration edit would page people about every
  open Alert Group on that Route at once. The mark is gone in L2, and changing a Route's Minimum severity never removes
  them either (ADR-0003).
- **Merge Alerts that differ only in ignored labels** instead of treating them as Replacements. Risks merging different
  problems and complicates resolution; changing only loudness reaches the goal.
- **Hold, flapping detection, maximum age now.** Each adds rules people must learn; they wait until operation shows a
  need.
- **An acknowledgement, Owner and status per Team** in an Alert Group that several Teams share (L2). A change of this
  state machine that every consumer would have to learn; planned for L3 (ADR-0018; journal D508, D535).
- **An acknowledgement through a Notification gateway that takes over** (L2). A call answered minutes after someone else
  acknowledged would take the Alert Group away from them loudly; only an explicit action takes over (journal D518).
