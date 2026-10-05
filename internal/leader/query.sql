-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- The installation-level platform state of the Leader: runtime_state, downtime_periods and the replica records have
-- no org_id. Times are business times, except the replica records, which are on the real clock.

-- EnsureRuntimeState writes the singleton row on a new database; it changes nothing when the row exists.
-- name: EnsureRuntimeState :exec
INSERT INTO runtime_state (updated_at)
VALUES (@updated_at)
ON CONFLICT (singleton) DO NOTHING;

-- LockRuntimeState reads the row and locks it until the transaction ends, so that two Leaders taking over at once
-- record one downtime.
-- name: LockRuntimeState :one
SELECT leader_replica_id, leader_since, alive_at, recovery_until
FROM runtime_state
FOR UPDATE;

-- name: GetRuntimeState :one
SELECT leader_replica_id, leader_since, alive_at, recovery_until
FROM runtime_state;

-- LatestOtherReplicaRefresh is the newest key-record refresh of any replica but the given one that started before
-- started_before, on the real clock: a replica that started later did not run across the gap.
-- name: LatestOtherReplicaRefresh :one
SELECT refreshed_at
FROM replicas
WHERE replica_id <> @replica_id
  AND started_at <= @started_before
ORDER BY refreshed_at DESC
LIMIT 1;

-- name: RecordDowntime :exec
INSERT INTO downtime_periods (started_at, ended_at, recorded_at)
VALUES (@started_at, @ended_at, @recorded_at);

-- name: LatestDowntimeEnd :one
SELECT ended_at
FROM downtime_periods
ORDER BY ended_at DESC
LIMIT 1;

-- TakeOver records a new Leader; a null recovery_until keeps the recovery window that is open, if any.
-- name: TakeOver :exec
UPDATE runtime_state
SET leader_replica_id = @leader_replica_id::text,
    leader_since      = @now::timestamptz,
    alive_at          = GREATEST(alive_at, @now::timestamptz),
    recovery_until    = COALESCE(sqlc.narg(recovery_until)::timestamptz, recovery_until),
    updated_at        = @now::timestamptz;

-- MarkAlive refreshes the alive mark; a Leader that finds another replica's id in the row writes its own.
-- name: MarkAlive :execrows
UPDATE runtime_state
SET leader_since      = CASE WHEN leader_replica_id IS DISTINCT FROM @leader_replica_id::text THEN @now ELSE leader_since END,
    leader_replica_id = @leader_replica_id::text,
    alive_at          = GREATEST(alive_at, @now),
    updated_at        = @now;

-- name: ListOrganizationIDs :many
SELECT id
FROM organizations
ORDER BY id;

-- LongestLearnedRepeatInterval is the longest repeat interval learned from the Organization's Alertmanager routes,
-- in milliseconds; 0 when none is learned.
-- name: LongestLearnedRepeatInterval :one
SELECT COALESCE(max(learned_repeat_interval_ms), 0)::bigint AS longest_ms
FROM alertmanager_routes
WHERE org_id = @org_id;
