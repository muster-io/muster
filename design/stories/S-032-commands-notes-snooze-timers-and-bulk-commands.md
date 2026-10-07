---
id: S-032
title: Commands, Takeover, refusals, allowed commands and bulk commands (BE)
capability: C-10
kind: be
layer: L1
depends_on: [S-016, S-029]
covers: [C-10.FR-1, C-10.FR-2, C-10.FR-3, C-10.FR-4, C-10.FR-5, C-10.FR-6, C-10.FR-7, C-10.FR-10, C-10.FR-11, C-10.FR-12, C-10.FR-14, C-10.FR-15, C-10.FR-16, C-10.AC-1, C-10.AC-2, C-10.AC-3, C-10.AC-4, C-10.AC-5, C-10.AC-6, C-10.AC-7, C-10.AC-8, C-10.AC-9, C-10.AC-11, C-10.AC-12, C-10.AC-13, C-10.AC-14, C-10.AC-15, C-10.AC-16, C-10.AC-17, C-10.AC-18, C-09.FR-1, C-09.FR-4, C-09.FR-5, C-09.FR-6, C-09.FR-9, C-04.FR-2, C-04.AC-6, C-09.FR-11]
files_touched:
  - internal/groups/commands.go
  - internal/groups/bulk.go
  - internal/groups/allowed.go
  - internal/groups/dispatcher.go
  - internal/groups/grouping.go
  - internal/groups/events.go
  - internal/groups/read.go
  - internal/groups/list.go
  - internal/groups/query.sql
  - internal/groups/commands_test.go
  - internal/groups/bulk_test.go
  - internal/groups/allowed_test.go
  - internal/groups/events_test.go
  - internal/groups/dispatcher_test.go
  - internal/groups/list_test.go
  - internal/groups/groups_integration_test.go
  - internal/api/commands.go
  - internal/api/commands_test.go
  - internal/api/alertgroups.go
  - internal/api/middleware.go
  - internal/api/server.go
  - internal/api/server_test.go
  - internal/api/problem.go
  - api/openapi.yaml
  - api/README.md
  - internal/metrics/catalogue.go
  - internal/logging/events.go
  - internal/runtime/runtime.go
  - test/e2e/commands_test.go
  - test/e2e/alert_group_list_test.go
