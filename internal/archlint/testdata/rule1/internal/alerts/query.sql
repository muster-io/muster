-- Good: every reference to a table with org_id is filtered at its own level.

-- name: GetAlert :one
SELECT id, state FROM alerts WHERE org_id = @org_id AND id = @id;

-- name: GetAlertDollar :one
SELECT a.id FROM alerts AS a WHERE a.org_id = $1 AND a.id = $2;

-- name: ListAlertsIn :many
SELECT id FROM alerts WHERE org_id IN (sqlc.arg(org_id)) AND state = sqlc.narg(state);

-- name: ListAlertsAny :many
SELECT id FROM alerts WHERE org_id = ANY(@org_ids::bigint[]) AND id IN (sqlc.slice(ids));

-- name: ListAlertsCast :many
SELECT id FROM alerts WHERE org_id::bigint = $1::bigint;

-- name: ListAlertsReversed :many
SELECT id FROM alerts WHERE @org_id = org_id;

-- name: ListAlertsWithGroups :many
SELECT a.id, g.state
FROM alerts a
JOIN alert_groups g ON g.id = a.group_id AND g.org_id = a.org_id
WHERE a.org_id = @org_id;

-- name: ListAlertsWithGroupsBothFiltered :many
SELECT a.id, g.state
FROM alerts a
JOIN alert_groups g ON g.id = a.group_id AND g.org_id = @org_id
WHERE a.org_id = @org_id;

-- name: ListAlertsJoinedUsing :many
SELECT a.id
FROM alerts a
JOIN alert_groups g USING (org_id)
WHERE a.org_id = @org_id AND g.id = a.group_id;

-- name: ListAlertsCommaJoin :many
SELECT a.id
FROM alerts a, alert_groups g
WHERE a.org_id = @org_id AND g.org_id = a.org_id AND g.id = a.group_id;

-- name: ListRoles :many
SELECT id, name FROM roles;

-- name: ListOrganizations :many
SELECT id FROM organizations;

-- name: ListLegacy :many
SELECT id FROM legacy;

-- name: ListAlertsInGroups :many
SELECT id FROM alerts
WHERE org_id = @org_id AND group_id IN (SELECT id FROM alert_groups WHERE org_id = @org_id);

-- name: ListAlertsInOpenGroups :many
SELECT a.id FROM alerts a
WHERE a.org_id = @org_id
  AND EXISTS (SELECT 1 FROM alert_groups g WHERE g.id = a.group_id AND g.org_id = a.org_id);

-- name: ListAlertsViaCTE :many
WITH alert_groups_open AS (
    SELECT id, org_id FROM alert_groups WHERE org_id = @org_id AND state = 'open'
)
SELECT a.id FROM alerts a JOIN alert_groups_open o ON o.id = a.group_id AND a.org_id = o.org_id;

-- name: ListAlertsShadowedByCTE :many
WITH alerts AS (SELECT 1 AS id)
SELECT id FROM alerts;

-- name: ListAlertsFromSubquery :many
SELECT s.id FROM (SELECT id FROM alerts WHERE org_id = @org_id) AS s;

-- name: ListAlertsAndIntegrations :many
SELECT id FROM alerts WHERE org_id = $1
UNION ALL
SELECT id FROM integrations WHERE org_id = $1;

-- name: ClaimAlerts :many
SELECT id FROM alerts
WHERE org_id = @org_id AND state = 'pending'
ORDER BY id
LIMIT 10
FOR UPDATE SKIP LOCKED;

-- name: CountEvents :one
SELECT count(*) FROM events_2026 WHERE org_id = @org_id;

-- name: CreateAlert :one
INSERT INTO alerts (org_id, group_id, integration_id, state)
VALUES (@org_id, @group_id, @integration_id, 'firing')
RETURNING id;

-- name: CopyAlerts :exec
INSERT INTO archived_alerts (id, org_id, group_id, integration_id, state)
SELECT id, org_id, group_id, integration_id, state FROM alerts WHERE org_id = @org_id;

-- name: SetAlertState :exec
UPDATE alerts SET state = @state WHERE org_id = @org_id AND id = @id;

-- name: ResolveAlertsOfIntegration :exec
UPDATE alerts a SET state = 'resolved'
FROM integrations i
WHERE a.integration_id = i.id AND i.org_id = @org_id AND a.org_id = i.org_id;

-- name: DeleteAlertsOfIntegration :exec
DELETE FROM alerts a USING integrations i
WHERE a.integration_id = i.id AND a.org_id = @org_id AND i.org_id = a.org_id;
