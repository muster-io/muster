---
id: S-235
title: The accelerated run of an Escalation chain as a computation over Schedules, Notification rules and Account links (BE)
capability: C-42
kind: be
layer: L2
depends_on: [S-169, S-155, S-157, S-156, S-131, S-152, S-154, S-047, S-164]
covers: [C-42.FR-8, C-42.FR-9, C-42.FR-10, C-42.AC-6, C-42.AC-7, C-42.AC-8]
files_touched:
  - api/openapi.yaml
  - api/README.md
  - internal/escalation/play.go
  - internal/escalation/play_test.go
  - internal/escalation/play_integration_test.go
  - internal/escalation/engine.go
  - internal/escalation/engine_test.go
  - internal/escalation/steps.go
  - internal/escalation/pages.go
  - internal/schedules/oncallat.go
  - internal/schedules/oncallat_test.go
  - internal/notificationrules/rules.go
  - internal/notificationrules/rules_test.go
  - internal/destinations/chainplay.go
  - internal/destinations/chainplay_test.go
  - internal/api/destinations.go
  - internal/api/destinations_test.go
  - internal/api/server.go
  - internal/runtime/runtime.go
  - test/e2e/destination_chain_play_test.go
acceptance:
  - "[C-42.FR-8, C-42.AC-6] `createDestinationChainPlay` (`POST /destinations/{id}/chain-plays`) with one chain of three steps — Page the On-call normally, wait 10 minutes, Page them importantly — and the important rule of a Mention, a wait of 5 minutes and a call, answers a timeline with moments \"+0 min\" (the step 1 Page and its normal rule's Mention), \"+10 min\" (step 3's Page and the Mention of the important rule) and \"+15 min\" (the call, \"would call @{login} through {gateway}\" without the contact), the Page of each step naming the On-call of the Schedule at that moment from its Result, normally or importantly, and the Destinations of the chain whose Threads it would Page in, every Destination named and not only this one (`play_test.go`, `play_integration_test.go`, `test/e2e/destination_chain_play_test.go`)."
  - "[C-42.FR-9, C-42.AC-6] Playing is only a computation: it runs in a read-only transaction, writes no row of an `escalation_runs`, `escalation_run_steps`, `notification_rule_runs`, `pages`, `gateway_calls` or `timers`, sends nothing through a messenger, an outgoing webhook or a Notification gateway, records no `page_not_delivered` or `escalation_step_skipped`, makes no Assignee, counts in no metric and logs no Page; with the fake gateway running, it receives nothing, and the business clock and the development clock read the same before and after — `/_dev/clock` is not used and the real clock is not read for the moments, which are the chosen start plus the waits on the business clock (`play_integration_test.go`, `test/e2e/destination_chain_play_test.go`)."
  - "[C-42.FR-8] Each action of each person's Notification rule is shown with its own waits (C-31.FR-3) — the Mention, the wait, the call — for the rule the person has at the moment, whether the Organization's, one of \"Call\" and \"Don't call\" or a custom one, and a person a Page would not reach is shown with the reason of C-32.FR-10 and FR-11 — no Account link in the Identity space of the Destination's Connection, no Root message, a gateway or contact gone; an outgoing webhook step shows as \"would send `escalation_webhook` to {Destination}\" (`play_test.go`, `test/e2e/destination_chain_play_test.go`)."
  - "[C-42.FR-8, C-42.AC-7] Played from a chosen Saturday 03:00 of the Team's time zone, a step limited to Working hours is shown skipped with its reason, a Schedule with a gap at that moment shows the skip of a gap, a step whose gateway or contact is gone, and one whose person became a Viewer or was disabled, each shows its reason as C-32.FR-14 words it; a step that Pages the whole Team or everyone in the chat is shown as such and names nobody (`play_test.go`, `test/e2e/destination_chain_play_test.go`)."
  - "[C-42.FR-8] Repeats are made as C-32.FR-15 makes them, up to the 4,020 steps a run makes, the passes after the first folded in the timeline as one entry with the count of passes; at the end the timeline shows the moment the Alert Group would become Unclaimed (C-32.FR-7) — after the last chain ran out and the last step's waits — or says that the chain repeats without end within the limit (`play_test.go`)."
  - "[C-42.FR-8, C-42.FR-9] The decisions of a step — whether it is skipped and why, the people it names, how repeats count and which Page importance it has — are made by the same functions the running engine calls (`internal/escalation/engine.go`), extracted so that a change of the engine changes the play, never by a copy; the engine's behaviour and its tests are unchanged (`engine_test.go`, `play_test.go`)."
  - "[C-42.FR-10, C-42.AC-8] The operation needs `destinations:test`: a Responder and a Viewer get `403`, without authentication `401`; a chain that does not exist or does not name the Route or the Team given is `422` `unknown_id`; a start in the past or the future is accepted and a start that is not a time is `400`; playing is not recorded in the Audit log (`destinations_test.go`, `test/e2e/destination_chain_play_test.go`)."
