-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- The Request log (C-23): one row per request, masked before it is written. A record's owner is its Connection, else
-- its outgoing webhook Destination, else nobody (the checks before saving of new Connections, from S-089); reads show
-- only the records newer than request_log.max_age among the newest request_log.max_records of their owner, and the
-- pruning deletes the rest.

-- name: InsertRecord :exec
INSERT INTO request_log (org_id, public_id, at, direction, operation, method, url, request_headers, request_body,
    request_body_bytes, request_body_truncated, status, response_headers, response_body, response_body_bytes,
    response_body_truncated, latency_ms, via, attempt, error_class, error_code, error, connection_id, destination_id,
    alert_group_id)
VALUES (@org_id, @public_id, @at, @direction, @operation, @method, @url, @request_headers, @request_body,
    @request_body_bytes, @request_body_truncated, sqlc.narg('status'), sqlc.narg('response_headers'),
    sqlc.narg('response_body'), @response_body_bytes, @response_body_truncated, @latency_ms, @via, @attempt,
    sqlc.narg('error_class'), sqlc.narg('error_code'), sqlc.narg('error'), sqlc.narg('connection_id'),
    sqlc.narg('destination_id'), sqlc.narg('alert_group_id'));

-- FindConnection reads the id of a Connection that is not deleted.
-- name: FindConnection :one
SELECT c.id FROM connections c WHERE c.org_id = @org_id AND c.public_id = @public_id AND c.deleted_at IS NULL;

-- FindDestination reads a Destination that is not deleted: its id, its type and its Connection, null for an outgoing
-- webhook.
-- name: FindDestination :one
SELECT d.id, d.type, d.connection_id
FROM destinations d
WHERE d.org_id = @org_id AND d.public_id = @public_id AND d.deleted_at IS NULL;

-- ListConnectionRecords lists, newest first, the records among the newest @max_records of the Connection that are not
-- older than @not_before, after the cursor and filtered — by @destination_id for the view of a messenger Destination.
-- The bounded set is chosen once, in a materialized CTE, so the filters scan at most @max_records rows.
-- name: ListConnectionRecords :many
WITH bounded AS MATERIALIZED (
    SELECT b.id FROM request_log b
    WHERE b.org_id = @org_id AND b.connection_id = @connection_id::bigint AND b.at >= @not_before::timestamptz
    ORDER BY b.at DESC, b.id DESC
    LIMIT @max_records
)
SELECT r.id, r.public_id, r.at, r.direction, r.operation, r.check_kind, r.check_run, r.method, r.url, r.status,
       r.latency_ms, r.via, r.attempt, r.error_class, r.error_code, r.request_body_truncated,
       r.response_body_truncated, c.public_id AS connection_public_id, d.public_id AS destination_public_id,
       d.name AS destination_name, d.type AS destination_type, d.health AS destination_health,
       d.broken_since AS destination_broken_since, d.broken_reason AS destination_broken_reason,
       g.public_id AS alert_group_public_id, g.number AS alert_group_number
FROM bounded
JOIN request_log r ON r.org_id = @org_id AND r.id = bounded.id
LEFT JOIN connections c ON c.org_id = @org_id AND c.id = r.connection_id
LEFT JOIN destinations d ON d.org_id = @org_id AND d.id = r.destination_id
LEFT JOIN alert_groups g ON g.org_id = @org_id AND g.id = r.alert_group_id
WHERE (sqlc.narg('destination_id')::bigint IS NULL OR r.destination_id = sqlc.narg('destination_id')::bigint)
  AND (sqlc.narg('direction')::text IS NULL OR r.direction = sqlc.narg('direction')::text)
  AND (NOT @no_answer::boolean OR r.status IS NULL)
  AND (sqlc.narg('status_from')::integer IS NULL
       OR (r.status >= sqlc.narg('status_from')::integer AND r.status < sqlc.narg('status_to')::integer))
  AND (sqlc.narg('operation')::text IS NULL OR r.operation = sqlc.narg('operation')::text)
  AND (sqlc.narg('alert_group')::text IS NULL OR g.public_id = sqlc.narg('alert_group')::text)
  AND (sqlc.narg('after_at')::timestamptz IS NULL
       OR (r.at, r.id) < (sqlc.narg('after_at')::timestamptz, sqlc.narg('after_id')::bigint))
ORDER BY r.at DESC, r.id DESC
LIMIT @page_size;

