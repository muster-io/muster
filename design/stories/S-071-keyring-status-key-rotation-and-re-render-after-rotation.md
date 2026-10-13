---
id: S-071
title: Keyring status, key rotation and re-render after rotation (BE)
capability: C-20
kind: be
layer: L1
depends_on: [S-055, S-069]
covers: [C-20.FR-6, C-20.FR-7, C-20.AC-2, C-20.AC-5, C-02.FR-15, C-17.FR-6]
files_touched:
  - api/openapi.yaml
  - internal/keyring/status.go
  - internal/keyring/status_test.go
  - internal/keyring/rotate.go
  - internal/keyring/replicas.go
  - internal/keyring/query.sql
  - internal/keyring/rotate_integration_test.go
  - internal/delivery/rerender.go
  - internal/delivery/outcomes.go
  - internal/delivery/query.sql
  - internal/delivery/rerender_integration_test.go
  - internal/delivery/lockorder_integration_test.go
  - internal/leader/tasks.go
  - internal/runtime/runtime.go
  - internal/cli/secrets.go
  - internal/cli/cli.go
  - internal/cli/secrets_test.go
  - internal/doctor/doctor.go
  - internal/doctor/doctor_integration_test.go
  - internal/api/organization.go
  - internal/api/organization_test.go
  - internal/api/server.go
  - internal/api/server_test.go
  - internal/archlint/secretleak.go
  - internal/logging/events.go
  - test/e2e/harness.go
  - test/e2e/keyring_test.go
acceptance:
  - "[C-20.FR-6] `getKeyring` lists every key id held by this or a live replica, which one is active, which live replicas hold or lack each, and for each other key whether a Secret or an open Root message still depends on it (`needed`) and whether it can be removed (`removable`); only a key that was active before is ever `removable`, and a key that was never active has the state `not_active_yet`."
  - "[C-20.FR-7, C-20.AC-2, C-02.FR-15] `muster secrets rotate-key --activate <id> --actor <name>` refuses with \"Replicas {names} do not hold key {id} yet.\" and exit 1 while a live replica lacks the key, and exits 2 without `--actor`; once every live replica holds it, it activates the key, re-encrypts every Secret and the key canary, records `keyring.key_activated` and sends the `organization` and `system-status` hints; replicas switch without a restart, the Leader's task `rerender_after_rotation` then recomputes the Desired state of every open Alert Group whose buttons another key signed, and the old key becomes `removable` once the open Root messages are edited."
  - "[C-20.AC-5] After the activation, the open Root messages in the fake Mattermost servers and the fake Telegram server are edited with buttons signed by the new key, and a press on them runs the command."
  - "[C-17.FR-6] After an activation and the removal of the old key from every replica, a press on a Reminder button sent before the activation is answered \"This button has expired; use the buttons on the Alert Group's message\" and changes nothing."
verify: "make ci test-integration e2e"
operator_attention: false
issue: 215
---

# S-071. Keyring status, key rotation and re-render after rotation (BE)

## Scope

**IN**

- The Keyring status in the API and in `muster doctor`, with the state of keys that were never active.
- `muster secrets rotate-key`, with re-encryption, the replica switch and the live hints.
- The Leader task `rerender_after_rotation` that re-signs the buttons of open Root messages, in the dispatcher's lock
  order.
- The expiry of Reminder buttons after a real rotation (C-17.FR-6, repeated from S-068).

**OUT**

- Organization settings, values seen and the outbound policy (S-055, from which this story was split).
- The Keyring page (S-056).
- The key rotation section of the documentation, with the note on CSRF tokens (S-060).

## Contracts

- **Operations implemented**: `getKeyring` (`organization:read`). Schemas: `Keyring`, `KeyringKey`. Spec first, in
  this pull request: `KeyringKey` gains the required `state` — `active`, `not_active_yet`, `needed` or `removable` — and
  the description of `removable` says that only a key that was active before can be removable.
