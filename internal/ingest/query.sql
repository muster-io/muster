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

-- ListPendingIntegrations finds the Integrations with pending Stored Snapshots received since @horizon; the partial
-- index holds only pending rows.
-- name: ListPendingIntegrations :many
SELECT DISTINCT integration_id
FROM stored_snapshots
WHERE org_id = @org_id AND state = 'pending' AND received_at >= @horizon::timestamptz;

-- EnsureIngestClaims creates the claim rows of Integrations that have none yet.
-- name: EnsureIngestClaims :exec
INSERT INTO ingest_claims (integration_id, org_id)
SELECT i.id, i.org_id
FROM integrations i
WHERE i.org_id = @org_id AND i.id = ANY(@integration_ids::bigint[])
ON CONFLICT (integration_id) DO NOTHING;

-- ClaimIntegrations leases at most @batch_size of the Integrations whose lease is free or ran out at @now (real
-- clock); a claim row another transaction holds is skipped.
-- name: ClaimIntegrations :many
UPDATE ingest_claims c
SET lease_owner = @owner, lease_until = @lease_until
FROM integrations i
WHERE c.org_id = @org_id AND i.org_id = @org_id AND i.id = c.integration_id AND c.integration_id IN (
    SELECT f.integration_id
    FROM ingest_claims f
    WHERE f.org_id = @org_id AND f.integration_id = ANY(@integration_ids::bigint[])
      AND (f.lease_until IS NULL OR f.lease_until <= @now::timestamptz)
    LIMIT @batch_size
    FOR UPDATE SKIP LOCKED)
RETURNING c.integration_id, i.public_id;

-- RenewIngestLease extends the lease this replica holds and locks the claim row until the Snapshot's transaction
-- ends, so that no other replica processes the Integration meanwhile; it returns what processing needs of the
-- Integration. No row means the lease went to another replica.
-- name: RenewIngestLease :one
UPDATE ingest_claims c
SET lease_until = @lease_until
FROM integrations i
WHERE c.org_id = @org_id AND c.integration_id = @integration_id AND c.lease_owner = @owner
  AND i.org_id = @org_id AND i.id = c.integration_id
RETURNING i.public_id, i.static_labels, i.duplicate_window_seconds, i.liveness_clock_ms;

-- ReleaseIngestClaim frees the lease this replica holds.
-- name: ReleaseIngestClaim :exec
UPDATE ingest_claims
SET lease_owner = NULL, lease_until = NULL
WHERE org_id = @org_id AND integration_id = @integration_id AND lease_owner = @owner;

-- NextPendingSnapshot reads the oldest pending Stored Snapshot of an Integration with its body.
-- name: NextPendingSnapshot :one
SELECT s.id, s.public_id, s.received_at, b.body
FROM stored_snapshots s
JOIN snapshot_bodies b ON b.org_id = @org_id AND b.body_sha256 = s.body_sha256 AND b.body_day = s.body_day
WHERE s.org_id = @org_id AND s.integration_id = @integration_id AND s.state = 'pending'
  AND s.received_at >= @horizon::timestamptz
ORDER BY s.received_at, s.id
LIMIT 1;

-- FinishSnapshot marks a pending Stored Snapshot processed or failed, with what processing read of its payload.
-- name: FinishSnapshot :execrows
UPDATE stored_snapshots
SET state = @state, processed_at = @processed_at, processing_error = sqlc.narg('processing_error'),
    group_key = sqlc.narg('group_key'), alert_count = sqlc.narg('alert_count'),
    truncated_alerts = sqlc.narg('truncated_alerts')
WHERE org_id = @org_id AND id = @id AND received_at = @received_at::timestamptz AND state = 'pending';

-- CountSnapshot counts a Stored Snapshot on its Integration; processing is serialized per Integration, so the
-- ingestion path never updates the Integration row.
-- name: CountSnapshot :exec
UPDATE integrations
SET snapshot_count = snapshot_count + 1, last_snapshot_at = greatest(last_snapshot_at, @received_at::timestamptz)
WHERE org_id = @org_id AND id = @integration_id;

-- UpsertAlertmanagerRoute finds or creates the Alertmanager route of a groupKey and refreshes when it was seen.
-- name: UpsertAlertmanagerRoute :one
INSERT INTO alertmanager_routes (org_id, integration_id, route_path, route_path_sha256, first_seen_at, last_seen_at)
VALUES (@org_id, @integration_id, @route_path, @route_path_sha256, @seen_at, @seen_at)
ON CONFLICT (integration_id, route_path_sha256) DO UPDATE
SET last_seen_at = greatest(alertmanager_routes.last_seen_at, excluded.last_seen_at)
RETURNING id, learned_repeat_interval_ms, repeat_observations, recent_repeat_gaps_ms;

