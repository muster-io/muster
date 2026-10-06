---
id: S-013
title: OIDC sign-in, account linking, background re-checks and the proxy settings (BE)
capability: C-03
kind: be
layer: L1
depends_on: [S-009, S-012]
covers: [C-03.FR-5, C-03.FR-6, C-03.FR-7, C-03.FR-8, C-03.FR-9, C-03.FR-10, C-03.FR-11, C-03.FR-14, C-03.FR-17, C-03.FR-19, C-03.FR-21, C-03.FR-24, C-03.FR-25, C-03.FR-28, C-03.FR-29, C-03.FR-30, C-03.AC-1, C-03.AC-6, C-03.AC-7, C-03.AC-8, C-03.AC-13, C-03.AC-16, C-03.AC-18, C-03.AC-20, C-03.AC-21, C-03.AC-22, C-03.AC-23, C-03.AC-24, C-03.FR-32, C-03.AC-26, C-02.FR-22, C-01.FR-13]
files_touched:
  - internal/oidc/settings.go
  - internal/oidc/provider.go
  - internal/oidc/signin.go
  - internal/oidc/link.go
  - internal/oidc/recheck.go
  - internal/oidc/query.sql
  - internal/oidc/signin_test.go
  - internal/oidc/recheck_test.go
  - internal/proxyconf/proxyconf.go
  - internal/proxyconf/proxyconf_test.go
  - internal/keyring/field.go
  - internal/db/claim.go
  - internal/db/claim_test.go
  - internal/api/oidc.go
  - internal/api/oidc_test.go
  - internal/api/sessions.go
  - internal/api/profile.go
  - internal/api/users.go
  - internal/users/admin.go
  - internal/auth/session.go
  - internal/cli/admin.go
  - internal/fakes/fakeoidc/fakeoidc.go
  - internal/fakes/fakeoidc/fakeoidc_test.go
  - internal/devmode/devmode.go
  - internal/runtime/runtime.go
  - internal/leader/tasks.go
  - internal/metrics/catalogue.go
  - internal/logging/events.go
  - internal/archlint/secretleak.go
  - docs/sign-in/oidc-keycloak.md
