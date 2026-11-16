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
  - internal/db/migrations/0044_telegram_discussion_members.up.sql
  - internal/db/migrations/0044_telegram_discussion_members.down.sql
  - design/db/schema.md
  - docs/install/versions.md
  - design/prd/l1/defaults.md
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - internal/schedules/reach.go
  - internal/schedules/reach_test.go
  - internal/schedules/reach_integration_test.go
  - internal/schedules/result.go
  - internal/schedules/oncallnow.go
  - internal/schedules/oncallnow_test.go
  - internal/schedules/overrides.go
  - internal/schedules/schedules.go
  - internal/schedules/query.sql
  - internal/api/schedules.go
  - internal/api/schedules_test.go
  - internal/api/destinations.go
  - internal/api/destinations_test.go
  - internal/telegram/client.go
  - internal/telegram/destcheck.go
  - internal/telegram/destcheck_test.go
  - internal/destinations/write.go
  - internal/destinations/write_test.go
  - internal/destinations/query.sql
  - internal/connections/telegram.go
  - internal/connections/telegram_test.go
  - internal/connections/doctor.go
  - internal/connections/doctor_test.go
  - internal/connections/connections_test.go
  - internal/connections/live_test.go
  - internal/connections/connections.go
  - internal/connections/export_test.go
  - test/e2e/telegram_test.go
  - web/e2e/telegram-destination.spec.ts
  - docs/messengers/telegram.md
  - internal/connections/query.sql
  - internal/archlint/secretleak.go
  - internal/doctor/doctor_test.go
  - internal/fakes/faketelegram/chats.go
  - internal/fakes/faketelegram/faketelegram_test.go
  - test/e2e/schedule_reach_test.go
  - docs/concepts/schedules.md
  - web/src/components/destination-check.tsx
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - AGENTS.md
acceptance:
  - "[C-27.FR-8, C-27.AC-6] An On-call without a Mattermost Account link for the Connection of a Mattermost Destination of their Team has, in `getScheduleOnCall`, a warning `unreachable` with the person, the Destination, the messenger `mattermost` and the reason `no_account_link` for the current segment and for each segment of the next `schedule.gap_view` in which they are On-call, and `getOnCallNow` marks the Team's On-call `unreachable` with the same; an On-call with the link has none (`reach_test.go`, `oncallnow_test.go`, `schedule_reach_test.go`)."
  - "[C-27.FR-8] The reasons follow the Destination: `no_account_link` for a Mattermost, Slack, Telegram or Bridge Destination without the person's Account link in the Identity space of its Connection; `bridge_no_native_mentions` for a Bridge Destination whose bridge does not declare `native_mentions`; `webhook_no_thread_reply` for an outgoing webhook Destination in template mode without a \"reply in thread\" request; `not_in_discussion_group` for a Telegram Destination whose last Destination check saw the person outside its discussion group; an outgoing webhook in events mode has no warning (`reach_test.go`)."
  - "[C-27.FR-8] The Destination check of a Telegram Destination of a Team calls `getChatMember` of its discussion group for each active User who is not a Viewer of the Teams that name it with a Telegram Account link (the Identity space `telegram`), records as outside the group, with the time of the check, the Muster Users it answers with the status `left`, `kicked` or `restricted` with `is_member` false, or with `400` `Bad Request: PARTICIPANT_ID_INVALID` (F-128), and passes with the item `discussion_group_members` whose `absent_users` names them; any other answer or failed call of one person, and the people not asked before the step's deadline, leave that person unknown, which keeps the verdict of the check before, and do not fail the check (`destcheck_test.go`, `write_test.go`, `live_test.go`)."
  - "[C-27.FR-9] `getOnCallNow` gives each Team's On-call `unreachable` ({`destination`, `messenger`, `reason`}[]) for the current segment (`oncallnow_test.go`)."
  - "[C-27.FR-14] Concepts → Schedules has \"Unreachable On-calls\" with each reason and what to do about it; `make docs-check` passes."
verify: "make ci test-integration e2e-go docs-check"
operator_attention: false
issue: 453
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
  covers from S-172; whether a call through a Notification gateway still reaches the person (C-30.FR-3), from S-150.
- The pages that show the warnings: S-135 and S-136.

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated):
  - `ScheduleWarning.kind` gains `unreachable`, with `destination` (`DestinationRef`), `messenger`
    (`DestinationType`: the type of the Destination's Connection, or `webhook`), `reason` (`ReachReason`:
    `no_account_link`, `bridge_no_native_mentions`, `webhook_no_thread_reply`, `not_in_discussion_group`), `start` and
    `end` (the segment).
  - `TeamOnCall` gains `unreachable` (`OnCallReach`[] {`destination`, `messenger`, `reason`}); a gap that follows is
    S-133's `next` with `user` null.
  - `DestinationCheckItem.name` gains `discussion_group_members`, and `DestinationCheckItem` gains `absent_users`
    (`UserRef`[]), set with that step only.