acceptance:
  - "[C-10.FR-1, C-10.FR-3, C-10.FR-5, C-10.FR-11] Acknowledge, Unacknowledge, Resolve, Unresolve, Snooze and Unsnooze change an Alert Group as the table of C-10.FR-1 says, through the dispatcher, with the Transport `ui` from a session and `api` from a token; status changes made by a person are Quiet; the dispatcher also records the Transports `mattermost` and `telegram` that S-061 and S-042 pass in."
  - "[C-09.FR-1] `getAlertGroup` and the items of `listAlertGroups` carry `owner`, `snooze_until` and `snoozed_by` as the Commands set them."
  - "[C-10.FR-2, C-10.FR-7, C-10.AC-1, C-10.AC-2] Acknowledge on a resolved Alert Group answers 409 `already_resolved` and changes nothing; Unresolve of an Alert Group the system resolved answers 409 `resolved_automatically`, with no Alert still firing `all_alerts_resolved`, and with a newer open Alert Group of the same key `newer_alert_group_exists` naming it."
  - "[C-10.FR-7, C-10.FR-16] `allowed_commands` offers `unresolve` only while every precondition of Unresolve holds; a person-resolved Alert Group with a newer open Alert Group of the same Route and key carries the notice `newer_alert_group_exists` with `related_alert_group` and no `unresolve`, and the API refusal stays for a race."
  - "[C-10.FR-4, C-10.FR-10, C-10.AC-3] Acknowledge by another user is a Takeover — Loud, mentioning `previous_owner`, an Audit log entry of its own; two users acknowledging in the same second leave the later one as Owner and a single `takeover` entry; Acknowledge by the current Owner answers `unchanged` and records nothing."
  - "[C-10.AC-4, C-10.FR-3] A Viewer's Acknowledge reaches the dispatcher, whose Permission step answers 403 and writes nothing but a `command_refused` log line; every Command operation is marked so that the middleware checks only authentication."
  - "[C-10.FR-12, C-10.AC-5] Each command made through the API appears once in the Audit log with its own action, the actor, the token's name and the Transport `api`."
  - "[C-10.FR-1, C-04.FR-2, C-04.AC-6] Acknowledge from a Service account token answers 409 `owner_must_be_user` and changes nothing; Resolve and Snooze from it succeed and show the Service account; Acknowledge with a Personal access token makes its User the Owner."
  - "[C-10.AC-6, C-09.FR-4] All Alerts of an acknowledged Alert Group resolving and one firing again within the Reopen window leave the same `#N` acknowledged by the same Owner with Reopen count 1 and a `reopened` entry, `loud`, `mentions` `[owner]`."
  - "[C-10.AC-7, C-10.AC-9, C-09.FR-6] A Continuation on an acknowledged Alert Group keeps it acknowledged with a Quiet `alert_continued` and no Loud entry; a new Alert joining it records `alerts_added` with `loudness` `quiet`."
  - "[C-10.AC-8, C-09.FR-5] After a person resolves an Alert Group with Alerts still firing, a new Alert Group appears when the Grace period ends and not before, marked \"firing again after a manual resolve of #N\"."
  - "[C-10.AC-12, C-10.AC-13, C-09.FR-9] In a Route whose Group key lacks `severity`, a critical Alert joining an acknowledged warning Alert Group makes it firing without an Owner with a Loud `urgency_raised` mentioning `[owner, rise_to_urgent]` while `route.urgent_rise_removes_ack` is on, and keeps the acknowledgement with a Quiet one while it is off; the same rise ends a Snooze set while not Urgent, Loudly, and leaves snoozed an Alert Group snoozed while Urgent whose critical Alert resolves and fires again."
  - "[C-10.FR-6, C-10.AC-17] A Snooze with neither `until` nor `no_end`, or an `until` in the past, answers 422; a bulk Snooze without `snooze` answers 422; `no_end: true` snoozes with no end."
  - "[C-10.FR-14, C-10.AC-11, C-10.AC-15] A bulk Resolve of three Alert Groups, one already resolved, resolves two, refuses one with `already_resolved` and writes two Audit log entries; a bulk Acknowledge by Bob of three firing Alert Groups, one owned by Alice, acknowledges two, returns \"skipped: owned by Alice\" and records no `takeover`."
  - "[C-10.FR-16, C-10.AC-18] `allowed_commands` of a firing Alert Group lists `acknowledge`, `resolve` and `snooze` for a Responder and no `acknowledge` for a Service account; a bulk Acknowledge by a Service account refuses every item with `owner_must_be_user`."
  - "[C-10.FR-15, C-10.AC-16, C-09.FR-11] Every row of the lifecycle event table of C-10.FR-15 except `note_added` records exactly one Timeline entry with its `event`, kind, `loudness` and `mentions`, and an Acknowledge by the current Owner records none (a table-driven test)."
  - "[C-10.AC-14] For Alert Groups of one Route acknowledged 5 and 15 minutes after they started — the second unacknowledged, acknowledged again, taken over and reopened later — and a third resolved without an acknowledgement, `getAlertGroupStatistics` returns a median time to acknowledge of 600 seconds over 2 Alert Groups."
verify: "make ci test-integration e2e"
operator_attention: false
issue: 32
---

# S-032. Commands, Takeover, refusals, allowed commands and bulk commands (BE)

## Scope

**IN**

- The Commands of C-10.FR-1 but Add Note, through the dispatcher: Permission, precondition, transition, Audit log,
  Timeline, re-render; Takeover; refusals; serialization per Alert Group.
- The Permission check moved from the middleware into the dispatcher for the Command operations.
- `allowed_commands`, the notice of a newer Alert Group, bulk commands.
- The acknowledged and snoozed paths of S-028 (Reopen, new Alerts, the rise to Urgent) and the Grace period after a
  person's resolve, now reachable and verified.

**OUT**

