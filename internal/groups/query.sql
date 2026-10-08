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
       last_changed_at, first_acknowledged_at
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
    grace_deadline = sqlc.narg('grace_deadline'), event_seq = @event_seq, last_changed_at = @last_changed_at,
    first_acknowledged_at = sqlc.narg('first_acknowledged_at')
WHERE org_id = @org_id AND id = @id;

-- IsActiveUser reports whether a User can still own an Alert Group: a disabled or deleted Owner has no
-- acknowledgements.
-- name: IsActiveUser :one
SELECT EXISTS (
    SELECT 1
    FROM users
    WHERE org_id = @org_id AND id = @id AND status = 'active'
)::boolean AS active;

-- LockActiveUser locks the row of a User who acknowledges, FOR SHARE, while they are active: a disable or a delete of
-- that User, which releases their acknowledgements, waits for the acknowledgement or makes it refused.
-- name: LockActiveUser :many
SELECT id
FROM users
WHERE org_id = @org_id AND id = @id AND status = 'active'
FOR SHARE;

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

-- InsertNote records a Note with the event_seq of its note_added lifecycle event (C-10.FR-8).
-- name: InsertNote :exec
INSERT INTO notes (
    org_id, public_id, alert_group_id, event_seq, body, actor_kind, actor_user_id, actor_service_account_id,
    api_token_id, token_name, transport, created_at
)
VALUES (
    @org_id, @public_id, @alert_group_id, @event_seq, @body, @actor_kind, sqlc.narg('actor_user_id'),
    sqlc.narg('actor_service_account_id'), sqlc.narg('api_token_id'), sqlc.narg('token_name'), @transport,
    @created_at
);

-- ListJoinedDuringSnooze lists the fingerprints of the Alerts that joined an Alert Group during its current Snooze
-- (C-09.FR-8): after the last Timeline entry that took it into snoozed from another status, and, when that entry is a
-- Reopen into snoozed, the Alerts that reopened it.
-- name: ListJoinedDuringSnooze :many
WITH snoozed AS (
    SELECT t.at, t.event
    FROM timeline_entries t
    WHERE t.org_id = @org_id AND t.alert_group_id = @alert_group_id AND t.to_status = 'snoozed'
      AND t.from_status IS DISTINCT FROM 'snoozed'
    ORDER BY t.at DESC, t.id DESC
    LIMIT 1
)
SELECT DISTINCT a.fingerprint
FROM alert_group_alerts m
JOIN alerts a ON a.org_id = m.org_id AND a.id = m.alert_id
JOIN snoozed s ON m.joined_at > s.at OR (s.event = 'reopened' AND m.joined_at = s.at)
WHERE m.org_id = @org_id AND m.alert_group_id = @alert_group_id
ORDER BY a.fingerprint;

-- ListOwnedGroups lists the Alert Groups a User owns, for the release of a disabled or deleted Owner (C-03.FR-13):
-- acknowledged by them, or resolved by the system inside a Reopen window that would reopen them acknowledged by
-- them.
-- name: ListOwnedGroups :many
SELECT id
FROM alert_groups
WHERE org_id = @org_id
  AND ((status = 'acknowledged' AND owner_user_id = @user_id::bigint)
       OR (status = 'resolved' AND prior_status = 'acknowledged' AND prior_owner_user_id = @user_id::bigint))
ORDER BY id;

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
-- label of its latest Replacement, the Alerts still firing in it, retention.alert_details, its Owner and Snooze, and,
-- when a person resolved it, the open Alert Group of the same Route and key that takes part in grouping (C-10.FR-7),
-- and whether its Route was deleted, which Unresolve needs. Urgency is derived from
-- the Route and organization.critical_is_urgent as they are now (C-08.FR-6), so that marking a Route urgent or
-- changing the setting shows on open Alert Groups at once without changing them. It has a Delivery problem
-- (C-13.FR-12) when a delivery of it is Not delivered or deleted in the messenger, waits for a Broken Destination, or
-- has a Thread not attached.
-- name: GetGroup :one
SELECT g.id, g.public_id, g.number, g.title, g.summary, g.status, g.severity_level,
       (r.urgent OR (g.severity_level = 'critical' AND o.critical_is_urgent))::boolean AS urgent, g.group_key_values,
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
        WHERE m.org_id = g.org_id AND m.alert_group_id = g.id AND m.state = 'firing')::bigint AS still_firing,
       o.retention_alert_details_days, g.owner_user_id, g.snooze_until, g.snoozed_by_user_id,
       g.snoozed_by_service_account_id,
       coalesce(nw.public_id, '')::text AS newer_public_id, coalesce(nw.number, 0)::bigint AS newer_number,
       (r.deleted_at IS NOT NULL)::boolean AS route_deleted, dp.delivery_problem::boolean AS delivery_problem
