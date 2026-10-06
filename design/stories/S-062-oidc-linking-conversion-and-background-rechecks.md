---
id: S-062
title: OIDC account linking, conversion to local, the offline token and background re-checks (BE)
capability: C-03
kind: be
layer: L1
depends_on: [S-013]
covers: [C-03.FR-9, C-03.FR-11, C-03.FR-14, C-03.FR-29, C-03.FR-30, C-03.FR-32, C-03.AC-20, C-03.AC-21, C-03.AC-22, C-03.AC-23, C-03.AC-24, C-03.AC-26]
files_touched:
  - internal/oidc/link.go
  - internal/oidc/recheck.go
  - internal/oidc/signin.go
  - internal/oidc/query.sql
  - internal/oidc/link_test.go
  - internal/oidc/recheck_test.go
  - internal/oidc/oidc_integration_test.go
  - internal/db/claim.go
  - internal/db/claim_test.go
  - internal/api/profile.go
  - internal/api/users.go
  - internal/api/server.go
  - internal/api/problem.go
  - internal/api/oidc_test.go
  - internal/api/users_test.go
  - internal/auth/session.go
  - internal/users/admin.go
  - internal/users/setup.go
  - internal/users/query.sql
  - internal/users/admin_test.go
  - internal/runtime/runtime.go
  - internal/runtime/runtime_test.go
  - internal/metrics/catalogue.go
  - internal/logging/events.go
  - internal/archlint/secretleak.go
  - test/e2e/oidc_test.go
acceptance:
  - "[C-03.FR-9, C-03.FR-29, C-03.AC-20] When Alice links OIDC from her session, her other sessions end, her password no longer signs in (401), she signs in through OIDC into the same account with TOTP still asked, and `GET /api/v1/users` shows her `sign_in_method` `oidc`; the same identity linked from Bob's session ends with `identity_linked_elsewhere`; a link callback replayed in another session ends with `invalid_request`."
  - "[C-03.FR-11, C-03.FR-29, C-03.AC-21] `POST /api/v1/users/{user_id}/convert-to-local` removes Alice's identity, ends her sessions and returns a password setup link, and her next OIDC sign-in is refused with `login_taken`; `muster admin reset-password --actor ops` on an account created through OIDC sets a password, removes the identity and writes an Audit log entry naming `ops`."
  - "[C-03.FR-29] `updateUser` answers 409 `role_locked` for a Role change of an OIDC account while OIDC and `oidc.sync_role` are both on; `changePassword` and `createPasswordSetupLink` answer 409 `local_user_only` for an account that signs in through OIDC."
  - "[C-03.FR-30, C-03.AC-22] After the fake IdP disables a signed-in user who holds an offline token, the next re-check — within one `auth.oidc_recheck_interval` — ends all of the user's sessions, the next request answers 401 `oidc_session_ended`, and the Audit log records the refusal; after a new OIDC sign-in the user's sessions work again."
  - "[C-03.FR-30, C-03.AC-23] While the fake IdP answers 503 to refresh requests, sessions stay active across several intervals, `muster_oidc_checks_total{outcome=\"unavailable\"}` grows and `oidc_check_failed` is logged; after the IdP recovers, the next check succeeds."
  - "[C-03.FR-30, C-03.AC-24] When the fake IdP grants no `offline_access`, an OIDC session ends `auth.oidc_fallback_session_lifetime` after sign-in although it is in use, and no background check runs for the user."
  - "[C-03.FR-32, C-03.AC-26] With `oidc.sync_role` on, when the fake IdP maps the last active Admin to a lower Role at a background re-check, the Role stays Admin and the sessions continue, the Audit log records `user.role_sync_kept_admin` with the mapped Role, and the OIDC settings carry the warning `last_admin_kept`; once a second Admin is active, the next re-check applies the lower Role and the warning is gone."
verify: "make ci test-integration e2e"
operator_attention: false
issue: null
---

# S-062. OIDC account linking, conversion to local, the offline token and background re-checks (BE)

## Scope

**IN**

- Linking an OIDC identity from the profile, conversion back to local, and the OIDC parts of `reset-password`,
  `role_locked` and `local_user_only`.
- The offline token and the background re-checks at the IdP, with the shared claim-with-lease helper.
- Role sync at a re-check, with the rule for the last active Admin of S-013.

**OUT**

- OIDC settings, the proxy settings, the sign-in flow, the fake IdP and its place in `muster dev` (S-013).
- The effect of a refusal or of the grace period on Personal access tokens (`oidc_recheck_required`, S-016).
- "Link OIDC" with its warning and the ended-session page in the SPA (S-014); conversion on the Users page (S-015).

## Contracts

- **Operations implemented**: `startOidcLink`, `completeOidcLink`, `convertUserToLocal`; `updateUser`,
  `changePassword` and `createPasswordSetupLink` gain their OIDC refusals. Schemas: `OidcLinkStart`, `UserCreated`.
