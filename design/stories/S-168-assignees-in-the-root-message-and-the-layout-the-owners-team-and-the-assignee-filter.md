---
id: S-168
title: Assignees — the people the chains Paged last — in the Root message and the layout, and the Assignee filter (BE)
capability: C-32
kind: be
layer: L2
depends_on: [S-126, S-155]
covers: [C-32.FR-12, C-32.FR-18, C-32.AC-2, C-32.AC-6, C-32.AC-10, C-32.AC-11, C-32.AC-13, C-29.FR-1, C-09.FR-1, C-09.FR-13, C-09.FR-22, C-10.FR-1, C-11.FR-20, C-12.FR-3, C-12.FR-12, C-15.FR-2]
files_touched:
  - api/openapi.yaml
  - api/bridge.yaml
  - internal/db/migrations/0050_assignees.up.sql
  - internal/db/migrations/0050_assignees.down.sql
  - design/db/schema.md
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - internal/groups/assignees.go
  - internal/groups/assignees_test.go
  - internal/groups/dispatcher.go
  - internal/groups/dispatcher_test.go
  - internal/groups/events.go
  - internal/groups/pages.go
  - internal/groups/pages_test.go
  - internal/groups/read.go
  - internal/groups/list.go
  - internal/groups/counts.go
  - internal/groups/filters.go
  - internal/groups/list_integration_test.go
  - internal/groups/list_test.go
  - internal/delivery/footer_integration_test.go
  - internal/escalation/engine.go
  - internal/escalation/engine_test.go
  - internal/escalation/pages.go
  - internal/escalation/pages_test.go
  - internal/escalation/pages_integration_test.go
  - internal/escalation/runs.go
  - internal/notificationrules/runs.go
  - internal/notificationrules/runs_test.go
  - internal/notificationrules/query.sql
  - internal/groups/query.sql
  - internal/archlint/archlint.go
  - AGENTS.md
  - internal/mentions/mentions.go
  - internal/mentions/mentions_test.go
  - internal/mentions/query.sql
  - internal/messages/message.go
  - internal/messages/default.go
  - internal/messages/default_test.go
  - internal/messages/layout.go
  - internal/messages/layout_test.go
  - internal/messages/render.go
  - internal/messages/render_test.go
  - internal/messages/shorten.go
  - internal/messages/query.sql
  - internal/messages/texts/en.json
  - internal/messages/texts/ru.json
  - internal/bridge/envelope.go
  - internal/bridge/envelope_test.go
  - internal/bridge/delivery.go
  - internal/webhooks/body.go
  - internal/webhooks/body_test.go
  - internal/webhooks/query.sql
  - internal/webhooks/testevent.go
  - internal/api/alertgroups.go
  - internal/api/alertgroups_test.go
  - internal/fakes/fakemattermost/fakemattermost.go
  - web/src/test/fixtures/alert-groups.ts
  - design/prd/L2.md
  - design/prd/l2/C-29-compact-message-layout.md
  - design/prd/l2/C-32-escalation-chains-and-assignees.md
  - design/adr/0018-on-call-model-teams-schedules-and-escalation-chains.md
  - test/e2e/assignees_test.go
  - docs/concepts/escalation-chains.md
  - docs/outgoing-webhooks/events.md
  - docs/install/versions.md
  - internal/delivery/enqueue.go
  - internal/delivery/enqueue_test.go
  - internal/groups/events_test.go
  - web/src/components/timeline.tsx
