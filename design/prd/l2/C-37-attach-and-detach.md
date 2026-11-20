# C-37. Attach and Detach

[L2 index](../L2.md) · Release: 0.2.x · UI: yes · Depends on: C-09, C-10, C-11, C-17, C-28, C-29, C-30, C-32, C-36

**Goal.** When several Alert Groups stand for one problem, a person attaches them to one of them, their main Alert
Group: an attached Alert Group follows the main one's status, stays out of the way — its Escalation chains and timers
stop, its Root messages shrink to a line that names the main Alert Group, without buttons — and its Alerts are shown on
the main Alert Group's page; Detach lets it live on its own again at any time (ADR-0004, ADR-0016; journal D547, D556).
Several teams working on one problem together is a matter for Incidents in L3.

Names in the form `area.setting` refer to rows of [defaults.md](../l1/defaults.md); the lifecycle events are rows of the
[L2 lifecycle event table](../l1/reference.md#lifecycle-events-added-in-l2). This capability is a draft: the behaviours
that the recorded decisions do not settle carry the recommended answer and name their item of
[Open questions](#open-questions); that section and the list of changes to shared tables exist only while it is a
draft, and move into the decided requirements, or into `P-NN` items of
[L2 §5.3](../L2.md#53-provisional-values-and-behaviours), when it is adopted.

## Scenarios

1. A database primary fails. `#1287 PostgresPrimaryDown` fires on the Route "databases", and within two minutes
   `#1288 CheckoutLatencyHigh` and `#1290 PaymentsErrorRate` fire on the Route "payments". Ivan opens `#1288`, presses
   "Attach to #…", picks `#1287` and confirms; he does the same for `#1290`. Their Root messages in `payments-critical`
   now read "Attached to #1287 PostgresPrimaryDown", with a link and without buttons, and the payments chain stops
   Paging.
2. Ivan acknowledges `#1287` in Mattermost: `#1288` and `#1290` are acknowledged by him too. The page of `#1287` lists
   both under "Attached Alert Groups" and shows their Alerts.
3. Petr sees that `#1290` has a cause of its own and presses "Detach": `#1290` keeps Ivan as its Owner, its Root message
   shows its own status and buttons again, and its Reminders start for Ivan.
4. Ivan resolves `#1287` after the failover: `#1288`, still attached, is resolved with it; `#1290` is not.
5. A Viewer tries to attach an Alert Group and is refused; Ivan tries to attach `#1291` to `#1288`, which is attached
   itself, and is told to choose its main Alert Group `#1287` instead.

## Functional requirements

### Attach and Detach

- **C-37.FR-1** Attach and Detach are Commands of the dispatcher (ADR-0016): Attach puts an Alert Group under another
  one, its main Alert Group; Detach lets an attached Alert Group live on its own. Both are made from the UI and the
  API — later also through the MCP server — and never from messengers, which have no button and no Text command for
  them. They
  need the Permission `alert-groups:attach`, which Admins and Responders have ([open question 1](#open-questions)); a
  Service account may make them. Each has its own Audit log type, `alert_group.attached` and `alert_group.detached`,
  naming both Alert Groups, the actor and the Transport; `allowed_commands` (C-10.FR-16) lists `attach` and `detach`
  where they are allowed; `muster_commands_total{command}` gains `attach` and `detach`.
- **C-37.FR-2** Attach names the main Alert Group. A main Alert Group that does not exist in the Organization is `404`;
  otherwise Attach is refused with `409` and the code of the first of these checks that fails, in this order:

  | Code | When |
  |---|---|
  | `attach_to_itself` | the main Alert Group is the Alert Group itself |
  | `already_resolved` | the Alert Group is resolved |
  | `already_attached` | the Alert Group is attached already; it is detached first |
  | `has_attached_alert_groups` | other Alert Groups are attached to the Alert Group |
  | `main_resolved` | the main Alert Group is resolved |
  | `main_is_attached` | the main Alert Group is itself attached; the refusal names its main Alert Group |
  | `attach_limit_reached` | the main Alert Group has `alert_group.attach_max` attached Alert Groups already |

  So attachment is one level deep and has no cycles ([open question 2](#open-questions)). An Alert Group may be attached
  to an Alert Group of any Route and Integration of the Organization. Detach of an Alert Group that is not attached is
  refused with `409` `not_attached`.
- **C-37.FR-3** Every Command and system transition that touches a main Alert Group or an attached one locks the main
  Alert Group first and then its attached Alert Groups in the order of their ids, and following (FR-4) runs in the
  transaction of the change of the main Alert Group, so that Commands on the two stay serialized (C-10.FR-10) and two
  Attaches never cross.
- **C-37.FR-4** Attach makes the Alert Group follow its main Alert Group at once: it takes the main Alert Group's status
  with its Owner and Snooze end ([open question 3](#open-questions)). Its running Escalation chains stop, so no later
  step is made, a queued call through a gateway is not made (C-30.FR-4) and the runs of Notification rules for its Pages
  stop (C-31.FR-3); its ack timeout, Reminders, the end of its Snooze and its count of unanswered Reminders stop. Its
  Assignees and its Unclaimed mark change only as its status does: taking an acknowledged, snoozed or resolved status
  clears the Assignees (C-32.FR-12) and ends Unclaimed (C-17.FR-3), while an Alert Group that stays firing keeps them
  as they are. The Alert Group records `attached` and the main Alert Group `alert_group_attached`, both with the person
  who attached it.
- **C-37.FR-5** While it is attached, every change of the main Alert Group's status is applied to the Alert Group as a
  system transition of the dispatcher, with Muster as the actor and the Transport `system` (ADR-0016), recording
  `status_followed` with the new status, the person or system change behind it and the main Alert Group: Acknowledge
  and a Takeover (with the same Owner), Unacknowledge, Snooze and Unsnooze, the end of a Snooze, auto-unacknowledge, a
  rise or new Alerts that remove the acknowledgement (C-09.FR-9, C-36.FR-6) and an Owner disabled or deleted. Following
  never starts the attached Alert Group's own Escalation chains, ack timeout or Reminders, whatever status it takes; the
  main Alert Group's run for it. Following is not in the Audit log; the Command on the main Alert Group is. Following an
  acknowledgement sets the attached Alert Group's first acknowledgement, when it has none, to the main Alert Group's
  Owner at that moment (C-09.FR-27; [open question 9](#open-questions)).
- **C-37.FR-6** An attachment ends when either Alert Group resolves ([open question 4](#open-questions)):
  - the main Alert Group resolved by a person: each attached Alert Group is resolved with it, recording
    `status_followed` with that person as the resolver; Alerts of it still firing are grouped again after the Route's
    Grace period, as after any manual resolve (C-09.FR-5);
  - the main Alert Group resolved by the system: every attached Alert Group still has firing Alerts, since one whose
    Alerts are gone has resolved already, so each is detached by the system, recording `detached` with the reason
    "#{M} resolved by itself", and lives on its own as after a Detach (FR-8);
  - an attached Alert Group whose own Alerts all resolve: it is resolved by the system as any other (C-09.FR-3), and
    the main Alert Group records `alert_group_detached`.

  An Unresolve or a Reopen of either Alert Group never attaches again; a Reopen of a formerly attached Alert Group is
  one of its own (C-09.FR-4). An Alert Group that the end of an attachment leaves resolved is edited to the Root message
  of its resolved status, with "Resolved with #{M}" when it followed the main Alert Group's resolve.
- **C-37.FR-7** While it is attached, an Alert Group still takes its own Alerts by its Route and Group key (C-09.FR-3)
  and its Severity level and Teams change with them, but none of that changes its status: a rise to critical ends no
  Snooze and removes no acknowledgement, and new Alerts remove none. Every lifecycle event recorded on it while it is
  attached is Quiet and carries no Mentions, the Owner's included, whatever its row and its Route's Loud and Quiet
  settings say (C-36.FR-2), and appears in a messenger only as an update of its line (FR-10), apart from the Thread
  replies of `attached` and `detached` ([open question 5](#open-questions)); a Destination it reaches meanwhile receives
  the line Quietly, and its `destination_joined` is Quiet. The main Alert Group's Severity level, Urgent flag, Teams,
  Destinations and Escalation chains do not take the attached Alert Groups' Alerts into account
  ([open question 6](#open-questions)).
- **C-37.FR-8** Detach lets the Alert Group live on its own again with the current state of its own Alerts, which still
  fire, since one whose Alerts are gone has resolved already (FR-6). It keeps the status, Owner and Snooze end it
  followed last ([open question 7](#open-questions)). Kept firing, it is a point at which it becomes firing again
  (C-17.FR-1, C-32.FR-5): its Escalation chains that apply start over at their first step, and where none applies its
  ack timeout starts over. Kept acknowledged, its Reminders start for the Owner from the Detach; kept snoozed, its
  Snooze ends at the end it kept. The Alert Group records `detached` and the main Alert Group `alert_group_detached`,
  both with the person.
- **C-37.FR-9** While an Alert Group is attached, the Commands Acknowledge, Unacknowledge, Resolve, Snooze and Unsnooze
  on it, from any Transport, are refused with `409` `alert_group_attached`, whose message names the main Alert Group:
  "#{N} follows #{M}; act on #{M}, or detach it first" ([open question 8](#open-questions)). Add Note and Detach stay
  allowed. A bulk command refuses such an item with that outcome and goes on with the others (C-10.FR-14). An
  acknowledgement reported by a Notification gateway for an attached Alert Group is answered as one for an Alert Group
  someone owns (C-30.FR-6).

### Messages

- **C-37.FR-10** In each of its Destinations, the Root message of an attached Alert Group is edited Quietly to one line
  in the Route's language — "Attached to #{M} {title of the main Alert Group}" — with a link to the main Alert Group's
  page in Muster and without buttons; the `layout` sent to bridges carries the same line (C-29.FR-8), and a Bridge Root
  message past its edit window is republished as on a change of status (C-22.FR-9). Attach also sends a Quiet Thread
  reply: "Attached to #{M} by @{login}; it follows the status of #{M}." On Detach the Root message is edited back to the
  layout of its status with its Destination's buttons (C-28.FR-8), and a Quiet Thread reply says "Detached from #{M} by
  @{login}" or gives the system's reason. Reminders and ack timeout notices of an attached Alert Group are not sent.
- **C-37.FR-11** The Root messages of a main Alert Group show, Quietly, the line "Attached: #1288, #1290" with up to
  five numbers and "+{K} more", without links ([open question 5](#open-questions)); its buttons act on the main Alert
  Group and, through it, on the attached ones, and the private answer to a press says so: "Acknowledged #1287 and 2
  attached Alert Groups".

### The UI, statistics and webhooks

- **C-37.FR-12** The Alert Group page of an attached Alert Group shows the notice "This Alert Group is attached to #{M}
  and follows its status." with the link and Detach, and offers no other command but Add Note. The page of a main
  Alert Group has a section "Attached Alert Groups" — number, title, Route, status, firing and total Alerts, and Detach
  for each — and its Alerts section lists the Alerts of each attached Alert Group under its number, after its own. The
  dialog "Attach to #…" searches the open Alert Groups by number and title, offers only those that may be a main Alert
  Group, and says before confirming what changes: the status it will take, the Escalation chains that stop, and, when
  the Alert Group is critical and the main Alert Group is not, "#{N} is critical and #{M} is not: its Escalation chains
  for critical stop."
- **C-37.FR-13** The Alert Group list shows an attached Alert Group with the mark "→ #{M}" and gains the filter
  "Attached" — all (the default), only attached, or none attached — kept in the URL; the main Alert Group shows how many
  are attached to it. The list offers Attach as a bulk command (C-10.FR-14): "Attach to #…" for the selected Alert
  Groups, each through the dispatcher with its own outcome ([open question 10](#open-questions)).
- **C-37.FR-14** The statistics leave out an Alert Group resolved by following its main Alert Group's resolve (FR-6): it
  stood for the problem of its main Alert Group, which is counted. One that was detached, or resolved on its own while
  attached, counts as any other ([open question 9](#open-questions)). The summary row keeps the main Alert Group of an
  Alert Group resolved with it, and the Alert Group page shows "Resolved with #{M}".
- **C-37.FR-15** Attach and Detach record these rows, with the columns and meaning of C-09.FR-22; outgoing webhooks in
  events mode receive them one to one (C-15.FR-2), and the `alert_group` of every event gains `attached_to`, the
  `public_id` and number of its main Alert Group, or null:

  | Event | When | Loud or Quiet | Mentions | Timeline kind | In a messenger (C-11) |
  |---|---|---|---|---|---|
  | `attached` | the Alert Group is attached (FR-4) | Quiet | — | `status` | Root message edited to the line, and a Thread reply |
  | `alert_group_attached` | an Alert Group is attached to this one | Quiet | — | `system` | Root message update |
  | `status_followed` | an attached Alert Group takes a new status of its main Alert Group (FR-5, FR-6) | Quiet | — | `status` | update of its line; on a resolve, the Root message of its resolved status |
  | `detached` | the Alert Group is detached by a person, or by the system (FR-6, FR-8) | Quiet | — | `status` | Root message edited back to its status, and a Thread reply |
  | `alert_group_detached` | an Alert Group attached to this one is detached, or resolves on its own (FR-6) | Quiet | — | `system` | Root message update |

- **C-37.FR-16** At most `alert_group.attach_max` Alert Groups are attached to one main Alert Group
  ([open question 11](#open-questions)), so that a Command on it changes a bounded number of Alert Groups.
- **C-37.FR-17** The documentation's page on Attach and Detach says when to attach and when Incidents of L3 will fit
  better, what an attached Alert Group does and does not do, what Detach returns to, and how statistics count; the
  dialog and the notice link to it.

### Changes to shared tables, to L1 and to L2

The story that implements this capability, in the same pull request:

- adds the rows of FR-15 to the [L2 lifecycle event table](../l1/reference.md#lifecycle-events-added-in-l2), with the
  rule of FR-7 that every event of an attached Alert Group is Quiet;
- adds `alert-groups:attach` to [Permissions added in L2](../l1/reference.md#permissions-added-in-l2) and then to the
  table of Roles and Permissions, and rewords "attaching an Escalation chain" there to "naming", since Attach is now a
  term of its own;
- marks the row of `muster_commands_total` in the [metrics catalogue](../l1/reference.md#metrics-catalogue) "from L2"
  with `attach` and `detach`; adds to [Buttons and links by status](../l1/reference.md#buttons-and-links-by-status) that
  an attached Root message has no buttons, and to [Banners, warnings and
  notices](../l1/reference.md#banners-warnings-and-notices) the notice of FR-12;
- adds `alert_group.attach_max` to [defaults.md](../l1/defaults.md#routing-and-lifecycle);
- marks with the revision of [L2 §2](../L2.md#2-conventions) C-09.FR-1, C-09.FR-3, C-09.FR-13, C-09.FR-14, C-09.FR-15,
  C-09.FR-28, C-10.FR-1, C-10.FR-2, C-10.FR-14, C-10.FR-16, C-15.FR-2 (`attached_to`), C-17.FR-1 and C-17.FR-3, and
  adds their row to [L2 §6](../L2.md#6-revisions-to-l1);
- adds a dated revision to C-30.FR-6, C-32.FR-5, C-32.FR-6, C-32.FR-7 and C-32.FR-12, for Attach, which stops the
  chains, and Detach, which is a point of starting over.

## UI

Alert Group page: "Attach to #…" among the commands, opening the dialog of FR-12 ("Attach #{N} to the main Alert Group",
search "Number or title", the changes listed, "Attach"); on an attached Alert Group the notice of FR-12 with "Detach";
on a main Alert Group the section "Attached Alert Groups" with Detach per row. Alert Group list: the mark "→ #{M}", the
filter "Attached", the count of attached Alert Groups on a main one, and "Attach to #…" among the bulk commands.
Everything works at phone width (C-09.FR-24).

## API surface

`alert-groups/{id}/attach` (create: the main Alert Group's id) and `alert-groups/{id}/detach` (create);
`attached_to` and the count of attached Alert Groups on `alert-groups`, with the filter `attached` (`any`, `only`,
`none`) and `attached_to={id}`; `attach` in `alert-groups/bulk-commands` with the main Alert Group as its argument;
`attach` and `detach` in `allowed_commands`; the events of FR-15 on `alert-groups/{id}/timeline` and in the outgoing
webhook event schema.

## Acceptance

Checked with the fake Alertmanager, a virtual clock, the fake messenger servers and the recording test adapter of C-11.

- **C-37.AC-1** On the setup of scenario 1, attaching `#1288` to `#1287` records `attached` on `#1288` and
  `alert_group_attached` on `#1287`, one Audit log entry `alert_group.attached`, and edits the Root message of `#1288`
  in the fake Mattermost server to "Attached to #1287 PostgresPrimaryDown" with a link and no buttons, Quietly, with a
  Quiet Thread reply.
- **C-37.AC-2** A firing `#1288` with a running chain, attached to a firing `#1287`: no later step of its chain is
  recorded, its Assignees stay as the chain left them, and no ack timeout notice comes for it with the clock advanced
  by an hour; once `#1287` is acknowledged, the Assignees of `#1288` are cleared with `assignees_changed`.
- **C-37.AC-3** Acknowledge, a Takeover, Snooze and Unsnooze of `#1287` through the API each record `status_followed`
  on `#1288` with the same status and Owner, and no Audit log entry for `#1288`; Unacknowledge of `#1287` makes
  `#1288` firing without an Owner and starts none of its chains.
- **C-37.AC-4** Each refusal of FR-2 returns `409` with its code, and a request that fails two checks gets the first;
  a Viewer gets `403`; Acknowledge of an attached Alert Group returns `409` `alert_group_attached`, and a bulk
  Acknowledge with it refuses that item and acknowledges the others.
- **C-37.AC-5** Resolving `#1287` by hand resolves `#1288` with the same resolver, ends the attachment and edits its
  Root message to "Resolved with #1287"; when the system resolves `#1287` while an Alert of `#1290` still fires, `#1290`
  records `detached` with the reason, is firing, and its chain starts at step 1; an attached Alert Group whose own
  Alerts resolve is resolved by the system, and `#1287` records `alert_group_detached` and no longer lists it.
- **C-37.AC-6** Detaching `#1290` while `#1287` is acknowledged by Ivan keeps `#1290` acknowledged by Ivan, edits its
  Root message back with Unack, Resolve and Snooze, and its first Reminder comes 4 hours after the Detach.
- **C-37.AC-7** A new critical Alert joining an attached, acknowledged warning Alert Group records Quiet entries only,
  makes no Thread reply, and leaves it acknowledged; the Severity level of the main Alert Group stays as it was. When
  the critical Alert brings it into a new Destination, that Destination receives the line Quietly.
- **C-37.AC-8** A press of Ack on the Root message of `#1287` acknowledges `#1288` too, and the private answer says
  "Acknowledged #1287 and 1 attached Alert Group"; the Root message of `#1287` shows "Attached: #1288".
- **C-37.AC-9** Every row of FR-15 records exactly one Timeline entry with its `event`, kind, `loudness` and `mentions`,
  and an events-mode outgoing webhook receives each with `attached_to` set (a table-driven test).
- **C-37.AC-10** On a statistics period with `#1287` and `#1288` resolved together by hand while attached, By Route
  counts one Alert Group and By people credits Ivan once; after a Detach and a later resolve, `#1288` counts on its own.
- **C-37.AC-11** A bulk Attach of three Alert Groups to `#1287`, one of them resolved, attaches two and refuses one with
  `already_resolved`; with `alert_group.attach_max` reached, the next Attach gets `attach_limit_reached`.
- **C-37.AC-12** In a real browser, Ivan attaches a critical Alert Group to a warning one through the dialog, which
  warns that its chains for critical stop; the page of the main Alert Group lists it with its Alerts, the list filter
  "only attached" shows it with "→ #1287", and Detach brings its commands back, in English and in Russian, 360 CSS
  pixels wide without horizontal scrolling of the page.

## Open questions

The recorded decisions (journal D547, D556, ADR-0004, ADR-0016) settle that an attached Alert Group follows the main
one's status — its Acknowledge, Snooze and Resolve apply to it — that its chains and timers stop, that its Root messages
shrink to a line with a link and no buttons, that its Alerts are shown on the main page, that Detach is possible at any
time and returns it to the current state of its own Alerts, that both are made from the UI and the API, that following
is a system transition, and the terms. These are not settled; the requirements above follow the recommendation, to be
confirmed before the stories.

1. **Which Permission?** Recommendation: a new `alert-groups:attach` for Admins and Responders, as each kind of
   Command has its own; reusing `alert-groups:acknowledge` would give the same Roles today but not after custom Roles.
2. **Which Alert Groups may be attached?** Recommendation: any open Alert Group to any open one of the Organization,
   across Routes and Integrations, since one problem often fires on several Routes; one level deep — a main Alert Group
   cannot be attached, an Alert Group with attached ones cannot be attached, and moving needs a Detach first — so there
   are no cycles and no chains of attachment to follow.
3. **What status does an Alert Group take on Attach?** Recommendation: the main Alert Group's status, Owner and Snooze
   end at once, as "follows" says, even when that ends its own acknowledgement or Snooze; the dialog shows it before
   confirming. Its Assignees and Unclaimed mark then change only as that status changes them, as `CONTEXT.md` defines
   both, so no glossary change is needed.
4. **What happens when an Alert Group of the pair resolves?** Recommendation: the attachment ends. A resolve of the
   main Alert Group by a person resolves the attached ones with the same resolver; a resolve by the system detaches
   them, since their Alerts still fire and the system never resolves an Alert Group with firing Alerts (C-09.FR-3); an
   attached Alert Group whose own Alerts are gone resolves by itself. An Unresolve brings nothing back together.
5. **What do the messages show?** Recommendation: every event of an attached Alert Group is Quiet, its new Alerts and
   rises included, and its Thread gets only the replies of Attach and Detach; the main Alert Group's Root messages get
   the line "Attached: #…", and nothing is written in its Thread, to keep it for the problem itself. The attached line
   links to the main Alert Group's page in Muster rather than to its Root message, which may be in another Destination.
   The cost is that a new serious Alert in an attached Alert Group is Loud for nobody; the main page lists it.
6. **Do attached Alerts raise the main Alert Group?** Recommendation: no — the main Alert Group's Severity level, Urgent
   flag, Teams, Destinations and chains stay its own; attaching a critical Alert Group to one that is not critical is
   allowed and the dialog warns that its chains for critical stop.
7. **What does Detach return to?** Recommendation: the status, Owner and Snooze end it followed last, so that whoever
   handles the problem keeps it; when firing, its chains or its ack timeout start over.
8. **May commands be made on an attached Alert Group?** Recommendation: only Add Note and Detach; the others are refused
   with `alert_group_attached`, since a status set on it would be overwritten by the next change of the main one.
9. **How do statistics and the first acknowledgement count?** Recommendation: leave out an Alert Group resolved by
   following its main Alert Group, from the counts, the times and By people, so that one problem is counted once and
   one acknowledgement is credited once (C-09.FR-27, C-09.FR-28); following an acknowledgement still sets the first
   acknowledgement, to the main Alert Group's Owner, so that an Alert Group detached later has one and does not count
   as "Unknown".
10. **Is there a bulk Attach and a list filter?** Recommendation: yes to both — an outage creates many Alert Groups at
    once, and the Open tab should be able to hide those that follow another.
11. **Is there a limit of attached Alert Groups?** Recommendation: `alert_group.attach_max`, 100, built in, the same as
    `alert_group.bulk_max`, since every Command on the main Alert Group changes each attached one in its transaction.

## Related ADRs

ADR-0004, ADR-0005, ADR-0016, ADR-0018.

## Depends on

C-09 — the state machine, the list and the page; C-10 — Commands, refusals and bulk commands; C-11 — delivery of the
line and the Thread replies; C-17 — the ack timeout and Reminders that stop and start again; C-28 — the buttons of a
Destination; C-29 — the layout of Root messages and bridges; C-30 — acknowledgements through gateways; C-32 —
Escalation chains and Assignees that stop and start over; C-36 — the loudness of the rows that FR-7 overrides.

## Suggested story split

- **BE** — the Commands Attach and Detach with their refusals, Permission and limit, following the main Alert Group,
  the end of the attachment on resolve, the stopping and starting over of chains and timers, the lifecycle events, the
  filter and bulk Attach, statistics.
- **BE** — the line of an attached Alert Group and the line "Attached" of the main one in every messenger and in the
  bridges' `layout`, with the Thread replies.
- **FE** — the dialog, the notice, the section of attached Alert Groups, the list mark and filter, bulk Attach.
- **Docs** — the page on Attach and Detach.
