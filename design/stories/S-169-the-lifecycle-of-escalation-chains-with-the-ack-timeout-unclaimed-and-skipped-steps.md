---
id: S-169
title: The lifecycle of Escalation chains on Alert Groups, with the ack timeout, Unclaimed, waits, repeats and skipped steps (BE)
capability: C-32
kind: be
layer: L2
depends_on: [S-154]
covers: [C-32.FR-3, C-32.FR-4, C-32.FR-5, C-32.FR-6, C-32.FR-7, C-32.FR-14, C-32.FR-15, C-32.FR-16, C-32.FR-18, C-32.AC-3, C-32.AC-4, C-32.AC-5, C-32.AC-8, C-32.AC-12, C-32.AC-15, C-32.AC-16, C-32.AC-17, C-32.AC-18, C-26.FR-2, C-25.FR-9, C-17.FR-1, C-17.FR-3, C-17.FR-11, C-09.FR-1, C-09.FR-9, C-09.FR-22, C-10.FR-1, C-11.FR-6, C-15.FR-2]
files_touched:
  - api/openapi.yaml
  - api/README.md
  - internal/db/migrations/0041_escalation_runs.up.sql
  - internal/db/migrations/0041_escalation_runs.down.sql
  - design/db/schema.md
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - internal/publicid/publicid.go
  - internal/escalation/runs.go
  - internal/escalation/runs_test.go
  - internal/escalation/engine.go
  - internal/escalation/engine_test.go
  - internal/escalation/query.sql
  - internal/escalation/runs_integration_test.go
  - internal/groups/escalation.go
  - internal/groups/escalation_test.go
  - internal/groups/timers.go
  - internal/groups/timers_test.go
  - internal/groups/dispatcher.go
  - internal/groups/events.go
  - internal/groups/read.go
  - internal/groups/query.sql
  - internal/timers/worker.go
  - internal/timers/query.sql
  - internal/api/escalationchains.go
  - internal/api/alertgroups.go
  - internal/webhooks/body.go
  - internal/webhooks/body_test.go
  - internal/runtime/runtime.go
  - internal/logging/events.go
  - web/src/test/fixtures/alert-groups.ts
  - design/prd/L2.md
  - test/e2e/escalation_runs_test.go
  - docs/concepts/escalation-chains.md
  - internal/delivery/enqueue.go
  - internal/delivery/enqueue_test.go
  - internal/groups/events_test.go
  - web/src/components/timeline.tsx
