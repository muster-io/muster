---
id: S-016
title: Personal access tokens, Service accounts, token authentication and rate limits (BE)
capability: C-04
kind: be
layer: L1
depends_on: [S-013, S-062]
covers: [C-04.FR-1, C-04.FR-2, C-04.FR-3, C-04.FR-4, C-04.FR-5, C-04.FR-6, C-04.FR-7, C-04.FR-8, C-04.AC-1, C-04.AC-2, C-04.AC-3, C-04.AC-4, C-04.AC-5, C-04.AC-7, C-04.AC-8, C-04.AC-9, C-03.FR-13, C-03.FR-27, C-03.FR-30, C-03.AC-14, C-03.AC-22, C-03.AC-24]
files_touched:
  - internal/tokens/tokens.go
  - internal/tokens/personal.go
  - internal/tokens/serviceaccounts.go
  - internal/tokens/authenticate.go
  - internal/tokens/ratelimit.go
  - internal/tokens/query.sql
  - internal/tokens/tokens_test.go
  - internal/tokens/authenticate_test.go
  - internal/tokens/ratelimit_test.go
  - internal/tokens/tokens_integration_test.go
  - internal/api/tokens.go
  - internal/api/serviceaccounts.go
  - internal/api/tokens_test.go
  - internal/api/middleware.go
  - internal/api/server.go
  - internal/api/problem.go
  - internal/api/server_test.go
  - internal/auth/identity.go
  - internal/audit/audit.go
  - internal/users/admin.go
  - internal/users/query.sql
  - internal/users/admin_test.go
  - internal/runtime/runtime.go
  - internal/runtime/runtime_test.go
  - internal/archlint/secretleak.go
  - sqlc.yaml
  - api/openapi.yaml
  - test/e2e/tokens_test.go
  - design/prd/l1/defaults.md
  - design/prd/L1.md
acceptance:
  - "[C-04.FR-1, C-04.FR-7, C-04.AC-1] A Personal access token of an Admin, created with read-only Permissions, gets 403 when creating a User; the same Admin's session succeeds."
  - "[C-04.FR-3, C-04.AC-2] A token's value starts with `mstr_pat_` or `mstr_sat_`, is returned only by the call that creates it, and no list or read returns it; the database stores only its hash."
  - "[C-04.FR-3] Every token shows its optional expiry and when and from which address it was last used, and can be revoked; an expired or revoked token answers 401."
  - "[C-04.FR-5, C-04.AC-3] Exceeding `api.rate_limit` with one token answers 429 with `Retry-After`, while requests with another token are unaffected; web sessions and ingestion are not rate-limited."
  - "[C-04.FR-1, C-03.FR-13, C-04.AC-4] Tokens of a deleted user answer 401 and are recorded as revoked with the reason `owner_deleted`; tokens of a disabled user answer 401 until the user is enabled again."
  - "[C-04.FR-6, C-04.AC-5] Creating a User with a Personal access token writes an Audit log entry shown as \"{user} via token {name}\"; issuing and revoking tokens are Audit log entries."
  - "[C-04.FR-7, C-04.AC-7] A Personal access token cannot create or revoke tokens (403 `session_required`); a session can, and asking for a Permission its User does not hold answers 422 `permission_not_held`."
  - "[C-04.FR-2, C-04.AC-8] Admins create, update, disable, enable and delete Service accounts with a Role and any number of tokens; a disabled Service account's tokens answer 401 until `enable` is called."
  - "[C-04.FR-4] The API accepts `Authorization: Bearer` with a Personal access token or a Service account token and refuses an Integration token (`mstr_int_`) with 401."
  - "[C-03.FR-27, C-03.AC-14] A Personal access token — even an Admin's with all Permissions — gets 403 `session_required` on creating a token, changing the password and enrolling or removing TOTP; a Service account token gets 403 `service_account_not_allowed` on `GET /api/v1/me`."
  - "[C-04.FR-8, C-03.FR-30, C-04.AC-9, C-03.AC-24] For an OIDC user without an offline token, the user's Personal access token answers 401 `oidc_recheck_required` with \"Sign in through OIDC to make your tokens work again.\" once `auth.oidc_token_grace` has passed since the last OIDC sign-in, and works again after the next OIDC sign-in without being reissued; a Service account token of the same age is not affected."
  - "[C-04.FR-8, C-03.AC-22, C-04.AC-9] For an OIDC user with an offline token, the token keeps working past the grace period while re-checks succeed, and answers 401 `oidc_recheck_required` within one `auth.oidc_recheck_interval` after the IdP refuses the user; after the next OIDC sign-in the same token works."
