---
id: S-063
title: Notes, Snooze ends, the user directory, Owner filters and the release of disabled and deleted Owners (BE)
capability: C-10
kind: be
layer: L1
depends_on: [S-032]
covers: [C-10.FR-1, C-10.FR-6, C-10.FR-8, C-10.FR-12, C-10.FR-13, C-10.FR-15, C-10.FR-16, C-10.AC-10, C-10.AC-16, C-10.AC-18, C-10.AC-19, C-10.AC-20, C-09.FR-8, C-09.FR-11, C-09.FR-12, C-09.FR-13, C-09.FR-16, C-09.FR-22, C-04.AC-6, C-03.FR-13, C-03.AC-27]
files_touched:
  - internal/groups/notes.go
  - internal/groups/snooze.go
  - internal/groups/release.go
  - internal/groups/commands.go
  - internal/groups/bulk.go
  - internal/groups/dispatcher.go
  - internal/groups/events.go
  - internal/groups/allowed.go
  - internal/groups/filters.go
  - internal/groups/list.go
  - internal/groups/counts.go
  - internal/groups/query.sql
  - internal/groups/notes_test.go
  - internal/groups/release_test.go
  - internal/groups/commands_test.go
  - internal/groups/bulk_test.go
  - internal/groups/dispatcher_test.go
  - internal/groups/allowed_test.go
  - internal/groups/events_test.go
  - internal/groups/list_test.go
  - internal/groups/groups_integration_test.go
  - internal/users/directory.go
  - internal/users/admin.go
  - internal/users/admin_test.go
  - internal/users/admin_integration_test.go
  - internal/users/query.sql
  - internal/api/alertgroups.go
  - internal/api/alertgroups_test.go
  - internal/api/commands.go
  - internal/api/commands_test.go
  - internal/api/users.go
  - internal/api/users_test.go
  - internal/api/server.go
  - internal/api/server_test.go
  - api/openapi.yaml
  - internal/runtime/runtime.go
  - internal/runtime/runtime_test.go
  - test/e2e/notes_test.go
  - test/e2e/alert_group_list_test.go
  - design/prd/l1/defaults.md
  - design/prd/L1.md
acceptance:
  - "[C-10.FR-8, C-10.FR-1, C-10.AC-20, C-09.FR-16] Notes up to `alert_group.note_max_length` characters are added on an Alert Group in any status, listed with their author and Transport and in the Timeline; with the development clock a Note on a resolved Alert Group is still listed after `retention.alert_details` and is gone with the summary row after `retention.alert_group_summaries`."
  - "[C-10.FR-1, C-10.FR-15, C-10.AC-16] Resolve with a Note records `resolved` and then `note_added` (Quiet, without Mentions), and a Note alone records one `note_added`."
  - "[C-04.AC-6] A Note from a Service account token succeeds and shows the Service account as its author."
  - "[C-10.FR-16, C-10.AC-18] `allowed_commands` lists `add_note` for every caller with `alert-groups:note`, in every status; a Viewer's Note answers 403 from the dispatcher's Permission step."
  - "[C-10.FR-6, C-10.AC-10, C-09.FR-8, C-09.FR-12] With the development clock, a Snooze until T on a firing Alert Group whose Alerts keep firing makes it firing without an Owner at T, with a `snooze_ended` entry — `loud`, `[snooze_ended]` — listing the Alerts that joined meanwhile; Unsnooze, Acknowledge or Resolve before T cancels the timer, and Unsnooze is Quiet."
  - "[C-10.FR-13, C-10.AC-19, C-09.FR-13] `owner=me`, `owner=none`, `owner=<id>` and `snoozed_no_end` filter the list and the counts; `owner=me` with a Service account token answers 422 `unsupported`; `listUserDirectory`, readable by a Viewer, lists a deleted user as deactivated, and the Timeline entry that names that user as the previous Owner shows them as deactivated."
  - "[C-03.FR-13, C-03.AC-27, C-09.FR-22] Disabling a user who owns acknowledged Alert Groups makes each one firing without an Owner, in the transaction of `disableUser`, with an `unacknowledged` Timeline entry by the system — `reason` `owner_disabled`, `previous_owner` that user, `loud`, no Mentions — and no Audit log entry of its own beside `user.disabled`; enabling the user again changes nothing; deleting a user does the same with `owner_deleted`; an Alert Group the user owned that the system resolved reopens into firing."
