-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- Destinations (C-11.FR-18): reading every type with its health and Routes. Secrets are read only as their status.

-- ListDestinations reads a page of the Destinations that are not deleted, in id order after a cursor, filtered by
-- type, health and the public_id of a Route that is not deleted.
-- name: ListDestinations :many
SELECT d.id, d.public_id, d.type, d.name, c.public_id AS connection_public_id, d.mattermost_team_id,
       d.mattermost_channel_id, d.mattermost_team_name, d.mattermost_channel_name, d.telegram_channel_id,
       d.telegram_discussion_chat_id, d.telegram_channel_title, d.telegram_discussion_group_title, d.webhook_mode,
       d.webhook_events_config, d.webhook_template_config, d.proxy, (d.proxy_password_ciphertext IS NOT NULL)::boolean AS proxy_password_set,
       d.proxy_password_updated_at, (d.signing_secret_ciphertext IS NOT NULL)::boolean AS signing_secret_set,
       d.signing_secret_updated_at, d.previous_signing_secret_since, d.mentions, d.limiter_limit,
       d.limiter_per_seconds, d.health, d.broken_since, d.broken_reason, d.template_error_since, d.template_error,
       d.created_at, d.version
FROM destinations d
LEFT JOIN connections c ON c.org_id = d.org_id AND c.id = d.connection_id
WHERE d.org_id = @org_id AND d.deleted_at IS NULL
  AND (sqlc.narg('type')::text IS NULL OR d.type = sqlc.narg('type')::text)
  AND (sqlc.narg('health')::text IS NULL OR d.health = sqlc.narg('health')::text)
  AND (sqlc.narg('route')::text IS NULL
       OR EXISTS (SELECT 1
                  FROM route_destinations rd
                  JOIN routes r ON r.org_id = rd.org_id AND r.id = rd.route_id
                  WHERE rd.org_id = d.org_id AND rd.destination_id = d.id AND r.public_id = sqlc.narg('route')::text
                    AND r.deleted_at IS NULL))
  AND (sqlc.narg('after_id')::bigint IS NULL OR d.id > sqlc.narg('after_id')::bigint)
ORDER BY d.id
LIMIT @page_size;

-- GetDestination reads a Destination that is not deleted.
-- name: GetDestination :one
SELECT d.id, d.public_id, d.type, d.name, c.public_id AS connection_public_id, d.mattermost_team_id,
       d.mattermost_channel_id, d.mattermost_team_name, d.mattermost_channel_name, d.telegram_channel_id,
       d.telegram_discussion_chat_id, d.telegram_channel_title, d.telegram_discussion_group_title, d.webhook_mode,
       d.webhook_events_config, d.webhook_template_config, d.proxy, (d.proxy_password_ciphertext IS NOT NULL)::boolean AS proxy_password_set,
       d.proxy_password_updated_at, (d.signing_secret_ciphertext IS NOT NULL)::boolean AS signing_secret_set,
       d.signing_secret_updated_at, d.previous_signing_secret_since, d.mentions, d.limiter_limit,
       d.limiter_per_seconds, d.health, d.broken_since, d.broken_reason, d.template_error_since, d.template_error,
       d.created_at, d.version
FROM destinations d
LEFT JOIN connections c ON c.org_id = d.org_id AND c.id = d.connection_id
WHERE d.org_id = @org_id AND d.public_id = @public_id AND d.deleted_at IS NULL;

-- ListDestinationRoutes lists the Routes that are not deleted of the Destinations, by name.
-- name: ListDestinationRoutes :many
SELECT rd.destination_id, r.public_id, r.name
FROM route_destinations rd
JOIN routes r ON r.org_id = rd.org_id AND r.id = rd.route_id
WHERE rd.org_id = @org_id AND rd.destination_id = ANY(@destination_ids::bigint[]) AND r.deleted_at IS NULL
ORDER BY r.name, r.id;

