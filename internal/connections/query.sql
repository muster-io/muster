-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- Connections (C-13.FR-1, FR-6, C-14.FR-1, C-22.FR-1, C-24.FR-1): a messenger server, bot or bridge through which
-- Destinations post. The bot token, the proxy password, the Telegram webhook secret token, the push secret of a Bridge
-- and the app-level token and Slack signing secret of a Slack Connection are read as ciphertexts only to build the
-- Connection's client or to check a request to Muster; reads show their status.

-- ListConnections reads a page of the Connections that are not deleted, in id order after a cursor, filtered by type,
-- each with the count of its Destinations that are not deleted.
-- name: ListConnections :many
SELECT c.id, c.public_id, c.type, c.name, c.mattermost_server_url, c.telegram_bot_api_base_url,
       c.telegram_update_mode, c.telegram_update_offset, c.telegram_webhook_secret_ciphertext,
       c.telegram_webhook_secret_key_id, c.telegram_webhook_secret_updated_at, c.bot_token_ciphertext,
       c.bot_token_key_id, c.bot_token_updated_at, c.bot_user_id, c.bot_username, c.proxy, c.proxy_password_ciphertext,
       c.proxy_password_key_id, c.proxy_password_updated_at, c.limiter_limit, c.limiter_per_seconds, c.created_at,
       c.version, c.bridge_base_url, c.bridge_event_delivery, c.bridge_push_secret_ciphertext,
       c.bridge_push_secret_key_id, c.bridge_push_secret_updated_at, c.bridge_info, c.bridge_info_read_at,
       c.slack_event_mode, c.slack_app_token_ciphertext, c.slack_app_token_key_id, c.slack_app_token_updated_at,
       c.slack_signing_secret_ciphertext, c.slack_signing_secret_key_id, c.slack_signing_secret_updated_at,
       c.slack_team_id, c.slack_team_name, c.slack_team_url, c.slack_app_id, c.slack_bot_id,
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
       c.version, c.bridge_base_url, c.bridge_event_delivery, c.bridge_push_secret_ciphertext,
       c.bridge_push_secret_key_id, c.bridge_push_secret_updated_at, c.bridge_info, c.bridge_info_read_at,
       c.slack_event_mode, c.slack_app_token_ciphertext, c.slack_app_token_key_id, c.slack_app_token_updated_at,
       c.slack_signing_secret_ciphertext, c.slack_signing_secret_key_id, c.slack_signing_secret_updated_at,
       c.slack_team_id, c.slack_team_name, c.slack_team_url, c.slack_app_id, c.slack_bot_id,
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

-- InsertConnection creates a Connection: a Mattermost one with its server URL, a Telegram one with its Bot API base
-- URL, its update mode and, in the webhook mode, its webhook secret token, a Bridge with its base URL, its event
-- delivery mode and, in the push mode, its push secret, or a Slack Connection with its event mode, its app-level token
-- and its Slack signing secret.
-- name: InsertConnection :one
INSERT INTO connections (
    org_id, public_id, type, name, mattermost_server_url, telegram_bot_api_base_url, telegram_update_mode,
    telegram_webhook_secret_ciphertext, telegram_webhook_secret_key_id, telegram_webhook_secret_updated_at,
    bridge_base_url, bridge_event_delivery, bridge_push_secret_ciphertext, bridge_push_secret_key_id,
    bridge_push_secret_updated_at, slack_event_mode, slack_app_token_ciphertext, slack_app_token_key_id,
    slack_app_token_updated_at, slack_signing_secret_ciphertext, slack_signing_secret_key_id,
    slack_signing_secret_updated_at, bot_token_ciphertext, bot_token_key_id, bot_token_updated_at, proxy,
    proxy_password_ciphertext, proxy_password_key_id, proxy_password_updated_at, limiter_limit, limiter_per_seconds,
    created_at, updated_at
)
VALUES (
    @org_id, @public_id, @type, @name, sqlc.narg('server_url'), sqlc.narg('bot_api_base_url'),
    sqlc.narg('update_mode'), sqlc.narg('webhook_secret_ciphertext'), sqlc.narg('webhook_secret_key_id'),
    sqlc.narg('webhook_secret_updated_at'), sqlc.narg('bridge_base_url'), sqlc.narg('event_delivery'),
    sqlc.narg('push_secret_ciphertext'), sqlc.narg('push_secret_key_id'), sqlc.narg('push_secret_updated_at'),
    sqlc.narg('slack_event_mode'), sqlc.narg('slack_app_token_ciphertext'), sqlc.narg('slack_app_token_key_id'),
    sqlc.narg('slack_app_token_updated_at'), sqlc.narg('slack_signing_secret_ciphertext'),
    sqlc.narg('slack_signing_secret_key_id'), sqlc.narg('slack_signing_secret_updated_at'), @bot_token_ciphertext,
    @bot_token_key_id, @bot_token_updated_at::timestamptz, @proxy,
    sqlc.narg('proxy_password_ciphertext'), sqlc.narg('proxy_password_key_id'), sqlc.narg('proxy_password_updated_at'),
    @limiter_limit, @limiter_per_seconds, @now::timestamptz, @now::timestamptz
)
RETURNING id;

