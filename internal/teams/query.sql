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
       (SELECT count(*)
        FROM team_users tu
        JOIN users u ON u.org_id = tu.org_id AND u.id = tu.user_id
        WHERE tu.org_id = t.org_id AND tu.team_id = t.id AND u.status <> 'deleted')::bigint AS user_count,
       lower(t.name)::text AS sort_name
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
       (SELECT count(*)
        FROM team_users tu
        JOIN users u ON u.org_id = tu.org_id AND u.id = tu.user_id
        WHERE tu.org_id = t.org_id AND tu.team_id = t.id AND u.status <> 'deleted')::bigint AS user_count
FROM teams t
WHERE t.org_id = @org_id AND t.public_id = @public_id AND t.deleted_at IS NULL;

-- LockTeam locks a Team that is not deleted for a change, FOR NO KEY UPDATE: a Route or a Destination saved with the
-- Team as its owner reads the Team FOR SHARE first, before its own row, so it waits for this lock and then finds a
-- deleted Team gone; the key share lock of their foreign key does not conflict with it. A deletion takes this lock,
-- then the Destinations and then the Routes the Team owns, each set in id order.
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

-- ListTeamSchedules lists the Schedules of a Team by name, which refuse its deletion (P-64).
-- name: ListTeamSchedules :many
SELECT public_id, name
FROM schedules
WHERE org_id = @org_id AND team_id = @team_id
ORDER BY lower(name), id;

-- DeleteTeamUsers removes every person from a Team.
-- name: DeleteTeamUsers :exec
DELETE FROM team_users
WHERE org_id = @org_id AND team_id = @team_id;

-- ReleaseOwnedDestinations leaves the Destinations that are not deleted and that the Team owns without an owner, each
-- with a new version, as a save of the Destination gives it, locking them in id order.
-- name: ReleaseOwnedDestinations :many
UPDATE destinations d
SET owning_team_id = NULL, updated_at = @now, version = d.version + 1
FROM (SELECT x.id
      FROM destinations x
      WHERE x.org_id = @org_id AND x.owning_team_id = @team_id AND x.deleted_at IS NULL
      ORDER BY x.id
      FOR NO KEY UPDATE OF x) AS locked
WHERE d.org_id = @org_id AND d.id = locked.id
RETURNING d.public_id, d.name;

-- ReleaseOwnedRoutes leaves the Routes that are not deleted and that the Team owns without an owner, each with a new
-- version, as a save of the Route gives it, locking them in id order.
-- name: ReleaseOwnedRoutes :many
UPDATE routes r
SET owning_team_id = NULL, updated_at = @now, version = r.version + 1
FROM (SELECT x.id
      FROM routes x
      WHERE x.org_id = @org_id AND x.owning_team_id = @team_id AND x.deleted_at IS NULL
      ORDER BY x.id
      FOR NO KEY UPDATE OF x) AS locked
WHERE r.org_id = @org_id AND r.id = locked.id
RETURNING r.public_id, r.name;

-- ListTeamUsers lists the people in a Team, by name, with their Role and status; a deleted User, whom the deletion
-- took out of every Team, is never listed.
-- name: ListTeamUsers :many
SELECT u.id, u.public_id, u.name, u.login, u.role, u.status, tu.by_hand
FROM team_users tu
JOIN users u ON u.org_id = tu.org_id AND u.id = tu.user_id
WHERE tu.org_id = @org_id AND tu.team_id = @team_id AND u.status <> 'deleted'
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

-- ListTeamMatchers lists the Team matchers of Teams, each Team's in their order.
-- name: ListTeamMatchers :many
SELECT team_id, label, op, value
FROM team_matchers
WHERE org_id = @org_id AND team_id = ANY(@team_ids::bigint[])
ORDER BY team_id, position;

-- DeleteTeamMatchers removes the Team matchers of a Team, which a save replaces as a whole.
-- name: DeleteTeamMatchers :exec
DELETE FROM team_matchers
WHERE org_id = @org_id AND team_id = @team_id;

-- InsertTeamMatchers writes the Team matchers of a Team in their order.
-- name: InsertTeamMatchers :exec
INSERT INTO team_matchers (org_id, team_id, label, op, value, position)
SELECT @org_id, @team_id, m.label, m.op, m.value, m.position
FROM (SELECT unnest(@labels::text[]) AS label, unnest(@ops::text[]) AS op, unnest(@matcher_values::text[]) AS value,
             generate_subscripts(@labels::text[], 1) - 1 AS position) AS m;

-- LockTeamsVersion locks the Organization's row FOR NO KEY UPDATE before a change of a Team, ahead of the Team's own
-- row: the lock order of a Route or a Destination saved with an owning Team, which takes the row of the Organization to
-- bump the order of its list first. The foreign-key checks of rows that name the Organization take a key share lock,
-- which this one lets through.
-- name: LockTeamsVersion :one
SELECT teams_version
FROM organizations
WHERE id = @org_id
FOR NO KEY UPDATE;

-- BumpTeamsVersion raises organizations.teams_version, the stamp of the cached classification of every replica, after
-- a change of a Team or of the Team label.
-- name: BumpTeamsVersion :exec
UPDATE organizations
SET teams_version = teams_version + 1
WHERE id = @org_id;

-- ListClassifiedTeams lists the Teams that are not deleted, which the Team label and the Team matchers name.
-- name: ListClassifiedTeams :many
SELECT id, public_id, name, slug
FROM teams
WHERE org_id = @org_id AND deleted_at IS NULL
ORDER BY id;

-- ListClassifiedMatchers lists the Team matchers of the Teams that are not deleted, each Team's in their order.
-- name: ListClassifiedMatchers :many
SELECT m.team_id, m.label, m.op, m.value
FROM team_matchers m
JOIN teams t ON t.org_id = m.org_id AND t.id = m.team_id
WHERE m.org_id = @org_id AND t.deleted_at IS NULL
ORDER BY m.team_id, m.position;
