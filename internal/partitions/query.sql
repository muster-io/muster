-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- ListPartitions lists the partitions of a partitioned table of the current schema, with whether a DETACH
-- CONCURRENTLY of it was interrupted.
-- name: ListPartitions :many
SELECT c.relname::text AS name, i.inhdetachpending AS detach_pending
FROM pg_catalog.pg_inherits i
JOIN pg_catalog.pg_class c ON c.oid = i.inhrelid
JOIN pg_catalog.pg_class p ON p.oid = i.inhparent
WHERE p.relname = @parent::text
  AND p.relnamespace = (SELECT n.oid FROM pg_catalog.pg_namespace n WHERE n.nspname = current_schema())
ORDER BY c.relname;

-- ListRetention lists the retention periods of every Organization, in days.
-- name: ListRetention :many
SELECT id, retention_stored_snapshots_days, retention_alert_details_days, retention_audit_log_days
FROM organizations
ORDER BY id;

-- ListDetachedPartitions lists the tables of the current schema whose name starts with prefix and that are not
-- partitions: a partition whose detach succeeded and whose drop did not.
-- name: ListDetachedPartitions :many
SELECT c.relname::text AS name
FROM pg_catalog.pg_class c
WHERE c.relkind = 'r'
  AND NOT c.relispartition
  AND starts_with(c.relname::text, @prefix::text)
  AND c.relnamespace = (SELECT n.oid FROM pg_catalog.pg_namespace n WHERE n.nspname = current_schema())
ORDER BY c.relname;
