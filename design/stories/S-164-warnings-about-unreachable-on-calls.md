---
id: S-164
title: Warnings about unreachable On-calls, with the members of a Telegram discussion group (BE)
capability: C-27
kind: be
layer: L2
depends_on: [S-121, S-132, S-133]
covers: [C-27.FR-8, C-27.FR-9, C-27.FR-14, C-27.AC-6]
files_touched:
  - api/openapi.yaml
  - internal/db/migrations/0031_telegram_discussion_members.up.sql
  - internal/db/migrations/0031_telegram_discussion_members.down.sql
  - design/db/schema.md
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - internal/schedules/reach.go
  - internal/schedules/reach_test.go
  - internal/schedules/result.go
  - internal/schedules/oncallnow.go
  - internal/schedules/oncallnow_test.go
  - internal/schedules/query.sql
  - internal/api/schedules.go
  - internal/api/schedules_test.go
  - internal/telegram/destcheck.go
  - internal/telegram/destcheck_test.go
  - internal/destinations/destinations.go
  - internal/destinations/query.sql
  - internal/fakes/faketelegram/chats.go
  - internal/fakes/faketelegram/faketelegram_test.go
  - test/e2e/schedule_reach_test.go
  - docs/concepts/schedules.md
acceptance:
  - "[C-27.FR-8, C-27.AC-6] An On-call without a Mattermost Account link for the Connection of a Mattermost Destination of their Team has, in `getScheduleOnCall`, a warning `unreachable` with the person, the Destination, the messenger `mattermost` and the reason `no_account_link` for the current segment and for each segment of the next `schedule.gap_view` in which they are On-call, and `getOnCallNow` marks the Team's On-call `unreachable` with the same; an On-call with the link has none (`reach_test.go`, `oncallnow_test.go`, `schedule_reach_test.go`)."
  - "[C-27.FR-8] The reasons follow the Destination: `no_account_link` for a Mattermost, Slack, Telegram or Bridge Destination without the person's Account link in the Identity space of its Connection; `bridge_no_native_mentions` for a Bridge Destination whose bridge does not declare `native_mentions`; `webhook_no_thread_reply` for an outgoing webhook Destination in template mode without a \"reply in thread\" request; `not_in_discussion_group` for a Telegram Destination whose last Destination check saw the person outside its discussion group; an outgoing webhook in events mode has no warning (`reach_test.go`)."
  - "[C-27.FR-8] The Destination check of a Telegram Destination of a Team calls `getChatMember` of its discussion group for each person of the Teams that name it who has a Telegram Account link for its Connection, as F-NNN of S-132 says, records the Muster Users who are not members with the time of the check, and passes with the item `discussion_group_members` whose `absent_users` names them; a failed `getChatMember` call of one person leaves that person unknown and does not fail the check (`destcheck_test.go`)."
  - "[C-27.FR-9] `getOnCallNow` gives each Team's On-call `unreachable` ({`destination`, `messenger`, `reason`}[]) for the current segment (`oncallnow_test.go`)."
  - "[C-27.FR-14] Concepts → Schedules has \"Unreachable On-calls\" with each reason and what to do about it; `make docs-check` passes."
verify: "make ci test-integration e2e-go docs-check"
operator_attention: false
issue: null
---

# S-164. Warnings about unreachable On-calls, with the members of a Telegram discussion group (BE)

## Scope

**IN**

- The warning that the On-call of a Schedule, now or in a segment of the next `schedule.gap_view`, cannot be mentioned
  in the Destinations of their Team: no Account link in the Identity space of a Destination's Connection, a bridge
  without `native_mentions`, an outgoing webhook in template mode without "reply in thread", or a person outside the
  discussion group of a Telegram channel as its Destination check saw it (C-27.FR-8).
- The same marks in "On call now" (C-27.FR-9).
- The Telegram Destination check that reads, with `getChatMember`, which people of its Teams are not in its discussion
  group, after the spike S-132.

**OUT**

