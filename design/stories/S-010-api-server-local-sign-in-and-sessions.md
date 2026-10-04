---
id: S-010
title: API server, local sign-in, sessions and the bootstrap Admin (BE)
capability: C-03
kind: be
layer: L1
depends_on: [S-008]
covers: [C-03.FR-1, C-03.FR-2, C-03.FR-3, C-03.FR-4, C-03.FR-9, C-03.FR-12, C-03.FR-14, C-03.FR-16, C-03.FR-22, C-03.FR-23, C-03.FR-24, C-03.FR-27, C-03.AC-3, C-03.AC-10, C-03.AC-11, C-03.AC-12, C-02.FR-19]
files_touched:
  - api/embed.go
  - internal/api/server.go
  - internal/api/problem.go
  - internal/api/middleware.go
  - internal/api/sessions.go
  - internal/api/profile.go
  - internal/api/roles.go
  - internal/api/server_test.go
  - internal/api/sessions_test.go
  - internal/auth/session.go
  - internal/auth/csrf.go
  - internal/auth/password.go
  - internal/auth/throttle.go
  - internal/auth/permissions.go
  - internal/auth/identity.go
  - internal/auth/query.sql
  - internal/auth/auth_test.go
  - internal/users/users.go
  - internal/users/bootstrap.go
  - internal/users/query.sql
  - internal/users/users_test.go
  - internal/audit/audit.go
  - internal/audit/query.sql
  - internal/audit/audit_test.go
  - internal/server/spa.go
  - internal/server/server_test.go
  - internal/runtime/runtime.go
  - internal/runtime/bootstrap.go
  - internal/devmode/devmode.go
  - internal/logging/events.go
  - internal/metrics/catalogue.go
  - sqlc.yaml
  - design/prd/l1/defaults.md
  - design/prd/L1.md
acceptance:
  - "[C-03.FR-22, C-03.AC-11] On a new database with `MUSTER_BOOTSTRAP_ADMIN_EMAIL=ops@example.org` and a password from `MUSTER_BOOTSTRAP_ADMIN_PASSWORD_FILE`, one local Admin is created with that value as login and email and the name `ops`, and signs in with that login and password; the Audit log stores `user.created` by the actor `bootstrap` and copies it to stdout; on the next start no user is created and `bootstrap_admin_ignored` is logged at WARN."
  - "[C-03.FR-3, C-03.FR-24] `POST /api/v1/sessions` with the right login in any letter case and the right password answers 201 with a `Session` in the state `active` and sets `muster_session` with `HttpOnly`, `Secure` and `SameSite=Lax`; a wrong login and a wrong password both answer 401 `invalid_credentials` with identical bodies."
  - "[C-03.FR-9, C-03.AC-3] A mutating request with the session cookie but without a valid `X-CSRF-Token` answers 403 `csrf_invalid`; with the token of the `Session` it succeeds."
  - "[C-03.FR-9] A session ends after `auth.session_idle_timeout` without use or `auth.session_lifetime` in total (401 `session_expired`, checked with a manual clock); signing out ends the current session, `DELETE /api/v1/me/sessions` ends all of them, and changing the password ends the user's other sessions."
  - "[C-03.FR-4, C-03.AC-10] After 3 consecutive failures for one account or one source address, attempts are refused with 429 and a `Retry-After` doubling from 1 to 60 seconds; six evaluated failures in a row raise `muster_login_failures_total{method=\"local\"}` by six and write six `session.sign_in_failed` entries; a success resets the count."
  - "[C-03.FR-4] With `MUSTER_TRUSTED_PROXIES` empty, failures are counted per socket address whatever `X-Forwarded-For` says; with the socket's network listed, they are counted per the first address of `X-Forwarded-For`, read from the right, outside the listed networks, so a forged left-most entry changes nothing."
  - "[C-03.FR-2] `GET /api/v1/roles` lists admin, responder and viewer with the Permissions of the reference matrix (31, 12 and 8); a test compares the seeded allocation with that matrix, and every operation is checked against its `x-permission`, never against a Role."
  - "[C-03.FR-12] `GET /api/v1/me` returns the user and their effective Permissions; `PUT /api/v1/me` changes the name, the time zone and the language; `GET /api/v1/me/sessions` lists the user's sessions and marks the current one."
  - "[C-03.FR-23, C-03.AC-12] `GET /api/v1/openapi.yaml` on the app listener returns, byte for byte, the api/openapi.yaml the binary was built from."
  - "[C-03.FR-16] Every SPA response carries the Content Security Policy and the security headers of the contract; an unknown path under /api/v1 answers a 404 `Problem`, not the SPA."
  - "[C-03.FR-24] `GET /api/v1/sign-in-options` without credentials answers `{\"oidc\":{\"enabled\":false}}` and says nothing about the version or the installation."
  - "[C-03.FR-14] Sign-in, failed sign-in, sign-out, ending all sessions, the profile update and the password change are Audit log entries with the actor and the Transport `ui`, each copied to stdout as `audit_entry`."
  - "[C-03.FR-1, C-03.FR-27] Every request is served for the single Organization and every query filters by `org_id` (`make lint-arch`); the profile operations that change the account accept only the web session with its CSRF token."
  - "[NFR-13, C-02.FR-19] Every response of the implemented operations validates against the spec in tests, every API request is counted in `muster_api_requests_total` and `muster_api_request_duration_seconds`, and an operation that no story has implemented yet answers 501 with a `Problem`."
