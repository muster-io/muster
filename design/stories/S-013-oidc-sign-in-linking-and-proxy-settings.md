---
id: S-013
title: OIDC settings, proxy settings and OIDC sign-in (BE)
capability: C-03
kind: be
layer: L1
depends_on: [S-009, S-012]
covers: [C-03.FR-5, C-03.FR-6, C-03.FR-7, C-03.FR-8, C-03.FR-10, C-03.FR-14, C-03.FR-17, C-03.FR-19, C-03.FR-21, C-03.FR-24, C-03.FR-25, C-03.FR-28, C-03.FR-30, C-03.FR-32, C-03.AC-1, C-03.AC-6, C-03.AC-7, C-03.AC-8, C-03.AC-13, C-03.AC-16, C-03.AC-18, C-03.AC-26, C-02.FR-22, C-01.FR-13]
files_touched:
  - internal/oidc/settings.go
  - internal/oidc/provider.go
  - internal/oidc/signin.go
  - internal/oidc/query.sql
  - internal/oidc/settings_test.go
  - internal/oidc/provider_test.go
  - internal/oidc/signin_test.go
  - internal/oidc/oidc_integration_test.go
  - internal/proxyconf/proxyconf.go
  - internal/proxyconf/proxyconf_test.go
  - internal/keyring/field.go
  - internal/keyring/field_test.go
  - internal/api/oidc.go
  - internal/api/oidc_test.go
  - internal/api/sessions.go
  - internal/api/server.go
  - internal/api/problem.go
  - internal/auth/session.go
  - internal/auth/query.sql
  - internal/fakes/fakeoidc/fakeoidc.go
  - internal/fakes/fakeoidc/fakeoidc_test.go
  - internal/fakes/fakeproxy/fakeproxy.go
  - internal/fakes/fakeproxy/fakeproxy_test.go
  - internal/devmode/devmode.go
  - internal/devmode/devmode_test.go
  - internal/cli/dev.go
  - internal/cli/dev_test.go
  - internal/runtime/runtime.go
  - internal/runtime/runtime_test.go
  - internal/leader/tasks.go
  - internal/metrics/catalogue.go
  - internal/logging/events.go
  - internal/archlint/secretleak.go
  - sqlc.yaml
  - go.mod
  - api/openapi.yaml
  - docs/sign-in/oidc-keycloak.md
