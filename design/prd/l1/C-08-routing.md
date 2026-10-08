# C-08. Routing

[L1 index](../L1.md) · Stage: Observe · UI: yes · Depends on: C-06, C-07

**Goal.** Users decide where each Alert goes and how its Alert Groups are handled: ordered Routes with Matchers, first
match wins, a Default route that always exists, and a Group key they can preview before saving.

Names in the form `area.setting` refer to rows of [defaults.md](defaults.md).

## Scenarios

1. A small installation keeps only the Default route.
2. The Admin creates the Route "payments-critical" with Matchers `team="payments"` and `severity="critical"`, marks it
   urgent and keeps the On-call profile.
3. The Admin changes a Route's Group key to `alertname, cluster, namespace` and sees how the Snapshots of the last day
   would have been grouped.
4. The Admin creates "certificates" with the Informational profile: no ack timeout, no Reminders, long Snooze durations
   (as those capabilities add the behaviour of these fields).
5. An Integration has a Heartbeat and only the Default route would take `MusterHeartbeatLost`; the Routes page offers a
   Route for it at the top of the list.

## Functional requirements

- **C-08.FR-1** A Route has a name unique in the Organization, a description, Matchers, a position in the list, an urgent
  mark (`route.urgent`), a Group key, a list of Destinations (empty until a Destination type exists, C-13 to C-15) and
  its policy fields: Reopen window and Grace period, and "rise to Urgent removes the acknowledgement" (C-09); Snooze
  durations (C-10); Thread batching window and Storm threshold (C-11); templates and language (C-12); ack timeout,
  Reminders and auto-unacknowledge (C-17). A Route stores every policy field from the start — a new Route takes the
  values of its Route profile ([defaults.md](defaults.md#routing-and-lifecycle)) — and the API reads and writes them
  all; the capability named with each field adds its behaviour and its section of the Route form. Until C-12 can check
  templates, a Route accepts only the built-in templates, and any other template is refused.
- **C-08.FR-2** Matchers use Alertmanager syntax — `=`, `!=`, `=~`, `!~` with RE2 regular expressions — and are combined
  with AND. The UI builds them from fields; the API accepts the same structure; an invalid expression is refused with a
  pointer to the field.
- **C-08.FR-3** Routes are evaluated in list order and the first Route whose Matchers all match takes the Alert. The
  Default route is always last, has no Matchers, cannot be deleted or moved, and takes every Alert no other Route took.
- **C-08.FR-4** The Group key is a list of label names (`route.group_key`). A missing label counts as an empty value.
- **C-08.FR-5** The Group key editor previews the Alert Groups that the Stored Snapshots of the selected period
  (`routing.group_key_preview_period`; at most `retention.stored_snapshots`) would produce with the current and with the
  proposed key: how many Alert Groups, and the largest of them as examples (`routing.group_key_preview_examples`). The
  preview covers the Alerts reported firing in those Snapshots that the Route would take at its place in the current
  evaluation order, each Alert once and with its Integration's Static labels. It reads the Snapshots from the newest
  back and takes at most `routing.group_key_preview_max_alerts` Alerts; when that limit stops it before the start of
  the period, the preview says that it covers only the most recent part. The preview also works while a Route is being
  created: it takes the Matchers of the unsaved Route, placed where a new Route goes — just before the Default route —
  and shows the proposed key alone.
- **C-08.FR-6** Each Alert gets a Severity level from the Organization's Severity levels (C-02.FR-23, editable in C-20):
  by default the `severity` label maps to critical, warning and info, `none` counts as info, a value without a mapping
  counts as warning and is shown as received, and an Alert without the label counts as info
  (`organization.severity_mapping`). An Alert Group's Severity level is the highest of its firing Alerts. An Alert Group
  is Urgent if its Route is marked urgent, or if its Severity level is critical and "critical is Urgent"
  (`organization.critical_is_urgent`) is on.
- **C-08.FR-7** Creating a Route offers two Route profiles that only pre-fill its settings and are not stored: On-call
  and Informational. Every policy field has a value for each profile, given in
  [defaults.md](defaults.md#routing-and-lifecycle). The profiles are read-only data served from the built-in defaults
  (`route-profiles`); they cannot be edited.
- **C-08.FR-8** Edits of a Route's settings apply at once, also to open Alert Groups (whose Root messages are re-rendered
  once delivery exists, C-11); they never change any Alert Group's status. A new Group key or new Matchers apply only to
  new Alert Groups, and a fingerprint already living in an open Alert Group stays there until that Alert Group is
  resolved (C-09).
- **C-08.FR-9** Deleting a Route removes it from the evaluation order; the Default route cannot be deleted. A Route with
  open Alert Groups is handled by C-09.FR-19.
- **C-08.FR-10** Route edits and reordering use optimistic locking: a stale `If-Match` gets `412`. A reorder that
  includes the Default route gets `409` (`default-route-immutable`); one whose Routes differ from the current set
  gets `422`.
- **C-08.FR-11** The Routes page suggests, at the top of the list, a Route for `alertname="MusterHeartbeatLost"` when an
  Integration has a Heartbeat and no Route other than the Default route matches that alert; and, from C-13 on, a Route
  for Internal alerts `alertname=~"Muster.*"` to a chosen Destination when no Route other than the Default route matches
  them (C-13.FR-11). Accepting a suggestion creates the Route at the top of the list — except the Internal alerts
  Route when a Route other than the Default route already takes an Internal alert, such as the Route for
  `MusterHeartbeatLost`: it goes directly below the last such Route, so that the broader Route does not take that alert
  first; dismissing a suggestion is remembered per user. Both are operations on the suggestion (`accept`, `dismiss`);
  either gets `409` (`suggestion_obsolete`) when the suggestion no longer applies, for example when a matching Route
  exists by now.
- **C-08.FR-12** Each Route exports `muster_route_info{route,name}`.
- **C-08.FR-13** Routing records which Route took each newly firing Alert and its Severity level; both are shown in the
  Integration's Alerts view (C-06.FR-19). An Alert that already lives in an open Alert Group is not routed again (C-09).

## UI

Routes list in evaluation order with drag-to-reorder and the Default route pinned last; suggestions at the top; Route
editor with the Matcher builder, profile choice on creation, Group key editor with preview, the Destinations picker and
the policy sections as later capabilities add them.

## API surface

`routes` (list, create, read, update, delete); `route-order` (replace); `route-profiles` (list); `route-suggestions`
(list; accept and dismiss one); `group-key-previews` (create: a saved Route or the Matchers of an unsaved one, and the
proposed key → preview).

## Acceptance

Checked with the fake Alertmanager, through the Integration's Alerts view and the API.

- **C-08.AC-1** With Routes A (`severity="critical"`) and B (`team="x"`), a newly firing Alert with both labels is taken
  by whichever is first; after reordering, the next newly firing Alert with both labels is taken by the other.
- **C-08.AC-2** In the Group key preview, Alerts missing the `cluster` label group with others missing it.
- **C-08.AC-6** The Default route cannot be deleted or moved; such requests get `409`.
- **C-08.AC-7** A stale `If-Match` on a Route edit or a reorder gets `412`.
- **C-08.AC-8** With a Heartbeat configured on an Integration and no Route other than the Default route for
  `MusterHeartbeatLost`, the Routes page offers one; accepting it creates the Route at the top of the list, and the
  suggestion does not come back.
- **C-08.AC-9** The Alerts view shows Severity levels by the default mapping: `severity="none"` as info,
  `severity="P5"` as warning shown as `P5`, no `severity` label as info.
- **C-08.AC-10** A Matcher with an invalid RE2 expression is refused with `422` and a pointer to the field.
- **C-08.AC-11** A reorder that lists the Default route gets `409`; accepting a suggestion twice gets `409` the second
  time; `route-profiles` returns the On-call and Informational profiles with the values of the defaults.
- **C-08.AC-12** The Group key preview with the Matchers of an unsaved Route and a proposed key returns the proposed
  side for the Snapshots of the period.

## Related ADRs

ADR-0003, ADR-0004.

## Depends on

C-06 — Alert changes to route and the Alerts view; C-07 — Heartbeat settings for the suggested Route.

## Suggested story split

- **BE** — Route model, Matchers, evaluation, Severity levels and urgency, Group key preview, suggestions, metrics.
- **FE** — Routes list, editor, Matcher builder, Group key preview, suggestions.