verify: "make ci test-integration e2e"
operator_attention: false
issue: 137
---

# S-063. Notes, Snooze ends, the user directory, Owner filters and the release of disabled and deleted Owners (BE)

## Scope

**IN**

- Notes: adding and listing them, Resolve with a Note, `add_note` in `allowed_commands`, keeping them past the
  details.
- The Snooze end timer.
- The user directory, the Owner filters of the list and the counts, deactivated users wherever a `UserRef` names them.
- Releasing the acknowledgements of a disabled or deleted user (C-03.FR-13), through a hook that the user
  administration calls inside its own transactions.

**OUT**

- The Commands, their refusals, `allowed_commands` and bulk commands (S-032).
- The UI: the Note box, the Owner filters and columns, the Snooze dialog (S-033).
- Restarting the ack timeout when a Snooze ends and the Reminders around a release (S-049).

## Contracts

- **Operations implemented**: `listAlertGroupNotes`, `createAlertGroupNote`, `listUserDirectory`;
  `resolveAlertGroup` accepts `note` (the `422 unsupported` of S-032 is removed); `listAlertGroups` and
  `getAlertGroupCounts` accept `owner` and `snoozed_no_end` (the `422 unsupported` of S-029 is removed, and its tests
  in `list_test.go`, `alertgroups_test.go` and `test/e2e/alert_group_list_test.go` change with it). Schemas: `NoteInput`, `Note(List)`,
  `UserDirectoryList`; `createAlertGroupNote` gains `x-permission-check: dispatcher` in `api/openapi.yaml`, like the
  Commands of S-032, so its Permission is checked by the dispatcher.
- **Notes** (C-10.FR-8, C-10.FR-1; `notes.go`, `notes`): Add Note through the dispatcher in any status, also after the
  details were removed; Permission `alert-groups:note`; 1 to `alert_group.note_max_length` characters (`too_long`:
  `400` from the request validation of the API, `422` from the dispatcher); a `notes` row with the author (User or Service account), token and Transport; `note_added`, Quiet,
  without Mentions; the Audit log action `alert_group.note_added`; `muster_commands_total{command="add_note"}`.
  `resolveAlertGroup` with `note` records `resolved` and then `note_added` in the same transaction. Notes are kept
  with the summary row and go with it (C-09.FR-16); the Timeline of an Alert Group whose details were removed still
  returns them (S-029).
- **`allowed_commands`** (C-10.FR-16): `add_note` always with `alert-groups:note`, whatever the status.
- **Snooze end** (C-09.FR-8, FR-12; `snooze.go`, `timers`): a Snooze with `until` writes a `snooze_end` timer at
  `until` in the Command's transaction, a new `until` moves it, and Unsnooze, Acknowledge and Resolve remove it. The
  dispatcher keeps the timer with the Snooze on every path that enters, moves or leaves one — the Commands, bulk
  Snooze, a resolution by the system, a Reopen into snoozed, a rise to Urgent and the end itself (D269). The worker of
  S-028 handles the kind `snooze_end`: a still snoozed Alert Group becomes firing without an Owner with
  `snooze_ended` (Loud, `[snooze_ended]`) listing the fingerprints of the Alerts that joined during the Snooze — after
  the entry that took it into snoozed, and for a Reopen into snoozed also the Alerts that reopened it. The worker
  wakes on a move of the development clock (S-028), so an advance past `until` ends the Snooze at once. S-049 restarts
  the ack timeout there.
- **Owner filters** (C-10.FR-13, C-09.FR-13; `filters.go`): `owner` — a user's `public_id`, `me` (`422 unsupported`
  for a Service account), or `none`; `snoozed_no_end`; for the list and the counts.