-- UpdateConnection replaces the configured fields of a Connection and its secrets as the write path computed them.
-- A changed server URL, Bot API base URL, bridge base URL or bot token forgets the bot the last check learned, what a
-- bridge told — its info and the cursor of its events — and what a Slack check learned — the workspace, the app and
-- the bot's id; a new bot token also forgets telegram_update_offset, since another bot's update ids start elsewhere.
-- name: UpdateConnection :exec
UPDATE connections
SET name = @name, mattermost_server_url = sqlc.narg('server_url'),
    telegram_bot_api_base_url = sqlc.narg('bot_api_base_url'), telegram_update_mode = sqlc.narg('update_mode'),
    telegram_webhook_secret_ciphertext = sqlc.narg('webhook_secret_ciphertext'),
    telegram_webhook_secret_key_id = sqlc.narg('webhook_secret_key_id'),
    telegram_webhook_secret_updated_at = sqlc.narg('webhook_secret_updated_at'),
    bridge_base_url = sqlc.narg('bridge_base_url'), bridge_event_delivery = sqlc.narg('event_delivery'),
    bridge_push_secret_ciphertext = sqlc.narg('push_secret_ciphertext'),
    bridge_push_secret_key_id = sqlc.narg('push_secret_key_id'),
    bridge_push_secret_updated_at = sqlc.narg('push_secret_updated_at'),
    slack_event_mode = sqlc.narg('slack_event_mode'),
    slack_app_token_ciphertext = sqlc.narg('slack_app_token_ciphertext'),
    slack_app_token_key_id = sqlc.narg('slack_app_token_key_id'),
    slack_app_token_updated_at = sqlc.narg('slack_app_token_updated_at'),
    slack_signing_secret_ciphertext = sqlc.narg('slack_signing_secret_ciphertext'),
    slack_signing_secret_key_id = sqlc.narg('slack_signing_secret_key_id'),
    slack_signing_secret_updated_at = sqlc.narg('slack_signing_secret_updated_at'),
    bot_token_ciphertext = @bot_token_ciphertext, bot_token_key_id = @bot_token_key_id,
    bot_token_updated_at = @bot_token_updated_at::timestamptz,
    bot_user_id = CASE WHEN @forget_bot::boolean THEN NULL ELSE bot_user_id END,
    bot_username = CASE WHEN @forget_bot::boolean THEN NULL ELSE bot_username END,
    bridge_info = CASE WHEN @forget_bot::boolean THEN NULL ELSE bridge_info END,
    bridge_info_read_at = CASE WHEN @forget_bot::boolean THEN NULL ELSE bridge_info_read_at END,
    bridge_events_cursor = CASE WHEN @forget_bot::boolean THEN NULL ELSE bridge_events_cursor END,
    slack_team_id = CASE WHEN @forget_bot::boolean THEN NULL ELSE slack_team_id END,
    slack_team_name = CASE WHEN @forget_bot::boolean THEN NULL ELSE slack_team_name END,
    slack_team_url = CASE WHEN @forget_bot::boolean THEN NULL ELSE slack_team_url END,
    slack_app_id = CASE WHEN @forget_bot::boolean THEN NULL ELSE slack_app_id END,
    slack_bot_id = CASE WHEN @forget_bot::boolean THEN NULL ELSE slack_bot_id END,
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

-- SetBridgeInfo records the info that a check of a Bridge Connection read, as the bridge sent it, at read_at on the
-- business clock, unless the Connection changed since the check read it; it is not a change of the configuration.
-- name: SetBridgeInfo :execrows
UPDATE connections
SET bridge_info = @info::jsonb, bridge_info_read_at = @read_at::timestamptz
WHERE org_id = @org_id AND id = @id AND type = 'bridge' AND version = @version AND deleted_at IS NULL;

-- SetSlackIdentity records what a passing check of a Slack Connection learned — the workspace, its address and the
-- bot, and the app when the check had the step app_token, keeping the stored app otherwise — unless the Connection
-- changed since the check read it; it is not a change of the configuration.
-- name: SetSlackIdentity :execrows
UPDATE connections
SET slack_team_id = @team_id, slack_team_name = @team_name, slack_team_url = @team_url, slack_bot_id = @bot_id,
    bot_user_id = @bot_user_id, bot_username = @bot_username,
    slack_app_id = coalesce(sqlc.narg('app_id')::text, slack_app_id)
