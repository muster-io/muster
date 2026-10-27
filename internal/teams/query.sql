-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- Teams (C-25): the Teams with their Working hours, the people in them, and the owners their deletion clears.

-- GetOrganizationWorkingHours reads the Organization's Working hours, which a Team without its own follows.
-- name: GetOrganizationWorkingHours :one
SELECT working_days, workday_start, workday_end, time_zone
FROM organizations
WHERE id = @org_id;

-- ListTeams reads a page of the Teams that are not deleted by name, after a cursor, with the number of people in each;
-- @q matches the name or the slug, ignoring case.
-- name: ListTeams :many
SELECT t.id, t.public_id, t.name, t.slug, t.description, t.own_working_hours, t.working_days, t.working_start,
       t.working_end, t.working_time_zone, t.created_at, t.version,
       (SELECT count(*) FROM team_users u WHERE u.org_id = t.org_id AND u.team_id = t.id)::bigint AS user_count
FROM teams t
WHERE t.org_id = @org_id AND t.deleted_at IS NULL
  AND (sqlc.narg('q')::text IS NULL OR strpos(lower(t.name), lower(sqlc.narg('q')::text)) > 0
       OR strpos(t.slug, lower(sqlc.narg('q')::text)) > 0)
  AND (sqlc.narg('after_id')::bigint IS NULL
       OR (lower(t.name), t.id) > (sqlc.narg('after_name')::text, sqlc.narg('after_id')::bigint))
ORDER BY lower(t.name), t.id
LIMIT @page_size;

-- GetTeam reads a Team that is not deleted with the number of people in it.
-- name: GetTeam :one
SELECT t.id, t.public_id, t.name, t.slug, t.description, t.own_working_hours, t.working_days, t.working_start,
       t.working_end, t.working_time_zone, t.created_at, t.version,
       (SELECT count(*) FROM team_users u WHERE u.org_id = t.org_id AND u.team_id = t.id)::bigint AS user_count
FROM teams t
WHERE t.org_id = @org_id AND t.public_id = @public_id AND t.deleted_at IS NULL;

-- LockTeam locks a Team that is not deleted for a change; the lock leaves the key alone, so that saving an object it
-- owns, whose foreign key takes a key share lock, waits only for the deletion, which the owner's share lock does.
-- name: LockTeam :one
SELECT id
FROM teams
WHERE org_id = @org_id AND public_id = @public_id AND deleted_at IS NULL
FOR NO KEY UPDATE;

-- InsertTeam creates a Team.
-- name: InsertTeam :one
INSERT INTO teams (
    org_id, public_id, name, slug, description, own_working_hours, working_days, working_start, working_end,
    working_time_zone, created_at, updated_at
)
VALUES (
    @org_id, @public_id, @name, @slug, @description, @own_working_hours, sqlc.narg('working_days')::text[],
    sqlc.narg('working_start')::text, sqlc.narg('working_end')::text, sqlc.narg('working_time_zone')::text, @now,
    @now
)
RETURNING id;

-- UpdateTeam replaces the configured fields of a Team and bumps its version.
-- name: UpdateTeam :exec
UPDATE teams
SET name = @name, slug = @slug, description = @description, own_working_hours = @own_working_hours,
    working_days = sqlc.narg('working_days')::text[], working_start = sqlc.narg('working_start')::text,
    working_end = sqlc.narg('working_end')::text, working_time_zone = sqlc.narg('working_time_zone')::text,
    updated_at = @now, version = version + 1
WHERE org_id = @org_id AND id = @id;

-- DeleteTeam soft-deletes a Team and removes its Working hours: the row stays for the names that point at it, and its
-- name and slug are free at once.
-- name: DeleteTeam :exec
UPDATE teams
SET deleted_at = @now, updated_at = @now, own_working_hours = false, working_days = NULL, working_start = NULL,
    working_end = NULL, working_time_zone = NULL, version = version + 1
WHERE org_id = @org_id AND id = @id;

-- DeleteTeamUsers removes every person from a Team.
-- name: DeleteTeamUsers :exec
DELETE FROM team_users
WHERE org_id = @org_id AND team_id = @team_id;

