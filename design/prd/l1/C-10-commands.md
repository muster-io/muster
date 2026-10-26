# C-10. Commands

[L1 index](../L1.md) · Stage: Observe · UI: yes · Depends on: C-04, C-09

**Goal.** People and automation change Alert Groups only through named commands with clear preconditions, the same
result from every Transport, and an Audit log entry for each — one at a time from the Alert Group page, or several at
once from the list.

Names in the form `area.setting` refer to rows of [defaults.md](defaults.md). Every command that changes an Alert Group
records a lifecycle event of FR-15 with its loudness and Mentions; Destinations receive the events from C-11 on.

## Scenarios

1. A responder presses Acknowledge on `#412` in the UI and becomes its Owner.
2. A second responder acknowledges the same Alert Group; it moves to them, and the Timeline records a Takeover from
   Alice (once delivery exists, a Loud Thread reply mentions Alice).
3. A responder snoozes `#412` until 09:00 tomorrow from the UI; another snoozes a known-noisy Alert Group with no end
   and confirms the warning.
4. A responder resolved the wrong Alert Group; they press Unresolve in the UI and it is firing again.
5. A script with a Personal access token resolves an Alert Group; the Audit log shows the token and the Transport `api`.
6. A Viewer tries to acknowledge and is refused.
7. After a noisy deploy, a responder selects eleven firing Alert Groups in the list and resolves them in one action;
   one of them had already resolved, and the result says so.
8. A responder filters the list by "Mine" to see what they own.
9. At 02:00 a responder in Berlin presses "Until tomorrow 09:00" on a message in Telegram: the Alert Group comes back at
   09:00 her time, seven hours later, not 31. On Friday evening a teammate snoozes another one "Until Monday 09:00".

## Functional requirements

- **C-10.FR-1** The commands, their preconditions and effects are:

  | Command | Allowed from | Effect |
  |---|---|---|
  | Acknowledge | firing, snoozed | → acknowledged with the caller as Owner; ends a Snooze. By the current Owner: no change, success. By another user: Takeover |
  | Unacknowledge | acknowledged | → firing without an Owner; ack timeout starts over. Allowed to every user with the Permission, not only the Owner; the Timeline names the Owner who lost the Alert Group |
  | Resolve | firing, acknowledged, snoozed | → resolved by the user; an optional Note in the same request |
  | Unresolve | resolved by a user | → firing without an Owner; Alerts still firing in the last Snapshot become active; ack timeout starts over |
  | Snooze(until) | firing, acknowledged, snoozed | → snoozed until a future time, or with no end (UI and API only) |
  | Unsnooze | snoozed | → firing without an Owner; ack timeout starts over |
  | Add Note | any | Timeline entry with author and Transport |

  The Owner is always a User. A Service account cannot become an Owner: Acknowledge from it is refused with the code
  `owner_must_be_user` (C-04.FR-2); it may Resolve, Snooze and add Notes. A Personal access token acts as its User.
  _Revised for L2 (2026-10-26):_ Acknowledge, Snooze and Resolve also stop every Escalation chain of the Alert Group and
  clear its Assignees (C-32.FR-5, C-32.FR-12). An Acknowledge reported by a Notification gateway, with the Transport
  `gateway`, applies only while nobody owns the Alert Group: firing or snoozed → acknowledged; acknowledged → unchanged,
  and the person is told who has it; it never makes a Takeover (C-30.FR-6).

- **C-10.FR-2** Refusals carry a stable code and a message: "already resolved" (Acknowledge, Resolve, Snooze on
  resolved); "not acknowledged"; "not snoozed"; "not resolved", "resolved automatically — it reopens by itself when an
  alert returns", "its Route was deleted" (`route_deleted`), "a newer open Alert Group #N exists" and "all alerts have
  already resolved" (Unresolve, checked in this order); "a Service account cannot be an Owner" (Acknowledge and "Still
  on it" from a Service account); "not permitted". The API returns them as `Problem` documents with `409` for
  preconditions and `403` for permissions.