FROM alert_groups g
JOIN routes r ON r.org_id = g.org_id AND r.id = g.route_id
JOIN organizations o ON o.id = g.org_id
LEFT JOIN LATERAL (SELECT n.public_id, n.number
                   FROM alert_groups n
                   WHERE g.resolved_by_kind = 'user' AND g.moved_from_route_id IS NULL AND n.org_id = g.org_id
                     AND n.route_id = g.route_id AND n.group_key_sha256 = g.group_key_sha256
                     AND n.status <> 'resolved' AND n.moved_from_route_id IS NULL
                   LIMIT 1) nw ON true
JOIN LATERAL (SELECT EXISTS (SELECT 1
                             FROM deliveries d
                             JOIN destinations ds ON ds.org_id = d.org_id AND ds.id = d.destination_id
                             WHERE d.org_id = g.org_id AND d.alert_group_id = g.id
                               AND (d.state IN ('not_delivered', 'deleted_in_messenger')
                                    OR (d.state = 'pending' AND ds.health = 'broken')
                                    OR d.thread_state = 'unattached')) AS delivery_problem) dp ON true
WHERE g.org_id = @org_id AND g.public_id = @public_id;

-- GetGroupID reads the id of an Alert Group by public_id, with when it was resolved and retention.alert_details, which
-- decide whether its details are removed.
-- name: GetGroupID :one
SELECT g.id, g.resolved_at, o.retention_alert_details_days
FROM alert_groups g
JOIN organizations o ON o.id = g.org_id
WHERE g.org_id = @org_id AND g.public_id = @public_id;

-- GetGroupRef names an Alert Group by id: its public_id and #N.
-- name: GetGroupRef :one
SELECT public_id, number
FROM alert_groups
WHERE org_id = @org_id AND id = @id;

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

-- The Alert Group list (C-09.FR-13) reads the summary rows in one of four orders. Every query runs with the plan of its
-- parameters (Store.CustomPlans), so that the filters left unset fold away and the planner picks the index of the
-- filters that are set: the partial open index for the default tab, the started or changed index for a range, the
-- trigram indexes for text, the jsonb_path_ops index for = label Matchers, the unique (org_id, number) for #N. The time
-- range selects lifetimes that overlap it — created_at < to AND (resolved_at IS NULL OR resolved_at >= from), written
-- with the status that the CHECK ties to resolved_at, so that the open index and alert_groups_resolved_idx serve it —
-- and is ignored for a number. Label Matchers other than = with a value are matched in Go on common_labels, after
-- these conditions; urgency and the newer open Alert Group are derived as in GetGroup. The Owner filter, with
-- @owner_set, selects the Alert Groups the User @owner_id owns, or nobody owns when it is null. The Delivery problem
-- (C-13.FR-12) is derived as in GetGroup.

-- ListGroupsStartedDesc reads a batch of the Alert Group list, newest start first, after the cursor when given.
-- name: ListGroupsStartedDesc :many
SELECT g.id, g.public_id, g.number, g.title, g.summary, g.status, g.severity_level,
       (r.urgent OR (g.severity_level = 'critical' AND o.critical_is_urgent))::boolean AS urgent, g.common_labels,
       g.integration_ids, g.reopen_count, g.firing_alert_count, g.resolved_alert_count, g.resolved_at,
       g.resolved_by_kind, g.resolved_by_user_id, g.resolved_by_service_account_id, g.resolve_reason,
       g.resolve_reason_text, g.created_at, g.last_changed_at, r.public_id AS route_public_id, r.name AS route_name,
       g.owner_user_id, g.snooze_until, g.snoozed_by_user_id, g.snoozed_by_service_account_id,
       coalesce(nw.public_id, '')::text AS newer_public_id, coalesce(nw.number, 0)::bigint AS newer_number,
       (r.deleted_at IS NOT NULL)::boolean AS route_deleted, dp.delivery_problem::boolean AS delivery_problem
FROM alert_groups g
JOIN routes r ON r.org_id = g.org_id AND r.id = g.route_id
JOIN organizations o ON o.id = g.org_id
LEFT JOIN LATERAL (SELECT n.public_id, n.number
                   FROM alert_groups n
                   WHERE g.resolved_by_kind = 'user' AND g.moved_from_route_id IS NULL AND n.org_id = g.org_id
                     AND n.route_id = g.route_id AND n.group_key_sha256 = g.group_key_sha256
                     AND n.status <> 'resolved' AND n.moved_from_route_id IS NULL
                   LIMIT 1) nw ON true
JOIN LATERAL (SELECT EXISTS (SELECT 1
                             FROM deliveries d
                             JOIN destinations ds ON ds.org_id = d.org_id AND ds.id = d.destination_id
                             WHERE d.org_id = g.org_id AND d.alert_group_id = g.id
                               AND (d.state IN ('not_delivered', 'deleted_in_messenger')
                                    OR (d.state = 'pending' AND ds.health = 'broken')
                                    OR d.thread_state = 'unattached')) AS delivery_problem) dp ON true