acceptance:
  - "[C-32.FR-4, C-26.FR-2, C-32.AC-12] The chains that apply to an Alert Group are those its Route names and — where the Route has `deliver_to_teams` and `run_team_chains` — those each of its Teams names, each whose Severity condition its Severity level meets; with `run_team_chains` off the Team's Destinations receive it and the Team's chain does not start; a chain that the Route and a Team both name runs once, with both as its sources (P-70) (`escalation_test.go`, `runs_integration_test.go`)."
  - "[C-32.FR-5, C-32.AC-3, C-09.FR-9, C-11.FR-6, C-10.FR-1] Every point of the table of C-32.FR-5 starts, starts over or stops the chains as its row says: the first Publication, an Alert Group's own Publication after a Storm summary stood for it and a delivered `created` event start them; an Alert Group created with no Destination that reaches it, or only Broken ones, starts them at once; a Reopen into firing, Unacknowledge, Unsnooze, the end of a Snooze, Unresolve, a rise to critical that ends an acknowledgement or a Snooze, an auto-unacknowledge and its Owner disabled or deleted start them over; a rise of a firing Alert Group nobody owns starts only the chains for critical without restarting running ones; a Team that joins it then starts that Team's chains; Acknowledge, Snooze and Resolve stop every chain; a fall of the Severity level stops none (a table-driven test, `escalation_test.go`)."
  - "[C-32.FR-5, C-32.AC-18] On a firing Alert Group nobody owns, saving a Team its Alerts now belong to, turning `run_team_chains` on, naming a new chain on its Route and raising its Route's Minimum severity to critical each start no chain and record no step; after an Acknowledge and an Unacknowledge the chains that now apply start (`escalation_test.go`)."
  - "[C-32.FR-6, C-32.AC-4, C-17.FR-1] When the first chain of an Alert Group starts, its ack timeout timer is removed and no notice follows; when it becomes firing again, the ack timeout starts over only if no chain starts; an Alert Group on which no chain applies gets the notices of C-17.AC-1 (`timers_test.go`)."
  - "[C-32.FR-7, C-32.AC-5, C-17.FR-3, C-17.FR-11] After every chain started on an Alert Group has passed its last step without an acknowledgement, it is Unclaimed and records `unclaimed` once; a chain of a Team that joins afterwards makes it no longer Unclaimed while it runs, and it is Unclaimed again, recording `unclaimed`, once that chain has run out (`escalation_test.go`, `runs_integration_test.go`)."
  - "[C-32.FR-14, C-32.AC-8, C-25.FR-9, C-32.AC-15, C-09.FR-22] A step limited to Working hours is skipped outside those of the chain's owning Team — else the Organization's — and made inside them; a step whose Severity condition the Alert Group does not meet, a `page_on_call` step whose Schedule has a gap now, and a person named by a step who has become a Viewer or been disabled since it was saved are skipped, each recording the Quiet `escalation_step_skipped` with the chain, the step and the reason (`outside_working_hours`, `severity`, `schedule_gap`, `viewer`, `disabled`), and the chain goes on with its next step at once (`engine_test.go`)."
  - "[C-32.FR-15, C-32.AC-8, C-32.FR-3] A `wait` step delays the next one by its minutes on the business clock; \"Repeat from step 3, up to 2 times\" makes steps 3 to 6 three times in all and then goes on (`engine_test.go`)."
  - "[C-32.FR-16, C-32.AC-16] A run keeps the steps its chain had when it started until it stops or starts over; a change of the chain while it runs leaves that run on its old steps, and the next run takes the new ones (P-73) (`runs_test.go`)."
  - "[C-32.FR-18, C-09.FR-1, C-32.AC-17] `getAlertGroup` reads `escalation_chains`: each chain of its current start with its state (`running`, `ran_out`, `stopped`), the current step and `next_step_at`; `listEscalationChainRuns` (`escalation-chains:read`) lists a chain's recent runs, newest first, each with its Alert Group, its steps made and skipped with their reasons, and what stopped it — the Command and its actor — or that it ran out (`runs_test.go`, `escalation_runs_test.go`)."
  - "[C-15.FR-2] Outgoing webhooks in events mode receive `escalation_step_skipped` one to one, with the chain, the step and the reason (`body_test.go`)."
  - "[C-32.FR-5] Steps are `timers` rows of the kind `escalation_step` that any replica claims with its lease, and every step runs as a system transition of the dispatcher with Muster as the actor; two replicas make each step once, so a lost Leader never stops a chain (NFR-4) (`runs_integration_test.go`)."
verify: "make ci test-integration e2e-go docs-check"
operator_attention: true
issue: 458
---

# S-169. The lifecycle of Escalation chains on Alert Groups, with the ack timeout, Unclaimed, waits, repeats and skipped steps (BE)

## Scope

**IN**

