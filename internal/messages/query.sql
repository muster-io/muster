-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- Messages (C-12): the reads a message is rendered from — through the caller's transaction at the dispatcher's
-- re-render step — the samples of dry runs and previews, and the template error state of a Route.

-- GetRenderGroup reads what a message of an Alert Group shows: the Alert Group, its Route with the Route's
-- templates, language, Snooze durations and template error, the Organization's time zone, and the names of its Owner,
-- of who snoozed it and of who resolved it. It reads through the caller's transaction, so that a change the
-- dispatcher made is seen before it commits.
-- name: GetRenderGroup :one
SELECT g.id, g.public_id, g.number, g.title, g.summary, g.status, g.severity_level, g.urgent, g.group_key_labels,
       g.group_key_values, g.common_labels, g.common_annotations, g.firing_alert_count, g.reopen_count,
       g.resolved_by_kind, g.resolve_reason, g.snooze_until, g.snooze_no_end, g.unclaimed, g.created_at,
       r.id AS route_id, r.public_id AS route_public_id, r.name AS route_name, r.language,
       r.snooze_durations_seconds, r.template_root_message, r.template_line, r.template_error_template,
       o.time_zone,
       coalesce(ou.name, '')::text AS owner_name,
       coalesce(su.name, ss.name, '')::text AS snoozed_by_name,
       coalesce(ru.name, rs.name, '')::text AS resolved_by_name
FROM alert_groups g
JOIN routes r ON r.org_id = g.org_id AND r.id = g.route_id
JOIN organizations o ON o.id = g.org_id
LEFT JOIN users ou ON ou.org_id = g.org_id AND ou.id = g.owner_user_id
LEFT JOIN users su ON su.org_id = g.org_id AND su.id = g.snoozed_by_user_id
LEFT JOIN service_accounts ss ON ss.org_id = g.org_id AND ss.id = g.snoozed_by_service_account_id
LEFT JOIN users ru ON ru.org_id = g.org_id AND ru.id = g.resolved_by_user_id
LEFT JOIN service_accounts rs ON rs.org_id = g.org_id AND rs.id = g.resolved_by_service_account_id
WHERE g.org_id = @org_id AND g.id = @id;

-- GetGroupIDByPublicID reads the internal id of an Alert Group by its public_id, for a preview.
-- name: GetGroupIDByPublicID :one
SELECT id
FROM alert_groups
WHERE org_id = @org_id AND public_id = @public_id;

-- ListRenderAlerts lists the Alerts of an Alert Group that a message shows — those still firing in it and those
-- resolved in it, not those that moved on — firing first and newest first, at most lim of them, with how many there
-- are in all.
-- name: ListRenderAlerts :many
SELECT a.fingerprint, a.labels, m.annotations, m.starts_at, m.ended_at, a.generator_url, m.state,
       count(*) OVER ()::bigint AS total
FROM alert_group_alerts m
JOIN alerts a ON a.org_id = m.org_id AND a.id = m.alert_id
WHERE m.org_id = @org_id AND m.alert_group_id = @alert_group_id AND m.state IN ('firing', 'resolved')
ORDER BY m.state = 'firing' DESC, m.starts_at DESC, m.id DESC
LIMIT @lim;

-- ListAlertsByFingerprint lists the Alerts of an Alert Group with the fingerprints a Thread reply names, newest
-- first.
-- name: ListAlertsByFingerprint :many
SELECT DISTINCT ON (a.fingerprint) a.fingerprint, a.labels, m.annotations, m.starts_at
FROM alert_group_alerts m
JOIN alerts a ON a.org_id = m.org_id AND a.id = m.alert_id
WHERE m.org_id = @org_id AND m.alert_group_id = @alert_group_id AND a.fingerprint = ANY(@fingerprints::text[])
ORDER BY a.fingerprint, m.id DESC;

-- GetReplyEntry reads the reason, the replaced label and the count of missed notices of the lifecycle event a Thread
-- reply is about.
-- name: GetReplyEntry :one
SELECT coalesce(reason, '')::text AS reason, coalesce(replaced_label, '')::text AS replaced_label,
       coalesce(missed_count, 0)::bigint AS missed_count
FROM timeline_entries
WHERE org_id = @org_id AND alert_group_id = @alert_group_id AND event_seq = @event_seq::bigint
LIMIT 1;

-- GetPreviewRoute reads a Route that is not deleted by public_id, for a preview.
-- name: GetPreviewRoute :one
SELECT id, public_id, name, language, snooze_durations_seconds
FROM routes
WHERE org_id = @org_id AND public_id = @public_id AND deleted_at IS NULL;

-- GetRenderSettings reads the Organization's time zone and the Default route's language and Snooze durations, for a
-- preview without a Route.
-- name: GetRenderSettings :one
SELECT o.time_zone, r.name, r.language, r.snooze_durations_seconds
FROM organizations o
JOIN routes r ON r.org_id = @org_id AND r.is_default AND r.deleted_at IS NULL
WHERE o.id = @org_id;

-- ListRouteSnapshots lists the bodies of the most recent Stored Snapshots whose Alerts the Route took, newest first,
-- within the retention of Stored Snapshots, for a dry run.
-- name: ListRouteSnapshots :many
SELECT s.public_id, b.body
FROM stored_snapshots s
JOIN snapshot_bodies b ON b.org_id = @org_id AND b.body_sha256 = s.body_sha256 AND b.body_day = s.body_day
WHERE s.org_id = @org_id AND @route_id::bigint = ANY(s.route_ids) AND s.source = 'webhook' AND s.state = 'processed'
  AND s.received_at >= @not_before::timestamptz
ORDER BY s.received_at DESC, s.id DESC
LIMIT @lim;

-- GetSnapshotBody reads the body of a Stored Snapshot by public_id within the retention of Stored Snapshots.
-- name: GetSnapshotBody :one
SELECT b.body
FROM stored_snapshots s
JOIN snapshot_bodies b ON b.org_id = @org_id AND b.body_sha256 = s.body_sha256 AND b.body_day = s.body_day
WHERE s.org_id = @org_id AND s.public_id = @public_id AND s.received_at >= @not_before::timestamptz
LIMIT 1;

-- GetSnapshotRetention reads retention.stored_snapshots in days.
-- name: GetSnapshotRetention :one
SELECT retention_stored_snapshots_days
FROM organizations
WHERE id = @org_id;

-- SetTemplateError records that a template of the Route keeps failing, when no template error is recorded yet. The
-- Route's row is skipped when another transaction holds it — grouping holds it for share — so that a render never
-- waits for it; the next failing render tries again. It returns the Route's name when it recorded the error.
-- name: SetTemplateError :many
UPDATE routes r
SET template_error_since = @now::timestamptz, template_error = @error::text, template_error_template = @template::text
FROM (SELECT l.id
      FROM routes l
      WHERE l.org_id = @org_id AND l.id = @id AND l.template_error IS NULL
      FOR UPDATE SKIP LOCKED) locked
WHERE r.org_id = @org_id AND r.id = locked.id
RETURNING r.name;

-- ClearTemplateError clears the template error of the Route when it is about the template given, once that template
-- rendered again; the Route's row is skipped while another transaction holds it, as in SetTemplateError.
-- name: ClearTemplateError :many
UPDATE routes r
SET template_error_since = NULL, template_error = NULL, template_error_template = NULL
FROM (SELECT l.id
      FROM routes l
      WHERE l.org_id = @org_id AND l.id = @id AND l.template_error_template = @template::text
      FOR UPDATE SKIP LOCKED) locked
WHERE r.org_id = @org_id AND r.id = locked.id
RETURNING r.public_id;
