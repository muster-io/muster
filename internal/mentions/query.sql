-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- Mentions (C-12.FR-8, FR-12, C-22.FR-16): the Mention settings of a Destination, the Owners a lifecycle event
-- recorded, the Users a Mention names with the username, the messenger's user id and the display name of their Account
-- link in an identity space, and the names of the user a footer names.

-- GetDestinationMentions reads the Mention settings of a Destination.
-- name: GetDestinationMentions :one
SELECT mentions
FROM destinations
WHERE org_id = @org_id AND id = @id;

-- GetEventOwners reads the Owner after a lifecycle event of an Alert Group and the previous Owner it recorded.
-- name: GetEventOwners :one
SELECT owner_user_id, previous_owner_user_id
FROM timeline_entries
WHERE org_id = @org_id AND alert_group_id = @alert_group_id AND event_seq = @event_seq::bigint
LIMIT 1;

-- ListMentionUsers reads the Users that are not deleted among the ids and the public_ids, with the username, the
-- messenger's user id and the display name of their Account link in the identity space, empty without one.
-- name: ListMentionUsers :many
SELECT u.id, u.public_id, u.name, u.login, coalesce(al.username, '')::text AS username,
       coalesce(al.external_id, '')::text AS external_id, coalesce(al.display_name, '')::text AS display_name
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

-- ListFooterUsernames lists the names, by identity space, of the user the footer of an Alert Group names: its Owner
-- while acknowledged, who snoozed it while snoozed, who resolved it while resolved. A Mattermost or Telegram link names
-- the user by its username; a Bridge link by the display name the bridge reported, else its username (C-22.FR-16).
-- name: ListFooterUsernames :many
SELECT al.identity_space, n.name::text AS username
FROM alert_groups g
JOIN account_links al ON al.org_id = g.org_id
                     AND al.user_id = CASE g.status WHEN 'acknowledged' THEN g.owner_user_id
                                                    WHEN 'snoozed' THEN g.snoozed_by_user_id
                                                    WHEN 'resolved' THEN g.resolved_by_user_id END
CROSS JOIN LATERAL (SELECT CASE WHEN al.messenger = 'bridge' THEN coalesce(nullif(al.display_name, ''), al.username)
                                ELSE al.username END AS name) n
WHERE g.org_id = @org_id AND g.id = @id AND n.name IS NOT NULL AND n.name <> ''
ORDER BY al.identity_space;
