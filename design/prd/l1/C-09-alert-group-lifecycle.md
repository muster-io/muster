# C-09. Alert Group lifecycle

[L1 index](../L1.md) · Stage: Observe · UI: yes · Depends on: C-08

**Goal.** Alert Groups follow the four-status state machine with predictable automatic transitions and visible reasons,
and people manage them in a full web UI: a live list of current Alert Groups with filters and search, a detail page with
the Alerts and the Timeline, past Alert Groups and statistics — on a desktop and on a phone.

Names in the form `area.setting` refer to rows of [defaults.md](defaults.md). Every change records a lifecycle event
with its loudness and Mentions (FR-22); Destinations receive the events from C-11 on (see the
[conventions](../L1.md#2-conventions)).

## Scenarios

1. A new Alert creates `#412`, Firing; it appears at the top of the open Alert Group list without a reload.
2. All Alerts of a firing Alert Group resolve; eight minutes later one returns. The Alert Group reopens with its Reopen
   count at 1, and the Timeline says why.
3. An exporter restarts and reports the same problem with a new `pod` label; the Timeline records a Replacement naming
   `pod`, and the page suggests removing Instance labels from the rule.
4. In a Route whose Group key lacks `severity`, an Alert rises from warning to critical; the Alert Group becomes Urgent
   and the Timeline records the rise.
5. A responder on duty opens Alert Groups, picks "Firing" and "Urgent", searches for "postgres", opens `#415`, reads its
   Alerts, labels and Timeline, and follows the link to the previous Alert Groups with the same key.
6. A team lead filters past Alert Groups by `namespace="payments"` over the last 30 days and compares time to
   acknowledge between Routes on the statistics page.
7. The Admin tries to delete a Route with open Alert Groups; Muster refuses and offers to move them to the Default
   route.
8. At night a responder opens the link from a message on a phone; the Alert Group page fits the screen and shows the
   status, the Alerts and the Timeline without sideways scrolling.

## Functional requirements

### State machine

- **C-09.FR-1** An Alert Group has one of four statuses — `firing`, `acknowledged` (with its Owner), `resolved` (by a
  user, or by the system with a reason), `snoozed` (until a time, or with no end) — and attributes: number `#N`,
  `public_id`, Route, Integrations, title and `summary` (FR-23), Severity level, start time, Urgent flag, Reopen count,
  counts of firing and resolved Alerts, the time of the last change, and — filled by later capabilities — Owner (C-10),
  Snooze end and who set it (C-10) and the Unclaimed flag (C-17).
- **C-09.FR-2** `#N` is assigned per Organization without gaps in the transaction that creates the Alert Group;
  `public_id` is opaque and used in URLs, the API and signed buttons.
- **C-09.FR-23** The title of an Alert Group is the `alertname` its Alerts share or, once Alerts with different
  `alertname` values have joined it, its Group key values (`cluster=prod, namespace=payments`). It is set when the Alert
  Group is created and changes at most once, at that switch; Alerts resolving never change it. The `summary` of an Alert
  Group is the `summary` annotation of the first of its Alerts that has one, set once. Templates never change either: a
  Root message template may render a heading of its own (C-12), but the list, search, the Alert Group page, statistics
  and outgoing webhook bodies use the stored title and `summary`, and both stay in the summary row (FR-16).
- **C-09.FR-3** Within its Route, a newly firing Alert joins the open Alert Group with the same Group key values,
  reopens a system-resolved one with those values (FR-4), or starts a new one. The system resolves an Alert Group when
  all its Alerts are resolved — explicitly by Alertmanager, as Gone, as Stale, or because their Integration was deleted
  — with the reason of the last one. A resolution by Alertmanager always wins and ends a Snooze.
- **C-09.FR-4** A Reopen is keyed by the Route and the Group key values, not by the Alert: if any Alert with the same
  Group key values fires on the same Route within the Route's Reopen window (`route.reopen_window`) after the system
  resolved the Alert Group — one of its own Alerts returning or an Alert it never had — the same Alert Group reopens,
  its Reopen count grows, and it returns to its previous status: acknowledged — same Owner, a Loud event mentioning only
  the Owner, Reminders continue (an Owner disabled or deleted meanwhile has no acknowledgements, so it reopens as
  firing, C-03.FR-13); snoozed with the end still ahead — snoozed again, Quiet; firing, or a Snooze that ended
  meanwhile — firing, a Loud event, and the ack timeout starts over.
- **C-09.FR-5** After a person resolves an Alert Group (C-10), no Reopen window applies. The Alert Group shows how many
  Alerts were still firing in the last Snapshot. If those Alerts are still reported firing when the Route's Grace period
  (`route.grace_period`) ends, they are grouped again on the Alert Group's Route as if they had just fired (FR-3): they
  join the open Alert Group with the same Group key values — such as one that a new Alert with the same key started
  within the Grace period — or reopen a system-resolved one within its Reopen window; otherwise a new Alert Group
  starts, marked "firing again after a manual resolve of #N". Within the Grace period, an Alert that fires again after a
  `resolved`, or a new Alert with the same key, starts a new Alert Group at once. A Continuation is never a new
  firing.
- **C-09.FR-6** New Alerts in an existing Alert Group: firing — a Loud event, batched into Thread replies by delivery
  (C-11); acknowledged — a Quiet event (Thread reply and Root message update), and the acknowledgement always stays;
  snoozed — a Quiet Root message update only, and what accumulated is listed when the Snooze ends.
- **C-09.FR-7** A new Alert that differs from a firing Alert of the same Alert Group only in Instance labels
  (`organization.instance_labels`) is a Replacement: a Quiet event names the label that differs, the old Alert going
  away is Quiet too, and the Alert Group page suggests fixing the source (for example `without(pod, instance)` in the
  rule). Fingerprints stay distinct.
- **C-09.FR-8** When a Snooze ends while Alerts still fire, the Alert Group becomes firing without an Owner, a Loud event
  says so and lists what accumulated, and the ack timeout starts over.
- **C-09.FR-9** When a rise in Severity level makes the Alert Group Urgent, it ends a Snooze — unless the Snooze was set
  while the Alert Group was already Urgent — and removes the acknowledgement when `route.urgent_rise_removes_ack` is on:
  the Alert Group becomes firing, a Loud event gives the reason and mentions the Owner, and the ack timeout starts over.
  A rise that leaves urgency unchanged, and any configuration edit, never removes an acknowledgement or a Snooze. The
  acceptance of this requirement needs Acknowledge and Snooze and is in C-10 (C-10.AC-12, C-10.AC-13).
- **C-09.FR-10** Every automatic status change carries its reason, shown on the Alert Group page, in the Timeline and,
  from C-11 on, in the Root message or Thread.
- **C-09.FR-11** The Timeline of an Alert Group shows, in order: Alerts added, resolved (with the reason), replaced and
  continued; Static label conflicts; status changes with the actor and Transport; Reopens; Notes; Takeovers; ack timeout
  notices, Reminders and their answers; Fallback template use; delivery events (Publication, possible duplicate, Not
  delivered, delivered late, deleted in the messenger, Thread not attached); moves to the Default route; Muster's
  downtime periods. Delivery events are not Timeline entries: delivery keeps them in a table of its own, outside the
  Alert Group tables, and the Timeline merges them with its entries by time as the kind `delivery` (C-11.FR-21). Each
  later capability adds its entry types. Every entry has a kind (FR-14); an entry that records a lifecycle event also
  carries the event name, its loudness and its Mentions (FR-22).
- **C-09.FR-12** Snooze ends, Reopen window ends and Grace period ends are timer rows with deadlines that any replica
  may claim; after downtime each overdue timer fires once.
- **C-09.FR-18** After downtime (C-02.FR-12), every open Alert Group gets a Timeline entry "Muster was unavailable from
  … to …".
- **C-09.FR-19** Deleting a Route with open Alert Groups is refused with their count. The UI offers to move them to the
  Default route, which adds a Timeline entry to each; the Route can be deleted afterwards. A moved Alert Group keeps its
  Group key values but takes no new Alerts and never reopens: it stays open until its Alerts resolve or a person
  resolves it, while new Alerts on the Default route are grouped by the Default route's own Group key.

### Lifecycle events

- **C-09.FR-22** Every transition and change of an Alert Group records the lifecycle event that the lifecycle event
  tables — the one below, C-10.FR-15 and C-17.FR-11 — give for it: one Timeline entry of the row's kind that carries the
  row's `event`, `loudness` (`loud` or `quiet`) and `mentions`. The tables are the whole contract: lifecycle events are
  the only input of delivery (C-11.FR-20) and of outgoing webhook events (C-15.FR-2), and the last column says how
  delivery shows each event from C-11 on. Timeline entries that are not lifecycle events — Fallback template use (C-12)
  and Muster's downtime periods (FR-18) — and the delivery events the Timeline shows (C-11.FR-21) reach neither; Static
  label conflicts are part of the `alerts_added` entry. Mentions are symbolic: `owner`, `previous_owner`, or the name of
  a Destination setting of C-12.FR-8 (`new_alert_group`, `new_alerts`, `reopen`, `ack_timeout`, `snooze_ended`,
  `rise_to_urgent`), which each Destination turns into real Mentions from C-12 on; "—" means none. `owner` is the Owner
  as it was **before** the transition the event records — in `urgency_raised` and `auto_unacknowledged` (C-17.FR-11) the
  Owner who loses the Alert Group, in a Reopen into acknowledged the Owner it returns to; `previous_owner` is used only
  by `takeover` (C-10.FR-15), whose actor is the new Owner.

  | Event | When | Loud or Quiet | Mentions | Timeline kind | In a messenger (C-11) |
  |---|---|---|---|---|---|
  | `created` | a new Alert Group starts (FR-3, FR-5) | Loud | `new_alert_group` | `status` | new Root message |
  | `alerts_added` | new Alerts join a firing Alert Group | Loud | `new_alerts` | `alerts` | Thread reply, batched (C-11.FR-4) |
  | `alerts_added` | new Alerts join an acknowledged Alert Group | Quiet | — | `alerts` | Thread reply and Root message update |
  | `alerts_added` | new Alerts join a snoozed Alert Group | Quiet | — | `alerts` | Root message update; listed when the Snooze ends |
  | `alert_replaced` | a Replacement (FR-7) | Quiet | — | `alerts` | Thread reply |
  | `alert_resolved` | an Alert resolves while others still fire, the old Alert of a Replacement included | Quiet | — | `alerts` | Root message update |
  | `alert_continued` | a Continuation (C-06.FR-11) | Quiet | — | `alerts` | Root message update where shown values change |
  | `annotations_changed` | an Alert's annotations change (C-06.FR-12) | Quiet | — | `alerts` | Root message update where shown values change |
  | `severity_raised` | the Severity level rises and urgency stays the same | Quiet | — | `alerts` | Root message update |
  | `urgency_raised` | a rise makes the Alert Group Urgent and ends a Snooze or removes the acknowledgement (FR-9) | Loud | `owner` (the Owner before the rise), `rise_to_urgent` | `status` | Thread reply with the reason |
  | `urgency_raised` | a rise makes the Alert Group Urgent and removes nothing | Quiet | — | `status` | Root message update |
  | `reopened` | a Reopen into firing (FR-4) | Loud | `reopen` | `status` | Thread reply |
  | `reopened` | a Reopen into acknowledged | Loud | `owner` only | `status` | Thread reply |
  | `reopened` | a Reopen into snoozed | Quiet | — | `status` | Root message update |
  | `snooze_ended` | a Snooze runs out while Alerts fire (FR-8) | Loud | `snooze_ended` | `status` | Thread reply listing what accumulated |
  | `resolved` | the system resolves the Alert Group (FR-3) | Quiet | — | `status` | Root message update and a Thread reply with the reason |
  | `moved_to_default_route` | its Route is deleted (FR-19) | Quiet | — | `system` | as a Destination removed from or added to a Route (C-11.FR-14) |
  | `unacknowledged` | its Owner is disabled or deleted (C-03.FR-13): firing without an Owner, the reason `owner_disabled` or `owner_deleted` | Loud | — | `status` | Thread reply with the reason and Root message update |

### Alert Group list

- **C-09.FR-13** The Alert Group list is the main working page of the UI:
  - **Status tabs** — Open (the default: firing, acknowledged and snoozed), Firing, Acknowledged, Snoozed, Resolved and
    All — each with the count of Alert Groups matching the other filters.
  - **Filters** — Route, Integration, Severity level, Urgent, resolved by (a user, or the system with its reason),
    Reopened (count above zero) and labels as Matchers in Alertmanager syntax (`namespace="payments"`,
    `pod=~"api-.*"`), all combinable; later capabilities add Owner and "snoozed with no end" (C-10), "Delivery problem"
    (C-13) and Unclaimed (C-17). Label Matchers are matched against the Alert Group's common labels — the labels every
    Alert of the group has with the same value; a label its Alerts do not share counts as absent. This is the rule that
    also works on summary rows after the Alerts are removed (FR-16).
  - **Time range** — presets (last hour, 24 hours, 7 days, 30 days) and a custom range; default
    `alert_group.list_range`. The range selects Alert Groups whose lifetime overlaps it — started before its end and
    not resolved before its start — so an Alert Group that has been firing for weeks is always in the default view.
  - **Search** by `#N` and by text in the title or the `summary` (FR-23), case-insensitive and matching parts of words.
    A search by number is a filter of its own (`number`) that ignores the time range, so an old `#N` is found.
  - **Columns** — status, `#N`, title, Severity level, Urgent mark, Route, Integrations, firing and total Alerts, start
    time and duration, last change, Reopen count, and the values of labels the user picks (none by default; a label's
    value is shown when all the Alert Group's Alerts share it); later Owner, Snooze end and Unclaimed.
  - **Sorting** by start time or by last change, either direction; **paging** by cursor with `api.page_size`.
  - **Live updates** — rows change in place; Alert Groups that newly match the filters are announced as "N new"
    above the list and shown on click, so the list never moves under the pointer.
  - Filters, tab, range, search, label columns and sorting are kept in the URL, so a view can be shared as a link.
- **C-09.FR-17** Times in the UI are shown in the user's time zone; durations are relative ("2 h 14 min"), and the
  absolute time is available on hover.
- **C-09.FR-24** The Alert Group list and the Alert Group page work at phone width (NFR-15): from a viewport 360 CSS
  pixels wide, without horizontal scrolling of the page. The list shows each Alert Group as a compact row — status,
  `#N`, title, Urgent mark and duration — and moves the filters into a panel; the page puts the header and, from C-10
  on, the commands first, then the Alerts and the Timeline.
- **C-09.FR-25** Live updates are a server-sent events stream at `GET /api/v1/live-updates`, fed by `LISTEN/NOTIFY`
  (ADR-0009). Events are hints `{type, id}` — an Alert Group, new Alert Groups, a configuration resource, the
  Organization-wide notices, the System status or the user's Account links — and carry no data, so the client re-reads
  through the API. The stream starts with `retry: 3000`; after a reconnect the client invalidates everything. When the
  user's session expires or ends, the server closes the stream, and the reconnect gets `401`, which makes the browser
  stop and sends the SPA to the sign-in page. The stream itself arrives earlier, with C-03 (C-03.FR-18), carrying the
  hints for the notices and the Organization; this capability adds the others.

### Alert Group page

- **C-09.FR-14** The Alert Group page shows:
  - the header — status, `#N`, title, Severity level, Urgent, Route (linked), Integrations, start time and duration,
    Reopen count, the reason of a resolution by the system; later the Owner, Snooze end, Unclaimed and the commands
    (C-10);
  - notices — Alerts still firing after a manual resolve, a Replacement hint (texts in
    [reference.md](reference.md#banners-warnings-and-notices));
  - the Alerts — firing first, resolved struck through with their reason; each with its labels, annotations
    (expandable), `startsAt`, time last seen, the Alertmanager groups listing it and its source link; filterable by
    firing or resolved;
  - the group labels, common labels and common annotations (`summary` and `description` prominent);
  - the Timeline, newest first or oldest first, filterable by kind — `status`, `alerts`, `notes`, `timers`, `delivery`
    (the delivery events merged in, C-11.FR-21) and `system` — each entry showing whether it was Loud and whom it asked
    to mention;
  - sections added later: links (C-12), delivery state per Destination (C-11, shown from C-13 on), the next scheduled
    notice or Reminder (C-17) and the Note box (C-10).
- **C-09.FR-20** The Alert Group page lists the previous Alert Groups of the same Route with the same Group key values —
  number, status, start, duration and who resolved them — linked to each.

### Past Alert Groups and statistics

- **C-09.FR-15** A statistics page shows, per Route or per Integration (all of them, or the chosen ones) and for a
  chosen period, the number of Alert Groups, time to acknowledge (from C-10 on) and time to resolve (median and 95th
  percentile), as totals and per day, from the summary rows. An Alert Group with Alerts from several Integrations counts
  for each of them. Time to acknowledge runs from the start of the Alert Group to its **first** acknowledgement;
  Takeover, Unacknowledge and Reopen do not change it, and Alert Groups resolved without ever being acknowledged are
  left out of it.
- **C-09.FR-16** Alerts inside Alert Groups, Timeline entries and the delivery events about Alert Groups are kept for
  `retention.alert_details`, Alert Group summary rows — with the title and `summary` — for
  `retention.alert_group_summaries`. Notes (C-10) are kept as long as the summary row, not with the details: removing
  the details never removes Notes, and Notes are deleted together with the summary row. The list and search cover the
  summary rows; an Alert Group whose details were removed opens as its summary and its Notes with the notice "Alerts
  and Timeline of this Alert Group were removed after {period}".
- **C-09.FR-21** The Integration delete dialog (C-05) shows how many open Alert Groups will be resolved.

## UI

Alert Group list (tabs, filters, time range, search, columns with chosen labels, live updates, shareable URL); Alert
Group page (header, notices, Alerts, labels and annotations, Timeline with loudness and Mentions, previous Alert Groups
with the same key); both at phone width; statistics page per Route or Integration; the count in the Integration delete
dialog; the move dialog when deleting a Route with open Alert Groups.

## API surface

`alert-groups` (list with filters, the `number` filter, search, time range, sorting and cursor; read — each with its title
and `summary`);
`alert-groups/{id}/alerts` (list); `alert-groups/{id}/timeline` (list with kind filter, delivery events merged in as the
kind `delivery`; each entry carries its kind, time, actor and Transport, and for a lifecycle event its `event`,
`loudness` and `mentions`); `alert-groups/{id}/related` (previous Alert Groups with the same key); `alert-group-counts`
(counts per status for the current filters); `alert-group-statistics` (read per Route or per Integration, all or chosen, for a period,
with totals and a per-day series); `routes/{id}/move-open-alert-groups` (to the Default route); the server-sent events
stream of change hints at `live-updates` (FR-25).

## Acceptance

Checked with the fake Alertmanager, through the API and the UI.

- **C-09.AC-1** Resolve all Alerts of a firing Alert Group by `resolved` and re-fire one within the Reopen window: the
  same `#N` is firing again, its Reopen count is 1, and its Timeline has a `reopened` entry with `loudness` `loud` and
  `mentions` `[reopen]`.
- **C-09.AC-2** The same after the Reopen window has passed: a new `#N`, firing.
- **C-09.AC-4** A new Alert differing only in `pod` from a firing one is recorded in the Timeline as `alert_replaced`
  naming `pod`, with `loudness` `quiet`, and the Alert Group page shows the Replacement hint.
- **C-09.AC-5** A list query without a time range returns the Alert Groups active within the last
  `alert_group.list_range`, including an open Alert Group that started earlier.
- **C-09.AC-6** Alerts with the same Group key values on one Route join one Alert Group; an Alert with another
  `cluster` starts a second one.
- **C-09.AC-7** Changing a Route's Matchers leaves an already grouped fingerprint in its open Alert Group.
- **C-09.AC-8** Marking a Route urgent does not change the status of any of its open Alert Groups.
- **C-09.AC-9** Deleting a Route with open Alert Groups returns `409` with their count; after moving them, deletion
  succeeds and each moved Alert Group's Timeline shows `moved_to_default_route`.
- **C-09.AC-10** A new `startsAt` without `resolved` on an open Alert Group records `alert_continued` and no Reopen or
  new firing.
- **C-09.AC-11** After stopping Muster for 10 minutes and starting it again, every open Alert Group's Timeline shows the
  unavailability period.
- **C-09.AC-12** A rise from warning to critical in a Route whose Group key lacks `severity` makes the firing Alert Group
  Urgent and records a Quiet `urgency_raised` in the Timeline.
- **C-09.AC-13** With the list open on the Firing tab, a new Alert Group appears as "1 new" within seconds, without a
  reload, and the Firing count grows by one.
- **C-09.AC-14** Deleting an Integration resolves its open Alert Groups with the reason "Integration {name} deleted".
- **C-09.AC-15** Filtering by `namespace="payments"` and Urgent over the last 30 days returns exactly the matching Alert
  Groups, and the URL reproduces the same view, chosen label columns included, when opened in another browser.
- **C-09.AC-16** The page of an Alert Group lists the earlier Alert Group with the same Route and Group key values.
- **C-09.AC-17** For a Route whose three Alert Groups in the chosen period resolved 10, 20 and 30 minutes after they
  started, the statistics page shows 3 Alert Groups and a median time to resolve of 20 minutes, split by the days they
  started; the same Alert Groups are counted under their Integration.
- **C-09.AC-18** With a virtual clock, once `retention.alert_details` has passed for a resolved Alert Group and its
  partitions are dropped, search still finds it by its title, and its page shows the summary with the notice "Alerts
  and Timeline of this Alert Group were removed after {period}".
- **C-09.AC-19** In a real browser 360 CSS pixels wide, the Alert Group list and the Alert Group page have no horizontal
  page scrolling, and a responder can pick the Firing tab, open an Alert Group and read its Alerts and Timeline.
- **C-09.AC-20** An Alert Group of `KubePodCrashLooping` Alerts whose first `summary` is "Pod postgres-0 is crash
  looping" is found by searching "postgres". In a Route whose Group key lacks `alertname`, an Alert with another
  `alertname` joining it changes the title to the Group key values once; later Alerts and resolutions leave it as it is.
- **C-09.AC-21** Every row of the lifecycle event table of FR-22 that this capability can produce records exactly one
  Timeline entry with the row's `event`, kind, `loudness` and `mentions` (a table-driven test).
- **C-09.AC-22** Resolve all Alerts of a firing Alert Group, then fire, within the Reopen window, an Alert with another
  fingerprint but the same Route and Group key values: the same `#N` reopens with its Reopen count at 1, and no new
  Alert Group starts.
- **C-09.AC-23** Searching by the number of an Alert Group resolved 60 days ago finds it with the default time range;
  `alert-group-statistics?group_by=route` returns one item per Route.
- **C-09.AC-24** `GET /api/v1/live-updates` starts with `retry: 3000`; ending the session closes the stream, and the next
  connection attempt gets `401`.
- **C-09.AC-25** Filtering by `namespace="payments"` matches an Alert Group whose Alerts all carry that value, and not
  one in which only some of its Alerts do; after its details are removed, the same filter still finds the first one.

## Related ADRs

ADR-0002, ADR-0003, ADR-0004, ADR-0006, ADR-0008, ADR-0009.

## Depends on

C-08 — Routes, Group keys and Severity levels.

## Suggested story split

- **BE** — grouping, state machine and system transitions, title, timers, Timeline, lifecycle events, list and search
  API, counts, related Alert Groups, statistics, live hints; may be split into "lifecycle" and "list, past Alert Groups
  and statistics".
- **FE** — Alert Group list, Alert Group page, both at phone width, statistics page, the dialog additions.
