---
id: S-155
title: Pages by Mention in the Threads of their own chains, the runs of Notification rules, and undelivered Pages (BE)
capability: C-32
kind: be
layer: L2
depends_on: [S-152, S-164, S-165, S-169, S-171]
covers: [C-32.FR-3, C-32.FR-8, C-32.FR-9, C-32.FR-10, C-32.FR-11, C-32.FR-19, C-32.AC-1, C-32.AC-6, C-32.AC-7, C-32.AC-11, C-32.AC-13, C-32.AC-16, C-31.FR-3, C-31.AC-1, C-09.FR-22, C-11.FR-2, C-11.FR-20, C-12.FR-3, C-15.FR-2, C-18.FR-4]
files_touched:
  - api/openapi.yaml
  - internal/db/migrations/0049_pages.up.sql
  - internal/db/migrations/0049_pages.down.sql
  - design/db/schema.md
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - docs/install/versions.md
  - internal/escalation/pages.go
  - internal/escalation/pages_test.go
  - internal/escalation/pages_integration_test.go
  - internal/escalation/engine.go
  - internal/escalation/engine_test.go
  - internal/escalation/runs.go
  - internal/escalation/runs_test.go
  - internal/escalation/query.sql
  - internal/notificationrules/runs.go
  - internal/notificationrules/runs_test.go
  - internal/notificationrules/query.sql
  - internal/groups/escalation.go
  - internal/groups/pages.go
  - internal/groups/pages_test.go
  - internal/groups/events.go
  - internal/groups/events_test.go
  - internal/groups/dispatcher.go
  - internal/groups/dispatcher_test.go
  - internal/groups/query.sql
  - internal/delivery/pages.go
  - internal/delivery/pages_test.go
  - internal/delivery/pages_integration_test.go
  - internal/delivery/where.go
  - internal/delivery/where_test.go
  - internal/delivery/threads.go
  - internal/delivery/outcomes.go
  - internal/delivery/enqueue.go
  - internal/delivery/enqueue_test.go
  - internal/delivery/delivery.go
  - internal/delivery/worker.go
  - internal/delivery/worker_test.go
  - internal/delivery/live_test.go
  - internal/delivery/render.go
  - internal/delivery/render_test.go
  - internal/delivery/query.sql
  - internal/messages/message.go
  - internal/messages/page.go
  - internal/messages/page_test.go
  - internal/messages/texts/en.json
  - internal/messages/texts/ru.json
  - internal/mattermost/layout.go
  - internal/mattermost/layout_test.go
  - internal/slack/layout.go
  - internal/slack/layout_test.go
  - internal/timers/query.sql
  - internal/timers/worker.go
  - internal/metrics/catalogue.go
  - internal/logging/events.go
  - internal/runtime/runtime.go
  - internal/runtime/runtime_test.go
  - internal/fakes/fakemattermost/fakemattermost.go
  - design/prd/l1/reference.md
  - test/e2e/pages_test.go
  - test/e2e/escalation_runs_test.go
  - docs/concepts/escalation-chains.md
  - docs/outgoing-webhooks/events.md
