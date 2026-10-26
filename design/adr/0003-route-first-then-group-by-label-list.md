# 0003. Route first, then group by a list of labels

- Status: Accepted
- Date: 2026-10-02
- Amended: 2026-10-26 — for L2 (journal D468, D510, D511, D525, D531, D532): an Alert Group is Urgent when its Severity
  level is critical; the Route's urgent mark and the Organization's switch for critical give way to the Route's Minimum
  severity, which raises only the Severity level of its Alert Groups; a Route may also deliver to the Destinations of
  the Teams of its Alerts (ADR-0018), with one status for all; a Route or a Team may limit each Destination to some
  Severity levels, and the conditions of a Destination named more than once combine with OR

## Context

Alertmanager already groups alerts, but its grouping serves notification batching: an Alertmanager group under one
`groupKey`, shaped by an Alertmanager routing tree that the Muster user may not control and that differs between
clusters. Muster needs Alert Groups that mean "one problem a person can take": stable when Alertmanager is reconfigured,
with their own number, status and Owner.

There are two possible orders: group alerts first and then decide where each resulting group goes, or decide where each
Alert goes and group inside that place. With grouping first, a key that ignores severity could put a critical Alert into
an Alert Group that is delivered to a Destination nobody watches at night. Users also need predictable behaviour when
several routing rules match, and a way to see the effect of a grouping change before saving it.

## Decision

**An Alert Group is not an Alertmanager group.** Muster splits every Snapshot into Alerts, one per fingerprint, and
regroups them by its own rules. `groupKey` is used only for Snapshot semantics (ADR-0002).

**Routes.** A Route picks Alerts with Matchers on labels in Alertmanager syntax — `=`, `!=`, `=~`, `!~` — all combined
with AND; the UI provides a builder. Routes form an ordered list and **the first matching Route wins**. A **Default
route** always exists, cannot be deleted and takes every Alert no other Route took; the minimal installation has only
this Route. Showing an Alert Group in several places means several Destinations on one Route; the Alert Group and its
status are shared by all of them. How an Alert Group is handled is set on its Route and edited in the UI: Destinations,
ack timeout, Reminders, Reopen window, Grace period, the Thread batching window, the Storm threshold, templates, Snooze
durations and the Group key, and from L2 its Minimum severity, its Escalation chains and which Lifecycle events are Loud
(ADR-0005, ADR-0018). Whom to mention is a setting of each Destination (ADR-0005); Severity levels and Instance labels
are settings of the Organization. A Route profile (On-call or Informational) only pre-fills the Route's settings when a
Route is created; it is not stored as a type.

**Delivery to Teams (L2).** A Route may also deliver to the Teams its Alerts belong to (ADR-0018), a Route setting that
is off by default. Its Alert Groups then go to the Route's Destinations together with the Destinations of those Teams —
one Root message per Destination, even when it is named more than once — and keep one status everywhere; the Route still
owns them and their timers. Each Destination of a Route or a Team may be limited to some Severity levels, checked
against the Alert Group's Severity level; the conditions of a Destination named more than once combine with OR. A Route
may belong to a Team, which owns it and filters it in the UI but adds no Destination: ownership and delivery are
separate relations (journal D468, D510, D525, D532).

**Route first, then group.** Grouping happens inside a Route, and an Alert Group never crosses a Route boundary. A
critical Alert therefore always lands on the Route meant for it, whatever the Group key.

**The Group key is a list of label names**, like `group_by` in Alertmanager. Default: `alertname`, `severity`,
`cluster`. A missing label counts as an empty value. Inside an Alert Group, Alerts are counted by fingerprint. With the
default key, one problem in three clusters is three Alert Groups — three Root messages in the same Destination, each
acknowledged on its own. The key cannot fail at runtime, so no fallback is needed; derived values (for example a regular
expression over a label) belong in Prometheus relabeling. The key editor shows a preview of how the most recent Stored
Snapshots would have been grouped with the new key.

**Urgency.** An Alert Group is Urgent when its Severity level is critical. The Organization defines Severity levels
once: which label carries severity and how its values map to levels. By default the `severity` label maps to the levels
critical, warning and info, with `none` counted as info; a value without a mapping counts as warning and is shown as
received, so that an unfamiliar severity is neither ignored nor treated as Urgent; an Alert without the label counts as
info. The same mapping drives colours and emoji. An Alert Group's Severity level is the highest level of its firing
Alerts, raised to the Route's **Minimum severity** where the Route sets one, such as critical on a Route whose every
Alert needs someone at once; the Minimum severity raises only the Alert Group's level, never that of its Alerts, so the
Alerts view and outgoing webhook events still show each Alert as reported (journal D511, D531). Urgency decides
individual publication during a Storm and the order in the delivery queue (ADR-0005), and from L2 whether an Alert Group
passes through a Maintenance window that lets Urgent ones through (ADR-0018); Urgent Alert Groups have a filter and a
column in the UI, a flag in outgoing webhook events and a count in the Storm summary, and the documentation has a page
on what critical changes. A rise in Severity level to critical ends a Snooze or an acknowledgement (ADR-0004), after
which the ack timeout and the Escalation chains start again; a rise that does not reach critical does not. Changing the
Minimum severity, like any other edit of a Route, never removes an acknowledgement or a Snooze and starts no Escalation
chain; the Alert Groups it raises to critical reach the Destinations limited to critical Quietly.

