-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- GetRuntimeState reads the Leader and the recovery window of the installation; runtime_state has one row and no
-- org_id.
-- name: GetRuntimeState :one
SELECT leader_replica_id, alive_at, recovery_until
FROM runtime_state;

-- LatestDowntime reads the last recorded downtime of the installation; downtime_periods has no org_id.
-- name: LatestDowntime :one
SELECT started_at, ended_at
FROM downtime_periods
ORDER BY ended_at DESC, id DESC
LIMIT 1;

-- ListDeliveryQueues lists the Destinations that are not deleted with pending deliveries that no Storm holds, Thread
-- replies due at @now or pending outgoing webhook events, and their count. It counts as CountDeliveryQueues of
-- internal/delivery counts muster_delivery_queue; a change of one changes the other.
-- name: ListDeliveryQueues :many
SELECT ds.public_id, ds.name, q.queued
FROM destinations ds
CROSS JOIN LATERAL (
    SELECT ((SELECT count(*)
             FROM deliveries d
             WHERE d.org_id = ds.org_id AND d.destination_id = ds.id AND d.state = 'pending'
               AND d.held_by_storm_id IS NULL)
          + (SELECT count(*)
             FROM thread_replies r
             WHERE r.org_id = ds.org_id AND r.destination_id = ds.id AND r.state IN ('collecting', 'pending')
               AND r.next_attempt_at <= @now::timestamptz)
          + (SELECT count(*)
             FROM webhook_events e
             WHERE e.org_id = ds.org_id AND e.destination_id = ds.id AND e.state = 'pending'))::bigint AS queued
) q
WHERE ds.org_id = @org_id AND ds.deleted_at IS NULL AND q.queued > 0
ORDER BY ds.name, ds.id;

-- ListBrokenDestinations lists the Broken Destinations that are not deleted, with the masked reason.
-- name: ListBrokenDestinations :many
SELECT public_id, name, broken_since::timestamptz AS broken_since, coalesce(broken_reason, '')::text AS reason
FROM destinations
WHERE org_id = @org_id AND deleted_at IS NULL AND health = 'broken'
ORDER BY broken_since, id;

-- ListIntegrationsAttention lists the Integrations that are not deleted and are Heartbeat lost, or have a truncated
-- groupKey, since when.
-- name: ListIntegrationsAttention :many
SELECT a.public_id, a.name, a.issue, a.since
FROM (
    SELECT i.public_id, i.name, 'heartbeat_lost'::text AS issue, i.heartbeat_lost_since AS since
    FROM integrations i
    WHERE i.org_id = @org_id AND i.deleted_at IS NULL AND i.heartbeat_state = 'lost'
    UNION ALL
    SELECT i.public_id, i.name, 'snapshot_truncated'::text, min(g.truncated_since)
    FROM integrations i
    JOIN alertmanager_groups g ON g.org_id = i.org_id AND g.integration_id = i.id AND g.truncated
    WHERE i.org_id = @org_id AND i.deleted_at IS NULL
    GROUP BY i.id, i.public_id, i.name
) a
ORDER BY a.name, a.public_id, a.issue;

-- ListTemplateErrors lists the Routes and the Destinations that are not deleted and whose template keeps failing.
-- name: ListTemplateErrors :many
SELECT t.kind, t.public_id, t.name, t.template, t.since, t.error
FROM (
    SELECT 'route'::text AS kind, r.public_id, r.name,
           coalesce(r.template_error_template, 'root_message')::text AS template,
           r.template_error_since::timestamptz AS since, coalesce(r.template_error, '')::text AS error
    FROM routes r
    WHERE r.org_id = @org_id AND r.deleted_at IS NULL AND r.template_error_since IS NOT NULL
    UNION ALL
    SELECT 'destination'::text, d.public_id, d.name, 'webhook_request'::text, d.template_error_since::timestamptz,
           coalesce(d.template_error, '')::text
    FROM destinations d
    WHERE d.org_id = @org_id AND d.deleted_at IS NULL AND d.template_error_since IS NOT NULL
) t
ORDER BY t.since, t.public_id;

-- ListActiveStorms lists the Storms that have not ended, with their Route; a Storm of a deleted Route, which may not
-- have ended yet, is left out, as muster_storm_active leaves it out.
-- name: ListActiveStorms :many
SELECT r.public_id, r.name, s.started_at, s.alert_group_count
FROM storms s
JOIN routes r ON r.org_id = s.org_id AND r.id = s.route_id
WHERE s.org_id = @org_id AND s.ended_at IS NULL AND r.deleted_at IS NULL
ORDER BY s.started_at, s.id;

-- GetOutgoingHeartbeatResult reads whether the outgoing heartbeat has a URL and the result of its last attempt;
-- organizations is keyed by id.
-- name: GetOutgoingHeartbeatResult :one
SELECT (outgoing_heartbeat_url_ciphertext IS NOT NULL)::boolean AS configured, outgoing_heartbeat_last_attempt_at,
       outgoing_heartbeat_last_ok, outgoing_heartbeat_last_error
FROM organizations
WHERE id = @org_id;
