---
id: S-055
title: Organization settings, outbound policy editing, Keyring status and key rotation (BE)
capability: C-20
kind: be
layer: L1
depends_on: [S-047, S-051, S-053]
covers: [C-20.FR-1, C-20.FR-2, C-20.FR-3, C-20.FR-6, C-20.FR-7, C-20.FR-8, C-20.AC-1, C-20.AC-2, C-20.AC-3, C-20.AC-4, C-20.AC-5, C-20.AC-6, C-02.FR-15, C-02.FR-21, C-17.FR-6]
files_touched:
  - internal/organization/settings.go
  - internal/organization/severityvalues.go
  - internal/organization/outbound.go
  - internal/organization/query.sql
  - internal/organization/organization_test.go
  - internal/organization/outbound_test.go
  - internal/api/organization.go
  - internal/api/organization_test.go
  - internal/keyring/status.go
  - internal/keyring/rotate.go
  - internal/keyring/replicas.go
  - internal/keyring/query.sql
  - internal/keyring/rotate_test.go
  - internal/delivery/enqueue.go
  - internal/delivery/enqueue_test.go
  - internal/cli/secrets.go
  - internal/cli/cli.go
  - internal/cli/secrets_test.go
  - internal/doctor/doctor.go
  - internal/routing/severity.go
  - internal/logging/events.go
  - test/e2e/harness.go
  - test/e2e/organization_test.go
acceptance:
  - "[C-20.FR-1, C-20.FR-8] `updateOrganization` with `If-Match` changes the name, time zone, Severity levels with their emoji and colours, \"critical is Urgent\", Instance labels, retention periods, the TOTP policy, the token grace and the outgoing heartbeat; every change is recorded as `organization.updated` with a before/after diff, a stale `If-Match` answers `412`, and nothing answers `unsupported` any more."
  - "[C-20.AC-6] An update that sets `retention.alert_group_summaries_days` below `retention.alert_details_days` answers `422` with `retention_order` at `/retention/alert_group_summaries_days` and changes nothing; equal periods are accepted."
  - "[C-20.FR-2, C-20.AC-3] After mapping `P1` to critical, a new Alert with `severity=\"P1\"` starts an Urgent Alert Group with the level critical, while an open Alert Group whose Alerts carry `P1` keeps its status, level and urgency."
  - "[C-20.FR-1, C-20.AC-4] After Alerts with `severity=\"P5\"` arrive, `listSeverityValues` lists `P5` with its count, the level warning and `mapped: false`, over `organization.severity_values_window`."
  - "[C-20.FR-1] After the time zone becomes `Europe/Berlin`, the times in new messages are shown in that zone."
  - "[C-20.FR-3, C-20.AC-1, C-02.FR-21] `updateOutboundPolicy` with `If-Match` replaces the policy and its lists, effective at once on every replica; under the standard policy a test of an outgoing webhook to 127.0.0.1 fails with `blocked` until `127.0.0.0/8` is allowed, and one to 169.254.169.254 stays `blocked` even with `169.254.0.0/16` allowed; `getOutboundPolicy` lists the rules that always apply."
  - "[C-20.FR-6] `getKeyring` lists every key id held by this or a live replica, which one is active, which live replicas hold or lack each, and for each older key whether a Secret or an open Root message still depends on it (`needed`) and whether it can be removed (`removable`)."
  - "[C-20.FR-7, C-20.AC-2, C-02.FR-15] `muster secrets rotate-key --activate <id> --actor <name>` refuses with \"Replicas {names} do not hold key {id} yet.\" and exit 1 while a live replica lacks the key, and exits 2 without `--actor`; once every live replica holds it, it activates the key, re-encrypts every Secret and the key canary, recomputes the Desired state of every open Alert Group and records `keyring.key_activated`; replicas switch without a restart, and the old key becomes `removable` once the open Root messages are edited."
  - "[C-20.AC-5] After the activation, the open Root messages in the fake Mattermost server are edited with buttons signed by the new key, and a press on them runs the command."
  - "[C-17.FR-6] After an activation and the removal of the old key from every replica, a press on a Reminder button sent before the activation is answered \"This button has expired; use the buttons on the Alert Group's message\" (end-to-end test)."
verify: "make ci test-integration e2e"
operator_attention: false
issue: null
---