- **User directory** (C-10.FR-13; `alert-groups:read`; `internal/users/directory.go`): every user, deleted ones
  included with `deactivated: true`, name and login filtered by `q`, by cursor. A deleted user shows
  `deactivated: true` wherever a `UserRef` names them — a previous Owner in the Timeline, who set a Snooze, the author
  of a Note (C-03.FR-13).
- **Owner release** (C-03.FR-13, C-09.FR-22; `internal/groups/release.go`, `internal/users/admin.go`): `users.Admin`
  runs `Disable` and `Delete` in transactions of its own (`setStatus` and `Delete` in `admin.go`), so the release runs
  inside them through a hook: `users` declares the interface it consumes — `OwnerReleaser` with `ReleaseOwner(ctx, tx,
  userID, reason)`, where `tx` is the transaction the store's `InTx` runs on — and calls it after the status change of a
  disable and after the pseudonymization of a delete, before the Audit log entry; `groups` implements it, and
  `runtime.go` sets it on the `Admin` that the API uses. The `Admin` that `muster admin reset-password` builds never
  disables or deletes and has none; an `Admin` without a releaser releases nothing. `ReleaseOwner` also returns what
  to run once that transaction committed (the counters and log lines of the status changes), which `Admin` runs
  after the commit. An error of the release rolls the disable or the delete back. It takes the locks in grouping's
  order: the counter row, then the Alert Groups in id order. Acknowledge locks the row of its User `FOR SHARE` while
  the User is active, before the Alert Group, so that an Acknowledge racing a disable or a delete is refused (`403`)
  instead of leaving that User as the Owner. For each acknowledged Alert Group the user owns, the dispatcher runs a system transition — actor
  `system`, Transport `system`, no Permission and no Audit log entry of its own, since `user.disabled` or
  `user.deleted` records the cause — to firing without an Owner, recording `unacknowledged` with `reason`
  `owner_disabled` or `owner_deleted`, `previous_owner_user_id` the user, Loud and without Mentions (the row of
  C-09.FR-22). The re-render hook gives delivery its Thread reply from S-034 on, with the text of S-036; S-049 ends the
  Reminders and starts the ack timeout over, as after an Unacknowledge. A Reopen into acknowledged (S-028) whose Owner
  is no longer active reopens into firing instead, with the Loud `reopened` of a Reopen into firing; the release also
  turns the status a system-resolved Alert Group of the user would reopen into, inside its Reopen window, into firing
  (without a Timeline entry), so that enabling the user again before the Reopen gives nothing back. Enabling the user
  again changes no Alert Group.
- **Wiring** (`internal/api/server.go`, `internal/runtime/runtime.go`): the three operations join the
  implemented-operations map; the API `Config` gains the user directory; the runtime sets the releaser.
- **Defaults**: P-15 (`alert_group.note_max_length`) is confirmed or changed.

## Steps

1. Write Notes, Resolve with a Note and `add_note`. Check: `notes_test.go` covers the length limit, a Service account
   and a Note on an Alert Group whose details were removed; `events_test.go` covers the `note_added` row.
2. Write the Snooze end timer. Check: tests with a manual clock cover C-10.AC-10 and the timer removed by Unsnooze,
   Acknowledge and Resolve.
3. Write the Owner filters and the user directory. Check: `list_test.go` covers each filter; `users_test.go` covers a
   deleted user.
4. Write the release hook and its implementation. Check: `release_test.go` covers disable, delete, enable and the Reopen
   of a released Alert Group; `admin_test.go` covers the hook called inside both transactions and a failing release
   rolling the disable back.
5. Record P-15. Check: Verification below.

## Verification

