---
id: S-011
title: User administration, password setup links and the Audit log (BE)
capability: C-03
kind: be
layer: L1
depends_on: [S-010]
covers: [C-03.FR-3, C-03.FR-9, C-03.FR-11, C-03.FR-13, C-03.FR-14, C-03.FR-15, C-03.FR-26, C-03.FR-31, C-03.AC-4, C-03.AC-11, C-03.AC-17, C-03.AC-19, C-03.AC-25, C-02.FR-15]
files_touched:
  - internal/api/users.go
  - internal/api/auditlog.go
  - internal/api/sessions.go
  - internal/api/etag.go
  - internal/api/pagination.go
  - internal/api/users_test.go
  - internal/api/auditlog_test.go
  - internal/users/admin.go
  - internal/users/setup.go
  - internal/users/query.sql
  - internal/users/admin_test.go
  - internal/auth/session.go
  - internal/audit/list.go
  - internal/audit/diff.go
  - internal/audit/query.sql
  - internal/audit/audit_test.go
  - internal/cli/cli.go
  - internal/cli/admin.go
  - internal/cli/admin_test.go
  - internal/logging/events.go
  - design/prd/l1/defaults.md
  - design/prd/L1.md
acceptance:
  - "[C-03.FR-3] `POST /api/v1/users` by an Admin creates a local user and returns a single-use link `MUSTER_PUBLIC_URL/password-setup#token=…` valid for `auth.password_setup_link_ttl`; `POST /api/v1/password-setups` with that token and a password of at least `auth.password_min_length` characters sets it, and the user signs in."
  - "[C-03.AC-19] A user created with the login `Alice.Smith` signs in as `alice.smith` and keeps the login as entered; creating a user `ALICE.SMITH` answers 409 `name_taken`."
  - "[C-03.FR-26, C-03.AC-17] A setup token past its TTL answers 410 `link_expired`; a used token, or one replaced by a newer link, answers 410 `link_used`; an unknown token answers 404; none of them sets a password."
  - "[C-03.FR-3, C-03.FR-9] Disabling a user ends their sessions and refuses their sign-in with 401 until they are enabled again; changing a user's Role ends their sessions."
  - "[C-03.FR-31, C-03.AC-25] With a single active Admin, `disableUser`, `deleteUser` and an `updateUser` that gives that Admin a lower Role — from that Admin's own session too — answer 409 `last_admin` and change nothing; once a second Admin is active, the same calls succeed."
  - "[C-03.FR-13, C-03.AC-4] Deleting a user renames them `deleted-user-<id>` (name and login), erases the email and ends their sessions; the Audit log still lists their earlier actions, shown under `deleted-user-<id>`."
  - "[C-03.FR-14, C-03.FR-15] `GET /api/v1/audit-log` lists entries newest first with cursor pagination and the filters `from`, `to`, `actor`, `action`, `resource_type` and `resource_id`; changes to users carry a before/after diff."
  - "[C-03.AC-11] The Audit log API shows the `user.created` entry of the bootstrap Admin with the actor kind `bootstrap`."
  - "[C-03.FR-11, C-02.FR-15] `muster admin reset-password --actor ops <login>` sets the password read from standard input, ends the account's sessions and writes an Audit log entry with the actor `cli` named `ops`; without `--actor` it refuses and changes nothing."
  - "[NFR-13] `PUT /api/v1/users/{user_id}` with a stale `If-Match` answers 412 and without one 428; lists answer `next_cursor` and never use `OFFSET`."
verify: "make ci test-integration"
operator_attention: false
issue: 11
---

# S-011. User administration, password setup links and the Audit log (BE)

## Scope

**IN**

- Admin operations on users: list, create, read, update (name, email, Role), disable, enable, delete with
  pseudonymization, and password setup links; the last active Admin cannot be removed.
- The public password setup operation with its `410` codes.
- The Audit log read API with its filters, and before/after diffs of configuration changes.
- `muster admin reset-password --actor`.
- The shared API helpers for `ETag`/`If-Match` and cursor pagination.

**OUT**

- Resetting another user's TOTP and `muster admin reset-totp` (S-012).
- OIDC accounts: `role_locked`, `convertUserToLocal`, `local_user_only` and removing the identity on a reset (S-013).
- Revoking a deleted user's tokens (S-016) and Account links (S-051); the Users and Audit log pages (S-015).
- Releasing the acknowledgements of a disabled or deleted user, which needs Owners (S-032), and its effect on the ack
  timeout and Reminders (S-049).

## Contracts