acceptance:
  - "[C-03.FR-5, C-03.FR-21, C-03.AC-6] Saving OIDC settings stores the client secret encrypted; reading them returns only `client_secret_status` (whether it is set and when it changed), and the Audit log diff says the secret changed without its value."
  - "[C-03.FR-6] With an empty mapping and `unmatched_role` `none` the settings carry the warning `nobody_can_sign_in`; after a check that finds the groups claim missing from discovery, `groups_claim_missing`."
  - "[C-03.FR-5, C-03.AC-7] With `client_secret_expires_on` 10 days ahead the settings carry the warning `secret_expiring` with that date; 30 days ahead they do not."
  - "[C-03.FR-8, C-03.FR-19, C-03.AC-8] The connection check fetches discovery from the fake IdP; with a proxy in the settings it reaches the IdP only through the fake proxy and reports `via: proxy`; a proxy at 169.254.169.254 fails the check, naming the rule."
  - "[C-03.FR-25, C-03.AC-16] `GET /api/v1/sessions/oidc/start` redirects to the IdP with PKCE `S256`, `state` and `nonce`; a person in no mapped group is redirected to `/sign-in?error=no_access`; a start with `return_to=//evil.example` or `return_to=https://evil.example` ignores it and lands on `/`."
  - "[C-03.FR-7, C-03.FR-28, C-03.AC-1] Against the fake IdP, a user with a mapped group is created on first sign-in with the mapped Role; a user without one is refused, and the Audit log entry lists the groups of the claim."
  - "[C-03.FR-28] A first OIDC sign-in whose `preferred_username` is `alice` while a local user `Alice` exists is refused to `/sign-in?error=login_taken`, creates no user, changes nothing on `Alice` and writes an Audit log entry."
  - "[C-03.FR-25] A callback without the state of its start, with a state used before, or from a browser that did not start it ends at `/sign-in?error=invalid_request`; an ID token whose signature, issuer, audience, nonce or times do not verify ends at `/sign-in?error=idp_error` or `invalid_request` and opens no session."
  - "[C-03.FR-10, C-03.AC-13] After an OIDC sign-in a user with TOTP gets a session in the state `totp_required`, unless `oidc.skip_totp_with_idp_mfa` is on and the IdP asserted multi-factor authentication in `amr`."
  - "[C-03.FR-30] Sign-in requests `offline_access` besides the configured scopes; until S-062 stores the offline token, every OIDC session ends at most `auth.oidc_fallback_session_lifetime` after sign-in."
  - "[C-03.FR-24, C-03.AC-18] `GET /api/v1/sign-in-options` without credentials returns the OIDC button with its `display_name` and nothing about the version."
  - "[C-03.FR-32, C-03.AC-26] With `oidc.sync_role` on, when the fake IdP maps the last active Admin to a lower Role at an OIDC sign-in, the Role stays Admin and the session opens, the Audit log records `user.role_sync_kept_admin` with the mapped Role, and the OIDC settings carry the warning `last_admin_kept` naming the user and that Role; once a second Admin is active, the next sign-in applies the lower Role and the warning is gone."
  - "[C-01.FR-13] `muster dev` also starts the fake IdP on `127.0.0.1:18090` and the fake HTTP and SOCKS5 proxies on `127.0.0.1:18091` and `127.0.0.1:18092`, and the demo OIDC configuration \"Dev IdP\" signs a scripted user in through the fake IdP."
  - "[C-03.FR-17] docs/sign-in/oidc-keycloak.md covers the Keycloak client, the groups mapper and claim, both redirect URIs, allowing `offline_access` and what happens without it, and local users with TOTP for an IdP Muster cannot reach."
verify: "make ci test-integration e2e"
operator_attention: false
issue: 13
---

# S-013. OIDC settings, proxy settings and OIDC sign-in (BE)

## Scope

**IN**

- OIDC settings with the write-only client secret, the expiry date, the group-to-Role mapping, the switches, the
  warnings and the connection check.
- The shared proxy settings object (C-03.FR-19) and the write-only rule for every secret field (C-03.FR-21).
- The OIDC sign-in redirect flow with PKCE, user creation on first sign-in, Role sync at sign-in with the rule for the
  last active Admin, and the refusals.
- The fake OIDC IdP, and in `muster dev` the fake IdP, the fake proxies and a demo OIDC configuration.
- The Keycloak walkthrough in the documentation.

**OUT**

- Linking an OIDC identity from the profile, conversion back to local, the OIDC parts of `reset-password`,
  `role_locked` and `local_user_only`, the offline token and the background re-checks with the shared claim helper
  (S-062).
- The effect of a refusal or of the grace period on Personal access tokens (`oidc_recheck_required`, S-016).
- The `MusterOIDCSecretExpiring` Internal alert (S-053); the OIDC settings page (S-015); the sign-in button, the error
  texts and "Link OIDC" in the SPA (S-014).

## Contracts

- **Operations implemented**: `getOidcSettings`, `updateOidcSettings`, `checkOidcSettings`, `startOidcSignIn`,
  `completeOidcSignIn`; `getSignInOptions` gains the OIDC button. Schemas: `OidcSettings`, `OidcSettingsBase`,
  `OidcSettingsInput`, `OidcGroupMapping`, `OidcUnmatchedRole`, `OidcWarning`, `OidcCheckResult`, `ProxyConfig`,
  `ProxyConfigInput`, `ProxyType`, `SecretStatus`, `ConnectionPath`.
