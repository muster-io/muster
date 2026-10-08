-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- ListRoutes lists the Routes that are not deleted in evaluation order, the Default route last, with their template
-- error and their active Storm.
-- name: ListRoutes :many
SELECT r.id, r.public_id, r.name, r.description, r.position, r.is_default, r.urgent, r.group_key,
       r.reopen_window_seconds, r.grace_period_seconds, r.urgent_rise_removes_ack, r.snooze_durations_seconds,
       r.thread_batching_window_seconds, r.storm_threshold, r.language, r.template_root_message, r.template_line,
       r.template_ack_timeout_notice, r.ack_timeout_enabled, r.ack_timeout_first_interval_seconds,
       r.reminders_enabled, r.reminders_first_interval_seconds, r.reminders_cap_seconds, r.auto_unacknowledge,
       r.created_at, r.version, r.template_error_since, r.template_error, s.started_at AS storm_since,
       s.alert_group_count AS storm_alert_group_count
FROM routes r
LEFT JOIN storms s ON s.org_id = r.org_id AND s.route_id = r.id AND s.ended_at IS NULL
WHERE r.org_id = @org_id AND r.deleted_at IS NULL
ORDER BY r.is_default, r.position, r.id;

-- GetRoute reads a Route that is not deleted with its place in evaluation order, zero-based, its template error and its
-- active Storm.
-- name: GetRoute :one
SELECT r.id, r.public_id, r.name, r.description, r.position, r.is_default, r.urgent, r.group_key,
       r.reopen_window_seconds, r.grace_period_seconds, r.urgent_rise_removes_ack, r.snooze_durations_seconds,
       r.thread_batching_window_seconds, r.storm_threshold, r.language, r.template_root_message, r.template_line,
       r.template_ack_timeout_notice, r.ack_timeout_enabled, r.ack_timeout_first_interval_seconds,
       r.reminders_enabled, r.reminders_first_interval_seconds, r.reminders_cap_seconds, r.auto_unacknowledge,
       r.created_at, r.version, r.template_error_since, r.template_error,
       (SELECT count(*)
        FROM routes o
        WHERE o.org_id = @org_id AND o.deleted_at IS NULL
          AND (o.is_default, o.position, o.id) < (r.is_default, r.position, r.id))::bigint AS place,
       s.started_at AS storm_since, s.alert_group_count AS storm_alert_group_count
FROM routes r
LEFT JOIN storms s ON s.org_id = r.org_id AND s.route_id = r.id AND s.ended_at IS NULL
WHERE r.org_id = @org_id AND r.public_id = @public_id AND r.deleted_at IS NULL;

-- LockRoute locks a Route that is not deleted for a change; the lock leaves the key alone, so that routing, whose
-- foreign key check on alerts.route_id takes a key share lock, never waits for it.
-- name: LockRoute :one
SELECT id
FROM routes
WHERE org_id = @org_id AND public_id = @public_id AND deleted_at IS NULL
FOR NO KEY UPDATE;

-- ListRouteMatchers lists the Matchers of the Routes, in their order.
-- name: ListRouteMatchers :many
SELECT route_id, label, op, value
FROM route_matchers
WHERE org_id = @org_id AND route_id = ANY(@route_ids::bigint[])
ORDER BY route_id, position;

-- BumpRouteOrder bumps the version of the Route list, the list ETag, and returns the new one; with @expected it
-- returns no row unless the version is still that one. The row lock serializes the changes of the list.
-- name: BumpRouteOrder :one
UPDATE organizations
SET route_order_version = route_order_version + 1
WHERE id = @org_id
  AND (sqlc.narg('expected')::bigint IS NULL OR route_order_version = sqlc.narg('expected')::bigint)
RETURNING route_order_version;

-- GetRouteOrderVersion reads the version of the Route list.
-- name: GetRouteOrderVersion :one
SELECT route_order_version
FROM organizations
WHERE id = @org_id;