- **Linking and conversion** (C-03.FR-29, FR-11, FR-9): `startOidcLink` (web session, an account with a password;
  `409 oidc_already_linked`, `409 oidc_not_enabled`) returns `OidcLinkStart` for a request with purpose `link`,
  `link_user_id` and `link_session_id`, with the PKCE, state and nonce of S-013 and `offline_access` requested.
  `completeOidcLink` is accepted only in the session that started it (`invalid_request` otherwise); an identity held by
  another user ends with `identity_linked_elsewhere` (Audit `user.oidc_link_refused`); success sets the identity and
  wipes the password in one update, ends the user's other sessions (`oidc_linked`) and continues the current one as an
  OIDC session, then redirects to `/profile` (failures to `/profile?error=<code>`). The TOTP enrolment stays.
  `convertUserToLocal` (`users:write`) removes the identity, wipes the offline token and its `oidc_checks` row, ends
  the sessions (`converted_to_local`) and returns `UserCreated` with a password setup link (`409 oidc_not_linked` for
  an account that does not sign in through OIDC). `muster admin reset-password` on an OIDC account removes the identity
  and wipes the offline token in the same update. `updateUser` answers `409 role_locked` for a Role change of an OIDC
  account while OIDC is enabled and `oidc.sync_role` is on — the IdP decides the Role only while OIDC is on — and the
  field `role_locked` of `User` says so;
  `changePassword` and `createPasswordSetupLink` answer `409 local_user_only` for an account that signs in through
  OIDC (a local account still waiting for its first password gets its setup link).
- **Offline token** (C-03.FR-30, `users`, `oidc_checks`): when the granted scope includes `offline_access` and a refresh
  token comes back — at a sign-in or a link — it is stored on the user as a Secret (replacing the previous one),
  `oidc_last_contact_at` is set, `oidc_refused_at` cleared and the user's `oidc_checks` row made due one
  `auth.oidc_recheck_interval` later. Without one, the session's `expires_at` is at most
  `auth.oidc_fallback_session_lifetime` after sign-in (S-013) and no check row exists. Disabling, deleting or converting
  the user wipes the token and the row.
- **Background re-checks** (C-03.FR-30): due `oidc_checks` rows are claimed by any replica with `FOR UPDATE SKIP LOCKED`
  and a lease through `internal/db/claim.go`, the shared claim helper that ingestion, delivery and timers reuse. The
  helper sets and compares `lease_until` on the real clock and due times — here `deadline` — on the business clock
  (S-006), so a claim query takes both. The worker polls every 2 s on every replica and runs per Organization: it
  iterates over the Organizations (one in L1) and passes `org_id` to every claim and query (lint 1). A user with no
  live session and no usable Personal access token is `skipped` without calling the IdP. The refresh goes through the
  background class and the OIDC proxy with a budget of 10 s for its attempts, and the lease (60 s) outlasts it; a
  rotated refresh token is stored. A refusal (`invalid_grant` or another `4xx` except `408` and `429`) ends every
  session of the user (`idp_refused`; the next request answers `401 oidc_session_ended`), sets `oidc_refused_at`, wipes
  the token, removes the row and writes `user.oidc_refused`. Unavailability (unreachable, timeout, `408`, `429`, `5xx`,
  the budget spent) changes nothing; the next attempt is one interval later and `oidc_check_failed` (WARN) is logged. A
  success records the contact and, with `sync_role` and a groups claim in a refreshed ID token, updates the Role — a
  changed Role ends the sessions, and groups that map to nothing with no Role for unmatched users count as a refusal; a
  refresh that returns no ID token, or an ID token without the groups claim, leaves the Role unchanged (D247). Every
  check increments `muster_oidc_checks_total{outcome}` (`ok`, `refused`, `unavailable`, `skipped`).
- **The last active Admin** (C-03.FR-32): a re-check that would give the last active Admin a lower Role keeps Admin
  under the `last_admin` lock, the sessions continue, and `user.role_sync_kept_admin` records the mapped Role, as at a
  sign-in (S-013).
- **Audit log actions**: `user.oidc_linked`, `user.oidc_link_refused`, `user.converted_to_local`, `user.oidc_refused`,
  `user.role_sync_kept_admin`; `user.password_reset` of the CLI records the removed identity.
- **The 2 s poll and the 10 s budget** are implementation constants of the worker, not settings, like the Leader's
  intervals; `auth.oidc_recheck_interval` stays the only setting of the re-checks.

## Steps

1. Write the claim helper. Check: integration tests cover the lease on the real clock, due times on the business clock,
   a row whose lease ran out claimed again, and two claimers that never take the same row.
2. Write linking, conversion and the OIDC parts of `reset-password`, `role_locked` and `local_user_only`. Check: tests
   cover the scenario of C-03.AC-20 and C-03.AC-21.
3. Store the offline token and write the re-checks. Check: tests with a manual clock cover refusal, unavailability,
   success with a Role change, the last active Admin kept at a re-check, skipping and the fallback lifetime.

