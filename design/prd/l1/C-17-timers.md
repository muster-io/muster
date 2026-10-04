# C-17. Timers

[L1 index](../L1.md) · Stage: Act · UI: yes · Depends on: C-10, C-12, C-13, C-14

**Goal.** Nobody forgets a Firing Alert Group or one they took: ack timeout notices that end in Unclaimed, Reminders to
the Owner, and — where a Route wants it — taking an Alert Group back from an Owner who stopped responding.

Names in the form `area.setting` refer to rows of [defaults.md](defaults.md). Timers record the lifecycle events of
FR-11.

## Scenarios

1. An Alert Group is posted at 03:00 and nobody acknowledges. Ack timeout notices arrive in its Thread at 03:15, 03:45
   and 04:45; then the Root message shows "Nobody has taken this".
2. Alice acknowledges at 10:00. Reminders mentioning her arrive at 14:00, 22:00, 14:00 the next day and then every 24
   hours, each with "Still on it" and "Unack"; pressing "Still on it" does not make them more frequent.
3. On an Informational Route nothing of this happens.
4. Bob takes the Alert Group over; Reminders start over for him.
5. Muster was down for two hours; one Thread reply says how many notices were missed.
6. On a Route with auto-unacknowledge, Carol acknowledged at 10:00 and went home without a word. She answers neither the
   Reminder at 14:00 nor the one at 22:00; at 14:00 the next day, instead of a third Reminder, the Alert Group is
   unacknowledged with the reason, Carol is mentioned, and the ack timeout starts over.

## Functional requirements

- **C-17.FR-1** The ack timeout runs only while an Alert Group is firing. It starts at the Alert Group's first
  Publication (for an Alert Group covered by a Storm summary, at its own Publication) and starts over whenever the Alert
  Group becomes firing again: Reopen into firing, Unacknowledge, Unsnooze, Unresolve, a Snooze ending, a rise to Urgent,
  an auto-unacknowledge. Acknowledge, Snooze and Resolve stop it. An Alert Group that has not been published — a Route
  without Destinations, or only Broken ones — has no ack timeout until its first Publication.
- **C-17.FR-2** Notices come at doubling intervals from the Route's first interval F (`route.ack_timeout`) — F, 2F and
  4F, that is F, 3F and 7F after the start — and there are at most three. They are Loud Thread replies whose text is the
  Route's ack timeout template (C-12); Mentions follow each Destination's setting. The ack timeout can be turned off per
  Route; it is a Route policy field added here, with the profile values of defaults.md.
- **C-17.FR-3** After the last notice the Alert Group is Unclaimed: the Root message and the UI show "Nobody has taken
  this", and the Alert Group list gains an Unclaimed filter, column and badge. Unclaimed ends when the Alert Group stops
  being firing — it is acknowledged, snoozed, or resolved by a person or by the system — and the status change that ends
  it removes the mark from the Root message. If the Alert Group becomes firing again, its ack timeout starts over and it
  is Unclaimed again only after the last notice of that ack timeout.
- **C-17.FR-4** Reminders follow `route.reminders`: the first comes one base interval after the acknowledgement, and
  each next interval doubles up to the cap. Each is a Loud Thread reply mentioning the Owner, with the buttons "Still on
  it" and "Unack". "Still on it" is recorded in the Timeline and does not reset the interval; "Unack" runs
  Unacknowledge. A Takeover starts Reminders over for the new Owner; a Reopen into acknowledged continues them; snoozed
  Alert Groups get none; there are no quiet hours.
- **C-17.FR-5** Auto-unacknowledge is a Route policy field (`route.auto_unacknowledge`), off by default. The Owner
  answers a Reminder by pressing "Still on it", by running any command on the Alert Group or by adding a Note to it
  before the next Reminder is due. When it is on and the Owner has answered neither of the last
  `timers.auto_unacknowledge_after` Reminders, the next Reminder is not sent; instead the system unacknowledges the Alert
  Group with the reason "No answer to the last two Reminders": it becomes firing without an Owner, a Loud Thread reply
  gives the reason and mentions the Owner, and the ack timeout starts over. A Reminder that reached no Destination does
  not count as unanswered.
- **C-17.FR-6** Buttons on Reminders are signed like Root message buttons but are not re-rendered after a key rotation;
  once the old key is removed they are answered privately with "This button has expired; use the buttons on the Alert
  Group's message".
- **C-17.FR-7** Timer rows are claimed by any replica. After downtime, the overdue notices of one Alert Group collapse
  into one Loud Thread reply saying how many were missed.
- **C-17.FR-8** Disabling or deleting the Owner releases the acknowledgement (C-03.FR-13): the Alert Group becomes
  firing without an Owner, so its Reminders end with the acknowledgement, no auto-unacknowledge follows, and the ack
  timeout starts over (FR-1) and can make it Unclaimed again (FR-3). Enabling the User again restores neither the
  acknowledgement nor the Reminders; the next Acknowledge starts them over.