- Notes and Resolve with a Note, the Snooze end timer, the user directory, the Owner filters and the release of the
  acknowledgements of disabled and deleted users (S-063). Until S-063, a Snooze with `until` stays snoozed past that
  time until a person changes it.
- The UI (S-033).
- Commands from Mattermost and Telegram and their private answers (S-061, S-042); presses from accounts without an
  Account link (S-051).
- "Still on it", Reminders, ack timeouts and auto-unacknowledge (S-049).

## Contracts

- **Operations implemented**: `acknowledgeAlertGroup`, `unacknowledgeAlertGroup`, `resolveAlertGroup`,
  `unresolveAlertGroup`, `snoozeAlertGroup`, `unsnoozeAlertGroup`, `runBulkCommand`; `AlertGroup` gains `owner`,
  `snooze_until`, `snoozed_by` and `allowed_commands`. Schemas: `CommandResult`, `CommandOutcome`, `CommandName`,
  `ResolveRequest`, `SnoozeRequest`, `BulkCommandRequest`, `BulkCommandItem`, `BulkCommandResult`, `BulkOutcome`,
  `UserRef`, `ActorRef`. `resolveAlertGroup` with `note` answers `422 unsupported` at `/note` until S-063.
- **Dispatcher steps** (C-10.FR-3, ADR-0016): the Permission of the command (`alert-groups:acknowledge`, `:resolve`,
  `:snooze`; `403` otherwise) → the precondition on the locked row (`409` `command-refused` with a code) → the
  transition → the Audit log entry → the Timeline entry of C-10.FR-15 → re-render (the hook of S-028) → the hints of
  S-029. A session gives the Transport `ui`, a token `api`; a Personal access token acts as its User, a Service account
  as itself. The row lock serializes commands on one Alert Group, each checked against the state the previous one left
  (C-10.FR-10).
- **Permission refusals in the dispatcher** (C-10.FR-3, C-10.AC-4; `internal/api/middleware.go`, `api/openapi.yaml`,
  `api/README.md`): today `permit` in the middleware answers `403` from an operation's `x-permission` before any
  handler runs, so a refused Command never reaches the dispatcher and no `command_refused` line is written. The Command
  operations — the six above and `runBulkCommand`; `createAlertGroupNote` from S-063 and `answerReminder` from S-049
  the same way — keep `x-permission`, which documents the Permission and feeds the generated reference, and gain the
  operation extension `x-permission-check: dispatcher`, added to the table of extensions in `api/README.md`.
  `readOperations` reads it, and for a marked operation `permit` checks only that the caller is authenticated. The
  dispatcher then checks the Permission as its first step, in the order of ADR-0016, writes `command_refused` with the
  code `forbidden`, and returns a domain error that `internal/api/problem.go` maps to the catalogued `403` `forbidden`
  Problem with the text the middleware uses ("This needs the Permission …"). For `runBulkCommand` the Permission of the
  chosen command is checked once before any item, and its refusal is a `403` for the whole request. A test in
  `server_test.go` checks that exactly the marked operations reach their handler without the Permission.