-- UpsertAlertmanagerGroup finds or creates the Alertmanager group of a groupKey and locks its row.
-- name: UpsertAlertmanagerGroup :one
INSERT INTO alertmanager_groups (
    org_id, integration_id, alertmanager_route_id, group_key, group_key_sha256, first_seen_at, last_snapshot_at,
    last_snapshot_clock_ms
)
VALUES (
    @org_id, @integration_id, @alertmanager_route_id, @group_key, @group_key_sha256, @seen_at, @seen_at,
    @clock_ms
)
ON CONFLICT (integration_id, group_key_sha256) DO UPDATE
SET alertmanager_route_id = alertmanager_groups.alertmanager_route_id
RETURNING id, window_seq, window_started_at, window_truncated, truncated, truncated_since, last_repeat_at,
    last_content_sha256, last_content_at, last_snapshot_at, last_snapshot_clock_ms;

-- UpdateAlertmanagerGroup writes the duplicate window, truncation and repeat bookkeeping of a groupKey.
-- name: UpdateAlertmanagerGroup :exec
UPDATE alertmanager_groups
SET last_snapshot_at = @last_snapshot_at, last_snapshot_clock_ms = @last_snapshot_clock_ms, window_seq = @window_seq,
    window_started_at = sqlc.narg('window_started_at'), window_truncated = @window_truncated,
    truncated = @truncated, truncated_since = sqlc.narg('truncated_since'),
    last_repeat_at = sqlc.narg('last_repeat_at'), last_content_sha256 = sqlc.narg('last_content_sha256'),
    last_content_at = sqlc.narg('last_content_at')
WHERE org_id = @org_id AND id = @id;

-- UpdateRepeatInterval stores a learned repeat interval and the ring of gaps it is the median of.
-- name: UpdateRepeatInterval :exec
UPDATE alertmanager_routes
SET learned_repeat_interval_ms = @learned_repeat_interval_ms, repeat_observations = @repeat_observations,
    recent_repeat_gaps_ms = @recent_repeat_gaps_ms::bigint[]
WHERE org_id = @org_id AND id = @id;

-- ListSnapshotAlerts reads the Alerts a Snapshot touches: those it lists by fingerprint and those with an active
-- presence in its groupKey.
-- name: ListSnapshotAlerts :many
SELECT a.id, a.fingerprint, a.labels, a.annotations, a.generator_url, a.static_label_conflicts, a.status,
       a.starts_at, a.ends_at, a.episode, a.fired_at, a.first_seen_at, a.last_seen_at, a.resolved_at,
       a.resolve_reason, a.resolve_reason_text
FROM alerts a
WHERE a.org_id = @org_id AND a.integration_id = @integration_id
  AND (a.fingerprint = ANY(@fingerprints::text[])
       OR a.id IN (SELECT p.alert_id
                   FROM alert_presences p
                   WHERE p.org_id = @org_id AND p.alertmanager_group_id = @alertmanager_group_id
                     AND p.state IN ('listed', 'missed')));

-- ListActivePresences reads the active presences of a groupKey, and whether each Alert has an active presence in
-- another groupKey.
-- name: ListActivePresences :many
SELECT p.alert_id, p.state, p.last_listed_window, p.missed_since,
       EXISTS (SELECT 1
               FROM alert_presences o
               WHERE o.org_id = @org_id AND o.alert_id = p.alert_id
                 AND o.alertmanager_group_id <> p.alertmanager_group_id
                 AND o.state IN ('listed', 'missed'))::boolean AS active_elsewhere
FROM alert_presences p
WHERE p.org_id = @org_id AND p.alertmanager_group_id = @alertmanager_group_id AND p.state IN ('listed', 'missed');

-- InsertAlerts creates the rows of newly firing fingerprints, given as a JSON array of rows.
-- name: InsertAlerts :many
INSERT INTO alerts (
    org_id, integration_id, fingerprint, labels, annotations, generator_url, static_label_conflicts, status,
    starts_at, episode, fired_at, first_seen_at, last_seen_at, updated_at
)
SELECT @org_id, @integration_id, u.fingerprint, u.labels, u.annotations, u.generator_url,
       u.static_label_conflicts, 'firing', u.starts_at, 1, @seen_at, @seen_at, @seen_at, @updated_at