# S-055. Organization settings, outbound policy editing, Keyring status and key rotation (BE)

## Scope

**IN**

- Editing every Organization setting that exists with its defaults since S-007, with the retention rule and the values
  seen of the severity label.
- Editing the outbound address policy, effective at once.
- The Keyring status and `muster secrets rotate-key`, with re-encryption and re-signed buttons.

**OUT**

- The Organization settings pages (S-056); the TOTP policy (S-012) and the outgoing heartbeat (S-053), whose editing
  exists already.
- The key rotation and backup sections of the documentation (S-060).

## Contracts

- **Operations implemented**: `updateOrganization` for every field (the remaining `unsupported` answers of S-012 are
  removed), `getOutboundPolicy` and `updateOutboundPolicy` (`organization:read` / `organization:write`), `getKeyring`
  and `listSeverityValues` (`organization:read`). Schemas: `OrganizationBase`, `OrganizationInput`,
  `RetentionSettings`, `SeverityMappingEntry`, `SeverityLevelStyle`, `OutboundPolicy`, `Keyring`, `KeyringKey`,
  `SeverityValue`, `SeverityValueList`; hint `organization`.
- **Organization** (C-20.FR-1, FR-8; `organization/settings.go`, `organizations`): validation — `name` not empty;
  `time_zone` an IANA zone (`invalid_format`); `severity_label` a label name; `severity_mapping` values unique
  (`duplicate` at `/severity_mapping/<i>/value`); `severity_styles` exactly one entry per level with a non-empty emoji
  and a `#rrggbb` colour; `instance_labels` label names without duplicates; retention periods of at least one day, with
  `alert_group_summaries_days` ≥ `alert_details_days` (`422` `retention_order` at
  `/retention/alert_group_summaries_days`, checked before the database `CHECK`); `oidc_token_grace_seconds` ≥ 1. A
  valid update writes the row, increments `version` (the ETag), records `organization.updated` with the diff (Secrets as
  `secret_changed`) and sends `NOTIFY organization`.
- **Later evaluations only** (C-20.FR-2; `routing/severity.go`): routing, Severity levels, urgency and Replacement read
  the settings through a per-replica cache that `NOTIFY organization` invalidates; levels, urgency and statuses already
  stored are never recomputed by a settings change. Retention tasks and message rendering read the current values at
  their next run.
- **Values seen** (C-20.FR-1, AC-4; `organization/severityvalues.go`): the distinct values of the label
  `severity_label` among Alerts whose `last_seen_at` is within `organization.severity_values_window`, each with the
  number of Alerts, the level of the current mapping (an unmapped value counts as warning) and `mapped`;
  `window_days` in the answer.
- **Outbound policy** (C-20.FR-3, C-02.FR-21; `organization/outbound.go`, `outbound_policies`): `policy` `standard` or
  `strict`; `allowed` and `denied` entries must parse with the policy's own parser of S-009 (`invalid_format` at
  `/allowed/<i>` or `/denied/<i>`, `duplicate` for repeats); `always_blocked` lists the rules in code (link-local, cloud
  metadata, unspecified, multicast) and is read-only. An update records `outbound_policy.updated` with the diff and sends
  `NOTIFY outbound_policy`, which drops the per-replica cache of S-009 at once (its 10-second age stays as a fallback),
  answering S-009's open question 1.
- **Keyring status** (C-20.FR-6; `keyring/status.go`): the keys are the ids of this replica's Keyring and of every live
  replica's record; `active` from `keyring_state`; `held_by` and `missing_on` over live replicas; `needed` when a row of
  `encrypted_values` carries the key id or a delivery of an open Alert Group has it as `actual_button_key_id`;
  `removable` when it is neither active nor needed. `muster doctor`'s `keyring` check (S-008) uses the same rule and
  says "key {id} is no longer used and can be removed from MUSTER_SECRET_KEYS".
