-- SPDX-License-Identifier: AGPL-3.0-only
-- Copyright The Muster Authors

-- Links (C-12.FR-9): Lookup tables with their rows, Link rules with their Matchers, the cells `lookup` reads, the
-- samples of a Link rule's dry run, and the Alert Group whose links getAlertGroup computes on read.

-- ListLookupTables lists a page of the Lookup tables in the order they were created, after the id after_id.
-- name: ListLookupTables :many
SELECT id, public_id, name, description, column_names, created_at, version
FROM lookup_tables
WHERE org_id = @org_id AND (sqlc.narg('after_id')::bigint IS NULL OR id > sqlc.narg('after_id')::bigint)
ORDER BY id
LIMIT @page_size;

-- GetLookupTable reads a Lookup table by public_id.
-- name: GetLookupTable :one
SELECT id, public_id, name, description, column_names, created_at, version
FROM lookup_tables
WHERE org_id = @org_id AND public_id = @public_id;

-- LockLookupTable locks a Lookup table by public_id for a change.
-- name: LockLookupTable :one
SELECT id
FROM lookup_tables
WHERE org_id = @org_id AND public_id = @public_id
FOR UPDATE;

-- ListLookupEntries lists the rows of the Lookup tables, by key.
-- name: ListLookupEntries :many
SELECT lookup_table_id, key, cells
FROM lookup_table_entries
WHERE org_id = @org_id AND lookup_table_id = ANY(@lookup_table_ids::bigint[])
ORDER BY lookup_table_id, key;

-- InsertLookupTable creates a Lookup table; the unique name is checked by its constraint.
-- name: InsertLookupTable :one
INSERT INTO lookup_tables (org_id, public_id, name, description, column_names, created_at, updated_at)
VALUES (@org_id, @public_id, @name, @description, @column_names::text[], @now, @now)
RETURNING id;

-- UpdateLookupTable replaces the fields of a Lookup table and bumps its version.
-- name: UpdateLookupTable :exec
UPDATE lookup_tables
SET name = @name, description = @description, column_names = @column_names::text[], updated_at = @now,
    version = version + 1
WHERE org_id = @org_id AND id = @id;

-- DeleteLookupEntries deletes every row of a Lookup table, which the rows of an update replace.
-- name: DeleteLookupEntries :exec
DELETE FROM lookup_table_entries
WHERE org_id = @org_id AND lookup_table_id = @lookup_table_id;

-- InsertLookupEntries writes rows of a Lookup table: keys[i] with cells[i].
-- name: InsertLookupEntries :exec
INSERT INTO lookup_table_entries (lookup_table_id, key, org_id, cells)
SELECT @lookup_table_id::bigint, unnest(@keys::text[]), @org_id::bigint, unnest(@cells::jsonb[]);

-- DeleteLookupTable deletes a Lookup table with its rows.
-- name: DeleteLookupTable :exec
DELETE FROM lookup_tables
WHERE org_id = @org_id AND id = @id;

-- GetLookupCells reads the cells of one row of the Lookup table named name, for `lookup`.
-- name: GetLookupCells :one
SELECT e.cells
FROM lookup_tables t
JOIN lookup_table_entries e ON e.org_id = t.org_id AND e.lookup_table_id = t.id
WHERE t.org_id = @org_id AND t.name = @name AND e.key = @key;

-- ListLinkRules lists a page of the Link rules in the order they were created, after the id after_id.
-- name: ListLinkRules :many
SELECT id, public_id, name, builtin, scope_type, scope_label, url_template, created_at, version
FROM link_rules
WHERE org_id = @org_id AND (sqlc.narg('after_id')::bigint IS NULL OR id > sqlc.narg('after_id')::bigint)
ORDER BY id
LIMIT @page_size;

-- GetLinkRule reads a Link rule by public_id.
-- name: GetLinkRule :one
SELECT id, public_id, name, builtin, scope_type, scope_label, url_template, created_at, version
FROM link_rules
WHERE org_id = @org_id AND public_id = @public_id;

-- LockLinkRule locks a Link rule by public_id for a change.
-- name: LockLinkRule :one
SELECT id
FROM link_rules
WHERE org_id = @org_id AND public_id = @public_id
FOR UPDATE;

-- ListLinkRuleMatchers lists the Matchers of the Link rules, in their order.
-- name: ListLinkRuleMatchers :many
SELECT link_rule_id, label, op, value
FROM link_rule_matchers
WHERE org_id = @org_id AND link_rule_id = ANY(@link_rule_ids::bigint[])
ORDER BY link_rule_id, position;