WHERE org_id = @org_id AND id = @id AND type = 'slack' AND version = @version AND deleted_at IS NULL;

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
    proxy_password_key_id = NULL, telegram_webhook_secret_ciphertext = NULL, telegram_webhook_secret_key_id = NULL,
    bridge_push_secret_ciphertext = NULL, bridge_push_secret_key_id = NULL, slack_app_token_ciphertext = NULL,
    slack_app_token_key_id = NULL, slack_signing_secret_ciphertext = NULL, slack_signing_secret_key_id = NULL
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
       c.proxy_password_ciphertext, c.proxy_password_key_id, c.proxy_password_updated_at, c.version, c.bot_user_id
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

-- GetTelegramDestinationTarget reads where a Telegram Destination sends — its channel as entered and the ids of the
-- channel and of its discussion group that its Destination check learned — with the Connection it sends through and
-- that Connection's secrets as stored. A deleted Destination is read too, since its final edit still runs; its
-- Connection must not be deleted.
-- name: GetTelegramDestinationTarget :one
SELECT d.telegram_channel_id, d.telegram_channel_chat_id, d.telegram_discussion_chat_id, c.id, c.public_id, c.type,
       c.name, c.telegram_bot_api_base_url, c.bot_token_ciphertext, c.bot_token_key_id, c.bot_token_updated_at,
       c.proxy, c.proxy_password_ciphertext, c.proxy_password_key_id, c.proxy_password_updated_at, c.version
FROM destinations d
JOIN connections c ON c.org_id = d.org_id AND c.id = d.connection_id
WHERE d.org_id = @org_id AND d.id = @destination_id AND d.type = 'telegram' AND c.deleted_at IS NULL;

-- ListTelegramDestinations lists the Telegram Destinations that are not deleted, in id order, with their Connection
-- and channel, for muster doctor.
-- name: ListTelegramDestinations :many
SELECT id, public_id, name, connection_id, telegram_channel_id
FROM destinations
WHERE org_id = @org_id AND type = 'telegram' AND deleted_at IS NULL
ORDER BY id;

-- GetBridgeDestinationTarget reads where a Bridge Destination posts — its channel id as the messenger names it — with
-- the Connection it posts through, that Connection's secrets as stored and its version, which its client is cached for.
-- A deleted Destination is read too, since its final edit still runs; its Connection must not be deleted.
-- name: GetBridgeDestinationTarget :one
SELECT d.bridge_channel_id, c.id, c.public_id, c.type, c.name, c.bridge_base_url, c.bot_token_ciphertext,
       c.bot_token_key_id, c.bot_token_updated_at, c.bridge_push_secret_ciphertext, c.bridge_push_secret_key_id,
       c.bridge_push_secret_updated_at, c.proxy, c.proxy_password_ciphertext, c.proxy_password_key_id,
       c.proxy_password_updated_at, c.version
FROM destinations d
JOIN connections c ON c.org_id = d.org_id AND c.id = d.connection_id
WHERE d.org_id = @org_id AND d.id = @destination_id AND d.type = 'bridge' AND c.deleted_at IS NULL;

-- ListBridgeDestinations lists the Bridge Destinations that are not deleted, in id order, with their Connection and
-- channel, for muster doctor.
-- name: ListBridgeDestinations :many
SELECT id, public_id, name, connection_id, bridge_channel_id
FROM destinations
WHERE org_id = @org_id AND type = 'bridge' AND deleted_at IS NULL
ORDER BY id;

-- ListSlackDestinations lists the Slack Destinations that are not deleted, in id order, with their Connection and
-- channel id, for muster doctor.
-- name: ListSlackDestinations :many
SELECT id, public_id, name, connection_id, slack_channel_id
FROM destinations
WHERE org_id = @org_id AND type = 'slack' AND deleted_at IS NULL
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

