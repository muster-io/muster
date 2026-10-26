# C-28. Mentions and buttons of a Destination

[L2 index](../L2.md) · Release: 0.2.0 · UI: yes · Depends on: C-12, C-13, C-22, C-24, C-25, C-26, C-27

**Goal.** Each Destination says whom it mentions on which Loud event, as one list of entries — everyone in the chat, a
person, a messenger group, a Team or the On-call of a Team, named or taken from the Alert Group — limited to what its
messenger can mention, so that one Destination serves the Alert Groups of every team; and which buttons its Root
messages show (issue #367), so that a busy channel shows only what people press there.

Names in the form `area.setting` refer to rows of [defaults.md](../l1/defaults.md).

## Scenarios

1. The operations Destination `infra-critical` mentions "On-call of the Team from the label" on every Loud event and
   `@here` only for a new Alert Group, a rise to critical and the start of a Storm.
2. A Destination of the Team "payments" mentions "Team payments" by default; the Team's Mattermost group `payments_team`
   is mentioned. In `search-notice`, whose Team has no Team mention, the same entry mentions nobody, and the message
   alone is enough.
3. A Telegram Destination offers only people with a linked Telegram account and the On-call entries; everyone in the
   chat and groups are shown unavailable: "Telegram cannot mention everyone or groups".
4. The On-call of "payments", Petr, has no linked Mattermost account; the new Alert Group's Timeline records that the
   Mention in `payments-notice` was not delivered, and the Destination page warns "Mention not delivered: @petr, the
   On-call of payments, has no linked Mattermost account". Nothing is written in the Thread.
5. A Storm starts on the Route "Infrastructure"; the first publication of its Storm summary in `infra-critical` mentions
   the On-call of devops, through the event "Start of a Storm".
6. After the upgrade to 0.2.0, a Destination that mentioned `@channel` only for new Alert Groups keeps doing exactly
   that — for a new Alert Group and, as before, for the start of a Storm.
7. The busy channel `infra-notice` shows only Acknowledge, "Snooze 24 h" and "Until tomorrow 09:00"; the other actions
   stay in Muster and in the Text commands of Bridges.

## Functional requirements

### Mentions

- **C-28.FR-1** A Destination's Mentions are a list of entries. Each entry names one target and the Loud events it
  applies to: a new Alert Group (`new_alert_group`, which also applies to `destination_joined`, C-26.FR-5), new Alerts
  (`new_alerts`), a Reopen (`reopen`), an ack timeout notice (`ack_timeout`), the end of a Snooze (`snooze_ended`), a
  rise to critical (`rise_to_urgent`) and the start of a Storm (`storm_started`, the first publication of a Storm
  summary). A new entry applies to all of them. The targets are: everyone in the chat (`channel`, `here`, `all` or
  `everyone`); a person, by User; a messenger group, by its name; a Team, by name; "Team from the label" — the Teams of
  the Alert Group (C-25.FR-6); "On-call of a Team", by name; and "On-call of the Team from the label" — the On-call of
  each Team of the Alert Group, both from C-27 on. A list without entries mentions nobody (`destination.mentions`).
- **C-28.FR-2** The list offers only the targets that the Destination's Connection can mention and shows the others as
  unavailable with the reason:

  | Destination type | Everyone in the chat | Messenger groups and Teams | People and On-call |
  |---|---|---|---|
  | Mattermost | `@channel`, `@here`, `@all` | Mattermost groups by name | linked people |
  | Slack | `<!channel>`, `<!here>`, `<!everyone>` | Slack user groups, when the Connection's check found the scope `usergroups:read` (C-24.FR-1) | linked people |
  | Telegram | none | none | linked people |
  | Bridge | those of `channel`, `here`, `all` and `everyone` that the bridge declares in `info` (C-22.FR-2) | group names, when the bridge declares group mentions in `info` | linked people; a bridge without `native_mentions` shows their plain name, which notifies nobody (C-22.FR-16) |
  | Outgoing webhook | every target, as data (C-15.FR-11) | every target, as data | every target, as data |

  Saving an entry that the Connection cannot mention is refused with `422` and the code `mention_unsupported`. When a
  Connection later loses an ability, such as a Slack app reinstalled without the scope, its entries stay, mention
  nobody, and the Destination page warns about each.
- **C-28.FR-3** For each Loud event, delivery turns the entries that apply to it into the Mentions of the message for
  that Destination: a person, by the Account link in the Identity space of the Destination's Connection (C-12.FR-12); a
  Team, by its Team mention for that Connection, and nobody when it has none — never its On-call instead; "Team from the
  label", by the Team mention of each Team of the Alert Group; "On-call of a Team", by the person whom the Team's main
  Schedule has on Shift at the moment of the event (C-27.FR-4), and nobody when the Team has no main Schedule or it has
  a gap; "On-call of the Team from the label", the same for each Team of the Alert Group. Each person and group is
  mentioned once in a message. A person whom an entry names but who has no Account link in that Identity space is not
  mentioned, and nothing is written in the Thread for it; through a bridge without `native_mentions`, a linked person is
  named by the plain name of C-22.FR-16, which notifies nobody. In both cases the Alert Group's Timeline records the
  delivery event "Mention not delivered" with the person, the entry, the event and the reason (C-11.FR-21), and the
  Destination page warns about each person and entry whose latest such delivery event is not followed by a delivered
  Mention of that person through that entry — computed from the delivery events, not stored: "Mention not delivered:
  @{login}, {entry}, has no linked {messenger} account" or "… the bridge cannot mention people".
- **C-28.FR-4** The rules of L1 stay: Reminders and auto-unacknowledge mention the Owner and a Takeover the previous
  Owner, whatever the entries; a Reopen into acknowledged mentions only the Owner; Mentions are never added by an edit
  (ADR-0005); a Quiet new message carries none.
- **C-28.FR-5** The 0.2.0 migration turns the L1 Mention settings of every Destination into entries with exactly the
  same effect: each target chosen for a kind of Loud event becomes an entry for the kinds it was chosen for, and an
  entry for `new_alert_group` also applies to `storm_started`, since an L1 Storm summary mentioned the targets of
  `new_alert_group`. A Destination that mentioned nobody keeps an empty list. Until 0.3.0 the API also returns the L1
  form of the settings, read-only, computed from the entries that L1 can express — everyone in the chat, chosen Users
  and messenger groups, for the six kinds of L1 — so that the other entries are not seen in it, and ignores it in
  requests. The L1 column keeps what the migration found and is not written after it, so that 0.1 replicas of a rolling
  update mention as before; 0.3.0 removes the field and the column. The upgrade notes say so.
- **C-28.FR-6** When a Destination becomes a Destination of a Team (C-26.FR-1) and has no entry naming that Team, Muster
  adds the entry "Team {Team}" for every Loud event, recorded in the Audit log; it mentions someone only where the Team
  has a Team mention for the Destination's Connection. An Admin may remove it.
- **C-28.FR-7** The first publication of a Storm summary (C-11.FR-6) carries the Mentions of the entries for
  `storm_started` of that Destination, with "On-call of the Team from the label" and "Team from the label" taken from
  the Teams of the Alert Groups the summary counts; a Storm summary starts no Escalation chain.

### Buttons

- **C-28.FR-8** The buttons a Root message shows come from a button set: the Organization's (`organization.buttons`),
  which by default is "all" — every button, durations a Route gains later included — until an Admin lists the buttons,
  or the Destination's own (`destination.buttons`). A set lists Acknowledge (with Unacknowledge), Resolve, each Snooze
  duration by its value, "Until tomorrow" and "Until {first day of the week}" (C-10.FR-18); the form offers the distinct
  Snooze durations of the Organization's Routes. A Root message shows, in the order of
  [reference.md](../l1/reference.md#buttons-and-links-by-status), the buttons its status offers that the set includes:
  of the Route's Snooze durations only those in the set, and the presets only on a Route that has Snooze durations.
  Unsnooze on a snoozed Root message is not part of the set and always shows. A Destination with "As in the
  Organization" follows later changes of the Organization's set.
- **C-28.FR-9** The set changes only the buttons of Root messages: the buttons of Reminders ("Still on it", "Unack"),
  the Text commands of Bridges (C-22.FR-14), the UI and the API keep every action. Each messenger lays out the shown
  buttons as before: a Slack Root message puts the shown durations and presets in its menu "Snooze…" and leaves the menu
  out when none is shown (C-24.FR-5); a Bridge drops buttons beyond its `max_per_message` as C-22.FR-5 says; a test
  message (C-16) shows the buttons of the Destination's set. A change of a set re-renders the open Root messages of the
  Destinations that use it.

### Settings

- **C-28.FR-10** The Mentions and the button set are part of the Destination form of every type (C-13.FR-9); the
  Organization's button set is on the Organization's page Alert handling (C-20). Changes are recorded in the Audit log
  with a before/after diff and use optimistic locking.
- **C-28.FR-11** The documentation's page on whom a message mentions describes the entries, the table of FR-2, Team
  mentions without a fallback to the On-call, and the migration of FR-5; the page on buttons describes the sets.

## UI

Destination form, section "Mentions": "Whom to mention and on which Loud events. A new entry is ticked for every event."
A grid with one row per entry — the target and, under it, what it resolves to now ("now: @ivan", "payments —
@payments_team; devops and search — nobody") — one column per Loud event ("New Alert Group", "New Alerts", "Reopen",
"Ack timeout", "End of Snooze", "Rise to critical", "Start of a Storm"), each a tick box, and "Remove"; "Add" opens the
targets: `@channel`, `@here`, `@all`, `@everyone`, "Group…", "Person…", "Team…", "Team from the label", "On-call of a
Team…", "On-call of the Team from the label", the unavailable ones greyed with their reason; with no entry, "Nobody is
mentioned: messages arrive without Mentions." Notes under the grid: "'Team' is the Team mention for this Connection, if
it is set; otherwise nobody. The On-call is not mentioned instead.", "A person without a linked account is not
mentioned; the Alert Group's Timeline and this page say so.", "Reminders and auto-unacknowledge always mention the
Owner, and a Takeover the previous Owner." Above the grid, the warnings of FR-3. A table "What each messenger supports"
(FR-2). Section "Buttons": "Which buttons the Root message shows. Hidden actions stay in Muster and in the Text commands
of Bridges." — "As in the Organization" or "Own for this Destination" with a tick box per button, and a preview "What
{Destination} shows" with the buttons of a firing and of an acknowledged Root message. Organization → Alert handling:
the Organization's button set.

## API surface

The Mention entries — target and Loud events — on `destinations`, with the L1 form read-only until 0.3.0; the button set
(`buttons`: the Organization's, or the Destination's own list) on `destinations` and `organization`; per entry, what it
resolves to now, on the Destination read.

## Acceptance

Checked through the recording test adapter of C-11 and the fake messenger servers.

- **C-28.AC-1** A Mattermost Destination with the entries "On-call of the Team from the label" (every event) and `@here`
  (new Alert Group only) mentions the On-call of "payments" and `@here` on a new Alert Group of "payments", and only the
  On-call on its Reopen.
- **C-28.AC-2** "Team payments" in a Mattermost Destination mentions `@payments_team` when the Team has that Team
  mention for the Connection, and nobody — and not the On-call — when it has none.
- **C-28.AC-3** A Telegram Destination refuses `@channel`, a group and a Team with `422` `mention_unsupported`, and
  takes a person and "On-call of a Team"; a Slack Destination takes `everyone`, and takes a user group only after its
  Connection's check found `usergroups:read`; a Bridge Destination takes `channel` only when its bridge declares it in
  `info`, and takes a person on any bridge, where one without `native_mentions` gets the plain name and the Timeline
  records "Mention not delivered" with the reason that the bridge cannot mention people.
- **C-28.AC-4** An On-call without a linked account in the Destination's Identity space is not mentioned; the Alert
  Group's Timeline has the delivery event "Mention not delivered" with that person and entry, nothing is posted in the
  Thread, and the Destination's read carries the warning for that person and entry until, after the person links an
  account, a later Mention of them through that entry is delivered.
- **C-28.AC-5** A Destination migrated from the L1 settings "`@channel` for `new_alert_group`, Alice for `ack_timeout`"
  has two entries — `@channel` for `new_alert_group` and `storm_started`, Alice for `ack_timeout` — and mentions on
  every Loud event exactly whom it mentioned before; its L1 form still reads the same.
- **C-28.AC-6** The first publication of a Storm summary mentions the targets of the entries for `storm_started`, and no
  chain starts for the summary.
- **C-28.AC-7** A Destination whose own set has only Acknowledge, "Snooze 24 h" and "Until tomorrow" shows exactly those
  on a firing Root message of a Route with the durations 1 h, 4 h and 24 h, and Unack, "Snooze 24 h" and "Until
  tomorrow" when it is acknowledged; a Reminder in its Thread still carries "Still on it" and "Unack", and the Text
  command `resolve` through a Bridge still resolves. With the Organization's set left "all", a Snooze duration of 2 h
  added to the Route later shows on the next re-render of a Destination that follows the Organization.
- **C-28.AC-8** Naming a Destination of the Team "payments" adds the entry "Team payments" when it had none.
- **C-28.AC-9** In a real browser, the Destination form of a Telegram Destination shows `@channel`, groups and Teams
  unavailable with "Telegram cannot mention everyone or groups", and the Buttons preview follows the tick boxes, in
  English and in Russian.

## Related ADRs

ADR-0005, ADR-0017, ADR-0018.

## Depends on

C-12 — Mentions in messages; C-13, C-22, C-24 — the Destination form and what each messenger can mention; C-25 — Teams
and Team mentions; C-26 — the Destinations of a Team; C-27 — the On-call of a Team.

## Suggested story split

- **BE** — the button sets of the Organization and the Destinations (#367); it needs neither Teams nor Schedules.
- **BE** — the Mention entries with their resolution, the undelivered Mentions and the migration of the L1 settings,
  `storm_started`.
- **FE** — the Buttons section of the Destination form and the Organization's button set.
- **FE** — the Mentions grid of the Destination form with its warnings.