- The Destinations in whose Threads the Escalation chains that Page a Schedule write (C-32.FR-8), which the warning
  covers from S-155; whether a call through a Notification gateway still reaches the person (C-30.FR-3), from S-145.
- The pages that show the warnings: S-135 and S-136.

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated):
  - `ScheduleWarning.kind` gains `unreachable`, with `destination` (`DestinationRef`), `messenger`
    (`ConnectionType`, or `webhook`), `reason` (`ReachReason`: `no_account_link`, `bridge_no_native_mentions`,
    `webhook_no_thread_reply`, `not_in_discussion_group`), `start` and `end` (the segment).
  - `TeamOnCall` gains `unreachable` (`OnCallReach`[] {`destination`, `messenger`, `reason`}); a gap that follows is
    S-133's `next` with `user` null.
  - `DestinationCheckItem.name` gains `discussion_group_members`, and `DestinationCheckItem` gains `absent_users`
    (`UserRef`[]), set with that step only.
- **Schema** (migration `0031_telegram_discussion_members`, expand only; `schema.md` §4.5):
  `destinations.telegram_absent_user_ids bigint[]` — Muster `users.id` of the people the check saw outside the
  discussion group, not Telegram user ids — and `destinations.telegram_members_checked_at timestamptz`, set by a
  Telegram Destination check and null for the other types. The down migration drops them.
- **Reach** (`internal/schedules/reach.go`): for a person and a Team, the Destinations of the Team (`team_destinations`,
  S-121) and, for each, whether a Mention of the person reaches it — the Account link in the Destination's Identity
  space (`mentions.IdentitySpace`), the bridge's `native_mentions` from the Connection's `info`, the outgoing webhook's
  mode and "reply in thread" request, and the Telegram Destination's `telegram_absent_user_ids`. `result.go` adds the
  `unreachable` warnings to the Result of a period, one per person, Destination and segment; `oncallnow.go` adds them
  to the current segment. Read-only: it changes nothing anywhere.
- **Telegram Destination check** (`internal/telegram/destcheck.go`): after its steps of C-14.FR-14 pass, on save, on
  "Check" and in `muster doctor` — never as the Broken probe — the step `discussion_group_members` calls `getChatMember`
  of the discussion group for each User with a Telegram Account link for the Connection who is in a Team that names the
  Destination (Open question 1), through the interactive client class like the other steps, and stores the people whose
  status is `left` or `kicked`, or whom the answer does not know, as the facts of S-132 say; the step always passes.
  `internal/destinations` stores the result.
- **Fake Telegram server** (`internal/fakes/faketelegram/chats.go`): `getChatMember` answers a person who never joined
  a chat as the facts of S-132 say — `left` until then, as today — and `PUT /_fake/chats/{chat}/members/{user}` sets a
  person's membership.
- **Settings**: `schedule.gap_view`.
- **Log events, metrics**: none new.
- **Documentation**: `docs/concepts/schedules.md` — "Unreachable On-calls": the reasons, linking an account in the
  profile, a bridge that declares `native_mentions`, the "reply in thread" request of an outgoing webhook, joining the
  discussion group of a Telegram channel and checking the Destination again.

## Steps

1. Write the migration and `schema.md`. Check: `make test-integration` runs up, down and up.
2. Write the reach and add it to the Result and "On call now"; add the spec. Check: `make generate-check`;
   `reach_test.go` covers every Destination type and reason, a Team with no Destination and a person On-call in two
   segments; `oncallnow_test.go`.
3. Add the step of the Telegram Destination check and the fake's answers. Check: `destcheck_test.go`,
   `faketelegram_test.go`.