- **Schema** (migration `0044_telegram_discussion_members`, expand only; `schema.md` §4.5; its upgrade and recovery
  notes in `docs/install/versions.md`):
  `destinations.telegram_absent_user_ids bigint[]` — Muster `users.id` of the people the check saw outside the
  discussion group, not Telegram user ids — and `destinations.telegram_members_checked_at timestamptz`, set by a
  Telegram Destination check and null for the other types; writing them bumps no version and records no Audit log
  entry. The down migration drops them.
- **Reach** (`internal/schedules/reach.go`): for a person and a Team, the Destinations of the Team (`team_destinations`,
  S-121) and, for each, whether a Mention of the person reaches it — the Account link in the Destination's Identity
  space (`mentions.IdentitySpace`; a messenger Destination without a Connection warns of nothing), the bridge's
  `native_mentions` from the Connection's `info` (an `info` never read warns too, since delivery then renders plain
  names), the outgoing webhook's
  mode and "reply in thread" request, and the Telegram Destination's `telegram_absent_user_ids`. `result.go` adds the
  `unreachable` warnings to the Result of a period, one per person, Destination and segment; `oncallnow.go` adds them to
  the current segment. Read-only: it changes nothing anywhere. The Destinations of a Team are the Team's part of the
  reach rule of S-121 (`routing.Reach`), read in SQL, so `reach_integration_test.go` (`TestIntegrationReachVectors`,
  on PostgreSQL, since the rule is read in SQL) loads the vectors of
  `internal/routing/testdata/reach/` (S-121), the one set of cases of who an Alert Group reaches, seeds the Team entries
  of each vector, and checks that the Destinations it finds for the Team are those the vectors name for the Team at any
  level, conditions aside, since an On-call is warned about every Destination of their Team (plan review, M4).
- **Telegram Destination check** (`internal/telegram/destcheck.go`): after its steps of C-14.FR-14 pass, on save, on
  "Check" and in `muster doctor` — never as the Broken probe — the step `discussion_group_members` calls `getChatMember`
  of the discussion group for each User with a Telegram Account link who is in a Team that names the Destination
  (settled 1) — an active User who is not a Viewer, since a disabled User or a Viewer is never On-call, with the link of
  the Identity space `telegram`, which every Telegram Connection shares and a Mention through the Destination's
  Connection uses — one after the other, in the interactive client class on save and "Check", limited by the Connection
  alone and not by the Destination, since `getChatMember` sends nothing to the chat and the Destination's bucket is for
  its sends and edits (F-016), and in the background class in `muster doctor`, each call with its own timeout there and
  paced at the Connection's limiter; the step stops after `destination.telegram.group_people_budget` (built in,
  `defaults.md`; `telegram.GroupPeopleBudget` in the code), on save, on "Check" and in `muster doctor`, so its cost is
  one request per person within that budget; it
  stores the people whose status is `left` or `kicked`, and those it answers `400` `Bad Request:
  PARTICIPANT_ID_INVALID`, as Telegram answers a subscriber of the channel who was never in the discussion group
  (F-128); any other error leaves the person unknown. `creator`, `administrator` and `member` count as in the group, an
  admin with `is_anonymous` included, and `restricted` by its `is_member`, as the Bot API reference documents (not seen
  in F-128). A leave reaches the answer only after a while — `member` about 20 s after it, `left` about 70 s after
  (F-128) — so a check just after a leave may still see the person in the group, until the next check. The step always
  passes; a request its runner refuses, such as one without a limiter token, or the deadline leaves the people not asked
  yet unknown. An unknown person keeps the verdict of the check before: the stored set is the people seen outside plus
  those stored before whom the check could not tell, so that only a definitive "in the group" ends an absence;
  `absent_users` names only the people this check saw outside. `internal/destinations` reads the people and stores the
  result in the save's transaction and on "Check", only while the stored discussion group is the one the check read, so
  that a check of another group, which fails until a save, or a slow one overtaken by a save that moved the Destination,
  writes nothing; the item names them in `absent_users`. `internal/connections` runs the step after the other steps for
  the Destination write path and in `muster doctor`, which records nothing, warns about a Destination whose group moved
  with the message of "Check" and reads no members there, and warns naming the people outside the group.
- **Fake Telegram server** (`internal/fakes/faketelegram/chats.go`): in a supergroup, such as a discussion group,
  `getChatMember` answers a person who is not one of the fake's bots and has no membership there with `400`
  `Bad Request: PARTICIPANT_ID_INVALID` (F-128) instead of `left` as today; a channel keeps answering `left`, which no
  fact checked, and the fake's bots keep their answers (F-003). `PUT /_fake/chats/{chat}/members/{user}` sets a
  person's membership, `left` included, which `getChatMember` answers at once — also `restricted` with `is_member`, an
  admin with `is_anonymous`, or an `error_code` with a `description` to answer instead, a failure of one person's call
  that `/_fake/faults`, which matches every `getChatMember` of the bot, cannot aim at.