verify: "make ci test-integration e2e"
operator_attention: false
issue: 16
---

# S-016. Personal access tokens, Service accounts, token authentication and rate limits (BE)

## Scope

**IN**

- Personal access tokens: create and revoke from the web session, list; Permissions narrowed to a subset of the
  owner's, recomputed at every request.
- Service accounts with a Role and tokens, and their lifecycle.
- Bearer authentication on the API, the refusals of C-03.FR-27 for tokens, and the per-token rate limit.
- Token attribution in the Audit log; revoking a deleted user's tokens; the OIDC re-check and grace rules for
  Personal access tokens.

**OUT**

- The pages (S-017).
- Refusing Acknowledge and "Still on it" from a Service account with `owner_must_be_user`, and `allowed_commands`
  (C-04.FR-2, C-04.AC-6; S-032 and S-049, when the commands exist).
- Refusing API tokens at ingestion (S-018, the ingestion endpoint).
- Editing the grace period (S-055, S-056).

## Contracts

- **Operations implemented**: `listPersonalAccessTokens`, `createPersonalAccessToken`, `revokePersonalAccessToken`,
  `listServiceAccounts`, `createServiceAccount`, `getServiceAccount`, `updateServiceAccount`, `deleteServiceAccount`,
  `disableServiceAccount`, `enableServiceAccount`, `listServiceAccountTokens`, `createServiceAccountToken`,
  `revokeServiceAccountToken`. Schemas: `PersonalAccessToken(List)`, `PersonalAccessTokenCreate`,
  `PersonalAccessTokenCreated`, `ServiceAccount(List)`, `ServiceAccountInput`, `ServiceAccountToken(List)`,
  `ServiceAccountTokenCreate`, `ServiceAccountTokenCreated`.
- **Tokens** (C-04.FR-3, ADR-0011; `api_tokens`, `api_token_permissions`): `mstr_pat_` or `mstr_sat_` followed by 32
  random bytes; only the SHA-256 is stored; the value is returned once, in the `…Created` response. `expires_at` is
  optional (`token.expiry`). `last_used_at` and `last_used_address` are refreshed at most once a minute (the address
  as in S-010). Revocation sets `revoked_at` and `revoked_reason`.
- **Personal access tokens** (C-04.FR-1, FR-7): created and revoked only from the web session (`403 session_required`
  with a token); the requested Permissions must be held by the creator at that moment (`422`, `errors[].code =
  permission_not_held`, pointer `/permissions/<n>`); the effective Permissions are the token's intersected with the
  owner's current Role at each request. A disabled owner's tokens answer `401` until the owner is enabled; deleting the
  owner revokes them with `owner_deleted`.
- **Service accounts** (C-04.FR-2; `service_accounts`): a name unique among accounts that are not deleted
  (`409 name_taken`), a Role, the status `active`, `disabled` or `deleted`; updates need `If-Match`; a disabled
  account's tokens answer `401` until it is enabled; deleting revokes its tokens. A Service account never signs in to
  the UI and has no profile.
- **Authentication** (C-04.FR-4): the app listener accepts `Authorization: Bearer` with `mstr_pat_` or `mstr_sat_`; the
  prefix gives the kind before the hash lookup; an Integration token, an unknown, revoked or expired token answers
  `401 invalid_credentials`. Requests with a token use the Transport `api` and need no CSRF token.
- **Refusals for tokens** (C-03.FR-27): the operations that change the caller's own account — profile, password, TOTP,
  Personal access tokens, Account links, ending sessions, linking OIDC — answer `403 session_required` to any token;
  `GET /api/v1/me` and its sub-resources answer `403 service_account_not_allowed` to a Service account token. A token
  never mints or revokes a token (C-04.FR-7): `createServiceAccountToken` and `revokeServiceAccountToken` take the web
  session only, and answer `403 session_required` to a token. `streamLiveUpdates` follows a web session — the Hub
  checks and ends streams per session — and also answers `403 session_required` to a token. These refusals come before
  request validation.
