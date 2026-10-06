-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- GetSettings reads the OIDC settings of the Organization, with the state of their Secrets.
-- name: GetSettings :one
SELECT org_id, enabled, display_name, issuer_url, client_id, client_secret_ciphertext, client_secret_key_id,
       client_secret_updated_at, client_secret_expires_on, scopes, groups_claim, group_mappings, unmatched_role,
       sync_role, skip_totp_with_idp_mfa, proxy, proxy_password_ciphertext, proxy_password_key_id,
       proxy_password_updated_at, groups_claim_missing_since, version, updated_at
FROM oidc_settings
WHERE org_id = @org_id;

-- LockSettings locks the settings row until the transaction ends and returns its version.
-- name: LockSettings :one
SELECT version
FROM oidc_settings
WHERE org_id = @org_id
FOR UPDATE;

-- InsertSettings stores the first OIDC settings of the Organization; it changes nothing when they exist, so that two
-- first saves at once leave one row and the second answers a version mismatch.
-- name: InsertSettings :execrows
INSERT INTO oidc_settings (
    org_id, enabled, display_name, issuer_url, client_id, client_secret_ciphertext, client_secret_key_id,
    client_secret_updated_at, client_secret_expires_on, scopes, groups_claim, group_mappings, unmatched_role, sync_role,
    skip_totp_with_idp_mfa, proxy, proxy_password_ciphertext, proxy_password_key_id, proxy_password_updated_at,
    version, updated_at
)
VALUES (
    @org_id, @enabled, sqlc.narg('display_name'), @issuer_url, @client_id, sqlc.narg('client_secret_ciphertext'),
    sqlc.narg('client_secret_key_id'), sqlc.narg('client_secret_updated_at'), sqlc.narg('client_secret_expires_on'),
    @scopes, @groups_claim, @group_mappings, @unmatched_role, @sync_role, @skip_totp_with_idp_mfa, @proxy,
    sqlc.narg('proxy_password_ciphertext'), sqlc.narg('proxy_password_key_id'), sqlc.narg('proxy_password_updated_at'),
    1, @updated_at
)
ON CONFLICT (org_id) DO NOTHING;

-- UpdateSettings replaces the settings at the version that was read.
-- name: UpdateSettings :execrows
UPDATE oidc_settings
SET enabled = @enabled, display_name = sqlc.narg('display_name'), issuer_url = @issuer_url, client_id = @client_id,
    client_secret_ciphertext = sqlc.narg('client_secret_ciphertext'),
    client_secret_key_id = sqlc.narg('client_secret_key_id'),
    client_secret_updated_at = sqlc.narg('client_secret_updated_at'),
    client_secret_expires_on = sqlc.narg('client_secret_expires_on'), scopes = @scopes, groups_claim = @groups_claim,
    group_mappings = @group_mappings, unmatched_role = @unmatched_role, sync_role = @sync_role,
    skip_totp_with_idp_mfa = @skip_totp_with_idp_mfa, proxy = @proxy,
    proxy_password_ciphertext = sqlc.narg('proxy_password_ciphertext'),
    proxy_password_key_id = sqlc.narg('proxy_password_key_id'),
    proxy_password_updated_at = sqlc.narg('proxy_password_updated_at'),
    groups_claim_missing_since = CASE
        WHEN groups_claim = @groups_claim AND issuer_url = @issuer_url THEN groups_claim_missing_since
    END,
    version = version + 1, updated_at = @updated_at
WHERE org_id = @org_id AND version = @version;

-- SetGroupsClaimMissing records since when the groups claim was found missing, or clears it with null; the warning is
-- not a configured value, so the version stays.
-- name: SetGroupsClaimMissing :exec
UPDATE oidc_settings
SET groups_claim_missing_since = CASE
        WHEN sqlc.narg('since')::timestamptz IS NULL THEN NULL
        ELSE coalesce(groups_claim_missing_since, sqlc.narg('since')::timestamptz)
    END
WHERE org_id = @org_id;

-- LatestKeptAdmin reads the latest entry in which Role sync kept the last active Admin, with the user it names, that
-- user's last OIDC contact and how many active Admins there are now.
-- name: LatestKeptAdmin :one
SELECT a.at, (a.details ->> 'mapped_role')::text AS mapped_role, u.public_id, u.name, u.login, u.role, u.status,
       u.oidc_last_contact_at,
       (SELECT count(*) FROM users c WHERE c.org_id = @org_id AND c.role = 'admin' AND c.status = 'active')::bigint
           AS active_admins
FROM audit_log a
JOIN users u ON u.org_id = @org_id AND u.public_id = a.resource_public_id
WHERE a.org_id = @org_id AND a.action = 'user.role_sync_kept_admin'
ORDER BY a.at DESC, a.id DESC
LIMIT 1;

-- InsertAuthRequest stores an OIDC redirect in flight, keyed by the hash of its state, unless the Organization already
-- has max_pending requests that have not expired: starts need no credentials, so their rows are bounded.
-- name: InsertAuthRequest :execrows
INSERT INTO oidc_auth_requests (
    state_hash, org_id, purpose, link_user_id, link_session_id, nonce, code_verifier_ciphertext, code_verifier_key_id,
    return_to, created_at, expires_at
)
SELECT @state_hash, @org_id, @purpose, sqlc.narg('link_user_id'), sqlc.narg('link_session_id'), @nonce,
       @code_verifier_ciphertext, @code_verifier_key_id, sqlc.narg('return_to'), @created_at, @expires_at
