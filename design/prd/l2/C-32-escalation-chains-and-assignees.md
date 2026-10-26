# C-32. Escalation chains and Assignees

[L2 index](../L2.md) · Release: 0.2.0 · UI: yes · Depends on: C-17, C-26, C-27, C-28, C-30, C-31

**Goal.** While an Alert Group stays Firing, Escalation chains Page more and more people: the On-call of a Schedule,
chosen people, a whole Team, a call through a Notification gateway, waits, an outgoing webhook and repeats. Routes and
Teams name their chains, several may run on one Alert Group, each Pages only in the Threads of its own Destinations, and
one Acknowledge stops them all. Until someone acknowledges, the Alert Group shows its Assignees — the people the chains
Paged last. Where no chain runs, the ack timeout of L1 stays.

Names in the form `area.setting` refer to rows of [defaults.md](../l1/defaults.md); the lifecycle events are rows of the
[L2 lifecycle event table](../l1/reference.md#lifecycle-events-added-in-l2).

## Scenarios

1. The Route "Infrastructure" names the chain "devops": Page the On-call of devops' "Main" normally, wait 10 minutes,
   Page them importantly. The Team "payments" names its chain: Page the On-call of "Main" normally; wait 10 minutes; for
   critical only, Page them importantly; wait 15 minutes; for critical only, Page the On-call of "Second line"
   importantly; during the working hours of payments, Page the whole Team; repeat from step 3 up to 2 times.
2. A warning `KubernetesContainerOOMKilled` of "payments" is published in `infra-notice` and `payments-notice`. The
   devops chain writes "Step 1: @ivan, you are needed" in the Thread of `infra-notice`; the payments chain writes in
   `payments-notice`, where Petr has no linked account: "Page not delivered: @petr has no linked Mattermost account".
   Step 3 of payments is skipped: "only for critical". The Root messages show "Assignees: @ivan (devops)".
3. Ivan acknowledges in `infra-notice` at 03:24. The Alert Group is acknowledged in `payments-notice` too, both chains
   stop, and the footer reads "Acknowledged by @ivan (devops) · Escalation chains stopped".
4. A critical `CheckoutQueueBacklog` of "payments": step 3 Pages Petr importantly; his rule calls him through the
   Notification gateway, and he presses 1 to acknowledge.
5. An Alert Group whose Route names a chain but no Destination starts its chain when it is created; only steps through a
   Notification gateway reach anyone, and the Route form warned about it.
6. Nobody acknowledges; both chains pass their last step, and the Alert Group shows "Nobody has taken this".

## Functional requirements

### Chains and steps

- **C-32.FR-1** An Escalation chain has a name, unique in the Organization, a description, an optional owning Team
  (C-25.FR-2), whose Working hours its steps use, and an ordered list of Escalation steps within `escalation.limits`.
  Only Admins create, change and delete chains (`escalation-chains:write`); everyone reads them.
- **C-32.FR-2** A Route names its Escalation chains, and a Team names its Escalation chains, as they name Destinations:
  each with a Severity condition — any, `critical` or `below_critical` (C-26.FR-1) — checked against the Alert Group's
  Severity level. Deleting a chain that a Route or a Team names is refused with `409` and the code `chain_in_use`.
- **C-32.FR-3** An Escalation step is one of:
  - **Page the On-call of a Schedule** — of any Team (C-27.FR-4) — normally or importantly;
  - **Page chosen people** — Users who may be Paged: saving a step that names a Viewer or a disabled User is refused
    with `422` and a pointer to the person — normally or importantly;
  - **Page a whole Team or everyone in the chat** — a Team, the chain's owning Team by default, by its Team mention, or
    everyone in the chat (`channel`, `here`, `all` or `everyone`); the form offers only what the Connections of the
    chain's Destinations can mention (C-28.FR-2);
  - **Call through a Notification gateway** — the On-call of a Schedule or chosen people, the chosen people under the
    same check as above, through a gateway it names when there is more than one (C-30.FR-4); offered only while a
    gateway exists (C-30.FR-9);
  - **Wait N minutes**;
  - **Send to an outgoing webhook** — the event `escalation_webhook` to an outgoing webhook Destination in events mode
    or both that the step names (FR-13);
  - **Repeat from step K**, up to N times.

  A step that Pages or sends may be limited to Working hours — those of the chain's owning Team, else the Organization's
  (C-25.FR-9) — and to a Severity condition. Paging a person normally or importantly runs their Notification rule of
  that name (C-31.FR-3).

### When chains run

- **C-32.FR-4** The chains that run on an Alert Group are those its Route names and — where the Route delivers to the
  Teams from the label and runs their chains (C-26.FR-2) — those that each Team of the Alert Group names, each whose
  Severity condition the Alert Group's Severity level meets. Several chains may run on one Alert Group at the same time.
  A chain that several of them name runs once and Pages in the Threads of each (_provisional_, P-70).
- **C-32.FR-5** A chain runs only while the Alert Group is firing, at the points of the ack timeout (C-17.FR-1):

  | Point | What happens to the chains |
  |---|---|
  | the Alert Group's first Publication; for one that a Storm summary stood for, its own Publication; for an outgoing webhook in events mode, its delivered `created` event | the chains that apply start at their first step |
  | the Alert Group is created while no Destination reaches it, or only Broken ones | the chains that apply start at once |
  | it becomes firing again: a Reopen into firing, Unacknowledge, Unsnooze, the end of a Snooze, Unresolve, a rise to critical that ends an acknowledgement or a Snooze, an auto-unacknowledge, its Owner disabled or deleted (C-03.FR-13) | the chains that apply start over at their first step |
  | its Severity level rises by its Alerts while it is firing and nobody owns it | the chains whose condition it now meets start; running chains go on |
  | a Team joins it by a new Alert (C-25.FR-6) while it is firing and nobody owns it | that Team's chains start, where the Route runs the chains of Teams |
  | a change of configuration: saving a Team (C-25.FR-13), turning on "Also to the Teams from the label" or "Run the Teams' Escalation chains", naming a chain on a Route or a Team or changing its Severity condition, changing the Severity condition of a Destination, changing the Minimum severity (C-26.FR-9) | no chain starts, and the running chains go on |
  | Acknowledge, Snooze, Resolve | every chain stops |

  A chain whose condition the Alert Group no longer meets after its Severity level falls goes on (ADR-0018, journal
  D469). A chain that a change of configuration would apply to starts at the next point of the table that starts or
  starts over the chains, such as Unacknowledge. New Alerts that remove an acknowledgement, where a Route allows it,
  become a point of starting over with the Route's Loud and Quiet settings, and how a Maintenance window keeps chains
  waiting is specified with Maintenance windows; both come later in L2. Steps are timer rows that any replica claims
  (ADR-0007); every step is a system transition of the dispatcher with Muster as the actor (ADR-0016).
- **C-32.FR-6** When the first chain of an Alert Group starts, its running ack timeout stops and sends no more notices.
  When the Alert Group becomes firing again, its ack timeout starts over only if none of its chains starts; an Alert
  Group on which no chain applies keeps the ack timeout of C-17.
- **C-32.FR-7** A chain has run out when it has passed its last step. An Alert Group with chains is Unclaimed once every
  chain started on it has run out without an acknowledgement; it records `unclaimed` (C-17.FR-11), and the Root message
  and the UI show "Nobody has taken this". A chain that starts later on an Unclaimed Alert Group — for a Team that
  joins, or a rise — makes it no longer Unclaimed while that chain runs, and it is Unclaimed again once that chain has
  run out too, as the definition of ADR-0004 says for every chain started on it (ADR-0018; journal D535).

### Where and how chains Page

- **C-32.FR-8** A chain Pages only in the Threads of its own Destinations: a Route's chain in those of the Route's
  Destinations that have the Alert Group's Root message; a Team's chain in those of the Team's Destinations that have
  it, and, where none has, in those of the Route's Destinations. So a step that Pages everyone in the chat never rings
  another team's chat.
- **C-32.FR-9** A Page by Mention is a Thread reply that delivery sends like any other (ADR-0005): Loud, never combined
  with the Pages of other Alert Groups. It names each person it Pages — as a Mention where they have an Account link in
  the Identity space of that Destination's Connection, and otherwise in a line "Page not delivered: @{login} has no
  linked {messenger} account" — and says which step and chain it comes from: "Step {n}: @ivan, you are needed", then
  "Chain of the Route {Route} · On-call of {Team} {Schedule} · normal", with "important, again in {N} min" when the
  person's rule repeats. A step for a Team or for everyone in the chat writes that Destination's Team mention or token;
  where the Destination's Connection can mention neither, such as Telegram, the step makes no reply there, and when it
  can make a reply in none of the chain's Destinations it records `escalation_step_skipped` with that reason. A call
  through a gateway writes nothing in the Thread.
- **C-32.FR-10** A Page whose person it reaches nowhere — no Root message to reply to in the chain's Destinations, no
  Account link in any of their Identity spaces, or Thread replies that all end Not delivered or are withheld, such as
  while a Destination is Broken — records `page_not_delivered` with the person and the reason and does not make them an
  Assignee. Thread replies that came due while a Destination was Broken, Pages among them, are not sent after it
  recovers (C-11.FR-19). Calls through a gateway depend on no Root message and keep working while a messenger is down.
- **C-32.FR-11** How a Page reaches a person, by where it goes:

  | Where | A Page is | It reaches the person when |
  |---|---|---|
  | Mattermost | a Thread reply with `@username` | they have an Account link for the Connection; push to phones is [still to be verified](../../facts.md#pending) |
  | Slack | a Thread reply with `<@U…>` | they have an Account link for the Connection; push to phones is still to be verified |
  | Telegram | a comment in the discussion group with a `tg://user?id=` link | they have a Telegram Account link and are in the discussion group ([F-014](../../facts.md#notifications)) |
  | Bridge | a Thread reply with a Mention marker (C-22.FR-16) | they have an Account link for the Connection and the bridge declares `native_mentions`; otherwise their plain name reaches nobody |
  | Outgoing webhook, events mode | the event `paged`, with the people as `mentions` data (C-15.FR-11) | the receiver acts on it |
  | Outgoing webhook, template mode | the "reply in thread" request, when it has one | the receiver acts on it |
  | Notification gateway | a call to the confirmed contact (C-30.FR-4) | the gateway reaches them, whatever the messengers do |

- **C-32.FR-12** Until someone acknowledges, an Alert Group shows its Assignees: for each running chain, the people its
  latest step Paging people by name — the On-call of a Schedule, chosen people, or a call through a gateway — reached,
  by a delivered Thread reply that mentions them or a call that the gateway accepted. A step that reached none of its
  people leaves that chain's Assignees as they were; steps that Page a Team or everyone in the chat change nothing. Each
  Assignee is shown with the owning Team of their chain. A change of Assignees records `assignees_changed`; Acknowledge,
  Snooze and Resolve clear them; they change only through system transitions of the dispatcher (ADR-0016), and nobody
  adds Assignees by hand — any Responder may acknowledge. The Root message names them before an acknowledgement
  (C-29.FR-1), as Mentions where their account is linked, and a new Quiet message with them may notify them. The Owner
  is named with their Team too: the Teams the Owner is in among the Teams of the Alert Group, the owning Team of its
  Route and the owning Teams of its chains, and none when there is no such Team (_provisional_, P-89).
- **C-32.FR-13** A step "Send to an outgoing webhook" sends the event `escalation_webhook` — the Alert Group as in every
  event, the chain and the step — to the outgoing webhook Destination it names, whether or not the Alert Group is
  delivered there, and that Destination receives no other event of the Alert Group unless it is one of its Destinations.
  The event is signed, ordered and delivered at least once like the other events of C-15.FR-2.
- **C-32.FR-14** A step is skipped, recording `escalation_step_skipped` with its reason, when it is limited to Working
  hours and the time is outside them, when the Alert Group's Severity level does not meet its condition, when the
  Schedule it Pages has a gap, when its gateway or the person's confirmed contact is gone, or for a person it names who
  has become a Viewer or been disabled since the step was saved; the chain goes on with its next step at once.
- **C-32.FR-15** A step "Repeat from step K, up to N times" goes back to step K until it has done so N times and then
  goes on; each pass makes its steps again.
- **C-32.FR-16** A change of a chain applies to the runs that start after it; a running chain keeps the steps it started
  with until it stops or starts over (_provisional_, P-73).

### Pages, warnings and the UI

- **C-32.FR-17** A Route that names a chain and no Destination, and does not deliver to Teams, shows the warning "Only a
  Notification gateway can reach anyone from this Escalation chain: the Route has no Destination." The chain form warns
  about a step for everyone in the chat that one of its Destinations' Connections cannot mention, such as Telegram.
- **C-32.FR-18** The Alert Group page shows its running chains — each with its current step and when its next step is
  due — and its Assignees with their Teams, and the Timeline shows each Page, skipped step and undelivered Page. The
  Alert Group list gains the column Assignees and the filter Assignee, a User or "me". The Home page lists, under "I am
  an Assignee", the Alert Groups where the signed-in User is an Assignee — "Alert Groups that a running Escalation chain
  Paged you about. They leave this list once acknowledged." — with their status, Assignees, the chain and its step, and
  Acknowledge and Snooze.
- **C-32.FR-19** Chains use optimistic locking and are recorded in the Audit log with a before/after diff. Each chain
  exports `muster_escalation_chain_info{chain,name}`; Pages are counted by `muster_pages_total{chain,kind,outcome}`,
  where `kind` is `mention` or `call`; the stories register their log events and add the lifecycle events of this
  capability to the [L2 lifecycle event table](../l1/reference.md#lifecycle-events-added-in-l2). The `mentions` of a
  Timeline entry, which L1 limits to eight symbolic names, gain `paged`, and the entry gains a field of its own for the
  people a Page names — expand steps of the schema (ADR-0006).
- **C-32.FR-20** The documentation has a page on Escalation chains — the steps, the table of FR-5, where chains Page,
  the table of FR-11, Assignees — and says that, until push for Mentions in Threads is verified, only a Notification
  gateway is reliable at night; and a page "How an alert reaches people" that walks one Alert from its Route and Alert
  Group to its Destinations, Teams, Escalation chains and Pages, with diagrams.

## UI

Team page → "Escalation chain of the Team" and the chain page: "Writes only in the Threads of {Destinations}. Starts at
publication." and "Stops when the Alert Group is acknowledged, snoozed or resolved; starts over on Unacknowledge, the
end of a Snooze, a Reopen, and a rise to critical that ends an acknowledgement or a Snooze."; "Run for: any severity /
Severity = critical / Severity < critical" — on the Team page the condition of the Team's own reference to the chain,
while each Route edits its condition in the Route form — and whose Alert Groups it runs on; "Steps", numbered, each with
its target, normal or important, and its limits as chips ("Severity: any", "always", "only in the working hours of
{Team} (Mon–Fri 11–19)", "Severity = critical"), and "Add step" with the seven kinds — "Page the On-call of a Schedule",
"Page people", "Page the whole Team", "Wait", "Send to an outgoing webhook", "Repeat from step", "Call through the
Notification gateway" — the last greyed with "Unavailable: a Notification gateway is needed (a bridge for calls and
SMS)" when there is none; the hint "Any step may be limited to working hours and to a Severity condition. Steps through
a Notification gateway do not depend on a messenger, but need the gateway."; "Whom 'Page the whole Team' calls" — the
Team mention, `@channel`, `@here`, `@all` or `@everyone`, the unavailable ones greyed with their reason; "Normal and
important": "What exactly happens is decided by each person's Notification rules. Muster sends no direct messages: a
Page is a Mention in the Thread of the Root message."; "How it went": the recent Alert Groups with their Pages, skipped
steps and the acknowledgement that stopped the chain. Route form: "Escalation chains" with their conditions and the hint
"Pages only in the Threads of the Route's Destinations. The Teams from the label Page with their own chains." Alert
Group page, list and Home as FR-18.

## API surface

`escalation-chains` (list, create, read, update, delete); `escalation-chains/{id}/runs` (list the recent runs of a chain
with their Alert Groups, Pages, skipped steps and what stopped them, for "How it went"); the chains with their
conditions on `routes` and `teams`; the running chains, their steps and the Assignees on `alert-groups`; the Assignee
filter of the list; `escalation_webhook` in the outgoing webhook event schema.

## Acceptance

Checked with a virtual clock, the fake messenger servers, the fake bridge as a gateway and the recording test adapter of
C-11.

- **C-32.AC-1** On the setup of scenario 1, a warning Alert Group published in `infra-notice` and `payments-notice` gets
  "Step 1" in each Thread from its own chain only; step 3 of payments is recorded as `escalation_step_skipped` with
  "only for critical"; 10 minutes later the devops chain Pages again importantly.
- **C-32.AC-2** Acknowledge from either Destination stops both chains: no later step is recorded, the Assignees are
  cleared with `assignees_changed`, and both Root messages read "Acknowledged by" with "Escalation chains stopped".
- **C-32.AC-3** Every point of the table of FR-5 starts, starts over or stops the chains as its row says, and a rise to
  critical of a firing Alert Group that nobody owns starts the chains for critical only without restarting the running
  ones (a table-driven test).
- **C-32.AC-4** While a chain runs, the Alert Group's ack timeout sends no notice; an Alert Group on which no chain
  applies gets the notices of C-17.AC-1.
- **C-32.AC-5** After every chain has passed its last step without an acknowledgement, the Alert Group is Unclaimed and
  records `unclaimed`; while a chain of a Team that joins afterwards runs, the Alert Group is not Unclaimed, and it is
  again once that chain has run out.
- **C-32.AC-6** A Page of a person without a Mattermost Account link writes "Page not delivered: @{login} has no linked
  Mattermost account" in that Thread, records `page_not_delivered` and leaves the Assignees unchanged; a Page whose
  Thread reply ends Not delivered does the same; a Page of a linked person makes them an Assignee once the reply is
  delivered.
- **C-32.AC-7** A Route with a chain and no Destination starts the chain when the Alert Group is created; its Mention
  steps record `page_not_delivered` "no Root message", and its gateway step calls the fake gateway.
- **C-32.AC-8** A step limited to the working hours of its Team is skipped outside them and made inside them; "Repeat
  from step 3, up to 2 times" makes steps 3 to 6 three times in all.
- **C-32.AC-9** A step "Send to an outgoing webhook" delivers one `escalation_webhook` to that Destination, which is on
  none of the Alert Group's Routes, and no other event of the Alert Group.
- **C-32.AC-10** The filter Assignee "me" returns the Alert Groups where the User is an Assignee, and the Home page
  lists them under "I am an Assignee" until they are acknowledged.
- **C-32.AC-11** Every row of the L2 lifecycle event table that this capability records produces exactly one Timeline
  entry with its `event`, kind, `loudness` and `mentions`, and the people of `paged` in the entry's field of its own (a
  table-driven test).
- **C-32.AC-12** With "Run the Teams' Escalation chains" off on the Route (C-26.FR-2), the Team's Destinations receive
  the Alert Group and the Team's chain does not start; with it on, the chain starts.
- **C-32.AC-13** Before an acknowledgement, the Root message lists "Assignees: @ivan (devops), @petr (payments)" as
  Mentions where the accounts are linked (C-29.FR-1), and the `layout` sent to a bridge with `native_mentions` names
  each Assignee with the `ref` of their Mention entry (C-29.FR-8); a step "Page the whole Team" whose only Destination
  is a Telegram channel records `escalation_step_skipped`.
- **C-32.AC-14** In a real browser, the chain page builds the payments chain of scenario 1, shows the gateway step
  unavailable without a gateway, and the Alert Group page shows the running chains, the next step and the Assignees, in
  English and in Russian, 360 CSS pixels wide without horizontal scrolling of the page.
- **C-32.AC-15** A Responder gets `403` on creating, changing or deleting a chain; deleting a chain that a Route names
  gets `409` `chain_in_use`; a step "Page chosen people" naming a Viewer gets `422`, and one whose person was turned
  into a Viewer later is skipped with that reason.
- **C-32.AC-16** A chain that both the Route and a Team of the Alert Group name runs once and Pages in the Threads of
  both; a change of a chain's steps while it runs leaves that run on its old steps, and the next run takes the new ones.
- **C-32.AC-17** A Route with a chain, no Destination and "Also to the Teams from the label" off shows "Only a
  Notification gateway can reach anyone from this Escalation chain: the Route has no Destination."; with a Destination
  named, the warning is gone. `escalation-chains/{id}/runs` lists the last run of the chain with its Pages and the
  acknowledgement that stopped it.
- **C-32.AC-18** On a firing Alert Group that nobody owns, saving a Team that its Alerts now belong to, turning "Run the
  Teams' Escalation chains" on, naming a new chain on its Route and raising its Route's Minimum severity to critical
  each start no chain and record no step; after an Acknowledge and an Unacknowledge, the chains that now apply start.

## Related ADRs

ADR-0004, ADR-0005, ADR-0007, ADR-0016, ADR-0018.

## Depends on

C-17 — the ack timeout and Unclaimed; C-26 — Routes and Teams with their Destinations; C-27 — Schedules and the On-call;
C-28 — what each messenger can mention; C-30 — Notification gateways; C-31 — Notification rules.

## Suggested story split

- **BE** — chains and steps, naming them on Routes and Teams, their lifecycle with the ack timeout and Unclaimed,
  skipped steps.
- **BE** — Pages by Mention in their own Threads, undelivered Pages, Assignees, with the Assignees of the layout
  (C-29.FR-1, C-29.FR-8).
- **BE** — `escalation_webhook`.
- **BE** — Pages through a gateway from steps and rules, queued as calls; the queue and its combining are C-30's.
- **FE** — the chain page, chains on the Route form and the Team page, running chains and Assignees on the Alert Group
  page, the Assignee filter, "I am an Assignee" on the Home page.
- **Docs** — Escalation chains and "How an alert reaches people".
