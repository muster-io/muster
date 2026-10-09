-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- Mentions (C-12.FR-8, FR-12): the Mention settings of a Destination, the Owners a lifecycle event recorded, the Users
-- a Mention names with the username of their Account link in an identity space, and the usernames of the user a
-- footer names.

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

-- ListMentionUsers reads the Users that are not deleted among the ids and the public_ids, with the username and the
-- messenger's user id of their Account link in the identity space, empty without one.
-- name: ListMentionUsers :many
SELECT u.id, u.public_id, u.name, u.login, coalesce(al.username, '')::text AS username,
       coalesce(al.external_id, '')::text AS external_id
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

-- ListFooterUsernames lists the usernames, by identity space, of the user the footer of an Alert Group names: its
-- Owner while acknowledged, who snoozed it while snoozed, who resolved it while resolved.
-- name: ListFooterUsernames :many
SELECT al.identity_space, al.username::text AS username
FROM alert_groups g
JOIN account_links al ON al.org_id = g.org_id
                     AND al.user_id = CASE g.status WHEN 'acknowledged' THEN g.owner_user_id
                                                    WHEN 'snoozed' THEN g.snoozed_by_user_id
                                                    WHEN 'resolved' THEN g.resolved_by_user_id END
WHERE g.org_id = @org_id AND g.id = @id AND al.username IS NOT NULL AND al.username <> ''
ORDER BY al.identity_space;