- **Commands** (C-10.FR-1, FR-4, FR-5, FR-6, FR-7; `alert_groups`, `timers`):
  - **Acknowledge**: firing or snoozed → acknowledged with the caller as Owner, ending a Snooze; sets
    `acknowledged_at`, and `first_acknowledged_at` once, observing `muster_alert_group_time_to_ack_seconds{route}` then.
    By the current Owner: `outcome` `unchanged`, no entry. By another user while acknowledged: Takeover — the Owner
    changes, `takeover` with `previous_owner_user_id`. A Service account: `409 owner_must_be_user`. Resolved: `409
    already_resolved`; it never reopens.
  - **Unacknowledge**: acknowledged → firing without an Owner, the entry naming the Owner who lost it; otherwise
    `409 not_acknowledged`.
  - **Resolve**: firing, acknowledged or snoozed → resolved by the User or Service account; a Snooze ends; the Grace
    period of S-028 starts. Resolved: `409 already_resolved`. The optional Note is S-063's.
  - **Unresolve** (UI and API only): resolved by a person → firing without an Owner, its Alerts still firing active
    again, the Grace period timer removed. Refused, in this order, with `not_resolved` when it is not resolved,
    `resolved_automatically` when the system resolved it, `route_deleted` when its Route was deleted meanwhile
    (Unresolve takes the Route FOR SHARE and then the counter row, as grouping does), `newer_alert_group_exists` with
    `related_alert_group` when an open Alert Group of the same Route and key takes part in grouping (never for an
    Alert Group moved to the Default route, which stays out of it, `design/db/schema.md` §4.9), and
    `all_alerts_resolved` when none of its Alerts still fires.
  - **Snooze**: firing, acknowledged or snoozed → snoozed with exactly one of `until` (in the future, otherwise `422
    out_of_range` at `/until`) and `no_end: true` (neither or both: `422 one_of_required`); the snoozer kept in
    `snoozed_by_*`, `snoozed_while_urgent` from the current urgency; an acknowledged Alert Group loses its Owner, whom
    the `snoozed` entry names; on a snoozed one only the end changes. Resolved: `409 already_resolved`. The
    `snooze_end` timer at `until` is S-063's.
  - **Unsnooze**: snoozed → firing without an Owner; otherwise `409 not_snoozed`.
- **Refusal codes** (C-10.FR-2): `already_resolved`, `not_acknowledged`, `not_snoozed`, `not_resolved`,
  `all_alerts_resolved`, `resolved_automatically`, `route_deleted`, `newer_alert_group_exists`, `owner_must_be_user` as
  `409 command-refused`, mapped in `internal/api/problem.go`; a missing Permission as `403`. Every refusal writes
  `command_refused` and nothing else.
- **`allowed_commands`** (C-10.FR-16): from the status, the caller's Permissions and identity, in the order of
  `CommandName` — firing: `acknowledge`, `resolve`, `snooze`; acknowledged: `acknowledge` (a Takeover, so not offered
  to the current Owner, for whom it changes nothing), `unacknowledge`, `resolve`, `snooze`; snoozed: `acknowledge`,
  `resolve`, `snooze`, `unsnooze`; resolved by a person: `unresolve` only while every precondition of Unresolve holds
  — its Route not deleted, no newer open Alert Group of the same Route and key and at least one Alert still firing;
  never `acknowledge` for a Service account; `add_note` from S-063, `still_on_it` from S-049.
- **Notice of a newer Alert Group** (C-10.FR-7; `api/openapi.yaml`): a person-resolved Alert Group for which a newer
  open Alert Group of the same Route and key takes part in grouping carries the notice `newer_alert_group_exists` with
  `related_alert_group` (`AlertGroupRef`: id and `#N`), so the UI shows a link to it in place of Unresolve (S-033)
  instead of offering a command that would be refused. The spec gains the kind in the `AlertGroupNotice` enum and the
  property `related_alert_group`. The `409` refusal stays for the race in which that Alert Group opens between the read
  and the command.
- **Bulk** (C-10.FR-14): `runBulkCommand` with `acknowledge`, `resolve`, `snooze` or `unsnooze` on 1 to
  `alert_group.bulk_max` ids; the caller needs the command's Permission (`403` for the whole request); `snooze` is
  required for `snooze` (`422 required` at `/snooze`). Each id runs through the dispatcher in a transaction of its own,
  with its own Audit log and Timeline entries; results come in request order — `done`, `unchanged`, `refused` with the
  code and message, `skipped` with `owned_by_other` and "skipped: owned by {Owner}" for a bulk Acknowledge of an Alert
  Group another user owns (never a Takeover), and `failed` with `not_found` and "not found" for an id that names no
  Alert Group the caller may see.
- **Audit log actions** (C-10.FR-12): `alert_group.acknowledged`, `alert_group.taken_over`,
  `alert_group.unacknowledged`, `alert_group.resolved`, `alert_group.unresolved`, `alert_group.snoozed`,
  `alert_group.unsnoozed`, each with the Alert Group, the actor, the token and the Transport; `alert_group.note_added`
  is S-063's.
