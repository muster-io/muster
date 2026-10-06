-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- ListIntegrations lists the Integrations that are not deleted, in the order they were created, after the cursor when
-- one is given.
-- name: ListIntegrations :many
SELECT i.id, i.public_id, i.name, i.description, i.builtin, i.connection_mode, i.static_labels,
       i.duplicate_window_seconds, i.heartbeat_enabled, i.heartbeat_timeout_seconds, i.heartbeat_state,
       i.heartbeat_last_signal_at, i.heartbeat_lost_since, i.snapshot_count, i.created_at, i.version
FROM integrations i
WHERE i.org_id = @org_id AND i.deleted_at IS NULL
  AND (sqlc.narg('after_id')::bigint IS NULL OR i.id > sqlc.narg('after_id')::bigint)
ORDER BY i.id
LIMIT @page_size;

-- GetIntegration reads an Integration that is not deleted.
-- name: GetIntegration :one
SELECT i.id, i.public_id, i.name, i.description, i.builtin, i.connection_mode, i.static_labels,
       i.duplicate_window_seconds, i.heartbeat_enabled, i.heartbeat_timeout_seconds, i.heartbeat_state,
       i.heartbeat_last_signal_at, i.heartbeat_lost_since, i.snapshot_count, i.created_at, i.version
FROM integrations i
WHERE i.org_id = @org_id AND i.public_id = @public_id AND i.deleted_at IS NULL;

-- LastSnapshotTimes returns the time of the newest Stored Snapshot of each of the Integrations that has one, each
-- read through the list index of its Integration.
-- name: LastSnapshotTimes :many
SELECT ids.id::bigint AS integration_id, newest.received_at
FROM unnest(@integration_ids::bigint[]) AS ids (id)
JOIN LATERAL (
    SELECT s.received_at
    FROM stored_snapshots s
    WHERE s.org_id = @org_id AND s.integration_id = ids.id
    ORDER BY s.received_at DESC
    LIMIT 1
) newest ON true;

-- LockIntegration locks an Integration that is not deleted for a change and returns its id.
-- name: LockIntegration :one
SELECT id
FROM integrations
WHERE org_id = @org_id AND public_id = @public_id AND deleted_at IS NULL
FOR UPDATE;

-- FindIntegrationByName returns the public_id of the Integration that is not deleted and has the name.
-- name: FindIntegrationByName :one
SELECT public_id
FROM integrations
WHERE org_id = @org_id AND name = @name AND deleted_at IS NULL;

-- name: InsertIntegration :one
INSERT INTO integrations (
    org_id, public_id, name, description, connection_mode, static_labels, duplicate_window_seconds,
    heartbeat_enabled, heartbeat_timeout_seconds, heartbeat_state, created_at, updated_at
)
VALUES (
    @org_id, @public_id, @name, @description, @connection_mode, @static_labels, @duplicate_window_seconds,
    false, @heartbeat_timeout_seconds, 'not_configured', @now::timestamptz, @now::timestamptz
)
RETURNING id;

-- UpdateIntegration replaces the configured fields; the runtime state (Heartbeat state, counters) is left alone.
-- name: UpdateIntegration :exec
UPDATE integrations
SET name = @name, description = @description, static_labels = @static_labels,
    duplicate_window_seconds = @duplicate_window_seconds, heartbeat_timeout_seconds = @heartbeat_timeout_seconds,
    updated_at = @now::timestamptz, version = version + 1
WHERE org_id = @org_id AND id = @id;

-- DeleteIntegration marks an Integration deleted; its row, its tokens and its Stored Snapshots stay.
-- name: DeleteIntegration :exec
UPDATE integrations
SET deleted_at = @now::timestamptz, updated_at = @now::timestamptz, version = version + 1
WHERE org_id = @org_id AND id = @id;

-- ListIntegrationInfo lists the id and the name of every Integration that is not deleted, for muster_integration_info.
-- name: ListIntegrationInfo :many
SELECT public_id, name
FROM integrations
WHERE org_id = @org_id AND deleted_at IS NULL
ORDER BY id;

-- InsertIntegrationToken stores an Integration token by the SHA-256 of its value.
-- name: InsertIntegrationToken :one
INSERT INTO integration_tokens (org_id, public_id, integration_id, name, token_hash, created_at)
VALUES (@org_id, @public_id, @integration_id, sqlc.narg('name'), @token_hash, @now::timestamptz)
RETURNING id;

-- LockDemo serializes the demo start-up step of replicas that start together, until the transaction ends.
-- name: LockDemo :exec
SELECT pg_advisory_xact_lock(@key::bigint);

-- EnsureIntegrationToken stores a token of the development demo, or gives the stored one back to the Integration
-- and takes back its revocation; it returns the token's public_id when it wrote a row, and no row otherwise.
-- name: EnsureIntegrationToken :one
INSERT INTO integration_tokens (org_id, public_id, integration_id, name, token_hash, created_at)
VALUES (@org_id, @public_id, @integration_id, sqlc.narg('name'), @token_hash, @now::timestamptz)
ON CONFLICT (token_hash) DO UPDATE
SET integration_id = EXCLUDED.integration_id, revoked_at = NULL
WHERE integration_tokens.org_id = @org_id
  AND (integration_tokens.integration_id <> EXCLUDED.integration_id OR integration_tokens.revoked_at IS NOT NULL)
RETURNING public_id;

-- ListIntegrationTokens lists the tokens of an Integration that are not revoked, the newest first.
-- name: ListIntegrationTokens :many
SELECT id, public_id, name, created_at, last_used_at
FROM integration_tokens
WHERE org_id = @org_id AND integration_id = @integration_id AND revoked_at IS NULL
ORDER BY created_at DESC, id DESC;

-- RevokeIntegrationToken revokes a token of an Integration that is not revoked yet and returns its name.
-- name: RevokeIntegrationToken :one
UPDATE integration_tokens
SET revoked_at = @now::timestamptz
WHERE org_id = @org_id AND integration_id = @integration_id AND public_id = @public_id AND revoked_at IS NULL
RETURNING id, name;

-- FindIngestToken finds an Integration token by the SHA-256 of its value, with what ingestion decides on: whether
-- the token is revoked, and the Integration with its deletion.
-- name: FindIngestToken :one
SELECT t.id, t.token_hash, t.revoked_at, t.last_used_at,
       i.id AS integration_id, i.public_id AS integration_public_id, i.deleted_at AS integration_deleted_at
FROM integration_tokens t
JOIN integrations i ON i.org_id = @org_id AND i.id = t.integration_id
WHERE t.org_id = @org_id AND t.token_hash = @token_hash;

-- TouchIntegrationToken records the use of a token, at most once a minute: a row used since @stale_before is left
-- alone.
-- name: TouchIntegrationToken :exec
UPDATE integration_tokens
SET last_used_at = @now::timestamptz
WHERE org_id = @org_id AND id = @id AND (last_used_at IS NULL OR last_used_at <= @stale_before::timestamptz);
