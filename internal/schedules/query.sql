-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- Schedules (C-27): the Schedules of the Teams, their Rotation layers with the people in turn, and the Overrides.

-- GetOrganizationTimeZone reads the Organization's time zone, which a Schedule created without one takes (P-77).
-- name: GetOrganizationTimeZone :one
SELECT time_zone
FROM organizations
WHERE id = @org_id;

-- LockScheduleTeam locks a Team that is not deleted for a change of its Schedules, FOR NO KEY UPDATE: the changes of
-- one Team's Schedules and the Team's deletion, which takes the same lock, run one after the other, so that the first
-- Schedule is the main one and a Team with Schedules is never deleted.
-- name: LockScheduleTeam :one
SELECT id, public_id, name, slug
FROM teams
WHERE org_id = @org_id AND public_id = @public_id AND deleted_at IS NULL
FOR NO KEY UPDATE;

-- LockScheduleTeamByID is LockScheduleTeam for the Team of a stored Schedule.
-- name: LockScheduleTeamByID :one
SELECT id, public_id, name, slug
FROM teams
WHERE org_id = @org_id AND id = @id
FOR NO KEY UPDATE;

-- CountTeamSchedules counts the Schedules of a Team.
-- name: CountTeamSchedules :one
SELECT count(*)::bigint
FROM schedules
WHERE org_id = @org_id AND team_id = @team_id;

-- ListSchedules reads a page of the Schedules by name, after a cursor, with their Team: of the Team @team, or of the
-- Teams of the User @member_id ("My Teams"), when one is set; @q matches the name, ignoring case.
-- name: ListSchedules :many
SELECT s.id, s.public_id, s.name, s.time_zone, s.main, s.version, s.created_at, t.public_id AS team_public_id,
       t.name AS team_name, t.slug AS team_slug, (t.deleted_at IS NOT NULL)::boolean AS team_deleted,
       lower(s.name)::text AS sort_name
FROM schedules s
JOIN teams t ON t.org_id = s.org_id AND t.id = s.team_id
WHERE s.org_id = @org_id
  AND (sqlc.narg('team')::text IS NULL OR t.public_id = sqlc.narg('team')::text)
  AND (sqlc.narg('member_id')::bigint IS NULL OR EXISTS (
        SELECT 1
        FROM team_users tu
        WHERE tu.org_id = @org_id AND tu.team_id = s.team_id AND tu.user_id = sqlc.narg('member_id')::bigint))
  AND (sqlc.narg('q')::text IS NULL OR strpos(lower(s.name), lower(sqlc.narg('q')::text)) > 0)
  AND (sqlc.narg('after_id')::bigint IS NULL
       OR (lower(s.name), s.id) > (sqlc.narg('after_name')::text, sqlc.narg('after_id')::bigint))
ORDER BY lower(s.name), s.id
LIMIT @page_size;

-- GetSchedule reads a Schedule with its Team.
-- name: GetSchedule :one
SELECT s.id, s.public_id, s.name, s.time_zone, s.main, s.version, s.created_at, s.team_id,
       t.public_id AS team_public_id, t.name AS team_name, t.slug AS team_slug,
       (t.deleted_at IS NOT NULL)::boolean AS team_deleted
FROM schedules s
JOIN teams t ON t.org_id = s.org_id AND t.id = s.team_id
WHERE s.org_id = @org_id AND s.public_id = @public_id;

-- FindScheduleTeam reads the Team of a Schedule, to lock it before the Schedule.
-- name: FindScheduleTeam :one
SELECT team_id
FROM schedules
WHERE org_id = @org_id AND public_id = @public_id;

-- LockSchedule locks a Schedule for a change of it or of its Overrides.
-- name: LockSchedule :one
SELECT id
FROM schedules
WHERE org_id = @org_id AND public_id = @public_id
FOR UPDATE;