- **Operations implemented**: `listUsers`, `createUser`, `getUser`, `updateUser`, `deleteUser`, `disableUser`,
  `enableUser`, `createPasswordSetupLink`, `completePasswordSetup`, `listAuditLog`. Schemas: `User`, `UserList`,
  `UserCreate`, `UserCreated`, `UserUpdate`, `UserStatus`, `UserSource`, `PasswordSetup`, `PasswordSetupLink`,
  `AuditEntry`, `AuditEntryList`, `AuditActor`, `AuditDiffEntry`.
- **Users** (C-03.FR-3, FR-13, `users`): logins are stored as entered and unique lowercased (`409 name_taken`).
  `listUsers` filters by `q`, `role`, `status` and `source` with cursor pagination (`api.page_size`). `updateUser` needs
  `If-Match` (`412` on a mismatch, `428` without); a Role change ends the user's sessions. `disableUser` ends the user's
  sessions and refuses sign-in until `enableUser`. Disabling, deleting or lowering the Role of the last active Admin
  answers `409 last_admin` (C-03.FR-31); the check runs under a lock in the same transaction as the change, so two Admins
  cannot remove each other at once. `deleteUser` sets the status `deleted`, renames name and login to
  `deleted-user-<public_id>`, erases the email and ends all sessions; the row stays, and Audit log rows keep pointing at
  it and show its current name.
- **Password setup links** (C-03.FR-3, FR-26, P-05, `password_setups`): created with every new local user and by
  `createPasswordSetupLink`; the token is 32 random bytes, stored as its SHA-256, carried in the URL fragment
  (`MUSTER_PUBLIC_URL/password-setup#token=<token>`), single use and valid for `auth.password_setup_link_ttl`. A new
  link supersedes the older ones. `completePasswordSetup` is public: unknown token `404`; expired `410 link_expired`;
  used or superseded `410 link_used`; password shorter than `auth.password_min_length` `422`.
- **Audit log** (C-03.FR-14, FR-15, `audit_log`): `listAuditLog` returns entries newest first, by a cursor on the time
  and id, filtered by `from`, `to`, `actor` (a user's or Service account's `public_id`), `action`, `resource_type` and
  `resource_id`. Diffs list the changed fields with their old and new values; a Secret appears only as changed
  (`secret_changed`), the rule S-013 relies on. Actions added: `user.created`, `user.updated`, `user.role_changed`,
  `user.disabled`, `user.enabled`, `user.deleted`, `user.password_setup_link_created`, `user.password_set`,
  `user.password_reset`.
- **Emergency CLI** (C-03.FR-11, ADR-0010): `muster admin reset-password --actor <name> <login>` reads the new password
  from standard input (without echo on a terminal), sets it, ends the account's sessions and writes `user.password_reset`
  with the actor kind `cli` and the given name and the Transport `cli`. Without `--actor` it exits 2 and changes
  nothing.
- **API helpers**: `ETag` and `If-Match` from each row's `version`; cursors that encode the last sort key, never
  `OFFSET` (a test checks the queries).

## Steps

1. Write the ETag and cursor helpers. Check: tests cover `412`, `428` and stable cursors across inserts.
2. Write the user operations with their session endings. Check: integration tests cover every operation, the
   case-insensitive uniqueness, the sessions ending on disable, Role change and delete, and the `last_admin`
   refusals.
3. Write the password setup links and the public operation. Check: tests with a manual clock cover `link_expired`,
   `link_used` after use and after a newer link, and `404`.
4. Write the diff helper and the Audit log list. Check: tests cover every filter, newest-first order and the name of a
   deleted actor.
5. Write `muster admin reset-password`. Check: tests cover the refusal without `--actor`, the session ending and the
   Audit log entry.
6. Confirm or change P-05. Check: defaults.md and L1.md say so.

## Verification