- **Rotation** (C-20.FR-7, ADR-0011; `muster secrets rotate-key --activate <id> --actor <name>`,
  `internal/cli/secrets.go`, `keyring/rotate.go`): exit 2 without `--actor`; exit 1 when this process does not hold the
  key, or when a live replica lacks it, printing "Replicas {names} do not hold key {id} yet."; exit 0 with "key {id} is
  already active" when it is. Otherwise, under the migration advisory lock and in one transaction: the active key and
  `activated_at` change and the canary is re-encrypted with it (`keyring_state` `CHECK`); every field listed by
  `encrypted_values` is decrypted with its key and encrypted with the new one, keeping its `*_updated_at`;
  `delivery.RerenderOpen` recomputes the Desired state of every open Alert Group, whose buttons the renderer now signs
  with the new key, so the delivery worker edits their Root messages; `keyring.key_activated` is recorded with the
  actor `cli`, the name, and the counts of re-encrypted fields and re-rendered deliveries; `NOTIFY keyring` follows the
  commit. Replicas switch to the new active key on that notification and at their next key record refresh, without a
  restart (`keyring/replicas.go`); a replica that sees an active key it does not hold stops, as in S-007. Thread reply
  buttons, such as those of Reminders, are not re-rendered and expire once the old key is removed (C-17.FR-6).
- **Development mode**: a `MUSTER_SECRET_KEYS` given to `muster dev` replaces the development key (S-004), so a
  rotation against the development database lists both keys explicitly — the development key first, so that it stays
  active until the rotation — and runs the CLI as `muster dev secrets rotate-key` with the same environment.
- **Log events**: `organization_updated` (INFO: `fields`), `outbound_policy_updated` (INFO), `key_activated` (WARN:
  `from`, `to`, `actor`), `key_switched` (INFO: `active`) on every replica.
- **Defaults**: `organization.*`, `retention.*`, `auth.oidc_token_grace`, `organization.outbound_policy`,
  `organization.outbound_allowed`, `organization.outbound_denied`, `replica.key_record_refresh`,
  `replica.live_expiry`.

## Steps

1. Write the Organization update with its validation and the cache invalidation. Check: `organization_test.go` covers
   every field, C-20.AC-6 and a stale `If-Match`; a routing test shows a mapping change affecting only later Alerts.
2. Write the values seen. Check: tests cover the window, the counts and unmapped values.
3. Write the outbound policy editing. Check: `outbound_test.go` covers C-20.AC-1 against the dialer of S-009 and the
   immediate effect on a second replica.
4. Write the Keyring status, the rotation with re-encryption and `RerenderOpen`, the replica switch and the CLI. Check:
   `rotate_test.go` with two replicas covers the refusal, the activation, the canary, every encrypted field and the
   switch without a restart; `secrets_test.go` covers the exit codes.
5. Write the end-to-end test. Check: Verification below.

## Verification

