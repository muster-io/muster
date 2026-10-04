-- Bad: each "want: 1" marks a line where rule 1 must report.

-- name: ListAllAlerts :many
SELECT id FROM alerts; -- want: 1

-- name: ListAlertsOfGroup :many
SELECT id FROM alerts WHERE group_id = $1; -- want: 1

-- name: ListAlertsOr :many
SELECT id FROM alerts WHERE org_id = $1 OR id = $2; -- want: 1

-- name: ListAlertsNotIn :many
SELECT id FROM alerts WHERE org_id NOT IN ($1); -- want: 1

-- name: ListAlertsSelfCompare :many
SELECT id FROM alerts WHERE org_id = org_id; -- want: 1

-- name: ListAlertsComparedWithOwnColumn :many
SELECT id FROM alerts WHERE org_id = group_id; -- want: 1

-- name: ListAlertsJoinUnfiltered :many
SELECT a.id
FROM alerts a
JOIN alert_groups g ON g.id = a.group_id -- want: 1
WHERE a.org_id = @org_id;

-- name: ListAlertsJoinedOnOrgOnly :many
SELECT a.id
FROM alerts a -- want: 1
JOIN alert_groups g ON g.org_id = a.org_id AND g.id = a.group_id; -- want: 1

-- name: ListAlertsAmbiguous :many
SELECT a.id
FROM alerts a -- want: 1
JOIN alert_groups g ON g.id = a.group_id -- want: 1
WHERE org_id = @org_id;

-- name: ListAlertsInAnyGroup :many
SELECT id FROM alerts
WHERE org_id = @org_id AND group_id IN (SELECT id FROM alert_groups); -- want: 1

-- name: ListAlertsViaUnfilteredCTE :many
WITH g AS (SELECT id FROM alert_groups) -- want: 1
SELECT id FROM alerts WHERE org_id = @org_id AND group_id IN (SELECT id FROM g);

-- name: ListAlertsFromUnfilteredSubquery :many
SELECT s.id FROM (SELECT id FROM alerts) AS s; -- want: 1

-- name: ListAlertsAndAllIntegrations :many
SELECT id FROM alerts WHERE org_id = $1
UNION ALL
SELECT id FROM integrations; -- want: 1

-- name: ListSchemaQualified :many
SELECT id FROM public.alerts; -- want: 1

-- name: ListPartition :many
SELECT body FROM events_2026; -- want: 1

-- name: ListAttachedPartition :many
SELECT body FROM events_2027; -- want: 1

-- name: ListDestinations :many
SELECT id FROM destinations; -- want: 1

-- name: ListArchived :many
SELECT id FROM archived_alerts; -- want: 1

-- name: ListRenamed :many
SELECT id FROM drafts_v2; -- want: 1

-- name: SetAlertStateEverywhere :exec
UPDATE alerts SET state = $2 WHERE id = $1; -- want: 1

-- name: ResolveAlertsFromUnfilteredIntegration :exec
UPDATE alerts a SET state = 'resolved'
FROM integrations i -- want: 1
WHERE a.integration_id = i.id AND a.org_id = @org_id;

-- name: DeleteAlert :exec
DELETE FROM alerts WHERE id = $1; -- want: 1

-- name: CreateAlertWithoutOrg :one
INSERT INTO alerts (group_id, integration_id, state) VALUES ($1, $2, 'firing') RETURNING id; -- want: 1

-- name: CreateAlertWithoutColumns :exec
INSERT INTO alerts VALUES ($1, $2, $3, $4, 'firing'); -- want: 1

-- name: CopyAllAlerts :exec
INSERT INTO archived_alerts (id, org_id, group_id, integration_id, state)
SELECT id, org_id, group_id, integration_id, state FROM alerts; -- want: 1

-- name: GetIntegrationWithEmptyExemption :one
-- archlint:org-exempt
SELECT id -- want: 1
FROM integrations WHERE token_hash = $1; -- want: 1

SELECT id FROM alerts WHERE state = 'firing'; -- want: 1
