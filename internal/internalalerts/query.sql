-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- FindBuiltinIntegration returns the built-in "Muster" Integration, which carries the Internal alerts.
-- name: FindBuiltinIntegration :one
SELECT id
FROM integrations
WHERE org_id = @org_id AND builtin;

-- InsertInternalBody stores the body of a synthetic Stored Snapshot once per Organization and UTC day.
-- name: InsertInternalBody :exec
INSERT INTO snapshot_bodies (org_id, body_sha256, body_day, body)
VALUES (@org_id, @body_sha256, @body_day, @body)
ON CONFLICT DO NOTHING;

-- InsertInternalSnapshot stores a synthetic Stored Snapshot as pending, for processing in order with the other
-- Stored Snapshots of its Integration.
-- name: InsertInternalSnapshot :exec
INSERT INTO stored_snapshots (
    org_id, public_id, integration_id, source, received_at, body_day, body_sha256, size_bytes, content_type, state
)
VALUES (
    @org_id, @public_id, @integration_id, 'internal', @received_at::timestamptz, @body_day, @body_sha256,
    @size_bytes, 'application/json', 'pending'
);

-- NotifyInternalSnapshot wakes the processing workers once the transaction commits.
-- name: NotifyInternalSnapshot :exec
SELECT pg_notify(@channel::text, @payload::text);

-- ListOpenInternalAlerts lists the firing Internal alerts of the built-in Integration whose labels contain
-- @contains, with what raising them again keeps.
-- name: ListOpenInternalAlerts :many
SELECT a.fingerprint, a.labels, a.starts_at
FROM alerts a
JOIN integrations i ON i.org_id = @org_id AND i.id = a.integration_id AND i.builtin
WHERE a.org_id = @org_id AND a.status = 'firing' AND a.labels @> @contains::jsonb
ORDER BY a.id;

-- ListPendingInternalRaises lists the bodies of the synthetic Stored Snapshots of the built-in Integration that wait
-- for processing, oldest first, so that a rename also reaches the raises not processed yet.
-- name: ListPendingInternalRaises :many
SELECT b.body
FROM stored_snapshots s
JOIN integrations i ON i.org_id = @org_id AND i.id = s.integration_id AND i.builtin
JOIN snapshot_bodies b ON b.org_id = @org_id AND b.body_sha256 = s.body_sha256 AND b.body_day = s.body_day
WHERE s.org_id = @org_id AND s.state = 'pending' AND s.source = 'internal'
ORDER BY s.received_at, s.id;
