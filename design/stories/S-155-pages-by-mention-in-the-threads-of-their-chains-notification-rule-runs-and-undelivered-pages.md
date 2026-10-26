---
id: S-155
title: Pages by Mention in the Threads of their own chains, the runs of Notification rules, and undelivered Pages (BE)
capability: C-32
kind: be
layer: L2
depends_on: [S-152, S-164, S-165, S-169]
covers: [C-32.FR-3, C-32.FR-8, C-32.FR-9, C-32.FR-10, C-32.FR-11, C-32.FR-19, C-32.AC-1, C-32.AC-6, C-32.AC-7, C-32.AC-11, C-32.AC-13, C-32.AC-16, C-32.AC-17, C-31.FR-3, C-31.AC-1, C-27.FR-8, C-09.FR-22, C-11.FR-2, C-11.FR-20, C-12.FR-3, C-15.FR-2, C-15.FR-11, C-18.FR-4]
files_touched:
  - api/openapi.yaml
  - internal/db/migrations/0042_pages.up.sql
  - internal/db/migrations/0042_pages.down.sql
  - design/db/schema.md
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - internal/escalation/pages.go
  - internal/escalation/pages_test.go
  - internal/escalation/query.sql
  - internal/escalation/pages_integration_test.go
  - internal/notificationrules/runs.go
  - internal/notificationrules/runs_test.go
  - internal/notificationrules/query.sql
  - internal/groups/escalation.go
  - internal/groups/pages.go
  - internal/groups/events.go
  - internal/groups/events_test.go
  - internal/delivery/pages.go
  - internal/delivery/pages_test.go
  - internal/delivery/where.go
  - internal/delivery/where_test.go
  - internal/delivery/threads.go
  - internal/delivery/query.sql
  - internal/messages/page.go
  - internal/messages/page_test.go
  - internal/messages/texts/en.json
  - internal/messages/texts/ru.json
  - internal/webhooks/body.go
  - internal/webhooks/body_test.go
  - internal/schedules/reach.go
  - internal/schedules/reach_test.go
  - internal/schedules/query.sql
  - internal/timers/query.sql
  - internal/metrics/catalogue.go
  - internal/runtime/runtime.go
  - design/prd/l1/reference.md
  - test/e2e/pages_test.go
  - docs/concepts/escalation-chains.md
acceptance:
  - "[C-32.FR-8, C-32.AC-1, C-32.AC-16] On the setup of scenario 1 of C-32, a warning Alert Group published in `infra-notice` and `payments-notice` gets \"Step 1\" in each Thread from its own chain only — the Route's chain in the Route's Destinations, the Team's chain in the Team's — and a Team's chain whose Team has no Destination with the Root message writes in the Route's; a chain that the Route and a Team both name Pages once in the Threads of both (`where_test.go`, `pages_test.go`, `pages_test.go` of e2e)."
  - "[C-32.FR-3, C-31.FR-3, C-31.AC-1] A `page_on_call` step Pages the On-call of its Schedule at that moment and a `page_people` step each person it names, each by their Notification rule of the step's importance: with the default rules a normal Page makes one Thread reply that mentions the person, an important Page a second one 5 minutes later, and none when the Alert Group was acknowledged in between; the run of a rule stops when the Alert Group is acknowledged, snoozed or resolved, and when its chain starts over or stops (`runs_test.go`, `pages_integration_test.go`)."
  - "[C-32.FR-9, C-32.AC-6, C-18.FR-4, C-11.FR-2, C-12.FR-3] A Page is a Loud Thread reply that delivery sends, never batched with other replies: \"Step {n}: @ivan, you are needed\" and \"Chain of the Route Infrastructure · On-call of devops Main · normal\", or \"· important, again in 5 min\" when the person's rule repeats; a person without an Account link in the Identity space of that Destination's Connection is named in the line \"Page not delivered: @petr has no linked Mattermost account\" instead of a Mention, in English or in the Route's language (`page_test.go`)."
  - "[C-32.FR-3, C-32.FR-9, C-32.AC-13] A `page_team` step writes the Destination's Team mention of the Team — the chain's owning Team by default — or its token for everyone in the chat; where a Destination's Connection can mention neither, such as Telegram, it makes no reply there, and when it can reply in none of the chain's Destinations it records `escalation_step_skipped` with the reason `no_mention_possible` (`pages_test.go`)."
  - "[C-32.FR-10, C-32.AC-6, C-32.AC-7] A Page records the Quiet `page_not_delivered` with the person and the reason — `no_root_message` when none of the chain's Destinations has the Alert Group's Root message, `no_account_link` when the person has no Account link in any of their Identity spaces, `not_delivered` when every Thread reply of the Page ended Not delivered or was withheld while its Destination was Broken; a reply withheld while Broken is not sent after recovery; a Route with a chain and no Destination records `page_not_delivered` `no_root_message` for its Mention steps (`pages_integration_test.go`)."
  - "[C-32.FR-19, C-32.AC-11, C-09.FR-22, C-11.FR-20] Every Page by Mention records one Loud `paged` (Timeline kind `escalation`) with `mentions` `[\"paged\"]` and its people in `paged_users`, or the Team or token of a `page_team` step; together with `page_not_delivered` and `escalation_step_skipped`, each row of the L2 lifecycle event table produces exactly one Timeline entry with its event, kind, loudness and Mentions (a table-driven test, `events_test.go`)."
  - "[C-32.FR-19] Pages count in `muster_pages_total{chain,kind,outcome}` with `kind` `mention` and `outcome` `delivered` or `not_delivered`; reference.md moves the metric into the catalogue (`catalogue.go`, `metrics_test.go`)."
  - "[C-32.FR-11, C-15.FR-2, C-15.FR-11] Outgoing webhooks in events mode receive `paged` with its people as `mentions` data, and `page_not_delivered`; a template-mode webhook among a chain's Destinations gets the Page through its \"reply in thread\" request when it has one; a Bridge Destination gets the Page with the Mention marker of C-22.FR-16 (`body_test.go`, `pages_test.go`)."
  - "[C-27.FR-8] The unreachable warnings of a Schedule cover, besides the Destinations of its Team, every Destination in whose Threads a chain with a step that Pages that Schedule writes (`reach_test.go`)."
  - "[C-32.AC-17] `listEscalationChainRuns` lists, with each step made, its Pages with their people and outcome (`pages_integration_test.go`); Concepts → Escalation chains has \"Where chains Page\" and \"How a Page reaches a person\" with the table of C-32.FR-11 and the note that the push of Mentions in Threads is still to be verified; `make docs-check` passes."
