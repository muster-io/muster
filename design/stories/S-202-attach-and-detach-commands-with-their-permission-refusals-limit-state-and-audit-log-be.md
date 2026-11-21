---
id: S-202
title: The Commands Attach and Detach with their Permission, refusals, limit, state and Audit log (BE)
capability: C-37
kind: be
layer: L2
depends_on: [S-198, S-169, S-168, S-174, S-173, S-197]
covers: [C-37.FR-1, C-37.FR-2, C-37.FR-3, C-37.FR-4, C-37.FR-8, C-37.FR-15, C-37.FR-16, C-37.AC-1, C-37.AC-4, C-37.AC-6, C-37.AC-11]
files_touched:
  - api/openapi.yaml
  - internal/db/migrations/00NN_alert_group_attachments.up.sql
  - internal/db/migrations/00NN_alert_group_attachments.down.sql
  - design/db/schema.md
  - internal/db/migrate_test.go
  - internal/auth/auth_integration_test.go
  - internal/groups/attach.go
  - internal/groups/attach_test.go
  - internal/groups/commands.go
  - internal/groups/commands_test.go
  - internal/groups/allowed.go
  - internal/groups/allowed_test.go
  - internal/groups/events.go
  - internal/groups/events_test.go
  - internal/groups/loudness.go
  - internal/groups/loudness_test.go
  - internal/groups/dispatcher.go
  - internal/groups/read.go
  - internal/groups/query.sql
  - internal/groups/groups_integration_test.go
  - internal/api/commands.go
  - internal/api/commands_test.go
  - internal/api/alertgroups.go
  - internal/api/alertgroups_test.go
  - internal/api/problem.go
  - internal/delivery/enqueue.go
  - internal/delivery/enqueue_test.go
  - internal/metrics/catalogue.go
  - internal/metrics/metrics_test.go
  - internal/logging/events.go
  - web/src/test/fixtures/alert-groups.ts
  - web/src/lib/commands.ts
  - web/src/components/timeline.tsx
  - web/src/components/timeline.test.tsx
  - web/src/locales/en.json
  - web/src/locales/ru.json
  - design/prd/l1/reference.md
  - test/e2e/attach_detach_test.go
acceptance:
  - "[C-37.FR-1, C-37.FR-15, C-37.AC-1] On the setup of scenario 1, attaching `#1288` to `#1287` with `attachAlertGroup` records `attached` on `#1288` and `alert_group_attached` on `#1287`, each Quiet without Mentions with the person and the Transport, one Audit log entry `alert_group.attached` naming both Alert Groups, the actor and the Transport, and `detachAlertGroup` records `detached` and `alert_group_detached` and the entry `alert_group.detached` (`attach_test.go`, `groups_integration_test.go`, `test/e2e/attach_detach_test.go`)."
  - "[C-37.FR-1, C-37.AC-4] The Permission `alert-groups:attach` is held by Admins and Responders and not by Viewers, a Service account may attach, and a Viewer gets `403` on both operations; `allowed_commands` lists `attach` and `detach` where they are allowed, and `muster_commands_total{command}` counts `attach` and `detach` (`allowed_test.go`, `commands_test.go`, `metrics_test.go`, `auth_integration_test.go`)."
  - "[C-37.FR-2, C-37.AC-4] Each refusal of FR-2 returns `409` with its code — `attach_to_itself`, `already_resolved`, `already_attached`, `has_attached_alert_groups`, `main_resolved`, `main_is_attached` (naming its main Alert Group) and `attach_limit_reached` — a request that fails two checks gets the first in that order, a main Alert Group that does not exist in the Organization gets `404`, and Detach of an Alert Group that is not attached gets `409` `not_attached` (`attach_test.go`, `commands_test.go`, `test/e2e/attach_detach_test.go`)."
  - "[C-37.FR-2] An Alert Group may be attached to one of any Route and Integration of the Organization, and the attachment is one level deep with no cycle: `#1291` attached to `#1288`, which is attached to `#1287`, is refused with `main_is_attached` naming `#1287` (`attach_test.go`, `test/e2e/attach_detach_test.go`)."
  - "[C-37.FR-3] Attach and Detach lock the main Alert Group first and then the Alert Group, and two Attaches that cross — `#A` to `#B` and `#B` to `#A` — end with exactly one succeeding, never both (`groups_integration_test.go`)."
  - "[C-37.FR-4] Attach makes the Alert Group take the main Alert Group's status with its Owner and Snooze end, even when that ends its own acknowledgement or Snooze; taking an acknowledged, snoozed or resolved status clears its Assignees and ends Unclaimed, while one that stays firing keeps them as they are (`attach_test.go`, `groups_integration_test.go`)."
  - "[C-37.FR-8, C-37.AC-6] Detach keeps the status, Owner and Snooze end the Alert Group followed last and its own Alerts, so detaching `#1290` while `#1287` is acknowledged by Ivan keeps `#1290` acknowledged by Ivan (`attach_test.go`, `groups_integration_test.go`)."
  - "[C-37.FR-16, C-37.AC-11] With `alert_group.attach_max` reached, the next Attach gets `attach_limit_reached`; the setting is read at the moment of the Attach (`attach_test.go`, `test/e2e/attach_detach_test.go`)."