- **Metrics and log events**: `muster_commands_total{command,transport,outcome}` (`done`, `unchanged`, `refused`,
  `skipped`, `failed`; the closed set of `command` names `add_note` and `still_on_it` already),
  `muster_alert_group_time_to_ack_seconds{route}`; `command_executed` (INFO: `command`, `group`, `actor`, `transport`,
  `outcome`) and `command_refused` (INFO: `command`, `group`, `actor`, `transport`, `code`).
- **Wiring** (`internal/api/server.go`, `internal/runtime/runtime.go`): the operations join the implemented-operations
  map, and the dispatcher's Command entry points reach the API through its `Config`.

## Steps

1. Add the extension `x-permission-check: dispatcher`, its reading in the middleware and the Permission and Audit log
   steps of the dispatcher. Check: `server_test.go` covers the marked operations; a Viewer's Acknowledge reaches the
   dispatcher.
2. Write the six commands with their refusals. Check: a table test covers every status and command, and two concurrent
   Acknowledges produce one Takeover.
3. Write `allowed_commands` and the notice of a newer Alert Group. Check: tests cover a Responder, a Viewer, a Service
   account, and a person-resolved Alert Group with and without a newer one.
4. Write bulk commands. Check: tests cover C-10.AC-11, AC-15, AC-17 and AC-18.
5. Run the acknowledged and snoozed paths of S-028 end to end and the lifecycle event rows of C-10.FR-15. Check:
   `events_test.go` covers every row but `note_added`; Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
# the Admin's session H (Transport ui) and Personal access token in A (Transport api) as in S-023; the Responder "bob"
# signed in as in S-011 with HB=(-b bob -H "X-CSRF-Token: $BCSRF" -H 'Content-Type: application/json'); Service accounts
# "robot" (Role responder) and "watcher" (Role viewer) with the tokens SAT and VAT (S-016); an Integration "cmd" with its
# fake receiver "cmd"; ADV, NOTIFY, PUTA and AG as in S-028; the Route "ops" (team="ops", Group key alertname, cluster)
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake
S=(-H "Authorization: Bearer $SAT" -H 'Content-Type: application/json'); V=(-H "Authorization: Bearer $VAT")
GET() { curl -s "${A[@]}" "$API/alert-groups/$1"; }
LASTE() { curl -s "${A[@]}" "$API/alert-groups/$1/timeline?limit=1" | jq -c '.items[0] | {event, loudness, mentions}'; }
curl -s -X PUT $FAM/groups/o1 -d '{"receiver":"cmd","route":"{}","labels":{"alertname":"QueueFull"}}' > /dev/null
for c in a b c d; do PUTA o1 $c "{\"labels\":{\"team\":\"ops\",\"cluster\":\"$c\",\"severity\":\"warning\"}}"; done
NOTIFY o1 '{"reason":"first notification"}'
GA=$(AG 'cluster%3D%22a%22'); GB=$(AG 'cluster%3D%22b%22'); GC=$(AG 'cluster%3D%22c%22'); GD=$(AG 'cluster%3D%22d%22')

# C-10.AC-18 and C-10.AC-4: the Viewer's refusal comes from the dispatcher
curl -s -b bob "$API/alert-groups/$GA" | jq -c .allowed_commands                    # ["acknowledge","resolve","snooze"]
curl -s "${S[@]}" "$API/alert-groups/$GA" | jq -c .allowed_commands                  # ["resolve","snooze"]
curl -s -o /dev/null -w '%{http_code}\n' "${V[@]}" -X POST "$API/alert-groups/$GA/acknowledge"            # 403
grep '"event":"command_refused"' dev.log | jq -c '{command, code}'                                         # {"command":"acknowledge","code":"forbidden"}
curl -s "${A[@]}" "$API/audit-log?action=alert_group.acknowledged" | jq '.items | length'                  # 0

