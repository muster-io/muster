-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- name: InsertAuditEntry :exec
INSERT INTO audit_log (
    org_id, public_id, at, actor_kind, actor_user_id, actor_service_account_id, actor_name, api_token_id, token_name,
    transport, action, resource_type, resource_public_id, resource_name, diff, details, source_address
)
VALUES (
    @org_id, @public_id, @at, @actor_kind, sqlc.narg('actor_user_id'), sqlc.narg('actor_service_account_id'),
    sqlc.narg('actor_name'), sqlc.narg('api_token_id'), sqlc.narg('token_name'), @transport, @action,
    sqlc.narg('resource_type'), sqlc.narg('resource_public_id'), sqlc.narg('resource_name'), @diff, @details,
    sqlc.narg('source_address')
);

-- FindUserActor resolves the public_id of a user, and FindServiceAccountActor that of a Service account, to the internal id the entries store.
-- name: FindUserActor :one
SELECT id FROM users WHERE org_id = @org_id AND public_id = @public_id;

-- name: FindServiceAccountActor :one
SELECT id FROM service_accounts WHERE org_id = @org_id AND public_id = @public_id;

-- ListAuditEntries is a page of entries, newest first by time and id, after the cursor (before_at, before_id) when
-- one is given, with the filters that are set. The actor, and a user as the resource, show their current name, so
-- that a deleted user appears as deleted-user-<id> (C-03.FR-13); rows are never rewritten.
-- name: ListAuditEntries :many
SELECT a.id, a.public_id, a.at, a.actor_kind, a.actor_name, a.token_name, a.transport, a.action, a.resource_type,
       a.resource_public_id, a.resource_name, ru.name AS resource_user_name, a.diff, a.details,
       u.public_id AS actor_user_public_id, u.name AS actor_user_name,
       sa.public_id AS actor_service_account_public_id, sa.name AS actor_service_account_name,
       t.public_id AS token_public_id
FROM audit_log a
LEFT JOIN users u ON u.org_id = @org_id AND u.id = a.actor_user_id
LEFT JOIN service_accounts sa ON sa.org_id = @org_id AND sa.id = a.actor_service_account_id
LEFT JOIN api_tokens t ON t.org_id = @org_id AND t.id = a.api_token_id
LEFT JOIN users ru ON ru.org_id = @org_id AND a.resource_type = 'user' AND ru.public_id = a.resource_public_id
WHERE a.org_id = @org_id
  AND (sqlc.narg('from')::timestamptz IS NULL OR a.at >= sqlc.narg('from')::timestamptz)
  AND (sqlc.narg('to')::timestamptz IS NULL OR a.at < sqlc.narg('to')::timestamptz)
  AND (sqlc.narg('actor_user_id')::bigint IS NULL OR a.actor_user_id = sqlc.narg('actor_user_id')::bigint)
  AND (sqlc.narg('actor_service_account_id')::bigint IS NULL
       OR a.actor_service_account_id = sqlc.narg('actor_service_account_id')::bigint)
  AND (sqlc.narg('action')::text IS NULL OR a.action = sqlc.narg('action')::text)
  AND (sqlc.narg('resource_type')::text IS NULL OR a.resource_type = sqlc.narg('resource_type')::text)
  AND (sqlc.narg('resource_id')::text IS NULL OR a.resource_public_id = sqlc.narg('resource_id')::text)
  AND (sqlc.narg('before_at')::timestamptz IS NULL
       OR (a.at, a.id) < (sqlc.narg('before_at')::timestamptz, sqlc.narg('before_id')::bigint))
ORDER BY a.at DESC, a.id DESC
LIMIT @page_size;