WHERE g.org_id = @org_id AND g.status = ANY(@statuses::text[])
  AND (sqlc.narg('number')::bigint IS NULL OR g.number = sqlc.narg('number')::bigint)
  AND (sqlc.narg('number')::bigint IS NOT NULL
       OR (g.created_at < @range_to::timestamptz
           AND (g.status <> 'resolved' OR (g.status = 'resolved' AND g.resolved_at >= @range_from::timestamptz))))
  AND (cardinality(@route_ids::bigint[]) = 0 OR g.route_id = ANY(@route_ids::bigint[]))
  AND (cardinality(@integration_ids::bigint[]) = 0 OR g.integration_ids && @integration_ids::bigint[])
  AND (cardinality(@severities::text[]) = 0 OR g.severity_level = ANY(@severities::text[]))
  AND (sqlc.narg('urgent')::boolean IS NULL
       OR (r.urgent OR (g.severity_level = 'critical' AND o.critical_is_urgent)) = sqlc.narg('urgent')::boolean)
  AND (sqlc.narg('resolved_by')::text IS NULL OR g.resolved_by_kind = sqlc.narg('resolved_by')::text)
  AND (sqlc.narg('resolve_reason')::text IS NULL OR g.resolve_reason = sqlc.narg('resolve_reason')::text)
  AND (sqlc.narg('reopened')::boolean IS NULL OR (g.reopen_count > 0) = sqlc.narg('reopened')::boolean)
  AND (NOT @owner_set::boolean OR g.owner_user_id IS NOT DISTINCT FROM sqlc.narg('owner_id')::bigint)
  AND (sqlc.narg('snoozed_no_end')::boolean IS NULL
       OR (g.status = 'snoozed' AND g.snooze_no_end) = sqlc.narg('snoozed_no_end')::boolean)
  AND (sqlc.narg('delivery_problem')::boolean IS NULL
       OR dp.delivery_problem = sqlc.narg('delivery_problem')::boolean)
  AND (sqlc.narg('contains')::jsonb IS NULL OR g.common_labels @> sqlc.narg('contains')::jsonb)
  AND (sqlc.narg('pattern')::text IS NULL OR g.title ILIKE sqlc.narg('pattern')::text
       OR g.summary ILIKE sqlc.narg('pattern')::text)
  AND (sqlc.narg('after_at')::timestamptz IS NULL
       OR (g.created_at, g.id) < (sqlc.narg('after_at')::timestamptz, sqlc.narg('after_id')::bigint))
ORDER BY g.created_at DESC, g.id DESC
LIMIT @lim;

-- ListGroupsStartedAsc reads a batch of the Alert Group list, oldest start first, after the cursor when given.
-- name: ListGroupsStartedAsc :many
SELECT g.id, g.public_id, g.number, g.title, g.summary, g.status, g.severity_level,
       (r.urgent OR (g.severity_level = 'critical' AND o.critical_is_urgent))::boolean AS urgent, g.common_labels,
       g.integration_ids, g.reopen_count, g.firing_alert_count, g.resolved_alert_count, g.resolved_at,
       g.resolved_by_kind, g.resolved_by_user_id, g.resolved_by_service_account_id, g.resolve_reason,
       g.resolve_reason_text, g.created_at, g.last_changed_at, r.public_id AS route_public_id, r.name AS route_name,
       g.owner_user_id, g.snooze_until, g.snoozed_by_user_id, g.snoozed_by_service_account_id,
       coalesce(nw.public_id, '')::text AS newer_public_id, coalesce(nw.number, 0)::bigint AS newer_number,
       (r.deleted_at IS NOT NULL)::boolean AS route_deleted, dp.delivery_problem::boolean AS delivery_problem
FROM alert_groups g
JOIN routes r ON r.org_id = g.org_id AND r.id = g.route_id
JOIN organizations o ON o.id = g.org_id
LEFT JOIN LATERAL (SELECT n.public_id, n.number
                   FROM alert_groups n
                   WHERE g.resolved_by_kind = 'user' AND g.moved_from_route_id IS NULL AND n.org_id = g.org_id
                     AND n.route_id = g.route_id AND n.group_key_sha256 = g.group_key_sha256
                     AND n.status <> 'resolved' AND n.moved_from_route_id IS NULL
                   LIMIT 1) nw ON true
JOIN LATERAL (SELECT EXISTS (SELECT 1
                             FROM deliveries d
                             JOIN destinations ds ON ds.org_id = d.org_id AND ds.id = d.destination_id
                             WHERE d.org_id = g.org_id AND d.alert_group_id = g.id
                               AND (d.state IN ('not_delivered', 'deleted_in_messenger')
                                    OR (d.state = 'pending' AND ds.health = 'broken')
                                    OR d.thread_state = 'unattached')) AS delivery_problem) dp ON true