- **C-10.FR-3** Every command passes through one dispatcher: permission → precondition → transition → Audit log →
  Timeline → re-render. The Transport (`ui`, `api`, `mattermost`, `telegram`, `bridge`, `slack`) is recorded;
  transitions Muster starts itself use the Transport `system` and appear in the Timeline only (ADR-0016).
  _Revised for L2 (2026-10-26):_ the Transport `gateway` joins them (C-30.FR-6), and `mcp` comes with the MCP server
  later in L2; the [Transports](reference.md#transports) are listed in reference.md.
- **C-10.FR-4** A Takeover needs no confirmation: ownership moves to the new user, a Loud event names both users and
  mentions the previous Owner, Reminders start over for the new Owner (C-17), and the Audit log records it as a
  Takeover.
- **C-10.FR-5** A status change made by a person is Quiet (a Root message update); the Takeover is the only Loud result
  of a command.
- **C-10.FR-6** Snooze accepts any future time or no end in the UI and the API, and the API asks for the choice
  explicitly: a request names either `until` or `no_end`, and one with neither is refused; a bulk Snooze does the same.
  Messengers offer the Route's Snooze durations (`route.snooze_durations`, a Route policy field added here). FR-17 and
  FR-18 add the Snooze presets: a third choice, `preset`, in the API, and two more choices in the UI and in messengers.
  A Snooze with no end requires confirming the warning "This Alert Group stays snoozed until someone unsnoozes it". The
  Owner of an acknowledged Alert Group that is snoozed is kept in the Timeline; when the Snooze ends, the Alert Group is
  firing without an Owner.
- **C-10.FR-7** Unresolve is offered only in the UI and the API, never in messengers.
- **C-10.FR-8** Notes are added from the UI and the API, up to `alert_group.note_max_length` characters, and are shown
  in the Timeline with their author and Transport. A Note is kept as long as its Alert Group's summary row, not with the
  details (C-09.FR-16).
- **C-10.FR-10** Commands on one Alert Group are serialized; each is checked against the state left by the previous one,
  so two simultaneous Acknowledges result in one acknowledgement and one Takeover.
- **C-10.FR-11** Commands from messengers arrive through C-13 and C-14 and are answered privately there; presses from
  accounts without an Account link, from disabled users and from Viewers are refused (C-18).
- **C-10.FR-12** Every command has its own Audit log type, including Takeover.
- **C-10.FR-13** The Alert Group list (C-09.FR-13) gains the filters Owner (a user, "me" or nobody) with a "Mine" quick
  filter, and "snoozed with no end", and the columns Owner and Snooze end; the Alert Group page shows the Owner, the
  Snooze end and who set the Snooze, which the Alert Group keeps until the Snooze ends. The Owner picker reads the user
  directory (`user-directory`: name, login and whether the user is deactivated), which every reader of Alert Groups may
  use, unlike the Users page.
- **C-10.FR-14** Bulk commands: from the list, a user selects up to `alert_group.bulk_max` Alert Groups and runs
  Acknowledge, Resolve, Snooze or Unsnooze on all of them. Each Alert Group goes through the dispatcher on its own, with
  its own Audit log entry and Timeline entry; the result lists the outcome per Alert Group, refusals included, and a
  refusal of one never stops the others. A bulk Acknowledge skips every Alert Group that another user owns, with the
  outcome "skipped: owned by {Owner}", so it never makes a Takeover; a Takeover is made one Alert Group at a time. An
  id that names no Alert Group the caller can see gets the outcome "failed: not found".
- **C-10.FR-15** Commands add these rows to the lifecycle event table of C-09.FR-22, with the same columns and meaning.
  Acknowledge by the current Owner changes nothing and records no event; Resolve with a Note records `resolved` and then
  `note_added`.

  | Event | When | Loud or Quiet | Mentions | Timeline kind | In a messenger (C-11) |
  |---|---|---|---|---|---|
  | `acknowledged` | Acknowledge of an Alert Group that nobody owns | Quiet | — | `status` | Root message update |
  | `takeover` | Acknowledge of an Alert Group that another user owns (FR-4) | Loud | `previous_owner` | `status` | Thread reply naming both users |
  | `unacknowledged` | Unacknowledge | Quiet | — | `status` | Root message update |
  | `resolved` | Resolve by a person | Quiet | — | `status` | Root message update |
  | `unresolved` | Unresolve | Quiet | — | `status` | Root message update |
  | `snoozed` | Snooze, also when it only changes the Snooze end | Quiet | — | `status` | Root message update |
  | `unsnoozed` | Unsnooze | Quiet | — | `status` | Root message update |
  | `note_added` | Add Note | Quiet | — | `notes` | nothing; Notes are shown only in Muster |

- **C-10.FR-16** Each Alert Group read through the API carries `allowed_commands`: the Commands the current status
  allows for the caller's Permissions and identity — the commands of FR-1 plus `still_on_it` (the caller is the Owner
  and a Reminder is waiting, C-17.FR-10) and `add_note` (the caller may add Notes). A Service account is never offered
  Acknowledge or "Still on it".