verify: "make ci test-integration e2e"
operator_attention: true
issue: null
---

# S-010. API server, local sign-in, sessions and the bootstrap Admin (BE)

## Scope

**IN**

- The generated strict server on the app listener with request validation, the `Problem` mapping, authentication by
  session cookie, the CSRF check, the Permission check and API metrics.
- Roles and Permissions from the reference data; `listRoles`.
- Local sign-in, sign-out, sessions and their expiry, sign-in throttling.
- The caller's profile: reading it, the name, time zone and language, the password change, the session list and
  "sign out everywhere".
- The bootstrap Admin.
- The Audit log writer with its stdout copy, used by every later story.
- Serving the SPA with its Content Security Policy and security headers, and serving the spec.

**OUT**

- User administration, password setup links, the Audit log read API and `muster admin reset-password` (S-011).
- TOTP and limited sessions, the `organization` resource, system notices and live updates (S-012).
- OIDC, the proxy settings object and the write-only secret fields (S-013).
- Bearer tokens: until S-016, a request with `Authorization: Bearer` answers 401.
- Every page (S-014, S-015).

## Contracts

- **Operations implemented**: `getSignInOptions`, `createSession`, `getCurrentSession`, `deleteCurrentSession`, `getMe`,
  `updateMe`, `changePassword`, `listMySessions`, `deleteMySessions`, `listRoles`, `getOpenApiSpec`. Schemas: `Session`,
  `SessionCreate`, `SessionState`, `SessionMethod`, `SessionInfo(List)`, `Me`, `MeUpdate`, `PasswordChange`, `User`,
  `Role(List)`, `Permission`, `SignInOptions`, `Problem`.
- **Server** (ADR-0008): the strict server from `internal/api/gen` is mounted under `/api/v1` on the app listener.
  Middleware, in order: security headers → request metrics → authentication (the session cookie) → CSRF for mutating
  requests made with the cookie → request validation (kin-openapi; the ingestion operations are excluded) → the
  Permission of the operation's `x-permission` → the handler. Every error becomes an RFC 9457 `Problem` whose `type`
  and `code` come from `x-problem-types` and `x-problem-codes`. Tests validate every response of the implemented
  operations against the spec. An operation no story has implemented yet answers `501` (`not-implemented`).
