-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- The queries of the Alert Group lifecycle. Only this package writes alert_groups, alert_group_alerts,
-- timeline_entries, notes and alert_group_counters (lint 2). Times are business times passed in from Go.

-- GetGroupingSettings reads the Organization settings grouping needs: critical is Urgent and the Instance labels.
-- name: GetGroupingSettings :one
SELECT critical_is_urgent, instance_labels
FROM organizations
WHERE id = @org_id;

-- ListChangedAlerts reads the Alerts of a Snapshot's changes, with the Route and Severity level routing stamped.
-- name: ListChangedAlerts :many
SELECT id, integration_id, fingerprint, labels, annotations, static_label_conflicts, status, starts_at, episode,
       route_id, severity_level
FROM alerts
WHERE org_id = @org_id AND id = ANY(@ids::bigint[]);

-- ListFiringMemberships reads where the Alerts fire: an Alert lives in at most one Alert Group at a time.
-- name: ListFiringMemberships :many
SELECT id, alert_group_id, alert_id, episode, starts_at, annotations
FROM alert_group_alerts
WHERE org_id = @org_id AND alert_id = ANY(@alert_ids::bigint[]) AND state = 'firing';

-- LockRoutes locks the Routes that are not deleted FOR SHARE before grouping creates or joins an Alert Group on
-- them, in id order: a deletion, which locks its Route FOR NO KEY UPDATE and then counts the open Alert Groups, waits
-- for the Snapshot, or commits first and leaves the Route out of the result.
-- name: LockRoutes :many
SELECT id, public_id, is_default, urgent, group_key, reopen_window_seconds, grace_period_seconds,
       urgent_rise_removes_ack
FROM routes
WHERE org_id = @org_id AND id = ANY(@ids::bigint[]) AND deleted_at IS NULL
ORDER BY id
FOR SHARE;

-- LockDefaultRoute locks the Default route FOR SHARE, for the Alerts whose Route was deleted meanwhile.
-- name: LockDefaultRoute :one
SELECT id, public_id, is_default, urgent, group_key, reopen_window_seconds, grace_period_seconds,
       urgent_rise_removes_ack
FROM routes
WHERE org_id = @org_id AND is_default
FOR SHARE;

-- GetRoutePolicy reads the policy of a Route, deleted or not, that an existing Alert Group belongs to.
-- name: GetRoutePolicy :one
SELECT id, public_id, is_default, urgent, group_key, reopen_window_seconds, grace_period_seconds,
       urgent_rise_removes_ack
FROM routes
WHERE org_id = @org_id AND id = @id;

-- FindOpenGroup finds the open Alert Group of a Route and Group key values; Alert Groups moved to the Default route
-- are left out, as alert_groups_open_key leaves them out.
-- name: FindOpenGroup :one
SELECT id
FROM alert_groups
WHERE org_id = @org_id AND route_id = @route_id AND group_key_sha256 = @group_key_sha256
  AND status <> 'resolved' AND moved_from_route_id IS NULL;

-- FindReopenableGroup finds the latest system-resolved Alert Group of a Route and Group key values whose Reopen
-- window is still open at now.
-- name: FindReopenableGroup :one
SELECT id
FROM alert_groups
WHERE org_id = @org_id AND route_id = @route_id AND group_key_sha256 = @group_key_sha256
  AND reopen_deadline > @now::timestamptz
ORDER BY resolved_at DESC, id DESC
LIMIT 1;

-- PeekGroup reads, without a lock, the Route and the Grace period of an Alert Group, so that the end of its Grace
-- period finds the Alert Groups it changes before it locks them all in id order.
-- name: PeekGroup :one
SELECT route_id, grace_deadline
FROM alert_groups
WHERE org_id = @org_id AND id = @id;