- **Keyring status** (C-20.FR-6; `keyring/status.go`): the keys are the ids of this replica's Keyring and of every live
  replica's record; `active` from `keyring_state`; `held_by` and `missing_on` over live replicas; `needed` when a row of
  `encrypted_values` carries the key id, or a delivery of an open Alert Group that is not `deleted_in_messenger` has it
  as `actual_button_key_id` (a Root message gone from the messenger depends on nothing). A key was **active before**
  when it is the active key, when it is the before or after value of `/active_key_id` in a `keyring.key_activated`
  Audit entry, or when it is `needed` (only an active key encrypts or signs). `removable` when it was active before and
  is neither active nor needed. `state` is `active`, else `needed`, else `removable`, else `not_active_yet` — a key
  added for a rotation that has not run yet, which C-20 tells the operator to activate, never to remove. Deriving
  "active before" from the Audit log needs no migration; an activation older than `retention.audit_log` is gone from it,
  so a key rotated out longer ago and still listed shows as `not_active_yet` — it is not needed either way.
  `muster doctor`'s `keyring` check (S-008) uses the same rule: "key {id} is no longer used and can be removed from
  MUSTER_SECRET_KEYS" for a removable key, "key {id} is not active yet" for a key never active.
- **Rotation** (C-20.FR-7, ADR-0011 as amended; `muster secrets rotate-key --activate <id> --actor <name>`,
  `internal/cli/secrets.go`, `keyring/rotate.go`): exit 2 without `--actor`; exit 1 when this process does not hold the
  key, or when a live replica lacks it, printing "Replicas {names} do not hold key {id} yet."; exit 0 with "key {id} is
  already active" when it is. Otherwise, under the migration advisory lock and in one transaction: the active key and
  `activated_at` change and the canary is re-encrypted with it (`keyring_state` `CHECK`); every field listed by
  `encrypted_values` is decrypted with its key and encrypted with the new one, keeping its `*_updated_at` (for the
  previous Signing secret, `previous_signing_secret_since`); `keyring.key_activated` is recorded with the actor `cli`,
  the name, the diff of `/active_key_id`, the count of re-encrypted fields and the count of open deliveries whose
  `desired_button_key_id` is another key; the transaction sends the `organization` and `system-status` hints
  (`db.NotifyHint`) and `NOTIFY muster_keyring`, all delivered when it commits. The CLI renders nothing: rendering needs
  the runtime's renderer, templates and adapters, which a CLI process does not build. Replicas switch to the new active
  key on that notification and at their next key record refresh, without a restart (`keyring/replicas.go`); a replica
  that sees an active key it does not hold stops, as in S-007.
- **Re-render after a rotation** (C-20.FR-7, AC-5; ADR-0007 as amended): the Leader task `rerender_after_rotation`
  (`leader/tasks.go`) wakes on `muster_keyring`, when its replica starts leading and on the maintenance interval as the
  fallback of a lost notification. In batches, `delivery.RerenderOpen` (`delivery/rerender.go`) recomputes the Desired
  state of every delivery of an open Alert Group whose `desired_button_key_id` is not the active key and that has a
  Root message to edit (`deleted_in_messenger`, `withheld` and `retired` deliveries are left alone), so the renderer
  signs the buttons with the new key and the delivery worker edits the Root messages within the limiters. After each
  batch it sends the `organization` hint.
- **Lock order** (D307; `delivery/rerender.go`, `delivery/query.sql`): `RerenderOpen` works one Alert Group per
  transaction in the dispatcher's order — the Alert Group row first, `FOR NO KEY UPDATE SKIP LOCKED`, then its
  deliveries as `Enqueue` locks them — and never locks a delivery before its Alert Group, as the `publishRoute` path of
  `delivery/membership.go` does for Route changes. An Alert Group a Command holds is skipped: that Command's own
  re-render signs with the active key, and whatever a replica signed with the old key before it switched is selected
  again by the next run. A delivery held by a Storm (`held_by_storm_id` set) is never re-rendered: the Storm summary
  stands for it, and its Root message is rendered with the active key when the Storm releases it.
  `lockorder_integration_test.go` gains a case that runs `RerenderOpen` against an acknowledgement of the same Alert
  Group and against the worker preparing an edit of its delivery; neither may end in a deadlock.
- **Safe to run twice** (ADR-0007): a delivery already re-signed is no longer selected, and two overlapping Leaders
  lock the same Alert Group in turn; the second finds nothing to do. Thread reply buttons, such as those of Reminders,
  are not re-rendered and expire once the old key is removed (C-17.FR-6, S-068).