WHERE (SELECT count(*) FROM oidc_auth_requests p WHERE p.org_id = @org_id AND p.expires_at > @created_at)
      < @max_pending::bigint;

-- TakeAuthRequest removes and returns the request of a state, so that a callback uses it once.
-- name: TakeAuthRequest :one
DELETE FROM oidc_auth_requests
WHERE org_id = @org_id AND state_hash = @state_hash
RETURNING purpose, link_user_id, link_session_id, nonce, code_verifier_ciphertext, code_verifier_key_id, return_to,
    expires_at;

-- PruneAuthRequests deletes up to batch_size requests that expired before @before. A request being taken is skipped
-- and goes at the next run.
-- name: PruneAuthRequests :execrows
DELETE FROM oidc_auth_requests r
WHERE r.org_id = @org_id AND r.state_hash IN (
    SELECT o.state_hash FROM oidc_auth_requests o
    WHERE o.org_id = @org_id AND o.expires_at < @before::timestamptz
    ORDER BY o.state_hash
    LIMIT @batch_size
    FOR UPDATE SKIP LOCKED
);

-- GetIdentityUser finds the account that holds an OIDC identity, with whether it has TOTP and the Organization's TOTP
-- policy.
-- name: GetIdentityUser :one
SELECT u.id, u.public_id, u.login, u.name, u.role, u.status,
       EXISTS (
           SELECT 1 FROM user_totp t
           WHERE t.org_id = u.org_id AND t.user_id = u.id AND t.enrolled_at IS NOT NULL
       ) AS totp_enrolled,
       o.totp_required
FROM users u
JOIN organizations o ON o.id = u.org_id
WHERE u.org_id = @org_id AND u.oidc_issuer = @issuer AND u.oidc_subject = @subject;

-- LoginTaken reports whether an account has the login, compared lowercased.
-- name: LoginTaken :one
SELECT EXISTS (SELECT 1 FROM users WHERE org_id = @org_id AND lower(login) = lower(@login))::boolean AS taken;

-- CreateIdentityUser creates the account of an OIDC identity at its first sign-in.
-- name: CreateIdentityUser :one
INSERT INTO users (
    org_id, public_id, login, name, email, role, source, status, oidc_issuer, oidc_subject, created_at, updated_at
)
VALUES (
    @org_id, @public_id, @login, @name, sqlc.narg('email'), @role, 'oidc', 'active', @issuer, @subject, @created_at,
    @created_at
)
RETURNING id;

-- LockActiveAdmins locks the rows of the active Admins, as the last_admin check of user administration does, before
-- the row of the user whose Role sync may lower an Admin.
-- name: LockActiveAdmins :many
SELECT id
FROM users
WHERE org_id = @org_id AND role = 'admin' AND status = 'active'
ORDER BY id
FOR NO KEY UPDATE;

-- LockUser locks the user whose Role sync runs and reads the Role and status it has now.
-- name: LockUser :one
SELECT role, status
FROM users
WHERE org_id = @org_id AND id = @id
FOR NO KEY UPDATE;

-- SetRole gives a user the Role the identity provider maps them to.
-- name: SetRole :execrows
UPDATE users
SET role = @role, updated_at = @now::timestamptz, version = version + 1
WHERE org_id = @org_id AND id = @id AND status = 'active';

-- EndUserSessions ends every session of a user that has not ended, with the reason.
-- name: EndUserSessions :execrows
UPDATE sessions
SET ended_at = @now::timestamptz, end_reason = @end_reason
WHERE org_id = @org_id AND user_id = @user_id AND ended_at IS NULL;

-- RecordContact records a successful contact with the identity provider and lifts an earlier refusal.
-- name: RecordContact :exec
UPDATE users
SET oidc_last_contact_at = @now::timestamptz, oidc_refused_at = NULL
WHERE org_id = @org_id AND id = @id;

-- InsertDemoSettings stores the demo OIDC configuration of `muster dev` when the Organization has none.
-- name: InsertDemoSettings :execrows
INSERT INTO oidc_settings (
    org_id, enabled, display_name, issuer_url, client_id, client_secret_ciphertext, client_secret_key_id,
    client_secret_updated_at, scopes, groups_claim, group_mappings, unmatched_role, sync_role, skip_totp_with_idp_mfa,
    version, updated_at
)
VALUES (
    @org_id, true, @display_name, @issuer_url, @client_id, @client_secret_ciphertext, @client_secret_key_id, @updated_at,
    @scopes, @groups_claim, @group_mappings, 'none', true, false, 1, @updated_at
)
ON CONFLICT (org_id) DO NOTHING;

-- AllowNetwork adds a network to the Organization's allowed outbound networks when it is not listed; `muster dev`
-- allows loopback this way so that Muster may call the fake servers.
-- name: AllowNetwork :execrows
UPDATE outbound_policies
SET allowed = array_append(allowed, @network::text), version = version + 1, updated_at = @updated_at
WHERE org_id = @org_id AND NOT (@network::text = ANY (allowed));