-- LockGroups locks Alert Groups for a change, in id order, so that two transactions never wait for each other.
-- name: LockGroups :many
SELECT id, public_id, number, route_id, moved_from_route_id, group_key_labels, group_key_values, group_key_sha256,
       title, title_from_group_key, summary, common_labels, common_annotations, integration_ids, status,
       severity_level, urgent, owner_user_id, acknowledged_at, snooze_until, snooze_no_end, snoozed_while_urgent,
       snoozed_by_user_id, snoozed_by_service_account_id, firing_alert_count, resolved_alert_count, reopen_count,
       resolved_at, resolved_by_kind, resolved_by_user_id, resolved_by_service_account_id, resolve_reason,
       resolve_reason_text, reopen_deadline, prior_status, prior_owner_user_id, prior_snooze_until,
       prior_snooze_no_end, prior_snoozed_while_urgent, prior_snoozed_by_user_id,
       prior_snoozed_by_service_account_id, grace_deadline, firing_again_after_id, event_seq, created_at,
       last_changed_at
FROM alert_groups
WHERE org_id = @org_id AND id = ANY(@ids::bigint[])
ORDER BY id
FOR UPDATE;

-- EnsureCounter creates the Organization's row of alert_group_counters on first use.
-- name: EnsureCounter :exec
INSERT INTO alert_group_counters (org_id)
VALUES (@org_id)
ON CONFLICT (org_id) DO NOTHING;

-- LockCounter locks the Organization's counter row: every change that makes an Alert Group open — a creation or a
-- Reopen — takes it first, so that two of them never race for one Route and Group key.
-- name: LockCounter :one
SELECT last_number
FROM alert_group_counters
WHERE org_id = @org_id
FOR UPDATE;

-- NextNumber takes the next #N; a rollback returns it, so no gap appears.
-- name: NextNumber :one
UPDATE alert_group_counters
SET last_number = last_number + 1
WHERE org_id = @org_id
RETURNING last_number;

-- InsertGroup creates an Alert Group, firing.
-- name: InsertGroup :one
INSERT INTO alert_groups (
    org_id, public_id, number, route_id, group_key_labels, group_key_values, group_key_sha256, title,
    title_from_group_key, summary, common_labels, common_annotations, integration_ids, status, severity_level, urgent,
    firing_again_after_id, created_at, last_changed_at
)
VALUES (
    @org_id, @public_id, @number, @route_id, @group_key_labels::text[], @group_key_values, @group_key_sha256, @title,
    @title_from_group_key, sqlc.narg('summary'), @common_labels, @common_annotations, @integration_ids::bigint[],
    'firing', @severity_level, @urgent, sqlc.narg('firing_again_after_id'), @created_at, @created_at
)
RETURNING id;

-- SaveGroup writes the state of an Alert Group after a change.
-- name: SaveGroup :exec
UPDATE alert_groups
SET route_id = @route_id, moved_from_route_id = sqlc.narg('moved_from_route_id'), title = @title,
    title_from_group_key = @title_from_group_key, summary = sqlc.narg('summary'), common_labels = @common_labels,
    common_annotations = @common_annotations, integration_ids = @integration_ids::bigint[], status = @status,
    severity_level = @severity_level, urgent = @urgent, owner_user_id = sqlc.narg('owner_user_id'),
    acknowledged_at = sqlc.narg('acknowledged_at'), snooze_until = sqlc.narg('snooze_until'),
    snooze_no_end = @snooze_no_end, snoozed_while_urgent = @snoozed_while_urgent,
    snoozed_by_user_id = sqlc.narg('snoozed_by_user_id'),
    snoozed_by_service_account_id = sqlc.narg('snoozed_by_service_account_id'),
    firing_alert_count = @firing_alert_count, resolved_alert_count = @resolved_alert_count,
    reopen_count = @reopen_count, resolved_at = sqlc.narg('resolved_at'),
    resolved_by_kind = sqlc.narg('resolved_by_kind'), resolved_by_user_id = sqlc.narg('resolved_by_user_id'),
    resolved_by_service_account_id = sqlc.narg('resolved_by_service_account_id'),
    resolve_reason = sqlc.narg('resolve_reason'), resolve_reason_text = sqlc.narg('resolve_reason_text'),
    reopen_deadline = sqlc.narg('reopen_deadline'), prior_status = sqlc.narg('prior_status'),
    prior_owner_user_id = sqlc.narg('prior_owner_user_id'), prior_snooze_until = sqlc.narg('prior_snooze_until'),
    prior_snooze_no_end = sqlc.narg('prior_snooze_no_end'),
    prior_snoozed_while_urgent = sqlc.narg('prior_snoozed_while_urgent'),
    prior_snoozed_by_user_id = sqlc.narg('prior_snoozed_by_user_id'),
    prior_snoozed_by_service_account_id = sqlc.narg('prior_snoozed_by_service_account_id'),
    grace_deadline = sqlc.narg('grace_deadline'), event_seq = @event_seq, last_changed_at = @last_changed_at