WHERE g.org_id = @org_id AND g.status = ANY(@statuses::text[])
  AND (sqlc.narg('number')::bigint IS NULL OR g.number = sqlc.narg('number')::bigint)
  AND (sqlc.narg('number')::bigint IS NOT NULL
       OR (g.created_at < @range_to::timestamptz
           AND (g.status <> 'resolved' OR (g.status = 'resolved' AND g.resolved_at >= @range_from::timestamptz))))
  AND (cardinality(@route_ids::bigint[]) = 0 OR g.route_id = ANY(@route_ids::bigint[]))
  AND (cardinality(@integration_ids::bigint[]) = 0 OR g.integration_ids && @integration_ids::bigint[])
  AND (cardinality(@severities::text[]) = 0 OR g.severity_level = ANY(@severities::text[]))
  AND (sqlc.narg('urgent')::boolean IS NULL
       OR (r.urgent OR (g.severity_level = 'critical' AND o.critical_is_urgent)) = sqlc.narg('urgent')::boolean)
  AND (sqlc.narg('resolved_by')::text IS NULL OR g.resolved_by_kind = sqlc.narg('resolved_by')::text)
  AND (sqlc.narg('resolve_reason')::text IS NULL OR g.resolve_reason = sqlc.narg('resolve_reason')::text)
  AND (sqlc.narg('reopened')::boolean IS NULL OR (g.reopen_count > 0) = sqlc.narg('reopened')::boolean)
  AND (NOT @owner_set::boolean OR g.owner_user_id IS NOT DISTINCT FROM sqlc.narg('owner_id')::bigint)
  AND (sqlc.narg('snoozed_no_end')::boolean IS NULL
       OR (g.status = 'snoozed' AND g.snooze_no_end) = sqlc.narg('snoozed_no_end')::boolean)
  AND (sqlc.narg('delivery_problem')::boolean IS NULL
       OR dp.delivery_problem = sqlc.narg('delivery_problem')::boolean)
  AND (sqlc.narg('contains')::jsonb IS NULL OR g.common_labels @> sqlc.narg('contains')::jsonb)
  AND (sqlc.narg('pattern')::text IS NULL OR g.title ILIKE sqlc.narg('pattern')::text
       OR g.summary ILIKE sqlc.narg('pattern')::text)
  AND (sqlc.narg('after_at')::timestamptz IS NULL
       OR (g.created_at, g.id) > (sqlc.narg('after_at')::timestamptz, sqlc.narg('after_id')::bigint))
ORDER BY g.created_at, g.id
LIMIT @lim;

-- ListGroupsChangedDesc reads a batch of the Alert Group list, latest change first, after the cursor when given.
-- name: ListGroupsChangedDesc :many
SELECT g.id, g.public_id, g.number, g.title, g.summary, g.status, g.severity_level,
       (r.urgent OR (g.severity_level = 'critical' AND o.critical_is_urgent))::boolean AS urgent, g.common_labels,
       g.integration_ids, g.reopen_count, g.firing_alert_count, g.resolved_alert_count, g.resolved_at,
       g.resolved_by_kind, g.resolved_by_user_id, g.resolved_by_service_account_id, g.resolve_reason,
       g.resolve_reason_text, g.created_at, g.last_changed_at, r.public_id AS route_public_id, r.name AS route_name,
       g.owner_user_id, g.snooze_until, g.snoozed_by_user_id, g.snoozed_by_service_account_id,
       coalesce(nw.public_id, '')::text AS newer_public_id, coalesce(nw.number, 0)::bigint AS newer_number,
       (r.deleted_at IS NOT NULL)::boolean AS route_deleted, dp.delivery_problem::boolean AS delivery_problem
FROM alert_groups g
JOIN routes r ON r.org_id = g.org_id AND r.id = g.route_id
JOIN organizations o ON o.id = g.org_id
LEFT JOIN LATERAL (SELECT n.public_id, n.number
                   FROM alert_groups n
                   WHERE g.resolved_by_kind = 'user' AND g.moved_from_route_id IS NULL AND n.org_id = g.org_id
                     AND n.route_id = g.route_id AND n.group_key_sha256 = g.group_key_sha256
                     AND n.status <> 'resolved' AND n.moved_from_route_id IS NULL
                   LIMIT 1) nw ON true
JOIN LATERAL (SELECT EXISTS (SELECT 1
                             FROM deliveries d
                             JOIN destinations ds ON ds.org_id = d.org_id AND ds.id = d.destination_id
                             WHERE d.org_id = g.org_id AND d.alert_group_id = g.id
                               AND (d.state IN ('not_delivered', 'deleted_in_messenger')
                                    OR (d.state = 'pending' AND ds.health = 'broken')
                                    OR d.thread_state = 'unattached')) AS delivery_problem) dp ON true