acceptance:
  - "[C-32.FR-12, C-32.AC-6] A Page by Mention whose Thread reply is delivered makes the person an Assignee of its chain, shown with the chain's owning Team; for each chain, the Assignees are the people that its latest step Paging people by name reached — a later step that reaches someone replaces them, the later Mentions of one step's rules add to them, and a step that reached none of its people leaves them as they were; a `page_team` step changes nothing; a person whose Page was not delivered never becomes an Assignee (`assignees_test.go`)."
  - "[C-32.FR-12, C-32.AC-2, C-10.FR-1, C-09.FR-22, C-32.AC-11] Each change of the Assignees records the Quiet `assignees_changed` (Timeline kind `escalation`) with the Assignees after it; Acknowledge, Snooze and Resolve clear them with `assignees_changed`; they change only through system transitions of the dispatcher, and no operation sets them (`assignees_test.go`, `events_test.go`)."
  - "[C-29.FR-1, C-32.AC-13, C-12.FR-12, C-11.FR-20, C-12.FR-3] Before an acknowledgement, item 7 of the Root message is \"Assignees: @ivan (devops), @petr (payments)\", each a Mention where the person has an Account link in the Destination's Identity space and their plain name otherwise; `assignees_changed` edits the Root messages Quietly; a new Quiet message carries them as Mentions; after an Acknowledge that stopped running chains, item 7 reads \"Acknowledged by @ivan · Escalation chains stopped\", the Owner without a Team (P-89), and without chains as in L1 (`default_test.go`)."
  - "[C-32.FR-12, C-32.AC-13] The `layout` sent to a bridge names each Assignee in `assignees` with the `ref` of their entry in the envelope's `mentions` where the message carries their Mention — a new message to a bridge with `native_mentions` — and `null` otherwise, each Assignee with the owning Team of their chain in the optional `team` of `LayoutPerson` (S-126); the Owner has no `team`, and \"Escalation chains stopped\" is a line of `notices` (`layout_test.go`, `envelope_test.go`)."
  - "[C-32.FR-12, C-32.AC-10] The Assignees of a chain that has run out stay: after every chain of an Alert Group has run out and it records `unclaimed`, its Assignees and `assignees_changed` history are unchanged, `listAlertGroups?assignee=me` still returns it for each of them, and only Acknowledge, Snooze or Resolve clears them (`assignees_test.go`, `list_integration_test.go`)."
  - "[C-32.FR-12] The Owner is named without a Team (P-89): item 7, `layout` and the outgoing webhook data name only the Assignees with Teams (`default_test.go`, `layout_test.go`)."
  - "[C-32.FR-18, C-32.AC-10, C-09.FR-1, C-09.FR-13] `getAlertGroup` and `listAlertGroups` read `assignees` ({`user`, `team`, `chain`, `since`}[]); `listAlertGroups` takes `assignee` — a User's `public_id` or `me`, `422` for a Service account token — and returns the open Alert Groups where that User is an Assignee, which leave it once acknowledged (`list_integration_test.go`)."
  - "[C-15.FR-2] The `alert_group` of every outgoing webhook event carries `assignees`, each User with the owning Team of their chain; events mode receives `assignees_changed` (`body_test.go`)."
verify: "make ci test-integration e2e-go docs-check"
operator_attention: true
issue: 457
---

# S-168. Assignees — the people the chains Paged last — in the Root message and the layout, and the Assignee filter (BE)

## Scope

**IN**

- Assignees per chain started on the Alert Group from the outcomes of Pages by Mention, `assignees_changed`, and their
  clearing by Acknowledge, Snooze and Resolve, only through the dispatcher (C-32.FR-12; C-10.FR-1 revised).
- Item 7 of the Root message before and after an acknowledgement — "Assignees: …" with their Teams, "Acknowledged by
  {user} · Escalation chains stopped" — as Mentions where linked, and the people of `layout` (C-29.FR-1,
  C-29.FR-8; C-12.FR-12 revised).
- The Owner named without a Team (P-89, decided by the maintainer with this story).
- The Assignees on the Alert Group's read and list, the filter Assignee, and in outgoing webhook data (C-32.FR-18;
  C-09.FR-1, FR-13, C-15.FR-2 revised).

**OUT**

- Assignees made by calls that the gateway accepted: S-157.
- The Alert Group page, the list's column and filter, and "I am an Assignee" on the Home page: S-159.

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated):
  - `AlertGroup` gains the required `assignees` (`Assignee`[] {`user` (`UserRef`), `team` (`TeamRef` or null — the
    owning Team of the chain), `chain` (`EscalationChainRef` or null once deleted), `since`}).
  - `listAlertGroups` and `getAlertGroupCounts`, which takes the list's filters, gain the parameter `AgAssignee`
    (`assignee`: a User's `public_id` or `me`; `422` `unsupported` at `/query/assignee` for `me` from a Service
    account token, `422` `unknown_id` for an id that names no User).
  - `LifecycleEvent` gains `assignees_changed` (`x-event-kinds` `escalation`), `WebhookEventName` too;
    `TimelineEscalationEntry` gains `assignees` (`Assignee`[] after the change, only on `assignees_changed`).
  - `WebhookAlertGroup` gains the required `assignees` (`WebhookAssignee`[] {`user` (`WebhookUser`), `team`
    (`WebhookTeam` or null)}).
  - `MessageLayout` and the Bridge protocol's `layout` (`api/bridge.yaml`) describe the Owner without a Team and
    "Escalation chains stopped" as a line of `notices` (descriptions only).