WHERE org_id = @org_id AND id = @id;

-- IsActiveUser reports whether a User can still own an Alert Group: a disabled or deleted Owner has no
-- acknowledgements.
-- name: IsActiveUser :one
SELECT EXISTS (
    SELECT 1
    FROM users
    WHERE org_id = @org_id AND id = @id AND status = 'active'
)::boolean AS active;

-- InsertMemberships adds Alerts to Alert Groups, firing.
-- name: InsertMemberships :exec
INSERT INTO alert_group_alerts (org_id, alert_group_id, alert_id, episode, state, joined_at, starts_at, annotations)
SELECT @org_id, unnest(@alert_group_ids::bigint[]), unnest(@alert_ids::bigint[]), unnest(@episodes::bigint[]),
       'firing', @joined_at, unnest(@starts_ats::timestamptz[]), unnest(@annotations::jsonb[]);

-- ResolveMemberships ends firing memberships whose Alerts resolved, each with its reason.
-- name: ResolveMemberships :exec
UPDATE alert_group_alerts m
SET state = 'resolved', ended_at = @ended_at::timestamptz, resolve_reason = r.reason, resolve_reason_text = r.reason_text
FROM (SELECT unnest(@ids::bigint[]) AS id, unnest(@reasons::text[]) AS reason,
             unnest(@reason_texts::text[]) AS reason_text) AS r
WHERE m.org_id = @org_id AND m.id = r.id AND m.state = 'firing';

-- MoveMemberships ends firing memberships whose Alerts were grouped again, into the Alert Group that took them.
-- name: MoveMemberships :exec
UPDATE alert_group_alerts m
SET state = 'moved', ended_at = @ended_at::timestamptz, moved_to_alert_group_id = r.moved_to
FROM (SELECT unnest(@ids::bigint[]) AS id, unnest(@moved_tos::bigint[]) AS moved_to) AS r
WHERE m.org_id = @org_id AND m.id = r.id AND m.state = 'firing';

-- UpdateMemberships records the startsAt of a Continuation and the annotations as last seen.
-- name: UpdateMemberships :exec
UPDATE alert_group_alerts m
SET starts_at = r.starts_at, annotations = r.annotations
FROM (SELECT unnest(@ids::bigint[]) AS id, unnest(@starts_ats::timestamptz[]) AS starts_at,
             unnest(@annotations::jsonb[]) AS annotations) AS r
WHERE m.org_id = @org_id AND m.id = r.id;

-- ListGroupFiringAlerts lists the Alerts firing in Alert Groups with their labels: the comparison of a Replacement,
-- and the tails that a Grace period end groups again.
-- name: ListGroupFiringAlerts :many
SELECT m.id, m.alert_group_id, m.alert_id, m.episode, m.starts_at, m.annotations, a.integration_id, a.fingerprint,
       a.labels, a.static_label_conflicts, a.severity_level, a.status
FROM alert_group_alerts m
JOIN alerts a ON a.org_id = m.org_id AND a.id = m.alert_id
WHERE m.org_id = @org_id AND m.alert_group_id = ANY(@alert_group_ids::bigint[]) AND m.state = 'firing'
ORDER BY m.alert_group_id, m.id;