# Acknowledge, Takeover (C-10.AC-3), unchanged, Service account (C-04.AC-6)
curl -s "${H[@]}" -X POST "$API/alert-groups/$GA/acknowledge" | jq -c '{outcome, s: .alert_group.status, o: .alert_group.owner.login}'
# {"outcome":"done","s":"acknowledged","o":"admin@example.org"}
curl -s "${H[@]}" -X POST "$API/alert-groups/$GA/acknowledge" | jq -r .outcome                            # unchanged
curl -s "${HB[@]}" -X POST "$API/alert-groups/$GA/acknowledge" | jq -r .alert_group.owner.login           # bob
LASTE $GA                                                       # {"event":"takeover","loudness":"loud","mentions":["previous_owner"]}
curl -s "${S[@]}" -X POST "$API/alert-groups/$GB/acknowledge" | jq -r .code                               # owner_must_be_user
curl -s "${H[@]}" -X POST "$API/alert-groups/$GB/acknowledge" > /dev/null & curl -s "${HB[@]}" -X POST "$API/alert-groups/$GB/acknowledge" > /dev/null & wait
curl -s "${A[@]}" "$API/alert-groups/$GB/timeline" | jq '[.items[] | select(.event == "takeover")] | length'   # 1

# C-10.AC-9 and C-10.AC-7 on the acknowledged GA
PUTA o1 a2 '{"labels":{"team":"ops","cluster":"a","severity":"warning","n":"2"}}'; NOTIFY o1 '{"reason":"new alerts added"}'
LASTE $GA                                                       # {"event":"alerts_added","loudness":"quiet","mentions":[]}
PUTA o1 a '{"labels":{"team":"ops","cluster":"a","severity":"warning"},"starts_at":"now"}'; NOTIFY o1 '{"reason":"repeat interval elapsed"}'
LASTE $GA                                                       # {"event":"alert_continued","loudness":"quiet","mentions":[]}

# C-10.AC-6: Reopen into acknowledged
PUTA o1 a '{"labels":{"team":"ops","cluster":"a","severity":"warning"},"status":"resolved"}'
PUTA o1 a2 '{"labels":{"team":"ops","cluster":"a","severity":"warning","n":"2"},"status":"resolved"}'
NOTIFY o1 '{"reason":"some alerts resolved"}'; GET $GA | jq -r .status                                    # resolved
curl -s "${A[@]}" -X POST "$API/alert-groups/$GA/unresolve" | jq -r .code                                  # resolved_automatically
ADV 300; PUTA o1 a3 '{"labels":{"team":"ops","cluster":"a","severity":"warning","n":"3"}}'; NOTIFY o1 '{"reason":"new alerts added"}'
GET $GA | jq -c '{status, o: .owner.login, reopen_count}'                                                # {"status":"acknowledged","o":"bob","reopen_count":1}
LASTE $GA                                                       # {"event":"reopened","loudness":"loud","mentions":["owner"]}

# C-10.AC-12: a rise to Urgent removes the acknowledgement of GA, and with the setting off keeps it on GB
PUTA o1 a4 '{"labels":{"team":"ops","cluster":"a","severity":"critical","n":"4"}}'; NOTIFY o1 '{"reason":"new alerts added"}'
GET $GA | jq -c '{status, owner}'                                                                         # {"status":"firing","owner":null}
LASTE $GA                                                       # {"event":"urgency_raised","loudness":"loud","mentions":["owner","rise_to_urgent"]}
# set policy.urgent_rise_removes_ack to false on "ops" (updateRoute with If-Match), then:
PUTA o1 b2 '{"labels":{"team":"ops","cluster":"b","severity":"critical","n":"2"}}'; NOTIFY o1 '{"reason":"new alerts added"}'
GET $GB | jq -r .status; LASTE $GB                              # acknowledged / {"event":"urgency_raised","loudness":"quiet","mentions":[]}

# C-10.AC-13: a Snooze set while not Urgent ends with the rise; one set while Urgent stays
curl -s "${A[@]}" -X POST "$API/alert-groups/$GC/snooze" -d '{"no_end":true}' | jq -r .alert_group.status  # snoozed
PUTA o1 c2 '{"labels":{"team":"ops","cluster":"c","severity":"critical","n":"2"}}'; NOTIFY o1 '{"reason":"new alerts added"}'
GET $GC | jq -r .status; LASTE $GC                              # firing / {"event":"urgency_raised","loudness":"loud",…}
curl -s "${A[@]}" -X POST "$API/alert-groups/$GC/snooze" -d '{"no_end":true}' > /dev/null              # snoozed while Urgent
PUTA o1 c2 '{"labels":{"team":"ops","cluster":"c","severity":"critical","n":"2"},"status":"resolved"}'; NOTIFY o1 '{"reason":"some alerts resolved"}'
PUTA o1 c2 '{"labels":{"team":"ops","cluster":"c","severity":"critical","n":"2"},"status":"firing","starts_at":"now"}'; NOTIFY o1 '{"reason":"new alerts added"}'
GET $GC | jq -r .status                                                                                   # snoozed

