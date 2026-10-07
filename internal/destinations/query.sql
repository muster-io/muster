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