- **C-10.FR-17** Snooze presets. Besides a duration, a time or no end, a Snooze can end at one of two Snooze presets,
  whose end Muster computes when the Command runs, on the business clock, from the start of the working day
  (`organization.workday_start`) and the first day of the week (`organization.week_start`):

  | Preset | API value | Label | Ends at |
  |---|---|---|---|
  | Until tomorrow | `tomorrow` | "Until tomorrow {start}", such as "Until tomorrow 09:00" | the first start of the working day after now |
  | Until the first day of the week | `week_start` | "Until {day} {start}", such as "Until Monday 09:00" | the first start of the working day on the first day of the week after now |

  "After now" is strict: at 09:00 sharp the next start is the next day's. Before the working day starts, "tomorrow" is
  the day about to start — the night belongs to the day before it — so at 02:00 "Until tomorrow 09:00" ends at 09:00
  the same day, and at 08:59 one minute later. There is no working-week calendar: Saturday and Sunday are ordinary days
  for `tomorrow`, and `week_start` is the preset for a weekend. The start is the wall-clock time in the preset's time
  zone, by that zone's rules on the day of the end: a start that a clock change skips moves forward by the length of
  the change (02:30 on a day the clocks jump from 02:00 to 03:00 is 03:30), and one that happens twice is the first of
  the two. The time zone is the request's `time_zone` when it names one (the UI sends the user's), else the profile
  time zone of the User the caller acts as (C-03.FR-12) — a Personal access token acts as its owner — else the
  Organization's (`organization.time_zone`); a Service account has no profile. The end is stored as an instant: a later
  change of the settings or of a time zone never moves a Snooze already set, and the Snooze ends like any other
  (C-09.FR-8, C-09.FR-12).