4. Write `test/e2e/schedule_reach_test.go` and the documentation. Check: the Verification below; `make docs-check`.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-131 and S-121: A; the Team "payments" (TP) with alice and bob, both Responders; the demo Mattermost Connection
# (C) of muster dev with the Destination payments-notice (PN) of the Team; alice linked to u-alice of the fake Mattermost
# server as in S-069, bob not linked; CLK.
API=localhost:8080/api/v1; CLK=localhost:8082/_dev/clock
uid() { curl -s "${A[@]}" "$API/users?q=$1" | jq -r '.items[0].id'; }
TODAY=$(date -u -d "$(curl -s $CLK | jq -r .now)" +%F)
SC=$(curl -s "${A[@]}" $API/schedules -d "{\"team_id\":\"$TP\",\"name\":\"Main\",\"time_zone\":\"UTC\",\"rotation_layers\":[
  {\"name\":\"Days\",\"user_ids\":[\"$(uid bob)\",\"$(uid alice)\"],\"shift_length\":\"day\",\"handover_time\":\"00:00\",
  \"start_date\":\"$TODAY\",\"limits\":null}]}" | jq -r .id)

# C-27.AC-6: today bob, without a Mattermost Account link, is On-call; tomorrow alice, linked
curl -s "${A[@]}" "$API/schedules/$SC/on-call?to=$(date -u -d "$TODAY +2 days" +%FT00:00:00Z)" |
  jq -c '[.warnings[] | select(.kind == "unreachable") | {u: .user.login, d: .destination.name, messenger, reason}] | unique'
# [{"u":"bob","d":"payments-notice","messenger":"mattermost","reason":"no_account_link"}]
curl -s "${A[@]}" "$API/on-call-now" | jq -c '.items[] | select(.team.slug == "payments") | {u: .user.login, r: [.unreachable[].reason]}'
# {"u":"bob","r":["no_account_link"]}
curl -s -X POST $CLK -d '{"advance_seconds": 86400}' > /dev/null
curl -s "${A[@]}" "$API/on-call-now" | jq -c '.items[] | select(.team.slug == "payments") | {u: .user.login, r: [.unreachable[].reason]}'
# {"u":"alice","r":[]}
```

`test/e2e/schedule_reach_test.go` repeats this and adds a Bridge Destination of the Team on a fake bridge whose `info`
declares no `native_mentions` (`bridge_no_native_mentions`), an outgoing webhook in template mode without "reply in
thread" (`webhook_no_thread_reply`), and a Telegram Destination of the Team with a person linked to Telegram whom
`PUT /_fake/chats/{group}/members/{user}` leaves out of the discussion group: after "Check" the warning
`not_in_discussion_group` appears, and after the person joins and "Check" runs again it is gone.

## Open questions

1. **Whom the Telegram Destination check looks at, and when.** C-27.FR-8 says "as the Destination check sees it" without
   naming the people. Options: (a) the people with a Telegram Account link for the Connection who are in a Team that
   names the Destination, at each check on save, on "Check" and in `muster doctor`, so the warning may be as old as the
   last check, which the warning's time shows; (b) every person with a Telegram Account link for the Connection; (c) a
   periodic check by the Leader. Recommended: (a) — the On-call of a Schedule is always in its Team (C-27.FR-2,
   FR-6), and the Leader adds only the gap scan and the gauges (L2.md NFR-2, NFR-4); the contract follows it.

## Notes

- Suggested commit: `feat(schedules): warn about on-calls whom the team's destinations cannot mention`.
- Migration `0031` follows the story map; a story merged out of that order renumbers its migration to the next free
  number in its own pull request (S-131's Notes).
- Split from S-133 at the contract boundary of C-27.FR-8: the gaps and their alert need no messenger, the reach needs
  every Destination type. It takes the next free ID, S-164, and keeps its place after S-133 in the story map.
- The warning never changes delivery: a Mention entry of the On-call still records "Mention not delivered" (S-165) when
  it reaches nobody; the warning only says it ahead of time.
- The facts of S-132 decide how a person the bot never saw is answered; until they are recorded the fake keeps
  answering `left`.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-27.FR-8 | partial | the Destinations of the Team; the chains' Destinations S-155, gateway calls S-145, the pages S-135 and S-136 |
| C-27.FR-9 | partial | the unreachable marks in the API; the pages are S-136 |
| C-27.FR-14 | partial | "Unreachable On-calls" |
| C-27.AC-6 | partial | the API; the marks on the Schedule page S-135 and in "On call now" S-136 |