```sh
K0=bXVzdGVyLWRldi1vbmx5LWtleS1ub3QtYS1zZWNyZXQ=        # the development key of S-004
K1=$(openssl rand -base64 32)
export MUSTER_SECRET_KEYS="$K0,$K1"                      # replaces the development default, so it lists both keys
make dev > dev.log 2>&1 &
# As in S-053: the Admin's session (`jar`, `H`), the Responder "bob" with his Mattermost link made in S-051, the
# Mattermost Destination "alerts" (D) on the Route "t", the Integration "lab" with FIRE, PUTA, NOTIFY, ADV and AG of S-049,
# the outgoing webhook "auto" (WD) of S-044 pointing at http://127.0.0.1:18093/hook/auto; the CLI runs as
# `./bin/muster dev <subcommand>` with the development defaults and the exported MUSTER_SECRET_KEYS
API=localhost:8080/api/v1; FAM=127.0.0.1:19093/_fake; FMM=127.0.0.1:18065/_fake; CLK=localhost:8082/_dev/clock
ORG() { O=$(curl -s -b jar $API/organization); curl -s "${H[@]}" -X PUT -H "If-Match: $(jq -r .etag <<<"$O")" $API/organization -d "$(jq -c "del(.id, .etag) | $1" <<<"$O")"; }
POL() { Q=$(curl -s -b jar $API/organization/outbound-policy); curl -s "${H[@]}" -X PUT -H "If-Match: $(jq -r .etag <<<"$Q")" $API/organization/outbound-policy -d "$(jq -c "del(.etag, .always_blocked) | $1" <<<"$Q")" > /dev/null; }
TEST() { curl -s "${H[@]}" -X POST $API/destinations/$1/tests | jq -c '{ok, c: ([.steps[] | .error_class] | map(select(. != null)) | first)}'; }

# C-20.AC-6, FR-8: the retention order; equal periods; a stale If-Match
ORG '.retention.alert_group_summaries_days = 30 | .retention.alert_details_days = 90' | jq -c '{status, p: .errors[0].pointer, c: .errors[0].code}'
# {"status":422,"p":"/retention/alert_group_summaries_days","c":"retention_order"}
ORG '.retention.alert_group_summaries_days = 90 | .retention.alert_details_days = 90' | jq -c .retention   # {…,"alert_details_days":90,"alert_group_summaries_days":90,…}
curl -s "${H[@]}" -X PUT -H 'If-Match: "1"' $API/organization -d "$(curl -s -b jar $API/organization | jq -c 'del(.id, .etag)')" | jq -r .status   # 412
curl -s -b jar "$API/audit-log?action=organization.updated&limit=1" | jq -c '[.items[0].diff[].field]'   # ["retention.alert_group_summaries_days"]

# C-20.FR-1, AC-4: values seen; C-20.AC-3, FR-2: mapping P1 changes later evaluations only
FIRE() { curl -s -X PUT $FAM/groups/$1 -d "{\"receiver\":\"lab\",\"route\":\"{}\",\"labels\":{\"alertname\":\"Sev\"}}" > /dev/null
  PUTA $1 a "{\"labels\":{\"team\":\"t\",\"host\":\"$1\",\"severity\":\"$2\"}}"; NOTIFY $1 '{"reason":"first notification"}'; AG "host%3D%22$1%22"; }
GOLD=$(FIRE v1 P1); FIRE v2 P5 > /dev/null
curl -s -b jar $API/organization/severity-values | jq -c '[.items[] | select(.value == "P5" or .value == "P1") | {value, count, level, mapped}]'
# [{"value":"P1","count":1,"level":"warning","mapped":false},{"value":"P5","count":1,"level":"warning","mapped":false}]
ORG '.severity_mapping += [{"value":"P1","level":"critical"}]' > /dev/null
curl -s -b jar $API/alert-groups/$GOLD | jq -c '{status, severity_level, urgent}'   # {"status":"firing","severity_level":"warning","urgent":false}
GNEW=$(FIRE v3 P1); curl -s -b jar $API/alert-groups/$GNEW | jq -c '{severity_level, urgent}'   # {"severity_level":"critical","urgent":true}

# C-20.FR-1: the time zone of messages
ORG '.time_zone = "Europe/Berlin"' > /dev/null; FIRE v4 warning > /dev/null; sleep 1
curl -s $FMM/posts | jq -r '[.[] | select(.root_id == "")] | last | .props.attachments[0].text' | grep -cE ' CES?T'   # 1

# C-20.AC-1, FR-3: the outbound address policy
curl -s -b jar $API/organization/outbound-policy | jq -c '{policy, allowed, a: (.always_blocked | length > 0)}'   # {"policy":"standard","allowed":["127.0.0.0/8"],"a":true}
POL '.allowed = []'; TEST $WD                                # {"ok":false,"c":"blocked"}
POL '.allowed = ["127.0.0.0/8"]'; TEST $WD                   # {"ok":true,"c":null}
MD=$(curl -s "${H[@]}" $API/destinations -d '{"type":"webhook","name":"meta","mode":"events","events":{"url":"http://169.254.169.254/latest","headers":[]},"limiter":{"limit":5,"per_seconds":1}}' | jq -r .destination.id)
POL '.allowed = ["127.0.0.0/8", "169.254.0.0/16"]'; TEST $MD  # {"ok":false,"c":"blocked"}
grep -c '"event":"outbound_blocked".*169.254.169.254' dev.log   # 1

# C-20.FR-6, FR-7, AC-2: the Keyring, a refused activation, then the activation
curl -s -b jar $API/organization/keyring | jq -c '[.keys | sort_by(.active | not)[] | {active, held: (.held_by | length), needed, removable}]'
# [{"active":true,"held":1,"needed":true,"removable":false},{"active":false,"held":1,"needed":false,"removable":true}]
K0ID=$(curl -s -b jar $API/organization/keyring | jq -r '.keys[] | select(.active) | .id')
K1ID=$(curl -s -b jar $API/organization/keyring | jq -r '.keys[] | select(.active | not) | .id')
psql "$MUSTER_DATABASE_URL" -qc "INSERT INTO replicas (replica_id, hostname, version, key_ids, started_at, refreshed_at)
  VALUES ('ghost-1', 'ghost', 'test', ARRAY['$K0ID'], now(), now())"                 # replica records use the real clock
./bin/muster dev secrets rotate-key --activate $K1ID; echo "exit=$?"                 # … --actor is required … exit=2
./bin/muster dev secrets rotate-key --activate $K1ID --actor ops; echo "exit=$?"
# Replicas ghost-1 do not hold key <K1ID> yet.
# exit=1
psql "$MUSTER_DATABASE_URL" -qc "DELETE FROM replicas WHERE replica_id = 'ghost-1'"
OPEN=$(curl -s -b jar "$API/alert-groups" | jq -r '.items[0].id')
./bin/muster dev secrets rotate-key --activate $K1ID --actor ops; echo "exit=$?"    # exit=0
psql "$MUSTER_DATABASE_URL" -Atc "SELECT count(*) FROM encrypted_values WHERE key_id <> '$K1ID'"   # 0
sleep 3; curl -s -b jar $API/organization/keyring | jq -c --arg k "$K0ID" '.keys[] | select(.id == $k) | {active, needed, removable}'
# {"active":false,"needed":false,"removable":true}
curl -s -b jar "$API/audit-log?action=keyring.key_activated&limit=1" | jq -c '.items[0] | {k: .actor.kind, n: .actor.name}'   # {"k":"cli","n":"ops"}
grep -c '"event":"key_switched"' dev.log                    # 1
./bin/muster dev doctor | grep '^OK keyring'                # OK keyring: key <K0ID> is no longer used and can be removed from MUSTER_SECRET_KEYS

# C-20.AC-5: open Root messages carry buttons signed by the new key, and a press works
ROOT=$(curl -s $FMM/posts | jq -r --arg g "$OPEN" '.[] | select(.root_id == "" and (.props.attachments[0].title_link | test($g))) | .id')
curl -s $FMM/posts | jq -r --arg r "$ROOT" '.[] | select(.id == $r) | .props.attachments[0].actions[0].integration.context.key_id' | grep -c "$K1ID"   # 1
curl -s -X POST $FMM/press -d "{\"post_id\":\"$ROOT\",\"action_id\":\"ack\",\"user_id\":\"u-bob\"}" > /dev/null; sleep 1
curl -s -b jar $API/alert-groups/$OPEN | jq -r .owner.login  # bob
```