acceptance:
  - "[C-32.FR-8, C-32.AC-1, C-32.AC-16] On the setup of scenario 1 of C-32, a warning Alert Group published in `infra-notice` and `payments-notice` gets \"Step 1\" in each Thread from its own chain only — the Route's chain in the Route's Destinations, the Team's chain in the Team's — and a Team's chain whose Team has no Destination with the Root message writes in the Route's, while one whose Team's Destinations with the Root message all take no Page yet (Telegram, a Bridge, a webhook) writes nowhere; a chain that the Route and a Team both name Pages once in the Threads of both (`where_test.go` over the reach vectors, `TestPageTargets`, e2e `TestPages`)."
  - "[C-32.FR-3, C-31.FR-3, C-31.AC-1] A `page_on_call` step Pages the On-call of its Schedule at that moment and a `page_people` step each person it names, each by their Notification rule of the step's importance: with the default rules a normal Page makes one Thread reply that mentions the person, an important Page a second one 5 minutes later, and none when the Alert Group was acknowledged in between; the run of a rule stops when the Alert Group is acknowledged, snoozed or resolved, and when its chain starts over or stops (`TestRuleRuns`, `TestRuleRunStops`, `TestMakeStep`, `TestStepPagesInProgress`, `TestIntegrationPages`, e2e `TestPages`)."
  - "[C-31.FR-3] A rule run whose timer comes due late, after Muster was down, with several Mentions overdue makes one Page of them instead of a burst, and its next wait counts from then (`TestMissedMentions`)."
  - "[C-32.FR-9, C-32.AC-6, C-18.FR-4, C-11.FR-2, C-12.FR-3] A Page is a Loud Thread reply that delivery sends, due at once and never batched with other replies: \"Step {n}: @ivan, you are needed\" and \"Chain of the Route Infrastructure · On-call of devops Main · normal\", or \"· important, again in 5 min\" when the person's rule repeats and \"· important, again\" for its later Mention; a person without an Account link in the Identity space of that Destination's Connection is named in the line \"Page not delivered: @petr has no linked Mattermost account\" instead of a Mention, in English or in the Route's language; in Mattermost and Slack the Mentions stand in the first line (`TestPageMessage`, `TestReplyPost`, `TestLinkedMentions` of Slack, `TestPageReplies`, e2e `TestPages`)."
  - "[C-32.FR-3, C-32.FR-9, C-32.AC-13] A `page_team` step writes the Destination's Team mention of the Team — the owning Team the chain had when its run started by default — or its token for everyone in the chat; where a Destination's Connection can mention neither it makes no reply there, and when it can reply in none of the chain's Destinations, such as when the only one is a Telegram channel, it records `escalation_step_skipped` with the reason `no_mention_possible` (`TestPageTargets`, `TestPageTeam`, `TestMakeStep`, `TestPageRepliesOfTeams`, e2e `TestPages`)."
  - "[C-32.FR-10, C-32.AC-6, C-32.AC-7] A Page records the Quiet `page_not_delivered` with the person and the reason — `no_root_message` when none of the chain's Destinations has the Alert Group's Root message, `no_account_link` when the person has no Account link in any of their Identity spaces, `not_delivered` when every Thread reply of the Page that would mention them ended Not delivered or was withheld while its Destination was Broken; a reply withheld while Broken is not sent after recovery; a Route with a chain and no Destination records `page_not_delivered` `no_root_message` for its Mention steps (`TestPagePeople`, `TestMentionPageNotDelivered`, `TestPageRepliesOfTeams`, e2e `TestPages`)."
  - "[C-32.FR-19, C-32.AC-11, C-09.FR-22, C-11.FR-20] Every Page by Mention records one Loud `paged` (Timeline kind `escalation`) with `mentions` `[\"paged\"]`, its people in `paged_user_ids`, the Team of a `page_team` step in `team_id` and its token in `page_everyone`, and the importance in `page_importance`; together with `page_not_delivered` and `escalation_step_skipped`, each row of the L2 lifecycle event table so far produces exactly one Timeline entry with its event, kind, loudness and Mentions (`TestLifecycleEventTable`, `TestTableMatchesLifecycleEvents`, `TestLifecycleRows`)."
  - "[C-32.FR-19] Pages count in `muster_pages_total{chain,kind,outcome}` with `kind` `mention` and `outcome` `delivered` or `not_delivered` once each ended, except a Page whose chain was deleted; reference.md moves the metric into the catalogue (`TestPagePeople`, `TestMentionPageNotDelivered`, refgen's `TestCatalogueMatchesRegistry`, e2e `TestPages`)."
  - "[C-15.FR-2] Outgoing webhooks in events mode receive `paged` as every lifecycle event and `page_not_delivered` with its new reasons (`TestTableMatchesLifecycleEvents`)."
  - "[C-31.FR-3, C-32.FR-3] The wait of a Notification rule run is a timer whose subject is the run — `notification_rule_run_id` set, `alert_group_id`, `storm_id` and `escalation_run_id` null — so a `page_people` step that Pages two people with repeating rules keeps two `notification_rule_action` timers on one Alert Group beside its chains' `escalation_step` timers and its Reminder, each firing for its own person and reading the Alert Group through the rule run; `timers_alert_group_key` and `UpsertTimer` are unchanged (`TestIntegrationPages` in `internal/escalation`, `TestIntegrationPages` in `internal/db`)."
  - "[C-32.FR-11] Concepts → Escalation chains has \"Where chains Page\", \"How a Page reaches a person\" with the rows of C-32.FR-11 for Mattermost and Slack and the note that the push of Mentions in Threads is still to be verified, and \"Undelivered Pages\"; the interim warning that a chain silences the ack timeout is gone; `make docs-check` passes."
