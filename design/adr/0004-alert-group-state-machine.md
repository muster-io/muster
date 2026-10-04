# 0004. Alert Group state machine

- Status: Accepted
- Date: 2026-10-02
- Amended: 2026-10-03 — Unclaimed ends when the Alert Group stops being firing: acknowledged, snoozed or resolved; a
  Reopen is keyed by the Route and the Group key values, so any Alert with the same key reopens the Alert Group;
  the Owner is always a User — a Service account cannot acknowledge

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

**Commands** pass through one dispatcher in the command layer (ADR-0016): permission → precondition → transition →
Audit log (people and automation) → Timeline → re-render. Transports — the web UI, the API, Mattermost and Telegram —
are thin adapters. Transitions that Muster starts itself, on a timer or because of ingestion, use the same dispatcher
with the Transport `system`; they are recorded in the Timeline, while the Audit log records what people and automation
did. Each command has its own Audit log type. A status change made by a person only edits the Root message and is Quiet
(ADR-0005), except where the table says otherwise.

| Command | Allowed from | Effect |
|---|---|---|
| Acknowledge | firing, snoozed | → acknowledged with the caller as Owner, who is always a User: a Personal access token acts as its User, a Service account is refused; ends a Snooze. Refused on resolved; it never reopens. Repeated by the Owner: no-op. By another user: Takeover — ownership moves without confirmation, and a Loud Thread reply says who took it from whom and mentions the previous Owner. |
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
- acknowledged → a Quiet Thread reply and a Root message update; the acknowledgement always stays;
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
Severity level that leaves urgency unchanged, and any edit of the Route — marking it urgent included — never removes an
acknowledgement or a Snooze.

**Timers.** The ack timeout runs only while the Alert Group is firing. It starts at the Alert Group's publication and
starts over whenever the Alert Group becomes firing again — a Reopen into firing, Unacknowledge, Unsnooze, Unresolve, a
Snooze ending, a rise to Urgent; Acknowledge, Snooze and Resolve stop it. Its notices come at doubling intervals — 15,
30 and 60 minutes, that is 15, 45 and 105 minutes after the timer started — and there are at most three; after that the
Alert Group is shown as Unclaimed until it stops being firing — acknowledged, snoozed or resolved. Reminders to the
Owner start after 4 hours and double up to 24 hours; when the Owner answers a Reminder, the interval does not reset; a
Takeover starts them over for the new Owner; snoozed Alert Groups get no Reminders. Timers are rows with deadlines that
any replica may claim (ADR-0006, ADR-0007).

**Auto-unacknowledge.** A Route setting, off by default, takes an Alert Group back from an Owner who has stopped
responding. The Owner answers a Reminder by pressing "Still on it", by running any command on the Alert Group or by
adding a Note to it before the next Reminder is due. When the Owner has answered neither of the last two Reminders,
the next Reminder is not sent; instead the system unacknowledges the Alert Group with that reason: it becomes firing
without an Owner, a Loud Thread reply gives the reason and mentions the Owner, and the ack timeout starts over. Like
every transition Muster starts itself, it goes through the dispatcher with the Transport `system`.

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
- Only two automatic changes take an acknowledgement away: a rise in Severity level that makes the Alert Group Urgent,
  and auto-unacknowledge on a Route that turns it on. New Alerts and configuration edits never do.
- An Alert Group lives as long as any of its Alerts fires; its age is visible from its start time, and Reminders keep
  the Owner aware.
- Restarted exporters stop paging people, while the Alert Group still shows the replaced Alerts.

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
- **A Route setting that removes the acknowledgement when new Alerts arrive.** Every new Alert in an owned Alert Group
  would page people again; the acknowledgement always stays and new Alerts are Quiet until a configurable Loud/Quiet
  matrix exists in a later layer.
- **Auto-unacknowledge a fixed time after the acknowledgement.** Takes long problems away from Owners who are still
  working on them; counting unanswered Reminders takes back only Alert Groups whose Owner has stopped responding.
- **Remove acknowledgements when a Route is marked urgent.** A configuration edit would page people about every open
  Alert Group on that Route at once.
- **Merge Alerts that differ only in ignored labels** instead of treating them as Replacements. Risks merging different
  problems and complicates resolution; changing only loudness reaches the goal.
- **Hold, flapping detection, maximum age now.** Each adds rules people must learn; they wait until operation shows a
  need.