-- LockUpdates takes, until the transaction ends, the update lock of a Telegram Connection (class
-- db.TelegramUpdateLockClass, keyed by hashint8 of its id), so that a second poller or a webhook request with the same
-- update waits for the first and then skips it; the delivery worker tries it shared, without waiting, before an edit of
-- a Root message of the Connection (delivery's TryShareUpdateLock). It never locks the connections row, which a save
-- of the Connection holds while setWebhook runs; a collision of the hash only serializes the updates of two
-- Connections.
-- name: LockUpdates :exec
SELECT pg_advisory_xact_lock(@lock_class::int, hashint8(@id::bigint));

-- GetUpdateOffset reads telegram_update_offset of a Telegram Connection that is not deleted — the id after the last
-- update handed to the router — and when its bot token was set, without a row lock, under the update lock of the
-- Connection.
-- name: GetUpdateOffset :one
SELECT telegram_update_offset, bot_token_updated_at
FROM connections
WHERE org_id = @org_id AND id = @id AND type = 'telegram' AND deleted_at IS NULL;

-- StoreUpdateOffset raises telegram_update_offset of a Telegram Connection to @next; it never lowers it, so that a
-- poller of a frozen old Leader cannot move it back. It changes nothing when the bot token was set again since
-- @token_updated_at, which forgot telegram_update_offset: the ids of another bot's updates start elsewhere. It is not
-- a change of the configuration.
-- name: StoreUpdateOffset :exec
UPDATE connections
SET telegram_update_offset = GREATEST(coalesce(telegram_update_offset, @next::bigint), @next::bigint)
WHERE org_id = @org_id AND id = @id AND type = 'telegram' AND bot_token_updated_at = @token_updated_at::timestamptz;

-- GetOutage reads, for the age of Telegram presses (C-14.FR-4), when Muster was last known not to run: the Leader's
-- alive mark and the latest downtime period, the zero time for none. All three are installation-wide and on the
-- business clock.
-- name: GetOutage :one
SELECT coalesce((SELECT rs.alive_at FROM runtime_state rs),
                '0001-01-01 00:00:00+00')::timestamptz AS alive_at,
       coalesce((SELECT d.started_at FROM downtime_periods d ORDER BY d.ended_at DESC, d.id DESC LIMIT 1),
                '0001-01-01 00:00:00+00')::timestamptz AS downtime_started_at,
       coalesce((SELECT d.ended_at FROM downtime_periods d ORDER BY d.ended_at DESC, d.id DESC LIMIT 1),
                '0001-01-01 00:00:00+00')::timestamptz AS downtime_ended_at;

-- ListBridgePollingConnections lists the Bridge Connections that are not deleted and receive their events by polling,
-- with their secrets as stored, the cursor of their events and their version, for the Leader's poller (C-22.FR-11).
-- name: ListBridgePollingConnections :many
SELECT id, public_id, bridge_base_url, bridge_events_cursor, bot_token_ciphertext, bot_token_key_id,
       bot_token_updated_at, proxy, proxy_password_ciphertext, proxy_password_key_id, proxy_password_updated_at,
       version
FROM connections
WHERE org_id = @org_id AND type = 'bridge' AND bridge_event_delivery = 'poll' AND deleted_at IS NULL
ORDER BY id;

-- StoreBridgeEventsCursor stores where the poller of a Bridge Connection goes on — null drops the cursor — unless the
-- Connection changed since the poller read it: a poller of an earlier version, or of a frozen old Leader, writes
-- nothing. It is not a change of the configuration.
-- name: StoreBridgeEventsCursor :execrows
UPDATE connections
SET bridge_events_cursor = sqlc.narg('cursor')::text
WHERE org_id = @org_id AND id = @id AND type = 'bridge' AND version = @version AND deleted_at IS NULL;

-- LockBridgeEvent takes, until the transaction ends, the claim lock of one event of a Bridge Connection (the class
-- bridgeEventLockClass of internal/connections, 0x6d75_0007, keyed by hashtext of the Connection's id and the event
-- id), so that a second copy of the event — a push retried while the first runs, or an overlapping poll — waits for
-- the first and then finds its id recorded. It never locks a row; a collision of the hash only serializes two events.
-- name: LockBridgeEvent :exec
SELECT pg_advisory_xact_lock(@lock_class::int, hashtext(format('%s/%s', @connection_id::bigint, @event_id::text)));

-- BridgeEventSeen reports whether the event id of a Bridge Connection was handled within bridge.event_dedup_window.
-- name: BridgeEventSeen :one
SELECT EXISTS (SELECT 1
               FROM bridge_events_seen
               WHERE org_id = @org_id AND connection_id = @connection_id AND event_id = @event_id)::boolean AS seen;

-- RecordBridgeEvent records the event id of a Bridge Connection as handled at seen_at, on the business clock.
-- name: RecordBridgeEvent :exec
INSERT INTO bridge_events_seen (org_id, connection_id, event_id, seen_at)
VALUES (@org_id, @connection_id, @event_id, @seen_at::timestamptz)
ON CONFLICT (org_id, connection_id, event_id) DO NOTHING;

-- PruneBridgeEventsSeen deletes at most @batch_size records of Bridge event ids seen before @before, skipping rows
-- another transaction holds.
-- name: PruneBridgeEventsSeen :execrows
DELETE FROM bridge_events_seen e
WHERE e.org_id = @org_id
  AND (e.connection_id, e.event_id) IN (
      SELECT s.connection_id, s.event_id
      FROM bridge_events_seen s
      WHERE s.org_id = @org_id AND s.seen_at < @before::timestamptz
      ORDER BY s.seen_at
      LIMIT @batch_size
      FOR UPDATE SKIP LOCKED);
