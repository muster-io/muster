# C-03. Sign-in and access

[L1 index](../L1.md) · Stage: Foundation · UI: yes · Depends on: C-02

**Goal.** People sign in with OIDC or a local account, optionally with TOTP, receive one of three Roles, and everything
people and automation do is attributable in the Audit log. This capability also builds the web application shell that
every later UI lives in, and the shared proxy settings form.

Names in the form `area.setting` refer to rows of [defaults.md](defaults.md).

## Scenarios

1. The Admin configures OIDC against Keycloak — issuer, client, groups claim, mapping `muster-admins → Admin` and
   `oncall → Responder`. A member of `oncall` signs in for the first time and is created as a Responder.
2. A person in none of the mapped groups is refused with "You have no access to Muster. Contact your administrator.",
   and the Audit log records the refusal with the groups the token carried.
3. The Admin creates a local user and hands over a one-time password setup link; the user sets a password, enrols TOTP
   and saves recovery codes.
4. The identity provider is inside a closed network and Muster outside it. The Organization uses local users with TOTP
   required for everyone.
5. A colleague leaves. The Admin deletes the user: the name becomes `deleted-user-<id>`, sessions end, later
   capabilities revoke the user's tokens and Account links, and every Alert Group the user had acknowledged is released:
   it becomes firing without an Owner, with a Loud Thread message, so that someone else takes it.
6. The Admin filters the Audit log by user and action to see who changed the OIDC settings.
7. A broken OIDC configuration locks out every OIDC user; the bootstrap admin signs in locally, or an operator runs
   `muster admin reset-password --actor ops-alice`.
8. The OIDC client secret was issued for one year; the Admin enters its expiry date, and the OIDC settings page warns
   two weeks before it.
9. Muster reaches the identity provider only through an HTTP proxy, configured in the OIDC settings.
10. A fresh installation starts with `MUSTER_BOOTSTRAP_ADMIN_EMAIL=ops@example.org` and a password from a Secret; the
    Admin signs in with `ops@example.org` and that password, creates the other users and later removes the variables.

## Functional requirements

