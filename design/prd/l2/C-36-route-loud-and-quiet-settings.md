# C-36. Loud and Quiet settings of a Route

[L2 index](../L2.md) · Release: 0.2.x · UI: yes · Depends on: C-08, C-09, C-11, C-17, C-26, C-28, C-32

**Goal.** Each Route says which of the seven Loud events are Loud on it — a new Alert Group, new Alerts, a Reopen, an
ack timeout notice, the end of a Snooze, a rise to critical and the start of a Storm — pre-filled by its Route profile,
so that an Informational Route is Loud only for a new Alert Group while a Paging Route keeps the table of L1; and
whether new Alerts remove the acknowledgement of an Alert Group, off by default (ADR-0004, ADR-0005; journal D546).
Whom a Loud event mentions stays with each Destination (C-28).

Names in the form `area.setting` refer to rows of [defaults.md](../l1/defaults.md); the lifecycle events are rows of the
lifecycle event tables of [C-09.FR-22](../l1/C-09-alert-group-lifecycle.md#lifecycle-events), C-10.FR-15, C-17.FR-11
and the [L2 lifecycle event table](../l1/reference.md#lifecycle-events-added-in-l2). What journal D546 and the ADRs
left open — that the rows about the Owner stay Loud, which new Alerts remove the acknowledgement and how, and what
existing Routes get — the maintainer decided with the draft of this capability (#674).

## Scenarios

1. The Admin creates the Route "certificates" with the Informational profile. A new Alert Group is Loud, with the
   Mentions of its Destinations for a new Alert Group; the new Alerts that join it, its Reopens into firing and the end
   of its Snoozes reach the Thread Quietly, without Mentions.
2. The Route "databases" gathers dozens of Alerts in each Alert Group. The Admin turns "New Alerts" off: new Alerts are
   still listed in the Thread, batched as before, but without Mentions and, in Telegram, without a sound; ack timeout
   notices stay Loud.
3. On the Route "payments-critical" the Admin turns on "New Alerts remove the acknowledgement". Alice acknowledged
   `#812`; a new Alert of another service joins it. `#812` is firing without an Owner, a Loud Thread reply says "New
   Alerts arrived; the acknowledgement of @alice is removed" and mentions her, and the Route's Escalation chain starts
   over at its first step.
4. After the upgrade, every Route is Loud and Quiet exactly as before: all seven events Loud and new Alerts never
   removing an acknowledgement, whatever profile it was created with.
5. On a Route whose ack timeout is off, the form shows "Ack timeout" unavailable with "The ack timeout is off on this
   Route"; turning the ack timeout on ticks it.

## Functional requirements

### The settings

- **C-36.FR-1** A Route has two Loud and Quiet settings, Route policy fields like the others (C-08.FR-1): the Loud
  events that are Loud on it (`route.loud_events`) — any of `new_alert_group`, `new_alerts`, `reopen`, `ack_timeout`,
  `snooze_ended`, `rise_to_urgent` and `storm_started`, the values of the `MentionEvent` schema that a Destination's
  Mention entries use (C-28.FR-1) — and "New Alerts remove the acknowledgement" (`route.new_alerts_remove_ack`). A new
  Route takes them from its Route profile (FR-8); the API reads and writes them; edits apply at once (C-08.FR-8), use
  optimistic locking (C-08.FR-10) and are recorded in the Audit log with a before/after diff. A name outside the list
  fails request validation with `400`; a Loud event listed twice is refused with `422` and a pointer to the field.
- **C-36.FR-2** Each Loud event of `route.loud_events` governs these rows; every other row keeps the loudness of its
  table:

  | Loud event | Rows it governs |
  |---|---|
  | `new_alert_group` | `created`; `destination_joined` while firing (C-26.FR-5); the delivery event "Publication after a Broken Destination recovers" of a firing Alert Group ([reference.md](../l1/reference.md#loud-and-quiet)) |
  | `new_alerts` | `alerts_added` into a firing Alert Group; the Destinations' Mentions of `alerts_added` that removes the acknowledgement (FR-6) |
  | `reopen` | `reopened` into firing |
  | `ack_timeout` | `ack_timeout`; `notices_missed` when it stands for ack timeout notices |
  | `snooze_ended` | `snooze_ended` |
  | `rise_to_urgent` | `urgency_raised` that ends a Snooze; the Destinations' Mentions of `urgency_raised` that removes the acknowledgement |
  | `storm_started` | the first publication of a Storm summary (C-11.FR-6) |

  A row that mentions the Owner because it ends or returns their acknowledgement stays Loud on every Route and always
  mentions the Owner: `reopened` into acknowledged, `urgency_raised` that removes the acknowledgement and `alerts_added`
  that removes it (FR-6); its Loud event decides only whether the Destinations' entries for that event are mentioned
  too, as ADR-0004 says for the rise and the removal and C-28.FR-4 for the Reopen. `takeover`, `reminder`,
  `auto_unacknowledged`, `unacknowledged` when the Owner is disabled or deleted, `notices_missed` when it stands for
  Reminders and `paged` by Mention stay Loud on every Route: each is about one person, or comes from an Escalation chain
  that the Route or a Team names on purpose. `notices_missed` keeps one row and takes the loudness of the notices it
  stands for.
- **C-36.FR-3** A governed row whose Loud event the Route does not list is recorded with the loudness `quiet` and no
  Mentions, and appears in a messenger as the row says, Quietly: the same new Root message, Thread reply or Storm
  summary, with `disable_notification` in Telegram, without Mentions in Mattermost, Slack and Bridges (C-13.FR-8,
  C-24.FR-6, C-22.FR-6), and with `notify` false in outgoing webhook events (C-15.FR-2). A Destination's Mention
  entries for that event mention nobody there (C-28.FR-4) and stay as they are.
- **C-36.FR-4** The loudness of an event is decided when it is recorded, from the settings of the Alert Group's Route at
  that moment: a change applies to the events recorded after it, an event recorded before keeps its loudness also when
  delivery sends it later, and no change makes an edit Loud or sends anything again (ADR-0005). The Route that decides
  is the Alert Group's, also in the Destinations of its Teams (C-26.FR-2); a Storm summary takes the settings of its
  Route.
- **C-36.FR-5** Loudness changes nothing else: statuses, the Destinations an Alert Group reaches, the ack timeout,
  Reminders, Unclaimed, Storms and Escalation chains behave as without these settings. Chains start at the Publication
  and at the points of ADR-0004, whatever the loudness of the event, so a Quiet `created` still starts them, and Pages
  stay Loud.

### New Alerts remove the acknowledgement

- **C-36.FR-6** With `route.new_alerts_remove_ack` on, an Alert that newly fires in an acknowledged Alert Group of the
  Route — one the Alert Group never had, or one of its own firing again — removes the acknowledgement: the Alert Group
  becomes firing without an Owner, with the reason "New Alerts arrived". It records `alerts_added` in a row of its own,
  told apart by the status change it records: Loud, with the Mention `owner` (the Owner before the transition,
  C-09.FR-22), and `new_alerts` as well where the Route lists `new_alerts` (FR-2); Timeline kind `alerts`, like every
  row of `alerts_added`; in a messenger a Thread reply that gives the reason, names the Owner and lists the new Alerts,
  and a Root message update to firing with the buttons of that status. The reply opens the Thread batching window, and
  further new Alerts within it go into one reply when it closes (C-11.FR-4). A Replacement (C-09.FR-7), a Continuation
  (C-06.FR-11), the Alerts of a Reopen into acknowledged (C-09.FR-4), new Alerts in a snoozed Alert Group (C-09.FR-6)
  and new Alerts in an attached Alert Group, which follows its main Alert Group (C-37), leave the acknowledgement as it
  is; a Reopen keeps its Owner, as journal D151 and D625 decided. The removal is a row of its own of `alerts_added`, as
  `urgency_raised` records a rise that removes the acknowledgement in a row of its own, rather than a new event. The
  rise to Urgent is checked first: an Alert that makes the Alert Group Urgent while `route.urgent_rise_removes_ack` is
  on records `alerts_added` (acknowledged) and `urgency_raised` (removes), as in L1 (C-09.FR-9), and no `alerts_added`
  (removes); with `route.urgent_rise_removes_ack` off, the same Alert removes the acknowledgement as this requirement
  says. With `route.new_alerts_remove_ack` off, the rows of L1 stand: new Alerts in an acknowledged Alert Group are
  Quiet and the acknowledgement stays.
- **C-36.FR-7** The removal is a system transition of the dispatcher, with Muster as the actor and the Transport
  `system`, recorded in the Timeline and not in the Audit log (ADR-0016). It is a point at which the Alert Group becomes
  firing again (ADR-0004, C-17.FR-1, C-32.FR-5): the Escalation chains that apply start over at their first step, and
  where none applies the ack timeout starts over; the Reminders of the former Owner end and the count of unanswered
  Reminders for auto-unacknowledge goes with them (C-17.FR-5); the Alert Group can become Unclaimed again (C-17.FR-3,
  C-32.FR-7); the Assignees, cleared by the acknowledgement, are filled again by the Pages that follow (C-32.FR-12). The
  first acknowledgement and time to acknowledge do not change (C-09.FR-27). The next Acknowledge by anyone, the former
  Owner included, is an Acknowledge and not a Takeover, and an Acknowledge through a Notification gateway applies again,
  since nobody owns the Alert Group (C-30.FR-6).

### Profiles, upgrade and API

- **C-36.FR-8** The Route profiles give these values; they only pre-fill a new Route (C-08.FR-7):

  | Setting | Paging | Informational |
  |---|---|---|
  | `route.loud_events` | all seven | `new_alert_group` |
  | `route.new_alerts_remove_ack` | off | off |

  `route-profiles` returns both settings of each profile.
- **C-36.FR-9** `ack_timeout` keeps its stored value while the Route's ack timeout is off; the form shows it unavailable
  then, and turning the ack timeout on in the form ticks it, so that an Informational Route that gains an ack timeout
  does not send Quiet notices unless the Admin unticks it.
- **C-36.FR-10** The release that brings the settings adds them as columns in an expand step: its migration gives every
  existing Route, the Default route included, all seven Loud events and `route.new_alerts_remove_ack` off, which is the
  fixed table of L1, whatever profile the Route was created with — profiles are not stored, and guessing one from the
  ack timeout and Reminders being off would change behaviour on upgrade; the release notes suggest unticking events on
  Informational Routes. No Alert Group, Timeline entry or Root message changes. Both fields are optional in the input
  of a Route: a create without them takes the values of the Paging profile, and an update without them keeps the
  stored values, so that earlier clients keep working. During a rolling update, replicas of the earlier version record
  every event with the loudness of L1, whatever a Route edited meanwhile on a new replica says.
- **C-36.FR-11** The documentation's page on Loud and Quiet describes the settings, the table of FR-2 and the rows that
  stay Loud, the profiles, what Quiet means in each messenger ([reference.md](../l1/reference.md#loud-and-quiet)), and
  "New Alerts remove the acknowledgement" with what it starts over; the Route form links to it.

### Changes to shared tables, to L1 and to L2

[defaults.md](../l1/defaults.md#routing-and-lifecycle) lists `route.loud_events` and `route.new_alerts_remove_ack`;
the [L2 lifecycle event table](../l1/reference.md#lifecycle-events-added-in-l2) has the row `alerts_added` (removes),
and [Loud and Quiet](../l1/reference.md#loud-and-quiet) the revision of its fixed loudness; C-08.FR-1, C-08.FR-7,
C-09.FR-4, C-09.FR-6, C-09.FR-9, C-09.FR-22, C-15.FR-2, C-17.FR-1, C-17.FR-2 and C-17.FR-11 carry the revision of
[L2 §2](../L2.md#2-conventions), listed in [L2 §6](../L2.md#6-revisions-to-l1), and C-26.FR-5, C-28.FR-4 and C-32.FR-5
a dated revision. The story that implements this capability adds the new fields to `api/openapi.yaml` and a
migration, and updates the note on C-32.FR-5 in [coverage.md](../../stories/coverage.md).

## UI

Route form, section "Loud and Quiet": "Which events of this Route are Loud. A Quiet event is still delivered, without
Mentions and without a sound where the messenger allows it. Whom to mention is set on each Destination." One tick box
per Loud event — "New Alert Group", "New Alerts", "Reopen", "Ack timeout", "End of Snooze", "Rise to critical", "Start
of a Storm" — "Ack timeout" unavailable with "The ack timeout is off on this Route" while it is off (FR-9); the note
"Pages of Escalation chains, Reminders, Takeovers and the Mention of an Owner who loses an acknowledgement are always
Loud."; and the tick box "New Alerts remove the acknowledgement" with "An acknowledged Alert Group becomes firing
without an Owner when a new Alert joins it; its Escalation chains, or its ack timeout, start over." On creation the
profile fills the section. The Routes list needs no change.

## API surface

`loud_events` (a list of `MentionEvent`) and `new_alerts_remove_ack` in the policy of `routes`, optional in the input;
both on `route-profiles`; the `alerts_added` entry of the removal, with the status it changed, on
`alert-groups/{id}/timeline`.

## Acceptance

Checked with the fake Alertmanager, a virtual clock, the fake messenger servers and the recording test adapter of C-11.

- **C-36.AC-1** `route-profiles` returns Paging with all seven Loud events and Informational with `new_alert_group`,
  both with `new_alerts_remove_ack` false; a Route created from the Informational profile in the UI stores them.
- **C-36.AC-2** On a Route whose `loud_events` is `[new_alert_group]`, a new Alert Group records `created` Loud with
  `[new_alert_group]`; a new Alert joining it while it is firing records `alerts_added` with `loudness` `quiet` and no
  Mentions, and the fake Telegram server receives that Thread reply with `disable_notification`; a Reopen into
  acknowledged records `reopened` Loud with `[owner]`.
- **C-36.AC-3** For every row of FR-2, with its Loud event off the row is recorded Quiet without Mentions, or for a row
  about the Owner Loud with only `owner`, and with it on as its table says; `takeover`, `reminder`,
  `auto_unacknowledged` and `paged` stay Loud with their Mentions whatever `loud_events` holds; `notices_missed` is
  Quiet for ack timeout notices with `ack_timeout` off and Loud for Reminders (a table-driven test).
- **C-36.AC-4** Taking `reopen` off a Route leaves a `reopened` entry recorded before the save Loud, also when its
  Thread reply is sent after the save; the next Reopen into firing is Quiet; no Root message is edited or sent by the
  save.
- **C-36.AC-5** With `new_alerts_remove_ack` on, a new Alert joining an Alert Group acknowledged by Alice makes it
  firing without an Owner and records `alerts_added` (removes) of kind `alerts`, Loud with `[owner, new_alerts]`; the
  Thread reply mentions Alice, the Root message shows Ack again, the next ack timeout notice comes 15 minutes later, no
  Reminder follows, and the first acknowledgement is still Alice's. With `new_alerts` off as well, the same entry is
  Loud with `[owner]` only.
- **C-36.AC-6** With `new_alerts_remove_ack` on and a chain named on the Route, the removal starts the chain over at
  step 1; an Acknowledge by Alice afterwards records `acknowledged`, not `takeover`.
- **C-36.AC-7** With `new_alerts_remove_ack` on, a Replacement, a Continuation and a new Alert in a snoozed Alert Group
  leave the status as it was; a critical Alert joining an acknowledged warning Alert Group with
  `urgent_rise_removes_ack` on records `alerts_added` (acknowledged) and `urgency_raised` (removes) and no
  `alerts_added` (removes); with `urgent_rise_removes_ack` off it records `alerts_added` (removes).
- **C-36.AC-8** After the migration every existing Route reads all seven Loud events and `new_alerts_remove_ack` false,
  and the rows of the lifecycle event tables record exactly the loudness and Mentions they recorded before it; a Route
  update without the two fields keeps them, and a create without them gets the values of Paging; `loud_events` with an
  unknown name gets `400`, and with a name listed twice `422`.
- **C-36.AC-9** With `storm_started` off, the first publication of a Storm summary of the Route is Quiet and carries no
  Mentions; with `new_alert_group` off, `destination_joined` into a Team's Destination is Quiet.
- **C-36.AC-10** In a real browser, the Route form fills "Loud and Quiet" from the chosen profile, shows "Ack timeout"
  unavailable while the ack timeout is off and ticks it when the ack timeout is turned on, and saves a change that the
  Audit log shows as a diff, in English and in Russian.

## Related ADRs

ADR-0004, ADR-0005, ADR-0016.

## Depends on

C-08 — Routes, policy fields and profiles; C-09, C-17 — the lifecycle events and timers whose loudness the settings
govern; C-11 — delivery, Storms and batching; C-26 — Teams' Destinations and `destination_joined`; C-28 — Mention
entries per Loud event; C-32 — Escalation chains that the removal starts over.

## Suggested story split

- **BE** — the two settings with their migration, the profiles, the loudness of the governed rows, the removal of the
  acknowledgement by new Alerts with the starting over of chains and timers.
- **FE** — the section "Loud and Quiet" of the Route form.
- **Docs** — the page on Loud and Quiet.
