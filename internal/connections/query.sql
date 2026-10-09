-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- Connections (C-13.FR-1, FR-6, C-14.FR-1): a messenger server or bot through which Destinations post. The bot token,
-- the proxy password and the Telegram webhook secret token are read as ciphertexts only to build the Connection's
-- client or to check a webhook request; reads show their status.

-- ListConnections reads a page of the Connections that are not deleted, in id order after a cursor, filtered by type,
-- each with the count of its Destinations that are not deleted.
-- name: ListConnections :many
SELECT c.id, c.public_id, c.type, c.name, c.mattermost_server_url, c.telegram_bot_api_base_url,
       c.telegram_update_mode, c.telegram_update_offset, c.telegram_webhook_secret_ciphertext,
       c.telegram_webhook_secret_key_id, c.telegram_webhook_secret_updated_at, c.bot_token_ciphertext,
       c.bot_token_key_id, c.bot_token_updated_at, c.bot_user_id, c.bot_username, c.proxy, c.proxy_password_ciphertext,
       c.proxy_password_key_id, c.proxy_password_updated_at, c.limiter_limit, c.limiter_per_seconds, c.created_at,
       c.version,
       (SELECT count(*)
        FROM destinations d
        WHERE d.org_id = c.org_id AND d.connection_id = c.id AND d.deleted_at IS NULL)::bigint AS destination_count
FROM connections c
WHERE c.org_id = @org_id AND c.deleted_at IS NULL
  AND (sqlc.narg('type')::text IS NULL OR c.type = sqlc.narg('type')::text)
  AND (sqlc.narg('after_id')::bigint IS NULL OR c.id > sqlc.narg('after_id')::bigint)
ORDER BY c.id
LIMIT @page_size;

-- GetConnection reads a Connection that is not deleted by its public_id, or by its id when public_id is null.
-- name: GetConnection :one
SELECT c.id, c.public_id, c.type, c.name, c.mattermost_server_url, c.telegram_bot_api_base_url,
       c.telegram_update_mode, c.telegram_update_offset, c.telegram_webhook_secret_ciphertext,
       c.telegram_webhook_secret_key_id, c.telegram_webhook_secret_updated_at, c.bot_token_ciphertext,
       c.bot_token_key_id, c.bot_token_updated_at, c.bot_user_id, c.bot_username, c.proxy, c.proxy_password_ciphertext,
       c.proxy_password_key_id, c.proxy_password_updated_at, c.limiter_limit, c.limiter_per_seconds, c.created_at,
       c.version,
       (SELECT count(*)
        FROM destinations d
        WHERE d.org_id = c.org_id AND d.connection_id = c.id AND d.deleted_at IS NULL)::bigint AS destination_count
FROM connections c
WHERE c.org_id = @org_id AND c.deleted_at IS NULL
  AND (c.public_id = sqlc.narg('public_id')::text
       OR (sqlc.narg('public_id')::text IS NULL AND c.id = sqlc.narg('id')::bigint));

-- LockConnection locks a Connection that is not deleted for a change or its deletion.
-- name: LockConnection :one
SELECT id, public_id, type, version
FROM connections
WHERE org_id = @org_id AND public_id = @public_id AND deleted_at IS NULL
FOR NO KEY UPDATE;

-- FindConnectionByName returns the id of the Connection that is not deleted and has the name.
-- name: FindConnectionByName :one
SELECT id
FROM connections
WHERE org_id = @org_id AND name = @name AND deleted_at IS NULL;

-- InsertConnection creates a Connection: a Mattermost one with its server URL, or a Telegram one with its Bot API base
-- URL, its update mode and, in the webhook mode, its webhook secret token.
-- name: InsertConnection :one
INSERT INTO connections (
    org_id, public_id, type, name, mattermost_server_url, telegram_bot_api_base_url, telegram_update_mode,
    telegram_webhook_secret_ciphertext, telegram_webhook_secret_key_id, telegram_webhook_secret_updated_at,
    bot_token_ciphertext, bot_token_key_id, bot_token_updated_at, proxy, proxy_password_ciphertext,
    proxy_password_key_id, proxy_password_updated_at, limiter_limit, limiter_per_seconds, created_at, updated_at
)
VALUES (
    @org_id, @public_id, @type, @name, sqlc.narg('server_url'), sqlc.narg('bot_api_base_url'),
    sqlc.narg('update_mode'), sqlc.narg('webhook_secret_ciphertext'), sqlc.narg('webhook_secret_key_id'),
    sqlc.narg('webhook_secret_updated_at'), @bot_token_ciphertext, @bot_token_key_id,
    @bot_token_updated_at::timestamptz, @proxy, sqlc.narg('proxy_password_ciphertext'),
    sqlc.narg('proxy_password_key_id'), sqlc.narg('proxy_password_updated_at'), @limiter_limit, @limiter_per_seconds,
    @now::timestamptz, @now::timestamptz
)
RETURNING id;