# C-10.AC-17: the Snooze request names its choice
curl -s "${A[@]}" -X POST "$API/alert-groups/$GD/snooze" -d '{}' | jq -r '.errors[0].code'                 # one_of_required
curl -s "${A[@]}" -X POST "$API/alert-groups/$GD/snooze" -d '{"until":"2001-01-01T00:00:00Z"}' | jq -r '.errors[0].code'   # out_of_range

# C-10.AC-1, C-10.AC-8, C-10.AC-2: a person's resolve, the Grace period, Unresolve and the newer Alert Group
curl -s "${A[@]}" -X POST "$API/alert-groups/$GD/resolve" -d '{}' \
  | jq -c '{s: .alert_group.status, n: [.alert_group.notices[] | select(.kind == "alerts_still_firing") | .count], u: (.alert_group.allowed_commands | index("unresolve") != null)}'
# {"s":"resolved","n":[1],"u":true}
curl -s "${A[@]}" -X POST "$API/alert-groups/$GD/acknowledge" | jq -r .code                                # already_resolved
ADV 840; NOTIFY o1 '{"reason":"repeat interval elapsed"}'; [ "$(AG 'cluster%3D%22d%22')" = "$GD" ] && echo "not yet"   # not yet
ADV 120; NEWD=$(AG 'cluster%3D%22d%22'); GET $NEWD | jq -c '[.notices[] | select(.kind == "firing_again_after_manual_resolve") | .resolved_number]'
# [<#N of GD>]
GET $GD | jq -c '{u: (.allowed_commands | index("unresolve") != null), n: [.notices[] | select(.kind == "newer_alert_group_exists") | .related_alert_group.id]}'
# {"u":false,"n":["<NEWD>"]}
curl -s "${A[@]}" -X POST "$API/alert-groups/$GD/unresolve" | jq -c '{code, r: .related_alert_group.id}'    # {"code":"newer_alert_group_exists","r":"<NEWD>"}

# C-10.AC-11 and C-10.AC-15: bulk
curl -s "${A[@]}" "$API/alert-groups/bulk-commands" -d "{\"command\":\"resolve\",\"alert_group_ids\":[\"$GA\",\"$GB\",\"$GD\"]}" \
  | jq -c '[.results[] | {outcome, code}]'
# [{"outcome":"done","code":null},{"outcome":"done","code":null},{"outcome":"refused","code":"already_resolved"}]
curl -s "${A[@]}" "$API/alert-groups/bulk-commands" -d '{"command":"snooze","alert_group_ids":["'"$GC"'"]}' | jq -r '.errors[0].pointer'   # /snooze
curl -s -o /dev/null -w '%{http_code}\n' "${V[@]}" -H 'Content-Type: application/json' "$API/alert-groups/bulk-commands" \
  -d "{\"command\":\"resolve\",\"alert_group_ids\":[\"$GC\"]}"                                                # 403
# three new firing Alert Groups, one acknowledged by the Admin; Bob acknowledges all three:
curl -s "${HB[@]}" "$API/alert-groups/bulk-commands" -d "{\"command\":\"acknowledge\",\"alert_group_ids\":$IDS3}" \
  | jq -c '[.results[] | {outcome, message}]'
# [{"outcome":"done","message":null},{"outcome":"done","message":null},{"outcome":"skipped","message":"skipped: owned by admin"}]
curl -s "${S[@]}" "$API/alert-groups/bulk-commands" -d "{\"command\":\"acknowledge\",\"alert_group_ids\":$IDS3}" | jq -c '[.results[].code] | unique'
# ["owner_must_be_user"]