- **C-03.FR-1** The schema carries an Organization id everywhere; the UI and API expose exactly one Organization.
- **C-03.FR-2** There are three Roles — Admin, Responder, Viewer — each a fixed set of Permissions; every check is made
  against a Permission, not a Role. The default allocation is the matrix in
  [reference.md](reference.md#roles-and-permissions).
- **C-03.FR-3** Local users: an Admin creates them with a name, a login and an optional email; Muster returns a
  single-use password setup link valid for `auth.password_setup_link_ttl` that the Admin hands over — Muster sends no
  email. A local user signs in with the login and the password. Logins are case-insensitive: a login is stored as
  entered, and uniqueness and sign-in compare it lowercased. Passwords are hashed with argon2id and must be at least
  `auth.password_min_length` characters long. Users change their own password; Admins can disable, enable and delete
  users.
- **C-03.FR-4** Failed sign-ins slow down as `auth.signin_throttle` describes, per account and per source address —
  the client address of C-02.FR-1, which behind a reverse proxy needs `MUSTER_TRUSTED_PROXIES`; a success resets the
  count. Failures are counted in `muster_login_failures_total{method}` (`local`, `oidc`, `totp`).
- **C-03.FR-5** OIDC settings live in the database and are edited through the API: issuer URL, client id, client secret
  (write-only), an optional client secret expiry date, scopes, the provider name shown on the sign-in button
  (`display_name`; by default the host of the issuer URL), the name of the groups claim, the mapping from IdP groups to
  Roles (when a user's groups map to several Roles, the highest wins: Admin, then Responder, then Viewer), the Role for
  users without a matching group (`oidc.unmatched_role`: none — refuse sign-in — or Viewer, or Responder), "sync Role
  at sign-in" (`oidc.sync_role`; on: the IdP decides the Role and the UI locks it; off: the mapping applies only when
  the user is created), and the proxy of FR-19. Users are created on first sign-in (FR-28).
  While the expiry date is less than `oidc.secret_expiry_lead` away, the OIDC settings page shows the expiry warning;
  the Internal alert for it is C-19.FR-5.
- **C-03.FR-6** Saving OIDC settings with an empty mapping and no Role for unmatched users shows the warning
  `nobody_can_sign_in`; when the connection check (FR-8) finds that discovery does not advertise the groups claim, or a
  test sign-in shows that the token has none, the warning is `groups_claim_missing`. Both show "Nobody will be able to
  sign in through OIDC" (the settings carry the warning kinds, with `secret_expiring` of FR-5).
- **C-03.FR-7** A refused OIDC sign-in shows "You have no access to Muster. Contact your administrator." and writes an
  Audit log entry with the groups from the claim.
- **C-03.FR-8** OIDC needs the back channel: Muster fetches discovery and key sets and exchanges the code itself,
  through the outbound HTTP package (C-02.FR-20). The OIDC settings page offers a connection check that fetches
  discovery.
- **C-03.FR-9** Sessions are stored in PostgreSQL and carried in a cookie with `HttpOnly; Secure; SameSite=Lax`; every
  mutating request needs a CSRF token or the mandatory header. A session ends after `auth.session_idle_timeout` idle or
  `auth.session_lifetime` in total. Disabling a user or changing a Role ends all of that user's sessions, and so does
  converting an account to local (FR-29); changing one's own password and linking an OIDC identity end the user's other
  sessions, while the session that made the change continues.
  OIDC accounts are re-checked at the IdP in the background (FR-30). Users can list their sessions and end all of them
  from the profile (`me/sessions`).
- **C-03.FR-10** TOTP (RFC 6238) is available to every user, local or OIDC, enrolled from the profile with a QR code and
  confirmed with a first code; enrolment issues `auth.totp_recovery_codes` single-use recovery codes, shown once. A user
  with TOTP gives the code (or a recovery code) as the second step of every sign-in, local or OIDC (FR-24). The
  Organization policy "TOTP required" (`organization.totp_required`) is one of: nobody, local users, everyone; for this
  policy a local user is any account that has a password; an account that signs in through OIDC — created through OIDC
  or linked (FR-29) — is not one. A user has one TOTP enrolment, asked at every sign-in and kept when the account
  changes its sign-in method. A user under the policy without TOTP must enrol right after sign-in before doing anything
  else. On an OIDC sign-in, "skip TOTP when the IdP asserted multi-factor authentication (`amr`)" is
  `oidc.skip_totp_with_idp_mfa`.
- **C-03.FR-11** An Admin can reset another user's TOTP (audited); `muster admin reset-totp --actor <name>` does the
  same from the CLI. `muster admin reset-password --actor <name>` is the emergency access, together with the bootstrap
  Admin (FR-22): it sets a password on any account, an OIDC account included — and then removes the account's OIDC
  identity, so that an account never holds both — ends the account's sessions and records the change in the Audit log
  with the `--actor` name.
- **C-03.FR-12** The profile shows and edits the user's display name, time zone (default: the browser's) and UI language
  (English or Russian; default: the browser's, falling back to English), lists sessions and TOTP, shows the sign-in
  method and, for an account with a password, offers "Link OIDC" (FR-29); Personal access tokens (C-04) and Account
  links (C-18) join it in their capabilities.
- **C-03.FR-13** Deleting a user deactivates and pseudonymizes it: the name becomes `deleted-user-<id>`, the email is
  erased and all sessions end; Personal access tokens (C-04.FR-1) and Account links (C-18.FR-8) follow in their
  capabilities. Audit log rows are never changed; they store the user id and show the current name, and the values
  that deleting the user erased (the email, the former name and login) are shown as `[erased]` in earlier diffs when
  the Audit log is read. Earlier Timeline entries show a deleted user as "(deactivated)". A disabled user cannot sign
  in. Disabling or deleting a user
  releases their acknowledgements: each acknowledged Alert Group they own becomes firing without an Owner, with a Loud
  Thread message and a Timeline entry by the system with the reason `owner_disabled` or `owner_deleted` (C-09.FR-22);
  its Reminders end (C-17.FR-8) and its ack timeout starts over, so that it can become Unclaimed again (C-17.FR-1,
  C-17.FR-3). An Alert Group they owned that reopens later reopens into firing (C-09.FR-4). Enabling the user again
  gives no acknowledgement back.
- **C-03.FR-14** The Audit log records configuration changes with a before/after diff, security events (sign-in,
  sign-out, failed or refused sign-in, token issued or revoked, TOTP enrolled, removed or reset, OIDC identity linked or
  refused, account converted to local, user refused by the IdP at a background re-check, Account link created, removed
  or rejected) and every command of people and automation on Alert Groups, with the actor, the Personal access token or
  Service account token used, and the Transport (`ui`, `api`, `mattermost`, `telegram`, or `cli` for the `--actor` CLI
  commands). Each capability adds its own entry types. The Audit log is append-only, kept for `retention.audit_log`, and
  every entry is also written to stdout as a registered log event.
- **C-03.FR-15** The Audit log page lists entries newest first, filtered by time range, actor, action type and resource.
- **C-03.FR-16** The SPA is served with a Content Security Policy and the usual security headers.
- **C-03.FR-17** The documentation contains a complete Keycloak walkthrough (client, groups mapper, claim, and the
  redirect URIs `MUSTER_PUBLIC_URL/api/v1/sessions/oidc/callback` and
  `MUSTER_PUBLIC_URL/api/v1/me/oidc-identity/callback`, and allowing the `offline_access` scope for the Muster client),
  says what happens without `offline_access` (FR-30: no background re-checks, OIDC sessions end after
  `auth.oidc_fallback_session_lifetime`, Personal access tokens follow `auth.oidc_token_grace`), and states that
  installations whose IdP Muster cannot reach use local users with TOTP.
- **C-03.FR-18** The application shell: navigation that shows only what the user's Permissions allow, the language and
  time zone of the profile, and a banner area for Organization-wide notices (C-02.FR-24) — "recovering after downtime"
  for every signed-in user, "no replica is leading" for Admins — with the texts of
  [reference.md](reference.md#banners-warnings-and-notices). Notices update live through the live-updates stream
  (C-09.FR-25), which arrives with this capability carrying the hints for the notices and the Organization; C-09 adds
  the hints for Alert Groups.
- **C-03.FR-19** A shared proxy settings object and form — use a proxy or not, type, address, username and password
  (write-only) — with the semantics of C-02.FR-22. OIDC settings are its first user; Connections, outgoing webhook
  Destinations and the outgoing heartbeat reuse it.
- **C-03.FR-20** The `organization` resource: read for every signed-in user; update of the TOTP policy for Admins. Other
  Organization settings become editable in C-19 and C-20; until then an update that changes any other field is refused
  with `422` and the code `unsupported` for that field.
- **C-03.FR-21** Secret fields are write-only in the API for everyone, Admins included: reads return whether a value is
  set and when it last changed, and Audit log diffs show only that a secret changed. Every later capability with secret
  fields follows this rule.
- **C-03.FR-22** At startup, if the Organization has no Admin, Muster creates a local Admin from the bootstrap variables
  (C-02.FR-1): its login and its email are the value of `MUSTER_BOOTSTRAP_ADMIN_EMAIL`, its display name is the part of
  that value before `@` until the Admin changes it, and its password is `MUSTER_BOOTSTRAP_ADMIN_PASSWORD` (or the file).
  The creation is recorded in the Audit log as done by "bootstrap". If an Admin exists, the variables are ignored and a
  warning says they can be removed (ADR-0010).
- **C-03.FR-23** The app listener serves the API specification the binary was built from at `/api/v1/openapi.yaml`
  (ADR-0008); the documentation site renders its API reference from the same document (C-21).
- **C-03.FR-24** Sign-in flow. Local sign-in is always available; no setting turns it off. The sign-in page first reads
  `sign-in-options` (public; it says whether to show the OIDC button with its provider name next to the local form, and
  nothing else — no version, no detail about the installation). A local sign-in creates a session. If the user has TOTP
  and the request carried no code, the session is `totp_required` and the page asks for the code or a recovery code
  (`sessions/current/totp`); under a TOTP policy that covers a user without TOTP the session is
  `totp_enrolment_required`. A limited session can read itself, sign out, give the code or enrol; every other call is
  refused with `403` and the code `totp_required` or `totp_enrolment_required`. A wrong login, password or code is `401`
  and never says which part was wrong.
- **C-03.FR-25** OIDC sign-in flow. `sessions/oidc/start` redirects the browser to the IdP with PKCE (`S256`), `state`
  and `nonce`, all mandatory; the redirect URI to register at the IdP is
  `MUSTER_PUBLIC_URL/api/v1/sessions/oidc/callback` on the app listener. The callback always ends in a redirect to the
  SPA: on success with the session cookie set (the session is `totp_required` when the user has TOTP and the IdP did not
  assert multi-factor authentication, FR-10), on failure to the sign-in page with an error — `no_access` (FR-7),
  `login_taken` (FR-28), `account_disabled`, `oidc_disabled`, `invalid_request` or `idp_error`. The optional `return_to`
  of the start must be a relative path beginning with a single `/`; anything else is ignored, so the flow is no open
  redirect.
- **C-03.FR-26** Password setup is public: `password-setups` takes the token of a setup link and a new password. The link
  carries the token in the URL fragment, so it reaches no server log or referrer until the page posts it. A token past
  `auth.password_setup_link_ttl` is refused with `410` and the code `link_expired`, one already used or replaced by a
  newer link with `link_used`.
- **C-03.FR-27** Operations that change the caller's own account — profile, password, TOTP enrolment, confirmation,
  removal and recovery codes, linking an OIDC identity, Personal access tokens (C-04), Account links (C-18) and ending
  sessions — accept the web session with its CSRF token only; a token, a Personal access token included, gets `403`
  (`session_required`). Removing TOTP needs the current password (local users) or a current TOTP or recovery code. A
  Service account has no profile: reading `me` and its sub-resources with a Service account token gets `403`
  (`service_account_not_allowed`).
- **C-03.FR-28** The first OIDC sign-in of an identity — issuer and subject — that no account holds creates the user,
  with the login taken from `preferred_username`, else the email, else `sub`. Accounts are never merged automatically,
  by login or by email: that would let whoever controls an identity at the IdP take over a Muster account. If the login
  is taken, the sign-in is refused with the callback error `login_taken` and the text "An account with this login
  already exists. Sign in with it and link OIDC in your profile.", and the Audit log records the refusal.
- **C-03.FR-29** The IdP is the only source of truth for an account that signs in through OIDC: an account signs in
  either with a password or through OIDC, never both, and the Users list shows its sign-in method. An OIDC identity
  joins an existing account only from that account's web session: profile → "Link OIDC" → the IdP flow → the identity is
  added to the current account. Before the flow starts, the UI warns "After linking you will sign in only through OIDC;
  your password will be removed." The link callback is accepted only from the session that started it, and an identity
  that already belongs to another user is refused (`identity_linked_elsewhere`). A successful link removes the password;
  the TOTP enrolment stays (FR-10), and `oidc.skip_totp_with_idp_mfa` and "sync Role at sign-in" (`oidc.sync_role`,
  FR-5) apply as to any OIDC account. A link requests `offline_access` like a sign-in (FR-30); the session that linked
  continues as an OIDC session, and the user's other sessions end. Users cannot unlink an identity themselves. An Admin
  converts an OIDC account — created through OIDC or linked — back to local: the identity is removed, the account's
  sessions end, and the Admin receives a password setup link as at user creation (FR-3); `muster admin reset-password`
  (FR-11) does the same from the CLI with a password instead of a link. Links, refusals and conversions are recorded in
  the Audit log.
- **C-03.FR-30** OIDC sign-in and linking request the standard scope `offline_access` (OIDC Core §11) besides the
  configured scopes. When the IdP grants it — the granted scope includes `offline_access` and a refresh token is
  returned — Muster stores that offline token on the user, encrypted as a Secret with the Keyring (ADR-0011); each new
  OIDC sign-in replaces it. The token never appears in an API response, is listed for key rotation, and is wiped when
  the user is disabled, deleted or converted to local (FR-29). Every `auth.oidc_recheck_interval` a background job
  refreshes the offline token of every OIDC user who has a live session or a Personal access token that has not expired:
  rows with deadlines that any replica claims with `FOR UPDATE SKIP LOCKED` and a lease, like timers, so one check per
  user runs at a time; it calls the IdP through the back channel (the background client class and the OIDC proxy,
  C-02.FR-20) and stores a rotated refresh token back.
  - **Refusal** — a `4xx` answer about the user's grant, such as `invalid_grant`, as when the user is banned or
    disabled at the IdP (any `4xx` except `408`, `429`, the proxy's `407` and the client errors below): all of the user's sessions end (their next request gets `401` with `oidc_session_ended` and the
    SPA lands on the sign-in page), the user's Personal access tokens are refused with `401` and
    `oidc_recheck_required` until the next successful OIDC sign-in (C-04.FR-8; they are not revoked), the offline token
    is wiped, and the Audit log records the refusal.
  - **IdP unavailable** — unreachable, a timeout, `408`, `429` or `5xx`, and errors about Muster's client itself
    rather than the user (`invalid_client` or `unauthorized_client`, or a `401` from the token endpoint, which signals
    a failed client authentication), so that a broken client secret never signs everyone out: nothing changes, and
    the next attempt comes one interval later. Each check is counted in `muster_oidc_checks_total{outcome}`, and each unavailable one is logged as
    the registered event `oidc_check_failed`, so an IdP incident never signs everybody out.
  - **Success** — the user's last successful contact with the IdP is recorded; with `oidc.sync_role` on, the Role
    follows the groups in the refreshed tokens when they carry the groups claim: a changed Role ends the user's sessions
    as FR-9 says, and groups that map to no Role, with no Role for unmatched users, count as a refusal.

  Without an offline token — the IdP did not grant `offline_access` — the user cannot be re-checked in the background:
  an OIDC session ends at most `auth.oidc_fallback_session_lifetime` after sign-in, and the user's Personal access
  tokens follow the grace rule of C-04.FR-8, which applies only to such users.
- **C-03.FR-31** The Organization always keeps an active Admin: disabling, deleting or giving a lower Role to the last
  active User with the Admin Role is refused with `409` and the code `last_admin`, whoever asks, that Admin included.
- **C-03.FR-32** Role sync from the identity provider (`oidc.sync_role`, at an OIDC sign-in or a background re-check,
  FR-30) never lowers the Role of the last active Admin (FR-31): the Role stays Admin and the sessions continue, the
  Audit log records the Role the IdP mapped and that it was not applied, and the OIDC settings page shows a warning
  naming the user and that Role ([reference.md](reference.md#banners-warnings-and-notices)) until another Admin is
  active or the IdP maps the user to Admin again.

## UI

The application shell (navigation, banner area, language and time zone); sign-in page (local form and "Sign in with
{provider}"), TOTP prompt and enrolment, password setup page; profile (details, time zone, language, password, sessions,
TOTP, the sign-in method, "Link OIDC" with its warning); Users (list with Role, source, sign-in method, last sign-in,
TOTP and status; create, disable, enable, delete, reset TOTP, convert to local); OIDC settings with the group mapping
editor, the client secret expiry date and warning, the warning that the last active Admin keeps the Role, the proxy
form and the connection check; Organization → Security with the TOTP policy; Audit log.

## API surface

`sign-in-options` (read, public); `sessions` (sign in; `sessions/current`: read, sign out; `sessions/current/totp`: give
the second factor); `sessions/oidc/start` and `sessions/oidc/callback` (the OIDC redirect flow, public);
`password-setups` (create from a setup link, public); `me` (read, update, change password); `me/oidc-identity` (start a
link) and `me/oidc-identity/callback` (the link redirect); `me/sessions` (list, sign out everywhere); `me/totp` (read
status, enrol, confirm, regenerate recovery codes, remove); `users` (list, create, read, update Role, disable, enable,
delete, reset TOTP, convert to local, issue password setup link); `user-directory` (list, see C-10); `roles` (list with
Permissions); `oidc-settings` (read, update); `oidc-settings/checks` (create → result); `audit-log` (list with filters);
`organization` (read; update of the TOTP policy); `system-notices` (list the active Organization-wide notices);
`live-updates` (the stream of C-09.FR-25, with the hints for the notices and the Organization);
`/api/v1/openapi.yaml`; the CLI subcommands `muster admin reset-password` and `muster admin reset-totp`.

## Acceptance

- **C-03.AC-1** Against the fake OIDC server, a user with a mapped group is created with the mapped Role; a user without
  one is refused and the Audit log entry lists the user's groups.
- **C-03.AC-2** With "TOTP required: everyone", a new local user cannot reach any page except TOTP enrolment until
  enrolled.
- **C-03.AC-3** A mutating API call from a browser session without the CSRF token is rejected with `403`.
- **C-03.AC-4** After deleting a user, the Audit log still shows their earlier actions under `deleted-user-<id>`, and
  their sessions are ended.
- **C-03.AC-6** Saving OIDC settings writes an Audit log entry whose diff shows that the client secret changed but not
  its value; reading the settings returns only whether the secret is set and when it changed.
- **C-03.AC-7** With a client secret expiry date 10 days ahead, the OIDC settings page shows the expiry warning; with a
  date 30 days ahead it does not.
- **C-03.AC-8** With a proxy configured in the OIDC settings, discovery reaches the fake IdP only through the fake proxy;
  a proxy address of `169.254.169.254` is refused, naming the rule.
- **C-03.AC-9** While the recovery notice of C-02 is active, every page shows the recovery banner, and it disappears
  when the notice ends without a reload.
- **C-03.AC-10** Six failed sign-ins in a row for one account make the later attempts wait as `auth.signin_throttle`
  describes, and `muster_login_failures_total{method="local"}` grows by six.
- **C-03.AC-11** On a new database with the bootstrap variables set, Muster creates one local Admin who signs in with
  the value of `MUSTER_BOOTSTRAP_ADMIN_EMAIL` as the login and the given password, and the Audit log shows the creation
  by "bootstrap"; on the next start, with an Admin present, no user is created and the warning is logged.
- **C-03.AC-12** `GET /api/v1/openapi.yaml` on the app listener returns the specification checked in as
  `api/openapi.yaml` for that build.
- **C-03.AC-13** A local user with TOTP who posts the right password gets a session in the state `totp_required`; every
  call except reading the session, signing out and `sessions/current/totp` gets `403` with the code `totp_required`; the
  right code makes the session `active`. The same holds after an OIDC sign-in of a user with TOTP.
- **C-03.AC-14** A Personal access token — even an Admin's with all Permissions — gets `403` (`session_required`) on
  creating a token, changing the password, enrolling or removing TOTP and removing an Account link; a Service account
  token gets `403` (`service_account_not_allowed`) on `GET /me`.
- **C-03.AC-15** Removing TOTP with a wrong password or code gets `401` and keeps TOTP; with the right proof it is
  removed and, under a covering policy, enrolment is demanded at the next sign-in.
- **C-03.AC-16** Against the fake OIDC server, a person in no mapped group is redirected to `/sign-in?error=no_access`;
  a start with `return_to=//evil.example` or `return_to=https://evil.example` ignores it and lands on `/`.
- **C-03.AC-17** A password setup token past its TTL gets `410` with `link_expired`; a used one gets `410` with
  `link_used`; neither sets a password.
- **C-03.AC-18** `GET /api/v1/sign-in-options` without credentials returns the OIDC button, and nothing about the
  version.
- **C-03.AC-19** A user created with the login `Alice.Smith` signs in as `alice.smith`, keeps the login as entered, and
  creating another user `ALICE.SMITH` gets `409` with `name_taken`.
- **C-03.AC-20** Against the fake OIDC server, a first sign-in whose `preferred_username` is `alice` while a local user
  `Alice` exists is refused to `/sign-in?error=login_taken` with the text of FR-28, creates no user, changes nothing on
  `Alice` and writes an Audit log entry. Alice then signs in locally, sees the warning of FR-29 and links OIDC from the
  profile: her other sessions end, her password no longer signs in (`401`), she signs in through OIDC into the same
  account with her TOTP still asked, and `users` shows her sign-in method `oidc`. Linking the same identity from Bob's
  session ends with `identity_linked_elsewhere`; a link callback replayed in another session ends with
  `invalid_request`.
- **C-03.AC-21** An Admin's `convert-to-local` of Alice removes her identity, ends her sessions and returns a password
  setup link; her next OIDC sign-in is refused with `login_taken`. `muster admin reset-password --actor ops` on an
  account created through OIDC sets a password, removes the identity and writes an Audit log entry naming `ops`.
- **C-03.AC-22** With a virtual clock, after the fake OIDC server disables a signed-in user who holds an offline token
  (refresh answers `invalid_grant`), within one `auth.oidc_recheck_interval` all of the user's sessions end — the next
  request gets `401` with `oidc_session_ended` and the SPA shows the sign-in page — the user's Personal access token
  gets `401` with `oidc_recheck_required`, and the Audit log records the refusal; after the user signs in through OIDC
  again, the same token works.
- **C-03.AC-23** While the fake OIDC server answers `503` to refresh requests, nothing changes for OIDC users across
  several intervals — sessions stay active and tokens work — while `muster_oidc_checks_total{outcome="unavailable"}`
  grows and `oidc_check_failed` is logged; after the server recovers, the next check succeeds.
- **C-03.AC-24** When the fake OIDC server does not grant `offline_access`, an OIDC session ends
  `auth.oidc_fallback_session_lifetime` after sign-in although it is in use, no background check runs for the user, and
  the user's Personal access tokens follow the grace rule of C-04.FR-8.
- **C-03.AC-25** With a single active Admin, disabling or deleting that Admin or giving them a lower Role gets `409`
  with `last_admin` and changes nothing; once a second Admin is active, the same change succeeds.
- **C-03.AC-26** With `oidc.sync_role` on and a single active Admin who signs in through OIDC, the fake OIDC server
  mapping that Admin to Responder — at a sign-in or at a background re-check — leaves the Role Admin, writes an Audit
  log entry with the mapped Role, and the OIDC settings page shows the warning; once a second Admin is active, the next
  sync applies Responder and the warning is gone.
- **C-03.AC-27** Disabling a user who owns an acknowledged Alert Group makes it firing without an Owner: its Timeline
  shows `unacknowledged` by the system with the reason `owner_disabled` and the user as the previous Owner, its Thread in
  the fake Mattermost gets the Loud message "The owner was disabled — this Alert Group has no owner now.", and its ack
  timeout starts over; enabling the user again leaves it firing. Deleting a user does the same with `owner_deleted` and
  "The owner was deleted — this Alert Group has no owner now."

## Related ADRs

ADR-0008, ADR-0009, ADR-0010, ADR-0011, ADR-0015, ADR-0016.

## Depends on

C-02 — runtime, Keyring for the encrypted client secret, outbound package and proxy model, Organization defaults,
notices.

## Suggested story split

- **BE** — local users and the bootstrap Admin, sessions, TOTP, Roles and Permissions, OIDC, Audit log, the proxy
  object, `organization`, `system-notices` and the served specification; may be split into "local sign-in, TOTP and
  Audit log" and "OIDC and proxy".
- **FE** — the application shell, sign-in and enrolment pages, profile, Users, OIDC settings with the proxy form,
  Organization → Security, Audit log.