FROM jsonb_to_recordset(@rows::jsonb) AS u(fingerprint text, labels jsonb, annotations jsonb, generator_url text,
                                           static_label_conflicts text[], starts_at timestamptz)
RETURNING id, fingerprint;

-- UpdateAlerts writes the new state of existing Alerts, given as a JSON array of rows.
-- name: UpdateAlerts :exec
UPDATE alerts a
SET labels = u.labels, annotations = u.annotations, generator_url = u.generator_url,
    static_label_conflicts = u.static_label_conflicts, status = u.status, starts_at = u.starts_at,
    ends_at = u.ends_at, episode = u.episode, fired_at = u.fired_at, last_seen_at = u.last_seen_at,
    resolved_at = u.resolved_at, resolve_reason = u.resolve_reason, resolve_reason_text = u.resolve_reason_text,
    updated_at = @updated_at
FROM jsonb_to_recordset(@rows::jsonb) AS u(id bigint, labels jsonb, annotations jsonb, generator_url text,
                                           static_label_conflicts text[], status text, starts_at timestamptz,
                                           ends_at timestamptz, episode bigint, fired_at timestamptz,
                                           last_seen_at timestamptz, resolved_at timestamptz,
                                           resolve_reason text, resolve_reason_text text)
WHERE a.org_id = @org_id AND a.id = u.id;

-- UpsertListedPresences records the Alerts a Snapshot lists as listed in its groupKey and current window.
-- name: UpsertListedPresences :exec
INSERT INTO alert_presences (
    alert_id, alertmanager_group_id, org_id, integration_id, state, first_listed_at, last_seen_at,
    last_seen_clock_ms, last_listed_window
)
SELECT u.alert_id, @alertmanager_group_id, @org_id, @integration_id, 'listed', @seen_at, @seen_at, @clock_ms,
       @window_seq
FROM unnest(@alert_ids::bigint[]) AS u(alert_id)
ON CONFLICT (alert_id, alertmanager_group_id) DO UPDATE
SET state = 'listed',
    first_listed_at = CASE WHEN alert_presences.state IN ('gone', 'stale') THEN excluded.first_listed_at
                           ELSE alert_presences.first_listed_at END,
    last_seen_at = excluded.last_seen_at, last_seen_clock_ms = excluded.last_seen_clock_ms,
    last_listed_window = excluded.last_listed_window, missed_since = NULL;

-- MarkPresencesMissed records listed presences of a groupKey as missed since the start of its current window.
-- name: MarkPresencesMissed :exec
UPDATE alert_presences
SET state = 'missed', missed_since = @missed_since
WHERE org_id = @org_id AND alertmanager_group_id = @alertmanager_group_id AND alert_id = ANY(@alert_ids::bigint[])
  AND state = 'listed';

-- EndPresences ends active presences: in one groupKey when it is given (Gone there), or in every groupKey for Alerts
-- that resolved.
-- name: EndPresences :exec
UPDATE alert_presences
SET state = 'gone', missed_since = NULL
WHERE org_id = @org_id AND alert_id = ANY(@alert_ids::bigint[]) AND state IN ('listed', 'missed')
  AND (sqlc.narg('alertmanager_group_id')::bigint IS NULL
       OR alertmanager_group_id = sqlc.narg('alertmanager_group_id')::bigint);

-- CountPendingSnapshots is the backlog of an Organization: its pending Stored Snapshots.
-- name: CountPendingSnapshots :one
SELECT count(*)
FROM stored_snapshots
WHERE org_id = @org_id AND state = 'pending';

-- FindViewIntegration finds an Integration that is not deleted, for its Alerts view, with the Organization's
-- retention.alert_details.
-- name: FindViewIntegration :one
SELECT i.id, o.retention_alert_details_days
FROM integrations i
JOIN organizations o ON o.id = i.org_id
WHERE i.org_id = @org_id AND i.public_id = @public_id AND i.deleted_at IS NULL;

-- ListViewAlertsByLastSeen is a batch of the Alerts view, newest last seen first, after the cursor when given:
-- firing Alerts and those resolved since @resolved_since, in the state when given, whose labels contain @contains.
-- name: ListViewAlertsByLastSeen :many
SELECT a.id, a.fingerprint, a.labels, a.annotations, a.static_label_conflicts, a.status, a.starts_at, a.last_seen_at,
       a.fired_at, a.resolved_at, a.resolve_reason, a.resolve_reason_text