-- ListDestinationRecords lists the records of an outgoing webhook Destination as ListConnectionRecords lists those of a
-- Connection.
-- name: ListDestinationRecords :many
WITH bounded AS MATERIALIZED (
    SELECT b.id FROM request_log b
    WHERE b.org_id = @org_id AND b.destination_id = @destination_id::bigint AND b.connection_id IS NULL
      AND b.at >= @not_before::timestamptz
    ORDER BY b.at DESC, b.id DESC
    LIMIT @max_records
)
SELECT r.id, r.public_id, r.at, r.direction, r.operation, r.check_kind, r.check_run, r.method, r.url, r.status,
       r.latency_ms, r.via, r.attempt, r.error_class, r.error_code, r.request_body_truncated,
       r.response_body_truncated, c.public_id AS connection_public_id, d.public_id AS destination_public_id,
       d.name AS destination_name, d.type AS destination_type, d.health AS destination_health,
       d.broken_since AS destination_broken_since, d.broken_reason AS destination_broken_reason,
       g.public_id AS alert_group_public_id, g.number AS alert_group_number
FROM bounded
JOIN request_log r ON r.org_id = @org_id AND r.id = bounded.id
LEFT JOIN connections c ON c.org_id = @org_id AND c.id = r.connection_id
LEFT JOIN destinations d ON d.org_id = @org_id AND d.id = r.destination_id
LEFT JOIN alert_groups g ON g.org_id = @org_id AND g.id = r.alert_group_id
WHERE (sqlc.narg('direction')::text IS NULL OR r.direction = sqlc.narg('direction')::text)
  AND (NOT @no_answer::boolean OR r.status IS NULL)
  AND (sqlc.narg('status_from')::integer IS NULL
       OR (r.status >= sqlc.narg('status_from')::integer AND r.status < sqlc.narg('status_to')::integer))
  AND (sqlc.narg('operation')::text IS NULL OR r.operation = sqlc.narg('operation')::text)
  AND (sqlc.narg('alert_group')::text IS NULL OR g.public_id = sqlc.narg('alert_group')::text)
  AND (sqlc.narg('after_at')::timestamptz IS NULL
       OR (r.at, r.id) < (sqlc.narg('after_at')::timestamptz, sqlc.narg('after_id')::bigint))
ORDER BY r.at DESC, r.id DESC
LIMIT @page_size;

-- GetRecord reads a record that is not older than @not_before, with what its owner's bound and Permission need: the
-- deletion of its Connection and of its Destination.
-- name: GetRecord :one
SELECT r.id, r.public_id, r.at, r.direction, r.operation, r.check_kind, r.check_run, r.method, r.url,
       r.request_headers, r.request_body, r.request_body_bytes, r.request_body_truncated, r.status, r.response_headers,
       r.response_body, r.response_body_bytes, r.response_body_truncated, r.latency_ms, r.via, r.attempt,
       r.error_class, r.error_code, r.error, r.connection_id, r.destination_id,
       c.public_id AS connection_public_id, c.deleted_at AS connection_deleted_at,
       d.public_id AS destination_public_id, d.name AS destination_name, d.type AS destination_type,
       d.health AS destination_health, d.broken_since AS destination_broken_since,
       d.broken_reason AS destination_broken_reason, d.deleted_at AS destination_deleted_at,
       g.public_id AS alert_group_public_id, g.number AS alert_group_number
FROM request_log r
LEFT JOIN connections c ON c.org_id = @org_id AND c.id = r.connection_id
LEFT JOIN destinations d ON d.org_id = @org_id AND d.id = r.destination_id
LEFT JOIN alert_groups g ON g.org_id = @org_id AND g.id = r.alert_group_id
WHERE r.org_id = @org_id AND r.public_id = @public_id AND r.at >= @not_before::timestamptz;

-- CountNewerOfConnection counts, up to @max_records, the records of the Connection newer than (@at, @id): a record
-- with @max_records newer ones is past the count bound.
-- name: CountNewerOfConnection :one
SELECT count(*) FROM (
    SELECT 1 FROM request_log n
    WHERE n.org_id = @org_id AND n.connection_id = @connection_id::bigint
      AND (n.at, n.id) > (@at::timestamptz, @id::bigint)
    LIMIT @max_records
) newer;

-- CountNewerOfDestination counts the newer records of an outgoing webhook Destination as CountNewerOfConnection does.
-- name: CountNewerOfDestination :one
SELECT count(*) FROM (
    SELECT 1 FROM request_log n
    WHERE n.org_id = @org_id AND n.destination_id = @destination_id::bigint AND n.connection_id IS NULL
      AND (n.at, n.id) > (@at::timestamptz, @id::bigint)
    LIMIT @max_records
) newer;

-- CountNewerUnowned counts the newer records without an owner as CountNewerOfConnection does.
-- name: CountNewerUnowned :one
SELECT count(*) FROM (
    SELECT 1 FROM request_log n
    WHERE n.org_id = @org_id AND n.connection_id IS NULL AND n.destination_id IS NULL
      AND (n.at, n.id) > (@at::timestamptz, @id::bigint)
    LIMIT @max_records
) newer;