- **Settings** (C-03.FR-5, FR-6, `oidc_settings`): `enabled`, `display_name` (default: the host of the issuer URL),
  `issuer_url`, `client_id`, `client_secret` (write-only), `client_secret_expires_on`, `scopes`, `groups_claim`,
  `group_mappings`, `unmatched_role` (`oidc.unmatched_role`), `sync_role` (`oidc.sync_role`), `skip_totp_with_idp_mfa`
  (`oidc.skip_totp_with_idp_mfa`) and `proxy`; updates need `If-Match`. A read returns `display_name` only when an
  Admin set one, so that a read sent back unchanged keeps the default. Before an Admin saves them, a read returns the
  defaults with OIDC off and the ETag `"0"`, which the first update names. `warnings`: `nobody_can_sign_in` (empty
  mapping and no Role for unmatched users), `groups_claim_missing` (the last check found discovery's `claims_supported`
  without the claim, or a sign-in token without it; a sign-in token that carries it clears the warning),
  `secret_expiring` (the expiry is less than `oidc.secret_expiry_lead` away; with `expires_on`), `last_admin_kept` (the
  latest `user.role_sync_kept_admin` entry names a user who is still the last active Admin and whose last OIDC contact
  is not newer than the entry; with that user and the mapped Role, the fields `user` and `role` of `OidcWarning`).
- **Secret fields** (C-03.FR-21, ADR-0011; `internal/keyring/field.go`): an omitted value keeps the stored one, `null`
  clears it, a string replaces it encrypted with the Keyring; reads return `SecretStatus` only; Audit log diffs show
  `secret_changed`. Every later Secret uses this helper.
- **Proxy settings** (C-03.FR-19, C-02.FR-22; `internal/proxyconf`): `{enabled, type (http, https, socks5), address
  (host:port), username, password (write-only)}`, stored as `proxy jsonb` beside the password triple, validated, and
  turned into the outbound client configuration of S-009. An omitted type, address or username keeps the stored one and
  a `null` username clears it. Connections, outgoing webhook Destinations and the outgoing heartbeat reuse it.
- **Back channel** (C-03.FR-8, ADR-0015): discovery, the key set, the token and the userinfo calls go through
  `outbound.Client.Do` with the OIDC proxy — discovery, the code exchange and userinfo with the interactive class, the
  key set with the background class. The key set is cached per issuer; a token signed with an unknown key id fetches it
  again, at most once every 30 s, and a failed fetch keeps the last good keys. ID tokens are verified with
  `github.com/go-jose/go-jose/v4` only, for the asymmetric algorithms (RS, PS, ES); `none` and HMAC are refused.
- **Connection check** (C-03.FR-8): `checkOidcSettings` fetches `<issuer>/.well-known/openid-configuration` with the
  saved settings and returns `ok`, `via` (`direct` or `proxy`), `latency_ms`, the endpoints, a masked `error` and
  warnings; it records whether discovery advertises the groups claim (`groups_claim_missing_since`).