verify: "make ci test-integration e2e-go"
operator_attention: false
issue: null
---

# S-202. The Commands Attach and Detach with their Permission, refusals, limit, state and Audit log (BE)

## Scope

**IN**

- The state of an attachment — which Alert Group is attached to which main Alert Group — and the Commands that change it:
  Attach and Detach in the dispatcher with the Permission `alert-groups:attach`, the ordered refusals of C-37.FR-2, the
  limit `alert_group.attach_max`, the lock order of the pair, the Audit log types and the Transport rules
  (C-37.FR-1, FR-2, FR-3, FR-16).
- What Attach does to the Alert Group's own status — it takes the main Alert Group's status with its Owner and Snooze end
  — and what Detach returns to (C-37.FR-4, FR-8).
- The five lifecycle events of C-37.FR-15 as rows of the closed table, and the recording of `attached`,
  `alert_group_attached`, `detached` and `alert_group_detached` for a person's Command.
- `allowed_commands`, `attach` and `detach` in `muster_commands_total{command}`, and `attached_to` and the count of
  attached Alert Groups in the read of an Alert Group.

**OUT**

- Following the main Alert Group's later changes, the refusal of other Commands on an attached Alert Group, the end of an
  attachment when either Alert Group resolves, and the Quiet rule for every event on an attached Alert Group: S-203.
- Stopping and starting over of Escalation chains, Notification rules, calls, the ack timeout, Reminders and the end of a
  Snooze: S-204. Until it merges, the timers of an attached Alert Group still run, which the Notes name.
- The line and the Thread replies in messengers and bridges: S-205. Until it merges the rows of FR-15 are known to
  delivery and show nothing.
- The list filter, `attached_to` as a filter, the bulk Attach, the statistics and the outgoing webhook events:
  S-206. The pages: S-207. The documentation: S-208.
- The MCP server: it does not exist yet; its tools are written with it.

## Contracts

- **Spec** (`api/openapi.yaml`, changed first, then generated): `attachAlertGroup` (`POST
  /alert-groups/{alert_group_id}/attach`, body `AttachRequest` {`main_alert_group_id`}, `x-permission:
  alert-groups:attach`) and `detachAlertGroup` (`POST /alert-groups/{alert_group_id}/detach`), both answering the
  `AlertGroup` as the other Commands do; `AlertGroup` gains the required `attached_to` (an `AlertGroupRef` {`id`,
  `number`, `title`}, or null) and `attached_count`; `CommandName` and `allowed_commands` gain `attach` and `detach`;
  `Permission` gains `alert-groups:attach`; `LifecycleEvent` gains the five events of FR-15 (`x-event-kinds`: `attached`,
  `status_followed`, `detached` are `status`; `alert_group_attached`, `alert_group_detached` are `system`) and
  `WebhookEventName` gains them; the Timeline entries of the five carry the other Alert Group as `main_alert_group`
  (`AlertGroupRef`, null once the Alert Group is gone from view) and, for the system ones, `attached_alert_group`; the
  problem codes of the refusals join `internal/api/problem.go`, with `alert_group_attached` left to S-203. `HintEvent`
  `alert-group` already covers any change of an Alert Group.
