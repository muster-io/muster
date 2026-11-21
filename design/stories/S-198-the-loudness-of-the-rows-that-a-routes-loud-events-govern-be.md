---
id: S-198
title: The loudness of the rows that a Route's Loud events govern (BE)
capability: C-36
kind: be
layer: L2
depends_on: [S-197, S-140, S-121, S-169, S-155, S-044]
covers: [C-36.FR-1, C-36.FR-2, C-36.FR-3, C-36.FR-4, C-36.FR-5, C-36.AC-2, C-36.AC-3, C-36.AC-4, C-36.AC-9, C-09.FR-4, C-09.FR-6, C-09.FR-9, C-09.FR-22, C-15.FR-2, C-17.FR-2, C-17.FR-11, C-26.FR-5, C-28.FR-4]
files_touched:
  - internal/groups/loudness.go
  - internal/groups/loudness_test.go
  - internal/groups/events.go
  - internal/groups/events_test.go
  - internal/groups/dispatcher.go
  - internal/groups/dispatcher_test.go
  - internal/groups/grouping.go
  - internal/groups/timers.go
  - internal/groups/timers_test.go
  - internal/groups/query.sql
  - internal/groups/groups_integration_test.go
  - internal/delivery/enqueue.go
  - internal/delivery/enqueue_test.go
  - internal/delivery/webhookevents.go
  - internal/delivery/webhookevents_test.go
  - internal/delivery/storm.go
  - internal/delivery/storm_test.go
  - internal/delivery/worker.go
  - internal/delivery/recovery.go
  - internal/delivery/query.sql
  - internal/delivery/loudness_integration_test.go
  - internal/fakes/faketelegram/chats.go
  - internal/fakes/faketelegram/faketelegram_test.go
  - test/e2e/loud_and_quiet_test.go
acceptance:
  - "[C-36.FR-2, C-36.FR-3, C-36.AC-2] On a Route whose `loud_events` is `[new_alert_group]`, a new Alert Group records `created` Loud with `[new_alert_group]`; a new Alert joining it while it is firing records `alerts_added` with `loudness` `quiet` and no Mentions, and the fake Telegram server receives that Thread reply with `disable_notification` true while the fake Mattermost server receives it without Mentions; a Reopen into acknowledged records `reopened` Loud with `[owner]` (`loudness_test.go`, `groups_integration_test.go`, `test/e2e/loud_and_quiet_test.go`)."
  - "[C-36.FR-2, C-36.AC-3] A table-driven test over every row of C-36.FR-2: with its Loud event off the row is recorded Quiet without Mentions, or for a row about the Owner Loud with only `owner`; with it on, as the lifecycle event tables say; `takeover`, `reminder`, `auto_unacknowledged`, `unacknowledged` (Owner disabled or deleted) and `paged` by Mention stay Loud with their Mentions whatever `loud_events` holds; `notices_missed` is Quiet for ack timeout notices with `ack_timeout` off and Loud for Reminders (`loudness_test.go`, `events_test.go`)."
  - "[C-36.FR-3, C-36.AC-2] A Quiet row appears as the row says, Quietly: the same new Root message, Thread reply or Storm summary, with `disable_notification` in Telegram, without Mentions in Mattermost, Slack and Bridges, and with `notify` false in the events-mode outgoing webhook event; the Mention entries of a Destination for that event mention nobody and stay as they are (`enqueue_test.go`, `webhookevents_test.go`, `loudness_integration_test.go`)."
  - "[C-36.FR-4, C-36.AC-4] The loudness is decided when the entry is recorded, from the Route's settings at that moment: taking `reopen` off a Route leaves a `reopened` entry recorded before the save Loud, also when its Thread reply is sent after the save; the next Reopen into firing is Quiet; the save edits no Root message and sends nothing; the Route that decides is the Alert Group's, also in the Destinations of its Teams (`groups_integration_test.go`, `loudness_integration_test.go`)."
  - "[C-36.FR-3, C-36.FR-4, C-36.AC-9] With `storm_started` off, the first publication of a Storm summary of the Route is Quiet and carries no Mentions; with `new_alert_group` off, `destination_joined` into a Team's Destination is Quiet and so is the Publication after a Broken Destination recovers of a firing Alert Group (`storm_test.go`, `loudness_integration_test.go`, `test/e2e/loud_and_quiet_test.go`)."
  - "[C-36.FR-5] Loudness changes nothing else: with `loud_events` empty of every event but the one that a case needs, the statuses, the Destinations an Alert Group reaches, the ack timeout, Reminders, Unclaimed, Storms and the Escalation chains run as with all seven, a Quiet `created` still starts the chains and Pages stay Loud (`groups_integration_test.go`, `test/e2e/loud_and_quiet_test.go`)."
