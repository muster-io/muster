-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- Delivery (C-11, ADR-0005, schema.md §4.11 and §5): the Desired state per Alert Group and Destination, its
-- reconciliation by the delivery worker, Thread replies, the shared limiter buckets and the delivery events. Only this
-- package writes deliveries, thread_replies, rate_limit_buckets and delivery_events. Due times are business times and
-- leases real times; both come from Go.

-- ListRouteDestinations lists the Destinations of a Route that are not deleted, with their health, in id order, for
-- Enqueue.
-- name: ListRouteDestinations :many
SELECT d.id, d.public_id, d.name, d.type, d.connection_id, d.health
FROM route_destinations rd
JOIN destinations d ON d.org_id = rd.org_id AND d.id = rd.destination_id
WHERE rd.org_id = @org_id AND rd.route_id = @route_id AND d.deleted_at IS NULL
ORDER BY d.id;

-- ShareRouteMembership takes, until the transaction ends, the shared membership lock of a Route, which a change of its
-- Destinations takes exclusively (routing's LockRouteMembership, class db.RouteMembershipLockClass): an Enqueue and a
-- change of the Route's Destinations serialize, so that neither misses a delivery the other creates or retires. An
-- advisory lock, not a lock of the Route's row: the move of open Alert Groups to the Default route holds that row and
-- then locks the Alert Groups, which a Command holds before its Enqueue.
-- name: ShareRouteMembership :exec
SELECT pg_advisory_xact_lock_shared(@lock_class::int, hashint8(@route_id::bigint));

-- GetRouteDelivery reads what delivery needs of a Route, deleted or not: its public_id and name, its language, its
-- Thread batching window and its Storm threshold.
-- name: GetRouteDelivery :one
SELECT public_id, name, language, thread_batching_window_seconds, storm_threshold
FROM routes
WHERE org_id = @org_id AND id = @id;

-- EnsureDelivery creates the delivery of an Alert Group and a Destination when it is missing, held by the Storm
-- @held_by_storm_id when one stands for it, and locks it either way; inserted tells a new one, state whether it ended
-- and held_by_storm_id whether a Storm holds it.
-- name: EnsureDelivery :one
INSERT INTO deliveries (org_id, destination_id, alert_group_id, held_by_storm_id, state, urgent, publication_loud,
                        next_attempt_at, created_at, updated_at)
VALUES (@org_id, @destination_id, @alert_group_id, sqlc.narg('held_by_storm_id')::bigint, 'pending', @urgent,
        sqlc.narg('publication_loud')::boolean, @now, @now, @now)
ON CONFLICT (alert_group_id, destination_id) WHERE alert_group_id IS NOT NULL
DO UPDATE SET updated_at = deliveries.updated_at
RETURNING id, desired_version, desired_hash, thread_batch_until, state, held_by_storm_id, (xmax = 0)::boolean AS inserted;

-- SetDesired stores a new Desired state: the next version, its text, payload and hash, the key that signed its
-- buttons, the receipt time of the oldest Snapshot it carries that is not delivered yet, and makes the delivery due
-- now, and returns the state it leaves. A delivery deleted in the messenger or retired stays so; a withheld one stays
-- so unless its Alert Group is open again (@open), when it is published after all, Loud when it is @firing
-- (C-11.FR-19); a Not delivered one starts a new delivery (C-11.FR-10). An open Alert Group carries no late note: a late Publication whose Alert Group opened again
-- before it was made loses the note and takes the loudness of the recovery rule, Loud when @firing (C-11.FR-11).
-- name: SetDesired :one
UPDATE deliveries
SET desired_version     = desired_version + 1,
    desired_text        = @desired_text::text,
    desired_payload     = @desired_payload,
    desired_hash        = @desired_hash,
    desired_button_key_id = sqlc.narg('button_key_id')::text,
    desired_received_at = coalesce(desired_received_at, sqlc.narg('received_at')::timestamptz),
    state               = CASE
                              WHEN state IN ('deleted_in_messenger', 'retired') THEN state
                              WHEN state = 'withheld' AND NOT @open::boolean THEN state
                              ELSE 'pending'
                          END,
    publication_loud    = CASE
                              WHEN (state = 'withheld' OR (late_note AND message_id IS NULL)) AND @open::boolean
                                  THEN @firing::boolean
                              ELSE publication_loud
                          END,
    late_note           = CASE WHEN @open::boolean THEN false ELSE late_note END,
    urgent              = @urgent,
    next_attempt_at     = @now,
    updated_at          = @now
WHERE org_id = @org_id AND id = @id
RETURNING state;

-- SetDeliveryUrgent follows the urgency of the Alert Group when its message did not change.
-- name: SetDeliveryUrgent :exec
UPDATE deliveries
SET urgent = @urgent, updated_at = @now
WHERE org_id = @org_id AND id = @id AND urgent <> @urgent;

-- SetThreadBatchUntil opens or moves the Thread batching window of a delivery.
-- name: SetThreadBatchUntil :exec
UPDATE deliveries
SET thread_batch_until = @until::timestamptz, updated_at = @now
WHERE org_id = @org_id AND id = @id;

-- InsertThreadReply queues a Thread reply, due at @due: pending, or dropped while its Destination is Broken.
-- name: InsertThreadReply :exec
INSERT INTO thread_replies (org_id, delivery_id, alert_group_id, destination_id, event, event_seqs, loudness,
                            mentions, fingerprints, state, next_attempt_at, created_at)
VALUES (@org_id, @delivery_id, @alert_group_id, @destination_id, @event, @event_seqs::bigint[], @loudness,
        @mentions::text[], @fingerprints::text[], @state::text, @due, @now);

-- CollectAlerts adds new Alerts to the open batch of a delivery, due when its Thread batching window closes, creating
-- the batch when there is none; inserted tells a new batch. A batch takes the loudness and Mentions of the loudest
-- event it carries, and keeps its due time.
-- name: CollectAlerts :one
INSERT INTO thread_replies (org_id, delivery_id, alert_group_id, destination_id, event, event_seqs, loudness,
                            mentions, fingerprints, state, next_attempt_at, created_at)
VALUES (@org_id, @delivery_id, @alert_group_id, @destination_id, 'alerts_added', @event_seqs::bigint[], @loudness,
        @mentions::text[], @fingerprints::text[], 'collecting', @due, @now)
ON CONFLICT (delivery_id) WHERE state = 'collecting'
DO UPDATE SET event_seqs   = thread_replies.event_seqs || excluded.event_seqs,
              fingerprints = ARRAY(SELECT u.f
                                   FROM unnest(thread_replies.fingerprints || excluded.fingerprints)
                                        WITH ORDINALITY AS u(f, n)
                                   GROUP BY u.f
                                   ORDER BY min(u.n)),
              mentions     = CASE
                                 WHEN excluded.loudness = thread_replies.loudness
                                     THEN ARRAY(SELECT DISTINCT m FROM unnest(thread_replies.mentions || excluded.mentions) AS m ORDER BY m)
                                 WHEN excluded.loudness = 'loud' THEN excluded.mentions
                                 ELSE thread_replies.mentions
                             END,
              loudness     = CASE WHEN excluded.loudness = 'loud' THEN 'loud' ELSE thread_replies.loudness END
RETURNING (xmax = 0)::boolean AS inserted;

-- NotifyDelivery wakes the delivery workers of every replica once the transaction commits.
-- name: NotifyDelivery :exec
SELECT pg_notify(@channel::text, '');

-- ClaimDueDeliveries leases the due pending deliveries of healthy Destinations whose lease is free or ran out, Urgent
-- first, then the earliest, then the one delivered longest ago, so that deliveries waiting for the same token take
-- turns; another claimer skips the rows this one locked. A delivery a Storm holds waits for its Storm, and one of a
-- deleted Destination is claimed only for its final edit. The choice is a materialized CTE, run once: as a subquery in
-- FROM, PostgreSQL may plan it as the inner side of a nested loop over every delivery, run once per row.
-- name: ClaimDueDeliveries :many
WITH due AS MATERIALIZED (
    SELECT x.id
    FROM deliveries x
    JOIN destinations ds ON ds.org_id = x.org_id AND ds.id = x.destination_id
    WHERE x.org_id = @org_id AND x.state = 'pending' AND x.next_attempt_at <= @due::timestamptz
      AND (x.lease_until IS NULL OR x.lease_until <= @now::timestamptz) AND ds.health = 'healthy'
      AND x.held_by_storm_id IS NULL AND (ds.deleted_at IS NULL OR x.desired_retire)
    ORDER BY x.urgent DESC, x.next_attempt_at, x.last_delivered_at NULLS FIRST, x.id
    LIMIT @lim
    FOR UPDATE OF x SKIP LOCKED
)
UPDATE deliveries d
SET lease_owner = @owner::text, lease_until = @lease_until::timestamptz
FROM due
WHERE d.org_id = @org_id AND d.id = due.id
RETURNING d.id, d.urgent, d.next_attempt_at, d.last_delivered_at;

-- GetLeasedDelivery reads, and locks, a delivery whose lease this replica still holds at the real time now: its latest
-- Desired state, whether its next call is the final edit, its actual message, its notes, its Destination with its
-- health and its Alert Group, or its Storm for a Storm summary, whose Alert Group fields are empty. No row when the
-- lease ran out or went to another replica.
-- name: GetLeasedDelivery :one
SELECT d.id, coalesce(d.alert_group_id, 0)::bigint AS alert_group_id, d.storm_id, d.desired_version,
       d.desired_payload, d.desired_hash, d.desired_received_at, d.desired_retire, d.publication_loud, d.late_note,
       d.republished_after_delete, d.actual_hash, d.message_id, d.message_url, d.publication_started_at, d.attempts,
       ds.id AS destination_id, ds.public_id AS destination_public_id, ds.name AS destination_name,
       ds.type AS destination_type, ds.connection_id, ds.health AS destination_health,
       coalesce(g.public_id, '')::text AS alert_group_public_id, coalesce(g.number, 0)::bigint AS number,
       coalesce(g.status, '')::text AS group_status, coalesce(g.created_at, d.created_at)::timestamptz AS group_created_at,
       g.resolved_at AS group_resolved_at
FROM deliveries d
JOIN destinations ds ON ds.org_id = d.org_id AND ds.id = d.destination_id
LEFT JOIN alert_groups g ON g.org_id = d.org_id AND g.id = d.alert_group_id
WHERE d.org_id = @org_id AND d.id = @id AND d.lease_owner = @owner::text AND d.lease_until > @now::timestamptz
FOR UPDATE OF d;

-- RenewDeliveryLease extends the lease of a delivery this replica holds before its call, so that the call is made
-- under a whole lease.
-- name: RenewDeliveryLease :exec
UPDATE deliveries
SET lease_until = @lease_until::timestamptz
WHERE org_id = @org_id AND id = @id AND lease_owner = @owner::text;

-- MarkDelivered records that the actual message already shows the desired version: nothing to call.
-- name: MarkDelivered :exec
UPDATE deliveries
SET state = 'delivered', desired_received_at = NULL, lease_owner = NULL, lease_until = NULL, updated_at = @now
WHERE org_id = @org_id AND id = @id AND lease_owner = @owner::text AND desired_version = @version;

-- RescheduleDelivery releases a delivery that waits for its limiter tokens until @at, without failing it.
-- name: RescheduleDelivery :exec
UPDATE deliveries
SET next_attempt_at = @at, lease_owner = NULL, lease_until = NULL, updated_at = @now
WHERE org_id = @org_id AND id = @id AND lease_owner = @owner::text;

-- StartPublication records, before the call, that a Publication started; a retry after it publishes again.
-- name: StartPublication :exec
UPDATE deliveries
SET publication_started_at = @now::timestamptz, updated_at = @now::timestamptz
WHERE org_id = @org_id AND id = @id AND lease_owner = @owner::text;

-- RecordDelivered records a successful call that brought the actual message to @version: the delivery is delivered
-- unless the Desired state grew meanwhile, or its Destination left the Route while the call was made, in which cases it
-- stays pending and the next call carries the newest state or the final edit.
-- The delivered version clears the receipt time, so a later call never observes a Snapshot already delivered.
-- A row that ended while the call was in flight — withheld, retired or deleted in the messenger — stays so, with the
-- actual message recorded, unless the call was a Publication that created a message on a row that had none (raced):
-- a Storm summary is then retired with it; an Alert Group whose Destination was deleted or left its Route meanwhile
-- gets the final edit of that message (pending, desired_retire); any other row follows the rule above, so that the
-- message that now exists is kept current.
-- name: RecordDelivered :one
UPDATE deliveries d
SET actual_version      = @version::bigint,
    actual_hash         = @hash,
    message_id          = coalesce(sqlc.narg('message_id')::text, d.message_id),
    message_url         = coalesce(sqlc.narg('message_url')::text, d.message_url),
    published_at        = CASE WHEN @published::boolean THEN coalesce(d.published_at, @now::timestamptz) ELSE d.published_at END,
    publications        = d.publications + CASE WHEN @published::boolean THEN 1 ELSE 0 END,
    last_delivered_at   = @now::timestamptz,
    state               = CASE
                              WHEN f.ended AND NOT f.raced THEN d.state
                              WHEN f.raced AND d.storm_id IS NOT NULL THEN 'retired'
                              WHEN f.raced AND f.left_destination THEN 'pending'
                              WHEN d.desired_version = @version::bigint AND NOT d.desired_retire THEN 'delivered'
                              ELSE 'pending'
                          END,
    desired_retire      = d.desired_retire OR (f.raced AND d.storm_id IS NULL AND f.left_destination),
    desired_received_at = NULL,
    next_attempt_at     = @now::timestamptz,
    attempts            = 0,
    first_failed_at     = NULL,
    last_error_class    = NULL,
    last_error          = NULL,
    lease_owner         = NULL,
    lease_until         = NULL,
    updated_at          = @now::timestamptz
FROM (SELECT x.id,
             x.state IN ('withheld', 'retired', 'deleted_in_messenger') AS ended,
             (x.state IN ('withheld', 'retired', 'deleted_in_messenger') AND x.message_id IS NULL
              AND sqlc.narg('message_id')::text IS NOT NULL) AS raced,
             (EXISTS (SELECT 1
                      FROM destinations ds
                      WHERE ds.org_id = x.org_id AND ds.id = x.destination_id AND ds.deleted_at IS NOT NULL)
              OR NOT EXISTS (SELECT 1
                             FROM alert_groups g
                             JOIN route_destinations rd ON rd.org_id = g.org_id AND rd.route_id = g.route_id
                             WHERE g.org_id = x.org_id AND g.id = x.alert_group_id
                               AND rd.destination_id = x.destination_id)) AS left_destination
      FROM deliveries x
      WHERE x.org_id = @org_id AND x.id = @id) AS f
WHERE d.org_id = @org_id AND d.id = f.id AND d.lease_owner = @owner::text
RETURNING d.state;

-- RecordDeliveryRetry records an outcome that is retried at @at: a RetryAfter or a Fatal error, which leave the
-- budget untouched, or a Transient error, which is @counted: attempts grows and the time budget starts. The messenger
-- did not take a Publication, so its start is cleared. No row when the lease went to another replica.
-- name: RecordDeliveryRetry :one
UPDATE deliveries
SET next_attempt_at        = @at,
    attempts               = attempts + CASE WHEN @counted::boolean THEN 1 ELSE 0 END,
    first_failed_at        = CASE WHEN @counted::boolean THEN coalesce(first_failed_at, @now) ELSE first_failed_at END,
    publication_started_at = CASE WHEN message_id IS NULL THEN NULL ELSE publication_started_at END,
    last_error_class       = sqlc.narg('error_class')::text,
    last_error             = sqlc.narg('error')::text,
    lease_owner            = NULL,
    lease_until            = NULL,
    updated_at             = @now
WHERE org_id = @org_id AND id = @id AND lease_owner = @owner::text
RETURNING attempts, first_failed_at;

-- RecordNotDelivered ends a delivery as Not delivered with the error class and the masked error (C-11.FR-10); a later
-- change of its Desired state makes it pending again. A final edit that was due ends with it, and a row that ended
-- while the call was in flight stays so. No row when the lease went to another replica.
-- name: RecordNotDelivered :one
UPDATE deliveries
SET state                  = CASE
                                 WHEN state IN ('withheld', 'retired', 'deleted_in_messenger') THEN state
                                 ELSE 'not_delivered'
                             END,
    desired_retire         = false,
    attempts               = 0,
    first_failed_at        = NULL,
    publication_started_at = CASE WHEN message_id IS NULL THEN NULL ELSE publication_started_at END,
    last_error_class       = @error_class::text,
    last_error             = @error::text,
    lease_owner            = NULL,
    lease_until            = NULL,
    updated_at             = @now
WHERE org_id = @org_id AND id = @id AND lease_owner = @owner::text
RETURNING id;

-- MarkPossibleDuplicate records that a Publication starts again after an earlier start that was never recorded as
-- taken (C-11.FR-12).
-- name: MarkPossibleDuplicate :exec
UPDATE deliveries
SET possible_duplicate = true, updated_at = @now
WHERE org_id = @org_id AND id = @id AND lease_owner = @owner::text;

-- GetLatestRepublishedAt is when the Root message of an Alert Group in a Destination was last found deleted, the time
-- the note of its republication shows; the zero time when it never was.
-- name: GetLatestRepublishedAt :one
SELECT coalesce(max(occurred_at), '0001-01-01 00:00:00+00')::timestamptz AS at
FROM delivery_events
WHERE org_id = @org_id AND alert_group_id = @alert_group_id::bigint AND destination_id = @destination_id
  AND kind = 'republished';

-- ResetForRepublish makes a delivery whose Root message was deleted in the messenger publish again, Quietly and once
-- (C-11.FR-13): the actual message and its Thread are forgotten, and its pending Thread replies wait for the new Root
-- message. A delivery republished once already, or ended, is left alone.
-- name: ResetForRepublish :execrows
UPDATE deliveries
SET message_id               = NULL,
    message_url              = NULL,
    actual_version           = NULL,
    actual_hash              = NULL,
    publication_started_at   = NULL,
    thread_state             = 'none',
    thread_anchor_id         = NULL,
    thread_chain_last_id     = NULL,
    republished_after_delete = true,
    publication_loud         = false,
    late_note                = false,
    state                    = 'pending',
    attempts                 = 0,
    first_failed_at          = NULL,
    last_error_class         = NULL,
    last_error               = NULL,
    next_attempt_at          = @now,
    lease_owner              = NULL,
    lease_until              = NULL,
    updated_at               = @now
WHERE org_id = @org_id AND id = @id AND NOT republished_after_delete
  AND state NOT IN ('withheld', 'deleted_in_messenger', 'retired');

-- MarkDeletedInMessenger leaves a delivery whose Root message was deleted in the messenger alone for good.
-- name: MarkDeletedInMessenger :execrows
UPDATE deliveries
SET state = 'deleted_in_messenger', lease_owner = NULL, lease_until = NULL, updated_at = @now
WHERE org_id = @org_id AND id = @id AND state NOT IN ('withheld', 'deleted_in_messenger', 'retired');

-- DropPendingReplies drops the Thread replies of a delivery that are still to be sent.
-- name: DropPendingReplies :exec
UPDATE thread_replies
SET state = 'dropped', lease_owner = NULL, lease_until = NULL
WHERE org_id = @org_id AND delivery_id = @delivery_id AND state IN ('collecting', 'pending');

-- SettleResolvedPublication decides the first Publication of a resolved Alert Group that is still pending (C-11.FR-11,
-- FR-19): withheld while its Destination is Broken, so that nothing waits for it; Quiet with the late note when it
-- waited for a RetryAfter or Transient retries; unchanged otherwise, also when no error was ever recorded
-- (last_error_class is null, which coalesce turns into false rather than a null late_note). Storm summaries and held
-- deliveries are not touched. A lease stays: the record of a call in flight, such as a probe's, then follows the rule
-- of RecordDelivered for a row that ended meanwhile.
-- name: SettleResolvedPublication :exec
UPDATE deliveries d
SET state            = CASE WHEN ds.health = 'broken' THEN 'withheld' ELSE d.state END,
    late_note        = d.late_note
                       OR coalesce(ds.health = 'healthy' AND d.last_error_class IN ('retry_after', 'transient'), false),
    publication_loud = CASE
                           WHEN ds.health = 'healthy' AND d.last_error_class IN ('retry_after', 'transient') THEN false
                           ELSE d.publication_loud
                       END,
    updated_at       = @now
FROM destinations ds
WHERE d.org_id = @org_id AND d.id = @id AND ds.org_id = @org_id AND ds.id = d.destination_id
  AND d.state = 'pending' AND d.message_id IS NULL AND d.held_by_storm_id IS NULL AND d.storm_id IS NULL;

-- EnsureBuckets creates the limiter buckets of a Destination and of its Connection, full, on first use.
-- name: EnsureBuckets :exec
INSERT INTO rate_limit_buckets (subject_kind, subject_id, org_id, tokens, refilled_at)
SELECT s.kind, s.id, @org_id, s.cap, @now
FROM (SELECT 'destination'::text AS kind, d.id, d.limiter_limit::float8 AS cap
      FROM destinations d
      WHERE d.org_id = @org_id AND d.id = sqlc.narg('destination_id')::bigint
      UNION ALL
      SELECT 'connection'::text, c.id, c.limiter_limit::float8
      FROM connections c
      WHERE c.org_id = @org_id AND c.id = sqlc.narg('connection_id')::bigint) AS s
ON CONFLICT (subject_kind, subject_id) DO NOTHING;

-- TakeTokens takes one token from each bucket of a Destination and its Connection, or from none: each bucket refills
-- at limit / per_seconds up to limit since refilled_at, which may lie in the future after a RetryAfter. It locks the
-- buckets in one order, so that two takers never wait for each other. taken says whether the tokens were taken; due is
-- when every bucket that has none will hold one, or the zero time.
-- name: TakeTokens :one
WITH clock AS (
    SELECT @now::timestamptz AS now
), want AS (
    SELECT 'destination'::text AS kind, d.id, d.limiter_limit::float8 AS cap,
           d.limiter_limit::float8 / d.limiter_per_seconds::float8 AS rate
    FROM destinations d
    WHERE d.org_id = @org_id AND d.id = sqlc.narg('destination_id')::bigint
    UNION ALL
    SELECT 'connection'::text, c.id, c.limiter_limit::float8, c.limiter_limit::float8 / c.limiter_per_seconds::float8
    FROM connections c
    WHERE c.org_id = @org_id AND c.id = sqlc.narg('connection_id')::bigint
), cur AS (
    SELECT b.subject_kind, b.subject_id, w.rate,
           least(w.cap, b.tokens + extract(epoch FROM (k.now - b.refilled_at))::float8 * w.rate) AS avail
    FROM rate_limit_buckets b
    JOIN want w ON w.kind = b.subject_kind AND w.id = b.subject_id
    CROSS JOIN clock k
    WHERE b.org_id = @org_id
    ORDER BY b.subject_kind DESC, b.subject_id
    FOR UPDATE OF b
), verdict AS (
    SELECT count(*) = (SELECT count(*) FROM want) AND coalesce(bool_and(avail >= 1), false) AS ok
    FROM cur
), took AS (
    UPDATE rate_limit_buckets b
    SET tokens = c.avail - 1, refilled_at = k.now
    FROM cur c, verdict v, clock k
    WHERE b.org_id = @org_id AND b.subject_kind = c.subject_kind AND b.subject_id = c.subject_id AND v.ok
    RETURNING b.subject_id
)
SELECT v.ok::boolean AS taken,
       coalesce((SELECT max(k.now + ((1 - c.avail) / c.rate) * interval '1 second')
                 FROM cur c
                 CROSS JOIN clock k
                 WHERE NOT v.ok AND c.avail < 1),
                '0001-01-01 00:00:00+00')::timestamptz AS due
FROM verdict v;

-- HoldBucket empties a bucket until @until after a RetryAfter: no token before then, and one token at that time; a
-- longer hold already in place stays.
-- name: HoldBucket :exec
UPDATE rate_limit_buckets
SET tokens = 1, refilled_at = @until
WHERE org_id = @org_id AND subject_kind = @subject_kind AND subject_id = @subject_id AND refilled_at < @until;

-- ClaimDueReplies leases the due Thread replies whose delivery has a Root message, of healthy Destinations, one
-- delivery's replies in id order: a reply waits while an earlier one of its delivery is pending. A collecting batch
-- that comes due closes: it becomes pending, and new Alerts start the next batch. The choice is a materialized CTE,
-- run once, as in ClaimDueDeliveries: planned as the inner side of a nested loop over every reply, it ran once per row
-- and took seconds once replies had piled up.
-- name: ClaimDueReplies :many
WITH due AS MATERIALIZED (
    SELECT x.id
    FROM thread_replies x
    JOIN deliveries d ON d.org_id = x.org_id AND d.id = x.delivery_id
    JOIN destinations ds ON ds.org_id = x.org_id AND ds.id = x.destination_id
    WHERE x.org_id = @org_id AND x.state IN ('collecting', 'pending') AND x.next_attempt_at <= @due::timestamptz
      AND (x.lease_until IS NULL OR x.lease_until <= @now::timestamptz) AND d.message_id IS NOT NULL
      AND ds.health = 'healthy'
      AND NOT EXISTS (SELECT 1
                      FROM thread_replies p
                      WHERE p.org_id = x.org_id AND p.delivery_id = x.delivery_id AND p.state = 'pending'
                        AND p.id < x.id)
    ORDER BY x.next_attempt_at, x.id
    LIMIT @lim
    FOR UPDATE OF x SKIP LOCKED
)
UPDATE thread_replies r
SET lease_owner = @owner::text, lease_until = @lease_until::timestamptz, state = 'pending'
FROM due
WHERE r.org_id = @org_id AND r.id = due.id
RETURNING r.id, r.next_attempt_at;

-- GetLeasedReply reads, and locks, a Thread reply whose lease this replica still holds at the real time now, with its
-- delivery's Root message, its Destination, its Alert Group and the language of its Route.
-- name: GetLeasedReply :one
SELECT r.id, r.delivery_id, r.alert_group_id, r.event, r.event_seqs, r.loudness, r.mentions, r.fingerprints,
       r.attempts, d.message_id, d.thread_anchor_id, d.thread_chain_last_id, d.republished_after_delete,
       ds.id AS destination_id, ds.public_id AS destination_public_id, ds.name AS destination_name,
       ds.type AS destination_type, ds.connection_id,
       g.public_id AS alert_group_public_id, g.number, g.title, g.status, g.urgent,
       rt.language
FROM thread_replies r
JOIN deliveries d ON d.org_id = r.org_id AND d.id = r.delivery_id
JOIN destinations ds ON ds.org_id = r.org_id AND ds.id = r.destination_id
JOIN alert_groups g ON g.org_id = r.org_id AND g.id = r.alert_group_id
JOIN routes rt ON rt.org_id = r.org_id AND rt.id = g.route_id
WHERE r.org_id = @org_id AND r.id = @id AND r.lease_owner = @owner::text AND r.lease_until > @now::timestamptz
FOR UPDATE OF r;

-- RenewReplyLease extends the lease of a Thread reply this replica holds before its call.
-- name: RenewReplyLease :exec
UPDATE thread_replies
SET lease_until = @lease_until::timestamptz
WHERE org_id = @org_id AND id = @id AND lease_owner = @owner::text;

-- RescheduleReply releases a Thread reply that waits for its limiter tokens until @at.
-- name: RescheduleReply :exec
UPDATE thread_replies
SET next_attempt_at = @at, lease_owner = NULL, lease_until = NULL
WHERE org_id = @org_id AND id = @id AND lease_owner = @owner::text;

-- RecordReplySent records a Thread reply the messenger accepted.
-- name: RecordReplySent :exec
UPDATE thread_replies
SET state = 'sent', sent_at = @now::timestamptz, message_id = sqlc.narg('message_id')::text, last_error_class = NULL,
    last_error = NULL, lease_owner = NULL, lease_until = NULL
WHERE org_id = @org_id AND id = @id AND lease_owner = @owner::text;

-- RecordReplyRetry records an outcome of a Thread reply that is retried at @at; a Transient error is @counted against
-- the budget. No row when the lease went to another replica.
-- name: RecordReplyRetry :one
UPDATE thread_replies
SET next_attempt_at  = @at,
    attempts         = attempts + CASE WHEN @counted::boolean THEN 1 ELSE 0 END,
    first_failed_at  = CASE WHEN @counted::boolean THEN coalesce(first_failed_at, @now::timestamptz) ELSE first_failed_at END,
    last_error_class = sqlc.narg('error_class')::text,
    last_error       = sqlc.narg('error')::text,
    lease_owner      = NULL,
    lease_until      = NULL
WHERE org_id = @org_id AND id = @id AND lease_owner = @owner::text
RETURNING attempts, first_failed_at;

-- RecordReplyNotDelivered ends a Thread reply as Not delivered with the error class and the masked error. No row when
-- the lease went to another replica.
-- name: RecordReplyNotDelivered :one
UPDATE thread_replies
SET state = 'not_delivered', last_error_class = @error_class::text, last_error = @error::text, lease_owner = NULL,
    lease_until = NULL
WHERE org_id = @org_id AND id = @id AND lease_owner = @owner::text
RETURNING id;

-- NextDeliveryWork is when the next delivery, Thread reply or Broken probe can be claimed: the earliest due time of
-- free work on the business clock, and the earliest end of a lease still held on the real clock; each the zero time
-- when there is none. A Broken Destination marked for its next due delivery wakes the worker when that one is due;
-- deliveries a Storm holds, and those of a deleted Destination other than its final edits, are not work.
-- name: NextDeliveryWork :one
WITH due AS (
    SELECT x.next_attempt_at AS at, x.lease_until
    FROM deliveries x
    JOIN destinations ds ON ds.org_id = x.org_id AND ds.id = x.destination_id
    WHERE x.org_id = @org_id AND x.state = 'pending' AND (ds.health = 'healthy' OR ds.next_probe_at = 'infinity')
      AND x.held_by_storm_id IS NULL AND (ds.deleted_at IS NULL OR x.desired_retire)
    UNION ALL
    SELECT b.next_probe_at, NULL::timestamptz
    FROM destinations b
    WHERE b.org_id = @org_id AND b.health = 'broken' AND b.next_probe_at <> 'infinity'
      AND (b.deleted_at IS NULL
           OR EXISTS (SELECT 1
                      FROM deliveries w
                      WHERE w.org_id = b.org_id AND w.destination_id = b.id AND w.state = 'pending'))
    UNION ALL
    SELECT y.next_attempt_at, y.lease_until
    FROM thread_replies y
    JOIN deliveries d ON d.org_id = y.org_id AND d.id = y.delivery_id
    JOIN destinations ds ON ds.org_id = y.org_id AND ds.id = y.destination_id
    WHERE y.org_id = @org_id AND y.state IN ('collecting', 'pending') AND d.message_id IS NOT NULL
      AND ds.health = 'healthy'
      AND NOT EXISTS (SELECT 1
                      FROM thread_replies p
                      WHERE p.org_id = y.org_id AND p.delivery_id = y.delivery_id AND p.state = 'pending'
                        AND p.id < y.id)
)
SELECT coalesce(min(at) FILTER (WHERE lease_until IS NULL OR lease_until <= @now::timestamptz),
                '0001-01-01 00:00:00+00')::timestamptz AS free_at,
       coalesce(min(lease_until) FILTER (WHERE lease_until > @now::timestamptz),
                '0001-01-01 00:00:00+00')::timestamptz AS lease_end
FROM due;

-- InsertDeliveryEvent records what happened while delivering to a Destination (C-11.FR-21).
-- name: InsertDeliveryEvent :exec
INSERT INTO delivery_events (org_id, public_id, occurred_at, destination_id, alert_group_id, storm_id, kind, loudness,
                             mentions, error_class, error, detail)
VALUES (@org_id, @public_id, @occurred_at, @destination_id, sqlc.narg('alert_group_id')::bigint,
        sqlc.narg('storm_id')::bigint, @kind, @loudness, @mentions::text[], sqlc.narg('error_class')::text,
        sqlc.narg('error')::text, sqlc.narg('detail')::jsonb);

-- GetGroupForDeliveries reads the id of an Alert Group by public_id.
-- name: GetGroupForDeliveries :one
SELECT id
FROM alert_groups
WHERE org_id = @org_id AND public_id = @public_id;

-- ListGroupDeliveries reads the delivery state of an Alert Group per Destination, by Destination name.
-- name: ListGroupDeliveries :many
SELECT d.state, d.thread_state, d.possible_duplicate, d.message_url, d.last_error, d.updated_at,
       ds.public_id AS destination_public_id, ds.name AS destination_name, ds.type AS destination_type, ds.health,
       ds.broken_since, ds.broken_reason
FROM deliveries d
JOIN destinations ds ON ds.org_id = d.org_id AND ds.id = d.destination_id
WHERE d.org_id = @org_id AND d.alert_group_id = @alert_group_id::bigint
ORDER BY ds.name, ds.id;

-- ClaimBrokenProbes claims the Broken Destinations whose probe is due (C-11.FR-9, schema.md §5): next_probe_at has
-- passed, or it is marked 'infinity' and one of its deliveries came due. Moving next_probe_at to @next_probe is the
-- lease; another claimer skips the rows this one locked. A deleted Destination is probed only while a final edit of
-- it waits.
-- name: ClaimBrokenProbes :many
WITH due AS MATERIALIZED (
    SELECT x.id
    FROM destinations x
    WHERE x.org_id = @org_id AND x.health = 'broken'
      AND (x.deleted_at IS NULL
           OR EXISTS (SELECT 1
                      FROM deliveries p
                      WHERE p.org_id = x.org_id AND p.destination_id = x.id AND p.state = 'pending'))
      AND (x.next_probe_at <= @due::timestamptz
           OR (x.next_probe_at = 'infinity'
               AND EXISTS (SELECT 1
                           FROM deliveries w
                           WHERE w.org_id = x.org_id AND w.destination_id = x.id AND w.state = 'pending'
                             AND w.held_by_storm_id IS NULL AND w.next_attempt_at <= @due::timestamptz
                             AND (w.lease_until IS NULL OR w.lease_until <= @now::timestamptz))))
    ORDER BY x.next_probe_at, x.id
    LIMIT @lim
    FOR UPDATE OF x SKIP LOCKED
)
UPDATE destinations ds
SET next_probe_at = @next_probe::timestamptz
FROM due
WHERE ds.org_id = @org_id AND ds.id = due.id
RETURNING ds.id, ds.public_id, ds.name, ds.type, ds.connection_id;

-- LeaseOldestWaiting is the probe's delivery: the oldest pending delivery of a Destination that no Storm holds, leased
-- to this replica when its lease is free. No row when nothing waits; free is false when it is leased elsewhere. A
-- first Publication of an Alert Group it leases follows the recovery rule: Loud only when the Alert Group is firing at
-- that moment, and without the late note (C-11.FR-19).
-- name: LeaseOldestWaiting :one
WITH oldest AS (
    SELECT x.id, (x.lease_until IS NULL OR x.lease_until <= @now::timestamptz) AS free
    FROM deliveries x
    WHERE x.org_id = @org_id AND x.destination_id = @destination_id AND x.state = 'pending'
      AND x.held_by_storm_id IS NULL
    ORDER BY x.next_attempt_at, x.id
    LIMIT 1
    FOR UPDATE OF x
), leased AS (
    UPDATE deliveries d
    SET lease_owner      = @owner::text,
        lease_until      = @lease_until::timestamptz,
        publication_loud = CASE
                               WHEN d.message_id IS NULL AND d.alert_group_id IS NOT NULL
                                   THEN coalesce((SELECT g.status = 'firing'
                                                  FROM alert_groups g
                                                  WHERE g.org_id = d.org_id AND g.id = d.alert_group_id), false)
                               ELSE d.publication_loud
                           END,
        late_note        = CASE WHEN d.message_id IS NULL THEN false ELSE d.late_note END
    FROM oldest o
    WHERE d.org_id = @org_id AND d.id = o.id AND o.free
    RETURNING d.id
)
SELECT o.id, o.free::boolean AS free
FROM oldest o;

-- MarkProbeOnNextDelivery marks a Broken Destination whose type has no Destination check: its next delivery that
-- comes due is the probe and is attempted at once.
-- name: MarkProbeOnNextDelivery :exec
UPDATE destinations
SET next_probe_at = 'infinity'
WHERE org_id = @org_id AND id = @id AND health = 'broken';

-- BreakDestination makes a healthy Destination Broken (C-11.FR-9, FR-18), probed first at @next_probe. No row when it
-- is Broken already.
-- name: BreakDestination :one
UPDATE destinations
SET health = 'broken', broken_since = @now::timestamptz, broken_cause = @cause::text, broken_reason = @reason::text,
    next_probe_at = @next_probe::timestamptz
WHERE org_id = @org_id AND id = @id AND health = 'healthy'
RETURNING public_id, name;

-- UpdateBrokenReason keeps a Broken Destination Broken with the error of its last failure as the reason, and its cause
-- when one is given.
-- name: UpdateBrokenReason :exec
UPDATE destinations
SET broken_reason = @reason::text, broken_cause = coalesce(sqlc.narg('cause')::text, broken_cause)
WHERE org_id = @org_id AND id = @id AND health = 'broken';

-- ResetDestinationBudgets gives the pending deliveries and Thread replies of a recovered Destination a fresh Transient
-- budget. It runs in the recovery, while no other replica works the Destination's rows: the claims skip a Broken
-- Destination, so no transaction of another replica holds them and waits for the Destination's row in return.
-- name: ResetDestinationBudgets :exec
WITH replies AS (
    UPDATE thread_replies r
    SET attempts = 0, first_failed_at = NULL
    WHERE r.org_id = @org_id AND r.destination_id = @destination_id AND r.state IN ('collecting', 'pending')
    RETURNING r.id
)
UPDATE deliveries d
SET attempts = 0, first_failed_at = NULL
WHERE d.org_id = @org_id AND d.destination_id = @destination_id AND d.state = 'pending';

-- MarkDestinationHealthy ends the Broken state of a Destination and tells whether it is deleted. No row when it is
-- healthy already.
-- name: MarkDestinationHealthy :one
UPDATE destinations d
SET health = 'healthy', broken_since = NULL, broken_cause = NULL, broken_reason = NULL, next_probe_at = NULL
FROM (SELECT x.id, x.broken_since
      FROM destinations x
      WHERE x.org_id = @org_id AND x.id = @id AND x.health = 'broken'
      FOR UPDATE OF x) AS old
WHERE d.org_id = @org_id AND d.id = old.id
RETURNING d.public_id, d.name, old.broken_since::timestamptz AS was_broken_since,
          (d.deleted_at IS NOT NULL)::boolean AS deleted;

-- DropDueReplies drops the Thread replies of a recovered Destination that came due while it was Broken (C-11.FR-19).
-- name: DropDueReplies :exec
UPDATE thread_replies
SET state = 'dropped', lease_owner = NULL, lease_until = NULL
WHERE org_id = @org_id AND destination_id = @destination_id AND state IN ('collecting', 'pending')
  AND next_attempt_at <= @now::timestamptz;

-- WithholdResolvedUnpublished withholds, on a recovered Destination, the deliveries never published there whose Alert
-- Group resolved meanwhile (C-11.FR-19).
-- name: WithholdResolvedUnpublished :exec
UPDATE deliveries d
SET state = 'withheld', lease_owner = NULL, lease_until = NULL, updated_at = @now
FROM alert_groups g
WHERE d.org_id = @org_id AND d.destination_id = @destination_id AND d.state = 'pending' AND d.message_id IS NULL
  AND d.held_by_storm_id IS NULL AND g.org_id = @org_id AND g.id = d.alert_group_id AND g.status = 'resolved';

-- RecoverUnpublished makes the open Alert Groups never published on a recovered Destination due now: Loud when it is
-- firing at that moment, Quiet otherwise (C-11.FR-19).
-- name: RecoverUnpublished :exec
UPDATE deliveries d
SET publication_loud = (g.status = 'firing'), late_note = false, next_attempt_at = @now, updated_at = @now
FROM alert_groups g
WHERE d.org_id = @org_id AND d.destination_id = @destination_id AND d.state = 'pending' AND d.message_id IS NULL
  AND d.held_by_storm_id IS NULL AND g.org_id = @org_id AND g.id = d.alert_group_id AND g.status <> 'resolved';

-- RecoverPublished makes the published Root messages of a recovered Destination due now: one edit to the current
-- state each (C-11.FR-19).
-- name: RecoverPublished :exec
UPDATE deliveries
SET next_attempt_at = @now, updated_at = @now
WHERE org_id = @org_id AND destination_id = @destination_id AND state = 'pending' AND message_id IS NOT NULL
  AND held_by_storm_id IS NULL;

-- ListDestinationHealth lists the health of each Destination that is not deleted, for muster_destination_broken.
-- name: ListDestinationHealth :many
SELECT public_id, health
FROM destinations
WHERE org_id = @org_id AND deleted_at IS NULL;

-- CountDeliveryQueues counts, per Destination that is not deleted, its pending deliveries that no Storm holds and its
-- Thread replies due at @now, for muster_delivery_queue.
-- name: CountDeliveryQueues :many
SELECT ds.public_id,
       ((SELECT count(*)
        FROM deliveries d
        WHERE d.org_id = ds.org_id AND d.destination_id = ds.id AND d.state = 'pending'
          AND d.held_by_storm_id IS NULL)::bigint
       + (SELECT count(*)
          FROM thread_replies r
          WHERE r.org_id = ds.org_id AND r.destination_id = ds.id AND r.state IN ('collecting', 'pending')
            AND r.next_attempt_at <= @now::timestamptz))::bigint AS queued
FROM destinations ds
WHERE ds.org_id = @org_id AND ds.deleted_at IS NULL;

-- GetRetentionDetailsDays reads retention.alert_details, in days.
-- name: GetRetentionDetailsDays :one
SELECT retention_alert_details_days
FROM organizations
WHERE id = @org_id;

-- DeleteExpiredReplies deletes at most @batch_size Thread replies that are sent, dropped or not delivered and were
-- created before @cutoff, skipping rows another transaction holds.
-- name: DeleteExpiredReplies :execrows
DELETE FROM thread_replies t
WHERE t.org_id = @org_id AND t.id IN (SELECT r.id
                                  FROM thread_replies r
                                  WHERE r.org_id = @org_id AND r.state IN ('sent', 'dropped', 'not_delivered')
                                    AND r.created_at < @cutoff
                                  ORDER BY r.created_at, r.id
                                  LIMIT @batch_size
                                  FOR UPDATE SKIP LOCKED);

-- Storms (C-11.FR-6; storms, timers kind storm_calm_check, deliveries.storm_id and held_by_storm_id).

-- CountRecentGroups counts the Alert Groups of a Route created after @since, the Storm count of the last minute.
-- name: CountRecentGroups :one
SELECT count(*)::bigint
FROM alert_groups
WHERE org_id = @org_id AND route_id = @route_id AND created_at > @since::timestamptz;

-- JoinStorm adds a new Alert Group to the active Storm of a Route, counting it Urgent when it is. No row when the Route
-- has no active Storm.
-- name: JoinStorm :one
UPDATE storms
SET alert_group_count = alert_group_count + 1,
    urgent_count      = urgent_count + CASE WHEN @urgent::boolean THEN 1 ELSE 0 END
WHERE org_id = @org_id AND route_id = @route_id AND ended_at IS NULL
RETURNING id, started_at, alert_group_count, urgent_count;

-- StartStorm starts the Storm of a Route with the Alert Group that started it. No row when another transaction started
-- one first.
-- name: StartStorm :one
INSERT INTO storms (org_id, route_id, started_at, alert_group_count, urgent_count)
VALUES (@org_id, @route_id, @now, 1, CASE WHEN @urgent::boolean THEN 1 ELSE 0 END)
ON CONFLICT (route_id) WHERE ended_at IS NULL DO NOTHING
RETURNING id, started_at, alert_group_count, urgent_count;

-- SetStormTimer sets the deadline of the calm check of a Storm, free of any lease.
-- name: SetStormTimer :exec
INSERT INTO timers (org_id, storm_id, kind, deadline, created_at, updated_at)
VALUES (@org_id, @storm_id::bigint, 'storm_calm_check', @deadline, @now, @now)
ON CONFLICT (storm_id, kind) WHERE storm_id IS NOT NULL
DO UPDATE SET deadline = excluded.deadline, lease_owner = NULL, lease_until = NULL, attempts = 0,
              updated_at = excluded.updated_at;

-- EnsureStormSummary creates the Storm summary of a Storm in a Destination when it is missing, Loud at its first
-- Publication, and locks it either way.
-- name: EnsureStormSummary :one
INSERT INTO deliveries (org_id, destination_id, storm_id, state, urgent, publication_loud, next_attempt_at, created_at,
                        updated_at)
VALUES (@org_id, @destination_id, @storm_id, 'pending', false, true, @now, @now, @now)
ON CONFLICT (storm_id, destination_id) WHERE storm_id IS NOT NULL
DO UPDATE SET updated_at = deliveries.updated_at
RETURNING id, desired_hash;

-- ListStormSummaries lists the Storm summaries of a Storm, in every Destination it reached.
-- name: ListStormSummaries :many
SELECT id, desired_hash
FROM deliveries
WHERE org_id = @org_id AND storm_id = @storm_id::bigint
ORDER BY id;

-- ReleaseHeld lets an Alert Group a Storm holds be published at once: it became Urgent.
-- name: ReleaseHeld :exec
UPDATE deliveries
SET held_by_storm_id = NULL, next_attempt_at = @now, updated_at = @now
WHERE org_id = @org_id AND id = @id AND held_by_storm_id IS NOT NULL;

-- LockStorm reads, and locks, a Storm that has not ended, with the public_id, name and Storm threshold of its Route.
-- name: LockStorm :one
SELECT s.id, s.route_id, s.started_at, s.calm_since, s.alert_group_count, s.urgent_count, r.public_id AS route_public_id,
       r.name AS route_name, r.language AS route_language, r.storm_threshold
FROM storms s
JOIN routes r ON r.org_id = s.org_id AND r.id = s.route_id
WHERE s.org_id = @org_id AND s.id = @id AND s.ended_at IS NULL
FOR UPDATE OF s;

-- GetStormRankTime is when the @rank-th most recent Alert Group of a Route created after @floor was created: one more
-- than the Storm threshold, so that the count of the last minute fell to the threshold one minute after it.
-- name: GetStormRankTime :one
SELECT x.created_at
FROM (SELECT g.created_at, row_number() OVER (ORDER BY g.created_at DESC, g.id DESC) AS n
      FROM alert_groups g
      WHERE g.org_id = @org_id AND g.route_id = @route_id AND g.created_at > @floor::timestamptz) AS x
WHERE x.n = @rank::bigint;

-- SetStormCalmSince records since when the count of a Storm stayed at or below the threshold, null while it does not.
-- name: SetStormCalmSince :exec
UPDATE storms
SET calm_since = sqlc.narg('calm_since')::timestamptz
WHERE org_id = @org_id AND id = @id;

-- CountHeldOpen counts the open Alert Groups a Storm holds, which its end publishes.
-- name: CountHeldOpen :one
SELECT count(DISTINCT d.alert_group_id)::bigint
FROM deliveries d
JOIN alert_groups g ON g.org_id = d.org_id AND g.id = d.alert_group_id
WHERE d.org_id = @org_id AND d.held_by_storm_id = @storm_id::bigint AND d.state = 'pending'
  AND g.status <> 'resolved';

-- ReleaseHeldOpen publishes the open Alert Groups a Storm held, Loud as @loud, within the limiters (C-11.FR-6).
-- name: ReleaseHeldOpen :exec
UPDATE deliveries d
SET held_by_storm_id = NULL, publication_loud = @loud::boolean, next_attempt_at = @now, updated_at = @now
FROM alert_groups g
WHERE d.org_id = @org_id AND d.held_by_storm_id = @storm_id::bigint AND g.org_id = @org_id
  AND g.id = d.alert_group_id AND g.status <> 'resolved';

-- WithholdHeldResolved withholds the Alert Groups a Storm held that resolved meanwhile: never published.
-- name: WithholdHeldResolved :exec
UPDATE deliveries d
SET held_by_storm_id = NULL, state = 'withheld', updated_at = @now
FROM alert_groups g
WHERE d.org_id = @org_id AND d.held_by_storm_id = @storm_id::bigint AND d.state = 'pending' AND g.org_id = @org_id
  AND g.id = d.alert_group_id AND g.status = 'resolved';

-- QuietStormSummaries makes the Storm summaries of a Storm that were never published Quiet: a summary first published
-- after its Storm ended announces nothing new.
-- name: QuietStormSummaries :exec
UPDATE deliveries
SET publication_loud = false, updated_at = @now
WHERE org_id = @org_id AND storm_id = @storm_id::bigint AND message_id IS NULL;

-- GetActiveStorm reads the active Storm of a Route. No row when there is none.
-- name: GetActiveStorm :one
SELECT id, started_at, alert_group_count, urgent_count
FROM storms
WHERE org_id = @org_id AND route_id = @route_id AND ended_at IS NULL;

-- RetireStormSummary ends the Storm summary of the active Storm of a Route in a Destination that left it: retired when
-- published there, withheld otherwise; it receives nothing more.
-- name: RetireStormSummary :exec
UPDATE deliveries d
SET state = CASE WHEN d.message_id IS NULL THEN 'withheld' ELSE 'retired' END, updated_at = @now
FROM storms s
WHERE d.org_id = @org_id AND d.destination_id = @destination_id AND d.storm_id = s.id AND s.org_id = @org_id
  AND s.route_id = @route_id AND s.ended_at IS NULL AND d.state NOT IN ('withheld', 'deleted_in_messenger', 'retired');

-- EndStorm ends a Storm.
-- name: EndStorm :exec
UPDATE storms
SET ended_at = @now::timestamptz, calm_since = coalesce(calm_since, sqlc.narg('calm_since')::timestamptz)
WHERE org_id = @org_id AND id = @id AND ended_at IS NULL;

-- ListStormActivity lists the Routes that are not deleted with whether a Storm is active, for muster_storm_active.
-- name: ListStormActivity :many
SELECT r.public_id,
       EXISTS (SELECT 1
               FROM storms s
               WHERE s.org_id = r.org_id AND s.route_id = r.id AND s.ended_at IS NULL)::boolean AS active
FROM routes r
WHERE r.org_id = @org_id AND r.deleted_at IS NULL;

-- Destinations joining and leaving Routes, and deleted Destinations (C-11.FR-14, C-09.FR-19, C-11.FR-20).

-- ListOpenGroupsOfRoute lists the open Alert Groups of a Route, as delivery renders them, in id order, with whether the
-- active Storm @storm_id of the Route, started at @storm_started, holds it: not Urgent, and created since the Storm
-- started or held by it in another Destination. held is false when there is no Storm.
-- name: ListOpenGroupsOfRoute :many
SELECT g.id, g.public_id, g.number, g.title, g.status, g.urgent,
       coalesce(NOT g.urgent AND sqlc.narg('storm_id')::bigint IS NOT NULL
                AND (g.created_at >= sqlc.narg('storm_started')::timestamptz
                     OR EXISTS (SELECT 1
                                FROM deliveries h
                                WHERE h.org_id = g.org_id AND h.alert_group_id = g.id
                                  AND h.held_by_storm_id = sqlc.narg('storm_id')::bigint)), false)::boolean AS held
FROM alert_groups g
WHERE g.org_id = @org_id AND g.route_id = @route_id AND g.status <> 'resolved'
ORDER BY g.id;

-- RejoinDelivery makes the delivery of an open Alert Group in a Destination that joins its Route current again: one
-- that left (retired) or was never published there (withheld) is published afresh, Quiet as @loud, held by the active
-- Storm @held_by_storm_id when one holds the Alert Group; one whose final edit still waits keeps its Root message and
-- is edited to the current state instead.
-- name: RejoinDelivery :execrows
UPDATE deliveries
SET message_id               = CASE WHEN state IN ('retired', 'withheld') THEN NULL ELSE message_id END,
    message_url              = CASE WHEN state IN ('retired', 'withheld') THEN NULL ELSE message_url END,
    actual_version           = CASE WHEN state IN ('retired', 'withheld') THEN NULL ELSE actual_version END,
    actual_hash              = CASE WHEN state IN ('retired', 'withheld') THEN NULL ELSE actual_hash END,
    thread_state             = CASE WHEN state IN ('retired', 'withheld') THEN 'none' ELSE thread_state END,
    thread_anchor_id         = CASE WHEN state IN ('retired', 'withheld') THEN NULL ELSE thread_anchor_id END,
    thread_chain_last_id     = CASE WHEN state IN ('retired', 'withheld') THEN NULL ELSE thread_chain_last_id END,
    publication_started_at   = CASE WHEN state IN ('retired', 'withheld') THEN NULL ELSE publication_started_at END,
    publication_loud         = CASE WHEN state IN ('retired', 'withheld') THEN @loud::boolean ELSE publication_loud END,
    republished_after_delete = CASE WHEN state IN ('retired', 'withheld') THEN false ELSE republished_after_delete END,
    late_note                = false,
    desired_retire           = false,
    held_by_storm_id         = CASE WHEN state IN ('retired', 'withheld') THEN sqlc.narg('held_by_storm_id')::bigint END,
    state                    = 'pending',
    next_attempt_at          = @now,
    updated_at               = @now
WHERE org_id = @org_id AND alert_group_id = @alert_group_id::bigint AND destination_id = @destination_id
  AND (state IN ('retired', 'withheld') OR desired_retire);

-- RetireRouteDeliveries gives the deliveries of the open Alert Groups of a Route in a Destination that left it their
-- final edit (C-11.FR-14): a published Root message is edited once more, and one never published there is withheld.
-- name: RetireRouteDeliveries :many
UPDATE deliveries d
SET state            = CASE WHEN d.message_id IS NULL AND d.publication_started_at IS NULL THEN 'withheld' ELSE 'pending' END,
    desired_retire   = NOT (d.message_id IS NULL AND d.publication_started_at IS NULL),
    held_by_storm_id = NULL,
    next_attempt_at  = @now,
    updated_at       = @now
FROM alert_groups g
WHERE d.org_id = @org_id AND d.destination_id = @destination_id AND g.org_id = @org_id AND g.id = d.alert_group_id
  AND g.route_id = @route_id AND g.status <> 'resolved'
  AND d.state NOT IN ('withheld', 'deleted_in_messenger', 'retired')
RETURNING d.id;

-- RetireGroupDeliveries gives the deliveries of an Alert Group in the Destinations other than @keep their final edit:
-- it moved to a Route they are not Destinations of (C-09.FR-19).
-- name: RetireGroupDeliveries :many
UPDATE deliveries
SET state            = CASE WHEN message_id IS NULL AND publication_started_at IS NULL THEN 'withheld' ELSE 'pending' END,
    desired_retire   = NOT (message_id IS NULL AND publication_started_at IS NULL),
    held_by_storm_id = NULL,
    next_attempt_at  = @now,
    updated_at       = @now
WHERE org_id = @org_id AND alert_group_id = @alert_group_id::bigint AND destination_id <> ALL(@keep::bigint[])
  AND state NOT IN ('withheld', 'deleted_in_messenger', 'retired')
RETURNING id;

-- RetireDestinationDeliveries gives the deliveries of the open Alert Groups in a deleted Destination their final edit.
-- name: RetireDestinationDeliveries :many
UPDATE deliveries d
SET state            = CASE WHEN d.message_id IS NULL AND d.publication_started_at IS NULL THEN 'withheld' ELSE 'pending' END,
    desired_retire   = NOT (d.message_id IS NULL AND d.publication_started_at IS NULL),
    held_by_storm_id = NULL,
    next_attempt_at  = @now,
    updated_at       = @now
FROM alert_groups g
WHERE d.org_id = @org_id AND d.destination_id = @destination_id AND g.org_id = @org_id AND g.id = d.alert_group_id
  AND g.status <> 'resolved' AND d.state NOT IN ('withheld', 'deleted_in_messenger', 'retired')
RETURNING d.id;

-- SettleDestinationLeftovers ends the other deliveries of a deleted Destination without a call: the pending ones of
-- resolved Alert Groups, and its Storm summaries, which the end of their Storm would otherwise edit again; retired when
-- published there, withheld otherwise.
-- name: SettleDestinationLeftovers :exec
UPDATE deliveries d
SET state            = CASE WHEN d.message_id IS NULL THEN 'withheld' ELSE 'retired' END,
    desired_retire   = false,
    held_by_storm_id = NULL,
    updated_at       = @now
WHERE d.org_id = @org_id AND d.destination_id = @destination_id AND NOT d.desired_retire
  AND (d.state = 'pending' OR (d.storm_id IS NOT NULL AND d.state NOT IN ('withheld', 'deleted_in_messenger', 'retired')))
  AND NOT EXISTS (SELECT 1
                  FROM alert_groups g
                  WHERE g.org_id = d.org_id AND g.id = d.alert_group_id AND g.status <> 'resolved');

-- DropDeliveryReplies drops the Thread replies still to be sent of deliveries that get their final edit.
-- name: DropDeliveryReplies :exec
UPDATE thread_replies
SET state = 'dropped', lease_owner = NULL, lease_until = NULL
WHERE org_id = @org_id AND delivery_id = ANY(@delivery_ids::bigint[]) AND state IN ('collecting', 'pending');

-- DropDestinationReplies drops the Thread replies still to be sent of a deleted Destination.
-- name: DropDestinationReplies :exec
UPDATE thread_replies
SET state = 'dropped', lease_owner = NULL, lease_until = NULL
WHERE org_id = @org_id AND destination_id = @destination_id AND state IN ('collecting', 'pending');

-- RecordRetired records the final edit of a delivery, or a Root message found deleted at it: the row is retired,
-- keeps the link to its last message and receives nothing more; one withheld or deleted in the messenger while the
-- call was in flight stays so. No row when the lease went to another replica.
-- name: RecordRetired :one
UPDATE deliveries
SET state = CASE WHEN state IN ('withheld', 'deleted_in_messenger') THEN state ELSE 'retired' END, desired_retire = false, attempts = 0, first_failed_at = NULL, last_error_class = NULL,
    last_error = NULL, last_delivered_at = @now::timestamptz, lease_owner = NULL, lease_until = NULL,
    updated_at = @now::timestamptz
WHERE org_id = @org_id AND id = @id AND lease_owner = @owner::text
RETURNING id;

-- WithholdLeased withholds a leased delivery whose Destination left before it was ever published there.
-- name: WithholdLeased :exec
UPDATE deliveries
SET state = 'withheld', desired_retire = false, lease_owner = NULL, lease_until = NULL, updated_at = @now
WHERE org_id = @org_id AND id = @id AND lease_owner = @owner::text;

-- GetDestinationState reads the public_id and health of a Destination, deleted or not.
-- name: GetDestinationState :one
SELECT public_id, health
FROM destinations
WHERE org_id = @org_id AND id = @id;

-- WipeDestinationSecrets wipes the secrets of a deleted Destination once none of its deliveries is pending: the
-- Signing secrets, the proxy password and its named Secrets (C-11.FR-14); the delete of the named Secrets runs whether
-- or not the update reads it. It changes nothing otherwise, and nothing the second time.
-- name: WipeDestinationSecrets :exec
WITH gone AS (
    SELECT x.id
    FROM destinations x
    WHERE x.org_id = @org_id AND x.id = @id AND x.deleted_at IS NOT NULL
      AND NOT EXISTS (SELECT 1
                      FROM deliveries p
                      WHERE p.org_id = x.org_id AND p.destination_id = x.id AND p.state = 'pending')
), secrets AS (
    DELETE FROM destination_secrets s
    USING gone
    WHERE s.org_id = @org_id AND s.destination_id = gone.id
    RETURNING s.destination_id
)
UPDATE destinations d
SET signing_secret_ciphertext          = NULL,
    signing_secret_key_id              = NULL,
    signing_secret_updated_at          = NULL,
    previous_signing_secret_ciphertext = NULL,
    previous_signing_secret_key_id     = NULL,
    previous_signing_secret_since      = NULL,
    proxy_password_ciphertext          = NULL,
    proxy_password_key_id              = NULL,
    proxy_password_updated_at          = NULL
FROM gone
WHERE d.org_id = @org_id AND d.id = gone.id;

-- AbandonConnectionDeliveries ends the deliveries still pending in the deleted Destinations of a deleted Connection as
-- Not delivered with @error (C-11.FR-14), with what the delivery_not_delivered line names: the public_ids of the
-- Destination and of the Alert Group, empty for a Storm summary, whether the call it ends was the final edit, and
-- whether it was a Publication.
-- name: AbandonConnectionDeliveries :many
UPDATE deliveries d
SET state = 'not_delivered', desired_retire = false, last_error_class = 'unknown', last_error = @error::text,
    lease_owner = NULL, lease_until = NULL, updated_at = @now
FROM (SELECT x.id, x.desired_retire, ds.public_id
      FROM deliveries x
      JOIN destinations ds ON ds.org_id = x.org_id AND ds.id = x.destination_id
      WHERE x.org_id = @org_id AND x.state = 'pending' AND ds.connection_id = @connection_id
        AND ds.deleted_at IS NOT NULL
      FOR UPDATE OF x) AS old
WHERE d.org_id = @org_id AND d.id = old.id
RETURNING d.id, d.destination_id, d.alert_group_id, d.storm_id, old.public_id AS destination_public_id,
          old.desired_retire AS final_edit, (d.message_id IS NULL)::boolean AS unpublished,
          coalesce((SELECT g.public_id
                    FROM alert_groups g
                    WHERE g.org_id = d.org_id AND g.id = d.alert_group_id), '')::text AS alert_group_public_id;

-- ListDeletedDestinationsOfConnection lists the deleted Destinations of a Connection.
-- name: ListDeletedDestinationsOfConnection :many
SELECT id
FROM destinations
WHERE org_id = @org_id AND connection_id = @connection_id AND deleted_at IS NOT NULL
ORDER BY id;

-- GetPressBinding reads what a button press on the Root message message_id of the Alert Group group_public_id is bound
-- to (C-13.FR-4): the delivery of that Alert Group to a Mattermost Destination of the Connection that is not deleted
-- and whose Root message is message_id, with the Destination's channel and the Route's language and Snooze durations.
-- name: GetPressBinding :one
SELECT ds.id AS destination_id, ds.public_id AS destination_public_id, ds.name AS destination_name,
       coalesce(ds.mattermost_channel_id, '')::text AS channel_id, r.language, r.snooze_durations_seconds
FROM deliveries d
JOIN destinations ds ON ds.org_id = d.org_id AND ds.id = d.destination_id
JOIN alert_groups g ON g.org_id = d.org_id AND g.id = d.alert_group_id
JOIN routes r ON r.org_id = g.org_id AND r.id = g.route_id
WHERE d.org_id = @org_id AND ds.connection_id = @connection_id AND ds.type = 'mattermost' AND ds.deleted_at IS NULL
  AND g.public_id = @group_public_id AND d.message_id = @message_id::text
ORDER BY d.id
LIMIT 1;

-- GetPostDestination reads the Mattermost Destination of the Connection, not deleted, whose channel is channel_id and
-- to which some delivery posted message_id.
-- name: GetPostDestination :one
SELECT ds.id, ds.public_id, ds.name
FROM destinations ds
WHERE ds.org_id = @org_id AND ds.connection_id = @connection_id AND ds.type = 'mattermost' AND ds.deleted_at IS NULL
  AND ds.mattermost_channel_id = @channel_id::text
  AND EXISTS (SELECT 1 FROM deliveries d
              WHERE d.org_id = @org_id AND d.destination_id = ds.id AND d.message_id = @message_id::text)
ORDER BY ds.id
LIMIT 1;