- **C-10.FR-18** Where the Snooze presets are offered and what they record. The UI's Snooze dialog and the bulk Snooze
  offer both presets after the Route's durations, each with the end it would set now (`snooze-presets`, which computes
  the ends of FR-17 for a time zone and changes nothing). The API takes `preset`, with an optional `time_zone`, as a
  third choice beside `until` and `no_end` — exactly one of the three — and a bulk Snooze with a preset computes the end
  once, so every Alert Group of the request gets the same end. Messengers offer both presets after the Route's Snooze
  durations — as buttons in Mattermost, Telegram and Bridges with buttons
  ([reference.md](reference.md#buttons-and-links-by-status)), and as Text commands through Bridges (C-22.FR-14) — and
  only on a Route with Snooze durations: an empty `route.snooze_durations` keeps Snooze out of messengers, presets
  included. A press or a Text command takes the profile time zone of the linked User, else the Organization's. A preset
  Snooze is an ordinary Snooze: the `snoozed` event of FR-15 with its `snooze_until`, the footer of the Root message
  (C-12.FR-1) and the Audit log entry of the Snooze, whose details name the preset and the time zone beside the end. A
  change of the two settings shows on the buttons of a Root message from its next re-render, as a new
  `organization.time_zone` does in message times; a press always computes with the current settings.

## UI

Command buttons on the Alert Group page and in list rows, enabled according to the current status, with "Take over from
{Owner}" when another user owns the Alert Group; Snooze dialog with the Route's quick durations, the Snooze presets with
the end each would set, a date-time picker and "no end" with its warning; Resolve dialog with an optional Note;
Unresolve with its refusal messages; Note box on the Timeline; row selection with bulk actions and a per-row result;
Owner and "Mine" filters. Commands, dialogs and the Note box work at phone width (C-09.FR-24).

## API surface

`alert-groups/{id}/acknowledge`, `…/unacknowledge`, `…/resolve`, `…/unresolve`, `…/snooze`, `…/unsnooze` (create);
`alert-groups/{id}/notes` (list, create); `alert-groups/bulk-commands` (create: command, Alert Group ids and arguments →
outcome per Alert Group); `user-directory` (list users for pickers and filters); `snooze-presets` (read the ends of the
Snooze presets now, for a time zone).

## Acceptance

- **C-10.AC-1** Acknowledge on a resolved Alert Group returns `409` with the code "already resolved" and changes
  nothing.
- **C-10.AC-2** Unresolve on an Alert Group resolved by the system returns `409` with "resolved automatically".
- **C-10.AC-3** Two users acknowledging within the same second leave the Alert Group with the later one as Owner and a
  single Takeover entry.
- **C-10.AC-4** A Viewer's Acknowledge returns `403`; nothing is written except a refusal log line.
- **C-10.AC-5** Each command made through the API appears once in the Audit log with its type, actor, token and
  Transport `api`.
- **C-10.AC-6** Resolve all Alerts of an acknowledged Alert Group by `resolved` and re-fire one within the Reopen window:
  the same `#N` is acknowledged by the same Owner, its Reopen count is 1, and its Timeline has a `reopened` entry with
  `loudness` `loud` and `mentions` `[owner]`.
- **C-10.AC-7** A new `startsAt` without `resolved` on an acknowledged Alert Group keeps it acknowledged and records
  `alert_continued` with `loudness` `quiet` and no entry with `loudness` `loud`.
- **C-10.AC-8** After a manual resolve with Alerts still firing, a new Alert Group appears after the Grace period and not
  before, marked "firing again after a manual resolve of #N".
- **C-10.AC-9** A new Alert in an acknowledged Alert Group keeps the acknowledgement and records `alerts_added` with
  `loudness` `quiet`.
- **C-10.AC-10** With a virtual clock, a Snooze until T on a firing Alert Group whose Alerts keep firing makes it firing
  without an Owner at T, with a `snooze_ended` entry — `loudness` `loud`, `mentions` `[snooze_ended]` — listing what
  accumulated.
- **C-10.AC-11** A bulk Resolve of three Alert Groups, one of them already resolved, resolves two, refuses one with
  "already resolved", and writes two Audit log entries.
- **C-10.AC-12** In a Route whose Group key lacks `severity`, with `route.urgent_rise_removes_ack` on, a critical Alert
  joining an acknowledged warning Alert Group makes it firing without an Owner and records `urgency_raised` with
  `loudness` `loud` and `mentions` `[owner, rise_to_urgent]`; with the setting off, the acknowledgement stays and
  `urgency_raised` is Quiet.
- **C-10.AC-13** The same rise ends the Snooze of an Alert Group snoozed while it was not Urgent, with a Loud
  `urgency_raised`; an Alert Group snoozed while it was Urgent, whose critical Alert then resolves and fires again,
  stays snoozed.
- **C-10.AC-14** For Alert Groups of one Route acknowledged 5 and 15 minutes after they started — the second one
  unacknowledged, acknowledged again, taken over and reopened later — and a third resolved without ever being
  acknowledged, the statistics page (C-09.FR-15) shows a median time to acknowledge of 10 minutes over 2 Alert Groups
  for that Route.
- **C-10.AC-15** A bulk Acknowledge by Bob of three firing Alert Groups, one of them owned by Alice, acknowledges two,
  returns "skipped: owned by Alice" for the third, and records no `takeover`.
- **C-10.AC-16** Every row of the lifecycle event table of FR-15 records exactly one Timeline entry with the row's
  `event`, kind, `loudness` and `mentions`, and an Acknowledge by the current Owner records none (a table-driven test).
- **C-10.AC-17** A Snooze request with none of `until`, `no_end` and `preset` (FR-18) gets `422`; a bulk Snooze without
  `snooze` gets `422`; `no_end: true` snoozes with no end.
- **C-10.AC-18** `allowed_commands` of a firing Alert Group lists `acknowledge`, `resolve`, `snooze` and `add_note` for a
  Responder, and no `acknowledge` for a Service account; a bulk Acknowledge by a Service account refuses every item with
  `owner_must_be_user`.
- **C-10.AC-19** `user-directory` lists a deleted user as deactivated and is readable by a Viewer.
- **C-10.AC-20** With a virtual clock, a Note on a resolved Alert Group is still listed, with its author, after
  `retention.alert_details` has passed and the details were removed; once `retention.alert_group_summaries` has passed,
  it is gone together with the summary row.
- **C-10.AC-21** With the working day starting at 09:00, the week on Monday and the time zone Europe/Berlin, the Snooze
  presets end as follows (a table-driven test on a manual clock):

  | Now, Berlin time | `tomorrow` ends | `week_start` ends |
  |---|---|---|
  | Tue 20 Oct 2026 02:00 | Tue 20 Oct 09:00 (07:00Z) | Mon 26 Oct 09:00 (08:00Z) |
  | Tue 20 Oct 08:59 | Tue 20 Oct 09:00 | Mon 26 Oct 09:00 |
  | Tue 20 Oct 09:00 | Wed 21 Oct 09:00 | Mon 26 Oct 09:00 |
  | Tue 20 Oct 23:30 | Wed 21 Oct 09:00 | Mon 26 Oct 09:00 |
  | Fri 23 Oct 18:00 | Sat 24 Oct 09:00 | Mon 26 Oct 09:00 |
  | Sat 24 Oct 10:00 (08:00Z) | Sun 25 Oct 09:00 (08:00Z) | Mon 26 Oct 09:00 (08:00Z): 48 hours, as the clocks go back on 25 Oct |
  | Mon 26 Oct 08:00 | Mon 26 Oct 09:00 | Mon 26 Oct 09:00 |
  | Mon 26 Oct 09:30 | Tue 27 Oct 09:00 | Mon 2 Nov 09:00 |

  In America/New_York, with the working day starting at 02:30, `tomorrow` from Sun 8 Mar 2026 00:00 ends at 03:30 EDT
  (07:30Z), the clocks jumping from 02:00 to 03:00 that night; with 01:30, `tomorrow` from Sun 1 Nov 2026 00:00 ends at
  the first 01:30 (05:30Z).
- **C-10.AC-22** At 02:00 UTC on the development clock, a Snooze with `preset` `tomorrow` and `time_zone` `UTC` sets
  `snooze_until` to 09:00 UTC the same day; the Timeline records `snoozed` with that `snooze_until`, and the Audit log
  entry `alert_group.snoozed` names `preset` `tomorrow` and `time_zone` `UTC`. Without `time_zone`, a User whose
  profile time zone is Asia/Tokyo gets 09:00 Tokyo time, the next day, and a Service account the Organization's time
  zone. A bulk Snooze of two Alert Groups with a preset gives both the same end, and `snooze-presets` lists the ends a
  Snooze then sets. A request with `preset` and `until` gets `422` `one_of_required`, `time_zone` without `preset`
  `422` `unsupported`, and the time zone `Mars/Base` `422` `invalid_format`. Once the development clock passes the end
  while Alerts still fire, the Alert Group is firing with a Loud `snooze_ended`.
- **C-10.AC-23** On a Route with the On-call durations, the Root message of a firing Alert Group carries "Until tomorrow
  09:00" and "Until Monday 09:00" after "Snooze 24 h" in Mattermost and Telegram — «До завтра 09:00» and «До
  понедельника 09:00» on a Route in Russian. A press on "Until tomorrow 09:00" by a linked User whose profile time zone
  is Asia/Tokyo snoozes it until the next 09:00 Tokyo time; by one without a profile time zone, until the next 09:00 of
  the Organization's time zone. A Route without Snooze durations offers neither preset; after the working day is set to
  start at 08:30, the next re-render of a Root message shows "Until tomorrow 08:30".
  _Revised for L2 (2026-10-26):_ from 0.2.0 the durations are those of the Paging profile, called On-call before
  (C-26.FR-11), and the Root message shows the buttons of its Destination's set, all of them by default (C-28.FR-8).
- **C-10.AC-24** The Snooze dialog of the Alert Group page and of a bulk Snooze offers "Until tomorrow 09:00" and "Until
  Monday 09:00" («До завтра 09:00», «До понедельника 09:00») with the end each would set in the user's time zone;
  choosing one snoozes until that end, which the page header then shows; the dialog fits a screen 360 CSS pixels wide
  in both languages.

## Related ADRs

ADR-0004, ADR-0008, ADR-0016.

## Depends on

C-04 — tokens and the Audit log attribution of automation; C-09 — Alert Groups and their state machine.

## Suggested story split

- **BE** — dispatcher, commands, Takeover, Notes, Snooze timers, refusals, bulk commands, lifecycle events, Audit log
  types.
- **FE** — buttons, dialogs, Note box, bulk selection, Owner filters and columns.
- Added with the Snooze presets (FR-17, FR-18), after the messengers and Bridges exist: **BE** — the presets in the
  command layer and the API, with the Organization's start of the working day and of the week (C-20.FR-9); **BE** —
  the presets on the buttons of Mattermost, Telegram and Bridges and in Text commands; **FE** — the presets in the
  Snooze dialog and the bulk Snooze, and the two settings on the General page.