verify: "make ci test-integration e2e-go"
operator_attention: false
issue: null
---

# S-198. The loudness of the rows that a Route's Loud events govern (BE)

## Scope

**IN**

- The decision, when a lifecycle event is recorded, whether a governed row is Loud or Quiet from the `route.loud_events`
  of the Alert Group's Route, with the Mentions of a Quiet row left out, and the rows that stay Loud on every Route
  (C-36.FR-2, FR-3, FR-4).
- Delivery reading that loudness from the recorded entry instead of from its own table, for Root messages, Thread
  replies, the Publication after a Broken Destination recovers, the first publication of a Storm summary and events-mode
  outgoing webhooks (C-36.FR-3).
- That nothing but loudness changes (C-36.FR-5).

**OUT**

- The two fields, their migration, the profiles and the API: S-197, which this story reads.
- The removal of an acknowledgement by new Alerts, and the row `alerts_added` (removes) that carries `owner` and
  `new_alerts`: S-199. This story gives that row its place in the governed list of FR-2 so that S-199 only records it.
- The form: S-200. The documentation: S-201.

## Contracts

- **Where the decision sits** (ADR-0016): the dispatcher of `internal/groups` records every entry in one place
  (`dispatcher.go`, where it reads `rowOf(e.Event, e.Variant)`); this story inserts there a resolver `loudness.go`:
  `loudnessOf(row, settings)` returns the loudness and Mentions the entry is recorded with. `Table` in `events.go`
  keeps the loudness of L1 as the default of every row, which is what a Route with all seven Loud events and a row that
  no Loud event governs get. The settings are those of the Alert Group's Route read in the transaction of the change
  (`groups/query.sql` `GetRoutePolicy` and `GetTimerPolicy` gain `loud_events`), so an edit applies at once and one
  recorded before it keeps what it recorded; the timers read them when they fire, as they read the rest of the timer
  policy. `groups` does not import `routing` for it, as for the other policy fields.
- **The governed rows** are the table of C-36.FR-2, implemented once in `loudness.go`: `created`; `destination_joined`
  while firing; `alerts_added` while firing; `reopened` into firing; `ack_timeout` and `notices_missed` standing for ack
  timeout notices; `snooze_ended`; `urgency_raised` that ends a Snooze (Quiet without Mentions when `rise_to_urgent` is
  off); and, for the rows about the Owner, the Mentions of the entries of a Destination only — `reopened` into
  acknowledged, `urgency_raised` that removes the acknowledgement and `alerts_added` that removes it (S-199) stay Loud
  with the Mention `owner`, whose Loud event decides only whether the Destinations' entries are mentioned too. The rows
  that always stay Loud are listed in the same file, and a test fails when a row of `Table` is in neither list: a new
  lifecycle event must say which it is.
- **`notices_missed`** keeps one row and takes the loudness and Mentions of the notices it stands for (C-36.FR-2): the
  timer records it Quiet when it stands for ack timeout notices of a Route whose `loud_events` lacks `ack_timeout`.
- **Delivery** (`internal/delivery`): `enqueue.go` takes `Loudness` and `Mentions` from `groups.Recorded` for every
  lifecycle event and keeps its `Table` only for the messenger form; the Publication of a `created` or
  `destination_joined` is Loud or Quiet by the entry, and a Quiet one in Telegram carries `disable_notification`
  through the adapter's existing path; `webhookevents.go` sets `notify` from the entry. The delivery events of the Loud
  and Quiet table of reference.md follow the settings of the Route at the moment delivery decides: the Publication after
  a Broken Destination recovers (`new_alert_group`, `recovery.go`) and the first publication of a Storm summary
  (`storm_started`, `storm.go`, `worker.go`, with `GetRouteDelivery` in `delivery/query.sql` reading `loud_events`);
  a Storm summary takes the settings of its Route. A Thread reply queued before a save keeps the loudness it was queued
  with (`thread_replies.loudness` is already stored).