- **Last re-signed message** (`delivery/outcomes.go`): when the outcome of an edit replaces an `actual_button_key_id`
  that is not the active key and no other delivery of an open Alert Group that is not `deleted_in_messenger` still
  carries that key, the outcome's transaction sends the `organization` hint, so the Keyring page shows the key as
  removable without a reload.
- **Secrets in errors and logs** (lint 5; `internal/archlint/secretleak.go`): a probe `key_rotation` runs the
  rotation with the secrets as master keys and as encrypted field values — a field that fails to decrypt, a key the
  process does not hold, a refused activation — and checks that no secret reaches a log line, the CLI output or a
  returned error.
- **Development mode**: a `MUSTER_SECRET_KEYS` given to `muster dev` replaces the development key (S-004), so a
  rotation against the development database lists both keys explicitly and runs the CLI as `muster dev secrets
  rotate-key` with the same environment. On a database that already exists the active key comes from `keyring_state`,
  not from the order of the list; the order only matters for a new database, whose first key becomes active.
- **Operations without a story**: with `getKeyring` in the `implemented` map of `internal/api/server.go`, every
  operation of the app listener has a story, so `TestNotFoundAndNotImplemented` (`server_test.go`) checks the `501`
  answer as a unit test of `notImplemented` with an operation id that the specification does not have.
- **Log events**: `key_activated` (WARN: `from`, `to`, `actor`), `key_switched` (INFO: `active`) on every replica.
- **Defaults**: `replica.key_record_refresh`, `replica.live_expiry`, `retention.audit_log`.

## Steps

1. Change `KeyringKey` in the specification and write the Keyring status. Check: `status_test.go` covers every
   `state`, a never-activated key and a `deleted_in_messenger` delivery; `make generate-check` passes.
2. Write the rotation with re-encryption, the hints and the replica switch, and the CLI. Check:
   `rotate_integration_test.go` with two replicas covers the refusal, the activation, the canary, every encrypted field,
   the hints and the switch without a restart; `secrets_test.go` covers the exit codes; the lint-5 probe passes.
3. Write `RerenderOpen`, the Leader task and the hint of the last re-signed message. Check:
   `rerender_integration_test.go` covers selecting only deliveries signed by another key, skipping Storm-held and
   `deleted_in_messenger` deliveries and a second run that changes nothing; `lockorder_integration_test.go` passes.
4. Extend `muster doctor`. Check: `doctor_integration_test.go` covers the removable and the not-yet-active lines.
5. Write the end-to-end test. Check: Verification below.

## Verification