**Urgency in L1.** Until L2, an Alert Group was also Urgent when its Route was marked urgent, and a critical Severity
level made it Urgent only under an Organization switch, on by default. L2 keeps one axis: Routes marked urgent get the
Minimum severity critical, the mark and the switch go, and an installation that had turned the switch off sees its
critical Alert Groups become Urgent, which the upgrade notes of 0.2.0 say. Route profiles set no Minimum severity
(journal D511, D531).

**Identifiers.** Each Alert Group has a number `#N`, unique within the Organization and assigned without gaps in the
creating transaction, for people (messages, UI, search), and an opaque `public_id` that cannot be enumerated, for URLs,
the API and signed buttons.

**Editing configuration while Alert Groups are open.**

- An Alert Group is bound to its Route by id and never moves.
- Changes to the Route's settings (timeouts, windows, templates, Destinations, the Minimum severity) take effect at
  once, also for open Alert Groups, which are re-rendered by reconciliation (ADR-0005); they never change an Alert
  Group's status.
- A new Group key or new Matchers apply only to new Alert Groups. A fingerprint already living in an open Alert Group
  stays there until that Alert Group is resolved, even if it would now match another Route.
- A Route with open Alert Groups cannot be deleted. The UI offers to wait, or to move them to the Default route with a
  Timeline entry on each.
- Deleting an Integration is soft: its open Alert Groups are resolved by the system with the deletion as the reason,
  and its tokens stop working immediately.
- A Destination removed from a Route receives one final edit of each open Root message, saying that it is no longer
  updated and pointing to Muster, and nothing after that. A newly added Destination receives the open Alert Groups
  Quietly, within its rate limit (ADR-0005), and so does a Destination that a changed Severity condition adds; a changed
  condition never removes a Destination from an Alert Group (journal D512).

## Consequences

- Alert Groups stay the same when Alertmanager is reconfigured and look alike across clusters.
- Because the default key includes `severity`, the warning and the critical versions of the same alert are separate
  Alert Groups.
- Users must keep the Route list in order; "first match wins" is easy to explain and to show in the UI.
- Matchers have no OR; some setups need two Routes. A richer expression language (CEL) can be added later as an
  advanced mode.
- Alerts that differ only in a label outside the key share an Alert Group. Exporter restarts that change Instance labels
  are handled as Replacements (ADR-0004), not by special grouping.
- Stored Snapshots make the key preview possible.
- One axis of importance: critical is Urgent everywhere, and a Route whose every Alert needs someone at once raises its
  Alert Groups with a Minimum severity instead of a mark of its own.
- From L2 one Route can reach many teams' channels with one Alert Group and one status (ADR-0018).

## Alternatives considered

- **Use Alertmanager's `groupKey` as the Alert Group** (the default of Grafana OnCall's Alertmanager integration). Ties
  Alert Groups to the Alertmanager routing tree and its batching; grouping changes whenever that tree changes.
- **One Alert Group per fingerprint.** Far too many messages during a wide outage.
- **A Group key rendered from a template.** Can fail at runtime and then needs a fallback, an error metric and a harder
  preview; a list of labels covers the real cases.
- **Group first, then route.** An Alert Group could span Routes, and a critical Alert could be delivered where nobody
  is paged.
- **Deliver to every matching Route**, as Alertmanager's `continue` does. The same Alert would live in several Alert
  Groups with independent statuses; several Destinations on one Route give the same reach with one status.
- **CEL expressions for Matchers from the start.** More power than L1 needs; kept for later.
- **An urgent mark on the Route and an Organization switch beside Severity levels** (L1). Two axes of importance: a
  warning Alert Group on an urgent Route went first in the queue but into the channel for non-critical alerts, and with
  the switch off a critical one did the opposite; one axis keeps Destinations, limiters, Storms and Maintenance windows
  in agreement (journal D511).
- **Raising the Severity level of each Alert to the Route's minimum.** The Alerts view and the Alerts in outgoing
  webhook events would disagree with the Organization's mapping; raising only the Alert Group keeps the Alerts as
  reported (journal D531).
- **Teams that subscribe to Alerts with Matchers of their own** (L2). Twelve rules for a common setup, and still one
  Alert Group with one status; see ADR-0018 (journal D468).
