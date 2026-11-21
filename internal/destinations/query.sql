-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- Destinations (C-11.FR-18): reading every type with its health and Routes. Secrets are read only as their status.
-- The L1 Mention settings in destinations.mentions are neither read nor written after 0043, except that a creation
-- stores the L1 default, nobody, for the replicas of 0.1 of a rolling update (C-28.FR-5); the Mention entries are
-- internal/mentions'.

-- ListDestinations reads a page of the Destinations that are not deleted, in id order after a cursor, filtered by
-- type, health, the public_id of a Route that is not deleted, and the public_id of the Team that owns them or a User
-- in the Team that owns them.
-- name: ListDestinations :many
SELECT d.id, d.public_id, d.type, d.name, c.public_id AS connection_public_id, d.mattermost_team_id,
       d.mattermost_channel_id, d.mattermost_team_name, d.mattermost_channel_name, d.telegram_channel_id,
       d.telegram_channel_chat_id, d.telegram_discussion_chat_id, d.telegram_channel_title, d.telegram_discussion_group_title,
       d.bridge_channel_id, d.bridge_channel_name, d.slack_channel_id, d.slack_channel_name, d.webhook_mode,
       d.webhook_events_config, d.webhook_template_config, d.proxy, (d.proxy_password_ciphertext IS NOT NULL)::boolean AS proxy_password_set,
       d.proxy_password_updated_at, (d.signing_secret_ciphertext IS NOT NULL)::boolean AS signing_secret_set,
       d.signing_secret_updated_at, d.previous_signing_secret_since, d.limiter_limit,
       d.limiter_per_seconds, d.health, d.broken_since, d.broken_reason, d.template_error_since, d.template_error,
       d.created_at, d.version, d.owning_team_id, t.public_id AS owning_team_public_id, t.name AS owning_team_name,
       t.slug AS owning_team_slug, (t.deleted_at IS NOT NULL)::boolean AS owning_team_deleted, d.buttons,
       o.buttons AS organization_buttons, d.managed_by
FROM destinations d
JOIN organizations o ON o.id = d.org_id
LEFT JOIN connections c ON c.org_id = d.org_id AND c.id = d.connection_id
LEFT JOIN teams t ON t.org_id = d.org_id AND t.id = d.owning_team_id
WHERE d.org_id = @org_id AND d.deleted_at IS NULL
  AND (sqlc.narg('type')::text IS NULL OR d.type = sqlc.narg('type')::text)
  AND (sqlc.narg('health')::text IS NULL OR d.health = sqlc.narg('health')::text)
  AND (sqlc.narg('route')::text IS NULL
       OR EXISTS (SELECT 1
                  FROM route_destinations rd
                  JOIN routes r ON r.org_id = rd.org_id AND r.id = rd.route_id
                  WHERE rd.org_id = d.org_id AND rd.destination_id = d.id AND r.public_id = sqlc.narg('route')::text
                    AND r.deleted_at IS NULL))
  AND (sqlc.narg('team')::text IS NULL OR t.public_id = sqlc.narg('team')::text)
  AND (sqlc.narg('team_user')::bigint IS NULL
       OR EXISTS (SELECT 1
                  FROM team_users tu
                  WHERE tu.org_id = d.org_id AND tu.team_id = d.owning_team_id
                    AND tu.user_id = sqlc.narg('team_user')::bigint))
  AND (sqlc.narg('after_id')::bigint IS NULL OR d.id > sqlc.narg('after_id')::bigint)
ORDER BY d.id
LIMIT @page_size;