- **Chains and timers** (C-36.FR-5): none of `escalation`, the ack timeout, Reminders or Unclaimed reads the loudness;
  a test with a Quiet `created` proves the chains still start.
- **Fakes**: `faketelegram` records `disable_notification` on every sent message so that the live check can assert it;
  the fake Mattermost and Slack servers already record the text a post carries.
- **Log events and metrics**: none new; `reference.md` already states the revised rules and needs no change.
- **Settings**: `route.loud_events`.

## Steps

1. Write `loudness.go` with the two lists and its table-driven test, and the settings queries. Check:
   `loudness_test.go`, `events_test.go` (every row of `Table` is governed or always Loud).
2. Call the resolver where the dispatcher records an entry, and in the timers. Check: `dispatcher_test.go`,
   `timers_test.go`, `groups_integration_test.go` (a Reopen recorded before a save keeps Loud).
3. Make delivery read the recorded loudness and Mentions, and the two delivery events the settings. Check:
   `enqueue_test.go`, `webhookevents_test.go`, `storm_test.go`, `loudness_integration_test.go`.
4. Extend the fake Telegram server; write `test/e2e/loud_and_quiet_test.go`. Check: the Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
API=localhost:8080/api/v1; CLK=localhost:8082/_dev/clock   # A: the Admin's token
# the Route "certificates" with loud_events ["new_alert_group"] (Informational profile) for an Integration with NOTIFY and PUTA
# a new Alert Group is Loud; a joining Alert and a Reopen into firing are Quiet
PUTA e1 a '{"labels":{"alertname":"CertExpiring","severity":"warning"}}'; NOTIFY e1 '{"reason":"first notification"}'
G=$(AG 'alertname%3D%22CertExpiring%22')
PUTA e1 b '{"labels":{"alertname":"CertExpiring","severity":"warning","host":"b"}}'; NOTIFY e1 '{"reason":"another alert"}'; sleep 2
curl -s -H "$A" "$API/alert-groups/$G/timeline?kind=alerts,status" | jq -c '[.items[] | {e: .event, l: .loudness, m: .mentions}]'
# [{"e":"created","l":"loud","m":["new_alert_group"]},{"e":"alerts_added","l":"quiet","m":[]}]
curl -s 127.0.0.1:18081/_fake/requests | jq -c '[.[] | select(.method == "sendMessage") | {t: (.body.text | .[0:30]), silent: .body.disable_notification}] | last'
# {"t":"...","silent":true}   the Thread reply of the new Alert in the fake Telegram server
curl -s 127.0.0.1:18065/_fake/requests | jq -r '[.[] | select(.path | test("/posts$"))] | last | .body.message' | grep -c '@'   # 0: no Mention in the Mattermost reply
# a Route edit applies to the events recorded after it only
curl -s -H "$A" -H "If-Match: $E" -X PUT $API/routes/$R -d "{...loud_events with \"reopen\" removed...}" > /dev/null
# resolve all Alerts, wait out the Reopen path with the development clock, fire again: the Reopen is Quiet
curl -s -X POST $CLK -d '{"advance_seconds": 120}' > /dev/null
curl -s -H "$A" "$API/alert-groups/$G/timeline?kind=status" | jq -c '[.items[] | select(.event=="reopened") | .loudness]'   # ["quiet"]
# a Storm: storm_started off, 25 Alert Groups in a minute; the summary has no Mentions and silent true
# ack timeout notice: a Route with ack_timeout off in loud_events and the ack timeout on: advance 15 minutes, the notice is quiet
curl -s -X POST $CLK -d '{"advance_seconds": 900}' > /dev/null; sleep 2
curl -s -H "$A" "$API/alert-groups/$G/timeline?kind=timers" | jq -c '[.items[] | {e: .event, l: .loudness}]'   # [{"e":"ack_timeout","l":"quiet"}]
# stays Loud on every Route: acknowledge as Alice, then Takeover as Bob
curl -s -H "$B" -X POST $API/alert-groups/$G/acknowledge > /dev/null
curl -s -H "$A" "$API/alert-groups/$G/timeline?kind=status" | jq -c '[.items[] | select(.event=="takeover") | {l: .loudness, m: .mentions}]'   # [{"l":"loud","m":["previous_owner"]}]
```

`test/e2e/loud_and_quiet_test.go` repeats these checks and adds the rows of `Table` one by one, the events-mode outgoing
webhook with `notify` false, a Quiet `created` that still starts a chain, the Destinations of a Team
(`destination_joined`), the Publication after a Broken Destination recovers, and the Reopen recorded before a save and
sent after it.

## Open questions

None.

## Notes

- Suggested commit: `feat(groups): record the rows a route's loud events govern as quiet when the route does not list them`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases. The story needs no
  migration: both columns are S-197's.