- Which chains run on an Alert Group — its Route's and, where the Route runs them, its Teams' — each whose Severity
  condition it meets, a chain named twice running once (C-32.FR-4, P-70; C-26.FR-2's `run_team_chains` now acts).
- Runs that start, start over and stop at the points of C-32.FR-5, and never on a change of configuration
  (C-32.FR-5; C-09.FR-9, C-10.FR-1, C-11.FR-6 revised).
- The ack timeout that the first chain stops and that starts over only where no chain starts, and Unclaimed once every
  chain has run out (C-32.FR-6, FR-7; C-17.FR-1, FR-3, FR-11 revised).
- The step engine: steps as timer rows on any replica, waits, repeats, steps limited to Working hours and Severity
  conditions, and the skips of C-32.FR-14 that need no gateway, recorded as `escalation_step_skipped`; a run keeps its
  steps (C-32.FR-14 to FR-16, P-73).
- The running chains on the Alert Group's read and the runs of a chain for "How it went" (C-32.FR-18).

**OUT**

- What a Page step does: S-155 (by Mention, with the Notification rules) and S-157 (calls). Until S-155 a `page_*` step
  is made without Paging anyone; the run and its timing are as they will be.
- The `webhook` step: S-156. Skips for a gateway or a contact that is gone: S-157.
- Assignees and "Escalation chains stopped": S-168. The pages: S-158 and S-159.

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated):
  - `AlertGroup` gains the required `escalation_chains` (`AlertGroupChain`[] {`run_id`, `chain` (`EscalationChainRef`),
    `sources` ({`kind`: `route` or `team`, `team`}[]), `state` (`running`, `ran_out`, `stopped`), `step` ({`number`,
    `kind`} or null), `next_step_at`, `started_at`}) — the runs of the chains since they last started.
  - `listEscalationChainRuns` (`GET /escalation-chains/{escalation_chain_id}/runs`, `escalation-chains:read`; `cursor`,
    `limit`): `EscalationRunList` of `EscalationRun` {`id`, `alert_group` ({`id`, `number`, `title`,
    `severity_level`}), `started_at`, `state`, `ended_at`, `stopped_by` ({`command`: `acknowledge`, `snooze`,
    `resolve`, or `started_over`; `actor`: `ActorRef`; `destination`: `DestinationRef` or null} or null), `steps`
    ({`number`, `kind`, `at`, `outcome` — `made` or `skipped` —, `reason`}[]); S-155 adds the Pages}.
  - `LifecycleEvent` gains `escalation_step_skipped` (`x-event-kinds` `escalation`), `WebhookEventName` too;
    `TimelineEscalationEntry` gains `chain` (`EscalationChainRef`), `step` (number) and `skip_reason`
    (`EscalationSkipReason`: `outside_working_hours`, `severity`, `schedule_gap`, `viewer`, `disabled`; S-155 and S-157
    add more); `HintEvent` `alert-group` covers a change of `escalation_chains`.
  - `PublicId` and `api/README.md` gain the prefix `ER` (run).