-- GetDestination reads a Destination that is not deleted.
-- name: GetDestination :one
SELECT d.id, d.public_id, d.type, d.name, c.public_id AS connection_public_id, d.mattermost_team_id,
       d.mattermost_channel_id, d.mattermost_team_name, d.mattermost_channel_name, d.telegram_channel_id,
       d.telegram_channel_chat_id, d.telegram_discussion_chat_id, d.telegram_channel_title, d.telegram_discussion_group_title,
       d.bridge_channel_id, d.bridge_channel_name, d.slack_channel_id, d.slack_channel_name, d.webhook_mode,
       d.webhook_events_config, d.webhook_template_config, d.proxy, (d.proxy_password_ciphertext IS NOT NULL)::boolean AS proxy_password_set,
       d.proxy_password_updated_at, (d.signing_secret_ciphertext IS NOT NULL)::boolean AS signing_secret_set,
       d.signing_secret_updated_at, d.previous_signing_secret_since, d.limiter_limit,
       d.limiter_per_seconds, d.health, d.broken_since, d.broken_reason, d.template_error_since, d.template_error,
       d.created_at, d.version, d.owning_team_id, t.public_id AS owning_team_public_id, t.name AS owning_team_name,
       t.slug AS owning_team_slug, (t.deleted_at IS NOT NULL)::boolean AS owning_team_deleted, d.buttons,
       o.buttons AS organization_buttons, d.managed_by
FROM destinations d
JOIN organizations o ON o.id = d.org_id
LEFT JOIN connections c ON c.org_id = d.org_id AND c.id = d.connection_id
LEFT JOIN teams t ON t.org_id = d.org_id AND t.id = d.owning_team_id
WHERE d.org_id = @org_id AND d.public_id = @public_id AND d.deleted_at IS NULL;

-- ListDestinationRoutes lists the Routes that are not deleted of the Destinations, by name.
-- name: ListDestinationRoutes :many
SELECT rd.destination_id, r.public_id, r.name
FROM route_destinations rd
JOIN routes r ON r.org_id = rd.org_id AND r.id = rd.route_id
WHERE rd.org_id = @org_id AND rd.destination_id = ANY(@destination_ids::bigint[]) AND r.deleted_at IS NULL
ORDER BY r.name, r.id;

-- ListDestinationTeams lists the Teams that are not deleted that name the Destinations as Destinations of the Team
-- (C-26.FR-1), by name.
-- name: ListDestinationTeams :many
SELECT td.destination_id, t.public_id, t.name, t.slug
FROM team_destinations td
JOIN teams t ON t.org_id = td.org_id AND t.id = td.team_id
WHERE td.org_id = @org_id AND td.destination_id = ANY(@destination_ids::bigint[]) AND t.deleted_at IS NULL
ORDER BY t.name, t.id;

-- ListRouteDestinationRefs lists the Destinations that are not deleted of the Routes, with their health, by name.
-- name: ListRouteDestinationRefs :many
SELECT rd.route_id, d.public_id, d.name, d.type, d.health, d.broken_since, d.broken_reason
FROM route_destinations rd
JOIN destinations d ON d.org_id = rd.org_id AND d.id = rd.destination_id
WHERE rd.org_id = @org_id AND rd.route_id = ANY(@route_ids::bigint[]) AND d.deleted_at IS NULL
ORDER BY d.name, d.id;

-- ListDestinationInfo lists the Destinations that are not deleted with their type, for muster_destination_info and
-- the series of the counters that start at zero.
-- name: ListDestinationInfo :many
SELECT public_id, name, type
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
    mattermost_channel_name, mentions, limiter_limit, limiter_per_seconds, buttons, managed_by, health, created_at,
    updated_at
)
VALUES (
    @org_id, @public_id, 'mattermost', @name, @connection_id, @team_id, @channel_id, @team_name, @channel_name,
    @mentions, @limiter_limit, @limiter_per_seconds, sqlc.narg('buttons')::jsonb, sqlc.narg('managed_by')::text,
    'healthy', @now::timestamptz, @now::timestamptz
)
RETURNING id;