```sh
make dev > dev.log 2>&1 &
# as in S-032: the Admin's session H and token A, the Responder "bob" with HB, the Service accounts' tokens SAT and VAT
# in S and V, the Integration "cmd", the Route "ops", ADV, NOTIFY, PUTA, AG, GET and LASTE
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake
curl -s -X PUT $FAM/groups/o2 -d '{"receiver":"cmd","route":"{}","labels":{"alertname":"DiskSlow"}}' > /dev/null
for c in e f g; do PUTA o2 $c "{\"labels\":{\"team\":\"ops\",\"cluster\":\"$c\",\"severity\":\"warning\"}}"; done
NOTIFY o2 '{"reason":"first notification"}'
GE=$(AG 'cluster%3D%22e%22'); GF=$(AG 'cluster%3D%22f%22'); GG=$(AG 'cluster%3D%22g%22')

# Notes (C-10.FR-8, C-04.AC-6, C-10.AC-18)
curl -s -b bob "$API/alert-groups/$GE" | jq -c .allowed_commands                    # ["acknowledge","resolve","snooze","add_note"]
curl -s "${S[@]}" "$API/alert-groups/$GE/notes" -d '{"body":"Restarted the disk daemon."}' | jq -c '{a: .author.name, t: .transport}'
# {"a":"robot","t":"api"}
curl -s -o /dev/null -w '%{http_code}\n' "${V[@]}" -H 'Content-Type: application/json' "$API/alert-groups/$GE/notes" -d '{"body":"x"}'   # 403
grep '"event":"command_refused"' dev.log | tail -1 | jq -c '{command, code}'                               # {"command":"add_note","code":"forbidden"}
curl -s "${A[@]}" "$API/alert-groups/$GE/notes" -d "{\"body\":\"$(printf 'x%.0s' $(seq 1 4001))\"}" | jq -r '.errors[0].code'   # too_long

# C-10.AC-10: a Snooze until T ends at T
UNTIL=$(curl -s localhost:8082/_dev/clock | jq -r '.now | sub("\\.[0-9]+"; "") | fromdateiso8601 + 600 | todate')
curl -s "${A[@]}" -X POST "$API/alert-groups/$GF/snooze" -d "{\"until\":\"$UNTIL\"}" | jq -r .alert_group.snooze_until   # UNTIL
PUTA o2 f2 '{"labels":{"team":"ops","cluster":"f","severity":"warning","n":"2"}}'; NOTIFY o2 '{"reason":"new alerts added"}'
ADV 610; GET $GF | jq -c '{status, owner}'                                                                 # {"status":"firing","owner":null}
curl -s "${A[@]}" "$API/alert-groups/$GF/timeline?limit=1" | jq -c '.items[0] | {event, loudness, mentions, n: (.fingerprints | length)}'
# {"event":"snooze_ended","loudness":"loud","mentions":["snooze_ended"],"n":1}

# C-10.AC-16: Resolve with a Note
curl -s "${A[@]}" -X POST "$API/alert-groups/$GF/resolve" -d '{"note":"Rolled back."}' > /dev/null
curl -s "${A[@]}" "$API/alert-groups/$GF/timeline?limit=2" | jq -c '[.items[].event]'                     # ["note_added","resolved"]

# C-10.AC-19 and the Owner filters
curl -s "${H[@]}" -X POST "$API/alert-groups/$GE/acknowledge" > /dev/null
curl -s "${HB[@]}" -X POST "$API/alert-groups/$GG/acknowledge" > /dev/null
curl -s "${A[@]}" "$API/alert-groups?owner=me" | jq '[.items[].owner.login] | unique'                      # ["admin@example.org"]
curl -s "${A[@]}" "$API/alert-groups?owner=none&status=resolved" | jq -c '[.items[].owner] | unique'     # [null]
curl -s "${S[@]}" "$API/alert-groups?owner=me" | jq -r '.errors[0].code'                                   # unsupported
curl -s "${A[@]}" -X POST "$API/alert-groups/$GE/snooze" -d '{"no_end":true}' > /dev/null
curl -s "${A[@]}" "$API/alert-groups?snoozed_no_end=true" | jq '[.items[].snooze_until] | unique'         # [null]

# C-03.FR-13, C-03.AC-27: disabling an Owner releases the acknowledgement; enabling the user again does not restore it
BOB=$(curl -s "${A[@]}" "$API/user-directory?q=bob" | jq -r '.items[0].id')
curl -s "${H[@]}" -X POST "$API/users/$BOB/disable" > /dev/null
GET $GG | jq -c '{status, owner}'                                                                         # {"status":"firing","owner":null}
curl -s "${A[@]}" "$API/alert-groups/$GG/timeline?limit=1" | jq -c '.items[0] | {event, a: .actor.kind, r: .reason, p: .previous_owner.login, loudness, mentions}'
# {"event":"unacknowledged","a":"system","r":"owner_disabled","p":"bob","loudness":"loud","mentions":[]}
curl -s "${A[@]}" "$API/audit-log?resource_id=$BOB" | jq -c '[.items[].action]'                              # ["user.disabled", …]   (no alert_group.* entry)
curl -s "${H[@]}" -X POST "$API/users/$BOB/enable" > /dev/null; GET $GG | jq -r .status                  # firing
curl -s -o /dev/null -w '%{http_code}\n' "${H[@]}" -X DELETE "$API/users/$BOB"                            # 204
curl -s "${V[@]}" "$API/user-directory" | jq -c --arg id "$BOB" '.items[] | select(.id == $id) | {deactivated}'
# {"deactivated":true}
```