- **Schema** (migration `0041_escalation_runs`, expand only; `schema.md` §4.9, §4.10, §4.18, §5):
  - `escalation_runs`: `id`, `org_id`, `public_id` (`CHECK (public_id ~ '^ER[0-9A-HJKMNP-TV-Z]{12}$')`),
    `alert_group_id` (FK, cascade), `chain_id` (FK, `ON DELETE SET NULL`), `chain_name`, `sources jsonb NOT NULL`,
    `steps jsonb NOT NULL` (the chain's steps when the run started, P-73), `step_index`, `repeats jsonb NOT NULL
    DEFAULT '{}'` (the passes of each `repeat` step), `state` (`CHECK IN ('running', 'ran_out', 'stopped')`),
    `started_at`, `ended_at`, `stop_command`, `stop_event_seq`, `generation` (the start of the Alert Group's chains it
    belongs to); indexes `(org_id, alert_group_id, generation)` and `(org_id, chain_id, started_at DESC)`.
  - `escalation_run_steps`: `org_id`, `run_id` (FK, cascade), `seq`, `number`, `kind`, `at`, `outcome`, `reason`;
    primary key `(run_id, seq)` — what "How it went" lists, pruned with the Alert Group's details.
  - `timers`: `kind` gains `escalation_step` and the table `escalation_run_id` (FK, cascade); `timers_subject_v2_check`
    (an `escalation_step` has both `alert_group_id` and `escalation_run_id`), `NOT VALID` then validated; a unique
    partial index `(escalation_run_id) WHERE kind = 'escalation_step'`.
  - `timeline_entries` gains `escalation_run_id`, `escalation_chain_id` and `step_number`; the kind and event exist
    since `_v4` (S-148).
  - `alert_groups.chain_generation bigint NOT NULL DEFAULT 0`, which each start or start-over of the chains raises.
  - The down migration drops all of it after deleting the timers of the new kind.
- **Packages and their imports** (ADR-0016; the same in S-155, S-168, S-156 and S-157): `groups` imports none of
  `escalation`, `notificationrules`, `gateways` or `delivery`; it declares the interfaces it consumes, here
  `groups.ChainRuns`, which `escalation` implements and `internal/runtime` wires, as L1 wires the hooks of `groups`.
  `escalation` imports `groups`, `schedules` and `teams` and turns what a step does into Commands of system
  transitions that the dispatcher runs; nothing it imports imports it.
- **Groups** (`internal/groups/escalation.go`, `timers.go`, `dispatcher.go`, `events.go`, `read.go`): the dispatcher,
  after each transition, compares the state before and after and, by the cause of the transition, asks
  `ChainRuns` in the same transaction to start the chains that apply, start them over, start only those newly met, or
  stop every running one; a transition of a configuration change (S-111's, S-163's, S-170's) asks nothing
  (C-32.FR-5). The first start removes the ack timeout timer; becoming firing again starts the ack timeout only when no
  chain starts (FR-6). The system transitions `EscalationStepSkipped` (records `escalation_step_skipped`: Quiet, no
  Mentions, kind `escalation`, nothing in a messenger) and `ChainsRanOut` (records `unclaimed` when every run of the
  current generation ran out and none was stopped) are the dispatcher's, with Muster as the actor; a chain that starts
  later ends Unclaimed while it runs (FR-7).
- **Escalation** (`internal/escalation`): `runs.go` — the runs, their sources, the snapshot of steps, their states, the
  reads, and the implementation of `groups.ChainRuns`, which writes `escalation_runs` and their timers in the
  dispatcher's transaction; `engine.go` — `FireStep`, the handler of the timer kind, takes the current step and makes
  it or skips it — its Working hours through `WorkingHoursOf` at the business clock, its Severity condition against the
  Alert Group's level, a `page_on_call` step's gap through `schedules.OnCallAt` (S-131), the current state of the people
  a step names — then waits, repeats or goes on at once, and dispatches `EscalationStepSkipped` and `ChainsRanOut`; a
  run is `ran_out` once its last step is made, and Acknowledge, Snooze and Resolve stop only the runs that are still
  `running`, leaving a run that ran out as it is; a step that Pages is handed to `StepPages`, an interface declared
  here, which this story wires to nothing.
- **Timers** (`internal/timers`): the claim returns `escalation_run_id`; the kind is registered in the runtime with its
  handler.
- **Event tables and SPA** (`internal/delivery/enqueue.go`, `internal/groups/events_test.go`,
  `web/src/components/timeline.tsx`): the row of `escalation_step_skipped` (nothing in a messenger) in
  `delivery.Table`, counted by `enqueue_test.go`; the table-driven test of the closed table covers it and `unclaimed`
  of chains; a case for it in `entryText` with S-148's generic text.
- **Webhooks** (`internal/webhooks/body.go`): `escalation_step_skipped` in events mode, with the chain, the step and the
  reason.
- **Web test fixtures** (`web/src/test/fixtures/alert-groups.ts`, S-166): the builder of Alert Group reads sets
  `escalation_chains` to `[]`, so that `make generate` keeps the TypeScript check of the web tests green.
- **Log events** (`internal/logging/events.go`): `escalation_run_started`, `escalation_run_stopped` and
  `escalation_run_ran_out` (debug; the Alert Group, the chain, the run), C-32.
- **Shared tables**: L2.md §5.3 moves P-70 and P-73 to "Decided since they were first listed".
- **Settings**: `team.working_hours`, `organization.working_hours`, `route.deliver_to_teams`, `route.run_team_chains`,
  `route.ack_timeout`.
- **Documentation**: Concepts → Escalation chains gains "When chains run" with the table of C-32.FR-5, "Chains and the
  ack timeout" and "Unclaimed".

## Steps

1. Write the migration and `schema.md`. Check: `make test-integration` runs up, down and up.
2. Write the runs and the engine. Check: `runs_test.go`, `engine_test.go` with a manual clock cover waits, repeats, the
   Working hours of a Team and of the Organization, Severity conditions, a gap, a Viewer and a disabled person, and a
   change of the chain while a run goes on.
3. Wire the points of C-32.FR-5 into the dispatcher with the ack timeout and Unclaimed. Check: `escalation_test.go` (a
   row per point, the rows of C-32.AC-18), `timers_test.go`.
4. Add the reads, the webhook event, the fixture and the log events; add the spec. Check: `make generate-check`;
   `body_test.go`; the web tests.
5. Confirm P-70 and P-73, write the documentation and `test/e2e/escalation_runs_test.go`. Check: the Verification below;
   `runs_integration_test.go` with two replicas; `make docs-check`.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-154: A, the chain "payments" (CP) named by the Team "payments" (TP) with its Destinations payments-critical
# and payments-notice; the Route "Infrastructure" (R) with deliver_to_teams on and its own chain "devops" (CD: page the
# On-call of devops' Main normally, wait 10, page them importantly); the Integration "sev" with NOTIFY, PUTA and AG; CLK.
# payments' Working hours Mon-Fri 11-19 Europe/Berlin. A is a Personal access token, so the advances keep it valid.
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake; CLK=localhost:8082/_dev/clock
# the development clock only moves forward: advance it to a Wednesday 03:10 in Berlin, 7 to 13 days ahead
NOW=$(curl -s $CLK | jq -r .now); D=$(TZ=Europe/Berlin date -d "$NOW" +%F); U=$(TZ=Europe/Berlin date -d "$NOW" +%u)
T=$(TZ=Europe/Berlin date -d "$D +$(( (10 - U) % 7 + 7 )) days 03:10" +%s)
curl -s -X POST $CLK -d "{\"advance_seconds\": $(( T - $(date -d "$NOW" +%s) ))}" > /dev/null
curl -s -X PUT $FAM/groups/e1 -d '{"receiver":"sev","route":"{}","labels":{"alertname":"KubernetesContainerOOMKilled"}}' > /dev/null
PUTA e1 a '{"labels":{"category":"infra","system":"k8s","team":"payments","severity":"warning"}}'; NOTIFY e1 '{"reason":"first notification"}'
G=$(AG 'team%3D%22payments%22'); sleep 1
curl -s "${A[@]}" $API/alert-groups/$G | jq -c '[.escalation_chains[] | {c: .chain.name, s: .step.number, st: .state}] | sort_by(.c)'
# [{"c":"devops","s":2,"st":"running"},{"c":"payments","s":2,"st":"running"}]
curl -s "${A[@]}" "$API/alert-groups/$G/timeline?kind=escalation" | jq -c '[.items[] | {e: .event, c: .chain.name, s: .step, r: .skip_reason}]'
# []   (steps 1 Page nobody until S-155; nothing was skipped yet)
curl -s -X POST $CLK -d '{"advance_seconds": 600}' > /dev/null; sleep 1
curl -s "${A[@]}" "$API/alert-groups/$G/timeline?kind=escalation" | jq -c '[.items[] | {c: .chain.name, s: .step, r: .skip_reason}]'
# [{"c":"payments","s":3,"r":"severity"}]   (step 3 of payments: only for critical)
curl -s "${A[@]}" "$API/alert-groups/$G/timeline?kind=timers" | jq '[.items[] | select(.event == "ack_timeout")] | length'   # 0

# Acknowledge stops both; Unacknowledge starts them over
curl -s "${A[@]}" -X POST $API/alert-groups/$G/acknowledge > /dev/null
curl -s "${A[@]}" $API/alert-groups/$G | jq -c '[.escalation_chains[].state] | unique'                  # ["ran_out","stopped"]
# (devops made its last step at +600 s and ran out; the Acknowledge stopped payments and left devops as it was)
curl -s "${A[@]}" -X POST $API/alert-groups/$G/unacknowledge > /dev/null
curl -s "${A[@]}" $API/alert-groups/$G | jq -c '[.escalation_chains[] | .step.number] | sort'           # [2,2]
# nobody acknowledges: both run out and the Alert Group is Unclaimed
curl -s -X POST $CLK -d '{"advance_seconds": 7200}' > /dev/null; sleep 2
curl -s "${A[@]}" $API/alert-groups/$G | jq -c '{unclaimed, s: [.escalation_chains[].state] | unique}'  # {"unclaimed":true,"s":["ran_out"]}
curl -s "${A[@]}" $API/escalation-chains/$CP/runs | jq -c '.items[0] | {state, steps: [.steps[] | {number, outcome}] | length}'
# {"state":"ran_out","steps":…}
```

`test/e2e/escalation_runs_test.go` repeats these checks and adds a step inside and outside the Working hours of
payments, "Repeat from step 3, up to 2 times" on a critical Alert Group (steps 3 to 6 three times), a Route without
`run_team_chains`, a chain named by the Route and the Team running once, a Team joining an Unclaimed Alert Group, a
change of the chain while a run goes on, the rows of C-32.AC-18, and `E2E_REPLICAS=2`.

## Open questions

1. **"How it went" for a run that started over.** Settled by the API surface of C-32, `escalation-chains/{id}/runs`,
   whose runs are one per start: an Alert Group whose chains started over shows a run per start.

## Notes

- Suggested commit: `feat(escalation): run escalation chains on alert groups with the ack timeout and unclaimed`.
- The maintainer confirms provisional values with this story (operator attention): P-70, a chain named by a Route and
  Teams more than once runs once, in the Threads of each; P-73, a change of a chain applies to runs that start
  afterwards.
- P-71 and P-72 are decided (L2.md §5.3): a running chain goes on when the Severity level falls, and a chain that starts
  later ends Unclaimed while it runs.
- New Alerts that remove an acknowledgement and Maintenance windows, which C-32.FR-5 mentions, are specified later in
  L2; nothing of them is here.
- Migration `0041` follows the story map; a story merged out of that order renumbers its migration (S-167's Notes).

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-32.FR-3 | partial | waits, repeats and the limits of steps; Page steps are S-155 and S-157, the webhook step S-156 |
| C-32.FR-4 | full | |
| C-32.FR-5 | full | |
| C-32.FR-6 | full | |
| C-32.FR-7 | full | |
| C-32.FR-14 | partial | Working hours, Severity, gaps and people; a gateway or a contact that is gone is S-157 |
| C-32.FR-15 | full | |
| C-32.FR-16 | full | |
| C-32.FR-18 | partial | the running chains in the API; the Assignees are S-168, the pages S-159 |
| C-32.AC-3 | full | |
| C-32.AC-4 | full | |
| C-32.AC-5 | full | |
| C-32.AC-8 | full | |
| C-32.AC-12 | full | |
| C-32.AC-15 | partial | the person turned into a Viewer later is skipped; the rest is S-154 |
| C-32.AC-16 | partial | runs once and keeps its steps; Paging in the Threads of both is S-155 |
| C-32.AC-17 | partial | the runs list; its Pages are S-155, the Route's warning S-158 |
| C-32.AC-18 | full | |
| C-26.FR-2 | partial | `run_team_chains` acts on chains |
| C-25.FR-9 | partial | steps limited to Working hours |
| C-17.FR-1 | full | revised for L2 |
| C-17.FR-3 | full | revised for L2 |
| C-17.FR-11 | full | revised for L2 |
| C-09.FR-1 | partial | revised for L2: the running chains of an Alert Group; the Assignees are S-168 |
| C-09.FR-9 | full | revised for L2: chains that start over on a rise to critical |
| C-09.FR-22 | partial | revised for L2: `escalation_step_skipped` and `unclaimed` of chains |
| C-10.FR-1 | partial | revised for L2: Acknowledge, Snooze and Resolve stop every chain |
| C-11.FR-6 | partial | revised for L2: the chains of an Alert Group a Storm summary stood for start at its own Publication |
| C-15.FR-2 | partial | revised for L2: `escalation_step_skipped` in events mode |
