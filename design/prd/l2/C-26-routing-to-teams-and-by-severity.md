# C-26. Routing to Teams and by Severity

[L2 index](../L2.md) · Release: 0.2.0 · UI: yes · Depends on: C-08, C-11, C-25

**Goal.** One Route serves many teams (issue #363): each Destination of a Route or a Team may be limited by Severity
level, a Route may also deliver its Alert Groups to the Destinations of the Teams of their Alerts — with or without the
Teams' Escalation chains — and every Destination shows the same Alert Group with one status. Importance has one axis: an
Alert Group is Urgent when its Severity level is critical, and a Route raises its Alert Groups with a Minimum severity
instead of an urgent mark.

Names in the form `area.setting` refer to rows of [defaults.md](../l1/defaults.md); the lifecycle events are rows of the
[L2 lifecycle event table](../l1/reference.md#lifecycle-events-added-in-l2).

## Scenarios

1. Five product teams each have a channel for critical and one for other alerts, and operations has `infra-critical` and
   `infra-notice`. The Route "Infrastructure" (`category="infra"`, Group key `alertname`, `system`, `team`) names
   `infra-critical` for critical and `infra-notice` for below critical, with "Also to the Teams from the label" on. A
   `KubernetesContainerOOMKilled` warning of `team="payments"` becomes one Alert Group with Root messages in
   `infra-notice` and `payments-notice`; Acknowledge in either updates both.
2. The Route "Products and business" (`category=~"product|business"`) names no Destination of its own and delivers only
   to the Teams from the label.
3. In a Route whose Group key lacks `team`, an Alert of "search" joins the firing Alert Group of "payments": it is
   published in `search-notice` Loudly, with the Mentions of a new Alert Group, while the other Root messages are
   updated. Had someone acknowledged it before, it would have arrived acknowledged and Quiet, and no chain of "search"
   would have started.
4. In a Route without `severity` in its Group key, a warning Alert Group gets a critical Alert. It is published in
   `infra-critical` and `payments-critical`; its messages in `infra-notice` and `payments-notice` stay and keep being
   updated, also after the critical Alert resolves.
5. The Route "Test clusters" (`cluster=~".*-stage|.*-preprod"`) comes first, names no Destination and has "Also to the
   Teams from the label" on and "Run the Teams' Escalation chains" off: teams see their test alerts in their channels,
   and nobody is Paged.
6. The Route for `MusterHeartbeatLost` has the Minimum severity critical: its Alert Groups are critical and Urgent,
   while the Alerts view still shows each Alert with its own Severity level.
7. An installation upgrades from 0.1: its Route marked urgent now has the Minimum severity critical, and the upgrade
   notes tell an Admin who had turned "critical is Urgent" off that critical Alert Groups are now Urgent.

## Functional requirements

### Destinations with Severity conditions

- **C-26.FR-1** A Route names its Destinations, and a Team names its Destinations — the Destinations of the Team — each
  with a Severity condition: any Severity level (`any`, the default), only `critical`, or below critical
  (`below_critical`), checked against the Alert Group's Severity level (FR-8). A Destination may be named by several
  Routes and Teams, each with its own condition. From C-28 on, naming a Destination of the Team adds the Mention entry
  of C-28.FR-6.
- **C-26.FR-2** A Route has the option "Also to the Teams from the label" (`route.deliver_to_teams`) and, under it, "Run
  the Teams' Escalation chains" (`route.run_team_chains`). With the option on, an Alert Group of the Route goes to the
  Route's Destinations whose condition its Severity level meets and to the Destinations of each Team of the Alert Group
  (C-25.FR-6) whose condition it meets; with it off, only to the Route's. With "Run the Teams' Escalation chains" off,
  those Teams receive the Alert Group in their Destinations, but their chains do not run on it (C-32.FR-4).
- **C-26.FR-3** A Destination that several entries reach — the Route's and a Team's, or two Teams' — gets one Root
  message for the Alert Group; it is reached when the condition of any of its entries is met.
- **C-26.FR-4** The Destinations of an Alert Group only grow. A Team that joins it (C-25.FR-6) adds that Team's
  Destinations whose condition its Severity level meets, and a rise of its Severity level adds the Destinations whose
  condition it now meets, of the Route and of its Teams. A Severity level that falls again, or a Team whose Alerts
  resolved, leaves every Root message in place, kept current until its final state.
- **C-26.FR-5** When an Alert Group reaches a Destination after its first Publication elsewhere — because its Alerts
  raised its Severity level into the Destination's condition or a new Alert brought a Team — the lifecycle event
  `destination_joined` publishes it there: Loudly, with the Mentions of the Destination's entries for `new_alert_group`
  (C-28.FR-1), when it is firing; Quietly when it is acknowledged or snoozed. One event is recorded for each Destination
  it joins; the other Destinations see only a Root message update. A Destination that a Team or a rise adds before the
  first Publication is part of that Publication. During a Storm of the Route, the Destination counts the Alert Group in
  its own Storm summary unless the Alert Group is Urgent (C-11.FR-6). A Destination that a change of configuration adds
  — saving a Team (C-25.FR-13), a changed Minimum severity (FR-9) or the changes of FR-6 — receives the Alert Group
  Quietly, without `destination_joined` and without starting an Escalation chain (C-32.FR-5). How a Maintenance window
  delays `destination_joined` is specified with Maintenance windows.
- **C-26.FR-6** Configuration changes add Destinations Quietly and remove none by a condition: naming a Destination on a
  Route or a Team, turning "Also to the Teams from the label" on, or changing a condition so that open Alert Groups now
  meet it publishes them there Quietly, within its limits, as a Destination added to a Route (C-11.FR-14). A condition
  changed so that an open Alert Group no longer meets it leaves its Root message there. A Destination taken off a Route
  or a Team, or reached no longer because the option was turned off, gets the final edit of C-11.FR-14 for each open
  Alert Group that no other entry still sends there.
- **C-26.FR-7** The owning Team of a Route (C-25.FR-2) adds no Destination and no Escalation chain; the Route form says
  so next to the field.

### Severity level and Urgent

- **C-26.FR-8** The Severity level of an Alert Group is the highest Severity level of its firing Alerts, raised to the
  Route's Minimum severity (`route.minimum_severity`: none, `warning` or `critical`). The Minimum severity never changes
  the level of an Alert: the Alerts view, the Alerts of the Alert Group page and `alerts[]` of outgoing webhook events
  show each Alert's own level. An Alert Group is Urgent when its Severity level is critical. Urgency decides, as in L1,
  the order in the delivery queue (C-11.FR-3), individual publication during a Storm (C-11.FR-6) and the end of a Snooze
  or an acknowledgement on a rise (C-09.FR-9); Urgent Alert Groups have their filter and column in the list, `urgent` in
  outgoing webhook events and their count in Storm summaries.
- **C-26.FR-9** The Minimum severity is a Route setting like any other (C-08.FR-8): a change applies at once to the open
  Alert Groups of the Route, never removes an acknowledgement or a Snooze and starts no Escalation chain. Each Alert
  Group it raises records a Quiet `severity_raised` — or the Quiet `urgency_raised` of an Alert Group it makes critical,
  which removes nothing (C-09.FR-22) — with the reason "Minimum severity of the Route"; a lowered Minimum severity
  records nothing, as a falling Severity level does. The open Root messages are re-rendered Quietly, and the Alert
  Groups it raises into a Destination's condition reach that Destination Quietly (FR-6).
- **C-26.FR-10** In 0.2.0 the Route's urgent mark (`route.urgent`) and the Organization's switch "critical is Urgent"
  (`organization.critical_is_urgent`) no longer act, in expand and contract steps. The 0.2.0 migration gives every Route
  marked urgent the Minimum severity critical. Until 0.3.0 the API keeps `urgent` on Routes and on Route profiles as a
  read-only field — `true` exactly when the Minimum severity is critical — and `critical_is_urgent` on the Organization
  as a read-only `true`; requests that send them are accepted and the values ignored. 0.3.0 removes the fields and their
  columns. The upgrade notes of 0.2.0 say: Routes marked urgent got the Minimum severity critical, which also makes
  their Alert Groups critical for Severity conditions, and an Organization that had turned "critical is Urgent" off now
  sees its critical Alert Groups as Urgent — a rise to critical ends their Snoozes and, where
  `route.urgent_rise_removes_ack` is on, their acknowledgements. The migration and the API also do this:
  - the Severity level and the Urgent flag stored on the open Alert Groups and their deliveries are computed again once,
    by the new rule, when 0.2.0 migrates: without lifecycle events, Mentions or Escalation chains, without ending a
    Snooze or an acknowledgement, and without publishing anywhere new;
  - `urgent` in the input of a Route and `critical_is_urgent` in the input of the Organization become optional in the
    API specification, so that clients may leave them out;
  - during a rolling update from 0.1, whose replicas still read the columns, 0.2.0 writes `routes.urgent` as `true`
    exactly when the Minimum severity is critical, and the migration sets `organizations.critical_is_urgent` to `true`,
    so that both versions compute the same urgency;
  - the change of the profile id `on_call` to `paging` (FR-11) breaks clients that type the enum, and the upgrade notes
    say so.
- **C-26.FR-11** The Route profiles are Paging (formerly On-call) and Informational; neither sets a Minimum severity.
  `route-profiles` returns the ids `paging` and `informational` from 0.2.0; profiles are not stored, so the rename needs
  no migration, and the upgrade notes name it.

### Route form and documentation

- **C-26.FR-12** The Route form shows, under its Destinations, "Also to the Teams from the label" with its sub-option,
  and the hint that a new Team only adds channels and removes none. When the option is on and the Group key lacks the
  Team label, the form says that Alerts of different Teams with the same key share one Alert Group, shown in the
  channels of all of them, and offers "Add {team label} to the key". A preview "Where alerts go" takes the recent Alerts
  that the Route would take, as the Group key preview does (C-08.FR-5), and shows the largest Alert Groups they would
  form under the unsaved settings — as many as the Group key preview shows (`routing.group_key_preview_examples`, 5) —
  with the Teams of each and the Destinations each reaches, and, for every Destination that any of those Alert Groups
  reaches, how many of them reach it.
- **C-26.FR-13** The documentation has a page on routing to Teams, with the worked example of operations channels and
  team channels by Severity, that says the feature is optional — many installations need only two operations channels —
  and explains, with an example, that a shared Alert Group has one status and one list of Alerts in every channel and
  that teams wanting their own add the Team label to the Group key, and a Route for test clusters without chains; the
  Route settings link to it. A page "What critical changes" lists the effects of an Urgent Alert Group (FR-8) and the
  Minimum severity.

## UI

Route form: "Route Destinations — where the Route sends the messages of its Alert Groups; each Destination may have a
Severity condition", each Destination with "Severity = critical", "Severity < critical" or "Any severity"; "Also to the
Teams from the label {team label}" with the hint "The Alert Group also goes to the Destinations of the Teams whose
values are in its Alerts, with their Severity conditions. A new Team adds channels and removes none." and, under it,
"Run the Teams' Escalation chains"; the Group key hint and "Add {team label} to the key"; "Severity, Escalation chains
and owner": "Minimum severity" (None, warning, critical) with "Only raises the Severity level of the Route's Alert
Groups. Replaces the former 'Urgent': an Urgent Alert Group is a critical one."; the Escalation chains (C-32); "Owning
Team" with "For ownership and the filter of the UI; does not affect delivery."; the preview "Where alerts go". The
Routes list shows for each Route its Destinations, "+ Teams from the label" and its chains. Team page: "Destinations of
the Team" — Destination, Connection, condition — with "The Alert Groups of {Team} arrive here from the Routes that
deliver to the Teams from the label: {Routes}." The Alert Group page lists, in its delivery state, by which entry each
Destination was reached.

## API surface

Destination entries with `severity_condition` on `routes` and `teams`; `deliver_to_teams`, `run_team_chains`,
`minimum_severity` and the owning Team on `routes`; `urgent` on `routes` and `route-profiles` read-only until 0.3.0;
`critical_is_urgent` on `organization` read-only until 0.3.0; the profile ids `paging` and `informational`; the delivery
preview on `group-key-previews`; per Destination, the entries that reached it on `alert-groups/{id}/deliveries`.

## Acceptance

Checked with the fake Alertmanager, the fake messenger servers and the recording test adapter of C-11.

- **C-26.AC-1** With the setup of scenario 1, a warning Alert of "payments" makes one Alert Group with one Root message
  in `infra-notice` and one in `payments-notice` and none in the critical channels; Acknowledge from `payments-notice`
  edits both, and both show the same Owner.
- **C-26.AC-2** With "Also to the Teams from the label" off, the same Alert reaches only `infra-notice`.
- **C-26.AC-3** A Destination named by the Route for `critical` and by a Team for `below_critical` receives a warning
  and a critical Alert Group, each with one Root message.
- **C-26.AC-4** An Alert of "search" joining a firing, unowned Alert Group of "payments" records `destination_joined`
  for `search-notice` with `loudness` `loud` and `mentions` `[new_alert_group]`, and a new Root message there; joining
  an acknowledged one records it Quiet, and the Root message there is Quiet.
- **C-26.AC-5** A rise from warning to critical publishes the Alert Group in the critical Destinations with
  `destination_joined` and keeps the Root messages below critical; after the critical Alert resolves, all Root messages
  stay and are edited to the current state.
- **C-26.AC-6** Changing a Route Destination's condition from `critical` to `any` publishes the Route's open warning
  Alert Groups there Quietly and records no `destination_joined`; changing it back removes none of them.
- **C-26.AC-7** A Route with the Minimum severity critical makes a warning Alert's Alert Group critical and Urgent,
  while the Alerts view and the `alerts[]` of its `created` webhook event show the Alert as warning; setting the Minimum
  severity of a Route with an acknowledged warning Alert Group to critical keeps it acknowledged and starts no
  Escalation chain.
- **C-26.AC-8** After the 0.2.0 migration, a Route that was marked urgent has the Minimum severity critical, reads
  `urgent: true`, and an update that sends `urgent: false` changes nothing; `route-profiles` returns `paging` and
  `informational`, and `organization` reads `critical_is_urgent: true` whatever an update sends. An open warning Alert
  Group of a Route marked urgent is critical and Urgent after the migration, with no new Timeline entry and no new
  delivery; a new Route created with the Minimum severity critical has `routes.urgent` set; a Route created without
  `urgent` in its input is accepted.
- **C-26.AC-9** In a real browser, the Route form with "Also to the Teams from the label" on and no `team` in the Group
  key shows the hint and "Add team to the key", and the preview "Where alerts go" lists for recent Alerts the Alert
  Groups with their Teams and Destinations; the Team page lists its Destinations with their conditions.
- **C-26.AC-10** The owning Team of a Route adds nothing: a Route owned by "payments", without "Also to the Teams from
  the label", delivers an Alert Group of `team="payments"` only to its own Destinations. Turning "Also to the Teams from
  the label" off gives the final edit of C-11.FR-14 in each Team Destination that no other entry reaches.
- **C-26.AC-11** Raising the Minimum severity of a Route to critical records a Quiet `urgency_raised` with the reason
  "Minimum severity of the Route" on its open warning Alert Groups, removes no acknowledgement, and publishes them
  Quietly in the Route's Destinations limited to critical, without `destination_joined`.

## Related ADRs

ADR-0003, ADR-0004, ADR-0005, ADR-0018.

## Depends on

C-08 — Routes, Severity levels and the Group key preview; C-11 — delivery, Storms and added Destinations; C-25 — Teams
and the Teams of an Alert Group.

## Suggested story split

- **BE** — the Minimum severity and Urgent as critical, with the migrations and the expand step of the urgent mark and
  of "critical is Urgent", and the profile Paging; it does not need Teams and may come first.
- **BE** — Severity conditions on Route and Team Destinations, delivery to Teams, `destination_joined`.
- **BE** — the preview "Where alerts go".
- **FE** — the Route form, the Destinations of a Team, the preview, the delivery state by entry.
- **Docs** — routing to Teams and "What critical changes".
