-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- name: GetKeyringState :one
SELECT active_key_id, activated_at, canary_ciphertext, canary_key_id
FROM keyring_state;

-- CreateKeyringState writes the first active key and its canary; it changes nothing when a row exists.
-- name: CreateKeyringState :execrows
INSERT INTO keyring_state (active_key_id, activated_at, canary_ciphertext, canary_key_id)
VALUES (@active_key_id, @activated_at, @canary_ciphertext, @active_key_id)
ON CONFLICT (singleton) DO NOTHING;

-- name: GetActiveKeyID :one
SELECT active_key_id
FROM keyring_state;

-- RecordReplica writes the replica's record at start and refreshes it; a pruned record comes back.
-- name: RecordReplica :exec
INSERT INTO replicas (replica_id, hostname, version, key_ids, started_at, refreshed_at)
VALUES (@replica_id, @hostname, @version, @key_ids, @started_at, @refreshed_at)
ON CONFLICT (replica_id) DO UPDATE
SET key_ids = EXCLUDED.key_ids, refreshed_at = EXCLUDED.refreshed_at;

-- name: DeleteReplica :exec
DELETE FROM replicas
WHERE replica_id = @replica_id;

-- name: ListLiveReplicas :many
SELECT replica_id, hostname, version, key_ids, started_at, refreshed_at
FROM replicas
WHERE refreshed_at > @live_since
ORDER BY replica_id;

-- PruneReplicas deletes the records of replicas not refreshed since refreshed_before, on the real clock.
-- name: PruneReplicas :execrows
DELETE FROM replicas
WHERE refreshed_at < @refreshed_before;

-- name: ListOrganizationIDs :many
SELECT id
FROM organizations
ORDER BY id;

-- CountEncryptedValuesByKey counts the Organization's encrypted values by the key that encrypted them.
-- name: CountEncryptedValuesByKey :many
SELECT key_id, count(*) AS encrypted
FROM encrypted_values
WHERE org_id = @org_id
GROUP BY key_id
ORDER BY key_id;
