-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- Notification rules (C-31): the rules of the Users in the states call, no_call and custom, the Organization's default
-- rules that the Users in the state organization follow, and the Notification gateways a call names.

-- LockRulesUser locks the User whose rules change, so that the rules of one User change one at a time and never after
-- the User's deletion, which locks the row first.
-- name: LockRulesUser :one
SELECT id, public_id, name, status
FROM users
WHERE org_id = @org_id AND id = @id
FOR NO KEY UPDATE;

-- GetRulesUser reads a User an Admin names, deleted ones included.
-- name: GetRulesUser :one
SELECT id, public_id, name, status
FROM users
WHERE org_id = @org_id AND public_id = @public_id;

-- GetRulesUserByID reads the signed-in User.
-- name: GetRulesUserByID :one
SELECT id, public_id, name, status
FROM users
WHERE org_id = @org_id AND id = @id;

-- GetOrganizationRules reads the Organization's default rules (organization.notification_rules) with the version of
-- the organization resource, from which the ETag of the state organization comes.
-- name: GetOrganizationRules :one
SELECT notification_rules, version
FROM organizations
WHERE id = @org_id;

-- GetRules reads the rules of a User in the state call, no_call or custom; no row is the state organization.
-- name: GetRules :one
SELECT state, gateway_connection_id, normal, important, version
FROM notification_rules
WHERE org_id = @org_id AND user_id = @user_id;

-- ListRuleGateways lists every Bridge Connection, deleted ones included, which a call action may name: whether it is
-- a Notification gateway now — not deleted, its info declaring the role gateway — and whether the User user_id has a
-- confirmed contact there.
-- name: ListRuleGateways :many
SELECT c.id, c.public_id, c.name,
       (c.deleted_at IS NULL AND coalesce(c.bridge_info -> 'roles' @> '["gateway"]'::jsonb, false))::boolean AS live,
       EXISTS (SELECT 1
               FROM gateway_contacts g
               WHERE g.org_id = c.org_id AND g.connection_id = c.id AND g.user_id = @user_id
                 AND g.confirmed_at IS NOT NULL)::boolean AS contact
FROM connections c
WHERE c.org_id = @org_id AND c.type = 'bridge'
ORDER BY lower(c.name), c.id;

-- UpsertRules writes the rules of a User with a new version from the sequence.
-- name: UpsertRules :one
INSERT INTO notification_rules (org_id, user_id, state, gateway_connection_id, normal, important, updated_at)
VALUES (@org_id, @user_id, @state, sqlc.narg('gateway_connection_id'), @normal, @important, @now::timestamptz)
ON CONFLICT (user_id) DO UPDATE
SET state                 = excluded.state,
    gateway_connection_id = excluded.gateway_connection_id,
    normal                = excluded.normal,
    important             = excluded.important,
    updated_at            = excluded.updated_at,
    version               = nextval('notification_rules_version_seq')
WHERE notification_rules.org_id = @org_id
RETURNING version;

-- DeleteRules deletes the rules of a User, who then follows the Organization's rules; and those of a deleted User.
-- name: DeleteRules :execrows
DELETE FROM notification_rules
WHERE org_id = @org_id AND user_id = @user_id;