```sh
K0=bXVzdGVyLWRldi1vbmx5LWtleS1ub3QtYS1zZWNyZXQ=        # the development key of S-004
K1=$(openssl rand -base64 32)
export MUSTER_SECRET_KEYS="$K0,$K1"                      # replaces the development default, so it lists both keys
make dev > dev.log 2>&1 &
# As in S-053 and S-069: the Admin's session (`jar`, `H`), the Responder "bob" with his Mattermost link made in S-051,
# on the Route "t" the Mattermost Destination "alerts" (Connection "mm", C), the Mattermost Destination "alerts-2" on the
# second server (Connection "mm-2" at 127.0.0.1:18066) and the Telegram Destination "tg"; the Integration "lab" with
# FIRE, PUTA, NOTIFY, ADV and AG of S-049; the CLI runs as `./bin/muster dev <subcommand>` with the development
# defaults and the exported MUSTER_SECRET_KEYS; the Route "t" has the On-call profile, so a Reminder comes 4 hours
# after Acknowledge
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake; FMM=127.0.0.1:18065/_fake; FMM2=127.0.0.1:18066/_fake
FIRE() { curl -s -X PUT $FAM/groups/$1 -d "{\"receiver\":\"lab\",\"route\":\"{}\",\"labels\":{\"alertname\":\"Rot\"}}" > /dev/null
  PUTA $1 a "{\"labels\":{\"team\":\"t\",\"host\":\"$1\",\"severity\":\"$2\"}}"; NOTIFY $1 '{"reason":"first notification"}'; AG "host%3D%22$1%22"; }

# C-20.FR-6: the Keyring before the rotation; the new key is not active yet, never removable
curl -s -b jar $API/organization/keyring | jq -c '[.keys | sort_by(.active | not)[] | {active, held: (.held_by | length), needed, removable, state}]'
# [{"active":true,"held":1,"needed":true,"removable":false,"state":"active"},{"active":false,"held":1,"needed":false,"removable":false,"state":"not_active_yet"}]
./bin/muster dev doctor | grep -c 'is not active yet'       # 1
K0ID=$(curl -s -b jar $API/organization/keyring | jq -r '.keys[] | select(.active) | .id')
K1ID=$(curl -s -b jar $API/organization/keyring | jq -r '.keys[] | select(.active | not) | .id')

# C-20.FR-7, AC-2: a refused activation
psql "$MUSTER_DATABASE_URL" -qc "INSERT INTO replicas (replica_id, hostname, version, key_ids, started_at, refreshed_at)
  VALUES ('ghost-1', 'ghost', 'test', ARRAY['$K0ID'], now(), now())"                 # replica records use the real clock
./bin/muster dev secrets rotate-key --activate $K1ID; echo "exit=$?"                 # … --actor is required … exit=2
./bin/muster dev secrets rotate-key --activate $K1ID --actor ops; echo "exit=$?"
# Replicas ghost-1 do not hold key <K1ID> yet.
# exit=1
psql "$MUSTER_DATABASE_URL" -qc "DELETE FROM replicas WHERE replica_id = 'ghost-1'"

# an open Alert Group in all three Destinations, and a Reminder sent before the activation
OPEN=$(FIRE rot critical)
curl -s "${H[@]}" -X POST $API/alert-groups/$OPEN/acknowledge > /dev/null; ADV 14400; sleep 1
OROOT=$(curl -s $FMM/posts | jq -r --arg g "$OPEN" '.[] | select(.root_id == "" and (.props.attachments[0].title_link | test($g))) | .id')
RP=$(curl -s $FMM/posts | jq -r --arg r "$OROOT" '[.[] | select(.root_id == $r and ((.props.attachments[0].actions // []) | length) == 2)] | last | .id')
RCTX=$(curl -s $FMM/posts | jq -c --arg rp "$RP" '.[] | select(.id == $rp) | .props.attachments[0].actions[0].integration.context')

# C-20.FR-7: the activation, with its hints
(timeout 20 curl -sN -b jar $API/live-updates > sse.txt &); sleep 1
./bin/muster dev secrets rotate-key --activate $K1ID --actor ops; echo "exit=$?"    # exit=0
psql "$MUSTER_DATABASE_URL" -Atc "SELECT count(*) FROM encrypted_values WHERE key_id <> '$K1ID'"   # 0
sleep 3; curl -s -b jar $API/organization/keyring | jq -c --arg k "$K0ID" '.keys[] | select(.id == $k) | {active, needed, removable, state}'
# {"active":false,"needed":false,"removable":true,"state":"removable"}
grep -o '"type":"[a-z-]*"' sse.txt | sort -u | grep -cE 'organization|system-status'   # 2
curl -s -b jar "$API/audit-log?action=keyring.key_activated&limit=1" | jq -c '.items[0] | {k: .actor.kind, n: .actor.name}'   # {"k":"cli","n":"ops"}
grep -c '"event":"key_switched"' dev.log                    # 1
./bin/muster dev doctor | grep '^OK keyring'                # OK keyring: key <K0ID> is no longer used and can be removed from MUSTER_SECRET_KEYS

# C-20.AC-5: every open Root message — both Mattermost servers and Telegram — is re-signed, and a press works
psql "$MUSTER_DATABASE_URL" -Atc "SELECT string_agg(DISTINCT dst.name, ',' ORDER BY dst.name) FROM deliveries d
  JOIN destinations dst ON dst.id = d.destination_id JOIN alert_groups g ON g.id = d.alert_group_id
  WHERE g.public_id = '$OPEN' AND d.actual_button_key_id = '$K1ID'"                   # alerts,alerts-2,tg
psql "$MUSTER_DATABASE_URL" -Atc "SELECT count(*) FROM deliveries d JOIN alert_groups g ON g.id = d.alert_group_id
  WHERE g.status <> 'resolved' AND d.actual_button_key_id <> '$K1ID'"                 # 0
curl -s $FMM2/posts | jq -r --arg g "$OPEN" '.[] | select(.root_id == "" and (.props.attachments[0].title_link | test($g))) | .props.attachments[0].actions[0].integration.context.key_id' | grep -c "$K1ID"   # 1
ROOT=$(curl -s $FMM/posts | jq -r --arg g "$OPEN" '.[] | select(.root_id == "" and (.props.attachments[0].title_link | test($g))) | .id')
curl -s $FMM/posts | jq -r --arg r "$ROOT" '.[] | select(.id == $r) | .props.attachments[0].actions[0].integration.context.key_id' | grep -c "$K1ID"   # 1
curl -s -X POST $FMM/press -d "{\"post_id\":\"$ROOT\",\"action_id\":\"ack\",\"user_id\":\"u-bob\"}" > /dev/null; sleep 1
curl -s -b jar $API/alert-groups/$OPEN | jq -r .owner.login  # bob

# C-17.FR-6: restart with the new key only; the Reminder sent before the activation has expired. The fake servers
# restart empty with muster dev, so the press is the callback the fake would have sent, with the context saved above.
kill %1; wait; export MUSTER_SECRET_KEYS="$K1"; make dev > dev.log 2>&1 & sleep 10
curl -s -X POST localhost:8081/api/v1/callbacks/mattermost/$C -d "{\"user_id\":\"u-bob\",\"channel_id\":\"ch-alerts\",\"post_id\":\"$RP\",\"context\":$RCTX}" | jq -r .ephemeral_text
# This button has expired; use the buttons on the Alert Group's message
```