-- UpdateConnection replaces the configured fields of a Connection and its secrets as the write path computed them.
-- A changed server URL, Bot API base URL or bot token forgets the bot the last check learned; a new bot token also
-- forgets telegram_update_offset, since another bot's update ids start elsewhere.
-- name: UpdateConnection :exec
UPDATE connections
SET name = @name, mattermost_server_url = sqlc.narg('server_url'),
    telegram_bot_api_base_url = sqlc.narg('bot_api_base_url'), telegram_update_mode = sqlc.narg('update_mode'),
    telegram_webhook_secret_ciphertext = sqlc.narg('webhook_secret_ciphertext'),
    telegram_webhook_secret_key_id = sqlc.narg('webhook_secret_key_id'),
    telegram_webhook_secret_updated_at = sqlc.narg('webhook_secret_updated_at'),
    bot_token_ciphertext = @bot_token_ciphertext, bot_token_key_id = @bot_token_key_id,
    bot_token_updated_at = @bot_token_updated_at::timestamptz,
    bot_user_id = CASE WHEN @forget_bot::boolean THEN NULL ELSE bot_user_id END,
    bot_username = CASE WHEN @forget_bot::boolean THEN NULL ELSE bot_username END,
    telegram_update_offset = CASE WHEN @forget_updates::boolean THEN NULL ELSE telegram_update_offset END,
    proxy = @proxy, proxy_password_ciphertext = sqlc.narg('proxy_password_ciphertext'),
    proxy_password_key_id = sqlc.narg('proxy_password_key_id'),
    proxy_password_updated_at = sqlc.narg('proxy_password_updated_at'), limiter_limit = @limiter_limit,
    limiter_per_seconds = @limiter_per_seconds, updated_at = @now::timestamptz, version = version + 1
WHERE org_id = @org_id AND id = @id;

-- SetBotIdentity records the bot that a successful Connection check found; it is not a change of the configuration.
-- name: SetBotIdentity :exec
UPDATE connections
SET bot_user_id = @bot_user_id, bot_username = @bot_username
WHERE org_id = @org_id AND id = @id AND deleted_at IS NULL;

-- CountConnectionDestinations counts the Destinations that are not deleted of a Connection, under the lock of the
-- Connection's deletion, which a Destination's save takes in share mode.
-- name: CountConnectionDestinations :one
SELECT count(*)
FROM destinations
WHERE org_id = @org_id AND connection_id = @connection_id AND deleted_at IS NULL;

-- MarkConnectionDeleted soft-deletes a Connection and wipes its secrets: it leaves every list and reads as missing,
-- the row stays for the history of its Destinations.
-- name: MarkConnectionDeleted :exec
UPDATE connections
SET deleted_at = @now::timestamptz, updated_at = @now::timestamptz, version = version + 1,
    bot_token_ciphertext = NULL, bot_token_key_id = NULL, proxy_password_ciphertext = NULL,
    proxy_password_key_id = NULL, telegram_webhook_secret_ciphertext = NULL, telegram_webhook_secret_key_id = NULL
WHERE org_id = @org_id AND id = @id;

-- LockDemo serializes the demo start-up step of replicas that start together, until the transaction ends.
-- name: LockDemo :exec
SELECT pg_advisory_xact_lock(@key::bigint);

-- GetDestinationTarget reads where a Mattermost Destination posts — its team and channel — with the Connection it
-- posts through and that Connection's secrets as stored. A deleted Destination is read too, since its final edit still
-- runs; its Connection must not be deleted.
-- name: GetDestinationTarget :one
SELECT d.mattermost_team_id, d.mattermost_team_name, d.mattermost_channel_id, c.id, c.public_id, c.type, c.name,
       c.mattermost_server_url, c.bot_token_ciphertext, c.bot_token_key_id, c.bot_token_updated_at, c.proxy,
       c.proxy_password_ciphertext, c.proxy_password_key_id, c.proxy_password_updated_at, c.version
FROM destinations d
JOIN connections c ON c.org_id = d.org_id AND c.id = d.connection_id
WHERE d.org_id = @org_id AND d.id = @destination_id AND d.type = 'mattermost' AND c.deleted_at IS NULL;

-- ListMattermostDestinations lists the Mattermost Destinations that are not deleted, in id order, with their
-- Connection, team and channel, for muster doctor.
-- name: ListMattermostDestinations :many
SELECT id, public_id, name, connection_id, mattermost_team_id, mattermost_channel_id
FROM destinations
WHERE org_id = @org_id AND type = 'mattermost' AND deleted_at IS NULL
ORDER BY id;

-- ListPollingConnections lists the Telegram Connections that are not deleted and are in the long-polling mode, with
-- their secrets as stored and their telegram_update_offset, for the Leader's poller.
-- name: ListPollingConnections :many
SELECT id, public_id, telegram_bot_api_base_url, telegram_update_offset, bot_token_ciphertext, bot_token_key_id,
       bot_token_updated_at, proxy, proxy_password_ciphertext, proxy_password_key_id, proxy_password_updated_at,
       version
FROM connections
WHERE org_id = @org_id AND type = 'telegram' AND telegram_update_mode = 'long_polling' AND deleted_at IS NULL
ORDER BY id;

-- LockUpdateOffset reads telegram_update_offset of a Telegram Connection that is not deleted — the id after the last
-- update handed to the router — and locks it until the transaction ends, so that a second poller or a webhook request
-- with the same update waits for the first and then skips it.
-- name: LockUpdateOffset :one
SELECT telegram_update_offset
FROM connections
WHERE org_id = @org_id AND id = @id AND type = 'telegram' AND deleted_at IS NULL
FOR NO KEY UPDATE;

-- StoreUpdateOffset raises telegram_update_offset of a Telegram Connection to @next; it never lowers it, so that a
-- poller of a frozen old Leader cannot move it back. It is not a change of the configuration.
-- name: StoreUpdateOffset :exec
UPDATE connections
SET telegram_update_offset = GREATEST(coalesce(telegram_update_offset, @next::bigint), @next::bigint)
WHERE org_id = @org_id AND id = @id AND type = 'telegram';