verify: "make ci test-integration e2e-go docs-check"
operator_attention: false
issue: 444
---

# S-155. Pages by Mention in the Threads of their own chains, the runs of Notification rules, and undelivered Pages (BE)

## Scope

**IN**

- The steps that Page by Mention: the On-call of a Schedule and chosen people through their Notification rules, a
  whole Team by its Team mention, and everyone in the chat (C-32.FR-3).
- The runs of Notification rules for those Pages, with their waits, their stops and the collapse of the Mentions missed
  while Muster was down; a Mention action is a Page of its step (C-31.FR-3).
- Where a chain Pages: the Threads of its own Destinations only (C-32.FR-8), in Mattermost and Slack.
- The Page as a Loud Thread reply of delivery with its text, its Mentions only through Account links, and the line of an
  unlinked person (C-32.FR-9; C-11.FR-2, C-18.FR-4 revised), the Mentions in the first line of the reply in the
  Mattermost and Slack adapters.
- Undelivered Pages and `page_not_delivered` (C-32.FR-10), `paged` with its people (C-32.FR-19), the metric of Pages,
  `paged` in events mode as every lifecycle event.

**OUT**

- Assignees, "Assignees: …" in the Root message and the layout: S-168.
- Calls — the `call` step and a rule's call actions, which this story's runs hand to the `CallPages` interface wired to
  nothing until S-157 — and the row "Notification gateway" of C-32.FR-11: S-157.
- The chains' Destinations in the unreachable warnings of Schedules, with the Telegram discussion group check for
  them: S-172.
- The `webhook` step: S-156. The pages of the Alert Group and the chain: S-158, S-159. The push facts: S-160.
- **Moved** when the orchestrator split this story before it was implemented (it would have touched about 80 files):
  - to **S-177**: Page replies in Telegram and through Bridges — the Mention line in the Telegram adapter and the bridge
    envelope, Bridge Mention markers, and no Mention through a bridge without `native_mentions` — and the outgoing
    webhooks: `paged` in events mode with its people as `mentions` data (C-15.FR-11), and Pages in the "reply in
    thread" request of a template-mode webhook; until S-177, `delivery.PageTargets` chooses the targets among the
    Destinations of every type with the Root message and then leaves Telegram, Bridge and webhook Destinations out;
  - to **S-178**: the read side — `listEscalationChainRuns` with each step's `pages`, the fields `team`, `everyone` and
    `importance` of `TimelineEscalationEntry`, the SPA's case of `paged` in `entryText`, and the end-to-end checks of
    those reads.

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated): `LifecycleEvent` gains `paged` (`x-event-kinds`
  `escalation`) and `MentionName` the Mention `paged`; `PageNotDeliveredReason` gains `no_root_message`,
  `no_account_link` and `not_delivered`; `EscalationSkipReason` gains `no_mention_possible`; `WebhookEventName` gains
  `paged`. The fields of the read side are S-178's.