-- UpdateMattermostDestination replaces the configured fields of a Mattermost Destination and the team and channel
-- names its Destination check read; its health is left alone.
-- name: UpdateMattermostDestination :exec
UPDATE destinations
SET name = @name, connection_id = @connection_id, mattermost_team_id = @team_id,
    mattermost_channel_id = @channel_id, mattermost_team_name = @team_name, mattermost_channel_name = @channel_name,
    limiter_limit = @limiter_limit, limiter_per_seconds = @limiter_per_seconds,
    buttons = sqlc.narg('buttons')::jsonb, managed_by = sqlc.narg('managed_by')::text,
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
    limiter_per_seconds, buttons, managed_by, health, created_at, updated_at
)
VALUES (
    @org_id, @public_id, 'telegram', @name, @connection_id, @channel_id, @channel_chat_id, @discussion_chat_id,
    @channel_title, @discussion_group_title, @mentions, @limiter_limit, @limiter_per_seconds,
    sqlc.narg('buttons')::jsonb, sqlc.narg('managed_by')::text, 'healthy', @now::timestamptz, @now::timestamptz
)
RETURNING id;

-- UpdateTelegramDestination replaces the configured fields of a Telegram Destination and what its Destination check
-- found; its health is left alone.
-- name: UpdateTelegramDestination :exec
UPDATE destinations
SET name = @name, connection_id = @connection_id, telegram_channel_id = @channel_id,
    telegram_channel_chat_id = @channel_chat_id, telegram_discussion_chat_id = @discussion_chat_id,
    telegram_channel_title = @channel_title, telegram_discussion_group_title = @discussion_group_title,
    limiter_limit = @limiter_limit, limiter_per_seconds = @limiter_per_seconds,
    buttons = sqlc.narg('buttons')::jsonb, managed_by = sqlc.narg('managed_by')::text,
    updated_at = @now::timestamptz, version = version + 1
WHERE org_id = @org_id AND id = @id AND type = 'telegram';

-- ListTelegramGroupPeople lists the active Users who are not Viewers — whom a Schedule never makes On-call — in a
-- Team that is not deleted and names the Destination (C-26.FR-1), with a Telegram Account link — the Identity space
-- telegram, which every Telegram Connection shares — and the Telegram user id of the link, by name: the people whose
-- membership of the discussion group the Destination check reads (C-27.FR-8).
-- name: ListTelegramGroupPeople :many
SELECT u.id, u.public_id, u.name, u.login, al.external_id
FROM users u
JOIN account_links al ON al.org_id = u.org_id AND al.user_id = u.id AND al.identity_space = 'telegram'
WHERE u.org_id = @org_id AND u.status = 'active' AND u.role <> 'viewer'
  AND EXISTS (SELECT 1
              FROM team_destinations td
              JOIN teams t ON t.org_id = td.org_id AND t.id = td.team_id AND t.deleted_at IS NULL
              JOIN team_users tu ON tu.org_id = td.org_id AND tu.team_id = td.team_id
              WHERE td.org_id = @org_id AND td.destination_id = @destination_id AND tu.user_id = u.id)
ORDER BY lower(u.name), u.id;