## Verification

```sh
make dev &      # with the fake IdP, the fake proxies and the demo OIDC configuration of S-013
# the Admin's session as in S-011: cookie jar `jar`, CSRF token in `CSRF`, H=(-b jar -H "X-CSRF-Token: $CSRF" ...)
# olga signs in through the fake IdP as in S-013; the fake grants offline_access by default
curl -s -X POST localhost:18090/_fake/next-user -d '{"sub":"u-1","preferred_username":"olga","groups":["oncall"]}'
curl -s -c olga -b olga -L -o /dev/null 'localhost:8080/api/v1/sessions/oidc/start'
curl -s -b jar "localhost:8080/api/v1/users?q=olga" | jq -r '.items[0].oidc_offline_access'
# true

# background re-check refusal (C-03.AC-22), triggered by making the check due now
curl -s -X POST localhost:18090/_fake/users/u-1/disable
psql "$MUSTER_DATABASE_URL" -qc "UPDATE oidc_checks SET deadline = now()"
sleep 5; curl -s -b olga localhost:8080/api/v1/me | jq -r .code
# oidc_session_ended

# the IdP unavailable (C-03.AC-23): the 2 s poll plus the 10 s budget of the refresh
curl -s -X POST localhost:18090/_fake/users/u-1/enable
curl -s -c olga -b olga -L -o /dev/null 'localhost:8080/api/v1/sessions/oidc/start'
curl -s -X POST localhost:18090/_fake/faults -d '{"path":"/token","status":503}'
psql "$MUSTER_DATABASE_URL" -qc "UPDATE oidc_checks SET deadline = now()"
sleep 15; curl -s localhost:8082/metrics | grep 'muster_oidc_checks_total{outcome="unavailable"}'
# muster_oidc_checks_total{outcome="unavailable"} 1
curl -s -o /dev/null -w '%{http_code}\n' -b olga localhost:8080/api/v1/me
# 200

# C-03.AC-26 at a re-check: ada is the last active Admin and the IdP now maps her to oncall
curl -s -X DELETE localhost:18090/_fake/faults
curl -s -X POST localhost:18090/_fake/next-user -d '{"sub":"u-9","preferred_username":"ada","groups":["oncall"]}'
psql "$MUSTER_DATABASE_URL" -qc "UPDATE oidc_checks SET deadline = now()"
sleep 15; curl -s -b ada localhost:8080/api/v1/me | jq -r .user.role                            # admin
curl -s -b ada 'localhost:8080/api/v1/audit-log?action=user.role_sync_kept_admin' | jq -r '.items[0].details.mapped_role'
# responder

# conversion (C-03.AC-21)
curl -s "${H[@]}" -X POST "localhost:8080/api/v1/users/$OLGA_ID/convert-to-local" | jq -c '{m: .user.sign_in_method, link: (.password_setup_link.url | test("#token="))}'
# {"m":"local","link":true}
./bin/muster dev admin reset-password --actor ops --login ada   # prompts for the password
curl -s -b jar 'localhost:8080/api/v1/audit-log?action=user.password_reset' | jq -c '.items[0] | {actor: .actor.name, d: .details.oidc_identity_removed}'
# {"actor":"ops","d":true}
```

The linking scenario of C-03.AC-20, the conversion of C-03.AC-21 and the fallback lifetime of C-03.AC-24 run as
end-to-end tests against `muster dev` (`make e2e`, `test/e2e/oidc_test.go`), and the pull request records their output.

## Open questions

None. Where the groups claim is read was settled in S-013 (D247).

## Notes

- Suggested commit: `feat(auth): add oidc account linking, conversion to local and background re-checks`.
- Split from S-013 before implementation (S-013 kept settings, the proxy object and sign-in).
- The claim helper written here — claim in a short transaction, lease, backoff with jitter, slow work outside the
  transaction — is the one of ADR-0006 that ingestion (S-020), timers (S-028) and delivery (S-034) reuse.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-03.FR-9 | partial | the OIDC parts and re-checks; local sessions are S-010 and S-011 |
| C-03.FR-11 | partial | `reset-password` on OIDC accounts; the page is S-015 |
| C-03.FR-14 | partial | the entry types of linking, conversion and re-checks |
| C-03.FR-29 | partial | the API; "Link OIDC" with its warning is S-014, conversion on the Users page S-015 |
| C-03.FR-30 | partial | the offline token and re-checks; the token effects are S-016 |
| C-03.FR-32 | partial | Role sync at a re-check; at a sign-in S-013, the warning on the page S-015 |
| C-03.AC-20 | partial | the API flow; the warning in the profile is S-014 |
| C-03.AC-21 | full | |
| C-03.AC-22 | partial | sessions and Audit log; the token part S-016, the SPA part S-014 |
| C-03.AC-23 | full | |
| C-03.AC-24 | partial | the session part; the token part is S-016 |
| C-03.AC-26 | partial | at a re-check; at a sign-in S-013, the warning on the page S-015 |
