# 0003. Route first, then group by a list of labels

- Status: Accepted
- Date: 2026-10-02

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
durations and the Group key. Whom to mention is a setting of each Destination (ADR-0005); Severity levels and Instance
labels are settings of the Organization. A Route profile (On-call or Informational) only pre-fills the Route's settings
when a Route is created; it is not stored as a type.

**Route first, then group.** Grouping happens inside a Route, and an Alert Group never crosses a Route boundary. A
critical Alert therefore always lands on the Route meant for it, whatever the Group key.

**The Group key is a list of label names**, like `group_by` in Alertmanager. Default: `alertname`, `severity`,
`cluster`. A missing label counts as an empty value. Inside an Alert Group, Alerts are counted by fingerprint. With the
default key, one problem in three clusters is three Alert Groups — three Root messages in the same Destination, each
acknowledged on its own. The key cannot fail at runtime, so no fallback is needed; derived values (for example a regular
expression over a label) belong in Prometheus relabeling. The key editor shows a preview of how the most recent Stored
Snapshots would have been grouped with the new key.

**Urgency.** An Alert Group is Urgent if its Route is marked urgent **or** its Severity level is critical; the second
condition is an Organization switch, on by default. The Organization defines Severity levels once: which label carries
severity and how its values map to levels. By default the `severity` label maps to the levels critical, warning and
info, with `none` counted as info; a value without a mapping counts as warning and is shown as received, so that an
unfamiliar severity is neither ignored nor treated as Urgent; an Alert without the label counts as info. The same
mapping drives colours and emoji. In L1, urgency decides individual publication during a Storm and the order in the
delivery queue (ADR-0005). A rise in Severity level that makes an Alert Group Urgent ends a Snooze or an acknowledgement
(ADR-0004); a rise that does not change urgency — because the Route is already urgent or the Organization switch is off
— does not. Marking a Route urgent, like any other edit of a Route, never removes an acknowledgement or a Snooze.

**Identifiers.** Each Alert Group has a number `#N`, unique within the Organization and assigned without gaps in the
creating transaction, for people (messages, UI, search), and an opaque `public_id` that cannot be enumerated, for URLs,
the API and signed buttons.

**Editing configuration while Alert Groups are open.**

- An Alert Group is bound to its Route by id and never moves.
- Changes to the Route's settings (timeouts, windows, templates, Destinations, the urgent mark) take effect at once,
  also for open Alert Groups, which are re-rendered by reconciliation (ADR-0005); they never change an Alert Group's
  status.
- A new Group key or new Matchers apply only to new Alert Groups. A fingerprint already living in an open Alert Group
  stays there until that Alert Group is resolved, even if it would now match another Route.
- A Route with open Alert Groups cannot be deleted. The UI offers to wait, or to move them to the Default route with a
  Timeline entry on each.
- Deleting an Integration is soft: its open Alert Groups are resolved by the system with the deletion as the reason,
  and its tokens stop working immediately.
- A Destination removed from a Route receives one final edit of each open Root message, saying that it is no longer
  updated and pointing to Muster, and nothing after that. A newly added Destination receives the open Alert Groups
  Quietly, within its rate limit (ADR-0005).

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
