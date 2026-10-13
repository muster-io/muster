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

-- RecordReplica writes the replica's record at start and refreshes it; a pruned record comes back. started_at moves
-- when the replica re-registers after it lost the database.
-- name: RecordReplica :exec
INSERT INTO replicas (replica_id, hostname, version, key_ids, started_at, refreshed_at)
VALUES (@replica_id, @hostname, @version, @key_ids, @started_at, @refreshed_at)
ON CONFLICT (replica_id) DO UPDATE
SET key_ids = EXCLUDED.key_ids, started_at = EXCLUDED.started_at, refreshed_at = EXCLUDED.refreshed_at;

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

-- CountOpenButtonKeys counts, by key id, the deliveries of the Organization's open Alert Groups whose Root message
-- carries buttons that key signed (C-20.FR-6). A delivery deleted in the messenger, retired or withheld depends on
-- nothing: the re-render after a rotation never re-signs it.
-- name: CountOpenButtonKeys :many
SELECT d.actual_button_key_id::text AS key_id, count(*) AS messages
FROM deliveries d
JOIN alert_groups g ON g.org_id = d.org_id AND g.id = d.alert_group_id
WHERE d.org_id = @org_id AND d.actual_button_key_id IS NOT NULL
  AND d.state NOT IN ('deleted_in_messenger', 'retired', 'withheld') AND g.status <> 'resolved'
GROUP BY d.actual_button_key_id
ORDER BY 1;

-- ListActivatedKeys reads the before and after values of /active_key_id in the Organization's keyring.key_activated
-- Audit entries: every key named there was active before (C-20.FR-6).
-- name: ListActivatedKeys :many
SELECT coalesce(c.change ->> 'before', '')::text AS before_key_id, coalesce(c.change ->> 'after', '')::text AS after_key_id
FROM audit_log a
CROSS JOIN LATERAL jsonb_array_elements(a.diff) AS c (change)
WHERE a.org_id = @org_id AND a.action = 'keyring.key_activated' AND c.change ->> 'pointer' = '/active_key_id';

-- LockKeyringState reads and locks the active key for its activation by `muster secrets rotate-key`.
-- name: LockKeyringState :one
SELECT active_key_id
FROM keyring_state
FOR UPDATE;

-- ActivateKey makes @key_id the active key at @activated_at with the canary re-encrypted with it, in the transaction
-- that re-encrypts every Secret; no row when the active key is no longer @previous_key_id.
-- name: ActivateKey :execrows
UPDATE keyring_state
SET active_key_id = @key_id::text, activated_at = @activated_at, canary_ciphertext = @canary_ciphertext,
    canary_key_id = @key_id::text
WHERE active_key_id = @previous_key_id::text;