verify: "make ci test-integration e2e-go docs-check"
operator_attention: false
issue: null
---

# S-155. Pages by Mention in the Threads of their own chains, the runs of Notification rules, and undelivered Pages (BE)

## Scope

**IN**

- The steps that Page by Mention: the On-call of a Schedule and chosen people through their Notification rules, a
  whole Team by its Team mention, and everyone in the chat (C-32.FR-3).
- The runs of Notification rules for those Pages, with their waits and stops; a Mention action is a Page of its step
  (C-31.FR-3).
- Where a chain Pages: the Threads of its own Destinations only (C-32.FR-8).
- The Page as a Loud Thread reply of delivery with its text, its Mentions only through Account links, and the line of an
  unlinked person (C-32.FR-9; C-11.FR-2, C-18.FR-4 revised).
- Undelivered Pages and `page_not_delivered` (C-32.FR-10), `paged` with its people (C-32.FR-19), the metric of Pages,
  `paged` in events mode, and the chains' Destinations in the unreachable warnings of Schedules (C-27.FR-8).

**OUT**

- Assignees, "Assignees: …" in the Root message and the layout, and the Owner's Team: S-168.
- Calls — the `call` step and a rule's call actions, which this story's runs hand to the `CallPages` interface wired to
  nothing until S-157 — and the row "Notification gateway" of C-32.FR-11: S-157.
- The `webhook` step: S-156. The pages of the Alert Group and the chain: S-158, S-159. The push facts: S-160.

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated):
  - `LifecycleEvent` gains `paged` (`x-event-kinds` `escalation`); `TimelineEscalationEntry` gains `team`
    (`TeamRef`), `everyone` and `importance`; `PageNotDeliveredReason` gains `no_root_message`, `no_account_link` and
    `not_delivered`; `EscalationSkipReason` gains `no_mention_possible`; `WebhookEventName` gains `paged`, whose
    `WebhookEvent` carries `mentions` (`WebhookMentionTarget`[], the people as `user` targets) and `page` {`chain`,
    `step`, `importance`}.
  - `EscalationRun.steps[]` gains `pages` ({`kind` `mention`, `users` (`UserRef`[]), `team`, `everyone`, `outcome` —
    `pending`, `delivered`, `not_delivered` —, `reason`}[]).