acceptance:
  - "[C-03.FR-5, C-03.FR-21, C-03.AC-6] Saving OIDC settings stores the client secret encrypted; reading them returns only `client_secret_status` (whether it is set and when it changed), and the Audit log diff says the secret changed without its value."
  - "[C-03.FR-6] With an empty mapping and `unmatched_role` `none` the settings carry the warning `nobody_can_sign_in`; after a check that finds the groups claim missing from discovery, `groups_claim_missing`."
  - "[C-03.FR-5, C-03.AC-7] With `client_secret_expires_on` 10 days ahead the settings carry the warning `secret_expiring` with that date; 30 days ahead they do not."
  - "[C-03.FR-8, C-03.FR-19, C-03.AC-8] The connection check fetches discovery from the fake IdP; with a proxy in the settings it reaches the IdP only through the fake proxy and reports `via: proxy`; a proxy at 169.254.169.254 fails the check, naming the rule."
  - "[C-03.FR-25, C-03.AC-16] `GET /api/v1/sessions/oidc/start` redirects to the IdP with PKCE `S256`, `state` and `nonce`; a person in no mapped group is redirected to `/sign-in?error=no_access`; a start with `return_to=//evil.example` or `return_to=https://evil.example` ignores it and lands on `/`."
  - "[C-03.FR-7, C-03.FR-28, C-03.AC-1] Against the fake IdP, a user with a mapped group is created on first sign-in with the mapped Role; a user without one is refused, and the Audit log entry lists the groups of the claim."
  - "[C-03.FR-28, C-03.AC-20] A first OIDC sign-in whose `preferred_username` is `alice` while a local user `Alice` exists is refused to `/sign-in?error=login_taken`, creates no user, changes nothing on `Alice` and writes an Audit log entry."
  - "[C-03.FR-9, C-03.FR-29, C-03.AC-20] When Alice links OIDC from her session, her other sessions end, her password no longer signs in (401), she signs in through OIDC into the same account with TOTP still asked, and `GET /api/v1/users` shows her `sign_in_method` `oidc`; the same identity linked from Bob's session ends with `identity_linked_elsewhere`; a link callback replayed in another session ends with `invalid_request`."
  - "[C-03.FR-11, C-03.FR-29, C-03.AC-21] `POST /api/v1/users/{user_id}/convert-to-local` removes Alice's identity, ends her sessions and returns a password setup link, and her next OIDC sign-in is refused with `login_taken`; `muster admin reset-password --actor ops` on an account created through OIDC sets a password, removes the identity and writes an Audit log entry naming `ops`."
  - "[C-03.FR-10, C-03.AC-13] After an OIDC sign-in a user with TOTP gets a session in the state `totp_required`, unless `oidc.skip_totp_with_idp_mfa` is on and the IdP asserted multi-factor authentication in `amr`."
  - "[C-03.FR-30, C-03.AC-22] After the fake IdP disables a signed-in user who holds an offline token, the next re-check — within one `auth.oidc_recheck_interval` — ends all of the user's sessions, the next request answers 401 `oidc_session_ended`, and the Audit log records the refusal; after a new OIDC sign-in the user's sessions work again."
  - "[C-03.FR-30, C-03.AC-23] While the fake IdP answers 503 to refresh requests, sessions stay active across several intervals, `muster_oidc_checks_total{outcome=\"unavailable\"}` grows and `oidc_check_failed` is logged; after the IdP recovers, the next check succeeds."
  - "[C-03.FR-30, C-03.AC-24] When the fake IdP grants no `offline_access`, an OIDC session ends `auth.oidc_fallback_session_lifetime` after sign-in although it is in use, and no background check runs for the user."
  - "[C-03.FR-24, C-03.AC-18] `GET /api/v1/sign-in-options` without credentials returns the OIDC button with its `display_name` and nothing about the version."
  - "[C-03.FR-32, C-03.AC-26] With `oidc.sync_role` on, when the fake IdP maps the last active Admin to a lower Role at an OIDC sign-in or a background re-check, the Role stays Admin and the sessions continue, the Audit log records `user.role_sync_kept_admin` with the mapped Role, and the OIDC settings carry the warning `last_admin_kept` naming the user and that Role; once a second Admin is active, the next sync applies the lower Role and the warning is gone."
  - "[C-01.FR-13] `muster dev` also starts the fake IdP on `127.0.0.1:18090` and the fake HTTP and SOCKS5 proxies on `127.0.0.1:18091` and `127.0.0.1:18092`, and the demo OIDC configuration \"Dev IdP\" signs a scripted user in through the fake IdP."
  - "[C-03.FR-17] docs/sign-in/oidc-keycloak.md covers the Keycloak client, the groups mapper and claim, both redirect URIs, allowing `offline_access` and what happens without it, and local users with TOTP for an IdP Muster cannot reach."
verify: "make ci test-integration e2e"
operator_attention: false
issue: 13
---

# S-013. OIDC sign-in, account linking, background re-checks and the proxy settings (BE)

## Scope

**IN**

- OIDC settings with the write-only client secret, the expiry date, the group-to-Role mapping, the switches, the
  warnings and the connection check.
- The shared proxy settings object (C-03.FR-19) and the write-only rule for every secret field (C-03.FR-21).
- The OIDC sign-in redirect flow with PKCE, user creation on first sign-in and the refusals.
- Linking an OIDC identity from the profile, conversion back to local, and the OIDC parts of `reset-password`,
  `role_locked` and `local_user_only`.
- The offline token and the background re-checks at the IdP, with the shared claim-with-lease helper.
- The fake OIDC IdP, and in `muster dev` the fake IdP, the fake proxies and a demo OIDC configuration.
- The Keycloak walkthrough in the documentation.

**OUT**

