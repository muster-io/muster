-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- Mentions (C-12.FR-8, FR-12, C-22.FR-16, C-24.FR-6, C-28.FR-1 to FR-7): the Mention entries of Destinations, what a
-- Connection can mention, the Teams of an Alert Group and of a Storm, the Owners a lifecycle event recorded, the Users a
-- Mention names with the username, the messenger's user id and the display name of their Account link in an identity
-- space, and the names of the user a footer names.

-- ListMentionEntries reads the Mention entries of the Destinations, in their order, each with its id, the User and the
-- Team it names, deleted ones included.
-- name: ListMentionEntries :many
SELECT e.id, e.destination_id, e.position, e.target_kind, coalesce(e.everyone, '')::text AS everyone,
       coalesce(e.group_name, '')::text AS group_name, coalesce(e.slack_usergroup_id, '')::text AS slack_usergroup_id,
       e.events, e.user_id, coalesce(u.public_id, '')::text AS user_public_id, coalesce(u.name, '')::text AS user_name,
       coalesce(u.login, '')::text AS user_login, coalesce(u.status = 'deleted', false)::boolean AS user_deleted,
       e.team_id, coalesce(t.public_id, '')::text AS team_public_id, coalesce(t.name, '')::text AS team_name,
       coalesce(t.slug, '')::text AS team_slug, (t.deleted_at IS NOT NULL)::boolean AS team_deleted
FROM mention_entries e
LEFT JOIN users u ON u.org_id = e.org_id AND u.id = e.user_id
LEFT JOIN teams t ON t.org_id = e.org_id AND t.id = e.team_id
WHERE e.org_id = @org_id AND e.destination_id = ANY(@destination_ids::bigint[])
ORDER BY e.destination_id, e.position;

-- DeleteMentionEntries removes the Mention entries of a Destination, before a save writes them again.
-- name: DeleteMentionEntries :exec
DELETE FROM mention_entries
WHERE org_id = @org_id AND destination_id = @destination_id;

-- InsertMentionEntries adds Mention entries to a Destination at the positions given; an empty text or a zero id is
-- the null of a field the kind does not have.
-- name: InsertMentionEntries :exec
INSERT INTO mention_entries (org_id, destination_id, position, target_kind, everyone, user_id, group_name,
                             slack_usergroup_id, team_id, events, created_at)
SELECT @org_id, @destination_id, e.position, e.kind, nullif(e.everyone, ''), nullif(e.user_id, 0),
       nullif(e.group_name, ''), nullif(e.usergroup_id, ''), nullif(e.team_id, 0),
       string_to_array(e.events, ','), @now::timestamptz
FROM (SELECT unnest(@positions::integer[]) AS position, unnest(@kinds::text[]) AS kind,
             unnest(@everyones::text[]) AS everyone, unnest(@user_ids::bigint[]) AS user_id,
             unnest(@group_names::text[]) AS group_name, unnest(@usergroup_ids::text[]) AS usergroup_id,
             unnest(@team_ids::bigint[]) AS team_id, unnest(@events::text[]) AS events) AS e;

-- ResolveEntryUsers reads the Users among the public_ids, deleted ones included, for the entries of a save.
-- name: ResolveEntryUsers :many
SELECT id, public_id, (status = 'deleted')::boolean AS deleted
FROM users
WHERE org_id = @org_id AND public_id = ANY(@public_ids::text[]);

-- ResolveEntryTeams reads the Teams among the public_ids, deleted ones included, for the entries of a save.
-- name: ResolveEntryTeams :many
SELECT id, public_id, (deleted_at IS NOT NULL)::boolean AS deleted
FROM teams
WHERE org_id = @org_id AND public_id = ANY(@public_ids::text[]);

-- ListLiveTeams reads the Teams that are not deleted, by name, for what the entries from the label mention now.
-- name: ListLiveTeams :many
SELECT id, public_id, name, slug
FROM teams
WHERE org_id = @org_id AND deleted_at IS NULL
ORDER BY lower(name), id;

-- GetMentionConnection reads what a Connection that is not deleted can mention, by its id or its public_id: its
-- type, whether the last check of a Slack Connection found usergroups:read, and from a Bridge's stored info whether it
-- declares group mentions and native mentions — false while none was read — the role messenger and the mentions of
-- everyone it declares.
-- name: GetMentionConnection :one
SELECT c.id, c.public_id, c.type, coalesce(c.slack_usergroups_read, false)::boolean AS usergroups_read,
       coalesce(c.bridge_info->'capabilities'->'group_mentions' = 'true'::jsonb, false)::boolean AS group_mentions,
       coalesce(c.bridge_info->'capabilities'->'native_mentions' = 'true'::jsonb, false)::boolean AS native_mentions,
       (c.bridge_info->'roles' IS NULL OR c.bridge_info->'roles' @> '["messenger"]'::jsonb)::boolean AS messenger,
       (CASE jsonb_typeof(c.bridge_info->'capabilities'->'everyone_mentions')
            WHEN 'array' THEN ARRAY(SELECT jsonb_array_elements_text(c.bridge_info->'capabilities'->'everyone_mentions'))
            ELSE '{}'::text[] END)::text[] AS everyone_mentions
FROM connections c
WHERE c.org_id = @org_id AND c.deleted_at IS NULL
  AND (c.id = sqlc.narg('id')::bigint OR c.public_id = sqlc.narg('public_id')::text);