- **Schema** (migration `0042_pages`, expand only; `schema.md` §4.9, §4.10, §4.11, §4.18):
  - `notification_rule_runs`: `id`, `org_id`, `alert_group_id` (FK, cascade), `escalation_run_id` (FK, cascade),
    `step_number`, `user_id`, `importance`, `actions jsonb NOT NULL` (the rule when it started), `position`, `state`
    (`running`, `done`, `stopped`), `started_at`, `ended_at`; index `(org_id, escalation_run_id)`.
  - `pages`: `id`, `org_id`, `alert_group_id` (FK, cascade), `escalation_run_id`, `notification_rule_run_id`,
    `step_number`, `kind` (`CHECK IN ('mention', 'call')`; calls are made from S-157), `team_id`, `everyone`, `state`
    (`pending`, `delivered`, `not_delivered`), `reason`, `created_at`, `ended_at`; `page_users` (`org_id`, `page_id`,
    `user_id`, `outcome`, `reason`; primary key `(page_id, user_id)`).
  - `thread_replies` gains `page_id` (FK, cascade) and `paged_user_ids bigint[]`; its event and mentions CHECKs are
    replaced by `_v2` ones with `paged`, `NOT VALID`, validated, the old ones dropped, as the Timeline's.
  - `timers` gains the kind `notification_rule_action` and `notification_rule_run_id` (FK, cascade), with
    `timers_subject_v3_check` and a unique partial index, as S-169 added its kind.
  - `timeline_entries` gains `page_everyone`, `page_importance`; `team_id` (S-118) names the Team of a `page_team` step.
  - The down migration drops all of it after deleting the rows of the new kinds.
- **Packages and their imports** (as in S-169): `groups` imports none of the packages of Paging and declares what it
  consumes — `ChainRuns` (S-169) and here `PageTargets` — which `internal/runtime` wires; `delivery` imports `groups`,
  as in L1, and implements `groups.PageTargets`; `gateways` imports `groups` and `delivery`; `notificationrules` imports
  `groups` and `gateways`; `escalation` imports `groups`, `notificationrules`, `schedules`, `teams` and `gateways`;
  `schedules` imports none of them. A Page is a Command of a system transition that the dispatcher runs: `groups`
  records the lifecycle event `paged` in the Timeline, and delivery fans that event out into Thread replies, as it does
  every lifecycle event.
