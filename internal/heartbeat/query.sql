-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- LockHeartbeat locks an Integration that is not deleted for a Heartbeat signal or check and reads its Heartbeat. The
-- lock is a non-key update lock, so that processing, which reads the name with FOR KEY SHARE, never waits for it.
-- name: LockHeartbeat :one
SELECT id, public_id, name, static_labels, builtin, heartbeat_enabled, heartbeat_timeout_seconds, heartbeat_state,
       heartbeat_last_signal_at, heartbeat_lost_since
FROM integrations
WHERE org_id = @org_id AND id = @id AND deleted_at IS NULL
FOR NO KEY UPDATE;

-- RecordSignal records a Heartbeat signal: the Heartbeat is live, the time of the last signal is @signal_at and the
-- liveness clock advances by @advance_ms. It never touches version: the Heartbeat state is runtime state.
-- name: RecordSignal :exec
UPDATE integrations
SET heartbeat_state = 'live', heartbeat_last_signal_at = @signal_at::timestamptz, heartbeat_lost_since = NULL,
    liveness_clock_ms = liveness_clock_ms + @advance_ms::bigint
WHERE org_id = @org_id AND id = @id;

-- MarkLost makes a live Heartbeat lost since @lost_since, the time of its last signal.
-- name: MarkLost :exec
UPDATE integrations
SET heartbeat_state = 'lost', heartbeat_lost_since = @lost_since::timestamptz
WHERE org_id = @org_id AND id = @id AND heartbeat_state = 'live';

-- ListOverdue lists the Integrations whose Heartbeat is live and whose last signal, or @measured_from when it is later,
-- is older than their timeout at @now.
-- name: ListOverdue :many
SELECT id
FROM integrations
WHERE org_id = @org_id AND deleted_at IS NULL AND heartbeat_state = 'live'
  AND greatest(heartbeat_last_signal_at, sqlc.narg('measured_from')::timestamptz)
      + make_interval(secs => heartbeat_timeout_seconds) < @now::timestamptz
ORDER BY id;

-- ListHeartbeats lists the Integrations that are not deleted and have their Heartbeat on, with its state, for
-- muster_heartbeat_lost.
-- name: ListHeartbeats :many
SELECT public_id, heartbeat_state
FROM integrations
WHERE org_id = @org_id AND deleted_at IS NULL AND heartbeat_enabled
ORDER BY id;

-- GetLeaderStart reads when the current Leader started leading and whether it recorded a downtime on taking over:
-- the takeover that records a downtime ends it at the moment it starts leading. Both are installation-wide.
-- name: GetLeaderStart :one
SELECT r.leader_since,
       coalesce((SELECT max(d.ended_at) FROM downtime_periods d) >= r.leader_since, false)::boolean AS after_downtime
FROM runtime_state r;