-- ListChainPagedTelegramPeople lists the active Users who are not Viewers, with a Telegram Account link — the
-- Identity space telegram, which every Telegram Connection shares, so the Destination's Connection among them — and
-- the Telegram user id of the link, whom a chain Paging in the Destination can Page through a Schedule (C-27.FR-8,
-- C-32.FR-8): those in a Rotation layer of a Schedule, or taking an Override of it that ends after @now, the business
-- clock's now, that a page_on_call step names in a chain that a Route or a Team that is not deleted and names the
-- Destination names, by name; a call of the On-call through a Notification gateway writes nothing in a Thread
-- (C-32.FR-9). Beside ListTelegramGroupPeople, the people whose membership of the discussion group the Destination
-- check reads.
-- name: ListChainPagedTelegramPeople :many
SELECT u.id, u.public_id, u.name, u.login, al.external_id
FROM users u
JOIN account_links al ON al.org_id = u.org_id AND al.user_id = u.id AND al.identity_space = 'telegram'
WHERE u.org_id = @org_id AND u.status = 'active' AND u.role <> 'viewer'
  AND EXISTS (SELECT 1
              FROM escalation_steps st
              WHERE st.org_id = @org_id AND st.kind = 'page_on_call'
                AND (EXISTS (SELECT 1
                             FROM route_escalation_chains rc
                             JOIN routes r ON r.org_id = rc.org_id AND r.id = rc.route_id AND r.deleted_at IS NULL
                             JOIN route_destinations rd ON rd.org_id = rc.org_id AND rd.route_id = rc.route_id
                             WHERE rc.org_id = @org_id AND rc.chain_id = st.chain_id
                               AND rd.destination_id = @destination_id)
                     OR EXISTS (SELECT 1
                                FROM team_escalation_chains tc
                                JOIN teams t ON t.org_id = tc.org_id AND t.id = tc.team_id AND t.deleted_at IS NULL
                                JOIN team_destinations td ON td.org_id = tc.org_id AND td.team_id = tc.team_id
                                WHERE tc.org_id = @org_id AND tc.chain_id = st.chain_id
                                  AND td.destination_id = @destination_id))
                AND (EXISTS (SELECT 1
                             FROM rotation_layer_users lu
                             JOIN rotation_layers l ON l.org_id = lu.org_id AND l.id = lu.rotation_layer_id
                             WHERE lu.org_id = @org_id AND l.schedule_id = st.schedule_id AND lu.user_id = u.id)
                     OR EXISTS (SELECT 1
                                FROM schedule_overrides o
                                WHERE o.org_id = @org_id AND o.schedule_id = st.schedule_id AND o.user_id = u.id
                                  AND o.ends_at > @now::timestamptz)))
ORDER BY lower(u.name), u.id;

-- SetTelegramAbsences records what the Destination check of a Telegram Destination that is not deleted saw of its
-- discussion group (C-27.FR-8): the Users it saw outside the group, and those of the Users stored as outside it whom
-- it could not tell — not asked, or not answered definitively — so that a check that could not confirm a person keeps
-- the absence a check saw before, while a definitive "in the group" ends it; and when, on the business clock. It writes
-- only while the stored group is the one the check read, so that a slow check does not overwrite a save that moved the
-- Destination to another group. It is not a change of the configuration: no version, no updated_at.
-- name: SetTelegramAbsences :exec
UPDATE destinations
SET telegram_absent_user_ids = ARRAY(
        SELECT a.user_id
        FROM (SELECT unnest(@absent_user_ids::bigint[]) AS user_id
              UNION
              (SELECT unnest(coalesce(telegram_absent_user_ids, '{}'::bigint[]))
               INTERSECT
               SELECT unnest(@unknown_user_ids::bigint[]))) AS a
        ORDER BY a.user_id),
    telegram_members_checked_at = @checked_at::timestamptz
WHERE org_id = @org_id AND id = @id AND type = 'telegram' AND deleted_at IS NULL
  AND telegram_discussion_chat_id = @group_id;

-- LockBridgeConnection takes the Bridge Connection that is not deleted in share mode for the save of one of its
-- Destinations, as LockMattermostConnection does.
-- name: LockBridgeConnection :one
SELECT id
FROM connections
WHERE org_id = @org_id AND id = @id AND type = 'bridge' AND deleted_at IS NULL
FOR SHARE;

-- InsertBridgeDestination creates a healthy Bridge Destination with its channel id and the channel's name its
-- Destination check read (C-22.FR-3).
-- name: InsertBridgeDestination :one
INSERT INTO destinations (
    org_id, public_id, type, name, connection_id, bridge_channel_id, bridge_channel_name, mentions, limiter_limit,
    limiter_per_seconds, buttons, managed_by, health, created_at, updated_at
)
VALUES (
    @org_id, @public_id, 'bridge', @name, @connection_id, @channel_id, @channel_name, @mentions, @limiter_limit,
    @limiter_per_seconds, sqlc.narg('buttons')::jsonb, sqlc.narg('managed_by')::text, 'healthy', @now::timestamptz,
    @now::timestamptz
)
RETURNING id;