-- ListRouteDestinationRefs lists the Destinations that are not deleted of the Routes, with their health, by name.
-- name: ListRouteDestinationRefs :many
SELECT rd.route_id, d.public_id, d.name, d.type, d.health, d.broken_since, d.broken_reason
FROM route_destinations rd
JOIN destinations d ON d.org_id = rd.org_id AND d.id = rd.destination_id
WHERE rd.org_id = @org_id AND rd.route_id = ANY(@route_ids::bigint[]) AND d.deleted_at IS NULL
ORDER BY d.name, d.id;

-- ListDestinationInfo lists the Destinations that are not deleted, for muster_destination_info.
-- name: ListDestinationInfo :many
SELECT public_id, name
FROM destinations
WHERE org_id = @org_id AND deleted_at IS NULL;

-- LockDestination locks a Destination that is not deleted for a change and reads what its Audit log entry names.
-- name: LockDestination :one
SELECT id, public_id, name, version
FROM destinations
WHERE org_id = @org_id AND public_id = @public_id AND deleted_at IS NULL
FOR NO KEY UPDATE;

-- MarkDestinationDeleted soft-deletes a Destination: it leaves every list and reads as missing, the row stays.
-- name: MarkDestinationDeleted :exec
UPDATE destinations
SET deleted_at = @now, updated_at = @now, version = version + 1
WHERE org_id = @org_id AND id = @id;

-- DeleteDestinationRoutes removes a deleted Destination from every Route.
-- name: DeleteDestinationRoutes :exec
DELETE FROM route_destinations
WHERE org_id = @org_id AND destination_id = @destination_id;

-- LockMattermostConnection takes the Mattermost Connection that is not deleted in share mode for the save of one of
-- its Destinations, so that the Connection's deletion, which locks it for update, sees the saved Destination.
-- name: LockMattermostConnection :one
SELECT id
FROM connections
WHERE org_id = @org_id AND id = @id AND type = 'mattermost' AND deleted_at IS NULL
FOR SHARE;

-- InsertMattermostDestination creates a healthy Mattermost Destination with the team and channel names its
-- Destination check read.
-- name: InsertMattermostDestination :one
INSERT INTO destinations (
    org_id, public_id, type, name, connection_id, mattermost_team_id, mattermost_channel_id, mattermost_team_name,
    mattermost_channel_name, mentions, limiter_limit, limiter_per_seconds, health, created_at, updated_at
)
VALUES (
    @org_id, @public_id, 'mattermost', @name, @connection_id, @team_id, @channel_id, @team_name, @channel_name,
    @mentions, @limiter_limit, @limiter_per_seconds, 'healthy', @now::timestamptz, @now::timestamptz
)
RETURNING id;

-- UpdateMattermostDestination replaces the configured fields of a Mattermost Destination and the team and channel
-- names its Destination check read; its health is left alone.
-- name: UpdateMattermostDestination :exec
UPDATE destinations
SET name = @name, connection_id = @connection_id, mattermost_team_id = @team_id,
    mattermost_channel_id = @channel_id, mattermost_team_name = @team_name, mattermost_channel_name = @channel_name,
    mentions = @mentions, limiter_limit = @limiter_limit, limiter_per_seconds = @limiter_per_seconds,
    updated_at = @now::timestamptz, version = version + 1
WHERE org_id = @org_id AND id = @id;

-- LockTelegramConnection takes the Telegram Connection that is not deleted in share mode for the save of one of its
-- Destinations, as LockMattermostConnection does.
-- name: LockTelegramConnection :one
SELECT id
FROM connections
WHERE org_id = @org_id AND id = @id AND type = 'telegram' AND deleted_at IS NULL
FOR SHARE;

