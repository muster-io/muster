---
id: S-007
title: Keyring, key canary, replica key records and Organization defaults (BE)
capability: C-02
kind: be
layer: L1
depends_on: [S-006]
covers: [C-02.FR-7, C-02.FR-8, C-02.FR-23, C-02.AC-1, C-02.AC-6, C-01.FR-5]
files_touched:
  - internal/keyring/keyring.go
  - internal/keyring/provider.go
  - internal/keyring/canary.go
  - internal/keyring/replicas.go
  - internal/keyring/query.sql
  - internal/keyring/keyring_test.go
  - internal/keyring/replicas_test.go
  - internal/publicid/publicid.go
  - internal/publicid/publicid_test.go
  - internal/organization/defaults.go
  - internal/organization/settings.go
  - internal/organization/query.sql
  - internal/organization/organization_test.go
  - internal/runtime/bootstrap.go
  - internal/runtime/bootstrap_test.go
  - internal/runtime/runtime.go
  - internal/runtime/runtime_test.go
  - internal/config/config.go
  - internal/config/config_test.go
  - internal/devmode/devmode.go
  - internal/archlint/secretleak.go
  - internal/logging/events.go
  - sqlc.yaml
  - deploy/compose/.env.example
  - Makefile
  - AGENTS.md
acceptance:
  - "[C-02.FR-7, C-02.AC-1] With a Keyring that cannot decrypt the key canary the replica exits non-zero and logs `key_canary_failed` with \"master key does not match the database\"; with the right Keyring it becomes ready."
  - "[C-02.FR-7] On a new database the first key of `MUSTER_SECRET_KEYS` becomes active and the canary is written under the migration lock, once, even when two replicas start at the same moment; adding a second key later leaves the active key unchanged."
  - "[C-02.FR-7] A key id is derived from the key material and is the same on every replica and after every restart; the material itself is never stored or logged."
  - "[C-02.FR-7, C-02.AC-6] Without any key, with the placeholder of the compose example, or — for the server command outside development mode (`muster dev`, also with `--replica`) — with the development key, Muster refuses to start with an error naming `MUSTER_SECRET_KEYS`; `muster dev <subcommand>` runs with the development key; the compose example becomes ready once the placeholder is replaced."
  - "[C-02.FR-8] Each replica records the ids of the keys it holds every `replica.key_record_refresh`; a running replica that finds an active key it does not hold stops with the canary error."
  - "[C-02.FR-23] After the first start the Organization exists with the defaults of defaults.md, together with its outbound address policy (standard, empty lists); another start changes nothing. The Alert Group counter row is not created here: `groups` creates it with the first Alert Group (S-028)."
  - "[C-02.FR-7] A secret encrypted with AES-256-GCM decrypts only for the field it was written for, and the lint-5 probe finds neither the key material nor a decrypted secret in any log line or error."
verify: "make ci test-integration"
operator_attention: false
issue: 7
---

# S-007. Keyring, key canary, replica key records and Organization defaults (BE)

## Scope

**IN**

- Loading the Keyring from the environment through the key provider interface, key ids, sub-keys per purpose and
  AES-256-GCM encryption of secret fields (ADR-0011).
- The active key and the key canary in `keyring_state`, written at first start under the migration lock; the canary
  check at every start; refusing no key, the compose placeholder and — for the server command outside development
  mode — the development key.
- The replica key records in `replicas` and the stop when an active key is not held.
- The `public_id` generator and parser (ADR-0008).
- Start-up "ensure" steps under the migration lock, starting with the Organization and its outbound address policy with
  the defaults of C-02.FR-23.
- `sqlc` configuration, with the first queries.

**OUT**

- Key activation and re-encryption (`muster secrets rotate-key`) and the Keyring page (S-055).
- Pruning replica records of gone replicas and the System status list of replicas (S-008, S-053).
- Button signatures (C-12/C-13) and CSRF tokens (S-010), which use the sub-keys defined here.
- Editing Organization settings (S-012 for the TOTP policy, S-053 and S-055 for the rest).

## Contracts

- **Key provider** (ADR-0011): an interface that returns the master keys; its first implementation reads
  `MUSTER_SECRET_KEYS` (base64 keys of 32 bytes, comma-separated) or `MUSTER_SECRET_KEYS_FILE` (one key per line). A
  value that is not a base64 key of 32 bytes stops startup, naming the variable and the position in the list.