-- UpdateBridgeDestination replaces the configured fields of a Bridge Destination and the channel's name its
-- Destination check read; its health is left alone.
-- name: UpdateBridgeDestination :exec
UPDATE destinations
SET name = @name, connection_id = @connection_id, bridge_channel_id = @channel_id,
    bridge_channel_name = @channel_name, limiter_limit = @limiter_limit,
    limiter_per_seconds = @limiter_per_seconds, buttons = sqlc.narg('buttons')::jsonb,
    managed_by = sqlc.narg('managed_by')::text, updated_at = @now::timestamptz, version = version + 1
WHERE org_id = @org_id AND id = @id AND type = 'bridge';

-- LockSlackConnection takes the Slack Connection that is not deleted in share mode for the save of one of its
-- Destinations, as LockMattermostConnection does.
-- name: LockSlackConnection :one
SELECT id
FROM connections
WHERE org_id = @org_id AND id = @id AND type = 'slack' AND deleted_at IS NULL
FOR SHARE;

-- InsertSlackDestination creates a healthy Slack Destination with its channel id as entered and the channel's name its
-- Destination check read (C-24.FR-3).
-- name: InsertSlackDestination :one
INSERT INTO destinations (
    org_id, public_id, type, name, connection_id, slack_channel_id, slack_channel_name, mentions, limiter_limit,
    limiter_per_seconds, buttons, managed_by, health, created_at, updated_at
)
VALUES (
    @org_id, @public_id, 'slack', @name, @connection_id, @channel_id, @channel_name, @mentions, @limiter_limit,
    @limiter_per_seconds, sqlc.narg('buttons')::jsonb, sqlc.narg('managed_by')::text, 'healthy', @now::timestamptz,
    @now::timestamptz
)
RETURNING id;

-- UpdateSlackDestination replaces the configured fields of a Slack Destination and the channel's name its Destination
-- check read; its health is left alone.
-- name: UpdateSlackDestination :exec
UPDATE destinations
SET name = @name, connection_id = @connection_id, slack_channel_id = @channel_id,
    slack_channel_name = @channel_name, limiter_limit = @limiter_limit,
    limiter_per_seconds = @limiter_per_seconds, buttons = sqlc.narg('buttons')::jsonb,
    managed_by = sqlc.narg('managed_by')::text, updated_at = @now::timestamptz, version = version + 1
WHERE org_id = @org_id AND id = @id AND type = 'slack';