-- PruneOlder deletes up to @batch_size records older than @before. The batch is chosen once, in a materialized CTE: a
-- subquery that a plan scans again for each deleted row skips the rows the statement already deleted, as locked by
-- itself, and takes more than the batch.
-- name: PruneOlder :execrows
WITH doomed AS MATERIALIZED (
    SELECT o.id FROM request_log o
    WHERE o.org_id = @org_id AND o.at < @before::timestamptz
    ORDER BY o.at
    LIMIT @batch_size
    FOR UPDATE SKIP LOCKED
)
DELETE FROM request_log r
USING doomed d
WHERE r.org_id = @org_id AND r.id = d.id;

-- ListConnectionIDs lists every Connection of the Organization, deleted ones included: each owns its records.
-- name: ListConnectionIDs :many
SELECT c.id FROM connections c WHERE c.org_id = @org_id ORDER BY c.id;

-- ListWebhookDestinationIDs lists every outgoing webhook Destination of the Organization, deleted ones included.
-- name: ListWebhookDestinationIDs :many
SELECT d.id FROM destinations d WHERE d.org_id = @org_id AND d.type = 'webhook' ORDER BY d.id;

-- PruneConnectionExcess deletes up to @batch_size records of the Connection older than the oldest of its newest
-- @max_records (edge), which a run never touches; with fewer records the edge is the oldest one and nothing goes. Both
-- choices are made once, in materialized CTEs, as in PruneOlder.
-- name: PruneConnectionExcess :execrows
WITH edge AS MATERIALIZED (
    SELECT e.at, e.id FROM (
        SELECT n.at, n.id FROM request_log n
        WHERE n.org_id = @org_id AND n.connection_id = @connection_id::bigint
        ORDER BY n.at DESC, n.id DESC
        LIMIT @max_records
    ) e
    ORDER BY e.at, e.id
    LIMIT 1
), doomed AS MATERIALIZED (
    SELECT o.id FROM request_log o, edge
    WHERE o.org_id = @org_id AND o.connection_id = @connection_id::bigint AND (o.at, o.id) < (edge.at, edge.id)
    ORDER BY o.at, o.id
    LIMIT @batch_size
    FOR UPDATE OF o SKIP LOCKED
)
DELETE FROM request_log r
USING doomed d
WHERE r.org_id = @org_id AND r.id = d.id;

-- PruneDestinationExcess deletes the records of an outgoing webhook Destination past the count bound as
-- PruneConnectionExcess does.
-- name: PruneDestinationExcess :execrows
WITH edge AS MATERIALIZED (
    SELECT e.at, e.id FROM (
        SELECT n.at, n.id FROM request_log n
        WHERE n.org_id = @org_id AND n.destination_id = @destination_id::bigint AND n.connection_id IS NULL
        ORDER BY n.at DESC, n.id DESC
        LIMIT @max_records
    ) e
    ORDER BY e.at, e.id
    LIMIT 1
), doomed AS MATERIALIZED (
    SELECT o.id FROM request_log o, edge
    WHERE o.org_id = @org_id AND o.destination_id = @destination_id::bigint AND o.connection_id IS NULL
      AND (o.at, o.id) < (edge.at, edge.id)
    ORDER BY o.at, o.id
    LIMIT @batch_size
    FOR UPDATE OF o SKIP LOCKED
)
DELETE FROM request_log r
USING doomed d
WHERE r.org_id = @org_id AND r.id = d.id;

-- PruneUnownedExcess deletes the records without an owner past the count bound as PruneConnectionExcess does.
-- name: PruneUnownedExcess :execrows
WITH edge AS MATERIALIZED (
    SELECT e.at, e.id FROM (
        SELECT n.at, n.id FROM request_log n
        WHERE n.org_id = @org_id AND n.connection_id IS NULL AND n.destination_id IS NULL
        ORDER BY n.at DESC, n.id DESC
        LIMIT @max_records
    ) e
    ORDER BY e.at, e.id
    LIMIT 1
), doomed AS MATERIALIZED (
    SELECT o.id FROM request_log o, edge
    WHERE o.org_id = @org_id AND o.connection_id IS NULL AND o.destination_id IS NULL
      AND (o.at, o.id) < (edge.at, edge.id)
    ORDER BY o.at, o.id
    LIMIT @batch_size
    FOR UPDATE OF o SKIP LOCKED
)
DELETE FROM request_log r
USING doomed d
WHERE r.org_id = @org_id AND r.id = d.id;
