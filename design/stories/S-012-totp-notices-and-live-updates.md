---
id: S-012
title: TOTP, the TOTP policy, system notices and live updates (BE)
capability: C-03
kind: be
layer: L1
depends_on: [S-011]
covers: [C-03.FR-10, C-03.FR-11, C-03.FR-14, C-03.FR-18, C-03.FR-20, C-03.FR-24, C-03.FR-27, C-03.AC-13, C-03.AC-15, C-02.FR-24, C-09.FR-25, C-02.FR-15, C-09.AC-24]
files_touched:
  - internal/api/totp.go
  - internal/api/organization.go
  - internal/api/system.go
  - internal/api/live.go
  - internal/api/sessions.go
  - internal/api/users.go
  - internal/api/middleware.go
  - internal/api/server.go
  - internal/api/problem.go
  - internal/api/totp_test.go
  - internal/api/organization_test.go
  - internal/api/live_test.go
  - internal/api/server_test.go
  - internal/totp/totp.go
  - internal/totp/recovery.go
  - internal/totp/query.sql
  - internal/totp/totp_test.go
  - internal/totp/totp_integration_test.go
  - internal/auth/session.go
  - internal/auth/query.sql
  - internal/auth/throttle.go
  - internal/auth/auth_test.go
  - internal/organization/organization.go
  - internal/organization/query.sql
  - internal/organization/organization_test.go
  - internal/db/notify.go
  - internal/db/notify_test.go
  - internal/live/hub.go
  - internal/live/stream.go
  - internal/live/notices.go
  - internal/live/live_test.go
  - internal/live/live_integration_test.go
  - internal/cli/admin.go
  - internal/cli/admin_test.go
  - internal/cli/cli.go
  - internal/runtime/runtime.go
  - internal/runtime/runtime_test.go
  - internal/logging/events.go
  - internal/archlint/secretleak.go
  - sqlc.yaml
  - design/prd/l1/defaults.md
  - design/prd/L1.md
acceptance:
  - "[C-03.FR-10] A user begins TOTP enrolment, receives a secret and an `otpauth://` URI, confirms it with a current code and receives `auth.totp_recovery_codes` single-use recovery codes once; `GET /api/v1/me/totp` then reports the enrolment and the codes remaining."
  - "[C-03.FR-24, C-03.AC-13] A local user with TOTP who posts the right password gets a session in the state `totp_required`; every call except reading the session, signing out and `POST /api/v1/sessions/current/totp` answers 403 `totp_required`; the right code, or an unused recovery code, makes the session `active`."
  - "[C-03.FR-10] A code already used in its time step is refused; a wrong code answers 401 `invalid_credentials`, counts towards sign-in throttling and increments `muster_login_failures_total{method=\"totp\"}`."
  - "[C-03.FR-10, C-03.FR-20] With `totp_required` set to `everyone`, a local user without TOTP gets a session in the state `totp_enrolment_required` that answers 403 `totp_enrolment_required` to every call except reading the session, signing out and the enrolment operations; confirming the enrolment makes it `active`."
  - "[C-03.FR-27, C-03.AC-15] Removing TOTP with a wrong password or code answers 401 and keeps TOTP; with the right proof it is removed, and under a covering policy enrolment is demanded at the next sign-in."
  - "[C-03.FR-11] `POST /api/v1/users/{user_id}/reset-totp` by an Admin and `muster admin reset-totp --actor <name> <login>` remove the user's TOTP, and both write an Audit log entry."
  - "[C-03.FR-20] `GET /api/v1/organization` answers every signed-in identity; `PUT /api/v1/organization` with `If-Match` lets an Admin change `totp_required` and records a before/after diff; a changed value in any other field answers 422 `unsupported` until S-053 and S-055."
  - "[C-03.FR-18, C-02.FR-24] While the recovery notice is active, `GET /api/v1/system-notices` returns it with its end to every signed-in identity; the notice that no replica is leading is returned only to Admins."
  - "[C-09.FR-25, C-09.AC-24] `GET /api/v1/live-updates` starts with `retry: 3000`, sends a `system-notices` hint within 5 seconds of a notice starting or ending and an `organization` hint after an Organization update, and closes when the session ends; the reconnect answers 401."