- **OIDC accounts** (C-04.FR-8, C-03.FR-30): a Personal access token whose owner signs in through OIDC answers `401
  oidc_recheck_required` with "Sign in through OIDC to make your tokens work again." while `users.oidc_refused_at` is
  set, or — only for an owner without an offline token — once the last OIDC sign-in is older than
  `auth.oidc_token_grace` (`organizations.oidc_token_grace_seconds`). It is not revoked and works again after the next
  OIDC sign-in. Service account tokens are not affected.
- **Rate limit** (C-04.FR-5, P-09): a token bucket per token and replica of `api.rate_limit` (20 requests per second,
  bursts of 100; with several replicas a token can reach the limit on each);
  an excess request answers `429` with `Retry-After`. Web sessions and ingestion are not rate-limited.
- **Audit log** (C-04.FR-6): actions `api_token.created`, `api_token.revoked`, `service_account.created`,
  `service_account.updated`, `service_account.disabled`, `service_account.enabled`, `service_account.deleted`; every
  entry made with a token stores `api_token_id` and `token_name`, read back as `AuditEntry.token_name` and shown as
  "{user} via token {name}" or as the Service account and its token.

## Steps

1. Write token generation, hashing and verification. Check: unit tests cover both prefixes, the hash-only storage and
   expiry.
2. Write Personal access tokens with the subset rule and the effective Permissions. Check: integration tests cover
   `permission_not_held`, a Role change shrinking a token, disable and delete of the owner.
3. Write Service accounts and their tokens. Check: tests cover the lifecycle, `name_taken`, `If-Match` and disabled
   tokens.
4. Add bearer authentication, the refusals and Audit log attribution to the middleware. Check: tests cover
   `session_required`, `service_account_not_allowed`, the Integration token refusal and "{user} via token {name}".
5. Add the OIDC rules. Check: tests with a manual clock cover the grace period, a refusal at re-check and recovery
   after an OIDC sign-in.
6. Add the rate limiter. Check: a test with two tokens shows `429` with `Retry-After` for one and nothing for the other.
7. Confirm or change P-09. Check: defaults.md and L1.md say so.

## Verification