- **Key ids and sub-keys**: a key's id is derived from a SHA-256 of its material, never the material itself, and is the
  same everywhere. From each key, HKDF-SHA256 derives a sub-key per purpose — `encryption`, `button-signature` and
  `csrf` — so material for one purpose never serves another.
- **Encryption**: secret fields are encrypted with AES-256-GCM under the active key's encryption sub-key, with a random
  nonce and the field name bound as associated data; the database stores `<name>_ciphertext` and `<name>_key_id`
  (`design/db/schema.md` §1). Every key of the Keyring decrypts what it encrypted.
- **Active key and canary** (`keyring_state`): on a database without a row, the first key of the list becomes active
  and a known value encrypted with it is stored as the canary, inside the same advisory lock as migrations. Every start
  decrypts the canary; failure stops the replica with `key_canary_failed` (ERROR) and "master key does not match the
  database".
- **Refusals** (C-02.FR-7, C-02.AC-6): an empty Keyring; the placeholder value of `deploy/compose/.env.example`
  (`REPLACE-ME-with-openssl-rand-base64-32`); and the fixed development key of S-004 when the server command runs
  outside development mode. Development mode accepts that key everywhere — `muster dev`, the additional replica of
  `muster dev --replica` and the CLI as `muster dev <subcommand>` (S-004) — and other subcommands do not check for it.
  Each refusal stops startup with an error that names `MUSTER_SECRET_KEYS` and says how to generate a key
  (`openssl rand -base64 32`). The bootstrap settings (S-006) no longer require the variable themselves, so that the
  Keyring gives these errors; `muster migrate` checks the keys the same way, except for the development key. A
  `MUSTER_SECRET_KEYS` that is set replaces the development default (S-004), so it is the whole Keyring in every mode.
- **Replica key records** (C-02.FR-8, `replicas`): a replica id is chosen at process start (host name plus a random
  suffix); the row with the held key ids, version, host name and start time is written at start and refreshed every
  `replica.key_record_refresh`; a replica counts as live while its row is younger than `replica.live_expiry`. These
  times are on the real clock of S-006, so the development clock never makes a replica look gone. At each refresh the
  replica compares the active key with the keys it holds; a key it does not hold stops it with `active_key_not_held`
  (ERROR) and the canary error. A graceful shutdown deletes the replica's row.
- **`public_id`** (`internal/publicid`): a two-letter prefix per type (the table of `api/README.md`) and 12 random
  Crockford base32 characters from `crypto/rand`; output in upper case; input normalized (any case, `O`→`0`, `I`/`L`→`1`)
  and checked against the expected prefix.
- **Start-up ensure steps** (`internal/runtime/bootstrap.go`): idempotent steps that run at every start inside the
  migration advisory lock, after migrations and before serving. Later capabilities add theirs (the bootstrap Admin in
  S-010, the built-in Integration in S-021, the Default route in S-025, the built-in Link rule in S-037).
- **Organization defaults** (C-02.FR-23): the first ensure step creates, if missing, the single Organization with the
  values of defaults.md — `organization.name`, `organization.time_zone`, `organization.severity_label`,
  `organization.severity_mapping`, `organization.severity_styles`, `organization.critical_is_urgent`, `organization.instance_labels`, `retention.*`, `organization.totp_required` and
  `auth.oidc_token_grace` — its `outbound_policies` row (`organization.outbound_policy`, empty
  `organization.outbound_allowed` and `organization.outbound_denied`). It does not create the `alert_group_counters`
  row: only `internal/groups` writes that table (lint 2), and S-028 creates the row lazily with the first Alert Group.
  `internal/organization` exposes a read accessor of these settings for later capabilities.
- **sqlc**: `sqlc.yaml` reads the schema from `internal/db/migrations` and generates each package's `query.sql` into
  `internal/<package>/dbgen/` (generated, never listed in `files_touched`). This is the first story with queries, so
  `make generate` and the generated-code check of S-002 gain `sqlc generate` here. sqlc is a pinned tool built into
  `bin/tools/` like golangci-lint (without cgo, so it parses with the WebAssembly build of the PostgreSQL parser), not a
  `go.mod` tool, which keeps its dependencies out of the module.
- **Log events**: `keyring_loaded` (INFO: key ids, active key id), `key_canary_failed` (ERROR), `active_key_not_held`
  (ERROR), `replica_record_failed` (WARN: a refresh of the replica record failed and is retried at the next one),
  `organization_created` (INFO).

## Steps

