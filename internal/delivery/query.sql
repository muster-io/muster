-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- Delivery (C-11, ADR-0005, schema.md §4.11 and §5): the Desired state per Alert Group and Destination, its
-- reconciliation by the delivery worker, Thread replies, the shared limiter buckets and the delivery events. Only this
-- package writes deliveries, thread_replies, rate_limit_buckets and delivery_events. Due times are business times and
-- leases real times; both come from Go.

-- ListRouteDestinations lists the Destinations of a Route that are not deleted, in id order, for Enqueue.
-- name: ListRouteDestinations :many
SELECT d.id, d.public_id, d.name, d.type, d.connection_id
FROM route_destinations rd
JOIN destinations d ON d.org_id = rd.org_id AND d.id = rd.destination_id
WHERE rd.org_id = @org_id AND rd.route_id = @route_id AND d.deleted_at IS NULL
ORDER BY d.id;

-- GetRouteDelivery reads what delivery needs of a Route, deleted or not: its language and its Thread batching window.
-- name: GetRouteDelivery :one
SELECT language, thread_batching_window_seconds
FROM routes
WHERE org_id = @org_id AND id = @id;

-- EnsureDelivery creates the delivery of an Alert Group and a Destination when it is missing, and locks it either way;
-- inserted tells a new one.
-- name: EnsureDelivery :one
INSERT INTO deliveries (org_id, destination_id, alert_group_id, state, urgent, publication_loud, next_attempt_at,
                        created_at, updated_at)
VALUES (@org_id, @destination_id, @alert_group_id, 'pending', @urgent, sqlc.narg('publication_loud')::boolean, @now,
        @now, @now)
ON CONFLICT (alert_group_id, destination_id) WHERE alert_group_id IS NOT NULL
DO UPDATE SET updated_at = deliveries.updated_at
RETURNING id, desired_version, desired_hash, thread_batch_until, (xmax = 0)::boolean AS inserted;

-- SetDesired stores a new Desired state: the next version, its text, payload and hash, the receipt time of the oldest
-- Snapshot it carries that is not delivered yet, and makes the delivery due now.
-- name: SetDesired :exec
UPDATE deliveries
SET desired_version     = desired_version + 1,
    desired_text        = @desired_text::text,
    desired_payload     = @desired_payload,
    desired_hash        = @desired_hash,
    desired_received_at = coalesce(desired_received_at, sqlc.narg('received_at')::timestamptz),
    state               = 'pending',
    urgent              = @urgent,
    next_attempt_at     = @now,
    updated_at          = @now
WHERE org_id = @org_id AND id = @id;

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

-- InsertThreadReply queues a Thread reply, due at @due.
-- name: InsertThreadReply :exec
INSERT INTO thread_replies (org_id, delivery_id, alert_group_id, destination_id, event, event_seqs, loudness,
                            mentions, fingerprints, state, next_attempt_at, created_at)
VALUES (@org_id, @delivery_id, @alert_group_id, @destination_id, @event, @event_seqs::bigint[], @loudness,
        @mentions::text[], @fingerprints::text[], 'pending', @due, @now);

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
-- turns; another claimer skips the rows this one locked.
-- name: ClaimDueDeliveries :many
UPDATE deliveries d
SET lease_owner = @owner::text, lease_until = @lease_until::timestamptz
FROM (SELECT x.id
      FROM deliveries x
      JOIN destinations ds ON ds.org_id = x.org_id AND ds.id = x.destination_id
      WHERE x.org_id = @org_id AND x.state = 'pending' AND x.next_attempt_at <= @due::timestamptz
        AND (x.lease_until IS NULL OR x.lease_until <= @now::timestamptz) AND ds.health = 'healthy'
      ORDER BY x.urgent DESC, x.next_attempt_at, x.last_delivered_at NULLS FIRST, x.id
      LIMIT @lim
      FOR UPDATE OF x SKIP LOCKED) AS due
WHERE d.org_id = @org_id AND d.id = due.id
RETURNING d.id, d.urgent, d.next_attempt_at, d.last_delivered_at;

-- GetLeasedDelivery reads, and locks, a delivery whose lease this replica still holds at the real time now: its latest
-- Desired state, its actual message, its Destination and its Alert Group. No row when the lease ran out or went to
-- another replica.
-- name: GetLeasedDelivery :one
SELECT d.id, d.alert_group_id::bigint AS alert_group_id, d.desired_version, d.desired_payload, d.desired_hash,
       d.desired_received_at, d.publication_loud,
       d.actual_hash, d.message_id, d.message_url, d.publication_started_at, d.attempts,
       ds.id AS destination_id, ds.public_id AS destination_public_id, ds.name AS destination_name,
       ds.type AS destination_type, ds.connection_id, g.public_id AS alert_group_public_id, g.number