-- InsertRoute creates a Route at the last position before the Default route, or with @first at the top of the list.
-- name: InsertRoute :one
INSERT INTO routes (
    org_id, public_id, name, description, position, is_default, urgent, group_key, reopen_window_seconds,
    grace_period_seconds, urgent_rise_removes_ack, snooze_durations_seconds, thread_batching_window_seconds,
    storm_threshold, language, template_root_message, template_line, template_ack_timeout_notice,
    ack_timeout_enabled, ack_timeout_first_interval_seconds, reminders_enabled, reminders_first_interval_seconds,
    reminders_cap_seconds, auto_unacknowledge, created_at, updated_at
)
SELECT @org_id, @public_id, @name, @description,
       coalesce((SELECT CASE WHEN @first::boolean THEN min(p.position) - 1 ELSE max(p.position) + 1 END
                 FROM routes p
                 WHERE p.org_id = @org_id AND NOT p.is_default AND p.deleted_at IS NULL), 0),
       false, @urgent, @group_key::text[], @reopen_window_seconds, @grace_period_seconds, @urgent_rise_removes_ack,
       @snooze_durations_seconds::bigint[], @thread_batching_window_seconds, @storm_threshold, @language,
       sqlc.narg('template_root_message'), sqlc.narg('template_line'), sqlc.narg('template_ack_timeout_notice'),
       @ack_timeout_enabled, @ack_timeout_first_interval_seconds, @reminders_enabled,
       @reminders_first_interval_seconds, @reminders_cap_seconds, @auto_unacknowledge, @now, @now
RETURNING id;

-- EnsureDefaultRoute creates the Default route of the Organization when it has none, and returns its public_id only
-- when it created it.
-- name: EnsureDefaultRoute :one
INSERT INTO routes (
    org_id, public_id, name, description, position, is_default, urgent, group_key, reopen_window_seconds,
    grace_period_seconds, urgent_rise_removes_ack, snooze_durations_seconds, thread_batching_window_seconds,
    storm_threshold, language, ack_timeout_enabled, ack_timeout_first_interval_seconds, reminders_enabled,
    reminders_first_interval_seconds, reminders_cap_seconds, auto_unacknowledge, created_at, updated_at
)
VALUES (
    @org_id, @public_id, @name, @description, 0, true, @urgent, @group_key::text[], @reopen_window_seconds,
    @grace_period_seconds, @urgent_rise_removes_ack, @snooze_durations_seconds::bigint[],
    @thread_batching_window_seconds, @storm_threshold, @language, @ack_timeout_enabled,
    @ack_timeout_first_interval_seconds, @reminders_enabled, @reminders_first_interval_seconds, @reminders_cap_seconds,
    @auto_unacknowledge, @now, @now
)
ON CONFLICT (org_id) WHERE is_default DO NOTHING
RETURNING public_id;

-- UpdateRoute replaces the configured fields of a Route and bumps its version.
-- name: UpdateRoute :exec
UPDATE routes
SET name = @name, description = @description, urgent = @urgent, group_key = @group_key::text[],
    reopen_window_seconds = @reopen_window_seconds, grace_period_seconds = @grace_period_seconds,
    urgent_rise_removes_ack = @urgent_rise_removes_ack, snooze_durations_seconds = @snooze_durations_seconds::bigint[],
    thread_batching_window_seconds = @thread_batching_window_seconds, storm_threshold = @storm_threshold,
    language = @language, template_root_message = sqlc.narg('template_root_message'),
    template_line = sqlc.narg('template_line'), template_ack_timeout_notice = sqlc.narg('template_ack_timeout_notice'),
    ack_timeout_enabled = @ack_timeout_enabled, ack_timeout_first_interval_seconds = @ack_timeout_first_interval_seconds,
    reminders_enabled = @reminders_enabled, reminders_first_interval_seconds = @reminders_first_interval_seconds,
    reminders_cap_seconds = @reminders_cap_seconds, auto_unacknowledge = @auto_unacknowledge, updated_at = @now,
    version = version + 1