- **Schema** (a new migration, expand only; the number is assigned by the orchestrator at dispatch; `schema.md` §4.9):
  `alert_groups.attached_to_id bigint REFERENCES alert_groups (id)` with `CHECK (attached_to_id <> id)`, an index
  `(org_id, attached_to_id)` for the attached Alert Groups of a main one, and `alert_groups.resolved_with_id bigint
  REFERENCES alert_groups (id)` — the main Alert Group that resolved this one with it (C-37.FR-6, FR-14), filled by
  S-203 and read by S-206, added here so that the one migration of the capability carries every column. Both
  columns are null for every existing Alert Group. The migration seeds the Permission `alert-groups:attach` for Admin and
  Responder as `0025` and `0046` seed theirs, and replaces `timeline_entries_event_vN_check`,
  `timeline_entries_event_kind_vN_check`, `timeline_entries_system_event_vN_check`, `timeline_entries_system_vN_check`,
  `webhook_events_event_vN_check` (the event names of `destinations.webhook_events` as S-118 and S-121 extend it) and
  `thread_replies_event_vN_check` with the five events added — each added `NOT VALID`, validated in a second
  transaction and the old one dropped in a third, as `0047` and `0049` do — with the next versions in the order the
  stories merge and every value present when it merges. The down migration deletes the entries and Thread replies of the
  new events first and drops the columns.
- **Package** (`internal/groups`, ADR-0016: one command layer): `attach.go` holds the transitions `attach` and `detach`
  — permission → precondition → transition → Audit log → Timeline → re-render, the same order as every Command — with
  `CommandAttach` and `CommandDetach` in `commands.go`, `PermissionAttach` (`alert-groups:attach`), `Service.Attach` and
  `Service.Detach`. The refusals are checked in the order of C-37.FR-2 on rows locked as FR-3 says: the main Alert Group
  first, then the Alert Group; a Detach reads the main Alert Group of an unlocked read, locks it, then the Alert Group,
  and checks again. The limit counts the Alert Groups with `attached_to_id` set to the main one. `allowed.go` offers
  `attach` where the status is not resolved and the Alert Group is not attached and has no attached Alert Groups, and
  `detach` where it is attached, to a caller with the Permission. The Audit log types `alert_group.attached` and
  `alert_group.detached` follow the other Commands' types.
- **What Attach does to the status** (C-37.FR-4): the Alert Group takes the main Alert Group's status, Owner and Snooze
  end in the same transition, through the same fields a Takeover or a Snooze sets, so that the checks of the table
  (`alert_groups_owner_check`, `alert_groups_snooze_check`) hold; it clears the Assignees when it takes a status other
  than firing, as C-32.FR-12 says, and ends Unclaimed (C-17.FR-3); an acknowledged status taken here sets the first
  acknowledgement, when the Alert Group has none, to the main Alert Group's Owner at that moment, as following does
  (C-37.FR-5, C-09.FR-27; see the Open question). `attached` records the Alert Group's own transition, `alert_group_attached` the main's.
- **Detach** (C-37.FR-8): clears `attached_to_id` and keeps everything else; the starting over of chains and timers is
  S-204's.
- **Rows** (`events.go`, the closed table, and its delivery twin in `delivery/enqueue.go` with `FormNothing` until S-205):
  `attached` (`status`, Quiet), `alert_group_attached` (`system`, Quiet), `status_followed` (`status`, Quiet),
  `detached` (`status`, Quiet), `alert_group_detached` (`system`, Quiet), none with Mentions; S-198's test that every
  row is governed or always Loud lists them among those that are never Loud.