-- CountOpenDeliveriesSignedByOtherKey counts the deliveries of the Organization's open Alert Groups whose Desired state
-- has buttons signed by a key other than @key_id and that the Leader re-renders after an activation (the selection of
-- the delivery package's ListRerenderGroups): pending or delivered, or Not delivered with a Root message, and no final
-- edit.
-- name: CountOpenDeliveriesSignedByOtherKey :one
SELECT count(*)
FROM deliveries d
JOIN alert_groups g ON g.org_id = d.org_id AND g.id = d.alert_group_id
WHERE d.org_id = @org_id AND g.status <> 'resolved' AND d.desired_button_key_id IS NOT NULL
  AND d.desired_button_key_id <> @key_id::text AND NOT d.desired_retire
  AND (d.state IN ('pending', 'delivered') OR (d.state = 'not_delivered' AND d.published_at IS NOT NULL));

-- NotifyKeyring wakes every replica on the keyring channel once the transaction commits.
-- name: NotifyKeyring :exec
SELECT pg_notify(@channel::text, @payload::text);

-- The re-encryption of `muster secrets rotate-key`: for every field of the encrypted_values view, the values of the
-- Organization that another key encrypted, locked, and the update of one value with its new ciphertext and key id.
-- An update keeps the field's *_updated_at (previous_signing_secret_since for the previous Signing secret): the
-- Secret itself did not change.

-- name: ListRotateHeartbeatURL :many
SELECT id, outgoing_heartbeat_url_ciphertext AS ciphertext, outgoing_heartbeat_url_key_id::text AS key_id
FROM organizations
WHERE id = @org_id AND outgoing_heartbeat_url_ciphertext IS NOT NULL AND outgoing_heartbeat_url_key_id <> @key_id::text
FOR NO KEY UPDATE;

-- name: ReencryptHeartbeatURL :execrows
UPDATE organizations
SET outgoing_heartbeat_url_ciphertext = @ciphertext, outgoing_heartbeat_url_key_id = @key_id::text
WHERE id = @org_id AND outgoing_heartbeat_url_key_id = @previous_key_id::text;

-- name: ListRotateHeartbeatProxyPassword :many
SELECT id, outgoing_heartbeat_proxy_password_ciphertext AS ciphertext,
       outgoing_heartbeat_proxy_password_key_id::text AS key_id
FROM organizations
WHERE id = @org_id AND outgoing_heartbeat_proxy_password_ciphertext IS NOT NULL
  AND outgoing_heartbeat_proxy_password_key_id <> @key_id::text
FOR NO KEY UPDATE;

-- name: ReencryptHeartbeatProxyPassword :execrows
UPDATE organizations
SET outgoing_heartbeat_proxy_password_ciphertext = @ciphertext, outgoing_heartbeat_proxy_password_key_id = @key_id::text
WHERE id = @org_id AND outgoing_heartbeat_proxy_password_key_id = @previous_key_id::text;

-- name: ListRotateOIDCClientSecret :many
SELECT org_id, client_secret_ciphertext AS ciphertext, client_secret_key_id::text AS key_id
FROM oidc_settings
WHERE org_id = @org_id AND client_secret_ciphertext IS NOT NULL AND client_secret_key_id <> @key_id::text
FOR NO KEY UPDATE;

-- name: ReencryptOIDCClientSecret :execrows
UPDATE oidc_settings
SET client_secret_ciphertext = @ciphertext, client_secret_key_id = @key_id::text
WHERE org_id = @org_id AND client_secret_key_id = @previous_key_id::text;

-- name: ListRotateOIDCProxyPassword :many
SELECT org_id, proxy_password_ciphertext AS ciphertext, proxy_password_key_id::text AS key_id
FROM oidc_settings
WHERE org_id = @org_id AND proxy_password_ciphertext IS NOT NULL AND proxy_password_key_id <> @key_id::text
FOR NO KEY UPDATE;

-- name: ReencryptOIDCProxyPassword :execrows
UPDATE oidc_settings
SET proxy_password_ciphertext = @ciphertext, proxy_password_key_id = @key_id::text
WHERE org_id = @org_id AND proxy_password_key_id = @previous_key_id::text;

-- name: ListRotateOIDCCodeVerifier :many
SELECT state_hash, code_verifier_ciphertext AS ciphertext, code_verifier_key_id::text AS key_id
FROM oidc_auth_requests
WHERE org_id = @org_id AND code_verifier_key_id <> @key_id::text
FOR NO KEY UPDATE;

-- name: ReencryptOIDCCodeVerifier :execrows
UPDATE oidc_auth_requests
SET code_verifier_ciphertext = @ciphertext, code_verifier_key_id = @key_id::text
WHERE org_id = @org_id AND state_hash = @state_hash AND code_verifier_key_id = @previous_key_id::text;

-- name: ListRotateOfflineToken :many
SELECT id, oidc_offline_token_ciphertext AS ciphertext, oidc_offline_token_key_id::text AS key_id
FROM users
WHERE org_id = @org_id AND oidc_offline_token_ciphertext IS NOT NULL AND oidc_offline_token_key_id <> @key_id::text
FOR NO KEY UPDATE;

-- name: ReencryptOfflineToken :execrows
UPDATE users
SET oidc_offline_token_ciphertext = @ciphertext, oidc_offline_token_key_id = @key_id::text
WHERE org_id = @org_id AND id = @id AND oidc_offline_token_key_id = @previous_key_id::text;

-- name: ListRotateTOTPSeed :many
SELECT user_id, seed_ciphertext AS ciphertext, seed_key_id::text AS key_id
FROM user_totp
WHERE org_id = @org_id AND seed_ciphertext IS NOT NULL AND seed_key_id <> @key_id::text
FOR NO KEY UPDATE;

-- name: ReencryptTOTPSeed :execrows
UPDATE user_totp
SET seed_ciphertext = @ciphertext, seed_key_id = @key_id::text
WHERE org_id = @org_id AND user_id = @user_id AND seed_key_id = @previous_key_id::text;

-- name: ListRotateTOTPPendingSeed :many
SELECT user_id, pending_seed_ciphertext AS ciphertext, pending_seed_key_id::text AS key_id
FROM user_totp
WHERE org_id = @org_id AND pending_seed_ciphertext IS NOT NULL AND pending_seed_key_id <> @key_id::text
FOR NO KEY UPDATE;

-- name: ReencryptTOTPPendingSeed :execrows
UPDATE user_totp
SET pending_seed_ciphertext = @ciphertext, pending_seed_key_id = @key_id::text
WHERE org_id = @org_id AND user_id = @user_id AND pending_seed_key_id = @previous_key_id::text;

-- name: ListRotateBotToken :many
SELECT id, bot_token_ciphertext AS ciphertext, bot_token_key_id::text AS key_id
FROM connections
WHERE org_id = @org_id AND bot_token_ciphertext IS NOT NULL AND bot_token_key_id <> @key_id::text
FOR NO KEY UPDATE;

-- name: ReencryptBotToken :execrows
UPDATE connections
SET bot_token_ciphertext = @ciphertext, bot_token_key_id = @key_id::text
WHERE org_id = @org_id AND id = @id AND bot_token_key_id = @previous_key_id::text;

-- name: ListRotateConnectionProxyPassword :many
SELECT id, proxy_password_ciphertext AS ciphertext, proxy_password_key_id::text AS key_id
FROM connections
WHERE org_id = @org_id AND proxy_password_ciphertext IS NOT NULL AND proxy_password_key_id <> @key_id::text
FOR NO KEY UPDATE;

-- name: ReencryptConnectionProxyPassword :execrows
UPDATE connections
SET proxy_password_ciphertext = @ciphertext, proxy_password_key_id = @key_id::text
WHERE org_id = @org_id AND id = @id AND proxy_password_key_id = @previous_key_id::text;

-- name: ListRotateTelegramWebhookSecret :many
SELECT id, telegram_webhook_secret_ciphertext AS ciphertext, telegram_webhook_secret_key_id::text AS key_id
FROM connections
WHERE org_id = @org_id AND telegram_webhook_secret_ciphertext IS NOT NULL
  AND telegram_webhook_secret_key_id <> @key_id::text
FOR NO KEY UPDATE;

-- name: ReencryptTelegramWebhookSecret :execrows
UPDATE connections
SET telegram_webhook_secret_ciphertext = @ciphertext, telegram_webhook_secret_key_id = @key_id::text
WHERE org_id = @org_id AND id = @id AND telegram_webhook_secret_key_id = @previous_key_id::text;

-- name: ListRotateDestinationProxyPassword :many
SELECT id, proxy_password_ciphertext AS ciphertext, proxy_password_key_id::text AS key_id
FROM destinations
WHERE org_id = @org_id AND proxy_password_ciphertext IS NOT NULL AND proxy_password_key_id <> @key_id::text
FOR NO KEY UPDATE;

-- name: ReencryptDestinationProxyPassword :execrows
UPDATE destinations
SET proxy_password_ciphertext = @ciphertext, proxy_password_key_id = @key_id::text
WHERE org_id = @org_id AND id = @id AND proxy_password_key_id = @previous_key_id::text;

-- name: ListRotateSigningSecret :many
SELECT id, signing_secret_ciphertext AS ciphertext, signing_secret_key_id::text AS key_id
FROM destinations
WHERE org_id = @org_id AND signing_secret_ciphertext IS NOT NULL AND signing_secret_key_id <> @key_id::text
FOR NO KEY UPDATE;

-- name: ReencryptSigningSecret :execrows
UPDATE destinations
SET signing_secret_ciphertext = @ciphertext, signing_secret_key_id = @key_id::text
WHERE org_id = @org_id AND id = @id AND signing_secret_key_id = @previous_key_id::text;

-- name: ListRotatePreviousSigningSecret :many
SELECT id, previous_signing_secret_ciphertext AS ciphertext, previous_signing_secret_key_id::text AS key_id
FROM destinations
WHERE org_id = @org_id AND previous_signing_secret_ciphertext IS NOT NULL
  AND previous_signing_secret_key_id <> @key_id::text
FOR NO KEY UPDATE;

-- name: ReencryptPreviousSigningSecret :execrows
UPDATE destinations
SET previous_signing_secret_ciphertext = @ciphertext, previous_signing_secret_key_id = @key_id::text
WHERE org_id = @org_id AND id = @id AND previous_signing_secret_key_id = @previous_key_id::text;

-- name: ListRotateDestinationSecret :many
SELECT destination_id, name, value_ciphertext AS ciphertext, value_key_id::text AS key_id
FROM destination_secrets
WHERE org_id = @org_id AND value_key_id <> @key_id::text
FOR NO KEY UPDATE;

-- name: ReencryptDestinationSecret :execrows
UPDATE destination_secrets
SET value_ciphertext = @ciphertext, value_key_id = @key_id::text
WHERE org_id = @org_id AND destination_id = @destination_id AND name = @name AND value_key_id = @previous_key_id::text;