verify: "make ci test-integration e2e"
operator_attention: false
issue: 12
---

# S-012. TOTP, the TOTP policy, system notices and live updates (BE)

## Scope

**IN**

- TOTP for every user: enrolment with a pending seed, confirmation, recovery codes, removal with proof, regeneration.
- The second step of sign-in and the limited session states `totp_required` and `totp_enrolment_required`.
- The Organization-wide TOTP policy through the `organization` resource, which every signed-in identity can read.
- Resetting another user's TOTP from the API and from `muster admin reset-totp --actor`.
- System notices and the server-sent events stream of live hints, with the `LISTEN`/`NOTIFY` hub every later
  capability uses.

**OUT**

- The second step after an OIDC sign-in and `oidc.skip_totp_with_idp_mfa` (S-013).
- The enrolment page, the TOTP prompt, the banners and the stream consumer in the SPA (S-014); the Security page
  (S-015).
- Hints about Alert Groups and configuration resources (their capabilities, from S-018 on).
- Editing the other Organization settings (S-053, S-055).

## Contracts

- **Operations implemented**: `getMyTotp`, `beginTotpEnrolment`, `confirmTotpEnrolment`, `removeTotp`,
  `regenerateTotpRecoveryCodes`, `submitSessionTotp`, `resetUserTotp`, `getOrganization`, `updateOrganization`,
  `listSystemNotices`, `streamLiveUpdates`; `createSession` gains `totp_code` and `recovery_code`. Schemas:
  `TotpStatus`, `TotpEnrolment`, `TotpCode`, `TotpRecoveryCodes`, `TotpRemoval`, `SessionTotp`, `TotpPolicy`,
  `Organization`, `OrganizationInput`, `SystemNotice(List)`, `HintEvent`.
- **TOTP** (C-03.FR-10, RFC 6238; `user_totp`, `user_recovery_codes`): 6 digits, 30-second steps, SHA-1 for
  authenticator compatibility, one step of tolerance either way; steps are counted on the real clock of S-006, so the
  development clock never moves them away from the authenticator app's. `beginTotpEnrolment` stores a pending seed (a Secret)
  and returns its base32 secret and an `otpauth://totp/Muster:<login>?secret=…&issuer=Muster` URI (`409
  totp_already_enrolled` when enrolled). `confirmTotpEnrolment` with a current code activates the seed and returns
  `auth.totp_recovery_codes` codes (P-08), shown once and stored argon2id-hashed (`409 totp_enrolment_not_started`
  without a pending seed). `last_used_step` refuses a replayed code. `regenerateTotpRecoveryCodes` needs a current code
  and replaces the unused codes. `removeTotp` needs the current password for an account with a password, or a current
  code or recovery code; a wrong proof answers `401 invalid_credentials` and changes nothing.
- **Second step and limited sessions** (C-03.FR-24): `createSession` for a user with TOTP and without a code creates a
  session in the state `totp_required`; with a right code, `active`. `submitSessionTotp` takes a code or a recovery
  code (`409 totp_not_pending` when nothing is pending). Under `organization.totp_required` — `nobody`, `local_users`
  (accounts that have a password) or `everyone` — a covered user without TOTP gets `totp_enrolment_required`. A limited
  session may read itself, sign out, and either give the code or enrol (`getMyTotp`, `beginTotpEnrolment`,
  `confirmTotpEnrolment`); every other call answers `403` with `totp_required` or `totp_enrolment_required`. A wrong
  code is `401 invalid_credentials`, counts towards throttling and increments
  `muster_login_failures_total{method="totp"}`.
- **Resets** (C-03.FR-11): `resetUserTotp` (`users:write`) deletes the enrolment and the recovery codes and ends the
  user's sessions (`end_reason` `totp_reset`), so that the user signs in again and enrols under a covering policy;
  `muster admin reset-totp --actor <name> <login>` does the same from the CLI (exit 2 without `--actor`). A user
  without TOTP is left as it is, and nothing is recorded. An Admin's reset of their own TOTP answers `403`: it goes
  through `removeTotp` with its proof, so that a stolen session cannot drop the second factor.