- **Settings**: `alert_group.attach_max` (100, built in; `defaults.md` already lists it).
- **Metrics** (`internal/metrics/catalogue.go`): `muster_commands_total{command}` gains `attach` and `detach`; the
  reference pages regenerate, and `reference.md` moves `alert-groups:attach` from "Permissions added in L2" into the
  table of Roles and Permissions and adds the two values to the metrics catalogue row, which the code checks.
- **Log events**: `internal/logging/events.go` already logs every Command by its name; no new event.
- **Web fixture, command list and Timeline** (`web/src/test/fixtures/alert-groups.ts`, `web/src/lib/commands.ts`,
  `web/src/components/timeline.tsx` and the two locale files): the builder sets `attached_to` to null and
  `attached_count` to 0, the list of command names knows the two new ones, and `entryText` of the Timeline gains a case
  for each of the five events with a one-line text from the entry's fields — "Attached to #{main}", "Detached from
  #{main}", "#{main} acknowledged this Alert Group" and so on, with the Russian versions — as S-178 did for `paged`, so
  that the exhaustive switch over `LifecycleEvent` and the TypeScript check stay green; the dialog, the notice and the
  marks are S-207's.

## Steps

1. Write the migration and `schema.md`. Check: `make test-integration` runs up, down and up; `migrate_test.go` finds the
   columns, the CHECK versions and the Permission; `auth_integration_test.go` counts the new Permission.
2. Write the transitions, the refusals in order, the limit and the lock order. Check: `attach_test.go` with a table over
   every refusal and every pair of failed checks; `groups_integration_test.go` with two crossing Attaches.
3. Add the rows, `allowed_commands` and the metric. Check: `events_test.go`, `loudness_test.go`, `allowed_test.go`,
   `enqueue_test.go`, `metrics_test.go`.
4. Add the spec, the handlers and the read fields. Check: `make generate-check`; `commands_test.go`,
   `alertgroups_test.go`.