-- InsertTimelineEntry records one Timeline entry: a lifecycle event or a system entry.
-- name: InsertTimelineEntry :exec
INSERT INTO timeline_entries (
    org_id, public_id, alert_group_id, at, kind, event, event_seq, system_event, loudness, mentions, actor_kind,
    actor_user_id, actor_service_account_id, api_token_id, token_name, transport, reason, from_status, to_status,
    owner_user_id, previous_owner_user_id, snooze_until, fingerprints, replaced_label, label_conflicts, period_from,
    period_to, detail
)
VALUES (
    @org_id, @public_id, @alert_group_id, @at, @kind, sqlc.narg('event'), sqlc.narg('event_seq'),
    sqlc.narg('system_event'), sqlc.narg('loudness'), @mentions::text[], @actor_kind, sqlc.narg('actor_user_id'),
    sqlc.narg('actor_service_account_id'), sqlc.narg('api_token_id'), sqlc.narg('token_name'), @transport,
    sqlc.narg('reason'), sqlc.narg('from_status'), sqlc.narg('to_status'), sqlc.narg('owner_user_id'),
    sqlc.narg('previous_owner_user_id'), sqlc.narg('snooze_until'), sqlc.narg('fingerprints')::text[],
    sqlc.narg('replaced_label'), sqlc.narg('label_conflicts')::text[], sqlc.narg('period_from'),
    sqlc.narg('period_to'), sqlc.narg('detail')
);

-- ListOpenGroupIDs lists the open Alert Groups of the Organization, for the downtime entries.
-- name: ListOpenGroupIDs :many
SELECT id
FROM alert_groups
WHERE org_id = @org_id AND status <> 'resolved'
ORDER BY id;

-- InsertDowntimeEntries records "Muster was unavailable from … to …" on Alert Groups.
-- name: InsertDowntimeEntries :exec
INSERT INTO timeline_entries (org_id, public_id, alert_group_id, at, kind, system_event, actor_kind, transport,
                              period_from, period_to)
SELECT @org_id, unnest(@public_ids::text[]), unnest(@alert_group_ids::bigint[]), @at, 'system',
       'muster_unavailable', 'system', 'system', @period_from::timestamptz, @period_to::timestamptz;

-- UpsertTimer sets the deadline of an Alert Group's timer of a kind, free of any lease.
-- name: UpsertTimer :exec
INSERT INTO timers (org_id, alert_group_id, kind, deadline, created_at, updated_at)
VALUES (@org_id, @alert_group_id::bigint, @kind, @deadline, @now, @now)
ON CONFLICT (alert_group_id, kind) WHERE alert_group_id IS NOT NULL
DO UPDATE SET deadline = excluded.deadline, lease_owner = NULL, lease_until = NULL, attempts = 0,
              updated_at = excluded.updated_at;

-- DeleteTimer stops an Alert Group's timer of a kind.
-- name: DeleteTimer :exec
DELETE FROM timers
WHERE org_id = @org_id AND alert_group_id = @alert_group_id::bigint AND kind = @kind;

-- NotifyTimers wakes the timer workers of every replica once the transaction commits.
-- name: NotifyTimers :exec
SELECT pg_notify(@channel::text, '');

-- LockRouteForMove locks a Route that is not deleted against grouping, which takes it FOR SHARE, while its open
-- Alert Groups move to the Default route.
-- name: LockRouteForMove :one
SELECT id, public_id, name, is_default
FROM routes
WHERE org_id = @org_id AND public_id = @public_id AND deleted_at IS NULL
FOR NO KEY UPDATE;

-- GetDefaultRouteID reads the id of the Default route.
-- name: GetDefaultRouteID :one
SELECT id
FROM routes
WHERE org_id = @org_id AND is_default;

-- ListOpenGroupsOfRoute lists the open Alert Groups of a Route.
-- name: ListOpenGroupsOfRoute :many
SELECT id
FROM alert_groups
WHERE org_id = @org_id AND route_id = @route_id AND status <> 'resolved' AND moved_from_route_id IS NULL
ORDER BY id;

-- GetGroup reads an Alert Group by public_id with its Route, the #N of the Alert Group it fires again after, the
-- label of its latest Replacement and the Alerts still firing in it.
-- name: GetGroup :one
SELECT g.id, g.public_id, g.number, g.title, g.summary, g.status, g.severity_level, g.urgent, g.group_key_values,
       g.common_labels, g.common_annotations, g.integration_ids, g.reopen_count, g.firing_alert_count,
       g.resolved_alert_count, g.resolved_at, g.resolved_by_kind, g.resolved_by_user_id,
       g.resolved_by_service_account_id, g.resolve_reason, g.resolve_reason_text, g.created_at, g.last_changed_at,
       r.public_id AS route_public_id, r.name AS route_name,
       coalesce((SELECT p.number
                 FROM alert_groups p
                 WHERE p.org_id = g.org_id AND p.id = g.firing_again_after_id), 0)::bigint AS firing_again_after_number,
       coalesce((SELECT e.replaced_label
                 FROM timeline_entries e
                 WHERE e.org_id = g.org_id AND e.alert_group_id = g.id AND e.event = 'alert_replaced'
                 ORDER BY e.at DESC, e.id DESC
                 LIMIT 1), '')::text AS replaced_label,
       (SELECT count(*)
        FROM alert_group_alerts m
        WHERE m.org_id = g.org_id AND m.alert_group_id = g.id AND m.state = 'firing')::bigint AS still_firing