WHERE org_id = @org_id AND id = @id;

-- DeleteRouteMatchers removes the Matchers of a Route before they are written again.
-- name: DeleteRouteMatchers :exec
DELETE FROM route_matchers
WHERE org_id = @org_id AND route_id = @route_id;

-- InsertRouteMatcher writes one Matcher of a Route at its position.
-- name: InsertRouteMatcher :exec
INSERT INTO route_matchers (route_id, org_id, position, label, op, value)
VALUES (@route_id, @org_id, @position, @label, @op, @value);

-- DeleteRoute soft-deletes a Route: it leaves every list and the evaluation order at once.
-- name: DeleteRoute :exec
UPDATE routes
SET deleted_at = @now, updated_at = @now, version = version + 1
WHERE org_id = @org_id AND id = @id AND NOT is_default;

-- SetRoutePositions rewrites the positions of the Routes.
-- name: SetRoutePositions :exec
UPDATE routes r
SET position = p.position
FROM (SELECT unnest(@ids::bigint[]) AS id, unnest(@positions::bigint[]) AS position) AS p
WHERE r.org_id = @org_id AND r.id = p.id;

-- ListRouteInfo lists the Routes that are not deleted for muster_route_info.
-- name: ListRouteInfo :many
SELECT public_id, name
FROM routes
WHERE org_id = @org_id AND deleted_at IS NULL;

-- GetRoutingStamp reads what decides whether the cached evaluation order is current — the version of the Route list,
-- which creation, deletion and reordering bump, with the sum of the versions of the Routes, which every edit bumps —
-- and the Severity level settings of the Organization.
-- name: GetRoutingStamp :one
SELECT o.route_order_version,
       (SELECT coalesce(sum(r.version), 0)
        FROM routes r
        WHERE r.org_id = @org_id AND r.deleted_at IS NULL)::bigint AS route_versions,
       o.severity_label, o.severity_mapping
FROM organizations o
WHERE o.id = @org_id;

-- ListAlertLabels reads the labels of Alerts, with their Static labels applied: the Alerts ids, and the Alerts
-- listed_ids that fire without a Route.
-- name: ListAlertLabels :many
SELECT id, labels
FROM alerts
WHERE org_id = @org_id
  AND (id = ANY(@ids::bigint[])
       OR (id = ANY(@listed_ids::bigint[]) AND status = 'firing' AND route_id IS NULL));

-- SetAlertRoutes records the Route and the Severity level of the current firing of each Alert; a route id of 0 and
-- an empty raw value are null.
-- name: SetAlertRoutes :exec
UPDATE alerts a
SET route_id = nullif(r.route_id, 0), severity_level = r.severity_level, severity_raw = nullif(r.severity_raw, '')
FROM (SELECT unnest(@ids::bigint[]) AS id, unnest(@route_ids::bigint[]) AS route_id,
             unnest(@severity_levels::text[]) AS severity_level, unnest(@severity_raws::text[]) AS severity_raw) AS r
WHERE a.org_id = @org_id AND a.id = r.id;

-- ListHeartbeatIntegrations lists the Integrations that are not deleted and have their Heartbeat on, with what
-- MusterHeartbeatLost carries of them: the public_id, the name and the Static labels.
-- name: ListHeartbeatIntegrations :many
SELECT public_id, name, static_labels
FROM integrations
WHERE org_id = @org_id AND deleted_at IS NULL AND heartbeat_enabled
ORDER BY id;

-- ListRouteSuggestionDismissals lists the Route suggestions a User dismissed.
-- name: ListRouteSuggestionDismissals :many
SELECT suggestion
FROM route_suggestion_dismissals
WHERE org_id = @org_id AND user_id = @user_id;