- The effect of a refusal or of the grace period on Personal access tokens (`oidc_recheck_required`, S-016).
- The `MusterOIDCSecretExpiring` Internal alert (S-053); the OIDC settings page (S-015); the sign-in button, the error
  texts and "Link OIDC" in the SPA (S-014).

## Contracts

- **Operations implemented**: `getOidcSettings`, `updateOidcSettings`, `checkOidcSettings`, `startOidcSignIn`,
  `completeOidcSignIn`, `startOidcLink`, `completeOidcLink`, `convertUserToLocal`; `getSignInOptions` gains the OIDC
  button. Schemas: `OidcSettings`, `OidcSettingsBase`, `OidcSettingsInput`, `OidcGroupMapping`, `OidcUnmatchedRole`,
  `OidcWarning`, `OidcCheckResult`, `OidcLinkStart`, `ProxyConfig`, `ProxyConfigInput`, `ProxyType`, `SecretStatus`,
  `ConnectionPath`.
- **Settings** (C-03.FR-5, FR-6, `oidc_settings`): `enabled`, `display_name` (default: the host of the issuer URL),
  `issuer_url`, `client_id`, `client_secret` (write-only), `client_secret_expires_on`, `scopes`, `groups_claim`,
  `group_mappings`, `unmatched_role` (`oidc.unmatched_role`), `sync_role` (`oidc.sync_role`), `skip_totp_with_idp_mfa`
  (`oidc.skip_totp_with_idp_mfa`) and `proxy`; updates need `If-Match`. `warnings`: `nobody_can_sign_in` (empty mapping
  and no Role for unmatched users), `groups_claim_missing` (the last check found discovery's `claims_supported` without
  the claim, or a sign-in token without it), `secret_expiring` (the expiry is less than `oidc.secret_expiry_lead` away;
  with `expires_on`), `last_admin_kept` (the latest `user.role_sync_kept_admin` entry names a user who is still the last
  active Admin; with that user and the mapped Role, the fields `user` and `role` of `OidcWarning`).
- **Secret fields** (C-03.FR-21, ADR-0011; `internal/keyring/field.go`): an omitted value keeps the stored one, `null`
  clears it, a string replaces it encrypted with the Keyring; reads return `SecretStatus` only; Audit log diffs show
  `secret_changed`. Every later Secret uses this helper.
- **Proxy settings** (C-03.FR-19, C-02.FR-22; `internal/proxyconf`): `{enabled, type (http, https, socks5), address
  (host:port), username, password (write-only)}`, stored as `proxy jsonb` beside the password triple, validated, and
  turned into the outbound client configuration of S-009. Connections, outgoing webhook Destinations and the outgoing
  heartbeat reuse it.
- **Connection check** (C-03.FR-8): `checkOidcSettings` fetches `<issuer>/.well-known/openid-configuration` through the
  outbound package (interactive class, the OIDC proxy) and returns `ok`, `via` (`direct` or `proxy`), `latency_ms`,
  the endpoints, a masked `error` and warnings. Key sets are fetched by the background class and the last good keys are
  kept.
- **Sign-in flow** (C-03.FR-25, FR-28, FR-7, FR-10; `oidc_auth_requests`): `startOidcSignIn` stores a request keyed by
  the hash of `state`, with the nonce, the PKCE verifier (a Secret), `return_to` (kept only when it is a relative path
  starting with a single `/`) and purpose `sign_in`, valid for `oidc.auth_request_ttl`, and redirects with the
  configured scopes plus `openid` and `offline_access` (`409 oidc_not_enabled` when OIDC is off).
  The story adds `oidc_auth_requests` to the `short_lived_pruning` Leader task (a `PruneOIDC` field of `leader.Work`
  and the table in `metrics.ShortLivedTables`): a request is deleted once its `expires_at` is more than 1 h past; a
  link request also goes with its web session, by the cascade from `sessions`.
  `completeOidcSignIn` exchanges the code (interactive class), verifies the ID token (signature, issuer, audience,
  nonce, and `exp` and `iat` against the real clock of S-006) and maps the groups claim to a Role (the highest wins, C-03.FR-5). Outcomes, always a `302`: success to `return_to`
  or `/` with the session cookie; failures to `/sign-in?error=` `no_access` (Audit `session.oidc_refused` with the
  groups of the claim; `muster_login_failures_total{method="oidc"}`), `login_taken` (Audit entry, nothing created),
  `account_disabled`, `oidc_disabled`, `invalid_request` and `idp_error`. A known identity signs into its account (the
  Role follows the mapping when `sync_role` is on, except that it never lowers the last active Admin, C-03.FR-32); a new
  one creates a user with source `oidc` and the login from `preferred_username`, else the email, else `sub`; accounts
  are never merged by login or email. The session has the method `oidc`; it starts in the state `totp_required` when the
  user has TOTP, unless `oidc.skip_totp_with_idp_mfa` is on and the IdP asserted multi-factor authentication in `amr`;
  the policy `everyone` makes it `totp_enrolment_required` for a user without TOTP.