- **Sign-in flow** (C-03.FR-25, FR-28, FR-7, FR-10; `oidc_auth_requests`): `startOidcSignIn` stores a request keyed by
  the hash of `state`, with the nonce, the PKCE verifier (a Secret), `return_to` (kept only when it is a relative path
  starting with a single `/`; the parameter carries no `pattern` in the spec, so anything else is ignored rather than
  refused) and purpose `sign_in`, valid for `oidc.auth_request_ttl`; it sets the cookie `muster_oidc_state` (HttpOnly,
  Secure, SameSite=Lax, path `/api/v1/`) to the state, which binds the callback to the browser that started it, and
  redirects with the configured scopes plus `openid` and `offline_access` (`409 oidc_not_enabled` when OIDC is off;
  `429` while 10 000 requests of the Organization are in flight, since a start needs no credentials and writes a row).
  The story adds `oidc_auth_requests` to the `short_lived_pruning` Leader task (a `PruneOIDC` field of `leader.Work`
  and the table in `metrics.ShortLivedTables`): a request is deleted once its `expires_at` is more than 1 h past; a
  link request also goes with its web session, by the cascade from `sessions`.
  `completeOidcSignIn` takes the request once (a used, expired or unknown state, or one that does not match the cookie,
  is `invalid_request`), exchanges the code (interactive class; `client_secret_basic`, or `client_secret_post` when
  discovery offers only that), verifies the ID token (signature, issuer, audience and `azp`, nonce, and `exp`, `nbf`
  and `iat` against the real clock of S-006 with 60 s of leeway; a token without the nonce of its request never
  verifies) and maps the groups claim to a Role (the highest wins,
  C-03.FR-5). The groups claim is read from the ID token, and from the userinfo endpoint when the ID token lacks it
  (D247). Outcomes, always a `302`: success to `return_to` or `/` with the session cookie; failures to
  `/sign-in?error=` `no_access` (Audit `session.oidc_refused` with the groups of the claim;
  `muster_login_failures_total{method="oidc"}`), `login_taken` (Audit entry, nothing created), `account_disabled`,
  `oidc_disabled`, `invalid_request` and `idp_error` (logged as `oidc_sign_in_failed`). Groups that map to no Role
  with no Role for unmatched users refuse the sign-in with `no_access`, for a new and for a known identity. A known
  identity signs into its account (the Role follows the mapping when `sync_role` is on — a changed Role ends the user's
  other sessions — except that it never lowers the last active Admin, C-03.FR-32); a new one creates a user with source
  `oidc` and the login from `preferred_username`, else the email, else `sub`; accounts are never merged by login or
  email. The session has the method `oidc`; it starts in the state `totp_required` when the user has TOTP, unless
  `oidc.skip_totp_with_idp_mfa` is on and the IdP asserted multi-factor authentication in `amr` (`mfa`, or a knowledge
  factor with a possession factor, RFC 8176); the policy `everyone` makes it `totp_enrolment_required` for a user
  without TOTP. The sign-in sets `oidc_last_contact_at` and clears `oidc_refused_at`.
- **Offline access** (C-03.FR-30, in part): sign-in requests `offline_access`; storing the offline token and the
  re-checks are S-062. Until then no user holds an offline token, so every OIDC session gets `expires_at` at most
  `auth.oidc_fallback_session_lifetime` after sign-in.
- **The last active Admin** (C-03.FR-32, C-03.FR-31): when Role sync at a sign-in would give the last active Admin a
  lower Role, the Role stays Admin under the lock of S-011's `last_admin` check, the session opens, and
  `user.role_sync_kept_admin` records the mapped Role in `details.mapped_role`; the next sync after another Admin
  became active applies the mapping. S-062 adds the same rule to the re-checks.
- **Audit log actions**: `oidc_settings.updated`, `session.oidc_refused` (with `details.reason`: `no_access`,
  `login_taken`, `account_disabled`), `user.role_sync_kept_admin`, and `user.role_changed` for a Role that sync
  changed; sign-ins and user creation reuse `session.signed_in` and `user.created` with the method `oidc`.
- **Log event**: `oidc_sign_in_failed` (WARN) — a callback that ended with `idp_error`, with the reason and the masked
  error.
- **Fake IdP** (`internal/fakes/fakeoidc`): discovery, key set, an authorization endpoint that approves the scripted
  next user (`sub`, `preferred_username`, `email`, `name`, `groups`, `amr`, optionally with the groups only in
  userinfo), a token endpoint that checks PKCE and rotates refresh tokens, userinfo, and control endpoints under
  `/_fake/`: the next user (`POST /_fake/next-user`), disabling and enabling a user (`POST
  /_fake/users/{sub}/disable`, `enable`; refresh then answers `invalid_grant`), `POST /_fake/config` with
  `grant_offline_access` and `omit_groups_claim` (from discovery), `POST /_fake/rotate-keys`, and the faults of the
  harness.
- **Fake proxies**: the fake HTTP and SOCKS5 proxies of S-009 also answer `GET /_fake/requests` on their own port with
  the targets they connected to, so that a live check can see what went through them.
- **Development mode**: `muster dev` also starts the fake IdP on `127.0.0.1:18090`, a fake HTTP proxy on
  `127.0.0.1:18091` and a fake SOCKS5 proxy on `127.0.0.1:18092`; at its first start on a database its demo
  configuration enables OIDC against the fake IdP (display name `Dev IdP`, client `muster-dev`, mapping
  `muster-admins` → admin, `oncall` → responder) and adds `127.0.0.0/8` to the Organization's allowed networks so that
  Muster may call the fakes.