-- InsertTelegramDestination creates a healthy Telegram Destination with the channel as entered and what its
-- Destination check found: the numeric ids and the titles of the channel and of its discussion group.
-- name: InsertTelegramDestination :one
INSERT INTO destinations (
    org_id, public_id, type, name, connection_id, telegram_channel_id, telegram_channel_chat_id,
    telegram_discussion_chat_id, telegram_channel_title, telegram_discussion_group_title, mentions, limiter_limit,
    limiter_per_seconds, health, created_at, updated_at
)
VALUES (
    @org_id, @public_id, 'telegram', @name, @connection_id, @channel_id, @channel_chat_id, @discussion_chat_id,
    @channel_title, @discussion_group_title, @mentions, @limiter_limit, @limiter_per_seconds, 'healthy',
    @now::timestamptz, @now::timestamptz
)
RETURNING id;

-- UpdateTelegramDestination replaces the configured fields of a Telegram Destination and what its Destination check
-- found; its health is left alone.
-- name: UpdateTelegramDestination :exec
UPDATE destinations
SET name = @name, connection_id = @connection_id, telegram_channel_id = @channel_id,
    telegram_channel_chat_id = @channel_chat_id, telegram_discussion_chat_id = @discussion_chat_id,
    telegram_channel_title = @channel_title, telegram_discussion_group_title = @discussion_group_title,
    mentions = @mentions, limiter_limit = @limiter_limit, limiter_per_seconds = @limiter_per_seconds,
    updated_at = @now::timestamptz, version = version + 1
WHERE org_id = @org_id AND id = @id AND type = 'telegram';

-- InsertWebhookDestination creates a healthy outgoing webhook Destination with its request, its proxy and the
-- password of the proxy, and its first Signing secret (C-15.FR-1, FR-5).
-- name: InsertWebhookDestination :one
INSERT INTO destinations (
    org_id, public_id, type, name, webhook_mode, webhook_events_config, webhook_template_config, proxy,
    proxy_password_ciphertext,
    proxy_password_key_id, proxy_password_updated_at, signing_secret_ciphertext, signing_secret_key_id,
    signing_secret_updated_at, mentions, limiter_limit, limiter_per_seconds, health, created_at, updated_at
)
VALUES (
    @org_id, @public_id, 'webhook', @name, @webhook_mode, sqlc.narg('webhook_events_config')::jsonb,
    sqlc.narg('webhook_template_config')::jsonb, @proxy,
    sqlc.narg('proxy_password_ciphertext')::bytea, sqlc.narg('proxy_password_key_id')::text,
    sqlc.narg('proxy_password_updated_at')::timestamptz, @signing_secret_ciphertext, @signing_secret_key_id,
    @now::timestamptz, @mentions, @limiter_limit, @limiter_per_seconds, 'healthy', @now::timestamptz,
    @now::timestamptz
)
RETURNING id;

-- UpdateWebhookDestination replaces the configured fields of an outgoing webhook Destination; the password of its
-- proxy changes only when @password_given, to the value given or to none. Its Signing secrets, Secrets and health are
-- left alone.
-- name: UpdateWebhookDestination :exec
UPDATE destinations
SET name = @name, webhook_mode = @webhook_mode, webhook_events_config = sqlc.narg('webhook_events_config')::jsonb,
    webhook_template_config = sqlc.narg('webhook_template_config')::jsonb, proxy = @proxy,
    proxy_password_ciphertext = CASE WHEN @password_given::boolean THEN sqlc.narg('proxy_password_ciphertext')::bytea
                                     ELSE proxy_password_ciphertext END,
    proxy_password_key_id     = CASE WHEN @password_given::boolean THEN sqlc.narg('proxy_password_key_id')::text
                                     ELSE proxy_password_key_id END,
    proxy_password_updated_at = CASE WHEN @password_given::boolean THEN @now::timestamptz
                                     ELSE proxy_password_updated_at END,
    mentions = @mentions, limiter_limit = @limiter_limit, limiter_per_seconds = @limiter_per_seconds,
    updated_at = @now::timestamptz, version = version + 1
WHERE org_id = @org_id AND id = @id AND type = 'webhook';