- **Offline token** (C-03.FR-30, `users`, `oidc_checks`): when the granted scope includes `offline_access` and a refresh
  token comes back, it is stored on the user as a Secret (replacing the previous one), `oidc_last_contact_at` is set,
  `oidc_refused_at` cleared and the user's `oidc_checks` row made due one `auth.oidc_recheck_interval` later. Without
  one, the session's `expires_at` is at most `auth.oidc_fallback_session_lifetime` after sign-in and no check row
  exists. Disabling, deleting or converting the user wipes the token and the row.
- **Background re-checks** (C-03.FR-30): due `oidc_checks` rows are claimed by any replica with `FOR UPDATE SKIP LOCKED`
  and a lease through `internal/db/claim.go`, the shared claim helper that ingestion, delivery and timers reuse. The
  helper sets and compares `lease_until` on the real clock and due times — here `deadline` — on the business clock
  (S-006), so a claim query takes both. The re-check worker runs per Organization: it iterates over the Organizations
  (one in L1) and passes `org_id` to every claim and query (lint 1). A user
  with no live session and no usable Personal access token is `skipped` without calling the IdP. The refresh goes
  through the background class and the OIDC proxy, and a rotated refresh token is stored. A refusal (`invalid_grant` or
  another `4xx` except `408` and `429`) ends every session of the user (next request `401 oidc_session_ended`), sets
  `oidc_refused_at`, wipes the token, removes the row and writes `user.oidc_refused`. Unavailability (unreachable,
  timeout, `408`, `429`, `5xx`) changes nothing; the next attempt is one interval later and `oidc_check_failed` (WARN) is
  logged. A success records the contact and, with `sync_role` and a groups claim in the refreshed token, updates the
  Role — a changed Role ends the sessions, and groups that map to nothing with no Role for unmatched users count as a
  refusal. Every check increments `muster_oidc_checks_total{outcome}` (`ok`, `refused`, `unavailable`, `skipped`).
- **The last active Admin** (C-03.FR-32, C-03.FR-31): when Role sync — at a sign-in or a re-check — would give the last
  active Admin a lower Role, the Role stays Admin under the lock of S-011's `last_admin` check, the sessions continue,
  and `user.role_sync_kept_admin` records the mapped Role in `details.mapped_role`; the next sync after another Admin
  became active applies the mapping.
- **Linking and conversion** (C-03.FR-29, FR-11, FR-9): `startOidcLink` (web session, an account with a password;
  `409 oidc_already_linked`) returns `OidcLinkStart` for a request with purpose `link`, `link_user_id` and
  `link_session_id`. `completeOidcLink` is accepted only in the session that started it (`invalid_request` otherwise);
  an identity held by another user ends with `identity_linked_elsewhere`; success sets the identity and wipes the
  password in one update, ends the user's other sessions and continues the current one as an OIDC session, then
  redirects to `/profile` (failures to `/profile?error=<code>`). `convertUserToLocal` (`users:write`) removes the
  identity, wipes the token, ends the sessions and returns `UserCreated` with a password setup link (`409
  oidc_not_linked` for an account that does not sign in through OIDC). `muster admin reset-password` on an OIDC account
  removes the identity in the same update. `updateUser` answers `409 role_locked` for a Role change of an OIDC account
  while `oidc.sync_role` is on; `changePassword` and `createPasswordSetupLink` answer `409 local_user_only` for an
  account without a password.