`test/e2e/organization_test.go` repeats these steps in the harness mode of S-004 with the fakes in the test process,
with two replicas started as `muster dev --replica` with explicit keys: the second replica starts without K1 and the
activation is refused naming it; after it restarts with both keys the activation succeeds and both replicas switch
without a restart; after both restart with K1 only, startup passes the canary, a press on a Root message works, and a
press on a Reminder button sent before the activation is answered "This button has expired; use the buttons on the Alert
Group's message" (C-17.FR-6). It also covers Instance labels, the token grace, a strict policy and the outbound policy
change seen by the second replica at once.

## Open questions

None.

## Notes

- Suggested commit: `feat(organization): edit organization settings and the outbound policy, rotate the master key`.
- Re-encryption runs in one transaction; at L1 sizes — a few hundred Secrets — it takes well under a second. The Desired
  state of open Alert Groups is only recomputed in that transaction; the edits themselves follow through the delivery
  worker within the limiters.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-20.FR-1 | partial | the API; the pages are S-056 |
| C-20.FR-2 | full | |
| C-20.FR-3 | partial | the API; the editor is S-056 |
| C-20.FR-6 | partial | the API and `muster doctor`; the Keyring page is S-056 |
| C-20.FR-7 | full | |
| C-20.FR-8 | full | |
| C-20.AC-1 | full | |
| C-20.AC-2 | partial | the refusal, the activation and `removable` in the API; the Keyring page is S-056 |
| C-20.AC-3 | full | |
| C-20.AC-4 | partial | the API; the Severity levels page is S-056 |
| C-20.AC-5 | full | |
| C-20.AC-6 | full | |
| C-02.FR-15 | partial | `muster secrets rotate-key` |
| C-02.FR-21 | partial | editing the policy, effective at once |
| C-17.FR-6 | partial | Reminder buttons expire after a real rotation (end-to-end test) |
