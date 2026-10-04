# C-04. API tokens

[L1 index](../L1.md) · Stage: Foundation · UI: yes · Depends on: C-03

**Goal.** Automation acts through Personal access tokens, as a person, or through Service accounts, as a non-person
identity, with tokens Muster can verify but never show again.

Names in the form `area.setting` refer to rows of [defaults.md](defaults.md).

## Scenarios

1. Alex creates a Personal access token "laptop-scripts" that expires in 90 days and allows only reading and
   acknowledging. A script acknowledges `#412` (once C-10 is merged); the Audit log says "alex via token
   laptop-scripts".
2. The Admin creates a Service account "terraform" with the Admin Role and two tokens, switches the pipeline to the
   second token and revokes the first.
3. A token leaks into a public repository; its `mstr_` prefix lets secret scanners find it, and the owner revokes it.
4. A runaway script receives `429` with `Retry-After`.

## Functional requirements

- **C-04.FR-1** A Personal access token belongs to a User and acts with that User's Permissions narrowed to the
  Permissions chosen for the token; it never has more than its owner. If the owner's Role changes, the token's
  effective Permissions shrink with it. Tokens of a disabled user stop working until the user is enabled again; tokens
  of a deleted user are revoked.
- **C-04.FR-2** A Service account has a name, a Role and any number of tokens; it cannot sign in to the UI and has no
  Account links. Admins create, disable, enable and delete Service accounts; a disabled account's tokens stop working
  until it is enabled again. The Owner of an Alert Group is always a User, so a Service account cannot become an
  Owner: Acknowledge and "Still on it" from it are refused with `409`, `command-refused` and the code
  `owner_must_be_user`, and `allowed_commands` (C-10.FR-16) does not offer them to it. It may Resolve, Snooze and add Notes,
  shown as the Service account. A Personal access token acts as its User and may do all of this; the Owner is that
  User.
- **C-04.FR-3** Tokens start with `mstr_` and the kind of the token — `mstr_pat_` for a Personal access token,
  `mstr_sat_` for a Service account token and `mstr_int_` for an Integration token (C-05) — so that scanners and logs
  tell them apart. They are stored only as hashes and are shown once, at creation. An expiry date is
  optional (`token.expiry`); the UI warns on tokens without one. Each token shows when and from which address it was
  last used — the client address of C-02.FR-1 (`MUSTER_TRUSTED_PROXIES`) — and can be revoked.
- **C-04.FR-4** API requests authenticate with `Authorization: Bearer <token>`. Integration tokens are not accepted by
  the API, and API tokens are not accepted by ingestion.
- **C-04.FR-5** The API is rate-limited per token by `api.rate_limit`; excess requests get `429` with `Retry-After`.
  Ingestion is not rate-limited.
- **C-04.FR-6** Issuing and revoking tokens is recorded in the Audit log; every action made with a token records its
  name, shown as "{user} via token {name}" or as the Service account and token.
- **C-04.FR-7** A token cannot mint or revoke tokens: Personal access tokens are issued and revoked from the web session
  only (C-03.FR-27). The Permissions chosen for a new token must be a subset of what its creator holds at that moment.
- **C-04.FR-8** A Personal access token of an account that signs in through OIDC follows the IdP (C-03.FR-30). After the
  IdP refuses the owner at a background re-check, the token gets `401` with the code `oidc_recheck_required` and the
  text "Sign in through OIDC to make your tokens work again." until the owner's next successful OIDC sign-in. While
  Muster holds an offline token for the owner, there is no other limit: the background re-checks stand in for the
  owner's activity. Only for an owner without an offline token — the IdP did not grant `offline_access` — does the grace
  rule apply: the token is refused the same way once the owner's last OIDC sign-in is older than
  `auth.oidc_token_grace`. In both cases the tokens are not revoked and work again after the owner's next OIDC sign-in.
  Service account tokens are not affected. The grace period is an Organization setting, edited on the Security page from
  C-20 on.
## UI

Profile → Personal access tokens (create with name, expiry and Permissions; list; revoke; the warning for tokens without
expiry; for an OIDC account without an offline token, the date until which its tokens work without a new OIDC sign-in).
Admin → Service accounts (create with Role; tokens; disable; delete).

## API surface

`me/personal-access-tokens` (list, create, revoke); `service-accounts` (list, create, read, update, disable, enable,
delete); `service-accounts/{id}/tokens` (list, create, revoke).

## Acceptance

- **C-04.AC-1** A Personal access token of an Admin, created with read-only Permissions, gets `403` when creating a
  User; the same Admin's session succeeds.
- **C-04.AC-2** A token is displayed once; listing tokens never returns its value.
- **C-04.AC-3** Exceeding the per-token limit returns `429` with `Retry-After`; another token is unaffected.
- **C-04.AC-4** Tokens of a deleted user are rejected with `401`; tokens of a disabled user are rejected until the user
  is enabled again.
- **C-04.AC-5** Creating a User with a Personal access token writes an Audit log entry showing "{user} via token
  {name}".
- **C-04.AC-6** Acknowledge from a Service account token returns `409` with the code `owner_must_be_user` and changes
  nothing; Resolve, Snooze and adding a Note from it succeed and are shown as the Service account; Acknowledge with a
  Personal access token makes its User the Owner.
- **C-04.AC-7** A Personal access token cannot create or revoke tokens (`403`); a session can, and cannot grant a
  Permission its User lacks (`422`).
- **C-04.AC-8** A disabled Service account's tokens get `401` until `enable` is called.
- **C-04.AC-9** With a virtual clock and a fake OIDC server that grants no offline token, a Personal access token of an
  OIDC user gets `401` with `oidc_recheck_required` and the text of FR-8 once `auth.oidc_token_grace` has passed since
  the user's last OIDC sign-in, and works again after the user signs in through OIDC, without being reissued. With an
  offline token granted, the same token keeps working past the grace period while background re-checks succeed, and is
  refused within one `auth.oidc_recheck_interval` after the IdP refuses the user. A Service account token of the same
  age is not affected.

## Related ADRs

ADR-0008, ADR-0011.

## Depends on

C-03 — Users, Roles, Permissions and the Audit log.

## Suggested story split

- **BE** — token model, hashing, authentication, rate limit, Service accounts, Audit log entries.
- **FE** — Personal access tokens in the profile, Service accounts pages.
