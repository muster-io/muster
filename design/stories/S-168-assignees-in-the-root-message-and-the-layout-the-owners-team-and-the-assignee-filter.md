---
id: S-168
title: Assignees — the people the chains Paged last — in the Root message and the layout, the Owner's Team, and the Assignee filter (BE)
capability: C-32
kind: be
layer: L2
depends_on: [S-126, S-155]
covers: [C-32.FR-12, C-32.FR-18, C-32.AC-2, C-32.AC-6, C-32.AC-10, C-32.AC-11, C-32.AC-13, C-29.FR-1, C-09.FR-1, C-09.FR-13, C-09.FR-22, C-10.FR-1, C-11.FR-20, C-12.FR-3, C-12.FR-12, C-15.FR-2]
files_touched:
  - api/openapi.yaml
  - internal/db/migrations/0043_assignees.up.sql
  - internal/db/migrations/0043_assignees.down.sql
  - design/db/schema.md
  - internal/db/db_test.go
  - internal/db/migrate_test.go
  - internal/groups/assignees.go
  - internal/groups/assignees_test.go
  - internal/groups/dispatcher.go
  - internal/groups/events.go
  - internal/groups/read.go
  - internal/groups/list.go
  - internal/groups/filters.go
  - internal/groups/list_integration_test.go
  - internal/groups/query.sql
  - internal/archlint/archlint.go
  - AGENTS.md
  - internal/delivery/pages.go
  - internal/delivery/render.go
  - internal/messages/message.go
  - internal/messages/default.go
  - internal/messages/default_test.go
  - internal/messages/layout.go
  - internal/messages/layout_test.go
  - internal/messages/texts/en.json
  - internal/messages/texts/ru.json
  - internal/bridge/envelope.go
  - internal/bridge/envelope_test.go
  - internal/webhooks/body.go
  - internal/webhooks/body_test.go
  - internal/api/alertgroups.go
  - web/src/test/fixtures/alert-groups.ts
  - design/prd/L2.md
  - test/e2e/assignees_test.go
  - docs/concepts/escalation-chains.md
acceptance:
  - "[C-32.FR-12, C-32.AC-6] A Page by Mention whose Thread reply is delivered makes the person an Assignee of its chain, shown with the chain's owning Team; for each chain, the Assignees are the people that its latest step Paging people by name reached — a later step that reaches someone replaces them, the later Mentions of one step's rules add to them, and a step that reached none of its people leaves them as they were; a `page_team` step changes nothing; a person whose Page was not delivered never becomes an Assignee (`assignees_test.go`)."
  - "[C-32.FR-12, C-32.AC-2, C-10.FR-1, C-09.FR-22, C-32.AC-11] Each change of the Assignees records the Quiet `assignees_changed` (Timeline kind `escalation`) with the Assignees after it; Acknowledge, Snooze and Resolve clear them with `assignees_changed`; they change only through system transitions of the dispatcher, and no operation sets them (`assignees_test.go`, `events_test.go`)."
  - "[C-29.FR-1, C-32.AC-13, C-12.FR-12, C-11.FR-20, C-12.FR-3] Before an acknowledgement, item 7 of the Root message is \"Assignees: @ivan (devops), @petr (payments)\", each a Mention where the person has an Account link in the Destination's Identity space and their plain name otherwise; `assignees_changed` edits the Root messages Quietly; a new Quiet message carries them as Mentions; after an Acknowledge that stopped running chains, item 7 reads \"Acknowledged by @ivan (devops) · Escalation chains stopped\", and without chains as in L1, with the Owner's Team (`default_test.go`)."
  - "[C-32.FR-12, C-32.AC-13] The `layout` sent to a bridge names each Assignee in `assignees` with the `ref` of their entry in the envelope's `mentions` where the message carries their Mention — a new message to a bridge with `native_mentions` — and `null` otherwise, each Assignee and the Owner with their Team in the optional `team` of `LayoutPerson` (S-126) (`layout_test.go`, `envelope_test.go`)."
  - "[C-32.FR-12] The Owner is named with the Teams they are in among the Teams of the Alert Group, its Route's owning Team and its chains' owning Teams, and with none when there is no such Team (P-89) (`default_test.go`, `assignees_test.go`)."
  - "[C-32.FR-18, C-32.AC-10, C-09.FR-1, C-09.FR-13] `getAlertGroup` and `listAlertGroups` read `assignees` ({`user`, `team`, `chain`, `since`}[]) and `owner_teams`; `listAlertGroups` takes `assignee` — a User's `public_id` or `me`, `422` for a Service account token — and returns the open Alert Groups where that User is an Assignee, which leave it once acknowledged (`list_integration_test.go`)."
  - "[C-15.FR-2] The `alert_group` of every outgoing webhook event carries `assignees` and the Owner's `teams` (`body_test.go`)."
