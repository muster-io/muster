---
id: S-178
title: The Pages of the runs of Escalation chains and of the Timeline in the API, and paged in the Timeline of the SPA (BE)
capability: C-32
kind: be
layer: L2
depends_on: [S-155]
covers: [C-32.AC-17, C-32.FR-18, C-32.FR-19]
files_touched:
  - api/openapi.yaml
  - internal/escalation/runs.go
  - internal/escalation/runs_test.go
  - internal/escalation/query.sql
  - internal/escalation/runs_integration_test.go
  - internal/api/escalationchains.go
  - internal/api/escalationruns_test.go
  - internal/groups/query.sql
  - internal/groups/read.go
  - internal/groups/escalation_test.go
  - internal/api/alertgroups.go
  - internal/api/alertgroups_test.go
  - web/src/components/timeline.tsx
  - web/src/components/timeline.test.tsx
  - test/e2e/pages_test.go
acceptance:
  - "[C-32.AC-17, C-32.FR-18] `listEscalationChainRuns` lists, with each step a run made, its Pages: `kind` `mention`, their people (`UserRef`[]) or the Team or the token of a `page_team` step, and `outcome` `pending`, `delivered` or `not_delivered` with its `reason`; the Pages of an Alert Group whose details are removed are hidden as its steps are (C-09.FR-16) (`runs_test.go`, `runs_integration_test.go`, `escalationruns_test.go`)."
  - "[C-32.FR-19] `TimelineEscalationEntry` gains `team` (`TeamRef`), `everyone` and `importance`, read from `team_id`, `page_everyone` and `page_importance` of the escalation entries (`escalation_test.go`, `alertgroups_test.go`)."
  - "[C-32.FR-18] The Timeline of the SPA shows `paged` with S-148's generic text of the escalation events until S-159 renders them in full, in English and in Russian (`timeline.test.tsx`)."
  - "[C-32.AC-17] The end-to-end checks of S-155 read the Pages of the runs of the devops chain and the `team`, `everyone` and `importance` of the escalation entries (e2e `TestPages`)."
verify: "make ci e2e-go"
operator_attention: false
issue: 607
---

# S-178. The Pages of the runs of Escalation chains and of the Timeline in the API, and paged in the Timeline of the SPA (BE)

## Scope

**IN**

- The Pages of each step a run made in "How it went", `listEscalationChainRuns` (C-32.AC-17, FR-18).
- The fields of a Page in the escalation entries of the Timeline (C-32.FR-19): its Team, its token and its importance.
- The case of `paged` in the Timeline of the SPA, with the generic text of the escalation events.

**OUT**

- The Pages themselves, their Timeline entries and their outcomes: S-155 and S-177. Calls: S-157, which adds the
  `kind` `call` with its gateway to the Pages of the runs.
- The chain page that shows "How it went": S-158. The Alert Group page with the full texts of the escalation entries:
  S-159.

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated): `EscalationRun.steps[]` gains `pages` ({`kind`
  `mention`, `users` (`UserRef`[]), `team` (`TeamRef`), `everyone`, `outcome` — `pending`, `delivered`,
  `not_delivered` —, `reason` (`PageNotDeliveredReason`)}[]); `TimelineEscalationEntry` gains `team` (`TeamRef`),
  `everyone` and `importance`.
- **Schema**: none new; `pages` and `page_users` (S-155) hold the Pages, `timeline_entries.team_id`, `page_everyone` and
  `page_importance` the fields of the entries.
- **Escalation** (`internal/escalation/runs.go`, `query.sql`): `ChainRuns` reads the Pages of the steps of its runs in
  one query per page of runs, by run and step number, with their people, hidden with the steps once the Alert Group's
  details are removed.
- **Groups** (`internal/groups/query.sql`, `read.go`): the escalation entries read `team_id`, `page_everyone` and
  `page_importance`; `internal/api/alertgroups.go` maps them.
- **SPA** (`web/src/components/timeline.tsx`): `entryText` gains a case for `paged` with S-148's generic text
  (`timeline.escalation.event`), which needs no new translation.

## Steps

1. Add the fields to the spec and generate. Check: `make generate-check`.
2. Read the Pages of the runs. Check: `runs_test.go`, `runs_integration_test.go`, `escalationruns_test.go`.
3. Read the fields of the escalation entries. Check: `escalation_test.go`, `alertgroups_test.go`.
4. Add the case of the SPA and the end-to-end checks. Check: `timeline.test.tsx`, the Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-155's Verification, after the devops chain Paged ivan and the Alert Group was acknowledged.
curl -s "${A[@]}" "$API/escalation-chains/$CD/runs" | jq -c '[.items[0].steps[] | {n: .number, p: [.pages[] | {u: [.users[].login], o: .outcome}]}]'
# [{"n":1,"p":[{"u":["ivan"],"o":"delivered"}]},{"n":2,"p":[]},{"n":3,"p":[{"u":["ivan"],"o":"delivered"}]}]
curl -s "${A[@]}" "$API/alert-groups/$G/timeline?kind=escalation" | jq -c '[.items[] | select(.event == "paged") | .importance]'
# ["normal","normal","important","important"]
```

`test/e2e/pages_test.go` repeats these checks and reads the `team` and `everyone` of the Pages of a `page_team` step.

## Open questions

None.

## Notes

- Suggested commit: `feat(api): list the pages of escalation chain runs and timeline entries`.
- Split from S-155 before its implementation, with S-177, when S-155 would have touched about 80 files. The later
  Mentions of a rule are Pages of the step that started the rule, so a step lists them all.
- `kind` is `be`, the kind of most of its files, as for the other backend stories with a small change in `web/` (S-121,
  S-139): the one SPA change is the case of `paged`, with the existing key `timeline.escalation.event`, so
  `web/src/locales/en.json` and `ru.json` do not change and are not in `files_touched`.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-32.AC-17 | partial | the Pages of the runs; the warning of the Route and the runs themselves are S-169, the chain page S-158 |
| C-32.FR-18 | partial | the Pages in "How it went" and the generic text in the Timeline; the pages are S-158 and S-159 |
| C-32.FR-19 | partial | the fields of a Page in the Timeline; the Pages and the metric are S-155 |