-- InsertSchedule creates a Schedule.
-- name: InsertSchedule :one
INSERT INTO schedules (org_id, public_id, team_id, name, time_zone, main, created_at, updated_at)
VALUES (@org_id, @public_id, @team_id, @name, @time_zone, @main, @now, @now)
RETURNING id;

-- UpdateSchedule replaces the configured fields of a Schedule and bumps its version.
-- name: UpdateSchedule :exec
UPDATE schedules
SET name = @name, time_zone = @time_zone, main = @main, updated_at = @now, version = version + 1
WHERE org_id = @org_id AND id = @id;

-- ClearMainSchedule takes the mark main off the Team's main Schedule other than @keep_id, with a new version, and
-- returns it.
-- name: ClearMainSchedule :many
UPDATE schedules
SET main = false, updated_at = @now, version = version + 1
WHERE org_id = @org_id AND team_id = @team_id AND main AND id <> @keep_id
RETURNING public_id, name;

-- DeleteSchedule deletes a Schedule with its Rotation layers and Overrides.
-- name: DeleteSchedule :exec
DELETE FROM schedules
WHERE org_id = @org_id AND id = @id;

-- ListRotationLayers reads the Rotation layers of Schedules, from the lowest.
-- name: ListRotationLayers :many
SELECT id, public_id, schedule_id, position, name, shift_length, shift_seconds, handover_time, handover_day,
       start_date, limit_days, limit_start, limit_end
FROM rotation_layers
WHERE org_id = @org_id AND schedule_id = ANY(@schedule_ids::bigint[])
ORDER BY schedule_id, position;

-- ListRotationLayerUsers reads the people of Rotation layers in turn, with their Role and status.
-- name: ListRotationLayerUsers :many
SELECT ru.rotation_layer_id, ru.position, u.id, u.public_id, u.name, u.login, u.role, u.status
FROM rotation_layer_users ru
JOIN users u ON u.org_id = ru.org_id AND u.id = ru.user_id
WHERE ru.org_id = @org_id AND ru.rotation_layer_id = ANY(@rotation_layer_ids::bigint[])
ORDER BY ru.rotation_layer_id, ru.position;

-- InsertRotationLayer adds a Rotation layer to a Schedule.
-- name: InsertRotationLayer :one
INSERT INTO rotation_layers (
    org_id, public_id, schedule_id, position, name, shift_length, shift_seconds, handover_time, handover_day,
    start_date, limit_days, limit_start, limit_end
)
VALUES (
    @org_id, @public_id, @schedule_id, @position, @name, @shift_length, sqlc.narg('shift_seconds')::integer,
    @handover_time, sqlc.narg('handover_day')::text, @start_date, sqlc.narg('limit_days')::text[],
    sqlc.narg('limit_start')::text, sqlc.narg('limit_end')::text
)
RETURNING id;

-- UpdateRotationLayer replaces a Rotation layer that a save keeps.
-- name: UpdateRotationLayer :exec
UPDATE rotation_layers
SET position = @position, name = @name, shift_length = @shift_length,
    shift_seconds = sqlc.narg('shift_seconds')::integer, handover_time = @handover_time,
    handover_day = sqlc.narg('handover_day')::text, start_date = @start_date,
    limit_days = sqlc.narg('limit_days')::text[], limit_start = sqlc.narg('limit_start')::text,
    limit_end = sqlc.narg('limit_end')::text
WHERE org_id = @org_id AND id = @id;

-- DeleteRotationLayers deletes the Rotation layers of a Schedule that a save does not keep, with their people.
-- name: DeleteRotationLayers :exec
DELETE FROM rotation_layers
WHERE org_id = @org_id AND schedule_id = @schedule_id AND NOT (id = ANY(@keep::bigint[]));

-- DeleteRotationLayerUsers removes the people of a Rotation layer, which a save writes again.
-- name: DeleteRotationLayerUsers :exec
DELETE FROM rotation_layer_users
WHERE org_id = @org_id AND rotation_layer_id = @rotation_layer_id;

