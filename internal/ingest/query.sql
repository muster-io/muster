-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- InsertSnapshotBody stores a body once per Organization and UTC day; the same bytes received again that day are
-- already there.
-- name: InsertSnapshotBody :exec
INSERT INTO snapshot_bodies (org_id, body_sha256, body_day, body)
VALUES (@org_id, @body_sha256, @body_day, @body)
ON CONFLICT DO NOTHING;

-- InsertStoredSnapshot stores a received webhook as pending, for processing.
-- name: InsertStoredSnapshot :exec
INSERT INTO stored_snapshots (
    org_id, public_id, integration_id, source, received_at, body_day, body_sha256, size_bytes, content_type, state
)
VALUES (
    @org_id, @public_id, @integration_id, 'webhook', @received_at::timestamptz, @body_day, @body_sha256, @size_bytes,
    sqlc.narg('content_type'), 'pending'
);

-- NotifySnapshot wakes the processing workers once the transaction commits.
-- name: NotifySnapshot :exec
SELECT pg_notify(@channel::text, @payload::text);

-- GetRetention returns how many days the Organization keeps Stored Snapshots.
-- name: GetRetention :one
SELECT retention_stored_snapshots_days
FROM organizations
WHERE id = @org_id;

-- FindSnapshotIntegration finds an Integration by its public_id, a deleted one included: its Stored Snapshots stay
-- until retention.
-- name: FindSnapshotIntegration :one
SELECT id, public_id, name
FROM integrations
WHERE org_id = @org_id AND public_id = @public_id;

-- ListStoredSnapshots lists the Stored Snapshots of an Integration received since @not_before, newest first, in the
-- time range and the processing states when given, after the cursor (received_at, id) when given.
-- name: ListStoredSnapshots :many
SELECT s.id, s.public_id, s.received_at, s.processed_at, s.size_bytes, s.state, s.processing_error, s.group_key,
       s.alert_count, s.truncated_alerts
FROM stored_snapshots s
WHERE s.org_id = @org_id AND s.integration_id = @integration_id AND s.received_at >= @not_before::timestamptz
  AND (sqlc.narg('from')::timestamptz IS NULL OR s.received_at >= sqlc.narg('from')::timestamptz)
  AND (sqlc.narg('to')::timestamptz IS NULL OR s.received_at < sqlc.narg('to')::timestamptz)
  AND (cardinality(@states::text[]) = 0 OR s.state = ANY(@states::text[]))
  AND (sqlc.narg('after_at')::timestamptz IS NULL
       OR (s.received_at, s.id) < (sqlc.narg('after_at')::timestamptz, sqlc.narg('after_id')::bigint))
ORDER BY s.received_at DESC, s.id DESC
LIMIT @page_size;

-- GetStoredSnapshot reads a Stored Snapshot received since @not_before with its body and its Integration, a deleted
-- one included; @not_before_day, the UTC day of @not_before, keeps the older partitions of the bodies out.
-- name: GetStoredSnapshot :one
SELECT s.id, s.public_id, s.received_at, s.processed_at, s.size_bytes, s.content_type, s.state, s.processing_error,
       s.group_key, s.alert_count, s.truncated_alerts, b.body,
       i.public_id AS integration_public_id, i.name AS integration_name
FROM stored_snapshots s
JOIN snapshot_bodies b ON b.org_id = @org_id AND b.body_sha256 = s.body_sha256 AND b.body_day = s.body_day
    AND b.body_day >= @not_before_day::date
JOIN integrations i ON i.org_id = @org_id AND i.id = s.integration_id
WHERE s.org_id = @org_id AND s.public_id = @public_id AND s.received_at >= @not_before::timestamptz
LIMIT 1;