verify: "make ci test-integration e2e-go"
operator_attention: false
issue: null
---

# S-235. The accelerated run of an Escalation chain as a computation over Schedules, Notification rules and Account links (BE)

## Scope

**IN**

- The computation: the timeline of one Escalation chain, played from a start on the business clock as if nobody
  acknowledged, with the Schedules' Results at each moment, the Notification rules of each person, the skips, the people
  not reached, the repeats and the moment of Unclaimed (C-42.FR-8).
- `createDestinationChainPlay` for chains named in the request, which proves the computation live; the guarantee that it
  writes, sends and counts nothing (C-42.FR-9).

**OUT**

- The chains the Destination would play on an Alert Group — "Escalation chains" listing from the Routes and the Teams,
  the Severity condition, "All chains" and the one timeline of overlapping chains: S-236.
- The page: S-238. The documentation: S-239.
- The step-by-step test: S-231 to S-234; the two features share a page and nothing else.

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated): `createDestinationChainPlay`
  (`POST /destinations/{destination_id}/chain-plays`, tag Destinations, `x-permission: destinations:test`,
  `x-listener: app`; the request has the source and Severity level as the test's `TestSource` and `severity_level`,
  `plays` — a list of `{chain_id, route_id | team_id}`, the Route or the Team whose naming of the chain makes it run —
  and `start`, a date-time, default the business clock's now) → `ChainPlay`: `start`, `moments` — each `{offset_seconds,
  at, entries}` — and `unclaimed_at` (a moment, or null with `repeats_without_end` true). The schema `ChainPlayEntry`
  has `kind` (`page`, `rule_action`, `call`, `skipped`, `not_reached`, `webhook`, `repeats`), `chain`, `step`,
  `importance`,
  `target` (the On-call of a Schedule, chosen people, a Team or everyone in the chat, by name), `destinations` (every
  Destination whose Threads the Page would be made in), `person`, `action`, `gateway`, `reason`, `count` (a folded
  repeat). Reasons are the codes C-32.FR-10, FR-11 and FR-14 name. `api/README.md` says that nothing is stored.
- **Package `escalation`** (flat; `org_id` in every query): `play.go` has `Play(ctx, in PlayInput) (Play, error)` —
  the chain and its steps as they are, the owning Team's Working hours else the Organization's, the people and Schedules
  as they are now —, run in a read-only transaction (`BEGIN READ ONLY`, so a write would fail the test) with the
  business
  clock injected; `engine.go` and `steps.go` give up their decisions (`skipReason`, the people of a step, repeat
  counting, importance) as functions of plain values that both the engine's `walk` and `Play` call.
- **Schedules** (`internal/schedules/oncallat.go`): `OnCallAt(ctx, db, scheduleID, t)` — the On-call of a Schedule at
  a moment from its Result (C-27), read only, beside `TeamOnCallAt`; a gap at `t` returns none with the Gap's reason.
- **Notification rules** (`internal/notificationrules/rules.go`): a read-only `EffectiveAt` that returns the rule a
  person has — Organization's, `call`, `no_call`, `custom` — for an importance, with each action's availability, without
  starting a run.
- **Destinations** (`internal/destinations/chainplay.go`): resolves the Destination, the source and the Severity level
  as
  `Test` does, checks the chains and their origins, and calls `escalation.Play`; the handler maps the timeline.
- **Clocks**: the moments are the chosen start plus the waits, computed on the business clock's value; no timer, run or
  row exists, and neither clock moves (C-42.FR-9). The real clock is not read for them.
- **Terminology**: "accelerated run" is a description, never a term of the glossary and never a run of a chain: the UI
  calls it "Play" (C-42.FR-9).

## Steps

1. Extract the decisions of a step from `engine.go` and `steps.go` into shared functions. Check: `engine_test.go`
   unchanged and green; `make lint-arch`.
2. Write `OnCallAt` and `EffectiveAt`. Check: `oncallat_test.go` (a handover, a gap, an Override), `rules_test.go`.
3. Write `play.go`. Check: `play_test.go` (the three-step chain of AC-6, skips, not reached, repeats folded, the 4,020
   limit, Unclaimed), `play_integration_test.go` (read-only, no rows, no timers, the same clock).
4. Add the spec, the service, the handler and the wiring. Check: `make generate-check`, `make lint`,
   `destinations_test.go`.
5. Write `test/e2e/destination_chain_play_test.go`. Check: the Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
API=localhost:8080/api/v1; FBR=127.0.0.1:18094/_fake; CLK=localhost:8082/_dev/clock
# A: the Admin's header array; the setup of S-169 and S-157: the Route "Infrastructure" naming the chain "devops" (Page the On-call of
# "Main" normally, wait 10 minutes, Page them importantly), ivan On-call, linked to the fake Mattermost, a confirmed contact at the
# fake gateway, an important rule of a Mention, a wait of 5 minutes and a call; the Team "payments" with Working hours Monday to Friday
D=$(curl -s "${A[@]}" $API/destinations | jq -r '.items[] | select(.name=="infra-critical") | .id')
BEFORE=$(curl -s $CLK | jq -r .now); RUNS=$(psql "$MUSTER_DB" -Atc "select (select count(*) from escalation_runs), (select count(*) from pages), (select count(*) from timers)")
# C-42.AC-6: the timeline
curl -s -X POST "${A[@]}" $API/destinations/$D/chain-plays -d '{"source":{"kind":"example"},"severity_level":"critical","plays":[{"chain_id":"'$CHAIN'","route_id":"'$ROUTE'"}]}' \
  | jq -r '.moments[] | "+\(.offset_seconds/60) min · " + ([.entries[] | .kind + " " + (.target // .person // "")] | join("; "))'
# +0 min · page On-call of devops Main; rule_action ivan
# +10 min · page On-call of devops Main; rule_action ivan
# +15 min · call ivan
# nothing was written, sent or moved
test "$(psql "$MUSTER_DB" -Atc "select (select count(*) from escalation_runs), (select count(*) from pages), (select count(*) from timers)")" = "$RUNS" && echo same
curl -s $FBR/notifications | jq length          # 0: the fake gateway received nothing
test "$BEFORE" = "$(curl -s $CLK | jq -r .now)" && echo clock-unmoved
curl -s localhost:8082/metrics | grep -E '^muster_pages_total' | md5sum      # unchanged
# C-42.AC-7: from a Saturday 03:00 of the Team's zone
curl -s -X POST "${A[@]}" $API/destinations/$D/chain-plays -d '{"source":{"kind":"example"},"severity_level":"warning","start":"2026-11-28T03:00:00+01:00","plays":[{"chain_id":"'$PAYCHAIN'","team_id":"'$PAYTEAM'"}]}' \
  | jq -c '[.moments[].entries[] | select(.kind=="skipped") | .reason]'
# ["only during Working hours", "gap in the Schedule"]
# a person a Page would not reach, and a webhook step
curl -s -X POST "${A[@]}" $API/destinations/$D/chain-plays -d "$PLAY_PETR" | jq -c '[.moments[].entries[] | select(.kind=="not_reached") | [.person,.reason]]'   # [["petr","no_account_link"]]
# repeats and Unclaimed
curl -s -X POST "${A[@]}" $API/destinations/$D/chain-plays -d "$PLAY_REPEATING" | jq -c '{u: .unclaimed_at, r: .repeats_without_end}'
# C-42.FR-10
curl -s -o /dev/null -w '%{http_code}\n' -X POST "${V[@]}" $API/destinations/$D/chain-plays -d "$PLAY"        # 403
curl -s "${A[@]}" "$API/audit-log?limit=5" | jq -c '[.items[].action] | index("destination.tested")'          # null: playing is not recorded
make lint-arch
```