- **Organization** (C-03.FR-20, `organizations`): `getOrganization` for every signed-in identity, the outgoing
  heartbeat URL shown only as `SecretStatus`. `updateOrganization` needs `organization:write` and `If-Match`; in this
  story it applies `totp_required` and refuses any other changed value with `422` and `errors[].code = unsupported`
  pointing at the field (C-03.FR-20). Every change is audited with a diff and sends an `organization` hint.
- **Audit log actions** (C-03.FR-14): `totp.enrolled`, `totp.removed`, `totp.recovery_codes_regenerated`,
  `totp.reset`, `session.second_factor_failed`, `organization.updated`.
- **System notices** (C-03.FR-18, C-02.FR-24): `listSystemNotices` returns the notices computed by S-008:
  `recovering_after_downtime` (audience `all`, with `since` and `until`) to everyone, `no_replica_leading` (audience
  `admins`) only to callers that hold `system-status:read`.
- **Live updates** (ADR-0009; C-09.FR-25 in part): `streamLiveUpdates` answers `text/event-stream`, starts with
  `retry: 3000`, sends `: keepalive` every 25 seconds and events named `hint` with an `id` line and the data
  `{"type": …, "id": …}`. Each replica runs one hub (`internal/db/notify.go`, `internal/live`) that keeps a `LISTEN` on
  the session connection, reconnects after a loss and fans hints out to its streams; writers send `NOTIFY` in their
  transaction. Because notices also change with time, each replica re-evaluates them every 5 seconds and sends a
  `system-notices` hint when the set changes. Hint types in this story: `system-notices` and `organization`; later
  capabilities add theirs. A stream ends when its session ends — each replica checks the sessions of its streams
  every 5 seconds (`live.check_interval`), without counting it as a use — and the reconnect answers `401`. A replica
  serves at most `live.max_streams` streams, and a session `live.max_streams_per_session`; beyond them the stream
  answers `429`.

## Steps

1. Write the TOTP package with enrolment, confirmation, recovery codes, removal and the replay guard. Check: unit tests
   with fixed clocks cover each operation and each refusal.
2. Add the second step and the limited session states to sign-in and the middleware. Check: integration tests cover
   both states, the allowed calls and the `403` codes.
3. Write the Organization read and the TOTP policy update. Check: tests cover `If-Match`, the diff and the
   `unsupported` refusal.
4. Write the admin reset and `muster admin reset-totp`. Check: tests cover both, with their Audit log entries.
5. Write the notify hub, the stream and the notice watcher, and `listSystemNotices`. Check: tests with a manual clock see
   a hint when the recovery window ends; ending the session closes the stream.
6. Confirm or change P-08. Check: defaults.md and L1.md say so.

## Verification