- **Roles and Permissions** (C-03.FR-2): `roles`, `permissions` and `role_permissions` are loaded at startup; checks use
  Permissions only; a test compares the seeded allocation with the matrix of
  [reference.md](../prd/l1/reference.md#roles-and-permissions). P-42 is confirmed here.
- **Sessions** (C-03.FR-9, `sessions`): the cookie `muster_session` (`HttpOnly; Secure; SameSite=Lax; Path=/`) holds
  32 random bytes; the database keeps only their SHA-256. `Session.csrf_token` is an HMAC of the session token with the
  Keyring's `csrf` sub-key (S-007) and is not stored; mutating requests with the cookie need it in `X-CSRF-Token`,
  otherwise `403 csrf_invalid`. A session ends after `auth.session_idle_timeout` without use or after
  `auth.session_lifetime` (`401 session_expired`); `last_used_at` is refreshed at most once a minute; every end records
  its reason. Changing the password ends the user's other sessions (C-03.FR-9).
- **Local sign-in** (C-03.FR-3, FR-24): the login is compared lowercased; passwords are argon2id PHC strings
  (`golang.org/x/crypto/argon2`; the parameters are recorded in the code). A wrong login or password, a disabled or a
  deleted account all answer `401 invalid_credentials` with the same body; an unknown login still runs a dummy hash so
  the timing does not tell. `getSignInOptions` is public and answers `{"oidc":{"enabled":false}}` until S-013.
- **Throttling** (C-03.FR-4, P-07, `sign_in_throttles`): per lowercased login and per source address. After 3
  consecutive failures the next attempt is allowed after 1 s, then 2, 4, … up to 60 s (`auth.signin_throttle`); an
  earlier attempt answers `429` with `Retry-After` and is not evaluated. A success resets both. Each evaluated failure
  increments `muster_login_failures_total{method="local"}`. The source address is the client address of C-02.FR-1,
  derived once per request by the middleware: the TCP peer, or — when the peer is inside `MUSTER_TRUSTED_PROXIES` — the
  first address of `X-Forwarded-For`, read from the right, outside those networks.
- **Profile** (C-03.FR-12, FR-27): `getMe` returns `Me`; `updateMe` changes `name`, `time_zone` (IANA name or `null`
  for the browser's) and `language` (`en`, `ru` or `null`); `changePassword` needs the current password (`401
  invalid_credentials` otherwise) and a new one of at least `auth.password_min_length` characters (P-06; `422`
  `too_short` otherwise); `listMySessions`
  and `deleteMySessions`. The operations that change the account accept only the web session with its CSRF token.
- **Bootstrap Admin** (C-03.FR-22, ADR-0010): a start-up ensure step. If the Organization has no Admin and
  `MUSTER_BOOTSTRAP_ADMIN_EMAIL` is set, it creates a local user with role `admin`, source `bootstrap`, login and email
  equal to the variable, the name before `@` and the password of `MUSTER_BOOTSTRAP_ADMIN_PASSWORD(_FILE)`, which must
  meet `auth.password_min_length` or startup stops naming the variable. The Audit log records `user.created` with the
  actor `bootstrap`. If an Admin exists, the variables are ignored and `bootstrap_admin_ignored` (WARN) says they can be
  removed; with no Admin and no variables, `bootstrap_admin_missing` (WARN) says how to create one. `muster dev` sets the
  development Admin of S-004.
- **Audit log writer** (C-03.FR-14, `audit_log`): appends an entry with the actor (`user`, `service_account`,
  `system`, `bootstrap`, or `cli` with the `--actor` name), the token used (from S-016), the Transport (`ui` for the web
  session, `api` for tokens, `cli`, `system`), a stable action `<resource>.<verb>`, the resource, a before/after diff
  where something was configured, and details. Every entry is also logged as `audit_entry` (INFO). Actions added here:
  `user.created`, `session.signed_in`, `session.sign_in_failed`, `session.signed_out`, `session.ended_all`,
  `user.profile_updated`, `user.password_changed`.
- **SPA and spec** (C-03.FR-16, FR-23): the app listener serves the embedded SPA for every path outside `/api/` with
  the index as fallback, and an unknown path under `/api/v1` answers a `404` `Problem`. Every SPA response carries
  `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:;
  connect-src 'self'; font-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'`,
  `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `X-Frame-Options: DENY`,
  `Cross-Origin-Opener-Policy: same-origin`, and `Strict-Transport-Security` when `MUSTER_PUBLIC_URL` is `https`.
  `getOpenApiSpec` (public) returns the embedded `api/openapi.yaml` byte for byte as `application/yaml`.
- **Metrics**: `muster_api_requests_total{route_pattern,method,code}` and `muster_api_request_duration_seconds
  {route_pattern,method}`, where `route_pattern` is the spec's path template; `muster_login_failures_total{method}`
  with the value set of the catalogue: `local`, `oidc`, `totp`.
- **Log events**: `audit_entry`, `bootstrap_admin_ignored`, `bootstrap_admin_missing`.

## Steps

1. Mount the strict server with the middleware chain, the `Problem` mapping and the 501 default. Check: tests show a
   validation error as a `Problem` with `errors[].pointer`, a 404 for an unknown API path and a 501 for an
   unimplemented operation.
2. Load Roles and Permissions and wire the Permission check. Check: the matrix test passes and `listRoles` answers.
3. Write passwords, sessions, the CSRF token and throttling. Check: integration tests with a manual clock cover expiry,
   CSRF refusal, the throttle sequence and the metric.
4. Write the profile operations. Check: tests cover each operation, including the refusal of a short password.
5. Write the Audit log writer and the bootstrap Admin ensure step. Check: a test on a new database creates the Admin
   once and logs the warnings on later starts.
6. Serve the SPA with its headers and the spec. Check: tests see the headers and the byte-identical spec.
7. Confirm P-06, P-07 and P-42. Check: defaults.md and L1.md say so.

## Verification

```sh
make dev &              # muster dev: the development Admin admin@example.org / muster-dev-password
curl -s localhost:8080/api/v1/sign-in-options
# {"oidc":{"enabled":false}}

curl -s -c jar -H 'Content-Type: application/json' \
  -d '{"login":"ADMIN@example.org","password":"muster-dev-password"}' localhost:8080/api/v1/sessions \
  | jq -c '{state, method, login: .user.login}'
# {"state":"active","method":"local","login":"admin@example.org"}
grep muster_session jar | cut -f1,2
# #HttpOnly_localhost	FALSE
curl -s -o /dev/null -D - -H 'Content-Type: application/json' \
  -d '{"login":"admin@example.org","password":"muster-dev-password"}' localhost:8080/api/v1/sessions | grep -i set-cookie
# Set-Cookie: muster_session=…; Path=/; HttpOnly; Secure; SameSite=Lax

curl -s -b jar -X PUT -H 'Content-Type: application/json' -d '{"name":"Admin"}' localhost:8080/api/v1/me | jq -r .code
# csrf_invalid
CSRF=$(curl -s -b jar localhost:8080/api/v1/sessions/current | jq -r .csrf_token)
curl -s -b jar -X PUT -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' \
  -d '{"name":"Admin","time_zone":"Europe/Berlin","language":"ru"}' localhost:8080/api/v1/me | jq -c '.user | {name, time_zone, language}'
# {"name":"Admin","time_zone":"Europe/Berlin","language":"ru"}

curl -s -b jar localhost:8080/api/v1/roles | jq -r '.items[] | "\(.name) \(.permissions | length)"'
# admin 31
# responder 12
# viewer 8

# throttling and the metric (C-03.AC-10): six evaluated failures, honouring Retry-After
before=$(curl -s localhost:8082/metrics | awk '/^muster_login_failures_total\{method="local"\}/{print $2}')
for i in 1 2 3 4 5 6; do
  while :; do
    r=$(curl -s -D /tmp/h -o /tmp/b -w '%{http_code}' -H 'Content-Type: application/json' \
        -d '{"login":"admin@example.org","password":"wrong"}' localhost:8080/api/v1/sessions)
    [ "$r" = 429 ] && { sleep "$(awk 'tolower($1)=="retry-after:"{print $2+0}' /tmp/h)"; continue; }
    echo "attempt $i: $r $(jq -r .code /tmp/b)"; break
  done
done
# attempt 1: 401 invalid_credentials … attempt 6: 401 invalid_credentials   (429 with Retry-After 1, 2, 4 in between)
after=$(curl -s localhost:8082/metrics | awk '/^muster_login_failures_total\{method="local"\}/{print $2}')
echo $((after - before))
# 6

curl -s localhost:8080/api/v1/openapi.yaml | cmp - api/openapi.yaml && echo identical
# identical
curl -sI localhost:8080/ | grep -iE '^(content-security-policy|x-frame-options|referrer-policy)'
# Content-Security-Policy: default-src 'self'; script-src 'self'; …; frame-ancestors 'none'
# X-Frame-Options: DENY
# Referrer-Policy: no-referrer
curl -s localhost:8080/api/v1/does-not-exist | jq -r .type
# https://muster-io.github.io/muster/problems/not-found
curl -s -b jar localhost:8080/api/v1/integrations -o /dev/null -w '%{http_code}\n'
# 501
```

Bootstrap on a fresh database (C-03.AC-11):

```sh
kill %1; wait %1    # stop `make dev`: this check runs the plain server, which needs its own settings
psql "$MUSTER_DATABASE_URL" -qc 'DROP SCHEMA public CASCADE; CREATE SCHEMA public'
printf 'ops-bootstrap-pass' > /tmp/admin-pw
export MUSTER_PUBLIC_URL=http://localhost:8080 MUSTER_SECRET_KEYS="$(openssl rand -base64 32)"
MUSTER_BOOTSTRAP_ADMIN_EMAIL=ops@example.org MUSTER_BOOTSTRAP_ADMIN_PASSWORD_FILE=/tmp/admin-pw ./bin/muster \
  > /tmp/bootstrap.log 2>&1 &
sleep 5; grep -m1 '"action":"user.created"' /tmp/bootstrap.log
# {"level":"INFO","event":"audit_entry","actor_kind":"bootstrap","action":"user.created","resource_name":"ops",...}
curl -s -H 'Content-Type: application/json' -d '{"login":"ops@example.org","password":"ops-bootstrap-pass"}' \
  localhost:8080/api/v1/sessions | jq -c '{state, name: .user.name, role: .user.role}'
# {"state":"active","name":"ops","role":"admin"}
# restart with the same variables:
# {"level":"WARN","event":"bootstrap_admin_ignored",...}
psql "$MUSTER_DATABASE_URL" -Atc 'SELECT count(*) FROM users'
# 1
```

## Open questions

None.

## Notes

- Suggested commit: `feat(auth): add API server with local sign-in, sessions and the bootstrap admin`.
- `operator_attention: true` — P-42 (the allocation of Permissions to Roles) is a product decision confirmed here.
- P-06 and P-07 are confirmed or changed here, with defaults.md and L1.md updated in the same pull request.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-03.FR-1 | full | the schema carries `org_id` since S-006 |
| C-03.FR-2 | full | |
| C-03.FR-3 | partial | sign-in, case-insensitive logins, argon2id, own password change; user administration is S-011 |
| C-03.FR-4 | full | |
| C-03.FR-9 | partial | local sessions; ending on disable and Role change is S-011, OIDC parts S-013; the profile page is C-03.FR-12 |
| C-03.FR-12 | partial | the API of the profile; the page is S-014 |
| C-03.FR-14 | partial | the writer and the security events of this story; each later story adds its entry types |
| C-03.FR-16 | full | |
| C-03.FR-22 | full | |
| C-03.FR-23 | full | |
| C-03.FR-24 | partial | local sign-in; limited sessions are S-012, the OIDC button S-013, the page S-014 |
| C-03.FR-27 | partial | the web session for profile changes; the token refusals are S-016 |
| C-03.AC-3 | full | |
| C-03.AC-10 | full | |
| C-03.AC-11 | full | the Audit log read API that shows it is S-011 |
| C-03.AC-12 | full | |
| C-02.FR-19 | partial | the API and sign-in metrics that the catalogue assigns to C-03 |