FROM alert_groups g
JOIN routes r ON r.org_id = g.org_id AND r.id = g.route_id
WHERE g.org_id = @org_id AND g.public_id = @public_id;

-- GetGroupID reads the id of an Alert Group by public_id.
-- name: GetGroupID :one
SELECT id
FROM alert_groups
WHERE org_id = @org_id AND public_id = @public_id;

-- ListIntegrationRefs names Integrations, deleted ones included.
-- name: ListIntegrationRefs :many
SELECT id, public_id, name
FROM integrations
WHERE org_id = @org_id AND id = ANY(@ids::bigint[])
ORDER BY name, id;

-- ListUserRefs names Users, deleted ones included.
-- name: ListUserRefs :many
SELECT id, public_id, name, login, status
FROM users
WHERE org_id = @org_id AND id = ANY(@ids::bigint[]);

-- ListServiceAccountRefs names Service accounts, deleted ones included.
-- name: ListServiceAccountRefs :many
SELECT id, public_id, name, status
FROM service_accounts
WHERE org_id = @org_id AND id = ANY(@ids::bigint[]);

-- ListGroupAlerts lists the Alerts of an Alert Group, firing first, then those that ended, after a position; @rank
-- is 0 for firing and 1 for ended.
-- name: ListGroupAlerts :many
SELECT m.id, (m.state <> 'firing')::boolean AS ended, m.state, m.starts_at, m.annotations, m.ended_at,
       m.resolve_reason, m.resolve_reason_text, a.id AS alert_id, a.fingerprint, a.labels, a.generator_url,
       a.last_seen_at, i.public_id AS integration_public_id, i.name AS integration_name
FROM alert_group_alerts m
JOIN alerts a ON a.org_id = m.org_id AND a.id = m.alert_id
JOIN integrations i ON i.org_id = a.org_id AND i.id = a.integration_id
WHERE m.org_id = @org_id AND m.alert_group_id = @alert_group_id
  AND (@all_states::boolean OR (m.state = 'firing') = @firing::boolean)
  AND ((m.state <> 'firing')::int, m.id) > (@after_rank::int, @after_id::bigint)
ORDER BY (m.state <> 'firing')::int, m.id
LIMIT @lim;

-- ListAlertGroupKeys lists the Alertmanager groupKeys that listed each Alert during its current or last firing.
-- name: ListAlertGroupKeys :many
SELECT p.alert_id, g.group_key
FROM alert_presences p
JOIN alerts a ON a.org_id = p.org_id AND a.id = p.alert_id
JOIN alertmanager_groups g ON g.org_id = p.org_id AND g.id = p.alertmanager_group_id
WHERE p.org_id = @org_id AND p.alert_id = ANY(@alert_ids::bigint[]) AND p.last_seen_at >= a.fired_at
ORDER BY p.alert_id, g.group_key;

-- The Timeline merges three sources by (at, source, id): its entries (source 0), Notes (1) and delivery events (2).
-- A page reads up to @lim of each after the position of the previous page and keeps the first @lim of the merge.

-- ListTimelineDesc reads the Timeline entries of an Alert Group, newest first, before a position.
-- name: ListTimelineDesc :many
SELECT id, public_id, at, kind, event, event_seq, system_event, loudness, mentions, actor_kind, actor_user_id,
       actor_service_account_id, token_name, transport, reason, from_status, to_status, owner_user_id,
       previous_owner_user_id, snooze_until, fingerprints, replaced_label, label_conflicts, notice_number,
       missed_count, period_from, period_to, detail
