-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- The claim of the timer rows (schema.md §5): due on the business clock, leased on the real clock.

-- ClaimDueTimers leases the due timers of the kinds this replica fires whose lease is free or ran out, earliest
-- first; another claimer skips the rows this one locked.
-- name: ClaimDueTimers :many
UPDATE timers t
SET lease_owner = @owner::text, lease_until = @lease_until::timestamptz, attempts = t.attempts + 1
FROM (SELECT d.id
      FROM timers d
      WHERE d.org_id = @org_id AND d.deadline <= @due AND d.kind = ANY(@kinds::text[])
        AND (d.lease_until IS NULL OR d.lease_until <= @now::timestamptz)
      ORDER BY d.deadline, d.id
      LIMIT @lim
      FOR UPDATE SKIP LOCKED) AS due
WHERE t.org_id = @org_id AND t.id = due.id
RETURNING t.id, t.alert_group_id, t.storm_id, t.kind, t.deadline, t.attempts;

-- HoldsTimer reports whether the lease of a claimed timer is still this replica's at now: a timer that was
-- rescheduled meanwhile lost its lease.
-- name: HoldsTimer :one
SELECT EXISTS (
    SELECT 1
    FROM timers
    WHERE org_id = @org_id AND id = @id AND lease_owner = @owner::text AND lease_until > @now::timestamptz
)::boolean AS held;

-- DeleteFiredTimer removes a timer that fired, unless it was rescheduled since it was claimed.
-- name: DeleteFiredTimer :exec
DELETE FROM timers
WHERE org_id = @org_id AND id = @id AND lease_owner = @owner::text AND deadline = @deadline;

-- NextTimerDeadline is when the next timer of the kinds this replica fires can be claimed: the earliest deadline of a
-- row whose lease is free at the real time now (business clock), and the earliest end of a lease still held (real
-- clock); each the zero time 0001-01-01 when there is none.
-- name: NextTimerDeadline :one
SELECT coalesce(min(deadline) FILTER (WHERE lease_until IS NULL OR lease_until <= @now::timestamptz),
                '0001-01-01 00:00:00+00')::timestamptz AS free_deadline,
       coalesce(min(lease_until) FILTER (WHERE lease_until > @now::timestamptz),
                '0001-01-01 00:00:00+00')::timestamptz AS lease_end
FROM timers
WHERE org_id = @org_id AND kind = ANY(@kinds::text[]);