WHERE g.org_id = @org_id AND g.status = ANY(@statuses::text[])
  AND (sqlc.narg('number')::bigint IS NULL OR g.number = sqlc.narg('number')::bigint)
  AND (sqlc.narg('number')::bigint IS NOT NULL
       OR (g.created_at < @range_to::timestamptz
           AND (g.status <> 'resolved' OR (g.status = 'resolved' AND g.resolved_at >= @range_from::timestamptz))))
  AND (cardinality(@route_ids::bigint[]) = 0 OR g.route_id = ANY(@route_ids::bigint[]))
  AND (cardinality(@integration_ids::bigint[]) = 0 OR g.integration_ids && @integration_ids::bigint[])
  AND (cardinality(@severities::text[]) = 0 OR g.severity_level = ANY(@severities::text[]))
  AND (sqlc.narg('urgent')::boolean IS NULL
       OR (r.urgent OR (g.severity_level = 'critical' AND o.critical_is_urgent)) = sqlc.narg('urgent')::boolean)
  AND (sqlc.narg('resolved_by')::text IS NULL OR g.resolved_by_kind = sqlc.narg('resolved_by')::text)
  AND (sqlc.narg('resolve_reason')::text IS NULL OR g.resolve_reason = sqlc.narg('resolve_reason')::text)
  AND (sqlc.narg('reopened')::boolean IS NULL OR (g.reopen_count > 0) = sqlc.narg('reopened')::boolean)
  AND (NOT @owner_set::boolean OR g.owner_user_id IS NOT DISTINCT FROM sqlc.narg('owner_id')::bigint)
  AND (sqlc.narg('snoozed_no_end')::boolean IS NULL
       OR (g.status = 'snoozed' AND g.snooze_no_end) = sqlc.narg('snoozed_no_end')::boolean)
  AND (sqlc.narg('delivery_problem')::boolean IS NULL
       OR dp.delivery_problem = sqlc.narg('delivery_problem')::boolean)
  AND (sqlc.narg('contains')::jsonb IS NULL OR g.common_labels @> sqlc.narg('contains')::jsonb)
  AND (sqlc.narg('pattern')::text IS NULL OR g.title ILIKE sqlc.narg('pattern')::text
       OR g.summary ILIKE sqlc.narg('pattern')::text)
  AND (sqlc.narg('after_at')::timestamptz IS NULL
       OR (g.last_changed_at, g.id) < (sqlc.narg('after_at')::timestamptz, sqlc.narg('after_id')::bigint))
ORDER BY g.last_changed_at DESC, g.id DESC
LIMIT @lim;

-- ListGroupsChangedAsc reads a batch of the Alert Group list, earliest change first, after the cursor when given.
-- name: ListGroupsChangedAsc :many
SELECT g.id, g.public_id, g.number, g.title, g.summary, g.status, g.severity_level,
       (r.urgent OR (g.severity_level = 'critical' AND o.critical_is_urgent))::boolean AS urgent, g.common_labels,
       g.integration_ids, g.reopen_count, g.firing_alert_count, g.resolved_alert_count, g.resolved_at,
       g.resolved_by_kind, g.resolved_by_user_id, g.resolved_by_service_account_id, g.resolve_reason,
       g.resolve_reason_text, g.created_at, g.last_changed_at, r.public_id AS route_public_id, r.name AS route_name,
       g.owner_user_id, g.snooze_until, g.snoozed_by_user_id, g.snoozed_by_service_account_id,
       coalesce(nw.public_id, '')::text AS newer_public_id, coalesce(nw.number, 0)::bigint AS newer_number,
       (r.deleted_at IS NOT NULL)::boolean AS route_deleted, dp.delivery_problem::boolean AS delivery_problem
FROM alert_groups g
JOIN routes r ON r.org_id = g.org_id AND r.id = g.route_id
JOIN organizations o ON o.id = g.org_id
LEFT JOIN LATERAL (SELECT n.public_id, n.number
                   FROM alert_groups n
                   WHERE g.resolved_by_kind = 'user' AND g.moved_from_route_id IS NULL AND n.org_id = g.org_id
                     AND n.route_id = g.route_id AND n.group_key_sha256 = g.group_key_sha256
                     AND n.status <> 'resolved' AND n.moved_from_route_id IS NULL
                   LIMIT 1) nw ON true
JOIN LATERAL (SELECT EXISTS (SELECT 1
                             FROM deliveries d
                             JOIN destinations ds ON ds.org_id = d.org_id AND ds.id = d.destination_id
                             WHERE d.org_id = g.org_id AND d.alert_group_id = g.id
                               AND (d.state IN ('not_delivered', 'deleted_in_messenger')
                                    OR (d.state = 'pending' AND ds.health = 'broken')
                                    OR d.thread_state = 'unattached')) AS delivery_problem) dp ON true