1. Write the key provider, key ids, sub-keys and encryption. Check: unit tests cover id stability, sub-key separation,
   the associated-data binding and decryption with a non-active key of the Keyring.
2. Write the canary, the active key and the refusals, and call them from the start-up order. Check: integration tests
   with two replicas starting together write one canary; a wrong Keyring fails; the three refusals name the variable.
3. Write the replica records and the active-key watch. Check: an integration test with a manual clock sees the refresh,
   the live window and the stop on an unknown active key.
4. Write `internal/publicid`. Check: tests cover generation, the alphabet, normalization and prefix checks.
5. Write the ensure steps and the Organization defaults, with `sqlc.yaml`. Check: a first start creates the two rows
   with the defaults; a second start changes nothing; `make generate-check` passes.
6. Register the lint-5 probes for key material and decrypted values. Check: `make lint-arch` passes.

## Verification

```sh
make dev-db && make build
export MUSTER_DATABASE_URL='postgres://muster:muster@127.0.0.1:55432/muster?sslmode=disable' \
       MUSTER_PUBLIC_URL=http://localhost:8080 MUSTER_MIGRATE_ON_START=true
psql "$MUSTER_DATABASE_URL" -qc 'DROP SCHEMA public CASCADE; CREATE SCHEMA public'
K1="$(openssl rand -base64 32)"; K2="$(openssl rand -base64 32)"

MUSTER_SECRET_KEYS="$K1" ./bin/muster &
curl -s -o /dev/null -w '%{http_code}\n' localhost:8082/health/ready         # 200
psql "$MUSTER_DATABASE_URL" -Atc 'SELECT active_key_id = canary_key_id FROM keyring_state'
# t
psql "$MUSTER_DATABASE_URL" -Atc "SELECT name, time_zone, totp_required, retention_stored_snapshots_days,
  retention_audit_log_days, oidc_token_grace_seconds FROM organizations"
# Muster|UTC|nobody|14|365|604800
psql "$MUSTER_DATABASE_URL" -Atc 'SELECT policy, cardinality(allowed), cardinality(denied) FROM outbound_policies'
# standard|0|0
psql "$MUSTER_DATABASE_URL" -Atc 'SELECT cardinality(key_ids) FROM replicas'
# 1
kill %1; wait %1

MUSTER_SECRET_KEYS="$K2" ./bin/muster; echo "exit=$?"
# {"level":"ERROR","event":"key_canary_failed",...,"error":"master key does not match the database"}
# exit=1
MUSTER_SECRET_KEYS="$K2,$K1" ./bin/muster &
curl -s -o /dev/null -w '%{http_code}\n' localhost:8082/health/ready         # 200; the active key is still K1's id

# C-02.FR-8: an active key this replica does not hold
psql "$MUSTER_DATABASE_URL" -qc "UPDATE keyring_state SET active_key_id = 'k-unknown', canary_key_id = 'k-unknown'"
wait %1; echo "exit=$?"                                                       # within replica.key_record_refresh
# {"level":"ERROR","event":"active_key_not_held",...}
# exit=1

MUSTER_SECRET_KEYS= ./bin/muster; echo "exit=$?"
# MUSTER_SECRET_KEYS is empty: generate a key with `openssl rand -base64 32`
# exit=1

# C-02.AC-6: the compose example
cd deploy/compose && cp .env.example .env && MUSTER_IMAGE=<locally built image> docker compose up -d
docker compose logs muster | grep -m1 'placeholder'
# MUSTER_SECRET_KEYS still holds the placeholder of the compose example: generate a key with `openssl rand -base64 32`
sed -i.bak "s|REPLACE-ME-with-openssl-rand-base64-32|$(openssl rand -base64 32)|" .env && docker compose up -d
curl -s -o /dev/null -w '%{http_code}\n' localhost:8080/                      # 200
```

## Open questions

None.

## Notes

- Suggested commit: `feat(keyring): add master key keyring, key canary and organization defaults`.
- The ensure steps, not migrations, create runtime rows (`design/db/schema.md` §1), so a database created by an earlier
  version gains the rows of later capabilities at its next start.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-02.FR-7 | full | |
| C-02.FR-8 | full | pruning of gone replicas is S-008 |
| C-02.FR-23 | full | editing comes with S-012, S-053 and S-055 |
| C-02.AC-1 | full | |
| C-02.AC-6 | full | |
| C-01.FR-5 | partial | `sqlc generate` in `make generate` and the generated-code check |
