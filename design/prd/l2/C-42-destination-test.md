# C-42. The step-by-step Destination test

[L2 index](../L2.md) · Release: a 0.2.x patch release after 0.2.0 · UI: yes · Depends on: C-09, C-10, C-11, C-12,
C-16, C-17, C-22, C-24, C-26, C-27, C-28, C-29, C-30, C-31, C-32

**Goal.** An Admin walks one Test message through the life of an Alert Group in a Destination — creation, a new Alert,
Acknowledge, a Reminder, Takeover, Snooze, Resolve and Unresolve — one step at a time, and sees in the messenger, or in
the requests to an outgoing webhook, every edit, Thread reply, Mention and button each step makes; then plays the
Escalation chains that would run there in accelerated time and sees whom each step would Page, where and when, without
Paging or calling anyone. No Alert Group, statistic, metric of Pages or Assignee changes (journal D141, D548;
ADR-0018). The one-message test of C-16 stays as it is; this capability carries what C-16.FR-6 left out of L1.

Names in the form `area.setting` refer to rows of [defaults.md](../l1/defaults.md). The questions of the draft were
decided by the maintainer in its review (#673).

## Scenarios

1. The Admin opens the Destination `infra-critical`, chooses "Step-by-step test" with the source "Example" and the
   Severity level critical, and presses "Start". A Root message marked "🧪 Test message" appears in Mattermost; the
   result shows the request, the answer and "Would mention: @devops, @ivan (On-call of devops)".
2. She presses "Next: New Alert": the Root message is edited to two Alerts and a Thread reply appears. "Next:
   Acknowledge" edits it to Acknowledged by her, with the buttons of an acknowledged Root message from the
   Destination's button set; then a Reminder, a Takeover by "Example person", a Snooze for 1 hour, Resolve and
   Unresolve, each showing its requests and answers.
3. Ivan presses Acknowledge on the Test message in Mattermost. He is answered "This is a test message; nothing was
   changed", and the result shows "A press reached Muster: @ivan, Acknowledge".
4. An outgoing webhook Destination in events mode receives, step by step, the events of each step marked `test: true`,
   and its receiver's answers are shown masked; in template mode it receives the "create", "update" and "reply in
   thread" requests that its templates build.
5. Under "Escalation chains", the test lists the chains that would Page in this Destination — "devops" of the Route
   "Infrastructure" and "payments" of the Team payments — and plays them from Saturday 03:00: "+0 min · devops · Step
   1 · would Page @ivan (On-call of devops, Main) normally in the Threads of infra-critical and infra-notice"; "+0 min ·
   payments · Step 1 · skipped: only during Working hours"; "+10 min · devops · Step 3 · would call @ivan through Phone
   gateway"; "+10 min · payments · Step 3 · would Page @petr importantly: Page not delivered, @petr has no linked
   Mattermost account"; "+25 min · the chains have run out: Unclaimed". Nothing is sent to anyone.
6. The Admin closes the page halfway. The test run ends 1 hour later; its Test messages stay in the Destination.

## Functional requirements

### The step-by-step test

- **C-42.FR-1** "Step-by-step test" on the Destination page starts a test run with a source — a recent Alert Group of
  one of the Destination's Routes or the built-in example (C-16.FR-1) — and a Severity level, by default the source's.
  Each run has its own Test message; several runs, of one Admin or of several, may go on at once without touching each
  other. A run is a row of the database with its Organization (`org_id`, lint 1), so that any replica takes its next
  step, and ends when its last step is made, when the Admin ends it, or once it has made no step for
  `destination_test.idle_timeout` on the business clock. An idle run is ended lazily: the next step, read or new run of
  that Destination that finds it expired deletes it, so no Leader task is added (ADR-0007); a step or read of an ended
  run gets `404`.
- **C-42.FR-2** The steps go in this order, each one a valid transition of the step before it, so that every message
  shows a state an Alert Group can have; the Reminder comes right after Acknowledge, since after Unresolve an Alert
  Group has no Owner to remind, and the end of a Snooze, an ack timeout notice and a rise to critical are not steps:
  1. **Created** — the Publication of the Root message, Loud, as `new_alert_group`;
  2. **New Alert** — a second Alert joins, Loud, as `new_alerts`;
  3. **Acknowledge** — by the Admin who runs the test, as its Owner, or by "Example person" when a Service account
     runs it, since a Service account is never an Owner (C-04.FR-2);
  4. **Reminder** — to the Owner, with its buttons "Still on it" and "Unack" (C-17);
  5. **Takeover** — by "Example person", a name written in the message only (by "Second example person" when a
     Service account runs the test), the previous Owner named as C-28.FR-4 says;
  6. **Snooze** — for the first Snooze duration of the Destination's button set, or 1 hour;
  7. **Resolve** — by the Admin;
  8. **Unresolve** — Firing again without an Owner (C-10.FR-1).

  Each step renders and sends what delivery sends for that lifecycle event to that Destination type — an edit of the
  Root message to its Desired state, a Thread reply, or both, as the [Loud and Quiet
  table](../l1/reference.md#loud-and-quiet) says, with the compact layout (C-29) and the Destination's button set
  (C-28.FR-9); to an outgoing webhook, the event in events mode, marked `test: true` like the `test` event of
  C-16.FR-1, and the "create", "update" or "reply in thread" request of its templates in template mode. "Next:
  {step}" makes the next step; a step is never made on its own.
- **C-42.FR-3** Every step goes through the interactive path (C-11.FR-2) under `delivery.interactive_budget`, as the
  Test message of C-16 does, and its result shows each request with Secrets, the Signing secret and tokens masked, the
  answer (truncated, as untrusted text, masked), the duration and the error class (C-16.FR-2); a step that gets no
  limiter token in time ends with `limited` and may be tried again. A failed step stops the run until it is tried
  again or the run is ended. The first step counts as a Test message for C-16.FR-7: when it succeeds on a Broken
  Destination, the Broken state ends.
- **C-42.FR-4** For each Loud step the result lists whom the Destination's Mention entries for that event would mention
  (C-28.FR-1, FR-3) — each person, messenger group, Team and On-call by name, and each entry that would mention nobody
  with the reason, such as "has no linked Mattermost account" — and, for the Reminder and the Takeover, the Owner and
  the previous Owner that L1 mentions whatever the entries (C-28.FR-4). The Test message never mentions them, so that
  a test notifies nobody's devices, possibly at night: a step sends no Mention, of an entry or of an Owner, and writes
  the names as plain text after "Would mention:". No "Mention not delivered" is recorded and no warning of the
  Destination changes (C-28.FR-3).
- **C-42.FR-5** The buttons of the Test message are those its status shows from the Destination's button set
  (C-28.FR-8). A press on any of them, by anyone, changes nothing and is answered privately with "This is a test
  message; nothing was changed" (C-16.FR-3), and the result of the run shows each press that reached Muster with the
  person, the button and the time — the proof that presses reach Muster for every messenger, a Slack and a Bridge
  Destination included, where the bot cannot press its own button (C-24.FR-13). A Mattermost run also presses the first
  button of its first step through the bot, as C-16.FR-3 does. Text commands in the Thread of a Test message of a
  Bridge Destination (C-22.FR-14) are answered and shown the same way.
- **C-42.FR-6** A run changes nothing outside itself: no Alert Group, Alert, Timeline, Note or Assignee is written, so
  `internal/groups` writes nothing (lint 2); its messages are never reconciled; it counts in no statistic (C-09.FR-15,
  C-40), no `muster_delivery_attempts_total`, `muster_commands_total` or `muster_pages_total`, and its requests count
  only in `muster_client_requests_total` with the client `interactive`, as those of C-16 do. Its Test messages stay in
  the Destination when it ends, marked "🧪 Test message", as those of C-16 stay; Muster never deletes them, which would
  need a messenger operation outside delivery (lint 3).

### The accelerated run of the Escalation chains

- **C-42.FR-7** "Escalation chains", beside the step-by-step test on the Destination page, takes a source and a
  Severity level of its own, as FR-1 does, whether or not a test run is going on, and lists the chains that would run
  on such an Alert Group and Page in this Destination: those that the Routes naming this Destination name and, where a
  Route delivers to the Teams and runs their chains (C-26.FR-2), those that the Teams naming this Destination name,
  each whose Severity condition that Severity level meets (C-32.FR-2, FR-4, FR-8). The Admin plays one of them or
  all, from the business clock's now or from a chosen time, so that nights, weekends and Shifts can be checked. It
  offers no other chain: a chain that Pages only through a gateway, or only in other Destinations, is not played here.
- **C-42.FR-8** Playing computes the run as C-32 would make it if nobody acknowledged, and shows it at once as a
  timeline of moments from the start — "+0 min", "+10 min" — without waiting: each step with its number and kind; whom
  it would Page — the On-call of the Schedule at that moment from its Result (C-27), chosen people, a Team or everyone
  in the chat — normally or importantly, and where, naming every Destination of the chain whose Threads it would Page
  in, not only this one (C-32.FR-8); each action of each person's Notification rule with its own waits (C-31.FR-3), a
  call reading "would call @{login} through {gateway}" without the contact; each person a Page would not reach, with the
  reason of C-32.FR-10 and FR-11; each skip with the reason of C-32.FR-14 — Working hours, Severity level, a gap in the
  Schedule, a gateway or contact gone, a Viewer or disabled person; repeats as C-32.FR-15 makes them, up to the 4,020
  steps a run makes, the timeline folding the passes after the first; an outgoing webhook step as "would send
  `escalation_webhook` to {Destination}"; and, at the end, the moment the Alert Group would become Unclaimed
  (C-32.FR-7). Overlapping chains are shown on one timeline, each step named with its chain.
- **C-42.FR-9** The accelerated run is only a computation, which the UI calls "Play" and never a run of a chain: it
  writes no row of a chain run, Notification rule run, call or timer, sends nothing through a messenger, an outgoing
  webhook or a Notification gateway, records no `page_not_delivered` or `escalation_step_skipped`, makes no Assignee
  and counts in no metric. It reads the chains, Schedules, Notification rules, Account links and gateway contacts as
  they are when it is played. Its moments are the chosen start plus the waits, computed on the business clock: neither
  the business clock nor the development clock moves, in production or under `muster dev` (`/_dev/clock` is not
  used), and the real clock is not read for them. Playing posts nothing in the Thread of any Test message; its
  timeline is shown only on the page. "Accelerated run" stays a description, not a term of the glossary.

### Access, record and documentation

- **C-42.FR-10** The step-by-step test and the accelerated run need `destinations:test`, which only Admins have, from
  the web session or a token. Each step that sends is recorded in the Audit log as a test of the Destination
  (`destination.tested`, C-16.FR-5) with the run and the step; playing the chains sends nothing and is not recorded.
- **C-42.FR-11** The test works from a viewport 360 CSS pixels wide without horizontal scrolling of the page: the steps
  one under the other, requests and answers folded, the timeline of the chains as a list. Every text is in English and
  Russian, and the built-in texts of the Test message are in the language of the source's Route (`route.language`,
  C-12.FR-3), for the built-in example that of the first Route, by name, that names the Destination.
- **C-42.FR-12** A new documentation page under Concepts on testing a Destination, linked from the pages of the
  messengers and of outgoing webhooks, describes the Test message, the step-by-step test, what each step sends to each
  Destination type, the presses it shows, the accelerated run of the chains and what neither changes, with
  screenshots (C-21.FR-9).

## UI

Destination page → "Test": "Send test message" (C-16) and "Step-by-step test", with the source selector of C-16 and
"Severity level"; "Start"; the steps with their names, "Next: {step}", "Try again" and "End the test"; for each step
"Request", "Answer", "Would mention:" and "Presses that reached Muster"; "Escalation chains" with the chains offered,
"All chains", "Start from" — "Now" or a date and time — and "Play"; the timeline with "+{N} min", "would Page",
"would call", "skipped", "Page not delivered", "would send" and "Unclaimed".

## API surface

To be specified in `api/openapi.yaml` by the backend story, each with `x-permission: destinations:test`:
`destinations/{id}/test-runs` (create with the source and the Severity level → the run and the result of its first
step); `destinations/{id}/test-runs/{run_id}` (read the run with its steps and presses; delete to end it);
`destinations/{id}/test-runs/{run_id}/steps` (create → the next step's result); `destinations/{id}/chain-plays`
(create with the source, the Severity level, the chains and the start → the computed timeline, nothing stored).

## Acceptance

Checked with the fake Mattermost, Telegram, Slack and bridge servers, a fake outgoing webhook endpoint, the fake
gateway and the development clock, through the API and in a real browser.

- **C-42.AC-1** A step-by-step test of a Mattermost Destination makes, in the fake server, one Test message marked 🧪
  and then, step by step, the edits and Thread replies of the eight steps, in order; each result shows its request and
  answer with the bot token masked; no Alert Group, Timeline or statistic changes, and
  `muster_delivery_attempts_total`, `muster_commands_total` and `muster_pages_total` stay as they were.
- **C-42.AC-2** The same test of an outgoing webhook in events mode delivers one event per step marked `test: true`,
  which the fake endpoint verifies with the Signing secret; in template mode the fake endpoint receives the "create",
  "update" and "reply in thread" requests of its templates.
- **C-42.AC-3** A press on the Test message in the fake Telegram, Slack and bridge servers is answered "This is a test
  message; nothing was changed" and appears in the run's result with the person and the button; nothing changes.
- **C-42.AC-4** For a Destination whose entries name a person without an Account link, the result says that the entry
  would mention nobody and why, the message carries no Mention, and the Destination gains no warning.
- **C-42.AC-5** A run left alone ends after `destination_test.idle_timeout`, advanced with the development clock; its
  next step then gets `404`, its row is gone, and its Test messages stay in the fake server.
- **C-42.AC-6** Playing a chain of three steps — Page the On-call normally, wait 10 minutes, Page them importantly —
  with an important rule of a Mention, a wait of 5 minutes and a call, shows "+0 min", "+10 min" and "+15 min" with the
  call as "would call"; the fake gateway receives nothing, no `escalation_runs` row or timer is written, and the
  business clock reads the same before and after.
- **C-42.AC-7** Played from a chosen Saturday 03:00, a step limited to Working hours is shown skipped with its reason,
  and a Schedule with a gap at that moment shows the skip of a gap.
- **C-42.AC-8** A Responder and a Viewer get `403` on each operation; each step that sends adds one Audit log entry and
  playing the chains adds none. A run started with an Admin Service account's token acknowledges and takes over as the
  example people.
- **C-42.AC-9** In a real browser 360 CSS pixels wide the test and the timeline work without horizontal scrolling, in
  English and in Russian.
- **C-42.AC-10** The Test message of a Loud step carries no Mention markup in the fake Mattermost server — the names
  stand after "Would mention:" as plain text — and playing the chains adds no Thread reply to it.

## Related ADRs

ADR-0004, ADR-0005, ADR-0015, ADR-0016, ADR-0018.

## Depends on

C-09, C-10, C-17 — the lifecycle events, Commands and Reminders that the steps show; C-11, C-12, C-29 — the
interactive path, built-in texts and the layout of messages; C-16 — the Test message, its result, presses and the
Broken state; C-22, C-24 — presses of Bridge and Slack Test messages; C-26 — Routes that run the chains of Teams;
C-27 — the Result of Schedules at a chosen time; C-28 — Mention entries and the button set; C-30, C-31 — gateways,
contacts and Notification rules; C-32 — Escalation chains, their steps, skips and Unclaimed.

## Suggested story split

- **BE** — test runs and their steps for every Destination type, presses on them, expiry, the Audit log.
- **BE** — the accelerated run of the chains as a computation over chains, Schedules, rules and Account links.
- **FE** — "Step-by-step test" and the timeline of the chains on the Destination page, at phone width.
- **docs** — the page on testing a Destination.