- **Schema** (migration `0050_assignees`, expand only; `schema.md` §4.9): `alert_group_assignees`: `org_id`,
  `alert_group_id` (FK, cascade), `escalation_run_id` (FK, cascade), `step_number`, `run_step_seq` (the
  `escalation_run_steps.seq` of the step that made the chain's Assignees, which orders the steps of a run, repeated
  ones and those of one firing included; null only for a Page of an earlier binary), `user_id`, `team_id` (FK, set
  null), `since`; `run_step_seq` on `pages` (the step of the first Mentions) and on `notification_rule_runs` (the step
  that started the rule run, which its later Mentions belong to), nullable; primary key `(alert_group_id, escalation_run_id, user_id)`; index
  `(org_id, user_id, alert_group_id)` for the filter; written only by `groups`, so it joins the tables of lint 2
  (`internal/archlint/archlint.go` and the row of lint 2 in AGENTS.md), as S-118 did for `alert_group_teams`; and
  `timeline_entries.assignees` (jsonb, the Assignees after an `assignees_changed` by internal ids). The down migration
  drops both.
- **Packages**: as S-169 and S-155 state them — the Assignees are Alert Group state that only `groups` writes; delivery
  (and from S-157 `gateways`) report a Page's outcome to it as Commands, and `groups` imports neither.
- **Groups** (`internal/groups/assignees.go`, `pages.go`, `dispatcher.go`, `events.go`, `read.go`, `list.go`,
  `counts.go`, `filters.go`): the system transition `PageReached` of S-155 — `SettlePage`, which delivery calls with
  the people a delivered reply mentioned — updates the Assignees of the run by the rule of C-32.FR-12 and records
  `assignees_changed` (Quiet, no Mentions, kind `escalation`, a Root message update) when they change; the change that
  makes an Alert Group stop being firing — Acknowledge, Snooze, Resolve — deletes them in its transaction with the
  same event; the filter `assignee` on the list and its counts.
- **Escalation and Notification rules** (`internal/escalation/engine.go`, `pages.go`, `runs.go`,
  `internal/notificationrules/runs.go`, `query.sql`): the seq of the run step a step is recorded with goes on
  `groups.StepPage` (`RunStepSeq`) and `notificationrules.Start`, into `pages.run_step_seq` and
  `notification_rule_runs.run_step_seq`.
- **Delivery**: the outcome of a Page dispatches `PageReached` for each person a delivered reply mentioned, as S-155
  already does (`internal/delivery/pages.go` unchanged); the Desired state carries the Assignees through the render's
  read of the Alert Group (`internal/messages/render.go`, `internal/mentions`), so `assignees_changed` edits the Root
  messages, and a new message names them as Mentions where linked (`internal/delivery/render.go` unchanged).
- **Mentions** (`internal/mentions`): `AssigneeUsers`, the Account links of the Assignees by identity space, as
  `FooterUsers` reads the footer's user.