-- DismissRouteSuggestion records that a User dismissed a Route suggestion; a second dismissal changes nothing.
-- name: DismissRouteSuggestion :exec
INSERT INTO route_suggestion_dismissals (user_id, suggestion, org_id, dismissed_at)
VALUES (@user_id, @suggestion, @org_id, @dismissed_at)
ON CONFLICT (user_id, suggestion) DO NOTHING;

-- CountOpenAlertGroups counts the open Alert Groups of a Route, read-only (only groups writes alert_groups); a
-- deletion counts them after LockRoute, so that a Snapshot grouping on the Route either committed before or waits.
-- name: CountOpenAlertGroups :one
SELECT count(*)::bigint
FROM alert_groups
WHERE org_id = @org_id AND route_id = @route_id AND status <> 'resolved' AND moved_from_route_id IS NULL;

-- ListOpenAlertGroupCounts counts the open Alert Groups of each Route that has any (Route.open_alert_group_count).
-- name: ListOpenAlertGroupCounts :many
SELECT route_id, count(*)::bigint AS count
FROM alert_groups
WHERE org_id = @org_id AND status <> 'resolved' AND moved_from_route_id IS NULL
GROUP BY route_id;

-- RestampAlertRoutes records another Route for the current firing of Alerts: the Default route, for Alerts whose Route
-- was deleted while their Snapshot waited for it.
-- name: RestampAlertRoutes :exec
UPDATE alerts
SET route_id = @route_id
WHERE org_id = @org_id AND id = ANY(@ids::bigint[]);

-- ResolveDestinations reads the Destinations that are not deleted among the public_ids, for the Destinations of a
-- Route.
-- name: ResolveDestinations :many
SELECT id, public_id
FROM destinations
WHERE org_id = @org_id AND public_id = ANY(@public_ids::text[]) AND deleted_at IS NULL;

-- ListRouteDestinationIDs lists the Destinations that are not deleted of the Routes, by public_id.
-- name: ListRouteDestinationIDs :many
SELECT rd.route_id, rd.destination_id, d.public_id
FROM route_destinations rd
JOIN destinations d ON d.org_id = rd.org_id AND d.id = rd.destination_id
WHERE rd.org_id = @org_id AND rd.route_id = ANY(@route_ids::bigint[]) AND d.deleted_at IS NULL
ORDER BY rd.route_id, d.public_id;

-- InsertRouteDestinations adds Destinations to a Route, added now.
-- name: InsertRouteDestinations :exec
INSERT INTO route_destinations (route_id, destination_id, org_id, added_at)
SELECT @route_id, unnest(@destination_ids::bigint[]), @org_id, @now;

-- DeleteRouteDestinations removes Destinations from a Route.
-- name: DeleteRouteDestinations :exec
DELETE FROM route_destinations
WHERE org_id = @org_id AND route_id = @route_id AND destination_id = ANY(@destination_ids::bigint[]);

-- LockRouteMembership takes, until the transaction ends, the membership lock of a Route exclusively before its
-- Destinations change: delivery's Enqueue takes it shared (ShareRouteMembership, class db.RouteMembershipLockClass),
-- so that a change of the Destinations and an Enqueue on the Route serialize.
-- name: LockRouteMembership :exec
SELECT pg_advisory_xact_lock(@lock_class::int, hashint8(@route_id::bigint));

-- BumpRoutesOfDestination gives each Route that is not deleted of a Destination about to leave them a new version,
-- locking them in id order, and returns them in that order.
-- name: BumpRoutesOfDestination :many
UPDATE routes r
SET version = r.version + 1, updated_at = @now
FROM (SELECT x.id
      FROM routes x
      WHERE x.org_id = @org_id AND x.deleted_at IS NULL
        AND x.id IN (SELECT rd.route_id
                     FROM route_destinations rd
                     WHERE rd.org_id = @org_id AND rd.destination_id = @destination_id)
      ORDER BY x.id
      FOR NO KEY UPDATE OF x) AS locked
WHERE r.org_id = @org_id AND r.id = locked.id
RETURNING r.id, r.public_id;