```sh
make dev &
# the Admin's session as in S-011: cookie jar `jar`, CSRF token in `CSRF`, H=(-b jar -H "X-CSRF-Token: $CSRF" ...)

curl -s "${H[@]}" -d '{"name":"read-only","permissions":["users:read","alert-groups:read"]}' \
  localhost:8080/api/v1/me/personal-access-tokens | tee /tmp/ro.json | jq -r '.value[0:9]'
# mstr_pat_
RO=$(jq -r .value /tmp/ro.json)
curl -s -b jar localhost:8080/api/v1/me/personal-access-tokens | jq '[.items[] | has("value")] | any'
# false
psql "$MUSTER_DATABASE_URL" -Atc "SELECT kind, octet_length(token_hash) FROM api_tokens WHERE name = 'read-only'"
# personal|32

# C-04.AC-1: the read-only token cannot create a user, the session can
curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $RO" -H 'Content-Type: application/json' \
  -d '{"name":"Eve","login":"eve","role":"viewer"}' localhost:8080/api/v1/users
# 403
curl -s -o /dev/null -w '%{http_code}\n' "${H[@]}" -d '{"name":"Eve","login":"eve","role":"viewer"}' localhost:8080/api/v1/users
# 201

# C-04.AC-5: "{user} via token {name}"
FULL=$(curl -s "${H[@]}" -d '{"name":"full","permissions":["users:read","users:write"]}' \
  localhost:8080/api/v1/me/personal-access-tokens | jq -r .value)
curl -s -o /dev/null -H "Authorization: Bearer $FULL" -H 'Content-Type: application/json' \
  -d '{"name":"Finn","login":"finn","role":"viewer"}' localhost:8080/api/v1/users
curl -s -b jar 'localhost:8080/api/v1/audit-log?action=user.created' | jq -r '.items[0] | "\(.actor.name) via token \(.token_name) (\(.transport))"'
# admin via token full (api)

# C-03.AC-14 and C-04.AC-7
curl -s -H "Authorization: Bearer $FULL" -H 'Content-Type: application/json' -d '{"name":"x","permissions":[]}' \
  localhost:8080/api/v1/me/personal-access-tokens | jq -r .code
# session_required
curl -s -H "Authorization: Bearer $FULL" -H 'Content-Type: application/json' \
  -d '{"current_password":"muster-dev-password","new_password":"another-password-1"}' -X PUT localhost:8080/api/v1/me/password | jq -r .code
# session_required

# C-04.AC-3: rate limit per token
seq 1 300 | xargs -P 50 -I{} curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $RO" \
  localhost:8080/api/v1/me | sort | uniq -c
#  1xx 200
#  1xx 429          (about 100 plus the refill succeed; the rest are refused)
curl -s -D - -o /dev/null -H "Authorization: Bearer $RO" localhost:8080/api/v1/me | grep -i '^retry-after'
# Retry-After: 1
curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $FULL" localhost:8080/api/v1/me
# 200

# Service accounts (C-04.AC-8, C-03.AC-14)
SA=$(curl -s "${H[@]}" -d '{"name":"terraform","role":"admin"}' localhost:8080/api/v1/service-accounts | jq -r .id)
SAT=$(curl -s "${H[@]}" -d '{"name":"ci"}' "localhost:8080/api/v1/service-accounts/$SA/tokens" | jq -r .value)
curl -s -H "Authorization: Bearer $SAT" localhost:8080/api/v1/me | jq -r .code
# service_account_not_allowed
curl -s -o /dev/null "${H[@]}" -X POST "localhost:8080/api/v1/service-accounts/$SA/disable"
curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $SAT" localhost:8080/api/v1/users
# 401
curl -s -o /dev/null "${H[@]}" -X POST "localhost:8080/api/v1/service-accounts/$SA/enable"
curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $SAT" localhost:8080/api/v1/users
# 200

# C-04.AC-9: an OIDC user without an offline token, after the grace period
curl -s -X POST localhost:18090/_fake/config -d '{"grant_offline_access":false}'
# sign in as the OIDC user olga through the fake IdP (as in S-013), create her token OLGA from her session, then:
psql "$MUSTER_DATABASE_URL" -qc "UPDATE users SET oidc_last_contact_at = oidc_last_contact_at - interval '8 days' WHERE login = 'olga'"
curl -s -H "Authorization: Bearer $OLGA" localhost:8080/api/v1/me | jq -c '{code, detail}'
# {"code":"oidc_recheck_required","detail":"Sign in through OIDC to make your tokens work again."}
# after olga signs in through the fake IdP again:
curl -s -o /dev/null -w '%{http_code}\n' -H "Authorization: Bearer $OLGA" localhost:8080/api/v1/me
# 200
```

The deleted and disabled owner cases (C-04.AC-4) and the refusal at a re-check (C-03.AC-22) run as end-to-end tests,
whose output the pull request records.

## Open questions

None.

## Notes

- Suggested commit: `feat(tokens): add personal access tokens, service accounts and token authentication`.
- P-09 (`api.rate_limit`) is confirmed or changed here.
- "Usable" Personal access tokens — not expired, not revoked, owner active — are what the background re-checks of S-062
  count when deciding whether to re-check a user.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-04.FR-1 | full | |
| C-04.FR-2 | partial | Service accounts and their tokens; the command refusals are S-032 and S-049 |
| C-04.FR-3 | partial | the API; the warning for tokens without expiry is S-017 |
| C-04.FR-4 | partial | the API side; ingestion refusing API tokens is S-018 |
| C-04.FR-5 | full | |
| C-04.FR-6 | full | |
| C-04.FR-7 | full | |
| C-04.FR-8 | partial | the API; the date shown in the profile is S-017 |
| C-04.AC-1 | full | |
| C-04.AC-2 | full | the single display in the UI is S-017 |
| C-04.AC-3 | full | |
| C-04.AC-4 | full | |
| C-04.AC-5 | full | |
| C-04.AC-7 | full | |
| C-04.AC-8 | full | |
| C-04.AC-9 | full | |
| C-03.FR-13 | partial | tokens of a deleted user |
| C-03.FR-27 | full | together with S-010 and S-012 |
| C-03.FR-30 | partial | the token effects |
| C-03.AC-14 | partial | everything except removing an Account link (S-051) |
| C-03.AC-22 | full | together with S-062 and S-014 |
| C-03.AC-24 | full | together with S-013 and S-062 |