```sh
make dev &
curl -s -c jar -H 'Content-Type: application/json' \
  -d '{"login":"admin@example.org","password":"muster-dev-password"}' localhost:8080/api/v1/sessions > /dev/null
CSRF=$(curl -s -b jar localhost:8080/api/v1/sessions/current | jq -r .csrf_token)
H=(-b jar -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json')

curl -s "${H[@]}" -d '{"name":"Alice Smith","login":"Alice.Smith","role":"responder"}' \
  localhost:8080/api/v1/users | tee /tmp/alice.json | jq -r .password_setup_link.url
# http://localhost:8080/password-setup#token=…
TOKEN=$(jq -r '.password_setup_link.url | split("#token=")[1]' /tmp/alice.json); ALICE=$(jq -r .user.id /tmp/alice.json)
curl -s -o /dev/null -w '%{http_code}\n' -H 'Content-Type: application/json' \
  -d "{\"token\":\"$TOKEN\",\"password\":\"alice-password-1\"}" localhost:8080/api/v1/password-setups
# 204
curl -s -H 'Content-Type: application/json' -d "{\"token\":\"$TOKEN\",\"password\":\"alice-password-2\"}" \
  localhost:8080/api/v1/password-setups | jq -r .code
# link_used
curl -s -c alice -H 'Content-Type: application/json' -d '{"login":"alice.smith","password":"alice-password-1"}' \
  localhost:8080/api/v1/sessions | jq -r .user.login
# Alice.Smith
curl -s "${H[@]}" -d '{"name":"Other","login":"ALICE.SMITH","role":"viewer"}' localhost:8080/api/v1/users | jq -r .code
# name_taken

# an expired link
curl -s "${H[@]}" -X POST "localhost:8080/api/v1/users/$ALICE/password-setup-links" | jq -r .url > /tmp/link
psql "$MUSTER_DATABASE_URL" -qc "UPDATE password_setups SET expires_at = expires_at - interval '25 hours' WHERE used_at IS NULL AND superseded_at IS NULL"
curl -s -H 'Content-Type: application/json' -d "{\"token\":\"$(cut -d= -f2 /tmp/link)\",\"password\":\"alice-password-3\"}" \
  localhost:8080/api/v1/password-setups | jq -r .code
# link_expired

# disable, enable, delete
curl -s "${H[@]}" -X POST "localhost:8080/api/v1/users/$ALICE/disable" | jq -r .status          # disabled
curl -s -o /dev/null -w '%{http_code}\n' -b alice localhost:8080/api/v1/me                         # 401
curl -s "${H[@]}" -X POST "localhost:8080/api/v1/users/$ALICE/enable" | jq -r .status           # active
curl -s -c alice -H 'Content-Type: application/json' -d '{"login":"alice.smith","password":"alice-password-1"}' \
  localhost:8080/api/v1/sessions > /dev/null
curl -s -o /dev/null -w '%{http_code}\n' "${H[@]}" -X DELETE "localhost:8080/api/v1/users/$ALICE"  # 204
curl -s -o /dev/null -w '%{http_code}\n' -b alice localhost:8080/api/v1/me                         # 401
curl -s -b jar "localhost:8080/api/v1/audit-log?actor=$ALICE" | jq -r '.items[] | "\(.action) \(.actor.name)"'
# session.signed_in deleted-user-SR…
# session.signed_in deleted-user-SR…
curl -s -b jar 'localhost:8080/api/v1/audit-log?action=user.created' | jq -r '.items[-1] | "\(.actor.kind) \(.resource_name)"'
# bootstrap admin

# the last active Admin (C-03.AC-25)
ADMIN=$(curl -s -b jar localhost:8080/api/v1/me | jq -r .user.id)
curl -s "${H[@]}" -X POST "localhost:8080/api/v1/users/$ADMIN/disable" | jq -r .code             # last_admin

# emergency CLI against the development database, with its defaults (muster dev <subcommand>, S-004)
curl -s "${H[@]}" -d '{"name":"Bob","login":"bob","role":"viewer"}' localhost:8080/api/v1/users > /dev/null
echo 'bob-new-password' | ./bin/muster dev admin reset-password bob; echo "exit=$?"
# --actor is required
# exit=2
echo 'bob-new-password' | ./bin/muster dev admin reset-password --actor ops bob; echo "exit=$?"
# exit=0
curl -s -b jar 'localhost:8080/api/v1/audit-log?action=user.password_reset' | jq -c '.items[0] | {kind: .actor.kind, name: .actor.name, transport}'
# {"kind":"cli","name":"ops","transport":"cli"}
```

## Open questions

1. The PRD gives the Audit log list no default time range. Proposal: none in the API (newest first by cursor); the page
   (S-015) starts with the last 7 days.

## Notes

- Suggested commit: `feat(users): add user administration, password setup links and the audit log api`.
- P-05 (`auth.password_setup_link_ttl`) is confirmed or changed here.
- The password setup page itself is S-014; until then the link can be completed with `curl` as above.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-03.FR-3 | partial | administration and setup links; the Users page is S-015 |
| C-03.FR-9 | partial | sessions end on disable, Role change and delete |
| C-03.FR-11 | partial | `reset-password` for local accounts; for OIDC accounts S-013, `reset-totp` S-012 |
| C-03.FR-13 | partial | pseudonymization and sessions; tokens S-016, Account links S-051, the release of acknowledgements S-032 and S-049 |
| C-03.FR-14 | partial | user entry types and diffs; later stories add theirs |
| C-03.FR-15 | partial | the API; the page is S-015 |
| C-03.FR-26 | partial | the API; the page is S-014 |
| C-03.FR-31 | full | |
| C-03.AC-4 | full | |
| C-03.AC-11 | full | together with S-010 |
| C-03.AC-17 | full | |
| C-03.AC-19 | full | |
| C-03.AC-25 | full | |
| C-02.FR-15 | partial | `muster admin reset-password` with `--actor` |
