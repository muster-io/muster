-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- name: GetUser :one
SELECT u.id, u.public_id, u.login, u.name, u.email, u.role, u.source, u.status, (u.password_hash IS NOT NULL)::boolean AS has_password,
       (u.oidc_subject IS NOT NULL)::boolean AS has_oidc_identity,
       (u.oidc_offline_token_ciphertext IS NOT NULL)::boolean AS has_offline_token,
       u.time_zone, u.language, u.last_sign_in_at, u.created_at, u.version,
       EXISTS (
           SELECT 1 FROM user_totp t
           WHERE t.org_id = u.org_id AND t.user_id = u.id AND t.enrolled_at IS NOT NULL
       ) AS totp_enabled,
       (u.oidc_subject IS NOT NULL AND EXISTS (
           SELECT 1 FROM oidc_settings o WHERE o.org_id = u.org_id AND o.enabled AND o.sync_role
       ))::boolean AS role_locked
FROM users u
WHERE u.org_id = @org_id AND u.id = @id;

-- UpdateProfile changes what the user edits in the profile: the display name, the time zone and the language.
-- name: UpdateProfile :execrows
UPDATE users
SET name = @name, time_zone = sqlc.narg('time_zone'), language = sqlc.narg('language'), updated_at = @updated_at,
    version = version + 1
WHERE org_id = @org_id AND id = @id;

-- CountAdmins counts the Admins that are not deleted; a disabled Admin still counts.
-- name: CountAdmins :one
SELECT count(*)
FROM users
WHERE org_id = @org_id AND role = 'admin' AND status <> 'deleted';

-- name: CreateUser :one
INSERT INTO users (
    org_id, public_id, login, name, email, role, source, status, password_hash, password_changed_at, created_at,
    updated_at
)
VALUES (
    @org_id, @public_id, @login, @name, sqlc.narg('email'), @role, @source, 'active', sqlc.narg('password_hash'),
    sqlc.narg('password_changed_at'), @created_at, @created_at
)
RETURNING id;

-- GetUserByPublicID reads a user by the public_id of the API, deleted users included.
-- name: GetUserByPublicID :one
SELECT u.id, u.public_id, u.login, u.name, u.email, u.role, u.source, u.status, (u.password_hash IS NOT NULL)::boolean AS has_password,
       (u.oidc_subject IS NOT NULL)::boolean AS has_oidc_identity,
       (u.oidc_offline_token_ciphertext IS NOT NULL)::boolean AS has_offline_token,
       u.time_zone, u.language, u.last_sign_in_at, u.created_at, u.version,
       EXISTS (
           SELECT 1 FROM user_totp t
           WHERE t.org_id = u.org_id AND t.user_id = u.id AND t.enrolled_at IS NOT NULL
       ) AS totp_enabled,
       (u.oidc_subject IS NOT NULL AND EXISTS (
           SELECT 1 FROM oidc_settings o WHERE o.org_id = u.org_id AND o.enabled AND o.sync_role
       ))::boolean AS role_locked
FROM users u
WHERE u.org_id = @org_id AND u.public_id = @public_id;

-- GetUserByLogin finds an account by its login, compared lowercased.
-- name: GetUserByLogin :one
SELECT id, public_id, name, status, (oidc_subject IS NOT NULL)::boolean AS has_oidc_identity
FROM users
WHERE org_id = @org_id AND lower(login) = lower(@login);

-- ListUsers is a page of users in the order of their lowercased name, then id, after the cursor (after_name,
-- after_id) when one is given; q matches the name, the login and the email case-insensitively.
-- name: ListUsers :many
SELECT u.id, u.public_id, u.login, u.name, u.email, u.role, u.source, u.status, (u.password_hash IS NOT NULL)::boolean AS has_password,
       (u.oidc_subject IS NOT NULL)::boolean AS has_oidc_identity,
       (u.oidc_offline_token_ciphertext IS NOT NULL)::boolean AS has_offline_token,
       u.time_zone, u.language, u.last_sign_in_at, u.created_at, u.version,
       EXISTS (
           SELECT 1 FROM user_totp t
           WHERE t.org_id = u.org_id AND t.user_id = u.id AND t.enrolled_at IS NOT NULL
       ) AS totp_enabled,
       (u.oidc_subject IS NOT NULL AND EXISTS (
           SELECT 1 FROM oidc_settings o WHERE o.org_id = u.org_id AND o.enabled AND o.sync_role
       ))::boolean AS role_locked,
       lower(u.name)::text AS sort_name
