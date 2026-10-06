-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- InsertToken stores a Personal access token or a Service account token by the SHA-256 of its value.
-- name: InsertToken :one
INSERT INTO api_tokens (org_id, public_id, kind, user_id, service_account_id, name, token_hash, expires_at, created_at)
VALUES (
    @org_id, @public_id, @kind, sqlc.narg('user_id'), sqlc.narg('service_account_id'), @name, @token_hash,
    sqlc.narg('expires_at')::timestamptz, @created_at
)
RETURNING id;

-- InsertTokenPermissions narrows a Personal access token to the given Permissions.
-- name: InsertTokenPermissions :exec
INSERT INTO api_token_permissions (api_token_id, org_id, permission)
SELECT @api_token_id, @org_id, p
FROM unnest(@permissions::text[]) AS p;

-- ListUserTokens lists the Personal access tokens of a user that are not revoked, the newest first, with their
-- Permissions.
-- name: ListUserTokens :many
SELECT t.id, t.public_id, t.name, t.expires_at, t.created_at, t.last_used_at, t.last_used_address,
       COALESCE(
           (SELECT array_agg(p.permission ORDER BY p.permission)
            FROM api_token_permissions p
            WHERE p.org_id = @org_id AND p.api_token_id = t.id),
           '{}'
       )::text[] AS permissions
FROM api_tokens t
WHERE t.org_id = @org_id AND t.kind = 'personal' AND t.user_id = @user_id AND t.revoked_at IS NULL
ORDER BY t.created_at DESC, t.id DESC;

-- ListServiceAccountTokens lists the tokens of a Service account that are not revoked, the newest first.
-- name: ListServiceAccountTokens :many
SELECT id, public_id, name, expires_at, created_at, last_used_at, last_used_address
FROM api_tokens
WHERE org_id = @org_id AND kind = 'service_account' AND service_account_id = @service_account_id
  AND revoked_at IS NULL
ORDER BY created_at DESC, id DESC;

-- RevokeUserToken revokes a Personal access token of a user that is not revoked yet and returns its id and name.
-- name: RevokeUserToken :one
UPDATE api_tokens
SET revoked_at = @now::timestamptz, revoked_reason = 'revoked'
WHERE org_id = @org_id AND kind = 'personal' AND user_id = @user_id AND public_id = @public_id
  AND revoked_at IS NULL
RETURNING id, name;

-- RevokeServiceAccountToken revokes a token of a Service account that is not revoked yet and returns its id and name.
-- name: RevokeServiceAccountToken :one
UPDATE api_tokens
SET revoked_at = @now::timestamptz, revoked_reason = 'revoked'
WHERE org_id = @org_id AND kind = 'service_account' AND service_account_id = @service_account_id
  AND public_id = @public_id AND revoked_at IS NULL
RETURNING id, name;

-- RevokeServiceAccountTokens revokes every token of a deleted Service account with the reason owner_deleted.
-- name: RevokeServiceAccountTokens :execrows
UPDATE api_tokens
SET revoked_at = @now::timestamptz, revoked_reason = 'owner_deleted'
WHERE org_id = @org_id AND kind = 'service_account' AND service_account_id = @service_account_id
  AND revoked_at IS NULL;

-- GetTokenByHash finds a token by the SHA-256 of its value, with what authentication decides on: the owner of a
-- Personal access token with its OIDC state, or the Service account, the Organization's token grace and the
-- Permissions the token is narrowed to.
-- name: GetTokenByHash :one
SELECT t.id, t.public_id, t.kind, t.name, t.token_hash, t.expires_at, t.revoked_at, t.last_used_at,
       t.last_used_address,
       u.id AS user_id, u.public_id AS user_public_id, u.name AS user_name, u.role AS user_role,
       u.status AS user_status, (u.oidc_subject IS NOT NULL)::boolean AS user_oidc,
       (u.oidc_offline_token_ciphertext IS NOT NULL)::boolean AS user_offline_token,
       u.oidc_last_contact_at AS user_oidc_last_contact_at, u.oidc_refused_at AS user_oidc_refused_at,
       sa.id AS service_account_id, sa.public_id AS service_account_public_id, sa.name AS service_account_name,
       sa.role AS service_account_role, sa.status AS service_account_status,
       o.oidc_token_grace_seconds,
       COALESCE(
           (SELECT array_agg(p.permission ORDER BY p.permission)
            FROM api_token_permissions p
            WHERE p.org_id = @org_id AND p.api_token_id = t.id),
           '{}'
       )::text[] AS permissions
