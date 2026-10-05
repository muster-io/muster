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