- **C-17.FR-9** The Alert Group page shows the next scheduled notice or Reminder.
- **C-17.FR-10** The Owner can answer a Reminder from the Alert Group page and through the API with "Still on it"; the
  answer is recorded like a press in a messenger. This is also the way to answer where buttons on Thread replies do not
  work in a messenger ([open question 6](../L1.md#51-test-environment-facts)). Only a User can be the Owner, so a
  Service account token is refused with the code `owner_must_be_user`; a Personal access token acts as its User.
- **C-17.FR-11** Timers add these rows to the lifecycle event table of C-09.FR-22, with the same columns and meaning.

  | Event | When | Loud or Quiet | Mentions | Timeline kind | In a messenger (C-11) |
  |---|---|---|---|---|---|
  | `ack_timeout` | an ack timeout notice (FR-2) | Loud | `ack_timeout` | `timers` | Thread reply |
  | `unclaimed` | the last notice has been sent (FR-3) | Quiet | — | `timers` | Root message update (the last notice already rang) |
  | `reminder` | a Reminder (FR-4) | Loud | `owner` | `timers` | Thread reply with "Still on it" and "Unack" |
  | `reminder_answered` | "Still on it" from a messenger, the UI or the API | Quiet | — | `timers` | nothing; the press is answered privately |
  | `auto_unacknowledged` | auto-unacknowledge (FR-5) | Loud | `owner` (the Owner before the transition, who loses the Alert Group; C-09.FR-22) | `status` | Thread reply with the reason |
  | `notices_missed` | overdue notices of one Alert Group collapsed after downtime (FR-7) | Loud | as the notices it stands for: `ack_timeout` for ack timeout notices, `owner` for Reminders | `timers` | one Thread reply saying how many were missed |

## UI

Route policy fields (ack timeout on/off and first interval, ack timeout notice template, Reminders on/off with their
first interval and cap, auto-unacknowledge); Unclaimed filter, column and badge; next scheduled notice on the Alert Group
page; "Still on it" for the Owner while a Reminder is unanswered.

## API surface

Policy fields on `routes`; `unclaimed` and the next notice time on `alert-groups`; `alert-groups/{id}/still-on-it`
(create, Owner only); the Reminder button actions through C-13 and C-14.

## Acceptance

Checked with a virtual clock, the recording test adapter of C-11 and the fake messenger servers.

- **C-17.AC-1** A firing Alert Group gets notices at exactly 15, 45 and 105 minutes and is Unclaimed after the third.
- **C-17.AC-2** Acknowledging at minute 20 stops further notices; Unacknowledge at minute 30 makes the next notice come
  at minute 45.
- **C-17.AC-3** Reminders come at 4, 12, 28 and 52 hours after the acknowledgement whether or not "Still on it" was
  answered; a Takeover at hour 13 moves the next Reminder to hour 17.
- **C-17.AC-4** A Route created with the Informational profile produces no notices and no Reminders.
- **C-17.AC-5** With auto-unacknowledge on and no answer to the Reminders at hours 4 and 12, there is no Reminder at hour
  28; instead the Alert Group is firing without an Owner, the Timeline has `auto_unacknowledged` with the reason,
  `loudness` `loud` and `mentions` `[owner]`, the recording test adapter receives a Thread reply mentioning the previous
  Owner, and the next ack timeout notice comes 15 minutes later. With a Note by the Owner at hour 5, the Reminder at hour
  28 is sent as usual.
- **C-17.AC-6** The Unclaimed filter of the Alert Group list returns the Alert Group after its third notice.
- **C-17.AC-7** Reminder Thread replies carry the buttons "Still on it" and "Unack" in the fake Mattermost and Telegram
  servers; in the fake Mattermost server, the private answer to a press on them is an ephemeral post whose `root_id` is
  the Root message (C-13.FR-4).
- **C-17.AC-8** An Unclaimed Alert Group stops being Unclaimed — in the UI and in the Root message — when it is
  acknowledged, snoozed or resolved; after Unacknowledge it is not Unclaimed again until the third notice of the new ack
  timeout.
- **C-17.AC-9** Every row of the lifecycle event table of FR-11 records exactly one Timeline entry with the row's
  `event`, kind, `loudness` and `mentions` (a table-driven test).

## Related ADRs

ADR-0004, ADR-0005, ADR-0007, ADR-0011, ADR-0016.

## Depends on

C-10 — commands and Owners; C-12 — the ack timeout template and texts; C-13 and C-14 — buttons on Thread replies.

## Suggested story split

- **BE** — ack timeout, Unclaimed, Reminders, auto-unacknowledge, collapsed overdue notices, "Still on it", lifecycle
  events.
- **FE** — Route policy fields, Unclaimed filter and badge, next notice and "Still on it" on the Alert Group page.