```sh
make dev &
curl -s -c jar -H 'Content-Type: application/json' \
  -d '{"login":"admin@example.org","password":"muster-dev-password"}' localhost:8080/api/v1/sessions > /dev/null
CSRF=$(curl -s -b jar localhost:8080/api/v1/sessions/current | jq -r .csrf_token)
H=(-b jar -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json')

curl -s "${H[@]}" -X POST localhost:8080/api/v1/me/totp | tee /tmp/enrol.json | jq -r .otpauth_uri
# otpauth://totp/Muster:admin@example.org?secret=…&issuer=Muster
SECRET=$(jq -r .secret /tmp/enrol.json)
curl -s "${H[@]}" -d "{\"code\":\"$(oathtool --totp -b "$SECRET")\"}" localhost:8080/api/v1/me/totp/confirmation \
  | jq '.codes | length'
# 10

# the second step (C-03.AC-13)
curl -s -c j2 -H 'Content-Type: application/json' \
  -d '{"login":"admin@example.org","password":"muster-dev-password"}' localhost:8080/api/v1/sessions | jq -r .state
# totp_required
curl -s -b j2 localhost:8080/api/v1/roles | jq -r .code
# totp_required
C2=$(curl -s -b j2 localhost:8080/api/v1/sessions/current | jq -r .csrf_token)
sleep 30   # a new time step, so the code is not a replay
curl -s -b j2 -H "X-CSRF-Token: $C2" -H 'Content-Type: application/json' \
  -d "{\"totp_code\":\"$(oathtool --totp -b "$SECRET")\"}" localhost:8080/api/v1/sessions/current/totp | jq -r .state
# active

# the policy (C-03.FR-20) and a new user under it
ORG=$(curl -s -b jar localhost:8080/api/v1/organization)
curl -s "${H[@]}" -X PUT -H "If-Match: $(jq -r .etag <<<"$ORG")" localhost:8080/api/v1/organization \
  -d "$(jq -c 'del(.id, .etag) | .totp_required = "everyone" | .outgoing_heartbeat = {proxy: {enabled: false}}' <<<"$ORG")" \
  | jq -r .totp_required
# everyone
curl -s "${H[@]}" -X PUT -H "If-Match: $(curl -s -b jar localhost:8080/api/v1/organization | jq -r .etag)" \
  localhost:8080/api/v1/organization \
  -d "$(curl -s -b jar localhost:8080/api/v1/organization | jq -c 'del(.id, .etag) | .time_zone = "Europe/Berlin" | .outgoing_heartbeat = {proxy: {enabled: false}}')" \
  | jq -c '[.errors[] | {pointer, code}]'
# [{"pointer":"/time_zone","code":"unsupported"}]
# create a user with a setup link and set the password as in S-011, then:
curl -s -c j3 -H 'Content-Type: application/json' -d '{"login":"carol","password":"carol-password-1"}' \
  localhost:8080/api/v1/sessions | jq -r .state
# totp_enrolment_required

# removing TOTP with a wrong proof (C-03.AC-15)
curl -s "${H[@]}" -d '{"password":"wrong-password"}' localhost:8080/api/v1/me/totp/removal | jq -r .code
# invalid_credentials
curl -s -b jar localhost:8080/api/v1/me/totp | jq -r .enrolled
# true

# notices and the stream: simulate a recovery window and watch the hint arrive
curl -sN -b jar localhost:8080/api/v1/live-updates > /tmp/sse & SSE=$!
psql "$MUSTER_DATABASE_URL" -qc "UPDATE runtime_state SET recovery_until = now() + interval '2 minutes'"
sleep 6; curl -s -b jar localhost:8080/api/v1/system-notices | jq -c '.items[] | {kind, audience}'
# {"kind":"recovering_after_downtime","audience":"all"}
kill $SSE; head -1 /tmp/sse; grep -m1 '^data:' /tmp/sse
# retry: 3000
# data: {"type":"system-notices","id":null}
```

## Open questions

None.

## Notes

- Suggested commit: `feat(auth): add totp, the totp policy, system notices and live updates`.
- P-08 (`auth.totp_recovery_codes`) is confirmed or changed here.
- `oathtool` (oath-toolkit) generates codes for the live checks; the pull request records the commands.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-03.FR-10 | partial | everything except the OIDC parts (S-013) and the pages (S-014) |
| C-03.FR-11 | partial | the TOTP resets; `reset-password` is S-011 and S-062 |
| C-03.FR-14 | partial | TOTP and Organization entry types |
| C-03.FR-18 | partial | the notices and the stream; the shell and its banners are S-014 |
| C-03.FR-20 | full | the Security page is S-015 |
| C-03.FR-24 | partial | the second step and limited sessions; the OIDC button is S-013, the page S-014 |
| C-03.FR-27 | partial | the proof for removing TOTP |
| C-03.AC-13 | partial | local sign-in; the OIDC part is S-013 |
| C-03.AC-15 | full | |
| C-02.FR-24 | full | together with S-008 |
| C-09.FR-25 | partial | the stream with the notice and Organization hints; Alert Group hints are S-029 |
| C-02.FR-15 | partial | `muster admin reset-totp` with `--actor` |
| C-09.AC-24 | full | the stream, brought forward from C-09 with the live updates of C-03; S-029 checks it again with its hints |