-- InsertRotationLayerUsers writes the people of a Rotation layer in turn, from position 1.
-- name: InsertRotationLayerUsers :exec
INSERT INTO rotation_layer_users (org_id, rotation_layer_id, position, user_id)
SELECT @org_id, @rotation_layer_id, p.ord::integer, p.user_id
FROM unnest(@user_ids::bigint[]) WITH ORDINALITY AS p (user_id, ord);

-- LookupUsers reads the Users that are not deleted among public ids, with their Role and status.
-- name: LookupUsers :many
SELECT id, public_id, name, login, role, status
FROM users
WHERE org_id = @org_id AND public_id = ANY(@public_ids::text[]) AND status <> 'deleted';

-- ListPageable reads Users with whether they are in a Team, for "may be Paged": active, not a Viewer and in the Team.
-- name: ListPageable :many
SELECT u.id, u.public_id, u.name, u.login, u.role, u.status,
       EXISTS (SELECT 1
               FROM team_users tu
               WHERE tu.org_id = u.org_id AND tu.team_id = @team_id AND tu.user_id = u.id)::boolean AS in_team
FROM users u
WHERE u.org_id = @org_id AND u.id = ANY(@user_ids::bigint[]);

-- IsTeamUser reports whether a User is in a Team.
-- name: IsTeamUser :one
SELECT EXISTS (SELECT 1
               FROM team_users
               WHERE org_id = @org_id AND team_id = @team_id AND user_id = @user_id)::boolean;

-- ListScheduleOverrides reads a page of the Overrides of a Schedule that end after @period_start and, when
-- @period_end is set, start before it, by start, after a cursor.
-- name: ListScheduleOverrides :many
SELECT o.id, o.public_id, o.starts_at, o.ends_at, o.version, o.created_at,
       u.public_id AS user_public_id, u.name AS user_name, u.login AS user_login, u.status AS user_status,
       r.public_id AS replaces_public_id, r.name AS replaces_name, r.login AS replaces_login,
       r.status AS replaces_status,
       cu.public_id AS by_user_public_id, cu.name AS by_user_name, cu.login AS by_user_login,
       cu.status AS by_user_status,
       sa.public_id AS by_service_account_public_id, sa.name AS by_service_account_name,
       sa.status AS by_service_account_status
FROM schedule_overrides o
JOIN users u ON u.org_id = o.org_id AND u.id = o.user_id
LEFT JOIN users r ON r.org_id = o.org_id AND r.id = o.replaces_user_id
LEFT JOIN users cu ON cu.org_id = o.org_id AND cu.id = o.created_by_user_id
LEFT JOIN service_accounts sa ON sa.org_id = o.org_id AND sa.id = o.created_by_service_account_id
WHERE o.org_id = @org_id AND o.schedule_id = @schedule_id AND o.ends_at > @period_start
  AND (sqlc.narg('period_end')::timestamptz IS NULL OR o.starts_at < sqlc.narg('period_end')::timestamptz)
  AND (sqlc.narg('after_id')::bigint IS NULL
       OR (o.starts_at, o.id) > (sqlc.narg('after_start')::timestamptz, sqlc.narg('after_id')::bigint))
ORDER BY o.starts_at, o.id
LIMIT @page_size;

-- ListPeriodOverrides reads the Overrides of a Schedule that overlap a period, in the order they were created: by id,
-- which every replica takes from the same sequence, where created_at may skew between replicas (P-65).
-- name: ListPeriodOverrides :many
SELECT o.id, o.public_id, o.user_id, o.starts_at, o.ends_at, o.version, o.created_at,
       u.public_id AS user_public_id, u.name AS user_name, u.login AS user_login, u.status AS user_status,
       r.public_id AS replaces_public_id, r.name AS replaces_name, r.login AS replaces_login,
       r.status AS replaces_status,
       cu.public_id AS by_user_public_id, cu.name AS by_user_name, cu.login AS by_user_login,
       cu.status AS by_user_status,
       sa.public_id AS by_service_account_public_id, sa.name AS by_service_account_name,
       sa.status AS by_service_account_status