`test/e2e/keyring_test.go` repeats these steps in the harness mode of S-004 with the fakes in the test process, with
two replicas started as `muster dev --replica` with explicit keys: the second replica starts without K1 and the
activation is refused naming it; after it restarts with both keys the activation succeeds and both replicas switch
without a restart; the Root messages in both fake Mattermost servers and the fake Telegram server are edited with
buttons signed by K1 (the Telegram `callback_data` carries K1's prefix); after both restart with K1 only, startup
passes the canary, a press on a Root message works, and a press on a Reminder button sent before the activation is
answered "This button has expired; use the buttons on the Alert Group's message" (C-17.FR-6). It also covers a Leader
handover during `rerender_after_rotation`, after which every open Root message is still re-signed once, and a Storm
that holds an Alert Group during the rotation, whose delivery is rendered with K1 when the Storm releases it.

## Open questions

None.

## Notes

- Suggested commit: `feat(keyring): show the keyring status and rotate the master key`.
- Tests that need PostgreSQL are `*_integration_test.go` files with the `integration` build tag, which
  `make test-integration` runs; the other test files stay unit tests.
- Split from S-055 by the plan review of the Operations phase, before its implementation, because with the files that
  review added S-055 touched about 40 files. The re-render moved from the CLI to the Leader with D298; ADR-0007
  (a new Leader task) and ADR-0011 (who re-encrypts and who re-renders) are amended for it.
- Re-encryption runs in one transaction; at L1 sizes — a few hundred Secrets — it takes well under a second. The edits
  themselves follow through the delivery worker within the limiters.
- Master key rotation re-encrypts the three outgoing webhook secret fields: the Signing secret, the previous Signing
  secret and the Secrets. The test of S-044 re-encrypts them by hand because rotation did not exist yet; this story
  covers them with real rotation.
- CSRF tokens carry the id of the key that signed them and verify while that key is held (`internal/auth/csrf.go`);
  S-060 documents what an open page does after the old key is removed.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-20.FR-6 | partial | the API and `muster doctor`; the Keyring page is S-056 |
| C-20.FR-7 | full | |
| C-20.AC-2 | partial | the refusal, the activation and `removable` in the API; the Keyring page is S-056 |
| C-20.AC-5 | full | |
| C-02.FR-15 | partial | `muster secrets rotate-key` |
| C-17.FR-6 | partial | Reminder buttons expire after a real rotation; the rule is S-068 |