`test/e2e/destination_chain_play_test.go` repeats these through the generated client.

## Open questions

- **The shape of the request and of the timeline.** The PRD names the operation, what it takes and what the timeline
  shows, not the schema; this story names `plays`, `ChainPlay` and `ChainPlayEntry` as above, with the origin of each
  chain — the Route or the Team that names it — in the request because the Destinations of a Page depend on it
  (C-32.FR-8). Say if the review wants the origin found by the server from the chain alone (a chain may be named by
  several Routes and Teams, so it could not).
- **Which gaps and reasons.** C-32.FR-14 names the skip reasons in words; this story reuses their codes. A skip that has
  no code in the spec today would be added to `ChainPlayEntry.reason` as a new value.

## Notes

- Suggested commit: `feat(escalation): add the accelerated run of an escalation chain as a computation without effects`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases. No migration.
- The extraction of the decisions is the load-bearing part: copying them would let the play drift from the engine, which
  is why a changed engine test is a defect of this story (acceptance statement 6), the way S-215 exports the adapters'
  payload builders.
- The moments are on the business clock but the play never advances it: under `muster dev` the development clock moves
  the business clock only by its own endpoint, which the play does not call (C-42.FR-9).
- A chain that Pages only through a gateway, or only in other Destinations, is not offered by S-236, but may be played
  here when named explicitly, since the request names the chain and its origin; the Destination only chooses the
  source's
  Routes and the layout of the answer.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-42.FR-8 | partial | the timeline of the chains named in the request; the offered chains and the one timeline of overlapping chains are S-236, the page S-238 |
| C-42.FR-9 | partial | no effects, no clock movement; that playing posts nothing in the Thread of a Test message is checked with the offers in S-236 |
| C-42.AC-6 | partial | the three-step chain and the unchanged clock; the offers and "All chains" are S-236 |
| C-42.AC-7 | full | |
| C-42.FR-10 | partial | the Permission of playing and that it is not recorded; the steps' Permission and Audit log are S-231 |
| C-42.AC-8 | partial | `403` and no Audit log entry for playing; the steps' part is S-231 |