FROM api_tokens t
JOIN organizations o ON o.id = t.org_id
LEFT JOIN users u ON u.org_id = @org_id AND u.id = t.user_id
LEFT JOIN service_accounts sa ON sa.org_id = @org_id AND sa.id = t.service_account_id
WHERE t.org_id = @org_id AND o.id = @org_id AND t.kind = @kind AND t.token_hash = @token_hash;

-- TouchToken records the use of a token and the client address, at most once a minute: a row used since
-- @stale_before is left alone.
-- name: TouchToken :exec
UPDATE api_tokens
SET last_used_at = @now::timestamptz, last_used_address = sqlc.narg('address')
WHERE org_id = @org_id AND id = @id AND (last_used_at IS NULL OR last_used_at <= @stale_before::timestamptz);

-- ListServiceAccounts lists the Service accounts that are not deleted, in the order they were created, after the
-- cursor when one is given, with how many tokens each has that are not revoked.
-- name: ListServiceAccounts :many
SELECT sa.id, sa.public_id, sa.name, sa.role, sa.status, sa.created_at, sa.version,
       (SELECT count(*)
        FROM api_tokens t
        WHERE t.org_id = @org_id AND t.service_account_id = sa.id AND t.revoked_at IS NULL) AS token_count
FROM service_accounts sa
WHERE sa.org_id = @org_id AND sa.status <> 'deleted'
  AND (sqlc.narg('after_id')::bigint IS NULL OR sa.id > sqlc.narg('after_id')::bigint)
ORDER BY sa.id
LIMIT @page_size;

-- GetServiceAccount reads a Service account that is not deleted, with how many tokens it has that are not revoked.
-- name: GetServiceAccount :one
SELECT sa.id, sa.public_id, sa.name, sa.role, sa.status, sa.created_at, sa.version,
       (SELECT count(*)
        FROM api_tokens t
        WHERE t.org_id = @org_id AND t.service_account_id = sa.id AND t.revoked_at IS NULL) AS token_count
FROM service_accounts sa
WHERE sa.org_id = @org_id AND sa.public_id = @public_id AND sa.status <> 'deleted';

-- LockServiceAccount locks a Service account that is not deleted for a change and returns its id.
-- name: LockServiceAccount :one
SELECT id
FROM service_accounts
WHERE org_id = @org_id AND public_id = @public_id AND status <> 'deleted'
FOR UPDATE;

-- name: InsertServiceAccount :one
INSERT INTO service_accounts (org_id, public_id, name, role, status, created_at, updated_at)
VALUES (@org_id, @public_id, @name, @role, 'active', @now::timestamptz, @now::timestamptz)
RETURNING id;

-- name: UpdateServiceAccount :exec
UPDATE service_accounts
SET name = @name, role = @role, updated_at = @now::timestamptz, version = version + 1
WHERE org_id = @org_id AND id = @id;

-- name: SetServiceAccountStatus :exec
UPDATE service_accounts
SET status = @status, updated_at = @now::timestamptz, version = version + 1
WHERE org_id = @org_id AND id = @id;

-- DeleteServiceAccount marks a Service account deleted; the row stays so that the Audit log shows it.
-- name: DeleteServiceAccount :exec
UPDATE service_accounts
SET status = 'deleted', deleted_at = @now::timestamptz, updated_at = @now::timestamptz, version = version + 1
WHERE org_id = @org_id AND id = @id;