- **Documentation** (C-03.FR-17): `docs/sign-in/oidc-keycloak.md` — the Keycloak client, the groups mapper and claim,
  the redirect URIs `MUSTER_PUBLIC_URL/api/v1/sessions/oidc/callback` and
  `MUSTER_PUBLIC_URL/api/v1/me/oidc-identity/callback`, allowing `offline_access`, what happens without it (no
  background re-checks, sessions end after `auth.oidc_fallback_session_lifetime`, Personal access tokens follow
  `auth.oidc_token_grace`), and local users with TOTP where the IdP cannot be reached.

## Steps

1. Write the secret-field helper and the proxy settings object. Check: tests cover keep, clear and replace, the masked
   reads and the `secret_changed` diff.
2. Write the back channel, the settings, the warnings and the connection check, with the fake IdP and the fake proxies.
   Check: tests cover each warning, key rotation and the check directly, through each proxy type and with a blocked
   proxy address.
3. Write the sign-in flow, user creation and Role sync at sign-in. Check: integration tests against the fake IdP cover
   each outcome, `return_to` handling and the last active Admin kept at a sign-in.
4. Extend `muster dev` and write the Keycloak page. Check: Verification below; the page lists every item of
   C-03.FR-17.

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
# {"groups":["contractors"],"reason":"no_access"}
curl -s -X POST localhost:18090/_fake/next-user -d '{"sub":"u-1","preferred_username":"olga","groups":["oncall"]}'
curl -s -c o2 -b o2 -L -o /dev/null -w '%{url_effective}\n' 'localhost:8080/api/v1/sessions/oidc/start?return_to=//evil.example'
# http://localhost:8080/

# login taken (C-03.FR-28): create the local user Alice through the API first
curl -s -X POST localhost:18090/_fake/next-user -d '{"sub":"u-3","preferred_username":"alice","groups":["oncall"]}'
curl -s -c a -b a -L -o /dev/null -w '%{url_effective}\n' 'localhost:8080/api/v1/sessions/oidc/start'
# http://localhost:8080/sign-in?error=login_taken

# settings: the secret is write-only (C-03.AC-6); expiry warning (C-03.AC-7); the check through a proxy (C-03.AC-8)
S=$(curl -s -b jar localhost:8080/api/v1/oidc-settings)
curl -s "${H[@]}" -X PUT -H "If-Match: $(jq -r .etag <<<"$S")" localhost:8080/api/v1/oidc-settings \
  -d "$(jq -c --arg d "$(date -u -v+10d +%F 2>/dev/null || date -u -d '+10 days' +%F)" \
    'del(.etag, .client_secret_status, .warnings, .updated_at) | .client_secret = "s3cr3t-new" | .client_secret_expires_on = $d
     | .proxy = {enabled: true, type: "socks5", address: "127.0.0.1:18092"}' <<<"$S")" \
  | jq -c '{client_secret_status, warnings: [.warnings[].kind]}'
# {"client_secret_status":{"set":true,"updated_at":"…"},"warnings":["secret_expiring"]}
curl -s -b jar 'localhost:8080/api/v1/audit-log?action=oidc_settings.updated' | jq -c '.items[0].diff[] | select(.pointer == "/client_secret")'
# {"pointer":"/client_secret","secret_changed":true}
curl -s "${H[@]}" -X POST localhost:8080/api/v1/oidc-settings/checks | jq -c '{ok, via}'
# {"ok":true,"via":"proxy"}
curl -s localhost:18092/_fake/requests | jq -r '.[-1].target'
# 127.0.0.1:18090
# with the proxy address changed to 169.254.169.254:1080:
# {"ok":false,"error":"blocked by the outbound address policy: proxy 169.254.169.254 is link-local (always blocked)"}