- **Escalation** (`internal/escalation/pages.go`): the `StepPages` of S-169 — `page_on_call` (the On-call at the
  business clock's now, `schedules.OnCallAt`) and `page_people` (each person who may still be Paged) start a run of
  each person's Notification rule (`notificationrules.Start`); `page_team` dispatches the Command `Page` with the Team —
  the chain's owning Team by default — or the token for everyone in the chat.
- **Notification rules** (`internal/notificationrules/runs.go`): a run per person and Page of a step, from
  `Effective(user)` (S-152): each Mention action dispatches the Command `Page` for its person; actions with no wait
  between them come due at the same moment, and the first actions of the people one step Pages that come due together
  are one Command naming them all (Open question 1); a wait is a timer of the kind `notification_rule_action`, whose
  handler this package registers in the runtime; a call action goes to `CallPages`, declared here and wired by S-157. A
  run stops with the run of its chain — acknowledged, snoozed, resolved, started over — which the `ChainRuns` of
  `escalation` tells it in the dispatcher's transaction, and an action due after that is not made.
- **Groups** (`internal/groups/pages.go`, `escalation.go`, `events.go`): the system transitions `Page`, `PageReached`
  (the Page delivered to a person) and `PageNotDelivered` (S-148), with Muster as the actor. `Page` asks `PageTargets`
  for the Destinations of the run that can carry it; with none for a `page_team` step it records
  `escalation_step_skipped` with `no_mention_possible` instead, and with none at all for people `page_not_delivered`
  with `no_root_message`; otherwise it writes the Page (`pages`, `page_users`) and records `paged` (Loud, Mentions
  `paged` or the Team mention or token, kind `escalation`, a Thread reply in the Threads of the chain), and the closed
  table gains its row.
- **Delivery** (`internal/delivery/where.go`, `pages.go`, `threads.go`): `where.go` implements `groups.PageTargets` —
  the Destinations of a run, by its sources, read through delivery's own queries: a Route's chain in the Route's
  Destinations whose delivery has a Root message, a Team's chain in the Team's, else in the Route's, a chain of both
  sources in the union, and for a `page_team` step only those whose Connection can mention the Team or the token
  (`mentions`, C-28.FR-2). The fan-out of `paged` creates one Thread reply per Destination, `pending` at once without
  the Thread batching window; the Mentions come from the people's Account links in each Destination's Identity space
  (`mentions.IdentitySpace`); the outcome of the replies is reported back by the Commands `PageReached` (S-168 adds
  the Assignees) and `PageNotDelivered` — any delivered reply that mentions a person delivers the Page to them, and all
  of a person's replies Not delivered or withheld make it `not_delivered`. Replies withheld while a Destination is
  Broken are dropped at recovery (C-11.FR-19).
- **Messages** (`internal/messages/page.go`): the text of a Page reply — "Step {n}: {Mentions}, you are needed" when it
  mentions anyone, "Chain
  of the Route {Route}" or "Chain of the Team {Team}", "On-call of {Team} {Schedule}" or the people, "normal" or
  "important" with "again in {N} min" when the rule repeats, and "Page not delivered: @{login} has no linked {messenger}
  account" — in the Route's language, in English and Russian (C-12.FR-3 revised).
- **Webhooks** (`internal/webhooks/body.go`): `paged` in events mode with the people as `mentions` data (C-15.FR-11
  revised); a template-mode Destination's "reply in thread" request carries a Page like any Thread reply.
- **Schedules** (`internal/schedules/reach.go`, `query.sql`): the Destinations of the reach of a Schedule add, through
  a query of its own, the Destinations of the Routes and Teams that name a chain with a step that Pages the Schedule.
- **Metric** (`internal/metrics/catalogue.go`): `muster_pages_total{chain,kind,outcome}` (counter, C-32), moved in
  reference.md from "Metrics added in L2".
- **Settings**: `delivery.transient_budget`, `route.language`; the rules of `organization.notification_rules`.
- **Documentation**: Concepts → Escalation chains gains "Where chains Page", "How a Page reaches a person" (the table of
  C-32.FR-11, the push of Mentions in Threads still to be verified, so that at night only a Notification gateway is
  reliable) and "Undelivered Pages".

## Steps

1. Write the migration and `schema.md`. Check: `make test-integration` runs up, down and up.
2. Write where chains Page and the Pages of each step kind. Check: `pages_test.go` and delivery's `where_test.go` cover
   a Route's chain, a Team's chain with and without its own Destination with the Root message, both sources, a Telegram
   Destination for `page_team`, and a Route without a Destination.
3. Write the runs of Notification rules with their waits and stops. Check: `runs_test.go` with a manual clock.
4. Enqueue Page replies, their text and their outcomes; record `paged` and `page_not_delivered`. Check: `page_test.go`,
   `pages_test.go` of delivery, `events_test.go` (one Timeline entry per row of the L2 table so far).
5. Add the webhook event, the metric, the reach and the runs' Pages. Check: `body_test.go`, `reach_test.go`,
   `pages_integration_test.go`.
6. Write `test/e2e/pages_test.go` and the documentation. Check: the Verification below; `make docs-check`.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-169: the Route "Infrastructure" (R, Destinations infra-critical and infra-notice of the fake Mattermost
# server) with deliver_to_teams on and the chain "devops"; the Team "payments" with payments-critical and
# payments-notice and the chain "payments" of scenario 1; ivan On-call of devops' Main and linked to u-ivan of the fake
# server; petr On-call of payments' Main without a Mattermost Account link; default Notification rules; CLK; A.
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake; FMM=127.0.0.1:18065/_fake; CLK=localhost:8082/_dev/clock
REPLIES() { curl -s $FMM/posts | jq -r --arg c "$1" '[.[] | select(.channel_id == $c and .root_id != "")] | map(.message) | .[]'; }
curl -s -X PUT $FAM/groups/p1 -d '{"receiver":"sev","route":"{}","labels":{"alertname":"KubernetesContainerOOMKilled"}}' > /dev/null
PUTA p1 a '{"labels":{"category":"infra","system":"k8s","team":"payments","severity":"warning"}}'; NOTIFY p1 '{"reason":"first notification"}'
G=$(AG 'team%3D%22payments%22'); sleep 2
REPLIES ch-infra-notice
# Step 1: @ivan, you are needed
# Chain of the Route Infrastructure · On-call of devops Main · normal
REPLIES ch-payments-notice
# Page not delivered: @petr has no linked Mattermost account
# Chain of the Team payments · On-call of payments Main · normal
curl -s "${A[@]}" "$API/alert-groups/$G/timeline?kind=escalation" | jq -c '[.items[] | {e: .event, u: [.paged_users[]?.login], r: .reason}]'
# [{"e":"paged","u":["ivan"],"r":null},{"e":"paged","u":["petr"],"r":null},{"e":"page_not_delivered","u":["petr"],"r":"no_account_link"}]

# the devops chain's important Page repeats after 5 minutes unless acknowledged
curl -s -X POST $CLK -d '{"advance_seconds": 600}' > /dev/null; sleep 1
REPLIES ch-infra-notice | grep -c "important, again in 5 min"                                         # 1
curl -s -X POST $CLK -d '{"advance_seconds": 300}' > /dev/null; sleep 1
REPLIES ch-infra-notice | grep -c "you are needed"                                                    # 3
curl -s localhost:8082/metrics | grep '^muster_pages_total{' | grep -c 'outcome="not_delivered"'      # 1

# acknowledged in between: the run stops and no second Mention follows
curl -s "${A[@]}" -X POST $API/alert-groups/$G/acknowledge > /dev/null
N=$(curl -s $FMM/posts | jq length); curl -s -X POST $CLK -d '{"advance_seconds": 1800}' > /dev/null; sleep 1
[ "$(curl -s $FMM/posts | jq '[.[] | select(.root_id != "")] | length')" -le "$N" ] && echo "no Page after the acknowledgement"
```

`test/e2e/pages_test.go` repeats these checks and adds a chain named by the Route and the Team Paging once in both
Threads, a Route with a chain and no Destination (`no_root_message`), a reply that the fake server refuses until it ends
Not delivered, a Destination Broken while a Page comes due and the reply not sent after recovery, `page_team` with a
Team mention and with `here`, `page_team` on a Telegram-only chain (`no_mention_possible`), a Bridge Destination without
`native_mentions`, the `paged` event in an events-mode webhook, and the Schedule warning for a chain's Destination.

## Open questions

1. **One Thread reply or one per person.** C-32.FR-9 shows "Step {n}: @ivan, you are needed" and the reference table
   "Step {n}: {Mentions}, you are needed", while each person runs their own rule. Options: (a) the first actions of the
   people a step Pages that come due together make one reply per Destination naming them all, and each later action of a
   rule a reply of its own; (b) one reply per person always. Recommended: (a), which matches the table and keeps a
   Thread short; the contract follows it.
2. **The text of a later Mention of a rule.** C-32.FR-9 gives the text of a step's Page. Options: (a) a later Mention
   of the person's rule repeats "Step {n}: @ivan, you are needed" with "· important, again" and no further repeat
   announced; (b) a text of its own, such as "Reminder of step {n}". Recommended: (a); the contract follows it, and the
   texts are confirmed in the pull request.

## Notes

- Suggested commit: `feat(escalation): page people by mention in the threads of their chains`.
- A Page by Mention is a Thread reply of delivery and nothing else (ADR-0005, ADR-0018): Muster sends no direct
  messages to Page people.
- Until S-157 a rule's call action and a `call` step do nothing; S-157 wires them to the queue of S-148.
- Until S-168 a delivered Page makes nobody an Assignee; S-168 hooks the Assignees into the outcome of Pages.
- Migration `0042` follows the story map; a story merged out of that order renumbers its migration (S-167's Notes).

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-32.FR-3 | partial | the steps that Page by Mention; calls are S-157 |
| C-32.FR-8 | full | |
| C-32.FR-9 | full | |
| C-32.FR-10 | partial | undelivered Pages by Mention; calls that work while a messenger is down are S-157 |
| C-32.FR-11 | partial | the rows of the messengers and outgoing webhooks; the gateway's is S-157, the push facts S-160, the page S-161 |
| C-32.FR-19 | partial | `paged` with its people and `muster_pages_total`; the expand step was S-148 |
| C-32.AC-1 | full | |
| C-32.AC-6 | partial | the line and `page_not_delivered`; the Assignees are S-168 |
| C-32.AC-7 | partial | `no_root_message` for Mention steps; the gateway step is S-157 |
| C-32.AC-11 | partial | `paged`, `page_not_delivered`, `escalation_step_skipped`; `assignees_changed` is S-168, `escalation_webhook` S-156 |
| C-32.AC-13 | partial | the Telegram-only `page_team` step; the Assignees in the Root message are S-168 |
| C-32.AC-16 | partial | Pages in the Threads of both; S-169 runs it once |
| C-32.AC-17 | partial | the Pages of the runs |
| C-31.FR-3 | partial | runs with Mentions and waits; call actions are S-157 |
| C-31.AC-1 | full | |
| C-27.FR-8 | partial | the Destinations of the chains |
| C-09.FR-22 | partial | revised for L2: `paged` |
| C-11.FR-2 | partial | revised for L2: Pages as Thread replies of delivery |
| C-11.FR-20 | partial | revised for L2: `paged` as Thread replies; `assignees_changed` is S-168 |
| C-12.FR-3 | partial | revised for L2: the texts of Pages |
| C-15.FR-2 | partial | revised for L2: `paged` in events mode |
| C-15.FR-11 | full | revised for L2: the people of `paged` as `mentions` data |
| C-18.FR-4 | full | revised for L2 |