-- ReleaseOwnedDestinations leaves the Destinations that are not deleted and that the Team owns without an owner, each
-- with a new version, as a save of the Destination gives it.
-- name: ReleaseOwnedDestinations :many
UPDATE destinations
SET owning_team_id = NULL, updated_at = @now, version = version + 1
WHERE org_id = @org_id AND owning_team_id = @team_id AND deleted_at IS NULL
RETURNING public_id, name;

-- ReleaseOwnedRoutes leaves the Routes that are not deleted and that the Team owns without an owner, each with a new
-- version, as a save of the Route gives it.
-- name: ReleaseOwnedRoutes :many
UPDATE routes
SET owning_team_id = NULL, updated_at = @now, version = version + 1
WHERE org_id = @org_id AND owning_team_id = @team_id AND deleted_at IS NULL
RETURNING public_id, name;

-- ListTeamUsers lists the people in a Team, by name, with their Role and status.
-- name: ListTeamUsers :many
SELECT u.id, u.public_id, u.name, u.login, u.role, u.status, tu.by_hand
FROM team_users tu
JOIN users u ON u.org_id = tu.org_id AND u.id = tu.user_id
WHERE tu.org_id = @org_id AND tu.team_id = @team_id
ORDER BY lower(u.name), u.id;

-- ListLinkedMessengers lists the Account links of the Users through Connections that are not deleted, by the name of
-- the Connection.
-- name: ListLinkedMessengers :many
SELECT al.user_id, c.type, c.public_id, c.name
FROM account_links al
JOIN connections c ON c.org_id = al.org_id AND c.id = al.connection_id
WHERE al.org_id = @org_id AND al.user_id = ANY(@user_ids::bigint[]) AND c.deleted_at IS NULL
ORDER BY al.user_id, c.name, c.id;

-- LockTeamUser reads a User that is not deleted to add to a Team, in share mode, so that the User's deletion, which
-- locks the row for update, either waits for the addition or is seen by it.
-- name: LockTeamUser :one
SELECT id, public_id, name, login, role, status
FROM users
WHERE org_id = @org_id AND public_id = @public_id AND status <> 'deleted'
FOR SHARE;

-- GetTeamUser reads one person in a Team.
-- name: GetTeamUser :one
SELECT u.id, u.public_id, u.name, u.login, u.role, u.status, tu.by_hand
FROM team_users tu
JOIN users u ON u.org_id = tu.org_id AND u.id = tu.user_id
WHERE tu.org_id = @org_id AND tu.team_id = @team_id AND u.public_id = @public_id;

-- InsertTeamUser adds a User to a Team by hand, or marks a person who is in it otherwise as added by hand too; it
-- changes nothing, and counts no row, when the User is in it by hand already.
-- name: InsertTeamUser :execrows
INSERT INTO team_users (org_id, team_id, user_id, by_hand, created_at)
VALUES (@org_id, @team_id, @user_id, true, @now)
ON CONFLICT (team_id, user_id) DO UPDATE SET by_hand = true
WHERE NOT team_users.by_hand;

-- DeleteTeamUser takes a User out of a Team.
-- name: DeleteTeamUser :execrows
DELETE FROM team_users
WHERE org_id = @org_id AND team_id = @team_id AND user_id = @user_id;

-- DeleteUserTeams takes a deleted User out of every Team and returns the Teams with the User, as the deletion left
-- their name, and how they were in each.
-- name: DeleteUserTeams :many
WITH removed AS (
    DELETE FROM team_users
    WHERE org_id = @org_id AND user_id = @user_id
    RETURNING team_id, by_hand
)
SELECT t.public_id, t.name, u.public_id AS user_public_id, u.name AS user_name, r.by_hand
FROM teams t
JOIN removed r ON r.team_id = t.id
JOIN users u ON u.org_id = t.org_id AND u.id = @user_id
WHERE t.org_id = @org_id
ORDER BY t.id;

-- ListTeamInfo lists the Teams that are not deleted, for muster_team_info.
-- name: ListTeamInfo :many
SELECT public_id, name
FROM teams
WHERE org_id = @org_id AND deleted_at IS NULL;
