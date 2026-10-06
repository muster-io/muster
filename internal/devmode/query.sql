-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- GetDevClockOffset reads how far the development clock runs ahead of the system time; no row means not at all.
-- name: GetDevClockOffset :one
SELECT dev_clock_offset_seconds
FROM runtime_state
WHERE singleton;

-- AdvanceDevClock moves the development clock @seconds further ahead, creating the singleton row when missing, and
-- returns how far ahead it now runs.
-- name: AdvanceDevClock :one
INSERT INTO runtime_state (singleton, dev_clock_offset_seconds, updated_at)
VALUES (true, @seconds, @updated_at)
ON CONFLICT (singleton) DO UPDATE
SET dev_clock_offset_seconds = runtime_state.dev_clock_offset_seconds + excluded.dev_clock_offset_seconds,
    updated_at = excluded.updated_at
RETURNING dev_clock_offset_seconds;

-- NotifyDevClock tells every replica that the development clock moved, once the transaction commits.
-- name: NotifyDevClock :exec
SELECT pg_notify(@channel::text, @payload::text);
