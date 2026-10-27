---
id: S-172
title: Warnings about On-calls whom the Destinations of their chains cannot mention, with the Telegram discussion group check for them (BE)
capability: C-27
kind: be
layer: L2
depends_on: [S-155]
covers: [C-27.FR-8]
files_touched:
  - internal/schedules/reach.go
  - internal/schedules/reach_test.go
  - internal/schedules/query.sql
  - internal/telegram/destcheck.go
  - internal/telegram/destcheck_test.go
  - internal/destinations/query.sql
  - internal/destinations/destinations.go
  - test/e2e/schedule_reach_test.go
  - docs/concepts/schedules.md
acceptance:
  - "[C-27.FR-8] The unreachable warnings of a Schedule cover, besides the Destinations of its Team (S-164), every Destination in whose Threads a chain with a step that Pages that Schedule writes — the Destinations of the Routes and Teams that name such a chain — with the same reasons, in `getScheduleOnCall` and `getOnCallNow` (`reach_test.go`)."
  - "[C-27.FR-8] The Destination check of a Telegram Destination calls `getChatMember` of its discussion group also for each person with a Telegram Account link for its Connection who is in a Rotation layer or an Override of a Schedule that a step of a chain Paging in that Destination names, besides the people of the Teams that name it (S-164), and records those who are not members in `absent_users` (`destcheck_test.go`)."
  - "[C-27.FR-8] Concepts → Schedules says, under \"Unreachable On-calls\", that the warnings also cover the Destinations of the chains that Page the Schedule; `make docs-check` passes."
verify: "make ci e2e-go docs-check"
operator_attention: false
issue: 461
---

# S-172. Warnings about On-calls whom the Destinations of their chains cannot mention, with the Telegram discussion group check for them (BE)

## Scope

**IN**

- The second half of C-27.FR-8: from C-32 on, the unreachable warnings of a Schedule cover all the Destinations in whose
  Threads the Escalation chains that Page this Schedule write (C-32.FR-8), as S-164 does for the Destinations of the
  Team.
- The Telegram Destination check of S-164, widened to the people those chains Page in that Destination.

**OUT**

- The warnings for the Destinations of the Team and the check for the people of its Teams: S-164. Whether a call
  through a Notification gateway still reaches the person: S-150. Where chains Page: S-155. The pages that show the
  warnings: S-135, S-136, S-150.

## Contracts

- **Spec**: none new; `ScheduleWarning` and `OnCallReach` (S-164, S-150) carry the warnings as before.
- **Schema**: none new; `destinations.telegram_absent_user_ids` (S-164) holds the result.
- **Schedules** (`internal/schedules/reach.go`, `query.sql`): the Destinations of the reach of a Schedule add, through a
  query of its own — `schedules` imports no package of Paging (S-169) — the Destinations of the Routes and Teams that
  name a chain with a `page_on_call` or `call` step on the Schedule, as `delivery.PageTargets` of S-155 finds them for a
  run; each warning keeps its reason (C-27.FR-8). This SQL computes a part of the reach rule of S-121, so
  `reach_test.go` also loads the vectors of `internal/routing/testdata/reach/` (S-121), the one
  set of cases of who an Alert Group reaches, and checks for the vectors with chains of a Route or a Team that the
  Destinations its query finds are those the vectors expect for those sources (plan review, M4).
- **Telegram Destination check** (`internal/telegram/destcheck.go`): the step `discussion_group_members` of S-164 also
  asks `getChatMember` for the people with a Telegram Account link for the Connection whom a chain Paging in that
  Destination can Page through a Schedule, once per person; a failed call leaves the person unknown, as in S-164.
- **Destinations** (`internal/destinations/query.sql`, `destinations.go`): the query `ListChainPagedTelegramPeople`,
  beside S-164's query of the people of the Teams that name the Destination — the Users with a Telegram Account link for
  the Destination's Connection who are in a Rotation layer, or take an Override that ends after the business clock's
  now, of a Schedule that a `page_on_call` or `call` step names in a chain named by a Route or a Team that names the
  Destination (`route_escalation_chains`, `team_escalation_chains`, `escalation_steps`, `rotation_layer_users`,
  `schedule_overrides`), with `org_id` at every level; `destinations.go` exposes it to the check as S-164's query is.
- **Imports** (ADR-0016, as S-164 and S-169 state them): the query reads those tables in SQL, so neither `telegram` nor
  `destinations` imports `schedules` or `escalation`, and `schedules` imports neither of them.
- **Settings**: `schedule.gap_view`.
- **Log events, metrics**: none new.
- **Documentation**: `docs/concepts/schedules.md` — "Unreachable On-calls" names the chains' Destinations.

## Steps

1. Extend the reach. Check: `reach_test.go` covers a Route's chain and a Team's chain Paging the Schedule, with a
   Mattermost Destination without the person's Account link and a Telegram one with the person outside its discussion
   group.
2. Widen the Telegram check. Check: `destcheck_test.go`.
3. Extend `test/e2e/schedule_reach_test.go` and the documentation. Check: the Verification below; `make docs-check`.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-164 and S-154: A; the Schedule "Main" (SM) of "payments" with bob On-call now, not linked in Mattermost; the
# chain "devops" on the Route "Infrastructure", whose Destination infra-notice (Mattermost) is not one of the Team's,
# with a step that Pages the On-call of SM.
API=localhost:8080/api/v1
curl -s "${A[@]}" "$API/schedules/$SM/on-call" | jq -c '[.warnings[] | select(.kind == "unreachable") | .destination.name] | unique'
# ["infra-notice","payments-notice"]
curl -s "${A[@]}" "$API/on-call-now?teams=all" | jq -c '[.items[] | select(.team.slug == "payments") | .unreachable[].destination.name] | unique'
# ["infra-notice","payments-notice"]
```

`test/e2e/schedule_reach_test.go` repeats these checks and adds a Telegram Destination of a Route whose chain Pages the
Schedule, checked with its Destination check, and the person outside its discussion group named in `absent_users`.

## Open questions

None.

## Notes

- Suggested commit: `feat(schedules): warn about on-calls whom the destinations of their chains cannot mention`.
- Split from S-155 when the review asked for the widened Telegram check: with it and the Timeline read path, S-155 would
  have touched about 47 files. It follows S-155 in the story map.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-27.FR-8 | partial | the chains' Destinations; the Team's are S-164, gateway calls and the pages S-150, the pages also S-135, S-136 |