- The first publication of a Storm summary is a delivery event, not a lifecycle entry, so it reads the Route's settings
  at publication time and records no Timeline entry (C-11.FR-21).
- C-36.FR-4 settles `destination_joined` into a Team's Destination: the Route of the Alert Group decides, so the first
  publication there of a firing Alert Group is Quiet when the Route lacks `new_alert_group`.
- The rows of C-34 (S-191's `maintenance_started` and `maintenance_ended`) and the rows of C-37 (S-203's and S-204's) are
  in `Table` too: the test that every row is governed or always Loud meets them. Whichever of the stories merges second
  classifies the rows of the other — S-198 when it merges after S-191, S-191 when it merges after S-198 — as S-204 does
  for the rows it records; until then they are listed as always Loud with their own rules.
- A question for the maintainer (non-blocking, a PRD gap): C-34.FR-13 makes `maintenance_ended` Loud with the Mentions of
  the entries for `new_alert_group` when the Alert Group is firing and nobody owns it, and C-36 does not say whether a
  Route that makes `created` Quiet (no `new_alert_group` in `loud_events`) makes it Quiet too. This story keeps it Loud
  (not governed by a Loud event) until the maintainer says otherwise.
- The Quiet `alerts_added` of an acknowledged or snoozed Alert Group keeps its L1 row; only the row of a firing Alert
  Group is governed by `new_alerts`.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-36.FR-1 | partial | edits apply at once and read the Alert Group's Route (here); the fields and the API are S-197 |
| C-36.FR-2 | partial | every row but the removal's; the row `alerts_added` (removes) is recorded by S-199 with the list of this story |
| C-36.FR-3 | full | |
| C-36.FR-4 | full | |
| C-36.FR-5 | full | |
| C-36.AC-2 | full | |
| C-36.AC-3 | partial | every row but the removal's, which S-199 adds to the same table-driven test |
| C-36.AC-4 | full | |
| C-36.AC-9 | full | |
| C-09.FR-4 | partial | revised for L2: the Reopen into firing as a Loud event of the Route; a Reopen into acknowledged keeps its Owner and its Alerts remove nothing are S-199's, a Reopen never attaches again S-203's |
| C-09.FR-6 | partial | revised for L2: new Alerts as a Loud event of the Route; the removal of an acknowledgement is S-199's, new Alerts in an attached Alert Group S-203's |
| C-09.FR-9 | partial | revised for L2: `rise_to_urgent` as a Loud event; the order against new Alerts is S-199's |
| C-09.FR-22 | partial | revised for L2: the loudness a Route's Loud events give the rows; the row `alerts_added` (removes) is S-199's |
| C-15.FR-2 | partial | revised for L2: `notify` of an events-mode webhook event from the loudness a Route gives the event |
| C-17.FR-2 | partial | revised for L2: the ack timeout notice Quiet where the Route does not list `ack_timeout` |
| C-17.FR-11 | partial | revised for L2: `ack_timeout` and `notices_missed` take the loudness of the Route's Loud events |
| C-26.FR-5 | partial | `destination_joined` Quiet where the Route does not list `new_alert_group` |
| C-28.FR-4 | partial | an entry for a Loud event that the Route makes Quiet mentions nobody there |
| reference.md Loud and Quiet | partial | revised for L2: the Loud events of a Route and the rows that stay Loud; the removal of an acknowledgement is S-199's |