-- ListAlertGroupTeams reads the Teams of an Alert Group that are not deleted, by name (C-25.FR-6).
-- name: ListAlertGroupTeams :many
SELECT t.id, t.public_id, t.name, t.slug
FROM alert_group_teams agt
JOIN teams t ON t.org_id = agt.org_id AND t.id = agt.team_id
WHERE agt.org_id = @org_id AND agt.alert_group_id = @alert_group_id AND t.deleted_at IS NULL
ORDER BY lower(t.name), t.id;

-- ListStormTeams reads the Teams that are not deleted of the Alert Groups a Storm counts, by name, for the first
-- publication of its Storm summary (C-28.FR-7): those storm_alert_groups records as the Storm starts and joins.
-- name: ListStormTeams :many
SELECT DISTINCT t.id, t.public_id, t.name, t.slug, lower(t.name) AS sort_name
FROM storm_alert_groups sag
JOIN alert_group_teams agt ON agt.org_id = sag.org_id AND agt.alert_group_id = sag.alert_group_id
JOIN teams t ON t.org_id = agt.org_id AND t.id = agt.team_id
WHERE sag.org_id = @org_id AND sag.storm_id = @storm_id AND t.deleted_at IS NULL
ORDER BY sort_name, t.id;

-- GetMentionEventAt reads when a lifecycle event of an Alert Group happened, at which its On-call is taken.
-- name: GetMentionEventAt :one
SELECT at
FROM timeline_entries
WHERE org_id = @org_id AND alert_group_id = @alert_group_id AND event_seq = @event_seq::bigint
LIMIT 1;

-- GetPublicationEventAt reads when the first Publication of an Alert Group in a Destination was requested Loudly: the
-- latest of its creation and its destination_joined in the Destination.
-- name: GetPublicationEventAt :one
SELECT at
FROM timeline_entries
WHERE org_id = @org_id AND alert_group_id = @alert_group_id
  AND (event = 'created' OR (event = 'destination_joined' AND destination_id = @destination_id::bigint))
ORDER BY at DESC
LIMIT 1;

-- GetStormStartedAt reads when a Storm started, the time of the first publication of its Storm summaries.
-- name: GetStormStartedAt :one
SELECT started_at
FROM storms
WHERE org_id = @org_id AND id = @id;

-- LockEntryDestination locks a Destination that is not deleted, for the entry a Team adds to it (C-28.FR-6).
-- name: LockEntryDestination :one
SELECT id, public_id, name, type
FROM destinations
WHERE org_id = @org_id AND id = @id AND deleted_at IS NULL
FOR NO KEY UPDATE;

-- BumpEntryDestination gives a Destination whose entries a Team changed a new version.
-- name: BumpEntryDestination :exec
UPDATE destinations
SET version = version + 1, updated_at = @now::timestamptz
WHERE org_id = @org_id AND id = @id;

-- GetEventOwners reads the Owner after a lifecycle event of an Alert Group and the previous Owner it recorded.
-- name: GetEventOwners :one
SELECT owner_user_id, previous_owner_user_id
FROM timeline_entries
WHERE org_id = @org_id AND alert_group_id = @alert_group_id AND event_seq = @event_seq::bigint
LIMIT 1;

-- ListMentionUsers reads the Users that are not deleted among the ids and the public_ids, with the username, the
-- messenger's user id and the display name of their Account link in the identity space, empty without one, and whether
-- they have one.
-- name: ListMentionUsers :many
SELECT u.id, u.public_id, u.name, u.login, coalesce(al.username, '')::text AS username,
       coalesce(al.external_id, '')::text AS external_id, coalesce(al.display_name, '')::text AS display_name,
       (al.user_id IS NOT NULL)::boolean AS linked
FROM users u
LEFT JOIN account_links al ON al.org_id = u.org_id AND al.user_id = u.id
                          AND al.identity_space = @identity_space::text
WHERE u.org_id = @org_id AND u.status <> 'deleted'
  AND (u.id = ANY(@ids::bigint[]) OR u.public_id = ANY(@public_ids::text[]))
ORDER BY u.id;

-- ListLiveUserPublicIDs lists which of the public_ids name Users that are not deleted.
-- name: ListLiveUserPublicIDs :many
SELECT public_id
FROM users
WHERE org_id = @org_id AND status <> 'deleted' AND public_id = ANY(@public_ids::text[]);

-- ListFooterUsers lists the Account links, by identity space, of the user the footer of an Alert Group names: its Owner
-- while acknowledged, who snoozed it while snoozed, who resolved it while resolved, with the messenger, the username,
-- the messenger's user id and the display name of each, empty without one, and the user's display name; FooterUsers
-- keeps the links its messenger can mention the user by (C-12.FR-12, C-22.FR-16, C-24.FR-6).
-- name: ListFooterUsers :many
SELECT al.identity_space, al.messenger, coalesce(al.username, '')::text AS username,
       coalesce(al.external_id, '')::text AS external_id, coalesce(al.display_name, '')::text AS display_name,
       u.name
FROM alert_groups g
JOIN users u ON u.org_id = g.org_id
            AND u.id = CASE g.status WHEN 'acknowledged' THEN g.owner_user_id
                                     WHEN 'snoozed' THEN g.snoozed_by_user_id
                                     WHEN 'resolved' THEN g.resolved_by_user_id END
JOIN account_links al ON al.org_id = u.org_id AND al.user_id = u.id
WHERE g.org_id = @org_id AND g.id = @id
ORDER BY al.identity_space;