# C-03.AC-26 at a sign-in: the last active Admin keeps the Role
curl -s -X POST localhost:18090/_fake/next-user -d '{"sub":"u-9","preferred_username":"ada","groups":["muster-admins"]}'
curl -s -c ada -b ada -L -o /dev/null 'localhost:8080/api/v1/sessions/oidc/start'
# give the bootstrap Admin the Role responder (ada is the other Admin), then let the IdP map ada to responder:
curl -s -X POST localhost:18090/_fake/next-user -d '{"sub":"u-9","preferred_username":"ada","groups":["oncall"]}'
curl -s -c ada -b ada -L -o /dev/null 'localhost:8080/api/v1/sessions/oidc/start'
curl -s -b ada localhost:8080/api/v1/me | jq -r .user.role                                       # admin
curl -s -b ada localhost:8080/api/v1/oidc-settings | jq -c '.warnings[] | select(.kind == "last_admin_kept") | {u: .user.name, role}'
# {"u":"ada","role":"responder"}
curl -s -b ada 'localhost:8080/api/v1/audit-log?action=user.role_sync_kept_admin' | jq -r '.items[0].details.mapped_role'
# responder
# make another user Admin, then ada signs in again with the same groups:
curl -s -c ada -b ada -L -o /dev/null 'localhost:8080/api/v1/sessions/oidc/start'
curl -s -b ada localhost:8080/api/v1/me | jq -r .user.role                                       # responder
```

## Open questions

1. ~~Where the groups claim is read~~ — resolved (D247): from the ID token, and from the userinfo endpoint when the ID
   token lacks it; a re-check whose refresh returns no ID token leaves the Role unchanged (S-062, C-03.FR-30 syncs the
   Role only "when they carry the groups claim").

## Notes

- Suggested commit: `feat(auth): add oidc settings, proxy settings and oidc sign-in`.
- Split from the original S-013 before implementation, because it touched about 50 files: linking, conversion, the
  offline token and the background re-checks moved to S-062. The file name keeps its slug, so the branch and the issue
  stay.
- `github.com/coreos/go-oidc` is not used: it needs an `*http.Client`, which architecture lint 4 allows only inside
  `internal/outbound`.
- The development configuration allows loopback only inside `muster dev`; a real installation keeps the standard
  policy.
- Keycloak does not list custom claims such as `groups` in `claims_supported`, so its check shows
  `groups_claim_missing` until the first sign-in whose token carries the claim; the Keycloak page says so.

## Coverage

| ID | Covered | Note |
|---|---|---|
| C-03.FR-5 | partial | the API; the page is S-015, the Internal alert S-053 |
| C-03.FR-6 | partial | the warning kinds; their text on the page is S-015 |
| C-03.FR-7 | partial | refusal and Audit log; the text on the sign-in page is S-014 |
| C-03.FR-8 | partial | the check and the back channel; the page is S-015 |
| C-03.FR-10 | partial | TOTP after an OIDC sign-in |
| C-03.FR-14 | partial | the OIDC sign-in and settings entry types; linking, conversion and re-checks are S-062 |
| C-03.FR-17 | full | |
| C-03.FR-19 | partial | the object and its API; the form is S-015 |
| C-03.FR-21 | partial | the rule and its helper; the pages are S-015 |
| C-03.FR-24 | partial | the OIDC button in `sign-in-options` |
| C-03.FR-25 | partial | the flow; the error texts on the page are S-014 |
| C-03.FR-28 | partial | the API; the text on the page is S-014 |
| C-03.FR-30 | partial | `offline_access` requested and the fallback lifetime; the offline token and re-checks are S-062, the token effects S-016 |
| C-03.FR-32 | partial | Role sync at a sign-in; at a re-check S-062, the warning on the page S-015 |
| C-03.AC-1 | full | |
| C-03.AC-6 | full | |
| C-03.AC-7 | partial | the warning in the API; the page is S-015 |
| C-03.AC-8 | full | |
| C-03.AC-13 | full | together with S-012 |
| C-03.AC-16 | full | |
| C-03.AC-18 | full | |
| C-03.AC-26 | partial | at a sign-in; at a re-check S-062, the warning on the page S-015 |
| C-02.FR-22 | partial | the settings object; the mechanism is S-009 |
| C-01.FR-13 | partial | the fake IdP; in `muster dev` with the fake proxies and the demo OIDC configuration |