FROM users u
WHERE u.org_id = @org_id
  AND (sqlc.narg('q')::text IS NULL
       OR strpos(lower(u.name), lower(sqlc.narg('q')::text)) > 0
       OR strpos(lower(u.login), lower(sqlc.narg('q')::text)) > 0
       OR strpos(lower(coalesce(u.email, '')), lower(sqlc.narg('q')::text)) > 0)
  AND (sqlc.narg('role')::text IS NULL OR u.role = sqlc.narg('role')::text)
  AND (sqlc.narg('status')::text IS NULL OR u.status = sqlc.narg('status')::text)
  AND (sqlc.narg('source')::text IS NULL OR u.source = sqlc.narg('source')::text)
  AND (sqlc.narg('after_name')::text IS NULL
       OR (lower(u.name), u.id) > (sqlc.narg('after_name')::text, sqlc.narg('after_id')::bigint))
ORDER BY lower(u.name), u.id
LIMIT @page_size;

-- ListUserDirectory is a page of the user directory (C-10.FR-13): every user, deleted ones included, with only what
-- pickers and filters show, in the order of the lowercased name, then id, after the cursor (after_name, after_id) when
-- one is given; q matches the name and the login case-insensitively, never the email.
-- name: ListUserDirectory :many
SELECT u.id, u.public_id, u.login, u.name, u.status, lower(u.name)::text AS sort_name
FROM users u
WHERE u.org_id = @org_id
  AND (sqlc.narg('q')::text IS NULL
       OR strpos(lower(u.name), lower(sqlc.narg('q')::text)) > 0
       OR strpos(lower(u.login), lower(sqlc.narg('q')::text)) > 0)
  AND (sqlc.narg('after_name')::text IS NULL
       OR (lower(u.name), u.id) > (sqlc.narg('after_name')::text, sqlc.narg('after_id')::bigint))
ORDER BY lower(u.name), u.id
LIMIT @page_size;

-- LockActiveAdmins locks the rows of the active Admins, so that a change that could remove the last of them is
-- decided by one transaction at a time; a row that stopped matching while waiting is left out. Every change that locks
-- them does so before it locks its own user, so that two changes never wait for each other.
-- name: LockActiveAdmins :many
SELECT id
FROM users
WHERE org_id = @org_id AND role = 'admin' AND status = 'active'
ORDER BY id
FOR NO KEY UPDATE;

-- LockUser locks the user an administrative change is about, so that it reads the row it changes.
-- name: LockUser :one
SELECT id
FROM users
WHERE org_id = @org_id AND public_id = @public_id
FOR NO KEY UPDATE;

-- UpdateUser changes what an Admin edits: the name, the email and the Role, at the version that was read.
-- name: UpdateUser :execrows
UPDATE users
SET name = @name, email = sqlc.narg('email'), role = @role, updated_at = @now::timestamptz, version = version + 1
WHERE org_id = @org_id AND id = @id AND version = @version AND status <> 'deleted';

-- SetUserStatus disables or enables a user that is not deleted; disabling wipes the offline token of an OIDC account.
-- name: SetUserStatus :execrows
UPDATE users
SET status = @status,
    oidc_offline_token_ciphertext = CASE WHEN @status = 'active' THEN oidc_offline_token_ciphertext END,
    oidc_offline_token_key_id = CASE WHEN @status = 'active' THEN oidc_offline_token_key_id END,
    oidc_offline_token_updated_at = CASE WHEN @status = 'active' THEN oidc_offline_token_updated_at END,
    updated_at = @now::timestamptz, version = version + 1
WHERE org_id = @org_id AND id = @id AND status <> 'deleted';

-- PseudonymizeUser deletes a user: the row stays with the status deleted, name and login become the pseudonym, and
-- the email, the password and the offline token are erased.
-- name: PseudonymizeUser :execrows
UPDATE users
SET status = 'deleted', name = @pseudonym, login = @pseudonym, email = NULL, password_hash = NULL,
    oidc_offline_token_ciphertext = NULL, oidc_offline_token_key_id = NULL, oidc_offline_token_updated_at = NULL,
    deleted_at = @now::timestamptz, updated_at = @now::timestamptz, version = version + 1
WHERE org_id = @org_id AND id = @id AND status <> 'deleted';

-- SetUserPassword sets the password of a local account that is not deleted.
-- name: SetUserPassword :execrows
UPDATE users
SET password_hash = @password_hash, password_changed_at = @now::timestamptz, updated_at = @now::timestamptz,
    version = version + 1
WHERE org_id = @org_id AND id = @id AND status <> 'deleted' AND oidc_subject IS NULL;