WHERE g.org_id = @org_id AND g.status = ANY(@statuses::text[])
  AND (sqlc.narg('number')::bigint IS NULL OR g.number = sqlc.narg('number')::bigint)
  AND (sqlc.narg('number')::bigint IS NOT NULL
       OR (g.created_at < @range_to::timestamptz
           AND (g.status <> 'resolved' OR (g.status = 'resolved' AND g.resolved_at >= @range_from::timestamptz))))
  AND (cardinality(@route_ids::bigint[]) = 0 OR g.route_id = ANY(@route_ids::bigint[]))
  AND (cardinality(@integration_ids::bigint[]) = 0 OR g.integration_ids && @integration_ids::bigint[])
  AND (cardinality(@severities::text[]) = 0 OR g.severity_level = ANY(@severities::text[]))
  AND (sqlc.narg('urgent')::boolean IS NULL
       OR (r.urgent OR (g.severity_level = 'critical' AND o.critical_is_urgent)) = sqlc.narg('urgent')::boolean)
  AND (sqlc.narg('resolved_by')::text IS NULL OR g.resolved_by_kind = sqlc.narg('resolved_by')::text)
  AND (sqlc.narg('resolve_reason')::text IS NULL OR g.resolve_reason = sqlc.narg('resolve_reason')::text)
  AND (sqlc.narg('reopened')::boolean IS NULL OR (g.reopen_count > 0) = sqlc.narg('reopened')::boolean)
  AND (NOT @owner_set::boolean OR g.owner_user_id IS NOT DISTINCT FROM sqlc.narg('owner_id')::bigint)
  AND (sqlc.narg('snoozed_no_end')::boolean IS NULL
       OR (g.status = 'snoozed' AND g.snooze_no_end) = sqlc.narg('snoozed_no_end')::boolean)
  AND (sqlc.narg('delivery_problem')::boolean IS NULL
       OR dp.delivery_problem = sqlc.narg('delivery_problem')::boolean)
  AND (sqlc.narg('contains')::jsonb IS NULL OR g.common_labels @> sqlc.narg('contains')::jsonb)
  AND (sqlc.narg('pattern')::text IS NULL OR g.title ILIKE sqlc.narg('pattern')::text
       OR g.summary ILIKE sqlc.narg('pattern')::text)
  AND (sqlc.narg('after_at')::timestamptz IS NULL
       OR (g.last_changed_at, g.id) > (sqlc.narg('after_at')::timestamptz, sqlc.narg('after_id')::bigint))
ORDER BY g.last_changed_at, g.id
LIMIT @lim;

-- CountGroups counts the Alert Groups of the list's filters per status, without the status and the cursor; with
-- @with_labels it groups them by common_labels too, for the Matchers that Go applies.
-- name: CountGroups :many
SELECT g.status, (CASE WHEN @with_labels::boolean THEN g.common_labels END)::jsonb AS common_labels,
       count(*)::bigint AS count
FROM alert_groups g
JOIN routes r ON r.org_id = g.org_id AND r.id = g.route_id
JOIN organizations o ON o.id = g.org_id
JOIN LATERAL (SELECT EXISTS (SELECT 1
                             FROM deliveries d
                             JOIN destinations ds ON ds.org_id = d.org_id AND ds.id = d.destination_id
                             WHERE d.org_id = g.org_id AND d.alert_group_id = g.id
                               AND (d.state IN ('not_delivered', 'deleted_in_messenger')
                                    OR (d.state = 'pending' AND ds.health = 'broken')
                                    OR d.thread_state = 'unattached')) AS delivery_problem) dp ON true
WHERE g.org_id = @org_id
  AND (sqlc.narg('number')::bigint IS NULL OR g.number = sqlc.narg('number')::bigint)
  AND (sqlc.narg('number')::bigint IS NOT NULL
       OR (g.created_at < @range_to::timestamptz
           AND (g.status <> 'resolved' OR (g.status = 'resolved' AND g.resolved_at >= @range_from::timestamptz))))
  AND (cardinality(@route_ids::bigint[]) = 0 OR g.route_id = ANY(@route_ids::bigint[]))
  AND (cardinality(@integration_ids::bigint[]) = 0 OR g.integration_ids && @integration_ids::bigint[])
  AND (cardinality(@severities::text[]) = 0 OR g.severity_level = ANY(@severities::text[]))
  AND (sqlc.narg('urgent')::boolean IS NULL
       OR (r.urgent OR (g.severity_level = 'critical' AND o.critical_is_urgent)) = sqlc.narg('urgent')::boolean)
  AND (sqlc.narg('resolved_by')::text IS NULL OR g.resolved_by_kind = sqlc.narg('resolved_by')::text)
  AND (sqlc.narg('resolve_reason')::text IS NULL OR g.resolve_reason = sqlc.narg('resolve_reason')::text)
  AND (sqlc.narg('reopened')::boolean IS NULL OR (g.reopen_count > 0) = sqlc.narg('reopened')::boolean)
  AND (NOT @owner_set::boolean OR g.owner_user_id IS NOT DISTINCT FROM sqlc.narg('owner_id')::bigint)
  AND (sqlc.narg('snoozed_no_end')::boolean IS NULL
       OR (g.status = 'snoozed' AND g.snooze_no_end) = sqlc.narg('snoozed_no_end')::boolean)
  AND (sqlc.narg('delivery_problem')::boolean IS NULL
       OR dp.delivery_problem = sqlc.narg('delivery_problem')::boolean)
  AND (sqlc.narg('contains')::jsonb IS NULL OR g.common_labels @> sqlc.narg('contains')::jsonb)
  AND (sqlc.narg('pattern')::text IS NULL OR g.title ILIKE sqlc.narg('pattern')::text
       OR g.summary ILIKE sqlc.narg('pattern')::text)
GROUP BY g.status, (CASE WHEN @with_labels::boolean THEN g.common_labels END);