- **Audit log actions**: `oidc_settings.updated`, `session.oidc_refused`, `user.oidc_linked`, `user.oidc_link_refused`,
  `user.converted_to_local`, `user.oidc_refused`, `user.role_sync_kept_admin`; sign-ins and user creation reuse
  `session.signed_in` and `user.created` with the method `oidc`.
- **Fake IdP** (`internal/fakes/fakeoidc`): discovery, key set, an authorization endpoint that approves the scripted
  next user (`sub`, `preferred_username`, `email`, `groups`, `amr`), a token endpoint with refresh-token rotation, and
  control endpoints under `/_fake/`: the next user, disabling a user (refresh answers `invalid_grant`), granting or
  withholding `offline_access`, omitting the groups claim from discovery, and the faults of the harness.
- **Development mode**: `muster dev` also starts the fake IdP on `127.0.0.1:18090`, a fake HTTP proxy on
  `127.0.0.1:18091` and a fake SOCKS5 proxy on `127.0.0.1:18092`; its demo configuration enables OIDC against the fake
  IdP (display name `Dev IdP`, mapping `muster-admins` → admin, `oncall` → responder) and adds `127.0.0.0/8` to the
  Organization's allowed networks so that Muster may call the fakes.
- **Documentation** (C-03.FR-17): `docs/sign-in/oidc-keycloak.md` — the Keycloak client, the groups mapper and claim,
  the redirect URIs `MUSTER_PUBLIC_URL/api/v1/sessions/oidc/callback` and
  `MUSTER_PUBLIC_URL/api/v1/me/oidc-identity/callback`, allowing `offline_access`, what happens without it (no
  background re-checks, sessions end after `auth.oidc_fallback_session_lifetime`, Personal access tokens follow
  `auth.oidc_token_grace`), and local users with TOTP where the IdP cannot be reached.

## Steps

1. Write the secret-field helper and the proxy settings object. Check: tests cover keep, clear and replace, the masked
   reads and the `secret_changed` diff.
2. Write the settings, the warnings and the connection check, with the fake IdP and the fake proxies. Check: tests
   cover each warning and the check directly, through each proxy type and with a blocked proxy address.
3. Write the sign-in flow and user creation. Check: integration tests against the fake IdP cover each outcome and
   `return_to` handling.
4. Write linking, conversion and the OIDC parts of `reset-password`, `role_locked` and `local_user_only`. Check: tests
   cover the scenario of C-03.AC-20 and C-03.AC-21.
5. Write the claim helper, the offline token and the re-checks. Check: tests with a manual clock cover refusal,
   unavailability, success with a Role change, the last active Admin kept at a sign-in and at a re-check, skipping and
   the fallback lifetime.
6. Extend `muster dev` and write the Keycloak page. Check: Verification below; the page lists every item of C-03.FR-17.

## Verification

