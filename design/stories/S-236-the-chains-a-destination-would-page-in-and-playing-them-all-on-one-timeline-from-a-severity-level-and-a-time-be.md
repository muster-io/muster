---
id: S-236
title: The chains a Destination would Page in, and playing them all on one timeline from a Severity level and a time (BE)
capability: C-42
kind: be
layer: L2
depends_on: [S-235, S-231, S-111, S-112, S-121, S-154, S-162, S-123]
covers: [C-42.FR-7, C-42.FR-8, C-42.FR-9, C-42.FR-10, C-42.AC-6, C-42.AC-8, C-42.AC-10]
files_touched:
  - api/openapi.yaml
  - api/README.md
  - internal/destinations/chainplay.go
  - internal/destinations/chainplay_test.go
  - internal/destinations/chainplay_integration_test.go
  - internal/destinations/query.sql
  - internal/escalation/play.go
  - internal/escalation/play_test.go
  - internal/api/destinations.go
  - internal/api/destinations_test.go
  - test/e2e/destination_chain_play_test.go
acceptance:
  - "[C-42.FR-7] `listDestinationChainPlayOffers` (`GET /destinations/{id}/chain-play-offers`, with `source` and `severity_level` as the test takes them) lists the chains that would run on such an Alert Group and Page in this Destination: those that the Routes naming this Destination name and, where a Route delivers to the Teams and runs their chains (C-26.FR-2), those that the Teams naming this Destination name, each whose Severity condition that Severity level meets (C-32.FR-2, FR-4, FR-8); each offer names the chain, its origin — the Route or the Team — and the Destinations its Pages would reach; with Routes \"Infrastructure\" naming `devops` and payments naming `payments` it offers both, for `warning` and for `critical` as the conditions say, and offers no chain that Pages only through a gateway, or only in other Destinations (`chainplay_test.go`, `chainplay_integration_test.go`, `test/e2e/destination_chain_play_test.go`)."
  - "[C-42.FR-7] The offers work whether or not a step-by-step run of the Destination is going on, with the source and Severity level of their own, and the default Severity level is the source's; the offers read the Routes, the Teams and their Destinations as they are when asked and write nothing (`chainplay_integration_test.go`)."
  - "[C-42.FR-8, C-42.AC-6] `createDestinationChainPlay` with `plays` left out, or with every offer, plays all the offered chains from one start and shows overlapping chains on one timeline ordered by moment, each entry named with its chain; with `plays` naming one it plays only that, with its Destinations as the offer names them; the start is the business clock's now or a chosen time, so that nights, weekends and Shifts can be checked: from the Saturday 03:00 of the Team's time zone the timeline reads \"+0 min · devops · Step 1 · would Page @ivan (On-call of devops, Main) normally in the Threads of infra-critical and infra-notice\", \"+0 min · payments · Step 1 · skipped: only during Working hours\", \"+10 min · devops · Step 3 · would call @ivan through Phone gateway\", \"+10 min · payments · Step 3 · would Page @petr importantly: Page not delivered, @petr has no linked Mattermost account\" and \"+25 min · the chains have run out: Unclaimed\" (`chainplay_test.go`, `test/e2e/destination_chain_play_test.go`)."
  - "[C-42.FR-9, C-42.AC-10] Playing all chains writes no row, sends nothing, counts in no metric and moves neither clock, as S-235 proves for one; it posts nothing in the Thread of any Test message of a step-by-step run — with a run going on, the fake Mattermost server holds the same Thread replies before and after — and its timeline is only the answer of the operation (`chainplay_integration_test.go`, `test/e2e/destination_chain_play_test.go`)."
  - "[C-42.FR-10, C-42.AC-8] The two operations need `destinations:test`: a Responder and a Viewer get `403` on each, without authentication `401`; playing the chains, one or all, adds no Audit log entry, while each step of a run that sends adds one (`destinations_test.go`, `test/e2e/destination_chain_play_test.go`)."
verify: "make ci test-integration e2e-go"
operator_attention: false
issue: null
---

# S-236. The chains a Destination would Page in, and playing them all on one timeline from a Severity level and a time (BE)

## Scope

**IN**

- The offers: which chains would run on an Alert Group of a source and a Severity level and Page in this Destination
  (C-42.FR-7).
- Playing all the offered chains from one start on one timeline, the choice of the start, and the proof that nothing is
  posted in any Test message's Thread (C-42.FR-8, FR-9).
- The Permission and the absence of an Audit log entry for playing (C-42.FR-10).

**OUT**

- The computation of one chain and `createDestinationChainPlay` for named chains: S-235.
- The page: S-238. The documentation: S-239.

## Contracts