FROM timeline_entries
WHERE org_id = @org_id AND alert_group_id = @alert_group_id
  AND (cardinality(@kinds::text[]) = 0 OR kind = ANY(@kinds::text[]))
  AND (sqlc.narg('at')::timestamptz IS NULL
       OR (at, 0, id) < (sqlc.narg('at')::timestamptz, @source::int, @id::bigint))
ORDER BY at DESC, id DESC
LIMIT @lim;

-- ListTimelineAsc reads the Timeline entries of an Alert Group, oldest first, after a position.
-- name: ListTimelineAsc :many
SELECT id, public_id, at, kind, event, event_seq, system_event, loudness, mentions, actor_kind, actor_user_id,
       actor_service_account_id, token_name, transport, reason, from_status, to_status, owner_user_id,
       previous_owner_user_id, snooze_until, fingerprints, replaced_label, label_conflicts, notice_number,
       missed_count, period_from, period_to, detail
FROM timeline_entries
WHERE org_id = @org_id AND alert_group_id = @alert_group_id
  AND (cardinality(@kinds::text[]) = 0 OR kind = ANY(@kinds::text[]))
  AND (sqlc.narg('at')::timestamptz IS NULL
       OR (at, 0, id) > (sqlc.narg('at')::timestamptz, @source::int, @id::bigint))
ORDER BY at, id
LIMIT @lim;

-- ListTimelineNotes reads the Notes of an Alert Group for the Timeline, newest first with @descending, after a
-- position.
-- name: ListTimelineNotes :many
SELECT id, public_id, created_at, body, actor_kind, actor_user_id, actor_service_account_id, token_name, transport
FROM notes
WHERE org_id = @org_id AND alert_group_id = @alert_group_id
  AND (sqlc.narg('at')::timestamptz IS NULL
       OR (@descending::boolean AND (created_at, 1, id) < (sqlc.narg('at')::timestamptz, @source::int, @id::bigint))
       OR (NOT @descending::boolean
           AND (created_at, 1, id) > (sqlc.narg('at')::timestamptz, @source::int, @id::bigint)))
ORDER BY CASE WHEN @descending::boolean THEN created_at END DESC, CASE WHEN @descending::boolean THEN id END DESC,
         created_at, id
LIMIT @lim;

-- ListTimelineDeliveryEvents reads the delivery events of an Alert Group for the Timeline, newest first with
-- @descending, after a position.
-- name: ListTimelineDeliveryEvents :many
SELECT e.id, e.public_id, e.occurred_at, e.kind, e.loudness, e.mentions, e.error, d.public_id AS destination_public_id,
       d.name AS destination_name
FROM delivery_events e
JOIN destinations d ON d.org_id = e.org_id AND d.id = e.destination_id
WHERE e.org_id = @org_id AND e.alert_group_id = @alert_group_id::bigint
  AND (sqlc.narg('at')::timestamptz IS NULL
       OR (@descending::boolean
           AND (e.occurred_at, 2, e.id) < (sqlc.narg('at')::timestamptz, @source::int, @id::bigint))
       OR (NOT @descending::boolean
           AND (e.occurred_at, 2, e.id) > (sqlc.narg('at')::timestamptz, @source::int, @id::bigint)))
ORDER BY CASE WHEN @descending::boolean THEN e.occurred_at END DESC,
         CASE WHEN @descending::boolean THEN e.id END DESC, e.occurred_at, e.id
LIMIT @lim;

-- CountOpenGroupsByRoute counts the open Alert Groups per Route that is not deleted and status, for
-- muster_alert_groups.
-- name: CountOpenGroupsByRoute :many
SELECT r.public_id, g.status, count(*)::bigint AS count
FROM alert_groups g
JOIN routes r ON r.org_id = g.org_id AND r.id = g.route_id
WHERE g.org_id = @org_id AND g.status <> 'resolved'
GROUP BY r.public_id, g.status;

-- ListRoutePublicIDs lists the Routes that are not deleted, for the zero series of muster_alert_groups.
-- name: ListRoutePublicIDs :many
SELECT public_id
FROM routes
WHERE org_id = @org_id AND deleted_at IS NULL;