# C-10.AC-5: commands through the API in the Audit log
curl -s "${A[@]}" "$API/audit-log?action=alert_group.resolved&limit=3" | jq -c '[.items[] | {a: .actor.name, t: .token_name, tr: .transport}] | unique'
# [{"a":"admin","t":"verify","tr":"api"}]
```

C-10.AC-14 (time to acknowledge over three Alert Groups) runs as an end-to-end test with the development clock, whose
output the pull request records; `events_test.go` is the evidence for C-10.AC-16.

## Open questions

None.

## Notes

- Suggested commit: `feat(groups): add commands, takeover, refusals and bulk commands`.
- Split before implementation, because with the wiring files and the Permission move it touched about 41 files: S-032
  keeps the Commands, their refusals, `allowed_commands` and bulk; S-063 takes Notes, the Snooze end timer, the user
  directory, the Owner filters and the Owner release.
- The Takeover is the only Loud result of a command (C-10.FR-5); the Loud `urgency_raised` and `reopened` here are
  system transitions of S-028 reached through the acknowledged and snoozed states.
- `IDS3` in Verification stands for three new firing Alert Groups prepared the same way; the pull request shows them.
- GD has one Alert of its own when resolved above, so `alerts_still_firing` counts 1.
- The command result returns the Alert Group even to a Personal access token narrowed without `alert-groups:read`,
  because every Role holds that Permission (maintainer decision on the pull request).

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-10.FR-1 | partial | every command but Add Note and the Note of Resolve, which are S-063 |
| C-10.FR-2 | partial | the API refusals; their texts in the UI are S-033 |
| C-10.FR-3 | full | the messenger Transports are passed in by S-061 and S-042 |
| C-10.FR-4 | partial | the Takeover; Reminders starting over are S-049 |
| C-10.FR-5 | full | |
| C-10.FR-6 | partial | `until` and `no_end` in the API; the Snooze end is S-063, the dialog and the messenger durations S-033, S-061 and S-042 |
| C-10.FR-7 | partial | the API and the notice of a newer Alert Group; the UI is S-033 |
| C-10.FR-10 | full | |
| C-10.FR-11 | partial | the dispatcher accepts the messenger Transports; their adapters are S-061 and S-042, refusals without an Account link S-051 |
| C-10.FR-12 | partial | the Audit log actions of the Commands; `alert_group.note_added` is S-063 |
| C-10.FR-14 | partial | the API; the selection is S-033 |
| C-10.FR-15 | partial | every row but `note_added` (S-063) |
| C-10.FR-16 | partial | without `add_note` (S-063) and `still_on_it` (S-049) |
| C-10.AC-1 | full | |
| C-10.AC-2 | full | |
| C-10.AC-3 | full | |
| C-10.AC-4 | full | |
| C-10.AC-5 | full | |
| C-10.AC-6 | full | |
| C-10.AC-7 | full | |
| C-10.AC-8 | full | |
| C-10.AC-9 | full | |
| C-10.AC-11 | full | |
| C-10.AC-12 | full | |
| C-10.AC-13 | full | |
| C-10.AC-14 | partial | the API; the statistics page is S-033 |
| C-10.AC-15 | full | |
| C-10.AC-16 | partial | the Command rows; the `note_added` row and Resolve with a Note are S-063 |
| C-10.AC-17 | full | |
| C-10.AC-18 | partial | without `add_note`, which S-063 adds |
| C-09.FR-1 | full | together with S-028; Unclaimed comes with S-049 |
| C-09.FR-4 | full | together with S-028 and S-031 |
| C-09.FR-5 | full | together with S-028 and S-031 |
| C-09.FR-6 | full | together with S-028 |
| C-09.FR-9 | full | together with S-028 and S-031 |
| C-04.FR-2 | partial | Acknowledge refused for Service accounts; "Still on it" is S-049 |
| C-04.AC-6 | partial | Acknowledge, Resolve and Snooze; a Note from a Service account is S-063 |
| C-09.FR-11 | partial | Command entries in the Timeline |