```sh
make dev &      # fake IdP on :18090, fake HTTP proxy on :18091, fake SOCKS5 proxy on :18092
# the Admin's session as in S-011: cookie jar `jar`, CSRF token in `CSRF`, H=(-b jar -H "X-CSRF-Token: $CSRF" ...)
curl -s localhost:8080/api/v1/sign-in-options
# {"oidc":{"enabled":true,"display_name":"Dev IdP"}}

# first sign-in with a mapped group (C-03.AC-1)
curl -s -X POST localhost:18090/_fake/next-user -d '{"sub":"u-1","preferred_username":"olga","groups":["oncall"]}'
curl -s -c olga -b olga -L -o /dev/null -w '%{url_effective}\n' 'localhost:8080/api/v1/sessions/oidc/start?return_to=/profile'
# http://localhost:8080/profile
curl -s -b olga localhost:8080/api/v1/me | jq -c '.user | {login, role, sign_in_method}'
# {"login":"olga","role":"responder","sign_in_method":"oidc"}

# no mapped group, and open-redirect attempts (C-03.AC-1, C-03.AC-16)
curl -s -X POST localhost:18090/_fake/next-user -d '{"sub":"u-2","preferred_username":"nora","groups":["contractors"]}'
curl -s -c n -b n -L -o /dev/null -w '%{url_effective}\n' 'localhost:8080/api/v1/sessions/oidc/start'
# http://localhost:8080/sign-in?error=no_access
curl -s -b jar 'localhost:8080/api/v1/audit-log?action=session.oidc_refused' | jq -c '.items[0].details'
# {"reason":"no_access","groups":["contractors"]}
curl -s -X POST localhost:18090/_fake/next-user -d '{"sub":"u-1","preferred_username":"olga","groups":["oncall"]}'
curl -s -c o2 -b o2 -L -o /dev/null -w '%{url_effective}\n' 'localhost:8080/api/v1/sessions/oidc/start?return_to=//evil.example'
# http://localhost:8080/

# settings: the secret is write-only (C-03.AC-6); expiry warning (C-03.AC-7); the check through a proxy (C-03.AC-8)
S=$(curl -s -b jar localhost:8080/api/v1/oidc-settings)
curl -s "${H[@]}" -X PUT -H "If-Match: $(jq -r .etag <<<"$S")" localhost:8080/api/v1/oidc-settings \
  -d "$(jq -c --arg d "$(date -u -v+10d +%F 2>/dev/null || date -u -d '+10 days' +%F)" \
    'del(.etag, .client_secret_status, .warnings, .updated_at) | .client_secret = "s3cr3t-new" | .client_secret_expires_on = $d
     | .proxy = {enabled: true, type: "socks5", address: "127.0.0.1:18092"}' <<<"$S")" \
  | jq -c '{client_secret_status, warnings: [.warnings[].kind]}'
# {"client_secret_status":{"set":true,"updated_at":"…"},"warnings":["secret_expiring"]}
curl -s -b jar 'localhost:8080/api/v1/audit-log?action=oidc_settings.updated' | jq -c '.items[0].diff[] | select(.field == "client_secret")'
# {"field":"client_secret","secret_changed":true}
curl -s "${H[@]}" -X POST localhost:8080/api/v1/oidc-settings/checks | jq -c '{ok, via}'
# {"ok":true,"via":"proxy"}
curl -s localhost:18092/_fake/requests | jq -r '.[-1].target'
# 127.0.0.1:18090
# with the proxy address changed to 169.254.169.254:1080:
# {"ok":false,"error":"blocked by the outbound address policy: proxy 169.254.169.254 is link-local (always blocked)"}

# background re-check refusal (C-03.AC-22), triggered by making the check due now
curl -s -X POST localhost:18090/_fake/users/u-1/disable
psql "$MUSTER_DATABASE_URL" -qc "UPDATE oidc_checks SET deadline = now()"
sleep 5; curl -s -b olga localhost:8080/api/v1/me | jq -r .code
# oidc_session_ended

# the IdP unavailable (C-03.AC-23)
curl -s -X POST localhost:18090/_fake/faults -d '{"path":"/token","status":503}'
psql "$MUSTER_DATABASE_URL" -qc "UPDATE oidc_checks SET deadline = now()"
sleep 5; curl -s localhost:8082/metrics | grep 'muster_oidc_checks_total{outcome="unavailable"}'
# muster_oidc_checks_total{outcome="unavailable"} 1

# C-03.AC-26: the last active Admin keeps the Role
curl -s -X DELETE localhost:18090/_fake/faults
curl -s -X POST localhost:18090/_fake/next-user -d '{"sub":"u-9","preferred_username":"ada","groups":["muster-admins"]}'
curl -s -c ada -b ada -L -o /dev/null 'localhost:8080/api/v1/sessions/oidc/start'
# give the bootstrap Admin the Role responder (ada is the other Admin), then let the IdP map ada to responder:
curl -s -X POST localhost:18090/_fake/next-user -d '{"sub":"u-9","preferred_username":"ada","groups":["oncall"]}'
curl -s -c ada -b ada -L -o /dev/null 'localhost:8080/api/v1/sessions/oidc/start'
curl -s -b ada localhost:8080/api/v1/me | jq -r .user.role                                       # admin
curl -s -b ada localhost:8080/api/v1/oidc-settings | jq -c '.warnings[] | select(.kind == "last_admin_kept") | {u: .user.login, role}'
# {"u":"ada","role":"responder"}
curl -s -b ada 'localhost:8080/api/v1/audit-log?action=user.role_sync_kept_admin' | jq -r '.items[0].details.mapped_role'
# responder
# make another user Admin, then ada signs in again with the same groups:
curl -s -c ada -b ada -L -o /dev/null 'localhost:8080/api/v1/sessions/oidc/start'
curl -s -b ada localhost:8080/api/v1/me | jq -r .user.role                                       # responder
```