C-10.AC-20 (a Note across both retention periods) runs as an end-to-end test with the development clock, and so does
the release by `deleteUser` (`owner_deleted`) with a system-resolved Alert Group of the user reopening into firing; the
pull request records their output. The retention task runs on a clock move (S-029), so the advances purge at once.

## Open questions

None.

## Notes

- Suggested commit: `feat(groups): add notes, snooze ends, owner filters and the release of disabled owners`.
- Split from S-032 before implementation: S-032 keeps the Commands, their refusals, `allowed_commands` and bulk; this
  story takes Notes, the Snooze end timer, the user directory, the Owner filters and the Owner release.
- P-15 (`alert_group.note_max_length`) is confirmed or changed here; `NoteInput` and `ResolveRequest` carry the same
  maximum.
- The Loud `snooze_ended` and the Loud `unacknowledged` of the release are system transitions, not results of a
  command (C-10.FR-5).
- The release goes through an interface that `users` declares as its consumer (ADR-0016), so `users` never imports
  `groups`.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-10.FR-1 | partial | Add Note and the Note of Resolve; the other commands are S-032 |
| C-10.FR-6 | partial | the Snooze end; together with S-032, the dialog and the messenger durations are S-033, S-061 and S-042 |
| C-10.FR-8 | partial | the API; the Note box is S-033 |
| C-10.FR-12 | partial | `alert_group.note_added`; the Command actions are S-032 |
| C-10.FR-13 | partial | the API; the filters and columns of the list are S-033 |
| C-10.FR-15 | partial | the `note_added` row; the Command rows are S-032 |
| C-10.FR-16 | partial | `add_note`; together with S-032, `still_on_it` is S-049 |
| C-10.AC-10 | full | |
| C-10.AC-16 | partial | the `note_added` row and Resolve with a Note; together with S-032 |
| C-10.AC-18 | partial | `add_note`; together with S-032 |
| C-10.AC-19 | full | |
| C-10.AC-20 | full | |
| C-09.FR-8 | partial | the Snooze end; the ack timeout starting over is S-049 |
| C-09.FR-11 | partial | Notes in the Timeline |
| C-09.FR-12 | full | together with S-028 |
| C-09.FR-13 | partial | the filters Owner and "snoozed with no end" |
| C-09.FR-16 | full | together with S-029 and S-030 |
| C-09.FR-22 | partial | the `unacknowledged` row of a disabled or deleted Owner |
| C-04.AC-6 | partial | a Note from a Service account; together with S-032 |
| C-03.FR-13 | partial | the release of the acknowledgements of disabled and deleted users; deleted users show as deactivated; the Reminders and the ack timeout are S-049 |
| C-03.AC-27 | partial | the release, its Timeline entry and enabling again; the Thread message, the ack timeout and the Reminders are S-049 |