- **Schema** (migration `0049_pages`, expand only; `schema.md` §1, §2, §4.9, §4.10, §4.11, §4.18). Migration 0034 already
  gave `timeline_entries` the event and Mention `paged`, `paged_user_ids` and the kind `escalation`, and `webhook_events`
  the event `paged`; 0049 adds the rest:
  - `notification_rule_runs`: `id`, `org_id`, `alert_group_id` (FK, cascade), `escalation_run_id` (FK, cascade),
    `step_number`, `user_id`, `importance`, `actions jsonb NOT NULL` (the rule when it started), `position`, `state`
    (`running`, `done`, `stopped`), `started_at`, `ended_at`; indexes `(escalation_run_id)` — which also serves the
    cascade, unlike `(org_id, escalation_run_id)` — and `(alert_group_id)`.
  - `pages`: `id`, `org_id`, `alert_group_id` (FK, cascade), `escalation_run_id` (FK, cascade),
    `notification_rule_run_id` (FK, cascade; set for a later Mention of a rule), `step_number`, `kind` (`CHECK IN
    ('mention', 'call')`; calls are made from S-157), `team_id`, `everyone`, `importance`, `schedule_id` (the On-call's
    Schedule, `ON DELETE SET NULL`), `chain_team_id` (the Team of "Chain of the Team"), `again_seconds` (the wait until
    the rule's next Mention), `again` (a later Mention), `state` (`pending`, `delivered`, `not_delivered`), `reason`,
    `created_at`, `ended_at`; `page_users` (`org_id`, `page_id`, `user_id`, `outcome`, `reason`; primary key
    `(page_id, user_id)`).
  - `thread_replies` gains `page_id` (FK, cascade), `paged_user_ids bigint[]` and `page_source` (`route` or `team`, the
    source whose Destination the reply is in, which its text names); its event and mentions CHECKs are replaced by
    `_v2` ones with `paged`, `NOT VALID`, validated, the old ones dropped, as the Timeline's.
  - `timers` gains the kind `notification_rule_action` through `timers_kind_v3_check`, replacing S-169's `_v2` (`NOT
    VALID`, validated, the old one dropped), and `notification_rule_run_id` (FK `notification_rule_runs`, `ON DELETE
    CASCADE`). As for S-169's `escalation_step`, the subject of such a timer is its run: `alert_group_id` and `storm_id`
    are null, so `timers_alert_group_key` and `UpsertTimer` stay untouched and the runs of every person a step Pages
    each have a timer. `timers_subject_v3_check` replaces S-169's `_v2` with the branch `notification_rule_action` →
    `notification_rule_run_id` set, `alert_group_id`, `storm_id` and `escalation_run_id` null, and
    `notification_rule_run_id` null on every other branch; a unique partial index `timers_notification_rule_run_key
    (notification_rule_run_id) WHERE notification_rule_run_id IS NOT NULL`, the predicate S-169 chose for its own key,
    which the cascade's `DELETE` implies. A rule run cascades from its chain's run and its Alert Group, and its timer
    from it.
  - `timeline_entries` gains `page_everyone`, `page_importance`; `team_id` (S-118) names the Team of a `page_team` step.
  - The down migration drops all of it after deleting the rows of the new kinds; `docs/install/versions.md` has the
    upgrade note and the recovery of a failure in any of the three transactions.
- **Packages and their imports**: `groups` imports none of the packages of Paging and declares what it consumes —
  `ChainRuns` (S-169) and here `PageTargets` — which `internal/runtime` wires; `delivery` imports `groups`, as in L1,
  and implements `groups.PageTargets`; `escalation` imports `groups`, `notificationrules`, `schedules` and `teams`;
  `notificationrules` imports neither `groups` nor `escalation`: `internal/organization` imports it and
  `internal/groups` imports `internal/organization`, so the import would be a cycle. A Page is a system transition of
  the dispatcher: `groups` records the lifecycle event `paged` in the Timeline, and delivery fans that event out into
  Thread replies, as it does every lifecycle event.
- **Escalation** (`internal/escalation/pages.go`, `engine.go`, `runs.go`): `StepPages` of S-169 returns what it made,
  `StepMade` — its Pages, which `ChainProgress.Pages` carries in the order of the steps among the skips and webhooks,
  or the reason to skip the step; `page_on_call` (the On-call at the business clock's now) and `page_people` (each
  person who may still be Paged) start a run of each person's Notification rule (`notificationrules.Start`); `page_team`
  Pages the Team — the run's `owning_team_id`, the owning Team the chain had when its run started, by default, else
  the first Team that named the chain — or the token, skipped with `no_mention_possible` when `PageTargets` finds no
  Destination that can carry it. The stop of runs stops their rule runs (`StopRules`). The handler of the timer
  `notification_rule_action`, `FireRule`, reads the rule run by the timer's `notification_rule_run_id`, takes its Alert
  Group through it, locks it through the dispatcher (`ChainStep`) and lets the rule run advance.
- **Notification rules** (`internal/notificationrules/runs.go`): a run per person and Page of a step, from the rule in
  effect (`Effective`, read in the dispatcher's transaction): actions with no wait between them come due at the same
  moment, and the first Mentions of the people one step Pages that come due together are one Page naming them all
  (Open question 1); a wait is a timer of the kind `notification_rule_action` whose subject is the rule run, written by
  this package's own upsert, never `UpsertTimer`; `Advance` locks the rule run after its Alert Group and does nothing
  when it is no longer `running` or its timer moved, and stops it when the Alert Group is no longer firing or its
  chain's run stopped; several Mentions overdue at once after an outage are one Page, and the next wait then counts from
  now, as `notices_missed` collapses overdue notices (C-17.FR-7); a call action goes to `CallPages`, declared here and
  wired by S-157. A run stops with the chains of its Alert Group — acknowledged, snoozed, resolved, started over —
  which the `ChainRuns` of `escalation` tells it in the dispatcher's transaction, deleting its timer (`StopForGroup`),
  also when the run of its own chain ran out already, as its rule may still repeat.
- **Timers** (`internal/timers`): the claim returns `notification_rule_run_id`, which `Timer` gains.
- **Groups** (`internal/groups/pages.go`, `escalation.go`, `events.go`, `dispatcher.go`): `ChainStep` records the Pages
  of a `ChainProgress` as system transitions with Muster as the actor: each asks `PageTargets` for the Destinations of
  the run that can carry it; with none for a `page_team` step it records `escalation_step_skipped` with
  `no_mention_possible` instead, and with none at all for people `page_not_delivered` with `no_root_message`;
  otherwise it writes the Page (`pages`, `page_users`) and records `paged` (Loud, Mention `paged`, kind `escalation`),
  then `page_not_delivered` with `no_account_link` for a person linked in none of the Destinations and
  `not_delivered` for one whose every reply is withheld. `SettlePage` is the transition of PageReached and
  PageNotDelivered: delivery reports the outcome of the replies, the people a delivered reply mentioned are reached, and
  once every reply ended the rest are not delivered (`page_not_delivered` `not_delivered`) and the Page ends. The
  closed table gains the row of `paged`.
- **Delivery** (`internal/delivery/where.go`, `pages.go`, `threads.go`, `enqueue.go`, `worker.go`): `where.go`
  implements `groups.PageTargets` — the Destinations of a run, by its sources, read through delivery's own queries: a
  Route's chain in the Route's Destinations whose delivery has a Root message — or whose first Publication is on its
  way and held by no Storm, as the chains start with the first Publication in one Destination, often before the
  others — a Team's chain in the Team's, else in the Route's, a chain of both sources in the union, and for a `page_team` step only those whose Connection can mention the
  Team (its Team mention for the Connection) or the token (`mentions.Can`, C-28.FR-2), each with the people linked in
  its Identity space (`mentions.IdentitySpace`) and whether it is Broken. Which Destinations a source's entries reach
  comes from `routing.Reach` (S-121), never a copy of the rule, and `where_test.go` loads the vectors of
  `internal/routing/testdata/reach/` (S-121), the one set of cases of who an Alert Group reaches (plan review, M4).
  Until S-177 only Mattermost and Slack Destinations carry Pages. The fan-out of `paged` creates one Thread reply per
  Destination of the Page, `pending` at once without the Thread batching window, with the people it mentions decided
  when it is queued; the reply is rendered from its Page and mentions its people by their Account links, or the Team
  mention or the token. Each reply settles its Page once its outcome committed, and every round of the worker settles
  the Pages whose replies all ended otherwise, such as those dropped at the recovery of a Broken Destination
  (C-11.FR-19): any delivered reply that mentions a person delivers the Page to them, and all of a person's replies Not
  delivered or withheld make it `not_delivered`.
- **Delivery's event table** (`internal/delivery/enqueue.go`): the row of `paged` (a Thread reply in the Threads of
  the chain); `enqueue_test.go` counts it.
- **Messages** (`internal/messages/page.go`, `message.go`): the text of a Page reply — the Lead "Step {n}: " and ",
  you are needed" around the Mentions when it mentions anyone, "Chain of the Route {Route}" or "Chain of the Team
  {Team}", "On-call of {Team} {Schedule}", the people, "Team {Team}" or "everyone in the chat", "normal" or
  "important" with "again in {N} min" when the rule repeats and "again" for its later Mention, and "Page not
  delivered: @{login} has no linked {messenger} account" — in the Route's language, in English and Russian (C-12.FR-3
  revised). An adapter writes the Lead around its Mention line (`Message.MentionLine`): Mattermost and Slack here.
- **Metric** (`internal/metrics/catalogue.go`): `muster_pages_total{chain,kind,outcome}` (counter, C-32), moved in
  reference.md from "Metrics added in L2".
- **Settings**: `delivery.transient_budget`, `route.language`; the rules of `organization.notification_rules`.
- **Documentation**: Concepts → Escalation chains gains "Where chains Page", "How a Page reaches a person" (the rows
  of C-32.FR-11 for Mattermost and Slack, the push of Mentions in Threads still to be verified, so that at night only a
  Notification gateway is reliable) and "Undelivered Pages", and loses the interim warning of S-169; Outgoing webhooks →
  Events lists `paged` and the new reasons.

## Steps

1. Write the migration and `schema.md`. Check: `make test-integration` runs up, down and up.
2. Write where chains Page and the Pages of each step kind. Check: `pages_test.go` and delivery's `where_test.go` cover
   a Route's chain, a Team's chain with and without its own Destination with the Root message, both sources, a Telegram
   Destination for `page_team`, and a Route without a Destination.
3. Write the runs of Notification rules with their waits, stops and missed Mentions. Check: `runs_test.go` with a manual
   clock, with two people Paged by one step whose important rules both repeat; `pages_integration_test.go` keeps their
   two timers, the chains' step timers and a Reminder on one Alert Group at once.
4. Enqueue Page replies, their text and their outcomes; record `paged` and `page_not_delivered`. Check: `page_test.go`,
   `pages_test.go` of delivery and groups, `events_test.go` (one Timeline entry per row of the L2 table so far).
5. Add the metric. Check: `pages_test.go` of groups, refgen's tests.
6. Write `test/e2e/pages_test.go` and the documentation. Check: the Verification below; `make docs-check`.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-169: the Route "Infrastructure" (R, Destinations infra-critical and infra-notice of the fake Mattermost
# server) with deliver_to_teams on and the chain "devops"; the Team "payments" with payments-critical and
# payments-notice and the chain "payments" of scenario 1; ivan On-call of devops' Main and linked to u-ivan of the fake
# server; petr On-call of payments' Main without a Mattermost Account link; default Notification rules; CLK; A.
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake; FMM=127.0.0.1:18065/_fake; CLK=localhost:8082/_dev/clock
REPLIES() { curl -s $FMM/posts | jq -r --arg c "$1" '[.[] | select(.channel_id == $c and .root_id != "")] | map(.message) | .[]' | sed 's/\xe2\x80\x8b//g'; }
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

`test/e2e/pages_test.go` repeats these checks and adds an acknowledgement between the two Mentions of an important rule,
a chain named by the Route and the Team Paging once in both Threads, a Route with a chain and no Destination
(`no_root_message`), `page_team` with a Team mention and with `here` and a Team without a Team mention for the
Connection (`no_mention_possible`), a reply that the fake server refuses until it ends Not delivered, and a Page in a
Slack Thread. The live run also checks, in psql, the rule runs (`notification_rule_runs`) and their timers (`timers`
with `kind='notification_rule_action'`), and `page_team` on a chain whose only Destination is a Telegram channel
(`escalation_step_skipped` `no_mention_possible`).

## Open questions

1. **One Thread reply or one per person.** Settled by reference.md (banners: "Step {n}: {Mentions}, you are needed"):
   the first actions of the people a step Pages that come due together make one reply per Destination naming them all,
   and each later action of a rule a reply of its own.
2. **The text of a later Mention of a rule.** Settled by the maintainer: it repeats "Step {n}: @ivan, you are needed"
   with "· important, again", and announces no further repeat.

## Notes

- Suggested commit: `feat(escalation): page people by mention in the threads of their chains`.
- A Page by Mention is a Thread reply of delivery and nothing else (ADR-0005, ADR-0018): Muster sends no direct
  messages to Page people.
- Until S-157 a rule's call action and a `call` step do nothing; S-157 wires them to the queue of S-148.
- Until S-168 a delivered Page makes nobody an Assignee; S-168 hooks the Assignees into the outcome of Pages
  (`SettlePage`).
- Until S-177 a chain makes no Thread reply in a Telegram or Bridge Destination or an outgoing webhook: a chain whose
  Destinations with the Root message are all of those records `page_not_delivered` `no_root_message` for its people,
  and its `page_team` steps `no_mention_possible`. Such a Destination still has the Root message when the targets are
  chosen (C-32.FR-8): a Team whose Destinations with it are all of those does not fall back to the Route's, so a step
  never rings another team's chat; only a Team none of whose Destinations has the Root message falls back.
- Migration `0049`, not `0042` as the story map first gave: the stories merged before this one took 0042 to 0048, and a
  story merged out of the story map's order renumbers its migration (S-167's Notes).
- The timers of rule runs name their run, not the Alert Group, for the reason S-169's Notes give (plan review, H2): one
  step may Page several people, each with a rule run that waits.
- The Team of "Chain of the Team" and the default Team of a `page_team` step come from `escalation_runs.owning_team_id`,
  the owning Team the chain had when its run started (P-73), else the first Team that named the chain; never the
  chain's current owner (follow-up of S-169).
- The Mentions a rule run misses while Muster was down are collapsed into one Page, as `notices_missed` collapses
  overdue notices (follow-up of S-169). A chain whose own steps come due late after an outage Pages each person, Team
  or everyone once in that firing: a step that would Page only whom the firing already Paged at an earlier due time
  waits, from then, as long as it would have after those Pages on time, and the steps after it wait with it; calls
  and webhooks are made each time (#594).
- A reply that ended, sent or Not delivered, settles its Page in a transaction of its own after its outcome committed,
  so that the Alert Group is locked before the Page as every change of it does; a reply attempted again settles
  nothing. A crash between the two, or replies that end otherwise (dropped, withheld), leave the Page to the next round
  of the worker, which settles every Page whose replies all ended.
- A later Mention of a rule reads the Schedule of its step through `schedules`: the run's steps are a snapshot that a
  Schedule deleted after the chain changed outlives, so the Page then has no Schedule and its reply names the person
  instead of "On-call of …", rather than failing on the foreign key at every lease.
- `muster_pages_total` does not count a Page whose chain was deleted before it ended, which has no `public_id` left to
  name it by.
- Which Destinations a source owns comes from the entries that reached them, not from the reach at the Alert Group's
  current level: a source owns a Destination with the Root message when one of the entries recorded on its delivery
  (`deliveries.entries`, which `ListPageRoots` returns) is the source's own and the source still names the
  Destination. `EnsureDelivery` appends every entry that `routing.Reach` found at a level the Alert Group had and
  never removes one, so after the level falls below critical a Team's chain still Pages in the Team's critical
  Destination that has the Root message instead of falling back to the Route's, and a Route's chain in the Route's;
  a Destination whose first Publication is on its way owns by its entries the same way. A source that names a shared
  Destination under a condition the Alert Group never met does not own it: with a warning Alert Group that was never
  critical, Team devops naming a shared channel for any level, and Team payments and the Route naming it for critical
  only, the chains of payments and of the Route do not Page there, so a payments `@here` never rings devops' channel;
  the union of `routing.Reach` over every level would give them that channel.
- The one Page of a step that names several people says "again in N min" only when every one of them has a rule
  whose next Mention follows after the same N; when their rules differ, or one of them does not repeat, it says nothing
  of a repeat (C-32.FR-9). A Mention of a rule after a wait is "again" only when the rule Mentioned the person before
  it: the first Mention of `[call, wait, mention]` is not.
- The worker's round settles the Pages whose replies all ended a batch at a time in id order after the last one, at
  most `PageSettleBatches` (4) batches a round, so that a backlog, such as the replies a Broken Destination dropped at
  its recovery, does not hold back the deliveries and replies of the next round. The next round goes on after the last
  Page listed, a cursor per Organization that each replica keeps in memory, and starts again from the oldest after a
  short batch. A round that stopped at its batches with a full last batch waits at most `MinWait` for the next one,
  so that a backlog drains without waiting for `MaxWait` between rounds. It logs a Page that fails to settle and goes
  on, so that failing Pages, even more than a round's batches of them, never hold back the ones after them.
- Story-file changes made with the implementation, against the first contract:
  - `files_touched` gained the files the contract missed: the engine and the runs of S-169 (`engine.go`, `runs.go`,
    their tests) for `ChainProgress.Pages` and the stop of rule runs; `internal/groups/dispatcher.go`,
    `dispatcher_test.go` and `pages_test.go` for the page columns of the Timeline and the fakes; delivery's
    `delivery.go`, `worker.go`, `worker_test.go`, `render.go`, `render_test.go` and `live_test.go` (whose fan-out of
    every lifecycle event needs a Page for `paged`) for the Page settler, the Team
    mentions and the render of a Page; `internal/messages/message.go` and the Mattermost and Slack layouts with their
    tests for the Mention line inside the text; `internal/fakes/fakemattermost/fakemattermost.go` for the user `ivan`
    of the Verification; `internal/runtime/runtime_test.go`; `docs/install/versions.md` and
    `docs/outgoing-webhooks/events.md`. It lost `internal/groups/read.go`, `internal/api/alertgroups.go` and
    `web/src/components/timeline.tsx`, which moved to S-178, and `internal/webhooks/body.go` with its test, which moved
    to S-177.
  - The schema gained `pages.importance`, `schedule_id`, `chain_team_id`, `again_seconds` and `again`, which the text of
    a reply needs, and `thread_replies.page_source`; the index of rule runs is `(escalation_run_id)`.
  - The handler of `notification_rule_action` is `escalation`'s `FireRule`, not `notificationrules`': the import cycle
    above.
  - After review: `files_touched` gained `internal/logging/events.go`, whose `delivery_work_failed` now lists the work
    `page`; the targets of a Team's chain are chosen among its Destinations of every type with the Root message
    (C-32.FR-8), the foreign keys of the new columns of `thread_replies` and `timers` are added `NOT VALID` and
    validated in the second transaction, with `pages_schedule_idx`, and the three Notes above on settling, deleted
    Schedules and deleted chains were added.
  - After the second review: `files_touched` gained `internal/delivery/outcomes.go`, whose outcome of a Thread reply
    now says whether it ended the reply, and `internal/delivery/pages_integration_test.go` for `ListPageRoots` on
    PostgreSQL; the three Notes above on the reach at every Severity level, "again" and the round's settling were
    added.
  - After the third review: the Note on which Destinations a source owns says that ownership comes from the entries
    recorded on the delivery, which `ListPageRoots` now returns, as the reach at every level gave a source a shared
    Destination that only another source's entry reached; the Note on the round's settling says that a round settles
    at most `PageSettleBatches` batches, that the next one goes on after the last Page listed, and that it comes after
    at most `MinWait` when the round stopped at its batches.
  - At the final gate: `files_touched` gained `test/e2e/escalation_runs_test.go`, whose Team payments now has a Team
    mention, because its `page_team` step, made without a reply until this story, is now skipped with
    `no_mention_possible` when the Team has none (C-32.AC-13).

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-32.FR-3 | partial | the steps that Page by Mention; calls are S-157 |
| C-32.FR-8 | partial | in Mattermost and Slack; Telegram, Bridges and outgoing webhooks are S-177 |
| C-32.FR-9 | partial | in Mattermost and Slack; Telegram and Bridges are S-177 |
| C-32.FR-10 | partial | undelivered Pages by Mention; calls that work while a messenger is down are S-157 |
| C-32.FR-11 | partial | the rows of Mattermost and Slack; Telegram, Bridge and outgoing webhooks S-177, the gateway S-157, the push facts S-160, the page S-161 |
| C-32.FR-19 | partial | `paged` with its people and `muster_pages_total`; the expand step was S-148 |
| C-32.AC-1 | full | |
| C-32.AC-6 | partial | the line and `page_not_delivered`; the Assignees are S-168 |
| C-32.AC-7 | partial | `no_root_message` for Mention steps; the gateway step is S-157 |
| C-32.AC-11 | partial | `paged`, `page_not_delivered`, `escalation_step_skipped`; `assignees_changed` is S-168, `escalation_webhook` S-156 |
| C-32.AC-13 | partial | the Telegram-only `page_team` step; the Assignees in the Root message are S-168 |
| C-32.AC-16 | partial | Pages in the Threads of both; S-169 runs it once |
| C-31.FR-3 | partial | runs with Mentions, waits, stops and missed Mentions; call actions are S-157 |
| C-31.AC-1 | full | |
| C-09.FR-22 | partial | revised for L2: `paged` |
| C-11.FR-2 | partial | revised for L2: Pages as Thread replies of delivery |
| C-11.FR-20 | partial | revised for L2: `paged` as Thread replies; `assignees_changed` is S-168 |
| C-12.FR-3 | partial | revised for L2: the texts of Pages |
| C-15.FR-2 | partial | revised for L2: `paged` in events mode; its people as data are S-177 |
| C-18.FR-4 | partial | revised for L2: in Mattermost and Slack; Telegram and Bridges are S-177 |