5. Write `test/e2e/attach_detach_test.go`. Check: the Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
API=localhost:8080/api/v1; CLK=localhost:8082/_dev/clock   # A: Admin; I: Ivan (Responder); V: a Viewer's token; AG1287, AG1288, AG1290 the public ids of three firing Alert Groups on two Routes
curl -s -H "$I" -X POST $API/alert-groups/$AG1288/attach -d "{\"main_alert_group_id\":\"$AG1287\"}" | jq -c '{n: .number, a: .attached_to.number, ac: .allowed_commands}'
# {"n":1288,"a":1287,"ac":["add_note","detach"]}
curl -s -H "$A" $API/alert-groups/$AG1287 | jq -c '{attached_count, allowed: .allowed_commands}'     # {"attached_count":1,...}  (an Alert Group with attached ones is not offered attach)
curl -s -H "$A" "$API/alert-groups/$AG1288/timeline?kind=status" | jq -c '[.items[] | {e: .event, l: .loudness, m: .mentions, main: .main_alert_group.number}] | last'
# {"e":"attached","l":"quiet","m":[],"main":1287}
curl -s -H "$A" "$API/alert-groups/$AG1287/timeline?kind=system" | jq -c '[.items[].event] | last'  # "alert_group_attached"
curl -s -H "$A" "$API/audit-log?limit=5" | jq -c '[.items[].action] | map(select(startswith("alert_group.")))'   # ["alert_group.attached"]
# refusals, in order
curl -s -H "$I" -X POST $API/alert-groups/$AG1287/attach -d "{\"main_alert_group_id\":\"$AG1287\"}" | jq -c '{status, code}'   # {"status":409,"code":"attach_to_itself"}
curl -s -H "$I" -X POST $API/alert-groups/$AG1290/attach -d "{\"main_alert_group_id\":\"$AG1288\"}" | jq -c '{status, code}'   # {"status":409,"code":"main_is_attached"} naming #1287
curl -s -H "$I" -X POST $API/alert-groups/$AG1287/attach -d "{\"main_alert_group_id\":\"$AG1290\"}" | jq -c '{status, code}'   # {"status":409,"code":"has_attached_alert_groups"}
curl -s -o /dev/null -w '%{http_code}\n' -H "$V" -X POST $API/alert-groups/$AG1290/attach -d "{\"main_alert_group_id\":\"$AG1287\"}"   # 403
curl -s -H "$I" -X POST $API/alert-groups/$AG1290/detach | jq -c '{status, code}'                      # {"status":409,"code":"not_attached"}
# the main Alert Group is acknowledged; a later attach takes its status and Owner
curl -s -H "$I" -X POST $API/alert-groups/$AG1287/acknowledge > /dev/null
curl -s -H "$I" -X POST $API/alert-groups/$AG1290/attach -d "{\"main_alert_group_id\":\"$AG1287\"}" | jq -c '{status, owner: .owner.login}'   # {"status":"acknowledged","owner":"ivan"}
# Detach keeps it
curl -s -H "$I" -X POST $API/alert-groups/$AG1290/detach | jq -c '{status, owner: .owner.login, a: .attached_to}'   # {"status":"acknowledged","owner":"ivan","a":null}
# the limit: alert_group.attach_max reduced for the check (the dev environment variable), the next attach is refused
psql "$MUSTER_DB" -c "select number, attached_to_id is not null as attached from alert_groups order by number"
```

`test/e2e/attach_detach_test.go` repeats these checks and adds the order of the checks over every pair, a Service account
that attaches, the crossing Attaches, the limit, an Alert Group of another Route and Integration, and the Audit log and
Timeline entries of each row.

## Open questions

1. **First acknowledgement on Attach.** C-37.FR-5 says that following an acknowledgement sets the attached Alert Group's
   first acknowledgement, when it has none, so that one detached later has one; FR-4 says Attach takes the main Alert
   Group's status but does not say whether taking an acknowledged status at Attach counts as following. This story sets
   it, so that the statistics of a detached Alert Group never lack a first acknowledgement (C-09.FR-27). The maintainer
   confirms; the alternative is to leave `first_acknowledged_at` empty until a later change of the main Alert Group
   (S-203) or an acknowledgement of its own.

## Notes

- Suggested commit: `feat(groups): add the attach and detach commands for alert groups`.
- Implementation starts after the tag of 0.2.0: these capabilities ship in 0.2.x patch releases. The migration number is
  assigned by the orchestrator at dispatch; it replaces the six CHECKs named above, each re-versioned in the order the
  stories merge (the C-34 migrations replace the same event CHECKs).
- Interim on `edge` between this story and S-204: an attached Alert Group still runs its chains and timers and the
  messengers show nothing of the attachment; S-203 to S-205 close these in order.
- `operator_attention` is false: `alert_group.attach_max` is decided (defaults.md).

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-37.FR-1 | partial | the Commands, Permission, Audit log types, `allowed_commands` and the metric; the MCP server is not built yet, and the messengers' lack of a button is S-205 |
| C-37.FR-2 | full | |
| C-37.FR-3 | partial | the pair of Attach and Detach; Commands and transitions on a main Alert Group with attached ones are S-203 |
| C-37.FR-4 | partial | the status taken and the Assignees and Unclaimed; the chains, ack timeout, Reminders and Snooze end are S-204 |
| C-37.FR-8 | partial | what Detach keeps; the chains and ack timeout starting over are S-204 |
| C-37.FR-15 | partial | the five rows and the four recorded by a person's Command; `status_followed` and the system detach S-203, the messenger column S-205, webhooks S-206 |
| C-37.FR-16 | full | |
| C-37.AC-1 | partial | the Timeline, Audit log and API; the Root message of the fake Mattermost server is S-205 |
| C-37.AC-4 | partial | the refusals of FR-2 and `403`; `alert_group_attached` is S-203, the bulk Acknowledge S-206 |
| C-37.AC-6 | partial | Detach keeps the Alert Group acknowledged by Ivan; the Root message edited back is S-205, the first Reminder 4 hours later S-204 |
| C-37.AC-11 | partial | the limit; the bulk Attach is S-206 |