-- EndSessionsOfUser ends every session of a user that has not ended, with the reason of the administrative action.
-- name: EndSessionsOfUser :execrows
UPDATE sessions
SET ended_at = @now::timestamptz, end_reason = @end_reason
WHERE org_id = @org_id AND user_id = @user_id AND ended_at IS NULL;

-- RevokeTokensOfUser revokes the Personal access tokens of a deleted user with the reason owner_deleted (C-04.FR-1).
-- name: RevokeTokensOfUser :execrows
UPDATE api_tokens
SET revoked_at = @now::timestamptz, revoked_reason = 'owner_deleted'
WHERE org_id = @org_id AND kind = 'personal' AND user_id = @user_id AND revoked_at IS NULL;

-- name: InsertPasswordSetup :exec
INSERT INTO password_setups (org_id, user_id, token_hash, created_by_user_id, created_at, expires_at)
VALUES (@org_id, @user_id, @token_hash, sqlc.narg('created_by_user_id'), @created_at, @expires_at);

-- SupersedePasswordSetups marks the open links of a user as replaced, before a newer one is issued or the user is
-- deleted.
-- name: SupersedePasswordSetups :execrows
UPDATE password_setups
SET superseded_at = @now::timestamptz
WHERE org_id = @org_id AND user_id = @user_id AND used_at IS NULL AND superseded_at IS NULL;

-- GetPasswordSetup finds a link by the hash of its token and, inside a transaction, locks it, so that it is used
-- once. The transaction locks the user first (LockUser), as every change of a user and its links does.
-- name: GetPasswordSetup :one
SELECT p.id, p.user_id, p.expires_at, p.used_at, p.superseded_at, u.public_id AS user_public_id, u.name AS user_name
FROM password_setups p
JOIN users u ON u.org_id = @org_id AND u.id = p.user_id
WHERE p.org_id = @org_id AND p.token_hash = @token_hash
FOR UPDATE OF p;

-- name: MarkPasswordSetupUsed :execrows
UPDATE password_setups
SET used_at = @now::timestamptz
WHERE org_id = @org_id AND id = @id AND used_at IS NULL AND superseded_at IS NULL;

-- PrunePasswordSetups deletes up to batch_size links that expired before @before, used and superseded ones included.
-- A link being used is skipped and taken at the next run.
-- name: PrunePasswordSetups :execrows
DELETE FROM password_setups p
WHERE p.org_id = @org_id AND p.id IN (
    SELECT o.id FROM password_setups o
    WHERE o.org_id = @org_id AND o.expires_at < @before::timestamptz
    ORDER BY o.id
    LIMIT @batch_size
    FOR UPDATE SKIP LOCKED
);

-- RoleSyncOn reports whether the identity provider decides the Role of OIDC accounts: OIDC and oidc.sync_role are both
-- on.
-- name: RoleSyncOn :one
SELECT EXISTS (
    SELECT 1 FROM oidc_settings WHERE org_id = @org_id AND enabled AND sync_role
)::boolean AS role_sync_on;

-- ConvertToLocal removes the OIDC identity of an account that is not deleted, with its offline token, so that the
-- account signs in with a password once one is set.
-- name: ConvertToLocal :execrows
UPDATE users
SET oidc_issuer = NULL, oidc_subject = NULL, oidc_offline_token_ciphertext = NULL, oidc_offline_token_key_id = NULL,
    oidc_offline_token_updated_at = NULL, updated_at = @now::timestamptz, version = version + 1
WHERE org_id = @org_id AND id = @id AND status <> 'deleted' AND oidc_subject IS NOT NULL;

-- ResetUserPassword is the emergency reset of the CLI: it sets the password of any account that is not deleted and
-- removes an OIDC identity and its offline token in the same update, so that the account never holds both.
-- name: ResetUserPassword :execrows
UPDATE users
SET password_hash = @password_hash, password_changed_at = @now::timestamptz, oidc_issuer = NULL, oidc_subject = NULL,
    oidc_offline_token_ciphertext = NULL, oidc_offline_token_key_id = NULL, oidc_offline_token_updated_at = NULL,
    updated_at = @now::timestamptz, version = version + 1
WHERE org_id = @org_id AND id = @id AND status <> 'deleted';

-- DeleteOIDCCheck removes the background re-check of a user whose offline token was wiped: disabled, deleted or
-- converted to local.
-- name: DeleteOIDCCheck :exec
DELETE FROM oidc_checks
WHERE org_id = @org_id AND user_id = @user_id;