- **Messages** (`internal/messages`): `Message` gains `Assignees` and `ChainsStopped` (CommonMark only, as `Owner`) and
  `FooterPeople`, the footer's several people per identity space, whose tokens `WithFooterIn` and `FooterText` write as
  each adapter's Mentions, so the adapters stay as they are; item 7 of the default layout (C-29.FR-1); `layout.go`
  fills `assignees` of `layout` (S-126's shape) with each Assignee's `team`, the Owner without one; the texts
  "Assignees: {people}" and "Escalation chains stopped" in English and Russian (C-12.FR-3 revised); "{person} ({team})"
  is no text, as it reads the same in both languages.
- **Bridge** (`internal/bridge/envelope.go`, `delivery.go`): the `ref` of each Assignee's Mention entry in `layout`, as
  S-126 does for the Owner.
- **Event tables and SPA** (`internal/delivery/enqueue.go`, `internal/groups/events_test.go`,
  `web/src/components/timeline.tsx`): the row of `assignees_changed` (a Root message update) in `delivery.Table`,
  counted by `enqueue_test.go`; the table-driven test of the closed table covers it; a case for it in `entryText`
  with S-148's generic text.
- **Webhooks** (`internal/webhooks/body.go`, `testevent.go`): `assignees` in `alert_group`, `[]` in a test event;
  `assignees_changed` in events mode.
- **Web test fixtures** (`web/src/test/fixtures/alert-groups.ts`, S-166): `assignees` `[]`.
- **Fakes** (`internal/fakes/fakemattermost`): the person `u-petr` (`petr`), a second On-call to link.
- **Shared tables**: L2.md §5.3 moves P-89 to "Decided since they were first listed", as the Owner named without a
  Team.
- **Log events, metrics**: none new.
- **Documentation**: Concepts → Escalation chains gains "Assignees": who they are, how they change, that any Responder
  may acknowledge, and that the Owner is named without a Team; Outgoing webhooks → Events names `assignees` and
  `assignees_changed`.

## Steps

1. Write the migration and `schema.md`. Check: `make test-integration` runs up, down and up.
2. Write the Assignees and their transitions. Check: `assignees_test.go` covers two chains, a
   later step replacing, a rule's later Mention adding, a step reaching nobody, `page_team`, and the clearing by each
   Command.
3. Render item 7 and `layout`; carry them in the Desired state. Check: `default_test.go`, `layout_test.go`,
   `envelope_test.go`.
4. Add the reads, the filter, the webhook data and the fixture; add the spec. Check: `make generate-check`;
   `list_integration_test.go`, `body_test.go`.
5. Record P-89 as decided, write the documentation and `test/e2e/assignees_test.go`. Check: the Verification below; `make
   docs-check`.

## Verification

```sh
make dev > dev.log 2>&1 &
# As in S-155, with petr now linked to u-petr of the fake Mattermost server; ivan in devops, petr in payments; A the
# Admin's token and I ivan's; NOTIFY, PUTA and AG for the Integration "sev" as in S-118.
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake; FMM=127.0.0.1:18065/_fake
ROOT() { curl -s $FMM/posts | jq -r --arg c "$1" '[.[] | select(.channel_id == $c and .root_id == "")] | last | .props.attachments[0].text'; }
curl -s -X PUT $FAM/groups/a1 -d '{"receiver":"sev","route":"{}","labels":{"alertname":"KubernetesContainerOOMKilled"}}' > /dev/null
PUTA a1 a '{"labels":{"category":"infra","system":"k8s","team":"payments","severity":"warning"}}'; NOTIFY a1 '{"reason":"first notification"}'
G=$(AG 'team%3D%22payments%22'); sleep 2
curl -s "${A[@]}" $API/alert-groups/$G | jq -c '[.assignees[] | {u: .user.login, t: .team.slug}] | sort_by(.u)'
# [{"u":"ivan","t":"devops"},{"u":"petr","t":"payments"}]
ROOT ch-infra-notice | grep -o 'Assignees: .*'                                  # Assignees: @ivan (devops), @petr (payments)
curl -s "${I[@]}" "$API/alert-groups?assignee=me" | jq -r '[.items[].id] | index("'$G'") != null'   # true
curl -s "${I[@]}" -X POST $API/alert-groups/$G/acknowledge > /dev/null; sleep 1
curl -s "${A[@]}" $API/alert-groups/$G | jq -c '{a: .assignees}'                                     # {"a":[]}
ROOT ch-payments-notice | grep -o 'Acknowledged by .*'                          # Acknowledged by @ivan · Escalation chains stopped
curl -s "${A[@]}" "$API/alert-groups/$G/timeline?kind=escalation" | jq '[.items[] | select(.event == "assignees_changed")] | length'   # 2 or more
curl -s "${I[@]}" "$API/alert-groups?assignee=me" | jq -r '[.items[].id] | index("'$G'") == null'   # true
```

`test/e2e/assignees_test.go` repeats these checks and adds a person Paged without an Account link who never becomes an
Assignee, a later step replacing the Assignees of its chain, a Snooze and a Resolve clearing them, the Assignees kept
once every chain ran out and the Alert Group is Unclaimed, the Bridge `layout` with the `ref` of each linked Assignee
and their Teams, `422` for `assignee=me` from a Service account token, the Owner named without a Team, and the webhook
data.

## Open questions

1. **The Assignees of a chain that ran out.** Settled by the maintainer, with CONTEXT.md, ADR-0018 and C-32.FR-12
   amended: a chain that ran out keeps its Assignees until Acknowledge, Snooze or Resolve, so an Unclaimed Alert Group
   still shows whom it Paged and "I am an Assignee" still lists it.

## Notes

- Suggested commit: `feat(groups): track the assignees that escalation chains page and show them in messages`.
- From S-126, `LayoutOf` in `internal/messages/layout.go` names the Owner in `owner` while the Alert Group is
  acknowledged and returns early without the footer "Acknowledged by {user}" among `notices`, because that footer was
  only its person. The suffix "· Escalation chains stopped" that this story adds to item 7 is not a person
  (C-29.FR-8: `notices` holds "the footer of item 7 other than its people"): it must become a line of `notices`, and
  the early return must not swallow it; `layout_test.go` covers both. The Owner has no `team`.
- The maintainer decided a provisional value with this story (operator attention): P-89, the Owner is named without
  any Team — no "Owner's Teams" are computed, read or sent, and C-32.FR-12, C-29.FR-1, ADR-0018 and L2.md §5.3 say so.
- "Assigned" is never used: the people are Assignees (CONTEXT.md).
- The migration is `0050`, as the maintainer numbered it: 0001–0049 were merged before this story (S-167's Notes).
- The rule of C-32.FR-12 by run step: escalation passes the seq a step is recorded with (`escalation_run_steps.seq`,
  one more than the run's steps before it) to the Page of its first Mentions and to the rule runs it starts; a later
  Mention belongs to its rule run's step (`LockPage`). A reach by a greater seq replaces the chain's Assignees; by the
  same seq — another reply, or a later Mention of that step's rules — adds; by a smaller seq — a late reply, or a later
  Mention of the rule of a step a later step replaced — changes nothing. Neither `step_number`, which a repeat makes
  again, nor time, which one firing shares for all its steps, can order them. A Page or a rule run of an earlier
  binary has no seq: its reach adds only to a run without Assignees or whose Assignees have none, and a reach with a
  seq replaces Assignees without one.
- `assignees_changed` that Acknowledge, Snooze or Resolve records belongs to their change, so its actor is theirs; the
  one a Page's outcome records is Muster's.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-32.FR-12 | partial | Assignees by Mention and their messages, the Owner without a Team; by calls is S-157 |
| C-32.FR-18 | partial | the Assignees and the filter in the API; the pages are S-159 |
| C-32.AC-2 | full | |
| C-32.AC-6 | partial | the Assignee once the reply is delivered; S-155 the rest |
| C-32.AC-10 | partial | the filter `me`; the Home page is S-159 |
| C-32.AC-11 | partial | `assignees_changed` |
| C-32.AC-13 | partial | the Assignees in the Root message and `layout` |
| C-29.FR-1 | partial | item 7 with the Assignees and "· Escalation chains stopped"; the rest is S-125 and S-126 |
| C-09.FR-1 | partial | revised for L2: the Assignees of an Alert Group |
| C-09.FR-13 | partial | revised for L2: the Assignee filter in the API |
| C-09.FR-22 | partial | revised for L2: `assignees_changed` |
| C-10.FR-1 | partial | revised for L2: Acknowledge, Snooze and Resolve clear the Assignees |
| C-11.FR-20 | partial | revised for L2: `assignees_changed` as a Root message update |
| C-12.FR-3 | partial | revised for L2: the texts of the Assignees |
| C-12.FR-12 | partial | revised for L2: the Assignees named as Mentions like the footer's people |
| C-15.FR-2 | partial | revised for L2: the Assignees in `alert_group` |