-- ListLinkRuleTemplates lists the names and URL templates of every Link rule, for the use of a Lookup table.
-- name: ListLinkRuleTemplates :many
SELECT public_id, name, url_template
FROM link_rules
WHERE org_id = @org_id
ORDER BY id;

-- InsertLinkRule creates a Link rule; the unique name is checked by its constraint.
-- name: InsertLinkRule :one
INSERT INTO link_rules (org_id, public_id, name, scope_type, scope_label, url_template, created_at, updated_at)
VALUES (@org_id, @public_id, @name, @scope_type, sqlc.narg('scope_label'), @url_template, @now, @now)
RETURNING id;

-- UpdateLinkRule replaces the fields of a Link rule and bumps its version.
-- name: UpdateLinkRule :exec
UPDATE link_rules
SET name = @name, scope_type = @scope_type, scope_label = sqlc.narg('scope_label'), url_template = @url_template,
    updated_at = @now, version = version + 1
WHERE org_id = @org_id AND id = @id;

-- DeleteLinkRuleMatchers deletes the Matchers of a Link rule, which an update writes again.
-- name: DeleteLinkRuleMatchers :exec
DELETE FROM link_rule_matchers
WHERE org_id = @org_id AND link_rule_id = @link_rule_id;

-- InsertLinkRuleMatcher writes one Matcher of a Link rule at its position.
-- name: InsertLinkRuleMatcher :exec
INSERT INTO link_rule_matchers (link_rule_id, org_id, position, label, op, value)
VALUES (@link_rule_id, @org_id, @position, @label, @op, @value);

-- DeleteLinkRule deletes a Link rule that is not the built-in one, with its Matchers.
-- name: DeleteLinkRule :exec
DELETE FROM link_rules
WHERE org_id = @org_id AND id = @id AND NOT builtin;

-- EnsureExploreRule creates the built-in "Explore" rule of the Organization unless it has one; it returns no row
-- when the rule exists.
-- name: EnsureExploreRule :one
INSERT INTO link_rules (org_id, public_id, name, builtin, scope_type, url_template, created_at, updated_at)
VALUES (@org_id, @public_id, @name, true, 'alert_group', @url_template, @now, @now)
ON CONFLICT (org_id) WHERE builtin DO NOTHING
RETURNING public_id;

-- GetLinkSnapshotRetention reads retention.stored_snapshots in days.
-- name: GetLinkSnapshotRetention :one
SELECT retention_stored_snapshots_days
FROM organizations
WHERE id = @org_id;

-- ListRecentSnapshots lists the bodies of the most recent processed Alertmanager Stored Snapshots of the Organization
-- within the retention of Stored Snapshots, newest first, for the dry run of a Link rule.
-- name: ListRecentSnapshots :many
SELECT b.body
FROM stored_snapshots s
JOIN snapshot_bodies b ON b.org_id = @org_id AND b.body_sha256 = s.body_sha256 AND b.body_day = s.body_day
WHERE s.org_id = @org_id AND s.source = 'webhook' AND s.state = 'processed'
  AND s.received_at >= @not_before::timestamptz
ORDER BY s.received_at DESC, s.id DESC
LIMIT @lim;

-- GetLinkGroup reads what the links of an Alert Group are computed from: the Alert Group, its Route, the
-- Organization's time zone and its Owner's name.
-- name: GetLinkGroup :one
SELECT g.id, g.public_id, g.number, g.title, g.summary, g.status, g.severity_level, g.urgent, g.group_key_values,
       g.common_labels, g.common_annotations, g.reopen_count, g.created_at, r.public_id AS route_public_id,
       r.name AS route_name, o.time_zone, coalesce(ou.name, '')::text AS owner_name
FROM alert_groups g
JOIN routes r ON r.org_id = g.org_id AND r.id = g.route_id
JOIN organizations o ON o.id = g.org_id
LEFT JOIN users ou ON ou.org_id = g.org_id AND ou.id = g.owner_user_id
WHERE g.org_id = @org_id AND g.id = @id;

-- ListLinkAlerts lists the Alerts of an Alert Group that its messages show, firing first and newest first, at most lim
-- of them.
-- name: ListLinkAlerts :many
SELECT a.fingerprint, a.labels, m.annotations, m.starts_at, m.ended_at, a.generator_url, m.state
FROM alert_group_alerts m
JOIN alerts a ON a.org_id = m.org_id AND a.id = m.alert_id
WHERE m.org_id = @org_id AND m.alert_group_id = @alert_group_id AND m.state IN ('firing', 'resolved')
ORDER BY m.state = 'firing' DESC, m.starts_at DESC, m.id DESC
LIMIT @lim;