FROM deliveries d
JOIN destinations ds ON ds.org_id = d.org_id AND ds.id = d.destination_id
JOIN alert_groups g ON g.org_id = d.org_id AND g.id = d.alert_group_id
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
-- unless the Desired state grew meanwhile, in which case it stays pending and the next call carries the newest one.
-- The delivered version clears the receipt time, so a later call never observes a Snapshot already delivered.
-- name: RecordDelivered :one
UPDATE deliveries
SET actual_version      = @version::bigint,
    actual_hash         = @hash,
    message_id          = coalesce(sqlc.narg('message_id')::text, message_id),
    message_url         = coalesce(sqlc.narg('message_url')::text, message_url),
    published_at        = CASE WHEN @published::boolean THEN coalesce(published_at, @now::timestamptz) ELSE published_at END,
    publications        = publications + CASE WHEN @published::boolean THEN 1 ELSE 0 END,
    last_delivered_at   = @now::timestamptz,
    state               = CASE WHEN desired_version = @version::bigint THEN 'delivered' ELSE 'pending' END,
    desired_received_at = NULL,
    next_attempt_at     = @now::timestamptz,
    attempts            = 0,
    first_failed_at     = NULL,
    last_error_class    = NULL,
    last_error          = NULL,
    lease_owner         = NULL,
    lease_until         = NULL,
    updated_at          = @now::timestamptz
WHERE org_id = @org_id AND id = @id AND lease_owner = @owner::text
RETURNING state;

-- RecordDeliveryRetry records an outcome that is retried at @at: a RetryAfter, which leaves attempts untouched, or an
-- outcome that S-035 gives its own rule.
-- name: RecordDeliveryRetry :exec
UPDATE deliveries
SET next_attempt_at  = @at,
    last_error_class = sqlc.narg('error_class')::text,
    last_error       = sqlc.narg('error')::text,
    lease_owner      = NULL,
    lease_until      = NULL,
    updated_at       = @now
WHERE org_id = @org_id AND id = @id AND lease_owner = @owner::text;

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
-- that comes due closes: it becomes pending, and new Alerts start the next batch.
-- name: ClaimDueReplies :many
UPDATE thread_replies r
SET lease_owner = @owner::text, lease_until = @lease_until::timestamptz, state = 'pending'
FROM (SELECT x.id
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
      FOR UPDATE OF x SKIP LOCKED) AS due
WHERE r.org_id = @org_id AND r.id = due.id
RETURNING r.id, r.next_attempt_at;

-- GetLeasedReply reads, and locks, a Thread reply whose lease this replica still holds at the real time now, with its
-- delivery's Root message, its Destination, its Alert Group and the language of its Route.
-- name: GetLeasedReply :one
SELECT r.id, r.delivery_id, r.event, r.event_seqs, r.loudness, r.mentions, r.fingerprints, r.attempts,
       d.message_id, d.thread_anchor_id, d.thread_chain_last_id,
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

-- RecordReplyRetry records an outcome of a Thread reply that is retried at @at.
-- name: RecordReplyRetry :exec
UPDATE thread_replies
SET next_attempt_at  = @at,
    last_error_class = sqlc.narg('error_class')::text,
    last_error       = sqlc.narg('error')::text,
    lease_owner      = NULL,
    lease_until      = NULL
WHERE org_id = @org_id AND id = @id AND lease_owner = @owner::text;

-- NextDeliveryWork is when the next delivery or Thread reply can be claimed: the earliest due time of free work on the
-- business clock, and the earliest end of a lease still held on the real clock; each the zero time when there is none.
-- name: NextDeliveryWork :one
WITH due AS (
    SELECT x.next_attempt_at AS at, x.lease_until
    FROM deliveries x
    JOIN destinations ds ON ds.org_id = x.org_id AND ds.id = x.destination_id
    WHERE x.org_id = @org_id AND x.state = 'pending' AND ds.health = 'healthy'
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
INSERT INTO delivery_events (org_id, public_id, occurred_at, destination_id, alert_group_id, kind, loudness, mentions,
                             error_class, error, detail)
VALUES (@org_id, @public_id, @occurred_at, @destination_id, sqlc.narg('alert_group_id')::bigint, @kind, @loudness,
        @mentions::text[], sqlc.narg('error_class')::text, sqlc.narg('error')::text, sqlc.narg('detail')::jsonb);

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

-- CountDeliveryQueues counts, per Destination that is not deleted, its pending deliveries and its Thread replies due
-- at @now, for muster_delivery_queue.
-- name: CountDeliveryQueues :many
SELECT ds.public_id,
       ((SELECT count(*)
        FROM deliveries d
        WHERE d.org_id = ds.org_id AND d.destination_id = ds.id AND d.state = 'pending')::bigint
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