-- GetListSettings reads what the list and the reads of one Alert Group need of the Organization: retention.alert_details
-- and organization.time_zone.
-- name: GetListSettings :one
SELECT retention_alert_details_days, time_zone
FROM organizations
WHERE id = @org_id;

-- ListRoutesByPublicID names Routes, deleted ones included, by public_id: the Route filter of the list and of the
-- statistics.
-- name: ListRoutesByPublicID :many
SELECT id, public_id, name
FROM routes
WHERE org_id = @org_id AND public_id = ANY(@public_ids::text[]);

-- ListUsersByPublicID names Users, deleted ones included, by public_id: the Owner filter of the list.
-- name: ListUsersByPublicID :many
SELECT id, public_id, name
FROM users
WHERE org_id = @org_id AND public_id = ANY(@public_ids::text[]);

-- ListIntegrationsByPublicID names Integrations, deleted ones included, by public_id.
-- name: ListIntegrationsByPublicID :many
SELECT id, public_id, name
FROM integrations
WHERE org_id = @org_id AND public_id = ANY(@public_ids::text[]);

-- GetGroupKey reads the Route and the Group key of an Alert Group, for its related Alert Groups.
-- name: GetGroupKey :one
SELECT id, route_id, group_key_sha256
FROM alert_groups
WHERE org_id = @org_id AND public_id = @public_id;

-- ListRelatedGroups lists the other Alert Groups of a Route with the same Group key values, newest first, after the
-- cursor when given (C-09.FR-20; alert_groups_related_idx).
-- name: ListRelatedGroups :many
SELECT g.id, g.public_id, g.number, g.status, g.created_at, g.resolved_at, g.resolved_by_kind, g.resolved_by_user_id,
       g.resolved_by_service_account_id, g.resolve_reason, g.resolve_reason_text
FROM alert_groups g
WHERE g.org_id = @org_id AND g.route_id = @route_id AND g.group_key_sha256 = @group_key_sha256 AND g.id <> @id
  AND (sqlc.narg('after_at')::timestamptz IS NULL
       OR (g.created_at, g.id) < (sqlc.narg('after_at')::timestamptz, sqlc.narg('after_id')::bigint))
ORDER BY g.created_at DESC, g.id DESC
LIMIT @lim;

-- The statistics (C-09.FR-15) aggregate the summary rows that started in [@range_from, @range_to): per subject, and
-- per subject and day of start in the time zone @time_zone. Time to resolve counts the resolved ones, time to
-- acknowledge those acknowledged at least once; both are seconds, their median and 95th percentile interpolated, and 0
-- when their count is 0.

-- RouteStatistics aggregates the Alert Groups per Route, only the Routes @route_ids when set.
-- name: RouteStatistics :many
SELECT s.subject_id, s.day, (GROUPING(s.day) = 1)::boolean AS total, count(*)::bigint AS alert_group_count,
       count(s.resolve)::bigint AS resolve_count,
       coalesce(percentile_cont(0.5) WITHIN GROUP (ORDER BY s.resolve), 0)::float8 AS resolve_median,
       coalesce(percentile_cont(0.95) WITHIN GROUP (ORDER BY s.resolve), 0)::float8 AS resolve_p95,
       count(s.ack)::bigint AS ack_count,
       coalesce(percentile_cont(0.5) WITHIN GROUP (ORDER BY s.ack), 0)::float8 AS ack_median,
       coalesce(percentile_cont(0.95) WITHIN GROUP (ORDER BY s.ack), 0)::float8 AS ack_p95
FROM (SELECT g.route_id AS subject_id, (g.created_at AT TIME ZONE @time_zone::text)::date AS day,
             extract(epoch FROM g.resolved_at - g.created_at)::float8 AS resolve,
             extract(epoch FROM g.first_acknowledged_at - g.created_at)::float8 AS ack
      FROM alert_groups g
      WHERE g.org_id = @org_id AND g.created_at >= @range_from::timestamptz AND g.created_at < @range_to::timestamptz
        AND (cardinality(@route_ids::bigint[]) = 0 OR g.route_id = ANY(@route_ids::bigint[]))) AS s
GROUP BY GROUPING SETS ((s.subject_id), (s.subject_id, s.day))
ORDER BY s.subject_id, s.day NULLS FIRST;

-- IntegrationStatistics aggregates the Alert Groups per Integration — one with Alerts from several counts for each —
-- only the Integrations @integration_ids when set.
-- name: IntegrationStatistics :many
SELECT s.subject_id, s.day, (GROUPING(s.day) = 1)::boolean AS total, count(*)::bigint AS alert_group_count,
       count(s.resolve)::bigint AS resolve_count,
       coalesce(percentile_cont(0.5) WITHIN GROUP (ORDER BY s.resolve), 0)::float8 AS resolve_median,
       coalesce(percentile_cont(0.95) WITHIN GROUP (ORDER BY s.resolve), 0)::float8 AS resolve_p95,
       count(s.ack)::bigint AS ack_count,
       coalesce(percentile_cont(0.5) WITHIN GROUP (ORDER BY s.ack), 0)::float8 AS ack_median,
       coalesce(percentile_cont(0.95) WITHIN GROUP (ORDER BY s.ack), 0)::float8 AS ack_p95