- **Spec** (`api/openapi.yaml`): the new operation `listDestinationChainPlayOffers`
  (`GET /destinations/{destination_id}/chain-play-offers`, tag Destinations, `x-permission: destinations:test`,
  `x-listener: app`; `source` and `severity_level`) answering `ChainPlayOffers` — `items` of `ChainPlayOffer`:
  `chain` and `origin` (`EntityRef`s; the origin's `kind` is `route` or `team`) and `destinations` — complete and not
  paginated. `createDestinationChainPlay` (S-235) takes `plays` optional: left out, it plays every offer. Descriptions say
  that the offers are the same ones, for the same source and Severity level, that the play would take.
- **Package `destinations`** (`chainplay.go`, flat CRUD, `org_id` in every query): the selection of C-42.FR-7 — the Routes
  that name this Destination through their entries (and, where `deliver_to_teams` runs the Teams' chains, the Teams that
  name it), the chains each names with its Severity condition, the chain's own Destinations — beside the resolution of S-235;
  a chain whose steps Page only through a gateway, or only in other Destinations, is not offered; `query.sql` has the
  reads, with no write.
- **Package `escalation`** (`play.go`): `Play` accepts several plays and merges their moments by offset and then by chain
  name, each entry carrying its chain, and the Unclaimed moment of the whole selection (after the last chain ran out)
  as C-32.FR-7 says; nothing else changes.
- **Not touched**: the step-by-step run's tables and messages; the play never reads or writes them, which is how it can
  post nothing in a Test message's Thread (C-42.FR-9).

## Steps

1. Write the selection of offers. Check: `chainplay_test.go` (a Route's and a Team's chain, `deliver_to_teams`, the
   Severity condition for `warning` and `critical`, a gateway-only chain and a chain of other Destinations left out).
2. Extend `Play` to several plays on one timeline. Check: `play_test.go` (the order, the chain names, Unclaimed after the
   last chain).
3. Add the spec part and the handler; make `plays` optional. Check: `make generate-check`, `make lint`,
   `destinations_test.go`.
4. Extend `test/e2e/destination_chain_play_test.go`. Check: the Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
API=localhost:8080/api/v1; FMM=127.0.0.1:18065/_fake
# A: the Admin's header array; V: a Viewer's; the setup of S-169 (see S-235): the Route "Infrastructure" naming `devops`, the Team payments naming `payments`
D=$(curl -s "${A[@]}" $API/destinations | jq -r '.items[] | select(.name=="infra-critical") | .id')
# C-42.FR-7: the offers for a critical Alert Group, and for a warning one
curl -s "${A[@]}" "$API/destinations/$D/chain-play-offers?severity_level=critical" | jq -c '[.items[] | [.chain.name, .origin.kind, .origin.name]]'
# [["devops","route","Infrastructure"],["payments","team","payments"]]
curl -s "${A[@]}" "$API/destinations/$D/chain-play-offers?severity_level=warning" | jq -c '[.items[].chain.name]'    # the chains whose condition a warning meets
# C-42.AC-6 and the scenario of C-42: all chains from a Saturday 03:00 on one timeline
curl -s -X POST "${A[@]}" $API/destinations/$D/chain-plays -d '{"source":{"kind":"example"},"severity_level":"critical","start":"2026-11-28T03:00:00+01:00"}' \
  | jq -r '.moments[] | .entries[] | "+\(.offset_seconds/60) min · \(.chain) · Step \(.step) · \(.kind) \(.target // .person // "") \(.reason // "")"'
# +0 min · devops · Step 1 · page On-call of devops Main
# +0 min · payments · Step 1 · skipped only during Working hours
# +10 min · devops · Step 3 · call ivan
# +10 min · payments · Step 3 · not_reached petr no_account_link
curl -s -X POST "${A[@]}" $API/destinations/$D/chain-plays -d '{"source":{"kind":"example"},"severity_level":"critical"}' | jq -c '.unclaimed_at'   # +25 min
# C-42.AC-10: with a run going on, playing adds no Thread reply to its Test message
RID=$(curl -s -X POST "${A[@]}" $API/destinations/$D/test-runs -d '{"source":{"kind":"example"},"severity_level":"critical"}' | jq -r .id)
N=$(curl -s $FMM/posts | jq length)
curl -s -X POST "${A[@]}" $API/destinations/$D/chain-plays -d '{"source":{"kind":"example"},"severity_level":"critical"}' > /dev/null
test "$N" = "$(curl -s $FMM/posts | jq length)" && echo no-reply-added
# C-42.AC-8
curl -s -o /dev/null -w '%{http_code}\n' "${V[@]}" "$API/destinations/$D/chain-play-offers?severity_level=critical"       # 403
curl -s -o /dev/null -w '%{http_code}\n' -X POST "${V[@]}" $API/destinations/$D/chain-plays -d '{"source":{"kind":"example"}}'   # 403
curl -s "${A[@]}" "$API/audit-log?limit=10" | jq -c '[.items[].action | select(.=="destination.tested")] | length'       # the steps that sent, none for the plays
make lint-arch
```

`test/e2e/destination_chain_play_test.go` repeats these through the generated client.

## Open questions

- **Listing the offers.** C-42's API surface names four operations and none that lists the offered chains, which the UI
  of C-42.FR-7 needs before "Play"; this story adds `listDestinationChainPlayOffers` as a read, with the same Permission.
  Say if the review prefers the offers inside the response of an empty play, or another path, before the generated
  clients carry it.

## Notes

- Suggested commit: `feat(destinations): offer the chains a destination would page in and play them together on one timeline`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases. No migration.
- "Would run on such an Alert Group" depends on the Severity level only through the chains' Severity conditions; the
  Routes' own Minimum severity and matchers are those of the Alert Groups that arrive, not of the sample, so the offers
  assume the Alert Group reaches the Route, as the Destination's Route preview does (S-123).
- Overlapping chains are shown on one timeline because a real Alert Group runs them together; the entries are ordered by
  moment and then by chain name so that the timeline is stable between plays.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-42.FR-7 | partial | the offers and the source and Severity level of their own; the listing on the page is S-238 |
| C-42.FR-8 | partial | all chains on one timeline; the page is S-238 |
| C-42.FR-9 | full | |
| C-42.FR-10 | partial | the Permission of the offers and of playing all, and no Audit log entry for playing; the steps' Permission and Audit log are S-231, the first play's S-235 |
| C-42.AC-6 | full | with S-235's three-step chain |
| C-42.AC-8 | partial | `403` for the offers and plays, no Audit log entry for playing; the steps' part is S-231 |
| C-42.AC-10 | partial | playing adds no Thread reply; the Mattermost Test message's markup is S-231 |