- **AGENTS.md**: the paragraph on development mode names the answers of `getChatMember` and `PUT
  /_fake/chats/{chat}/members/{user}` of the fake Telegram server.
- **Imports** (ADR-0016): `schedules` imports `mentions` for `IdentitySpace`; `mentions` never imports `schedules`
  (S-140 declares the interface `OnCalls` there, which `schedules` satisfies and `internal/runtime` wires).
- **Exhaustive switches of the SPA** (S-166's rule): `checkItemName` of `web/src/components/destination-check.tsx`
  gains the case `discussion_group_members` with the text "Members of the discussion group" / «Участники группы
  обсуждения» in `en.json` and `ru.json`; `ReachReason` and the `unreachable` warnings meet no exhaustive switch until
  S-135 and S-136 show them.
- **Settings**: `schedule.gap_view`; the built-in `destination.telegram.group_people_budget`, new in `defaults.md`.
- **Log events, metrics**: none new.
- **Documentation**: `docs/concepts/schedules.md` — "Unreachable On-calls": the reasons, linking an account in the
  profile, a bridge that declares `native_mentions`, the "reply in thread" request of an outgoing webhook, joining the
  discussion group of a Telegram channel and checking the Destination again, and that a leave may show in the check
  only some time later, about a minute in F-128.

## Steps

1. Write the migration and `schema.md`. Check: `make test-integration` runs up, down and up.
2. Write the reach and add it to the Result and "On call now"; add the spec. Check: `make generate-check`;
   `reach_test.go` covers every Destination type and reason, a Team with no Destination and a person On-call in two
   segments; `reach_integration_test.go` the vectors of `internal/routing/testdata/reach/`; `oncallnow_test.go`.
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
thread" (`webhook_no_thread_reply`), and a Telegram Destination of the Team with a person linked to Telegram who was
never in the discussion group, whom the fake answers `400` `Bad Request: PARTICIPANT_ID_INVALID` (F-128): after "Check"
the warning `not_in_discussion_group` appears; after `PUT /_fake/chats/{group}/members/{user}` makes the person a
member and "Check" runs again it is gone, and after it sets them `left` and "Check" runs again it is back.

## Open questions

None open. Settled:

1. **Settled: the Telegram Destination check looks at the people with a Telegram Account link for the Connection who
   are in a Team that names the Destination**, at each check on save, on "Check" and in `muster doctor`, so the
   warning may be as old as the last check, which its time shows; the On-call of a Schedule is always in its Team
   (C-27.FR-2, FR-6), and the Leader adds only the gap scan and the gauges (L2.md NFR-2, NFR-4). S-172 widens the
   check to the people of the Schedules that Escalation chains Page in the Destination, once chains Page.

## Notes

- Suggested commit: `feat(schedules): warn about on-calls whom the team's destinations cannot mention`.
- Migration `0044`: the story map's `0031` was taken long before this story was worked; master ended at `0042` and
  `0043` is S-140's, worked at the same time, so the merges are ordered and the later one renumbers if needed (S-131's
  Notes). Until `0043` is on master, `TestMigrationsContiguous` fails here by design.
- Split from S-133 at the contract boundary of C-27.FR-8: the gaps and their alert need no messenger, the reach needs
  every Destination type. It takes the next free ID, S-164, and keeps its place after S-133 in the story map.
- The warning never changes delivery: a Mention entry of the On-call still records "Mention not delivered" (S-165) when
  it reaches nobody; the warning only says it ahead of time.
- F-128 settles the answers the step relies on: `400` `Bad Request: PARTICIPANT_ID_INVALID` for a subscriber of the
  channel who was never in the discussion group, and `left` a while after a leave; the privacy setting for groups and
  channels did not change the answer about the creator. A person the bot never saw was not checked: if Telegram answers
  them with another error, they stay unknown and get no warning until a fact says otherwise. F-129 found that a Mention
  in a muted discussion group did not ring its anonymous creator; the check sees such a person only as `creator` or
  `is_anonymous`, never the mute, and C-27.FR-8 names no such reason, so the warning does not use it and the
  documentation says it (S-137).

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-27.FR-8 | partial | the Destinations of the Team; the chains' Destinations S-172, gateway calls S-150, the pages S-135 and S-136 |
| C-27.FR-9 | partial | the unreachable marks in the API; the pages are S-136 |
| C-27.FR-14 | partial | "Unreachable On-calls" |
| C-27.AC-6 | partial | the API; the marks on the Schedule page S-135 and in "On call now" S-136 |