verify: "make ci test-integration e2e-go docs-check"
operator_attention: true
issue: null
---

# S-168. Assignees — the people the chains Paged last — in the Root message and the layout, the Owner's Team, and the Assignee filter (BE)

## Scope

**IN**

- Assignees per running chain from the outcomes of Pages by Mention, `assignees_changed`, and their clearing by
  Acknowledge, Snooze and Resolve, only through the dispatcher (C-32.FR-12; C-10.FR-1 revised).
- Item 7 of the Root message before and after an acknowledgement — "Assignees: …" with their Teams, "Acknowledged by
  {user} ({Team}) · Escalation chains stopped" — as Mentions where linked, and the people of `layout` (C-29.FR-1,
  C-29.FR-8; C-12.FR-12 revised).
- The Owner's Team (P-89).
- The Assignees on the Alert Group's read and list, the filter Assignee, and in outgoing webhook data (C-32.FR-18;
  C-09.FR-1, FR-13, C-15.FR-2 revised).

**OUT**

- Assignees made by calls that the gateway accepted: S-157.
- The Alert Group page, the list's column and filter, and "I am an Assignee" on the Home page: S-159.

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated):
  - `AlertGroup` gains the required `assignees` (`Assignee`[] {`user` (`UserRef`), `team` (`TeamRef` or null — the
    owning Team of the chain), `chain` (`EscalationChainRef`), `since`}) and `owner_teams` (`TeamRef`[]).
  - `listAlertGroups` gains the parameter `AgAssignee` (`assignee`: a User's `public_id` or `me`).
  - `LifecycleEvent` gains `assignees_changed` (`x-event-kinds` `escalation`), `WebhookEventName` too;
    `TimelineEscalationEntry` gains `assignees` (`Assignee`[] after the change).
  - `WebhookAlertGroup` gains `assignees` ({`user` (`WebhookUser`), `team`}[]) and `owner_teams`.
- **Schema** (migration `0043_assignees`, expand only; `schema.md` §4.9): `alert_group_assignees`: `org_id`,
  `alert_group_id` (FK, cascade), `escalation_run_id` (FK, cascade), `step_number`, `user_id`, `team_id`, `since`;
  primary key `(alert_group_id, escalation_run_id, user_id)`; index `(org_id, user_id, alert_group_id)` for the filter;
  written only by `groups`, so it joins the tables of lint 2 (`internal/archlint/archlint.go` and the row of lint 2 in
  AGENTS.md), as S-118 did for `alert_group_teams`. The down migration drops it.
- **Packages**: as S-169 and S-155 state them — the Assignees are Alert Group state that only `groups` writes; delivery
  (and from S-157 `gateways`) report a Page's outcome to it as Commands, and `groups` imports neither.
- **Groups** (`internal/groups/assignees.go`, `dispatcher.go`, `events.go`, `read.go`, `list.go`, `filters.go`): the
  system transition `PageReached` of S-155 — which delivery dispatches when a Page reaches a person — updates the
  Assignees of the run by the rule of C-32.FR-12 and records `assignees_changed` (Quiet, no Mentions, kind `escalation`,
  a Root message update) when they change; Acknowledge, Snooze and Resolve delete them in their transaction with the
  same event; the Owner's Teams of P-89; the filter `assignee`.
- **Delivery** (`internal/delivery/pages.go`, `render.go`): the outcome of a Page dispatches `PageReached` for each
  person a delivered reply mentioned, as S-155 does; the Desired state carries the Assignees and the Owner's Teams, so
  `assignees_changed` edits the Root messages, and a new message names them as Mentions where linked.
- **Messages** (`internal/messages`): `Message` gains `Assignees` and `OwnerTeams`; item 7 of the default layout
  (C-29.FR-1); `layout.go` fills `assignees` and `owner` of `layout` (S-126's shape), with each person's `team`; the texts "Assignees: {people}",
  "{person} ({team})", "· Escalation chains stopped" in English and Russian (C-12.FR-3 revised).
- **Bridge** (`internal/bridge/envelope.go`): the `ref` of each Assignee's Mention entry in `layout`, as S-126 does for
  the Owner.
- **Webhooks** (`internal/webhooks/body.go`): `assignees` and `owner_teams` in `alert_group`; `assignees_changed` in
  events mode.
- **Web test fixtures** (`web/src/test/fixtures/alert-groups.ts`, S-166): `assignees` `[]` and `owner_teams` `[]`.
- **Shared tables**: L2.md §5.3 moves P-89 to "Decided since they were first listed".
- **Log events, metrics**: none new.
- **Documentation**: Concepts → Escalation chains gains "Assignees": who they are, how they change, that any Responder
  may acknowledge, and the Owner's Team.

## Steps

1. Write the migration and `schema.md`. Check: `make test-integration` runs up, down and up.
2. Write the Assignees and their transitions, and the Owner's Teams. Check: `assignees_test.go` covers two chains, a
   later step replacing, a rule's later Mention adding, a step reaching nobody, `page_team`, and the clearing by each
   Command.
3. Render item 7 and `layout`; carry them in the Desired state. Check: `default_test.go`, `layout_test.go`,
   `envelope_test.go`.
4. Add the reads, the filter, the webhook data and the fixture; add the spec. Check: `make generate-check`;
   `list_integration_test.go`, `body_test.go`.
5. Confirm P-89, write the documentation and `test/e2e/assignees_test.go`. Check: the Verification below; `make
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
curl -s "${A[@]}" $API/alert-groups/$G | jq -c '{a: .assignees, t: [.owner_teams[].slug]}'          # {"a":[],"t":["devops"]}
ROOT ch-payments-notice | grep -o 'Acknowledged by .*'                          # Acknowledged by @ivan (devops) · Escalation chains stopped
curl -s "${A[@]}" "$API/alert-groups/$G/timeline?kind=escalation" | jq '[.items[] | select(.event == "assignees_changed")] | length'   # 2 or more
curl -s "${I[@]}" "$API/alert-groups?assignee=me" | jq -r '[.items[].id] | index("'$G'") == null'   # true
```

`test/e2e/assignees_test.go` repeats these checks and adds a person Paged without an Account link who never becomes an
Assignee, a later step replacing the Assignees of its chain, a Snooze and a Resolve clearing them, the Bridge `layout`
with the `ref` of each Assignee, an Owner in none of the Alert Group's Teams named without a Team, and the webhook data.

## Open questions

1. **The Assignees of a chain that ran out.** C-32.FR-12 speaks of "each running chain", while CONTEXT.md says only
   Acknowledge, Snooze and Resolve clear the Assignees. Options: (a) a chain that ran out keeps its Assignees until one
   of them, so an Unclaimed Alert Group still shows whom it Paged and "I am an Assignee" still lists it; (b) its
   Assignees go when it runs out. Recommended: (a); the contract follows it.

## Notes

- Suggested commit: `feat(groups): track the assignees that escalation chains page and show them in messages`.
- The maintainer confirms a provisional value with this story (operator attention): P-89, the Owner is named with the
  Teams they are in among the Teams of the Alert Group, its Route's and its chains' owning Teams.
- "Assigned" is never used: the people are Assignees (CONTEXT.md).
- Migration `0043` follows the story map; a story merged out of that order renumbers its migration (S-167's Notes).

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-32.FR-12 | partial | Assignees by Mention, their messages and the Owner's Team; by calls is S-157 |
| C-32.FR-18 | partial | the Assignees and the filter in the API; the pages are S-159 |
| C-32.AC-2 | full | |
| C-32.AC-6 | partial | the Assignee once the reply is delivered; S-155 the rest |
| C-32.AC-10 | partial | the filter `me`; the Home page is S-159 |
| C-32.AC-11 | partial | `assignees_changed` |
| C-32.AC-13 | partial | the Assignees in the Root message and `layout` |
| C-29.FR-1 | partial | item 7 with the Assignees and the Owner's Team; the rest is S-125 and S-126 |
| C-09.FR-1 | partial | revised for L2: the Assignees of an Alert Group |
| C-09.FR-13 | partial | revised for L2: the Assignee filter in the API |
| C-09.FR-22 | partial | revised for L2: `assignees_changed` |
| C-10.FR-1 | partial | revised for L2: Acknowledge, Snooze and Resolve clear the Assignees |
| C-11.FR-20 | partial | revised for L2: `assignees_changed` as a Root message update |
| C-12.FR-3 | partial | revised for L2: the texts of the Assignees |
| C-12.FR-12 | partial | revised for L2: the Assignees named as Mentions like the footer's people |
| C-15.FR-2 | partial | revised for L2: the Assignees in `alert_group` |