The linking scenario of C-03.AC-20, the conversion of C-03.AC-21 and the fallback lifetime of C-03.AC-24 run as
end-to-end tests against `muster dev` (`make e2e`), and the pull request records their output.

## Open questions

1. Where the groups claim is read: proposal — from the ID token, and from the userinfo endpoint when the ID token lacks
   it; a re-check whose refresh returns no ID token leaves the Role unchanged (C-03.FR-30 syncs the Role only "when they
   carry the groups claim").

## Notes

- Suggested commit: `feat(auth): add oidc sign-in, account linking, background re-checks and proxy settings`.
- The claim helper written here — claim in a short transaction, lease, backoff with jitter, slow work outside the
  transaction — is the one of ADR-0006 that ingestion, delivery and timers reuse.
- The development configuration allows loopback only inside `muster dev`; a real installation keeps the standard
  policy.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-03.FR-5 | partial | the API; the page is S-015, the Internal alert S-053 |
| C-03.FR-6 | partial | the warning kinds; their text on the page is S-015 |
| C-03.FR-7 | partial | refusal and Audit log; the text on the sign-in page is S-014 |
| C-03.FR-8 | full | |
| C-03.FR-9 | partial | the OIDC parts and re-checks |
| C-03.FR-10 | partial | TOTP after an OIDC sign-in |
| C-03.FR-11 | partial | `reset-password` on OIDC accounts |
| C-03.FR-14 | partial | OIDC entry types |
| C-03.FR-17 | full | |
| C-03.FR-19 | partial | the object and its API; the form is S-015 |
| C-03.FR-21 | full | |
| C-03.FR-24 | partial | the OIDC button in `sign-in-options` |
| C-03.FR-25 | partial | the flow; the error texts on the page are S-014 |
| C-03.FR-28 | partial | the API; the text on the page is S-014 |
| C-03.FR-29 | partial | the API; "Link OIDC" with its warning is S-014, conversion on the Users page S-015 |
| C-03.FR-30 | partial | sessions and re-checks; the token effects are S-016 |
| C-03.AC-1 | full | |
| C-03.AC-6 | full | |
| C-03.AC-7 | partial | the warning in the API; the page is S-015 |
| C-03.AC-8 | full | |
| C-03.AC-13 | full | together with S-012 |
| C-03.AC-16 | full | |
| C-03.AC-18 | full | |
| C-03.AC-20 | partial | the API flow; the warning in the profile is S-014 |
| C-03.AC-21 | full | |
| C-03.AC-22 | partial | sessions and Audit log; the token part S-016, the SPA part S-014 |
| C-03.AC-23 | full | |
| C-03.AC-24 | partial | the session part; the token part is S-016 |
| C-03.FR-32 | partial | the API; the warning on the page is S-015 |
| C-03.AC-26 | partial | the API; the warning on the page is S-015 |
| C-02.FR-22 | partial | the settings object; the mechanism is S-009 |
| C-01.FR-13 | partial | the fake IdP; in `muster dev` with the fake proxies and the demo OIDC configuration |