FROM schedule_overrides o
JOIN users u ON u.org_id = o.org_id AND u.id = o.user_id
LEFT JOIN users r ON r.org_id = o.org_id AND r.id = o.replaces_user_id
LEFT JOIN users cu ON cu.org_id = o.org_id AND cu.id = o.created_by_user_id
LEFT JOIN service_accounts sa ON sa.org_id = o.org_id AND sa.id = o.created_by_service_account_id
WHERE o.org_id = @org_id AND o.schedule_id = @schedule_id AND o.ends_at > @period_start AND o.starts_at < @period_end
ORDER BY o.id;

-- CountOverlappingOverrides counts the Overrides of a Schedule other than @except_id that overlap a span.
-- name: CountOverlappingOverrides :one
SELECT count(*)::bigint
FROM schedule_overrides
WHERE org_id = @org_id AND schedule_id = @schedule_id AND id <> @except_id AND ends_at > @span_start
  AND starts_at < @span_end;

-- GetScheduleOverride reads an Override of a Schedule.
-- name: GetScheduleOverride :one
SELECT o.id, o.public_id, o.user_id, o.replaces_user_id, o.starts_at, o.ends_at, o.version, o.created_at,
       u.public_id AS user_public_id, u.name AS user_name, u.login AS user_login, u.status AS user_status,
       r.public_id AS replaces_public_id, r.name AS replaces_name, r.login AS replaces_login,
       r.status AS replaces_status,
       cu.public_id AS by_user_public_id, cu.name AS by_user_name, cu.login AS by_user_login,
       cu.status AS by_user_status,
       sa.public_id AS by_service_account_public_id, sa.name AS by_service_account_name,
       sa.status AS by_service_account_status
FROM schedule_overrides o
JOIN users u ON u.org_id = o.org_id AND u.id = o.user_id
LEFT JOIN users r ON r.org_id = o.org_id AND r.id = o.replaces_user_id
LEFT JOIN users cu ON cu.org_id = o.org_id AND cu.id = o.created_by_user_id
LEFT JOIN service_accounts sa ON sa.org_id = o.org_id AND sa.id = o.created_by_service_account_id
WHERE o.org_id = @org_id AND o.schedule_id = @schedule_id AND o.public_id = @public_id;

-- LockScheduleOverride locks an Override of a Schedule for a change.
-- name: LockScheduleOverride :one
SELECT id
FROM schedule_overrides
WHERE org_id = @org_id AND schedule_id = @schedule_id AND public_id = @public_id
FOR UPDATE;

-- InsertScheduleOverride creates an Override.
-- name: InsertScheduleOverride :one
INSERT INTO schedule_overrides (
    org_id, public_id, schedule_id, user_id, replaces_user_id, starts_at, ends_at, created_by_user_id,
    created_by_service_account_id, created_at, updated_at
)
VALUES (
    @org_id, @public_id, @schedule_id, @user_id, sqlc.narg('replaces_user_id')::bigint, @starts_at, @ends_at,
    sqlc.narg('created_by_user_id')::bigint, sqlc.narg('created_by_service_account_id')::bigint, @now, @now
)
RETURNING id;

-- UpdateScheduleOverride replaces the people and the span of an Override and bumps its version.
-- name: UpdateScheduleOverride :exec
UPDATE schedule_overrides
SET user_id = @user_id, replaces_user_id = sqlc.narg('replaces_user_id')::bigint, starts_at = @starts_at,
    ends_at = @ends_at, updated_at = @now, version = version + 1
WHERE org_id = @org_id AND id = @id;

-- DeleteScheduleOverride deletes an Override.
-- name: DeleteScheduleOverride :exec
DELETE FROM schedule_overrides
WHERE org_id = @org_id AND id = @id;

-- ListScheduleInfo lists the Schedules, for muster_schedule_info.
-- name: ListScheduleInfo :many
SELECT public_id, name
FROM schedules
WHERE org_id = @org_id;