FROM (SELECT i.integration_id::bigint AS subject_id, (g.created_at AT TIME ZONE @time_zone::text)::date AS day,
             extract(epoch FROM g.resolved_at - g.created_at)::float8 AS resolve,
             extract(epoch FROM g.first_acknowledged_at - g.created_at)::float8 AS ack
      FROM alert_groups g
      CROSS JOIN LATERAL unnest(g.integration_ids) AS i(integration_id)
      WHERE g.org_id = @org_id AND g.created_at >= @range_from::timestamptz AND g.created_at < @range_to::timestamptz
        AND (cardinality(@integration_ids::bigint[]) = 0 OR i.integration_id = ANY(@integration_ids::bigint[]))) AS s
GROUP BY GROUPING SETS ((s.subject_id), (s.subject_id, s.day))
ORDER BY s.subject_id, s.day NULLS FIRST;

-- ListStatisticsRoutes lists the subjects of the statistics per Route: those that are not deleted and @with_ids (the
-- deleted ones with Alert Groups in the period), or only @only_ids when set.
-- name: ListStatisticsRoutes :many
SELECT id, public_id, name
FROM routes
WHERE org_id = @org_id
  AND (CASE WHEN cardinality(@only_ids::bigint[]) > 0 THEN id = ANY(@only_ids::bigint[])
            ELSE deleted_at IS NULL OR id = ANY(@with_ids::bigint[]) END)
ORDER BY name, id;

-- ListStatisticsIntegrations lists the subjects of the statistics per Integration, as ListStatisticsRoutes does.
-- name: ListStatisticsIntegrations :many
SELECT id, public_id, name
FROM integrations
WHERE org_id = @org_id
  AND (CASE WHEN cardinality(@only_ids::bigint[]) > 0 THEN id = ANY(@only_ids::bigint[])
            ELSE deleted_at IS NULL OR id = ANY(@with_ids::bigint[]) END)
ORDER BY name, id;

-- CountOpenGroupsByIntegration counts the open Alert Groups with an Alert from each of the Integrations
-- (C-09.FR-21): those that deleting it would resolve.
-- name: CountOpenGroupsByIntegration :many
SELECT i.public_id, count(*)::bigint AS count
FROM alert_groups g
CROSS JOIN LATERAL unnest(g.integration_ids) AS u(integration_id)
JOIN integrations i ON i.org_id = g.org_id AND i.id = u.integration_id
WHERE g.org_id = @org_id AND g.status <> 'resolved' AND i.public_id = ANY(@public_ids::text[])
GROUP BY i.public_id;

-- Retention (C-09.FR-16; design/db/schema.md §6) deletes in batches on the Leader, the rows that already qualify only,
-- so that running it twice deletes nothing more. Details go first: the Alerts inside Alert Groups that ended
-- retention.alert_details ago; then the summary rows resolved retention.alert_group_summaries ago, with their Notes,
-- timers, deliveries and queue rows by cascade.

-- GetRetentionPeriods reads retention.alert_details and retention.alert_group_summaries, in days.
-- name: GetRetentionPeriods :one
SELECT retention_alert_details_days, retention_alert_group_summaries_days
FROM organizations
WHERE id = @org_id;

-- DeleteExpiredMemberships deletes at most @batch_size Alerts inside Alert Groups that ended before @cutoff; rows
-- another transaction holds wait for the next run.
-- name: DeleteExpiredMemberships :execrows
DELETE FROM alert_group_alerts m
WHERE m.org_id = @org_id AND m.id IN (
    SELECT e.id
    FROM alert_group_alerts e
    WHERE e.org_id = @org_id AND e.state <> 'firing' AND e.ended_at < @cutoff::timestamptz
    ORDER BY e.ended_at
    LIMIT @batch_size
    FOR UPDATE SKIP LOCKED);

-- DeleteExpiredGroups deletes at most @batch_size summary rows resolved before @cutoff. A summary row that an Alert
-- inside another Alert Group still names as where it moved waits for the details of that one to go first; a later
-- Alert Group that fires again after it keeps its row, without the reference (ON DELETE SET NULL).
-- name: DeleteExpiredGroups :execrows
DELETE FROM alert_groups g
WHERE g.org_id = @org_id AND g.id IN (
    SELECT e.id
    FROM alert_groups e
    WHERE e.org_id = @org_id AND e.status = 'resolved' AND e.resolved_at < @cutoff::timestamptz
      AND NOT EXISTS (SELECT 1
                      FROM alert_group_alerts m
                      WHERE m.org_id = @org_id AND m.moved_to_alert_group_id = e.id)
    ORDER BY e.resolved_at
    LIMIT @batch_size
    FOR UPDATE SKIP LOCKED);

-- GetSnapshotReceivedAt reads when the Stored Snapshot behind a change was received, for the latency of its delivery.
-- name: GetSnapshotReceivedAt :one
SELECT received_at
FROM stored_snapshots
WHERE org_id = @org_id AND id = @id;