-- InsertWebhookDestination creates a healthy outgoing webhook Destination with its request, its proxy and the
-- password of the proxy, and its first Signing secret (C-15.FR-1, FR-5).
-- name: InsertWebhookDestination :one
INSERT INTO destinations (
    org_id, public_id, type, name, webhook_mode, webhook_events_config, webhook_template_config, proxy,
    proxy_password_ciphertext,
    proxy_password_key_id, proxy_password_updated_at, signing_secret_ciphertext, signing_secret_key_id,
    signing_secret_updated_at, mentions, limiter_limit, limiter_per_seconds, managed_by, health, created_at,
    updated_at
)
VALUES (
    @org_id, @public_id, 'webhook', @name, @webhook_mode, sqlc.narg('webhook_events_config')::jsonb,
    sqlc.narg('webhook_template_config')::jsonb, @proxy,
    sqlc.narg('proxy_password_ciphertext')::bytea, sqlc.narg('proxy_password_key_id')::text,
    sqlc.narg('proxy_password_updated_at')::timestamptz, @signing_secret_ciphertext, @signing_secret_key_id,
    @now::timestamptz, @mentions, @limiter_limit, @limiter_per_seconds, sqlc.narg('managed_by')::text, 'healthy',
    @now::timestamptz, @now::timestamptz
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
    limiter_limit = @limiter_limit, limiter_per_seconds = @limiter_per_seconds,
    managed_by = sqlc.narg('managed_by')::text, updated_at = @now::timestamptz, version = version + 1
WHERE org_id = @org_id AND id = @id AND type = 'webhook';

-- LockOwningTeam reads the Team that is not deleted that a Destination is to name as its owner, in share mode, so that
-- the Team's deletion, which locks it for update, either waits for the save or is seen by it.
-- name: LockOwningTeam :one
SELECT id, public_id, name, slug
FROM teams
WHERE org_id = @org_id AND public_id = @public_id AND deleted_at IS NULL
FOR SHARE;

-- SetDestinationOwningTeam writes the owning Team of a Destination in the transaction that saved it, which moved its
-- version.
-- name: SetDestinationOwningTeam :exec
UPDATE destinations
SET owning_team_id = sqlc.narg('owning_team_id')::bigint
WHERE org_id = @org_id AND id = @id;

-- InsertDestinationUpdate requests that a change of a Destination's button set (C-28.FR-9, destination_buttons) or of
-- its Connection (C-12.FR-12, destination_connection) reach its open Root messages: the open_group_updates worker of
-- any replica claims the row.
-- name: InsertDestinationUpdate :exec
INSERT INTO open_group_updates (org_id, kind, destination_id, requested_at)
VALUES (@org_id, @kind, @destination_id, @requested_at::timestamptz);

-- NotifyOpenGroupUpdates wakes the open Alert Group update workers of every replica once the transaction commits.
-- name: NotifyOpenGroupUpdates :exec
SELECT pg_notify(@channel::text, '');

-- ListMentionWarnings computes the warnings of the Destinations about undelivered Mentions (C-28.FR-3), in one query
-- for a page: for each Destination, person — or, through a bridge, group or everyone in the chat, by its Mention — and
-- entry target, among the mention_not_delivered and mention_delivered delivery events kept — within
-- retention.alert_details before @now, as the API hides what is past it even while its partition exists — those after
-- the last mention_delivered, when the latest is mention_not_delivered: the time of the first of them and the reason
-- of the latest, with the person, unless deleted, or the Mention, and user_id 0. delivered_after counts the
-- mention_delivered at or after each event. A template's token, which names no entry, warns about nothing. On
-- delivery_events_mentions_idx.
-- name: ListMentionWarnings :many
WITH kept AS (
    SELECT (sqlc.arg('now')::timestamptz - o.retention_alert_details_days * interval '1 day')::timestamptz AS cutoff
    FROM organizations o
    WHERE o.id = @org_id
), events AS (
    SELECT e.destination_id, e.detail ->> 'user_id' AS user_ref, e.detail -> 'mention' AS mention,
           e.detail -> 'target' AS target, e.detail ->> 'reason' AS reason, e.occurred_at, e.id,
           count(*) FILTER (WHERE e.kind = 'mention_delivered') OVER (
               PARTITION BY e.destination_id, e.detail ->> 'user_id', e.detail -> 'mention', e.detail -> 'target'
               ORDER BY e.occurred_at DESC, e.id DESC) AS delivered_after
    FROM delivery_events e
    JOIN kept k ON e.occurred_at >= k.cutoff
    WHERE e.org_id = @org_id AND e.destination_id = ANY(@destination_ids::bigint[])
      AND e.kind IN ('mention_not_delivered', 'mention_delivered') AND e.detail -> 'target' IS NOT NULL
)
SELECT ev.destination_id, coalesce(u.id, 0)::bigint AS user_id, coalesce(u.public_id, '')::text AS user_public_id,
       coalesce(u.name, '')::text AS user_name, coalesce(u.login, '')::text AS user_login,
       ev.mention::jsonb AS mention, ev.target::jsonb AS target, min(ev.occurred_at)::timestamptz AS since,
       (array_agg(ev.reason ORDER BY ev.occurred_at DESC, ev.id DESC))[1]::text AS reason
FROM events ev
LEFT JOIN users u ON u.org_id = @org_id AND u.id = ev.user_ref::bigint AND u.status <> 'deleted'
WHERE ev.delivered_after = 0 AND (u.id IS NOT NULL OR ev.mention IS NOT NULL)
GROUP BY ev.destination_id, u.id, u.public_id, u.name, u.login, ev.mention, ev.target
ORDER BY ev.destination_id, lower(coalesce(u.name, ev.mention ->> 'name')), u.id;