FROM alerts a
WHERE a.org_id = @org_id AND a.integration_id = @integration_id
  AND (a.status = 'firing' OR a.resolved_at >= @resolved_since::timestamptz)
  AND (sqlc.narg('status')::text IS NULL OR a.status = sqlc.narg('status')::text)
  AND a.labels @> @contains::jsonb
  AND (sqlc.narg('after_at')::timestamptz IS NULL
       OR (a.last_seen_at, a.id) < (sqlc.narg('after_at')::timestamptz, sqlc.narg('after_id')::bigint))
ORDER BY a.last_seen_at DESC, a.id DESC
LIMIT @batch_size;

-- ListViewAlertsByLastSeenAsc is ListViewAlertsByLastSeen, oldest last seen first.
-- name: ListViewAlertsByLastSeenAsc :many
SELECT a.id, a.fingerprint, a.labels, a.annotations, a.static_label_conflicts, a.status, a.starts_at, a.last_seen_at,
       a.fired_at, a.resolved_at, a.resolve_reason, a.resolve_reason_text
FROM alerts a
WHERE a.org_id = @org_id AND a.integration_id = @integration_id
  AND (a.status = 'firing' OR a.resolved_at >= @resolved_since::timestamptz)
  AND (sqlc.narg('status')::text IS NULL OR a.status = sqlc.narg('status')::text)
  AND a.labels @> @contains::jsonb
  AND (sqlc.narg('after_at')::timestamptz IS NULL
       OR (a.last_seen_at, a.id) > (sqlc.narg('after_at')::timestamptz, sqlc.narg('after_id')::bigint))
ORDER BY a.last_seen_at, a.id
LIMIT @batch_size;

-- ListViewAlertsByStartsAt is ListViewAlertsByLastSeen sorted by startsAt, newest first.
-- name: ListViewAlertsByStartsAt :many
SELECT a.id, a.fingerprint, a.labels, a.annotations, a.static_label_conflicts, a.status, a.starts_at, a.last_seen_at,
       a.fired_at, a.resolved_at, a.resolve_reason, a.resolve_reason_text
FROM alerts a
WHERE a.org_id = @org_id AND a.integration_id = @integration_id
  AND (a.status = 'firing' OR a.resolved_at >= @resolved_since::timestamptz)
  AND (sqlc.narg('status')::text IS NULL OR a.status = sqlc.narg('status')::text)
  AND a.labels @> @contains::jsonb
  AND (sqlc.narg('after_at')::timestamptz IS NULL
       OR (a.starts_at, a.id) < (sqlc.narg('after_at')::timestamptz, sqlc.narg('after_id')::bigint))
ORDER BY a.starts_at DESC, a.id DESC
LIMIT @batch_size;

-- ListViewAlertsByStartsAtAsc is ListViewAlertsByLastSeen sorted by startsAt, oldest first.
-- name: ListViewAlertsByStartsAtAsc :many
SELECT a.id, a.fingerprint, a.labels, a.annotations, a.static_label_conflicts, a.status, a.starts_at, a.last_seen_at,
       a.fired_at, a.resolved_at, a.resolve_reason, a.resolve_reason_text
FROM alerts a
WHERE a.org_id = @org_id AND a.integration_id = @integration_id
  AND (a.status = 'firing' OR a.resolved_at >= @resolved_since::timestamptz)
  AND (sqlc.narg('status')::text IS NULL OR a.status = sqlc.narg('status')::text)
  AND a.labels @> @contains::jsonb
  AND (sqlc.narg('after_at')::timestamptz IS NULL
       OR (a.starts_at, a.id) > (sqlc.narg('after_at')::timestamptz, sqlc.narg('after_id')::bigint))
ORDER BY a.starts_at, a.id
LIMIT @batch_size;

-- ListViewGroupKeys lists the groupKeys that listed each Alert during its current or last firing.
-- name: ListViewGroupKeys :many
SELECT p.alert_id, g.group_key
FROM alert_presences p
JOIN alerts a ON a.org_id = @org_id AND a.id = p.alert_id
JOIN alertmanager_groups g ON g.org_id = @org_id AND g.id = p.alertmanager_group_id
WHERE p.org_id = @org_id AND p.alert_id = ANY(@alert_ids::bigint[]) AND p.last_seen_at >= a.fired_at
ORDER BY p.alert_id, g.group_key;
